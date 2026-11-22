package litellm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
)

// DBPlan is what a database sync changes: rows to create, rows to replace
// (each carries the desired row) and ids of wt-managed rows to remove.
type DBPlan struct {
	Add, Update []DBModel
	Delete      []string
}

// Empty reports whether the plan changes nothing.
func (p DBPlan) Empty() bool { return len(p.Add)+len(p.Update)+len(p.Delete) == 0 }

// PlanDB compares the desired rows with the database's, matching by
// DBModel.ID. Only current rows whose model_info carries wt_managed: true
// are wt's: any other row (the LiteLLM UI's, another owner's) is never
// updated or deleted. A desired row is equal to a current one when its
// model_name and litellm_params match after a JSON round trip (YAML's int
// 4096 and the API's float64 4096 compare equal; key order never matters);
// model_info is not compared. Every list is sorted by id. Pure.
func PlanDB(desired, current []DBModel) DBPlan {
	managed := make(map[string]DBModel, len(current))
	for _, m := range current {
		if v, _ := m.Info[ManagedKey].(bool); v {
			managed[m.ID] = m
		}
	}
	var p DBPlan
	want := make(map[string]bool, len(desired))
	for _, d := range desired {
		want[d.ID] = true
		cur, ok := managed[d.ID]
		switch {
		case !ok:
			p.Add = append(p.Add, d)
		case !sameDBRoute(d, cur):
			p.Update = append(p.Update, d)
		}
	}
	for id := range managed {
		if !want[id] {
			p.Delete = append(p.Delete, id)
		}
	}
	byID := func(a, b DBModel) int {
		switch {
		case a.ID < b.ID:
			return -1
		case a.ID > b.ID:
			return 1
		}
		return 0
	}
	slices.SortFunc(p.Add, byID)
	slices.SortFunc(p.Update, byID)
	slices.Sort(p.Delete)
	return p
}

// sameDBRoute reports whether two rows route the same: equal model_name and
// equal litellm_params once both are normalized through JSON.
func sameDBRoute(a, b DBModel) bool {
	return a.ModelName == b.ModelName && reflect.DeepEqual(jsonNormal(a.Params), jsonNormal(b.Params))
}

// jsonNormal returns v as encoding/json would decode it (numbers float64,
// slices []any, maps map[string]any). A value that cannot be encoded is
// returned as is, so it compares unequal to anything decoded.
func jsonNormal(v map[string]any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return v
	}
	return out
}

// ApplyDB carries out p: deletes first, then updates, then adds. Every item
// is attempted; the failures are returned joined, each naming its id.
//
// An Update is a Delete then an Add of the same id: LiteLLM's update endpoint
// is not idempotent for params. If the Delete succeeds and the Add fails, the
// route is missing until the next sync, and the error says so.
//
// LiteLLM answers an Add of an id already present with a generic HTTP 500
// (not an "already exists" message), so a failed Add re-lists the database:
// the id counts as converged (another sync got there first) only when the
// stored row has the same model_name and params; otherwise the Add's error
// is returned. A Delete of a row already gone is nil (AdminClient.Delete).
func (c *AdminClient) ApplyDB(ctx context.Context, p DBPlan) (added, updated, deleted int, err error) {
	var errs []error
	for _, id := range p.Delete {
		if e := c.Delete(ctx, id); e != nil {
			errs = append(errs, fmt.Errorf("litellm: delete database model %s: %w", id, e))
			continue
		}
		deleted++
	}
	for _, m := range p.Update {
		if e := c.Delete(ctx, m.ID); e != nil {
			errs = append(errs, fmt.Errorf("litellm: update database model %s: delete: %w", m.ID, e))
			continue
		}
		if e := c.addConverged(ctx, m); e != nil {
			errs = append(errs, fmt.Errorf("litellm: update database model %s: deleted but not re-added, its route is missing until the next sync: %w", m.ID, e))
			continue
		}
		updated++
	}
	for _, m := range p.Add {
		if e := c.addConverged(ctx, m); e != nil {
			errs = append(errs, fmt.Errorf("litellm: add database model %s: %w", m.ID, e))
			continue
		}
		added++
	}
	return added, updated, deleted, errors.Join(errs...)
}

// addConverged adds m; when the Add fails it re-lists and returns nil only
// if an equal row with m's id is now stored.
func (c *AdminClient) addConverged(ctx context.Context, m DBModel) error {
	addErr := c.Add(ctx, m)
	if addErr == nil {
		return nil
	}
	rows, listErr := c.List(ctx)
	if listErr != nil {
		return errors.Join(addErr, fmt.Errorf("re-list after the failed add: %w", listErr))
	}
	for _, r := range rows {
		if r.ID == m.ID && sameDBRoute(m, r) {
			return nil
		}
	}
	return addErr
}
