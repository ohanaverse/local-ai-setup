package modelserver

import (
	"fmt"
	"slices"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/sessionreg"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tiers"
	"github.com/ohanaverse/local-ai-setup/wt/internal/windows"
)

// aliasRows builds one catalog row per registry alias (#596, decision 5)
// from the model rows already built: id and gateway id are the alias id,
// provider and family "alias", the name after "alias/" its model name (what
// claude labels a slot with), registered, ordered from orderBase in file
// order, with its targets, the window windows.Alias allows, the tier of its
// lowest-order target (a redirect row, #687, carries its target alias's targets,
// window and tier and names it in RedirectTo; it stays launchable) (the lowest tier among equal orders) and no price. A
// malformed alias, or one config.AliasFault rejects, is a block row naming
// its fault; one whose id lacks the alias/ prefix gets no row (servedAliasID:
// the id may be a model's, whose own row stands). routed is the gateway's id set (nil: unknown, read as routed).
func aliasRows(cfg *config.Config, models []modelapi.Row, ix tiers.Index, sessions *sessionreg.Registry, routed map[string]bool, orderBase int) []modelapi.Row {
	if len(cfg.Aliases) == 0 {
		return nil
	}
	rows := make(map[string]modelapi.Row, len(models))
	for _, r := range models {
		rows[r.ID] = r
	}
	var ids []string
	for _, a := range cfg.Aliases {
		if servedAliasID(a.ID) {
			ids = append(ids, a.ID)
		}
	}
	counts := sessions.Counts(ids)
	seen := map[string]bool{}
	var out []modelapi.Row
	for i, a := range cfg.Aliases {
		if !servedAliasID(a.ID) || seen[a.ID] {
			continue
		}
		seen[a.ID] = true
		row := modelapi.Row{
			ID: a.ID, GatewayID: a.ID, Family: config.AliasProvider, ProviderID: config.AliasProvider,
			ModelName:  strings.TrimPrefix(a.ID, config.AliasIDPrefix),
			Location:   string(config.LocationCloud),
			Tags:       []string{},
			Registered: true,
			Order:      orderBase + i,
			Protocols:  []string{},
			Sessions:   counts[a.ID],
			Kind:       modelapi.KindAlias,
			RedirectTo: a.RedirectTo,
		}
		if routed != nil {
			v := routed[a.ID]
			row.Routed = &v
		}
		for _, t := range cfg.AliasTargets(a) {
			at := modelapi.AliasTarget{ID: t.Model, Order: t.Order}
			if r, ok := rows[t.Model]; ok {
				at.ProviderID, at.Location, at.State, at.Context = r.ProviderID, r.Location, r.State, r.Context
			}
			row.Targets = append(row.Targets, at)
		}
		row.Context, _ = windows.Alias(cfg, a, ix)
		row.Protocols = sharedProtocols(row.Targets, rows)
		copyTier(&row, row.Targets, rows)
		if fault := config.AliasFault(cfg, a); fault != nil {
			row.State, row.Action, row.BlockReason = modelapi.StateBlocked, modelapi.ActionBlock, fault.Error()
		} else {
			row.Location, row.State, row.Action, row.BlockReason = aliasAction(row.Targets, rows, routed == nil || routed[a.ID])
		}
		out = append(out, row)
	}
	return out
}

