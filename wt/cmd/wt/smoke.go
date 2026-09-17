// wt smoke — given a model, finds every agent currently eligible for it
// and runs a one-shot prompt through each via wt's real in-process launch
// machinery (agents.BuildLaunchCmd), reporting PASS/FAIL/SKIP. Distinct
// from `make test-agents` (agents-smoke.sh's hand-curated regression
// matrix): this command answers "is this model healthy across wt right
// now" for whichever model you point it at.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/smoke"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tui"
	"github.com/spf13/cobra"
)

// smokeExit is a test seam wrapping os.Exit. Production sets the process
// exit code to 1 when any row FAILed (after the report has already been
// printed); tests override it to record the code instead of terminating
// the test process.
var smokeExit = os.Exit

// pickModelTUI is a test seam wrapping tui.PickModel (the same decorated
// picker the wt agent flow uses): production dials the real TUI, which
// needs a TTY; tests stub it to avoid one.
var pickModelTUI = tui.PickModel

// smokeNow is a test seam wrapping time.Now so progress-log timestamps are
// deterministic in tests.
var smokeNow = time.Now

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
			m, eligible, err := resolveSmokeModel(a.cfg, a.theme, modelID)
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
			stderr := cmd.ErrOrStderr()
			rows := make([]smoke.RowResult, 0, len(agentsToRun))
			for _, agentName := range agentsToRun {
				prompt, sentinel := promptOverride, ""
				if promptOverride == "" {
					prompt, sentinel = smoke.DefaultPrompt(agentName, runID)
				}
				logSmokeStart(stderr, agentName, m.ID)
				r := smoke.RunRow(a.cfg, agentName, m, prompt, sentinel, timeout, cwd)
				logSmokeResult(stderr, r)
				rows = append(rows, r)
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
// an interactive pick from the eligible list when omitted, via the same
// decorated picker (tui.PickModel) the wt agent flow uses — unfiltered by
// agent, since here the eligible agents are determined *from* the chosen
// model rather than the other way around. Also returns the resolved
// model's eligible agents (from the same smoke.Eligibility call that
// resolved the model) so the caller doesn't need a second
// smoke.EligibleAgents call — that would pay its own localgate probe round
// on top of this one.
func resolveSmokeModel(cfg *config.Config, theme themes.Theme, modelID string) (config.Model, []string, error) {
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
	m, ok, err := pickModelTUI(cfg, all, theme)
	if err != nil {
		return config.Model{}, nil, err
	}
	if !ok {
		return config.Model{}, nil, fmt.Errorf("model selection canceled")
	}
	return m, agentsForModel[m.ID], nil
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
			writeSmokeFailDetail(w, r)
		}
	}
	fmt.Fprintf(w, "=== PASS: %d FAIL: %d SKIP: %d (model=%s, runid=%s) ===\n", pass, fail, skip, modelID, runID)
	return fail > 0, nil
}

// writeSmokeFailDetail writes a FAIL row's command/exit-code/error/output
// block. Shared by printSmokeHuman's final report and logSmokeResult's live
// stderr progress line so a FAIL is fully debuggable the moment it happens —
// without waiting for the run to finish or re-running anything by hand — and
// the two renderings can never drift apart.
func writeSmokeFailDetail(w io.Writer, r smoke.RowResult) {
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

func formatSmokeDuration(d time.Duration) string {
	return fmt.Sprintf("%.1fs", d.Seconds())
}

// logSmokeStart writes a timestamped "starting" progress line to stderr
// before an agent's row runs, so a long-running or hung agent is visible in
// real time instead of silence until the final report.
func logSmokeStart(w io.Writer, agentName, modelID string) {
	fmt.Fprintf(w, "[%s] wt smoke: %s x %s - starting\n", smokeNow().Format("15:04:05"), agentName, modelID)
}

// logSmokeResult writes a timestamped result line to stderr right after an
// agent's row finishes. PASS is a single line; SKIP appends its one-line
// reason (e.g. agent not installed); FAIL appends its one-line reason and
// then the same command/exit-code/error/output detail block the final
// report shows, so a failure is fully debuggable live, without waiting for
// the run to finish.
func logSmokeResult(w io.Writer, r smoke.RowResult) {
	ts := smokeNow().Format("15:04:05")
	switch r.Status {
	case smoke.StatusPass:
		fmt.Fprintf(w, "[%s] wt smoke: %s x %s - PASS (%s)\n", ts, r.Agent, r.Model, formatSmokeDuration(r.Duration))
	case smoke.StatusSkip:
		fmt.Fprintf(w, "[%s] wt smoke: %s x %s - SKIP (%s): %v\n", ts, r.Agent, r.Model, formatSmokeDuration(r.Duration), r.Err)
	default:
		fmt.Fprintf(w, "[%s] wt smoke: %s x %s - FAIL (%s): %v\n", ts, r.Agent, r.Model, formatSmokeDuration(r.Duration), r.Err)
		writeSmokeFailDetail(w, r)
	}
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
