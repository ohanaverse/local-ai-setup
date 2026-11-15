package modelserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tiers"
)

// fakeLife is a fake lifecycle engine: every Deps function field is a closure
// over it that records the call. Safe for concurrent use, since auto-stop
// jobs run on the job manager's worker goroutine.
type fakeLife struct {
	mu    sync.Mutex
	snap  localmodels.Snapshot
	calls []string
	opts  []lifecycle.Options
	stops []localmodels.Entry

	startErr  error
	startOut  string // written to Options.Out by Start
	settleErr error
	routedErr error
	stopOwed  bool
	stopErr   error
	panicIn   string // "inventory" makes Inventory panic
}

func (f *fakeLife) record(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, s)
}

func (f *fakeLife) callList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeLife) stopped() []localmodels.Entry {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]localmodels.Entry(nil), f.stops...)
}

type fakeBatch struct {
	f *fakeLife
}

func (b fakeBatch) Stop(_ context.Context, en localmodels.Entry) (bool, error) {
	b.f.record("Stop " + en.ModelID)
	b.f.mu.Lock()
	defer b.f.mu.Unlock()
	b.f.stops = append(b.f.stops, en)
	return b.f.stopOwed, b.f.stopErr
}

func (f *fakeLife) deps(cfg *config.Config) Deps {
	return Deps{
		Config: cfg,
		Inventory: func(*config.Config) localmodels.Snapshot {
			if f.panicIn == "inventory" {
				panic("inventory exploded")
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.snap
		},
		Tiers: func() tiers.Index { return nil },
		Now:   func() time.Time { return testNow },
		Start: func(_ context.Context, _ *config.Config, t lifecycle.Target, o lifecycle.Options) error {
			f.record("Start " + t.ModelID)
			f.mu.Lock()
			f.opts = append(f.opts, o)
			f.mu.Unlock()
			if f.startOut != "" && o.Out != nil {
				_, _ = io.WriteString(o.Out, f.startOut)
			}
			return f.startErr
		},
		Settle: func(_ context.Context, _ io.Writer, _ *config.Config, t lifecycle.Target) error {
			f.record("Settle " + t.ModelID)
			return f.settleErr
		},
		BeginStops: func(_ context.Context, _ *config.Config, entries []localmodels.Entry) StopBatcher {
			ids := make([]string, len(entries))
			for i, e := range entries {
				ids[i] = e.ModelID
			}
			f.record("BeginStops " + strings.Join(ids, ","))
			return fakeBatch{f: f}
		},
		SettleRoutes: func(context.Context, io.Writer, *config.Config) { f.record("SettleRoutes") },
		Routed: func(_ context.Context, id string) error {
			f.record("Routed " + id)
			return f.routedErr
		},
	}
}

// serverCfg is a registry with a Pool provider (omlx), an Exclusive one
// (mtplx), a cloud one and one wt has no start backend for (llamacpp).
func serverCfg() *config.Config {
	return &config.Config{
		Providers: []config.Provider{
			{ID: "openrouter", Location: config.LocationCloud},
			{ID: "omlx", Location: config.LocationLocal},
			{ID: "mtplx", Location: config.LocationLocal},
			{ID: "llamacpp", Location: config.LocationLocal},
		},
		Models: []config.Model{
			{ID: "omlx/a", Family: "f", ProviderID: "omlx", ModelName: "a"},
			{ID: "omlx/b", Family: "f", ProviderID: "omlx", ModelName: "b"},
			{ID: "mtplx/x", Family: "f", ProviderID: "mtplx", ModelName: "x"},
			{ID: "mtplx/y", Family: "f", ProviderID: "mtplx", ModelName: "y"},
			{ID: "openrouter/c", Family: "f", ProviderID: "openrouter", ModelName: "c"},
			{ID: "llamacpp/l", Family: "f", ProviderID: "llamacpp", ModelName: "l"},
		},
	}
}

func entry(provider, name string, running bool) localmodels.Entry {
	return localmodels.Entry{
		ProviderID: provider, Artifact: name, ModelID: provider + "/" + name, ModelName: name,
		Registered: true, Running: running, ArtifactKnown: true,
	}
}

// serverSnap has every local model on disk; the ones named in running run.
func serverSnap(running ...string) localmodels.Snapshot {
	snap := localmodels.Snapshot{Providers: map[string]localmodels.Status{
		"omlx": localmodels.StatusOK, "mtplx": localmodels.StatusOK,
	}}
	for _, pm := range [][2]string{{"omlx", "a"}, {"omlx", "b"}, {"mtplx", "x"}, {"mtplx", "y"}, {"llamacpp", "l"}} {
		snap.Entries = append(snap.Entries, entry(pm[0], pm[1], slices.Contains(running, pm[0]+"/"+pm[1])))
	}
	return snap
}

// stepLog records the steps a job reports.
type stepLog struct {
	mu    sync.Mutex
	steps []string
}

func (s *stepLog) step(v string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.steps = append(s.steps, v)
}

func (s *stepLog) list() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.steps...)
}

