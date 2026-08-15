package agents

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/ohanaverse/agent-worktree/internal/config"
)

// LaunchCmd describes a fully-built process to exec.
type LaunchCmd struct {
	Bin  string
	Args []string
	Env  []string // extra env vars, merged over os.Environ() by the caller
}

// Driver knows how to build a launch command for one agent.
type Driver interface {
	// Build returns the command to run agent for the given model.
	// yolo adds the agent's skip-permissions flag.
	Build(m config.Model, yolo bool) LaunchCmd
	// YoloFlag is the agent's permission-skip flag.
	YoloFlag() string
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

// Command resolves the agent binary and returns an exec.Cmd ready to run in
// workdir, with the driver's extra env merged over the inherited environment.
func Command(d Driver, m config.Model, yolo bool, workdir string) (*exec.Cmd, error) {
	lc := d.Build(m, yolo)
	bin, err := exec.LookPath(lc.Bin)
	if err != nil {
		return nil, fmt.Errorf("agent %s not installed", lc.Bin)
	}
	cmd := exec.Command(bin, lc.Args...)
	cmd.Dir = workdir
	cmd.Env = append(os.Environ(), lc.Env...)
	return cmd, nil
}
