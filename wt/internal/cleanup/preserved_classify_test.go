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

// dirtyPreservedWT is preservedWT with an untracked file and a record whose
// snapshot holds it, so Gather reads the worktree's dirt as covered.
func dirtyPreservedWT(t *testing.T, o recordOpts) (*fixture, string, string) {
	t.Helper()
	f, wt := preservedWT(t)
	f.DirtyIn(wt, "scratch.txt")
	o.Snapshot = true
	ref := f.addRecord(t, wt, o)
	return f, wt, ref
}

// wantDirtCovered checks a row's dirt reads covered: Preserved with
// CoversDirt, the raw Dirty kept and the effective one cleared.
func wantDirtCovered(t *testing.T, r Row) {
	t.Helper()
	if r.Preserved == nil || !r.Preserved.CoversDirt {
		t.Fatalf("Preserved = %+v, want CoversDirt", r.Preserved)
	}
	if !r.Losses.Dirty || r.EffectiveLosses().Dirty {
		t.Fatalf("raw Dirty %v effective Dirty %v, want true and false", r.Losses.Dirty, r.EffectiveLosses().Dirty)
	}
}

// TestClassifyDirtyCoveredBranchWithUnpushed rescues a dirty worktree with an
// unpushed commit and checks it reads less_sure, evidence rescued, with a soft
// preserved reason and neither dirty nor unpushed, its own branch proven, and
// the dirt kept raw but cleared in the effective view. Why: this is the PR B2
// case itself; covered dirt still needs --accept-loss, never --force.
func TestClassifyDirtyCoveredBranchWithUnpushed(t *testing.T) {
	f, _, _ := dirtyPreservedWT(t, recordOpts{})
	noGh(t)
	r := rowOf(t, gather(t, f), KindWorktree, "topic")
	wantRow(t, r, StatusLessSure, EvRescued, []string{ReasonPreserved}, true, true)
	wantDirtCovered(t, r)
	if r.Losses.Unpushed != 1 || r.EffectiveLosses().Unpushed != 0 {
		t.Fatalf("Unpushed raw %d effective %d, want 1 and 0", r.Losses.Unpushed, r.EffectiveLosses().Unpushed)
	}
}

// TestClassifyDirtyCoveredMergedBranch rescues a dirty worktree whose branch
// is an ancestor of main, so the dirt is the only part the record must cover.
// Why: the ladder's evidence never reaches the rescued rung there, so without
// a separate attach step the dirt would be dropped from the reasons yet still
// counted raw, or vanish silently (Review Focus 2). The evidence stays ancestor.
func TestClassifyDirtyCoveredMergedBranch(t *testing.T) {
	f, wt := preservedWT(t)
	gitIn(t, wt, "push", "-u", "origin", "topic")
	f.FastForward("topic")
	f.Commit("later.txt", "later", "later")
	f.Push("main")
	f.DirtyIn(wt, "scratch.txt")
	f.addRecord(t, wt, recordOpts{Snapshot: true})
	noGh(t)
	r := rowOf(t, gather(t, f), KindWorktree, "topic")
	wantRow(t, r, StatusLessSure, EvAncestor, []string{ReasonPreserved}, true, true)
	wantDirtCovered(t, r)
}

// TestClassifyDirtyCoveredDetached rescues a dirty detached worktree, once at
// a commit the base holds and once on a commit only it has. Why: a detached
// row builds its evidence in worktree() itself; the dirt must be covered on
// both paths, and the unique HEAD read rescued only in the second.
func TestClassifyDirtyCoveredDetached(t *testing.T) {
	t.Run("no unique commits", func(t *testing.T) {
		f := newFixture(t)
		wt := f.Worktree("loose")
		gitIn(t, wt, "checkout", "--detach")
		f.DirtyIn(wt, "scratch.txt")
		f.addRecord(t, wt, recordOpts{Snapshot: true})
		noGh(t)
		r := rowOf(t, gather(t, f), KindWorktree, filepath.Base(wt))
		if r.Status != StatusLessSure || r.Evidence != EvSameCommit || r.Preserved == nil ||
			!slices.Contains(reasonCodes(r.Reasons), ReasonPreserved) || slices.Contains(reasonCodes(r.Reasons), ReasonDirty) {
			t.Fatalf("row = %s ev %q reasons %v preserved %+v", r.Status, r.Evidence, reasonCodes(r.Reasons), r.Preserved)
		}
		wantDirtCovered(t, r)
	})
	t.Run("unique commits", func(t *testing.T) {
		f := newFixture(t)
		wt := f.Worktree("loose")
		f.DetachOrphan(wt)
		f.DirtyIn(wt, "scratch.txt")
		f.addRecord(t, wt, recordOpts{Snapshot: true})
		noGh(t)
		r := rowOf(t, gather(t, f), KindWorktree, filepath.Base(wt))
		wantRow(t, r, StatusLessSure, EvRescued, []string{ReasonPreserved}, false, true)
		wantDirtCovered(t, r)
		if !r.Losses.DetachedUnique || r.EffectiveLosses().DetachedUnique {
			t.Fatal("DetachedUnique: want raw true, effective false")
		}
	})
}

