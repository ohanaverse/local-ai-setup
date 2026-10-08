package config

import (
	"os"
	"slices"
	"strings"
	"testing"
)

func onPath(commands ...string) func(string) bool {
	return func(c string) bool { return slices.Contains(commands, c) }
}

func seed(t *testing.T, env SeedEnv) (added []string, changed bool) {
	t.Helper()
	added, _, changed = seedReport(t, env)
	return added, changed
}

// seedReport is seed with the providers seeding had no row for.
func seedReport(t *testing.T, env SeedEnv) (added, unseeded []string, changed bool) {
	t.Helper()
	changed, err := UpdateRegistry(func(d *RegistryDoc) error {
		var err error
		added, unseeded, err = SeedRegistryDefaults(d, env)
		return err
	})
	if err != nil {
		t.Fatalf("seeding: %v", err)
	}
	return added, unseeded, changed
}

// TestSeedWritesTheRowsModelmanWrites pins every default row byte for byte
// against what modelman writes for the same machine (the expected rows were
// produced by modelman's default_provider_entry and sync_agent_providers
// through tomli-w), placed ahead of the models as modelman orders the file.
// A base_url or name that differs would give wt and modelman two ideas of
// the same provider for as long as both exist.
func TestSeedWritesTheRowsModelmanWrites(t *testing.T) {
	path := scratchRegistry(t, `[[models]]
id = "mlx_lm_server/pair"
family = "f"
provider_id = "mlx_lm_server"
model_name = "org/target"
`)
	added, changed := seed(t, SeedEnv{Agents: []string{"claude", "agy"}, OnPath: onPath("ollama", "omlx", "mtplx")})
	if want := []string{"ollama", "omlx", "mlx_lm_server", "mtplx", "claude", "agy"}; !slices.Equal(added, want) || !changed {
		t.Fatalf("added = %v (changed %v), want %v", added, changed, want)
	}
	const want = `[[providers]]
id = "ollama"
name = "Ollama"
location = "local"
protocols = [
    "anthropic",
    "openai-chat",
]

[providers.auth]
type = "none"
base_url = "http://localhost:11434"

[[providers]]
id = "omlx"
name = "oMLX"
location = "local"

[providers.auth]
type = "none"
base_url = "http://localhost:8000"

[[providers]]
id = "mlx_lm_server"
name = "mlx-lm server (target+draft)"
location = "local"

[providers.auth]
type = "none"
base_url = "http://localhost:8001/v1"

[[providers]]
id = "mtplx"
name = "MTPLX"
location = "local"
model_dir = "~/.mtplx/models"

[providers.auth]
type = "none"
base_url = "http://localhost:8003/v1"

[[providers]]
id = "claude"
name = "Claude"
location = "cloud"

[providers.auth]
type = "native"

[[providers]]
id = "agy"
name = "Agy"
location = "cloud"

[providers.auth]
type = "native"

[[models]]
id = "mlx_lm_server/pair"
family = "f"
provider_id = "mlx_lm_server"
model_name = "org/target"
`
	if got := readFile(t, path); got != want {
		t.Errorf("seeded registry:\n%s\nwant:\n%s", got, want)
	}
	// What wt wrote, wt reads and accepts.
	providers, models, err := loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	cfg := &Config{DefaultTag: "code", Providers: providers, Models: models}
	if err := cfg.ValidateAll(); err != nil {
		t.Errorf("the seeded registry should validate: %v", err)
	}
}

