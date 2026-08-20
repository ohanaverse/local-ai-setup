// Package ollamacheck verifies whether an Ollama model is locally available.
package ollamacheck

import (
	"github.com/ohanaverse/agent-worktree/internal/config"
	"github.com/ohanaverse/agent-worktree/internal/registry"
)

// IsOllamaModel returns true if the model is from the ollama provider.
func IsOllamaModel(m config.Model) bool {
	return m.ProviderID == "ollama"
}

// Check reports whether m is locally available. Non-ollama models are always
// available (true, nil); ollama models are checked against `ollama list`.
func Check(m config.Model) (bool, error) {
	if !IsOllamaModel(m) {
		return true, nil
	}
	return Available(m.ModelName)
}

// Available checks whether modelName appears in `ollama list` output.
// Returns false with a nil error when ollama is not installed.
// Returns an error when `ollama list` exits non-zero.
func Available(modelName string) (bool, error) {
	models, err := registry.Ollama{}.Discover()
	if err != nil {
		return false, err
	}
	for _, m := range models {
		if m.ModelName == modelName {
			return true, nil
		}
	}
	return false, nil
}
