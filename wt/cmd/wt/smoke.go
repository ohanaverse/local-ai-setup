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
	"sync"
	"syscall"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/exitcode"
	"github.com/ohanaverse/local-ai-setup/wt/internal/launchwarn"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
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
// route rules, and the agent-less route check the picker otherwise runs could block a
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
			"model that isn't running is started on the model server first; the\n" +
			"server stops it after its idle grace. Needs a model server\n" +
			"([model-server] in config.toml, or WT_MODEL_URL); with none, exit 1.",
		Args: cobra.MaximumNArgs(1),
		// main prints the one error line; cobra's own print made a failed
		// start's message, log tail included, come out twice (#344).
		SilenceErrors: true,
		// A usage mistake shows usage (RunE below sets it per error), so the
		// walk in rootCmd leaves this command's SilenceUsage alone.
		Annotations: map[string]string{usageOnMistakeAnnotation: "smoke"},
		RunE: func(cmd *cobra.Command, args []string) error {
			anyFail, err := runSmoke(cmd, a, args)
			return smokeFinish(cmd, anyFail, err)
		},
	}
	cmd.Flags().String("prompt", "", "Override the default sentinel prompt (weakens verification to exit-code-only)")
	cmd.Flags().Bool("tools", false, "Ask each agent to read a nonce file with a tool call (catches models that cannot call tools; not with --cwd or --prompt)")
	cmd.Flags().Int("hops", 1, "With --tools, chain this many dependent file reads (each file names the next)")
	cmd.Flags().Duration("timeout", 0, "Per-agent timeout (default 3m for cloud models, 15m for local)")
	cmd.Flags().String("only", "", "Comma-separated agents to restrict the run to")
	cmd.Flags().Bool("json", false, "Emit machine-readable JSON instead of a table")
	return cmd
}

