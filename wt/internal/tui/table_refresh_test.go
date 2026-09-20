package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/list"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// runningCell reports whether a rendered row line shows the RUNNING column's
// "run" flag (fields are checked whole so "run" cannot match another cell).
func runningCell(line string) bool {
	for _, f := range strings.Fields(line) {
		if f == "run" {
			return true
		}
	}
	return false
}

// TestRefreshTableReprobesAndKeepsCursor verifies refreshTable re-runs the
// live inventory and updates each row's RUNNING state in place while keeping
// the cursor on the same model id. After a failed replace the old occupant is
// no longer running; a table that still showed it as running would lie.
func TestRefreshTableReprobesAndKeepsCursor(t *testing.T) {
	// Arrange: a cfg with an omlx provider and two local models omlx/a and
	// omlx/b (the fixture shape of TestEnterModelPhaseAllLocalNoneRunningShows
	// BlockedRows); the FIRST probe reports omlx/a running, later probes report
	// nothing running. The probe count is tracked in the stub closure.
	cfg := &config.Config{
		DefaultTag: "code",
		Providers:  []config.Provider{{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}}},
		Models: []config.Model{
			{ID: "omlx/a", ProviderID: "omlx", ModelName: "a", Family: "a", Tags: []string{"code"}},
			{ID: "omlx/b", ProviderID: "omlx", ModelName: "b", Family: "b", Tags: []string{"code"}},
		},
		Agents: []config.Agent{{Name: "pi", SupportedProviders: []string{"omlx"}}},
	}
	cfg.ExposeAllForTest()

	probes := 0
	old := runInventory
	runInventory = func(*config.Config) localmodels.Snapshot {
		probes++
		if probes == 1 {
			return localmodels.Snapshot{Entries: []localmodels.Entry{
				{ProviderID: "omlx", Artifact: "a", ModelID: "omlx/a", Registered: true, Running: true},
			}}
		}
		return localmodels.Snapshot{}
	}
	t.Cleanup(func() { runInventory = old })

	// Act: enter the model phase, move the cursor to omlx/b, then refresh.
	got := flowEnter(t, model{cfg: cfg, width: 80, height: 24}, "pi")
	idxB := indexOfID(got, "omlx/b")
	if idxB < 0 {
		t.Fatalf("no omlx/b row in %v", itemIDs(got))
	}
	got.models.Select(idxB)
	idxA := indexOfID(got, "omlx/a")
	if idxA < 0 {
		t.Fatalf("no omlx/a row in %v", itemIDs(got))
	}
	before := got.models.Items()[idxA].(*modelItem)
	if !runningCell(before.line) {
		t.Fatalf("precondition: omlx/a line %q should show run before refresh", before.line)
	}

	got, refreshCmd := got.refreshTable()
	got = drainCmds(t, got, refreshCmd)

	// Assert: omlx/a's RUNNING cell flips to "-", the cursor is still on
	// omlx/b, and the inventory was probed one more time (enterModelPhase's
	// build was the first).
	idxA = indexOfID(got, "omlx/a")
	if idxA < 0 {
		t.Fatalf("omlx/a row missing after refresh: %v", itemIDs(got))
	}
	after := got.models.Items()[idxA].(*modelItem)
	if runningCell(after.line) {
		t.Errorf("omlx/a line %q still shows run after refresh", after.line)
	}
	if id := selectedModelID(got); id != "omlx/b" {
		t.Errorf("cursor = %q, want omlx/b (refresh must keep the cursor)", id)
	}
	if probes != 2 {
		t.Errorf("runInventory calls = %d, want 2", probes)
	}
}

// refreshTestCfg is the picker config shared by the refresh tests below: one
// local omlx provider offering two models, so a filter can narrow the table and
// the cursor has somewhere to move.
func refreshTestCfg() *config.Config {
	cfg := &config.Config{
		DefaultTag: "code",
		Providers:  []config.Provider{{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}}},
		Models: []config.Model{
			{ID: "omlx/qwen-a", ProviderID: "omlx", ModelName: "qwen-a", Family: "qwen-a", Tags: []string{"code"}},
			{ID: "omlx/qwen-b", ProviderID: "omlx", ModelName: "qwen-b", Family: "qwen-b", Tags: []string{"code"}},
		},
		Agents: []config.Agent{{Name: "pi", SupportedProviders: []string{"omlx"}}},
	}
	cfg.ExposeAllForTest()
	return cfg
}

