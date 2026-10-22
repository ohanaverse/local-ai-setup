// Tests for the picker's marker and starting cursor once slot-profile rows
// exist: the marker reads last-picked.state, a usable profile picked last
// keeps the cursor, and every other case follows the rotation pointer.
package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
	stubRefcountStore(t)
	if seed != nil {
		seed(rotation.NewAt(dir))
	}
	cfg := slotTestConfig()
	// The rotation cases below expect openrouter/b to follow openrouter/a in
	// the registry's order, which is the order rotation walks.
	if i := config.IndexModelByID(cfg.Models, "openrouter/a"); i < 0 || i+1 >= len(cfg.Models) || cfg.Models[i+1].ID != "openrouter/b" {
		t.Fatalf("fixture: slotTestConfig must list openrouter/b right after openrouter/a")
	}
	models, err := cfg.EligibleModels("claude", "", "")
	if err != nil {
		t.Fatalf("EligibleModels: %v", err)
	}
	m := model{cfg: cfg, agent: "claude", slotChecks: slotCursorChecks(), width: 80, height: 24}
	got, _ := m.enterModelPhase("claude", models, "code")
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

// TestSlotStartingCursor pins where the picker opens and which row carries
// the "> " marker, for each state a profile launch can leave behind. A bare
// Enter launches the row under the cursor, so a wrong rule here launches
// something the user did not expect: a profile must be offered again after
// it was used (profiles do not rotate), and must never capture the cursor
// when it is blocked, gone, or was followed by a single-model launch.
func TestSlotStartingCursor(t *testing.T) {
	tests := []struct {
		name       string
		seed       func(t *testing.T, r *rotation.Rotation)
		wantCursor string
		wantMarked []string
	}{
		{
			name: "a usable profile was picked last",
			seed: func(t *testing.T, r *rotation.Rotation) {
				record(t, r.RecordFor("claude", "openrouter/a"))
				record(t, r.RecordProfile("claude", "profile/ok"))
			},
			wantCursor: "profile/ok",
			wantMarked: []string{"profile/ok"},
		},
		{
			name: "a blocked profile was picked last",
			seed: func(t *testing.T, r *rotation.Rotation) {
				record(t, r.RecordFor("claude", "openrouter/a"))
				record(t, r.RecordProfile("claude", "profile/broken"))
			},
			wantCursor: "openrouter/b",
			wantMarked: []string{"profile/broken"},
		},
		{
			name: "the profile picked last is no longer in the file",
			seed: func(t *testing.T, r *rotation.Rotation) {
				record(t, r.RecordFor("claude", "openrouter/a"))
				record(t, r.RecordProfile("claude", "profile/gone"))
			},
			wantCursor: "openrouter/b",
			wantMarked: nil,
		},
		{
			name: "a model was launched after a profile",
			seed: func(t *testing.T, r *rotation.Rotation) {
				record(t, r.RecordProfile("claude", "profile/ok"))
				record(t, r.RecordFor("claude", "openrouter/a"))
			},
			wantCursor: "openrouter/b",
			wantMarked: []string{"openrouter/a"},
		},
		{
			name: "state from before last-picked.state existed",
			seed: func(t *testing.T, r *rotation.Rotation) {
				record(t, r.RecordFor("claude", "openrouter/a"))
				record(t, os.Remove(filepath.Join(config.Dir(), "last-picked.state")))
			},
			wantCursor: "openrouter/b",
			wantMarked: []string{"openrouter/a"},
		},
		{
			name:       "no state: the first row that is not blocked",
			seed:       nil,
			wantCursor: "profile/ok",
			wantMarked: nil,
		},
		{
			name: "a blocked profile is the only launch so far",
			seed: func(t *testing.T, r *rotation.Rotation) {
				record(t, r.RecordProfile("claude", "profile/broken"))
			},
			wantCursor: "profile/ok",
			wantMarked: []string{"profile/broken"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var seed func(*rotation.Rotation)
			if tt.seed != nil {
				seed = func(r *rotation.Rotation) { tt.seed(t, r) }
			}
			got := slotCursorPicker(t, seed)
			if id := selectedModelID(got); id != tt.wantCursor {
				t.Errorf("cursor on %q, want %q (rows %v)", id, tt.wantCursor, itemIDs(got))
			}
			if marked := markedIDs(got); !slices.Equal(marked, tt.wantMarked) {
				t.Errorf("marked rows = %v, want %v", marked, tt.wantMarked)
			}
		})
	}
}

// TestSlotMarkerSurvivesTableRefresh verifies a rebuilt table (the refresh
// after a failed or cancelled start) reads the marker the way the first build
// did. If the refresh read rotation.state, the marker would jump from the
// profile to the last single model as soon as a start was cancelled.
func TestSlotMarkerSurvivesTableRefresh(t *testing.T) {
	m := slotCursorPicker(t, func(r *rotation.Rotation) {
		record(t, r.RecordFor("claude", "openrouter/a"))
		record(t, r.RecordProfile("claude", "profile/ok"))
	})

	next, cmd := m.refreshTable()
	got := drainCmds(t, next, cmd)

	if marked := markedIDs(got); !slices.Equal(marked, []string{"profile/ok"}) {
		t.Errorf("marked rows after a refresh = %v, want [profile/ok]", marked)
	}
	if id := selectedModelID(got); id != "profile/ok" {
		t.Errorf("cursor after a refresh on %q, want it kept on profile/ok", id)
	}
}

// TestLaunchAndRecordMovesMarkerOffProfile verifies the TUI's single-model
// commit point writes last-picked.state as well as rotation.state. If it
// wrote rotation.state alone, the marker would stay on a profile launched
// earlier and the cursor would start there again, although the user's last
// launch was a single model.
func TestLaunchAndRecordMovesMarkerOffProfile(t *testing.T) {
	dir := tempStateDir(t)
	record(t, rotation.NewAt(dir).RecordProfile("claude", "profile/ok"))
	m := phaseModelWithList(t, testConfig(), "claude", "code")
	first, ok := m.models.Items()[0].(*modelItem)
	if !ok {
		t.Fatalf("items[0] is %T, want *modelItem", m.models.Items()[0])
	}
	m.launchModel = first.model
	// The returned command would run the agent; it is dropped, so nothing runs.
	m, _ = m.launchAndRecord(exec.Command("true"))

	for _, name := range []string{"rotation.state", "last-picked.state"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if want := first.model.ID + "\n"; string(data) != want {
			t.Errorf("%s = %q, want %q", name, data, want)
		}
	}
	if got, _ := rotation.NewAt(dir).LastPicked(); got != first.model.ID {
		t.Errorf("LastPicked = %q, want %q", got, first.model.ID)
	}
}
