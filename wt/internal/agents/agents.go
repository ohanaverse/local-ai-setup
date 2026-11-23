package agents

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// LaunchCmd describes a fully-built process to exec.
type LaunchCmd struct {
	Bin      string
	Args     []string
	Env      []string // extra env vars, merged over os.Environ() by the caller
	ClearEnv []string // env var names stripped from the inherited environment
	// ClearEnvPrefixes strips every inherited variable whose name starts
	// with one of these (claude's header mode: CLAUDE_CODE_USE_*).
	ClearEnvPrefixes []string
	Warn             string // printed to stderr by Command when non-empty
	// HeaderToken is set only in claude's header mode (a model on the
	// owner's Claude subscription, #597): the gateway token, which command
	// sends as the x-wt-model-token line of ANTHROPIC_CUSTOM_HEADERS. It is
	// never in Args or Env. With it empty, command strips an inherited wt
	// line instead (every agent, every other launch).
	HeaderToken string
	// ModelFallback reports that the driver could not select the requested
	// model and the agent will run on its OWN default model instead (Warn
	// says why). A real launch warns and continues; wt smoke fails the row,
	// because the default model answers the smoke prompt just as well and
	// would otherwise report a model it never exercised as PASS.
	ModelFallback bool
}

// Route is an alias so drivers in this package can refer to config.Route
// unqualified while keeping the type definition in the config package.
type Route = config.Route

// Driver knows how to build a launch command for one agent.
type Driver interface {
	// Build returns the command to run agent for the given model.
	// yolo adds the agent's skip-permissions flag.
	// r carries the resolved route (LitellM or direct provider) for the model.
	Build(m config.Model, yolo bool, r Route) LaunchCmd
	// YoloFlag is the agent's permission-skip flag.
	YoloFlag() string
}

// ArgSetter is an optional Driver capability: some agents (e.g. shell) need
// to receive the user's passthrough args to construct their command. When
// the driver implements ArgSetter, BuildLaunchCmdGateway passes the args before
// calling Build and does not append them again.
type ArgSetter interface {
	SetArgs(args []string)
}

// Commanded is an optional Driver capability: a driver that implements
// it marks itself as a command (no model layer) rather than an agent.
// Drivers that do not implement Commanded default to IsCommand() == false.
// This lets new commands be added by implementing the method, with no
// changes to IsCommand or its callers.
type Commanded interface {
	IsCommand() bool
}

// OneShotRunner is an optional Driver capability for agents that can run a
// single prompt non-interactively and exit. wt smoke appends OneShotArgs'
// return value to the command Build already constructed, to verify a
// model works through this agent without an interactive session.
type OneShotRunner interface {
	OneShotArgs(prompt string) []string
}

// StateDirer is an optional Driver capability for agents that keep state of
// their own (transcripts, memory) in a directory keyed on the working
// directory. wt smoke removes it for the temporary directory a row ran in,
// which would otherwise leave one such directory behind per row.
type StateDirer interface {
	// StateDir returns that directory for the working directory path, which
	// must be the path the agent itself sees (symlinks resolved).
	StateDir(path string) string
}

// InstructionPointer describes a single file created by `wt --init`.
type InstructionPointer struct {
	Path    string // relative to the repo root, e.g. "CLAUDE.md"
	Content string // file body, e.g. "@AGENTS.md\n"
}

// Seeder is an optional Driver capability for agents that need a
// project-level instruction pointer file created by `wt --init`.
type Seeder interface {
	// InstructionPointers returns the pointer files to create. Each pointer
	// is written only if it does not already exist.
	InstructionPointers() []InstructionPointer
}

// AgentListEntry is one row returned by ListEntries. Callers convert it to
// their own list.Item / bubbletea item type.
type AgentListEntry struct {
	Name       string
	Command    bool   // true for commands like shell (no model layer)
	Configured bool   // present in config.toml
	Installed  bool   // binary found on PATH (always true for commands)
	Issue      string // human-readable launch blocker ("" if ready)
}

