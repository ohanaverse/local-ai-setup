package modelsetup

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
)

const (
	obsTokenPath = "/Users/user_name/.config/wt-model/token"
	obsFakeToken = "tok-SECRET-0123456789"
	obsFakeKey   = "sk-PAIRKEY-9876543210"
	obsAgentPath = "/Users/user_name/Library/LaunchAgents/local.wt-model.serve.plist"
)

// obsConfigured returns a fake with a local-mode config, a 0600 token file and
// nothing else, the baseline the Observe tests vary.
func obsConfigured(t *testing.T) *fakeEnv {
	f := newFakeEnv(t)
	f.configFile = config.ModelServerFile{
		Path: "/Users/user_name/.config/agent-wt/config.toml", Exists: true,
		Table: &config.ModelServerState{Mode: "local", URL: "http://127.0.0.1:8787", TokenFile: obsTokenPath},
	}
	f.files[obsTokenPath] = []byte(obsFakeToken + "\n")
	f.modes[obsTokenPath] = 0o600
	return f
}

// TestObserveMissingConfigIsEmptyNotAnError pins that a machine with no
// config.toml observes as an empty, error-free Config part, so a first run
// is not reported as a failure.
func TestObserveMissingConfigIsEmptyNotAnError(t *testing.T) {
	f := newFakeEnv(t)
	st := Observe(context.Background(), f.Env(), ObserveOptions{})
	if st.Config.Exists || st.Config.ParseErr != "" || st.Config.TableErr != "" {
		t.Errorf("Config = %+v, want empty and error-free", st.Config)
	}
	if st.Server.Checked {
		t.Errorf("Server.Checked with nothing configured")
	}
}

// TestObserveReportsAParseErrorAndStillFillsTheRest pins that a config.toml
// syntax error is carried as text while the token, agent and server parts are
// still observed from what is known.
func TestObserveReportsAParseErrorAndStillFillsTheRest(t *testing.T) {
	f := obsConfigured(t)
	f.configFile.ParseErr = errors.New("toml: line 3: bad")
	f.env["WT_MODEL_URL"] = "http://127.0.0.1:8787"
	f.env["WT_MODEL_TOKEN_FILE"] = obsTokenPath
	f.files[obsAgentPath] = RenderAgentPlist(AgentSpec{Binary: "/b/wt-model", Listen: DefaultListen, TokenFile: obsTokenPath, Path: "/bin", Home: "/h"})
	f.jobs[AgentLabel] = LaunchdJob{Loaded: true, Running: true, PID: 7}
	f.ping = modelapi.Ping{APIMajor: 1, Schema: 1, Server: "wt-model"}
	st := Observe(context.Background(), f.Env(), ObserveOptions{})
	if !strings.Contains(st.Config.ParseErr, "line 3") {
		t.Errorf("ParseErr = %q", st.Config.ParseErr)
	}
	if !st.Token.Exists || st.Token.Path != obsTokenPath {
		t.Errorf("Token = %+v", st.Token)
	}
	if !st.Agent.Present || !st.Agent.Managed || !st.Agent.Job.Running {
		t.Errorf("Agent = %+v", st.Agent)
	}
	if !st.Server.Checked || !st.Server.OK {
		t.Errorf("Server = %+v", st.Server)
	}
}

// TestObserveClassifiesEachPingFailure pins that every modelclient error type
// maps to its own ServerKind, so the tab can say what is wrong.
func TestObserveClassifiesEachPingFailure(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want ServerKind
	}{
		{"none", nil, KindNone},
		{"unreachable", &modelclient.UnreachableError{Addr: "a", Reason: "refused"}, KindUnreachable},
		{"unauthorized", &modelclient.UnauthorizedError{Addr: "a"}, KindUnauthorized},
		{"not wt-model", &modelclient.NotModelServerError{Addr: "a", Why: "x"}, KindNotWtModel},
		{"skew", &modelclient.SkewError{Addr: "a", Server: 2, Client: 1}, KindSkew},
		{"schema", &modelclient.SchemaError{Addr: "a", Server: 2, Client: 1}, KindSchema},
		{"wrapped", fmt.Errorf("ctx: %w", &modelclient.UnauthorizedError{Addr: "a"}), KindUnauthorized},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := obsConfigured(t)
			f.pingErr = c.err
			f.ping = modelapi.Ping{APIMajor: 1, Schema: 3}
			st := Observe(context.Background(), f.Env(), ObserveOptions{})
			if !st.Server.Checked || st.Server.Kind != c.want || st.Server.OK != (c.err == nil) {
				t.Errorf("Server = %+v, want kind %v", st.Server, c.want)
			}
			if c.err != nil && st.Server.Message == "" {
				t.Errorf("no message for %v", c.err)
			}
			if c.err == nil && (st.Server.API != 1 || st.Server.Schema != 3) {
				t.Errorf("API/Schema = %d/%d", st.Server.API, st.Server.Schema)
			}
		})
	}
}

