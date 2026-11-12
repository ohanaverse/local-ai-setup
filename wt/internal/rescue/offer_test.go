package rescue

import (
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// TestOfferedScreensRowsWithoutGit checks which listed rows get a rescue
// offer: worktrees with something to keep (dirty, unpushed commits, a unique
// detached HEAD, or unpushed unknown), including a current or locked one and
// one carrying the held label; and not primary, mid-operation, unreadable,
// clean, branch or remote rows. Why: the list's `r` key must not offer what
// Preflight is certain to refuse.
func TestOfferedScreensRowsWithoutGit(t *testing.T) {
	wt := func(l cleanup.Losses, b ...string) cleanup.Row {
		r := cleanup.Row{Kind: cleanup.KindWorktree, Name: "x", Losses: l}
		for _, c := range b {
			r.Blockers = append(r.Blockers, cleanup.Reason{Code: c})
		}
		return r
	}
	// held carries the held label the way the classification raises it: a
	// cleanup.ReasonHeld reason, never a blocker.
	held := func(r cleanup.Row) cleanup.Row {
		r.Reasons = append(r.Reasons, cleanup.Reason{Code: cleanup.ReasonHeld})
		return r
	}
	for _, c := range []struct {
		name string
		row  cleanup.Row
		ok   bool
		why  string
	}{
		{"dirty", wt(cleanup.Losses{Dirty: true}), true, ""},
		{"unpushed", wt(cleanup.Losses{Unpushed: 2}), true, ""},
		{"unpushed unknown", wt(cleanup.Losses{Unpushed: -1}), true, ""},
		{"detached unique", wt(cleanup.Losses{DetachedUnique: true}), true, ""},
		{"current and dirty", wt(cleanup.Losses{Dirty: true}, cleanup.BlockCurrent), true, ""},
		{"locked, held and dirty", held(wt(cleanup.Losses{Dirty: true}, cleanup.BlockLocked)), true, ""},
		{"clean", wt(cleanup.Losses{}), false, "nothing to keep"},
		{"ignored only", wt(cleanup.Losses{IgnoredEntries: 3}), false, "nothing to keep"},
		{"primary", wt(cleanup.Losses{Dirty: true}, cleanup.BlockPrimary), false, "restore-root"},
		{"operation", wt(cleanup.Losses{Dirty: true}, cleanup.BlockOperation), false, "operation"},
		{"unreadable", wt(cleanup.Losses{GatesUnknown: true, Dirty: true}), false, "could not be read"},
		{"branch", cleanup.Row{Kind: cleanup.KindBranch, Name: "b"}, false, "wt -W b"},
		{"remote", cleanup.Row{Kind: cleanup.KindRemote, Name: "b"}, false, "worktree"},
	} {
		ok, why := Offered(c.row)
		if ok != c.ok || !strings.Contains(why, c.why) {
			t.Errorf("%s: Offered = %v, %q; want %v, containing %q", c.name, ok, why, c.ok, c.why)
		}
	}
}

// TestOfferedStillOffersAPreservedRow checks that a worktree row with a
// rescue record applied (Preserved set) and raw Unpushed commits is still
// offered a rescue. Why: Offered reads the raw losses, so a second rescue of
// an already-preserved original stays possible; the effective view would
// report "nothing to keep".
func TestOfferedStillOffersAPreservedRow(t *testing.T) {
	row := cleanup.Row{Kind: cleanup.KindWorktree, Name: "x",
		Losses:    cleanup.Losses{Unpushed: 3},
		Preserved: &cleanup.Preserved{Ref: "refs/wt/rescued/0123456789abcdef", DestBranch: "rescued/x", DestPath: "/copy"}}
	if ok, why := Offered(row); !ok || why != "" {
		t.Errorf("Offered = %v, %q; want true, empty", ok, why)
	}
}

// TestRescueStillOffersDirtyPreserved checks that a worktree row whose record
// covers its uncommitted work (CoversDirt, raw Dirty still set) is still
// offered a rescue. Why: Offered reads raw losses so a second rescue of a
// dirty, already-preserved original stays possible; the effective view
// clears Dirty and would say "nothing to keep".
func TestRescueStillOffersDirtyPreserved(t *testing.T) {
	row := cleanup.Row{Kind: cleanup.KindWorktree, Name: "x",
		Losses:    cleanup.Losses{Dirty: true},
		Preserved: &cleanup.Preserved{Ref: "refs/wt/rescued/0123456789abcdef", DestBranch: "rescued/x", DestPath: "/copy", CoversDirt: true}}
	if ok, why := Offered(row); !ok || why != "" {
		t.Errorf("Offered = %v, %q; want true, empty", ok, why)
	}
}
