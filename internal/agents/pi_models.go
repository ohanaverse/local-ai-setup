package agents

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/ohanaverse/agent-worktree/internal/config"
)

// piModelsFile mirrors the on-disk shape of ~/.pi/agent/models.json. Only the
// ollama provider is relevant to wt; its api/apiKey/baseUrl fields are
// preserved verbatim so pi's own configuration is never clobbered by a sync.
type piModelsFile struct {
	Providers struct {
		Ollama struct {
			API     string    `json:"api"`
			APIKey  string    `json:"apiKey"`
			BaseURL string    `json:"baseUrl"`
			Models  []piModel `json:"models"`
		} `json:"ollama"`
	} `json:"providers"`
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

// isLocalOllamaBaseURL reports whether baseURL points at the local Ollama
// OpenAI-compatible endpoint (localhost or 127.0.0.1 on port 11434), in any
// form pi or wt may have written. Used by the direct-mode reset to normalize
// local-ollama values without touching a user's custom remote provider.
func isLocalOllamaBaseURL(baseURL string) bool {
	return baseURL == defaultPiOllamaBaseURL || baseURL == "http://127.0.0.1:11434/v1"
}

// isDefaultOllamaAPIKey reports whether apiKey is the default local-ollama key
// (empty or "ollama"). A non-default key is treated as user config and
// preserved by the direct-mode reset.
func isDefaultOllamaAPIKey(apiKey string) bool {
	return apiKey == "" || apiKey == "ollama"
}

// syncModels adds any non-native models from cfg that are missing from pi's
// models.json, marking each _launch: true. It is idempotent (only adds, never
// removes) and preserves existing entries. In gateway mode it points the ollama
// provider at the LiteLLM gateway; in direct mode it undoes gateway settings
// (only values that match the configured gateway, so a user's own custom
// provider config is preserved verbatim) so gateway settings are not sticky.
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

	existing := make(map[string]bool, len(f.Providers.Ollama.Models))
	for _, m := range f.Providers.Ollama.Models {
		existing[m.ID] = true
	}

	added := 0
	for _, m := range cfg.Models {
		if m.Native || m.ModelName == "" {
			continue
		}
		id := m.ModelName
		if cfg.Gateway.IsLitellm() {
			id = m.ID
		}
		if existing[id] {
			continue
		}
		f.Providers.Ollama.Models = append(f.Providers.Ollama.Models, piModel{
			Launch:        true,
			ContextWindow: 262144,
			ID:            id,
			Input:         []string{"text", "image"},
			Reasoning:     true,
		})
		existing[id] = true
		added++
	}

	// Compute the target provider config. In gateway mode the ollama provider
	// points at LiteLLM. In direct mode, the provider is reset to the standard
	// local Ollama endpoint when the current values are either gateway values
	// wt itself wrote (they match the configured gateway) or local-ollama
	// values in a non-canonical form (e.g. 127.0.0.1 instead of localhost). A
	// user's own custom provider config (e.g. a remote ollama server) is
	// preserved verbatim. (Limitation: if the whole [gateway] section was
	// deleted rather than flipped to mode="direct", the URL is gone and the
	// revert cannot match; the user must edit models.json by hand in that case.)
	targetBaseURL := f.Providers.Ollama.BaseURL
	targetAPIKey := f.Providers.Ollama.APIKey
	if cfg.Gateway.IsLitellm() {
		targetBaseURL = cfg.Gateway.BaseURL() + "/v1"
		targetAPIKey = cfg.Gateway.APIKey
	} else if isLocalOllamaBaseURL(f.Providers.Ollama.BaseURL) || f.Providers.Ollama.BaseURL == cfg.Gateway.BaseURL()+"/v1" {
		targetBaseURL = defaultPiOllamaBaseURL
		// The baseUrl was local-ollama or gateway-set, so the apiKey was too.
		if isDefaultOllamaAPIKey(f.Providers.Ollama.APIKey) || (cfg.Gateway.APIKey != "" && f.Providers.Ollama.APIKey == cfg.Gateway.APIKey) {
			targetAPIKey = ""
		}
	}

	// Skip the write only when nothing changed: no new models and the provider
	// config already matches the target. Writing unconditionally would churn
	// pi's file on every launch; skipping when the provider config still needs
	// reverting would leave gateway settings sticky.
	if added == 0 && targetBaseURL == f.Providers.Ollama.BaseURL && targetAPIKey == f.Providers.Ollama.APIKey {
		return nil
	}

	f.Providers.Ollama.BaseURL = targetBaseURL
	f.Providers.Ollama.APIKey = targetAPIKey

	out, err := json.MarshalIndent(&f, "", "  ")
	if err != nil {
		return err
	}
	return config.WriteFileAtomic(path, out, 0o644)
}

// isLaunchable reports whether name is present in pi's models.json and marked
// _launch: true. A missing or unparseable file is treated as "not launchable"
// (the caller falls back to pi's default model).
func isLaunchable(name, path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var f piModelsFile
	if err := json.Unmarshal(data, &f); err != nil {
		return false
	}
	for _, m := range f.Providers.Ollama.Models {
		if m.ID == name && m.Launch {
			return true
		}
	}
	return false
}
