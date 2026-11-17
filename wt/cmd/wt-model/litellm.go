// `wt-model litellm` — the set|status twins of `wt litellm`'s proxy pair
// commands. The pair wt-model's gateway forwards to LiteLLM with lives in the
// wt-model config home: `set` is its one writer (config.SetProxy) and
// `status` resolves it home-first against the legacy [litellm] fallback
// (config.ResolveProxy). Command definitions are per-binary (each help names
// its own binary); the documents are shared config code (WriteProxyStatus,
// MaskKey, ProxyFallbackNote), so the twins cannot diverge.
package main

import (
	"errors"
	"fmt"
	"io"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/spf13/cobra"
)

// litellmCmd is `wt-model litellm`: set and show the LiteLLM proxy url and
// key.
func litellmCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "litellm",
		Short: "Manage the LiteLLM proxy url and key",
	}
	var statusJSON bool
	statusC := &cobra.Command{
		Use:   "status",
		Short: "Show the LiteLLM proxy url and whether its api key is set (the key is never printed)",
		Long: `Show the LiteLLM proxy pair wt-model forwards to LiteLLM with: the base
URL and whether an api key is set. The key itself is never printed. The pair
is resolved from the wt-model config home (~/.config/wt-model/config.toml and
its key file), with the legacy [litellm] table of config.toml a read-only
fallback when the home holds no [litellm] table; a fallback run prints one
note line on stderr.

Flags:
  --json   print one JSON document (docs/contracts/litellm-cli.sample.json)

Exit codes:
  0  done
  1  any failure`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runLitellmStatus(cmd.OutOrStdout(), cmd.ErrOrStderr(), statusJSON)
		},
	}
	statusC.Flags().BoolVar(&statusJSON, "json", false, "print one JSON document (docs/contracts/litellm-cli.sample.json)")
	var setURL, setKey string
	setC := &cobra.Command{
		Use:   "set",
		Short: "Set the LiteLLM proxy url and/or api key",
		Long: `Set the LiteLLM proxy pair wt-model forwards to LiteLLM with. This is the
setup command other messages name:

	'wt-model litellm set --url URL --api-key KEY'

The url is written to ~/.config/wt-model/config.toml and the api key to the
0600 file its key_file names (litellm.key by default); the legacy [litellm]
table of config.toml is never written. A flag left out keeps its current
value; --api-key "" clears the key without deleting the file.

Flags:
  --url URL        set the proxy base URL
  --api-key KEY    set the proxy api key

Exit codes:
  0  done
  1  any failure`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var u, k *string
			if cmd.Flags().Changed("url") {
				u = &setURL
			}
			if cmd.Flags().Changed("api-key") {
				k = &setKey
			}
			if u == nil && k == nil {
				return errors.New("nothing to set: pass --url and/or --api-key")
			}
			return runLitellmSet(cmd.OutOrStdout(), u, k)
		},
	}
	setC.Flags().StringVar(&setURL, "url", "", "LiteLLM proxy base URL")
	setC.Flags().StringVar(&setKey, "api-key", "", "LiteLLM proxy API key")
	c.AddCommand(statusC, setC)
	return c
}

// runLitellmStatus is `wt-model litellm status`: the shared status body
// (config.ShowProxyStatus) — the pair as every reader resolves it, never the
// key itself, with the fallback note on errOut when the legacy table supplied
// it (ruling 7).
func runLitellmStatus(out, errOut io.Writer, asJSON bool) error {
	return config.ShowProxyStatus(out, errOut, asJSON)
}

// runLitellmSet is `wt-model litellm set`: the one writer of the wt-model
// home (SetProxy), a nil field leaving that field unchanged. No note here:
// after a set the home is authoritative.
func runLitellmSet(out io.Writer, url, key *string) error {
	if err := config.SetProxy(url, key); err != nil {
		return err
	}
	fmt.Fprintln(out, "litellm: updated")
	return nil
}
