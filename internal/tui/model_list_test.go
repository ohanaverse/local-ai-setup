package tui

import (
	"testing"

	"github.com/ohanaverse/agent-worktree/internal/config"
)

// FindAfter must return the model at target+1, wrapping to models[0]
// when target is the last item. The picker uses this to position the
// cursor at the next rotation entry.
func TestFindAfterMiddle(t *testing.T) {
	models := []config.Model{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	got, ok := FindAfter(models, config.Model{ID: "b"})
	if !ok || got.ID != "c" {
		t.Errorf("FindAfter = (%q, %v), want (c, true)", got.ID, ok)
	}
}

// FindAfter must return models[0] when target is not in the list.
// Mirrors the rotation.FirstAfter contract.
func TestFindAfterMissing(t *testing.T) {
	models := []config.Model{{ID: "a"}}
	got, ok := FindAfter(models, config.Model{ID: "ghost"})
	if !ok || got.ID != "a" {
		t.Errorf("FindAfter = (%q, %v), want (a, true)", got.ID, ok)
	}
}
