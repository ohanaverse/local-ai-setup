// wt start / wt stop — direct control of local models without launching an
// agent. Start reuses the non-TUI start driver (startModel); stop reuses the
// survey package's stop loop and picker. wt served reports what a provider's
// server is serving, the probe the other two act on.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/survey"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/spf13/cobra"
)

// Test seams: production talks to the live inventory/providers; cmd/wt's
// TestMain stubs all four.
var (
	stopCandidates = survey.StopCandidates
	stopEntries    = survey.StopEntries
	// stopPickerAll runs the in-use-inclusive picker and reports whether it had
	// anything to offer, so `wt stop` needs no inventory probe of its own.
	stopPickerAll = func(cfg *config.Config) bool {
		return survey.PickerWith(os.Stdin, os.Stdout, cfg, survey.Options{IncludeInUse: true})
	}
	confirmStop = promptStop
	// stopProvider stops a provider's server as a whole (`wt stop omlx`).
	stopProvider = lifecycle.Stop
)

// promptStop is promptReplace's twin for stopping an in-use model: y/N on the
// controlling terminal, default No, so piped input can never authorise it.
func promptStop(question string) (bool, error) {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return false, fmt.Errorf("%s — rerun with --yes to confirm", question)
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "%s [y/N] ", question); err != nil {
		return false, err
	}
	return askYesNo(f)
}

func stopCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stop [model|provider]",
		Short: "Stop a running local model, or every model of a provider",
		Long: "Stop a running local model (<provider>/<name>) or every running model of a\n" +
			"provider (ollama, omlx, omlx-6bit, mtplx). With no argument, shows the\n" +
			"stop picker (requires a TTY), which also lists models other wt sessions\n" +
			"are using, marked with their session count.\n\n" +
			"On omlx, stopping a model unloads that model and leaves the service and its other\n" +
			"models up; \"wt stop omlx\" stops the service.\n\n" +
			"Stopping a model a live wt session uses asks for confirmation; --yes skips it.",
		Example: "  wt stop ollama/qwen3.8:27b-mlx\n  wt stop ollama\n  wt stop",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if a.cfgErr != nil {
				return configError(a.cfgErr)
			}
			arg := ""
			if len(args) > 0 {
				arg = args[0]
			}
			yes, _ := cmd.Flags().GetBool("yes")
			return runStop(cmd.OutOrStdout(), a.cfg, arg, yes)
		},
	}
	cmd.Flags().Bool("yes", false, "Skip the confirmation when a target is in use by a live wt session")
	return cmd
}

// stoppableProviders lists, sorted, the configured provider ids wt can stop.
// The bare-provider check and its error message both read it, so they cannot
// disagree about what is valid.
func stoppableProviders(cfg *config.Config) []string {
	var ids []string
	for _, p := range cfg.Providers {
		if lifecycle.CanStop(p.ID) {
			ids = append(ids, p.ID)
		}
	}
	slices.Sort(ids)
	return ids
}

// runStop implements `wt stop`. Argument errors return before any side effect.
func runStop(out io.Writer, cfg *config.Config, arg string, yes bool) error {
	// A bare provider is validated before the live inventory probe; a model id
	// still needs the probe to find its running entry.
	if arg != "" && !strings.Contains(arg, "/") {
		if valid := stoppableProviders(cfg); !slices.Contains(valid, arg) {
			return fmt.Errorf("unknown provider %q (valid: %s)", arg, strings.Join(valid, ", "))
		}
	}
	if arg == "" {
		if !stdinTTY() {
			return fmt.Errorf("wt stop needs a TTY to list models; pass a model or provider (wt stop <provider>/<name> | wt stop <provider>)")
		}
		// The picker takes the one inventory snapshot itself and reports whether
		// it had anything to offer, so nothing is probed twice.
		if !stopPickerAll(cfg) {
			fmt.Fprintln(out, "wt: no running local models")
		}
		return nil
	}
	cands := stopCandidates(cfg)

	var targets []survey.Candidate
	if strings.Contains(arg, "/") {
		for _, c := range cands {
			if c.Entry.ModelID == arg {
				targets = append(targets, c)
			}
		}
		if len(targets) == 0 {
			if config.IndexModelByID(cfg.Models, arg) >= 0 {
				return fmt.Errorf("model %q is not running", arg)
			}
			return fmt.Errorf("unknown model %q", arg)
		}
	} else {
		pool := lifecycle.TenancyOf(arg) == lifecycle.Pool
		for _, c := range cands {
			// A pool's server is stopped as a whole, so every model of the
			// family is affected, whichever of its rows it is listed under.
			if c.Entry.ProviderID == arg || (pool && localmodels.Family(c.Entry.ProviderID) == localmodels.Family(arg)) {
				targets = append(targets, c)
			}
		}
		if len(targets) == 0 && !pool {
			fmt.Fprintf(out, "wt: nothing running on %s\n", arg)
			return nil
		}
	}

	targets = withFamilyCollateral(out, cands, targets)
	entries := make([]localmodels.Entry, 0, len(targets))
	for _, c := range targets {
		entries = append(entries, c.Entry)
	}
	if inUse, users := stopImpact(targets); inUse > 0 && !yes {
		ok, err := confirmStop(fmt.Sprintf("%s is in use by %d live wt session(s) (%s); stop anyway?", arg, inUse, strings.Join(users, ", ")))
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("cancelled — %s is still running", arg)
		}
	}
	if !strings.Contains(arg, "/") && lifecycle.TenancyOf(arg) == lifecycle.Pool {
		// A model stop on a pool only unloads; the bare provider halts the
		// service, which is the one way to free the server itself.
		ctx, cancel := startSignalCtx()
		defer cancel()
		fmt.Fprintf(out, "Stopping %s... ", arg)
		if err := stopProvider(ctx, cfg, arg); err != nil {
			fmt.Fprintln(out, "failed")
			return err
		}
		fmt.Fprintln(out, "done")
		return nil
	}
	return stopEntries(out, cfg, entries)
}

