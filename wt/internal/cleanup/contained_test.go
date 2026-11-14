package cleanup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/dirtsnap"
	"github.com/ohanaverse/local-ai-setup/wt/internal/gittest"
)

// containedLines renders line1..line10: the shared body of the squash
// fixtures, so each test states only which line it changes.
func containedLines() []string {
	out := make([]string, 10)
	for i := range out {
		out[i] = "line" + string(rune('1'+i))
	}
	return out
}

// containedRender joins the lines with newlines and one trailing newline, the
// shape git stores.
func containedRender(ls []string) string { return strings.Join(ls, "\n") + "\n" }

// containedCopy returns a fresh slice so a test can mutate its own copy.
func containedCopy(ls []string) []string {
	out := make([]string, len(ls))
	copy(out, ls)
	return out
}

// TestContainedSquashAfterMainEditedSameFile pins that a squash-merged branch
// whose file main later edited still reverse-applies onto main's tree (TriYes)
// — the case hash-equality proofs miss. Why: this is the rung-6 gap. The
// adjacent-edit subtest is the layout where merge-tree conflicts (so rung 5
// declines today) even though the branch's change really is in main;
// containment must say TriYes there. The far-edit subtest is the plan's
// line-1/line-9 layout, where rung 5 already proves it and containment must
// agree rather than disagree.
func TestContainedSquashAfterMainEditedSameFile(t *testing.T) {
	base := containedLines()

	t.Run("far edit, main edited line 9", func(t *testing.T) {
		f := newFixture(t)
		f.Commit("a.txt", containedRender(base), "add a.txt")
		f.Branch("topic")
		f.Checkout("topic")
		ls := containedCopy(base)
		ls[0] = "CHANGED1"
		f.Commit("a.txt", containedRender(ls), "topic changes line 1")
		f.Squash("topic")
		after := containedCopy(ls)
		after[8] = "EDITED9"
		f.Commit("a.txt", containedRender(after), "main edits line 9")

		got := tipContainedInBase(Git{Dir: f.Dir}, "refs/heads/main", "refs/heads/topic")
		if got != TriYes {
			t.Fatalf("tipContainedInBase = %v, want TriYes (main holds the squashed line-1 change)", got)
		}
	})

	t.Run("adjacent edit, merge-tree conflicts but the change is in main", func(t *testing.T) {
		f := newFixture(t)
		f.Commit("a.txt", containedRender(base), "add a.txt")
		f.Branch("topic")
		f.Checkout("topic")
		ls := containedCopy(base)
		ls[4] = "CHANGED5"
		f.Commit("a.txt", containedRender(ls), "topic changes line 5")
		f.Squash("topic")
		after := containedCopy(ls)
		after[3] = "EDITED4"
		f.Commit("a.txt", containedRender(after), "main edits line 4")

		// Sanity: the rung-5 probe really declines this layout (merge-tree
		// conflicts once main edits a line adjacent to the branch's change),
		// so a TriYes here is the gap rung 6 exists to close.
		f.mustFail(f.Dir, "", "merge-tree", "--write-tree", "main", "topic")

		got := tipContainedInBase(Git{Dir: f.Dir}, "refs/heads/main", "refs/heads/topic")
		if got != TriYes {
			t.Fatalf("tipContainedInBase = %v, want TriYes (line 5's change is in main; only an adjacent context line moved)", got)
		}
	})
}

// TestContainedFailsWhenMainRevertedTheChange pins that main later reverting
// the squashed change reads TriNo, never TriYes. Why: a revert defeats
// cherry/patch-id (the change's text is gone from main), and containment must
// fail the same way — a false TriYes here would prove a row whose work main
// deliberately removed.
func TestContainedFailsWhenMainRevertedTheChange(t *testing.T) {
	f := newFixture(t)
	base := containedLines()
	f.Commit("a.txt", containedRender(base), "add a.txt")
	f.Branch("topic")
	f.Checkout("topic")
	ls := containedCopy(base)
	ls[0] = "CHANGED1"
	f.Commit("a.txt", containedRender(ls), "topic changes line 1")
	f.Squash("topic")
	f.Revert(f.git("rev-parse", "HEAD"))

	got := tipContainedInBase(Git{Dir: f.Dir}, "refs/heads/main", "refs/heads/topic")
	if got != TriNo {
		t.Fatalf("tipContainedInBase = %v, want TriNo (main reverted the change; never TriYes)", got)
	}
}

