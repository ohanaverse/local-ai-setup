package modelsetup

import (
	"context"
	"errors"
	"fmt"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
)

// wantNotCheckedPairAtURL pins the fixed text every LiteLLM network check
// (and the port check, which reads the url) carries when the pair url holds
// an '@': the checks are not made, and the words hold none of the url.
const wantNotCheckedPairAtURL = "not checked: the pair url contains an @"

// pairFixedTexts are the fixed sentences a pair url with an '@' may be
// replaced by; they are taken out of an output before it is searched.
var pairFixedTexts = []string{
	"(hidden: the url contains an @)",
	"not checked: the pair url contains an @",
}

// pairObserve observes a local-mode host whose wt-model pair is raw with a
// key set, whose LiteLLM plist is readable (port 4000, a master key) and
// whose every Net.Get answers 200, so any probe of the pair url that ran
// would be recorded and would read as a success.
func pairObserve(t *testing.T, raw string) (*fakeEnv, State) {
	t.Helper()
	f := obsConfigured(t)
	f.pair = config.ProxyPair{URL: raw, APIKey: obsFakeKey, Source: config.ProxySourceHome}
	f.plistInfo = litellm.ProxyPlistInfo{Port: 4000, Mode: 0o600, EnvNames: []string{"LITELLM_MASTER_KEY"}}
	f.plistVars["LITELLM_MASTER_KEY"] = apPlistKey
	f.getStatus, f.getBody = 200, []byte(`{"data":[],"db":"connected"}`)
	return f, Observe(context.Background(), f.Env(), ObserveOptions{})
}

// pairScenario is one plan built from a State that observed the pair: the
// State it starts from (the observed Pair and LiteLLM checks laid over it),
// the request, and the fake Apply runs it on.
type pairScenario struct {
	name string
	st   func() State
	want Desired
	env  func(t *testing.T) *fakeEnv
}

// pairScenarios are every plan that keeps, re-sets or leaves the pair: local
// keeping a set pair, local re-pairing from the plist (use-existing-key) and
// from an entered key, tighten, create and rotate (which write a new pair),
// and native and remote (which leave local, keeping the pair).
func pairScenarios() []pairScenario {
	ap := func(t *testing.T) *fakeEnv { return apEnv(t) }
	return []pairScenario{
		{"local keep", baseState, Desired{Mode: modelclient.ModeLocal}, ap},
		{"local use-existing-key", baseState, Desired{Mode: modelclient.ModeLocal, LiteLLM: LiteLLMUseExistingKey}, ap},
		{"local entered key", func() State {
			st := baseState()
			st.LiteLLMPlist.HasMasterKey = false
			st.Pair.KeySet = false
			return st
		}, Desired{Mode: modelclient.ModeLocal, MasterKey: plMasterKey}, ap},
		{"tighten", func() State { st := baseState(); st.LiteLLMPlist.Mode = 0o644; return st },
			Desired{Mode: modelclient.ModeLocal, LiteLLM: LiteLLMTighten}, func(t *testing.T) *fakeEnv {
				f := apEnv(t)
				f.modes[plLiteLLM] = 0o644
				return f
			}},
		{"create", createState, Desired{Mode: modelclient.ModeLocal, LiteLLM: LiteLLMCreate}, func(t *testing.T) *fakeEnv {
			f := apEnv(t)
			liteLLMUp(f, 77)
			return f
		}},
		{"rotate", rotateState, Desired{Mode: modelclient.ModeLocal, LiteLLM: LiteLLMRotateKey}, rotEnv},
		{"native", baseState, Desired{Mode: modelclient.ModeNative}, ap},
		{"remote", baseState, Desired{Mode: modelclient.ModeRemote, URL: "gpu.example:8787", PastedToken: plPasted, SaveUnchecked: true}, ap},
	}
}

// pairSurfaces runs a pair url through every output it can reach and returns
// each by name, with the recorded calls of each fake: Observe's Pair and
// LiteLLM checks, then for every pair scenario the plan (Format, notes,
// steps) or its refusal and Apply's result, recovery, progress and notes. It
// also returns, per scenario, the fake Apply ran on, for the request and
// write checks.
func pairSurfaces(t *testing.T, raw string) (map[string]string, *fakeEnv, map[string]*fakeEnv) {
	t.Helper()
	out := map[string]string{}
	of, obs := pairObserve(t, raw)
	out["Observe Pair"] = fmt.Sprintf("%+v", obs.Pair)
	out["Observe LiteLLM"] = fmt.Sprintf("%+v", obs.LiteLLM)
	envs := map[string]*fakeEnv{}
	for _, sc := range pairScenarios() {
		st := sc.st()
		keySet := st.Pair.KeySet
		st.Pair = obs.Pair
		if st.Pair.URL == "" || sc.name == "create" {
			st.Pair = PairState{}
		}
		if !keySet {
			st.Pair.KeySet = false
		}
		st.LiteLLM = obs.LiteLLM
		p, err := BuildPlan(st, sc.want)
		if err != nil {
			out[sc.name+": refusal"] = err.Error()
			continue
		}
		text := p.Format() + "\n" + strings.Join(p.Notes, "\n")
		for _, s := range p.Steps {
			text += fmt.Sprintf("\n%+v", s)
		}
		out[sc.name+": plan"] = text
		f := sc.env(t)
		f.pair.URL = raw
		r, lines := apply(t, f, p)
		// Failed is a pointer: printed by value, so its address (hex
		// digits a two-byte piece can match) never reads as a leak.
		res := fmt.Sprintf("%+v", r.Done)
		if r.Failed != nil {
			res += "\n" + fmt.Sprintf("%+v", *r.Failed)
		}
		res += "\n" + r.Recovery + "\n" + strings.Join(r.Notes, "\n") + "\n" + strings.Join(r.Warnings, "\n")
		if r.Err != nil {
			res += "\n" + r.Err.Error()
		}
		out[sc.name+": apply"] = res
		out[sc.name+": progress"] = strings.Join(lines, "\n")
		envs[sc.name] = f
	}
	return out, of, envs
}

