package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelserver"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tlstest"
)

// appendHome appends body to the scratch wt-model home's config.toml
// (seedProxyHome's file).
func appendHome(t *testing.T, body string) {
	t.Helper()
	p := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "wt-model", "config.toml")
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString("\n" + body); err != nil {
		t.Fatal(err)
	}
}

// tlsGateway is the [gateway] table naming a certificate and key.
func tlsGateway(cert, key string) string {
	return "[gateway]\ntls_cert = \"" + cert + "\"\ntls_key = \"" + key + "\"\n"
}

// noPEMBody fails when text holds any base64 body line of the PEM files.
func noPEMBody(t *testing.T, text string, files ...string) {
	t.Helper()
	for _, line := range tlstest.BodyLines(t, files...) {
		if strings.Contains(text, line) {
			t.Errorf("output holds a PEM body line of %v:\n%s", files, text)
			return
		}
	}
}

// TestServeRefusesABadTLSConfig pins decision 13's fail-closed start: a
// missing file, a loose key, a half-set pair, a wrong-typed value, a key that
// does not match the certificate and an unparseable certificate each refuse
// with one line naming the path or the key, before listening (no announce
// line) and before serve.json, and no output holds key or certificate bytes.
func TestServeRefusesABadTLSConfig(t *testing.T) {
	asWTModel(t)
	dir := t.TempDir()
	cert, key := tlstest.Gen(t, dir, tlstest.Opts{})
	cert2, _ := tlstest.Gen(t, dir, tlstest.Opts{})
	_, looseKey := tlstest.Gen(t, dir, tlstest.Opts{KeyMode: 0o644})
	junk := filepath.Join(dir, "junk.crt")
	if err := os.WriteFile(junk, []byte("not a certificate\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "missing.crt")
	for _, c := range []struct {
		name, gateway string
		want          []string
	}{
		{"missing file", tlsGateway(missing, key), []string{"[gateway] tls_cert/tls_key: ", missing}},
		{"loose key", tlsGateway(cert, looseKey), []string{"[gateway] tls_cert/tls_key: ", looseKey, "chmod 600"}},
		{"half pair", "[gateway]\ntls_cert = \"" + cert + "\"\n", []string{"[gateway] tls_cert and tls_key must be set together"}},
		{"wrong type", "[gateway]\ntls_cert = 1\ntls_key = \"" + key + "\"\n", []string{"[gateway] tls_cert must be a string"}},
		{"mismatch", tlsGateway(cert2, key), []string{"[gateway] tls_cert/tls_key: ", key}},
		{"unparseable", tlsGateway(junk, key), []string{"[gateway] tls_cert/tls_key: ", junk}},
	} {
		t.Run(c.name, func(t *testing.T) {
			seedProxyHome(t, "http://127.0.0.1:4000", "sk-test", 0o600)
			appendHome(t, c.gateway)
			rt := testRuntime(t)
			cmd := rootCmd(fakeDeps, rt)
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs([]string{"serve", "--listen", "127.0.0.1:0", "--token-file", writeToken(t, 0o600)})
			err := cmd.ExecuteContext(t.Context())
			if err == nil {
				t.Fatalf("serve started; output %q", out.String())
			}
			msg := err.Error()
			if strings.Contains(msg, "\n") {
				t.Errorf("refusal is more than one line: %q", msg)
			}
			for _, w := range c.want {
				if !strings.Contains(msg, w) {
					t.Errorf("refusal %q does not contain %q", msg, w)
				}
			}
			if strings.HasPrefix(msg, "config error") {
				t.Errorf("refusal %q is wrapped; want the one [gateway] line", msg)
			}
			if strings.Contains(out.String(), "listening on") {
				t.Errorf("serve announced before refusing:\n%s", out.String())
			}
			if _, serr := os.Stat(rt.Record.Path); !errors.Is(serr, os.ErrNotExist) {
				t.Errorf("serve.json exists after a refusal (%v)", serr)
			}
			noPEMBody(t, msg+"\n"+out.String(), cert, key, cert2, looseKey)
		})
	}
}

// TestServeExpiredCertServesPlainHTTP pins owner confirmation 3: a
// configured certificate that is expired or not yet valid starts serve on
// plain http (never a refusal: a KeepAlive LaunchAgent would loop), the
// announce line stays byte-identical and first, exactly one warning names the
// date (no "TLS is on" line, no PEM bytes), serve.json records no tls_cert,
// and the ping says tls_off with the date reason. Through the same options
// serve builds, a header-mode request from a non-loopback peer is refused
// with 0 upstream calls while a token-swap request is served.
func TestServeExpiredCertServesPlainHTTP(t *testing.T) {
	asWTModel(t)
	now := time.Now()
	for _, c := range []struct {
		name       string
		opts       tlstest.Opts
		warn, date string
		reason     string
	}{
		{"expired", tlstest.Opts{NotBefore: now.AddDate(0, -2, 0), NotAfter: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)},
			"expired on 2026-11-01", "2026-11-01", "certificate expired 2026-11-01"},
		{"not yet valid", tlstest.Opts{NotBefore: now.AddDate(0, 0, 10).Truncate(24 * time.Hour), NotAfter: now.AddDate(1, 0, 0)},
			"is not valid until", now.AddDate(0, 0, 10).Truncate(24 * time.Hour).UTC().Format("2006-01-02"), "certificate not valid until "},
	} {
		t.Run(c.name, func(t *testing.T) {
			var upCalls atomic.Int32
			var upAuth atomic.Value
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				upCalls.Add(1)
				upAuth.Store(r.Header.Get("Authorization"))
				_, _ = io.WriteString(w, `{"data":[]}`)
			}))
			defer up.Close()
			cert, key := tlstest.Gen(t, t.TempDir(), c.opts)
			seedProxyHome(t, up.URL, "sk-test", 0o600)
			appendHome(t, "[gateway]\nsubscription_passthrough = true\ntls_cert = \""+cert+"\"\ntls_key = \""+key+"\"\n")
			tok := filepath.Join(t.TempDir(), "token")
			if err := os.WriteFile(tok, []byte("tok-abc\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			rt := testRuntime(t)
			read, readOut, stop := serveUntilListeningWith(t, rt, tok)
			addr := listeningAddr(t, read())
			record, err := os.ReadFile(rt.Record.Path)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(record, []byte("tls_cert")) {
				t.Errorf("serve.json records tls_cert while TLS is off:\n%s", record)
			}
			req, _ := http.NewRequest("GET", "http://"+addr+"/control/v1/ping", nil)
			req.Header.Set("Authorization", "Bearer tok-abc")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("plain-http ping: %v", err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			var p modelapi.Ping
			if err := json.Unmarshal(body, &p); err != nil || resp.StatusCode != http.StatusOK || !strings.HasPrefix(p.TLSOff, c.reason) || !strings.Contains(p.TLSOff, c.date) {
				t.Errorf("ping = %d %s, want 200 with tls_off %q…%s", resp.StatusCode, body, c.reason, c.date)
			}
			if err := stop(); err != nil {
				t.Fatalf("serve returned %v", err)
			}
			stderr := read()
			lines := strings.Split(strings.TrimRight(stderr, "\n"), "\n")
			if lines[0] != strings.TrimSuffix(announceLine(addr), "\n") {
				t.Errorf("first line %q, want the announce line", lines[0])
			}
			var warns []string
			for _, l := range lines {
				if strings.HasPrefix(l, "wt-model: warning:") {
					warns = append(warns, l)
				}
				if strings.Contains(l, "TLS is on") {
					t.Errorf("a TLS-on line while TLS is off: %q", l)
				}
			}
			if len(warns) != 1 || !strings.Contains(warns[0], c.warn) || !strings.Contains(warns[0], c.date) || !strings.Contains(warns[0], cert) || !strings.Contains(warns[0], "serving plain http") {
				t.Errorf("warnings = %q, want exactly one naming %s %s and plain http", warns, c.warn, c.date)
			}
			noPEMBody(t, stderr+readOut()+string(record), cert, key)

			// The same options serve builds, through Handler (no
			// non-loopback listener).
			gw, _, err := config.GatewaySettings()
			if err != nil {
				t.Fatal(err)
			}
			st, err := loadServeTLS(gw, time.Now())
			if err != nil || st.Config != nil || st.Off == "" {
				t.Fatalf("loadServeTLS = %+v, %v; want plain http with the reason", st, err)
			}
			u, _ := url.Parse(up.URL)
			opts := serverOptions("127.0.0.1:0", "tok-abc", time.Hour, u, "sk-test", gw, st, log.New(io.Discard, "", 0))
			srv, err := modelserver.NewServer(opts, fakeDeps(nil, nil, u, "sk-test"))
			if err != nil {
				t.Fatal(err)
			}
			before := upCalls.Load()
			hreq := httptest.NewRequest("GET", "/v1/models", nil)
			hreq.RemoteAddr = "192.0.2.7:1"
			hreq.Header.Set("x-wt-model-token", "tok-abc")
			hreq.Header.Set("Authorization", "Bearer sk-ant-oat01-wtSENTINEL")
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, hreq)
			var ge modelapi.GatewayError
			_ = json.Unmarshal(rec.Body.Bytes(), &ge)
			if rec.Code != http.StatusForbidden || ge.Error.Code != modelapi.CodeSubscriptionInsecure || upCalls.Load() != before {
				t.Errorf("header mode from 192.0.2.7 = %d %s, upstream calls %d; want 403 %s and none",
					rec.Code, rec.Body.Bytes(), upCalls.Load()-before, modelapi.CodeSubscriptionInsecure)
			}
			sreq := httptest.NewRequest("GET", "/v1/models", nil)
			sreq.RemoteAddr = "192.0.2.7:1"
			sreq.Header.Set("Authorization", "Bearer tok-abc")
			rec = httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, sreq)
			if rec.Code != http.StatusOK || upAuth.Load() != "Bearer sk-test" {
				t.Errorf("token swap = %d %s, upstream saw %v; want 200 with the host key", rec.Code, rec.Body.Bytes(), upAuth.Load())
			}
		})
	}
}

