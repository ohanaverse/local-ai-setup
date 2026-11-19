package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/cli"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/exitcode"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rotation"
)

// launchCommand is a command agent's launch (shell): no model layer, so no
// model server and no registry. It is a package-variable seam
// (not yet per-app): the dispatch tests swap it to see which command a
// launch reached and with what arguments, without running it.
var launchCommand = launchCommandImpl

// launchNative launches a model-driven agent in native mode, with no model
// server (#615): the native model and a zero Gateway, so the agent's own
// configuration decides the model and no gateway token reaches it. It prints
// the one native note, registers no client session and records no rotation or
// usage (there is no model id). Everything else a launch does (the worktree,
// the guard, claims, --yolo, passthrough args, the summary) is unchanged.
func launchNative(a *app, agent, worktreePath string, cfg *config.Config, yolo bool, extraArgs []string, pp *precomputedProfiles) error {
	m := agents.NativeModel()
	cmd, _, err := a.hooks.build(agent, m, worktreePath, yolo, agents.Gateway{}, nil, extraArgs)
	if err != nil {
		return remoteExit(modelclient.Still(err, launchStill))
	}
	fmt.Fprintln(osStderr, agents.NativeNote(agent))
	return runLaunch(cmd, agent, m, cfg, pp, extraArgs, runTail{})
}

func launchCommandImpl(agent, worktreePath string, cfg *config.Config, yolo, pinnedSupplied bool, extraArgs []string, pp *precomputedProfiles) error {
	if pinnedSupplied {
		fmt.Fprintf(os.Stderr, "wt: -M ignored for command %q\n", agent)
	}
	cmd, _, err := agents.BuildLaunchCmdGateway(agent, config.Model{}, worktreePath, yolo, agents.Gateway{}, nil, extraArgs)
	if err != nil {
		return err
	}
	return runLaunch(cmd, agent, config.Model{}, cfg, pp, extraArgs, runTail{})
}

// errRemoteNeedsPicker is launchFilteredRemote's answer for a launch the
// picker must decide (several launchable models, no -M, a terminal):
// runLaunchPath opens the model server's picker for it.
var errRemoteNeedsPicker = errors.New("several models match: pick one")

// launchStill is what a remote launch that stopped short says is still true.
const launchStill = "nothing was launched"

// launchFilteredRemote is the non-TUI launch on the model server: every non-TUI
// launch (Task 18; with no endpoint configured a model-driven agent refuses
// at openModels, before anything starts). It reads
// config.toml only (a.sessionCfg), never the registry, config.yaml or a local
// provider, and starts nothing local. In order:
//
//  1. resolve the model on the server (resolveModelRemote: a -M pin through
//     resolve, else the catalog's one launchable row); a -M pin of a slot
//     profile (slotPinRemote) goes to launchSlotProfileRemote instead;
//  2. several launchable rows and no pin: on a terminal, errRemoteNeedsPicker
//     (the caller opens the picker, whose cursor is the rotation's next
//     row); without one the registry path's pickerNeedsTTYError;
//  3. register the client session with the model before any start, so the
//     server's idle scan counts it; a failed register is one `wt: note:` and
//     the launch goes on (client sessions are advisory);
//  4. run a start job only when catalog.NeedsStart says so (a start row, or a
//     running local model the gateway is known not to list; an unknown route
//     never starts, R7), answering its questions with cliAnswerer;
//  5. build the agent against the gateway, record the rotation, run it with
//     the heartbeat going, and end the client session before any exit.
//
// A start that fails takes the model out of the client session and ends it;
// Ctrl-C during the start detaches from the job (it keeps running on the
// server), ends the client session and exits 2. Every error carries its exit
// code (remoteExit).
func launchFilteredRemote(ctx context.Context, a *app, agent, worktreePath string, yolo bool, tags, family, pinned string, pinnedSupplied bool, extraArgs []string, others string, allowReplace bool, pp *precomputedProfiles) error {
	cfg, err := a.sessionCfg()
	if err != nil {
		return remoteExit(modelclient.Still(err, launchStill))
	}
	if agents.IsCommand(agent) {
		// A command has no model layer: it needs no model server.
		return launchCommand(agent, worktreePath, cfg, yolo, pinnedSupplied, extraArgs, pp)
	}
	ag, err := cfg.AgentByName(agent)
	if err != nil {
		return remoteExit(modelclient.Still(err, launchStill))
	}
	// Native mode and no model asked for: native (#615).
	if _, berr := a.buildModels(launchStill); berr != nil && nativeFallback(berr, pinned, pinnedSupplied, tags, family) {
		return launchNative(a, agent, worktreePath, cfg, yolo, extraArgs, pp)
	}
	// A -M pin of a slot profile is its own launch unit, as on the registry
	// path: the server knows no profile id, so the pin never goes to its
	// resolve. A profile hidden by -T/-F or held by another agent is refused
	// with the registry path's reason.
	if pinned != "" {
		c, isProfile, perr := slotPinRemote(a.slotChecks, agent, pinned, tags, family)
		if perr != nil {
			return remoteExit(modelclient.Still(perr, launchStill))
		}
		if isProfile {
			// launchSlotProfileRemote reads the package-level others and
			// allowReplace values (its own flags): this function's `others`
			// and `allowReplace` parameters are ignored for a profile pin.
			prow := catalog.Row{Model: config.Model{ID: c.Profile.ID()}, Profile: &catalog.ProfileRow{Check: c}}
			return launchSlotProfileRemote(ctx, a, agent, worktreePath, yolo, prow, extraArgs)
		}
	}
	ms, err := a.openModels(ctx, launchStill)
	if err != nil {
		return remoteExit(err) // openModels' errors carry the still clause
	}

	row, rot, err := resolveModelRemote(ctx, ms, *ag, tags, family, pinned)
	if err != nil && pinned == "" && len(rot) > 0 {
		// Several launchable models and no -M: the user picks, as on the
		// registry path. On a terminal runLaunchPath opens the model server's
		// picker (its cursor on the rotation's next row); without one this is
		// the registry path's refusal (P5), naming -M. Nothing is registered
		// or started yet.
		if stdinTTY() {
			return errRemoteNeedsPicker
		}
		return pickerNeedsTTYError(agent)
	}
	if err != nil {
		return remoteExit(modelclient.Still(err, launchStill))
	}

	// The client session holds the model from before its start to the
	// agent's exit. Its notes (a failed or recovered heartbeat) are notes.
	// The heartbeat goroutine calls note: it captures the stream once.
	whenFailed := "the launch goes on"
	if row.Location == "local" {
		// Only a local model is ever stopped for want of a client.
		whenFailed += fmt.Sprintf(", but the server may stop %s once it is idle", row.ID)
	}
	sess, _, end := openClientSession(ctx, ms, []string{row.ID}, whenFailed)

	if catalog.NeedsStart(row) {
		if serr := startRemote(ctx, ms, row, others, allowReplace); serr != nil {
			// Nothing will use the model: take it out of the client session
			// before ending it (a re-register without it).
			_ = sess.Remove(context.WithoutCancel(ctx), row.ID)
			end()
			return remoteExit(startStill(serr))
		}
	}

	m := catalog.ToModel(row)
	gw := agents.Gateway{Origin: ms.ep.Origin(), Token: ms.ep.Token}
	var models []config.Model
	if _, ok := agents.ByName(agent).(agents.GatewaySyncer); ok && !m.Native {
		models, err = gatewayModels(ctx, ms)
		if err != nil {
			end()
			return remoteExit(modelclient.Still(err, launchStill))
		}
	}
	cmd, _, err := a.hooks.build(agent, m, worktreePath, yolo, gw, models, extraArgs)
	if err != nil {
		end()
		return remoteExit(modelclient.Still(err, launchStill))
	}
	if rerr := rotation.New().RecordFor(agent, m.ID); rerr != nil {
		// RecordFor names the file that failed.
		fmt.Fprintf(osStderr, "wt: note: %v\n", rerr)
	}
	return runLaunch(cmd, agent, m, cfg, pp, extraArgs, runTail{End: end, PriceNote: remotePriceNoteFunc(ctx, ms)})
}

