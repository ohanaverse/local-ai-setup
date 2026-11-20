package hoststop

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// stopHarness builds stop deps over a fixed snapshot and session counts and
// records every stop call, so tests assert on exactly what would be stopped.
type stopHarness struct {
	snap   localmodels.Snapshot
	counts map[string]int
	stops  []string // "provider|modelName"
	failOn string   // modelName whose stop fails
	// cancelOn names a modelName whose stop cancels the picker's context, so a
	// test can simulate Ctrl+C arriving while that stop was in flight; cancel is
	// the CancelFunc for the context the test installed via stopSignalCtx.
	cancelOn string
	cancel   context.CancelFunc
	// cancelFails makes the cancelOn stop return the context's own error after
	// cancelling instead of nil — the shape a provider CLI killed by the signal
	// produces, and the only way a cancellation reaches the picker as an error.
	// Without it a cancelled stop takes the success path and never tests the
	// picker's cancelled-vs-failed distinction.
	cancelFails bool
	// ctxSeen is the context the picker handed to the first stop, so a test can
	// prove it is cancellable — context.Background() has a nil Done channel.
	ctxSeen context.Context
	// notOwed makes successful stops report no restart owed (their model had
	// no route), so a test can prove the loop then skips the settle.
	notOwed bool
	// settles counts settle calls: the proxy restarts the stop loop asked for.
	settles int
	// loading is what each family's pidfile says; loadingAsked records the
	// families the stop state read it for.
	loading      map[string]lifecycle.Loading
	loadingAsked []string
	// machine is the instance each ScopeMachine family's readiness command
	// finds on another port; machineAsked records the families asked.
	machine      map[string]localmodels.Entry
	machineAsked []string
	// finishingOn names a modelName whose stop ollama accepted and has not
	// carried out within the batch's wait (*lifecycle.StillFinishingError).
	finishingOn string
	// noticeOn names a modelName whose stop gives the wait's notice before it
	// returns, as the real one does after about two seconds.
	noticeOn string
	// accepted makes every ollama entry of the batch one whose stop ollama
	// accepted when the batch began, so Accepted lists what has not reported.
	accepted bool
	// begun is every entry handed to begin, in order.
	begun []localmodels.Entry
}

// harnessBatch is the batch begin hands the stop loop: each Stop is the
// harness's own stop, and Accepted lists the ollama entries begun and not yet
// reported when the harness says ollama accepted them.
type harnessBatch struct {
	h        *stopHarness
	answered map[string]bool
}

func (b *harnessBatch) Stop(ctx context.Context, en localmodels.Entry) (bool, error) {
	h := b.h
	if h.ctxSeen == nil {
		h.ctxSeen = ctx
	}
	h.stops = append(h.stops, en.ProviderID+"|"+en.ModelName)
	if en.ModelName == h.noticeOn {
		h.noticeCtx(ctx, lifecycle.StopNotice{Msg: "waiting for ollama to finish a request in flight (up to 3m0s)"})
	}
	if h.cancel != nil && en.ModelName == h.cancelOn {
		h.cancel()
		if h.cancelFails {
			return false, ctx.Err()
		}
	}
	b.answered[en.ModelID] = true
	if en.ModelName == h.failOn {
		return false, errors.New("boom")
	}
	if en.ModelName == h.finishingOn {
		return false, &lifecycle.StillFinishingError{Provider: "ollama", Model: en.ModelName, Waited: 180 * time.Second}
	}
	return !h.notOwed, nil
}

func (b *harnessBatch) Accepted() []localmodels.Entry {
	if !b.h.accepted {
		return nil
	}
	var out []localmodels.Entry
	for _, en := range b.h.begun {
		if en.ProviderID == "ollama" && !b.answered[en.ModelID] {
			out = append(out, en)
		}
	}
	return out
}

// noticeCtx delivers n the way lifecycle's stops do: to the function the
// stop loop put on ctx (lifecycle.WithStopNotice). It goes through a real
// lifecycle context so the test reads what the loop installed, not a copy.
func (h *stopHarness) noticeCtx(ctx context.Context, n lifecycle.StopNotice) {
	lifecycle.DeliverStopNoticeForTest(ctx, n)
}

