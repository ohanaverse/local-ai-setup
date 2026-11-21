package modelmode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/urlrule"
)

// plPasted is a stand-in pasted token for the Secret test (copied from
// modelsetup/plan_test.go, whose plans have their own copy).
const plPasted = "tok-PASTED-SECRET-42"

// TestSecretRedactsEveryPrintedForm pins that a Secret never prints or
// marshals its value, so a stray %v, %#v, log or JSON dump cannot leak the
// pasted token or master key; only Reveal returns it. (Moved unchanged from
// modelsetup: Secret itself now lives here.)
func TestSecretRedactsEveryPrintedForm(t *testing.T) {
	s := Secret(plPasted)
	txt, _ := s.MarshalText()
	js, _ := json.Marshal(struct{ S Secret }{s})
	for _, out := range []string{fmt.Sprint(s), fmt.Sprintf("%v %+v %#v %s %q", s, s, s, s, s), string(txt), string(js)} {
		if strings.Contains(out, plPasted) || !strings.Contains(out, "[redacted]") {
			t.Errorf("printed form %q", out)
		}
	}
	if s.Reveal() != plPasted {
		t.Errorf("Reveal = %q", s.Reveal())
	}
	if Secret("").String() != "" {
		t.Errorf("an empty secret should print empty, so absence stays visible")
	}
}

// TestReasonSaysNothingWasChanged pins the refusal-reason words byte for
// byte: fact, way out, "nothing was changed"; the credentials reason is the
// fixed url sentence under the same three parts; RefusalError joins its
// reasons with newlines.
func TestReasonSaysNothingWasChanged(t *testing.T) {
	if got := Reason("a", "b"); got != "a; b; nothing was changed" {
		t.Errorf("Reason = %q", got)
	}
	want := "the url contains an @ (read as a user:password); wt does not store credentials in config.toml; use a token file; nothing was changed"
	if got := CredentialsReason("the url"); got != want {
		t.Errorf("CredentialsReason = %q, want %q", got, want)
	}
	if got := (&RefusalError{Reasons: []string{"x", "y"}}).Error(); got != "x\ny" {
		t.Errorf("RefusalError.Error() = %q, want %q", got, "x\ny")
	}
}

// TestStepLabelKnowsOnlyTheSessionKinds pins the split vocabulary: modelmode
// labels the two kinds the session side writes (token file, [model-server]);
// every admin kind (plist, bootout, …) answers unknown here and is labelled
// by modelsetup's own stepLabel.
func TestStepLabelKnowsOnlyTheSessionKinds(t *testing.T) {
	if l, ok := StepLabel(StepToken); !ok || l != "token file" {
		t.Errorf("StepLabel(StepToken) = (%q, %t), want (\"token file\", true)", l, ok)
	}
	if l, ok := StepLabel(StepConfig); !ok || l != "write [model-server]" {
		t.Errorf("StepLabel(StepConfig) = (%q, %t), want (\"write [model-server]\", true)", l, ok)
	}
	for _, k := range []StepKind{"plist", "bootout"} {
		if l, ok := StepLabel(k); ok || l != "" {
			t.Errorf("StepLabel(%q) = (%q, %t), want (\"\", false)", k, l, ok)
		}
	}
}

// TestFormatNamesEveryStepAndNote pins Format's exact words: the mode, the
// step count in singular and plural, the numbered steps and the notes
// section — the admin goldens and the editor's plan screen both render it.
func TestFormatNamesEveryStepAndNote(t *testing.T) {
	if got := (Plan{Want: Desired{Mode: ModeNative}}).Format(); got != "nothing to change for mode native\n" {
		t.Errorf("empty: %q", got)
	}
	if got := (Plan{Want: Desired{Mode: ModeRemote}, Steps: []Step{{Detail: "a"}, {Detail: "b"}}, Notes: []string{"n"}}).Format(); got != "set up mode remote in 2 steps:\n  1. a\n  2. b\nleft alone and notes:\n  - n\n" {
		t.Errorf("two steps: %q", got)
	}
	if got := (Plan{Want: Desired{Mode: ModeRemote}, Steps: []Step{{Detail: "a"}}}).Format(); got != "set up mode remote in 1 step:\n  1. a\n" {
		t.Errorf("one step: %q", got)
	}
}

