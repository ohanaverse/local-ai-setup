package modelsetup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelmode"
	"github.com/ohanaverse/local-ai-setup/wt/internal/urlrule"
)

const (
	plHome       = "/Users/user_name"
	plConfigHome = plHome + "/.config"
	plCfgPath    = plConfigHome + "/agent-wt/config.toml"
	plLocalTok   = plConfigHome + "/wt-model/server.token"
	plRemoteTok  = plConfigHome + "/agent-wt/remote-model-server.token"
	plAgentPath  = plHome + "/Library/LaunchAgents/local.wt-model.serve.plist"
	plBinary     = plHome + "/.local/bin/wt-model"
	plPATH       = "/opt/homebrew/bin:/usr/bin:/bin"
	plLiteLLM    = plHome + "/Library/LaunchAgents/local.litellm.proxy.plist"
	plPasted     = "tok-PASTED-SECRET-42"
	plMasterKey  = "sk-ENTERED-SECRET-77"
)

// plSpec is the agent spec a local plan with default inputs renders.
func plSpec() AgentSpec {
	return AgentSpec{Binary: plBinary, Listen: DefaultListen, TokenFile: plLocalTok, Path: plPATH, Home: plHome}
}

// baseState is a macOS host where local mode is fully applied: the stored
// table, a 0600 token, a set pair, a loaded and running managed agent whose
// plist equals what a plan would render, and our own listener on 8787.
func baseState() State {
	return State{
		Platform: Platform{OK: true},
		State: modelmode.State{
			Config: ConfigState{Path: plCfgPath, Exists: true, Table: config.ModelServerState{
				Mode: "local", URL: "http://" + DefaultListen, TokenFile: plLocalTok,
			}},
			Token:      TokenState{Path: plLocalTok, Exists: true},
			TokenFiles: []TokenState{{Path: plRemoteTok}},
			Server:     ServerState{Checked: true, OK: true, API: 1, Schema: 1, URL: "http://" + DefaultListen},
			Host:       modelmode.HostState{Home: plHome, ConfigHome: plConfigHome},
		},
		Pair: PairState{URL: "http://localhost:4000", KeySet: true},
		Agent: AgentState{
			PlistPath: plAgentPath, Present: true, Managed: true,
			Job:    LaunchdJob{Loaded: true, Running: true, PID: 4123},
			Listen: DefaultListen, Binary: plBinary, TokenFile: plLocalTok,
			Content: RenderAgentPlist(plSpec()),
		},
		Listener:     ListenerState{Port: 8787, PID: 4123, Ours: true},
		Tools:        ToolsState{Binary: plBinary, Path: plPATH, UID: 501},
		LiteLLMPlist: LiteLLMPlistState{Path: plLiteLLM, Present: true, Port: 4000, Mode: 0o600, HasMasterKey: true},
	}
}

// freshState is the same host before anything was set up: no config.toml,
// no token, no pair, no agent and nothing listening, with a LiteLLM plist that
// holds a master key.
func freshState() State {
	st := baseState()
	st.Config = ConfigState{Path: plCfgPath}
	st.Token = TokenState{}
	st.TokenFiles = []TokenState{{Path: plLocalTok}, {Path: plRemoteTok}}
	st.Pair = PairState{}
	st.Agent = AgentState{PlistPath: plAgentPath}
	st.Listener = ListenerState{Port: 8787}
	st.Server = ServerState{}
	return st
}

func kinds(p Plan) []StepKind {
	var k []StepKind
	for _, s := range p.Steps {
		k = append(k, s.Kind)
	}
	return k
}

func sameKinds(got []StepKind, want ...StepKind) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func stepOf(p Plan, k StepKind) (Step, bool) {
	for _, s := range p.Steps {
		if s.Kind == k {
			return s, true
		}
	}
	return Step{}, false
}

// mustRefuse asserts a refusal: a *RefusalError, no steps, and a reason that
// contains every one of wants.
func mustRefuse(t *testing.T, p Plan, err error, wants ...string) {
	t.Helper()
	var re *RefusalError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v, want *RefusalError", err)
	}
	if len(p.Steps) != 0 {
		t.Errorf("refused plan has steps: %v", kinds(p))
	}
	for _, w := range wants {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("refusal %q does not contain %q", err.Error(), w)
		}
	}
	if !strings.Contains(err.Error(), "nothing was changed") {
		t.Errorf("refusal %q does not say nothing was changed", err.Error())
	}
}

func mustPlan(t *testing.T, st State, want Desired) Plan {
	t.Helper()
	p, err := BuildPlan(st, want)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if n := len(p.Steps); n > 0 && p.Steps[n-1].Kind != StepConfig {
		for _, s := range p.Steps[:n-1] {
			if s.Kind == StepConfig {
				t.Errorf("config step is not last: %v", kinds(p))
			}
		}
	}
	text := p.Format()
	js, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("json.Marshal(plan): %v", err)
	}
	dump := fmt.Sprintf("%v %+v %#v %s", p, p, p, js)
	for _, secret := range []string{plPasted, plMasterKey} {
		if strings.Contains(dump, secret) {
			t.Errorf("printed or marshalled plan holds a secret: %s", dump)
		}
		for _, s := range p.Steps {
			if strings.Contains(s.Detail+s.Path+s.Value, secret) {
				t.Errorf("step %s holds a secret", s.Kind)
			}
		}
		if strings.Contains(text, secret) {
			t.Errorf("Format holds a secret:\n%s", text)
		}
	}
	return p
}

// TestBuildPlanNativeIsOnlyTheConfigStep pins that choosing native from a
// local setup writes the table and nothing else: the agent stays installed.
func TestBuildPlanNativeIsOnlyTheConfigStep(t *testing.T) {
	p := mustPlan(t, baseState(), Desired{Desired: modelmode.Desired{Mode: modelclient.ModeNative}})
	if !sameKinds(kinds(p), StepConfig) {
		t.Fatalf("steps = %v, want [config]", kinds(p))
	}
	if p.Want.URL != "" || p.Want.TokenFile != "" {
		t.Errorf("native stores only the mode: %+v", p.Want)
	}
	if !strings.Contains(p.Format(), "left installed") {
		t.Errorf("Format does not say the agent is kept:\n%s", p.Format())
	}
	st := baseState()
	st.Config.Table = config.ModelServerState{Mode: "native"}
	if p = mustPlan(t, st, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeNative}}); len(p.Steps) != 0 {
		t.Errorf("already native: steps = %v", kinds(p))
	}
}

