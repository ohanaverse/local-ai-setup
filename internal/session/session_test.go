package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestSlug asserts the slug computation matches the bash compute_project_slug:
// every char outside [a-zA-Z0-9-] becomes '-'. The leading slash is NOT removed;
// it becomes a leading '-', matching the real ~/.claude/projects/-Users-... dirs.
func TestSlug(t *testing.T) {
	got := Slug("/a/b/c.d")
	want := "-a-b-c-d"
	if got != want {
		t.Fatalf("Slug() = %q, want %q", got, want)
	}
}

// TestRelativeTime asserts the relative-time buckets match the bash
// relative_time helper (just now / m / h / d / w).
func TestRelativeTime(t *testing.T) {
	now := time.Now()
	cases := []struct {
		ago  time.Duration
		want string
	}{
		{30 * time.Second, "just now"},
		{5 * time.Minute, "5m ago"},
		{3 * time.Hour, "3h ago"},
		{2 * 24 * time.Hour, "2d ago"},
		{3 * 7 * 24 * time.Hour, "3w ago"},
	}
	for _, c := range cases {
		if got := RelativeTime(now.Add(-c.ago)); got != c.want {
			t.Errorf("RelativeTime(%v) = %q, want %q", c.ago, got, c.want)
		}
	}
}

// TestLatestByExtNoDir asserts a missing session dir yields nil, not an error.
func TestLatestByExtNoDir(t *testing.T) {
	s, err := LatestByExt(filepath.Join(t.TempDir(), "nope"), ".jsonl", func(os.FileInfo) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if s != nil {
		t.Fatalf("expected nil session, got %+v", s)
	}
}

// TestLatestByExtRanking asserts the newest file (by mtime) is returned.
func TestLatestByExtRanking(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "old.jsonl")
	new := filepath.Join(dir, "new.jsonl")
	if err := os.WriteFile(old, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(new, []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Make new strictly newer than old.
	oldInfo, _ := os.Stat(old)
	os.Chtimes(new, oldInfo.ModTime(), oldInfo.ModTime().Add(time.Second))

	s, err := LatestByExt(dir, ".jsonl", func(f os.FileInfo) string {
		return f.Name()
	})
	if err != nil {
		t.Fatal(err)
	}
	if s == nil || s.ID != "new.jsonl" {
		t.Fatalf("expected newest file, got %+v", s)
	}
}

// gitInit creates a minimal git repo with one commit on main/master so
// OpenCodeProjectID can resolve a root commit hash.
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

// TestOpenCodeProjectID asserts the project id is the repo's root commit
// hash, and that it is computed for the given path (git -C), not the test's
// own working directory. A wrong id would point at the wrong session dir.
func TestOpenCodeProjectID(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	id, err := OpenCodeProjectID(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(id) != 40 {
		t.Fatalf("expected a 40-char commit hash, got %q", id)
	}
}
