package modelclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"strings"
	"sync/atomic"
	"testing"
)

// hostileAtURLs are model-server urls that each hold an ASCII '@': the
// bypasses of every span-cutting design the Model server tab tried (copied
// from internal/modelsetup's list), with distinctive user and password tokens
// (USERX, PASSX, SECRETPW, s3cr, hunter2, ...) that no fixed text holds.
// "et" and "file" are spelled "etQ" and "smbq" because they occur in
// ordinary text.
var hostileAtURLs = []string{
	"u:SECRETPW@host:8787", "https://u:SECRETPW@host%zz", "https://u:SECRETPW@[::1",
	"//USERX:PASSX@hq", "///USERX:PASSX@hq", "USERX:p/PASSX@hq", "USERX:p?PASSX@hq",
	"USERX:p#PASSX@hq", "user9:p/ss9@h1", "user9:p?ss9@h1", "user9:p#ss9@h1",
	"http:/USERX:PASSX@hq", "http:///USERX:PASSX@hq",
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
	"https://USERX:" + "PASSX@" + strings.Repeat("longhostq.", 300) + "example",
	"http://pw/@h", "SeCrEt://u:p@x", "http://%C3%A9%C3%A9/@x",
}

// atURLsFor is hostileAtURLs plus urls holding an '@' that name the live
// address addr (host:port), so a request that is built at all reaches it.
func atURLsFor(addr string) []string {
	return append([]string{
		"http://USERX:PASSX@" + addr, "USERX:PASSX@" + addr, "http://USERX@" + addr + "/",
		"http://" + addr + "/?k=USERX@PASSX", "http://hunter2:@" + addr,
	}, hostileAtURLs...)
}

// plainEndpointURLs carry no '@' and must resolve as given.
var plainEndpointURLs = []string{
	"gpu.example:8787", "https://gpu.example:8787", "http://127.0.0.1:8787/", "[::1]:8787",
	"http://[::1]:8787", "https://gpu-1.example.com:8787/path",
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

// leakedPiece returns the first piece of raw (credTokens, compared ignoring
// case and also percent-decoded) that got shows once the fixed refusal and
// placeholder texts are taken out, or "" when none does.
func leakedPiece(raw, got string) string {
	for _, fixed := range []string{(&CredentialsInURLError{}).Error(), HiddenURL, unparsedAddr, unbuildableRequest} {
		got = strings.ReplaceAll(got, fixed, "")
	}
	lower := strings.ToLower(got)
	for _, tok := range credTokens(raw) {
		forms := []string{tok}
		if dec, err := neturl.PathUnescape(tok); err == nil && dec != tok && len(dec) >= 2 {
			forms = append(forms, dec)
		}
		for _, f := range forms {
			if strings.Contains(lower, strings.ToLower(f)) {
				return f
			}
		}
	}
	return ""
}

// countingServer is an httptest server that counts every request it gets.
func countingServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n.Add(1)
		w.WriteHeader(http.StatusTeapot)
	}))
	t.Cleanup(s.Close)
	return s, &n
}

// TestHasUserinfoIsAnyASCIIAt pins the predicate the session side and the
// setup screens (Mode and Setup tabs) share: any ASCII '@' counts, with no parsing (every
// parse-based reading was bypassed); a url with none never does, and the
// fullwidth U+FF20 and %40 are not '@'.
func TestHasUserinfoIsAnyASCIIAt(t *testing.T) {
	for _, raw := range hostileAtURLs {
		if !HasUserinfo(raw) {
			t.Errorf("HasUserinfo(%q) = false", raw)
		}
	}
	for _, raw := range append(plainEndpointURLs, "uq:pq\uff20h1", "uq:pq%40h1", "") {
		if HasUserinfo(raw) {
			t.Errorf("HasUserinfo(%q) = true for a url with no ASCII '@'", raw)
		}
	}
}

// TestResolveEndpointRefusesAnAtURLFromEverySource is the issue #639
// property at the resolver: for every url holding an '@' (at least 40), from
// WT_MODEL_URL, from url under [model-server], or from config overridden by
// the environment, ResolveEndpoint refuses with the one fixed
// *CredentialsInURLError (never a "no server" case), whatever the token
// source, and no piece of the url is in the text. Before the fix a
// schemeless or malformed url went on to a request whose error quoted it.
func TestResolveEndpointRefusesAnAtURLFromEverySource(t *testing.T) {
	if len(hostileAtURLs) < 40 {
		t.Fatalf("only %d hostile urls", len(hostileAtURLs))
	}
	tf := tokenFileWith(t, "tok", "filetok\n", 0o600)
	for _, raw := range atURLsFor("127.0.0.1:9") {
		if !strings.Contains(raw, "@") {
			t.Fatalf("hostile url %q has no '@'", raw)
		}
		for name, call := range map[string]func() (Endpoint, error){
			"env url, env token": func() (Endpoint, error) {
				return ResolveEndpoint("", "", envOf(map[string]string{"WT_MODEL_URL": raw, "WT_MODEL_TOKEN": "t"}))
			},
			"env url, config file": func() (Endpoint, error) {
				return ResolveEndpoint("", tf, envOf(map[string]string{"WT_MODEL_URL": raw}))
			},
			"config url, token file": func() (Endpoint, error) { return ResolveEndpoint(raw, tf, envOf(nil)) },
			"config url, env file": func() (Endpoint, error) {
				return ResolveEndpoint(raw, "", envOf(map[string]string{"WT_MODEL_TOKEN_FILE": tf}))
			},
			"config url, no token": func() (Endpoint, error) { return ResolveEndpoint(raw, "", envOf(nil)) },
			"env over a plain config": func() (Endpoint, error) {
				return ResolveEndpoint("http://cfg:1", tf, envOf(map[string]string{"WT_MODEL_URL": raw}))
			},
		} {
			ep, err := call()
			var ce *CredentialsInURLError
			if !errors.As(err, &ce) {
				t.Errorf("%s: %q resolved to %+v, %v; want *CredentialsInURLError", name, raw, ep, err)
				continue
			}
			if NoServer(err) || ep != (Endpoint{}) {
				t.Errorf("%s: %q: NoServer %v, endpoint %+v; want a hard refusal and no endpoint", name, raw, NoServer(err), ep)
			}
			if p := leakedPiece(raw, err.Error()); p != "" {
				t.Errorf("%s: %q: the refusal shows %q: %s", name, raw, p, err)
			}
		}
	}
}

