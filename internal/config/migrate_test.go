package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// parseBashArray extracts the array name and quoted values from a single-line
// bash array assignment. This is the core parser for the legacy format.
func TestParseBashArray(t *testing.T) {
	name, vals := parseBashArray(`CODE_MODELS=("native:copilot" "deepseek-v4-pro:cloud")`)
	if name != "CODE_MODELS" {
		t.Fatalf("name = %q, want CODE_MODELS", name)
	}
	if len(vals) != 2 || vals[0] != "native:copilot" || vals[1] != "deepseek-v4-pro:cloud" {
		t.Fatalf("vals = %v, want the two models", vals)
	}
}

// parseBashArray must return empty for non-array lines (comments, scalar
// assignments, blank lines) so the caller can skip them safely.
func TestParseBashArray_NonArray(t *testing.T) {
	for _, line := range []string{
		`DEFAULT_MODEL="kimi-k2.6:cloud"`,
		`# a comment`,
		``,
		`PROVIDER_OLLAMA_BASE_URL="http://localhost:11434"`,
	} {
		name, vals := parseBashArray(line)
		if name != "" || vals != nil {
			t.Errorf("parseBashArray(%q) = (%q, %v), want empty", line, name, vals)
		}
	}
}

// stripComments removes bash comments so commented-out model entries (e.g.
// `# "nemotron-3-ultra:cloud"`) are not migrated. The real models.conf uses
// comments to disable models, and those must not appear in the new config.
func TestStripComments(t *testing.T) {
	in := "CODE_MODELS=(\n  \"a\"\n  # \"b\"\n  \"c\"\n)\n"
	out := stripComments(in)
	if strings.Contains(out, `"b"`) {
		t.Errorf("stripComments left commented model in output: %q", out)
	}
	if !strings.Contains(out, `"a"`) || !strings.Contains(out, `"c"`) {
		t.Errorf("stripComments removed active models: %q", out)
	}
}

// convertModels maps the three legacy model forms to the new Model struct:
// native:X → provider X with native auth, :cloud → ollama cloud, bare → ollama local.
func TestConvertModels(t *testing.T) {
	raw := []string{"native:claude", "deepseek-v4-pro:cloud", "llama3.3:70b"}
	models, natives := convertModels(raw, "code")

	if len(models) != 3 {
		t.Fatalf("expected 3 models, got %d", len(models))
	}
	if len(natives) != 1 || natives[0] != "claude" {
		t.Fatalf("natives = %v, want [claude]", natives)
	}

	// native:claude
	if models[0].ID != "claude/native" || models[0].ProviderID != "claude" || models[0].Location != LocationCloud {
		t.Errorf("native model wrong: %+v", models[0])
	}

	// :cloud
	if models[1].ID != "ollama/deepseek-v4-pro:cloud" || models[1].ProviderID != "ollama" || models[1].Location != LocationCloud {
		t.Errorf("cloud model wrong: %+v", models[1])
	}
	if models[1].Family != "deepseek-v4-pro" {
		t.Errorf("cloud family = %q, want deepseek-v4-pro", models[1].Family)
	}

	// bare (local)
	if models[2].ID != "ollama/llama3.3:70b" || models[2].Location != LocationLocal {
		t.Errorf("local model wrong: %+v", models[2])
	}
}

// addModels must merge models that share an ID (a model in both CODE_MODELS
// and DESIGN_MODELS) into a single entry with the union of tags, rather than
// producing duplicate IDs that fail validation.
func TestAddModels_MergesTags(t *testing.T) {
	existing := []Model{{ID: "copilot/native", Tags: []string{"code"}}}
	new := []Model{{ID: "copilot/native", Tags: []string{"design"}}}

	out := addModels(existing, new)
	if len(out) != 1 {
		t.Fatalf("expected 1 model after merge, got %d", len(out))
	}
	if !out[0].HasTag("code") || !out[0].HasTag("design") {
		t.Errorf("merged tags = %v, want [code design]", out[0].Tags)
	}
}

