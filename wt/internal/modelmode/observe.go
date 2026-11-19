package modelmode

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/modelping"
	"github.com/ohanaverse/local-ai-setup/wt/internal/urlrule"
)

// envOverrideNames are the variables that override config.toml, in the order
// they are reported.
var envOverrideNames = []string{"WT_MODEL_MODE", "WT_MODEL_URL", "WT_MODEL_TOKEN_FILE", "WT_MODEL_TOKEN"}

// Observe reads the session half of the machine through the four seams and
// never writes to it. It is total: every part carries its own error text. The
// token is read through the FS seam (Stat for the group/other rule
// tokenfile.Load applies, then ReadFile), so the fake can supply it; the
// value is used only for the ping and never stored. The admin reads (platform,
// tools, agent, pair, LiteLLM) are modelsetup's Observe, which calls this one
// with the agent's own token file and listen as the Local fallback.
func Observe(ctx context.Context, env Env, opts ObserveOptions) State {
	var st State
	getenv := env.Host.Getenv

	// Config.
	file := env.Config.Read()
	st.Config = ConfigState{Path: file.Path, Exists: file.Exists}
	if file.ParseErr != nil {
		st.Config.ParseErr = file.ParseErr.Error()
	}
	if file.TableErr != nil {
		st.Config.TableErr = file.TableErr.Error()
	}
	if file.RestErr != nil {
		st.Config.RestErr = file.RestErr.Error()
	}
	// rawURL is the configured url as written, kept out of State: it may
	// carry a user:password. A url with an @ is never pinged (fail closed);
	// rawURL only feeds redactCheck, which scrubs it from other check text.
	var rawURL string
	if file.Table != nil {
		t := *file.Table
		t.Token = "" // a secret never goes into State; Problems reports its presence
		rawURL = t.URL
		// Only a url with an @ is replaced, whole, by urlrule.Hidden (ShownURL
		// leaves any other exactly as written, so an ordinary url never reads
		// as a change).
		t.URL = ShownURL(t.URL)
		st.Config.Table = t
		if file.Table.Token != "" {
			st.Config.Problems = append(st.Config.Problems, "token under [model-server] is set; move it into a 0600 token_file")
		}
		if t.URL != rawURL {
			st.Config.URLHadCredentials = true
			st.Config.Problems = append(st.Config.Problems, "url under [model-server] contains an @ (read as credentials), so wt will not display it; take them out of the url and use a token file")
		}
	}
	tbl := st.Config.Table
	st.Resolution = ResolveMode(tbl.Mode, tbl.URL, tbl.TokenFile, getenv)

	var set []string
	for _, n := range envOverrideNames {
		if getenv(n) != "" {
			set = append(set, n)
		}
	}
	st.EnvOverride = strings.Join(set, ", ")

	// Host: the session half only (the admin facts are modelsetup's Tools).
	home, herr := env.Host.Home()
	if herr == nil {
		st.Host.Home = home
	}
	st.Host.ConfigHome = configHome(getenv("XDG_CONFIG_HOME"), home, herr)

	// Token.
	// This mirrors the session's resolution (modelclient.ResolveEndpoint and
	// cmd/wt-session/models.go), read through the FS seam: an env token file
	// wins and never falls back to the literal; a configured token_file is
	// used (with ~ expanded) unless WT_MODEL_TOKEN is set; the literal is used
	// untrimmed. Only when nothing is set in local mode does the local
	// fallback's token file apply, which a session would not do.
	var token string
	switch {
	case getenv("WT_MODEL_TOKEN_FILE") != "":
		st.Token.Path = getenv("WT_MODEL_TOKEN_FILE")
		token = readToken(env, &st.Token)
	case tbl.TokenFile != "" && getenv("WT_MODEL_TOKEN") == "":
		st.Token.Path = tbl.TokenFile
		if p, err := expandHome(tbl.TokenFile, home, herr); err != nil {
			st.Token.Err = err.Error()
		} else {
			st.Token.Path = p
			token = readToken(env, &st.Token)
		}
	case getenv("WT_MODEL_TOKEN") != "":
		token = getenv("WT_MODEL_TOKEN")
	case st.Resolution.Mode == ModeLocal && opts.Local.TokenFile != "":
		st.Token.Path = opts.Local.TokenFile
		token = readToken(env, &st.Token)
	}

	// The other token files a plan may choose, each described once.
	seen := map[string]bool{st.Token.Path: st.Token.Path != ""}
	var extra []string
	if st.Host.ConfigHome != "" {
		extra = append(extra, DefaultLocalTokenPath(st.Host.ConfigHome), RemoteTokenPath(st.Host.ConfigHome))
	}
	extra = append(extra, opts.Local.TokenFile)
	extra = append(extra, opts.TokenPaths...)
	for _, p := range extra {
		if p == "" {
			continue
		}
		if x, err := expandHome(p, home, herr); err == nil {
			p = x
		}
		if seen[p] {
			continue
		}
		seen[p] = true
		ts := TokenState{Path: p}
		_ = readToken(env, &ts) // only the description is kept
		st.TokenFiles = append(st.TokenFiles, ts)
	}

	// Server.
	envURL := getenv("WT_MODEL_URL")
	url := envURL
	if url == "" {
		url = rawURL
	}
	if url == "" && st.Resolution.Mode == ModeLocal {
		listen := opts.Local.Listen
		if listen == "" {
			listen = DefaultListen
		}
		url = "http://" + listen
	}
	if check := strings.TrimSpace(opts.CheckURL); check != "" {
		url, token = check, strings.TrimSpace(opts.CheckToken.Reveal())
		if path := strings.TrimSpace(opts.CheckTokenFile); token == "" && path != "" {
			ts := TokenState{Path: path}
			if x, err := expandHome(path, home, herr); err == nil {
				ts.Path = x
			}
			token = readToken(env, &ts)
		}
		if token == "" {
			st.Server.Message = "no token entered for " + ShownURL(url)
		}
	}
	switch {
	case urlrule.HasUserinfo(url):
		// Never pinged: the ping's failure quotes the parsed host, which no
		// redaction of the typed text can find, and quotes a malformed url
		// whole. The plan refuses the url anyway; the check reads as not
		// made, in words that hold none of it.
		st.Server = ServerState{URL: urlrule.Hidden, Message: NotCheckedAtURL}
	case ctx.Err() != nil:
		st.Server.Message = "not checked: " + ctx.Err().Error()
	case url != "" && token != "":
		st.Server = PingServer(ctx, env, modelping.Endpoint{URL: url, Token: token})
		st.Server.URL = ShownURL(url)
		// Defence in depth: the url pinged holds no '@' here, but the stored
		// or entered one beside it may.
		st.Server.Message = RedactCheck(st.Server.Message, url, rawURL, envURL, opts.CheckURL)
	}
	return st
}

