package modelsetup

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
)

// ObserveOptions tunes Observe. ListenPort is the port to look for a
// listener on; zero means the port of the LaunchAgent's listen address, else
// of DefaultListen. TokenPaths are extra token files to describe in
// State.TokenFiles (a path the user typed for remote, say), so BuildPlan can
// judge them without reading the disk itself.
//
// CheckURL, when set, is the url the user entered: Observe pings it instead
// of the configured one, with CheckToken (a pasted token) or else the token
// read from CheckTokenFile, so a remote plan's check is about the server the
// user named. Neither the token nor the url's userinfo goes into State. A
// url holding an '@' (entered, stored or WT_MODEL_URL) is never pinged: the
// check reads as not made, with the fixed notCheckedAtURL message.
type ObserveOptions struct {
	ListenPort     int
	TokenPaths     []string
	CheckURL       string
	CheckToken     Secret
	CheckTokenFile string
	// SkipLiteLLMProbe leaves out the lookups only a local-mode LiteLLM
	// create plan reads: the binary search and the listener on the default
	// port. Native and remote choices set it, so a read there pays for none.
	SkipLiteLLMProbe bool
	// ProbeLiteLLMWhenLocal overrides SkipLiteLLMProbe when the mode the
	// read resolves is local: a caller that does not yet know the mode (the
	// configuration editor's first read) pays for the probe only then.
	ProbeLiteLLMWhenLocal bool
}

// LiteLLMProbeSkipped reports whether a read made with o and resolving
// mode left out the LiteLLM probe (so its binary and port are unknown, not
// absent). Observe follows the same rule.
func (o ObserveOptions) LiteLLMProbeSkipped(mode modelclient.Mode) bool {
	return o.SkipLiteLLMProbe && !(o.ProbeLiteLLMWhenLocal && mode == modelclient.ModeLocal)
}

// Platform says whether the local mode can be set up here: macOS with launchd.
type Platform struct {
	OK     bool
	Reason string
}

// ConfigState is config.toml's [model-server] table as read.
// URLHadCredentials is true when the stored url held an '@' (read as a
// user:password), so Observe replaced Table.URL whole with hiddenURL
// (Problems says so in words); a plan that writes a url writes a retyped one
// in its place (hiddenURL itself holds an '@' and is refused), so the
// credentials are not kept.
type ConfigState struct {
	Path                        string
	Exists                      bool
	ParseErr, TableErr, RestErr string
	Table                       config.ModelServerState
	Problems                    []string
	URLHadCredentials           bool
}

// TokenState describes the token file; the token itself is never held.
// Empty is true when the file exists, is not loose and holds nothing but
// white space (a loose file is never read, so its content is unknown).
type TokenState struct {
	Path                 string
	Exists, Loose, Empty bool
	Err                  string
}

// PairState describes the LiteLLM proxy pair; the key is only a flag.
type PairState struct {
	URL    string
	KeySet bool
	Err    string
}

// AgentState describes the wt-model LaunchAgent: its plist and launchd job.
type AgentState struct {
	PlistPath        string
	Present, Managed bool
	Job              LaunchdJob
	Listen, Binary   string
	TokenFile        string
	// Content is the plist file as read (it holds no secret: the token is a
	// path), so a plan can tell whether a rewrite would change it.
	Content []byte
	Err     string
}

// ListenerState is whatever listens on the model server's port. Ours is true
// when it is the LaunchAgent job's own process.
type ListenerState struct {
	Port, PID int
	Ours      bool
}

// ServerKind says how a ping failed.
type ServerKind int

const (
	KindNone ServerKind = iota
	KindUnreachable
	KindUnauthorized
	KindNotWtModel
	KindSkew
	KindSchema
)

// ServerState is the result of pinging the configured model server. Checked
// is true only when a URL and a token were known and a Ping was attempted.
// When the URL or token came from the local agent (its listen address or
// token file, used in local mode when nothing else is set) that is a fallback
// a session would not apply, so the tab words it as the agent's, not the
// session's.
//
// URL is the endpoint that was pinged, with any userinfo removed, so a plan
// can tell whether a check was of the url it is about to store.
type ServerState struct {
	URL         string
	Checked, OK bool
	Kind        ServerKind
	Message     string
	API, Schema int
}

// HostState is what a plan needs from the host so BuildPlan stays pure: the
// wt-model binary (beside the running wt, else on PATH, written as found), the
// captured PATH, the home directory, the base config directory
// ($XDG_CONFIG_HOME, else ~/.config; the parent of agent-wt and wt-model) and
// the uid.
type HostState struct {
	Binary, BinaryErr, Path, Home string
	ConfigHome                    string
	UID                           int
}

