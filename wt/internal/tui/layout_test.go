package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tuilayout"
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
	// floor, and the view would run one line over, which costs the top line:
	// the status. So the status has the screen, with the hint that says how
	// to get the table back (statusAlone), and the next key takes it down.
	m = resized(t, m, 80, 8)
	view = m.View()
	assertFits(t, "status, 8 lines", view, 80, 8)
	if m.models.Height() != 7 || !strings.Contains(view, layoutStatus) || !strings.Contains(view, aloneHint) || strings.Contains(view, "RUNNING") {
		t.Errorf("8 lines with a status: list height = %d, view = %q; want the table's floor, 7, and the status alone with its hint", m.models.Height(), view)
	}
	m, _ = updateMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	view = m.View()
	assertFits(t, "8 lines, after a key", view, 80, 8)
	if m.status != "" || !strings.Contains(view, "omlx/qwen3.8") {
		t.Errorf("8 lines, after a key: status = %q, view = %q; want the status gone and the table back", m.status, view)
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

// timedPickerConfig is modelTestConfig with the three models of
// timedTableRows added under the fixture's cloud provider: one priced by
// OpenRouter's schedule, one with ollama's off-peak row, one with a single
// price. The ids are the real ones' length, because the fit depends on it.
func timedPickerConfig() *config.Config {
	cfg := modelTestConfig()
	for _, r := range timedTableRows(time.Time{}) {
		_, name, _ := strings.Cut(r.Model.ID, "/")
		cfg.Models = append(cfg.Models, config.Model{ID: "claude/" + name, ProviderID: "claude", ModelName: name, Family: r.Model.Family, Tags: []string{"code"}, Cost: r.Model.Cost})
	}
	return cfg
}

// TestModelPickerWithTimePricedModelsFitsTheTerminal pins the model picker
// with time-priced models in it at every supported terminal size, built at
// an instant on each side of both models' windows. The COST cell of such a
// model is one column wider than a plain one (the "~" mark) and its heading
// is longer, so this is where a table could come out one column too wide for
// its list, which loses the header or the last column on a real screen. The
// view must fit; where the COST column is drawn it must show the price in
// force at that instant, marked; and at 120 columns it must be drawn, so the
// price is somewhere a user can see it. Both pickers are measured: the
// launcher's and the standalone one `wt start` and `wt smoke` open.
func TestModelPickerWithTimePricedModelsFitsTheTerminal(t *testing.T) {
	old := pickerNow
	t.Cleanup(func() { pickerNow = old })
	for _, c := range []struct {
		name             string
		at               time.Time
		deepseek, ollama string
	}{
		{"deepseek dear, ollama off-peak", deepseekDear, " 1.3200  0.0440  3.9600~", " 0.2500  0.0250  1.0000~"},
		{"deepseek cheap, ollama peak", ollamaPeak, " 0.6600  0.0220  1.9800~", " 0.5000  0.0500  2.0000~"},
	} {
		pickerNow = func() time.Time { return c.at }
		for _, width := range layoutWidths {
			for _, height := range layoutHeights {
				tempStateDir(t)
				stubUsageStore(t)
				stubRefcountStore(t)
				stubInventory(t, runningOmlxSnapshot())
				cfg := timedPickerConfig()
				check := func(picker, view string, cols *tuilayout.Columns) {
					t.Helper()
					assertFits(t, c.name+", "+picker, view, width, height)
					if width == 120 && !cols.Shown(colCost) {
						t.Errorf("%s, %s at %dx%d: the COST column is not drawn", c.name, picker, width, height)
					}
					if !cols.Shown(colCost) {
						return
					}
					// A short terminal shows one page of the rows; the two
					// time-priced models sort first among the priced ones, so
					// at 24 lines and more both are on screen.
					if !strings.Contains(view, "COST (~ varies by time)") {
						t.Errorf("%s, %s at %dx%d: the COST heading does not explain the mark:\n%s", c.name, picker, width, height, view)
					}
					if height >= 24 && (!strings.Contains(view, c.deepseek) || !strings.Contains(view, c.ollama)) {
						t.Errorf("%s, %s at %dx%d: the view lacks %q or %q:\n%s", c.name, picker, width, height, c.deepseek, c.ollama, view)
					}
				}

				m := flowEnter(t, model{cfg: cfg, agent: "claude", selectedPath: t.TempDir(), width: width, height: height}, "claude")
				m = resized(t, m, width, height)
				check("the launcher's picker", m.View(), m.models.Items()[0].(*modelItem).cols)

				pm := newPickModel(cfg, cfg.Models, themes.Default, false)
				next, _ := pm.Update(tea.WindowSizeMsg{Width: width, Height: height})
				standalone := next.(pickModel)
				check("the standalone picker", standalone.View(), standalone.list.Items()[0].(*modelItem).cols)
			}
		}
	}
}

// TestAnOpenPickerKeepsThePricesItOpenedWith pins when the picker reads its
// clock: once, when the table is built, and not again while the picker is
// open. The cost sort is also the default selection, so a table that priced
// itself again on a resize or a cursor move would reorder its rows under the
// cursor at a window boundary, between the user reading a row and pressing
// Enter. Here the clock moves from deepseek's dear window to its cheap one
// (and from ollama's off-peak hours to its peak) while the picker is
// resized through every width, redrawn and moved in: the clock is read
// once, the rows keep their order, and the COST cells still show the prices
// of the instant the picker opened at.
func TestAnOpenPickerKeepsThePricesItOpenedWith(t *testing.T) {
	reads, now := 0, deepseekDear
	old := pickerNow
	pickerNow = func() time.Time { reads++; return now }
	t.Cleanup(func() { pickerNow = old })
	stubInventory(t, runningOmlxSnapshot())
	m := flowEnter(t, model{cfg: timedPickerConfig(), agent: "claude", selectedPath: t.TempDir(), width: 120, height: 24}, "claude")
	m = resized(t, m, 120, 24)
	order := strings.Join(itemIDs(m), ",")
	if view := m.View(); reads != 1 || !strings.Contains(view, " 1.3200  0.0440  3.9600~") || !strings.Contains(view, " 0.2500  0.0250  1.0000~") {
		t.Fatalf("on opening: the clock was read %d time(s), want 1, and the view must show deepseek's dear price and ollama's off-peak one:\n%s", reads, view)
	}

	now = ollamaPeak
	for _, size := range [][2]int{{40, 12}, {120, 50}, {80, 24}, {120, 24}} {
		m = resized(t, m, size[0], size[1])
		_ = m.View()
		m, _ = updateMsg(m, tea.KeyMsg{Type: tea.KeyDown})
		_ = m.View()
		m, _ = updateMsg(m, tea.KeyMsg{Type: tea.KeyUp})
		_ = m.View()
	}
	if reads != 1 {
		t.Errorf("after four resizes and eight cursor moves the clock was read %d time(s), want the one reading the table was built with", reads)
	}
	if got := strings.Join(itemIDs(m), ","); got != order {
		t.Errorf("the rows moved while the picker was open:\n%s\nwant the order it opened with:\n%s", got, order)
	}
	if view := m.View(); !strings.Contains(view, " 1.3200  0.0440  3.9600~") || strings.Contains(view, " 0.6600  0.0220  1.9800~") {
		t.Errorf("the open picker shows the new window's price, want the one it opened with:\n%s", view)
	}
}

// tencentCheap is Monday 2026-10-12 17:00 UTC: after 16:00, when the two
// tencent models are at their cheaper level; deepseek is in its cheap window
// and ollama in its peak hours.
var tencentCheap = time.Date(2026, 10, 12, 17, 0, 0, 0, time.UTC)

// modeLinePickerConfig is timedPickerConfig with the two tencent models
// OpenRouter prices by time of day, as `wt cloud-sync` stores them: the
// dearer level flat, the cheaper one a row from 16:00 UTC. Their prices are
// the longest of the real time-priced models', which is what the mode line
// has to fit at 40 columns.
func modeLinePickerConfig(litellm bool) *config.Config {
	cfg := timedPickerConfig()
	evening := []config.CostWindow{{Days: []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}, Start: "16:00", End: "24:00"}}
	for _, m := range []struct {
		name       string
		flat, late [3]float64
	}{
		{"hy3", [3]float64{0.132, 0.033, 0.528}, [3]float64{0.0825, 0.020625, 0.33}},
		{"hy4-preview", [3]float64{0.834, 0.042, 2.501}, [3]float64{0.7506, 0.0378, 2.2509}},
	} {
		cfg.Models = append(cfg.Models, config.Model{ID: "claude/" + m.name, ProviderID: "claude", ModelName: m.name, Family: "hy", Tags: []string{"code"}, Cost: config.ModelCost{
			InputPricePerMillion: f64(m.flat[0]), CachePricePerMillion: f64(m.flat[1]), OutputPricePerMillion: f64(m.flat[2]),
			TimePrices: []config.TimePrice{{Label: "openrouter", Timezone: "UTC", Windows: evening,
				InputPricePerMillion: f64(m.late[0]), CachePricePerMillion: f64(m.late[1]), OutputPricePerMillion: f64(m.late[2])}}}})
	}
	if !litellm {
		cfg.SetLitellmForTest(config.LitellmState{})
	}
	return cfg
}

