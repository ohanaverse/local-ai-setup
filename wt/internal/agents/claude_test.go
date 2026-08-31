package agents

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/session"
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

// TestClaudeLatestSession asserts claudeDriver finds the newest .jsonl
// session file under ~/.claude/projects/<slug> and strips the extension.
func TestClaudeLatestSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	worktree := "/some/worktree/path"
	dir := filepath.Join(home, ".claude", "projects", session.Slug(worktree))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(dir, "old.jsonl")
	new := filepath.Join(dir, "new.jsonl")
	if err := os.WriteFile(old, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(new, []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldInfo, _ := os.Stat(old)
	os.Chtimes(new, oldInfo.ModTime(), oldInfo.ModTime().Add(time.Second))

	d := claudeDriver{}
	s, err := d.LatestSession(worktree)
	if err != nil {
		t.Fatal(err)
	}
	if s == nil || s.ID != "new" {
		t.Fatalf("expected newest claude session id \"new\", got %+v", s)
	}
}

// TestClaudeLatestSessionNoDir asserts claudeDriver returns nil when no
// session directory exists, without returning an error.
func TestClaudeLatestSessionNoDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	d := claudeDriver{}
	s, err := d.LatestSession("/nonexistent/worktree")
	if err != nil {
		t.Fatal(err)
	}
	if s != nil {
		t.Fatalf("expected nil session, got %+v", s)
	}
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

// TestClaudeOllamaURL asserts claudeDriver returns the bare gateway URL.
func TestClaudeOllamaURL(t *testing.T) {
	var d Driver = claudeDriver{}
	u, ok := d.(OllamaURLer)
	if !ok {
		t.Fatal("claudeDriver does not implement OllamaURLer")
	}
	if got := u.OllamaURL(); got != "http://localhost:11434" {
		t.Errorf("OllamaURL() = %q, want http://localhost:11434", got)
	}
}

// TestClaudeBuildLitellm asserts the claude driver routes through the LiteLLM
// gateway with the registry model id and gateway credentials.
func TestClaudeBuildLitellm(t *testing.T) {
	m := config.Model{ID: "ollama/qwen3.8:27b-mlx", ModelName: "qwen3.8:27b-mlx", ProviderID: "ollama"}
	gw := Gateway{Mode: "litellm", URL: "http://localhost:4000", APIKey: "sk-litellm"}
	lc := claudeDriver{}.Build(m, false, gw)
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
	gw := Gateway{Mode: "litellm", URL: "http://localhost:4000", APIKey: "sk-litellm"}
	lc := claudeDriver{}.Build(m, true, gw)
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
	gw := Gateway{Mode: "litellm", URL: "http://localhost:4000", APIKey: "sk-litellm"}
	lc := claudeDriver{}.Build(m, false, gw)

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
