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
// is native (Claude Code knows those), has no entry, has an entry with no
// context, or has one whose window is not its host's: tiers.toml carries a
// window for cloud models only, a window for some of the models says nothing
// about the others, which may be smaller, and a window that is the largest of
// the model's models.dev rows (context_basis "largest_row", or a file with no
// basis at all) is not what any host serves, so declaring it would have Claude
// Code compact too late against a window the provider rejects (#470).
func claudeWindow(ix tiers.Index, models ...config.Model) (int, bool) {
	window := 0
	for _, m := range models {
		if m.Native {
			return 0, false
		}
		e, ok := ix.Lookup(m.ProviderID, m.ModelName)
		if !ok || e.Context <= 0 || e.ContextBasis != tiers.ContextBasisHost {
			return 0, false
		}
		if window == 0 || e.Context < window {
			window = e.Context
		}
	}
	return window, window > 0
}

// claudeSlotWindow is the window a slot profile's session declares (#471):
// the default slot's (claudeActiveModels, strict as claudeWindow is), lowered
// to the smallest window of any other slot that has one, since /model can move
// the session onto any slot and the variable is one value per session. A slot
// with no window of its own (a local build, a native model, a file with no
// basis) is ignored, not a veto: requiring all four would withdraw the
// declaration from every profile with one local slot and return it to Claude
// Code's assumed 200K. Such a slot can still run past a smaller declared value;
// the profile's own env is the escape hatch.
func claudeSlotWindow(ix tiers.Index, in profiles.SlotBuildInput) (int, bool) {
	window, ok := claudeWindow(ix, claudeActiveModels(in)...)
	if !ok {
		return 0, false
	}
	for _, slot := range claudeSlots {
		if m, filled := in.Models[slot]; filled {
			if w, known := claudeWindow(ix, m); known && w < window {
				window = w
			}
		}
	}
	return window, true
}

// declaredWindow is the variable's value for window's answer, or false when
// there is none to declare or the user already set the variable in the
// environment wt was started from: one value per session, and the user's own
// always wins.
func declaredWindow(window func(tiers.Index) (int, bool)) (string, bool) {
	if os.Getenv(claudeContextVar) != "" {
		return "", false
	}
	w, ok := window(loadTiers())
	if !ok {
		return "", false
	}
	return strconv.Itoa(w), true
}

// claudeWindowValue is the window, as the variable's value, that a
// single-model launch of models should declare.
func claudeWindowValue(models ...config.Model) (string, bool) {
	return declaredWindow(func(ix tiers.Index) (int, bool) { return claudeWindow(ix, models...) })
}

// claudeWindowEnv is claudeWindowValue as a "KEY=VALUE" entry, or "" when
// there is none.
func claudeWindowEnv(models ...config.Model) string {
	if w, ok := claudeWindowValue(models...); ok {
		return claudeContextVar + "=" + w
	}
	return ""
}

// claudeActiveModels are the models whose window anchors a slot profile's
// declaration: the default slot's, or for opusplan the opus and sonnet slots'
// (plan mode and every other mode). claudeSlotWindow lowers it by the other
// slots.
func claudeActiveModels(in profiles.SlotBuildInput) []config.Model {
	if in.Default == claudeOpusplan {
		return []config.Model{in.Models["opus"], in.Models["sonnet"]}
	}
	return []config.Model{in.Models[in.Default]}
}

// claudeWindowSetting is the declared window for a slot profile's settings
// env, set only when neither the profile's env nor its config_content.env
// names the variable (env already holds both).
func claudeWindowSetting(env map[string]any, in profiles.SlotBuildInput) (string, bool) {
	if _, set := env[claudeContextVar]; set {
		return "", false
	}
	return declaredWindow(func(ix tiers.Index) (int, bool) { return claudeSlotWindow(ix, in) })
}
