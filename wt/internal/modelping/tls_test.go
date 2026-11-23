package modelping

import (
	"context"
	"crypto/tls"
	"encoding/pem"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
)

// TestNewHTTPClientForTrustsTheCAFile pins decision 14's transport: a ping to
// a TLS server succeeds when Endpoint.CAFile holds its certificate and fails
// without it (the system roots do not know a test certificate), and a CAFile
// with no PEM certificate in it is an error before any dial. The client floor
// is TLS 1.2 in every case, and nothing skips verification.
func TestNewHTTPClientForTrustsTheCAFile(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pingBody(modelapi.APIMajor, modelapi.SchemaVersion, ServerName))
	}))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0) // the untrusted handshake is expected
	srv.StartTLS()
	defer srv.Close()
	dir := t.TempDir()
	ca := filepath.Join(dir, "server.crt")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := Ping(ctx, Endpoint{URL: srv.URL, Token: "t", CAFile: ca}); err != nil {
		t.Fatalf("Ping with the CA file = %v", err)
	}
	if _, err := Ping(ctx, Endpoint{URL: srv.URL, Token: "t"}); err == nil {
		t.Fatal("Ping without the CA file succeeded: the certificate was not verified")
	}
	if err := GetJSON(ctx, Endpoint{URL: srv.URL, Token: "t", CAFile: ca}, "/control/v1/ping", nil); err != nil {
		t.Errorf("GetJSON with the CA file = %v", err)
	}

	before := hits.Load()
	junk := filepath.Join(dir, "junk.pem")
	_ = os.WriteFile(junk, []byte("no certificate here\n"), 0o644)
	for _, bad := range []string{junk, filepath.Join(dir, "missing.pem")} {
		if _, err := NewHTTPClientFor(Endpoint{URL: srv.URL, CAFile: bad}); err == nil {
			t.Errorf("NewHTTPClientFor(%s) = nil error", bad)
		}
		if _, err := Ping(ctx, Endpoint{URL: srv.URL, Token: "t", CAFile: bad}); err == nil {
			t.Errorf("Ping with CAFile %s succeeded", bad)
		}
	}
	if hits.Load() != before {
		t.Errorf("a bad CA file still dialled the server (%d requests)", hits.Load()-before)
	}

	for _, ep := range []Endpoint{{}, {CAFile: ca, ServerName: "127.0.0.1"}} {
		hc, err := NewHTTPClientFor(ep)
		if err != nil {
			t.Fatal(err)
		}
		cfg := hc.Transport.(*http.Transport).TLSClientConfig
		if cfg == nil || cfg.MinVersion != tls.VersionTLS12 || cfg.InsecureSkipVerify || cfg.ServerName != ep.ServerName {
			t.Errorf("%+v: TLS config = %+v, want a 1.2 floor, verification on and ServerName %q", ep, cfg, ep.ServerName)
		}
		if (cfg.RootCAs != nil) != (ep.CAFile != "") {
			t.Errorf("%+v: RootCAs set = %t", ep, cfg.RootCAs != nil)
		}
		if hc.Transport.(*http.Transport).Proxy != nil || hc.CheckRedirect == nil {
			t.Errorf("%+v: the transport lost NewHTTPClient's no-proxy or no-redirect rule", ep)
		}
	}
	if NewHTTPClient().Transport.(*http.Transport).TLSClientConfig.MinVersion != tls.VersionTLS12 {
		t.Error("NewHTTPClient is not the no-CA case of NewHTTPClientFor")
	}
}
