package refcount

import (
	"os"
	"testing"
)

// TestReleaseKeepsOtherPidsWithCorruptLines verifies a corrupt line in the
// state file does not stop Release from preserving other live sessions'
// entries (the corrupt line itself is dropped, as in Sweep) — a damaged file
// must never cause a live session's model to look unused.
func TestReleaseKeepsOtherPidsWithCorruptLines(t *testing.T) {
	store := NewStoreAt(t.TempDir())
	if err := store.Record(222, "ollama/a"); err != nil {
		t.Fatalf("Record: %v", err)
	}
	seed, err := os.ReadFile(store.path())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := os.WriteFile(store.path(), append([]byte("not json\n"), seed...), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := store.Record(111, "ollama/a"); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := store.Release(111); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if got := store.Counts([]string{"ollama/a"})["ollama/a"]; got != 1 {
		t.Fatalf("Counts = %d, want 1 (pid 222 preserved)", got)
	}
}

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
