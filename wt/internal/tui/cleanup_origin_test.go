package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/removal"
)

// originBranch pushes branch at main's tip and stubs gh so that it has one
// merged PR at that tip and nothing targets it: a proven origin row once the
// list is gathered with a fetch. It returns the tip.
func (r *rmRepo) originBranch(branch string) string {
	r.t.Helper()
	r.git("branch", branch)
	r.git("push", "-u", "origin", branch)
	tip := r.git("rev-parse", "main")
	merged := fmt.Sprintf(`[{"number":8,"state":"MERGED","headRefOid":%q,"baseRefName":"main","mergeCommit":{"oid":%q},"isCrossRepository":false,"headRepository":{"name":"r","nameWithOwner":"o/r"}}]`, tip, tip)
	r.t.Cleanup(cleanup.StubPRListForTest(func(_, b string) ([]byte, error) {
		if b == branch {
			return []byte(merged), nil
		}
		return []byte("[]"), nil
	}))
	return tip
}

// originHarness is a harness whose gathers fetch (an origin row needs this
// run's fetch to be proven).
func (r *rmRepo) originHarness() *rmHarness {
	r.t.Helper()
	h := &rmHarness{repo: r, gatherOpts: cleanup.GatherInputs{CWD: r.dir, OwnRepo: "o/r", Parallel: 2, Fetch: true, OriginRows: true}}
	h.regather()
	return h
}

// originTip is the bare origin's tip of branch, or "".
func (r *rmRepo) originTip(branch string) string {
	r.t.Helper()
	root := strings.TrimSuffix(r.dir, "/repo")
	return rmGit(r.t, root+"/origin.git", "for-each-ref", "--format=%(objectname)", "refs/heads/"+branch)
}

// TestOriginRowsAreTickableButNeverTickedForYou lists a proven origin row, an
// unproven one and another remote's. What: the proven origin row is not
// locked yet starts unticked and `a` leaves it unticked, space ticks it; the
// unproven and non-origin rows stay locked with their own reason in the
// detail. Why: a destructive public action must be a deliberate, per-row
// choice, and the list may only offer what the engine can do.
func TestOriginRowsAreTickableButNeverTickedForYou(t *testing.T) {
	r := cleanupFixture()
	for i := range r.Rows {
		if r.Rows[i].Kind == cleanup.KindRemote && r.Rows[i].Name == "merged-remote" {
			r.Rows[i].Ref = "refs/remotes/origin/merged-remote"
		}
	}
	r.Rows = append(r.Rows, cleanup.Row{Kind: cleanup.KindRemote, Name: "theirs", Branch: "theirs", Remote: "upstream", Status: cleanup.StatusProven, Evidence: cleanup.EvPRTip})
	m := newSizedCleanupList(t, r, 120, 40)
	at := func(name string) int {
		for i, it := range m.items {
			if it.heading == "" && cleanup.DisplayName(it.row) == name {
				return i
			}
		}
		t.Fatalf("no row %s", name)
		return -1
	}
	merged, old, theirs := at("origin/merged-remote"), at("origin/old-remote"), at("upstream/theirs")
	if m.items[merged].locked || !m.items[old].locked || !m.items[theirs].locked {
		t.Fatalf("locked: merged %v old %v theirs %v; want false true true", m.items[merged].locked, m.items[old].locked, m.items[theirs].locked)
	}
	if m.ticked[merged] {
		t.Fatal("a proven origin row starts ticked")
	}
	m = cleanupSend(t, m, keyRunes("a"))
	if m.ticked[merged] || m.ticked[old] || m.ticked[theirs] {
		t.Fatal("`a` ticked an origin row")
	}
	m.cursor = merged
	m = cleanupSend(t, m, spaceKey)
	if !m.ticked[merged] {
		t.Fatal("space did not tick the origin row")
	}
	if d := m.detail(); !strings.Contains(d, "visible to everyone with access to the repository") {
		t.Errorf("detail of a tickable origin row does not name the audience:\n%s", d)
	}
	m.cursor = theirs
	if d := m.detail(); !strings.Contains(d, "only origin") {
		t.Errorf("detail of another remote's row: %s", d)
	}
	m.cursor = old
	if d := m.detail(); !strings.Contains(d, "cannot be ticked") {
		t.Errorf("detail of an unproven origin row: %s", d)
	}
}

