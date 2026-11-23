package servestate

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelping"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tlstest"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tokenfile"
)

// fakeServer is a wt-model control API answering ping and catalog for
// token only (401 with an APIError body otherwise); it counts catalog hits.
func fakeServer(t *testing.T, token string, rows ...modelapi.Row) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var catalogHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(modelapi.APIError{Error: modelapi.ErrorObject{Message: "unauthorized", Code: "unauthorized"}})
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/control/v1/ping":
			_ = json.NewEncoder(w).Encode(modelapi.Ping{APIMajor: modelapi.APIMajor, Schema: modelapi.SchemaVersion, Server: "wt-model"})
		case r.Method == http.MethodGet && r.URL.Path == "/control/v1/catalog":
			catalogHits.Add(1)
			_ = json.NewEncoder(w).Encode(modelapi.Catalog{Schema: modelapi.SchemaVersion, Rows: rows})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &catalogHits
}

// writeToken writes tok to a 0600 temp file and returns its path.
func writeToken(t *testing.T, tok string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(p, []byte(tok), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// rec builds the record a server at srv would have written, its token file
// holding tok.
func rec(t *testing.T, addr, tok string) Record {
	t.Helper()
	return Record{Version: RecordVersion, PID: os.Getpid(), Listen: addr, TokenFile: writeToken(t, tok), Started: time.Now()}
}

// TestSessionsCountsFromTheCatalog checks that only models with a client
// session are returned, mapped to their session count.
func TestSessionsCountsFromTheCatalog(t *testing.T) {
	srv, _ := fakeServer(t, "tok", modelapi.Row{ID: "omlx/busy", Sessions: 2}, modelapi.Row{ID: "omlx/idle"})
	got, err := Sessions(context.Background(), rec(t, srv.Listener.Addr().String(), "tok"))
	if err != nil {
		t.Fatalf("Sessions: %v", err)
	}
	if len(got) != 1 || got["omlx/busy"] != 2 {
		t.Fatalf("Sessions = %v, want map[omlx/busy:2]", got)
	}
}

// TestSessionsWithNoSessionsIsAnEmptyMap checks that a server with no client
// session answers a non-nil empty map, not nil.
func TestSessionsWithNoSessionsIsAnEmptyMap(t *testing.T) {
	srv, _ := fakeServer(t, "tok", modelapi.Row{ID: "omlx/idle"})
	got, err := Sessions(context.Background(), rec(t, srv.Listener.Addr().String(), "tok"))
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("Sessions = %v (nil %v), %v; want an empty non-nil map, nil", got, got == nil, err)
	}
}

// TestSessionsDialsLoopbackForAnUnspecifiedHost checks that a server bound to
// every interface (0.0.0.0 or [::]) is reached on loopback, and that a
// specific host is kept as is.
func TestSessionsDialsLoopbackForAnUnspecifiedHost(t *testing.T) {
	srv, hits := fakeServer(t, "tok")
	_, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	for _, listen := range []string{"0.0.0.0:" + port, "[::]:" + port} {
		if _, err := Sessions(context.Background(), rec(t, listen, "tok")); err != nil {
			t.Fatalf("Sessions(%s): %v", listen, err)
		}
	}
	if n := hits.Load(); n != 2 {
		t.Fatalf("catalog hits = %d, want 2", n)
	}
	for in, want := range map[string]string{
		"0.0.0.0:8787":   "http://127.0.0.1:8787",
		"[::]:8787":      "http://127.0.0.1:8787",
		"127.0.0.1:8787": "http://127.0.0.1:8787",
		"[::1]:8787":     "http://[::1]:8787",
	} {
		if got := dialURL(in); got != want {
			t.Errorf("dialURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestSessionsErrorNeverCarriesTheToken checks that a refused token is an
// UnauthorizedError whose text holds neither the sent nor the wanted token.
func TestSessionsErrorNeverCarriesTheToken(t *testing.T) {
	srv, _ := fakeServer(t, "right-token")
	_, err := Sessions(context.Background(), rec(t, srv.Listener.Addr().String(), "tok-secret-xyz"))
	if err == nil {
		t.Fatal("Sessions = nil error, want unauthorized")
	}
	if strings.Contains(err.Error(), "tok-secret-xyz") || strings.Contains(err.Error(), "right-token") {
		t.Fatalf("error carries a token: %v", err)
	}
	var ue *modelping.UnauthorizedError
	if !errors.As(err, &ue) {
		t.Fatalf("error = %T %v, want *modelping.UnauthorizedError", err, err)
	}
}

// TestSessionsReportsAnUnreadableTokenFile checks that a missing token file
// is tokenfile's error, and that no request is sent without a token.
func TestSessionsReportsAnUnreadableTokenFile(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	r := rec(t, srv.Listener.Addr().String(), "tok")
	r.TokenFile = filepath.Join(t.TempDir(), "missing")
	_, err := Sessions(context.Background(), r)
	var te *tokenfile.Error
	if err == nil || !errors.As(err, &te) {
		t.Fatalf("Sessions = %v, want *tokenfile.Error", err)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("server saw %d requests, want 0", n)
	}
}

// TestSessionsReportsAStaleRecord checks that a record naming a server that
// is gone is an UnreachableError, the caller's to tolerate.
func TestSessionsReportsAStaleRecord(t *testing.T) {
	srv, _ := fakeServer(t, "tok")
	addr := srv.Listener.Addr().String()
	srv.Close()
	_, err := Sessions(context.Background(), rec(t, addr, "tok"))
	var ue *modelping.UnreachableError
	if err == nil || !errors.As(err, &ue) {
		t.Fatalf("Sessions = %v, want *modelping.UnreachableError", err)
	}
}

// TestSessionsGivesUpOnAServerThatNeverAnswers checks that a record whose
// address accepts and never answers (a hung server, or an unrelated process
// on the port) must not hang a host stop: the caller's context bounds it
// (Review Focus #5).
func TestSessionsGivesUpOnAServerThatNeverAnswers(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var conns []net.Conn
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			c.Close()
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = Sessions(ctx, rec(t, ln.Addr().String(), "tok-secret-xyz"))
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("Sessions took %v, want under 3s", took)
	}
	if err == nil {
		t.Fatal("Sessions = nil error, want a timeout")
	}
	if strings.Contains(err.Error(), "tok-secret-xyz") {
		t.Fatalf("error carries the token: %v", err)
	}
}

// TestSessionsStaleRecordSendsNothing checks that a record whose pid is not
// running is an error and no request, token-bearing or not, reaches its address.
func TestSessionsStaleRecordSendsNothing(t *testing.T) {
	srv, hits := fakeServer(t, "tok", modelapi.Row{ID: "omlx/busy", Sessions: 1})
	r := rec(t, srv.Listener.Addr().String(), "tok")
	got, err := sessions(context.Background(), r, func(int) bool { return false })
	if err == nil || got != nil {
		t.Fatalf("Sessions = %v, %v; want nil, error", got, err)
	}
	if hits.Load() != 0 {
		t.Fatalf("catalog hit %d times, want 0", hits.Load())
	}
}

// tlsFakeServer is fakeServer behind TLS with a tlstest certificate; it
// returns the server and the certificate's path.
func tlsFakeServer(t *testing.T, token string, rows ...modelapi.Row) (*httptest.Server, string, *atomic.Int32) {
	t.Helper()
	cert, key := tlstest.Gen(t, t.TempDir(), tlstest.Opts{})
	var hits atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/control/v1/ping":
			_ = json.NewEncoder(w).Encode(modelapi.Ping{APIMajor: modelapi.APIMajor, Schema: modelapi.SchemaVersion, Server: "wt-model"})
		case "/control/v1/catalog":
			_ = json.NewEncoder(w).Encode(modelapi.Catalog{Schema: modelapi.SchemaVersion, Rows: rows})
		default:
			http.NotFound(w, r)
		}
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{tlstest.Pair(t, cert, key)}}
	srv.Config.ErrorLog = log.New(io.Discard, "", 0) // refused handshakes are expected
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv, cert, &hits
}

// TestSessionsDialHTTPSWhenRecorded pins decision 13's host-command half:
// a record carrying tls_cert is dialled over https, trusting exactly that
// certificate, so wt-model stop and wt-model alias keep reading client
// sessions once serve speaks TLS; a record without it against the same
// server is an error (http to a TLS port), never an empty map that would read
// as "no client session uses this model". Liveness comes through the
// per-call alive argument (no package var).
func TestSessionsDialHTTPSWhenRecorded(t *testing.T) {
	srv, cert, _ := tlsFakeServer(t, "tok", modelapi.Row{ID: "omlx/busy", Sessions: 3})
	alive := func(int) bool { return true }
	r := rec(t, srv.Listener.Addr().String(), "tok")
	r.TLSCert = cert
	got, err := sessions(context.Background(), r, alive)
	if err != nil || got["omlx/busy"] != 3 {
		t.Fatalf("sessions over TLS = %v, %v; want map[omlx/busy:3]", got, err)
	}
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	r.Listen = "0.0.0.0:" + port
	if got, err := sessions(context.Background(), r, alive); err != nil || got["omlx/busy"] != 3 {
		t.Fatalf("sessions over TLS on an unspecified listen = %v, %v", got, err)
	}
	r.Listen = srv.Listener.Addr().String()
	r.TLSCert = ""
	got, err = sessions(context.Background(), r, alive)
	if err == nil || got != nil {
		t.Fatalf("a record without tls_cert against a TLS server = %v, %v; want nil and an error", got, err)
	}
	// A record naming another certificate fails verification: nothing is
	// trusted beyond the recorded file.
	other, _ := tlstest.Gen(t, t.TempDir(), tlstest.Opts{})
	r.TLSCert = other
	if got, err := sessions(context.Background(), r, alive); err == nil || got != nil {
		t.Fatalf("sessions trusting another certificate = %v, %v; want an error", got, err)
	}
	for in, want := range map[string]string{
		"0.0.0.0:8787":   "https://127.0.0.1:8787",
		"127.0.0.1:8787": "https://127.0.0.1:8787",
	} {
		if got := dialURLTLS(in); got != want {
			t.Errorf("dialURLTLS(%q) = %q, want %q", in, got, want)
		}
	}
}
