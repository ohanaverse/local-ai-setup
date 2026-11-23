package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
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

// TestResolveModelRemotePinMustBeEligible proves a pin is checked against the
// agent, -T and -F like today's resolveModel: another agent's native row, a
// pin outside -T and a pin outside -F are refused with the same wording, and a
// refused start row is never returned; an eligible pin still resolves.
func TestResolveModelRemotePinMustBeEligible(t *testing.T) {
	native := wireRow("copilot/gpt", modelapi.ActionLaunch, 1)
	native.ProviderID, native.Native = "copilot", true
	startNative := wireRow("copilot/start", modelapi.ActionStart, 2)
	startNative.ProviderID, startNative.Native = "copilot", true
	untagged := wireRow("openrouter/u", modelapi.ActionStart, 3)
	untagged.Tags = []string{"chat"}
	ok := wireRow("openrouter/a", modelapi.ActionLaunch, 4)
	f := newFakeModelServer(t, native, startNative, untagged, ok)
	ms := openFake(t, f)
	ctx := context.Background()
	for _, tc := range []struct{ name, pin, tags, family string }{
		{"other agent native", "copilot/gpt", "", ""},
		{"other agent native start row", "copilot/start", "", ""},
		{"outside -T", "openrouter/u", "code", ""},
		{"outside -F", "openrouter/a", "", "zzz"},
	} {
		row, _, err := resolveModelRemote(ctx, ms, remoteAgent(), tc.tags, tc.family, tc.pin)
		want := fmt.Sprintf("model %q is not in the eligible list for agent %q", tc.pin, "claude")
		if err == nil || err.Error() != want || row.ID != "" {
			t.Errorf("%s: row=%q err=%v, want %q and no row", tc.name, row.ID, err, want)
		}
	}
	row, _, err := resolveModelRemote(ctx, ms, remoteAgent(), "code", "f", "openrouter/a")
	if err != nil || row.ID != "openrouter/a" {
		t.Errorf("eligible pin: row=%q err=%v", row.ID, err)
	}
}

// TestResolveModelRemoteEmptyMessages proves the no-pin errors say which of
// the two things is wrong: nothing eligible (a -T typo) or eligible rows none
// of which can be launched as they are.
func TestResolveModelRemoteEmptyMessages(t *testing.T) {
	start := wireRow("omlx/start", modelapi.ActionStart, 1)
	f := newFakeModelServer(t, start)
	ms := openFake(t, f)
	ctx := context.Background()
	_, _, err := resolveModelRemote(ctx, ms, remoteAgent(), "typo", "", "")
	if want := `no models match agent "claude" with tags "typo" and family ""`; err == nil || err.Error() != want {
		t.Errorf("nothing eligible: err = %v, want %q", err, want)
	}
	_, _, err = resolveModelRemote(ctx, ms, remoteAgent(), "", "", "")
	if err == nil || !strings.Contains(err.Error(), "wt -M <id>") {
		t.Errorf("none launchable: err = %v, want the start-one text", err)
	}
}

// TestResolveModelRemoteSchemaSkewSurfacesAtCatalog proves a catalog schema
// newer than this wt reads is the unpinned resolve's error (openModels pings
// and never reads the schema), typed, and without a caller's still clause.
func TestResolveModelRemoteSchemaSkewSurfacesAtCatalog(t *testing.T) {
	f := newFakeModelServer(t, wireRow("openrouter/a", modelapi.ActionLaunch, 1))
	ms := openFake(t, f)
	f.Schema = modelapi.SchemaVersion + 1
	_, _, err := resolveModelRemote(context.Background(), ms, remoteAgent(), "", "", "")
	var se *modelclient.SchemaError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v, want *SchemaError", err)
	}
	if strings.Contains(err.Error(), "nothing was") {
		t.Errorf("err carries a still clause: %v", err)
	}
}

// aliasWire is an alias row over targets, launchable as a pi/claude row.
func aliasWire(id string, n int, targets ...modelapi.AliasTarget) modelapi.Row {
	return modelapi.Row{
		ID: id, GatewayID: id, ProviderID: "alias", ModelName: strings.TrimPrefix(id, "alias/"), Family: "alias",
		Kind: modelapi.KindAlias, Location: "cloud", Registered: true, State: modelapi.StateRunning,
		Action: modelapi.ActionLaunch, Order: n, Targets: targets,
	}
}

