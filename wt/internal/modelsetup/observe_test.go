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
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelmode"
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

// TestObserveFindsTheModelBinaryBesideWtThenOnPath pins the Tools lookup
// order and that the path is reported as found (Binary, PATH and uid are the
// admin half — ToolsState; Home stays in the session half, modelmode's
// HostState).
func TestObserveFindsTheModelBinaryBesideWtThenOnPath(t *testing.T) {
	f := newFakeEnv(t)
	f.env["PATH"] = "/usr/bin"
	f.paths["wt-model"] = "/opt/bin/wt-model"
	st := Observe(context.Background(), f.Env(), ObserveOptions{})
	if st.Tools.Binary != "/opt/bin/wt-model" || st.Tools.Path != "/usr/bin" || st.Tools.UID != 501 {
		t.Errorf("PATH fallback: %+v", st.Tools)
	}
	if st.State.Host.Home != f.home {
		t.Errorf("Home = %q, want %q", st.State.Host.Home, f.home)
	}
	f.files["/Users/user_name/.local/bin/wt-model"] = []byte("x")
	if st = Observe(context.Background(), f.Env(), ObserveOptions{}); st.Tools.Binary != "/Users/user_name/.local/bin/wt-model" {
		t.Errorf("beside: %+v", st.Tools)
	}
	delete(f.paths, "wt-model")
	delete(f.files, "/Users/user_name/.local/bin/wt-model")
	if st = Observe(context.Background(), f.Env(), ObserveOptions{}); st.Tools.Binary != "" || st.Tools.BinaryErr == "" {
		t.Errorf("missing: %+v", st.Tools)
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
	st := Observe(context.Background(), f.Env(), ObserveOptions{ObserveOptions: modelmode.ObserveOptions{TokenPaths: []string{"/srv/tok", obsTokenPath}}})
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
// leaves out the binary search and the port lookup a create plan alone uses
// (the fake has a litellm binary and a listener, so an ignored skip shows).
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
	f.litellmBin = plLiteLLMBin
	st = Observe(context.Background(), f.Env(), ObserveOptions{SkipLiteLLMProbe: true})
	if got := st.LiteLLMInstall; got.Binary != "" || got.ListenerChecked || got.ListenerPID != 0 {
		t.Errorf("skipped probe: %+v", got)
	}
	if f.called("LiteLLM.Binary") || f.called("Net.ListenerPID 4000") {
		t.Errorf("skipped probe still searched: %q", f.calls)
	}
	// Without the skip the same fake is probed, so the checks above can fail.
	st = Observe(context.Background(), f.Env(), ObserveOptions{})
	if got := st.LiteLLMInstall; got.Binary != plLiteLLMBin || !got.ListenerChecked || got.ListenerPID != 4242 {
		t.Errorf("probe: %+v", got)
	}
}

// TestObserveProbesLiteLLMWhenTheResolvedModeIsLocal pins
// ProbeLiteLLMWhenLocal: with SkipLiteLLMProbe also set, the binary search
// and the port lookup still run when the mode Observe resolves is local, and
// are left out for any other mode. The configuration editor's first read is
// made before it knows the mode; without this, a config that stores local
// showed a menu that said no litellm binary was found when nobody looked.
// LiteLLMProbeSkipped answers the same rule for the caller.
func TestObserveProbesLiteLLMWhenTheResolvedModeIsLocal(t *testing.T) {
	opts := ObserveOptions{SkipLiteLLMProbe: true, ProbeLiteLLMWhenLocal: true}
	for _, c := range []struct {
		mode  string
		probe bool
	}{{"local", true}, {"remote", false}, {"native", false}} {
		f := obsConfigured(t)
		f.configFile.Table.Mode = c.mode
		f.plistErr = &fs.PathError{Op: "open", Path: f.plistPath, Err: fs.ErrNotExist}
		f.listeners = map[int]int{4000: 4242}
		f.litellmBin = plLiteLLMBin
		st := Observe(context.Background(), f.Env(), opts)
		got := st.LiteLLMInstall
		if probed := got.Binary == plLiteLLMBin && got.ListenerChecked && got.ListenerPID == 4242; probed != c.probe {
			t.Errorf("%s: install %+v, want probed %v", c.mode, got, c.probe)
		}
		if f.called("LiteLLM.Binary") != c.probe || f.called("Net.ListenerPID 4000") != c.probe {
			t.Errorf("%s: calls %q, want the probe %v", c.mode, f.calls, c.probe)
		}
		if skipped := opts.LiteLLMProbeSkipped(st.Resolution.Mode); skipped == c.probe {
			t.Errorf("%s: LiteLLMProbeSkipped = %v, want %v", c.mode, skipped, !c.probe)
		}
	}
	// Without ProbeLiteLLMWhenLocal the skip holds for local too.
	f := obsConfigured(t)
	f.litellmBin = plLiteLLMBin
	st := Observe(context.Background(), f.Env(), ObserveOptions{SkipLiteLLMProbe: true})
	if st.Resolution.Mode != "local" || st.LiteLLMInstall.Binary != "" || f.called("LiteLLM.Binary") {
		t.Errorf("skip alone, local: mode %q install %+v", st.Resolution.Mode, st.LiteLLMInstall)
	}
	if !(ObserveOptions{SkipLiteLLMProbe: true}).LiteLLMProbeSkipped(st.Resolution.Mode) || (ObserveOptions{}).LiteLLMProbeSkipped("") {
		t.Errorf("LiteLLMProbeSkipped ignores SkipLiteLLMProbe")
	}
}

// TestObserveCountsDanglingAgentPlistAndTokenLinksAsPresent pins that a
// symlink that does not resolve at wt's LaunchAgent plist or at the local
// token file reads as present with an error naming it, not absent, so the
// plan refuses it up front (naming the link) instead of planning a new plist
// or token that the write would then refuse at apply time.
func TestObserveCountsDanglingAgentPlistAndTokenLinksAsPresent(t *testing.T) {
	f := obsConfigured(t)
	delete(f.files, obsTokenPath)
	f.dangling = map[string]bool{obsAgentPath: true, obsTokenPath: true}
	st := Observe(context.Background(), f.Env(), ObserveOptions{})
	if a := st.Agent; !a.Present || a.Managed || !strings.Contains(a.Err, obsAgentPath+" is a symlink that does not resolve") {
		t.Errorf("dangling agent plist link: %+v", a)
	}
	if tk := st.Token; tk.Path != obsTokenPath || !tk.Exists || !strings.Contains(tk.Err, obsTokenPath+" is a symlink that does not resolve") {
		t.Errorf("dangling token link: %+v", tk)
	}
	st.Host.ConfigHome = "/Users/user_name/.config"
	p, err := BuildPlan(st, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal, TokenFile: obsTokenPath}})
	if err == nil || !strings.Contains(err.Error(), obsAgentPath) || !strings.Contains(err.Error(), obsTokenPath) || len(p.Steps) != 0 {
		t.Errorf("plan over dangling links: err %v, steps %d", err, len(p.Steps))
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
	p, err := BuildPlan(st, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}, AdminWant: AdminWant{LiteLLM: LiteLLMCreate}})
	if err == nil || !strings.Contains(err.Error(), "already exists") || len(p.Steps) != 0 {
		t.Errorf("create over a dangling link: err %v, steps %d", err, len(p.Steps))
	}
}

