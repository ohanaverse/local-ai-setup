// wt start / wt stop — direct control of local models without launching an
// agent. Start reuses the non-TUI start driver (startModel); stop reuses the
// survey package's stop loop and picker.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

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
			"Stopping a model a live wt session uses asks for confirmation; --yes skips it.",
		Example: "  wt stop ollama/qwen3.8:27b-mlx\n  wt stop ollama\n  wt stop",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if a.cfgErr != nil {
				return fmt.Errorf("config error: %w (run `wt config` to repair)", a.cfgErr)
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
		for _, c := range cands {
			if c.Entry.ProviderID == arg {
				targets = append(targets, c)
			}
		}
		if len(targets) == 0 {
			fmt.Fprintf(out, "wt: nothing running on %s\n", arg)
			return nil
		}
	}

	inUse := 0
	entries := make([]localmodels.Entry, 0, len(targets))
	for _, c := range targets {
		entries = append(entries, c.Entry)
		if c.Sessions > inUse {
			inUse = c.Sessions
		}
	}
	if inUse > 0 && !yes {
		ok, err := confirmStop(fmt.Sprintf("%s is in use by %d live wt session(s); stop anyway?", arg, inUse))
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("cancelled — %s is still running", arg)
		}
	}
	return stopEntries(out, cfg, entries)
}

func startCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "start [model]",
		Short: "Start a local model",
		Long: "Start a local model (<provider>/<name>, as with -M). With no argument, shows\n" +
			"the full model picker over every configured and detected local model,\n" +
			"running ones included (requires a TTY); picking a running model does nothing.\n\n" +
			"If the provider's single slot is occupied, asks before replacing the running\n" +
			"model; --replace skips the question.",
		Example: "  wt start ollama/qwen3.8:27b-mlx\n  wt start",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if a.cfgErr != nil {
				return fmt.Errorf("config error: %w (run `wt config` to repair)", a.cfgErr)
			}
			id := ""
			if len(args) > 0 {
				id = args[0]
			}
			replace, _ := cmd.Flags().GetBool("replace")
			return runStart(cmd.OutOrStdout(), a.cfg, a.theme, id, replace)
		},
	}
	return cmd
}

// localRows builds catalog rows for every configured and detected local model
// from one live inventory snapshot — screen 1's row set.
func localRows(cfg *config.Config) []catalog.Row {
	snap := probeInventory(cfg)
	var models []config.Model
	for _, m := range cfg.Models {
		if loc, err := cfg.ResolveLocation(m); err == nil && loc == config.LocationLocal {
			models = append(models, m)
		}
	}
	return catalog.Build(catalog.Input{Config: cfg, Models: models, Inventory: &snap})
}

// runStart implements `wt start`. Argument errors return before any side effect.
func runStart(out io.Writer, cfg *config.Config, theme themes.Theme, id string, replace bool) error {
	rows := localRows(cfg)
	if id == "" {
		if !stdinTTY() {
			return fmt.Errorf("wt start needs a TTY to list models; pass a model id directly (wt start <provider>/<name>)")
		}
		if len(rows) == 0 {
			return fmt.Errorf("no local models are configured or detected")
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
		fmt.Fprintf(out, "wt: %s is already running\n", id)
		return nil
	default: // catalog.ActionBlock: BlockReason is non-empty by definition
		return errors.New(row.BlockReason())
	}
}