// TestContainedZeroContextHunk pins that a one-line file's change — a hunk
// with no context lines at all (@@ -1 +1 @@) — is judged TriYes when main
// holds the same text. Why: --unidiff-zero is what lets git apply check such a
// hunk at all; without the flag this layout reads unknown, and the zero-context
// path would silently never prove.
func TestContainedZeroContextHunk(t *testing.T) {
	f := newFixture(t)
	f.Commit("one.txt", "alpha\n", "add one.txt")
	f.Branch("topic")
	f.Checkout("topic")
	f.Commit("one.txt", "bravo\n", "topic edits the one-line file")
	f.Squash("topic")

	got := tipContainedInBase(Git{Dir: f.Dir}, "refs/heads/main", "refs/heads/topic")
	if got != TriYes {
		t.Fatalf("tipContainedInBase = %v, want TriYes (main holds bravo, the squashed one-line change)", got)
	}
}

// TestContainedBinaryAndRenames pins that a binary change contained in base
// reads TriYes (the literal GIT binary patch reverse-applies), and a rename is
// judged — through the --no-renames delete+add pair — as TriYes once squashed,
// never TriUnknown. Why: both are shapes a naive line-oriented check cannot
// judge; the engine must handle them or rows with binaries/renames would sit
// at unknown forever.
func TestContainedBinaryAndRenames(t *testing.T) {
	t.Run("binary change squashed into main", func(t *testing.T) {
		f := newFixture(t)
		f.Commit("bin.dat", "\x00\x01\x02BIN\x00DATA", "add binary")
		f.Branch("topic")
		f.Checkout("topic")
		f.Commit("bin.dat", "\x00\x01\x02OTHER\x03\x04\x00", "topic changes the binary")
		f.Squash("topic")

		got := tipContainedInBase(Git{Dir: f.Dir}, "refs/heads/main", "refs/heads/topic")
		if got != TriYes {
			t.Fatalf("tipContainedInBase = %v, want TriYes (main holds the squashed blob)", got)
		}
	})

	t.Run("rename squashed into main", func(t *testing.T) {
		f := newFixture(t)
		f.Commit("alpha.txt", "hello\n", "add alpha.txt")
		f.Branch("topic")
		f.Checkout("topic")
		f.git("mv", "alpha.txt", "beta.txt")
		f.git("commit", "-m", "topic renames alpha to beta")
		f.Squash("topic")

		got := tipContainedInBase(Git{Dir: f.Dir}, "refs/heads/main", "refs/heads/topic")
		if got != TriYes {
			t.Fatalf("tipContainedInBase = %v, want TriYes (delete+add judged, not unknown)", got)
		}
	})
}

