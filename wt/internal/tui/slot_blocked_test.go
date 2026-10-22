package tui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)

// Blocked-profile gaps of the picker (#412 A4): the remembered failed proxy
// restart through the real lifecycle.ProbeProxy, the blocked text of rows
// that static validation refuses, and a step-1 re-resolve that turns a
// launch row into a blocked one.

// fakeProxy is a stand-in LiteLLM proxy that answers 200 on every path, the
// liveness endpoint included. A test points [litellm] url at it so that a
// probe or readiness wait that does run reaches this server, never whatever
// answers at the developer's proxy address. cmd/wt's blocked-profile test
// keeps a copy of this helper and of failLastProxyRestart below it; the
// launch path each package tests, not the helpers, differ.
func fakeProxy(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	t.Cleanup(srv.Close)
	return srv.URL
}

// failLastProxyRestart makes internal/lifecycle remember a failed proxy
// restart the way a real one is remembered: lifecycle.RestartProxy runs the
// restart command WT_LITELLM_RESTART_CMD names ("false", which exits 1) and
// records the failure. It returns that restart's error. cfg's [litellm] url
// must be a fakeProxy registered before this call: on cleanup, which runs
// before the server closes, a restart that succeeds (the command "true", then
// the readiness wait against that server) clears the remembered failure, so
// it does not leak into later tests of this process.
func failLastProxyRestart(t *testing.T, cfg *config.Config) error {
	t.Helper()
	t.Setenv("WT_LITELLM_RESTART_CMD", "false")
	t.Cleanup(func() {
		// Runs before t.Setenv's own restore, so the variable is set here.
		_ = os.Setenv("WT_LITELLM_RESTART_CMD", "true")
		if err := lifecycle.RestartProxy(context.Background(), cfg); err != nil {
			t.Errorf("clearing the remembered restart failure: %v", err)
		}
		if err := lifecycle.ProbeProxy(context.Background(), cfg); err != nil {
			t.Errorf("the remembered restart failure was not cleared: %v", err)
		}
	})
	return lifecycle.RestartProxy(context.Background(), cfg)
}

// TestPickerProfileRefusedAfterARememberedFailedRestart verifies, with the
// real lifecycle.ProbeProxy in place of the probe seam, that a proxy restart
// which failed earlier in the same wt process refuses a later profile launch
// from the picker whose own routes were all in place: the picker comes back
// with the refusal as its status, no agent runs and nothing is recorded. The
// proxy answers its liveness probe here, as the old proxy does after a failed
// restart; only the remembered failure can say that the routes written since
// are not live. Without it Claude Code would start against a proxy that
// answers "Invalid model name" for its slots.
func TestPickerProfileRefusedAfterARememberedFailedRestart(t *testing.T) {
	m := slotFlowFixture(t, idleOmlxSnapshot(), cloudProfile())
	m.cfg.SetLitellmForTest(config.LitellmState{Enabled: true, URL: fakeProxy(t), APIKey: "sk-test"})
	if err := failLastProxyRestart(t, m.cfg); err == nil {
		t.Fatal("fixture: the restart did not fail, so the test proves nothing")
	}
	stub := stubSlotSeams(t)
	probeProxyNow = lifecycle.ProbeProxy // stubSlotSeams' cleanup restores the stub
	stubSlotApplier(t, func(*exec.Cmd, string, catalog.Row) (func() error, error) {
		t.Error("the applier ran for a launch that was refused")
		return nil, nil
	})

	preparing, cmd := enterProfileRow(t, m, "profile/cloud")
	back, after := updateMsg(preparing, slotStageFrom(t, cmd)())

	const prefix = "the last LiteLLM proxy restart failed, so routes written since may not be live: "
	if !strings.HasPrefix(back.status, prefix) || !strings.Contains(back.status, "restart it manually: false") {
		t.Errorf("status = %q, want it to begin %q and name the failed restart command", back.status, prefix)
	}
	if back.phase != phaseModel || back.slot != nil {
		t.Fatalf("phase = %v slot = %v, want the picker with the flow ended", back.phase, back.slot)
	}
	if got, want := stub.got(), "ensure:openrouter/a,openrouter/b:owed=false,wait"; got != want {
		t.Errorf("events = %q, want %q (no restart owed, the wait, then the refusing probe)", got, want)
	}
	if after == nil {
		t.Fatal("no command: the picker must re-probe after a refused launch")
	}
	if _, ok := after().(tableRefreshedMsg); !ok {
		t.Error("the command after the refusal is not the table refresh: an agent may run")
	}
	assertNothingRecorded(t)
}

