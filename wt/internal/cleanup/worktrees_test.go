package cleanup

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// factFor lists the fixture's worktrees as seen from cwd and returns the one
// at path (compared after EvalSymlinks: t.TempDir is under /var, which git
// reports as /private/var on macOS).
func factFor(t *testing.T, f *fixture, cwd, path string) WorktreeFact {
	t.Helper()
	facts, err := ListWorktrees(Git{Dir: f.Dir}, cwd)
	if err != nil {
		t.Fatalf("ListWorktrees: %v", err)
	}
	want, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range facts {
		if got, err := filepath.EvalSymlinks(w.Path); err == nil && got == want {
			return w
		}
	}
	t.Fatalf("no worktree at %s in %+v", path, facts)
	return WorktreeFact{}
}

func codes(g Gates) []string {
	var out []string
	for _, r := range g.Blockers {
		out = append(out, r.Code)
	}
	return out
}

// TestListWorktreesParsesLockedPrunableDetached reads a repo with a locked
// worktree, a prunable one (its directory deleted) and a detached one. Why:
// the gates and rows trust these flags and the full branch ref; a misparsed
// -z record would let a locked worktree through or tie a row to the wrong
// branch.
func TestListWorktreesParsesLockedPrunableDetached(t *testing.T) {
	f := newFixture(t)
	locked := f.Worktree("topic")
	gitIn(t, f.Dir, "worktree", "lock", "--reason", "keep me", locked)
	gone := f.Worktree("gone")
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}
	det := f.Worktree("det")
	sha := f.DetachOrphan(det)

	facts, err := ListWorktrees(Git{Dir: f.Dir}, det)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 4 {
		t.Fatalf("want 4 worktrees, got %+v", facts)
	}
	if p := facts[0]; !p.Primary || p.Branch != "refs/heads/main" || p.Current || p.Detached {
		t.Errorf("primary: %+v", p)
	}
	for _, w := range facts[1:] {
		if w.Primary {
			t.Errorf("only the first entry is primary: %+v", w)
		}
	}
	l := factFor(t, f, det, locked)
	if !l.Locked || l.Prunable || l.Branch != "refs/heads/topic" || l.Current {
		t.Errorf("locked: %+v", l)
	}
	var g WorktreeFact
	for _, w := range facts {
		if filepath.Base(w.Path) == filepath.Base(gone) {
			g = w
		}
	}
	if !g.Prunable || g.Locked || g.Branch != "refs/heads/gone" {
		t.Errorf("prunable: %+v", g)
	}
	d := factFor(t, f, det, det)
	if !d.Detached || d.Branch != "" || d.Head != sha || !d.Current {
		t.Errorf("detached: %+v (want head %s)", d, sha)
	}
}

// TestListWorktreesCurrentFromSubdirectory marks the worktree that holds cwd
// even when cwd is a subdirectory of it. Why: running from deep inside a
// worktree must still block its removal.
func TestListWorktreesCurrentFromSubdirectory(t *testing.T) {
	f := newFixture(t)
	wt := f.Worktree("topic")
	sub := filepath.Join(wt, "deep", "er")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if w := factFor(t, f, sub, wt); !w.Current {
		t.Errorf("cwd %s is inside %s: %+v", sub, wt, w)
	}
	if w := factFor(t, f, sub, f.Dir); w.Current {
		t.Errorf("primary is not current: %+v", w)
	}
}

