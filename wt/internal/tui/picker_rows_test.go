package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
)

// The picker is a view over cleanup rows: these tests drive the app model
// from the rows load (loadRowsCmd) through the list's launch mode to the
// selectedEntryMsg the rest of the launch flow already handles.

// pickerFixture is a hand-built report for the picker: the root (standing in
// a linked worktree under it, so both are Current), another worktree, a
// local branch with no worktree and a remote-only branch.
func pickerFixture() cleanup.Report {
	return cleanup.Report{
		Repo: "/repo",
		Base: cleanup.Base{Default: "main", Ref: "origin/main"},
		GH:   cleanup.GHOK,
		Rows: []cleanup.Row{
			{Kind: cleanup.KindWorktree, Name: "main", Path: "/repo", Branch: "main", Ref: "refs/heads/main",
				Primary: true, Current: true, Status: cleanup.StatusInUse,
				Blockers: []cleanup.Reason{{Code: cleanup.BlockPrimary}, {Code: cleanup.BlockCurrent}}},
			{Kind: cleanup.KindWorktree, Name: "deep", Path: "/repo/.worktrees/deep", Branch: "deep", Ref: "refs/heads/deep",
				Current: true, Status: cleanup.StatusInUse, Blockers: []cleanup.Reason{{Code: cleanup.BlockCurrent}}},
			{Kind: cleanup.KindWorktree, Name: "other", Path: "/repo/.worktrees/other", Branch: "other", Ref: "refs/heads/other",
				Status: cleanup.StatusLessSure},
			{Kind: cleanup.KindBranch, Name: "loose", Branch: "loose", Ref: "refs/heads/loose", Status: cleanup.StatusLessSure},
			{Kind: cleanup.KindRemote, Name: "teammate", Branch: "teammate", Remote: "origin", Ref: "refs/remotes/origin/teammate",
				Status: cleanup.StatusLessSure},
		},
	}
}

// pickerReady is a sized model whose list was loaded from rep.
func pickerReady(t *testing.T, rep cleanup.Report) model {
	t.Helper()
	m := model{width: 100, height: 40, theme: themes.Default, cfg: &config.Config{}}
	m, _ = updateMsg(m, rowsLoadedMsg{report: rep, repoRoot: rep.Repo})
	if !m.ready {
		t.Fatalf("not ready after the rows load; status %q", m.status)
	}
	return m
}

// pickerCursorTo moves the picker's cursor to the row named name (display
// name), failing when no item has it.
func pickerCursorTo(t *testing.T, m model, name string) model {
	t.Helper()
	for i, it := range m.rows.items {
		if it.heading == "" && !it.newWorktree && cleanup.DisplayName(it.row) == name {
			m.rows.cursor = i
			return m
		}
	}
	t.Fatalf("no picker row %q", name)
	return m
}

// pickerEnter presses enter and follows the commands it leads to until a
// selectedEntryMsg comes out, which it returns without applying.
func pickerEnter(t *testing.T, m model) (model, selectedEntryMsg) {
	t.Helper()
	m, cmd := updateMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	for i := 0; cmd != nil && i < 5; i++ {
		msg := cmd()
		if sel, ok := msg.(selectedEntryMsg); ok {
			return m, sel
		}
		m, cmd = updateMsg(m, msg)
	}
	t.Fatal("enter never produced a selectedEntryMsg")
	return m, selectedEntryMsg{}
}

// pickerLoad runs loadRowsCmd for dir (no fetch, PR lookups through prList)
// and applies its message: the picker as it first paints.
func pickerLoad(t *testing.T, dir string, prList func(dir, branch string) ([]byte, error)) (model, tea.Cmd) {
	t.Helper()
	in := cleanup.GatherInputs{CWD: dir, OwnRepo: "o/r", Parallel: 2, PRList: prList,
		PRBaseList: func(string, string) ([]byte, error) { return []byte("[]"), nil }}
	msg := loadRowsCmd(dir, nil, in, false)()
	m := model{width: 120, height: 50, theme: themes.Default, cfg: &config.Config{}}
	return updateMsg(m, msg)
}