// TestSeedAddsOnlyWhatTheMachineNeeds pins the rule for each row: a provider
// nobody references and nothing installs gets no row (wt would probe a server
// the machine does not have); mlx_lm_server never comes from PATH; an
// installed omlx stays out when an omlx-6bit row already stands for that
// server, but a model that references `omlx`, or an agent that lists it,
// still gets its row. A provider an agent lists gets its default row whether
// or not it is installed, because wt refuses a config whose agent lists a
// provider with no row; openrouter comes from a model that references it or
// an agent that lists it, never from PATH — `wt model add openrouter …` on a
// registry with no openrouter row must not be refused for a row wt can write.
func TestSeedAddsOnlyWhatTheMachineNeeds(t *testing.T) {
	const sixBit = `[[providers]]
id = "omlx-6bit"
name = "oMLX 6-bit"
location = "local"

[providers.auth]
type = "none"
base_url = "http://localhost:8000"
`
	const omlxModel = `
[[models]]
id = "omlx/m"
family = "f"
provider_id = "omlx"
model_name = "m"
`
	const openrouterModel = `[[models]]
id = "openrouter/m"
family = "f"
provider_id = "openrouter"
model_name = "org/m"
`
	cases := []struct {
		name     string
		registry string
		env      SeedEnv
		want     []string
	}{
		{"nothing installed, nothing referenced", "", SeedEnv{}, nil},
		{"a nil OnPath means nothing is installed", "", SeedEnv{Agents: []string{"claude"}}, []string{"claude"}},
		{"only what is on PATH", "", SeedEnv{OnPath: onPath("mtplx")}, []string{"mtplx"}},
		{"mlx_lm_server is never taken from PATH", "", SeedEnv{OnPath: func(string) bool { return true }}, []string{"ollama", "omlx", "mtplx"}},
		{"an installed omlx is covered by an omlx-6bit row", sixBit, SeedEnv{OnPath: onPath("omlx", "ollama")}, []string{"ollama"}},
		{"but a model that references omlx gets the row", sixBit + omlxModel, SeedEnv{OnPath: onPath("omlx")}, []string{"omlx"}},
		{"an agent whose name is already a provider is skipped", sixBit, SeedEnv{Agents: []string{"omlx-6bit", "", "pi"}}, []string{"pi"}},
		{"an agent named like a default provider gets the default row, once", "", SeedEnv{Agents: []string{"ollama"}, OnPath: onPath("ollama")}, []string{"ollama"}},
		{"an agent lists a default provider that is not installed", "", SeedEnv{AgentProviders: []string{"ollama", "mlx_lm_server"}}, []string{"ollama", "mlx_lm_server"}},
		{"an agent lists omlx beside an omlx-6bit row", sixBit, SeedEnv{AgentProviders: []string{"omlx"}}, []string{"omlx"}},
		{"an agent lists openrouter", "", SeedEnv{AgentProviders: []string{"openrouter"}}, []string{"openrouter"}},
		{"an agent lists a provider that has its row", sixBit, SeedEnv{AgentProviders: []string{"omlx-6bit"}}, nil},
		{"openrouter is not seeded from PATH", "", SeedEnv{OnPath: func(string) bool { return true }}, []string{"ollama", "omlx", "mtplx"}},
		{"a model that references openrouter gets the row", openrouterModel, SeedEnv{}, []string{"openrouter"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			scratchRegistry(t, c.registry)
			added, _ := seed(t, c.env)
			if !slices.Equal(added, c.want) {
				t.Errorf("added = %v, want %v", added, c.want)
			}
		})
	}
}

// TestSeedAnAgentNamedAfterAProviderGetsThatProvidersRow pins what the agent
// trigger writes for an agent whose name is a provider with a default row,
// when nothing else asks for that provider. The agent's "own" row would be a
// native cloud row under the id `ollama` or `openrouter`, and every model of
// that provider would then resolve through it: no address, no key, wrong
// location. The provider's default row is written instead, once.
func TestSeedAnAgentNamedAfterAProviderGetsThatProvidersRow(t *testing.T) {
	path := scratchRegistry(t, "")
	added, unseeded, _ := seedReport(t, SeedEnv{Agents: []string{"openrouter", "ollama", "pi"}})
	if want := []string{"ollama", "openrouter", "pi"}; !slices.Equal(added, want) || len(unseeded) != 0 {
		t.Fatalf("added = %v, unseeded = %v, want %v and none", added, unseeded, want)
	}
	got := readFile(t, path)
	if n := strings.Count(got, `type = "native"`); n != 1 {
		t.Errorf("%d native rows, want pi's only:\n%s", n, got)
	}
	for _, want := range []string{
		"id = \"ollama\"\nname = \"Ollama\"\nlocation = \"local\"\n",
		"base_url = \"http://localhost:11434\"\n",
		"id = \"openrouter\"\nname = \"OpenRouter\"\nlocation = \"cloud\"\n",
		"type = \"api_key\"\nsecret_ref = \"OPENROUTER_API_KEY\"\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the seeded registry lacks %q:\n%s", want, got)
		}
	}
}

