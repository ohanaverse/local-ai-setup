package modelclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
)

// fakeClock is the driver's injected clock and sleeper: sleeping advances it,
// so a poll schedule of minutes runs in microseconds of wall time.
type fakeClock struct {
	mu      sync.Mutex
	t       time.Time
	slept   []time.Duration
	still   bool                // sleeping records but does not advance
	onSleep func(time.Duration) // called after each sleep, outside the lock
}

func newFakeClock() *fakeClock { return &fakeClock{t: time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)} }

func (f *fakeClock) now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

func (f *fakeClock) advance(d time.Duration) {
	f.mu.Lock()
	f.t = f.t.Add(d)
	f.mu.Unlock()
}

func (f *fakeClock) sleep(ctx context.Context, d time.Duration) error {
	f.mu.Lock()
	f.slept = append(f.slept, d)
	if !f.still {
		f.t = f.t.Add(d)
	}
	hook := f.onSleep
	f.mu.Unlock()
	if hook != nil {
		hook(d)
	}
	return ctx.Err()
}

// jobFake is a model server with start, stop, job and cancel routes whose
// answers each test scripts.
type jobFake struct {
	*fixture
	mu       sync.Mutex
	starts   []modelapi.StartRequest
	rawStart []string
	stops    []modelapi.StopRequest
	cancels  int
	polls    int
	active   []modelapi.JobStatus
	// submit returns the job id for the n-th (0-based) submission.
	submit func(n int) string
	// job answers the n-th (0-based, over all jobs) poll of id; nil status
	// means the handler already wrote the response.
	job func(w http.ResponseWriter, r *http.Request, id string, n int) *modelapi.JobStatus
}

func newJobFake(t *testing.T) *jobFake {
	t.Helper()
	jf := &jobFake{fixture: newFixture(t)}
	jf.submit = func(n int) string { return "j" + strconv.Itoa(n+1) }
	post := func(w http.ResponseWriter, r *http.Request, stop bool) {
		b, _ := io.ReadAll(r.Body)
		jf.mu.Lock()
		var n int
		if stop {
			var req modelapi.StopRequest
			_ = json.Unmarshal(b, &req)
			jf.stops = append(jf.stops, req)
			n = len(jf.stops) - 1
		} else {
			var req modelapi.StartRequest
			_ = json.Unmarshal(b, &req)
			jf.starts = append(jf.starts, req)
			jf.rawStart = append(jf.rawStart, string(b))
			n = len(jf.starts) - 1
		}
		id := jf.submit(n)
		jf.mu.Unlock()
		writeJSONBody(w, 202, modelapi.JobRef{ID: id})
	}
	jf.mux.HandleFunc("POST /control/v1/start", func(w http.ResponseWriter, r *http.Request) { post(w, r, false) })
	jf.mux.HandleFunc("POST /control/v1/stop", func(w http.ResponseWriter, r *http.Request) { post(w, r, true) })
	jf.mux.HandleFunc("GET /control/v1/jobs", func(w http.ResponseWriter, r *http.Request) {
		// As the server's Active: a model filter keeps the jobs named by it.
		model := r.URL.Query().Get("model")
		jf.mu.Lock()
		var jobs []modelapi.JobStatus
		for _, j := range jf.active {
			if model == "" || j.Model == model {
				jobs = append(jobs, j)
			}
		}
		jf.mu.Unlock()
		writeJSONBody(w, 200, modelapi.JobList{Jobs: jobs})
	})
	jf.mux.HandleFunc("GET /control/v1/jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		jf.mu.Lock()
		n := jf.polls
		jf.polls++
		fn := jf.job
		jf.mu.Unlock()
		if js := fn(w, r, r.PathValue("id"), n); js != nil {
			writeJSONBody(w, 200, js)
		}
	})
	jf.mux.HandleFunc("POST /control/v1/jobs/{id}/cancel", func(w http.ResponseWriter, _ *http.Request) {
		jf.mu.Lock()
		jf.cancels++
		jf.mu.Unlock()
		w.WriteHeader(204)
	})
	return jf
}

