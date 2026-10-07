// The usage half of `wt stats`: launches from usage.jsonl joined with the
// LiteLLM proxy's spend log, one row per model. Split from stats.go so the
// survey report and the usage report can each be read whole.
package main

import (
	"sort"
	"strings"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/spend"
	"github.com/ohanaverse/local-ai-setup/wt/internal/survey"
	"github.com/ohanaverse/local-ai-setup/wt/internal/usage"
)

// usageRow is one model in the usage table. Spend is nil when the report
// has no spend data at all (every status but spendOK); with spend data, a
// model the proxy logged nothing for has a zero Row, not nil.
type usageRow struct {
	Model    string
	Family   string
	Launches int
	Spend    *spend.Row
}

// familyFor is a model's family: the registry's, else the id's provider
// prefix (the part before the first "/"), else "unknown". The order is
// modelman's (usage/reconcile.py _family_for), so a model that has left the
// registry still answers to --family. Unlike modelman, an empty registry
// family or an empty prefix ("/x") falls through to the next rule: no row
// gets a family --family cannot name.
func familyFor(id string, families map[string]string) string {
	if f, ok := families[id]; ok && f != "" {
		return f
	}
	if prefix, _, ok := strings.Cut(id, "/"); ok && prefix != "" {
		return prefix
	}
	return "unknown"
}

// launchesIn picks the count for the report's window.
func launchesIn(c usage.UsageCounts, window time.Duration) int {
	switch window {
	case survey.Window1d:
		return c.OneDay
	case survey.Window7d:
		return c.SevenDay
	}
	return c.ThirtyDay
}

// buildUsageRows joins launch counts with spend rows on the model id — the
// LiteLLM route's model_name is the registry id, so the proxy's model_group
// and usage.jsonl's model_id are the same string. A model gets a row when
// it has a launch in the window or a spend row; sp is nil when there is no
// spend data. Both filters are exact matches and apply to every observed
// id, registered or not. Rows are sorted by model id.
func buildUsageRows(counts map[string]usage.UsageCounts, window time.Duration, sp *spend.Result, families map[string]string, modelFilter, familyFilter string) []usageRow {
	byModel := map[string]*usageRow{}
	row := func(id string) *usageRow {
		r, ok := byModel[id]
		if !ok {
			r = &usageRow{Model: id, Family: familyFor(id, families)}
			if sp != nil {
				r.Spend = &spend.Row{Model: id}
			}
			byModel[id] = r
		}
		return r
	}
	for id, c := range counts {
		if n := launchesIn(c, window); n > 0 {
			row(id).Launches = n
		}
	}
	if sp != nil {
		for _, s := range sp.Rows {
			row(s.Model).Spend = &s
		}
	}

	rows := make([]usageRow, 0, len(byModel))
	for _, r := range byModel {
		if modelFilter != "" && r.Model != modelFilter {
			continue
		}
		if familyFilter != "" && r.Family != familyFilter {
			continue
		}
		rows = append(rows, *r)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Model < rows[j].Model })
	return rows
}