// TestOriginDeleteNeedsItsOwnTypedPhrase ticks a proven origin row, then
// answers the confirm screen with y, with plain "delete" and with "delete
// origin". What: only the last runs (origin loses the branch); the others
// leave origin alone. The screen shows an ORIGIN BRANCHES group and the
// audience banner. Why: the plain word is the loss approval for local items;
// it must never publish a deletion.
func TestOriginDeleteNeedsItsOwnTypedPhrase(t *testing.T) {
	r := newRmRepo(t)
	tip := r.originBranch("topic")
	h := r.originHarness()
	open := func() cleanupListModel {
		m := setTicks(t, h.list(120, 40), "origin/topic")
		m = press(t, m, enterKey)
		if m.phase != cleanupPhaseConfirm {
			t.Fatalf("phase %v, status %q", m.phase, m.status)
		}
		return m
	}
	m := open()
	v := m.View()
	for _, want := range []string{"ORIGIN BRANCHES", "1 branch on origin", "visible to everyone with access to the repository", "delete origin", "git push origin " + tip + ":refs/heads/topic"} {
		if !strings.Contains(v, want) && !strings.Contains(squash(v), squash(want)) {
			t.Errorf("confirm screen lacks %q:\n%s", want, v)
		}
	}
	// y does not run it.
	if m2 := press(t, m, keyRunes("y")); m2.phase != cleanupPhaseConfirm || h.runCalls != 0 {
		t.Fatalf("y: phase %v runs %d", m2.phase, h.runCalls)
	}
	// plain delete does not run it.
	m2 := press(t, typeWord(t, m, "delete"), enterKey)
	if m2.phase == cleanupPhaseResults || h.runCalls != 0 || r.originTip("topic") != tip {
		t.Fatalf("plain delete: phase %v runs %d origin %q", m2.phase, h.runCalls, r.originTip("topic"))
	}
	// delete origin does.
	m = open()
	m = typeWord(t, m, "delete origin")
	if m.conf == nil || m.conf.typed != "delete origin" {
		t.Fatalf("typed %q, status %q, phase %v", func() string {
			if m.conf == nil {
				return "<nil>"
			}
			return m.conf.typed
		}(), m.status, m.phase)
	}
	m = press(t, m, enterKey)
	if h.runCalls != 1 || r.originTip("topic") != "" {
		t.Fatalf("delete origin: runs %d, origin tip %q", h.runCalls, r.originTip("topic"))
	}
	if len(h.ran) != 1 || h.ran[0].Approved != (removal.Needs{Remote: true}) {
		t.Fatalf("approvals handed to Run: %+v", h.ran)
	}
	if v := m.View(); !strings.Contains(v, "remote origin/topic: branch deleted") || !strings.Contains(v, tip) {
		t.Errorf("results lack the outcome or the restore tip:\n%s", v)
	}
}

// TestOriginPhraseApprovesEachItemItsOwnNeedsOnly ticks a proven origin row
// and a dirty worktree. What: "delete origin" runs both, but the origin item
// is approved for Remote alone and the worktree for confirm and force alone,
// never Remote. Why: approvals are per item; a mixed batch must not let one
// item's approval reach another (Ruling K).
func TestOriginPhraseApprovesEachItemItsOwnNeedsOnly(t *testing.T) {
	r := newRmRepo(t)
	r.originBranch("topic")
	r.dirty("wip")
	h := r.originHarness()
	m := setTicks(t, h.list(120, 50), "origin/topic", "wip")
	m = press(t, m, enterKey)
	m = press(t, typeWord(t, m, "delete origin"), enterKey)
	if len(h.ran) != 2 {
		t.Fatalf("ran %d items, status %q", len(h.ran), m.status)
	}
	for _, it := range h.ran {
		switch it.Kind {
		case cleanup.KindRemote:
			if it.Approved != (removal.Needs{Remote: true}) {
				t.Errorf("origin item approved %+v", it.Approved)
			}
		default:
			if it.Approved.Remote || !it.Approved.Force || !it.Approved.Confirm {
				t.Errorf("worktree item approved %+v", it.Approved)
			}
		}
	}
}

// TestOriginConfirmFitsEightyByTwentyFour renders the confirm screen of an
// origin deletion at 80x24. What: no line is wider than 80 and the screen is
// at most 24 lines, with the banner and the phrase prompt still on it. Why:
// the TUI rule is that a view fits the terminal; the extra banner must not
// push the prompt off a small screen.
func TestOriginConfirmFitsEightyByTwentyFour(t *testing.T) {
	r := newRmRepo(t)
	r.originBranch("topic")
	h := r.originHarness()
	m := setTicks(t, h.list(80, 24), "origin/topic")
	m = press(t, m, enterKey)
	v := m.View()
	lines := strings.Split(v, "\n")
	if len(lines) > 24 {
		t.Errorf("%d lines, want at most 24:\n%s", len(lines), v)
	}
	for _, l := range lines {
		if w := ansi.StringWidth(l); w > 80 {
			t.Errorf("line of width %d: %q", w, l)
		}
	}
	if !strings.Contains(squash(v), squash("delete origin")) || !strings.Contains(squash(v), squash("visible to everyone")) {
		t.Errorf("banner or prompt missing at 80x24:\n%s", v)
	}
}
