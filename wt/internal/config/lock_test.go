package config

import (
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"
)

// TestWithLockSerializesConcurrentCallers pins the mutual-exclusion guarantee
// PatchSave/Save depend on: two overlapping WithLock calls must never run
// their critical sections at the same time, even across goroutines racing
// for the same config.toml. Without this, two writers could interleave their
// read-modify-write steps and still lose an update despite "using the lock".
func TestWithLockSerializesConcurrentCallers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	active := 0
	maxActive := 0
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = WithLock(func() error {
				mu.Lock()
				active++
				if active > maxActive {
					maxActive = active
				}
				mu.Unlock()

				time.Sleep(10 * time.Millisecond)

				mu.Lock()
				active--
				mu.Unlock()
				return nil
			})
		}()
	}
	wg.Wait()

	if maxActive != 1 {
		t.Fatalf("WithLock allowed %d concurrent critical sections, want 1", maxActive)
	}
}

// TestWithLockCleansUpLockFile verifies WithLock does not leak the lock file
// it creates outside of its own directory bookkeeping — it must reuse
// config.toml.lock rather than scattering temp files an operator would have
// to notice and clean up by hand.
func TestWithLockCleansUpLockFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)

	if err := WithLock(func() error { return nil }); err != nil {
		t.Fatal(err)
	}

	want := filepath.Join(Dir(), "config.toml.lock")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("expected lock file at %s: %v", want, err)
	}
}

// TestPatchSavePreservesConcurrentDefaultTagChange reproduces issue #143's
// editor-vs-writer race with the two fields wt's config.toml still holds for
// itself: the `wt config` editor loads a Config, a concurrent save changes
// DefaultTag on disk, and then the editor saves its own Agents edit. With
// whole-file Save the editor's stale snapshot would overwrite the concurrent
// change; PatchSave must instead merge: it writes only the fields apply
// touches and leaves everything else as it is on disk right now.
func TestPatchSavePreservesConcurrentDefaultTagChange(t *testing.T) {
	scratchRegistry(t, "providers = []\nmodels = []\n")
	must(t, Path(), "default_tag = \"code\"\n")

	// The editor "opens" here: it loads a stale snapshot.
	editorCfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}

	// Meanwhile, another process saves its own DefaultTag change (PatchSave
	// re-reads the file fresh itself, so its own snapshot's age is inert).
	if err := (&Config{}).PatchSave(func(fresh *Config) {
		fresh.DefaultTag = "design"
	}); err != nil {
		t.Fatal(err)
	}

	// The editor now saves its (still stale) snapshot, having only ever
	// touched Agents.
	editorCfg.Agents = append(editorCfg.Agents, Agent{Name: "claude", SupportedProviders: nil})
	if err := editorCfg.PatchSave(func(fresh *Config) {
		fresh.Agents = editorCfg.Agents
	}); err != nil {
		t.Fatal(err)
	}

	reloaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.DefaultTag != "design" {
		t.Fatalf("editor save clobbered the concurrent DefaultTag change: %q", reloaded.DefaultTag)
	}
	if !hasAgent(reloaded.Agents, "claude") {
		t.Fatalf("editor's own agent change was lost: %+v", reloaded.Agents)
	}
}

// TestEditorSavePreservesConcurrentLockedApplyChange is the reverse of
// TestLockedApplyPreservesConcurrentEditorChange: an editor save against a
// snapshot taken before a concurrent Load migration write must not revert
// that write's agy-agent fixup when it persists its own change. (The
// editor-vs-`wt litellm set` pair this test used to pin disappears with the
// wt-model config home: `set` no longer touches config.toml at all.)
func TestEditorSavePreservesConcurrentLockedApplyChange(t *testing.T) {
	scratchRegistry(t, "providers = []\nmodels = []\n")
	must(t, Path(), "default_tag = \"code\"\n")

	// The editor opens its snapshot first — readConfigFile, not Load, so the
	// migration below still has something to persist.
	stale, _, err := readConfigFile()
	if err != nil {
		t.Fatal(err)
	}

	// ...then Load's schema-migration write runs (the config.toml fixture has
	// no "agy" agent, so migrateConfigSchema always reports a change).
	if _, changed, err := lockedApply(func(fresh *Config) (bool, error) {
		return migrateConfigSchema(fresh)
	}); err != nil {
		t.Fatal(err)
	} else if !changed {
		t.Fatal("expected the agy-agent fixup to report a change")
	}

	// Now the editor persists its own change from its stale snapshot.
	if err := stale.PatchSave(func(fresh *Config) { fresh.DefaultTag = "design" }); err != nil {
		t.Fatal(err)
	}

	reloaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.DefaultTag != "design" {
		t.Fatal("editor's own change was lost")
	}
	if !hasAgent(reloaded.Agents, "agy") {
		t.Fatalf("concurrent migration fixup was clobbered: %+v", reloaded.Agents)
	}
}

// TestLockedApplyPreservesConcurrentEditorChange reproduces the same class
// of race PatchSave closes (issue #143), but for Load's self-persisting
// schema-migration write: lockedApply must re-read config.toml fresh at
// write time rather than trust whatever a caller decided from an earlier,
// now-stale read. Without this, Load's migration write (Save(cfg) on the
// original unlocked read) would silently clobber a concurrent editor save
// that lands in the window between Load's initial read and its eventual
// save — reopening the exact lost-update race PatchSave closes, just for
// this third writer.
func TestLockedApplyPreservesConcurrentEditorChange(t *testing.T) {
	scratchRegistry(t, "providers = []\nmodels = []\n")
	must(t, Path(), "default_tag = \"code\"\n")

	// An editor save runs to completion (a locked read-modify-write) before
	// lockedApply below ever takes the lock. PatchSave only needs a *Config
	// to call the method on — it re-reads config.toml fresh itself — so this
	// deliberately does not go through Load(), which would perform (and
	// persist) the schema migration itself and leave nothing left for
	// lockedApply below to change.
	if err := (&Config{}).PatchSave(func(fresh *Config) { fresh.DefaultTag = "design" }); err != nil {
		t.Fatal(err)
	}

	// Load's schema-migration write now runs. The config.toml fixture has no
	// "agy" agent, so migrateConfigSchema always reports a change here,
	// independent of the concurrent editor write above.
	fresh, changed, err := lockedApply(func(fresh *Config) (bool, error) {
		return migrateConfigSchema(fresh)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected the agy-agent fixup to report a change")
	}
	if fresh.DefaultTag != "design" {
		t.Fatalf("lockedApply clobbered the concurrent editor change: %q", fresh.DefaultTag)
	}

	reloaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.DefaultTag != "design" {
		t.Fatalf("concurrent editor change lost on disk: %q", reloaded.DefaultTag)
	}
	if !hasAgent(reloaded.Agents, "agy") {
		t.Fatalf("schema migration's own agy fixup was lost: %+v", reloaded.Agents)
	}
}

func hasAgent(agents []Agent, name string) bool {
	return slices.ContainsFunc(agents, func(a Agent) bool { return a.Name == name })
}

func must(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
