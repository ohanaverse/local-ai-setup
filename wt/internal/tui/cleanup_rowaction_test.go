package tui

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/removal"
)

// The per-row removal keys (`x`, `d`, `o`) act on the highlighted row alone:
// these tests drive the keys through Update over real repositories (the
// engine's preview is the only one that confirms, Ruling N) and pin what each
// previews, what the confirm screen demands and what the run reaches.

// foldedOrigin is a worktree whose branch is pushed to origin at main's tip
// with a merged PR there: origin/<branch> is proven and folds into the
// worktree's row, so the row carries a local worktree, a local branch and one
// origin ref to choose between. It returns the worktree path and the tip.
func (r *rmRepo) foldedOrigin(branch string) (string, string) {
	r.t.Helper()
	p := r.worktree(branch)
	r.git("push", "-u", "origin", branch)
	tip := r.git("rev-parse", "main")
	merged := fmt.Sprintf(`[{"number":8,"state":"MERGED","headRefOid":%q,"baseRefName":"main","mergeCommit":{"oid":%q},"isCrossRepository":false,"headRepository":{"name":"r","nameWithOwner":"o/r"}}]`, tip, tip)
	r.t.Cleanup(cleanup.StubPRListForTest(func(_, b string) ([]byte, error) {
		if b == branch {
			return []byte(merged), nil
		}
		return []byte("[]"), nil
	}))
	return p, tip
}

// TestCleanupListXPreviewsOnlyTheHighlightedRow pins `x`: it previews the
// highlighted row alone, whatever is ticked — the ticks belong to enter
// (`enter remove ticked`), never to these keys — in manage and launch mode
// alike, with ticks and without.
func TestCleanupListXPreviewsOnlyTheHighlightedRow(t *testing.T) {
	// Manage, with the proven rows ticked and the cursor on an unticked one.
	m := moveTo(t, newSizedCleanupList(t, cleanupFixture(), 120, 40), "spike-picker")
	got, fm := runPreview(t, m, keyRunes("x"))
	if strings.Join(got, ",") != "spike-picker" {
		t.Errorf("x previewed %v, want only the highlighted row (ticks ignored)", got)
	}
	if fm.phase != cleanupPhasePreviewing {
		t.Errorf("phase = %v, want previewing", fm.phase)
	}
	// Manage, nothing ticked: the highlighted row all the same.
	m = setTicks(t, newSizedCleanupList(t, cleanupFixture(), 120, 40))
	m = moveTo(t, m, "fix-default-guard")
	got, _ = runPreview(t, m, keyRunes("x"))
	if strings.Join(got, ",") != "fix-default-guard" {
		t.Errorf("x previewed %v, want only the highlighted row", got)
	}
	// Launch mode: the highlighted row.
	l := moveTo(t, newLaunchList(t, cleanupFixture(), 120, 40), "fix-default-guard")
	got, fl := runPreview(t, l, keyRunes("x"))
	if strings.Join(got, ",") != "fix-default-guard" || fl.phase != cleanupPhasePreviewing {
		t.Errorf("launch x previewed %v (phase %v), want the highlighted row", got, fl.phase)
	}
}

