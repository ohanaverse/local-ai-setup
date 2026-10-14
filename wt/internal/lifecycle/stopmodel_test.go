package lifecycle

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// TestStopModelOllamaRunsOllamaStop verifies stopModel unloads exactly the
// named ollama model via `ollama stop <name>`, pinned to the registry origin
// with OLLAMA_HOST so the CLI stops the same daemon the re-probe queries (an
// inherited OLLAMA_HOST pointing elsewhere would stop a daemon wt never
// probes). Ollama is multi-tenant, so the post-exit picker must stop only the
// model the user ticked, never the daemon or the other loaded models.
func TestStopModelOllamaRunsOllamaStop(t *testing.T) {
	var ran, envs []string
	e := testEnv()
	e.lookPath = func(string) (string, error) { return "/bin/ollama", nil }
	e.runEnv = func(_ context.Context, env []string, name string, args ...string) ([]byte, error) {
		ran = append(ran, name+" "+strings.Join(args, " "))
		envs = append(envs, env...)
		return nil, nil
	}
	daemon := psServing(t) // nothing loaded; the re-probe never reaches a real daemon
	if err := stopModel(context.Background(), e, provCfg("ollama", daemon.URL), "ollama", "qwen3.8:27b-mlx"); err != nil {
		t.Fatalf("stopModel: %v", err)
	}
	if len(ran) != 1 || ran[0] != "/bin/ollama stop qwen3.8:27b-mlx" {
		t.Fatalf("ran = %v, want [/bin/ollama stop qwen3.8:27b-mlx]", ran)
	}
	if len(envs) != 1 || envs[0] != "OLLAMA_HOST="+daemon.URL {
		t.Fatalf("env = %v, want [OLLAMA_HOST=%s]", envs, daemon.URL)
	}
}

// psServing is a stub ollama daemon whose /api/ps lists the given loaded
// models; used so the post-failure re-probe never touches a real daemon.
// Built on the serveFree listener handoff like every server in this package.
func psServing(t *testing.T, loaded ...string) *httptest.Server {
	t.Helper()
	srv, _ := serveFree(t, psBodyHandler(loaded...))
	return srv
}

// psBodyHandler answers every request with an /api/ps body listing loaded.
func psBodyHandler(loaded ...string) http.Handler {
	body := `{"models":[`
	for i, n := range loaded {
		if i > 0 {
			body += ","
		}
		body += `{"name":"` + n + `"}`
	}
	body += `]}`
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	})
}

// failingOllamaStop is an env whose `ollama stop` exits 1 with "model not found".
func failingOllamaStop() *env {
	e := testEnv()
	e.lookPath = func(string) (string, error) { return "/bin/ollama", nil }
	e.runEnv = func(context.Context, []string, string, ...string) ([]byte, error) {
		return []byte("model not found\n"), errors.New("exit 1")
	}
	return e
}

// TestStopModelOllamaAlreadyUnloadedIsSuccess verifies that when `ollama stop`
// exits non-zero but /api/ps shows the model is no longer loaded (daemon
// restart or eviction between the picker's probe and the stop), the stop is a
// success. The goal — the model is not loaded — is met, and a spurious
// "failed: model not found" reads as a broken provider CLI.
func TestStopModelOllamaAlreadyUnloadedIsSuccess(t *testing.T) {
	daemon := psServing(t) // nothing loaded
	err := stopModel(context.Background(), failingOllamaStop(), provCfg("ollama", daemon.URL), "ollama", "x")
	if err != nil {
		t.Fatalf("err = %v, want nil: the model is already unloaded", err)
	}
}

// TestStopModelOllamaStillLoadedSurfacesFailure verifies a failed `ollama stop`
// is still an error when /api/ps shows the model loaded — including under
// ollama's implicit ":latest" tag — so a real failure is never masked. It is
// returned after the one probe: wt waits for an unload only after a stop
// ollama accepted (exit 0), so a refused stop never sits out the wait's bound.
func TestStopModelOllamaStillLoadedSurfacesFailure(t *testing.T) {
	for _, loaded := range []string{"x", "x:latest"} {
		daemon, probes := psSequence(t, func(int) ([]string, bool) { return []string{loaded}, true })
		err := stopModel(context.Background(), failingOllamaStop(), provCfg("ollama", daemon.URL), "ollama", "x")
		if err == nil || err.Error() != "model not found" {
			t.Errorf("loaded=%q: err = %v, want ollama's own message", loaded, err)
		}
		if got := probes.Load(); got != 1 {
			t.Errorf("loaded=%q: probes = %d, want 1 (a refused stop is reported at once)", loaded, got)
		}
	}
}

