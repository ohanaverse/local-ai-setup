package modelserver

import (
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// TestFromWireParityWithServerBuiltRows pins that the session-side adapter
// reads what the server wrote: rows from catalog.Build, converted by
// BuildCatalog and back through catalog.FromWire, answer Action, BlockReason,
// Ready, Running and Loading as the originals do. It lives here because
// modelserver imports catalog; a catalog test importing modelserver would be
// an import cycle.
func TestFromWireParityWithServerBuiltRows(t *testing.T) {
	cfg := testCfg(
		config.Model{ID: "omlx/run", Family: "f", ProviderID: "omlx", ModelName: "run"},
		config.Model{ID: "omlx/idle", Family: "f", ProviderID: "omlx", ModelName: "idle"},
		config.Model{ID: "omlx/load", Family: "f", ProviderID: "omlx", ModelName: "load"},
		config.Model{ID: "openrouter/c", Family: "f", ProviderID: "openrouter", ModelName: "c"},
		config.Model{ID: "acme/m", Family: "f", ProviderID: "acme", ModelName: "m"},
		config.Model{ID: "nat/m", Family: "f", ProviderID: "nat", ModelName: "m", Native: true},
		config.Model{ID: "llamacpp/x", Family: "f", ProviderID: "llamacpp", ModelName: "x"},
	)
	cfg.Providers = append(cfg.Providers,
		config.Provider{ID: "acme", Location: config.LocationCloud},
		config.Provider{ID: "nat", Location: config.LocationCloud})
	load := omlxEntry("omlx/load", "load", true)
	load.Loading = true
	snap := localmodels.Snapshot{Entries: []localmodels.Entry{
		omlxEntry("omlx/run", "run", true), omlxEntry("omlx/idle", "idle", false), load,
		{ProviderID: "llamacpp", Artifact: "x", ModelID: "llamacpp/x", ModelName: "x", Registered: true, ArtifactKnown: true},
	}}
	orig := catalog.Build(catalog.Input{Config: cfg, Models: cfg.Models, Inventory: &snap})
	c := BuildCatalog(cfg, snap, nil, testSessions(), nil, testNow)
	if len(orig) != 7 || len(c.Rows) != 7 {
		t.Fatalf("rows: built %d, wire %d, want 7 each", len(orig), len(c.Rows))
	}
	for _, o := range orig {
		g := catalog.FromWire(rowByID(t, c, o.Model.ID))
		if o.Model.ID == "acme/m" {
			// The session never launches direct, so the server's block for a
			// cloud model the gateway cannot route is authoritative: this row
			// diverges from the original Build row, which launches.
			if g.Action() != catalog.ActionBlock || !strings.Contains(g.BlockReason(), "no LiteLLM mapping") || g.BlockReason() != rowByID(t, c, o.Model.ID).BlockReason {
				t.Errorf("acme/m: action %v reason %q, want block with the server's reason", g.Action(), g.BlockReason())
			}
			continue
		}
		if g.Action() != o.Action() || g.BlockReason() != o.BlockReason() || g.Ready() != o.Ready() || g.Running != o.Running || g.Loading != o.Loading {
			t.Errorf("%s: wire action/reason/ready/running/loading = %v/%q/%v/%v/%v, original %v/%q/%v/%v/%v", o.Model.ID,
				g.Action(), g.BlockReason(), g.Ready(), g.Running, g.Loading,
				o.Action(), o.BlockReason(), o.Ready(), o.Running, o.Loading)
		}
	}
}
