package modelserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/sessionreg"
)

// aliasServerCfg is serverCfg with three aliases: alias/x over omlx/a
// (order 1) and openrouter/c (order 2), alias/m over mtplx/y (an Exclusive
// family) and alias/cloud over openrouter/c alone.
func aliasServerCfg() *config.Config {
	cfg := serverCfg()
	cfg.Aliases = []config.Alias{
		{ID: "alias/x", Targets: []config.AliasTarget{{Model: "omlx/a", Order: 1}, {Model: "openrouter/c", Order: 2}}},
		{ID: "alias/m", Targets: []config.AliasTarget{{Model: "mtplx/y", Order: 1}}},
		{ID: "alias/cloud", Targets: []config.AliasTarget{{Model: "openrouter/c", Order: 1}}},
		// Two local targets of one Pool provider (#690), listed out of order.
		{ID: "alias/two", Targets: []config.AliasTarget{{Model: "omlx/b", Order: 2}, {Model: "omlx/a", Order: 1}}},
	}
	return cfg
}

// cfgBox is a registry a test can re-point under a running server.
type cfgBox struct {
	mu  sync.Mutex
	cfg *config.Config
}

func (b *cfgBox) get() *config.Config {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cfg
}

func (b *cfgBox) set(c *config.Config) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cfg = c
}

// aliasRig is an AutoStopper over fake deps and a session registry wired
// the way the server wires them (tapConfig), on a settable clock.
func aliasRig(t *testing.T, box *cfgBox, f *fakeLife, grace time.Duration) (*AutoStopper, *sessionreg.Registry, *fakeClock) {
	t.Helper()
	clk := &fakeClock{t: testNow}
	d := f.deps(nil)
	d.Config = box.get
	d.Now = clk.now
	reg := sessionreg.New(time.Minute, clk.now)
	d = tapConfig(d, reg)
	return &AutoStopper{Deps: d, Sessions: reg, Grace: grace, Jobs: newJobs(t, clk)}, reg, clk
}

// tickStops runs one scan, waits for its jobs and makes every stop take
// effect in the fake inventory (running lists what was started).
func tickStops(t *testing.T, a *AutoStopper, f *fakeLife, running ...string) {
	t.Helper()
	a.Tick(context.Background())
	drain(t, a.Jobs)
	stopped := stoppedIDs(f)
	f.setSnap(serverSnap(slices.DeleteFunc(running, func(id string) bool { return slices.Contains(stopped, id) })...))
}

func stoppedIDs(f *fakeLife) []string {
	var ids []string
	for _, e := range f.stopped() {
		ids = append(ids, e.ModelID)
	}
	return ids
}

// TestAutoStopKeepsATargetAnAliasSessionUses pins Review Focus 3: a client
// session registers the alias id, never its target, so a local target idle
// past the grace by its own clock must still be kept while a live session
// uses its alias; stopping it would cut that session off mid-request.
func TestAutoStopKeepsATargetAnAliasSessionUses(t *testing.T) {
	const grace = time.Minute
	f := &fakeLife{snap: serverSnap("omlx/a")}
	a, reg, clk := aliasRig(t, &cfgBox{cfg: aliasServerCfg()}, f, grace)
	_ = reg.Register("old", []string{"omlx/a"})
	reg.End("old")
	_ = reg.Register("s", []string{"alias/x"})
	for range 4 {
		clk.advance(30 * time.Second)
		_ = reg.Heartbeat("s", nil)
	}
	a.Tick(context.Background())
	drain(t, a.Jobs)
	if got := stoppedIDs(f); len(got) != 0 {
		t.Errorf("stopped %v under a live client session on alias/x", got)
	}
}

