package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sessionHome points the config home at a scratch dir holding the given
// config.toml (and no registry) and returns the config.toml path.
func sessionHome(t *testing.T, body string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "agent-wt"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(home, "agent-wt", "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestLoadSessionNeedsNoRegistry proves the session client loads with only a
// config.toml: no registry, no provider validation of an unknown id.
func TestLoadSessionNeedsNoRegistry(t *testing.T) {
	sessionHome(t, `default_tag = "code"

[[agents]]
name = "claude"
supported_providers = ["no-such-provider"]

[model-server]
url = "http://127.0.0.1:9"
token_file = "~/tok"
`)
	cfg, err := LoadSession()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Models) != 0 || len(cfg.Providers) != 0 {
		t.Errorf("models/providers not empty: %d/%d", len(cfg.Models), len(cfg.Providers))
	}
	ms := cfg.ModelServer()
	if ms.URL != "http://127.0.0.1:9" || ms.TokenFile != "~/tok" {
		t.Errorf("model server = %+v", ms)
	}
	a, err := cfg.AgentByName("claude")
	if err != nil || a.SupportedProviders[0] != "no-such-provider" {
		t.Errorf("claude agent = %+v, %v", a, err)
	}
}

// TestLoadSessionSynthesizesDriverAgents proves a driver with no entry gets an
// in-memory all-providers agent, an explicit entry is kept as written, and a
// PatchSave never writes a synthesized agent.
func TestLoadSessionSynthesizesDriverAgents(t *testing.T) {
	path := sessionHome(t, `default_tag = "code"

[[agents]]
name = "claude"
supported_providers = ["ollama"]

[model-server]
url = "http://127.0.0.1:9"
token_file = "/t"
`)
	cfg, err := LoadSession("claude", "pi")
	if err != nil {
		t.Fatal(err)
	}
	pi, err := cfg.AgentByName("pi")
	if err != nil || !pi.AllProviders {
		t.Fatalf("pi = %+v, %v", pi, err)
	}
	cl, err := cfg.AgentByName("claude")
	if err != nil || cl.AllProviders || len(cl.SupportedProviders) != 1 || cl.SupportedProviders[0] != "ollama" {
		t.Errorf("claude widened or lost: %+v, %v", cl, err)
	}
	if err := cfg.PatchSave(func(fresh *Config) { fresh.DefaultTag = "design" }); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), `name = "pi"`) || strings.Contains(string(raw), `name = 'pi'`) {
		t.Errorf("synthesized agent was written:\n%s", raw)
	}
}

