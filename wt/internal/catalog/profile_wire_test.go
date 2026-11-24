// Tests for slot-profile rows resolved from the model server's catalog
// (ResolveSlotProfileWire, SlotPrefixHolderWire): the registry-free rules a
// client session applies to a profile, with the server's rows in place of the
// registry, the inventory probe and the [litellm] table.
package catalog

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
)

// wireSlotRows are the catalog rows the wire rules are tested against: two
// cloud rows, a native row, a server-blocked row, and local rows in each state
// the rules read.
func wireSlotRows() map[string]modelapi.Row {
	routed := true
	unrouted := false
	rows := []modelapi.Row{
		{ID: "openrouter/a", GatewayID: "openrouter/a", ProviderID: "openrouter", ModelName: "a", Location: "cloud", State: modelapi.StateRunning, Action: modelapi.ActionLaunch},
		{ID: "openrouter/b", GatewayID: "gw-b", ProviderID: "openrouter", ModelName: "b", Location: "cloud", State: modelapi.StateRunning, Action: modelapi.ActionLaunch},
		{ID: "claude/native", ProviderID: "claude", ModelName: "native", Location: "cloud", Native: true, State: modelapi.StateRunning, Action: modelapi.ActionLaunch},
		{ID: "nopolicy/x", ProviderID: "nopolicy", ModelName: "x", Location: "cloud", State: modelapi.StateBlocked, Action: modelapi.ActionBlock, BlockReason: "nopolicy/x has no LiteLLM mapping on the model server"},
		{ID: "omlx/run", GatewayID: "omlx/run", ProviderID: "omlx", ModelName: "run", Location: "local", State: modelapi.StateRunning, Action: modelapi.ActionLaunch, Routed: &routed},
		{ID: "omlx/unknown", GatewayID: "omlx/unknown", ProviderID: "omlx", ModelName: "unknown", Location: "local", State: modelapi.StateRunning, Action: modelapi.ActionLaunch},
		{ID: "omlx/unrouted", GatewayID: "omlx/unrouted", ProviderID: "omlx", ModelName: "unrouted", Location: "local", State: modelapi.StateRunning, Action: modelapi.ActionLaunch, Routed: &unrouted},
		{ID: "omlx/idle", GatewayID: "omlx/idle", ProviderID: "omlx", ModelName: "idle", Location: "local", State: modelapi.StateStopped, Action: modelapi.ActionStart},
		{ID: "ollama/g", GatewayID: "ollama/g", ProviderID: "ollama", ModelName: "g", Location: "local", State: modelapi.StateStopped, Action: modelapi.ActionStart},
	}
	out := map[string]modelapi.Row{}
	for _, r := range rows {
		out[r.ID] = r
	}
	return out
}

