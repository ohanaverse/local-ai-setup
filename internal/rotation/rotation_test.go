package rotation

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ohanaverse/agent-worktree/internal/config"
)

// TestLastMissingFile returns no last launch when rotation.state is absent.
func TestLastMissingFile(t *testing.T) {
	r := NewAt(t.TempDir())
	if _, ok := r.Last(); ok {
		t.Fatal("Last on missing file returned ok=true")
	}
}

// TestRecordAndLastRoundTrip verifies Record writes the model ID and Last
// reads it back.
func TestRecordAndLastRoundTrip(t *testing.T) {
	dir := t.TempDir()
	r := NewAt(dir)
	if err := r.Record("alpha"); err != nil {
		t.Fatalf("Record: %v", err)
	}
	got, ok := r.Last()
	if !ok {
		t.Fatal("Last returned !ok after Record")
	}
	if got != "alpha" {
		t.Errorf("Last = %q, want alpha", got)
	}
}

// TestRecordOverwrites verifies subsequent launches update the saved model ID.
func TestRecordOverwrites(t *testing.T) {
	dir := t.TempDir()
	r := NewAt(dir)
	_ = r.Record("alpha")
	_ = r.Record("beta")
	got, _ := r.Last()
	if got != "beta" {
		t.Errorf("Last = %q, want beta", got)
	}
}

// TestRecordWritesSingleLineAnd0600 verifies the new state file format and
// permissions.
func TestRecordWritesSingleLineAnd0600(t *testing.T) {
	dir := t.TempDir()
	r := NewAt(dir)
	if err := r.Record("alpha"); err != nil {
		t.Fatalf("Record: %v", err)
	}
	path := r.statePath()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("perm = %o, want 0600", perm)
	}
	data, _ := os.ReadFile(path)
	if got := string(data); got != "alpha\n" {
		t.Errorf("file = %q, want %q", got, "alpha\n")
	}
}

// TestNextReturnsFirstEligibleWhenEmpty returns the first agent-eligible
// model when there is no prior launch.
func TestNextReturnsFirstEligibleWhenEmpty(t *testing.T) {
	cfg := &config.Config{
		Models: []config.Model{
			{ID: "ollama/a", ProviderID: "ollama"},
			{ID: "claude/sonnet", ProviderID: "claude"},
		},
		Agents: []config.Agent{
			{Name: "claude", SupportedProviders: []string{"claude"}},
		},
	}
	r := NewAt(t.TempDir())
	got, ok := r.Next(cfg, "claude", "", "")
	if !ok || got.ID != "claude/sonnet" {
		t.Fatalf("Next = %q, %v; want claude/sonnet, true", got.ID, ok)
	}
}

// TestNextAdvancesAfterLast asserts that rotation.Next picks the
// model in the global list that follows the model recorded by
// rotation.Record (the same model returned by rotation.Last).
// Without this advance, every launch would pick the same model
// and rotation would be a no-op.
func TestNextAdvancesAfterLast(t *testing.T) {
	cfg := &config.Config{
		Models: []config.Model{
			{ID: "ollama/a", ProviderID: "ollama"},
			{ID: "claude/sonnet", ProviderID: "claude"},
			{ID: "claude/opus", ProviderID: "claude"},
		},
		Agents: []config.Agent{
			{Name: "claude", SupportedProviders: []string{"claude"}},
		},
	}
	dir := t.TempDir()
	r := NewAt(dir)
	_ = r.Record("claude/sonnet")
	got, ok := r.Next(cfg, "claude", "", "")
	if !ok || got.ID != "claude/opus" {
		t.Fatalf("Next = %q, %v; want claude/opus, true", got.ID, ok)
	}
}

// TestNextWrapsAround verifies rotation wraps from the end of the global
// model list back to the first eligible model.
func TestNextWrapsAround(t *testing.T) {
	cfg := &config.Config{
		Models: []config.Model{
			{ID: "claude/sonnet", ProviderID: "claude"},
			{ID: "ollama/a", ProviderID: "ollama"},
			{ID: "claude/opus", ProviderID: "claude"},
		},
		Agents: []config.Agent{
			{Name: "claude", SupportedProviders: []string{"claude"}},
		},
	}
	dir := t.TempDir()
	r := NewAt(dir)
	_ = r.Record("claude/opus")
	got, ok := r.Next(cfg, "claude", "", "")
	if !ok || got.ID != "claude/sonnet" {
		t.Fatalf("Next = %q, %v; want claude/sonnet, true", got.ID, ok)
	}
}

// TestNextSkipsIneligibleModels verifies models not supported by the agent
// are skipped when searching after the last launch.
func TestNextSkipsIneligibleModels(t *testing.T) {
	cfg := &config.Config{
		Models: []config.Model{
			{ID: "ollama/a", ProviderID: "ollama"},
			{ID: "ollama/b", ProviderID: "ollama"},
			{ID: "claude/sonnet", ProviderID: "claude"},
		},
		Agents: []config.Agent{
			{Name: "claude", SupportedProviders: []string{"claude"}},
		},
	}
	dir := t.TempDir()
	r := NewAt(dir)
	_ = r.Record("ollama/b")
	got, ok := r.Next(cfg, "claude", "", "")
	if !ok || got.ID != "claude/sonnet" {
		t.Fatalf("Next = %q, %v; want claude/sonnet, true", got.ID, ok)
	}
}

// TestNextReturnsFalseWhenNoModels returns no model when the agent has no
// supported models.
func TestNextReturnsFalseWhenNoModels(t *testing.T) {
	cfg := &config.Config{
		Models: []config.Model{
			{ID: "ollama/a", ProviderID: "ollama"},
		},
		Agents: []config.Agent{
			{Name: "claude", SupportedProviders: []string{"claude"}},
		},
	}
	r := NewAt(t.TempDir())
	_, ok := r.Next(cfg, "claude", "", "")
	if ok {
		t.Fatal("Next returned ok=true with no eligible models")
	}
}

