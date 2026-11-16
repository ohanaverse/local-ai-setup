package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/exitcode"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
	"github.com/ohanaverse/local-ai-setup/wt/internal/survey"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// stopSelectIn is where the stop picker reads its answer: stdin, a seam for
// tests.
var stopSelectIn = func() io.Reader { return os.Stdin }

// interruptCtx is ctx that Ctrl-C or SIGTERM cancels once: after the first,
// the default handler is back, so a second Ctrl-C kills wt. The returned stop
// releases the handler.
func interruptCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	sctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sctx.Done()
		stop()
	}()
	return sctx, stop
}

// stillUnlessTyped adds what is still true to a failure that came before any
// job outcome. An error that already says how the job ended (a failed,
// detached, lost or declined one) or carries an exit code is left alone.
func stillUnlessTyped(err error, still string) error {
	var (
		declined *modelclient.DeclinedError
		job      *modelclient.JobError
		lost     *modelclient.LostServerError
		jobLost  *modelclient.JobLostError
		detached *modelclient.DetachedError
	)
	if _, coded := exitcode.Code(err); coded ||
		errors.As(err, &declined) || errors.As(err, &job) || errors.As(err, &lost) ||
		errors.As(err, &jobLost) || errors.As(err, &detached) {
		return err
	}
	return modelclient.Still(err, still)
}

// interactiveTTY opens the terminal a question is asked on (P3): none when
// stdin is redirected, a script is never asked (#424).
func interactiveTTY() (*os.File, error) {
	if !stdinTTY() {
		return nil, errNoTerminal
	}
	return openTTY()
}

// localRowsOf is the catalog's local rows, in the server's order.
func localRowsOf(rows []modelapi.Row) []modelapi.Row {
	var out []modelapi.Row
	for _, r := range rows {
		if r.Location == string(config.LocationLocal) {
			out = append(out, r)
		}
	}
	slices.SortStableFunc(out, func(x, y modelapi.Row) int { return x.Order - y.Order })
	return out
}

// runStartRemote is `wt start` on the model server (local rows only: a cloud
// row's start is the server's to refuse, with its failed text). With no id it
// opens the start picker over the catalog it fetched (a terminal is needed
// first, P5). A running local model the gateway lists, or whose route the
// server could not read (R7), is reported ("ID is already running") and left
// alone. Otherwise, in order (R14):
//
//  1. register a client session with the model, so the server's idle scan
//     counts it from before the start (a failed register is one note and the
//     start goes on);
//  2. run the start job, answering its questions with ans; a second start for
//     a model with a start job running attaches to that job;
//  3. end the client session on every exit, a detach and a failure included;
//  4. after a success, print the stderr note that the model server stops the
//     model after its idle grace unless a client session uses it.
//
// Ctrl-C during the job detaches (exit 2; the job keeps running); a declined
// question exits 3.
func runStartRemote(ctx context.Context, out io.Writer, ms *modelSession, theme themes.Theme, id string, ans modelclient.Answerer) error {
	const still = "nothing was started"
	if id == "" && !stdinTTY() {
		return exitcode.New(1, errors.New("the model picker needs a TTY; pass a model id (wt start ID) to start without it"), false)
	}
	cat, err := ms.client.Catalog(ctx)
	if err != nil {
		return remoteExit(modelclient.Still(err, still))
	}
	if id == "" {
		local := localRowsOf(cat.Rows)
		if len(local) == 0 {
			return exitcode.New(1, errors.New("the model server lists no local model; nothing was started"), false)
		}
		models := make([]config.Model, len(local))
		for i, r := range local {
			models[i] = catalog.ToModel(r)
		}
		m, ok, perr := pickStartModelTUI(nil, models, theme)
		if perr != nil {
			return exitcode.New(1, perr, false)
		}
		if !ok {
			return exitcode.New(1, errors.New("model selection canceled"), false)
		}
		id = m.ID
	}
	for _, r := range cat.Rows {
		if r.ID == id && r.Location == string(config.LocationLocal) && r.State == modelapi.StateRunning && !catalog.NeedsStart(r) {
			fmt.Fprintf(out, "%s is already running\n", cleanup.Flatten(id))
			return nil
		}
	}
	if s, ok := ans.(interface{ setModel(string) }); ok {
		s.setModel(id)
	}

	note := func(s string) { fmt.Fprintf(osStderr, "wt: note: %s\n", s) }
	sess := ms.client.NewSession([]string{id}, modelclient.SessionOptions{Interval: remoteBeatInterval, Note: note})
	registered := true
	if rerr := sess.Start(ctx); rerr != nil {
		registered = !registerRefused(rerr)
		note(fmt.Sprintf("the model server did not register this client session (%s) — the model starts, but the server may stop %s once it is idle", rerr, id))
	}
	end := func() {
		if eerr := sess.End(context.WithoutCancel(ctx)); eerr != nil && registered {
			note(fmt.Sprintf("could not end the client session on the model server (%s) — it forgets it within a minute", eerr))
		}
	}

	sctx, stop := interruptCtx(ctx)
	defer stop()
	_, err = ms.client.RunStart(sctx, modelapi.StartRequest{Model: id}, ans, startProgressRemote(osStderr, id))
	end()
	if err != nil {
		return remoteExit(stillUnlessTyped(err, still))
	}
	fmt.Fprintf(out, "%s is running\n", cleanup.Flatten(id))
	note(fmt.Sprintf("%s is up; the model server stops it after its idle grace unless a client session uses it", cleanup.Flatten(id)))
	return nil
}