// TestBuildPlanNativeOffersToRemoveAManagedAgent pins the optional agent
// removal: bootout and remove the plist before the config, and only for a
// plist wt manages.
func TestBuildPlanNativeOffersToRemoveAManagedAgent(t *testing.T) {
	p := mustPlan(t, baseState(), Desired{Desired: modelmode.Desired{Mode: modelclient.ModeNative}, AdminWant: AdminWant{RemoveAgent: true}})
	if !sameKinds(kinds(p), StepBootout, StepRemovePlist, StepConfig) {
		t.Fatalf("steps = %v", kinds(p))
	}
	if s, _ := stepOf(p, StepRemovePlist); s.Path != plAgentPath {
		t.Errorf("remove path = %q", s.Path)
	}
	st := baseState()
	st.Agent.Managed = false
	p = mustPlan(t, st, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeNative}, AdminWant: AdminWant{RemoveAgent: true}})
	if !sameKinds(kinds(p), StepConfig) {
		t.Errorf("unmanaged plist must be left alone: %v", kinds(p))
	}
}

// TestBuildPlanLeavingLocalNeverRemovesTheTokenOrThePair pins that no step of
// a native or remote plan touches the local token file or the LiteLLM pair,
// and that the text says they are kept.
func TestBuildPlanLeavingLocalNeverRemovesTheTokenOrThePair(t *testing.T) {
	st := baseState()
	st.Server = ServerState{}
	for _, want := range []Desired{{Desired: modelmode.Desired{Mode: modelclient.ModeNative}, AdminWant: AdminWant{RemoveAgent: true}}, {Desired: modelmode.Desired{Mode: modelclient.ModeRemote, URL: "https://gpu.example:8787", PastedToken: plPasted}, AdminWant: AdminWant{RemoveAgent: true}}} {
		p := mustPlan(t, st, want)
		for _, s := range p.Steps {
			if s.Kind == StepPair || s.Path == plLocalTok {
				t.Errorf("%s plan touches the token or pair: %+v", want.Mode, s)
			}
		}
		text := p.Format()
		if !strings.Contains(text, plLocalTok) || !strings.Contains(text, "pair") {
			t.Errorf("%s plan does not say the token and pair are kept:\n%s", want.Mode, text)
		}
	}
}

// TestBuildPlanLocalStepsInOrderWithConfigLast pins the local order on a
// fresh host: token, pair, plist, bootstrap, wait, config.
func TestBuildPlanLocalStepsInOrderWithConfigLast(t *testing.T) {
	p := mustPlan(t, freshState(), Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}})
	if !sameKinds(kinds(p), StepToken, StepPair, StepPlist, StepBootstrap, StepWait, StepConfig) {
		t.Fatalf("steps = %v", kinds(p))
	}
	if p.Agent == nil || *p.Agent != plSpec() {
		t.Errorf("Agent = %+v, want %+v", p.Agent, plSpec())
	}
	if s, _ := stepOf(p, StepToken); s.Action != ActionGenerateToken || s.Path != plLocalTok {
		t.Errorf("token step = %+v", s)
	}
	if p.Want.URL != "http://127.0.0.1:8787" || p.Want.TokenFile != plLocalTok || p.Admin.Listen != DefaultListen {
		t.Errorf("Want = %+v", p.Want)
	}
	// A loaded job whose plist changes is booted out first.
	st := baseState()
	st.Listener = ListenerState{Port: 8788}
	p = mustPlan(t, st, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}, AdminWant: AdminWant{Listen: "127.0.0.1:8788"}})
	if !sameKinds(kinds(p), StepPlist, StepBootout, StepBootstrap, StepWait, StepConfig) {
		t.Errorf("port change: steps = %v", kinds(p))
	}
}

// TestBuildPlanLocalReusesAnExistingPairAndToken pins that a valid token and
// a set pair are kept, with no step for either.
func TestBuildPlanLocalReusesAnExistingPairAndToken(t *testing.T) {
	st := freshState()
	st.Token = TokenState{Path: plLocalTok, Exists: true}
	st.TokenFiles = []TokenState{{Path: plRemoteTok}}
	st.Pair = PairState{URL: "http://localhost:4000", KeySet: true}
	p := mustPlan(t, st, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}})
	if !sameKinds(kinds(p), StepPlist, StepBootstrap, StepWait, StepConfig) {
		t.Fatalf("steps = %v", kinds(p))
	}
	if text := p.Format(); !strings.Contains(text, "keep the token file "+plLocalTok) || !strings.Contains(text, "keep wt-model's LiteLLM pair") {
		t.Errorf("Format does not say what is kept:\n%s", text)
	}
}

// TestBuildPlanLocalTakesThePairFromTheLiteLLMPlist pins the pair's url from
// the plist's --port (4000 when absent) and that the key is only a marker to
// read at apply time.
func TestBuildPlanLocalTakesThePairFromTheLiteLLMPlist(t *testing.T) {
	st := freshState()
	st.LiteLLMPlist.Port = 4111
	p := mustPlan(t, st, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}, AdminWant: AdminWant{MasterKey: plMasterKey}})
	s, _ := stepOf(p, StepPair)
	if s.Action != ActionPairKeyFromPlist || s.Value != "http://localhost:4111" || s.Path != plLiteLLM {
		t.Errorf("pair step = %+v", s)
	}
	st.LiteLLMPlist.Port = 0
	p = mustPlan(t, st, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}})
	if s, _ = stepOf(p, StepPair); s.Value != "http://localhost:4000" {
		t.Errorf("default port: %+v", s)
	}
	if !strings.Contains(p.Format(), "LITELLM_MASTER_KEY") {
		t.Errorf("Format does not say where the key comes from:\n%s", p.Format())
	}
}

