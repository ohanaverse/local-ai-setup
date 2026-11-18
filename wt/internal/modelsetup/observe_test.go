package modelsetup

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
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

// TestObserveKeepsAnInlineConfigTokenOutOfState pins that a literal token in
// [model-server] is reported as a problem but never copied into State.
func TestObserveKeepsAnInlineConfigTokenOutOfState(t *testing.T) {
	f := obsConfigured(t)
	f.configFile.Table.Token = obsFakeToken
	st := Observe(context.Background(), f.Env(), ObserveOptions{})
	if strings.Contains(fmt.Sprintf("%+v", st), obsFakeToken) {
		t.Errorf("State holds the inline token: %+v", st.Config)
	}
	if len(st.Config.Problems) == 0 {
		t.Errorf("inline token not reported as a problem")
	}
}

// TestObserveExpandsATildeInTheConfiguredTokenFile pins that a config
// token_file of ~/... is read from the home directory, as a session does.
func TestObserveExpandsATildeInTheConfiguredTokenFile(t *testing.T) {
	f := obsConfigured(t)
	f.configFile.Table.TokenFile = "~/.config/wt-model/token"
	st := Observe(context.Background(), f.Env(), ObserveOptions{})
	if !st.Token.Exists || st.Token.Err != "" || !st.Server.Checked {
		t.Errorf("Token = %+v Server = %+v", st.Token, st.Server)
	}
}

// TestObserveTokenChoiceMirrorsTheSession pins ResolveEndpoint's order: an env
// token file wins and never falls back to a literal WT_MODEL_TOKEN, which is
// otherwise used untrimmed.
func TestObserveTokenChoiceMirrorsTheSession(t *testing.T) {
	f := obsConfigured(t)
	f.env["WT_MODEL_TOKEN_FILE"] = "/missing/token"
	f.env["WT_MODEL_TOKEN"] = obsFakeToken
	st := Observe(context.Background(), f.Env(), ObserveOptions{})
	if st.Token.Path != "/missing/token" || st.Token.Exists || st.Server.Checked {
		t.Errorf("env file must win without fallback: %+v %+v", st.Token, st.Server)
	}
	f = obsConfigured(t)
	f.env["WT_MODEL_TOKEN"] = " " + obsFakeToken + " "
	f.calls = nil
	st = Observe(context.Background(), f.Env(), ObserveOptions{})
	if !st.Server.Checked || f.called("FS.ReadFile "+obsTokenPath) {
		t.Errorf("literal token: %+v calls %q", st.Server, f.calls)
	}
}

// TestObserveDescribesTheTokenFilesAPlanMayChoose pins the additions BuildPlan
// reads instead of the disk: the config home, the default local and fixed
// remote token files plus any asked-for path (each once), an empty file
// flagged Empty, and the agent plist's bytes.
func TestObserveDescribesTheTokenFilesAPlanMayChoose(t *testing.T) {
	f := obsConfigured(t)
	f.env["XDG_CONFIG_HOME"] = "~/xdg"
	f.files["/Users/user_name/xdg/wt-model/server.token"] = []byte(" \n")
	f.modes["/Users/user_name/xdg/wt-model/server.token"] = 0o600
	f.files["/srv/tok"] = []byte(obsFakeToken)
	f.modes["/srv/tok"] = 0o644
	plist := RenderAgentPlist(AgentSpec{Binary: "/b/wt-model", Listen: DefaultListen, TokenFile: obsTokenPath, Path: "/bin", Home: "/h"})
	f.files[obsAgentPath] = plist
	st := Observe(context.Background(), f.Env(), ObserveOptions{TokenPaths: []string{"/srv/tok", obsTokenPath}})
	if st.Host.ConfigHome != "/Users/user_name/xdg" {
		t.Errorf("ConfigHome = %q", st.Host.ConfigHome)
	}
	want := []TokenState{
		{Path: "/Users/user_name/xdg/wt-model/server.token", Exists: true, Empty: true, Err: "token file is empty"},
		{Path: "/Users/user_name/xdg/agent-wt/remote-model-server.token"},
		{Path: "/srv/tok", Exists: true, Loose: true},
	}
	if fmt.Sprint(st.TokenFiles) != fmt.Sprint(want) {
		t.Errorf("TokenFiles = %+v, want %+v", st.TokenFiles, want)
	}
	if string(st.Agent.Content) != string(plist) {
		t.Errorf("Agent.Content not kept")
	}
	if strings.Contains(fmt.Sprintf("%+v", st), obsFakeToken) {
		t.Errorf("State holds a token")
	}
}

