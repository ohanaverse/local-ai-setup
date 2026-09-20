package tui

import (
	"sort"

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/survey"
	"github.com/ohanaverse/local-ai-setup/wt/internal/usage"
)

// tableRow is one line of the selector table: the shared catalog rules
// (presence status, action, block reason) plus the picker-only decorations
// (agent-scoped usage counts, survey stats).
type tableRow struct {
	catalog.Row
	counts usage.UsageCounts
	stats  survey.Stats
}

// tableInput gathers everything buildRows needs. models is the agent's
// eligible list (exposed cloud + every configured local model, already
// filtered by agent/-T/-F); inventory is nil when no local probe ran.
type tableInput struct {
	cfg            *config.Config
	agent          string
	models         []config.Model
	inventory      *localmodels.Snapshot
	hideDiscovered bool
	usage          usage.Store
	stats          map[string]survey.Stats
}

// buildRows builds the shared catalog rows and decorates them with the
// picker's per-agent usage counts and per-model survey stats. The row rules
// themselves live in internal/catalog so the non-TUI launch path and
// `wt smoke` reach the same verdicts this table displays.
func buildRows(in tableInput) []tableRow {
	rows := catalog.Build(catalog.Input{
		Config:         in.cfg,
		Agent:          in.agent,
		Models:         in.models,
		Inventory:      in.inventory,
		HideDiscovered: in.hideDiscovered,
	})
	out := make([]tableRow, len(rows))
	ids := make([]string, len(rows))
	for i, r := range rows {
		out[i] = tableRow{Row: r}
		ids[i] = r.Model.ID
	}
	var counts map[string]usage.UsageCounts
	if in.usage != nil {
		if in.agent != "" {
			counts = in.usage.CountsForAgent(in.agent, ids)
		} else {
			counts = in.usage.Counts(ids)
		}
	}
	for i := range out {
		out[i].counts = counts[out[i].Model.ID]
		out[i].stats = in.stats[out[i].Model.ID]
	}
	return out
}

// costKey is the sort key for "cost ascending": output price per million,
// then input price per million. Local models and subscription-only models
// (no per-token prices) count as $0; a model with no cost data at all sorts
// after every priced model.
type costKey struct {
	noData  bool
	out, in float64
}

func rowCostKey(r tableRow) costKey {
	if r.Location == config.LocationLocal {
		return costKey{}
	}
	c := r.Model.Cost
	if c.InputPricePerMillion == nil && c.CachePricePerMillion == nil && c.OutputPricePerMillion == nil {
		if c.SubscriptionPrice != nil {
			return costKey{}
		}
		return costKey{noData: true}
	}
	var k costKey
	if c.OutputPricePerMillion != nil {
		k.out = *c.OutputPricePerMillion
	}
	if c.InputPricePerMillion != nil {
		k.in = *c.InputPricePerMillion
	}
	return k
}

// sortRows orders rows in place: group 1 (cloud + running local) by cost
// ascending then 7-day usage ascending then id; group 2 (non-running local)
// alphabetical by id.
func sortRows(rows []tableRow) {
	group1 := func(r tableRow) bool { return r.Location != config.LocationLocal || r.Running }
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		ga, gb := group1(a), group1(b)
		if ga != gb {
			return ga
		}
		if !ga {
			return a.Model.ID < b.Model.ID
		}
		ka, kb := rowCostKey(a), rowCostKey(b)
		if ka.noData != kb.noData {
			return !ka.noData
		}
		if ka.out != kb.out {
			return ka.out < kb.out
		}
		if ka.in != kb.in {
			return ka.in < kb.in
		}
		if a.counts.SevenDay != b.counts.SevenDay {
			return a.counts.SevenDay < b.counts.SevenDay
		}
		return a.Model.ID < b.Model.ID
	})
}
