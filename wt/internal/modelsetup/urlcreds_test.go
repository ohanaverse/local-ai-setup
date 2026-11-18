package modelsetup

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
)

// credentialedURLs are typed urls that carry a user (and maybe a password),
// with and without a scheme. Each user part holds "USERX" and each password
// "PASSX", so a message that quotes either is caught.
var credentialedURLs = []string{
	"USERX:PASSX@gpu.example:8787",
	"uUSERX:pPASSX@host",
	"https://USERX:PASSX@gpu.example:8787",
	"https://USERX@gpu.example:8787",
	"USERX:PASSX:with-colon@gpu.example:8787",
	"USERX:PASSX@with-at@gpu.example:8787",
	"USERX:PASSX@gpu example:8787",
	"USERX:PASSX%40encoded@gpu.example:8787",
	"http://USERX:PASSX@[::1]:8787",
}

// bypassURLs are the typed urls that slipped past the parse-based reading of
// fb18fd78 and the span cut of 974d21c3: a leading "//" or "///", a password
// holding '/', '?', '#' or "://", and malformed scheme typos. Every one
// carries "USERX" and "PASSX" whole, so any output quoting either is caught.
var bypassURLs = []string{
	"//USERX:PASSX@gpu.example:8787",
	"///USERX:PASSX@gpu.example:8787",
	"USERX:PASSX/x@gpu.example:8787",
	"USERX:p/PASSX@gpu.example:8787",
	"USERX:1/PASSX@gpu.example:8787",
	"USERX:PASSX?x@gpu.example:8787",
	"USERX:PASSX#x@gpu.example:8787",
	"https://USERX:PASSX/x@gpu.example:8787",
	"http:/USERX:PASSX@gpu.example:8787",
	"http:///USERX:PASSX@gpu.example:8787",
	"http//USERX:PASSX@gpu.example:8787",
	"1http://USERX:PASSX@gpu.example:8787",
	"http:/\\USERX:PASSX@gpu.example:8787",
	"http:/ /USERX:PASSX@gpu.example:8787",
	"USERX:PASSX://@gpu.example:8787",
	"USERX:s3cr://PASSX@gpu.example:8787",
	"http://USERX:p@PASSX/x@gpu.example",
}

// atElsewhereURLs hold an '@' only in their path or query. They are refused
// too, by a deliberate fail-closed ruling: a model-server url has no
// legitimate '@', and telling a user from a path is exactly what the
// bypasses above got wrong.
var atElsewhereURLs = []string{
	"http://gpu.example:8787/path?x=a@b",
	"gpu.example:8787/path?x=a@b",
	"https://gpu.example:8787/a@b",
}

// everyCredentialedURL is every typed url the rule must refuse and hide.
func everyCredentialedURL() []string {
	all := append([]string{}, credentialedURLs...)
	return append(append(all, bypassURLs...), atElsewhereURLs...)
}

// plainURLs carry no '@' and must plan, and show, as typed: host:port with
// and without a path, loopback forms, IPv6, and urls with a scheme.
var plainURLs = []string{
	"gpu.example:8787",
	"gpu.example:8787/path",
	"localhost:8787",
	"127.0.0.1:8787",
	"[::1]:8787",
	"https://gpu.example:8787",
	"http://gpu.example:8787/",
	"https://gpu-1.example.com:8787",
	"http://[::1]:8787",
}

// leaks reports whether s quotes the test credentials or keeps an '@' other
// than the ones in the fixed hiddenURL and checkHidden texts.
func leaks(s string) bool {
	s = strings.ReplaceAll(strings.ReplaceAll(s, hiddenURL, ""), checkHidden, "")
	return strings.Contains(s, "USERX") || strings.Contains(s, "PASSX") || strings.Contains(s, "@")
}

