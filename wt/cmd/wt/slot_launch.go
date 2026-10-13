// A slot-profile launch on the non-TUI path: the -M profile/<name> pin, the
// ten steps of the launch flow, the fail-closed applier and the post-exit
// flow. See docs/wt-agents/slot-profiles.md.
package main

import (
	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)

// launchSlotChecks is the process-wide set of slot profiles resolveModel
// offers as pin targets. runLaunchPath sets it from the app; it is nil
// everywhere else, so `wt start`, `wt smoke` and every other caller of the
// catalog see no profile row. Like allowReplace it is CLI mode state, not a
// parameter.
var launchSlotChecks []profiles.SlotCheck

// slotPin is the error resolveModel returns for a -M pin that names a slot
// profile whose row is not blocked. It is not a failure: the caller
// (launchFilteredImpl) launches row through launchSlotProfile. A sentinel
// error, as errCommandAgent is, because a profile is not a config.Model and
// resolveModel returns one.
type slotPin struct{ row catalog.Row }

func (p *slotPin) Error() string { return "model pin " + p.row.Model.ID + " is a slot profile" }

// Seams over the engine a profile launch drives. cmd/wt's TestMain makes each
// fail with "<name> not stubbed in this test", so no test writes the real
// config.yaml, restarts the real proxy or starts a real model; tests call
// stubSlotSeams.
var (
	ensureSlotRoutes = lifecycle.EnsureModelRoutes
	restartProxyNow  = lifecycle.RestartProxy
	probeProxyNow    = lifecycle.ProbeProxy
	settleRoutesNow  = lifecycle.SettleRoutes
	startSlotModel   = startForLaunchOwed
	buildSlotCmd     = agents.BuildSlotLaunchCmd
)