// TestResolveSlotProfileWireRules pins every verdict ResolveSlotProfileWire
// gives and each block reason it words: a missing id, a native slot, a slot
// the server blocks, two local models, an all-cloud profile, one local model
// that is running and routed, one whose routing is unknown (R7: never a
// start), one known to be unrouted, and one that is stopped. The row is what
// the picker shows and what decides whether a launch starts anything, so a
// wrong verdict starts a model nobody asked for, or launches Claude Code with
// a slot the gateway cannot serve. A native row must block: it has no gateway
// route, and the gateway's token must never reach it (Review Focus 4).
func TestResolveSlotProfileWireRules(t *testing.T) {
	byID := wireSlotRows()
	cases := []struct {
		name    string
		slots   map[string]string
		verdict Action
		reason  string // a substring of the block reason; "" for none
		running bool
		local   string // Profile.Local's id; "" for none
		loc     string
	}{
		{name: "missing id", slots: map[string]string{"sonnet": "openrouter/a", "haiku": "openrouter/gone"},
			verdict: ActionBlock, reason: `slot haiku: openrouter/gone is not in the model server's catalog`, loc: "cloud"},
		{name: "native slot", slots: map[string]string{"sonnet": "openrouter/a", "haiku": "claude/native"},
			verdict: ActionBlock, reason: "slot haiku: claude/native is a native model, which has no gateway route", loc: "cloud"},
		{name: "blocked slot", slots: map[string]string{"sonnet": "openrouter/a", "haiku": "nopolicy/x"},
			verdict: ActionBlock, reason: "slot haiku: nopolicy/x has no LiteLLM mapping on the model server", loc: "cloud"},
		{name: "two local models", slots: map[string]string{"sonnet": "omlx/run", "haiku": "omlx/idle"},
			verdict: ActionBlock, reason: "slots name two local models (omlx/run and omlx/idle); a slot profile may name one", loc: "local"},
		{name: "all cloud", slots: map[string]string{"sonnet": "openrouter/a", "haiku": "openrouter/b"},
			verdict: ActionLaunch, loc: "cloud"},
		{name: "one local running routed", slots: map[string]string{"sonnet": "openrouter/a", "haiku": "omlx/run"},
			verdict: ActionLaunch, running: true, local: "omlx/run", loc: "mixed"},
		{name: "one local routing unknown", slots: map[string]string{"sonnet": "omlx/unknown"},
			verdict: ActionLaunch, running: true, local: "omlx/unknown", loc: "local"},
		{name: "one local running unrouted", slots: map[string]string{"sonnet": "openrouter/a", "haiku": "omlx/unrouted"},
			verdict: ActionStart, running: true, local: "omlx/unrouted", loc: "mixed"},
		{name: "one local stopped", slots: map[string]string{"sonnet": "openrouter/a", "haiku": "omlx/idle"},
			verdict: ActionStart, local: "omlx/idle", loc: "mixed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := ResolveSlotProfileWire(byID, slotCheck(0, "p", "sonnet", tc.slots))
			if !row.IsProfile() || row.Model.ID != "profile/p" {
				t.Fatalf("row id %q profile %v, want profile/p", row.Model.ID, row.IsProfile())
			}
			if row.Action() != tc.verdict {
				t.Errorf("verdict = %v (%q), want %v", row.Action(), row.BlockReason(), tc.verdict)
			}
			if tc.reason == "" && row.BlockReason() != "" {
				t.Errorf("reason = %q, want none", row.BlockReason())
			}
			if tc.reason != "" && !strings.Contains(row.BlockReason(), tc.reason) {
				t.Errorf("reason = %q, want it to contain %q", row.BlockReason(), tc.reason)
			}
			if row.Running != tc.running {
				t.Errorf("running = %v, want %v", row.Running, tc.running)
			}
			p := row.Profile
			switch {
			case tc.local == "" && p.Local != nil:
				t.Errorf("Local = %q, want none", p.Local.Model.ID)
			case tc.local != "" && (p.Local == nil || p.Local.Model.ID != tc.local || p.Local.Wire == nil):
				t.Errorf("Local = %+v, want the server's own row for %s", p.Local, tc.local)
			}
			if p.Loc != tc.loc {
				t.Errorf("loc = %q, want %q", p.Loc, tc.loc)
			}
		})
	}

	// The models handed to the slot build carry the server's location and the
	// gateway id, and an unfilled declared slot takes the default's model.
	row := ResolveSlotProfileWire(byID, slotCheck(0, "p", "sonnet", map[string]string{"sonnet": "openrouter/b"}))
	if m := row.Profile.Models["haiku"]; m.ID != "openrouter/b" || m.GatewayID != "gw-b" || m.Location != "cloud" {
		t.Errorf("haiku = %+v, want the default's model with its gateway id and location", m)
	}
	// A static error is carried through, and a check of no slots blocks.
	bad := slotCheck(0, "p", "sonnet", map[string]string{"sonnet": "openrouter/a"})
	bad.Err = errors.New("default names an unknown slot")
	if r := ResolveSlotProfileWire(byID, bad); r.Action() != ActionBlock || r.BlockReason() != "default names an unknown slot" {
		t.Errorf("static error: %v %q", r.Action(), r.BlockReason())
	}
}

// TestSlotPrefixHolderWire pins the wire twin of SlotPrefixHolder: a
// provider with id "profile" or a model whose id begins with "profile/" holds
// the prefix every profile row id uses, so a server that has one gets no
// profile rows (their ids would collide with the model's); a catalog without
// either leaves the prefix free.
func TestSlotPrefixHolderWire(t *testing.T) {
	free := []modelapi.Row{{ID: "openrouter/a", ProviderID: "openrouter"}}
	if got := SlotPrefixHolderWire(free); got != "" {
		t.Errorf("free catalog: %q, want empty", got)
	}
	byModel := append(free, modelapi.Row{ID: "profile/x", ProviderID: "openrouter"})
	if got := SlotPrefixHolderWire(byModel); !strings.Contains(got, "profile/x") {
		t.Errorf("model holder: %q, want it to name profile/x", got)
	}
	byProvider := append(free, modelapi.Row{ID: "profile/y", ProviderID: "profile"})
	if got := SlotPrefixHolderWire(byProvider); !strings.Contains(got, `a provider with id "profile"`) {
		t.Errorf("provider holder: %q, want the provider named", got)
	}
}

