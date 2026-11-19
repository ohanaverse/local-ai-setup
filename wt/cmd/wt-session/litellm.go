// wt litellm — the proxy pair commands (status, set) and the hidden stubs of
// retired and removed subcommands. The route commands (sync, list,
// providers) and the proxy restart moved to `wt-model litellm` in phase 4
// stage 2; their wt names are pointer stubs (moved.go).
package main

import (
	"errors"
	"fmt"
	"io"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/exitcode"
	"github.com/spf13/cobra"
)

// runLitellmStatus is `wt litellm status`: the shared status body
// (config.ShowProxyStatus) — the pair as every reader resolves it, never the
// key itself, with the fallback note on errOut when the legacy table supplied
// it (ruling 7).
func runLitellmStatus(out, errOut io.Writer, asJSON bool) error {
	return config.ShowProxyStatus(out, errOut, asJSON)
}

// runLitellmSet is `wt litellm set`: the one writer of the wt-model home
// (SetProxy), a nil field leaving that field unchanged. No note here: after a
// set the home is authoritative.
func runLitellmSet(out io.Writer, url, key *string) error {
	if err := config.SetProxy(url, key); err != nil {
		return err
	}
	fmt.Fprintln(out, "litellm: updated")
	return nil
}

// litellmCmd is `wt litellm`: the proxy pair's status and set. The route
// commands and the proxy restart moved to `wt-model litellm` (their wt names
// are hidden pointers the root adds); no subcommand here reads the host
// config.
func litellmCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "litellm",
		Short: "Show and set the LiteLLM proxy url and key",
	}
	// status and set carry no config gate (ruling 8): they reach only the
	// proxy pair — the wt-model home and its legacy fallback — so a broken
	// registry or config.toml cannot lock the user out of the one surface
	// that repairs the pair. `set` is the escape hatch (gateless); `status`
	// fails loud on exactly the files its resolution needs (ResolveProxy).
	var statusJSON bool
	statusC := &cobra.Command{
		Use: "status", Short: "Show the LiteLLM proxy url and whether its api key is set (the key is never printed)", Args: cobra.NoArgs, SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runLitellmStatus(cmd.OutOrStdout(), cmd.ErrOrStderr(), statusJSON)
		},
	}
	statusC.Flags().BoolVar(&statusJSON, "json", false, "machine-readable output")
	var setURL, setKey string
	setC := &cobra.Command{
		Use: "set", Short: "Set the LiteLLM proxy url and/or api key", Args: cobra.NoArgs, SilenceUsage: true,
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
	c.AddCommand(
		statusC, setC,
		removedLitellmCmd("expose"), removedLitellmCmd("unexpose"),
		retiredLitellmToggleCmd("on"), retiredLitellmToggleCmd("off"),
	)
	return c
}

// litellmToggleRetired is the date `wt litellm on|off` were retired (style
// guide C5: a hidden stub carries its removal date).
const litellmToggleRetired = "2026-11-15"

// retiredLitellmToggleCmd is the hidden stub `wt litellm on|off` became when
// every session started routing through the model server (Task 18): the
// [litellm] enabled key they flipped is read by nothing, so running either
// changes nothing and says where routing is configured now, exit 1. A stub
// rather than a removal: cobra would print the parent's help for an unknown
// name and exit 0, so `wt litellm on && ...` would "succeed". Flag parsing is
// off so a stray flag reaches the same text.
func retiredLitellmToggleCmd(name string) *cobra.Command {
	return &cobra.Command{
		Use: name, Hidden: true, DisableFlagParsing: true, SilenceUsage: true,
		Annotations: map[string]string{removedCmdAnnotation: litellmToggleRetired},
		RunE: func(*cobra.Command, []string) error {
			return exitcode.New(1, fmt.Errorf("litellm %s is retired — every session routes through the model server; set url and token_file under [model-server] in config.toml; nothing was changed", name), false)
		},
	}
}

// removedLitellmCmd is a hidden stub for a subcommand #179 removed. Without
// it cobra prints the parent's help and exits 0 for the unknown name, so a
// script running `wt litellm expose X && ...` would "succeed" having written
// nothing. Flag parsing is off so legacy flags (--json, --dry-run,
// --skip-ready-gate) reach the same pointer to `wt-model litellm sync`
// instead of "unknown flag".
// The removedCmdAnnotation marks the Hidden as deliberate for
// TestNoWtCommandIsHidden.
func removedLitellmCmd(name string) *cobra.Command {
	return &cobra.Command{
		Use: name, Hidden: true, DisableFlagParsing: true, SilenceUsage: true,
		Annotations: map[string]string{removedCmdAnnotation: "#179"},
		RunE: func(*cobra.Command, []string) error {
			// Mirrors sync's own wording: a pulled ollama model is routed
			// whether or not it is loaded, so "running" alone understates
			// what sync does.
			return fmt.Errorf("`wt litellm %s` was removed (#179): configured cloud models and running (or pulled ollama) local models are routed automatically — run `wt-model litellm sync`", name)
		},
	}
}
