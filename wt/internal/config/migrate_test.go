package config

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// loadConfigFile reads and decodes config.toml directly (bypassing Load's
// registry join) so migration tests can assert the raw migration output — the
// providers/models Migrate seeds stay in config.toml and Load never returns
// them: the catalog it joins in comes from registry.toml.
func loadConfigFile(t *testing.T, dir string) *Config {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "agent-wt", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if _, err := toml.Decode(string(data), &cfg); err != nil {
		t.Fatal(err)
	}
	return &cfg
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
	must(t, filepath.Join(cfgDir, "models.conf"), legacy)
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

	// Read the migrated config.toml directly: Load would return
	// registry.toml's providers/models, not the ones Migrate seeded here.
	cfg := loadConfigFile(t, tmp)
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

	legacy := `CODE_MODELS=(
	  "native:agy"
	  "native:google"
	)
	`
	must(t, filepath.Join(tmp, "agent-wt", "models.conf"), legacy)
	t.Setenv("XDG_CONFIG_HOME", tmp)

	migrated, err := Migrate()
	if err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	if !migrated {
		t.Fatal("expected migration to run")
	}

	cfg := loadConfigFile(t, tmp)
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

	agyAgents := 0
	for _, a := range cfg.Agents {
		if a.Name == "agy" {
			agyAgents++
		}
	}
	if agyAgents != 1 {
		t.Fatalf("expected exactly 1 agy agent, got %d in %+v", agyAgents, cfg.Agents)
	}
}

// TestMigrateConfigSchemaOnlyProbesTheGatewayBlock proves the schema fixup
// that is left looks at the legacy [gateway] block and nothing else: a file
// with the block reports changed true, one without it false, and neither adds
// or edits an agent (an agent whose old list holds google is still read as an
// agent named as written, since nothing reads the list). Why: the google to
// agy rename and the agent rewiring are gone, so a fixup that touched an
// agent would write a config.toml nobody asked to change.
func TestMigrateConfigSchemaOnlyProbesTheGatewayBlock(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	const agents = `
[[agents]]
name = "agy"
supported_providers = ["google"]
default_provider = "google"

[[agents]]
name = "opencode"
`
	for _, c := range []struct {
		name, body string
		want       bool
	}{
		{"legacy gateway block", "default_tag = \"code\"\n\n[gateway]\nsubscription_passthrough = true\n" + agents, true},
		{"no gateway block", "default_tag = \"code\"\n" + agents, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			must(t, Path(), c.body)
			cfg := loadConfigFile(t, tmp)
			before := slices.Clone(cfg.Agents)
			changed, err := migrateConfigSchema(cfg)
			if err != nil {
				t.Fatalf("migrateConfigSchema: %v", err)
			}
			if changed != c.want {
				t.Errorf("changed = %v, want %v", changed, c.want)
			}
			if !reflect.DeepEqual(before, cfg.Agents) {
				t.Errorf("agents edited: before %+v, after %+v", before, cfg.Agents)
			}
			if len(cfg.Agents) != 2 || cfg.Agents[0].Name != "agy" || cfg.Agents[1].Name != "opencode" {
				t.Errorf("agents = %+v, want agy and opencode as written", cfg.Agents)
			}
		})
	}
}