// TestWritesURLNeedsAConfigStepAndAURL pins WritesURL: a plan writes the url
// only when it has a config step and a url to write.
func TestWritesURLNeedsAConfigStepAndAURL(t *testing.T) {
	if !(Plan{Want: Desired{URL: "u"}, Steps: []Step{{Kind: StepConfig}}}).WritesURL() {
		t.Errorf("config step with a url: want true")
	}
	if (Plan{Want: Desired{URL: "u"}, Steps: []Step{{Kind: StepToken}}}).WritesURL() {
		t.Errorf("token step: want false")
	}
	if (Plan{Want: Desired{URL: ""}, Steps: []Step{{Kind: StepConfig}}}).WritesURL() {
		t.Errorf("no url: want false")
	}
}

// ---- The plan-building fixtures: the test-only paths and states the moved
// modelsetup plan tests were written over. ----

const (
	plHome       = "/Users/user_name"
	plConfigHome = plHome + "/.config"
	plCfgPath    = plConfigHome + "/agent-wt/config.toml"
	plLocalTok   = plConfigHome + "/wt-model/server.token"
	plRemoteTok  = plConfigHome + "/agent-wt/remote-model-server.token"
)

// freshState is an untouched host as the session side reads it: a config
// path, both fixed token paths unseen, and the home directories.
func freshState() State {
	return State{
		Config:     ConfigState{Path: plCfgPath},
		TokenFiles: []TokenState{{Path: plLocalTok}, {Path: plRemoteTok}},
		Host:       HostState{Home: plHome, ConfigHome: plConfigHome},
	}
}

// localState is fresh with a stored local table already applied.
func localState() State {
	st := freshState()
	st.Config.Exists = true
	st.Config.Table = config.ModelServerState{Mode: "local", URL: "http://127.0.0.1:8787", TokenFile: plLocalTok}
	st.Resolution = Resolution{Mode: ModeLocal, Source: SourceStored}
	return st
}

