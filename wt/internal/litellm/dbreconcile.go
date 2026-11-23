package litellm

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ParamsHashKey is the model_info key ApplyDB stamps on every row it adds:
// routeHash of the row as wt sent it (a resolved api_key enters it only as
// an HMAC under the master key). LiteLLM's /model/info strips api_key
// (and masks other sensitive-looking values) from litellm_params, so the
// params it returns cannot be compared with the desired ones; the hash can.
const ParamsHashKey = "wt_params_sha"

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
// and whose id starts with DBIDPrefix are wt's: any other row (the LiteLLM
// UI's, another owner's, a marked row under a foreign id that ApplyDB would
// refuse to touch) is never updated or deleted. A managed row is equal to the desired one when its
// model_info.wt_params_sha equals routeHash(desired, hashKey); a managed row with no
// hash is treated as changed. hashKey is the proxy master key the hash keys
// a resolved api_key with (routeHash; the AdminClient's Key), so a plan and
// the ApplyDB that stamps its rows must use the same one. Every list is
// sorted by id. Pure.
func PlanDB(desired, current []DBModel, hashKey string) DBPlan {
	managed := make(map[string]DBModel, len(current))
	for _, m := range current {
		if isManagedDB(m) && strings.HasPrefix(m.ID, DBIDPrefix) {
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
		case !hashMatches(cur, d, hashKey):
			p.Update = append(p.Update, d)
		}
	}
	for id := range managed {
		if !want[id] {
			p.Delete = append(p.Delete, id)
		}
	}
	byID := func(a, b DBModel) int { return strings.Compare(a.ID, b.ID) }
	slices.SortFunc(p.Add, byID)
	slices.SortFunc(p.Update, byID)
	slices.Sort(p.Delete)
	return p
}

func isManagedDB(m DBModel) bool {
	v, _ := m.Info[ManagedKey].(bool)
	return v
}

// hashMatches reports whether stored carries the hash of want's route
// (routeHash under hashKey).
func hashMatches(stored, want DBModel, hashKey string) bool {
	h := routeHash(want, hashKey)
	got, _ := stored.Info[ParamsHashKey].(string)
	return h != "" && got == h
}

