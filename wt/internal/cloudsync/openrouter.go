package cloudsync

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
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
		out[id] = p
	}
	return out, nil
}

// OpenRouterProvider is the provider_id of the models the openrouter flow
// refreshes.
const OpenRouterProvider = "openrouter"

// OpenRouterPriced reports whether e takes its price from OpenRouter: a
// model whose provider_id is "openrouter", and no other. It is
// config.Config.OpenRouterPriced over registry rows, and both are pinned by
// docs/contracts/catalog-predicates.sample.toml. A provider row's
// openrouter_priced key, which overrode this until #322, is not read.
//
// The ollama flow addresses only rows whose provider_id is "ollama"
// (isOllamaCloud, findEntry), so no row is ever in both flows' scope:
// TestTheTwoFlowsNeverShareAModel.
func OpenRouterPriced(e Entry) bool { return e.ProviderID == OpenRouterProvider }

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
}

// HasWork reports whether applying the plan would write anything. A matched
// model whose price is unchanged still gets its stamp.
func (p *PricePlan) HasWork() bool { return len(p.Matched) > 0 }

// PlanPrices matches the registry's OpenRouter-priced models to api on
// model_name. Input and output prices are always overwritten: they are what
// the refresh measures. The cache price is overwritten only when the API
// reported one (it usually does not), and the subscription and time_prices
// are never touched, so a refresh never clears a price set by hand.
func PlanPrices(entries []Entry, api map[string]APIPrice) *PricePlan {
	plan := &PricePlan{}
	for _, e := range entries {
		if !OpenRouterPriced(e) {
			continue
		}
		plan.Candidates++
		price, ok := api[e.ModelName]
		if !ok {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("No OpenRouter match for %s (%s)", e.ID, e.ModelName))
			continue
		}
		if len(price.Skipped) > 0 {
			// The whole entry is refused: a model whose price "varies" has
			// no per-token price to record.
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("Could not use OpenRouter's pricing for %s: its %s price is negative or not a number",
				e.ID, strings.Join(price.Skipped, " and ")))
			continue
		}
		if price.Input == nil && price.Output == nil && price.Cache == nil {
			plan.Warnings = append(plan.Warnings, "No pricing data for "+e.ID)
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
		plan.Matched = append(plan.Matched, PriceChange{
			ModelID: e.ID, Before: e.Cost, After: after, Changed: !sameCost(e.Cost, after),
		})
	}
	return plan
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
	for _, w := range p.Warnings {
		lines = append(lines, "warning: "+w)
	}
	return strings.Join(lines, "\n")
}
