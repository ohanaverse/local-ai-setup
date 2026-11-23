package windows

import (
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tiers"
)

// tiersIndex parses a tiers.toml with one entry per (provider, model, context, basis).
func tiersIndex(t *testing.T, provider, name string, context int, basis string) tiers.Index {
	t.Helper()
	text := "[[models]]\nprovider_id = \"" + provider + "\"\nmodel_name = \"" + name + "\"\n" +
		"tier = \"standard\"\ncode = \"S\"\nconfidence = \"high\"\ncontext = " + itoa(context) + "\ncontext_basis = \"" + basis + "\"\n"
	ix, err := tiers.Parse([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return ix
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for ; n > 0; n /= 10 {
		s = string(rune('0'+n%10)) + s
	}
	return s
}

// TestRegistryWindowOverridesTiers pins that a registry context_window beats the
// tiers file for a cloud model: the owner's number is the one a gateway slot
// must use, and the advisory tiers value is only a fallback.
func TestRegistryWindowOverridesTiers(t *testing.T) {
	cfg := &config.Config{Providers: []config.Provider{{ID: "openrouter", Location: config.LocationCloud}}}
	m := config.Model{ID: "openrouter/a", ProviderID: "openrouter", ModelName: "a", Location: config.LocationCloud, ContextWindow: 64000}
	ix := tiersIndex(t, "openrouter", "a", 200000, tiers.ContextBasisHost)
	if got := Model(cfg, m, ix); got != 64000 {
		t.Errorf("Model = %d, want the registry's 64000", got)
	}
}

// TestCloudWindowFromTiersWhenLocationIsInherited pins that a cloud model whose
// location is set only on its provider row still reads the host-basis tiers
// window: ResolveLocation, not the model's own field, decides cloud.
func TestCloudWindowFromTiersWhenLocationIsInherited(t *testing.T) {
	cfg := &config.Config{Providers: []config.Provider{{ID: "openrouter", Location: config.LocationCloud}}}
	m := config.Model{ID: "openrouter/a", ProviderID: "openrouter", ModelName: "a"}
	if got := Model(cfg, m, tiersIndex(t, "openrouter", "a", 200000, tiers.ContextBasisHost)); got != 200000 {
		t.Errorf("Model = %d, want the tiers 200000", got)
	}
	if got := Model(cfg, m, tiersIndex(t, "openrouter", "a", 1000000, "largest_row")); got != 0 {
		t.Errorf("a largest_row window read as %d, want 0 (not a window any host serves)", got)
	}
}

// TestLocalModelWindowComesOnlyFromRegistry pins that a local model never takes
// a window from tiers, even when its provider/name matches a host-basis entry:
// the served window of a local build is whatever the server was started with.
func TestLocalModelWindowComesOnlyFromRegistry(t *testing.T) {
	cfg := &config.Config{Providers: []config.Provider{{ID: "ollama", Location: config.LocationLocal}}}
	m := config.Model{ID: "ollama/a", ProviderID: "ollama", ModelName: "a"}
	ix := tiersIndex(t, "ollama", "a", 131072, tiers.ContextBasisHost)
	if got := Model(cfg, m, ix); got != 0 {
		t.Errorf("Model = %d, want 0 for a local model without a registry value", got)
	}
	m.ContextWindow = 32768
	if got := Model(cfg, m, ix); got != 32768 {
		t.Errorf("Model = %d, want the registry's 32768", got)
	}
	bad := config.Model{ID: "nope/a", ProviderID: "nope", ModelName: "a"}
	if got := Model(cfg, bad, tiersIndex(t, "nope", "a", 5, tiers.ContextBasisHost)); got != 0 {
		t.Errorf("a model whose location does not resolve read %d, want 0", got)
	}
}
