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

func (ollamaBackend) singleModel() bool { return false }

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
// code. When the re-probe cannot answer, the CLI's exit decides (exit 0 with
// a daemon gone mid-stop has nothing left to check); a cancelled stop returns
// the cancellation without probing, so Ctrl-C can never be read as success.
func (ollamaBackend) stopModel(ctx context.Context, e *env, cfg *config.Config, modelName string) error {
	bin, err := e.lookPath("ollama")
	if err != nil {
		return &BinaryMissingError{Binary: "ollama"}
	}
	origin, _ := localmodels.FamilyOrigin(cfg, "ollama")
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
	loaded, perr := localmodels.OllamaLoaded(ctx, e.probeClient, origin)
	if perr == nil {
		if slices.ContainsFunc(loaded, func(n string) bool { return SameModel("ollama", n, modelName) }) {
			if cliErr != nil {
				return cliErr
			}
			return fmt.Errorf("ollama stop %s: /api/ps still lists the model", modelName)
		}
		return nil
	}
	// The re-probe cannot answer: the daemon gave no usable verdict, so the
	// CLI's own exit decides.
	return cliErr
}

func (ollamaBackend) start(ctx context.Context, e *env, cfg *config.Config, t Target, report func(Stage)) error {
	origin, _ := localmodels.FamilyOrigin(cfg, "ollama")
	health := origin + "/api/tags"
	if responded, _ := e.probe(ctx, health, 2*time.Second); !responded {
		if err := ctx.Err(); err != nil {
			return err
		}
		return &DaemonDownError{Provider: "ollama", Origin: origin}
	}
	report(StageWarming)
	return e.warmup(ctx, origin+"/v1/chat/completions", t.ModelName, health, e.warmupTimeout)
}
