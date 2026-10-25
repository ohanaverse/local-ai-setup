package lifecycle

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/provider"
)

// readySpec is a command-json process spec shaped like rec's: readiness is
// `<bin> status` printing a JSON object whose "ready" is true.
var readySpec = provider.ProcessSpec{
	Binary: "recx",
	Ready: provider.Ready{
		Kind:      provider.ReadyCommandJSON,
		Command:   []string{"status"},
		Path:      "ready",
		Equals:    "true",
		ModelPath: "model",
	},
}

// stubReadySpec makes processSpecFor answer readySpec for the recx family.
func stubReadySpec(t *testing.T) {
	t.Helper()
	old := processSpecFor
	processSpecFor = func(family string) (provider.ProcessSpec, bool) {
		if family == "recx" {
			return readySpec, true
		}
		return old(family)
	}
	t.Cleanup(func() { processSpecFor = old })
}

// countingProc reports exited from the n-th call on (n == 0: never).
type countingProc struct {
	calls  int32
	exitAt int32
}

func (p *countingProc) exited() (bool, error) {
	n := atomic.AddInt32(&p.calls, 1)
	if p.exitAt > 0 && n >= p.exitAt {
		return true, errors.New("exit status 1")
	}
	return false, nil
}

// TestReadyNowCommandJSON verifies command-json readiness reads only the
// configured JSON value: true (or the text "true") is ready and names the
// model, while false, a failed command, non-JSON, a missing path or a
// command that outlives its timeout are all "not ready" with no error. A
// readiness check that panics or reports ready on garbage output would hand
// an agent a server that is still loading.
func TestReadyNowCommandJSON(t *testing.T) {
	for name, tc := range map[string]struct {
		out   string
		err   error
		block bool
		want  readyState
	}{
		"ready":         {out: `{"ready":true,"model":"flint"}`, want: readyState{Ready: true, Model: "flint"}},
		"not ready":     {out: `{"ready":false}`},
		"text true":     {out: `{"ready":"true"}`, want: readyState{Ready: true}},
		"exit error":    {out: "", err: errors.New("exit status 1")},
		"error w/ json": {out: `{"ready":true}`, err: errors.New("exit status 1")},
		"not json":      {out: "not json"},
		"empty object":  {out: `{}`},
		"array":         {out: `[true]`},
		"object value":  {out: `{"ready":{"x":1}}`},
		"timeout":       {block: true},
	} {
		t.Run(name, func(t *testing.T) {
			e := testEnv()
			e.readyCmdTimeout = 20 * time.Millisecond
			var gotName string
			var gotArgs []string
			e.run = func(ctx context.Context, n string, args ...string) ([]byte, error) {
				gotName, gotArgs = n, args
				if _, ok := ctx.Deadline(); !ok {
					t.Error("readiness command ran without a deadline")
				}
				if tc.block {
					<-ctx.Done()
					return nil, ctx.Err()
				}
				return []byte(tc.out), tc.err
			}
			got, err := e.readyNow(context.Background(), readySpec, "/bin/recx", "http://127.0.0.1:1/v1/models", "flint", "")
			if err != nil {
				t.Fatalf("readyNow err = %v, want nil", err)
			}
			if got != tc.want {
				t.Errorf("readyNow = %+v, want %+v", got, tc.want)
			}
			if gotName != "/bin/recx" || strings.Join(gotArgs, " ") != "status" {
				t.Errorf("ran %q %q, want /bin/recx [status]", gotName, gotArgs)
			}
		})
	}
}

// TestWaitReadyKeepsWaitingWhilePortClosed verifies a server still loading
// (its readiness command says not ready, its process alive) is waited for,
// not failed: rec keeps its port closed for ~10 s while loading, and treating
// that as failure would kill every rec start.
func TestWaitReadyKeepsWaitingWhilePortClosed(t *testing.T) {
	stubReadySpec(t)
	e := testEnv()
	var runs int32
	e.run = func(ctx context.Context, n string, args ...string) ([]byte, error) {
		if atomic.AddInt32(&runs, 1) < 3 {
			return []byte("rec: no managed rec instance is reachable"), errors.New("exit status 1")
		}
		return []byte(`{"ready":true,"model":"flint"}`), nil
	}
	err := e.waitReady(context.Background(), "recx", &countingProc{}, "/bin/recx", "http://127.0.0.1:1/v1/models", "flint", "", time.Second)
	if err != nil {
		t.Fatalf("waitReady = %v, want nil", err)
	}
	if runs != 3 {
		t.Errorf("readiness command ran %d times, want 3", runs)
	}
}

