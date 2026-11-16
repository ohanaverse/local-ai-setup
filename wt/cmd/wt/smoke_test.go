// Tests for `wt smoke`'s CLI-layer logic: model resolution, --only
// filtering, the interactive picker, and human/JSON rendering. Row
// execution itself is internal/smoke's responsibility (tested there via
// the buildAndRun seam) — these tests work entirely with synthetic
// smoke.RowResult values, no real subprocess exec.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/exitcode"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
	"github.com/ohanaverse/local-ai-setup/wt/internal/smoke"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tui"
	"github.com/spf13/cobra"
)

// runSmokeCmd runs `wt smoke` with args, as the command, on a. It returns
// stdout and stderr apart, and the command's error.
func runSmokeCmd(a *app, args ...string) (stdout, stderr string, err error) {
	return runSmokeOn(smokeCmd(a), args...)
}

// runSmokeOn runs cmd with args and returns stdout and stderr apart, and its
// error.
func runSmokeOn(cmd *cobra.Command, args ...string) (stdout, stderr string, err error) {
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err = cmd.Execute()
	return out.String(), errOut.String(), err
}

// TestFilterOnlyAgentsEmpty asserts an empty --only returns the eligible
// list unchanged.
func TestFilterOnlyAgentsEmpty(t *testing.T) {
	got, err := filterOnlyAgents([]string{"claude", "codex"}, "")
	if err != nil {
		t.Fatalf("filterOnlyAgents: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %v, want unchanged 2-element list", got)
	}
}

// TestFilterOnlyAgentsSubset asserts a valid subset is returned as given.
func TestFilterOnlyAgentsSubset(t *testing.T) {
	got, err := filterOnlyAgents([]string{"claude", "codex"}, "codex")
	if err != nil {
		t.Fatalf("filterOnlyAgents: %v", err)
	}
	if len(got) != 1 || got[0] != "codex" {
		t.Fatalf("got %v, want [codex]", got)
	}
}

// TestFilterOnlyAgentsUnknown asserts an agent outside the eligible set
// fails fast, before any row would run.
func TestFilterOnlyAgentsUnknown(t *testing.T) {
	_, err := filterOnlyAgents([]string{"claude"}, "codex")
	if err == nil || !strings.Contains(err.Error(), "not eligible for this model") {
		t.Fatalf("err = %v, want not-eligible error", err)
	}
}

// TestPrintSmokeHumanReportsFailAndDetail asserts a FAIL row gets an
// expanded detail block while PASS/SKIP stay single-line, and the
// function reports fail=true so the caller can set a non-zero exit code.
func TestPrintSmokeHumanReportsFailAndDetail(t *testing.T) {
	rows := []smoke.RowResult{
		{Agent: "claude", Status: smoke.StatusPass, Duration: 2 * time.Second},
		{Agent: "codex", Status: smoke.StatusFail, Duration: time.Second, Command: "codex exec ...", ExitCode: 1, Output: "boom"},
		{Agent: "copilot", Status: smoke.StatusSkip, Duration: 0},
	}
	var buf bytes.Buffer
	fail, err := printSmokeHuman(&buf, "run-1", "ollama/x", rows)
	if err != nil {
		t.Fatalf("printSmokeHuman: %v", err)
	}
	if !fail {
		t.Fatal("fail = false, want true (one FAIL row)")
	}
	out := buf.String()
	if !strings.Contains(out, "[FAIL ] codex") || !strings.Contains(out, "boom") {
		t.Fatalf("output missing FAIL detail: %q", out)
	}
	if !strings.Contains(out, "PASS: 1 FAIL: 1 SKIP: 1") {
		t.Fatalf("output missing summary counts: %q", out)
	}
}

