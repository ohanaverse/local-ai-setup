package modeladmin

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tomlw"
)

// AddRequest is one model to add to the registry.
type AddRequest struct {
	ProviderID string
	// ModelName is the name as the provider lists it: an ollama tag, an omlx
	// model directory's name, an mtplx org/name, a cloud provider's model id.
	ModelName string
	// ID overrides the derived id (DeriveID) when not empty.
	ID string
	Fields
	// ModelInfo is written as the row's model_info table when not empty: the
	// capabilities looked up before the write (OllamaCapabilities).
	ModelInfo map[string]any
}

// AddResult is what an add did.
type AddResult struct {
	ID string
	// ProvidersAdded are the provider rows seeding added in the same write;
	// ProvidersUnseeded the providers a configured agent lists that wt has no
	// default row for (config.SeedRegistryDefaults).
	ProvidersAdded    []string
	ProvidersUnseeded []string
	Warnings          []string
}

// addPlan is an add that passed every check that needs no registry: the
// request with its text trimmed, the id the row gets, and the row's keys.
type addPlan struct {
	req AddRequest
	id  string
	set map[string]any
}

// planAdd checks a request by itself — what is required, each field's value,
// the subscription rule, the shape of a given id — and works out the row.
func planAdd(req AddRequest) (addPlan, error) {
	req.ProviderID, req.ModelName, req.ID = strings.TrimSpace(req.ProviderID), strings.TrimSpace(req.ModelName), strings.TrimSpace(req.ID)
	switch {
	case req.ProviderID == "":
		return addPlan{}, fieldErr(FieldProvider, "a provider is required")
	case req.ModelName == "":
		return addPlan{}, fieldErr(FieldName, "a model name is required")
	case req.Family == nil:
		return addPlan{}, fieldErr(FieldFamily, "family is required")
	case localmodels.RunningOnly(req.ProviderID):
		return addPlan{}, fieldErr(FieldProvider, "an mlx_lm_server model is a target+draft pairing, which this command cannot add yet: add it to %s by hand", config.RegistryPath())
	}
	if err := checkID(req.ID); err != nil {
		return addPlan{}, err
	}
	set, _, err := req.Fields.patch()
	if err != nil {
		return addPlan{}, err
	}
	_, hasPrice := set["cost.subscription_price"]
	period, _ := set["cost.subscription_period"].(string)
	if err := checkSubscription(hasPrice, period); err != nil {
		return addPlan{}, err
	}
	if _, ok := set["tags"]; !ok {
		set["tags"] = []string{} // every row modelman writes has the key
	}
	if len(req.ModelInfo) > 0 {
		set["model_info"] = req.ModelInfo
	}
	id := req.ID
	if id == "" {
		id = DeriveID(req.ProviderID, req.ModelName)
	}
	return addPlan{req: req, id: id, set: set}, nil
}

// checkID refuses a given id that is not <provider>/<name>: a part on each
// side of the first "/", and no space or control character anywhere. The
// rest of wt takes that shape for granted — `wt stop <arg>` reads an argument
// with no "/" as a provider, and an id with a space cannot be passed to -M
// unquoted. "" (derive the id) is fine.
func checkID(id string) error {
	if id == "" {
		return nil
	}
	provider, name, ok := strings.Cut(id, "/")
	bad := strings.ContainsFunc(id, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })
	if !ok || provider == "" || name == "" || bad {
		return fieldErr(FieldID, "an id is <provider>/<name> with no spaces, got %q", id)
	}
	return nil
}

// CheckAdd reports what Add would refuse without reading the registry: a
// missing provider, name or family, a value that cannot be used, a
// subscription price with no period, an id of the wrong shape. Add makes the
// same checks; this is for a caller that has something slow to do before the
// write (the `ollama show` lookup), so that a request that is going to be
// refused anyway does not wait on it first. A taken id and an artifact that
// is already registered need the registry, and are Add's to refuse.
func CheckAdd(req AddRequest) error {
	_, err := planAdd(req)
	return err
}

