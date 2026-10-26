package cloudsync

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// OpenRouterModelsURL is the public model list the openrouter flow reads.
const OpenRouterModelsURL = "https://openrouter.ai/api/v1/models"

// APIPrice is what OpenRouter publishes for one model, per million tokens.
// A nil price is one the API did not report.
type APIPrice struct {
	Input, Cache, Output *float64
	// Skipped names the fields the API reported that are not prices: a
	// number that is negative (OpenRouter's -1 for "varies") or not finite,
	// or text that is not a number at all. A model with any is not
	// refreshed.
	Skipped []string
	// Rates are the other price levels of a model OpenRouter prices by time
	// of day, dearest first, each with the times of the UTC week it applies.
	// For such a model Input, Cache and Output are the level mainRate picks
	// (the dearest) and not the top-level prices, which are those of the
	// window in force when the list was fetched. Nil for every other model.
	Rates []Rate
	// Unusable says why a model's time-of-day entries could not be read; a
	// model with one is not refreshed. "" otherwise.
	Unusable string
}

// perMillion converts one per-token price. OpenRouter sends decimal strings;
// a JSON number is accepted too. The arithmetic is float(value) * 1_000_000,
// one float64 multiplication, so the same price always writes the same
// digits.
//
// skipped is true for a value that was sent and is not a price: negative,
// not finite, or text that does not parse as a number ("soon", "", a number
// too large to hold). JSON null, a missing key and any other JSON type are
// "not reported" (nil, false). The difference matters to PlanPrices: an
// input or output price that was not reported is cleared, and one that was
// skipped leaves the model alone with a warning. Unparsable text read as
// not reported would clear the registry's price without a word.
func perMillion(v any) (price *float64, skipped bool) {
	var text string
	switch x := v.(type) {
	case nil:
		return nil, false
	case string:
		text = strings.TrimSpace(x)
	case json.Number:
		text = x.String()
	default:
		return nil, false
	}
	f, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return nil, true
	}
	f *= 1_000_000
	if f < 0 || math.IsNaN(f) || math.IsInf(f, 0) {
		return nil, true
	}
	return &f, false
}

// ParseOpenRouter reads the body of OpenRouterModelsURL into prices by model
// id. A body that is not `{"data": [...]}` is an error; an item with no
// string id is skipped.
func ParseOpenRouter(body []byte) (map[string]APIPrice, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("OpenRouter response is not JSON: %w", err)
	}
	obj, ok := doc.(map[string]any)
	if !ok {
		return nil, errors.New("OpenRouter response is not a JSON object")
	}
	items, ok := obj["data"].([]any)
	if !ok {
		return nil, errors.New("OpenRouter response missing 'data' list")
	}
	out := make(map[string]APIPrice, len(items))
	for _, item := range items {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id, ok := entry["id"].(string)
		if !ok {
			continue
		}
		pricing := entry
		if raw, has := entry["pricing"]; has {
			pricing, _ = raw.(map[string]any)
		}
		var p APIPrice
		for _, f := range []struct {
			key  string
			into **float64
		}{{"prompt", &p.Input}, {"completion", &p.Output}, {"input_cache_read", &p.Cache}} {
			v, skipped := perMillion(pricing[f.key])
			*f.into = v
			if skipped {
				p.Skipped = append(p.Skipped, f.key)
			}
		}
		rates, unusable := parseSchedule(pricing)
		p.Unusable = unusable
		if len(rates) > 0 {
			// The schedule supplies these prices, so the top-level ones are
			// not read, not even to refuse them.
			main, supplied := rates[mainRate(rates)], []string{"prompt", "completion"}
			p.Input, p.Output = main.Input, main.Output
			if main.Cache != nil {
				p.Cache, supplied = main.Cache, append(supplied, "input_cache_read")
			}
			if p.Skipped = slices.DeleteFunc(p.Skipped, func(key string) bool { return slices.Contains(supplied, key) }); len(p.Skipped) == 0 {
				p.Skipped = nil
			}
			if rest := slices.Delete(rates, mainRate(rates), mainRate(rates)+1); len(rest) > 0 {
				p.Rates = rest
			}
		}
		out[id] = p
	}
	return out, nil
}