// remoteStored is fresh with a stored remote table and resolution.
func remoteStored() State {
	st := freshState()
	st.Config.Exists = true
	st.Config.Table = config.ModelServerState{Mode: "remote", URL: "https://gpu.example:8787", TokenFile: plRemoteTok}
	st.Resolution = Resolution{Mode: ModeRemote, Source: SourceStored}
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

// mustPlan builds a plan, pins that the config step is last and that no
// pasted secret reaches any printed or marshalled form (the master key is
// modelsetup's: this package never sees one).
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
	for _, secret := range []string{plPasted} {
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

// ---- The moved plan tests: native, remote and the refusal gates that every
// mode passes. Local is now the local pointer. ----

// TestBuildPlanRefusesAParseErrorBeforeAnyStep pins that a config.toml with a
// syntax error refuses every mode, naming the path, since it is never
// overwritten.
func TestBuildPlanRefusesAParseErrorBeforeAnyStep(t *testing.T) {
	for _, m := range []Mode{ModeNative, ModeLocal, ModeRemote} {
		st := freshState()
		st.Config.Exists, st.Config.ParseErr = true, "toml: line 3: bad"
		p, err := BuildPlan(st, Desired{Mode: m, URL: "https://x", PastedToken: plPasted})
		mustRefuse(t, p, err, plCfgPath, "line 3")
	}
}

// TestBuildPlanRefusesAnUnreadableTableBeforeAnyStep pins that a
// [model-server] table that does not decode refuses every mode: the config
// step re-reads the whole file and would fail last, after the token, plist and
// launchd steps had already run.
func TestBuildPlanRefusesAnUnreadableTableBeforeAnyStep(t *testing.T) {
	for _, m := range []Mode{ModeNative, ModeLocal, ModeRemote} {
		st := freshState()
		st.Config.Exists, st.Config.TableErr = true, "toml: cannot load TOML value of type int64 into a Go string"
		p, err := BuildPlan(st, Desired{Mode: m, URL: "https://x", PastedToken: plPasted})
		mustRefuse(t, p, err, plCfgPath, "cannot be read", "int64")
	}
}

// TestBuildPlanRefusesAnUnknownMode pins that a mode other than the three is
// refused, since the config writer does not validate it.
func TestBuildPlanRefusesAnUnknownMode(t *testing.T) {
	p, err := BuildPlan(freshState(), Desired{Mode: "lokal"})
	mustRefuse(t, p, err, "lokal")
	p, err = BuildPlan(freshState(), Desired{})
	mustRefuse(t, p, err, "native, local or remote")
}

// TestBuildPlanRefusesAKeyElsewhereInTheFile pins that a key outside
// [model-server] that the writer cannot decode refuses every mode before any
// step: the config step is last, so it would fail only after every machine
// change before it.
func TestBuildPlanRefusesAKeyElsewhereInTheFile(t *testing.T) {
	st := freshState()
	st.Config.RestErr = "agents.name: incompatible types"
	for _, m := range []Mode{ModeNative, ModeLocal, ModeRemote} {
		if _, err := BuildPlan(st, Desired{Mode: m, URL: "https://gpu.example:8787", PastedToken: plPasted}); err == nil {
			t.Errorf("mode %s planned despite RestErr", m)
		}
	}
}

// TestBuildPlanRemoteRefusesAFailedCheckUnlessSavedUnchecked pins that a
// failed ping of the remote server refuses with the classified failure unless
// the user chose to save anyway.
func TestBuildPlanRemoteRefusesAFailedCheckUnlessSavedUnchecked(t *testing.T) {
	st := freshState()
	st.Server = ServerState{Checked: true, Kind: KindUnauthorized, Message: "401 from https://gpu.example:8787", URL: "https://gpu.example:8787/"}
	want := Desired{Mode: ModeRemote, URL: "https://gpu.example:8787", PastedToken: plPasted}
	p, err := BuildPlan(st, want)
	mustRefuse(t, p, err, "rejected the token", "save anyway")
	want.SaveUnchecked = true
	p = mustPlan(t, st, want)
	if !sameKinds(kinds(p), StepToken, StepConfig) {
		t.Errorf("steps = %v", kinds(p))
	}
	if p.Want.TokenFile != plRemoteTok || p.Want.URL != want.URL {
		t.Errorf("Want = %+v", p.Want)
	}
	if !strings.Contains(p.Format(), "not answer the check") {
		t.Errorf("Format does not say it was saved unchecked:\n%s", p.Format())
	}
	// A remote needs a url and a token.
	p, err = BuildPlan(freshState(), Desired{Mode: ModeRemote, PastedToken: plPasted})
	mustRefuse(t, p, err, "url")
	p, err = BuildPlan(freshState(), Desired{Mode: ModeRemote, URL: "https://x"})
	mustRefuse(t, p, err, "token")
	// A typed token file must exist with mode 0600.
	st = freshState()
	st.TokenFiles = append(st.TokenFiles, TokenState{Path: "/srv/tok", Exists: true, Loose: true})
	p, err = BuildPlan(st, Desired{Mode: ModeRemote, URL: "https://x", TokenFile: "/srv/tok"})
	mustRefuse(t, p, err, "/srv/tok", "chmod 600")
	st.TokenFiles[len(st.TokenFiles)-1].Loose = false
	p = mustPlan(t, st, Desired{Mode: ModeRemote, URL: "https://x", TokenFile: "/srv/tok"})
	if !sameKinds(kinds(p), StepConfig) {
		t.Errorf("existing token file: steps = %v", kinds(p))
	}
}

// TestBuildPlanRemoteReplacesADifferentPastedTokenOnlyWhenTheTextSaysSo pins
// that a pasted token over an existing file at the fixed path is a step whose
// text says "replace", and a fresh one says "write" without "replace".
func TestBuildPlanRemoteReplacesADifferentPastedTokenOnlyWhenTheTextSaysSo(t *testing.T) {
	want := Desired{Mode: ModeRemote, URL: "https://gpu.example:8787", PastedToken: plPasted}
	p := mustPlan(t, freshState(), want)
	s, ok := stepOf(p, StepToken)
	if !ok || s.Path != plRemoteTok || s.Action != ActionWritePastedToken {
		t.Fatalf("token step = %+v", s)
	}
	if strings.Contains(p.Format(), "replace") {
		t.Errorf("fresh file says replace:\n%s", p.Format())
	}
	st := freshState()
	st.TokenFiles = []TokenState{{Path: plRemoteTok, Exists: true}}
	p = mustPlan(t, st, want)
	s, _ = stepOf(p, StepToken)
	if s.Action != ActionReplacePastedToken || !strings.Contains(p.Format(), "replace "+plRemoteTok) {
		t.Errorf("existing file: step %+v\n%s", s, p.Format())
	}
}

// TestBuildPlanRemoteCheckOfAnotherURLIsNotAVerification pins that a ping of
// a different endpoint (the working local server, say) neither passes nor
// fails the remote gate: it reads as not checked, so the confirm text never
// implies the new server was verified.
func TestBuildPlanRemoteCheckOfAnotherURLIsNotAVerification(t *testing.T) {
	want := Desired{Mode: ModeRemote, URL: "https://typo.example:8787", PastedToken: plPasted}
	p := mustPlan(t, freshState(), want)
	if !strings.Contains(p.Format(), "was not checked") {
		t.Errorf("a ping of the local server counted as a check:\n%s", p.Format())
	}
	st := freshState()
	st.Server = ServerState{Checked: true, Kind: KindUnreachable, URL: "http://127.0.0.1:8787"}
	p = mustPlan(t, st, want)
	if !strings.Contains(p.Format(), "was not checked") {
		t.Errorf("a failed ping of another url:\n%s", p.Format())
	}
	// The same endpoint, written with a trailing slash or upper-case host, is
	// the same check.
	st.Server = ServerState{Checked: true, Kind: KindUnreachable, URL: "https://TYPO.example:8787/"}
	_, err := BuildPlan(st, want)
	var re *RefusalError
	if !errors.As(err, &re) {
		t.Errorf("a failed check of the same url did not refuse: %v", err)
	}
}

// TestBuildPlanRemoteCheckMatchesATypedURLWithCredentials pins that a typed
// url carrying a user:password is refused whatever its check said (a passed
// check of the same host, or a failed one): config.toml is not a secret
// file, so wt never stores the credentials; and that the same url without
// them still matches a check recorded of that plain url, so a passed check
// is not called unchecked.
func TestBuildPlanRemoteCheckMatchesATypedURLWithCredentials(t *testing.T) {
	want := Desired{Mode: ModeRemote, URL: "https://u:p@gpu.example:8787", PastedToken: plPasted}
	st := freshState()
	st.Server = ServerState{Checked: true, OK: true, URL: "https://gpu.example:8787"}
	p, err := BuildPlan(st, want)
	mustRefuse(t, p, err, "user:password")
	st.Server = ServerState{Checked: true, Kind: KindUnreachable, URL: "https://gpu.example:8787"}
	p, err = BuildPlan(st, want)
	mustRefuse(t, p, err, "user:password")
	want.URL = "https://gpu.example:8787/"
	st.Server = ServerState{Checked: true, OK: true, URL: "https://gpu.example:8787"}
	if p := mustPlan(t, st, want); strings.Contains(p.Format(), "was not checked") {
		t.Errorf("a passed check of the same host read as unchecked:\n%s", p.Format())
	}
}

// TestBuildPlanRemoteRefusesAURLWithCredentials pins that a typed url
// carrying a user:password is refused (config.toml is 0644, so wt never
// stores one), that the refusal is fixed text naming nothing of the url (no
// user, no password, not even the host), and that a plain url still plans.
func TestBuildPlanRemoteRefusesAURLWithCredentials(t *testing.T) {
	const user, pass = "alice-USER", "hunter2-PASSWORD"
	for _, u := range []string{
		"https://" + user + ":" + pass + "@gpu.example:8787",
		"https://" + user + "@gpu.example:8787",
	} {
		st := freshState()
		st.Server = ServerState{Checked: true, OK: true, URL: "https://gpu.example:8787"}
		p, err := BuildPlan(st, Desired{Mode: ModeRemote, URL: u, PastedToken: plPasted})
		mustRefuse(t, p, err, "user:password", "token file", "credentials")
		if err != nil && (strings.Contains(err.Error(), user) || strings.Contains(err.Error(), pass) || strings.Contains(err.Error(), "gpu.example")) {
			t.Errorf("refusal quotes the credentials: %q", err.Error())
		}
	}
	mustPlan(t, freshState(), Desired{Mode: ModeRemote, URL: "https://gpu.example:8787", PastedToken: plPasted, SaveUnchecked: true})
}

// TestPlanRefusesAPastedTokenOverADanglingLink pins that a pasted token aimed
// at a token path holding a link that does not resolve is refused when the
// plan is built, naming the link, instead of planning a "replace" that the
// write refuses only after the user confirmed it.
func TestPlanRefusesAPastedTokenOverADanglingLink(t *testing.T) {
	f := obsConfigured(t)
	st := Observe(context.Background(), f.Env(), ObserveOptions{})
	st.Host.ConfigHome = "/Users/user_name/.config"
	st.TokenFiles = []TokenState{{Path: RemoteTokenPath(st.Host.ConfigHome), Exists: true, Dangling: true, Err: RemoteTokenPath(st.Host.ConfigHome) + " is a symlink that does not resolve"}}
	p, err := BuildPlan(st, Desired{Mode: ModeRemote, URL: "http://m:1", PastedToken: Secret("tok")})
	if err == nil || !strings.Contains(err.Error(), "symlink that does not resolve") || len(p.Steps) != 0 {
		t.Errorf("pasted token over a dangling link: err %v, steps %d", err, len(p.Steps))
	}
}

// TestPlanReplacesAnUnreadableTokenWithAPastedOne pins that a token file that
// exists but cannot be read (not a dangling link) is still replaced by a
// pasted token: pasting is the remedy the file-path refusal names for it.
func TestPlanReplacesAnUnreadableTokenWithAPastedOne(t *testing.T) {
	f := obsConfigured(t)
	st := Observe(context.Background(), f.Env(), ObserveOptions{})
	st.Host.ConfigHome = "/Users/user_name/.config"
	st.TokenFiles = []TokenState{{Path: RemoteTokenPath(st.Host.ConfigHome), Exists: true, Err: "read: permission denied"}}
	p, err := BuildPlan(st, Desired{Mode: ModeRemote, URL: "http://m:1", PastedToken: Secret("tok")})
	if err != nil || len(p.Steps) == 0 {
		t.Errorf("pasted token over an unreadable file: err %v, steps %d", err, len(p.Steps))
	}
}

// TestPlanWritesURLIsData pins Plan.WritesURL: true exactly when the plan has
// a config step that writes a url (the local pointer and remote setups),
// false for native (which clears it) and for a plan that keeps the stored
// table.
func TestPlanWritesURLIsData(t *testing.T) {
	remote := mustPlan(t, freshState(), Desired{Mode: ModeRemote, URL: "https://gpu.example:8787", PastedToken: plPasted, SaveUnchecked: true})
	local := mustPlan(t, freshState(), Desired{Mode: ModeLocal})
	native := mustPlan(t, freshState(), Desired{Mode: ModeNative})
	kept := mustPlan(t, localState(), Desired{Mode: ModeLocal})
	for name, c := range map[string]struct {
		p    Plan
		want bool
	}{"remote": {remote, true}, "local": {local, true}, "native": {native, false}, "kept": {kept, false}} {
		if got := c.p.WritesURL(); got != c.want {
			t.Errorf("%s: WritesURL = %v, want %v (steps %v)", name, got, c.want, kinds(c.p))
		}
	}
}

// ---- The local pointer: local mode planned from the session side. ----

// TestLocalPointerKeepsStoredValuesOnlyWhenAlreadyLocal pins that a local
// plan keeps the stored url and token file only when the stored table is
// already local: a remote url must never become local's url, and a typed url
// or token file is never used — the pointer stores what this machine runs.
func TestLocalPointerKeepsStoredValuesOnlyWhenAlreadyLocal(t *testing.T) {
	defaultURL := "http://" + DefaultListen
	defaultTok := DefaultLocalTokenPath(plConfigHome)
	storedLocal := func(table config.ModelServerState) State {
		st := freshState()
		st.Config.Exists = true
		st.Config.Table = table
		st.Resolution = Resolution{Mode: ModeLocal, Source: SourceStored}
		return st
	}
	remoteGPU := remoteStored()
	remoteGPU.Config.Table.URL = "https://gpu.example:8787"
	remoteGPU.Config.Table.TokenFile = "/y/t"
	remoteAt := remoteStored()
	remoteAt.Config.Table.URL = urlrule.Hidden
	remoteAt.Config.URLHadCredentials = true
	native := freshState()
	native.Config.Exists = true
	native.Config.Table = config.ModelServerState{Mode: "native"}
	native.Resolution = Resolution{Mode: ModeNative, Source: SourceStored}
	unset := freshState()
	unset.Config.Table = config.ModelServerState{TokenFile: "/z/t"}

	for _, c := range []struct {
		name       string
		st         State
		url, tok   string
		configStep bool
		absent     []string
	}{
		{"stored local", storedLocal(config.ModelServerState{Mode: "local", URL: "http://127.0.0.1:9999", TokenFile: "/x/t"}), "http://127.0.0.1:9999", "/x/t", false, nil},
		{"stored local, no url", storedLocal(config.ModelServerState{Mode: "local", TokenFile: "/x/t"}), defaultURL, "/x/t", true, nil},
		{"stored local, ~ token", storedLocal(config.ModelServerState{Mode: "local", TokenFile: "~/t"}), defaultURL, plHome + "/t", true, nil},
		{"stored remote", remoteGPU, defaultURL, defaultTok, true, []string{"gpu.example", "/y/t"}},
		{"stored remote, at-url", remoteAt, defaultURL, defaultTok, true, nil},
		{"stored native", native, defaultURL, defaultTok, true, nil},
		{"mode unset", unset, defaultURL, defaultTok, true, nil},
	} {
		p, err := BuildPlan(c.st, Desired{Mode: ModeLocal, URL: "http://ignored:1", TokenFile: "/ignored"})
		if err != nil {
			t.Errorf("%s: BuildPlan: %v", c.name, err)
			continue
		}
		if p.Want.Mode != ModeLocal || p.Want.URL != c.url || p.Want.TokenFile != c.tok {
			t.Errorf("%s: Want = %+v, want url %q token %q", c.name, p.Want, c.url, c.tok)
		}
		n := 0
		for _, s := range p.Steps {
			if s.Kind == StepConfig {
				n++
			}
		}
		wantN := 0
		if c.configStep {
			wantN = 1
		}
		if n != wantN {
			t.Errorf("%s: %d config steps, want %d (steps %v)", c.name, n, wantN, kinds(p))
		}
		if c.configStep {
			// Built from the constant, never a literal of this test's own.
			if !strings.Contains(p.Format(), "url = \"http://"+DefaultListen+"\"") {
				t.Errorf("%s: Format does not write the default url:\n%s", c.name, p.Format())
			}
		}
		for _, s := range c.absent {
			if strings.Contains(p.Format(), s) {
				t.Errorf("%s: Format keeps %q:\n%s", c.name, s, p.Format())
			}
		}
	}
}

// TestBuildPlanLocalPointerIsANoteWhenAlreadyLocal pins that a local table
// already applied plans no config step and says both what it keeps and where
// the rest of the setup lives.
func TestBuildPlanLocalPointerIsANoteWhenAlreadyLocal(t *testing.T) {
	p, err := BuildPlan(localState(), Desired{Mode: ModeLocal})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(p.Steps) != 0 {
		t.Errorf("steps = %v, want none", kinds(p))
	}
	want := []string{
		"keep [model-server] in " + plCfgPath + " (already mode = \"local\")",
		"run `wt-model setup` to set up wt-model on this machine",
	}
	if len(p.Notes) != len(want) {
		t.Fatalf("Notes = %q, want %q", p.Notes, want)
	}
	for i := range want {
		if p.Notes[i] != want[i] {
			t.Errorf("Notes[%d] = %q, want %q", i, p.Notes[i], want[i])
		}
	}
}

// TestBuildPlanLocalPointerRefusesAStoredAtURL pins that a kept local url
// holding an '@' (Observe stores urlrule.Hidden whole) is refused alone: the
// pointer would keep it, and config.toml is not a secret file. A stored
// remote at-url is overwritten instead, never kept (the table-driven test
// above).
func TestBuildPlanLocalPointerRefusesAStoredAtURL(t *testing.T) {
	st := freshState()
	st.Config.Exists = true
	st.Config.Table = config.ModelServerState{Mode: "local", URL: urlrule.Hidden}
	st.Config.URLHadCredentials = true
	p, err := BuildPlan(st, Desired{Mode: ModeLocal})
	var re *RefusalError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v, want *RefusalError", err)
	}
	if len(p.Steps) != 0 {
		t.Errorf("refused plan has steps: %v", kinds(p))
	}
	if len(re.Reasons) != 1 || re.Reasons[0] != CredentialsReason("the stored url") {
		t.Errorf("Reasons = %q, want exactly [%q]", re.Reasons, CredentialsReason("the stored url"))
	}
}

// TestBuildPlanLocalPointerNeedsAConfigHomeForTheDefaultToken pins that the
// default token path needs a known config directory — but only when the
// default is needed: a stored local token file is kept without it, and a
// stored remote token file is not kept, so it still refuses.
func TestBuildPlanLocalPointerNeedsAConfigHomeForTheDefaultToken(t *testing.T) {
	st := freshState()
	st.Host.ConfigHome = ""
	p, err := BuildPlan(st, Desired{Mode: ModeLocal})
	mustRefuse(t, p, err, "set HOME or XDG_CONFIG_HOME")

	st.Config.Exists = true
	st.Config.Table = config.ModelServerState{Mode: "local", TokenFile: "/x/t"}
	if _, err := BuildPlan(st, Desired{Mode: ModeLocal}); err != nil {
		t.Errorf("stored local token file: %v", err)
	}

	st.Config.Table = config.ModelServerState{Mode: "remote", TokenFile: "/x/t"}
	p, err = BuildPlan(st, Desired{Mode: ModeLocal})
	mustRefuse(t, p, err, "set HOME or XDG_CONFIG_HOME")
}

// TestBuildPlanWithRunsTheHookInPlaceOfTheNote pins PlanHooks: a set hook
// runs where the leaving-local note would go (after the token step, before
// the config step) and replaces it, so modelsetup can plan the LaunchAgent
// and the kept local files without this package knowing they exist.
func TestBuildPlanWithRunsTheHookInPlaceOfTheNote(t *testing.T) {
	p, err := BuildPlanWith(localState(), Desired{Mode: ModeRemote, URL: "https://gpu.example:8787", PastedToken: plPasted, SaveUnchecked: true}, PlanHooks{
		LeavingLocal: func(p *Plan) {
			p.Notes = append(p.Notes, "HOOK")
			p.Steps = append(p.Steps, Step{Kind: "hook"})
		},
	})
	if err != nil {
		t.Fatalf("BuildPlanWith: %v", err)
	}
	if !sameKinds(kinds(p), StepToken, "hook", StepConfig) {
		t.Errorf("steps = %v, want [token hook config]", kinds(p))
	}
	text := p.Format()
	if !strings.Contains(text, "HOOK") || strings.Contains(text, "LaunchAgent is untouched") {
		t.Errorf("hook did not replace the note:\n%s", text)
	}
}

// ---- The four golden plans: native and remote from a stored remote table,
// the local pointer and native from local. ----

// goldenPlans are the four plans whose text is pinned by a golden file.
func goldenPlans(t *testing.T) map[string]Plan {
	remote := freshState()
	remote.TokenFiles[1].Exists = true
	return map[string]Plan{
		"plan_native.golden":            mustPlan(t, remoteStored(), Desired{Mode: ModeNative}),
		"plan_remote.golden":            mustPlan(t, remote, Desired{Mode: ModeRemote, URL: "https://gpu.example:8787", PastedToken: plPasted}),
		"plan_local_pointer.golden":     mustPlan(t, freshState(), Desired{Mode: ModeLocal}),
		"plan_native_from_local.golden": mustPlan(t, localState(), Desired{Mode: ModeNative}),
	}
}

// TestFormatMatchesTheGoldens pins the exact confirm text of the four plans,
// so any wording change is reviewed. Run with UPDATE_GOLDEN=1 to regenerate.
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

// TestLeavingLocalNamesTheAnsweringServer pins the strengthened note L1 when
// the local server answers (its check held in LocalServer: in the
// local→remote flow Server is the ping of the entered remote url), and the
// unchanged short note when it does not.
func TestLeavingLocalNamesTheAnsweringServer(t *testing.T) {
	st := localState()
	// The flow: Server is the check of the entered remote url, LocalServer
	// the local check carried in from before.
	st.Server = ServerState{URL: "https://gpu.example:8787", Checked: true, OK: true}
	st.LocalServer = ServerState{URL: "http://127.0.0.1:8787", Checked: true, OK: true}
	p, err := BuildPlan(st, Desired{Mode: ModeRemote, URL: "https://gpu.example:8787", PastedToken: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(p.Notes, "\n")
	for _, want := range []string{
		"wt-model's LaunchAgent is untouched",
		"the local server answers at http://127.0.0.1:8787 and will be unused",
		"next: stop it with x on `wt-model setup`",
		"apply native there to remove the LaunchAgent",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("answering server: notes %q lack %q", text, want)
		}
	}

	st.LocalServer = ServerState{URL: "http://127.0.0.1:8787", Checked: true, OK: false}
	p, err = BuildPlan(st, Desired{Mode: ModeRemote, URL: "https://gpu.example:8787", PastedToken: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	text = strings.Join(p.Notes, "\n")
	if text != "wt-model's LaunchAgent is untouched; remove it with `wt-model setup`" {
		t.Errorf("silent server: notes = %q, want the short note unchanged", text)
	}
}
