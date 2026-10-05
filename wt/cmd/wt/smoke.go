// wt smoke — given a model, finds every agent currently eligible for it
// and runs a one-shot prompt through each via wt's real in-process launch
// machinery (agents.BuildLaunchCmdInfo), reporting PASS/FAIL/SKIP. Distinct
// from `make test-agents` (agents-smoke.sh's hand-curated regression
// matrix): this command answers "is this model healthy across wt right
// now" for whichever model you point it at.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
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

// pickModelTUI is a test seam wrapping tui.PickStartModel: production dials the
// real TUI, which needs a TTY; tests stub it to avoid one. The route-skipping
// picker is deliberate — smoke.Candidates already applied each agent's own
// route rules, and the agent-less route check in tui.PickModel could block a
// row that an agent's forced-through-LiteLLM route accepts.
var pickModelTUI = tui.PickStartModel

// pickStartModelTUI is the test seam for `wt start`'s picker (tui.PickStartModel:
// no launch-route gating, since starting is not launching).
var pickStartModelTUI = tui.PickStartModel

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
			"Each agent runs with its permission checks off, in a fresh temporary git\n" +
			"repository that is removed afterwards; pass --cwd to run in the current\n" +
			"directory instead.\n\n" +
			"With no model-id, shows the full model picker (requires a TTY). A local\n" +
			"model that isn't running is started first; on exit, offers to stop\n" +
			"running local models.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			anyFail, err := runSmoke(cmd, a, args)
			if err != nil {
				// An interrupt is not a usage mistake: no flag summary after it.
				cmd.SilenceUsage = errors.Is(err, errSmokeInterrupted)
				return err
			}
			if anyFail {
				// The deferred stop flow has already run and may have kicked
				// off an async LiteLLM proxy restart; smokeExit bypasses
				// main's own wait, so settle it here first.
				lifecycle.WaitPendingRoutes()
				smokeExit(1) // after runSmoke's deferred stop flow has already run
			}
			return nil
		},
	}
	cmd.Flags().String("prompt", "", "Override the default sentinel prompt (weakens verification to exit-code-only)")
	cmd.Flags().Duration("timeout", 0, "Per-agent timeout (default 3m for cloud models, 15m for local)")
	cmd.Flags().String("only", "", "Comma-separated agents to restrict the run to")
	cmd.Flags().Bool("json", false, "Emit machine-readable JSON instead of a table")
	return cmd
}

// errSmokeInterrupted is what runSmoke returns for a run Ctrl+C cut short.
var errSmokeInterrupted = errors.New("interrupted")

