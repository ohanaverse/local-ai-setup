package main

import (
	"context"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
)

func remoteAgent() config.Agent { return config.Agent{Name: "claude", AllProviders: true} }

func openFake(t *testing.T, f *fakeModelServer) *modelSession {
	t.Helper()
	ms, err := pointAtFake(t, f, "").openModels(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	return ms
}

// TestResolveModelRemoteNoPin proves an unpinned resolve builds the rotation
// list from the server's catalog: registered launch rows only, in Order, with
// blocked rows, start rows and a running discovered model left out, and that
// several launchable rows are an ambiguity error that still returns the list.
func TestResolveModelRemoteNoPin(t *testing.T) {
	blocked := wireRow("openrouter/blk", modelapi.ActionBlock, 1)
	blocked.BlockReason = "no route"
	start := wireRow("omlx/start", modelapi.ActionStart, 2)
	start.Location = "local"
	disc := wireRow("omlx/disc", modelapi.ActionLaunch, 0)
	disc.Registered, disc.Discovered = false, true
	f := newFakeModelServer(t,
		wireRow("openrouter/c", modelapi.ActionLaunch, 5), blocked, start, disc,
		wireRow("openrouter/a", modelapi.ActionLaunch, 3))
	ms := openFake(t, f)
	_, list, err := resolveModelRemote(context.Background(), ms, remoteAgent(), "", "", "")
	if err == nil || !strings.Contains(err.Error(), "multiple models match") {
		t.Fatalf("err = %v, want the ambiguity error", err)
	}
	var ids []string
	for _, r := range list {
		ids = append(ids, r.ID)
	}
	if got := strings.Join(ids, ","); got != "openrouter/a,openrouter/c" {
		t.Errorf("rotation list = %s, want openrouter/a,openrouter/c", got)
	}
	// A single launchable row is the answer.
	f.Rows = []modelapi.Row{wireRow("openrouter/a", modelapi.ActionLaunch, 3), blocked, start}
	row, list, err := resolveModelRemote(context.Background(), ms, remoteAgent(), "", "", "")
	if err != nil || row.ID != "openrouter/a" || len(list) != 1 {
		t.Errorf("row=%q list=%d err=%v", row.ID, len(list), err)
	}
	// Nothing launchable is an error.
	f.Rows = []modelapi.Row{blocked, start}
	if _, _, err := resolveModelRemote(context.Background(), ms, remoteAgent(), "", "", ""); err == nil {
		t.Error("no launchable row: want an error")
	}
}

// TestResolveModelRemotePinUnknownShowsServerMessage proves a pin the server
// does not know fails with the server's own message and that the pin travels
// in the query, not the path.
func TestResolveModelRemotePinUnknownShowsServerMessage(t *testing.T) {
	f := newFakeModelServer(t, wireRow("openrouter/a", modelapi.ActionLaunch, 1))
	ms := openFake(t, f)
	_, _, err := resolveModelRemote(context.Background(), ms, remoteAgent(), "", "", "nope/x y")
	if err == nil || err.Error() != `unknown model "nope/x y" on the server` {
		t.Fatalf("err = %v", err)
	}
	last := f.Reqs[len(f.Reqs)-1]
	if !strings.HasPrefix(last, "GET /control/v1/resolve?model=") {
		t.Errorf("last request = %q, want the pin in the query", last)
	}
}

// TestResolveModelRemoteBlockedPinShowsServerReason proves a pin the server
// blocks returns its block_reason as the error, a start row comes back for the
// caller to start, and a launch row comes back as it is.
func TestResolveModelRemoteBlockedPinShowsServerReason(t *testing.T) {
	blocked := wireRow("openrouter/blk", modelapi.ActionBlock, 1)
	blocked.BlockReason = "this model has no route here"
	start := wireRow("omlx/start", modelapi.ActionStart, 2)
	f := newFakeModelServer(t, blocked, start, wireRow("openrouter/a", modelapi.ActionLaunch, 3))
	ms := openFake(t, f)
	_, _, err := resolveModelRemote(context.Background(), ms, remoteAgent(), "", "", "openrouter/blk")
	if err == nil || err.Error() != "this model has no route here" {
		t.Errorf("blocked err = %v", err)
	}
	row, _, err := resolveModelRemote(context.Background(), ms, remoteAgent(), "", "", "omlx/start")
	if err != nil || row.Action != modelapi.ActionStart {
		t.Errorf("start pin: row=%+v err=%v", row, err)
	}
	row, _, err = resolveModelRemote(context.Background(), ms, remoteAgent(), "", "", "openrouter/a")
	if err != nil || row.ID != "openrouter/a" {
		t.Errorf("launch pin: row=%+v err=%v", row, err)
	}
}