// TestObserveRecordsThePingedURLWithoutUserinfo pins that a url holding an
// '@' is never stored in ServerState: it is not pinged at all (the check
// reads as not made) and is recorded as the fixed hiddenURL, whole (no host
// kept).
func TestObserveRecordsThePingedURLWithoutUserinfo(t *testing.T) {
	f := obsConfigured(t)
	f.configFile.Table.Mode = "remote"
	f.configFile.Table.URL = "https://me:pw-SECRET@gpu.example:8787/"
	st := Observe(context.Background(), f.Env(), ObserveOptions{})
	if st.Server.Checked || st.Server.URL != hiddenURL || f.called("Net.Ping") {
		t.Errorf("Server = %+v, calls %q", st.Server, f.calls)
	}
	if strings.Contains(fmt.Sprintf("%+v", st.Server), "pw-SECRET") {
		t.Errorf("ServerState holds the url's password")
	}
}

// TestObserveStripsUserinfoFromTheConfiguredURL pins that a [model-server]
// url holding an '@' never reaches State.Config.Table (the tab draws that
// table): it is replaced whole by hiddenURL, host included; that it is
// reported as a problem the way an inline token is, and that it is not
// pinged (a failed ping's text could quote the url, or all of it when malformed).
func TestObserveStripsUserinfoFromTheConfiguredURL(t *testing.T) {
	f := obsConfigured(t)
	f.configFile.Table.Mode = "remote"
	f.configFile.Table.URL = "https://me:pw-SECRET@gpu.example:8787/"
	st := Observe(context.Background(), f.Env(), ObserveOptions{})
	if st.Config.Table.URL != hiddenURL {
		t.Errorf("Config.Table.URL = %q, want %q", st.Config.Table.URL, hiddenURL)
	}
	if strings.Contains(fmt.Sprintf("%+v", st), "pw-SECRET") {
		t.Errorf("State holds the url's password")
	}
	found := false
	for _, p := range st.Config.Problems {
		if strings.Contains(p, "credentials") {
			found = true
		}
	}
	if !found {
		t.Errorf("Problems = %q, want one about the url's credentials", st.Config.Problems)
	}
	if st.Server.Checked || f.called("Net.Ping") {
		t.Errorf("Server = %+v, calls %q, want the url not pinged", st.Server, f.calls)
	}
}

