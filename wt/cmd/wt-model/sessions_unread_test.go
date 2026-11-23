package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/exitcode"
	"github.com/ohanaverse/local-ai-setup/wt/internal/hoststop"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/servestate"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tlstest"
)

// deadPID is a pid no process has: a record naming it is a server that is
// not running (stale).
const deadPID = 1 << 30

// untrustedTLSRecord starts a TLS fake wt-model whose catalog gives
// ollama/a:1 two client sessions, and writes a record naming it with this
// process's pid (alive) and pid, trusting a different certificate: the
// session read fails TLS verification against a server that is up.
func untrustedTLSRecord(t *testing.T, pid int) servestate.Store {
	t.Helper()
	cert, key := tlstest.Gen(t, t.TempDir(), tlstest.Opts{})
	other, _ := tlstest.Gen(t, t.TempDir(), tlstest.Opts{})
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/control/v1/ping":
			_ = json.NewEncoder(w).Encode(modelapi.Ping{APIMajor: modelapi.APIMajor, Schema: modelapi.SchemaVersion, Server: "wt-model"})
		default:
			_ = json.NewEncoder(w).Encode(modelapi.Catalog{Schema: modelapi.SchemaVersion, Rows: []modelapi.Row{{ID: "ollama/a:1", Sessions: 2}}})
		}
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{tlstest.Pair(t, cert, key)}}
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	t.Cleanup(srv.Close)
	tok := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tok, []byte("tok"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := testRuntime(t).Record
	if err := store.Write(servestate.Record{PID: pid, Listen: srv.Listener.Addr().String(), TokenFile: tok, Started: time.Now(), TLSCert: other}); err != nil {
		t.Fatal(err)
	}
	return store
}

// TestSessionReaderSaysALiveServerCouldNotBeRead pins review finding I2 (b)
// for the reader: a record whose pid is alive and whose server cannot be
// read (here a TLS verification failure) sets the unread error, and its one
// note says the server is running and that wt asks, never that models are
// stopped without asking; a record whose pid is not running keeps today's
// note and leaves the unread error nil.
func TestSessionReaderSaysALiveServerCouldNotBeRead(t *testing.T) {
	var w bytes.Buffer
	var unread error
	if got := hostSessionReader(t.Context(), &w, untrustedTLSRecord(t, os.Getpid()), &unread)(); got != nil {
		t.Errorf("sessions = %v, want nil", got)
	}
	s := w.String()
	if unread == nil {
		t.Error("unread = nil for a live server that could not be read")
	}
	if !strings.HasPrefix(s, noteHead) || strings.Count(s, "\n") != 1 || strings.Contains(s, "stopped without asking") ||
		strings.Contains(s, "start it with") || !strings.Contains(s, "is running") {
		t.Errorf("note = %q, want one line saying the server is running and wt asks", s)
	}

	w.Reset()
	unread = nil
	_ = hostSessionReader(t.Context(), &w, untrustedTLSRecord(t, deadPID), &unread)()
	if unread != nil || !strings.HasPrefix(w.String(), noteHead) || !strings.HasSuffix(w.String(), noteTail) {
		t.Errorf("stale record: unread %v, note %q; want today's note and no unread error", unread, w.String())
	}
}

// TestStopAsksWhenALiveServersSessionsCannotBeRead pins review finding I2
// (b) for `wt-model stop`: when the running server's client sessions cannot
// be read (its certificate fails verification), the stop never goes on
// silently: it asks, a decline stops nothing (exit 3), and --yes stops. A
// record of a server that is not running stops without asking, as before.
func TestStopAsksWhenALiveServersSessionsCannotBeRead(t *testing.T) {
	run := func(t *testing.T, pid int, confirm bool, args ...string) (*[]string, int, error) {
		h := testHost()
		stopped := stubStop(t, h, nil)
		h.stopState = func(*config.Config) hoststop.StopState {
			n := h.stopSessions()["ollama/a:1"]
			return hoststop.StopState{Candidates: []hoststop.Candidate{cand("ollama", "ollama/a:1", "a:1", n)}}
		}
		asked := stubStopConfirm(t, h, confirm)
		_, _, err := runStopOn(h, modelCmdConfig(), untrustedTLSRecord(t, pid), args...)
		return asked, len(*stopped), err
	}
	asked, n, err := run(t, os.Getpid(), false, "ollama")
	if len(*asked) != 1 || !strings.Contains((*asked)[0], "could not be read") || n != 0 || exitcode.Of(err) != 3 {
		t.Errorf("live, declined: asked %q, stopped %d, err %v; want one question, nothing stopped, exit 3", *asked, n, err)
	}
	asked, n, err = run(t, os.Getpid(), false, "ollama", "--yes")
	if len(*asked) != 0 || n != 1 || err != nil {
		t.Errorf("live, --yes: asked %q, stopped %d, err %v; want no question and the stop", *asked, n, err)
	}
	asked, n, err = run(t, os.Getpid(), false, "--all")
	if len(*asked) != 1 || !strings.Contains((*asked)[0], "could not be read") || n != 0 || exitcode.Of(err) != 3 {
		t.Errorf("live --all, declined: asked %q, stopped %d, err %v", *asked, n, err)
	}
	asked, n, err = run(t, deadPID, false, "ollama")
	if len(*asked) != 0 || n != 1 || err != nil {
		t.Errorf("not running: asked %q, stopped %d, err %v; want today's stop without asking", *asked, n, err)
	}
}

// TestAliasRmRefusesWhenALiveServersSessionsCannotBeRead pins review finding
// I2 (b) for `wt-model alias rm`: an unknown session count from a running
// server is not "no session": the rm is refused like an alias in use (exit
// 3, naming --force-in-use, the registry unchanged), and --force-in-use
// removes it.
func TestAliasRmRefusesWhenALiveServersSessionsCannotBeRead(t *testing.T) {
	h, _, path := aliasEnv(t, aliasRegistryWithX)
	h.clientSessions = func(_ context.Context, _ io.Writer, _ servestate.Store, unread *error) func() map[string]int {
		return func() map[string]int {
			if unread != nil {
				*unread = errors.New("tls: failed to verify certificate")
			}
			return nil
		}
	}
	_, _, err := execAlias(t, h, "alias", "rm", "alias/x", "--yes")
	if err == nil || exitcode.Of(err) != 3 || !strings.Contains(err.Error(), "--force-in-use") || !strings.Contains(err.Error(), "could not be read") {
		t.Fatalf("err %v (exit %d), want exit 3 naming --force-in-use", err, exitcode.Of(err))
	}
	if mustRead(t, path) != aliasRegistryWithX {
		t.Fatal("a refused rm changed the registry")
	}
	out, _, err := execAlias(t, h, "alias", "rm", "alias/x", "--yes", "--force-in-use")
	if err != nil || !strings.HasPrefix(out, "removed alias: alias/x\n") {
		t.Fatalf("err %v stdout %q", err, out)
	}
}