// addModels must append models with distinct IDs without touching existing ones.
func TestAddModels_DistinctIDs(t *testing.T) {
	existing := []Model{{ID: "a", Tags: []string{"code"}}}
	new := []Model{{ID: "b", Tags: []string{"code"}}}

	out := addModels(existing, new)
	if len(out) != 2 {
		t.Fatalf("expected 2 models, got %d", len(out))
	}
	if out[0].ID != "a" || out[1].ID != "b" {
		t.Errorf("unexpected models: %v", out)
	}
}

// mergeTags returns the union of two tag slices without duplicates.
func TestMergeTags(t *testing.T) {
	out := mergeTags([]string{"code", "design"}, []string{"design", "code"})
	if len(out) != 2 {
		t.Fatalf("expected 2 unique tags, got %v", out)
	}
	if out[0] != "code" || out[1] != "design" {
		t.Errorf("unexpected tags: %v", out)
	}
}

// Migrate must convert a real multi-line models.conf into a valid config.toml,
// creating providers, models (with merged tags), and agents. This is the
// end-to-end test for the whole migration path.
func TestMigrate_EndToEnd(t *testing.T) {
	tmp := t.TempDir()
	cfgDir := filepath.Join(tmp, "agent-wt")
	os.MkdirAll(cfgDir, 0755)

	legacy := `# comment
CODE_MODELS=(
  "native:copilot"
  "deepseek-v4-pro:cloud"
  # "commented-out:cloud"
)
DESIGN_MODELS=(
  "native:copilot"
  "kimi-k2.6:cloud"
)
PROVIDER_OLLAMA_BASE_URL="http://localhost:9999"
`
	os.WriteFile(filepath.Join(cfgDir, "models.conf"), []byte(legacy), 0644)
	t.Setenv("XDG_CONFIG_HOME", tmp)

	migrated, err := Migrate()
	if err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	if !migrated {
		t.Fatal("expected migration to run")
	}

	// Config file should now exist
	if _, err := os.Stat(filepath.Join(cfgDir, "config.toml")); err != nil {
		t.Fatalf("config.toml not written: %v", err)
	}

	// Load and validate
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	// Providers: ollama + copilot + agy (3 total).
	if len(cfg.Providers) != 3 {
		t.Fatalf("expected 3 providers, got %d", len(cfg.Providers))
	}
	providerByID := map[string]Provider{}
	for _, p := range cfg.Providers {
		providerByID[p.ID] = p
	}
	if p, ok := providerByID["ollama"]; !ok || p.Auth.BaseURL != "http://localhost:9999" {
		t.Errorf("ollama provider wrong: %+v", providerByID["ollama"])
	}
	if p, ok := providerByID["copilot"]; !ok || p.Auth.Type != "native" {
		t.Errorf("copilot provider wrong: %+v", providerByID["copilot"])
	}
	if p, ok := providerByID["agy"]; !ok || p.Name != "Antigravity" {
		t.Errorf("agy provider wrong: %+v", providerByID["agy"])
	}

	// Models: copilot/native (merged tags), deepseek, kimi, agy/native.
	// No commented-out model.
	if len(cfg.Models) != 4 {
		t.Fatalf("expected 4 models, got %d: %v", len(cfg.Models), cfg.Models)
	}
	copilot := cfg.Models[0]
	if copilot.ID != "copilot/native" || !copilot.HasTag("code") || !copilot.HasTag("design") {
		t.Errorf("copilot model wrong: %+v", copilot)
	}
	for _, m := range cfg.Models {
		if m.ID == "ollama/commented-out:cloud" {
			t.Errorf("commented-out model was migrated: %+v", m)
		}
	}

	// Agents: copilot (from nativeSeen), pi + opencode (noNativeAgents loop),
	// agy. 4 total — opencode is now seeded by the noNativeAgents loop.
	if len(cfg.Agents) != 4 {
		t.Fatalf("expected 4 agents, got %d: %v", len(cfg.Agents), cfg.Agents)
	}
	agentByName := map[string]Agent{}
	for _, a := range cfg.Agents {
		agentByName[a.Name] = a
	}
	for _, want := range []string{"copilot", "pi", "opencode", "agy"} {
		if _, ok := agentByName[want]; !ok {
			t.Errorf("expected agent %q, not found in %v", want, cfg.Agents)
		}
	}

	// Second run must not migrate again
	migrated, err = Migrate()
	if err != nil {
		t.Fatalf("second Migrate() error = %v", err)
	}
	if migrated {
		t.Error("expected second migration to be skipped")
	}
}

