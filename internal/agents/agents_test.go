package agents

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohanaverse/agent-worktree/internal/config"
)

func nativeModel(agent string) config.Model {
	return config.Model{ID: agent + "/native", ModelName: "native", Location: config.LocationCloud}
}

func cloudModel(id string) config.Model {
	return config.Model{ID: id, ModelName: id, Location: config.LocationCloud}
}

func localModel(id string) config.Model {
	return config.Model{ID: id, ModelName: id, Location: config.LocationLocal}
}

// Names must return every agent registered in the package. A mismatch means
// the registry is incomplete and the CLI will be unable to list or launch
// one or more agents.
func TestNames(t *testing.T) {
	names := Names()
	want := map[string]bool{"claude": true, "codex": true, "copilot": true, "opencode": true, "pi": true, "agy": true, "shell": true}
	if len(names) != len(want) {
		t.Fatalf("Names() = %v, want %d agents", names, len(want))
	}
	for _, n := range names {
		if !want[n] {
			t.Errorf("unexpected agent %q", n)
		}
	}
}

// ByName must return nil for an unknown agent name so callers (the CLI, the
// TUI) can distinguish between supported and unsupported agents rather than
// panicking on a nil dereference.
func TestByNameUnknown(t *testing.T) {
	if d := ByName("nope"); d != nil {
		t.Fatalf("ByName(nope) = %v, want nil", d)
	}
}

// The Claude driver handles three cases: native (no args/env), cloud (sets
// the Ollama gateway env var and passes --model), and yolo (prepends the
// skip-permissions flag). Getting any of these wrong means the launched
// claude process receives the wrong CLI flags.
func TestClaude(t *testing.T) {
	d := ByName("claude")
	if d == nil {
		t.Fatal("claude driver not registered")
	}

	// Native — no args/env.
	lc := d.Build(nativeModel("claude"), false)
	if lc.Bin != "claude" || len(lc.Args) != 0 || len(lc.Env) != 0 {
		t.Errorf("native build = %+v, want bare claude", lc)
	}

	// Cloud — env gateway + --model.
	lc = d.Build(cloudModel("deepseek-v4-pro:cloud"), false)
	if len(lc.Args) != 2 || lc.Args[0] != "--model" || lc.Args[1] != "deepseek-v4-pro:cloud" {
		t.Errorf("cloud args = %v, want [--model deepseek-v4-pro:cloud]", lc.Args)
	}
	if !hasEnv(lc.Env, "ANTHROPIC_BASE_URL=http://localhost:11434") {
		t.Errorf("cloud env missing gateway: %v", lc.Env)
	}

	// Yolo flag prepended.
	lc = d.Build(cloudModel("x"), true)
	if len(lc.Args) < 1 || lc.Args[0] != "--dangerously-skip-permissions" {
		t.Errorf("yolo args = %v, want leading --dangerously-skip-permissions", lc.Args)
	}
}

// Codex is a cloud-only agent that passes --model for non-native models and
// nothing for native. It has no custom env vars. Verify both paths.
func TestCodex(t *testing.T) {
	d := ByName("codex")
	if d == nil {
		t.Fatal("codex driver not registered")
	}
	lc := d.Build(cloudModel("deepseek-v4-pro:cloud"), false)
	if len(lc.Args) != 2 || lc.Args[0] != "--model" || lc.Args[1] != "deepseek-v4-pro:cloud" {
		t.Errorf("args = %v, want [--model deepseek-v4-pro:cloud]", lc.Args)
	}
	if len(lc.Env) != 0 {
		t.Errorf("env = %v, want none", lc.Env)
	}
	if d.Build(nativeModel("codex"), false).Args != nil {
		t.Errorf("native build should have no args")
	}
}

// Copilot never receives --model on the CLI. Instead it gets three COPILOT_*
// env vars that tell the VS Code extension which provider and model to use.
// Native models must produce no env vars at all.
func TestCopilot(t *testing.T) {
	d := ByName("copilot")
	if d == nil {
		t.Fatal("copilot driver not registered")
	}
	lc := d.Build(cloudModel("deepseek-v4-pro:cloud"), false)
	if len(lc.Args) != 0 {
		t.Errorf("copilot should not pass --model, got args %v", lc.Args)
	}
	if !hasEnv(lc.Env, "COPILOT_MODEL=deepseek-v4-pro:cloud") {
		t.Errorf("env missing COPILOT_MODEL: %v", lc.Env)
	}
	if !hasEnv(lc.Env, "COPILOT_PROVIDER_BASE_URL=http://localhost:11434") {
		t.Errorf("env missing base url: %v", lc.Env)
	}
	if len(d.Build(nativeModel("copilot"), false).Env) != 0 {
		t.Errorf("native build should have no env")
	}
}

