package tui

import (
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/usage"
)

// stubUsageStore swaps the newUsageStore seam to a Store rooted at a fresh
// temp directory, so tests that build the model picker never read the
// developer's real ~/.config/agent-wt/usage.jsonl — whose event counts feed
// the family-usage sort, making fixture order and displayed counts depend
// on host state. Returns the stubbed store so a test can seed events via
// Record; the seam is restored on cleanup.
func stubUsageStore(t *testing.T) *usage.Store {
	t.Helper()
	store := usage.NewStoreAt(t.TempDir())
	old := newUsageStore
	newUsageStore = func() *usage.Store { return store }
	t.Cleanup(func() { newUsageStore = old })
	return store
}