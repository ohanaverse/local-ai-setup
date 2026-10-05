package agents

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)

// TestOpenCodeBuildLitellm asserts that in gateway mode opencode routes
// through a wt-declared @ai-sdk/openai-compatible provider pointed at the
// LiteLLM gateway. The builtin "openai" provider cannot be used: opencode
// resolves its models through its own catalog, so an unknown registry id
// errors with "Model not found", and the models.dev-listed openai path uses
// the responses API whose bridged stream opencode cannot map ("text part not
// found"). The explicit models map registers the registry id; small_model is
// pinned to the same gateway model so background summarization (default
// gpt-5-nano) does not query the proxy with nonexistent model names.
func TestOpenCodeBuildLitellm(t *testing.T) {
	m := config.Model{ID: "ollama/qwen3.8:27b-mlx", ModelName: "qwen3.8:27b-mlx", ProviderID: "ollama"}
	gw := config.LitellmState{Enabled: true, URL: "http://localhost:4000", APIKey: "sk-litellm"}
	lc := opencodeDriver{}.Build(m, false, routeFor(m, gw))
	content := envValue(t, lc.Env, "OPENCODE_CONFIG_CONTENT")
	if !strings.Contains(content, `"model":"`+opencodeGatewayProviderID+`/ollama/qwen3.8:27b-mlx"`) {
		t.Errorf("config content = %s, want model %s/ollama/qwen3.8:27b-mlx", content, opencodeGatewayProviderID)
	}
	if !strings.Contains(content, `"small_model":"`+opencodeGatewayProviderID+`/ollama/qwen3.8:27b-mlx"`) {
		t.Errorf("config content = %s, want small_model pinned to the gateway (default gpt-5-nano is unknown to the proxy)", content)
	}
	if !strings.Contains(content, `"npm":"@ai-sdk/openai-compatible"`) {
		t.Errorf("config content = %s, want @ai-sdk/openai-compatible provider (chat wire, no responses bridging)", content)
	}
	if !strings.Contains(content, `"baseURL":"http://localhost:4000/v1"`) {
		t.Errorf("config content = %s, want baseURL http://localhost:4000/v1", content)
	}
	if !strings.Contains(content, `"apiKey":"sk-litellm"`) {
		t.Errorf("config content = %s, want apiKey sk-litellm", content)
	}
	if !strings.Contains(content, `"models":{"ollama/qwen3.8:27b-mlx":{`) {
		t.Errorf("config content = %s, want registry id declared in provider models map (opencode rejects catalog-unknown model ids)", content)
	}
}

// envValue returns the value of key in env, failing the test if absent.
func envValue(t *testing.T, env []string, key string) string {
	t.Helper()
	prefix := key + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return strings.TrimPrefix(e, prefix)
		}
	}
	t.Fatalf("env %s not found in %v", key, env)
	return ""
}

// OpenCode was the one driver whose Build ignored the Native model bit —
// opencode is ollama-only in normal use, so it never received a Native
// model before the unconfigured-agent passthrough sentinel (issue #147).
// Without this guard, a native launch would still emit
// OPENCODE_CONFIG_CONTENT pointing at an empty gateway base URL instead of a
// bare `opencode` command.
func TestOpenCodeNativeBypass(t *testing.T) {
	d := ByName("opencode")
	if d == nil {
		t.Fatal("opencode driver not registered")
	}
	m := nativeModel("opencode")
	lc := d.Build(m, false, directRoute(m))
	if lc.Bin != "opencode" || len(lc.Args) != 0 || len(lc.Env) != 0 {
		t.Errorf("native build = %+v, want bare opencode (no args, no env)", lc)
	}
}

// TestOpenCodeDriverDeclaresEnvAndConfigFileProfileMechanisms verifies
// opencode accepts env and config_file — config_file mechanically merges
// into the OPENCODE_CONFIG_CONTENT env entry (internal/profiles handles
// that dispatch), but from the capability-declaration side it is still
// the config_file mechanism the compaction-tuning profile uses.
func TestOpenCodeDriverDeclaresEnvAndConfigFileProfileMechanisms(t *testing.T) {
	mechs := opencodeDriver{}.ProfileMechanisms()
	want := map[profiles.Mechanism]bool{profiles.MechanismEnv: true, profiles.MechanismConfigFile: true}
	if len(mechs) != len(want) {
		t.Fatalf("ProfileMechanisms() = %v, want exactly %v", mechs, want)
	}
	for _, m := range mechs {
		if !want[m] {
			t.Errorf("unexpected mechanism %q", m)
		}
	}
}

// TestOpenCodeYoloFlagIsAuto pins opencode's skip-permissions flag to the
// literal "--auto=true". opencode 1.18 has no "--dangerously-skip-permissions"
// (passing it printed the usage and exited 1, failing `wt --yolo -A opencode`
// and every opencode `wt smoke` row). The "=true" is load-bearing: "--auto" is
// declared per command, so a bare "--auto" in front of a subcommand
// ("opencode --auto run <prompt>") is parsed as taking "run" as its value and
// the run command is never reached. With its value attached the flag cannot
// swallow what follows, so Build can emit it leading for every argv form.
func TestOpenCodeYoloFlagIsAuto(t *testing.T) {
	d := opencodeDriver{}
	if got := d.YoloFlag(); got != "--auto=true" {
		t.Errorf("YoloFlag() = %q, want --auto=true", got)
	}
	m := config.Model{ID: "ollama/x", ModelName: "x", ProviderID: "ollama"}
	for _, model := range []config.Model{m, {Native: true, ModelName: "native"}} {
		lc := d.Build(model, true, directRoute(model))
		if !slices.Equal(lc.Args, []string{"--auto=true"}) {
			t.Errorf("yolo args (native=%v) = %v, want [--auto=true]", model.Native, lc.Args)
		}
		if lc := d.Build(model, false, directRoute(model)); len(lc.Args) != 0 {
			t.Errorf("non-yolo args (native=%v) = %v, want none", model.Native, lc.Args)
		}
	}
}

// TestOpenCodeYoloPassthroughSubcommand pins the argv of a yolo launch whose
// passthrough args start with a subcommand (`wt --yolo -A opencode -- run
// "do X"`): the flag must carry its own value, so opencode still resolves
// "run". With a bare "--auto" there, opencode printed its root help instead
// of running the prompt.
func TestOpenCodeYoloPassthroughSubcommand(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "opencode"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	cmd, err := BuildLaunchCmd("opencode", config.Model{Native: true, ModelName: "native"}, t.TempDir(), true, nil, []string{"run", "do X"})
	if err != nil {
		t.Fatalf("BuildLaunchCmd: %v", err)
	}
	if got := cmd.Args[1:]; !slices.Equal(got, []string{"--auto=true", "run", "do X"}) {
		t.Errorf("args = %v, want [--auto=true run \"do X\"]", got)
	}
}
