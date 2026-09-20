package lifecycle

import (
	"context"
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