// pieceShown returns the first piece of raw (two bytes or longer, split on
// url punctuation and white space; compared ignoring case and also
// percent-decoded) that got shows and the plain-url baseline does not, or "".
// A piece the baseline shows too ("http", "8787") is ordinary plan text, not
// a leak; the credential pieces (USERX, PASSX, hunter2, ...) never are.
func pieceShown(raw, got, baseline string) string {
	for _, fixed := range pairFixedTexts {
		got = strings.ReplaceAll(got, fixed, "")
	}
	lower, base := strings.ToLower(got), strings.ToLower(baseline)
	for _, tok := range credTokens(raw) {
		forms := []string{tok}
		if dec, err := neturl.PathUnescape(tok); err == nil && dec != tok && len(dec) >= 2 {
			forms = append(forms, dec)
		}
		for _, form := range forms {
			lf := strings.ToLower(form)
			if strings.Contains(lower, lf) && !strings.Contains(base, lf) {
				return form
			}
		}
	}
	if strings.Contains(got, "@") {
		return "@"
	}
	return ""
}

// TestNoPieceOfAnAtPairURLReachesAnyOutput is the "no parsing, no partial
// echo" property for the wt-model LiteLLM pair url (issue #640), over the
// same hostile urls as the model-server url's property: with any of them as
// the stored pair, (a) no piece of it that a plain pair url's output does
// not hold appears in Observe's Pair or LiteLLM checks, in any plan's
// Format, notes or steps (local keep, use-existing-key, entered key,
// tighten, create, rotate, native, remote), or in Apply's result, recovery
// or progress; (b) no request is made to it: Observe makes no Net.Get at
// all, and Apply's only requests go to the url the plan derived itself
// (http://localhost:<port>); (c) the LiteLLM checks read as not made, with
// the fixed sentence; and a plan that writes the pair writes that clean url,
// never the '@' one.
func TestNoPieceOfAnAtPairURLReachesAnyOutput(t *testing.T) {
	if len(hostileAtURLs) < 60 {
		t.Fatalf("only %d hostile urls", len(hostileAtURLs))
	}
	baseline, bf, _ := pairSurfaces(t, "http://localhost:4000")
	if !bf.called("Net.Get http://localhost:4000/health/liveliness") {
		t.Fatalf("the plain pair url was not probed; the property would hold vacuously: %q", bf.calls)
	}
	for _, raw := range hostileAtURLs {
		got, of, envs := pairSurfaces(t, raw)
		for name, text := range got {
			if piece := pieceShown(raw, text, baseline[name]); piece != "" {
				t.Errorf("%s for %q shows %q:\n%s", name, raw, piece, text)
			}
		}
		if of.called("Net.Get") {
			t.Errorf("%q: Observe made a request: %q", raw, of.calls)
		}
		if len(envs) != len(pairScenarios()) {
			t.Errorf("%q: only %d of %d scenarios planned; the property would skip the rest", raw, len(envs), len(pairScenarios()))
		}
		// The plans that write the pair re-pair it with the url they derived
		// (DefaultLiteLLMPort or the plist's --port), never the stored one.
		for _, name := range []string{"create", "rotate", "local use-existing-key", "local entered key"} {
			if f := envs[name]; f == nil || !f.called("Config.SetPair url=true") {
				t.Errorf("%s %q: the pair was not written; the write check would hold vacuously", name, raw)
			}
		}
		for name, f := range envs {
			for _, c := range f.calls {
				if strings.HasPrefix(c, "Net.Get ") && !strings.HasPrefix(c, "Net.Get http://localhost:4000/") {
					t.Errorf("%s %q: a request not to the derived url: %q", name, raw, c)
				}
			}
			if f.called("Config.SetPair url=true") && f.pair.URL != "http://localhost:4000" {
				t.Errorf("%s %q: the pair was written with url %q, want the derived http://localhost:4000", name, raw, f.pair.URL)
			}
		}
	}
}

