package main

import (
	"context"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/exitcode"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
	"github.com/spf13/cobra"
)

// slotWireRows are the catalog rows every remote slot-launch test serves (the
// rows slot_launch_remote_parity.txt names): two cloud rows, a native row,
// and a local row in each state the launch reads.
func slotWireRows() []modelapi.Row {
	cloud := func(id string, n int) modelapi.Row {
		r := wireRow(id, modelapi.ActionLaunch, n)
		return r
	}
	return []modelapi.Row{
		cloud("openrouter/a", 1),
		cloud("openrouter/b", 2),
		{ID: "claude/native", ProviderID: "claude", ModelName: "native", Native: true, Location: "cloud", Registered: true,
			State: modelapi.StateRunning, Action: modelapi.ActionLaunch, Tags: []string{"code"}, Order: 3},
		localRow("omlx/run", modelapi.StateRunning, modelapi.ActionLaunch, boolp(true)),
		localRow("omlx/unknown", modelapi.StateRunning, modelapi.ActionLaunch, nil),
		localRow("omlx/unrouted", modelapi.StateRunning, modelapi.ActionLaunch, boolp(false)),
		localRow("omlx/idle", modelapi.StateStopped, modelapi.ActionStart, nil),
	}
}

// slotRig is a remote rig whose app holds one claude slot profile "mix" with
// slots, and whose slot build is recorded as "BUILD" in the fake's request
// log (the real build runs after, against the fake claude on PATH).
func slotRig(t *testing.T, slots map[string]string) (*remoteRig, *[]*exec.Cmd) {
	t.Helper()
	r := newRemoteRig(t, slotWireRows()...)
	store := profiles.SlotStore{Profiles: []profiles.SlotProfile{{Name: "mix", Agent: "claude", Default: "sonnet", Slots: slots}}}
	r.a.slotChecks = profiles.ValidateSlots(store, agents.SlotDrivers())
	built := &[]*exec.Cmd{}
	r.a.hooks.buildSlotGateway = func(sl profiles.SlotLaunch, wt string, yolo bool, gw agents.Gateway, extra []string) (*exec.Cmd, error) {
		r.f.mu.Lock()
		r.f.Reqs = append(r.f.Reqs, "BUILD")
		r.f.mu.Unlock()
		cmd, err := agents.BuildSlotLaunchCmdGateway(sl, wt, yolo, gw, extra)
		if cmd != nil {
			*built = append(*built, cmd)
		}
		return cmd, err
	}
	return r, built
}

// slotCalls is the fake's request log normalized as the parity file writes
// it: catalog, register=<ids>, start=<id>, build; pings, job polls,
// heartbeats and the client session's end are left out.
func slotCalls(f *fakeModelServer) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	reg, start := 0, 0
	for _, r := range f.Reqs {
		switch {
		case r == "BUILD":
			out = append(out, "build")
		case strings.HasPrefix(r, "GET /control/v1/catalog"):
			out = append(out, "catalog")
		case r == "POST /control/v1/sessions":
			out = append(out, "register="+strings.Join(f.Registers[reg], ","))
			reg++
		case r == "POST /control/v1/start":
			out = append(out, "start="+f.Starts[start].Model)
			start++
		}
	}
	return out
}

// TestSlotLaunchRemoteRegistersAllDistinctModels pins the body order of a
// remote slot-profile launch: the client session is registered once with
// every distinct slot model (the default slot's first), before the start
// job is posted, and only then is the agent built. A model started before
// its client session holds it could be auto-stopped by the server's idle
// scan before Claude Code reaches it, and a slot model left out of the
// register is one the server may stop under the running agent.
func TestSlotLaunchRemoteRegistersAllDistinctModels(t *testing.T) {
	r, built := slotRig(t, map[string]string{"sonnet": "openrouter/a", "opus": "openrouter/a", "haiku": "omlx/idle"})
	if err := r.launch(t, context.Background(), "profile/mix", "", false); err != nil {
		t.Fatalf("launch: %v", err)
	}
	reqs := r.f.reqs()
	reg, start, build := index(reqs, "POST /control/v1/sessions"), index(reqs, "POST /control/v1/start"), index(reqs, "BUILD")
	if reg < 0 || start < 0 || build < 0 || !(reg < start && start < build) {
		t.Errorf("order register=%d start=%d build=%d, want increasing\n%s", reg, start, build, strings.Join(reqs, "\n"))
	}
	if got := r.f.Registers; len(got) == 0 || !slices.Equal(got[0], []string{"openrouter/a", "omlx/idle"}) {
		t.Errorf("registers = %v, want [openrouter/a omlx/idle] first", got)
	}
	if len(*built) != 1 {
		t.Errorf("agents built = %d, want 1", len(*built))
	}
	if index(reqs, "DELETE /control/v1/sessions/") < build {
		t.Errorf("the client session was not ended after the agent ran\n%s", strings.Join(reqs, "\n"))
	}
}

