package tui

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
)

const testDrain = "the LiteLLM proxy is finishing a request in flight; it restarts when the request ends (up to 3m0s)"

// stubDrain clears the in-progress notice and sets whether a restart is in
// flight, restoring both after the test.
func stubDrain(t *testing.T, pending bool) {
	t.Helper()
	oldPending := routesPending
	routesPending = func() bool { return pending }
	drainLine.Store(nil)
	t.Cleanup(func() { routesPending = oldPending; drainLine.Store(nil) })
}

// TestNoticeBufferShowsTheDrainLive pins where a check's or a slot stage's
// restart notices go: the in-progress one to the waiting screen, never into
// the notes printed after the restart, when it would be stale; the cut-off,
// which stays true, into the notes.
func TestNoticeBufferShowsTheDrainLive(t *testing.T) {
	stubDrain(t, true)
	var b noticeBuffer
	b.RestartNotice(litellm.Notice{Msg: testDrain, InProgress: true})
	if b.Len() != 0 {
		t.Fatalf("notes = %q, want the in-progress notice kept out of them", b.String())
	}
	if got := drainView(); got != testDrain {
		t.Fatalf("drainView = %q, want %q", got, testDrain)
	}
	b.RestartNotice(litellm.Notice{Msg: "cut off"})
	if got := b.String(); got != "wt: cut off\n" {
		t.Fatalf("notes = %q, want the cut-off line", got)
	}
}

// TestDrainViewNeedsARestartInFlight pins that a notice left from a restart
// that has since finished is not shown: the screens would otherwise say a
// proxy is draining when none is.
func TestDrainViewNeedsARestartInFlight(t *testing.T) {
	stubDrain(t, false)
	showDrain(testDrain)
	if got := drainView(); got != "" {
		t.Fatalf("drainView = %q with no restart in flight, want nothing", got)
	}
}

// TestWaitingScreensShowTheDrain pins the screens that wait on a restart: the
// routing screen, the slot-profile screen and the start screen each show the
// in-progress notice under their head line while the restart runs, so a wait
// of up to three minutes is explained while it lasts.
func TestWaitingScreensShowTheDrain(t *testing.T) {
	stubDrain(t, true)
	showDrain(testDrain)
	m := model{width: 200}
	m.routing = &routingState{modelID: "ollama/a:1", began: time.Now()}
	m.slot = &slotFlow{item: &modelItem{}, began: time.Now()}
	m.slot.item.model.ID = "profile/p"
	m.start = &startState{item: &modelItem{}, began: time.Now()}
	m.start.item.model.ID = "ollama/a:1"
	for name, view := range map[string]string{"routing": m.routingView(), "slot": m.slotView(), "starting": m.startingView()} {
		if !strings.Contains(view, testDrain) {
			t.Errorf("%s screen = %q, want the in-progress notice", name, view)
		}
		if !strings.HasSuffix(view, "quit wt") && !strings.HasSuffix(view, "cancel") {
			t.Errorf("%s screen = %q, want the key hint last", name, view)
		}
	}
}

// TestStartOutputTakesRestartNotices pins the start's and the settling
// restart's writer: under the alt screen the in-progress notice goes to the
// screen, and once release has given the output a real terminal it is
// printed there, since no screen is left to show it.
func TestStartOutputTakesRestartNotices(t *testing.T) {
	stubDrain(t, true)
	o := &startOutput{}
	o.RestartNotice(litellm.Notice{Msg: testDrain, InProgress: true})
	if o.text() != "" || drainView() != testDrain {
		t.Fatalf("kept %q, screen %q: want the notice on the screen only", o.text(), drainView())
	}
	o.RestartNotice(litellm.Notice{Msg: "cut off"})
	var term bytes.Buffer
	o.release(&term)
	o.RestartNotice(litellm.Notice{Msg: testDrain, InProgress: true})
	if want := "wt: cut off\nwt: " + testDrain + "\n"; term.String() != want {
		t.Fatalf("terminal = %q, want %q", term.String(), want)
	}
}

// TestSettleOutOverflowNamesTheRestart pins the overflow line of settleOut,
// which collects a background proxy restart's output: it must not call that
// output a start's.
func TestSettleOutOverflowNamesTheRestart(t *testing.T) {
	o := &startOutput{what: settleOutWhat}
	fmt.Fprint(o, strings.Repeat("x", startOutputMax)+"\n")
	if got := o.text(); !strings.HasSuffix(got, "more bytes of LiteLLM proxy restart output not kept\n") {
		t.Fatalf("overflow line = %q", got[max(len(got)-80, 0):])
	}
}
