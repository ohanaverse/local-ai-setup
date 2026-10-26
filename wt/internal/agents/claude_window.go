package agents

import (
	"os"
	"strconv"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tiers"
)

// claudeContextVar is the variable Claude Code reads for the context window
// of a model it does not recognize. Without it a model id the proxy routes
// (anything that is not an Anthropic id) gets an assumed 200K window and the
// startup notice `Model "..." isn't recognized`.
const claudeContextVar = "CLAUDE_CODE_MAX_CONTEXT_TOKENS"

// loadTiers is the seam for reading tiers.toml; tests swap it so the
// developer's own file is never read (#405).
var loadTiers = tiers.Load

// claudeWindow is the context window, in tokens, the tiers file records for
// every one of models, the smallest of them. It reports false when any model
// is native (Claude Code knows those), has no entry, or has an entry with no
// context: tiers.toml carries a window for cloud models only, and a window
// for some of the models says nothing about the others, which may be smaller.
func claudeWindow(ix tiers.Index, models ...config.Model) (int, bool) {
	window := 0
	for _, m := range models {
		if m.Native {
			return 0, false
		}
		e, ok := ix.Lookup(m.ProviderID, m.ModelName)
		if !ok || e.Context <= 0 {
			return 0, false
		}
		if window == 0 || e.Context < window {
			window = e.Context
		}
	}
	return window, window > 0
}

// claudeWindowValue is the window, as the variable's value, that models'
// session should declare, or false when there is none to declare or the user
// already set the variable in the environment wt was started from: one value
// per session, and the user's own always wins.
func claudeWindowValue(models ...config.Model) (string, bool) {
	if os.Getenv(claudeContextVar) != "" {
		return "", false
	}
	w, ok := claudeWindow(loadTiers(), models...)
	if !ok {
		return "", false
	}
	return strconv.Itoa(w), true
}

// claudeWindowEnv is claudeWindowValue as a "KEY=VALUE" entry, or "" when
// there is none.
func claudeWindowEnv(models ...config.Model) string {
	if w, ok := claudeWindowValue(models...); ok {
		return claudeContextVar + "=" + w
	}
	return ""
}

// claudeActiveModels are the models whose window one session's variable has
// to describe: the default slot's, or for opusplan the opus and sonnet slots'
// (plan mode and every other mode). The variable is one value per session
// while a profile has four slots with four windows, so the default slot's is
// the approximation, and the other slots inherit it.
func claudeActiveModels(in profiles.SlotBuildInput) []config.Model {
	if in.Default == claudeOpusplan {
		return []config.Model{in.Models["opus"], in.Models["sonnet"]}
	}
	return []config.Model{in.Models[in.Default]}
}

// claudeWindowSetting is claudeWindowValue for a slot profile's settings env,
// set only when neither the profile's env nor its config_content.env names the
// variable (env already holds both).
func claudeWindowSetting(env map[string]any, in profiles.SlotBuildInput) (string, bool) {
	if _, set := env[claudeContextVar]; set {
		return "", false
	}
	return claudeWindowValue(claudeActiveModels(in)...)
}
