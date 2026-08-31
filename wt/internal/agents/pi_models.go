package agents

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/ohanaverse/agent-worktree/internal/config"
)

// piModelsFile mirrors the on-disk shape of ~/.pi/agent/models.json: a map of
// provider id to provider config. wt manages exactly two keys — "ollama" (the
// direct local-Ollama provider) and "litellm" (a wt-created gateway provider);
// every other provider entry round-trips untouched.
type piModelsFile struct {
	Providers map[string]piProvider `json:"providers"`
}

// piProvider is one provider block in pi's models.json. Only these fields are
// relevant to wt; unknown siblings pi supports round-trip through the generic
// JSON decode only if they are absent — extra fields inside a provider block
// are dropped, so wt never writes provider blocks it does not own.
type piProvider struct {
	API     string    `json:"api"`
	APIKey  string    `json:"apiKey"`
	BaseURL string    `json:"baseUrl"`
	Models  []piModel `json:"models"`
}

// piModel is a single entry in pi's model catalog.
type piModel struct {
	Launch        bool     `json:"_launch"`
	ContextWindow int      `json:"contextWindow"`
	ID            string   `json:"id"`
	Input         []string `json:"input"`
	Reasoning     bool     `json:"reasoning"`
}

// piModelsPath resolves the location of pi's model catalog.
func piModelsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".pi", "agent", "models.json"), nil
}

const defaultPiOllamaBaseURL = "http://localhost:11434/v1"

// defaultPiOllamaAPIKey is pi's placeholder apiKey for keyless local Ollama.
// pi's models.json schema requires a non-empty apiKey; an empty value makes
// pi reject the whole catalog.
const defaultPiOllamaAPIKey = "ollama"

const (
	// piOllamaProviderID is pi's direct local-Ollama provider. Direct-mode
	// entries are keyed by bare model name (config.Model.ModelName).
	piOllamaProviderID = "ollama"

	// piLitellmProviderID hosts gateway models keyed by full registry id
	// (config.Model.ID, e.g. "ollama/glm-5.3-flash:cloud"). They must not live
	// under the "ollama" provider: pi resolves a --model value by splitting on
	// the FIRST slash and matching the remainder against the named provider's
	// entries, then sends the matched entry's id verbatim as the API model
	// name. Under "ollama", a registry id would be swallowed as provider
	// segment + bare pattern, the bare entry would win, and LiteLLM would
	// receive the unprefixed name. A provider whose id cannot appear as the
	// first path segment of a registry model id keeps ids verbatim.
	piLitellmProviderID = "litellm"
)

// isLocalOllamaBaseURL reports whether baseURL points at the local Ollama
// OpenAI-compatible endpoint (localhost or 127.0.0.1 on port 11434), in any
// form pi or wt may have written. Used by the direct-mode reset to normalize
// local-ollama values without touching a user's custom remote provider.
func isLocalOllamaBaseURL(baseURL string) bool {
	return baseURL == defaultPiOllamaBaseURL || baseURL == "http://127.0.0.1:11434/v1"
}

// isDefaultOllamaAPIKey reports whether apiKey is the default local-ollama key
// (empty or "ollama"). A non-default key is treated as user config and
// preserved by the direct-mode reset. The revert writes "ollama" — pi's
// documented keyless-local placeholder — because an empty apiKey fails pi's
// models.json schema validation (min length 1) and would invalidate the
// entire catalog.
func isDefaultOllamaAPIKey(apiKey string) bool {
	return apiKey == "" || apiKey == "ollama"
}

// syncModels updates the wt-owned parts of pi's models.json. It is idempotent
// and leaves every non-wt provider and model entry untouched.
//
// Direct mode adds any non-native cfg models missing from the "ollama"
// provider (keyed by bare ModelName, marked _launch: true) and resets the
// ollama provider to the standard local endpoint when its values are either
// gateway values wt itself wrote (they match the configured gateway) or
// local-ollama values in a non-canonical form. A user's own custom provider
// config (e.g. a remote ollama server) is preserved verbatim. (Limitation: if
// the whole [gateway] section was deleted rather than flipped to
// mode="direct", the URL is gone and the revert cannot match; the user must
// edit models.json by hand in that case.)
//
// Litellm mode creates/updates the dedicated "litellm" provider with
// registry-id entries pointing at the LiteLLM gateway, restores a
// previously-gateway-redirected ollama provider to the local endpoint (its
// bare entries would 400 through LiteLLM), and prunes the "ollama/…" entries
// an older litellm-mode sync left under the ollama provider, which pi's
// --model grammar can never launch.
func syncModels(cfg *config.Config, path string) error {
	var f piModelsFile
	data, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
		// No catalog yet. In gateway mode, create one so pi routes through
		// LiteLLM on first launch; in direct mode there is nothing to sync.
		if !cfg.Gateway.IsLitellm() {
			return nil
		}
	case err != nil:
		return err
	default:
		if err := json.Unmarshal(data, &f); err != nil {
			return err
		}
	}
	if f.Providers == nil {
		f.Providers = make(map[string]piProvider, 2)
	}

	mutated := false
	if cfg.Gateway.IsLitellm() {
		mutated = syncLitellmProvider(cfg, f) || mutated
		mutated = revertOllamaProvider(cfg, f) || mutated
	} else {
		mutated = syncDirectOllama(cfg, f) || mutated
	}
	if !mutated {
		return nil
	}

	out, err := json.MarshalIndent(&f, "", "  ")
	if err != nil {
		return err
	}
	return config.WriteFileAtomic(path, out, 0o644)
}

