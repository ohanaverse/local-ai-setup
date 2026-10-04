// Tests for internal/smoke's eligibility computation and row classification.
// Process execution is stubbed via the buildAndRun seam throughout — no
// real subprocess is ever exec'd here.
package smoke

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// smokeFixtureConfig builds a small multi-agent, multi-provider config:
// claude supports ollama+openrouter, codex supports only ollama, agy only
// its own native provider, and shell is deliberately included as an Agent
// entry (with a provider it would otherwise match) to prove EligibleAgents
// excludes it via IsCommand rather than relying on it being absent from
// config.toml. The probe is stubbed to smokeIdleSnapshot — the local model is
// on disk but not running, so it has a start row (#179 Phase B: an empty
// snapshot would give it no row at all) — unless a test says otherwise.
func smokeFixtureConfig(t *testing.T) *config.Config {
	t.Helper()
	stubSmokeProbe(t, smokeIdleSnapshot())
	cfg := &config.Config{
		Providers: []config.Provider{
			{ID: "ollama", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}},
			{ID: "openrouter", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "api_key"}},
			{ID: "agy", Auth: config.AuthConfig{Type: "native"}},
		},
		Models: []config.Model{
			{ID: "ollama/qwen3.8:27b-mlx", ProviderID: "ollama", ModelName: "qwen3.8:27b-mlx", Location: config.LocationLocal},
			{ID: "openrouter/glm-5.3-flash", ProviderID: "openrouter", ModelName: "glm-5.3-flash", Location: config.LocationCloud},
			{ID: "agy/native", ProviderID: "agy", ModelName: "native", Native: true, Location: config.LocationCloud},
		},
		Agents: []config.Agent{
			{Name: "claude", SupportedProviders: []string{"ollama", "openrouter"}},
			{Name: "codex", SupportedProviders: []string{"ollama"}},
			{Name: "agy", SupportedProviders: []string{"agy"}},
			{Name: "shell", SupportedProviders: []string{"ollama"}},
		},
	}
	return cfg
}

// smokeIdleSnapshot is the live-probe answer that makes the fixture's ollama
// model an idle start row: on disk, not running.
func smokeIdleSnapshot() localmodels.Snapshot {
	return localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "ollama", Artifact: "qwen3.8:27b-mlx", ModelID: "ollama/qwen3.8:27b-mlx", ModelName: "qwen3.8:27b-mlx", Registered: true, ArtifactKnown: true},
		},
	}
}

// smokeRunningSnapshot is the live-probe answer that makes the fixture's
// ollama model a running row.
func smokeRunningSnapshot() localmodels.Snapshot {
	return localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "ollama", Artifact: "qwen3.8:27b-mlx", ModelID: "ollama/qwen3.8:27b-mlx", ModelName: "qwen3.8:27b-mlx", Registered: true, Running: true, ArtifactKnown: true},
		},
	}
}

// stubSmokeProbe swaps the smoke package's live-probe seam so no test
// touches a real ollama/omlx/mtplx server or reads a real model directory.
func stubSmokeProbe(t *testing.T, snap localmodels.Snapshot) {
	t.Helper()
	old := smokeProbe
	smokeProbe = func(*config.Config) localmodels.Snapshot { return snap }
	t.Cleanup(func() { smokeProbe = old })
}

// findModel returns the fixture's registry model for id, failing the test
// when the fixture lacks it.
func findModel(t *testing.T, cfg *config.Config, id string) config.Model {
	t.Helper()
	idx := config.IndexModelByID(cfg.Models, id)
	if idx < 0 {
		t.Fatalf("fixture missing model %q", id)
	}
	return cfg.Models[idx]
}

// TestEligibleAgentsExcludesShellAndMatchesProviders asserts a local model
// is eligible only for agents whose supported_providers include its
// provider, and that shell is excluded even though it's configured with a
// matching provider in this fixture — proving the exclusion comes from
// agents.IsCommand, not from shell simply being absent.
func TestEligibleAgentsExcludesShellAndMatchesProviders(t *testing.T) {
	cfg := smokeFixtureConfig(t)
	stubSmokeProbe(t, smokeRunningSnapshot()) // live truth: the probe must report the model running
	m := findModel(t, cfg, "ollama/qwen3.8:27b-mlx")
	got := EligibleAgents(cfg, m)
	want := []string{"claude", "codex"}
	if !slices.Equal(got, want) {
		t.Fatalf("EligibleAgents = %v, want %v", got, want)
	}
}

// TestEligibleAgentsAgyNarrowedToNative asserts agy — whose only supported
// provider is its own native one — is eligible only for agy/native, never
// for a model under another provider, mirroring agents-smoke.sh's comment
// that agy's driver ignores whatever model is passed.
func TestEligibleAgentsAgyNarrowedToNative(t *testing.T) {
	cfg := smokeFixtureConfig(t)
	m := findModel(t, cfg, "agy/native")
	got := EligibleAgents(cfg, m)
	want := []string{"agy"}
	if !slices.Equal(got, want) {
		t.Fatalf("EligibleAgents = %v, want %v", got, want)
	}
}

// TestEligibleAgentsCloudModel asserts a cloud model is only eligible for
// the agent(s) whose supported_providers list that provider.
func TestEligibleAgentsCloudModel(t *testing.T) {
	cfg := smokeFixtureConfig(t)
	m := findModel(t, cfg, "openrouter/glm-5.3-flash")
	got := EligibleAgents(cfg, m)
	want := []string{"claude"}
	if !slices.Equal(got, want) {
		t.Fatalf("EligibleAgents = %v, want %v", got, want)
	}
}

// TestAllEligibleModelsUnionsAcrossAgents asserts the interactive picker's
// candidate list is the union (deduped, sorted by id) of every model
// eligible for at least one non-command agent.
func TestAllEligibleModelsUnionsAcrossAgents(t *testing.T) {
	cfg := smokeFixtureConfig(t)
	stubSmokeProbe(t, smokeRunningSnapshot()) // live truth: the probe must report the model running
	got := AllEligibleModels(cfg)
	var ids []string
	for _, m := range got {
		ids = append(ids, m.ID)
	}
	want := []string{"agy/native", "ollama/qwen3.8:27b-mlx", "openrouter/glm-5.3-flash"}
	if !slices.Equal(ids, want) {
		t.Fatalf("AllEligibleModels ids = %v, want %v", ids, want)
	}
}

// TestEligibilityExcludesIdleLocalModels verifies a configured local model the
// live probe did not report as running is not eligible. It matters because wt
// smoke must never advertise a model a launch would refuse — the whole point
// of moving eligibility onto live rows.
func TestEligibilityExcludesIdleLocalModels(t *testing.T) {
	cfg := smokeFixtureConfig(t) // fixture probes smokeIdleSnapshot: on disk, not running
	// LiteLLM routing lets the idle start row resolve a route, so it is a
	// real candidate. Guard against a vacuous pass: the idle model must be a
	// Candidates entry (it is startable), so its absence from Eligibility
	// below is the idle rule, not a missing row.
	cfg.SetLitellmForTest(config.LitellmState{Enabled: true, URL: "http://localhost:4000", APIKey: "sk-test"})
	if !slices.ContainsFunc(Candidates(cfg), func(c Candidate) bool { return c.Row.Model.ID == "ollama/qwen3.8:27b-mlx" }) {
		t.Fatal("fixture's idle local model has no candidate row; the exclusion check would pass vacuously")
	}
	models, _ := Eligibility(cfg)
	for _, m := range models {
		if m.ID == "ollama/qwen3.8:27b-mlx" {
			t.Errorf("idle local model reported eligible: %+v", m)
		}
	}
}