func (jf *jobFake) driver(fc *fakeClock, opts ...Option) *Client {
	return jf.client(append([]Option{withDriveClock(fc.now, fc.sleep)}, opts...)...)
}

func (jf *jobFake) counts() (starts, stops, cancels int) {
	jf.mu.Lock()
	defer jf.mu.Unlock()
	return len(jf.starts), len(jf.stops), jf.cancels
}

func done(id string) *modelapi.JobStatus {
	return &modelapi.JobStatus{ID: id, Kind: "start", Model: "omlx/m", State: modelapi.JobSucceeded, Step: "ready", Result: &modelapi.JobResult{OK: true}}
}

func running(id string) *modelapi.JobStatus {
	return &modelapi.JobStatus{ID: id, Kind: "start", Model: "omlx/m", State: modelapi.JobRunning, Step: "warming the model"}
}

func failedWith(id string, p modelapi.Problem) *modelapi.JobStatus {
	return &modelapi.JobStatus{ID: id, Kind: "start", Model: "omlx/m", State: modelapi.JobFailed, Result: &modelapi.JobResult{Error: &p}}
}

// cut drops the connection without an answer: a transport failure.
func cut(w http.ResponseWriter) {
	conn, _, err := w.(http.Hijacker).Hijack()
	if err == nil {
		conn.Close()
	}
}

// fakeAnswerer records each question and answers from its fields.
type fakeAnswerer struct {
	mu                       sync.Mutex
	replace, others, confirm int
	replaceOK, confirmOK     bool
	othersOK                 bool
	stop                     []string
	lastReplace              modelapi.Problem
}

func (a *fakeAnswerer) Replace(_ context.Context, p modelapi.Problem) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.replace++
	a.lastReplace = p
	return a.replaceOK, nil
}

func (a *fakeAnswerer) Others(context.Context, modelapi.Problem) ([]string, bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.others++
	return a.stop, a.othersOK, nil
}

func (a *fakeAnswerer) ConfirmInUse(context.Context, modelapi.Problem) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.confirm++
	return a.confirmOK, nil
}

// views collects what Progress was told.
type views struct {
	mu sync.Mutex
	vs []JobView
}

func (v *views) prog(jv JobView) {
	v.mu.Lock()
	v.vs = append(v.vs, jv)
	v.mu.Unlock()
}

func (v *views) all() []JobView {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]JobView{}, v.vs...)
}

var occupiedProblem = modelapi.Problem{Code: modelapi.CodeOccupied, Message: "omlx/x is running", Occupants: []string{"omlx/x"}}

// TestRunStartAnswersReplaceThenSucceeds pins the replace flow: an occupied
// first job asks Replace, and the re-post carries allow_replace.
func TestRunStartAnswersReplaceThenSucceeds(t *testing.T) {
	jf := newJobFake(t)
	jf.job = func(_ http.ResponseWriter, _ *http.Request, id string, _ int) *modelapi.JobStatus {
		if id == "j1" {
			return failedWith(id, occupiedProblem)
		}
		return done(id)
	}
	ans := &fakeAnswerer{replaceOK: true}
	js, err := jf.driver(newFakeClock()).RunStart(ctx(), modelapi.StartRequest{Model: "omlx/m"}, ans, nil)
	if err != nil || js.ID != "j2" || js.State != modelapi.JobSucceeded {
		t.Fatalf("js=%+v err=%v", js, err)
	}
	if ans.replace != 1 || len(jf.starts) != 2 || jf.starts[0].AllowReplace || !jf.starts[1].AllowReplace {
		t.Fatalf("replace asked %d, starts %+v", ans.replace, jf.starts)
	}
	if ans.lastReplace.Code != modelapi.CodeOccupied || len(ans.lastReplace.Occupants) != 1 {
		t.Errorf("Replace saw %+v", ans.lastReplace)
	}
}

