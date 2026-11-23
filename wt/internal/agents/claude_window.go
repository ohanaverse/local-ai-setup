package agents

import (
	"os"
	"strconv"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)

// claudeContextVar is the variable Claude Code reads for the context window
// of a model it does not recognize. Without it a model id the proxy routes
// (anything that is not an Anthropic id) gets an assumed 200K window and the
// startup notice `Model "..." isn't recognized`.
const claudeContextVar = "CLAUDE_CODE_MAX_CONTEXT_TOKENS"

// claudeWindow is the context window, in tokens, of every one of models, the
// smallest of them: each model's HostContext, the window the model server
// reports for it (its registry context_window, #626; else, for a cloud model,
// the host's tiers.toml entry when its context_basis is "host", #405, #470;
// for an alias, the minimum over its targets, #596). It reports false when any model is native (Claude Code
// knows those) or has no HostContext: a window for some of the models says
// nothing about the others, which may be smaller.
func claudeWindow(models ...config.Model) (int, bool) {
	window := 0
	for _, m := range models {
		if m.Native || m.HostContext <= 0 {
			return 0, false
		}
		if window == 0 || m.HostContext < window {
			window = m.HostContext
		}
	}
	return window, window > 0
}

// claudeSlotWindow is the window a slot profile's session declares (#471):
// the default slot's (claudeActiveModels, strict as claudeWindow is), lowered
// to the smallest window of any other slot that has one, since /model can move
// the session onto any slot and the variable is one value per session. A slot
// with no window of its own (a local build, a native model, a host window the
// server does not know) is ignored, not a veto: requiring all four would withdraw the
// declaration from every profile with one local slot and return it to Claude
// Code's assumed 200K. Such a slot can still run past a smaller declared value;
// the profile's own env is the escape hatch.
func claudeSlotWindow(in profiles.SlotBuildInput) (int, bool) {
	window, ok := claudeWindow(claudeActiveModels(in)...)
	if !ok {
		return 0, false
	}
	for _, slot := range claudeSlots {
		if m, filled := in.Models[slot]; filled {
			if w, known := claudeWindow(m); known && w < window {
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
func declaredWindow(window func() (int, bool)) (string, bool) {
	if os.Getenv(claudeContextVar) != "" {
		return "", false
	}
	w, ok := window()
	if !ok {
		return "", false
	}
	return strconv.Itoa(w), true
}

// claudeWindowValue is the window, as the variable's value, that a
// single-model launch of models should declare.
func claudeWindowValue(models ...config.Model) (string, bool) {
	return declaredWindow(func() (int, bool) { return claudeWindow(models...) })
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
	return declaredWindow(func() (int, bool) { return claudeSlotWindow(in) })
}
