package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// printingStart scripts a start that prints text the way the engine does,
// into Options.Out, and ends with err.
func printingStart(err error, text string) func(int, context.Context, lifecycle.Target, lifecycle.Options) error {
	return func(_ int, _ context.Context, _ lifecycle.Target, opts lifecycle.Options) error {
		if opts.Out != nil {
			fmt.Fprint(opts.Out, text)
		}
		return err
	}
}

// The three lines of a start that evicted a model wt thought would stay and
// then could not write config.yaml, as internal/lifecycle prints them
// (TestStartSendsEveryLineItPrintsToOut).
const startLines = "wt: omlx unloaded omlx/old to make room (not predicted)\n" +
	"wt: LiteLLM route not updated: open /scratch/config.yaml: permission denied\n" +
	"wt: LiteLLM route not updated: open /scratch/config.yaml: permission denied\n"

// TestStartHandsTheEngineAWriter pins #275 at its root: the picker's start
// gives the engine somewhere to print. Without Options.Out every line of a
// start went to stderr under the alt screen.
func TestStartHandsTheEngineAWriter(t *testing.T) {
	m := startFixture(t, "omlx", "omlx/qwen3.8", "qwen3.8")
	calls := stubStartModel(t, printingStart(errors.New("boom"), ""))
	got, _ := enterStartRow(t, m, "omlx/qwen3.8")
	_, _ = updateMsg(got, recvStart(t, got))
	if calls.len() != 1 || calls.at(0).opts.Out == nil {
		t.Fatalf("the engine was started %d times; want once, with Options.Out set", calls.len())
	}
}

// TestSuccessfulStartPutsTheEnginesLinesAboveTheAgent pins the case #275
// costs most. The start succeeds, the route write fails, the agent launches
// and LiteLLM answers "Invalid model name"; the line that says why was
// printed under the alt screen. Now every line the engine printed is in the
// notes that are printed on the real terminal the moment the alt screen is
// released, in the engine's own words, "(not predicted)" included. The launch
// is made to fail here (an agent no driver knows), so the picker comes back
// and the same lines must be on its status ahead of the failure.
func TestSuccessfulStartPutsTheEnginesLinesAboveTheAgent(t *testing.T) {
	stubRouteNotes(t)
	m := startFixture(t, "omlx", "omlx/qwen3.8", "qwen3.8")
	m.agent = "not-a-real-agent"
	stubStartModel(t, printingStart(nil, startLines))
	got, _ := enterStartRow(t, m, "omlx/qwen3.8")
	got, _ = updateMsg(got, recvStart(t, got))
	if pendingRouteNotes != startLines {
		t.Errorf("route notes = %q\nwant          %q", pendingRouteNotes, startLines)
	}
	want := "omlx unloaded omlx/old to make room (not predicted)\n" +
		"LiteLLM route not updated: open /scratch/config.yaml: permission denied\n" +
		"LiteLLM route not updated: open /scratch/config.yaml: permission denied\n" +
		"launch failed: "
	if !strings.HasPrefix(got.status, want) {
		t.Errorf("status = %q\nwant it to begin %q", got.status, want)
	}
}

// TestCancelledStartKeepsItsLinesForTheTerminal verifies a cancelled start
// leaves the same record a failed one does: the engine's lines, then what
// became of the start.
func TestCancelledStartKeepsItsLinesForTheTerminal(t *testing.T) {
	stubRouteNotes(t)
	m := startFixture(t, "omlx", "omlx/qwen3.8", "qwen3.8")
	stubStartModel(t, func(_ int, ctx context.Context, _ lifecycle.Target, opts lifecycle.Options) error {
		<-ctx.Done()
		return unloadingStart(ctx.Err(), "omlx/old")(0, ctx, lifecycle.Target{}, opts)
	})
	got, _ := enterStartRow(t, m, "omlx/qwen3.8")
	got, _ = updateMsg(got, tea.KeyMsg{Type: tea.KeyEsc})
	_, _ = updateMsg(got, recvStart(t, got))
	if want := "wt: omlx unloaded omlx/old to make room\nwt: cancelled\n"; pendingRouteNotes != want {
		t.Errorf("route notes = %q, want %q", pendingRouteNotes, want)
	}
}

