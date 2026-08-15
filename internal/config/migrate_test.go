package config

import (
	"os"
	"path/filepath"
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

	// Providers: ollama + copilot
	if len(cfg.Providers) != 2 {
		t.Fatalf("expected 2 providers, got %d", len(cfg.Providers))
	}
	if cfg.Providers[0].ID != "ollama" || cfg.Providers[0].Auth.BaseURL != "http://localhost:9999" {
		t.Errorf("ollama provider wrong: %+v", cfg.Providers[0])
	}
	if cfg.Providers[1].ID != "copilot" || cfg.Providers[1].Auth.Type != "native" {
		t.Errorf("copilot provider wrong: %+v", cfg.Providers[1])
	}

	// Models: copilot/native (merged tags), deepseek, kimi. No commented-out model.
	if len(cfg.Models) != 3 {
		t.Fatalf("expected 3 models, got %d: %v", len(cfg.Models), cfg.Models)
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

	// Agents: copilot
	if len(cfg.Agents) != 1 || cfg.Agents[0].Name != "copilot" {
		t.Fatalf("expected 1 copilot agent, got %v", cfg.Agents)
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
