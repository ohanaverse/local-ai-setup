package rootclaim

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// linkedRepo returns a repository with one commit on main and a linked
// worktree checked out on feat at <root>/.worktrees/feat — the shape every
// launch claims in: two worktrees, one common git directory. Both paths have
// their symlinks resolved (repo does), so they are already the claim-path
// text git answers with.
func linkedRepo(t *testing.T) (root, linked string) {
	t.Helper()
	root = repo(t)
	linked = filepath.Join(root, ".worktrees", "feat")
	if out, err := exec.Command("git", "-C", root, "worktree", "add", "-b", "feat", linked).CombinedOutput(); err != nil {
		t.Fatalf("worktree add: %v\n%s", err, out)
	}
	return root, linked
}

// TestLocateSiteLinkedWorktree checks a directory in a linked worktree — the
// worktree itself or a subdirectory of it — locates that worktree's own top
// level (the claim path two launches in one worktree share), the common git
// directory every claim lives in and the worktree's branch, and is not the
// root: the site is where the directory sits, not what the repository is.
func TestLocateSiteLinkedWorktree(t *testing.T) {
	root, linked := linkedRepo(t)
	sub := filepath.Join(linked, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	want := Site{Path: linked, CommonDir: filepath.Join(root, ".git"), Branch: "feat"}
	for name, from := range map[string]string{"linked worktree": linked, "its subdirectory": sub} {
		s, err := LocateSite(from)
		if err != nil || s != want {
			t.Errorf("LocateSite(%s) = %+v, %v; want %+v", name, s, err, want)
		}
	}
}

// TestLocateSiteRootMatchesLookup checks LocateSite's answer for the root
// checkout is the one Lookup has always given the launch paths — the same
// path spelling (git's text and the resolved form name one checkout), the
// same common directory, the same branch. The one deliberate difference is a
// detached HEAD: Lookup labels it "(detached)" while Site.Branch is empty,
// which the drift readers (a later task) read as detached.
func TestLocateSiteRootMatchesLookup(t *testing.T) {
	root, _ := linkedRepo(t)
	s, err := LocateSite(root)
	if err != nil {
		t.Fatalf("LocateSite(root) = %v", err)
	}
	r, ok, err := Lookup(root)
	if err != nil || !ok {
		t.Fatalf("Lookup(root) = %+v, %v, %v", r, ok, err)
	}
	if s.Path != r.Path || s.CommonDir != r.CommonDir || s.Branch != r.Branch {
		t.Errorf("LocateSite(root) = %+v; want Lookup's path %s, common dir %s, branch %s", s, r.Path, r.CommonDir, r.Branch)
	}
	if out, err := exec.Command("git", "-C", root, "checkout", "-q", "--detach").CombinedOutput(); err != nil {
		t.Fatalf("checkout --detach: %v\n%s", err, out)
	}
	s, err = LocateSite(root)
	r, _, _ = Lookup(root)
	if err != nil || s.Branch != "" || r.Branch != "(detached)" {
		t.Errorf("detached: Site = %+v, %v; Lookup branch %q; want Site.Branch empty and Lookup (detached)", s, err, r.Branch)
	}
}
