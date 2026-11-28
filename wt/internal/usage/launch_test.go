package usage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRecordLaunchWritesTheMarkerAndTheUsageEvent verifies one RecordLaunch
// call leaves both things a launch owes: the picker's last-picked marker (one
// line, mode 0600) and one usage event. The two used to take two packages; a
// launch missing either shows a wrong marker or wrong counts in the picker.
func TestRecordLaunchWritesTheMarkerAndTheUsageEvent(t *testing.T) {
	dir := t.TempDir()
	s := NewStoreAt(dir)
	if err := s.RecordLaunch("claude", "openrouter/a"); err != nil {
		t.Fatalf("RecordLaunch: %v", err)
	}
	if got, ok := s.LastPicked(); !ok || got != "openrouter/a" {
		t.Errorf("LastPicked = %q, %v; want openrouter/a, true", got, ok)
	}
	path := filepath.Join(dir, "last-picked.state")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if string(data) != "openrouter/a\n" {
		t.Errorf("marker file = %q, want one line", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat marker: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("marker mode = %v, want 0600", info.Mode().Perm())
	}
	counts := s.CountsForAgent("claude", []string{"openrouter/a"})
	if counts["openrouter/a"].ThirtyDay != 1 {
		t.Errorf("launches = %d, want 1", counts["openrouter/a"].ThirtyDay)
	}
}

// TestRecordLaunchOverwritesTheMarker verifies a later launch replaces the
// marker while usage keeps a count per id: the marker is "the last row
// launched", the counts are history.
func TestRecordLaunchOverwritesTheMarker(t *testing.T) {
	s := NewStoreAt(t.TempDir())
	for _, id := range []string{"openrouter/a", "ollama/b"} {
		if err := s.RecordLaunch("claude", id); err != nil {
			t.Fatalf("RecordLaunch(%s): %v", id, err)
		}
	}
	if got, ok := s.LastPicked(); !ok || got != "ollama/b" {
		t.Errorf("LastPicked = %q, %v; want ollama/b, true", got, ok)
	}
	counts := s.CountsForAgent("claude", []string{"openrouter/a", "ollama/b"})
	for _, id := range []string{"openrouter/a", "ollama/b"} {
		if counts[id].ThirtyDay != 1 {
			t.Errorf("launches of %s = %d, want 1", id, counts[id].ThirtyDay)
		}
	}
}

// TestRecordLaunchKeepsUsageWhenTheMarkerCannotBeWritten verifies a marker
// that cannot be written is reported and costs nothing else: the launch is
// still counted. Both launch paths print the error as a note and launch
// anyway, so a launch dropped from usage.jsonl here would be missing from the
// picker's counts and from `wt stats`.
func TestRecordLaunchKeepsUsageWhenTheMarkerCannotBeWritten(t *testing.T) {
	dir := t.TempDir()
	s := NewStoreAt(dir)
	// A directory where the file belongs: the atomic rename onto it fails.
	if err := os.Mkdir(filepath.Join(dir, "last-picked.state"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	err := s.RecordLaunch("claude", "ollama/a")
	if err == nil || !strings.Contains(err.Error(), "last-picked.state not saved") {
		t.Fatalf("RecordLaunch error = %v, want one naming last-picked.state not saved", err)
	}
	if got := s.CountsForAgent("claude", []string{"ollama/a"})["ollama/a"].ThirtyDay; got != 1 {
		t.Errorf("launches = %d, want 1 (counted although the marker failed)", got)
	}
}

// TestLastPickedMissingOrEmpty verifies a missing, empty or whitespace-only
// marker reads as "nothing picked", so the picker starts with no marker
// instead of one on an empty id.
func TestLastPickedMissingOrEmpty(t *testing.T) {
	dir := t.TempDir()
	s := NewStoreAt(dir)
	if got, ok := s.LastPicked(); ok || got != "" {
		t.Errorf("missing file: LastPicked = %q, %v; want empty, false", got, ok)
	}
	path := filepath.Join(dir, "last-picked.state")
	for _, content := range []string{"", " \n\t\n"} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if got, ok := s.LastPicked(); ok || got != "" {
			t.Errorf("content %q: LastPicked = %q, %v; want empty, false", content, got, ok)
		}
	}
}

// TestLastPickedIgnoresALeftoverRotationState verifies rotation's old file is
// neither a fallback for the marker nor touched by a launch: rotation is gone,
// and an older install's rotation.state must neither resurrect a marker nor be
// rewritten.
func TestLastPickedIgnoresALeftoverRotationState(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "rotation.state")
	if err := os.WriteFile(old, []byte("openrouter/a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewStoreAt(dir)
	if got, ok := s.LastPicked(); ok || got != "" {
		t.Errorf("LastPicked = %q, %v; want empty, false", got, ok)
	}
	if err := s.RecordLaunch("claude", "ollama/b"); err != nil {
		t.Fatalf("RecordLaunch: %v", err)
	}
	data, err := os.ReadFile(old)
	if err != nil || string(data) != "openrouter/a\n" {
		t.Errorf("rotation.state = %q, %v; want it untouched", data, err)
	}

	fresh := t.TempDir()
	if err := NewStoreAt(fresh).RecordLaunch("claude", "ollama/b"); err != nil {
		t.Fatalf("RecordLaunch: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fresh, "rotation.state")); !os.IsNotExist(err) {
		t.Errorf("rotation.state created by RecordLaunch (stat err = %v)", err)
	}
}

// TestRecordLaunchAcceptsAProfileID verifies a slot-profile launch goes
// through the same call as a model launch and stores the profile id as the
// marker: the picker's marker may sit on a profile row.
func TestRecordLaunchAcceptsAProfileID(t *testing.T) {
	s := NewStoreAt(t.TempDir())
	if err := s.RecordLaunch("claude", "profile/work"); err != nil {
		t.Fatalf("RecordLaunch: %v", err)
	}
	if got, ok := s.LastPicked(); !ok || got != "profile/work" {
		t.Errorf("LastPicked = %q, %v; want profile/work, true", got, ok)
	}
}
