package agents

import (
	"io"
	"os"
	"testing"
	"time"
)

// TestPriceNoticeThresholds pins when the notice speaks. Refreshing is manual
// now, so the notice is the only reminder: it must stay quiet for a week
// after a refresh (a line on every launch trains people to ignore it), speak
// once the newest price is more than seven days old, and say "never" when no
// OpenRouter price carries a stamp. Both wordings name the command to run.
func TestPriceNoticeThresholds(t *testing.T) {
	now := time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		last    time.Time
		present bool
		want    string
	}{
		{"refreshed a minute ago", now.Add(-time.Minute), true, ""},
		{"six days ago", now.Add(-6 * 24 * time.Hour), true, ""},
		{"exactly seven days ago", now.Add(-7 * 24 * time.Hour), true, ""},
		{"seven days and a second", now.Add(-7*24*time.Hour - time.Second), true, "wt: token pricing last refreshed 2026-09-30 — run 'wt cloud-sync'"},
		{"a month ago", time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC), true, "wt: token pricing last refreshed 2026-09-01 — run 'wt cloud-sync'"},
		{"a stamp a few minutes ahead (a skewed clock)", now.Add(10 * time.Minute), true, ""},
		{"never", time.Time{}, false, "wt: token pricing has never been refreshed — run 'wt cloud-sync'"},
	}
	for _, tc := range cases {
		if got := PriceNotice(tc.last, tc.present, now); got != tc.want {
			t.Errorf("%s: PriceNotice = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestPriceNoticeShowsTheLocalDate pins that the date in the notice is the
// reader's, not UTC's: a refresh at 02:31 UTC on the 4th happened on the
// evening of the 3rd in California, and that is the day the user remembers
// running it.
func TestPriceNoticeShowsTheLocalDate(t *testing.T) {
	pacific := time.FixedZone("PDT", -7*3600)
	last := time.Date(2026, 10, 4, 2, 31, 25, 0, time.UTC)
	now := time.Date(2026, 10, 20, 9, 0, 0, 0, pacific)
	if got, want := PriceNotice(last, true, now), "wt: token pricing last refreshed 2026-10-03 — run 'wt cloud-sync'"; got != want {
		t.Errorf("PriceNotice = %q, want %q", got, want)
	}
}

// captureStdout returns what fn printed to os.Stdout.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()
	fn()
	w.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}
