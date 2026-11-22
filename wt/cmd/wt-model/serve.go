package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelserver"
	"github.com/ohanaverse/local-ai-setup/wt/internal/servestate"
	"github.com/spf13/cobra"
)

// defaultIdleGrace is how long an unused model survives before auto-stop.
const defaultIdleGrace = 10 * time.Minute

// announceFormat is the one line serve prints on stderr once it is listening;
// %s is the bound address (ln.Addr(), so an ephemeral port reads as the port
// the kernel gave). It is the only machine-readable startup signal (S1).
const announceFormat = "wt-model: listening on %s (gateway /v1, control /control/v1)\n"

// announceLine is announceFormat for addr.
func announceLine(addr string) string { return fmt.Sprintf(announceFormat, addr) }

// depsFactory builds the server's dependencies from the host config and the
// LiteLLM upstream; main passes modelserver.DefaultDeps, tests a fake. reload re-reads the host config on each use (config.Load).
type depsFactory func(cfg *config.Config, reload func() (*config.Config, error), upstream *url.URL, upstreamKey string) modelserver.Deps

// loadServeConfig loads the host config and the LiteLLM base URL and key the
// gateway forwards to LiteLLM with, as Load's merge resolves them (the
// wt-model config home first, config.toml's legacy [litellm] table the
// read-only fallback).
func loadServeConfig() (*config.Config, *url.URL, string, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, "", config.ConfigError(err)
	}
	// The pair is what serve forwards with: a home Load could not resolve
	// (a loose key file, an unparseable home) refuses the start here, the one
	// place it matters, with the load's own wording and hint.
	if perr := cfg.ProxyErr(); perr != nil {
		return nil, nil, "", config.ConfigError(perr)
	}
	base, key := cfg.LitellmBaseURL(), cfg.LitellmAPIKey()
	if base == "" || key == "" {
		return nil, nil, "", errors.New("the LiteLLM proxy is not configured: run 'wt-model litellm set --url URL --api-key KEY'")
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme == "" || u.Host == "" {
		// A url with an '@' is never quoted (#640): the placeholder stands in.
		shown := strconv.Quote(base)
		if config.ProxyURLHasAt(base) {
			shown = config.ProxyURLHidden
		}
		return nil, nil, "", fmt.Errorf("the LiteLLM url %s is not a valid URL: run 'wt-model litellm set --url URL --api-key KEY'", shown)
	}
	return cfg, u, key, nil
}

// recordNoteFormat is the one stderr note when the runtime record cannot be
// written; serve goes on serving. %s is the write error.
const recordNoteFormat = "wt-model: note: could not write the runtime record (%s) — host commands cannot see this server's client sessions\n"

