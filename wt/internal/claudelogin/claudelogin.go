// Package claudelogin asks Claude Code whether this machine holds a claude.ai
// login and which version is installed (#597, decision 8), for the
// pre-launch guard of a launch that sends that login through the model
// server's gateway.
//
// It runs `claude auth status --json` and `claude --version` with the
// credential variables scrubbed from the environment, so the answer reflects
// the user's own stored login and not a token or key exported in the shell
// (with one of those set Claude Code reports `oauth_token`, a false positive).
// Only four fields of the status are decoded (loggedIn, authMethod,
// apiProvider, loginExpiresAt): the email, organization and every other field
// are never kept. No error text carries the command's output.
//
// The package imports only the standard library.
package claudelogin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// MinVersion is the oldest Claude Code a header-mode launch accepts: the
// first that sends ANTHROPIC_CUSTOM_HEADERS on every request.
const MinVersion = "2.1.227"

// Timeout bounds the whole check (both commands).
const Timeout = 10 * time.Second

// maxOutput bounds what is read from either command.
const maxOutput = 64 << 10

// Status is what the check learned. It holds no personal field.
type Status struct {
	LoggedIn  bool
	Method    string
	Provider  string
	ExpiresAt time.Time
	Version   string
}

// OK reports a claude.ai login: logged in, method claude.ai, provider
// firstParty. An env token (`oauth_token`), an API key, a cloud provider or
// no login is not one.
func (s Status) OK() bool {
	return s.LoggedIn && s.Method == "claude.ai" && s.Provider == "firstParty"
}

// VersionAtLeast reports whether s.Version is min or newer. An unreadable
// version is not.
func (s Status) VersionAtLeast(min string) bool {
	have, ok := parseVersion(s.Version)
	want, wok := parseVersion(min)
	if !ok || !wok {
		return false
	}
	for i := range want {
		if have[i] != want[i] {
			return have[i] > want[i]
		}
	}
	return true
}

// scrubbedNames are removed from the environment the check runs in.
// ANTHROPIC_CUSTOM_HEADERS goes too: a nested session's copy carries the wt
// token, which the check has no use for.
var scrubbedNames = []string{
	"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL",
	"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_CUSTOM_HEADERS",
}

// scrubbedPrefixes are removed by prefix: Bedrock, Vertex, Foundry and any
// later CLAUDE_CODE_USE_* switch.
var scrubbedPrefixes = []string{"CLAUDE_CODE_USE_"}

// Scrub returns environ without the credential variables.
func Scrub(environ []string) []string {
	out := make([]string, 0, len(environ))
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		drop := false
		for _, n := range scrubbedNames {
			if name == n {
				drop = true
			}
		}
		for _, p := range scrubbedPrefixes {
			if strings.HasPrefix(name, p) {
				drop = true
			}
		}
		if !drop {
			out = append(out, kv)
		}
	}
	return out
}

// Fixed error texts: none carries the command's output.
var (
	ErrNoStatus     = errors.New("claude auth status did not report a login")
	ErrStatusFailed = errors.New("claude auth status exited with an error (no login)")
	ErrTimeout      = errors.New("claude did not answer within 10 s")
)

// Check runs bin (the claude binary's path) twice in a scrubbed copy of
// environ, within Timeout: `--version`, then `auth status --json`. The
// version is read even when the login is not. An error says why no login
// could be read (a non-zero exit, unreadable output, a timeout); the Status
// then reports no login.
func Check(ctx context.Context, bin string, environ []string) (Status, error) {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	env := Scrub(environ)
	var st Status
	if out, err := run(ctx, bin, env, "--version"); err == nil {
		st.Version = versionOf(out)
	} else if ctx.Err() != nil {
		return st, ErrTimeout
	}
	out, err := run(ctx, bin, env, "auth", "status", "--json")
	if ctx.Err() != nil {
		return st, ErrTimeout
	}
	if err != nil {
		return st, ErrStatusFailed
	}
	var raw struct {
		LoggedIn       *bool           `json:"loggedIn"`
		AuthMethod     string          `json:"authMethod"`
		APIProvider    string          `json:"apiProvider"`
		LoginExpiresAt json.RawMessage `json:"loginExpiresAt"`
	}
	if jerr := json.Unmarshal(bytes.TrimSpace(out), &raw); jerr != nil || raw.LoggedIn == nil {
		return st, ErrNoStatus
	}
	st.LoggedIn, st.Method, st.Provider = *raw.LoggedIn, raw.AuthMethod, raw.APIProvider
	st.ExpiresAt = expiryOf(raw.LoginExpiresAt)
	return st, nil
}

// run runs bin with args and returns its stdout (bounded); stderr is
// discarded, stdin is empty.
func run(ctx context.Context, bin string, env []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = env
	cmd.Stdin = nil
	cmd.Stderr = io.Discard
	// A child that leaves a grandchild holding the pipe must not hold the
	// check past its deadline.
	cmd.WaitDelay = time.Second
	var buf limitedBuffer
	buf.max = maxOutput
	cmd.Stdout = &buf
	err := cmd.Run()
	return buf.b.Bytes(), err
}

// limitedBuffer keeps at most max bytes and discards the rest.
type limitedBuffer struct {
	b   bytes.Buffer
	max int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := l.max - l.b.Len(); room > 0 {
		if len(p) > room {
			l.b.Write(p[:room])
		} else {
			l.b.Write(p)
		}
	}
	return len(p), nil
}

var versionRE = regexp.MustCompile(`\b(\d+)\.(\d+)\.(\d+)\b`)

// versionOf is the first x.y.z in out, "" when none.
func versionOf(out []byte) string {
	return versionRE.FindString(string(out))
}

// parseVersion reads x.y.z.
func parseVersion(v string) ([3]int, bool) {
	var out [3]int
	m := versionRE.FindStringSubmatch(v)
	if m == nil {
		return out, false
	}
	for i := 0; i < 3; i++ {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// expiryOf reads loginExpiresAt: an RFC 3339 string, or a number of epoch
// milliseconds (seconds when it is too small to be milliseconds). Anything
// else is the zero time (unknown).
func expiryOf(raw json.RawMessage) time.Time {
	if len(raw) == 0 {
		return time.Time{}
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t
		}
		return time.Time{}
	}
	var n float64
	if json.Unmarshal(raw, &n) == nil && n > 0 {
		if n < 1e11 {
			return time.Unix(int64(n), 0)
		}
		return time.UnixMilli(int64(n))
	}
	return time.Time{}
}