func wantProblem(t *testing.T, res modelapi.JobResult, code string) *modelapi.Problem {
	t.Helper()
	if res.OK || res.Error == nil {
		t.Fatalf("result = %+v, want a %q problem", res, code)
	}
	if res.Error.Code != code {
		t.Fatalf("problem code = %q (%s), want %q", res.Error.Code, res.Error.Message, code)
	}
	return res.Error
}

// TestStartSuccessWaitsForRoute pins the order of a successful start: the
// engine's Start, then SettleStart, then the wait for the gateway to list the
// id; the job is OK only after all three, and the gateway wait is a step.
func TestStartSuccessWaitsForRoute(t *testing.T) {
	f := &fakeLife{snap: serverSnap()}
	var steps stepLog
	res := f.deps(serverCfg()).StartJob(modelapi.StartRequest{Model: "omlx/a"})(context.Background(), steps.step)
	if !res.OK || res.Error != nil {
		t.Fatalf("result = %+v, want OK", res)
	}
	want := []string{"Start omlx/a", "Settle omlx/a", "Routed omlx/a"}
	if got := f.callList(); !slices.Equal(got, want) {
		t.Fatalf("calls = %q, want %q", got, want)
	}
	if !slices.Contains(steps.list(), "waiting for the gateway") {
		t.Errorf("steps = %q, want one %q", steps.list(), "waiting for the gateway")
	}
}

// TestStartOccupiedIsReportedNotLooped pins that a refusal is handed back to
// the client as an occupied problem carrying the occupants and the other
// providers' residents, after exactly one Start call: the job never answers
// the question itself.
func TestStartOccupiedIsReportedNotLooped(t *testing.T) {
	f := &fakeLife{snap: serverSnap("mtplx/y"), startErr: &lifecycle.OccupiedError{
		Occupants:     []localmodels.Entry{entry("mtplx", "y", true)},
		Others:        []lifecycle.Resident{{Entry: entry("omlx", "b", true), Stoppable: true}},
		OthersUnknown: []string{"sushi"},
	}}
	res := f.deps(serverCfg()).StartJob(modelapi.StartRequest{Model: "mtplx/x"})(context.Background(), func(string) {})
	p := wantProblem(t, res, modelapi.CodeOccupied)
	if got := f.callList(); !slices.Equal(got, []string{"Start mtplx/x"}) {
		t.Fatalf("calls = %q, want one Start and nothing else", got)
	}
	if !slices.Equal(p.Occupants, []string{"mtplx/y"}) {
		t.Errorf("occupants = %q, want [mtplx/y]", p.Occupants)
	}
	if want := []modelapi.Resident{{ID: "omlx/b", Label: "omlx/b", Stoppable: true}}; !slices.Equal(p.Others, want) {
		t.Errorf("others = %+v, want %+v", p.Others, want)
	}
	if !slices.Equal(p.OthersUnknown, []string{"sushi"}) {
		t.Errorf("others_unknown = %q, want [sushi]", p.OthersUnknown)
	}
	if p.Message == "" {
		t.Error("problem has no message")
	}
}

// TestStartOthersOnlyIsOthersCode pins that a refusal asking only about other
// providers' residents (no same-family occupant) is the "others" problem, the
// one a client answers with others=keep|stop rather than allow_replace.
func TestStartOthersOnlyIsOthersCode(t *testing.T) {
	f := &fakeLife{snap: serverSnap("omlx/b"), startErr: &lifecycle.OccupiedError{
		Others: []lifecycle.Resident{{Entry: entry("omlx", "b", true), Stoppable: true}},
	}}
	res := f.deps(serverCfg()).StartJob(modelapi.StartRequest{Model: "mtplx/x"})(context.Background(), func(string) {})
	p := wantProblem(t, res, modelapi.CodeOthers)
	if len(p.Others) != 1 || p.Others[0].ID != "omlx/b" {
		t.Errorf("others = %+v, want omlx/b", p.Others)
	}
}

