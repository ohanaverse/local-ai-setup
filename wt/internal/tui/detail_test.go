package tui

import (
	"io"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/removal"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// The detail screen: one phase of the list that shows a row in full and
// offers only the actions that apply to it. These tests drive it through
// Update over hand-built reports (the engine's preview is faked per call)
// and pin the three things the screen exists for: it opens without acting,
// it says everything about the row, and it offers exactly the actions the
// row's own predicates allow.

// detailLists is what a wired RemoveFuncs.Detail answers in these tests: one
// commit, one uncommitted and one ignored file.
func detailLists() cleanup.RowDetail {
	return cleanup.RowDetail{
		Commits:     []cleanup.DetailCommit{{Hash: "1a2b3c4d", Subject: "fix the thing"}},
		Uncommitted: []cleanup.DetailFile{{Status: "M", Path: "a.txt"}},
		Ignored:     []string{".env.local"},
	}
}

// detailPreview is a proven preview of row, as previewCmd checks it: the item
// the row builds, its fresh re-classification, and the losses it carries.
func detailPreview(row cleanup.Row) removal.ItemPreview {
	return removal.ItemPreview{
		Item:     removal.ItemFromRow(row),
		Fresh:    row,
		Approval: removal.ApprovalProven,
	}
}

// detailPreviewFn answers one preview per requested row, in order.
func detailPreviewFn(rows []cleanup.Row) []removal.ItemPreview {
	out := make([]removal.ItemPreview, len(rows))
	for i, r := range rows {
		out[i] = detailPreview(r)
	}
	return out
}

// detailReady builds the manage list over rep with the detail funcs wired:
// RemoveFuncs.Preview answers a proven preview per row and Detail answers
// lists (nil leaves Detail unwired, which the screen must survive).
func detailReady(t *testing.T, rep cleanup.Report, lists *cleanup.RowDetail) cleanupListModel {
	t.Helper()
	m := newSizedCleanupList(t, rep, 100, 40)
	m.rm.Preview = detailPreviewFn
	m.rm.Run = func([]removal.Item, func(removal.Outcome)) []removal.Outcome { return nil }
	m.rm.Rescue = func(cleanup.Row, io.Reader, io.Writer, io.Writer) error { return nil }
	m.rm.RestoreRoot = func(io.Reader, io.Writer, io.Writer) error { return nil }
	if lists != nil {
		d := *lists
		m.rm.Detail = func(cleanup.Row) cleanup.RowDetail { return d }
	}
	return m
}

// detailActionKeys is the keys of the detail screen's action list.
func detailActionKeys(m cleanupListModel) []string {
	var out []string
	for _, a := range m.detailActions() {
		out = append(out, a.Key)
	}
	return out
}

// wantKeys compares a key list with the wanted one.
func wantKeys(t *testing.T, what string, got, want []string) {
	t.Helper()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("%s: actions %v, want %v", what, got, want)
	}
}

// detailRootReport is the root on its default branch: nothing can be done
// with it but go back.
func detailRootReport() cleanup.Report {
	return cleanup.Report{
		Repo: "/repo", Base: cleanup.Base{Default: "main", Ref: "origin/main"},
		Fetch: cleanup.FetchResult{OK: true, At: time.Now()}, GH: cleanup.GHOK,
		Rows: []cleanup.Row{{
			Kind: cleanup.KindWorktree, Name: "main", Path: "/repo", Branch: "main", Ref: "refs/heads/main",
			Primary: true, Head: strings.Repeat("a", 40), Status: cleanup.StatusInUse,
			Blockers: []cleanup.Reason{{Code: cleanup.BlockPrimary}, {Code: cleanup.BlockDefaultBranch}},
		}},
	}
}

// detailWorktreeRow is a worktree row with local work to keep.
func detailWorktreeRow(name string) cleanup.Row {
	return cleanup.Row{
		Kind: cleanup.KindWorktree, Name: name, Path: "/repo/.worktrees/" + name, Branch: name, Ref: "refs/heads/" + name,
		Head: strings.Repeat("b", 40), Status: cleanup.StatusUniqueWork,
		Losses:   cleanup.Losses{Dirty: true, Unpushed: 2},
		Diff:     cleanup.DiffStat{Added: 3, Removed: 1},
		AgeDays:  2,
		AgeKnown: true,
	}
}