// aliasWireRows adds alias rows to wireSlotRows: alias/idle and alias/idle2
// both front the stopped local omlx/idle (local-first), alias/run fronts the
// running omlx/run, and alias/cloudfirst puts openrouter/a before omlx/idle,
// so the server sends it as a cloud row.
func aliasWireRows() map[string]modelapi.Row {
	byID := wireSlotRows()
	alias := func(id, loc, state, action string, targets ...modelapi.AliasTarget) {
		byID[id] = modelapi.Row{ID: id, GatewayID: id, ProviderID: "alias", Family: "alias", ModelName: id[len("alias/"):],
			Kind: modelapi.KindAlias, Location: loc, State: state, Action: action, Registered: true, Targets: targets}
	}
	idle := modelapi.AliasTarget{ID: "omlx/idle", Order: 1, ProviderID: "omlx", Location: "local", State: modelapi.StateStopped}
	alias("alias/idle", "local", modelapi.StateStopped, modelapi.ActionStart, idle)
	alias("alias/idle2", "local", modelapi.StateStopped, modelapi.ActionStart,
		modelapi.AliasTarget{ID: "openrouter/b", Order: 2, ProviderID: "openrouter", Location: "cloud", State: modelapi.StateRunning}, idle)
	alias("alias/run", "local", modelapi.StateRunning, modelapi.ActionLaunch,
		modelapi.AliasTarget{ID: "omlx/run", Order: 1, ProviderID: "omlx", Location: "local", State: modelapi.StateRunning})
	alias("alias/cloudfirst", "cloud", modelapi.StateRunning, modelapi.ActionLaunch,
		modelapi.AliasTarget{ID: "openrouter/a", Order: 1, ProviderID: "openrouter", Location: "cloud", State: modelapi.StateRunning},
		modelapi.AliasTarget{ID: "omlx/idle", Order: 2, ProviderID: "omlx", Location: "local", State: modelapi.StateStopped})
	return byID
}

// TestSlotProfileAliasAndItsTargetAreOneLocalModel pins that the one-local-
// model rule counts local TARGETS, not slot ids: two slots on one alias, an
// alias beside its own local target, and two aliases fronting the same local
// target all name one local model, which one start brings up. Counting ids
// would refuse a profile that loads nothing twice. The start goes to the row
// that needs it, an alias first in slot order (its start also adds the
// alias's routes); a cloud-first alias is a cloud slot and starts nothing.
func TestSlotProfileAliasAndItsTargetAreOneLocalModel(t *testing.T) {
	byID := aliasWireRows()
	for _, tc := range []struct {
		name    string
		slots   map[string]string
		verdict Action
		local   string
		loc     string
	}{
		{"two slots on one alias", map[string]string{"sonnet": "alias/idle", "haiku": "alias/idle"}, ActionStart, "alias/idle", "local"},
		{"alias and its target", map[string]string{"sonnet": "alias/idle", "haiku": "omlx/idle"}, ActionStart, "alias/idle", "local"},
		{"target and its alias", map[string]string{"sonnet": "omlx/idle", "haiku": "alias/idle"}, ActionStart, "omlx/idle", "local"},
		{"two aliases on one target", map[string]string{"sonnet": "alias/idle", "haiku": "alias/idle2", "opus": "openrouter/a"}, ActionStart, "alias/idle", "mixed"},
		{"running alias and target", map[string]string{"sonnet": "alias/run", "haiku": "omlx/run"}, ActionLaunch, "alias/run", "local"},
		{"cloud-first alias beside a local model", map[string]string{"sonnet": "alias/cloudfirst", "haiku": "omlx/run"}, ActionLaunch, "omlx/run", "mixed"},
		{"cloud-first alias alone", map[string]string{"sonnet": "alias/cloudfirst"}, ActionLaunch, "", "cloud"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := ResolveSlotProfileWire(byID, slotCheck(0, "p", "sonnet", tc.slots))
			if row.Action() != tc.verdict {
				t.Fatalf("verdict = %v (%q), want %v", row.Action(), row.BlockReason(), tc.verdict)
			}
			p := row.Profile
			switch {
			case tc.local == "" && p.Local != nil:
				t.Errorf("Local = %q, want none", p.Local.Model.ID)
			case tc.local != "" && (p.Local == nil || p.Local.Model.ID != tc.local || p.Local.Wire == nil):
				t.Errorf("Local = %+v, want the server's row for %s", p.Local, tc.local)
			}
			if p.Loc != tc.loc {
				t.Errorf("loc = %q, want %q", p.Loc, tc.loc)
			}
		})
	}
}

