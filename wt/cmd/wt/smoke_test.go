// Tests for `wt smoke`'s CLI-layer logic: model resolution, --only
// filtering, the interactive picker, and human/JSON rendering. Row
// execution itself is internal/smoke's responsibility (tested there via
// the buildAndRun seam) — these tests work entirely with synthetic
// smoke.RowResult values, no real subprocess exec.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/smoke"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tui"
)

// smokeFixtureConfig builds a one-agent, two-model config: one model is
// exposed and running (eligible), the other is registered but never
// exposed/running (not eligible) — enough to exercise resolveSmokeModel's
// three outcomes (eligible / registered-but-ineligible / unknown). Eligibility
// now reads live inventory rows, so the probe is stubbed with qwen3.8:27b-mlx
// running — that is what makes it eligible while not-eligible:x, absent from
// the snapshot, stays a start row no launch can use — and no test here probes
// this machine's real servers.
func smokeFixtureConfig(t *testing.T) *config.Config {
	t.Helper()
	stubSmokeProbe(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "ollama", Artifact: "qwen3.8:27b-mlx", ModelID: "ollama/qwen3.8:27b-mlx", ModelName: "qwen3.8:27b-mlx", Registered: true, Running: true, ArtifactKnown: true},
			{ProviderID: "ollama", Artifact: "not-eligible:x", ModelID: "ollama/not-eligible:x", ModelName: "not-eligible:x", Registered: true, ArtifactKnown: true},
			{ProviderID: "ollama", Artifact: "", ModelID: "ollama/gone:x", ModelName: "gone:x", Registered: true, ArtifactKnown: true},
		},
	})
	cfg := &config.Config{
		Providers: []config.Provider{
			// Serves claude's wire protocol with a base_url so an idle model's
			// start row resolves a direct route (a forced-LiteLLM route with no
			// gateway would error and drop the row from smoke.Candidates).
			{ID: "ollama", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:11434"}, Protocols: []config.Protocol{config.ProtocolAnthropic}},
		},
		Models: []config.Model{
			{ID: "ollama/qwen3.8:27b-mlx", ProviderID: "ollama", ModelName: "qwen3.8:27b-mlx", Location: config.LocationLocal},
			{ID: "ollama/not-eligible:x", ProviderID: "ollama", ModelName: "not-eligible:x", Location: config.LocationLocal},
			{ID: "ollama/gone:x", ProviderID: "ollama", ModelName: "gone:x", Location: config.LocationLocal},
		},
		Agents: []config.Agent{
			{Name: "claude", SupportedProviders: []string{"ollama"}},
		},
	}
	cfg.ExposeAllForTest()
	return cfg
}

// stubSmokeProbe makes smoke.Eligibility (which resolveSmokeModel calls)
// see snap instead of probing live: cmd/wt cannot assign internal/smoke's
// package-private seam, so internal/smoke exposes SetSmokeProbeForTest for
// exactly this — the same package-level seam-and-restore pattern as
// cmd/wt's own probeInventory seam in resolve.go.
func stubSmokeProbe(t *testing.T, snap localmodels.Snapshot) {
	t.Helper()
	t.Cleanup(smoke.SetSmokeProbeForTest(snap))
}

// TestResolveSmokeModelPinnedEligible asserts a pinned id already in the
// eligible set resolves directly, with no TTY/picker interaction.
func TestResolveSmokeModelPinnedEligible(t *testing.T) {
	cfg := smokeFixtureConfig(t)
	tgt, err := resolveSmokeModel(cfg, themes.Default, "ollama/qwen3.8:27b-mlx")
	if err != nil {
		t.Fatalf("resolveSmokeModel: %v", err)
	}
	m, eligible := tgt.Row.Model, tgt.Agents
	if m.ID != "ollama/qwen3.8:27b-mlx" {
		t.Fatalf("got model %q", m.ID)
	}
	if len(eligible) != 1 || eligible[0] != "claude" {
		t.Fatalf("eligible agents = %v, want [claude]", eligible)
	}
}

// TestResolveSmokeModelPinnedNotEligible asserts a pinned id that exists in
// the registry but cannot be smoke-tested (a local model absent from disk)
// gets a specific "cannot be smoke-tested" message, distinct from
// "unknown model".
func TestResolveSmokeModelPinnedNotEligible(t *testing.T) {
	cfg := smokeFixtureConfig(t)
	_, err := resolveSmokeModel(cfg, themes.Default, "ollama/gone:x")
	if err == nil || !strings.Contains(err.Error(), "cannot be smoke-tested") {
		t.Fatalf("err = %v, want a cannot-be-smoke-tested message", err)
	}
}

