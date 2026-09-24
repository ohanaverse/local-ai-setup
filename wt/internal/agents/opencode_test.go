package agents

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
	"github.com/ohanaverse/local-ai-setup/wt/internal/session"
)

func gitInit(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "t@t"},
		{"config", "user.name", "t"},
		{"commit", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
		}
	}
}

// TestOpenCodeLatestSession asserts opencodeDriver finds the newest .json
// session file under the project-id directory.
func TestOpenCodeLatestSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repo := t.TempDir()
	gitInit(t, repo)
	id, err := session.OpenCodeProjectID(repo)
	if err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(home, ".local", "share", "opencode", "storage", "session", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(dir, "old.json")
	new := filepath.Join(dir, "new.json")
	if err := os.WriteFile(old, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(new, []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldInfo, _ := os.Stat(old)
	os.Chtimes(new, oldInfo.ModTime(), oldInfo.ModTime().Add(time.Second))

	d := opencodeDriver{}
	s, err := d.LatestSession(repo)
	if err != nil {
		t.Fatal(err)
	}
	if s == nil || s.ID != "new.json" {
		t.Fatalf("expected newest opencode session id \"new.json\", got %+v", s)
	}
}

// TestOpenCodeLatestSessionNoDir asserts opencodeDriver returns nil when no
// session directory exists, without returning an error.
func TestOpenCodeLatestSessionNoDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repo := t.TempDir()
	gitInit(t, repo)

	d := opencodeDriver{}
	s, err := d.LatestSession(repo)
	if err != nil {
		t.Fatal(err)
	}
	if s != nil {
		t.Fatalf("expected nil session, got %+v", s)
	}
}

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