// claimRootByOther makes another live process hold dir's root with a session claim,
// so the root row is held by another session.
func claimRootByOther(t *testing.T, dir string) {
	t.Helper()
	sleep := exec.Command("sleep", "120")
	if err := sleep.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sleep.Process.Kill(); _ = sleep.Wait() })
	common := rmGit(t, dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	claims := filepath.Join(common, "wt-sessions")
	if err := os.MkdirAll(claims, 0o755); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("pid=%d\nstarted=%d\npath=%s\nbranch=\nagent=claude\n", sleep.Process.Pid, time.Now().Unix(), root)
	if err := os.WriteFile(filepath.Join(claims, fmt.Sprint(sleep.Process.Pid)), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestPickerLoadsRowsFromCleanupAndRendersRootDefaultAndHeld pins the end of
// the picker's filters: the rows come from cleanup.Gather, and the root (held
// by another session here), the default branch (a bare local main while the
// root is on another branch) and a worktree's branch all show. The old
// picker dropped the root, the bare default branch and any branch a worktree
// held, so a user could not see what git had.
func TestPickerLoadsRowsFromCleanupAndRendersRootDefaultAndHeld(t *testing.T) {
	r := newRmRepo(t)
	r.git("switch", "-c", "drift")
	r.worktree("topic")
	claimRootByOther(t, r.dir)
	m, _ := pickerLoad(t, r.dir, func(string, string) ([]byte, error) { return []byte("[]"), nil })
	if !m.ready {
		t.Fatalf("picker not ready: status %q, listError %q", m.status, m.listError)
	}
	var root, mainBranch, topic *cleanupItem
	for i := range m.rows.items {
		it := &m.rows.items[i]
		switch {
		case it.heading != "" || it.newWorktree:
		case it.row.Primary:
			root = it
		case it.row.Kind == cleanup.KindBranch && it.row.Name == "main":
			mainBranch = it
		case it.row.Kind == cleanup.KindWorktree && it.row.Name == "topic":
			topic = it
		}
	}
	if root == nil || mainBranch == nil || topic == nil {
		t.Fatalf("root %v, default branch %v, topic %v: want all three rows", root != nil, mainBranch != nil, topic != nil)
	}
	held := false
	for _, tag := range root.tags {
		held = held || strings.HasPrefix(tag.Text, "held by session")
	}
	if !held {
		t.Errorf("root tags %v, want a held-by-session tag", root.tags)
	}
	v := m.View()
	for _, want := range []string{"root", "held by session", "drift", "topic", "main"} {
		if !strings.Contains(v, want) {
			t.Errorf("view lacks %q:\n%s", want, v)
		}
	}
	// Every row is launchable: enter on the held root and on the default
	// branch both choose it.
	for _, name := range []string{"drift", "main"} {
		mm := pickerCursorTo(t, m, name)
		if _, sel := pickerEnter(t, mm); sel.entry.Branch != name {
			t.Errorf("enter on %s chose %+v", name, sel.entry)
		}
	}
}

// TestPickerStartsOnCurrentRow pins where the cursor opens: on the deepest
// worktree holding the caller's directory (cleanup marks every containing
// worktree Current, the root too), else on the root when standing in it.
// The old picker pinned the current row second; the list keeps its order and
// moves the cursor instead.
func TestPickerStartsOnCurrentRow(t *testing.T) {
	m := pickerReady(t, pickerFixture())
	if row, ok := m.rows.selectedRow(); !ok || row.Name != "deep" {
		t.Errorf("cursor on %+v (ok %v), want the deep worktree", row, ok)
	}
	inRoot := pickerFixture()
	inRoot.Rows[1].Current, inRoot.Rows[1].Blockers, inRoot.Rows[1].Status = false, nil, cleanup.StatusLessSure
	m = pickerReady(t, inRoot)
	if row, ok := m.rows.selectedRow(); !ok || !row.Primary {
		t.Errorf("cursor on %+v (ok %v), want the root row", row, ok)
	}
	// The labels the old picker drew: (current) on the current row and
	// (default) on the default branch's.
	tags := func(name string) string {
		var out []string
		for _, it := range m.rows.items {
			if !it.newWorktree && it.heading == "" && it.row.Name == name {
				for _, tg := range it.tags {
					out = append(out, tg.Text)
				}
			}
		}
		return strings.Join(out, ",")
	}
	if got := tags("main"); !strings.Contains(got, "current") || !strings.Contains(got, "default") {
		t.Errorf("root tags %q, want current and default", got)
	}
	if got := tags("other"); strings.Contains(got, "current") || strings.Contains(got, "default") {
		t.Errorf("other's tags %q, want neither current nor default", got)
	}
}

// TestPickerEnterOnRootRowLaunchesViaSelectedEntryMsg pins that the root row
// launches through the same selectedEntryMsg every other pick uses, as an
// entry marked Root with the root's path, so the shared-root check and the
// rest of the flow see it unchanged.
func TestPickerEnterOnRootRowLaunchesViaSelectedEntryMsg(t *testing.T) {
	m := pickerCursorTo(t, pickerReady(t, pickerFixture()), "main")
	_, sel := pickerEnter(t, m)
	if !sel.entry.Root || sel.entry.Path != "/repo" || sel.entry.Branch != "main" {
		t.Errorf("entry = %+v, want the root at /repo on main", sel.entry)
	}
}

// TestPickerEnterOnBareBranchRowCreatesWorktree pins that a local branch with
// no worktree still gets one when chosen: entryOfRow makes a branch entry
// with no path, and selectedEntryMsg's handler creates the worktree through
// ensureBranchWorktreeCmd, as the old picker's bare-branch row did.
func TestPickerEnterOnBareBranchRowCreatesWorktree(t *testing.T) {
	r := newRmRepo(t)
	r.loose("loose")
	m, _ := pickerLoad(t, r.dir, func(string, string) ([]byte, error) { return []byte("[]"), nil })
	m = pickerCursorTo(t, m, "loose")
	m, sel := pickerEnter(t, m)
	if sel.entry.Type != worktree.TypeBranch || sel.entry.Path != "" || sel.entry.Branch != "loose" {
		t.Fatalf("entry = %+v, want a bare branch entry for loose", sel.entry)
	}
	m, cmd := updateMsg(m, sel)
	if !m.creating || cmd == nil {
		t.Fatalf("creating %v, cmd %v: want a worktree create in flight", m.creating, cmd != nil)
	}
	created, ok := cmd().(branchWorktreeCreatedMsg)
	if !ok || created.err != nil {
		t.Fatalf("create = %+v", created)
	}
	if fi, err := os.Stat(created.path); err != nil || !fi.IsDir() {
		t.Errorf("worktree %s not created: %v", created.path, err)
	}
}

// TestPickerEnterOnRemoteOnlyRowUsesOriginForm pins the remote-only row's
// entry: the origin spelling ("origin/teammate") the old picker passed to
// EnsureForBranch, with RemoteBranch the bare name, so choosing it creates a
// local tracking branch and worktree as before.
func TestPickerEnterOnRemoteOnlyRowUsesOriginForm(t *testing.T) {
	m := pickerCursorTo(t, pickerReady(t, pickerFixture()), "origin/teammate")
	_, sel := pickerEnter(t, m)
	want := worktree.Entry{Type: worktree.TypeBranch, Branch: "origin/teammate", RemoteBranch: "teammate"}
	if sel.entry != want {
		t.Errorf("entry = %+v, want %+v", sel.entry, want)
	}
	if e := entryOfRow(cleanup.Row{Kind: cleanup.KindBranch, Name: "loose", Branch: "loose"}); e.Type != worktree.TypeBranch || e.Path != "" || e.Branch != "loose" {
		t.Errorf("branch row entry = %+v", e)
	}
	if e := entryOfRow(cleanup.Row{Kind: cleanup.KindWorktree, Name: "x", Path: "/repo/.worktrees/x", Branch: "x"}); e.Type != worktree.TypeWorktree || e.Path != "/repo/.worktrees/x" || e.Branch != "x" || e.Root {
		t.Errorf("worktree row entry = %+v", e)
	}
}

// TestPickerPRFillArrivesAfterFirstPaint pins the asynchronous PR lookup: the
// list paints from git alone with "PR ?" (no gh call has run), then the
// filled report swaps in with the PR tag and the cursor stays on the row the
// user had highlighted.
func TestPickerPRFillArrivesAfterFirstPaint(t *testing.T) {
	r := newRmRepo(t)
	r.loose("loose")
	r.worktree("topic")
	asked := 0
	prs := func(_, branch string) ([]byte, error) {
		asked++
		if branch == "loose" {
			return []byte(`[{"number":7,"state":"OPEN","headRefOid":"","baseRefName":"main"}]`), nil
		}
		return []byte("[]"), nil
	}
	m, cmd := pickerLoad(t, r.dir, prs)
	if asked != 0 {
		t.Fatalf("the first paint asked gh %d times, want none", asked)
	}
	if v := m.View(); !strings.Contains(v, "PR ?") {
		t.Fatalf("first view has no PR ?:\n%s", v)
	}
	if cmd == nil {
		t.Fatal("no PR fill command after the load")
	}
	m = pickerCursorTo(t, m, "loose")
	msg := cmd()
	if _, ok := msg.(rowPRsLoadedMsg); !ok {
		t.Fatalf("fill answered %T", msg)
	}
	m, _ = updateMsg(m, msg)
	if asked == 0 {
		t.Error("the fill asked gh nothing")
	}
	if row, ok := m.rows.selectedRow(); !ok || row.Name != "loose" {
		t.Errorf("cursor on %+v after the fill, want loose", row)
	}
	v := m.View()
	if !strings.Contains(v, "#7 open") {
		t.Errorf("filled view lacks #7 open:\n%s", v)
	}
	if strings.Contains(v, "PR ?") {
		t.Errorf("filled view still has PR ?:\n%s", v)
	}
	// The PR number finds its row, as the old picker's filter did.
	m.rows.filter = "#7"
	m.rows.settleCursor()
	if row, ok := m.rows.selectedRow(); !ok || row.Name != "loose" || m.rows.hidden(m.rows.cursor) {
		t.Errorf("filter #7 left the cursor on %+v", row)
	}
}

// TestPickerOpenResolvesTheRepoRootOnce pins #390 on the new path: one
// picker open asks git for the repository root once (findRepoRoot), and a
// failed lookup is the load's error, with nothing gathered.
func TestPickerOpenResolvesTheRepoRootOnce(t *testing.T) {
	old := findRepoRoot
	calls := 0
	findRepoRoot = func() (string, error) { calls++; return "", errMock("not a git repo") }
	t.Cleanup(func() { findRepoRoot = old })
	msg, ok := loadPickerCmd()().(rowsLoadedMsg)
	if !ok || msg.err == nil {
		t.Fatalf("load = %+v, want the lookup's error", msg)
	}
	if calls != 1 {
		t.Errorf("findRepoRoot called %d times, want 1", calls)
	}
}

// TestPickerStalePRTagSuppressed pins #451 on the new path: a merged PR whose
// head is neither in the branch nor ahead of it (the branch name was reused
// after the merge) tags nothing, while a PR whose head is the branch's tip
// keeps its tag. cleanup's lookup is by branch name, so without this the
// reused name would show the old PR as merged.
func TestPickerStalePRTagSuppressed(t *testing.T) {
	r := newRmRepo(t)
	r.git("switch", "-c", "reused")
	r.commit(r.dir, "old.txt", "old work")
	oldHead := r.git("rev-parse", "HEAD")
	r.git("switch", "main")
	r.git("branch", "-D", "reused")
	r.git("switch", "-c", "reused")
	r.commit(r.dir, "new.txt", "new work")
	r.git("switch", "main")
	r.loose("kept")
	keptTip := r.git("rev-parse", "kept")
	prs := func(_, branch string) ([]byte, error) {
		switch branch {
		case "reused":
			return []byte(`[{"number":9,"state":"MERGED","headRefOid":"` + oldHead + `","baseRefName":"main"}]`), nil
		case "kept":
			return []byte(`[{"number":11,"state":"MERGED","headRefOid":"` + keptTip + `","baseRefName":"main"}]`), nil
		}
		return []byte("[]"), nil
	}
	m, cmd := pickerLoad(t, r.dir, prs)
	m, _ = updateMsg(m, cmd())
	for _, it := range m.rows.items {
		if it.heading != "" || it.newWorktree {
			continue
		}
		var texts []string
		for _, tg := range it.tags {
			texts = append(texts, tg.Text)
		}
		got := strings.Join(texts, ",")
		switch it.row.Name {
		case "reused":
			if strings.Contains(got, "#9") {
				t.Errorf("reused branch tagged with its old PR: %q", got)
			}
		case "kept":
			if !strings.Contains(got, "#11 merged") {
				t.Errorf("kept branch tags %q, want #11 merged", got)
			}
		}
	}
}

// TestPickerFilterTakesActionKeys pins type-to-filter in the picker: `/`
// opens the filter, and while it is open every printable key — q, n, x, j —
// is typed into it rather than quitting, opening the new-worktree prompt,
// removing or moving; only matching rows stay; esc clears it.
func TestPickerFilterTakesActionKeys(t *testing.T) {
	m := pickerReady(t, pickerFixture())
	m, _ = updateMsg(m, keyRunes("/"))
	for _, k := range "qnxj" {
		var cmd tea.Cmd
		m, cmd = updateMsg(m, keyRunes(string(k)))
		if cmd != nil {
			if msg := cmd(); msg != nil {
				t.Fatalf("typing %q into the filter produced %T", k, msg)
			}
		}
	}
	if m.phase != phaseList || m.rows.filter != "qnxj" {
		t.Fatalf("phase %v, filter %q; want the list with filter qnxj", m.phase, m.rows.filter)
	}
	if !m.isTyping() {
		t.Error("isTyping false while the filter is open")
	}
	// backspace to "" and type a name: only that row is left.
	for range 4 {
		m, _ = updateMsg(m, tea.KeyMsg{Type: tea.KeyBackspace})
	}
	for _, k := range "loo" {
		m, _ = updateMsg(m, keyRunes(string(k)))
	}
	m, _ = updateMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.rows.filtering {
		t.Fatal("enter did not close the filter")
	}
	if row, ok := m.rows.selectedRow(); !ok || row.Name != "loose" {
		t.Errorf("cursor on %+v, want loose (the only match)", row)
	}
	if v := m.View(); strings.Contains(v, "teammate") || !strings.Contains(v, "loose") {
		t.Errorf("filtered view:\n%s", v)
	}
	m, cmd := updateMsg(m, tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil {
		if _, quit := cmd().(tea.QuitMsg); quit {
			t.Fatal("esc with a filter applied quit instead of clearing it")
		}
	}
	if m.rows.filter != "" || !strings.Contains(m.View(), "teammate") {
		t.Errorf("esc left filter %q", m.rows.filter)
	}
}

// TestLaunchModeRemovalWordingIsNeutral pins Task 108's review fix: removing
// the highlighted row from the picker (launch mode, nothing ticked) never
// says "ticked" or "wt cleanup ·" on the re-check, confirm and results
// screens, while `wt cleanup` keeps its own words.
func TestLaunchModeRemovalWordingIsNeutral(t *testing.T) {
	r := newRmRepo(t)
	r.merged("done")
	h := r.harness()
	m := newRowList(h.report, rowListOpts{Mode: modeLaunch, Theme: themes.Default, Now: rmLater()})
	m.rm = h.funcs()
	m = cleanupSend(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = moveTo(t, m, "done")
	next, cmd := m.Update(keyRunes("x"))
	m = next.(cleanupListModel)
	check := func(screen, v string) {
		t.Helper()
		if strings.Contains(v, "ticked") || strings.Contains(v, "wt cleanup") {
			t.Errorf("%s screen says ticked or wt cleanup:\n%s", screen, v)
		}
	}
	check("re-check", m.View())
	next, _ = m.Update(cmd())
	m = next.(cleanupListModel)
	if m.phase != cleanupPhaseConfirm {
		t.Fatalf("phase %v, want confirm; status %q", m.phase, m.status)
	}
	check("confirm", m.View())
	m = press(t, m, keyRunes("y"))
	if m.phase != cleanupPhaseResults {
		t.Fatalf("phase %v, want results", m.phase)
	}
	check("results", m.View())
}

// TestSelectedRowIsFalseOffRows pins selectedRow: the new-worktree item and a
// heading are not rows, so anything acting on "the highlighted row" (rescue,
// removal) must see ok=false there rather than a zero Row.
func TestSelectedRowIsFalseOffRows(t *testing.T) {
	m := newRowList(pickerFixture(), rowListOpts{Mode: modeLaunch, Theme: themes.Default, Now: time.Now()})
	m.cursor = 0
	if !m.items[0].newWorktree {
		t.Fatal("item 0 is not the new-worktree item")
	}
	if _, ok := m.selectedRow(); ok {
		t.Error("selectedRow ok on the new-worktree item")
	}
	m.cursor = 1
	if m.items[1].heading == "" {
		t.Fatal("item 1 is not a heading")
	}
	if _, ok := m.selectedRow(); ok {
		t.Error("selectedRow ok on a heading")
	}
}