// LiteLLMPlistState is the proxy LaunchAgent plist as read; never a secret.
// Binary is a bplist (it cannot be edited); Err never quotes the XML parser,
// whose text can echo a fragment of the file.
type LiteLLMPlistState struct {
	Path         string
	Present      bool
	Port         int
	Mode         os.FileMode
	HasMasterKey bool
	// HasSaltKey and HasDatabaseURL say whether LITELLM_SALT_KEY and
	// DATABASE_URL are set (names only, never values).
	HasSaltKey, HasDatabaseURL bool
	Binary                     bool
	Err                        string
	// JobChecked says launchd was asked about the plist's job (only when a
	// plist is present and launchd is available); JobLoaded is its answer.
	// A rotation restarts the job with kickstart, which fails when the job
	// is not loaded, so the plan refuses first.
	JobChecked, JobLoaded bool
}

// LiteLLMInstallState is what a create plan needs to know of an installed
// LiteLLM: the binary (LiteLLMFiles.Binary, empty when none was found) and
// whether its config.yaml exists. wt never installs either.
type LiteLLMInstallState struct {
	Binary, ConfigPath string
	ConfigExists       bool
	// Port is the port a created plist would use; ListenerPID is the
	// process listening on it (0 for none), ListenerChecked whether it was
	// looked at (only when no LiteLLM plist exists).
	Port, ListenerPID int
	ListenerChecked   bool
}

// CheckStatus is the outcome of one LiteLLM check.
type CheckStatus int

const (
	CheckSkipped CheckStatus = iota
	CheckOK
	CheckWarn
	CheckFail
)

// Check is one LiteLLM check's outcome and the words for it.
type Check struct {
	Status CheckStatus
	Detail string
}

// LiteLLMState holds the LiteLLM checks (filled by CheckLiteLLM, which Observe
// calls in local mode only).
type LiteLLMState struct {
	Checked                                                                             bool
	Reachable, Database, KeyAccepted, RoutesInSync, RestartOwed, PortMatches, PlistMode Check
}

// State is everything Observe saw. It never holds a secret.
type State struct {
	Platform    Platform
	Resolution  modelclient.Resolution
	EnvOverride string
	Config      ConfigState
	// Token is the token file a session would use (see Observe).
	Token TokenState
	// TokenFiles describes the other token files a plan may choose: the
	// local default (DefaultLocalTokenPath), the fixed remote path
	// (RemoteTokenPath), the agent's own token file and
	// ObserveOptions.TokenPaths, each once, in that order.
	TokenFiles     []TokenState
	Pair           PairState
	Agent          AgentState
	Listener       ListenerState
	Server         ServerState
	Host           HostState
	LiteLLMPlist   LiteLLMPlistState
	LiteLLMInstall LiteLLMInstallState
	LiteLLM        LiteLLMState
}

// envOverrideNames are the variables that override config.toml, in the order
// they are reported.
var envOverrideNames = []string{"WT_MODEL_MODE", "WT_MODEL_URL", "WT_MODEL_TOKEN_FILE", "WT_MODEL_TOKEN"}