// TestRunStartDeclinedReturnsDeclinedError pins that a declined replace is
// *DeclinedError with the exact text, and nothing is re-posted.
func TestRunStartDeclinedReturnsDeclinedError(t *testing.T) {
	jf := newJobFake(t)
	jf.job = func(_ http.ResponseWriter, _ *http.Request, id string, _ int) *modelapi.JobStatus {
		return failedWith(id, occupiedProblem)
	}
	_, err := jf.driver(newFakeClock()).RunStart(ctx(), modelapi.StartRequest{Model: "omlx/m"}, &fakeAnswerer{}, nil)
	var de *DeclinedError
	if !errors.As(err, &de) || de.What != "started" || err.Error() != "declined — nothing was started" {
		t.Fatalf("err = %v", err)
	}
	if s, _, _ := jf.counts(); s != 1 {
		t.Errorf("starts posted %d, want 1 (no re-post)", s)
	}
}

// TestRunStartAsksBothQuestionsOnce pins that an occupied problem that also
// names other residents asks both questions in one step and re-posts once
// with both answers; and that an others-only problem answered with nothing to
// stop still sends a non-nil others object (the answer "keep them").
func TestRunStartAsksBothQuestionsOnce(t *testing.T) {
	jf := newJobFake(t)
	p := occupiedProblem
	p.Others = []modelapi.Resident{{ID: "ollama/y", Label: "ollama/y", Stoppable: true, Figure: "12 GB"}}
	jf.job = func(_ http.ResponseWriter, _ *http.Request, id string, _ int) *modelapi.JobStatus {
		if id == "j1" {
			return failedWith(id, p)
		}
		return done(id)
	}
	ans := &fakeAnswerer{replaceOK: true, othersOK: true, stop: []string{"ollama/y"}}
	if _, err := jf.driver(newFakeClock()).RunStart(ctx(), modelapi.StartRequest{Model: "omlx/m"}, ans, nil); err != nil {
		t.Fatal(err)
	}
	if ans.replace != 1 || ans.others != 1 || len(jf.starts) != 2 {
		t.Fatalf("replace %d others %d starts %d", ans.replace, ans.others, len(jf.starts))
	}
	re := jf.starts[1]
	if !re.AllowReplace || re.Others == nil || len(re.Others.Stop) != 1 || re.Others.Stop[0] != "ollama/y" {
		t.Fatalf("re-post = %+v", re)
	}

	// others alone, nothing to stop
	jf2 := newJobFake(t)
	jf2.job = func(_ http.ResponseWriter, _ *http.Request, id string, _ int) *modelapi.JobStatus {
		if id == "j1" {
			return failedWith(id, modelapi.Problem{Code: modelapi.CodeOthers, Message: "others", Others: p.Others})
		}
		return done(id)
	}
	ans2 := &fakeAnswerer{othersOK: true}
	if _, err := jf2.driver(newFakeClock()).RunStart(ctx(), modelapi.StartRequest{Model: "omlx/m"}, ans2, nil); err != nil {
		t.Fatal(err)
	}
	if ans2.replace != 0 || ans2.others != 1 || len(jf2.rawStart) != 2 {
		t.Fatalf("replace %d others %d starts %d", ans2.replace, ans2.others, len(jf2.rawStart))
	}
	if !strings.Contains(jf2.rawStart[1], `"others":{"stop":[]}`) || jf2.starts[1].AllowReplace {
		t.Errorf("re-post body = %s", jf2.rawStart[1])
	}
}