// TestObservePingsTheEnteredEndpoint pins ObserveOptions.Check: when the tab
// asks about a url the user typed, Observe pings that url (not the stored
// one) with the token the user entered (pasted, else read from the typed
// token file), so a remote plan's check gate is about the server the user
// named. Without an entered token nothing is pinged and the reason is said;
// no State field holds the entered token. An entered url with an '@' is
// pinged neither (TestObserveNeverPingsAnAtURL has the full set), nor is the
// stored one in its place.
func TestObservePingsTheEnteredEndpoint(t *testing.T) {
	f := obsConfigured(t)
	st := Observe(context.Background(), f.Env(), ObserveOptions{
		CheckURL: "https://gpu.example:8787", CheckToken: Secret("  tok-ENTERED  "),
	})
	if !f.called("Net.Ping https://gpu.example:8787") || f.called("Net.Ping http://127.0.0.1:8787") {
		t.Errorf("calls = %q, want the entered url pinged as typed and not the stored one", f.calls)
	}
	if len(f.pingTokens) != 1 || f.pingTokens[0] != "tok-ENTERED" {
		t.Errorf("ping tokens = %q, want the pasted token, trimmed", f.pingTokens)
	}
	if !st.Server.Checked || st.Server.URL != "https://gpu.example:8787" {
		t.Errorf("Server = %+v, want the entered url recorded", st.Server)
	}
	if s := fmt.Sprintf("%+v", st); strings.Contains(s, "tok-ENTERED") {
		t.Errorf("State holds the entered token")
	}

	// An entered url with an '@': nothing is pinged, the stored url included.
	f = obsConfigured(t)
	st = Observe(context.Background(), f.Env(), ObserveOptions{
		CheckURL: "https://me:pw-SECRET@gpu.example:8787", CheckToken: Secret("tok-ENTERED"),
	})
	if f.called("Net.Ping") || st.Server.Checked || st.Server.URL != hiddenURL {
		t.Errorf("calls = %q Server = %+v, want no ping and the url hidden", f.calls, st.Server)
	}
	if s := fmt.Sprintf("%+v", st); strings.Contains(s, "tok-ENTERED") || strings.Contains(s, "pw-SECRET") {
		t.Errorf("State holds the entered token or the url's password")
	}

	// A typed token file is read for the ping.
	f = obsConfigured(t)
	f.files["/srv/remote.token"] = []byte("tok-FILE\n")
	f.modes["/srv/remote.token"] = 0o600
	st = Observe(context.Background(), f.Env(), ObserveOptions{CheckURL: "https://gpu.example:8787", CheckTokenFile: "/srv/remote.token"})
	if len(f.pingTokens) != 1 || f.pingTokens[0] != "tok-FILE" || !st.Server.Checked {
		t.Errorf("ping tokens = %q Server = %+v, want the typed file's token", f.pingTokens, st.Server)
	}

	// No entered token: not pinged, and the reason is said.
	f = obsConfigured(t)
	st = Observe(context.Background(), f.Env(), ObserveOptions{CheckURL: "https://gpu.example:8787"})
	if f.called("Net.Ping") || st.Server.Checked || !strings.Contains(st.Server.Message, "no token") {
		t.Errorf("calls = %q Server = %+v, want no ping and a message naming the missing token", f.calls, st.Server)
	}
}

// TestObserveDescribesTheLiteLLMInstallAndHidesParserText pins what a create
// or rotate plan reads: the litellm binary through its seam, whether
// config.yaml exists through FS, a binary plist as Binary, and a parser
// error reduced to fixed words (it can quote the document).
func TestObserveDescribesTheLiteLLMInstallAndHidesParserText(t *testing.T) {
	f := newFakeEnv(t)
	f.litellmBin = "/Users/user_name/.local/bin/litellm"
	f.files[f.configPath] = []byte("model_list: []\n")
	f.plistErr = fmt.Errorf("%s: %w", f.plistPath, &xml.SyntaxError{Msg: "invalid character entity &sk-LEAK-2;", Line: 9})
	st := Observe(context.Background(), f.Env(), ObserveOptions{})
	want := LiteLLMInstallState{Binary: f.litellmBin, ConfigPath: f.configPath, ConfigExists: true}
	if st.LiteLLMInstall != want {
		t.Errorf("LiteLLMInstall = %+v, want %+v", st.LiteLLMInstall, want)
	}
	if !st.LiteLLMPlist.Present || st.LiteLLMPlist.Binary || strings.Contains(st.LiteLLMPlist.Err, "sk-LEAK-2") || st.LiteLLMPlist.Err == "" {
		t.Errorf("LiteLLMPlist = %+v", st.LiteLLMPlist)
	}

	// No plist: the LiteLLM port is checked for a listener (create needs it).
	f = newFakeEnv(t)
	f.plistErr = &fs.PathError{Op: "open", Path: f.plistPath, Err: fs.ErrNotExist}
	f.listeners = map[int]int{4000: 4242}
	st = Observe(context.Background(), f.Env(), ObserveOptions{})
	if got := st.LiteLLMInstall; got.Port != 4000 || got.ListenerPID != 4242 || !got.ListenerChecked {
		t.Errorf("no plist: LiteLLMInstall = %+v", got)
	}
	// A plist: its env names say whether a salt key and DATABASE_URL are set,
	// and the port is not checked.
	f = newFakeEnv(t)
	f.listeners = map[int]int{4000: 4242}
	f.plistInfo = litellm.ProxyPlistInfo{Port: 4000, EnvNames: []string{"DATABASE_URL", "LITELLM_MASTER_KEY", "LITELLM_SALT_KEY"}}
	st = Observe(context.Background(), f.Env(), ObserveOptions{})
	if lp := st.LiteLLMPlist; !lp.HasSaltKey || !lp.HasDatabaseURL || !lp.HasMasterKey || st.LiteLLMInstall.ListenerChecked {
		t.Errorf("plist names: %+v %+v", lp, st.LiteLLMInstall)
	}

	f = newFakeEnv(t)
	f.plistErr = fmt.Errorf("%s is a %w; convert it", f.plistPath, litellm.ErrBinaryPlist)
	st = Observe(context.Background(), f.Env(), ObserveOptions{})
	if !st.LiteLLMPlist.Binary || st.LiteLLMInstall.ConfigExists || st.LiteLLMInstall.Binary != "" {
		t.Errorf("binary plist: %+v %+v", st.LiteLLMPlist, st.LiteLLMInstall)
	}
}

