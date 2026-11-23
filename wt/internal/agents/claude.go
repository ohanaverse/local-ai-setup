package agents

import (
	"os"
	"path/filepath"
	"regexp"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
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

// StateDir is claude's per-project directory (transcripts and memory).
func (claudeDriver) StateDir(path string) string {
	return filepath.Join(os.Getenv("HOME"), ".claude", "projects", claudeProjectSlug(path))
}

// claudeNonSlug matches every character claude replaces when it names a
// project directory after a working directory.
var claudeNonSlug = regexp.MustCompile(`[^a-zA-Z0-9-]`)

// claudeProjectSlug converts an absolute path to claude's project directory
// name: every character outside [a-zA-Z0-9-] becomes '-'.
func claudeProjectSlug(path string) string {
	return claudeNonSlug.ReplaceAllString(path, "-")
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
	// ollama anthropic-compatible gateway. An inherited x-wt-model-token
	// line of ANTHROPIC_CUSTOM_HEADERS (a header-mode session's shell) is
	// stripped by the env assembler (launchEnv), so the wt token never
	// reaches api.anthropic.com from here.
	if m.Native {
		if !isNativeLaunch(m) {
			lc.ClearEnv = []string{"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL"}
		}
		if m.ModelName != "native" {
			lc.Args = append(lc.Args, "--model", m.ModelName)
		}
		return lc
	}

	// Every model slot is mapped to the same reference --model carries, so a
	// request Claude Code makes on a tier of its own choosing reaches this
	// model instead of an Anthropic id the endpoint rejects. The inherited
	// values are cleared so the command holds one entry per variable: an
	// applied automatic profile appends its env after this (see
	// profiles.ApplyEnvAndArgs), and the last entry wins, so a profile that
	// sets one of these still overrides wt's value.
	lc.ClearEnv = claudeSlotKeys()
	if r.Subscription {
		// Header mode (#597): no ANTHROPIC_AUTH_TOKEN, so Claude Code keeps
		// its own claude.ai login; an empty ANTHROPIC_API_KEY so a stray key
		// never beats it; an inherited key, auth or OAuth token is cleared so
		// the login used is the stored one that was checked, and every
		// CLAUDE_CODE_USE_* so the session cannot go to Bedrock or Vertex
		// (with the token header) instead of the gateway. The gateway token
		// goes back as HeaderToken, which the env assembler sends as the
		// x-wt-model-token line of ANTHROPIC_CUSTOM_HEADERS.
		lc.Env = append(lc.Env,
			"ANTHROPIC_API_KEY=",
			"ANTHROPIC_BASE_URL="+r.BaseOrigin,
		)
		lc.ClearEnv = append(lc.ClearEnv, claudeHeaderModeClear...)
		lc.ClearEnvPrefixes = claudeHeaderModeClearPrefixes()
		lc.HeaderToken = r.APIKey
	} else {
		// The Anthropic client needs a non-empty auth token even when the
		// endpoint doesn't validate it; fall back to the "ollama" placeholder
		// only when the route carries no key (a gateway route always carries
		// the gateway token).
		token := r.APIKey
		if token == "" {
			token = "ollama"
		}
		lc.Env = append(lc.Env,
			"ANTHROPIC_AUTH_TOKEN="+token,
			"ANTHROPIC_API_KEY=",
			"ANTHROPIC_BASE_URL="+r.BaseOrigin,
		)
	}
	lc.Args = append(lc.Args, "--model", r.ModelRef)
	lc.Env = append(lc.Env, claudeSingleModelSlotEnv(r.ModelRef, m)...)
	// Claude Code does not know this model's window and would assume 200K:
	// declare the one tiers.toml records, when it has one and the user has
	// not set their own (#405).
	if w := claudeWindowEnv(m); w != "" {
		lc.Env = append(lc.Env, w)
	}
	return lc
}

// ProfileMechanisms declares which profile mechanisms claude accepts:
// env (the attribution-header/thinking-token tweaks, and any override of the
// ANTHROPIC_DEFAULT_*_MODEL variables wt itself sets on a single-model
// launch), config_file (settings written to .claude/settings.local.json), and
// config_defaults (the same settings, applied only where a leaf is absent).
func (claudeDriver) ProfileMechanisms() []profiles.Mechanism {
	return []profiles.Mechanism{profiles.MechanismEnv, profiles.MechanismConfigFile, profiles.MechanismConfigDefaults}
}

// OneShotArgs runs a single prompt non-interactively and exits — used by
// wt smoke to verify a model works through this agent.
func (claudeDriver) OneShotArgs(prompt string) []string { return []string{"-p", prompt} }
