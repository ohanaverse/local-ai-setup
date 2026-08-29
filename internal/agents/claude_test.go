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