// TestIKeyOpensDetailWithoutActing pins that every key that opens the screen
// opens it and does nothing else: `i`, `x`, `d`, `r`, `R` and `o` all land on
// the detail phase with the program still running, and none of the removal
// or hand-off funcs is called. Why: the screen is where the user decides; a
// key that removed (or quit into `wt rescue`) on the way in would decide for
// them.
func TestIKeyOpensDetailWithoutActing(t *testing.T) {
	for _, key := range []string{"i", "x", "d", "r", "R", "o"} {
		rep := cleanup.Report{
			Repo: "/repo", Base: cleanup.Base{Default: "main", Ref: "origin/main"},
			Fetch: cleanup.FetchResult{OK: true, At: time.Now()}, GH: cleanup.GHOK,
			Rows: []cleanup.Row{detailWorktreeRow("topic")},
		}
		m := detailReady(t, rep, nil)
		previews := 0
		acted := false
		m.rm.Preview = func(rows []cleanup.Row) []removal.ItemPreview {
			previews++
			return detailPreviewFn(rows)
		}
		m.rm.Run = func([]removal.Item, func(removal.Outcome)) []removal.Outcome { acted = true; return nil }
		m.rm.Rescue = func(cleanup.Row, io.Reader, io.Writer, io.Writer) error { acted = true; return nil }
		m.rm.RestoreRoot = func(io.Reader, io.Writer, io.Writer) error { acted = true; return nil }
		m = press(t, m, keyRunes(key))
		if m.phase != cleanupPhaseDetail {
			t.Errorf("%q: phase = %v, want the detail screen (status %q)", key, m.phase, m.status)
		}
		if m.done || m.restoreRoot || m.rescueRow != nil {
			t.Errorf("%q: the list acted (done %v, restoreRoot %v, rescue %v)", key, m.done, m.restoreRoot, m.rescueRow)
		}
		if acted {
			t.Errorf("%q: a removal or hand-off ran on the way into the screen", key)
		}
		// The read the screen does itself: the engine's preview (read-only, so
		// `i` and `x` show the same "will lose" lines).
		if previews != 1 {
			t.Errorf("%q: the screen made %d preview reads, want 1", key, previews)
		}
	}
}

// TestDetailBlockShowsRowFacts pins the block's content for a worktree row:
// the full path exactly once, the branch, each commit's hash and subject, the
// uncommitted file with its status letter, and the ignored path. Why: these
// are what the user decides on — a block that hides the path or the files
// makes the removal a guess.
func TestDetailBlockShowsRowFacts(t *testing.T) {
	row := detailWorktreeRow("topic")
	d := &rowDetail{row: row, lists: ptrDetail(detailLists())}
	block := detailBlock(cleanup.Report{Repo: "/repo", Rows: []cleanup.Row{row}}, row, d, themes.Default, 100)
	text := strings.Join(block, "\n")
	if n := strings.Count(text, row.Path); n != 1 {
		t.Errorf("the full path appears %d times, want once:\n%s", n, text)
	}
	for _, want := range []string{"branch", "topic", "1a2b3c4d fix the thing", "M  a.txt", ".env.local"} {
		if !strings.Contains(text, want) {
			t.Errorf("block lacks %q:\n%s", want, text)
		}
	}
	// The name is not printed twice when the directory is named after it.
	if n := strings.Count(text, "topic"); n > 2 {
		t.Errorf("the worktree name is repeated (%d mentions of topic):\n%s", n, text)
	}
}