// ListEntries returns every configured agent and every registered driver
// exactly once, deduplicated and sorted alphabetically by name. Commands are
// classified by IsCommand; non-commands are marked configured/installed and
// receive an issue string if they cannot launch. installed reports whether a
// binary is on PATH; callers pass agents.Installed in production and a stub
// in tests so the result is deterministic regardless of the host's binaries.
func ListEntries(cfg *config.Config, installed func(string) bool) []AgentListEntry {
	seen := map[string]bool{}
	var entries []AgentListEntry

	add := func(name string) {
		if seen[name] {
			return
		}
		seen[name] = true

		_, err := cfg.AgentByName(name)
		configured := err == nil

		command := IsCommand(name)
		entry := AgentListEntry{
			Name:       name,
			Command:    command,
			Configured: configured,
			Installed:  command || installed(name),
			Issue:      IssueFor(cfg, name, installed),
		}

		entries = append(entries, entry)
	}

	for _, a := range cfg.Agents {
		add(a.Name)
	}
	for _, n := range Names() {
		add(n)
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})

	return entries
}

// NotInstalledIssue is the shared "binary not found" message. IssueFor
// returns it, and the TUI's agent picker (internal/tui/agent_picker.go)
// reuses the same constant for its own not-installed row, since it must
// decide that case itself (see the precedence note below) rather than
// trusting IssueFor's string.
const NotInstalledIssue = "not installed — install the binary"

// IssueFor returns the launch blocker for a single agent, or "" if it can
// launch. An agent must be both configured (so it has a model catalog) and
// installed (so its binary can be exec'd). Commands (e.g. shell) are always
// launchable. installed reports whether a binary is on PATH; callers pass
// agents.Installed in production and a stub in tests.
//
// IssueFor is consulted by ListEntries only — it is NOT the source of truth
// for the actual launch-blocking precedence used elsewhere. Both
// cmd/wt-session/main.go's launch gate and the TUI's pinned-agent path
// (proceedFromSelectedPath in internal/tui/app.go) re-implement this
// precedence independently, checking installed BEFORE configured (a pinned
// agent that is neither installed nor configured must report "not
// installed", since there is no binary to launch bare or otherwise either
// way) — the opposite order from the configured-first check below. The
// TUI's agent+command picker (buildAgentList in
// internal/tui/agent_picker.go) also branches on installed/configured
// directly rather than reading this function's return value, for the same
// reason. A change to this precedence must be mirrored in all three places.
func IssueFor(cfg *config.Config, name string, installed func(string) bool) string {
	if IsCommand(name) {
		return ""
	}
	if _, err := cfg.AgentByName(name); err != nil {
		return "not configured — add it to config.toml"
	}
	if !installed(name) {
		return NotInstalledIssue
	}
	return ""
}

// Installed reports whether bin resolves on PATH.
func Installed(bin string) bool {
	_, err := exec.LookPath(bin)
	return err == nil
}

// registry maps agent name -> driver constructor. mu guards concurrent
// access so tests using RegisterTest can safely run under t.Parallel.
var (
	registryMu sync.RWMutex
	registry   = map[string]func() Driver{}
)

func register(name string, f func() Driver) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[name] = f
}

// RegisterTest installs (or removes) a driver under name. It returns a
// cleanup function that restores the previous registration. Passing nil
// as f removes the entry. Tests use this to inject stub drivers without
// exposing the internal register function to other packages.
func RegisterTest(name string, f func() Driver) (cleanup func()) {
	registryMu.Lock()
	prev, hadPrev := registry[name]
	if f == nil {
		delete(registry, name)
	} else {
		registry[name] = f
	}
	registryMu.Unlock()
	return func() {
		registryMu.Lock()
		defer registryMu.Unlock()
		if !hadPrev {
			delete(registry, name)
		} else {
			registry[name] = prev
		}
	}
}

// ByName returns the driver for an agent, or nil if unknown.
func ByName(name string) Driver {
	registryMu.RLock()
	f, ok := registry[name]
	registryMu.RUnlock()
	if !ok {
		return nil
	}
	return f()
}

// Names lists registered agents.
func Names() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]string, 0, len(registry))
	for n := range registry {
		out = append(out, n)
	}
	return out
}

// IsCommand reports whether name is a registered command (no model layer),
// as opposed to an agent. Drivers that implement the Commanded optional
// interface and return true from IsCommand() are commands. Unknown names
// and drivers that do not implement Commanded return false.
func IsCommand(name string) bool {
	d := ByName(name)
	if d == nil {
		return false
	}
	if c, ok := d.(Commanded); ok {
		return c.IsCommand()
	}
	return false
}

