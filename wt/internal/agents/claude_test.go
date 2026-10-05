package agents

import (
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)

func assertEnv(t *testing.T, env []string, key, want string) {
	t.Helper()
	prefix := key + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			got := strings.TrimPrefix(e, prefix)
			if got != want {
				t.Fatalf("env %s: got %q, want %q", key, got, want)
			}
			return
		}
	}
	t.Fatalf("env %s not found in %v", key, env)
}

// assertEnvMissing fails if env contains a value (including an empty value) for key.
func assertEnvMissing(t *testing.T, env []string, key string) {
	t.Helper()
	prefix := key + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			t.Fatalf("env %s unexpectedly set to %q", key, strings.TrimPrefix(e, prefix))
		}
	}
}

// argsFlagValue returns the value immediately following flag in args, or ("", false).
func argsFlagValue(args []string, flag string) (string, bool) {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

// TestClaudeSeeder asserts claudeDriver returns the CLAUDE.md pointer.
func TestClaudeSeeder(t *testing.T) {
	var d Driver = claudeDriver{}
	s, ok := d.(Seeder)
	if !ok {
		t.Fatal("claudeDriver does not implement Seeder")
	}
	ptrs := s.InstructionPointers()
	if len(ptrs) != 1 {
		t.Fatalf("expected 1 pointer, got %d", len(ptrs))
	}
	if ptrs[0].Path != "CLAUDE.md" || ptrs[0].Content != "@AGENTS.md\n" {
		t.Errorf("pointer = %+v, want CLAUDE.md @AGENTS.md", ptrs[0])
	}
}

// TestClaudeBuildLitellm asserts the claude driver routes through the LiteLLM
// gateway with the registry model id and gateway credentials.
func TestClaudeBuildLitellm(t *testing.T) {
	m := config.Model{ID: "ollama/qwen3.8:27b-mlx", ModelName: "qwen3.8:27b-mlx", ProviderID: "ollama"}
	gw := config.LitellmState{Enabled: true, URL: "http://localhost:4000", APIKey: "sk-litellm"}
	lc := claudeDriver{}.Build(m, false, routeFor(m, gw))
	assertEnv(t, lc.Env, "ANTHROPIC_BASE_URL", "http://localhost:4000")
	assertEnv(t, lc.Env, "ANTHROPIC_AUTH_TOKEN", "sk-litellm")
	assertEnv(t, lc.Env, "ANTHROPIC_API_KEY", "")
	got, ok := argsFlagValue(lc.Args, "--model")
	if !ok {
		t.Fatalf("expected --model flag in args, got %v", lc.Args)
	}
	if got != m.ID {
		t.Fatalf("--model value = %q, want %q", got, m.ID)
	}
}

// TestClaudeBuildLitellmYolo asserts gateway routing still works when the yolo
// permission-skip flag is requested: the yolo flag precedes --model in args.
func TestClaudeBuildLitellmYolo(t *testing.T) {
	m := config.Model{ID: "ollama/qwen3.8:27b-mlx", ModelName: "qwen3.8:27b-mlx", ProviderID: "ollama"}
	gw := config.LitellmState{Enabled: true, URL: "http://localhost:4000", APIKey: "sk-litellm"}
	lc := claudeDriver{}.Build(m, true, routeFor(m, gw))
	assertEnv(t, lc.Env, "ANTHROPIC_BASE_URL", "http://localhost:4000")
	assertEnv(t, lc.Env, "ANTHROPIC_AUTH_TOKEN", "sk-litellm")
	assertEnv(t, lc.Env, "ANTHROPIC_API_KEY", "")

	yolo := claudeDriver{}.YoloFlag()
	if len(lc.Args) < 3 {
		t.Fatalf("expected yolo flag + --model pair, got %v", lc.Args)
	}
	if lc.Args[0] != yolo {
		t.Fatalf("first arg = %q, want yolo flag %q", lc.Args[0], yolo)
	}
	got, ok := argsFlagValue(lc.Args, "--model")
	if !ok {
		t.Fatalf("expected --model flag in args, got %v", lc.Args)
	}
	if got != m.ID {
		t.Fatalf("--model value = %q, want %q", got, m.ID)
	}
}

// TestClaudeBuildDirectUsesResolvedAPIKey asserts that in direct mode the
// claude driver authenticates with the route's resolved API key (from
// auth.secret_ref) rather than the hardcoded "ollama" placeholder — a
// registry-driven anthropic-protocol provider other than ollama may need a
// real secret to authenticate.
func TestClaudeBuildDirectUsesResolvedAPIKey(t *testing.T) {
	m := config.Model{ID: "someprovider/x", ModelName: "x", ProviderID: "someprovider"}
	r := config.Route{BaseOrigin: "http://localhost:9999", APIKey: "sk-real-secret", ModelRef: m.ModelName, Litellm: false}
	lc := claudeDriver{}.Build(m, false, r)
	assertEnv(t, lc.Env, "ANTHROPIC_AUTH_TOKEN", "sk-real-secret")
	assertEnv(t, lc.Env, "ANTHROPIC_BASE_URL", "http://localhost:9999")
}

// TestClaudeBuildDirectFallsBackToOllamaPlaceholder asserts that when the
// route resolves no API key (e.g. ollama's auth.type=none provider), the
// driver still sends a non-empty placeholder token — the Anthropic client
// needs some value even when the endpoint doesn't validate it.
func TestClaudeBuildDirectFallsBackToOllamaPlaceholder(t *testing.T) {
	m := config.Model{ID: "ollama/x", ModelName: "x", ProviderID: "ollama"}
	r := config.Route{BaseOrigin: "http://localhost:11434", APIKey: "", ModelRef: m.ModelName, Litellm: false}
	lc := claudeDriver{}.Build(m, false, r)
	assertEnv(t, lc.Env, "ANTHROPIC_AUTH_TOKEN", "ollama")
}

// TestClaudeNativeIgnoresGateway asserts that a native Claude model wins over
// any gateway configuration: gateway env is cleared, no gateway URL or key is
// emitted, and --model uses the bare model name.
func TestClaudeNativeIgnoresGateway(t *testing.T) {
	m := config.Model{
		ID:         "anthropic/claude-3-5-sonnet-latest",
		ModelName:  "claude-3-5-sonnet-latest",
		ProviderID: "anthropic",
		Native:     true,
	}
	gw := config.LitellmState{Enabled: true, URL: "http://localhost:4000", APIKey: "sk-litellm"}
	lc := claudeDriver{}.Build(m, false, routeFor(m, gw))

	wantClear := []string{"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL"}
	for _, key := range wantClear {
		if !slices.Contains(lc.ClearEnv, key) {
			t.Fatalf("expected ClearEnv to contain %s, got %v", key, lc.ClearEnv)
		}
		assertEnvMissing(t, lc.Env, key)
	}

	got, ok := argsFlagValue(lc.Args, "--model")
	if !ok {
		t.Fatalf("expected --model flag in args, got %v", lc.Args)
	}
	if got != m.ModelName {
		t.Fatalf("--model value = %q, want %q", got, m.ModelName)
	}
}

// TestClaudeDriverDeclaresEnvAndConfigFileProfileMechanisms verifies
// claude's ProfileMechanisms matches the Phase-1 design (env for the
// attribution header/thinking cap, config_content for the
// ANTHROPIC_DEFAULT_*_MODEL mapping via settings.local.json) — a profile
// using any other mechanism for claude must fail Validate.
func TestClaudeDriverDeclaresEnvAndConfigFileProfileMechanisms(t *testing.T) {
	mechs := claudeDriver{}.ProfileMechanisms()
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

// TestClaudeStateDir pins where claude keeps a working directory's project
// state: ~/.claude/projects/<slug>, every character outside [a-zA-Z0-9-]
// (an underscore included) turned into a dash.
func TestClaudeStateDir(t *testing.T) {
	t.Setenv("HOME", "/h")
	got := claudeDriver{}.StateDir("/private/var/x_y/T/wt-smoke-1")
	if want := "/h/.claude/projects/-private-var-x-y-T-wt-smoke-1"; got != want {
		t.Fatalf("StateDir = %q, want %q", got, want)
	}
}

// TestPiStateDir pins where pi keeps a working directory's sessions:
// <agent dir>/sessions/--<path>--, the leading slash dropped and only "/",
// "\" and ":" turned into dashes (an underscore survives), with
// PI_CODING_AGENT_DIR overriding ~/.pi/agent.
func TestPiStateDir(t *testing.T) {
	t.Setenv("HOME", "/h")
	t.Setenv("PI_CODING_AGENT_DIR", "")
	got := piDriver{}.StateDir("/private/var/x_y/T/wt-smoke-1")
	if want := "/h/.pi/agent/sessions/--private-var-x_y-T-wt-smoke-1--"; got != want {
		t.Fatalf("StateDir = %q, want %q", got, want)
	}
	t.Setenv("PI_CODING_AGENT_DIR", "/elsewhere")
	got = piDriver{}.StateDir("/tmp/wt-smoke-1")
	if want := "/elsewhere/sessions/--tmp-wt-smoke-1--"; got != want {
		t.Fatalf("StateDir with PI_CODING_AGENT_DIR = %q, want %q", got, want)
	}
}