// TestHasUserinfoIsAnyAt pins the whole rule, with no parsing: any typed url
// holding an ASCII '@' counts as credentialed, and a url with none never
// does. Fullwidth U+FF20 and %40 are not '@' (no dialler reads them as the
// user separator), so they stay allowed.
func TestHasUserinfoIsAnyAt(t *testing.T) {
	for _, raw := range append(everyCredentialedURL(), hostileAtURLs...) {
		if !hasUserinfo(raw) {
			t.Errorf("hasUserinfo(%q) = false, want true", raw)
		}
	}
	for _, raw := range append(plainURLs, "uq:pq\uff20h1", "uq:pq%40h1") {
		if hasUserinfo(raw) {
			t.Errorf("hasUserinfo(%q) = true for a url with no ASCII '@'", raw)
		}
	}
}

// TestBuildPlanRemoteRefusesASchemelessURLWithCredentials pins that every
// url with an '@', scheme or not, well-formed or not, is refused at plan time
// by the one fixed credentials reason, which echoes nothing of the url (no
// host either: naming one meant parsing, and every parse was bypassed),
// whether the url was not checked or its check failed; and that a plain url
// still plans.
func TestBuildPlanRemoteRefusesASchemelessURLWithCredentials(t *testing.T) {
	want := "the url contains an @ (read as a user:password); wt does not store credentials in config.toml; use a token file; nothing was changed"
	for _, raw := range everyCredentialedURL() {
		for _, server := range []ServerState{
			{},
			{Checked: true, Kind: KindUnreachable, Message: checkHidden, URL: hiddenURL},
		} {
			st := freshState()
			st.Server = server
			p, err := BuildPlan(st, Desired{Mode: modelclient.ModeRemote, URL: raw, PastedToken: plPasted, SaveUnchecked: true})
			if err == nil {
				t.Errorf("%q planned:\n%s", raw, p.Format())
				continue
			}
			if err.Error() != want {
				t.Errorf("%q: refusal %q, want the fixed %q", raw, err.Error(), want)
			}
		}
	}
	for _, raw := range plainURLs {
		mustPlan(t, freshState(), Desired{Mode: modelclient.ModeRemote, URL: raw, PastedToken: plPasted, SaveUnchecked: true})
	}
}

// TestShownURLHidesTheWholeURL pins the display form of a url (the check's
// "no token entered for" message, ServerState.URL, the table read from
// config.toml): a url holding an '@' becomes the fixed hiddenURL, whole (no
// host kept), and a url with no '@' comes back byte-identical.
func TestShownURLHidesTheWholeURL(t *testing.T) {
	for _, raw := range append(everyCredentialedURL(), hostileAtURLs...) {
		if got := shownURL(raw); got != hiddenURL {
			t.Errorf("shownURL(%q) = %q, want %q", raw, got, hiddenURL)
		}
	}
	for _, raw := range plainURLs {
		if got := shownURL(raw); got != raw {
			t.Errorf("shownURL(%q) = %q, want it unchanged", raw, got)
		}
	}
}

// TestRedactCheckHidesAnyQuote pins the check-message guard: a message that
// holds an '@', or any 3-byte run of a known url holding an '@', becomes the
// fixed checkHidden whole; a message about plain urls comes back unchanged,
// and so does one that shares no 3-byte run with the '@' url.
func TestRedactCheckHidesAnyQuote(t *testing.T) {
	for _, raw := range everyCredentialedURL() {
		for _, text := range []string{
			"dial " + raw + "/control/v1/ping: refused (" + raw + ")",
			"stray @ in an error",
			"dial " + strings.ReplaceAll(raw, "@", "%40") + ": refused",
		} {
			if got := redactCheck(text, raw); got != checkHidden {
				t.Errorf("redactCheck(%q, %q) = %q, want %q", text, raw, got, checkHidden)
			}
		}
		if got := redactCheck("dial "+strings.ReplaceAll(raw, "@", "")+": refused", "plain.test:1", raw); got != checkHidden {
			t.Errorf("redactCheck with %q among the known urls kept %q", raw, got)
		}
	}
	if got := redactCheck("timeout", "USERX:PASSX@hq"); got != "timeout" {
		t.Errorf("redactCheck hid a message sharing no run with the url: %q", got)
	}
	for _, raw := range plainURLs {
		text := "dial " + raw + ": refused"
		if got := redactCheck(text, raw, "", raw); got != text {
			t.Errorf("redactCheck(.., %q) = %q, want it unchanged", raw, got)
		}
	}
	if got := redactCheck("", "USERX@hq"); got != "" {
		t.Errorf("redactCheck of an empty message = %q", got)
	}
}