// smokeSignalCtx is a test seam over signal.NotifyContext, as startSignalCtx
// is for the start step: the context the rows run under, cancelled by Ctrl+C
// or SIGTERM.
var smokeSignalCtx = func() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// runSmoke resolves the target, starts it if idle, runs every selected agent
// and prints the report. It returns whether any row FAILed rather than exiting,
// so the deferred stop flow runs before the caller sets the exit code.
func runSmoke(cmd *cobra.Command, a *app, args []string) (anyFail bool, err error) {
	if a.cfgErr != nil {
		return false, configError(a.cfgErr)
	}

	var modelID string
	if len(args) > 0 {
		modelID = args[0]
	}
	t, err := resolveSmokeModel(a.cfg, a.theme, modelID)
	if err != nil {
		return false, err
	}
	m, eligible := t.Row.Model, t.Agents

	if len(eligible) == 0 {
		return false, fmt.Errorf("model %q has no currently eligible agents", m.ID)
	}
	agentsToRun, err := filterOnlyAgents(eligible, mustGetString(cmd, "only"))
	if err != nil {
		return false, err
	}

	promptOverride := mustGetString(cmd, "prompt")
	timeout, _ := cmd.Flags().GetDuration("timeout")
	// Validate only an explicit value: the unset flag is 0, meaning "pick the
	// default for this model's location".
	if cmd.Flags().Changed("timeout") {
		if err := validateSmokeTimeout(timeout); err != nil {
			return false, err
		}
	}
	timeout = smokeTimeout(a.cfg, m, timeout)
	jsonOut, _ := cmd.Flags().GetBool("json")
	// --cwd is the root command's persistent flag; here it opts a run back
	// into the caller's directory. The default is a fresh temporary git
	// repository per row (#193): every row runs its agent with permission
	// checks off, and a model's stray tool call must not land in a real repo.
	inPlace, _ := cmd.Flags().GetBool("cwd")
	cwd := ""
	if inPlace {
		if cwd, err = os.Getwd(); err != nil {
			return false, err
		}
	}

	if t.Start() {
		replace, _ := cmd.Flags().GetBool("replace")
		if err := startModel(a.cfg, t.Row, replace); err != nil {
			return false, err
		}
		// The rows below fire a one-shot prompt through the LiteLLM proxy at
		// once, so the route hook's async restart has to have landed first: a
		// model wt itself just started would otherwise report a spurious FAIL
		// against a refused connection or a route table that predates it. The
		// production start driver waits for the same thing internally; this
		// wait belongs to the code about to USE the proxy, and costs nothing
		// when nothing is pending.
		waitPendingRoutes()
	} else {
		// Already running, so no start hook ran: a model started outside wt
		// has no route yet, and every row below would FAIL against the proxy
		// with "Invalid model name". Writes that one route if it is missing
		// and removes nothing; a no-op for a cloud target.
		ensureRouteBeforeLaunch(a.cfg, m)
	}
	stderr := cmd.ErrOrStderr()
	// Every row's directory, swept once more when the run ends: a row removes
	// its own directory at once, but a background hook an agent leaves behind
	// can write into it afterwards and bring it back. Registered before the
	// stop flow so that it runs after it — the time the stop picker takes is
	// time a late writer has had to finish. A directory that cannot be removed
	// is named, since it holds whatever an unsupervised agent wrote.
	var rowDirs []string
	defer func() {
		for _, d := range smoke.SweepRowDirs(rowDirs) {
			fmt.Fprintf(stderr, "wt: could not remove the smoke directory %s\n", d)
		}
	}()
	// Registered only once the start step succeeded: a failed start returns its
	// error without an interactive stop picker burying it. An interrupted run
	// skips it too — Ctrl+C means leave now.
	interrupted := false
	defer func() {
		if !interrupted {
			smokeStopFlow(a.cfg, jsonOut)
		}
	}()

	runID := smoke.NewRunID()

	// Reuse newApp()'s already-loaded profiles.toml state instead of
	// loading and re-validating the file a second time. If profiles.toml
	// was missing or malformed, degrade gracefully: smoke still runs but
	// without profile application (same best-effort posture as
	// applyProfileForLaunch).
	pp := a.profileState()
	store, loadErr, validateErr := pp.store, pp.loadErr, pp.validateErr
	if loadErr != nil {
		fmt.Fprintf(stderr, "wt: profiles.toml: %v (profiles disabled for smoke)\n", loadErr)
		store = profiles.Store{}
	}
	if validateErr != nil {
		fmt.Fprintf(stderr, "wt: profiles.toml: %v (profiles disabled for smoke)\n", validateErr)
	}

	// Ctrl+C or SIGTERM cancels ctx instead of killing wt: the running row's
	// agent is killed with its process group, its directory is removed, and
	// the rows that ran are still reported.
	ctx, stopSignals := smokeSignalCtx()
	defer stopSignals()

	rows := make([]smoke.RowResult, 0, len(agentsToRun))
	for _, agentName := range agentsToRun {
		prompt, sentinel := promptOverride, ""
		if promptOverride == "" {
			prompt, sentinel = smoke.DefaultPrompt(agentName, runID)
		}
		logSmokeStart(stderr, agentName, m.ID)

		// Resolve the profile for this agent×model pair. If one matches,
		// build a ProfileApplier that applies it via the same
		// applyResolvedProfile used by a real launch (cmd/wt/launch.go),
		// so smoke and a real launch can never drift on apply/rollback
		// semantics.
		var pa smoke.ProfileApplier
		if loadErr == nil && validateErr == nil && store.Enabled {
			rp := profiles.Resolve(store, agentName, a.cfg, m)
			if !rp.Empty() {
				pa = func(cmd *exec.Cmd, oneShotArgs []string) (func() error, error) {
					return applyResolvedProfile(cmd, agentName, rp, oneShotArgs)
				}
			}
		}

		// The row's directory is made only once the agent is known to run:
		// a row whose agent is not installed never pays for it or the sweep.
		rowDir, removeRowDir := smoke.FixedDir(cwd), func() {}
		if !inPlace {
			// One directory per row, so one agent's leftovers cannot reach
			// the next agent's run. A directory that cannot be created fails
			// its own row (RunRow reports the error): the rows that have
			// already run are still reported.
			rowDir = func() (string, error) {
				dir, remove, err := smoke.NewRowDir()
				if err != nil {
					return "", err
				}
				removeRowDir = remove
				// What the agent records about this directory in its own
				// state (claude's project entry, pi's sessions) goes with it.
				rowDirs = append(append(rowDirs, dir), smoke.AgentStateDirs(agentName, dir)...)
				return dir, nil
			}
		}
		r := smoke.RunRow(ctx, a.cfg, agentName, m, prompt, sentinel, timeout, rowDir, pa)
		removeRowDir()
		logSmokeResult(stderr, r)
		rows = append(rows, r)
		if ctx.Err() != nil {
			interrupted = true
			break
		}
	}
	// Restore the default handler: a second Ctrl+C during the report, the
	// sweep or the stop picker kills wt.
	stopSignals()

	if jsonOut {
		anyFail, err = printSmokeJSON(cmd.OutOrStdout(), runID, m.ID, rows)
	} else {
		anyFail, err = printSmokeHuman(cmd.OutOrStdout(), runID, m.ID, rows)
	}
	if interrupted && err == nil {
		err = errSmokeInterrupted
	}
	return anyFail, err
}

