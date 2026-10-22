package cloudsync

import "github.com/ohanaverse/local-ai-setup/wt/internal/tomlw"

// Entry is what the planners read of one registry model row. It is built
// from the row as written, not from config.Model: the planners need keys
// wt's typed reader does not model (catalog_name, the time_prices rows with
// whatever keys they hold).
type Entry struct {
	ID, Family, ProviderID, ModelName, Location string
	// CatalogName is the page name an earlier sync recorded on the row, so a
	// model stays matched after its tag is renamed. "" when there is none.
	CatalogName string
	// Cost is nil when the row has no cost table.
	Cost *Cost
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
			CatalogName: str(row, CatalogNameKey),
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