// serveCmd is `wt-model serve`. rt.Record is where it keeps its runtime
// record while it runs.
func serveCmd(mk depsFactory, rt serveRuntime) *cobra.Command {
	var listen, tokenFile string
	idle := defaultIdleGrace
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the model server",
		Long: `Run the model server: the control API under /control/v1 starts and stops
wt's local models for agent sessions that run elsewhere, and the gateway under
/v1 forwards model requests to the LiteLLM proxy configured by
'wt-model litellm set --url URL --api-key KEY'.
Every request needs the shared bearer token, on every address.

Flags:
  --listen ADDR       address to listen on, host:port (required)
  --token-file PATH   file holding the shared bearer token, mode 0600 (required)
  --idle-grace DUR    how long an unused model survives before auto-stop
                      (default 10m0s, must be greater than zero)

Once listening it prints one line on stderr naming the bound address, then
reconciles the LiteLLM routes once, as 'wt-model litellm sync' does
(local routes through the proxy's model API when the pair is set): a proxy
that restarted or lost its database gets its routes back. What that sync
changes or warns about is printed on stderr; a failure there is a warning
and serving goes on.
While it runs it keeps a runtime record, serve.json in the wt-model home
(pid, address, token file path; never the token), and removes it when it
stops cleanly.

Exit codes:
  0  stopped cleanly on SIGINT or SIGTERM
  1  any failure (a bad flag, an unreadable token file, an unconfigured
     LiteLLM proxy, an address that cannot be bound)`,
		Example: `  wt-model serve --listen 127.0.0.1:8787 --token-file ~/.config/wt-model/server.token`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if listen == "" || tokenFile == "" {
				return errors.New("--listen and --token-file are required: wt-model serve --listen HOST:PORT --token-file PATH")
			}
			if idle <= 0 {
				return fmt.Errorf("--idle-grace must be greater than zero, got %s", idle)
			}
			token, err := modelserver.LoadToken(tokenFile)
			if err != nil {
				return err
			}
			cfg, upstream, key, err := loadServeConfig()
			if err != nil {
				return err
			}
			// A pair the legacy [litellm] fallback supplied prints the one
			// fallback note before anything listens (ruling 7): the fallback
			// is read-only now, and the note names the writer. The source is
			// the one from the load above (cfg.ProxySource) — the pair being
			// forwarded and the pair judged for the note are the same
			// resolution, never two that could disagree (fix round 2,
			// Finding 4).
			if cfg.ProxySource() == config.ProxySourceLegacy {
				fmt.Fprintln(cmd.ErrOrStderr(), config.ProxyFallbackNote())
			}
			srv, err := modelserver.NewServer(modelserver.Options{
				Listen: listen, Token: token, IdleGrace: idle,
				Upstream: upstream, UpstreamKey: key,
			}, mk(cfg, config.Load, upstream, key))
			if err != nil {
				return err
			}
			// The record names the token file by an absolute path, so a host
			// command run from another directory finds it; never the token.
			abs, aerr := filepath.Abs(tokenFile)
			if aerr != nil {
				abs = tokenFile
			}
			// Remove deletes the record only while it names this pid: one a
			// later serve wrote over it stays.
			defer func() { _ = rt.Record.Remove(os.Getpid()) }()
			// The start reconcile (rt.StartSync) runs once the server is
			// listening, off the serving path: it waits for the proxy (up to
			// 90 s at login) and lists its database, and a session must be
			// able to reach the server meanwhile. serve waits for it before
			// returning, so a stop never cuts a config.yaml write short; every
			// wait in it ends with the serve's context, and it restarts no
			// proxy itself (the restart it owes is the tracked bounce the
			// shutdown's route-settle step waits for, like a job's).
			var startSync sync.WaitGroup
			defer startSync.Wait()
			return srv.Serve(cmd.Context(), func(addr string) {
				// The record lands before the announce line, so whoever saw
				// the line can read the record.
				if err := rt.Record.Write(servestate.Record{
					PID: os.Getpid(), Listen: addr, TokenFile: abs, Started: time.Now().UTC(),
				}); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), recordNoteFormat, err)
				}
				fmt.Fprint(cmd.ErrOrStderr(), announceLine(addr))
				// After the announce line, so it stays the first line a
				// script reads (S1).
				if rt.StartSync != nil {
					startSync.Add(1)
					go func() {
						defer startSync.Done()
						rt.StartSync(cmd.Context(), cfg, cmd.ErrOrStderr())
					}()
				}
			})
		},
	}
	cmd.Flags().StringVar(&listen, "listen", "", "address to listen on, host:port")
	cmd.Flags().StringVar(&tokenFile, "token-file", "", "file holding the shared bearer token (mode 0600); required on every address")
	cmd.Flags().DurationVar(&idle, "idle-grace", defaultIdleGrace, "how long an unused model survives before auto-stop (must be > 0)")
	return cmd
}

// serveStartSync is the route sync `wt-model serve` runs once as it starts
// (#598): a proxy restarted, or one whose database was wiped, has lost every
// database route, and nothing else would put them back until the next start
// or stop. It is startSync with the real probe, readiness wait and restart.
func serveStartSync(probe litellm.Probe) func(ctx context.Context, cfg *config.Config, errOut io.Writer) {
	return startSync{
		probe:        probe,
		waitReady:    litellm.WaitReady,
		bounce:       lifecycle.BounceProxyTo,
		readyTimeout: startSyncReadyTimeout,
		listTimeout:  startSyncListTimeout,
	}.run
}

