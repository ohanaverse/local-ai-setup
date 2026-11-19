package modelsetup

import (
	"context"
	neturl "net/url"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelmode"
)

// The url-rule property tests over the session half (plan, apply, observe)
// live in internal/modelmode's urlcreds_test.go; this file keeps the two
// surfaces only modelsetup builds: Apply's Recovery and the local plan.

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
	// The parsed host modelclient quotes is not a 3-byte run of these, so
	// only not pinging them keeps a piece of them off the screen.
	"http://pw/@h", "SeCrEt://u:p@x", "http://%C3%A9%C3%A9/@x",
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
	"not checked: the url contains an @; retype it without credentials",
}

// modelclientUnreachable is the error modelclient's Ping gives for raw when
// nothing answers: Addr is the parsed host of the trimmed url (the origin
// when it has none), as Client.addr builds it, and the reason is the
// transport's, which names the lowercased scheme it does not support or
// the host it could not dial. Neither is a 3-byte run of raw in general.
func modelclientUnreachable(raw string) error {
	origin := strings.TrimRight(raw, "/")
	addr := origin
	u, err := neturl.Parse(origin)
	if err == nil && u.Host != "" {
		addr = u.Host
	}
	reason := "dial tcp: lookup " + addr + ": no such host"
	if err == nil && u.Scheme != "" && u.Scheme != "http" && u.Scheme != "https" {
		reason = `unsupported protocol scheme "` + u.Scheme + `"`
	}
	return &modelclient.UnreachableError{Addr: addr, Reason: reason}
}

// atURLSurfaces runs raw through the outputs only modelsetup builds and
// returns each by name: Apply's Recovery after a config step whose url holds
// the '@', and Plan.Format of a local plan built from a State that observed
// the stored url (pinged as written, its failure quoting the parsed host).
// modelmode's copy covers every session-side surface.
func atURLSurfaces(t *testing.T, raw string) map[string]string {
	t.Helper()
	out := map[string]string{}

	f := apEnv(t)
	r, _ := apply(t, f, Plan{Plan: modelmode.Plan{Want: modelmode.Desired{Mode: modelclient.ModeRemote, URL: raw, TokenFile: plRemoteTok}, Steps: []Step{{Kind: StepConfig, Path: plCfgPath}}}})
	if r.Failed == nil || f.called("Config.Update") {
		t.Errorf("%q: Apply wrote config.toml (Failed %+v)", raw, r.Failed)
	}
	out["Apply recovery"] = r.Recovery

	f = obsConfigured(t)
	f.configFile.Table.Mode, f.configFile.Table.URL = "remote", raw
	f.pingErr = modelclientUnreachable(raw)
	st := Observe(context.Background(), f.Env(), ObserveOptions{})
	if p, err := BuildPlan(st, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}}); err == nil {
		out["Format local"] = p.Format()
	}
	return out
}

// TestNoPieceOfAnAtURLReachesAnyOutput is the property behind the rule "no
// parsing, no partial echo": for every hostile url holding an ASCII '@', no
// piece of it (two bytes or longer, split on url punctuation and white
// space; compared ignoring case and also percent-decoded, since a parser
// lowercases a scheme and decodes a host) appears in any output once the
// fixed placeholder and refusal sentences are taken out, however the url
// would parse. The fake ping fails the way modelclient does, quoting the
// parsed host, so a url that is pinged at all shows a piece here.
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
			lower := strings.ToLower(got)
		tokens:
			for _, tok := range credTokens(raw) {
				forms := []string{tok}
				if dec, err := neturl.PathUnescape(tok); err == nil && dec != tok && len(dec) >= 2 {
					forms = append(forms, dec)
				}
				for _, form := range forms {
					if strings.Contains(lower, strings.ToLower(form)) {
						t.Errorf("%s for %q shows %q:\n%s", name, raw, form, got)
						break tokens
					}
				}
			}
		}
	}
}
