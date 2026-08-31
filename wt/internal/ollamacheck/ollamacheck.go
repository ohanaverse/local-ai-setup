// Package ollamacheck verifies whether an Ollama model is locally available.
package ollamacheck

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
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
//
// Deliberately self-contained (no internal/registry import): this is a
// runtime availability probe, not model discovery — modelman owns discovery.
func Available(modelName string) (bool, error) {
	if _, err := exec.LookPath("ollama"); err != nil {
		return false, nil // ollama not installed — nothing is available
	}
	out, err := exec.Command("ollama", "list").Output()
	if err != nil {
		return false, fmt.Errorf("ollama list: %w", err)
	}
	for _, name := range parseOllamaNames(string(out)) {
		if name == modelName {
			return true, nil
		}
	}
	return false, nil
}

// parseOllamaNames extracts the NAME column from `ollama list` output.
// Mirrors the row shape the old registry parser accepted: header line
// skipped, rows with fewer than 3 fields skipped, cloud rows (SIZE "-")
// included since a cloud model is "available" to ollama.
func parseOllamaNames(output string) []string {
	var names []string
	for i, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if i == 0 {
			continue // header row: NAME  ID  SIZE  MODIFIED
		}
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[0] == "" {
			continue
		}
		names = append(names, fields[0])
	}
	return names
}