// psSequence is a stub ollama daemon whose /api/ps answers the nth probe
// (from 1) with step(n): the loaded models, or ok=false for a 500 (a probe
// that cannot answer). It returns the probe counter, so a test of the wait
// after `ollama stop` advances by counting probes and never by sleeping.
func psSequence(t *testing.T, step func(n int) (loaded []string, ok bool)) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var probes atomic.Int32
	srv, _ := serveFree(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		loaded, ok := step(int(probes.Add(1)))
		if !ok {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		psBodyHandler(loaded...).ServeHTTP(w, r)
	}))
	return srv, &probes
}

// cleanOllamaStop is an env whose `ollama stop` exits 0, as it does when
// ollama accepts a stop, and whose wait for the unload is 5 probes after the
// first (bound 5ns at a 1ns interval), so no test waits on a clock.
func cleanOllamaStop() *env {
	e := testEnv()
	e.lookPath = func(string) (string, error) { return "/bin/ollama", nil }
	e.runEnv = func(context.Context, []string, string, ...string) ([]byte, error) { return nil, nil }
	e.pollInterval = time.Nanosecond
	e.ollamaUnloadTimeout = 5 * time.Nanosecond
	return e
}

// TestStopModelOllamaWaitsForInFlightRequest verifies the false failure is
// gone: ollama accepts a stop of a model that is still answering a request,
// keeps it in /api/ps ("Stopping...") and unloads it some seconds later, so a
// model listed on the first probe and gone on a later one is a successful
// stop. Before the wait, the picker printed "failed" and `wt stop` exited 1
// for a model that did stop. The implicit ":latest" tag counts as listed.
func TestStopModelOllamaWaitsForInFlightRequest(t *testing.T) {
	for _, listed := range []string{"x", "x:latest"} {
		daemon, probes := psSequence(t, func(n int) ([]string, bool) {
			if n <= 3 {
				return []string{listed, "other"}, true
			}
			return []string{"other"}, true
		})
		if err := stopModel(context.Background(), cleanOllamaStop(), provCfg("ollama", daemon.URL), "ollama", "x"); err != nil {
			t.Errorf("listed=%q: err = %v, want nil: the model left /api/ps on the 4th probe", listed, err)
		}
		if got := probes.Load(); got != 4 {
			t.Errorf("listed=%q: probes = %d, want 4 (wt stops probing once the model is gone)", listed, got)
		}
	}
}

// TestStopModelOllamaExitZeroButStillLoadedIsError verifies a clean `ollama
// stop` exit is never trusted by itself: when /api/ps still lists the model
// once the wait's bound has passed (a stop racing a re-load, a request that
// outlasts the bound, or a CLI quirk reporting success wrongly), the stop is
// an error, so the picker never prints "done" for a loaded model. The message
// says how long wt waited and that ollama may still be finishing a request,
// since the user's next step is to look again, not to assume a broken stop.
// The probes are bounded: the first one plus bound/interval, never more.
func TestStopModelOllamaExitZeroButStillLoadedIsError(t *testing.T) {
	daemon, probes := psSequence(t, func(int) ([]string, bool) { return []string{"x"}, true })
	e := cleanOllamaStop()
	e.pollInterval = 15 * time.Nanosecond
	e.ollamaUnloadTimeout = 60 * time.Nanosecond
	err := stopModel(context.Background(), e, provCfg("ollama", daemon.URL), "ollama", "x")
	want := "ollama stop x: /api/ps still lists the model after waiting 60ns (ollama may still be finishing a request; `ollama ps` shows whether it has unloaded)"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v\nwant  %s", err, want)
	}
	if got := probes.Load(); got != 5 {
		t.Errorf("probes = %d, want 5 (the first, then 60ns/15ns more)", got)
	}
}

// TestStopModelOllamaDefaultWaitCoversObservedUnloads pins the production
// bound and interval: ollama was seen to take 6 to 20 seconds to unload a
// model with a request in flight, and 55 seconds behind a long generation, so
// the wait must cover that with margin while a stop that will never finish
// still ends within about three minutes.
func TestStopModelOllamaDefaultWaitCoversObservedUnloads(t *testing.T) {
	e := defaultEnv()
	if e.ollamaUnloadTimeout != 180*time.Second || e.pollInterval != time.Second {
		t.Fatalf("wait = %v every %v, want 3m0s every 1s", e.ollamaUnloadTimeout, e.pollInterval)
	}
	want := "ollama stop x: /api/ps still lists the model after waiting 180s (ollama may still be finishing a request; `ollama ps` shows whether it has unloaded)"
	if got := ollamaStillListed("x", e.ollamaUnloadTimeout).Error(); got != want {
		t.Fatalf("message = %s\nwant      %s", got, want)
	}
}

