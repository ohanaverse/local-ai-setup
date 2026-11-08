package removal

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// stubRescan swaps the engine's rescan for the rest of the test.
func stubRescan(t *testing.T, fn func(g cleanup.Git, prior cleanup.Snapshot, in cleanup.GatherInputs, o cleanup.Options, tg cleanup.Target) (cleanup.Row, error)) {
	t.Helper()
	old := rescan
	rescan = fn
	t.Cleanup(func() { rescan = old })
}

// countRescans counts rescans, running the real one.
func countRescans(t *testing.T) *int {
	t.Helper()
	n := 0
	stubRescan(t, func(g cleanup.Git, prior cleanup.Snapshot, in cleanup.GatherInputs, o cleanup.Options, tg cleanup.Target) (cleanup.Row, error) {
		n++
		return cleanup.Rescan(g, prior, in, o, tg)
	})
	return &n
}

// TestEngineRefusesUnknownSawStatus hands the engine items whose Saw is not
// a status a user can tick (empty, in_use, a misspelling): "worse than what
// the user saw" cannot be judged against it (an unknown status ranks worst,
// so nothing is ever worse), so the item is refused before any rescan. The
// reviewer's probe: a worktree listed proven that gained an ignored .env was
// removed when Saw was empty.
func TestEngineRefusesUnknownSawStatus(t *testing.T) {
	for _, saw := range []cleanup.Status{"", cleanup.StatusInUse, "Proven", "bogus"} {
		t.Run(string(saw), func(t *testing.T) {
			noGh(t)
			f := newFixture(t)
			p := f.Worktree("topic")
			s, r := f.report()
			row := f.row(r, cleanup.KindWorktree, "topic")
			if row.Status != cleanup.StatusProven {
				t.Fatalf("precondition: status %s, want proven", row.Status)
			}
			f.Ignored(p, ".env")
			it := itemOf(row)
			it.Saw = saw
			n := countRescans(t)
			o := one(t, f.engine(s, f.inputs(), Options{}), approve(it, true, true))
			if o.Worktree != WtRefused || !strings.Contains(o.Reason, "invalid target: unknown status seen") {
				t.Fatalf("outcome %+v, want refused: unknown status seen", o)
			}
			if *n != 0 {
				t.Errorf("%d rescans for an invalid item, want 0", *n)
			}
			if !exists(t, filepath.Join(p, ".env")) {
				t.Error("the worktree was removed")
			}
		})
	}
	t.Run("proven", func(t *testing.T) {
		noGh(t)
		f := newFixture(t)
		p := f.Worktree("topic")
		s, r := f.report()
		o := one(t, f.engine(s, f.inputs(), Options{}), itemOf(f.row(r, cleanup.KindWorktree, "topic")))
		if o.Worktree != WtRemoved || exists(t, p) {
			t.Fatalf("outcome %+v, want removed", o)
		}
	})
}

// TestEngineRefusesInvalidSawTip refuses an item whose SawTip could never
// match a fresh commit: empty on a branch item, or not a full commit id. A
// tip check that cannot be made must not be skipped silently.
func TestEngineRefusesInvalidSawTip(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	p := f.Worktree("topic")
	s, r := f.report()
	good := itemOf(f.row(r, cleanup.KindWorktree, "topic"))
	for name, tip := range map[string]string{
		"empty on a branch": "",
		"short":             good.SawTip[:12],
		"uppercase":         strings.ToUpper(good.SawTip),
		"junk":              "HEAD",
	} {
		t.Run(name, func(t *testing.T) {
			it := good
			it.SawTip = tip
			o := one(t, f.engine(s, f.inputs(), Options{}), approve(it, true, false))
			if o.Worktree != WtRefused || !strings.Contains(o.Reason, "invalid target") {
				t.Fatalf("outcome %+v, want refused: invalid target", o)
			}
		})
	}
	if !exists(t, p) {
		t.Error("worktree removed")
	}
}

