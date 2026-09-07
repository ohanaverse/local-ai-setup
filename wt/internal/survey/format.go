package survey

import (
	"fmt"
	"time"
	"unicode/utf8"
)

// ratingCell renders one rating average with its one-letter prefix ("q" or
// "s"), or "<prefix>-" when the rating is unrated (RatedX == 0).
func ratingCell(prefix string, avg float64, ok bool) string {
	if !ok {
		return prefix + "-"
	}
	return fmt.Sprintf("%s%.1f", prefix, avg)
}

// FormatPickerSegment renders the compact 30-day survey segment appended
// to a model picker row: "[⚠]✓<pct> q<quality> s<speed> n<answered>".
// Returns "" when Answered == 0 so the caller can omit the segment
// entirely rather than showing a meaningless "✓- q- s- n0".
//
// ⚠ replaces ✓ when the combo looks broken: WorkedPct < 70% with at least
// 3 answered surveys (Answered < 3 is too little signal to flag).
func FormatPickerSegment(s Stats) string {
	pct, ok := s.WorkedPct()
	if !ok {
		return ""
	}
	marker := "✓"
	if pct < 70 && s.Answered >= 3 {
		marker = "⚠"
	}
	quality, qok := s.QualityAvg()
	speed, sok := s.SpeedAvg()
	return fmt.Sprintf("%s%d%% %s %s n%d",
		marker, int(pct+0.5), ratingCell("q", quality, qok), ratingCell("s", speed, sok), s.Answered)
}

// windowSpec pairs a window's display label with its duration, in the
// fixed 1d/7d/30d display order.
type windowSpec struct {
	label string
	dur   time.Duration
}

var afterSurveyWindows = []windowSpec{
	{"1d", Window1d},
	{"7d", Window7d},
	{"30d", Window30d},
}

// windowSegment renders one window's cell: "<label> —" when nothing was
// answered in that window, otherwise "<label> ✓<pct> q<quality> s<speed>
// (<n>)".
func windowSegment(label string, s Stats) string {
	pct, ok := s.WorkedPct()
	if !ok {
		return label + " —"
	}
	quality, qok := s.QualityAvg()
	speed, sok := s.SpeedAvg()
	return fmt.Sprintf("%s ✓%d%% %s %s (%d)",
		label, int(pct+0.5), ratingCell("q", quality, qok), ratingCell("s", speed, sok), s.Answered)
}

// formatStatsRow pads label to labelWidth, then each segment to its
// column's width (colWidth[i]) with 4 trailing spaces — except the final
// segment, which is unpadded. Used by FormatAfterSurvey so its two output
// rows align column-for-column regardless of which window has data.
func formatStatsRow(label string, labelWidth int, segs []string, colWidth []int) string {
	row := fmt.Sprintf("%-*s  ", labelWidth, label)
	for i, seg := range segs {
		if i == len(segs)-1 {
			row += seg
			continue
		}
		row += fmt.Sprintf("%-*s    ", colWidth[i], seg)
	}
	return row
}

// FormatAfterSurvey renders the two-row post-survey stats block: the
// model's own row (all agents) and the agent×model combo row, with each
// window's segment padded so the 1d/7d/30d columns align between the two
// rows regardless of which segments have data.
func FormatAfterSurvey(events []Event, agent, modelID string, asOf time.Time) string {
	modelLabel := "model"
	comboLabel := fmt.Sprintf("%s × model", agent)
	labelWidth := utf8.RuneCountInString(modelLabel)
	if w := utf8.RuneCountInString(comboLabel); w > labelWidth {
		labelWidth = w
	}

	modelSegs := make([]string, len(afterSurveyWindows))
	comboSegs := make([]string, len(afterSurveyWindows))
	for i, w := range afterSurveyWindows {
		modelSegs[i] = windowSegment(w.label, ModelStats(events, w.dur, asOf)[modelID])
		comboSegs[i] = windowSegment(w.label, AgentModelStats(events, agent, w.dur, asOf)[modelID])
	}

	colWidth := make([]int, len(afterSurveyWindows))
	for i := range afterSurveyWindows {
		if w := utf8.RuneCountInString(modelSegs[i]); w > colWidth[i] {
			colWidth[i] = w
		}
		if w := utf8.RuneCountInString(comboSegs[i]); w > colWidth[i] {
			colWidth[i] = w
		}
	}

	return fmt.Sprintf("%s\n%s",
		formatStatsRow(modelLabel, labelWidth, modelSegs, colWidth),
		formatStatsRow(comboLabel, labelWidth, comboSegs, colWidth))
}
