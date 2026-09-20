package refcount

import "testing"

// TestReleaseDropsOnlyThatPid verifies Release removes every entry for the
// given pid and leaves other sessions' entries alone. It matters because the
// post-exit model-stopping picker releases wt's own entry before asking which
// models are unused; if it removed another live session's entry, that
// session's model could be offered for stopping while still in use.
func TestReleaseDropsOnlyThatPid(t *testing.T) {
	store := NewStoreAt(t.TempDir())
	if err := store.Record(111, "ollama/a"); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := store.Record(222, "ollama/a"); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := store.Release(111); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if got := store.Counts([]string{"ollama/a"})["ollama/a"]; got != 1 {
		t.Fatalf("Counts = %d, want 1 (only pid 222 remains)", got)
	}
}

// TestReleaseMissingFileIsNoop verifies Release on a store that never
// recorded anything succeeds. Post-exit calls it unconditionally (command
// agents like shell never Record), so an error here would print a spurious
// warning after every shell session.
func TestReleaseMissingFileIsNoop(t *testing.T) {
	store := NewStoreAt(t.TempDir())
	if err := store.Release(111); err != nil {
		t.Fatalf("Release on empty store: %v", err)
	}
}
