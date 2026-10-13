package rotation

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/usage"
)

// TestLastMissingFile returns no last launch when rotation.state is absent.
func TestLastMissingFile(t *testing.T) {
	r := NewAt(t.TempDir())
	if _, ok := r.Last(); ok {
		t.Fatal("Last on missing file returned ok=true")
	}
}

// TestRecordAndLastRoundTrip verifies RecordFor writes the model ID and Last
// reads it back.
func TestRecordAndLastRoundTrip(t *testing.T) {
	dir := t.TempDir()
	r := NewAt(dir)
	if err := r.RecordFor("", "alpha"); err != nil {
		t.Fatalf("RecordFor: %v", err)
	}
	got, ok := r.Last()
	if !ok {
		t.Fatal("Last returned !ok after RecordFor")
	}
	if got != "alpha" {
		t.Errorf("Last = %q, want alpha", got)
	}
}

// TestRecordOverwrites verifies subsequent launches update the saved model ID.
func TestRecordOverwrites(t *testing.T) {
	dir := t.TempDir()
	r := NewAt(dir)
	_ = r.RecordFor("", "alpha")
	_ = r.RecordFor("", "beta")
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
	if err := r.RecordFor("", "alpha"); err != nil {
		t.Fatalf("RecordFor: %v", err)
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

// eligibleFor returns the models cfg lists for the claude agent with no tag
// or family filter: the eligible slice the TestNext* cases rotate over.
func eligibleFor(t *testing.T, cfg *config.Config) []config.Model {
	t.Helper()
	eligible, err := cfg.EligibleModels("claude", "", "")
	if err != nil {
		t.Fatalf("EligibleModels: %v", err)
	}
	return eligible
}

// claudeCfg returns the config the TestNext* cases share: a local ollama
// provider, a cloud claude provider, one claude agent that supports only the
// claude provider, and the given models in the order rotation walks them.
func claudeCfg(models ...config.Model) *config.Config {
	return &config.Config{
		Providers: []config.Provider{
			{ID: "ollama", Location: config.LocationLocal},
			{ID: "claude", Location: config.LocationCloud},
		},
		Models: models,
		Agents: []config.Agent{
			{Name: "claude", SupportedProviders: []string{"claude"}},
		},
	}
}

// TestNextReturnsFirstEligibleWhenEmpty returns the first agent-eligible
// model when there is no prior launch.
func TestNextReturnsFirstEligibleWhenEmpty(t *testing.T) {
	cfg := claudeCfg(
		config.Model{ID: "ollama/a", ProviderID: "ollama"},
		config.Model{ID: "claude/sonnet", ProviderID: "claude"},
	)
	r := NewAt(t.TempDir())
	got, ok := r.NextFromEligible(eligibleFor(t, cfg), cfg)
	if !ok || got.ID != "claude/sonnet" {
		t.Fatalf("NextFromEligible = %q, %v; want claude/sonnet, true", got.ID, ok)
	}
}

// TestNextAdvancesAfterLast asserts that rotation.NextFromEligible picks
// the model in the global list that follows the model recorded by
// rotation.RecordFor (the same model returned by rotation.Last).
// Without this advance, every launch would pick the same model
// and rotation would be a no-op.
func TestNextAdvancesAfterLast(t *testing.T) {
	cfg := claudeCfg(
		config.Model{ID: "ollama/a", ProviderID: "ollama"},
		config.Model{ID: "claude/sonnet", ProviderID: "claude"},
		config.Model{ID: "claude/opus", ProviderID: "claude"},
	)
	dir := t.TempDir()
	r := NewAt(dir)
	_ = r.RecordFor("", "claude/sonnet")
	got, ok := r.NextFromEligible(eligibleFor(t, cfg), cfg)
	if !ok || got.ID != "claude/opus" {
		t.Fatalf("NextFromEligible = %q, %v; want claude/opus, true", got.ID, ok)
	}
}

// TestNextWrapsAround verifies rotation wraps from the end of the global
// model list back to the first eligible model.
func TestNextWrapsAround(t *testing.T) {
	cfg := claudeCfg(
		config.Model{ID: "claude/sonnet", ProviderID: "claude"},
		config.Model{ID: "ollama/a", ProviderID: "ollama"},
		config.Model{ID: "claude/opus", ProviderID: "claude"},
	)
	dir := t.TempDir()
	r := NewAt(dir)
	_ = r.RecordFor("", "claude/opus")
	got, ok := r.NextFromEligible(eligibleFor(t, cfg), cfg)
	if !ok || got.ID != "claude/sonnet" {
		t.Fatalf("NextFromEligible = %q, %v; want claude/sonnet, true", got.ID, ok)
	}
}

// TestNextSkipsIneligibleModels verifies models not supported by the agent
// are skipped when searching after the last launch.
func TestNextSkipsIneligibleModels(t *testing.T) {
	cfg := claudeCfg(
		config.Model{ID: "ollama/a", ProviderID: "ollama"},
		config.Model{ID: "ollama/b", ProviderID: "ollama"},
		config.Model{ID: "claude/sonnet", ProviderID: "claude"},
	)
	dir := t.TempDir()
	r := NewAt(dir)
	_ = r.RecordFor("", "ollama/b")
	got, ok := r.NextFromEligible(eligibleFor(t, cfg), cfg)
	if !ok || got.ID != "claude/sonnet" {
		t.Fatalf("NextFromEligible = %q, %v; want claude/sonnet, true", got.ID, ok)
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
	eligible := eligibleFor(t, cfg)
	if len(eligible) != 0 {
		t.Fatalf("eligible = %v, want none", eligible)
	}
	if _, ok := r.NextFromEligible(eligible, cfg); ok {
		t.Fatal("NextFromEligible returned ok=true with no eligible models")
	}
}

// TestNextFallsBackToStartWhenLastUnknown starts from the beginning of the
// global list if the saved last model is no longer in the config.
func TestNextFallsBackToStartWhenLastUnknown(t *testing.T) {
	cfg := claudeCfg(
		config.Model{ID: "claude/sonnet", ProviderID: "claude"},
		config.Model{ID: "claude/opus", ProviderID: "claude"},
	)
	dir := t.TempDir()
	r := NewAt(dir)
	_ = r.RecordFor("", "claude/ghost")
	got, ok := r.NextFromEligible(eligibleFor(t, cfg), cfg)
	if !ok || got.ID != "claude/sonnet" {
		t.Fatalf("NextFromEligible = %q, %v; want claude/sonnet, true", got.ID, ok)
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
	_ = r1.RecordFor("", "ollama/new")

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
	if err := r.RecordFor("", "a"); err != nil {
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

// TestRecordForAttributesUsageToAgent verifies RecordFor writes the
// rotation state and also records a usage event tagged with the agent, so
// launch history is attributable to an agent-model pair.
func TestRecordForAttributesUsageToAgent(t *testing.T) {
	dir := t.TempDir()
	r := NewAt(dir)
	if err := r.RecordFor("claude", "omlx/Qwen3.8-27B-4bit"); err != nil {
		t.Fatalf("RecordFor: %v", err)
	}
	if last, _ := r.Last(); last != "omlx/Qwen3.8-27B-4bit" {
		t.Errorf("Last = %q", last)
	}
	got := usage.NewStoreAt(dir).CountsForAgent("claude", []string{"omlx/Qwen3.8-27B-4bit"})
	if got["omlx/Qwen3.8-27B-4bit"].ThirtyDay != 1 {
		t.Errorf("claude pair 30d = %d, want 1", got["omlx/Qwen3.8-27B-4bit"].ThirtyDay)
	}
}

// TestNextFromEligibleNeverPicksDiscovered pins #179 Phase B's rotation rule:
// rotation walks the registry's model order, so a discovered (unregistered)
// model is never auto-selected even when it is launchable — the user can
// still pick one in the picker or pin it with -M. A discovered model has no
// overlay family or tags, so rotating onto it would launch a model the user
// never configured.
func TestNextFromEligibleNeverPicksDiscovered(t *testing.T) {
	cfg := &config.Config{Models: []config.Model{
		{ID: "omlx/registered", ProviderID: "omlx", ModelName: "registered"},
	}}
	disc := config.Model{ID: config.DiscoveredModelID("omlx", "stray"), ProviderID: "omlx", ModelName: "stray", Source: config.SourceDiscovered}
	r := NewAt(t.TempDir())
	m, ok := r.NextFromEligible([]config.Model{disc, cfg.Models[0]}, cfg)
	if !ok || m.ID != "omlx/registered" {
		t.Fatalf("NextFromEligible = (%q, %v), want the registered model", m.ID, ok)
	}
	if m, ok := r.NextFromEligible([]config.Model{disc}, cfg); ok {
		t.Fatalf("NextFromEligible(discovered only) = %q, want no pick", m.ID)
	}
}

// TestLastPicked verifies which id the picker's marker reads: the one in
// last-picked.state, and the one in rotation.state when that file is missing
// or empty. Without the fallback, a user upgrading wt would lose the marker
// until their next launch; without the preference, a slot-profile launch
// could never move the marker, since rotation.state never names a profile.
func TestLastPicked(t *testing.T) {
	missing := "\x00" // a file that is not written
	tests := []struct {
		name             string
		rotation, picked string
		want             string
		wantOK           bool
	}{
		{name: "no state at all", rotation: missing, picked: missing, want: "", wantOK: false},
		{name: "state from before last-picked.state existed", rotation: "ollama/a\n", picked: missing, want: "ollama/a", wantOK: true},
		{name: "a profile was launched after a model", rotation: "ollama/a\n", picked: "profile/mixed\n", want: "profile/mixed", wantOK: true},
		{name: "an empty last-picked.state falls back", rotation: "ollama/a\n", picked: " \n", want: "ollama/a", wantOK: true},
		{name: "a profile is the only launch so far", rotation: missing, picked: "profile/mixed\n", want: "profile/mixed", wantOK: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, content := range map[string]string{"rotation.state": tt.rotation, "last-picked.state": tt.picked} {
				if content == missing {
					continue
				}
				if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
					t.Fatalf("seed %s: %v", name, err)
				}
			}
			got, ok := NewAt(dir).LastPicked()
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("LastPicked = %q, %v; want %q, %v", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

// TestRecordForWritesLastPicked verifies a single-model launch writes both
// state files, each one line and mode 0600. If it wrote rotation.state alone,
// the picker's marker would stay on a slot profile launched earlier, pointing
// at a row the user did not launch last.
func TestRecordForWritesLastPicked(t *testing.T) {
	dir := t.TempDir()
	r := NewAt(dir)
	if err := r.RecordProfile("claude", "profile/mixed"); err != nil {
		t.Fatalf("RecordProfile: %v", err)
	}
	if err := r.RecordFor("claude", "ollama/a"); err != nil {
		t.Fatalf("RecordFor: %v", err)
	}
	for _, name := range []string{"rotation.state", "last-picked.state"} {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if string(data) != "ollama/a\n" {
			t.Errorf("%s = %q, want %q", name, data, "ollama/a\n")
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("%s perm = %o, want 0600", name, perm)
		}
	}
	if got, ok := r.LastPicked(); !ok || got != "ollama/a" {
		t.Errorf("LastPicked = %q, %v; want ollama/a, true", got, ok)
	}
}

// TestRecordProfileLeavesRotationStateAlone verifies a slot-profile launch
// moves the marker and counts as a launch of the profile, and leaves
// rotation.state byte for byte as it was. rotation.state is one bare registry
// id that an older wt binary reads whole; a profile id in it would make that
// binary, and rotation itself, treat the state as stale and restart rotation
// from the first model.
func TestRecordProfileLeavesRotationStateAlone(t *testing.T) {
	cfg := claudeCfg(
		config.Model{ID: "claude/sonnet", ProviderID: "claude"},
		config.Model{ID: "claude/opus", ProviderID: "claude"},
	)
	dir := t.TempDir()
	r := NewAt(dir)
	if err := r.RecordFor("claude", "claude/sonnet"); err != nil {
		t.Fatalf("RecordFor: %v", err)
	}
	before, err := os.ReadFile(r.statePath())
	if err != nil {
		t.Fatalf("read rotation.state: %v", err)
	}

	if err := r.RecordProfile("claude", "profile/mixed"); err != nil {
		t.Fatalf("RecordProfile: %v", err)
	}

	after, err := os.ReadFile(r.statePath())
	if err != nil {
		t.Fatalf("read rotation.state after the profile launch: %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("rotation.state = %q after a profile launch, want it unchanged (%q)", after, before)
	}
	if got, _ := r.Last(); got != "claude/sonnet" {
		t.Errorf("Last = %q, want claude/sonnet", got)
	}
	if got, ok := r.NextFromEligible(eligibleFor(t, cfg), cfg); !ok || got.ID != "claude/opus" {
		t.Errorf("NextFromEligible = %q, %v; want claude/opus, true (rotation did not move)", got.ID, ok)
	}
	if got, ok := r.LastPicked(); !ok || got != "profile/mixed" {
		t.Errorf("LastPicked = %q, %v; want profile/mixed, true", got, ok)
	}
	picked, err := os.ReadFile(r.pickedPath())
	if err != nil {
		t.Fatalf("read last-picked.state: %v", err)
	}
	if string(picked) != "profile/mixed\n" {
		t.Errorf("last-picked.state = %q, want %q", picked, "profile/mixed\n")
	}
	info, err := os.Stat(r.pickedPath())
	if err != nil {
		t.Fatalf("stat last-picked.state: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("last-picked.state perm = %o, want 0600", perm)
	}
	store := usage.NewStoreAt(dir)
	if got := store.CountsForAgent("claude", []string{"profile/mixed"})["profile/mixed"].ThirtyDay; got != 1 {
		t.Errorf("claude launches of profile/mixed = %d, want 1", got)
	}
	if got := store.CountsForAgent("claude", []string{"claude/sonnet"})["claude/sonnet"].ThirtyDay; got != 1 {
		t.Errorf("claude launches of claude/sonnet = %d, want 1 (the profile launch is not a launch of a model)", got)
	}
}

// TestRecordProfileDoesNotCreateRotationState verifies a slot-profile launch
// on a fresh install writes no rotation.state. A file created there would
// have to hold something, and anything but a registry id breaks the next
// single-model rotation.
func TestRecordProfileDoesNotCreateRotationState(t *testing.T) {
	dir := t.TempDir()
	r := NewAt(dir)
	if err := r.RecordProfile("claude", "profile/mixed"); err != nil {
		t.Fatalf("RecordProfile: %v", err)
	}
	if _, err := os.Stat(r.statePath()); !os.IsNotExist(err) {
		t.Errorf("rotation.state exists after a profile launch (stat err = %v), want it absent", err)
	}
	if got, ok := r.Last(); ok {
		t.Errorf("Last = %q, true; want no last-launched model", got)
	}
	if got, ok := r.LastPicked(); !ok || got != "profile/mixed" {
		t.Errorf("LastPicked = %q, %v; want profile/mixed, true", got, ok)
	}
}

// TestRecordKeepsUsageWhenLastPickedFails verifies a last-picked.state that
// cannot be written is reported and costs nothing else: rotation still
// advances and the launch is still counted. Both launch paths print the error
// as a note and launch anyway, so a launch dropped from usage.jsonl here
// would be missing from the picker's counts and from `wt stats`.
func TestRecordKeepsUsageWhenLastPickedFails(t *testing.T) {
	dir := t.TempDir()
	r := NewAt(dir)
	// A directory where the file belongs: the atomic rename onto it fails.
	if err := os.Mkdir(r.pickedPath(), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if err := r.RecordFor("claude", "ollama/a"); err == nil {
		t.Error("RecordFor = nil, want the last-picked.state write error")
	}
	if got, _ := r.Last(); got != "ollama/a" {
		t.Errorf("Last = %q, want ollama/a (rotation.state is written first)", got)
	}
	if err := r.RecordProfile("claude", "profile/mixed"); err == nil {
		t.Error("RecordProfile = nil, want the last-picked.state write error")
	}
	counts := usage.NewStoreAt(dir).CountsForAgent("claude", []string{"ollama/a", "profile/mixed"})
	for _, id := range []string{"ollama/a", "profile/mixed"} {
		if counts[id].ThirtyDay != 1 {
			t.Errorf("launches of %s = %d, want 1", id, counts[id].ThirtyDay)
		}
	}
}
