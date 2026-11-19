package modelsetup

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelmode"
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
		for _, p := range []string{"FS.WriteAtomic", "FS.Chmod", "FS.Remove", "Launchd.", "Config.Update", "Config.SetPair", "LiteLLM.PlistVar", "Host.RandRead", "Net.Ping", "Net.Get http://localhost:4000"} {
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
	p := mustPlan(t, freshState(), Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}})
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
	p = mustPlan(t, st, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}, AdminWant: AdminWant{Listen: "127.0.0.1:8788"}})
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
	plan := func() Plan {
		return mustPlan(t, freshState(), Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}})
	}

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
	r, _ := apply(t, f, mustPlan(t, freshState(), Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}}))
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
	p := mustPlan(t, st, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}})
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
	r, _ := apply(t, f, mustPlan(t, st, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}}))
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
	p, err := BuildPlan(st, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}})
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
	p, err = BuildPlan(st, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}})
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
	r, _ := apply(t, f, mustPlan(t, freshState(), Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}}))
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
	if r, _ = apply(t, f, mustPlan(t, freshState(), Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}})); r.Err != nil {
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
	want := Desired{Desired: modelmode.Desired{Mode: modelclient.ModeRemote, URL: "https://gpu.example:8787", PastedToken: plPasted}}
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
	r := Apply(context.Background(), f.Env(), mustPlan(t, freshState(), Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}}), func(s string) {
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
	r, _ = apply(t, f, mustPlan(t, freshState(), Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}}))
	if r.Failed == nil || r.Failed.Kind != StepPair || f.called("Config.SetPair") {
		t.Errorf("missing key: Failed = %+v, Err = %v", r.Failed, r.Err)
	}

	// An entered key never reads the plist.
	f = apEnv(t)
	st := freshState()
	st.LiteLLMPlist.HasMasterKey = false
	r, lines = apply(t, f, mustPlan(t, st, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}, AdminWant: AdminWant{MasterKey: plMasterKey}}))
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
		"native": mustPlan(t, baseState(), Desired{Desired: modelmode.Desired{Mode: modelclient.ModeNative}}),
		"remote": mustPlan(t, freshState(), Desired{Desired: modelmode.Desired{Mode: modelclient.ModeRemote, URL: "https://gpu.example:8787", PastedToken: plPasted}}),
	}
	st := baseState()
	st.Token.Loose = true
	plans["tighten"] = mustPlan(t, st, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}})
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
	p := Plan{Plan: modelmode.Plan{Want: modelmode.Desired{Mode: modelclient.ModeNative}, Steps: []Step{{Kind: "mystery"}, {Kind: StepConfig}}}}
	r, _ := apply(t, f, p)
	if r.Failed == nil || r.Failed.Kind != "mystery" || r.Err == nil || f.called("Config.Update") {
		t.Errorf("Failed = %+v, Err = %v, calls %q", r.Failed, r.Err, f.mutations())
	}
}

const (
	apOldMaster = "sk-OLD-MASTER-1"
	apOldSalt   = "SALT-KEEP-1"
)

// rotEnv is a fake whose LiteLLM plist is a real XML plist on the fake FS
// (mode 0644, an old master key and a salt key), with the wt-model agent
// fully set up as in rotateState.
func rotEnv(t *testing.T) *fakeEnv {
	f := apEnv(t)
	f.files[plLocalTok], f.modes[plLocalTok] = []byte("tok\n"), 0o600
	f.files[plLiteLLM] = RenderLiteLLMPlist(LiteLLMSpec{Binary: plLiteLLMBin, ConfigPath: plLiteLLMCfg, Path: plPATH, Home: plHome,
		Port: 4000, MasterKey: apOldMaster, SaltKey: apOldSalt})
	f.modes[plLiteLLM] = 0o644
	f.pair.URL, f.pair.APIKey = "http://localhost:4000", apOldMaster
	liteLLMUp(f, 0)
	return f
}

// liteLLMUp makes the fake LiteLLM answer its liveliness and authenticated
// checks, and (pid != 0) makes a bootstrap of its plist run the job with
// that pid, which also listens on 4000.
func liteLLMUp(f *fakeEnv, pid int) {
	f.getReplies = map[string]getReply{
		"http://localhost:4000/health/liveliness": {status: 200},
		"http://localhost:4000/v1/models":         {status: 200},
	}
	if pid != 0 {
		f.litellmPID = pid
		f.listeners = map[int]int{4000: pid}
	}
}

