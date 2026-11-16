package modelserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modeljobs"
	"github.com/ohanaverse/local-ai-setup/wt/internal/sessionreg"
)

// fakeClock is a settable clock shared by the session registry and Deps.Now.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// drain waits until every job submitted so far has run: the manager is FIFO,
// so a probe job submitted now finishes after all of them.
func drain(t *testing.T, m *modeljobs.Manager) {
	t.Helper()
	id := m.Submit("probe", "probe", func(context.Context, func(string)) modelapi.JobResult {
		return modelapi.JobResult{OK: true}
	})
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if st, _ := m.Get(id); st.State == modelapi.JobSucceeded {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("probe job did not finish")
}

func newJobs(t *testing.T, clk *fakeClock) *modeljobs.Manager {
	t.Helper()
	m := modeljobs.NewManager(clk.now)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = m.Shutdown(ctx)
	})
	return m
}

// TestAutoStopOnlyIdleSeenLocalModels pins which models one auto-stop scan
// stops: a running local model whose sessions ended at least the grace ago.
// Not one idle for less than the grace, one a live session still uses, a
// cloud model, a running model no session ever registered, or a sibling of
// an Exclusive model another session of the family still uses.
func TestAutoStopOnlyIdleSeenLocalModels(t *testing.T) {
	const grace = 10 * time.Minute
	for _, tc := range []struct {
		name    string
		running []string
		setup   func(reg *sessionreg.Registry, clk *fakeClock)
		want    string // model id stopped, "" for none
	}{
		{"idle past grace", []string{"omlx/a"}, func(reg *sessionreg.Registry, clk *fakeClock) {
			_ = reg.Register("s", []string{"omlx/a"})
			reg.End("s")
			clk.advance(grace)
		}, "omlx/a"},
		{"idle under grace", []string{"omlx/a"}, func(reg *sessionreg.Registry, clk *fakeClock) {
			_ = reg.Register("s", []string{"omlx/a"})
			reg.End("s")
			clk.advance(grace - time.Second)
		}, ""},
		{"live session", []string{"omlx/a"}, func(reg *sessionreg.Registry, clk *fakeClock) {
			_ = reg.Register("s", []string{"omlx/a"})
			clk.advance(30 * time.Second)
			_ = reg.Heartbeat("s", nil)
			clk.advance(30 * time.Second)
		}, ""},
		{"cloud", []string{"omlx/a"}, func(reg *sessionreg.Registry, clk *fakeClock) {
			_ = reg.Register("s", []string{"openrouter/c"})
			reg.End("s")
			clk.advance(grace)
		}, ""},
		{"never seen", []string{"omlx/a"}, func(reg *sessionreg.Registry, clk *fakeClock) {
			_ = reg.Register("s", []string{"omlx/b"})
			reg.End("s")
			clk.advance(grace)
		}, ""},
		{"exclusive sibling in use", []string{"mtplx/x"}, func(reg *sessionreg.Registry, clk *fakeClock) {
			_ = reg.Register("s1", []string{"mtplx/x"})
			reg.End("s1")
			clk.advance(grace)
			_ = reg.Register("s2", []string{"mtplx/y"})
		}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clk := &fakeClock{t: testNow}
			f := &fakeLife{snap: serverSnap(tc.running...)}
			d := f.deps(serverCfg())
			d.Now = clk.now
			reg := sessionreg.New(time.Minute, clk.now)
			tc.setup(reg, clk)
			jobs := newJobs(t, clk)
			a := &AutoStopper{Deps: d, Sessions: reg, Grace: grace, Jobs: jobs}
			a.Tick(context.Background())
			drain(t, jobs)
			stops := f.stopped()
			switch {
			case tc.want == "" && len(stops) != 0:
				t.Errorf("stopped %+v, want nothing", stops)
			case tc.want != "" && (len(stops) != 1 || stops[0].ModelID != tc.want):
				t.Errorf("stopped %+v, want only %s", stops, tc.want)
			}
		})
	}
}

// TestAutoStopSkipsModelWithPendingJob pins that a model with a queued or
// running job (a start or stop someone asked for) is left to that job: the
// scan submits no auto-stop for it.
func TestAutoStopSkipsModelWithPendingJob(t *testing.T) {
	const grace = time.Minute
	clk := &fakeClock{t: testNow}
	f := &fakeLife{snap: serverSnap("omlx/a")}
	d := f.deps(serverCfg())
	d.Now = clk.now
	reg := sessionreg.New(time.Minute, clk.now)
	_ = reg.Register("s", []string{"omlx/a"})
	reg.End("s")
	clk.advance(grace)
	jobs := newJobs(t, clk)
	release := make(chan struct{})
	jobs.Submit("start", "omlx/a", func(context.Context, func(string)) modelapi.JobResult {
		<-release
		return modelapi.JobResult{OK: true}
	})
	a := &AutoStopper{Deps: d, Sessions: reg, Grace: grace, Jobs: jobs}
	a.Tick(context.Background())
	close(release)
	drain(t, jobs)
	if stops := f.stopped(); len(stops) != 0 {
		t.Errorf("stopped %+v, want nothing while a job was pending", stops)
	}
}

