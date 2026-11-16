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

// PrintPriceNotice prints the stale-pricing notice when the registry's
// OpenRouter prices are more than a week old or were never refreshed. It is
// the shared production emission helper used by both the TUI and non-TUI
// launch paths. Silent when cfg has no OpenRouter-priced model (#151):
// `wt cloud-sync` would have nothing to refresh.
func PrintPriceNotice(cfg *config.Config) {
	if !cfg.OpenRouterPricedModel() {
		return
	}
	now := time.Now()
	last, present := cfg.LastPriceRefresh(now)
	if notice := PriceNotice(last, present, now); notice != "" {
		fmt.Println(notice)
	}
}

// PriceNotice renders the post-run stale-pricing notice (issue #69), or ""
// when pricing is fresh: refreshed within the last seven days. wt only
// prints the reminder; refreshing is the user's to run (`wt cloud-sync`).
// last and present are config.Config.LastPriceRefresh's answer. The date is
// shown in now's time zone.
func PriceNotice(last time.Time, present bool, now time.Time) string {
	if !present {
		return "wt: token pricing has never been refreshed — run 'wt cloud-sync'"
	}
	if now.Sub(last) <= priceStaleAfter {
		return ""
	}
	return fmt.Sprintf("wt: token pricing last refreshed %s — run 'wt cloud-sync'", last.In(now.Location()).Format("2006-01-02"))
}