// TestModelPickerNamesTheHighlightedModelsPrice pins the mode line under the
// table: beside the LiteLLM mode it names the price in force for the
// highlighted model, `cost <input>/<cached>/<output>` per million tokens,
// as `cost~` when that price depends on the time. The COST column is the
// first thing a narrow table gives up after usage and survey, and a table
// with an OpenRouter id in it gives it up at 80 columns, so without this
// line the price a user picks by is on screen only on a wide terminal.
//
// The price is on the line whole or not at all. The footer is clipped at the
// terminal's edge with no sign of the cut, so a price that did not fit used
// to lose its last digits and read as another price (output 0.33 as 0) and
// lose its mark; at 40 columns that was every price under "LiteLLM: off
// (direct)" and two of the three real time-priced models under "LiteLLM:
// on". Now the mode gives way to the price when both do not fit. The line
// follows the cursor, is the mode alone for a row with no per-token price,
// and costs no line.
func TestModelPickerNamesTheHighlightedModelsPrice(t *testing.T) {
	old := pickerNow
	t.Cleanup(func() { pickerNow = old })
	const deepseek, ollama, plain, hy3, hy4 = "claude/deepseek--deepseek-v4-pro-0813", "claude/glm-5.3:cloud", "claude/z-ai--glm-5.2", "claude/hy3", "claude/hy4-preview"
	// fits: the note fits beside "LiteLLM: on" in the 36 columns a 40-column
	// terminal leaves the line.
	type note struct {
		text string
		fits bool
	}
	for _, c := range []struct {
		name string
		at   time.Time
		want map[string]note
	}{
		{"deepseek dear, ollama off-peak, tencent dear", deepseekDear, map[string]note{
			deepseek: {"cost~ 1.32/0.044/3.96", true}, ollama: {"cost~ 0.25/0.025/1", true}, plain: {"cost 0.06/0.059/6", true},
			hy3: {"cost~ 0.132/0.033/0.528", false}, hy4: {"cost~ 0.834/0.042/2.501", false}}},
		{"deepseek cheap, ollama peak, tencent cheap", tencentCheap, map[string]note{
			deepseek: {"cost~ 0.66/0.022/1.98", true}, ollama: {"cost~ 0.5/0.05/2", true}, plain: {"cost 0.06/0.059/6", true},
			hy3: {"cost~ 0.0825/0.020625/0.33", false}, hy4: {"cost~ 0.7506/0.0378/2.2509", false}}},
	} {
		pickerNow = func() time.Time { return c.at }
		for _, litellm := range []bool{true, false} {
			mode := "LiteLLM: off (direct)"
			if litellm {
				mode = "LiteLLM: on"
			}
			for _, size := range [][2]int{{80, 24}, {40, 12}, {120, 50}} {
				width, height := size[0], size[1]
				tempStateDir(t)
				stubUsageStore(t)
				stubRefcountStore(t)
				stubInventory(t, runningOmlxSnapshot())
				m := flowEnter(t, model{cfg: modeLinePickerConfig(litellm), agent: "claude", selectedPath: t.TempDir(), width: width, height: height}, "claude")
				for id, n := range c.want {
					want := mode + "   " + n.text
					if width == 40 && !(litellm && n.fits) {
						want = n.text
					}
					m.models.Select(indexOfID(m, id))
					m = resized(t, m, width, height)
					view := m.View()
					assertFits(t, c.name+", on "+id, view, width, height)
					// Exactly this line and no other with a price on it: the
					// three numbers whole, the mark when the price depends on
					// the time, and nothing after the last number.
					found := false
					for _, line := range strings.Split(view, "\n") {
						switch {
						case strings.TrimSpace(line) == want:
							found = true
						case strings.Contains(line, "cost"):
							t.Errorf("%s, %s at %dx%d, on %s: the line %q names a price and is not %q", c.name, mode, width, height, id, strings.TrimSpace(line), want)
						}
					}
					if !found {
						t.Errorf("%s, %s at %dx%d, on %s: no line reads %q:\n%s", c.name, mode, width, height, id, want, view)
					}
				}
				// A row with no per-token price in force (the fixture's local
				// model, and its native one) leaves the line as it was.
				for _, id := range []string{"omlx/qwen3.8", "claude/opus"} {
					m.models.Select(indexOfID(m, id))
					m = resized(t, m, width, height)
					if view := m.View(); strings.Contains(view, "cost") || !strings.Contains(view, mode) {
						t.Errorf("%s, %s at %dx%d, on %s: the mode line names a price or lacks the mode:\n%s", c.name, mode, width, height, id, view)
					}
				}
			}
		}
	}

	// modeLine, which decides what the line holds: both when both fit, the
	// price alone when it fits alone, and the mode alone otherwise. Never a
	// part of a price.
	for _, c := range []struct {
		mode, cost string
		width      int
		want       string
	}{
		{"LiteLLM: on", "", 36, "LiteLLM: on"},
		{"LiteLLM: on", "cost~ 1.32/0.044/3.96", 36, "LiteLLM: on   cost~ 1.32/0.044/3.96"},
		{"LiteLLM: on", "cost~ 1.32/0.044/3.96", 35, "LiteLLM: on   cost~ 1.32/0.044/3.96"},
		{"LiteLLM: on", "cost~ 1.32/0.044/3.96", 34, "cost~ 1.32/0.044/3.96"},
		{"LiteLLM: on", "cost~ 0.0825/0.020625/0.33", 36, "cost~ 0.0825/0.020625/0.33"},
		{"LiteLLM: off (direct)", "cost 0.06/0.059/6", 36, "cost 0.06/0.059/6"},
		// Under "LiteLLM: off (direct)" the 36 columns of a 40-column
		// terminal hold a price of 12 characters beside the mode, and no
		// longer one: the mode gives way from 13.
		{"LiteLLM: off (direct)", "cost 4/0.4/8", 36, "LiteLLM: off (direct)   cost 4/0.4/8"},
		{"LiteLLM: off (direct)", "cost 4/0.45/8", 36, "cost 4/0.45/8"},
		{"LiteLLM: off (direct)", "cost~ 0.66/0.022/1.98", 76, "LiteLLM: off (direct)   cost~ 0.66/0.022/1.98"},
		{"LiteLLM: on", "cost~ 0.000123456/0.000123456/0.000123456", 36, "LiteLLM: on"},
		{"LiteLLM: on", "cost~ 0.000123456/0.000123456/0.000123456", 41, "cost~ 0.000123456/0.000123456/0.000123456"},
	} {
		if got := modeLine(c.mode, c.cost, c.width); got != c.want {
			t.Errorf("modeLine(%q, %q, %d) = %q, want %q", c.mode, c.cost, c.width, got, c.want)
		}
	}

	// costNote, which the price is made from: every price that is missing is
	// a dash, a model with no price at all has no note, and neither has a
	// discovered row.
	row := timedTableRows(ollamaPeak)[0]
	row.Model.Cost.CachePricePerMillion, row.Model.Cost.TimePrices[0].CachePricePerMillion = nil, nil
	row = row.pricedAt(ollamaPeak)
	if got := costNote(row); got != "cost~ 0.66/-/1.98" {
		t.Errorf("costNote with no cached-input price = %q", got)
	}
	row.Discovered = true
	if got := costNote(row); got != "" {
		t.Errorf("costNote of a discovered row = %q, want none", got)
	}
	if got := costNote(tableRow{Row: catalog.Row{Model: config.Model{ID: "x", Cost: config.ModelCost{SubscriptionPrice: f64(20)}}}}); got != "" {
		t.Errorf("costNote of a subscription-only model = %q, want none", got)
	}
}
