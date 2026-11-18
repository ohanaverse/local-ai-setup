package modelsetup

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
)

// apPlistKey is the LITELLM_MASTER_KEY the fake LiteLLM plist holds.
const apPlistKey = "sk-PLIST-SECRET-9"

// apGeneratedToken is what a generated token is with the fake's RandRead
// (bytes 0..31), so tests can look for it.
func apGeneratedToken() string {
	b := make([]byte, 32)
	for i := range b {
		b[i] = byte(i)
	}
	return base64.StdEncoding.EncodeToString(b)
}

// apEnv is a fake whose LiteLLM plist holds a master key and whose server
// answers every ping.
func apEnv(t *testing.T) *fakeEnv {
	f := newFakeEnv(t)
	f.plistPath = plLiteLLM
	f.plistVars["LITELLM_MASTER_KEY"] = apPlistKey
	f.ping = modelapi.Ping{APIMajor: 1, Schema: 1, Server: "wt-model"}
	f.configFile.Path = plCfgPath
	return f
}

// mutations is the recorded calls that change something or reach launchd,
// the network, entropy or a secret: everything Apply should do on purpose.
func (f *fakeEnv) mutations() []string {
	var out []string
	for _, c := range f.calls {
		for _, p := range []string{"FS.WriteAtomic", "FS.Chmod", "FS.Remove", "Launchd.", "Config.Update", "Config.SetPair", "LiteLLM.PlistVar", "Host.RandRead", "Net.Ping"} {
			if strings.HasPrefix(c, p) {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

func stepKinds(steps []Step) []StepKind {
	var k []StepKind
	for _, s := range steps {
		k = append(k, s.Kind)
	}
	return k
}

// noSecrets fails when any secret shows up in a recorded call, a progress
// line or any printed form of the result.
func noSecrets(t *testing.T, f *fakeEnv, progress []string, r Result, secrets ...string) {
	t.Helper()
	dump := strings.Join(f.calls, "\n") + "\n" + strings.Join(progress, "\n") + "\n" +
		fmt.Sprintf("%v %+v %#v", r, r, r)
	if r.Err != nil {
		dump += "\n" + r.Err.Error()
	}
	for _, s := range secrets {
		if s != "" && strings.Contains(dump, s) {
			t.Errorf("secret %q leaked into calls, progress or result:\n%s", s, dump)
		}
	}
}

func apply(t *testing.T, f *fakeEnv, p Plan) (Result, []string) {
	t.Helper()
	var lines []string
	r := Apply(context.Background(), f.Env(), p, func(s string) { lines = append(lines, s) })
	for _, l := range lines {
		for _, c := range l {
			if c > 0x7e || (c < 0x20 && c != '\t') {
				t.Errorf("progress line %q is not one line of ASCII", l)
				break
			}
		}
	}
	return r, lines
}

// TestApplyRunsStepsInOrderAndWritesConfigLast pins the local apply on a
// fresh host: token, pair, plist, bootstrap, wait, then the config, each with
// one progress line, so a half-applied setup is never pointed at. It also
// pins that a bootout of a job that is not loaded does not fail the apply.
func TestApplyRunsStepsInOrderAndWritesConfigLast(t *testing.T) {
	f := apEnv(t)
	p := mustPlan(t, freshState(), Desired{Mode: modelclient.ModeLocal})
	r, lines := apply(t, f, p)
	if r.Err != nil || r.Failed != nil {
		t.Fatalf("Apply: failed %+v err %v", r.Failed, r.Err)
	}
	if !sameKinds(stepKinds(r.Done), StepToken, StepPair, StepPlist, StepBootstrap, StepWait, StepConfig) {
		t.Errorf("Done = %v", stepKinds(r.Done))
	}
	want := []string{
		"Host.RandRead 32",
		"FS.WriteAtomic " + plLocalTok + " 0600",
		"LiteLLM.PlistVar " + plLiteLLM + " LITELLM_MASTER_KEY",
		"Config.SetPair url=true key=true",
		"FS.WriteAtomic " + plAgentPath + " 0644",
		"Launchd.Bootstrap " + plAgentPath,
		"Net.Ping http://127.0.0.1:8787",
		"Config.Update",
	}
	if got := f.mutations(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("calls:\n got %q\nwant %q", got, want)
	}
	if len(lines) != len(p.Steps) {
		t.Errorf("progress = %q, want one line per step", lines)
	}
	if got := string(f.files[plAgentPath]); got != string(RenderAgentPlist(plSpec())) || !strings.Contains(got, "managed by wt") {
		t.Errorf("plist written:\n%s", got)
	}
	tbl := f.configFile.Table
	if tbl == nil || tbl.Mode != "local" || tbl.URL != "http://127.0.0.1:8787" || tbl.TokenFile != plLocalTok {
		t.Errorf("table = %+v", tbl)
	}
	if f.pair.URL != "http://localhost:4000" || f.pair.APIKey != apPlistKey {
		t.Errorf("pair url = %q, key set to the plist's: %t", f.pair.URL, f.pair.APIKey == apPlistKey)
	}
	noSecrets(t, f, lines, r, apPlistKey, apGeneratedToken())

	// A changed port reloads; a bootout of a job that turns out not to be
	// loaded is not a failure.
	f = apEnv(t)
	f.files[plLocalTok], f.modes[plLocalTok] = []byte("tok\n"), 0o600
	f.bootoutErr = errors.New("Boot-out failed: 3: No such process")
	st := baseState()
	st.Listener = ListenerState{Port: 8788}
	p = mustPlan(t, st, Desired{Mode: modelclient.ModeLocal, Listen: "127.0.0.1:8788"})
	r, _ = apply(t, f, p)
	if r.Err != nil {
		t.Fatalf("bootout of an unloaded job failed the apply: %v", r.Err)
	}
	if !sameKinds(stepKinds(r.Done), StepPlist, StepBootout, StepBootstrap, StepWait, StepConfig) {
		t.Errorf("Done = %v", stepKinds(r.Done))
	}
	if len(r.Warnings) == 0 {
		t.Errorf("no warning says the job was not loaded")
	}
}

// TestApplyStopsAtTheFirstFailureAndLeavesConfigUntouched pins Review Focus
// 5: a failed bootstrap, a server that never answers, or a cancelled context
// stops the apply at that step, names it, lists what ran, and never calls
// Config.Update, so the stored mode never points at a broken server.
func TestApplyStopsAtTheFirstFailureAndLeavesConfigUntouched(t *testing.T) {
	plan := func() Plan { return mustPlan(t, freshState(), Desired{Mode: modelclient.ModeLocal}) }

	f := apEnv(t)
	f.bootstrapErr = errors.New("Bootstrap failed: 5: Input/output error")
	r, _ := apply(t, f, plan())
	if r.Failed == nil || r.Failed.Kind != StepBootstrap || r.Err == nil {
		t.Fatalf("Failed = %+v, Err = %v", r.Failed, r.Err)
	}
	if !sameKinds(stepKinds(r.Done), StepToken, StepPair, StepPlist) {
		t.Errorf("Done = %v", stepKinds(r.Done))
	}
	if f.called("Config.Update") || f.called("Net.Ping") {
		t.Errorf("ran past the failure: %q", f.mutations())
	}
	if !strings.Contains(r.Err.Error(), "Input/output error") {
		t.Errorf("Err = %v, does not carry the cause", r.Err)
	}

	f = apEnv(t)
	f.pingErr = &modelclient.UnreachableError{Addr: "http://127.0.0.1:8787", Reason: "connection refused"}
	r, _ = apply(t, f, plan())
	if r.Failed == nil || r.Failed.Kind != StepWait {
		t.Fatalf("Failed = %+v, Err = %v", r.Failed, r.Err)
	}
	if !sameKinds(stepKinds(r.Done), StepToken, StepPair, StepPlist, StepBootstrap) {
		t.Errorf("Done = %v", stepKinds(r.Done))
	}
	if f.called("Config.Update") {
		t.Errorf("config written after a wait that never answered")
	}

	f = apEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r = Apply(ctx, f.Env(), plan(), func(string) {})
	if r.Failed == nil || r.Failed.Kind != StepToken || !errors.Is(r.Err, context.Canceled) || len(r.Done) != 0 {
		t.Errorf("cancelled: Failed = %+v, Err = %v, Done = %v", r.Failed, r.Err, stepKinds(r.Done))
	}
	if m := f.mutations(); len(m) != 0 {
		t.Errorf("cancelled apply did %q", m)
	}
}

// TestApplyWritesTheTokenFile0600AndKeepsAnExistingOne pins that a generated
// token is 32 random bytes in base64 written 0600 through FS, and that a plan
// keeping a valid token never touches the file.
func TestApplyWritesTheTokenFile0600AndKeepsAnExistingOne(t *testing.T) {
	f := apEnv(t)
	r, _ := apply(t, f, mustPlan(t, freshState(), Desired{Mode: modelclient.ModeLocal}))
	if r.Err != nil {
		t.Fatalf("Apply: %v", r.Err)
	}
	if f.modes[plLocalTok] != 0o600 {
		t.Errorf("token mode = %04o", f.modes[plLocalTok])
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(f.files[plLocalTok])))
	if err != nil || len(raw) != 32 {
		t.Errorf("token is not 32 bytes of base64 (%d bytes, %v)", len(raw), err)
	}

	f = apEnv(t)
	f.files[plLocalTok], f.modes[plLocalTok] = []byte("existing\n"), 0o600
	st := freshState()
	st.Token = TokenState{Path: plLocalTok, Exists: true}
	st.TokenFiles = []TokenState{{Path: plRemoteTok}}
	p := mustPlan(t, st, Desired{Mode: modelclient.ModeLocal})
	if _, ok := stepOf(p, StepToken); ok {
		t.Fatalf("plan has a token step: %v", kinds(p))
	}
	if r, _ = apply(t, f, p); r.Err != nil {
		t.Fatalf("Apply: %v", r.Err)
	}
	if string(f.files[plLocalTok]) != "existing\n" || f.called("FS.WriteAtomic "+plLocalTok) || f.called("Host.RandRead") {
		t.Errorf("existing token touched: %q", f.mutations())
	}
}

// TestApplyTightensALooseTokenFile pins that a loose token is chmodded to
// 0600 and keeps its content (a running server already holds it).
func TestApplyTightensALooseTokenFile(t *testing.T) {
	f := apEnv(t)
	f.files[plLocalTok], f.modes[plLocalTok] = []byte("keep-me\n"), 0o644
	st := baseState()
	st.Token.Loose = true
	r, _ := apply(t, f, mustPlan(t, st, Desired{Mode: modelclient.ModeLocal}))
	if r.Err != nil {
		t.Fatalf("Apply: %v", r.Err)
	}
	want := []string{"FS.Chmod " + plLocalTok + " 0600"}
	if got := f.mutations(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("calls = %q, want %q", got, want)
	}
	if string(f.files[plLocalTok]) != "keep-me\n" || f.modes[plLocalTok] != 0o600 {
		t.Errorf("token = %q mode %04o", f.files[plLocalTok], f.modes[plLocalTok])
	}
}

// TestApplyIsIdempotent pins that what Apply produced is what Observe and
// BuildPlan call done: the next plan has no step and applying it writes
// nothing and reaches neither launchd nor the config.
func TestApplyIsIdempotent(t *testing.T) {
	f := apEnv(t)
	f.env["PATH"] = plPATH
	f.files[plBinary], f.modes[plBinary] = []byte("bin"), 0o755
	f.plistInfo = litellm.ProxyPlistInfo{Path: plLiteLLM, Port: 4000, EnvNames: []string{"LITELLM_MASTER_KEY"}}
	f.bootstrapPID = 4123
	f.listenerFromJob = true
	ctx := context.Background()

	st := Observe(ctx, f.Env(), ObserveOptions{})
	p, err := BuildPlan(st, Desired{Mode: modelclient.ModeLocal})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(p.Steps) == 0 {
		t.Fatalf("first plan is empty:\n%s", p.Format())
	}
	if r := Apply(ctx, f.Env(), p, func(string) {}); r.Err != nil {
		t.Fatalf("first Apply: %v", r.Err)
	}

	st = Observe(ctx, f.Env(), ObserveOptions{})
	p, err = BuildPlan(st, Desired{Mode: modelclient.ModeLocal})
	if err != nil {
		t.Fatalf("second BuildPlan: %v", err)
	}
	if len(p.Steps) != 0 {
		t.Fatalf("second plan has steps:\n%s", p.Format())
	}
	f.calls = nil
	r := Apply(ctx, f.Env(), p, func(string) {})
	if r.Err != nil || len(r.Done) != 0 {
		t.Errorf("second Apply: done %v err %v", stepKinds(r.Done), r.Err)
	}
	if m := f.mutations(); len(m) != 0 {
		t.Errorf("second Apply did %q", m)
	}
}

// TestApplyWaitTimesOutAndPointsAtTheLog pins the wait: it pings every
// ReadyPoll on the fake clock (no real sleep), gives up after ReadyTimeout
// with the classified error and the err log path, and passes as soon as the
// server answers.
func TestApplyWaitTimesOutAndPointsAtTheLog(t *testing.T) {
	f := apEnv(t)
	f.pingErr = &modelclient.UnauthorizedError{Addr: "http://127.0.0.1:8787"}
	start := f.now
	began := time.Now()
	r, _ := apply(t, f, mustPlan(t, freshState(), Desired{Mode: modelclient.ModeLocal}))
	if time.Since(began) > 5*time.Second {
		t.Errorf("the wait slept for real")
	}
	if r.Failed == nil || r.Failed.Kind != StepWait {
		t.Fatalf("Failed = %+v, Err = %v", r.Failed, r.Err)
	}
	var we *WaitError
	if !errors.As(r.Err, &we) || we.Kind != KindUnauthorized {
		t.Errorf("Err = %#v, want *WaitError with KindUnauthorized", r.Err)
	}
	msg := r.Err.Error()
	for _, w := range []string{"http://127.0.0.1:8787", "30s", "rejected the token", plHome + "/.wt-model.err.log"} {
		if !strings.Contains(msg, w) {
			t.Errorf("Err %q does not contain %q", msg, w)
		}
	}
	if got := f.now.Sub(start); got < ReadyTimeout || got > ReadyTimeout+ReadyPoll {
		t.Errorf("fake clock advanced %v, want about %v", got, ReadyTimeout)
	}
	sleeps, pings := 0, 0
	for _, c := range f.calls {
		if c == "Host.Sleep "+ReadyPoll.String() {
			sleeps++
		}
		if strings.HasPrefix(c, "Net.Ping") {
			pings++
		}
	}
	if want := int(ReadyTimeout / ReadyPoll); sleeps != want || pings != want+1 {
		t.Errorf("sleeps = %d, pings = %d, want %d and %d", sleeps, pings, want, want+1)
	}

	// A server that answers on the fourth ping passes.
	f = apEnv(t)
	unreach := &modelclient.UnreachableError{Addr: "http://127.0.0.1:8787", Reason: "refused"}
	f.pingErrs = []error{unreach, unreach, unreach}
	if r, _ = apply(t, f, mustPlan(t, freshState(), Desired{Mode: modelclient.ModeLocal})); r.Err != nil {
		t.Fatalf("Apply: %v", r.Err)
	}
	if !f.called("Config.Update") {
		t.Errorf("config not written after the server answered")
	}
}

// TestApplyRemoteWritesThePastedTokenBeforeTheConfig pins that a pasted
// remote token is written 0600 to the fixed path before the table names it,
// never appears in any call, line or result, and that an identical token
// already on disk is not rewritten.
func TestApplyRemoteWritesThePastedTokenBeforeTheConfig(t *testing.T) {
	want := Desired{Mode: modelclient.ModeRemote, URL: "https://gpu.example:8787", PastedToken: plPasted}
	f := apEnv(t)
	p := mustPlan(t, freshState(), want)
	r, lines := apply(t, f, p)
	if r.Err != nil {
		t.Fatalf("Apply: %v", r.Err)
	}
	wantCalls := []string{"FS.WriteAtomic " + plRemoteTok + " 0600", "Config.Update"}
	if got := f.mutations(); strings.Join(got, "|") != strings.Join(wantCalls, "|") {
		t.Errorf("calls = %q, want %q", got, wantCalls)
	}
	if strings.TrimSpace(string(f.files[plRemoteTok])) != plPasted {
		t.Errorf("token file does not hold the pasted token")
	}
	tbl := f.configFile.Table
	if tbl == nil || tbl.Mode != "remote" || tbl.URL != want.URL || tbl.TokenFile != plRemoteTok {
		t.Errorf("table = %+v", tbl)
	}
	noSecrets(t, f, lines, r, plPasted)

	// The same token already there: the replace step writes nothing.
	f = apEnv(t)
	f.files[plRemoteTok], f.modes[plRemoteTok] = []byte(plPasted+"\n"), 0o600
	st := freshState()
	st.TokenFiles = []TokenState{{Path: plLocalTok}, {Path: plRemoteTok, Exists: true}}
	p = mustPlan(t, st, want)
	if s, _ := stepOf(p, StepToken); s.Action != ActionReplacePastedToken {
		t.Fatalf("token step = %+v", s)
	}
	r, lines = apply(t, f, p)
	if r.Err != nil {
		t.Fatalf("Apply: %v", r.Err)
	}
	if f.called("FS.WriteAtomic " + plRemoteTok) {
		t.Errorf("identical token rewritten")
	}
	noSecrets(t, f, lines, r, plPasted)

	// A different one is replaced.
	f = apEnv(t)
	f.files[plRemoteTok], f.modes[plRemoteTok] = []byte("old\n"), 0o600
	if r, _ = apply(t, f, p); r.Err != nil || strings.TrimSpace(string(f.files[plRemoteTok])) != plPasted {
		t.Errorf("different token not replaced: %v", r.Err)
	}
}

// TestApplyReadsTheLiteLLMKeyOnlyInThePairStep pins that the master key is
// read from the LiteLLM plist once, inside the pair step, written only
// through SetPair, and that an entered key never reads the plist; neither key
// shows in any call, line or result.
func TestApplyReadsTheLiteLLMKeyOnlyInThePairStep(t *testing.T) {
	f := apEnv(t)
	var reads []string
	var lines []string
	r := Apply(context.Background(), f.Env(), mustPlan(t, freshState(), Desired{Mode: modelclient.ModeLocal}), func(s string) {
		lines = append(lines, s)
		reads = append(reads, fmt.Sprint(f.called("LiteLLM.PlistVar")))
	})
	if r.Err != nil {
		t.Fatalf("Apply: %v", r.Err)
	}
	// progress runs before each step: the plist is unread before the pair
	// step's line and read from the plist step's on.
	if strings.Join(reads, ",") != "false,false,true,true,true,true" {
		t.Errorf("PlistVar read by step = %v", reads)
	}
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, "LiteLLM.PlistVar") {
			n++
		}
	}
	if n != 1 || f.pair.APIKey != apPlistKey {
		t.Errorf("PlistVar calls = %d, pair key from the plist: %t", n, f.pair.APIKey == apPlistKey)
	}
	noSecrets(t, f, lines, r, apPlistKey, apGeneratedToken())

	// A plist with no key fails the pair step without writing the pair.
	f = apEnv(t)
	delete(f.plistVars, "LITELLM_MASTER_KEY")
	r, _ = apply(t, f, mustPlan(t, freshState(), Desired{Mode: modelclient.ModeLocal}))
	if r.Failed == nil || r.Failed.Kind != StepPair || f.called("Config.SetPair") {
		t.Errorf("missing key: Failed = %+v, Err = %v", r.Failed, r.Err)
	}

	// An entered key never reads the plist.
	f = apEnv(t)
	st := freshState()
	st.LiteLLMPlist.HasMasterKey = false
	r, lines = apply(t, f, mustPlan(t, st, Desired{Mode: modelclient.ModeLocal, MasterKey: plMasterKey}))
	if r.Err != nil {
		t.Fatalf("Apply: %v", r.Err)
	}
	if f.called("LiteLLM.PlistVar") || f.pair.APIKey != plMasterKey {
		t.Errorf("entered key: plist read %t, pair key entered %t", f.called("LiteLLM.PlistVar"), f.pair.APIKey == plMasterKey)
	}
	noSecrets(t, f, lines, r, plMasterKey, apPlistKey)
}

// TestControlStartStopRestart pins the tab's start, stop and restart: start
// bootstraps an unloaded agent and kickstarts a loaded one, stop boots it
// out (an unloaded job is already stopped), restart is kickstart -k.
func TestControlStartStopRestart(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name   string
		job    LaunchdJob
		action ControlAction
		want   string
	}{
		{"start unloaded", LaunchdJob{}, ControlStart, "Launchd.Bootstrap " + plAgentPath},
		{"start loaded", LaunchdJob{Loaded: true}, ControlStart, "Launchd.Kickstart " + AgentLabel + " kill=false"},
		{"stop", LaunchdJob{Loaded: true, Running: true, PID: 7}, ControlStop, "Launchd.Bootout " + AgentLabel},
		{"restart", LaunchdJob{Loaded: true, Running: true, PID: 7}, ControlRestart, "Launchd.Kickstart " + AgentLabel + " kill=true"},
	}
	for _, c := range cases {
		f := newFakeEnv(t)
		f.jobs[AgentLabel] = c.job
		if err := Control(ctx, f.Env(), c.action); err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
		var acts []string
		for _, call := range f.calls {
			if strings.HasPrefix(call, "Launchd.") && !strings.HasPrefix(call, "Launchd.Print") && !strings.HasPrefix(call, "Launchd.Available") {
				acts = append(acts, call)
			}
		}
		if len(acts) != 1 || acts[0] != c.want {
			t.Errorf("%s: launchd calls = %q, want %q", c.name, acts, c.want)
		}
	}

	f := newFakeEnv(t)
	f.bootoutErr = errors.New("Boot-out failed: 3: No such process")
	if err := Control(ctx, f.Env(), ControlStop); err != nil {
		t.Errorf("stop of an unloaded job: %v", err)
	}
	f = newFakeEnv(t)
	f.jobs[AgentLabel] = LaunchdJob{Loaded: true}
	f.bootoutErr = errors.New("Boot-out failed: 5: Input/output error")
	if err := Control(ctx, f.Env(), ControlStop); err == nil {
		t.Errorf("a failed bootout of a loaded job was not reported")
	}
	if err := Control(ctx, f.Env(), ControlAction(99)); err == nil {
		t.Errorf("an unknown action was accepted")
	}
}

