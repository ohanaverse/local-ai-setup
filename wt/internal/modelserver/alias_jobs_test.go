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
