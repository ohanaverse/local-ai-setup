package tui

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// TestExitPromptRunsAfterTheStopPickerAndBeforeTheSummaryInTheTUI pins the
// TUI tail's order for a model launch, a command-agent launch and a
// slot-profile launch: release, (survey), stop picker, worktree prompt,
// summary. It must match the non-TUI tails, which the docs promise.
func TestExitPromptRunsAfterTheStopPickerAndBeforeTheSummaryInTheTUI(t *testing.T) {
	cases := []struct {
		name    string
		pending pendingSurvey
		want    string
	}{
		{"model", pendingSurvey{agent: "claude", m: config.Model{ID: "ollama/qwen"}}, "release,survey,picker,offer,notice"},
		{"command agent", pendingSurvey{agent: "shell"}, "release,survey,offer"},
		{"slot profile", pendingSurvey{profile: true}, "release,picker,offer"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ps, psu, rs, rel, rp, of, np := pendingSummary, pendingSurveyState, runSurvey, releaseSession, runStopPhase, offerExitPrompt, emitPriceNotice
			t.Cleanup(func() {
				pendingSummary, pendingSurveyState, runSurvey, releaseSession, runStopPhase, offerExitPrompt, emitPriceNotice = ps, psu, rs, rel, rp, of, np
			})
			var order []string
			pendingSummary = "SUMMARY-LINE"
			pendingSurveyState = c.pending
			releaseSession = func() { order = append(order, "release") }
			runSurvey = func(string, config.Model) string { order = append(order, "survey"); return "" }
			runStopPhase = func(*config.Config) { order = append(order, "picker"); os.Stdout.WriteString("PICKER-MARKER\n") }
			offerExitPrompt = func(*config.Config) { order = append(order, "offer"); os.Stdout.WriteString("OFFER-MARKER\n") }
			emitPriceNotice = func(*config.Config) { order = append(order, "notice") }

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
			s := string(out)

			if got := strings.Join(order, ","); got != c.want {
				t.Errorf("order = %s, want %s", got, c.want)
			}
			p, o, sum := strings.Index(s, "PICKER-MARKER"), strings.Index(s, "OFFER-MARKER"), strings.Index(s, "SUMMARY-LINE")
			if o < 0 || sum < 0 || o > sum || (p >= 0 && p > o) {
				t.Errorf("stdout = %q, want picker, then offer, then the summary", s)
			}
		})
	}
}

// TestExitPromptSkippedWithoutATerminalOrWhenKeptInTheTUI runs the real
// offer seam with exit_prompt = "keep" and with stdin not a terminal (the
// test's stdin): both print nothing, so the TUI tail never asks a script or
// an opted-out user.
func TestExitPromptSkippedWithoutATerminalOrWhenKeptInTheTUI(t *testing.T) {
	for name, cfg := range map[string]*config.Config{
		"keep":        {LaunchTable: &config.LaunchState{ExitPrompt: "keep"}},
		"no terminal": {},
	} {
		t.Run(name, func(t *testing.T) {
			old := os.Stdout
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			os.Stdout = w
			realOfferExitPrompt(cfg)
			w.Close()
			os.Stdout = old
			out, _ := io.ReadAll(r)
			if len(out) != 0 {
				t.Errorf("stdout = %q, want nothing", out)
			}
		})
	}
}
