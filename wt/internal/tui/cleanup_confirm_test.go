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
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/removal"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// The removal flow's tests drive the list model through Update with the
// real engine's previews (only Engine.Preview can make a preview that
// confirms, Ruling N), over a real repository per test: a bare origin and a
// clone, linked worktrees under .worktrees/. RemoveFuncs wrap the engine to
// count and record what reaches Preview and Run; a test that must not remove
// anything stubs Run.

// rmRepo is one test's repository.
type rmRepo struct {
	t   *testing.T
	dir string
}

func rmGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	b, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, b)
	}
	return strings.TrimSpace(string(b))
}

// newRmRepo builds the repository with the developer's git config kept out
// (a hook or merge driver would change what is proven) and gh stubbed with an
// empty listing.
func newRmRepo(t *testing.T) *rmRepo {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Cleanup(cleanup.StubPRListForTest(func(string, string) ([]byte, error) { return []byte("[]"), nil }))
	t.Cleanup(cleanup.StubPRBaseListForTest(func(string, string) ([]byte, error) { return []byte("[]"), nil }))
	root := t.TempDir()
	origin, dir := filepath.Join(root, "origin.git"), filepath.Join(root, "repo")
	if b, err := exec.Command("git", "init", "--bare", "-b", "main", origin).CombinedOutput(); err != nil {
		t.Fatalf("init: %v\n%s", err, b)
	}
	if b, err := exec.Command("git", "clone", origin, dir).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, b)
	}
	r := &rmRepo{t: t, dir: dir}
	r.git("config", "user.email", "t@example.com")
	r.git("config", "user.name", "T")
	r.git("config", "commit.gpgsign", "false")
	r.git("checkout", "-B", "main")
	ex := r.git("rev-parse", "--path-format=absolute", "--git-path", "info/exclude")
	if err := os.MkdirAll(filepath.Dir(ex), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ex, []byte(".worktrees/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.commit(dir, "README", "init")
	r.git("push", "-u", "origin", "main")
	r.git("remote", "set-head", "origin", "main")
	return r
}

func (r *rmRepo) git(args ...string) string { r.t.Helper(); return rmGit(r.t, r.dir, args...) }

func (r *rmRepo) commit(dir, file, content string) {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
	rmGit(r.t, dir, "add", file)
	rmGit(r.t, dir, "commit", "-m", file)
}

// worktree adds a linked worktree on a new branch and returns its path.
func (r *rmRepo) worktree(branch string) string {
	r.t.Helper()
	p := filepath.Join(r.dir, ".worktrees", branch)
	r.git("worktree", "add", "-b", branch, p)
	return p
}

// merged is a worktree whose branch was pushed and squash-merged: proven.
func (r *rmRepo) merged(branch string) string {
	r.t.Helper()
	p := r.worktree(branch)
	r.commit(p, branch+".txt", "work")
	r.git("push", "-u", "origin", branch)
	r.git("merge", "--squash", branch)
	r.git("commit", "-m", "squash "+branch)
	r.git("push", "origin", "main")
	return p
}

// dirty is a worktree at main with an untracked file: needs --force.
func (r *rmRepo) dirty(branch string) string {
	r.t.Helper()
	p := r.worktree(branch)
	if err := os.WriteFile(filepath.Join(p, "wip.txt"), []byte("wip"), 0o644); err != nil {
		r.t.Fatal(err)
	}
	return p
}

// unpushed is a worktree with a commit only here: needs confirmation and
// offers deleting its branch.
func (r *rmRepo) unpushed(branch string) string {
	r.t.Helper()
	p := r.worktree(branch)
	r.commit(p, branch+".txt", "only here")
	return p
}

// loose is a pushed, unmerged branch with no worktree: a branch item
// needing -D.
func (r *rmRepo) loose(branch string) {
	r.t.Helper()
	r.git("switch", "-c", branch)
	r.commit(r.dir, branch+".txt", "work")
	r.git("push", "-u", "origin", branch)
	r.git("switch", "main")
}

func (r *rmRepo) refExists(ref string) bool {
	r.t.Helper()
	return r.git("for-each-ref", "--format=%(refname)", ref) == ref
}

// rmLater is an hour ahead: every branch is older than the min-age by then.
func rmLater() time.Time { return time.Now().Add(time.Hour) }

// fakeProcs reports one process in every directory whose base is in dirs.
type fakeProcs map[string]removal.Proc

func (f fakeProcs) InDir(dir string) ([]removal.Proc, error) {
	if p, ok := f[filepath.Base(dir)]; ok {
		return []removal.Proc{p}, nil
	}
	return nil, nil
}

// rmHarness is the repository's report, the engine over it and RemoveFuncs
// that record what reaches the engine.
type rmHarness struct {
	repo       *rmRepo
	report     cleanup.Report
	eng        removal.Engine
	previewed  [][]cleanup.Row
	runCalls   int
	ran        []removal.Item
	refreshed  int
	stubRun    func(items []removal.Item, progress func(removal.Outcome)) []removal.Outcome
	procs      fakeProcs
	gatherOpts cleanup.GatherInputs
}

func (r *rmRepo) harness() *rmHarness {
	r.t.Helper()
	h := &rmHarness{repo: r, gatherOpts: cleanup.GatherInputs{CWD: r.dir, OwnRepo: "o/r", Parallel: 2}}
	h.regather()
	return h
}

func (h *rmHarness) regather() {
	h.repo.t.Helper()
	s, err := cleanup.Gather(cleanup.Git{Dir: h.repo.dir}, h.gatherOpts)
	if err != nil {
		h.repo.t.Fatalf("Gather: %v", err)
	}
	h.report = cleanup.Classify(s, cleanup.Options{Now: rmLater()})
	h.eng = removal.Engine{G: cleanup.Git{Dir: h.repo.dir}, Prior: s, In: h.gatherOpts,
		Classify: cleanup.Options{Now: rmLater()}, Opts: removal.Options{Now: rmLater}}
}

func (h *rmHarness) funcs() RemoveFuncs {
	return RemoveFuncs{
		Preview: func(rows []cleanup.Row) []removal.ItemPreview {
			h.previewed = append(h.previewed, rows)
			items := make([]removal.Item, len(rows))
			for i, r := range rows {
				items[i] = removal.ItemFromRow(r)
			}
			e := h.eng
			if h.procs != nil {
				e.Opts.Procs = h.procs
			}
			return e.Preview(items)
		},
		Run: func(items []removal.Item, progress func(removal.Outcome)) []removal.Outcome {
			h.runCalls++
			h.ran = append(h.ran, items...)
			if h.stubRun != nil {
				return h.stubRun(items, progress)
			}
			return h.eng.Run(items, progress)
		},
		Refresh: func() (cleanup.Report, error) {
			h.refreshed++
			h.regather()
			return h.report, nil
		},
	}
}

// list is the tick list over the harness's report at w x h.
func (h *rmHarness) list(w, ht int) cleanupListModel {
	m := newCleanupList(h.report, themes.Default, rmLater())
	m.rm = h.funcs()
	return cleanupSend(h.repo.t, m, tea.WindowSizeMsg{Width: w, Height: ht})
}

// press sends one key and runs every command it leads to (preview, the run's
// stream, the refresh) until none is left or the program quits.
func press(t *testing.T, m cleanupListModel, k tea.KeyMsg) cleanupListModel {
	t.Helper()
	next, cmd := m.Update(k)
	m = next.(cleanupListModel)
	for cmd != nil {
		msg := cmd()
		if _, ok := msg.(tea.QuitMsg); ok {
			return m
		}
		next, cmd = m.Update(msg)
		m = next.(cleanupListModel)
	}
	return m
}

// typeWord sends s one rune at a time, as a keyboard does.
func typeWord(t *testing.T, m cleanupListModel, s string) cleanupListModel {
	t.Helper()
	for _, r := range s {
		m = press(t, m, keyRunes(string(r)))
	}
	return m
}

var (
	enterKey = tea.KeyMsg{Type: tea.KeyEnter}
	escKey   = tea.KeyMsg{Type: tea.KeyEsc}
	ctrlCKey = tea.KeyMsg{Type: tea.KeyCtrlC}
	tabKey   = tea.KeyMsg{Type: tea.KeyTab}
	downKey  = tea.KeyMsg{Type: tea.KeyDown}
)

// setTicks leaves exactly the named rows ticked (display names).
func setTicks(t *testing.T, m cleanupListModel, names ...string) cleanupListModel {
	t.Helper()
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	for i, it := range m.items {
		if it.heading != "" {
			continue
		}
		if m.ticked[i] != want[cleanup.DisplayName(it.row)] {
			m.cursor = i
			m = cleanupSend(t, m, spaceKey)
		}
	}
	got := strings.Join(m.tickedNames(), ",")
	if len(m.tickedNames()) != len(names) {
		t.Fatalf("ticked %q, want %v", got, names)
	}
	return m
}

// confirmEntryAt moves the confirm cursor to the entry named name.
func confirmEntryAt(t *testing.T, m cleanupListModel, name string) cleanupListModel {
	t.Helper()
	for i, e := range m.conf.entries {
		if e.pv.Item.Name == name {
			m.conf.cursor = i
			return m
		}
	}
	t.Fatalf("no confirm entry %q", name)
	return m
}

// TestConfirmListsLossesForEveryTickedItem pins the confirm screen: every
// ticked item is on it under its approval group with what it would lose, the
// process running in it, its note, the offered branch toggle and the
// approvals the typed word gives it — the user approves what is shown.
func TestConfirmListsLossesForEveryTickedItem(t *testing.T) {
	r := newRmRepo(t)
	r.merged("done")
	r.dirty("dirty")
	r.unpushed("kept")
	r.loose("loose")
	h := r.harness()
	h.procs = fakeProcs{"dirty": {PID: 4242, Command: "vim wip.txt"}}
	m := setTicks(t, h.list(120, 80), "done", "dirty", "kept", "loose")
	m = press(t, m, enterKey)
	if m.phase != cleanupPhaseConfirm {
		t.Fatalf("phase %v, want confirm; status %q", m.phase, m.status)
	}
	v := m.View()
	for _, want := range []string{
		"PROVEN MERGED", "NOT PROVEN, OR LOSES WHAT IS LISTED", "UNCOMMITTED CHANGES OR DETACHED COMMITS", "UNMERGED BRANCHES",
		"worktree done", "worktree dirty", "worktree kept", "branch loose",
		"will lose: uncommitted changes will be lost",
		"running in it: pid 4242 vim wip.txt",
		"will lose: 1 commit exists only on this machine",
		"note: branch kept will be kept: not proven merged",
		"[ ] also delete unmerged branch kept (tab)",
		"deletes unmerged branch loose",
		"approvals: none needed (proven)",
		"approvals: --accept-loss --force",
		"approvals: --accept-loss -D",
		"type delete and press enter to remove 4 items",
		"(1 proven, 1 not proven or losing something, 1 with uncommitted changes or detached commits, 1 unmerged branch)",
		"worktree dirty  .worktrees/dirty",
	} {
		if !strings.Contains(v, want) && !strings.Contains(squash(v), squash(want)) {
			t.Errorf("confirm screen lacks %q:\n%s", want, v)
		}
	}
	// Group order: proven before not-proven before force before -D.
	last := -1
	for _, g := range []string{"worktree done", "worktree kept", "worktree dirty", "branch loose"} {
		i := strings.Index(v, g)
		if i <= last {
			t.Errorf("%q out of group order:\n%s", g, v)
		}
		last = i
	}
	if h.runCalls != 0 {
		t.Errorf("Run called %d times before any confirmation", h.runCalls)
	}
}

// TestConfirmProvenOnlyAcceptsY pins the light path: a selection of proven
// items only is confirmed by y (or Y, TestAllProvenConfirmAcceptsUppercaseY)
// and by nothing else; another key cancels and Run is never called; y
// removes the worktree and its branch.
func TestConfirmProvenOnlyAcceptsY(t *testing.T) {
	r := newRmRepo(t)
	p := r.merged("done")
	h := r.harness()
	m := setTicks(t, h.list(120, 40), "done")
	m = press(t, m, enterKey)
	if v := m.View(); !strings.Contains(v, "y removes 1 item (0 force, 0 -D) · any other key cancels") || strings.Contains(v, "type delete") {
		t.Fatalf("proven-only prompt:\n%s", v)
	}
	for _, k := range []tea.KeyMsg{keyRunes("x"), keyRunes("n"), enterKey, keyRunes("d")} {
		m2 := press(t, m, k)
		if m2.phase != cleanupPhaseList || h.runCalls != 0 || !strings.Contains(m2.status, "nothing was removed") {
			t.Fatalf("key %q: phase %v, runs %d, status %q; want cancelled", k.String(), m2.phase, h.runCalls, m2.status)
		}
		m = press(t, m2, enterKey)
	}
	m = press(t, m, keyRunes("y"))
	if h.runCalls != 1 || m.phase != cleanupPhaseResults {
		t.Fatalf("y: runs %d, phase %v", h.runCalls, m.phase)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) || r.refExists("refs/heads/done") {
		t.Errorf("done not removed: stat err %v, ref %v", err, r.refExists("refs/heads/done"))
	}
}

// TestConfirmUnprovenRequiresTypedDelete pins the typed confirmation (Ruling
// B): with an item that needs approval, y alone confirms nothing, a wrong
// word cancels with nothing removed, and only `delete` plus enter runs — with
// exactly the approvals that item needs.
func TestConfirmUnprovenRequiresTypedDelete(t *testing.T) {
	r := newRmRepo(t)
	p := r.dirty("dirty")
	h := r.harness()
	m := setTicks(t, h.list(120, 40), "dirty")
	for _, word := range []string{"y", "delet", "deletee", "DELETE", "remove"} {
		m = press(t, m, enterKey)
		m = typeWord(t, m, word)
		if m.phase != cleanupPhaseConfirm {
			t.Fatalf("typing %q left the confirm screen (phase %v)", word, m.phase)
		}
		m = press(t, m, enterKey)
		if m.phase != cleanupPhaseList || h.runCalls != 0 || !strings.Contains(m.status, "nothing was removed") {
			t.Fatalf("%q+enter: phase %v, runs %d, status %q; want cancelled", word, m.phase, h.runCalls, m.status)
		}
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%q removed the worktree", word)
		}
	}
	// A typo fixed with backspace counts.
	m = press(t, m, enterKey)
	m = typeWord(t, m, "deletx")
	m = press(t, m, tea.KeyMsg{Type: tea.KeyBackspace})
	m = typeWord(t, m, "e")
	if !strings.Contains(m.View(), "> delete_") {
		t.Errorf("typed word not echoed:\n%s", m.View())
	}
	m = press(t, m, enterKey)
	if h.runCalls != 1 || len(h.ran) != 1 {
		t.Fatalf("delete+enter: runs %d items %d", h.runCalls, len(h.ran))
	}
	if got := h.ran[0].Approved; got != (removal.Needs{Confirm: true, Force: true}) {
		t.Errorf("dirty approved %+v, want Confirm+Force only", got)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("dirty worktree still there after the typed confirmation")
	}
}

