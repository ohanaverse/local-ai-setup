package agents

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ohanaverse/agent-worktree/internal/session"
)

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
