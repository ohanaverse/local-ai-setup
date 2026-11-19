package modeview

import (
	"reflect"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/editorkit"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelmode"
)

// TestModeSourceWords pins the word for each place a mode can come from:
// both editors show it after the mode, so the wording must be one copy.
func TestModeSourceWords(t *testing.T) {
	cases := map[modelmode.ModeSource]string{
		modelmode.SourceEnvMode:     "from WT_MODEL_MODE",
		modelmode.SourceEnvEndpoint: "from WT_MODEL_URL with a token",
		modelmode.SourceStored:      "from config.toml",
		modelmode.SourceInferred:    "inferred: a url or token is set",
		modelmode.SourceNone:        "",
	}
	for src, want := range cases {
		if got := ModeSource(modelmode.Resolution{Source: src}); got != want {
			t.Errorf("ModeSource(%v) = %q, want %q", src, got, want)
		}
	}
}

// TestKindWords pins how a failed ping is named, so the server line and the
// save-anyway prompt say the same thing in either editor.
func TestKindWords(t *testing.T) {
	cases := map[modelmode.ServerKind]string{
		modelmode.KindUnauthorized: "token refused",
		modelmode.KindNotWtModel:   "not a wt-model server",
		modelmode.KindSkew:         "api version differs",
		modelmode.KindSchema:       "schema differs",
		modelmode.KindUnreachable:  "unreachable",
	}
	for k, want := range cases {
		if got := KindWord(k); got != want {
			t.Errorf("KindWord(%v) = %q, want %q", k, got, want)
		}
	}
}

// TestModeLineCases covers the four shapes of the mode line (resolved,
// unreadable config, a mode problem, unset) and their tones, because the
// tone is what tells the user to act.
func TestModeLineCases(t *testing.T) {
	cases := []struct {
		name string
		st   modelmode.State
		want editorkit.Line
	}{
		{"resolved", modelmode.State{Resolution: modelmode.Resolution{Mode: "remote", Source: modelmode.SourceStored}},
			editorkit.Line{Label: "mode", Text: "remote (from config.toml)", Tone: editorkit.ToneNone}},
		{"unreadable", modelmode.State{Config: modelmode.ConfigState{ParseErr: "x"}},
			editorkit.Line{Label: "mode", Text: "unknown: config.toml is not readable", Tone: editorkit.ToneWarn}},
		{"problem", modelmode.State{Resolution: modelmode.Resolution{Problem: "WT_MODEL_MODE=x is not a mode"}},
			editorkit.Line{Label: "mode", Text: "problem: WT_MODEL_MODE=x is not a mode", Tone: editorkit.ToneBad}},
		{"unset", modelmode.State{},
			editorkit.Line{Label: "mode", Text: "unset: choose one below", Tone: editorkit.ToneWarn}},
	}
	for _, c := range cases {
		if got := ModeLine(c.st); got != c.want {
			t.Errorf("%s: ModeLine = %+v, want %+v", c.name, got, c.want)
		}
	}
}

// TestURLLineScrubsAndSaysNone checks the url line says "none set" for an
// empty url and never shows a url's credentials.
func TestURLLineScrubsAndSaysNone(t *testing.T) {
	if got, want := URLLine(modelmode.State{}), (editorkit.Line{Label: "url", Text: "none set", Tone: editorkit.ToneNone}); got != want {
		t.Errorf("URLLine(empty) = %+v, want %+v", got, want)
	}
	var st modelmode.State
	st.Config.Table.URL = "https://u:p@h:8787"
	got := URLLine(st)
	if got.Text != editorkit.Scrub("https://u:p@h:8787") || strings.Contains(got.Text, "u:p") {
		t.Errorf("URLLine(creds) = %q, want the scrubbed url without u:p", got.Text)
	}
}

