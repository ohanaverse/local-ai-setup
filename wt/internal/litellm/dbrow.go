package litellm

import (
	"errors"
	"fmt"

	"gopkg.in/yaml.v3"
)

// DBID is the model_info.id wt gives the database row of a registry model.
func DBID(registryID string) string { return DBIDPrefix + registryID }

// DBIDPrefix starts every database row id wt writes; ApplyDB never deletes
// or replaces a row whose id lacks it.
const DBIDPrefix = "wt:"

// DBModelFromRow converts a model_list row (as BuildEntry builds it) into a
// database payload: litellm_params are copied whole (os.environ/X references
// stay literal), the row's model_info is kept with wt_managed forced true,
// and the id is DBID(model_name), held in DBModel.ID rather than Info.
func DBModelFromRow(row *yaml.Node) (DBModel, error) {
	var r struct {
		ModelName string         `yaml:"model_name"`
		Params    map[string]any `yaml:"litellm_params"`
		Info      map[string]any `yaml:"model_info"`
	}
	if row == nil {
		return DBModel{}, errors.New("litellm: nil row")
	}
	if err := row.Decode(&r); err != nil {
		return DBModel{}, fmt.Errorf("litellm: decode row: %w", err)
	}
	if r.ModelName == "" {
		return DBModel{}, errors.New("litellm: row has no model_name")
	}
	info := make(map[string]any, len(r.Info)+1)
	for k, v := range r.Info {
		if k != "id" {
			info[k] = v
		}
	}
	info[ManagedKey] = true
	if r.Params == nil {
		r.Params = map[string]any{}
	}
	return DBModel{ID: DBID(r.ModelName), ModelName: r.ModelName, Params: r.Params, Info: info}, nil
}
