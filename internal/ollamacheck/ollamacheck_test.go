package ollamacheck

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ohanaverse/agent-worktree/internal/config"
)

func TestIsOllamaModel(t *testing.T) {
	if !IsOllamaModel(config.Model{ProviderID: "ollama"}) {
		t.Error("expected true for ollama provider")
	}
	if IsOllamaModel(config.Model{ProviderID: "openrouter"}) {
		t.Error("expected false for openrouter provider")
	}
	if IsOllamaModel(config.Model{ProviderID: "claude"}) {
		t.Error("expected false for claude provider")
	}
}

func TestAvailable(t *testing.T) {
	// Create a fake ollama binary that prints known output.
	tmpDir := t.TempDir()
	fakeOllama := filepath.Join(tmpDir, "ollama")
	script := `#!/bin/sh
echo "NAME              ID    SIZE      MODIFIED"
echo "gemma4:9b         abc   5.0 GB    2 days ago"
echo "deepseek-v4-pro   def   1.2 GB    1 week ago"`
	if err := exec.Command("sh", "-c", "cat > "+fakeOllama+" <<'EOF'\n"+script+"\nEOF\nchmod +x "+fakeOllama).Run(); err != nil {
		t.Fatalf("creating fake ollama: %v", err)
	}

	oldPath := filepath.Dir(fakeOllama)
	// We'll set PATH in a sub-test pattern using t.Setenv below.
	t.Setenv("PATH", oldPath)

	ok, err := Available("gemma4:9b")
	if err != nil {
		t.Fatalf("Available(gemma4:9b): %v", err)
	}
	if !ok {
		t.Error("expected gemma4:9b to be available")
	}

	ok, err = Available("missing-model")
	if err != nil {
		t.Fatalf("Available(missing-model): %v", err)
	}
	if ok {
		t.Error("expected missing-model to be unavailable")
	}
}

func TestAvailableNotInstalled(t *testing.T) {
	// Clear PATH so ollama is not found.
	t.Setenv("PATH", "")
	ok, err := Available("anything")
	if err != nil {
		t.Fatalf("expected no error when ollama not installed, got %v", err)
	}
	if ok {
		t.Error("expected false when ollama not installed")
	}
}

func TestParseOllamaNames(t *testing.T) {
	out := "NAME              ID    SIZE      MODIFIED\n" +
		"gemma4:9b         abc   5.0 GB    2 days ago\n" +
		"kimi-k2.6:cloud   def   -         3 days ago\n"
	names := parseOllamaNames(out)
	if len(names) != 2 {
		t.Fatalf("parseOllamaNames: got %v, want 2 names", names)
	}
	if names[0] != "gemma4:9b" || names[1] != "kimi-k2.6:cloud" {
		t.Errorf("parseOllamaNames: got %v, want [gemma4:9b kimi-k2.6:cloud]", names)
	}
}
