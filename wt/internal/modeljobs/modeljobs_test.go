package modeljobs

import (
	"context"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
)

func fixedNow() time.Time { return time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC) }

// waitFor polls cond until it holds or a bounded timeout passes; it exists
// only for state the manager reaches on its own goroutine after a channel
// event the test cannot observe directly.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func stateOf(m *Manager, id string) string {
	st, _ := m.Get(id)
	return st.State
}

func ok(context.Context, func(string)) modelapi.JobResult { return modelapi.JobResult{OK: true} }

// TestJobsRunOneAtATime pins the single-worker FIFO: a second job stays
// queued while the first blocks, and runs only after the first is released.
func TestJobsRunOneAtATime(t *testing.T) {
	m := NewManager(fixedNow)
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	started, release := make(chan struct{}), make(chan struct{})
	a := m.Submit("start", "m1", func(context.Context, func(string)) modelapi.JobResult {
		close(started)
		<-release
		return modelapi.JobResult{OK: true}
	})
	bRan := make(chan struct{})
	b := m.Submit("start", "m2", func(context.Context, func(string)) modelapi.JobResult {
		close(bRan)
		return modelapi.JobResult{OK: true}
	})
	<-started
	if got := stateOf(m, a); got != modelapi.JobRunning {
		t.Fatalf("a = %q, want running", got)
	}
	if got := stateOf(m, b); got != modelapi.JobQueued {
		t.Fatalf("b = %q, want queued", got)
	}
	select {
	case <-bRan:
		t.Fatal("b ran while a was still running")
	default:
	}
	close(release)
	<-bRan
	waitFor(t, "b succeeded", func() bool { return stateOf(m, b) == modelapi.JobSucceeded })
	if got := stateOf(m, a); got != modelapi.JobSucceeded {
		t.Fatalf("a = %q, want succeeded", got)
	}
}

// TestJobOutlivesItsSubmitter pins that a job's context is independent of the
// caller of Submit: cancelling the caller's context must not cancel the job,
// which is how a job survives its HTTP request.
func TestJobOutlivesItsSubmitter(t *testing.T) {
	m := NewManager(fixedNow)
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	reqCtx, cancelReq := context.WithCancel(context.Background())
	proceed := make(chan struct{})
	sawCancel := make(chan bool, 1)
	id := func() string {
		defer cancelReq()
		return m.Submit("start", "m", func(ctx context.Context, _ func(string)) modelapi.JobResult {
			<-proceed
			sawCancel <- ctx.Err() != nil
			return modelapi.JobResult{OK: true}
		})
	}()
	<-reqCtx.Done()
	close(proceed)
	if <-sawCancel {
		t.Fatal("job context was cancelled with its submitter")
	}
	waitFor(t, "succeeded", func() bool { return stateOf(m, id) == modelapi.JobSucceeded })
}

// TestCancelQueuedNeverRuns pins that cancelling a queued job finishes it as
// cancelled without ever invoking its RunFunc.
func TestCancelQueuedNeverRuns(t *testing.T) {
	m := NewManager(fixedNow)
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	started, release := make(chan struct{}), make(chan struct{})
	m.Submit("start", "m1", func(context.Context, func(string)) modelapi.JobResult {
		close(started)
		<-release
		return modelapi.JobResult{OK: true}
	})
	ran := false
	b := m.Submit("start", "m2", func(context.Context, func(string)) modelapi.JobResult {
		ran = true
		return modelapi.JobResult{OK: true}
	})
	<-started
	if !m.Cancel(b) {
		t.Fatal("Cancel(queued) = false")
	}
	st, _ := m.Get(b)
	if st.State != modelapi.JobCancelled || st.Result == nil || st.Result.Error == nil || st.Result.Error.Code != modelapi.CodeCancelled {
		t.Fatalf("queued cancel status = %+v", st)
	}
	if m.Cancel(b) {
		t.Fatal("Cancel(finished) = true")
	}
	close(release)
	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ran {
		t.Fatal("cancelled queued job ran")
	}
	if m.Cancel("nope") {
		t.Fatal("Cancel(unknown) = true")
	}
}

// TestCancelRunningCancelsContext pins that cancelling a running job cancels
// its context and the job ends cancelled, carrying CodeCancelled when the
// RunFunc returned no error of its own.
func TestCancelRunningCancelsContext(t *testing.T) {
	m := NewManager(fixedNow)
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	started := make(chan struct{})
	id := m.Submit("start", "m", func(ctx context.Context, _ func(string)) modelapi.JobResult {
		close(started)
		<-ctx.Done()
		return modelapi.JobResult{}
	})
	<-started
	if !m.Cancel(id) {
		t.Fatal("Cancel(running) = false")
	}
	waitFor(t, "cancelled", func() bool { return stateOf(m, id) == modelapi.JobCancelled })
	st, _ := m.Get(id)
	if st.Result == nil || st.Result.Error == nil || st.Result.Error.Code != modelapi.CodeCancelled {
		t.Fatalf("result = %+v", st.Result)
	}
}

// TestStepIsVisibleWhileRunning pins that Get reports the latest step the
// running job announced, so a poller can show progress.
func TestStepIsVisibleWhileRunning(t *testing.T) {
	m := NewManager(fixedNow)
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	stepped, release := make(chan struct{}), make(chan struct{})
	id := m.Submit("start", "m", func(_ context.Context, step func(string)) modelapi.JobResult {
		step("loading")
		step("warming up")
		close(stepped)
		<-release
		return modelapi.JobResult{OK: true}
	})
	<-stepped
	if st, _ := m.Get(id); st.Step != "warming up" || st.State != modelapi.JobRunning {
		t.Fatalf("status = %+v", st)
	}
	close(release)
	waitFor(t, "succeeded", func() bool { return stateOf(m, id) == modelapi.JobSucceeded })
}

