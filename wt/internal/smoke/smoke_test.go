// Tests for internal/smoke's eligibility computation and row classification.
// Process execution is stubbed via the buildAndRun seam throughout — no
// real subprocess is ever exec'd here.
package smoke

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// smokeFixtureConfig builds a small multi-agent, multi-provider config:
// claude supports ollama+openrouter, codex supports only ollama, agy only
// its own native provider, and shell is deliberately included as an Agent
// entry (with a provider it would otherwise match) to prove EligibleAgents
// excludes it via IsCommand rather than relying on it being absent from
// config.toml. The probe is stubbed to an empty snapshot, so the local
// model reads as not-running unless a test says otherwise.
func smokeFixtureConfig(t *testing.T) *config.Config {
	t.Helper()
	stubSmokeProbe(t, localmodels.Snapshot{})
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
	cfg.ExposeAllForTest()
	return cfg
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
	cfg := smokeFixtureConfig(t) // fixture probes an empty snapshot: nothing is running
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

// TestEligibilityExcludesDiscoveredUnderLitellm verifies a discovered running
// model routed through LiteLLM is NOT eligible. It matters because the picker
// makes such a row unselectable (internal/tui/modeltable.go) and the CLI
// refuses it (pickerBlockedReason) — the gateway's model_list has no entry for
// a model wt discovered on disk — so advertising it would break smoke's
// contract that a model it reports eligible is a model a real launch accepts.
func TestEligibilityExcludesDiscoveredUnderLitellm(t *testing.T) {
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
	for _, m := range models {
		if m.ID == disc {
			t.Errorf("discovered model routed through LiteLLM reported eligible: %+v", m)
		}
	}
}

// withStubBuildAndRun replaces the buildAndRun seam for the duration of a
// test, restoring the original on cleanup.
func withStubBuildAndRun(t *testing.T, out execOutcome) {
	t.Helper()
	old := buildAndRun
	buildAndRun = func(cfg *config.Config, agentName string, m config.Model, prompt, cwd string, timeout time.Duration) execOutcome {
		return out
	}
	t.Cleanup(func() { buildAndRun = old })
}

// TestRunRowPass asserts exit 0 plus the sentinel present in captured
// output classifies PASS.
func TestRunRowPass(t *testing.T) {
	withStubBuildAndRun(t, execOutcome{ExitCode: 0, Output: "before WT-SMOKE-claude-run-1 after"})
	res := RunRow(&config.Config{}, "claude", config.Model{ID: "ollama/x"}, "prompt", "WT-SMOKE-claude-run-1", time.Second, ".")
	if res.Status != StatusPass {
		t.Fatalf("Status = %v, want PASS (err=%v)", res.Status, res.Err)
	}
}

// TestRunRowFailMissingSentinel asserts exit 0 without the sentinel in
// output classifies FAIL, not PASS — a process exiting cleanly is not
// enough on its own when a sentinel was requested.
func TestRunRowFailMissingSentinel(t *testing.T) {
	withStubBuildAndRun(t, execOutcome{ExitCode: 0, Output: "wrong output"})
	res := RunRow(&config.Config{}, "claude", config.Model{ID: "ollama/x"}, "prompt", "WT-SMOKE-claude-run-1", time.Second, ".")
	if res.Status != StatusFail {
		t.Fatalf("Status = %v, want FAIL", res.Status)
	}
}

// TestRunRowFailNonZeroExit asserts a non-zero exit code is FAIL
// regardless of what the output contains.
func TestRunRowFailNonZeroExit(t *testing.T) {
	withStubBuildAndRun(t, execOutcome{ExitCode: 1, Output: "WT-SMOKE-claude-run-1"})
	res := RunRow(&config.Config{}, "claude", config.Model{ID: "ollama/x"}, "prompt", "WT-SMOKE-claude-run-1", time.Second, ".")
	if res.Status != StatusFail {
		t.Fatalf("Status = %v, want FAIL", res.Status)
	}
}

// TestRunRowSkipNotInstalled asserts a StartErr matching agents.Command's
// exact "agent <name> not installed" text classifies SKIP — the same
// substring convention agents-smoke.sh's classifier uses.
func TestRunRowSkipNotInstalled(t *testing.T) {
	withStubBuildAndRun(t, execOutcome{StartErr: fmt.Errorf("agent claude not installed")})
	res := RunRow(&config.Config{}, "claude", config.Model{ID: "ollama/x"}, "prompt", "sentinel", time.Second, ".")
	if res.Status != StatusSkip {
		t.Fatalf("Status = %v, want SKIP", res.Status)
	}
}

// TestRunRowFailOtherStartErr asserts a StartErr that is NOT the
// not-installed message (e.g. a route-resolution failure) classifies FAIL,
// not SKIP — only the exact installed-check message means SKIP.
func TestRunRowFailOtherStartErr(t *testing.T) {
	withStubBuildAndRun(t, execOutcome{StartErr: fmt.Errorf(`unknown provider "x" for model "y"`)})
	res := RunRow(&config.Config{}, "claude", config.Model{ID: "ollama/x"}, "prompt", "sentinel", time.Second, ".")
	if res.Status != StatusFail {
		t.Fatalf("Status = %v, want FAIL", res.Status)
	}
}

// TestRunRowFailTimeout asserts a timed-out row classifies FAIL with a
// timeout-specific error, and never blocks on a hung process.
func TestRunRowFailTimeout(t *testing.T) {
	withStubBuildAndRun(t, execOutcome{TimedOut: true})
	res := RunRow(&config.Config{}, "claude", config.Model{ID: "ollama/x"}, "prompt", "sentinel", time.Second, ".")
	if res.Status != StatusFail || res.Err == nil || !strings.Contains(res.Err.Error(), "timed out") {
		t.Fatalf("Status = %v, Err = %v, want FAIL with a timeout error", res.Status, res.Err)
	}
}

// TestRunRowCustomPromptSkipsSentinelCheck asserts an empty sentinel (the
// --prompt override case) makes exit-code-0 alone sufficient for PASS —
// the documented verification degradation for a custom prompt.
func TestRunRowCustomPromptSkipsSentinelCheck(t *testing.T) {
	withStubBuildAndRun(t, execOutcome{ExitCode: 0, Output: "anything at all"})
	res := RunRow(&config.Config{}, "claude", config.Model{ID: "ollama/x"}, "do the task", "", time.Second, ".")
	if res.Status != StatusPass {
		t.Fatalf("Status = %v, want PASS (empty sentinel means exit-code-only)", res.Status)
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

// TestDefaultPrompt asserts the sentinel is embedded verbatim in the
// prompt RunRow will later search for in captured output.
func TestDefaultPrompt(t *testing.T) {
	prompt, sentinel := DefaultPrompt("claude", "run-abcd1234")
	wantSentinel := "WT-SMOKE-claude-run-abcd1234"
	if sentinel != wantSentinel {
		t.Fatalf("sentinel = %q, want %q", sentinel, wantSentinel)
	}
	if !strings.Contains(prompt, sentinel) {
		t.Fatalf("prompt %q does not contain sentinel %q", prompt, sentinel)
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
	cfg.ExposeAllForTest()
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