// Migrate must be a no-op when there is no legacy models.conf.
func TestMigrate_NoLegacyFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	migrated, err := Migrate()
	if err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	if migrated {
		t.Error("expected no migration when models.conf is absent")
	}
}

// Migrate must keep agy restricted to its own agy provider and avoid
// duplicating agy provider/model when a legacy config includes native:agy.
func TestMigrate_AgySeedingIsIdempotent(t *testing.T) {
	tmp := t.TempDir()
	cfgDir := filepath.Join(tmp, "agent-wt")
	os.MkdirAll(cfgDir, 0755)

	legacy := `CODE_MODELS=(
	  "native:agy"
	  "native:google"
	)
	`
	os.WriteFile(filepath.Join(cfgDir, "models.conf"), []byte(legacy), 0644)
	t.Setenv("XDG_CONFIG_HOME", tmp)

	migrated, err := Migrate()
	if err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	if !migrated {
		t.Fatal("expected migration to run")
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	agyProviders := 0
	for _, p := range cfg.Providers {
		if p.ID == "agy" {
			agyProviders++
		}
	}
	if agyProviders != 1 {
		t.Fatalf("expected exactly 1 agy provider, got %d", agyProviders)
	}

	agyNativeModels := 0
	for _, m := range cfg.Models {
		if m.ID == "agy/native" {
			agyNativeModels++
			if !m.HasTag("code") || !m.HasTag("design") {
				t.Fatalf("agy/native tags = %v, want both code and design", m.Tags)
			}
		}
	}
	if agyNativeModels != 1 {
		t.Fatalf("expected exactly 1 agy/native model, got %d", agyNativeModels)
	}

	for _, a := range cfg.Agents {
		if a.Name == "agy" {
			if len(a.SupportedProviders) != 1 || a.SupportedProviders[0] != "agy" || a.DefaultProvider != "agy" {
				t.Fatalf("agy agent not restricted to agy provider: %+v", a)
			}
			return
		}
	}
	t.Fatal("expected agy agent in migrated config")
}

// TestMigrateConfigSchema covers the three idempotent fixups applied to an
// already-decoded config (rename google→agy, ensure agy provider/model/agent,
// remove opencode native). A user with an older config.toml must end up with
// the new shape after a single Load(), and a second Load() must not touch
// the file. Failing this test means users with old configs would silently
// keep references to the dropped google/opencode-native entities.
func TestMigrateConfigSchema(t *testing.T) {
	tmp := t.TempDir()
	cfgDir := filepath.Join(tmp, "agent-wt")
	os.MkdirAll(cfgDir, 0755)
	t.Setenv("XDG_CONFIG_HOME", tmp)

	writeCfg := func(t *testing.T, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("rename google to agy", func(t *testing.T) {
		writeCfg(t, `
default_tag = "code"

[[providers]]
id = "google"
name = "Google"
location = "cloud"
[providers.auth]
type = "native"

[[models]]
id = "google/native"
family = "google"
provider_id = "google"
model_name = "native"
location = "cloud"
tags = ["code", "design"]

[[agents]]
name = "agy"
supported_providers = ["google"]
default_provider = "google"
`)
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}

		// Provider renamed.
		var agy *Provider
		for i := range cfg.Providers {
			if cfg.Providers[i].ID == "agy" {
				agy = &cfg.Providers[i]
				break
			}
		}
		if agy == nil {
			t.Fatalf("agy provider missing after fixup: %+v", cfg.Providers)
		}
		if agy.Name != "Antigravity" {
			t.Errorf("agy provider Name = %q, want %q", agy.Name, "Antigravity")
		}
		if got := countProviders(cfg.Providers, "google"); got != 0 {
			t.Errorf("google provider still present: %d", got)
		}

		// Model renamed.
		if got := countModels(cfg.Models, "agy/native"); got != 1 {
			t.Errorf("agy/native models = %d, want 1", got)
		}
		if got := countModels(cfg.Models, "google/native"); got != 0 {
			t.Errorf("google/native models = %d, want 0", got)
		}

		// Agent rewired.
		a, err := cfg.AgentByName("agy")
		if err != nil {
			t.Fatalf("agy agent missing: %v", err)
		}
		if !slices.Equal(a.SupportedProviders, []string{"agy"}) || a.DefaultProvider != "agy" {
			t.Errorf("agy agent = %+v, want supported=[agy] default=agy", a)
		}
	})

	t.Run("strip opencode native", func(t *testing.T) {
		writeCfg(t, `
default_tag = "code"

[[providers]]
id = "ollama"
name = "Ollama"
[providers.auth]
type = "none"

[[providers]]
id = "opencode"
name = "OpenCode"
location = "cloud"
[providers.auth]
type = "native"

[[models]]
id = "opencode/native"
family = "opencode"
provider_id = "opencode"
model_name = "native"
location = "cloud"
tags = ["code"]

[[models]]
id = "ollama/deepseek-v4-pro:cloud"
family = "deepseek-v4-pro"
provider_id = "ollama"
model_name = "deepseek-v4-pro:cloud"
location = "cloud"
tags = ["code"]

[[agents]]
name = "opencode"
supported_providers = ["opencode"]
default_provider = "opencode"
`)
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}

		if got := countProviders(cfg.Providers, "opencode"); got != 0 {
			t.Errorf("opencode provider still present: %d", got)
		}
		if got := countModels(cfg.Models, "opencode/native"); got != 0 {
			t.Errorf("opencode/native model still present: %d", got)
		}
		a, err := cfg.AgentByName("opencode")
		if err != nil {
			t.Fatalf("opencode agent missing: %v", err)
		}
		if !slices.Equal(a.SupportedProviders, []string{"ollama"}) || a.DefaultProvider != "ollama" {
			t.Errorf("opencode agent = %+v, want supported=[ollama] default=ollama", a)
		}

		// Ollama model survives.
		if got := countModels(cfg.Models, "ollama/deepseek-v4-pro:cloud"); got != 1 {
			t.Errorf("ollama model removed: %d", got)
		}
	})

	t.Run("idempotent", func(t *testing.T) {
		writeCfg(t, `
default_tag = "code"

[[providers]]
id = "ollama"
name = "Ollama"
[providers.auth]
type = "none"

[[agents]]
name = "claude"
supported_providers = ["claude", "ollama"]
default_provider = "claude"
`)
		// First Load applies any fixups (no-op for this clean config).
		if _, err := Load(); err != nil {
			t.Fatalf("first Load: %v", err)
		}
		firstData, err := os.ReadFile(filepath.Join(cfgDir, "config.toml"))
		if err != nil {
			t.Fatal(err)
		}
		// Second Load must not rewrite the file. With no fixups applied, the
		// file should be byte-identical.
		if _, err := Load(); err != nil {
			t.Fatalf("second Load: %v", err)
		}
		secondData, err := os.ReadFile(filepath.Join(cfgDir, "config.toml"))
		if err != nil {
			t.Fatal(err)
		}
		if string(firstData) != string(secondData) {
			t.Errorf("config.toml rewritten on second Load:\nbefore: %s\nafter:  %s", firstData, secondData)
		}
	})
}

// countProviders returns the number of providers whose ID matches id.
func countProviders(providers []Provider, id string) int {
	n := 0
	for _, p := range providers {
		if p.ID == id {
			n++
		}
	}
	return n
}

// countModels returns the number of models whose ID matches id.
func countModels(models []Model, id string) int {
	n := 0
	for _, m := range models {
		if m.ID == id {
			n++
		}
	}
	return n
}
