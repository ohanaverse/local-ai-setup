package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// unloadingStart scripts a start that makes omlx unload ids before it ends
// with err, reporting each the way the engine does: through OnUnloaded. A flow
// that passes no OnUnloaded is told nothing, as the real engine would tell it
// nothing, so its tests fail on what the user sees rather than on a nil call.
func unloadingStart(err error, ids ...string) func(int, context.Context, lifecycle.Target, lifecycle.Options) error {
	return func(_ int, _ context.Context, _ lifecycle.Target, opts lifecycle.Options) error {
		for _, id := range ids {
			if opts.OnUnloaded != nil {
				opts.OnUnloaded(localmodels.Entry{ProviderID: "omlx", ModelID: id})
			}
		}
		return err
	}
}

// TestFailedStartShowsWhatOmlxUnloaded pins #258. omlx can unload a model to
// make room and then fail the load. The engine's "omlx unloaded X" line goes
// to stderr, which the alt screen hides, so the picker came back showing only
// the failure: a model another session was using was gone and nothing said so.
// The status line now names every unloaded model, ahead of the failure.
func TestFailedStartShowsWhatOmlxUnloaded(t *testing.T) {
	stubRouteNotes(t)
	m := startFixture(t, "omlx", "omlx/qwen3.8", "qwen3.8")
	stubStartModel(t, unloadingStart(errors.New("boom"), "omlx/old", "omlx/older"))
	got, _ := enterStartRow(t, m, "omlx/qwen3.8")
	got, _ = updateMsg(got, recvStart(t, got))
	if want := "omlx unloaded omlx/old, omlx/older to make room; failed to start omlx/qwen3.8: boom"; got.status != want {
		t.Errorf("status = %q\nwant     %q", got.status, want)
	}
	if got.phase != phaseModel {
		t.Errorf("phase = %v, want the picker", got.phase)
	}
	if pendingRouteNotes != "" {
		t.Errorf("route notes = %q, want none: the picker's status line already says it", pendingRouteNotes)
	}
}

// TestStartThatUnloadedNothingLeavesTheStatusAlone verifies the note appears
// only when there is something to say: an ordinary failed start keeps exactly
// the status it had, and an ordinary successful one prints nothing extra.
func TestStartThatUnloadedNothingLeavesTheStatusAlone(t *testing.T) {
	stubRouteNotes(t)
	m := startFixture(t, "omlx", "omlx/qwen3.8", "qwen3.8")
	stubStartModel(t, unloadingStart(errors.New("boom")))
	got, _ := enterStartRow(t, m, "omlx/qwen3.8")
	got, _ = updateMsg(got, recvStart(t, got))
	if want := "failed to start omlx/qwen3.8: boom"; got.status != want {
		t.Errorf("status = %q, want %q", got.status, want)
	}

	m = startFixture(t, "omlx", "omlx/qwen3.8", "qwen3.8")
	m.agent = "not-a-real-agent" // the launch after the start fails, observably
	stubStartModel(t, unloadingStart(nil))
	got, _ = enterStartRow(t, m, "omlx/qwen3.8")
	_, _ = updateMsg(got, recvStart(t, got))
	if pendingRouteNotes != "" {
		t.Errorf("route notes = %q, want none", pendingRouteNotes)
	}
}

// TestCancelledStartShowsWhatOmlxUnloaded verifies a start the user cancelled
// still reports what omlx unloaded before the cancel took effect: the load is
// what evicts, so cancelling it does not bring the evicted model back.
func TestCancelledStartShowsWhatOmlxUnloaded(t *testing.T) {
	m := startFixture(t, "omlx", "omlx/qwen3.8", "qwen3.8")
	stubStartModel(t, func(_ int, ctx context.Context, _ lifecycle.Target, opts lifecycle.Options) error {
		<-ctx.Done()
		return unloadingStart(ctx.Err(), "omlx/old")(0, ctx, lifecycle.Target{}, opts)
	})
	got, _ := enterStartRow(t, m, "omlx/qwen3.8")
	got, _ = updateMsg(got, tea.KeyMsg{Type: tea.KeyEsc})
	got, _ = updateMsg(got, recvStart(t, got))
	if want := "omlx unloaded omlx/old to make room; cancelled"; got.status != want {
		t.Errorf("status = %q, want %q", got.status, want)
	}
}

