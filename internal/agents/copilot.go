package agents

import (
	"github.com/ohanaverse/agent-worktree/internal/config"
)

func init() { register("copilot", func() Driver { return copilotDriver{} }) }

type copilotDriver struct{}

func (copilotDriver) YoloFlag() string { return "--yolo" }

func (copilotDriver) Build(m config.Model, yolo bool) LaunchCmd {
	lc := LaunchCmd{Bin: "copilot"}
	if yolo {
		lc.Args = append(lc.Args, copilotDriver{}.YoloFlag())
	}
	// Provider-keyed dispatch: native-provider models (copilot/native and
	// any future copilot/* model that talks to the GitHub subscription)
	// use the copilot subscription and clear any inherited ollama gateway
	// vars so the subscription wins. Anything else routes through the
	// ollama OpenAI-compatible gateway via COPILOT_PROVIDER_* env vars.
	if m.ProviderID == "copilot" {
		lc.ClearEnv = []string{"COPILOT_PROVIDER_BASE_URL", "COPILOT_PROVIDER_API_KEY", "COPILOT_PROVIDER_WIRE_API", "COPILOT_MODEL"}
		return lc
	}
	lc.Env = append(lc.Env,
		"COPILOT_PROVIDER_BASE_URL="+config.OllamaBaseURL+"/v1",
		"COPILOT_PROVIDER_API_KEY=",
		"COPILOT_PROVIDER_WIRE_API=responses",
		"COPILOT_MODEL="+m.ModelName,
	)
	return lc
}
