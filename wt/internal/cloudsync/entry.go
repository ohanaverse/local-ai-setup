package cloudsync

import (
	"fmt"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tomlw"
)

// Entry is what the planners read of one registry model row. It is built
// from the row as written, not from config.Model: the planners need keys
// wt's typed reader does not model (catalog_name, the time_prices rows with
// whatever keys they hold).
type Entry struct {
	ID, Family, ProviderID, ModelName, Location string
	// CatalogName is the page name an earlier sync recorded on the row, so a
	// model stays matched after its tag is renamed. "" when there is none.
	CatalogName string
	// Source is the row's source key: "curated", "discovered", "manual" or
	// "" when absent.
	Source string
	// Cost is nil when the row has no cost table.
	Cost *Cost
	// AliasUsers are the registry aliases that target the model (EntriesOf
	// reads them from the raw [[aliases]] tables, as RemoveModel does). A
	// model an alias targets is never a removal (decision 11, #596): the
	// planners keep it and say so (keptForAliases).
	AliasUsers []string
}

// Manual reports whether the row was added by hand and has not yet been seen
// on the source its flow mirrors: such a row is never a removal candidate.
func (e Entry) Manual() bool { return e.Source == string(config.SourceManual) }

// CoveredBySync reports whether a sync flow can address a model of this
// provider, location and name: any openrouter model, and an ollama model
// that is cloud (location "cloud" or a cloud tag). Only such a model can be
// marked manual.
func CoveredBySync(providerID, location, modelName string) bool {
	switch providerID {
	case config.OpenRouterProvider:
		return true
	case ollamaProvider:
		return location == "cloud" || IsCloudTag(modelName)
	}
	return false
}

// Cost is a row's cost table as the planners read it. A nil price is a key
// that is not there.
type Cost struct {
	Input, Cache, Output *float64
	SubscriptionPrice    *float64
	// SubscriptionPeriod is nil when the key is absent.
	SubscriptionPeriod *string
	// TimePrices are the cost.time_prices rows, whole, in file order.
	TimePrices []*tomlw.Table
}

// Provider is what the command reads of one provider row: its id, which is
// how it knows the registry has an ollama row at all.
type Provider struct {
	ID string
}

// Entries reads model rows (config.RegistryDoc.Models) into Entry values. A
// row with no string id is skipped: nothing can address it.
func Entries(rows []*tomlw.Table) []Entry {
	out := make([]Entry, 0, len(rows))
	for _, row := range rows {
		id := str(row, "id")
		if id == "" {
			continue
		}
		e := Entry{
			ID: id, Family: str(row, "family"), ProviderID: str(row, "provider_id"),
			ModelName: str(row, "model_name"), Location: str(row, "location"),
			CatalogName: str(row, CatalogNameKey), Source: str(row, "source"),
		}
		if v, ok := row.Get("cost"); ok {
			if t, isTable := v.(*tomlw.Table); isTable {
				e.Cost = costOf(t)
			}
		}
		out = append(out, e)
	}
	return out
}

// EntriesOf reads doc's model rows (Entries) with, on each, the aliases that
// target it, read from the raw [[aliases]] tables exactly as
// RegistryDoc.RemoveModel reads them, so a malformed alias still protects
// its target. Both flows plan from it, inside and outside the registry lock.
func EntriesOf(doc *config.RegistryDoc) []Entry {
	entries := Entries(doc.Models())
	for i := range entries {
		entries[i].AliasUsers = doc.AliasesTargeting(entries[i].ID)
	}
	return entries
}

// keptForAliases is the warning a plan carries for a model it would have
// removed but keeps because aliases target it.
func keptForAliases(e Entry) string {
	verb := "targets"
	if len(e.AliasUsers) > 1 {
		verb = "target"
	}
	return fmt.Sprintf("kept %s: %s %s it", e.ID, strings.Join(e.AliasUsers, ", "), verb)
}

// Providers reads provider rows (config.RegistryDoc.Providers).
func Providers(rows []*tomlw.Table) []Provider {
	out := make([]Provider, 0, len(rows))
	for _, row := range rows {
		out = append(out, Provider{ID: str(row, "id")})
	}
	return out
}

func costOf(t *tomlw.Table) *Cost {
	c := &Cost{
		Input:             number(t, "input_price_per_million"),
		Cache:             number(t, "cache_price_per_million"),
		Output:            number(t, "output_price_per_million"),
		SubscriptionPrice: number(t, "subscription_price"),
	}
	if v, ok := t.Get("subscription_period"); ok {
		if s, isString := v.(string); isString {
			c.SubscriptionPeriod = &s
		}
	}
	if v, ok := t.Get("time_prices"); ok {
		if arr, isArray := v.([]any); isArray {
			for _, item := range arr {
				if row, isTable := item.(*tomlw.Table); isTable {
					c.TimePrices = append(c.TimePrices, row)
				}
			}
		}
	}
	return c
}

func str(t *tomlw.Table, key string) string {
	v, _ := t.Get(key)
	s, _ := v.(string)
	return s
}

// number reads a price: a TOML integer or float. Anything else is no price.
func number(t *tomlw.Table, key string) *float64 {
	v, _ := t.Get(key)
	switch n := v.(type) {
	case int64:
		f := float64(n)
		return &f
	case float64:
		return &n
	}
	return nil
}
