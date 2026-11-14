package tui

import (
	"io"
	"slices"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/removal"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// wiredRemoveFuncs is a RemoveFuncs with every function set, so no handler
// refuses for want of a function and only the row decides.
func wiredRemoveFuncs() RemoveFuncs {
	return RemoveFuncs{
		Preview:     func([]cleanup.Row) []removal.ItemPreview { return nil },
		Run:         func([]removal.Item, func(removal.Outcome)) []removal.Outcome { return nil },
		Refresh:     func() (cleanup.Report, error) { return cleanup.Report{}, nil },
		Refetch:     func() (cleanup.Report, error) { return cleanup.Report{}, nil },
		Rescue:      func(cleanup.Row, io.Reader, io.Writer, io.Writer) error { return nil },
		RestoreRoot: func(io.Reader, io.Writer, io.Writer) error { return nil },
	}
}

// footerKeysReport has one row for each way the row keys can differ: a free
// worktree with work to keep, a locked worktree, a branch whose origin ref is
// a proven row, a branch with no origin ref, and the root off its default.
func footerKeysReport() cleanup.Report {
	return cleanup.Report{
		Repo: "/repo", Base: cleanup.Base{Default: "main", Ref: "origin/main"}, Fetch: cleanup.FetchResult{OK: true, At: time.Now()}, GH: cleanup.GHOK,
		Rows: []cleanup.Row{
			{Kind: cleanup.KindWorktree, Name: "topic", Path: "/repo", Branch: "topic", Ref: "refs/heads/topic", Primary: true,
				Head: "0123456789abcdef0123456789abcdef01234567", Status: cleanup.StatusLessSure,
				Blockers: []cleanup.Reason{{Code: cleanup.BlockPrimary}, {Code: cleanup.BlockCurrent}},
				Reasons:  []cleanup.Reason{{Code: cleanup.ReasonRootDrift, Detail: "root is on topic, not main"}}},
			{Kind: cleanup.KindWorktree, Name: "free", Path: "/repo/.worktrees/free", Branch: "free", Ref: "refs/heads/free",
				Status: cleanup.StatusUniqueWork, Losses: cleanup.Losses{Dirty: true}},
			{Kind: cleanup.KindWorktree, Name: "busy", Path: "/repo/.worktrees/busy", Branch: "busy", Ref: "refs/heads/busy",
				Status: cleanup.StatusInUse, Blockers: []cleanup.Reason{{Code: cleanup.BlockCurrent}}},
			{Kind: cleanup.KindBranch, Name: "withup", Branch: "withup", Status: cleanup.StatusProven, Evidence: cleanup.EvAncestor,
				Upstream: &cleanup.Upstream{Ref: "refs/remotes/origin/withup", Remote: "origin", Name: "withup"}},
			{Kind: cleanup.KindBranch, Name: "lone", Branch: "lone", Status: cleanup.StatusLessSure},
			{Kind: cleanup.KindRemote, Name: "withup", Branch: "withup", Ref: "refs/remotes/origin/withup", Remote: "origin",
				Status: cleanup.StatusProven, Evidence: cleanup.EvPRTip, PR: &cleanup.PRInfo{Number: 9, State: "MERGED"}},
		},
	}
}

func hasKey(hints []keyHint, key string) bool {
	return slices.ContainsFunc(hints, func(h keyHint) bool { return h.key == key })
}

// TestRowKeysMatchTheHandlers is the footer's parity guard: on every real row
// of both modes, with every function wired, a row key (x d o r R) is in
// rowKeys exactly when pressing it is not refused by its handler. Why: the
// footer is built from copies of the handlers' predicates, and a copy that
// drifts either hides a key that works or offers one that answers "cannot".
func TestRowKeysMatchTheHandlers(t *testing.T) {
	for _, mode := range []listMode{modeManage, modeLaunch} {
		m := newRowList(footerKeysReport(), rowListOpts{Mode: mode, Theme: themes.Default, Now: time.Now(), RemoveFuncs: wiredRemoveFuncs()})
		m = cleanupSend(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
		for i, it := range m.items {
			if it.heading != "" || it.newWorktree {
				continue
			}
			m.cursor = i
			hints := m.rowKeys()
			for _, key := range []string{"x", "d", "o", "r", "R"} {
				next, _ := m.Update(keyRunes(key))
				fm := next.(cleanupListModel)
				accepted := fm.phase != cleanupPhaseList || fm.done || fm.restoreRoot
				if got := hasKey(hints, key); got != accepted {
					t.Errorf("mode %v row %q key %q: in footer = %v, handler accepts = %v (status %q)",
						mode, cleanup.DisplayName(it.row), key, got, accepted, fm.status)
				}
			}
		}
	}
}

// TestRowKeysOfferNothingWhenRemovalIsNotWired pins that a list with no
// removal functions (or one whose registry did not load) offers no key whose
// handler would answer "not available": x, d, o, r; R follows its own func.
func TestRowKeysOfferNothingWhenRemovalIsNotWired(t *testing.T) {
	for name, rm := range map[string]RemoveFuncs{
		"zero": {},
		"off":  func() RemoveFuncs { r := wiredRemoveFuncs(); r.Off = "registry did not load"; return r }(),
	} {
		m := newRowList(footerKeysReport(), rowListOpts{Mode: modeManage, Theme: themes.Default, Now: time.Now(), RemoveFuncs: rm})
		m = moveTo(t, m, "free")
		hints := m.rowKeys()
		for _, key := range []string{"x", "d", "o"} {
			if hasKey(hints, key) {
				t.Errorf("%s: footer offers %q with removal unavailable", name, key)
			}
		}
		if name == "zero" && (hasKey(hints, "r") || hasKey(hints, "R")) {
			t.Errorf("zero: footer offers r or R with no Rescue/RestoreRoot wired")
		}
		if name == "off" && !hasKey(hints, "r") {
			t.Errorf("off: r does not need the registry; the footer should offer it on a row with work to keep")
		}
	}
}

// TestRowKeysOnTheNewWorktreeItemAndAHiddenCursor pins that the + New
// worktree… item and a cursor whose rows the filter hides get no row key, and
// a hidden cursor no enter-to-launch either.
func TestRowKeysOnTheNewWorktreeItemAndAHiddenCursor(t *testing.T) {
	m := newRowList(footerKeysReport(), rowListOpts{Mode: modeLaunch, Theme: themes.Default, Now: time.Now(), RemoveFuncs: wiredRemoveFuncs()})
	m.cursor = 0
	hints := m.rowKeys()
	if !hasKey(hints, "enter") || !hasKey(hints, "n") {
		t.Errorf("new-worktree item: want enter and n, got %+v", hints)
	}
	for _, key := range []string{"x", "d", "o", "r", "R"} {
		if hasKey(hints, key) {
			t.Errorf("new-worktree item offers row key %q", key)
		}
	}
	m = moveTo(t, m, "free")
	m.filter = "zzz-no-such-row"
	hints = m.rowKeys()
	for _, key := range []string{"x", "d", "o", "r", "R", "enter"} {
		if hasKey(hints, key) {
			t.Errorf("filter hides every row but the footer offers %q", key)
		}
	}
}

// TestRowKeysWhileFiltering pins that with the `/` input open the footer
// names only what that input owns: every printable key goes into the query.
func TestRowKeysWhileFiltering(t *testing.T) {
	m := newRowList(footerKeysReport(), rowListOpts{Mode: modeLaunch, Theme: themes.Default, Now: time.Now(), RemoveFuncs: wiredRemoveFuncs()})
	m.filtering = true
	var got []string
	for _, h := range m.rowKeys() {
		got = append(got, h.key)
	}
	want := []string{"↑/↓", "enter", "esc", "ctrl+c"}
	if !slices.Equal(got, want) {
		t.Errorf("filtering footer keys = %v, want %v", got, want)
	}
}

// TestRowKeysManageModeNamesTickKeys pins manage mode's own keys: space and
// a tick, enter removes the ticked rows; space is not offered on a locked row.
func TestRowKeysManageModeNamesTickKeys(t *testing.T) {
	m := newRowList(footerKeysReport(), rowListOpts{Mode: modeManage, Theme: themes.Default, Now: time.Now(), RemoveFuncs: wiredRemoveFuncs()})
	m = moveTo(t, m, "free")
	hints := m.rowKeys()
	for _, key := range []string{"space", "a", "enter"} {
		if !hasKey(hints, key) {
			t.Errorf("manage footer on a free row misses %q: %+v", key, hints)
		}
	}
	m = moveTo(t, m, "busy")
	if hasKey(m.rowKeys(), "space") {
		t.Errorf("manage footer offers space on a locked row")
	}
	if hasKey(m.rowKeys(), "n") || hasKey(m.rowKeys(), "/") {
		t.Errorf("manage footer offers launch-only keys")
	}
}