// TestSuccessfulStartPrintsWhatOmlxUnloadedAboveTheAgent verifies the note
// after a start that succeeded. The agent launches at once, so the line joins
// the route notes, which are printed on the real terminal the moment the alt
// screen is released: above the agent's output. When that launch fails the
// picker comes back instead, and the note must be on its status line too,
// ahead of the failure — otherwise the user sees only "launch failed" until wt
// exits. Here the launch is made to fail (an agent no driver knows), so both
// are checked.
func TestSuccessfulStartPrintsWhatOmlxUnloadedAboveTheAgent(t *testing.T) {
	stubRouteNotes(t)
	m := startFixture(t, "omlx", "omlx/qwen3.8", "qwen3.8")
	m.agent = "not-a-real-agent"
	stubStartModel(t, unloadingStart(nil, "omlx/old"))
	got, _ := enterStartRow(t, m, "omlx/qwen3.8")
	got, _ = updateMsg(got, recvStart(t, got))
	if want := "wt: omlx unloaded omlx/old to make room\n"; pendingRouteNotes != want {
		t.Errorf("route notes = %q, want %q", pendingRouteNotes, want)
	}
	if want := "omlx unloaded omlx/old to make room; launch failed: "; !strings.HasPrefix(got.status, want) {
		t.Errorf("status = %q, want it to begin %q", got.status, want)
	}
}

// TestUnloadedNoteSurvivesALongFailureAtEightyColumns verifies the note is
// still on screen when the failure that follows it is longer than the
// terminal. The status line is one line cut at the terminal's width, and
// omlx's own refusal text runs to several hundred characters: with the note
// after it, the one fact the user cannot find anywhere else would be the part
// cut off.
func TestUnloadedNoteSurvivesALongFailureAtEightyColumns(t *testing.T) {
	m := startFixture(t, "omlx", "omlx/qwen3.8", "qwen3.8")
	stubStartModel(t, unloadingStart(errors.New(strings.Repeat("omlx says no. ", 40)), "omlx/old"))
	got, _ := enterStartRow(t, m, "omlx/qwen3.8")
	got, cmd := updateMsg(got, recvStart(t, got))
	got = drainCmds(t, got, cmd)
	view := got.View()
	if !strings.Contains(view, "omlx unloaded omlx/old to make room") {
		t.Errorf("the 80-column picker does not show the note:\n%s", view)
	}
	assertFits(t, "picker after a failed start", view, 80, 24)
}

// TestUnloadedNoteWithRealModelIDsAtEightyColumns pins the price of putting the
// note first. With ids as long as real ones, the note and "failed to start
// <id>: " already fill an 80-column status line, so the reason for the failure
// is what gets cut. That is the accepted trade: the unloaded model is the fact
// found nowhere else, and `wt start <id>` on the command line prints the
// reason in full. What must hold is that the note is whole, the line still
// says the start failed, and nothing overflows.
func TestUnloadedNoteWithRealModelIDsAtEightyColumns(t *testing.T) {
	m := startFixture(t, "omlx", "omlx/Ornith-1.5-35B-6bit", "Ornith-1.5-35B-6bit")
	stubStartModel(t, unloadingStart(errors.New("boom"), "omlx/Qwen3.8-27B-4bit"))
	got, _ := enterStartRow(t, m, "omlx/Ornith-1.5-35B-6bit")
	got, cmd := updateMsg(got, recvStart(t, got))
	got = drainCmds(t, got, cmd)
	view := got.View()
	if !strings.Contains(view, "omlx unloaded omlx/Qwen3.8-27B-4bit to make room; failed to start") {
		t.Errorf("the 80-column picker does not show the note and that the start failed:\n%s", view)
	}
	assertFits(t, "picker after a failed start", view, 80, 24)
}
