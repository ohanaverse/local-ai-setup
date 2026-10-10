package tui

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
)

// These tests pin where #343's check sits in #275's start flow: on the
// start's own goroutine, after the wait for the proxy, with its result and
// its output travelling in the start's one result message.

// goneQwen is what the check reports for a server that was stopped during
// the proxy wait, and goneStatus the picker's words for it.
var goneQwen = &lifecycle.StoppedError{Why: "omlx no longer has it loaded"}

const (
	goneStatus = "omlx/qwen3.8 is not running: it started, and was stopped while wt updated the LiteLLM routes (omlx no longer has it loaded)"
	// What the check's route removal prints when config.yaml cannot be
	// written, and what the proxy restart that removal started prints.
	goneRouteLine   = "wt: LiteLLM route not updated: open /scratch/config.yaml: permission denied\n"
	goneRestartLine = "wt: LiteLLM restart failed: exit status 1\n"
)

// stubConfirmStarted swaps the confirmStarted seam for fn until the test ends.
func stubConfirmStarted(t *testing.T, fn func(ctx context.Context, out io.Writer, cfg *config.Config, target lifecycle.Target) error) {
	t.Helper()
	old := confirmStarted
	confirmStarted = fn
	t.Cleanup(func() { confirmStarted = old })
}

// TestStartWhoseServerIsGoneIsAFailedStartWithItsLines pins what the picker
// makes of a start whose server was gone after the proxy wait: a failed
// start like any other (#275), not a launch. The engine's lines, the line
// the check's route removal printed and the warning of the restart that
// removal started are all on the status above the failure, and left for the
// terminal with the failure under them. The check is handed the writer the
// engine was handed, so none of it goes to stderr under the alt screen. And
// the launch-time route check never runs: the table still shows the row as
// started, so that check would write the removed route straight back.
func TestStartWhoseServerIsGoneIsAFailedStartWithItsLines(t *testing.T) {
	stubRouteNotes(t)
	m := startFixture(t, "omlx", "omlx/qwen3.8", "qwen3.8")
	m.agent = "not-a-real-agent" // a launch, if one were attempted, fails observably
	routes := stubEnsureRoute(t)
	const engineLine = "wt: omlx unloaded omlx/old to make room\n"
	var engineOut, confirmOut io.Writer
	stubStartModel(t, func(_ int, _ context.Context, _ lifecycle.Target, opts lifecycle.Options) error {
		engineOut = opts.Out
		fmt.Fprint(opts.Out, engineLine)
		return nil
	})
	// The second wait is the one for the restart the route removal started,
	// which prints until that wait returns. All of this runs on the start's
	// goroutine and is read after its result was received.
	waits := 0
	oldWait := waitPendingRoutes
	waitPendingRoutes = func() {
		if waits++; waits == 2 && confirmOut != nil {
			fmt.Fprint(confirmOut, goneRestartLine)
		}
	}
	t.Cleanup(func() { waitPendingRoutes = oldWait })
	stubConfirmStarted(t, func(_ context.Context, out io.Writer, _ *config.Config, _ lifecycle.Target) error {
		confirmOut = out
		if out != nil {
			fmt.Fprint(out, goneRouteLine)
		}
		return goneQwen
	})

	got, _ := enterStartRow(t, m, "omlx/qwen3.8")
	got, _ = updateMsg(got, recvStart(t, got))
	if confirmOut == nil || confirmOut != engineOut {
		t.Errorf("the check was handed writer %v, want the one the engine was handed (%v)", confirmOut, engineOut)
	}
	if got.phase != phaseModel || got.start != nil {
		t.Errorf("phase = %v, start kept = %v; want the model picker with no start in flight", got.phase, got.start != nil)
	}
	want := "omlx unloaded omlx/old to make room\n" +
		"LiteLLM route not updated: open /scratch/config.yaml: permission denied\n" +
		"LiteLLM restart failed: exit status 1\n" +
		goneStatus
	if got.status != want {
		t.Errorf("status = %q\nwant     %q", got.status, want)
	}
	if want := engineLine + goneRouteLine + goneRestartLine + "wt: " + goneStatus + "\n"; pendingRouteNotes != want {
		t.Errorf("route notes = %q\nwant          %q", pendingRouteNotes, want)
	}
	if strings.Contains(strings.Join(routes.events, " "), "ensure:") {
		t.Errorf("the launch-time route check ran for a row whose server is gone: %v", routes.events)
	}
	if strings.Contains(got.status, "launch failed") {
		t.Errorf("a launch was attempted on a server that is gone: %q", got.status)
	}
}