// afterTrip is a transport that hands back each response fully read and then
// calls after with the probe's number (from 1). A cancellation made there
// lands after the probe answered and before the wait's next step, with no
// timer: the buffered body reads the same whatever the context says.
type afterTrip struct {
	n     int
	after func(n int)
}

func (a *afterTrip) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return nil, err
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	a.n++
	a.after(a.n)
	return resp, nil
}

// TestStopModelOllamaCancelledDuringWaitReturnsCancel verifies Ctrl-C during
// the wait for an unload surfaces as a cancellation, whether it lands between
// two probes or inside one, the first included: the picker prints
// "cancelled", never "failed", and never "done" (a cancelled probe fails, and
// a failed probe after a clean exit would otherwise read as success).
func TestStopModelOllamaCancelledDuringWaitReturnsCancel(t *testing.T) {
	t.Run("between probes", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		daemon, probes := psSequence(t, func(int) ([]string, bool) { return []string{"x"}, true })
		e := cleanOllamaStop()
		e.ollamaUnloadTimeout = 100 * time.Nanosecond
		e.probeClient = &http.Client{Transport: &afterTrip{after: func(n int) {
			if n == 2 {
				cancel()
			}
		}}}
		if err := stopModel(ctx, e, provCfg("ollama", daemon.URL), "ollama", "x"); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		if got := probes.Load(); got != 2 {
			t.Errorf("probes = %d, want 2 (none after the cancellation)", got)
		}
	})
	for _, at := range []int{1, 3} {
		t.Run(fmt.Sprintf("inside probe %d", at), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			daemon, probes := psSequence(t, func(n int) ([]string, bool) {
				if n == at {
					cancel()
					return nil, false // what the probe is told no longer matters
				}
				return []string{"x"}, true
			})
			if err := stopModel(ctx, cleanOllamaStop(), provCfg("ollama", daemon.URL), "ollama", "x"); !errors.Is(err, context.Canceled) {
				t.Fatalf("err = %v, want context.Canceled", err)
			}
			if got := int(probes.Load()); got != at {
				t.Errorf("probes = %d, want %d (none after the cancellation)", got, at)
			}
		})
	}
}

// TestStopModelOllamaProbeFailureDuringWaitDefersToCLI verifies a daemon that
// stops answering part-way through the wait ends it at once under the same
// rule as a first probe that cannot answer: the CLI's exit decides, and it
// was 0 (wt waits only after a clean exit), so the stop is a success and wt
// does not sit out the bound against a daemon with nothing left to check.
func TestStopModelOllamaProbeFailureDuringWaitDefersToCLI(t *testing.T) {
	daemon, probes := psSequence(t, func(n int) ([]string, bool) { return []string{"x"}, n <= 2 })
	if err := stopModel(context.Background(), cleanOllamaStop(), provCfg("ollama", daemon.URL), "ollama", "x"); err != nil {
		t.Fatalf("err = %v, want nil: the probe gave no verdict and `ollama stop` exited 0", err)
	}
	if got := probes.Load(); got != 3 {
		t.Errorf("probes = %d, want 3 (no probe after the one that failed)", got)
	}
}