// envOf reads the environment variables of the plist at path on the fake FS.
func envOf(t *testing.T, f *fakeEnv, path string) map[string]string {
	t.Helper()
	sc, err := scanEnv(f.files[path])
	if err != nil {
		t.Fatalf("plist %s does not parse: %v", path, err)
	}
	return sc.vars
}

// backupsOf lists the backups of path on the fake FS: in wt-model's backups
// directory (plBackupDir), never beside the plist.
func backupsOf(f *fakeEnv, path string) []string {
	var out []string
	for _, n := range f.fileNames() {
		if strings.HasPrefix(n, plBackupDir+"/"+filepath.Base(path)+".bak-") || strings.HasPrefix(n, path+".bak-") {
			out = append(out, n)
		}
	}
	return out
}

// TestApplyRotateKeepsTheBackupAndTightensTheMode pins a rotation end to end:
// a dated backup beside the plist holds the old bytes at the old mode, the
// plist carries a new key (and the same salt key) at 0600, the pair holds the
// same new key, and LiteLLM is restarted before wt-model.
func TestApplyRotateKeepsTheBackupAndTightensTheMode(t *testing.T) {
	f := rotEnv(t)
	old := append([]byte(nil), f.files[plLiteLLM]...)
	p := mustPlan(t, rotateState(), Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}, AdminWant: AdminWant{LiteLLM: LiteLLMRotateKey}})
	r, lines := apply(t, f, p)
	if r.Err != nil {
		t.Fatalf("Apply: %v (recovery %q)", r.Err, r.Recovery)
	}
	b := backupsOf(f, plLiteLLM)
	if len(b) != 1 {
		t.Fatalf("backups = %q, want one", b)
	}
	if string(f.files[b[0]]) != string(old) || f.modes[b[0]] != 0o600 {
		t.Errorf("backup %s: old bytes kept %t, mode %04o", b[0], string(f.files[b[0]]) == string(old), f.modes[b[0]])
	}
	vars := envOf(t, f, plLiteLLM)
	newKey := vars["LITELLM_MASTER_KEY"]
	if newKey == apOldMaster || !strings.HasPrefix(newKey, "sk-") || len(newKey) != 67 {
		t.Errorf("master key not replaced with a generated one (len %d)", len(newKey))
	}
	if vars["LITELLM_SALT_KEY"] != apOldSalt {
		t.Errorf("salt key changed")
	}
	if f.modes[plLiteLLM] != 0o600 {
		t.Errorf("plist mode = %04o", f.modes[plLiteLLM])
	}
	if f.pair.APIKey != newKey || f.pair.URL != "http://localhost:4000" {
		t.Errorf("pair does not hold the new key (url %q)", f.pair.URL)
	}
	want := []string{
		"Host.RandRead 32",
		"FS.WriteAtomic " + plLiteLLM + " 0600",
		"Config.SetPair url=true key=true",
		"Launchd.Kickstart local.litellm.proxy kill=true",
		"Net.Get http://localhost:4000/health/liveliness bearer=false",
		"Net.Get http://localhost:4000/v1/models bearer=true",
		"Launchd.Kickstart " + AgentLabel + " kill=true",
		"Net.Ping http://127.0.0.1:8787",
	}
	if got := f.mutations(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("calls:\n got %q\nwant %q", got, want)
	}
	if !f.called("FS.Backup " + plLiteLLM) {
		t.Errorf("no backup call")
	}
	if !strings.Contains(strings.Join(r.Notes, "\n"), b[0]) {
		t.Errorf("Result does not name the backup: %q", r.Notes)
	}
	noSecrets(t, f, lines, r, newKey, apOldMaster, apOldSalt)

	// A second rotation in the same second keeps both backups.
	first := b[0]
	p = mustPlan(t, func() State { st := rotateState(); st.LiteLLMPlist.Mode = 0o600; return st }(), Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}, AdminWant: AdminWant{LiteLLM: LiteLLMRotateKey}})
	if r, _ = apply(t, f, p); r.Err != nil {
		t.Fatalf("second Apply: %v", r.Err)
	}
	b = backupsOf(f, plLiteLLM)
	if len(b) != 2 || string(f.files[first]) != string(old) {
		t.Errorf("second rotation in the same second: backups %q, first kept %t", b, string(f.files[first]) == string(old))
	}
}

