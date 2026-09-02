package tui

import (
	"testing"

	"github.com/charmbracelet/bubbles/list"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/usage"
)

// compactModelList builds the model picker list exactly the way production
// enterModelPhase does: buildModelItems + ThemedListDelegate with
// ShowDescription=false and Spacing 0. Tests use it instead of hand-rolling
// list.New so filter/wrap/cursor assertions run against the real layout.
func compactModelList(t *testing.T, models []config.Model) list.Model {
	t.Helper()
	items := buildModelItems(models, newUsageStore())
	delegate := ThemedListDelegate(themes.Default)
	delegate.ShowDescription = false
	delegate.SetSpacing(0)
	listItems := make([]list.Item, len(items))
	for i, it := range items {
		listItems[i] = it
	}
	ml := list.New(listItems, delegate, 78, 22)
	ml.Title = "Models"
	ml.SetShowStatusBar(false)
	return ml
}

// stubUsageStore swaps the newUsageStore seam to a Store rooted at a fresh
// temp directory, so tests that build the model picker never read the
// developer's real ~/.config/agent-wt/usage.jsonl — whose event counts feed
// the family-usage sort, making fixture order and displayed counts depend
// on host state. Returns the stubbed store so a test can seed events via
// Record; the seam is restored on cleanup.
func stubUsageStore(t *testing.T) usage.Store {
	t.Helper()
	store := usage.NewStoreAt(t.TempDir())
	old := newUsageStore
	newUsageStore = func() usage.Store { return store }
	t.Cleanup(func() { newUsageStore = old })
	return store
}
