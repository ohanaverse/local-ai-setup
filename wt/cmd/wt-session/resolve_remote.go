package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
)

// resolveModelRemote resolves a launch's model over the model server's rows. With a pin
// it asks the server to resolve it (the id goes in the query): the server's
// unknown-model message is the error, then a pin the agent cannot use under
// -T and -F (judged before the block, as the TUI does, so an agent that could
// never use the row is not told how to unblock it), then a blocked row's
// block_reason; a start row comes back for the caller to start. Without a pin
// it lists the catalog, narrows it to what agent can use (tags, family) and
// keeps the launch rows that are neither aliases nor subscription rows (a
// no-pin launch never chooses either: a Claude subscription model is
// pin-only, #597). The second result is the list rotation walks: registered
// rows only, in Order, never a blocked or start row, an alias, a subscription
// row or a discovered model. One launchable row is the answer; several are
// "multiple models match"; when the only launchable rows were subscription
// rows the error says to name one with -M.
//
// Its errors (Catalog, Resolve, a block reason, an ineligible pin) come back
// WITHOUT a still clause: the caller adds it.
func resolveModelRemote(ctx context.Context, ms *modelSession, agent config.Agent, tags, family, pinned string) (modelapi.Row, []modelapi.Row, error) {
	if agents.IsCommand(agent.Name) {
		return modelapi.Row{}, nil, errCommandAgent
	}
	if pinned != "" {
		row, err := ms.client.Resolve(ctx, pinned)
		if err != nil {
			return modelapi.Row{}, nil, err
		}
		if len(catalog.EligibleWire([]modelapi.Row{row}, agent, tags, family)) == 0 {
			return modelapi.Row{}, nil, fmt.Errorf("model %q is not in the eligible list for agent %q", pinned, agent.Name)
		}
		if row.Action == modelapi.ActionBlock {
			// A subscription row an off or older server blocked says why
			// in decision 2's words, not the row's (an older server's is
			// "unmapped", #597).
			if r := subscriptionBlockedReason(ctx, ms, []modelapi.Row{row}); r != "" {
				return modelapi.Row{}, nil, errors.New(r)
			}
			reason := row.BlockReason
			if reason == "" {
				reason = fmt.Sprintf("model %q is blocked", row.ID)
			}
			return modelapi.Row{}, nil, errors.New(reason)
		}
		return row, nil, nil
	}
	cat, err := ms.client.Catalog(ctx)
	if err != nil {
		return modelapi.Row{}, nil, err
	}
	eligible := catalog.EligibleWire(cat.Rows, agent, tags, family)
	// A no-pin launch never chooses an alias (decision 14) or a subscription
	// row (#597: pin-only, so the owner's subscription is never spent
	// unasked): the picker lists them and -M launches them, but neither is
	// the one launchable row nor a rotation entry.
	var launchable, rotation []modelapi.Row
	subscriptionOnly := false
	for _, r := range catalog.Launchable(eligible) {
		if r.Kind == modelapi.KindAlias {
			continue
		}
		if catalog.UsesSubscription(r) {
			subscriptionOnly = true
			continue
		}
		launchable = append(launchable, r)
		if r.Registered {
			rotation = append(rotation, r)
		}
	}
	switch {
	case len(eligible) == 0:
		return modelapi.Row{}, nil, fmt.Errorf("no models match agent %q with tags %q and family %q", agent.Name, tags, family)
	case len(launchable) == 0 && subscriptionOnly:
		return modelapi.Row{}, nil, fmt.Errorf("the only launchable models for agent %q use your Claude subscription, which wt never picks on its own — name one with `wt -M <id>`", agent.Name)
	case len(launchable) == 0:
		return modelapi.Row{}, nil, fmt.Errorf("no cloud or running local model for agent %q — start one with `wt -M <id>`", agent.Name)
	case len(launchable) > 1:
		return modelapi.Row{}, rotation, fmt.Errorf("multiple models match for agent %q", agent.Name)
	}
	return launchable[0], rotation, nil
}
