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

// depsFactory builds the server's dependencies from the host config and the
// LiteLLM upstream; main passes modelserver.DefaultDeps, tests a fake. reload re-reads the host config on each use (config.Load).
type depsFactory func(cfg *config.Config, reload func() (*config.Config, error), upstream *url.URL, upstreamKey string) modelserver.Deps

// loadServeConfig loads the host config and the LiteLLM base URL and key the
// gateway forwards to, from wt's own [litellm] table.
func loadServeConfig() (*config.Config, *url.URL, string, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, "", err
	}
	base, key := cfg.LitellmBaseURL(), cfg.LitellmAPIKey()
	if base == "" || key == "" {
		return nil, nil, "", errors.New("the LiteLLM proxy is not configured: run `wt litellm set --url URL --api-key KEY`")
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, nil, "", fmt.Errorf("the LiteLLM url %q is not a valid URL: run `wt litellm set --url URL --api-key KEY`", base)
	}
	return cfg, u, key, nil
}

// serveCmd is `wt-model serve`.
func serveCmd(mk depsFactory) *cobra.Command {
	var listen, tokenFile string
	idle := defaultIdleGrace
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve the control API (/control/v1) and the gateway (/v1)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
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
				fmt.Fprintf(cmd.ErrOrStderr(), "wt-model: listening on %s (gateway /v1, control /control/v1)\n", addr)
			})
		},
	}
	cmd.Flags().StringVar(&listen, "listen", "", "address to listen on, host:port")
	cmd.Flags().StringVar(&tokenFile, "token-file", "", "file holding the shared bearer token (mode 0600); required on every address")
	cmd.Flags().DurationVar(&idle, "idle-grace", defaultIdleGrace, "how long an unused model survives before auto-stop (must be > 0)")
	_ = cmd.MarkFlagRequired("listen")
	_ = cmd.MarkFlagRequired("token-file")
	return cmd
}
