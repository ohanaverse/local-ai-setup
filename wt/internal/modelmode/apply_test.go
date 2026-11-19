package modelmode

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// apEnv is a fake whose config file has the path a config step writes.
func apEnv(t *testing.T) *fakeEnv {
	f := newFakeEnv(t)
	f.configFile.Path = plCfgPath
	return f
}

// apply runs Apply and checks that every progress line is one line of ASCII.
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

// TestApplyRemoteWritesThePastedTokenBeforeTheConfig pins that a pasted
// remote token is written 0600 to the fixed path before the table names it,
// never appears in any call, line or result, and that an identical token
// already on disk is not rewritten. (Moved from modelsetup: the pasted-token
// half of applyToken lives here now.)
func TestApplyRemoteWritesThePastedTokenBeforeTheConfig(t *testing.T) {
	want := Desired{Mode: ModeRemote, URL: "https://gpu.example:8787", PastedToken: plPasted}
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

// TestApplyConfigRefusesAURLWithCredentials pins that a config step whose
// wanted url carries a user:password is refused before env.Config.Update, and
// that the refusal quotes nothing of the url. (Moved from modelsetup.)
func TestApplyConfigRefusesAURLWithCredentials(t *testing.T) {
	f := apEnv(t)
	p := Plan{Want: Desired{Mode: ModeRemote, URL: "https://u:pw-SECRET@gpu.example:8787", TokenFile: plRemoteTok}, Steps: []Step{{Kind: StepConfig, Path: plCfgPath}}}
	r, lines := apply(t, f, p)
	if r.Failed == nil || r.Failed.Kind != StepConfig || f.called("Config.Update") {
		t.Fatalf("Failed = %+v, Config.Update called %t", r.Failed, f.called("Config.Update"))
	}
	noSecrets(t, f, lines, r, "pw-SECRET")
}

// TestApplyTightensALooseTokenFile pins that a tighten action only chmods
// the token file to 0600 and keeps its content — the remote twin of
// modelsetup's local-plan test.
func TestApplyTightensALooseTokenFile(t *testing.T) {
	f := apEnv(t)
	f.files[plRemoteTok], f.modes[plRemoteTok] = []byte("tok-SECRET-0123456789\n"), 0o644
	p := Plan{Want: Desired{Mode: ModeRemote}, Steps: []Step{{Kind: StepToken, Path: plRemoteTok, Action: ActionTightenToken}}}
	r, _ := apply(t, f, p)
	if r.Err != nil {
		t.Fatalf("Apply: %v", r.Err)
	}
	wantCalls := []string{"FS.Chmod " + plRemoteTok + " 0600"}
	if got := f.mutations(); strings.Join(got, "|") != strings.Join(wantCalls, "|") {
		t.Errorf("calls = %q, want %q", got, wantCalls)
	}
	if f.modes[plRemoteTok] != 0o600 || string(f.files[plRemoteTok]) != "tok-SECRET-0123456789\n" {
		t.Errorf("mode = %04o content = %q, want 0600 and the old content", f.modes[plRemoteTok], f.files[plRemoteTok])
	}
}

// TestApplyRefusesEveryAdminStep pins that the session-side Apply can never
// run an admin step: each one fails on the spot with the fixed refusal, the
// config step behind it never runs, and nothing is written — and a
// generate-token step (modelsetup's action) is refused as an unknown token
// action, without a write.
func TestApplyRefusesEveryAdminStep(t *testing.T) {
	for _, k := range []string{"pair", "plist", "bootout", "bootstrap", "kickstart", "wait", "remove-plist", "litellm-plist", "backup", "master-key", "chmod", "kickstart-litellm"} {
		f := apEnv(t)
		p := Plan{Want: Desired{Mode: ModeLocal}, Steps: []Step{{Kind: StepKind(k)}, {Kind: StepConfig, Path: plCfgPath}}}
		r, _ := apply(t, f, p)
		if r.Failed == nil || r.Failed.Kind != StepKind(k) || r.Err == nil {
			t.Errorf("%s: Failed = %+v err = %v", k, r.Failed, r.Err)
			continue
		}
		if !strings.Contains(r.Err.Error(), "is not one Apply knows; nothing of it was done") {
			t.Errorf("%s: err = %q", k, r.Err.Error())
		}
		if f.called("Config.Update") {
			t.Errorf("%s: the config step behind it ran", k)
		}
		if m := f.mutations(); len(m) != 0 {
			t.Errorf("%s: mutations = %q, want none", k, m)
		}
	}
	f := apEnv(t)
	p := Plan{Want: Desired{Mode: ModeLocal}, Steps: []Step{{Kind: StepToken, Path: "/t", Action: "generate-token"}}}
	r, _ := apply(t, f, p)
	if r.Err == nil || !strings.Contains(r.Err.Error(), "token action \"generate-token\" is not one Apply knows") {
		t.Errorf("generate-token: err = %v", r.Err)
	}
	if m := f.mutations(); len(m) != 0 {
		t.Errorf("generate-token: mutations = %q, want none", m)
	}
}

// TestApplyWritesConfigWithTheWantedFields pins the config step through the
// seam: the stored table takes the plan's mode, url and token file, one
// progress line names it by its label, and nothing failed, so no Recovery is
// needed.
func TestApplyWritesConfigWithTheWantedFields(t *testing.T) {
	f := apEnv(t)
	p := Plan{Want: Desired{Mode: ModeRemote, URL: "https://gpu.example:8787", TokenFile: plRemoteTok}, Steps: []Step{{Kind: StepConfig, Path: plCfgPath}}}
	r, lines := apply(t, f, p)
	if r.Err != nil {
		t.Fatalf("Apply: %v", r.Err)
	}
	tbl := f.configFile.Table
	if tbl == nil || tbl.Mode != "remote" || tbl.URL != "https://gpu.example:8787" || tbl.TokenFile != plRemoteTok {
		t.Errorf("table = %+v", tbl)
	}
	if len(lines) != 1 || lines[0] != "step 1/1: write [model-server]" {
		t.Errorf("progress = %q", lines)
	}
	if r.Recovery != "" {
		t.Errorf("Recovery = %q, want empty", r.Recovery)
	}
}
