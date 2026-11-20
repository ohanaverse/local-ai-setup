package main

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
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

Once listening it prints one line on stderr naming the bound address.
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
			return srv.Serve(cmd.Context(), func(addr string) {
				// The record lands before the announce line, so whoever saw
				// the line can read the record.
				if err := rt.Record.Write(servestate.Record{
					PID: os.Getpid(), Listen: addr, TokenFile: abs, Started: time.Now().UTC(),
				}); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), recordNoteFormat, err)
				}
				fmt.Fprint(cmd.ErrOrStderr(), announceLine(addr))
			})
		},
	}
	cmd.Flags().StringVar(&listen, "listen", "", "address to listen on, host:port")
	cmd.Flags().StringVar(&tokenFile, "token-file", "", "file holding the shared bearer token (mode 0600); required on every address")
	cmd.Flags().DurationVar(&idle, "idle-grace", defaultIdleGrace, "how long an unused model survives before auto-stop (must be > 0)")
	return cmd
}