// TestApplyConfigRefusesASchemelessURLWithCredentials pins the apply-time
// backstop on the same rule as the plan: a hand-built plan whose config step
// would write a url with an '@', scheme or not, fails that step with the
// fixed text, never calls Config.Update, and nothing it prints (the failure,
// the Recovery) quotes the user or the password.
func TestApplyConfigRefusesASchemelessURLWithCredentials(t *testing.T) {
	want := "the url contains an @ (read as a user:password); wt does not store credentials in config.toml; use a token file; config.toml was not written"
	for _, raw := range everyCredentialedURL() {
		f := apEnv(t)
		p := Plan{Want: Desired{Mode: modelclient.ModeRemote, URL: raw, TokenFile: plRemoteTok},
			Steps: []Step{{Kind: StepConfig, Path: plCfgPath}}}
		r, lines := apply(t, f, p)
		if r.Failed == nil || r.Failed.Kind != StepConfig || f.called("Config.Update") {
			t.Errorf("%q: Failed = %+v, Config.Update called %t", raw, r.Failed, f.called("Config.Update"))
			continue
		}
		if r.Err == nil || !strings.Contains(r.Err.Error(), want) {
			t.Errorf("%q: Err = %v, want the fixed %q", raw, r.Err, want)
		}
		noSecrets(t, f, lines, r, "USERX")
		noSecrets(t, f, lines, r, "PASSX")
	}
}

// TestHiddenURLIsNeverWrittenToConfig pins that the placeholder a hidden url
// shows as can never become a stored url: it holds an '@', so a plan of it
// (the form prefills it from State) is refused and Apply's backstop refuses
// a hand-built config step with it, Config.Update never called; while a
// plan that retypes a plain url, or native (which clears the url), writes
// no placeholder.
func TestHiddenURLIsNeverWrittenToConfig(t *testing.T) {
	if !hasUserinfo(hiddenURL) {
		t.Fatalf("hiddenURL %q holds no '@', so the refusal would not catch it", hiddenURL)
	}
	f := obsConfigured(t)
	f.configFile.Table.Mode, f.configFile.Table.URL = "remote", "https://USERX:PASSX@gpu.example:8787/"
	st := Observe(context.Background(), f.Env(), ObserveOptions{})
	if st.Config.Table.URL != hiddenURL || !st.Config.URLHadCredentials {
		t.Fatalf("Config = %+v, want the url hidden and flagged", st.Config)
	}
	p, err := BuildPlan(st, Desired{Mode: modelclient.ModeRemote, URL: st.Config.Table.URL, PastedToken: "tok-NEW", SaveUnchecked: true})
	mustRefuse(t, p, err, "user:password", "token file")
	a := apEnv(t)
	r, _ := apply(t, a, Plan{Want: Desired{Mode: modelclient.ModeRemote, URL: hiddenURL, TokenFile: plRemoteTok},
		Steps: []Step{{Kind: StepConfig, Path: plCfgPath}}})
	if r.Failed == nil || a.called("Config.Update") {
		t.Errorf("Apply wrote the placeholder: Failed %+v", r.Failed)
	}
	for _, want := range []Desired{
		{Mode: modelclient.ModeRemote, URL: "gpu.example:8787", PastedToken: "tok-NEW", SaveUnchecked: true},
		{Mode: modelclient.ModeNative},
	} {
		p := mustPlan(t, st, want)
		if strings.Contains(p.Format(), "hidden") || p.Want.URL == hiddenURL {
			t.Errorf("%s plan carries the placeholder:\n%s", want.Mode, p.Format())
		}
		a := apEnv(t)
		a.configFile.Table = &config.ModelServerState{Mode: "remote", URL: "https://USERX:PASSX@gpu.example:8787/"}
		if r, _ := apply(t, a, Plan{Want: p.Want, Steps: []Step{{Kind: StepConfig, Path: plCfgPath}}}); r.Failed != nil {
			t.Fatalf("%s: apply failed: %v", want.Mode, r.Err)
		}
		if got := a.configFile.Table.URL; got != p.Want.URL || strings.Contains(got, "hidden") {
			t.Errorf("%s: stored url = %q, want %q", want.Mode, got, p.Want.URL)
		}
	}
}