// TestClassifyDirtyCoveredDoesNotOverride walks covered dirt beside every
// other thing that lowers a row: an open PR, an unknown PR lookup, ignored
// files, a local base, the default branch, a hard blocker, and a HEAD other
// than the record's under a matching branch tip. Why: the record
// covers the dirt and commits only; every other reason must still decide the
// status, and a default-branch or blocked row gets no Preserved at all
// (Review Focus 4).
func TestClassifyDirtyCoveredDoesNotOverride(t *testing.T) {
	cases := []struct {
		name      string
		setup     func(t *testing.T, f *fixture, wt string)
		mut       func(t *testing.T, s Snapshot, wt string) Snapshot
		status    Status
		codes     []string
		preserved bool
	}{
		{"open PR", func(t *testing.T, f *fixture, wt string) {
			stubGhBase(t, func(string, string) ([]byte, error) { return []byte("[]"), nil })
			body := mustJSON(t, []any{prJSON(7, "OPEN", "o/r")})
			stubGh(t, func(_, b string) ([]byte, error) {
				if b == "topic" {
					return body, nil
				}
				return []byte("[]"), nil
			})
		}, nil, StatusUniqueWork, []string{ReasonOpenPR, ReasonPreserved}, true},
		{"PR lookup unknown", func(t *testing.T, f *fixture, wt string) {
			stubGhBase(t, func(string, string) ([]byte, error) { return []byte("[]"), nil })
			stubGh(t, func(string, string) ([]byte, error) { return nil, errors.New("gh down") })
		}, nil, StatusLessSure, []string{ReasonPRUnknown, ReasonPreserved}, true},
		{"ignored file", func(t *testing.T, f *fixture, wt string) {
			noGh(t)
			f.IgnoredIn(wt, "junk.log")
		}, nil, StatusLessSure, []string{ReasonIgnoredFiles, ReasonPreserved}, true},
		{"base is local", func(t *testing.T, f *fixture, wt string) { noGh(t) },
			func(t *testing.T, s Snapshot, wt string) Snapshot {
				s.Base = Base{Default: "main", Ref: "main", Fallback: true}
				return s
			}, StatusLessSure, []string{ReasonPreserved, ReasonBaseIsLocal}, true},
		{"default branch", func(t *testing.T, f *fixture, wt string) { noGh(t) },
			func(t *testing.T, s Snapshot, wt string) Snapshot {
				s.Base.Default = "topic"
				return s
			}, StatusInUse, []string{ReasonDirty}, false},
		{"hard blocker", func(t *testing.T, f *fixture, wt string) { noGh(t) },
			func(t *testing.T, s Snapshot, wt string) Snapshot {
				// Gather never matches a locked worktree; force the covering
				// record on to check the classifier refuses it too.
				return withPreserved(t, s, wt, preservedOfState(t, s, wt), func(ws *WorktreeState) {
					ws.Gates.Blockers = append(ws.Gates.Blockers, Reason{Code: BlockLocked})
				})
			}, StatusInUse, []string{ReasonDirty, ReasonUnpushed}, false},
		{"HEAD is not the record's head", func(t *testing.T, f *fixture, wt string) { noGh(t) },
			func(t *testing.T, s Snapshot, wt string) Snapshot {
				// The branch tip still equals the record's head; only the
				// worktree's HEAD differs, which must undo the whole cover.
				return withPreserved(t, s, wt, preservedOfState(t, s, wt), func(ws *WorktreeState) {
					ws.Fact.Head = gitIn(t, wt, "rev-parse", "main")
				})
			}, StatusUniqueWork, []string{ReasonDirty, ReasonUnpushed}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, wt, _ := dirtyPreservedWT(t, recordOpts{})
			c.setup(t, f, wt)
			s := gather(t, f)
			if p := preservedOfState(t, s, wt); p == nil || !p.CoversDirt {
				t.Fatalf("control: Gather's Preserved = %+v, want CoversDirt", p)
			}
			if c.mut != nil {
				s = c.mut(t, s, wt)
			}
			r := rowOf(t, s, KindWorktree, "topic")
			if r.Status != c.status || !slices.Equal(reasonCodes(r.Reasons), c.codes) || (r.Preserved != nil) != c.preserved {
				t.Fatalf("row = %s reasons %v preserved %+v, want %s %v preserved %v",
					r.Status, reasonCodes(r.Reasons), r.Preserved, c.status, c.codes, c.preserved)
			}
			if !c.preserved && !r.EffectiveLosses().Dirty {
				t.Fatal("effective Dirty cleared on a row with no Preserved")
			}
		})
	}
}