// TestRunStartNeverAsksTheSameQuestionTwice pins that a server that keeps
// answering occupied after the user agreed ends as *JobError, not a second
// prompt and an endless loop.
func TestRunStartNeverAsksTheSameQuestionTwice(t *testing.T) {
	jf := newJobFake(t)
	jf.job = func(_ http.ResponseWriter, _ *http.Request, id string, _ int) *modelapi.JobStatus {
		return failedWith(id, occupiedProblem)
	}
	ans := &fakeAnswerer{replaceOK: true}
	_, err := jf.driver(newFakeClock()).RunStart(ctx(), modelapi.StartRequest{Model: "omlx/m"}, ans, nil)
	var je *JobError
	if !errors.As(err, &je) || je.Problem.Code != modelapi.CodeOccupied || err.Error() != "omlx/x is running" {
		t.Fatalf("err = %v", err)
	}
	if ans.replace != 1 || len(jf.starts) != 2 {
		t.Errorf("replace %d starts %d", ans.replace, len(jf.starts))
	}
}

// TestRunStartAttachesToAnActiveJob pins that a start for a model with an
// active start job still posts its own start (the server joins it and hands
// back the same id) and tells Progress it attached.
func TestRunStartAttachesToAnActiveJob(t *testing.T) {
	jf := newJobFake(t)
	jf.active = []modelapi.JobStatus{*running("j9")}
	jf.submit = func(int) string { return "j9" }
	jf.job = func(_ http.ResponseWriter, _ *http.Request, id string, n int) *modelapi.JobStatus {
		if n < 2 {
			return running(id)
		}
		return done(id)
	}
	var v views
	js, err := jf.driver(newFakeClock()).RunStart(ctx(), modelapi.StartRequest{Model: "omlx/m"}, &fakeAnswerer{}, v.prog)
	if err != nil || js.ID != "j9" {
		t.Fatalf("js=%+v err=%v", js, err)
	}
	if s, _, _ := jf.counts(); s != 1 {
		t.Errorf("starts posted %d, want 1", s)
	}
	vs := v.all()
	if len(vs) == 0 || !vs[0].Attached || vs[0].ID != "j9" {
		t.Errorf("views = %+v, want the first attached to j9", vs)
	}

	// no active job: not attached
	jf2 := newJobFake(t)
	jf2.job = func(_ http.ResponseWriter, _ *http.Request, id string, _ int) *modelapi.JobStatus { return done(id) }
	var v2 views
	if _, err := jf2.driver(newFakeClock()).RunStart(ctx(), modelapi.StartRequest{Model: "omlx/m"}, &fakeAnswerer{}, v2.prog); err != nil {
		t.Fatal(err)
	}
	if vs := v2.all(); len(vs) == 0 || vs[0].Attached {
		t.Errorf("views = %+v, want not attached", vs)
	}
}

// TestRunStartMarksAnAliasJoinAttached pins that a start of an alias whose
// local target already has a start job running is reported as joined: the
// server keys and names an alias's start job by its target (startKey), so
// the POST of alias/x hands back the target's job. Matching active jobs by
// the requested id alone missed it, and `wt start alias/x` said "started"
// for a start another client began. A start whose POST makes a new job is
// still not attached, even while another model's start job runs.
func TestRunStartMarksAnAliasJoinAttached(t *testing.T) {
	jf := newJobFake(t)
	target := running("j9")
	target.Model = "omlx/target"
	jf.active = []modelapi.JobStatus{*target}
	jf.submit = func(int) string { return "j9" }
	jf.job = func(_ http.ResponseWriter, _ *http.Request, id string, n int) *modelapi.JobStatus {
		if n < 1 {
			return running(id)
		}
		return done(id)
	}
	var v views
	if _, err := jf.driver(newFakeClock()).RunStart(ctx(), modelapi.StartRequest{Model: "alias/x"}, &fakeAnswerer{}, v.prog); err != nil {
		t.Fatal(err)
	}
	if vs := v.all(); len(vs) == 0 || !vs[0].Attached || vs[0].ID != "j9" {
		t.Errorf("views = %+v, want the first attached to j9", vs)
	}

	// Another model's job is running, and this POST starts a new one.
	jf2 := newJobFake(t)
	jf2.active = []modelapi.JobStatus{*target}
	jf2.submit = func(int) string { return "j10" }
	jf2.job = func(_ http.ResponseWriter, _ *http.Request, id string, _ int) *modelapi.JobStatus { return done(id) }
	var v2 views
	if _, err := jf2.driver(newFakeClock()).RunStart(ctx(), modelapi.StartRequest{Model: "omlx/other"}, &fakeAnswerer{}, v2.prog); err != nil {
		t.Fatal(err)
	}
	if vs := v2.all(); len(vs) == 0 || vs[0].Attached {
		t.Errorf("views = %+v, want not attached", vs)
	}
}