// TestPlainURLsPassUntouched pins that the rule costs a url with no '@'
// nothing: not refused, and the stored, shown and checked value is
// byte-identical to what was typed or stored.
func TestPlainURLsPassUntouched(t *testing.T) {
	for _, raw := range plainURLs {
		p := mustPlan(t, freshState(), Desired{Mode: modelclient.ModeRemote, URL: raw, PastedToken: plPasted, SaveUnchecked: true})
		if p.Want.URL != raw || !strings.Contains(p.Format(), `url = "`+raw+`"`) {
			t.Errorf("%q: plan Want.URL %q, Format:\n%s", raw, p.Want.URL, p.Format())
		}
		f := obsConfigured(t)
		f.configFile.Table.Mode, f.configFile.Table.URL = "remote", raw
		st := Observe(context.Background(), f.Env(), ObserveOptions{})
		if st.Config.Table.URL != raw || st.Server.URL != raw || st.Config.URLHadCredentials {
			t.Errorf("%q: Table.URL %q, Server.URL %q, URLHadCredentials %t", raw, st.Config.Table.URL, st.Server.URL, st.Config.URLHadCredentials)
		}
		f = obsConfigured(t)
		f.pingErr = &modelclient.UnreachableError{Addr: raw, Reason: "connection refused"}
		st = Observe(context.Background(), f.Env(), ObserveOptions{CheckURL: raw, CheckToken: Secret("tok")})
		if st.Server.URL != raw || st.Server.Message == checkHidden {
			t.Errorf("%q: Server = %+v, want the url and message as they were", raw, st.Server)
		}
	}
}

// TestObserveCheckMessageDropsTheTypedCredentials pins that a failed check of
// a typed url with an '@', scheme or not, keeps nothing of it in
// ServerState: the url is the fixed hiddenURL and the message (modelclient's
// errors quote the url they dialled, and the tab shows it) is the fixed
// checkHidden.
func TestObserveCheckMessageDropsTheTypedCredentials(t *testing.T) {
	for _, raw := range everyCredentialedURL() {
		f := obsConfigured(t)
		f.pingErr = &modelclient.UnreachableError{Addr: raw, Reason: "dial " + raw + "/control/v1/ping: refused"}
		st := Observe(context.Background(), f.Env(), ObserveOptions{CheckURL: raw, CheckToken: Secret("tok")})
		if !st.Server.Checked || st.Server.OK || st.Server.URL != hiddenURL || st.Server.Message != checkHidden {
			t.Errorf("%q: Server = %+v, want a failed check with the url and message hidden", raw, st.Server)
		}
		if s := fmt.Sprintf("%+v", st.Server); leaks(s) {
			t.Errorf("%q: Server quotes the credentials: %s", raw, s)
		}
	}
}