// TestEligibilityIncludesRunningLocalModel verifies a local model the probe
// reports as running IS eligible, so the smoke list reflects what can actually
// serve a request right now rather than what is merely configured.
func TestEligibilityIncludesRunningLocalModel(t *testing.T) {
	cfg := smokeFixtureConfig(t)
	stubSmokeProbe(t, smokeRunningSnapshot()) // live truth: the probe reports the model running
	got := AllEligibleModels(cfg)
	var found bool
	for _, m := range got {
		if m.ID == "ollama/qwen3.8:27b-mlx" {
			found = true
		}
	}
	if !found {
		t.Errorf("running local model not eligible: %+v", got)
	}
}

// TestEligibilityIncludesDiscoveredRunningModel verifies a running model that
// is not in the registry — discovered from the provider's own model directory
// — is eligible. It matters because the non-TUI path now accepts a -M pin
// naming a discovered model, so smoke would otherwise under-report exactly the
// models a launch would accept.
func TestEligibilityIncludesDiscoveredRunningModel(t *testing.T) {
	cfg := smokeFixtureConfig(t)
	disc := config.DiscoveredModelID("ollama", "extra")
	stubSmokeProbe(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "ollama", Artifact: "extra", ModelID: disc, ModelName: "extra", Running: true},
		},
	})
	models, _ := Eligibility(cfg)
	var found bool
	for _, m := range models {
		if m.ID == disc {
			found = true
		}
	}
	if !found {
		t.Errorf("running discovered model not eligible: %+v", models)
	}
}

// TestEligibilityIncludesDiscoveredUnderLitellm verifies a discovered running
// model routed through LiteLLM IS eligible (#179 Phase B): wt routes it under
// its discovered id, so a real launch accepts it and smoke must offer it —
// before, the picker refused such a row and smoke hid it to match.
func TestEligibilityIncludesDiscoveredUnderLitellm(t *testing.T) {
	cfg := smokeFixtureConfig(t)
	disc := config.DiscoveredModelID("ollama", "extra")
	stubSmokeProbe(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "ollama", Artifact: "extra", ModelID: disc, ModelName: "extra", Running: true},
		},
	})
	cfg.SetLitellmForTest(config.LitellmState{Enabled: true, URL: "http://localhost:4000", APIKey: "sk-test"})
	models, _ := Eligibility(cfg)
	if !slices.ContainsFunc(models, func(m config.Model) bool { return m.ID == disc }) {
		t.Errorf("discovered model routed through LiteLLM not eligible: %+v", models)
	}
}

// withStubBuildAndRun replaces the buildAndRun seam for the duration of a
// test, restoring the original on cleanup.
func withStubBuildAndRun(t *testing.T, out execOutcome) {
	t.Helper()
	old := buildAndRun
	buildAndRun = func(_ context.Context, _ *config.Config, _ string, _ config.Model, _ string, _ RowDir, _ time.Duration, _ ProfileApplier, cleanup *func() error) execOutcome {
		*cleanup = func() error { return nil }
		return out
	}
	t.Cleanup(func() { buildAndRun = old })
}

// TestRunRowPass asserts exit 0 plus the sentinel present in captured
// output classifies PASS.
func TestRunRowPass(t *testing.T) {
	withStubBuildAndRun(t, execOutcome{ExitCode: 0, Output: "before WT-SMOKE-claude-run-1 after"})
	res := RunRow(context.Background(), &config.Config{}, "claude", config.Model{ID: "ollama/x"}, "prompt", "WT-SMOKE-claude-run-1", time.Second, FixedDir("."), nil)
	if res.Status != StatusPass {
		t.Fatalf("Status = %v, want PASS (err=%v)", res.Status, res.Err)
	}
}

// TestRunRowFailMissingSentinel asserts exit 0 without the sentinel in
// output classifies FAIL, not PASS — a process exiting cleanly is not
// enough on its own when a sentinel was requested.
func TestRunRowFailMissingSentinel(t *testing.T) {
	withStubBuildAndRun(t, execOutcome{ExitCode: 0, Output: "wrong output"})
	res := RunRow(context.Background(), &config.Config{}, "claude", config.Model{ID: "ollama/x"}, "prompt", "WT-SMOKE-claude-run-1", time.Second, FixedDir("."), nil)
	if res.Status != StatusFail {
		t.Fatalf("Status = %v, want FAIL", res.Status)
	}
}

// TestRunRowFailNonZeroExit asserts a non-zero exit code is FAIL
// regardless of what the output contains.
func TestRunRowFailNonZeroExit(t *testing.T) {
	withStubBuildAndRun(t, execOutcome{ExitCode: 1, Output: "WT-SMOKE-claude-run-1"})
	res := RunRow(context.Background(), &config.Config{}, "claude", config.Model{ID: "ollama/x"}, "prompt", "WT-SMOKE-claude-run-1", time.Second, FixedDir("."), nil)
	if res.Status != StatusFail {
		t.Fatalf("Status = %v, want FAIL", res.Status)
	}
}

// TestRunRowSkipNotInstalled asserts a StartErr matching agents.Command's
// exact "agent <name> not installed" text classifies SKIP — the same
// substring convention agents-smoke.sh's classifier uses.
func TestRunRowSkipNotInstalled(t *testing.T) {
	withStubBuildAndRun(t, execOutcome{StartErr: fmt.Errorf("agent claude not installed")})
	res := RunRow(context.Background(), &config.Config{}, "claude", config.Model{ID: "ollama/x"}, "prompt", "sentinel", time.Second, FixedDir("."), nil)
	if res.Status != StatusSkip {
		t.Fatalf("Status = %v, want SKIP", res.Status)
	}
}

// TestRunRowFailOtherStartErr asserts a StartErr that is NOT the
// not-installed message (e.g. a route-resolution failure) classifies FAIL,
// not SKIP — only the exact installed-check message means SKIP.
func TestRunRowFailOtherStartErr(t *testing.T) {
	withStubBuildAndRun(t, execOutcome{StartErr: fmt.Errorf(`unknown provider "x" for model "y"`)})
	res := RunRow(context.Background(), &config.Config{}, "claude", config.Model{ID: "ollama/x"}, "prompt", "sentinel", time.Second, FixedDir("."), nil)
	if res.Status != StatusFail {
		t.Fatalf("Status = %v, want FAIL", res.Status)
	}
}

// TestRunRowFailTimeout asserts a timed-out row classifies FAIL with a
// timeout-specific error, and never blocks on a hung process.
func TestRunRowFailTimeout(t *testing.T) {
	withStubBuildAndRun(t, execOutcome{TimedOut: true})
	res := RunRow(context.Background(), &config.Config{}, "claude", config.Model{ID: "ollama/x"}, "prompt", "sentinel", time.Second, FixedDir("."), nil)
	if res.Status != StatusFail || res.Err == nil || !strings.Contains(res.Err.Error(), "timed out") {
		t.Fatalf("Status = %v, Err = %v, want FAIL with a timeout error", res.Status, res.Err)
	}
}