// TestAttachedJobProblemIsAskedOfTheAttacher pins that when the job this
// client joined ends occupied, this client's own Replace is asked and its own
// answer goes into the re-post (the first requester's flags do not carry). A
// declining attacher ends as *DeclinedError with only the one start posted.
func TestAttachedJobProblemIsAskedOfTheAttacher(t *testing.T) {
	jf := newJobFake(t)
	jf.active = []modelapi.JobStatus{*running("j9")}
	jf.submit = func(n int) string {
		if n == 0 {
			return "j9"
		}
		return "j10"
	}
	jf.job = func(_ http.ResponseWriter, _ *http.Request, id string, _ int) *modelapi.JobStatus {
		if id == "j9" {
			return failedWith(id, occupiedProblem)
		}
		return done(id)
	}
	decline := &fakeAnswerer{replaceOK: false}
	_, err := jf.driver(newFakeClock()).RunStart(ctx(), modelapi.StartRequest{Model: "omlx/m"}, decline, nil)
	var de *DeclinedError
	if !errors.As(err, &de) || decline.replace != 1 || len(jf.starts) != 1 {
		t.Fatalf("decline: err=%v replace=%d starts=%d", err, decline.replace, len(jf.starts))
	}
	jf.starts = nil
	ans := &fakeAnswerer{replaceOK: true}
	js, err := jf.driver(newFakeClock()).RunStart(ctx(), modelapi.StartRequest{Model: "omlx/m"}, ans, nil)
	if err != nil || js.ID != "j10" {
		t.Fatalf("js=%+v err=%v", js, err)
	}
	if ans.replace != 1 || len(jf.starts) != 2 || jf.starts[0].AllowReplace || !jf.starts[1].AllowReplace {
		t.Fatalf("replace %d starts %+v", ans.replace, jf.starts)
	}
}

// TestDetachLeavesTheJobRunning pins that cancelling the caller's context
// mid-poll detaches: *DetachedError with the exact text, and no cancel is
// sent (the job is the server's to finish).
func TestDetachLeavesTheJobRunning(t *testing.T) {
	jf := newJobFake(t)
	c, cancel := context.WithCancel(context.Background())
	defer cancel()
	jf.job = func(_ http.ResponseWriter, _ *http.Request, id string, n int) *modelapi.JobStatus {
		if n == 2 {
			cancel()
		}
		return running(id)
	}
	_, err := jf.driver(newFakeClock()).RunStart(c, modelapi.StartRequest{Model: "omlx/m"}, &fakeAnswerer{}, nil)
	var de *DetachedError
	want := "detached — job j1 keeps running on the model server (watch it with 'wt job j1', cancel it with 'wt job j1 --cancel')"
	if !errors.As(err, &de) || de.JobID != "j1" || err.Error() != want {
		t.Fatalf("err = %v", err)
	}
	if _, _, cancels := jf.counts(); cancels != 0 {
		t.Errorf("cancel sent %d times", cancels)
	}
}

// TestDetachDuringOutageRetry pins that a context cancelled while the poll is
// retrying through an outage is a detach, not a lost server.
func TestDetachDuringOutageRetry(t *testing.T) {
	jf := newJobFake(t)
	c, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	failures := 0
	jf.job = func(w http.ResponseWriter, _ *http.Request, id string, n int) *modelapi.JobStatus {
		if n == 0 {
			return running(id)
		}
		mu.Lock()
		failures++
		mu.Unlock()
		cut(w)
		return nil
	}
	fc := newFakeClock()
	fc.onSleep = func(time.Duration) {
		mu.Lock()
		f := failures
		mu.Unlock()
		if f >= 3 {
			cancel()
		}
	}
	_, err := jf.driver(fc).RunStart(c, modelapi.StartRequest{Model: "omlx/m"}, &fakeAnswerer{}, nil)
	var de *DetachedError
	if !errors.As(err, &de) {
		t.Fatalf("err = %v, want *DetachedError", err)
	}
	if _, _, cancels := jf.counts(); cancels != 0 {
		t.Errorf("cancel sent %d times", cancels)
	}
}

