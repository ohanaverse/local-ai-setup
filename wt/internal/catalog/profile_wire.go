package catalog

import (
	"fmt"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)

// ResolveSlotProfileWire is ResolveSlotProfile on a client session: the
// resolved rules that need no registry, with the model server's catalog rows
// (byID, every row the catalog lists, keyed by id) in place of the registry,
// the inventory probe and the [litellm] table. The first failing rule blocks
// the row and is its reason, checked over every filled slot in FilledOrder
// before the next rule runs:
//
//  1. a static error from profiles.ValidateSlots is the reason as it is;
//  2. every slot id must be a catalog row;
//  3. a native row has no gateway route (Review Focus 4: the gateway's
//     origin and token never reach a native model);
//  4. a row the server blocks blocks the profile with its block_reason;
//  5. at most one distinct local model, counted by the model each slot row
//     loads (an alias row's local target, or the row itself).
//
// The one local model's row is Profile.Local (FromWire) and supplies the
// row's RUNNING cell; the verdict is start exactly when NeedsStart says the
// row needs a start (a stopped model, or a running one the gateway is known
// not to list; an unknown route never starts, R7). A slot profile always uses
// the gateway, so there is no proxy check.
func ResolveSlotProfileWire(byID map[string]modelapi.Row, c profiles.SlotCheck) Row {
	p := &ProfileRow{Check: c, Verdict: ActionLaunch}
	row := Row{Model: config.Model{ID: c.Profile.ID()}, Status: StatusOK, Profile: p}
	judged := false
	block := func(reason string) Row {
		p.Verdict, p.Reason = ActionBlock, reason
		if !judged && !c.Duplicate {
			row.Running, row.Loading = slotLocalStateWire(byID, c.Profile)
		}
		return row
	}
	if c.Err != nil {
		return block(c.Err.Error())
	}

	slots := c.FilledOrder()
	p.Models = map[string]config.Model{}
	cloud, local := false, false
	for _, s := range slots {
		r, ok := byID[c.Profile.Slots[s]]
		if !ok {
			continue
		}
		p.Models[s] = ToModel(r)
		if r.Location == string(config.LocationLocal) {
			local = true
		} else {
			cloud = true
		}
	}
	if def, ok := p.Models[c.RestSlot()]; ok {
		for _, s := range c.Declared {
			if _, filled := c.Profile.Slots[s]; !filled {
				p.Models[s] = def
			}
		}
	}
	switch {
	case cloud && local:
		p.Loc = "mixed"
	case local:
		p.Loc = string(config.LocationLocal)
	case cloud:
		p.Loc = string(config.LocationCloud)
	}

	for _, s := range slots {
		if _, ok := p.Models[s]; !ok {
			return block(fmt.Sprintf("slot %s: %s is not in the model server's catalog", s, c.Profile.Slots[s]))
		}
	}
	for _, s := range slots {
		if m := p.Models[s]; m.Native {
			return block(fmt.Sprintf("slot %s: %s is a native model, which has no gateway route", s, m.ID))
		}
	}
	for _, s := range slots {
		r := byID[p.Models[s].ID]
		if r.Action == modelapi.ActionBlock {
			reason := r.BlockReason
			if reason == "" {
				reason = r.ID + " is blocked on the model server"
			}
			return block("slot " + s + ": " + reason)
		}
	}

	// Local models are counted by the model each one loads (localTarget): an
	// alias and its own local target, or two aliases on one target, are one
	// local model and one start.
	var groups [][]modelapi.Row
	group := map[string]int{}
	for _, m := range p.Distinct() {
		r := byID[m.ID]
		key := localTarget(r)
		if key == "" {
			continue
		}
		i, ok := group[key]
		if !ok {
			i = len(groups)
			group[key] = i
			groups = append(groups, nil)
		}
		groups[i] = append(groups[i], r)
	}
	if len(groups) > 1 {
		return block(fmt.Sprintf("slots name two local models (%s and %s); a slot profile may name one", groups[0][0].ID, groups[1][0].ID))
	}
	if len(groups) == 0 {
		return row
	}
	// The row the launch starts is the first, in slot order, that needs a
	// start (an alias's start also adds the alias's routes), else the first.
	pick := groups[0][0]
	for _, r := range groups[0] {
		if NeedsStart(r) {
			pick = r
			break
		}
	}
	own := FromWire(pick)
	judged = true
	p.Local = &own
	row.Running, row.Loading = own.Running, own.Loading
	if NeedsStart(pick) {
		p.Verdict = ActionStart
	}
	return row
}

