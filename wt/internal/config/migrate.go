package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

// bashArrayRe matches a bash array assignment `NAME=( "a" "b" ... )`,
// capturing the name and the text between the parentheses.
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

// Migrate converts a legacy models.conf into the new TOML Config, if the new
// config does not already exist. Returns whether if performed a migration.
func Migrate() (bool, error) {
	if _, err := os.Stat(Path()); err == nil {
		return false, nil // new config already exists
	} else if !os.IsNotExist(err) {
		return false, err
	}

	data, err := os.ReadFile(filepath.Join(Dir(), "models.conf"))
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
			raw := strings.Trim(line[len("PROVIDER_OLLAMA_BASE_URL="):], `"`)
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
			// this, the model lingers in cfg.Models and fails validation
			// (unknown provider).
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
					// agent entry is added below.
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
				ensureAgent(cfg, n)
			}
		}
	}

	// Ensure an agent entry exists for each noNativeAgent. For "pi", this is
	// the only entry (it never appeared as native:pi). For "opencode", the
	// early continue above means it never gets a native provider here, so
	// this loop just creates a missing entry.
	for name := range noNativeAgents {
		ensureAgent(cfg, name)
	}

	// Seed agy: provider + model + agent. Use the same naming scheme as
	// the registry seed so a fresh install ends up with the same rows an
	// upgraded install has.
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
	ensureAgent(cfg, "agy")

	if err := saveFull(cfg); err != nil {
		return false, err
	}
	return true, nil
}

// saveFull writes the complete cfg — including Providers/Models — to the
// config path. Only the legacy models.conf migration uses it. The
// provider/model sections it seeds stay on disk; nothing reads them back
// (the registry is registry.toml). Regular saves use Save, which persists
// wt-owned fields only.
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

// noNativeAgents lists agents that have no native provider after the
// native-provider alignment. The legacy migration skips creating a native
// provider for these agents (in case a legacy models.conf contains
// "native:opencode") and ensures each one has an agent entry.
var noNativeAgents = map[string]bool{
	"pi":       true,
	"opencode": true,
}

// migrateConfigSchema applies the idempotent fixup that is left to an
// already-decoded cfg: notice and drop wt's legacy [gateway] block. It edits
// no agent: an agent names no provider, so the old google-to-agy rewrite and
// the provider-list rewiring are gone.
//
// Provider and model data are owned by the registry and are reloaded from
// it after migration, so provider/model-level fixups are intentionally
// omitted here.
//
// The fixup is self-extinguishing: once applied, the old pattern no longer
// exists in cfg, so subsequent calls are no-ops. The boolean return is true
// iff the fixup changed cfg.
func migrateConfigSchema(cfg *Config) (bool, error) {
	// GatewayConfig was deleted (Task 9): LiteLLM routing is now a wt-owned
	// [litellm] table (managed with `wt litellm ...`). The decoded Config
	// simply has no Gateway field, so re-saving drops the block; this
	// fixup only detects its presence to point the user at the new
	// control surface. Self-extinguishing: after the triggered Save, the
	// block is gone and the probe no longer matches.
	return dropLegacyGateway(Path()), nil
}

// dropLegacyGateway detects a config.toml still carrying [gateway] (from
// before GatewayConfig was deleted) and reports a one-time notice. The
// Gateway field no longer exists on Config, so decoding config.toml
// simply ignores an unknown [gateway] table (toml decoders skip unknown
// keys by default) — this fixup only needs to detect it well enough to
// tell the user where their settings moved (legacyGatewayNotice).
func dropLegacyGateway(path string) (changed bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	msg, ok := legacyGatewayNotice(raw)
	if !ok {
		return false
	}
	fmt.Fprintln(os.Stderr, msg)
	return true
}

// legacyGatewayNotice is dropLegacyGateway's decision on config.toml's bytes:
// the one notice line and whether there is a [gateway] table at all.
// A table holding subscription_passthrough, tls_cert or tls_key is the host
// side written in the wrong file (serve never reads agent-wt's config.toml):
// the notice then names those keys and the wt-model home's config.toml
// instead of the legacy wording.
func legacyGatewayNotice(raw []byte) (string, bool) {
	var probe struct {
		Gateway map[string]any `toml:"gateway"`
	}
	if err := toml.Unmarshal(raw, &probe); err != nil || probe.Gateway == nil {
		return "", false
	}
	var keys []string
	for _, k := range []string{gatewayPassthroughKey, gatewayTLSCertKey, gatewayTLSKeyKey} {
		if _, ok := probe.Gateway[k]; ok {
			keys = append(keys, k)
		}
	}
	if len(keys) > 0 {
		verb, pron := "is", "it belongs"
		if len(keys) > 1 {
			verb, pron = "are", "they belong"
		}
		named := strings.Join(keys[:len(keys)-1], ", ")
		if named != "" {
			named += " and "
		}
		named += keys[len(keys)-1]
		return BinaryName + ": config.toml: [gateway] " + named + " " + verb + " not read here — " + pron + " under [gateway] in " + ProxyHomeFile() + ", which wt-model serve reads; this block will be dropped on next save", true
	}
	return BinaryName + ": found a legacy [gateway] block in config.toml — LiteLLM routing is now controlled by wt (see 'wt litellm status'); this block will be dropped on next save", true
}

// ensureAgent appends an Agent named name to cfg.Agents when none has that
// name, and reports whether it did. The legacy models.conf migration calls it
// for each agent it seeds; the result is ignored there.
func ensureAgent(cfg *Config, name string) bool {
	if _, err := cfg.AgentByName(name); err == nil {
		return false
	}
	cfg.Agents = append(cfg.Agents, Agent{Name: name})
	return true
}