// Default per-agent smoke budgets. Local models cold-prefill the 10-40k-token
// preambles agents send at tens of tok/s, so one row can take 5-9 minutes;
// cloud models answer in seconds and a short budget surfaces real hangs fast.
const (
	smokeCloudTimeout = 180 * time.Second
	smokeLocalTimeout = 900 * time.Second
)

// smokeTimeout returns the explicit timeout when set (non-zero), otherwise the
// default for the model's location. A model whose location cannot be resolved
// is treated as non-local, matching Config.InCatalog.
func smokeTimeout(cfg *config.Config, m config.Model, explicit time.Duration) time.Duration {
	if explicit != 0 {
		return explicit
	}
	if loc, err := cfg.ResolveLocation(m); err == nil && loc == config.LocationLocal {
		return smokeLocalTimeout
	}
	return smokeCloudTimeout
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

// smokeTarget is the resolved model to test, its catalog row (Action tells
// whether it must be started first), and the agents that can run it.
type smokeTarget struct {
	Row    catalog.Row
	Agents []string
}

// Start reports whether the model is idle and must be started before testing.
func (t smokeTarget) Start() bool { return t.Row.Action() == catalog.ActionStart }

// resolveSmokeModel resolves the model to test from smoke.Candidates (launch
// rows plus startable idle local models): the pinned id if given (with a
// message distinguishing "unknown" from "exists but cannot be tested"), or an
// interactive pick via the shared picker (tui.PickModel), unfiltered by agent
// since the eligible agents are determined from the chosen model. The target
// carries its eligible agents so no second inventory round is needed.
func resolveSmokeModel(cfg *config.Config, theme themes.Theme, modelID string) (smokeTarget, error) {
	cands := smoke.Candidates(cfg)
	if modelID != "" {
		for _, c := range cands {
			if c.Row.Model.ID == modelID {
				return smokeTarget{Row: c.Row, Agents: c.Agents}, nil
			}
		}
		// A registry local model with no row (not on disk, or a stopped
		// mlx_lm_server pairing) or a blocked local row is not a candidate, so
		// name its real reason as `wt start` does rather than guessing.
		rows, snap := localRowsSnap(cfg)
		if row, ok := catalog.Find(rows, modelID); ok {
			if row.Action() == catalog.ActionBlock {
				return smokeTarget{}, errors.New(row.BlockReason())
			}
		} else if reason := catalog.MissingReason(&snap, modelID); reason != "" {
			return smokeTarget{}, errors.New(reason)
		}
		if config.IndexModelByID(cfg.Models, modelID) >= 0 {
			return smokeTarget{}, fmt.Errorf(
				"model %q cannot be smoke-tested right now (not in the catalog, not on disk, or no agent supports it — check `wt litellm status`; a local model must exist on disk)", modelID)
		}
		return smokeTarget{}, fmt.Errorf("unknown model %q", modelID)
	}
	if len(cands) == 0 {
		return smokeTarget{}, fmt.Errorf("no models are available for any agent — add a cloud model to the registry or pull a local one")
	}
	if !stdinTTY() {
		return smokeTarget{}, fmt.Errorf("wt smoke needs a TTY to list models; pass a model id directly (wt smoke <provider>/<name>)")
	}
	models := make([]config.Model, len(cands))
	for i, c := range cands {
		models[i] = c.Row.Model
	}
	m, ok, err := pickModelTUI(cfg, models, theme)
	if err != nil {
		return smokeTarget{}, err
	}
	if !ok {
		return smokeTarget{}, fmt.Errorf("model selection canceled")
	}
	for _, c := range cands {
		if c.Row.Model.ID == m.ID {
			return smokeTarget{Row: c.Row, Agents: c.Agents}, nil
		}
	}
	return smokeTarget{}, fmt.Errorf("unknown model %q", m.ID)
}

// smokeStopFlow is the exit flow: offer to stop running local models nothing
// else uses. Skipped for --json and when stdin is not a TTY. Unlike a real agent
// launch (cmd/wt/launch.go), wt smoke never records a refcount entry for this
// process — its one-shot agents run through smoke.RunRow and the start step is
// not a session — so there is nothing to release before the picker.
func smokeStopFlow(cfg *config.Config, jsonOut bool) {
	if jsonOut || !stdinTTY() {
		return
	}
	runStopPicker(cfg)
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