// TestSlotProfileTwoAliasesTwoLocalTargetsRefused pins that the one-local-
// model rule still holds across aliases: two aliases whose local targets are
// different models load two local models, and are refused as two local ids
// always were, naming the slot rows; so is an alias beside a different local
// model. The blocked row reports no running local model.
func TestSlotProfileTwoAliasesTwoLocalTargetsRefused(t *testing.T) {
	byID := aliasWireRows()
	for _, tc := range []struct {
		slots map[string]string
		want  string
	}{
		{map[string]string{"sonnet": "alias/idle", "haiku": "alias/run"}, "slots name two local models (alias/idle and alias/run); a slot profile may name one"},
		{map[string]string{"sonnet": "alias/run", "haiku": "omlx/idle"}, "slots name two local models (alias/run and omlx/idle); a slot profile may name one"},
	} {
		row := ResolveSlotProfileWire(byID, slotCheck(0, "p", "sonnet", tc.slots))
		if row.Action() != ActionBlock || row.BlockReason() != tc.want {
			t.Errorf("%v: %v %q, want block %q", tc.slots, row.Action(), row.BlockReason(), tc.want)
		}
		if row.Running || row.Profile.Local != nil {
			t.Errorf("%v: running %v local %v, want neither", tc.slots, row.Running, row.Profile.Local)
		}
	}
}

// TestSlotRedirectNotesNameEachRenamedSlotOnce pins #687's client note: one
// line per distinct slot id the catalog reports as a redirect, in FilledOrder,
// with the exact repoint command; a slot on an ordinary alias or a model, or
// on an id the catalog lacks, gets none. The launch is never blocked by it.
func TestSlotRedirectNotesNameEachRenamedSlotOnce(t *testing.T) {
	rows := []modelapi.Row{
		{ID: "alias/old", Kind: modelapi.KindAlias, RedirectTo: "alias/new"},
		{ID: "alias/new", Kind: modelapi.KindAlias},
		{ID: "openrouter/a"},
	}
	slots := map[string]string{"sonnet": "alias/old", "opus": "alias/old", "haiku": "openrouter/a", "fable": "alias/ghost"}
	got := SlotRedirectNotes(rows, slotCheck(0, "p", "sonnet", slots))
	want := []string{"alias/old is renamed to alias/new; run 'wt profile slots repoint alias/old alias/new'"}
	if !slices.Equal(got, want) {
		t.Errorf("notes = %q, want %q", got, want)
	}
	if got := SlotRedirectNotes(rows, slotCheck(0, "p", "sonnet", map[string]string{"sonnet": "alias/new"})); len(got) != 0 {
		t.Errorf("an ordinary alias got notes %q", got)
	}
}

// TestSlotProfileAliasWithSeveralLocalTargetsIsOneLocalGroup pins #690: an
// alias that starts two local models counts as one local group keyed by the
// whole set, so two slots on it, or two aliases with the same set (in any
// order), are one local model and one start; the alias beside one of its own
// targets, or beside an alias with a different set, is two local models and
// the block message names the set.
func TestSlotProfileAliasWithSeveralLocalTargetsIsOneLocalGroup(t *testing.T) {
	byID := aliasWireRows()
	local := func(id, state string, order int) modelapi.AliasTarget {
		return modelapi.AliasTarget{ID: id, Order: order, ProviderID: "omlx", Location: "local", State: state}
	}
	alias := func(id string, targets ...modelapi.AliasTarget) {
		byID[id] = modelapi.Row{ID: id, GatewayID: id, ProviderID: "alias", Family: "alias", ModelName: id[len("alias/"):],
			Kind: modelapi.KindAlias, Location: "local", State: modelapi.StateStopped, Action: modelapi.ActionStart, Registered: true, Targets: targets}
	}
	alias("alias/pair", local("omlx/run", modelapi.StateRunning, 1), local("omlx/idle", modelapi.StateStopped, 2))
	alias("alias/pair-rev", local("omlx/idle", modelapi.StateStopped, 1), local("omlx/run", modelapi.StateRunning, 2))
	for _, slots := range []map[string]string{
		{"sonnet": "alias/pair", "haiku": "alias/pair"},
		{"sonnet": "alias/pair", "haiku": "alias/pair-rev"},
	} {
		row := ResolveSlotProfileWire(byID, slotCheck(0, "p", "sonnet", slots))
		if row.Action() != ActionStart {
			t.Errorf("%v: verdict %v (%q), want start (one local group)", slots, row.Action(), row.BlockReason())
		}
	}
	for _, slots := range []map[string]string{
		{"sonnet": "alias/pair", "haiku": "omlx/idle"},
		{"sonnet": "alias/pair", "haiku": "alias/idle"},
	} {
		row := ResolveSlotProfileWire(byID, slotCheck(0, "p", "sonnet", slots))
		if row.Action() != ActionBlock || !strings.Contains(row.BlockReason(), "alias/pair (omlx/idle + omlx/run)") ||
			!strings.Contains(row.BlockReason(), "slots name two local models") {
			t.Errorf("%v: %v %q, want the two-local-models block naming the set", slots, row.Action(), row.BlockReason())
		}
	}
}
