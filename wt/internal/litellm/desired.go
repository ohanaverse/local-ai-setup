package litellm

import (
	"slices"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// DesiredIDs lists the model_names config.yaml would hold once Sync(cfg,
// local, o) had run: the rows it leaves (hand-written ones and frozen ones
// included) minus the ones the plan removes, plus the ones it adds. It reads
// config.yaml and writes nothing, and it decides through planSync, the one
// function Sync and PlanSync decide with, so it cannot drift from them. It
// refuses what Sync refuses (a missing or invalid file, a registry redirected
// without a config path). Recheck is forced off, as in PlanSync. With
// Options.Admin the database's wt routes are read (List only) and counted as
// routes; a listing that fails leaves them out.
func DesiredIDs(cfg *config.Config, local []config.Model, o Options) ([]string, error) {
	o.Recheck = nil
	f, err := Open(o.path())
	if err != nil {
		return nil, err
	}
	if err := checkRegistryPairing(o); err != nil {
		return nil, err
	}
	if err := f.checkModelList(); err != nil {
		return nil, err
	}
	// With Options.Admin the database's wt rows are routes too (#598): they
	// count for the plan and are listed beside config.yaml's.
	db, dbRows := readOnlyDB(o, f)
	defer db.close()
	plan, _ := planSyncWithDB(cfg, f, local, o, dbRows)
	var ids []string
	held := f.RoutedIDs()
	for _, r := range dbRows {
		held = append(held, r.ID)
	}
	for _, id := range held {
		if !slices.Contains(plan.Remove, id) && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	for _, id := range plan.Add {
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	return ids, nil
}