// TestObserveLoosenessOfTheTokenFile pins that a group/other-accessible token
// file is reported Loose and is never pinged with, while 0600 is fine.
func TestObserveLoosenessOfTheTokenFile(t *testing.T) {
	f := obsConfigured(t)
	f.modes[obsTokenPath] = 0o644
	st := Observe(context.Background(), f.Env(), ObserveOptions{})
	if !st.Token.Exists || !st.Token.Loose {
		t.Errorf("Token = %+v, want Exists and Loose", st.Token)
	}
	if st.Server.Checked || f.called("Net.Ping") {
		t.Errorf("pinged with a loose token file")
	}
	f.modes[obsTokenPath] = 0o600
	if st := Observe(context.Background(), f.Env(), ObserveOptions{}); st.Token.Loose || !st.Server.Checked {
		t.Errorf("0600: Token = %+v Server = %+v", st.Token, st.Server)
	}
}

// TestObserveRecognisesAManagedAndAnUnmanagedPlist pins that the agent plist
// is Managed only when wt wrote it, and that its fields are reported.
func TestObserveRecognisesAManagedAndAnUnmanagedPlist(t *testing.T) {
	f := obsConfigured(t)
	spec := AgentSpec{Binary: "/b/wt-model", Listen: "127.0.0.1:9000", TokenFile: obsTokenPath, Path: "/bin", Home: "/h"}
	f.files[obsAgentPath] = RenderAgentPlist(spec)
	st := Observe(context.Background(), f.Env(), ObserveOptions{})
	if !st.Agent.Present || !st.Agent.Managed || st.Agent.Listen != "127.0.0.1:9000" || st.Agent.Binary != "/b/wt-model" || st.Agent.TokenFile != obsTokenPath {
		t.Errorf("managed Agent = %+v", st.Agent)
	}
	f.files[obsAgentPath] = []byte(strings.Replace(string(RenderAgentPlist(spec)), "<!-- managed by wt -->\n", "", 1))
	st = Observe(context.Background(), f.Env(), ObserveOptions{})
	if !st.Agent.Present || st.Agent.Managed {
		t.Errorf("unmanaged Agent = %+v", st.Agent)
	}
	f.files[obsAgentPath] = []byte("not a plist")
	if st = Observe(context.Background(), f.Env(), ObserveOptions{}); st.Agent.Err == "" {
		t.Errorf("garbage plist gave no Err: %+v", st.Agent)
	}
}

// TestObserveListenerOursVersusForeign pins that a listener is Ours only when
// its pid is the LaunchAgent job's pid.
func TestObserveListenerOursVersusForeign(t *testing.T) {
	f := obsConfigured(t)
	f.jobs[AgentLabel] = LaunchdJob{Loaded: true, Running: true, PID: 4242}
	f.listenerOK, f.listenerPID = true, 4242
	st := Observe(context.Background(), f.Env(), ObserveOptions{ListenPort: 8787})
	if st.Listener.Port != 8787 || st.Listener.PID != 4242 || !st.Listener.Ours {
		t.Errorf("ours: %+v", st.Listener)
	}
	f.listenerPID = 99
	if st = Observe(context.Background(), f.Env(), ObserveOptions{ListenPort: 8787}); st.Listener.PID != 99 || st.Listener.Ours {
		t.Errorf("foreign: %+v", st.Listener)
	}
	f.listenerOK = false
	if st = Observe(context.Background(), f.Env(), ObserveOptions{ListenPort: 8787}); st.Listener.PID != 0 || st.Listener.Ours {
		t.Errorf("nothing listening: %+v", st.Listener)
	}
}

// TestObserveOffMacOSDisablesLocal pins that Platform.OK needs darwin and an
// available launchd, and that launchctl is not asked otherwise.
func TestObserveOffMacOSDisablesLocal(t *testing.T) {
	f := obsConfigured(t)
	if st := Observe(context.Background(), f.Env(), ObserveOptions{}); !st.Platform.OK {
		t.Errorf("darwin: %+v", st.Platform)
	}
	f.goos = "linux"
	st := Observe(context.Background(), f.Env(), ObserveOptions{})
	if st.Platform.OK || st.Platform.Reason == "" {
		t.Errorf("linux: %+v", st.Platform)
	}
	f = obsConfigured(t)
	f.launchdUnavailable = true
	f.calls = nil
	st = Observe(context.Background(), f.Env(), ObserveOptions{})
	if st.Platform.OK || st.Platform.Reason == "" || f.called("Launchd.Print") {
		t.Errorf("no launchd: %+v calls %q", st.Platform, f.calls)
	}
}