// TestGatesBlockers sets up one hard blocker per case on a linked worktree
// (or the primary) and checks CheckGates names exactly that code. Why: each
// blocker is a way removal would destroy something live (the repo, the
// shell's cwd, another session, a lock, a nested checkout, a half-done
// rebase, a model artifact); a missing one is data loss.
func TestGatesBlockers(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, f *fixture, wt string) (target, cwd string, in GateInputs)
		want  string
	}{
		{"primary", func(t *testing.T, f *fixture, wt string) (string, string, GateInputs) {
			return f.Dir, t.TempDir(), GateInputs{}
		}, "primary"},
		{"current", func(t *testing.T, f *fixture, wt string) (string, string, GateInputs) {
			return wt, wt, GateInputs{}
		}, "current"},
		{"held", func(t *testing.T, f *fixture, wt string) (string, string, GateInputs) {
			return wt, t.TempDir(), GateInputs{HeldBy: func(string) bool { return true }}
		}, "held"},
		{"locked", func(t *testing.T, f *fixture, wt string) (string, string, GateInputs) {
			gitIn(t, f.Dir, "worktree", "lock", wt)
			return wt, t.TempDir(), GateInputs{}
		}, "locked"},
		{"nested", func(t *testing.T, f *fixture, wt string) (string, string, GateInputs) {
			f.NestedWorktree(wt, "inner")
			return wt, t.TempDir(), GateInputs{}
		}, "nested"},
		{"protected artifact", func(t *testing.T, f *fixture, wt string) (string, string, GateInputs) {
			return wt, t.TempDir(), GateInputs{ProtectedPaths: []string{filepath.Join(wt, "models", "m")}}
		}, "protected_artifact"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			wt := f.Worktree("topic")
			target, cwd, in := c.setup(t, f, wt)
			in.Base = baseRef
			w := factFor(t, f, cwd, target)
			g, err := CheckGates(Git{Dir: f.Dir}, w, in)
			if err != nil {
				t.Fatalf("CheckGates: %v", err)
			}
			if got := codes(g); !slices.Equal(got, []string{c.want}) {
				t.Errorf("blockers = %v, want [%s] (%+v)", got, c.want, g.Blockers)
			}
		})
	}
}

// TestGatesOperationInProgress stops each kind of multi-step git operation
// midway in a linked worktree and checks the operation_in_progress blocker.
// Why: removing the worktree throws away the half-done operation (and, for a
// resumed multi-commit cherry-pick, the commits still queued in the
// sequencer); every kind leaves a different marker.
func TestGatesOperationInProgress(t *testing.T) {
	cases := []struct {
		name  string
		setup func(f *fixture, wt string)
	}{
		{"rebase", (*fixture).StoppedRebase},
		{"merge conflict", (*fixture).StoppedMerge},
		{"cherry-pick conflict", (*fixture).StoppedCherryPick},
		{"multi-commit cherry-pick resumed", (*fixture).ResumedCherryPick},
		{"revert conflict", (*fixture).StoppedRevert},
		{"bisect start", (*fixture).StartedBisect},
		{"am", (*fixture).StoppedAm},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			wt := f.Worktree("topic")
			c.setup(f, wt)
			w := factFor(t, f, t.TempDir(), wt)
			g, err := CheckGates(Git{Dir: f.Dir}, w, GateInputs{Base: baseRef})
			if err != nil {
				t.Fatalf("CheckGates: %v", err)
			}
			if !slices.Contains(codes(g), "operation_in_progress") {
				t.Errorf("blockers = %v, want operation_in_progress", g.Blockers)
			}
		})
	}
}

// TestGatesPrunableWithDirectoryFailsClosed removes a nested linked
// worktree's .git file but keeps its directory, inside the primary checkout.
// Why: git -C on that directory walks up into the enclosing repository and
// would report its status, ignored files and operation markers as this
// worktree's; the gate must be an error, never an answer about another repo.
func TestGatesPrunableWithDirectoryFailsClosed(t *testing.T) {
	f := newFixture(t)
	wt := filepath.Join(f.Dir, ".claude", "worktrees", "topic")
	f.git("worktree", "add", "-b", "topic", wt)
	if err := os.Remove(filepath.Join(wt, ".git")); err != nil {
		t.Fatal(err)
	}
	// The primary tracks the directory's file, so every git call there
	// succeeds and answers "clean" about the primary. (Left untracked,
	// `ls-files --directory` from inside it dies with "directory entry not
	// superset of prefix", which would fail closed by accident.)
	f.git("add", "-f", ".claude/worktrees/topic/README")
	f.git("commit", "-m", "track")
	w := factFor(t, f, t.TempDir(), wt)
	if !w.Prunable {
		t.Fatalf("want a prunable worktree: %+v", w)
	}
	if g, err := CheckGates(Git{Dir: f.Dir}, w, GateInputs{Base: baseRef}); err == nil {
		t.Errorf("want an error, got %+v", g)
	}
}

