package main

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

func startTestRow() catalog.Row {
	return catalog.Row{
		Model:    config.Model{ID: "omlx/qwen3.8", ProviderID: "omlx", ModelName: "qwen3.8"},
		Location: config.LocationLocal,
		Status:   catalog.StatusOK,
	}
}

// stubLifecycleStart replaces the engine with scripted outcomes: the nth call
// returns outcomes[n]. It records each call's AllowReplace so the two-phase
// replace dance is observable.
func stubLifecycleStart(t *testing.T, outcomes []error) *[]bool {
	t.Helper()
	replaces := &[]bool{}
	var calls int32
	old := lifecycleStart
	lifecycleStart = func(_ context.Context, _ *config.Config, _ lifecycle.Target, opts lifecycle.Options) error {
		n := int(atomic.AddInt32(&calls, 1)) - 1
		*replaces = append(*replaces, opts.AllowReplace)
		if n >= len(outcomes) {
			return nil
		}
		return outcomes[n]
	}
	t.Cleanup(func() { lifecycleStart = old })
	return replaces
}

// stubSignals replaces the signal-context seam with a plain cancellable
// context, so a test can cancel a start without installing a real handler.
func stubSignals(t *testing.T) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	old := startSignalCtx
	startSignalCtx = func() (context.Context, context.CancelFunc) { return ctx, cancel }
	t.Cleanup(func() { startSignalCtx = old; cancel() })
	return cancel
}

// TestStartForLaunchSucceedsWithoutReplace verifies the happy path calls the
// engine once with AllowReplace false and returns nil: a cold start must never
// carry permission to stop a model.
func TestStartForLaunchSucceedsWithoutReplace(t *testing.T) {
	stubSignals(t)
	replaces := stubLifecycleStart(t, []error{nil})

	if err := startForLaunch(&config.Config{}, startTestRow(), false); err != nil {
		t.Fatalf("startForLaunch() error = %v, want nil", err)
	}
	if len(*replaces) != 1 || (*replaces)[0] {
		t.Errorf("AllowReplace calls = %v, want a single false", *replaces)
	}
}

// TestStartForLaunchRetriesOccupiedOnlyWithReplace verifies an occupied
// provider makes the driver retry once with AllowReplace true — after it has
// the occupant's id for the "replacing X" notice — and that the first attempt
// never carried the permission. Retrying without the notice would stop a
// running model the user was never told about.
func TestStartForLaunchRetriesOccupiedOnlyWithReplace(t *testing.T) {
	stubSignals(t)
	occ := &lifecycle.OccupiedError{Occupant: localmodelsEntry("omlx/other", "other")}
	replaces := stubLifecycleStart(t, []error{occ, nil})

	if err := startForLaunch(&config.Config{}, startTestRow(), true); err != nil {
		t.Fatalf("startForLaunch() error = %v, want nil", err)
	}
	if len(*replaces) != 2 || (*replaces)[0] || !(*replaces)[1] {
		t.Errorf("AllowReplace calls = %v, want [false true]", *replaces)
	}
}

// TestStartForLaunchNonInteractiveOccupiedRefuses verifies that without
// --replace and without a TTY the driver refuses and names the occupant — it
// must never stop a running model on its own initiative in a pipeline, where
// there is nobody to ask.
func TestStartForLaunchNonInteractiveOccupiedRefuses(t *testing.T) {
	stubSignals(t)
	oldTTY := stdinTTY
	stdinTTY = func() bool { return false }
	t.Cleanup(func() { stdinTTY = oldTTY })
	asked := false
	oldConfirm := confirmReplace
	confirmReplace = func(string) (bool, error) { asked = true; return true, nil }
	t.Cleanup(func() { confirmReplace = oldConfirm })
	occ := &lifecycle.OccupiedError{Occupant: localmodelsEntry("omlx/other", "other")}
	replaces := stubLifecycleStart(t, []error{occ})

	err := startForLaunch(&config.Config{}, startTestRow(), false)
	if err == nil || !strings.Contains(err.Error(), "omlx/other") {
		t.Fatalf("err = %v, want a refusal naming omlx/other", err)
	}
	if !strings.Contains(err.Error(), "--replace") {
		t.Errorf("err = %q, want it to name the --replace flag", err)
	}
	if asked {
		t.Error("no prompt may be attempted without a TTY")
	}
	if len(*replaces) != 1 {
		t.Errorf("engine calls = %v, want 1 (no retry)", *replaces)
	}
}