// OpenRouterPriced reports whether e takes its price from OpenRouter: a
// model whose provider_id is "openrouter", and no other. It is
// config.Config.OpenRouterPriced over registry rows, and both are pinned by
// docs/contracts/catalog-predicates.sample.toml. A provider row's
// openrouter_priced key, which overrode this until #322, is not read.
//
// The ollama flow addresses only rows whose provider_id is "ollama"
// (isOllamaCloud, findEntry), so no row is ever in both flows' scope:
// TestTheTwoFlowsNeverShareAModel.
func OpenRouterPriced(e Entry) bool { return e.ProviderID == config.OpenRouterProvider }

// PriceChange is one registry model OpenRouter has a price for.
type PriceChange struct {
	ModelID string
	// Before is nil when the entry had no cost table.
	Before *Cost
	After  Cost
	// Changed is false when the prices already match. The model is still
	// stamped: every matched model is.
	Changed bool
}

// PricePlan is what a price refresh changes.
type PricePlan struct {
	// Candidates counts the registry's OpenRouter-priced models.
	Candidates int
	Matched    []PriceChange
	Warnings   []string
	// RemovableCandidates counts the OpenRouter-priced models that are not
	// manual: the denominator of the mass-removal guard.
	RemovableCandidates int
	// Removals are the ids of registry models absent from OpenRouter's list.
	// Only absence removes: a model listed with no usable price still exists.
	Removals []string
	// Kept are manual models absent from the list; Graduations are manual
	// models on it (Apply sets their source to curated).
	Kept, Graduations []string
	// Withheld says why no removal was planned when the list looked like an
	// outage (fewer than MinListedForRemovals models); "" otherwise. It is
	// not part of Format: the command prints it.
	Withheld string
}

// MinListedForRemovals is the fewest models an OpenRouter list may hold for
// its absences to be read as removals. OpenRouter lists hundreds; a shorter
// list is a truncated or failed response, and treating it as complete would
// remove every model.
const MinListedForRemovals = 20

// EntriesGone is how many models the plan removes: the number the
// mass-removal guard weighs and its refusal prints.
func (p *PricePlan) EntriesGone() int { return len(p.Removals) }

// MassRemoval reports whether more than half the removable OpenRouter models
// would go, which is more likely a short or odd list than a catalog that
// shrank.
func (p *PricePlan) MassRemoval() bool { return p.EntriesGone()*2 > p.RemovableCandidates }

// RemovalDigest fingerprints the removals (the shared digest format over the
// sorted ids), or "" when there are none. `--yes` applies them only under
// the digest a reviewed dry run printed.
func (p *PricePlan) RemovalDigest() string {
	items := slices.Clone(p.Removals)
	sort.Strings(items)
	return digestOf(items)
}

// HasWork reports whether applying the plan would write anything. A matched
// model whose price is unchanged still gets its stamp.
func (p *PricePlan) HasWork() bool {
	return len(p.Matched)+len(p.Removals)+len(p.Graduations) > 0
}