// withFamilyCollateral widens targets to every running model of any
// Exclusive provider (mtplx) they touch: that provider stops as a whole, so a
// sibling variant goes down with the one named. A Pool (omlx) is not widened:
// stopping one of its models leaves the others loaded. Collateral models are
// announced on out. Targets keep their order; collateral follows.
func withFamilyCollateral(out io.Writer, cands, targets []survey.Candidate) []survey.Candidate {
	have := map[string]bool{}
	fams := map[string]bool{}
	for _, c := range targets {
		have[c.Entry.ModelID] = true
		if lifecycle.TenancyOf(c.Entry.ProviderID) == lifecycle.Exclusive {
			fams[localmodels.Family(c.Entry.ProviderID)] = true
		}
	}
	for _, c := range cands {
		if have[c.Entry.ModelID] || lifecycle.TenancyOf(c.Entry.ProviderID) != lifecycle.Exclusive || !fams[localmodels.Family(c.Entry.ProviderID)] {
			continue
		}
		fmt.Fprintf(out, "wt: %s runs one model per process; stopping it also stops %s\n", localmodels.Family(c.Entry.ProviderID), c.Entry.ModelID)
		targets = append(targets, c)
	}
	return targets
}

// stopImpact totals the live wt sessions a stop would hit and names the models
// they use. A single-model provider's candidates each carry the whole family's
// count, so the family is counted once; other candidates sum.
func stopImpact(targets []survey.Candidate) (sessions int, users []string) {
	famSeen := map[string]bool{}
	for _, c := range targets {
		if c.Sessions == 0 {
			continue
		}
		users = append(users, c.Entry.ModelID)
		if lifecycle.TenancyOf(c.Entry.ProviderID) == lifecycle.Exclusive {
			fam := localmodels.Family(c.Entry.ProviderID)
			if famSeen[fam] {
				continue
			}
			famSeen[fam] = true
		}
		sessions += c.Sessions
	}
	return sessions, users
}

func startCmd(a *app) *cobra.Command {
	var asJSON, plan bool
	cmd := &cobra.Command{
		Use:   "start [model]",
		Short: "Start a local model",
		Long: "Start a local model (<provider>/<name>, as with -M). With no argument, shows\n" +
			"the full model picker over every local model that is on disk or running,\n" +
			"registered or detected (requires a TTY).\n\n" +
			"A model that is already running is left running; its LiteLLM route is\n" +
			"written if it is missing. A model omlx is still loading is waited for.\n\n" +
			"On a provider that serves one model (mtplx), starting another replaces it.\n" +
			"On omlx a model loads beside the ones already loaded; when it does not fit,\n" +
			"omlx unloads the least recently used. wt asks before either; --replace skips\n" +
			"the question.",
		Example: "  wt start ollama/qwen3.8:27b-mlx\n  wt start",
		Args:    cobra.MaximumNArgs(1),
		// A refused or failed JSON start is not a usage mistake.
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if a.cfgErr != nil {
				return configError(a.cfgErr)
			}
			id := ""
			if len(args) > 0 {
				id = args[0]
			}
			replace, _ := cmd.Flags().GetBool("replace")
			if plan && !asJSON {
				return errors.New("--plan needs --json")
			}
			if asJSON {
				return runStartJSON(cmd.OutOrStdout(), a.cfg, id, plan, replace)
			}
			return runStart(cmd.OutOrStdout(), a.cfg, a.theme, id, replace)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable output; never prompts")
	cmd.Flags().BoolVar(&plan, "plan", false, "with --json: report what a start would unload, and change nothing")
	return cmd
}

// servedProbeTimeout bounds each request `wt served` makes — the lifecycle
// engine's probe timeout, since it asks the same question.
const servedProbeTimeout = 5 * time.Second

// servedFamilies are the providers `wt served` answers for: the ones whose
// server says what it is serving. Ollama has no such answer — a pulled model
// loads on request — so it is left out rather than answered with its pulls.
var servedFamilies = []string{"mlx_lm_server", "mtplx", "omlx"}

func servedCmd(a *app) *cobra.Command {
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
		Example: "  wt served omlx\n  wt served mtplx --json",
		Args:    cobra.ExactArgs(1),
		// A probe that gets no answer is the expected failure here, not a
		// usage mistake.
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// The load error only: this reads the registry's provider rows
			// and writes nothing, so a validation gap elsewhere in the
			// config must not make the probe unavailable.
			if a.loadErr != nil {
				return configError(a.loadErr)
			}
			client := &http.Client{Timeout: servedProbeTimeout}
			return runServed(cmd.OutOrStdout(), a.cfg, client, args[0], asJSON)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable output")
	return cmd
}

func runServed(out io.Writer, cfg *config.Config, client *http.Client, family string, asJSON bool) error {
	if !slices.Contains(servedFamilies, family) {
		return fmt.Errorf("unknown provider %q: wt served answers for %s", family, strings.Join(servedFamilies, ", "))
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

func warmCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "warm <provider> <model>",
		Short: "Load a model into a running omlx server",
		Long: "Load a model into an omlx server that is already running, by sending it one\n" +
			"short request. <model> is the name omlx serves it under, or a repo id whose\n" +
			"last segment is that name. When the server has an API key, wt sends the one\n" +
			"the registry's omlx provider names (auth.secret_ref).\n\n" +
			"Nothing is started, stopped or routed: this is the warmup step of `wt start`\n" +
			"by itself, which `modelman start` asks for when omlx refuses its keyless\n" +
			"request. To start a model, use `wt start`.",
		Example: "  wt warm omlx Qwen3.8-27B-4bit",
		Args:    cobra.ExactArgs(2),
		// A server that refuses or never loads the model is the expected
		// failure here, not a usage mistake.
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// The load error only, as for `wt served`: this reads the
			// registry's provider rows and writes nothing.
			if a.loadErr != nil {
				return configError(a.loadErr)
			}
			ctx, cancel := startSignalCtx()
			defer cancel()
			return runWarm(ctx, cmd.OutOrStdout(), a.cfg, args[0], args[1])
		},
	}
}

