package cleanup

import (
	"errors"
	"maps"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

// rowOf classifies s with a clock an hour ahead and returns the row of kind k
// named name.
func rowOf(t *testing.T, s Snapshot, k Kind, name string) Row {
	t.Helper()
	return findRow(t, Classify(s, Options{Now: later()}), k, name)
}

// withPreserved returns a copy of s whose worktree at path has Preserved set
// to p (and, when mut is not nil, its state changed by mut).
func withPreserved(t *testing.T, s Snapshot, path string, p *Preserved, mut func(*WorktreeState)) Snapshot {
	t.Helper()
	s.Worktrees = slices.Clone(s.Worktrees)
	for i := range s.Worktrees {
		if resolve(s.Worktrees[i].Fact.Path) == resolve(path) {
			s.Worktrees[i].Preserved = p
			if mut != nil {
				mut(&s.Worktrees[i])
			}
			return s
		}
	}
	t.Fatalf("no worktree %s", path)
	return s
}

// preservedOfState returns the Preserved Gather set on the worktree at path.
func preservedOfState(t *testing.T, s Snapshot, path string) *Preserved {
	t.Helper()
	for _, ws := range s.Worktrees {
		if resolve(ws.Fact.Path) == resolve(path) {
			return ws.Preserved
		}
	}
	t.Fatalf("no worktree %s", path)
	return nil
}

// wantRow checks a row's status, evidence, reason codes, BranchProven and
// whether Preserved is set.
func wantRow(t *testing.T, r Row, st Status, ev Evidence, codes []string, branchProven, preserved bool) {
	t.Helper()
	if r.Status != st || r.Evidence != ev || !slices.Equal(reasonCodes(r.Reasons), codes) ||
		r.BranchProven != branchProven || (r.Preserved != nil) != preserved {
		t.Fatalf("row = status %s ev %q reasons %v branchProven %v preserved %+v\nwant status %s ev %q reasons %v branchProven %v preserved %v",
			r.Status, r.Evidence, reasonCodes(r.Reasons), r.BranchProven, r.Preserved,
			st, ev, codes, branchProven, preserved)
	}
}

// TestPreservedRowIsProvenRescued is the positive case: a clean branch
// worktree whose unpushed commits a matching record covers reads proven with
// evidence rescued, the informational preserved reason, and its own branch
// proven; the raw unpushed count is kept. Why: this is the whole point of the
// signal, and the raw Losses must stay for the deliberate raw readers.
func TestPreservedRowIsProvenRescued(t *testing.T) {
	f, wt := preservedWT(t)
	ref := f.addRecord(t, wt, recordOpts{DestPath: "/tmp/copy"})
	noGh(t)
	r := rowOf(t, gather(t, f), KindWorktree, "topic")
	wantRow(t, r, StatusProven, EvRescued, []string{ReasonPreserved}, true, true)
	if r.Losses.Unpushed != 1 {
		t.Fatalf("raw Losses.Unpushed = %d, want 1", r.Losses.Unpushed)
	}
	if want := "preserved by " + ref + " (copy: /tmp/copy)"; r.Reasons[0].Detail != want {
		t.Fatalf("detail = %q, want %q", r.Reasons[0].Detail, want)
	}
	if r.Preserved.Ref != ref || r.Preserved.SourceHead != r.Tip {
		t.Fatalf("Preserved = %+v", r.Preserved)
	}
	if got := ReasonText(r.Reasons[0].Code, r.Reasons[0].Detail); got != r.Reasons[0].Detail {
		t.Fatalf("ReasonText = %q, want the detail alone", got)
	}
	if got := evidenceText(r, ""); got != "not merged, a wt rescue record holds its commits" {
		t.Fatalf("evidenceText = %q", got)
	}
}

// TestPreservedDetailIsFlattened writes runs of blanks into the record's
// destination path and hands the classifier a destination holding a control
// sequence and a newline. Why: the trailers are untrusted text, and the reason
// is printed on one line by every sink.
func TestPreservedDetailIsFlattened(t *testing.T) {
	f, wt := preservedWT(t)
	ref := f.addRecord(t, wt, recordOpts{DestPath: "/tmp/a   b"})
	noGh(t)
	s := gather(t, f)
	r := rowOf(t, s, KindWorktree, "topic")
	if r.Preserved == nil {
		t.Fatal("not preserved")
	}
	if want := "preserved by " + ref + " (copy: /tmp/a b)"; r.Reasons[0].Detail != want {
		t.Fatalf("detail = %q, want %q", r.Reasons[0].Detail, want)
	}
	p := *r.Preserved
	p.DestPath = "/tmp/x\x1b[31m\ny"
	r = rowOf(t, withPreserved(t, s, wt, &p, nil), KindWorktree, "topic")
	if d := r.Reasons[0].Detail; d != flat(d) || d == "" {
		t.Fatalf("detail %q is not flat", d)
	}
}

// TestPreservedStatusMatrix walks the cases where a preserved row keeps every
// other check: ignored files, a local base, an unknown PR lookup and an open
// PR. Why: preserved covers only the commits; anything else that today lowers
// the row must still lower it (Review Focus 2 and 3).
func TestPreservedStatusMatrix(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(t *testing.T, f *fixture, wt string)
		mut     func(s *Snapshot)
		status  Status
		codes   []string
		bProven bool
	}{
		{"ignored file", func(t *testing.T, f *fixture, wt string) {
			noGh(t)
			f.IgnoredIn(wt, "junk.log")
		}, nil, StatusLessSure, []string{ReasonIgnoredFiles, ReasonPreserved}, true},
		{"base is local", func(t *testing.T, f *fixture, wt string) { noGh(t) },
			func(s *Snapshot) { s.Base = Base{Default: "main", Ref: "main", Fallback: true} },
			StatusLessSure, []string{ReasonBaseIsLocal, ReasonPreserved}, false},
		{"PR lookup unknown", func(t *testing.T, f *fixture, wt string) {
			stubGhBase(t, func(string, string) ([]byte, error) { return []byte("[]"), nil })
			stubGh(t, func(string, string) ([]byte, error) { return nil, errors.New("gh down") })
		}, nil, StatusLessSure, []string{ReasonPRUnknown, ReasonPreserved}, false},
		{"open PR", func(t *testing.T, f *fixture, wt string) {
			stubGhBase(t, func(string, string) ([]byte, error) { return []byte("[]"), nil })
			body := mustJSON(t, []any{prJSON(7, "OPEN", "o/r")})
			stubGh(t, func(_, b string) ([]byte, error) {
				if b == "topic" {
					return body, nil
				}
				return []byte("[]"), nil
			})
		}, nil, StatusUniqueWork, []string{ReasonOpenPR, ReasonPreserved}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, wt := preservedWT(t)
			f.addRecord(t, wt, recordOpts{})
			c.setup(t, f, wt)
			s := gather(t, f)
			if c.mut != nil {
				c.mut(&s)
			}
			r := rowOf(t, s, KindWorktree, "topic")
			wantRow(t, r, c.status, EvRescued, c.codes, c.bProven, true)
			if r.Losses.Unpushed != 1 {
				t.Fatalf("raw Losses.Unpushed = %d, want 1", r.Losses.Unpushed)
			}
		})
	}
}