// TestBuildPlanLocalFallsBackToTheEnteredMasterKey pins that with no key in
// the LiteLLM plist the entered key is used, marked, never shown.
func TestBuildPlanLocalFallsBackToTheEnteredMasterKey(t *testing.T) {
	st := freshState()
	st.LiteLLMPlist.HasMasterKey = false
	p := mustPlan(t, st, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}, AdminWant: AdminWant{MasterKey: plMasterKey}})
	if s, _ := stepOf(p, StepPair); s.Action != ActionPairKeyEntered {
		t.Errorf("pair step = %+v", s)
	}
}

// TestBuildPlanRefusesWhenNoMasterKeyIsAvailable pins the refusal naming the
// LiteLLM guide when neither the plist nor the user supplies a key.
func TestBuildPlanRefusesWhenNoMasterKeyIsAvailable(t *testing.T) {
	st := freshState()
	st.LiteLLMPlist.HasMasterKey = false
	p, err := BuildPlan(st, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}})
	mustRefuse(t, p, err, "docs/guides/14-litellm-install-and-configuration.md")
}

// TestBuildPlanTightensALooseTokenFile pins that a loose token file is kept
// and tightened to 0600 rather than regenerated.
func TestBuildPlanTightensALooseTokenFile(t *testing.T) {
	st := baseState()
	st.Token.Loose = true
	p := mustPlan(t, st, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}})
	if !sameKinds(kinds(p), StepToken) {
		t.Fatalf("steps = %v", kinds(p))
	}
	s, _ := stepOf(p, StepToken)
	if s.Action != ActionTightenToken || !strings.Contains(s.Detail, "0600") {
		t.Errorf("token step = %+v", s)
	}
	// An empty one is regenerated, and the running server is restarted.
	st = baseState()
	st.Token.Empty = true
	p = mustPlan(t, st, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}})
	if s, _ := stepOf(p, StepToken); s.Action != ActionGenerateToken {
		t.Errorf("empty token: %+v", s)
	}
	if !sameKinds(kinds(p), StepToken, StepKickstart, StepWait) {
		t.Errorf("empty token: steps = %v", kinds(p))
	}
}

// TestBuildPlanKickstartsWhenOnlyThePairChanged pins that a loaded agent with
// an unchanged plist is kickstarted, not reloaded, when the pair changes.
func TestBuildPlanKickstartsWhenOnlyThePairChanged(t *testing.T) {
	st := baseState()
	st.Pair = PairState{}
	p := mustPlan(t, st, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}})
	if !sameKinds(kinds(p), StepPair, StepKickstart, StepWait) {
		t.Errorf("steps = %v", kinds(p))
	}
}

// TestBuildPlanRefusesAForeignListener pins that a port held by another
// process refuses naming the pid and offering another port.
func TestBuildPlanRefusesAForeignListener(t *testing.T) {
	st := freshState()
	st.Listener = ListenerState{Port: 8787, PID: 999}
	p, err := BuildPlan(st, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}})
	mustRefuse(t, p, err, "pid 999", "8787", "another port")
	// A port nobody checked is refused too.
	p, err = BuildPlan(freshState(), Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}, AdminWant: AdminWant{Listen: "127.0.0.1:9000"}})
	mustRefuse(t, p, err, "9000", "not checked")
}

// TestBuildPlanRefusesAnUnmarkedPlist pins that a plist at the agent path
// without wt's marker is never overwritten.
func TestBuildPlanRefusesAnUnmarkedPlist(t *testing.T) {
	st := freshState()
	st.Agent.Present = true
	p, err := BuildPlan(st, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}})
	mustRefuse(t, p, err, plAgentPath, "managed by wt")
}

// TestBuildPlanOverwritesAMarkedPlist pins that a managed plist with other
// arguments is rewritten and the job reloaded.
func TestBuildPlanOverwritesAMarkedPlist(t *testing.T) {
	st := baseState()
	st.Agent.Content = RenderAgentPlist(AgentSpec{Binary: "/old/wt-model", Listen: DefaultListen, TokenFile: plLocalTok, Path: "/bin", Home: plHome})
	st.Agent.Binary = "/old/wt-model"
	p := mustPlan(t, st, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}})
	if !sameKinds(kinds(p), StepPlist, StepBootout, StepBootstrap, StepWait) {
		t.Fatalf("steps = %v", kinds(p))
	}
	if !strings.Contains(p.Format(), "overwrite") {
		t.Errorf("Format does not say overwrite:\n%s", p.Format())
	}
}

// TestBuildPlanRefusesWhenWtModelIsMissingAndNamesMakeInstall pins the
// refusal when no wt-model binary was found.
func TestBuildPlanRefusesWhenWtModelIsMissingAndNamesMakeInstall(t *testing.T) {
	st := freshState()
	st.Tools.Binary, st.Tools.BinaryErr = "", "wt-model was not found beside wt or on PATH"
	p, err := BuildPlan(st, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}})
	mustRefuse(t, p, err, "not found", "make install")
}

// TestBuildPlanLocalIsEmptyOfWorkWhenAlreadyApplied pins idempotence: an
// applied local setup plans nothing, or only the config step when the stored
// table differs.
func TestBuildPlanLocalIsEmptyOfWorkWhenAlreadyApplied(t *testing.T) {
	p := mustPlan(t, baseState(), Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}})
	if len(p.Steps) != 0 {
		t.Errorf("steps = %v, want none", kinds(p))
	}
	if !strings.Contains(p.Format(), "nothing to change") {
		t.Errorf("Format:\n%s", p.Format())
	}
	st := baseState()
	st.Config.Table.Mode = ""
	if p = mustPlan(t, st, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}}); !sameKinds(kinds(p), StepConfig) {
		t.Errorf("table differs: steps = %v", kinds(p))
	}
	// A ~ path in the table is the same file.
	st = baseState()
	st.Config.Table.TokenFile = "~/.config/wt-model/server.token"
	if p = mustPlan(t, st, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}}); len(p.Steps) != 0 {
		t.Errorf("~ token_file: steps = %v", kinds(p))
	}
}

