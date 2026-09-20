package tui

import (
	"fmt"
	"sort"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localgate"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/survey"
	"github.com/ohanaverse/local-ai-setup/wt/internal/usage"
)

type rowStatus string

const (
	statusOK      rowStatus = "ok"      // on disk (local) or simply available (cloud)
	statusAbsent  rowStatus = "absent"  // the provider answered and does not have this model
	statusUnknown rowStatus = "unknown" // local model whose presence the probe could not determine
	statusNew     rowStatus = "new"     // discovered, unregistered
)

// tableRow is one line of the selector table before rendering.
type tableRow struct {
	model      config.Model
	location   config.Location
	status     rowStatus
	exposed    bool
	running    bool
	discovered bool
	counts     usage.UsageCounts
	stats      survey.Stats
}

// tableInput gathers everything buildRows needs. models is the agent's
// PRE-GATE eligible list (exposed cloud + every configured local model,
// already filtered by agent/-T/-F); inventory is nil when no local probe ran,
// in which case no local row is assessed at all: locals read as not running
// with status ok. When inventory is set, a local row's status is ok, absent
// (the probe answered and found no artifact), unknown (it could not tell), or
// new (discovered, unregistered).
type tableInput struct {
	cfg            *config.Config
	agent          string
	models         []config.Model
	inventory      *localmodels.Snapshot
	hideDiscovered bool
	usage          usage.Store
	stats          map[string]survey.Stats
}

func buildRows(in tableInput) []tableRow {
	// Only REGISTERED entries describe a configured model. A discovered
	// entry may share an id with a registry row by construction
	// (DiscoveredModelID is provider/artifact); it must never overwrite the
	// registered entry's Artifact/Running.
	byID := map[string]localmodels.Entry{}
	if in.inventory != nil {
		for _, e := range in.inventory.Entries {
			if e.Registered {
				byID[e.ModelID] = e
			}
		}
	}
	rows := make([]tableRow, 0, len(in.models))
	seen := map[string]bool{}
	for _, m := range in.models {
		loc := m.Location
		if in.cfg != nil {
			if l, err := in.cfg.ResolveLocation(m); err == nil {
				loc = l
			}
		}
		r := tableRow{model: m, location: loc, status: statusOK}
		if loc == config.LocationLocal && in.inventory != nil {
			e, ok := byID[m.ID]
			switch {
			case !ok:
				// No registered entry: the probe skipped this model (an
				// unresolvable location) or its family has no probe at all.
				// Nothing was discovered about it, so presence is unknown.
				r.status = statusUnknown
			case e.Running:
				// Serving right now, so nothing about the row is missing.
				// Checked before the artifact tests because a running
				// mlx_lm_server row never has a resolved artifact.
				r.running = true
			case !e.ArtifactKnown:
				r.status = statusUnknown
			case e.Artifact == "":
				r.status = statusAbsent
			}
		}
		r.exposed = m.Native || (in.cfg != nil && in.cfg.ExposedFlag(m.ID))
		rows = append(rows, r)
		seen[m.ID] = true
	}
	if in.inventory != nil && !in.hideDiscovered {
		for _, e := range in.inventory.Entries {
			if e.Registered || seen[e.ModelID] {
				continue
			}
			if in.agent != "" && (in.cfg == nil || !in.cfg.AgentSupportsProvider(in.agent, e.ProviderID)) {
				continue
			}
			rows = append(rows, tableRow{
				model: config.Model{
					ID: e.ModelID, ProviderID: e.ProviderID, ModelName: e.Artifact,
					Location: config.LocationLocal, Source: config.SourceDiscovered,
				},
				location: config.LocationLocal, status: statusNew, running: e.Running, discovered: true,
			})
			seen[e.ModelID] = true
		}
	}

	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.model.ID
	}
	var counts map[string]usage.UsageCounts
	if in.usage != nil {
		if in.agent != "" {
			counts = in.usage.CountsForAgent(in.agent, ids)
		} else {
			counts = in.usage.Counts(ids)
		}
	}
	for i := range rows {
		rows[i].counts = counts[rows[i].model.ID]
		rows[i].stats = in.stats[rows[i].model.ID]
	}
	return rows
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
	if r.location == config.LocationLocal {
		return costKey{}
	}
	c := r.model.Cost
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
	group1 := func(r tableRow) bool { return r.location != config.LocationLocal || r.running }
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		ga, gb := group1(a), group1(b)
		if ga != gb {
			return ga
		}
		if !ga {
			return a.model.ID < b.model.ID
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
		return a.model.ID < b.model.ID
	})
}

// launchable reports whether Enter may launch the row now: cloud always; a
// running local row; and a pulled ollama model even when not loaded, because
// ollama's daemon loads models on demand (and modelman starts ollama models
// flag-only, so gating them would regress launching them).
func (r tableRow) launchable() bool {
	if r.location != config.LocationLocal || r.running {
		return true
	}
	return r.model.ProviderID == "ollama" && r.status != statusAbsent
}

// notLaunchableHint is the status line shown when Enter lands on a row that
// cannot launch yet.
func (r tableRow) notLaunchableHint() string {
	if r.discovered {
		return fmt.Sprintf("%s is not running — start it with the %s CLI", r.model.ID, r.model.ProviderID)
	}
	return (&localgate.NotRunningError{ModelID: r.model.ID}).Error()
}