// TestEngineSkipsWhenLossesGrew lists a less_sure worktree with one ignored
// entry, then adds more: the user approved losing that one entry, not what
// is there now (Ruling G), so the item is skipped. The same or fewer
// ignored entries proceed.
func TestEngineSkipsWhenLossesGrew(t *testing.T) {
	for _, c := range []struct {
		name string
		prep func(f *fixture, p string)
		want WorktreeOutcome
	}{
		{"more", func(f *fixture, p string) {
			f.Ignored(p, "a.log")
			f.Ignored(p, "b.log")
			f.Ignored(p, "c.log")
		}, WtSkippedChanged},
		{"same", func(*fixture, string) {}, WtRemoved},
		{"fewer", func(f *fixture, p string) {
			if err := os.Remove(filepath.Join(p, ".env")); err != nil {
				f.t.Fatal(err)
			}
		}, WtRemoved},
	} {
		t.Run(c.name, func(t *testing.T) {
			noGh(t)
			f := newFixture(t)
			p := f.Worktree("topic")
			f.Ignored(p, ".env")
			s, r := f.report()
			row := f.row(r, cleanup.KindWorktree, "topic")
			if row.Status != cleanup.StatusLessSure || row.Losses.IgnoredEntries != 1 {
				t.Fatalf("precondition: %s with losses %+v, want less_sure with one ignored entry", row.Status, row.Losses)
			}
			c.prep(f, p)
			o := one(t, f.engine(s, f.inputs(), Options{}), itemOf(row))
			if o.Worktree != c.want {
				t.Fatalf("outcome %+v, want %s", o, c.want)
			}
			if c.want == WtSkippedChanged && (!strings.Contains(o.Reason, "what would be lost changed") || !exists(t, p)) {
				t.Errorf("outcome %+v, on disk %v; want the loss change named and the worktree kept", o, exists(t, p))
			}
		})
	}
}

// TestLossesWorse pins the loss comparison of Ruling G: any loss the user
// did not see, or a count that grew or became unknown, is worse; equal or
// smaller is not. A zero SawLosses means the user saw nothing to lose.
func TestLossesWorse(t *testing.T) {
	base := cleanup.Losses{IgnoredEntries: 2, IgnoredBytes: 100, Unpushed: 1}
	for _, c := range []struct {
		name       string
		saw, fresh cleanup.Losses
		worse      bool
	}{
		{"equal", base, base, false},
		{"fewer", base, cleanup.Losses{IgnoredEntries: 1, IgnoredBytes: 10}, false},
		{"dirty", base, with(base, func(l *cleanup.Losses) { l.Dirty = true }), true},
		{"entries", base, with(base, func(l *cleanup.Losses) { l.IgnoredEntries = 3 }), true},
		{"bytes", base, with(base, func(l *cleanup.Losses) { l.IgnoredBytes = 101 }), true},
		{"unpushed up", base, with(base, func(l *cleanup.Losses) { l.Unpushed = 2 }), true},
		{"unpushed unknown", base, with(base, func(l *cleanup.Losses) { l.Unpushed = -1 }), true},
		{"unpushed still unknown", cleanup.Losses{Unpushed: -1}, cleanup.Losses{Unpushed: -1}, false},
		{"unpushed counted after unknown", cleanup.Losses{Unpushed: -1}, cleanup.Losses{Unpushed: 2}, true},
		{"unpushed zero after unknown", cleanup.Losses{Unpushed: -1}, cleanup.Losses{}, false},
		{"detached unique", base, with(base, func(l *cleanup.Losses) { l.DetachedUnique = true }), true},
		{"gates unknown", base, with(base, func(l *cleanup.Losses) { l.GatesUnknown = true }), true},
		{"zero saw, any loss", cleanup.Losses{}, cleanup.Losses{IgnoredEntries: 1, IgnoredBytes: 1}, true},
		{"zero saw, none", cleanup.Losses{}, cleanup.Losses{}, false},
	} {
		if got := lossesWorse(c.fresh, c.saw); got != c.worse {
			t.Errorf("%s: lossesWorse(%+v, saw %+v) = %v, want %v", c.name, c.fresh, c.saw, got, c.worse)
		}
	}
}

