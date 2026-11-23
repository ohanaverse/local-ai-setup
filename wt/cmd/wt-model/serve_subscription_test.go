package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// subInfo and subWarn are subscriptionLines' two fixed lines.
const (
	subInfo = "wt-model: subscription passthrough is on (x-wt-model-token; refused from non-loopback peers over plain http; owner's own login only)"
	subWarn = "wt-model: warning: remote subscription requests need TLS (set tls_cert and tls_key under [gateway] in ~/.config/wt-model/config.toml) or a tunnel arriving from loopback; other remote traffic is unaffected"
)

// TestSubscriptionLines pins serve's report of the mode (decision 2) as one
// pure function: off prints nothing; on prints the info line, plus one
// warning when the listen address is not loopback and TLS is off; TLS on
// silences the warning; TLS off on the certificate's dates ends the info
// line with the reason and prints no second warning.
func TestSubscriptionLines(t *testing.T) {
	const reason = "certificate expired 2026-11-01"
	tlsOffInfo := "wt-model: subscription passthrough is on (x-wt-model-token; refused from non-loopback peers over plain http; owner's own login only; TLS is off: the certificate expired 2026-11-01, so header mode is refused from every non-loopback peer)"
	type tc struct {
		addr   string
		on     bool
		tlsOn  bool
		tlsOff string
		want   []string
	}
	var cases []tc
	for _, addr := range []string{"0.0.0.0:8787", "[::]:8787", "192.0.2.7:8787"} {
		cases = append(cases,
			tc{addr, true, false, "", []string{subInfo, subWarn}},
			tc{addr, true, true, "", []string{subInfo}},
			tc{addr, true, false, reason, []string{tlsOffInfo}},
		)
	}
	cases = append(cases,
		tc{"127.0.0.1:0", true, false, "", []string{subInfo}},
		tc{"[::1]:8787", true, false, "", []string{subInfo}},
		tc{"localhost:8787", true, false, "", []string{subInfo}},
		tc{"0.0.0.0:8787", false, false, "", nil},
		tc{"127.0.0.1:0", false, false, "", nil},
	)
	for _, c := range cases {
		got := subscriptionLines(c.addr, c.on, c.tlsOn, c.tlsOff)
		if !slices.Equal(got, c.want) {
			t.Errorf("subscriptionLines(%q, on=%t, tlsOn=%t, tlsOff=%q) =\n %q\nwant\n %q", c.addr, c.on, c.tlsOn, c.tlsOff, got, c.want)
		}
		for _, l := range got {
			if strings.Contains(l, "note:") {
				t.Errorf("line %q is a note; the info line is bare (M4)", l)
			}
		}
	}
}

// enablePassthrough appends the on switch to the scratch wt-model home's
// config.toml (seedProxyHome's file, or a new one).
func enablePassthrough(t *testing.T) {
	t.Helper()
	p := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "wt-model", "config.toml")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString("\n[gateway]\nsubscription_passthrough = true\n"); err != nil {
		t.Fatal(err)
	}
}

// TestServeSubscriptionOffByDefault pins that a home with no [gateway]
// table serves with the mode off and prints no subscription line at all.
func TestServeSubscriptionOffByDefault(t *testing.T) {
	asWTModel(t)
	seedProxyHome(t, "http://127.0.0.1:4000", "sk-test", 0o600)
	read, readOut, stop := serveUntilListening(t)
	if err := stop(); err != nil {
		t.Fatalf("serve returned %v", err)
	}
	if out := read() + readOut(); strings.Contains(out, "subscription") {
		t.Errorf("serve with the mode off printed a subscription line:\n%s", out)
	}
}

