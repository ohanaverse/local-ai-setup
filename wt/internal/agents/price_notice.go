package agents

import (
	"fmt"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// priceStaleAfter is how old the newest OpenRouter price may be before the
// notice fires. Refreshing is manual (`wt cloud-sync`), so the notice is the
// only reminder; a week keeps it from nagging on every launch.
const priceStaleAfter = 7 * 24 * time.Hour

// priceStampSkew is how far ahead of the clock a stamp may be and still
// count. Two machines' clocks disagree by minutes; a stamp further ahead than
// a day is a typo (2062 for 2026), and counting it would silence the notice
// until that date came.
const priceStampSkew = 24 * time.Hour

// PrintPriceNotice prints the stale-pricing notice when the registry's
// OpenRouter prices are more than a week old or were never refreshed. It is
// the shared production emission helper used by both the TUI and non-TUI
// launch paths. Silent when cfg has no OpenRouter-priced model (#151):
// `wt cloud-sync` would have nothing to refresh.
func PrintPriceNotice(cfg *config.Config) {
	if !HasOpenRouterPricedModel(cfg) {
		return
	}
	now := time.Now()
	last, present := LastPriceRefresh(cfg, now)
	if notice := PriceNotice(last, present, now); notice != "" {
		fmt.Println(notice)
	}
}

// LastPriceRefresh is when OpenRouter prices were last refreshed, and whether
// they ever were: the newest pricing_updated_at among cfg's OpenRouter-priced
// models. Nothing stores the date. `wt cloud-sync` stamps every model it
// matches, whether or not its price moved, so the newest stamp is the last
// refresh. A model with no stamp, one that is not a time, or one more than a
// day ahead of now is skipped.
func LastPriceRefresh(cfg *config.Config, now time.Time) (time.Time, bool) {
	var newest time.Time
	found := false
	if cfg == nil {
		return newest, false
	}
	for _, m := range cfg.Models {
		if !cfg.OpenRouterPriced(m) {
			continue
		}
		at, ok := m.PricingUpdated()
		if !ok || at.Sub(now) > priceStampSkew {
			continue
		}
		if !found || at.After(newest) {
			newest, found = at, true
		}
	}
	return newest, found
}

// PriceNotice renders the post-run stale-pricing notice (issue #69), or ""
// when pricing is fresh: refreshed within the last seven days. wt only
// prints the reminder; refreshing is the user's to run (`wt cloud-sync`).
// last and present are LastPriceRefresh's answer. The date is shown in now's
// time zone.
func PriceNotice(last time.Time, present bool, now time.Time) string {
	if !present {
		return "wt: token pricing has never been refreshed — run 'wt cloud-sync'"
	}
	if now.Sub(last) <= priceStaleAfter {
		return ""
	}
	return fmt.Sprintf("wt: token pricing last refreshed %s — run 'wt cloud-sync'", last.In(now.Location()).Format("2006-01-02"))
}

// HasOpenRouterPricedModel reports whether any model in cfg takes its price
// from OpenRouter — what `wt cloud-sync`'s prices flow refreshes. It
// delegates to config.OpenRouterPriced: an openrouter model, or a model of a
// non-native cloud provider. Keyed on the provider's location, not the
// model's, so ollama cloud models (location "cloud" on the local ollama
// provider, priced by ollama.com) don't count. A model whose ProviderID has
// no registry provider doesn't count either (p == nil, location
// unresolvable). A nil cfg has no models.
func HasOpenRouterPricedModel(cfg *config.Config) bool {
	if cfg == nil {
		return false
	}
	for _, m := range cfg.Models {
		if cfg.OpenRouterPriced(m) {
			return true
		}
	}
	return false
}