// TestStartWaitsForTheProxyOffTheUpdateGoroutine pins where the wait for the
// proxy restart happens: on the start's own goroutine, before it reports, and
// never in Update. finishStart used to call it, which froze the start screen
// for as long as the restart took (the one wait left on the update goroutine,
// docs/internals/tui.md). The result is not sent while the wait is held, it
// carries what the restart printed meanwhile, and handling it waits for
// nothing.
func TestStartWaitsForTheProxyOffTheUpdateGoroutine(t *testing.T) {
	stubRouteNotes(t)
	m := startFixture(t, "omlx", "omlx/qwen3.8", "qwen3.8")
	m.agent = "not-a-real-agent"
	out := &startOutputProbe{}
	stubStartModel(t, func(_ int, _ context.Context, _ lifecycle.Target, opts lifecycle.Options) error {
		out.set(opts)
		return nil
	})
	entered, release := make(chan struct{}), make(chan struct{})
	var inUpdate bool
	var mu sync.Mutex
	oldWait := waitPendingRoutes
	waitPendingRoutes = func() {
		mu.Lock()
		called := inUpdate
		mu.Unlock()
		if called {
			t.Error("waitPendingRoutes was called while Update was running")
			return
		}
		close(entered)
		<-release
		// What a proxy restart prints while it is waited for.
		out.print("wt: LiteLLM restart failed: exit status 1\n")
	}
	t.Cleanup(func() { waitPendingRoutes = oldWait })

	got, _ := enterStartRow(t, m, "omlx/qwen3.8")
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the start's goroutine never waited for the proxy restart")
	}
	select {
	case msg := <-got.start.ch:
		t.Fatalf("the start reported %T while the proxy restart was still pending", msg)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	msg := recvStart(t, got)
	done, ok := msg.(startDoneMsg)
	if !ok || done.out != "wt: LiteLLM restart failed: exit status 1\n" {
		t.Fatalf("the start reported %#v, want a result carrying the restart's warning", msg)
	}
	mu.Lock()
	inUpdate = true
	mu.Unlock()
	got, _ = updateMsg(got, msg)
	if !strings.HasPrefix(got.status, "LiteLLM restart failed: exit status 1\nlaunch failed: ") {
		t.Errorf("status = %q, want the restart's warning above the launch failure", got.status)
	}
}

// TestStartedModelOffersNoCancelWhileItWaitsForTheProxy pins the window the
// wait on the start's goroutine opens (#275): the engine has started the
// model and is updating its route, or has returned and the proxy restart is
// what is left — 10 to 20 seconds in which the start screen used to be frozen
// and now takes keys. Nothing can be called off there: the model is loaded.
// So from the routing stage on the screen offers no cancel, esc and q do
// nothing, the launch follows the wait, and ctrl+c quits wt — phaseRouting's
// rule, for the same reason. An esc there used to end in "cancelled" with the
// model loaded and routed.
func TestStartedModelOffersNoCancelWhileItWaitsForTheProxy(t *testing.T) {
	stubRouteNotes(t)
	m := startFixture(t, "omlx", "omlx/qwen3.8", "qwen3.8")
	m.agent = "not-a-real-agent"
	stubStartModel(t, func(_ int, _ context.Context, _ lifecycle.Target, opts lifecycle.Options) error {
		opts.Progress(lifecycle.StageRouting)
		return nil
	})
	entered, release := make(chan struct{}), make(chan struct{})
	oldWait := waitPendingRoutes
	waitPendingRoutes = func() { close(entered); <-release }
	t.Cleanup(func() { waitPendingRoutes = oldWait })

	got, _ := enterStartRow(t, m, "omlx/qwen3.8")
	got, _ = updateMsg(got, recvStart(t, got))
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the start's goroutine never waited for the proxy restart")
	}
	view := got.View()
	if !strings.Contains(view, "updating LiteLLM routes") || strings.Contains(view, "cancel") || !strings.Contains(view, "[ctrl+c] quit wt") {
		t.Errorf("the routing stage's screen must name the wait and ctrl+c, and offer no cancel:\n%s", view)
	}
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEsc}, {Type: tea.KeyRunes, Runes: []rune("q")}} {
		next, cmd := updateMsg(got, key)
		if next.start == nil || next.start.cancelling || cmd != nil || next.phase != phaseStarting {
			t.Errorf("%q at the routing stage: cancelling = %v, a command = %v, phase = %v; want it ignored", key.String(), next.start != nil && next.start.cancelling, cmd != nil, next.phase)
		}
		got = next
	}
	if quit, cmd := updateMsg(got, tea.KeyMsg{Type: tea.KeyCtrlC}); cmd == nil || quit.start.cancelling {
		t.Errorf("ctrl+c at the routing stage: a command = %v, cancelling = %v; want tea.Quit and no cancel", cmd != nil, quit.start.cancelling)
	}
	close(release)
	got, _ = updateMsg(got, recvStart(t, got))
	if !strings.HasPrefix(got.status, "launch failed: ") {
		t.Errorf("status = %q, want the launch attempted (it fails here: no such agent), not a cancel", got.status)
	}
}

