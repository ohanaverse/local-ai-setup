package tui

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/removal"
)

// Review round 1 of the removal flow: signals during a run (Ruling T), tall
// entries, the cursor's toggle, typed-prompt keys, sanitising and the
// mapping of outcomes to items.

// gatedRun is a stub Run that waits for one value on gate before each item
// and reports it removed.
func gatedRun(gate chan struct{}) func([]removal.Item, func(removal.Outcome)) []removal.Outcome {
	return func(items []removal.Item, progress func(removal.Outcome)) []removal.Outcome {
		var out []removal.Outcome
		for _, it := range items {
			<-gate
			o := removal.NewOutcome(it, removal.WtRemoved, removal.BrDeleted, "")
			o.DeletedTip = strings.Repeat("c", 40)
			out = append(out, o)
			progress(o)
		}
		return out
	}
}

// TestSignalDuringRunWaitsAndKeepsOutcomes pins Ruling T: a SIGINT/SIGTERM
// (and ctrl+c) while a removal runs does not end the program; the screen
// says to wait; each outcome reaches the exit log as it arrives; once the
// run ends the asked-for quit happens, with every outcome logged once.
func TestSignalDuringRunWaitsAndKeepsOutcomes(t *testing.T) {
	r := newRmRepo(t)
	r.merged("one")
	r.merged("two")
	h := r.harness()
	gate := make(chan struct{}, 2)
	h.stubRun = gatedRun(gate)
	m := press(t, setTicks(t, h.list(80, 24), "one", "two"), enterKey)
	next, cmd := m.Update(keyRunes("y"))
	m = next.(cleanupListModel)
	if m.phase != cleanupPhaseRunning {
		t.Fatalf("phase %v", m.phase)
	}
	if !strings.Contains(m.View(), "removing, please wait") {
		t.Errorf("running screen lacks the wait line:\n%s", m.View())
	}
	for _, msg := range []tea.Msg{cleanupSignalMsg{sig: syscall.SIGTERM}, cleanupSignalMsg{sig: syscall.SIGINT}, ctrlCKey} {
		var c tea.Cmd
		next, c = m.Update(msg)
		m = next.(cleanupListModel)
		if m.done || c != nil || m.phase != cleanupPhaseRunning {
			t.Fatalf("%T mid-run: done %v cmd %v phase %v", msg, m.done, c != nil, m.phase)
		}
	}
	if !strings.Contains(m.View(), "wt quits when this run ends") {
		t.Errorf("a quit asked mid-run is not said:\n%s", m.View())
	}
	gate <- struct{}{}
	next, cmd = m.Update(cmd())
	m = next.(cleanupListModel)
	if !strings.Contains(m.exitMessage(), "worktree one: worktree removed") {
		t.Errorf("first outcome not in the exit log as it arrived:\n%s", m.exitMessage())
	}
	gate <- struct{}{}
	quit := false
	for cmd != nil {
		msg := cmd()
		if _, ok := msg.(tea.QuitMsg); ok {
			quit = true
			break
		}
		next, cmd = m.Update(msg)
		m = next.(cleanupListModel)
	}
	if !quit || !m.done {
		t.Fatalf("the quit asked mid-run did not happen after the run: quit %v done %v phase %v", quit, m.done, m.phase)
	}
	log := m.exitMessage()
	for _, name := range []string{"worktree one:", "worktree two:"} {
		if n := strings.Count(log, name); n != 1 {
			t.Errorf("%s logged %d times:\n%s", name, n, log)
		}
	}
}

// TestSignalOutsideRunQuits pins Ruling T's other half: outside a run a
// signal quits as before (the list, the confirm screen, the results).
func TestSignalOutsideRunQuits(t *testing.T) {
	m := newSizedCleanupList(t, cleanupFixture(), 80, 24)
	next, cmd := m.Update(cleanupSignalMsg{sig: syscall.SIGINT})
	if !next.(cleanupListModel).done || cmd == nil {
		t.Fatalf("signal on the list: done %v cmd %v", next.(cleanupListModel).done, cmd)
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("signal on the list did not quit")
	}
}

