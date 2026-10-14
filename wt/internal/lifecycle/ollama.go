package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// ollamaBackend loads a model into a running ollama daemon. There is no
// process to start (wt does not kickstart the daemon) and ollama is
// multi-tenant, so nothing is ever replaced.
type ollamaBackend struct{}

func (ollamaBackend) tenancy() Tenancy { return Shared }

func (ollamaBackend) stop(ctx context.Context, e *env, cfg *config.Config) error { return nil }

// stopModel unloads one model with `ollama stop <name>`; the daemon and every
// other loaded model stay up. It is a batch of one: sendStop, then
// awaitUnload under a wait of its own. A batch (StopBatch) makes the same two
// calls with every sendStop first and one wait shared by all of them.
func (b ollamaBackend) stopModel(ctx context.Context, e *env, cfg *config.Config, modelName string) error {
	sent := b.sendStop(ctx, e, cfg, modelName)
	if sent.fatal != nil {
		return sent.fatal
	}
	return b.awaitUnload(ctx, e, cfg, modelName, sent.cliErr, newUnloadWait(e))
}

// stopSent is what one `ollama stop` came back with. fatal ends the stop with
// no probe: a missing binary, or a cancellation that landed in the CLI (so
// Ctrl-C can never be read as success). cliErr is a stop ollama refused (a
// non-zero exit, with ollama's own message when it gave one); nil with no
// fatal is a stop ollama accepted.
type stopSent struct {
	fatal  error
	cliErr error
}

// sendStop runs `ollama stop <name>`. The CLI is pinned to the registry
// origin with OLLAMA_HOST so it stops the same daemon the re-probe queries —
// an inherited OLLAMA_HOST pointing elsewhere would make the CLI stop against
// a daemon the probe never sees, reporting success for a model still loaded.
// ollama answers at once: a stop of a model with a request in flight is
// accepted (exit 0) and carried out when the request ends.
func (ollamaBackend) sendStop(ctx context.Context, e *env, cfg *config.Config, modelName string) stopSent {
	bin, err := e.lookPath("ollama")
	if err != nil {
		return stopSent{fatal: &BinaryMissingError{Binary: "ollama"}}
	}
	origin := localmodels.FamilyOrigin(cfg, "ollama")
	out, runErr := e.runEnv(ctx, []string{"OLLAMA_HOST=" + origin}, bin, "stop", modelName)
	if err := ctx.Err(); err != nil {
		return stopSent{fatal: err}
	}
	if runErr == nil {
		return stopSent{}
	}
	if msg := strings.TrimSpace(string(out)); msg != "" {
		return stopSent{cliErr: errors.New(msg)}
	}
	return stopSent{cliErr: fmt.Errorf("ollama stop %s failed: %w", modelName, runErr)}
}

// awaitUnload decides a sent stop by /api/ps. The exit code is never trusted
// by itself: the goal is "the model is not loaded", so a model already gone
// is a success on every exit code (a daemon restart or eviction between the
// picker's probe and this stop makes `ollama stop` exit 1 with "model not
// found") — the same trust-the-state-not-the-exit-code shape omlx and mtplx
// use.
//
// A model still listed is judged by the exit. A refused stop (cliErr) returns
// ollama's message at once and never waits. An accepted one has not finished:
// with a request to the model in flight ollama keeps it in /api/ps (as
// "Stopping...") and unloads it when the request ends, so wt re-probes every
// pollInterval, spending w's probes, until the model is gone. A model still
// listed once w is spent is a *StillFinishingError: ollama accepted the stop
// and will carry it out, which a caller that needs the model gone treats as
// the failure it is and the stop loop reports as its own outcome.
//
// Once the wait has slept stopNoticeAfter for this model it gives one notice
// (stopNotice): the line that otherwise sits open for up to the whole bound.
//
// When a probe cannot answer, the first or a later one, the CLI's exit
// decides and the wait ends (exit 0 with a daemon gone mid-stop has nothing
// left to check). A cancellation in the wait returns the cancellation without
// another probe, so Ctrl-C can never be read as success.
func (ollamaBackend) awaitUnload(ctx context.Context, e *env, cfg *config.Config, modelName string, cliErr error, w *unloadWait) error {
	origin := localmodels.FamilyOrigin(cfg, "ollama")
	// What was left of the wait when this model's began: the notice's "up
	// to", which in a batch is what the stops before it have not spent.
	left := w.remaining()
	slept := time.Duration(0)
	noticed := false
	for {
		loaded, perr := localmodels.OllamaLoaded(ctx, e.probeClient, origin)
		if err := ctx.Err(); err != nil {
			return err
		}
		if perr != nil {
			// The probe cannot answer: the daemon gave no usable verdict, so
			// the CLI's own exit decides.
			return cliErr
		}
		if !slices.ContainsFunc(loaded, func(n string) bool { return SameModel("ollama", n, modelName) }) {
			return nil
		}
		if cliErr != nil {
			return cliErr
		}
		if w.left <= 0 {
			return &StillFinishingError{Provider: "ollama", Model: modelName, Waited: w.slept()}
		}
		if !noticed && e.stopNoticeAfter > 0 && slept >= e.stopNoticeAfter {
			noticed = true
			stopNotice(ctx, StopNotice{Msg: fmt.Sprintf("waiting for ollama to finish a request in flight (up to %s)", roundWait(left))})
		}
		if err := e.sleep(ctx, w.interval); err != nil {
			return err
		}
		w.left--
		slept += w.interval
	}
}

