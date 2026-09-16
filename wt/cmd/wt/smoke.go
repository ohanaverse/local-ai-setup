// wt smoke — given a model, finds every agent currently eligible for it
// and runs a one-shot prompt through each via wt's real in-process launch
// machinery (agents.BuildLaunchCmd), reporting PASS/FAIL/SKIP. Distinct
// from `make test-agents` (agents-smoke.sh's hand-curated regression
// matrix): this command answers "is this model healthy across wt right
// now" for whichever model you point it at.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/smoke"
	"github.com/spf13/cobra"
)

// smokeExit is a test seam wrapping os.Exit. Production sets the process
// exit code to 1 when any row FAILed (after the report has already been
// printed); tests override it to record the code instead of terminating
// the test process.
var smokeExit = os.Exit

func smokeCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "smoke [model-id]",
		Short: "Run a one-shot prompt through every agent eligible for a model",
		Long: "Find every agent currently eligible for a model and run a one-shot\n" +
			"prompt through each, using wt's real launch machinery. Reports\n" +
			"PASS/FAIL/SKIP per agent with enough detail to diagnose a failure\n" +
			"without re-running anything by hand.\n\n" +
			"With no model-id, lists every currently eligible model and prompts\n" +
			"for a choice (requires a TTY).",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if a.cfgErr != nil {
				return fmt.Errorf("config error: %w (run `wt config` to repair)", a.cfgErr)
			}

			var modelID string
			if len(args) > 0 {
				modelID = args[0]
			}
			m, eligible, err := resolveSmokeModel(a.cfg, modelID)
			if err != nil {
				return err
			}

			if len(eligible) == 0 {
				return fmt.Errorf("model %q has no currently eligible agents", m.ID)
			}
			agentsToRun, err := filterOnlyAgents(eligible, mustGetString(cmd, "only"))
			if err != nil {
				return err
			}

			promptOverride := mustGetString(cmd, "prompt")
			timeout, _ := cmd.Flags().GetDuration("timeout")
			if err := validateSmokeTimeout(timeout); err != nil {
				return err
			}
			jsonOut, _ := cmd.Flags().GetBool("json")

			cwd, err := os.Getwd()
			if err != nil {
				return err
			}

			runID := smoke.NewRunID()
			rows := make([]smoke.RowResult, 0, len(agentsToRun))
			for _, agentName := range agentsToRun {
				prompt, sentinel := promptOverride, ""
				if promptOverride == "" {
					prompt, sentinel = smoke.DefaultPrompt(agentName, runID)
				}
				rows = append(rows, smoke.RunRow(a.cfg, agentName, m, prompt, sentinel, timeout, cwd))
			}

			var anyFail bool
			if jsonOut {
				anyFail, err = printSmokeJSON(cmd.OutOrStdout(), runID, m.ID, rows)
			} else {
				anyFail, err = printSmokeHuman(cmd.OutOrStdout(), runID, m.ID, rows)
			}
			if err != nil {
				return err
			}
			if anyFail {
				smokeExit(1)
			}
			return nil
		},
	}
	cmd.Flags().String("prompt", "", "Override the default sentinel prompt (weakens verification to exit-code-only)")
	cmd.Flags().Duration("timeout", 180*time.Second, "Per-agent timeout")
	cmd.Flags().String("only", "", "Comma-separated agents to restrict the run to")
	cmd.Flags().Bool("json", false, "Emit machine-readable JSON instead of a table")
	return cmd
}

// validateSmokeTimeout rejects a non-positive --timeout before any row
// runs. Without this, time.After(timeout) in internal/smoke.realBuildAndRun
// fires immediately for --timeout 0 (or a negative duration), killing every
// agent the instant it starts and reporting "FAIL: timed out after 0s" for
// all of them — a confusing failure mode that reads as every agent being
// broken rather than a bad flag value.
func validateSmokeTimeout(timeout time.Duration) error {
	if timeout <= 0 {
		return fmt.Errorf("--timeout must be positive, got %s", timeout)
	}
	return nil
}

// resolveSmokeModel resolves the model to test: the pinned id if given
// (validated against the currently eligible set, with a message
// distinguishing "unknown" from "exists but not eligible right now"), or
// an interactive pick from the eligible list when omitted. Also returns the
// resolved model's eligible agents (from the same smoke.Eligibility call
// that resolved the model) so the caller doesn't need a second
// smoke.EligibleAgents call — that would pay its own localgate probe round
// on top of this one.
func resolveSmokeModel(cfg *config.Config, modelID string) (config.Model, []string, error) {
	all, agentsForModel := smoke.Eligibility(cfg)
	if modelID != "" {
		if idx := config.IndexModelByID(all, modelID); idx >= 0 {
			return all[idx], agentsForModel[all[idx].ID], nil
		}
		if idx := config.IndexModelByID(cfg.Models, modelID); idx >= 0 {
			return config.Model{}, nil, fmt.Errorf(
				"model %q is not currently eligible for any agent (not exposed, or a local "+
					"model that isn't running — check `modelman start %s` or `modelman litellm status`)",
				modelID, modelID)
		}
		return config.Model{}, nil, fmt.Errorf("unknown model %q", modelID)
	}
	if len(all) == 0 {
		return config.Model{}, nil, fmt.Errorf("no models are currently eligible for any agent — expose a cloud model or start a local one with `modelman start <id>`")
	}
	if !stdinTTY() {
		return config.Model{}, nil, fmt.Errorf("wt smoke needs a TTY to list models; pass a model id directly (wt smoke <provider>/<name>)")
	}
	m, err := pickModelInteractive(os.Stdin, os.Stdout, all)
	if err != nil {
		return config.Model{}, nil, err
	}
	return m, agentsForModel[m.ID], nil
}