// TestPrintSmokeJSONSchema asserts --json emits the documented contract
// (run_id/model/rows) and reports fail correctly for downstream callers.
func TestPrintSmokeJSONSchema(t *testing.T) {
	rows := []smoke.RowResult{
		{Agent: "claude", Model: "ollama/x", Status: smoke.StatusPass, Duration: 1500 * time.Millisecond},
	}
	var buf bytes.Buffer
	fail, err := printSmokeJSON(&buf, "run-1", "ollama/x", rows)
	if err != nil {
		t.Fatalf("printSmokeJSON: %v", err)
	}
	if fail {
		t.Fatal("fail = true, want false (no FAIL rows)")
	}
	var report smokeJSONReport
	if err := json.Unmarshal(buf.Bytes(), &report); err != nil {
		t.Fatalf("invalid JSON: %v (%s)", err, buf.String())
	}
	if report.RunID != "run-1" || report.Model != "ollama/x" || len(report.Rows) != 1 {
		t.Fatalf("report = %+v", report)
	}
	if report.Rows[0].DurationMS != 1500 {
		t.Fatalf("DurationMS = %d, want 1500", report.Rows[0].DurationMS)
	}
}

// TestValidateSmokeTimeoutRejectsNonPositive asserts --timeout 0 (and a
// negative duration) is rejected with a clear error before any row runs —
// without this check, a non-positive timeout makes every agent get killed
// the instant it starts and reported as "timed out after 0s", which reads
// as every agent being broken rather than a bad flag value.
func TestValidateSmokeTimeoutRejectsNonPositive(t *testing.T) {
	for _, d := range []time.Duration{0, -1 * time.Second} {
		err := validateSmokeTimeout(d)
		if err == nil || !strings.Contains(err.Error(), "--timeout must be positive") {
			t.Fatalf("validateSmokeTimeout(%s) = %v, want a positive-timeout error", d, err)
		}
	}
}

// TestValidateSmokeTimeoutAcceptsPositive asserts an ordinary positive
// timeout passes validation unchanged.
func TestValidateSmokeTimeoutAcceptsPositive(t *testing.T) {
	if err := validateSmokeTimeout(60 * time.Second); err != nil {
		t.Fatalf("validateSmokeTimeout(60s) = %v, want nil", err)
	}
}

// TestLogSmokeStartWritesTimestampedLine asserts the pre-run stderr line
// names the agent and model and is timestamped from the smokeNow seam, so a
// hung agent is visible (no output since its start line) instead of silence
// until the final report.
func TestLogSmokeStartWritesTimestampedLine(t *testing.T) {
	old := smokeNow
	smokeNow = func() time.Time { return time.Date(2026, 1, 1, 15, 4, 5, 0, time.UTC) }
	defer func() { smokeNow = old }()

	var buf bytes.Buffer
	logSmokeStart(&buf, "claude", "ollama/x")
	got := buf.String()
	if !strings.HasPrefix(got, "[15:04:05]") {
		t.Fatalf("output = %q, want a 15:04:05 timestamp prefix", got)
	}
	if !strings.Contains(got, "claude") || !strings.Contains(got, "ollama/x") || !strings.Contains(got, "starting") {
		t.Fatalf("output = %q, want agent, model, and starting", got)
	}
}

// TestLogSmokeResultFail asserts a FAIL row's live stderr line carries the
// same command/exit-code/error/output detail as the final report's FAIL
// block — a FAIL must be fully debuggable the moment it happens, without
// waiting for the run to finish.
func TestLogSmokeResultFail(t *testing.T) {
	r := smoke.RowResult{
		Agent: "codex", Model: "ollama/x", Status: smoke.StatusFail,
		Duration: time.Second, Command: "codex exec ...", ExitCode: 1,
		Output: "boom", Err: fmt.Errorf("exit code 1"),
	}
	var buf bytes.Buffer
	logSmokeResult(&buf, r)
	got := buf.String()
	for _, want := range []string{"FAIL", "codex exec ...", "exit code: 1", "exit code 1", "boom"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output = %q, missing %q", got, want)
		}
	}
}

// TestLogSmokeResultSkipIncludesReason asserts a SKIP row's live stderr line
// states why (e.g. agent not installed) instead of a bare status, matching
// the final report's r.Err (SKIP rows never get a detail block there
// either).
func TestLogSmokeResultSkipIncludesReason(t *testing.T) {
	r := smoke.RowResult{
		Agent: "copilot", Model: "ollama/x", Status: smoke.StatusSkip,
		Err: fmt.Errorf("agent copilot not installed"),
	}
	var buf bytes.Buffer
	logSmokeResult(&buf, r)
	got := buf.String()
	if !strings.Contains(got, "SKIP") || !strings.Contains(got, "agent copilot not installed") {
		t.Fatalf("output = %q, want SKIP and its reason", got)
	}
}

