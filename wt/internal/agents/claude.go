package agents

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/session"
)

func init() {
	register("claude", func() Driver { return claudeDriver{} })
}

type claudeDriver struct{}

func (claudeDriver) YoloFlag() string { return "--dangerously-skip-permissions" }

func (claudeDriver) InstructionPointers() []InstructionPointer {
	return []InstructionPointer{
		{Path: "CLAUDE.md", Content: "@AGENTS.md\n"},
	}
}

func (claudeDriver) OllamaURL() string { return config.OllamaBaseURL }

func (claudeDriver) ResumeFlag() string { return "--resume" }

func (claudeDriver) LatestSession(path string) (*session.Session, error) {
	dir := filepath.Join(os.Getenv("HOME"), ".claude", "projects", session.Slug(path))
	return session.LatestByExt(dir, ".jsonl", func(f os.FileInfo) string {
		return strings.TrimSuffix(f.Name(), ".jsonl")
	})
}

func (claudeDriver) Build(m config.Model, yolo bool, gw Gateway) LaunchCmd {
	args := []string{}
	if yolo {
		args = append(args, claudeDriver{}.YoloFlag())
	}
	lc := LaunchCmd{Bin: "claude", Args: args}

	// Native dispatch. Native-provider models (claude/native,
	// claude/opus, etc.) use the claude subscription: clear any inherited
	// ollama gateway vars so the subscription wins, and pass --model
	// only when a specific claude/* model is named. The sentinel
	// claude/native launches bare. Anything else routes through the
	// ollama anthropic-compatible gateway.
	if m.Native {
		lc.ClearEnv = []string{"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL"}
		if m.ModelName != "native" {
			lc.Args = append(lc.Args, "--model", m.ModelName)
		}
		return lc
	}

	if gw.IsLitellm() {
		lc.Env = append(lc.Env,
			"ANTHROPIC_AUTH_TOKEN="+gw.APIKey,
			"ANTHROPIC_API_KEY=",
			"ANTHROPIC_BASE_URL="+gw.BaseURL(),
		)
		lc.Args = append(lc.Args, "--model", m.ID)
		return lc
	}

	lc.Env = append(lc.Env,
		"ANTHROPIC_AUTH_TOKEN=ollama",
		"ANTHROPIC_API_KEY=",
		"ANTHROPIC_BASE_URL="+claudeDriver{}.OllamaURL(),
	)
	lc.Args = append(lc.Args, "--model", m.ModelName)
	return lc
}
