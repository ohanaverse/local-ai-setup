package modelsetup

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
)

// credentialedURLs are typed urls that carry a user (and maybe a password),
// with and without a scheme. Each user part holds "USERX" and each password
// "PASSX", so a message that quotes either is caught; host is what the
// refusal may name ("" when the url does not parse far enough to know it).
var credentialedURLs = []struct{ raw, host string }{
	{"USERX:PASSX@gpu.example:8787", "gpu.example"},
	{"uUSERX:pPASSX@host", "host"},
	{"https://USERX:PASSX@gpu.example:8787", "gpu.example"},
	{"https://USERX@gpu.example:8787", "gpu.example"},
	{"USERX:PASSX:with-colon@gpu.example:8787", "gpu.example"},
	{"USERX:PASSX@with-at@gpu.example:8787", "gpu.example"},
	{"USERX:PASSX@gpu example:8787", ""}, // does not parse: the @ alone counts
	{"USERX:PASSX%40encoded@gpu.example:8787", "gpu.example"},
	{"http://USERX:PASSX@[::1]:8787", "::1"},
}

// bypassURLs are the typed urls that slipped past the parse-based reading of
// fb18fd78: a leading "//" or "///" (parsed with an empty host and the user in
// the path), a password holding '/', '?' or '#' with no scheme (the parse
// fails and the fallback cut at the first of those before seeing the '@'),
// and malformed scheme typos that parse with no user. Every one carries
// "USERX" and "PASSX" whole, so any output quoting either is caught.
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

