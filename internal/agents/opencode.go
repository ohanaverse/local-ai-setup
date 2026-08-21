package agents

import (
	"fmt"

	"github.com/ohanaverse/agent-worktree/internal/config"
)

func init() { register("opencode", func() Driver { return opencodeDriver{} }) }

type opencodeDriver struct{}

func (opencodeDriver) YoloFlag() string { return "--dangerously-skip-permissions" }

// OpenCode is ollama-only after the native-provider alignment. The
// OPENCODE_CONFIG_CONTENT env var routes through the ollama gateway with
// the provider/model form ("ollama/<ModelName>"). The bare provider-side
// name comes from m.ModelName, not m.ID — m.ID already carries the
// "ollama/" registry prefix, so using it would produce
// "ollama/ollama/<model>" (a double prefix that the gateway rejects).
func (opencodeDriver) Build(m config.Model, yolo bool) LaunchCmd {
	lc := LaunchCmd{Bin: "opencode"}
	if yolo {
		lc.Args = append(lc.Args, opencodeDriver{}.YoloFlag())
	}
	lc.Env = append(lc.Env,
		"OPENCODE_CONFIG_CONTENT="+fmt.Sprintf(
			`{"model":"ollama/%s","provider":{"ollama":{"options":{"baseURL":"%s/v1","apiKey":""}}}}`,
			m.ModelName, config.OllamaBaseURL,
		),
	)
	return lc
}
