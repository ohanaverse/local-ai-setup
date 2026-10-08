package survey

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
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
	// notOwed makes successful stops report no restart owed (their model had
	// no route), so a test can prove the loop then skips the settle.
	notOwed bool
	// settles counts settle calls: the proxy restarts the stop loop asked for.
	settles int
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
		stop: func(ctx context.Context, _ *config.Config, en localmodels.Entry) (bool, error) {
			if h.ctxSeen == nil {
				h.ctxSeen = ctx
			}
			h.stops = append(h.stops, en.ProviderID+"|"+en.ModelName)
			if h.cancel != nil && en.ModelName == h.cancelOn {
				h.cancel()
				if h.cancelFails {
					return false, ctx.Err()
				}
			}
			if en.ModelName == h.failOn {
				return false, errors.New("boom")
			}
			return !h.notOwed, nil
		},
		settle: func(context.Context, *config.Config) { h.settles++ },
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
		runStopPicker(strings.NewReader("all\n"), &out, &config.Config{}, h.deps())
		if out.Len() != 0 || len(h.stops) != 0 {
			t.Errorf("%s: output = %q stops = %v, want none", name, out.String(), h.stops)
		}
	}
}

// TestStopPickerStopsOnlySelected verifies a typed number stops exactly that
// model, and a model another session uses is never offered or stopped (issue
// #115's "ref count zero" rule).
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
	runStopPicker(strings.NewReader("2\n"), &out, &config.Config{}, h.deps())
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