// hostileAtURLs feed TestNoPieceOfAnAtURLReachesAnyOutput: every earlier
// bypass of the span-cutting design, with distinctive user and password
// tokens (USERX, PASSX, s3cr, hunter2, ...) that no fixed text holds. The
// review's "admin:s3cr://et@..." is spelled "etQ" and a "file:" scheme
// "smbq:", because "et" and "file" occur in ordinary plan text ("set up",
// "token file") and would read as a leak that is not one.
var hostileAtURLs = []string{
	"//USERX:PASSX@hq", "///USERX:PASSX@hq", "USERX:p/PASSX@hq", "USERX:p?PASSX@hq",
	"USERX:p#PASSX@hq", "http:/USERX:PASSX@hq", "http:///USERX:PASSX@hq",
	"http//USERX:PASSX@hq", "1http://USERX:PASSX@hq", "http:/\\USERX:PASSX@hq",
	"http:/ /USERX:PASSX@hq",
	"admin:s3cr://etQ@gpu.example:8787", "admin:hunter2://@gpu.example:8787",
	"USERX:PASSX://@hq", " USERX:PASSX://@hq", "USERX2:PASSX2://@hq", "uq:p://q@h1",
	"USERX:PASSX@gpu.example:8787", "https://USERX:PASSX@gpu.example:8787",
	"https://USERX@gpu.example:8787", "USERX@hostq", "USERX:@hostq", "u:p@[::1]:8787",
	"USERX:PASSX@[::1]:8787", "http://USERX:PASSX@[::1]:8787/",
	"@", "@@", "@hostq", "USERX@", "USERX:PASSX@", "@USERX:PASSX",
	"USERX:PASSX@hostq@other9", "USERX@PASSX@hostq", "a1@b2@c3@d4",
	"USERX:PASS\tX@hostq", "USERX:PASSX\n@hostq", "USERX\n:PASSX@hostq\n",
	"USERX:PASSX%40enc@hostq", "USERX%3APASSX@hostq", "http://USERX:PASSX%40w@hostq",
	"http://USERX:p@PASSX/x@gpu.example", "USERX:p@PASSX/x@gpu.example:8787",
	"https://USERX@PASSX?q@hostq", "USERX:PASSX@hostq:8787#fr@other9",
	"http://hostq/path?x=a@b9", "hostq:8787/path?x=USERX@PASSX", "https://hostq:8787/a@b9",
	"\\\\USERX:PASSX@hostq", "http:\\\\USERX:PASSX@hostq", "smbq:///USERX:PASSX@hostq",
	"ftp://USERX:PASSX@hostq", "HTTP://USERX:PASSX@HOSTQ", "javascript://USERX:PASSX@hostq",
	"http://USERX:PASSX@hostq?next=http://vq9:wq9@other9", "http://aq9/bq9://cq9@d19",
	"aq9@bq9://cq9@d19", "USERX:PASSX@@hostq", "USERX:PASSX@:8787", "USERX:PASSX@/zz9",
	"http://[USERX:PASSX@::1]:8787", "admin:s3cr://etQ@gpu.example:8787/v1?k=v#f",
	"USERX:\xff\xfePASSX@hostq", "\xffUSERX@\xfe", "USERX:PASSX@host q",
	"https://USERX:" + strings.Repeat("LONGPASSX", 400) + "@hostq:8787",
	"https://USERX:PASSX@" + strings.Repeat("longhostq.", 300) + "example",
}

// credTokens are the pieces of a hostile url a leak would show: split on the
// url punctuation and white space, kept when two bytes or longer.
func credTokens(raw string) []string {
	var out []string
	for _, tok := range strings.FieldsFunc(raw, func(r rune) bool {
		return strings.ContainsRune(":/?#@\\", r) || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	}) {
		if len(tok) >= 2 {
			out = append(out, tok)
		}
	}
	return out
}

// fixedURLTexts are the fixed sentences a url with an '@' may be replaced
// by; they are taken out of an output before it is searched.
var fixedURLTexts = []string{
	"(hidden: the url contains an @)",
	"the check failed; the url contains an @ so the error text is hidden",
	"the url contains an @ (read as a user:password); wt does not store credentials in config.toml",
}