// refreshTestSnapshot reports both refreshTestCfg models present and not
// running.
func refreshTestSnapshot() localmodels.Snapshot {
	return localmodels.Snapshot{Entries: []localmodels.Entry{
		{ProviderID: "omlx", Artifact: "qwen-a", ModelID: "omlx/qwen-a", Registered: true},
		{ProviderID: "omlx", Artifact: "qwen-b", ModelID: "omlx/qwen-b", Registered: true},
	}}
}

// TestRefreshTableProbesOffTheUpdateLoop verifies refreshTable hands the
// inventory probe to a tea.Cmd instead of running it inside Update. The probe
// dials ollama, omlx/mtplx and mlx_lm_server with multi-second per-request
// timeouts, so probing inline would freeze the picker with no repaint on every
// failed or cancelled start — worst in a retry loop, where each attempt
// (Enter, fail, Enter, fail) re-freezes for the probe's duration.
func TestRefreshTableProbesOffTheUpdateLoop(t *testing.T) {
	var probes int
	old := runInventory
	runInventory = func(*config.Config) localmodels.Snapshot {
		probes++
		return refreshTestSnapshot()
	}
	t.Cleanup(func() { runInventory = old })

	got := flowEnter(t, model{cfg: refreshTestCfg(), width: 80, height: 24}, "pi")
	afterEnter := probes
	if afterEnter == 0 {
		t.Fatal("precondition: entering the model phase should have probed once")
	}

	got, cmd := got.refreshTable()
	if cmd == nil {
		t.Fatal("refreshTable returned a nil cmd; the probe must be deferred to one")
	}
	if probes != afterEnter {
		t.Errorf("probes = %d right after refreshTable, want %d — the probe ran on the update loop",
			probes, afterEnter)
	}

	_ = drainCmds(t, got, cmd)
	if probes != afterEnter+1 {
		t.Errorf("probes = %d after draining the cmd, want %d", probes, afterEnter+1)
	}
}

// TestRefreshTableKeepsFilteredRowsVisible verifies a refresh while the
// picker's "/" filter is applied leaves the matching rows on screen. bubbles'
// SetItems clears filteredItems when a filter is applied and only its returned
// command repopulates them, so a refresh that drops that command empties the
// list: the picker renders "No items.", Enter does nothing (SelectedItem is
// nil), and esc — the list's own "clear filter" — is consumed by the app's esc
// chain and quits wt, so the user cannot recover. Any failed or cancelled start
// reaches this. Regression measured before the fix: visible 2 -> 0.
func TestRefreshTableKeepsFilteredRowsVisible(t *testing.T) {
	stubInventory(t, refreshTestSnapshot())

	got := flowEnter(t, model{cfg: refreshTestCfg(), width: 80, height: 24}, "pi")
	got.models.SetFilterText("qwen")
	if n := len(got.models.VisibleItems()); n != 2 {
		t.Fatalf("precondition: filter shows %d rows, want the 2 qwen rows", n)
	}

	got, cmd := got.refreshTable()
	got = drainCmds(t, got, cmd)

	if n := len(got.models.VisibleItems()); n == 0 {
		t.Fatalf("VisibleItems() = 0 after a refresh under an applied filter; view = %q", got.models.View())
	}
	if got.models.SelectedItem() == nil {
		t.Error("SelectedItem() = nil after refresh, so Enter would do nothing")
	}
	if got.models.FilterState() != list.FilterApplied {
		t.Errorf("FilterState() = %v, want the user's filter still applied", got.models.FilterState())
	}
}

// TestRefreshTableKeepsCursorUnderFilter verifies the cursor returns to the same
// model after a refresh with a filter applied. Once a filter is applied
// bubbles' Select/Index are visible-item coordinates, and the visible order
// only exists after SetItems' filter command lands — so the refresh must
// re-place the cursor from the repopulated list, not replay an index computed
// over the unfiltered one.
func TestRefreshTableKeepsCursorUnderFilter(t *testing.T) {
	stubInventory(t, refreshTestSnapshot())

	got := flowEnter(t, model{cfg: refreshTestCfg(), width: 80, height: 24}, "pi")
	got.models.SetFilterText("qwen")
	// Move off index 0 so "kept the cursor" cannot pass by accident, since 0 is
	// what a reset would produce anyway.
	got.models.Select(1)
	want := selectedModelID(got)
	if want == "" || want == "omlx/qwen-a" {
		t.Fatalf("fixture: cursor = %q, want the second qwen row", want)
	}

	got, cmd := got.refreshTable()
	got = drainCmds(t, got, cmd)

	if gotID := selectedModelID(got); gotID != want {
		t.Errorf("cursor = %q after refresh, want %q", gotID, want)
	}
}