// TestStopPickerSkipPaths verifies a bare Enter, "q", "esc", "skip" and an
// exhausted reader all stop nothing: the default is to stop nothing, because
// stopping costs a model reload and must be an explicit choice.
func TestStopPickerSkipPaths(t *testing.T) {
	for _, input := range []string{"\n", "   \n", "q\n", "esc\n", "skip\n", ""} {
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
	runStopPicker(strings.NewReader("all\n"), &out, &config.Config{}, h.deps())
	if len(h.stops) != 2 {
		t.Fatalf("stops = %v, want both attempted", h.stops)
	}
	if !strings.Contains(out.String(), "Stopping ollama/a... failed: boom") ||
		!strings.Contains(out.String(), "Stopping ollama/b... done") {
		t.Errorf("output = %q, want a failed line then a done line", out.String())
	}
}

// TestStopPickerEnterAppliesTheLine pins the one-line prompt (#139): Enter
// applies what was typed, with no second Enter to confirm. Space-separated
// numbers stop those models, in list order and each once however they were
// typed; "a" or "all" stops every offered model. The prompt used to be a
// checkbox list — a number only ticked a line and a further Enter confirmed —
// which was two steps for the common "stop this one" and three inputs for two
// models typed one at a time.
func TestStopPickerEnterAppliesTheLine(t *testing.T) {
	for input, want := range map[string][]string{
		"1\n":      {"ollama|a"},
		"1 3\n":    {"ollama|a", "ollama|c"},
		"3 1 1\n":  {"ollama|a", "ollama|c"},
		" 2  3 \n": {"ollama|b", "ollama|c"},
		"a\n":      {"ollama|a", "ollama|b", "ollama|c"},
		"all\n":    {"ollama|a", "ollama|b", "ollama|c"},
		"ALL\n":    {"ollama|a", "ollama|b", "ollama|c"},
	} {
		h := &stopHarness{snap: localmodels.Snapshot{Entries: []localmodels.Entry{
			runningEntry("ollama", "ollama/a", "a"),
			runningEntry("ollama", "ollama/b", "b"),
			runningEntry("ollama", "ollama/c", "c"),
		}}}
		var out bytes.Buffer
		// Only one line is supplied: a prompt that still waited for a
		// confirming Enter would hit the end of input and stop nothing.
		runStopPicker(strings.NewReader(input), &out, &config.Config{}, h.deps())
		if !slices.Equal(h.stops, want) {
			t.Errorf("input %q: stops = %v, want %v", input, h.stops, want)
		}
		if strings.Count(out.String(), "stop> ") != 1 {
			t.Errorf("input %q: prompted %d times, want once:\n%s", input, strings.Count(out.String(), "stop> "), out.String())
		}
	}
}

// TestStopPickerInvalidInputReprompts verifies a line with anything on it that
// is not a listed number (or "a"/"all" alone) stops nothing, says which part
// it did not understand and asks again — the whole line, so "1 x" does not
// stop model 1 on a typo. The next valid line is applied as usual.
func TestStopPickerInvalidInputReprompts(t *testing.T) {
	for input, bad := range map[string]string{
		"zzz\n":   `"zzz"`,
		"9\n":     `"9"`,
		"0\n":     `"0"`,
		"-1\n":    `"-1"`,
		"1 x\n":   `"x"`,
		"1,2\n":   `"1,2"`,
		"all 2\n": `"all"`,
		"none\n":  `"none"`,
		"1 7 8\n": `"7" "8"`,
	} {
		h := &stopHarness{snap: localmodels.Snapshot{Entries: []localmodels.Entry{
			runningEntry("ollama", "ollama/a", "a"),
			runningEntry("ollama", "ollama/b", "b"),
		}}}
		var out bytes.Buffer
		runStopPicker(strings.NewReader(input), &out, &config.Config{}, h.deps())
		if len(h.stops) != 0 {
			t.Errorf("input %q: stops = %v, want none", input, h.stops)
		}
		want := "not a number from the list: " + bad + " — type numbers (e.g. 1 2), a for all, or Enter to stop nothing\n"
		if !strings.Contains(out.String(), want) {
			t.Errorf("input %q: output lacks %q:\n%s", input, want, out.String())
		}
		if strings.Count(out.String(), "stop> ") != 2 {
			t.Errorf("input %q: prompted %d times, want twice (the retry)", input, strings.Count(out.String(), "stop> "))
		}
	}
	h := &stopHarness{snap: localmodels.Snapshot{Entries: []localmodels.Entry{
		runningEntry("ollama", "ollama/a", "a"), runningEntry("ollama", "ollama/b", "b")}}}
	var out bytes.Buffer
	runStopPicker(strings.NewReader("9\n2\n"), &out, &config.Config{}, h.deps())
	if !slices.Equal(h.stops, []string{"ollama|b"}) {
		t.Errorf("a valid line after a rejected one: stops = %v, want [ollama|b]", h.stops)
	}
}

// TestStopPickerListsModelsWithoutCheckboxes pins what the prompt shows: the
// numbered models and one line saying what can be typed. There is no ticked
// state to draw any more, so no "[ ]" boxes, and the list is printed once.
func TestStopPickerListsModelsWithoutCheckboxes(t *testing.T) {
	h := &stopHarness{snap: localmodels.Snapshot{Entries: []localmodels.Entry{
		runningEntry("ollama", "ollama/a", "a"), runningEntry("ollama", "ollama/b", "b")}}}
	var out bytes.Buffer
	runStopPicker(strings.NewReader("\n"), &out, &config.Config{}, h.deps())
	want := exitFlowHeader + "\n  1  ollama/a\n  2  ollama/b\n  numbers (e.g. 1 2) · a = all · q = skip · Enter = none\nstop> "
	if out.String() != want {
		t.Errorf("prompt = %q\nwant     %q", out.String(), want)
	}
}

// TestPickerNoopWhenNotTTY verifies the exported Picker does nothing on a
// non-interactive exit (piped stdin, CI), matching PromptRun's TTY guard.
func TestPickerNoopWhenNotTTY(t *testing.T) {
	withTTY(t, false)
	var out bytes.Buffer
	Picker(strings.NewReader("all\n"), &out, &config.Config{})
	if out.Len() != 0 {
		t.Fatalf("output = %q, want empty", out.String())
	}
}

// TestStopPickerDrainsAfterReadingOnEveryExit verifies the paste-residue drain
// runs after the picker has read, on every path that consumed input. A pasted
// block whose first line is "q"/"esc", or whose first line is blank (a bare
// Enter), leaves every later line in the kernel's TTY input queue,
// where the parent shell runs them as commands once wt exits — the hazard the
// drain exists to prevent. Asserting the ordering (not just the count) is what
// stops a future refactor from draining before the prompt and eating input the
// user typed ahead.
func TestStopPickerDrainsAfterReadingOnEveryExit(t *testing.T) {
	prevFlush := flushTTY
	t.Cleanup(func() { flushTTY = prevFlush })

	for _, input := range []string{"\n", "q\n", "esc\n", "1\n"} {
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
	runStopPicker(strings.NewReader("all\n"), &out, &config.Config{}, h.deps())

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
	runStopPicker(strings.NewReader("all\n"), &out, &config.Config{}, h.deps())

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

// blockingReader signals its first Read and then blocks until released — the
// shape of Ctrl+C landing while the user sits at the "stop>" prompt with nothing
// typed. The first Read comes after every prompt line is written (chooseLabeled
// prints the header, the list and the hint, then the prompt, and only then
// scans), so waiting on read is how the test knows the reader goroutine is done
// writing and can read the output buffer without racing it.
type blockingReader struct {
	read  chan struct{}
	block chan struct{}
	once  sync.Once
}

func (b *blockingReader) Read([]byte) (int, error) {
	b.once.Do(func() { close(b.read) })
	<-b.block
	return 0, io.EOF
}

// TestStopPickerCtrlCAtMenuSkipsAndDrains verifies Ctrl+C while the menu is
// waiting for input abandons the prompt, stops nothing, and still runs the
// deferred TTY drain. Without it the default SIGINT disposition killed wt before
// the summary, the stats and the drain, leaving paste residue queued to run as
// shell commands.
//
// It also pins the picker's shared writer: the context is cancelled *before* the
// picker starts, so the picker prints "cancelled" while the reader goroutine may
// still be printing the menu — the one window where the two really are writing
// at once. Unserialized, -race reports that; cancelling from inside Read, as
// this test used to, ordered the reader's writes before the cancellation and
// so could never have caught it.
func TestStopPickerCtrlCAtMenuSkipsAndDrains(t *testing.T) {
	prev, prevFlush := stopSignalCtx, flushTTY
	t.Cleanup(func() { stopSignalCtx, flushTTY = prev, prevFlush })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	stopSignalCtx = func() (context.Context, context.CancelFunc) { return ctx, cancel }
	flushes := 0
	flushTTY = func() { flushes++ }
	// Cancelled up front, and the reader never answers, so the select's ctx case
	// is the only ready one: with a readable answer both would be ready and the
	// scheduler would choose between them.
	cancel()

	h := &stopHarness{
		snap: localmodels.Snapshot{Entries: []localmodels.Entry{
			runningEntry("ollama", "ollama/a", "a"),
		}},
	}
	rd := &blockingReader{read: make(chan struct{}), block: make(chan struct{})}
	t.Cleanup(func() { close(rd.block) })
	var out bytes.Buffer
	runStopPicker(rd, &out, &config.Config{}, h.deps())
	<-rd.read

	if len(h.stops) != 0 {
		t.Errorf("stops = %v, want none after a cancelled menu", h.stops)
	}
	if !strings.Contains(out.String(), "cancelled") {
		t.Errorf("output = %q, want a cancelled line", out.String())
	}
	if !strings.Contains(out.String(), exitFlowHeader) {
		t.Errorf("output = %q, want the menu printed before the cancellation", out.String())
	}
	if flushes != 1 {
		t.Errorf("flushes = %d, want 1: the drain must still run", flushes)
	}
}

// TestStopCandidatesReportsSessionCounts verifies StopCandidates returns every
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
	for _, c := range stopCandidates(&config.Config{}, h.deps()) {
		got[c.Entry.ModelID] = c.Sessions
	}
	want := map[string]int{"ollama/a": 0, "ollama/busy": 2, "omlx/x": 1, "omlx-6bit/y": 0, "mtplx/p": 1, "mtplx/q": 1}
	for id, n := range want {
		if got[id] != n {
			t.Errorf("Sessions[%s] = %d, want %d (all: %v)", id, got[id], n, got)
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
	runStopPickerWith(strings.NewReader("1\n"), &out, &config.Config{}, h.deps(), Options{IncludeInUse: true})
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
	// The error is ErrStopCancelled itself: `wt stop --all` tells a Ctrl+C
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
// mid-load; `wt stop omlx` is what calls a load off, and the message says so.)
func TestStopCandidatesIncludeAModelMidLoad(t *testing.T) {
	loading := runningEntry("omlx", "omlx/x", "x")
	loading.Loading = true
	h := &stopHarness{snap: localmodels.Snapshot{Entries: []localmodels.Entry{loading}}}
	cands := stopCandidates(&config.Config{}, h.deps())
	if len(cands) != 1 || cands[0].Entry.ModelID != "omlx/x" {
		t.Errorf("candidates = %+v, want the loading omlx/x", cands)
	}
}
