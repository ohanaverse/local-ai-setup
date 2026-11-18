package modelclient

import "github.com/ohanaverse/local-ai-setup/wt/internal/modelmode"

// The mode rules live in internal/modelmode; these are modelclient's names
// for them, so the session side keeps its vocabulary (and every caller's
// modelclient.ResolveMode, ModeUnsetError and friends keep compiling).

// Mode is how a session reaches a model server: native (none), local or remote.
type Mode = modelmode.Mode

// The three modes.
const (
	ModeNative = modelmode.ModeNative
	ModeLocal  = modelmode.ModeLocal
	ModeRemote = modelmode.ModeRemote
)

// ModeSource says where a resolved mode came from.
type ModeSource = modelmode.ModeSource

// The sources, highest precedence first (after SourceNone).
const (
	SourceNone        = modelmode.SourceNone
	SourceEnvMode     = modelmode.SourceEnvMode
	SourceEnvEndpoint = modelmode.SourceEnvEndpoint
	SourceStored      = modelmode.SourceStored
	SourceInferred    = modelmode.SourceInferred
)

// Resolution is the effective mode. Mode == "" and Problem == "" means unset.
type Resolution = modelmode.Resolution

// ModeUnsetError says no mode is chosen anywhere.
type ModeUnsetError = modelmode.ModeUnsetError

// NativeModeError says the mode is native, which has no model server.
type NativeModeError = modelmode.NativeModeError

// ModeProblemError says a stored or environment mode value is invalid.
type ModeProblemError = modelmode.ModeProblemError

// ParseMode reads a mode name, trimmed and case-insensitive. It is
// modelmode.ParseMode.
func ParseMode(s string) (Mode, bool) { return modelmode.ParseMode(s) }

// ResolveMode decides the effective mode: a valid WT_MODEL_MODE; the env url
// plus a token; the stored mode; any endpoint value (inferred remote, so a
// half-configured server refuses later in ResolveEndpoint); else unset. An
// explicit native ignores stray endpoint values. It is modelmode.ResolveMode.
func ResolveMode(storedMode, storedURL, storedTokenFile string, env func(string) string) Resolution {
	return modelmode.ResolveMode(storedMode, storedURL, storedTokenFile, env)
}

// IsModeUnset reports whether err is, or wraps, a *ModeUnsetError. It is
// modelmode.IsModeUnset.
func IsModeUnset(err error) bool { return modelmode.IsModeUnset(err) }

// IsNativeMode reports whether err is, or wraps, a *NativeModeError. It is
// modelmode.IsNativeMode.
func IsNativeMode(err error) bool { return modelmode.IsNativeMode(err) }

// NoServer reports whether err means there is no model server to use: the
// mode is unset or native, or the legacy NoServerConfigured holds. It stays
// here: NoServerConfigured is this package's endpoint rule.
func NoServer(err error) bool {
	return IsModeUnset(err) || IsNativeMode(err) || NoServerConfigured(err)
}
