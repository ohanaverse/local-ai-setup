package survey

import "time"

// Window durations for stats aggregation.
const (
	Window1d  = 24 * time.Hour
	Window7d  = 7 * 24 * time.Hour
	Window30d = 30 * 24 * time.Hour
)

// Stats accumulates survey outcomes for one (model) or (agent, model) key
// within a time window. Answered = Worked + Failed; Skipped is tracked
// separately since it is excluded from WorkedPct's denominator (a skip
// records participation but carries no verdict).
type Stats struct {
	Answered     int
	Worked       int
	Failed       int
	Skipped      int
	RatedSpeed   int
	SpeedSum     int
	RatedQuality int
	QualitySum   int
}

// WorkedPct returns the worked percentage (0-100) over answered surveys
// only. ok is false when Answered == 0 (nothing to divide by).
func (s Stats) WorkedPct() (float64, bool) {
	if s.Answered == 0 {
		return 0, false
	}
	return float64(s.Worked) / float64(s.Answered) * 100, true
}

// SpeedAvg returns the average speed rating (1-5). ok is false when no
// worked survey carried a speed rating.
func (s Stats) SpeedAvg() (float64, bool) {
	if s.RatedSpeed == 0 {
		return 0, false
	}
	return float64(s.SpeedSum) / float64(s.RatedSpeed), true
}

// QualityAvg returns the average quality rating (1-5). ok is false when no
// worked survey carried a quality rating.
func (s Stats) QualityAvg() (float64, bool) {
	if s.RatedQuality == 0 {
		return 0, false
	}
	return float64(s.QualitySum) / float64(s.RatedQuality), true
}

// accumulate folds one event into s. Callers filter by window/agent/model
// before calling — accumulate itself does no filtering.
func accumulate(s *Stats, ev Event) {
	if ev.Skipped {
		s.Skipped++
		return
	}
	if ev.Worked == nil {
		return
	}
	s.Answered++
	if !*ev.Worked {
		s.Failed++
		return
	}
	s.Worked++
	if ev.Speed != nil {
		s.RatedSpeed++
		s.SpeedSum += *ev.Speed
	}
	if ev.Quality != nil {
		s.RatedQuality++
		s.QualitySum += *ev.Quality
	}
}

// inWindow reports whether ev happened within window of asOf.
func inWindow(ev Event, window time.Duration, asOf time.Time) bool {
	return asOf.Sub(ev.Timestamp.UTC()) < window
}

// ModelStats aggregates events across all agents, keyed by model id, for
// events within window of asOf. Used by the picker's own-model row and by
// `wt stats`'s per-model "(all)" aggregate row.
func ModelStats(events []Event, window time.Duration, asOf time.Time) map[string]Stats {
	out := map[string]Stats{}
	for _, ev := range events {
		if !inWindow(ev, window, asOf) {
			continue
		}
		s := out[ev.ModelID]
		accumulate(&s, ev)
		out[ev.ModelID] = s
	}
	return out
}

// AgentModelStats aggregates events for one agent, keyed by model id, for
// events within window of asOf. Used by the model picker (agent-scoped
// segment) and the after-survey combo row.
func AgentModelStats(events []Event, agent string, window time.Duration, asOf time.Time) map[string]Stats {
	out := map[string]Stats{}
	for _, ev := range events {
		if ev.Agent != agent || !inWindow(ev, window, asOf) {
			continue
		}
		s := out[ev.ModelID]
		accumulate(&s, ev)
		out[ev.ModelID] = s
	}
	return out
}

// ComboRow is one (agent, model) row for AllAgentModelStats / `wt stats`.
type ComboRow struct {
	Agent   string
	ModelID string
	Stats   Stats
}

// AllAgentModelStats aggregates events for every observed (agent, model)
// pair within window of asOf, for the `wt stats` report command.
func AllAgentModelStats(events []Event, window time.Duration, asOf time.Time) []ComboRow {
	type key struct{ agent, model string }
	acc := map[key]*Stats{}
	var order []key
	for _, ev := range events {
		if !inWindow(ev, window, asOf) {
			continue
		}
		k := key{ev.Agent, ev.ModelID}
		s, ok := acc[k]
		if !ok {
			s = &Stats{}
			acc[k] = s
			order = append(order, k)
		}
		accumulate(s, ev)
	}
	rows := make([]ComboRow, 0, len(order))
	for _, k := range order {
		rows = append(rows, ComboRow{Agent: k.agent, ModelID: k.model, Stats: *acc[k]})
	}
	return rows
}
