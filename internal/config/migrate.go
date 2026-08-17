package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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
		case "CODE_MODELS":
			models, natives := convertModels(vals, "code")
			cfg.Models = addModels(cfg.Models, models)
			for _, n := range natives {
				if !nativeSeen[n] {
					nativeSeen[n] = true
					cfg.Providers = append(cfg.Providers, nativeProvider(n))
					cfg.Agents = append(cfg.Agents, nativeAgent(n))
				}
			}
		case "DESIGN_MODELS":
			models, natives := convertModels(vals, "design")
			cfg.Models = addModels(cfg.Models, models)
			for _, n := range natives {
				if !nativeSeen[n] {
					nativeSeen[n] = true
					cfg.Providers = append(cfg.Providers, nativeProvider(n))
					cfg.Agents = append(cfg.Agents, nativeAgent(n))
				}
			}
		}
	}

	// Pi is a special case: it does not use native:pi in legacy configs.
	// It uses ollama models via its own models.json, so it has no native
	// provider. Add the agent entry if not already present.
	piFound := false
	for _, a := range cfg.Agents {
		if a.Name == "pi" {
			piFound = true
			break
		}
	}
	if !piFound {
		cfg.Agents = append(cfg.Agents, Agent{
			Name:               "pi",
			SupportedProviders: []string{"ollama"},
			DefaultProvider:    "ollama",
		})
	}

	if err := Save(cfg); err != nil {
		return false, err
	}

	// Migrate rotation position: log last-selected models to stderr.
	if err := migrateRotationState(stateDir); err != nil {
		return false, err
	}
	return true, nil
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

// LastSelected returns the last model ID chosen in the rotation-<tag>.state
// file under stateDir. Returns "" if the file is missing or has no second
// line (i.e. the rotation has never been advanced). Used by Rotation.Next to
// cross-skip models between tag groups.
func LastSelected(stateDir, tag string) string {
	data, err := os.ReadFile(filepath.Join(stateDir, "rotation-"+tag+".state"))
	if err != nil {
		return ""
	}
	parts := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(parts) < 2 {
		return ""
	}
	return parts[1]
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