// stopAnswerer answers a stop job's one question, whether to stop a model
// other client sessions use: --yes answers it, a terminal is asked (default
// No), and with neither it is approval missing (P4, exit 3).
type stopAnswerer struct {
	id  string
	yes bool
	tty func() (*os.File, error)
}

func (a *stopAnswerer) Replace(context.Context, modelapi.Problem) (bool, error) {
	return false, errors.New("the model server asked to replace a model while stopping one")
}

func (a *stopAnswerer) Others(context.Context, modelapi.Problem) ([]string, bool, error) {
	return nil, false, errors.New("the model server asked about other models while stopping one")
}

// ConfirmInUse asks whether to stop a model other client sessions use.
func (a *stopAnswerer) ConfirmInUse(ctx context.Context, p modelapi.Problem) (bool, error) {
	if a.yes {
		return true, nil
	}
	what := p.Message
	if n := len(p.Sessions); n > 0 {
		what = fmt.Sprintf("%d client session(s) use %s", n, a.id)
	}
	f, err := a.tty()
	if err != nil {
		why := errNoTerminal.Error()
		if !errors.Is(err, errNoTerminal) {
			why = "could not open the terminal: " + err.Error()
		}
		return false, exitcode.New(exitApprovalMissing,
			fmt.Errorf("approval missing: %s; pass --yes to stop it anyway (%s); nothing was stopped", what, why), false)
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "%s; stop it anyway? [y/N] ", what); err != nil {
		return false, err
	}
	return askOnTTY(ctx, f, askYesNo)
}

// runStopRemote is `wt stop` on the model server, for a model id only: a
// provider name and --all stay on the host path (R3). With no id it opens a
// numbered picker over the running local rows (a terminal is needed first,
// P5), marking each a client session uses. The stop request carries no client
// session (this process registers none); --yes answers the in-use question.
// A stop the provider accepted but has not finished is a success.
func runStopRemote(ctx context.Context, out io.Writer, ms *modelSession, arg string, yes bool) error {
	const still = "nothing was stopped"
	ids := []string{arg}
	if arg == "" {
		if !stdinTTY() {
			return exitcode.New(1, errors.New("the stop picker needs a TTY; pass a model id (wt stop ID) to stop without it"), false)
		}
		cat, err := ms.client.Catalog(ctx)
		if err != nil {
			return remoteExit(modelclient.Still(err, still))
		}
		var running []modelapi.Row
		var labels []string
		for _, r := range localRowsOf(cat.Rows) {
			if r.State != modelapi.StateRunning {
				continue
			}
			label := r.ID
			if r.Sessions > 0 {
				label += fmt.Sprintf(" (%d client session(s))", r.Sessions)
			}
			running = append(running, r)
			labels = append(labels, label)
		}
		if len(running) == 0 {
			fmt.Fprintln(out, "no running local models on the model server")
			return nil
		}
		sctx, stop := interruptCtx(ctx)
		defer stop()
		picked, cancelled := survey.Select(sctx, stopSelectIn(), out, "Stop running local models? (client session(s) = other clients using it)", labels)
		if cancelled || len(picked) == 0 {
			return nil
		}
		ids = ids[:0]
		for _, i := range picked {
			ids = append(ids, running[i].ID)
		}
	}
	// A batch prints each failed model's reason once, as it happens; the one
	// error at the end counts them and does not repeat a reason. A detach ends
	// the batch: the models after it were never asked, which is not a failure.
	failed, declinedN := 0, 0
	for i, id := range ids {
		err := stopOneRemote(ctx, out, ms, id, yes)
		if err == nil {
			continue
		}
		if len(ids) == 1 {
			return err
		}
		fmt.Fprintf(osStderr, "wt: %s: %v\n", cleanup.Flatten(id), err)
		failed++
		var det *modelclient.DetachedError
		if exitcode.Of(err) == exitApprovalMissing {
			declinedN++ // declined, or approval missing: nothing changed
		}
		if errors.As(err, &det) {
			rest := len(ids) - i - 1
			msg := fmt.Sprintf("%d of %d stops failed", failed, len(ids))
			if rest > 0 {
				if rest == 1 {
					msg += "; 1 was not stopped"
				} else {
					msg += fmt.Sprintf("; %d were not stopped", rest)
				}
			}
			return exitcode.New(2, errors.New(msg), false)
		}
	}
	if failed > 0 {
		code := 2
		if declinedN == failed {
			code = exitApprovalMissing // nothing changed: every stop was declined
		}
		return exitcode.New(code, fmt.Errorf("%d of %d stops failed", failed, len(ids)), false)
	}
	return nil
}

// stopOneRemote runs one stop job and prints its result on out.
func stopOneRemote(ctx context.Context, out io.Writer, ms *modelSession, id string, yes bool) error {
	sctx, stop := interruptCtx(ctx)
	defer stop()
	ans := &stopAnswerer{id: id, yes: yes, tty: interactiveTTY}
	js, err := ms.client.RunStop(sctx, modelapi.StopRequest{Model: id}, ans, jobProgressRemote(osStderr, "stopping", id))
	if err != nil {
		return remoteExit(stillUnlessTyped(err, "nothing was stopped"))
	}
	if strings.HasPrefix(js.Step, modelapi.StepStillFinishing) {
		fmt.Fprintf(out, "%s — %s\n", cleanup.Flatten(id), js.Step)
		return nil
	}
	fmt.Fprintf(out, "%s stopped\n", cleanup.Flatten(id))
	return nil
}
