package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/servestate"
)

// fakeServer is a wt-model control API answering ping and catalog for token
// only (401 with an APIError body otherwise); it counts catalog hits. It is
// this package's copy of servestate's test helper.
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

// recordFor writes, into a scratch store, the runtime record a server at srv
// would have written, its token file holding token, and returns that store.
func recordFor(t *testing.T, srv *httptest.Server, token string) servestate.Store {
	t.Helper()
	tok := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tok, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	store := testRuntime(t).Record
	r := servestate.Record{PID: os.Getpid(), Listen: srv.Listener.Addr().String(), TokenFile: tok, Started: time.Now()}
	if err := store.Write(r); err != nil {
		t.Fatal(err)
	}
	return store
}

const noteHead = "wt-model: note: could not read client sessions from the model server ("
const noteTail = ") — models they use are stopped without asking\n"

// TestHostSessionReaderCountsFromTheCatalog proves the host stop reads the
// live server's per-row session counts through the record, once, and keeps
// only models a session uses.
func TestHostSessionReaderCountsFromTheCatalog(t *testing.T) {
	srv, catalogHits := fakeServer(t, "tok",
		modelapi.Row{ID: "omlx/busy", Sessions: 2}, modelapi.Row{ID: "omlx/idle", Sessions: 0})
	store := recordFor(t, srv, "tok")
	var w bytes.Buffer
	read := hostSessionReader(t.Context(), &w, store, nil)
	got := read()
	if len(got) != 1 || got["omlx/busy"] != 2 {
		t.Fatalf("sessions = %v, want map[omlx/busy:2]", got)
	}
	read()
	if n := catalogHits.Load(); n != 1 {
		t.Errorf("catalog read %d times, want 1: the reader must read once", n)
	}
	if w.Len() != 0 {
		t.Errorf("unexpected note: %q", w.String())
	}
}

// TestHostSessionReaderIsSilentWithNoRecord proves a host stop with no
// running server (no record, or no config home at all) has no sessions to
// ask about and prints no note.
func TestHostSessionReaderIsSilentWithNoRecord(t *testing.T) {
	for name, store := range map[string]servestate.Store{
		"no file": testRuntime(t).Record,
		"no path": {},
	} {
		t.Run(name, func(t *testing.T) {
			var w bytes.Buffer
			if got := hostSessionReader(t.Context(), &w, store, nil)(); got != nil || w.Len() != 0 {
				t.Errorf("sessions = %v, note = %q; want nil and no note", got, w.String())
			}
		})
	}
}

// TestHostSessionReaderNotesAnUnreachableServer proves a record naming a
// server that no longer answers is one note line, and no sessions. While the
// record's pid is alive the note says the server is running and wt asks
// (review finding I2: an unknown count is not "no session"), and the unread
// error is set; a record whose pid is gone keeps today's note.
func TestHostSessionReaderNotesAnUnreachableServer(t *testing.T) {
	srv, _ := fakeServer(t, "tok")
	store := recordFor(t, srv, "tok")
	srv.Close()
	var w bytes.Buffer
	var unread error
	if got := hostSessionReader(t.Context(), &w, store, &unread)(); got != nil {
		t.Errorf("sessions = %v, want nil", got)
	}
	s := w.String()
	if !strings.HasPrefix(s, noteHead) || strings.HasSuffix(s, noteTail) || !strings.Contains(s, "is running") || strings.Count(s, "\n") != 1 || unread == nil {
		t.Errorf("live record: note = %q, unread %v; want one line saying the server is running, and unread set", s, unread)
	}
	r, _, _ := store.Read()
	r.PID = deadPID
	if err := store.Write(r); err != nil {
		t.Fatal(err)
	}
	w.Reset()
	unread = nil
	_ = hostSessionReader(t.Context(), &w, store, &unread)()
	if s := w.String(); !strings.HasPrefix(s, noteHead) || !strings.HasSuffix(s, noteTail) || strings.Count(s, "\n") != 1 || unread != nil {
		t.Errorf("stale record: note = %q, unread %v; want today's %q...%q", s, unread, noteHead, noteTail)
	}
}

// TestHostSessionReaderNotesAnUnreadableRecord proves a record that does not
// decode is one note line naming the record's path, and no sessions.
func TestHostSessionReaderNotesAnUnreadableRecord(t *testing.T) {
	store := testRuntime(t).Record
	if err := os.WriteFile(store.Path, []byte(`{"pid":`), 0o600); err != nil {
		t.Fatal(err)
	}
	var w bytes.Buffer
	if got := hostSessionReader(t.Context(), &w, store, nil)(); got != nil {
		t.Errorf("sessions = %v, want nil", got)
	}
	s := w.String()
	if !strings.HasPrefix(s, noteHead) || !strings.HasSuffix(s, noteTail) || strings.Count(s, "\n") != 1 {
		t.Errorf("note = %q, want one line %q...%q", s, noteHead, noteTail)
	}
	if !strings.Contains(s, store.Path) {
		t.Errorf("note = %q, want it to name %s", s, store.Path)
	}
}

// TestHostSessionReaderNeverPrintsTheToken proves a server refusing the
// recorded token is a note that carries neither the token the file holds nor
// the one the server wants.
func TestHostSessionReaderNeverPrintsTheToken(t *testing.T) {
	srv, _ := fakeServer(t, "right-token")
	store := recordFor(t, srv, "tok-secret-xyz")
	var w bytes.Buffer
	if got := hostSessionReader(t.Context(), &w, store, nil)(); got != nil {
		t.Errorf("sessions = %v, want nil", got)
	}
	s := w.String()
	if !strings.HasPrefix(s, noteHead) {
		t.Errorf("note = %q, want it to start %q", s, noteHead)
	}
	for _, secret := range []string{"tok-secret-xyz", "right-token"} {
		if strings.Contains(s, secret) {
			t.Errorf("note %q carries %q", s, secret)
		}
	}
}

// TestHostSessionReaderIsSilentWhenCancelled proves a session read cut short
// by Ctrl+C (the command's context cancelled) prints no note: the note says
// models are stopped without asking, which a cancelled command never does.
func TestHostSessionReaderIsSilentWhenCancelled(t *testing.T) {
	srv, _ := fakeServer(t, "tok", modelapi.Row{ID: "ollama/a:1", Sessions: 2})
	store := recordFor(t, srv, "tok")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var w bytes.Buffer
	if got := hostSessionReader(ctx, &w, store, nil)(); got != nil || w.Len() != 0 {
		t.Errorf("sessions = %v, note = %q; want nil and no note", got, w.String())
	}
}

// TestAliasCommandsSessionNoteFitsTheirContext proves the session reader the
// alias commands get (newHost's clientSessions) words its failure note for
// them: `wt-model stop`'s "models they use are stopped without asking" would
// tell an `alias list|set|rm` user something that never happens.
func TestAliasCommandsSessionNoteFitsTheirContext(t *testing.T) {
	store := testRuntime(t).Record
	if err := os.WriteFile(store.Path, []byte(`{"pid":`), 0o600); err != nil {
		t.Fatal(err)
	}
	var w bytes.Buffer
	if got := newHost().clientSessions(t.Context(), &w, store, nil)(); got != nil {
		t.Errorf("sessions = %v, want nil", got)
	}
	s := w.String()
	if !strings.HasPrefix(s, noteHead) || strings.Contains(s, "stopped without asking") || strings.Count(s, "\n") != 1 {
		t.Errorf("note = %q, want one line starting %q and not worded for wt-model stop", s, noteHead)
	}
}