// TestSeedIsIdempotentAndNeverEditsARow pins the two safety properties of
// seeding. Run twice it changes nothing the second time (so `wt model add`
// can seed on every call for free). And a row that exists is left exactly as
// it is, however incomplete: seeding appends, it does not repair — a user's
// own base_url must never be reset to the default.
func TestSeedIsIdempotentAndNeverEditsARow(t *testing.T) {
	const custom = `[[providers]]
id = "ollama"
name = "My Ollama"
location = "local"

[providers.auth]
type = "none"
base_url = "http://gpu-box:11434"

[[providers]]
id = "omlx"
name = "oMLX"

[providers.auth]
type = "none"
`
	path := scratchRegistry(t, custom)
	env := SeedEnv{Agents: []string{"claude"}, OnPath: onPath("ollama", "omlx")}
	added, changed := seed(t, env)
	if !slices.Equal(added, []string{"claude"}) || !changed {
		t.Fatalf("first run: added %v (changed %v), want [claude]", added, changed)
	}
	after := readFile(t, path)
	if !strings.HasPrefix(after, custom) {
		t.Errorf("existing rows were edited:\n%s", after)
	}
	added, changed = seed(t, env)
	if len(added) != 0 || changed {
		t.Errorf("second run: added %v (changed %v), want nothing", added, changed)
	}
	if got := readFile(t, path); got != after {
		t.Error("a second seeding changed the file")
	}
}

// TestSeedCreatesAMissingRegistry pins the first run on a new machine: no
// registry at all, one tool installed. Seeding creates the file with that
// tool's row; on a machine with nothing installed it still creates the file,
// empty, so the next `wt` run no longer reports a missing registry.
func TestSeedCreatesAMissingRegistry(t *testing.T) {
	path := scratchRegistry(t, "")
	added, changed := seed(t, SeedEnv{OnPath: onPath("ollama")})
	if !slices.Equal(added, []string{"ollama"}) || !changed {
		t.Fatalf("added %v (changed %v), want [ollama] and a write", added, changed)
	}
	if got := readFile(t, path); !strings.HasPrefix(got, "[[providers]]\nid = \"ollama\"\n") {
		t.Errorf("new registry:\n%s", got)
	}

	empty := scratchRegistry(t, "")
	added, changed = seed(t, SeedEnv{})
	if len(added) != 0 || !changed {
		t.Fatalf("nothing to add: added %v (changed %v), want an empty registry created", added, changed)
	}
	if info, err := os.Stat(empty); err != nil || info.Size() != 0 {
		t.Errorf("want an empty registry file, got err %v", err)
	}
}

