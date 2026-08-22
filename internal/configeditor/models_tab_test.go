package configeditor

import (
	"strings"
	"testing"

	"github.com/ohanaverse/agent-worktree/internal/config"
)

// TestModelsTab_SortByProviderFamilyName verifies models sort first by
// provider, then family, then model name. This groups related models
// together so the user can scan a provider's catalog efficiently.
func TestModelsTab_SortByProviderFamilyName(t *testing.T) {
	cfg := &config.Config{
		Models: []config.Model{
			{ID: "ollama/b", ProviderID: "ollama", Family: "gemma4", ModelName: "b"},
			{ID: "agy/a", ProviderID: "agy", Family: "-", ModelName: "a"},
			{ID: "ollama/a", ProviderID: "ollama", Family: "gemma4", ModelName: "a"},
		},
	}
	l := buildModelsList(testTheme(), 80, 24, cfg)
	if len(l.Items()) != 3 {
		t.Fatalf("expected 3 items, got %d", len(l.Items()))
	}

	ids := make([]string, 3)
	for i, it := range l.Items() {
		ids[i] = it.(modelItem).model.ID
	}
	want := []string{"agy/a", "ollama/a", "ollama/b"}
	for i, w := range want {
		if ids[i] != w {
			t.Errorf("item[%d] = %q, want %q; got %v", i, ids[i], w, ids)
			break
		}
	}
}

// TestModelsTab_Title_UsesFullID verifies that the title displays the model's
// full ID rather than a provider/model_name split. This matches the registry
// key users see elsewhere.
func TestModelsTab_Title_UsesFullID(t *testing.T) {
	cfg := &config.Config{
		Models: []config.Model{{ID: "ollama/gemma4:9b", ProviderID: "ollama", ModelName: "gemma4:9b"}},
	}
	l := buildModelsList(testTheme(), 80, 24, cfg)
	title := l.Items()[0].(modelItem).Title()
	if !strings.Contains(title, "ollama/gemma4:9b") {
		t.Errorf("title %q should contain full model ID", title)
	}
}

// TestModelsTab_Title_ResolvesInheritedLocation verifies that when a model
// has no explicit location, the title resolves the provider's location.
func TestModelsTab_Title_ResolvesInheritedLocation(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "ollama", Location: config.LocationCloud}},
		Models:    []config.Model{{ID: "ollama/gemma4:9b", ProviderID: "ollama", Location: ""}},
	}
	l := buildModelsList(testTheme(), 80, 24, cfg)
	title := l.Items()[0].(modelItem).Title()
	if !strings.Contains(title, "CLOUD") {
		t.Errorf("title %q should resolve inherited cloud location", title)
	}
}
