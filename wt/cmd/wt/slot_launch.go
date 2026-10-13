// A slot-profile launch on the non-TUI path: the -M profile/<name> pin, the
// ten steps of the launch flow, the fail-closed applier and the post-exit
// flow. See docs/wt-agents/slot-profiles.md.
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
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

// applySlotProfile is the fail-closed applier of a slot-profile launch. It
// runs the self-heal of a settings file left by an earlier automatic-profile
// session, then applies a hand-built profiles.ResolvedProfile that holds only
// ExtraArgs (the profile's args) and Wrapper. Env and ConfigContent stay
// empty: the agent's slot build delivered them (for claude, inside the inline
// --settings JSON), so nothing is written to disk here.
//
// It is not applyResolvedProfile: that one degrades to an unprofiled launch on
// an error, and a slot profile that cannot be fully applied must not launch.
// It never prompts, never reads profiles.toml and never resolves an automatic
// profile. An ApplyWrapper error is returned and aborts the launch. cleanup is
// always a non-nil no-op. Its signature is tui.SlotProfileApplier's, so the
// TUI is handed this same function.
func applySlotProfile(cmd *exec.Cmd, agent string, row catalog.Row) (cleanup func() error, err error) {
	noop := func() error { return nil }
	if row.Profile == nil {
		return noop, fmt.Errorf("%s is not a slot profile", row.Model.ID)
	}
	selfHealAgentConfigContentTarget(agent, cmd.Dir)
	sp := row.Profile.Check.Profile
	rp := profiles.ResolvedProfile{ExtraArgs: sp.Args, Wrapper: sp.Wrapper}
	profiles.ApplyEnvAndArgs(cmd, rp)
	if rp.Wrapper != nil {
		if werr := profiles.ApplyWrapper(cmd, rp.Wrapper); werr != nil {
			return noop, werr
		}
	}
	return noop, nil
}

// runSlotAgentCmd runs the built command of a slot-profile launch and then
// the post-exit flow of a profile session: release this session's refcount
// lines, run the stop picker (not filtered to the profile: it offers every
// running local model no live session uses), print the summary line naming
// the profile. There is no survey and no price notice: both are per model,
// and a profile session used several. It ends as runAgentCmd does, by
// propagating the agent's exit code.
//
// It never calls applyProfileForLaunch: automatic profiles are not applied to
// a slot-profile launch. cleanup is the applier's and must not be nil.
func runSlotAgentCmd(cmd *exec.Cmd, agent string, row catalog.Row, cfg *config.Config, cleanup func() error) error {
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	duration, err := agents.RunAndCleanup(cmd, cleanup)

	releaseSession()
	runStopPicker(cfg)
	// Leading "\n" for the same reason as runAgentCmd's: the agent's last
	// byte may not be a newline.
	fmt.Println("\n" + agents.Summary(agent, config.Model{ID: row.Model.ID}, duration))
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			// This os.Exit bypasses main's own wait, and the stop picker just
			// above may have started an async proxy restart.
			lifecycle.WaitPendingRoutes()
			os.Exit(ee.ExitCode())
		}
		return err
	}
	return nil
}