// TestApplyRotateFailureLeavesThePairUntouched pins the failure rule: a
// failed backup, an edit the plist refuses or a failed write stops the apply
// before the pair, so wt-model keeps the key LiteLLM still runs with; and a
// failed LiteLLM restart leaves plist and pair holding the same new key, with
// the exact commands to run by hand.
func TestApplyRotateFailureLeavesThePairUntouched(t *testing.T) {
	plan := func() Plan {
		return mustPlan(t, rotateState(), Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}, AdminWant: AdminWant{LiteLLM: LiteLLMRotateKey}})
	}
	cases := map[string]struct {
		set    func(f *fakeEnv)
		failed StepKind
	}{
		"backup fails": {func(f *fakeEnv) { f.backupErr = errors.New("disk full") }, StepBackup},
		// The key edit is dry-run before the backup, so a plist it refuses
		// stops the backup step and no copy of the old key is made.
		"no master key": {func(f *fakeEnv) {
			f.files[plLiteLLM] = []byte(envPlist("", "\t\t<key>PATH</key>\n\t\t<string>/bin</string>\n"))
		}, StepBackup},
		"binary plist": {func(f *fakeEnv) { f.files[plLiteLLM] = []byte("bplist00\x00\x01") }, StepBackup},
		"write fails":  {func(f *fakeEnv) { f.writeErr[plLiteLLM] = errors.New("read-only file system") }, StepMasterKey},
	}
	for name, c := range cases {
		f := rotEnv(t)
		c.set(f)
		before := append([]byte(nil), f.files[plLiteLLM]...)
		r, lines := apply(t, f, plan())
		if r.Failed == nil || r.Failed.Kind != c.failed {
			t.Errorf("%s: Failed = %+v, Err = %v", name, r.Failed, r.Err)
			continue
		}
		if f.called("Config.SetPair") || f.pair.APIKey != apOldMaster {
			t.Errorf("%s: the pair was touched", name)
		}
		if f.called("Launchd.Kickstart") {
			t.Errorf("%s: restarted after a failure", name)
		}
		if string(f.files[plLiteLLM]) != string(before) {
			t.Errorf("%s: the plist changed", name)
		}
		// "unchanged" after a kept backup, "not touched" when none was made.
		if r.Recovery == "" || !strings.Contains(r.Recovery, "unchanged") && !strings.Contains(r.Recovery, "not touched") {
			t.Errorf("%s: Recovery = %q", name, r.Recovery)
		}
		if c.failed == StepBackup && len(backupsOf(f, plLiteLLM)) != 0 {
			t.Errorf("%s: a backup was left although the rotation stopped at the backup step", name)
		}
		noSecrets(t, f, lines, r, apOldMaster, apOldSalt)
	}

	// A failed LiteLLM restart: plist and pair agree, and the Result says
	// exactly what to run.
	f := rotEnv(t)
	f.kickstartErr["local.litellm.proxy"] = errors.New("Could not find service")
	r, lines := apply(t, f, plan())
	if r.Failed == nil || r.Failed.Kind != StepKickstartLiteLLM {
		t.Fatalf("Failed = %+v, Err = %v", r.Failed, r.Err)
	}
	newKey := envOf(t, f, plLiteLLM)["LITELLM_MASTER_KEY"]
	if newKey == apOldMaster || f.pair.APIKey != newKey {
		t.Errorf("plist and pair disagree after a failed restart")
	}
	for _, w := range []string{"launchctl kickstart -k gui/501/local.litellm.proxy", "launchctl kickstart -k gui/501/" + AgentLabel, "launchctl bootstrap gui/501 " + plLiteLLM} {
		if !strings.Contains(r.Recovery, w) {
			t.Errorf("Recovery %q does not say %q", r.Recovery, w)
		}
	}
	if f.called("Launchd.Kickstart " + AgentLabel) {
		t.Errorf("wt-model restarted after LiteLLM failed")
	}
	noSecrets(t, f, lines, r, newKey, apOldMaster, apOldSalt)

	// A step failing after the edit and before the pair: the plist holds the
	// new key, the pair the old one, and the Result says how to restore the
	// backup. The rotation plan has no step between the edit (its second) and
	// the pair, so a hand-built StepChmod (a real step only of the tighten
	// plan) on a missing path, put right after the backup and the edit,
	// stands in for any failure there.
	f = rotEnv(t)
	f.modes[plLiteLLM] = 0o644
	p := plan()
	r, _ = apply(t, f, Plan{Plan: modelmode.Plan{Want: p.Want, Steps: append(append([]Step{}, p.Steps[:2]...), Step{Kind: StepChmod, Path: "/nope"}), Notes: p.Notes}, Admin: p.Admin, Agent: p.Agent})
	if r.Failed == nil || r.Failed.Kind != StepChmod || f.called("Config.SetPair") {
		t.Fatalf("chmod: Failed = %+v", r.Failed)
	}
	if b := backupsOf(f, plLiteLLM); len(b) != 1 || !strings.Contains(r.Recovery, "cp -p "+b[0]+" "+plLiteLLM+" && chmod 600 "+plLiteLLM) {
		t.Errorf("Recovery %q does not name the restore (backups %q)", r.Recovery, b)
	}
}