// TestResolveEndpointKeepsPlainURLs pins that the refusal touches only urls
// holding an '@': a plain url, schemeless or not, IPv6 included, resolves
// exactly as given.
func TestResolveEndpointKeepsPlainURLs(t *testing.T) {
	for _, raw := range plainEndpointURLs {
		ep, err := ResolveEndpoint(raw, "", envOf(map[string]string{"WT_MODEL_TOKEN": "t"}))
		if err != nil || ep.URL != raw || ep.Token != "t" {
			t.Errorf("config %q = %+v, %v; want it unchanged", raw, ep, err)
		}
		ep, err = ResolveEndpoint("", "", envOf(map[string]string{"WT_MODEL_URL": raw, "WT_MODEL_TOKEN": "t"}))
		if err != nil || ep.URL != raw {
			t.Errorf("env %q = %+v, %v; want it unchanged", raw, ep, err)
		}
	}
}

// TestClientNeverSendsToAnAtURL is the defence in depth behind the resolver:
// a Client built directly on an '@' url (bypassing ResolveEndpoint, as
// modelsetup and smoke build theirs) sends nothing, not even to a live
// server the url names, and every call's error is the fixed refusal with no
// piece of the url. Before the fix a schemeless url's ping failed with the
// raw url in the address and the --listen hint.
func TestClientNeverSendsToAnAtURL(t *testing.T) {
	s, n := countingServer(t)
	addr := strings.TrimPrefix(s.URL, "http://")
	for _, raw := range atURLsFor(addr) {
		c := New(Endpoint{URL: raw, Token: "t"})
		_, perr := c.Ping(ctx())
		_, cerr := c.Catalog(ctx())
		_, gerr := c.GatewayStatus(ctx())
		for name, err := range map[string]error{"Ping": perr, "Catalog": cerr, "GatewayStatus": gerr} {
			var ce *CredentialsInURLError
			if !errors.As(err, &ce) {
				t.Errorf("%s(%q) = %v, want *CredentialsInURLError", name, raw, err)
				continue
			}
			if p := leakedPiece(raw, err.Error()); p != "" {
				t.Errorf("%s(%q) shows %q: %s", name, raw, p, err)
			}
		}
		if got := c.addr(); got != HiddenURL {
			t.Errorf("addr(%q) = %q, want %q", raw, got, HiddenURL)
		}
	}
	if got := n.Load(); got != 0 {
		t.Errorf("%d requests reached the server named by an '@' url, want 0", got)
	}
}

// TestUnbuildableRequestAndHostlessAddrQuoteNothing pins the other two
// defences: a url net/http cannot build a request from fails with a fixed
// text that quotes none of it (not url.Parse's message, which quotes the
// whole url), and addr() of a url with no parsable host is a fixed
// placeholder rather than the raw origin, so no error or --listen hint
// echoes it. A url with a host still names host:port.
func TestUnbuildableRequestAndHostlessAddrQuoteNothing(t *testing.T) {
	for _, raw := range []string{"http://hostzz9%zz", "http://[::1", "http://hostzz9\x7f:1"} {
		_, err := New(Endpoint{URL: raw, Token: "t"}).Ping(ctx())
		if err == nil || !strings.Contains(err.Error(), unbuildableRequest) || strings.Contains(err.Error(), "hostzz9") || strings.Contains(err.Error(), "[::1") {
			t.Errorf("Ping(%q) = %v, want the fixed %q", raw, err, unbuildableRequest)
		}
	}
	for _, raw := range []string{"secretzz9:8787", "secretzz9", "[::1]:8787", "/just/a/path"} {
		c := New(Endpoint{URL: raw, Token: "t"})
		if got := c.addr(); got != unparsedAddr {
			t.Errorf("addr(%q) = %q, want %q", raw, got, unparsedAddr)
		}
		_, err := c.Ping(context.Background())
		if err == nil || strings.Contains(err.Error(), "secretzz9") || strings.Contains(err.Error(), "just/a") {
			t.Errorf("Ping(%q) = %v, want no piece of the url", raw, err)
		}
	}
	if got := New(Endpoint{URL: "http://127.0.0.1:8787/x", Token: "t"}).addr(); got != "127.0.0.1:8787" {
		t.Errorf("addr of a url with a host = %q, want 127.0.0.1:8787", got)
	}
}