// smokeFinish turns a smoke run's outcome into the command's: an error is
// returned (usage shown only for a usage mistake), a FAIL row exits 1
// through smokeExit, a clean run is nil.
func smokeFinish(cmd *cobra.Command, anyFail bool, err error) error {
	if err != nil {
		// An interrupt is not a usage mistake: no flag summary after it.
		// Neither is a start that failed or was refused, and with
		// cobra's "Error:" line gone the summary would stand alone
		// above main's one line. A "no" at the --cwd launch question
		// is a decline (exit 3), not a usage mistake either.
		_, coded := exitcode.Code(err) // a model-server failure is not a usage mistake either
		cmd.SilenceUsage = errors.Is(err, errSmokeInterrupted) || isStartError(err) || errors.Is(err, errCancelled) || coded
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
}

// errSmokeInterrupted is what runSmoke returns for a run Ctrl+C cut short.
var errSmokeInterrupted = errors.New("interrupted")

// smokeToolFlags is the validated combination of the flags that decide what
// a row asks the agent to do: the tool-call check, how many dependent reads
// it chains, and the prompt override.
type smokeToolFlags struct {
	toolCheck bool
	hops      int
	prompt    string
}

// validateSmokeToolFlags checks the --tools/--hops/--prompt/--cwd
// combination and returns it. It runs before anything else in a smoke run:
// these are usage errors, known without a model, and a usage error must not
// cost the user an interactive model pick (or a model start) first.
func validateSmokeToolFlags(cmd *cobra.Command) (smokeToolFlags, error) {
	f := smokeToolFlags{
		toolCheck: mustGetBool(cmd, "tools"),
		hops:      mustGetInt(cmd, "hops"),
		prompt:    mustGetString(cmd, "prompt"),
	}
	inPlace := mustGetBool(cmd, "cwd")
	if f.toolCheck && inPlace {
		return f, errors.New("--tools and --cwd are mutually exclusive: the nonce file is written into the row's temporary directory, never your checkout")
	}
	if f.toolCheck && f.prompt != "" {
		return f, errors.New("--tools and --prompt are mutually exclusive: a custom prompt has no nonce to verify")
	}
	// The range is checked first: an out-of-range value is wrong whatever
	// else is on the command line.
	if f.hops < 1 || f.hops > 10 {
		return f, fmt.Errorf("--hops %d: must be between 1 and 10", f.hops)
	}
	// --hops is meaningful only with --tools: on its own it changes nothing.
	// Flag.Changed tells an explicit --hops 1 apart from the default.
	if cmd.Flags().Changed("hops") && !f.toolCheck {
		return f, errors.New("--hops needs --tools")
	}
	return f, nil
}

// smokeSignalCtx is a test seam over signal.NotifyContext, as startSignalCtx
// is for the start step: the context the rows run under, cancelled by Ctrl+C
// or SIGTERM.
var smokeSignalCtx = func() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// runSmoke validates the flags, runs the launch warnings for --cwd, and runs
// the smoke test as a client session of the model server (runSmokeRemote).
// It reads config.toml only, never the registry, so a registry that does not
// load is not its error; with no model server configured it refuses before
// anything starts (Task 18). It returns whether any row FAILed rather than
// exiting, so the caller sets the exit code.
func runSmoke(cmd *cobra.Command, a *app, args []string) (anyFail bool, err error) {
	toolFlags, inPlace, cwd, err := smokePrelude(cmd)
	if err != nil {
		return false, err
	}
	return runSmokeRemote(cmd, a, args, toolFlags, inPlace, cwd)
}

// smokePrelude is what a smoke run checks before any model is resolved: the
// tool flags, and with --cwd the launch warnings for the directory the rows
// run in. It returns the tool flags, whether the rows run in place, and that
// directory.
func smokePrelude(cmd *cobra.Command) (toolFlags smokeToolFlags, inPlace bool, cwd string, err error) {
	// Flag validation comes before the model is resolved: these are usage
	// errors, knowable without a model, and a usage error must not cost the
	// user an interactive model pick first.
	if toolFlags, err = validateSmokeToolFlags(cmd); err != nil {
		return toolFlags, false, "", err
	}

	// The launch warnings run here, for the flag-validation reason above:
	// with --cwd (the root command's persistent flag) every row runs in the
	// caller's directory, so standing in the repository root warns exactly as
	// a --cwd launch does — and proceeds, terminal or not — and a declined
	// question must cost neither an interactive model pick nor a model start.
	// smoke claims the root for no session of its own (rows may run several
	// agents), so nothing is claimed here. Without --cwd every row runs in a
	// fresh temporary git repository (#193): permission checks are off, and a
	// model's stray tool call must not land in a real repo.
	inPlace, _ = cmd.Flags().GetBool("cwd")
	if inPlace {
		if cwd, err = os.Getwd(); err != nil {
			return toolFlags, inPlace, "", err
		}
		t, terr := launchwarn.ForDir(cwd)
		if gerr := confirmLaunch(t, terr, cmd.InOrStdin(), osStderr, stdinTTY()); gerr != nil {
			return toolFlags, inPlace, cwd, gerr // declined: exit 3, like every other no
		}
	}
	return toolFlags, inPlace, cwd, nil
}

// runSmokeDirect is the smoke run on the registry, from before the model
// server was required: it resolves the target from cfg, starts it if idle
// through the local lifecycle, runs every selected agent and prints the
// report, then offers the stop picker. No command reaches it since the flip
// (Task 18); Task 19 deletes it.
func runSmokeDirect(cmd *cobra.Command, a *app, cfg *config.Config, args []string, toolFlags smokeToolFlags, inPlace bool, cwd string) (anyFail bool, err error) {
	var modelID string
	if len(args) > 0 {
		modelID = args[0]
	}
	t, err := resolveSmokeModel(cfg, a.theme, modelID)
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

	timeout, _ := cmd.Flags().GetDuration("timeout")
	// Validate only an explicit value: the unset flag is 0, meaning "pick the
	// default for this model's location".
	if cmd.Flags().Changed("timeout") {
		if err := validateSmokeTimeout(timeout); err != nil {
			return false, err
		}
	}
	timeout = smokeTimeout(cfg, m, timeout)
	jsonOut, _ := cmd.Flags().GetBool("json")

	if t.Start() {
		replace, _ := cmd.Flags().GetBool("replace")
		if err := startModel(cfg, t.Row, replace); err != nil {
			// Marked, text unchanged, so the command can tell a start that
			// failed or was refused from a usage mistake.
			return false, asStartError(err)
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
		// and removes nothing. A cloud target takes this branch too, and
		// gets its route back the same way if config.yaml lost it.
		ensureRouteBeforeLaunch(cfg, m)
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
			smokeStopFlow(cfg, jsonOut)
		}
	}()

	runID := smoke.NewRunID()

	// Ctrl+C or SIGTERM cancels ctx instead of killing wt: the running row's
	// agent is killed with its process group, its directory is removed, and
	// the rows that ran are still reported.
	ctx, stopSignals := smokeSignalCtx()
	defer stopSignals()

	var rows []smoke.RowResult
	rows, interrupted = a.runSmokeRows(ctx, stderr, cfg, m, agentsToRun, toolFlags, runID, inPlace, cwd, &rowDirs,
		func(ctx context.Context, agentName, prompt, sentinel string, rowDir smoke.RowDir, pa smoke.ProfileApplier) smoke.RowResult {
			return smoke.RunRow(ctx, cfg, agentName, m, prompt, sentinel, timeout, rowDir, pa)
		}, nil)
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

// serverGone reports whether a readiness error means the model server is gone
// or refuses this client (unreachable, lost, token refused, clock skew), as
// opposed to a gateway that is merely down: no later row can run either.
func serverGone(err error) bool {
	var (
		unreachable *modelclient.UnreachableError
		lost        *modelclient.LostServerError
		unauth      *modelclient.UnauthorizedError
		skew        *modelclient.SkewError
		notServer   *modelclient.NotModelServerError
	)
	return errors.As(err, &unreachable) || errors.As(err, &lost) || errors.As(err, &unauth) || errors.As(err, &skew) || errors.As(err, &notServer)
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
	loc, err := cfg.ResolveLocation(m)
	if err != nil {
		return smokeCloudTimeout
	}
	return smokeTimeoutAt(loc, 0)
}

// smokeTimeoutAt is smokeTimeout for a location already known: a client
// session has no registry to resolve it from, and the catalog row carries the
// model server's answer (row.Location).
func smokeTimeoutAt(loc config.Location, explicit time.Duration) time.Duration {
	if explicit != 0 {
		return explicit
	}
	if loc == config.LocationLocal {
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
// interactive pick via the shared picker (tui.PickStartModel), unfiltered by agent
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
		} else if reason := catalog.MissingReason(cfg, &snap, modelID); reason != "" {
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

// smokeRowFunc runs one agent's row for the model under test: the registry
// path's smoke.RunRow, or a client session's smoke.RunRowGateway.
type smokeRowFunc func(ctx context.Context, agentName, prompt, sentinel string, rowDir smoke.RowDir, pa smoke.ProfileApplier) smoke.RowResult

// runSmokeRows runs every agent in agentsToRun through run, one row each, and
// returns the rows it ran and whether Ctrl+C cut the run short. It is the part
// of a smoke run the registry path and a client session share: the prompt, the
// profile for the agent x model pair (resolved against cfg), the row's
// directory and its progress lines. Every directory it makes is added to
// *rowDirs for the caller's end-of-run sweep.
func (a *app) runSmokeRows(ctx context.Context, stderr io.Writer, cfg *config.Config, m config.Model, agentsToRun []string, tf smokeToolFlags, runID string, inPlace bool, cwd string, rowDirs *[]string, run smokeRowFunc, halt func() bool) (rows []smoke.RowResult, interrupted bool) {
	// Reuse newApp()'s already-loaded profiles.toml state instead of
	// loading and re-validating the file a second time. If profiles.toml
	// was missing or malformed, degrade gracefully: smoke still runs but
	// without profile application (same best-effort posture as
	// applyProfileForLaunch).
	pp := a.profileState()
	store, loadErr, validateErr := pp.store, pp.loadErr, pp.validateErr
	if loadErr != nil {
		fmt.Fprintf(stderr, "wt: profiles.toml: %v (profiles disabled for smoke)\n", loadErr)
	}
	if validateErr != nil {
		fmt.Fprintf(stderr, "wt: profiles.toml: %v (profiles disabled for smoke)\n", validateErr)
	}

	rows = make([]smoke.RowResult, 0, len(agentsToRun))
	for _, agentName := range agentsToRun {
		prompt, sentinel, planted := tf.prompt, "", map[string]string(nil)
		switch {
		case tf.toolCheck:
			prompt, sentinel, planted = smoke.ToolPrompt(agentName, tf.hops)
		case tf.prompt == "":
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
			rp := profiles.Resolve(store, agentName, cfg, m)
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
				prev := removeRowDir
				removeRowDir = func() { prev(); remove() } // a rerun's directory too
				// What the agent records about this directory in its own
				// state (claude's project entry, pi's sessions) goes with it.
				*rowDirs = append(append(*rowDirs, dir), smoke.AgentStateDirs(agentName, dir)...)
				return dir, nil
			}
		}
		if planted != nil {
			rowDir = smoke.WithFiles(rowDir, planted)
		}
		r := run(ctx, agentName, prompt, sentinel, rowDir, pa)
		removeRowDir()
		logSmokeResult(stderr, r)
		rows = append(rows, r)
		if ctx.Err() != nil {
			interrupted = true
			break
		}
		if halt != nil && halt() {
			break
		}
	}
	return rows, interrupted
}

// smokeStill is what a client smoke run that stopped short says is still true.
const smokeStill = "nothing was run"

// resolveSmokeModelRemote is resolveSmokeModel over the model server's rows.
// It reads the catalog and keeps the candidates (smoke.CandidatesWire over the
// session's agents); a pinned id that is one resolves directly, and one that is
// not is asked of the server (Resolve) so the refusal says why: an id the
// server does not know is its own text (unknown, or the reason a local model
// is missing); a blocked row is its block_reason; any other row is "exists but
// cannot be tested" (no agent can use it). With no id the shared picker opens
// over the candidates (a terminal is needed first, P5). The second result is
// the catalog's rows, which a gateway syncer (pi) lists. Server errors carry
// remoteExit and the still clause; a refusal is a plain error (exit 1).
func resolveSmokeModelRemote(ctx context.Context, ms *modelSession, cfg *config.Config, theme themes.Theme, modelID string) (smokeTarget, []modelapi.Row, error) {
	if modelID == "" && !stdinTTY() {
		return smokeTarget{}, nil, fmt.Errorf("wt smoke needs a TTY to list models; pass a model id directly (wt smoke <provider>/<name>)")
	}
	cat, err := ms.client.Catalog(ctx)
	if err != nil {
		return smokeTarget{}, nil, remoteExit(modelclient.Still(err, smokeStill))
	}
	cands := smoke.CandidatesWire(cat.Rows, cfg.Agents)
	target := func(id string) (smokeTarget, bool) {
		for _, c := range cands {
			if c.Row.Model.ID == id {
				return smokeTarget{Row: c.Row, Agents: c.Agents}, true
			}
		}
		return smokeTarget{}, false
	}
	if modelID != "" {
		if t, ok := target(modelID); ok {
			return t, cat.Rows, nil
		}
		row, rerr := ms.client.Resolve(ctx, modelID)
		var unknown *modelclient.UnknownModelError
		switch {
		case errors.As(rerr, &unknown):
			return smokeTarget{}, nil, errors.New(unknown.Message)
		case rerr != nil:
			return smokeTarget{}, nil, remoteExit(modelclient.Still(rerr, smokeStill))
		case row.Action == modelapi.ActionBlock:
			reason := row.BlockReason
			if reason == "" {
				reason = fmt.Sprintf("model %q is blocked", row.ID)
			}
			return smokeTarget{}, nil, errors.New(reason)
		}
		return smokeTarget{}, nil, fmt.Errorf(
			"model %q cannot be smoke-tested right now (no agent supports it, or its route is not set up — check `wt litellm status` on the model server host)", modelID)
	}
	if len(cands) == 0 {
		return smokeTarget{}, nil, fmt.Errorf("the model server has no models to test; add one on the server host with wt model add, or pull a local one there")
	}
	models := make([]config.Model, len(cands))
	for i, c := range cands {
		models[i] = c.Row.Model
	}
	m, ok, perr := pickModelTUI(nil, models, theme)
	if perr != nil {
		return smokeTarget{}, nil, perr
	}
	if !ok {
		return smokeTarget{}, nil, fmt.Errorf("model selection canceled")
	}
	if t, ok := target(m.ID); ok {
		return t, cat.Rows, nil
	}
	return smokeTarget{}, nil, fmt.Errorf("unknown model %q", m.ID)
}

// runSmokeRemote is runSmoke on a client session (a model server is
// configured). It reads config.toml only and talks to the model server. In
// order:
//
//  1. resolve the model (resolveSmokeModelRemote), the agents to run and the
//     timeout (the row's location decides the default);
//  2. register ONE client session with the model before any start, so the
//     server's idle scan counts it; a failed register is one note and the run
//     goes on;
//  3. run a start job only when catalog.NeedsStart says so (a start row, or a
//     running local model the gateway is known not to list; an unknown route
//     never starts, R7);
//  4. run every row through the gateway (smoke.RunRowGateway: the endpoint
//     origin and token, a wait for the gateway before each row, one rerun
//     after a 503), then end the session on every path (the exit stop prompt
//     of the registry path does not exist here: the model server stops what
//     smoke started once its idle grace passes);
//  5. print the report; on a terminal, when smoke started a model, one note
//     says so.
func runSmokeRemote(cmd *cobra.Command, a *app, args []string, tf smokeToolFlags, inPlace bool, cwd string) (anyFail bool, err error) {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	cfg, err := a.sessionCfg()
	if err != nil {
		return false, remoteExit(modelclient.Still(err, smokeStill))
	}
	ms, err := a.openModels(ctx, smokeStill)
	if err != nil {
		return false, remoteExit(err) // openModels' errors carry the still clause
	}
	var modelID string
	if len(args) > 0 {
		modelID = args[0]
	}
	t, catRows, err := resolveSmokeModelRemote(ctx, ms, cfg, a.theme, modelID)
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
	timeout, _ := cmd.Flags().GetDuration("timeout")
	if cmd.Flags().Changed("timeout") {
		if err := validateSmokeTimeout(timeout); err != nil {
			return false, err
		}
	}
	timeout = smokeTimeoutAt(t.Row.Location, timeout)
	jsonOut, _ := cmd.Flags().GetBool("json")
	stderr := cmd.ErrOrStderr()

	// The client session holds the model from before its start to the end of
	// the run. end runs once, on a context no cancel reaches, and never fails
	// the run; after a register the server refused, its DELETE is refused too,
	// which is not news.
	note := func(s string) { fmt.Fprintf(osStderr, "wt: note: %s\n", s) }
	sess := ms.client.NewSession([]string{m.ID}, modelclient.SessionOptions{Interval: remoteBeatInterval, Note: note})
	registered := true
	if rerr := sess.Start(ctx); rerr != nil {
		registered = !registerRefused(rerr)
		note(fmt.Sprintf("the model server did not register this client session (%s) — the run goes on, but the server may stop %s once it is idle", rerr, m.ID))
	}
	var endOnce sync.Once
	end := func() {
		endOnce.Do(func() {
			if eerr := sess.End(context.WithoutCancel(ctx)); eerr != nil && registered {
				note(fmt.Sprintf("could not end the client session on the model server (%s) — it forgets it within a minute", eerr))
			}
		})
	}
	defer end()

	started := false
	if t.Row.Wire != nil && catalog.NeedsStart(*t.Row.Wire) {
		replace, _ := cmd.Flags().GetBool("replace")
		if serr := startRemote(ctx, ms, *t.Row.Wire, string(othersMode), replace); serr != nil {
			// Nothing will use the model: take it out of the client session
			// before it ends.
			_ = sess.Remove(context.WithoutCancel(ctx), m.ID)
			end()
			return false, remoteExit(stillUnlessTyped(serr, smokeStill))
		}
		started = true
	}

	// Every row's directory, swept once more when the run ends (see runSmoke).
	var rowDirs []string
	defer func() {
		for _, d := range smoke.SweepRowDirs(rowDirs) {
			fmt.Fprintf(stderr, "wt: could not remove the smoke directory %s\n", d)
		}
	}()

	runID := smoke.NewRunID()
	gw := agents.Gateway{Origin: ms.ep.Origin(), Token: ms.ep.Token}
	gwModels := catalog.NonNativeModels(catRows)
	sctx, stopSignals := smokeSignalCtx()
	defer stopSignals()
	// Two things end a run early. A gateway that stayed down for a row's whole
	// wait makes every later row SKIP at once (each would otherwise wait its
	// own 3 minutes), and a model server that is gone or refuses this client
	// stops the run: the rows that ran are reported, the cause is named once.
	var gwDown bool
	var fatal error
	ready := func(ctx context.Context) error {
		err := ms.client.WaitGatewayReady(ctx)
		if serverGone(err) && fatal == nil {
			fatal = err
		}
		return err
	}
	rows, interrupted := a.runSmokeRows(sctx, stderr, cfg, m, agentsToRun, tf, runID, inPlace, cwd, &rowDirs,
		func(ctx context.Context, agentName, prompt, sentinel string, rowDir smoke.RowDir, pa smoke.ProfileApplier) smoke.RowResult {
			if gwDown {
				return smoke.RowResult{Agent: agentName, Model: m.ID, Status: smoke.StatusSkip, Err: errors.New(smoke.GatewayDownReason)}
			}
			r := smoke.RunRowGateway(ctx, gw, gwModels, agentName, m, prompt, sentinel, timeout, rowDir, pa, ready)
			if r.Status == smoke.StatusSkip && r.Err != nil && r.Err.Error() == smoke.GatewayDownReason {
				gwDown = true
			}
			return r
		}, func() bool { return fatal != nil })
	if fatal != nil && len(rows) > 0 && rows[len(rows)-1].Err != nil && errors.Is(rows[len(rows)-1].Err, fatal) {
		rows = rows[:len(rows)-1] // the row that found the server gone says nothing about the model
	}
	// The rows are over: the model is no longer in use. Restore the default
	// handler too: a second Ctrl+C during the report kills wt.
	end()
	stopSignals()

	if jsonOut {
		anyFail, err = printSmokeJSON(cmd.OutOrStdout(), runID, m.ID, rows)
	} else {
		anyFail, err = printSmokeHuman(cmd.OutOrStdout(), runID, m.ID, rows)
	}
	if !jsonOut && stdinTTY() {
		// The server's auto-stop reaches any local model a client session
		// registered, so the note covers a model smoke only used, too.
		switch {
		case started:
			fmt.Fprintf(stderr, "wt: note: smoke started %s; the model server stops it after its idle grace unless a client session uses it\n", cleanup.Flatten(m.ID))
		case t.Row.Location == config.LocationLocal:
			fmt.Fprintf(stderr, "wt: note: smoke used %s, which was already running; the model server stops it after its idle grace unless a client session uses it\n", cleanup.Flatten(m.ID))
		}
	}
	if fatal != nil && err == nil {
		err = remoteExit(modelclient.Still(fatal, "the remaining rows did not run"))
	}
	if interrupted && err == nil {
		err = errSmokeInterrupted
	}
	return anyFail, err
}