// TestServeTLSAnnounceUnchanged pins the report with TLS on: the announce
// line is byte-identical and first (the bound address, no scheme), the TLS
// line follows, then the subscription line with no remote warning, a client
// trusting the certificate pings over https, and serve.json records the
// certificate's path and never the key's.
func TestServeTLSAnnounceUnchanged(t *testing.T) {
	asWTModel(t)
	cert, key := tlstest.Gen(t, t.TempDir(), tlstest.Opts{NotAfter: time.Now().AddDate(0, 3, 0)})
	seedProxyHome(t, "http://127.0.0.1:4000", "sk-test", 0o600)
	appendHome(t, "[gateway]\nsubscription_passthrough = true\ntls_cert = \""+cert+"\"\ntls_key = \""+key+"\"\n")
	tok := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tok, []byte("tok-abc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rt := testRuntime(t)
	read, _, stop := serveUntilListeningWith(t, rt, tok)
	addr := listeningAddr(t, read())
	record, err := os.ReadFile(rt.Record.Path)
	if err != nil {
		t.Fatal(err)
	}
	var r struct {
		TLSCert string `json:"tls_cert"`
	}
	_ = json.Unmarshal(record, &r)
	if r.TLSCert != cert || bytes.Contains(record, []byte(key)) {
		t.Errorf("serve.json = %s, want tls_cert %s and no key path", record, cert)
	}
	tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: tlstest.Pool(t, cert)}, ForceAttemptHTTP2: true}
	defer tr.CloseIdleConnections()
	req, _ := http.NewRequest("GET", "https://"+addr+"/control/v1/ping", nil)
	req.Header.Set("Authorization", "Bearer tok-abc")
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		t.Fatalf("https ping: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("https ping = %d", resp.StatusCode)
	}
	if err := stop(); err != nil {
		t.Fatalf("serve returned %v", err)
	}
	lines := strings.Split(strings.TrimRight(read(), "\n"), "\n")
	want := []string{
		strings.TrimSuffix(announceLine(addr), "\n"),
		"wt-model: TLS is on (tls_cert " + cert + ", TLS 1.2+; clients that do not trust it need ca_file or a CA the system trusts)",
		subInfo,
	}
	if !slices.Equal(lines, want) {
		t.Errorf("lines = %q\nwant  %q", lines, want)
	}
}