// TestStartIsConfirmedOffTheUpdateGoroutine pins where the check runs: on
// the start's own goroutine, after the wait for the proxy, before the start
// reports — never in Update. It is a request to the provider's server and,
// when the server is gone, a config.yaml write; made in finishStart, where
// #343 first put it, it would freeze the start screen that #275 had just
// unfrozen. While the check is held the start has not reported and Update
// still answers a tick and a key; once it is released, handling the result
// asks nothing again.
func TestStartIsConfirmedOffTheUpdateGoroutine(t *testing.T) {
	stubRouteNotes(t)
	m := startFixture(t, "omlx", "omlx/qwen3.8", "qwen3.8")
	m.agent = "not-a-real-agent"
	stubStartModel(t, func(_ int, _ context.Context, _ lifecycle.Target, opts lifecycle.Options) error {
		opts.Progress(lifecycle.StageRouting)
		return nil
	})
	var mu sync.Mutex
	var inUpdate, waited bool
	calls := 0
	oldWait := waitPendingRoutes
	waitPendingRoutes = func() {
		mu.Lock()
		defer mu.Unlock()
		waited = true
	}
	t.Cleanup(func() { waitPendingRoutes = oldWait })
	entered, release := make(chan struct{}), make(chan struct{})
	stubConfirmStarted(t, func(context.Context, io.Writer, *config.Config, lifecycle.Target) error {
		mu.Lock()
		calls++
		first, called, after := calls == 1, inUpdate, waited
		mu.Unlock()
		if called {
			t.Error("confirmStarted was called while Update was running")
		}
		if !after {
			t.Error("confirmStarted was called before the wait for the proxy")
		}
		if first {
			close(entered)
			<-release
		}
		return goneQwen
	})
	update := func(m model, msg tea.Msg) model {
		mu.Lock()
		inUpdate = true
		mu.Unlock()
		next, _ := updateMsg(m, msg)
		mu.Lock()
		inUpdate = false
		mu.Unlock()
		return next
	}

	got, _ := enterStartRow(t, m, "omlx/qwen3.8")
	got = update(got, recvStart(t, got)) // the routing stage
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the start's goroutine never asked whether the server is still there")
	}
	select {
	case msg := <-got.start.ch:
		t.Fatalf("the start reported %T while the check was still held", msg)
	case <-time.After(100 * time.Millisecond):
	}
	// Update is not what the check holds up.
	got = update(got, startTickMsg{id: got.start.id})
	got = update(got, tea.KeyMsg{Type: tea.KeyEsc})
	if got.phase != phaseStarting || got.start == nil {
		t.Fatalf("phase = %v, start kept = %v; want the start screen still up while the check is held", got.phase, got.start != nil)
	}
	close(release)
	msg := recvStart(t, got)
	done, ok := msg.(startDoneMsg)
	if !ok || done.err != error(goneQwen) {
		t.Fatalf("the start reported %#v, want a result carrying the check's error", msg)
	}
	got = update(got, msg)
	if got.status != goneStatus {
		t.Errorf("status = %q, want %q", got.status, goneStatus)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Errorf("confirmStarted was called %d times, want once, by the start's goroutine", calls)
	}
}