func (h *stopHarness) deps() stopDeps {
	return stopDeps{
		inventory: func(*config.Config) localmodels.Snapshot { return h.snap },
		counts: func(ids []string) map[string]int {
			out := map[string]int{}
			for _, id := range ids {
				out[id] = h.counts[id]
			}
			return out
		},
		live: func() map[string]int { return h.counts },
		begin: func(_ context.Context, _ *config.Config, entries []localmodels.Entry) stopBatch {
			h.begun = append(h.begun, entries...)
			return &harnessBatch{h: h, answered: map[string]bool{}}
		},
		settle: func(context.Context, *config.Config) { h.settles++ },
		loading: func(_ *config.Config, fam string) lifecycle.Loading {
			h.loadingAsked = append(h.loadingAsked, fam)
			return h.loading[fam]
		},
		machine: func(_ *config.Config, _ localmodels.Snapshot, fam string) (localmodels.Entry, bool) {
			h.machineAsked = append(h.machineAsked, fam)
			en, ok := h.machine[fam]
			return en, ok
		},
	}
}

func runningEntry(provider, id, name string) localmodels.Entry {
	return localmodels.Entry{ProviderID: provider, ModelID: id, ModelName: name, Running: true}
}

// TestStopCandidatesReportsSessionCounts verifies StopState.Candidates holds every
// running stoppable model INCLUDING ones a live session uses, with the count,
// that a single-model provider (mtplx) reports its whole family's count, and
// that an omlx pool counts per model (stopping one leaves the others). `wt stop`
// needs the in-use models to warn before stopping them; the exit-of-session
// picker's hiding of them is asserted by TestStopPickerStopsOnlySelected.
func TestStopCandidatesReportsSessionCounts(t *testing.T) {
	h := &stopHarness{
		snap: localmodels.Snapshot{Entries: []localmodels.Entry{
			runningEntry("ollama", "ollama/a", "a"),
			runningEntry("ollama", "ollama/busy", "busy"),
			runningEntry("omlx", "omlx/x", "x"),
			runningEntry("omlx-6bit", "omlx-6bit/y", "y"),
			runningEntry("mtplx", "mtplx/p", "p"),
			runningEntry("mtplx", "mtplx/q", "q"),
		}},
		counts: map[string]int{"ollama/busy": 2, "omlx/x": 1, "mtplx/p": 1},
	}
	got := map[string]int{}
	for _, c := range stopState(&config.Config{}, h.deps()).Candidates {
		got[c.Entry.ModelID] = c.Sessions
	}
	want := map[string]int{"ollama/a": 0, "ollama/busy": 2, "omlx/x": 1, "omlx-6bit/y": 0, "mtplx/p": 1, "mtplx/q": 1}
	for id, n := range want {
		if got[id] != n {
			t.Errorf("Sessions[%s] = %d, want %d (all: %v)", id, got[id], n, got)
		}
	}
}

// TestStopEntriesReportsFailures verifies the exported stop loop stops every
// entry, keeps going after a failure, and reports how many failed so a CLI can
// choose a non-zero exit code.
func TestStopEntriesReportsFailures(t *testing.T) {
	h := &stopHarness{failOn: "b"}
	var out bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entries := []localmodels.Entry{runningEntry("ollama", "ollama/a", "a"), runningEntry("ollama", "ollama/b", "b")}
	err := stopEntries(ctx, &out, &config.Config{}, h.deps(), entries)
	if err == nil || !strings.Contains(err.Error(), "1 of 2 stops failed") {
		t.Fatalf("err = %v, want a 1 of 2 failure", err)
	}
	if len(h.stops) != 2 {
		t.Errorf("stops = %v, want both attempted", h.stops)
	}
}

// TestStopEntriesSettlesOnce pins issue #142: stopping several models asks for
// ONE proxy restart after the loop, not one per stop. Per-stop restarts fired
// overlapping `launchctl kickstart -k`s, each killing the proxy the previous
// one had just brought up.
func TestStopEntriesSettlesOnce(t *testing.T) {
	h := &stopHarness{}
	entries := []localmodels.Entry{
		runningEntry("ollama", "ollama/a", "a"),
		runningEntry("ollama", "ollama/b", "b"),
		runningEntry("ollama", "ollama/c", "c"),
	}
	if err := stopEntries(context.Background(), io.Discard, &config.Config{}, h.deps(), entries); err != nil {
		t.Fatal(err)
	}
	if len(h.stops) != 3 || h.settles != 1 {
		t.Fatalf("stops = %v, settles = %d, want 3 stops and 1 settle", h.stops, h.settles)
	}
}

