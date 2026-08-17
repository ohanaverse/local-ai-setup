package agents

import (
	"fmt"

	"github.com/ohanaverse/agent-worktree/internal/config"
)

func init() { register("opencode", func() Driver { return opencodeDriver{} }) }

type opencodeDriver struct{}

func (opencodeDriver) YoloFlag() string { return "--dangerously-skip-permissions" }

func (opencodeDriver) Build(m config.Model, yolo bool) LaunchCmd {
	lc := LaunchCmd{Bin: "opencode"}
	if yolo {
		lc.Args = append(lc.Args, opencodeDriver{}.YoloFlag())
	}
	if !m.IsNative() {
		// Pass the ollama provider config inline — OpenCode's highest
		// precedence config layer.
		lc.Env = append(lc.Env,
			"OPENCODE_CONFIG_CONTENT="+fmt.Sprintf(
				`{"model":"ollama/%s","provider":{"ollama":{"options":{"baseURL":"%s/v1","apiKey":""}}}}`,
				m.ID, config.OllamaBaseURL,
			),
		)
	}
	return lc
}