// TestQuitMidRunNamesTheInFlightItem pins Ruling T (d): a program ended
// mid-run anyway prints the finished outcomes and names the item in flight
// as state unknown and the rest as not attempted — never silence.
func TestQuitMidRunNamesTheInFlightItem(t *testing.T) {
	r := newRmRepo(t)
	r.merged("one")
	r.merged("two")
	r.merged("three")
	h := r.harness()
	gate := make(chan struct{}, 3)
	h.stubRun = gatedRun(gate)
	m := press(t, setTicks(t, h.list(80, 24), "one", "two", "three"), enterKey)
	next, cmd := m.Update(keyRunes("y"))
	m = next.(cleanupListModel)
	gate <- struct{}{}
	next, _ = m.Update(cmd())
	m = next.(cleanupListModel)
	log := m.exitMessage()
	// The run's order is the list's, not the tick order.
	name := func(k int) string { return m.run.entries[m.run.runAt[k]].pv.Item.Name }
	for _, want := range []string{"worktree " + name(0) + ": worktree removed", "worktree " + name(1) + ": state unknown",
		"worktree " + name(2) + ": not attempted"} {
		if !strings.Contains(log, want) {
			t.Errorf("exit message lacks %q:\n%s", want, log)
		}
	}
	close(gate)
}