// TestCleanupListXOnProvenWorktreeAcceptsY pins the light path through a row
// action: `x` on a proven worktree opens the y prompt and y removes exactly
// that row — its worktree and its branch — while the other ticked rows are
// left alone (a proven row accepts y, the confirmation contract later pins).
func TestCleanupListXOnProvenWorktreeAcceptsY(t *testing.T) {
	r := newRmRepo(t)
	done := r.merged("done")
	extra := r.merged("extra")
	h := r.harness()
	m := moveTo(t, h.list(120, 50), "done")
	m = press(t, m, keyRunes("x"))
	if m.phase != cleanupPhaseConfirm {
		t.Fatalf("x: phase %v, want the confirm screen (status %q)", m.phase, m.status)
	}
	v := m.View()
	if !strings.Contains(v, "y removes 1 item (0 force, 0 -D) · any other key cancels") {
		t.Fatalf("proven row's prompt:\n%s", v)
	}
	// The screens must say what chose the row: x chose the highlighted row,
	// so the summary says "selected", never "ticked" (enter's word for rows
	// the ticks chose — the picker pins the same in launch mode).
	if !strings.Contains(squash(v), squash("remove 1 of 1 selected")) {
		t.Errorf("the confirm summary does not say what chose the row:\n%s", v)
	}
	m = press(t, m, keyRunes("y"))
	if m.phase != cleanupPhaseResults || h.runCalls != 1 {
		t.Fatalf("y: phase %v runs %d; want the run to happen", m.phase, h.runCalls)
	}
	if len(h.ran) != 1 || h.ran[0].Name != "done" {
		t.Errorf("ran %v, want only the highlighted row", h.ran)
	}
	if _, err := os.Stat(done); !os.IsNotExist(err) || r.refExists("refs/heads/done") {
		t.Errorf("done not removed: stat err %v, ref %v", err, r.refExists("refs/heads/done"))
	}
	if _, err := os.Stat(extra); err != nil || !r.refExists("refs/heads/extra") {
		t.Errorf("the ticked extra row went too: stat err %v, ref %v", err, r.refExists("refs/heads/extra"))
	}
}

// TestCleanupListXOnDirtyWorktreeNeedsTypedDelete pins the at-risk path
// through a row action: `x` on a dirty worktree demands the typed word
// (y alone confirms nothing and enter without the word cancels), and only
// `delete` plus enter runs it — with exactly what that row needs, nothing
// more, and no ticked row riding along.
func TestCleanupListXOnDirtyWorktreeNeedsTypedDelete(t *testing.T) {
	r := newRmRepo(t)
	p := r.dirty("dirty")
	r.merged("done")
	h := r.harness()
	open := func() cleanupListModel {
		return press(t, moveTo(t, h.list(120, 50), "dirty"), keyRunes("x"))
	}
	m := open()
	if m.phase != cleanupPhaseConfirm {
		t.Fatalf("x: phase %v, want the confirm screen (status %q)", m.phase, m.status)
	}
	if v := m.View(); !strings.Contains(v, "type delete and press enter to remove 1 item") || strings.Contains(v, "y removes") {
		t.Fatalf("dirty row's prompt must demand the typed word:\n%s", v)
	}
	// y is not the word: enter cancels, nothing runs.
	m = press(t, press(t, m, keyRunes("y")), enterKey)
	if m.phase != cleanupPhaseList || h.runCalls != 0 || !strings.Contains(m.status, "nothing was removed") {
		t.Fatalf("y+enter: phase %v runs %d status %q; want cancelled", m.phase, h.runCalls, m.status)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal("the worktree was removed without the typed word")
	}
	// The typed word runs it with its own approvals only.
	m = press(t, typeWord(t, open(), "delete"), enterKey)
	if h.runCalls != 1 || len(h.ran) != 1 {
		t.Fatalf("delete+enter: runs %d items %d", h.runCalls, len(h.ran))
	}
	if h.ran[0].Approved != (removal.Needs{Confirm: true, Force: true}) {
		t.Errorf("dirty approved %+v, want Confirm+Force only", h.ran[0].Approved)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("dirty worktree still there after the typed word")
	}
	if !r.refExists("refs/heads/done") {
		t.Error("the ticked done row went with the highlighted one")
	}
}