// TestConfirmCancelRemovesNothing pins every cancel key on the confirm
// screen: esc, n and ctrl+c go back to the list with nothing removed and Run
// never called, whether the prompt is y or the typed word.
func TestConfirmCancelRemovesNothing(t *testing.T) {
	r := newRmRepo(t)
	done := r.merged("done")
	dirty := r.dirty("dirty")
	h := r.harness()
	for _, sel := range [][]string{{"done"}, {"done", "dirty"}} {
		for _, k := range []tea.KeyMsg{escKey, keyRunes("n"), ctrlCKey} {
			m := setTicks(t, h.list(120, 40), sel...)
			m = press(t, m, enterKey)
			if m.conf.needsTyped() {
				// Half the word typed: the cancel keys still cancel.
				m = typeWord(t, m, "de")
			}
			m = press(t, m, k)
			if m.phase != cleanupPhaseList || m.done || !strings.Contains(m.status, "nothing was removed") {
				t.Errorf("%v %q: phase %v done %v status %q", sel, k.String(), m.phase, m.done, m.status)
			}
		}
	}
	if h.runCalls != 0 {
		t.Errorf("Run called %d times", h.runCalls)
	}
	for _, p := range []string{done, dirty} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s gone after cancel", p)
		}
	}
}

// TestProgressShowsEachOutcome pins the progress screen: Run's progress
// arrives as one message per item, and between them the screen shows the
// finished items' outcomes and the one in flight.
func TestProgressShowsEachOutcome(t *testing.T) {
	r := newRmRepo(t)
	r.merged("one")
	r.merged("two")
	h := r.harness()
	gate := make(chan struct{}, 2)
	h.stubRun = func(items []removal.Item, progress func(removal.Outcome)) []removal.Outcome {
		var out []removal.Outcome
		for _, it := range items {
			<-gate
			o := removal.NewOutcome(it, removal.WtRemoved, removal.BrDeleted, "")
			out = append(out, o)
			progress(o)
		}
		return out
	}
	m := setTicks(t, h.list(120, 40), "one", "two")
	m = press(t, m, enterKey)
	next, cmd := m.Update(keyRunes("y"))
	m = next.(cleanupListModel)
	if m.phase != cleanupPhaseRunning || !strings.Contains(m.View(), "now   worktree one") || !strings.Contains(m.View(), "1 waiting") {
		t.Fatalf("before the first outcome:\n%s", m.View())
	}
	// Keys wait while a removal runs.
	if m2 := cleanupSend(t, m, ctrlCKey); m2.done || m2.phase != cleanupPhaseRunning {
		t.Fatalf("ctrl+c during the run: done %v phase %v", m2.done, m2.phase)
	}
	gate <- struct{}{}
	msg := cmd()
	if _, ok := msg.(cleanupProgressMsg); !ok {
		t.Fatalf("first message %T, want one progress message", msg)
	}
	next, cmd = m.Update(msg)
	m = next.(cleanupListModel)
	v := m.View()
	if !strings.Contains(v, "done  worktree one: worktree removed, branch deleted") || !strings.Contains(v, "now   worktree two") {
		t.Fatalf("after the first outcome:\n%s", v)
	}
	gate <- struct{}{}
	for cmd != nil {
		next, cmd = m.Update(cmd())
		m = next.(cleanupListModel)
	}
	if m.phase != cleanupPhaseResults || len(m.run.results) != 2 {
		t.Fatalf("phase %v results %d", m.phase, len(m.run.results))
	}
}