// TestCancelThatLostToTheStartSaysTheModelIsRunning pins the one cancel that
// can still meet a started model: esc pressed before the routing stage was
// on screen, and an engine that had already got past the point of stopping.
// The launch is called off, which is what was asked, and the status says
// what is true — the model is running — where it used to say "cancelled".
func TestCancelThatLostToTheStartSaysTheModelIsRunning(t *testing.T) {
	stubRouteNotes(t)
	m := startFixture(t, "omlx", "omlx/qwen3.8", "qwen3.8")
	stubStartModel(t, func(_ int, ctx context.Context, _ lifecycle.Target, opts lifecycle.Options) error {
		<-ctx.Done()
		fmt.Fprint(opts.Out, "wt: omlx unloaded omlx/old to make room\n")
		return nil
	})
	got, _ := enterStartRow(t, m, "omlx/qwen3.8")
	got, _ = updateMsg(got, tea.KeyMsg{Type: tea.KeyEsc})
	got, _ = updateMsg(got, recvStart(t, got))
	if want := "omlx unloaded omlx/old to make room\nstarted omlx/qwen3.8; launch cancelled"; got.status != want || got.phase != phaseModel {
		t.Errorf("status = %q, phase = %v; want %q on the model picker", got.status, got.phase, want)
	}
	if want := "wt: omlx unloaded omlx/old to make room\nwt: started omlx/qwen3.8; launch cancelled\n"; pendingRouteNotes != want {
		t.Errorf("route notes = %q, want %q", pendingRouteNotes, want)
	}
}

// TestRefusedStartThatPrintedKeepsItsLinesForTheTerminal verifies the replace
// dialog's way out: the dialog has no status line, so a start that was
// refused as occupied after printing something leaves its lines for the real
// terminal instead of dropping them.
func TestRefusedStartThatPrintedKeepsItsLinesForTheTerminal(t *testing.T) {
	stubRouteNotes(t)
	m := startFixture(t, "omlx", "omlx/qwen3.8", "qwen3.8")
	stubStartModel(t, printingStart(&lifecycle.OccupiedError{Occupants: []localmodels.Entry{{ModelID: "omlx/old"}}}, "wt: LiteLLM route not updated: boom\n"))
	got, _ := enterStartRow(t, m, "omlx/qwen3.8")
	got, _ = updateMsg(got, recvStart(t, got))
	if got.phase != phaseReplaceConfirm || pendingRouteNotes != "wt: LiteLLM route not updated: boom\n" {
		t.Errorf("phase = %v, route notes = %q; want the replace dialog and the line kept", got.phase, pendingRouteNotes)
	}
}

// startOutputProbe hands a test the writer the flow gave the engine, so the
// test can print into it later, as the engine's proxy restart does.
type startOutputProbe struct {
	mu   sync.Mutex
	opts lifecycle.Options
}

func (p *startOutputProbe) set(opts lifecycle.Options) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.opts = opts
}

func (p *startOutputProbe) print(text string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.opts.Out != nil {
		fmt.Fprint(p.opts.Out, text)
	}
}

// TestFailedStartThatOwesARestartSaysWhatItWaitsFor verifies the start screen
// after a start that failed having already displaced a model: a proxy restart
// is in flight, the flow waits for it, and the screen says so instead of
// showing the engine's last stage (or, after a cancel, "waiting for the
// server to stop" over a server that has stopped). A failed start that left
// no restart behind reports at once, with no such stage.
func TestFailedStartThatOwesARestartSaysWhatItWaitsFor(t *testing.T) {
	old := routesPending
	t.Cleanup(func() { routesPending = old })

	routesPending = func() bool { return true }
	m := startFixture(t, "omlx", "omlx/qwen3.8", "qwen3.8")
	stubStartModel(t, func(_ int, ctx context.Context, _ lifecycle.Target, _ lifecycle.Options) error {
		<-ctx.Done()
		return ctx.Err()
	})
	got, _ := enterStartRow(t, m, "omlx/qwen3.8")
	got, _ = updateMsg(got, tea.KeyMsg{Type: tea.KeyEsc})
	msg := recvStart(t, got)
	if stage, ok := msg.(startStageMsg); !ok || stage.stage != lifecycle.StageRouting {
		t.Fatalf("after a cancelled start with a restart pending the flow sent %#v, want the routing stage", msg)
	}
	got, _ = updateMsg(got, msg)
	if view := got.View(); !strings.Contains(view, "waiting for the LiteLLM proxy restart") || strings.Contains(view, "waiting for the server to stop") {
		t.Errorf("the cancelling screen does not say it waits for the proxy:\n%s", view)
	}
	if _, ok := recvStart(t, got).(startDoneMsg); !ok {
		t.Error("the result did not follow the routing stage")
	}

	routesPending = func() bool { return false }
	m = startFixture(t, "omlx", "omlx/qwen3.8", "qwen3.8")
	stubStartModel(t, printingStart(errors.New("boom"), ""))
	got, _ = enterStartRow(t, m, "omlx/qwen3.8")
	if msg := recvStart(t, got); fmt.Sprintf("%T", msg) != "tui.startDoneMsg" {
		t.Errorf("a failed start with no restart pending sent %#v first, want its result", msg)
	}
}