// TestClassifyDirtyNotCoveredUnchanged edits a file after the record was
// written. Why: the record no longer holds the dirt, so the row must read
// exactly as with no record: unique_work, dirty, no Preserved.
func TestClassifyDirtyNotCoveredUnchanged(t *testing.T) {
	f, wt, _ := dirtyPreservedWT(t, recordOpts{})
	f.DirtyIn(wt, "later.txt")
	noGh(t)
	r := rowOf(t, gather(t, f), KindWorktree, "topic")
	wantRow(t, r, StatusUniqueWork, "", []string{ReasonDirty, ReasonUnpushed}, false, false)
	if !r.EffectiveLosses().Dirty {
		t.Fatal("effective Dirty cleared")
	}
}

// TestEffectiveLossesClearsDirtOnlyWhenCovered builds rows by hand and checks
// the view clears Dirty only for a Preserved with CoversDirt, and never
// touches GatesUnknown, the ignored files or an uncounted Unpushed. Why: the
// removal engine's approvals read this view; clearing too much would let
// --accept-loss stand in for --force or hide a loss the record does not hold.
func TestEffectiveLossesClearsDirtOnlyWhenCovered(t *testing.T) {
	raw := Losses{Dirty: true, IgnoredEntries: 3, IgnoredBytes: 10, Unpushed: -1, GatesUnknown: true}
	cases := []struct {
		name string
		p    *Preserved
		want bool // effective Dirty
	}{
		{"no record", nil, true},
		{"record of commits only", &Preserved{Ref: "r"}, true},
		{"record covering the dirt", &Preserved{Ref: "r", CoversDirt: true}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Row{Losses: raw, Preserved: c.p}
			got := r.EffectiveLosses()
			want := raw
			want.Dirty = c.want
			if got != want {
				t.Fatalf("EffectiveLosses = %+v, want %+v", got, want)
			}
			if r.Losses != raw {
				t.Fatalf("raw Losses changed: %+v", r.Losses)
			}
		})
	}
}

// TestPreservedDetailMentionsDirtOnlyWhenCovered compares the preserved
// reason of a dirty covered row with a clean one's. Why: the reason is the
// only place a reader learns the uncommitted changes are in the record too;
// a clean row must not claim it.
func TestPreservedDetailMentionsDirtOnlyWhenCovered(t *testing.T) {
	f, _, ref := dirtyPreservedWT(t, recordOpts{DestPath: "/tmp/copy"})
	noGh(t)
	r := rowOf(t, gather(t, f), KindWorktree, "topic")
	want := "preserved by " + ref + " (copy: /tmp/copy), including its uncommitted changes"
	if r.Reasons[0].Code != ReasonPreserved || r.Reasons[0].Detail != want {
		t.Fatalf("reason = %+v, want detail %q", r.Reasons[0], want)
	}
	if got := ReasonText(r.Reasons[0].Code, r.Reasons[0].Detail); got != want {
		t.Fatalf("ReasonText = %q, want the detail alone", got)
	}

	f2, wt2 := preservedWT(t)
	ref2 := f2.addRecord(t, wt2, recordOpts{DestPath: "/tmp/copy"})
	clean := rowOf(t, gather(t, f2), KindWorktree, "topic")
	if d := clean.Reasons[0].Detail; d != "preserved by "+ref2+" (copy: /tmp/copy)" {
		t.Fatalf("clean detail = %q", d)
	}
}