func with(l cleanup.Losses, f func(*cleanup.Losses)) cleanup.Losses { f(&l); return l }

// TestEngineRechecksPathAtDeletion has the rescan return a row for another
// path (a bug or a race in what Rescan matched): right before git runs the
// engine re-validates the fresh path and requires it to be the item's, and
// refuses otherwise, so git is never handed a path the user did not tick.
func TestEngineRechecksPathAtDeletion(t *testing.T) {
	for _, c := range []struct {
		name, want string
		path       func(f *fixture, other string) string
	}{
		{"another worktree", "path changed", func(_ *fixture, other string) string { return other }},
		{"the filesystem root", "invalid target", func(*fixture, string) string { return "/" }},
	} {
		t.Run(c.name, func(t *testing.T) {
			noGh(t)
			f := newFixture(t)
			p := f.Worktree("topic")
			other := f.Worktree("topic2")
			s, r := f.report()
			stubRescan(t, func(g cleanup.Git, prior cleanup.Snapshot, in cleanup.GatherInputs, o cleanup.Options, tg cleanup.Target) (cleanup.Row, error) {
				row, err := cleanup.Rescan(g, prior, in, o, tg)
				row.Path = c.path(f, other)
				return row, err
			})
			calls := recordRemoves(t)
			o := one(t, f.engine(s, f.inputs(), Options{}), approve(itemOf(f.row(r, cleanup.KindWorktree, "topic")), true, false))
			if o.Worktree != WtRefused || !strings.Contains(o.Reason, c.want) {
				t.Fatalf("outcome %+v, want refused: %s", o, c.want)
			}
			if len(*calls) != 0 || !exists(t, p) || !exists(t, other) {
				t.Errorf("git calls %q; want none and both worktrees kept", *calls)
			}
		})
	}
}

// TestEngineChecksProcessesBeforeRescan keeps the slow process check out of
// the window between the last fresh read and git: it must run before the
// rescan, and its warning still reaches a removed item's outcome.
func TestEngineChecksProcessesBeforeRescan(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	f.Worktree("topic")
	s, r := f.report()
	var order []string
	stubRescan(t, func(g cleanup.Git, prior cleanup.Snapshot, in cleanup.GatherInputs, o cleanup.Options, tg cleanup.Target) (cleanup.Row, error) {
		order = append(order, "rescan")
		return cleanup.Rescan(g, prior, in, o, tg)
	})
	pf := orderProcs{order: &order}
	o := one(t, f.engine(s, f.inputs(), Options{Procs: pf}), itemOf(f.row(r, cleanup.KindWorktree, "topic")))
	if strings.Join(order, ",") != "procs,rescan" {
		t.Errorf("order %v, want procs then rescan", order)
	}
	if o.Worktree != WtRemoved || len(o.Warnings) != 1 || !strings.Contains(o.Warnings[0], "1 process running") {
		t.Errorf("outcome %+v, want removed with the process warning", o)
	}
}

type orderProcs struct{ order *[]string }

func (p orderProcs) InDir(string) ([]Proc, error) {
	*p.order = append(*p.order, "procs")
	return []Proc{{7, "vim"}}, nil
}

// confirmedDirtyPreserved is a dirty rescued original confirmed through the
// real Preview -> Confirmed path, with its engine.
func confirmedDirtyPreserved(t *testing.T, f *fixture) (rescued, Engine, Item) {
	t.Helper()
	rs := dirtyRescuedTopic(t, f)
	s, r := f.report()
	eng := f.engine(s, f.inputs(), Options{})
	it, err := previewOne(t, eng, ItemFromRow(dirtyPreservedRow(t, f, r))).Confirmed(Needs{Confirm: true})
	if err != nil {
		t.Fatal(err)
	}
	return rs, eng, it
}