// TestApplyCreateWritesThePlistLoadsItAndPairsTheSameKey pins create end to
// end: a generated master key (sk- + 64 hex) and salt key (64 raw hex), the
// plist written 0600 through FS, bootstrapped, waited on, and the pair set to
// the same master key before the wt-model agent; a LiteLLM label still loaded
// from elsewhere or a plist that appeared since the plan stops before
// anything is written.
func TestApplyCreateWritesThePlistLoadsItAndPairsTheSameKey(t *testing.T) {
	f := apEnv(t)
	liteLLMUp(f, 77)
	p := mustPlan(t, createState(), Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}, AdminWant: AdminWant{LiteLLM: LiteLLMCreate}})
	r, lines := apply(t, f, p)
	if r.Err != nil {
		t.Fatalf("Apply: %v", r.Err)
	}
	if f.modes[plLiteLLM] != 0o600 {
		t.Errorf("plist mode %04o", f.modes[plLiteLLM])
	}
	vars := envOf(t, f, plLiteLLM)
	master, salt := vars["LITELLM_MASTER_KEY"], vars["LITELLM_SALT_KEY"]
	if !regexp.MustCompile(`^sk-[0-9a-f]{64}$`).MatchString(master) || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(salt) {
		t.Errorf("keys are not real-shaped (master %d chars, salt %d chars)", len(master), len(salt))
	}
	if master == "sk-"+salt {
		t.Errorf("master and salt keys are the same bytes")
	}
	if f.pair.APIKey != master || f.pair.URL != "http://localhost:4000" {
		t.Errorf("pair does not hold the plist's new key")
	}
	if _, ok := vars["DATABASE_URL"]; ok {
		t.Errorf("DATABASE_URL written")
	}
	want := []string{
		"Host.RandRead 32", // the wt-model token
		"FS.WriteAtomic " + plLocalTok + " 0600",
		"Launchd.Print local.litellm.proxy",
		"Host.RandRead 32", "Host.RandRead 32",
		"FS.WriteAtomic " + plLiteLLM + " 0600",
		"Launchd.Bootstrap " + plLiteLLM,
		"Net.Get http://localhost:4000/health/liveliness bearer=false",
		"Launchd.Print local.litellm.proxy",
		"Net.Get http://localhost:4000/v1/models bearer=true",
		"Config.SetPair url=true key=true",
	}
	got := f.mutations()
	if len(got) < len(want) {
		t.Fatalf("calls %q", got)
	}
	if strings.Join(got[:len(want)], "|") != strings.Join(want, "|") {
		t.Errorf("calls:\n got %q\nwant %q", f.mutations(), want)
	}
	noSecrets(t, f, lines, r, master, salt)

	// The label still loaded from another plist: nothing written.
	f = apEnv(t)
	f.jobs["local.litellm.proxy"] = LaunchdJob{Loaded: true, Running: true, PID: 77}
	r, _ = apply(t, f, p)
	if r.Failed == nil || r.Failed.Kind != StepLiteLLMPlist || f.called("FS.WriteAtomic "+plLiteLLM) || f.called("Config.SetPair") {
		t.Errorf("loaded label: Failed = %+v, Err = %v", r.Failed, r.Err)
	}
	// A plist that appeared since the plan is never overwritten.
	f = apEnv(t)
	f.files[plLiteLLM] = []byte("theirs")
	r, _ = apply(t, f, p)
	if r.Failed == nil || r.Failed.Kind != StepLiteLLMPlist || string(f.files[plLiteLLM]) != "theirs" {
		t.Errorf("appeared plist: Failed = %+v, Err = %v", r.Failed, r.Err)
	}
	// A spec that fails validation (a hand-built plan) writes nothing.
	f = apEnv(t)
	bad := *p.LiteLLM
	bad.Port = 0
	q := p
	q.LiteLLM = &bad
	if r, _ = apply(t, f, q); r.Failed == nil || r.Failed.Kind != StepLiteLLMPlist || f.called("FS.WriteAtomic "+plLiteLLM) {
		t.Errorf("bad port: Failed = %+v, Err = %v", r.Failed, r.Err)
	}
	// LiteLLM that never answers: the pair is not set and the Result says how
	// to finish.
	f = apEnv(t)
	f.getStatus = 503
	r, _ = apply(t, f, p)
	if r.Failed == nil || r.Failed.Kind != StepWait || f.called("Config.SetPair") || f.called("Config.Update") {
		t.Errorf("LiteLLM down: Failed = %+v, Err = %v", r.Failed, r.Err)
	}
	if r.Err == nil || !strings.Contains(r.Err.Error(), plHome+"/.litellm.err.log") || !strings.Contains(r.Recovery, "wt-model litellm set") {
		t.Errorf("LiteLLM down: Err %v, Recovery %q", r.Err, r.Recovery)
	}
	// Minor iii: a failed create also says how to abandon it.
	for _, w := range []string{"launchctl bootout gui/501/local.litellm.proxy", "remove " + plLiteLLM} {
		if !strings.Contains(r.Recovery, w) {
			t.Errorf("Recovery %q does not say %q", r.Recovery, w)
		}
	}
}