// localTarget is the local model a catalog row loads: its own id for a local
// model row, an alias row's local target for a local alias (one whose
// lowest-order level holds its local target; the server sends any other
// alias as a cloud row), and "" for a cloud row. A local alias row whose
// targets name no local model (an older server) counts as itself.
func localTarget(r modelapi.Row) string {
	if r.Location != string(config.LocationLocal) {
		return ""
	}
	if r.Kind == modelapi.KindAlias {
		for _, t := range r.Targets {
			if t.Location == string(config.LocationLocal) {
				return t.ID
			}
		}
	}
	return r.ID
}

// slotLocalStateWire is slotLocalState over catalog rows: the Running and
// Loading of the one local model sp's slots name, for a row blocked before
// that model was judged. Both are false when the slots name no local catalog
// row, or more than one.
func slotLocalStateWire(byID map[string]modelapi.Row, sp profiles.SlotProfile) (running, loading bool) {
	var local *modelapi.Row
	for _, id := range sp.Slots {
		r, ok := byID[id]
		if !ok || localTarget(r) == "" {
			continue
		}
		if local != nil && localTarget(*local) != localTarget(r) {
			return false, false
		}
		local = &r
	}
	if local == nil {
		return false, false
	}
	own := FromWire(*local)
	return own.Running, own.Loading
}

// SlotPrefixHolderWire is SlotPrefixHolder over the model server's catalog:
// what in it holds the "profile/" prefix every profile row id uses, worded to
// follow "the model server's catalog has "; "" while the prefix is free. A
// provider with id profiles.SlotReservedProvider holds it (named first), and
// so does any row whose id begins with the prefix. A client session builds no
// profile row while it is held, so a row id stays unique.
func SlotPrefixHolderWire(rows []modelapi.Row) string {
	for _, r := range rows {
		if r.ProviderID == profiles.SlotReservedProvider {
			return fmt.Sprintf("a provider with id %q", profiles.SlotReservedProvider)
		}
	}
	for _, r := range rows {
		if strings.HasPrefix(r.ID, profiles.SlotIDPrefix) {
			return fmt.Sprintf("a model whose id begins with %q (%s)", profiles.SlotIDPrefix, r.ID)
		}
	}
	return ""
}

// SlotPrefixHeldReason is what a launch, a show or a pin says when the model
// server's catalog holds the "profile/" prefix (holder is SlotPrefixHolderWire's
// answer): no profile row exists, and the way out is on the server. One
// wording for the command line and the picker.
func SlotPrefixHeldReason(holder string) string {
	return "the model server's catalog has " + holder + " — slot profiles are unavailable; rename that provider or model on the model server to free the \"profile/\" prefix, or launch a model with -M <id> instead of a slot profile"
}

// WireByID indexes catalog rows by id, the map ResolveSlotProfileWire reads.
func WireByID(rows []modelapi.Row) map[string]modelapi.Row {
	out := make(map[string]modelapi.Row, len(rows))
	for _, r := range rows {
		out[r.ID] = r
	}
	return out
}

// SlotRedirectNotes is, for a slot profile's slots, one note per distinct slot
// model the server's catalog reports as a redirect (#687, modelapi.Row.
// RedirectTo): `alias/old is renamed to alias/new; run 'wt profile slots
// repoint alias/old alias/new'`, in FilledOrder. The launch goes on: the old
// id stays routed until the redirect is removed, so this is advice, never a
// block. A launch prints each after "wt: note: "; the picker adds them to its
// launch notes (both paths call this, so the wording cannot drift). rows are
// the catalog's rows.
func SlotRedirectNotes(rows []modelapi.Row, c profiles.SlotCheck) []string {
	byID := WireByID(rows)
	var out []string
	seen := map[string]bool{}
	for _, slot := range c.FilledOrder() {
		id := c.Profile.Slots[slot]
		r, ok := byID[id]
		if !ok || r.RedirectTo == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, fmt.Sprintf("%s is renamed to %s; run 'wt profile slots repoint %s %s'", id, r.RedirectTo, id, r.RedirectTo))
	}
	return out
}