// Observe reads the machine and never writes to it. It is total: every part
// carries its own error text. The token is read through the FS seam (Stat for
// the group/other rule tokenfile.Load applies, then ReadFile), so the fake can
// supply it; the value is used only for the ping and never stored.
func Observe(ctx context.Context, env Env, opts ObserveOptions) State {
	var st State
	getenv := env.Host.Getenv

	// Platform.
	switch {
	case env.Host.GOOS() != "darwin":
		st.Platform.Reason = "the local model server runs as a macOS LaunchAgent; this is " + env.Host.GOOS()
	case !env.Launchd.Available():
		st.Platform.Reason = "launchctl is not available"
	default:
		st.Platform.OK = true
	}

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
		// Only a url with an @ is replaced, whole, by hiddenURL (shownURL
		// leaves any other exactly as written, so an ordinary url never reads
		// as a change).
		t.URL = shownURL(t.URL)
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
	st.Resolution = modelclient.ResolveMode(tbl.Mode, tbl.URL, tbl.TokenFile, getenv)

	var set []string
	for _, n := range envOverrideNames {
		if getenv(n) != "" {
			set = append(set, n)
		}
	}
	st.EnvOverride = strings.Join(set, ", ")

	// Host.
	st.Host.UID = env.Host.UID()
	st.Host.Path = getenv("PATH")
	home, herr := env.Host.Home()
	if herr == nil {
		st.Host.Home = home
	}
	st.Host.Binary, st.Host.BinaryErr = findModelBinary(env)
	st.Host.ConfigHome = configHome(getenv("XDG_CONFIG_HOME"), home, herr)

	// Agent.
	if herr == nil {
		st.Agent.PlistPath = filepath.Join(home, "Library", "LaunchAgents", AgentLabel+".plist")
		if data, err := env.FS.ReadFile(st.Agent.PlistPath); err == nil {
			st.Agent.Present = true
			st.Agent.Content = data
			if p, perr := ParseAgentPlist(data); perr != nil {
				st.Agent.Err = perr.Error()
			} else {
				st.Agent.Managed, st.Agent.Listen, st.Agent.Binary, st.Agent.TokenFile = p.Managed, p.Listen, p.Binary, p.TokenFile
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			st.Agent.Err = err.Error()
		}
	} else {
		st.Agent.Err = "home directory: " + herr.Error()
	}
	if st.Platform.OK && ctx.Err() == nil {
		job, err := env.Launchd.Print(ctx, AgentLabel)
		if err != nil {
			st.Agent.Err = joinErr(st.Agent.Err, "launchctl: "+err.Error())
		}
		st.Agent.Job = job
	}

	// Token.
	// This mirrors the session's resolution (modelclient.ResolveEndpoint and
	// cmd/wt-session/models.go), read through the FS seam: an env token file
	// wins and never falls back to the literal; a configured token_file is
	// used (with ~ expanded) unless WT_MODEL_TOKEN is set; the literal is used
	// untrimmed. Only when nothing is set in local mode does the local agent's
	// own token file apply, which a session would not do.
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
	case st.Resolution.Mode == modelclient.ModeLocal && st.Agent.TokenFile != "":
		st.Token.Path = st.Agent.TokenFile
		token = readToken(env, &st.Token)
	}

	// The other token files a plan may choose, each described once.
	seen := map[string]bool{st.Token.Path: st.Token.Path != ""}
	var extra []string
	if st.Host.ConfigHome != "" {
		extra = append(extra, DefaultLocalTokenPath(st.Host.ConfigHome), RemoteTokenPath(st.Host.ConfigHome))
	}
	extra = append(extra, st.Agent.TokenFile)
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

	// Pair.
	pair, perr := env.Config.Pair()
	st.Pair = PairState{URL: pair.URL, KeySet: pair.APIKey != ""}
	if perr != nil {
		st.Pair.Err = perr.Error()
	}

	// LiteLLM plist.
	lp := env.LiteLLM.PlistPath()
	st.LiteLLMPlist.Path = lp
	if info, err := env.LiteLLM.ReadPlist(lp); err != nil {
		st.LiteLLMPlist.Present = !errors.Is(err, fs.ErrNotExist)
		var se *xml.SyntaxError
		if !st.LiteLLMPlist.Present {
			// A symlink there that does not resolve is a plist that exists
			// (a create must not replace the link), but one wt cannot read.
			if fi, lerr := env.FS.Lstat(lp); lerr == nil && fi.Mode()&fs.ModeSymlink != 0 {
				st.LiteLLMPlist.Present = true
				st.LiteLLMPlist.Err = lp + " is a symlink that does not resolve"
			}
		}
		switch {
		case !st.LiteLLMPlist.Present, st.LiteLLMPlist.Err != "":
		case errors.Is(err, litellm.ErrBinaryPlist):
			st.LiteLLMPlist.Binary, st.LiteLLMPlist.Err = true, err.Error()
		case errors.As(err, &se):
			st.LiteLLMPlist.Err = lp + " is not a well-formed XML plist"
		default:
			st.LiteLLMPlist.Err = PlistReadError(lp, err)
		}
	} else {
		st.LiteLLMPlist.Present, st.LiteLLMPlist.Port, st.LiteLLMPlist.Mode = true, info.Port, info.Mode
		for _, n := range info.EnvNames {
			switch n {
			case "LITELLM_MASTER_KEY":
				st.LiteLLMPlist.HasMasterKey = true
			case "LITELLM_SALT_KEY":
				st.LiteLLMPlist.HasSaltKey = true
			case "DATABASE_URL":
				st.LiteLLMPlist.HasDatabaseURL = true
			}
		}
	}

	if st.LiteLLMPlist.Present && st.Platform.OK && ctx.Err() == nil {
		if job, err := env.Launchd.Print(ctx, litellm.ProxyLabel); err == nil {
			st.LiteLLMPlist.JobChecked, st.LiteLLMPlist.JobLoaded = true, job.Loaded
		}
	}

	// LiteLLM install (for a create plan): the binary and config.yaml.
	skipProbe := opts.LiteLLMProbeSkipped(st.Resolution.Mode)
	if !skipProbe {
		st.LiteLLMInstall.Binary, _ = env.LiteLLM.Binary()
	}
	st.LiteLLMInstall.ConfigPath = env.LiteLLM.ConfigPath()
	if st.LiteLLMInstall.ConfigPath != "" {
		if _, err := env.FS.Stat(st.LiteLLMInstall.ConfigPath); err == nil {
			st.LiteLLMInstall.ConfigExists = true
		}
	}

	// With no LiteLLM plist, whatever listens on the port a created one would
	// use (a LiteLLM run by hand or under another label) blocks a create.
	if !st.LiteLLMPlist.Present && !skipProbe && ctx.Err() == nil {
		st.LiteLLMInstall.Port = DefaultLiteLLMPort
		if pid, ok, err := env.Net.ListenerPID(ctx, DefaultLiteLLMPort); err == nil {
			st.LiteLLMInstall.ListenerChecked = true
			if ok {
				st.LiteLLMInstall.ListenerPID = pid
			}
		}
	}

	// Listener.
	port := opts.ListenPort
	if port == 0 {
		port = portOf(st.Agent.Listen)
	}
	if port == 0 {
		port = portOf(DefaultListen)
	}
	st.Listener.Port = port
	if ctx.Err() == nil {
		if pid, ok, err := env.Net.ListenerPID(ctx, port); err == nil && ok {
			st.Listener.PID = pid
			st.Listener.Ours = st.Agent.Job.Running && st.Agent.Job.PID == pid
		}
	}

	// Server.
	envURL := getenv("WT_MODEL_URL")
	url := envURL
	if url == "" {
		url = rawURL
	}
	if url == "" && st.Resolution.Mode == modelclient.ModeLocal {
		listen := st.Agent.Listen
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
			st.Server.Message = "no token entered for " + shownURL(url)
		}
	}
	switch {
	case hasUserinfo(url):
		// Never pinged: modelclient's failure quotes the parsed host, which no
		// redaction of the typed text can find, and quotes a malformed url whole. The plan refuses the url
		// anyway; the check reads as not made, in words that hold none of it.
		st.Server = ServerState{URL: hiddenURL, Message: notCheckedAtURL}
	case ctx.Err() != nil:
		st.Server.Message = "not checked: " + ctx.Err().Error()
	case url != "" && token != "":
		st.Server = pingServer(ctx, env, modelclient.Endpoint{URL: url, Token: token})
		st.Server.URL = shownURL(url)
		// Defence in depth: the url pinged holds no '@' here, but the stored
		// or entered one beside it may.
		st.Server.Message = redactCheck(st.Server.Message, url, rawURL, envURL, opts.CheckURL)
	}

	// LiteLLM checks: only where the local model server is in use.
	if st.Resolution.Mode == modelclient.ModeLocal && ctx.Err() == nil {
		st.LiteLLM = CheckLiteLLM(ctx, env, pair)
	}
	return st
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