// TestBuildPlanRefusesLocalOffMacOS pins that local refuses with the
// platform's reason where there is no launchd.
func TestBuildPlanRefusesLocalOffMacOS(t *testing.T) {
	st := freshState()
	st.Platform = Platform{Reason: "the local model server runs as a macOS LaunchAgent; this is linux"}
	p, err := BuildPlan(st, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}})
	mustRefuse(t, p, err, "linux")
}

// goldenPlans are the three plans whose text is pinned by a golden file.
func goldenPlans(t *testing.T) map[string]Plan {
	remote := freshState()
	remote.Agent = baseState().Agent
	remote.TokenFiles = []TokenState{{Path: plLocalTok}, {Path: plRemoteTok, Exists: true}}
	return map[string]Plan{
		"plan_local.golden":          mustPlan(t, freshState(), Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}}),
		"plan_remote.golden":         mustPlan(t, remote, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeRemote, URL: "https://gpu.example:8787", PastedToken: plPasted}, AdminWant: AdminWant{RemoveAgent: true}}),
		"plan_native.golden":         mustPlan(t, baseState(), Desired{Desired: modelmode.Desired{Mode: modelclient.ModeNative}}),
		"plan_litellm_create.golden": mustPlan(t, createState(), Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}, AdminWant: AdminWant{LiteLLM: LiteLLMCreate}}),
		"plan_litellm_rotate.golden": mustPlan(t, rotateState(), Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}, AdminWant: AdminWant{LiteLLM: LiteLLMRotateKey}}),
	}
}

// TestFormatMatchesTheGoldens pins the exact confirm text of a fresh local,
// a remote with a replaced token and agent removal, and native from local, so
// any wording change is reviewed. Run with UPDATE_GOLDEN=1 to regenerate.
func TestFormatMatchesTheGoldens(t *testing.T) {
	for name, p := range goldenPlans(t) {
		got := p.Format()
		path := "testdata/" + name
		if os.Getenv("UPDATE_GOLDEN") != "" {
			if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if got != string(want) {
			t.Errorf("%s differs:\n%s", name, got)
		}
		for i, r := range got {
			if r > 0x7e || (r < 0x20 && r != '\n') {
				t.Errorf("%s: non-ASCII or control rune %q at %d", name, r, i)
				break
			}
		}
		for _, line := range strings.Split(strings.TrimSpace(got), "\n") {
			if l := strings.TrimLeft(line, " -0123456789."); l != "" && l[0] >= 'A' && l[0] <= 'Z' {
				t.Errorf("%s: line leads upper-case: %q", name, line)
			}
		}
	}
}

// TestFormatIsDeterministic pins that the same plan formats byte-identically
// twice, so the text confirmed is the text shown.
func TestFormatIsDeterministic(t *testing.T) {
	for name, p := range goldenPlans(t) {
		if a, b := p.Format(), p.Format(); a != b {
			t.Errorf("%s: two calls differ", name)
		}
	}
}

// TestBuildPlanKeepsEachSecretToItsMode pins that a plan carries the master
// key only in local mode and the pasted token only in remote mode, so a
// secret typed into the other mode's field never rides along.
func TestBuildPlanKeepsEachSecretToItsMode(t *testing.T) {
	st := freshState()
	in := Desired{Desired: modelmode.Desired{URL: "https://gpu.example:8787", PastedToken: plPasted}, AdminWant: AdminWant{MasterKey: plMasterKey}}
	in.Mode = modelclient.ModeNative
	if p := mustPlan(t, st, in); p.Want.PastedToken != "" || p.Admin.MasterKey != "" {
		t.Errorf("native keeps a secret")
	}
	in.Mode = modelclient.ModeLocal
	if p := mustPlan(t, st, in); p.Want.PastedToken != "" || p.Admin.MasterKey.Reveal() != plMasterKey {
		t.Errorf("local: pasted kept or key lost")
	}
	in.Mode = modelclient.ModeRemote
	if p := mustPlan(t, st, in); p.Admin.MasterKey != "" || p.Want.PastedToken.Reveal() != plPasted {
		t.Errorf("remote: key kept or pasted lost")
	}
}

// TestObserveAndPlanFlagACredentialedURLThatWillBeRewritten runs the real
// Observe and the real BuildPlan end to end: a stored url with a
// user:password sets ConfigState.URLHadCredentials (beside the Problems line
// a person reads) and shows as urlrule.Hidden. A remote plan that keeps the url
// as the State shows it is refused (the placeholder holds an '@'), so the
// placeholder is never written; one with a retyped url writes that url over
// the stored one (Plan.WritesURL), so its credentials are not kept. Those
// facts are what the tab's note reads, so neither may depend on wording.
func TestObserveAndPlanFlagACredentialedURLThatWillBeRewritten(t *testing.T) {
	f := obsConfigured(t)
	f.configFile.Table.Mode = "remote"
	f.configFile.Table.URL = "https://me:pw-SECRET@gpu.example:8787/"
	st := Observe(context.Background(), f.Env(), ObserveOptions{})
	if !st.Config.URLHadCredentials || st.Config.Table.URL != urlrule.Hidden {
		t.Fatalf("URLHadCredentials = %t, Table.URL = %q for a url with userinfo", st.Config.URLHadCredentials, st.Config.Table.URL)
	}
	p, err := BuildPlan(st, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeRemote, URL: st.Config.Table.URL, PastedToken: "tok-NEW"}})
	mustRefuse(t, p, err, "user:password", "token file")
	p, err = BuildPlan(st, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeRemote, URL: "https://gpu.example:8787/", PastedToken: "tok-NEW", SaveUnchecked: true}})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if !p.WritesURL() {
		t.Errorf("a retyped url rewrites the table, yet WritesURL is false:\n%s", p.Format())
	}
	f = obsConfigured(t)
	if st = Observe(context.Background(), f.Env(), ObserveOptions{}); st.Config.URLHadCredentials {
		t.Errorf("URLHadCredentials = true for a plain url")
	}
}