// TestStopModelOllamaCancelledReturnsCancelWithoutProbe verifies the picker's
// Ctrl-C path surfaces as a cancellation, not a success: the re-probe never
// runs on a cancelled stop, where the model unloading during the probe's
// timeout window would turn "cancelled" into "done".
func TestStopModelOllamaCancelledReturnsCancelWithoutProbe(t *testing.T) {
	daemon := psServing(t) // nothing loaded: a probe would report success
	e := failingOllamaStop()
	ctx, cancel := context.WithCancel(context.Background())
	e.runEnv = func(_ context.Context, _ []string, _ string, _ ...string) ([]byte, error) {
		cancel() // the picker cancels the stop in flight
		return nil, errors.New("exit 1")
	}
	if err := stopModel(ctx, e, provCfg("ollama", daemon.URL), "ollama", "x"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// TestStopModelOllamaReprobeFailureKeepsOriginalError verifies that when the
// re-probe itself cannot get an answer (daemon gone), the original CLI message
// is returned rather than guessing success: with no evidence the model is
// unloaded, the stop is reported as it failed.
//
// The daemon is a server answering 500, the deterministic stand-in for a
// re-probe that cannot get an answer, rather than a closed one: closing a real
// server would leave a window in which another process takes the freed
// ephemeral port — the flake vector #131's listener handoff removed, since a
// stranger's JSON without a "models" key decodes to an empty loaded list and
// would fake a successful stop.
func TestStopModelOllamaReprobeFailureKeepsOriginalError(t *testing.T) {
	err := stopModel(context.Background(), failingOllamaStop(), provCfg("ollama", errorSrv(t)), "ollama", "x")
	if err == nil || err.Error() != "model not found" {
		t.Fatalf("err = %v, want ollama's own message", err)
	}
}

// TestStopModelOllamaMissingBinary verifies a missing binary returns
// *BinaryMissingError (no probe happens, so &config.Config{} is fine there).
func TestStopModelOllamaMissingBinary(t *testing.T) {
	e := testEnv()
	e.lookPath = func(string) (string, error) { return "", errors.New("nope") }
	var bm *BinaryMissingError
	if err := stopModel(context.Background(), e, &config.Config{}, "ollama", "x"); !errors.As(err, &bm) {
		t.Fatalf("err = %v, want *BinaryMissingError", err)
	}
}

// TestStopModelRoutesToProviderBackend verifies dispatch: the picker's
// `stopModel` reaches the backend registered for each provider id (including
// the `omlx-6bit` alias). Body unchanged from the original delegation test.
func TestStopModelRoutesToProviderBackend(t *testing.T) {
	var calls []string
	e := fakeEnv(localmodels.Snapshot{}, true, &calls)
	for _, provider := range []string{"omlx", "omlx-6bit", "mtplx"} {
		calls = nil
		if err := stopModel(context.Background(), e, &config.Config{}, provider, "m"); err != nil {
			t.Fatalf("%s: stopModel: %v", provider, err)
		}
		if len(calls) != 1 || calls[0] != "stopModel:m" {
			t.Errorf("%s: calls = %v, want [stopModel:m]", provider, calls)
		}
	}
}

// TestStopModelUnsupportedProvider verifies providers without a backend
// return *UnsupportedError rather than silently reporting success, so the
// picker never claims to have stopped a model it cannot control.
func TestStopModelUnsupportedProvider(t *testing.T) {
	var calls []string
	e := fakeEnv(localmodels.Snapshot{}, true, &calls)
	var ue *UnsupportedError
	if err := stopModel(context.Background(), e, &config.Config{}, "mlx_lm_server", "m"); !errors.As(err, &ue) {
		t.Fatalf("err = %v, want *UnsupportedError", err)
	}
}

// TestCanStop verifies CanStop is true exactly for the families wt has a stop
// backend for (including the omlx-6bit alias) and false for mlx_lm_server and
// unknown ids, keeping the post-exit picker from offering unstoppable models.
func TestCanStop(t *testing.T) {
	for id, want := range map[string]bool{
		"ollama": true, "omlx": true, "omlx-6bit": true, "mtplx": true,
		"mlx_lm_server": false, "anthropic": false,
	} {
		if got := CanStop(id); got != want {
			t.Errorf("CanStop(%q) = %v, want %v", id, got, want)
		}
	}
}

// TestStopModelMtplxRunsMtplxStopAndWaitsForPort is the mtplx counterpart: the
// thunk must run `mtplx stop --port N ...` and confirm the port closed.
func TestStopModelMtplxRunsMtplxStopAndWaitsForPort(t *testing.T) {
	srv, addr := serveFree(t, chatHandler())
	_, portStr, _ := net.SplitHostPort(addr)
	e := testEnv()
	e.lookPath = func(string) (string, error) { return "/bin/mtplx", nil }
	var ran []string
	e.run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		ran = append([]string{name}, args...)
		srv.Close()
		return nil, nil
	}
	if err := stopModel(context.Background(), e, provCfg("mtplx", "http://"+addr+"/v1"), "mtplx", "m"); err != nil {
		t.Fatalf("stopModel: %v", err)
	}
	want := []string{"/bin/mtplx", "stop", "--port", portStr, "--grace-seconds", "10"}
	if !reflect.DeepEqual(ran, want) {
		t.Fatalf("ran = %v, want %v", ran, want)
	}
}
