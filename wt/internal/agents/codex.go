package agents

import (
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

func init() { register("codex", func() Driver { return codexDriver{} }) }

type codexDriver struct{}

func (codexDriver) YoloFlag() string { return "--dangerously-bypass-approvals-and-sandbox" }

// ollamaProvider is the model provider codex-wt declares with inline -c
// overrides so a non-native launch routes through the local Ollama gateway
// instead of falling back to the "openai" provider, whose lack of credentials
// prompts codex to sign in. It is namespaced "agent-wt" rather than
// "ollama-launch" so a user-authored [model_providers.ollama-launch] in their
// main ~/.codex/config.toml cannot leak a stray field into the set we fully
// control. The base_url uses the OpenAI-compatible /v1/ path that the Ollama
// gateway exposes (claude/copilot use the bare gateway URL for the
// anthropic-compatible endpoint instead).
const ollamaProvider = "agent-wt"

// codexGatewayEnvKey names the env var carrying the LiteLLM gateway key in
// gateway mode. codex custom providers take no inline API key — they read one
// from the env var named by model_providers.<id>.env_key; without it the
// proxy rejects every request with 401 "No api key passed in".
const codexGatewayEnvKey = "AGENT_WT_GATEWAY_API_KEY"

func (codexDriver) OllamaURL() string { return config.OllamaBaseURL + "/v1/" }

func (codexDriver) Build(m config.Model, yolo bool, gw Gateway) LaunchCmd {
	lc := LaunchCmd{Bin: "codex"}
	if yolo {
		lc.Args = append(lc.Args, codexDriver{}.YoloFlag())
	}
	// Native models use the user's own codex subscription: no provider override,
	// so codex uses its default model (and may prompt to sign in, which is the
	// expected behavior for an explicit native choice). Ollama-routed models get
	// the Ollama endpoint declared inline so codex never falls back to "openai"
	// auth and prompts to sign in.
	if m.Native {
		return lc
	}

	baseURL := codexDriver{}.OllamaURL()
	modelName := m.ModelName
	configArgs := []string{}
	if gw.IsLitellm() {
		baseURL = gw.BaseURL() + "/v1/"
		modelName = m.ID
		// The local ollama endpoint needs no key; LiteLLM's v1 API does.
		lc.Env = append(lc.Env, codexGatewayEnvKey+"="+gw.APIKey)
		configArgs = append(configArgs,
			"-c", "model_providers."+ollamaProvider+".env_key=\""+codexGatewayEnvKey+"\"",
		)
	}

	lc.Args = append(lc.Args,
		"-c", "model_provider="+ollamaProvider,
		"-c", "model_providers."+ollamaProvider+".name=\"Ollama\"",
		"-c", "model_providers."+ollamaProvider+".base_url=\""+baseURL+"\"",
		"-c", "model_providers."+ollamaProvider+".wire_api=\"responses\"",
	)
	lc.Args = append(lc.Args, configArgs...)
	lc.Args = append(lc.Args,
		"--model", modelName,
	)
	return lc
}