// TestPreservedNotAppliedLeavesTheRowAsToday forces a Preserved value onto
// worktrees where it must not apply (dirty, unreadable gates, an unknown
// unpushed count, a fresh tip other than the record's head, a merged branch)
// and compares the row with the one built with no Preserved at all. Why: the
// classifier is the last guard; when it does not apply the signal, the row
// must be byte-identical to today's.
func TestPreservedNotAppliedLeavesTheRowAsToday(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, f *fixture, wt string)
		mut   func(t *testing.T, s Snapshot, wt string) (Snapshot, func(*WorktreeState))
		check func(t *testing.T, r Row)
	}{
		{"dirty", func(t *testing.T, f *fixture, wt string) { f.DirtyIn(wt, "scratch.txt") }, nil,
			func(t *testing.T, r Row) {
				wantRow(t, r, StatusUniqueWork, "", []string{ReasonDirty, ReasonUnpushed}, false, false)
			}},
		{"gates error", nil, func(t *testing.T, s Snapshot, wt string) (Snapshot, func(*WorktreeState)) {
			return s, func(ws *WorktreeState) { ws.GatesErr = "boom" }
		}, func(t *testing.T, r Row) {
			wantRow(t, r, StatusUniqueWork, "", []string{ReasonUnpushed, ReasonGatesUnavailable}, false, false)
		}},
		{"unpushed unknown", nil, func(t *testing.T, s Snapshot, wt string) (Snapshot, func(*WorktreeState)) {
			s.Proofs = maps.Clone(s.Proofs)
			tip := gitIn(t, wt, "rev-parse", "HEAD")
			p := s.Proofs[tip]
			p.Unpushed, p.UnpushedErr = -1, "count failed"
			s.Proofs[tip] = p
			return s, nil
		}, func(t *testing.T, r Row) {
			wantRow(t, r, StatusLessSure, "", []string{ReasonUnpushedUnknown}, false, false)
			if r.Losses.Unpushed != -1 {
				t.Fatalf("Losses.Unpushed = %d", r.Losses.Unpushed)
			}
		}},
		{"tip is not the record's head", nil, func(t *testing.T, s Snapshot, wt string) (Snapshot, func(*WorktreeState)) {
			return s, func(ws *WorktreeState) {
				p := *ws.Preserved
				p.SourceHead = gitIn(t, wt, "rev-parse", "main")
				ws.Preserved = &p
			}
		}, func(t *testing.T, r Row) {
			wantRow(t, r, StatusUniqueWork, "", []string{ReasonUnpushed}, false, false)
		}},
		{"merged branch", func(t *testing.T, f *fixture, wt string) {
			gitIn(t, wt, "push", "-u", "origin", "topic")
			f.FastForward("topic")
			f.Push("main")
		}, nil, func(t *testing.T, r Row) {
			if r.Preserved != nil || r.Evidence == EvRescued || r.Evidence == "" {
				t.Fatalf("row = %+v, want the ladder evidence and no Preserved", r)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, wt := preservedWT(t)
			if c.setup != nil {
				c.setup(t, f, wt)
			}
			noGh(t)
			s := gather(t, f)
			// A record of the worktree's present head, which Gather may or
			// may not have matched; the classifier must ignore it here.
			head := gitIn(t, wt, "rev-parse", "HEAD")
			forced := preservedOfState(t, s, wt)
			if forced == nil {
				forced = &Preserved{Ref: RecordRefPrefix + head[:16], Commit: head, SourceHead: head, DestBranch: "rescue/x", DestPath: "/tmp/dest"}
			}
			var mut func(*WorktreeState)
			if c.mut != nil {
				s, mut = c.mut(t, s, wt)
			}
			with := rowOf(t, withPreserved(t, s, wt, forced, mut), KindWorktree, "topic")
			without := rowOf(t, withPreserved(t, s, wt, nil, func(ws *WorktreeState) {
				if mut != nil {
					p := *forced
					ws.Preserved = &p
					mut(ws)
				}
				ws.Preserved = nil
			}), KindWorktree, "topic")
			if !reflect.DeepEqual(with, without) {
				t.Fatalf("Preserved changed the row:\n with    %+v\n without %+v", with, without)
			}
			c.check(t, with)
		})
	}
}

