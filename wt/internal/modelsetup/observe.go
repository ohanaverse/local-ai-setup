package modelsetup

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
)

// ObserveOptions tunes Observe. ListenPort is the port to look for a
// listener on; zero means the port of the LaunchAgent's listen address, else
// of DefaultListen.
type ObserveOptions struct{ ListenPort int }

// Platform says whether the local mode can be set up here: macOS with launchd.
type Platform struct {
	OK     bool
	Reason string
}

// ConfigState is config.toml's [model-server] table as read.
type ConfigState struct {
	Path               string
	Exists             bool
	ParseErr, TableErr string
	Table              config.ModelServerState
	Problems           []string
}

// TokenState describes the token file; the token itself is never held.
type TokenState struct {
	Path          string
	Exists, Loose bool
	Err           string
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
	Err              string
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
type ServerState struct {
	Checked, OK bool
	Kind        ServerKind
	Message     string
	API, Schema int
}

// HostState is what a plan needs from the host so BuildPlan stays pure: the
// wt-model binary (beside the running wt, else on PATH, written as found), the
// captured PATH, the home directory and the uid.
type HostState struct {
	Binary, BinaryErr, Path, Home string
	UID                           int
}

// LiteLLMPlistState is the proxy LaunchAgent plist as read; never a secret.
type LiteLLMPlistState struct {
	Path         string
	Present      bool
	Port         int
	Mode         os.FileMode
	HasMasterKey bool
	Err          string
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

// LiteLLMState holds the LiteLLM checks (filled by CheckLiteLLM, not Observe).
type LiteLLMState struct {
	Checked                                                                             bool
	Reachable, Database, KeyAccepted, RoutesInSync, RestartOwed, PortMatches, PlistMode Check
}

// State is everything Observe saw. It never holds a secret.
type State struct {
	Platform     Platform
	Resolution   modelclient.Resolution
	EnvOverride  string
	Config       ConfigState
	Token        TokenState
	Pair         PairState
	Agent        AgentState
	Listener     ListenerState
	Server       ServerState
	Host         HostState
	LiteLLMPlist LiteLLMPlistState
	LiteLLM      LiteLLMState
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
	if file.Table != nil {
		t := *file.Table
		t.Token = "" // a secret never goes into State; Problems reports its presence
		st.Config.Table = t
		if file.Table.Token != "" {
			st.Config.Problems = append(st.Config.Problems, "token under [model-server] is set; move it into a 0600 token_file")
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

	// Agent.
	if herr == nil {
		st.Agent.PlistPath = filepath.Join(home, "Library", "LaunchAgents", AgentLabel+".plist")
		if data, err := env.FS.ReadFile(st.Agent.PlistPath); err == nil {
			st.Agent.Present = true
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
		if st.LiteLLMPlist.Present {
			st.LiteLLMPlist.Err = err.Error()
		}
	} else {
		st.LiteLLMPlist.Present, st.LiteLLMPlist.Port, st.LiteLLMPlist.Mode = true, info.Port, info.Mode
		for _, n := range info.EnvNames {
			if n == "LITELLM_MASTER_KEY" {
				st.LiteLLMPlist.HasMasterKey = true
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
	url := getenv("WT_MODEL_URL")
	if url == "" {
		url = tbl.URL
	}
	if url == "" && st.Resolution.Mode == modelclient.ModeLocal {
		listen := st.Agent.Listen
		if listen == "" {
			listen = DefaultListen
		}
		url = "http://" + listen
	}
	if err := ctx.Err(); err != nil {
		st.Server.Message = "not checked: " + err.Error()
	} else if url != "" && token != "" {
		st.Server = pingServer(ctx, env, modelclient.Endpoint{URL: url, Token: token})
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
