package agents

import (
	"github.com/ohanaverse/agent-worktree/internal/config"
)

func init() { register("codex", func() Driver { return codexDriver{} }) }

type codexDriver struct{}

func (codexDriver) YoloFlag() string { return "--dangerously-bypass-approvals-and-sandbox" }

// ollamaProvider is the model provider codex-wt declares with inline -c
// overrides so a non-native launch routes through the local Ollama gateway
// instead of falling back to the "openai" provider, whose lack of credentials
// prompts codex to sign in. It is namespaced "agent-wt" rather than
// "ollama-launch" so a user-authored [model_providers.ollama-launch] in their
// main ~/.codex/config.toml cannot leak a stray field into the set we fully
// control. The base_url uses the OpenAI-compatible /v1/ path that the Ollama
// gateway exposes (claude/copilot use the bare gateway URL for the
// anthropic-compatible endpoint instead).
const ollamaProvider = "agent-wt"

func (codexDriver) OllamaURL() string { return "http://localhost:11434/v1/" }

func (codexDriver) Build(m config.Model, yolo bool) LaunchCmd {
	lc := LaunchCmd{Bin: "codex"}
	if yolo {
		lc.Args = append(lc.Args, codexDriver{}.YoloFlag())
	}
	// Native models use the user's own codex subscription: no provider override,
	// so codex uses its default model (and may prompt to sign in, which is the
	// expected behavior for an explicit native choice). Ollama-routed models get
	// the Ollama endpoint declared inline so codex never falls back to "openai"
	// auth and prompts to sign in.
	if !m.Native {
		lc.Args = append(lc.Args,
			"-c", "model_provider="+ollamaProvider,
			"-c", "model_providers."+ollamaProvider+".name=\"Ollama\"",
			"-c", "model_providers."+ollamaProvider+".base_url=\""+codexDriver{}.OllamaURL()+"\"",
			"-c", "model_providers."+ollamaProvider+".wire_api=\"responses\"",
			"--model", m.ModelName,
		)
	}
	return lc
}