// TestAutoStopRunTicksUntilCancelled pins that Run scans on its interval and
// returns once its context is cancelled.
func TestAutoStopRunTicksUntilCancelled(t *testing.T) {
	clk := &fakeClock{t: testNow}
	var probes atomic.Int32
	f := &fakeLife{snap: serverSnap()}
	d := f.deps(serverCfg())
	inv := d.Inventory
	d.Inventory = func(c *config.Config) localmodels.Snapshot { probes.Add(1); return inv(c) }
	reg := sessionreg.New(time.Minute, clk.now)
	_ = reg.Register("s", []string{"omlx/a"})
	a := &AutoStopper{Deps: d, Sessions: reg, Grace: time.Minute, Jobs: newJobs(t, clk)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.Run(ctx, time.Millisecond); close(done) }()
	deadline := time.Now().Add(10 * time.Second)
	for probes.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if probes.Load() < 2 {
		t.Errorf("%d scans, want at least 2", probes.Load())
	}
}

// TestRoutedPollsUntilListed pins the default gateway wait: it polls
// GET <upstream>/v1/models with the LiteLLM key until the id is listed.
func TestRoutedPollsUntilListed(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer sk-up" {
			http.Error(w, "bad request", http.StatusUnauthorized)
			return
		}
		if calls.Add(1) < 3 {
			_, _ = w.Write([]byte(`{"data":[{"id":"other"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"other"},{"id":"omlx/a"}]}`))
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	w := routeWaiter{client: srv.Client(), upstream: u, key: "sk-up", every: time.Millisecond, timeout: 10 * time.Second}
	if err := w.wait(context.Background(), "omlx/a"); err != nil {
		t.Fatalf("wait = %v, want nil", err)
	}
	if calls.Load() != 3 {
		t.Errorf("%d polls, want 3", calls.Load())
	}
}