// TestDetailActionsOnlyWhatApplies pins the action list per row shape: the
// root offers only esc (plus R off its default branch), a clean worktree x
// and d but no rescue, a dirty one the rescue too, a branch row no rescue and
// no R, a row with an origin ref the `o`, and a locked row only esc. Why: an
// action shown when it cannot apply is an offer the handler then refuses.
func TestDetailActionsOnlyWhatApplies(t *testing.T) {
	// The root on its default branch.
	m := press(t, detailReady(t, detailRootReport(), nil), keyRunes("i"))
	wantKeys(t, "root on main", detailActionKeys(m), []string{"esc"})

	// The root off its default branch: restore-root is offered, nothing else.
	rep := detailRootReport()
	rep.Rows[0].Branch, rep.Rows[0].Ref = "topic", "refs/heads/topic"
	rep.Rows[0].Blockers = []cleanup.Reason{{Code: cleanup.BlockPrimary}}
	rep.Rows[0].Reasons = []cleanup.Reason{{Code: cleanup.ReasonRootDrift, Detail: "root is on topic, not main"}}
	rep.Rows[0].Status = cleanup.StatusLessSure
	m = press(t, detailReady(t, rep, nil), keyRunes("i"))
	wantKeys(t, "root off its default", detailActionKeys(m), []string{"R", "esc"})

	// A clean worktree with no local work: no rescue.
	clean := detailWorktreeRow("clean")
	clean.Status, clean.Losses, clean.Diff = cleanup.StatusProven, cleanup.Losses{}, cleanup.DiffStat{}
	m = press(t, detailReady(t, cleanup.Report{Repo: "/repo", Base: cleanup.Base{Default: "main"}, Rows: []cleanup.Row{clean}}, nil), keyRunes("i"))
	wantKeys(t, "clean worktree", detailActionKeys(m), []string{"x", "d", "esc"})

	// A dirty worktree: rescue offered.
	m = press(t, detailReady(t, cleanup.Report{Repo: "/repo", Base: cleanup.Base{Default: "main"}, Rows: []cleanup.Row{detailWorktreeRow("dirty")}}, nil), keyRunes("i"))
	wantKeys(t, "dirty worktree", detailActionKeys(m), []string{"x", "d", "r", "esc"})

	// A branch-only row: no rescue (there is no worktree to copy) and no R.
	branch := cleanup.Row{Kind: cleanup.KindBranch, Name: "solo", Branch: "solo", Ref: "refs/heads/solo", Status: cleanup.StatusProven}
	m = press(t, detailReady(t, cleanup.Report{Repo: "/repo", Base: cleanup.Base{Default: "main"}, Rows: []cleanup.Row{branch}}, nil), keyRunes("i"))
	wantKeys(t, "branch row", detailActionKeys(m), []string{"x", "d", "esc"})

	// A local row with a proven origin ref: `o` is offered beside x and d.
	withup := branch
	withup.Name, withup.Branch, withup.Ref = "withup", "withup", "refs/heads/withup"
	withup.Upstream = &cleanup.Upstream{Ref: "refs/remotes/origin/withup", Remote: "origin", Name: "withup"}
	origin := cleanup.Row{Kind: cleanup.KindRemote, Name: "withup", Branch: "withup", Ref: "refs/remotes/origin/withup",
		Remote: "origin", Status: cleanup.StatusProven, Evidence: cleanup.EvAncestor}
	m = press(t, detailReady(t, cleanup.Report{Repo: "/repo", Base: cleanup.Base{Default: "main"}, Rows: []cleanup.Row{withup}, Folded: []cleanup.Row{origin}}, nil), keyRunes("i"))
	wantKeys(t, "row with an origin ref", detailActionKeys(m), []string{"x", "d", "o", "esc"})

	// A locked non-root row: no action, and the reason is on the screen.
	locked := detailWorktreeRow("busy")
	locked.Status = cleanup.StatusInUse
	locked.Blockers = []cleanup.Reason{{Code: cleanup.BlockCurrent}}
	locked.Losses, locked.Diff = cleanup.Losses{}, cleanup.DiffStat{} // nothing to rescue
	m = detailReady(t, cleanup.Report{Repo: "/repo", Base: cleanup.Base{Default: "main"}, Rows: []cleanup.Row{locked}}, nil)
	m = press(t, m, keyRunes("i"))
	wantKeys(t, "locked row", detailActionKeys(m), []string{"esc"})
	if v := m.View(); !strings.Contains(squash(v), squash("you are here")) {
		t.Errorf("a locked row's screen does not say why:\n%s", v)
	}
}

// TestRootDetailVerdictLineSaysWhy pins the root's Verdict line: it says the
// root cannot be removed (a fact, not an action row), and the block carries
// no line starting with an action key. Why: the old screen's footer offered
// keys whose handlers refused; the reason has to live in the reading part.
func TestRootDetailVerdictLineSaysWhy(t *testing.T) {
	m := detailReady(t, detailRootReport(), nil)
	m = press(t, m, keyRunes("i"))
	block := detailBlock(m.report, m.rowDetail.row, m.rowDetail, themes.Default, 100)
	text := strings.Join(block, "\n")
	if !strings.Contains(text, "the root cannot be removed") {
		t.Errorf("the Verdict line does not say the root cannot be removed:\n%s", text)
	}
	for _, line := range block {
		trim := strings.TrimSpace(line)
		for _, key := range []string{"x ", "d ", "r ", "R ", "o "} {
			if strings.HasPrefix(trim, key) {
				t.Errorf("the block carries an action line %q:\n%s", trim, text)
			}
		}
	}
	if strings.Contains(strings.Join(detailActionKeys(m), ","), "x") {
		t.Errorf("the root offers a removal action: %v", detailActionKeys(m))
	}
}

