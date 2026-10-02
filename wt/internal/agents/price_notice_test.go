package agents

import (
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
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

// TestHasOpenRouterPricedModel pins issue #151's rule, which mirrors
// modelman's _is_openrouter_priced: the stale-pricing notice is only worth
// printing when some model's price comes from OpenRouter — an openrouter
// model or a model of a non-native cloud provider. Ollama cloud models
// (location "cloud" on the local ollama provider, priced by ollama.com) and
// native agent models never count, or users with no OpenRouter models are
// nagged after every session about a refresh that has nothing to do.
func TestHasOpenRouterPricedModel(t *testing.T) {
	providers := []config.Provider{
		{ID: "ollama", Location: config.LocationLocal},
		{ID: "openrouter", Location: config.LocationCloud},
		{ID: "claude", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "native"}},
		{ID: "acme", Location: config.LocationCloud},
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
