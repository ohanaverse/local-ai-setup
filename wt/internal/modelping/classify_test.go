package modelping

import (
	"context"
	"crypto/tls"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tlstest"
)

// TestClassifyMapsEachPingError pins the classification table that replaces
// modelsetup's observe switch: every typed ping error gets its Kind (through
// wraps), an unknown type reads as unreachable, nil reads as the zero
// Outcome, and Message is the error's text whole for every non-nil error.
func TestClassifyMapsEachPingError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		kind Kind
		api  int
		sch  int
	}{
		{"nil", nil, KindNone, 0, 0},
		{"unreachable", &UnreachableError{"a", "refused"}, KindUnreachable, 0, 0},
		{"unauthorized", &UnauthorizedError{"a"}, KindUnauthorized, 0, 0},
		{"not a wt-model", &NotModelServerError{"a", "x"}, KindNotWtModel, 0, 0},
		{"skew", &SkewError{"a", 2, 1}, KindSkew, 2, 0},
		{"schema", &SchemaError{"a", 2, 1}, KindSchema, 0, 2},
		{"wrapped", fmt.Errorf("ctx: %w", &UnauthorizedError{"a"}), KindUnauthorized, 0, 0},
		{"unknown type", errors.New("x"), KindUnreachable, 0, 0},
		{"credentials in url", &CredentialsInURLError{}, KindUnreachable, 0, 0},
	}
	for _, c := range cases {
		got := Classify(c.err)
		if got.Kind != c.kind || got.API != c.api || got.Schema != c.sch {
			t.Errorf("%s: Outcome = %+v, want Kind=%d API=%d Schema=%d", c.name, got, c.kind, c.api, c.sch)
		}
		if c.err == nil {
			if got.Message != "" {
				t.Errorf("nil: Message = %q, want \"\"", got.Message)
			}
			continue
		}
		if got.Message != c.err.Error() {
			t.Errorf("%s: Message = %q, want %q", c.name, got.Message, c.err.Error())
		}
	}
}

// TestCheckedReturnsTheClassifiedFailure pins Checked's triple: a good server
// returns (ping, KindNone, "", nil); a failed ping returns the error beside
// its classification, with Message equal to the error's own text.
func TestCheckedReturnsTheClassifiedFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(fmt.Sprintf(`{"api_major":%d,"schema":%d,"server":%q}`, modelapi.APIMajor, modelapi.SchemaVersion, ServerName)))
	}))
	ep := Endpoint{URL: srv.URL, Token: "t"}
	p, kind, msg, err := Checked(context.Background(), ep)
	if err != nil || kind != KindNone || msg != "" {
		t.Errorf("good server: (%v, %d, %q, %v), want (ping, KindNone, \"\", nil)", p, kind, msg, err)
	}
	srv.Close()

	_, kind, msg, err = Checked(context.Background(), ep)
	if err == nil || kind != KindUnreachable {
		t.Fatalf("closed server: (kind %d, msg %q, err %v), want KindUnreachable with an error", kind, msg, err)
	}
	if msg != err.Error() {
		t.Errorf("msg = %q, want the error's own text %q", msg, err.Error())
	}
}

// pingJSON answers a clean wt-model ping.
func pingJSON(w http.ResponseWriter) {
	_, _ = w.Write([]byte(fmt.Sprintf(`{"api_major":%d,"schema":%d,"server":%q}`, modelapi.APIMajor, modelapi.SchemaVersion, ServerName)))
}

// countingTLSServer is an httptest TLS server whose handler counts the
// requests it sees, serving cert when set (else httptest's own).
func countingTLSServer(t *testing.T, certPath, keyPath string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n.Add(1)
		pingJSON(w)
	}))
	if certPath != "" {
		srv.TLS = &tls.Config{Certificates: []tls.Certificate{tlstest.Pair(t, certPath, keyPath)}}
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv, &n
}

// trustErr is err's *TLSTrustError, failing the test when there is none.
func trustErr(t *testing.T, err error) *TLSTrustError {
	t.Helper()
	var te *TLSTrustError
	if !errors.As(err, &te) {
		t.Fatalf("err = %v (%T), want a *TLSTrustError", err, err)
	}
	if strings.Contains(err.Error(), "SENTINEL") {
		t.Errorf("the error holds the token: %q", err.Error())
	}
	if o := Classify(err); o.Kind != KindUnreachable || o.Message != err.Error() {
		t.Errorf("Classify = %+v, want unreachable with the hint", o)
	}
	return te
}