// TestRunRowCustomPromptSkipsSentinelCheck asserts an empty sentinel (the
// --prompt override case) makes exit-code-0 alone sufficient for PASS —
// the documented verification degradation for a custom prompt.
func TestRunRowCustomPromptSkipsSentinelCheck(t *testing.T) {
	withStubBuildAndRun(t, execOutcome{ExitCode: 0, Output: "anything at all"})
	res := RunRow(context.Background(), &config.Config{}, "claude", config.Model{ID: "ollama/x"}, "do the task", "", time.Second, FixedDir("."), nil)
	if res.Status != StatusPass {
		t.Fatalf("Status = %v, want PASS (empty sentinel means exit-code-only)", res.Status)
	}
}

// writeFakeAgentBinary points PATH at a temp dir containing a no-op
// executable named name, so agents.BuildLaunchCmd's exec.LookPath resolves
// to this harmless script rather than a real codex/agy/etc binary that
// might happen to be installed on the machine running the test — a
// realBuildAndRun test actually calls cmd.Start(), unlike every other test
// in this file (which stubs buildAndRun and never execs anything).
func writeFakeAgentBinary(t *testing.T, name string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake %s binary: %v", name, err)
	}
	t.Setenv("PATH", dir)
}

// TestRealBuildAndRunOneShotArgsNilApplier locks realBuildAndRun's own
// "profileApplier == nil" branch. Every other test in this file exercises
// RunRow through the buildAndRun stub and never runs realBuildAndRun's own
// oneShotArgs branching (code review finding, 2026-09-25) — without this, a
// regression that silently dropped oneShotArgs when no profile applies
// (the common case: most agent/model pairs have no profile configured)
// would compile and pass every other test in this package.
func TestRealBuildAndRunOneShotArgsNilApplier(t *testing.T) {
	writeFakeAgentBinary(t, "agy")
	var cleanup func() error
	outcome := realBuildAndRun(context.Background(), &config.Config{}, "agy", config.Model{Native: true}, "the prompt", FixedDir(t.TempDir()), time.Second, nil, &cleanup)
	if cleanup == nil {
		t.Fatal("realBuildAndRun never set *cleanup")
	}
	if !strings.HasSuffix(outcome.Command, "-p the prompt") {
		t.Fatalf("Command = %q, want it to end with agy's one-shot args \"-p the prompt\"", outcome.Command)
	}
}

// TestRealBuildAndRunPassesYolo pins that wt smoke's one-shot launches grant
// tool-use permission (copilot: --yolo). Without it, copilot CLI's own docs
// say non-interactive mode ("-p") cannot get tool-call approval at all — any
// tool call the backing model attempts is denied outright
// ("Permission denied and could not request permission from user"), which a
// less rigidly instruction-following model can spiral on for many minutes
// before giving up or timing out (observed live against an mtplx model,
// 2026-09-25: ~1118s and ~200K tokens burned narrating the denials as a
// "macOS security restriction" before it finally answered the trivial
// smoke prompt). A regression here would reintroduce that failure mode
// silently, since a well-behaved model (as in the ollama case that passed)
// never exercises the missing flag.
func TestRealBuildAndRunPassesYolo(t *testing.T) {
	writeFakeAgentBinary(t, "copilot")
	var cleanup func() error
	outcome := realBuildAndRun(context.Background(), &config.Config{}, "copilot", config.Model{Native: true}, "the prompt", FixedDir(t.TempDir()), time.Second, nil, &cleanup)
	if !strings.HasSuffix(outcome.Command, "--yolo -p the prompt") {
		t.Fatalf("Command = %q, want it to end with \"--yolo -p the prompt\" (yolo must be passed for one-shot copilot launches)", outcome.Command)
	}
}

// TestRealBuildAndRunSkipsYoloForCodex pins that wt smoke does NOT force
// codex's yolo flag (--dangerously-bypass-approvals-and-sandbox). Unlike
// copilot, codex's "exec" subcommand is already built for non-interactive
// use — it never prompts for tool-call approval (code review finding,
// 2026-09-25: live-observed as "approval: never" in codex exec's own
// startup banner) — so forcing its yolo flag buys smoke nothing but does
// strip codex's own sandbox (workspace-write by default) for no reason,
// widening the blast radius of a trivial smoke prompt with no evidence codex
// ever needed it. A regression here would silently drop that sandbox on
// every codex smoke run.
func TestRealBuildAndRunSkipsYoloForCodex(t *testing.T) {
	writeFakeAgentBinary(t, "codex")
	var cleanup func() error
	outcome := realBuildAndRun(context.Background(), &config.Config{}, "codex", config.Model{Native: true}, "the prompt", FixedDir(t.TempDir()), time.Second, nil, &cleanup)
	if strings.Contains(outcome.Command, "--dangerously-bypass-approvals-and-sandbox") {
		t.Fatalf("Command = %q, want it to NOT contain codex's yolo flag (codex exec never prompts; forcing it only strips its sandbox)", outcome.Command)
	}
	if !strings.HasSuffix(outcome.Command, "exec the prompt") {
		t.Fatalf("Command = %q, want it to end with codex's one-shot args \"exec the prompt\"", outcome.Command)
	}
}

// TestRealBuildAndRunOpencodeYolo pins the one-shot argv wt smoke hands
// opencode: "--auto=true run <prompt>", the skip-permissions flag exactly
// once and with its value attached. opencode declares the flag per command,
// so a bare "--auto" in front of "run" parses "run" as the flag's value and
// falls into the default (TUI) command; and the flag it replaced,
// "--dangerously-skip-permissions", no longer exists at all — opencode
// printed its usage and exited 1, failing the opencode row on every model.
func TestRealBuildAndRunOpencodeYolo(t *testing.T) {
	writeFakeAgentBinary(t, "opencode")
	var cleanup func() error
	outcome := realBuildAndRun(context.Background(), &config.Config{}, "opencode", config.Model{Native: true}, "the prompt", FixedDir(t.TempDir()), 10*time.Second, nil, &cleanup)
	if !strings.HasSuffix(outcome.Command, "opencode --auto=true run the prompt") {
		t.Fatalf("Command = %q, want it to end with \"opencode --auto=true run the prompt\"", outcome.Command)
	}
}

// TestRealBuildAndRunOneShotArgsPlacedByApplier locks realBuildAndRun's
// "profileApplier != nil, success" branch: the applier — not
// realBuildAndRun — places oneShotArgs, and realBuildAndRun must adopt the
// applier's returned cleanup instead of its own no-op default.
func TestRealBuildAndRunOneShotArgsPlacedByApplier(t *testing.T) {
	writeFakeAgentBinary(t, "agy")
	cleanupCalled := false
	applier := func(cmd *exec.Cmd, oneShotArgs []string) (func() error, error) {
		cmd.Args = append(cmd.Args, "--extra")
		cmd.Args = append(cmd.Args, oneShotArgs...)
		return func() error { cleanupCalled = true; return nil }, nil
	}
	var cleanup func() error
	outcome := realBuildAndRun(context.Background(), &config.Config{}, "agy", config.Model{Native: true}, "the prompt", FixedDir(t.TempDir()), time.Second, applier, &cleanup)
	if !strings.HasSuffix(outcome.Command, "--extra -p the prompt") {
		t.Fatalf("Command = %q, want the applier's own flag then oneShotArgs at the end", outcome.Command)
	}
	if cleanup == nil {
		t.Fatal("realBuildAndRun did not adopt the applier's cleanup")
	}
	if err := cleanup(); err != nil || !cleanupCalled {
		t.Fatalf("cleanup() called=%v err=%v, want realBuildAndRun's *cleanup to be the applier's own cleanup", cleanupCalled, err)
	}
}

