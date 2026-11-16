// Tests for the picker's marker and starting cursor once slot-profile rows
// exist: the marker reads last-picked.state, a usable profile picked last
// keeps the cursor, and every other case follows the rotation pointer.
package tui

import (
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rotation"
)

// slotCursorChecks is the two profiles these tests show: "broken" names a
// model the registry does not have, so its row is blocked; "ok" is an
// all-cloud launch row. File order is broken, then ok.
func slotCursorChecks() []profiles.SlotCheck {
	return []profiles.SlotCheck{
		slotCheckFor(0, "broken", "opus", map[string]string{"opus": "openrouter/missing"}),
		slotCheckFor(1, "ok", "opus", map[string]string{"opus": "openrouter/a"}),
	}
}

// slotCursorPicker enters the claude model phase on slotTestConfig with
// slotCursorChecks, after seed wrote the state files of earlier launches into
// the isolated state directory. It does not use slotPicker: that helper
// isolates the state directory itself, so nothing can be seeded before it.
func slotCursorPicker(t *testing.T, seed func(r *rotation.Rotation)) model {
	t.Helper()
	dir := tempStateDir(t)
	stubUsageStore(t)
	if seed != nil {
		seed(rotation.NewAt(dir))
	}
	cfg := slotTestConfig()
	// The rotation cases below expect openrouter/b to follow openrouter/a in
	// the registry's order, which is the order rotation walks.
	if i := config.IndexModelByID(cfg.Models, "openrouter/a"); i < 0 || i+1 >= len(cfg.Models) || cfg.Models[i+1].ID != "openrouter/b" {
		t.Fatalf("fixture: slotTestConfig must list openrouter/b right after openrouter/a")
	}
	m := model{cfg: cfg, agent: "claude", slotChecks: slotCursorChecks(), width: 80, height: 24}
	got := flowEnterKeepState(t, m, "claude")
	if got.phase != phaseModel {
		t.Fatalf("phase = %v (status %q), want phaseModel", got.phase, got.status)
	}
	// slotTestConfig has no native model, so the profile band is on top.
	if ids := itemIDs(got); len(ids) < 2 || ids[0] != "profile/broken" || ids[1] != "profile/ok" {
		t.Fatalf("fixture: rows = %v, want profile/broken then profile/ok first", ids)
	}
	return got
}

// markedIDs is the id of every row that carries the last-picked marker.
func markedIDs(m model) []string {
	var ids []string
	for _, it := range m.models.Items() {
		if mi := it.(*modelItem); mi.marked {
			ids = append(ids, mi.model.ID)
		}
	}
	return ids
}

// record is a seed step that fails the test when a state write fails.
func record(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("seed state: %v", err)
	}
}
