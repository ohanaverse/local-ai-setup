// Package modeljobs runs the wt-model server's start/stop jobs strictly one
// at a time, in memory. A job outlives whoever submitted it: its context
// derives from context.Background(), never from an HTTP request.
package modeljobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
)

// maxFinished is how many finished jobs the manager keeps for Get.
const maxFinished = 100

// RunFunc does a job's work. It reports progress through step and should
// return promptly once ctx is cancelled. If the job was cancelled and the
// returned result carries no error, the manager records a CodeCancelled one.
type RunFunc func(ctx context.Context, step func(string)) modelapi.JobResult

type job struct {
	status    modelapi.JobStatus
	run       RunFunc
	cancel    context.CancelFunc // set while running
	cancelled bool               // a Cancel arrived while running
}

// Manager is a FIFO queue drained by one worker goroutine.
type Manager struct {
	now func() time.Time

	mu       sync.Mutex
	cond     *sync.Cond
	jobs     map[string]*job
	queue    []*job
	finished []string // finished ids, oldest first
	running  *job
	closed   bool
	done     chan struct{} // closed when the worker exits
}

// NewManager starts a manager and its worker. now stamps job times.
func NewManager(now func() time.Time) *Manager {
	m := &Manager{now: now, jobs: map[string]*job{}, done: make(chan struct{})}
	m.cond = sync.NewCond(&m.mu)
	go m.worker()
	return m
}

func (m *Manager) stamp() string { return m.now().UTC().Format(time.RFC3339) }

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("modeljobs: crypto/rand: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// cancelledResult is the result of a job cancelled before its work began
// (queued, or submitted after Shutdown): nothing ran, so it says what is
// still true (M7). A cancel that arrives while the work is running is
// cancelledRunning instead, which does not claim that.
func cancelledResult(kind string) *modelapi.JobResult {
	msg := "cancelled — nothing was changed"
	switch kind {
	case "start":
		msg = "cancelled — nothing was started"
	case "stop":
		msg = "cancelled — nothing was stopped"
	}
	return &modelapi.JobResult{Error: &modelapi.Problem{Code: modelapi.CodeCancelled, Message: msg}}
}

// cancelledRunning is the message of a job cancelled after its work began and
// that returned no error of its own: part of the work may be done.
const cancelledRunning = "cancelled while running — the work may be partly done; check the model's state"

// Submit queues a job and returns its id at once. After Shutdown the job is
// recorded as cancelled and never runs.
func (m *Manager) Submit(kind, model string, run RunFunc) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.stamp()
	j := &job{run: run, status: modelapi.JobStatus{
		ID: newID(), Kind: kind, Model: model, State: modelapi.JobQueued, Created: now, Updated: now,
	}}
	m.jobs[j.status.ID] = j
	if m.closed {
		m.finishLocked(j, modelapi.JobCancelled, cancelledResult(j.status.Kind))
		return j.status.ID
	}
	m.queue = append(m.queue, j)
	m.cond.Signal()
	return j.status.ID
}

// Get returns a copy of a job's status.
func (m *Manager) Get(id string) (modelapi.JobStatus, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, found := m.jobs[id]
	if !found {
		return modelapi.JobStatus{}, false
	}
	st := j.status
	if st.Result != nil {
		r := *st.Result
		if r.Error != nil {
			e := *r.Error
			r.Error = &e
		}
		st.Result = &r
	}
	return st, true
}

// Cancel cancels a queued job (it never runs) or a running job's context. It
// reports false for an unknown or already finished id.
func (m *Manager) Cancel(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, found := m.jobs[id]
	if !found {
		return false
	}
	switch j.status.State {
	case modelapi.JobQueued:
		for i, q := range m.queue {
			if q == j {
				m.queue = append(m.queue[:i], m.queue[i+1:]...)
				break
			}
		}
		m.finishLocked(j, modelapi.JobCancelled, cancelledResult(j.status.Kind))
		return true
	case modelapi.JobRunning:
		j.cancelled = true
		j.cancel()
		return true
	}
	return false
}

// Pending reports whether any job for model is queued or running.
func (m *Manager) Pending(model string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running != nil && m.running.status.Model == model {
		return true
	}
	for _, q := range m.queue {
		if q.status.Model == model {
			return true
		}
	}
	return false
}

// Shutdown stops accepting jobs, cancels the queued ones and waits for the
// running job (which it does not cancel) or for ctx, whichever comes first.
// It may be called again after a ctx timeout.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	m.closed = true
	for _, q := range m.queue {
		m.finishLocked(q, modelapi.JobCancelled, cancelledResult(q.status.Kind))
	}
	m.queue = nil
	m.cond.Broadcast()
	m.mu.Unlock()
	select {
	case <-m.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// finishLocked records a terminal state and evicts the oldest finished jobs
// beyond the cap. m.mu must be held.
func (m *Manager) finishLocked(j *job, state string, res *modelapi.JobResult) {
	j.status.State = state
	j.status.Result = res
	j.status.Updated = m.stamp()
	j.cancel = nil
	m.finished = append(m.finished, j.status.ID)
	for len(m.finished) > maxFinished {
		delete(m.jobs, m.finished[0])
		m.finished = m.finished[1:]
	}
}

func (m *Manager) worker() {
	defer close(m.done)
	for {
		m.mu.Lock()
		for len(m.queue) == 0 && !m.closed {
			m.cond.Wait()
		}
		if len(m.queue) == 0 {
			m.mu.Unlock()
			return
		}
		j := m.queue[0]
		m.queue = m.queue[1:]
		ctx, cancel := context.WithCancel(context.Background())
		j.cancel = cancel
		j.status.State = modelapi.JobRunning
		j.status.Updated = m.stamp()
		m.running = j
		m.mu.Unlock()

		res := j.run(ctx, func(s string) {
			m.mu.Lock()
			j.status.Step = s
			j.status.Updated = m.stamp()
			m.mu.Unlock()
		})
		cancel()

		m.mu.Lock()
		m.running = nil
		switch {
		case res.OK:
			// A Cancel that lost the race with completion must not turn
			// finished work into cancelled.
			m.finishLocked(j, modelapi.JobSucceeded, &res)
		case j.cancelled:
			if res.Error == nil {
				res.Error = &modelapi.Problem{Code: modelapi.CodeCancelled, Message: cancelledRunning}
			}
			m.finishLocked(j, modelapi.JobCancelled, &res)
		default:
			m.finishLocked(j, modelapi.JobFailed, &res)
		}
		m.mu.Unlock()
	}
}
