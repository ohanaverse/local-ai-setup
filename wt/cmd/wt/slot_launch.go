// A slot-profile launch on the non-TUI path: the -M profile/<name> pin, the
// launch on the model server (launchSlotProfileRemote), the fail-closed
// applier and the post-exit flow. See docs/wt-agents/slot-profiles.md.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rootclaim"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rotation"
	"github.com/ohanaverse/local-ai-setup/wt/internal/sessionclaim"
)

// buildSlotGatewayCmd is the slot build on the launch path
// (launchSlotProfileRemote): against the model-server gateway. Tests wrap it
// to see when the agent's command is built.
var buildSlotGatewayCmd = agents.BuildSlotLaunchCmdGateway

// slotUnknownKeyNote is what wt says about a key of slot-profiles.toml the
// schema does not name: a launch prints it after "wt: ", and
// `wt profile list` after "warning: ".
func slotUnknownKeyNote(key string) string {
	return fmt.Sprintf("slot-profiles.toml: unknown key %q — ignored", key)
}

// slotWarningsRemote are the lines a launch prints once about
// slot-profiles.toml: the load error; one line per key the schema does not
// name (the file still loaded, without what the key holds); one line per
// profile that is not Usable (no usable name or agent, so it has no row and
// cannot be pinned); and one line per thing a passing profile's driver warns
// about in its config_content (slotContentNotes). A Usable profile that fails
// validation gets no line: its blocked row carries the reason. A session
// never reads the registry, so there is no "profile/" holder line: the
// catalog's holder blocks the profile launch itself with its own reason.
func slotWarningsRemote(a *app) []string {
	var out []string
	if a.slotLoadErr != nil {
		out = append(out, fmt.Sprintf("wt: %v — slot profiles are unavailable", a.slotLoadErr))
	}
	for _, key := range a.slotUnknown {
		out = append(out, "wt: "+slotUnknownKeyNote(key))
	}
	for _, c := range a.slotChecks {
		if !c.Usable {
			out = append(out, fmt.Sprintf("wt: %s: %v — skipped (see 'wt profile list')", c.Label(), c.Err))
		}
	}
	for _, note := range slotContentNotes(a) {
		out = append(out, "wt: "+note)
	}
	return out
}

// slotContentNotes are what the drivers say, without blocking, about the
// config_content of each profile that passed validation, in file order
// (slotContentNotesFor). A launch prints them after "wt: ", and `wt profile
// list` after "warning: ". They are read from the static checks only, so they
// cost a launch no probe.
func slotContentNotes(a *app) []string {
	var out []string
	for _, c := range a.slotChecks {
		out = append(out, slotContentNotesFor(c)...)
	}
	return out
}

// slotContentNotesFor is what c's driver says, without blocking, about the
// config_content of one profile that passed validation
// (agents.SlotConfigWarnings; claude's: a modelOverrides target that is not
// one of the profile's slot models, #412), each line naming the profile. nil
// for a profile that failed validation. slotContentNotes and `wt profile
// show` (#419) both word the notes here, so the two cannot drift.
func slotContentNotesFor(c profiles.SlotCheck) []string {
	var out []string
	for _, w := range agents.SlotConfigWarnings(c) {
		out = append(out, fmt.Sprintf("%s (%s): %s", c.Label(), c.Profile.Agent, w))
	}
	return out
}

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
// the post-exit flow of a profile session: end the client session (tail),
// print the summary line naming the profile. There is no survey and no price
// notice: both are per model, and a profile session used several. It ends as runAgentCmd does, by
// propagating the agent's exit code.
//
// It never calls applyProfileForLaunch: automatic profiles are not applied to
// a slot-profile launch. cleanup is the applier's and must not be nil. There
// is no stop picker: the model server stops idle models after its grace.
func runSlotAgentCmd(cmd *exec.Cmd, agent string, row catalog.Row, cleanup func() error, tail runTail) error {
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// Every launch claims the worktree the agent runs in, for exactly the
	// agent's run (Task 405) — a slot-profile launch is a launch path like
	// any other. The site is read from cmd.Dir, after any worktree creation.
	// A command with no Dir names no directory and claims nothing:
	// filepath.Abs("") is the working directory, and a claim there would
	// give every other session a false held warning for a root this launch
	// never named (claimLaunch guards the same way). HoldLaunch's others are
	// dropped on purpose: the launch warnings' pass (RunE's confirmLaunch,
	// or the TUI's gate) named any other holder already, and a launch prints
	// exactly one held line. A claim that cannot be read or written is one
	// note and never a refusal.
	claimRelease := func() {} // the no-Dir release, so the defer below is unconditional
	if cmd.Dir != "" {
		release, _, cerr := rootclaim.HoldLaunch(cmd.Dir, agent)
		claimRelease = release
		if cerr != nil {
			fmt.Fprintf(osStderr, "wt: note: %s\n", sessionclaim.CheckFailed(cmd.Dir, cerr))
		}
	}
	defer claimRelease() // every exit path below releases; safe twice

	duration, err := agents.RunAndCleanup(cmd, cleanup)
	// Explicit, not deferred: the os.Exit below would skip a deferred release.
	// Both releases take down the same claim file and are safe twice.
	claimRelease()
	releaseRootClaim()

	tail.release()
	// Leading "\n" for the same reason as runAgentCmd's: the agent's last
	// byte may not be a newline.
	fmt.Println("\n" + agents.Summary(agent, config.Model{ID: row.Model.ID}, duration))
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			os.Exit(ee.ExitCode())
		}
		return err
	}
	return nil
}