// TestPreservedDetachedRowIsProvenRescued rescues a detached worktree whose
// HEAD no branch or remote reaches. Why: a detached row builds its evidence
// in worktree() itself, not through local(); it must still drop
// detached_unique, read rescued and keep its raw DetachedUnique loss.
func TestPreservedDetachedRowIsProvenRescued(t *testing.T) {
	f := newFixture(t)
	wt := f.Worktree("loose")
	f.DetachOrphan(wt)
	f.addRecord(t, wt, recordOpts{})
	noGh(t)
	s := gather(t, f)
	r := rowOf(t, s, KindWorktree, filepath.Base(wt))
	wantRow(t, r, StatusProven, EvRescued, []string{ReasonPreserved}, false, true)
	if !r.Losses.DetachedUnique {
		t.Fatal("raw Losses.DetachedUnique cleared")
	}
	// The same worktree with no record reads as today.
	plain := rowOf(t, withPreserved(t, s, wt, nil, nil), KindWorktree, filepath.Base(wt))
	wantRow(t, plain, StatusUniqueWork, "", []string{ReasonDetachedUnique}, false, false)
	// A dirty detached worktree is not preserved even with a forced value.
	f.DirtyIn(wt, "scratch.txt")
	s = gather(t, f)
	forced := withPreserved(t, s, wt, r.Preserved, nil)
	with, without := rowOf(t, forced, KindWorktree, filepath.Base(wt)), rowOf(t, withPreserved(t, s, wt, nil, nil), KindWorktree, filepath.Base(wt))
	if !reflect.DeepEqual(with, without) || with.Preserved != nil {
		t.Fatalf("dirty detached row changed by Preserved:\n with    %+v\n without %+v", with, without)
	}
}

// TestPreservedNeverOnBranchRowOrDestination checks the branch left after
// its worktree is gone, and the rescue destination worktree on the same
// commits, are never preserved. Why: the record speaks for the source
// worktree only; the destination is the copy itself.
func TestPreservedNeverOnBranchRowOrDestination(t *testing.T) {
	t.Run("destination", func(t *testing.T) {
		f, wt := preservedWT(t)
		head := gitIn(t, wt, "rev-parse", "HEAD")
		dest := filepath.Join(filepath.Dir(f.Dir), "wt-rescued-x")
		f.git("worktree", "add", "-b", "rescued/x", dest, head)
		f.addRecord(t, wt, recordOpts{DestBranch: "rescued/x", DestPath: dest})
		noGh(t)
		rep := Classify(gather(t, f), Options{Now: later()})
		wantRow(t, findRow(t, rep, KindWorktree, "rescued/x"), StatusUniqueWork, "", []string{ReasonUnpushed}, false, false)
		wantRow(t, findRow(t, rep, KindWorktree, "topic"), StatusProven, EvRescued, []string{ReasonPreserved}, true, true)
	})
	t.Run("branch row", func(t *testing.T) {
		f, wt := preservedWT(t)
		f.addRecord(t, wt, recordOpts{})
		f.git("worktree", "remove", wt)
		noGh(t)
		r := rowOf(t, gather(t, f), KindBranch, "topic")
		wantRow(t, r, StatusUniqueWork, "", []string{ReasonUnpushed}, false, false)
	})
}
