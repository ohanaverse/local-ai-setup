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
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// Status is a row's presence state.
type Status string

const (
	StatusOK      Status = "ok"      // on disk (local) or simply available (cloud)
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
	Running    bool
	Discovered bool
	// Unmapped marks a cloud row whose provider has no LiteLLM mapping: it
	// is in the catalog (every configured model is, #179) but sync never
	// routes it, so a launch through the proxy would fail with "Invalid
	// model name". RefusedByRoute refuses it there; direct, it launches.
	Unmapped bool
}

// Input gathers everything Build needs. Models is the caller's eligible list
// (every catalog model the agent supports, already filtered by
// agent/-T/-F); Inventory is nil when no local probe ran, in which case no
// local row is assessed at all: locals read as not running with status ok.
// When Inventory is set, local rows come only from it (#179 Phase B): a
// registry local model gets a row only when its inventory entry is running,
// on disk, or of a family whose discovery failed (status unknown — a flaky
// probe must not hide a pulled model); a model confirmed missing from disk,
// a non-running mlx_lm_server pairing, and a model with no inventory entry
// get none. A local row's status is ok, unknown, or new (discovered,
// unregistered). HideDiscovered drops discovered rows; callers set it
// whenever -T/-F is active, since a discovered row has no family or tags a
// filter could match.
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
	// registered entry's Artifact/Running. A discovered model handed in via
	// Models (e.g. a caller that already built rows and re-feeds them to a
	// picker) is described by its non-registered entry, so both maps are filled
	// in one pass over the inventory.
	byID := map[string]localmodels.Entry{}
	discByID := map[string]localmodels.Entry{}
	if in.Inventory != nil {
		for _, e := range in.Inventory.Entries {
			if e.Registered {
				byID[e.ModelID] = e
			} else {
				discByID[e.ModelID] = e
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
		if de, isDisc := discByID[m.ID]; isDisc && m.Source == config.SourceDiscovered && loc == config.LocationLocal {
			r.Status, r.Running, r.Discovered = StatusNew, de.Running, true
		} else if loc == config.LocationLocal && in.Inventory != nil {
			e, ok := byID[m.ID]
			if !ok || !listed(e) {
				continue
			}
			r.Running = e.Running
			if !e.Running && !e.ArtifactKnown {
				r.Status = StatusUnknown
			}
		}
		if _, ok := litellm.PolicyFor(m.ProviderID); !ok && !m.Native && loc == config.LocationCloud {
			r.Unmapped = true
		}
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

// listed reports whether a registered inventory entry gets a row: it is
// serving, or its artifact is on disk, or discovery could not tell (unknown,
// not missing) — except for mlx_lm_server, whose pairings can never be
// discovered, so only a serving one is listed.
func listed(e localmodels.Entry) bool {
	switch {
	case e.Running, e.Artifact != "":
		return true
	case !e.ArtifactKnown:
		return localmodels.Family(e.ProviderID) != "mlx_lm_server"
	}
	return false
}

// MissingReason is the message for a pin of a registry local model that has
// no row (#179 Phase B: local rows come only from the inventory): "<id> is
// not on disk" when the probe confirmed its artifact is missing, or the
// `modelman start` hint for a non-running mlx_lm_server pairing, which wt can
// neither discover nor start. It is "" for any id the snapshot cannot vouch
// for — a model with a row, an unknown artifact, a discovered or unknown id,
// or a nil snapshot (no probe ran) — so the caller keeps its own wording.
// `wt -M` (both paths), `wt start` and `wt smoke` consult it only when
// catalog.Find misses.
func MissingReason(snap *localmodels.Snapshot, id string) string {
	if snap == nil {
		return ""
	}
	for _, e := range snap.Entries {
		if !e.Registered || e.ModelID != id || e.Running {
			continue
		}
		switch {
		case localmodels.Family(e.ProviderID) == "mlx_lm_server":
			return fmt.Sprintf("local model %q is not running — start it with `modelman start %s`", id, id)
		case e.ArtifactKnown && e.Artifact == "":
			return fmt.Sprintf("%s is not on disk — pull or download it first", id)
		}
	}
	return ""
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
// when its provider has a lifecycle backend (a model missing from disk has no
// row at all).
func (r Row) Action() Action {
	if r.Location != config.LocationLocal || r.Running {
		return ActionLaunch
	}
	if !startable(r.Model.ProviderID) {
		return ActionBlock
	}
	return ActionStart
}

// BlockReason is the status line for an ActionBlock row ("" otherwise): a
// provider wt cannot start needs modelman.
func (r Row) BlockReason() string {
	if r.Action() != ActionBlock {
		return ""
	}
	return fmt.Sprintf("local model %q is not running — start it with `modelman start %s`", r.Model.ID, r.Model.ID)
}

// RefusedByRoute reports whether the row must be refused because of how it
// routes: a discovered model — one wt found on disk, absent from the LiteLLM
// gateway's model_list — or an Unmapped cloud model cannot be launched
// through the proxy. It is false when the route failed to resolve (a launch
// row with a route error stays selectable and reports the error on Enter).
// The picker table, the non-TUI -M pin and `wt smoke` all decide through this
// one rule, and RouteRefusal gives them its one wording.
func (r Row) RefusedByRoute(route config.Route, routeErr error) bool {
	return (r.Discovered || r.Unmapped) && routeErr == nil && (route.Litellm || route.Forced)
}

// RouteRefusal is RefusedByRoute's reason, or "" when the row is not refused.
func (r Row) RouteRefusal(route config.Route, routeErr error) string {
	switch {
	case !r.RefusedByRoute(route, routeErr):
		return ""
	case r.Discovered:
		return "discovered model " + r.Model.ID + " is not in LiteLLM — turn LiteLLM routing off (wt litellm off) to use it"
	default:
		return fmt.Sprintf("cloud model %s is not in LiteLLM (provider %q has no LiteLLM mapping) — turn LiteLLM routing off (wt litellm off) to use it", r.Model.ID, r.Model.ProviderID)
	}
}