// LaunchInfo is what a built launch says about itself beyond the exec.Cmd.
type LaunchInfo struct {
	// ModelFallback mirrors LaunchCmd.ModelFallback: the agent will run on
	// its own default model, not the requested one. Warn is the driver's
	// reason (already printed to stderr by Command).
	ModelFallback bool
	Warn          string
	// HeaderMode reports that the command was built in claude's header mode
	// (#597): the model, a slot model or an alias target uses the owner's
	// Claude subscription, so the agent keeps its own login and the gateway
	// token travels only in ANTHROPIC_CUSTOM_HEADERS. The pre-launch guard
	// reads it.
	HeaderMode bool
}

// launchWithRoute is the part of a launch every route source shares: hand
// ArgSetter drivers their args, build the command from route and append the
// passthrough args (BuildLaunchCmdGateway's body after the route and the
// sync).
func launchWithRoute(d Driver, m config.Model, worktreePath string, yolo bool, route Route, extraArgs []string) (*exec.Cmd, LaunchInfo, error) {
	var info LaunchInfo
	// If the driver consumes args itself (e.g. shell), hand them over and
	// clear the slice so they are not double-appended.
	if as, ok := d.(ArgSetter); ok {
		as.SetArgs(extraArgs)
		extraArgs = nil
	}
	cmd, lc, err := command(d, m, yolo, route, worktreePath)
	if err != nil {
		return nil, info, err
	}
	info = LaunchInfo{ModelFallback: lc.ModelFallback, Warn: lc.Warn, HeaderMode: lc.HeaderToken != ""}
	// Append passthrough args for regular agents.
	if len(extraArgs) > 0 {
		cmd.Args = append(cmd.Args, extraArgs...)
	}
	return cmd, info, nil
}

// command resolves the agent binary and returns an exec.Cmd ready to run in
// workdir, with the driver's extra env merged over the inherited environment
// and ANTHROPIC_CUSTOM_HEADERS settled by launchEnv,
// plus the driver's LaunchCmd, so BuildLaunchCmdGateway can report what the
// driver decided (ModelFallback) alongside the exec.Cmd.
func command(d Driver, m config.Model, yolo bool, r Route, workdir string) (*exec.Cmd, LaunchCmd, error) {
	lc := d.Build(m, yolo, r)
	bin, err := exec.LookPath(lc.Bin)
	if err != nil {
		return nil, lc, fmt.Errorf("agent %s not installed", lc.Bin)
	}
	if lc.Warn != "" {
		fmt.Fprintln(os.Stderr, lc.Warn)
	}
	env, err := launchEnv(os.Environ(), lc.ClearEnv, lc.ClearEnvPrefixes, lc.Env, lc.HeaderToken)
	if err != nil {
		return nil, lc, err
	}
	cmd := exec.Command(bin, lc.Args...)
	cmd.Env = env
	SetDir(cmd, workdir)
	return cmd, lc, nil
}

// SetDir makes dir the working directory cmd runs in, PWD included. cmd's
// environment is inherited from wt, whose PWD names the caller's directory,
// and os/exec only fills PWD in when the environment has none — so an agent
// that trusts PWD over getcwd (opencode does) would open the caller's
// directory instead of dir.
func SetDir(cmd *exec.Cmd, dir string) {
	cmd.Dir = dir
	if dir == "" {
		return
	}
	env := cmd.Env
	if env == nil {
		env = os.Environ()
	}
	cmd.Env = append(filterEnv(env, []string{"PWD"}), "PWD="+dir)
}

// filterEnv returns env with any entry whose key is in clear removed. It is
// used to strip inherited gateway vars (e.g. ANTHROPIC_BASE_URL) before a
// native launch, so the agent uses its own subscription rather than routing
// to a gateway a parent shell happened to export.
func filterEnv(env, clear []string) []string {
	drop := make(map[string]bool, len(clear))
	for _, k := range clear {
		drop[k] = true
	}
	out := make([]string, 0, len(env))
	for _, e := range env {
		key := e
		if i := strings.IndexByte(e, '='); i >= 0 {
			key = e[:i]
		}
		if !drop[key] {
			out = append(out, e)
		}
	}
	return out
}