// TestStartRefusalAlreadyAnsweredIsFailed pins that a refusal asking only
// what the request already answered (allow_replace given, others decided) is
// a plain failure, never another occupied/others problem the client would
// answer the same way forever.
func TestStartRefusalAlreadyAnsweredIsFailed(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"occupied", &lifecycle.OccupiedError{
			Occupants: []localmodels.Entry{entry("mtplx", "y", true)},
			Others:    []lifecycle.Resident{{Entry: entry("omlx", "b", true), Stoppable: true}},
		}},
		{"unknown occupancy", &lifecycle.OccupancyUnknownError{ProviderID: "mtplx", Origin: "http://127.0.0.1:8003"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeLife{snap: serverSnap(), startErr: tc.err}
			req := modelapi.StartRequest{Model: "mtplx/x", AllowReplace: true, Others: &modelapi.OthersAnswer{}}
			res := f.deps(serverCfg()).StartJob(req)(context.Background(), func(string) {})
			wantProblem(t, res, modelapi.CodeFailed)
			if n := len(f.callList()); n != 1 {
				t.Errorf("%d calls, want exactly one Start", n)
			}
		})
	}
}

// TestStartOccupancyUnknown pins that an unreadable target family is the
// occupancy_unknown problem the client answers with allow_replace.
func TestStartOccupancyUnknown(t *testing.T) {
	f := &fakeLife{snap: serverSnap(), startErr: &lifecycle.OccupancyUnknownError{ProviderID: "mtplx", Origin: "http://127.0.0.1:8003"}}
	res := f.deps(serverCfg()).StartJob(modelapi.StartRequest{Model: "mtplx/x"})(context.Background(), func(string) {})
	wantProblem(t, res, modelapi.CodeOccupancyUnknown)
}

// TestStartCarriesAnswersIntoOptions pins that the request's answers reach
// the engine: allow_replace as AllowReplace, an others answer as a Decided
// OthersAnswer with its Stop list, no answer as an undecided one; the job
// also hands the engine an output writer and a progress callback that turns
// stages into their labels.
func TestStartCarriesAnswersIntoOptions(t *testing.T) {
	f := &fakeLife{snap: serverSnap()}
	d := f.deps(serverCfg())
	var steps stepLog
	d.Start = func(_ context.Context, _ *config.Config, _ lifecycle.Target, o lifecycle.Options) error {
		f.mu.Lock()
		f.opts = append(f.opts, o)
		f.mu.Unlock()
		o.Progress(lifecycle.StageWarming)
		return nil
	}
	d.StartJob(modelapi.StartRequest{Model: "omlx/a", AllowReplace: true, Others: &modelapi.OthersAnswer{Stop: []string{"mtplx/y"}}})(context.Background(), steps.step)
	d.StartJob(modelapi.StartRequest{Model: "omlx/a"})(context.Background(), func(string) {})
	if len(f.opts) != 2 {
		t.Fatalf("%d Start calls, want 2", len(f.opts))
	}
	o := f.opts[0]
	if !o.AllowReplace || !o.Others.Decided || !slices.Equal(o.Others.Stop, []string{"mtplx/y"}) {
		t.Errorf("answered options = %+v, want AllowReplace and Others{Decided, [mtplx/y]}", o)
	}
	if o.Out == nil || o.Progress == nil {
		t.Error("options carry no Out writer or Progress callback")
	}
	if o.RestartOwed {
		t.Error("RestartOwed set; a server start owes no earlier restart")
	}
	if !slices.Contains(steps.list(), lifecycle.StageLabel(lifecycle.StageWarming)) {
		t.Errorf("steps = %q, want the warming stage label", steps.list())
	}
	if o := f.opts[1]; o.AllowReplace || o.Others.Decided || o.Others.Stop != nil {
		t.Errorf("unanswered options = %+v, want zero answers", o)
	}
}

