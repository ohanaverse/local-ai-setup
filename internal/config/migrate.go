package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
)

// legacyPaths returns the paths to the legacy models.conf and state directory.
func legacyPaths() (modelsConf, stateDir string) {
	dir := Dir()
	return filepath.Join(dir, "models.conf"), dir
}

// parseBashArray extracts the quoted tokens from `NAME=( "a" "b" ... )`.
var bashArrayRe = regexp.MustCompile(`([A-Z_]+)=\(([^)]*)\)`)

// quotedRe matches a double-quoted token.
var quotedRe = regexp.MustCompile(`"([^"]*)"`)

// extractQuoted returns the quoted tokens in s (e.g. `"a" "b"` → [a b]).
func extractQuoted(s string) []string {
	var vals []string
	for _, q := range quotedRe.FindAllStringSubmatch(s, -1) {
		vals = append(vals, q[1])
	}
	return vals
}

// stripComments removes bash comments (from # to end of line). Model names
// never contain #, so this is safe for the legacy format.
func stripComments(s string) string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if idx := strings.Index(line, "#"); idx >= 0 {
			line = line[:idx]
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func parseBashArray(line string) (name string, vals []string) {
	m := bashArrayRe.FindStringSubmatch(line)
	if m == nil {
		return "", nil
	}
	return m[1], extractQuoted(m[2])
}

// Migrate converts a legacy models.conf into the new TOML Config, if the new
// config does not already exist. Returns whether if performed a migration.
func Migrate() (bool, error) {
	if _, err := os.Stat(Path()); err == nil {
		return false, nil // new config already exists
	} else if !os.IsNotExist(err) {
		return false, err
	}

	legacyFile, stateDir := legacyPaths()
	data, err := os.ReadFile(legacyFile)
	if os.IsNotExist(err) {
		return false, nil // nothing to migrate
	}
	if err != nil {
		return false, err
	}

	cfg := &Config{DefaultTag: "code"}

	// Always create the ollama provider.
	cfg.Providers = append(cfg.Providers, Provider{
		ID:   "ollama",
		Name: "Ollama",
		Auth: AuthConfig{
			Type:    "none",
			BaseURL: OllamaBaseURL,
		},
	})

	// Track which native agents we've seen so we don't create duplicates.
	nativeSeen := map[string]bool{}

	content := stripComments(string(data))

	// Parse PROVIDER_OLLAMA_BASE_URL (single-line assignment).
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "PROVIDER_OLLAMA_BASE_URL=") {
			raw := trimQuotes(line[len("PROVIDER_OLLAMA_BASE_URL="):])
			if raw != "" {
				cfg.Providers[0].Auth.BaseURL = raw
			}
			break
		}
	}

	// Parse model arrays. The regex matches across newlines, so run it over
	// the whole file content rather than line-by-line (the real models.conf
	// spreads each array across multiple lines).
	for _, m := range bashArrayRe.FindAllStringSubmatch(content, -1) {
		name := m[1]
		vals := extractQuoted(m[2])

		switch name {
		case "CODE_MODELS", "DESIGN_MODELS":
			tag := strings.ToLower(strings.TrimSuffix(name, "_MODELS"))
			models, natives := convertModels(vals, tag)
			// Filter out models for natives that are skipped below (google is
			// a pseudo-native; noNativeAgents have no native provider). Without
			// this, the model lingers in cfg.Models and either fails validation
			// (unknown provider) or gets renamed by migrateConfigSchema into a
			// duplicate (google/native → agy/native).
			filtered := models[:0]
			for _, m := range models {
				if m.ModelName == "native" && (m.Family == "google" || noNativeAgents[m.Family]) {
					continue
				}
				filtered = append(filtered, m)
			}
			cfg.Models = addModels(cfg.Models, filtered)
			for _, n := range natives {
				if nativeSeen[n] {
					continue
				}
				if noNativeAgents[n] {
					// Skip native provider/model creation for agents that don't
					// have a native provider after the schema alignment. The
					// agent entry is added below, pinned to ollama.
					continue
				}
				if n == "google" {
					// "native:google" was a pseudo-native used only by the old
					// agy special case. The agy provider is now seeded directly
					// as "agy", so skip creating a separate "google" provider.
					continue
				}
				nativeSeen[n] = true
				cfg.Providers = append(cfg.Providers, nativeProvider(n))
				cfg.Agents = append(cfg.Agents, nativeAgent(n))
			}
		}
	}

	// Ensure an agent entry exists for each noNativeAgent, pinned to ollama.
	// For "pi", this is the only entry (it never appeared as native:pi).
	// For "opencode", this corrects configs that may have seeded opencode
	// via the native loop before this fix; after the early continue above,
	// opencode will never get a native provider here, so this loop just
	// creates a missing entry or rewires an existing one to ollama-only.
	for name := range noNativeAgents {
		found := false
		for i := range cfg.Agents {
			if cfg.Agents[i].Name != name {
				continue
			}
			found = true
			cfg.Agents[i].SupportedProviders = []string{"ollama"}
			cfg.Agents[i].DefaultProvider = "ollama"
			break
		}
		if !found {
			cfg.Agents = append(cfg.Agents, Agent{
				Name:               name,
				SupportedProviders: []string{"ollama"},
				DefaultProvider:    "ollama",
			})
		}
	}

	// Seed agy: provider + model + agent. Use the same naming scheme as
	// the schema fixup so a fresh install ends up with the same shape that
	// migrateConfigSchema would produce on an upgraded install.
	if cfg.ProviderByID("agy") == nil {
		cfg.Providers = append(cfg.Providers, Provider{
			ID:       "agy",
			Name:     "Antigravity",
			Location: LocationCloud,
			Auth:     AuthConfig{Type: "native"},
		})
	}
	// Always run through addModels so tags are merged if the model already
	// exists (e.g. native:agy in legacy config created it with one tag).
	cfg.Models = addModels(cfg.Models, []Model{{
		ID:         "agy/native",
		Family:     "agy",
		ProviderID: "agy",
		ModelName:  "native",
		Location:   LocationCloud,
		Tags:       []string{"code", "design"},
	}})
	agyFound := false
	for i := range cfg.Agents {
		if cfg.Agents[i].Name == "agy" {
			agyFound = true
			cfg.Agents[i].SupportedProviders = []string{"agy"}
			cfg.Agents[i].DefaultProvider = "agy"
			break
		}
	}
	if !agyFound {
		cfg.Agents = append(cfg.Agents, Agent{
			Name:               "agy",
			SupportedProviders: []string{"agy"},
			DefaultProvider:    "agy",
		})
	}

	if err := saveFull(cfg); err != nil {
		return false, err
	}

	// Migrate rotation position: log last-selected models to stderr.
	if err := migrateRotationState(stateDir); err != nil {
		return false, err
	}
	return true, nil
}