// DefaultLocalTokenPath is where the local plan creates the model server's
// token: <config home>/wt-model/server.token.
func DefaultLocalTokenPath(configHome string) string {
	return filepath.Join(configHome, "wt-model", "server.token")
}

// RemoteTokenPath is the one file a pasted remote token is written to:
// <config home>/agent-wt/remote-model-server.token.
func RemoteTokenPath(configHome string) string {
	return filepath.Join(configHome, "agent-wt", "remote-model-server.token")
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
// is missing, empty, unreadable or loose (a loose file is never read).
func readToken(env Env, ts *TokenState) string {
	info, err := env.FS.Stat(ts.Path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
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

// pingServer pings ep and maps the failure to a ServerKind. An error of no
// known type reads as unreachable.
func pingServer(ctx context.Context, env Env, ep modelclient.Endpoint) ServerState {
	s := ServerState{Checked: true}
	p, err := env.Net.Ping(ctx, ep)
	if err == nil {
		s.OK, s.API, s.Schema = true, p.APIMajor, p.Schema
		return s
	}
	s.Message = err.Error()
	var un *modelclient.UnreachableError
	var ua *modelclient.UnauthorizedError
	var ns *modelclient.NotModelServerError
	var sk *modelclient.SkewError
	var sc *modelclient.SchemaError
	switch {
	case errors.As(err, &ua):
		s.Kind = KindUnauthorized
	case errors.As(err, &ns):
		s.Kind = KindNotWtModel
	case errors.As(err, &sk):
		s.Kind, s.API = KindSkew, sk.Server
	case errors.As(err, &sc):
		s.Kind, s.Schema = KindSchema, sc.Server
	case errors.As(err, &un):
		s.Kind = KindUnreachable
	default:
		s.Kind = KindUnreachable
	}
	return s
}

// findModelBinary looks for wt-model beside the running executable, then on
// PATH; the path is written as found, not symlink-resolved.
func findModelBinary(env Env) (path, errText string) {
	if exe, err := env.Host.Executable(); err == nil {
		cand := filepath.Join(filepath.Dir(exe), "wt-model")
		if info, serr := env.FS.Stat(cand); serr == nil && !info.IsDir() {
			return cand, ""
		}
	}
	if p, err := env.Host.LookPath("wt-model"); err == nil {
		return p, ""
	}
	return "", "wt-model was not found beside wt or on PATH"
}

func portOf(listen string) int {
	_, p, err := net.SplitHostPort(listen)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(p)
	return n
}

func joinErr(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
}
