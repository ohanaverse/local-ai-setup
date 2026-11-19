package main

import (
	"slices"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// loadingFixture is the inventory with one omlx model, omlx/c, that omlx is
// still loading: Running (it occupies the pool) and Loading. The pool reading
// is one in which a fresh start of it would need room (ceiling 100, 60 in use,
// 60 more wanted), so a test can tell "planned as a new start" from "joins the
// load". It returns the config and the snapshot.
func loadingFixture() (*config.Config, localmodels.Snapshot) {
	return modelCmdConfig(), localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "omlx", ModelID: "omlx/c", ModelName: "c", Artifact: "c", Registered: true, Running: true, Loading: true, ArtifactKnown: true},
			{ProviderID: "omlx", ModelID: "omlx/other", ModelName: "other", Artifact: "other", Running: true, ArtifactKnown: true},
		},
		OmlxPool: &localmodels.Pool{Ceiling: 100, InUse: 60, SizesKnown: true, Models: []localmodels.PoolModel{
			{ID: "c", Loading: true, Size: 60}, {ID: "other", Loaded: true, Size: 60},
		}},
	}
}

// TestALoadingModelKeepsItsRoute verifies the half of #259 that must not
// change: a model mid-load is still in the set `wt-model litellm sync`
// routes. Dropping it would remove the route a start wrote moments ago and
// restart the proxy under every other session, only to add the route back
// when the load ends.
func TestALoadingModelKeepsItsRoute(t *testing.T) {
	cfg, snap := loadingFixture()
	_, o, _ := litellm.SyncInputs(cfg, func(*config.Config) localmodels.Snapshot { return snap })
	if got := o.Recheck(); !slices.Equal(got, []string{"omlx/c", "omlx/other"}) {
		t.Errorf("desiredLocalIDs = %v, want the loading omlx/c beside omlx/other", got)
	}
}