// PlanPrices matches the registry's OpenRouter-priced models to api on
// model_name. Input and output prices are always overwritten: they are what
// the refresh measures. The cache price is overwritten only when the API
// reported one (it usually does not). The subscription is never touched,
// and of the time_prices rows only the flow's own (OpenRouterLabel): those
// are replaced or dropped wholesale, ones written by hand under that label
// included, and a row under any other label is never touched.
//
// It reads no clock, and api is a function of the response alone, a model
// priced by time of day included (parseSchedule): the same registry and
// response give the same plan at any hour. The command relies on that when
// it plans again under the registry lock and compares the text.
func PlanPrices(entries []Entry, api map[string]APIPrice) *PricePlan {
	plan := &PricePlan{}
	for _, e := range entries {
		if !OpenRouterPriced(e) {
			continue
		}
		plan.Candidates++
		if !e.Manual() {
			plan.RemovableCandidates++
		}
		price, ok := api[e.ModelName]
		if !ok {
			switch {
			case e.Manual():
				plan.Kept = append(plan.Kept, e.ID)
			case len(api) < MinListedForRemovals:
				plan.Withheld = fmt.Sprintf("openrouter: OpenRouter's list has %d models (fewer than %d), so no removals are planned", len(api), MinListedForRemovals)
				plan.Warnings = append(plan.Warnings, fmt.Sprintf("No OpenRouter match for %s (%s)", e.ID, e.ModelName))
			case suffixedListedModel(e.ModelName, api):
				// A routing variant (`:nitro`, `:floor`) or alias of a model the
				// list carries under its bare id: still served, so not removed. It
				// is kept rather than removed, and its price is refreshed from the
				// base the list carries, so a kept row cannot sit on a stale price
				// while its base moved. A suffix that routes at a rate of its own
				// (ownRateSuffix) is kept at the price it has: the base's would be
				// a wrong one, `:free` turned into a charge included.
				base, suffix, _ := strings.Cut(e.ModelName, ":")
				basePrice := api[base]
				usable := usablePrice(basePrice)
				own := ownRateSuffix(suffix)
				unpriced := e.Cost == nil || (e.Cost.Input == nil && e.Cost.Output == nil)
				var note string
				var change *PriceChange
				switch {
				case own && unpriced:
					note = fmt.Sprintf("left unpriced: :%s bills at its own rate, not %s's; set a price by hand", suffix, base)
				case own:
					note = fmt.Sprintf("kept at its current price: :%s bills at its own rate, not %s's", suffix, base)
				case !usable && unpriced:
					note = fmt.Sprintf("left unpriced: %s has no usable price; set a price by hand", base)
				case !usable:
					note = fmt.Sprintf("kept at its current price: %s has no usable price", base)
				default:
					var after Cost
					if e.Cost != nil {
						after = *e.Cost
					}
					after.Input, after.Output = basePrice.Input, basePrice.Output
					if basePrice.Cache != nil {
						after.Cache = basePrice.Cache
					}
					after.TimePrices = withOpenRouterRows(after.TimePrices, basePrice.Rates)
					change = &PriceChange{
						ModelID: e.ID, Before: e.Cost, After: after, Changed: !sameCost(e.Cost, after),
					}
					plan.Matched = append(plan.Matched, *change)
					// An unchanged price priced from the base is the ordinary
					// case and needs no line on every sync; the :online fee
					// caveat and a price that moved are worth one.
					switch {
					case suffix == "online":
						note = fmt.Sprintf("kept and priced from %s (:online adds a per-request web-search fee that price does not include)", base)
					case change.Changed:
						note = fmt.Sprintf("kept and priced from %s", base)
					}
				}
				if note != "" {
					plan.Warnings = append(plan.Warnings, fmt.Sprintf("No OpenRouter match for %s (%s); its base model is listed, so it is %s", e.ID, e.ModelName, note))
				}
			default:
				plan.Removals = append(plan.Removals, e.ID)
			}
			continue
		}
		if e.Manual() {
			// Listed is enough, whatever the price.
			plan.Graduations = append(plan.Graduations, e.ID)
		}
		if w, refused := priceWarning(e.ID, price); refused {
			plan.Warnings = append(plan.Warnings, w)
			continue
		}
		var after Cost
		if e.Cost != nil {
			after = *e.Cost
		}
		after.Input, after.Output = price.Input, price.Output
		if price.Cache != nil {
			after.Cache = price.Cache
		}
		after.TimePrices = withOpenRouterRows(after.TimePrices, price.Rates)
		plan.Matched = append(plan.Matched, PriceChange{
			ModelID: e.ID, Before: e.Cost, After: after, Changed: !sameCost(e.Cost, after),
		})
	}
	return plan
}

