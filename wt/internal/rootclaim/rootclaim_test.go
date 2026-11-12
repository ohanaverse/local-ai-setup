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

// TestAskYesNoDefaultsToNo checks only y or Y answers yes; Enter, n, q, Esc,
// Ctrl-C and end of input all answer no, and the prompt text and the [y/N]
// default are printed, so a stray key never confirms the removal it is asked
// about.
func TestAskYesNoDefaultsToNo(t *testing.T) {
	cases := map[string]bool{"y": true, "Y": true, "n": false, "\n": false, "q": false, "\x1b": false, "\x03": false, "": false}
	for in, want := range cases {
		var out bytes.Buffer
		if got := AskYesNo("remove it?", strings.NewReader(in), &out); got != want {
			t.Errorf("AskYesNo(%q) = %v; want %v", in, got, want)
		}
		if want := "wt: remove it? [y/N]: "; !strings.Contains(out.String(), want) {
			t.Errorf("AskYesNo(%q) printed %q; want it to contain %q", in, out.String(), want)
		}
	}
}

// TestHintNamesTheDefault checks the restore-root hint appears only when the
// root sits on a named branch other than a known default: on the default, on a
// detached HEAD or with no known default (origin/HEAD unset) it is empty, so
// the line never points at a move there is nothing to do for or cannot name.
func TestHintNamesTheDefault(t *testing.T) {
	want := "run `wt restore-root` to move it and put the root back on main"
	if got := Hint(Root{Branch: "topic", Default: "main"}); got != want {
		t.Errorf("Hint(topic on main) = %q; want %q", got, want)
	}
	for name, r := range map[string]Root{
		"on the default":  {Branch: "main", Default: "main"},
		"detached":        {Branch: "(detached)", Default: "main"},
		"unknown default": {Branch: "topic", Default: ""},
	} {
		if got := Hint(r); got != "" {
			t.Errorf("Hint(%s) = %q; want empty", name, got)
		}
	}
}

// TestLookupReadsTheDefault checks Lookup fills Root.Default from origin/HEAD
// and leaves it empty without one, which is what hides the hint there.
func TestLookupReadsTheDefault(t *testing.T) {
	root := repo(t)
	if r, _, _ := Lookup(root); r.Default != "" {
		t.Errorf("Default = %q with no origin/HEAD; want empty", r.Default)
	}
	for _, args := range [][]string{
		{"update-ref", "refs/remotes/origin/main", "HEAD"},
		{"symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if r, _, _ := Lookup(root); r.Default != "main" {
		t.Errorf("Default = %q; want main", r.Default)
	}
}

// TestAskContinueDefaultsToYes checks an empty line, y and Y continue and
// anything else, including Esc, Ctrl-C and the end of input, stops.
func TestAskContinueDefaultsToYes(t *testing.T) {
	for in, want := range map[string]bool{
		"\n": true, "y\n": true, "Y\n": true, " y \n": true,
		"n\n": false, "N\n": false, "x\n": false, "\x1b\n": false, "\x03\n": false,
		"yes please\n": false, "": false,
	} {
		var out bytes.Buffer
		if got := AskContinue(strings.NewReader(in), &out); got != want {
			t.Errorf("AskContinue(%q) = %v, want %v", in, got, want)
		}
		if strings.Contains(out.String(), "[Y/n]") {
			t.Errorf("AskContinue printed a prompt: %q", out.String())
		}
	}
}
