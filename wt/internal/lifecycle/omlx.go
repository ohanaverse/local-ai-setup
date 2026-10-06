package lifecycle

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// omlxBackend serves omlx and omlx-6bit, which are ONE physical daemon on one
// port holding a pool of loaded models.
type omlxBackend struct{}

func (omlxBackend) tenancy() Tenancy { return Pool }

func (omlxBackend) start(ctx context.Context, e *env, cfg *config.Config, t Target, report func(Stage)) error {
	origin, _ := localmodels.FamilyOrigin(cfg, "omlx")
	health := origin + "/v1/models"
	if responded, _ := e.probe(ctx, health, 2*time.Second); !responded {
		if err := ctx.Err(); err != nil {
			return err
		}
		bin, err := e.lookPath("omlx")
		if err != nil {
			return &BinaryMissingError{Binary: "omlx"}
		}
		report(StageStarting)
		out, runErr := e.run(ctx, bin, "start")
		up, werr := e.waitPortOpen(ctx, health, e.portUpTimeout)
		if werr != nil {
			return werr
		}
		if !up {
			return fmt.Errorf("omlx did not come up at %s (omlx start: %v: %s)", health, runErr, strings.TrimSpace(string(out)))
		}
	}
	report(StageWarming)
	return omlxLoad(ctx, e, cfg, t.ModelName)
}

// omlxWarm is `wt warm`'s request (the start path loads through omlxLoad): a
// chat completion that makes the omlx server already answering load modelName.
// omlx serves directory basenames; registry names are HF repo ids. The
// request carries the key the registry's omlx provider names, when it names
// one: an omlx started with an API key refuses a keyless chat completion
// (#256), and that same 401 on the liveness probe still counts as "up".
func omlxWarm(ctx context.Context, e *env, cfg *config.Config, modelName string) error {
	origin, _ := localmodels.FamilyOrigin(cfg, "omlx")
	key := localmodels.FamilyAPIKey(cfg, "omlx")
	return e.warmup(ctx, origin+"/v1/chat/completions", path.Base(modelName), origin+"/v1/models", key, e.warmupTimeout)
}

func (omlxBackend) stop(ctx context.Context, e *env, cfg *config.Config) error {
	origin, _ := localmodels.FamilyOrigin(cfg, "omlx")
	if bin, err := e.lookPath("omlx"); err == nil {
		_, _ = e.run(ctx, bin, "stop")
	}
	closed, err := e.portClosedWithin(ctx, origin+"/v1/models", e.stopTimeout)
	if err != nil {
		return err
	}
	if !closed {
		return fmt.Errorf("omlx still listening at %s", origin)
	}
	return nil
}

// stopModel unloads the one named model; the service and every other loaded
// model stay up (omlxUnload).
func (omlxBackend) stopModel(ctx context.Context, e *env, cfg *config.Config, modelName string) error {
	return omlxUnload(ctx, e, cfg, modelName)
}

// Warm loads modelName into providerID's server, which must already be
// running: one keyed warmup request, nothing started, stopped or routed. It
// exists for modelman, whose own warmup is keyless and resolves no
// secret_ref — so it is omlx only, the one local server that can want a key.
func Warm(ctx context.Context, cfg *config.Config, providerID, modelName string) error {
	return warm(ctx, defaultEnv(), cfg, providerID, modelName)
}

func warm(ctx context.Context, e *env, cfg *config.Config, providerID, modelName string) error {
	if localmodels.Family(providerID) != "omlx" {
		return &UnsupportedError{ProviderID: providerID}
	}
	return omlxWarm(ctx, e, cfg, modelName)
}
