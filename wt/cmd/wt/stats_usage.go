// The usage half of `wt stats`: launches from usage.jsonl joined with the
// LiteLLM proxy's spend log, one row per model. Split from stats.go so the
// survey report and the usage report can each be read whole.
package main

import (
	"context"
	"errors"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
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

// The four answers to "is there spend data in this report".
const (
	spendOK            = "ok"
	spendNotConfigured = "not_configured" // nothing names a database
	spendUnavailable   = "unavailable"    // psql missing, database down, query failed
	spendSkipped       = "skipped"        // --agent: the spend log has no agent
)

// usageReport is everything the usage table and its notes are built from.
type usageReport struct {
	Rows         []usageRow
	SpendStatus  string
	SpendReason  string // why spend is missing; "" when SpendStatus is spendOK
	Unattributed int64  // requests the proxy logged with no model, in the whole window
	Narrowed     bool   // --model or --family was given: Rows is part of the window
}

// registryNote is the extra note for --family when the registry did not
// load: every family is then an id's provider prefix, and a registry family
// (gemma4) matches nothing.
const registryNote = "the registry did not load (run `wt config` to repair), so --family matched each id's provider prefix"

// querySpend asks the LiteLLM database for per-model totals between start
// and end. A seam: cmd/wt's TestMain replaces it, so no test resolves a
// connection string or runs psql.
var querySpend = realQuerySpend

func realQuerySpend(ctx context.Context, start, end time.Time) (spend.Result, error) {
	dsn, err := litellm.DatabaseURL()
	if err != nil {
		return spend.Result{}, err
	}
	return spend.Query(ctx, dsn, start, end)
}

// stdoutWidth is the terminal's width in columns, or 0 when stdout is not
// a terminal (a pipe or a file has no width to fit). A seam so tests do not
// depend on where `go test` was run.
var stdoutWidth = realStdoutWidth

func realStdoutWidth() int {
	w, _, err := term.GetSize(os.Stdout.Fd())
	if err != nil {
		return 0
	}
	return w
}

// collectUsage builds the usage report for one window ending at asOf.
// It never fails: every way of having no spend data is a status and a
// reason, and the launch counts are reported regardless.
func collectUsage(ctx context.Context, cfg *config.Config, window time.Duration, asOf time.Time, modelFilter, familyFilter, agentFilter string) usageReport {
	rep := usageReport{SpendStatus: spendOK, Narrowed: modelFilter != "" || familyFilter != ""}
	var sp *spend.Result
	if agentFilter != "" {
		rep.SpendStatus = spendSkipped
		rep.SpendReason = "--agent narrows launches only; LiteLLM does not log which agent sent a request, so spend is not shown"
	} else if res, err := querySpend(ctx, asOf.Add(-window), asOf); err != nil {
		rep.SpendStatus = spendUnavailable
		if errors.Is(err, litellm.ErrNoDatabase) {
			rep.SpendStatus = spendNotConfigured
		}
		rep.SpendReason = "spend unavailable: " + err.Error()
	} else {
		sp = &res
		rep.Unattributed = res.Unattributed
	}
	rep.Rows = buildUsageRows(usage.NewStore().AllCounts(agentFilter), window, sp, registryFamilies(cfg), modelFilter, familyFilter)
	return rep
}

// notes are the lines `wt stats` writes to stderr after the tables: at
// most one about missing spend, and one counting requests with no model.
// The count is the whole window's — the query is never filtered — so it is
// left out when --model or --family narrowed the rows, where it would read
// as a fact about the model shown.
func (r usageReport) notes() []string {
	var out []string
	if r.SpendReason != "" {
		out = append(out, r.SpendReason)
	}
	switch {
	case r.Narrowed:
	case r.Unattributed == 1:
		out = append(out, "1 request had no model and is not shown")
	case r.Unattributed > 1:
		out = append(out, formatCount(r.Unattributed)+" requests had no model and are not shown")
	}
	return out
}

// registryFamilies maps each registry model id to its family. cfg is never
// nil (newApp substitutes an empty config when the registry does not load),
// and an empty model list simply yields no families: familyFor then falls
// back to each id's prefix.
func registryFamilies(cfg *config.Config) map[string]string {
	out := map[string]string{}
	for _, m := range cfg.Models {
		out[m.ID] = m.Family
	}
	return out
}