// TestRoutedTimesOut pins that the gateway wait gives up after its timeout
// with an error naming the id, and returns the context's error on a cancel.
func TestRoutedTimesOut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	w := routeWaiter{client: srv.Client(), upstream: u, key: "k", every: time.Millisecond, timeout: 50 * time.Millisecond}
	err := w.wait(context.Background(), "omlx/a")
	if err == nil || !strings.Contains(err.Error(), "omlx/a") {
		t.Fatalf("wait = %v, want a timeout naming omlx/a", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w.timeout = 10 * time.Second
	if err := w.wait(ctx, "omlx/a"); err != context.Canceled {
		t.Fatalf("cancelled wait = %v, want context.Canceled", err)
	}
}

// TestDefaultDepsWiresEveryField pins that DefaultDeps leaves no function
// field nil (a nil field would panic inside a job), without calling any.
func TestDefaultDepsWiresEveryField(t *testing.T) {
	u, _ := url.Parse("http://127.0.0.1:4000")
	d := DefaultDeps(serverCfg(), func() (*config.Config, error) { return serverCfg(), nil }, u, "k")
	if d.Config == nil || d.Inventory == nil || d.Tiers == nil || d.Now == nil || d.Start == nil ||
		d.Settle == nil || d.BeginStops == nil || d.SettleRoutes == nil || d.Routed == nil {
		t.Fatalf("DefaultDeps left a field nil: %+v", d)
	}
}

// TestAutoStopWaitsAfterSuccessfulStart pins that a successful start restarts
// the idle clock of a model sessions have used: a model idle past the grace
// that a client has just started again is not stopped by the next scan, and
// is once the grace passes after the start.
func TestAutoStopWaitsAfterSuccessfulStart(t *testing.T) {
	const grace = 10 * time.Minute
	clk := &fakeClock{t: testNow}
	f := &fakeLife{snap: serverSnap("omlx/a")}
	d := f.deps(serverCfg())
	d.Now = clk.now
	reg := sessionreg.New(time.Minute, clk.now)
	_ = reg.Register("s", []string{"omlx/a"})
	reg.End("s")
	clk.advance(grace + time.Minute) // idle past the grace
	jobs := newJobs(t, clk)
	if res := d.StartJob(modelapi.StartRequest{Model: "omlx/a"}, reg)(context.Background(), func(string) {}); !res.OK {
		t.Fatalf("start = %+v, want OK", res)
	}
	a := &AutoStopper{Deps: d, Sessions: reg, Grace: grace, Jobs: jobs}
	a.Tick(context.Background())
	drain(t, jobs)
	if stops := f.stopped(); len(stops) != 0 {
		t.Fatalf("stopped %+v right after a successful start, want nothing", stops)
	}
	clk.advance(grace)
	a.Tick(context.Background())
	drain(t, jobs)
	if stops := f.stopped(); len(stops) != 1 {
		t.Fatalf("stopped %+v once the grace passed after the start, want one stop", stops)
	}
}

// TestAutoStopSkipsUntrustedProbe pins that a family whose probe is not
// trustworthy (the server answered partially or not at all) is skipped: the
// scan must not submit a stop job that would only fail on every pass.
func TestAutoStopSkipsUntrustedProbe(t *testing.T) {
	const grace = time.Minute
	clk := &fakeClock{t: testNow}
	snap := serverSnap("omlx/a")
	snap.Providers["omlx"] = localmodels.StatusPartial
	f := &fakeLife{snap: snap}
	d := f.deps(serverCfg())
	d.Now = clk.now
	var probes atomic.Int32
	inv := d.Inventory
	d.Inventory = func(c *config.Config) localmodels.Snapshot { probes.Add(1); return inv(c) }
	reg := sessionreg.New(time.Minute, clk.now)
	_ = reg.Register("s", []string{"omlx/a"})
	reg.End("s")
	clk.advance(grace)
	jobs := newJobs(t, clk)
	a := &AutoStopper{Deps: d, Sessions: reg, Grace: grace, Jobs: jobs}
	a.Tick(context.Background())
	drain(t, jobs)
	if stops := f.stopped(); len(stops) != 0 {
		t.Errorf("stopped %+v, want nothing for an untrusted probe", stops)
	}
	// A submitted StopJob would probe the inventory a second time (and fail).
	if n := probes.Load(); n != 1 {
		t.Errorf("%d inventory probes, want 1: the scan submitted a job for an untrusted probe", n)
	}
	// The probe recovering makes the model stoppable again.
	f.mu.Lock()
	f.snap.Providers["omlx"] = localmodels.StatusOK
	f.mu.Unlock()
	a.Tick(context.Background())
	drain(t, jobs)
	if stops := f.stopped(); len(stops) != 1 {
		t.Errorf("stopped %+v after the probe recovered, want one stop", stops)
	}
}

// setSnap replaces the inventory the fake engine reports.
func (f *fakeLife) setSnap(snap localmodels.Snapshot) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snap = snap
}

