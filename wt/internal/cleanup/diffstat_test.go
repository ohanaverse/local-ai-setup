package cleanup

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/dirtsnap"
	"github.com/ohanaverse/local-ai-setup/wt/internal/sessionclaim"
)

// diffLines renders line1..lineN with a trailing newline: the bodies the
// diffstat fixtures count.
func diffLines(n int) string {
	out := ""
	for i := 1; i <= n; i++ {
		out += fmt.Sprintf("line%d\n", i)
	}
	return out
}

// writeFileAt writes content to p relative to dir (creating parents).
func writeFileAt(t *testing.T, dir, p, content string) {
	t.Helper()
	full := filepath.Join(dir, p)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestDiffStatCountsStagedUnstagedAndUntracked pins readDiffStat's shape on
// the three change kinds at once: a staged add (+96), an unstaged deletion
// (-55) and a 3-line untracked file (+3) sum to +99/-55 with Unknown clear.
// Why: the DIFF cell shows exactly these numbers; a stat that counted only
// one side of the index, or double-counted HEAD, would misreport how much
// work sits in a worktree.
func TestDiffStatCountsStagedUnstagedAndUntracked(t *testing.T) {
	f := newFixture(t)
	writeFileAt(t, f.Dir, "b.txt", diffLines(60))
	f.git("add", "b.txt")
	f.git("commit", "-m", "add b")
	// Unstaged deletion: b shrinks 60 → 5 lines (first 5 unchanged).
	writeFileAt(t, f.Dir, "b.txt", diffLines(5))
	// Staged add: a new file, added but not committed.
	writeFileAt(t, f.Dir, "new1.txt", diffLines(96))
	f.git("add", "new1.txt")
	// Untracked: three lines, never added.
	writeFileAt(t, f.Dir, "untracked.txt", "u1\nu2\nu3\n")

	got := readDiffStat(Git{Dir: f.Dir})
	want := DiffStat{Added: 99, Removed: 55}
	if got != want {
		t.Fatalf("readDiffStat = %+v, want %+v", got, want)
	}
}

// TestDiffStatIgnoredFilesNeverCount pins that a worktree whose only changes
// are ignored files reads +0/-0, Dirty false and IgnoredCount 1. Why: ignored
// work has its own column and never counts as a change — DIFF and the dirty
// gate must both look past it, or a .env alone would mark a worktree dirty.
func TestDiffStatIgnoredFilesNeverCount(t *testing.T) {
	f := newFixture(t)
	wt := f.Worktree("topic")
	f.IgnoredIn(wt, ".env")

	if got := readDiffStat(Git{Dir: wt}); got != (DiffStat{}) {
		t.Fatalf("readDiffStat = %+v, want +0/-0 (ignored files never count)", got)
	}
	w := factFor(t, f, f.Dir, wt)
	g, err := CheckGates(Git{Dir: f.Dir}, w, GateInputs{Base: baseRef})
	if err != nil {
		t.Fatalf("CheckGates: %v", err)
	}
	if g.Dirty {
		t.Error("Dirty = true with only ignored changes")
	}
	if g.IgnoredCount != 1 {
		t.Errorf("IgnoredCount = %d, want 1", g.IgnoredCount)
	}
}

// TestDiffStatBinaryAndOverCapAreUnknownOrZero pins git's diffstat rule for
// binaries (they contribute no lines, tracked or untracked) and the caps
// (over DirtMaxFiles the counts are Unknown, never a confident number). Why:
// a binary counted as text would report thousands of phantom lines, and a
// change-set over the caps must read as "could not tell", not as zero.
func TestDiffStatBinaryAndOverCapAreUnknownOrZero(t *testing.T) {
	t.Run("binary changes add no lines", func(t *testing.T) {
		f := newFixture(t)
		writeFileAt(t, f.Dir, "bin.dat", "\x00\x01\x02ONE\x00")
		f.git("add", "bin.dat")
		f.git("commit", "-m", "add binary")
		writeFileAt(t, f.Dir, "bin.dat", "\x00\x03\x04TWO\x00\x05") // unstaged binary edit
		writeFileAt(t, f.Dir, "untracked-bin.dat", "\x00\x00binary\x00")

		got := readDiffStat(Git{Dir: f.Dir})
		if got != (DiffStat{}) {
			t.Fatalf("readDiffStat = %+v, want +0/-0 (binaries contribute no lines)", got)
		}
	})
	t.Run("lowered caps read unknown", func(t *testing.T) {
		f := newFixture(t)
		f.DirtyIn(f.Dir, "scratch.txt")
		old := dirtLimits
		dirtLimits = dirtsnap.Limits{MaxFiles: 0, MaxBytes: DirtMaxBytes}
		t.Cleanup(func() { dirtLimits = old })

		got := readDiffStat(Git{Dir: f.Dir})
		if !got.Unknown {
			t.Fatalf("readDiffStat = %+v, want Unknown over the file cap", got)
		}
	})
}

// TestGatesInBaseOnlyWhenDirty pins when Gates.InBase is computed: a real
// answer (here TriNo — the untracked file is not in the base) only for a
// dirty worktree with a base and readable diff counts; TriUnknown for a clean
// worktree, a missing base, or a diff over the caps. Why: STATUS's "in main"
// hangs off this Tri, and an unknown must never masquerade as contained (nor
// a clean worktree answer a question nobody asked).
func TestGatesInBaseOnlyWhenDirty(t *testing.T) {
	f := newFixture(t)
	wt := f.Worktree("topic")
	w := factFor(t, f, f.Dir, wt)

	// Clean: nothing uncommitted, so "in main" is not asked — unknown.
	g, err := CheckGates(Git{Dir: f.Dir}, w, GateInputs{Base: baseRef})
	if err != nil {
		t.Fatalf("CheckGates (clean): %v", err)
	}
	if g.InBase != TriUnknown {
		t.Errorf("clean InBase = %v, want TriUnknown", g.InBase)
	}

	// Dirty with an untracked file main does not hold: a real answer, TriNo.
	f.DirtyIn(wt, "scratch.txt")
	g, err = CheckGates(Git{Dir: f.Dir}, w, GateInputs{Base: baseRef})
	if err != nil {
		t.Fatalf("CheckGates (dirty): %v", err)
	}
	if !g.Dirty {
		t.Fatal("Dirty = false with an untracked file")
	}
	if g.InBase != TriNo {
		t.Errorf("dirty InBase = %v, want TriNo (scratch.txt is not in the base)", g.InBase)
	}

	// Dirty without a base: containment has nothing to judge against.
	if g, err = CheckGates(Git{Dir: f.Dir}, w, GateInputs{}); err != nil {
		t.Fatalf("CheckGates (no base): %v", err)
	}
	if g.InBase != TriUnknown {
		t.Errorf("no base InBase = %v, want TriUnknown", g.InBase)
	}

	// Dirty over the file cap: the counts are unknown, so containment is too.
	old := dirtLimits
	dirtLimits = dirtsnap.Limits{MaxFiles: 0, MaxBytes: DirtMaxBytes}
	t.Cleanup(func() { dirtLimits = old })
	if g, err = CheckGates(Git{Dir: f.Dir}, w, GateInputs{Base: baseRef}); err != nil {
		t.Fatalf("CheckGates (over cap): %v", err)
	}
	if !g.Diff.Unknown {
		t.Errorf("over cap Diff = %+v, want Unknown", g.Diff)
	}
	if g.InBase != TriUnknown {
		t.Errorf("over cap InBase = %v, want TriUnknown", g.InBase)
	}
}

// TestRescanComputesTheSameGateFacts records the gates for a dirty worktree
// during a full Gather and during a Rescan of the same row (through the
// checkGates seam) and compares them. Why: removal re-checks through Rescan;
// if the rescan's gates lacked the new diffstat/in-main facts (or ran with
// different inputs), `wt remove --yes` would judge rows by facts the list
// called differently than the re-check does.
func TestRescanComputesTheSameGateFacts(t *testing.T) {
	f := newFixture(t)
	noGh(t)
	wt := f.Worktree("topic")
	f.DirtyIn(wt, "notes.txt")
	writeFileAt(t, wt, "staged.txt", "s1\ns2\n")
	gitIn(t, wt, "add", "staged.txt")
	o := Options{Now: later()}

	var gathered, rescanned Gates
	phase := "gather"
	old := checkGates
	checkGates = func(g Git, wf WorktreeFact, in GateInputs) (Gates, error) {
		gg, err := old(g, wf, in)
		if resolve(wf.Path) == resolve(wt) {
			if phase == "gather" {
				gathered = gg
			} else {
				rescanned = gg
			}
		}
		return gg, err
	}
	t.Cleanup(func() { checkGates = old })

	s := gather(t, f)
	w := findRow(t, Classify(s, o), KindWorktree, "topic")
	phase = "rescan"
	mustRescan(t, f, s, o, targetOf(w))

	if gathered.Diff.Unknown || gathered.Diff.Added == 0 {
		t.Fatalf("gathered Diff = %+v, want counted lines: the fixture is dirty", gathered.Diff)
	}
	if gathered.Diff != rescanned.Diff {
		t.Errorf("Diff: gather %+v != rescan %+v", gathered.Diff, rescanned.Diff)
	}
	if gathered.InBase != rescanned.InBase {
		t.Errorf("InBase: gather %v != rescan %v", gathered.InBase, rescanned.InBase)
	}
	if gathered.InBase != TriNo {
		t.Errorf("InBase = %v, want TriNo (notes.txt is not in the base) in both runs", gathered.InBase)
	}
}

// TestSessionsOnExcludesSelfAndDead pins SessionsOn's count through the real
// claim files: one live holder child counts; this process's own claim and a
// dead pid's leftover file do not. Why: the session digit counts *running*
// sessions other than the browsing one — a digit that showed the picker's own
// session, or a crashed session that never released, would lie about who is
// using a worktree.
func TestSessionsOnExcludesSelfAndDead(t *testing.T) {
	f := newFixture(t)
	wt := f.Worktree("topic")
	common := gitIn(t, f.Dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	holder := holderClaim(t, common, wt, "topic", "codex")

	dir := filepath.Join(common, "wt-sessions")
	self := os.Getpid()
	selfFile := filepath.Join(dir, fmt.Sprint(self))
	if err := os.WriteFile(selfFile, []byte(fmt.Sprintf("pid=%d\nstarted=1\npath=%s\nbranch=topic\nagent=claude\n", self, wt)), 0o644); err != nil {
		t.Fatal(err)
	}
	dead := holder.PID + 1_000_000 // no such pid
	deadFile := filepath.Join(dir, fmt.Sprint(dead))
	if err := os.WriteFile(deadFile, []byte(fmt.Sprintf("pid=%d\nstarted=1\npath=%s\nbranch=topic\nagent=codex\n", dead, wt)), 0o644); err != nil {
		t.Fatal(err)
	}

	var s Snapshot
	s.CommonDir = common
	s.readClaims(GatherInputs{Claim: sessionclaim.Claim{PID: self, Started: 1}})
	if n := s.SessionsOn(wt); n != 1 {
		t.Fatalf("SessionsOn = %d, want 1: the live holder counts; this process's claim and the dead pid's do not", n)
	}
}
