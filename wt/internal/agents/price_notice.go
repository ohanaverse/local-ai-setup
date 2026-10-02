package agents

import (
	"fmt"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// PrintPriceNotice reads modelman's price_refresh_last_run and prints the
// stale-pricing notice when pricing is not fresh. It is the shared
// production emission helper used by both the TUI and non-TUI launch paths.
// Silent when cfg has no OpenRouter-priced model (#151) — `modelman
// refresh-prices` would have nothing to refresh. Errors reading or parsing
// modelman.toml are silently ignored, matching the exposure-flags tolerance
// model.
func PrintPriceNotice(cfg *config.Config) {
	if !HasOpenRouterPricedModel(cfg) {
		return
	}
	last, present := config.PriceRefreshLastRun()
	if notice := PriceNotice(last, present, time.Now()); notice != "" {
		fmt.Println(notice)
	}
}

// PriceNotice renders the post-run stale-pricing notice (issue #69) or ""
// when pricing is fresh. wt is a passive consumer: it only prints the
// reminder — modelman owns the refresh (`modelman refresh-prices`).
// lastRun is modelman's price_refresh_last_run value (YYYY-MM-DD);
// present is false when the file/key is missing or unreadable, which
// means pricing has never been refreshed. A malformed date is treated as
// "never refreshed" so the user sees the stronger "never been refreshed"
// wording rather than a nonsense date.
func PriceNotice(lastRun string, present bool, now time.Time) string {
	today := now.Format("2006-01-02")
	if present {
		if _, err := time.Parse("2006-01-02", lastRun); err != nil {
			present = false
		} else if lastRun == today {
			return ""
		}
	}
	if present {
		return fmt.Sprintf("wt: token pricing last refreshed %s — run 'modelman refresh-prices'", lastRun)
	}
	return "wt: token pricing has never been refreshed — run 'modelman refresh-prices'"
}

// HasOpenRouterPricedModel reports whether any model in cfg takes its price
// from OpenRouter — what `modelman refresh-prices` refreshes. It delegates to
// config.OpenRouterPriced, which mirrors modelman's _is_openrouter_priced
// (pricing.py): an openrouter model, or a model of a non-native cloud
// provider. Keyed on the provider's location, not the model's, so ollama
// cloud models (location "cloud" on the local ollama provider, priced by
// ollama.com) don't count. A model whose ProviderID has no registry provider
// doesn't count either (p == nil, location unresolvable) — the Python side
// skips it silently too. A nil cfg has no models.
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
