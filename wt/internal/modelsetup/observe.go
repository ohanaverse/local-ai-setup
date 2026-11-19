package modelsetup

import (
	"context"
	"encoding/xml"
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"

	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelmode"
	"github.com/ohanaverse/local-ai-setup/wt/internal/urlrule"
)

// ObserveOptions tunes Observe. ListenPort is the port to look for a
// listener on; zero means the port of the LaunchAgent's listen address, else
// of DefaultListen.
type ObserveOptions struct {
	modelmode.ObserveOptions
	ListenPort int
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
func (o ObserveOptions) LiteLLMProbeSkipped(mode modelmode.Mode) bool {
	return o.SkipLiteLLMProbe && !(o.ProbeLiteLLMWhenLocal && mode == modelmode.ModeLocal)
}

// Platform says whether the local mode can be set up here: macOS with launchd.
type Platform struct {
	OK     bool
	Reason string
}

// ConfigState is config.toml's [model-server] table as read (modelmode's
// session-half type).
type ConfigState = modelmode.ConfigState

// TokenState describes the token file (modelmode's session-half type).
type TokenState = modelmode.TokenState

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

// ServerKind says how a ping failed (modelmode's alias of modelping.Kind).
type ServerKind = modelmode.ServerKind

const (
	KindNone         = modelmode.KindNone
	KindUnreachable  = modelmode.KindUnreachable
	KindUnauthorized = modelmode.KindUnauthorized
	KindNotWtModel   = modelmode.KindNotWtModel
	KindSkew         = modelmode.KindSkew
	KindSchema       = modelmode.KindSchema
)

// ServerState is the result of pinging the configured model server
// (modelmode's session-half type).
type ServerState = modelmode.ServerState

// HostState is the session half of the host's facts: home and config home
// (modelmode's type). The admin facts are ToolsState.
type HostState = modelmode.HostState

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

// State is everything Observe saw: the session half embedded (modelmode's
// State: the mode read, the config and token files, the ping, the host's
// session paths) plus the admin half. It never holds a secret.
type State struct {
	Platform Platform
	modelmode.State
	Pair           PairState
	Agent          AgentState
	Listener       ListenerState
	Tools          ToolsState
	LiteLLMPlist   LiteLLMPlistState
	LiteLLMInstall LiteLLMInstallState
	LiteLLM        LiteLLMState
}

// ToolsState is the admin host facts a plan needs: the wt-model binary
// (beside the running wt, else on PATH, written as found), the captured PATH
// and the uid.
type ToolsState struct {
	Binary, BinaryErr, Path string
	UID                     int
}

// Observe reads the machine and never writes to it. It is total: every part
// carries its own error text. The session half (config and mode read, the
// token choice, the files a plan may choose, the ping) comes from
// modelmode.Observe through the four session seams; this function adds the
// admin reads around it: platform, tools, agent, pair, LiteLLM.
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

	// Tools and home: the admin host facts (the session half re-reads home
	// itself through env.Mode(); no test pins call order across the seams).
	st.Tools.UID = env.Host.UID()
	st.Tools.Path = getenv("PATH")
	home, herr := env.Host.Home()
	st.Tools.Binary, st.Tools.BinaryErr = findModelBinary(env)

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
		} else if modelmode.DanglingLink(env.FS, st.Agent.PlistPath) {
			// A link there that does not resolve is a plist that exists (a
			// plan must not write over the link), but one wt cannot read.
			st.Agent.Present, st.Agent.Err = true, st.Agent.PlistPath+" is a symlink that does not resolve"
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

	// The session half, over the four session seams: config and mode read,
	// the token choice, the files a plan may choose and the ping. The agent's
	// own token file and listen go along as the local fallback a local-mode
	// read with nothing stored falls back to.
	o := opts.ObserveOptions
	o.Local = modelmode.LocalFallback{TokenFile: st.Agent.TokenFile, Listen: st.Agent.Listen}
	st.State = modelmode.Observe(ctx, env.Mode(), o)

	// Pair.
	pair, perr := env.Config.Pair()
	// The pair url is shown like every url here: one holding an '@' is the
	// fixed placeholder, whole (#640). The pair still reads as present.
	st.Pair = PairState{URL: modelmode.ShownURL(pair.URL), KeySet: pair.APIKey != ""}
	if perr != nil {
		st.Pair.Err = perr.Error()
		if urlrule.HasUserinfo(st.Pair.Err) {
			st.Pair.Err = pairErrHidden
		}
	}

	// LiteLLM plist.
	lp := env.LiteLLM.PlistPath()
	st.LiteLLMPlist.Path = lp
	if info, err := env.LiteLLM.ReadPlist(lp); err != nil {
		// A symlink there that does not resolve is a plist that exists (a
		// create must not replace the link), but one wt cannot read.
		dangling := modelmode.DanglingLink(env.FS, lp)
		st.LiteLLMPlist.Present = dangling || !errors.Is(err, fs.ErrNotExist)
		var se *xml.SyntaxError
		switch {
		case dangling:
			st.LiteLLMPlist.Err = lp + " is a symlink that does not resolve"
		case !st.LiteLLMPlist.Present:
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

	// LiteLLM checks: only where the local model server is in use.
	if st.Resolution.Mode == modelmode.ModeLocal && ctx.Err() == nil {
		st.LiteLLM = CheckLiteLLM(ctx, env, pair)
	}
	return st
}

// DefaultLocalTokenPath is where the local plan creates the model server's
// token: <config home>/wt-model/server.token. It is
// modelmode.DefaultLocalTokenPath.
func DefaultLocalTokenPath(configHome string) string {
	return modelmode.DefaultLocalTokenPath(configHome)
}

// RemoteTokenPath is the one file a pasted remote token is written to:
// <config home>/agent-wt/remote-model-server.token. It is
// modelmode.RemoteTokenPath.
func RemoteTokenPath(configHome string) string {
	return modelmode.RemoteTokenPath(configHome)
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
