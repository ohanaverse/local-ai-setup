package config

import (
	"os/exec"
	"strings"
	"unicode"

	"github.com/ohanaverse/local-ai-setup/wt/internal/provider"
)

// SeedEnv is what seeding needs to know about the machine. It is built
// before the registry is locked and passed in, so the apply function that
// seeds stays pure and tests can describe a machine without being one. OnPath
// is asked while apply runs, which is still pure: a PATH lookup changes
// nothing and answers the same on a retry. No file is read: config.toml has
// no say in which rows are seeded.
type SeedEnv struct {
	// OnPath reports whether a command is installed. nil means none is.
	OnPath func(command string) bool
}

// DefaultSeedEnv describes the real machine: what exec.LookPath finds. It
// reads no file.
func DefaultSeedEnv() SeedEnv {
	return SeedEnv{
		OnPath: func(command string) bool {
			_, err := exec.LookPath(command)
			return err == nil
		},
	}
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
//   - An agent in NativeCapableAgents gets its native cloud provider row
//     when a model references it (`wt-model model add claude native ...`),
//     after the default rows. Which native rows a machine has otherwise is
//     SeedNativeProviders'.
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
			if existing[id] || !wanted[id] {
				continue
			}
			if err := add(provider.SeedRow(id)); err != nil {
				return nil, err
			}
		}
	}
	for _, id := range NativeCapableAgents {
		if existing[id] || !wanted[id] {
			continue
		}
		if err := add(nativeProviderRow(id)); err != nil {
			return nil, err
		}
	}
	return added, nil
}

// SeedNativeProviders appends the native cloud provider row (auth.type
// "native", the agent's own login, no gateway) of every agent in
// NativeCapableAgents that has no row, in that table's order, and reports the
// ids it added. The set is fixed: it does not depend on which agents are
// installed or configured, so an unused row is inert (it is offered only to
// the agent it is named for) and a machine gets the same rows whatever
// config.toml says. A row that exists is never edited. A user who does not
// want one deletes its [[providers]] block from registry.toml by hand (no
// command removes a provider row); `wt-model model init` adds it back.
// It runs on the document UpdateRegistry hands an apply function, like
// SeedRegistryDefaults.
func SeedNativeProviders(d *RegistryDoc) (added []string, err error) {
	existing := map[string]bool{}
	for _, row := range d.rows("providers") {
		existing[rowID(row)] = true
	}
	for _, id := range NativeCapableAgents {
		if existing[id] {
			continue
		}
		if err := d.AddProvider(nativeProviderRow(id)); err != nil {
			return nil, err
		}
		added = append(added, id)
	}
	return added, nil
}

// nativeProviderRow is the native cloud row of agent id: its provider id is
// the agent's name, titled the way older registries hold it.
func nativeProviderRow(id string) map[string]any {
	return map[string]any{
		"id": id, "name": pyTitle(id), "location": "cloud",
		"auth": map[string]any{"type": "native"},
	}
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
