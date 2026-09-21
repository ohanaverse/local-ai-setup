// wt litellm — the primitives that manage LiteLLM's config.yaml and the proxy.
// wt owns this since the 2026-09-21 ownership move; modelman shells out to
// these commands (see docs/superpowers/specs/2026-09-21-wt-litellm-ownership-design.md).
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/spf13/cobra"
)

// litellmFlags carries the change subcommands' flags.
type litellmFlags struct {
	JSON          bool
	DryRun        bool
	SkipReadyGate bool
}

type litellmOutcomeJSON struct {
	ID     string `json:"id"`
	Action string `json:"action,omitempty"`
	Error  string `json:"error,omitempty"`
}

type litellmResultJSON struct {
	Outcomes []litellmOutcomeJSON `json:"outcomes"`
	Changed  bool                 `json:"changed"`
	Warnings []string             `json:"warnings"`
}

var errLitellmIDFailed = errors.New("one or more models could not be applied")

// reportLitellm prints a Result (text or JSON) and returns errLitellmIDFailed
// when any id was rejected, so the process exits 1 while the per-id detail is
// still printed for callers that parse it.
func reportLitellm(out, errOut io.Writer, res litellm.Result, asJSON bool) error {
	failed := false
	doc := litellmResultJSON{Outcomes: []litellmOutcomeJSON{}, Changed: res.Changed, Warnings: append([]string{}, res.Warnings...)}
	for _, o := range res.Outcomes {
		j := litellmOutcomeJSON{ID: o.ID, Action: o.Action}
		if o.Err != nil {
			j.Action, j.Error, failed = "", o.Err.Error(), true
		}
		doc.Outcomes = append(doc.Outcomes, j)
	}
	if asJSON {
		if err := json.NewEncoder(out).Encode(doc); err != nil {
			return err
		}
	} else {
		for _, j := range doc.Outcomes {
			if j.Error != "" {
				fmt.Fprintf(errOut, "%s: %s\n", j.ID, j.Error)
			} else {
				fmt.Fprintf(out, "%s: %s\n", j.ID, j.Action)
			}
		}
		for _, w := range doc.Warnings {
			fmt.Fprintf(errOut, "warning: %s\n", w)
		}
	}
	if failed {
		return errLitellmIDFailed
	}
	return nil
}

func runLitellmChange(out, errOut io.Writer, cfg *config.Config, expose bool, ids []string, fl litellmFlags) error {
	o := litellm.Options{SkipReadyGate: fl.SkipReadyGate}
	var res litellm.Result
	var err error
	switch {
	case expose && fl.DryRun:
		res.Outcomes = litellm.Check(cfg, ids, fl.SkipReadyGate)
	case expose:
		res, err = litellm.Apply(cfg, ids, nil, o)
	default:
		res, err = litellm.Apply(cfg, nil, ids, o)
	}
	if err != nil {
		return err
	}
	return reportLitellm(out, errOut, res, fl.JSON)
}

func runLitellmSync(out, errOut io.Writer, cfg *config.Config, asJSON bool) error {
	var running []string
	for _, e := range probeInventory(cfg).Entries {
		if e.Running && e.Registered {
			running = append(running, e.ModelID)
		}
	}
	res, err := litellm.Sync(cfg, running, litellm.Options{})
	if err != nil {
		return err
	}
	return reportLitellm(out, errOut, res, asJSON)
}

func runLitellmList(out io.Writer, asJSON bool) error {
	f, err := litellm.Open(litellm.DefaultPath())
	if err != nil {
		return err
	}
	ids := f.RoutedIDs()
	if ids == nil {
		ids = []string{}
	}
	if asJSON {
		return json.NewEncoder(out).Encode(map[string][]string{"routed": ids})
	}
	for _, id := range ids {
		fmt.Fprintln(out, id)
	}
	return nil
}

func runLitellmProviders(out io.Writer, asJSON bool) error {
	provs := litellm.Providers()
	if asJSON {
		doc := map[string]map[string]map[string]bool{"providers": {}}
		for id, cloud := range provs {
			doc["providers"][id] = map[string]bool{"cloud": cloud}
		}
		return json.NewEncoder(out).Encode(doc)
	}
	ids := make([]string, 0, len(provs))
	for id := range provs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		fmt.Fprintf(out, "%s cloud=%v\n", id, provs[id])
	}
	return nil
}

func litellmCmd(a *app) *cobra.Command {
	c := &cobra.Command{
		Use:   "litellm",
		Short: "Manage LiteLLM routes and routing state for registry models",
	}
	change := func(use, short string, expose bool) *cobra.Command {
		var fl litellmFlags
		cc := &cobra.Command{
			Use: use, Short: short, Args: cobra.MinimumNArgs(1), SilenceUsage: true,
			RunE: func(cmd *cobra.Command, args []string) error {
				return runLitellmChange(cmd.OutOrStdout(), cmd.ErrOrStderr(), a.cfg, expose, args, fl)
			},
		}
		cc.Flags().BoolVar(&fl.JSON, "json", false, "machine-readable output")
		if expose {
			cc.Flags().BoolVar(&fl.DryRun, "dry-run", false, "validate only; change nothing")
			cc.Flags().BoolVar(&fl.SkipReadyGate, "skip-ready-gate", false, "caller already verified readiness (used by modelman)")
		}
		return cc
	}
	var syncJSON, listJSON, provJSON bool
	syncC := &cobra.Command{
		Use: "sync", Short: "Make local-model routes match the running models", Args: cobra.NoArgs, SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runLitellmSync(cmd.OutOrStdout(), cmd.ErrOrStderr(), a.cfg, syncJSON)
		},
	}
	syncC.Flags().BoolVar(&syncJSON, "json", false, "machine-readable output")
	listC := &cobra.Command{
		Use: "list", Short: "List routed model ids from config.yaml", Args: cobra.NoArgs, SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error { return runLitellmList(cmd.OutOrStdout(), listJSON) },
	}
	listC.Flags().BoolVar(&listJSON, "json", false, "machine-readable output")
	provC := &cobra.Command{
		Use: "providers", Short: "List providers with a LiteLLM mapping", Args: cobra.NoArgs, SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error { return runLitellmProviders(cmd.OutOrStdout(), provJSON) },
	}
	provC.Flags().BoolVar(&provJSON, "json", false, "machine-readable output")
	c.AddCommand(
		change("expose <model-id>...", "Add LiteLLM routes and restart the proxy once", true),
		change("unexpose <model-id>...", "Remove LiteLLM routes and restart the proxy once", false),
		syncC, listC, provC,
	)
	return c
}
