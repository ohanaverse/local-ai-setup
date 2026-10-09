package agents

import (
	"io"
	"os"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
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

// TestLastPriceRefresh pins where the date comes from now that nothing
// stores it: the newest pricing_updated_at among OpenRouter-priced models
// only. An ollama cloud model's stamp must not count (the ollama flow
// writes those, and an ollama-only run would silence a notice about prices
// it never refreshed), a model without a stamp or with a mistyped one is
// skipped, not fatal, and with no usable stamp the answer is "never". A
// stamp more than a day ahead of the clock is a typo and is skipped too:
// counted, a year typed as 2062 would silence the notice for good.
func TestLastPriceRefresh(t *testing.T) {
	providers := []config.Provider{
		{ID: "ollama", Location: config.LocationLocal},
		{ID: "openrouter", Location: config.LocationCloud},
	}
	or := func(id string, stamp any) config.Model {
		return config.Model{ID: id, ProviderID: "openrouter", PricingUpdatedAt: stamp}
	}
	ollamaCloud := config.Model{ID: "ollama/glm:cloud", ProviderID: "ollama", Location: config.LocationCloud, PricingUpdatedAt: "2026-10-07T00:00:00+00:00"}
	newest := time.Date(2026, 10, 4, 2, 31, 25, 0, time.UTC)
	now := time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)

	cfg := &config.Config{Providers: providers, Models: []config.Model{
		ollamaCloud,
		or("openrouter/old", "2026-09-01T00:00:00+00:00"),
		or("openrouter/newest", "2026-10-03T19:31:25-07:00"),
		or("openrouter/unstamped", nil),
		or("openrouter/typo", "last tuesday"),
		or("openrouter/number", int64(20261004)),
	}}
	if got, ok := LastPriceRefresh(cfg, now); !ok || !got.Equal(newest) {
		t.Errorf("LastPriceRefresh = (%v, %v), want (%v, true)", got, ok, newest)
	}

	// A date-time a hand edit left as a TOML value, and one with no offset.
	cfg.Models = []config.Model{or("openrouter/a", newest), or("openrouter/b", "2026-10-01T08:00:00")}
	if got, ok := LastPriceRefresh(cfg, now); !ok || !got.Equal(newest) {
		t.Errorf("with a TOML date-time: LastPriceRefresh = (%v, %v), want (%v, true)", got, ok, newest)
	}

	// A stamp far in the future is a typo, not a refresh: it is skipped, so
	// it cannot silence the notice until that date comes. One that is ahead
	// by less than a day is another machine's clock, and counts.
	typo := or("openrouter/typo-year", "2062-10-04T02:31:25+00:00")
	cfg.Models = []config.Model{typo, or("openrouter/a", newest)}
	if got, ok := LastPriceRefresh(cfg, now); !ok || !got.Equal(newest) {
		t.Errorf("with a stamp in 2062: LastPriceRefresh = (%v, %v), want (%v, true)", got, ok, newest)
	}
	cfg.Models = []config.Model{typo}
	if got, ok := LastPriceRefresh(cfg, now); ok {
		t.Errorf("with only a stamp in 2062: LastPriceRefresh = (%v, true), want never", got)
	}
	ahead := now.Add(23 * time.Hour)
	cfg.Models = []config.Model{or("openrouter/skewed", ahead), or("openrouter/a", newest)}
	if got, ok := LastPriceRefresh(cfg, now); !ok || !got.Equal(ahead) {
		t.Errorf("with a stamp 23 hours ahead: LastPriceRefresh = (%v, %v), want (%v, true)", got, ok, ahead)
	}

	cfg.Models = []config.Model{ollamaCloud, or("openrouter/unstamped", nil)}
	if got, ok := LastPriceRefresh(cfg, now); ok {
		t.Errorf("with no stamped OpenRouter model: LastPriceRefresh = (%v, true), want never", got)
	}
	if _, ok := LastPriceRefresh(nil, now); ok {
		t.Error("LastPriceRefresh(nil, now) reported a refresh")
	}
}