// TestApplyCreateRefusesAnAnswerFromAnotherProcess pins Important 2(b): when
// /health/liveliness is answered by a process that is not the job just
// bootstrapped (a LiteLLM started by hand holds port 4000 while the new job
// crash-loops on bind), or LiteLLM rejects the new key, the pair is never
// written and the Result says what answered and how to finish or abandon.
func TestApplyCreateRefusesAnAnswerFromAnotherProcess(t *testing.T) {
	p := mustPlan(t, createState(), Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}, AdminWant: AdminWant{LiteLLM: LiteLLMCreate}})
	f := apEnv(t)
	liteLLMUp(f, 77)
	f.listeners[4000] = 99
	r, lines := apply(t, f, p)
	if r.Failed == nil || r.Failed.Kind != StepWait || f.called("Config.SetPair") || f.called("Config.Update") {
		t.Fatalf("Failed = %+v, Err = %v", r.Failed, r.Err)
	}
	for _, w := range []string{"pid 99", "pid 77", "4000"} {
		if !strings.Contains(r.Err.Error(), w) {
			t.Errorf("Err %q does not say %q", r.Err, w)
		}
	}
	if !strings.Contains(r.Recovery, "launchctl bootout gui/501/local.litellm.proxy") {
		t.Errorf("Recovery = %q", r.Recovery)
	}
	noSecrets(t, f, lines, r, envOf(t, f, plLiteLLM)["LITELLM_MASTER_KEY"])

	// LiteLLM answers but rejects the new key.
	f = apEnv(t)
	liteLLMUp(f, 77)
	f.getReplies["http://localhost:4000/v1/models"] = getReply{status: 401}
	r, _ = apply(t, f, p)
	if r.Failed == nil || r.Failed.Kind != StepWait || f.called("Config.SetPair") || !strings.Contains(r.Err.Error(), "rejected the new master key") {
		t.Errorf("rejected key: Failed = %+v, Err = %v", r.Failed, r.Err)
	}
}