// OpenCode receives its model configuration via a single OPENCODE_CONFIG_CONTENT
// env var containing inline JSON. Native models must not set this var.
func TestOpenCode(t *testing.T) {
	d := ByName("opencode")
	if d == nil {
		t.Fatal("opencode driver not registered")
	}
	lc := d.Build(cloudModel("deepseek-v4-pro:cloud"), false)
	if len(lc.Args) != 0 {
		t.Errorf("opencode should not pass --model, got args %v", lc.Args)
	}
	if len(lc.Env) != 1 || !strings.Contains(lc.Env[0], "OPENCODE_CONFIG_CONTENT=") {
		t.Fatalf("env = %v, want OPENCODE_CONFIG_CONTENT", lc.Env)
	}
	if !strings.Contains(lc.Env[0], `"model":"ollama/deepseek-v4-pro:cloud"`) {
		t.Errorf("config missing model: %s", lc.Env[0])
	}
	if len(d.Build(nativeModel("opencode"), false).Env) != 0 {
		t.Errorf("native build should have no env")
	}
}

// writePiModels points $HOME at a temp dir and writes a models.json there, so
// piDriver.Build (which resolves the real path via os.UserHomeDir) reads the
// test fixture instead of the user's real catalog.
func writePiModels(t *testing.T, content string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".pi", "agent")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "models.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Pi has no yolo flag (the pi CLI doesn't support skipping permissions).
func TestPiYoloFlag(t *testing.T) {
	d := ByName("pi")
	if d == nil {
		t.Fatal("pi driver not registered")
	}
	if d.YoloFlag() != "" {
		t.Errorf("pi yolo flag = %q, want empty", d.YoloFlag())
	}
}

// Pi passes --model <ModelName> only when the model is present in models.json
// and marked _launch: true. Passing --model for an unverified model would
// launch a model pi doesn't intend to use.
func TestPiBuildVerified(t *testing.T) {
	writePiModels(t, `{"providers":{"ollama":{"models":[{"_launch":true,"id":"deepseek-v4-pro:cloud"}]}}}`)
	d := ByName("pi")
	lc := d.Build(config.Model{ID: "ollama/deepseek-v4-pro:cloud", ModelName: "deepseek-v4-pro:cloud"}, false)
	if len(lc.Args) != 2 || lc.Args[0] != "--model" || lc.Args[1] != "deepseek-v4-pro:cloud" {
		t.Errorf("args = %v, want [--model deepseek-v4-pro:cloud]", lc.Args)
	}
	if lc.Warn != "" {
		t.Errorf("warn = %q, want empty for a verified model", lc.Warn)
	}
}

// Pi falls back to its default model (no --model) and sets a warning when the
// model is present but not marked _launch: true.
func TestPiBuildNotVerified(t *testing.T) {
	writePiModels(t, `{"providers":{"ollama":{"models":[{"_launch":false,"id":"deepseek-v4-pro:cloud"}]}}}`)
	d := ByName("pi")
	lc := d.Build(config.Model{ID: "ollama/deepseek-v4-pro:cloud", ModelName: "deepseek-v4-pro:cloud"}, false)
	if len(lc.Args) != 0 {
		t.Errorf("args = %v, want none (fallback to default)", lc.Args)
	}
	if lc.Warn == "" {
		t.Error("warn should be set when falling back")
	}
}

// Pi falls back to its default model when models.json is missing entirely.
func TestPiBuildMissingFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // no models.json created
	d := ByName("pi")
	lc := d.Build(config.Model{ID: "ollama/deepseek-v4-pro:cloud", ModelName: "deepseek-v4-pro:cloud"}, false)
	if len(lc.Args) != 0 {
		t.Errorf("args = %v, want none (fallback to default)", lc.Args)
	}
	if lc.Warn == "" {
		t.Error("warn should be set when models.json is missing")
	}
}

// Pi native models produce no args and no warning.
func TestPiBuildNative(t *testing.T) {
	d := ByName("pi")
	lc := d.Build(nativeModel("pi"), false)
	if len(lc.Args) != 0 || lc.Warn != "" {
		t.Errorf("native build = %+v, want bare pi", lc)
	}
}

// Agy is model-agnostic — the model is chosen inside its TUI. The driver must
// ignore the model entirely and only handle the yolo flag. A broken driver
// could accidentally pass --model to agy, which would crash or misbehave.
func TestAgy(t *testing.T) {
	d := ByName("agy")
	if d == nil {
		t.Fatal("agy driver not registered")
	}
	// Model is ignored entirely.
	lc := d.Build(cloudModel("anything"), false)
	if len(lc.Args) != 0 || len(lc.Env) != 0 {
		t.Errorf("agy build = %+v, want bare agy", lc)
	}
	lc = d.Build(cloudModel("anything"), true)
	if len(lc.Args) != 1 || lc.Args[0] != "--dangerously-skip-permissions" {
		t.Errorf("agy yolo args = %v, want [--dangerously-skip-permissions]", lc.Args)
	}
}

// Installed must return true for binaries on PATH (e.g. git) and false for
// ones that don't exist. This is the gate check the TUI uses to grey out
// unavailable agents.
func TestInstalled(t *testing.T) {
	if !Installed("git") {
		t.Error("expected git to be on PATH")
	}
	if Installed("not-a-real-binary-ever-12345") {
		t.Error("expected unknown binary to not be installed")
	}
}

