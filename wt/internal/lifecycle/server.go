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
	"github.com/ohanaverse/local-ai-setup/wt/internal/provider"
)

// processServer is the backend of every process family (provider.ProcessFor):
// wt spawns the family's server as a process per model on one port, so
// starting another model replaces the current one. It holds only the family
// name and reads the spec through processSpecFor on use, which keeps it
// comparable: the registry of backends is checked with maps.Equal.
type processServer struct{ family string }

func (b processServer) tenancy() Tenancy {
	f, _ := provider.FamilyByName(b.family)
	return f.Tenancy
}

// serverStartEndpoint returns the origin, /v1/models URL and port a start
// serves family at. All three come from one resolution so the port passed to
// the spawned server cannot differ from the port the wait polls. A registry
// origin with no port, or one that does not parse, is a *PortlessAddressError:
// the server a start would spawn is not at that address.
func serverStartEndpoint(cfg *config.Config, family string) (origin, modelsURL string, port int, err error) {
	origin, port, err = localmodels.FamilyOriginPort(cfg, family)
	if err != nil {
		asRead := localmodels.FamilyOrigin(cfg, family)
		return "", "", 0, &PortlessAddressError{Provider: family, Origin: asRead}
	}
	return origin, origin + "/v1/models", port, nil
}

// serverEndpoint is serverStartEndpoint for a stop and for identifying the
// server's process, which have no url to refuse: for a registry origin a
// start refuses they look where wt serves the family when the registry says
// nothing. The fallback goes through the same resolver with no registry
// rather than to a local constant, so the port still has exactly one source.
func serverEndpoint(cfg *config.Config, family string) (origin, modelsURL string, port int) {
	origin, modelsURL, port, err := serverStartEndpoint(cfg, family)
	if err != nil {
		origin, modelsURL, port, _ = serverStartEndpoint(&config.Config{}, family)
	}
	return origin, modelsURL, port
}

// expandArgs is a spec's argv template with {model} and {port} replaced in
// every element. The template itself is not changed.
func expandArgs(tmpl []string, model string, port int) []string {
	r := strings.NewReplacer("{model}", model, "{port}", strconv.Itoa(port))
	out := make([]string, len(tmpl))
	for i, a := range tmpl {
		out[i] = r.Replace(a)
	}
	return out
}

// spec is the family's process spec; a family with none has no backend.
func (b processServer) spec() (provider.ProcessSpec, error) {
	spec, ok := processSpecFor(b.family)
	if !ok {
		return provider.ProcessSpec{}, &UnsupportedError{ProviderID: b.family}
	}
	return spec, nil
}

func (b processServer) start(ctx context.Context, e *env, cfg *config.Config, t Target, report func(Stage)) (err error) {
	spec, err := b.spec()
	if err != nil {
		return err
	}
	proc, ok := e.serverProc[b.family]
	if !ok {
		return fmt.Errorf("no pidfile is configured for %s", b.family)
	}
	bin, lerr := e.lookPath(spec.Binary)
	if lerr != nil {
		return &BinaryMissingError{Binary: spec.Binary}
	}
	origin, models, port, aerr := serverStartEndpoint(cfg, b.family)
	if aerr != nil {
		return aerr
	}
	// The key the registry names for this server, resolved once per start
	// and sent with every probe and the warmup; "" (no header) when the
	// family's provider row has no secret_ref.
	key := localmodels.FamilyAPIKey(cfg, b.family)
	report(StageStarting)
	closed, cerr := e.portClosedWithin(ctx, models, key, e.prebindTimeout)
	if cerr != nil {
		return cerr
	}
	if !closed {
		return &PortBusyError{Port: port}
	}
	sp, serr := proc.spawn(bin, expandArgs(spec.Spawn, t.ModelName, port))
	if serr != nil {
		return serr
	}
	// What a later `wt stop` identifies this process by while its port is
	// still closed (server_loading.go).
	e.recordStart(b.family, sp.cmd.Process.Pid)
	defer func() {
		if err != nil {
			sp.kill() // never leave a half-started server holding the port
		}
	}()
	report(StageWaiting)
	if err = e.waitForModel(ctx, models, t.ModelName, sp, key, e.loadTimeout); err != nil {
		if tail := proc.logTailLines(512); tail != "" && !errors.Is(err, context.Canceled) {
			err = fmt.Errorf("%w; log tail: %s", err, strings.TrimSpace(tail))
		}
		return err
	}
	report(StageWarming)
	return e.warmup(ctx, origin+"/v1/chat/completions", t.ModelName, models, key, 120*time.Second)
}

func (b processServer) stop(ctx context.Context, e *env, cfg *config.Config) error {
	spec, err := b.spec()
	if err != nil {
		return err
	}
	bin, lerr := e.lookPath(spec.Binary)
	if lerr != nil {
		return &BinaryMissingError{Binary: spec.Binary}
	}
	_, models, port := serverEndpoint(cfg, b.family)
	// What the pidfile names now, while the server is still there to be
	// identified: only that process's pidfile is removed below.
	named, _ := e.loadingServer(cfg, b.family)
	out, runErr := e.run(ctx, bin, expandArgs(spec.Stop, "", port)...)
	closed, cerr := e.portClosedWithin(ctx, models, localmodels.FamilyAPIKey(cfg, b.family), e.stopTimeout)
	if cerr != nil {
		return cerr
	}
	if closed {
		e.forgetStopped(ctx, b.family, named)
		return nil
	}
	if runErr != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = spec.Binary + " stop failed"
		}
		return errors.New(msg)
	}
	return fmt.Errorf("%s still listening on port %d", spec.Binary, port)
}

// stopModel: a process server serves one model per process, so stopping its
// occupant is stopping the process.
func (b processServer) stopModel(ctx context.Context, e *env, cfg *config.Config, _ string) error {
	return b.stop(ctx, e, cfg)
}