// TestResolveModelRemoteNoPinNeverPicksAnAlias pins decision 14 on the no-pin
// launch: an alias row is listed (the picker shows it) but never counts as a
// launchable choice or a rotation entry. One model plus an alias over it
// launches the model directly (an alias counted would make it "multiple
// models match" and a script would fail with exit 1); a lone alias is not
// launched unasked but reads as the zero-launchable error; and `-M alias/x`
// still resolves the alias.
func TestResolveModelRemoteNoPinNeverPicksAnAlias(t *testing.T) {
	target := modelapi.AliasTarget{ID: "openrouter/a", ProviderID: "openrouter", Location: "cloud", State: modelapi.StateRunning, Order: 1}
	alias := aliasWire("alias/x", 0, target)
	f := newFakeModelServer(t, wireRow("openrouter/a", modelapi.ActionLaunch, 1), alias)
	ms := openFake(t, f)
	ctx := context.Background()
	row, list, err := resolveModelRemote(ctx, ms, remoteAgent(), "", "", "")
	if err != nil || row.ID != "openrouter/a" || len(list) != 1 || list[0].ID != "openrouter/a" {
		t.Errorf("model + alias: row=%q list=%v err=%v, want openrouter/a launched directly", row.ID, list, err)
	}
	f.Rows = []modelapi.Row{alias}
	row, list, err = resolveModelRemote(ctx, ms, remoteAgent(), "", "", "")
	if err == nil || !strings.Contains(err.Error(), "no cloud or running local model") || row.ID != "" || len(list) != 0 {
		t.Errorf("lone alias: row=%q list=%d err=%v, want the zero-launchable error", row.ID, len(list), err)
	}
	row, _, err = resolveModelRemote(ctx, ms, remoteAgent(), "", "", "alias/x")
	if err != nil || row.ID != "alias/x" {
		t.Errorf("-M alias/x: row=%q err=%v, want the alias", row.ID, err)
	}
}

// subWire is a claude-subscription wire row with the given action.
func subWire(id, action string, n int) modelapi.Row {
	r := wireRow(id, action, n)
	r.ProviderID, r.Family = config.SubscriptionProvider, "claude"
	return r
}

// TestResolveModelRemoteNoPinNeverPicksASubscriptionRow pins that subscription
// rows are pin-only (#597): with the host's passthrough on, a subscription row
// (or an alias over one) is neither the one launchable row nor a rotation
// entry, so a launch with no -M never spends the owner's subscription unasked.
// When subscription rows were the only launchable ones the error says a Claude
// subscription model must be named with -M, not that nothing is running; with
// another launchable row beside it, that row is launched directly.
func TestResolveModelRemoteNoPinNeverPicksASubscriptionRow(t *testing.T) {
	sub := subWire("claude-haiku-5-5", modelapi.ActionLaunch, 0)
	group := aliasWire("alias/haiku", 2, modelapi.AliasTarget{ID: "claude-haiku-5-5", ProviderID: config.SubscriptionProvider, Location: "cloud", Order: 1})
	f := newFakeModelServer(t, sub, group)
	ms := openFake(t, f)
	ctx := context.Background()
	row, list, err := resolveModelRemote(ctx, ms, remoteAgent(), "", "", "")
	if err == nil || row.ID != "" || len(list) != 0 {
		t.Fatalf("only a subscription row: row=%q list=%d err=%v, want an error and no launch", row.ID, len(list), err)
	}
	if !strings.Contains(err.Error(), "-M") || !strings.Contains(err.Error(), "Claude subscription") || strings.Contains(err.Error(), "no cloud or running local model") {
		t.Errorf("only a subscription row: err = %q, want it to say a Claude subscription model is named with -M", err)
	}
	f.Rows = []modelapi.Row{sub, group, wireRow("openrouter/a", modelapi.ActionLaunch, 1)}
	row, list, err = resolveModelRemote(ctx, ms, remoteAgent(), "", "", "")
	if err != nil || row.ID != "openrouter/a" || len(list) != 1 || list[0].ID != "openrouter/a" {
		t.Errorf("mixed: row=%q list=%v err=%v, want openrouter/a launched directly", row.ID, list, err)
	}
	row, _, err = resolveModelRemote(ctx, ms, remoteAgent(), "", "", "claude-haiku-5-5")
	if err != nil || row.ID != "claude-haiku-5-5" {
		t.Errorf("-M claude-haiku-5-5: row=%q err=%v, want the subscription row", row.ID, err)
	}
}

// TestResolveModelRemotePinIneligibleBeforeBlock pins the CLI pin order the
// TUI already has: eligibility is judged before the block reason, so
// `wt -A pi -M claude-haiku-5-5` with the host's passthrough off says pi
// cannot use the model, instead of telling the user to turn on a host key that
// would not help pi.
func TestResolveModelRemotePinIneligibleBeforeBlock(t *testing.T) {
	sub := subWire("claude-haiku-5-5", modelapi.ActionBlock, 0)
	sub.State, sub.BlockReason = modelapi.StateBlocked, "subscription passthrough is off on the model server ([gateway] subscription_passthrough in ~/.config/wt-model/config.toml)"
	f := newFakeModelServer(t, sub)
	ms := openFake(t, f)
	pi := config.Agent{Name: "pi", AllProviders: true}
	_, _, err := resolveModelRemote(context.Background(), ms, pi, "", "", "claude-haiku-5-5")
	if err == nil || !strings.Contains(err.Error(), "not in the eligible list for agent \"pi\"") {
		t.Errorf("pi pin of a blocked subscription row: err = %v, want the not-eligible error", err)
	}
	// claude is eligible, so it gets the block reason naming the key.
	_, _, err = resolveModelRemote(context.Background(), ms, remoteAgent(), "", "", "claude-haiku-5-5")
	if err == nil || !strings.Contains(err.Error(), "[gateway] subscription_passthrough") {
		t.Errorf("claude pin of a blocked subscription row: err = %v, want the block reason", err)
	}
}