// TestLogSmokeResultPassIsSingleLine asserts a PASS row's live stderr line
// has no trailing detail block, mirroring the final report's PASS rendering.
func TestLogSmokeResultPassIsSingleLine(t *testing.T) {
	r := smoke.RowResult{Agent: "claude", Model: "ollama/x", Status: smoke.StatusPass, Duration: 2 * time.Second}
	var buf bytes.Buffer
	logSmokeResult(&buf, r)
	got := buf.String()
	if strings.Count(got, "\n") != 1 {
		t.Fatalf("output = %q, want exactly one line", got)
	}
	if !strings.Contains(got, "PASS") {
		t.Fatalf("output = %q, want PASS", got)
	}
}

// TestSmokeCmdFlags asserts the command's flags are registered with the
// documented defaults — a cheap guard against a typo'd flag name/default
// silently breaking --timeout or --json.
func TestSmokeCmdFlags(t *testing.T) {
	cmd := smokeCmd(&app{hostSet: true, hostCfg: &config.Config{}})
	if cmd.Use != "smoke [model-id]" {
		t.Fatalf("Use = %q", cmd.Use)
	}
	// The flag default is 0 = "unset": the effective default depends on the
	// model's location and is resolved by smokeTimeout.
	timeout, err := cmd.Flags().GetDuration("timeout")
	if err != nil || timeout != 0 {
		t.Fatalf("timeout default = %v, err = %v", timeout, err)
	}
	if v, _ := cmd.Flags().GetBool("json"); v {
		t.Fatal("json flag default should be false")
	}
}

// TestSmokePickerSkipsAgentlessRouteCheck verifies wt smoke's interactive
// picker is the route-skipping one (tui.PickStartModel). smoke.Candidates has
// already admitted each row using a real agent's protocols; the picker resolves
// routes with no agent, which can fail where the agent-specific route succeeds
// (a provider with no direct base_url that a protocol-mismatched agent is
// forced through LiteLLM for). With route checking on, the picker would block a
// row `wt smoke <id>` accepts.
func TestSmokePickerSkipsAgentlessRouteCheck(t *testing.T) {
	got := reflect.ValueOf(pickModelTUI).Pointer()
	want := reflect.ValueOf(tui.PickStartModel).Pointer()
	if got != want {
		t.Fatalf("pickModelTUI is not tui.PickStartModel: smoke's picker would re-check routes without an agent and block rows Candidates admitted")
	}
}

// TestSmokeCmdRunsRowsInAFreshGitRepo pins #193: smoke runs each agent with
// permission checks off, so by default a row runs in a fresh temporary git
// repository — never the caller's directory, where a model's stray tool call
// has written junk into a real repo — and the directory is removed afterwards.
func TestSmokeCmdRunsRowsInAFreshGitRepo(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir, wasGitRepo, existsAfter := smokeRowDirs(t, false)
	if dir == wd {
		t.Fatalf("the row ran in the working directory %s", wd)
	}
	if rel, rerr := filepath.Rel(wd, dir); rerr == nil && !strings.HasPrefix(rel, "..") {
		t.Fatalf("the row's directory %s is inside the working directory %s", dir, wd)
	}
	if !wasGitRepo {
		t.Errorf("the row's directory %s was not a git repository: codex refuses to run outside one", dir)
	}
	if existsAfter {
		t.Errorf("the row's directory %s was left behind", dir)
	}
}

// TestSmokeCmdCwdFlagRunsInPlace pins the opt-out: with --cwd a row runs in
// the caller's directory, as every row did before #193, and smoke neither
// creates nor removes anything.
func TestSmokeCmdCwdFlagRunsInPlace(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir, _, existsAfter := smokeRowDirs(t, true)
	if dir != wd {
		t.Fatalf("with --cwd the row ran in %s, want the working directory %s", dir, wd)
	}
	if !existsAfter {
		t.Fatal("smoke removed the working directory")
	}
}

