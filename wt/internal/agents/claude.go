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

func (claudeDriver) Protocols() []Protocol { return []Protocol{config.ProtocolAnthropic} }

func (claudeDriver) InstructionPointers() []InstructionPointer {
	return []InstructionPointer{
		{Path: "CLAUDE.md", Content: "@AGENTS.md\n"},
	}
}

func (claudeDriver) ResumeFlag() string { return "--resume" }

func (claudeDriver) LatestSession(path string) (*session.Session, error) {
	dir := filepath.Join(os.Getenv("HOME"), ".claude", "projects", session.Slug(path))
	return session.LatestByExt(dir, ".jsonl", func(f os.FileInfo) string {
		return strings.TrimSuffix(f.Name(), ".jsonl")
	})
}

func (claudeDriver) Build(m config.Model, yolo bool, r Route) LaunchCmd {
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

	// The Anthropic client needs a non-empty auth token even when the
	// endpoint doesn't validate it (e.g. ollama's direct gateway, where
	// r.APIKey resolves to "" for an auth.type=none provider); fall back
	// to the "ollama" placeholder only when ResolveRoute gave us no real
	// key, so a direct-mode provider with a configured secret_ref (not
	// just ollama) actually authenticates with it.
	token := r.APIKey
	if token == "" {
		token = "ollama"
	}
	lc.Env = append(lc.Env,
		"ANTHROPIC_AUTH_TOKEN="+token,
		"ANTHROPIC_API_KEY=",
		"ANTHROPIC_BASE_URL="+r.BaseOrigin,
	)
	lc.Args = append(lc.Args, "--model", r.ModelRef)
	return lc
}
