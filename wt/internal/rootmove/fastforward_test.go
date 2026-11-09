package rootmove

import (
	"os"
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

// TestExecuteFastForwardIgnoredCollision puts an ignored file in the root at
// a path the fast-forward would add, with exact and case-differing names.
// The fast-forward must be skipped with a warning, the file's content kept,
// and the status stay moved. Why: git merge has no no-overwrite-ignore flag
// and would overwrite the ignored file (the root switch is the one that has
// it); on a case-insensitive setting a name differing only in case collides.
func TestExecuteFastForwardIgnoredCollision(t *testing.T) {
	cases := []struct {
		name       string
		ignorecase bool
		file       string
		skipped    bool
	}{
		{"exact", false, "upstream-0.txt", true},
		{"case differs, ignorecase on", true, "UPSTREAM-0.txt", true},
		{"case differs, ignorecase off", false, "UPSTREAM-0.txt", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			f.onBranch("topic")
			f.originAhead(1)
			f.exclude(c.file)
			f.write(f.Dir, c.file, "precious")
			if c.ignorecase {
				f.git("config", "core.ignorecase", "true")
			} else {
				f.git("config", "core.ignorecase", "false")
			}
			eng := f.engine()
			p, err := eng.Preflight("")
			if err != nil {
				t.Fatalf("Preflight: %v", err)
			}
			o := eng.Execute(p)
			wantStatus(t, o, rescue.StatusMoved, "")
			if c.skipped {
				if o.FastForwarded || !hasWarning(o, "ignored", c.file) {
					t.Errorf("FastForwarded %v, warnings %q; want skipped with an ignored-file warning", o.FastForwarded, o.Plan.Warnings)
				}
				if got := rev(f.Dir, "refs/heads/main"); got == rev(f.Dir, "refs/remotes/origin/main") {
					t.Errorf("main was fast-forwarded")
				}
			}
			b, err := os.ReadFile(filepath.Join(f.Dir, c.file))
			if err != nil || string(b) != "precious" {
				t.Errorf("ignored file = %q, %v; want it intact", b, err)
			}
		})
	}
}
