package agents

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

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
