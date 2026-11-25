package agents

import (
	"fmt"
	"os"
	"strconv"
	"strings"

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
// the profile's own env is the escape hatch. A profile's explicit `context`
// (#689) replaces the derivation altogether.
func claudeSlotWindow(in profiles.SlotBuildInput) (int, bool) {
	if in.Context > 0 {
		return in.Context, true // the profile's own `context` key (#689) wins over the derived minimum
	}
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

// Slot-window sources, as SlotWindow reports them.
const (
	SlotWindowFromContext = "profile context key"
	SlotWindowFromSlots   = "slots"
	SlotWindowUnknown     = "unknown"
)

// SlotWindow is the window a slot profile's session declares to its agent,
// and where it comes from, for `wt profile show` (#689). Only claude declares
// one: another agent gets (0, ""). It is claudeSlotWindow's answer before the
// environment overrides (the profile env, config_content.env and the user's
// own variable still win at launch).
func SlotWindow(sl profiles.SlotLaunch) (int, string) {
	if sl.Agent != "claude" {
		return 0, ""
	}
	in := profiles.SlotBuildInput{SlotLaunch: sl}
	if sl.Context > 0 {
		return sl.Context, SlotWindowFromContext
	}
	if w, ok := claudeSlotWindow(in); ok {
		return w, SlotWindowFromSlots
	}
	return 0, SlotWindowUnknown
}

// WindowNote is the launch-time note for a gateway model whose context window
// is unknown (#689): the agent then assumes its own default, which can be
// larger or smaller than the model's. "" when there is nothing to say: any
// agent but claude and pi, a native model, or a known window. For an alias it
// names the target without a window (the alias's own HostContext is 0 exactly
// then). The note is advice, not a launch warning: it never reaches
// LaunchCmd.Warn, which `wt smoke` treats as a failure.
func WindowNote(agent string, models ...config.Model) string {
	var assumes string
	switch agent {
	case "claude":
		assumes = "claude assumes 200K"
	case "pi":
		assumes = fmt.Sprintf("pi assumes %d", piDefaultWindow)
	default:
		return ""
	}
	for _, m := range models {
		if m.Native || m.HostContext > 0 {
			continue
		}
		name := m.ID
		if m.GatewayID != "" {
			name = m.GatewayID
		}
		if m.WindowLimiter != "" {
			return fmt.Sprintf("%s has no known context window (%s has none): %s; set it with `wt-model model edit %s --context-window N`", name, m.WindowLimiter, assumes, m.WindowLimiter)
		}
		return fmt.Sprintf("%s has no known context window: %s; set it with `wt-model model edit %s --context-window N`", name, assumes, m.ID)
	}
	return ""
}

// EnvDeclaresWindow reports whether env ("KEY=VALUE" entries, as an
// exec.Cmd carries them) already sets Claude Code's context-window variable:
// the user's own value, or one a profile put there. A launch that does is not
// told its window is unknown.
func EnvDeclaresWindow(env []string) bool {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, claudeContextVar+"="); ok && v != "" {
			return true
		}
	}
	return false
}

// SlotWindowNote is WindowNote for a slot-profile launch: "" unless the
// profile declares no window at all (no `context` key, no window for the
// default slot, nothing in the profile's env), in which case it names the
// first model of the default slot (both opus and sonnet for opusplan) that
// has none. A slot with no window while the default has one is ignored, as
// the declaration ignores it (claudeSlotWindow).
func SlotWindowNote(sl profiles.SlotLaunch) string {
	if sl.Agent != "claude" || sl.Context > 0 {
		return ""
	}
	in := profiles.SlotBuildInput{SlotLaunch: sl}
	if _, ok := claudeSlotWindow(in); ok {
		return ""
	}
	if _, set := sl.Env[claudeContextVar]; set {
		return ""
	}
	if env, ok := sl.ConfigContent["env"].(map[string]any); ok {
		if _, set := env[claudeContextVar]; set {
			return ""
		}
	}
	return WindowNote("claude", claudeActiveModels(in)...)
}
