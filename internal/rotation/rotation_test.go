package rotation

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ohanaverse/agent-worktree/internal/config"
)

// LastLaunched must return (zero, false) when no state file exists so
// the caller (the picker entry handler) can fall back to index 0
// without panicking or returning a zero-value model that the snapshot
// contains.
func TestLastLaunchedMissingFile(t *testing.T) {
	r := New("code", []config.Model{{ID: "alpha"}}, t.TempDir())
	if _, ok := r.LastLaunched(); ok {
		t.Fatal("LastLaunched on missing file returned ok=true")
	}
}

// RecordLaunch must persist the model ID and LastLaunched must read
// it back. The state file is the on-disk contract for "what was last
// launched in this rotation" — without the round-trip the picker
// can't advance after a launch.
func TestRecordLaunchRoundTrip(t *testing.T) {
	dir := t.TempDir()
	r := New("code", []config.Model{{ID: "alpha"}}, dir)
	if err := r.RecordLaunch(config.Model{ID: "alpha"}); err != nil {
		t.Fatalf("RecordLaunch: %v", err)
	}
	got, ok := r.LastLaunched()
	if !ok {
		t.Fatal("LastLaunched returned !ok after RecordLaunch")
	}
	if got.ID != "alpha" {
		t.Errorf("LastLaunched.ID = %q, want alpha", got.ID)
	}
}

// RecordLaunch must overwrite any prior value. The picker may launch
// the same model multiple times in a row if the user keeps pressing
// Enter without navigating; each launch must normalize the file to
// the latest pick.
func TestRecordLaunchOverwrites(t *testing.T) {
	dir := t.TempDir()
	r := New("code", []config.Model{{ID: "alpha"}, {ID: "beta"}}, dir)
	_ = r.RecordLaunch(config.Model{ID: "alpha"})
	_ = r.RecordLaunch(config.Model{ID: "beta"})
	got, _ := r.LastLaunched()
	if got.ID != "beta" {
		t.Errorf("LastLaunched.ID = %q, want beta (overwritten)", got.ID)
	}
}

// LastLaunched must read the legacy 2-line state file by taking the
// last non-empty line. Without backward-compat the existing state
// files on every user's machine would suddenly point at nothing
// and the picker would fall back to index 0 — losing the rotation
// memory they had.
func TestLastLaunchedReadsLegacyTwoLineFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "rotation-code.state"), []byte("5\nollama/x:cloud\n"), 0o600); err != nil {
		t.Fatalf("write legacy state: %v", err)
	}
	r := New("code", []config.Model{{ID: "ollama/x:cloud"}}, dir)
	got, ok := r.LastLaunched()
	if !ok {
		t.Fatal("LastLaunched returned !ok on legacy 2-line file")
	}
	if got.ID != "ollama/x:cloud" {
		t.Errorf("LastLaunched.ID = %q, want ollama/x:cloud", got.ID)
	}
}

// LastLaunched must return (zero, false) when the saved ID is no
// longer in the snapshot. Config changes between launches (a model
// was removed) should not crash or return a phantom model; the
// caller falls back to index 0.
func TestLastLaunchedConfigChanged(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "rotation-code.state"), []byte("ollama/removed:cloud\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := New("code", []config.Model{{ID: "ollama/current:cloud"}}, dir)
	if _, ok := r.LastLaunched(); ok {
		t.Error("LastLaunched returned ok=true for ID not in snapshot")
	}
}

// RecordLaunch must write a single-line file (no index prefix) and
// the file must be 0600. The single-line format is the new on-disk
// contract; 0600 is the existing security baseline.
func TestRecordLaunchWritesSingleLine(t *testing.T) {
	dir := t.TempDir()
	r := New("code", []config.Model{{ID: "alpha"}}, dir)
	if err := r.RecordLaunch(config.Model{ID: "alpha"}); err != nil {
		t.Fatalf("RecordLaunch: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, "rotation-code.state"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("perm = %o, want 0600", perm)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "rotation-code.state"))
	if got := string(data); got != "alpha\n" {
		t.Errorf("file = %q, want %q", got, "alpha\n")
	}
}

// FirstAfter must return the model at target+1, wrapping to models[0]
// if target is the last item. The picker uses this to compute "what
// to show on the next entry after a launch" without knowing the
// picker's order at the call site.
func TestFirstAfterMiddle(t *testing.T) {
	models := []config.Model{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	got, ok := FirstAfter(models, config.Model{ID: "b"})
	if !ok {
		t.Fatal("FirstAfter returned !ok")
	}
	if got.ID != "c" {
		t.Errorf("FirstAfter = %q, want c", got.ID)
	}
}

// FirstAfter must wrap to the first model when target is the last.
// Without wrapping the picker would advance past the end of the
// list on every cycle and the cursor would never move.
func TestFirstAfterWraps(t *testing.T) {
	models := []config.Model{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	got, ok := FirstAfter(models, config.Model{ID: "c"})
	if !ok {
		t.Fatal("FirstAfter returned !ok")
	}
	if got.ID != "a" {
		t.Errorf("FirstAfter = %q, want a (wrap to start)", got.ID)
	}
}

// FirstAfter must return models[0] when target is not in the list.
// This is the "saved ID was removed from config" recovery path —
// the picker falls back to the first model instead of failing.
func TestFirstAfterMissingTarget(t *testing.T) {
	models := []config.Model{{ID: "a"}, {ID: "b"}}
	got, ok := FirstAfter(models, config.Model{ID: "ghost"})
	if !ok {
		t.Fatal("FirstAfter returned !ok")
	}
	if got.ID != "a" {
		t.Errorf("FirstAfter = %q, want a (fallback to first)", got.ID)
	}
}

// FirstAfter must return (zero, false) on an empty snapshot. The
// picker's validation gate at selectedEntryMsg prevents this in
// practice, but the helper must defend against it.
func TestFirstAfterEmpty(t *testing.T) {
	if _, ok := FirstAfter(nil, config.Model{ID: "x"}); ok {
		t.Error("FirstAfter on empty snapshot returned ok=true")
	}
}

// StateFile must return a predictable path under the given directory.
// This is the contract between the Rotation and the config package's
// LastSelected helper, which reads the same files.
func TestStateFilePath(t *testing.T) {
	got := StateFile("/tmp/cfg", "code")
	want := "/tmp/cfg/rotation-code.state"
	if got != want {
		t.Errorf("StateFile = %q, want %q", got, want)
	}
}

// The default state directory must respect XDG_CONFIG_HOME, matching
// the behaviour of config.Path(). A mismatch would mean rotation
// state and config files end up in different directories.
func TestStateFile_DefaultDirRespectsXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg-test")
	r := New("design", []config.Model{{ID: "x"}}, "")
	if got, want := r.StateDir(), "/tmp/xdg-test/agent-wt"; got != want {
		t.Errorf("StateDir with XDG = %q, want %q", got, want)
	}
}
