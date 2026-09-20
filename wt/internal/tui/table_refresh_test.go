package tui

import (
	"strings"
	"testing"

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

	got = got.refreshTable()

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