// TestResolveSmokeModelPinnedBlockedNamesReason asserts a pinned local model
// that is blocked (registered, but the provider answered and it is not on disk)
// gets that row's real BlockReason — the same wording `wt start` gives — rather
// than the generic three-guess message, so the user learns what to fix.
func TestResolveSmokeModelPinnedBlockedNamesReason(t *testing.T) {
	cfg := smokeFixtureConfig(t)
	stubProbeInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "ollama", Artifact: "", ModelID: "ollama/gone:x", ModelName: "gone:x", Registered: true, ArtifactKnown: true},
		},
	})
	_, err := resolveSmokeModel(cfg, themes.Default, "ollama/gone:x")
	if err == nil || !strings.Contains(err.Error(), "not on disk") {
		t.Fatalf("err = %v, want the row's not-on-disk block reason", err)
	}
}

// TestResolveSmokeModelIdleLocalIsStartTarget verifies a registered local model
// that is pulled but not running now resolves (with Start() true) instead of
// erroring, since `wt smoke` starts it before testing.
func TestResolveSmokeModelIdleLocalIsStartTarget(t *testing.T) {
	cfg := smokeFixtureConfig(t)
	got, err := resolveSmokeModel(cfg, themes.Theme{}, "ollama/not-eligible:x")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Start() || len(got.Agents) == 0 {
		t.Fatalf("target = %+v, want a start target with agents", got)
	}
}

// TestSmokeStopFlow verifies the exit flow runs the stop picker — and touches no
// refcount state, since wt smoke never records a session — and is skipped for
// --json or without a TTY (JSON consumers and pipes must not get an interactive
// prompt).
func TestSmokeStopFlow(t *testing.T) {
	oldRel, oldPick, oldTTY := releaseSession, runStopPicker, stdinTTY
	t.Cleanup(func() { releaseSession, runStopPicker, stdinTTY = oldRel, oldPick, oldTTY })
	var calls []string
	releaseSession = func() { calls = append(calls, "release") }
	runStopPicker = func(*config.Config) { calls = append(calls, "picker") }

	stdinTTY = func() bool { return true }
	smokeStopFlow(nil, false)
	if strings.Join(calls, ",") != "picker" {
		t.Fatalf("calls = %v, want only the picker (no release: nothing was recorded)", calls)
	}
	calls = nil
	smokeStopFlow(nil, true)
	stdinTTY = func() bool { return false }
	smokeStopFlow(nil, false)
	if len(calls) != 0 {
		t.Fatalf("calls = %v, want none for --json / no TTY", calls)
	}
}

// TestSmokeCmdIdlePinStartsRunsThenStops is the end-to-end ordering check: for
// an idle pinned model the command starts it once, then runs the agent row,
// then runs the stop flow, and a FAILed row triggers smokeExit(1) only after
// the stop flow — so the deferred cleanup is never skipped by the exit.
func TestSmokeCmdIdlePinStartsRunsThenStops(t *testing.T) {
	cfg := smokeFixtureConfig(t)
	var events []string
	oldStart := startModel
	startModel = func(*config.Config, catalog.Row, bool) error {
		events = append(events, "start")
		return nil
	}
	oldRel, oldPick, oldTTY, oldExit := releaseSession, runStopPicker, stdinTTY, smokeExit
	t.Cleanup(func() {
		startModel, releaseSession, runStopPicker, stdinTTY, smokeExit = oldStart, oldRel, oldPick, oldTTY, oldExit
	})
	releaseSession = func() {}
	runStopPicker = func(*config.Config) { events = append(events, "stop") }
	stdinTTY = func() bool { return true }
	smokeExit = func(code int) { events = append(events, fmt.Sprintf("exit%d", code)) }
	t.Cleanup(smoke.SetBuildAndRunForTest(func(*config.Config, string, config.Model, string, string, time.Duration) (string, int) {
		events = append(events, "row")
		return "boom", 1
	}))

	cmd := smokeCmd(&app{cfg: cfg})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"ollama/not-eligible:x"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(events, ","); got != "start,row,stop,exit1" {
		t.Fatalf("events = %s, want start,row,stop,exit1", got)
	}
}

