// Tests for `wt stats`. Verifies window selection, --model/--agent
// filters, and the empty-store message. Seeds survey.jsonl directly
// (rather than through survey.Record) so timestamps can be placed at
// precise offsets from "now" without reaching into the survey package's
// internal now() seam.
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/survey"
)

func boolPtr(b bool) *bool { return &b }
func intPtr(i int) *int    { return &i }

// seedSurveyEvents writes raw JSONL lines directly to <tmp>/agent-wt/survey.jsonl.
func seedSurveyEvents(t *testing.T, tmp string, events []survey.Event) {
	t.Helper()
	dir := filepath.Join(tmp, "agent-wt")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var data []byte
	for _, e := range events {
		line, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, append(line, '\n')...)
	}
	if err := os.WriteFile(filepath.Join(dir, "survey.jsonl"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestStatsCmdEmptyStorePrintsMessage verifies `wt stats` never errors on
// a fresh install (no survey.jsonl yet) — it reports "no survey data"
// instead.
func TestStatsCmdEmptyStorePrintsMessage(t *testing.T) {
	a, _ := newTestApp(t)
	cmd := statsCmd(a)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if strings.TrimSpace(out.String()) != "no survey data" {
		t.Fatalf("output = %q, want \"no survey data\"", out.String())
	}
}

// TestStatsCmdReportsAllAgentAndComboRows verifies the default (30d, no
// filters) report includes both the per-model "(all)" aggregate row and
// per-(agent,model) combo rows.
func TestStatsCmdReportsAllAgentAndComboRows(t *testing.T) {
	a, tmp := newTestApp(t)
	now := time.Now()
	seedSurveyEvents(t, tmp, []survey.Event{
		{Agent: "claude", ModelID: "ollama/gemma4:9b", Timestamp: now.Add(-1 * time.Hour), Worked: boolPtr(true), Speed: intPtr(4), Quality: intPtr(5)},
		{Agent: "codex", ModelID: "ollama/gemma4:9b", Timestamp: now.Add(-1 * time.Hour), Worked: boolPtr(false)},
	})

	cmd := statsCmd(a)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "(all)") {
		t.Errorf("output = %q, want a (all) aggregate row", got)
	}
	if !strings.Contains(got, "claude") || !strings.Contains(got, "codex") {
		t.Errorf("output = %q, want both agent combo rows", got)
	}
	if !strings.Contains(got, "ollama/gemma4:9b") {
		t.Errorf("output = %q, want the model id", got)
	}
}

// TestStatsCmdWindowFilter verifies --window 1d excludes an event outside
// the 1-day window that a wider window would include.
func TestStatsCmdWindowFilter(t *testing.T) {
	a, tmp := newTestApp(t)
	now := time.Now()
	seedSurveyEvents(t, tmp, []survey.Event{
		{Agent: "claude", ModelID: "old-model", Timestamp: now.Add(-10 * 24 * time.Hour), Worked: boolPtr(true)},
	})

	cmd := statsCmd(a)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--window", "1d"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if strings.TrimSpace(out.String()) != "no survey data" {
		t.Errorf("output = %q, want \"no survey data\" (10-day-old event excluded from 1d window)", out.String())
	}

	cmd30 := statsCmd(a)
	var out30 bytes.Buffer
	cmd30.SetOut(&out30)
	cmd30.SetArgs([]string{"--window", "30d"})
	if err := cmd30.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out30.String(), "old-model") {
		t.Errorf("30d output = %q, want old-model included", out30.String())
	}
}

// TestStatsCmdModelAndAgentFilters verifies --model and --agent narrow the
// report to matching rows only.
func TestStatsCmdModelAndAgentFilters(t *testing.T) {
	a, tmp := newTestApp(t)
	now := time.Now()
	seedSurveyEvents(t, tmp, []survey.Event{
		{Agent: "claude", ModelID: "model-a", Timestamp: now.Add(-1 * time.Hour), Worked: boolPtr(true)},
		{Agent: "codex", ModelID: "model-b", Timestamp: now.Add(-1 * time.Hour), Worked: boolPtr(true)},
	})

	cmd := statsCmd(a)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--model", "model-a"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out.String(), "model-a") || strings.Contains(out.String(), "model-b") {
		t.Errorf("output = %q, want only model-a rows", out.String())
	}

	cmd2 := statsCmd(a)
	var out2 bytes.Buffer
	cmd2.SetOut(&out2)
	cmd2.SetArgs([]string{"--agent", "codex"})
	if err := cmd2.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out2.String(), "codex") || strings.Contains(out2.String(), "claude") {
		t.Errorf("output = %q, want only codex rows", out2.String())
	}
}

// TestStatsCmdInvalidWindow verifies an unrecognized --window value
// returns a clear error rather than silently falling back to a default.
func TestStatsCmdInvalidWindow(t *testing.T) {
	a, _ := newTestApp(t)
	cmd := statsCmd(a)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"--window", "5d"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected an error for an invalid --window value")
	}
}

