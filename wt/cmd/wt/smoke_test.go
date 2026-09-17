// Tests for `wt smoke`'s CLI-layer logic: model resolution, --only
// filtering, the interactive picker, and human/JSON rendering. Row
// execution itself is internal/smoke's responsibility (tested there via
// the buildAndRun seam) — these tests work entirely with synthetic
// smoke.RowResult values, no real subprocess exec.
package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/smoke"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// smokeFixtureConfig builds a one-agent, two-model config: one model is
// exposed and running (eligible), the other is registered but never
// exposed/running (not eligible) — enough to exercise resolveSmokeModel's
// three outcomes (eligible / registered-but-ineligible / unknown).
func smokeFixtureConfig() *config.Config {
	cfg := &config.Config{
		Providers: []config.Provider{
			{ID: "ollama", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}},
		},
		Models: []config.Model{
			{ID: "ollama/qwen3.8:27b-mlx", ProviderID: "ollama", ModelName: "qwen3.8:27b-mlx", Location: config.LocationLocal},
			{ID: "ollama/not-eligible:x", ProviderID: "ollama", ModelName: "not-eligible:x", Location: config.LocationLocal},
		},
		Agents: []config.Agent{
			{Name: "claude", SupportedProviders: []string{"ollama"}},
		},
	}
	cfg.ExposeAllForTest()
	cfg.SetLocalRunningForTest("ollama/qwen3.8:27b-mlx") // "not-eligible" stays flagged off
	return cfg
}

// TestResolveSmokeModelPinnedEligible asserts a pinned id already in the
// eligible set resolves directly, with no TTY/picker interaction.
func TestResolveSmokeModelPinnedEligible(t *testing.T) {
	cfg := smokeFixtureConfig()
	m, eligible, err := resolveSmokeModel(cfg, themes.Default, "ollama/qwen3.8:27b-mlx")
	if err != nil {
		t.Fatalf("resolveSmokeModel: %v", err)
	}
	if m.ID != "ollama/qwen3.8:27b-mlx" {
		t.Fatalf("got model %q", m.ID)
	}
	if len(eligible) != 1 || eligible[0] != "claude" {
		t.Fatalf("eligible agents = %v, want [claude]", eligible)
	}
}

// TestResolveSmokeModelPinnedNotEligible asserts a pinned id that exists in
// the registry but isn't currently eligible (a local model that isn't
// running) gets a specific "not eligible" message, distinct from
// "unknown model".
func TestResolveSmokeModelPinnedNotEligible(t *testing.T) {
	cfg := smokeFixtureConfig()
	_, _, err := resolveSmokeModel(cfg, themes.Default, "ollama/not-eligible:x")
	if err == nil || !strings.Contains(err.Error(), "not currently eligible") {
		t.Fatalf("err = %v, want a not-currently-eligible message", err)
	}
}

// TestResolveSmokeModelUnknown asserts a pinned id absent from the
// registry entirely gets "unknown model".
func TestResolveSmokeModelUnknown(t *testing.T) {
	cfg := smokeFixtureConfig()
	_, _, err := resolveSmokeModel(cfg, themes.Default, "ollama/does-not-exist")
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

	cfg := smokeFixtureConfig()
	_, _, err := resolveSmokeModel(cfg, themes.Default, "")
	if err == nil || !strings.Contains(err.Error(), "needs a TTY") {
		t.Fatalf("err = %v, want a TTY-required message", err)
	}
}

// TestResolveSmokeModelNoneEligible asserts an empty eligible set errors
// with actionable guidance instead of silently opening an empty picker.
func TestResolveSmokeModelNoneEligible(t *testing.T) {
	cfg := &config.Config{}
	_, _, err := resolveSmokeModel(cfg, themes.Default, "")
	if err == nil || !strings.Contains(err.Error(), "no models are currently eligible") {
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
		return models[0], true, nil
	}
	defer func() { pickModelTUI = oldPick }()

	cfg := smokeFixtureConfig()
	m, eligible, err := resolveSmokeModel(cfg, themes.Default, "")
	if err != nil {
		t.Fatalf("resolveSmokeModel: %v", err)
	}
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

	cfg := smokeFixtureConfig()
	_, _, err := resolveSmokeModel(cfg, themes.Default, "")
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
