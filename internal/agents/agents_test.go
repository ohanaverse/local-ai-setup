package agents

import (
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
	want := map[string]bool{"claude": true, "codex": true, "copilot": true, "opencode": true, "pi": true, "agy": true}
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

// Pi passes --model for non-native models and has no yolo flag (the pi CLI
// doesn't support skipping permissions). Native models produce no args.
func TestPi(t *testing.T) {
	d := ByName("pi")
	if d == nil {
		t.Fatal("pi driver not registered")
	}
	if d.YoloFlag() != "" {
		t.Errorf("pi yolo flag = %q, want empty", d.YoloFlag())
	}
	lc := d.Build(cloudModel("deepseek-v4-pro:cloud"), false)
	if len(lc.Args) != 2 || lc.Args[0] != "--model" {
		t.Errorf("args = %v, want [--model ...]", lc.Args)
	}
	if len(d.Build(nativeModel("pi"), false).Args) != 0 {
		t.Errorf("native build should have no args")
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

func hasEnv(env []string, want string) bool {
	for _, e := range env {
		if e == want {
			return true
		}
	}
	return false
}