// TestStartUnknownAndBlockedRows pins the rows a start refuses before the
// engine is reached: an id no row has (unknown_model), a local model wt has
// no start backend for (blocked, with the row's reason) and a cloud model
// (nothing to start: failed).
func TestStartUnknownAndBlockedRows(t *testing.T) {
	for _, tc := range []struct{ id, code, msg string }{
		{"omlx/nope", modelapi.CodeUnknownModel, "omlx/nope"},
		{"llamacpp/l", modelapi.CodeBlocked, "llamacpp/l"},
		{"openrouter/c", modelapi.CodeFailed, "cloud"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			f := &fakeLife{snap: serverSnap()}
			res := f.deps(serverCfg()).StartJob(modelapi.StartRequest{Model: tc.id})(context.Background(), func(string) {})
			p := wantProblem(t, res, tc.code)
			if !strings.Contains(p.Message, tc.msg) {
				t.Errorf("message %q does not mention %q", p.Message, tc.msg)
			}
			if got := f.callList(); len(got) != 0 {
				t.Errorf("calls = %q, want none", got)
			}
		})
	}
}

// TestStartStoppedDuringSettle pins that a model stopped again while its
// routes settled (SettleStart's *StoppedError) is the stopped problem, and
// the gateway is not waited for.
func TestStartStoppedDuringSettle(t *testing.T) {
	f := &fakeLife{snap: serverSnap(), settleErr: &lifecycle.StoppedError{Why: "the server went away"}}
	res := f.deps(serverCfg()).StartJob(modelapi.StartRequest{Model: "omlx/a"})(context.Background(), func(string) {})
	p := wantProblem(t, res, modelapi.CodeStopped)
	if !strings.Contains(p.Message, "omlx/a") {
		t.Errorf("message %q does not name the model", p.Message)
	}
	if got := f.callList(); slices.Contains(got, "Routed omlx/a") {
		t.Errorf("calls = %q, want no gateway wait after a stop", got)
	}
}

// TestStartFailureCarriesEngineOutput pins that an engine failure is the
// failed problem worded by lifecycle.StartErrorMessage, with what the engine
// printed to Options.Out appended so the client sees it.
func TestStartFailureCarriesEngineOutput(t *testing.T) {
	f := &fakeLife{snap: serverSnap(), startErr: errors.New("boom"), startOut: "wt: route not updated\n"}
	res := f.deps(serverCfg()).StartJob(modelapi.StartRequest{Model: "omlx/a"})(context.Background(), func(string) {})
	p := wantProblem(t, res, modelapi.CodeFailed)
	if !strings.Contains(p.Message, lifecycle.StartErrorMessage("omlx/a", errors.New("boom"))) || !strings.Contains(p.Message, "route not updated") {
		t.Errorf("message = %q, want the start error and the engine output", p.Message)
	}
}

// TestStartCancelled pins that a start or gateway wait ended by the job's
// cancellation is the cancelled problem, not a failure.
func TestStartCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, f := range []*fakeLife{
		{snap: serverSnap(), startErr: fmt.Errorf("waiting: %w", context.Canceled)},
		{snap: serverSnap(), routedErr: context.Canceled},
	} {
		res := f.deps(serverCfg()).StartJob(modelapi.StartRequest{Model: "omlx/a"})(ctx, func(string) {})
		wantProblem(t, res, modelapi.CodeCancelled)
	}
}

// TestStartGatewayNeverListsIsFailed pins that a start whose id never shows
// up through the gateway is a failure naming the model: the job promises the
// model answers through the gateway, not just that a server came up.
func TestStartGatewayNeverListsIsFailed(t *testing.T) {
	f := &fakeLife{snap: serverSnap(), routedErr: errors.New("not listed after 30s")}
	res := f.deps(serverCfg()).StartJob(modelapi.StartRequest{Model: "omlx/a"})(context.Background(), func(string) {})
	p := wantProblem(t, res, modelapi.CodeFailed)
	if !strings.Contains(p.Message, "omlx/a") || !strings.Contains(p.Message, "not listed") {
		t.Errorf("message = %q, want the model and the cause", p.Message)
	}
}

// TestJobRecoversFromPanic pins that a panic inside a job (here in the
// inventory probe) becomes a failed result instead of crashing the daemon,
// for both start and stop jobs.
func TestJobRecoversFromPanic(t *testing.T) {
	f := &fakeLife{panicIn: "inventory"}
	d := f.deps(serverCfg())
	res := d.StartJob(modelapi.StartRequest{Model: "omlx/a"})(context.Background(), func(string) {})
	p := wantProblem(t, res, modelapi.CodeFailed)
	if !strings.Contains(p.Message, "inventory exploded") {
		t.Errorf("start message = %q, want the panic value", p.Message)
	}
	res = d.StopJob(modelapi.StopRequest{Model: "omlx/a"}, testSessions())(context.Background(), func(string) {})
	wantProblem(t, res, modelapi.CodeFailed)
}