// TestStaticallyBlockedProfileRowsCarryTheCatalogReason verifies the picker
// row of a profile that static validation refuses, run through the real
// pipeline (profiles.ValidateSlots with the registered agent drivers, then
// the table build): two profiles sharing a name, an opusplan default with the
// opus slot unfilled, and a config_content.env that is not a table. Each row
// is blocked (not a start row) with exactly the reason the catalog's row
// gives, which is what Enter shows as the status. A row drawn as launchable
// would write routes for a profile that can never run; a different text
// would leave the user hunting for the wrong mistake in slot-profiles.toml.
func TestStaticallyBlockedProfileRowsCarryTheCatalogReason(t *testing.T) {
	cloud := map[string]string{"opus": "openrouter/a"}
	store := profiles.SlotStore{Profiles: []profiles.SlotProfile{
		{Name: "twin", Agent: "claude", Default: "opus", Slots: cloud},
		{Name: "twin", Agent: "claude", Default: "opus", Slots: map[string]string{"opus": "openrouter/b"}},
		{Name: "plan", Agent: "claude", Default: "opusplan", Slots: map[string]string{"sonnet": "openrouter/a"}},
		{Name: "string-env", Agent: "claude", Default: "opus", Slots: cloud, ConfigContent: map[string]any{"env": "x"}},
	}}
	checks := profiles.ValidateSlots(store, agents.SlotDrivers())
	cfg := slotTestConfig()
	snap := runningOmlxSnapshot()
	tbl := slotTable(t, cfg, snap, checks)
	models, err := cfg.EligibleModels("claude", "", "")
	if err != nil {
		t.Fatalf("EligibleModels: %v", err)
	}
	rows := catalog.Build(catalog.Input{Config: cfg, Agent: "claude", Models: models, Inventory: &snap, SlotProfiles: checks})

	for _, c := range []struct{ id, want string }{
		{"profile/twin", "duplicate profile name"},
		{"profile/plan", `default "opusplan" needs the opus slot filled`},
		{"profile/string-env", "config_content.env must be a table"},
	} {
		row, ok := catalog.Find(rows, c.id)
		if !ok || row.Action() != catalog.ActionBlock || row.BlockReason() != c.want {
			t.Errorf("%s: catalog row found = %t action = %v reason = %q, want blocked with %q", c.id, ok, row.Action(), row.BlockReason(), c.want)
		}
		it := tableItem(t, tbl, c.id)
		if it.blocked != c.want || it.blocked != row.BlockReason() || it.start {
			t.Errorf("%s: blocked = %q start = %t, want blocked with the catalog's %q and not a start row", c.id, it.blocked, it.start, c.want)
		}
	}
	twins := 0
	for _, it := range tbl.items {
		if it.model.ID == "profile/twin" {
			twins++
		}
	}
	if twins != 1 {
		t.Errorf("%d rows for profile/twin, want the duplicates collapsed into one", twins)
	}
}

// TestPickerStepOneReResolvesAgainstAFreshProbe verifies step 1 of the
// picker's profile launch: the highlighted row (a launch row, its local model
// running when the table was built) is judged again against a fresh probe,
// and when that probe no longer has the local model the launch is refused
// with the new reason as the picker's status, before any route is written or
// probed, with no agent run and nothing recorded. Judged on the table's probe
// alone, a model stopped in another terminal while the picker was open would
// leave Claude Code with a dead slot.
func TestPickerStepOneReResolvesAgainstAFreshProbe(t *testing.T) {
	m := slotFlowFixture(t, runningOmlxSnapshot(), mixedProfile())
	it := m.models.Items()[indexOfID(m, "profile/mixed")].(*modelItem)
	if it.blocked != "" || it.start {
		t.Fatalf("fixture: profile/mixed blocked = %q start = %t, want a launch row", it.blocked, it.start)
	}
	stub := stubSlotSeams(t)
	stubSlotApplier(t, func(*exec.Cmd, string, catalog.Row) (func() error, error) {
		t.Error("the applier ran for a launch the fresh probe blocked")
		return nil, nil
	})
	// The probe made after the table was built: the local model is gone.
	stubInventory(t, localmodels.Snapshot{Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK}})

	preparing, cmd := enterProfileRow(t, m, "profile/mixed")
	back, after := updateMsg(preparing, slotStageFrom(t, cmd)())

	if want := "slot haiku: omlx/qwen3.8 is not on disk or running"; back.status != want {
		t.Errorf("status = %q, want %q", back.status, want)
	}
	if back.phase != phaseModel || back.slot != nil {
		t.Fatalf("phase = %v slot = %v, want the picker with the flow ended", back.phase, back.slot)
	}
	if got := stub.got(); got != "" {
		t.Errorf("events = %q, want none: a blocked profile routes and probes nothing", got)
	}
	if after == nil {
		t.Fatal("no command: the picker must re-probe after a refused launch")
	}
	if _, ok := after().(tableRefreshedMsg); !ok {
		t.Error("the command after the refusal is not the table refresh: an agent may run")
	}
	assertNothingRecorded(t)
}