// TestResultsShowReasonsAndRetainedBranches pins the results screen: each
// item's outcome with its reason, a kept unmerged branch said as kept, a
// deleted branch's commit; a key rebuilds the list from the repository, and
// the outcomes print again when the program ends.
func TestResultsShowReasonsAndRetainedBranches(t *testing.T) {
	r := newRmRepo(t)
	r.merged("done")
	r.unpushed("kept")
	tip := r.git("rev-parse", "refs/heads/done")
	h := r.harness()
	m := setTicks(t, h.list(120, 50), "done", "kept")
	m = press(t, m, enterKey)
	m = typeWord(t, m, "delete")
	m = press(t, m, enterKey)
	if m.phase != cleanupPhaseResults {
		t.Fatalf("phase %v", m.phase)
	}
	v := m.View()
	for _, want := range []string{
		"results: removed 2 of 2",
		"worktree done: worktree removed, branch deleted",
		"branch tip was " + tip,
		"worktree kept: worktree removed, branch retained_unmerged",
		"loss: 1 commit exists only on this machine",
	} {
		if !strings.Contains(v, want) {
			t.Errorf("results lack %q:\n%s", want, v)
		}
	}
	if !r.refExists("refs/heads/kept") {
		t.Error("kept's unmerged branch was deleted without its toggle")
	}
	m = press(t, m, keyRunes("x"))
	if m.phase != cleanupPhaseList || h.refreshed != 1 {
		t.Fatalf("after a key: phase %v refreshed %d", m.phase, h.refreshed)
	}
	for _, it := range m.items {
		if it.heading == "" && it.row.Kind == cleanup.KindWorktree && (it.row.Name == "done") {
			t.Errorf("rebuilt list still has worktree done")
		}
	}
	if !strings.Contains(m.View(), "removed 2 of 2") {
		t.Errorf("rebuilt list does not say what was removed:\n%s", m.View())
	}
	if msg := m.exitMessage(); !strings.Contains(msg, "branch tip was "+tip) || !strings.Contains(msg, "worktree kept") {
		t.Errorf("exit message:\n%s", msg)
	}
}