// TestServeRefusesAnUnparseableGatewayHome pins that a TOML syntax error in
// the home config.toml fails the start (never "off"), and the refusal quotes
// no value from the file.
func TestServeRefusesAnUnparseableGatewayHome(t *testing.T) {
	asWTModel(t)
	seedProxyHome(t, "http://127.0.0.1:4000", "sk-test", 0o600)
	p := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "wt-model", "config.toml")
	if err := os.WriteFile(p, []byte("[litellm]\nurl = \"http://127.0.0.1:4000\"\nkey_file = \"litellm.key\"\n[gateway]\nsubscription_passthrough = SECRETVAL\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, "serve", "--listen", "127.0.0.1:0", "--token-file", writeToken(t, 0o600))
	if err == nil {
		t.Fatalf("serve started on an unparseable home; output %q", out)
	}
	if strings.Contains(err.Error()+out, "SECRETVAL") {
		t.Errorf("the refusal quotes a value: %v / %q", err, out)
	}
}

// TestServeSubscriptionLineAfterAnnounce pins the report's place: the
// announce line stays byte-identical and first, and the bare subscription
// line follows it (loopback listen, so no warning). With a legacy-fallback
// pair, the one fallback note still comes before the announce line, once.
func TestServeSubscriptionLineAfterAnnounce(t *testing.T) {
	asWTModel(t)
	check := func(t *testing.T, wantNote bool) {
		read, _, stop := serveUntilListening(t)
		if err := stop(); err != nil {
			t.Fatalf("serve returned %v", err)
		}
		all := read()
		addr := listeningAddr(t, all)
		lines := strings.Split(strings.TrimRight(all, "\n"), "\n")
		if wantNote {
			if len(lines) == 0 || !strings.HasPrefix(lines[0], "wt-model: note: ") {
				t.Fatalf("lines = %q, want the fallback note first", lines)
			}
			for _, l := range lines[1:] {
				if strings.HasPrefix(l, "wt-model: note: ") {
					t.Errorf("a second note: %q", l)
				}
			}
			lines = lines[1:]
		}
		want := []string{strings.TrimSuffix(announceLine(addr), "\n"), subInfo}
		if !slices.Equal(lines, want) {
			t.Errorf("lines = %q\nwant  %q", lines, want)
		}
	}
	t.Run("home pair", func(t *testing.T) {
		seedProxyHome(t, "http://127.0.0.1:4000", "sk-test", 0o600)
		enablePassthrough(t)
		check(t, false)
	})
	t.Run("legacy pair", func(t *testing.T) {
		seedConfig(t, true)
		enablePassthrough(t)
		check(t, true)
	})
}

// TestServeNeverPrintsForwardedSecrets pins Review Focus 3 at the process
// level: header-mode requests carrying the wt token and a Claude login go
// through the running serve's gateway (a success, a 403 and an upstream
// that aborts mid-body, which makes the proxy write an error line to serve's
// stderr), and neither secret appears on stderr, stdout or in serve.json.
func TestServeNeverPrintsForwardedSecrets(t *testing.T) {
	asWTModel(t)
	const (
		login = "sk-ant-oat01-wtSENTINEL"
		tok   = "wt-SENTINEL-tok"
	)
	var mu sync.Mutex
	var upAuth []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		upAuth = append(upAuth, r.Header.Get("Authorization"))
		mu.Unlock()
		if r.URL.Path == "/v1/messages" {
			w.Header().Set("Content-Length", "1000")
			_, _ = io.WriteString(w, `{"partial":`)
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler)
		}
		_, _ = io.WriteString(w, `{"data":[]}`)
	}))
	defer up.Close()
	seedProxyHome(t, up.URL, "sk-test", 0o600)
	enablePassthrough(t)
	tokPath := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokPath, []byte(tok+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rt := testRuntime(t)
	read, readOut, stop := serveUntilListeningWith(t, rt, tokPath)
	addr := listeningAddr(t, read())

	send := func(method, path string, extra map[string]string) (int, error) {
		req, _ := http.NewRequestWithContext(context.Background(), method, "http://"+addr+path, strings.NewReader(`{"model":"m"}`))
		req.Header.Set("x-wt-model-token", tok)
		req.Header.Set("Authorization", "Bearer "+login)
		for k, v := range extra {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0, err
		}
		_, rerr := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp.StatusCode, rerr
	}
	if st, err := send("GET", "/v1/models", nil); st != http.StatusOK || err != nil {
		t.Errorf("success case = %d %v", st, err)
	}
	if st, _ := send("GET", "/v1/models", map[string]string{"X-Forwarded-Proto": "http"}); st != http.StatusForbidden {
		t.Errorf("forbidden case = %d, want 403", st)
	}
	_, _ = send("POST", "/v1/messages", nil) // the upstream aborts mid-body

	// The record is read while serve runs (a clean stop removes it).
	record, rerr := os.ReadFile(rt.Record.Path)
	if rerr != nil {
		t.Fatalf("serve.json: %v", rerr)
	}
	if err := stop(); err != nil {
		t.Fatalf("serve returned %v", err)
	}

	mu.Lock()
	sawLogin := slices.Contains(upAuth, "Bearer "+login)
	n := len(upAuth)
	mu.Unlock()
	if !sawLogin || n != 2 {
		t.Errorf("upstream saw %d requests (login forwarded %t), want 2 with the login", n, sawLogin)
	}
	stderr := read()
	if !strings.Contains(stderr, "ReverseProxy") {
		t.Errorf("no proxy error line reached serve's stderr, so the scan proves nothing:\n%s", stderr)
	}
	for name, text := range map[string]string{"stderr": stderr, "stdout": readOut(), "serve.json": string(record)} {
		for _, s := range []string{login, tok, "SENTINEL"} {
			if strings.Contains(text, s) {
				t.Errorf("%s holds %q:\n%s", name, s, text)
			}
		}
	}
}