// TestObserveLiteLLMJobAndProbeGate pins that a present LiteLLM plist has its
// job's loaded state read (JobChecked, JobLoaded), and that SkipLiteLLMProbe
// leaves out the binary search and the port lookup a create plan alone uses.
func TestObserveLiteLLMJobAndProbeGate(t *testing.T) {
	f := newFakeEnv(t)
	f.plistInfo = litellm.ProxyPlistInfo{Port: 4000, EnvNames: []string{"LITELLM_MASTER_KEY"}}
	st := Observe(context.Background(), f.Env(), ObserveOptions{})
	if lp := st.LiteLLMPlist; !lp.JobChecked || lp.JobLoaded {
		t.Errorf("unloaded job: %+v, want checked and not loaded", lp)
	}
	f.jobs[litellm.ProxyLabel] = LaunchdJob{Loaded: true}
	if lp := Observe(context.Background(), f.Env(), ObserveOptions{}).LiteLLMPlist; !lp.JobLoaded {
		t.Errorf("loaded job: %+v", lp)
	}

	f = newFakeEnv(t)
	f.plistErr = &fs.PathError{Op: "open", Path: f.plistPath, Err: fs.ErrNotExist}
	f.listeners = map[int]int{4000: 4242}
	st = Observe(context.Background(), f.Env(), ObserveOptions{SkipLiteLLMProbe: true})
	if got := st.LiteLLMInstall; got.Binary != "" || got.ListenerChecked || got.ListenerPID != 0 {
		t.Errorf("skipped probe: %+v", got)
	}
}

// TestObserveCountsADanglingLiteLLMPlistLinkAsPresent pins that a LiteLLM
// plist path holding a symlink that does not resolve reads as present (with
// an error naming it), not absent, so a create plan refuses it instead of
// offering to write a new plist over the link.
func TestObserveCountsADanglingLiteLLMPlistLinkAsPresent(t *testing.T) {
	f := newFakeEnv(t)
	f.litellmBin = plLiteLLMBin
	f.files[f.configPath] = []byte("model_list: []\n")
	f.plistErr = &fs.PathError{Op: "open", Path: f.plistPath, Err: fs.ErrNotExist}
	f.dangling = map[string]bool{f.plistPath: true}
	st := Observe(context.Background(), f.Env(), ObserveOptions{})
	lp := st.LiteLLMPlist
	if !lp.Present || !strings.Contains(lp.Err, f.plistPath+" is a symlink that does not resolve") {
		t.Fatalf("dangling plist link: %+v", lp)
	}
	st.Host.ConfigHome = "/Users/user_name/.config"
	p, err := BuildPlan(st, Desired{Mode: modelclient.ModeLocal, LiteLLM: LiteLLMCreate})
	if err == nil || !strings.Contains(err.Error(), "already exists") || len(p.Steps) != 0 {
		t.Errorf("create over a dangling link: err %v, steps %d", err, len(p.Steps))
	}
}