// secondRescan stubs rescan: call 1 (the gate) is real, call 2 (the re-check
// right before git) returns second(); it fails the test on a third call.
func secondRescan(t *testing.T, second func(g cleanup.Git, prior cleanup.Snapshot, in cleanup.GatherInputs, o cleanup.Options, tg cleanup.Target) (cleanup.Row, error)) *int {
	t.Helper()
	n := 0
	stubRescan(t, func(g cleanup.Git, prior cleanup.Snapshot, in cleanup.GatherInputs, o cleanup.Options, tg cleanup.Target) (cleanup.Row, error) {
		n++
		switch n {
		case 1:
			return cleanup.Rescan(g, prior, in, o, tg)
		case 2:
			return second(g, prior, in, o, tg)
		}
		t.Errorf("rescan call %d, want 2", n)
		return cleanup.Rescan(g, prior, in, o, tg)
	})
	return &n
}

// TestPreReCheckCatchesEditAfterGate edits a covered file after the gate's
// rescan but before the re-check right before git (stubbed: call 1 real,
// call 2 preceded by the edit): the item is skipped_changed with the re-check
// reason and the fresh loss lines, and git never runs. Why: git is handed
// --force for a dirty original, and would delete an edit made in the gate's
// compare window; the last read before git must be the one that decides.
func TestPreReCheckCatchesEditAfterGate(t *testing.T) {
	f := newFixture(t)
	rs, eng, it := confirmedDirtyPreserved(t, f)
	n := secondRescan(t, func(g cleanup.Git, prior cleanup.Snapshot, in cleanup.GatherInputs, o cleanup.Options, tg cleanup.Target) (cleanup.Row, error) {
		f.write(rs.Src, "topic.txt", "edited after the gate")
		return cleanup.Rescan(g, prior, in, o, tg)
	})
	calls := recordRemoves(t)
	o := one(t, eng, it)
	const reason = "changed while being checked: the rescue record no longer covers the uncommitted changes"
	if o.Worktree != WtSkippedChanged || o.Reason != reason || o.Branch != BrNotAttempted {
		t.Fatalf("outcome %s/%s %q, want skipped_changed/not_attempted %q", o.Worktree, o.Branch, o.Reason, reason)
	}
	if !slices.ContainsFunc(o.LossLines, func(l string) bool { return strings.Contains(l, "uncommitted") }) {
		t.Errorf("loss lines %q, want the fresh uncommitted-changes line", o.LossLines)
	}
	if *n != 2 || len(*calls) != 0 {
		t.Errorf("%d rescans, git calls %q; want 2 and none", *n, *calls)
	}
	if b, err := os.ReadFile(filepath.Join(rs.Src, "topic.txt")); err != nil || string(b) != "edited after the gate" {
		t.Errorf("the edit is gone: %q %v", b, err)
	}
}

// TestPreReCheckErrorIsRefused has the re-check right before git fail: the
// item is refused ("could not re-check: ...") and git never runs. Why: a
// re-check that cannot be made is not a pass (fail closed), and the gate
// treats a rescan error the same way.
func TestPreReCheckErrorIsRefused(t *testing.T) {
	f := newFixture(t)
	rs, eng, it := confirmedDirtyPreserved(t, f)
	secondRescan(t, func(cleanup.Git, cleanup.Snapshot, cleanup.GatherInputs, cleanup.Options, cleanup.Target) (cleanup.Row, error) {
		return cleanup.Row{}, errors.New("listing broke")
	})
	calls := recordRemoves(t)
	o := one(t, eng, it)
	if o.Worktree != WtRefused || o.Reason != "could not re-check: listing broke" || o.Branch != BrNotAttempted {
		t.Fatalf("outcome %s/%s %q, want refused: could not re-check: listing broke", o.Worktree, o.Branch, o.Reason)
	}
	if len(*calls) != 0 || !exists(t, rs.Src) {
		t.Errorf("git calls %q; want none and the original kept", *calls)
	}
}

