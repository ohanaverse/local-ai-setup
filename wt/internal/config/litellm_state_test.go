package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func litellmStateEnv(t *testing.T, wtToml string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("WT_REGISTRY", "")
	t.Setenv("MODELMAN_REGISTRY", "")
	writeUnder(t, home, "local-ai/registry.toml", "providers = []\nmodels = []\n")
	if wtToml != "" {
		writeUnder(t, home, "agent-wt/config.toml", wtToml)
	}
	return home
}

// writeUnder writes body to home/rel, creating the directories.
func writeUnder(t *testing.T, home, rel, body string) {
	t.Helper()
	p := filepath.Join(home, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestLoadNeverReadsModelmanToml pins the end of the legacy fallback: wt used
// to take LiteLLM routing state from modelman.toml's [litellm] table when its
// own config.toml had none, and to copy it in. modelman is gone, and the file
// is one the user is told to delete, so it must change nothing: routing stays
// off, config.toml is not created, the key in the old file is not copied
// into it, and a file that is not even TOML no longer stops every wt command
// with "parse modelman.toml". The old file is present in every case, at the
// path wt read it from ($XDG_CONFIG_HOME/local-ai) and at the pre-XDG default
// (~/.config/local-ai), so the test proves it is ignored, not that it is
// absent.
func TestLoadNeverReadsModelmanToml(t *testing.T) {
	const populated = "[litellm]\nenabled = true\nurl = \"http://localhost:4000\"\napi_key = \"sk-legacy\"\n"
	const ownConfig = "default_tag = \"code\"\n"
	for _, tc := range []struct {
		name, wtToml, modelmanToml string
	}{
		{"config.toml without [litellm]", ownConfig, populated},
		{"no config.toml", "", populated},
		{"modelman.toml is not TOML", ownConfig, "this is not toml {{{"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := litellmStateEnv(t, tc.wtToml)
			writeUnder(t, home, "local-ai/modelman.toml", tc.modelmanToml)
			writeUnder(t, home, ".config/local-ai/modelman.toml", tc.modelmanToml)
			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load = %v, want modelman.toml ignored", err)
			}
			if cfg.IsLitellm() || cfg.LitellmBaseURL() != "" || cfg.LitellmAPIKey() != "" || cfg.LitellmTable != nil {
				t.Fatalf("routing state = %+v (table %+v), want none: modelman.toml was read", cfg.litellm, cfg.LitellmTable)
			}
			got, err := os.ReadFile(filepath.Join(home, "agent-wt", "config.toml"))
			switch {
			case tc.wtToml == "" && !os.IsNotExist(err):
				t.Fatalf("config.toml was created by Load (err=%v):\n%s", err, got)
			case tc.wtToml != "" && (strings.Contains(string(got), "litellm") || strings.Contains(string(got), "sk-legacy")):
				t.Fatalf("modelman.toml's [litellm] was copied into config.toml:\n%s", got)
			}
		})
	}
}

// TestUpdateLitellmPersists pins the setter used by `wt litellm on/off/set`:
// it updates memory and config.toml, and writes the file 0600 when it holds an
// api_key (the LiteLLM master key must not be world-readable).
func TestUpdateLitellmPersists(t *testing.T) {
	home := litellmStateEnv(t, "default_tag = \"code\"\n")
	cfg, _ := Load()
	if err := cfg.UpdateLitellm(func(s *LitellmState) { s.Enabled, s.URL, s.APIKey = true, "http://x:4000/", "sk-new" }); err != nil {
		t.Fatal(err)
	}
	if !cfg.IsLitellm() || cfg.LitellmBaseURL() != "http://x:4000" {
		t.Fatalf("in-memory not updated: %+v", cfg.litellm)
	}
	p := filepath.Join(home, "agent-wt", "config.toml")
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("config.toml mode = %v, want 0600 when it holds an api_key", st.Mode().Perm())
	}
	cfg2, _ := Load()
	if cfg2.LitellmAPIKey() != "sk-new" {
		t.Fatal("state did not round-trip through config.toml")
	}
}

