// Package catalog owns the model-selector row rules shared by wt's pickers
// and its non-TUI launch path: which candidates become rows, each row's live
// presence status, and what selecting a row can do (launch, start, or block
// with a reason). It knows nothing about rendering, usage counts or sorting —
// those stay with the callers, which wrap a Row in their own type.
package catalog

import (
	"fmt"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// Status is a row's presence state.
type Status string

const (
	StatusOK      Status = "ok"      // on disk (local) or simply available (cloud)
	StatusAbsent  Status = "absent"  // the provider answered and does not have this model
	StatusUnknown Status = "unknown" // local model whose presence the probe could not determine
	StatusNew     Status = "new"     // discovered, unregistered
)

// Action is what selecting a row does.
type Action int

const (
	ActionLaunch Action = iota // cloud row, or a local model already running
	ActionStart                // a local model wt can start (its provider has a lifecycle backend)
	ActionBlock                // cannot proceed; BlockReason says why
)

// Row is one model-selector row before rendering.
type Row struct {
	Model      config.Model
	Location   config.Location
	Status     Status
	Exposed    bool
	Running    bool
	Discovered bool
}

// Input gathers everything Build needs. Models is the caller's eligible list
// (exposed cloud + every configured local model, already filtered by
// agent/-T/-F); Inventory is nil when no local probe ran, in which case no
// local row is assessed at all: locals read as not running with status ok.
// When Inventory is set, a local row's status is ok, absent (the probe
// answered and found no artifact), unknown (it could not tell), or new
// (discovered, unregistered).
type Input struct {
	Config         *config.Config
	Agent          string
	Models         []config.Model
	Inventory      *localmodels.Snapshot
	HideDiscovered bool
}

// Build turns Input into rows.
func Build(in Input) []Row {
	// Only REGISTERED entries describe a configured model. A discovered
	// entry may share an id with a registry row by construction
	// (DiscoveredModelID is provider/artifact); it must never overwrite the
	// registered entry's Artifact/Running.
	byID := map[string]localmodels.Entry{}
	if in.Inventory != nil {
		for _, e := range in.Inventory.Entries {
			if e.Registered {
				byID[e.ModelID] = e
			}
		}
	}
	rows := make([]Row, 0, len(in.Models))
	seen := map[string]bool{}
	for _, m := range in.Models {
		loc := m.Location
		if in.Config != nil {
			if l, err := in.Config.ResolveLocation(m); err == nil {
				loc = l
			}
		}
		r := Row{Model: m, Location: loc, Status: StatusOK}
		if loc == config.LocationLocal && in.Inventory != nil {
			e, ok := byID[m.ID]
			switch {
			case !ok:
				// No registered entry: the probe skipped this model (an
				// unresolvable location) or its family has no probe at all.
				// Nothing was discovered about it, so presence is unknown.
				r.Status = StatusUnknown
			case e.Running:
				// Serving right now, so nothing about the row is missing.
				// Checked before the artifact tests because a running
				// mlx_lm_server row never has a resolved artifact.
				r.Running = true
			case !e.ArtifactKnown:
				r.Status = StatusUnknown
			case e.Artifact == "":
				r.Status = StatusAbsent
			}
		}
		r.Exposed = m.Native || (in.Config != nil && in.Config.ExposedFlag(m.ID))
		rows = append(rows, r)
		seen[m.ID] = true
	}
	if in.Inventory != nil && !in.HideDiscovered {
		for _, e := range in.Inventory.Entries {
			if e.Registered || seen[e.ModelID] {
				continue
			}
			if in.Agent != "" && (in.Config == nil || !in.Config.AgentSupportsProvider(in.Agent, e.ProviderID)) {
				continue
			}
			rows = append(rows, Row{
				Model: config.Model{
					ID: e.ModelID, ProviderID: e.ProviderID, ModelName: e.Artifact,
					Location: config.LocationLocal, Source: config.SourceDiscovered,
				},
				Location: config.LocationLocal, Status: StatusNew, Running: e.Running, Discovered: true,
			})
			seen[e.ModelID] = true
		}
	}
	return rows
}

// Find returns the row for id, discovered rows included — a -M pin names a
// model, not necessarily a registry entry.
func Find(rows []Row, id string) (Row, bool) {
	for _, r := range rows {
		if r.Model.ID == id {
			return r, true
		}
	}
	return Row{}, false
}

// startable reports whether wt has a lifecycle backend for the provider
// family. It delegates to lifecycle.Startable — the single source of truth —
// so adding a backend there updates every picker with no second edit here.
func startable(providerID string) bool { return lifecycle.Startable(providerID) }

// Action reports what selecting r does. A non-running local row is startable
// when its provider has a lifecycle backend and the model is not known to be
// missing from disk.
func (r Row) Action() Action {
	if r.Location != config.LocationLocal || r.Running {
		return ActionLaunch
	}
	if !startable(r.Model.ProviderID) {
		return ActionBlock
	}
	if r.Status == StatusAbsent {
		return ActionBlock
	}
	return ActionStart
}

// BlockReason is the status line for an ActionBlock row ("" otherwise): a
// model missing from disk needs pulling; a provider wt cannot start needs
// modelman.
func (r Row) BlockReason() string {
	if r.Action() != ActionBlock {
		return ""
	}
	if startable(r.Model.ProviderID) {
		return fmt.Sprintf("%s is not on disk — pull or download it first", r.Model.ID)
	}
	return fmt.Sprintf("local model %q is not running — start it with `modelman start %s`", r.Model.ID, r.Model.ID)
}

// RefusedByRoute reports whether the row must be refused because of how it
// routes: a discovered model — one wt found on disk, absent from the LiteLLM
// gateway's model_list — cannot be launched through the proxy. It is false
// when the route failed to resolve (a launch row with a route error stays
// selectable and reports the error on Enter). The picker table, the non-TUI
// -M pin and `wt smoke` all decide through this one rule; each keeps its own
// wording.
func (r Row) RefusedByRoute(route config.Route, routeErr error) bool {
	return r.Discovered && routeErr == nil && (route.Litellm || route.Forced)
}
