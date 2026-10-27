package rootclaim

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// repo makes a repository with one commit on main and returns its root with
// symlinks resolved.
func repo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"symbolic-ref", "HEAD", "refs/heads/main"},
		{"commit", "--allow-empty", "-q", "-m", "init"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// TestLookupRoot checks Lookup names the root, its common directory and its
// branch, and that Self builds the claim from them: both launch paths build
// their claim here, so a drift in this answer changes what repo-cleanup reads.
func TestLookupRoot(t *testing.T) {
	root := repo(t)
	r, ok, err := Lookup(root)
	if !ok || err != nil {
		t.Fatalf("Lookup = %+v, %v, %v", r, ok, err)
	}
	if r.Path != root || r.CommonDir != filepath.Join(root, ".git") || r.Branch != "main" {
		t.Errorf("Lookup = %+v; want root %s, common dir .git, branch main", r, root)
	}
	c, err := r.Self("claude")
	if err != nil || c.Path != root || c.Branch != "main" || c.Agent != "claude" {
		t.Errorf("Self = %+v, %v", c, err)
	}
}

// TestLookupDetachedHead checks a detached HEAD is labelled, not left blank.
func TestLookupDetachedHead(t *testing.T) {
	root := repo(t)
	if out, err := exec.Command("git", "-C", root, "checkout", "-q", "--detach").CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	r, ok, err := Lookup(root)
	if !ok || err != nil || r.Branch != "(detached)" {
		t.Errorf("Lookup = %+v, %v, %v; want branch (detached)", r, ok, err)
	}
}

// TestLookupNotRoot checks a subdirectory is not a root, so it gets no claim.
func TestLookupNotRoot(t *testing.T) {
	root := repo(t)
	if _, ok, _ := Lookup(t.TempDir()); ok {
		t.Error("a directory outside any repository counts as a root")
	}
	sub := filepath.Join(root, "sub")
	if out, err := exec.Command("mkdir", sub).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if _, ok, _ := Lookup(sub); ok {
		t.Error("a subdirectory counts as a root")
	}
}

// TestChooseKeys checks each key maps to its choice, Esc, Ctrl-C and end of
// input are q, other keys are skipped, and the menu is printed first.
func TestChooseKeys(t *testing.T) {
	cases := map[string]byte{"c": 'c', "C": 'c', "w": 'w', "q": 'q', "\x1b": 'q', "\x03": 'q', "": 'q', "xyzw": 'w'}
	for in, want := range cases {
		var out bytes.Buffer
		if got := Choose(Root{Path: "/r"}, nil, strings.NewReader(in), &out); got != want {
			t.Errorf("Choose(%q) = %q; want %q", in, got, want)
		}
		if !strings.Contains(out.String(), "choose [c/w/q]: ") {
			t.Errorf("Choose(%q) printed %q; want the prompt", in, out.String())
		}
	}
}
