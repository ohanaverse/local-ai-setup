package modelclient

import (
	"errors"
	"strings"
	"testing"
)

// TestResolveModeTable pins the five resolution rules in order. It matters
// because the mode decides whether a session runs natively, locally or
// against a remote server, and a wrong precedence silently changes that.
func TestResolveModeTable(t *testing.T) {
	cases := []struct {
		name                 string
		env                  map[string]string
		mode, url, tokenFile string
		want                 Mode
		src                  ModeSource
		problem              bool
	}{
		{"env native beats env url", map[string]string{"WT_MODEL_MODE": "native", "WT_MODEL_URL": "http://x"}, "", "", "", ModeNative, SourceEnvMode, false},
		{"stored native + env url and token is remote", map[string]string{"WT_MODEL_URL": "http://x", "WT_MODEL_TOKEN": "t"}, "native", "", "", ModeRemote, SourceEnvEndpoint, false},
		{"stored native + env url only stays native", map[string]string{"WT_MODEL_URL": "http://x"}, "native", "", "", ModeNative, SourceStored, false},
		{"env url only is inferred remote", map[string]string{"WT_MODEL_URL": "http://x"}, "", "", "", ModeRemote, SourceInferred, false},
		{"env token only is inferred remote", map[string]string{"WT_MODEL_TOKEN": "t"}, "", "", "", ModeRemote, SourceInferred, false},
		{"stored url only is inferred remote", nil, "", "http://x", "", ModeRemote, SourceInferred, false},
		{"bogus env mode is a problem", map[string]string{"WT_MODEL_MODE": "bogus"}, "", "", "", "", SourceNone, true},
		{"stored LOCAL is local", nil, "LOCAL", "", "", ModeLocal, SourceStored, false},
		{"bogus stored mode is a problem", nil, "bogus", "", "", "", SourceNone, true},
		{"no mode, url+token_file is inferred remote", nil, "", "http://x", "/tok", ModeRemote, SourceInferred, false},
		{"env pair via token file counts", map[string]string{"WT_MODEL_URL": "http://x", "WT_MODEL_TOKEN_FILE": "/tok"}, "local", "", "", ModeRemote, SourceEnvEndpoint, false},
		{"env mode wins over stored", map[string]string{"WT_MODEL_MODE": " Remote "}, "native", "", "", ModeRemote, SourceEnvMode, false},
		{"nothing anywhere is unset", nil, "", "", "", "", SourceNone, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ResolveMode(c.mode, c.url, c.tokenFile, envOf(c.env))
			if got.Mode != c.want || got.Source != c.src || (got.Problem != "") != c.problem {
				t.Fatalf("got %+v, want mode %q source %d problem %v", got, c.want, c.src, c.problem)
			}
			if c.problem && !strings.Contains(got.Problem, "native, local, remote") {
				t.Fatalf("problem %q does not name the valid modes", got.Problem)
			}
		})
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
// anything else, so existing native fallbacks keep working.
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

// TestModeErrorsKeepTheExistingLead checks both messages start with the lead
// current tests and llmbench match on.
func TestModeErrorsKeepTheExistingLead(t *testing.T) {
	const lead = "no model server is configured"
	for _, e := range []error{&ModeUnsetError{}, &NativeModeError{}} {
		if !strings.HasPrefix(e.Error(), lead) {
			t.Fatalf("%q does not start with %q", e.Error(), lead)
		}
	}
}

// TestModeUnsetMessageNamesTheFix checks the unset message tells the user
// both ways out, and the native message the one.
func TestModeUnsetMessageNamesTheFix(t *testing.T) {
	want := "no model server is configured — choose a mode: run wt config (Model server tab) or set WT_MODEL_MODE to native, local or remote"
	if got := (&ModeUnsetError{}).Error(); got != want {
		t.Fatalf("got %q", got)
	}
	want = "no model server is configured — native mode has no model server; change the mode in wt config"
	if got := (&NativeModeError{}).Error(); got != want {
		t.Fatalf("got %q", got)
	}
}

// TestParseModeTrimsAndFolds checks ParseMode accepts only the three modes,
// trimmed and case-insensitive.
func TestParseModeTrimsAndFolds(t *testing.T) {
	if m, ok := ParseMode("  Native "); !ok || m != ModeNative {
		t.Fatal("native")
	}
	if _, ok := ParseMode(""); ok {
		t.Fatal("empty must be invalid")
	}
	if _, ok := ParseMode("x"); ok {
		t.Fatal("x must be invalid")
	}
}
