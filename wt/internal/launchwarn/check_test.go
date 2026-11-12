package launchwarn

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// failReader fails the test if it is read.
type failReader struct{ t *testing.T }

func (r failReader) Read([]byte) (int, error) {
	r.t.Error("Check read input, but no question should have been asked")
	return 0, errors.New("unexpected read")
}

var twoWarnings = []Warning{{CodeRootDrift, "root is on feat, not main"}, {CodeUpstreamGone, "its upstream branch is gone"}}

func TestCheckNoTTYPrintsWarningsAndProceeds(t *testing.T) {
	var out bytes.Buffer
	if !Check(failReader{t}, &out, false, twoWarnings, "launching in /d", nil) {
		t.Fatal("non-TTY must continue")
	}
	for _, s := range []string{"wt: root is on feat, not main\n", "wt: its upstream branch is gone\n"} {
		if !strings.Contains(out.String(), s) {
			t.Errorf("missing %q in %q", s, out.String())
		}
	}
	if strings.Contains(out.String(), "[Y/n]") || strings.Contains(out.String(), "launching in") {
		t.Errorf("unexpected text: %q", out.String())
	}
	out.Reset()
	Check(failReader{t}, &out, false, nil, "launching in /d", nil)
	if out.String() != "wt: launching in /d\n" {
		t.Errorf("info = %q", out.String())
	}
}

func TestCheckTTYWithWarningsPromptsOnce(t *testing.T) {
	for in, want := range map[string]bool{"\n": true, "n\n": false, "": false} {
		var out bytes.Buffer
		if got := Check(strings.NewReader(in), &out, true, twoWarnings, "launching in /d", nil); got != want {
			t.Errorf("input %q: got %v want %v", in, got, want)
		}
		if n := strings.Count(out.String(), "[Y/n]"); n != 1 {
			t.Errorf("input %q: %d prompts in %q", in, n, out.String())
		}
	}
}

func TestCheckTTYNoWarningsDoesNotPrompt(t *testing.T) {
	var out bytes.Buffer
	if !Check(failReader{t}, &out, true, nil, "launching in /d", nil) {
		t.Fatal("want true")
	}
	if out.String() != "wt: launching in /d\n" {
		t.Errorf("out = %q", out.String())
	}
}

func TestCheckCreationOnlyDoesNotPrompt(t *testing.T) {
	for _, tty := range []bool{true, false} {
		var out bytes.Buffer
		c := &Creation{Branch: "b", Tracks: "origin/b", NewBranch: true, Path: "/r/.worktrees/b"}
		if !Check(failReader{t}, &out, tty, nil, "launching in /d", c) {
			t.Fatal("want true")
		}
		if out.String() != "wt: creating branch b tracking origin/b and worktree /r/.worktrees/b\n" {
			t.Errorf("tty=%v out = %q", tty, out.String())
		}
	}
	var out bytes.Buffer
	Check(failReader{t}, &out, true, nil, "", &Creation{Branch: "b", Path: "/p"})
	if out.String() != "wt: creating worktree /p for branch b\n" {
		t.Errorf("existing branch: %q", out.String())
	}
}

func TestCheckCreationThenWarningsPrompt(t *testing.T) {
	var out bytes.Buffer
	c := &Creation{Branch: "b", Path: "/p", NewBranch: true}
	Check(strings.NewReader("\n"), &out, true, twoWarnings[:1], "x", c)
	s := out.String()
	if strings.Index(s, "creating") > strings.Index(s, "[Y/n]") || strings.Index(s, "creating") < 0 {
		t.Errorf("creation must precede prompt: %q", s)
	}
}

func TestCheckLinesStartWithWtPrefix(t *testing.T) {
	c := &Creation{Branch: "b", Tracks: "origin/b", NewBranch: true, Path: "/p"}
	for _, tty := range []bool{true, false} {
		var out bytes.Buffer
		Check(strings.NewReader("\n"), &out, tty, twoWarnings, "launching in /d", c)
		for _, l := range strings.Split(strings.TrimRight(out.String(), "\n"), "\n") {
			if !strings.HasPrefix(l, "wt: ") {
				t.Errorf("tty=%v line %q lacks prefix", tty, l)
			}
		}
	}
}