// TestPollSurvivesBriefOutageThenLosesServer pins the 60 s poll grace on the
// injected clock: a 59 s outage recovers and the job's result comes back; a
// 61 s outage is *LostServerError with the exact text.
func TestPollSurvivesBriefOutageThenLosesServer(t *testing.T) {
	for _, tc := range []struct {
		outage time.Duration
		lost   bool
	}{{59 * time.Second, false}, {61 * time.Second, true}} {
		jf := newJobFake(t)
		fc := newFakeClock()
		start := fc.now()
		var failFrom time.Time
		jf.job = func(w http.ResponseWriter, _ *http.Request, id string, _ int) *modelapi.JobStatus {
			now := fc.now()
			if failFrom.IsZero() && now.Sub(start) >= 6*time.Second {
				failFrom = now
			}
			if !failFrom.IsZero() && now.Before(failFrom.Add(tc.outage)) {
				cut(w)
				return nil
			}
			if !failFrom.IsZero() {
				return done(id)
			}
			return running(id)
		}
		js, err := jf.driver(fc).RunStart(ctx(), modelapi.StartRequest{Model: "omlx/m"}, &fakeAnswerer{}, nil)
		if !tc.lost {
			if err != nil || js.State != modelapi.JobSucceeded {
				t.Errorf("%v outage: js=%+v err=%v, want recovery", tc.outage, js, err)
			}
			continue
		}
		var le *LostServerError
		want := "lost the model server at " + addrOf(jf.fixture) + " while job j1 was running — the job continues there; check it with 'wt job j1'"
		if !errors.As(err, &le) || err.Error() != want {
			t.Errorf("%v outage: err = %v, want %q", tc.outage, err, want)
		}
	}
}

