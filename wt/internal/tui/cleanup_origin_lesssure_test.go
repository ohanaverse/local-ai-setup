package tui

import (
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/removal"
)

// lessSureOriginBranch pushes branch at main's tip with no PR anywhere: its
// content is in the base but nothing merged it, so after a fetch the row is a
// known less_sure origin row. It returns the tip.
func (r *rmRepo) lessSureOriginBranch(branch string) string {
	r.t.Helper()
	r.git("branch", branch)
	r.git("push", "-u", "origin", branch)
	r.git("branch", "-D", branch) // so origin/<branch> is a row of its own
	return r.git("rev-parse", "main")
}

// TestCleanupLockedOriginRows is cleanupLocked over origin rows. What: proven
// and known less-sure rows without a blocker are removable; an Unknown row,
// unique work, a hard stop and another remote's row are locked. Why: the list
// may offer only what the engine will do, and the engine fails closed on
// anything it could not read.
func TestCleanupLockedOriginRows(t *testing.T) {
	origin := func(mod func(r *cleanup.Row)) cleanup.Row {
		r := cleanup.Row{Kind: cleanup.KindRemote, Name: "x", Remote: "origin", Ref: "refs/remotes/origin/x", Status: cleanup.StatusProven}
		mod(&r)
		return r
	}
	cases := []struct {
		name string
		row  cleanup.Row
		want bool
	}{
		{"proven", origin(func(*cleanup.Row) {}), false},
		{"less sure and known", origin(func(r *cleanup.Row) { r.Status = cleanup.StatusLessSure }), false},
		{"less sure and unknown", origin(func(r *cleanup.Row) { r.Status = cleanup.StatusLessSure; r.Unknown = true }), true},
		{"unique work", origin(func(r *cleanup.Row) { r.Status = cleanup.StatusUniqueWork }), true},
		{"in use", origin(func(r *cleanup.Row) { r.Status = cleanup.StatusInUse }), true},
		{"less sure, base of an open PR", origin(func(r *cleanup.Row) {
			r.Status = cleanup.StatusLessSure
			r.Blockers = []cleanup.Reason{{Code: cleanup.BlockBaseOfOpenPR}}
			r.HardStop = cleanup.BlockBaseOfOpenPR
		}), true},
		{"less sure on another remote", origin(func(r *cleanup.Row) { r.Status = cleanup.StatusLessSure; r.Remote = "upstream" }), true},
	}
	for _, c := range cases {
		if got := cleanupLocked(c.row); got != c.want {
			t.Errorf("%s: cleanupLocked = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestLessSureOriginNeedsTheOriginPhraseAndBothApprovals ticks a known
// less-sure origin row and walks the confirm screen. What: the screen names
// the loss approval and the origin phrase; plain `delete` runs nothing;
// `delete origin` runs it with Remote and Confirm together, and origin loses
// the branch. Why: this is the approved escape for a branch wt could not
// prove merged; it must stay a deliberate, typed, origin-specific act.
func TestLessSureOriginNeedsTheOriginPhraseAndBothApprovals(t *testing.T) {
	r := newRmRepo(t)
	tip := r.lessSureOriginBranch("topic")
	h := r.originHarness()
	open := func() cleanupListModel {
		m := setTicks(t, h.list(120, 40), "origin/topic")
		m = press(t, m, enterKey)
		if m.phase != cleanupPhaseConfirm {
			t.Fatalf("phase %v, status %q", m.phase, m.status)
		}
		return m
	}
	v := open().View()
	for _, want := range []string{"ORIGIN BRANCHES", "--accept-loss", "delete origin"} {
		if !strings.Contains(v, want) && !strings.Contains(squash(v), squash(want)) {
			t.Errorf("confirm screen lacks %q:\n%s", want, v)
		}
	}
	m2 := press(t, typeWord(t, open(), "delete"), enterKey)
	if m2.phase == cleanupPhaseResults || h.runCalls != 0 || r.originTip("topic") != tip {
		t.Fatalf("plain delete: phase %v runs %d origin %q", m2.phase, h.runCalls, r.originTip("topic"))
	}
	m := press(t, typeWord(t, open(), "delete origin"), enterKey)
	if h.runCalls != 1 || r.originTip("topic") != "" || m.phase != cleanupPhaseResults {
		t.Fatalf("delete origin: runs %d origin %q phase %v", h.runCalls, r.originTip("topic"), m.phase)
	}
	if len(h.ran) != 1 || h.ran[0].Approved != (removal.Needs{Remote: true, Confirm: true}) {
		t.Fatalf("approvals handed to Run: %+v", h.ran)
	}
}

// TestMixedSelectionGivesEachItemOnlyItsOwnApprovals ticks a dirty worktree
// and a less-sure origin row together. What: one `delete origin` runs both;
// the worktree gets confirm and force, never Remote; the origin item gets
// Remote and confirm, never Force. Why: approvals are per item (Ruling K); a
// mixed batch must not let one item's approval reach another.
func TestMixedSelectionGivesEachItemOnlyItsOwnApprovals(t *testing.T) {
	r := newRmRepo(t)
	r.lessSureOriginBranch("topic")
	r.dirty("wip")
	h := r.originHarness()
	m := setTicks(t, h.list(120, 50), "origin/topic", "wip")
	m = press(t, m, enterKey)
	if m.conf == nil || m.conf.word() != "delete origin" {
		t.Fatalf("confirm is %+v (status %q), want the delete origin word", m.conf, m.status)
	}
	m = press(t, typeWord(t, m, "delete origin"), enterKey)
	if len(h.ran) != 2 {
		t.Fatalf("ran %d items, status %q", len(h.ran), m.status)
	}
	for _, it := range h.ran {
		switch it.Kind {
		case cleanup.KindRemote:
			if it.Approved != (removal.Needs{Remote: true, Confirm: true}) {
				t.Errorf("origin item approved %+v", it.Approved)
			}
		default:
			if it.Approved.Remote || !it.Approved.Force || !it.Approved.Confirm {
				t.Errorf("worktree item approved %+v", it.Approved)
			}
		}
	}
}
