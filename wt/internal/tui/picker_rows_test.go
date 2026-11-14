package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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

// pickerLoad runs loadRowsCmd for dir with pickerInputs (no fetch, PR
// lookups through prList)
// and applies its message: the picker as it first paints.
func pickerLoad(t *testing.T, dir string, prList func(dir, branch string) ([]byte, error)) (model, tea.Cmd) {
	t.Helper()
	// The production inputs, with the PR hooks and own repository a test
	// needs (a local-path origin names no GitHub repository).
	in := pickerInputs(dir)
	in.OwnRepo, in.PRList = "o/r", prList
	in.PRBaseList = func(string, string) ([]byte, error) { return []byte("[]"), nil }
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
	// Enter on the held root and on the default branch's bare row each emit
	// a selectedEntryMsg for that row. (Launching the default branch's bare
	// row still errors in EnsureForBranch until Part 2 removes the refusal.)
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
// entry marked Root with the root's path, so the launch gate and the rest of
// the flow see it unchanged.
func TestPickerEnterOnRootRowLaunchesViaSelectedEntryMsg(t *testing.T) {
	m := pickerCursorTo(t, pickerReady(t, pickerFixture()), "main")
	_, sel := pickerEnter(t, m)
	if !sel.entry.Root || sel.entry.Path != "/repo" || sel.entry.Branch != "main" {
		t.Errorf("entry = %+v, want the root at /repo on main", sel.entry)
	}
}

// TestPickerEnterOnBareBranchRowPlansTheCreation pins what choosing a local
// branch with no worktree does: entryOfRow makes a branch entry with no path,
// and selectedEntryMsg's handler binds the owed creation — a read-only plan
// whose path becomes the launch directory — without creating anything yet.
// The commit point's launch gate makes the worktree, so a declined gate
// creates nothing and the agent never launches in wt's CWD (cmd.Dir="").
func TestPickerEnterOnBareBranchRowPlansTheCreation(t *testing.T) {
	r := newRmRepo(t)
	r.loose("loose")
	m, _ := pickerLoad(t, r.dir, func(string, string) ([]byte, error) { return []byte("[]"), nil })
	m = pickerCursorTo(t, m, "loose")
	m, sel := pickerEnter(t, m)
	if sel.entry.Type != worktree.TypeBranch || sel.entry.Path != "" || sel.entry.Branch != "loose" {
		t.Fatalf("entry = %+v, want a bare branch entry for loose", sel.entry)
	}
	m, _ = updateMsg(m, sel)
	if m.creation == nil || m.creation.name != "loose" {
		t.Fatalf("creation = %+v, want the owed plan for loose", m.creation)
	}
	if m.selectedPath != m.creation.plan.Path() {
		t.Errorf("selectedPath = %q, want the plan path %q", m.selectedPath, m.creation.plan.Path())
	}
	if _, err := os.Stat(m.selectedPath); err == nil {
		t.Errorf("%s exists at pick time; the creation belongs to the commit point", m.selectedPath)
	}
}

// TestPickerEnterOnRemoteOnlyRowUsesOriginForm pins the remote-only row's
// entry: the origin spelling ("origin/teammate") handed to worktree.Resolve,
// with RemoteBranch the bare name, so choosing it plans a local tracking
// branch and worktree the commit point makes.
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
	if !m.rows.filling() {
		t.Fatalf("first paint is not the facts stage:\n%s", m.View())
	}
	if cmd == nil {
		t.Fatal("no fill command after the load")
	}
	m = pickerCursorTo(t, m, "loose")
	// Stage 2: the fill lands (still no gh: the PR lookups are stage 3).
	fillMsg := cmd()
	if _, ok := fillMsg.(rowsFilledMsg); !ok {
		t.Fatalf("the load's command answered %T, want rowsFilledMsg", fillMsg)
	}
	m, cmd = updateMsg(m, fillMsg)
	if m.rows.filling() {
		t.Fatal("still filling after the fill landed")
	}
	if v := m.View(); !strings.Contains(v, "PR ?") {
		t.Fatalf("filled view has no PR ? (the lookups are still owed):\n%s", v)
	}
	if cmd == nil {
		t.Fatal("no PR fill command after the fill")
	}
	// Stage 3: the PR lookups.
	msg := cmd()
	if _, ok := msg.(rowPRsLoadedMsg); !ok {
		t.Fatalf("the fill answered %T, want rowPRsLoadedMsg", msg)
	}
	m, _ = updateMsg(m, msg)
	if asked == 0 {
		t.Error("the PR lookups asked gh nothing")
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
	m, cmd = updateMsg(m, cmd()) // the fill
	m, _ = updateMsg(m, cmd())   // the PR lookups
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

// TestPickerListsBranchesOfAnUntrackedRemote pins "show everything local":
// a remote no local branch tracks (upstream here) still has its branches in
// the picker, through GatherInputs.AllRemotes, while the default Gather (what
// `wt cleanup --json` and --plain run) keeps omitting them, so that output
// does not change.
func TestPickerListsBranchesOfAnUntrackedRemote(t *testing.T) {
	r := newRmRepo(t)
	up := filepath.Join(t.TempDir(), "upstream.git")
	rmGit(t, r.dir, "init", "--bare", "-b", "main", up)
	r.git("remote", "add", "upstream", up)
	r.git("push", "upstream", "main:foo")
	r.git("fetch", "upstream")
	m, _ := pickerLoad(t, r.dir, func(string, string) ([]byte, error) { return []byte("[]"), nil })
	m = pickerCursorTo(t, m, "upstream/foo")
	if _, sel := pickerEnter(t, m); sel.entry.Branch != "upstream/foo" || sel.entry.RemoteBranch != "foo" {
		t.Errorf("entry = %+v, want upstream/foo", sel.entry)
	}

	snap, err := cleanup.Gather(cleanup.Git{Dir: r.dir}, cleanup.GatherInputs{CWD: r.dir, OwnRepo: "o/r", SkipPRs: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range cleanup.Classify(snap, cleanup.Options{Now: time.Now()}).AllRows() {
		if row.Remote == "upstream" && row.Kind == cleanup.KindRemote {
			t.Errorf("the default Gather lists %s/%s; its output must not change", row.Remote, row.Name)
		}
	}
}

// TestPickerFailedFetchReadsAsFailed pins that the `F` refresh's fetch, when
// it fails, is reported as failed in the list's header, not as "fetch:
// skipped".
func TestPickerFailedFetchReadsAsFailed(t *testing.T) {
	r := newRmRepo(t)
	r.git("remote", "add", "dead", filepath.Join(t.TempDir(), "missing.git"))
	msg := loadRowsCmd(r.dir, nil, cleanup.GatherInputs{CWD: r.dir, OwnRepo: "o/r"}, true)().(rowsLoadedMsg)
	if msg.err != nil {
		t.Fatal(msg.err)
	}
	if msg.report.Fetch.Err == "" || msg.report.Fetch.OK {
		t.Errorf("fetch = %+v, want a failure", msg.report.Fetch)
	}
	header := strings.Join(cleanup.HeaderLines(msg.report, time.Now()), "\n")
	if !strings.Contains(header, "fetch: FAILED") {
		t.Errorf("header does not say the fetch failed:\n%s", header)
	}
}

// TestPickerOpenFetchReadsAsNotRun pins the open's fetch wording, live
// behavior with no change behind it: the header reads "fetch: not run (this
// list does not fetch when it opens)" from the NotAttempted marker
// pickerInputs gives every open. Deleting that marker reverts the header
// line to the zero value's "fetch: skipped; …" with every other test green;
// this pin fails then. (The zero value's --no-fetch naming is the origin
// rows', not this line's: classify.go's originFetchProblem answers "origin
// was not fetched this run (--no-fetch)" for it there.)
func TestPickerOpenFetchReadsAsNotRun(t *testing.T) {
	r := newRmRepo(t)
	empty := func(string, string) ([]byte, error) { return []byte("[]"), nil }
	m, _ := pickerLoad(t, r.dir, empty)
	if header := m.rows.headerBlock(); !strings.Contains(header, "fetch: not run") {
		t.Errorf("the open's header does not read \"fetch: not run\":\n%s", header)
	}
}

// TestPickerFooterDoesNotOfferRemoval pins that the picker's footer does not
// advertise `x remove` while the picker has no removal wired in (every `x`
// would answer "not available"), and still names what works.
func TestPickerFooterDoesNotOfferRemoval(t *testing.T) {
	m := pickerReady(t, pickerFixture())
	if foot := m.rows.footer(); strings.Contains(foot, "x remove") || !strings.Contains(foot, "enter launch") {
		t.Errorf("footer = %q", foot)
	}
}

// TestPRFillKeepsTheStatusLine pins that a PR fill landing right after a
// status message (a refused x, say) leaves the message on screen: withReport
// keeps what the user was shown, not only the cursor and filter.
func TestPRFillKeepsTheStatusLine(t *testing.T) {
	m := pickerReady(t, pickerFixture())
	m.rows.status = "cannot be removed: primary checkout"
	m, _ = updateMsg(m, rowPRsLoadedMsg{Report: pickerFixture(), gen: m.rowsGen})
	if m.rows.status != "cannot be removed: primary checkout" {
		t.Errorf("status after the fill = %q", m.rows.status)
	}
}

// TestPickerPaintsFactsThenFillsThenPRs pins the three-stage picker: the
// rows load returns immediately with the facts report (every row Pending:
// names and session digits on screen, detail cells "-", no verdict), its
// command fills the details off the update goroutine (rowsFilledMsg), and
// that landing dispatches the PR lookup (rowPRsLoadedMsg). Why: the first
// paint must never wait on the gates/ladder/gh — three messages, in that
// order, each swapping through the generation guard.
func TestPickerPaintsFactsThenFillsThenPRs(t *testing.T) {
	r := newRmRepo(t)
	r.worktree("topic")
	m, cmd := pickerLoad(t, r.dir, func(string, string) ([]byte, error) { return []byte("[]"), nil })
	if cmd == nil {
		t.Fatal("no fill command after the facts load")
	}
	// First paint: rows exist and are Pending — "-" detail cells, names.
	view := m.View()
	if !strings.Contains(view, "topic") {
		t.Fatalf("first paint lacks the row name:\n%s", view)
	}
	if !strings.Contains(view, "-") {
		t.Fatalf("first paint lacks the - cells:\n%s", view)
	}
	pending := 0
	for _, it := range m.rows.items {
		if it.heading == "" && !it.newWorktree {
			if !it.row.Pending {
				t.Fatalf("first paint row %q is not Pending", it.row.Name)
			}
			pending++
		}
	}
	if pending == 0 {
		t.Fatal("first paint has no rows")
	}

	// Stage 2: the fill lands.
	filled := cmd()
	if _, ok := filled.(rowsFilledMsg); !ok {
		t.Fatalf("the load's command answered %T, want rowsFilledMsg", filled)
	}
	m, cmd = updateMsg(m, filled)
	for _, it := range m.rows.items {
		if it.heading == "" && !it.newWorktree && it.row.Pending {
			t.Fatalf("row %q still Pending after the fill", it.row.Name)
		}
	}
	if cmd == nil {
		t.Fatal("no PR command after the fill")
	}

	// Stage 3: the PR lookups land.
	prs := cmd()
	if _, ok := prs.(rowPRsLoadedMsg); !ok {
		t.Fatalf("the fill's command answered %T, want rowPRsLoadedMsg", prs)
	}
	m, _ = updateMsg(m, prs)
	if m.rows.filling() {
		t.Error("still filling after every stage landed")
	}
}

// TestStaleFillIsDropped pins the generation guard on the fill: a
// rowsFilledMsg for an older load leaves the list untouched. Why: a slow
// fill for a repository state the user has moved past must never overwrite
// the newer screen.
func TestStaleFillIsDropped(t *testing.T) {
	m := pickerReady(t, pickerFixture())
	m.rowsGen = 5
	before := m.rows.withReport // struct copy: compare fields we care about
	_ = before
	rep := pickerFixture()
	rep.Rows = append(rep.Rows[:0:0], rep.Rows...)
	rep.Rows[0].Name = "stale-swap"
	m, cmd := updateMsg(m, rowsFilledMsg{Report: rep, gen: 4})
	if cmd != nil {
		t.Errorf("stale fill returned a command, want none: %v", cmd)
	}
	for _, it := range m.rows.items {
		if it.row.Name == "stale-swap" {
			t.Fatal("a stale fill swapped the list")
		}
	}
}

// TestFillKeepsTheHighlightedRowByKey pins that the facts→fill swap keeps
// the cursor on the same row (withReport's keyed selection): the details
// landing under the cursor must not move it to another row.
func TestFillKeepsTheHighlightedRowByKey(t *testing.T) {
	r := newRmRepo(t)
	r.worktree("topic")
	r.loose("loose")
	m, cmd := pickerLoad(t, r.dir, func(string, string) ([]byte, error) { return []byte("[]"), nil })
	if cmd == nil {
		t.Fatal("no fill command")
	}
	m = pickerCursorTo(t, m, "loose")
	m, _ = updateMsg(m, cmd())
	if row, ok := m.rows.selectedRow(); !ok || row.Name != "loose" {
		t.Fatalf("after the fill the cursor sits on %+v (ok=%v), want loose", row.Name, ok)
	}
}

// TestRemovalKeysUnavailableWhileFilling pins that a facts-first list offers
// no removal: rowKeys lacks x/d/o/r/R (the nil-RemoveFuncs rule), the row
// keys say so when pressed, and launching still works. Why: nothing may be
// removed from a half-read repository, but choosing where to work must not
// wait for the fill.
func TestRemovalKeysUnavailableWhileFilling(t *testing.T) {
	r := newRmRepo(t)
	r.worktree("topic")
	m, _ := pickerLoad(t, r.dir, func(string, string) ([]byte, error) { return []byte("[]"), nil })
	keys := m.rows.footer()
	for _, banned := range []string{"x remove", "d ", "o delete", "r rescue", "R restore"} {
		if strings.Contains(keys, banned) {
			t.Errorf("footer offers %q while filling: %q", banned, keys)
		}
	}
	if !strings.Contains(keys, "enter launch") {
		t.Errorf("footer lacks enter launch while filling: %q", keys)
	}
	// Pressing x answers with the existing unavailable status, not a flow.
	got, _ := updateMsg(m, keyRunes("x"))
	if got.rows.phase != cleanupPhaseList {
		t.Errorf("x while filling entered phase %v, want the list with an unavailable status", got.rows.phase)
	}
	// Launching works: enter produces the launch message.
	if _, sel := pickerEnter(t, m); sel.entry.Branch == "" && sel.entry.Path == "" {
		t.Error("enter while filling did not choose the row")
	}
}

// TestFReloadReplacesAnInFlightFill pins the refresh rule and its recovery:
// a fill landing while an F's re-read is in flight is judged by generation
// alone (it still matches, so it lands — a fill is never silently dropped),
// the F's own facts load then replaces it and spends the generation so any
// older fill is stale, and if that re-read FAILS the list is not stranded at
// the facts stage: the landed fill's rows stay, filling() is false and the
// removal keys come back, with the failure on the status. Why: dropping the
// in-flight fill and then failing the reload left rows Pending forever —
// verdict cells "-", no removal — recoverable only by an F that succeeded.
func TestFReloadReplacesAnInFlightFill(t *testing.T) {
	m := pickerReady(t, pickerFixture())
	m.rowsGen = 3
	// An F is in flight (rowsRefreshing set, reloadGen at dispatch).
	m.rowsRefreshing, m.reloadGen = true, m.rowsGen

	// The fill that was in flight lands: generation matches, so it applies.
	filled := pickerFixture()
	filled.Rows = append(filled.Rows[:0:0], filled.Rows...)
	filled.Rows[0].Name = "filled-during-f"
	m, cmd := updateMsg(m, rowsFilledMsg{Report: filled, gen: 3})
	if cmd == nil {
		t.Fatal("a fill whose generation still matches was dropped: the gen guard is the only supersession rule")
	}
	seen := false
	for _, it := range m.rows.items {
		if it.row.Name == "filled-during-f" {
			seen = true
		}
	}
	if !seen {
		t.Fatal("the in-flight fill's rows are not on the list")
	}

	// The F's re-read then fails: the error is shown, the filled rows stay,
	// and the list is NOT stuck at the facts stage.
	m, cmd = updateMsg(m, rowsLoadedMsg{err: errMock("fetch dead"), gen: 3})
	if cmd != nil {
		t.Errorf("a failed reload returned a command: %v", cmd)
	}
	if !strings.Contains(m.listError, "fetch dead") {
		t.Errorf("listError = %q, want the reload's failure", m.listError)
	}
	if m.rows.filling() {
		t.Error("the list is stuck filling after a failed F: the fill landed but the state read as pending")
	}
	if m.rowsRefreshing {
		t.Error("rowsRefreshing stayed set after the reload failed: no later F could start")
	}

	// An older fill (generation spent by a later load) is still dropped.
	stale := filled
	stale.Rows = append(filled.Rows[:0:0], filled.Rows...)
	stale.Rows[0].Name = "stale-fill"
	m.rowsGen = 5
	m, cmd = updateMsg(m, rowsFilledMsg{Report: stale, gen: 4})
	if cmd != nil {
		t.Errorf("stale fill returned a command: %v", cmd)
	}
	for _, it := range m.rows.items {
		if it.row.Name == "stale-fill" {
			t.Fatal("a fill for a superseded generation swapped the list")
		}
	}
}

// TestTicksDefaultAfterFillAndKeepLaterChoices pins withReport's pending
// tick rule: a row that was Pending in the old list is unseen — it takes
// the new report's default tick (proven rows ticked in manage mode), and a
// manual tick made on it during the fill does not survive. Why: the facts
// report cannot know which rows will prove; carrying a choice made against
// the unknown state into the filled list would tick (or untick) rows the
// user never judged.
func TestTicksDefaultAfterFillAndKeepLaterChoices(t *testing.T) {
	facts := pickerFixture()
	for i := range facts.Rows {
		facts.Rows[i].Pending = true
		facts.Rows[i].Status = cleanup.StatusLessSure
	}
	filled := pickerFixture()
	filled.Rows[3].Status = cleanup.StatusProven // loose: Tickable when filled
	filled.Rows[3].Evidence = cleanup.EvAncestor
	filled.Rows[3].Ref = "refs/heads/loose"
	filled.Rows[3].Tip = strings.Repeat("a", 40)

	m := model{width: 100, height: 40, theme: themes.Default, cfg: &config.Config{}}
	m, _ = updateMsg(m, rowsLoadedMsg{report: facts, repoRoot: facts.Repo})
	m.rows.opts.Mode = modeManage // the tick list (Task 10 wires manage fills)
	// A manual tick on a pending row during the fill.
	for i, it := range m.rows.items {
		if it.row.Name == "deep" {
			m.rows.ticked[i] = true
		}
	}
	m.rows = m.rows.withReport(filled)

	ticked := map[string]bool{}
	for i, it := range m.rows.items {
		if it.heading == "" && !it.newWorktree {
			ticked[it.row.Name] = m.rows.ticked[i]
		}
	}
	if ticked["loose"] != true {
		t.Errorf("filled proven row loose ticked=%v, want the new default (true)", ticked["loose"])
	}
	if ticked["deep"] {
		t.Error("a tick made on a pending row survived the fill; pending rows are unseen")
	}
}

// TestRowOrderStableAcrossStages pins that the facts→fill swap keeps the
// rendered row order (kind groups, root first, display names): the fill
// lands under the cursor, and rows jumping beneath it is the anti-goal.
func TestRowOrderStableAcrossStages(t *testing.T) {
	facts := pickerFixture()
	for i := range facts.Rows {
		facts.Rows[i].Pending = true
	}
	filled := pickerFixture()
	filled.Rows[0].Status = cleanup.StatusProven

	m := model{width: 100, height: 40, theme: themes.Default, cfg: &config.Config{}}
	m, _ = updateMsg(m, rowsLoadedMsg{report: facts, repoRoot: facts.Repo})
	names := func() []string {
		var out []string
		for _, g := range cleanup.KindGroups(m.rows.report) {
			for _, row := range g.Rows {
				out = append(out, cleanup.DisplayName(row)+"/"+g.Heading)
			}
		}
		return out
	}
	before := names()
	m.rows = m.rows.withReport(filled)
	if after := names(); !slices.Equal(before, after) {
		t.Errorf("row order changed across stages:\n before %v\n after  %v", before, after)
	}
}
