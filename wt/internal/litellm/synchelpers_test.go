package litellm

import "github.com/ohanaverse/local-ai-setup/wt/internal/config"

// Test helpers copied from cmd/wt-session's litellm_test.go for the sync
// input tests that moved here with the helpers they test.

func litellmTestConfig() *config.Config {
	return &config.Config{
		Providers: []config.Provider{
			{ID: "ollama", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:11434"}},
			{ID: "claude", Auth: config.AuthConfig{Type: "native"}},
		},
		Models: []config.Model{
			{ID: "ollama/gemma:9b", ProviderID: "ollama", ModelName: "gemma:9b", Location: config.LocationLocal},
			{ID: "claude/sonnet", ProviderID: "claude", ModelName: "sonnet", Native: true},
		},
	}
}

// downProbeWarn is the warning a provider whose server refused the probe
// connection produces in both sync modes, spelled out once so the dry-run and
// real-run tests compare the same string.
const downProbeWarn = `provider "ollama" refused the probe connection: nothing is listening there, so its local routes are treated as stale`