// TestApplyRotateNeverWritesTheKeyAtALooseMode pins Important 1: the edited
// plist is written in one step without group or other bits (0644 is written
// 0600, never 0644 first), the backup (which holds the salt key and any
// DATABASE_URL) is created without them, an 0600 plist stays 0600 and a
// stricter one (0400) is never loosened.
func TestApplyRotateNeverWritesTheKeyAtALooseMode(t *testing.T) {
	for _, c := range []struct{ from, want os.FileMode }{{0o644, 0o600}, {0o600, 0o600}, {0o400, 0o400}, {0o755, 0o700}} {
		f := rotEnv(t)
		f.modes[plLiteLLM] = c.from
		st := rotateState()
		st.LiteLLMPlist.Mode = c.from
		r, _ := apply(t, f, mustPlan(t, st, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}, AdminWant: AdminWant{LiteLLM: LiteLLMRotateKey}}))
		if r.Err != nil {
			t.Fatalf("%04o: %v", c.from, r.Err)
		}
		var writes []string
		for _, call := range f.calls {
			if strings.HasPrefix(call, "FS.WriteAtomic "+plLiteLLM) {
				writes = append(writes, call)
			}
		}
		if want := fmt.Sprintf("FS.WriteAtomic %s %04o", plLiteLLM, c.want); len(writes) != 1 || writes[0] != want {
			t.Errorf("%04o: plist writes %q, want only %q", c.from, writes, want)
		}
		if m := f.modes[plLiteLLM]; m&0o077 != 0 || (c.from&0o077 == 0 && m != c.from) {
			t.Errorf("%04o: plist ends %04o", c.from, m)
		}
		b := backupsOf(f, plLiteLLM)
		if len(b) != 1 || f.modes[b[0]]&0o077 != 0 || f.modes[b[0]] != c.from&^0o077 {
			t.Errorf("%04o: backup %q mode %04o", c.from, b, f.modes[b[0]])
		}
	}
}

// TestApplyRotateWaitsForLiteLLMWithTheNewKey pins minor (i): after the
// LiteLLM restart the rotation waits for it to answer and accept the new key
// before restarting wt-model; one that does not come back stops the apply
// with plist and pair agreeing, wt-model untouched, and a Recovery naming
// the log and the wt-model restart.
func TestApplyRotateWaitsForLiteLLMWithTheNewKey(t *testing.T) {
	f := rotEnv(t)
	f.getReplies = nil
	f.getStatus = 502
	r, _ := apply(t, f, mustPlan(t, rotateState(), Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}, AdminWant: AdminWant{LiteLLM: LiteLLMRotateKey}}))
	if r.Failed == nil || r.Failed.Kind != StepWait || r.Failed.Action != ActionWaitLiteLLM {
		t.Fatalf("Failed = %+v, Err = %v", r.Failed, r.Err)
	}
	if k := envOf(t, f, plLiteLLM)["LITELLM_MASTER_KEY"]; k == apOldMaster || f.pair.APIKey != k {
		t.Errorf("plist and pair disagree")
	}
	if f.called("Launchd.Kickstart " + AgentLabel) {
		t.Errorf("wt-model restarted although LiteLLM did not come back")
	}
	for _, w := range []string{plHome + "/.litellm.err.log", "launchctl kickstart -k gui/501/" + AgentLabel} {
		if !strings.Contains(r.Err.Error()+r.Recovery, w) {
			t.Errorf("Err/Recovery do not say %q: %v / %q", w, r.Err, r.Recovery)
		}
	}
}

// TestApplyBootoutWaitsUntilTheLabelIsGone pins carry-forward 2: after a
// bootout launchd can still list the label for a moment, and a bootstrap then
// fails with an I/O error. The bootout step polls Print (on the fake clock)
// until the label is gone, and gives up with a message after BootoutTimeout,
// never bootstrapping.
func TestApplyBootoutWaitsUntilTheLabelIsGone(t *testing.T) {
	plan := func() Plan {
		st := baseState()
		st.Listener = ListenerState{Port: 8788}
		return mustPlan(t, st, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}, AdminWant: AdminWant{Listen: "127.0.0.1:8788"}})
	}
	f := apEnv(t)
	f.files[plLocalTok], f.modes[plLocalTok] = []byte("tok\n"), 0o600
	f.loadedAfterBootout = 3
	began := time.Now()
	r, _ := apply(t, f, plan())
	if r.Err != nil {
		t.Fatalf("Apply: %v", r.Err)
	}
	var seq []string
	for _, c := range f.calls {
		if strings.HasPrefix(c, "Launchd.") || strings.HasPrefix(c, "Host.Sleep") {
			seq = append(seq, c)
		}
	}
	want := []string{"Launchd.Bootout " + AgentLabel,
		"Launchd.Print " + AgentLabel, "Host.Sleep " + BootoutPoll.String(),
		"Launchd.Print " + AgentLabel, "Host.Sleep " + BootoutPoll.String(),
		"Launchd.Print " + AgentLabel, "Host.Sleep " + BootoutPoll.String(),
		"Launchd.Print " + AgentLabel,
		"Launchd.Bootstrap " + plAgentPath}
	if len(seq) < len(want) || strings.Join(seq[:len(want)], "|") != strings.Join(want, "|") {
		t.Errorf("launchd sequence:\n got %q\nwant %q", seq, want)
	}

	f = apEnv(t)
	f.loadedAfterBootout = 1 << 20
	r, _ = apply(t, f, plan())
	if time.Since(began) > 5*time.Second {
		t.Errorf("the poll slept for real")
	}
	if r.Failed == nil || r.Failed.Kind != StepBootout || f.called("Launchd.Bootstrap") {
		t.Fatalf("Failed = %+v, Err = %v", r.Failed, r.Err)
	}
	for _, w := range []string{AgentLabel, BootoutTimeout.String(), "launchctl print gui/501/" + AgentLabel} {
		if !strings.Contains(r.Err.Error(), w) {
			t.Errorf("Err %q does not say %q", r.Err, w)
		}
	}
}