const (
	plLiteLLMBin = plHome + "/.local/bin/litellm"
	plLiteLLMCfg = plHome + "/.config/litellm/config.yaml"
)

// createState is a fresh host where LiteLLM is installed (binary and
// config.yaml) but has no LaunchAgent plist yet, and wt-model's pair is unset.
func createState() State {
	st := freshState()
	st.LiteLLMPlist = LiteLLMPlistState{Path: plLiteLLM}
	st.LiteLLMInstall = LiteLLMInstallState{Binary: plLiteLLMBin, ConfigPath: plLiteLLMCfg, ConfigExists: true,
		Port: 4000, ListenerChecked: true}
	return st
}

// rotateState is a fully applied local host whose LiteLLM plist holds a
// master key and is readable by others (0644).
func rotateState() State {
	st := baseState()
	st.LiteLLMPlist.Mode = 0o644
	st.LiteLLMPlist.HasSaltKey = true
	st.LiteLLMInstall = LiteLLMInstallState{Binary: plLiteLLMBin, ConfigPath: plLiteLLMCfg, ConfigExists: true}
	return st
}

// TestBuildPlanCreateRefusesWhenLiteLLMIsNotInstalled pins "wt never installs
// LiteLLM": with no litellm binary, or no config.yaml, a create request is
// refused naming guide 14 and what is missing, and no step is planned.
func TestBuildPlanCreateRefusesWhenLiteLLMIsNotInstalled(t *testing.T) {
	st := createState()
	st.LiteLLMInstall.Binary = ""
	p, err := BuildPlan(st, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}, AdminWant: AdminWant{LiteLLM: LiteLLMCreate}})
	mustRefuse(t, p, err, "litellm binary", guide14)

	st = createState()
	st.LiteLLMInstall.ConfigExists = false
	p, err = BuildPlan(st, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}, AdminWant: AdminWant{LiteLLM: LiteLLMCreate}})
	mustRefuse(t, p, err, plLiteLLMCfg, guide14)
}

// TestBuildPlanCreateOnlyWhenAbsent pins that create never replaces a LiteLLM
// plist: one that exists (with a key, or unreadable and not wt's) refuses;
// with none, the plan writes it, loads it, waits for LiteLLM, then sets the
// pair before the wt-model agent and the config, and says what it omits.
func TestBuildPlanCreateOnlyWhenAbsent(t *testing.T) {
	present := createState()
	present.LiteLLMPlist = LiteLLMPlistState{Path: plLiteLLM, Present: true, Port: 4000, Mode: 0o600, HasMasterKey: true}
	p, err := BuildPlan(present, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}, AdminWant: AdminWant{LiteLLM: LiteLLMCreate}})
	mustRefuse(t, p, err, plLiteLLM, "already exists", guide14)

	unmanaged := createState()
	unmanaged.LiteLLMPlist = LiteLLMPlistState{Path: plLiteLLM, Present: true, Err: "cannot parse"}
	p, err = BuildPlan(unmanaged, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}, AdminWant: AdminWant{LiteLLM: LiteLLMCreate}})
	mustRefuse(t, p, err, plLiteLLM, "already exists")

	p = mustPlan(t, createState(), Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}, AdminWant: AdminWant{LiteLLM: LiteLLMCreate}})
	if !sameKinds(kinds(p), StepToken, StepLiteLLMPlist, StepBootstrap, StepWait, StepPair, StepPlist, StepBootstrap, StepWait, StepConfig) {
		t.Fatalf("steps = %v", kinds(p))
	}
	if p.LiteLLM == nil {
		t.Fatalf("Plan.LiteLLM is nil for create")
	}
	want := LiteLLMSpec{Binary: plLiteLLMBin, ConfigPath: plLiteLLMCfg, Path: plPATH, Home: plHome, Port: 4000}
	if *p.LiteLLM != want {
		t.Errorf("LiteLLM spec = %+v, want %+v (no key at plan time)", *p.LiteLLM, want)
	}
	if s := p.Steps[1]; s.Path != plLiteLLM {
		t.Errorf("plist step = %+v", s)
	}
	if s := p.Steps[2]; s.Path != plLiteLLM {
		t.Errorf("LiteLLM bootstrap = %+v", s)
	}
	if s := p.Steps[3]; s.Action != ActionWaitLiteLLM || s.Value != "http://localhost:4000" {
		t.Errorf("LiteLLM wait = %+v", s)
	}
	if s, _ := stepOf(p, StepPair); s.Action != ActionPairKeyNew || s.Value != "http://localhost:4000" {
		t.Errorf("pair step = %+v", s)
	}
	text := p.Format()
	for _, w := range []string{"DATABASE_URL", "UI login", "provider keys", guide14, "0600"} {
		if !strings.Contains(text, w) {
			t.Errorf("Format does not say %q:\n%s", w, text)
		}
	}
	// A plan that does not create carries no spec.
	if p = mustPlan(t, freshState(), Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}}); p.LiteLLM != nil {
		t.Errorf("Plan.LiteLLM set without create")
	}
}

