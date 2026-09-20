package survey

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// stopHarness builds picker deps over a fixed snapshot and refcount map and
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
	// ctxSeen is the context the picker handed to the first stop, so a test can
	// prove it is cancellable — context.Background() has a nil Done channel.
	ctxSeen context.Context
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
		stop: func(ctx context.Context, _ *config.Config, provider, name string) error {
			if h.ctxSeen == nil {
				h.ctxSeen = ctx
			}
			h.stops = append(h.stops, provider+"|"+name)
			if h.cancel != nil && name == h.cancelOn {
				h.cancel()
			}
			if name == h.failOn {
				return errors.New("boom")
			}
			return nil
		},
	}
}

func runningEntry(provider, id, name string) localmodels.Entry {
	return localmodels.Entry{ProviderID: provider, ModelID: id, ModelName: name, Running: true}
}

// TestStopPickerSilentWhenNothingToOffer verifies the picker prints nothing
// when no model is running, when the running one is in use by another session,
// or when it is on a provider wt cannot stop. The spec requires skipping the
// picker and all its output entirely, so a plain exit stays quiet.
func TestStopPickerSilentWhenNothingToOffer(t *testing.T) {
	cases := map[string]*stopHarness{
		"none running": {snap: localmodels.Snapshot{Entries: []localmodels.Entry{
			{ProviderID: "ollama", ModelID: "ollama/a", ModelName: "a"}}}},
		"in use elsewhere": {
			snap:   localmodels.Snapshot{Entries: []localmodels.Entry{runningEntry("ollama", "ollama/a", "a")}},
			counts: map[string]int{"ollama/a": 1}},
		"unsupported provider": {snap: localmodels.Snapshot{Entries: []localmodels.Entry{
			runningEntry("mlx_lm_server", "mlx_lm_server/a", "a")}}},
		"untrusted probe": {snap: localmodels.Snapshot{
			Entries:   []localmodels.Entry{runningEntry("ollama", "ollama/a", "a")},
			Providers: map[string]localmodels.Status{"ollama": localmodels.StatusPartial}}},
	}
	for name, h := range cases {
		var out bytes.Buffer
		runStopPicker(strings.NewReader("all\n\n"), &out, &config.Config{}, h.deps())
		if out.Len() != 0 || len(h.stops) != 0 {
			t.Errorf("%s: output = %q stops = %v, want none", name, out.String(), h.stops)
		}
	}
}

// TestStopPickerStopsOnlySelected verifies toggling by number then Enter
// stops exactly the ticked models, and a model another session uses is never
// offered or stopped (issue #115's "ref count zero" rule).
func TestStopPickerStopsOnlySelected(t *testing.T) {
	h := &stopHarness{
		snap: localmodels.Snapshot{Entries: []localmodels.Entry{
			runningEntry("ollama", "ollama/a", "a:1b"),
			runningEntry("omlx", "omlx/b", "b"),
			runningEntry("ollama", "ollama/busy", "busy"),
		}},
		counts: map[string]int{"ollama/busy": 2},
	}
	var out bytes.Buffer
	runStopPicker(strings.NewReader("2\n\n"), &out, &config.Config{}, h.deps())
	if len(h.stops) != 1 || h.stops[0] != "omlx|b" {
		t.Fatalf("stops = %v, want [omlx|b]", h.stops)
	}
	if strings.Contains(out.String(), "busy") {
		t.Errorf("output offers the in-use model: %q", out.String())
	}
	if !strings.Contains(out.String(), "Stopping omlx/b... done") {
		t.Errorf("output = %q, want a done line for omlx/b", out.String())
	}
}

// TestStopPickerSkipPaths verifies Enter with nothing selected, "q", "esc"
// and an exhausted reader all stop nothing: nothing is pre-selected, because
// stopping costs a model reload and must be an explicit choice.
func TestStopPickerSkipPaths(t *testing.T) {
	for _, input := range []string{"\n", "q\n", "esc\n", "", "all\nnone\n\n"} {
		h := &stopHarness{snap: localmodels.Snapshot{Entries: []localmodels.Entry{
			runningEntry("ollama", "ollama/a", "a")}}}
		var out bytes.Buffer
		runStopPicker(strings.NewReader(input), &out, &config.Config{}, h.deps())
		if len(h.stops) != 0 {
			t.Errorf("input %q: stops = %v, want none", input, h.stops)
		}
	}
}

