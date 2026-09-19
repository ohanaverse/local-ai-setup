package tui

import (
	"os"
	"testing"

	"github.com/charmbracelet/bubbles/list"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/refcount"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/usage"
)

// compactModelList builds a model picker list the way production does for the
// selector table: buildTable rows (header as the list title, shared
// styleTableTitle) + ThemedListDelegate with ShowDescription=false and
// Spacing 0. Tests use it instead of hand-rolling list.New so
// filter/wrap/cursor assertions run against the real layout.
func compactModelList(t *testing.T, models []config.Model) list.Model {
	t.Helper()
	tbl := buildTable(tableInput{models: models, usage: newUsageStore()}, newRefcountStore(), "")
	delegate := ThemedListDelegate(themes.Default)
	delegate.ShowDescription = false
	delegate.SetSpacing(0)
	listItems := make([]list.Item, len(tbl.items))
	for i, it := range tbl.items {
		listItems[i] = it
	}
	ml := list.New(listItems, delegate, 78, 22)
	ml.Title = tbl.header
	styleTableTitle(&ml, themes.Default)
	ml.SetShowStatusBar(false)
	return ml
}

// tableItems builds picker items the way production does, for tests.
func tableItems(t *testing.T, cfg *config.Config, agent string, models []config.Model, snap *localmodels.Snapshot) modelTable {
	t.Helper()
	return buildTable(tableInput{cfg: cfg, agent: agent, models: models, inventory: snap, usage: newUsageStore()}, newRefcountStore(), "")
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

// stubRefcountStore swaps the newRefcountStore seam to a Store rooted at a
// fresh temp directory, so tests that build the model picker through a path
// that calls newRefcountStore() directly (rather than passing their own
// Store into buildTable) never read the developer's real
// ~/.config/agent-wt/refcount.jsonl — whose live "in use" counts would
// otherwise make the ref column (and any assertion on it) depend on host
// state. Mirrors stubUsageStore's isolation of usage.jsonl.
func stubRefcountStore(t *testing.T) refcount.Store {
	t.Helper()
	store := refcount.NewStoreAt(t.TempDir())
	old := newRefcountStore
	newRefcountStore = func() refcount.Store { return store }
	t.Cleanup(func() { newRefcountStore = old })
	return store
}

// TestMain stubs the live-provider inventory so no test in this package ever
// probes the developer's real ollama/omlx/mtplx servers or reads their model
// directories. Tests that need local rows call stubInventory.
func TestMain(m *testing.M) {
	runInventory = func(*config.Config) localmodels.Snapshot { return localmodels.Snapshot{} }
	os.Exit(m.Run())
}

// stubInventory makes enterModelPhase see snap for the duration of a test.
func stubInventory(t *testing.T, snap localmodels.Snapshot) {
	t.Helper()
	old := runInventory
	runInventory = func(*config.Config) localmodels.Snapshot { return snap }
	t.Cleanup(func() { runInventory = old })
}

// selectedModelID returns the ID of the model under the picker cursor, so
// cursor assertions stay valid regardless of the table's sort order.
func selectedModelID(m model) string {
	it, ok := m.models.SelectedItem().(*modelItem)
	if !ok {
		return ""
	}
	return it.model.ID
}
