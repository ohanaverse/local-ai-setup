package modelserver

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tlsfile"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tlstest"
)

// tlsServerConfig loads a fresh tlstest pair through tlsfile, as serve does,
// and returns its server config and the certificate's path.
func tlsServerConfig(t *testing.T) (*tls.Config, string) {
	t.Helper()
	cert, key := tlstest.Gen(t, t.TempDir(), tlstest.Opts{})
	pair, _, err := tlsfile.Load(cert, key, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return tlsfile.Config(pair), cert
}

// serveOn runs s.Serve until the test ends and returns the announced address.
func serveOn(t *testing.T, s *Server) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	addrc := make(chan string, 1)
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, func(a string) { addrc <- a }) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Serve = %v", err)
			}
		case <-time.After(15 * time.Second):
			t.Error("Serve did not return")
		}
	})
	select {
	case a := <-addrc:
		return a
	case <-time.After(5 * time.Second):
		t.Fatal("Serve never announced")
		return ""
	}
}

// trustingClient is an HTTP/2-capable client that trusts exactly certPath.
func trustingClient(t *testing.T, certPath string) *http.Client {
	t.Helper()
	tr := &http.Transport{
		TLSClientConfig:   &tls.Config{RootCAs: tlstest.Pool(t, certPath), MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2: true,
	}
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Transport: tr}
}

// TestServeOverTLS pins decision 13's serving path: with Options.TLS set,
// Serve still announces the bound address of a :0 listener, a client that
// trusts the certificate pings 200 over HTTP/2, and plain http to the same
// port is not a ping (the server speaks only TLS there).
func TestServeOverTLS(t *testing.T) {
	cfg, cert := tlsServerConfig(t)
	s, err := NewServer(Options{Listen: "127.0.0.1:0", Token: testToken, IdleGrace: time.Hour, TLS: cfg},
		(&fakeLife{snap: serverSnap()}).deps(serverCfg()))
	if err != nil {
		t.Fatal(err)
	}
	addr := serveOn(t, s)
	req, _ := http.NewRequest("GET", "https://"+addr+"/control/v1/ping", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := trustingClient(t, cert).Do(req)
	if err != nil {
		t.Fatalf("ping over TLS: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.ProtoMajor != 2 {
		t.Fatalf("ping = %d proto %s %s, want 200 over HTTP/2", resp.StatusCode, resp.Proto, body)
	}
	var p modelapi.Ping
	if err := json.Unmarshal(body, &p); err != nil || p.Server != "wt-model" || p.TLSOff != "" {
		t.Errorf("ping body = %s (%v)", body, err)
	}
	preq, _ := http.NewRequest("GET", "http://"+addr+"/control/v1/ping", nil)
	preq.Header.Set("Authorization", "Bearer "+testToken)
	presp, err := (&http.Client{Timeout: 5 * time.Second}).Do(preq)
	if err == nil {
		pbody, _ := io.ReadAll(presp.Body)
		presp.Body.Close()
		if presp.StatusCode == http.StatusOK || bytes.Contains(pbody, []byte(`"wt-model"`)) {
			t.Errorf("plain http to the TLS port answered a ping: %d %s", presp.StatusCode, pbody)
		}
	}
}

// TestGatewayStreamsOverTLS pins that the gateway's streaming survives TLS
// and HTTP/2: each server-sent event reaches the client before the upstream
// writes the next (FlushInterval -1 over h2), and a client that aborts
// cancels the upstream request.
func TestGatewayStreamsOverTLS(t *testing.T) {
	next := make(chan struct{})
	upDone := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: one\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-next:
		case <-r.Context().Done():
			close(upDone)
			return
		}
		fmt.Fprint(w, "data: two\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(upDone)
	}))
	defer up.Close()
	u, _ := url.Parse(up.URL)
	cfg, cert := tlsServerConfig(t)
	s, err := NewServer(Options{Listen: "127.0.0.1:0", Token: testToken, IdleGrace: time.Hour, TLS: cfg,
		Upstream: u, UpstreamKey: "upstream-key", ErrorLog: log.New(io.Discard, "", 0)},
		(&fakeLife{snap: serverSnap()}).deps(serverCfg()))
	if err != nil {
		t.Fatal(err)
	}
	addr := serveOn(t, s)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://"+addr+"/v1/chat/completions", strings.NewReader(`{"stream":true}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := trustingClient(t, cert).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.ProtoMajor != 2 {
		t.Fatalf("stream = %d %s, want 200 over HTTP/2", resp.StatusCode, resp.Proto)
	}
	lines := make(chan string, 8)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if l := sc.Text(); l != "" {
				lines <- l
			}
		}
		close(lines)
	}()
	want := func(s string) {
		t.Helper()
		select {
		case l := <-lines:
			if l != s {
				t.Fatalf("event %q, want %q", l, s)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("event %q never arrived: not flushed over h2", s)
		}
	}
	want("data: one") // before the upstream is allowed to write the second
	close(next)
	want("data: two")
	cancel()
	select {
	case <-upDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the client abort did not cancel the upstream request")
	}
}