// registerRefused reports a register error no retry can heal: the same three
// kinds modelclient's Session.Start stops its heartbeat for.
func registerRefused(err error) bool { return modelclient.IsPermanent(err) }

// gatewayModels is the server's non-native models, which a GatewaySyncer
// (pi's models.json) lists.
func gatewayModels(ctx context.Context, ms *modelSession) ([]config.Model, error) {
	cat, err := ms.client.Catalog(ctx)
	if err != nil {
		return nil, err
	}
	return catalog.NonNativeModels(cat.Rows), nil
}

// startRemote runs the start job for row on the server, with progress on
// stderr and the questions answered by --replace, --others and the terminal.
// Ctrl-C detaches (the job keeps running); a second Ctrl-C kills wt.
func startRemote(ctx context.Context, ms *modelSession, row modelapi.Row, others string, allowReplace bool) error {
	// A second Ctrl-C kills wt (interruptCtx restores the default handler).
	sctx, stop := interruptCtx(ctx)
	defer stop()
	ans := &cliAnswerer{
		id: row.ID, replace: allowReplace, others: others, out: osStderr,
		tty: func() (*os.File, error) {
			// A redirected stdin is a script even with a terminal attached
			// (#424): it is never asked.
			if !stdinTTY() {
				return nil, cli.ErrNoTerminal
			}
			return openTTY()
		},
	}
	_, err := ms.client.RunStart(sctx, modelapi.StartRequest{Model: row.ID, AllowReplace: allowReplace}, ans, startProgressRemote(osStderr, row.ID))
	if errors.Is(err, context.Canceled) && sctx.Err() != nil {
		// Ctrl-C before the job had an id (the active-jobs read or the
		// submit): RunStart returns the bare cancel, not a *DetachedError.
		// Same meaning, same exit 2: a submit already on the wire may have
		// created a job.
		return &interruptedStartError{Model: row.ID}
	}
	return err
}

// interruptedStartError is a Ctrl-C that landed before the start job's id was
// known. Like *modelclient.DetachedError it is exit 2 (remoteExit), because a
// submit already on the wire can have created a job.
type interruptedStartError struct{ Model string }

func (e *interruptedStartError) Error() string {
	return fmt.Sprintf("detached — interrupted before the start of %s was confirmed; a job may be running on the model server (see 'wt model list --server')", e.Model)
}

// startStill adds what is still true to a failed start: nothing was launched.
// An approval-missing refusal or a decline already says nothing was started.
func startStill(err error) error {
	var declined *modelclient.DeclinedError
	if _, coded := exitcode.Code(err); coded || errors.As(err, &declined) {
		return err
	}
	return modelclient.Still(err, launchStill)
}

// remotePriceNoteFunc is the runTail.PriceNote of a launch on the model
// server: the stale-pricing notice from the catalog's pricing member, aged on
// the server's clock (agents.PrintRemotePriceNotice). One cheap, best-effort
// catalog fetch after the agent exits; the registry's stamps are never read.
func remotePriceNoteFunc(ctx context.Context, ms *modelSession) func() {
	return func() { agents.PrintRemotePriceNotice(ctx, ms.client, nil, false) }
}