// TestQuittingDuringAStartPrintsWhatItHadPrinted pins the one way out that
// never reads the start's result: ctrl+c twice on the start screen quits wt
// while the engine is still tearing down. What the engine had printed is
// printed once the alt screen is gone, and what it prints after that goes
// straight to the terminal. Both used to be lost.
func TestQuittingDuringAStartPrintsWhatItHadPrinted(t *testing.T) {
	stubRouteNotes(t)
	m := startFixture(t, "omlx", "omlx/qwen3.8", "qwen3.8")
	printed, hang, late := make(chan struct{}), make(chan struct{}), make(chan struct{})
	stubStartModel(t, func(_ int, _ context.Context, _ lifecycle.Target, opts lifecycle.Options) error {
		fmt.Fprint(opts.Out, "wt: omlx unloaded omlx/old to make room (not predicted)\n")
		close(printed)
		<-hang
		fmt.Fprint(opts.Out, "wt: LiteLLM route not updated: boom\n")
		close(late)
		return context.Canceled
	})
	got, _ := enterStartRow(t, m, "omlx/qwen3.8")
	<-printed
	got, _ = updateMsg(got, tea.KeyMsg{Type: tea.KeyCtrlC})
	got, cmd := updateMsg(got, tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("the second ctrl+c returned no command, want tea.Quit")
	}
	var term lockedBuffer
	flushRouteNotesAfterRun(got, &term)
	if want := "wt: omlx unloaded omlx/old to make room (not predicted)\n"; term.String() != want {
		t.Errorf("printed at exit = %q, want %q", term.String(), want)
	}
	close(hang)
	<-late
	if want := "wt: omlx unloaded omlx/old to make room (not predicted)\nwt: LiteLLM route not updated: boom\n"; term.String() != want {
		t.Errorf("after the engine's later line = %q, want %q", term.String(), want)
	}
}

// lockedBuffer is a bytes.Buffer two goroutines may use.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestStartOutputKeepsALimitedAmountAndSaysSo verifies the buffer between the
// engine and the screen: it keeps what it is given up to its limit, cuts at a
// whole line and says how much is missing, takes writes from two goroutines,
// and after release passes everything straight through.
func TestStartOutputKeepsALimitedAmountAndSaysSo(t *testing.T) {
	var o startOutput
	fmt.Fprint(&o, "wt: one\n")
	fmt.Fprint(&o, "wt: two\n")
	if got := o.text(); got != "wt: one\nwt: two\n" {
		t.Errorf("text = %q", got)
	}

	var big startOutput
	line := "wt: " + strings.Repeat("x", 95) + "\n" // 100 bytes
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				fmt.Fprint(&big, line)
				_ = big.text()
			}
		}()
	}
	wg.Wait()
	got := big.text()
	kept := startOutputMax / 100 * 100
	wantTail := fmt.Sprintf("wt: … %d more bytes of start output not kept\n", 200*100-kept)
	if !strings.HasSuffix(got, wantTail) || len(got) != kept+len(wantTail) {
		t.Errorf("a %d-byte output kept %d bytes ending %q, want %d bytes of whole lines and %q", 200*100, len(got), got[max(len(got)-70, 0):], kept, wantTail)
	}
	if strings.Count(got, line) != kept/100 {
		t.Errorf("the kept part holds %d whole lines, want %d", strings.Count(got, line), kept/100)
	}

	var term bytes.Buffer
	o.release(&term)
	fmt.Fprint(&o, "wt: three\n")
	if term.String() != "wt: one\nwt: two\nwt: three\n" || o.text() != "" {
		t.Errorf("after release the terminal has %q and the buffer %q; want all three lines and nothing", term.String(), o.text())
	}
}
