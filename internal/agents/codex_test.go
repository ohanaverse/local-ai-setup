package agents

import (
	"slices"
	"testing"

	"github.com/ohanaverse/agent-worktree/internal/config"
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

// codexLitellmProviderArgs builds the exact expected args for a LiteLLM-routed
// codex launch, matching codex.go's litellm branch. Reusing ollamaProvider keeps
// the test in lockstep with the driver and avoids hardcoding the namespace.
func codexLitellmProviderArgs(baseURL, model string) []string {
	return []string{
		"-c", "model_provider=" + ollamaProvider,
		"-c", "model_providers." + ollamaProvider + ".name=\"Ollama\"",
		"-c", "model_providers." + ollamaProvider + ".base_url=\"" + baseURL + "\"",
		"-c", "model_providers." + ollamaProvider + ".wire_api=\"responses\"",
		"--model", model,
	}
}

// TestCodexBuildLitellm asserts that in LiteLLM gateway mode codex uses the
// gateway URL as its OpenAI-compatible base URL and passes the registry model id
// rather than the bare model name.
func TestCodexBuildLitellm(t *testing.T) {
	m := config.Model{ID: "ollama/qwen3.8:27b-mlx", ModelName: "qwen3.8:27b-mlx", ProviderID: "ollama"}
	gw := Gateway{Mode: "litellm", URL: "http://localhost:4000", APIKey: "sk-litellm"}
	lc := codexDriver{}.Build(m, false, gw)
	want := codexLitellmProviderArgs("http://localhost:4000/v1/", "ollama/qwen3.8:27b-mlx")
	if !slices.Equal(lc.Args, want) {
		t.Fatalf("args = %v, want %v", lc.Args, want)
	}
}