// TestStopEntriesSettlesAfterFailureAndCancel pins that a loop ending early
// still settles: the removals already written are in config.yaml, and without
// the restart the proxy keeps routing to models that are gone.
func TestStopEntriesSettlesAfterFailureAndCancel(t *testing.T) {
	entries := []localmodels.Entry{runningEntry("ollama", "ollama/a", "a"), runningEntry("ollama", "ollama/b", "b")}

	failed := &stopHarness{failOn: "b"}
	if err := stopEntries(context.Background(), io.Discard, &config.Config{}, failed.deps(), entries); err == nil {
		t.Fatal("want a failure error")
	}
	if failed.settles != 1 {
		t.Errorf("after a failed stop: settles = %d, want 1", failed.settles)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelled := &stopHarness{cancelOn: "a", cancel: cancel}
	// The error is ErrStopCancelled itself: `wt-model stop --all` tells a Ctrl+C
	// from a failed stop by it, and halts the omlx service after the second
	// only.
	if err := stopEntries(ctx, io.Discard, &config.Config{}, cancelled.deps(), entries); !errors.Is(err, ErrStopCancelled) {
		t.Fatalf("err = %v, want ErrStopCancelled", err)
	}
	if len(cancelled.stops) != 1 || cancelled.settles != 1 {
		t.Errorf("after Ctrl+C: stops = %v, settles = %d, want 1 stop and 1 settle", cancelled.stops, cancelled.settles)
	}
}

// TestStopEntriesSkipsSettleWhenNothingOwed pins that stops which changed no
// route (unrouted models, or every stop failed) cost no proxy restart.
func TestStopEntriesSkipsSettleWhenNothingOwed(t *testing.T) {
	entries := []localmodels.Entry{runningEntry("ollama", "ollama/a", "a"), runningEntry("ollama", "ollama/b", "b")}

	unrouted := &stopHarness{notOwed: true}
	_ = stopEntries(context.Background(), io.Discard, &config.Config{}, unrouted.deps(), entries)
	if unrouted.settles != 0 {
		t.Errorf("unrouted models: settles = %d, want 0", unrouted.settles)
	}

	allFailed := &stopHarness{failOn: "a"}
	_ = stopEntries(context.Background(), io.Discard, &config.Config{}, allFailed.deps(), entries[:1])
	if allFailed.settles != 0 {
		t.Errorf("every stop failed: settles = %d, want 0", allFailed.settles)
	}
}

// TestStopCandidatesIncludeAModelMidLoad verifies the loading flag does not
// hide a model from `wt stop`: it stays a stop candidate, in `wt stop <id>` and
// in the stop picker. A user who sees the model occupying the pool must be able
// to name it there. (omlx 0.7.0 refuses to unload a model mid-load, so the stop
// itself reports that omlx is still loading <id> and will not unload it
// mid-load; `wt-model stop omlx` is what calls a load off, and the message says so.)
func TestStopCandidatesIncludeAModelMidLoad(t *testing.T) {
	loading := runningEntry("omlx", "omlx/x", "x")
	loading.Loading = true
	h := &stopHarness{snap: localmodels.Snapshot{Entries: []localmodels.Entry{loading}}}
	cands := stopState(&config.Config{}, h.deps()).Candidates
	if len(cands) != 1 || cands[0].Entry.ModelID != "omlx/x" {
		t.Errorf("candidates = %+v, want the loading omlx/x", cands)
	}
}

// TestStopEntriesStillFinishingIsNotAFailure pins #379's first item: a stop
// ollama accepted and has not carried out when the wait's bound is spent is
// its own outcome. It is reported as still finishing, never as "done" (the
// model is still loaded) and never as "failed" (it unloads once its request
// ends): the loop returns nil, so `wt stop` exits 0, and the batch goes on.
func TestStopEntriesStillFinishingIsNotAFailure(t *testing.T) {
	h := &stopHarness{finishingOn: "a"}
	var out bytes.Buffer
	entries := []localmodels.Entry{runningEntry("ollama", "ollama/a", "a"), runningEntry("ollama", "ollama/b", "b")}
	if err := stopEntries(context.Background(), &out, &config.Config{}, h.deps(), entries); err != nil {
		t.Fatalf("err = %v, want nil: a stop ollama accepted is not a failed stop", err)
	}
	want := "Stopping ollama/a... still finishing a request; ollama unloads it when that ends\nStopping ollama/b... done\n"
	if out.String() != want {
		t.Fatalf("output:\n%s\nwant:\n%s", out.String(), want)
	}
	if len(h.stops) != 2 {
		t.Errorf("stops = %v, want both: the batch goes on past a model still finishing", h.stops)
	}
}

// TestStopEntriesBeginsTheWholeBatchFirst pins #379's second item at the
// loop: every entry reaches the batch (lifecycle.BeginStops sends every
// ollama stop) before the first model is reported, so the waits overlap and
// share one bound instead of running one after another.
func TestStopEntriesBeginsTheWholeBatchFirst(t *testing.T) {
	entries := []localmodels.Entry{
		runningEntry("ollama", "ollama/a", "a"),
		runningEntry("mtplx", "mtplx/m", "m"),
		runningEntry("ollama", "ollama/b", "b"),
	}
	h := &stopHarness{}
	d := h.deps()
	begin := d.begin
	var begunAtFirstStop int
	d.begin = func(ctx context.Context, cfg *config.Config, es []localmodels.Entry) stopBatch {
		b := begin(ctx, cfg, es)
		return stopFunc{b, func() {
			if begunAtFirstStop == 0 {
				begunAtFirstStop = len(h.begun)
			}
		}}
	}
	if err := stopEntries(context.Background(), io.Discard, &config.Config{}, d, entries); err != nil {
		t.Fatal(err)
	}
	if begunAtFirstStop != 3 || !slices.Equal(h.begun, entries) {
		t.Fatalf("begun = %v (%d at the first stop), want all 3 entries before any stop", h.begun, begunAtFirstStop)
	}
	if want := []string{"ollama|a", "mtplx|m", "ollama|b"}; !slices.Equal(h.stops, want) {
		t.Errorf("stops = %v, want %v: models are still reported in the caller's order", h.stops, want)
	}
}

// stopFunc is a stopBatch that runs before ahead of each Stop.
type stopFunc struct {
	stopBatch
	before func()
}

func (s stopFunc) Stop(ctx context.Context, en localmodels.Entry) (bool, error) {
	s.before()
	return s.stopBatch.Stop(ctx, en)
}

// TestStopEntriesPrintsTheWaitNoticeOnTheOpenLine pins #379's third item:
// the notice a stop gives while it waits for ollama (lifecycle.StopNotice)
// continues the model's open "Stopping x... " line on the loop's own writer,
// so a line that can sit open for minutes says why. It never goes to stderr
// or anywhere else the caller did not hand the loop: the exit flow's picker
// prints only through its writer, after the TUI's alt screen is gone.
func TestStopEntriesPrintsTheWaitNoticeOnTheOpenLine(t *testing.T) {
	r, wr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prev := os.Stderr
	os.Stderr = wr
	t.Cleanup(func() { os.Stderr = prev })

	h := &stopHarness{noticeOn: "a"}
	var out bytes.Buffer
	entries := []localmodels.Entry{runningEntry("ollama", "ollama/a", "a")}
	if err := stopEntries(context.Background(), &out, &config.Config{}, h.deps(), entries); err != nil {
		t.Fatal(err)
	}
	os.Stderr = prev
	_ = wr.Close()
	stderr, _ := io.ReadAll(r)

	want := "Stopping ollama/a... waiting for ollama to finish a request in flight (up to 3m0s)... done\n"
	if out.String() != want {
		t.Fatalf("output:\n%q\nwant:\n%q", out.String(), want)
	}
	if len(stderr) != 0 {
		t.Errorf("stderr = %q, want nothing: the notice belongs to the loop's writer", stderr)
	}
}

// TestStopEntriesCancelNamesStopsOllamaAccepted pins what a Ctrl+C says once
// the batch has sent every ollama stop: the models after the one in flight
// are skipped by the loop, but ollama accepted their stops and still carries
// them out. Without the line, "cancelled" reads as "nothing else was
// touched" while those models go on to unload.
func TestStopEntriesCancelNamesStopsOllamaAccepted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := &stopHarness{cancelOn: "a", cancel: cancel, cancelFails: true, accepted: true}
	var out bytes.Buffer
	entries := []localmodels.Entry{
		runningEntry("ollama", "ollama/a", "a"),
		runningEntry("ollama", "ollama/b", "b"),
		runningEntry("ollama", "ollama/c", "c"),
	}
	if err := stopEntries(ctx, &out, &config.Config{}, h.deps(), entries); !errors.Is(err, ErrStopCancelled) {
		t.Fatalf("err = %v, want ErrStopCancelled", err)
	}
	want := "Stopping ollama/a... cancelled\nwt: ollama had already accepted the stop of ollama/a, ollama/b, ollama/c; it unloads each one once any request in flight ends (`ollama ps` shows when)\n"
	if out.String() != want {
		t.Fatalf("output:\n%s\nwant:\n%s", out.String(), want)
	}
}