// TestRealBuildAndRunRevertsAndReappendsOneShotArgsOnApplierError locks the
// exact invariant code review flagged as uncovered (2026-09-25): when
// profileApplier fails after already mutating cmd.Args, realBuildAndRun
// must revert to the pre-apply snapshot AND still reattach oneShotArgs — a
// regression that reordered this (or dropped the revert) would leak the
// applier's partial mutation ("--partial" below) into the degraded command,
// or land the degraded command with no oneShotArgs at all.
func TestRealBuildAndRunRevertsAndReappendsOneShotArgsOnApplierError(t *testing.T) {
	writeFakeAgentBinary(t, "agy")
	applier := func(cmd *exec.Cmd, oneShotArgs []string) (func() error, error) {
		cmd.Args = append(cmd.Args, "--partial")
		return func() error { return nil }, fmt.Errorf("boom")
	}
	var cleanup func() error
	outcome := realBuildAndRun(context.Background(), &config.Config{}, "agy", config.Model{Native: true}, "the prompt", FixedDir(t.TempDir()), time.Second, applier, &cleanup)
	if strings.Contains(outcome.Command, "--partial") {
		t.Fatalf("Command = %q, still contains the failed applier's partial mutation — revert did not happen", outcome.Command)
	}
	if !strings.HasSuffix(outcome.Command, "-p the prompt") {
		t.Fatalf("Command = %q, want oneShotArgs reattached after the revert", outcome.Command)
	}
}

// TestRunRowFailModelFallback asserts a row whose agent could not select the
// model under test and fell back to its own default is FAIL — even with exit
// 0 and the sentinel in the output — with a reason naming the fallback. The
// default model echoes the sentinel just as well, so without this rule wt
// smoke reported PASS for a model it never exercised (pi × a discovered
// local model, #179 Phase B host acceptance).
func TestRunRowFailModelFallback(t *testing.T) {
	const warn = `pi: model "ollama/x" not configured for pi, using default model`
	withStubBuildAndRun(t, execOutcome{ExitCode: 0, Output: "WT-SMOKE-pi-run-1", ModelFallback: warn})
	res := RunRow(context.Background(), &config.Config{}, "pi", config.Model{ID: "ollama/x"}, "prompt", "WT-SMOKE-pi-run-1", time.Second, FixedDir("."), nil)
	if res.Status != StatusFail {
		t.Fatalf("Status = %v, want FAIL for a model fallback", res.Status)
	}
	if res.Err == nil || !strings.Contains(res.Err.Error(), "fell back") || !strings.Contains(res.Err.Error(), "ollama/x") || !strings.Contains(res.Err.Error(), warn) {
		t.Fatalf("Err = %v, want it to name the fallback, the model under test and the driver's reason", res.Err)
	}
}