// TestRemoteRowsNeverEnterTheSelection pins that origin rows can never be
// ticked, by space or by `a`, and so never reach the engine: wt does not
// delete origin branches.
func TestRemoteRowsNeverEnterTheSelection(t *testing.T) {
	r := newRmRepo(t)
	r.merged("done")
	r.loose("loose")
	// origin/done folds into its local row; a remote-only ref stays a row.
	r.git("update-ref", "refs/remotes/origin/remote-only", "main")
	h := r.harness()
	m := h.list(120, 50)
	remotes := 0
	for i, it := range m.items {
		if it.heading == "" && it.row.Kind == cleanup.KindRemote {
			remotes++
			m.cursor = i
			m = cleanupSend(t, m, spaceKey)
		}
	}
	if remotes == 0 {
		t.Fatal("fixture has no remote rows")
	}
	m = cleanupSend(t, m, keyRunes("a"))
	for _, row := range m.tickedRows() {
		if row.Kind == cleanup.KindRemote {
			t.Errorf("remote row %s ticked", cleanup.DisplayName(row))
		}
	}
	// Even a remote row forced into the tick map stays out of the preview.
	for i, it := range m.items {
		if it.heading == "" && it.row.Kind == cleanup.KindRemote {
			m.ticked[i] = true
		}
	}
	m = press(t, m, enterKey)
	for _, rows := range h.previewed {
		for _, row := range rows {
			if row.Kind == cleanup.KindRemote {
				t.Errorf("remote row %s previewed", cleanup.DisplayName(row))
			}
		}
	}
}

