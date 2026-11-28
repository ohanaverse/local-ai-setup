package localmodels

// The host half of the model-selector row rules: which models become rows
// and the message for a pin with no row. Both read a live inventory snapshot,
// so they live beside the probes, on the model server's side of the import
// graph; internal/catalog keeps Row and what a row does, which a session
// needs without linking this package (internal/depcheck).

import (
	"fmt"

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// CatalogInput gathers everything CatalogRows needs. Models is the caller's
// eligible list (every catalog model the agent supports, already filtered by
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
type CatalogInput struct {
	Config         *config.Config
	Agent          string
	Models         []config.Model
	Inventory      *Snapshot
	HideDiscovered bool
}

// CatalogRows turns in into the model-selector rows (catalog.Row) the
// model server serves: the host half of the row rules, which reads a live
// inventory snapshot. What a row then does (Action, BlockReason) is
// internal/catalog's.
func CatalogRows(in CatalogInput) []catalog.Row {
	// Only REGISTERED entries describe a configured model. A discovered
	// entry may share an id with a registry row by construction
	// (DiscoveredModelID is provider/artifact); it must never overwrite the
	// registered entry's Artifact/Running. A discovered model handed in via
	// Models (e.g. a caller that already built rows and re-feeds them to a
	// picker) is described by its non-registered entry, so both maps are filled
	// in one pass over the inventory.
	byID := map[string]Entry{}
	discByID := map[string]Entry{}
	if in.Inventory != nil {
		for _, e := range in.Inventory.Entries {
			if e.Registered {
				byID[e.ModelID] = e
			} else {
				discByID[e.ModelID] = e
			}
		}
	}
	rows := make([]catalog.Row, 0, len(in.Models))
	seen := map[string]bool{}
	for _, m := range in.Models {
		r := catalog.ModelRow(in.Config, m)
		loc := r.Location
		if de, isDisc := discByID[m.ID]; isDisc && m.Source == config.SourceDiscovered && loc == config.LocationLocal {
			r.Status, r.Running, r.Loading, r.Discovered = catalog.StatusNew, de.Running, de.Loading, true
		} else if loc == config.LocationLocal && in.Inventory != nil {
			e, ok := byID[m.ID]
			if !ok || !listed(e) {
				// The hidden registry id still shadows a discovered entry
				// spelling the same id (provider/artifact): surfacing that
				// artifact under a registry id would let usage history and
				// profiles — all keyed by id — resolve it to the registry
				// model, whose model_name is a different, missing artifact.
				seen[m.ID] = true
				continue
			}
			r.Running, r.Loading = e.Running, e.Loading
			if !e.Running && !e.ArtifactKnown {
				r.Status = catalog.StatusUnknown
			}
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
			rows = append(rows, catalog.Row{
				Model: config.Model{
					ID: e.ModelID, ProviderID: e.ProviderID, ModelName: e.Artifact,
					Location: config.LocationLocal, Source: config.SourceDiscovered,
				},
				Location: config.LocationLocal, Status: catalog.StatusNew, Running: e.Running, Loading: e.Loading, Discovered: true,
			})
			seen[e.ModelID] = true
		}
	}
	return rows
}

// listed reports whether a registered inventory entry gets a row: it is
// serving, or its artifact is on disk, or discovery could not tell (unknown,
// not missing) — except for a running-only family (RunningOnly:
// mlx_lm_server), whose models can never be discovered, so only a serving one
// is listed.
func listed(e Entry) bool {
	switch {
	case e.Running, e.Artifact != "":
		return true
	case !e.ArtifactKnown:
		return !RunningOnly(e.ProviderID)
	}
	return false
}

// MissingReason is the message for a pin of a registry local model that has
// no row (#179 Phase B: local rows come only from the inventory): "<id> is
// not on disk" when the probe confirmed its artifact is missing, or the
// command that starts it for a non-running model of a running-only family
// (RunningOnly: an mlx_lm_server pairing), which wt can neither discover nor
// start. cfg is where the pairing's target and draft are read
// from (nil is allowed). It is "" for any id the snapshot cannot vouch for — a
// model with a row, an unknown artifact, a discovered or unknown id, or a nil
// snapshot (no probe ran) — so the caller keeps its own wording. `wt start`,
// `wt smoke` and `wt -M` (both paths) consult it only when catalog.Find
// misses; `wt -M` additionally only for a pin in the agent's eligible list, so
// a pin the agent cannot use at all is never told to pull.
func MissingReason(cfg *config.Config, snap *Snapshot, id string) string {
	if snap == nil {
		return ""
	}
	for _, e := range snap.Entries {
		if !e.Registered || e.ModelID != id || e.Running {
			continue
		}
		switch {
		case RunningOnly(e.ProviderID):
			m := config.Model{ID: id, ProviderID: e.ProviderID}
			if cfg != nil {
				if i := config.IndexModelByID(cfg.Models, id); i >= 0 {
					m = cfg.Models[i]
				}
			}
			return catalog.NotRunning(m)
		case e.ArtifactKnown && e.Artifact == "":
			return fmt.Sprintf("%s is not on disk — pull or download it first", id)
		}
	}
	return ""
}