// Command builds an exec.Cmd with the correct binary, args, working
// directory, and merged environment. This is the final step before the
// tool replaces itself with the agent process.
func TestCommand(t *testing.T) {
	d := ByName("pi")
	m := cloudModel("test-model")
	workdir := "/tmp"

	cmd, err := Command(d, m, false, workdir)
	if err != nil {
		// pi may not be installed; that's fine, just verify the error is clear.
		if !strings.Contains(err.Error(), "not installed") {
			t.Fatalf("unexpected error: %v", err)
		}
		return
	}
	if cmd.Dir != workdir {
		t.Errorf("cmd.Dir = %q, want %q", cmd.Dir, workdir)
	}
	// cmd.Env should start with os.Environ() (inherited env).
	if len(cmd.Env) == 0 {
		t.Error("cmd.Env should include inherited environment")
	}
}

// BuildLaunchCmd must append extraArgs to cmd.Args for regular agents (those
// that do not implement ArgSetter). Failing to append means passthrough args
// like `--verbose` after `--` are silently dropped and never reach the agent.
func TestBuildLaunchCmdAppendsExtraArgs(t *testing.T) {
	// Use a test-only driver backed by bash (always on PATH) that does not
	// implement ArgSetter, exercising the regular-agent append path.
	register("_test_regular", func() Driver { return regularTestDriver{} })
	t.Cleanup(func() { delete(registry, "_test_regular") })

	cmd, err := BuildLaunchCmd("_test_regular", config.Model{}, "/tmp", false, nil, nil, []string{"--foo", "--bar"})
	if err != nil {
		t.Fatalf("BuildLaunchCmd: %v", err)
	}
	args := cmd.Args
	if len(args) < 2 {
		t.Fatalf("cmd.Args = %v, want at least [--foo --bar] appended", args)
	}
	last2 := args[len(args)-2:]
	if last2[0] != "--foo" || last2[1] != "--bar" {
		t.Errorf("cmd.Args suffix = %v, want [--foo --bar]", last2)
	}
}

// BuildLaunchCmd must not double-append extraArgs for agents that implement
// ArgSetter (e.g. shell). The args must be passed to SetArgs only; if they
// were also appended to cmd.Args the shell would receive duplicate arguments,
// causing the command to malform or fail at runtime.
func TestBuildLaunchCmdShellNoDoubleAppend(t *testing.T) {
	cmd, err := BuildLaunchCmd("shell", config.Model{}, "/tmp", false, nil, nil, []string{"echo", "hello"})
	if err != nil {
		t.Fatalf("BuildLaunchCmd: %v", err)
	}
	// The shell driver execs the args directly as argv (no bash -c wrapping):
	// cmd.Args must be exactly [<echo path> hello] — no extra args appended.
	if len(cmd.Args) != 2 {
		t.Fatalf("cmd.Args = %v (len %d), want exactly 2 elements [<echo path> hello]", cmd.Args, len(cmd.Args))
	}
	if !strings.HasSuffix(cmd.Args[0], "echo") {
		t.Errorf("cmd.Args[0] = %q, want it to resolve to echo", cmd.Args[0])
	}
	if cmd.Args[1] != "hello" {
		t.Errorf("cmd.Args[1] = %q, want %q", cmd.Args[1], "hello")
	}
}

// regularTestDriver is a test-only Driver (no ArgSetter) that uses bash as its
// binary so tests always run without requiring a real agent to be installed.
type regularTestDriver struct{}

func (regularTestDriver) Build(_ config.Model, _ bool) LaunchCmd {
	return LaunchCmd{Bin: "bash"}
}
func (regularTestDriver) YoloFlag() string { return "" }

func hasEnv(env []string, want string) bool {
	for _, e := range env {
		if e == want {
			return true
		}
	}
	return false
}

// warnDriver is a test-only Driver whose Build returns a fixed warning, used
// to verify Command surfaces it on stderr.
type warnDriver struct{}

func (warnDriver) Build(m config.Model, yolo bool) LaunchCmd {
	return LaunchCmd{Bin: "true", Warn: "test warning"}
}
func (warnDriver) YoloFlag() string { return "" }

// Command must print a non-empty LaunchCmd.Warn to stderr before returning the
// command. This is how the pi driver tells the user it fell back to pi's
// default model; if the warning is swallowed, the user silently gets a
// different model than they selected.
func TestCommandPrintsWarning(t *testing.T) {
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	_, cmdErr := Command(warnDriver{}, config.Model{}, false, "/tmp")
	w.Close()
	os.Stderr = old
	if cmdErr != nil {
		t.Fatalf("Command: %v", cmdErr)
	}
	out, _ := io.ReadAll(r)
	if !strings.Contains(string(out), "test warning") {
		t.Errorf("stderr = %q, want it to contain %q", string(out), "test warning")
	}
}