// TestSeedAgentNamesFollowWhatLoadWillValidate pins which agents get a
// provider row. They come from config.toml; and when config.toml exists agy
// is always among them, because Load's schema migration adds an agy agent to
// every existing config.toml and a registry with no agy provider then fails
// validation with `unknown provider "agy"`. The end-to-end check is that a
// registry seeded from this list loads and validates. A config.toml that
// cannot be read names nothing, so seeding still works beside a broken one.
func TestSeedAgentNamesFollowWhatLoadWillValidate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("WT_REGISTRY", "")
	t.Setenv("MODELMAN_REGISTRY", "")
	if names, providers, err := seedAgents(); names != nil || providers != nil || err != nil {
		t.Errorf("with no config.toml: agents = %v, providers = %v, err = %v, want none and no error", names, providers, err)
	}
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte("default_tag = \"code\"\n\n[[agents]]\nname = \"claude\"\nsupported_providers = [\"claude\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	names, providers, err := seedAgents()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"claude", "agy"}; !slices.Equal(names, want) || !slices.Equal(providers, want) {
		t.Fatalf("agents = %v, providers = %v, want %v for both", names, providers, want)
	}
	env := DefaultSeedEnv()
	env.OnPath = nil // this machine's PATH is not the test's business
	if added, _ := seed(t, env); !slices.Equal(added, []string{"claude", "agy"}) {
		t.Fatalf("added = %v, want [claude agy]", added)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load after seeding: %v", err)
	}
	if err := cfg.ValidateAll(); err != nil {
		t.Errorf("the seeded registry should validate against config.toml: %v", err)
	}

	// An unreadable config.toml names nothing, and says why: the caller
	// reports it, or the user sees "nothing to add" with no reason.
	if err := os.WriteFile(Path(), []byte("this is not toml [[["), 0o644); err != nil {
		t.Fatal(err)
	}
	names, providers, err = seedAgents()
	if names != nil || providers != nil {
		t.Errorf("with an unreadable config.toml: agents = %v, providers = %v, want none", names, providers)
	}
	if err == nil || !strings.Contains(err.Error(), "parse config") {
		t.Errorf("with an unreadable config.toml: err = %v, want the parse error", err)
	}
	if env := DefaultSeedEnv(); env.ConfigErr == nil || len(env.Agents) != 0 {
		t.Errorf("DefaultSeedEnv with an unreadable config.toml: ConfigErr = %v, agents = %v", env.ConfigErr, env.Agents)
	}
}

// TestSeedRowsForTheProvidersAgentsList pins the rows the agent trigger
// writes and what it reports. A default local provider gets the same row it
// would get if it were installed. openrouter gets a row with its address and
// no key, only the name of the environment variable the key is read from: a
// row with no secret_ref at all would give a LiteLLM route with an empty
// api_key, where a named variable that is unset is refused out loud. A
// provider wt has no default row for is left out
// and reported, on every run, so the command can tell the user which row to
// write by hand.
func TestSeedRowsForTheProvidersAgentsList(t *testing.T) {
	path := scratchRegistry(t, "")
	env := SeedEnv{AgentProviders: []string{"ollama", "corp-gateway", "openrouter", "", "corp-gateway"}}
	added, unseeded, changed := seedReport(t, env)
	if want := []string{"ollama", "openrouter"}; !slices.Equal(added, want) || !changed {
		t.Fatalf("added = %v (changed %v), want %v", added, changed, want)
	}
	if want := []string{"corp-gateway"}; !slices.Equal(unseeded, want) {
		t.Errorf("unseeded = %v, want %v", unseeded, want)
	}
	const want = `[[providers]]
id = "ollama"
name = "Ollama"
location = "local"
protocols = [
    "anthropic",
    "openai-chat",
]

[providers.auth]
type = "none"
base_url = "http://localhost:11434"

[[providers]]
id = "openrouter"
name = "OpenRouter"
location = "cloud"

[providers.auth]
type = "api_key"
secret_ref = "OPENROUTER_API_KEY"
base_url = "https://openrouter.ai/api/v1"
`
	if got := readFile(t, path); got != want {
		t.Errorf("seeded registry:\n%s\nwant:\n%s", got, want)
	}

	added, unseeded, changed = seedReport(t, env)
	if len(added) != 0 || changed {
		t.Errorf("second run: added %v (changed %v), want nothing", added, changed)
	}
	if want := []string{"corp-gateway"}; !slices.Equal(unseeded, want) {
		t.Errorf("second run: unseeded = %v, want %v", unseeded, want)
	}
}