// aliasAction is decision 5's policy for an alias row, from its targets and
// the model rows by id. The location is local only when the lowest-order
// level holds the alias's local target. The action, in order: start when
// that level's local target must start, or runs while the alias is not
// routed (an alias start re-runs the target's route write, which adds the
// alias's rows, so catalog.NeedsStart agrees); launch when a target is
// servable and the alias routed; block when a target is servable but the
// alias is not routed; start when any target can start; else block, joining
// the targets' reasons. The state follows the action, as on every model row
// (blocked exactly when block, a running row launches): launch is running,
// block is blocked, and start is stopped, or the local target's own state
// (stopped, loading, unknown) when the start is that target's start. A
// target with no model row blocks the alias.
func aliasAction(targets []modelapi.AliasTarget, rows map[string]modelapi.Row, routed bool) (location, state, action, blockReason string) {
	location = string(config.LocationCloud)
	if len(targets) == 0 {
		return location, modelapi.StateBlocked, modelapi.ActionBlock, "the alias has no targets"
	}
	low := lowestOrder(targets)
	var levelLocal *modelapi.Row
	servable, canStart := false, false
	for _, t := range targets {
		r, ok := rows[t.ID]
		if !ok {
			return location, modelapi.StateBlocked, modelapi.ActionBlock,
				fmt.Sprintf("target %s is not in the model server's catalog", t.ID)
		}
		if t.Order == low && r.Location == string(config.LocationLocal) {
			levelLocal = &r
		}
		servable = servable || r.Action == modelapi.ActionLaunch
		canStart = canStart || r.Action == modelapi.ActionStart
	}
	if levelLocal != nil {
		location = string(config.LocationLocal)
	}
	switch {
	case levelLocal != nil && levelLocal.Action == modelapi.ActionStart:
		return location, levelLocal.State, modelapi.ActionStart, ""
	case levelLocal != nil && levelLocal.State == modelapi.StateRunning && !routed:
		return location, modelapi.StateStopped, modelapi.ActionStart, ""
	case servable && routed:
		return location, modelapi.StateRunning, modelapi.ActionLaunch, ""
	case servable:
		return location, modelapi.StateBlocked, modelapi.ActionBlock,
			"the alias is not routed on the host; run 'wt-model litellm sync' there"
	case canStart:
		return location, modelapi.StateStopped, modelapi.ActionStart, ""
	}
	var reasons []string
	for _, t := range targets {
		reason := rows[t.ID].BlockReason
		if reason == "" {
			reason = "not available"
		}
		reasons = append(reasons, t.ID+": "+reason)
	}
	return location, modelapi.StateBlocked, modelapi.ActionBlock, strings.Join(reasons, "; ")
}

// lowestOrder is the smallest order among targets.
func lowestOrder(targets []modelapi.AliasTarget) int {
	low := targets[0].Order
	for _, t := range targets[1:] {
		low = min(low, t.Order)
	}
	return low
}

// tierRank orders tier codes from lowest to highest: quick, standard, deep,
// marathon; an unrated target (no code) ranks below them all.
func tierRank(code string) int {
	return strings.Index("QSDM", code) + 1
}

// copyTier gives the alias row the tier fields of its lowest-order target,
// the lowest-ranked one among targets that share that order.
func copyTier(row *modelapi.Row, targets []modelapi.AliasTarget, rows map[string]modelapi.Row) {
	if len(targets) == 0 {
		return
	}
	low := lowestOrder(targets)
	var pick *modelapi.Row
	for _, t := range targets {
		r, ok := rows[t.ID]
		if !ok || t.Order != low {
			continue
		}
		if pick == nil || tierRank(r.TierCode) < tierRank(pick.TierCode) {
			pick = &r
		}
	}
	if pick == nil {
		return
	}
	row.Tier, row.TierCode, row.Confidence = pick.Tier, pick.TierCode, pick.Confidence
	row.Aligned, row.TierSources = pick.Aligned, pick.TierSources
}

// sharedProtocols is the protocols every target speaks, in the first
// target's order: a request through the alias may reach any of them.
func sharedProtocols(targets []modelapi.AliasTarget, rows map[string]modelapi.Row) []string {
	out := []string{}
	for i, t := range targets {
		r, ok := rows[t.ID]
		if !ok {
			return []string{}
		}
		if i == 0 {
			out = append(out, r.Protocols...)
			continue
		}
		out = slices.DeleteFunc(out, func(p string) bool { return !slices.Contains(r.Protocols, p) })
	}
	return out
}
