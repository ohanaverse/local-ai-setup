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
	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
)

// layoutHeights and layoutWidths are the terminal sizes every layout test
// measures: short, the common default and tall; narrow, default and wide.
var (
	layoutHeights = []int{12, 24, 50}
	layoutWidths  = []int{40, 80, 120}
)

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

// rowsConfig is modelTestConfig with as many more cloud models as it takes to
// make the given number of rows.
func rowsConfig(rows int) *config.Config {
	cfg := modelTestConfig()
	for i := len(cfg.Models); i < rows; i++ {
		name := fmt.Sprintf("extra-%02d", i)
		cfg.Models = append(cfg.Models, config.Model{ID: "claude/" + name, ProviderID: "claude", ModelName: name, Family: "extra", Tags: []string{"code"}})
	}
	return cfg
}

// rowsPicker is the model picker with the given number of rows in a terminal
// of the given size: modelTestConfig's running local row and cloud row, plus
// as many more cloud rows as it takes. The row count matters because a list
// that needs more than one page draws a taller pagination line, which is where
// a fit can go wrong by a line. The agent has no driver, so Enter on a launch
// row fails the launch and sets a status in that one Update.
func rowsPicker(t *testing.T, rows, width, height int) model {
	t.Helper()
	tempStateDir(t)
	stubUsageStore(t)
	stubRefcountStore(t)
	stubInventory(t, runningOmlxSnapshot())
	cfg := rowsConfig(rows)
	m := flowEnter(t, model{cfg: cfg, agent: "claude", selectedPath: t.TempDir(), width: width, height: height}, "claude")
	if got := len(m.models.Items()); got != rows {
		t.Fatalf("picker has %d rows, want %d", got, rows)
	}
	m.models.Select(indexOfID(m, "omlx/qwen3.8"))
	m.agent = "not-a-real-agent"
	return resized(t, m, width, height)
}

// modelPickerAt is the two-row model picker in an 80-column terminal of the
// given height.
func modelPickerAt(t *testing.T, height int) model {
	t.Helper()
	m := rowsPicker(t, 2, 80, height)
	m.agent = "claude"
	return resized(t, m, 80, height)
}

// assertFits fails when a view has more lines than the terminal or a line
// wider than it. Bubble Tea drops the TOP lines of a view that is too tall, so
// "too tall" means the header or status line is silently missing on a real
// screen even though the View() string contains it; and it cuts a line that is
// too wide at the right edge, so "too wide" means the last columns are gone.
// Width is display width (lipgloss.Width), as the terminal counts it: escape
// sequences take no columns and wide characters take two.
func assertFits(t *testing.T, name, view string, width, height int) {
	t.Helper()
	if got := lipgloss.Height(view); got > height {
		t.Errorf("%s at %dx%d: view is %d lines, want at most %d", name, width, height, got, height)
	}
	for i, line := range strings.Split(view, "\n") {
		if got := lipgloss.Width(line); got > width {
			t.Errorf("%s at %dx%d: line %d is %d columns, want at most %d: %q", name, width, height, i, got, width, line)
			return
		}
	}
}

// withFilter puts the model picker's filter in the given state: "open" (the
// input focused), "applied" (a query matching rows, accepted) or "nomatch" (a
// query matching nothing, accepted). The applied states are set with
// SetFilterText, which computes the matches at once — typing the query would
// leave them to a command.
func withFilter(t *testing.T, m model, state string) model {
	t.Helper()
	switch state {
	case "open":
		m, _ = updateMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
		if m.models.FilterState() != list.Filtering {
			t.Fatalf("filter state = %v, want the input open", m.models.FilterState())
		}
	case "applied":
		m.models.SetFilterText("o")
		if len(m.models.VisibleItems()) == 0 {
			t.Fatal("filter \"o\" matched nothing, want rows")
		}
	case "nomatch":
		m.models.SetFilterText("zzzz")
		if len(m.models.VisibleItems()) != 0 {
			t.Fatalf("filter \"zzzz\" matched %d rows, want none", len(m.models.VisibleItems()))
		}
	}
	return m
}

