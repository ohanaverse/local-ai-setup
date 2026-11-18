package modelmode

import (
	"errors"
	"strings"
)

// Mode is how a session reaches a model server: native (none), local or remote.
type Mode string

// The three modes.
const (
	ModeNative Mode = "native"
	ModeLocal  Mode = "local"
	ModeRemote Mode = "remote"
)

// ParseMode reads a mode name, trimmed and case-insensitive.
func ParseMode(s string) (Mode, bool) {
	switch m := Mode(strings.ToLower(strings.TrimSpace(s))); m {
	case ModeNative, ModeLocal, ModeRemote:
		return m, true
	}
	return "", false
}

// ModeSource says where a resolved mode came from.
type ModeSource int

// The sources, highest precedence first (after SourceNone).
const (
	SourceNone ModeSource = iota
	SourceEnvMode
	SourceEnvEndpoint
	SourceStored
	SourceInferred
)

// Resolution is the effective mode. Mode == "" and Problem == "" means unset.
type Resolution struct {
	Mode    Mode
	Source  ModeSource
	Problem string
}

const modeNames = "native, local, remote"

// ResolveMode decides the effective mode: a valid WT_MODEL_MODE; the env url
// plus a token; the stored mode; any endpoint value (inferred remote, so a
// half-configured server refuses later in ResolveEndpoint); else unset. An
// explicit native ignores stray endpoint values.
func ResolveMode(storedMode, storedURL, storedTokenFile string, env func(string) string) Resolution {
	if v := strings.TrimSpace(env("WT_MODEL_MODE")); v != "" {
		if m, ok := ParseMode(v); ok {
			return Resolution{Mode: m, Source: SourceEnvMode}
		}
		return Resolution{Problem: "WT_MODEL_MODE is " + quote(v) + "; it must be one of " + modeNames}
	}
	if env("WT_MODEL_URL") != "" && (env("WT_MODEL_TOKEN_FILE") != "" || env("WT_MODEL_TOKEN") != "") {
		return Resolution{Mode: ModeRemote, Source: SourceEnvEndpoint}
	}
	if v := strings.TrimSpace(storedMode); v != "" {
		if m, ok := ParseMode(v); ok {
			return Resolution{Mode: m, Source: SourceStored}
		}
		return Resolution{Problem: "mode under [model-server] is " + quote(v) + "; it must be one of " + modeNames}
	}
	if storedURL != "" || storedTokenFile != "" || env("WT_MODEL_URL") != "" ||
		env("WT_MODEL_TOKEN_FILE") != "" || env("WT_MODEL_TOKEN") != "" {
		return Resolution{Mode: ModeRemote, Source: SourceInferred}
	}
	return Resolution{}
}

func quote(s string) string { return `"` + s + `"` }

// ModeUnsetError says no mode is chosen anywhere.
type ModeUnsetError struct{}

func (*ModeUnsetError) Error() string {
	return "no model server is configured — choose a mode: run wt config (Model server tab) or set WT_MODEL_MODE to native, local or remote"
}

// NativeModeError says the mode is native, which has no model server.
type NativeModeError struct{ Source ModeSource }

func (*NativeModeError) Error() string {
	return "no model server is configured — native mode has no model server; change the mode in wt config"
}

// ModeProblemError says a stored or environment mode value is invalid.
type ModeProblemError struct{ Problem string }

func (e *ModeProblemError) Error() string { return e.Problem }

// IsModeUnset reports whether err is, or wraps, a *ModeUnsetError.
func IsModeUnset(err error) bool {
	var e *ModeUnsetError
	return errors.As(err, &e)
}

// IsNativeMode reports whether err is, or wraps, a *NativeModeError.
func IsNativeMode(err error) bool {
	var e *NativeModeError
	return errors.As(err, &e)
}
