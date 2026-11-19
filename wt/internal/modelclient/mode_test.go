package modelclient

import (
	"errors"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/modelmode"
)

// var _ pins at compile time that modelclient's ResolveMode still returns a
// modelmode.Resolution: the function is a one-line wrapper, one type behind.
var _ modelmode.Resolution = ResolveMode("", "", "", func(string) string { return "" })

// TestModeNamesAreModelmodeNames pins the aliases: modelclient's mode names
// are modelmode's — one type behind Resolution, one constant behind ModeLocal,
// and a *modelmode.NativeModeError wrapped in this package's Still is still
// recognized as this package's *NativeModeError (the same type).
func TestModeNamesAreModelmodeNames(t *testing.T) {
	if ModeLocal != modelmode.ModeLocal {
		t.Errorf("ModeLocal = %q, modelmode.ModeLocal = %q", ModeLocal, modelmode.ModeLocal)
	}
	if !errors.As(Still(&modelmode.NativeModeError{}, "x"), new(*NativeModeError)) {
		t.Error("a *modelmode.NativeModeError wrapped in Still is not a *NativeModeError: the alias broke")
	}
}

// TestErrorsAreRecognisedThroughWrapping checks errors.As and the Is*
// helpers see the typed errors through Still, because callers append
// advice with Still before returning.
func TestErrorsAreRecognisedThroughWrapping(t *testing.T) {
	un := Still(&ModeUnsetError{}, "nothing started")
	nat := Still(&NativeModeError{Source: SourceStored}, "nothing started")
	var ue *ModeUnsetError
	var ne *NativeModeError
	if !errors.As(un, &ue) || !IsModeUnset(un) || IsNativeMode(un) {
		t.Fatal("unset error not recognised through Still")
	}
	if !errors.As(nat, &ne) || ne.Source != SourceStored || !IsNativeMode(nat) || IsModeUnset(nat) {
		t.Fatal("native error not recognised through Still")
	}
	var pe *ModeProblemError
	if !errors.As(Still(&ModeProblemError{Problem: "p"}, "x"), &pe) || pe.Problem != "p" {
		t.Fatal("problem error not recognised through Still")
	}
}

// TestNoServerCoversUnsetNativeAndLegacy checks NoServer is true for the two
// new "no server" modes and the legacy NoServerConfigured, and false for
// anything else, so callers that treat no server softly keep working.
func TestNoServerCoversUnsetNativeAndLegacy(t *testing.T) {
	if !NoServer(&ModeUnsetError{}) || !NoServer(&NativeModeError{}) {
		t.Fatal("unset and native must count as no server")
	}
	if !NoServer(&NotConfiguredError{MissingURL: true, MissingToken: true}) {
		t.Fatal("legacy no-server must count")
	}
	if NoServer(&NotConfiguredError{MissingURL: true}) || NoServer(&ModeProblemError{Problem: "p"}) || NoServer(errors.New("x")) || NoServer(nil) {
		t.Fatal("half-configured, problem and other errors must not count")
	}
}
