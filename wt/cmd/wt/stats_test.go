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
// and no calculated averages) are filtered out of the report. "All skipped"
// rows are also excluded.
func TestStatsCmdExcludesEmptyRows(t *testing.T) {
	a, tmp := newTestApp(t)
	now := time.Now()

	seedSurveyEvents(t, tmp, []survey.Event{
		// Data row: should be kept
		{Agent: "claude", ModelID: "model-data", Timestamp: now.Add(-1 * time.Hour), Worked: boolPtr(true)},
		// All skipped row: should be excluded
		{Agent: "codex", ModelID: "model-skipped", Timestamp: now.Add(-1 * time.Hour), Worked: nil},
	})

	cmd := statsCmd(a)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "model-data") {
		t.Errorf("output = %q, want model-data included", got)
	}
	if strings.Contains(got, "model-skipped") {
		t.Errorf("output = %q, want model-skipped excluded (all skipped)", got)
	}
}
