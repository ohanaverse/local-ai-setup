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
	if !m.IsNative() {
		lc.Env = append(lc.Env,
			"COPILOT_PROVIDER_BASE_URL="+config.OllamaBaseURL,
			"COPILOT_PROVIDER_API_KEY=",
			"COPILOT_MODEL="+m.ID,
		)
	}
	return lc
}