// TestTokenLineWords covers each token-file state's words and tone, with the
// path shown under ~ for the user's home.
func TestTokenLineWords(t *testing.T) {
	const home = "/Users/user_name"
	withTok := func(tk modelmode.TokenState) modelmode.State {
		st := modelmode.State{Token: tk}
		st.Host.Home = home
		return st
	}
	p := home + "/.config/t"
	cases := []struct {
		name string
		st   modelmode.State
		want editorkit.Line
	}{
		{"env", modelmode.State{EnvOverride: "WT_MODEL_TOKEN"},
			editorkit.Line{Label: "token", Text: "from WT_MODEL_TOKEN (not shown)", Tone: editorkit.ToneNone}},
		{"none", modelmode.State{},
			editorkit.Line{Label: "token", Text: "none set", Tone: editorkit.ToneDim}},
		{"loose", withTok(modelmode.TokenState{Path: p, Exists: true, Loose: true}),
			editorkit.Line{Label: "token", Text: "~/.config/t  loose: chmod 600 ~/.config/t", Tone: editorkit.ToneBad}},
		{"err", withTok(modelmode.TokenState{Path: p, Exists: true, Err: "x"}),
			editorkit.Line{Label: "token", Text: "~/.config/t  problem: x", Tone: editorkit.ToneBad}},
		{"missing", withTok(modelmode.TokenState{Path: p}),
			editorkit.Line{Label: "token", Text: "~/.config/t  missing", Tone: editorkit.ToneBad}},
		{"ok", withTok(modelmode.TokenState{Path: p, Exists: true}),
			editorkit.Line{Label: "token", Text: "~/.config/t  ok", Tone: editorkit.ToneNone}},
	}
	for _, c := range cases {
		if got := TokenLine(c.st, home); got != c.want {
			t.Errorf("%s: TokenLine = %+v, want %+v", c.name, got, c.want)
		}
	}
}

// TestServerCheckLineWords covers the server line: native, a message that
// already says "not checked" (no double prefix), what an unchecked remote
// lacked, a good ping and a failed one.
func TestServerCheckLineWords(t *testing.T) {
	native := modelmode.State{Resolution: modelmode.Resolution{Mode: modelmode.ModeNative}}
	remote := modelmode.Resolution{Mode: modelmode.ModeRemote}
	cases := []struct {
		name string
		st   modelmode.State
		want editorkit.Line
	}{
		{"native", native, editorkit.Line{Label: "server", Text: "none in native mode", Tone: editorkit.ToneDim}},
		{"message", modelmode.State{Resolution: remote, Server: modelmode.ServerState{Message: "not checked: the url contains an @; retype it without credentials"}},
			editorkit.Line{Label: "server", Text: "not checked: the url contains an @; retype it without credentials", Tone: editorkit.ToneDim}},
		{"missing", modelmode.State{Resolution: remote},
			editorkit.Line{Label: "server", Text: "not checked: no url and no token", Tone: editorkit.ToneDim}},
		{"ok", modelmode.State{Resolution: remote, Server: modelmode.ServerState{Checked: true, OK: true, URL: "http://127.0.0.1:8787", API: 1, Schema: 1}},
			editorkit.Line{Label: "server", Text: "ok: wt-model answers at http://127.0.0.1:8787 (api 1, schema 1)", Tone: editorkit.ToneNone}},
		{"failed", modelmode.State{Resolution: remote, Server: modelmode.ServerState{Checked: true, Kind: modelmode.KindSkew, Message: "m"}},
			editorkit.Line{Label: "server", Text: "problem: api version differs: m", Tone: editorkit.ToneBad}},
	}
	for _, c := range cases {
		if got := ServerCheckLine(c.st); got != c.want {
			t.Errorf("%s: ServerCheckLine = %+v, want %+v", c.name, got, c.want)
		}
	}
}

// TestEnvAndProblemLines checks the env-override line appears only when a
// variable overrides config.toml, and the problem lines carry one label on
// the first line in the table, rest, list order.
func TestEnvAndProblemLines(t *testing.T) {
	l, ok := EnvLine(modelmode.State{EnvOverride: "WT_MODEL_URL, WT_MODEL_TOKEN"})
	if want := (editorkit.Line{Label: "env", Text: "WT_MODEL_URL, WT_MODEL_TOKEN set: they override config.toml", Tone: editorkit.ToneWarn}); !ok || l != want {
		t.Errorf("EnvLine = %+v, %v; want %+v, true", l, ok, want)
	}
	if _, ok := EnvLine(modelmode.State{}); ok {
		t.Error("EnvLine with no override = true, want false")
	}
	st := modelmode.State{Config: modelmode.ConfigState{TableErr: "t", RestErr: "r", Problems: []string{"p"}}}
	want := []editorkit.Line{
		{Label: "problems", Text: "[model-server]: t", Tone: editorkit.ToneBad},
		{Label: "", Text: "config.toml: r", Tone: editorkit.ToneBad},
		{Label: "", Text: "p", Tone: editorkit.ToneBad},
	}
	if got := ProblemLines(st); !reflect.DeepEqual(got, want) {
		t.Errorf("ProblemLines = %+v, want %+v", got, want)
	}
	if got := EnvNames(modelmode.State{EnvOverride: "A, B"}); !reflect.DeepEqual(got, []string{"A", "B"}) {
		t.Errorf("EnvNames = %v, want [A B]", got)
	}
}