// Add appends one model row and seeds any missing default provider row, in
// one locked registry write. It runs no route sync: the caller does, once.
// The registry is created when it is missing.
func Add(req AddRequest, env config.SeedEnv) (AddResult, error) {
	plan, err := planAdd(req)
	if err != nil {
		return AddResult{}, err
	}
	req, id, set := plan.req, plan.id, plan.set
	res := AddResult{ID: id}
	_, err = config.UpdateRegistry(func(d *config.RegistryDoc) error {
		// Everything assigned here is assigned afresh: apply may run again.
		res = AddResult{ID: id}
		if other := sameArtifact(d.Models(), req.ProviderID, req.ModelName); other != "" {
			return fieldErr(FieldName, "model %q already registers %s on %s; edit that one (wt model edit %s)", other, req.ModelName, req.ProviderID, other)
		}
		if err := d.AddModel(map[string]any{
			"id": id, "family": set["family"], "provider_id": req.ProviderID, "model_name": req.ModelName,
		}); err != nil {
			return err
		}
		rest := map[string]any{}
		for k, v := range set {
			if k != "family" {
				rest[k] = v
			}
		}
		if err := d.PatchModel(id, rest, nil); err != nil {
			return err
		}
		// After the model is in: seeding adds the default row of a provider
		// a model references.
		var err error
		res.ProvidersAdded, res.ProvidersUnseeded, err = config.SeedRegistryDefaults(d, env)
		if err != nil {
			return err
		}
		res.Warnings = providerWarnings(d.Providers(), req.ProviderID)
		return nil
	})
	if err != nil {
		return AddResult{}, describe(err, req.ProviderID)
	}
	return res, nil
}

// sameArtifact returns the id of a model row that already stands for the
// same local artifact: the same provider family and model_name. The
// inventory gives an artifact to the first row that matches it, so a second
// row would read as "missing" for ever. "" for a provider with no probe (a
// cloud provider may list one model under two ids, at two prices).
func sameArtifact(models []*tomlw.Table, providerID, modelName string) string {
	family := localmodels.Family(providerID)
	if family == "" {
		return ""
	}
	for _, m := range models {
		if localmodels.Family(tableString(m, "provider_id")) == family && tableString(m, "model_name") == modelName {
			return tableString(m, "id")
		}
	}
	return ""
}

// providerWarnings names what is wrong with the provider row a model was
// just added under, when wt can route the model only once it is repaired.
func providerWarnings(providers []*tomlw.Table, providerID string) []string {
	for _, p := range providers {
		if tableString(p, "id") != providerID {
			continue
		}
		auth, _ := p.Get("auth")
		at, _ := auth.(*tomlw.Table)
		if at != nil && tableString(at, "type") == "api_key" && tableString(at, "secret_ref") == "" {
			return []string{fmt.Sprintf("provider %q has no auth.secret_ref, so its LiteLLM routes carry an empty api_key: set it in %s", providerID, config.RegistryPath())}
		}
	}
	return nil
}

func mustGet(t *tomlw.Table, key string) any {
	v, _ := t.Get(key)
	return v
}

func tableString(t *tomlw.Table, key string) string {
	v, _ := t.Get(key)
	s, _ := v.(string)
	return s
}

// describe adds what a user can do about a registry refusal that names a
// provider with no row, for an add and for an edit of a row that has the gap
// (the edit cannot repair it: provider_id is not editable). Seeding added none: a model's reference seeds only
// the providers wt has a default row for, and wt has no command that writes
// any other provider row.
func describe(err error, providerID string) error {
	if errors.Is(err, config.ErrNoProviderRow) {
		return fmt.Errorf("%w: add a [[providers]] block for %q to %s and run this again (wt adds a default row by itself only for ollama, omlx, mtplx, mlx_lm_server and openrouter)",
			err, providerID, config.RegistryPath())
	}
	return err
}