// smokePiFixture points HOME at a temp dir holding pi's models.json with the
// given content, puts a no-op fake pi on PATH, and returns a direct-mode
// config whose only provider is ollama — so realBuildAndRun runs the real
// pi driver (sync + Build) without touching ~/.pi or a real pi binary.
func smokePiFixture(t *testing.T, modelsJSON string) *config.Config {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	piDir := filepath.Join(home, ".pi", "agent")
	if err := os.MkdirAll(piDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(piDir, "models.json"), []byte(modelsJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	writeFakeAgentBinary(t, "pi")
	return &config.Config{
		Providers: []config.Provider{{ID: "ollama", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:11434"}}},
	}
}

// smokeDiscoveredOllama is a discovered local model: on disk, no registry
// entry, so absent from cfg.Models.
func smokeDiscoveredOllama() config.Model {
	return config.Model{
		ID:         config.DiscoveredModelID("ollama", "llama3.2:1b"),
		ModelName:  "llama3.2:1b",
		ProviderID: "ollama",
		Location:   config.LocationLocal,
		Source:     config.SourceDiscovered,
	}
}

// TestRealBuildAndRunReportsModelFallback asserts the real seam carries the
// driver's fallback signal into the outcome and does not run the agent: here
// the user disabled the model's pi entry (_launch: false), so pi would run
// its default model. Running it would spend a real request on a model the
// row is not about, and its exit 0 + sentinel is exactly the false PASS this
// guards against. RunRow on the same fixture must be FAIL.
func TestRealBuildAndRunReportsModelFallback(t *testing.T) {
	cfg := smokePiFixture(t, `{"providers":{"ollama":{"api":"openai-completions","apiKey":"ollama","baseUrl":"http://localhost:11434/v1","models":[{"_launch":false,"id":"llama3.2:1b"}]}}}`)
	var cleanup func() error
	outcome := realBuildAndRun(context.Background(), cfg, "pi", smokeDiscoveredOllama(), "the prompt", FixedDir(t.TempDir()), time.Second, nil, &cleanup)
	if cleanup == nil {
		t.Fatal("realBuildAndRun never set *cleanup")
	}
	if !strings.Contains(outcome.ModelFallback, "using default model") {
		t.Fatalf("ModelFallback = %q, want the driver's fallback warning", outcome.ModelFallback)
	}
	if outcome.Command != "" || outcome.StartErr != nil {
		t.Fatalf("Command = %q, StartErr = %v, want the agent never started", outcome.Command, outcome.StartErr)
	}
	if res := RunRow(context.Background(), cfg, "pi", smokeDiscoveredOllama(), "the prompt", "", time.Second, FixedDir(t.TempDir()), nil); res.Status != StatusFail {
		t.Fatalf("RunRow Status = %v, want FAIL (err=%v)", res.Status, res.Err)
	}
}

// TestRealBuildAndRunDiscoveredModelNoFallback asserts the non-fallback case
// is unaffected: a discovered model pi CAN select runs with --model naming
// it, reports no fallback, and its row passes. Guards against the fallback
// rule failing healthy rows, and pins end to end that a discovered model
// reaches pi as itself rather than as pi's default.
func TestRealBuildAndRunDiscoveredModelNoFallback(t *testing.T) {
	cfg := smokePiFixture(t, `{"providers":{"ollama":{"api":"openai-completions","apiKey":"ollama","baseUrl":"http://localhost:11434/v1","models":[]}}}`)
	var cleanup func() error
	outcome := realBuildAndRun(context.Background(), cfg, "pi", smokeDiscoveredOllama(), "the prompt", FixedDir(t.TempDir()), 10*time.Second, nil, &cleanup)
	if outcome.ModelFallback != "" {
		t.Fatalf("ModelFallback = %q, want none", outcome.ModelFallback)
	}
	if !strings.HasSuffix(outcome.Command, "--model ollama/llama3.2:1b -p the prompt") {
		t.Fatalf("Command = %q, want it to end with \"--model ollama/llama3.2:1b -p the prompt\"", outcome.Command)
	}
	if res := RunRow(context.Background(), cfg, "pi", smokeDiscoveredOllama(), "the prompt", "", 10*time.Second, FixedDir(t.TempDir()), nil); res.Status != StatusPass {
		t.Fatalf("RunRow Status = %v, want PASS (err=%v)", res.Status, res.Err)
	}
}

// silentFallbackDriver reports a model fallback without saying why
// (LaunchCmd.ModelFallback set, Warn empty) — the shape a future driver could
// take, since nothing ties the two fields together.
type silentFallbackDriver struct{}

func (silentFallbackDriver) Build(config.Model, bool, config.Route) agents.LaunchCmd {
	return agents.LaunchCmd{Bin: "silent-fallback", ModelFallback: true}
}

func (silentFallbackDriver) YoloFlag() string { return "" }

// TestRealBuildAndRunModelFallbackWithoutWarnStillFails asserts a fallback
// the driver gave no warning for is still a FAIL. The outcome carries the
// fallback as its reason string, so an empty reason used to read as "no
// fallback": the agent was never run, yet the row's zero exit code passed it
// whenever --prompt was set (no sentinel to miss) — a PASS for a row that
// executed nothing.
func TestRealBuildAndRunModelFallbackWithoutWarnStillFails(t *testing.T) {
	t.Cleanup(agents.RegisterTest("silent-fallback", func() agents.Driver { return silentFallbackDriver{} }))
	writeFakeAgentBinary(t, "silent-fallback")
	var cleanup func() error
	outcome := realBuildAndRun(context.Background(), &config.Config{}, "silent-fallback", config.Model{Native: true}, "the prompt", FixedDir(t.TempDir()), time.Second, nil, &cleanup)
	if outcome.ModelFallback == "" || outcome.Command != "" {
		t.Fatalf("ModelFallback = %q, Command = %q, want a non-empty fallback reason and the agent never started", outcome.ModelFallback, outcome.Command)
	}
	if res := RunRow(context.Background(), &config.Config{}, "silent-fallback", config.Model{Native: true}, "the prompt", "", time.Second, FixedDir(t.TempDir()), nil); res.Status != StatusFail {
		t.Fatalf("RunRow Status = %v, want FAIL (err=%v)", res.Status, res.Err)
	}
}

// TestNewRunIDFormat asserts NewRunID produces the "run-<8 hex chars>"
// shape RunRow's default prompt embeds.
func TestNewRunIDFormat(t *testing.T) {
	id := NewRunID()
	if !strings.HasPrefix(id, "run-") {
		t.Fatalf("NewRunID() = %q, want run- prefix", id)
	}
	suffix := strings.TrimPrefix(id, "run-")
	if len(suffix) != 8 {
		t.Fatalf("NewRunID() suffix = %q, want 8 hex chars", suffix)
	}
	for _, r := range suffix {
		if !strings.ContainsRune("0123456789abcdef", r) {
			t.Fatalf("NewRunID() = %q contains non-hex character %q", id, r)
		}
	}
}

// TestTruncateOutputShortUnchanged asserts output at or under the 8KiB
// bound passes through byte-for-byte, with no marker prepended — only
// output that actually exceeds the bound should ever be flagged as cut.
func TestTruncateOutputShortUnchanged(t *testing.T) {
	short := strings.Repeat("a", maxCapturedOutput)
	got := truncateOutput(short)
	if got != short {
		t.Fatalf("truncateOutput changed output at the exact bound (len %d)", len(short))
	}
	tiny := "hello world"
	if got := truncateOutput(tiny); got != tiny {
		t.Fatalf("truncateOutput(%q) = %q, want unchanged", tiny, got)
	}
}

// TestTruncateOutputLongTailWithMarker asserts output over the 8KiB bound
// is cut to its last maxCapturedOutput bytes and prefixed with a marker —
// this is what caps the FAIL detail block (human renderer) and the --json
// "output" field, per the design spec's "truncated to a bounded tail".
func TestTruncateOutputLongTailWithMarker(t *testing.T) {
	long := strings.Repeat("x", maxCapturedOutput) + "TAIL-MARKER-END"
	got := truncateOutput(long)
	if !strings.HasPrefix(got, truncatedMarker) {
		t.Fatalf("truncateOutput output missing truncation marker prefix: %q", got[:min(80, len(got))])
	}
	if !strings.HasSuffix(got, "TAIL-MARKER-END") {
		t.Fatal("truncateOutput dropped the tail of long output instead of keeping the last bytes")
	}
	body := strings.TrimPrefix(got, truncatedMarker)
	if len(body) != maxCapturedOutput {
		t.Fatalf("truncated body len = %d, want %d", len(body), maxCapturedOutput)
	}
}

// TestBoundedWriterStaysBoundedDuringWrites asserts boundedWriter never
// retains more than maxCapturedOutput bytes at any point while writes are
// still arriving — not just after the fact like truncateOutput — so a
// runaway agent that logs megabytes before its timeout can't balloon the
// in-memory buffer realBuildAndRun captures stdout/stderr into.
func TestBoundedWriterStaysBoundedDuringWrites(t *testing.T) {
	var w boundedWriter
	for i := 0; i < 20; i++ {
		chunk := strings.Repeat("x", maxCapturedOutput/2)
		if _, err := w.Write([]byte(chunk)); err != nil {
			t.Fatalf("Write: %v", err)
		}
		if len(w.tail) > maxCapturedOutput {
			t.Fatalf("after write %d, retained %d bytes, want <= %d", i, len(w.tail), maxCapturedOutput)
		}
	}
}

// TestBoundedWriterMatchesTruncateOutput asserts boundedWriter's final
// String() equals truncateOutput applied to the same bytes written in one
// shot — the incremental, memory-bounded capture path must produce the
// exact same marker+tail output the existing truncateOutput contract (and
// its tests) already pin down.
func TestBoundedWriterMatchesTruncateOutput(t *testing.T) {
	full := strings.Repeat("y", maxCapturedOutput) + "TAIL-MARKER-END"
	want := truncateOutput(full)

	var w boundedWriter
	for _, chunk := range []string{full[:100], full[100:5000], full[5000:]} {
		if _, err := w.Write([]byte(chunk)); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if got := w.String(); got != want {
		t.Fatalf("boundedWriter.String() = %q, want %q (from truncateOutput)", got[:min(80, len(got))], want[:min(80, len(want))])
	}
}

// TestDefaultPrompt pins #193: the sentinel RunRow searches for must never
// appear verbatim in the prompt. Some agents print the prompt back (codex's
// transcript starts with a "user" block holding it), so a sentinel the prompt
// contains would be found in the output whatever the model replied — or even
// if no model replied at all. The prompt carries the text in lower case and
// asks for it in upper case; the check looks for the upper-case form.
func TestDefaultPrompt(t *testing.T) {
	prompt, sentinel := DefaultPrompt("claude", "run-abcd1234")
	if want := "WT-SMOKE-CLAUDE-RUN-ABCD1234"; sentinel != want {
		t.Fatalf("sentinel = %q, want %q", sentinel, want)
	}
	if strings.Contains(prompt, sentinel) {
		t.Fatalf("prompt %q contains the sentinel %q: a prompt echo would pass the row", prompt, sentinel)
	}
	if !strings.Contains(prompt, "wt-smoke-claude-run-abcd1234") {
		t.Fatalf("prompt %q does not carry the lower-case text the model is asked to convert", prompt)
	}
}

// TestRunRowFailsOnPromptEcho is the false PASS #193 reported: an agent that
// exits 0 having printed only the prompt back — the model's own reply being
// junk or absent — must FAIL the default-prompt check.
func TestRunRowFailsOnPromptEcho(t *testing.T) {
	prompt, sentinel := DefaultPrompt("codex", "run-abcd1234")
	withStubBuildAndRun(t, execOutcome{ExitCode: 0, Output: "user\n" + prompt + "\ncodex\n{\"malformed\": tool call\n"})
	res := RunRow(context.Background(), &config.Config{}, "codex", config.Model{ID: "ollama/x"}, prompt, sentinel, time.Second, FixedDir("."), nil)
	if res.Status != StatusFail {
		t.Fatalf("Status = %v, want FAIL: the output holds only the echoed prompt", res.Status)
	}
}

// TestRunRowPassesOnTheConvertedReply is the other half: the same echo plus
// the model's upper-case reply passes.
func TestRunRowPassesOnTheConvertedReply(t *testing.T) {
	prompt, sentinel := DefaultPrompt("codex", "run-abcd1234")
	withStubBuildAndRun(t, execOutcome{ExitCode: 0, Output: "user\n" + prompt + "\ncodex\n" + sentinel + "\n"})
	res := RunRow(context.Background(), &config.Config{}, "codex", config.Model{ID: "ollama/x"}, prompt, sentinel, time.Second, FixedDir("."), nil)
	if res.Status != StatusPass {
		t.Fatalf("Status = %v (%v), want PASS", res.Status, res.Err)
	}
}

// TestNewRowDirIsAFreshGitRepoAndCleansUp pins the directory a smoke row runs
// in by default (#193): a new, empty directory outside the caller's tree that
// is a git repository — codex refuses to run outside one — and that its
// cleanup removes, along with anything the agent wrote there.
func TestNewRowDirIsAFreshGitRepoAndCleansUp(t *testing.T) {
	dir, cleanup, err := NewRowDir()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		t.Fatalf("row dir %s is not a git repository: %v", dir, err)
	}
	wd, _ := os.Getwd()
	if rel, err := filepath.Rel(wd, dir); err == nil && !strings.HasPrefix(rel, "..") {
		t.Fatalf("row dir %s is inside the working directory %s", dir, wd)
	}
	if err := os.WriteFile(filepath.Join(dir, "junk.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("row dir %s still exists after cleanup (err = %v)", dir, err)
	}
}

// candidatesFixture builds an ollama-only config with three local models — one
// running, one pulled but idle, one absent from disk — supported by one agent,
// and stubs the probe to match. Returns the config and the probe restore func.
func candidatesFixture(t *testing.T) (*config.Config, func()) {
	t.Helper()
	cfg := &config.Config{
		Providers: []config.Provider{
			// Serves claude's wire protocol and has a base_url so the start
			// row's direct route resolves (a forced-LiteLLM route with no gateway would error and
			// drop the row).
			{ID: "ollama", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:11434"}, Protocols: []config.Protocol{config.ProtocolAnthropic}},
		},
		Models: []config.Model{
			{ID: "ollama/running:1", ProviderID: "ollama", ModelName: "running:1", Location: config.LocationLocal},
			{ID: "ollama/idle:1", ProviderID: "ollama", ModelName: "idle:1", Location: config.LocationLocal},
			{ID: "ollama/absent:1", ProviderID: "ollama", ModelName: "absent:1", Location: config.LocationLocal},
		},
		Agents: []config.Agent{{Name: "claude", SupportedProviders: []string{"ollama"}}},
	}
	snap := localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "ollama", ModelID: "ollama/running:1", ModelName: "running:1", Artifact: "running:1", Registered: true, Running: true, ArtifactKnown: true},
			{ProviderID: "ollama", ModelID: "ollama/idle:1", ModelName: "idle:1", Artifact: "idle:1", Registered: true, ArtifactKnown: true},
			{ProviderID: "ollama", ModelID: "ollama/absent:1", ModelName: "absent:1", Artifact: "", Registered: true, ArtifactKnown: true},
		},
	}
	return cfg, SetSmokeProbeForTest(snap)
}

// TestCandidatesIncludeStartRowsButNotBlocked verifies Candidates lists a
// launchable model, a non-running startable local model (Action start), and
// omits a model missing from disk (blocked). `wt smoke` starts the second kind
// before testing it; Eligibility must keep excluding it so existing callers
// are unchanged.
func TestCandidatesIncludeStartRowsButNotBlocked(t *testing.T) {
	cfg, restore := candidatesFixture(t)
	defer restore()

	got := map[string]catalog.Action{}
	for _, c := range Candidates(cfg) {
		got[c.Row.Model.ID] = c.Row.Action()
	}
	if got["ollama/running:1"] != catalog.ActionLaunch || got["ollama/idle:1"] != catalog.ActionStart {
		t.Errorf("candidates = %v, want running=launch idle=start", got)
	}
	if _, ok := got["ollama/absent:1"]; ok {
		t.Errorf("blocked (absent) model must not be a candidate: %v", got)
	}
	models, _ := Eligibility(cfg)
	for _, m := range models {
		if m.ID == "ollama/idle:1" {
			t.Error("Eligibility must still exclude start rows")
		}
	}
}

// TestCandidatesGateStartRowsPerAgent verifies an idle local model lists only
// the agents whose OWN route resolves: claude reaches the provider directly
// (it serves the anthropic protocol) while codex is forced through a LiteLLM
// that is not configured, so its route errors. The candidate row is shared, but
// its agent list must never inherit one agent's route verdict for another —
// otherwise `wt smoke <id>` would start the model and run an agent whose launch
// cannot be built, reporting a FAIL that reads as a model problem.
func TestCandidatesGateStartRowsPerAgent(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.Provider{
			{ID: "ollama", Location: config.LocationLocal, Protocols: []config.Protocol{config.ProtocolAnthropic, config.ProtocolOpenAIChat}, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:11434"}},
		},
		Models: []config.Model{
			{ID: "ollama/idle:1", ProviderID: "ollama", ModelName: "idle:1", Location: config.LocationLocal},
		},
		Agents: []config.Agent{
			{Name: "claude", SupportedProviders: []string{"ollama"}},
			{Name: "codex", SupportedProviders: []string{"ollama"}},
		},
	}
	defer SetSmokeProbeForTest(localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "ollama", ModelID: "ollama/idle:1", ModelName: "idle:1", Artifact: "idle:1", Registered: true, ArtifactKnown: true},
		},
	})()

	for i := 0; i < 20; i++ { // agent iteration order must not matter
		var got []string
		for _, c := range Candidates(cfg) {
			if c.Row.Model.ID == "ollama/idle:1" {
				got = c.Agents
			}
		}
		if !slices.Equal(got, []string{"claude"}) {
			t.Fatalf("agents = %v, want only [claude] (codex's route cannot resolve)", got)
		}
	}
}