// TestCleanupListDOnWorktreeRowSeedsDeleteBranch pins `d` on a worktree row:
// it previews that row like `x`, then seeds delBranch=true on the confirm
// entry (the tab toggle comes pre-toggled), so the typed word removes the
// worktree AND its unmerged branch — the branch `x` alone keeps.
func TestCleanupListDOnWorktreeRowSeedsDeleteBranch(t *testing.T) {
	r := newRmRepo(t)
	p := r.unpushed("kept")
	h := r.harness()
	m := moveTo(t, h.list(120, 50), "kept")
	m = press(t, m, keyRunes("d"))
	if m.phase != cleanupPhaseConfirm {
		t.Fatalf("d: phase %v, want the confirm screen (status %q)", m.phase, m.status)
	}
	if len(h.previewed) != 1 || len(h.previewed[0]) != 1 || h.previewed[0][0].Name != "kept" {
		t.Fatalf("d previewed %v, want only the highlighted row", h.previewed)
	}
	if len(m.conf.entries) != 1 {
		t.Fatalf("confirm entries = %d, want 1", len(m.conf.entries))
	}
	if !m.conf.entries[0].delBranch {
		t.Error("d did not seed delBranch on its confirm entry")
	}
	if v := m.View(); !strings.Contains(v, "[x] also delete unmerged branch kept (tab)") {
		t.Errorf("the seeded toggle is not drawn on:\n%s", v)
	}
	m = press(t, typeWord(t, m, "delete"), enterKey)
	if h.runCalls != 1 || len(h.ran) != 1 {
		t.Fatalf("delete+enter: runs %d items %d", h.runCalls, len(h.ran))
	}
	if h.ran[0].Approved != (removal.Needs{Confirm: true, Delete: true}) {
		t.Errorf("d approved %+v, want Confirm+Delete", h.ran[0].Approved)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Error("the worktree is still there")
	}
	if r.refExists("refs/heads/kept") {
		t.Error("its unmerged branch was kept although d seeds the branch deletion")
	}
}

// TestCleanupListDOnBranchRowIsRemove pins `d` on a branch row: it is `x`.
// A branch row has no worktree and no branch of its own to add, so `d` must
// take the identical path — same preview, same typed word, same approvals,
// the branch removed — never a second, divergent behavior behind one key.
func TestCleanupListDOnBranchRowIsRemove(t *testing.T) {
	r := newRmRepo(t)
	r.loose("d-spike")
	r.loose("x-spike")
	h := r.harness()
	// `x` on one loose branch: the typed word removes it.
	m := press(t, moveTo(t, h.list(120, 50), "x-spike"), keyRunes("x"))
	if m.phase != cleanupPhaseConfirm {
		t.Fatalf("x: phase %v, want the confirm screen (status %q)", m.phase, m.status)
	}
	m = press(t, typeWord(t, m, "delete"), enterKey)
	// `d` on the other: exactly the same treatment.
	m = press(t, moveTo(t, h.list(120, 50), "d-spike"), keyRunes("d"))
	if m.phase != cleanupPhaseConfirm {
		t.Fatalf("d: phase %v, want the confirm screen (status %q)", m.phase, m.status)
	}
	m = press(t, typeWord(t, m, "delete"), enterKey)
	if h.runCalls != 2 || len(h.ran) != 2 {
		t.Fatalf("runs %d items %d, want one per key", h.runCalls, len(h.ran))
	}
	if h.ran[0].Kind != cleanup.KindBranch || h.ran[1].Kind != cleanup.KindBranch {
		t.Errorf("ran %+v, want two branch items", h.ran)
	}
	if h.ran[0].Approved != h.ran[1].Approved {
		t.Errorf("d approved %+v but x approved %+v; d on a branch row is x", h.ran[1].Approved, h.ran[0].Approved)
	}
	for _, b := range []string{"x-spike", "d-spike"} {
		if r.refExists("refs/heads/" + b) {
			t.Errorf("%s not removed", b)
		}
	}
}