// TestStartForLaunchTTYDeclineKeepsRunningModel verifies answering "no" at the
// prompt aborts the start and leaves the occupant alone: the prompt defaults
// to No precisely so an accidental Enter cannot stop a model.
func TestStartForLaunchTTYDeclineKeepsRunningModel(t *testing.T) {
	stubSignals(t)
	oldTTY := stdinTTY
	stdinTTY = func() bool { return true }
	t.Cleanup(func() { stdinTTY = oldTTY })
	oldConfirm := confirmReplace
	confirmReplace = func(q string) (bool, error) {
		if !strings.Contains(q, "omlx/other") {
			t.Errorf("question = %q, want it to name the occupant", q)
		}
		return false, nil
	}
	t.Cleanup(func() { confirmReplace = oldConfirm })
	occ := &lifecycle.OccupiedError{Occupant: localmodelsEntry("omlx/other", "other")}
	replaces := stubLifecycleStart(t, []error{occ})

	err := startForLaunch(&config.Config{}, startTestRow(), false)
	if err == nil || !strings.Contains(err.Error(), "omlx/other") {
		t.Fatalf("err = %v, want a cancellation naming omlx/other", err)
	}
	if len(*replaces) != 1 {
		t.Errorf("engine calls = %v, want 1 (declined, no retry)", *replaces)
	}
}

// TestStartForLaunchTTYConfirmReplaces verifies answering "yes" retries with
// AllowReplace true, so the interactive path can replace a running model
// while the non-interactive one cannot.
func TestStartForLaunchTTYConfirmReplaces(t *testing.T) {
	stubSignals(t)
	oldTTY := stdinTTY
	stdinTTY = func() bool { return true }
	t.Cleanup(func() { stdinTTY = oldTTY })
	oldConfirm := confirmReplace
	confirmReplace = func(string) (bool, error) { return true, nil }
	t.Cleanup(func() { confirmReplace = oldConfirm })
	occ := &lifecycle.OccupiedError{Occupant: localmodelsEntry("omlx/other", "other")}
	replaces := stubLifecycleStart(t, []error{occ, nil})

	if err := startForLaunch(&config.Config{}, startTestRow(), false); err != nil {
		t.Fatalf("startForLaunch() error = %v, want nil", err)
	}
	if len(*replaces) != 2 || (*replaces)[0] || !(*replaces)[1] {
		t.Errorf("AllowReplace calls = %v, want [false true]", *replaces)
	}
}

// TestStartForLaunchUnknownOccupancyNeedsReplace verifies the indeterminate
// case behaves like the occupied one: a provider whose server answered
// unusably must not be replaced without consent, because wt cannot know
// whether a model is still serving there.
func TestStartForLaunchUnknownOccupancyNeedsReplace(t *testing.T) {
	stubSignals(t)
	unk := &lifecycle.OccupancyUnknownError{ProviderID: "omlx", Origin: "http://localhost:8000"}
	replaces := stubLifecycleStart(t, []error{unk, nil})

	if err := startForLaunch(&config.Config{}, startTestRow(), true); err != nil {
		t.Fatalf("startForLaunch() error = %v, want nil", err)
	}
	if len(*replaces) != 2 || (*replaces)[0] || !(*replaces)[1] {
		t.Errorf("AllowReplace calls = %v, want [false true]", *replaces)
	}
}

// TestStartForLaunchOtherFailureDoesNotRetry verifies a plain engine failure
// (daemon down, binary missing, port busy) is returned as-is with a single
// attempt — retrying with AllowReplace would both be pointless and grant
// permission to stop a model for a start that cannot succeed anyway.
func TestStartForLaunchOtherFailureDoesNotRetry(t *testing.T) {
	stubSignals(t)
	down := &lifecycle.DaemonDownError{Provider: "omlx", Origin: "http://localhost:8000"}
	replaces := stubLifecycleStart(t, []error{down})

	err := startForLaunch(&config.Config{}, startTestRow(), true)
	if !errors.Is(err, error(down)) {
		t.Fatalf("err = %v, want the engine's DaemonDownError", err)
	}
	if len(*replaces) != 1 {
		t.Errorf("engine calls = %v, want 1", *replaces)
	}
}

// TestStartForLaunchCancelIsReported verifies a cancelled start (Ctrl+C)
// returns an error naming the model rather than nil, so the caller aborts the
// launch instead of running the agent against a server that never came up.
func TestStartForLaunchCancelIsReported(t *testing.T) {
	cancel := stubSignals(t)
	old := lifecycleStart
	lifecycleStart = func(ctx context.Context, _ *config.Config, _ lifecycle.Target, _ lifecycle.Options) error {
		cancel()
		<-ctx.Done()
		return ctx.Err()
	}
	t.Cleanup(func() { lifecycleStart = old })

	err := startForLaunch(&config.Config{}, startTestRow(), false)
	if err == nil || !strings.Contains(err.Error(), "omlx/qwen3.8") {
		t.Fatalf("err = %v, want a cancellation naming the model", err)
	}
}

// TestStartProgressReportsEachStage verifies the progress callback writes one
// line per stage, naming the model, the stage phrase and an elapsed time. A
// silent start would leave the user staring at nothing during a long warmup.
func TestStartProgressReportsEachStage(t *testing.T) {
	oldOut := osStderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	osStderr = w
	t.Cleanup(func() { osStderr = oldOut })

	done := make(chan struct{})
	report := startProgress("omlx/qwen3.8", done, time.Now().Add(-12*time.Second))
	report(lifecycle.StageStarting)
	report(lifecycle.StageWarming)
	close(done)
	_ = w.Close()

	buf, _ := io.ReadAll(r)
	_ = r.Close()
	got := string(buf)
	for _, want := range []string{"starting omlx/qwen3.8", "starting the server", "warming the model", "(12s)"} {
		if !strings.Contains(got, want) {
			t.Errorf("progress output = %q, want it to contain %q", got, want)
		}
	}
}