// TestForwardSignalsSendsAMessage pins the program's own signal handler
// (bubbletea's is off): a SIGTERM to the process becomes one
// cleanupSignalMsg, and the process is not ended by it.
func TestForwardSignalsSendsAMessage(t *testing.T) {
	got := make(chan tea.Msg, 4)
	stop := forwardSignals(func(msg tea.Msg) { got <- msg })
	defer stop()
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case msg := <-got:
		if s, ok := msg.(cleanupSignalMsg); !ok || s.sig != syscall.SIGTERM {
			t.Errorf("got %#v", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no message for SIGTERM")
	}
}

// manyProcs reports n processes in the directory named base.
type manyProcs struct {
	base string
	n    int
}

func (p manyProcs) InDir(dir string) ([]removal.Proc, error) {
	if !strings.HasSuffix(dir, p.base) {
		return nil, nil
	}
	var out []removal.Proc
	for i := 0; i < p.n; i++ {
		out = append(out, removal.Proc{PID: 1000 + i, Command: "node server.js"})
	}
	return out, nil
}

// TestTallEntryShowsToggleAndApprovalsFirst pins that an entry taller than
// the screen (a worktree with 40 processes) shows its branch toggle and its
// approvals without scrolling, at 80x24 and at 40x8, and that pgdown
// scrolls within it (and pgup back) instead of snapping back.
func TestTallEntryShowsToggleAndApprovalsFirst(t *testing.T) {
	r := newRmRepo(t)
	r.merged("done")
	r.unpushed("kept")
	h := r.harness()
	h.procs = nil
	for _, size := range [][2]int{{80, 24}, {40, 8}} {
		h2 := *h
		m := setTicks(t, h2.list(size[0], size[1]), "done", "kept")
		m.rm.Preview = func(rows []cleanup.Row) []removal.ItemPreview {
			items := make([]removal.Item, len(rows))
			for i, row := range rows {
				items[i] = removal.ItemFromRow(row)
			}
			e := h.eng
			e.Opts.Procs = manyProcs{base: "kept", n: 40}
			return e.Preview(items)
		}
		m = press(t, m, enterKey)
		m = press(t, m, downKey) // the cursor on kept
		v := m.View()
		checkRemovalScreen(t, fmt.Sprintf("tall %dx%d", size[0], size[1]), v, size[0], size[1])
		if !strings.Contains(squash(v), squash("[ ] also delete unmerged branch kept")) || !strings.Contains(v, "approvals: --accept-loss") {
			t.Errorf("%dx%d: toggle or approvals not on screen:\n%s", size[0], size[1], v)
		}
		m2 := press(t, m, tea.KeyMsg{Type: tea.KeyPgDown})
		if m2.View() == v || !strings.Contains(m2.View(), "running in it: pid 10") {
			t.Errorf("%dx%d: pgdown did not scroll within the entry:\n%s", size[0], size[1], m2.View())
		}
		m3 := press(t, m2, tea.KeyMsg{Type: tea.KeyPgUp})
		if !strings.Contains(m3.View(), "approvals: --accept-loss") {
			t.Errorf("%dx%d: pgup did not come back:\n%s", size[0], size[1], m3.View())
		}
	}
}

// TestTabTogglesTheCursorEntry pins that tab toggles the HIGHLIGHTED entry,
// reached with the arrow keys, after the confirm screen grouped the items
// (the proven one first): the first entries' branches stay.
func TestTabTogglesTheCursorEntry(t *testing.T) {
	r := newRmRepo(t)
	r.merged("done")
	r.unpushed("kept1")
	r.unpushed("kept2")
	h := r.harness()
	m := setTicks(t, h.list(120, 60), "kept1", "kept2", "done")
	m = press(t, m, enterKey)
	if m.conf.entries[0].pv.Item.Name != "done" {
		t.Fatalf("grouping: first entry %s", m.conf.entries[0].pv.Item.Name)
	}
	m = press(t, m, downKey)
	m = press(t, m, downKey)
	m = press(t, m, tabKey)
	if !strings.Contains(m.View(), "[x] also delete unmerged branch kept2") || !strings.Contains(m.View(), "[ ] also delete unmerged branch kept1") {
		t.Fatalf("tab did not toggle the cursor entry:\n%s", m.View())
	}
	m = typeWord(t, m, "delete")
	m = press(t, m, enterKey)
	if !r.refExists("refs/heads/kept1") || r.refExists("refs/heads/kept2") {
		t.Errorf("kept1 exists %v (want kept), kept2 exists %v (want deleted)", r.refExists("refs/heads/kept1"), r.refExists("refs/heads/kept2"))
	}
}

// TestTypedPromptCancelsOnSpaceAndAlt pins that keys that cannot be part of
// the word cancel the typed prompt instead of being dropped or counted:
// space, and an alt-modified letter (alt+d would otherwise type "d").
func TestTypedPromptCancelsOnSpaceAndAlt(t *testing.T) {
	r := newRmRepo(t)
	r.dirty("dirty")
	h := r.harness()
	for _, k := range []tea.KeyMsg{spaceKey, {Type: tea.KeyRunes, Runes: []rune("d"), Alt: true}} {
		m := press(t, setTicks(t, h.list(120, 40), "dirty"), enterKey)
		m = typeWord(t, m, "del")
		m = press(t, m, k)
		if m.phase != cleanupPhaseList || !strings.Contains(m.status, "nothing was removed") {
			t.Errorf("%q: phase %v status %q; want cancelled", k.String(), m.phase, m.status)
		}
	}
	if h.runCalls != 0 {
		t.Errorf("Run called %d times", h.runCalls)
	}
}

// TestRemovalScreensSanitiseEngineText pins that an escape sequence or a
// bell in engine or OS text (a process command, a loss line, an outcome's
// reason, warning and loss line) never reaches the terminal.
func TestRemovalScreensSanitiseEngineText(t *testing.T) {
	const evil = "x\x1b[31m\x1b]0;title\x07y"
	r := newRmRepo(t)
	r.dirty("dirty")
	h := r.harness()
	h.procs = fakeProcs{"dirty": {PID: 7, Command: "vim " + evil}}
	h.stubRun = func(items []removal.Item, progress func(removal.Outcome)) []removal.Outcome {
		o := removal.Outcome{Item: items[0], Worktree: removal.WtFailed, Branch: removal.BrRetainedUnknown,
			Reason: "reason " + evil, Warnings: []string{"warn " + evil}, LossLines: []string{"loss " + evil}}
		progress(o)
		return []removal.Outcome{o}
	}
	m := press(t, setTicks(t, h.list(120, 40), "dirty"), enterKey)
	m.conf.entries[0].pv.LossLines = append(m.conf.entries[0].pv.LossLines, "preview loss "+evil)
	bad := func(name, v string) {
		t.Helper()
		if strings.Contains(v, "\x1b[31m") || strings.Contains(v, "\x1b]0;") || strings.Contains(v, "\x07") {
			t.Errorf("%s: raw control text reached the screen: %q", name, v)
		}
	}
	v := m.View()
	if !strings.Contains(v, "vim x") || !strings.Contains(v, "preview loss x") {
		t.Fatalf("confirm screen lacks the process or loss line:\n%s", v)
	}
	bad("confirm", v)
	m = typeWord(t, m, "delete")
	m = press(t, m, enterKey)
	v = m.View()
	if !strings.Contains(v, "reason x") || !strings.Contains(v, "warn x") || !strings.Contains(v, "loss x") {
		t.Fatalf("results lack the outcome's text:\n%s", v)
	}
	bad("results", v)
	bad("exit message", m.exitMessage())
}

// TestResultsKeepTheRestoreIDWholeAt40Columns pins that a deleted branch's
// 40-character commit id is on one line of the results screen at 40
// columns, so it can be copied whole.
func TestResultsKeepTheRestoreIDWholeAt40Columns(t *testing.T) {
	r := newRmRepo(t)
	r.merged("done")
	tip := r.git("rev-parse", "refs/heads/done")
	h := r.harness()
	m := press(t, setTicks(t, h.list(40, 50), "done"), enterKey)
	m = press(t, m, keyRunes("y"))
	v := m.View()
	whole := false
	for _, l := range strings.Split(v, "\n") {
		whole = whole || strings.Contains(l, tip)
	}
	if !whole {
		t.Errorf("the restore id %s is not whole on any line at 40 columns:\n%s", tip, v)
	}
	checkRemovalScreen(t, "results 40x50", v, 40, 50)
}

// TestRefreshErrorEndsWithTheResults pins that a repository that cannot be
// re-read after a run ends the program with the error, and the exit
// message still carries every outcome.
func TestRefreshErrorEndsWithTheResults(t *testing.T) {
	r := newRmRepo(t)
	r.merged("done")
	h := r.harness()
	m := setTicks(t, h.list(80, 24), "done")
	m.rm.Refresh = func() (cleanup.Report, error) { return cleanup.Report{}, errors.New("git broke") }
	m = press(t, m, enterKey)
	m = press(t, m, keyRunes("y"))
	m = press(t, m, keyRunes("x"))
	if !m.done || m.exitErr == nil || !strings.Contains(m.exitErr.Error(), "git broke") {
		t.Fatalf("done %v exitErr %v", m.done, m.exitErr)
	}
	if !strings.Contains(m.exitMessage(), "worktree done: worktree removed") {
		t.Errorf("exit message:\n%s", m.exitMessage())
	}
}

// TestPreviewMustAnswerTheTickedRows pins previewCmd's check: a preview of
// other items (here the ticked rows in the other order) or of fewer items
// removes nothing and says why.
func TestPreviewMustAnswerTheTickedRows(t *testing.T) {
	r := newRmRepo(t)
	r.merged("one")
	r.merged("two")
	h := r.harness()
	for name, answer := range map[string]func([]removal.ItemPreview) []removal.ItemPreview{
		"swapped": func(ps []removal.ItemPreview) []removal.ItemPreview { return []removal.ItemPreview{ps[1], ps[0]} },
		"short":   func(ps []removal.ItemPreview) []removal.ItemPreview { return ps[:1] },
	} {
		m := setTicks(t, h.list(80, 24), "one", "two")
		real := m.rm.Preview
		m.rm.Preview = func(rows []cleanup.Row) []removal.ItemPreview { return answer(real(rows)) }
		m = press(t, m, enterKey)
		if m.phase != cleanupPhaseList || !strings.Contains(m.status, "nothing was removed") || h.runCalls != 0 {
			t.Errorf("%s: phase %v status %q runs %d", name, m.phase, m.status, h.runCalls)
		}
	}
}

// TestOutcomesMapToTheirItemsAroundARefusal pins the run's index mapping:
// with the middle entry refused at confirmation (its preview edited, so not
// genuine), the first and third are run and each outcome lands on its own
// item, the refusal on the middle one.
func TestOutcomesMapToTheirItemsAroundARefusal(t *testing.T) {
	r := newRmRepo(t)
	pa := r.merged("aa")
	pb := r.merged("bb")
	pc := r.merged("cc")
	h := r.harness()
	m := press(t, setTicks(t, h.list(120, 40), "aa", "bb", "cc"), enterKey)
	if m.conf.entries[1].pv.Item.Name != "bb" {
		t.Fatalf("entries %v", m.conf.entries)
	}
	m.conf.entries[1].pv.Needs.Confirm = true // edited: Confirmed refuses it
	m = typeWord(t, m, "delete")
	m = press(t, m, enterKey)
	if len(h.ran) != 2 || h.ran[0].Name != "aa" || h.ran[1].Name != "cc" {
		t.Fatalf("ran %v", h.ran)
	}
	res := m.run.results
	if res[0].Item.Name != "aa" || res[0].Worktree != removal.WtRemoved ||
		res[1].Item.Name != "bb" || res[1].Worktree != removal.WtRefused ||
		res[2].Item.Name != "cc" || res[2].Worktree != removal.WtRemoved {
		t.Errorf("results %+v", res)
	}
	if _, err := os.Stat(pb); err != nil {
		t.Error("the refused item was removed")
	}
	for _, p := range []string{pa, pc} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s not removed", p)
		}
	}
}
