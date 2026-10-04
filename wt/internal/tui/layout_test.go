package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
)

// layoutHeights are the terminal heights every layout test measures: a short
// terminal, the common default and a tall one.
var layoutHeights = []int{12, 24, 50}

// layoutStatus is a status line as the route check leaves it; any status takes
// the same room.
const layoutStatus = "wt: LiteLLM route for omlx/qwen3.8 updated"

// resized sends m a window size through Update, the way the terminal reports
// it, and returns the settled model.
func resized(t *testing.T, m model, width, height int) model {
	t.Helper()
	next, _ := updateMsg(m, tea.WindowSizeMsg{Width: width, Height: height})
	return next
}

// modelPickerAt is the model picker over modelTestConfig (a running local row
// and a cloud row) in an 80-column terminal of the given height.
func modelPickerAt(t *testing.T, height int) model {
	t.Helper()
	m := launchRowFixture(t, "omlx/qwen3.8")
	m.agent = "claude"
	return resized(t, m, 80, height)
}

// assertFits fails when a view has more lines than the terminal. Bubble Tea
// drops the TOP lines of a view that is too tall, so "too tall" means the
// header or status line at the top is silently missing on a real screen even
// though the View() string contains it.
func assertFits(t *testing.T, name string, m model, height int) string {
	t.Helper()
	view := m.View()
	if got := lipgloss.Height(view); got > height {
		t.Errorf("%s at height %d: view is %d lines, want at most %d", name, height, got, height)
	}
	return view
}

// TestModelPickerFitsTheTerminal pins that the model picker never renders more
// lines than the terminal has — with or without a status line, with the filter
// input open, and with a filter applied — and that its agent/tag header and
// its status line are in the view. The list used to be sized to the window
// minus two while the header, footer, padding and status added six to eight
// lines, so the top of the screen (the header, and every status the picker
// ever set: start errors, "cancelled", resume warnings, the route note) was
// pushed off a real terminal.
//
// One combination cannot show everything: 12 lines with a status. The table
// needs seven lines, the status two, the mode line and hints two, which leaves
// one — not the two the header needs. There the header is the part given up
// (TestModelPickerShortTerminalGivesUpTheHeaderFirst); everywhere else it must
// be present.
func TestModelPickerFitsTheTerminal(t *testing.T) {
	for _, height := range layoutHeights {
		for _, status := range []string{"", layoutStatus} {
			for _, filter := range []string{"none", "open", "applied"} {
				name := fmt.Sprintf("status=%t filter=%s", status != "", filter)
				m := modelPickerAt(t, height)
				switch filter {
				case "open":
					m, _ = updateMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
				case "applied":
					m, _ = updateMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
					m, _ = updateMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("o")})
					m, _ = updateMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
					if m.models.FilterState() != list.FilterApplied {
						t.Fatalf("%s: filter state = %v, want applied", name, m.models.FilterState())
					}
				}
				// A status arrives with some message; any message re-fits.
				m.status = status
				m = resized(t, m, 80, height)

				view := assertFits(t, name, m, height)
				wants := []string{"agent : claude", "tag   : code", "[enter] launch or start", "LiteLLM: on"}
				if status != "" && height < 13 {
					wants = wants[2:] // the header is what a too-short terminal gives up
				}
				for _, want := range wants {
					if !strings.Contains(view, want) {
						t.Errorf("%s at height %d: view lacks %q", name, height, want)
					}
				}
				if status != "" && !strings.Contains(view, status) {
					t.Errorf("%s at height %d: view lacks the status line", name, height)
				}
				if m.models.Height() < minTableListHeight {
					t.Errorf("%s at height %d: list height %d is below the minimum %d", name, height, m.models.Height(), minTableListHeight)
				}
			}
		}
	}
}

// TestModelPickerFillsTheTerminal pins the other side of the fit: at an
// ordinary height the picker uses every line, so the list is as tall as it can
// be and there is no gap under it. It also pins the arithmetic the other tests
// rely on: six lines of chrome (top and bottom padding, agent, tag, the mode
// line and the key hints), plus two for a status line and its blank.
func TestModelPickerFillsTheTerminal(t *testing.T) {
	for _, height := range []int{24, 50} {
		m := modelPickerAt(t, height)
		if got := lipgloss.Height(m.View()); got != height || m.models.Height() != height-6 {
			t.Errorf("height %d: view is %d lines with a %d-line list, want %d and %d", height, got, m.models.Height(), height, height-6)
		}
		m.status = layoutStatus
		m = resized(t, m, 80, height)
		if got := lipgloss.Height(m.View()); got != height || m.models.Height() != height-8 {
			t.Errorf("height %d with a status: view is %d lines with a %d-line list, want %d and %d", height, got, m.models.Height(), height, height-8)
		}
	}
}

