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
// unknown-model message and a blocked row's block_reason are the errors, and a
// start row comes back for the caller to start. Without a pin it lists the
// catalog, narrows it to what agent can use (tags, family) and keeps the
// launch rows. The second result is the list rotation walks: registered rows
// only, in Order, never a blocked or start row and never a discovered model.
// One launchable row is the answer; several are "multiple models match". A pin
// must also be one the agent can use under -T and -F.
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
		if row.Action == modelapi.ActionBlock {
			reason := row.BlockReason
			if reason == "" {
				reason = fmt.Sprintf("model %q is blocked", row.ID)
			}
			return modelapi.Row{}, nil, errors.New(reason)
		}
		if len(catalog.EligibleWire([]modelapi.Row{row}, agent, tags, family)) == 0 {
			return modelapi.Row{}, nil, fmt.Errorf("model %q is not in the eligible list for agent %q", pinned, agent.Name)
		}
		return row, nil, nil
	}
	cat, err := ms.client.Catalog(ctx)
	if err != nil {
		return modelapi.Row{}, nil, err
	}
	eligible := catalog.EligibleWire(cat.Rows, agent, tags, family)
	launchable := catalog.Launchable(eligible)
	var rotation []modelapi.Row
	for _, r := range launchable {
		if r.Registered {
			rotation = append(rotation, r)
		}
	}
	switch {
	case len(eligible) == 0:
		return modelapi.Row{}, nil, fmt.Errorf("no models match agent %q with tags %q and family %q", agent.Name, tags, family)
	case len(launchable) == 0:
		return modelapi.Row{}, nil, fmt.Errorf("no cloud or running local model for agent %q — start one with `wt -M <id>`", agent.Name)
	case len(launchable) > 1:
		return modelapi.Row{}, rotation, fmt.Errorf("multiple models match for agent %q", agent.Name)
	}
	return launchable[0], rotation, nil
}
