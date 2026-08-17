package agents

import (
	"github.com/ohanaverse/agent-worktree/internal/config"
)

func init() {
	register("claude", func() Driver { return claudeDriver{} })
}

type claudeDriver struct{}

func (claudeDriver) YoloFlag() string { return "--dangerously-skip-permissions" }

func (claudeDriver) Build(m config.Model, yolo bool) LaunchCmd {
	args := []string{}
	if yolo {
		args = append(args, claudeDriver{}.YoloFlag())
	}
	lc := LaunchCmd{Bin: "claude", Args: args}

	if m.IsNative() {
		// native model — no extra args/env
		return lc
	}

	// Cloud and local models both route through the ollama
	// Anthropic-compatible gateway.
	lc.Env = append(lc.Env,
		"ANTHROPIC_AUTH_TOKEN=ollama",
		"ANTHROPIC_API_KEY=",
		"ANTHROPIC_BASE_URL="+config.OllamaBaseURL,
	)
	lc.Args = append(lc.Args, "--model", m.ID)
	return lc
}