// TestQuitAtTheRoutingStageWithAGoneServerNeverLaunches is #275's
// TestQuitAtTheRoutingStageNeverLaunches for a start whose server was gone
// after the wait. ctrl+c at the routing stage quits wt; the result that can
// still arrive before the QuitMsg now carries the check's error, and it is
// dropped like any other result of a start the user quit out of: no launch,
// no route check, no status, nothing recorded for the terminal twice. The
// start is kept, so what the engine and the check printed is printed at exit.
func TestQuitAtTheRoutingStageWithAGoneServerNeverLaunches(t *testing.T) {
	stubRouteNotes(t)
	m := startFixture(t, "omlx", "omlx/qwen3.8", "qwen3.8")
	m.agent = "not-a-real-agent"
	routes := stubEnsureRoute(t)
	const engineLine = "wt: omlx unloaded omlx/old to make room\n"
	stubStartModel(t, func(_ int, _ context.Context, _ lifecycle.Target, opts lifecycle.Options) error {
		fmt.Fprint(opts.Out, engineLine)
		opts.Progress(lifecycle.StageRouting)
		return nil
	})
	stubConfirmStarted(t, func(_ context.Context, out io.Writer, _ *config.Config, _ lifecycle.Target) error {
		if out != nil {
			fmt.Fprint(out, goneRouteLine)
		}
		return goneQwen
	})
	got, _ := enterStartRow(t, m, "omlx/qwen3.8")
	got, _ = updateMsg(got, recvStart(t, got))
	quit, cmd := updateMsg(got, tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil || !quit.start.quitting {
		t.Fatalf("ctrl+c at the routing stage: a command = %v, quitting = %v; want tea.Quit and the start marked", cmd != nil, quit.start.quitting)
	}
	msg := recvStart(t, got)
	done, ok := msg.(startDoneMsg)
	if !ok || done.err != error(goneQwen) {
		t.Fatalf("the start sent %#v, want its result carrying the check's error", msg)
	}
	after, cmd := updateMsg(quit, msg)
	if cmd != nil || after.phase != phaseStarting || after.status != "" || after.start == nil {
		t.Errorf("the result of a start the user quit out of: a command = %v, phase = %v, status = %q, start kept = %v; want it dropped", cmd != nil, after.phase, after.status, after.start != nil)
	}
	if strings.Contains(strings.Join(routes.events, " "), "ensure:") {
		t.Errorf("the launch-time route check ran after the quit: %v", routes.events)
	}
	if pendingRouteNotes != "" {
		t.Errorf("route notes = %q: the dropped result recorded its lines, so they would print twice", pendingRouteNotes)
	}
	var term lockedBuffer
	flushRouteNotesAfterRun(after, &term)
	if want := engineLine + goneRouteLine; term.String() != want {
		t.Errorf("printed at exit = %q, want %q", term.String(), want)
	}
}

// TestCancelThatLostToAStartWhoseServerIsGoneSaysSo pins the check under a
// cancel that lost: esc before the routing stage, an engine that started the
// model anyway, and a server that was gone after the wait. The check is
// still made, on a context the cancel has not ended (a probe under a done
// context settles nothing), because the status is about to say "started …".
// What it says instead is that the model is not running — neither that it
// started nor "cancelled", which would name the cancel as what stopped it.
func TestCancelThatLostToAStartWhoseServerIsGoneSaysSo(t *testing.T) {
	stubRouteNotes(t)
	m := startFixture(t, "omlx", "omlx/qwen3.8", "qwen3.8")
	stubStartModel(t, func(_ int, ctx context.Context, _ lifecycle.Target, _ lifecycle.Options) error {
		<-ctx.Done()
		return nil
	})
	var ctxErr error // written by the start's goroutine, read after its result
	stubConfirmStarted(t, func(ctx context.Context, _ io.Writer, _ *config.Config, _ lifecycle.Target) error {
		ctxErr = ctx.Err()
		return goneQwen
	})
	got, _ := enterStartRow(t, m, "omlx/qwen3.8")
	got, _ = updateMsg(got, tea.KeyMsg{Type: tea.KeyEsc})
	got, _ = updateMsg(got, recvStart(t, got))
	if ctxErr != nil {
		t.Errorf("the check's context was done (%v): its probe could settle nothing", ctxErr)
	}
	if got.status != goneStatus || got.phase != phaseModel {
		t.Errorf("status = %q, phase = %v; want %q on the model picker", got.status, got.phase, goneStatus)
	}
}