// TestDetailWhileFillingHasNoRemovalActions pins the fill in flight: `i` opens
// the screen on a pending row, it says the rest is still loading, and it
// offers no removal action (the removal funcs are built from the filled
// snapshot only). Why: an action offered before the gates are read would run
// against facts the engine never judged.
func TestDetailWhileFillingHasNoRemovalActions(t *testing.T) {
	rep := cleanup.Report{
		Repo: "/repo", Base: cleanup.Base{Default: "main", Ref: "origin/main"},
		Fetch: cleanup.FetchResult{OK: true, At: time.Now()}, GH: cleanup.GHOK,
		Rows: []cleanup.Row{{
			Kind: cleanup.KindWorktree, Name: "pending", Path: "/repo/.worktrees/pending", Branch: "pending",
			Ref: "refs/heads/pending", Pending: true, Status: cleanup.StatusLessSure,
		}},
	}
	m := detailReady(t, rep, nil)
	if !m.filling() {
		t.Fatal("the fixture is not the facts stage")
	}
	// The first frame, before the screen's own read lands.
	next, cmd := m.Update(keyRunes("i"))
	m = next.(cleanupListModel)
	if m.phase != cleanupPhaseDetail {
		t.Fatalf("i on a pending row: phase = %v, want the detail screen", m.phase)
	}
	if v := m.View(); !strings.Contains(strings.ToLower(v), "loading") {
		t.Errorf("the screen does not say the rest is loading:\n%s", v)
	}
	// Once the read lands, the facts are there and no removal action is.
	if cmd != nil {
		next, _ := m.Update(cmd())
		m = next.(cleanupListModel)
	}
	wantKeys(t, "pending row", detailActionKeys(m), []string{"esc"})
}

