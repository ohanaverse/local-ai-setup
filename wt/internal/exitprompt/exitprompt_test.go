package exitprompt

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
)

// testRepo makes a fresh repository with one commit on main.
func testRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main", dir},
		{"-C", dir, "config", "user.email", "test@test"},
		{"-C", dir, "config", "user.name", "Test"},
		{"-C", dir, "config", "commit.gpgsign", "false"},
		{"-C", dir, "commit", "--allow-empty", "-q", "-m", "init"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

// TestRecordStoresTheCreationTip checks Record keeps the path, the branch and
// the worktree's HEAD sha at the moment of creation, because the exit prompt
// later compares the branch tip with it to tell whether anything happened.
func TestRecordStoresTheCreationTip(t *testing.T) {
	Reset()
	t.Cleanup(Reset)
	root := testRepo(t)
	path, err := worktree.EnsureForName(root, "topic")
	if err != nil {
		t.Fatal(err)
	}
	if err := Record(path, "topic"); err != nil {
		t.Fatal(err)
	}
	want, err := exec.Command("git", "-C", path, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	got, ok := Recorded()
	if !ok {
		t.Fatal("Recorded() = false after Record")
	}
	if got.Path != path || got.Branch != "topic" || got.Tip != strings.TrimSpace(string(want)) {
		t.Errorf("Recorded() = %+v; want path %s, branch topic, tip %s", got, path, want)
	}
}

// TestRecordedIsEmptyUntilRecorded checks a launch that created nothing has
// nothing recorded, so no route can prompt by accident, and that a failed
// Record (not a git directory) records nothing.
func TestRecordedIsEmptyUntilRecorded(t *testing.T) {
	Reset()
	t.Cleanup(Reset)
	if _, ok := Recorded(); ok {
		t.Fatal("Recorded() = true before any Record")
	}
	if err := Record(t.TempDir(), "x"); err == nil {
		t.Error("Record outside a repository returned nil")
	}
	if _, ok := Recorded(); ok {
		t.Error("a failed Record left something recorded")
	}
}
