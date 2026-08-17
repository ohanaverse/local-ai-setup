package agents

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/ohanaverse/agent-worktree/internal/config"
	"github.com/ohanaverse/agent-worktree/internal/session"
)

// LaunchCmd describes a fully-built process to exec.
type LaunchCmd struct {
	Bin  string
	Args []string
	Env  []string // extra env vars, merged over os.Environ() by the caller
	Warn string   // printed to stderr by Command when non-empty
}

// Driver knows how to build a launch command for one agent.
type Driver interface {
	// Build returns the command to run agent for the given model.
	// yolo adds the agent's skip-permissions flag.
	Build(m config.Model, yolo bool) LaunchCmd
	// YoloFlag is the agent's permission-skip flag.
	YoloFlag() string
}

// Syncer is an optional Driver capability: a pre-launch step that needs the
// full config (e.g. pi syncing its model catalog). Launch paths call it once
// before Build.
type Syncer interface {
	SyncModels(cfg *config.Config) error
}

// ArgSetter is an optional Driver capability: some agents (e.g. shell) need
// to receive the user's passthrough args to construct their command. When
// the driver implements ArgSetter, BuildLaunchCmd passes the args before
// calling Build and does not append them again.
type ArgSetter interface {
	SetArgs(args []string)
}

// Installed reports whether bin resolves on PATH.
func Installed(bin string) bool {
	_, err := exec.LookPath(bin)
	return err == nil
}

// registry maps agent name -> driver constructor.
var registry = map[string]func() Driver{}

func register(name string, f func() Driver) { registry[name] = f }

// ByName returns the driver for an agent, or nil if unknown.
func ByName(name string) Driver {
	f, ok := registry[name]
	if !ok {
		return nil
	}
	return f()
}

// Names lists registered agents.
func Names() []string {
	out := make([]string, 0, len(registry))
	for n := range registry {
		out = append(out, n)
	}
	return out
}

// BuildLaunchCmd resolves the agent driver, runs any pre-launch sync, builds
// the exec.Cmd, appends passthrough args, and adds a resume flag when a prior
// session is provided. It is the single shared launch constructor used by both
// the non-TUI launch path (cmd/wt) and the TUI launch path (internal/tui).
//
// extraArgs are user-supplied args after `--`. For regular agents they are
// appended to the agent's command. For agents implementing ArgSetter (e.g.
// shell), the args are passed via SetArgs and not appended again.
func BuildLaunchCmd(agent string, m config.Model, worktreePath string, yolo bool, sess *session.Session, cfg *config.Config, extraArgs []string) (*exec.Cmd, error) {
	d := ByName(agent)
	if d == nil {
		return nil, fmt.Errorf("unknown agent: %s", agent)
	}
	if s, ok := d.(Syncer); ok {
		if err := s.SyncModels(cfg); err != nil {
			return nil, err
		}
	}
	// If the driver consumes args itself (e.g. shell), hand them over and
	// clear the slice so they are not double-appended.
	if as, ok := d.(ArgSetter); ok {
		as.SetArgs(extraArgs)
		extraArgs = nil
	}
	cmd, err := Command(d, m, yolo, worktreePath)
	if err != nil {
		return nil, err
	}
	// Append passthrough args for regular agents.
	if len(extraArgs) > 0 {
		cmd.Args = append(cmd.Args, extraArgs...)
	}
	if sess != nil {
		switch agent {
		case "claude":
			cmd.Args = append(cmd.Args, "--resume", sess.ID)
		case "opencode":
			cmd.Args = append(cmd.Args, "--session", sess.ID)
		}
	}
	return cmd, nil
}

// Command resolves the agent binary and returns an exec.Cmd ready to run in
// workdir, with the driver's extra env merged over the inherited environment.
func Command(d Driver, m config.Model, yolo bool, workdir string) (*exec.Cmd, error) {
	lc := d.Build(m, yolo)
	bin, err := exec.LookPath(lc.Bin)
	if err != nil {
		return nil, fmt.Errorf("agent %s not installed", lc.Bin)
	}
	if lc.Warn != "" {
		fmt.Fprintln(os.Stderr, lc.Warn)
	}
	cmd := exec.Command(bin, lc.Args...)
	cmd.Dir = workdir
	cmd.Env = append(os.Environ(), lc.Env...)
	return cmd, nil
}