// TestGatesToplevelMismatchFailsClosed hands CheckGates a fact (not marked
// prunable) whose directory is not a worktree top level but sits inside the
// primary checkout. Why: whatever the porcelain says, the status checks must
// run in this worktree's own repository, never an enclosing one.
func TestGatesToplevelMismatchFailsClosed(t *testing.T) {
	f := newFixture(t)
	sub := filepath.Join(f.Dir, "sub")
	f.DirtyIn(f.Dir, "sub/x")
	w := WorktreeFact{Path: sub, Branch: "refs/heads/topic"}
	if g, err := CheckGates(Git{Dir: f.Dir}, w, GateInputs{Base: baseRef}); err == nil {
		t.Errorf("want an error, got %+v", g)
	}
}

// TestGatesHiddenIndexFlagsAreDirty edits a tracked file flagged
// skip-worktree or assume-unchanged. Why: status does not report those
// edits, so the worktree would read clean while removal destroys them;
// either flag counts as dirty (fail closed, sparse checkouts included).
func TestGatesHiddenIndexFlagsAreDirty(t *testing.T) {
	for _, flag := range []string{"--skip-worktree", "--assume-unchanged"} {
		t.Run(flag, func(t *testing.T) {
			f := newFixture(t)
			wt := f.Worktree("topic")
			gitIn(t, wt, "update-index", flag, "README")
			if err := os.WriteFile(filepath.Join(wt, "README"), []byte("edited"), 0o644); err != nil {
				t.Fatal(err)
			}
			if out := gitIn(t, wt, "status", "--porcelain"); out != "" {
				t.Fatalf("setup: status should hide the edit, got %q", out)
			}
			w := factFor(t, f, t.TempDir(), wt)
			g, err := CheckGates(Git{Dir: f.Dir}, w, GateInputs{Base: baseRef})
			if err != nil {
				t.Fatal(err)
			}
			if !g.Dirty {
				t.Errorf("edit hidden by %s: %+v", flag, g)
			}
		})
	}
}

// TestGatesNoBlockersOnPlainWorktree checks a clean linked worktree has no
// blockers and is not dirty. Why: the blocker table above only proves each
// gate fires; this proves none fires spuriously.
func TestGatesNoBlockersOnPlainWorktree(t *testing.T) {
	f := newFixture(t)
	wt := f.Worktree("topic")
	w := factFor(t, f, t.TempDir(), wt)
	g, err := CheckGates(Git{Dir: f.Dir}, w, GateInputs{Base: baseRef, HeldBy: func(string) bool { return false }})
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Blockers) != 0 || g.Dirty || g.IgnoredCount != 0 || g.DetachedUnique {
		t.Errorf("want no gates, got %+v", g)
	}
}

// TestGatesDirtyUsesPinnedStatus hides an untracked file behind
// status.showUntrackedFiles=no and an all-ignoring submodule setting. Why:
// user config must not make a worktree with unsaved work look clean; the
// status flags are pinned for that.
func TestGatesDirtyUsesPinnedStatus(t *testing.T) {
	f := newFixture(t)
	wt := f.Worktree("topic")
	f.git("config", "status.showUntrackedFiles", "no")
	f.git("config", "diff.ignoreSubmodules", "all")
	f.DirtyIn(wt, "notes.txt")
	w := factFor(t, f, t.TempDir(), wt)
	g, err := CheckGates(Git{Dir: f.Dir}, w, GateInputs{Base: baseRef})
	if err != nil {
		t.Fatal(err)
	}
	if !g.Dirty {
		t.Errorf("untracked file hidden by config: %+v", g)
	}
}

// TestGatesIgnoredFilesDemoteToLessSure puts an ignored .env in a linked
// worktree. Why: ignored files die with the directory, so they are counted
// and sized for the row (LESS SURE), but they are not unsaved work and must
// not make the worktree dirty.
func TestGatesIgnoredFilesDemoteToLessSure(t *testing.T) {
	f := newFixture(t)
	wt := f.Worktree("topic")
	f.IgnoredIn(wt, ".env")
	w := factFor(t, f, t.TempDir(), wt)
	g, err := CheckGates(Git{Dir: f.Dir}, w, GateInputs{Base: baseRef})
	if err != nil {
		t.Fatal(err)
	}
	if g.Dirty || g.IgnoredCount != 1 || g.IgnoredBytes != int64(len("dirty")) {
		t.Errorf("want clean with 1 ignored file of 5 bytes, got %+v", g)
	}
}

