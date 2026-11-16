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
// path (issues #115/#116): end the client session → survey → summary →
// after-survey stats → pricing notice. The session ends first so the server's
// idle grace starts as soon as the agent is gone; the summary, stats and
// notice come last so the survey prompt does not scroll them away. The end
// stub writes a marker through os.Stdout so the summary's position relative
// to it is pinned too: the seam-only `order` slice cannot see the summary.
func TestPostExitOrder(t *testing.T) {
	prevSummary, prevSurvey, prevRun := pendingSummary, pendingSurveyState, runSurvey
	t.Cleanup(func() {
		pendingSummary, pendingSurveyState, runSurvey = prevSummary, prevSurvey, prevRun
	})

	var order []string
	pendingSummary = "SUMMARY-LINE"
	pendingSurveyState = pendingSurvey{agent: "claude", m: config.Model{ID: "ollama/qwen3.8"}}
	runSurvey = func(string, config.Model) string { order = append(order, "survey"); return "STATS-BLOCK" }
	tail := exitTail{
		end: func() {
			order = append(order, "end")
			fmt.Fprint(os.Stdout, "END-MARKER\n")
		},
		priceNote: func() { order = append(order, "notice") },
	}

	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	printPendingSummaryAndSurveyTail(tail)
	w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)

	if got := strings.Join(order, ","); got != "end,survey,notice" {
		t.Errorf("order = %s, want end,survey,notice", got)
	}
	s := string(out)
	if i, j := strings.Index(s, "SUMMARY-LINE"), strings.Index(s, "STATS-BLOCK"); i < 0 || j < 0 || i > j {
		t.Errorf("stdout = %q, want summary before stats", s)
	}
	if i, j := strings.Index(s, "END-MARKER"), strings.Index(s, "SUMMARY-LINE"); i < 0 || j < 0 || i > j {
		t.Errorf("stdout = %q, want the session ended before the summary line", s)
	}
}

// TestProfilePostExitSkipsSurveyAndPriceNotice pins the slot-profile tail
// (printPendingSummaryAndSurveyTail with pendingSurveyState.profile): the
// client session ends and the summary prints, but there is no survey and no
// price notice — both are per model, and a profile session used several. A
// survey here would record answers under "profile/<name>", which is no model.
func TestProfilePostExitSkipsSurveyAndPriceNotice(t *testing.T) {
	prevSummary, prevSurvey, prevRun := pendingSummary, pendingSurveyState, runSurvey
	t.Cleanup(func() {
		pendingSummary, pendingSurveyState, runSurvey = prevSummary, prevSurvey, prevRun
	})
	var order []string
	pendingSummary = "wt: claude · profile/mix · 1s"
	pendingSurveyState = pendingSurvey{agent: "claude", m: config.Model{ID: "profile/mix"}, profile: true}
	runSurvey = func(string, config.Model) string { order = append(order, "survey"); return "" }
	tail := exitTail{
		end:       func() { order = append(order, "end") },
		priceNote: func() { order = append(order, "notice") },
	}
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	printPendingSummaryAndSurveyTail(tail)
	w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	if got := strings.Join(order, ","); got != "end" {
		t.Errorf("order = %s, want only the session's end (no survey, no notice)", got)
	}
	if !strings.Contains(string(out), "profile/mix") {
		t.Errorf("stdout = %q, want the summary line", out)
	}
}
