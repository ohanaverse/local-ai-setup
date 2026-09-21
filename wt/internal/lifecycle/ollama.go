package lifecycle

import (
	"context"
	"errors"
	"fmt"
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
// other loaded model stay up. A non-zero exit is not by itself a failure: the
// goal is "the model is not loaded", so it re-probes /api/ps and succeeds when
// the model is already gone (a daemon restart or eviction between the picker's
// probe and this stop makes `ollama stop` exit 1 with "model not found") —
// the same trust-the-state-not-the-exit-code shape omlx and mtplx use. When
// the re-probe cannot answer, or the model is still loaded, the error carries
// ollama's own message.
func (ollamaBackend) stopModel(ctx context.Context, e *env, cfg *config.Config, modelName string) error {
	bin, err := e.lookPath("ollama")
	if err != nil {
		return &BinaryMissingError{Binary: "ollama"}
	}
	out, runErr := e.run(ctx, bin, "stop", modelName)
	if runErr == nil {
		return nil
	}
	origin, _ := localmodels.FamilyOrigin(cfg, "ollama")
	if loaded, perr := localmodels.OllamaLoaded(e.probeClient, origin); perr == nil && !anySameModel("ollama", loaded, modelName) {
		return nil
	}
	if msg := strings.TrimSpace(string(out)); msg != "" {
		return errors.New(msg)
	}
	return fmt.Errorf("ollama stop %s failed: %w", modelName, runErr)
}

// anySameModel reports whether any name in names denotes want under family's
// matching rule.
func anySameModel(family string, names []string, want string) bool {
	for _, n := range names {
		if sameModel(family, n, want) {
			return true
		}
	}
	return false
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