// unloadWait is the bound on waiting for ollama to unload what it accepted a
// stop of: a count of probes after each model's first, not a deadline, so the
// wait is bounded whatever a clock says. One stop has one; a batch's stops
// share one (StopBatch), so N models finishing long requests cost one bound,
// not N.
type unloadWait struct {
	left, total int
	interval    time.Duration
}

// newUnloadWait is ollamaUnloadTimeout as a count of pollInterval sleeps.
func newUnloadWait(e *env) *unloadWait {
	n := 0
	if e.pollInterval > 0 {
		n = int(e.ollamaUnloadTimeout / e.pollInterval)
	}
	return &unloadWait{left: n, total: n, interval: e.pollInterval}
}

// remaining is the sleeping time the wait has left.
func (w *unloadWait) remaining() time.Duration { return time.Duration(w.left) * w.interval }

// slept is the sleeping total the wait has sat out, interval by interval,
// not the configured bound: integer division can cut the count under
// ollamaUnloadTimeout, and a message must not claim more than the wait sat
// out.
func (w *unloadWait) slept() time.Duration { return time.Duration(w.total-w.left) * w.interval }

// roundWait prints d to the second once it is a second or more ("3m0s").
func roundWait(d time.Duration) string {
	if d >= time.Second {
		d = d.Round(time.Second)
	}
	return d.String()
}

// StillFinishingError is a stop the provider accepted and has not carried
// out within wt's wait: ollama keeps a model with a request in flight loaded
// until that request ends, then unloads it by itself. It is an error, so a
// caller that needs the model gone before it goes on fails on it as on any
// other; the stop loop (internal/survey) reports it as its own outcome, not a
// failed stop. Waited is the sleeping total the wait sat out.
type StillFinishingError struct {
	Provider string
	Model    string
	Waited   time.Duration
}

func (e *StillFinishingError) Error() string {
	d := e.Waited.String()
	if e.Waited >= time.Second {
		d = fmt.Sprintf("%.0fs", e.Waited.Seconds())
	}
	return fmt.Sprintf("%s stop %s: /api/ps still lists the model after waiting %s (ollama may still be finishing a request; `ollama ps` shows whether it has unloaded)", e.Provider, e.Model, d)
}

// ollamaStillListed is the outcome of a model `ollama stop` accepted and
// /api/ps still lists after waiting waited.
func ollamaStillListed(modelName string, waited time.Duration) error {
	return &StillFinishingError{Provider: "ollama", Model: modelName, Waited: waited}
}

func (ollamaBackend) start(ctx context.Context, e *env, cfg *config.Config, t Target, report func(Stage)) error {
	origin := localmodels.FamilyOrigin(cfg, "ollama")
	health := origin + "/api/tags"
	if responded, _ := e.probe(ctx, health, 2*time.Second); !responded {
		if err := ctx.Err(); err != nil {
			return err
		}
		return &DaemonDownError{Provider: "ollama", Origin: origin}
	}
	report(StageWarming)
	return e.warmup(ctx, origin+"/v1/chat/completions", t.ModelName, health, "", e.warmupTimeout)
}
