// wt start / wt stop — direct control of local models without launching an
// agent. Start reuses the non-TUI start driver (startModel); stop reuses the
// survey package's stop loop and picker.
package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/survey"
	"github.com/spf13/cobra"
)

// Test seams: production talks to the live inventory/providers; cmd/wt's
// TestMain stubs all four.
var (
	stopCandidates = survey.StopCandidates
	stopEntries    = survey.StopEntries
	stopPickerAll  = func(cfg *config.Config) {
		survey.PickerWith(os.Stdin, os.Stdout, cfg, survey.Options{IncludeInUse: true})
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

// runStop implements `wt stop`. Argument errors return before any side effect.
func runStop(out io.Writer, cfg *config.Config, arg string, yes bool) error {
	cands := stopCandidates(cfg)
	if arg == "" {
		if !stdinTTY() {
			return fmt.Errorf("wt stop needs a TTY to list models; pass a model or provider (wt stop <provider>/<name> | wt stop <provider>)")
		}
		if len(cands) == 0 {
			fmt.Fprintln(out, "wt: no running local models")
			return nil
		}
		stopPickerAll(cfg)
		return nil
	}

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
		if !lifecycle.CanStop(arg) {
			return fmt.Errorf("unknown provider %q (valid: ollama, omlx, omlx-6bit, mtplx)", arg)
		}
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
