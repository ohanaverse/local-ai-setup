package localmodels

import "github.com/ohanaverse/local-ai-setup/wt/internal/config"

// OnDiskSnapshotForTest is the default inventory stub for tests outside this
// package: every registry local model is on disk (artifact = its model_name)
// and nothing is running, with every family's probe OK. Since #179 Phase B a
// local row comes only from the inventory, so an empty snapshot would hide
// every configured local model. A model of a running-only family
// (RunningOnly) gets no artifact, exactly as the real inventory reports one,
// so a stopped pairing has no row here either. Production code never calls
// it; it is exported, like internal/config's ...ForTest helpers, so cmd/wt
// and internal/tui share one definition of "on disk" instead of a copy each.
func OnDiskSnapshotForTest(cfg *config.Config) Snapshot {
	snap := Snapshot{Providers: map[string]Status{}}
	for _, m := range cfg.Models {
		if loc, err := cfg.ResolveLocation(m); err != nil || loc != config.LocationLocal {
			continue
		}
		if fam := Family(m.ProviderID); fam != "" {
			snap.Providers[fam] = StatusOK
		}
		e := Entry{ProviderID: m.ProviderID, ModelID: m.ID, ModelName: m.ModelName, Registered: true}
		if !RunningOnly(m.ProviderID) {
			e.Artifact, e.ArtifactKnown = m.ModelName, true
		}
		snap.Entries = append(snap.Entries, e)
	}
	return snap
}
