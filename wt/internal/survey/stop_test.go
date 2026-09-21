package survey

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
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
	// cancelFails makes the cancelOn stop return the context's own error after
	// cancelling instead of nil — the shape a provider CLI killed by the signal
	// produces, and the only way a cancellation reaches the picker as an error.
	// Without it a cancelled stop takes the success path and never tests the
	// picker's cancelled-vs-failed distinction.
	cancelFails bool
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
				if h.cancelFails {
					return ctx.Err()
				}
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

// readTrackingReader counts the reads a caller has issued, so a test can assert
// the drain runs *after* the picker has read — the property that makes it a
// drain of residue rather than a pre-emptive flush that would swallow
// typed-ahead input on a real terminal.
type readTrackingReader struct {
	r     io.Reader
	reads int
}

func (t *readTrackingReader) Read(p []byte) (int, error) {
	t.reads++
	return t.r.Read(p)
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

// TestStopPickerDrainsAfterReadingOnEveryExit verifies the paste-residue drain
// runs after the picker has read, on every path that consumed input. A pasted
// block whose first line is "q"/"esc", or whose first line is blank (Enter with
// nothing ticked), leaves every later line in the kernel's TTY input queue,
// where the parent shell runs them as commands once wt exits — the hazard the
// drain exists to prevent. Asserting the ordering (not just the count) is what
// stops a future refactor from draining before the prompt and eating input the
// user typed ahead.
func TestStopPickerDrainsAfterReadingOnEveryExit(t *testing.T) {
	prevFlush := flushTTY
	t.Cleanup(func() { flushTTY = prevFlush })

	for _, input := range []string{"\n", "q\n", "esc\n", "1\n\n"} {
		flushes := 0
		src := &readTrackingReader{r: strings.NewReader(input)}
		flushTTY = func() {
			flushes++
			if src.reads == 0 {
				t.Errorf("input %q: drained before the picker read anything", input)
			}
		}
		h := &stopHarness{snap: localmodels.Snapshot{Entries: []localmodels.Entry{
			runningEntry("ollama", "ollama/a", "a")}}}
		runStopPicker(src, &bytes.Buffer{}, &config.Config{}, h.deps())
		if flushes != 1 {
			t.Errorf("input %q: flushes = %d, want 1", input, flushes)
		}
	}
}

// TestStopPickerExhaustedReaderStillDrains verifies the drain is unconditional
// once the picker has offered a choice, even when the reader ends without
// returning a line. On a real terminal the scanner can consume part of the
// input queue and then hit the end, so residue may still be queued; gating the
// drain on "a line was returned" would leave exactly that residue to run as
// shell commands in the parent terminal.
func TestStopPickerExhaustedReaderStillDrains(t *testing.T) {
	prevFlush := flushTTY
	t.Cleanup(func() { flushTTY = prevFlush })

	flushes := 0
	flushTTY = func() { flushes++ }
	h := &stopHarness{snap: localmodels.Snapshot{Entries: []localmodels.Entry{
		runningEntry("ollama", "ollama/a", "a")}}}
	runStopPicker(strings.NewReader(""), &bytes.Buffer{}, &config.Config{}, h.deps())
	if flushes != 1 {
		t.Errorf("exhausted reader: flushes = %d, want 1", flushes)
	}
}

// TestStopPickerFlushesNothingWhenNothingOffered verifies a run that offers no
// model stays flush-free: the picker returns before reading a line, so it has
// no residue of its own and must not clear a queue it never consumed.
func TestStopPickerFlushesNothingWhenNothingOffered(t *testing.T) {
	prevFlush := flushTTY
	t.Cleanup(func() { flushTTY = prevFlush })

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
	// The picker must call this itself: signal.NotifyContext keeps its handler
	// installed until the returned CancelFunc runs, so dropping the deferred
	// cancel would leave wt's SIGINT handler live past the picker.
	released := false
	stopSignalCtx = func() (context.Context, context.CancelFunc) {
		return ctx, func() { released = true }
	}

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
	if !released {
		t.Error("the picker never called the context's CancelFunc, leaving its signal handler installed")
	}
	// The guard must skip the print, not just the stop: an unattempted model
	// must not be announced as "Stopping …". Match the print's own prefix —
	// the choice list above legitimately names every offered model.
	if strings.Contains(out.String(), "Stopping ollama/b") {
		t.Errorf("output = %q, want no Stopping line for the unattempted model b", out.String())
	}
}

// TestStopPickerCancelledLastStopReportsCancelled verifies that Ctrl+C landing
// while the *last* (or only) selected model's stop is in flight prints
// "cancelled" rather than "failed: context canceled". The top-of-loop guard
// only runs when another model remains, so on the common single-model cancel
// the cancellation reaches the loop as the stop's own error: if the picker
// reports that as a failure, wt tells the user their provider CLI is broken
// when they simply interrupted it — the one place the picker's Ctrl+C story is
// visible, since these stops run after the agent has exited.
func TestStopPickerCancelledLastStopReportsCancelled(t *testing.T) {
	prev := stopSignalCtx
	t.Cleanup(func() { stopSignalCtx = prev })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopSignalCtx = func() (context.Context, context.CancelFunc) { return ctx, func() {} }

	h := &stopHarness{
		snap: localmodels.Snapshot{Entries: []localmodels.Entry{
			runningEntry("ollama", "ollama/a", "a"),
		}},
		cancelOn:    "a",
		cancel:      cancel,
		cancelFails: true,
	}
	var out bytes.Buffer
	runStopPicker(strings.NewReader("all\n\n"), &out, &config.Config{}, h.deps())

	if h.ctxSeen == nil || h.ctxSeen.Done() == nil {
		t.Error("stop ran on a context with no cancellation path (context.Background)")
	}
	if len(h.stops) != 1 || h.stops[0] != "ollama|a" {
		t.Fatalf("stops = %v, want [ollama|a]", h.stops)
	}
	if !strings.Contains(out.String(), "cancelled") {
		t.Errorf("output = %q, want a cancelled line", out.String())
	}
	if strings.Contains(out.String(), "failed:") {
		t.Errorf("output = %q, want no failed line: the stop was cancelled, not broken", out.String())
	}
}

// TestReleaseSessionWarnsWhenReleaseFails verifies a failed refcount release
// warns on stderr and never panics or aborts the exit path. The release is
// best-effort by design — the next launch's Sweep prunes a dead pid's entry —
// but the warning is the user's only signal that the "in use" column may be
// stale, and this branch previously had no coverage at all.
//
// It drives Release's *read*-error path, not a write failure: a directory where
// the state file belongs makes os.ReadFile fail EISDIR, which is not
// os.IsNotExist, so Release propagates it and ReleaseSession warns. A genuine
// write failure cannot be driven this way (WriteFileAtomic renames over its
// target), and both branches share the one warning string, so the name stays
// branch-neutral.
//
// The assertion pins the warning's whole shape — prefix, separator and the
// error value — because this is user-facing stderr text on the post-exit path:
// a reword that drops the "note: " cue or the underlying cause must fail here,
// not pass silently.
func TestReleaseSessionWarnsWhenReleaseFails(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	// A *directory* where the state file belongs makes the read fail with
	// EISDIR, which is not os.IsNotExist — the one error class Release
	// propagates rather than swallowing.
	if err := os.MkdirAll(filepath.Join(dir, "agent-wt", "refcount.jsonl"), 0o755); err != nil {
		t.Fatal(err)
	}

	prevStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = prevStderr })

	ReleaseSession()

	os.Stderr = prevStderr
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(r)
	r.Close()
	// Pin the full prefix — "note: ", the message, and the ": " separator
	// including its trailing space — rather than a bare substring, so a
	// reworded warning cannot pass silently.
	const wantPrefix = "note: refcount state not released: "
	if !strings.HasPrefix(string(got), wantPrefix) {
		t.Fatalf("stderr = %q, want the refcount release warning prefixed %q", got, wantPrefix)
	}
	// The remainder must carry the underlying cause, so the %v argument itself
	// is guarded and not just the literal prose: EISDIR's text proves the read
	// error is actually threaded through.
	detail := strings.TrimSuffix(strings.TrimPrefix(string(got), wantPrefix), "\n")
	if detail == "" {
		t.Errorf("stderr = %q, want a non-empty error detail after %q", got, wantPrefix)
	}
	if !strings.Contains(detail, "is a directory") {
		t.Errorf("stderr = %q, want the read error (EISDIR) threaded through after %q", got, wantPrefix)
	}
}

