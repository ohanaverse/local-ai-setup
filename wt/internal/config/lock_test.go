package config

import (
	"os"
	"path/filepath"
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

// TestPatchSavePreservesConcurrentLitellmChange reproduces issue #143's
// editor-vs-litellm race: the `wt config` editor loads a Config, a
// concurrent `wt litellm set` changes [litellm] on disk, and then the
// editor saves. With whole-file Save the editor's stale in-memory
// [litellm] (empty, from before the concurrent change) would overwrite it.
// PatchSave must instead merge: it writes only the fields apply touches
// and leaves everything else as it is on disk right now.
func TestPatchSavePreservesConcurrentLitellmChange(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("MODELMAN_REGISTRY", "")
	must(t, filepath.Join(home, "local-ai", "registry.toml"), "providers = []\nmodels = []\n")
	must(t, filepath.Join(home, "agent-wt", "config.toml"), "default_tag = \"code\"\n")

	// The editor "opens" here: it loads a stale snapshot with no [litellm].
	editorCfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}

	// Meanwhile, another process runs `wt litellm set` against a fresh load.
	writerCfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := writerCfg.UpdateLitellm(func(s *LitellmState) {
		s.Enabled, s.URL, s.APIKey = true, "http://localhost:4000", "sk-concurrent"
	}); err != nil {
		t.Fatal(err)
	}

	// The editor now saves its (still stale, no-litellm) snapshot, having
	// only ever touched Agents/DefaultTag.
	editorCfg.Agents = append(editorCfg.Agents, Agent{Name: "claude", SupportedProviders: nil})
	if err := editorCfg.PatchSave(func(fresh *Config) {
		fresh.Agents = editorCfg.Agents
		fresh.DefaultTag = editorCfg.DefaultTag
	}); err != nil {
		t.Fatal(err)
	}

	reloaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.IsLitellm() || reloaded.LitellmAPIKey() != "sk-concurrent" {
		t.Fatalf("editor save clobbered concurrent litellm change: %+v", reloaded.litellm)
	}
	if !hasAgent(reloaded.Agents, "claude") {
		t.Fatalf("editor's own agent change was lost: %+v", reloaded.Agents)
	}
}

// TestUpdateLitellmPreservesConcurrentAgentChange is the reverse of
// TestPatchSavePreservesConcurrentLitellmChange: a `wt litellm on` run
// against a config loaded before a concurrent editor session added an
// agent must not revert that agent when it persists its own change.
func TestUpdateLitellmPreservesConcurrentAgentChange(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("MODELMAN_REGISTRY", "")
	must(t, filepath.Join(home, "local-ai", "registry.toml"), "providers = []\nmodels = []\n")
	must(t, filepath.Join(home, "agent-wt", "config.toml"), "default_tag = \"code\"\n")

	// The `wt litellm on` process loads its config first...
	litellmCfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}

	// ...then the editor loads its own copy and saves a new agent.
	editorCfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	editorCfg.Agents = append(editorCfg.Agents, Agent{Name: "claude", SupportedProviders: nil})
	if err := editorCfg.PatchSave(func(fresh *Config) {
		fresh.Agents = editorCfg.Agents
		fresh.DefaultTag = editorCfg.DefaultTag
	}); err != nil {
		t.Fatal(err)
	}

	// Now the litellm process persists its change from its (agent-less)
	// stale snapshot.
	if err := litellmCfg.UpdateLitellm(func(s *LitellmState) { s.Enabled = true }); err != nil {
		t.Fatal(err)
	}

	reloaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.IsLitellm() {
		t.Fatal("litellm process's own change was lost")
	}
	if !hasAgent(reloaded.Agents, "claude") {
		t.Fatalf("concurrent editor's agent was clobbered: %+v", reloaded.Agents)
	}
}

func hasAgent(agents []Agent, name string) bool {
	for _, a := range agents {
		if a.Name == name {
			return true
		}
	}
	return false
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
