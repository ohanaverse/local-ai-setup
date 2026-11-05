package removal

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// TestEngineRemovesWorktreeWithIgnoredFiles removes a merged worktree whose
// only extra is an ignored .env: the list told the user (Losses counts it),
// the ignored file goes with the directory, and no --force is needed.
func TestEngineRemovesWorktreeWithIgnoredFiles(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	p := f.Worktree("topic")
	f.Ignored(p, ".env")
	s, r := f.report()
	row := f.row(r, cleanup.KindWorktree, "topic")
	if row.Losses.IgnoredEntries != 1 || row.Losses.Dirty {
		t.Fatalf("precondition: losses %+v, want one ignored entry and not dirty", row.Losses)
	}
	o := one(t, f.engine(s, f.inputs(), Options{}), itemOf(row))
	if o.Worktree != WtRemoved {
		t.Fatalf("outcome %+v, want removed", o)
	}
	if exists(t, filepath.Join(p, ".env")) || exists(t, p) || f.registered(p) {
		t.Error("the worktree or its ignored file is still there")
	}
}

// TestInterruptedRemoveLeavesPrunableAndIsReported makes the removal die
// after the directory is gone but before git unregisters it (a crash or
// timeout mid-way): the outcome must be failed and say a prunable
// registration remains, never success or silence.
func TestInterruptedRemoveLeavesPrunableAndIsReported(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	p := f.Worktree("topic")
	s, r := f.report()
	stubRunGit(t, func(g cleanup.Git, args ...string) (cleanup.Out, error) {
		if err := os.RemoveAll(p); err != nil {
			t.Fatal(err)
		}
		return cleanup.Out{Code: 128, Stderr: "fatal: interrupted\n"}, nil
	})
	o := one(t, f.engine(s, f.inputs(), Options{}), itemOf(f.row(r, cleanup.KindWorktree, "topic")))
	if o.Worktree != WtFailed || !strings.Contains(o.Reason, "prunable") || !strings.Contains(o.Reason, "interrupted") {
		t.Fatalf("outcome %+v, want failed naming the prunable registration and git's error", o)
	}
	if o.Branch != BrNotAttempted {
		t.Errorf("branch outcome %s after a failed removal, want not_attempted", o.Branch)
	}
	if exists(t, p) || !f.registered(p) {
		t.Fatalf("fixture: on disk %v, registered %v; want gone but registered", exists(t, p), f.registered(p))
	}
}

// TestRemoveFailureLeavesWorktreeAndIsReported has git fail the removal with
// the directory still there: failed, with git's stderr on one line, and no
// claim that the tree is intact (git may have deleted part of it first).
func TestRemoveFailureLeavesWorktreeAndIsReported(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	p := f.Worktree("topic")
	s, r := f.report()
	stubRunGit(t, func(cleanup.Git, ...string) (cleanup.Out, error) {
		return cleanup.Out{Code: 128, Stderr: "fatal: cannot remove\n\x1b[31mred\n"}, nil
	})
	o := one(t, f.engine(s, f.inputs(), Options{}), itemOf(f.row(r, cleanup.KindWorktree, "topic")))
	if o.Worktree != WtFailed || !strings.Contains(o.Reason, "fatal: cannot remove") || !strings.Contains(o.Reason, "may be partly removed") {
		t.Fatalf("outcome %+v, want failed with git's error", o)
	}
	if strings.ContainsAny(o.Reason, "\n\x1b") {
		t.Errorf("reason %q is not one sanitised line", o.Reason)
	}
	if !exists(t, p) {
		t.Error("worktree gone")
	}
}

// TestRemoveTimeoutIsFailed has the removal time out: failed, naming the
// timeout and what is left, and the configured timeout reaches the runner.
func TestRemoveTimeoutIsFailed(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	f.Worktree("topic")
	s, r := f.report()
	var got cleanup.Git
	stubRunGit(t, func(g cleanup.Git, _ ...string) (cleanup.Out, error) {
		got = g
		return cleanup.Out{}, context.DeadlineExceeded
	})
	e := f.engine(s, f.inputs(), Options{RemoveTimeout: 7e9})
	o := one(t, e, itemOf(f.row(r, cleanup.KindWorktree, "topic")))
	if o.Worktree != WtFailed || !strings.Contains(o.Reason, "deadline") || !strings.Contains(o.Reason, "may be partly removed") {
		t.Fatalf("outcome %+v, want failed naming the timeout", o)
	}
	if got.Timeout != 7e9 || got.Dir != f.Dir {
		t.Errorf("runner got %+v, want Dir %s and the 7s timeout", got, f.Dir)
	}
	f.engine(s, f.inputs(), Options{}).Run([]Item{itemOf(f.row(r, cleanup.KindWorktree, "topic"))}, nil)
	if got.Timeout != DefaultRemoveTimeout {
		t.Errorf("zero RemoveTimeout gave %v, want %v", got.Timeout, DefaultRemoveTimeout)
	}
}

// TestRemoveVerifiesGitSuccess has git report success (exit 0) without
// finishing the job: the engine must not take git's word for it. A
// directory still there, or a registration left behind, is failed; only
// both gone is removed.
func TestRemoveVerifiesGitSuccess(t *testing.T) {
	for _, c := range []struct {
		name  string
		act   func(t *testing.T, g cleanup.Git, p string, args []string)
		want  WorktreeOutcome
		words []string
	}{
		{"directory remains", func(*testing.T, cleanup.Git, string, []string) {}, WtFailed,
			[]string{"git reported success", "the directory is still there"}},
		{"registration remains", func(t *testing.T, _ cleanup.Git, p string, _ []string) {
			if err := os.RemoveAll(p); err != nil {
				t.Fatal(err)
			}
		}, WtFailed, []string{"git reported success", "prunable registration"}},
		{"both gone", func(t *testing.T, g cleanup.Git, _ string, args []string) {
			if o, err := g.Run(args...); err != nil || o.Code != 0 {
				t.Fatalf("real removal: %v %+v", err, o)
			}
		}, WtRemoved, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			noGh(t)
			f := newFixture(t)
			p := f.Worktree("topic")
			s, r := f.report()
			stubRunGit(t, func(g cleanup.Git, args ...string) (cleanup.Out, error) {
				c.act(t, g, p, args)
				return cleanup.Out{}, nil
			})
			o := one(t, f.engine(s, f.inputs(), Options{}), itemOf(f.row(r, cleanup.KindWorktree, "topic")))
			if o.Worktree != c.want {
				t.Fatalf("outcome %+v, want %s", o, c.want)
			}
			for _, w := range c.words {
				if !strings.Contains(o.Reason, w) {
					t.Errorf("reason %q does not say %q", o.Reason, w)
				}
			}
			if c.want == WtFailed && o.Branch != BrNotAttempted {
				t.Errorf("branch outcome %s after an unverified removal, want not_attempted", o.Branch)
			}
		})
	}
}
