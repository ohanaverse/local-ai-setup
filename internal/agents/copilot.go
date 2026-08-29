package agents

import (
	"github.com/ohanaverse/agent-worktree/internal/config"
)

func init() { register("copilot", func() Driver { return copilotDriver{} }) }

type copilotDriver struct{}

func (copilotDriver) YoloFlag() string { return "--yolo" }

func (copilotDriver) InstructionPointers() []InstructionPointer {
	return []InstructionPointer{
		{Path: ".github/copilot-instructions.md", Content: "Read AGENTS.md and follow all instructions in it.\n"},
	}
}

func (copilotDriver) OllamaURL() string { return "http://localhost:11434/v1" }

func (copilotDriver) Build(m config.Model, yolo bool) LaunchCmd {
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
	lc.Env = append(lc.Env,
		"COPILOT_PROVIDER_BASE_URL="+copilotDriver{}.OllamaURL(),
		"COPILOT_PROVIDER_API_KEY=",
		"COPILOT_PROVIDER_WIRE_API=responses",
		"COPILOT_MODEL="+m.ModelName,
	)
	return lc
}