// routeHash is the sha256 hex of m's model_name and litellm_params as
// canonical JSON: values normalized through a JSON round trip (YAML's int
// 4096 and the API's float64 4096 hash the same) and map keys sorted (Go's
// encoder sorts them). It is "" when the params cannot be encoded, which
// never matches a stored hash.
//
// The hash is stored unmasked in model_info, so it must never cover a
// resolved secret: an api_key that is not an os.environ/ reference (rec's
// key, read from `rec auth token`) is replaced, before hashing, by its
// HMAC-SHA256 under hashKey (the proxy master key), hex. A master-key
// rotation therefore changes the hash of keyed rows: the next sync replaces
// them once. An os.environ/ reference is hashed as written.
func routeHash(m DBModel, hashKey string) string {
	params := make(map[string]any, len(m.Params))
	for k, v := range m.Params {
		params[k] = v
	}
	if v, ok := params["api_key"].(string); ok && !strings.HasPrefix(v, "os.environ/") {
		mac := hmac.New(sha256.New, []byte(hashKey))
		mac.Write([]byte(v))
		params["api_key"] = "hmac-sha256:" + hex.EncodeToString(mac.Sum(nil))
	}
	b, err := json.Marshal(params)
	if err != nil {
		return ""
	}
	var norm any
	if err := json.Unmarshal(b, &norm); err != nil {
		return ""
	}
	b, err = json.Marshal(map[string]any{"model_name": m.ModelName, "litellm_params": norm})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ApplyDB carries out p: deletes first, then updates, then adds. Every item
// is attempted; the failures are returned joined, each naming its id. An id
// in Delete or Update without DBIDPrefix is refused with no HTTP call: wt
// touches no row it did not name.
//
// Every Add stamps model_info.wt_params_sha (ParamsHashKey) on a copy of the
// row's Info. An Update is a Delete then an Add of the same id: LiteLLM's
// update endpoint is not idempotent for params. If the Delete succeeds and
// the Add fails, the route is missing until the next sync, and the error
// says so.
//
// A transport error (no HTTP answer: the proxy is down or the context ended)
// stops the run at once: the error is returned with the counts of what was
// done and no later item is attempted, so a dead proxy costs one timeout,
// not one per item.
//
// LiteLLM answers an Add of an id already present with a generic HTTP 500
// (not an "already exists" message), so a failed Add re-lists the database:
// the id counts as converged (another sync got there first) only when the
// stored row is wt-managed and carries the same hash; otherwise the Add's
// error is returned. A Delete of a row already gone is nil
// (AdminClient.Delete).
func (c *AdminClient) ApplyDB(ctx context.Context, p DBPlan) (added, updated, deleted int, err error) {
	var errs []error
	for _, id := range p.Delete {
		if !strings.HasPrefix(id, DBIDPrefix) {
			errs = append(errs, fmt.Errorf("litellm: refusing to delete database model %q: its id does not start with %q", id, DBIDPrefix))
			continue
		}
		if e := c.Delete(ctx, id); e != nil {
			errs = append(errs, fmt.Errorf("litellm: delete database model %s: %w", id, e))
			if stopApply(ctx, e) {
				return added, updated, deleted, errors.Join(errs...)
			}
			continue
		}
		deleted++
	}
	for _, m := range p.Update {
		if !strings.HasPrefix(m.ID, DBIDPrefix) {
			errs = append(errs, fmt.Errorf("litellm: refusing to update database model %q: its id does not start with %q", m.ID, DBIDPrefix))
			continue
		}
		if e := c.Delete(ctx, m.ID); e != nil {
			errs = append(errs, fmt.Errorf("litellm: update database model %s: delete: %w", m.ID, e))
			if stopApply(ctx, e) {
				return added, updated, deleted, errors.Join(errs...)
			}
			continue
		}
		if e := c.addConverged(ctx, m, isManagedDB); e != nil {
			errs = append(errs, fmt.Errorf("litellm: update database model %s: deleted but not re-added, its route is missing until the next sync: %w", m.ID, e))
			if stopApply(ctx, e) {
				return added, updated, deleted, errors.Join(errs...)
			}
			continue
		}
		updated++
	}
	for _, m := range p.Add {
		if e := c.addConverged(ctx, m, isManagedDB); e != nil {
			errs = append(errs, fmt.Errorf("litellm: add database model %s: %w", m.ID, e))
			if stopApply(ctx, e) {
				return added, updated, deleted, errors.Join(errs...)
			}
			continue
		}
		added++
	}
	return added, updated, deleted, errors.Join(errs...)
}

// stopApply reports whether ApplyDB must give up after err: the proxy gave
// no answer, or the context has ended.
func stopApply(ctx context.Context, err error) bool {
	return isTransport(err) || ctx.Err() != nil
}

// addConverged adds m with its hash stamped; when the Add fails it re-lists
// and returns nil only if a row with m's id and hash that owned accepts is
// now stored. owned is the caller's ownership test: isManagedDB for a model
// row (ApplyDB), IsAliasDB for an alias row (ApplyAliasDB), so neither path
// counts the other's row as its own.
func (c *AdminClient) addConverged(ctx context.Context, m DBModel, owned func(DBModel) bool) error {
	info := make(map[string]any, len(m.Info)+1)
	for k, v := range m.Info {
		info[k] = v
	}
	info[ParamsHashKey] = routeHash(m, c.Key)
	stamped := m
	stamped.Info = info
	addErr := c.Add(ctx, stamped)
	if addErr == nil {
		return nil
	}
	// No answer at all: a re-list would only wait for the same dead proxy.
	if stopApply(ctx, addErr) {
		return addErr
	}
	rows, listErr := c.List(ctx)
	if listErr != nil {
		return errors.Join(addErr, fmt.Errorf("re-list after the failed add: %w", listErr))
	}
	for _, r := range rows {
		if r.ID == m.ID && owned(r) && hashMatches(r, m, c.Key) {
			return nil
		}
	}
	return addErr
}