// blockingReader cancels the picker's context on its first Read and then blocks
// forever — the shape of Ctrl+C landing while the user sits at the "stop>"
// prompt with nothing typed. Cancelling from inside Read guarantees the menu has
// already been printed, so the test never races the picker's own writes.
type blockingReader struct {
	cancel context.CancelFunc
	block  chan struct{}
}

func (b *blockingReader) Read([]byte) (int, error) {
	b.cancel()
	<-b.block
	return 0, io.EOF
}

// TestStopPickerCtrlCAtMenuSkipsAndDrains verifies Ctrl+C while the menu is
// waiting for input abandons the prompt, stops nothing, and still runs the
// deferred TTY drain. Without it the default SIGINT disposition killed wt before
// the summary, the stats and the drain, leaving paste residue queued to run as
// shell commands.
func TestStopPickerCtrlCAtMenuSkipsAndDrains(t *testing.T) {
	prev, prevFlush := stopSignalCtx, flushTTY
	t.Cleanup(func() { stopSignalCtx, flushTTY = prev, prevFlush })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopSignalCtx = func() (context.Context, context.CancelFunc) { return ctx, cancel }
	flushes := 0
	flushTTY = func() { flushes++ }

	h := &stopHarness{
		snap: localmodels.Snapshot{Entries: []localmodels.Entry{
			runningEntry("ollama", "ollama/a", "a"),
		}},
	}
	rd := &blockingReader{cancel: cancel, block: make(chan struct{})}
	defer close(rd.block)
	var out bytes.Buffer
	runStopPicker(rd, &out, &config.Config{}, h.deps())

	if len(h.stops) != 0 {
		t.Errorf("stops = %v, want none after a cancelled menu", h.stops)
	}
	if !strings.Contains(out.String(), "cancelled") {
		t.Errorf("output = %q, want a cancelled line", out.String())
	}
	if flushes != 1 {
		t.Errorf("flushes = %d, want 1: the drain must still run", flushes)
	}
}

