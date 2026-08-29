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

// syncModels adds any non-native models from cfg that are missing from pi's
// models.json, marking each _launch: true. It is idempotent (only adds, never
// removes) and preserves existing entries and pi's provider config verbatim.
func syncModels(cfg *config.Config, path string) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil // nothing to sync
	}
	if err != nil {
		return err
	}
	var f piModelsFile
	if err := json.Unmarshal(data, &f); err != nil {
		return err
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
		if existing[m.ModelName] {
			continue
		}
		f.Providers.Ollama.Models = append(f.Providers.Ollama.Models, piModel{
			Launch:        true,
			ContextWindow: 262144,
			ID:            m.ModelName,
			Input:         []string{"text", "image"},
			Reasoning:     true,
		})
		existing[m.ModelName] = true
		added++
	}

	if added == 0 {
		return nil
	}

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