// TestStatsCmdExcludesEmptyRows verifies that rows with no data (no answered,
// and no skipped) are filtered out of the report. "All skipped" rows (Answered == 0,
// Skipped > 0) are kept to show that the agent×model combo was tried but never
// produced data.
func TestStatsCmdExcludesEmptyRows(t *testing.T) {
	a, tmp := newTestApp(t)
	now := time.Now()

	seedSurveyEvents(t, tmp, []survey.Event{
		// Data row: should be kept
		{Agent: "claude", ModelID: "model-data", Timestamp: now.Add(-1 * time.Hour), Worked: boolPtr(true)},
		// All skipped row: should be kept (shows the combo was tried)
		{Agent: "codex", ModelID: "model-skipped", Timestamp: now.Add(-1 * time.Hour), Skipped: true},
		// Empty row (no answered, no skipped): should be excluded
		// This event is outside the 1d window so won't be counted
		{Agent: "copilot", ModelID: "model-empty", Timestamp: now.Add(-48 * time.Hour), Skipped: true},
	})

	cmd := statsCmd(a)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--window", "1d"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "model-data") {
		t.Errorf("output = %q, want model-data included", got)
	}
	if !strings.Contains(got, "model-skipped") {
		t.Errorf("output = %q, want model-skipped included (all skipped)", got)
	}
	if strings.Contains(got, "model-empty") {
		t.Errorf("output = %q, want model-empty excluded (no data)", got)
	}
}

// TestStatsRowsSortsAgentsWithAllFirst verifies that "(all)" aggregate rows
// sort before per-agent combo rows, regardless of how agent names compare
// byte-wise to "(all)". Within each group, rows sort by model id.
// This matters because agent names are user-configured and may sort before
// "(" (e.g., digits, "-", non-ASCII characters).
func TestStatsRowsSortsAgentsWithAllFirst(t *testing.T) {
	_, tmp := newTestApp(t)
	now := time.Now()

	// Seed events for multiple agents and models to test sort order
	seedSurveyEvents(t, tmp, []survey.Event{
		// Agent "alpha" with model-a
		{Agent: "alpha", ModelID: "model-a", Timestamp: now.Add(-1 * time.Hour), Worked: boolPtr(true)},
		// Agent "beta" with model-a
		{Agent: "beta", ModelID: "model-a", Timestamp: now.Add(-1 * time.Hour), Worked: boolPtr(true)},
		// Agent "alpha" with model-b
		{Agent: "alpha", ModelID: "model-b", Timestamp: now.Add(-1 * time.Hour), Worked: boolPtr(true)},
		// Agent "1numeric" (sorts before "(" in ASCII) with model-a
		{Agent: "1numeric", ModelID: "model-a", Timestamp: now.Add(-1 * time.Hour), Worked: boolPtr(true)},
	})

	rows := buildStatsRows(
		survey.NewStore().Events(),
		survey.Window30d,
		now,
		"", // no model filter
		"", // no agent filter
	)

	// Verify we have both aggregate and combo rows
	hasAggregate := false
	hasCombo := false
	for _, r := range rows {
		if r.Agent == statsAllAgents {
			hasAggregate = true
		} else {
			hasCombo = true
		}
	}
	if !hasAggregate {
		t.Fatal("expected aggregate rows")
	}
	if !hasCombo {
		t.Fatal("expected combo rows")
	}

	// Verify all aggregate rows come before all combo rows
	foundCombo := false
	for _, r := range rows {
		if r.Agent == statsAllAgents {
			if foundCombo {
				t.Errorf("aggregate row after combo row: model=%s, agent=%s", r.ModelID, r.Agent)
			}
		} else {
			foundCombo = true
		}
	}

	// Verify aggregate rows are sorted by model id
	var aggregateModelIDs []string
	for _, r := range rows {
		if r.Agent == statsAllAgents {
			aggregateModelIDs = append(aggregateModelIDs, r.ModelID)
		}
	}
	if !sort.StringsAreSorted(aggregateModelIDs) {
		t.Errorf("aggregate rows not sorted by model id: got %v", aggregateModelIDs)
	}

	// Verify combo rows are sorted by agent, then by model id
	type comboKey struct{ Agent, ModelID string }
	var comboKeys []comboKey
	for _, r := range rows {
		if r.Agent != statsAllAgents {
			comboKeys = append(comboKeys, comboKey{r.Agent, r.ModelID})
		}
	}
	for i := 1; i < len(comboKeys); i++ {
		prev, curr := comboKeys[i-1], comboKeys[i]
		if prev.Agent != curr.Agent {
			if prev.Agent > curr.Agent {
				t.Errorf("combo rows not sorted by agent: %s before %s", prev.Agent, curr.Agent)
			}
		} else if prev.ModelID > curr.ModelID {
			t.Errorf("combo rows with same agent not sorted by model id: %s before %s (agent=%s)",
				prev.ModelID, curr.ModelID, prev.Agent)
		}
	}
}