// TestCleanupListOOnFoldedRowDeletesOnlyTheOriginRef pins `o` on a row with a
// folded remote ref: it previews the origin ref alone (never the worktree or
// its local branch), reaches the engine as the remote-only item
// (`wt remove origin/<name> --remote`'s shape), and only the typed phrase
// `delete origin` runs it — y and the plain `delete` leave origin alone. The
// run deletes the origin ref and nothing else.
func TestCleanupListOOnFoldedRowDeletesOnlyTheOriginRef(t *testing.T) {
	r := newRmRepo(t)
	p, tip := r.foldedOrigin("topic")
	h := r.originHarness()
	open := func() cleanupListModel {
		return press(t, moveTo(t, h.list(120, 50), "topic"), keyRunes("o"))
	}
	m := open()
	if m.phase != cleanupPhaseConfirm {
		t.Fatalf("o: phase %v, want the confirm screen (status %q)", m.phase, m.status)
	}
	if len(h.previewed) != 1 || len(h.previewed[0]) != 1 {
		t.Fatalf("o previewed %v, want one row", h.previewed)
	}
	if got := h.previewed[0][0]; got.Kind != cleanup.KindRemote || got.Ref != "refs/remotes/origin/topic" {
		t.Errorf("o previewed %+v, want only the folded origin ref", got)
	}
	if v := m.View(); !strings.Contains(squash(v), squash("type delete origin and press enter")) {
		t.Fatalf("the prompt must demand the phrase delete origin:\n%s", v)
	}
	// y confirms nothing here (an origin deletion is never a plain yes). Each
	// step opens its own confirm screen: the typed buffer is shared state.
	if m2 := press(t, open(), keyRunes("y")); m2.phase != cleanupPhaseConfirm || h.runCalls != 0 {
		t.Fatalf("y: phase %v runs %d", m2.phase, h.runCalls)
	}
	// The plain word is not the phrase: enter cancels, origin keeps its branch.
	m2 := press(t, typeWord(t, open(), "delete"), enterKey)
	if m2.phase == cleanupPhaseResults || h.runCalls != 0 || r.originTip("topic") != tip {
		t.Fatalf("plain delete: phase %v runs %d origin tip %q", m2.phase, h.runCalls, r.originTip("topic"))
	}
	// The phrase deletes the origin ref, and only that.
	m = press(t, typeWord(t, open(), "delete origin"), enterKey)
	if h.runCalls != 1 || len(h.ran) != 1 {
		t.Fatalf("delete origin: runs %d items %d", h.runCalls, len(h.ran))
	}
	if h.ran[0].Kind != cleanup.KindRemote || h.ran[0].Approved != (removal.Needs{Remote: true}) {
		t.Errorf("ran %+v, want the origin ref with the Remote approval alone", h.ran)
	}
	if r.originTip("topic") != "" {
		t.Errorf("origin still has the branch at %q", r.originTip("topic"))
	}
	if _, err := os.Stat(p); err != nil {
		t.Errorf("the worktree was removed too: %v", err)
	}
	if !r.refExists("refs/heads/topic") {
		t.Error("the local branch was deleted too")
	}
}

// TestRowActionKeysOnTheNewWorktreeItemSayItCreates pins the status line of
// x, d and o on the launch list's "+ New worktree…" item: the item is not a
// row, so each key says it creates a worktree and has nothing to remove — it
// never borrows a key's removal wording ("o deletes the ... origin ref") — and
// no removal is previewed or started.
func TestRowActionKeysOnTheNewWorktreeItemSayItCreates(t *testing.T) {
	const want = "this is the + New worktree… item, not a row: it creates a worktree, there is nothing to remove"
	for _, key := range []string{"x", "d", "o"} {
		m := newLaunchList(t, cleanupFixture(), 120, 40)
		m.rm.Preview = func([]cleanup.Row) []removal.ItemPreview {
			t.Errorf("%s on the new-worktree item previewed a removal", key)
			return nil
		}
		if !m.items[m.cursor].newWorktree {
			t.Fatalf("%s: the launch list does not open on the new-worktree item", key)
		}
		m = press(t, m, keyRunes(key))
		if m.status != want {
			t.Errorf("%s: status = %q, want %q", key, m.status, want)
		}
		if m.phase != cleanupPhaseList {
			t.Errorf("%s: phase = %v, want the list", key, m.phase)
		}
	}
}