// TestResolveSmokeModelUnknown asserts a pinned id absent from the
// registry entirely gets "unknown model".
func TestResolveSmokeModelUnknown(t *testing.T) {
	cfg := smokeFixtureConfig(t)
	_, err := resolveSmokeModel(cfg, themes.Default, "ollama/does-not-exist")
	if err == nil || !strings.Contains(err.Error(), "unknown model") {
		t.Fatalf("err = %v, want unknown model error", err)
	}
}

// TestResolveSmokeModelNoModelNoTTY asserts omitting the model id without a
// TTY fails clearly, pointing at passing a model id directly, instead of
// trying to open a picker and failing opaquely.
func TestResolveSmokeModelNoModelNoTTY(t *testing.T) {
	old := stdinTTY
	stdinTTY = func() bool { return false }
	defer func() { stdinTTY = old }()

	cfg := smokeFixtureConfig(t)
	_, err := resolveSmokeModel(cfg, themes.Default, "")
	if err == nil || !strings.Contains(err.Error(), "needs a TTY") {
		t.Fatalf("err = %v, want a TTY-required message", err)
	}
}

// TestResolveSmokeModelNoneEligible asserts an empty eligible set errors
// with actionable guidance instead of silently opening an empty picker.
func TestResolveSmokeModelNoneEligible(t *testing.T) {
	cfg := &config.Config{}
	_, err := resolveSmokeModel(cfg, themes.Default, "")
	if err == nil || !strings.Contains(err.Error(), "no models are available") {
		t.Fatalf("err = %v, want a no-eligible-models message", err)
	}
}

// TestResolveSmokeModelInteractivePicksViaTUI asserts that omitting the
// model id on a TTY delegates to the shared tui.PickModel picker (the same
// decorated list the wt agent flow uses) rather than a bare numbered
// prompt, and resolves to whatever model the picker returns.
func TestResolveSmokeModelInteractivePicksViaTUI(t *testing.T) {
	old := stdinTTY
	stdinTTY = func() bool { return true }
	defer func() { stdinTTY = old }()

	oldPick := pickModelTUI
	pickModelTUI = func(cfg *config.Config, models []config.Model, theme themes.Theme) (config.Model, bool, error) {
		// Candidates now include idle start models, so pick the running one by id
		// rather than relying on list order.
		for _, m := range models {
			if m.ID == "ollama/qwen3.8:27b-mlx" {
				return m, true, nil
			}
		}
		return models[0], true, nil
	}
	defer func() { pickModelTUI = oldPick }()

	cfg := smokeFixtureConfig(t)
	tgt, err := resolveSmokeModel(cfg, themes.Default, "")
	if err != nil {
		t.Fatalf("resolveSmokeModel: %v", err)
	}
	m, eligible := tgt.Row.Model, tgt.Agents
	if m.ID != "ollama/qwen3.8:27b-mlx" {
		t.Fatalf("got model %q, want the picker's returned model", m.ID)
	}
	if len(eligible) != 1 || eligible[0] != "claude" {
		t.Fatalf("eligible agents = %v, want [claude]", eligible)
	}
}

// TestResolveSmokeModelInteractiveCanceled asserts that canceling the TUI
// picker (Esc/q/Ctrl+C) surfaces a clear error instead of silently
// resolving to a zero-value model.
func TestResolveSmokeModelInteractiveCanceled(t *testing.T) {
	old := stdinTTY
	stdinTTY = func() bool { return true }
	defer func() { stdinTTY = old }()

	oldPick := pickModelTUI
	pickModelTUI = func(cfg *config.Config, models []config.Model, theme themes.Theme) (config.Model, bool, error) {
		return config.Model{}, false, nil
	}
	defer func() { pickModelTUI = oldPick }()

	cfg := smokeFixtureConfig(t)
	_, err := resolveSmokeModel(cfg, themes.Default, "")
	if err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("err = %v, want a canceled-picker message", err)
	}
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
	cmd := smokeCmd(&app{cfg: &config.Config{}})
	if cmd.Use != "smoke [model-id]" {
		t.Fatalf("Use = %q", cmd.Use)
	}
	timeout, err := cmd.Flags().GetDuration("timeout")
	if err != nil || timeout != 180*time.Second {
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