// TestSeedMakesAFreshConfigLoad is the first run on a machine that has a
// config.toml and no registry, end to end: nothing is installed, one agent
// lists openrouter, and the opencode agent is one Load's migration rewires to
// ollama. After seeding, Load succeeds and the config validates — without the
// agent trigger every wt command would stop on `unknown provider "ollama"`
// and `unknown provider "openrouter"` until the user wrote both rows by hand.
// A provider wt has no default row for is the one case seeding cannot fix:
// it is reported, and validation names it.
func TestSeedMakesAFreshConfigLoad(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("WT_REGISTRY", "")
	t.Setenv("MODELMAN_REGISTRY", "")
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	const agents = `default_tag = "code"

[[agents]]
name = "claude"
supported_providers = ["claude", "openrouter"]

[[agents]]
name = "opencode"
supported_providers = ["opencode"]
`
	if err := os.WriteFile(Path(), []byte(agents), 0o644); err != nil {
		t.Fatal(err)
	}
	env := DefaultSeedEnv()
	env.OnPath = nil // nothing is installed
	if want := []string{"claude", "openrouter", "ollama", "agy"}; !slices.Equal(env.AgentProviders, want) {
		t.Fatalf("providers the agents list = %v, want %v", env.AgentProviders, want)
	}
	added, unseeded, _ := seedReport(t, env)
	if want := []string{"ollama", "openrouter", "claude", "opencode", "agy"}; !slices.Equal(added, want) {
		t.Fatalf("added = %v, want %v", added, want)
	}
	if len(unseeded) != 0 {
		t.Errorf("unseeded = %v, want none", unseeded)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load after seeding: %v", err)
	}
	if err := cfg.ValidateAll(); err != nil {
		t.Errorf("the seeded registry should validate against config.toml: %v", err)
	}
	// The one secret_ref seeding writes is a variable's name, never a key —
	// even with a key in the environment.
	if got := strings.Count(readFile(t, RegistryPath()), "secret_ref"); got != 1 {
		t.Errorf("the seeded registry has %d secret_ref lines, want openrouter's only", got)
	}
	if ref := cfg.ProviderByID("openrouter").Auth.SecretRef; ref != OpenRouterKeyEnv {
		t.Errorf("openrouter's secret_ref = %q, want the variable name %q", ref, OpenRouterKeyEnv)
	}

	// A provider with no default row: reported, not seeded, and still the
	// one thing validation complains about.
	if err := os.WriteFile(Path(), []byte(agents+"\n[[agents]]\nname = \"pi\"\nsupported_providers = [\"corp-gateway\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env = DefaultSeedEnv()
	env.OnPath = nil
	added, unseeded, _ = seedReport(t, env)
	if !slices.Equal(added, []string{"pi"}) || !slices.Equal(unseeded, []string{"corp-gateway"}) {
		t.Fatalf("added = %v, unseeded = %v; want [pi] and [corp-gateway]", added, unseeded)
	}
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	const wantErr = `agent "pi": unknown provider "corp-gateway"`
	if err := cfg.ValidateAll(); err == nil || err.Error() != wantErr {
		t.Errorf("ValidateAll = %v, want only %q", err, wantErr)
	}
}

// TestPyTitleMatchesPython pins the port of str.title() against Python's own
// answers, so the display name wt gives an agent's provider row is the one
// modelman would have given it.
func TestPyTitleMatchesPython(t *testing.T) {
	cases := map[string]string{
		"claude": "Claude", "agy": "Agy", "opencode": "Opencode", "my-agent": "My-Agent",
		"gpt4o": "Gpt4O", "ALLCAPS": "Allcaps", "x_y z": "X_Y Z", "": "",
	}
	for in, want := range cases {
		if got := pyTitle(in); got != want {
			t.Errorf("pyTitle(%q) = %q, want %q", in, got, want)
		}
	}
}
