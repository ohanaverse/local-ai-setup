package modelclient

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
)

const (
	pollFast     = 500 * time.Millisecond
	pollFastFor  = 5 * time.Second
	pollSlow     = 2 * time.Second
	defaultGrace = 60 * time.Second
)

// driveOptions are the job driver's clock and limits. now must be monotonic
// (time.Now is): every duration the driver reports or judges comes from it,
// never from a server timestamp.
type driveOptions struct {
	// PollGrace is how long polls may keep failing at the transport (the
	// server unreachable, or a poll hung until the request timeout) before
	// the job is reported lost with the server.
	PollGrace time.Duration
	now       func() time.Time
	sleep     func(context.Context, time.Duration) error
}

// withDriveClock replaces the driver's clock and sleeper for a test.
func withDriveClock(now func() time.Time, sleep func(context.Context, time.Duration) error) Option {
	return func(c *Client) { c.drive.now, c.drive.sleep = now, sleep }
}

func (c *Client) driveOpts() driveOptions {
	d := c.drive
	if d.PollGrace <= 0 {
		d.PollGrace = defaultGrace
	}
	if d.now == nil {
		d.now = time.Now
	}
	if d.sleep == nil {
		d.sleep = c.sleepFor
	}
	return d
}

// Answerer answers the questions a job's problem asks. A false (or, for
// Others, answered=false) is a decline: the run ends as *DeclinedError.
type Answerer interface {
	// Replace asks whether to stop the model(s) occupying the slot.
	Replace(ctx context.Context, p modelapi.Problem) (bool, error)
	// Others asks which other resident models to stop; an empty stop with
	// answered=true keeps them all.
	Others(ctx context.Context, p modelapi.Problem) (stop []string, answered bool, err error)
	// ConfirmInUse asks whether to stop a model other client sessions use.
	ConfirmInUse(ctx context.Context, p modelapi.Problem) (bool, error)
}

// JobView is one poll's view of a job, for progress display.
type JobView struct {
	ID, State, Step string
	Queued          bool
	// Attached is true when this run joined a job another request had
	// already started (the server hands back that job's id).
	Attached bool
	// Elapsed is the client's clock since the run's first poll.
	Elapsed time.Duration
}

// Progress is told each poll's view.
type Progress func(JobView)

// JobError is a job that ended with a problem no question answers.
type JobError struct{ Problem modelapi.Problem }

func (e *JobError) Error() string {
	if m := clean(e.Problem.Message); m != "" {
		return m
	}
	return "the job ended: " + clean(e.Problem.Code)
}

// DetachedError says the caller stopped waiting; the job keeps running.
type DetachedError struct{ JobID string }

func (e *DetachedError) Error() string {
	id := clean(e.JobID)
	return fmt.Sprintf("detached — job %s keeps running on the model server (watch it with 'wt job %s', cancel it with 'wt job %s --cancel')", id, id, id)
}

// LostServerError says the polls failed for longer than the grace.
type LostServerError struct{ JobID, Addr string }

func (e *LostServerError) Error() string {
	id := clean(e.JobID)
	return fmt.Sprintf("lost the model server at %s while job %s was running — the job continues there; check it with 'wt job %s'", clean(e.Addr), id, id)
}

// JobLostError says the server no longer knows the job.
type JobLostError struct{ JobID string }

func (e *JobLostError) Error() string {
	return fmt.Sprintf("the model server no longer knows job %s (it finished and was dropped from its list, or the server restarted) — run 'wt model list --server' to see whether the model came up", clean(e.JobID))
}

// DeclinedError says the user declined a question; What is "started" or
// "stopped".
type DeclinedError struct{ What string }

func (e *DeclinedError) Error() string { return "declined — nothing was " + e.What }

// RunStart submits a start, polls it to the end and answers its questions by
// re-posting. A start for a model whose start job is already active joins that
// job (the server hands back its id; an alias's start joins its local
// target's); a problem that job ends with is asked of
// this caller and answered with this caller's own choices. Cancelling ctx
// while the job runs detaches (*DetachedError); the job is never cancelled.
func (c *Client) RunStart(ctx context.Context, req modelapi.StartRequest, ans Answerer, prog Progress) (modelapi.JobStatus, error) {
	// Every active start job, not only those named by req.Model: the server
	// keys an alias's start by its local target and names the job after it
	// (#596), so a start of alias/x joins the target's job. Joining is the
	// POST handing back an id that was already active.
	jobs, err := c.ActiveJobs(ctx, "")
	if err != nil {
		return modelapi.JobStatus{}, err
	}
	active := map[string]bool{}
	for _, j := range jobs {
		if j.Kind == "start" {
			active[j.ID] = true
		}
	}
	r := c.newRun(prog)
	asked := map[string]bool{} // questions answered in this run
	for {
		id, err := c.SubmitStart(ctx, req)
		if err != nil {
			return modelapi.JobStatus{}, err
		}
		attached := active[id]
		active = nil // a re-post's job is this run's own
		js, err := r.poll(ctx, id, attached)
		if err != nil || js.State == modelapi.JobSucceeded {
			return js, err
		}
		p := problemOf(js)
		switch p.Code {
		case modelapi.CodeOccupied, modelapi.CodeOccupancyUnknown:
			if asked["replace"] || ans == nil {
				return js, &JobError{Problem: p}
			}
			ok, err := ans.Replace(ctx, p)
			if err != nil {
				return js, err
			}
			if !ok {
				return js, &DeclinedError{What: "started"}
			}
			asked["replace"] = true
			req.AllowReplace = true
			if (len(p.Others) > 0 || len(p.OthersUnknown) > 0) && !asked["others"] {
				if err := askOthers(ctx, ans, p, &req); err != nil {
					return js, err
				}
				asked["others"] = true
			}
		case modelapi.CodeOthers:
			if asked["others"] || ans == nil {
				return js, &JobError{Problem: p}
			}
			if err := askOthers(ctx, ans, p, &req); err != nil {
				return js, err
			}
			asked["others"] = true
		default:
			return js, &JobError{Problem: p}
		}
	}
}

