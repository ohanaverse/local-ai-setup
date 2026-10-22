package survey

import (
	"strings"
	"testing"
	"time"
)

// TestFormatAfterSurveyShowsDashForEmptyWindow verifies the combo row
// renders "—" for a window with zero answered surveys, while the model row
// (all agents) still shows real data from an event recorded by a different
// agent — this is the "model looks fine, this agent×model combo has no
// data yet" case from the design's illustrative example.
func TestFormatAfterSurveyShowsDashForEmptyWindow(t *testing.T) {
	asOf := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	events := []Event{
		{Agent: "claude", ModelID: "m", Timestamp: asOf.Add(-1 * time.Hour), Worked: boolPtr(true), Speed: intPtr(4), Quality: intPtr(5)},
	}
	out := FormatAfterSurvey(events, "codex", "m", asOf)
	lines := strings.Split(out, "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	if !strings.Contains(lines[0], "✓100%") {
		t.Errorf("model row = %q, want a ✓100%% segment (event is by claude, counted in the all-agents model row)", lines[0])
	}
	if !strings.Contains(lines[1], "—") {
		t.Errorf("combo row = %q, want a — segment (no codex events)", lines[1])
	}
	if !strings.HasPrefix(lines[1], "codex × model") {
		t.Errorf("combo row = %q, want it to start with the agent label", lines[1])
	}
}

// TestFormatAfterSurveyAlignsColumns verifies the two rows' window
// segments are padded to matching column widths: when both rows report
// identical stats (the only event is by the surveyed agent), everything
// after the label must be byte-identical regardless of the two labels'
// different lengths ("model" vs "claude × model").
func TestFormatAfterSurveyAlignsColumns(t *testing.T) {
	asOf := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	events := []Event{
		{Agent: "claude", ModelID: "m", Timestamp: asOf.Add(-1 * time.Hour), Worked: boolPtr(true), Speed: intPtr(4), Quality: intPtr(5)},
	}
	out := FormatAfterSurvey(events, "claude", "m", asOf)
	lines := strings.Split(out, "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	modelBody := lines[0][strings.Index(lines[0], "1d"):]
	comboBody := lines[1][strings.Index(lines[1], "1d"):]
	if modelBody != comboBody {
		t.Fatalf("model body = %q, combo body = %q, want equal (column alignment)", modelBody, comboBody)
	}
}
