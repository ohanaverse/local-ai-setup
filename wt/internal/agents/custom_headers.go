package agents

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)

// Header mode (#597, decision 7): a claude launch whose model uses the
// owner's Claude subscription keeps Claude Code's own login and sends the
// model-server token only as one line of ANTHROPIC_CUSTOM_HEADERS,
// "x-wt-model-token: <token>". The drivers stay pure and hand the token back
// as LaunchCmd.HeaderToken or SlotCommand.HeaderToken; the env assembler
// (launchEnv, called by command and slotLaunchCmd) builds the variable from
// the inherited one. Every other launch, of every agent, strips an inherited
// wt line, so a session started from a header-mode shell never carries the
// token further.

// ErrBadHeaderToken is what errors.Is finds when the model-server token
// cannot travel in a header line. The message never holds the token.
var ErrBadHeaderToken = errors.New("the model-server token cannot be sent as the x-wt-model-token header")

// checkHeaderToken refuses a token that is empty, or holds a character
// outside printable ASCII (a newline, a carriage return, a tab or any other
// control character, DEL, or a non-ASCII byte): a newline would split the
// ANTHROPIC_CUSTOM_HEADERS value into another header, and the rest would
// corrupt it. The error never holds the token.
func checkHeaderToken(token string) error {
	if token == "" {
		return fmt.Errorf("%w: it is empty", ErrBadHeaderToken)
	}
	for i := 0; i < len(token); i++ {
		if c := token[i]; c < 0x20 || c > 0x7e {
			return fmt.Errorf("%w: it holds a newline, a control character or a character outside printable ASCII", ErrBadHeaderToken)
		}
	}
	return nil
}

// headerLines splits an ANTHROPIC_CUSTOM_HEADERS value into its trimmed,
// non-blank lines (\r\n, a lone \r and \n all end one).
func headerLines(v string) []string { return profiles.CustomHeaderLines(v) }

// keepHeaders is v's lines without those whose header name drop reports,
// joined with \n.
func keepHeaders(v string, drop func(name string) bool) string {
	var kept []string
	for _, line := range headerLines(v) {
		if !drop(profiles.CustomHeaderName(line)) {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

// mergeCustomHeaders is the header-mode value of ANTHROPIC_CUSTOM_HEADERS:
// the inherited lines without any x-wt-model-token or authorization line
// (case-insensitive, trimmed), then "x-wt-model-token: <token>". The
// authorization line goes because the login must be the one credential
// Claude Code sends beside the wt token.
func mergeCustomHeaders(inherited, token string) string {
	kept := keepHeaders(inherited, func(name string) bool {
		return name == profiles.WtHeaderName || name == "authorization"
	})
	line := profiles.WtHeaderName + ": " + token
	if kept == "" {
		return line
	}
	return kept + "\n" + line
}

// stripWtHeader is the value of ANTHROPIC_CUSTOM_HEADERS on every launch that
// is not in header mode: the inherited lines without any x-wt-model-token
// line. Everything else, an authorization line included, stays. "" means the
// variable is removed.
func stripWtHeader(inherited string) string {
	return keepHeaders(inherited, func(name string) bool { return name == profiles.WtHeaderName })
}

// hasWtHeader reports whether v holds an x-wt-model-token line.
func hasWtHeader(v string) bool {
	for _, line := range headerLines(v) {
		if profiles.CustomHeaderName(line) == profiles.WtHeaderName {
			return true
		}
	}
	return false
}

// launchEnv assembles a launched agent's environment: inherited without the
// names in clear and the names starting with one of prefixes, then extra, and
// ANTHROPIC_CUSTOM_HEADERS settled by mode. With headerToken set (header
// mode) the variable is replaced by mergeCustomHeaders' value, once, last.
// Otherwise an inherited value holding a wt line is replaced by
// stripWtHeader's (removed when that is empty), and one without a wt line is
// left exactly as inherited.
func launchEnv(inherited, clear, prefixes, extra []string, headerToken string) ([]string, error) {
	if headerToken != "" {
		if err := checkHeaderToken(headerToken); err != nil {
			return nil, err
		}
	}
	cur, have := "", false
	for _, e := range inherited {
		if k, v, ok := strings.Cut(e, "="); ok && k == profiles.CustomHeadersVar {
			cur, have = v, true
		}
	}
	clear = clear[:len(clear):len(clear)]
	var set string
	switch {
	case headerToken != "":
		clear = append(clear, profiles.CustomHeadersVar)
		set = mergeCustomHeaders(cur, headerToken)
	case have && hasWtHeader(cur):
		clear = append(clear, profiles.CustomHeadersVar)
		set = stripWtHeader(cur)
	}
	env := append(filterEnvPrefixes(filterEnv(inherited, clear), prefixes), extra...)
	if set != "" {
		env = append(env, profiles.CustomHeadersVar+"="+set)
	}
	return env, nil
}