// TestSweepRowDirsRemovesWhatALateWriterRecreates pins the race seen on a real
// run (#193): an agent's own background hook — a session-save script claude
// leaves running after it exits — wrote into the row's directory just as it
// was being removed, and the directory survived. The end-of-run sweep keeps
// removing until the directories have stayed gone for a moment.
func TestSweepRowDirsRemovesWhatALateWriterRecreates(t *testing.T) {
	dir, cleanup, err := NewRowDir()
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	// The sweep races a timed writer: should it ever lose, don't leave the
	// directory behind in the system temp directory.
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(150 * time.Millisecond)
		_ = os.MkdirAll(filepath.Join(dir, ".hook", "logs"), 0o755)
		_ = os.WriteFile(filepath.Join(dir, ".hook", "logs", "late.log"), []byte("x"), 0o644)
	}()
	SweepRowDirs([]string{dir})
	<-done
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("row dir %s survived the sweep (err = %v)", dir, err)
	}
}

// TestSweepRowDirsReturnsPromptlyWhenNothingIsLeft pins the cost: with no late
// writer the sweep waits only its short settle time, and with no directories
// at all it does not wait.
func TestSweepRowDirsReturnsPromptlyWhenNothingIsLeft(t *testing.T) {
	start := time.Now()
	SweepRowDirs(nil)
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Fatalf("an empty sweep took %s", d)
	}
	dir, cleanup, err := NewRowDir()
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	start = time.Now()
	SweepRowDirs([]string{dir})
	if d := time.Since(start); d > time.Second {
		t.Fatalf("a sweep with nothing to remove took %s", d)
	}
}

