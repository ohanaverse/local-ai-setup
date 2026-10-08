package cloudsync

import (
	"slices"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// Stamp formats a pricing_updated_at value the way modelman writes it: UTC,
// to the second, with a numeric offset.
func Stamp(now time.Time) string { return now.UTC().Format("2006-01-02T15:04:05+00:00") }

// costPatch is the PatchModel arguments that give a row the prices of c: a
// price that is nil is deleted, and the time_prices key goes when no row is
// left. A subscription is only ever set, never cleared: no flow changes one.
func costPatch(c Cost) (set map[string]any, unset []string) {
	set = map[string]any{}
	for _, kv := range []struct {
		key string
		v   *float64
	}{
		{"cost.input_price_per_million", c.Input},
		{"cost.cache_price_per_million", c.Cache},
		{"cost.output_price_per_million", c.Output},
		{"cost.subscription_price", c.SubscriptionPrice},
	} {
		switch {
		case kv.v != nil:
			set[kv.key] = *kv.v
		case kv.key != "cost.subscription_price":
			unset = append(unset, kv.key)
		}
	}
	if c.SubscriptionPeriod != nil {
		set["cost.subscription_period"] = *c.SubscriptionPeriod
	}
	if len(c.TimePrices) == 0 {
		unset = append(unset, "cost.time_prices")
	} else {
		rows := make([]any, len(c.TimePrices))
		for i, row := range c.TimePrices {
			rows[i] = row
		}
		set["cost.time_prices"] = rows
	}
	return set, unset
}

// CatalogApplied is what Apply did to the registry and what is left to do in
// ollama.
type CatalogApplied struct {
	Updated, Added, Removed int
	// Pulls are the tags to `ollama pull`, in plan order.
	Pulls []string
	// Removes are the tags to `ollama rm`: first those of the entries Apply
	// removed that were pulled, then the strays. A tag a remaining ollama
	// entry still names is never among them, and neither is a tag that is
	// not a cloud tag: this flow never removes local weights.
	Removes []string
}

// Changed reports whether Apply changed a price or the set of models, which
// is when LiteLLM's routes need a sync.
func (a CatalogApplied) Changed() bool { return a.Updated+a.Added+a.Removed > 0 }

// Apply makes the plan's registry changes on doc: prices and the catalog
// name on updated entries, the new entries, and the removals. It runs no
// command and reads no clock, so it is safe as (part of) a
// config.UpdateRegistry apply function. pulled is the `ollama list` the plan
// was made from.
//
// Every row it changes or adds is stamped with now in pricing_updated_at.
// An entry it removes is gone from the registry before its tag is removed
// from ollama: if the `ollama rm` then fails, the tag is a stray the next
// run removes.
func (p *CatalogPlan) Apply(doc *config.RegistryDoc, pulled []string, now time.Time) (CatalogApplied, error) {
	var done CatalogApplied
	stamp := Stamp(now)
	for _, u := range p.Updates {
		set, unset := costPatch(u.After)
		set[CatalogNameKey] = u.CatalogName
		set["pricing_updated_at"] = stamp
		if err := doc.PatchModel(u.ModelID, set, unset); err != nil {
			return CatalogApplied{}, err
		}
		done.Updated++
	}
	for _, a := range p.Additions {
		set, unset := costPatch(a.Cost)
		set[CatalogNameKey] = a.CatalogName
		set["pricing_updated_at"] = stamp
		if a.CloneOf != "" {
			if err := doc.CloneModel(a.CloneOf, map[string]any{
				"id": a.ID, "model_name": a.ModelName, "location": "cloud", "source": "curated",
			}); err != nil {
				return CatalogApplied{}, err
			}
		} else if err := doc.AddModel(map[string]any{
			"id": a.ID, "family": a.Family, "provider_id": ollamaProvider, "model_name": a.ModelName,
			"location": "cloud", "source": "curated", "tags": []string{},
		}); err != nil {
			return CatalogApplied{}, err
		}
		if err := doc.PatchModel(a.ID, set, unset); err != nil {
			return CatalogApplied{}, err
		}
		done.Added++
	}
	var removedTags []string
	for _, id := range p.Removals {
		row, err := doc.RemoveModel(id)
		if err != nil {
			return CatalogApplied{}, err
		}
		removedTags = append(removedTags, str(row, "model_name"))
		done.Removed++
	}

	named := map[string]bool{}
	for _, e := range Entries(doc.Models()) {
		if e.ProviderID == ollamaProvider {
			named[e.ModelName] = true
		}
	}
	for _, tag := range removedTags {
		// Only a cloud tag: a removed entry that was marked cloud over a
		// local tag (the plan warned) must not cost anyone their weights.
		if IsCloudTag(tag) && slices.Contains(pulled, tag) && !named[tag] && !slices.Contains(done.Removes, tag) {
			done.Removes = append(done.Removes, tag)
		}
	}
	for _, tag := range p.StrayTags {
		if !named[tag] && !slices.Contains(done.Removes, tag) {
			done.Removes = append(done.Removes, tag)
		}
	}
	for _, pull := range p.Pulls {
		done.Pulls = append(done.Pulls, pull.Tag)
	}
	return done, nil
}

// PricesApplied is what PricePlan.Apply did.
type PricesApplied struct {
	// Stamped counts the models whose pricing_updated_at was set: every
	// matched one. Changed counts those whose price moved, which is when
	// LiteLLM's routes need a sync.
	Stamped, Changed int
}

// Apply writes the plan's prices to doc and stamps every matched model with
// now, changed or not: the stale-price notice reads the newest stamp, so a
// refresh that found every price current must still leave its mark. A price
// that did not change is not rewritten (config.RegistryDoc.PatchModel skips a
// value that is already there), so an integer stays an integer. Like
// CatalogPlan.Apply it is safe inside config.UpdateRegistry.
func (p *PricePlan) Apply(doc *config.RegistryDoc, now time.Time) (PricesApplied, error) {
	var done PricesApplied
	stamp := Stamp(now)
	for _, m := range p.Matched {
		set, unset := costPatch(m.After)
		set["pricing_updated_at"] = stamp
		if err := doc.PatchModel(m.ModelID, set, unset); err != nil {
			return PricesApplied{}, err
		}
		done.Stamped++
		if m.Changed {
			done.Changed++
		}
	}
	return done, nil
}
