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

func TestByNameUnknown(t *testing.T) {
	if d := ByName("nope"); d != nil {
		t.Fatalf("ByName(nope) = %v, want nil", d)
	}
}

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

func hasEnv(env []string, want string) bool {
	for _, e := range env {
		if e == want {
			return true
		}
	}
	return false
}
