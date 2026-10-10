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
// gives the engine somewhere to print, and what the engine prints there is
// what the picker shows. Without Options.Out every line of a start went to
// stderr under the alt screen.
func TestStartHandsTheEngineAWriter(t *testing.T) {
	stubRouteNotes(t)
	m := startFixture(t, "omlx", "omlx/qwen3.8", "qwen3.8")
	calls := stubStartModel(t, printingStart(errors.New("boom"), "wt: LiteLLM route not updated: boom\n"))
	got, _ := enterStartRow(t, m, "omlx/qwen3.8")
	got, _ = updateMsg(got, recvStart(t, got))
	if calls.len() != 1 {
		t.Fatalf("the engine was started %d times; want once", calls.len())
	}
	if want := "LiteLLM route not updated: boom\n" + lifecycle.StartErrorMessage("omlx/qwen3.8", errors.New("boom")); got.status != want {
		t.Errorf("status = %q\nwant     %q", got.status, want)
	}
	if view := got.View(); !strings.Contains(view, "LiteLLM route not updated: boom") {
		t.Errorf("the line the engine printed is not on the picker:\n%s", view)
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
	stubProxyWait(t, func() {
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
	})

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
	stubProxyWait(t, func() { close(entered); <-release })

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
// no restart behind goes straight back to the picker, with no such screen.
func TestFailedStartThatOwesARestartSaysWhatItWaitsFor(t *testing.T) {
	stubRouteNotes(t)
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
	got, _ = updateMsg(got, recvStart(t, got))
	if view := got.View(); !strings.Contains(view, "waiting for the LiteLLM proxy restart") || strings.Contains(view, "waiting for the server to stop") {
		t.Errorf("the cancelling screen does not say it waits for the proxy:\n%s", view)
	}
	got, _ = updateMsg(got, recvStart(t, got))
	if got.phase != phaseModel || got.status != "cancelled" {
		t.Errorf("after the wait: phase = %v, status = %q; want the picker and %q", got.phase, got.status, "cancelled")
	}

	// Failed, not cancelled: the same wait, on the screen of a start.
	m = startFixture(t, "omlx", "omlx/qwen3.8", "qwen3.8")
	stubStartModel(t, printingStart(errors.New("boom"), ""))
	got, _ = enterStartRow(t, m, "omlx/qwen3.8")
	got, _ = updateMsg(got, recvStart(t, got))
	if view := got.View(); got.phase != phaseStarting || !strings.Contains(view, "Starting omlx/qwen3.8 — updating LiteLLM routes") || strings.Contains(view, "cancel") || !strings.Contains(view, "[ctrl+c] quit wt") {
		t.Fatalf("a failed start with a restart pending must say what it waits for and offer no cancel (phase %v):\n%s", got.phase, view)
	}
	got, _ = updateMsg(got, recvStart(t, got))
	if want := lifecycle.StartErrorMessage("omlx/qwen3.8", errors.New("boom")); got.phase != phaseModel || got.status != want {
		t.Errorf("after the wait: phase = %v, status = %q; want the picker and %q", got.phase, got.status, want)
	}

	routesPending = func() bool { return false }
	m = startFixture(t, "omlx", "omlx/qwen3.8", "qwen3.8")
	stubStartModel(t, printingStart(errors.New("boom"), ""))
	got, _ = enterStartRow(t, m, "omlx/qwen3.8")
	got, _ = updateMsg(got, recvStart(t, got))
	if want := lifecycle.StartErrorMessage("omlx/qwen3.8", errors.New("boom")); got.phase != phaseModel || got.status != want {
		t.Errorf("a failed start with no restart pending: phase = %v, status = %q; want the picker at once, with %q", got.phase, got.status, want)
	}
}

// TestCancelThatLostSaysItWaitsForTheProxy pins the stage the flow reports
// itself whenever a restart is in flight, after a start that succeeded too.
// The engine reports its stages through a context the cancel has ended, so
// its own routing stage can be lost; the cancelling screen then read "waiting
// for the server to stop" for the 10 to 20 seconds of a restart, over a model
// that had loaded.
func TestCancelThatLostSaysItWaitsForTheProxy(t *testing.T) {
	stubRouteNotes(t)
	old := routesPending
	routesPending = func() bool { return true }
	t.Cleanup(func() { routesPending = old })
	m := startFixture(t, "omlx", "omlx/qwen3.8", "qwen3.8")
	stubStartModel(t, func(_ int, ctx context.Context, _ lifecycle.Target, _ lifecycle.Options) error {
		<-ctx.Done()
		return nil
	})
	got, _ := enterStartRow(t, m, "omlx/qwen3.8")
	got, _ = updateMsg(got, tea.KeyMsg{Type: tea.KeyEsc})
	got, _ = updateMsg(got, recvStart(t, got))
	if view := got.View(); got.phase != phaseStarting || !strings.Contains(view, "waiting for the LiteLLM proxy restart") {
		t.Fatalf("the cancelling screen does not say it waits for the proxy (phase %v):\n%s", got.phase, view)
	}
	got, _ = updateMsg(got, recvStart(t, got))
	if want := "started omlx/qwen3.8; launch cancelled"; got.status != want {
		t.Errorf("status = %q, want %q", got.status, want)
	}
}

// TestQuitAtTheRoutingStageNeverLaunches pins the gap between ctrl+c at the
// routing stage and wt's exit. tea.Quit is a command, so the start's result
// can be delivered before the QuitMsg; handled, it would run the launch-time
// route check (a config.yaml write, a proxy restart), record rotation and
// refcount and issue the agent's command, for a wt the user had just quit.
// The result is dropped instead, the model the key was given is left as it
// was, and what the engine printed is still printed at exit.
func TestQuitAtTheRoutingStageNeverLaunches(t *testing.T) {
	stubRouteNotes(t)
	m := startFixture(t, "omlx", "omlx/qwen3.8", "qwen3.8")
	m.agent = "not-a-real-agent"
	routes := stubEnsureRoute(t)
	stubStartModel(t, func(_ int, _ context.Context, _ lifecycle.Target, opts lifecycle.Options) error {
		fmt.Fprint(opts.Out, "wt: omlx unloaded omlx/old to make room\n")
		opts.Progress(lifecycle.StageRouting)
		return nil
	})
	got, _ := enterStartRow(t, m, "omlx/qwen3.8")
	got, _ = updateMsg(got, recvStart(t, got))
	quit, cmd := updateMsg(got, tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("ctrl+c at the routing stage returned no command, want tea.Quit")
	}
	if got.start.quitting {
		t.Error("the key changed the model it was given: Update must return a new one")
	}
	done := recvStart(t, got)
	if _, ok := done.(startDoneMsg); !ok {
		t.Fatalf("the start sent %#v, want its result", done)
	}
	after, cmd := updateMsg(quit, done)
	if cmd != nil || after.phase != phaseStarting || after.status != "" || after.start == nil {
		t.Errorf("the result of a start the user quit out of: a command = %v, phase = %v, status = %q, start kept = %v; want it dropped", cmd != nil, after.phase, after.status, after.start != nil)
	}
	if strings.Contains(strings.Join(routes.events, " "), "ensure:") {
		t.Errorf("the launch-time route check ran after the quit: %v", routes.events)
	}
	var term lockedBuffer
	flushRouteNotesAfterRun(after, &term)
	if want := "wt: omlx unloaded omlx/old to make room\n"; term.String() != want {
		t.Errorf("printed at exit = %q, want %q", term.String(), want)
	}
}

// TestStartScreenSaysItsStageAtEveryWidth pins what the start screen must
// never lose to the terminal's edge: the stage. It was one clipped line, so
// with a real model id a 40-column terminal showed `Starting <id> — star`,
// and `updating LiteLLM routes` — the only statement of why esc stopped
// working, and of what a failed start is waiting for — was cut off. A line
// that does not fit is broken between the id and the stage, so the stage is
// read as one phrase and no dash is left hanging at the end of the id.
func TestStartScreenSaysItsStageAtEveryWidth(t *testing.T) {
	const id = "omlx/Qwen3.8-27B-Instruct-MLX-6bit"
	old := routesPending
	routesPending = func() bool { return true }
	t.Cleanup(func() { routesPending = old })
	for _, width := range layoutWidths {
		for _, height := range layoutHeights {
			for _, c := range []struct {
				stage      lifecycle.Stage
				cancelling bool
				want       []string
			}{
				{lifecycle.StageWaiting, false, []string{"waiting for the model to load", "[esc] cancel"}},
				{lifecycle.StageRouting, false, []string{"updating LiteLLM routes", "[ctrl+c] quit wt"}},
				{lifecycle.StageRouting, true, []string{"waiting for the LiteLLM proxy restart", "[ctrl+c] quit wt"}},
			} {
				name := fmt.Sprintf("stage %v, cancelling %v at %dx%d", c.stage, c.cancelling, width, height)
				m := resized(t, startFixture(t, "omlx", id, "Qwen3.8-27B-Instruct-MLX-6bit"), width, height)
				hold := make(chan struct{})
				t.Cleanup(func() { close(hold) })
				stubStartModel(t, func(_ int, ctx context.Context, _ lifecycle.Target, opts lifecycle.Options) error {
					if c.cancelling {
						// The flow reports the stage itself: the engine's
						// own goes through a context the cancel has ended.
						<-ctx.Done()
						return ctx.Err()
					}
					opts.Progress(c.stage)
					<-hold
					return nil
				})
				got, _ := enterStartRow(t, m, id)
				if c.cancelling {
					got, _ = updateMsg(got, tea.KeyMsg{Type: tea.KeyEsc})
				}
				got, _ = updateMsg(got, recvStart(t, got))
				view := got.View()
				assertFits(t, name, view, width, height)
				for _, want := range append(c.want, "Qwen3.8-27B-Instruct-MLX-6bit") {
					if !strings.Contains(view, want) {
						t.Errorf("%s: %q is not on screen:\n%s", name, want, view)
					}
				}
				for _, line := range strings.Split(view, "\n") {
					if strings.HasSuffix(strings.TrimRight(line, " "), "—") {
						t.Errorf("%s: a line ends in the dash that joins the id and the stage:\n%s", name, view)
					}
				}
			}
		}
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
