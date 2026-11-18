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

// plainURLs carry no user or password and must plan as before: a host:port
// typed without a scheme, an @ only in the query, a plain https url and an
// IPv6 host:port.
var plainURLs = []string{
	"gpu.example:8787",
	"http://gpu.example:8787/path?x=a@b",
	"gpu.example:8787/path?x=a@b",
	"https://gpu.example:8787",
	"[::1]:8787",
	"http://[::1]:8787",
}

// TestHasUserinfoReadsASchemelessURLAsAnAuthority pins the one shared rule:
// a url typed without a scheme ("user:pass@host:port") is read as an
// authority, so its user and password are found (url.Parse alone reads
// "user" as the scheme and the rest as opaque, with no user at all), while an
// @ in the query or an IPv6 host never counts.
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
	for _, raw := range plainURLs {
		if _, ok := hasUserinfo(raw); ok {
			t.Errorf("hasUserinfo(%q) = true for a url with no user", raw)
		}
	}
}

// TestBuildPlanRemoteRefusesASchemelessURLWithCredentials pins that every
// credentialed url, scheme or not, is refused at plan time by the one
// credentials reason, which names only the host and never the user or the
// password (config.toml is 0644, so wt never stores them), even when the
// check of it failed or it was not checked; and that a plain url still plans.
func TestBuildPlanRemoteRefusesASchemelessURLWithCredentials(t *testing.T) {
	for _, c := range credentialedURLs {
		for _, server := range []ServerState{
			{},
			{Checked: true, Kind: KindUnreachable, Message: "dial tcp: connection refused", URL: stripUserinfo(c.raw)},
		} {
			st := freshState()
			st.Server = server
			p, err := BuildPlan(st, Desired{Mode: modelclient.ModeRemote, URL: c.raw, PastedToken: plPasted, SaveUnchecked: true})
			if err == nil {
				t.Errorf("%q planned:\n%s", c.raw, p.Format())
				continue
			}
			msg := err.Error()
			if !strings.Contains(msg, "user:password") || (c.host != "" && !strings.Contains(msg, c.host)) {
				t.Errorf("%q: refusal %q is not the credentials reason naming %q", c.raw, msg, c.host)
			}
			if strings.Contains(msg, "USERX") || strings.Contains(msg, "PASSX") {
				t.Errorf("%q: refusal quotes the credentials: %q", c.raw, msg)
			}
		}
	}
	for _, raw := range plainURLs {
		mustPlan(t, freshState(), Desired{Mode: modelclient.ModeRemote, URL: raw, PastedToken: plPasted, SaveUnchecked: true})
	}
}

// TestStripUserinfoDropsASchemelessUser pins that the display form of a typed
// url (the check's "no token entered for" message, ServerState.URL, the
// table read from config.toml) drops the user and password of a url typed
// without a scheme too, and leaves a plain url as typed.
func TestStripUserinfoDropsASchemelessUser(t *testing.T) {
	for _, c := range credentialedURLs {
		if got := stripUserinfo(c.raw); strings.Contains(got, "USERX") || strings.Contains(got, "PASSX") {
			t.Errorf("stripUserinfo(%q) = %q, still carries the credentials", c.raw, got)
		}
	}
	for in, want := range map[string]string{
		"USERX:PASSX@gpu.example:8787": "gpu.example:8787",
		"gpu.example:8787":             "gpu.example:8787",
		"https://gpu.example:8787":     "https://gpu.example:8787",
		"[::1]:8787":                   "[::1]:8787",
	} {
		if got := stripUserinfo(in); got != want {
			t.Errorf("stripUserinfo(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestApplyConfigRefusesASchemelessURLWithCredentials pins the apply-time
// backstop on the same rule as the plan: a hand-built plan whose config step
// would write a credentialed url, scheme or not, fails that step, never calls
// Config.Update, and nothing it prints (the failure, the Recovery) quotes the
// user or the password.
func TestApplyConfigRefusesASchemelessURLWithCredentials(t *testing.T) {
	for _, c := range credentialedURLs {
		f := apEnv(t)
		p := Plan{Want: Desired{Mode: modelclient.ModeRemote, URL: c.raw, TokenFile: plRemoteTok},
			Steps: []Step{{Kind: StepConfig, Path: plCfgPath}}}
		r, lines := apply(t, f, p)
		if r.Failed == nil || r.Failed.Kind != StepConfig || f.called("Config.Update") {
			t.Errorf("%q: Failed = %+v, Config.Update called %t", c.raw, r.Failed, f.called("Config.Update"))
			continue
		}
		noSecrets(t, f, lines, r, "USERX")
		noSecrets(t, f, lines, r, "PASSX")
	}
}

// TestObserveCheckMessageDropsTheTypedCredentials pins that a failed check of
// a typed url with a user:password, scheme or not, keeps neither in
// ServerState (its URL or its Message): modelclient's errors quote the url
// they dialled, and the tab shows the Message on screen.
func TestObserveCheckMessageDropsTheTypedCredentials(t *testing.T) {
	for _, c := range credentialedURLs {
		f := obsConfigured(t)
		f.pingErr = &modelclient.UnreachableError{Addr: c.raw, Reason: "dial " + c.raw + "/control/v1/ping: refused"}
		st := Observe(context.Background(), f.Env(), ObserveOptions{CheckURL: c.raw, CheckToken: Secret("tok")})
		if !st.Server.Checked || st.Server.OK {
			t.Errorf("%q: Server = %+v, want a failed check", c.raw, st.Server)
		}
		if s := fmt.Sprintf("%+v", st.Server); strings.Contains(s, "USERX") || strings.Contains(s, "PASSX") {
			t.Errorf("%q: Server quotes the credentials: %s", c.raw, s)
		}
	}
}
