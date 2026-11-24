package tui

import (
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)

// Blocked-profile gaps of the picker (#412 A4): the blocked text of rows
// that static validation refuses, and a step-1 re-resolve that turns a
// launch row into a blocked one. (The remembered failed proxy restart,
// lifecycle.ProbeProxy, was deleted with the launch-time route check, #691.)

// TestStaticallyBlockedProfileRowsCarryTheCatalogReason verifies the picker
// row of a profile that static validation refuses, run through the real
// pipeline (profiles.ValidateSlots with the registered agent drivers, then
// the picker's catalog load): two profiles sharing a name, an opusplan default with the
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
	m := slotPicker(t, slotTestConfig(), checks, 80, 24)
	byID := catalog.WireByID(catalogFor(t, slotTestConfig()).Rows)

	for _, c := range []struct{ id, want string }{
		{"profile/twin", "duplicate profile name"},
		{"profile/plan", `default "opusplan" needs the opus slot filled`},
		{"profile/string-env", "config_content.env must be a table"},
	} {
		var check profiles.SlotCheck
		for _, ch := range checks {
			if ch.Profile.ID() == c.id {
				check = ch
				break
			}
		}
		row := catalog.ResolveSlotProfileWire(byID, check)
		if row.Action() != catalog.ActionBlock || row.BlockReason() != c.want {
			t.Errorf("%s: catalog row action = %v reason = %q, want blocked with %q", c.id, row.Action(), row.BlockReason(), c.want)
		}
		it := pickerItem(t, m, c.id)
		if it.blocked != c.want || it.blocked != row.BlockReason() || it.start {
			t.Errorf("%s: blocked = %q start = %t, want blocked with the catalog's %q and not a start row", c.id, it.blocked, it.start, c.want)
		}
	}
	twins := 0
	for _, id := range itemIDs(m) {
		if id == "profile/twin" {
			twins++
		}
	}
	if twins != 1 {
		t.Errorf("%d rows for profile/twin, want the duplicates collapsed into one", twins)
	}
}

// pickerItem is the picker's row for id.
func pickerItem(t *testing.T, m model, id string) *modelItem {
	t.Helper()
	for _, it := range m.models.Items() {
		if mi := it.(*modelItem); mi.model.ID == id {
			return mi
		}
	}
	t.Fatalf("the picker has no row %q (rows %v)", id, itemIDs(m))
	return nil
}