// TestAutoStoppedModelStartedByHandIsLeftAlone pins that the registry does
// not remember a model forever. A model auto-stopped (or stopped anyhow) and
// idle past the grace is forgotten, so when it runs again with no client
// session (started by hand on the host, or by llmbench) it survives any
// number of scans: a model no client session registered is left alone. A
// model whose client session registered before its start is not forgotten
// while it is not yet running, even once that session is older than the
// grace, and is stopped as usual after the session ends — and a queued start
// keeps it from being forgotten even after that session has lapsed entirely.
func TestAutoStoppedModelStartedByHandIsLeftAlone(t *testing.T) {
	t.Run("hand start after auto-stop", func(t *testing.T) {
		const grace = 10 * time.Minute
		clk := &fakeClock{t: testNow}
		f := &fakeLife{snap: serverSnap("omlx/a")}
		d := f.deps(serverCfg())
		d.Now = clk.now
		reg := sessionreg.New(time.Minute, clk.now)
		_ = reg.Register("s", []string{"omlx/a"})
		reg.End("s")
		clk.advance(grace)
		jobs := newJobs(t, clk)
		a := &AutoStopper{Deps: d, Sessions: reg, Grace: grace, Jobs: jobs}
		a.Tick(context.Background())
		drain(t, jobs)
		if stops := f.stopped(); len(stops) != 1 {
			t.Fatalf("stopped %+v, want the idle model stopped once", stops)
		}
		f.setSnap(serverSnap()) // the stop took effect
		clk.advance(autoStopEvery)
		a.Tick(context.Background())
		drain(t, jobs)
		f.setSnap(serverSnap("omlx/a")) // started again by hand
		for range 5 {
			clk.advance(grace)
			a.Tick(context.Background())
			drain(t, jobs)
		}
		if stops := f.stopped(); len(stops) != 1 {
			t.Errorf("stopped %d times, want the hand-started model left alone", len(stops))
		}
	})
	t.Run("pending start keeps its client session", func(t *testing.T) {
		const grace = 30 * time.Second // under the 1-minute TTL: a live session can be older than the grace
		clk := &fakeClock{t: testNow}
		f := &fakeLife{snap: serverSnap()} // omlx/b is not running yet
		d := f.deps(serverCfg())
		d.Now = clk.now
		reg := sessionreg.New(time.Minute, clk.now)
		_ = reg.Register("s", []string{"omlx/b"})
		clk.advance(45 * time.Second)
		jobs := newJobs(t, clk)
		a := &AutoStopper{Deps: d, Sessions: reg, Grace: grace, Jobs: jobs}
		a.Tick(context.Background())
		drain(t, jobs)
		if _, seen := reg.IdleSince("omlx/b"); !seen {
			t.Fatal("a model with a live client session was forgotten before it ran")
		}
		f.setSnap(serverSnap("omlx/b")) // the start lands
		reg.End("s")
		clk.advance(grace)
		a.Tick(context.Background())
		drain(t, jobs)
		if stops := f.stopped(); len(stops) != 1 || stops[0].ModelID != "omlx/b" {
			t.Errorf("stopped %+v, want omlx/b once its session ended and the grace passed", stops)
		}
	})
	t.Run("queued start keeps the model after its session lapses", func(t *testing.T) {
		// The client registers, asks for a start and stops heartbeating: its
		// session lapses after the TTL while the job is still queued or running
		// (one job at a time can wait behind others longer than the grace). The
		// job must keep the model from being forgotten, or it starts unseen —
		// Touch does not bring a forgotten model back — and is never stopped.
		const grace = time.Minute
		clk := &fakeClock{t: testNow}
		f := &fakeLife{snap: serverSnap()} // omlx/b is not running yet
		d := f.deps(serverCfg())
		d.Now = clk.now
		reg := sessionreg.New(time.Minute, clk.now)
		_ = reg.Register("s", []string{"omlx/b"})
		reg.End("s") // the client is gone; no live session
		clk.advance(2 * grace)
		jobs := newJobs(t, clk)
		started := make(chan struct{})
		gate := make(chan struct{})
		release := sync.OnceFunc(func() { close(gate) })
		t.Cleanup(release) // never leave the worker blocked, even on a failure
		jobs.Submit("start", "omlx/b", func(context.Context, func(string)) modelapi.JobResult {
			close(started)
			<-gate
			return modelapi.JobResult{OK: true}
		})
		<-started
		a := &AutoStopper{Deps: d, Sessions: reg, Grace: grace, Jobs: jobs}
		a.Tick(context.Background())
		if _, seen := reg.IdleSince("omlx/b"); !seen {
			t.Fatal("a model with a queued start was forgotten before it ran")
		}
		release()
		drain(t, jobs) // the job finished; the next scan forgets it as usual
		clk.advance(grace)
		a.Tick(context.Background())
		if _, seen := reg.IdleSince("omlx/b"); seen {
			t.Fatal("after its job finished the idle model was still remembered")
		}
	})
	t.Run("untrusted probe does not forget an absent model", func(t *testing.T) {
		// An absent model under a partial probe may only look absent because
		// the probe failed: it must stay remembered, so once the probe recovers
		// and shows it running, the idle model is stopped.
		const grace = time.Minute
		clk := &fakeClock{t: testNow}
		snap := serverSnap() // omlx/a is not listed as running
		snap.Providers["omlx"] = localmodels.StatusPartial
		f := &fakeLife{snap: snap}
		d := f.deps(serverCfg())
		d.Now = clk.now
		reg := sessionreg.New(time.Minute, clk.now)
		_ = reg.Register("s", []string{"omlx/a"})
		reg.End("s")
		clk.advance(2 * grace) // absent for longer than the grace
		jobs := newJobs(t, clk)
		a := &AutoStopper{Deps: d, Sessions: reg, Grace: grace, Jobs: jobs}
		a.Tick(context.Background())
		drain(t, jobs)
		if _, seen := reg.IdleSince("omlx/a"); !seen {
			t.Fatal("a model absent under an untrusted probe was forgotten")
		}
		ok := serverSnap("omlx/a") // the probe recovers; the model is running
		f.setSnap(ok)
		a.Tick(context.Background())
		drain(t, jobs)
		if stops := f.stopped(); len(stops) != 1 {
			t.Errorf("stopped %+v after the probe recovered, want exactly one stop", stops)
		}
	})
}
