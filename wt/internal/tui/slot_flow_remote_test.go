package tui

import (
	"context"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)

// slotRemoteCatalog serves the rows slot_launch_remote_parity.txt names.
func slotRemoteCatalog() modelapi.Catalog {
	routed, unrouted := true, false
	local := func(id, state, action string, r *bool) modelapi.Row {
		return wireRow(id, "q", func(row *modelapi.Row) {
			row.Location, row.State, row.Action, row.Routed, row.Order = "local", state, action, r, 5
		})
	}
	return modelapi.Catalog{Schema: 1, Generated: "2026-11-16T12:00:00Z", Rows: []modelapi.Row{
		wireRow("openrouter/a", "a", func(r *modelapi.Row) { r.Order = 1 }),
		wireRow("openrouter/b", "b", func(r *modelapi.Row) { r.Order = 2 }),
		wireRow("claude/native", "claude", func(r *modelapi.Row) { r.Native, r.GatewayID, r.Order = true, "", 3 }),
		local("omlx/run", modelapi.StateRunning, modelapi.ActionLaunch, &routed),
		local("omlx/unknown", modelapi.StateRunning, modelapi.ActionLaunch, nil),
		local("omlx/unrouted", modelapi.StateRunning, modelapi.ActionLaunch, &unrouted),
		local("omlx/idle", modelapi.StateStopped, modelapi.ActionStart, nil),
	}}
}

// slotRemoteTable is the API picker's table with one claude slot profile
// "mix" of slots, loaded from a fake whose starts succeed. From here on the
// fake logs its catalog reads, and the slot build is recorded as "BUILD" and
// returns a harmless command (the fake's gateway is empty).
func slotRemoteTable(t *testing.T, slots map[string]string) (model, *fakeModels) {
	t.Helper()
	f := &fakeModels{cat: slotRemoteCatalog()}
	f.startFn = func(context.Context, int, modelapi.StartRequest, modelclient.Progress) (modelapi.JobStatus, error) {
		return modelapi.JobStatus{State: modelapi.JobSucceeded}, nil
	}
	m := remoteModel(t, f, 120, 30)
	store := profiles.SlotStore{Profiles: []profiles.SlotProfile{{Name: "mix", Agent: "claude", Default: "sonnet", Slots: slots}}}
	m.slotChecks = profiles.ValidateSlots(store, agents.SlotDrivers())
	m.selectedPath = t.TempDir()
	m, cmd := m.enterModelPhaseForAgent()
	m = settle(t, m, cmd)
	if m.phase != phaseModel {
		t.Fatalf("phase = %v, status %q: want the table", m.phase, m.status)
	}
	f.mu.Lock()
	f.logCatalog = true
	f.mu.Unlock()
	old := buildSlotGatewayLaunch
	buildSlotGatewayLaunch = func(profiles.SlotLaunch, string, bool, agents.Gateway, []string) (*exec.Cmd, error) {
		f.record("BUILD")
		return exec.Command("true"), nil
	}
	t.Cleanup(func() { buildSlotGatewayLaunch = old })
	return m, f
}

// driveSlotLaunch presses Enter on the profile row and runs the flow to the
// agent's build: the prep command, then any start job to its end.
func driveSlotLaunch(t *testing.T, m model) model {
	t.Helper()
	m, cmd := enterRemote(t, m, "profile/mix")
	if cmd == nil {
		t.Fatalf("Enter on the profile row returned no command (status %q)", m.status)
	}
	m, _ = updateMsg(m, cmd())
	for m.phase == phaseStarting {
		m, _ = updateMsg(m, recvStart(t, m))
	}
	return m
}

// tuiSlotCalls is the fake's log normalized as the parity file writes it.
func tuiSlotCalls(f *fakeModels) []string {
	var out []string
	for _, l := range f.history() {
		switch {
		case l == "catalog":
			out = append(out, "catalog")
		case l == "BUILD":
			out = append(out, "build")
		case strings.HasPrefix(l, "register "):
			out = append(out, "register="+strings.TrimPrefix(l, "register "))
		case strings.HasPrefix(l, "add "):
			out = append(out, "register="+strings.TrimPrefix(l, "add "))
		case strings.HasPrefix(l, "start "):
			out = append(out, "start="+strings.TrimPrefix(l, "start "))
		}
	}
	return out
}