// PingServer pings ep through the seam, which answers with the classification
// beside the error: kind and message for State, the error unchanged for
// errors.As. An error of no known type reads as unreachable (the seam's
// Classify says so).
func PingServer(ctx context.Context, env Env, ep modelping.Endpoint) ServerState {
	s := ServerState{Checked: true}
	p, kind, msg, err := env.Net.Ping(ctx, ep)
	if err == nil {
		s.OK, s.API, s.Schema = true, p.APIMajor, p.Schema
		return s
	}
	s.Kind, s.Message = kind, msg
	// CheckPing returns the decoded ping beside a skew or schema error, so
	// the numbers the server speaks come from the answer itself.
	if kind == KindSkew {
		s.API = p.APIMajor
	}
	if kind == KindSchema {
		s.Schema = p.Schema
	}
	return s
}

// ExpandIn expands a leading ~ against home; a path it cannot expand is
// returned as given. It is modelsetup's plan-side ~ expansion over the moved
// expandHome.
func ExpandIn(path, home string) string {
	if p, err := expandHome(path, home, nil); err == nil {
		return p
	}
	return path
}

// expandHome is config.ExpandHome with the home directory taken from the
// Host seam.
func expandHome(path, home string, herr error) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	if herr != nil || home == "" {
		return "", fmt.Errorf("expand ~ in token_file: no home directory")
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, path[2:]), nil
}

// configHome is config's baseConfigHome over the Host seam: XDG_CONFIG_HOME
// (a leading ~ expanded), else ~/.config; "" when neither can be known.
func configHome(xdg, home string, herr error) string {
	if xdg != "" {
		if p, err := expandHome(xdg, home, herr); err == nil {
			return p
		}
		return xdg
	}
	if herr != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".config")
}

// readToken fills ts from the token file and returns the token, or "" when it
// is missing, empty, unreadable or loose (a loose file is never read). A
// symlink that does not resolve reads as existing, with an error naming it.
func readToken(env Env, ts *TokenState) string {
	info, err := env.FS.Stat(ts.Path)
	if err != nil {
		switch {
		case DanglingLink(env.FS, ts.Path):
			// A token file that is a link to nothing exists, unreadable: a
			// plan must not make a new token over the link.
			ts.Exists, ts.Dangling, ts.Err = true, true, ts.Path+" is a symlink that does not resolve"
		case !errors.Is(err, fs.ErrNotExist):
			ts.Err = err.Error()
		}
		return ""
	}
	ts.Exists = true
	if info.Mode().Perm()&0o077 != 0 {
		ts.Loose = true
		return ""
	}
	raw, err := env.FS.ReadFile(ts.Path)
	if err != nil {
		ts.Err = err.Error()
		return ""
	}
	tok := strings.TrimSpace(string(raw))
	if tok == "" {
		ts.Empty = true
		ts.Err = "token file is empty"
	}
	return tok
}