// saveFull writes the complete cfg — including Providers/Models — to the
// config path. Only the legacy models.conf migration uses it: the
// provider/model sections it seeds must survive on disk so `modelman
// migrate` can import them into registry.toml. Regular saves use Save,
// which persists wt-owned fields only.
func saveFull(cfg *Config) error {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
		return err
	}
	return WriteFileAtomic(Path(), buf.Bytes(), 0o644)
}

// convertModels maps legacy model strings to new Model entries.
// Returns the models and the set of native agent names found.
func convertModels(raw []string, tag string) ([]Model, []string) {
	models := make([]Model, 0, len(raw))
	var natives []string

	for _, id := range raw {
		switch {
		case strings.HasPrefix(id, "native:"):
			agent := strings.TrimPrefix(id, "native:")
			natives = append(natives, agent)
			models = append(models, Model{
				ID:         agent + "/native",
				Family:     agent,
				ProviderID: agent,
				ModelName:  "native",
				Location:   LocationCloud,
				Tags:       []string{tag},
			})
		case strings.HasSuffix(id, ":cloud"):
			family := strings.TrimSuffix(id, ":cloud")
			models = append(models, Model{
				ID:         "ollama/" + id,
				Family:     family,
				ProviderID: "ollama",
				ModelName:  id,
				Location:   LocationCloud,
				Tags:       []string{tag},
			})
		default:
			models = append(models, Model{
				ID:         "ollama/" + id,
				Family:     id,
				ProviderID: "ollama",
				ModelName:  id,
				Location:   LocationLocal,
				Tags:       []string{tag},
			})
		}
	}
	return models, natives
}