// TestSweepRowDirsReportsWhatItCannotRemove pins the sweep's return value: a
// directory an agent left unremovable (a read-only tree) is handed back so
// `wt smoke` can name it, rather than an unsupervised agent's output staying
// in the temp directory with nothing said.
func TestSweepRowDirsReportsWhatItCannotRemove(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can remove a read-only tree")
	}
	oldLimit := sweepLimit
	sweepLimit = 250 * time.Millisecond
	t.Cleanup(func() { sweepLimit = oldLimit })

	dir := t.TempDir()
	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	gone := filepath.Join(t.TempDir(), "gone")

	left := SweepRowDirs([]string{dir, gone})
	if len(left) != 1 || left[0] != dir {
		t.Fatalf("left = %v, want only %s", left, dir)
	}
}

// TestRunRowFailInterrupted asserts a row whose agent was killed by a
// cancelled run classifies FAIL with an interrupt-specific error.
func TestRunRowFailInterrupted(t *testing.T) {
	withStubBuildAndRun(t, execOutcome{Interrupted: true})
	res := RunRow(context.Background(), &config.Config{}, "claude", config.Model{ID: "ollama/x"}, "prompt", "sentinel", time.Second, FixedDir("."), nil)
	if res.Status != StatusFail || res.Err == nil || !strings.Contains(res.Err.Error(), "interrupted") {
		t.Fatalf("Status = %v, Err = %v, want FAIL with an interrupted error", res.Status, res.Err)
	}
}

// TestRealBuildAndRunCancelKillsTheAgentGroup pins what Ctrl+C does to a
// running row. The agent runs in its own process group (Setpgid), so the
// terminal's SIGINT never reaches it: unless the cancelled context kills that
// group, wt exits and the agent — and anything it spawned — keeps running
// with permission checks off for the rest of its timeout.
func TestRealBuildAndRunCancelKillsTheAgentGroup(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	// The agent records its pid, then waits on a child of its own: the child
	// is what a kill aimed at the direct process alone would leave behind.
	script := "#!/bin/sh\necho $$ > " + pidFile + "\n/bin/sleep 30 &\nwait\n"
	if err := os.WriteFile(filepath.Join(dir, "agy"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan execOutcome, 1)
	go func() {
		var cleanup func() error
		done <- realBuildAndRun(ctx, &config.Config{}, "agy", config.Model{Native: true}, "the prompt", FixedDir(t.TempDir()), 3*time.Second, nil, &cleanup)
	}()

	var pgid int
	for deadline := time.Now().Add(2 * time.Second); pgid == 0; time.Sleep(10 * time.Millisecond) {
		if b, err := os.ReadFile(pidFile); err == nil {
			pgid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		}
		if time.Now().After(deadline) {
			t.Fatal("the fake agent never started")
		}
	}
	// Whatever the outcome, the test must not leave the group running.
	t.Cleanup(func() { _ = syscall.Kill(-pgid, syscall.SIGKILL) })
	cancel()

	out := <-done
	if !out.Interrupted {
		t.Fatalf("outcome = %+v, want Interrupted", out)
	}
	for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if err := syscall.Kill(-pgid, 0); err == syscall.ESRCH {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("process group %d is still alive after the run was cancelled", pgid)
		}
	}
}

// TestAgentStateDirsOnlyForSmokeDirs pins the guard on what smoke may delete
// from an agent's own state: only the entry for a directory NewRowDir made.
// The caller's real project (a --cwd run) must never be offered for removal —
// that entry holds the user's transcripts.
func TestAgentStateDirsOnlyForSmokeDirs(t *testing.T) {
	t.Setenv("HOME", "/h")
	if got := AgentStateDirs("claude", "/private/tmp/wt-smoke-123"); len(got) != 1 || got[0] != "/h/.claude/projects/-private-tmp-wt-smoke-123" {
		t.Errorf("claude in a smoke directory: %v, want its project directory", got)
	}
	if got := AgentStateDirs("claude", "/Users/me/repo"); got != nil {
		t.Errorf("claude in a real directory: %v, want nothing", got)
	}
	if got := AgentStateDirs("codex", "/private/tmp/wt-smoke-123"); got != nil {
		t.Errorf("codex keeps no per-directory state: %v, want nothing", got)
	}
}

// TestNewRowDirIsTheResolvedPath pins that NewRowDir hands back the path the
// agent will see: macOS's temp dir sits behind a symlink (/var →
// /private/var), and an agent keys its own state on the resolved one.
func TestNewRowDirIsTheResolvedPath(t *testing.T) {
	dir, cleanup, err := NewRowDir()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if real, err := filepath.EvalSymlinks(dir); err != nil || real != dir {
		t.Fatalf("row dir %s resolves to %s (err = %v)", dir, real, err)
	}
}

// TestNewRowDirIgnoresInheritedGitDir pins the isolation under a git hook or a
// shell that exports GIT_DIR: `git -C <dir> init` obeys GIT_DIR over -C, so it
// would re-initialise the caller's repository and leave the row's directory
// without one.
func TestNewRowDirIgnoresInheritedGitDir(t *testing.T) {
	other := filepath.Join(t.TempDir(), "caller.git")
	t.Setenv("GIT_DIR", other)
	dir, cleanup, err := NewRowDir()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		t.Errorf("row dir %s is not a git repository: %v", dir, err)
	}
	if _, err := os.Stat(other); err == nil {
		t.Errorf("git init ran against the inherited GIT_DIR %s", other)
	}
}

// fakeAgy puts an executable `agy` with the given shell body first on PATH.
// The body must call other programs by absolute path: PATH holds nothing else.
func fakeAgy(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "agy"), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

// TestRealBuildAndRunDropsInheritedGitEnv pins that the agent does not
// inherit the variables that point git at a repository: with them set, the
// agent's git commands would act on the caller's repository from inside the
// row's temporary directory. Variables that only describe the user survive.
func TestRealBuildAndRunDropsInheritedGitEnv(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "env")
	fakeAgy(t, "/usr/bin/env > "+envFile+"\n")
	t.Setenv("GIT_DIR", "/caller/.git")
	t.Setenv("GIT_WORK_TREE", "/caller")
	t.Setenv("GIT_INDEX_FILE", "/caller/.git/index")
	t.Setenv("GIT_AUTHOR_NAME", "someone")
	var cleanup func() error
	out := realBuildAndRun(context.Background(), &config.Config{}, "agy", config.Model{Native: true}, "the prompt", FixedDir(t.TempDir()), 5*time.Second, nil, &cleanup)
	if out.StartErr != nil || out.ExitCode != 0 {
		t.Fatalf("outcome = %+v", out)
	}
	env, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"GIT_DIR=", "GIT_WORK_TREE=", "GIT_INDEX_FILE="} {
		if strings.Contains("\n"+string(env), "\n"+name) {
			t.Errorf("the agent inherited %s", name)
		}
	}
	if !strings.Contains(string(env), "GIT_AUTHOR_NAME=someone") {
		t.Error("GIT_AUTHOR_NAME was dropped; only repository-locating variables should be")
	}
}

