package rootmove

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rescue"
)

func hasWarning(o Outcome, parts ...string) bool {
	return slices.ContainsFunc(o.Plan.Warnings, func(w string) bool {
		for _, p := range parts {
			if !strings.Contains(w, p) {
				return false
			}
		}
		return true
	})
}

// TestExecuteFastForward moves a root whose default branch is strictly behind
// origin (fast-forwarded: the tip equals origin/main, no merge commit), ahead
// of it, diverged from it, or has no remote branch (all skipped with
// FastForwarded false, the reason in the warnings, status moved). Why: the
// plan allows a fast-forward only when origin/<default> is a strict
// descendant of the local default, and never a merge.
func TestExecuteFastForward(t *testing.T) {
	run := func(t *testing.T, f *fixture) Outcome {
		t.Helper()
		eng := f.engine()
		p, err := eng.Preflight("")
		if err != nil {
			t.Fatalf("Preflight: %v", err)
		}
		return eng.Execute(p)
	}
	t.Run("strictly behind", func(t *testing.T) {
		f := newFixture(t)
		f.onBranch("topic")
		f.dirty()
		f.originAhead(2)
		o := run(t, f)
		wantStatus(t, o, rescue.StatusMoved, "")
		if !o.FastForwarded {
			t.Errorf("FastForwarded = false, want true")
		}
		if got, want := rev(f.Dir, "refs/heads/main"), rev(f.Dir, "refs/remotes/origin/main"); got != want {
			t.Errorf("main = %s, want origin/main %s", got, want)
		}
		if n := strings.TrimSpace(soft(f.Dir, "rev-list", "--merges", "--count", "main")); n != "0" {
			t.Errorf("merge commits on main = %s, want 0", n)
		}
		wantRootOnDefault(t, f)
	})
	t.Run("ahead", func(t *testing.T) {
		f := newFixture(t)
		f.git("switch", "-q", "main")
		f.Commit(f.Dir, "local.txt", "l", "local only")
		f.onBranch("topic")
		o := run(t, f)
		wantStatus(t, o, rescue.StatusMoved, "")
		if o.FastForwarded || !hasWarning(o, "ahead") {
			t.Errorf("FastForwarded %v, warnings %q; want skipped with the ahead reason", o.FastForwarded, o.Plan.Warnings)
		}
	})
	t.Run("diverged", func(t *testing.T) {
		f := newFixture(t)
		f.git("switch", "-q", "main")
		f.Commit(f.Dir, "local.txt", "l", "local only")
		f.originAhead(1)
		f.onBranch("topic")
		before := rev(f.Dir, "refs/heads/main")
		o := run(t, f)
		wantStatus(t, o, rescue.StatusMoved, "")
		if o.FastForwarded || !hasWarning(o, "diverged") {
			t.Errorf("FastForwarded %v, warnings %q; want skipped with the diverged reason", o.FastForwarded, o.Plan.Warnings)
		}
		if got := rev(f.Dir, "refs/heads/main"); got != before {
			t.Errorf("main moved to %s from %s", got, before)
		}
	})
	t.Run("no remote branch", func(t *testing.T) {
		f := newFixture(t)
		f.onBranch("topic")
		f.git("update-ref", "-d", "refs/remotes/origin/main")
		o := run(t, f)
		wantStatus(t, o, rescue.StatusMoved, "")
		if o.FastForwarded || !hasWarning(o, "origin/main") {
			t.Errorf("FastForwarded %v, warnings %q; want skipped with the no-remote reason", o.FastForwarded, o.Plan.Warnings)
		}
	})
}

// TestExecuteFastForwardMergeFails makes the merge fail through the stepGit
// seam after a dirty move. Execute must warn and stay moved: the root is on
// main and clean, the new worktree holds the work (row 6 of the recovery
// table). Why: a fast-forward is a convenience; its failure never undoes or
// fails the move.
func TestExecuteFastForwardMergeFails(t *testing.T) {
	f := newFixture(t)
	f.onBranch("topic")
	f.dirty()
	f.originAhead(1)
	eng := f.engine()
	p, err := eng.Preflight("")
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	before := rev(f.Dir, "refs/heads/main")
	swap(t, &stepGit, func(g cleanup.Git, args ...string) (cleanup.Out, error) {
		if slices.Contains(args, "merge") {
			return cleanup.Out{Code: 128, Stderr: "fatal: simulated merge failure"}, nil
		}
		return g.Run(args...)
	})
	o := eng.Execute(p)
	wantStatus(t, o, rescue.StatusMoved, "")
	if o.FastForwarded || !hasWarning(o, "merge", "simulated merge failure") {
		t.Errorf("FastForwarded %v, warnings %q; want a merge-failure warning", o.FastForwarded, o.Plan.Warnings)
	}
	wantRootOnDefault(t, f)
	if got := rev(f.Dir, "refs/heads/main"); got != before {
		t.Errorf("main = %s, want unchanged %s", got, before)
	}
	if got := headRef(p.DestPath); got != "refs/heads/topic" {
		t.Errorf("new worktree on %q, want refs/heads/topic", got)
	}
	if got := statusOf(p.DestPath); !strings.Contains(got, "staged.txt") || !strings.Contains(got, "new.txt") {
		t.Errorf("new worktree status %q does not hold the work", got)
	}
}