// TestPreReCheckTargetGoneIsSkipped has the re-check right before git find
// the target gone (ErrTargetGone): skipped_changed "no longer exists", as at
// the gate, and git never runs. Why: a worktree that vanished in the window
// changed since the user confirmed it; there is nothing to approve.
func TestPreReCheckTargetGoneIsSkipped(t *testing.T) {
	f := newFixture(t)
	_, eng, it := confirmedDirtyPreserved(t, f)
	secondRescan(t, func(cleanup.Git, cleanup.Snapshot, cleanup.GatherInputs, cleanup.Options, cleanup.Target) (cleanup.Row, error) {
		return cleanup.Row{}, cleanup.ErrTargetGone
	})
	calls := recordRemoves(t)
	o := one(t, eng, it)
	if o.Worktree != WtSkippedChanged || o.Reason != "no longer exists" || o.Branch != BrNotAttempted {
		t.Fatalf("outcome %s/%s %q, want skipped_changed: no longer exists", o.Worktree, o.Branch, o.Reason)
	}
	if len(*calls) != 0 {
		t.Errorf("git calls %q, want none", *calls)
	}
}

// TestPreReCheckMismatches feeds the re-check right before git a row that
// differs from the gate's in one way each (the record dropped, its dirt no
// longer covered, another record ref or commit, the tip moved, a blocker,
// in use, unreadable gates, a worse status, a worse loss): each is
// skipped_changed with the re-check reason and git never runs. Why: each is
// a way the tree or the record changed in the window, and --force would
// remove what changed.
func TestPreReCheckMismatches(t *testing.T) {
	for _, c := range []struct {
		name string
		edit func(r *cleanup.Row)
	}{
		{"record dropped", func(r *cleanup.Row) { r.Preserved = nil }},
		{"dirt no longer covered", func(r *cleanup.Row) { p := *r.Preserved; p.CoversDirt = false; r.Preserved = &p }},
		{"another record ref", func(r *cleanup.Row) { p := *r.Preserved; p.Ref += "0"; r.Preserved = &p }},
		{"another record commit", func(r *cleanup.Row) { p := *r.Preserved; p.Commit = strings.Repeat("e", 40); r.Preserved = &p }},
		{"tip moved", func(r *cleanup.Row) { r.Tip = strings.Repeat("e", 40) }},
		{"blocker", func(r *cleanup.Row) { r.Blockers = []cleanup.Reason{{Code: cleanup.BlockLocked}} }},
		{"in use", func(r *cleanup.Row) { r.Status = cleanup.StatusInUse }},
		{"gates unknown", func(r *cleanup.Row) { r.Losses.GatesUnknown = true }},
		{"worse status", func(r *cleanup.Row) { r.Status = cleanup.StatusUniqueWork }},
		{"worse loss", func(r *cleanup.Row) { r.Losses.IgnoredEntries++ }},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			rs, eng, it := confirmedDirtyPreserved(t, f)
			secondRescan(t, func(g cleanup.Git, prior cleanup.Snapshot, in cleanup.GatherInputs, o cleanup.Options, tg cleanup.Target) (cleanup.Row, error) {
				r, err := cleanup.Rescan(g, prior, in, o, tg)
				c.edit(&r)
				return r, err
			})
			calls := recordRemoves(t)
			o := one(t, eng, it)
			if o.Worktree != WtSkippedChanged || o.Reason != "changed while being checked: the rescue record no longer covers the uncommitted changes" {
				t.Fatalf("outcome %s %q, want skipped_changed by the re-check", o.Worktree, o.Reason)
			}
			if len(*calls) != 0 || !exists(t, rs.Src) {
				t.Errorf("git calls %q; want none and the original kept", *calls)
			}
		})
	}
}
