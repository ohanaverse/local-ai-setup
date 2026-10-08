package config

import (
	"os/exec"
	"slices"
	"strings"
	"unicode"
)

// SeedEnv is what seeding needs to know about the machine. It is built
// before the registry is locked and passed in, so the apply function that
// seeds stays pure and tests can describe a machine without being one. The
// agents are read once, up front; OnPath is asked while apply runs, which is
// still pure: a PATH lookup changes nothing and answers the same on a retry.
type SeedEnv struct {
	// Agents are the agent names wt is configured with, in config.toml order.
	Agents []string
	// AgentProviders are the provider ids those agents list in
	// supported_providers, as the next Load will validate them.
	AgentProviders []string
	// OnPath reports whether a command is installed. nil means none is.
	OnPath func(command string) bool
	// ConfigErr is why config.toml could not be read, when it is there and
	// could not be. Agents and AgentProviders are then empty: seeding goes on
	// without them, and the caller tells the user why no agent got a row.
	ConfigErr error
}

// DefaultSeedEnv describes the real machine: the agents in wt's config.toml,
// the providers they list, and what exec.LookPath finds.
func DefaultSeedEnv() SeedEnv {
	names, providers, err := seedAgents()
	return SeedEnv{
		Agents:         names,
		AgentProviders: providers,
		ConfigErr:      err,
		OnPath: func(command string) bool {
			_, err := exec.LookPath(command)
			return err == nil
		},
	}
}

// seedAgents lists the agents config.toml names and the providers they
// list, as the next Load will see them: Load's schema migration adds an agy
// agent to every existing config.toml and rewrites two older spellings
// (google becomes agy; the opencode agent lists ollama only), and it runs
// only once the registry loads, so on a fresh machine it has not run yet. A
// registry seeded from the file as written would then fail validation on the
// very next run. A missing or unreadable config.toml names nothing — seeding
// tolerates an absent wt setup, as modelman's sync_agent_providers does — and
// an unreadable one is also returned as err, for the caller to report.
// Nothing is written: the migration is applied to this read only.
func seedAgents() (names, providers []string, err error) {
	cfg, exists, err := readConfigFile()
	if err != nil || !exists {
		return nil, nil, err
	}
	migrateAgentRefs(cfg)
	for _, a := range cfg.Agents {
		names = append(names, a.Name)
		for _, id := range a.SupportedProviders {
			if !slices.Contains(providers, id) {
				providers = append(providers, id)
			}
		}
	}
	return names, providers, nil
}

// defaultProviderIDs are the local providers with a default row, in the
// order the rows are added (modelman's DEFAULT_PROVIDER_IDS).
var defaultProviderIDs = []string{"ollama", "omlx", "mlx_lm_server", "mtplx"}

// installedProviderCommands maps a default provider to the command whose
// presence on PATH shows it is installed. mlx_lm_server has none — it is a
// module run from omlx's Python — so it gets a row only from a model that
// references it or an agent that lists it.
var installedProviderCommands = map[string]string{"ollama": "ollama", "omlx": "omlx", "mtplx": "mtplx"}

// defaultProviderRow is the row modelman writes for a default provider
// (registry.py's _DEFAULT_PROVIDER_TEMPLATES through _provider_to_dict, which
// leaves `protocols` out when it is just ["openai-chat"], the default both
// tools assume). A fresh map on every call.
func defaultProviderRow(id string) map[string]any {
	switch id {
	case "ollama":
		return map[string]any{
			"id": "ollama", "name": "Ollama", "location": "local",
			"protocols": []string{"anthropic", "openai-chat"},
			"auth":      map[string]any{"type": "none", "base_url": "http://localhost:11434"},
		}
	case "omlx":
		return map[string]any{
			"id": "omlx", "name": "oMLX", "location": "local",
			"auth": map[string]any{"type": "none", "base_url": "http://localhost:8000"},
		}
	case "mlx_lm_server":
		return map[string]any{
			"id": "mlx_lm_server", "name": "mlx-lm server (target+draft)", "location": "local",
			"auth": map[string]any{"type": "none", "base_url": "http://localhost:8001/v1"},
		}
	case "mtplx":
		return map[string]any{
			"id": "mtplx", "name": "MTPLX", "location": "local", "model_dir": "~/.mtplx/models",
			"auth": map[string]any{"type": "none", "base_url": "http://localhost:8003/v1"},
		}
	}
	return nil
}

// cloudProviderIDs are the cloud providers with a default row: the ones wt
// can route (internal/litellm's policy table).
var cloudProviderIDs = []string{"openrouter"}

