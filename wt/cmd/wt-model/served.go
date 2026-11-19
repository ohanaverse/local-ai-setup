// wt-model served / wt-model warm — what a local provider's server is
// serving now, and loading one model into a running omlx. Both read the
// registry's provider rows (for the address and the key) and change nothing
// else; they moved here from wt, whose old names only point at them.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/provider"
	"github.com/spf13/cobra"
)

// servedProbeTimeout bounds each request `wt-model served` makes — the
// lifecycle engine's probe timeout, since it asks the same question.
const servedProbeTimeout = 5 * time.Second

// servedFamilies are the providers `wt-model served` answers for: the ones
// whose server says what it is serving. Ollama has no such answer — a pulled
// model loads on request — so it is left out rather than answered with its
// pulls.
var servedFamilies = provider.ServedFamilies()

func servedCmd(h *host) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "served <provider>",
		Short: "List the models a local provider's server is serving now",
		Long: "List the model ids a local provider's server (" + strings.Join(servedFamilies, ", ") + ") is serving\n" +
			"now, one per line, as the server names them. This is the probe `wt start`,\n" +
			"`wt stop` and the pickers act on. For omlx it is the models loaded or loading,\n" +
			"not every model it lists; when the server has an API key, wt sends the one\n" +
			"the registry's omlx provider names (auth.secret_ref).\n\n" +
			"Nothing listed and exit 0 means the server answered and serves nothing.\n" +
			"Exit 1 means it gave no usable answer — not running, or it would not say.",
		Example: "  wt-model served omlx\n  wt-model served mtplx --json",
		Args:    cobra.ExactArgs(1),
		// A probe that gets no answer is the expected failure here, not a
		// usage mistake.
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// The load error only: this reads the registry's provider rows
			// and writes nothing, so a validation gap elsewhere in the
			// config must not make the probe unavailable.
			cfg, loadErr := h.hostConfig()
			if loadErr != nil {
				return config.ConfigError(loadErr)
			}
			client := &http.Client{Timeout: servedProbeTimeout}
			return runServed(cmd.OutOrStdout(), cfg, client, args[0], asJSON)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable output")
	return cmd
}

func runServed(out io.Writer, cfg *config.Config, client *http.Client, family string, asJSON bool) error {
	if !slices.Contains(servedFamilies, family) {
		return fmt.Errorf("unknown provider %q: wt-model served answers for %s", family, strings.Join(servedFamilies, ", "))
	}
	ids, err := localmodels.ServedIDs(cfg, client, family)
	if err != nil {
		return fmt.Errorf("%s gave no usable answer: %w", family, err)
	}
	if asJSON {
		if ids == nil {
			ids = []string{}
		}
		return json.NewEncoder(out).Encode(map[string]any{"provider": family, "served": ids})
	}
	for _, id := range ids {
		fmt.Fprintln(out, id)
	}
	return nil
}

func warmCmd(h *host) *cobra.Command {
	return &cobra.Command{
		Use:   "warm <provider> <model>",
		Short: "Load a model into a running omlx server",
		Long: "Load a model into an omlx server that is already running, by sending it one\n" +
			"short request. <model> is the name omlx serves it under, or a repo id whose\n" +
			"last segment is that name. When the server has an API key, wt sends the one\n" +
			"the registry's omlx provider names (auth.secret_ref).\n\n" +
			"Nothing is started, stopped or routed: this is the warmup step of `wt start`\n" +
			"by itself, which llmbench's omlx backend asks for when omlx refuses its\n" +
			"keyless request. To start a model, use `wt start`.",
		Example: "  wt-model warm omlx Qwen3.8-27B-4bit",
		Args:    cobra.ExactArgs(2),
		// A server that refuses or never loads the model is the expected
		// failure here, not a usage mistake.
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// The load error only, as for `wt-model served`: this reads the
			// registry's provider rows and writes nothing.
			cfg, loadErr := h.hostConfig()
			if loadErr != nil {
				return config.ConfigError(loadErr)
			}
			// cmd.Context() is main's: SIGINT and SIGTERM cancel it.
			return runWarm(cmd.Context(), cmd.OutOrStdout(), cfg, args[0], args[1])
		},
	}
}

func runWarm(ctx context.Context, out io.Writer, cfg *config.Config, provider, model string) error {
	if localmodels.Family(provider) != "omlx" {
		return fmt.Errorf("unknown provider %q: wt-model warm loads models into omlx only", provider)
	}
	if err := lifecycle.Warm(ctx, cfg, provider, model); err != nil {
		return err
	}
	fmt.Fprintf(out, "%s is loaded on %s\n", model, provider)
	return nil
}