// TestApplyRecoveryAfterABootoutTimeoutBootstrapsTheNewAgent pins that when
// the pair is set and the agent plist was replaced but launchd never unloads
// the old agent (the bootout poll times out), Recovery tells the user to
// bootout and bootstrap the new plist: a kickstart would restart the old
// definition and the new plist would never load. A failed wt-model kickstart
// (the old definition is the one to restart) still recommends kickstart.
func TestApplyRecoveryAfterABootoutTimeoutBootstrapsTheNewAgent(t *testing.T) {
	plan := func() Plan {
		p := mustPlan(t, rotateState(), Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}, AdminWant: AdminWant{LiteLLM: LiteLLMRotateKey}})
		spec := plSpec()
		p.Agent = &spec
		var steps []Step
		for _, s := range p.Steps {
			if s.Kind == StepKickstart {
				steps = append(steps,
					Step{Kind: StepPlist, Path: plAgentPath},
					Step{Kind: StepBootout, Path: plAgentPath},
					Step{Kind: StepBootstrap, Path: plAgentPath})
				continue
			}
			steps = append(steps, s)
		}
		p.Steps = steps
		return p
	}
	f := rotEnv(t)
	f.loadedAfterBootout = 1 << 20
	r, _ := apply(t, f, plan())
	if r.Failed == nil || r.Failed.Kind != StepBootout || !f.called("Config.SetPair") {
		t.Fatalf("Failed = %+v, Err = %v", r.Failed, r.Err)
	}
	for _, w := range []string{"launchctl bootout gui/501/" + AgentLabel, "launchctl bootstrap gui/501 " + plAgentPath} {
		if !strings.Contains(r.Recovery, w) {
			t.Errorf("Recovery %q does not say %q", r.Recovery, w)
		}
	}
	if strings.Contains(r.Recovery, "kickstart -k gui/501/"+AgentLabel) {
		t.Errorf("Recovery %q still recommends kickstarting wt-model", r.Recovery)
	}

	// The wt-model kickstart failing keeps the kickstart advice.
	f = rotEnv(t)
	f.kickstartErr[AgentLabel] = errors.New("Could not find service")
	r, _ = apply(t, f, mustPlan(t, rotateState(), Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}, AdminWant: AdminWant{LiteLLM: LiteLLMRotateKey}}))
	if r.Failed == nil || r.Failed.Kind != StepKickstart || !strings.Contains(r.Recovery, "kickstart -k gui/501/"+AgentLabel) {
		t.Errorf("kickstart failure: Failed = %+v, Recovery %q", r.Failed, r.Recovery)
	}
}