// slotPinRemote is the -M profile/<name> pin on a client session's path. It
// reads the loaded slot-profiles.toml only, so it costs no server call:
//
//   - a Usable profile of that name for agent, with no -T or -F set: its
//     check, ok true (the caller launches it through launchSlotProfileRemote,
//     which resolves it against the catalog);
//   - one hidden by -T/-F, or one only another agent has: SlotPinReason's
//     reason as an error, ok true;
//   - no profile of that name (or pin is not a profile id): ok false, and the
//     pin goes to the server's resolve as any model id does (a model whose
//     id begins with "profile/" is the server's to know).
func slotPinRemote(checks []profiles.SlotCheck, agent, pin, tags, family string) (profiles.SlotCheck, bool, error) {
	name, ok := strings.CutPrefix(pin, profiles.SlotIDPrefix)
	if !ok {
		return profiles.SlotCheck{}, false, nil
	}
	if tags == "" && family == "" {
		for _, c := range checks {
			if c.Usable && c.Profile.Agent == agent && c.Profile.Name == name {
				return c, true, nil
			}
		}
	}
	// The catalog's "profile/" holder is checked by the launch.
	if reason := catalog.SlotPinReason(checks, agent, pin, tags, family); reason != "" {
		return profiles.SlotCheck{}, true, errors.New(reason)
	}
	return profiles.SlotCheck{}, false, nil
}

// slotPrefixHeldReason is the block of a profile launch or show when the
// model server's catalog holds the "profile/" prefix: no profile row exists
// then.
func slotPrefixHeldReason(holder string) string {
	return catalog.SlotPrefixHeldReason(holder)
}

