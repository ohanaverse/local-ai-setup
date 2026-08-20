package tui

import (
	"github.com/ohanaverse/agent-worktree/internal/config"
	"github.com/ohanaverse/agent-worktree/internal/rotation"
)

// positionAfterLastLaunched rebuilds the rotation snapshot for slot over
// models and positions the picker cursor on the model after the
// last-launched one, falling back to index 0 when there is no
// last-launched model or its ID is no longer in the snapshot. Shared by
// the picker-entry (selectedEntryMsg and phaseAgent Enter) paths so the
// cursor-positioning logic lives in one place. The Slot carries the
// (agent, tag, family) tuple that scopes per-launch rotation state,
// introduced in PR 3a.
func (m *model) positionAfterLastLaunched(slot rotation.Slot, models []config.Model) {
	m.rotation = rotation.NewForSlot(slot, models, "")
	if last, ok := m.rotation.LastLaunched(); ok {
		if next, ok := FindAfter(models, last); ok {
			if idx := indexOfModel(models, next); idx >= 0 {
				m.models.Select(idx)
			}
		}
	}
}