func askOthers(ctx context.Context, ans Answerer, p modelapi.Problem, req *modelapi.StartRequest) error {
	stop, answered, err := ans.Others(ctx, p)
	if err != nil {
		return err
	}
	if !answered {
		return &DeclinedError{What: "started"}
	}
	if stop == nil {
		stop = []string{} // "others": {"stop": []} is the answer "keep them"
	}
	req.Others = &modelapi.OthersAnswer{Stop: stop}
	return nil
}

// RunStop submits a stop, polls it to the end and answers an in-use problem
// by re-posting with confirm_in_use. A stop the provider accepted but has not
// finished is a success whose step begins with modelapi.StepStillFinishing.
// Cancelling ctx detaches as RunStart does.
func (c *Client) RunStop(ctx context.Context, req modelapi.StopRequest, ans Answerer, prog Progress) (modelapi.JobStatus, error) {
	r := c.newRun(prog)
	asked := map[string]bool{}
	for {
		id, err := c.SubmitStop(ctx, req)
		if err != nil {
			return modelapi.JobStatus{}, err
		}
		js, err := r.poll(ctx, id, false)
		if err != nil || js.State == modelapi.JobSucceeded {
			return js, err
		}
		p := problemOf(js)
		if p.Code != modelapi.CodeInUse || asked["in_use"] || ans == nil {
			return js, &JobError{Problem: p}
		}
		ok, err := ans.ConfirmInUse(ctx, p)
		if err != nil {
			return js, err
		}
		if !ok {
			return js, &DeclinedError{What: "stopped"}
		}
		asked["in_use"] = true
		req.ConfirmInUse = true
	}
}

// problemOf is a finished, unsuccessful job's problem; a job that ended
// without one gets one from its state.
func problemOf(js modelapi.JobStatus) modelapi.Problem {
	if js.Result != nil && js.Result.Error != nil {
		return *js.Result.Error
	}
	code := modelapi.CodeFailed
	if js.State == modelapi.JobCancelled {
		code = modelapi.CodeCancelled
	}
	return modelapi.Problem{Code: code, Message: fmt.Sprintf("job %s %s", js.ID, js.State)}
}

// run is one RunStart or RunStop: its clock starts at the first poll and
// carries across re-posts.
type run struct {
	c       *Client
	o       driveOptions
	prog    Progress
	started bool
	start   time.Time
}

func (c *Client) newRun(prog Progress) *run {
	if prog == nil {
		prog = func(JobView) {}
	}
	return &run{c: c, o: c.driveOpts(), prog: prog}
}

func (r *run) elapsed() time.Duration {
	if !r.started {
		return 0
	}
	return r.o.now().Sub(r.start)
}

func (r *run) interval() time.Duration {
	if r.elapsed() < pollFastFor {
		return pollFast
	}
	return pollSlow
}

// poll reads job id until it reaches a terminal state. Transport failures
// (UnreachableError, a hung poll included) are retried until they have
// lasted PollGrace on the client's clock; a cancelled ctx at any point is a
// detach.
func (r *run) poll(ctx context.Context, id string, attached bool) (modelapi.JobStatus, error) {
	var outageFrom time.Time
	inOutage := false
	for {
		if ctx.Err() != nil {
			return modelapi.JobStatus{}, &DetachedError{JobID: id}
		}
		t0 := r.o.now()
		if !r.started {
			r.started, r.start = true, t0
		}
		js, err := r.c.Job(ctx, id)
		if ctx.Err() != nil {
			return modelapi.JobStatus{}, &DetachedError{JobID: id}
		}
		if err != nil {
			if IsCode(err, modelapi.CodeUnknownJob) {
				return modelapi.JobStatus{}, &JobLostError{JobID: id}
			}
			var ue *UnreachableError
			if !errors.As(err, &ue) {
				return modelapi.JobStatus{}, err
			}
			if !inOutage {
				inOutage, outageFrom = true, t0
			}
			if r.o.now().Sub(outageFrom) >= r.o.PollGrace {
				return modelapi.JobStatus{}, &LostServerError{JobID: id, Addr: r.c.addr()}
			}
		} else {
			inOutage = false
			r.prog(JobView{ID: js.ID, State: js.State, Step: js.Step, Queued: js.State == modelapi.JobQueued, Attached: attached, Elapsed: r.elapsed()})
			switch js.State {
			case modelapi.JobSucceeded, modelapi.JobFailed, modelapi.JobCancelled:
				return js, nil
			}
		}
		if err := r.o.sleep(ctx, r.interval()); err != nil || ctx.Err() != nil {
			return modelapi.JobStatus{}, &DetachedError{JobID: id}
		}
	}
}

// QueuedDetail is the progress detail of a job waiting its turn behind
// another start or stop; the non-TUI start and the picker word it alike.
const QueuedDetail = "queued behind another start or stop on the model server"
