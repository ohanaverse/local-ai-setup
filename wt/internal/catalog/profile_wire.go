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
//  5. at most one distinct local model.
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

	var locals []config.Model
	for _, m := range p.Distinct() {
		if m.Location == config.LocationLocal {
			locals = append(locals, m)
		}
	}
	if len(locals) > 1 {
		return block(fmt.Sprintf("slots name two local models (%s and %s); a slot profile may name one", locals[0].ID, locals[1].ID))
	}
	if len(locals) == 0 {
		return row
	}
	own := FromWire(byID[locals[0].ID])
	judged = true
	p.Local = &own
	row.Running, row.Loading = own.Running, own.Loading
	if NeedsStart(*own.Wire) {
		p.Verdict = ActionStart
	}
	return row
}

// slotLocalStateWire is slotLocalState over catalog rows: the Running and
// Loading of the one local model sp's slots name, for a row blocked before
// that model was judged. Both are false when the slots name no local catalog
// row, or more than one.
func slotLocalStateWire(byID map[string]modelapi.Row, sp profiles.SlotProfile) (running, loading bool) {
	var local *modelapi.Row
	for _, id := range sp.Slots {
		r, ok := byID[id]
		if !ok || r.Location != string(config.LocationLocal) {
			continue
		}
		if local != nil && local.ID != r.ID {
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

// WireByID indexes catalog rows by id, the map ResolveSlotProfileWire reads.
func WireByID(rows []modelapi.Row) map[string]modelapi.Row {
	out := make(map[string]modelapi.Row, len(rows))
	for _, r := range rows {
		out[r.ID] = r
	}
	return out
}