// atURLSurfaces runs raw through every output a url reaches and returns each
// output by name: the plan's refusal, Apply's config-step refusal and its
// Recovery, Observe's server url, stored table url, problems, "no token
// entered" note and check message (a stored url, an entered url and
// WT_MODEL_URL, each with a ping error quoting it), and Plan.Format for
// plans built from a State that observed the stored url.
func atURLSurfaces(t *testing.T, raw string) map[string]string {
	t.Helper()
	out := map[string]string{}
	pingErr := &modelclient.UnreachableError{Addr: raw, Reason: "dial " + raw + "/control/v1/ping: refused (" + raw + ")"}

	if p, err := BuildPlan(freshState(), Desired{Mode: modelclient.ModeRemote, URL: raw, PastedToken: plPasted, SaveUnchecked: true}); err == nil {
		t.Errorf("%q planned:\n%s", raw, p.Format())
	} else {
		out["BuildPlan refusal"] = err.Error()
	}

	f := apEnv(t)
	r, lines := apply(t, f, Plan{Want: Desired{Mode: modelclient.ModeRemote, URL: raw, TokenFile: plRemoteTok},
		Steps: []Step{{Kind: StepConfig, Path: plCfgPath}}})
	if r.Failed == nil || f.called("Config.Update") {
		t.Errorf("%q: Apply wrote config.toml (Failed %+v)", raw, r.Failed)
	}
	if r.Err != nil {
		out["Apply error"] = r.Err.Error()
	}
	out["Apply recovery"] = r.Recovery
	out["Apply lines"] = strings.Join(lines, "\n")
	out["Apply notes"] = strings.Join(append(r.Warnings, r.Notes...), "\n")

	// A stored url, pinged as written.
	f = obsConfigured(t)
	f.configFile.Table.Mode, f.configFile.Table.URL = "remote", raw
	f.pingErr = pingErr
	st := Observe(context.Background(), f.Env(), ObserveOptions{})
	out["stored: Server.URL"] = st.Server.URL
	out["stored: Server.Message"] = st.Server.Message
	out["stored: Config.Table.URL"] = st.Config.Table.URL
	out["stored: Problems"] = strings.Join(st.Config.Problems, "\n")
	if st.Config.Table.URL != "" && !st.Config.URLHadCredentials {
		t.Errorf("%q: URLHadCredentials = false", raw)
	}
	for _, want := range []Desired{
		{Mode: modelclient.ModeRemote, URL: "benign.test:9", PastedToken: plPasted, SaveUnchecked: true},
		{Mode: modelclient.ModeNative},
		{Mode: modelclient.ModeLocal},
	} {
		if p, err := BuildPlan(st, want); err == nil {
			out["Format "+string(want.Mode)] = strings.ReplaceAll(p.Format(), "benign.test:9", "")
		}
	}
	// The url the form prefills (the State's) is refused, never written.
	if p, err := BuildPlan(st, Desired{Mode: modelclient.ModeRemote, URL: st.Config.Table.URL, TokenFile: obsTokenPath, SaveUnchecked: true}); err == nil {
		t.Errorf("%q: a plan of the observed url %q was not refused:\n%s", raw, st.Config.Table.URL, p.Format())
	} else {
		out["prefill refusal"] = err.Error()
	}

	// An entered url with no token: the note.
	f = obsConfigured(t)
	st = Observe(context.Background(), f.Env(), ObserveOptions{CheckURL: raw})
	out["no token: Server.Message"] = st.Server.Message

	// An entered url with a token, its check failing.
	f = obsConfigured(t)
	f.pingErr = pingErr
	st = Observe(context.Background(), f.Env(), ObserveOptions{CheckURL: raw, CheckToken: Secret("tok")})
	out["entered: Server.URL"] = st.Server.URL
	out["entered: Server.Message"] = st.Server.Message

	// WT_MODEL_URL, its check failing.
	f = obsConfigured(t)
	f.configFile.Table.Mode, f.configFile.Table.URL = "remote", ""
	f.env["WT_MODEL_URL"] = raw
	f.pingErr = pingErr
	st = Observe(context.Background(), f.Env(), ObserveOptions{})
	out["env: Server.URL"] = st.Server.URL
	out["env: Server.Message"] = st.Server.Message
	return out
}

// TestNoPieceOfAnAtURLReachesAnyOutput is the property behind the rule "no
// parsing, no partial echo": for every hostile url holding an ASCII '@', no
// piece of it (two bytes or longer, split on url punctuation and white
// space) appears in any output once the fixed placeholder and refusal
// sentences are taken out, however the url would parse.
func TestNoPieceOfAnAtURLReachesAnyOutput(t *testing.T) {
	if len(hostileAtURLs) < 60 {
		t.Fatalf("only %d hostile urls", len(hostileAtURLs))
	}
	for _, raw := range hostileAtURLs {
		if !strings.Contains(raw, "@") {
			t.Fatalf("hostile url %q has no '@'", raw)
		}
		for name, got := range atURLSurfaces(t, raw) {
			for _, fixed := range fixedURLTexts {
				got = strings.ReplaceAll(got, fixed, "")
			}
			for _, tok := range credTokens(raw) {
				if strings.Contains(got, tok) {
					t.Errorf("%s for %q shows %q:\n%s", name, raw, tok, got)
					break
				}
			}
		}
	}
}
