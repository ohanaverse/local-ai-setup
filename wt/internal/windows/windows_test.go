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

// aliasCfg has a cloud model with a registry window, a cloud model whose
// window comes from tiers, and a local model with no window.
func aliasCfg() *config.Config {
	return &config.Config{
		Providers: []config.Provider{{ID: "openrouter", Location: config.LocationCloud}, {ID: "ollama", Location: config.LocationLocal}},
		Models: []config.Model{
			{ID: "openrouter/a", ProviderID: "openrouter", ModelName: "a", ContextWindow: 200000},
			{ID: "openrouter/t", ProviderID: "openrouter", ModelName: "t"},
			{ID: "ollama/b", ProviderID: "ollama", ModelName: "b"},
		},
	}
}

// TestAliasWindowWithheldWhenATargetIsUnknown pins Review Focus 4: an alias
// declares no window (0) when any one target's window is unknown, or a
// target names no model, and `limiting` names that target. A min over the
// known targets alone would over-declare, and Claude would compact too late
// on the target with the smaller window.
func TestAliasWindowWithheldWhenATargetIsUnknown(t *testing.T) {
	cfg := aliasCfg()
	ix := tiersIndex(t, "openrouter", "t", 128000, tiers.ContextBasisHost)
	a := config.Alias{ID: "alias/x", Targets: []config.AliasTarget{{Model: "openrouter/a", Order: 1}, {Model: "ollama/b", Order: 2}}}
	if got, lim := Alias(cfg, a, ix); got != 0 || lim != "ollama/b" {
		t.Errorf("Alias = (%d, %q), want (0, ollama/b): the local target's window is unknown", got, lim)
	}
	a.Targets[1].Model = "ollama/ghost"
	if got, lim := Alias(cfg, a, ix); got != 0 || lim != "ollama/ghost" {
		t.Errorf("Alias = (%d, %q), want (0, ollama/ghost): a missing target", got, lim)
	}
	if got, _ := Alias(cfg, config.Alias{ID: "alias/empty"}, ix); got != 0 {
		t.Errorf("an alias with no targets read %d, want 0", got)
	}
}

// TestAliasWindowIsTheMinimum pins Review Focus 4 from the other side: with
// every target's window known, the alias declares the smallest, from any
// source (registry or host-basis tiers), and names the target that sets it.
func TestAliasWindowIsTheMinimum(t *testing.T) {
	cfg := aliasCfg()
	cfg.Models[2].ContextWindow = 32768
	ix := tiersIndex(t, "openrouter", "t", 128000, tiers.ContextBasisHost)
	a := config.Alias{ID: "alias/x", Targets: []config.AliasTarget{
		{Model: "openrouter/a", Order: 1}, {Model: "openrouter/t", Order: 2}, {Model: "ollama/b", Order: 3}}}
	if got, lim := Alias(cfg, a, ix); got != 32768 || lim != "ollama/b" {
		t.Errorf("Alias = (%d, %q), want (32768, ollama/b)", got, lim)
	}
	a.Targets = a.Targets[:2]
	if got, lim := Alias(cfg, a, ix); got != 128000 || lim != "openrouter/t" {
		t.Errorf("Alias = (%d, %q), want (128000, openrouter/t) from the tiers file", got, lim)
	}
}

func TestShort(t *testing.T) {
	for n, want := range map[int]string{-1: "-", 0: "-", 512: "512", 999: "999", 1000: "1k", 8192: "8k", 131072: "131k", 200000: "200k",
		262144: "262k", 999499: "999k", 999500: "1M", 1000000: "1M", 1048576: "1M", 1500000: "1.5M", 2097152: "2.1M"} {
		if got := Short(n); got != want {
			t.Errorf("Short(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestMin(t *testing.T) {
	if Min() != 0 || Min(5, 0) != 0 || Min(0, 5) != 0 || Min(7, 3, 9) != 3 {
		t.Error("Min must be 0 for none or any unknown, else the smallest")
	}
}