// caseInsensitiveFS says whether the test's temp directory folds case.
func caseInsensitiveFS(t *testing.T) bool {
	t.Helper()
	d := t.TempDir()
	if err := os.WriteFile(filepath.Join(d, "probe"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := os.Stat(filepath.Join(d, "PROBE"))
	return err == nil
}

// TestExecuteFastForwardIgnoredCollision puts an ignored file in the root at
// a path the fast-forward would add, with exact and case-differing names, or
// at a parent of an added path (an ignored file `sub` where origin adds
// `sub/f`, or `a/b` where it adds `a/b/c/d`). The fast-forward must be
// skipped with a warning naming the ignored file, its content kept, the tip
// not advanced, status moved. With ignorecase off and a case-differing name
// the outcome depends on the filesystem, so it is asserted either way
// without claiming an ignored-file collision. Why: git merge has no
// no-overwrite-ignore flag and silently replaces an ignored file by a
// directory or overwrites it.
func TestExecuteFastForwardIgnoredCollision(t *testing.T) {
	cases := []struct {
		name       string
		ignorecase bool
		ignored    string   // the ignored file in the root
		upstream   []string // files origin adds
		skipped    bool
	}{
		{"exact", false, "upstream-0.txt", []string{"upstream-0.txt"}, true},
		{"ignored file under a directory the merge replaces", false, "build/x.o", []string{"build"}, true},
		{"case differs, ignorecase on", true, "UPSTREAM-0.txt", []string{"upstream-0.txt"}, true},
		{"case differs, ignorecase off", false, "UPSTREAM-0.txt", []string{"upstream-0.txt"}, false},
		{"ignored file is a parent", false, "sub", []string{"sub/f"}, true},
		{"ignored file is a deeper parent", false, "a/b", []string{"a/b/c/d"}, true},
		{"ignored parent, case differs, ignorecase on", true, "SUB", []string{"sub/f"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			f.onBranch("topic")
			other := filepath.Join(t.TempDir(), "other")
			if b, err := exec.Command("git", "clone", "-q", f.Origin, other).CombinedOutput(); err != nil {
				t.Fatalf("clone: %v\n%s", err, b)
			}
			gitIn(t, other, "config", "user.email", "o@example.com")
			gitIn(t, other, "config", "user.name", "O")
			gitIn(t, other, "config", "commit.gpgsign", "false")
			for _, u := range c.upstream {
				f.write(other, u, "u")
				gitIn(t, other, "add", u)
			}
			gitIn(t, other, "commit", "-m", "upstream")
			gitIn(t, other, "push", "-q", "origin", "HEAD:main")
			f.git("fetch", "-q", "origin")
			f.exclude(c.ignored)
			f.write(f.Dir, c.ignored, "precious")
			f.git("config", "core.ignorecase", fmt.Sprint(c.ignorecase))
			eng := f.engine()
			p, err := eng.Preflight("")
			if err != nil {
				t.Fatalf("Preflight: %v", err)
			}
			before := rev(f.Dir, "refs/heads/main")
			o := eng.Execute(p)
			wantStatus(t, o, rescue.StatusMoved, "")
			switch {
			case c.skipped:
				if o.FastForwarded || !hasWarning(o, "ignored file", c.ignored) {
					t.Errorf("FastForwarded %v, warnings %q; want skipped with an ignored-file warning", o.FastForwarded, o.Plan.Warnings)
				}
				if got := rev(f.Dir, "refs/heads/main"); got != before {
					t.Errorf("main advanced to %s", got)
				}
			case caseInsensitiveFS(t):
				// git refuses to overwrite the untracked name: a merge-failure warning.
				if o.FastForwarded || !hasWarning(o, "merge --ff-only") || hasWarning(o, "ignored file") {
					t.Errorf("FastForwarded %v, warnings %q; want a plain merge-failure warning", o.FastForwarded, o.Plan.Warnings)
				}
			default:
				if !o.FastForwarded {
					t.Errorf("FastForwarded false, warnings %q; want it to fast-forward on a case-sensitive filesystem", o.Plan.Warnings)
				}
			}
			b, err := os.ReadFile(filepath.Join(f.Dir, c.ignored))
			if err != nil || string(b) != "precious" {
				t.Errorf("ignored file = %q, %v; want it intact", b, err)
			}
		})
	}
}

// TestExecuteFastForwardAlreadyAtOrigin moves a root whose default already
// equals origin's. Nothing is fast-forwarded and nothing is wrong, so the
// note is a step in Completed and not a warning. Why: a benign state must
// not train users to ignore warnings.
func TestExecuteFastForwardAlreadyAtOrigin(t *testing.T) {
	f := newFixture(t)
	f.onBranch("topic")
	eng := f.engine()
	p, err := eng.Preflight("")
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	o := eng.Execute(p)
	wantStatus(t, o, rescue.StatusMoved, "")
	if o.FastForwarded {
		t.Errorf("FastForwarded = true, want false")
	}
	if hasWarning(o, "already at") {
		t.Errorf("warnings %q, want no already-at note", o.Plan.Warnings)
	}
	if !slices.ContainsFunc(o.Completed, func(s string) bool { return strings.Contains(s, "already at origin") }) {
		t.Errorf("Completed = %q, want a step saying main is already at origin", o.Completed)
	}
}
