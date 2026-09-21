package tui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// TestPostExitOrder verifies the TUI post-exit order matches the non-TUI
// path (issues #115/#116): release → survey → stop picker → summary →
// after-survey stats → pricing notice. Release must precede the picker or
// the just-used model always counts as in use; the summary, stats and notice
// come last so the interactive prompts do not scroll them away. The picker
// stub writes a marker through os.Stdout so the summary's position *relative
// to the picker* is pinned too: the seam-only `order` slice cannot see it,
// because the summary is printed by real code rather than by a seam, so a
// summary moved above the picker would land inside the picker's alt-screen
// frame with every other assertion still green.
func TestPostExitOrder(t *testing.T) {
	prevSummary, prevSurvey := pendingSummary, pendingSurveyState
	prevRun, prevNotice, prevRelease, prevStop := runSurvey, emitPriceNotice, releaseSession, runStopPhase
	t.Cleanup(func() {
		pendingSummary, pendingSurveyState = prevSummary, prevSurvey
		runSurvey, emitPriceNotice, releaseSession, runStopPhase = prevRun, prevNotice, prevRelease, prevStop
	})

	var order []string
	pendingSummary = "SUMMARY-LINE"
	pendingSurveyState = pendingSurvey{agent: "claude", m: config.Model{ID: "ollama/qwen3.8"}}
	releaseSession = func() { order = append(order, "release") }
	runSurvey = func(string, config.Model) string { order = append(order, "survey"); return "STATS-BLOCK" }
	runStopPhase = func(*config.Config) {
		order = append(order, "picker")
		// Written through the redirected os.Stdout so it lands in the captured
		// output next to the real summary line.
		fmt.Fprint(os.Stdout, "PICKER-MARKER\n")
	}
	emitPriceNotice = func() { order = append(order, "notice") }

	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	printPendingSummaryAndSurvey(&config.Config{})
	w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)

	if got := strings.Join(order, ","); got != "release,survey,picker,notice" {
		t.Errorf("order = %s, want release,survey,picker,notice", got)
	}
	s := string(out)
	if i, j := strings.Index(s, "SUMMARY-LINE"), strings.Index(s, "STATS-BLOCK"); i < 0 || j < 0 || i > j {
		t.Errorf("stdout = %q, want summary before stats", s)
	}
	// Pins picker → summary → stats as one chain: the seam `order` check above
	// and the summary-before-stats check just before this one are each blind to
	// the summary moving ahead of the picker.
	if i, j := strings.Index(s, "PICKER-MARKER"), strings.Index(s, "SUMMARY-LINE"); i < 0 || j < 0 || i > j {
		t.Errorf("stdout = %q, want the picker marker before the summary line", s)
	}
}

// TestPostExitCommandAgentSkipsStopPhase verifies a command agent (shell,
// m.ID == "") launched no model, so the stop picker never runs after it.
func TestPostExitCommandAgentSkipsStopPhase(t *testing.T) {
	prevSummary, prevSurvey := pendingSummary, pendingSurveyState
	prevRun, prevStop := runSurvey, runStopPhase
	t.Cleanup(func() {
		pendingSummary, pendingSurveyState = prevSummary, prevSurvey
		runSurvey, runStopPhase = prevRun, prevStop
	})
	pendingSummary = ""
	pendingSurveyState = pendingSurvey{agent: "shell", m: config.Model{}}
	runSurvey = func(string, config.Model) string { return "" }
	called := false
	runStopPhase = func(*config.Config) { called = true }

	printPendingSummaryAndSurvey(&config.Config{})
	if called {
		t.Error("stop phase ran for a command agent")
	}
}