// TestNextFallsBackToStartWhenLastUnknown starts from the beginning of the
// global list if the saved last model is no longer in the config.
func TestNextFallsBackToStartWhenLastUnknown(t *testing.T) {
	cfg := &config.Config{
		Models: []config.Model{
			{ID: "claude/sonnet", ProviderID: "claude"},
			{ID: "claude/opus", ProviderID: "claude"},
		},
		Agents: []config.Agent{
			{Name: "claude", SupportedProviders: []string{"claude"}},
		},
	}
	dir := t.TempDir()
	r := NewAt(dir)
	_ = r.Record("claude/ghost")
	got, ok := r.Next(cfg, "claude", "", "")
	if !ok || got.ID != "claude/sonnet" {
		t.Fatalf("Next = %q, %v; want claude/sonnet, true", got.ID, ok)
	}
}

// TestMigrationImportsNewestOldFile verifies the first NewAt call migrates
// the most recent per-slot rotation file into rotation.state and removes the
// old files.
func TestMigrationImportsNewestOldFile(t *testing.T) {
	dir := t.TempDir()
	old1 := filepath.Join(dir, "rotation-claude-code-.state")
	old2 := filepath.Join(dir, "rotation-claude-design-.state")
	_ = os.WriteFile(old1, []byte("ollama/a\n"), 0o600)
	_ = os.WriteFile(old2, []byte("ollama/b\n"), 0o600)

	// Ensure old2 is newer.
	future := time.Now().Add(time.Hour)
	_ = os.Chtimes(old2, future, future)

	r := NewAt(dir)
	last, ok := r.Last()
	if !ok || last != "ollama/b" {
		t.Fatalf("Last = %q, %v; want ollama/b, true", last, ok)
	}
	if _, err := os.Stat(old1); !os.IsNotExist(err) {
		t.Fatalf("old1 still exists after migration")
	}
	if _, err := os.Stat(old2); !os.IsNotExist(err) {
		t.Fatalf("old2 still exists after migration")
	}
}

// TestMigrationReadsLegacyTwoLineFile verifies the migration imports the
// model ID from the last non-empty line of legacy files.
func TestMigrationReadsLegacyTwoLineFile(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "rotation-code.state")
	_ = os.WriteFile(legacy, []byte("5\nollama/x:cloud\n"), 0o600)

	r := NewAt(dir)
	last, ok := r.Last()
	if !ok || last != "ollama/x:cloud" {
		t.Fatalf("Last = %q, %v; want ollama/x:cloud, true", last, ok)
	}
}

// TestMigrationDoesNotOverwriteExistingState verifies a second NewAt does
// not re-run migration when rotation.state already exists.
func TestMigrationDoesNotOverwriteExistingState(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "rotation-claude-code-.state")
	_ = os.WriteFile(old, []byte("ollama/old\n"), 0o600)

	r1 := NewAt(dir)
	_ = r1.Record("ollama/new")

	r2 := NewAt(dir)
	last, _ := r2.Last()
	if last != "ollama/new" {
		t.Fatalf("Last = %q, want ollama/new", last)
	}
}

// TestFirstAfterMiddle is retained for callers that still use FirstAfter
// directly with an ordered snapshot.
func TestFirstAfterMiddle(t *testing.T) {
	models := []config.Model{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	got, ok := FirstAfter(models, config.Model{ID: "b"})
	if !ok || got.ID != "c" {
		t.Fatalf("FirstAfter = %q, %v; want c, true", got.ID, ok)
	}
}

// TestFirstAfterWraps verifies FirstAfter wraps from the last model to the
// first.
func TestFirstAfterWraps(t *testing.T) {
	models := []config.Model{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	got, ok := FirstAfter(models, config.Model{ID: "c"})
	if !ok || got.ID != "a" {
		t.Fatalf("FirstAfter = %q, %v; want a, true", got.ID, ok)
	}
}

// TestFirstAfterMissingTarget verifies FirstAfter falls back to the first
// model when target is not in the list.
func TestFirstAfterMissingTarget(t *testing.T) {
	models := []config.Model{{ID: "a"}, {ID: "b"}}
	got, ok := FirstAfter(models, config.Model{ID: "ghost"})
	if !ok || got.ID != "a" {
		t.Fatalf("FirstAfter = %q, %v; want a, true", got.ID, ok)
	}
}

// TestFirstAfterEmpty verifies FirstAfter returns no model on an empty
// snapshot.
func TestFirstAfterEmpty(t *testing.T) {
	if _, ok := FirstAfter(nil, config.Model{ID: "x"}); ok {
		t.Error("FirstAfter on empty snapshot returned ok=true")
	}
}

// TestRotationNextFromEligible verifies the rotation core can operate on
// a precomputed eligible slice without recomputing it from cfg.EligibleModels.
func TestRotationNextFromEligible(t *testing.T) {
	dir := t.TempDir()
	r := NewAt(dir)
	cfg := &config.Config{
		Models: []config.Model{
			{ID: "a"},
			{ID: "b"},
			{ID: "c"},
		},
	}
	if err := r.Record("a"); err != nil {
		t.Fatal(err)
	}

	eligible := []config.Model{{ID: "b"}, {ID: "c"}}
	m, ok := r.NextFromEligible(eligible, cfg)
	if !ok {
		t.Fatal("expected a next model")
	}
	if m.ID != "b" {
		t.Errorf("next = %q, want b (first after a)", m.ID)
	}
}
