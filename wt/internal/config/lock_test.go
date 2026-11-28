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
	editorCfg.Agents = append(editorCfg.Agents, Agent{Name: "claude"})
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
// snapshot taken before a concurrent lockedApply write (Load's self-persisting
// migration path) must not revert that write when it persists its own change.
// The concurrent write adds an agent, a change the editor's DefaultTag edit
// does not own, so a save of the stale snapshot as a whole would lose it. (The
// editor-vs-`wt litellm set` pair this test used to pin disappears with the
// wt-model config home: `set` no longer touches config.toml at all.)
func TestEditorSavePreservesConcurrentLockedApplyChange(t *testing.T) {
	scratchRegistry(t, "providers = []\nmodels = []\n")
	must(t, Path(), "default_tag = \"code\"\n\n[[agents]]\nname = \"claude\"\n")

	// The editor opens its snapshot first (readConfigFile, not Load).
	stale, _, err := readConfigFile()
	if err != nil {
		t.Fatal(err)
	}

	// ...then a locked read-modify-write lands a change of its own, as Load's
	// migration write does: a second agent.
	if _, changed, err := lockedApply(func(fresh *Config) (bool, error) {
		fresh.Agents = append(fresh.Agents, Agent{Name: "codex"})
		return true, nil
	}); err != nil {
		t.Fatal(err)
	} else if !changed {
		t.Fatal("lockedApply reported no change for an apply that made one")
	}

	// Now the editor persists its own change from its stale snapshot.
	if err := stale.PatchSave(func(fresh *Config) { fresh.DefaultTag = "design" }); err != nil {
		t.Fatal(err)
	}

	// Read the file raw, before any Load: both writers' changes must be there.
	onDisk, _, err := readConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	if !hasAgent(onDisk.Agents, "codex") {
		t.Fatalf("concurrent lockedApply change was lost: agents = %+v", onDisk.Agents)
	}
	if onDisk.DefaultTag != "design" {
		t.Fatalf("editor's own change was lost: default_tag = %q", onDisk.DefaultTag)
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
	must(t, Path(), "default_tag = \"code\"\n\n[[agents]]\nname = \"claude\"\n")

	// An editor save runs to completion (a locked read-modify-write) before
	// lockedApply below ever takes the lock. PatchSave only needs a *Config
	// to call the method on: it re-reads config.toml fresh itself.
	if err := (&Config{}).PatchSave(func(fresh *Config) { fresh.DefaultTag = "design" }); err != nil {
		t.Fatal(err)
	}

	// Load's schema-migration write now runs. Its apply is a stand-in that
	// makes one change of its own (an agent) and reports it, as a fixup
	// would; it must be handed the file as it is now, not an earlier read.
	fresh, changed, err := lockedApply(func(fresh *Config) (bool, error) {
		if fresh.DefaultTag != "design" {
			t.Errorf("apply saw default_tag %q, want the concurrent editor's %q", fresh.DefaultTag, "design")
		}
		fresh.Agents = append(fresh.Agents, Agent{Name: "codex"})
		return true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("lockedApply reported no change for an apply that made one")
	}
	if fresh.DefaultTag != "design" {
		t.Fatalf("lockedApply clobbered the concurrent editor change: %q", fresh.DefaultTag)
	}

	// Read the file raw (see the sibling test): both writers' changes must be
	// on disk.
	onDisk, _, err := readConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	if onDisk.DefaultTag != "design" || !hasAgent(onDisk.Agents, "codex") || !hasAgent(onDisk.Agents, "claude") {
		t.Fatalf("a writer's change was lost on disk: default_tag %q, agents %+v", onDisk.DefaultTag, onDisk.Agents)
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

// TestWithFileLockSharedLetsReadersRunTogether pins #628: two shared
// readers hold the lock at once while an exclusive writer waits — the
// proxy-pair reads stop queueing behind one another.
func TestWithFileLockSharedLetsReadersRunTogether(t *testing.T) {
	lock := filepath.Join(t.TempDir(), "pair.lock")

	release := make(chan struct{})
	first := make(chan error, 1)
	go func() { first <- WithFileLockShared(lock, func() error { <-release; return nil }) }()
	// Give the first reader time to take the lock.
	time.Sleep(100 * time.Millisecond)
	second := make(chan error, 1)
	go func() { second <- WithFileLockShared(lock, func() error { return nil }) }()
	select {
	case err := <-second:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a shared reader queued behind another shared reader")
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}

	// The writer excludes: while a shared lock is held, the exclusive one waits.
	held := make(chan struct{})
	release2 := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- WithFileLockShared(lock, func() error { close(held); <-release2; return nil }) }()
	<-held
	writerDone := make(chan error, 1)
	go func() { writerDone <- WithFileLock(lock, func() error { return nil }) }()
	select {
	case err := <-writerDone:
		t.Fatalf("the writer ran while a shared lock was held: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	close(release2)
	select {
	case err := <-writerDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the writer never ran after the shared lock was released")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
