package modeleditor

import "testing"

// TestModelWriteResetsAliasesSnapshot pins that a Models-tab save or removal
// makes the Aliases tab read the registry again on its next visit: its
// snapshot (targets, windows, shrink check) would otherwise not know the
// model (issue #688).
func TestModelWriteResetsAliasesSnapshot(t *testing.T) {
	t.Run("saved", func(t *testing.T) {
		m := &model{}
		m.models.form = &modelForm{}
		m.aliases.asked = true
		m.applyModelSaved(modelSavedMsg{id: "ollama/new", changed: true})
		if m.aliases.asked {
			t.Error("aliases.asked stays true after a model save")
		}
	})
	t.Run("removed", func(t *testing.T) {
		m := &model{}
		m.aliases.asked = true
		m.applyModelRemoved(modelRemovedMsg{id: "ollama/old"})
		if m.aliases.asked {
			t.Error("aliases.asked stays true after a model removal")
		}
	})
}
