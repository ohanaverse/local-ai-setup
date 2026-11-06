package cleanup

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestHeldRefsRepoPathWithBracket: the administrative-directory lookup took
// the repository path as a glob pattern, so a repository under `x[1]` matched
// nothing and one under an unbalanced `bad[` was a pattern error. Either way
// every HeldRefs call for a linked worktree failed, which (failing closed)
// silently kept every branch from ever being removed. The lookup lists the
// worktrees directory instead.
func TestHeldRefsRepoPathWithBracket(t *testing.T) {
	for _, parent := range []string{"x[1]", "bad["} {
		t.Run(parent, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), parent, "repo")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			gitIn(t, dir, "init", "-q", "-b", "main")
			gitIn(t, dir, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "i")
			p := filepath.Join(dir, ".worktrees", "topic")
			gitIn(t, dir, "worktree", "add", "-q", "-b", "topic", p)
			// A stale admin entry with no gitdir file is skipped, not an error.
			if err := os.MkdirAll(filepath.Join(dir, ".git", "worktrees", "stale"), 0o755); err != nil {
				t.Fatal(err)
			}
			f := &fixture{t: t, Dir: dir}
			got, err := HeldRefs(Git{Dir: dir}, factAt(t, f, p))
			if err != nil {
				t.Fatalf("HeldRefs: %v", err)
			}
			if !slices.Equal(got, []string{"refs/heads/topic"}) {
				t.Errorf("HeldRefs = %q, want [refs/heads/topic]", got)
			}
		})
	}
}