// TestSmokeCmdCwdAtRootWarnsAndProceeds checks wt smoke --cwd standing in a
// repository root warns and proceeds, terminal or not: the root warning is
// printed, no terminal gets no question (a question would read a pipe), and a
// terminal is asked the one default-yes question — enter goes on to the model
// resolution, a "no" ends the run with exit 3 (declined, C9) without
// reaching model resolution and without claiming or creating anything. Why:
// the root gate and its smoke's-remedies refusal are gone — smoke runs where
// it was told to run, and the warning is the only thing saying the root is
// shared; the decline must stay a clean refusal, not a half-started run.
func TestSmokeCmdCwdAtRootWarnsAndProceeds(t *testing.T) {
	for _, tc := range []struct {
		name    string
		tty     bool
		in      io.Reader
		wantErr string // the error text: "unknown model" when the run proceeds and fails there; the decline when it ends at the question
	}{
		{name: "no terminal", tty: false, in: failReader{t}, wantErr: "unknown model"},
		{name: "terminal, enter", tty: true, in: strings.NewReader("\n"), wantErr: "unknown model"},
		{name: "terminal, no", tty: true, in: strings.NewReader("n\n"), wantErr: "approval missing: declined; nothing was launched"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := smokeRig(t, func(string, string, string, *func() error) smoke.ExecOutcome { return smoke.StubOutcome("ok", 0) })
			stubStdinTTY(t, tc.tty)
			root := sharedRootRepo(t)
			t.Chdir(root)
			notes := captureNotes(t)

			cmd, _, _ := smokeRootCmd(t, r, tc.in, "--cwd", "ollama/does-not-exist")
			err := cmd.Execute()
			if tc.name == "terminal, no" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) || exitcode.Of(err) != 3 {
					t.Fatalf("err = %v (exit %d); want a declined run to end with exit 3, reaching no model resolution", err, exitcode.Of(err))
				}
				if fileExists(ownClaim(root)) {
					t.Error("a root claim was written although the question was declined")
				}
				if strings.Contains(notes.String(), "creating") {
					t.Errorf("notes = %q; a declined run must create nothing", notes.String())
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v; want the run to proceed to the model resolution", err)
			}
			s := notes.String()
			if !strings.Contains(s, "is the repository root") {
				t.Errorf("notes = %q; want the root warning", s)
			}
			if got := strings.Contains(s, "continue?"); got != tc.tty {
				t.Errorf("notes = %q; asked=%v, want %v (a question is asked only on a terminal)", s, got, tc.tty)
			}
		})
	}
}

// TestSmokeInheritsTheRootCwdFlag pins the wiring the tests above assume:
// smokeRowDirs registers a --cwd flag of its own, because a bare smokeCmd has
// no parent, so on their own they would still pass if the root command lost
// the flag — and `wt smoke --cwd` would then be an unknown-flag error.
func TestSmokeInheritsTheRootCwdFlag(t *testing.T) {
	sc, _, err := rootCmd().Find([]string{"smoke"})
	if err != nil || sc.Name() != "smoke" {
		t.Fatalf("no smoke command under the root command (err = %v)", err)
	}
	f := sc.InheritedFlags().Lookup("cwd")
	if f == nil {
		t.Fatal("wt smoke does not inherit --cwd from the root command")
	}
	if f.Value.Type() != "bool" {
		t.Fatalf("--cwd is a %s flag, want bool: runSmoke reads it with GetBool", f.Value.Type())
	}
	if !strings.Contains(f.Usage, "current directory") {
		t.Errorf("--cwd usage %q does not say what it does for wt smoke (runs in the current directory)", f.Usage)
	}
}

// smokeRig is a fake model server holding one cloud row, openrouter/a, that
// every model-driven agent can run, with each row's agent run replaced by fn
// (the agent's name, the prompt, the row's directory and its cleanup). No
// agent process runs and no registry is read.
func smokeRig(t *testing.T, fn func(agentName, prompt, cwd string, cleanup *func() error) smoke.ExecOutcome) *remoteRig {
	t.Helper()
	r := newRemoteRig(t, wireRow("openrouter/a", modelapi.ActionLaunch, 1))
	t.Cleanup(smoke.SetGatewayRunForTest(func(_ agents.Gateway, _ []config.Model, agentName string, _ config.Model, prompt, cwd string, _ time.Duration, _ smoke.ProfileApplier, cleanup *func() error) smoke.ExecOutcome {
		*cleanup = func() error { return nil }
		return fn(agentName, prompt, cwd, cleanup)
	}))
	oldExit := smokeExit
	t.Cleanup(func() { smokeExit = oldExit })
	smokeExit = func(int) {}
	return r
}

