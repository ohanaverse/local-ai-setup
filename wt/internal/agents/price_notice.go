package agents

import (
	"context"
	"fmt"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
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

// PriceNoticeFromCatalog is PriceNotice over the model server's catalog: p is
// Catalog.Pricing, which is present exactly when some model is priced from
// OpenRouter, so nil is silence (`wt cloud-sync` would have nothing to
// refresh, #151). A pricing with no stamp, or one that is not a time, reads
// as never refreshed. now is the server's clock (modelclient's Now): the
// stamp is the server's, so its age is measured on the same clock (R16).
func PriceNoticeFromCatalog(p *modelapi.Pricing, now time.Time) string {
	if p == nil {
		return ""
	}
	last, err := time.Parse(time.RFC3339, p.Updated)
	return PriceNotice(last, err == nil && p.Updated != "", now)
}

// PricingSource is what the remote notice reads from a model server client:
// the catalog and the server's clock.
type PricingSource interface {
	Catalog(ctx context.Context) (modelapi.Catalog, error)
	Now() time.Time
}

// remoteNoticeBound caps the one catalog fetch the notice may make.
const remoteNoticeBound = 3 * time.Second

// PrintRemotePriceNotice prints the stale-pricing notice for a launch on the
// model server, the counterpart of PrintPriceNotice. cached is the catalog's
// pricing from a fetch the caller already made (have says there was one);
// without one it makes one cheap, bounded fetch. It is best-effort: a failed
// fetch prints nothing, and nothing here changes an exit code. The age is
// measured on src's clock, read after the fetch so a fresh response's
// Date header is in it.
func PrintRemotePriceNotice(ctx context.Context, src PricingSource, cached *modelapi.Pricing, have bool) {
	p := cached
	if !have {
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), remoteNoticeBound)
		defer cancel()
		cat, err := src.Catalog(fctx)
		if err != nil {
			return
		}
		p = cat.Pricing
	}
	if notice := PriceNoticeFromCatalog(p, src.Now()); notice != "" {
		fmt.Println(notice)
	}
}
