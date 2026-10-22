package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
)

// aloneHint is the line the picker ends with when a status has the screen to
// itself, as the user reads it.
const aloneHint = "press a key to go back to the models"

// evictingFailure scripts a start that fails after omlx unloaded n models,
// printing what the engine prints for each: the eviction, and a route removal
// that could not be written. Real ids and a real path, so the lines are as
// long as they are in use.
func evictingFailure(n int) func(int, context.Context, lifecycle.Target, lifecycle.Options) error {
	return func(_ int, _ context.Context, _ lifecycle.Target, opts lifecycle.Options) error {
		for i := range n {
			fmt.Fprintf(opts.Out, "wt: omlx unloaded omlx/Qwen3.8-27B-4bit-%d to make room (not predicted)\n", i)
			fmt.Fprint(opts.Out, "wt: LiteLLM route not updated: open /Users/someone/.config/litellm/config.yaml: permission denied\n")
		}
		return errors.New("omlx has no room for it")
	}
}

// TestPickerFitsAStartReportOfAnyLength pins the model picker's fit now that
// its status carries the engine's own lines after a start (#275): two lines
// for every model omlx unloaded, above the failure. At every supported size
// the view fits the terminal, and no line of the status is cut at the edge:
// the part of an engine line that says why is its end, and the 94 columns of
// a route line do not fit an 80-column terminal (#209 was this, for another
// status). While the wrapped status and the table both fit, the status is
// above the table. When they do not — Bubble Tea would drop the view's top
// lines, which are the status — the status has the screen: wrapped to the
// width, its middle cut behind a counting marker if it is still too tall, the
// failure and a hint kept. The next key takes it down and does nothing else,
// and the table that comes back fits.
func TestPickerFitsAStartReportOfAnyLength(t *testing.T) {
	for _, width := range layoutWidths {
		for _, height := range layoutHeights {
			for _, n := range []int{1, 2, 3, 6} {
				name := fmt.Sprintf("%d unloaded at %dx%d", n, width, height)
				stubRouteNotes(t)
				m := resized(t, startFixture(t, "omlx", "omlx/Ornith-1.5-35B-6bit", "Ornith-1.5-35B-6bit"), width, height)
				stubStartModel(t, evictingFailure(n))
				got, _ := enterStartRow(t, m, "omlx/Ornith-1.5-35B-6bit")
				got, cmd := updateMsg(got, recvStart(t, got))
				got = drainCmds(t, got, cmd)
				view := got.View()
				assertFits(t, name, view, width, height)
				if !strings.Contains(view, "failed to start") {
					t.Errorf("%s: the failure is not on screen:\n%s", name, view)
				}
				// The ends of the engine's lines, which are what a cut at the
				// edge loses: whether wt saw the eviction coming, and why the
				// route was not written. One model's report is short enough
				// to be whole at every size; a longer one is whole wherever
				// the terminal is tall enough that nothing is cut from its
				// middle.
				if words := strings.Join(strings.Fields(view), " "); n == 1 || height == 50 {
					for _, end := range []string{"(not predicted)", "permission denied", "omlx has no room for it"} {
						if !strings.Contains(words, end) {
							t.Errorf("%s: %q is not on screen: a line of the status was cut:\n%s", name, end, view)
						}
					}
				}
				if !strings.Contains(view, aloneHint) {
					if !strings.Contains(view, "MODEL") {
						t.Errorf("%s: neither the table nor the hint of a status alone is on screen:\n%s", name, view)
					}
					continue
				}
				if strings.Contains(view, "MODEL") {
					t.Errorf("%s: the status has the screen and the table's header is still drawn:\n%s", name, view)
				}
				cursor := got.models.Index()
				next, cmd := updateMsg(got, tea.KeyMsg{Type: tea.KeyDown})
				if next.status != "" || cmd != nil || next.models.Index() != cursor || next.phase != phaseModel {
					t.Errorf("%s: the key after a status alone left status %q, a command %v, the cursor at %d (was %d): want the status gone and nothing else done", name, next.status, cmd != nil, next.models.Index(), cursor)
				}
				back := next.View()
				assertFits(t, name+", after a key", back, width, height)
				if !strings.Contains(back, "MODEL") || strings.Contains(back, aloneHint) {
					t.Errorf("%s: the table did not come back after a key:\n%s", name, back)
				}
			}
		}
	}
}