// TestObservePassesTheAgentAsTheLocalFallback pins the handoff: modelsetup
// reads the agent plist and hands its token file and listen address to
// modelmode.Observe as the local fallback, so a local-mode read with nothing
// stored pings the agent's listen with the agent's token.
func TestObservePassesTheAgentAsTheLocalFallback(t *testing.T) {
	f := obsConfigured(t)
	f.configFile.Table.URL = ""
	f.configFile.Table.TokenFile = ""
	f.files[obsAgentPath] = RenderAgentPlist(AgentSpec{Binary: "/b/wt-model", Listen: "127.0.0.1:9911", TokenFile: "/a/tok", Path: "/bin", Home: "/h"})
	f.files["/a/tok"] = []byte("tok-AGENT\n")
	f.modes["/a/tok"] = 0o600
	st := Observe(context.Background(), f.Env(), ObserveOptions{})
	if !f.called("Net.Ping http://127.0.0.1:9911") {
		t.Errorf("calls = %q, want the agent's listen pinged", f.calls)
	}
	if len(f.pingTokens) != 1 || f.pingTokens[0] != "tok-AGENT" {
		t.Errorf("ping tokens = %q, want the agent's token", f.pingTokens)
	}
	if st.Agent.Listen != "127.0.0.1:9911" {
		t.Errorf("Agent.Listen = %q", st.Agent.Listen)
	}
}
