package lifecycle

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// evictingStart is a start of omlx/B, which fits beside the loaded omlx/A by
// wt's reckoning, on a pool that unloads A all the same; the occupant hook is
// the production one (routeAfterOccupantStopped), and every write of
// config.yaml fails. It is the start that prints the most: the eviction with
// its "(not predicted)", the route removal that failed and the route add
// that failed. It returns what the start returned.
func evictingStart(t *testing.T, opts Options) error {
	t.Helper()
	sizes := map[string]int64{"A": 20, "B": 20}
	fp := &fakePool{loaded: map[string]bool{"A": true, "B": false}, sizes: sizes, ceiling: 100, evictOnLoad: []string{"A"}}
	e, _ := poolEnv(t, poolSnap(100, sizes, "A"))
	e.onOccupantStopped = routeAfterOccupantStopped
	return startWith(context.Background(), e, provCfg("omlx", fp.serve(t)), Target{ProviderID: "omlx", ModelName: "B", ModelID: "omlx/B"}, opts)
}

const evictingStartLines = "wt: omlx unloaded omlx/A to make room (not predicted)\n" +
	"wt: LiteLLM route not updated: open /scratch/config.yaml: permission denied\n" +
	"wt: LiteLLM route not updated: open /scratch/config.yaml: permission denied\n"

// TestStartSendsEveryLineItPrintsToOut pins #275 in the engine: a caller that
// owns the screen hands Start a writer and gets every line the start prints,
// in the engine's own words, and nothing reaches stderr. The model picker
// passed no writer, so these three lines were written to the terminal under
// its alt screen and lost — among them the one that says why the model that
// was just started answers "Invalid model name" through LiteLLM.
func TestStartSendsEveryLineItPrintsToOut(t *testing.T) {
	_, stderr := stubRoutes(t, litellm.Result{}, errors.New("open /scratch/config.yaml: permission denied"))
	var out bytes.Buffer
	var stages []Stage
	if err := evictingStart(t, Options{Out: &out, Progress: func(s Stage) { stages = append(stages, s) }}); err != nil {
		t.Fatalf("start = %v, want nil: a failed route write does not fail the start", err)
	}
	if out.String() != evictingStartLines {
		t.Errorf("Out = %q, want %q", out.String(), evictingStartLines)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want nothing: the caller asked for the lines", stderr.String())
	}
	if len(stages) == 0 || stages[len(stages)-1] != StageRouting {
		t.Errorf("stages = %v, want them to end with %q", stages, StageRouting)
	}
}

// TestStartWithNoOutPrintsToStderr pins what `wt start` gets: it passes no
// writer, and the same lines go to stderr, as they always have.
func TestStartWithNoOutPrintsToStderr(t *testing.T) {
	_, stderr := stubRoutes(t, litellm.Result{}, errors.New("open /scratch/config.yaml: permission denied"))
	if err := evictingStart(t, Options{}); err != nil {
		t.Fatal(err)
	}
	if stderr.String() != evictingStartLines {
		t.Errorf("stderr = %q, want %q", stderr.String(), evictingStartLines)
	}
}

// TestALateRestartWarningFollowsTheStartToOut pins the half of the output
// that arrives after Start has returned. The proxy restart runs on its own
// goroutine; RoutesPending says one is in flight, WaitPendingRoutes waits
// for it, and what it prints goes to the writer of the start that caused it,
// so a caller that waits has the whole text.
func TestALateRestartWarningFollowsTheStartToOut(t *testing.T) {
	_, stderr := stubRoutes(t, litellm.Result{Changed: true}, nil)
	release := make(chan struct{})
	restartProxy = func(context.Context) []string {
		<-release
		return []string{"LiteLLM restart failed: exit status 1"}
	}
	if RoutesPending() {
		t.Fatal("a restart is pending before the start")
	}
	e := testEnv()
	e.inventory = func(*config.Config) localmodels.Snapshot { return localmodels.Snapshot{} }
	var calls []string
	e.backends = map[string]backend{"mtplx": &fakeBackend{single: true, calls: &calls}}
	var out bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	err := startWith(ctx, e, provCfg("mtplx", "http://127.0.0.1:9"), Target{ProviderID: "mtplx", ModelName: "M", ModelID: "mtplx/M"}, Options{Out: &out})
	cancel() // as the picker does once the start has returned
	if err != nil {
		t.Fatal(err)
	}
	if !RoutesPending() {
		t.Error("RoutesPending = false while the restart is held, want true")
	}
	close(release)
	WaitPendingRoutes()
	if RoutesPending() {
		t.Error("RoutesPending = true after WaitPendingRoutes, want false")
	}
	want := "wt: LiteLLM restart failed: exit status 1\n"
	if out.String() != want {
		t.Errorf("Out = %q, want %q", out.String(), want)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want nothing", stderr.String())
	}
}
