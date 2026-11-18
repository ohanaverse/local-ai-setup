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

// TestHostSessionReaderIsSilentWithNoServer proves a host stop on a machine
// with no model server to ask (an unset mode, or native mode) has simply no
// sessions to ask about and prints no note: only a configured server that
// cannot be read is worth one. The native case keeps a stray url and token
// file naming a stopped server, which native mode ignores: it must neither
// try that server nor note it.
func TestHostSessionReaderIsSilentWithNoServer(t *testing.T) {
	stray, _ := closedServerConfig(t, "native", "")
	for name, body := range map[string]string{
		"unset":  "default_tag = \"code\"\n",
		"native": stray,
	} {
		t.Run(name, func(t *testing.T) {
			writeSessionConfig(t, body)
			var w bytes.Buffer
			if got := (&app{hooks: testHooks()}).hostSessionReader(context.Background(), &w)(); got != nil || w.Len() != 0 {
				t.Errorf("sessions = %v, note = %q; want nil and no note", got, w.String())
			}
		})
	}
}