// TestSlotLaunchRemoteStartsTheLocalSlotOnce proves a profile whose one local
// model is stopped posts exactly one start job, for that model, after one
// catalog read and no resolve (a profile id is not a model the server knows),
// and that a running, routed local model or a cloud-only profile starts
// nothing. The cloud-route write, the proxy restart and the proxy probe of the
// registry path do not exist here: cmd/wt-session's TestMain makes each fail.
func TestSlotLaunchRemoteStartsTheLocalSlotOnce(t *testing.T) {
	for _, tc := range []struct {
		name   string
		slots  map[string]string
		starts []string
	}{
		{"stopped local", map[string]string{"sonnet": "openrouter/a", "haiku": "omlx/idle"}, []string{"omlx/idle"}},
		{"running local", map[string]string{"sonnet": "openrouter/a", "haiku": "omlx/run"}, nil},
		{"cloud only", map[string]string{"sonnet": "openrouter/a", "haiku": "openrouter/b"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := slotRig(t, tc.slots)
			failOnRegistry(t)
			if err := r.launch(t, context.Background(), "profile/mix", "", false); err != nil {
				t.Fatalf("launch: %v", err)
			}
			var got []string
			for _, s := range r.f.Starts {
				got = append(got, s.Model)
			}
			if !slices.Equal(got, tc.starts) {
				t.Errorf("starts = %v, want %v", got, tc.starts)
			}
			if n := r.f.count("GET /control/v1/catalog"); n != 1 {
				t.Errorf("catalog reads = %d, want 1", n)
			}
			if n := r.f.count("GET /control/v1/resolve"); n != 0 {
				t.Errorf("resolve calls = %d, want 0", n)
			}
		})
	}
}

// TestSlotLaunchRemoteNeverSendsNativeToTheGateway is Review Focus 4 for slot
// profiles: a profile that names a native model in any slot is refused before
// anything is registered, started or built, with the reason naming the slot,
// so the gateway's origin and token can never be handed to the native
// subscription. The refusal is exit 1 and says nothing was launched. A
// profile of gateway models only still launches, and its command carries the
// gateway and no native id.
func TestSlotLaunchRemoteNeverSendsNativeToTheGateway(t *testing.T) {
	r, built := slotRig(t, map[string]string{"sonnet": "openrouter/a", "haiku": "claude/native"})
	err := r.launch(t, context.Background(), "profile/mix", "", false)
	if err == nil || !strings.Contains(err.Error(), "slot haiku: claude/native is a native model, which has no gateway route") ||
		!strings.HasSuffix(err.Error(), "nothing was launched") {
		t.Fatalf("err = %v, want the native refusal and the still clause", err)
	}
	if exitcode.Of(err) != 1 {
		t.Errorf("exit = %d, want 1", exitcode.Of(err))
	}
	if len(r.f.Registers) != 0 || len(r.f.Starts) != 0 || len(*built) != 0 {
		t.Errorf("registers=%v starts=%v built=%d, want none", r.f.Registers, r.f.Starts, len(*built))
	}

	r, built = slotRig(t, map[string]string{"sonnet": "openrouter/a", "haiku": "openrouter/b"})
	if err := r.launch(t, context.Background(), "profile/mix", "", false); err != nil {
		t.Fatalf("launch: %v", err)
	}
	if len(*built) != 1 {
		t.Fatalf("agents built = %d, want 1", len(*built))
	}
	all := strings.Join(append(slices.Clone((*built)[0].Args), (*built)[0].Env...), "\n")
	if !strings.Contains(all, r.f.URL) || strings.Contains(all, "claude/native") {
		t.Errorf("gateway launch: want the gateway origin and no native id in\n%s", all)
	}
}

