package agents

import (
	"slices"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// codexLitellmProviderArgs builds the exact expected args for a LiteLLM-routed
// codex launch, matching codex.go's litellm branch. Reusing ollamaProvider keeps
// the test in lockstep with the driver and avoids hardcoding the namespace.
func codexLitellmProviderArgs(baseURL, model string) []string {
	return []string{
		"-c", "model_provider=" + ollamaProvider,
		"-c", "model_providers." + ollamaProvider + ".name=\"Ollama\"",
		"-c", "model_providers." + ollamaProvider + ".base_url=\"" + baseURL + "\"",
		"-c", "model_providers." + ollamaProvider + ".wire_api=\"responses\"",
		"-c", "model_providers." + ollamaProvider + ".env_key=\"" + codexGatewayEnvKey + "\"",
		"--model", model,
	}
}

// TestCodexBuildLitellm asserts that in LiteLLM gateway mode codex uses the
// gateway URL as its OpenAI-compatible base URL, passes the registry model id
// rather than the bare model name, and reads the gateway key from an env var:
// codex custom providers take no inline api key field — without env_key the
// proxy rejects every request with 401 "No api key passed in".
func TestCodexBuildLitellm(t *testing.T) {
	m := config.Model{ID: "ollama/qwen3.8:27b-mlx", ModelName: "qwen3.8:27b-mlx", ProviderID: "ollama"}
	gw := config.LitellmState{Enabled: true, URL: "http://localhost:4000", APIKey: "sk-litellm"}
	lc := codexDriver{}.Build(m, false, routeFor(m, gw))
	want := codexLitellmProviderArgs("http://localhost:4000/v1/", "ollama/qwen3.8:27b-mlx")
	if !slices.Equal(lc.Args, want) {
		t.Fatalf("args = %v, want %v", lc.Args, want)
	}
	wantEnv := []string{codexGatewayEnvKey + "=sk-litellm"}
	if !slices.Equal(lc.Env, wantEnv) {
		t.Fatalf("env = %v, want %v (gateway key for env_key lookup)", lc.Env, wantEnv)
	}
}