// pickModelInteractive prints a numbered list of models to w and reads a
// selection from r. Takes an explicit reader/writer (rather than os.Stdin/
// os.Stdout directly) so tests can drive it without a real TTY.
func pickModelInteractive(r io.Reader, w io.Writer, models []config.Model) (config.Model, error) {
	fmt.Fprintln(w, "Eligible models:")
	for i, m := range models {
		fmt.Fprintf(w, "  %d) %s\n", i+1, m.ID)
	}
	fmt.Fprint(w, "Select a model: ")
	line, err := bufio.NewReader(r).ReadString('\n')
	// ReadString returns the data read so far alongside the error when the
	// stream ends before the delimiter (e.g. stdin closed with no trailing
	// newline) — only bail if nothing was read at all.
	if err != nil && line == "" {
		return config.Model{}, err
	}
	line = strings.TrimSpace(line)
	n, err := strconv.Atoi(line)
	if err != nil || n < 1 || n > len(models) {
		return config.Model{}, fmt.Errorf("invalid selection %q", line)
	}
	return models[n-1], nil
}

// filterOnlyAgents narrows eligible to the comma-separated --only list,
// erroring before anything runs if a requested agent isn't eligible for
// this model (fail fast, mirrors agents-smoke.sh's validate_only_agents).
// An empty only returns eligible unchanged.
func filterOnlyAgents(eligible []string, only string) ([]string, error) {
	wanted := config.ParseFilterList(only)
	if len(wanted) == 0 {
		return eligible, nil
	}
	eligibleSet := make(map[string]bool, len(eligible))
	for _, a := range eligible {
		eligibleSet[a] = true
	}
	for _, w := range wanted {
		if !eligibleSet[w] {
			return nil, fmt.Errorf("agent %q is not eligible for this model (eligible: %s)", w, strings.Join(eligible, ", "))
		}
	}
	return wanted, nil
}

// printSmokeHuman renders the per-row lines (with an expanded detail block
// for every FAIL) and the trailing counts line. Returns whether any row
// FAILed.
func printSmokeHuman(w io.Writer, runID, modelID string, rows []smoke.RowResult) (bool, error) {
	var pass, fail, skip int
	for _, r := range rows {
		fmt.Fprintf(w, "[%-5s] %-10s %s (%s)\n", r.Status, r.Agent, modelID, formatSmokeDuration(r.Duration))
		switch r.Status {
		case smoke.StatusPass:
			pass++
		case smoke.StatusSkip:
			skip++
		default:
			fail++
			fmt.Fprintf(w, "  command: %s\n", r.Command)
			fmt.Fprintf(w, "  exit code: %d\n", r.ExitCode)
			if r.Err != nil {
				fmt.Fprintf(w, "  error: %v\n", r.Err)
			}
			if r.Output != "" {
				fmt.Fprintln(w, "  output:")
				for _, line := range strings.Split(strings.TrimRight(r.Output, "\n"), "\n") {
					fmt.Fprintf(w, "    %s\n", line)
				}
			}
		}
	}
	fmt.Fprintf(w, "=== PASS: %d FAIL: %d SKIP: %d (model=%s, runid=%s) ===\n", pass, fail, skip, modelID, runID)
	return fail > 0, nil
}

func formatSmokeDuration(d time.Duration) string {
	return fmt.Sprintf("%.1fs", d.Seconds())
}

// smokeJSONRow and smokeJSONReport are --json's wire schema — the contract
// a scripted caller (a future agents-smoke.sh refactor, or an LLM agent
// parsing output) consumes.
type smokeJSONRow struct {
	Agent      string `json:"agent"`
	Model      string `json:"model"`
	Status     string `json:"status"`
	ExitCode   int    `json:"exit_code"`
	DurationMS int64  `json:"duration_ms"`
	Command    string `json:"command"`
	Output     string `json:"output"`
	Error      string `json:"error,omitempty"`
}

type smokeJSONReport struct {
	RunID string         `json:"run_id"`
	Model string         `json:"model"`
	Rows  []smokeJSONRow `json:"rows"`
}

// printSmokeJSON encodes rows as the --json contract and returns whether
// any row FAILed.
func printSmokeJSON(w io.Writer, runID, modelID string, rows []smoke.RowResult) (bool, error) {
	report := smokeJSONReport{RunID: runID, Model: modelID, Rows: make([]smokeJSONRow, 0, len(rows))}
	var anyFail bool
	for _, r := range rows {
		errStr := ""
		if r.Err != nil {
			errStr = r.Err.Error()
		}
		if r.Status == smoke.StatusFail {
			anyFail = true
		}
		report.Rows = append(report.Rows, smokeJSONRow{
			Agent:      r.Agent,
			Model:      r.Model,
			Status:     string(r.Status),
			ExitCode:   r.ExitCode,
			DurationMS: r.Duration.Milliseconds(),
			Command:    r.Command,
			Output:     r.Output,
			Error:      errStr,
		})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		return false, err
	}
	return anyFail, nil
}