// TestGatesIgnoredDirectorySized counts an ignored directory once and sizes
// its contents. Why: --directory collapses .venv to one entry; its size must
// still be the files inside, not the directory inode.
func TestGatesIgnoredDirectorySized(t *testing.T) {
	f := newFixture(t)
	wt := f.Worktree("topic")
	f.IgnoredIn(wt, "venv/")
	if err := os.Remove(filepath.Join(wt, "venv")); err != nil { // IgnoredIn wrote a file there
		t.Fatal(err)
	}
	f.DirtyIn(wt, "venv/a")
	f.DirtyIn(wt, "venv/lib/b")
	w := factFor(t, f, t.TempDir(), wt)
	g, err := CheckGates(Git{Dir: f.Dir}, w, GateInputs{Base: baseRef})
	if err != nil {
		t.Fatal(err)
	}
	if g.Dirty || g.IgnoredCount != 1 || g.IgnoredBytes != 2*int64(len("dirty")) {
		t.Errorf("want 1 ignored dir of 10 bytes, got %+v", g)
	}
}

// TestGatesDetachedReachableVsUnique checks a detached HEAD on a commit the
// base reaches versus one on no branch. Why: the first loses nothing when
// removed and can be PROVEN; the second is UNIQUE WORK only that worktree
// holds.
func TestGatesDetachedReachableVsUnique(t *testing.T) {
	t.Run("reachable", func(t *testing.T) {
		f := newFixture(t)
		wt := f.Worktree("topic")
		gitIn(t, wt, "checkout", "--detach", "main")
		w := factFor(t, f, t.TempDir(), wt)
		g, err := CheckGates(Git{Dir: f.Dir}, w, GateInputs{Base: baseRef})
		if err != nil {
			t.Fatal(err)
		}
		if !w.Detached || g.DetachedUnique {
			t.Errorf("detached at a base ancestor: fact %+v gates %+v", w, g)
		}
	})
	t.Run("unique", func(t *testing.T) {
		f := newFixture(t)
		wt := f.Worktree("topic")
		f.DetachOrphan(wt)
		w := factFor(t, f, t.TempDir(), wt)
		g, err := CheckGates(Git{Dir: f.Dir}, w, GateInputs{Base: baseRef})
		if err != nil {
			t.Fatal(err)
		}
		if !g.DetachedUnique {
			t.Errorf("detached on a commit no branch has: %+v", g)
		}
	})
}

// TestGatesFailClosed hands CheckGates a worktree whose directory is gone and
// an unqualified base. Why: a gate that cannot be evaluated must be an error
// (UNKNOWN), never a clean worktree.
func TestGatesFailClosed(t *testing.T) {
	t.Run("missing directory", func(t *testing.T) {
		f := newFixture(t)
		wt := f.Worktree("topic")
		w := factFor(t, f, t.TempDir(), wt)
		if err := os.RemoveAll(wt); err != nil {
			t.Fatal(err)
		}
		if g, err := CheckGates(Git{Dir: f.Dir}, w, GateInputs{Base: baseRef}); err == nil {
			t.Errorf("want an error, got %+v", g)
		}
	})
	t.Run("unqualified base", func(t *testing.T) {
		f := newFixture(t)
		wt := f.Worktree("topic")
		gitIn(t, wt, "checkout", "--detach", "main")
		w := factFor(t, f, t.TempDir(), wt)
		if g, err := CheckGates(Git{Dir: f.Dir}, w, GateInputs{Base: "main"}); err == nil {
			t.Errorf("want an error, got %+v", g)
		}
	})
}

// TestGatesListFailureKeepsProtectedArtifact makes the repository-wide
// worktree list fail (the runner points at a directory that is not a
// repository) for a worktree holding a registered model artifact. Why: the
// artifact check needs no list; a failure after it must still report the
// blocker with the error, or the row could be offered for removal.
func TestGatesListFailureKeepsProtectedArtifact(t *testing.T) {
	f := newFixture(t)
	wt := f.Worktree("topic")
	w := factFor(t, f, t.TempDir(), wt)
	in := GateInputs{Base: baseRef, ProtectedPaths: []string{filepath.Join(wt, "models", "m")}}
	g, err := CheckGates(Git{Dir: t.TempDir()}, w, in)
	if err == nil {
		t.Fatalf("want an error from the worktree list, got %+v", g)
	}
	if !slices.Contains(codes(g), BlockProtectedArtifact) {
		t.Fatalf("blockers %v with error %v, want %q", codes(g), err, BlockProtectedArtifact)
	}
}