// TestModelPickerFitsTheTerminal pins that the model picker never renders more
// lines than the terminal has, nor a line wider than it — with or without a
// status line, with the filter input open, applied, and applied with no match
// — and that its agent/tag header and its status line are in the view. The
// list used to be sized to the window minus two each way while the header,
// footer, padding and status added six to eight lines and four columns, so the
// top of the screen (the header, and every status the picker ever set: start
// errors, "cancelled", a blocked row's reason) was pushed off a real
// terminal and the table's last two columns were cut at its right edge.
//
// One combination cannot show everything: 12 lines with a status. The table
// needs seven lines, the status two, the mode line and hints two, which leaves
// one — not the two the header needs. There the header is the part given up
// (TestModelPickerShortTerminalGivesUpTheHeaderFirst); everywhere else it must
// be present.
func TestModelPickerFitsTheTerminal(t *testing.T) {
	for _, width := range layoutWidths {
		for _, height := range layoutHeights {
			for _, status := range []string{"", layoutStatus} {
				for _, filter := range []string{"none", "open", "applied", "nomatch"} {
					name := fmt.Sprintf("status=%t filter=%s", status != "", filter)
					m := rowsPicker(t, 2, width, height)
					m.agent = "claude"
					m = withFilter(t, m, filter)
					// A status arrives with some message; Update re-fits.
					m.status = status
					m = resized(t, m, width, height)

					view := m.View()
					assertFits(t, name, view, width, height)
					wants := []string{"agent : claude", "tag   : code", "LiteLLM: on"}
					if filter == "nomatch" && !strings.Contains(view, "No items") {
						t.Errorf("%s at %dx%d: view lacks the empty-list message", name, width, height)
					}
					if status != "" && height < 13 {
						wants = wants[2:] // the header is what a too-short terminal gives up
					}
					for _, want := range wants {
						if !strings.Contains(view, want) {
							t.Errorf("%s at %dx%d: view lacks %q", name, width, height, want)
						}
					}
					// The status is 42 columns; a 40-column terminal clips it.
					if status != "" && !strings.Contains(view, status[:30]) {
						t.Errorf("%s at %dx%d: view lacks the status line", name, width, height)
					}
					if got, want := m.models.Width(), width-4; got != want {
						t.Errorf("%s at %dx%d: list width = %d, want %d (the terminal minus the picker's side padding)", name, width, height, got, want)
					}
				}
			}
		}
	}
}