// TestRowTickableWhenDirtCovered checks a covered dirty row reads less_sure
// with no blocker and sits in the less_sure group, and that Tickable stays
// false for it (Tickable is proven-only), so it is never ticked by default or
// by select-all. Why: covered dirt is a loss the user must accept, never one
// ticked for them.
func TestRowTickableWhenDirtCovered(t *testing.T) {
	f, _, _ := dirtyPreservedWT(t, recordOpts{})
	noGh(t)
	rep := Classify(gather(t, f), Options{Now: later()})
	r := findRow(t, rep, KindWorktree, "topic")
	if r.Status != StatusLessSure || len(r.Blockers) != 0 {
		t.Fatalf("row = %s blockers %v, want less_sure with none", r.Status, r.Blockers)
	}
	if r.Tickable() {
		t.Fatal("a less_sure row is ticked by default")
	}
	for _, g := range Groups(rep) {
		for _, gr := range g.Rows {
			if gr.Kind == KindWorktree && gr.Name == "topic" && g.Status != StatusLessSure {
				t.Fatalf("row in group %s", g.Status)
			}
		}
	}
}

// TestClassifyDirtyCoveredDetachedWithoutAProof pins a detached oddity: a
// dirty detached worktree on a commit only it has (DetachedUnique), its dirt
// covered by a record, whose merge proof is Undetermined rather than
// NotMerged. detachedPreserved needs NotMerged, so the HEAD is not read
// rescued: detached_unique and the unavailable proof stay and the row stays
// unique_work. The attach step still gives it Preserved (the dirt is
// covered), and the effective view then clears DetachedUnique as well as
// Dirty. Why that is truthful: preservedOf only matched the record after
// recordCovers proved the HEAD reachable from the record ref, so the record
// does hold the commit; and it is conservative, because the status and its
// reasons still come from the ladder, so nothing reads proven or less_sure
// on the record's say-so, and the user still has to accept the item.
func TestClassifyDirtyCoveredDetachedWithoutAProof(t *testing.T) {
	f := newFixture(t)
	wt := f.Worktree("loose")
	f.DetachOrphan(wt)
	f.DirtyIn(wt, "scratch.txt")
	f.addRecord(t, wt, recordOpts{Snapshot: true})
	noGh(t)
	s := gather(t, f)
	head := gitIn(t, wt, "rev-parse", "HEAD")
	p, ok := s.Proofs[head]
	if !ok || p.Verdict != NotMerged {
		t.Fatalf("control: proof %+v (found %v), want NotMerged", p, ok)
	}
	p.Verdict = Undetermined
	s.Proofs[head] = p
	r := rowOf(t, s, KindWorktree, filepath.Base(wt))
	wantRow(t, r, StatusUniqueWork, "", []string{ReasonDetachedUnique, ReasonMergeProofUnavailable, ReasonPreserved}, false, true)
	wantDirtCovered(t, r)
	if !r.Losses.DetachedUnique || r.EffectiveLosses().DetachedUnique {
		t.Fatal("DetachedUnique: want raw true, effective false")
	}
}

// TestClassifyDirtyCoveredOpenPRClearsUnpushed rescues a dirty worktree with
// an unpushed commit whose branch has an open PR: the PR keeps the row
// unique_work, while Preserved is attached, the dirt reads covered and the
// effective Unpushed is 0 (raw 1). Why: the record holds both the commit and
// the dirt, so neither is a loss, but an open PR is a reason of its own that
// the record must not override (Review Focus 4).
func TestClassifyDirtyCoveredOpenPRClearsUnpushed(t *testing.T) {
	f, _, _ := dirtyPreservedWT(t, recordOpts{})
	stubGhBase(t, func(string, string) ([]byte, error) { return []byte("[]"), nil })
	body := mustJSON(t, []any{prJSON(7, "OPEN", "o/r")})
	stubGh(t, func(_, b string) ([]byte, error) {
		if b == "topic" {
			return body, nil
		}
		return []byte("[]"), nil
	})
	r := rowOf(t, gather(t, f), KindWorktree, "topic")
	if r.Status != StatusUniqueWork || !slices.Equal(reasonCodes(r.Reasons), []string{ReasonOpenPR, ReasonPreserved}) || r.Preserved == nil {
		t.Fatalf("row = %s reasons %v preserved %+v, want unique_work [open_pr preserved] with Preserved",
			r.Status, reasonCodes(r.Reasons), r.Preserved)
	}
	wantDirtCovered(t, r)
	if r.Losses.Unpushed != 1 || r.EffectiveLosses().Unpushed != 0 {
		t.Fatalf("unpushed raw %d effective %d, want 1 and 0", r.Losses.Unpushed, r.EffectiveLosses().Unpushed)
	}
}
