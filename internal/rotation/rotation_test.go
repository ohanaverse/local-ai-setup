package rotation

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ohanaverse/agent-worktree/internal/config"
)

// Next must advance the index each call and persist it to disk so the
// rotation survives process restarts. After cycling through all models it
// must wrap around. The state file must contain both the next index and
// the last selected model ID.
func TestNext_AdvancesAndPersists(t *testing.T) {
	dir := t.TempDir()
	models := []config.Model{
		{ID: "alpha"},
		{ID: "beta"},
		{ID: "gamma"},
	}
	r := New("code", models, dir)

	got := make([]string, 0, 4)
	for i := 0; i < 4; i++ {
		m, ok := r.Next("")
		if !ok {
			t.Fatalf("iteration %d: Next returned !ok", i)
		}
		got = append(got, m.ID)
	}

	// After 4 picks, the index has wrapped around once and returned to alpha.
	want := []string{"alpha", "beta", "gamma", "alpha"}
	if len(got) != len(want) {
		t.Fatalf("got %d picks, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("pick %d: got %q, want %q", i, got[i], want[i])
		}
	}

	// State file should reflect the next index (1, after advancing from
	// alpha at index 0) and last selected (alpha).
	data, err := os.ReadFile(StateFile(dir, "code"))
	if err != nil {
		t.Fatalf("read state file: %v", err)
	}
	gotState := string(data)
	wantState := "1\nalpha\n"
	if gotState != wantState {
		t.Errorf("state file = %q, want %q", gotState, wantState)
	}
}

// An empty rotation group (no models tagged with the requested tag) must
// return !ok so the caller can show a helpful message instead of panicking
// or entering an infinite loop.
func TestNext_EmptyGroup(t *testing.T) {
	r := New("code", nil, t.TempDir())
	if _, ok := r.Next(""); ok {
		t.Fatal("Next on empty group returned ok=true")
	}
}

// When the other tag group just used a model, Next must skip that model
// to avoid both code and design landing on the same model simultaneously.
// This prevents redundant usage and encourages variety.
func TestNext_CrossSkipsOtherTag(t *testing.T) {
	dir := t.TempDir()
	models := []config.Model{
		{ID: "alpha"},
		{ID: "beta"},
		{ID: "gamma"},
	}
	r := New("code", models, dir)

	// Write a fake "design" state that says design last picked "beta".
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(dir, "rotation-design.state"),
		[]byte("0\nbeta\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	m, ok := r.Next("design")
	if !ok {
		t.Fatal("Next returned !ok")
	}
	if m.ID == "beta" {
		t.Errorf("Next returned %q which design just used; expected cross-skip", m.ID)
	}
}

// When every candidate in the group matches the cross-skip target (e.g.
// a single-model group), Next must still return a model so the caller
// never gets stuck. This is the graceful-degradation path for small
// rotation groups.
func TestNext_CrossSkipFallsBackWhenAllCandidatesMatch(t *testing.T) {
	dir := t.TempDir()
	// Single-model group: every pick is "alpha", and "design" just used it.
	// Next must still return alpha so we always make progress.
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(dir, "rotation-design.state"),
		[]byte("0\nalpha\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	r := New("code", []config.Model{{ID: "alpha"}}, dir)
	m, ok := r.Next("design")
	if !ok {
		t.Fatal("Next returned !ok")
	}
	if m.ID != "alpha" {
		t.Errorf("fallback pick = %q, want alpha", m.ID)
	}
}

// ForTag builds a Rotation from only the models in cfg that carry the
// requested tag. Models without the tag must be excluded so the rotation
// doesn't pick an irrelevant model.
func TestForTag_FiltersModels(t *testing.T) {
	cfg := &config.Config{
		Models: []config.Model{
			{ID: "a", Tags: []string{"code"}},
			{ID: "b", Tags: []string{"design"}},
			{ID: "c", Tags: []string{"code", "design"}},
			{ID: "d"},
		},
	}
	r := ForTag(cfg, "code")
	if got := len(r.models); got != 2 {
		t.Fatalf("ForTag(code) returned %d models, want 2", got)
	}
	if r.models[0].ID != "a" || r.models[1].ID != "c" {
		t.Errorf("ForTag(code) returned %v, want [a, c]", r.models)
	}
}

// StateFile must return a predictable path under the given directory. This
// is the contract between the Rotation and the config package's
// LastSelected helper, which reads the same files.
func TestStateFilePath(t *testing.T) {
	got := StateFile("/tmp/cfg", "code")
	want := "/tmp/cfg/rotation-code.state"
	if got != want {
		t.Errorf("StateFile = %q, want %q", got, want)
	}
}

// The default state directory must respect XDG_CONFIG_HOME, matching the
// behaviour of config.Path(). A mismatch would mean rotation state and
// config files end up in different directories.
func TestStateFile_DefaultDirRespectsXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg-test")
	r := New("design", []config.Model{{ID: "x"}}, "")
	if got, want := r.StateDir(), "/tmp/xdg-test/agent-wt"; got != want {
		t.Errorf("StateDir with XDG = %q, want %q", got, want)
	}
}
