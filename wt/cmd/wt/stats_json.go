// `wt stats --json`: both tables as one JSON document on stdout.
package main

import (
	"encoding/json"
	"io"
	"time"
)

// statsJSON is the document `wt stats --json` prints: one line, one
// object. Every list is present and is [] when empty; a value that does not
// exist (an average with nothing rated, spend with no spend data) is null,
// never 0.
type statsJSON struct {
	Window string          `json:"window"`
	AsOf   string          `json:"as_of"`
	Survey []surveyRowJSON `json:"survey"`
	Usage  usageJSON       `json:"usage"`
}

// surveyRowJSON is one survey table row. Agent is null for a model's
// all-agents aggregate (the table's "(all)") and only for it: an agent whose
// configured name is "(all)" keeps its name here, so a document still says
// which agent a row covers.
type surveyRowJSON struct {
	Model      string   `json:"model"`
	Agent      *string  `json:"agent"`
	Answered   int      `json:"answered"`
	Worked     int      `json:"worked"`
	Failed     int      `json:"failed"`
	Skipped    int      `json:"skipped"`
	WorkedPct  *float64 `json:"worked_pct"`
	QualityAvg *float64 `json:"quality_avg"`
	SpeedAvg   *float64 `json:"speed_avg"`
}

// usageJSON is the usage table. SpendStatus is one of ok, not_configured,
// unavailable, skipped; SpendReason is the stderr note's text ("" for ok).
type usageJSON struct {
	SpendStatus          string         `json:"spend_status"`
	SpendReason          string         `json:"spend_reason"`
	UnattributedRequests *int64         `json:"unattributed_requests"`
	Rows                 []usageRowJSON `json:"rows"`
}

// usageRowJSON is one model. The four spend fields are null unless
// spend_status is ok. AlsoLoggedAs lists the other spellings of Model whose
// requests are included in those fields (buildUsageRows' fold); like every
// list it is [] when empty.
type usageRowJSON struct {
	Model            string   `json:"model"`
	Family           string   `json:"family"`
	Launches         int      `json:"launches"`
	Requests         *int64   `json:"requests"`
	PromptTokens     *int64   `json:"prompt_tokens"`
	CompletionTokens *int64   `json:"completion_tokens"`
	Spend            *float64 `json:"spend"`
	AlsoLoggedAs     []string `json:"also_logged_as"`
}

// optional returns &v when ok, else nil (JSON null).
func optional(v float64, ok bool) *float64 {
	if !ok {
		return nil
	}
	return &v
}

// buildStatsJSON assembles the document from the same rows the text tables
// are rendered from, so the two renderings cannot disagree.
func buildStatsJSON(window string, asOf time.Time, survey []statsRow, rep usageReport) statsJSON {
	doc := statsJSON{
		Window: window,
		AsOf:   asOf.UTC().Format(time.RFC3339),
		Survey: []surveyRowJSON{},
		Usage:  usageJSON{SpendStatus: rep.SpendStatus, SpendReason: rep.SpendReason, Rows: []usageRowJSON{}},
	}
	for _, r := range survey {
		if surveyEmptyStats(r.Stats) {
			continue
		}
		j := surveyRowJSON{
			Model: r.ModelID, Answered: r.Stats.Answered, Worked: r.Stats.Worked,
			Failed: r.Stats.Failed, Skipped: r.Stats.Skipped,
			WorkedPct:  optional(r.Stats.WorkedPct()),
			QualityAvg: optional(r.Stats.QualityAvg()),
			SpeedAvg:   optional(r.Stats.SpeedAvg()),
		}
		if !r.Aggregate {
			agent := r.Agent
			j.Agent = &agent
		}
		doc.Survey = append(doc.Survey, j)
	}
	if rep.SpendStatus == spendOK {
		n := rep.Unattributed
		doc.Usage.UnattributedRequests = &n
	}
	for _, r := range rep.Rows {
		j := usageRowJSON{Model: r.Model, Family: r.Family, Launches: r.Launches, AlsoLoggedAs: []string{}}
		j.AlsoLoggedAs = append(j.AlsoLoggedAs, r.AlsoLoggedAs...)
		if r.Spend != nil {
			s := *r.Spend
			j.Requests, j.PromptTokens, j.CompletionTokens, j.Spend = &s.Requests, &s.PromptTokens, &s.CompletionTokens, &s.Spend
		}
		doc.Usage.Rows = append(doc.Usage.Rows, j)
	}
	return doc
}

// writeStatsJSON prints doc as one line.
func writeStatsJSON(out io.Writer, doc statsJSON) error {
	return json.NewEncoder(out).Encode(doc)
}
