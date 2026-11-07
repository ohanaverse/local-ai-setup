package rescue

import (
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// TestOfferedScreensRowsWithoutGit checks which listed rows get a rescue
// offer: worktrees with something to keep (dirty, unpushed commits, a unique
// detached HEAD, or unpushed unknown), including a current or locked one; and
// not primary, mid-operation, unreadable, clean, branch or remote rows. Why: the
// list's `r` key must not offer what Preflight is certain to refuse.
func TestOfferedScreensRowsWithoutGit(t *testing.T) {
	wt := func(l cleanup.Losses, b ...string) cleanup.Row {
		r := cleanup.Row{Kind: cleanup.KindWorktree, Name: "x", Losses: l}
		for _, c := range b {
			r.Blockers = append(r.Blockers, cleanup.Reason{Code: c})
		}
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
		{"locked and dirty", wt(cleanup.Losses{Dirty: true}, cleanup.BlockLocked, cleanup.BlockHeld), true, ""},
		{"clean", wt(cleanup.Losses{}), false, "nothing to keep"},
		{"ignored only", wt(cleanup.Losses{IgnoredEntries: 3}), false, "nothing to keep"},
		{"primary", wt(cleanup.Losses{Dirty: true}, cleanup.BlockPrimary), false, "primary"},
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