// smokeRootCmd builds a bare smoke command on r's app: a --cwd flag of its own
// (a bare smokeCmd has no parent to inherit it from), captured output, and the
// caller's stdin choice.
func smokeRootCmd(t *testing.T, r *remoteRig, stdin io.Reader, args ...string) (*cobra.Command, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	cmd := smokeCmd(r.a)
	cmd.Flags().Bool("cwd", false, "")
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetIn(stdin)
	cmd.SetArgs(args)
	return cmd, &out, &errOut
}

// smokeRowDirs runs `wt smoke` on the fake's cloud model with a stubbed agent
// run and returns the directory the (single) row's agent was given, whether
// it was a git repository while the row ran, and whether it still exists once
// the command has returned. cwdFlag passes --cwd.
func smokeRowDirs(t *testing.T, cwdFlag bool) (dir string, wasGitRepo, existsAfter bool) {
	t.Helper()
	r := smokeRig(t, func(_, _, cwd string, _ *func() error) smoke.ExecOutcome {
		dir = cwd
		_, err := os.Stat(filepath.Join(cwd, ".git"))
		wasGitRepo = err == nil
		// Exit 0 with a --prompt override: exit-code-only, so the row passes.
		return smoke.StubOutcome("ok", 0)
	})
	args := []string{"openrouter/a", "--only", "claude", "--prompt", "say ok"}
	if cwdFlag {
		args = append(args, "--cwd")
	}
	cmd, _, _ := smokeRootCmd(t, r, strings.NewReader(""), args...)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if dir == "" {
		t.Fatal("no row ran")
	}
	_, err := os.Stat(dir)
	return dir, wasGitRepo, err == nil
}

// TestSmokeCmdExplicitZeroTimeoutRejected verifies that passing --timeout 0
// (or a negative value) through the command fails validation before anything is
// started, even though an unset flag also defaults to 0. Without the
// Changed("timeout") guard, 0 would silently mean "use the location default",
// or reach the row timer and fail every row immediately.
func TestSmokeCmdExplicitZeroTimeoutRejected(t *testing.T) {
	for _, val := range []string{"0", "-5s"} {
		r := newRemoteRig(t, localRow("omlx/q", modelapi.StateStopped, modelapi.ActionStart, nil))
		stubGatewayRows(t, r, passRows)
		run := smokeOnFake(t, r, "omlx/q", "--only", "claude", "--timeout", val)
		if run.err == nil || !strings.Contains(run.err.Error(), "timeout") {
			t.Fatalf("--timeout %s: err = %v, want a timeout validation error", val, run.err)
		}
		if n := r.f.count("POST"); n != 0 {
			t.Fatalf("--timeout %s: the model server was asked to start or register (%d POSTs) despite an invalid timeout", val, n)
		}
	}
}

// TestSmokeCmdAppliesLocalModelProfile verifies that wt smoke resolves and
// applies a local-model profile for a matching agent×model pair: the app's
// precomputed profile state (a.profiles) holds a model-tier profile for
// claude, the way newApp() would after loading profiles.toml, and smoke runs
// against the model it matches. The row's agent run is stubbed, and it is
// handed the profile applier. The run completes without error, proving the
// profile resolution and application path doesn't crash.
func TestSmokeCmdAppliesLocalModelProfile(t *testing.T) {
	r := newRemoteRig(t, wireRow("openrouter/a", modelapi.ActionLaunch, 1))
	r.a.profiles = profiles.Store{
		Enabled: true,
		Profiles: []profiles.Profile{{
			Agent: "claude", Match: "model", Model: "openrouter/a",
			Env:  map[string]string{"WT_PROFILE_TEST": "applied"},
			Args: []string{"--profile-test"},
		}},
	}
	var rows []string
	appliers := 0
	t.Cleanup(smoke.SetGatewayRunForTest(func(_ agents.Gateway, _ []config.Model, agentName string, _ config.Model, _, _ string, _ time.Duration, pa smoke.ProfileApplier, cleanup *func() error) smoke.ExecOutcome {
		rows = append(rows, agentName)
		if pa != nil {
			appliers++
		}
		*cleanup = func() error { return nil }
		return smoke.StubOutcome("ok", 0)
	}))
	run := smokeOnFake(t, r, "openrouter/a", "--only", "claude")
	if run.err != nil {
		t.Fatalf("smoke executed with error: %v\n%s", run.err, run.stderr)
	}
	if strings.Join(rows, ",") != "claude" || appliers != 1 {
		t.Fatalf("rows = %v, appliers = %d; want one claude row handed the profile applier", rows, appliers)
	}
}

