package agents

import (
	"fmt"
	"time"
)

// PriceNotice renders the post-run stale-pricing notice (issue #69) or ""
// when pricing is fresh. wt is a passive consumer: it only prints the
// reminder — modelman owns the refresh (`modelman refresh-prices`).
// lastRun is modelman's price_refresh_last_run value (YYYY-MM-DD);
// present is false when the file/key is missing or unreadable, which
// means pricing has never been refreshed. Comparison is plain string
// equality against today's date — modelman only writes dates, and a
// malformed present value simply won't match today and still warns.
func PriceNotice(lastRun string, present bool, now time.Time) string {
	today := now.Format("2006-01-02")
	if present && lastRun == today {
		return ""
	}
	if present {
		return fmt.Sprintf("wt: token pricing last refreshed %s — run 'modelman refresh-prices'", lastRun)
	}
	return "wt: token pricing has never been refreshed — run 'modelman refresh-prices'"
}