// TestBuildPlanRotateOrdersBackupEditPairKickstart pins the rotation order:
// backup, edit (which writes the file without group or other bits), pair with the
// same new key, then restart LiteLLM and then wt-model. The plan names what
// else may hold the old key and what it never changes.
func TestBuildPlanRotateOrdersBackupEditPairKickstart(t *testing.T) {
	p := mustPlan(t, rotateState(), Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}, AdminWant: AdminWant{LiteLLM: LiteLLMRotateKey}})
	if !sameKinds(kinds(p), StepBackup, StepMasterKey, StepPair, StepKickstartLiteLLM, StepWait, StepKickstart, StepWait) {
		t.Fatalf("steps = %v", kinds(p))
	}
	if s := p.Steps[4]; s.Action != ActionWaitLiteLLM || s.Value != "http://localhost:4000" {
		t.Errorf("LiteLLM wait after the restart = %+v", s)
	}
	for _, k := range []StepKind{StepBackup, StepMasterKey} {
		if s, _ := stepOf(p, k); s.Path != plLiteLLM {
			t.Errorf("%s path = %q", k, s.Path)
		}
	}
	if s, _ := stepOf(p, StepPair); s.Action != ActionPairKeyNew || s.Value != "http://localhost:4000" {
		t.Errorf("pair step = %+v", s)
	}
	if p.LiteLLM != nil {
		t.Errorf("rotate carries a create spec")
	}
	text := p.Format()
	for _, w := range []string{"old key", plCfgPath, "LITELLM_SALT_KEY", "DATABASE_URL", "launchctl bootout gui/501/local.litellm.proxy", "launchctl bootstrap gui/501 " + plLiteLLM} {
		if !strings.Contains(text, w) {
			t.Errorf("Format does not say %q:\n%s", w, text)
		}
	}
	st := rotateState()
	st.LiteLLMPlist.Mode = 0o600
	p = mustPlan(t, st, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}, AdminWant: AdminWant{LiteLLM: LiteLLMRotateKey}})
	if !sameKinds(kinds(p), StepBackup, StepMasterKey, StepPair, StepKickstartLiteLLM, StepWait, StepKickstart, StepWait) {
		t.Errorf("0600 plist: steps = %v", kinds(p))
	}
}

// TestBuildPlanRotateRefusesWithoutAReadableXMLPlist pins that rotation needs
// an existing XML plist holding a master key: absent, binary, unreadable or
// keyless each refuse naming the file and guide 14, and a binary one names
// the conversion. A raw parser message never reaches the refusal.
func TestBuildPlanRotateRefusesWithoutAReadableXMLPlist(t *testing.T) {
	cases := map[string]struct {
		plist LiteLLMPlistState
		wants []string
	}{
		"absent":     {LiteLLMPlistState{Path: plLiteLLM}, []string{plLiteLLM, guide14}},
		"binary":     {LiteLLMPlistState{Path: plLiteLLM, Present: true, Binary: true, Err: "bplist"}, []string{plLiteLLM, "plutil -convert xml1", guide14}},
		"unreadable": {LiteLLMPlistState{Path: plLiteLLM, Present: true, Err: "XML syntax error on line 3: invalid character entity &sk-LEAK;"}, []string{plLiteLLM, guide14}},
		"no key":     {LiteLLMPlistState{Path: plLiteLLM, Present: true, Mode: 0o600}, []string{plLiteLLM, "LITELLM_MASTER_KEY", guide14}},
		// The restart is kickstart -k, which fails on a job that is not loaded.
		"not loaded": {LiteLLMPlistState{Path: plLiteLLM, Present: true, Mode: 0o600, HasMasterKey: true, JobChecked: true}, []string{"is not loaded", "launchctl bootstrap gui/501 " + plLiteLLM}},
	}
	for name, c := range cases {
		st := rotateState()
		st.LiteLLMPlist = c.plist
		p, err := BuildPlan(st, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}, AdminWant: AdminWant{LiteLLM: LiteLLMRotateKey}})
		mustRefuse(t, p, err, c.wants...)
		if err != nil && strings.Contains(err.Error(), "sk-LEAK") {
			t.Errorf("%s: refusal quotes the parser: %v", name, err)
		}
	}
}

// TestBuildPlanUseExistingKeyIsTheDefault pins that LiteLLMUseExistingKey
// plans byte-identically to the zero value where the pair is not set (the
// key is read from the plist into the pair), and that it never edits the
// plist anywhere. Over a set pair it differs on purpose, by re-setting the
// pair (TestBuildPlanUseExistingKeyRePairsOverASetPair).
func TestBuildPlanUseExistingKeyIsTheDefault(t *testing.T) {
	for _, st := range []State{freshState()} {
		a := mustPlan(t, st, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}})
		b := mustPlan(t, st, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}, AdminWant: AdminWant{LiteLLM: LiteLLMUseExistingKey}})
		if a.Format() != b.Format() || fmt.Sprint(kinds(a)) != fmt.Sprint(kinds(b)) {
			t.Errorf("use-existing differs from the default:\n%s\n---\n%s", a.Format(), b.Format())
		}
	}
	for _, st := range []State{freshState(), baseState(), rotateState()} {
		for _, s := range mustPlan(t, st, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}, AdminWant: AdminWant{LiteLLM: LiteLLMUseExistingKey}}).Steps {
			if s.Kind == StepMasterKey || s.Kind == StepLiteLLMPlist || s.Kind == StepBackup {
				t.Errorf("use-existing edits the plist")
			}
		}
	}
	p, err := BuildPlan(freshState(), Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}, AdminWant: AdminWant{LiteLLM: "reinstall"}})
	mustRefuse(t, p, err, "reinstall")
}

// TestBuildPlanCreateValidatesTheSpecBeforeAnyStep pins carry-forward 3: a
// path or value with a line break or a character XML 1.0 cannot carry, or a
// port outside 1..65535, refuses with *RefusalError before any step, so
// RenderLiteLLMPlist never writes a broken or injected plist.
func TestBuildPlanCreateValidatesTheSpecBeforeAnyStep(t *testing.T) {
	for name, mut := range map[string]func(*State){
		"newline in PATH":     func(st *State) { st.Tools.Path = "/bin\n<key>X</key>" },
		"control in home":     func(st *State) { st.Host.Home = plHome + "\x01" },
		"newline in binary":   func(st *State) { st.LiteLLMInstall.Binary = plLiteLLMBin + "\r" },
		"bad utf-8 in config": func(st *State) { st.LiteLLMInstall.ConfigPath = "/x/\xffconfig.yaml" },
	} {
		st := createState()
		mut(&st)
		p, err := BuildPlan(st, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}, AdminWant: AdminWant{LiteLLM: LiteLLMCreate}})
		var re *RefusalError
		if !errors.As(err, &re) || len(p.Steps) != 0 {
			t.Errorf("%s: err = %v, steps %v", name, err, kinds(p))
		}
	}
	for _, port := range []int{0, -1, 65536} {
		s := LiteLLMSpec{Binary: plLiteLLMBin, ConfigPath: plLiteLLMCfg, Path: plPATH, Home: plHome, Port: port}
		var re *RefusalError
		if err := checkLiteLLMSpec(s); !errors.As(err, &re) || !strings.Contains(err.Error(), "port") {
			t.Errorf("port %d: err = %v", port, err)
		}
	}
	ok := LiteLLMSpec{Binary: plLiteLLMBin, ConfigPath: plLiteLLMCfg, Path: plPATH, Home: plHome, Port: 65535}
	if err := checkLiteLLMSpec(ok); err != nil {
		t.Errorf("valid spec refused: %v", err)
	}
}