// TestStartProgressRepeatsWhileWarming verifies the heartbeat — the
// ticker-driven repeat of the current stage, not the synchronous emit the
// stage change itself causes: with the engine stuck on one stage the driver
// still prints that stage every interval, so a multi-minute warmup does not
// look like a hang.
func TestStartProgressRepeatsWhileWarming(t *testing.T) {
	oldOut := osStderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	osStderr = w
	t.Cleanup(func() { osStderr = oldOut })
	oldInterval := startProgressInterval
	startProgressInterval = 5 * time.Millisecond
	t.Cleanup(func() { startProgressInterval = oldInterval })

	done := make(chan struct{})
	report := startProgress("omlx/q", done, time.Now())
	report(lifecycle.StageWarming)
	_ = readSome(t, r) // drain the synchronous emit; only ticker output remains
	output := ""
	found := false
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !found {
		output += readSome(t, r)
		found = strings.Contains(output, "warming the model (")
		time.Sleep(5 * time.Millisecond)
	}
	close(done)
	_ = w.Close()
	if !found {
		t.Fatal("heartbeat did not repeat the warming stage within the deadline")
	}
}

// TestAskYesNoDefaultsToNo verifies the confirm rule the replace prompt
// relies on: only an explicit y/yes consents; an empty line, other text, or
// EOF is a refusal. A default-yes regression here would let an accidental
// Enter stop a running model.
func TestAskYesNoDefaultsToNo(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  bool
	}{
		{"explicit y", "y\n", true},
		{"explicit yes", "YES\n", true},
		{"empty line refuses", "\n", false},
		{"other text refuses", "n\n", false},
		{"eof refuses", "", false},
	}
	for _, tc := range cases {
		ok, err := askYesNo(strings.NewReader(tc.input))
		if err != nil {
			t.Errorf("%s: err = %v, want nil", tc.name, err)
		}
		if ok != tc.want {
			t.Errorf("%s: askYesNo() = %v, want %v", tc.name, ok, tc.want)
		}
	}
}

// localmodelsEntry builds the occupant entry the engine reports, so the
// tests' expectations read as model names rather than struct literals.
func localmodelsEntry(id, name string) localmodels.Entry {
	return localmodels.Entry{ModelID: id, ModelName: name, Artifact: name, Registered: true}
}

// readSome drains everything readable from r without blocking, so the
// heartbeat test can poll for output while the ticker is still running.
func readSome(t *testing.T, r *os.File) string {
	t.Helper()
	if err := r.SetReadDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	var buf [4096]byte
	n, _ := r.Read(buf[:])
	return string(buf[:n])
}

// TestStartForLaunchCtrlCAbortsReplacePrompt verifies Ctrl+C while the y/N
// prompt is blocked aborts the start immediately. The prompt's terminal read
// cannot be interrupted and the signal handler swallows SIGINT, so without the
// ctx race the user would have to press Enter and then Ctrl+C a second time.
func TestStartForLaunchCtrlCAbortsReplacePrompt(t *testing.T) {
	cancel := stubSignals(t)
	oldTTY := stdinTTY
	stdinTTY = func() bool { return true }
	t.Cleanup(func() { stdinTTY = oldTTY })
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	oldConfirm := confirmReplace
	confirmReplace = func(string) (bool, error) {
		cancel()
		<-release // a terminal read that never returns until Enter
		return false, nil
	}
	t.Cleanup(func() { confirmReplace = oldConfirm })
	occ := &lifecycle.OccupiedError{Occupant: localmodelsEntry("omlx/other", "other")}
	replaces := stubLifecycleStart(t, []error{occ})

	err := startForLaunch(&config.Config{}, startTestRow(), false)
	if err == nil || !strings.Contains(err.Error(), "cancelled before omlx/qwen3.8") {
		t.Fatalf("err = %v, want a cancellation naming the model", err)
	}
	if len(*replaces) != 1 {
		t.Errorf("engine calls = %v, want 1 (no retry after Ctrl+C)", *replaces)
	}
}

// TestStartForLaunchFailureUsesSharedWording verifies a down daemon is worded
// like the TUI's status line, not as a raw engine error, while the typed cause
// stays on the error chain. The two start paths must not drift.
func TestStartForLaunchFailureUsesSharedWording(t *testing.T) {
	stubSignals(t)
	down := &lifecycle.DaemonDownError{Provider: "omlx", Origin: "http://localhost:8000"}
	stubLifecycleStart(t, []error{down})

	err := startForLaunch(&config.Config{}, startTestRow(), true)
	want := lifecycle.StartErrorMessage("omlx/qwen3.8", down)
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
	if !errors.Is(err, error(down)) {
		t.Errorf("err = %v, want DaemonDownError still on the chain", err)
	}
}
