package main

import (
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelserver"
	"github.com/spf13/cobra"
)

// defaultIdleGrace is how long an unused model survives before auto-stop.
const defaultIdleGrace = 10 * time.Minute

// announceFormat is the one line serve prints on stderr once it is listening;
// %s is the bound address (ln.Addr(), so an ephemeral port reads as the port
// the kernel gave). It is the only machine-readable startup signal (S6).
const announceFormat = "wt-model: listening on %s (gateway /v1, control /control/v1)\n"

// announceLine is announceFormat for addr.
func announceLine(addr string) string { return fmt.Sprintf(announceFormat, addr) }

// depsFactory builds the server's dependencies from the host config and the
// LiteLLM upstream; main passes modelserver.DefaultDeps, tests a fake. reload re-reads the host config on each use (config.Load).
type depsFactory func(cfg *config.Config, reload func() (*config.Config, error), upstream *url.URL, upstreamKey string) modelserver.Deps

// loadServeConfig loads the host config and the LiteLLM base URL and key the
// gateway forwards to, from wt's own [litellm] table.
func loadServeConfig() (*config.Config, *url.URL, string, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, "", config.ConfigError(err)
	}
	base, key := cfg.LitellmBaseURL(), cfg.LitellmAPIKey()
	if base == "" || key == "" {
		return nil, nil, "", errors.New("the LiteLLM proxy is not configured: run 'wt litellm set --url URL --api-key KEY'")
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, nil, "", fmt.Errorf("the LiteLLM url %q is not a valid URL: run 'wt litellm set --url URL --api-key KEY'", base)
	}
	return cfg, u, key, nil
}

// serveCmd is `wt-model serve`.
func serveCmd(mk depsFactory) *cobra.Command {
	var listen, tokenFile string
	idle := defaultIdleGrace
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the model server",
		Long: `Run the model server: the control API under /control/v1 starts and stops
wt's local models for agent sessions that run elsewhere, and the gateway under
/v1 forwards model requests to the LiteLLM proxy configured by 'wt litellm set'.
Every request needs the shared bearer token, on every address.

Flags:
  --listen ADDR       address to listen on, host:port (required)
  --token-file PATH   file holding the shared bearer token, mode 0600 (required)
  --idle-grace DUR    how long an unused model survives before auto-stop
                      (default 10m0s, must be greater than zero)

Once listening it prints one line on stderr naming the bound address.

Exit codes:
  0  stopped cleanly on SIGINT or SIGTERM
  1  any failure (a bad flag, an unreadable token file, an unconfigured
     LiteLLM proxy, an address that cannot be bound)`,
		Example: `  wt-model serve --listen 127.0.0.1:8787 --token-file ~/.config/wt-model/token`,
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
			srv, err := modelserver.NewServer(modelserver.Options{
				Listen: listen, Token: token, IdleGrace: idle,
				Upstream: upstream, UpstreamKey: key,
			}, mk(cfg, config.Load, upstream, key))
			if err != nil {
				return err
			}
			return srv.Serve(cmd.Context(), func(addr string) {
				fmt.Fprint(cmd.ErrOrStderr(), announceLine(addr))
			})
		},
	}
	cmd.Flags().StringVar(&listen, "listen", "", "address to listen on, host:port")
	cmd.Flags().StringVar(&tokenFile, "token-file", "", "file holding the shared bearer token (mode 0600); required on every address")
	cmd.Flags().DurationVar(&idle, "idle-grace", defaultIdleGrace, "how long an unused model survives before auto-stop (must be > 0)")
	return cmd
}