// TestWrongFileGatewayKeyPointsAtTheHome pins decision 1's wrong-file notice:
// subscription_passthrough written under [gateway] in agent-wt's config.toml
// (which serve never reads) gets a notice naming the wt-model home's
// config.toml, not the legacy-routing wording, while an old [gateway] block
// without the key keeps the legacy notice.
func TestWrongFileGatewayKeyPointsAtTheHome(t *testing.T) {
	msg, ok := legacyGatewayNotice([]byte("[gateway]\nsubscription_passthrough = true\n"))
	if !ok {
		t.Fatal("no notice for a [gateway] table in agent-wt's config.toml")
	}
	if !strings.Contains(msg, ProxyHomeFile()) || !strings.Contains(msg, "subscription_passthrough") {
		t.Errorf("notice = %q, want it to name subscription_passthrough and %s", msg, ProxyHomeFile())
	}
	if strings.Contains(msg, "LiteLLM routing is now controlled by wt") {
		t.Errorf("notice = %q, want the home pointer, not the legacy wording", msg)
	}
	legacy, ok := legacyGatewayNotice([]byte("[gateway]\nmode = \"litellm\"\n"))
	if !ok || !strings.Contains(legacy, "LiteLLM routing is now controlled by wt") {
		t.Errorf("legacy block notice = %q (%t), want the legacy wording", legacy, ok)
	}
	if _, ok := legacyGatewayNotice([]byte("default_tag = \"code\"\n")); ok {
		t.Error("a notice without any [gateway] table")
	}
}

// TestWrongFileGatewayTLSKeysPointAtTheHome pins decision 13's wrong-file
// case: tls_cert/tls_key under [gateway] in agent-wt's config.toml are never
// read (serve reads only the wt-model home), so the notice names the keys and
// the home's config.toml instead of the legacy wording, and quotes no value.
func TestWrongFileGatewayTLSKeysPointAtTheHome(t *testing.T) {
	for _, body := range []string{
		"[gateway]\ntls_cert = \"/x/SECRETPATH.crt\"\ntls_key = \"/x/SECRETPATH.key\"\n",
		"[gateway]\ntls_key = \"/x/SECRETPATH.key\"\n",
		"[gateway]\nsubscription_passthrough = true\ntls_cert = \"/x/SECRETPATH.crt\"\n",
	} {
		msg, ok := legacyGatewayNotice([]byte(body))
		if !ok {
			t.Fatalf("no notice for %q", body)
		}
		if !strings.Contains(msg, ProxyHomeFile()) || strings.Contains(msg, "LiteLLM routing is now controlled by wt") {
			t.Errorf("notice = %q, want the home pointer", msg)
		}
		for _, k := range []string{"tls_cert", "tls_key", "subscription_passthrough"} {
			if strings.Contains(body, k+" =") && !strings.Contains(msg, k) {
				t.Errorf("notice = %q, want it to name %s", msg, k)
			}
		}
		if strings.Contains(msg, "SECRETPATH") {
			t.Errorf("notice quotes a value: %q", msg)
		}
	}
}

// TestWrongFileGatewayNoticeNamesTheMovedConfigHome pins #700 for the notice
// that sends a [gateway] key to the wt-model home: with XDG_CONFIG_HOME moved
// it names the file under it, never the conventional ~/.config path.
func TestWrongFileGatewayNoticeNamesTheMovedConfigHome(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	msg, ok := legacyGatewayNotice([]byte("[gateway]\nsubscription_passthrough = true\n"))
	if !ok {
		t.Fatal("no notice")
	}
	want := filepath.Join(xdg, "wt-model", "config.toml")
	if !strings.Contains(msg, "under [gateway] in "+want+",") || strings.Contains(msg, "~/.config") {
		t.Errorf("notice = %q, want it to name %s", msg, want)
	}
}

// TestEnsureAgent proves ensureAgent appends an agent named as given the first
// time, reports true, and then finds it and reports false without a second
// entry. Why: the legacy models.conf migration calls it for every agent it
// seeds, and a duplicate name would fail validation of the file it writes.
func TestEnsureAgent(t *testing.T) {
	cfg := &Config{}
	if !ensureAgent(cfg, "pi") {
		t.Error("first ensureAgent reported no change")
	}
	if ensureAgent(cfg, "pi") {
		t.Error("second ensureAgent reported a change")
	}
	if len(cfg.Agents) != 1 || cfg.Agents[0].Name != "pi" {
		t.Errorf("agents = %+v, want one agent named pi", cfg.Agents)
	}
}