func runWarm(ctx context.Context, out io.Writer, cfg *config.Config, provider, model string) error {
	if localmodels.Family(provider) != "omlx" {
		return fmt.Errorf("unknown provider %q: wt warm loads models into omlx only", provider)
	}
	if err := lifecycle.Warm(ctx, cfg, provider, model); err != nil {
		return err
	}
	fmt.Fprintf(out, "%s is loaded on %s\n", model, provider)
	return nil
}

// localRows builds catalog rows for every local model the live inventory
// lists (registry models on disk or running, plus detected ones) from one
// snapshot — screen 1's row set.
func localRows(cfg *config.Config) []catalog.Row {
	rows, _ := localRowsSnap(cfg)
	return rows
}

// localRowsSnap is localRows plus the snapshot the rows were built from, for
// callers that must explain an id with no row (catalog.MissingReason).
func localRowsSnap(cfg *config.Config) ([]catalog.Row, localmodels.Snapshot) {
	snap := probeInventory(cfg)
	var models []config.Model
	for _, m := range cfg.Models {
		if loc, err := cfg.ResolveLocation(m); err == nil && loc == config.LocationLocal {
			models = append(models, m)
		}
	}
	return catalog.Build(catalog.Input{Config: cfg, Models: models, Inventory: &snap}), snap
}

// runStart implements `wt start`. Argument errors return before any side effect.
func runStart(out io.Writer, cfg *config.Config, theme themes.Theme, id string, replace bool) error {
	rows, snap := localRowsSnap(cfg)
	if id == "" {
		if !stdinTTY() {
			return fmt.Errorf("wt start needs a TTY to list models; pass a model id directly (wt start <provider>/<name>)")
		}
		if len(rows) == 0 {
			return fmt.Errorf("no local model is on disk or running")
		}
		models := make([]config.Model, len(rows))
		for i, r := range rows {
			models[i] = r.Model
		}
		m, ok, err := pickStartModelTUI(cfg, models, theme)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("model selection canceled")
		}
		id = m.ID
	}
	row, ok := catalog.Find(rows, id)
	if !ok {
		if reason := catalog.MissingReason(&snap, id); reason != "" {
			return errors.New(reason)
		}
		if config.IndexModelByID(cfg.Models, id) >= 0 {
			return fmt.Errorf("%q is not a local model — wt start only starts local models", id)
		}
		return fmt.Errorf("unknown model %q", id)
	}
	switch row.Action() {
	case catalog.ActionStart:
		if err := startModel(cfg, row, replace); err != nil {
			return err
		}
		fmt.Fprintf(out, "wt: %s is running\n", id)
		return nil
	case catalog.ActionLaunch:
		// Already running, but not necessarily started by wt: write its route
		// if it is missing, removing nothing. Unconditional, whatever the
		// routing toggle says — the start hook writes its route the same way,
		// and `wt start` has no agent whose route could say whether the proxy
		// is on its path.
		ensureRouteBeforeLaunch(cfg, row.Model)
		fmt.Fprintf(out, "wt: %s is already running\n", id)
		return nil
	default: // catalog.ActionBlock: BlockReason is non-empty by definition
		return errors.New(row.BlockReason())
	}
}
