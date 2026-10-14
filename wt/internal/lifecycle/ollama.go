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
// other loaded model stay up. The CLI is pinned to the registry origin with
// OLLAMA_HOST so it stops the same daemon the re-probe queries — an inherited
// OLLAMA_HOST pointing elsewhere would make the CLI stop against a daemon the
// probe never sees, reporting success for a model still loaded. The exit code
// is never trusted by itself: the goal is "the model is not loaded", so it
// re-probes /api/ps and succeeds when the model is already gone (a daemon
// restart or eviction between the picker's probe and this stop makes
// `ollama stop` exit 1 with "model not found") — the same
// trust-the-state-not-the-exit-code shape omlx and mtplx use, on every exit
// code.
//
// A model still listed is judged by the exit. Non-zero is a stop ollama
// refused: its message is returned at once. Zero is a stop ollama accepted
// and has not finished: with a request to the model in flight ollama keeps it
// in /api/ps (as "Stopping...") and unloads it when the request ends, so wt
// re-probes every pollInterval until the model is gone, and only a model
// still listed after ollamaUnloadTimeout is an error.
//
// When a probe cannot answer, the first or a later one, the CLI's exit
// decides and the wait ends (exit 0 with a daemon gone mid-stop has nothing
// left to check). A cancelled stop returns the cancellation, without probing
// when it landed in the CLI and without another probe when it landed in the
// wait, so Ctrl-C can never be read as success.
func (ollamaBackend) stopModel(ctx context.Context, e *env, cfg *config.Config, modelName string) error {
	bin, err := e.lookPath("ollama")
	if err != nil {
		return &BinaryMissingError{Binary: "ollama"}
	}
	origin := localmodels.FamilyOrigin(cfg, "ollama")
	out, runErr := e.runEnv(ctx, []string{"OLLAMA_HOST=" + origin}, bin, "stop", modelName)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var cliErr error
	if runErr != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			cliErr = errors.New(msg)
		} else {
			cliErr = fmt.Errorf("ollama stop %s failed: %w", modelName, runErr)
		}
	}
	// The probes after the first: none for a refused stop, and a count, not a
	// deadline, so the wait is bounded whatever a clock says.
	waits := 0
	if cliErr == nil && e.pollInterval > 0 {
		waits = int(e.ollamaUnloadTimeout / e.pollInterval)
	}
	for ; ; waits-- {
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
		if waits <= 0 {
			return ollamaStillListed(modelName, e.ollamaUnloadTimeout)
		}
		if err := e.sleep(ctx, e.pollInterval); err != nil {
			return err
		}
	}
}

// ollamaStillListed is the failed stop of a model `ollama stop` accepted and
// /api/ps still lists after the whole wait. It says how long wt waited and
// what the likeliest cause is, since the model usually does unload.
func ollamaStillListed(modelName string, waited time.Duration) error {
	d := waited.String()
	if waited >= time.Second {
		d = fmt.Sprintf("%.0fs", waited.Seconds())
	}
	return fmt.Errorf("ollama stop %s: /api/ps still lists the model after waiting %s (ollama may still be finishing a request; `ollama ps` shows whether it has unloaded)", modelName, d)
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