// TestEnterWithNothingTickedSaysSo pins that enter with no ticked row says
// "nothing ticked" on the list and asks the engine nothing.
func TestEnterWithNothingTickedSaysSo(t *testing.T) {
	r := newRmRepo(t)
	r.merged("done")
	h := r.harness()
	m := setTicks(t, h.list(80, 24))
	m = press(t, m, enterKey)
	if m.phase != cleanupPhaseList || !strings.Contains(m.View(), "nothing ticked") {
		t.Errorf("phase %v view:\n%s", m.phase, m.View())
	}
	if len(h.previewed) != 0 || h.runCalls != 0 {
		t.Errorf("previewed %d, ran %d", len(h.previewed), h.runCalls)
	}
}

// TestUntickedItemsAreNeverPreviewedOrRun pins that only the ticked rows
// reach Preview and Run: an unticked proven row (ticked by default) is left
// alone.
func TestUntickedItemsAreNeverPreviewedOrRun(t *testing.T) {
	r := newRmRepo(t)
	keep := r.merged("keepme")
	r.merged("done")
	h := r.harness()
	m := setTicks(t, h.list(120, 40), "done")
	m = press(t, m, enterKey)
	m = press(t, m, keyRunes("y"))
	if len(h.previewed) != 1 || len(h.previewed[0]) != 1 || h.previewed[0][0].Name != "done" {
		t.Errorf("previewed %v, want only done", h.previewed)
	}
	if len(h.ran) != 1 || h.ran[0].Name != "done" {
		t.Errorf("ran %v, want only done", h.ran)
	}
	if _, err := os.Stat(keep); err != nil || !r.refExists("refs/heads/keepme") {
		t.Error("the unticked worktree or its branch was removed")
	}
}