// TestPrintPriceNoticeSpeaksOnlyAboutOpenRouterPrices pins what a launch
// actually prints. A registry with no OpenRouter-priced model gets no line
// at all, however old its other stamps are: `wt cloud-sync` would have
// nothing to refresh there, and a reminder the user cannot act on is noise
// after every session. "OpenRouter-priced" is the registry's rule, not the
// presence of an `openrouter` provider row: a model that another provider
// marks openrouter_priced is reminded about, and an openrouter row with no
// model is not.
func TestPrintPriceNoticeSpeaksOnlyAboutOpenRouterPrices(t *testing.T) {
	yes := true
	fresh := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	ollamaCloud := config.Model{ID: "ollama/glm:cloud", ProviderID: "ollama", Location: config.LocationCloud, PricingUpdatedAt: "2020-01-01T00:00:00+00:00"}
	native := config.Model{ID: "claude/native", ProviderID: "claude", Native: true}
	const never = "wt: token pricing has never been refreshed — run 'wt cloud-sync'\n"
	cases := []struct {
		name      string
		providers []config.Provider
		models    []config.Model
		want      string
	}{
		{
			"no OpenRouter-priced model, although an openrouter provider row exists",
			[]config.Provider{
				{ID: "ollama", Location: config.LocationLocal},
				{ID: "openrouter", Location: config.LocationCloud},
				{ID: "claude", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "native"}},
			},
			[]config.Model{ollamaCloud, native},
			"",
		},
		{
			"an openrouter model that was never stamped",
			[]config.Provider{{ID: "openrouter", Location: config.LocationCloud}},
			[]config.Model{{ID: "openrouter/x", ProviderID: "openrouter"}},
			never,
		},
		{
			"no openrouter provider row, but a provider marked openrouter_priced",
			[]config.Provider{{ID: "gateway", Location: config.LocationLocal, OpenRouterPriced: &yes}},
			[]config.Model{{ID: "gateway/x", ProviderID: "gateway"}},
			never,
		},
		{
			"an openrouter model refreshed an hour ago",
			[]config.Provider{{ID: "openrouter", Location: config.LocationCloud}},
			[]config.Model{{ID: "openrouter/x", ProviderID: "openrouter", PricingUpdatedAt: fresh}},
			"",
		},
	}
	for _, tc := range cases {
		cfg := &config.Config{Providers: tc.providers, Models: tc.models}
		if got := captureStdout(t, func() { PrintPriceNotice(cfg) }); got != tc.want {
			t.Errorf("%s: PrintPriceNotice printed %q, want %q", tc.name, got, tc.want)
		}
	}
	if got := captureStdout(t, func() { PrintPriceNotice(nil) }); got != "" {
		t.Errorf("PrintPriceNotice(nil) printed %q, want nothing", got)
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

// TestHasOpenRouterPricedModel pins issue #151's rule: the stale-pricing
// notice is only worth printing when some model's price comes from
// OpenRouter — an openrouter model or a model of a non-native cloud
// provider. Ollama cloud models
// (location "cloud" on the local ollama provider, priced by ollama.com) and
// native agent models never count, or users with no OpenRouter models are
// nagged after every session about a refresh that has nothing to do. A
// provider's explicit openrouter_priced overrides the inference in both
// directions (a corporate gateway opts out, a local proxy opts in) but never
// makes a native provider count. `wt cloud-sync` refreshes the models the
// same rule selects (internal/cloudsync's predicate, held to the same
// docs/contracts/catalog-predicates fixture), so a notice that judged
// otherwise would nag about a refresh that changes nothing, or stay silent
// on one that is due.
func TestHasOpenRouterPricedModel(t *testing.T) {
	no, yes := false, true
	providers := []config.Provider{
		{ID: "ollama", Location: config.LocationLocal},
		{ID: "openrouter", Location: config.LocationCloud},
		{ID: "claude", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "native"}},
		{ID: "acme", Location: config.LocationCloud},
		{ID: "corp", Location: config.LocationCloud, OpenRouterPriced: &no},
		{ID: "gateway", Location: config.LocationLocal, OpenRouterPriced: &yes},
		{ID: "agent", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "native"}, OpenRouterPriced: &yes},
	}
	ollamaCloud := config.Model{ID: "ollama/glm:cloud", ProviderID: "ollama", Location: config.LocationCloud}
	native := config.Model{ID: "claude/native", ProviderID: "claude", Native: true}
	cases := []struct {
		name   string
		cfg    *config.Config
		models []config.Model
		want   bool
	}{
		{"nil config", nil, nil, false},
		{"ollama cloud and native only", &config.Config{}, []config.Model{ollamaCloud, native}, false},
		{"openrouter model", &config.Config{}, []config.Model{ollamaCloud, {ID: "openrouter/x", ProviderID: "openrouter"}}, true},
		{"non-native cloud provider", &config.Config{}, []config.Model{{ID: "acme/x", ProviderID: "acme"}}, true},
		{"cloud provider marked openrouter_priced = false", &config.Config{}, []config.Model{{ID: "corp/x", ProviderID: "corp"}}, false},
		{"local provider marked openrouter_priced = true", &config.Config{}, []config.Model{{ID: "gateway/x", ProviderID: "gateway"}}, true},
		{"native provider marked openrouter_priced = true", &config.Config{}, []config.Model{{ID: "agent/x", ProviderID: "agent"}}, false},
	}
	for _, tc := range cases {
		if tc.cfg != nil {
			tc.cfg.Providers = providers
			tc.cfg.Models = tc.models
		}
		if got := HasOpenRouterPricedModel(tc.cfg); got != tc.want {
			t.Errorf("%s: HasOpenRouterPricedModel = %v, want %v", tc.name, got, tc.want)
		}
	}
}
