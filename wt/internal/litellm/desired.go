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
// without a config path). Recheck is forced off, as in PlanSync.
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
	plan, _ := planSync(cfg, f, local, o)
	var ids []string
	for _, id := range f.RoutedIDs() {
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