// TestStopPickerAllAndFailureContinues verifies "all" selects every offered
// model, and that one failed stop prints "failed" and does not prevent the
// rest from being stopped, so a stuck model never leaves the others running.
func TestStopPickerAllAndFailureContinues(t *testing.T) {
	h := &stopHarness{
		snap: localmodels.Snapshot{Entries: []localmodels.Entry{
			runningEntry("ollama", "ollama/a", "a"),
			runningEntry("ollama", "ollama/b", "b"),
		}},
		failOn: "a",
	}
	var out bytes.Buffer
	runStopPicker(strings.NewReader("all\n\n"), &out, &config.Config{}, h.deps())
	if len(h.stops) != 2 {
		t.Fatalf("stops = %v, want both attempted", h.stops)
	}
	if !strings.Contains(out.String(), "Stopping ollama/a... failed: boom") ||
		!strings.Contains(out.String(), "Stopping ollama/b... done") {
		t.Errorf("output = %q, want a failed line then a done line", out.String())
	}
}

// TestStopPickerInvalidInputReprompts verifies junk and out-of-range numbers
// re-prompt with a hint instead of toggling or exiting, so a typo cannot stop
// the wrong model.
func TestStopPickerInvalidInputReprompts(t *testing.T) {
	h := &stopHarness{snap: localmodels.Snapshot{Entries: []localmodels.Entry{
		runningEntry("ollama", "ollama/a", "a")}}}
	var out bytes.Buffer
	runStopPicker(strings.NewReader("zzz\n9\nq\n"), &out, &config.Config{}, h.deps())
	if len(h.stops) != 0 {
		t.Fatalf("stops = %v, want none", h.stops)
	}
	if strings.Count(out.String(), "type a number to toggle") != 2 {
		t.Errorf("output = %q, want two hints", out.String())
	}
}

// TestPickerNoopWhenNotTTY verifies the exported Picker does nothing on a
// non-interactive exit (piped stdin, CI), matching PromptRun's TTY guard.
func TestPickerNoopWhenNotTTY(t *testing.T) {
	withTTY(t, false)
	var out bytes.Buffer
	Picker(strings.NewReader("all\n\n"), &out, &config.Config{})
	if out.Len() != 0 {
		t.Fatalf("output = %q, want empty", out.String())
	}
}

// TestStopPickerFlushesTTYOnEveryReadExit verifies the paste-residue drain runs
// on the picker's skip paths too, not only after a confirmed stop. A pasted
// block whose first line is "q"/"esc", or whose first line is blank (Enter with
// nothing ticked), leaves every later line in the kernel's TTY input queue,
// where the parent shell runs them as commands once wt exits — the hazard the
// drain exists to prevent. A run where nothing is offered must stay flush-free:
// the picker returns before reading a line, so it has no residue of its own.
func TestStopPickerFlushesTTYOnEveryReadExit(t *testing.T) {
	prevFlush := flushTTY
	t.Cleanup(func() { flushTTY = prevFlush })

	for _, input := range []string{"\n", "q\n", "esc\n", "1\n\n", ""} {
		flushes := 0
		flushTTY = func() { flushes++ }
		h := &stopHarness{snap: localmodels.Snapshot{Entries: []localmodels.Entry{
			runningEntry("ollama", "ollama/a", "a")}}}
		runStopPicker(strings.NewReader(input), &bytes.Buffer{}, &config.Config{}, h.deps())
		if flushes != 1 {
			t.Errorf("input %q: flushes = %d, want 1", input, flushes)
		}
	}

	flushes := 0
	flushTTY = func() { flushes++ }
	h := &stopHarness{snap: localmodels.Snapshot{}}
	runStopPicker(strings.NewReader("\n"), &bytes.Buffer{}, &config.Config{}, h.deps())
	if flushes != 0 {
		t.Errorf("nothing offered: flushes = %d, want 0", flushes)
	}
}

// TestStopPickerStopIsCancellable verifies the picker's stops run on a
// cancellable context, and that cancelling it abandons the remaining models
// instead of starting more stops. These stops happen after the agent has exited,
// so this is the only Ctrl+C handling left on the path: on a non-cancellable
// context a wedged provider CLI would block the session with no way out.
func TestStopPickerStopIsCancellable(t *testing.T) {
	prev := stopSignalCtx
	t.Cleanup(func() { stopSignalCtx = prev })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopSignalCtx = func() (context.Context, context.CancelFunc) { return ctx, func() {} }

	h := &stopHarness{
		snap: localmodels.Snapshot{Entries: []localmodels.Entry{
			runningEntry("ollama", "ollama/a", "a"),
			runningEntry("ollama", "ollama/b", "b"),
		}},
		cancelOn: "a",
		cancel:   cancel,
	}
	var out bytes.Buffer
	runStopPicker(strings.NewReader("all\n\n"), &out, &config.Config{}, h.deps())

	if h.ctxSeen == nil || h.ctxSeen.Done() == nil {
		t.Error("stop ran on a context with no cancellation path (context.Background)")
	}
	if len(h.stops) != 1 || h.stops[0] != "ollama|a" {
		t.Fatalf("stops = %v, want only the first: the second must not start after a cancel", h.stops)
	}
	if !strings.Contains(out.String(), "cancelled") {
		t.Errorf("output = %q, want a cancelled line", out.String())
	}
}