// everyCredentialedURL is every typed url the rule must refuse and scrub.
func everyCredentialedURL() []string {
	var all []string
	for _, c := range credentialedURLs {
		all = append(all, c.raw)
	}
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

// leaks reports whether s quotes the test credentials or keeps an '@'.
func leaks(s string) bool {
	return strings.Contains(s, "USERX") || strings.Contains(s, "PASSX") || strings.Contains(s, "@")
}

// TestHasUserinfoReadsASchemelessURLAsAnAuthority pins the host the refusal
// names for the well-formed credentialed urls: a url typed without a scheme
// ("user:pass@host:port") is read as an authority, so the host after the
// user is the one named, never the user.
func TestHasUserinfoReadsASchemelessURLAsAnAuthority(t *testing.T) {
	for _, c := range credentialedURLs {
		host, ok := hasUserinfo(c.raw)
		if !ok {
			t.Errorf("hasUserinfo(%q) = false, want true", c.raw)
		}
		if host != c.host {
			t.Errorf("hasUserinfo(%q) host = %q, want %q", c.raw, host, c.host)
		}
	}
}

// TestHasUserinfoFailsClosedOnAnyAt pins the fail-closed rule: any typed url
// holding an ASCII '@' counts as credentialed, however it parses (the
// bypasses of fb18fd78 parsed with no user and were stored in the 0644
// config.toml), the host it names never quotes the user or the password, and
// a url with no '@' never counts.
func TestHasUserinfoFailsClosedOnAnyAt(t *testing.T) {
	for _, raw := range everyCredentialedURL() {
		host, ok := hasUserinfo(raw)
		if !ok {
			t.Errorf("hasUserinfo(%q) = false, want true", raw)
		}
		if leaks(host) {
			t.Errorf("hasUserinfo(%q) host = %q quotes the credentials", raw, host)
		}
	}
	for _, raw := range plainURLs {
		if _, ok := hasUserinfo(raw); ok {
			t.Errorf("hasUserinfo(%q) = true for a url with no '@'", raw)
		}
	}
}

// TestBuildPlanRemoteRefusesASchemelessURLWithCredentials pins that every
// url with an '@', scheme or not, well-formed or not, is refused at plan time
// by the one credentials reason, which never quotes the user or the password
// (config.toml is 0644, so wt never stores them), whether the url was not
// checked or its check failed; and that a plain url still plans.
func TestBuildPlanRemoteRefusesASchemelessURLWithCredentials(t *testing.T) {
	for _, raw := range everyCredentialedURL() {
		for _, server := range []ServerState{
			{},
			{Checked: true, Kind: KindUnreachable, Message: redactUserinfo("dial "+raw+": connection refused", raw), URL: stripUserinfo(raw)},
		} {
			st := freshState()
			st.Server = server
			p, err := BuildPlan(st, Desired{Mode: modelclient.ModeRemote, URL: raw, PastedToken: plPasted, SaveUnchecked: true})
			if err == nil {
				t.Errorf("%q planned:\n%s", raw, p.Format())
				continue
			}
			msg := err.Error()
			if !strings.Contains(msg, "user:password") || !strings.Contains(msg, "token file") {
				t.Errorf("%q: refusal %q is not the credentials reason", raw, msg)
			}
			if strings.Contains(msg, "USERX") || strings.Contains(msg, "PASSX") {
				t.Errorf("%q: refusal quotes the credentials: %q", raw, msg)
			}
		}
	}
	for _, c := range credentialedURLs {
		_, err := BuildPlan(freshState(), Desired{Mode: modelclient.ModeRemote, URL: c.raw, PastedToken: plPasted, SaveUnchecked: true})
		if err != nil && c.host != "" && !strings.Contains(err.Error(), c.host) {
			t.Errorf("%q: refusal %q does not name %q", c.raw, err, c.host)
		}
	}
	for _, raw := range plainURLs {
		mustPlan(t, freshState(), Desired{Mode: modelclient.ModeRemote, URL: raw, PastedToken: plPasted, SaveUnchecked: true})
	}
}

// TestStripUserinfoDropsASchemelessUser pins that the display form of a typed
// url (the check's "no token entered for" message, ServerState.URL, the
// table read from config.toml) drops everything up to the last '@', so no
// user, password or '@' survives whatever characters the password holds,
// and leaves a url with no '@' exactly as typed.
func TestStripUserinfoDropsASchemelessUser(t *testing.T) {
	for _, raw := range everyCredentialedURL() {
		if got := stripUserinfo(raw); leaks(got) {
			t.Errorf("stripUserinfo(%q) = %q, still carries the credentials", raw, got)
		}
	}
	for in, want := range map[string]string{
		"USERX:PASSX@gpu.example:8787":           "gpu.example:8787",
		"https://USERX:PASSX/x@gpu.example:8787": "https://gpu.example:8787",
		"//USERX:PASSX@gpu.example:8787":         "//gpu.example:8787",
	} {
		if got := stripUserinfo(in); got != want {
			t.Errorf("stripUserinfo(%q) = %q, want %q", in, got, want)
		}
	}
	for _, raw := range plainURLs {
		if got := stripUserinfo(raw); got != raw {
			t.Errorf("stripUserinfo(%q) = %q, want it unchanged", raw, got)
		}
	}
}

// TestRedactUserinfoScrubsEveryQuote pins that a failed check's message,
// which quotes the dialled address (sometimes more than once), keeps no
// user, password or '@' of the typed url, and that text about a url with no
// '@' comes back unchanged.
func TestRedactUserinfoScrubsEveryQuote(t *testing.T) {
	for _, raw := range everyCredentialedURL() {
		text := "dial " + raw + "/control/v1/ping: refused (" + raw + ")"
		if got := redactUserinfo(text, raw); leaks(got) {
			t.Errorf("redactUserinfo(.., %q) = %q, still carries the credentials", raw, got)
		}
	}
	for _, raw := range plainURLs {
		text := "dial " + raw + ": refused"
		if got := redactUserinfo(text, raw); got != text {
			t.Errorf("redactUserinfo(.., %q) = %q, want it unchanged", raw, got)
		}
	}
}

// TestApplyConfigRefusesASchemelessURLWithCredentials pins the apply-time
// backstop on the same rule as the plan: a hand-built plan whose config step
// would write a url with an '@', scheme or not, fails that step, never calls
// Config.Update, and nothing it prints (the failure, the Recovery) quotes the
// user or the password.
func TestApplyConfigRefusesASchemelessURLWithCredentials(t *testing.T) {
	for _, raw := range everyCredentialedURL() {
		f := apEnv(t)
		p := Plan{Want: Desired{Mode: modelclient.ModeRemote, URL: raw, TokenFile: plRemoteTok},
			Steps: []Step{{Kind: StepConfig, Path: plCfgPath}}}
		r, lines := apply(t, f, p)
		if r.Failed == nil || r.Failed.Kind != StepConfig || f.called("Config.Update") {
			t.Errorf("%q: Failed = %+v, Config.Update called %t", raw, r.Failed, f.called("Config.Update"))
			continue
		}
		noSecrets(t, f, lines, r, "USERX")
		noSecrets(t, f, lines, r, "PASSX")
	}
}

// TestObserveCheckMessageDropsTheTypedCredentials pins that a failed check of
// a typed url with an '@', scheme or not, keeps no user or password in
// ServerState (its URL or its Message): modelclient's errors quote the url
// they dialled, and the tab shows the Message on screen.
func TestObserveCheckMessageDropsTheTypedCredentials(t *testing.T) {
	for _, raw := range everyCredentialedURL() {
		f := obsConfigured(t)
		f.pingErr = &modelclient.UnreachableError{Addr: raw, Reason: "dial " + raw + "/control/v1/ping: refused"}
		st := Observe(context.Background(), f.Env(), ObserveOptions{CheckURL: raw, CheckToken: Secret("tok")})
		if !st.Server.Checked || st.Server.OK {
			t.Errorf("%q: Server = %+v, want a failed check", raw, st.Server)
		}
		if s := fmt.Sprintf("%+v", st.Server); strings.Contains(s, "USERX") || strings.Contains(s, "PASSX") {
			t.Errorf("%q: Server quotes the credentials: %s", raw, s)
		}
	}
}

// hostileURLs feed TestStripAndRedactNeverKeepWhatPrecedesTheLastAt: the
// bypasses plus odd shapes (bare '@', several '@', '://' after an '@',
// whitespace, backslashes, odd schemes, IPv6 brackets around the user). The
// part before the last '@' is chosen so it never reappears after it.
var hostileURLs = []string{
	"//uq:pq@h1", "///uq:pq@h1", "uq:p/q@h1:8787", "uq:p?q@h1", "uq:p#q@h1",
	"http:/uq:pq@h1", "http:///uq:pq@h1", "http//uq:pq@h1", "1http://uq:pq@h1",
	"http:/\\uq:pq@h1", "http:/ /uq:pq@h1", "@", "@@", "xq@", "@h1", "uq@",
	"uq:pq@h1@i2", "http://uq:pq@h1/xq@i2", "uq:pq@[::1]:8787", "uq:pq@h1:8787#fr@i2",
	"http://uq:pq%40w@h1", "UQ:PQ\t@h1", " uq:pq@h1", "uq:pq@h1 ", "uq%3Apq@h1",
	"\\\\uq:pq@h1", "http:\\\\uq:pq@h1", "file:///uq:pq@h1", "ftp://uq:pq@h1",
	"HTTP://UQ:PQ@H1", "http://[uq:pq@::1]:8787", "uq:pq@:8787", "uq:pq@/zz",
	"javascript://uq:pq@h1", "http://uq:pq@h1?next=http://vq:wq@i2",
	"http://aq/bq://cq@d1", "aq@bq://cq@d1", "uq:pq@@h1", "uq:p://q@h1",
	"http://uq:pq@", "//xq@h1",
	// Fullwidth '＠' (U+FF20) is not '@': no dialler reads it as the user
	// separator, so it is not refused and may stay allowed.
	"uq:pq\uff20h1",
	// Percent-encoded %40 is not '@': it is a literal in the host or path,
	// never the user separator, so it is not refused and may stay allowed.
	"uq:pq%40h1",
}

// TestStripAndRedactNeverKeepWhatPrecedesTheLastAt is the table-driven
// fuzz-ish check of the cut: for every hostile string with an ASCII '@',
// neither the display form (stripUserinfo) nor a message quoting it twice
// (redactUserinfo) keeps the text before its last '@' or any '@' at all.
func TestStripAndRedactNeverKeepWhatPrecedesTheLastAt(t *testing.T) {
	for _, raw := range hostileURLs {
		at := strings.LastIndex(raw, "@")
		if at < 0 {
			if _, ok := hasUserinfo(raw); ok {
				t.Errorf("hasUserinfo(%q) = true with no ASCII '@'", raw)
			}
			continue
		}
		if _, ok := hasUserinfo(raw); !ok {
			t.Errorf("hasUserinfo(%q) = false", raw)
		}
		pre := raw[:at]
		for name, got := range map[string]string{
			"stripUserinfo":  stripUserinfo(raw),
			"redactUserinfo": redactUserinfo(raw+" | "+raw, raw),
		} {
			if strings.Contains(got, "@") || (pre != "" && strings.Contains(got, pre)) {
				t.Errorf("%s(%q) = %q keeps %q or an '@'", name, raw, got, pre)
			}
		}
	}
}