// usablePrice reports whether p carries a price wt can store: time-of-day
// pricing it can read (Unusable empty), no top-level price that is negative
// or not a number (Skipped empty), and at least one price. A listed model
// that fails it is left as it is, and so is a variant row whose base fails
// it; both paths of PlanPrices ask this one function, so a variant is never
// priced from a base the main path would warn about.
func usablePrice(p APIPrice) bool {
	return p.Unusable == "" && len(p.Skipped) == 0 && (p.Input != nil || p.Output != nil || p.Cache != nil)
}

// priceWarning is the warning for a listed model whose price usablePrice
// refuses, and false for one it accepts (so the refusal and its message cannot
// drift apart). The reasons are tested in this order: time-of-day
// pricing wt cannot read first (its top-level price is that of the window in
// force now, which stored would change with the hour the sync runs in, so the
// warning must not depend on the hour), then a price that is negative or not
// a number (OpenRouter gives -1 for one that varies; the whole entry is
// refused, since a model whose price varies has no per-token price to
// record), then no price at all.
func priceWarning(id string, p APIPrice) (string, bool) {
	switch {
	case usablePrice(p):
		return "", false
	case p.Unusable != "":
		return fmt.Sprintf("Could not use OpenRouter's time-of-day pricing for %s: %s", id, p.Unusable), true
	case len(p.Skipped) > 0:
		return fmt.Sprintf("Could not use OpenRouter's pricing for %s: its %s price is negative or not a number", id, strings.Join(p.Skipped, " and ")), true
	}
	return "No pricing data for " + id, true
}

// ownRateSuffix reports whether a `:variant` routing suffix is billed at a rate
// of its own, which the bare model's price does not give: `:free` costs
// nothing, `:nitro` and `:floor` route to the fastest or the cheapest
// provider and are priced as that provider is. The listing carries such a
// variant under its own id when it serves it; once it does not, nothing says
// what it would cost, so a kept row keeps the price it has.
func ownRateSuffix(suffix string) bool {
	switch suffix {
	case "free", "nitro", "floor":
		return true
	}
	return false
}

// suffixedListedModel reports whether name is a listed id plus a `:variant`
// routing suffix that the list does not carry as an id of its own.
func suffixedListedModel(name string, api map[string]APIPrice) bool {
	base, _, found := strings.Cut(name, ":")
	if !found {
		return false
	}
	_, ok := api[base]
	return ok
}

// Format prints the plan: each changed model as `id: old -> new`, then how
// many matched models already had the price. The command prints it for
// review and compares it with the re-plan made under the registry lock.
func (p *PricePlan) Format() string {
	var changed []string
	unchanged := 0
	for _, m := range p.Matched {
		if !m.Changed {
			unchanged++
			continue
		}
		changed = append(changed, fmt.Sprintf("  %s: %s -> %s", m.ModelID, formatCost(m.Before), formatCost(&m.After)))
	}
	lines := []string{fmt.Sprintf("openrouter.ai: %d OpenRouter-priced models in the registry (prices are input/cached/output per million tokens)", p.Candidates)}
	lines = append(lines, fmt.Sprintf("Price updates (%d):", len(changed)))
	lines = append(lines, changed...)
	lines = append(lines, fmt.Sprintf("Unchanged prices: %d", unchanged))
	if len(p.Removals) > 0 {
		lines = append(lines, idBlock(fmt.Sprintf("Registry removals — not on openrouter.ai/api/v1/models (%d):", len(p.Removals)), p.Removals)...)
		// An alias or a redirect id has no `:` to tell it from a model that
		// is gone, so it lands here like one. The hint is not part of the
		// digest (RemovalDigest reads the ids), so the approval is unchanged.
		lines = append(lines, "Note: a model OpenRouter serves under another name (an alias or a redirect id) is listed here too; check each before approving.")
		lines = append(lines, digestLine(p.RemovalDigest()))
	}
	lines = append(lines, graduatedLines(p.Graduations)...)
	lines = append(lines, keptLines("openrouter.ai", p.Kept)...)
	for _, w := range p.Warnings {
		lines = append(lines, "warning: "+w)
	}
	return strings.Join(lines, "\n")
}