// TestEachItemGetsOnlyItsOwnApprovals pins Ruling K on the TUI path: with a
// proven item, a dirty one and an unpushed one confirmed together by one
// typed word, each Item handed to Run carries exactly its own preview's
// needs; the dirty item's --force reaches no other item.
func TestEachItemGetsOnlyItsOwnApprovals(t *testing.T) {
	r := newRmRepo(t)
	r.merged("done")
	r.dirty("dirty")
	r.unpushed("kept")
	h := r.harness()
	m := setTicks(t, h.list(120, 60), "done", "dirty", "kept")
	m = press(t, m, enterKey)
	m = typeWord(t, m, "delete")
	m = press(t, m, enterKey)
	want := map[string]removal.Needs{"done": {}, "dirty": {Confirm: true, Force: true}, "kept": {Confirm: true}}
	if len(h.ran) != 3 {
		t.Fatalf("ran %d items", len(h.ran))
	}
	for _, it := range h.ran {
		if it.Approved != want[it.Name] {
			t.Errorf("%s approved %+v, want %+v", it.Name, it.Approved, want[it.Name])
		}
	}
	if !r.refExists("refs/heads/kept") {
		t.Error("kept's unmerged branch was deleted")
	}
}

// TestDeleteBranchToggleIsPerItem pins Ruling M on the TUI path: tab on one
// worktree item toggles deleting ITS offered unmerged branch only, the
// screen names the branch, a proven-only screen turns into the typed one,
// and the other item's branch is kept.
func TestDeleteBranchToggleIsPerItem(t *testing.T) {
	r := newRmRepo(t)
	r.unpushed("kept1")
	r.unpushed("kept2")
	h := r.harness()
	m := setTicks(t, h.list(120, 60), "kept1", "kept2")
	m = press(t, m, enterKey)
	m = confirmEntryAt(t, m, "kept1")
	m = press(t, m, tabKey)
	v := m.View()
	if !strings.Contains(v, "[x] also delete unmerged branch kept1") || !strings.Contains(v, "[ ] also delete unmerged branch kept2") {
		t.Fatalf("toggle not shown per item:\n%s", v)
	}
	m = typeWord(t, m, "delete")
	m = press(t, m, enterKey)
	for _, it := range h.ran {
		switch it.Name {
		case "kept1":
			if it.Approved != (removal.Needs{Confirm: true, Delete: true}) {
				t.Errorf("kept1 approved %+v", it.Approved)
			}
		case "kept2":
			if it.Approved != (removal.Needs{Confirm: true}) {
				t.Errorf("kept2 approved %+v", it.Approved)
			}
		}
	}
	if r.refExists("refs/heads/kept1") || !r.refExists("refs/heads/kept2") {
		t.Errorf("kept1 exists %v (want deleted), kept2 exists %v (want kept)", r.refExists("refs/heads/kept1"), r.refExists("refs/heads/kept2"))
	}
}