// TestSlotLaunchRemoteRefusesAHiddenProfilePin pins the slot pin's remote
// counterpart (slotPinRemote, over catalog.SlotPinReason): a profile pin
// hidden by -T is refused with the registry path's own reason before any
// server call, and nothing is registered. Without it the pin would go to the
// server's resolve, which knows no profile, and fail with "unknown model".
func TestSlotLaunchRemoteRefusesAHiddenProfilePin(t *testing.T) {
	r, _ := slotRig(t, map[string]string{"sonnet": "openrouter/a"})
	var err error
	captureFile(t, &os.Stdout, func() {
		err = launchFilteredRemote(context.Background(), r.a, "claude", t.TempDir(), false, "code", "", "profile/mix", true, nil, "", false, nil)
	})
	if err == nil || !strings.Contains(err.Error(), `model "profile/mix" is a slot profile, excluded by -T`) {
		t.Fatalf("err = %v, want the -T exclusion", err)
	}
	if len(r.f.Registers) != 0 {
		t.Errorf("registers = %v, want none", r.f.Registers)
	}
}

// TestSlotLaunchRemoteRefusesAPinWhenTheCatalogHoldsTheProfilePrefix is the
// pin's half of the check the picker's test makes: the same refusal text with
// its way out, exit 1, nothing registered, started or built, and the pin never
// sent to the server as a model id.
func TestSlotLaunchRemoteRefusesAPinWhenTheCatalogHoldsTheProfilePrefix(t *testing.T) {
	r, built := slotRig(t, map[string]string{"sonnet": "openrouter/a"})
	r.f.Rows = append(r.f.Rows, wireRow("profile/clash", modelapi.ActionLaunch, 9))
	err := r.launch(t, context.Background(), "profile/mix", "", false)
	for _, want := range []string{"the model server's catalog has a model whose id begins with", "slot profiles are unavailable; rename that provider or model", launchStill} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %v, want %q", err, want)
		}
	}
	if exitcode.Of(err) != 1 {
		t.Errorf("exit = %d, want 1", exitcode.Of(err))
	}
	if len(r.f.Registers) != 0 || len(r.f.Starts) != 0 || len(*built) != 0 || r.f.count("GET /control/v1/resolve") != 0 {
		t.Errorf("registers %v starts %v built %d requests %v, want nothing", r.f.Registers, r.f.Starts, len(*built), r.f.reqs())
	}
}

// TestSlotLaunchRemoteFailedStartEndsTheSessionAndExits2 pins a slot
// profile's start job that failed: the client session ends (its DELETE goes
// out), the error is startStill's (the server's message and "nothing was
// launched"), exit 2, and no command is built.
func TestSlotLaunchRemoteFailedStartEndsTheSessionAndExits2(t *testing.T) {
	r, built := slotRig(t, map[string]string{"sonnet": "openrouter/a", "haiku": "omlx/idle"})
	r.f.Outcome = func(int, modelapi.StartRequest) *modelapi.Problem {
		return &modelapi.Problem{Code: modelapi.CodeFailed, Message: "omlx/idle did not load: out of memory"}
	}
	err := r.launch(t, context.Background(), "profile/mix", "", false)
	if err == nil || !strings.Contains(err.Error(), "out of memory") || !strings.HasSuffix(err.Error(), "nothing was launched") {
		t.Fatalf("err = %v, want the server's message and the still clause", err)
	}
	if exitcode.Of(err) != 2 {
		t.Errorf("exit = %d, want 2", exitcode.Of(err))
	}
	if r.f.count("DELETE /control/v1/sessions/") != 1 {
		t.Errorf("the client session was not ended:\n%s", strings.Join(r.f.reqs(), "\n"))
	}
	if len(*built) != 0 {
		t.Errorf("commands built = %d, want none", len(*built))
	}
}

// TestSlotLaunchRemoteRegisterFailureIsANoteAndLaunches is the slot launch's
// counterpart of the single-model one: a register the server refuses is one
// `wt: note:` line and the profile still launches.
func TestSlotLaunchRemoteRegisterFailureIsANoteAndLaunches(t *testing.T) {
	r, built := slotRig(t, map[string]string{"sonnet": "openrouter/a", "haiku": "openrouter/b"})
	r.f.RegisterStatus = 500
	if err := r.launch(t, context.Background(), "profile/mix", "", false); err != nil {
		t.Fatalf("launch: %v", err)
	}
	if len(*built) != 1 {
		t.Errorf("commands built = %d, want 1", len(*built))
	}
	errOut := r.stderr.String()
	if n := strings.Count(errOut, "did not register this client session"); n != 1 {
		t.Errorf("stderr has %d register notes, want one:\n%s", n, errOut)
	}
}

