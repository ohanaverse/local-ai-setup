package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func litellmStateEnv(t *testing.T, wtToml, modelmanToml string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("MODELMAN_REGISTRY", "")
	must := func(rel, body string) {
		p := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("local-ai/registry.toml", "providers = []\nmodels = []\n")
	if wtToml != "" {
		must("agent-wt/config.toml", wtToml)
	}
	if modelmanToml != "" {
		must("local-ai/modelman.toml", modelmanToml)
	}
	return home
}

const legacyLitellm = "[litellm]\nenabled = true\nurl = \"http://localhost:4000\"\napi_key = \"sk-legacy\"\n"

// TestLitellmStateMigratesFromModelmanOnce pins the one-time migration: when
// wt's config.toml has no [litellm] but modelman.toml does, Load copies it
// into config.toml (so wt owns it from then on), and a second Load reads wt's
// copy. Without it, users would silently lose their proxy URL/key on upgrade.
func TestLitellmStateMigratesFromModelmanOnce(t *testing.T) {
	home := litellmStateEnv(t, "default_tag = \"code\"\n", legacyLitellm)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.IsLitellm() || cfg.LitellmBaseURL() != "http://localhost:4000" || cfg.LitellmAPIKey() != "sk-legacy" {
		t.Fatalf("legacy state not read: %+v", cfg.litellm)
	}
	b, _ := os.ReadFile(filepath.Join(home, "agent-wt", "config.toml"))
	if !strings.Contains(string(b), "[litellm]") || !strings.Contains(string(b), "sk-legacy") {
		t.Fatalf("state not persisted to config.toml:\n%s", b)
	}
	// Owner is now wt: changing modelman's copy must have no effect.
	os.WriteFile(filepath.Join(home, "local-ai", "modelman.toml"), []byte("[litellm]\nenabled = false\n"), 0o644)
	cfg2, _ := Load()
	if !cfg2.IsLitellm() {
		t.Fatal("wt's own [litellm] must win over modelman.toml after migration")
	}
}

// TestLitellmStateNoConfigTomlDoesNotCreateIt pins the safety guard: when
// config.toml does not exist yet, Load must not create it just to persist a
// migration (that would pre-empt agent seeding); it falls back to modelman's
// values in memory.
func TestLitellmStateNoConfigTomlDoesNotCreateIt(t *testing.T) {
	home := litellmStateEnv(t, "", legacyLitellm)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.IsLitellm() {
		t.Fatal("fallback to modelman.toml not applied")
	}
	if _, err := os.Stat(filepath.Join(home, "agent-wt", "config.toml")); !os.IsNotExist(err) {
		t.Fatalf("config.toml was created by Load (err=%v)", err)
	}
}

// TestUpdateLitellmPersists pins the setter used by `wt litellm on/off/set`:
// it updates memory and config.toml, and writes the file 0600 when it holds an
// api_key (the LiteLLM master key must not be world-readable).
func TestUpdateLitellmPersists(t *testing.T) {
	home := litellmStateEnv(t, "default_tag = \"code\"\n", "")
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