// nativeProvider creates a Provider for a native agent (e.g. claude, copilot)
func nativeProvider(agent string) Provider {
	return Provider{
		ID:       agent,
		Name:     displayName(agent),
		Location: LocationCloud,
		Auth:     AuthConfig{Type: "native"},
	}
}

// nativeAgent creates an Agent entry for a native agent.
func nativeAgent(agent string) Agent {
	return Agent{
		Name:               agent,
		SupportedProviders: []string{agent, "ollama"},
		DefaultProvider:    agent,
	}
}

// addModels appends models, merging tags for models that share an ID.
// A model can appear in both CODE_MODELS and DESIGN_MODELS; it should become
// a single entry with both tags rather than two duplicate IDs.
func addModels(existing []Model, new []Model) []Model {
	for _, m := range new {
		found := false
		for i := range existing {
			if existing[i].ID == m.ID {
				existing[i].Tags = mergeTags(existing[i].Tags, m.Tags)
				found = true
				break
			}
		}
		if !found {
			existing = append(existing, m)
		}
	}
	return existing
}

// mergeTags returns the union of two tag slices, preserving order and
// removing duplicates.
func mergeTags(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range append(a, b...) {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

// displayName returns a human-readable name for an agent id.
func displayName(agent string) string {
	switch agent {
	case "claude":
		return "Claude Code"
	case "copilot":
		return "GitHub Copilot"
	case "codex":
		return "OpenAI Codex"
	case "pi":
		return "pi Coding Agent"
	default:
		return agent
	}
}

// lastSelected reads the last line of a legacy <mode>.state file. Used only
// by migrateRotationState for one-time migration logging.
func lastSelected(stateDir, mode string) string {
	data, err := os.ReadFile(filepath.Join(stateDir, "rotation-"+mode+".state"))
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 0 {
		return ""
	}
	return lines[len(lines)-1]
}

func migrateRotationState(stateDir string) error {
	for _, mode := range []string{"code", "design"} {
		last := lastSelected(stateDir, mode)
		if last != "" {
			fmt.Fprintf(os.Stderr, "wt: migrated %s rotation; last selected: %s\n", mode, last)
		}
	}
	return nil
}

func trimQuotes(s string) string {
	return strings.Trim(s, `"`)
}

// noNativeAgents lists agents that, after the native-provider alignment, have
// no native provider: they only support ollama. The legacy migration skips
// creating a native provider for these agents (in case a legacy models.conf
// contains "native:opencode") and ensures each one has an agent entry pinned
// to ollama only.
var noNativeAgents = map[string]bool{
	"pi":       true,
	"opencode": true,
}

// migrateConfigSchema applies idempotent fixups to an already-decoded cfg:
//  1. Rename the legacy "google" provider/model/agent references to "agy".
//  2. Ensure an agy provider, agy/native model, and agy agent exist.
//  3. Remove the opencode native provider/model and rewire the opencode
//     agent to ollama only.
//
// Each fixup is self-extinguishing — once applied, the old pattern no longer
// exists in cfg, so subsequent calls are no-ops. The boolean return is true
// iff any fixup actually changed cfg.
func migrateConfigSchema(cfg *Config) (bool, error) {
	changed := false

	// ── Fixup 1: rename "google" → "agy" everywhere ──────────────────
	// If "agy" already exists, drop the legacy "google" provider to avoid
	// creating a duplicate provider ID that would fail validation.
	{
		hasAgy := cfg.ProviderByID("agy") != nil
		filtered := make([]Provider, 0, len(cfg.Providers))
		for _, p := range cfg.Providers {
			if p.ID == "google" {
				if !hasAgy {
					p.ID = "agy"
					p.Name = "Antigravity"
					filtered = append(filtered, p)
				}
				// else: drop the legacy google provider — agy already exists
				changed = true
				continue
			}
			filtered = append(filtered, p)
		}
		cfg.Providers = filtered
	}
	for i := range cfg.Models {
		if cfg.Models[i].ProviderID == "google" {
			cfg.Models[i].ProviderID = "agy"
			changed = true
		}
	}
	// If "agy/native" already exists, drop the legacy "google/native" model
	// to avoid a duplicate model ID that would fail validation.
	{
		hasAgyNative := false
		for _, m := range cfg.Models {
			if m.ID == "agy/native" {
				hasAgyNative = true
				break
			}
		}
		filtered := make([]Model, 0, len(cfg.Models))
		for _, m := range cfg.Models {
			if m.ID == "google/native" {
				if !hasAgyNative {
					m.ID = "agy/native"
					m.Family = "agy"
					filtered = append(filtered, m)
				}
				// else: drop the legacy google/native model — agy/native already exists
				changed = true
				continue
			}
			filtered = append(filtered, m)
		}
		cfg.Models = filtered
	}
	for i := range cfg.Agents {
		a := &cfg.Agents[i]
		rewired := false
		for j, p := range a.SupportedProviders {
			if p == "google" {
				a.SupportedProviders[j] = "agy"
				rewired = true
			}
		}
		if a.DefaultProvider == "google" {
			a.DefaultProvider = "agy"
			rewired = true
		}
		if rewired {
			changed = true
		}
	}

	// ── Fixup 2: ensure agy provider/model/agent exist ───────────────
	if cfg.ProviderByID("agy") == nil {
		cfg.Providers = append(cfg.Providers, Provider{
			ID:       "agy",
			Name:     "Antigravity",
			Location: LocationCloud,
			Auth:     AuthConfig{Type: "native"},
		})
		changed = true
	}
	if !hasModel(cfg.Models, "agy/native") {
		cfg.Models = append(cfg.Models, Model{
			ID:         "agy/native",
			Family:     "agy",
			ProviderID: "agy",
			ModelName:  "native",
			Location:   LocationCloud,
			Tags:       []string{"code", "design"},
		})
		changed = true
	}
	agyAgentFound := false
	for i := range cfg.Agents {
		if cfg.Agents[i].Name == "agy" {
			agyAgentFound = true
			if !slices.Equal(cfg.Agents[i].SupportedProviders, []string{"agy"}) ||
				cfg.Agents[i].DefaultProvider != "agy" {
				cfg.Agents[i].SupportedProviders = []string{"agy"}
				cfg.Agents[i].DefaultProvider = "agy"
				changed = true
			}
			break
		}
	}
	if !agyAgentFound {
		cfg.Agents = append(cfg.Agents, Agent{
			Name:               "agy",
			SupportedProviders: []string{"agy"},
			DefaultProvider:    "agy",
		})
		changed = true
	}

	// ── Fixup 3: remove opencode native provider/model ───────────────
	newProviders := make([]Provider, 0, len(cfg.Providers))
	opencodeRemoved := false
	for _, p := range cfg.Providers {
		if p.ID == "opencode" {
			opencodeRemoved = true
			continue
		}
		newProviders = append(newProviders, p)
	}
	cfg.Providers = newProviders

	newModels := make([]Model, 0, len(cfg.Models))
	opencodeModelRemoved := false
	for _, m := range cfg.Models {
		if m.ProviderID == "opencode" {
			opencodeModelRemoved = true
			continue
		}
		newModels = append(newModels, m)
	}
	cfg.Models = newModels

	for i := range cfg.Agents {
		if cfg.Agents[i].Name != "opencode" {
			continue
		}
		if !slices.Equal(cfg.Agents[i].SupportedProviders, []string{"ollama"}) ||
			cfg.Agents[i].DefaultProvider != "ollama" {
			cfg.Agents[i].SupportedProviders = []string{"ollama"}
			cfg.Agents[i].DefaultProvider = "ollama"
			changed = true
		}
	}

	if opencodeRemoved || opencodeModelRemoved {
		changed = true
	}

	return changed, nil
}

// hasModel reports whether a model with the given id exists in models.
func hasModel(models []Model, id string) bool {
	for _, m := range models {
		if m.ID == id {
			return true
		}
	}
	return false
}