// TestTLSLines pins serve's TLS report as one pure function: nothing without
// a certificate; the one TLS-on line; plus a warning when the certificate
// expires within 14 days; plus a warning when it names neither 127.0.0.1 nor
// localhost (local sessions dial loopback); and for an expired or
// not-yet-valid certificate exactly the one plain-http warning, no TLS-on
// line.
func TestTLSLines(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	const path = "/etc/wt/server.crt"
	on := "wt-model: TLS is on (tls_cert " + path + ", TLS 1.2+; clients that do not trust it need ca_file or a CA the system trusts)"
	loop := []net.IP{net.ParseIP("127.0.0.1")}
	leaf := func(nb, na time.Time, dns []string, ips []net.IP) *x509.Certificate {
		return &x509.Certificate{NotBefore: nb, NotAfter: na, DNSNames: dns, IPAddresses: ips}
	}
	for _, c := range []struct {
		name string
		path string
		leaf *x509.Certificate
		want []string
	}{
		{"plain", "", nil, nil},
		{"on", path, leaf(now.AddDate(0, -1, 0), now.AddDate(0, 3, 0), nil, loop), []string{on}},
		{"on via localhost", path, leaf(now.AddDate(0, -1, 0), now.AddDate(0, 3, 0), []string{"localhost"}, nil), []string{on}},
		{"expiring", path, leaf(now.AddDate(0, -1, 0), now.AddDate(0, 0, 10), nil, loop), []string{on,
			"wt-model: warning: the certificate " + path + " expires on 2026-12-02 (in 10 days): renew it (replace both files, then restart serve)"}},
		{"expiring within a day", path, leaf(now.AddDate(0, -1, 0), now.Add(5*time.Hour), nil, loop), []string{on,
			"wt-model: warning: the certificate " + path + " expires on 2026-11-22 (within a day): renew it (replace both files, then restart serve)"}},
		{"no loopback name", path, leaf(now.AddDate(0, -1, 0), now.AddDate(0, 3, 0), []string{"gpu.example"}, []net.IP{net.ParseIP("192.0.2.7")}), []string{on,
			"wt-model: warning: the certificate " + path + " names neither 127.0.0.1 nor localhost; local sessions dial loopback and will refuse it"}},
		{"expired", path, leaf(now.AddDate(0, -3, 0), time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), []string{"gpu.example"}, nil), []string{
			"wt-model: warning: TLS is off: the certificate " + path + " expired on 2026-11-01; serving plain http. Remote https clients fail and the subscription header mode is refused from non-loopback peers until it is renewed and serve restarted"}},
		{"not yet valid", path, leaf(time.Date(2026, 12, 5, 0, 0, 0, 0, time.UTC), now.AddDate(1, 0, 0), nil, loop), []string{
			"wt-model: warning: TLS is off: the certificate " + path + " is not valid until 2026-12-05; serving plain http. Remote https clients fail and the subscription header mode is refused from non-loopback peers until it is renewed and serve restarted"}},
	} {
		if got := tlsLines(c.path, c.leaf, now); !slices.Equal(got, c.want) {
			t.Errorf("%s: tlsLines =\n %q\nwant\n %q", c.name, got, c.want)
		}
	}
}

// TestPlainServeRecordUnchanged pins Review Focus 2 at the process level:
// with no TLS keys serve's output and serve.json are today's (no tls_cert
// key, no TLS line), and the plain-http ping carries no tls_off.
func TestPlainServeRecordUnchanged(t *testing.T) {
	asWTModel(t)
	seedProxyHome(t, "http://127.0.0.1:4000", "sk-test", 0o600)
	rt := testRuntime(t)
	read, _, stop := serveUntilListeningWith(t, rt, writeToken(t, 0o600))
	addr := listeningAddr(t, read())
	record, err := os.ReadFile(rt.Record.Path)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("GET", "http://"+addr+"/control/v1/ping", nil)
	req.Header.Set("Authorization", "Bearer tok-abc")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err := stop(); err != nil {
		t.Fatalf("serve returned %v", err)
	}
	if bytes.Contains(record, []byte("tls_cert")) {
		t.Errorf("serve.json has tls_cert:\n%s", record)
	}
	if bytes.Contains(body, []byte("tls_off")) {
		t.Errorf("ping has tls_off: %s", body)
	}
	if out := read(); strings.Contains(out, "TLS") {
		t.Errorf("plain serve printed a TLS line:\n%s", out)
	}
}