// TestSlotFlowParity runs every scenario of the shared table
// (internal/profiles/testdata/slot_launch_remote_parity.txt) through the
// -M profile/<name> pin on the model server (launchSlotProfileRemote) and
// checks the server calls it makes, in order, against the table. The picker's
// TestSlotFlowParity (internal/tui) runs the same table through Enter on a
// profile row, so the two client paths are held to one list: a register moved
// after the start, a second catalog read or a start of a routed model on one
// path would otherwise pass that path's own test.
func TestSlotFlowParity(t *testing.T) {
	for _, sc := range readRemoteSlotParity(t) {
		t.Run(sc.name, func(t *testing.T) {
			r, _ := slotRig(t, sc.slots)
			if err := r.launch(t, context.Background(), "profile/mix", "", false); err != nil {
				t.Fatalf("launch: %v", err)
			}
			if got := slotCalls(r.f); !slices.Equal(got, sc.calls) {
				t.Errorf("calls = %v\nwant    %v", got, sc.calls)
			}
		})
	}
}

// remoteSlotScenario is one block of slot_launch_remote_parity.txt.
type remoteSlotScenario struct {
	name  string
	slots map[string]string
	calls []string
}

// readRemoteSlotParity parses the shared table: blocks of "key: value" lines
// separated by blank lines, "#" lines ignored. internal/tui keeps a copy of
// this parser; the file, not the parser, is what is shared.
func readRemoteSlotParity(t *testing.T) []remoteSlotScenario {
	t.Helper()
	const path = "../../internal/profiles/testdata/slot_launch_remote_parity.txt"
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var out []remoteSlotScenario
	var cur *remoteSlotScenario
	flush := func() {
		if cur != nil {
			out = append(out, *cur)
			cur = nil
		}
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			continue
		}
		if line == "" {
			flush()
			continue
		}
		key, val, ok := strings.Cut(line, ": ")
		if !ok {
			t.Fatalf("%s: malformed line %q", path, line)
		}
		if cur == nil {
			cur = &remoteSlotScenario{slots: map[string]string{}}
		}
		switch key {
		case "scenario":
			cur.name = val
		case "slots":
			for _, pair := range strings.Fields(val) {
				s, m, _ := strings.Cut(pair, "=")
				cur.slots[s] = m
			}
		case "calls":
			cur.calls = strings.Fields(val)
		default:
			t.Fatalf("%s: unknown key %q", path, key)
		}
	}
	flush()
	if len(out) == 0 {
		t.Fatalf("%s: no scenarios", path)
	}
	return out
}

// TestSlotLaunchRemotePrintsSlotNotes is carry-forward (b) of the remote
// launch: what is wrong with slot-profiles.toml (an unknown key, a profile
// that is not usable) is said once on a client session's launch too, before
// the agent runs, as on the registry path. The registry's "profile/" holder
// is not among the notes: a client session never reads the registry (here a
// registry config holding the prefix is in place and must not be named).
func TestSlotLaunchRemotePrintsSlotNotes(t *testing.T) {
	r := newRemoteRig(t, wireRow("openrouter/a", modelapi.ActionLaunch, 1))
	r.a.slotUnknown = []string{"stray"}
	hostCfgOf(r.a).Providers = append(hostCfgOf(r.a).Providers, config.Provider{ID: profiles.SlotReservedProvider, Location: config.LocationCloud})
	r.a.slotChecks = []profiles.SlotCheck{slotFixtureCheck(0, "ok", "sonnet", map[string]string{"sonnet": "openrouter/a"})}
	cmd := &cobra.Command{}
	cmd.Flags().String("model", "", "")
	cmd.Flags().Bool("yolo", false, "")
	var err error
	stderr := captureFile(t, &os.Stderr, func() {
		captureFile(t, &os.Stdout, func() {
			err = runLaunchPath(cmd, r.a, "claude", "", "", "", nil, t.TempDir(), "")
		})
	})
	if err != nil {
		t.Fatalf("runLaunchPath: %v", err)
	}
	if n := strings.Count(stderr, `slot-profiles.toml: unknown key "stray"`); n != 1 {
		t.Errorf("unknown-key note printed %d times, want once:\n%s", n, stderr)
	}
	if strings.Contains(stderr, "the registry has") {
		t.Errorf("a client session named the registry's holder:\n%s", stderr)
	}
	if len(*r.built) != 1 {
		t.Errorf("agents built = %d, want 1", len(*r.built))
	}
}