// TestWorktreeContainedUntrackedBlobEquality pins the untracked half of a
// worktree's change-set: an untracked file whose blob equals base:<path> is
// contained, one differing byte is not, and an ignored file never enters the
// change-set at all. Why: untracked work has no patch to reverse-apply, so
// blob equality is the containment claim for it; ignored work must stay
// invisible (it is IGN's subject, never DIFF's or containment's).
func TestWorktreeContainedUntrackedBlobEquality(t *testing.T) {
	// build makes a topic worktree forked before main gained new.txt, so an
	// untracked new.txt there is compared against main's blob.
	build := func(t *testing.T) (*fixture, string) {
		t.Helper()
		f := newFixture(t)
		wp := f.Worktree("topic")
		f.Commit("new.txt", "hello\n", "main adds new.txt")
		return f, wp
	}

	t.Run("equal blob", func(t *testing.T) {
		_, wp := build(t)
		if err := os.WriteFile(filepath.Join(wp, "new.txt"), []byte("hello\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		got := worktreeContainedInBase(Git{Dir: wp}, "refs/heads/main")
		if got != TriYes {
			t.Fatalf("worktreeContainedInBase = %v, want TriYes (blob equals main's)", got)
		}
	})

	t.Run("one byte differs", func(t *testing.T) {
		_, wp := build(t)
		if err := os.WriteFile(filepath.Join(wp, "new.txt"), []byte("hello!\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		got := worktreeContainedInBase(Git{Dir: wp}, "refs/heads/main")
		if got != TriNo {
			t.Fatalf("worktreeContainedInBase = %v, want TriNo (blob differs from main's)", got)
		}
	})

	t.Run("ignored file never considered", func(t *testing.T) {
		f, wp := build(t)
		f.IgnoredIn(wp, "scratch.log")
		got := worktreeContainedInBase(Git{Dir: wp}, "refs/heads/main")
		if got != TriYes {
			t.Fatalf("worktreeContainedInBase = %v, want TriYes (ignored work never enters the change-set)", got)
		}
	})
}

// TestContainedUnknownOnGitFailureAndOverCaps pins fail-closed: a bogus tip,
// a bogus base, a patch over the byte cap and untracked files over the file
// cap all read TriUnknown — never TriYes. Why: every downstream verdict
// treats unknown as "not proven"; a cap or failure that read as contained
// would hand out a false proven on a repository the engine could not judge.
func TestContainedUnknownOnGitFailureAndOverCaps(t *testing.T) {
	f := newFixture(t)
	base := containedLines()
	f.Commit("a.txt", containedRender(base), "add a.txt")
	f.Branch("topic")
	f.Checkout("topic")
	ls := containedCopy(base)
	ls[0] = "CHANGED1"
	f.Commit("a.txt", containedRender(ls), "topic changes line 1")
	f.Squash("topic")
	g := Git{Dir: f.Dir}

	t.Run("bogus tip", func(t *testing.T) {
		if got := tipContainedInBase(g, "refs/heads/main", "refs/heads/does-not-exist"); got != TriUnknown {
			t.Fatalf("bogus tip = %v, want TriUnknown", got)
		}
	})
	t.Run("bogus base", func(t *testing.T) {
		if got := tipContainedInBase(g, "refs/heads/missing-base", "refs/heads/topic"); got != TriUnknown {
			t.Fatalf("bogus base = %v, want TriUnknown", got)
		}
	})
	t.Run("patch over the byte cap", func(t *testing.T) {
		old := dirtLimits
		dirtLimits = dirtsnap.Limits{MaxFiles: DirtMaxFiles, MaxBytes: 4}
		t.Cleanup(func() { dirtLimits = old })
		if got := tipContainedInBase(g, "refs/heads/main", "refs/heads/topic"); got != TriUnknown {
			t.Fatalf("over the byte cap = %v, want TriUnknown, never TriYes", got)
		}
	})
	t.Run("untracked files over the file cap", func(t *testing.T) {
		old := dirtLimits
		dirtLimits = dirtsnap.Limits{MaxFiles: 1, MaxBytes: DirtMaxBytes}
		t.Cleanup(func() { dirtLimits = old })
		wp := f.Worktree("topic2")
		for _, name := range []string{"new-1.txt", "new-2.txt"} {
			if err := os.WriteFile(filepath.Join(wp, name), []byte("x\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if got := worktreeContainedInBase(Git{Dir: wp}, "refs/heads/main"); got != TriUnknown {
			t.Fatalf("over the file cap = %v, want TriUnknown, never TriYes", got)
		}
	})
}

// TestContainedLeavesTheRealIndexAlone pins that both containment calls leave
// the real index byte-for-byte unchanged on an armed (racy, rewrite-visible)
// probe. Why: internal/cleanup is read-only apart from documented fetches —
// `git diff HEAD` rewrites the index's stat cache even under
// GIT_OPTIONAL_LOCKS=0, so the worktree change-set must diff against a
// throwaway copy, and this test is what fails if it ever touches the real one.
func TestContainedLeavesTheRealIndexAlone(t *testing.T) {
	f := newFixture(t)
	base := containedLines()
	f.Commit("a.txt", containedRender(base), "add a.txt")
	f.Branch("topic")
	f.Checkout("topic")
	ls := containedCopy(base)
	ls[0] = "CHANGED1"
	f.Commit("a.txt", containedRender(ls), "topic changes line 1")
	f.Checkout("main")
	f.Squash("topic")
	// An unstaged edit and an untracked file, so the worktree change-set runs
	// both its diff and its untracked walk under the probe.
	edited := containedCopy(ls)
	edited[9] = "UNSTAGED10"
	if err := os.WriteFile(filepath.Join(f.Dir, "a.txt"), []byte(containedRender(edited)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.Dir, "scratch.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	gittest.ArmIndexProbe(t, f.Dir)
	gittest.AssertProbeArmed(t, f.Dir)
	before := gittest.RawIndex(t, f.Dir)

	tipContainedInBase(Git{Dir: f.Dir}, "refs/heads/main", "refs/heads/topic")
	worktreeContainedInBase(Git{Dir: f.Dir}, "refs/heads/main")

	if after := gittest.RawIndex(t, f.Dir); after != before {
		t.Fatal("the containment check rewrote the real index (it must diff against a throwaway copy)")
	}
}