// TestApplyNeverCallsLaunchctlWhenThePlanHasNoLaunchdSteps pins that native,
// remote and token-only plans never reach launchd, so they work off macOS and
// never disturb a running agent.
func TestApplyNeverCallsLaunchctlWhenThePlanHasNoLaunchdSteps(t *testing.T) {
	plans := map[string]Plan{
		"native": mustPlan(t, baseState(), Desired{Mode: modelclient.ModeNative}),
		"remote": mustPlan(t, freshState(), Desired{Mode: modelclient.ModeRemote, URL: "https://gpu.example:8787", PastedToken: plPasted}),
	}
	st := baseState()
	st.Token.Loose = true
	plans["tighten"] = mustPlan(t, st, Desired{Mode: modelclient.ModeLocal})
	for name, p := range plans {
		f := apEnv(t)
		f.files[plLocalTok], f.modes[plLocalTok] = []byte("tok\n"), 0o644
		r, _ := apply(t, f, p)
		if r.Err != nil {
			t.Errorf("%s: %v", name, r.Err)
		}
		if f.called("Launchd.") {
			t.Errorf("%s: launchd reached: %q", name, f.calls)
		}
	}
	// The native plan clears url and token_file.
	f := apEnv(t)
	f.configFile.Table = &config.ModelServerState{Mode: "local", URL: "http://127.0.0.1:8787", TokenFile: plLocalTok}
	apply(t, f, plans["native"])
	if tbl := f.configFile.Table; tbl.Mode != "native" || tbl.URL != "" || tbl.TokenFile != "" {
		t.Errorf("native table = %+v", tbl)
	}
}

// TestApplyFailsAnUnknownStepKind pins that a step Apply does not know fails
// the apply instead of being skipped, and the config after it never runs.
func TestApplyFailsAnUnknownStepKind(t *testing.T) {
	f := apEnv(t)
	p := Plan{Want: Desired{Mode: modelclient.ModeNative}, Steps: []Step{{Kind: "mystery"}, {Kind: StepConfig}}}
	r, _ := apply(t, f, p)
	if r.Failed == nil || r.Failed.Kind != "mystery" || r.Err == nil || f.called("Config.Update") {
		t.Errorf("Failed = %+v, Err = %v, calls %q", r.Failed, r.Err, f.mutations())
	}
}