// TestObserveHidesAnAtPairURLAndChecksNothingOfIt pins Observe's side of the
// rule for the pair url: State.Pair.URL is the fixed placeholder (the key
// still reads as set: the pair is present, only unusable for display), and
// the LiteLLM checks run (Checked) but every one that reads the url carries
// the fixed not-checked sentence with no status, while the file checks that
// need no url (restart owed, plist mode) still run.
func TestObserveHidesAnAtPairURLAndChecksNothingOfIt(t *testing.T) {
	for _, raw := range append([]string{modelclient.HiddenURL}, hostileAtURLs...) {
		f, st := pairObserve(t, raw)
		if st.Pair.URL != modelclient.HiddenURL || !st.Pair.KeySet || st.Pair.Err != "" {
			t.Errorf("%q: Pair = %+v, want the url hidden and the key set", raw, st.Pair)
		}
		l := st.LiteLLM
		if !l.Checked {
			t.Errorf("%q: LiteLLM.Checked = false", raw)
		}
		for name, c := range map[string]Check{"reachable": l.Reachable, "database": l.Database, "key": l.KeyAccepted,
			"routes": l.RoutesInSync, "port": l.PortMatches} {
			if c != (Check{Detail: wantNotCheckedPairAtURL}) {
				t.Errorf("%q: %s = %+v, want not checked with %q", raw, name, c, wantNotCheckedPairAtURL)
			}
		}
		if l.PlistMode.Status != CheckOK || l.RestartOwed.Detail == wantNotCheckedPairAtURL {
			t.Errorf("%q: the file checks did not run: plist mode %+v restart %+v", raw, l.PlistMode, l.RestartOwed)
		}
		if f.called("Net.Get") || f.called("DesiredRoutes") {
			t.Errorf("%q: probed: %q", raw, f.calls)
		}
	}
}

// TestObservePairErrorWithAnAtIsHidden pins the defence on the pair's read
// error: a resolve error whose text holds an '@' (a TOML parse error may
// quote the line it stopped on) is replaced by fixed text, and one with none
// is kept as it was.
func TestObservePairErrorWithAnAtIsHidden(t *testing.T) {
	f := obsConfigured(t)
	f.pairErr = errors.New(`toml: line 1: expected '"' after url = "USERX:PASSX@hq`)
	st := Observe(context.Background(), f.Env(), ObserveOptions{})
	if st.Pair.Err != pairErrHidden || strings.Contains(st.Pair.Err, "USERX") {
		t.Errorf("Pair.Err = %q, want fixed text with nothing of the url", st.Pair.Err)
	}
	// The real error, not an invented one: config.ResolveProxy on a home
	// whose url is typed without quotes. BurntSushi quotes the token before
	// the '@' (found "USERX") and not the '@', so this is held at the source.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	cfgPath := filepath.Join(home, "wt-model", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte("[litellm]\nurl = USERX:PASSX@hq\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, realErr := config.ResolveProxy()
	if realErr == nil {
		t.Fatal("ResolveProxy() on an unquoted url = nil error, want a parse error")
	}
	f = obsConfigured(t)
	f.pairErr = realErr
	st = Observe(context.Background(), f.Env(), ObserveOptions{})
	if st.Pair.Err == "" || strings.Contains(st.Pair.Err, "USERX") || strings.Contains(st.Pair.Err, "PASSX") {
		t.Errorf("Pair.Err = %q, want an error with nothing of the url", st.Pair.Err)
	}
	f = obsConfigured(t)
	f.pairErr = errors.New("toml: line 1: bad")
	if st := Observe(context.Background(), f.Env(), ObserveOptions{}); st.Pair.Err != "toml: line 1: bad" {
		t.Errorf("Pair.Err = %q, want the error as it was", st.Pair.Err)
	}
}

// TestPlainPairURLIsShownAndCheckedAsBefore pins that the rule costs a pair
// url with no '@' nothing: Observe keeps it byte for byte and probes it, and
// the keep note names it as stored.
func TestPlainPairURLIsShownAndCheckedAsBefore(t *testing.T) {
	for _, raw := range []string{"http://localhost:4000", "http://127.0.0.1:4000/"} {
		f, st := pairObserve(t, raw)
		if st.Pair.URL != raw {
			t.Errorf("Pair.URL = %q, want %q", st.Pair.URL, raw)
		}
		base := strings.TrimRight(raw, "/")
		if !f.called("Net.Get "+base+"/health/liveliness") || st.LiteLLM.Reachable.Status != CheckOK {
			t.Errorf("%q: not probed as before: %q %+v", raw, f.calls, st.LiteLLM.Reachable)
		}
		b := baseState()
		b.Pair = st.Pair
		p := mustPlan(t, b, Desired{Mode: modelclient.ModeLocal})
		if !strings.Contains(p.Format(), "keep wt-model's LiteLLM pair ("+raw+")") {
			t.Errorf("%q: keep note missing:\n%s", raw, p.Format())
		}
	}
}