// TestNoPlanOrResultTextContainsAKey generates real-shaped keys through the
// fake RandRead (create and rotate, success and failure) and asserts no key
// appears in Format, notes, steps, any fmt verb of Plan, Desired, Step or the
// spec, the Result (errors, recovery, notes), progress lines or recorded
// calls (the argv launchctl would get).
func TestNoPlanOrResultTextContainsAKey(t *testing.T) {
	type run struct {
		name  string
		f     *fakeEnv
		st    State
		want  Desired
		fails func(*fakeEnv)
	}
	mk := func() []run {
		c := apEnv(t)
		liteLLMUp(c, 77)
		c2 := apEnv(t)
		c2.getStatus = 500
		r2 := rotEnv(t)
		r2.kickstartErr["local.litellm.proxy"] = errors.New("boom")
		return []run{
			{"create", c, createState(), Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}, AdminWant: AdminWant{LiteLLM: LiteLLMCreate}}, nil},
			{"create down", c2, createState(), Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}, AdminWant: AdminWant{LiteLLM: LiteLLMCreate}}, nil},
			{"rotate", rotEnv(t), rotateState(), Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}, AdminWant: AdminWant{LiteLLM: LiteLLMRotateKey}}, nil},
			{"rotate restart fails", r2, rotateState(), Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}, AdminWant: AdminWant{LiteLLM: LiteLLMRotateKey}}, nil},
		}
	}
	for _, c := range mk() {
		p := mustPlan(t, c.st, c.want)
		var oldKeys []string // a rotation's old keys, read from the plist first
		if raw, ok := c.f.files[plLiteLLM]; ok {
			if sc, err := scanEnv(raw); err == nil {
				oldKeys = []string{sc.vars["LITELLM_MASTER_KEY"], sc.vars["LITELLM_SALT_KEY"]}
			}
		}
		r, lines := apply(t, c.f, p)
		vars := envOf(t, c.f, plLiteLLM)
		keys := append([]string{vars["LITELLM_MASTER_KEY"], vars["LITELLM_SALT_KEY"], c.f.pair.APIKey}, oldKeys...)
		if keys[0] == "" || keys[0] == apOldMaster {
			t.Fatalf("%s: no new key was generated", c.name)
		}
		text := p.Format() + strings.Join(p.Notes, "\n") + fmt.Sprintf("%v %+v %#v %v %+v %#v", p, p, p, p.Want, p.Want, p.Want)
		for _, s := range p.Steps {
			text += fmt.Sprintf("%v %+v %#v", s, s, s)
		}
		if p.LiteLLM != nil {
			text += fmt.Sprintf("%v %+v %#v", *p.LiteLLM, *p.LiteLLM, *p.LiteLLM)
		}
		text += fmt.Sprintf("%v %+v %#v %s", r, r, r, r.Recovery) + strings.Join(r.Notes, "\n") + strings.Join(r.Warnings, "\n")
		if r.Err != nil {
			text += r.Err.Error()
		}
		for _, k := range keys {
			if k != "" && strings.Contains(text, k) {
				t.Errorf("%s: a key shows in plan or result text", c.name)
			}
		}
		noSecrets(t, c.f, lines, r, keys...)
		// Neither does the hex of the key without its prefix.
		if h := strings.TrimPrefix(keys[0], "sk-"); strings.Contains(text, h) {
			t.Errorf("%s: the key's hex shows", c.name)
		}
	}
}

// TestApplyCreateCountsADanglingSymlinkAsPresent pins that create's "no
// plist exists" check does not follow a final symlink: a dangling link at the
// LiteLLM plist path (a dotfiles tool's link to a file not there yet) is a
// plist that exists, so create refuses before generating a key, writes
// nothing and leaves the pair alone, instead of renaming a regular file over
// the link with new keys in it.
func TestApplyCreateCountsADanglingSymlinkAsPresent(t *testing.T) {
	p := mustPlan(t, createState(), Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}, AdminWant: AdminWant{LiteLLM: LiteLLMCreate}})
	f := apEnv(t)
	liteLLMUp(f, 77)
	f.dangling = map[string]bool{plLiteLLM: true}
	r, lines := apply(t, f, p)
	if r.Failed == nil || r.Failed.Kind != StepLiteLLMPlist {
		t.Fatalf("Failed = %+v, Err = %v", r.Failed, r.Err)
	}
	if !strings.Contains(r.Err.Error(), plLiteLLM+" exists now") {
		t.Errorf("Err = %v", r.Err)
	}
	if f.called("FS.WriteAtomic "+plLiteLLM) || f.called("Config.SetPair") || f.called("Launchd.Bootstrap "+plLiteLLM) {
		t.Errorf("wrote through a dangling link: %q", f.mutations())
	}
	noSecrets(t, f, lines, r)
}
