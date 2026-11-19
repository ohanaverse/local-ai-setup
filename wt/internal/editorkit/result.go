package editorkit

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tuilayout"
)

// ResultScreen is what an apply left to read: the commands to run by
// hand and the notes (the backup's path), each scrubbed, and the result's
// own text. It is a screen of its own, scrolled with up and down, because
// a recovery command cut off is useless.
type ResultScreen struct {
	Recovery string
	Notes    []string
	Text     string
	Top      int
}

// NewResultScreen is the screen for a result, or nil when it has neither
// notes nor recovery to show.
func NewResultScreen(recovery string, notes []string, text string) *ResultScreen {
	if len(notes) == 0 && recovery == "" {
		return nil
	}
	r := &ResultScreen{Recovery: Scrub(recovery), Text: text}
	for _, n := range notes {
		r.Notes = append(r.Notes, Scrub(n))
	}
	return r
}

// Lines lays the screen out at width: the recovery first (the part that
// must be read), then the notes, then the result's text.
func (r *ResultScreen) Lines(width int, warn, dim lipgloss.Style) []string {
	var out []string
	add := func(s string, style lipgloss.Style) {
		for _, l := range strings.Split(WrapText(s, width), "\n") {
			out = append(out, style.Render(l))
		}
	}
	plain := lipgloss.NewStyle()
	if r.Recovery != "" {
		add("to put things right by hand:", warn)
		add(r.Recovery, plain)
		out = append(out, "")
	}
	for _, n := range r.Notes {
		add("note: "+n, plain)
	}
	if r.Text != "" {
		if len(r.Notes) > 0 || r.Recovery != "" {
			out = append(out, "")
		}
		add(r.Text, dim)
	}
	return out
}

// window is the screen's lines, how many rows show and whether it
// scrolls, under a bar barH rows tall.
func (r *ResultScreen) window(width, height, barH int, warn, dim lipgloss.Style) (lines []string, rows int, scrolls bool) {
	lines = r.Lines(width, warn, dim)
	rows = max(height-barH-1, 1)
	if len(lines) > rows {
		return lines, max(rows-1, 1), true
	}
	return lines, rows, false
}

// Key takes keys while the result screen shows: up and down scroll, esc
// and enter leave it (done); q, ctrl+c and tab work as ever (not handled).
// Every other key is eaten, c and r included: a check or re-read would
// replace the status, and the Recovery commands and notes of a failed apply
// could not be shown again.
func (r *ResultScreen) Key(key string, width, height, barH int, warn, dim lipgloss.Style) (handled, done bool) {
	lines, rows, _ := r.window(width, height, barH, warn, dim)
	switch key {
	case "up":
		r.Top = max(r.Top-1, 0)
	case "down":
		r.Top = min(r.Top+1, max(len(lines)-rows, 0))
	case "esc", "enter":
		return true, true
	case "q", "ctrl+c", "tab":
		return false, false
	}
	return true, false
}

// View draws the result screen under bar; hints gives the hint lines for
// a screen that scrolls or not.
func (r *ResultScreen) View(bar string, hints func(scrolls bool) []string, width, height int, warn, dim lipgloss.Style) string {
	lines, rows, scrolls := r.window(width, height, lipgloss.Height(bar), warn, dim)
	top := min(r.Top, max(len(lines)-rows, 0))
	end := min(top+rows, len(lines))
	shown := append([]string{}, lines[top:end]...)
	if scrolls {
		shown = append(shown, dim.Render(fmt.Sprintf("... lines %d-%d of %d; up/down scroll ...", top+1, end, len(lines))))
	}
	return tuilayout.MessageAlone(bar, strings.Join(shown, "\n"), FitHints(width, hints(scrolls)), width, height, dim)
}