// TestModelPickerFillsTheTerminal pins the other side of the fit: at an
// ordinary height the picker uses every line, so the list is as tall as it can
// be and there is no gap under it. It also pins the arithmetic the other tests
// rely on: six lines of chrome (top and bottom padding, agent, tag, the mode
// line and the key hints), plus two for a status line and its blank; and the
// measured floor of a table list, seven lines.
func TestModelPickerFillsTheTerminal(t *testing.T) {
	for _, height := range []int{24, 50} {
		m := modelPickerAt(t, height)
		if got := lipgloss.Height(m.View()); got != height || m.models.Height() != height-6 {
			t.Errorf("height %d: view is %d lines with a %d-line list, want %d and %d", height, got, m.models.Height(), height, height-6)
		}
		if _, floor := listExtent(m.models, 76); floor != 7 {
			t.Errorf("height %d: table list floor = %d, want 7 (title bar 2, one row, pagination 2, help 2)", height, floor)
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
// else on screen, while the header repeats a choice the user just made. After
// the header go the mode line and key hints. Below
// even that, the table keeps its minimum height — bubbles draws that much
// whatever it is told — and the view is allowed to exceed the terminal, which
// costs the lines at its top.
func TestModelPickerShortTerminalGivesUpTheHeaderFirst(t *testing.T) {
	// 12 lines, no status: only the margin goes (2 header + 8 table + 2 footer).
	m := modelPickerAt(t, 12)
	view := m.View()
	assertFits(t, "no status", view, 80, 12)
	if !strings.Contains(view, "agent : claude") || strings.HasPrefix(view, "\n") || m.models.Height() != 8 {
		t.Errorf("12 lines, no status: header shown = %t, list height = %d; want the header kept, the margin dropped and an 8-line list",
			strings.Contains(view, "agent : claude"), m.models.Height())
	}

	// 12 lines with a status: the header goes too (2 status + 8 table + 2 footer).
	m.status = layoutStatus
	m = resized(t, m, 80, 12)
	view = m.View()
	assertFits(t, "status", view, 80, 12)
	if strings.Contains(view, "agent : claude") || !strings.Contains(view, layoutStatus) || !strings.Contains(view, "[enter] launch or start") {
		t.Errorf("12 lines with a status: view = %q, want the status and hints kept and the header given up", view)
	}

	// 10 lines with a status: the mode line and key hints go as well, which
	// leaves the status and the table (2 + 8).
	m = resized(t, m, 80, 10)
	view = m.View()
	assertFits(t, "status, 10 lines", view, 80, 10)
	if !strings.Contains(view, layoutStatus) || !strings.Contains(view, "omlx/qwen3.8") || strings.Contains(view, "[enter] launch or start") {
		t.Errorf("10 lines with a status: view = %q, want the status and the table, without the key hints", view)
	}

	// 8 lines with a status: not even that fits (2 + 7). The table keeps its
	// floor and the view runs one line over, which costs the top line.
	m = resized(t, m, 80, 8)
	view = m.View()
	if m.models.Height() != 7 || lipgloss.Height(view) != 9 {
		t.Errorf("8 lines: list height = %d, view = %d lines; want the table's floor, 7, under the 2-line status", m.models.Height(), lipgloss.Height(view))
	}
	if !strings.Contains(view, "omlx/qwen3.8") {
		t.Errorf("8 lines: view = %q, want a table row still rendered", view)
	}
}

// TestModelPickerFitsWhenOneUpdateAddsAPage pins the fit for a list sitting on
// a page boundary. bubbles works out how many rows fit from the height of its
// pagination line BEFORE a resize, and that line is one row with one page and
// two with several — so a single resize that tips the list from one page to
// two leaves it a line too tall. In a view that otherwise fits exactly, that
// one line pushed the top of the screen off: the blank margin, then the agent
// header, then the status line. Each case does exactly one Update — a status
// appearing (a launch that fails), or one resize step — and measures View()
// at once, because the overflow lasted until the next message and the picker,
// having no tick, could sit like that until the next key.
func TestModelPickerFitsWhenOneUpdateAddsAPage(t *testing.T) {
	for _, tc := range []struct {
		height, rows int
	}{
		{24, 12}, {24, 13}, {50, 38}, {50, 39}, {16, 4}, {16, 5},
	} {
		name := fmt.Sprintf("status appears, %d rows", tc.rows)
		m := rowsPicker(t, tc.rows, 80, tc.height)
		stub := stubEnsureRoute(t)
		stub.changed = false
		assertFits(t, name+" (before)", m.View(), 80, tc.height)

		next, _ := updateMsg(m, tea.KeyMsg{Type: tea.KeyEnter})

		if next.phase != phaseModel || !strings.Contains(next.status, "launch failed") {
			t.Fatalf("%s: phase = %v status = %q, want the picker with a failed launch", name, next.phase, next.status)
		}
		view := next.View()
		assertFits(t, name, view, 80, tc.height)
		if !strings.Contains(view, "launch failed") {
			t.Errorf("%s at height %d: view lacks the status line", name, tc.height)
		}
	}

	// One resize step that shrinks the terminal across the boundary.
	for _, tc := range []struct {
		from, to, rows int
		status         string
		want           string
	}{
		{14, 13, 3, layoutStatus, layoutStatus},
		{12, 11, 3, "", "agent : not-a-real-agent"},
		{17, 16, 9, "", "agent : not-a-real-agent"},
		{25, 24, 17, "", "agent : not-a-real-agent"},
		{51, 50, 43, "", "agent : not-a-real-agent"},
	} {
		name := fmt.Sprintf("resize %d to %d, %d rows", tc.from, tc.to, tc.rows)
		m := rowsPicker(t, tc.rows, 80, tc.from)
		m.status = tc.status
		m = resized(t, m, 80, tc.from)
		assertFits(t, name+" (before)", m.View(), 80, tc.from)

		next := resized(t, m, 80, tc.to)

		view := next.View()
		assertFits(t, name, view, 80, tc.to)
		if !strings.Contains(view, tc.want) {
			t.Errorf("%s: view lacks %q, the line at the top", name, tc.want)
		}
	}

	// Every single-line shrink and growth across a range of sizes and row
	// counts: wherever the boundary falls, one Update is enough.
	for rows := 2; rows <= 45; rows++ {
		m := rowsPicker(t, rows, 80, 52)
		for height := 51; height >= 9; height-- {
			m = resized(t, m, 80, height)
			assertFits(t, fmt.Sprintf("%d rows shrinking", rows), m.View(), 80, height)
		}
		for height := 10; height <= 52; height++ {
			m = resized(t, m, 80, height)
			assertFits(t, fmt.Sprintf("%d rows growing", rows), m.View(), 80, height)
		}
	}
}

// TestModelPickerFitsWithFullHelpOpen pins that expanding the list's help with
// `?` keeps the picker within the terminal. The expanded help is several lines
// taller, so the table's least height grows with it; a fit that assumed the
// short help's seven lines left the view four lines too tall on a short
// terminal, and the header and status went off the top. Where the expanded help
// leaves no room for the picker's own mode line and key hints, those are given
// up (the help shows the keys); they return when the help is closed.
func TestModelPickerFitsWithFullHelpOpen(t *testing.T) {
	for _, height := range layoutHeights {
		m := rowsPicker(t, 30, 80, height)
		_, short := listExtent(m.models, 76)

		next, _ := updateMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})

		_, full := listExtent(next.models, 76)
		if full <= short {
			t.Fatalf("height %d: list floor with full help = %d, want more than the short help's %d", height, full, short)
		}
		view := next.View()
		assertFits(t, "full help", view, 80, height)
		// The picker's own key hints stay wherever there is room for them
		// under the expanded help; a 12-line terminal has none.
		if got, want := strings.Contains(view, "[enter] launch or start"), height >= full+2; got != want {
			t.Errorf("height %d (help needs %d): key hints shown = %t, want %t", height, full, got, want)
		}

		closed, _ := updateMsg(next, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
		assertFits(t, "help closed again", closed.View(), 80, height)
		if closed.models.Height() != m.models.Height() {
			t.Errorf("height %d: list height after closing the help = %d, want %d back", height, closed.models.Height(), m.models.Height())
		}
	}
}

// helpKey is `?`, which bubbles' list handles itself: it opens or closes the
// full help.
var helpKey = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")}

// tooTall collects the cases of a sweep whose view has more lines than the
// terminal, and reports them as one failure with a few examples: a sweep that
// goes wrong goes wrong in hundreds of cases at once.
type tooTall struct {
	count    int
	examples []string
}

func (o *tooTall) check(name, view string, height int) {
	if got := lipgloss.Height(view); got > height {
		o.count++
		if len(o.examples) < 6 {
			o.examples = append(o.examples, fmt.Sprintf("%s: view is %d lines, want at most %d", name, got, height))
		}
	}
}

func (o *tooTall) report(t *testing.T, what string) {
	t.Helper()
	if o.count > 0 {
		t.Errorf("%s: %d cases taller than the terminal, e.g.\n  %s", what, o.count, strings.Join(o.examples, "\n  "))
	}
}

// TestModelPickerFitsWhenHelpTogglesInOneUpdate pins that `?` — one key, one
// Update — leaves the picker within the terminal at once, for every row count
// and height, with and without a status. bubbles' own `?` handler re-paginates
// for the taller help without refreshing which keys are enabled, and the full
// help is two lines taller once the next/previous-page keys are; so a list
// that `?` tips from one page to several (12 rows in 24 lines) was sized
// against the shorter help and drew two lines over. Those two lines came off
// the top — the blank margin and the `agent :` line — and stayed off until the
// next key. Closing the help is swept the same way. Each case also sizes once
// more and expects nothing to move: a fit that only settles on the following
// message is the bug.
//
// A terminal too short for the expanded help under the sparest layout (the
// help's own lines plus a status) is not counted: there the list keeps its
// least height and the view is over by design (fitList).
func TestModelPickerFitsWhenHelpTogglesInOneUpdate(t *testing.T) {
	// The reviewer's two reproductions, by name, before the sweep.
	for _, tc := range []struct{ rows, height int }{{12, 24}, {11, 24}, {13, 24}, {4, 16}} {
		m := rowsPicker(t, tc.rows, 80, tc.height)
		if m.models.Paginator.TotalPages != 1 {
			t.Fatalf("%d rows at height %d: %d pages before the help opens, want a single page", tc.rows, tc.height, m.models.Paginator.TotalPages)
		}
		open, _ := updateMsg(m, helpKey)
		view := open.View()
		assertFits(t, fmt.Sprintf("%d rows, help opened", tc.rows), view, 80, tc.height)
		if !strings.Contains(view, "agent : not-a-real-agent") {
			t.Errorf("%d rows at height %d: view lacks the agent line with the help open", tc.rows, tc.height)
		}
	}

	var opened, closed tooTall
	unsettled, unsettledExample := 0, ""
	for rows := 2; rows <= 49; rows++ {
		base := rowsPicker(t, rows, 80, 60)
		for _, status := range []string{"", layoutStatus} {
			for height := 12; height <= 60; height++ {
				name := fmt.Sprintf("%d rows, height %d, status=%t", rows, height, status != "")
				m := base
				m.status = status
				m = resized(t, m, 80, height)

				open, _ := updateMsg(m, helpKey)
				if !open.models.Help.ShowAll || open.status != status {
					t.Fatalf("%s: full help shown = %t, status = %q; want the help open and the status kept", name, open.models.Help.ShowAll, open.status)
				}
				room := height
				if status != "" {
					room -= 2
				}
				if _, floor := listExtent(open.models, 76); floor <= room {
					opened.check(name, open.View(), height)
					// Sizing again must change nothing.
					again := resized(t, open, 80, height)
					if got, want := open.models.Paginator.PerPage, again.models.Paginator.PerPage; got != want {
						unsettled++
						if unsettled == 1 {
							unsettledExample = fmt.Sprintf("%s: %d rows per page, %d once sized again", name, got, want)
						}
					}
				}

				shut, _ := updateMsg(open, helpKey)
				closed.check(name, shut.View(), height)
				if shut.models.Help.ShowAll {
					t.Fatalf("%s: the full help did not close", name)
				}
			}
		}
	}
	opened.report(t, "help opened in one Update")
	closed.report(t, "help closed in one Update")
	if unsettled > 0 {
		t.Errorf("help opened in one Update: %d cases changed when sized again (the first fit had not settled), e.g. %s", unsettled, unsettledExample)
	}
}

// TestModelPickerFitsAcrossResizes pins that shrinking, growing, narrowing and
// widening the terminal while on the model picker keeps the view within it and
// the status on screen: the resize handler must size the list for the picker's
// own chrome, not for the bare window.
func TestModelPickerFitsAcrossResizes(t *testing.T) {
	m := modelPickerAt(t, 24)
	m.status = layoutStatus
	for _, size := range [][2]int{{100, 12}, {100, 50}, {40, 16}, {120, 24}, {60, 24}, {80, 24}} {
		width, height := size[0], size[1]
		m = resized(t, m, width, height)
		view := m.View()
		assertFits(t, "after a resize", view, width, height)
		if !strings.Contains(view, layoutStatus[:30]) {
			t.Errorf("after a resize to %dx%d: view lacks the status line", width, height)
		}
		// 12 lines cannot hold the header beside a status; growing back must
		// bring it back.
		if got, want := strings.Contains(view, "agent : claude"), height >= 13; got != want {
			t.Errorf("after a resize to %dx%d: header shown = %t, want %t", width, height, got, want)
		}
		if got := m.models.Width(); got != width-4 {
			t.Errorf("after a resize to %dx%d: list width = %d, want %d", width, height, got, width-4)
		}
	}
}

// TestModelPickerFitsWhenStatusComesAndGoes pins that a status appearing on
// the picker (a start that fails) and then going away each leave the view
// within the terminal: the list gives up two lines for the status and takes
// them back.
func TestModelPickerFitsWhenStatusComesAndGoes(t *testing.T) {
	const height = 24
	m := resized(t, startFixture(t, "ollama", "ollama/gemma4:9b", "gemma4:9b"), 80, height)
	stubStartModel(t, func(int, context.Context, lifecycle.Target, lifecycle.Options) error {
		return errors.New("boom")
	})
	without := m.models.Height()
	assertFits(t, "before the start", m.View(), 80, height)

	starting, _ := enterStartRow(t, m, "ollama/gemma4:9b")
	failed, cmd := updateMsg(starting, recvStart(t, starting))
	failed = drainCmds(t, failed, cmd)
	if failed.phase != phaseModel || failed.status == "" {
		t.Fatalf("phase = %v status = %q, want the picker with the start error", failed.phase, failed.status)
	}
	view := failed.View()
	assertFits(t, "with the start error", view, 80, height)
	if !strings.Contains(view, failed.status) || !strings.Contains(view, "agent : claude") {
		t.Errorf("view lacks the start error or the header: %q", view)
	}
	if failed.models.Height() != without-2 {
		t.Errorf("list height with a status = %d, want %d (two lines given up)", failed.models.Height(), without-2)
	}

	failed.status = ""
	cleared := resized(t, failed, 80, height)
	assertFits(t, "with the status cleared", cleared.View(), 80, height)
	if cleared.models.Height() != without {
		t.Errorf("list height with the status cleared = %d, want %d back", cleared.models.Height(), without)
	}
}

// TestLongStatusStaysWithinTheTerminal pins that a status longer than the
// terminal is wide is cut at the edge, not wrapped: the view stays within both
// the width and the height. A wrapped status would take more lines than the
// fit counted and push the top of the screen off; an uncut one makes the whole
// picker as wide as the status, and the table's columns go off the right edge
// with it.
func TestLongStatusStaysWithinTheTerminal(t *testing.T) {
	long := "launch failed: " + strings.Repeat("a very long reason ", 12)
	for _, width := range layoutWidths {
		m := rowsPicker(t, 2, width, 24)
		m.status = long
		m = resized(t, m, width, 24)
		view := m.View()
		assertFits(t, "model picker, long status", view, width, 24)
		if !strings.Contains(view, "launch failed: a very long") {
			t.Errorf("width %d: view lacks the start of the status", width)
		}
		if got := lipgloss.Height(view); got != 24 {
			t.Errorf("width %d: view is %d lines, want 24 — the status must stay one line", width, got)
		}

		a := buildModelInPhaseAgent(t, singleModelConfig())
		a.selectedPath = "/tmp/" + strings.Repeat("deep/", 40)
		a.status = long
		a = resized(t, a, width, 24)
		assertFits(t, "agent picker, long path and status", a.View(), width, 24)
	}
}

// sizedView is a screen's View() once the terminal has reported its size.
type sizedView func(t *testing.T, width, height int) string

// modelPhase adapts a model fixture to sizedView: the fixture is built, then
// told the terminal's size through Update.
func modelPhase(build func(t *testing.T, width, height int) model) sizedView {
	return func(t *testing.T, width, height int) string {
		return resized(t, build(t, width, height), width, height).View()
	}
}

// TestEveryListPhaseFitsTheTerminal measures each screen that renders a list
// plus lines of its own — and the two progress screens, and the standalone
// picker `wt start` and `wt smoke` use — at each layout width and height, with
// and without a status where the screen shows one. A screen taller than the
// terminal loses its top lines on a real screen, which is where each of them
// puts its header; one wider than it loses its right edge.
func TestEveryListPhaseFitsTheTerminal(t *testing.T) {
	entries := []worktree.EntryGroup{{Kind: worktree.GroupWorktrees, Entries: []worktree.Entry{
		{Type: worktree.TypeCurrent, Branch: "main", Path: "/tmp/repo"},
	}}}
	worktreeList := func(mutate func(*model)) sizedView {
		return modelPhase(func(t *testing.T, width, height int) model {
			m, _ := updateMsg(model{width: width, height: height}, entriesLoadedMsg{groups: entries})
			mutate(&m)
			return m
		})
	}
	agentPicker := func(status string) sizedView {
		return modelPhase(func(t *testing.T, width, height int) model {
			m := buildModelInPhaseAgent(t, singleModelConfig())
			m.selectedPath = "/tmp/repo"
			m.status = status
			return m
		})
	}
	modelPicker := func(status string) sizedView {
		return modelPhase(func(t *testing.T, width, height int) model {
			m := rowsPicker(t, 2, width, height)
			m.status = status
			return m
		})
	}
	phases := []struct {
		name string
		view sizedView
	}{
		{"worktree list", worktreeList(func(*model) {})},
		{"worktree list, status", worktreeList(func(m *model) { m.status = layoutStatus })},
		{"worktree list, reload error", worktreeList(func(m *model) { m.listError = "boom" })},
		{"agent picker", agentPicker("")},
		{"agent picker, status", agentPicker(layoutStatus)},
		{"agent picker, long status", agentPicker(pairingPinReason())},
		{"model picker", modelPicker("")},
		{"model picker, status", modelPicker(layoutStatus)},
		{"ollama warning", modelPhase(func(t *testing.T, width, height int) model {
			m := model{cfg: &config.Config{}, theme: themes.Default, phase: phaseOllamaWarn, width: width, height: height}
			m.ollamaWarnModel = list.New(buildOllamaChoices(), ThemedListDelegate(m.theme), 78, 22)
			m.ollamaWarnModel.Title = "Model not available: gemma4:9b"
			return m
		})},
		{"replace confirm", modelPhase(func(t *testing.T, width, height int) model {
			m := model{cfg: &config.Config{}, theme: themes.Default, phase: phaseReplaceConfirm, width: width, height: height}
			choices := list.New(buildReplaceChoices(), ThemedListDelegate(m.theme), 78, 22)
			choices.Title = "Starting omlx/a will stop omlx/b, which is running"
			m.replace = &replaceState{choices: choices}
			return m
		})},
		{"routing", modelPhase(func(t *testing.T, width, height int) model {
			m := rowsPicker(t, 2, width, height)
			stubEnsureRoute(t)
			stubRouteNotes(t)
			next, _ := updateMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
			if next.phase != phaseRouting {
				t.Fatalf("phase = %v, want phaseRouting", next.phase)
			}
			return next
		})},
		{"starting", modelPhase(func(t *testing.T, width, height int) model {
			m := startFixture(t, "ollama", "ollama/gemma4:9b", "gemma4:9b")
			stubStartModel(t, func(_ int, ctx context.Context, _ lifecycle.Target, _ lifecycle.Options) error {
				<-ctx.Done()
				return ctx.Err()
			})
			next, _ := enterStartRow(t, m, "ollama/gemma4:9b")
			t.Cleanup(next.start.cancel)
			if next.phase != phaseStarting {
				t.Fatalf("phase = %v, want phaseStarting", next.phase)
			}
			return next
		})},
		{"standalone picker", func(t *testing.T, width, height int) string {
			stubUsageStore(t)
			stubRefcountStore(t)
			stubInventory(t, runningOmlxSnapshot())
			cfg := modelTestConfig()
			next, _ := newPickModel(cfg, cfg.Models, themes.Default, false).Update(tea.WindowSizeMsg{Width: width, Height: height})
			return next.View()
		}},
		{"standalone picker, notice", func(t *testing.T, width, height int) string {
			stubUsageStore(t)
			stubRefcountStore(t)
			stubInventory(t, runningOmlxSnapshot())
			cfg := modelTestConfig()
			// The notice is raised the way a user raises it — Enter on a blocked
			// row — so the list is fitted to the frame that carries it: the
			// picker sizes its list in Update, like every other screen here.
			pm := newPickModel(cfg, cfg.Models, themes.Default, false)
			pm.list.SelectedItem().(*modelItem).blocked = "omlx/qwen3.8 cannot be used here: " + strings.Repeat("a long reason ", 8)
			next, _ := pm.Update(tea.WindowSizeMsg{Width: width, Height: height})
			next, _ = next.Update(tea.KeyMsg{Type: tea.KeyEnter})
			if next.(pickModel).notice == "" {
				t.Fatal("Enter on the blocked row raised no notice")
			}
			return next.View()
		}},
	}
	for _, p := range phases {
		for _, width := range layoutWidths {
			for _, height := range layoutHeights {
				view := p.view(t, width, height)
				assertFits(t, p.name, view, width, height)
				t.Logf("%-28s %3dx%2d: %2d lines, %3d columns", p.name, width, height, lipgloss.Height(view), lipgloss.Width(view))
			}
		}
	}
}

// pairingPinReason is the status a refused `-M <pairing>` pin brings back to
// the agent picker: catalog's own text for a registered mlx_lm_server pairing
// that is not running, with ids of the length real pairings have.
func pairingPinReason() string {
	const id = "mlx_lm_server/Qwen3.8-27B-4bit+draft-Qwen3.8-4B-4bit"
	cfg := &config.Config{Models: []config.Model{{
		ID: id, ProviderID: "mlx_lm_server", ModelName: "Qwen3.8-27B-4bit+draft-Qwen3.8-4B-4bit",
		Fetch: config.ModelArtifact{Repo: "mlx-community/Qwen3.8-27B-4bit"}, Draft: config.ModelArtifact{Repo: "mlx-community/Qwen3.8-4B-4bit"},
	}}}
	snap := &localmodels.Snapshot{Entries: []localmodels.Entry{{ProviderID: "mlx_lm_server", ModelID: id, Registered: true}}}
	return catalog.MissingReason(cfg, snap, id)
}

// TestRefusedPairingPinShowsItsStartCommand verifies the agent picker shows a
// refused pin's whole reason, not its first line's worth. `wt -M <pairing>`
// with no -A comes back to this screen with a reason that ends in the llmbench
// command that starts the pairing, its target and its draft; cut at the
// terminal's edge, as it was, the screen named the problem and stopped before
// the command at every width — while the unit tests of the text passed
// (#209). At 80x24 and 120x50 the command and both sides must be on screen,
// wrapped between words; at 40x12 there is no room to wrap it above the list,
// so the line is cut — with an ellipsis, so the cut is visible — and the view
// still fits.
func TestRefusedPairingPinShowsItsStartCommand(t *testing.T) {
	reason := pairingPinReason()
	if !strings.Contains(reason, "llmbench provider isolate --solo mlx_lm_server") {
		t.Fatalf("the fixture's reason = %q, want the pairing's start command", reason)
	}
	at := func(width, height int) string {
		m := buildModelInPhaseAgent(t, singleModelConfig())
		m.selectedPath = "/tmp/repo"
		m.status = reason
		view := resized(t, m, width, height).View()
		assertFits(t, "agent picker, refused pairing pin", view, width, height)
		return view
	}
	for _, size := range [][2]int{{80, 24}, {120, 50}} {
		view := at(size[0], size[1])
		// The wrap puts line breaks between words; read the screen as text.
		text := strings.Join(strings.Fields(view), " ")
		for _, want := range []string{
			"llmbench provider isolate --solo mlx_lm_server",
			"mlx-community/Qwen3.8-27B-4bit --draft mlx-community/Qwen3.8-4B-4bit",
			"status: " + reason,
			"directory: /tmp/repo",
			"claude (agent)",
		} {
			if !strings.Contains(text, want) {
				t.Errorf("%dx%d: the screen does not show %q:\n%s", size[0], size[1], want, view)
			}
		}
		// Between words only: a side broken in two could not be pasted.
		for _, word := range []string{"mlx-community/Qwen3.8-27B-4bit", "mlx-community/Qwen3.8-4B-4bit`", "mlx_lm_server"} {
			if !strings.Contains(view, word) {
				t.Errorf("%dx%d: %q is broken across lines:\n%s", size[0], size[1], word, view)
			}
		}
	}
	small := at(40, 12)
	if !strings.Contains(small, "status: local model") || !strings.Contains(small, "…") {
		t.Errorf("40x12: want the status on one line, ending in an ellipsis where it is cut:\n%s", small)
	}
	if !strings.Contains(small, "claude  (agent)") {
		t.Errorf("40x12: the list is gone:\n%s", small)
	}
}

// TestCutLinesMarksWhatItCut verifies the last-resort status line says when it
// is cut: a line that fits is untouched, one that does not ends in an
// ellipsis inside the width, and each line of a two-line status is judged by
// itself. Without the mark a cut status reads as a complete sentence.
func TestCutLinesMarksWhatItCut(t *testing.T) {
	cases := []struct {
		in    string
		width int
		want  string
	}{
		{"fits", 10, "fits"},
		{"exactly 10", 10, "exactly 10"},
		{"one more than", 12, "one more th…"},
		{"short\nthis line is long", 9, "short\nthis lin…"},
		{"anything", 1, "…"},
		{"no width yet", 0, "no width yet"},
	}
	for _, c := range cases {
		if got := cutLines(c.in, c.width); got != c.want {
			t.Errorf("cutLines(%q, %d) = %q, want %q", c.in, c.width, got, c.want)
		}
	}
}

// TestDrawnFrameIsTheFrameTheListWasSizedFor pins the shortcut View takes.
// fitTo measures the list (listExtent: up to sixteen probe renders) to choose a
// frame and a height; View used to repeat that whole measurement on every
// render — every keystroke of a filter — to arrive at the same frame. It now
// asks drawnFrame for the fullest frame that leaves the list the height it
// already has. That is only right if it is always the frame the measurement
// would choose, so this compares the two across every height, with and without
// a status, on one page of rows and on several.
func TestDrawnFrameIsTheFrameTheListWasSizedFor(t *testing.T) {
	for _, rows := range []int{2, 30} {
		for height := 1; height <= 40; height++ {
			for _, status := range []string{"", layoutStatus} {
				m := rowsPicker(t, rows, 80, height)
				m.status = status
				m = resized(t, m, 80, height)
				frames := m.modelFrames()
				_, floor := listExtent(m.models, max(1, 80-frameSides(frames[0])))
				measured, _ := fitList(height, floor, frames...)
				drawn := drawnFrame(&m.models, height, frames...)
				if got, want := drawn("LIST"), measured("LIST"); got != want {
					t.Errorf("%d rows, height %d, status %q: View draws\n%s\nbut the list was sized for\n%s", rows, height, status, got, want)
				}
			}
		}
	}
}

// TestLoadingStatusIsClearedWhenWorktreesLoad pins a bug the picker's height
// fix exposed: "loading worktrees..." is the model's initial status, shown
// until the worktree list arrives, and nothing cleared it on a successful
// load. While the status line was pushed off the top of the agent and model
// screens nobody saw it; once those screens fit the terminal it sat on every
// one of them. A status some other path set while the load was in flight is
// not the placeholder and must survive.
func TestLoadingStatusIsClearedWhenWorktreesLoad(t *testing.T) {
	groups := []worktree.EntryGroup{{Kind: worktree.GroupWorktrees, Entries: []worktree.Entry{
		{Type: worktree.TypeCurrent, Branch: "main", Path: "/tmp/repo"},
	}}}
	fresh := newRunModel(false, false, "", "", "", "", nil, themes.Theme{}, "", &config.Config{})
	fresh.width, fresh.height = 80, 24
	if !strings.Contains(fresh.View(), "loading worktrees") {
		t.Fatalf("before the load the view is %q, want the loading placeholder", fresh.View())
	}

	loaded, _ := updateMsg(fresh, entriesLoadedMsg{groups: groups})
	if loaded.status != "" {
		t.Errorf("status after a successful load = %q, want it cleared", loaded.status)
	}
	if strings.Contains(loaded.View(), "loading worktrees") {
		t.Errorf("the worktree list still shows the loading placeholder:\n%s", loaded.View())
	}

	other := fresh
	other.status = "config error: boom"
	kept, _ := updateMsg(other, entriesLoadedMsg{groups: groups})
	if kept.status != "config error: boom" {
		t.Errorf("status = %q, want a status that is not the placeholder kept", kept.status)
	}

	failed, _ := updateMsg(fresh, entriesLoadedMsg{err: errors.New("not a git repo")})
	if !strings.Contains(failed.status, "not a git repo") {
		t.Errorf("status after a failed load = %q, want the error", failed.status)
	}
}
