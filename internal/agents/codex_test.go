package agents

import (
	"testing"
)

// TestCodexOllamaURL asserts codexDriver returns the /v1/ endpoint used by
// the inline model provider override.
func TestCodexOllamaURL(t *testing.T) {
	var d Driver = codexDriver{}
	u, ok := d.(OllamaURLer)
	if !ok {
		t.Fatal("codexDriver does not implement OllamaURLer")
	}
	if got := u.OllamaURL(); got != "http://localhost:11434/v1/" {
		t.Errorf("OllamaURL() = %q, want http://localhost:11434/v1/", got)
	}
}