// TestPingReportsTLSOff pins decision 2's TLS fact: Options.TLSOff set (TLS
// configured, serve fell back to plain http on the certificate's dates) is
// the ping's tls_off; empty leaves the key out of the JSON.
func TestPingReportsTLSOff(t *testing.T) {
	for _, off := range []string{"", "certificate expired 2026-11-01"} {
		s, err := NewServer(Options{Listen: "127.0.0.1:0", Token: testToken, IdleGrace: time.Hour, TLSOff: off},
			(&fakeLife{snap: serverSnap()}).deps(serverCfg()))
		if err != nil {
			t.Fatal(err)
		}
		ts := httptest.NewServer(s.Handler())
		st, b := call(t, ts, "GET", "/control/v1/ping", "")
		ts.Close()
		if st != http.StatusOK {
			t.Fatalf("ping = %d %s", st, b)
		}
		var p modelapi.Ping
		if err := json.Unmarshal(b, &p); err != nil || p.TLSOff != off {
			t.Errorf("off=%q: ping = %+v (%v)", off, p, err)
		}
		if has := bytes.Contains(b, []byte("tls_off")); has != (off != "") {
			t.Errorf("off=%q: raw ping %s carries tls_off = %t", off, b, has)
		}
	}
}

// TestPlainServeUnchanged pins Review Focus 2 for TLS: without Options.TLS
// the http.Server is today's exactly (Handler and ReadHeaderTimeout, no
// IdleTimeout, no ErrorLog, no TLSConfig, no read or write timeout), and only
// with TLS does it gain IdleTimeout 120s and the ErrorLog, still with no
// read or write timeout (prompts and streams are long).
func TestPlainServeUnchanged(t *testing.T) {
	logger := log.New(io.Discard, "", 0)
	s, err := NewServer(Options{Listen: "127.0.0.1:0", Token: testToken, IdleGrace: time.Hour, ErrorLog: logger},
		(&fakeLife{snap: serverSnap()}).deps(serverCfg()))
	if err != nil {
		t.Fatal(err)
	}
	srv := s.httpServer()
	if srv.Handler == nil || srv.ReadHeaderTimeout != 10*time.Second || srv.IdleTimeout != 0 || srv.ErrorLog != nil ||
		srv.TLSConfig != nil || srv.ReadTimeout != 0 || srv.WriteTimeout != 0 || srv.TLSNextProto != nil {
		t.Errorf("plain http.Server = %+v, want only Handler and ReadHeaderTimeout", srv)
	}
	cfg, _ := tlsServerConfig(t)
	s, err = NewServer(Options{Listen: "127.0.0.1:0", Token: testToken, IdleGrace: time.Hour, ErrorLog: logger, TLS: cfg},
		(&fakeLife{snap: serverSnap()}).deps(serverCfg()))
	if err != nil {
		t.Fatal(err)
	}
	srv = s.httpServer()
	if srv.TLSConfig == nil || srv.IdleTimeout != 120*time.Second || srv.ErrorLog != logger ||
		srv.ReadTimeout != 0 || srv.WriteTimeout != 0 || srv.ReadHeaderTimeout != 10*time.Second {
		t.Errorf("TLS http.Server = %+v, want TLSConfig, IdleTimeout 120s and the ErrorLog only", srv)
	}
	if srv.TLSConfig.InsecureSkipVerify {
		t.Error("InsecureSkipVerify set")
	}
}
