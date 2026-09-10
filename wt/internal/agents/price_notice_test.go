package agents

import (
	"strings"
	"testing"
	"time"
)

// TestPriceNoticeTodaySilent guards against the notice nagging when
// modelman refreshed pricing today — the user just did the right thing
// and extra output on every launch would train them to ignore the line.
func TestPriceNoticeTodaySilent(t *testing.T) {
	now := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	if got := PriceNotice("2026-09-15", true, now); got != "" {
		t.Errorf("PriceNotice(today) = %q, want empty", got)
	}
}

// TestPriceNoticeStaleDateShows notices when the last refresh date is
// anything other than today — this is the user-facing point of the
// feature (issue #69): point the user at `modelman refresh-prices`.
func TestPriceNoticeStaleDateShows(t *testing.T) {
	now := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	got := PriceNotice("2026-09-14", true, now)
	if !strings.Contains(got, "2026-09-14") || !strings.Contains(got, "modelman refresh-prices") {
		t.Errorf("PriceNotice(yesterday) = %q, want date + refresh hint", got)
	}
	if !strings.HasPrefix(got, "wt: ") {
		t.Errorf("PriceNotice(yesterday) = %q, want 'wt: ' prefix", got)
	}
}

// TestPriceNoticeMissingKey covers a fresh install (file or key absent):
// the wording must say pricing was never refreshed rather than printing
// an empty date placeholder.
func TestPriceNoticeMissingKey(t *testing.T) {
	now := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	got := PriceNotice("", false, now)
	if !strings.Contains(got, "never been refreshed") || !strings.Contains(got, "modelman refresh-prices") {
		t.Errorf("PriceNotice(missing) = %q, want never-refreshed wording", got)
	}
}

// TestPriceNoticeMalformedDateFallsBackToNever guards against a corrupt
// price_refresh_last_run value (e.g. hand-edited TOML) being printed
// verbatim. The user-facing fallback must be the "never been refreshed"
// wording so the notice stays actionable.
func TestPriceNoticeMalformedDateFallsBackToNever(t *testing.T) {
	now := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	got := PriceNotice("not-a-date", true, now)
	if !strings.Contains(got, "never been refreshed") || !strings.Contains(got, "modelman refresh-prices") {
		t.Errorf("PriceNotice(malformed) = %q, want never-refreshed wording", got)
	}
}