// TestBuildPlanCreateRefusesAListenerOnTheLiteLLMPort pins Important 2(a): a
// LiteLLM started by hand or under another label holds port 4000 when no
// local.litellm.proxy plist exists; the new job would crash-loop on bind
// while the old process answered the checks, so create refuses naming the
// pid and guide 14, and a port nobody checked refuses too.
func TestBuildPlanCreateRefusesAListenerOnTheLiteLLMPort(t *testing.T) {
	st := createState()
	st.LiteLLMInstall.ListenerPID = 4242
	p, err := BuildPlan(st, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}, AdminWant: AdminWant{LiteLLM: LiteLLMCreate}})
	mustRefuse(t, p, err, "pid 4242", "4000", guide14)
	st = createState()
	st.LiteLLMInstall.ListenerChecked = false
	p, err = BuildPlan(st, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}, AdminWant: AdminWant{LiteLLM: LiteLLMCreate}})
	mustRefuse(t, p, err, "4000", "not checked")
}

// TestBuildPlanRotateGuardsTheSaltFallback pins Important 3: LiteLLM encrypts
// stored credentials with LITELLM_MASTER_KEY when no salt key is set, so a
// plist with DATABASE_URL and no salt key refuses rotation (naming guide 14's
// key section); with both it proceeds, and with neither it proceeds saying
// there is no salt key, so "never changed: LITELLM_SALT_KEY" is only said
// when one exists.
func TestBuildPlanRotateGuardsTheSaltFallback(t *testing.T) {
	want := Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}, AdminWant: AdminWant{LiteLLM: LiteLLMRotateKey}}
	st := rotateState()
	st.LiteLLMPlist.HasSaltKey, st.LiteLLMPlist.HasDatabaseURL = false, true
	p, err := BuildPlan(st, want)
	mustRefuse(t, p, err, "DATABASE_URL", "LITELLM_SALT_KEY", "falls back to the master key",
		"set LITELLM_SALT_KEY to the current master key's value", guide14+"#6-generate-the-keys-file-mode")

	st.LiteLLMPlist.HasSaltKey = true
	text := mustPlan(t, st, want).Format()
	if !strings.Contains(text, "never changed in "+plLiteLLM+": LITELLM_SALT_KEY") {
		t.Errorf("both set:\n%s", text)
	}

	st.LiteLLMPlist.HasSaltKey, st.LiteLLMPlist.HasDatabaseURL = false, false
	text = mustPlan(t, st, want).Format()
	if strings.Contains(text, "never changed in "+plLiteLLM+": LITELLM_SALT_KEY") || !strings.Contains(text, "no salt key set; nothing to preserve") {
		t.Errorf("neither set:\n%s", text)
	}
}

// TestBuildPlanTightenLiteLLMIsAChmodOnly pins the tighten action: a plist
// looser than 0600 plans exactly one chmod step and touches nothing else
// (no token, pair, agent or config step); a 0600 or absent plist refuses,
// naming the file, and a non-local mode ignores the action.
func TestBuildPlanTightenLiteLLMIsAChmodOnly(t *testing.T) {
	st := rotateState() // plist mode 0644
	p := mustPlan(t, st, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}, AdminWant: AdminWant{LiteLLM: LiteLLMTighten}})
	if !sameKinds(kinds(p), StepChmod) {
		t.Fatalf("steps = %v, want only chmod", kinds(p))
	}
	if s := p.Steps[0]; s.Path != plLiteLLM || !strings.Contains(s.Detail, "0600") || !strings.Contains(s.Detail, "0644") {
		t.Errorf("chmod step = %+v", s)
	}
	tight := rotateState()
	tight.LiteLLMPlist.Mode = 0o600
	p, err := BuildPlan(tight, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}, AdminWant: AdminWant{LiteLLM: LiteLLMTighten}})
	mustRefuse(t, p, err, plLiteLLM, "0600")
	gone := rotateState()
	gone.LiteLLMPlist = LiteLLMPlistState{Path: plLiteLLM}
	p, err = BuildPlan(gone, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}, AdminWant: AdminWant{LiteLLM: LiteLLMTighten}})
	mustRefuse(t, p, err, plLiteLLM)
	p = mustPlan(t, freshState(), Desired{Desired: modelmode.Desired{Mode: modelclient.ModeNative}, AdminWant: AdminWant{LiteLLM: LiteLLMTighten}})
	if p.Admin.LiteLLM != LiteLLMNone {
		t.Errorf("native plan kept the action %q", p.Admin.LiteLLM)
	}
}