// OpenRouterKeyEnv is the environment variable the seeded openrouter row
// names as its key: the name OpenRouter's own tools and this repo's LiteLLM
// setup use.
const OpenRouterKeyEnv = "OPENROUTER_API_KEY"

// cloudProviderRow is the default row for a cloud provider: its name, its
// public address and how it authenticates. auth.secret_ref is the NAME of
// the environment variable that holds the key, never a key: seeding must not
// write a secret or guess where one is kept. The name is there so that a
// missing key fails loudly — a row with no secret_ref gives a LiteLLM route
// with an empty api_key, while a named variable that is unset is refused
// ("resolved empty") and the route is not written. A user who keeps the key
// elsewhere edits secret_ref; seeding never changes a row that exists. A
// fresh map on every call.
func cloudProviderRow(id string) map[string]any {
	if id == "openrouter" {
		return map[string]any{
			"id": "openrouter", "name": "OpenRouter", "location": "cloud",
			"auth": map[string]any{"type": "api_key", "secret_ref": OpenRouterKeyEnv, "base_url": "https://openrouter.ai/api/v1"},
		}
	}
	return nil
}

// SeedRegistryDefaults adds the provider rows a working registry needs and
// reports the ids it added, in the order it added them. It is the Go port of
// modelman's default-provider logic (sync.py's _ensure_provider_entries and
// registry.py's sync_agent_providers) plus one trigger modelman lacks, and
// the one seeding implementation in wt. It only ever appends rows: a row
// that exists is never edited.
//
//   - A default local provider (ollama, omlx, mlx_lm_server, mtplx) gets its
//     default row when a model references it, when an agent lists it, or —
//     for the three that have a command — when that command is on PATH. An
//     installed omlx gets no row when an `omlx-6bit` row exists: the two are
//     one server, and a second row would change which one discovery uses.
//   - A cloud provider with a default row (openrouter) gets it when a model
//     references it or an agent lists it — never from PATH: it has no
//     command. The row holds no key, only the name of the environment
//     variable the key is read from (OpenRouterKeyEnv).
//   - Every agent in env.Agents gets a native cloud provider row under its
//     own name — unless that name is one of the providers above, which then
//     gets its own default row: a native row under `ollama` would take the
//     id every ollama model resolves through.
//
// The agent trigger is what lets wt's own validation pass after seeding on a
// machine that has a config.toml and no registry: Config.Validate refuses an
// agent that lists a provider with no row. unseeded names the providers an
// agent lists that still have no row, because wt has no default for them;
// the caller tells the user, and those stay a hand edit of registry.toml.
//
// It runs on the document UpdateRegistry hands an apply function, so a caller
// can seed and make its own change in one locked write:
//
//	var added, unseeded []string
//	changed, err := config.UpdateRegistry(func(d *config.RegistryDoc) error {
//		var err error
//		added, unseeded, err = config.SeedRegistryDefaults(d, env)
//		return err
//	})
//
// Reads never seed: Load does not call this, and a missing registry stays
// ErrRegistryMissing until `wt model init` or a write creates one.
func SeedRegistryDefaults(d *RegistryDoc, env SeedEnv) (added, unseeded []string, err error) {
	existing := map[string]bool{}
	for _, row := range d.rows("providers") {
		existing[rowID(row)] = true
	}
	listed := map[string]bool{}
	for _, id := range env.AgentProviders {
		listed[id] = true
	}
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
	for id, command := range installedProviderCommands {
		if env.OnPath == nil || !env.OnPath(command) {
			continue
		}
		if id == "omlx" && existing["omlx-6bit"] {
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
	for _, id := range defaultProviderIDs {
		if existing[id] || !(wanted[id] || listed[id]) {
			continue
		}
		if err := add(defaultProviderRow(id)); err != nil {
			return nil, nil, err
		}
	}
	for _, id := range cloudProviderIDs {
		if existing[id] || !(wanted[id] || listed[id]) {
			continue
		}
		if err := add(cloudProviderRow(id)); err != nil {
			return nil, nil, err
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
			return nil, nil, err
		}
	}
	for _, id := range env.AgentProviders {
		if id != "" && !existing[id] && !slices.Contains(unseeded, id) {
			unseeded = append(unseeded, id)
		}
	}
	return added, unseeded, nil
}

// pyTitle is Python's str.title(), which modelman names an agent's provider
// row with: a letter that follows a letter is lowercased, any other letter is
// uppercased ("claude" -> "Claude", "my-agent" -> "My-Agent", "gpt4o" ->
// "Gpt4O"). Ported so a row wt seeds is the row modelman would have seeded.
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