// TestPollCountsFailedPollsTowardPollGrace pins that a poll that fails after
// a long time in flight counts that time as outage time on the client's
// clock: the sleeper here never advances the clock, so only the failing
// requests' (fake) duration can reach the grace. It covers the transport
// failure path only; a poll that expires by its own timeout is not driven
// here (that needs a real timer, which this test avoids on purpose).
//
// The duration is fake on purpose and the failure is a cut connection, not a
// real hang cut off by a short WithRequestTimeout: that timeout is the
// client's one bound for every call, the handshake ping and the start POST
// included, so a 30 ms one failed those on a loaded machine (a stall of 30 ms
// is enough) with an *UnreachableError out of RunStart before any poll ran.
// With no real timer in the test, nothing it asserts depends on the machine's
// speed.
func TestPollCountsFailedPollsTowardPollGrace(t *testing.T) {
	jf := newJobFake(t)
	fc := newFakeClock()
	fc.still = true
	var mu sync.Mutex
	failed := 0
	jf.job = func(w http.ResponseWriter, _ *http.Request, id string, n int) *modelapi.JobStatus {
		if n == 0 {
			return running(id)
		}
		mu.Lock()
		failed++
		h := failed
		mu.Unlock()
		if h > 50 { // a driver that ignores failed-poll time would spin; end it
			return done(id)
		}
		fc.advance(10 * time.Second) // each handler call advances the fake clock 10 s...
		cut(w)                       // ...then fails (the transport may retry a cut GET, so a poll can call this twice)
		return nil
	}
	_, err := jf.driver(fc).RunStart(ctx(), modelapi.StartRequest{Model: "omlx/m"}, &fakeAnswerer{}, nil)
	var le *LostServerError
	if !errors.As(err, &le) || le.JobID != "j1" {
		t.Fatalf("err = %v, want *LostServerError", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if failed > 20 {
		t.Errorf("%d failed polls before giving up", failed)
	}
}

// TestUnknownJobIsNotResubmitted pins that a job the server no longer knows
// is *JobLostError with the exact text and no second start is posted.
func TestUnknownJobIsNotResubmitted(t *testing.T) {
	jf := newJobFake(t)
	jf.job = func(w http.ResponseWriter, _ *http.Request, _ string, _ int) *modelapi.JobStatus {
		apiErr(w, 404, modelapi.CodeUnknownJob, "no such job")
		return nil
	}
	_, err := jf.driver(newFakeClock()).RunStart(ctx(), modelapi.StartRequest{Model: "omlx/m"}, &fakeAnswerer{}, nil)
	var jl *JobLostError
	want := "the model server no longer knows job j1 (it finished and was dropped from its list, or the server restarted) — run 'wt model list --server' to see whether the model came up"
	if !errors.As(err, &jl) || err.Error() != want {
		t.Fatalf("err = %v", err)
	}
	if s, _, _ := jf.counts(); s != 1 {
		t.Errorf("starts posted %d", s)
	}
}

// TestQueuedStateShowsQueued pins that a queued job reaches Progress as
// Queued, and a running one not.
func TestQueuedStateShowsQueued(t *testing.T) {
	jf := newJobFake(t)
	jf.job = func(_ http.ResponseWriter, _ *http.Request, id string, n int) *modelapi.JobStatus {
		switch n {
		case 0:
			return &modelapi.JobStatus{ID: id, Kind: "start", State: modelapi.JobQueued}
		case 1:
			return running(id)
		}
		return done(id)
	}
	var v views
	if _, err := jf.driver(newFakeClock()).RunStart(ctx(), modelapi.StartRequest{Model: "omlx/m"}, &fakeAnswerer{}, v.prog); err != nil {
		t.Fatal(err)
	}
	vs := v.all()
	if len(vs) != 3 || !vs[0].Queued || vs[0].State != modelapi.JobQueued || vs[1].Queued || vs[1].Step != "warming the model" {
		t.Fatalf("views = %+v", vs)
	}
}

// TestJobViewElapsedIgnoresServerTimestamps pins that Elapsed is the client's
// clock since the first poll: server stamps five minutes off change nothing,
// and the poll interval is 500 ms for the first 5 s, then 2 s.
func TestJobViewElapsedIgnoresServerTimestamps(t *testing.T) {
	jf := newJobFake(t)
	off := time.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339)
	jf.job = func(_ http.ResponseWriter, _ *http.Request, id string, n int) *modelapi.JobStatus {
		js := running(id)
		if n >= 12 {
			js = done(id)
		}
		js.Created, js.Updated = off, off
		return js
	}
	fc := newFakeClock()
	var v views
	if _, err := jf.driver(fc).RunStart(ctx(), modelapi.StartRequest{Model: "omlx/m"}, &fakeAnswerer{}, v.prog); err != nil {
		t.Fatal(err)
	}
	vs := v.all()
	want := []time.Duration{0}
	for i := 1; i <= 10; i++ {
		want = append(want, time.Duration(i)*500*time.Millisecond)
	}
	want = append(want, 7*time.Second, 9*time.Second)
	if len(vs) != len(want) {
		t.Fatalf("%d views, want %d", len(vs), len(want))
	}
	for i, w := range want {
		if vs[i].Elapsed != w {
			t.Errorf("view %d Elapsed = %v, want %v", i, vs[i].Elapsed, w)
		}
	}
}

// TestStillFinishingStopIsSuccess pins that a stop the provider accepted but
// has not finished is a success, its step reaching Progress unchanged.
func TestStillFinishingStopIsSuccess(t *testing.T) {
	jf := newJobFake(t)
	step := modelapi.StepStillFinishing + ": ollama is still unloading omlx/m"
	jf.job = func(_ http.ResponseWriter, _ *http.Request, id string, _ int) *modelapi.JobStatus {
		return &modelapi.JobStatus{ID: id, Kind: "stop", Model: "ollama/m", State: modelapi.JobSucceeded, Step: step, Result: &modelapi.JobResult{OK: true}}
	}
	var v views
	js, err := jf.driver(newFakeClock()).RunStop(ctx(), modelapi.StopRequest{Model: "ollama/m"}, &fakeAnswerer{}, v.prog)
	if err != nil || js.Step != step {
		t.Fatalf("js=%+v err=%v", js, err)
	}
	if vs := v.all(); len(vs) != 1 || vs[0].Step != step {
		t.Errorf("views = %+v", vs)
	}
}

// TestStopInUseRepostsWithConfirm pins the in-use flow: ConfirmInUse is
// asked once and the re-post carries confirm_in_use and the requester's
// client session id; a decline is *DeclinedError "nothing was stopped".
func TestStopInUseRepostsWithConfirm(t *testing.T) {
	inUse := modelapi.Problem{Code: modelapi.CodeInUse, Message: "in use by 1 client session(s)", Sessions: []string{"s2"}}
	jf := newJobFake(t)
	jf.job = func(_ http.ResponseWriter, _ *http.Request, id string, _ int) *modelapi.JobStatus {
		if id == "j1" {
			return failedWith(id, inUse)
		}
		return done(id)
	}
	ans := &fakeAnswerer{confirmOK: true}
	if _, err := jf.driver(newFakeClock()).RunStop(ctx(), modelapi.StopRequest{Model: "omlx/m", Session: "s1"}, ans, nil); err != nil {
		t.Fatal(err)
	}
	if ans.confirm != 1 || len(jf.stops) != 2 || jf.stops[0].ConfirmInUse || !jf.stops[1].ConfirmInUse || jf.stops[1].Session != "s1" {
		t.Fatalf("confirm %d stops %+v", ans.confirm, jf.stops)
	}

	jf2 := newJobFake(t)
	jf2.job = func(_ http.ResponseWriter, _ *http.Request, id string, _ int) *modelapi.JobStatus {
		return failedWith(id, inUse)
	}
	_, err := jf2.driver(newFakeClock()).RunStop(ctx(), modelapi.StopRequest{Model: "omlx/m"}, &fakeAnswerer{}, nil)
	var de *DeclinedError
	if !errors.As(err, &de) || err.Error() != "declined — nothing was stopped" {
		t.Fatalf("err = %v", err)
	}
	if _, s, _ := jf2.counts(); s != 1 {
		t.Errorf("stops posted %d", s)
	}
}

// TestRunTerminalProblemsAreJobErrors pins that the codes no question can
// answer (blocked, stopped, unknown_model, failed, cancelled) end as *JobError
// carrying the cleaned server message, with no question asked.
func TestRunTerminalProblemsAreJobErrors(t *testing.T) {
	for _, code := range []string{modelapi.CodeBlocked, modelapi.CodeStopped, modelapi.CodeUnknownModel, modelapi.CodeFailed, modelapi.CodeCancelled} {
		jf := newJobFake(t)
		jf.job = func(_ http.ResponseWriter, _ *http.Request, id string, _ int) *modelapi.JobStatus {
			return failedWith(id, modelapi.Problem{Code: code, Message: "why\n\x1b[31mnot"})
		}
		ans := &fakeAnswerer{replaceOK: true, othersOK: true, confirmOK: true}
		_, err := jf.driver(newFakeClock()).RunStart(ctx(), modelapi.StartRequest{Model: "omlx/m"}, ans, nil)
		var je *JobError
		if !errors.As(err, &je) || je.Problem.Code != code || err.Error() != "why [31mnot" {
			t.Errorf("%s: err = %q", code, err)
		}
		if ans.replace+ans.others+ans.confirm != 0 {
			t.Errorf("%s: a question was asked", code)
		}
	}
}