// TestStopCandidatesReportsSessionCounts verifies StopCandidates returns every
// running stoppable model INCLUDING ones a live session uses, with the count,
// and that a single-model provider reports its whole family's count. `wt stop`
// needs the in-use models to warn before stopping them; the exit-of-session
// picker must still hide them.
func TestStopCandidatesReportsSessionCounts(t *testing.T) {
	h := &stopHarness{
		snap: localmodels.Snapshot{Entries: []localmodels.Entry{
			runningEntry("ollama", "ollama/a", "a"),
			runningEntry("ollama", "ollama/busy", "busy"),
			runningEntry("omlx", "omlx/x", "x"),
			runningEntry("omlx-6bit", "omlx-6bit/y", "y"),
		}},
		counts: map[string]int{"ollama/busy": 2, "omlx/x": 1},
	}
	got := map[string]int{}
	for _, c := range stopCandidates(&config.Config{}, h.deps()) {
		got[c.Entry.ModelID] = c.Sessions
	}
	want := map[string]int{"ollama/a": 0, "ollama/busy": 2, "omlx/x": 1, "omlx-6bit/y": 1}
	for id, n := range want {
		if got[id] != n {
			t.Errorf("Sessions[%s] = %d, want %d (all: %v)", id, got[id], n, got)
		}
	}
	// The exit-flow view is unchanged: only zero-session models.
	for _, e := range stoppable(&config.Config{}, h.deps()) {
		if e.ModelID != "ollama/a" {
			t.Errorf("stoppable offered %s, want only ollama/a", e.ModelID)
		}
	}
}

// TestStopPickerIncludeInUseMarksSessions verifies that with IncludeInUse the
// picker lists an in-use model with its session count and can stop it. This is
// what lets explicit `wt stop` act on a model while making the risk visible.
func TestStopPickerIncludeInUseMarksSessions(t *testing.T) {
	h := &stopHarness{
		snap:   localmodels.Snapshot{Entries: []localmodels.Entry{runningEntry("ollama", "ollama/busy", "busy")}},
		counts: map[string]int{"ollama/busy": 2},
	}
	var out bytes.Buffer
	runStopPickerWith(strings.NewReader("1\n\n"), &out, &config.Config{}, h.deps(), Options{IncludeInUse: true})
	if !strings.Contains(out.String(), "ollama/busy (2 sessions)") {
		t.Errorf("output = %q, want the session count next to the model", out.String())
	}
	if len(h.stops) != 1 || h.stops[0] != "ollama|busy" {
		t.Errorf("stops = %v, want [ollama|busy]", h.stops)
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