// TestStatusAloneLeavesTheOtherScreensTheirKeys verifies the rule is the
// picker's own and no wider: a status that fits is not taken down by a key
// (the launch failure stays while the cursor moves), ctrl+c still quits from
// a status that has the screen, and the list's full help — which at 12 lines
// is allowed to run over, and is closed with the key that opened it — is
// never taken for a status alone.
func TestStatusAloneLeavesTheOtherScreensTheirKeys(t *testing.T) {
	m := modelPickerAt(t, 24)
	m.status = layoutStatus
	m = resized(t, m, 80, 24)
	next, _ := updateMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	if next.status != layoutStatus {
		t.Errorf("a key on a status that fits left status %q, want it kept", next.status)
	}

	tall := modelPickerAt(t, 12)
	tall.status = strings.Repeat("a line of a long report\n", 8) + "failed"
	tall = resized(t, tall, 80, 12)
	if view := tall.View(); !strings.Contains(view, aloneHint) {
		t.Fatalf("fixture: a nine-line status at 12 lines does not have the screen:\n%s", view)
	}
	if _, cmd := updateMsg(tall, tea.KeyMsg{Type: tea.KeyCtrlC}); cmd == nil {
		t.Error("ctrl+c on a status alone returned no command, want tea.Quit")
	}

	// A filter being typed: every key is a character of it, so the status
	// never takes the screen, and the key is not taken for "go back".
	typing, _ := updateMsg(resized(t, modelPickerAt(t, 12), 80, 12), tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	typing.status = tall.status
	if !typing.models.SettingFilter() {
		t.Fatal("fixture: \"/\" did not open the filter")
	}
	if strings.Contains(typing.View(), aloneHint) {
		t.Errorf("a status took the screen while a filter was being typed:\n%s", typing.View())
	}
	typed, _ := updateMsg(typing, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if typed.status != tall.status || typed.models.FilterInput.Value() != "q" {
		t.Errorf("a key typed into the filter: status = %q, filter = %q; want the status kept and the key in the filter", typed.status, typed.models.FilterInput.Value())
	}

	// No status: a terminal too short even for the bare table (5 lines) is
	// drawn over its edge, as it always was, and keeps its keys. There is no
	// status to show alone, and a hint with nothing above it would take
	// every key and move nothing.
	short := resized(t, modelPickerAt(t, 12), 80, 5)
	if strings.Contains(short.View(), aloneHint) {
		t.Errorf("a picker with no status shows the hint of a status alone:\n%s", short.View())
	}
	if moved, _ := updateMsg(short, tea.KeyMsg{Type: tea.KeyDown}); moved.models.Index() == short.models.Index() {
		t.Error("a key on a short picker with no status did not move the cursor")
	}
	// No size yet (a status set before the first size message): every view
	// is taller than a height of zero, which says nothing about the status.
	unsized := modelPickerAt(t, 12)
	unsized.status, unsized.height = layoutStatus, 0
	if kept, _ := updateMsg(unsized, tea.KeyMsg{Type: tea.KeyDown}); kept.status != layoutStatus {
		t.Errorf("a key on a picker with no size yet left status %q, want it kept: the status did not have the screen", kept.status)
	}

	help := modelPickerAt(t, 12)
	help.status = layoutStatus
	help = resized(t, help, 80, 12)
	open, _ := updateMsg(help, helpKey)
	if !open.models.Help.ShowAll || strings.Contains(open.View(), aloneHint) {
		t.Fatalf("the full help at 12 lines with a status: shown = %t, view:\n%s", open.models.Help.ShowAll, open.View())
	}
	closed, _ := updateMsg(open, helpKey)
	if closed.models.Help.ShowAll || closed.status != layoutStatus {
		t.Errorf("closing the help: still shown = %t, status = %q; want it closed and the status kept", closed.models.Help.ShowAll, closed.status)
	}
}