// launchSlotProfileRemote is the whole launch of one slot profile on a client
// session's path (an endpoint is configured): the -M profile/<name> pin.
// row carries the profile's check; it is resolved here, against the model
// server, in this order:
//
//  1. one Catalog read; the profile is resolved against its rows
//     (catalog.ResolveSlotProfileWire), and a blocked row stops here;
//  2. a passthrough flag that would replace the slot mapping is refused;
//  3. the client session is registered with every distinct slot model
//     (Distinct) before any start, so the server's idle scan counts them; a
//     failed register is one `wt: note:` and the launch goes on;
//  4. the one local slot model is started with a start job, only when
//     catalog.NeedsStart says so (an unknown route never starts, R7); its
//     questions are answered as on any -M pin (--replace, --others, the
//     terminal); Ctrl-C detaches (exit 2, nothing was launched);
//  5. the agent's slot build against the gateway (BuildSlotLaunchCmdGateway),
//     the fail-closed applier, the launch recorded, the agent run with the
//     heartbeat going, and the client session ended before any exit.
//
// There is no cloud-route write, proxy restart or proxy probe on this path:
// the model server owns LiteLLM. A native slot model is refused at step 1
// (Review Focus 4). The internal/tui twin is slot_flow.go's
// beginRemoteSlotLaunch: change both paths together (TestSlotFlowParity).
// Every error carries its exit code (remoteExit) and says nothing was
// launched.
func launchSlotProfileRemote(ctx context.Context, a *app, agent, worktreePath string, yolo bool, row catalog.Row, extraArgs []string) error {
	if row.Profile == nil {
		return remoteExit(modelclient.Still(fmt.Errorf("%s is not a slot profile", row.Model.ID), launchStill))
	}
	if _, err := a.sessionCfg(); err != nil {
		return remoteExit(modelclient.Still(err, launchStill))
	}
	ms, err := a.openModels(ctx, launchStill)
	if err != nil {
		return remoteExit(err) // openModels' errors carry the still clause
	}

	// Step 1: one catalog read, the profile resolved against it.
	cat, err := ms.client.Catalog(ctx)
	if err != nil {
		return remoteExit(modelclient.Still(err, launchStill))
	}
	if holder := catalog.SlotPrefixHolderWire(cat.Rows); holder != "" {
		return remoteExit(modelclient.Still(errors.New(slotPrefixHeldReason(holder)), launchStill))
	}
	row = catalog.ResolveSlotProfileWire(catalog.WireByID(cat.Rows), row.Profile.Check)
	p := row.Profile
	if row.Action() == catalog.ActionBlock {
		return remoteExit(modelclient.Still(errors.New(row.BlockReason()), launchStill))
	}

	// Step 2: a passthrough flag that would replace the slot mapping.
	if err := agents.CheckSlotPassthrough(agent, extraArgs); err != nil {
		return remoteExit(modelclient.Still(err, launchStill))
	}

	// Step 3: the client session holds every slot model from before the
	// start to the agent's exit.
	distinct := p.Distinct()
	ids := make([]string, len(distinct))
	for i, m := range distinct {
		ids[i] = m.ID
	}
	w := osStderr
	note := func(s string) { fmt.Fprintf(w, "wt: note: %s\n", s) }
	sess := ms.client.NewSession(ids, modelclient.SessionOptions{Interval: remoteBeatInterval, Note: note})
	registered := true
	if rerr := sess.Start(ctx); rerr != nil {
		registered = !registerRefused(rerr)
		note(fmt.Sprintf("the model server did not register this client session (%s) — the launch goes on, but the server may stop %s once it is idle", rerr, strings.Join(ids, ", ")))
	}
	end := func() {
		if eerr := sess.End(context.WithoutCancel(ctx)); eerr != nil && registered {
			note(fmt.Sprintf("could not end the client session on the model server (%s) — it forgets it within a minute", eerr))
		}
	}

	// Step 4: the one local slot model, started only when it needs it.
	if p.Local != nil && row.Action() == catalog.ActionStart {
		if serr := startRemote(ctx, ms, *p.Local.Wire, string(othersMode), allowReplace); serr != nil {
			// Nothing will use the model: take it out of the client session
			// before ending it (a re-register without it), as the single-model
			// launch does (launchFilteredRemote) and the picker's twin does
			// (internal/tui's runRemoteStart drops it on a job error).
			_ = sess.Remove(context.WithoutCancel(ctx), p.Local.Wire.ID)
			end()
			return remoteExit(startStill(serr))
		}
	}

	// Step 5: the command against the gateway, the applier, the record, the
	// run. A failure before the agent runs ends the client session first.
	gw := agents.Gateway{Origin: ms.ep.Origin(), Token: ms.ep.Token}
	cmd, err := buildSlotGatewayCmd(p.Launch(), worktreePath, yolo, gw, extraArgs)
	if err != nil {
		end()
		return remoteExit(modelclient.Still(err, launchStill))
	}
	if wd, werr := shellDir(); werr == nil {
		for _, n := range agents.RelativeArgNotes(agent, wd, cmd.Dir, extraArgs) {
			fmt.Fprintln(osStderr, n)
		}
	}
	cleanup, err := applySlotProfile(cmd, agent, row)
	if err != nil {
		end()
		return remoteExit(modelclient.Still(err, launchStill))
	}
	if rerr := rotation.New().RecordProfile(agent, row.Model.ID); rerr != nil {
		fmt.Fprintf(osStderr, "wt: note: %v\n", rerr)
	}
	return runSlotAgentCmd(cmd, agent, row, cleanup, runTail{End: end})
}