// TestWaitReadyFailsFastWhenProcessDies verifies the wait ends the moment
// the spawned process has exited, with the "exited during model load" error,
// instead of polling out the whole load budget for a server that is gone.
func TestWaitReadyFailsFastWhenProcessDies(t *testing.T) {
	stubReadySpec(t)
	e := testEnv()
	var runs int32
	e.run = func(ctx context.Context, n string, args ...string) ([]byte, error) {
		atomic.AddInt32(&runs, 1)
		return []byte(`{"ready":false}`), nil
	}
	err := e.waitReady(context.Background(), "recx", &countingProc{exitAt: 2}, "/bin/recx", "http://127.0.0.1:1/v1/models", "flint", "", 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "exited during model load") {
		t.Fatalf("waitReady = %v, want an exited-during-load error", err)
	}
	if runs >= 10 {
		t.Errorf("readiness command ran %d times after the process died, want fewer than 10", runs)
	}
}

// TestWaitReadyRejectsAnInstanceServingAnotherModel verifies a readiness
// command that reports ready for a different model does not satisfy the
// wait: it times out naming the model the instance serves, while the same
// command naming the requested model still succeeds. A machine-scope
// family's readiness command answers for whatever instance holds the
// machine, so a foreign instance finishing its own load must not read as
// the server wt spawned being ready.
func TestWaitReadyRejectsAnInstanceServingAnotherModel(t *testing.T) {
	stubReadySpec(t)
	e := testEnv()
	e.run = func(ctx context.Context, n string, args ...string) ([]byte, error) {
		return []byte(`{"ready":true,"model":"other"}`), nil
	}
	err := e.waitReady(context.Background(), "recx", &countingProc{}, "/bin/recx", "http://127.0.0.1:1/v1/models", "flint", "", 50*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "recx reports an instance serving other, not flint") {
		t.Fatalf("waitReady = %v, want a timeout naming the other model", err)
	}
	e.run = func(ctx context.Context, n string, args ...string) ([]byte, error) {
		return []byte(`{"ready":true,"model":"flint"}`), nil
	}
	if err := e.waitReady(context.Background(), "recx", &countingProc{}, "/bin/recx", "http://127.0.0.1:1/v1/models", "flint", "", time.Second); err != nil {
		t.Errorf("waitReady for the matching model = %v, want nil", err)
	}
}

// TestWaitReadyTimeout verifies a server that never becomes ready ends the
// wait at the deadline with an error naming the family, and that a cancelled
// context ends it with the context's error: a start must not hang forever.
func TestWaitReadyTimeout(t *testing.T) {
	stubReadySpec(t)
	e := testEnv()
	e.run = func(ctx context.Context, n string, args ...string) ([]byte, error) {
		return []byte(`{"ready":false}`), nil
	}
	err := e.waitReady(context.Background(), "recx", &countingProc{}, "/bin/recx", "http://127.0.0.1:1/v1/models", "flint", "", 50*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "timed out") || !strings.Contains(err.Error(), "recx") {
		t.Fatalf("waitReady = %v, want a timeout naming recx", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := e.waitReady(ctx, "recx", &countingProc{}, "/bin/recx", "http://127.0.0.1:1/v1/models", "flint", "", time.Second); !errors.Is(err, context.Canceled) {
		t.Errorf("waitReady after cancel = %v, want context.Canceled", err)
	}
}

// TestWaitReadyTreatsAReadyAnswerWithNoModelAsNotReady verifies a ready
// answer that names no model, from a spec that reads one, does not end the
// wait: an instance mid-transition may serve something else, and warming the
// wrong model is worse than a timeout that says what was seen.
func TestWaitReadyTreatsAReadyAnswerWithNoModelAsNotReady(t *testing.T) {
	stubReadySpec(t)
	e := testEnv()
	e.run = func(context.Context, string, ...string) ([]byte, error) { return []byte(`{"ready":true}`), nil }
	err := e.waitReady(context.Background(), "recx", &countingProc{}, "/bin/recx", "http://127.0.0.1:1/v1/models", "flint", "", 50*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "names no model") {
		t.Fatalf("waitReady = %v, want a timeout saying the instance names no model", err)
	}
}