// Edit patches the given fields of model id in one locked registry write and
// reports whether the file changed. The id, the provider and the model name
// are not editable: usage history, rotation and launch profiles key on the
// id, and the inventory matches a row to its weights by provider and name.
// It does not stamp pricing_updated_at; `wt cloud-sync` owns that date.
//
// Two things about the row's cost table follow from an edit of a price or of
// a subscription field, and only from one: a table still in modelman's old
// layout is moved to the current one (legacyCost), and a table the edit
// emptied goes with its last key, so a model whose prices were all cleared
// reads like one that never had any.
func Edit(id string, f Fields) (changed bool, err error) {
	given, givenUnset, err := f.patch()
	if err != nil {
		return false, err
	}
	providerID := ""
	changed, err = config.UpdateRegistry(func(d *config.RegistryDoc) error {
		// Copies: apply may run again, and each run starts from what the
		// user gave.
		set, unset := maps.Clone(given), slices.Clone(givenUnset)
		cost := tomlw.NewTable()
		found := false
		for _, m := range d.Models() {
			if tableString(m, "id") == id {
				found = true
				providerID = tableString(m, "provider_id")
				if t, ok := mustGet(m, "cost").(*tomlw.Table); ok {
					cost = t
				}
			}
		}
		// Before the rules over the row's cost table: an id that is not there
		// has no row to judge, and "a subscription price needs a period" is
		// the wrong thing to say about a mistyped id.
		if !found {
			return fmt.Errorf("%w: %q", config.ErrModelNotFound, id)
		}
		costKey := func(k string) bool { return strings.HasPrefix(k, "cost.") }
		setsCost := func() bool { return slices.ContainsFunc(slices.Collect(maps.Keys(set)), costKey) }
		if setsCost() || slices.ContainsFunc(unset, costKey) {
			unset = append(unset, legacyCost(cost, set, unset)...)
			// The subscription rule, over the row as the patch leaves it.
			after := cost.Clone()
			for _, k := range unset {
				after.Delete(strings.TrimPrefix(k, "cost."))
			}
			hasPrice, period := after.Has("subscription_price"), tableString(after, "subscription_period")
			if _, ok := set["cost.subscription_price"]; ok {
				hasPrice = true
			}
			if p, ok := set["cost.subscription_period"].(string); ok {
				period = p
			}
			if err := checkSubscription(hasPrice, period); err != nil {
				return err
			}
			if cost.Len() > 0 && after.Len() == 0 && !setsCost() {
				unset = append(unset, "cost")
			}
		}
		return d.PatchModel(id, set, unset)
	})
	if err != nil {
		return false, describe(err, providerID)
	}
	return changed, nil
}

// legacyCost moves a cost table in modelman's old layout (cost.kind, with
// the price under price_per_million_tokens or price_per_period and the
// period under period) to the current one, as modelman's own loader reads it:
// a per-token price is the input and the output price, a subscription's
// price and period take their current names. It adds the carried values to
// set — never over a key this edit sets or clears — and returns the old keys
// to delete. modelman reads a table that has `kind` by its old keys alone, so
// a current key written beside them would be a price wt shows and modelman
// ignores. Nothing for a table that is not in the old layout.
func legacyCost(cost *tomlw.Table, set map[string]any, unset []string) []string {
	kind, isLegacy := cost.Get("kind")
	if !isLegacy {
		return nil
	}
	carry := func(from string, to ...string) {
		v, ok := cost.Get(from)
		if !ok {
			return
		}
		for _, key := range to {
			if _, given := set[key]; !given && !slices.Contains(unset, key) {
				set[key] = v
			}
		}
	}
	switch kind {
	case "per_token":
		carry("price_per_million_tokens", "cost.input_price_per_million", "cost.output_price_per_million")
	case "subscription":
		carry("price_per_period", "cost.subscription_price")
		carry("period", "cost.subscription_period")
	}
	return []string{"cost.kind", "cost.price_per_million_tokens", "cost.price_per_period", "cost.period"}
}

// Remove deletes the model rows ids from the registry in one locked write:
// all of them, or — when one is not there — none. It touches nothing else:
// no weights, no [[families]] entry, no route (the caller syncs).
func Remove(ids []string) error {
	_, err := config.UpdateRegistry(func(d *config.RegistryDoc) error {
		for _, id := range ids {
			if _, err := d.RemoveModel(id); err != nil {
				return err
			}
		}
		return nil
	})
	return err
}
