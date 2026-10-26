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
	"github.com/ohanaverse/local-ai-setup/wt/internal/provider"
)

// processServer is the backend of every process family (provider.ProcessFor):
// wt spawns the family's server as a process per model on one port, so
// starting another model replaces the current one. It holds only the family
// name and reads the spec through processSpecFor on use, which keeps it
// comparable: the registry of backends is checked with maps.Equal.
type processServer struct{ family string }

func (b processServer) tenancy() Tenancy {
	f, _ := familySpecFor(b.family)
	return f.Tenancy
}

// familyOriginPort is the one seam through which serverStartEndpoint, and so
// serverEndpoint, resolve a family's address; a test swaps it to give a
// synthetic family, which has no provider-table row, a real port.
var familyOriginPort = localmodels.FamilyOriginPort

// serverStartEndpoint returns the origin, /v1/models URL and port a start
// serves family at. All three come from one resolution so the port passed to
// the spawned server cannot differ from the port the wait polls. A registry
// origin with no port, or one that does not parse, is a *PortlessAddressError:
// the server a start would spawn is not at that address.
func serverStartEndpoint(cfg *config.Config, family string) (origin, modelsURL string, port int, err error) {
	origin, port, err = familyOriginPort(cfg, family)
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

// expandArgs is provider.ExpandArgs, for the stop command's template.
func expandArgs(tmpl []string, model string, port int) []string {
	return provider.ExpandArgs(tmpl, model, port)
}

// serveArgsFor is the registry row's serve_args for the model t starts: the
// row with t's id, else the one of the family that names the same model. nil
// when there is no row (a discovered model) or it carries none. A row whose
// serve_args cannot be used is a *ServeArgsError, whatever the family: a
// provider that takes none is named too, not ignored.
func serveArgsFor(cfg *config.Config, family string, t Target) ([]string, error) {
	i := config.IndexModelByID(cfg.Models, t.ModelID)
	if i < 0 {
		i = slices.IndexFunc(cfg.Models, func(m config.Model) bool {
			return provider.FamilyOr(m.ProviderID) == family && m.ModelName == t.ModelName
		})
	}
	if i < 0 {
		return nil, nil
	}
	m := cfg.Models[i]
	if problems := m.ServeArgsProblems(); len(problems) > 0 {
		return nil, &ServeArgsError{ModelID: m.ID, Problems: problems}
	}
	return m.ServeArgs.Args, nil
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
	serveArgs, serr := serveArgsFor(cfg, b.family, t)
	if serr != nil {
		return serr
	}
	origin, models, port, aerr := serverStartEndpoint(cfg, b.family)
	if aerr != nil {
		return aerr
	}
	// The key the registry names for this server, resolved once per start
	// and sent with every probe and the warmup; "" (no header) when the
	// family's provider row has no secret_ref. A secret_ref that cannot be
	// read ends the start here, before the port check and the spawn: the
	// server would refuse every keyless probe and the start would only time
	// out, never naming the key.
	key, kerr := localmodels.FamilyAPIKeyErr(cfg, b.family)
	if kerr != nil {
		return kerr
	}
	report(StageStarting)
	closed, cerr := e.portClosedWithin(ctx, models, key, e.prebindTimeout)
	if cerr != nil {
		return cerr
	}
	if !closed {
		return &PortBusyError{Port: port}
	}
	sp, spawnErr := e.spawn(proc, bin, spec.SpawnArgs(t.ModelName, port, serveArgs))
	if spawnErr != nil {
		return spawnErr
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
	if err = e.waitReady(ctx, b.family, sp, bin, models, t.ModelName, key, e.loadTimeout); err != nil {
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
	if _, machine := machineScoped(b.family); machine {
		return b.stopMachineInstance(ctx, e, cfg, spec, bin, port)
	}
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
		return stopCommandError(spec, out)
	}
	return fmt.Errorf("%s still listening on port %d", spec.Binary, port)
}

// machineScoped reports whether family's server is one instance on the whole
// machine (provider.ScopeMachine) that wt finds through a readiness command:
// only such a command can see an instance on a port the registry does not
// name. TestTableInvariants holds every ScopeMachine row to that.
func machineScoped(family string) (provider.ProcessSpec, bool) {
	f, ok := familySpecFor(family)
	if !ok || f.Scope != provider.ScopeMachine {
		return provider.ProcessSpec{}, false
	}
	spec, ok := processSpecFor(family)
	if !ok || spec.Ready.Kind != provider.ReadyCommandJSON {
		return provider.ProcessSpec{}, false
	}
	return spec, true
}

// foreignInstance asks a ScopeMachine family's readiness command whether the
// family's one instance on this machine is running, on whatever port, and
// whoever started it: running is true when the command says ready, and
// st.Model is the model it serves ("" when the command names none) and
// st.Endpoint the URL it says it listens at ("" when the spec reads no
// EndpointPath or the command names none). A family
// of any other scope (mtplx) runs nothing and reports nothing running.
func (e *env) foreignInstance(ctx context.Context, family string, bin, key string) (st readyState, running bool) {
	spec, ok := machineScoped(family)
	if !ok {
		return readyState{}, false
	}
	st, err := e.readyNow(ctx, spec, bin, "", "", key)
	if err != nil || !st.Ready {
		return readyState{}, false
	}
	return st, true
}

// stopMachineInstance stops a ScopeMachine family's instance with the spec's
// stop command, which stops it whatever port it listens on, and waits up to
// e.stopTimeout for the readiness command to say it is no longer ready. No
// pid is signalled: the instance may not be one wt started. The pidfile wt
// keeps is removed only for the process it named before the stop, once that
// process is gone (forgetStopped).
func (b processServer) stopMachineInstance(ctx context.Context, e *env, cfg *config.Config, spec provider.ProcessSpec, bin string, port int) error {
	named, _ := e.loadingServer(cfg, b.family)
	key := localmodels.FamilyAPIKey(cfg, b.family)
	out, runErr := e.run(ctx, bin, expandArgs(spec.Stop, "", port)...)
	deadline := time.Now().Add(e.stopTimeout)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		st, _ := e.foreignInstance(ctx, b.family, bin, key)
		// "Not ready" is also what an instance still loading answers, so the
		// stop is done only once the process wt named is gone too.
		alive := false
		if named.pid > 0 {
			_, l := e.loadingServer(cfg, b.family)
			alive = l.PID == named.pid
		}
		if !st.Ready && !alive {
			// A readiness command cut short by the cancelled context reads as
			// "not ready" too; that is not the instance having stopped.
			if err := ctx.Err(); err != nil {
				return err
			}
			e.forgetStopped(ctx, b.family, named)
			return nil
		}
		if !time.Now().Before(deadline) {
			break
		}
		if err := e.sleep(ctx, e.pollInterval); err != nil {
			return err
		}
	}
	if runErr != nil {
		return stopCommandError(spec, out)
	}
	if named.pid > 0 {
		if _, l := e.loadingServer(cfg, b.family); l.PID == named.pid {
			return fmt.Errorf("%s (pid %d) is still running after `%s %s`", spec.Binary, named.pid, spec.Binary, strings.Join(spec.Stop, " "))
		}
	}
	return fmt.Errorf("%s still reports an instance ready after `%s %s`", spec.Binary, spec.Binary, strings.Join(spec.Stop, " "))
}

// stopCommandError is the error for a spec's stop command that failed and left
// the server up: the command's own output, else a line naming the binary.
func stopCommandError(spec provider.ProcessSpec, out []byte) error {
	msg := strings.TrimSpace(string(out))
	if msg == "" {
		msg = spec.Binary + " stop failed"
	}
	return errors.New(msg)
}

// stopModel: a process server serves one model per process, so stopping its
// occupant is stopping the process.
func (b processServer) stopModel(ctx context.Context, e *env, cfg *config.Config, _ string) error {
	return b.stop(ctx, e, cfg)
}