// TestObserveNamesEnvOverrides pins that the environment variables in force
// are named (never their values) and that an unset environment reads "".
func TestObserveNamesEnvOverrides(t *testing.T) {
	f := obsConfigured(t)
	if st := Observe(context.Background(), f.Env(), ObserveOptions{}); st.EnvOverride != "" {
		t.Errorf("EnvOverride = %q, want empty", st.EnvOverride)
	}
	f.env["WT_MODEL_URL"] = "http://elsewhere:1"
	f.env["WT_MODEL_TOKEN_FILE"] = obsTokenPath
	st := Observe(context.Background(), f.Env(), ObserveOptions{})
	if st.EnvOverride != "WT_MODEL_URL, WT_MODEL_TOKEN_FILE" {
		t.Errorf("EnvOverride = %q", st.EnvOverride)
	}
	if strings.Contains(st.EnvOverride, "elsewhere") {
		t.Errorf("EnvOverride holds a value: %q", st.EnvOverride)
	}
}

// TestObserveNeverPrintsASecret pins that no part of State holds the token or
// the pair's API key, and that Observe makes no write call.
func TestObserveNeverPrintsASecret(t *testing.T) {
	f := obsConfigured(t)
	f.pair = config.ProxyPair{URL: "http://127.0.0.1:4000", APIKey: obsFakeKey}
	f.env["WT_MODEL_TOKEN"] = obsFakeToken
	f.plistInfo.EnvNames = []string{"LITELLM_MASTER_KEY"}
	f.plistVars["LITELLM_MASTER_KEY"] = obsFakeKey
	f.pingErr = &modelclient.UnauthorizedError{Addr: "http://127.0.0.1:8787"}
	st := Observe(context.Background(), f.Env(), ObserveOptions{})
	dump := fmt.Sprintf("%+v", st)
	for _, secret := range []string{obsFakeToken, obsFakeKey} {
		if strings.Contains(dump, secret) {
			t.Errorf("State leaks %q: %s", secret, dump)
		}
	}
	if !st.Pair.KeySet || !st.LiteLLMPlist.HasMasterKey {
		t.Errorf("presence flags: %+v %+v", st.Pair, st.LiteLLMPlist)
	}
	for _, c := range f.calls {
		for _, w := range []string{"FS.WriteAtomic", "FS.Chmod", "FS.Remove", "FS.Backup", "Launchd.Bootstrap", "Launchd.Bootout", "Launchd.Kickstart", "Config.SetPair", "Config.Update", "Host.RandRead"} {
			if strings.HasPrefix(c, w) {
				t.Errorf("Observe wrote: %s", c)
			}
		}
	}
}

// TestObserveHonoursACancelledContext pins that a cancelled context stops the
// network steps instead of running them.
func TestObserveHonoursACancelledContext(t *testing.T) {
	f := obsConfigured(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	st := Observe(ctx, f.Env(), ObserveOptions{})
	if f.called("Net.") {
		t.Errorf("network touched on a cancelled context: %q", f.calls)
	}
	if st.Server.Message == "" {
		t.Errorf("no message: %+v", st.Server)
	}
}

// TestObserveFindsTheModelBinaryBesideWtThenOnPath pins the HostState lookup
// order and that the path is reported as found.
func TestObserveFindsTheModelBinaryBesideWtThenOnPath(t *testing.T) {
	f := newFakeEnv(t)
	f.env["PATH"] = "/usr/bin"
	f.paths["wt-model"] = "/opt/bin/wt-model"
	st := Observe(context.Background(), f.Env(), ObserveOptions{})
	if st.Host.Binary != "/opt/bin/wt-model" || st.Host.Path != "/usr/bin" || st.Host.Home != f.home || st.Host.UID != 501 {
		t.Errorf("PATH fallback: %+v", st.Host)
	}
	f.files["/Users/user_name/.local/bin/wt-model"] = []byte("x")
	if st = Observe(context.Background(), f.Env(), ObserveOptions{}); st.Host.Binary != "/Users/user_name/.local/bin/wt-model" {
		t.Errorf("beside: %+v", st.Host)
	}
	delete(f.paths, "wt-model")
	delete(f.files, "/Users/user_name/.local/bin/wt-model")
	if st = Observe(context.Background(), f.Env(), ObserveOptions{}); st.Host.Binary != "" || st.Host.BinaryErr == "" {
		t.Errorf("missing: %+v", st.Host)
	}
}