// TestDeleteBranchToggleAddsTheDeleteApproval pins that toggling an
// entry's offered branch deletion adds -D to the approvals that entry shows
// (and gets), and toggling it off again restores the screen exactly. (An
// entry that offers a branch is never proven, so its prompt is already the
// typed word; the toggle never turns a y prompt into a typed one.)
func TestDeleteBranchToggleAddsTheDeleteApproval(t *testing.T) {
	r := newRmRepo(t)
	r.unpushed("kept")
	h := r.harness()
	m := setTicks(t, h.list(120, 40), "kept")
	m = press(t, m, enterKey)
	before := m.View()
	m = press(t, m, tabKey)
	if !strings.Contains(m.View(), "approvals: --accept-loss -D") {
		t.Errorf("toggle on: approvals not shown:\n%s", m.View())
	}
	m = press(t, m, tabKey)
	if m.View() != before {
		t.Errorf("toggle off did not restore the screen")
	}
}

// TestRunPanicSurfacesAsResults pins that a Run that panics never ends the
// program silently: what progress reported stands, the rest is said to be
// unknown, and the exit message carries both.
func TestRunPanicSurfacesAsResults(t *testing.T) {
	r := newRmRepo(t)
	r.merged("one")
	r.merged("two")
	h := r.harness()
	h.stubRun = func(items []removal.Item, progress func(removal.Outcome)) []removal.Outcome {
		progress(removal.NewOutcome(items[0], removal.WtRemoved, removal.BrDeleted, ""))
		panic("boom")
	}
	m := setTicks(t, h.list(120, 40), "one", "two")
	m = press(t, m, enterKey)
	m = press(t, m, keyRunes("y"))
	if m.phase != cleanupPhaseResults || m.done {
		t.Fatalf("phase %v done %v", m.phase, m.done)
	}
	v := m.View()
	for _, want := range []string{"worktree one: worktree removed, branch deleted", "worktree two: worktree failed, branch retained_unknown",
		"the removal stopped: boom", "may or may not have been removed"} {
		if !strings.Contains(v, want) {
			t.Errorf("results lack %q:\n%s", want, v)
		}
	}
	if msg := m.exitMessage(); !strings.Contains(msg, "worktree two") || !strings.Contains(msg, "boom") {
		t.Errorf("exit message:\n%s", msg)
	}
}

// TestRemovalOffSaysWhy pins that removal turned off (the registry did not
// load) is said on enter and previews nothing.
func TestRemovalOffSaysWhy(t *testing.T) {
	r := newRmRepo(t)
	r.merged("done")
	h := r.harness()
	m := h.list(120, 40)
	m.rm.Off = "removal refused: the registry failed to load: boom"
	m = press(t, m, enterKey)
	if m.phase != cleanupPhaseList || !strings.Contains(m.View(), "the registry failed to load") || len(h.previewed) != 0 {
		t.Errorf("phase %v previewed %d view:\n%s", m.phase, len(h.previewed), m.View())
	}
}

// confirmScreens are the removal flow's screens over one repository, for the
// fit test: confirm (y prompt, typed prompt with a dirty worktree and
// toggles), progress and results.
func confirmScreens(t *testing.T) map[string]cleanupListModel {
	t.Helper()
	r := newRmRepo(t)
	r.merged("done")
	r.dirty("a-dirty-worktree-with-a-long-name")
	r.unpushed("kept")
	r.loose("loose")
	h := r.harness()
	h.procs = fakeProcs{"a-dirty-worktree-with-a-long-name": {PID: 4242, Command: "node " + strings.Repeat("server.js ", 12)}}
	out := map[string]cleanupListModel{}
	m := press(t, setTicks(t, h.list(80, 24), "done"), enterKey)
	out["confirm, proven"] = m
	m = press(t, setTicks(t, h.list(80, 24), "done", "a-dirty-worktree-with-a-long-name", "kept", "loose"), enterKey)
	m = press(t, m, downKey)
	m = press(t, m, tabKey)
	m = typeWord(t, m, "delet")
	out["confirm, typed"] = m
	gate := make(chan struct{})
	h.stubRun = func(items []removal.Item, progress func(removal.Outcome)) []removal.Outcome {
		<-gate
		o := removal.NewOutcome(items[0], removal.WtRemoved, removal.BrDeleted, "")
		progress(o)
		return nil
	}
	m = press(t, setTicks(t, h.list(80, 24), "done", "kept"), enterKey)
	m = typeWord(t, m, "delete")
	next, cmd := m.Update(enterKey)
	out["progress"] = next.(cleanupListModel)
	close(gate)
	_ = cmd
	h.stubRun = func(items []removal.Item, progress func(removal.Outcome)) []removal.Outcome {
		var outs []removal.Outcome
		for _, it := range items {
			o := removal.NewOutcome(it, removal.WtFailed, removal.BrRetainedUnknown, "git worktree said: "+strings.Repeat("a long reason ", 15))
			o.LossLines = []string{"1 commit exists only on this machine"}
			o.AddWarning("a warning\x1b[31m with an escape")
			o.DeletedTip = strings.Repeat("ab", 20)
			outs = append(outs, o)
		}
		return outs
	}
	m = press(t, setTicks(t, h.list(80, 24), "done", "kept", "loose", "a-dirty-worktree-with-a-long-name"), enterKey)
	m = typeWord(t, m, "delete")
	out["results"] = press(t, m, enterKey)
	return out
}