// TestModelPickerShortTerminalGivesUpTheHeaderFirst pins what a terminal too
// short for the whole picker loses, and in what order: first the blank margin,
// then the agent/tag header, and never the table, the status line or the key
// hints. The status outranks the header because it is news that exists nowhere
// else on screen, while the header repeats a choice the user just made. Below
// even that, the table keeps its minimum height — a list bubbles cannot draw is
// no picker — and the view is allowed to exceed the terminal, which costs the
// lines at its top.
func TestModelPickerShortTerminalGivesUpTheHeaderFirst(t *testing.T) {
	// 12 lines, no status: only the margin goes (2 header + 8 table + 2 footer).
	m := modelPickerAt(t, 12)
	view := assertFits(t, "no status", m, 12)
	if !strings.Contains(view, "agent : claude") || strings.HasPrefix(view, "\n") || m.models.Height() != 8 {
		t.Errorf("12 lines, no status: header shown = %t, list height = %d; want the header kept, the margin dropped and an 8-line list",
			strings.Contains(view, "agent : claude"), m.models.Height())
	}

	// 12 lines with a status: the header goes too (2 status + 8 table + 2 footer).
	m.status = layoutStatus
	m = resized(t, m, 80, 12)
	view = assertFits(t, "status", m, 12)
	if strings.Contains(view, "agent : claude") || !strings.Contains(view, layoutStatus) || !strings.Contains(view, "[enter] launch or start") {
		t.Errorf("12 lines with a status: view = %q, want the status and hints kept and the header given up", view)
	}

	// 8 lines with a status: nothing fits. The table stays usable.
	m = resized(t, m, 80, 8)
	view = m.View()
	if m.models.Height() != minTableListHeight {
		t.Errorf("8 lines: list height = %d, want the minimum %d", m.models.Height(), minTableListHeight)
	}
	if !strings.Contains(view, "omlx/qwen3.8") || !strings.Contains(view, "[enter] launch or start") {
		t.Errorf("8 lines: view = %q, want a table row and the key hints still rendered", view)
	}
}

// TestModelPickerFitsAcrossResizes pins that shrinking and growing the
// terminal while on the model picker keeps the view within it and the header
// on screen: the resize handler must size the list for the picker's own
// chrome, not for the bare window.
func TestModelPickerFitsAcrossResizes(t *testing.T) {
	m := modelPickerAt(t, 24)
	m.status = layoutStatus
	for _, height := range []int{12, 50, 16, 24} {
		m = resized(t, m, 100, height)
		view := assertFits(t, "after a resize", m, height)
		if !strings.Contains(view, layoutStatus) {
			t.Errorf("after a resize to %d: view lacks the status line", height)
		}
		// 12 lines cannot hold the header beside a status; growing back must
		// bring it back.
		if got, want := strings.Contains(view, "agent : claude"), height >= 13; got != want {
			t.Errorf("after a resize to %d: header shown = %t, want %t", height, got, want)
		}
	}
}

