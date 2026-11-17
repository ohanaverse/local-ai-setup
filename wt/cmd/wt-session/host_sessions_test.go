package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
)

// TestHostSessionReaderCountsFromTheCatalog proves the host stop reads the
// model server's per-row session counts, once, and keeps only models a
// session uses.
func TestHostSessionReaderCountsFromTheCatalog(t *testing.T) {
	busy := wireRow("omlx/busy", modelapi.ActionLaunch, 1)
	busy.Sessions = 2
	idle := wireRow("omlx/idle", modelapi.ActionLaunch, 2)
	f := newFakeModelServer(t, busy, idle)
	a := pointAtFake(t, f, "")
	var w bytes.Buffer
	read := a.hostSessionReader(context.Background(), &w)
	got := read()
	if len(got) != 1 || got["omlx/busy"] != 2 {
		t.Fatalf("sessions = %v, want only omlx/busy:2", got)
	}
	before := f.count("GET /control/v1/catalog")
	read()
	if f.count("GET /control/v1/catalog") != before {
		t.Error("the catalog was read again: the reader must read once")
	}
	if w.Len() != 0 {
		t.Errorf("unexpected note: %q", w.String())
	}
}

// TestHostSessionReaderNotesAnUnreachableServer proves a configured server
// that cannot be read is a note (the stop then asks about nothing), while an
// unconfigured one is silent.
func TestHostSessionReaderNotesAnUnreachableServer(t *testing.T) {
	f := newFakeModelServer(t)
	a := pointAtFake(t, f, "")
	f.Close()
	var w bytes.Buffer
	if got := a.hostSessionReader(context.Background(), &w)(); got != nil {
		t.Errorf("sessions = %v, want nil", got)
	}
	if !strings.Contains(w.String(), "could not read client sessions from the model server") {
		t.Errorf("note = %q", w.String())
	}
}