// TestRealBuildAndRunWaitsForTheAgentGroup pins that a row is not over until
// what the agent left running is: a background process in the agent's group
// (a session-save hook) that writes after the agent exits has written by the
// time the row returns, so removing the row's directory afterwards is final.
func TestRealBuildAndRunWaitsForTheAgentGroup(t *testing.T) {
	late := filepath.Join(t.TempDir(), "late")
	fakeAgy(t, "(/bin/sleep 0.4; echo x > "+late+") >/dev/null 2>&1 &\nexit 0\n")
	var cleanup func() error
	out := realBuildAndRun(context.Background(), &config.Config{}, "agy", config.Model{Native: true}, "the prompt", FixedDir(t.TempDir()), 5*time.Second, nil, &cleanup)
	if out.StartErr != nil || out.ExitCode != 0 {
		t.Fatalf("outcome = %+v", out)
	}
	if _, err := os.Stat(late); err != nil {
		t.Fatalf("the row returned before the agent's background process had finished: %v", err)
	}
}

// TestRealBuildAndRunKillsAGroupThatOutlivesTheAgent pins the bound on that
// wait: a background process still running when groupGrace is up is killed,
// so nothing in the agent's group can write into the row's directory later.
func TestRealBuildAndRunKillsAGroupThatOutlivesTheAgent(t *testing.T) {
	oldGrace := groupGrace
	groupGrace = 200 * time.Millisecond
	t.Cleanup(func() { groupGrace = oldGrace })
	pidFile := filepath.Join(t.TempDir(), "pid")
	fakeAgy(t, "echo $$ > "+pidFile+"\n/bin/sleep 30 >/dev/null 2>&1 &\nexit 0\n")
	var cleanup func() error
	out := realBuildAndRun(context.Background(), &config.Config{}, "agy", config.Model{Native: true}, "the prompt", FixedDir(t.TempDir()), 5*time.Second, nil, &cleanup)
	if out.StartErr != nil || out.ExitCode != 0 {
		t.Fatalf("outcome = %+v", out)
	}
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pgid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	if pgid == 0 {
		t.Fatalf("no pid in %q", b)
	}
	t.Cleanup(func() { _ = syscall.Kill(-pgid, syscall.SIGKILL) })
	for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if err := syscall.Kill(-pgid, 0); err == syscall.ESRCH {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("process group %d is still alive after the row returned", pgid)
		}
	}
}

// TestRealBuildAndRunMakesNoDirForAnAgentThatCannotRun pins when a row's
// directory is asked for: only once the agent is about to start. A row whose
// agent is not installed (SKIP) or cannot select the model (FAIL) runs
// nothing, so it must not cost a `git init` and a sweep at the end of the run.
func TestRealBuildAndRunMakesNoDirForAnAgentThatCannotRun(t *testing.T) {
	asked := 0
	rowDir := func() (string, error) { asked++; return t.TempDir(), nil }
	var cleanup func() error

	t.Setenv("PATH", t.TempDir()) // agy is not installed
	if out := realBuildAndRun(context.Background(), &config.Config{}, "agy", config.Model{Native: true}, "the prompt", rowDir, time.Second, nil, &cleanup); out.StartErr == nil {
		t.Fatalf("outcome = %+v, want a not-installed error", out)
	}
	if asked != 0 {
		t.Errorf("a row whose agent is not installed asked for a directory")
	}

	t.Cleanup(agents.RegisterTest("silent-fallback", func() agents.Driver { return silentFallbackDriver{} }))
	writeFakeAgentBinary(t, "silent-fallback")
	if out := realBuildAndRun(context.Background(), &config.Config{}, "silent-fallback", config.Model{Native: true}, "the prompt", rowDir, time.Second, nil, &cleanup); out.ModelFallback == "" {
		t.Fatalf("outcome = %+v, want a model fallback", out)
	}
	if asked != 0 {
		t.Errorf("a row whose agent fell back to its default model asked for a directory")
	}

	writeFakeAgentBinary(t, "agy")
	if out := realBuildAndRun(context.Background(), &config.Config{}, "agy", config.Model{Native: true}, "the prompt", rowDir, time.Second, nil, &cleanup); out.StartErr != nil {
		t.Fatalf("outcome = %+v", out)
	}
	if asked != 1 {
		t.Errorf("a row that ran asked for a directory %d times, want 1", asked)
	}
}

// TestRealBuildAndRunRunsTheAgentInTheRowDir pins that the directory the row
// was given is the agent's working directory.
func TestRealBuildAndRunRunsTheAgentInTheRowDir(t *testing.T) {
	pwdFile := filepath.Join(t.TempDir(), "pwd")
	fakeAgy(t, "/bin/pwd -P > "+pwdFile+"\n")
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var cleanup func() error
	if out := realBuildAndRun(context.Background(), &config.Config{}, "agy", config.Model{Native: true}, "the prompt", FixedDir(dir), 5*time.Second, nil, &cleanup); out.StartErr != nil || out.ExitCode != 0 {
		t.Fatalf("outcome = %+v", out)
	}
	got, err := os.ReadFile(pwdFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(got)) != dir {
		t.Fatalf("the agent ran in %q, want %q", strings.TrimSpace(string(got)), dir)
	}
}

// TestRealBuildAndRunSetsPWDToTheRowDir pins the isolation for an agent that
// takes its directory from PWD: opencode ran its smoke row in the caller's
// directory, permissions off, although the process's cwd was the row's
// temporary one — the inherited PWD still named the caller's (#193).
func TestRealBuildAndRunSetsPWDToTheRowDir(t *testing.T) {
	writeFakeAgentBinary(t, "agy")
	t.Setenv("PWD", "/stale/caller")
	dir := t.TempDir()
	// A shell script cannot report the PWD it was handed (sh corrects a
	// wrong one on startup), so read the command the agent is started with.
	var gotDir string
	var pwd []string
	applier := func(cmd *exec.Cmd, oneShotArgs []string) (func() error, error) {
		gotDir = cmd.Dir
		for _, kv := range cmd.Env {
			if strings.HasPrefix(kv, "PWD=") {
				pwd = append(pwd, kv)
			}
		}
		cmd.Args = append(cmd.Args, oneShotArgs...)
		return func() error { return nil }, nil
	}
	var cleanup func() error
	if out := realBuildAndRun(context.Background(), &config.Config{}, "agy", config.Model{Native: true}, "the prompt", FixedDir(dir), 5*time.Second, applier, &cleanup); out.StartErr != nil || out.ExitCode != 0 {
		t.Fatalf("outcome = %+v", out)
	}
	if gotDir != dir {
		t.Fatalf("the agent's directory was %q, want the row directory %q", gotDir, dir)
	}
	if len(pwd) != 1 || pwd[0] != "PWD="+dir {
		t.Fatalf("the agent's PWD entries were %v, want exactly PWD=%s", pwd, dir)
	}
}
