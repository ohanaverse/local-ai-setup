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
		// Native model — no extra args/env. Clear any inherited gateway
		// vars so the native subscription is used instead of routing to
		// the ollama gateway (which a parent shell may have exported).
		lc.ClearEnv = []string{"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL"}
		return lc
	}

	// Cloud and local models both route through the ollama
	// Anthropic-compatible gateway.
	lc.Env = append(lc.Env,
		"ANTHROPIC_AUTH_TOKEN=ollama",
		"ANTHROPIC_API_KEY=",
		"ANTHROPIC_BASE_URL="+config.OllamaBaseURL,
	)
	lc.Args = append(lc.Args, "--model", m.ModelName)
	return lc
}