// TestSmokeCmdRowDirFailureFailsTheRow pins what a row directory that cannot
// be created costs: that row FAILs with the reason and the report is still
// printed. Returning the error instead would discard every row that had
// already run — minutes of a local model's time — and leave a --json caller
// with no report at all.
func TestSmokeCmdRowDirFailureFailsTheRow(t *testing.T) {
	ran := false
	r := smokeRig(t, func(string, string, string, *func() error) smoke.ExecOutcome {
		ran = true
		return smoke.StubOutcome("ok", 0)
	})
	exitCode := 0
	smokeExit = func(code int) { exitCode = code }

	// Set last: a temp root that does not exist makes os.MkdirTemp fail.
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
	out, _, err := runSmokeCmd(r.a, "openrouter/a", "--only", "claude", "--json")
	if err != nil {
		t.Fatalf("smoke returned %v instead of reporting the row", err)
	}
	if ran {
		t.Fatal("the agent ran although its row directory could not be created")
	}
	var report smokeJSONReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("no JSON report: %v\n%s", err, out)
	}
	if len(report.Rows) != 1 || report.Rows[0].Status != string(smoke.StatusFail) || !strings.Contains(report.Rows[0].Error, "smoke directory") {
		t.Fatalf("rows = %+v, want one FAIL row naming the smoke directory", report.Rows)
	}
	if exitCode != 1 {
		t.Fatalf("exit code = %d, want 1", exitCode)
	}
}

// TestSmokeCmdInterruptEndsTheRun pins Ctrl+C during a row: the row's
// directory is still removed, no further agent runs, the rows that did run
// are still reported, and the command fails without a usage block. Before,
// the signal killed wt outright: no cleanup ran and the row's directory
// stayed in the temp dir.
func TestSmokeCmdInterruptEndsTheRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var dirs []string
	r := smokeRig(t, func(_, _, cwd string, _ *func() error) smoke.ExecOutcome {
		dirs = append(dirs, cwd)
		cancel() // Ctrl+C lands while the first agent runs
		return smoke.StubOutcome("ok", 0)
	})
	oldSig := smokeSignalCtx
	t.Cleanup(func() { smokeSignalCtx = oldSig })
	smokeSignalCtx = func() (context.Context, context.CancelFunc) { return ctx, cancel }

	out, errOut, err := runSmokeCmd(r.a, "openrouter/a", "--only", "claude,pi", "--prompt", "say ok")
	if err == nil || !strings.Contains(err.Error(), "interrupted") {
		t.Fatalf("err = %v, want an interrupted error", err)
	}
	if len(dirs) != 1 {
		t.Fatalf("%d agents ran, want 1: nothing may start after the interrupt", len(dirs))
	}
	if _, serr := os.Stat(dirs[0]); serr == nil {
		t.Errorf("the row's directory %s was left behind", dirs[0])
	}
	if !strings.Contains(out, "claude") && !strings.Contains(out, "pi") {
		t.Errorf("the row that ran was not reported:\n%s", out)
	}
	// An interrupt is not a usage mistake: the flag summary must not follow it.
	// (cobra prints it to the command's out writer when one is set.)
	if printed := out + errOut; strings.Contains(printed, "Usage:") {
		t.Errorf("the usage text was printed after an interrupt:\n%s", printed)
	}
}

