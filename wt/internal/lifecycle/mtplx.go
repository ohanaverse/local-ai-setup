package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// mtplxBackend runs one `mtplx serve` process per model on one port, so
// starting another model replaces the current one.
type mtplxBackend struct{}

func (mtplxBackend) singleModel() bool { return true }

// mtplxEndpoint returns the origin, /v1/models URL and port for the family.
// All three come from one resolution so the port passed to `mtplx serve` cannot
// differ from the port the wait polls.
func mtplxEndpoint(cfg *config.Config) (origin, modelsURL string, port int) {
	origin, port, err := localmodels.FamilyOriginPort(cfg, "mtplx")
	if err != nil {
		// FamilyOriginPort only fails on an unparseable registry origin. Fall back
		// through the same resolver with no registry rather than to a local
		// constant, so the port still has exactly one source.
		origin, port, _ = localmodels.FamilyOriginPort(&config.Config{}, "mtplx")
	}
	return origin, origin + "/v1/models", port
}

func (mtplxBackend) start(ctx context.Context, e *env, cfg *config.Config, t Target, report func(Stage)) (err error) {
	bin, lerr := e.lookPath("mtplx")
	if lerr != nil {
		return &BinaryMissingError{Binary: "mtplx"}
	}
	origin, models, port := mtplxEndpoint(cfg)
	report(StageStarting)
	closed, cerr := e.portClosedWithin(ctx, models, e.prebindTimeout)
	if cerr != nil {
		return cerr
	}
	if !closed {
		return &PortBusyError{Port: port}
	}
	sp, serr := e.mtplxProc.spawn(bin, []string{
		"serve", "--model", t.ModelName, "--port", strconv.Itoa(port),
		"--host", "127.0.0.1", "--model-id", t.ModelName,
	})
	if serr != nil {
		return serr
	}
	defer func() {
		if err != nil {
			sp.kill() // never leave a half-started server holding the port
		}
	}()
	report(StageWaiting)
	if err = e.waitForModel(ctx, models, t.ModelName, sp, e.loadTimeout); err != nil {
		if tail := e.mtplxProc.logTail(512); tail != "" && !errors.Is(err, context.Canceled) {
			err = fmt.Errorf("%w; log tail: %s", err, strings.TrimSpace(tail))
		}
		return err
	}
	report(StageWarming)
	return e.warmup(ctx, origin+"/v1/chat/completions", t.ModelName, models, 120*time.Second)
}

func (mtplxBackend) stop(ctx context.Context, e *env, cfg *config.Config) error {
	bin, lerr := e.lookPath("mtplx")
	if lerr != nil {
		return &BinaryMissingError{Binary: "mtplx"}
	}
	_, models, port := mtplxEndpoint(cfg)
	out, runErr := e.run(ctx, bin, "stop", "--port", strconv.Itoa(port), "--grace-seconds", "10")
	closed, cerr := e.portClosedWithin(ctx, models, e.stopTimeout)
	if cerr != nil {
		return cerr
	}
	if closed {
		return nil
	}
	if runErr != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = "mtplx stop failed"
		}
		return errors.New(msg)
	}
	return fmt.Errorf("mtplx still listening on port %d", port)
}