// TestAutoStopSeesTheTargetAfterARepoint pins that a re-point is seen
// without the client session registering again: the new target is kept for
// the session, the old target is stopped once idle past the grace (not
// leaked), and the new target is stopped after the session ends.
func TestAutoStopSeesTheTargetAfterARepoint(t *testing.T) {
	const grace = time.Minute
	box := &cfgBox{cfg: aliasServerCfg()}
	f := &fakeLife{snap: serverSnap("omlx/a", "omlx/b")}
	a, reg, clk := aliasRig(t, box, f, grace)
	_ = reg.Register("s", []string{"alias/x"})
	tickStops(t, a, f, "omlx/a", "omlx/b")
	repointed := aliasServerCfg()
	repointed.Aliases[0].Targets[0].Model = "omlx/b"
	box.set(repointed)
	for range 4 {
		clk.advance(30 * time.Second)
		_ = reg.Heartbeat("s", nil)
		tickStops(t, a, f, "omlx/a", "omlx/b")
	}
	if got := stoppedIDs(f); !reflect.DeepEqual(got, []string{"omlx/a"}) {
		t.Fatalf("stopped %v while the session used the re-pointed alias, want only the old target omlx/a", got)
	}
	reg.End("s")
	clk.advance(grace)
	tickStops(t, a, f, "omlx/a", "omlx/b")
	if got := stoppedIDs(f); !reflect.DeepEqual(got, []string{"omlx/a", "omlx/b"}) {
		t.Errorf("stopped %v after the session ended, want the new target too", got)
	}
}

// TestAutoStopStopsAnAliasTargetAfterTheGrace pins the other half of Review
// Focus 3: a local target used only through an alias is not leaked. The
// session on alias/x ends, the clock passes the grace, and within two scans
// a stop job is submitted for the target.
func TestAutoStopStopsAnAliasTargetAfterTheGrace(t *testing.T) {
	const grace = 10 * time.Minute
	f := &fakeLife{snap: serverSnap("omlx/a")}
	a, reg, clk := aliasRig(t, &cfgBox{cfg: aliasServerCfg()}, f, grace)
	_ = reg.Register("s", []string{"alias/x"})
	reg.End("s")
	clk.advance(grace)
	for range 2 {
		tickStops(t, a, f, "omlx/a")
	}
	if got := stoppedIDs(f); !reflect.DeepEqual(got, []string{"omlx/a"}) {
		t.Errorf("stopped %v, want omlx/a once", got)
	}
}

// gatedServer is a test server over the alias registry whose engine Start
// blocks until release is called, on a settable clock, with the given idle
// grace.
func gatedServer(t *testing.T, f *fakeLife, grace time.Duration) (*Server, *fakeClock, func()) {
	t.Helper()
	clk := &fakeClock{t: testNow}
	cfg := aliasServerCfg()
	d := f.deps(cfg)
	d.Now = clk.now
	gate := make(chan struct{})
	release := sync.OnceFunc(func() { close(gate) })
	t.Cleanup(release)
	start := d.Start
	d.Start = func(ctx context.Context, c *config.Config, tg lifecycle.Target, o lifecycle.Options) error {
		<-gate
		return start(ctx, c, tg, o)
	}
	s, err := NewServer(Options{Listen: "127.0.0.1:0", Token: testToken, IdleGrace: grace}, d)
	if err != nil {
		t.Fatal(err)
	}
	return s, clk, release
}

// newHandlerTS serves s's handler on a test server.
func newHandlerTS(t *testing.T, s *Server) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// postAliasStart posts a start through the server's handler and returns the job id.
func postAliasStart(t *testing.T, s *Server, model string) string {
	t.Helper()
	ts := newHandlerTS(t, s)
	st, b := call(t, ts, "POST", "/control/v1/start", `{"model":"`+model+`"}`)
	if st != 202 {
		t.Fatalf("start %s: %d %s", model, st, b)
	}
	var ref modelapi.JobRef
	if err := json.Unmarshal(b, &ref); err != nil {
		t.Fatal(err)
	}
	return ref.ID
}

