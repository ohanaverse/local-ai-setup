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
	changed, err := UpdateRegistry(func(d *RegistryDoc) error {
		var err error
		added, err = SeedRegistryDefaults(d, env)
		return err
	})
	if err != nil {
		t.Fatalf("seeding: %v", err)
	}
	return added, changed
}

// seedNatives runs SeedNativeProviders inside UpdateRegistry, as the helper
// above does for SeedRegistryDefaults.
func seedNatives(t *testing.T) (added []string, changed bool) {
	t.Helper()
	changed, err := UpdateRegistry(func(d *RegistryDoc) error {
		var err error
		added, err = SeedNativeProviders(d)
		return err
	})
	if err != nil {
		t.Fatalf("seeding natives: %v", err)
	}
	return added, changed
}

// TestSeedWritesTheDefaultRows pins every default row byte for byte: each
// provider's name, location, auth and base_url, its keys in schema order, the
// rows placed ahead of the models. The expected text is what the registries
// already on disk hold for the same machine, so a row `wt-model model init` seeds
// today is the row an older registry has. A base_url that drifted would make
// wt probe a port the provider does not serve, and a name or key order that
// drifted would give a fresh machine a registry that differs from every
// existing one for no reason.
func TestSeedWritesTheDefaultRows(t *testing.T) {
	path := scratchRegistry(t, `[[models]]
id = "mlx_lm_server/pair"
family = "f"
provider_id = "mlx_lm_server"
model_name = "org/target"
`)
	added, changed := seed(t, SeedEnv{OnPath: onPath("ollama", "omlx", "mtplx")})
	if want := []string{"ollama", "omlx", "mlx_lm_server", "mtplx"}; !slices.Equal(added, want) || !changed {
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
// server, but a model that references `omlx` still gets its row; openrouter
// comes from a model that references it, never from PATH — `wt-model model
// add openrouter …` on a registry with no openrouter row must not be refused
// for a row wt can write.
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
	const ollamaModel = `[[models]]
id = "ollama/m"
family = "f"
provider_id = "ollama"
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
		{"a nil OnPath means nothing is installed", "", SeedEnv{}, nil},
		{"only what is on PATH", "", SeedEnv{OnPath: onPath("mtplx")}, []string{"mtplx"}},
		{"mlx_lm_server is never taken from PATH", "", SeedEnv{OnPath: func(string) bool { return true }}, []string{"ollama", "omlx", "mtplx", "sushi"}},
		{"an installed omlx is covered by an omlx-6bit row", sixBit, SeedEnv{OnPath: onPath("omlx", "ollama")}, []string{"ollama"}},
		{"but a model that references omlx gets the row", sixBit + omlxModel, SeedEnv{OnPath: onPath("omlx")}, []string{"omlx"}},
		{"a model that references a default provider gets its row, once", ollamaModel, SeedEnv{OnPath: onPath("ollama")}, []string{"ollama"}},
		{"openrouter is not seeded from PATH", "", SeedEnv{OnPath: func(string) bool { return true }}, []string{"ollama", "omlx", "mtplx", "sushi"}},
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

// TestSeedIsIdempotentAndNeverEditsARow pins the two safety properties of
// seeding. Run twice it changes nothing the second time (so `wt-model model add`
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
	env := SeedEnv{OnPath: onPath("ollama", "omlx", "mtplx")}
	added, changed := seed(t, env)
	if !slices.Equal(added, []string{"mtplx"}) || !changed {
		t.Fatalf("first run: added %v (changed %v), want [mtplx]: ollama and omlx already have a row", added, changed)
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

// TestSeedNativeProvidersAddsTheFixedSet pins the native rows an empty
// registry gets: claude, codex, copilot and agy, in that order, each written
// as a native cloud row, and no row for pi or opencode (they have no native
// provider). The set is fixed, not read from config.toml, so a machine gets
// the same four whether or not the agents are installed or configured. A
// second call adds nothing and changes nothing.
func TestSeedNativeProvidersAddsTheFixedSet(t *testing.T) {
	path := scratchRegistry(t, "")
	added, changed := seedNatives(t)
	if want := []string{"claude", "codex", "copilot", "agy"}; !slices.Equal(added, want) || !changed {
		t.Fatalf("added = %v (changed %v), want %v", added, changed, want)
	}
	// Four short rows fit tomli-w's inline-table limit, so a registry that
	// holds only these is laid out the way tomli-w lays it out: one inline
	// row each (a layout every reader and the next edit already handle).
	const want = `providers = [
    { id = "claude", name = "Claude", location = "cloud", auth = { type = "native" } },
    { id = "codex", name = "Codex", location = "cloud", auth = { type = "native" } },
    { id = "copilot", name = "Copilot", location = "cloud", auth = { type = "native" } },
    { id = "agy", name = "Agy", location = "cloud", auth = { type = "native" } },
]
`
	if got := readFile(t, path); got != want {
		t.Errorf("seeded registry:\n%s\nwant:\n%s", got, want)
	}
	added, changed = seedNatives(t)
	if len(added) != 0 || changed {
		t.Errorf("second call: added %v (changed %v), want nothing", added, changed)
	}
	if got := readFile(t, path); got != want {
		t.Error("a second call changed the file")
	}
}

// TestSeedNativeProvidersLeavesExistingRowsAlone pins that seeding the native
// set only appends: a hand-edited claude row and a pi native row (not in the
// fixed set) stay byte for byte, and the three missing members are appended
// after them. A user's own edit of a native row must survive every `model
// init`. The hand-edited name is long on purpose: a registry of only short
// rows is re-laid out inline by the writer, which would hide a byte-for-byte
// comparison.
func TestSeedNativeProvidersLeavesExistingRowsAlone(t *testing.T) {
	const custom = `[[providers]]
id = "claude"
name = "My Claude, hand-edited with a name long enough to keep its row out of the inline layout"
location = "cloud"

[providers.auth]
type = "native"

[[providers]]
id = "pi"
name = "Pi"
location = "cloud"

[providers.auth]
type = "native"
`
	path := scratchRegistry(t, custom)
	added, changed := seedNatives(t)
	if want := []string{"codex", "copilot", "agy"}; !slices.Equal(added, want) || !changed {
		t.Fatalf("added = %v (changed %v), want %v", added, changed, want)
	}
	got := readFile(t, path)
	if !strings.HasPrefix(got, custom) {
		t.Errorf("existing rows were edited:\n%s", got)
	}
	if strings.Count(got, `id = "pi"`) != 1 || strings.Count(got, `id = "claude"`) != 1 {
		t.Errorf("a row was duplicated:\n%s", got)
	}
}

// TestSeedAddsTheNativeRowAModelReferences pins the other trigger for a native
// row: a registry model that names claude (one added with `wt-model model add
// claude native ...`) gets the claude row from SeedRegistryDefaults, and only
// that native one. A model on pi gets no row: pi has no native provider, so
// the add stays refused with ErrNoProviderRow instead of seeding a row no
// agent could use.
func TestSeedAddsTheNativeRowAModelReferences(t *testing.T) {
	const claudeModel = `[[models]]
id = "claude/native"
family = "claude"
provider_id = "claude"
model_name = "native"
`
	const piModel = `[[models]]
id = "pi/native"
family = "pi"
provider_id = "pi"
model_name = "native"
`
	path := scratchRegistry(t, claudeModel)
	added, changed := seed(t, SeedEnv{})
	if want := []string{"claude"}; !slices.Equal(added, want) || !changed {
		t.Fatalf("added = %v (changed %v), want %v", added, changed, want)
	}
	if got := readFile(t, path); strings.Count(got, `type = "native"`) != 1 || !strings.Contains(got, `{ id = "claude", name = "Claude", location = "cloud", auth = { type = "native" } }`) {
		t.Errorf("want one native row, claude's:\n%s", got)
	}

	scratchRegistry(t, piModel)
	if added, changed := seed(t, SeedEnv{}); len(added) != 0 || changed {
		t.Errorf("a model on pi: added %v (changed %v), want nothing", added, changed)
	}
}

// TestSeedMakesAFreshConfigLoad is the first run on a machine that has a
// config.toml and no registry, end to end: nothing is installed and the file
// names two agents. Seeding reads none of it: the defaults add nothing and
// SeedNativeProviders adds the four native rows, and after both Load succeeds
// and the config validates, so a fresh machine is usable after one init.
func TestSeedMakesAFreshConfigLoad(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	const agents = `default_tag = "code"

[[agents]]
name = "claude"

[[agents]]
name = "opencode"
`
	must(t, Path(), agents)
	env := DefaultSeedEnv()
	env.OnPath = nil // nothing is installed
	added, _ := seed(t, env)
	if len(added) != 0 {
		t.Fatalf("defaults added = %v, want nothing (no ollama, no openrouter)", added)
	}
	added, _ = seedNatives(t)
	if want := []string{"claude", "codex", "copilot", "agy"}; !slices.Equal(added, want) {
		t.Fatalf("natives added = %v, want %v (no opencode row: it has no native provider)", added, want)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load after seeding: %v", err)
	}
	if err := cfg.ValidateAll(); err != nil {
		t.Errorf("the seeded registry should validate against config.toml: %v", err)
	}
}

// TestSeedMakesAFreshConfigWithOldKeysLoad is TestSeedMakesAFreshConfigLoad
// with the agent lists an older build wrote put back (claude lists openrouter,
// which the seed never adds): after seeding, Load and ValidateAll succeed with
// no ollama and no openrouter row. Why: the lists are no longer read, so a
// machine whose config.toml still has them must not be refused for a provider
// the registry lacks.
func TestSeedMakesAFreshConfigWithOldKeysLoad(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	const agents = `default_tag = "code"

[[agents]]
name = "claude"
supported_providers = ["claude", "openrouter"]
default_provider = "claude"

[[agents]]
name = "opencode"
supported_providers = ["opencode"]
`
	must(t, Path(), agents)
	env := DefaultSeedEnv()
	env.OnPath = nil // nothing is installed
	seed(t, env)
	seedNatives(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load after seeding: %v", err)
	}
	if err := cfg.ValidateAll(); err != nil {
		t.Errorf("ValidateAll with the old lists in config.toml: %v", err)
	}
	for _, id := range []string{"ollama", "openrouter"} {
		if cfg.ProviderByID(id) != nil {
			t.Errorf("seeding added a %s row for a list that is no longer read", id)
		}
	}
}

// TestPyTitleMatchesPython pins the port of str.title() against Python's own
// answers, so the display name wt gives an agent's provider row is the one
// older registries already hold for that agent.
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