// TestModelPickerFitsWhenStatusComesAndGoes pins that a status appearing on
// the picker (a start that fails) and then going away (esc from the dialog
// the next attempt opens) each leave the view within the terminal: the list
// gives up two lines for the status and takes them back.
func TestModelPickerFitsWhenStatusComesAndGoes(t *testing.T) {
	const height = 24
	m := resized(t, startFixture(t, "ollama", "ollama/gemma4:9b", "gemma4:9b"), 80, height)
	stubStartModel(t, func(int, context.Context, lifecycle.Target, lifecycle.Options) error {
		return errors.New("boom")
	})
	without := m.models.Height()
	assertFits(t, "before the start", m, height)

	starting, _ := enterStartRow(t, m, "ollama/gemma4:9b")
	failed, cmd := updateMsg(starting, recvStart(t, starting))
	failed = drainCmds(t, failed, cmd)
	if failed.phase != phaseModel || failed.status == "" {
		t.Fatalf("phase = %v status = %q, want the picker with the start error", failed.phase, failed.status)
	}
	view := assertFits(t, "with the start error", failed, height)
	if !strings.Contains(view, failed.status) || !strings.Contains(view, "agent : claude") {
		t.Errorf("view lacks the start error or the header: %q", view)
	}
	if failed.models.Height() != without-2 {
		t.Errorf("list height with a status = %d, want %d (two lines given up)", failed.models.Height(), without-2)
	}

	failed.status = ""
	cleared := resized(t, failed, 80, height)
	assertFits(t, "with the status cleared", cleared, height)
	if cleared.models.Height() != without {
		t.Errorf("list height with the status cleared = %d, want %d back", cleared.models.Height(), without)
	}
}

// TestEveryListPhaseFitsTheTerminal measures each screen that renders a list
// plus lines of its own, at each layout height, with and without a status
// where the screen shows one. A screen taller than the terminal loses its top
// lines on a real screen, which is where each of them puts its header.
func TestEveryListPhaseFitsTheTerminal(t *testing.T) {
	entries := []worktree.EntryGroup{{Kind: worktree.GroupWorktrees, Entries: []worktree.Entry{
		{Type: worktree.TypeCurrent, Branch: "main", Path: "/tmp/repo"},
	}}}
	phases := []struct {
		name  string
		build func(t *testing.T, height int) model
	}{
		{"worktree list", func(t *testing.T, height int) model {
			m, _ := updateMsg(model{width: 80, height: height}, entriesLoadedMsg{groups: entries})
			return m
		}},
		{"worktree list, status", func(t *testing.T, height int) model {
			m, _ := updateMsg(model{width: 80, height: height}, entriesLoadedMsg{groups: entries})
			m.status = layoutStatus
			return m
		}},
		{"worktree list, reload error", func(t *testing.T, height int) model {
			m, _ := updateMsg(model{width: 80, height: height}, entriesLoadedMsg{groups: entries})
			m.listError = "boom"
			return m
		}},
		{"agent picker", func(t *testing.T, height int) model {
			m := buildModelInPhaseAgent(t, singleModelConfig())
			m.selectedPath = "/tmp/repo"
			return m
		}},
		{"agent picker, status", func(t *testing.T, height int) model {
			m := buildModelInPhaseAgent(t, singleModelConfig())
			m.selectedPath = "/tmp/repo"
			m.status = layoutStatus
			return m
		}},
		{"model picker", func(t *testing.T, height int) model { return modelPickerAt(t, height) }},
		{"model picker, status", func(t *testing.T, height int) model {
			m := modelPickerAt(t, height)
			m.status = layoutStatus
			return m
		}},
		{"resume prompt", func(t *testing.T, height int) model {
			m, _ := resumePromptFixture(t, 80, height, "")
			return m
		}},
		{"resume prompt, status", func(t *testing.T, height int) model {
			m, _ := resumePromptFixture(t, 80, height, layoutStatus+"\n")
			return m
		}},
		{"ollama warning", func(t *testing.T, height int) model {
			m := model{cfg: &config.Config{}, theme: themes.Default, phase: phaseOllamaWarn, width: 80, height: height}
			m.ollamaWarnModel = list.New(buildOllamaChoices(), ThemedListDelegate(m.theme), 78, 22)
			m.ollamaWarnModel.Title = "Model not available: gemma4:9b"
			return m
		}},
		{"replace confirm", func(t *testing.T, height int) model {
			m := model{cfg: &config.Config{}, theme: themes.Default, phase: phaseReplaceConfirm, width: 80, height: height}
			choices := list.New(buildReplaceChoices(), ThemedListDelegate(m.theme), 78, 22)
			choices.Title = "Starting omlx/a will stop omlx/b, which is running"
			m.replace = &replaceState{choices: choices}
			return m
		}},
	}
	for _, p := range phases {
		for _, height := range layoutHeights {
			m := resized(t, p.build(t, height), 80, height)
			view := assertFits(t, p.name, m, height)
			t.Logf("%-28s height %2d: %2d lines", p.name, height, lipgloss.Height(view))
		}
	}
}
