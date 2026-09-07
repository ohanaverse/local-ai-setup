package survey

import (
	"testing"
	"time"
)

// TestStatsWorkedPctZeroWhenNoAnswered verifies WorkedPct reports ok=false
// on a zero-value Stats, so callers (the picker segment, wt stats) know to
// omit a percentage rather than divide by zero or show a misleading 0%.
func TestStatsWorkedPctZeroWhenNoAnswered(t *testing.T) {
	var s Stats
	if _, ok := s.WorkedPct(); ok {
		t.Fatal("WorkedPct ok = true, want false for zero Answered")
	}
}

// TestStatsWorkedPct verifies the core worked-percentage formula
// (worked / (worked + failed) * 100), the number every survey-derived
// display ultimately renders.
func TestStatsWorkedPct(t *testing.T) {
	s := Stats{Answered: 4, Worked: 3, Failed: 1}
	pct, ok := s.WorkedPct()
	if !ok {
		t.Fatal("WorkedPct ok = false, want true")
	}
	if pct != 75 {
		t.Fatalf("WorkedPct = %v, want 75", pct)
	}
}

// TestStatsSpeedQualityAvg verifies SpeedAvg/QualityAvg divide their sums
// by their own rated counts independently, since a user can rate speed
// without rating quality (or vice versa) on any given survey.
func TestStatsSpeedQualityAvg(t *testing.T) {
	s := Stats{RatedSpeed: 2, SpeedSum: 7, RatedQuality: 3, QualitySum: 12}
	speed, ok := s.SpeedAvg()
	if !ok || speed != 3.5 {
		t.Fatalf("SpeedAvg = %v, %v, want 3.5, true", speed, ok)
	}
	quality, ok := s.QualityAvg()
	if !ok || quality != 4 {
		t.Fatalf("QualityAvg = %v, %v, want 4, true", quality, ok)
	}
}

// TestStatsSpeedQualityAvgUnrated verifies SpeedAvg/QualityAvg report
// ok=false when no rated events exist, so a worked-but-unrated combo
// renders a dash instead of a fabricated 0.0 rating.
func TestStatsSpeedQualityAvgUnrated(t *testing.T) {
	var s Stats
	if _, ok := s.SpeedAvg(); ok {
		t.Fatal("SpeedAvg ok = true, want false when no rated events")
	}
	if _, ok := s.QualityAvg(); ok {
		t.Fatal("QualityAvg ok = true, want false when no rated events")
	}
}

// TestModelStatsExcludesSkipsFromDenominator verifies the design's core
// semantics: worked% = worked / (worked + failed) over answered surveys
// only. A skip is recorded (visible via Skipped) but must not inflate or
// deflate the denominator.
func TestModelStatsExcludesSkipsFromDenominator(t *testing.T) {
	asOf := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	events := []Event{
		{Agent: "claude", ModelID: "m", Timestamp: asOf.Add(-1 * time.Hour), Worked: boolPtr(true)},
		{Agent: "claude", ModelID: "m", Timestamp: asOf.Add(-1 * time.Hour), Worked: boolPtr(false)},
		{Agent: "claude", ModelID: "m", Timestamp: asOf.Add(-1 * time.Hour), Skipped: true},
	}
	got := ModelStats(events, Window1d, asOf)["m"]
	if got.Answered != 2 {
		t.Fatalf("Answered = %d, want 2 (skip excluded)", got.Answered)
	}
	if got.Skipped != 1 {
		t.Fatalf("Skipped = %d, want 1", got.Skipped)
	}
	pct, ok := got.WorkedPct()
	if !ok || pct != 50 {
		t.Fatalf("WorkedPct = %v, %v, want 50, true", pct, ok)
	}
}

// TestModelStatsWindowExcludesOldEvents verifies events outside the
// requested window are dropped from the aggregate, so a 1d query does not
// pick up a 10-day-old event.
func TestModelStatsWindowExcludesOldEvents(t *testing.T) {
	asOf := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	events := []Event{
		{Agent: "claude", ModelID: "m", Timestamp: asOf.Add(-30 * time.Minute), Worked: boolPtr(true)},
		{Agent: "claude", ModelID: "m", Timestamp: asOf.Add(-10 * 24 * time.Hour), Worked: boolPtr(true)},
	}
	got := ModelStats(events, Window1d, asOf)["m"]
	if got.Answered != 1 {
		t.Fatalf("Answered = %d, want 1 (10-day-old event excluded from 1d window)", got.Answered)
	}
	got30 := ModelStats(events, Window30d, asOf)["m"]
	if got30.Answered != 2 {
		t.Fatalf("Answered(30d) = %d, want 2", got30.Answered)
	}
}

// TestAgentModelStatsFiltersByAgent verifies AgentModelStats only
// aggregates events for the requested agent — the picker's agent-scoped
// stats must not leak another agent's verdicts for the same model.
func TestAgentModelStatsFiltersByAgent(t *testing.T) {
	asOf := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	events := []Event{
		{Agent: "claude", ModelID: "m", Timestamp: asOf.Add(-1 * time.Hour), Worked: boolPtr(true)},
		{Agent: "codex", ModelID: "m", Timestamp: asOf.Add(-1 * time.Hour), Worked: boolPtr(false)},
	}
	got := AgentModelStats(events, "claude", Window1d, asOf)["m"]
	if got.Answered != 1 || got.Worked != 1 {
		t.Fatalf("claude stats = %+v, want Answered=1 Worked=1", got)
	}
	if _, ok := AgentModelStats(events, "claude", Window1d, asOf)["nonexistent"]; ok {
		t.Fatal("expected no entry for a model with no events")
	}
}

// TestAllAgentModelStatsGroupsByCombo verifies every observed (agent,
// model) pair gets its own row, and that two agents sharing a model do
// not merge into one row — this is what makes a bad agent×model combo
// visible in `wt stats` even when the model itself looks fine overall.
func TestAllAgentModelStatsGroupsByCombo(t *testing.T) {
	asOf := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	events := []Event{
		{Agent: "claude", ModelID: "m", Timestamp: asOf.Add(-1 * time.Hour), Worked: boolPtr(true)},
		{Agent: "codex", ModelID: "m", Timestamp: asOf.Add(-1 * time.Hour), Worked: boolPtr(false)},
	}
	rows := AllAgentModelStats(events, Window1d, asOf)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	byAgent := map[string]ComboRow{}
	for _, r := range rows {
		byAgent[r.Agent] = r
	}
	if byAgent["claude"].Stats.Worked != 1 {
		t.Fatalf("claude row = %+v, want Worked=1", byAgent["claude"])
	}
	if byAgent["codex"].Stats.Failed != 1 {
		t.Fatalf("codex row = %+v, want Failed=1", byAgent["codex"])
	}
}