// TestFailedResultMapsToFailed pins that a RunFunc reporting OK=false ends
// the job failed and keeps its error.
func TestFailedResultMapsToFailed(t *testing.T) {
	m := NewManager(fixedNow)
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	id := m.Submit("stop", "m", func(context.Context, func(string)) modelapi.JobResult {
		return modelapi.JobResult{Error: &modelapi.Problem{Code: modelapi.CodeFailed, Message: "boom"}}
	})
	waitFor(t, "failed", func() bool { return stateOf(m, id) == modelapi.JobFailed })
	if st, _ := m.Get(id); st.Result.Error.Message != "boom" {
		t.Fatalf("result = %+v", st.Result)
	}
}

// TestPendingCoversQueuedAndRunningOnly pins Pending's contract, which the
// idle auto-stop relies on: true while a model has a queued or running job,
// false for other models and once the job is finished.
func TestPendingCoversQueuedAndRunningOnly(t *testing.T) {
	m := NewManager(fixedNow)
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	started, release := make(chan struct{}), make(chan struct{})
	m.Submit("start", "run", func(context.Context, func(string)) modelapi.JobResult {
		close(started)
		<-release
		return modelapi.JobResult{OK: true}
	})
	m.Submit("start", "wait", ok)
	<-started
	if !m.Pending("run") || !m.Pending("wait") {
		t.Fatal("running and queued models must be pending")
	}
	if m.Pending("other") {
		t.Fatal("unrelated model pending")
	}
	close(release)
	waitFor(t, "idle", func() bool { return !m.Pending("run") && !m.Pending("wait") })
}

// TestFinishedJobsAreCapped pins the retention bound: after 101 finished jobs
// the oldest id is gone and the newest 100 remain.
func TestFinishedJobsAreCapped(t *testing.T) {
	m := NewManager(fixedNow)
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	var ids []string
	for i := 0; i < 101; i++ {
		ids = append(ids, m.Submit("start", "m", ok))
	}
	waitFor(t, "all finished", func() bool { return stateOf(m, ids[100]) == modelapi.JobSucceeded })
	if _, found := m.Get(ids[0]); found {
		t.Fatal("oldest finished job was kept")
	}
	if _, found := m.Get(ids[1]); !found {
		t.Fatal("second-oldest finished job was dropped")
	}
}

// TestShutdownCancelsQueueAndWaitsForRunning pins Shutdown: queued jobs are
// cancelled, the running job is waited for, and later Submits never run.
func TestShutdownCancelsQueueAndWaitsForRunning(t *testing.T) {
	m := NewManager(fixedNow)
	started, release := make(chan struct{}), make(chan struct{})
	a := m.Submit("start", "m1", func(context.Context, func(string)) modelapi.JobResult {
		close(started)
		<-release
		return modelapi.JobResult{OK: true}
	})
	b := m.Submit("start", "m2", ok)
	<-started
	done := make(chan error, 1)
	go func() { done <- m.Shutdown(context.Background()) }()
	waitFor(t, "queue cancelled", func() bool { return stateOf(m, b) == modelapi.JobCancelled })
	select {
	case <-done:
		t.Fatal("Shutdown returned before the running job finished")
	default:
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := stateOf(m, a); got != modelapi.JobSucceeded {
		t.Fatalf("running job = %q", got)
	}
	c := m.Submit("start", "m3", ok)
	if got := stateOf(m, c); got != modelapi.JobCancelled {
		t.Fatalf("post-shutdown submit = %q, want cancelled", got)
	}
}

// TestShutdownHonoursItsContext pins that Shutdown gives up when its context
// expires while a job is still running, returning the context's error.
func TestShutdownHonoursItsContext(t *testing.T) {
	m := NewManager(fixedNow)
	started, release := make(chan struct{}), make(chan struct{})
	m.Submit("start", "m", func(context.Context, func(string)) modelapi.JobResult {
		close(started)
		<-release
		return modelapi.JobResult{OK: true}
	})
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := m.Shutdown(ctx); err == nil {
		t.Fatal("Shutdown with expired ctx returned nil")
	}
	close(release)
	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// TestCancelAfterCompletionKeepsSuccess pins that a Cancel arriving after the
// RunFunc has done its work, but before the manager records the outcome, does
// not turn a successful result into cancelled: the work happened.
func TestCancelAfterCompletionKeepsSuccess(t *testing.T) {
	m := NewManager(fixedNow)
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	cancelled, release := make(chan struct{}), make(chan struct{})
	id := m.Submit("start", "m1", func(ctx context.Context, _ func(string)) modelapi.JobResult {
		<-ctx.Done()
		close(cancelled)
		<-release
		return modelapi.JobResult{OK: true} // finished the work despite the cancel
	})
	waitFor(t, "running", func() bool { return stateOf(m, id) == modelapi.JobRunning })
	if !m.Cancel(id) {
		t.Fatal("Cancel = false")
	}
	<-cancelled
	close(release)
	waitFor(t, "finished", func() bool { return stateOf(m, id) != modelapi.JobRunning })
	st, _ := m.Get(id)
	if st.State != modelapi.JobSucceeded || st.Result == nil || !st.Result.OK {
		t.Fatalf("state = %q result = %+v, want succeeded OK", st.State, st.Result)
	}
}