// TestUntrustedServerCertGivesTheCAFileHint proves a server whose certificate
// this machine does not trust fails the ping with decision 14's ca_file hint,
// and the handshake fails before the request is written: the server's
// handler sees nothing, so the bearer token never left. With ca_file naming
// the server's certificate the same ping succeeds.
func TestUntrustedServerCertGivesTheCAFileHint(t *testing.T) {
	srv, n := countingTLSServer(t, "", "")
	ep := Endpoint{URL: srv.URL, Token: "wt-SENTINEL-tok"}
	_, err := Ping(context.Background(), ep)
	te := trustErr(t, err)
	if !strings.Contains(te.Error(), "set ca_file under [model-server] in ~/.config/agent-wt/config.toml (or WT_MODEL_CA_FILE) to the server's certificate") {
		t.Errorf("hint = %q", te.Error())
	}
	if n.Load() != 0 {
		t.Errorf("the handler saw %d requests: the token was sent to an untrusted server", n.Load())
	}
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o644); err != nil {
		t.Fatal(err)
	}
	ep.CAFile = ca
	if _, err := Ping(context.Background(), ep); err != nil {
		t.Fatalf("ping with ca_file: %v", err)
	}
}

// TestHostnameMismatchHint proves a trusted certificate that does not name
// the host dialled fails with the hint naming that host and the SAN fix.
func TestHostnameMismatchHint(t *testing.T) {
	dir := t.TempDir()
	cert, key := tlstest.Gen(t, dir, tlstest.Opts{DNS: []string{"wrong.invalid"}})
	srv, n := countingTLSServer(t, cert, key)
	_, err := Ping(context.Background(), Endpoint{URL: srv.URL, Token: "wt-SENTINEL-tok", CAFile: cert})
	te := trustErr(t, err)
	if !strings.Contains(te.Error(), "the certificate does not name 127.0.0.1: regenerate it with a subjectAltName for that host (an IP needs an IP SAN)") {
		t.Errorf("hint = %q", te.Error())
	}
	if n.Load() != 0 {
		t.Errorf("the handler saw %d requests", n.Load())
	}
}

// TestExpiredServerCertHint proves an expired certificate (a server whose
// clock disagrees with this one) fails with a hint naming the date.
func TestExpiredServerCertHint(t *testing.T) {
	dir := t.TempDir()
	end := time.Now().Add(-24 * time.Hour)
	cert, key := tlstest.Gen(t, dir, tlstest.Opts{NotBefore: end.Add(-48 * time.Hour), NotAfter: end})
	srv, n := countingTLSServer(t, cert, key)
	_, err := Ping(context.Background(), Endpoint{URL: srv.URL, Token: "wt-SENTINEL-tok", CAFile: cert})
	te := trustErr(t, err)
	if !strings.Contains(te.Error(), "expired on "+end.UTC().Format("2006-01-02")) {
		t.Errorf("hint = %q, want the expiry date", te.Error())
	}
	if n.Load() != 0 {
		t.Errorf("the handler saw %d requests", n.Load())
	}
}

// TestSchemeMismatchHints proves both scheme mistakes get their hint: https
// to a plain server names the scheme and the expired-certificate fallback;
// http to a TLS server says the token was already sent unencrypted to the
// address, and for a non-loopback host to rotate it on the host.
func TestSchemeMismatchHints(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { pingJSON(w) }))
	t.Cleanup(plain.Close)
	addr := plain.Listener.Addr().String()
	_, err := Ping(context.Background(), Endpoint{URL: "https://" + addr, Token: "wt-SENTINEL-tok"})
	te := trustErr(t, err)
	if !strings.Contains(te.Error(), "speaks plain http") || !strings.Contains(te.Error(), "if the server's TLS certificate expired, wt-model serve falls back to plain http: renew it on the host and restart serve") {
		t.Errorf("https to plain: %q", te.Error())
	}

	srv, _ := countingTLSServer(t, "", "")
	taddr := srv.Listener.Addr().String()
	_, err = Ping(context.Background(), Endpoint{URL: "http://" + taddr, Token: "wt-SENTINEL-tok"})
	te = trustErr(t, err)
	want := "the model server speaks https: use https://" + taddr + " in [model-server] url; this request already sent the token unencrypted to " + taddr
	if !strings.Contains(te.Error(), want) || strings.Contains(te.Error(), "rotate") {
		t.Errorf("http to TLS, loopback: %q, want %q and no rotate", te.Error(), want)
	}
	if h := httpToTLSHint("192.0.2.7:8787"); !strings.HasSuffix(h, "this request already sent the token unencrypted to 192.0.2.7:8787; rotate the token on the host (wt-model setup)") {
		t.Errorf("non-loopback hint = %q", h)
	}
}