// TestBuildPlanUseExistingKeyRePairsOverASetPair pins that choosing "use the
// plist's key" reads the plist's master key into wt-model's pair even when
// the pair is already set (it may hold a stale key), as a pair-from-plist
// step whose text names the plist and never a key; the default request
// (LiteLLMNone) over the same state still only keeps the pair, and so does a
// use request when the plist has no key to read.
func TestBuildPlanUseExistingKeyRePairsOverASetPair(t *testing.T) {
	st := baseState() // pair set, plist holds a key
	p := mustPlan(t, st, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}, AdminWant: AdminWant{LiteLLM: LiteLLMUseExistingKey}})
	s, ok := stepOf(p, StepPair)
	if !ok || s.Action != ActionPairKeyFromPlist || s.Path != plLiteLLM {
		t.Fatalf("pair step = %+v (found %v), want the pair read from %s", s, ok, plLiteLLM)
	}
	text := p.Format()
	if !strings.Contains(text, "read from "+plLiteLLM) || strings.Contains(text, "keep wt-model's LiteLLM pair") {
		t.Errorf("Format does not say the pair is re-set from the plist:\n%s", text)
	}
	kept := mustPlan(t, st, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}})
	if _, ok := stepOf(kept, StepPair); ok || !strings.Contains(kept.Format(), "keep wt-model's LiteLLM pair") {
		t.Errorf("the default request no longer keeps the pair:\n%s", kept.Format())
	}
	keyless := baseState()
	keyless.LiteLLMPlist.HasMasterKey = false
	p = mustPlan(t, keyless, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}, AdminWant: AdminWant{LiteLLM: LiteLLMUseExistingKey}})
	if _, ok := stepOf(p, StepPair); ok {
		t.Errorf("a plist without a key planned a pair step")
	}
}

// TestApplyUseExistingKeyWritesThePlistKeyOverAStalePair pins that applying
// that plan reads the plist's key and writes it into the pair, replacing the
// stale one, and that neither key appears in a call, line or result.
func TestApplyUseExistingKeyWritesThePlistKeyOverAStalePair(t *testing.T) {
	f := apEnv(t)
	f.pair.URL, f.pair.APIKey = "http://localhost:4000", "sk-STALE-1"
	st := baseState()
	var lines []string
	full := mustPlan(t, st, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}, AdminWant: AdminWant{LiteLLM: LiteLLMUseExistingKey}})
	pairStep, _ := stepOf(full, StepPair)
	// The pair step alone: the restart and wait after it need a live server.
	r := Apply(context.Background(), f.Env(), Plan{Plan: modelmode.Plan{Want: full.Want, Steps: []Step{pairStep}}, Admin: full.Admin},
		func(s string) { lines = append(lines, s) })
	if r.Err != nil || f.pair.APIKey != apPlistKey {
		t.Fatalf("err %v, pair key from the plist: %t", r.Err, f.pair.APIKey == apPlistKey)
	}
	noSecrets(t, f, lines, r, apPlistKey, "sk-STALE-1")
}

// unmanagedAgentContent is a hand-written copy of what wt renders: wt's plist
// without its "managed by wt" marker comment.
func unmanagedAgentContent() []byte {
	return bytes.Replace(RenderAgentPlist(plSpec()), []byte("<!-- "+agentMarker+" -->\n"), nil, 1)
}

// TestBuildPlanRotateLeavesAHandWrittenLoadedAgentAlone pins #708: a key
// rotate writes only the LiteLLM plist and the pair, so a loaded agent plist
// that is not marked "managed by wt" is kept as written and only restarted,
// restarted through a kickstart (never a bootout or bootstrap of the file it
// does not write), and only when the plan's listen and token file match the
// plist's own --listen and --token-file: they are what the kept server keeps
// running, and the restart wt makes changes neither. A plain local apply, an
// agent that is not loaded, a plist wt cannot read, and one whose arguments
// name another listen or token file, still refuse.
func TestBuildPlanRotateLeavesAHandWrittenLoadedAgentAlone(t *testing.T) {
	rotate := Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}, AdminWant: AdminWant{LiteLLM: LiteLLMRotateKey}}

	st := rotateState()
	st.Agent.Managed = false
	st.Agent.Content = unmanagedAgentContent()
	p := mustPlan(t, st, rotate)
	if _, ok := stepOf(p, StepPlist); ok {
		t.Fatalf("the rotate plans a write of the unmanaged agent plist: %v", kinds(p))
	}
	for _, k := range []StepKind{StepBootout, StepBootstrap} {
		if _, ok := stepOf(p, k); ok {
			t.Errorf("the rotate plans %v for the unmanaged agent: %v", k, kinds(p))
		}
	}
	if _, ok := stepOf(p, StepKickstart); !ok {
		t.Errorf("the rotate does not restart the agent so it reads the new pair: %v", kinds(p))
	}
	if !strings.Contains(strings.Join(p.Notes, "\n"), "not marked \"managed by wt\"") {
		t.Errorf("the plan does not say the agent plist is left alone: %v", p.Notes)
	}

	plain := st
	_, err := BuildPlan(plain, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}})
	mustRefuse(t, Plan{}, err, "not marked \"managed by wt\"")

	unloaded := rotateState()
	unloaded.Agent.Managed = false
	unloaded.Agent.Content = unmanagedAgentContent()
	unloaded.Agent.Job = LaunchdJob{}
	_, err = BuildPlan(unloaded, rotate)
	mustRefuse(t, Plan{}, err, "not marked \"managed by wt\"")

	// A keep never rewrites the plist, so a url or token wt cannot tell the
	// kept server runs refuses: the [model-server] it would store must be the
	// server the kept plist itself names.
	unparsed := rotateState()
	unparsed.Agent.Managed = false
	unparsed.Agent.Content = []byte("hand written")
	unparsed.Agent.Err = "plist has no top-level dict"
	_, err = BuildPlan(unparsed, rotate)
	mustRefuse(t, Plan{}, err, "cannot tell", "nothing was changed")

	st = rotateState()
	st.Agent.Managed = false
	st.Agent.Content = unmanagedAgentContent()
	st.Agent.TokenFile = plHome + "/keys/token"
	_, err = BuildPlan(st, rotate)
	mustRefuse(t, Plan{}, err, "runs wt-model --token-file "+plHome+"/keys/token", plLocalTok)

	st = rotateState()
	st.Agent.Managed = false
	st.Agent.Content = unmanagedAgentContent()
	st.Agent.Listen = "127.0.0.1:9000"
	_, err = BuildPlan(st, rotate)
	mustRefuse(t, Plan{}, err, "runs wt-model --listen 127.0.0.1:9000")
}
