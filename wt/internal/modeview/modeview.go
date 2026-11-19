// Package modeview words the model-server mode for a screen: the mode and
// where it came from, the url, the token file, the server check, the
// env-override line and the config problems, as editorkit lines. It is one
// copy of those lines for every editor that shows them, and a session-safe
// leaf: it reads only modelmode.State and links no admin package.
package modeview

import (
	"fmt"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/editorkit"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelmode"
)

// ModeSource says where the effective mode came from.
func ModeSource(r modelmode.Resolution) string {
	switch r.Source {
	case modelmode.SourceEnvMode:
		return "from WT_MODEL_MODE"
	case modelmode.SourceEnvEndpoint:
		return "from WT_MODEL_URL with a token"
	case modelmode.SourceStored:
		return "from config.toml"
	case modelmode.SourceInferred:
		return "inferred: a url or token is set"
	}
	return ""
}

// EnvNames splits State.EnvOverride into the variable names.
func EnvNames(st modelmode.State) []string {
	if st.EnvOverride == "" {
		return nil
	}
	return strings.Split(st.EnvOverride, ", ")
}

func hasEnv(st modelmode.State, name string) bool {
	for _, n := range EnvNames(st) {
		if n == name {
			return true
		}
	}
	return false
}

// ModeLine is the effective mode and where it came from.
func ModeLine(st modelmode.State) editorkit.Line {
	r := st.Resolution
	switch {
	case r.Mode != "":
		return editorkit.Line{Label: "mode", Text: string(r.Mode) + " (" + ModeSource(r) + ")", Tone: editorkit.ToneNone}
	case st.Config.ParseErr != "":
		// The banner above says why, once.
		return editorkit.Line{Label: "mode", Text: "unknown: config.toml is not readable", Tone: editorkit.ToneWarn}
	case r.Problem != "":
		return editorkit.Line{Label: "mode", Text: "problem: " + editorkit.Scrub(r.Problem), Tone: editorkit.ToneBad}
	}
	return editorkit.Line{Label: "mode", Text: "unset: choose one below", Tone: editorkit.ToneWarn}
}

// URLLine is the stored url, scrubbed, or "none set".
func URLLine(st modelmode.State) editorkit.Line {
	url := st.Config.Table.URL
	if url == "" {
		url = "none set"
	}
	return editorkit.Line{Label: "url", Text: editorkit.Scrub(url), Tone: editorkit.ToneNone}
}

// TokenLine is the token file and its state, as a word.
func TokenLine(st modelmode.State, userHome string) editorkit.Line {
	t := st.Token
	if t.Path == "" {
		if hasEnv(st, "WT_MODEL_TOKEN") {
			return editorkit.Line{Label: "token", Text: "from WT_MODEL_TOKEN (not shown)", Tone: editorkit.ToneNone}
		}
		return editorkit.Line{Label: "token", Text: "none set", Tone: editorkit.ToneDim}
	}
	path := editorkit.HomePath(t.Path, st.Host.Home, userHome)
	switch {
	case t.Loose:
		return editorkit.Line{Label: "token", Text: path + "  loose: chmod 600 " + path, Tone: editorkit.ToneBad}
	case t.Err != "":
		return editorkit.Line{Label: "token", Text: path + "  problem: " + t.Err, Tone: editorkit.ToneBad}
	case !t.Exists:
		return editorkit.Line{Label: "token", Text: path + "  missing", Tone: editorkit.ToneBad}
	}
	return editorkit.Line{Label: "token", Text: path + "  ok", Tone: editorkit.ToneNone}
}

// KindWord names how a ping failed.
func KindWord(k modelmode.ServerKind) string {
	switch k {
	case modelmode.KindUnauthorized:
		return "token refused"
	case modelmode.KindNotWtModel:
		return "not a wt-model server"
	case modelmode.KindSkew:
		return "api version differs"
	case modelmode.KindSchema:
		return "schema differs"
	}
	return "unreachable"
}

// ServerCheckLine is the server check: none in native mode, not checked and
// why, ok, or the problem.
func ServerCheckLine(st modelmode.State) editorkit.Line {
	s := st.Server
	switch {
	case st.Resolution.Mode == modelmode.ModeNative:
		return editorkit.Line{Label: "server", Text: "none in native mode", Tone: editorkit.ToneDim}
	case !s.Checked && s.Message != "":
		// A message that already says so (a url with an '@', a cancelled
		// read) is not prefixed twice.
		return editorkit.Line{Label: "server", Text: "not checked: " + editorkit.Scrub(strings.TrimPrefix(s.Message, "not checked: ")), Tone: editorkit.ToneDim}
	case !s.Checked:
		return editorkit.Line{Label: "server", Text: "not checked: " + missingForCheck(st), Tone: editorkit.ToneDim}
	case s.OK:
		return editorkit.Line{Label: "server", Text: fmt.Sprintf("ok: wt-model answers at %s (api %d, schema %d)", editorkit.Scrub(s.URL), s.API, s.Schema), Tone: editorkit.ToneNone}
	}
	return editorkit.Line{Label: "server", Text: "problem: " + KindWord(s.Kind) + ": " + editorkit.Scrub(s.Message), Tone: editorkit.ToneBad}
}

// missingForCheck says what a check of the server lacked: the url, the token
// or both. A local server's url is the agent's listen address, never missing.
func missingForCheck(st modelmode.State) string {
	noURL := st.Resolution.Mode != modelmode.ModeLocal && st.Config.Table.URL == "" && st.Server.URL == "" && !hasEnv(st, "WT_MODEL_URL")
	t := st.Token
	noToken := !(t.Path != "" && t.Exists && t.Err == "") && !hasEnv(st, "WT_MODEL_TOKEN")
	switch {
	case noURL && noToken:
		return "no url and no token"
	case noURL:
		return "no url"
	case noToken:
		return "no token"
	}
	return "no url or no token"
}

// EnvLine names the variables that override config.toml; false when none do.
func EnvLine(st modelmode.State) (editorkit.Line, bool) {
	names := EnvNames(st)
	if len(names) == 0 {
		return editorkit.Line{}, false
	}
	return editorkit.Line{Label: "env", Text: strings.Join(names, ", ") + " set: they override config.toml", Tone: editorkit.ToneWarn}, true
}

// ProblemLines are config.toml's problems, the first labelled "problems":
// the [model-server] table's, the rest of the file's, then the listed ones.
func ProblemLines(st modelmode.State) []editorkit.Line {
	var problems []string
	if st.Config.TableErr != "" {
		problems = append(problems, "[model-server]: "+st.Config.TableErr)
	}
	if st.Config.RestErr != "" {
		problems = append(problems, "config.toml: "+st.Config.RestErr)
	}
	problems = append(problems, st.Config.Problems...)
	var lines []editorkit.Line
	for i, p := range problems {
		label := ""
		if i == 0 {
			label = "problems"
		}
		lines = append(lines, editorkit.Line{Label: label, Text: editorkit.Scrub(p), Tone: editorkit.ToneBad})
	}
	return lines
}