// TestLoadSessionMigratesAgentRefsInMemory proves the legacy google reference
// reads as agy while the file on disk stays untouched.
func TestLoadSessionMigratesAgentRefsInMemory(t *testing.T) {
	body := `default_tag = "code"

[[agents]]
name = "claude"
supported_providers = ["google", "ollama"]
default_provider = "google"
`
	path := sessionHome(t, body)
	cfg, err := LoadSession()
	if err != nil {
		t.Fatal(err)
	}
	a, err := cfg.AgentByName("claude")
	if err != nil || a.SupportedProviders[0] != "agy" || a.DefaultProvider != "agy" {
		t.Errorf("claude = %+v, %v", a, err)
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != body {
		t.Errorf("file changed:\n%s", raw)
	}
}

// TestModelServerTableSurvivesAnUnrelatedSave proves an unrelated PatchSave
// keeps the [model-server] table intact, like [launch]: struct-equal when
// read back and still present as a table in the file.
func TestModelServerTableSurvivesAnUnrelatedSave(t *testing.T) {
	path := sessionHome(t, `default_tag = "code"

[model-server]
url = "http://127.0.0.1:9"
token_file = "~/tok"
`)
	cfg, err := LoadSession()
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.PatchSave(func(fresh *Config) { fresh.DefaultTag = "design" }); err != nil {
		t.Fatal(err)
	}
	again, _, err := readConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	if again.ModelServerTable == nil || *again.ModelServerTable != *cfg.ModelServerTable {
		t.Errorf("table = %+v, want %+v", again.ModelServerTable, cfg.ModelServerTable)
	}
	raw, _ := os.ReadFile(path)
	for _, want := range []string{"[model-server]", `url = "http://127.0.0.1:9"`, `token_file = "~/tok"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("%q missing:\n%s", want, raw)
		}
	}
}

// TestWholeFileSaveNeverWritesSynthesizedAgents proves Save of a LoadSession
// config drops the in-memory all-providers agents (R13) while keeping an
// explicit entry, so a whole-file save cannot persist them.
func TestWholeFileSaveNeverWritesSynthesizedAgents(t *testing.T) {
	path := sessionHome(t, `default_tag = "code"

[[agents]]
name = "claude"
supported_providers = ["ollama"]

[model-server]
url = "http://127.0.0.1:9"
token_file = "/t"
`)
	cfg, err := LoadSession("claude", "pi")
	if err != nil {
		t.Fatal(err)
	}
	before := len(cfg.Agents)
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), `"pi"`) || strings.Contains(string(raw), `'pi'`) {
		t.Errorf("synthesized agent was written:\n%s", raw)
	}
	if !strings.Contains(string(raw), `name = "claude"`) {
		t.Errorf("explicit agent lost:\n%s", raw)
	}
	if len(cfg.Agents) != before {
		t.Errorf("Save mutated the caller's agents: %d", len(cfg.Agents))
	}
}

// TestInlineTokenIsAProblem proves an inline token is reported (it is never
// read) and a clean table reports none.
func TestInlineTokenIsAProblem(t *testing.T) {
	got := ModelServerState{URL: "u", TokenFile: "f", Token: "secret"}.Problems()
	want := "[model-server] token is not read from config.toml, which is not private — put the token in a file with mode 0600 and set token_file"
	if len(got) != 1 || got[0] != want {
		t.Errorf("problems = %q", got)
	}
	if p := (ModelServerState{URL: "u", TokenFile: "f"}).Problems(); len(p) != 0 {
		t.Errorf("clean table problems = %q", p)
	}
}

// TestIgnoredLitellmKeys proves a [litellm] enabled = true is named as ignored.
func TestIgnoredLitellmKeys(t *testing.T) {
	c := &Config{}
	if k := c.IgnoredLitellmKeys(); len(k) != 0 {
		t.Errorf("empty = %q", k)
	}
	c.LitellmTable = &LitellmState{Enabled: true}
	if k := c.IgnoredLitellmKeys(); len(k) != 1 || k[0] != "[litellm] enabled" {
		t.Errorf("enabled = %q", k)
	}
	c.LitellmTable = &LitellmState{}
	if k := c.IgnoredLitellmKeys(); len(k) != 0 {
		t.Errorf("disabled = %q", k)
	}
}

// TestLegacyLitellmKeysSurviveASave pins R4 across the flip: the [litellm]
// table is host state (`wt-model serve` reads its url and api_key as the
// read-only fallback of the wt-model home) that the session leaves alone, so
// every save path a session or host command takes (an unrelated PatchSave of
// a LoadSession config, a whole-file Save) keeps url, api_key and the retired
// enabled key exactly as written. A save that dropped them would cut the
// model server off from LiteLLM, and one that dropped enabled would rewrite a
// file wt only promised to ignore.
func TestLegacyLitellmKeysSurviveASave(t *testing.T) {
	const body = `default_tag = "code"

[litellm]
enabled = true
url = "http://127.0.0.1:4000"
api_key = "sk-host"

[model-server]
url = "http://127.0.0.1:9"
token_file = "/t"
`
	want := LitellmState{Enabled: true, URL: "http://127.0.0.1:4000", APIKey: "sk-host"}
	check := func(t *testing.T, step string) {
		t.Helper()
		again, _, err := readConfigFile()
		if err != nil {
			t.Fatal(err)
		}
		if again.LitellmTable == nil || *again.LitellmTable != want {
			t.Errorf("%s: [litellm] = %+v, want %+v", step, again.LitellmTable, want)
		}
	}
	sessionHome(t, body)
	cfg, err := LoadSession("claude")
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.PatchSave(func(fresh *Config) { fresh.DefaultTag = "design" }); err != nil {
		t.Fatal(err)
	}
	check(t, "PatchSave")
	cfg, err = LoadSession("claude")
	if err != nil {
		t.Fatal(err)
	}
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	check(t, "Save")
}
