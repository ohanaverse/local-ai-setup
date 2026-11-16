package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/exitcode"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rotation"
)

// buildGatewayCmd is a test seam over agents.BuildLaunchCmdGateway: tests
// wrap it to see when, and with what, the agent command is built.
var buildGatewayCmd = agents.BuildLaunchCmdGateway

// remoteBeatInterval is the client session's heartbeat interval; 0 is
// modelclient's default (20 s). Tests shorten it.
var remoteBeatInterval time.Duration

// errRemoteNeedsPicker is launchFilteredRemote's answer for a launch the
// picker must decide (several launchable models, no -M, a terminal):
// runLaunchPath opens the model server's picker for it.
var errRemoteNeedsPicker = errors.New("several models match: pick one")

// launchStill is what a remote launch that stopped short says is still true.
const launchStill = "nothing was launched"

// launchFilteredRemote is launchFiltered on the model server, the non-TUI
// launch when an endpoint is configured (a.endpointConfigured). It reads
// config.toml only (a.sessionCfg), never the registry, config.yaml or a local
// provider, and starts nothing local. In order:
//
//  1. resolve the model on the server (resolveModelRemote: a -M pin through
//     resolve, else the catalog's one launchable row);
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
		if pinnedSupplied {
			fmt.Fprintf(os.Stderr, "wt: -M ignored for command %q\n", agent)
		}
		cmd, err := agents.BuildLaunchCmd(agent, config.Model{}, worktreePath, yolo, cfg, extraArgs)
		if err != nil {
			return err
		}
		return runLaunch(cmd, agent, config.Model{}, cfg, pp, extraArgs, runTail{Remote: true})
	}
	ag, err := cfg.AgentByName(agent)
	if err != nil {
		return remoteExit(modelclient.Still(err, launchStill))
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
	w := osStderr
	note := func(s string) { fmt.Fprintf(w, "wt: note: %s\n", s) }
	sess := ms.client.NewSession([]string{row.ID}, modelclient.SessionOptions{Interval: remoteBeatInterval, Note: note})
	registered := true
	if rerr := sess.Start(ctx); rerr != nil {
		registered = false
		note(fmt.Sprintf("the model server did not register this client session (%s) — the launch goes on, but the server may stop %s once it is idle", rerr, row.ID))
	}
	// end runs once, on a context no cancel reaches (modelclient's End), and
	// never fails the launch. After a register the server refused, its DELETE
	// is refused too: that is not news, so it says nothing then.
	end := func() {
		if eerr := sess.End(context.WithoutCancel(ctx)); eerr != nil && registered {
			note(fmt.Sprintf("could not end the client session on the model server (%s) — it forgets it within a minute", eerr))
		}
	}

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
	cmd, _, err := buildGatewayCmd(agent, m, worktreePath, yolo, gw, models, extraArgs)
	if err != nil {
		end()
		return remoteExit(modelclient.Still(err, launchStill))
	}
	if rerr := rotation.New().RecordFor(agent, m.ID); rerr != nil {
		// RecordFor names the file that failed.
		fmt.Fprintf(osStderr, "wt: note: %v\n", rerr)
	}
	return runLaunch(cmd, agent, m, cfg, pp, extraArgs, runTail{Remote: true, End: end, PriceNote: remotePriceNoteFunc(ctx, ms)})
}

// gatewayModels is the server's non-native models, which a GatewaySyncer
// (pi's models.json) lists.
func gatewayModels(ctx context.Context, ms *modelSession) ([]config.Model, error) {
	cat, err := ms.client.Catalog(ctx)
	if err != nil {
		return nil, err
	}
	var out []config.Model
	for _, r := range cat.Rows {
		if !r.Native {
			out = append(out, catalog.ToModel(r))
		}
	}
	return out, nil
}

// startRemote runs the start job for row on the server, with progress on
// stderr and the questions answered by --replace, --others and the terminal.
// Ctrl-C detaches (the job keeps running); a second Ctrl-C kills wt.
func startRemote(ctx context.Context, ms *modelSession, row modelapi.Row, others string, allowReplace bool) error {
	sctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-sctx.Done()
		stop() // the default handler again: a second Ctrl-C kills wt
	}()
	ans := &cliAnswerer{
		id: row.ID, replace: allowReplace, others: others, out: osStderr,
		tty: func() (*os.File, error) {
			// A redirected stdin is a script even with a terminal attached
			// (#424): it is never asked.
			if !stdinTTY() {
				return nil, errNoTerminal
			}
			return openTTY()
		},
	}
	_, err := ms.client.RunStart(sctx, modelapi.StartRequest{Model: row.ID, AllowReplace: allowReplace}, ans, startProgressRemote(osStderr, row.ID))
	return err
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
