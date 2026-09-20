package tui

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/refcount"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/usage"
)

// compactModelList builds a 78x22 list.Model from buildTable rows for models
// (no config, inventory or rotation marker; header as the list title via
// styleTableTitle) with ThemedListDelegate at ShowDescription=false and
// Spacing 0 — the production picker layout. Tests use it instead of
// hand-rolling list.New so filter/wrap/cursor assertions run against it.
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

// stubUsageStore swaps the newUsageStore seam to a Store rooted at a fresh
// temp directory, so tests that build the model picker never read the
// developer's real ~/.config/agent-wt/usage.jsonl — whose event counts feed
// the picker's 1d/7d/30d columns and the 7d-usage tie-break, making displayed
// counts and row order depend on host state. Returns the stubbed store so a test can seed events via
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
// directories, and stubs startModel so no test can start a real model
// process. Tests that need local rows call stubInventory; tests that exercise
// the start flow call stubStartModel.
func TestMain(m *testing.M) {
	runInventory = func(*config.Config) localmodels.Snapshot { return localmodels.Snapshot{} }
	startModel = func(context.Context, *config.Config, lifecycle.Target, lifecycle.Options) error {
		return errors.New("startModel not stubbed in this test")
	}
	// Post-exit seams: no test may rewrite the real refcount file or offer to
	// stop the developer's running models.
	releaseSession = func() {}
	runStopPhase = func(*config.Config) {}
	os.Exit(m.Run())
}

// stubInventory makes both enterModelPhase and newPickModel see snap (via the
// runInventory seam) for the duration of a test.
func stubInventory(t *testing.T, snap localmodels.Snapshot) {
	t.Helper()
	old := runInventory
	runInventory = func(*config.Config) localmodels.Snapshot { return snap }
	t.Cleanup(func() { runInventory = old })
}

// drainCmds executes cmd and feeds each resulting message back through Update,
// following the command chain (tea.Batch included) the way the tea runtime
// would, and returns the settled model. A test asserting on state a command
// produces — a refreshed table, a repopulated filter — must drain it: calling
// the command and discarding its message proves nothing, because the message
// is what applies the change.
func drainCmds(t *testing.T, m model, cmd tea.Cmd) model {
	t.Helper()
	queue := []tea.Cmd{cmd}
	for i := 0; len(queue) > 0; i++ {
		if i > 32 {
			t.Fatal("drainCmds: command chain did not settle")
		}
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		msg := c()
		if msg == nil {
			continue
		}
		// tea.Batch returns a BatchMsg carrying the commands to run, not a
		// message Update knows how to handle; unwrap it instead of routing it.
		if batch, ok := msg.(tea.BatchMsg); ok {
			queue = append(queue, batch...)
			continue
		}
		next, nextCmd := m.Update(msg)
		m = next.(model)
		queue = append(queue, nextCmd)
	}
	return m
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
