package agents

import (
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

func init() { register("copilot", func() Driver { return copilotDriver{} }) }

type copilotDriver struct{}

func (copilotDriver) YoloFlag() string { return "--yolo" }

func (copilotDriver) InstructionPointers() []InstructionPointer {
	return []InstructionPointer{
		{Path: ".github/copilot-instructions.md", Content: "Read AGENTS.md and follow all instructions in it.\n"},
	}
}

func (copilotDriver) OllamaURL() string { return config.OllamaBaseURL + "/v1" }

func (copilotDriver) Build(m config.Model, yolo bool, gw Gateway) LaunchCmd {
	lc := LaunchCmd{Bin: "copilot"}
	if yolo {
		lc.Args = append(lc.Args, copilotDriver{}.YoloFlag())
	}
	// Native dispatch: native-provider models (copilot/native and
	// any future copilot/* model that talks to the GitHub subscription)
	// use the copilot subscription and clear any inherited ollama gateway
	// vars so the subscription wins. Anything else routes through the
	// ollama OpenAI-compatible gateway via COPILOT_PROVIDER_* env vars.
	if m.Native {
		lc.ClearEnv = []string{"COPILOT_PROVIDER_BASE_URL", "COPILOT_PROVIDER_API_KEY", "COPILOT_PROVIDER_WIRE_API", "COPILOT_MODEL"}
		return lc
	}

	baseURL := copilotDriver{}.OllamaURL()
	modelName := m.ModelName
	apiKey := ""
	if gw.IsLitellm() {
		baseURL = gw.BaseURL() + "/v1"
		modelName = m.ID
		apiKey = gw.APIKey
	}

	// Use the chat-completions wire API for Ollama/LiteLLM backends.
	// Copilot CLI's "responses" wire drops leading characters through the
	// OpenAI-compatible bridge — observed in litellm mode (LiteLLM's
	// /v1/responses→ollama_chat bridge, with glm-5.3-flash:cloud and others),
	// making one-shot prompts unreliable; codex speaks responses through the
	// same bridge without truncation, so the trigger is the copilot-client×
	// bridge interaction, not localized to either side. "completions" is
	// fully supported by both Ollama and LiteLLM. Verified live in both
	// direct (ollama /v1) and litellm modes 2026-09-01 via agents-smoke.sh.
	// This deliberately diverges from `ollama launch copilot`, which still
	// sets responses — re-test when LiteLLM's responses bridge is fixed
	// (BerriAI/litellm#37452) or copilot CLI's responses client changes.
	lc.Env = append(lc.Env,
		"COPILOT_PROVIDER_BASE_URL="+baseURL,
		"COPILOT_PROVIDER_API_KEY="+apiKey,
		"COPILOT_PROVIDER_WIRE_API=completions",
		"COPILOT_MODEL="+modelName,
	)
	return lc
}