// TestSmokeCmdRemovesTheAgentStateDir pins that a row leaves nothing behind
// in the agent's own state either: claude registers the row's temporary
// directory as a project under ~/.claude/projects, and one such entry per row
// — transcript and memory directory included — otherwise stays forever.
func TestSmokeCmdRemovesTheAgentStateDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var stateDir string
	r := smokeRig(t, func(_, _, cwd string, _ *func() error) smoke.ExecOutcome {
		// What claude does on a real run, keyed on the directory it sees.
		real, err := filepath.EvalSymlinks(cwd)
		if err != nil {
			t.Error(err)
		}
		stateDir = claudeStateDir(t, home, real)
		if err := os.MkdirAll(filepath.Join(stateDir, "memory"), 0o755); err != nil {
			t.Error(err)
		}
		return smoke.StubOutcome("ok", 0)
	})

	if _, errOut, err := runSmokeCmd(r.a, "openrouter/a", "--only", "claude", "--prompt", "say ok"); err != nil {
		t.Fatalf("%v\n%s", err, errOut)
	}
	if stateDir == "" {
		t.Fatal("no row ran")
	}
	if _, err := os.Stat(stateDir); err == nil {
		t.Fatalf("claude's project directory %s was left behind", stateDir)
	}
}

// TestSmokeToolFlagConflicts pins the usage errors the --tools/--hops flags
// must refuse before anything runs: --tools needs a temporary directory for
// its nonce file and a prompt it controls, and --hops is meaningless without
// it. It matters because a conflict caught only after the model picker opens
// costs the user an interactive round trip before telling them the run could
// never have worked.
func TestSmokeToolFlagConflicts(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"tools with cwd", []string{"--tools", "--cwd"}, "mutually exclusive"},
		{"tools with prompt", []string{"--tools", "--prompt", "say ok"}, "mutually exclusive"},
		{"hops without tools", []string{"--hops", "2"}, "needs --tools"},
		{"hops out of range", []string{"--tools", "--hops", "11"}, "between 1 and 10"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := smokeRig(t, func(string, string, string, *func() error) smoke.ExecOutcome { return smoke.StubOutcome("ok", 0) })
			cmd, _, _ := smokeRootCmd(t, r, strings.NewReader(""), c.args...)
			err := cmd.Execute()
			if err == nil {
				t.Fatalf("err = nil, want a %q error", c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want it to mention %q", err, c.want)
			}
			if n := len(r.f.reqs()); n != 0 {
				t.Errorf("the model server got %d requests before the usage error: %v", n, r.f.reqs())
			}
		})
	}
}

// TestSmokeToolsPlantsTheNonceInTheRowDirectory pins the --tools wiring
// end to end: the row's temporary directory holds the nonce file the prompt
// names, and the prompt never carries the nonce itself. It matters because
// the whole check rests on those two facts — the nonce in the file is what
// forces a tool call, and its absence from the prompt is what stops an agent
// that echoes its prompt from passing without reading anything.
func TestSmokeToolsPlantsTheNonceInTheRowDirectory(t *testing.T) {
	var prompt, dir string
	var files map[string]string
	r := smokeRig(t, func(_, p, cwd string, _ *func() error) smoke.ExecOutcome {
		prompt, dir = p, cwd
		files = map[string]string{}
		entries, err := os.ReadDir(cwd)
		if err != nil {
			t.Errorf("reading the row directory: %v", err)
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			b, err := os.ReadFile(filepath.Join(cwd, e.Name()))
			if err != nil {
				t.Errorf("reading %s: %v", e.Name(), err)
				continue
			}
			files[e.Name()] = string(b)
		}
		return smoke.StubOutcome("ok", 0)
	})

	if _, errOut, err := runSmokeCmd(r.a, "openrouter/a", "--only", "claude", "--tools"); err != nil {
		t.Fatalf("%v\n%s", err, errOut)
	}
	if dir == "" {
		t.Fatal("no row ran")
	}
	content, ok := files["smoke-nonce.txt"]
	if !ok {
		t.Fatalf("the row directory holds %v, want smoke-nonce.txt", files)
	}
	if !strings.Contains(prompt, "smoke-nonce.txt") {
		t.Errorf("the prompt %q does not name the nonce file", prompt)
	}
	if strings.Contains(prompt, strings.TrimSpace(content)) {
		t.Errorf("the prompt %q carries the nonce %q: the check must require a tool call", prompt, content)
	}
}
