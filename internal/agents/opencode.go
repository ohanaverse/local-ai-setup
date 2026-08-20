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
	if m.IsNative() {
		// Native model — clear any inherited gateway config so the native
		// subscription is used instead of routing to the ollama gateway.
		lc.ClearEnv = []string{"OPENCODE_CONFIG_CONTENT"}
		return lc
	}
	// Pass the ollama provider config inline — OpenCode's highest
	// precedence config layer. OpenCode's CLI requires the
	// provider/model form, so we construct "ollama/<bare>" from
	// m.ModelName (the bare provider-specific name). Using m.ID
	// would produce "ollama/ollama/<bare>" because the registry
	// already prefixes IDs with the provider id.
	lc.Env = append(lc.Env,
		"OPENCODE_CONFIG_CONTENT="+fmt.Sprintf(
			`{"model":"ollama/%s","provider":{"ollama":{"options":{"baseURL":"%s/v1","apiKey":""}}}}`,
			m.ModelName, config.OllamaBaseURL,
		),
	)
	return lc
}
