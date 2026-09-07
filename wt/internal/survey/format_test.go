package survey

import (
	"strings"
	"testing"
	"time"
)

// TestFormatPickerSegmentOmittedWhenNoAnswered verifies FormatPickerSegment
// returns "" for a model with zero answered surveys, so the picker row
// omits the segment entirely rather than showing a meaningless "✓- q- s- n0".
func TestFormatPickerSegmentOmittedWhenNoAnswered(t *testing.T) {
	if got := FormatPickerSegment(Stats{}); got != "" {
		t.Fatalf("FormatPickerSegment(zero) = %q, want empty", got)
	}
}

// TestFormatPickerSegmentChecksWorked pins the exact rendered format
// ("✓92% q4.2 s3.9 n12") end-to-end, so a change to rounding, field order,
// or the prefix scheme in FormatPickerSegment is caught immediately.
func TestFormatPickerSegmentChecksWorked(t *testing.T) {
	s := Stats{Answered: 12, Worked: 11, Failed: 1, RatedQuality: 10, QualitySum: 42, RatedSpeed: 10, SpeedSum: 39}
	got := FormatPickerSegment(s)
	want := "✓92% q4.2 s3.9 n12"
	if got != want {
		t.Fatalf("FormatPickerSegment = %q, want %q", got, want)
	}
}

// TestFormatPickerSegmentWarnsBelowThreshold verifies the ⚠ swap: it only
// triggers when the combo has enough signal (Answered >= 3) AND is below
// 70% worked — either condition alone must not flag it, so a single bad
// run doesn't scare a user off a combo with too little data to judge.
func TestFormatPickerSegmentWarnsBelowThreshold(t *testing.T) {
	belowThresholdEnoughData := Stats{Answered: 3, Worked: 2, Failed: 1} // 66.7% < 70%, Answered >= 3
	if got := FormatPickerSegment(belowThresholdEnoughData); !strings.HasPrefix(got, "⚠") {
		t.Fatalf("FormatPickerSegment = %q, want ⚠ prefix (67%% < 70%% with 3 answered)", got)
	}

	belowThresholdNotEnoughData := Stats{Answered: 2, Worked: 1, Failed: 1} // 50% < 70%, Answered < 3
	if got := FormatPickerSegment(belowThresholdNotEnoughData); !strings.HasPrefix(got, "✓") {
		t.Fatalf("FormatPickerSegment = %q, want ✓ prefix (only 2 answered, too little signal to warn)", got)
	}

	aboveThreshold := Stats{Answered: 10, Worked: 8, Failed: 2} // 80% >= 70%
	if got := FormatPickerSegment(aboveThreshold); !strings.HasPrefix(got, "✓") {
		t.Fatalf("FormatPickerSegment = %q, want ✓ prefix (80%% >= 70%%)", got)
	}
}

// TestFormatPickerSegmentUnratedShowsDash verifies a worked-but-unrated
// combo still renders a segment (Answered > 0), with a dash in place of
// whichever rating was never given.
func TestFormatPickerSegmentUnratedShowsDash(t *testing.T) {
	s := Stats{Answered: 3, Worked: 3}
	got := FormatPickerSegment(s)
	want := "✓100% q- s- n3"
	if got != want {
		t.Fatalf("FormatPickerSegment = %q, want %q", got, want)
	}
}

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
