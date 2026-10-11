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

func (mtplxBackend) tenancy() Tenancy { return Exclusive }

// mtplxStartEndpoint returns the origin, /v1/models URL and port a start
// serves mtplx at. All three come from one resolution so the port passed to
// `mtplx serve` cannot differ from the port the wait polls. A registry origin
// with no port, or one that does not parse, is a *MtplxAddressError: the
// server a start would spawn is not at that address.
func mtplxStartEndpoint(cfg *config.Config) (origin, modelsURL string, port int, err error) {
	origin, port, err = localmodels.FamilyOriginPort(cfg, "mtplx")
	if err != nil {
		asRead := localmodels.FamilyOrigin(cfg, "mtplx")
		return "", "", 0, &MtplxAddressError{Origin: asRead}
	}
	return origin, origin + "/v1/models", port, nil
}

// mtplxEndpoint is mtplxStartEndpoint for a stop and for identifying the
// server's process, which have no url to refuse: for a registry origin a
// start refuses they look where wt serves mtplx when the registry says
// nothing. The fallback goes through the same resolver with no registry
// rather than to a local constant, so the port still has exactly one source.
func mtplxEndpoint(cfg *config.Config) (origin, modelsURL string, port int) {
	origin, modelsURL, port, err := mtplxStartEndpoint(cfg)
	if err != nil {
		origin, modelsURL, port, _ = mtplxStartEndpoint(&config.Config{})
	}
	return origin, modelsURL, port
}

func (mtplxBackend) start(ctx context.Context, e *env, cfg *config.Config, t Target, report func(Stage)) (err error) {
	bin, lerr := e.lookPath("mtplx")
	if lerr != nil {
		return &BinaryMissingError{Binary: "mtplx"}
	}
	origin, models, port, aerr := mtplxStartEndpoint(cfg)
	if aerr != nil {
		return aerr
	}
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
	// What a later `wt stop` identifies this process by while its port is
	// still closed (mtplx_loading.go).
	e.recordStart(sp.cmd.Process.Pid)
	defer func() {
		if err != nil {
			sp.kill() // never leave a half-started server holding the port
		}
	}()
	report(StageWaiting)
	if err = e.waitForModel(ctx, models, t.ModelName, sp, e.loadTimeout); err != nil {
		if tail := e.mtplxProc.logTailLines(512); tail != "" && !errors.Is(err, context.Canceled) {
			err = fmt.Errorf("%w; log tail: %s", err, strings.TrimSpace(tail))
		}
		return err
	}
	report(StageWarming)
	return e.warmup(ctx, origin+"/v1/chat/completions", t.ModelName, models, "", 120*time.Second)
}

func (mtplxBackend) stop(ctx context.Context, e *env, cfg *config.Config) error {
	bin, lerr := e.lookPath("mtplx")
	if lerr != nil {
		return &BinaryMissingError{Binary: "mtplx"}
	}
	_, models, port := mtplxEndpoint(cfg)
	// What the pidfile names now, while the server is still there to be
	// identified: only that process's pidfile is removed below.
	named, _ := e.loadingMtplx(cfg)
	out, runErr := e.run(ctx, bin, "stop", "--port", strconv.Itoa(port), "--grace-seconds", "10")
	closed, cerr := e.portClosedWithin(ctx, models, e.stopTimeout)
	if cerr != nil {
		return cerr
	}
	if closed {
		e.forgetStopped(ctx, named)
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

// stopModel: mtplx is single-model-per-process, so stopping its occupant is
// stopping the process.
func (b mtplxBackend) stopModel(ctx context.Context, e *env, cfg *config.Config, _ string) error {
	return b.stop(ctx, e, cfg)
}
