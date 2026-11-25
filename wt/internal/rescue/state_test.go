package rescue

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHashPathReadsNothingBehindALeadingSymlinkOrNonDirectory checks that a
// path whose parent is a symlink, a file or missing reads "absent", as git
// sees it (dirtsnap.prefixCheck is the same rule). Why: os.Lstat only declines
// to follow the last component, so a tracked directory replaced by a symlink
// used to be hashed through the link and an edit outside the repository read
// as a source change.
func TestHashPathReadsNothingBehindALeadingSymlinkOrNonDirectory(t *testing.T) {
	root := t.TempDir()
	out := t.TempDir()
	if err := os.WriteFile(filepath.Join(out, "y.txt"), []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(out, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("f"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "dir", "y.txt"), []byte("in"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(out, filepath.Join(root, "dir", "deep")); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, rel, wantPrefix string
	}{
		{"parent is a symlink to an outside directory", "link/y.txt", "absent"},
		{"a symlink two levels down", "dir/deep/y.txt", "absent"},
		{"parent is a regular file", "file/y.txt", "absent"},
		{"parent is missing", "nope/y.txt", "absent"},
		{"the symlink itself is read as a link", "link", "link:"},
		{"a normal nested file", "dir/y.txt", "file:644:"},
		{"a missing file in a real directory", "dir/none.txt", "absent"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := hashPath(root, c.rel)
			if !strings.HasPrefix(got, c.wantPrefix) {
				t.Fatalf("hashPath(%q) = %q, want prefix %q", c.rel, got, c.wantPrefix)
			}
		})
	}
}

// TestPreflightIgnoresAnEditBehindATrackedDirectoryReplacedByASymlink checks
// that with a tracked directory replaced by a symlink to an outside
// directory, changing the file outside leaves SourceUnchanged() empty and
// DestinationMatches agreeing. Why: git does not look through the link, so an
// outside edit is not a change to the source. The classifier's side of the
// same shape is dirtsnap_test.go ("tracked directory replaced by a symlink to
// an outside directory"); this test covers the fingerprint's side.
func TestPreflightIgnoresAnEditBehindATrackedDirectoryReplacedByASymlink(t *testing.T) {
	f := newFixture(t)
	p := f.Worktree("topic")
	f.Commit(p, "x/y.txt", "tracked", "add x/y.txt")
	out := t.TempDir()
	outFile := filepath.Join(out, "y.txt")
	if err := os.WriteFile(outFile, []byte("outside one"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(p, "x")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(out, filepath.Join(p, "x")); err != nil {
		t.Fatal(err)
	}
	_, plan := preflightPlan(t, f, p)

	if err := os.WriteFile(outFile, []byte("outside two, longer"), 0o644); err != nil {
		t.Fatal(err)
	}

	if diff, err := plan.SourceUnchanged(); err != nil || diff != "" {
		t.Fatalf("SourceUnchanged = %q, %v; want unchanged after an edit outside the repository", diff, err)
	}
	if got := plan.fp.files["x/y.txt"]; got != "absent" {
		t.Fatalf("fingerprint of x/y.txt = %q, want absent", got)
	}

	// The verify side: a destination with the same tracked file replaced by a
	// symlink to the same outside directory (whose file was edited after the
	// fingerprint) must match, because DestinationMatches uses the same hashPath.
	dest := f.Worktree("dest")
	f.Commit(dest, "x/y.txt", "tracked", "add x/y.txt")
	if err := os.RemoveAll(filepath.Join(dest, "x")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(out, filepath.Join(dest, "x")); err != nil {
		t.Fatal(err)
	}
	if d, err := plan.DestinationMatches(dest); err != nil || d != "" {
		t.Fatalf("DestinationMatches = %q, %v; want a match despite the outside edit", d, err)
	}
}