// TestDetailEscReturnsToList pins the one way back: esc on the detail screen
// lands on the list with the cursor still on that row, and the program runs
// on. Why: the screen is a look at a row, not a mode that traps the user.
func TestDetailEscReturnsToList(t *testing.T) {
	m := detailReady(t, cleanup.Report{Repo: "/repo", Base: cleanup.Base{Default: "main"}, Rows: []cleanup.Row{detailWorktreeRow("topic")}}, nil)
	m = press(t, m, keyRunes("i"))
	m = press(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.phase != cleanupPhaseList || m.done || m.rowDetail != nil {
		t.Errorf("esc: phase %v, done %v, detail %v; want the list", m.phase, m.done, m.rowDetail != nil)
	}
	if row, ok := m.selectedRow(); !ok || row.Name != "topic" {
		t.Errorf("cursor left the row: %+v (ok %v)", row, ok)
	}
}

// TestDetailKeysRoute pins what each key does on the screen: `x`, `d` and `o`
// start the existing preview flow for that row, `r` and `R` keep the
// quit-and-hand-off flow. Why: the screen offers actions, it does not
// reimplement them — a key that took a different path would skip the
// confirm screen or the hand-off's own prompts.
func TestDetailKeysRoute(t *testing.T) {
	rep := func() cleanup.Report {
		return cleanup.Report{Repo: "/repo", Base: cleanup.Base{Default: "main", Ref: "origin/main"},
			Fetch: cleanup.FetchResult{OK: true, At: time.Now()}, GH: cleanup.GHOK,
			Rows: []cleanup.Row{detailWorktreeRow("topic")}}
	}
	for _, tc := range []struct {
		key  string
		want cleanupPhase
	}{{"x", cleanupPhaseConfirm}, {"d", cleanupPhaseConfirm}} {
		var previewed []string
		m := detailReady(t, rep(), nil)
		m.rm.Preview = func(rows []cleanup.Row) []removal.ItemPreview {
			for _, r := range rows {
				previewed = append(previewed, cleanup.DisplayName(r))
			}
			return detailPreviewFn(rows)
		}
		m = press(t, m, keyRunes("i"))
		m = press(t, m, keyRunes(tc.key))
		if m.phase != tc.want {
			t.Errorf("%q: phase = %v, want %v (status %q)", tc.key, m.phase, tc.want, m.status)
		}
		// Two reads of the same row: the screen's own on the way in, then the
		// action's fresh re-check before the confirm.
		if strings.Join(previewed, ",") != "topic,topic" {
			t.Errorf("%q previewed %v, want the shown row twice", tc.key, previewed)
		}
		if !m.confFromDetail {
			t.Errorf("%q: confFromDetail is false; the confirm would not return to the screen", tc.key)
		}
	}
	// `o` on a row with an origin ref previews the origin row, not the local
	// one, under the engine's own remote-only item.
	withup := cleanup.Row{Kind: cleanup.KindBranch, Name: "withup", Branch: "withup", Ref: "refs/heads/withup",
		Status: cleanup.StatusProven, Upstream: &cleanup.Upstream{Ref: "refs/remotes/origin/withup", Remote: "origin", Name: "withup"}}
	origin := cleanup.Row{Kind: cleanup.KindRemote, Name: "withup", Branch: "withup", Ref: "refs/remotes/origin/withup",
		Remote: "origin", Status: cleanup.StatusProven, Evidence: cleanup.EvAncestor}
	var previewed []string
	m := detailReady(t, cleanup.Report{Repo: "/repo", Base: cleanup.Base{Default: "main"}, Rows: []cleanup.Row{withup}, Folded: []cleanup.Row{origin}}, nil)
	m.rm.Preview = func(rows []cleanup.Row) []removal.ItemPreview {
		for _, r := range rows {
			previewed = append(previewed, string(r.Kind)+" "+cleanup.DisplayName(r))
		}
		return detailPreviewFn(rows)
	}
	m = press(t, m, keyRunes("i"))
	m = press(t, m, keyRunes("o"))
	if m.phase != cleanupPhaseConfirm || !strings.Contains(strings.Join(previewed, ","), "remote origin/withup") {
		t.Errorf("o: phase %v, previewed %v; want the origin ref's confirm", m.phase, previewed)
	}
	// `r` and `R` quit into their hand-off.
	m = detailReady(t, rep(), nil)
	m.rm.Rescue = func(cleanup.Row, io.Reader, io.Writer, io.Writer) error { return nil }
	m = press(t, m, keyRunes("i"))
	m = press(t, m, keyRunes("r"))
	if !m.done || m.rescueRow == nil || m.rescueRow.Name != "topic" {
		t.Errorf("r: done %v, rescueRow %+v; want the hand-off", m.done, m.rescueRow)
	}
	rootRep := detailRootReport()
	rootRep.Rows[0].Branch, rootRep.Rows[0].Ref = "topic", "refs/heads/topic"
	rootRep.Rows[0].Blockers = []cleanup.Reason{{Code: cleanup.BlockPrimary}}
	rootRep.Rows[0].Status = cleanup.StatusLessSure
	m = detailReady(t, rootRep, nil)
	m.rm.RestoreRoot = func(io.Reader, io.Writer, io.Writer) error { return nil }
	m = press(t, m, keyRunes("i"))
	m = press(t, m, keyRunes("R"))
	if !m.done || !m.restoreRoot {
		t.Errorf("R: done %v, restoreRoot %v; want the hand-off", m.done, m.restoreRoot)
	}
}

// TestDetailClosesWhenRowVanishes pins the stale screen: when a refreshed
// report no longer carries the row the screen was opened on (an F refetch, a
// return from a hand-off), the screen closes and the status line says the row
// is gone. Why: a screen describing a row that no longer exists would offer
// actions against a stale plan.
func TestDetailClosesWhenRowVanishes(t *testing.T) {
	m := detailReady(t, cleanup.Report{Repo: "/repo", Base: cleanup.Base{Default: "main"}, Rows: []cleanup.Row{detailWorktreeRow("topic")}}, nil)
	m = press(t, m, keyRunes("i"))
	next, _ := m.Update(cleanupFillMsg{report: cleanup.Report{Repo: "/repo", Base: cleanup.Base{Default: "main"}}, gen: m.fillGen})
	fm := next.(cleanupListModel)
	if fm.phase != cleanupPhaseList {
		t.Errorf("phase = %v, want the list", fm.phase)
	}
	if !strings.Contains(fm.status, "gone") {
		t.Errorf("status %q does not say the row is gone", fm.status)
	}
}

// TestDetailScrollsAndFits80x24 pins the small terminal: sixty uncommitted
// files render at most 24 lines, pgdown moves what is shown, and the actions
// section is reachable at the end. Why: the row list must fit 80x24, and the
// detail screen is the one place a long listing is allowed to scroll instead
// of growing the frame.
func TestDetailScrollsAndFits80x24(t *testing.T) {
	var files []cleanup.DetailFile
	for i := 0; i < 60; i++ {
		files = append(files, cleanup.DetailFile{Status: "M", Path: strings.Repeat("f", 20) + "-" + string(rune('a'+i%26)) + ".txt"})
	}
	lists := cleanup.RowDetail{Uncommitted: files}
	m := newSizedCleanupList(t, cleanup.Report{Repo: "/repo", Base: cleanup.Base{Default: "main"}, Rows: []cleanup.Row{detailWorktreeRow("topic")}}, 80, 24)
	m.rm.Preview = detailPreviewFn
	m.rm.Detail = func(cleanup.Row) cleanup.RowDetail { return lists }
	m = press(t, m, keyRunes("i"))
	view := m.View()
	if h := lipgloss.Height(view); h > 24 {
		t.Errorf("the screen is %d lines at 24:\n%s", h, view)
	}
	for _, line := range strings.Split(view, "\n") {
		if w := lipgloss.Width(line); w > 80 {
			t.Errorf("a line is %d columns wide: %q", w, line)
		}
	}
	before := m.rowDetail.offset
	m = press(t, m, tea.KeyMsg{Type: tea.KeyPgDown})
	if m.rowDetail.offset <= before {
		t.Errorf("pgdown left the offset at %d", m.rowDetail.offset)
	}
	// Scrolling to the end keeps the actions on screen.
	for i := 0; i < 40; i++ {
		m = press(t, m, tea.KeyMsg{Type: tea.KeyPgDown})
	}
	view = m.View()
	if !strings.Contains(view, "esc") {
		t.Errorf("the actions section is not reachable at the end:\n%s", view)
	}
	if h := lipgloss.Height(view); h > 24 {
		t.Errorf("at the end the screen is %d lines at 24:\n%s", h, view)
	}
}

// TestRowKeysListI pins that `i` is in the footer on every row, the root and a
// remote row included: it is the one key that only reads. Why: a key the
// footer hides is a key the user never finds.
func TestRowKeysListI(t *testing.T) {
	for _, mode := range []listMode{modeManage, modeLaunch} {
		m := newRowList(footerKeysReport(), rowListOpts{Mode: mode, Theme: themes.Default, Now: time.Now(), RemoveFuncs: wiredRemoveFuncs()})
		m = cleanupSend(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
		for i, it := range m.items {
			if it.newWorktree {
				continue
			}
			m.cursor = i
			if !hasKey(m.rowKeys(), "i") {
				t.Errorf("mode %v row %q: the footer does not offer i", mode, cleanup.DisplayName(it.row))
			}
		}
	}
}

// pressRowAction presses key on the list — which opens the detail screen on
// the highlighted row — and then key again on that screen: the two-key path
// every row action takes now (detail.go).
func pressRowAction(t *testing.T, m cleanupListModel, key string) cleanupListModel {
	t.Helper()
	first := press(t, m, keyRunes(key))
	if first.phase != cleanupPhaseDetail {
		t.Fatalf("%q did not open the detail screen: phase %v (status %q)", key, first.phase, first.status)
	}
	return press(t, first, keyRunes(key))
}

// pickerRowAction sends key twice through the picker: the first press opens
// the row's detail screen, the second starts the action from there — the path
// every row action takes now (detail.go).
func pickerRowAction(t *testing.T, m model, key string) model {
	t.Helper()
	m = pickerKey(t, m, keyRunes(key))
	if m.rows.phase != cleanupPhaseDetail {
		t.Fatalf("%q: phase %v, want the detail screen", key, m.rows.phase)
	}
	// The action's own command runs, its message left to the caller — as
	// pickerKey leaves it: these tests inject their own cleanupPreviewMsg.
	return pickerKey(t, m, keyRunes(key))
}

// lastPreviewed is what a row action previewed: the screen's own read of the
// row comes first (openDetail), the action's fresh re-check second.
func lastPreviewed(got []string) string {
	if len(got) == 0 {
		return ""
	}
	return got[len(got)-1]
}

// ptrDetail is a pointer to a copy of d.
func ptrDetail(d cleanup.RowDetail) *cleanup.RowDetail { return &d }