// TestLitellmOwnEmptyTableIsATableNotAbsence pins that a config.toml with its
// own zero-valued [litellm] (the user turned routing off in wt), or a bare
// [litellm] header, decodes to a non-nil table with routing off. `wt litellm
// status` and UpdateLitellm tell "off" from "never set" by that pointer.
func TestLitellmOwnEmptyTableIsATableNotAbsence(t *testing.T) {
	for _, body := range []string{
		"default_tag = \"code\"\n[litellm]\nenabled = false\nurl = \"\"\napi_key = \"\"\n",
		"default_tag = \"code\"\n[litellm]\n",
	} {
		litellmStateEnv(t, body)
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.LitellmTable == nil {
			t.Fatalf("[litellm] in %q must decode to a non-nil pointer", body)
		}
		if cfg.IsLitellm() || cfg.LitellmAPIKey() != "" || cfg.LitellmBaseURL() != "" {
			t.Fatalf("empty [litellm] turned routing on: %+v", cfg.litellm)
		}
	}
}

// TestWriteFileAtomicChmodsStaleTmp pins that a stale 0644 <path>.tmp left by
// a crash cannot make a 0600 write land world-readable: the tmp is chmod'ed
// before rename (now: the stale file is simply ignored because temp names are
// unique). Matters because config.toml can hold the LiteLLM api_key.
func TestWriteFileAtomicChmodsStaleTmp(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p+".tmp", []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p+".tmp", 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(p, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", st.Mode().Perm())
	}
	if b, _ := os.ReadFile(p); string(b) != "secret" {
		t.Fatalf("content = %q, want the new payload (stale tmp must not matter)", b)
	}
}

// TestWriteFileAtomicConcurrent pins that concurrent writers to one path never
// expose a torn or empty file: every read sees exactly one complete payload,
// no *.tmp files linger, and the final file equals one payload. With a shared
// fixed temp name, O_TRUNC by one writer tore what another was about to
// rename into place, and a reader of the empty config.toml made wt rewrite the
// user's config with defaults.
func TestWriteFileAtomicConcurrent(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	const writers, iters = 8, 60
	payloads := make([][]byte, writers)
	for i := range payloads {
		payloads[i] = bytes.Repeat([]byte(fmt.Sprintf("writer-%d;", i)), 2500) // ~20 KB
	}
	valid := func(b []byte) bool {
		for _, pl := range payloads {
			if bytes.Equal(b, pl) {
				return true
			}
		}
		return false
	}
	if err := WriteFileAtomic(p, payloads[0], 0o600); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var readerDone sync.WaitGroup
	var mu sync.Mutex
	var torn []int
	readerDone.Add(1)
	go func() {
		defer readerDone.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if b, err := os.ReadFile(p); err == nil && !valid(b) {
				mu.Lock()
				torn = append(torn, len(b))
				mu.Unlock()
			}
		}
	}()
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				if err := WriteFileAtomic(p, payloads[i], 0o600); err != nil {
					t.Errorf("writer %d: %v", i, err)
					return
				}
			}
		}(i)
	}
	wg.Wait()
	close(stop)
	readerDone.Wait()
	if len(torn) > 0 {
		t.Fatalf("observed %d torn/empty reads (first sizes %v)", len(torn), torn[:min(5, len(torn))])
	}
	if b, _ := os.ReadFile(p); !valid(b) {
		t.Fatalf("final file is not exactly one payload (len %d)", len(b))
	}
	if m, _ := filepath.Glob(filepath.Join(dir, "*.tmp")); len(m) != 0 {
		t.Fatalf("leftover temp files: %v", m)
	}
}

// TestUpdateLitellmRollsBackOnSaveFailure pins that a failed persist leaves the
// in-memory state untouched. Otherwise `wt litellm on` reports an error while
// this process keeps routing through LiteLLM on a state that was never saved.
func TestUpdateLitellmRollsBackOnSaveFailure(t *testing.T) {
	home := litellmStateEnv(t, "default_tag = \"code\"\n")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, "agent-wt")
	if err := os.Chmod(dir, 0o500); err != nil { // read-only: atomic write cannot create its temp file
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	if err := cfg.UpdateLitellm(func(s *LitellmState) { s.Enabled = true }); err == nil {
		t.Skip("directory permissions did not block the write (running as root?)")
	}
	if cfg.IsLitellm() || cfg.LitellmTable != nil {
		t.Fatalf("state changed despite the failed save: enabled=%v table=%v", cfg.IsLitellm(), cfg.LitellmTable)
	}
}
