package configeditor

import (
	"testing"

	"github.com/ohanaverse/agent-worktree/internal/config"
)

// TestProvidersTab_SortByID verifies providers are sorted alphabetically
// by ID. Without this, the provider list would be in config-file order,
// which is unpredictable and harder to scan.
func TestProvidersTab_SortByID(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.Provider{
			{ID: "zeta"},
			{ID: "alpha"},
			{ID: "mu"},
		},
	}
	l := buildProvidersList(testTheme(), 80, 24, cfg)
	if len(l.Items()) != 3 {
		t.Fatalf("expected 3 items, got %d", len(l.Items()))
	}

	ids := make([]string, 3)
	for i, it := range l.Items() {
		ids[i] = it.(providerItem).provider.ID
	}
	want := []string{"alpha", "mu", "zeta"}
	for i, w := range want {
		if ids[i] != w {
			t.Errorf("item[%d] = %q, want %q; got %v", i, ids[i], w, ids)
			break
		}
	}
}
