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
// port; only one model is treated as the occupant.
type omlxBackend struct{}

func (omlxBackend) singleModel() bool { return true }

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
	// omlx serves directory basenames; registry names are HF repo ids.
	return e.warmup(ctx, origin+"/v1/chat/completions", path.Base(t.ModelName), health, e.warmupTimeout)
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

// stopModel: omlx serves one model at a time, so stopping its occupant is
// stopping the daemon's model.
func (b omlxBackend) stopModel(ctx context.Context, e *env, cfg *config.Config, _ string) error {
	return b.stop(ctx, e, cfg)
}
