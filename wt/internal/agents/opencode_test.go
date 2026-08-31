package agents

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/agent-worktree/internal/config"
	"github.com/ohanaverse/agent-worktree/internal/session"
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

// TestOpenCodeOllamaURL asserts opencodeDriver returns the /v1 endpoint.
func TestOpenCodeOllamaURL(t *testing.T) {
	var d Driver = opencodeDriver{}
	u, ok := d.(OllamaURLer)
	if !ok {
		t.Fatal("opencodeDriver does not implement OllamaURLer")
	}
	if got := u.OllamaURL(); got != "http://localhost:11434/v1" {
		t.Errorf("OllamaURL() = %q, want http://localhost:11434/v1", got)
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
	gw := Gateway{Mode: "litellm", URL: "http://localhost:4000", APIKey: "sk-litellm"}
	lc := opencodeDriver{}.Build(m, false, gw)
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