// TestSlotFlowParity runs every scenario of the shared table
// (internal/profiles/testdata/slot_launch_remote_parity.txt) through the
// picker on the model server: Enter on a profile row, the prep (one catalog
// read, the client session's register with every slot model), the start
// flow when the local slot needs one, and the build against the gateway.
// cmd/wt's TestSlotFlowParity runs the same table through the -M
// profile/<name> pin, so the two client paths make the same server calls in
// the same order; a register after the start or a second catalog read on one
// path would otherwise pass that path's own test.
func TestSlotFlowParity(t *testing.T) {
	for _, sc := range readRemoteSlotParity(t) {
		t.Run(sc.name, func(t *testing.T) {
			m, f := slotRemoteTable(t, sc.slots)
			m = driveSlotLaunch(t, m)
			if got := tuiSlotCalls(f); !slices.Equal(got, sc.calls) {
				t.Errorf("calls = %v\nwant    %v\n(status %q)", got, sc.calls, m.status)
			}
		})
	}
}

// TestSlotFlowRemoteShowsProfileRows proves the API picker lists the agent's
// slot profiles as rows resolved from the server's catalog: a launchable one
// with its location, a start one, and one naming a native model blocked with
// the gateway reason, so Enter on it says why and calls nothing.
func TestSlotFlowRemoteShowsProfileRows(t *testing.T) {
	m, f := slotRemoteTable(t, map[string]string{"sonnet": "openrouter/a", "haiku": "claude/native"})
	i := indexOfID(m, "profile/mix")
	if i < 0 {
		t.Fatalf("no profile row in %v", itemIDs(m))
	}
	it := m.models.Items()[i].(*modelItem)
	if !strings.Contains(it.blocked, "slot haiku: claude/native is a native model, which has no gateway route") {
		t.Errorf("blocked = %q, want the native reason", it.blocked)
	}
	m, _ = enterRemote(t, m, "profile/mix")
	if len(f.history()) != 0 {
		t.Errorf("Enter on a blocked profile called %v", f.history())
	}
	if !strings.Contains(m.status, "native model") {
		t.Errorf("status = %q, want the reason", m.status)
	}
}

// remoteSlotScenario is one block of slot_launch_remote_parity.txt.
type remoteSlotScenario struct {
	name  string
	slots map[string]string
	calls []string
}

// readRemoteSlotParity parses the shared table: blocks of "key: value" lines
// separated by blank lines, "#" lines ignored. cmd/wt keeps a copy of this
// parser; the file, not the parser, is what is shared.
func readRemoteSlotParity(t *testing.T) []remoteSlotScenario {
	t.Helper()
	const path = "../profiles/testdata/slot_launch_remote_parity.txt"
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
				s, mdl, _ := strings.Cut(pair, "=")
				cur.slots[s] = mdl
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

// TestSlotFlowRemotePinBeginsTheProfileLaunch is carry-forward (a) on the
// picker: a -M profile/<name> pin on the API path never goes to the server's
// resolve (it knows no profile id); the table's profile row is chosen and its
// launch begins, as the registry path's pin does. A pin of a profile hidden by
// -T goes back to the agent screen with the registry path's reason.
func TestSlotFlowRemotePinBeginsTheProfileLaunch(t *testing.T) {
	f := &fakeModels{cat: slotRemoteCatalog()}
	m := remoteModel(t, f, 120, 30)
	store := profiles.SlotStore{Profiles: []profiles.SlotProfile{{Name: "mix", Agent: "claude", Default: "sonnet", Slots: map[string]string{"sonnet": "openrouter/a"}}}}
	m.slotChecks = profiles.ValidateSlots(store, agents.SlotDrivers())
	m.selectedPath = t.TempDir()
	m.pinnedModel = "profile/mix"
	m, cmd := m.enterModelPhaseForAgent()
	m = settle(t, m, cmd)
	if m.phase != phaseLaunching || m.rslot == nil {
		t.Fatalf("phase = %v, status %q: want the profile's launch prep", m.phase, m.status)
	}
	if len(f.resolved) != 0 {
		t.Errorf("resolve called for %v, want none", f.resolved)
	}

	g := &fakeModels{cat: slotRemoteCatalog()}
	h := remoteModel(t, g, 120, 30)
	h.slotChecks = m.slotChecks
	h.pinnedModel, h.activeTags = "profile/mix", "code"
	h, cmd = h.enterModelPhaseForAgent()
	h = settle(t, h, cmd)
	if h.phase != phaseAgent || !strings.Contains(h.status, `model "profile/mix" is a slot profile, excluded by -T`) {
		t.Errorf("phase = %v, status %q: want the -T exclusion on the agent screen", h.phase, h.status)
	}
}
