package config

import (
	"os/exec"
	"strings"
	"unicode"

	"github.com/ohanaverse/local-ai-setup/wt/internal/provider"
)

// SeedEnv is what seeding needs to know about the machine. It is built
// before the registry is locked and passed in, so the apply function that
// seeds stays pure and tests can describe a machine without being one. The
// agents are read once, up front; OnPath is asked while apply runs, which is
// still pure: a PATH lookup changes nothing and answers the same on a retry.
type SeedEnv struct {
	// Agents are the agent names wt is configured with, in config.toml order.
	Agents []string
	// OnPath reports whether a command is installed. nil means none is.
	OnPath func(command string) bool
	// ConfigErr is why config.toml could not be read, when it is there and
	// could not be. Agents is then empty: seeding goes on without it, and the
	// caller tells the user why no agent got a row.
	ConfigErr error
}

// DefaultSeedEnv describes the real machine: the agents in wt's config.toml
// and what exec.LookPath finds.
func DefaultSeedEnv() SeedEnv {
	names, err := seedAgents()
	return SeedEnv{
		Agents:    names,
		ConfigErr: err,
		OnPath: func(command string) bool {
			_, err := exec.LookPath(command)
			return err == nil
		},
	}
}

// seedAgents lists the agent names config.toml carries, in file order, and
// reads nothing else from them: no list an agent carries decides a provider
// row, and no schema migration is applied (the one it had renamed a name in
// those lists). A missing or unreadable config.toml names nothing — seeding
// tolerates an absent wt setup — and an unreadable one is also returned as
// err, for the caller to report. Nothing is written.
func seedAgents() (names []string, err error) {
	cfg, exists, err := readConfigFile()
	if err != nil || !exists {
		return nil, err
	}
	for _, a := range cfg.Agents {
		names = append(names, a.Name)
	}
	return names, nil
}

// SeedableProviderIDs is every provider wt can add a default row for by
// itself — the local ones and the cloud ones, from internal/provider's table.
// Callers that offer providers for a new model row, like the Models tab's add
// form, take it so the choice grows with the table instead of copying it.
func SeedableProviderIDs() []string {
	return append(provider.SeedableLocalIDs(), provider.SeedableCloudIDs()...)
}

// OpenRouterKeyEnv is the environment variable the seeded openrouter row
// names as its key: the name OpenRouter's own tools and this repo's LiteLLM
// setup use. The row itself is internal/provider's: auth.secret_ref is the
// NAME of that variable, never a key, so seeding writes no secret and a
// missing key fails loudly — a row with no secret_ref gives a LiteLLM route
// with an empty api_key, while a named variable that is unset is refused
// ("resolved empty") and the route is not written. A user who keeps the key
// elsewhere edits secret_ref; seeding never changes a row that exists.
const OpenRouterKeyEnv = provider.OpenRouterKeyEnv

// SeedRegistryDefaults adds the provider rows a working registry needs and
// reports the ids it added, in the order it added them. It is the
// default-provider rule (a model references the provider, or its command is
// installed) and the one seeding implementation in wt. It only ever appends
// rows: a row that exists is never edited.
//
//   - A default local provider (ollama, omlx, mlx_lm_server, mtplx, rec,
//     sushi) gets its default row when a model references it or — for the
//     four whose seed names a command (ollama, omlx, mtplx, sushi) — when
//     that command is on PATH. mlx_lm_server has no command and rec
//     deliberately none (SoX ships its own `rec`). An installed omlx gets no
//     row when an `omlx-6bit` row exists: the two are one server, and a
//     second row would change which one discovery uses.
//   - A cloud provider with a default row (openrouter) gets it when a model
//     references it — never from PATH: it has no command. The row holds no
//     key, only the name of the environment variable the key is read from
//     (OpenRouterKeyEnv).
//   - Every agent in env.Agents gets a native cloud provider row under its
//     own name — unless that name is one of the providers above, which then
//     gets its own default row: a native row under `ollama` would take the
//     id every ollama model resolves through.
//
// What an agent's supported_providers list says decides no row.
//
// It runs on the document UpdateRegistry hands an apply function, so a caller
// can seed and make its own change in one locked write:
//
//	var added []string
//	changed, err := config.UpdateRegistry(func(d *config.RegistryDoc) error {
//		var err error
//		added, err = config.SeedRegistryDefaults(d, env)
//		return err
//	})
//
// Reads never seed: Load does not call this, and a missing registry stays
// ErrRegistryMissing until `wt-model model init` or a write creates one.
func SeedRegistryDefaults(d *RegistryDoc, env SeedEnv) (added []string, err error) {
	existing := map[string]bool{}
	for _, row := range d.rows("providers") {
		existing[rowID(row)] = true
	}
	listed := map[string]bool{}
	// An agent named after a provider with a default row asks for that row.
	for _, name := range env.Agents {
		listed[name] = true
	}
	wanted := map[string]bool{}
	for _, row := range d.rows("models") {
		if v, _ := row.Get("provider_id"); v != nil {
			if id, ok := v.(string); ok {
				wanted[id] = true
			}
		}
	}
	for id, command := range provider.InstalledCommands() {
		if env.OnPath == nil || !env.OnPath(command) {
			continue
		}
		if by := provider.PathSuppressedBy(id); by != "" && existing[by] {
			continue
		}
		wanted[id] = true
	}
	add := func(row map[string]any) error {
		id, _ := row["id"].(string)
		if err := d.AddProvider(row); err != nil {
			return err
		}
		existing[id] = true
		added = append(added, id)
		return nil
	}
	for _, ids := range [][]string{provider.SeedableLocalIDs(), provider.SeedableCloudIDs()} {
		for _, id := range ids {
			if existing[id] || !(wanted[id] || listed[id]) {
				continue
			}
			if err := add(provider.SeedRow(id)); err != nil {
				return nil, err
			}
		}
	}
	for _, name := range env.Agents {
		if name == "" || existing[name] {
			continue
		}
		row := map[string]any{
			"id": name, "name": pyTitle(name), "location": "cloud",
			"auth": map[string]any{"type": "native"},
		}
		if err := add(row); err != nil {
			return nil, err
		}
	}
	return added, nil
}

// pyTitle is Python's str.title(), the casing the name of an agent's
// provider row has in older registries: a letter that follows a letter is
// lowercased, any other letter is uppercased ("claude" -> "Claude",
// "my-agent" -> "My-Agent", "gpt4o" -> "Gpt4O"). Kept so a row seeded today
// reads like one seeded before.
func pyTitle(s string) string {
	var b strings.Builder
	afterLetter := false
	for _, r := range s {
		if unicode.IsUpper(r) || unicode.IsLower(r) || unicode.IsTitle(r) {
			if afterLetter {
				b.WriteRune(unicode.ToLower(r))
			} else {
				b.WriteRune(unicode.ToTitle(r))
			}
			afterLetter = true
			continue
		}
		b.WriteRune(r)
		afterLetter = false
	}
	return b.String()
}