// syncLitellmProvider ensures provider "litellm" exists with the configured
// gateway endpoint and one _launch entry per non-native cfg model, keyed by
// full registry id. Existing entries are never removed or reordered.
func syncLitellmProvider(cfg *config.Config, f piModelsFile) bool {
	p := f.Providers[piLitellmProviderID]
	mutated := false
	if p.BaseURL != cfg.Gateway.BaseURL()+"/v1" || p.APIKey != cfg.Gateway.APIKey {
		p.BaseURL = cfg.Gateway.BaseURL() + "/v1"
		p.APIKey = cfg.Gateway.APIKey
		if p.API == "" {
			p.API = "openai-completions"
		}
		mutated = true
	}
	existing := make(map[string]bool, len(p.Models))
	for _, m := range p.Models {
		existing[m.ID] = true
	}
	for _, m := range cfg.Models {
		if m.Native || m.ModelName == "" {
			continue
		}
		if existing[m.ID] {
			continue
		}
		p.Models = append(p.Models, piModel{
			Launch:        true,
			ContextWindow: 262144,
			ID:            m.ID,
			Input:         []string{"text", "image"},
			Reasoning:     true,
		})
		existing[m.ID] = true
		mutated = true
	}
	if mutated {
		f.Providers[piLitellmProviderID] = p
	}
	return mutated
}

// revertOllamaProvider undoes the gateway redirect an older litellm-mode sync
// wrote into pi's ollama provider (restoring the standard local endpoint so
// its bare-named entries work directly again) and prunes launcher-generated
// registry-id entries that pi's --model grammar can never resolve properly.
// Values that do not match the configured gateway or the local endpoint are
// user config and preserved. Returns whether anything changed.
func revertOllamaProvider(cfg *config.Config, f piModelsFile) bool {
	p, ok := f.Providers[piOllamaProviderID]
	if !ok {
		return false
	}
	mutated := false
	if isLocalOllamaBaseURL(p.BaseURL) || p.BaseURL == cfg.Gateway.BaseURL()+"/v1" {
		p.BaseURL = defaultPiOllamaBaseURL
		// The apiKey was local-ollama or gateway-set; restore pi's placeholder.
		if isDefaultOllamaAPIKey(p.APIKey) || (cfg.Gateway.APIKey != "" && p.APIKey == cfg.Gateway.APIKey) {
			if p.APIKey != defaultPiOllamaAPIKey {
				p.APIKey = defaultPiOllamaAPIKey
				mutated = true
			}
		}
		mutated = true
	}
	// Prune wt-generated litellm artifacts: entries whose id is the full
	// registry id of a non-native cfg model. Only in gateway mode, where the
	// replacement entries exist; a bare ModelName that coincides with any
	// such id is never equal to its own registry key, so direct-mode-managed
	// entries are never pruned.
	kept := p.Models[:0]
	for _, m := range p.Models {
		prune := false
		for _, cm := range cfg.Models {
			if !cm.Native && cm.ModelName != "" && m.ID == cm.ID && m.ID != cm.ModelName {
				prune = true
				break
			}
		}
		if prune {
			mutated = true
			continue
		}
		kept = append(kept, m)
	}
	if mutated {
		p.Models = kept
		f.Providers[piOllamaProviderID] = p
	}
	return mutated
}

// syncDirectOllama adds missing non-native cfg models to provider "ollama"
// keyed by bare ModelName and reverts gateway-set provider values, mirroring
// the pre-gateway behavior. Returns whether anything changed.
func syncDirectOllama(cfg *config.Config, f piModelsFile) bool {
	p := f.Providers[piOllamaProviderID]
	mutated := false

	existing := make(map[string]bool, len(p.Models))
	for _, m := range p.Models {
		existing[m.ID] = true
	}
	for _, m := range cfg.Models {
		if m.Native || m.ModelName == "" {
			continue
		}
		if existing[m.ModelName] {
			continue
		}
		p.Models = append(p.Models, piModel{
			Launch:        true,
			ContextWindow: 262144,
			ID:            m.ModelName,
			Input:         []string{"text", "image"},
			Reasoning:     true,
		})
		existing[m.ModelName] = true
		mutated = true
	}

	// Reset the provider to the standard local Ollama endpoint when the
	// current values are either gateway values wt itself wrote (they match
	// the configured gateway) or local-ollama values in a non-canonical form
	// (e.g. 127.0.0.1 instead of localhost). A user's own custom provider
	// config (e.g. a remote ollama server) is preserved verbatim.
	if isLocalOllamaBaseURL(p.BaseURL) || p.BaseURL == cfg.Gateway.BaseURL()+"/v1" {
		if p.BaseURL != defaultPiOllamaBaseURL {
			p.BaseURL = defaultPiOllamaBaseURL
			mutated = true
		}
		// The baseUrl was local-ollama or gateway-set, so the apiKey was too.
		if isDefaultOllamaAPIKey(p.APIKey) || (cfg.Gateway.APIKey != "" && p.APIKey == cfg.Gateway.APIKey) {
			if p.APIKey != defaultPiOllamaAPIKey {
				p.APIKey = defaultPiOllamaAPIKey
				mutated = true
			}
		}
	}
	if mutated {
		f.Providers[piOllamaProviderID] = p
	}
	return mutated
}

// isLaunchable reports whether id is present under providerID in pi's
// models.json and marked _launch: true. A missing or unparseable file is
// treated as "not launchable" (the caller falls back to pi's default model).
func isLaunchable(providerID, id, path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var f piModelsFile
	if err := json.Unmarshal(data, &f); err != nil {
		return false
	}
	for _, m := range f.Providers[providerID].Models {
		if m.ID == id && m.Launch {
			return true
		}
	}
	return false
}