// TestConfirmFitsTheTerminal80x24 pins the removal flow's screens at 80x24
// (the size the real-screen check uses): no line wider than 80, no more than
// 24 lines, the prompt (the screen's last line) always on screen, and no raw
// escape from engine text. 40x12 and 40x8 too: at 40x8 the title and the
// wrapped prompt do not fit together, and the title gives way so the prompt
// stays.
func TestConfirmFitsTheTerminal80x24(t *testing.T) {
	screens := confirmScreens(t)
	for _, size := range [][2]int{{80, 24}, {40, 12}, {40, 8}, {30, 12}} {
		for name, m := range screens {
			name = fmt.Sprintf("%s %dx%d", name, size[0], size[1])
			m = cleanupSend(t, m, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			checkRemovalScreen(t, name, m.View(), size[0], size[1])
		}
	}
}

// checkRemovalScreen asserts one removal screen fits and keeps its prompt.
func checkRemovalScreen(t *testing.T, name, v string, width, height int) {
	t.Helper()
	{
		lines := strings.Split(v, "\n")
		if len(lines) > height {
			t.Errorf("%s: %d lines", name, len(lines))
		}
		for _, l := range lines {
			if w := lipgloss.Width(l); w > width {
				t.Errorf("%s: line %d wide: %q", name, w, l)
			}
		}
		if strings.Contains(ansi.Strip(v), "\x1b") {
			t.Errorf("%s: raw escape in view", name)
		}
		last := ansi.Strip(lines[len(lines)-1])
		switch {
		case strings.HasPrefix(name, "confirm, proven"):
			if !strings.Contains(last, "cancel") {
				t.Errorf("%s: prompt not last: %q", name, last)
			}
		case strings.HasPrefix(name, "confirm, typed"):
			if !strings.HasPrefix(last, "> delet_") {
				t.Errorf("%s: typed line not last: %q", name, last)
			}
		}
		if strings.HasPrefix(name, "confirm") {
			checkPromptCounts(t, name, v)
		}
	}
}

// checkPromptCounts asserts Ruling U on a confirm screen: the prompt states
// the --force and -D counts at every size.
func checkPromptCounts(t *testing.T, name, v string) {
	t.Helper()
	if s := squash(ansi.Strip(v)); !strings.Contains(s, "force,") || !strings.Contains(s, "-D)") {
		t.Errorf("%s: the prompt does not count the force and -D approvals:\n%s", name, v)
	}
}

// removalScreens caches confirmScreens per test, so the fit test builds the
// repository once and measures each screen at every size.
var removalScreens = map[*testing.T]map[string]cleanupListModel{}

// removalScreen is the named removal-flow screen resized to width x height,
// for TestEveryListPhaseFitsTheTerminal.
func removalScreen(name string) func(t *testing.T, width, height int) string {
	return func(t *testing.T, width, height int) string {
		t.Helper()
		screens, ok := removalScreens[t]
		if !ok {
			screens = confirmScreens(t)
			removalScreens[t] = screens
			t.Cleanup(func() { delete(removalScreens, t) })
		}
		m, ok := screens[name]
		if !ok {
			t.Fatalf("no removal screen %q", name)
		}
		v := cleanupSend(t, m, tea.WindowSizeMsg{Width: width, Height: height}).View()
		if strings.HasPrefix(name, "confirm") {
			checkPromptCounts(t, name+fmt.Sprintf(" %dx%d", width, height), v)
		}
		return v
	}
}