// startSyncReadyTimeout bounds the start sync's wait for the proxy to answer:
// at login both LaunchAgents start together, and the proxy (Python, then its
// database) answers well after serve listens. startSyncListTimeout bounds
// the listing that proves its database answers too.
const (
	startSyncReadyTimeout = 90 * time.Second
	startSyncListTimeout  = 15 * time.Second
)

// startSync is the start reconcile's parts, each a per-value seam: probe is
// the local inventory, waitReady the wait for the proxy's liveness endpoint
// (litellm.WaitReady), bounce the restart owed when the sync changed
// config.yaml (lifecycle.BounceProxyTo: asynchronous, one restart at a time,
// waited for by WaitPendingRoutes at exit).
type startSync struct {
	probe                     litellm.Probe
	waitReady                 func(ctx context.Context, baseURL string, timeout time.Duration) error
	bounce                    func(ctx context.Context, out io.Writer, cfg *config.Config)
	readyTimeout, listTimeout time.Duration
}

// startSyncSkipped is the one warning of a start sync that did not run
// because it could not see the database; %s says why.
const startSyncSkipped = "wt-model: warning: LiteLLM routes not synced at start: %s; nothing was changed (run 'wt-model litellm sync' once the proxy answers)\n"

// run is the start reconcile: `wt-model litellm sync` (litellm.SyncInputs
// over probe, then litellm.Sync with the proxy pair's model API, the same
// Options), reporting on errOut.
//
// It runs only when it can see the database: it first waits for the proxy
// to answer (readyTimeout) and then lists its database (listTimeout). A
// proxy that is still starting, or a database that cannot be listed, skips
// the whole sync with one warning and writes nothing: a sync that cannot see
// the database would put every local route back in config.yaml and restart a
// proxy that is still coming up.
//
// The write never restarts the proxy itself (NoRestart): a sync that changed
// config.yaml (the first-time setting, the migration, a cloud route) hands
// the restart to bounce, so it runs one at a time with the route jobs'
// restarts and the serve's stop never waits on it here. Every wait honors
// ctx. A failure is one warning line and never stops serving; no config.yaml
// at all (LiteLLM not set up here) is silent.
func (s startSync) run(ctx context.Context, cfg *config.Config, errOut io.Writer) {
	if _, err := os.Stat(litellm.DefaultPath()); errors.Is(err, os.ErrNotExist) {
		return
	}
	admin := adminFromPair(cfg.LitellmBaseURL(), cfg.LitellmAPIKey())
	if admin == nil {
		fmt.Fprintf(errOut, startSyncSkipped, "the LiteLLM proxy pair is not set")
		return
	}
	if err := s.waitReady(ctx, admin.BaseURL, s.readyTimeout); err != nil {
		fmt.Fprintf(errOut, startSyncSkipped, fmt.Sprintf("the proxy did not answer within %s (%v)", s.readyTimeout, err))
		return
	}
	lctx, cancel := context.WithTimeout(ctx, s.listTimeout)
	_, lerr := admin.List(lctx)
	cancel()
	if lerr != nil {
		fmt.Fprintf(errOut, startSyncSkipped, "its database routes could not be listed ("+strings.Join(strings.Fields(lerr.Error()), " ")+")")
		return
	}
	desired, o, probeWarns := litellm.SyncInputs(cfg, s.probe)
	o.Admin, o.Ctx, o.NoRestart = admin, ctx, true
	res, err := litellm.Sync(cfg, desired, o)
	if errors.Is(err, litellm.ErrMissing) {
		return
	}
	if err != nil {
		fmt.Fprintf(errOut, "wt-model: warning: LiteLLM routes not synced at start: %v\n", err)
		return
	}
	for _, oc := range res.Outcomes {
		if oc.Err != nil {
			fmt.Fprintf(errOut, "wt-model: warning: start route sync: %s: %v\n", oc.ID, oc.Err)
			continue
		}
		fmt.Fprintf(errOut, "wt-model: start route sync: %s: %s\n", oc.ID, oc.Action)
	}
	for _, w := range append(res.Warnings, probeWarns...) {
		fmt.Fprintf(errOut, "wt-model: warning: start route sync: %s\n", w)
	}
	if res.Changed {
		s.bounce(ctx, errOut, cfg)
	}
}
