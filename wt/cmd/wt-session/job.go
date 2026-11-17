package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/exitcode"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
	"github.com/spf13/cobra"
)

const (
	// jobCancelPoll and jobCancelPolls bound how long --cancel waits for the
	// job to end: 60 polls half a second apart.
	jobCancelPoll  = 500 * time.Millisecond
	jobCancelPolls = 60
)

// jobEnvelope is `wt job --json`: the contract document
// docs/contracts/job.sample.json. A job found is Job (a failed or cancelled
// one too: the exit code says it did not succeed); anything that stopped the
// command from reading or cancelling it is Error.
type jobEnvelope struct {
	Version int                   `json:"version"`
	Job     *modelapi.JobStatus   `json:"job,omitempty"`
	Error   *modelapi.ErrorObject `json:"error,omitempty"`
}

func jobCmd(a *app) *cobra.Command {
	var cancel, asJSON bool
	c := &cobra.Command{
		Use:   "job ID",
		Short: "Show or cancel a model server job",
		Long: "Show a job the model server is running or ran (a start or a stop; the id comes\n" +
			"from a detach message or from 'wt start'), or cancel it. The result is one line\n" +
			"on stdout, 'job ID — STATE — STEP'.\n\n" +
			"--cancel asks the server to cancel the job, then waits (up to 30 seconds,\n" +
			"progress on stderr) until it ends and shows how it ended.\n" +
			"--json prints the job (or, when the command fails, the error) as one JSON\n" +
			"document with the same exit code, described by docs/contracts/job.sample.json.\n\n" +
			"Exit 0 for a job that is queued, running or succeeded, 2 for a job that failed\n" +
			"or was cancelled (its problem is the error line) or for a --cancel whose wait\n" +
			"failed after the cancel was sent, 1 when the server does not\n" +
			"know the job (it finished and was dropped from the list, or the server\n" +
			"restarted) or cannot be reached. Needs a model server ([model-server] in\n" +
			"config.toml, or WT_MODEL_URL).",
		Example: "  wt job 7f3a\n  wt job 7f3a --cancel\n  wt job 7f3a --json",
		Args:    cobra.ExactArgs(1),
		// Past the argument check nothing this returns is a usage mistake.
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runJob(cmd.Context(), cmd.OutOrStdout(), a, args[0], cancel, asJSON)
		},
	}
	c.Flags().BoolVar(&cancel, "cancel", false, "cancel the job and wait for it to end")
	c.Flags().BoolVar(&asJSON, "json", false, "print the result as one JSON document (docs/contracts/job.sample.json)")
	return c
}

// jobUnknownError is a cancel of an id the server does not know.
type jobUnknownError struct{ text string }

func (e *jobUnknownError) Error() string { return e.text }

// jobFailure ends `wt job` with err and exit code code: under --json the
// error envelope on out and a quiet error (J4), otherwise the error for main
// to print.
func jobFailure(out io.Writer, asJSON bool, code int, err error) error {
	if !asJSON {
		return exitcode.New(code, err, false)
	}
	c := modelapi.CodeFailed
	var (
		re      *modelapi.ResponseError
		unknown *jobUnknownError
		lost    *modelclient.JobLostError
	)
	switch {
	case errors.As(err, &unknown), errors.As(err, &lost):
		c = modelapi.CodeUnknownJob
	case errors.As(err, &re) && re.Code != "":
		c = re.Code
	}
	if werr := writeJSON(out, jobEnvelope{Version: 1, Error: &modelapi.ErrorObject{Message: err.Error(), Code: c}}); werr != nil {
		return exitcode.New(2, fmt.Errorf("%w; and the error document could not be written: %v", err, werr), false)
	}
	return exitcode.New(code, err, true)
}

func runJob(ctx context.Context, out io.Writer, a *app, id string, cancel, asJSON bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	still := "the job was not read"
	if cancel {
		still = "nothing was cancelled"
	}
	ms, err := a.openModels(ctx, still)
	if err != nil {
		return jobFailure(out, asJSON, 1, err)
	}
	var js modelapi.JobStatus
	if cancel {
		sctx, stop := interruptCtx(ctx)
		defer stop()
		js, err = cancelJob(sctx, ms, id)
	} else {
		js, err = ms.client.Job(ctx, id)
		if modelclient.IsCode(err, modelapi.CodeUnknownJob) {
			err = &modelclient.JobLostError{JobID: id}
		}
	}
	if err != nil {
		return jobFailure(out, asJSON, jobErrorExit(err), err)
	}
	code := 0
	var problem error
	switch js.State {
	case modelapi.JobFailed:
		code = 2
		problem = fmt.Errorf("job %s failed", js.ID)
		if js.Result != nil && js.Result.Error != nil && js.Result.Error.Message != "" {
			problem = errors.New(js.Result.Error.Message)
		}
	case modelapi.JobCancelled:
		code = 2
		problem = fmt.Errorf("job %s was cancelled", js.ID)
	}
	if asJSON {
		if err := writeJSON(out, jobEnvelope{Version: 1, Job: &js}); err != nil {
			return exitcode.New(2, err, false)
		}
		if code != 0 {
			return exitcode.New(code, problem, true)
		}
		return nil
	}
	line := fmt.Sprintf("job %s — %s", js.ID, js.State)
	if js.Step != "" {
		line += " — " + js.Step
	}
	fmt.Fprintln(out, line)
	switch {
	case js.State == modelapi.JobFailed:
		return exitcode.New(code, problem, false)
	case code != 0:
		return exitcode.New(code, problem, true) // cancelled: the line says it
	}
	return nil
}

// jobErrorExit is the exit status of a job command that could not read or
// cancel its job: 1 (could not start), except a wait that ended early, 2.
func jobErrorExit(err error) int {
	if c, ok := exitcode.Code(err); ok {
		return c
	}
	return 1
}

// cancelJob posts the cancel and polls the job to a terminal state, with
// progress on stderr. An id the server does not know says nothing was cancelled.
func cancelJob(ctx context.Context, ms *modelSession, id string) (modelapi.JobStatus, error) {
	if err := ms.client.CancelJob(ctx, id); err != nil {
		if modelclient.IsCode(err, modelapi.CodeUnknownJob) {
			return modelapi.JobStatus{}, &jobUnknownError{fmt.Sprintf("no job %s on the model server — it is unknown, finished and dropped from the list, or the server restarted; nothing was cancelled", cleanup.Flatten(id))}
		}
		return modelapi.JobStatus{}, modelclient.Still(err, "it is not known whether the job was cancelled")
	}
	last := ""
	for n := 0; n < jobCancelPolls; n++ {
		js, err := ms.client.Job(ctx, id)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			// The cancel was sent and what became of it is unknown: not
			// "nothing changed" (1), but 2, as the timeout below.
			return modelapi.JobStatus{}, exitcode.New(2, modelclient.Still(err, fmt.Sprintf("the cancel was sent; check the job with 'wt job %s'", id)), false)
		}
		switch js.State {
		case modelapi.JobSucceeded, modelapi.JobFailed, modelapi.JobCancelled:
			return js, nil
		}
		if p := fmt.Sprintf("%s (%s)", js.State, js.Step); p != last {
			last = p
			fmt.Fprintf(osStderr, "wt: cancelling job %s — %s\n", js.ID, p)
		}
		if err := ms.hooks.sleep(ctx, jobCancelPoll); err != nil {
			break
		}
	}
	return modelapi.JobStatus{}, exitcode.New(2, fmt.Errorf("stopped waiting for job %s to end — the cancel was sent; check the job with 'wt job %s'", id, id), false)
}
