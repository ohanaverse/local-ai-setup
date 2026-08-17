// Package ollamacheck verifies whether an Ollama model is locally available.
package ollamacheck

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/ohanaverse/agent-worktree/internal/config"
)

// IsOllamaModel returns true if the model is from the ollama provider.
func IsOllamaModel(m config.Model) bool {
	return m.ProviderID == "ollama"
}

// Available checks whether modelName appears in `ollama list` output.
// Returns false with a nil error when ollama is not installed.
// Returns an error when `ollama list` exits non-zero.
func Available(modelName string) (bool, error) {
	if _, err := exec.LookPath("ollama"); err != nil {
		return false, nil // ollama not installed
	}
	out, err := exec.Command("ollama", "list").Output()
	if err != nil {
		return false, fmt.Errorf("ollama list: %w", err)
	}
	return parseOllamaList(string(out), modelName), nil
}

// parseOllamaList checks whether target appears in the output of `ollama list`.
func parseOllamaList(output, target string) bool {
	for i, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if i == 0 {
			continue // skip header
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if fields[0] == target {
			return true
		}
	}
	return false
}