// waitJob waits until job id ends and returns its status.
func waitJob(t *testing.T, s *Server, id string) modelapi.JobStatus {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		st, _ := s.jobs.Get(id)
		if st.State != modelapi.JobQueued && st.State != modelapi.JobRunning {
			return st
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("job %s did not finish", id)
	return modelapi.JobStatus{}
}

// TestAutoStopDoesNotForgetATargetWhileItsAliasStartRuns pins that a start
// of alias/x is keyed by its local target (startKey): while it runs,
// Jobs.Pending(target) is true, so an auto-stop scan neither forgets the
// target nor the alias even after the client session lapsed. Forgetting
// either would let the start land unseen and the model run forever.
func TestAutoStopDoesNotForgetATargetWhileItsAliasStartRuns(t *testing.T) {
	const grace = time.Minute
	f := &fakeLife{snap: serverSnap()}
	s, clk, release := gatedServer(t, f, grace)
	_ = s.sessions.Register("s", []string{"alias/x"})
	s.sessions.End("s")
	clk.advance(2 * grace)
	id := postAliasStart(t, s, "alias/x")
	if !s.jobs.Pending("omlx/a") {
		t.Fatal("Jobs.Pending(omlx/a) is false while a start of alias/x runs")
	}
	s.stopper.Tick(context.Background())
	if _, seen := s.sessions.IdleSince("omlx/a"); !seen {
		t.Error("the target was forgotten while its alias's start ran")
	}
	if _, seen := s.sessions.IdleSince("alias/x"); !seen {
		t.Error("the alias was forgotten while its start ran")
	}
	f.setSnap(serverSnap("omlx/a")) // the start lands
	release()
	if st := waitJob(t, s, id); st.State != modelapi.JobSucceeded {
		t.Fatalf("alias start = %+v", st)
	}
	s.stopper.Tick(context.Background())
	drain(t, s.jobs)
	if got := stoppedIDs(f); len(got) != 0 {
		t.Errorf("stopped %v right after the alias start, want nothing", got)
	}
	clk.advance(grace)
	s.stopper.Tick(context.Background())
	drain(t, s.jobs)
	if got := stoppedIDs(f); !reflect.DeepEqual(got, []string{"omlx/a"}) {
		t.Errorf("stopped %v once the grace passed after the start, want omlx/a", got)
	}
}

// TestAliasStartJoinsARunningTargetStart pins that a start of alias/x and a
// start of its local target share one job, so two client sessions (one on
// the alias, one on the model) never queue a second start of one model.
func TestAliasStartJoinsARunningTargetStart(t *testing.T) {
	f := &fakeLife{snap: serverSnap()}
	s, _, release := gatedServer(t, f, time.Hour)
	first := postAliasStart(t, s, "alias/x")
	second := postAliasStart(t, s, "omlx/a")
	release()
	if first != second {
		t.Errorf("alias start %s and target start %s are two jobs, want one", first, second)
	}
	waitJob(t, s, first)
	if slices.Index(f.callList(), "Start omlx/a") < 0 || strings.Count(strings.Join(f.callList(), "\n"), "Start omlx/a") != 1 {
		t.Errorf("engine calls %v, want one Start omlx/a", f.callList())
	}
}

// TestStartAliasStartsItsLocalTargetAndWaitsForTheTarget pins the alias
// start job: it starts the local target, waits for the target's gateway id
// (the alias rows follow the target's route write), confirms the alias's row
// for that target, and touches the alias and the target. A missing row fails
// the job only when the alias id is not routed at all; an alias with no local
// target has nothing to start.
func TestStartAliasStartsItsLocalTargetAndWaitsForTheTarget(t *testing.T) {
	run := func(t *testing.T, mut func(*Deps)) (modelapi.JobResult, *fakeLife, *stepLog, *sessionreg.Registry, *fakeClock) {
		clk := &fakeClock{t: testNow}
		f := &fakeLife{snap: serverSnap()}
		d := f.deps(aliasServerCfg())
		d.Now = clk.now
		mut(&d)
		reg := sessionreg.New(time.Minute, clk.now)
		d = tapConfig(d, reg)
		_ = reg.Register("s", []string{"alias/x"})
		reg.End("s")
		clk.advance(time.Hour)
		steps := &stepLog{}
		return d.StartJob(modelapi.StartRequest{Model: "alias/x"}, reg)(context.Background(), steps.step), f, steps, reg, clk
	}
	t.Run("listed", func(t *testing.T) {
		var asked []string
		res, f, _, reg, clk := run(t, func(d *Deps) {
			d.AliasListed = func(_ context.Context, _ *config.Config, alias, target string) (bool, error) {
				asked = append(asked, alias+" "+target)
				return true, nil
			}
		})
		if !res.OK {
			t.Fatalf("result = %+v", res.Error)
		}
		if want := []string{"Start omlx/a", "Settle omlx/a", "Routed omlx/a"}; !reflect.DeepEqual(f.callList(), want) {
			t.Errorf("engine calls %v, want %v", f.callList(), want)
		}
		if !reflect.DeepEqual(asked, []string{"alias/x omlx/a"}) {
			t.Errorf("alias row checks %v", asked)
		}
		for _, id := range []string{"alias/x", "omlx/a"} {
			if at, ok := reg.IdleSince(id); !ok || !at.Equal(clk.now()) {
				t.Errorf("%s idle since %v %v, want touched now", id, at, ok)
			}
		}
	})
	t.Run("row missing, alias unrouted", func(t *testing.T) {
		res, _, _, _, _ := run(t, func(d *Deps) {
			d.AliasListed = func(context.Context, *config.Config, string, string) (bool, error) { return false, nil }
			d.RoutedIDs = func(context.Context) (map[string]bool, error) { return map[string]bool{"omlx/a": true}, nil }
		})
		p := wantProblem(t, res, modelapi.CodeFailed)
		if !strings.Contains(p.Message, "alias/x") || !strings.Contains(p.Message, "'wt-model litellm sync'") {
			t.Errorf("message = %q", p.Message)
		}
	})
	t.Run("row missing, alias routed", func(t *testing.T) {
		res, _, steps, _, _ := run(t, func(d *Deps) {
			d.AliasListed = func(context.Context, *config.Config, string, string) (bool, error) {
				return false, errors.New("proxy busy")
			}
			d.RoutedIDs = func(context.Context) (map[string]bool, error) { return map[string]bool{"alias/x": true}, nil }
		})
		if !res.OK {
			t.Fatalf("result = %+v, want OK with a warning step", res.Error)
		}
		var warned int
		for _, s := range steps.list() {
			if strings.HasPrefix(s, "warning: ") && strings.Contains(s, "alias/x") {
				warned++
			}
		}
		if warned != 1 {
			t.Errorf("steps %v, want one warning naming alias/x", steps.list())
		}
	})
	t.Run("no local target", func(t *testing.T) {
		f := &fakeLife{snap: serverSnap()}
		d := f.deps(aliasServerCfg())
		res := d.StartJob(modelapi.StartRequest{Model: "alias/cloud"}, nil)(context.Background(), func(string) {})
		p := wantProblem(t, res, modelapi.CodeFailed)
		if !strings.Contains(p.Message, "nothing to start") || len(f.callList()) != 0 {
			t.Errorf("message %q, engine calls %v", p.Message, f.callList())
		}
	})
}

// TestStopOfAnAliasIDNamesItsTarget pins that a stop of an alias id stops
// nothing and says which model to stop instead: an alias is a name, and a
// stop must never guess which of its targets the caller meant.
func TestStopOfAnAliasIDNamesItsTarget(t *testing.T) {
	f := &fakeLife{snap: serverSnap("omlx/a")}
	d := f.deps(aliasServerCfg())
	res := d.StopJob(modelapi.StopRequest{Model: "alias/x"}, testSessions())(context.Background(), func(string) {})
	p := wantProblem(t, res, modelapi.CodeFailed)
	if p.Message != "alias/x has nothing to stop; stop omlx/a" {
		t.Errorf("message = %q", p.Message)
	}
	if len(f.stopped()) != 0 {
		t.Errorf("stopped %v", f.stopped())
	}
}

// TestStopBlockersNameAliasSessions pins Review Focus 3 on an asked stop: a
// stop of a local target refuses (in_use) and names the client sessions that
// use it through an alias, and on an Exclusive provider a session on an
// alias of a sibling model blocks too, since the server stops as a whole.
func TestStopBlockersNameAliasSessions(t *testing.T) {
	for _, c := range []struct{ running, stop string }{{"omlx/a", "omlx/a"}, {"mtplx/x", "mtplx/x"}} {
		f := &fakeLife{snap: serverSnap(c.running)}
		d := f.deps(aliasServerCfg())
		reg := testSessions()
		d = tapConfig(d, reg)
		_ = reg.Register("s-alias", []string{"alias/x", "alias/m"})
		res := d.StopJob(modelapi.StopRequest{Model: c.stop}, reg)(context.Background(), func(string) {})
		p := wantProblem(t, res, modelapi.CodeInUse)
		if !reflect.DeepEqual(p.Sessions, []string{"s-alias"}) {
			t.Errorf("%s: sessions = %v, want the alias session", c.stop, p.Sessions)
		}
		if len(f.stopped()) != 0 {
			t.Errorf("%s: stopped %v", c.stop, f.stopped())
		}
	}
}

// prefixlessAliasCfg is serverCfg plus a malformed alias whose id has no
// alias/ prefix and equals a model id (a hand edit the reader keeps, marked
// Malformed), with a local target other than that model.
func prefixlessAliasCfg() *config.Config {
	cfg := serverCfg()
	cfg.Aliases = []config.Alias{{ID: "omlx/a", Malformed: `id must start with "alias/"`,
		Targets: []config.AliasTarget{{Model: "omlx/b", Order: 1}}}}
	return cfg
}

// TestPrefixlessMalformedAliasNeverShadowsTheModel pins that only an id under
// alias/ is ever an alias on the server: a hand-edited registry whose
// malformed alias keeps the id of a model (omlx/a) must leave that model
// startable, stoppable, listed once as itself and unexpanded in session
// accounting. Read as an alias it would refuse a stop ("has nothing to
// stop"), block a start, key the start by another model and list a second
// omlx/a row.
func TestPrefixlessMalformedAliasNeverShadowsTheModel(t *testing.T) {
	cfg := prefixlessAliasCfg()
	if got := startKey(cfg, "omlx/a"); got != "omlx/a" {
		t.Errorf("startKey(omlx/a) = %q, want omlx/a", got)
	}
	if exp := aliasExpansion(cfg); len(exp) != 0 {
		t.Errorf("aliasExpansion = %v, want none", exp)
	}
	c := BuildCatalog(cfg, serverSnap(), nil, testSessions(), nil, testNow, CatalogOptions{})
	var n int
	for _, r := range c.Rows {
		if r.ID == "omlx/a" {
			n++
			if r.Kind == modelapi.KindAlias {
				t.Errorf("omlx/a row is an alias row: %+v", r)
			}
		}
	}
	if n != 1 {
		t.Errorf("catalog lists omlx/a %d times, want once", n)
	}

	f := &fakeLife{snap: serverSnap()}
	res := f.deps(cfg).StartJob(modelapi.StartRequest{Model: "omlx/a"}, nil)(context.Background(), func(string) {})
	if !res.OK || !slices.Contains(f.callList(), "Start omlx/a") {
		t.Errorf("start omlx/a: result %+v, engine calls %v, want the model started", res.Error, f.callList())
	}

	f = &fakeLife{snap: serverSnap("omlx/a")}
	res = f.deps(cfg).StopJob(modelapi.StopRequest{Model: "omlx/a"}, testSessions())(context.Background(), func(string) {})
	if !res.OK || len(f.stopped()) != 1 || f.stopped()[0].ModelID != "omlx/a" {
		t.Errorf("stop omlx/a: result %+v, stopped %v, want the model stopped", res.Error, f.stopped())
	}
}

// twoRig is a fake engine whose Start marks the started model running in the
// inventory (as a real start does); evict names models a start removes from
// the inventory again (a Pool eviction), by started model.
type twoRig struct {
	f      *fakeLife
	d      Deps
	evict  map[string][]string
	failOn string
	onTgt  func(id string)
}

func newTwoRig(t *testing.T, running ...string) *twoRig {
	t.Helper()
	r := &twoRig{f: &fakeLife{snap: serverSnap(running...)}, evict: map[string][]string{}}
	r.d = r.f.deps(aliasServerCfg())
	base := r.d.Start
	r.d.Start = func(ctx context.Context, c *config.Config, tg lifecycle.Target, o lifecycle.Options) error {
		if err := base(ctx, c, tg, o); err != nil {
			return err
		}
		if r.onTgt != nil {
			r.onTgt(tg.ModelID)
		}
		if tg.ModelID == r.failOn {
			return errors.New("boom")
		}
		r.f.mu.Lock()
		defer r.f.mu.Unlock()
		var now []string
		for _, e := range r.f.snap.Entries {
			if e.Running && !slices.Contains(r.evict[tg.ModelID], e.ModelID) {
				now = append(now, e.ModelID)
			}
		}
		r.f.snap = serverSnap(append(now, tg.ModelID)...)
		return nil
	}
	return r
}

func (r *twoRig) run(ctx context.Context, model string) (modelapi.JobResult, *stepLog, *sessionreg.Registry) {
	steps := &stepLog{}
	reg := sessionreg.New(time.Minute, func() time.Time { return testNow })
	return r.d.StartJob(modelapi.StartRequest{Model: model}, reg)(ctx, steps.step), steps, reg
}

// TestStartAliasStartsEveryLocalTargetInOrder pins #690: a start of an alias
// with two local targets starts both, in (order, file position) order, each
// through the per-model path, reports "starting <id> (i of n)", then confirms
// every target and touches the alias and both targets.
func TestStartAliasStartsEveryLocalTargetInOrder(t *testing.T) {
	r := newTwoRig(t)
	var asked []string
	r.d.AliasListed = func(_ context.Context, _ *config.Config, alias, target string) (bool, error) {
		asked = append(asked, alias+" "+target)
		return true, nil
	}
	steps := &stepLog{}
	reg := sessionreg.New(time.Minute, func() time.Time { return testNow })
	d := tapConfig(r.d, reg)
	_ = reg.Register("s", []string{"alias/two"})
	reg.End("s")
	res := d.StartJob(modelapi.StartRequest{Model: "alias/two"}, reg)(context.Background(), steps.step)
	if !res.OK {
		t.Fatalf("result = %+v", res.Error)
	}
	want := []string{"Start omlx/a", "Settle omlx/a", "Routed omlx/a", "Start omlx/b", "Settle omlx/b", "Routed omlx/b"}
	if got := r.f.callList(); !reflect.DeepEqual(got, want) {
		t.Errorf("engine calls %v, want %v", got, want)
	}
	var starting []string
	for _, s := range steps.list() {
		if strings.HasPrefix(s, "starting ") {
			starting = append(starting, s)
		}
	}
	if !reflect.DeepEqual(starting, []string{"starting omlx/a (1 of 2)", "starting omlx/b (2 of 2)"}) {
		t.Errorf("steps %v", starting)
	}
	if !reflect.DeepEqual(asked, []string{"alias/two omlx/a", "alias/two omlx/b"}) {
		t.Errorf("alias row checks %v, want one per target", asked)
	}
	for _, id := range []string{"alias/two", "omlx/a", "omlx/b"} {
		if _, ok := reg.IdleSince(id); !ok {
			t.Errorf("%s was not touched", id)
		}
	}
}

// TestStartAliasSkipsATargetAlreadyRunningAndRouted pins that a re-post after
// the client answered a problem resumes: a target running and routed (with the
// alias routed) is not started again.
func TestStartAliasSkipsATargetAlreadyRunningAndRouted(t *testing.T) {
	r := newTwoRig(t, "omlx/a")
	r.d.RoutedIDs = func(context.Context) (map[string]bool, error) {
		return map[string]bool{"omlx/a": true, "alias/two": true}, nil
	}
	res, _, _ := r.run(context.Background(), "alias/two")
	if !res.OK {
		t.Fatalf("result = %+v", res.Error)
	}
	if got := r.f.callList(); slices.Contains(got, "Start omlx/a") || !slices.Contains(got, "Start omlx/b") {
		t.Errorf("engine calls %v, want only omlx/b started", got)
	}
	// Running but not routed: started again so the route write runs.
	r = newTwoRig(t, "omlx/a")
	r.d.RoutedIDs = func(context.Context) (map[string]bool, error) { return map[string]bool{}, nil }
	r.d.AliasListed = func(context.Context, *config.Config, string, string) (bool, error) { return true, nil }
	if res, _, _ := r.run(context.Background(), "alias/two"); !res.OK {
		t.Fatalf("result = %+v", res.Error)
	}
	if got := r.f.callList(); !slices.Contains(got, "Start omlx/a") {
		t.Errorf("engine calls %v, want omlx/a started: it is not routed", got)
	}
}

// TestStartAliasPartialFailureNamesWhatStarted pins the no-rollback rule: when
// the second target fails, the first keeps running and the message names the
// started target, the failed one and why, and says to run the start again.
func TestStartAliasPartialFailureNamesWhatStarted(t *testing.T) {
	r := newTwoRig(t)
	r.failOn = "omlx/b"
	res, _, _ := r.run(context.Background(), "alias/two")
	p := wantProblem(t, res, modelapi.CodeFailed)
	for _, want := range []string{"started omlx/a", "omlx/b", "boom", "run the start again"} {
		if !strings.Contains(p.Message, want) {
			t.Errorf("message %q lacks %q", p.Message, want)
		}
	}
	if len(r.f.stopped()) != 0 {
		t.Errorf("stopped %v: a partial failure rolls nothing back", r.f.stopped())
	}
	// A failure of the first target is the plain per-model failure.
	r = newTwoRig(t)
	r.failOn = "omlx/a"
	p = wantProblem(t, func() modelapi.JobResult { res, _, _ := r.run(context.Background(), "alias/two"); return res }(), modelapi.CodeFailed)
	if strings.Contains(p.Message, "started") || slices.Contains(r.f.callList(), "Start omlx/b") {
		t.Errorf("message %q, calls %v: nothing started, the second must not start", p.Message, r.f.callList())
	}
}

// TestStartAliasFailsWhenALaterStartDisplacedAnEarlierTarget pins the final
// check: a Pool eviction by the second start removes the first from the
// inventory, which fails the job naming the displaced model.
func TestStartAliasFailsWhenALaterStartDisplacedAnEarlierTarget(t *testing.T) {
	r := newTwoRig(t)
	r.evict["omlx/b"] = []string{"omlx/a"}
	res, _, _ := r.run(context.Background(), "alias/two")
	p := wantProblem(t, res, modelapi.CodeFailed)
	if !strings.Contains(p.Message, "displaced omlx/a") {
		t.Errorf("message = %q, want it to name the displaced omlx/a", p.Message)
	}
}

// TestStartAliasCancelStopsBetweenTargets pins that cancelling the job after
// the first target started starts no second one.
func TestStartAliasCancelStopsBetweenTargets(t *testing.T) {
	r := newTwoRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.onTgt = func(id string) {
		if id == "omlx/a" {
			cancel()
		}
	}
	res, _, _ := r.run(ctx, "alias/two")
	p := wantProblem(t, res, modelapi.CodeCancelled)
	if slices.Contains(r.f.callList(), "Start omlx/b") || !strings.Contains(p.Message, "omlx/b") {
		t.Errorf("message %q, calls %v: want cancelled before omlx/b started", p.Message, r.f.callList())
	}
}

// TestStartKeyIsTheTargetForOneLocalTargetAndTheAliasForSeveral pins D4: a
// one-local alias still joins its target's job; a several-local alias is keyed
// by its own id.
func TestStartKeyIsTheTargetForOneLocalTargetAndTheAliasForSeveral(t *testing.T) {
	cfg := aliasServerCfg()
	if got := startKey(cfg, "alias/x"); got != "omlx/a" {
		t.Errorf("startKey(alias/x) = %q, want omlx/a", got)
	}
	if got := startKey(cfg, "alias/two"); got != "alias/two" {
		t.Errorf("startKey(alias/two) = %q, want the alias id", got)
	}
}

// TestAutoStopDoesNotForgetATargetWhileAMultiTargetAliasStartRuns pins that a
// running start of a several-local alias (keyed by the alias id) protects
// each of its targets from forgetIdle and from an auto stop.
func TestAutoStopDoesNotForgetATargetWhileAMultiTargetAliasStartRuns(t *testing.T) {
	const grace = time.Minute
	f := &fakeLife{snap: serverSnap("omlx/a")}
	s, clk, release := gatedServer(t, f, grace)
	_ = s.sessions.Register("s", []string{"alias/two"})
	s.sessions.End("s")
	clk.advance(2 * grace)
	id := postAliasStart(t, s, "alias/two")
	if !s.jobs.Pending("alias/two") {
		t.Fatal("Jobs.Pending(alias/two) is false while its start runs")
	}
	s.stopper.Tick(context.Background())
	for _, m := range []string{"omlx/a", "omlx/b", "alias/two"} {
		if _, seen := s.sessions.IdleSince(m); !seen {
			t.Errorf("%s was forgotten while the multi-target start ran", m)
		}
	}
	if got := stoppedIDs(f); len(got) != 0 {
		t.Errorf("stopped %v under a running start", got)
	}
	release()
	waitJob(t, s, id)
}

// TestSessionOnAMultiTargetAliasCountsForEveryTarget pins D7: the session
// accounting expands an alias to all its local targets.
func TestSessionOnAMultiTargetAliasCountsForEveryTarget(t *testing.T) {
	exp := aliasExpansion(aliasServerCfg())
	if got := exp["alias/two"]; !reflect.DeepEqual(got, []string{"omlx/a", "omlx/b"}) && !reflect.DeepEqual(got, []string{"omlx/b", "omlx/a"}) {
		t.Errorf("expansion of alias/two = %v, want both targets", got)
	}
	reg := sessionreg.New(time.Minute, func() time.Time { return testNow })
	reg.SetExpand(func() map[string][]string { return exp })
	_ = reg.Register("s", []string{"alias/two"})
	for _, m := range []string{"omlx/a", "omlx/b"} {
		if got := reg.Users(m); !reflect.DeepEqual(got, []string{"s"}) {
			t.Errorf("Users(%s) = %v, want the session on alias/two", m, got)
		}
	}
}

// TestStopOfAMultiTargetAliasNamesEveryTarget pins D7's message.
func TestStopOfAMultiTargetAliasNamesEveryTarget(t *testing.T) {
	f := &fakeLife{snap: serverSnap("omlx/a", "omlx/b")}
	res := f.deps(aliasServerCfg()).StopJob(modelapi.StopRequest{Model: "alias/two"}, testSessions())(context.Background(), func(string) {})
	p := wantProblem(t, res, modelapi.CodeFailed)
	if p.Message != "alias/two has nothing to stop; stop omlx/a and omlx/b" {
		t.Errorf("message = %q", p.Message)
	}
}
