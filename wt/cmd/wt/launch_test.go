package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/initseed"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
	"github.com/ohanaverse/local-ai-setup/wt/internal/refcount"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rotation"
	"github.com/ohanaverse/local-ai-setup/wt/internal/session"
	"github.com/ohanaverse/local-ai-setup/wt/internal/usage"
)

// TestBuildLaunchUnknownAgent asserts that an unregistered agent returns a
// clear error rather than a nil command. Without this the launch path could
// dereference a nil driver.
func TestBuildLaunchUnknownAgent(t *testing.T) {
	_, err := buildLaunch("not-an-agent", config.Model{}, "/tmp", false, nil, nil, nil)
	if err == nil {
		t.Fatal("expected error for unknown agent")
	}
	if !strings.Contains(err.Error(), "unknown agent") {
		t.Errorf("error = %q, want 'unknown agent'", err.Error())
	}
}

// TestBuildLaunchClaudeResume asserts that a claude launch with a session
// appends --resume <id>. This is the non-TUI resume wiring that the bash
// claude-wt wrapper used to do. The model is a provider-hosted (non-native)
// model: native models must never resume (see TestBuildLaunchNativeSkipsResume).
func TestBuildLaunchClaudeResume(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not installed on PATH; skipping launcher test")
	}
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "ollama", Protocols: []config.Protocol{config.ProtocolAnthropic, config.ProtocolOpenAIChat}, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:11434"}}},
	}
	cmd, err := buildLaunch("claude", config.Model{ID: "ollama/kimi-k2.7-code:cloud", ModelName: "kimi-k2.7-code:cloud"}, "/tmp/repo", false,
		&session.Session{ID: "abc-123", MTime: time.Now()}, cfg, nil)
	if err != nil {
		t.Fatalf("buildLaunch: %v", err)
	}
	got := strings.Join(cmd.Args, " ")
	if !strings.Contains(got, "--resume abc-123") {
		t.Errorf("args = %q, want --resume abc-123", got)
	}
}

// TestBuildLaunchNativeSkipsResume asserts that a native model (e.g.
// claude/native) never appends --resume, even when a session is supplied.
// Native models launch with no model override, so resuming a session would
// restore the session's stored model and silently override the user's
// "native" choice — the exact bug where selecting claude/native launched
// claude with a prior session's kimi-k2.7-code:cloud model.
func TestBuildLaunchNativeSkipsResume(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not installed on PATH; skipping launcher test")
	}
	cmd, err := buildLaunch("claude", config.Model{ID: "claude/native", ModelName: "native", Native: true}, "/tmp/repo", false,
		&session.Session{ID: "abc-123", MTime: time.Now()}, nil, nil)
	if err != nil {
		t.Fatalf("buildLaunch: %v", err)
	}
	got := strings.Join(cmd.Args, " ")
	if strings.Contains(got, "--resume") || strings.Contains(got, "--session") {
		t.Errorf("args = %q, native model must not resume", got)
	}
}

// TestBuildLaunchOpenCodeResume asserts that an opencode launch with a session
// appends --session <id>.
func TestBuildLaunchOpenCodeResume(t *testing.T) {
	if _, err := exec.LookPath("opencode"); err != nil {
		t.Skip("opencode not installed on PATH; skipping launcher test")
	}
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "ollama", Protocols: []config.Protocol{config.ProtocolAnthropic, config.ProtocolOpenAIChat}, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:11434"}}},
	}
	cmd, err := buildLaunch("opencode", config.Model{ID: "ollama/gemma4:9b"}, "/tmp/repo", false,
		&session.Session{ID: "proj-123.json", MTime: time.Now()}, cfg, nil)
	if err != nil {
		t.Fatalf("buildLaunch: %v", err)
	}
	got := strings.Join(cmd.Args, " ")
	if !strings.Contains(got, "--session proj-123.json") {
		t.Errorf("args = %q, want --session proj-123.json", got)
	}
}

// TestBuildLaunchNoSessionOmitsResume asserts that a nil session injects no
// resume/session flag on a non-native model. This is the "start fresh" path
// — and the test deliberately uses a non-native model so it exercises only
// the sess==nil short-circuit. Native-model behavior is pinned separately:
// TestBuildLaunchNativeSkipsResume covers native+session, and
// TestBuildLaunchCmdNativeSkipsResume in the agents package pins the
// defense-in-depth `!m.Native` guard directly.
func TestBuildLaunchNoSessionOmitsResume(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not installed on PATH; skipping launcher test")
	}
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "ollama", Protocols: []config.Protocol{config.ProtocolAnthropic, config.ProtocolOpenAIChat}, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:11434"}}},
	}
	cmd, err := buildLaunch("claude", config.Model{ID: "ollama/kimi-k2.7-code:cloud", ModelName: "kimi-k2.7-code:cloud"}, "/tmp/repo", false, nil, cfg, nil)
	if err != nil {
		t.Fatalf("buildLaunch: %v", err)
	}
	got := strings.Join(cmd.Args, " ")
	if strings.Contains(got, "--resume") || strings.Contains(got, "--session") {
		t.Errorf("args = %q, should not contain resume/session flags", got)
	}
}

// TestInitUsesAgentFlag verifies that the --init path passes the explicit
// --agent value to initseed.Seed so that agent-specific pointer files
// (e.g. CLAUDE.md) are created. This mirrors the bash wrapper behavior where
// claude-wt --init seeded CLAUDE.md.
func TestInitUsesAgentFlag(t *testing.T) {
	root := t.TempDir()
	res, err := initseed.Seed("claude", root)
	if err != nil {
		t.Fatalf("Seed(claude): %v", err)
	}
	found := false
	for _, name := range res.Created {
		if name == "CLAUDE.md" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected CLAUDE.md created, got %v", res.Created)
	}
}

// buildLaunch must invoke the pi driver's SyncModels before building the
// command, so a rotation-selected model is present in models.json by the time
// the _launch check runs. Without the sync, pi would fall back to its default.
// The sync runs before LookPath, so it is observable even when pi is not
// installed (the "not installed" error is tolerated).
func TestBuildLaunchSyncsPi(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".pi", "agent")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	modelsPath := filepath.Join(dir, "models.json")
	if err := os.WriteFile(modelsPath, []byte(`{"providers":{"ollama":{"models":[]}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "ollama", Protocols: []config.Protocol{config.ProtocolAnthropic, config.ProtocolOpenAIChat}, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:11434"}}},
		Models: []config.Model{
			{ID: "ollama/deepseek-v4-pro:cloud", ModelName: "deepseek-v4-pro:cloud", ProviderID: "ollama"},
		},
	}
	m := config.Model{ID: "ollama/deepseek-v4-pro:cloud", ModelName: "deepseek-v4-pro:cloud", ProviderID: "ollama"}
	cmd, err := buildLaunch("pi", m, "/tmp", false, nil, cfg, nil)
	if err != nil && !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("buildLaunch: %v", err)
	}
	// The sync must have added the model to models.json regardless of whether
	// pi is installed (the sync runs before LookPath).
	data, _ := os.ReadFile(modelsPath)
	if !strings.Contains(string(data), "deepseek-v4-pro:cloud") {
		t.Errorf("models.json = %s, want it to contain deepseek-v4-pro:cloud (sync ran)", string(data))
	}
	if err == nil {
		got := strings.Join(cmd.Args, " ")
		if !strings.Contains(got, "--model ollama/deepseek-v4-pro:cloud") {
			t.Errorf("args = %q, want --model ollama/deepseek-v4-pro:cloud (sync + verify)", got)
		}
	}
}

// TestLaunchUsesResolveModel verifies that the non-TUI launch path's filter
// resolution goes through resolveModel when -M is supplied, returning the
// pinned model. Without -M and with multiple eligible models, resolveModel
// must surface a clear "multiple models match" error rather than silently picking one.
// This is the contract launchFiltered in launch.go relies on (rotation
// outside the function advances through the eligible list when pinned == "").
func TestLaunchUsesResolveModel(t *testing.T) {
	cfg := &config.Config{
		DefaultTag: "code",
		Providers: []config.Provider{
			{ID: "claude", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "native"}},
		},
		Models: []config.Model{
			{ID: "claude/opus", ProviderID: "claude", ModelName: "opus", Tags: []string{"code"}},
			{ID: "claude/sonnet", ProviderID: "claude", ModelName: "sonnet", Tags: []string{"code"}},
		},
		Agents: []config.Agent{
			{Name: "claude", SupportedProviders: []string{"claude"}},
		},
	}
	cfg.ExposeAllForTest()

	// Two models, no -M → resolveModel errors with "multiple models match".
	// Any ambiguous eligible list errors so callers can route through
	// rotation rather than silently picking one.
	if _, _, err := resolveModel("claude", cfg, "", "", ""); err == nil {
		t.Fatal("expected error for ambiguous eligible list, got nil")
	}

	// With -M claude/opus → resolves correctly.
	m, _, err := resolveModel("claude", cfg, "", "", "claude/opus")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.ID != "claude/opus" {
		t.Errorf("got %q, want claude/opus", m.ID)
	}
}

// TestBuildFilteredCmdCommandAgentUsesWorktree is the regression lock for the
// shell worktree-path drop: `wt -W foo --agent shell` must run in the worktree,
// not the caller's CWD. The command-agent branch of launchFiltered previously
// routed through a CWD-only helper that hardcoded worktreePath=".", discarding
// the worktree path that EnsureForName had just created. buildFilteredCmd must
// build the command with cmd.Dir set to the requested worktree.
func TestBuildFilteredCmdCommandAgentUsesWorktree(t *testing.T) {
	cfg := &config.Config{}
	worktree := "/tmp/wt-shell-regression"

	_, cmd, err := buildFilteredCmd("shell", worktree, cfg, false, "", "", "", []string{"ls", "-la"})
	if err != nil {
		t.Fatalf("buildFilteredCmd: %v", err)
	}
	if cmd.Dir != worktree {
		t.Errorf("cmd.Dir = %q, want %q (shell must run in the worktree, not the CWD)", cmd.Dir, worktree)
	}
	// Shell execs the passthrough args directly as argv (no model layer).
	if got := strings.Join(cmd.Args, " "); !strings.Contains(got, "ls -la") {
		t.Errorf("args = %q, want the passthrough argv (ls -la)", got)
	}
}

// TestLaunchFilteredCommandAgentRunsInWorktree is the end-to-end regression
// lock for the shell worktree-path drop: it exercises the full launchFiltered
// → buildFilteredCmd → runAgentCmd path (not just buildFilteredCmd in
// isolation) and asserts the command actually runs inside the requested
// worktree. The build-only test above catches cmd.Dir; this catches a future
// regression where the wired path bypasses buildFilteredCmd again (as the old
// launchDirect helper did) and silently drops the worktree path.
func TestLaunchFilteredCommandAgentRunsInWorktree(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("MODELMAN_REGISTRY", "")
	worktree := t.TempDir()

	// Recorder script: write its real CWD (via pwd) to an output file passed
	// as argv[1]. The shell driver execs argv[0] directly with cmd.Dir set to
	// the worktree, so the recorded CWD must equal the worktree path.
	binDir := t.TempDir()
	recorder := filepath.Join(binDir, "record-pwd.sh")
	if err := os.WriteFile(recorder, []byte("#!/bin/sh\npwd > \"$1\"\n"), 0o755); err != nil {
		t.Fatalf("write recorder: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cfg := &config.Config{
		Agents: []config.Agent{
			{Name: "shell", SupportedProviders: nil},
		},
	}

	out := filepath.Join(t.TempDir(), "pwd.txt")
	if err := launchFiltered("shell", worktree, cfg, false, "", "", "", false, []string{recorder, out}, nil, nil); err != nil {
		t.Fatalf("launchFiltered: %v", err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read recorded pwd: %v", err)
	}
	// Resolve symlinks so macOS /var → /private/var doesn't cause a spurious
	// mismatch (the worktree and the recorder's pwd may report differently).
	gotDir, _ := filepath.EvalSymlinks(strings.TrimSpace(string(got)))
	wantDir, _ := filepath.EvalSymlinks(worktree)
	if gotDir != wantDir {
		t.Errorf("recorded CWD = %q, want %q (shell must run in the worktree)", gotDir, wantDir)
	}
}

// (TestBuildFilteredCmdCommandAgentNotesIgnoredModel removed in PR #56.
// The -M-with-command warning now lives in launchFiltered (where the
// pinnedSupplied signal is available); see
// TestLaunchFilteredWarnWhenModelPassedToCommand below for the contract test.)

// TestRunAgentCmdPrintsSummary asserts the non-TUI launch path prints the
// summary line to stdout after the subprocess exits.
func TestRunAgentCmdPrintsSummary(t *testing.T) {
	// Isolate XDG_CONFIG_HOME: runAgentCmd now reaches applyProfileForLaunch
	// for a non-command, non-native model, which calls loadProfileStore
	// (profiles.Load) and the config_content self-heal check — without
	// this a test on a machine with a real ~/.config/agent-wt/profiles.toml
	// could prompt on /dev/tty mid test-run or otherwise behave differently
	// than intended (finding #7 of the final whole-branch review).
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skip("`true` not available")
	}
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()

	cmd := exec.Command(truePath)
	if err := runAgentCmd(cmd, "claude", config.Model{ID: "claude/sonnet"}, &config.Config{}, nil); err != nil {
		t.Fatalf("runAgentCmd: %v", err)
	}
	w.Close()
	out, _ := io.ReadAll(r)
	if !strings.Contains(string(out), "wt: claude · claude/sonnet ·") {
		t.Errorf("stdout = %q, want summary line", string(out))
	}
}

// TestRunAgentCmdLeadingNewlineBeforeSummary pins the leading "\n"
// before the summary line. Without it, an agent whose last byte was
// not a newline (e.g. a bare prompt or a SIGINT-truncated line) would
// have the summary glued to that partial output — the user would see
// "…thinking about itwt: claude · claude/sonnet · 12s" instead of two
// clean lines.
func TestRunAgentCmdLeadingNewlineBeforeSummary(t *testing.T) {
	// Isolate XDG_CONFIG_HOME — see TestRunAgentCmdPrintsSummary's comment
	// (finding #7 of the final whole-branch review).
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skip("`true` not available")
	}
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()

	cmd := exec.Command(truePath)
	if err := runAgentCmd(cmd, "claude", config.Model{ID: "claude/sonnet"}, &config.Config{}, nil); err != nil {
		t.Fatalf("runAgentCmd: %v", err)
	}
	w.Close()
	out, _ := io.ReadAll(r)
	// The summary line must be preceded by a newline so the user's
	// prior output (which may lack a trailing newline) doesn't glue
	// to it. Find the summary and check the byte immediately before.
	idx := strings.Index(string(out), "wt: claude · claude/sonnet ·")
	if idx <= 0 {
		t.Fatalf("stdout = %q, want summary line", string(out))
	}
	if out[idx-1] != '\n' {
		t.Errorf("stdout[%d-1] = %q, want '\\n' before summary line. Full output: %q", idx, out[idx-1], string(out))
	}
}

// TestRunAgentCmdSurveyNoopWithoutTTY verifies runAgentCmd still returns
// normally and prints only the summary line when stdin is not a TTY (the
// state of the test process's stdin) — the post-run survey prompt must
// never hang or error a non-interactive run, and must not print any
// prompt text in that case.
func TestRunAgentCmdSurveyNoopWithoutTTY(t *testing.T) {
	// Isolate XDG_CONFIG_HOME — see TestRunAgentCmdPrintsSummary's comment
	// (finding #7 of the final whole-branch review).
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skip("`true` not available")
	}
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()

	cmd := exec.Command(truePath)
	if err := runAgentCmd(cmd, "claude", config.Model{ID: "claude/sonnet"}, &config.Config{}, nil); err != nil {
		t.Fatalf("runAgentCmd: %v", err)
	}
	w.Close()
	out, _ := io.ReadAll(r)
	if strings.Contains(string(out), "survey ·") {
		t.Errorf("stdout = %q, should not contain survey prompts when stdin is not a TTY", string(out))
	}
	if !strings.Contains(string(out), "wt: claude · claude/sonnet ·") {
		t.Errorf("stdout = %q, want the summary line", string(out))
	}
}

// TestOllamaUnavailableErrorIncludesPullHint verifies the user-facing error
// text for unavailable local ollama models.
func TestOllamaUnavailableErrorIncludesPullHint(t *testing.T) {
	err := ollamaUnavailableError("gemma4:9b")
	if err == nil {
		t.Fatal("ollamaUnavailableError returned nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, `model "gemma4:9b" is not available locally`) {
		t.Fatalf("error %q missing unavailable-model text", msg)
	}
	if !strings.Contains(msg, "ollama pull gemma4:9b") {
		t.Fatalf("error %q missing pull hint", msg)
	}
}

// TestLaunchFilteredSkipsOllamaCheckInLitellm verifies that the local ollama
// availability check is skipped when the gateway is in litellm mode. In
// gateway mode models may be served by any upstream behind LiteLLM (llama.cpp,
// oMLX, OpenRouter), so a model absent from `ollama list` must not hard-block
// the launch — the model can never be pulled because it is not an ollama model.
func TestLaunchFilteredSkipsOllamaCheckInLitellm(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("MODELMAN_REGISTRY", "")
	worktree := t.TempDir()

	// Install a fake claude binary so launchFiltered can execute without
	// requiring the real CLI on PATH.
	binDir := t.TempDir()
	claudeBin := filepath.Join(binDir, "claude")
	if err := os.WriteFile(claudeBin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cfg := &config.Config{
		DefaultTag: "code",
		Providers: []config.Provider{
			{ID: "ollama", Location: config.LocationLocal},
		},
		Models: []config.Model{
			// A model that is definitely not in `ollama list` — in direct mode
			// this would abort with the unavailable-model error.
			{ID: "ollama/remote-only-model", ProviderID: "ollama", ModelName: "remote-only-model", Tags: []string{"code"}},
		},
		Agents: []config.Agent{
			{Name: "claude", SupportedProviders: []string{"ollama"}},
		},
	}
	cfg.SetLitellmForTest(config.LitellmState{Enabled: true, URL: "http://localhost:4000", APIKey: "sk-litellm"})
	cfg.ExposeAllForTest()

	// The model must be launchable under live-truth semantics: the stubbed
	// probe reports it registered and serving, so the row is a launch row
	// and resolveModel hands it to launchFiltered — the point here is the
	// litellm-mode skip of the ollama availability check, which only runs
	// once resolution succeeds.
	stubProbeInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "ollama", ModelID: "ollama/remote-only-model", Artifact: "remote-only-model", ModelName: "remote-only-model", Registered: true, Running: true},
		},
	})

	if err := launchFiltered("claude", worktree, cfg, false, "", "", "", false, nil, nil, nil); err != nil {
		t.Fatalf("launchFiltered in litellm mode: %v", err)
	}
}

// TestLaunchFilteredSkipsOllamaCheckWhenProtocolForcesLitellm verifies that
// the pre-launch ollama-availability gate consults the per-model resolved
// route, not the raw litellm on/off toggle: codex speaks only
// openai-responses, which no local ollama provider serves, so ResolveRoute
// forces litellm regardless of the toggle. With the toggle off, the old
// cfg.IsLitellm()-only gate would spuriously run the local `ollama list`
// check and block on a model absent from it, even though the launch will
// actually go through the (upstream-served) proxy.
func TestLaunchFilteredSkipsOllamaCheckWhenProtocolForcesLitellm(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("MODELMAN_REGISTRY", "")
	worktree := t.TempDir()

	binDir := t.TempDir()
	codexBin := filepath.Join(binDir, "codex")
	if err := os.WriteFile(codexBin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cfg := &config.Config{
		DefaultTag: "code",
		Providers: []config.Provider{
			// No Protocols set: EffectiveProtocols defaults to openai-chat
			// only, which codex's openai-responses never overlaps.
			{ID: "ollama", Location: config.LocationLocal},
		},
		Models: []config.Model{
			{ID: "ollama/remote-only-model", ProviderID: "ollama", ModelName: "remote-only-model", Tags: []string{"code"}},
		},
		Agents: []config.Agent{
			{Name: "codex", SupportedProviders: []string{"ollama"}},
		},
	}
	// Toggle is off, but a URL+key are configured — codex must still force
	// litellm via the protocol mismatch, not the toggle.
	cfg.SetLitellmForTest(config.LitellmState{Enabled: false, URL: "http://localhost:4000", APIKey: "sk-litellm"})
	cfg.ExposeAllForTest()

	// The model must be launchable under live-truth semantics: the stubbed
	// probe reports it registered and serving, so the row is a launch row
	// and resolveModel hands it to launchFiltered — the point here is the
	// route-forced litellm skip of the ollama availability check, which
	// only runs once resolution succeeds.
	stubProbeInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "ollama", ModelID: "ollama/remote-only-model", Artifact: "remote-only-model", ModelName: "remote-only-model", Registered: true, Running: true},
		},
	})

	if err := launchFiltered("codex", worktree, cfg, false, "", "", "", false, nil, nil, nil); err != nil {
		t.Fatalf("launchFiltered with protocol-forced litellm: %v", err)
	}
}

// TestLaunchFilteredUsesEligibleAndSlot verifies that the non-TUI launch path
// (a) calls cfg.EligibleModels to resolve the model list, (b) consults the
// global rotation via rotation.Last/rotation.Next (no per-slot state) to
// pick the next-to-use model when no -M pin is supplied, and (c) honors the
// -M pin without consulting rotation. Rotation is global, not per
// agent+tag+family, so the eligible list is the sole source of truth for
// which models are in play. This is the pin path's contract; the rotation
// advance itself is exercised separately by TestLaunchFilteredRotationAdvances.
func TestLaunchFilteredUsesEligibleAndSlot(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("MODELMAN_REGISTRY", "")

	cfg := &config.Config{
		DefaultTag: "code",
		Providers: []config.Provider{
			{ID: "claude", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "native"}},
		},
		Models: []config.Model{
			{ID: "claude/opus", ProviderID: "claude", ModelName: "opus", Tags: []string{"code"}},
			{ID: "claude/sonnet", ProviderID: "claude", ModelName: "sonnet", Tags: []string{"code"}},
		},
		Agents: []config.Agent{
			{Name: "claude", SupportedProviders: []string{"claude"}},
		},
	}
	cfg.ExposeAllForTest()

	// Two eligible models, no -M → resolveModel errors with "multiple models
	// match". The defaultModel fallback is gone, so any ambiguous eligible
	// list surfaces a clear error. The rotation advance in launchFiltered's
	// run path wraps resolveModel so callers see the rotated model instead.
	if _, _, err := resolveModel("claude", cfg, "", "", ""); err == nil {
		t.Fatal("expected error for ambiguous eligible list")
	}

	// Pinned → resolves to claude/opus.
	m, _, err := resolveModel("claude", cfg, "", "", "claude/opus")
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "claude/opus" {
		t.Errorf("got %q, want claude/opus", m.ID)
	}

	// Global rotation state lives at rotation.state under the config dir.
	expectedPath := filepath.Join(dir, "agent-wt", "rotation.state")
	gotPath := filepath.Join(config.Dir(), "rotation.state")
	if gotPath != expectedPath {
		t.Errorf("state file = %q, want %q", gotPath, expectedPath)
	}
}

// TestLaunchFilteredRotationAdvances verifies that when launchFiltered is
// invoked repeatedly with multiple eligible models and no -M pin, the
// non-TUI launch path rotates through the global model list and records
// each launch in the global rotation.state file.
func TestLaunchFilteredRotationAdvances(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("MODELMAN_REGISTRY", "")
	worktree := t.TempDir()

	// Install a fake claude binary so launchFiltered can execute without
	// requiring the real CLI on PATH.
	binDir := t.TempDir()
	claudeBin := filepath.Join(binDir, "claude")
	if err := os.WriteFile(claudeBin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cfg := &config.Config{
		DefaultTag: "code",
		Providers: []config.Provider{
			{ID: "claude", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "native"}},
		},
		Models: []config.Model{
			{ID: "claude/a", ProviderID: "claude", ModelName: "a", Tags: []string{"code"}},
			{ID: "claude/b", ProviderID: "claude", ModelName: "b", Tags: []string{"code"}},
			{ID: "claude/c", ProviderID: "claude", ModelName: "c", Tags: []string{"code"}},
		},
		Agents: []config.Agent{
			{Name: "claude", SupportedProviders: []string{"claude"}},
		},
	}
	cfg.ExposeAllForTest()

	want := []string{"claude/a", "claude/b", "claude/c"}
	statePath := filepath.Join(dir, "agent-wt", "rotation.state")
	for i, id := range want {
		if err := launchFiltered("claude", worktree, cfg, false, "", "", "", false, nil, nil, nil); err != nil {
			t.Fatalf("launchFiltered run %d: %v", i+1, err)
		}
		data, err := os.ReadFile(statePath)
		if err != nil {
			t.Fatalf("read state file run %d: %v", i+1, err)
		}
		if got := strings.TrimSpace(string(data)); got != id {
			t.Fatalf("run %d state = %q, want %q", i+1, got, id)
		}
	}
}

// TestLaunchFilteredRotationRespectsTagFilter verifies that the rotation
// fallback in launchFiltered (triggered when resolveModel's "multiple
// models match" error fires) stays scoped to the -T filter instead of
// walking the full agent-eligible list. Before this fix, rotation.Next
// ignored tags/family and could select+record a model the user's -T
// filter explicitly excluded — a regression from the pre-PR
// EligibleModels(agent, tags, family)-scoped rotation, flagged in PR #82
// review.
func TestLaunchFilteredRotationRespectsTagFilter(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("MODELMAN_REGISTRY", "")
	worktree := t.TempDir()

	binDir := t.TempDir()
	claudeBin := filepath.Join(binDir, "claude")
	if err := os.WriteFile(claudeBin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// Global model order interleaves a code-tagged model right after the
	// last-launched design-tagged model, so a fallback that ignores the
	// -T filter would land on it.
	cfg := &config.Config{
		Providers: []config.Provider{
			{ID: "claude", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "native"}},
		},
		Models: []config.Model{
			{ID: "claude/design-a", ProviderID: "claude", ModelName: "design-a", Tags: []string{"design"}},
			{ID: "claude/code-a", ProviderID: "claude", ModelName: "code-a", Tags: []string{"code"}},
			{ID: "claude/design-b", ProviderID: "claude", ModelName: "design-b", Tags: []string{"design"}},
		},
		Agents: []config.Agent{
			{Name: "claude", SupportedProviders: []string{"claude"}},
		},
	}
	cfg.ExposeAllForTest()

	if err := rotation.New().Record("claude/design-a"); err != nil {
		t.Fatalf("seed rotation state: %v", err)
	}

	if err := launchFiltered("claude", worktree, cfg, false, "design", "", "", false, nil, nil, nil); err != nil {
		t.Fatalf("launchFiltered: %v", err)
	}

	statePath := filepath.Join(dir, "agent-wt", "rotation.state")
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state file: %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != "claude/design-b" {
		t.Fatalf("recorded model = %q, want claude/design-b (a code-tagged model leaked past the -T design filter)", got)
	}
}

// TestLaunchFilteredRecordsRefcount verifies the non-TUI launch path
// records a live-session refcount entry (this process's pid + the launched
// model) at the same commit point as rotation.Record, so the picker's "in
// use" column can see a launch made through -W/--cwd/outside-repo, not
// just the TUI.
func TestLaunchFilteredRecordsRefcount(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("MODELMAN_REGISTRY", "")
	worktree := t.TempDir()

	binDir := t.TempDir()
	claudeBin := filepath.Join(binDir, "claude")
	if err := os.WriteFile(claudeBin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cfg := &config.Config{
		DefaultTag: "code",
		Providers: []config.Provider{
			{ID: "claude", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "native"}},
		},
		Models: []config.Model{
			{ID: "claude/opus", ProviderID: "claude", ModelName: "opus", Tags: []string{"code"}},
		},
		Agents: []config.Agent{
			{Name: "claude", SupportedProviders: []string{"claude"}},
		},
	}
	cfg.ExposeAllForTest()

	if err := launchFiltered("claude", worktree, cfg, false, "", "", "", false, nil, nil, nil); err != nil {
		t.Fatalf("launchFiltered: %v", err)
	}

	store := refcount.NewStoreAt(filepath.Join(dir, "agent-wt"))
	got := store.Counts([]string{"claude/opus"})
	if got["claude/opus"] != 1 {
		t.Fatalf("refcount Counts = %d, want 1", got["claude/opus"])
	}
}

// TestLaunchFilteredRecordsUsageForAgent verifies the non-TUI launch path
// records a usage event tagged with the launching agent, so per-pair 1d/7d/30d
// counts include launches made through -W/--cwd.
func TestLaunchFilteredRecordsUsageForAgent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("MODELMAN_REGISTRY", "")
	worktree := t.TempDir()

	binDir := t.TempDir()
	claudeBin := filepath.Join(binDir, "claude")
	if err := os.WriteFile(claudeBin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cfg := &config.Config{
		DefaultTag: "code",
		Providers: []config.Provider{
			{ID: "claude", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "native"}},
		},
		Models: []config.Model{
			{ID: "claude/opus", ProviderID: "claude", ModelName: "opus", Tags: []string{"code"}},
		},
		Agents: []config.Agent{
			{Name: "claude", SupportedProviders: []string{"claude"}},
		},
	}
	cfg.ExposeAllForTest()

	if err := launchFiltered("claude", worktree, cfg, false, "", "", "", false, nil, nil, nil); err != nil {
		t.Fatalf("launchFiltered: %v", err)
	}

	store := usage.NewStoreAt(filepath.Join(dir, "agent-wt"))
	if got := store.CountsForAgent("claude", []string{"claude/opus"})["claude/opus"]; got.ThirtyDay != 1 {
		t.Fatalf("claude pair 30d = %d, want 1", got.ThirtyDay)
	}
	if got := store.CountsForAgent("codex", []string{"claude/opus"})["claude/opus"]; got.ThirtyDay != 0 {
		t.Fatalf("codex pair 30d = %d, want 0", got.ThirtyDay)
	}
}

// TestCommandAgentDoesNotRecordRefcount verifies a shell (command agent)
// launch never writes a refcount entry — command agents have no model
// layer, so there is nothing to attribute an "in use" count to. Command
// agents take the early-return path in launchFilteredImpl, before the
// record call, so this pins that control-flow contract (the design notes
// no explicit guard is needed; this test is the regression lock for that
// claim).
func TestCommandAgentDoesNotRecordRefcount(t *testing.T) {
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skip("`true` not available")
	}
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("MODELMAN_REGISTRY", "")
	worktree := t.TempDir()

	cfg := &config.Config{
		Agents: []config.Agent{{Name: "shell", SupportedProviders: nil}},
	}

	if err := launchFiltered("shell", worktree, cfg, false, "", "", "", false, []string{truePath}, nil, nil); err != nil {
		t.Fatalf("launchFiltered: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "agent-wt", "refcount.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("refcount.jsonl unexpectedly created for a command agent launch (err=%v)", err)
	}
}

// TestLaunchFilteredWarnWhenModelPassedToCommand verifies that when the
// user passes -M together with a command agent (e.g. `-A shell -M foo`),
// launchFiltered prints a stderr note `wt: -M ignored for command
// "shell"` before launching the command. Without this warning the user
// would see the command launch but the -M pin silently dropped —
// surprising and hard to debug. The note is the spec's
// error-handling row 6 contract.
//
// Note: a symmetric "no warning when -A is an agent" test is not added
// here — exercising that branch through launchFiltered requires running
// the actual agent binary, which hangs in the test environment. The
// warning's only write site is the `if pinnedSupplied && IsCommand(agent)`
// branch at launch.go:74-76; the inverse is locked by code review.
func TestLaunchFilteredWarnWhenModelPassedToCommand(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("MODELMAN_REGISTRY", "")

	cfg := &config.Config{
		DefaultTag: "code",
		Agents: []config.Agent{
			{Name: "shell", SupportedProviders: nil},
		},
	}

	// Capture stderr.
	oldStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	defer func() { os.Stderr = oldStderr }()

	// launchFiltered with -A shell -M claude/opus. The actual exec may
	// fail (no TTY, no args), but the stderr note is printed before the
	// exec, so it lands in our pipe regardless of the outcome.
	_ = launchFiltered("shell", ".", cfg, false, "", "", "claude/opus", true, nil, nil, nil)

	// Close the writer to flush the pipe and read.
	_ = w.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	if !strings.Contains(buf.String(), `wt: -M ignored for command "shell"`) {
		t.Errorf("stderr = %q; want it to contain %q", buf.String(), `wt: -M ignored for command "shell"`)
	}
}

// TestRunAgentCmdInvokesPriceNotice verifies the non-TUI launch path calls
// the stale-pricing notice emitter after the subprocess exits (issue #69) —
// the call site's position between summary and survey is reviewed code, the
// seam test only guards against the call being dropped.
func TestRunAgentCmdInvokesPriceNotice(t *testing.T) {
	// Isolate XDG_CONFIG_HOME — see TestRunAgentCmdPrintsSummary's comment
	// (finding #7 of the final whole-branch review).
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	prevNotice := emitPriceNotice
	t.Cleanup(func() { emitPriceNotice = prevNotice })

	called := false
	emitPriceNotice = func() { called = true }

	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skip("`true` not available")
	}

	cmd := exec.Command(truePath)
	if err := runAgentCmd(cmd, "claude", config.Model{ID: "ollama/qwen3.8"}, &config.Config{}, nil); err != nil {
		t.Fatalf("runAgentCmd() error: %v", err)
	}
	if !called {
		t.Error("emitPriceNotice was not invoked after the run")
	}
}

// TestRunAgentCmdNoticePrintedWithSummary verifies the stale-pricing
// notice seam is invoked and the summary line is printed in the non-TUI
// path. The test uses a captured stdout pipe; notice output (when
// emitted) is concatenated after the summary in the captured stream.
func TestRunAgentCmdNoticePrintedWithSummary(t *testing.T) {
	// Isolate XDG_CONFIG_HOME — see TestRunAgentCmdPrintsSummary's comment
	// (finding #7 of the final whole-branch review).
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	prevNotice := emitPriceNotice
	t.Cleanup(func() { emitPriceNotice = prevNotice })

	var order []string
	emitPriceNotice = func() { order = append(order, "notice") }

	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skip("`true` not available")
	}

	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w

	cmd := exec.Command(truePath)
	if err := runAgentCmd(cmd, "claude", config.Model{ID: "ollama/qwen3.8"}, &config.Config{}, nil); err != nil {
		w.Close()
		os.Stdout = old
		t.Fatalf("runAgentCmd() error: %v", err)
	}
	w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)

	// Summary must be present.
	if !strings.Contains(string(out), "wt: claude ·") {
		t.Errorf("stdout missing summary line: %q", string(out))
	}
	// The notice seam was invoked after the summary.
	if len(order) == 0 {
		t.Error("emitPriceNotice was not invoked")
	}
}

// TestRunAgentCmdSkipsPriceNoticeForCommandAgent verifies the pricing
// reminder does NOT fire for command agents (e.g. shell), which launch with
// a zero-value config.Model (m.ID == "") and never touch a priced model —
// the reminder is meaningless there and was previously firing unconditionally.
func TestRunAgentCmdSkipsPriceNoticeForCommandAgent(t *testing.T) {
	prevNotice := emitPriceNotice
	t.Cleanup(func() { emitPriceNotice = prevNotice })

	called := false
	emitPriceNotice = func() { called = true }

	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skip("`true` not available")
	}

	cmd := exec.Command(truePath)
	if err := runAgentCmd(cmd, "shell", config.Model{}, &config.Config{}, nil); err != nil {
		t.Fatalf("runAgentCmd() error: %v", err)
	}
	if called {
		t.Error("emitPriceNotice was invoked for a command agent (m.ID == \"\")")
	}
}

// TestRunAgentCmdPostExitOrder verifies the non-TUI post-exit order: the
// session's refcount entry is released, then the stop picker runs, then the
// summary line, then the pricing notice (issues #115/#116). Release must
// precede the picker or the just-used model always counts as in use; the
// summary and notice must follow the interactive steps so the prompts do not
// scroll them away. The picker stub writes a marker through os.Stdout so the
// summary's position *relative to the picker* is pinned too: the seam-only
// `order` slice cannot see it, because the summary is printed by real code
// rather than by a seam, so a summary moved above the picker would keep every
// other assertion green.
func TestRunAgentCmdPostExitOrder(t *testing.T) {
	// Isolate XDG_CONFIG_HOME — see TestRunAgentCmdPrintsSummary's comment
	// (finding #7 of the final whole-branch review).
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	prevNotice, prevRelease, prevPicker := emitPriceNotice, releaseSession, runStopPicker
	t.Cleanup(func() { emitPriceNotice, releaseSession, runStopPicker = prevNotice, prevRelease, prevPicker })

	var order []string
	releaseSession = func() { order = append(order, "release") }
	runStopPicker = func(*config.Config) {
		order = append(order, "picker")
		// Written through the redirected os.Stdout so it lands in the captured
		// output next to the real summary line.
		fmt.Fprint(os.Stdout, "PICKER-MARKER\n")
	}
	emitPriceNotice = func() { order = append(order, "notice") }

	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skip("`true` not available")
	}
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	runErr := runAgentCmd(exec.Command(truePath), "claude", config.Model{ID: "ollama/qwen3.8"}, &config.Config{}, nil)
	w.Close()
	os.Stdout = old
	if runErr != nil {
		t.Fatalf("runAgentCmd: %v", runErr)
	}
	out, _ := io.ReadAll(r)
	s := string(out)
	if !strings.Contains(s, "wt: claude · ollama/qwen3.8 ·") {
		t.Errorf("stdout = %q, want the summary line", s)
	}
	if i, j := strings.Index(s, "PICKER-MARKER"), strings.Index(s, "wt: claude · ollama/qwen3.8 ·"); i < 0 || j < 0 || i > j {
		t.Errorf("stdout = %q, want the picker marker before the summary line", s)
	}
	if got := strings.Join(order, ","); got != "release,picker,notice" {
		t.Errorf("order = %s, want release,picker,notice", got)
	}
}

// TestRunAgentCmdCommandAgentSkipsStopPicker verifies command agents (shell,
// m.ID == "") never offer to stop models: they launched none, so the picker
// would be noise after every shell session.
func TestRunAgentCmdCommandAgentSkipsStopPicker(t *testing.T) {
	prev := runStopPicker
	t.Cleanup(func() { runStopPicker = prev })
	called := false
	runStopPicker = func(*config.Config) { called = true }

	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skip("`true` not available")
	}
	if err := runAgentCmd(exec.Command(truePath), "shell", config.Model{}, &config.Config{}, nil); err != nil {
		t.Fatalf("runAgentCmd: %v", err)
	}
	if called {
		t.Error("stop picker ran for a command agent")
	}
}

// TestRunAgentCmdNativeModelSkipsStopPicker verifies a native model (which runs
// no local server) never offers to stop models after the session: the picker
// would list unrelated idle models the session had nothing to do with.
func TestRunAgentCmdNativeModelSkipsStopPicker(t *testing.T) {
	prev := runStopPicker
	t.Cleanup(func() { runStopPicker = prev })
	called := false
	runStopPicker = func(*config.Config) { called = true }

	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skip("`true` not available")
	}
	if err := runAgentCmd(exec.Command(truePath), "claude", config.Model{ID: "claude/native", Native: true}, &config.Config{}, nil); err != nil {
		t.Fatalf("runAgentCmd: %v", err)
	}
	if called {
		t.Error("stop picker ran for a native model")
	}
}

// TestRunAgentCmdAppliesConfirmedProfile verifies a matching, TTY-confirmed
// profile's Env lands on the launched process's environment — the
// end-to-end path from resolution through ApplyEnvAndArgs, exercised through
// runAgentCmd itself (not just the internal/profiles unit tests) so a
// wiring mistake in launch.go is caught here.
func TestRunAgentCmdAppliesConfirmedProfile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "agent-wt"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `
[[profiles]]
agent = "claude"
match = "location"
location = "local"
env = { WT_TEST_PROFILE_APPLIED = "1" }
`
	if err := os.WriteFile(filepath.Join(dir, "agent-wt", "profiles.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	oldConfirm := confirmProfile
	confirmProfile = func(profiles.ResolvedProfile) (bool, error) { return true, nil }
	t.Cleanup(func() { confirmProfile = oldConfirm })

	cfg := &config.Config{Providers: []config.Provider{{ID: "ollama", Location: config.LocationLocal}}}
	m := config.Model{ID: "ollama/x", ProviderID: "ollama", ModelName: "x"}
	cmd := exec.Command("true")
	if err := runAgentCmd(cmd, "claude", m, cfg, nil); err != nil {
		t.Fatalf("runAgentCmd() error = %v", err)
	}
	found := false
	for _, e := range cmd.Env {
		if e == "WT_TEST_PROFILE_APPLIED=1" {
			found = true
		}
	}
	if !found {
		t.Errorf("cmd.Env = %v, want WT_TEST_PROFILE_APPLIED=1", cmd.Env)
	}
}

// TestRunAgentCmdSkipsProfileWhenDeclined verifies declining the confirm
// prompt runs the agent completely unprofiled — the "use default" branch
// of the prompt must actually skip application, not just skip the prompt
// text.
func TestRunAgentCmdSkipsProfileWhenDeclined(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "agent-wt"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `
[[profiles]]
agent = "claude"
match = "location"
location = "local"
env = { WT_TEST_PROFILE_APPLIED = "1" }
`
	if err := os.WriteFile(filepath.Join(dir, "agent-wt", "profiles.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	oldConfirm := confirmProfile
	confirmProfile = func(profiles.ResolvedProfile) (bool, error) { return false, nil }
	t.Cleanup(func() { confirmProfile = oldConfirm })

	cfg := &config.Config{Providers: []config.Provider{{ID: "ollama", Location: config.LocationLocal}}}
	m := config.Model{ID: "ollama/x", ProviderID: "ollama", ModelName: "x"}
	cmd := exec.Command("true")
	if err := runAgentCmd(cmd, "claude", m, cfg, nil); err != nil {
		t.Fatalf("runAgentCmd() error = %v", err)
	}
	for _, e := range cmd.Env {
		if e == "WT_TEST_PROFILE_APPLIED=1" {
			t.Errorf("cmd.Env = %v, profile was declined but applied anyway", cmd.Env)
		}
	}
}

// TestRunAgentCmdGlobalOffSkipsPromptAndApplication verifies `enabled =
// false` in profiles.toml is a true kill switch: no prompt (confirmProfile
// must not even be called) and no application, even when a profile would
// otherwise match.
func TestRunAgentCmdGlobalOffSkipsPromptAndApplication(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "agent-wt"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `
enabled = false

[[profiles]]
agent = "claude"
match = "location"
location = "local"
env = { WT_TEST_PROFILE_APPLIED = "1" }
`
	if err := os.WriteFile(filepath.Join(dir, "agent-wt", "profiles.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	promptCalled := false
	oldConfirm := confirmProfile
	confirmProfile = func(profiles.ResolvedProfile) (bool, error) { promptCalled = true; return true, nil }
	t.Cleanup(func() { confirmProfile = oldConfirm })

	cfg := &config.Config{Providers: []config.Provider{{ID: "ollama", Location: config.LocationLocal}}}
	m := config.Model{ID: "ollama/x", ProviderID: "ollama", ModelName: "x"}
	cmd := exec.Command("true")
	if err := runAgentCmd(cmd, "claude", m, cfg, nil); err != nil {
		t.Fatalf("runAgentCmd() error = %v", err)
	}
	if promptCalled {
		t.Error("confirmProfile was called with profiles disabled — it must never be reached")
	}
}

// TestRunAgentCmdSkipsProfileForCommandAndNativeModels verifies command
// agents (m.ID == "") and native models never trigger profile resolution
// at all — confirmProfile must not be called, matching the existing
// "no priced model, no survey" convention for both cases.
func TestRunAgentCmdSkipsProfileForCommandAndNativeModels(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	promptCalled := false
	oldConfirm := confirmProfile
	confirmProfile = func(profiles.ResolvedProfile) (bool, error) { promptCalled = true; return true, nil }
	t.Cleanup(func() { confirmProfile = oldConfirm })

	cfg := &config.Config{}
	if err := runAgentCmd(exec.Command("true"), "shell", config.Model{}, cfg, nil); err != nil {
		t.Fatalf("runAgentCmd() error = %v", err)
	}
	if promptCalled {
		t.Error("confirmProfile called for a command agent (m.ID == \"\")")
	}
	if err := runAgentCmd(exec.Command("true"), "claude", config.Model{ID: "claude/native", Native: true, ModelName: "native"}, cfg, nil); err != nil {
		t.Fatalf("runAgentCmd() error = %v", err)
	}
	if promptCalled {
		t.Error("confirmProfile called for a native model")
	}
}

// TestRunAgentCmdMalformedProfilesTomlDegradesGracefully verifies a
// syntax error in profiles.toml warns to stderr and still launches
// normally — a user's typo in a hand-edited file must never block an
// agent launch.
func TestRunAgentCmdMalformedProfilesTomlDegradesGracefully(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "agent-wt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent-wt", "profiles.toml"), []byte("not [ valid"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldConfirm := confirmProfile
	confirmProfile = func(profiles.ResolvedProfile) (bool, error) {
		t.Fatal("confirmProfile called despite a malformed profiles.toml")
		return false, nil
	}
	t.Cleanup(func() { confirmProfile = oldConfirm })

	cfg := &config.Config{Providers: []config.Provider{{ID: "ollama", Location: config.LocationLocal}}}
	m := config.Model{ID: "ollama/x", ProviderID: "ollama", ModelName: "x"}
	if err := runAgentCmd(exec.Command("true"), "claude", m, cfg, nil); err != nil {
		t.Fatalf("runAgentCmd() error = %v, want a normal launch despite the malformed file", err)
	}
}

// TestRunAgentCmdProfileApplyErrorReleasesAndPrintsSummary is the
// regression lock for the code-review finding that a pre-launch profile
// application error (e.g. a wrapper profile naming a missing binary) used
// to return immediately from runAgentCmd, skipping releaseSession() and the
// summary line — unlike the ollama-check failure path in launchFilteredImpl,
// which explicitly prints the summary before returning. This verifies
// cmd.Run() never executes (the sentinel file is never created), the
// refcount entry is released, and the summary line is printed with a 0
// duration.
func TestRunAgentCmdProfileApplyErrorReleasesAndPrintsSummary(t *testing.T) {
	oldConfirm := confirmProfile
	confirmProfile = func(profiles.ResolvedProfile) (bool, error) { return true, nil }
	t.Cleanup(func() { confirmProfile = oldConfirm })

	released := false
	oldRelease := releaseSession
	releaseSession = func() { released = true }
	t.Cleanup(func() { releaseSession = oldRelease })

	pp := &precomputedProfiles{store: profiles.Store{Enabled: true, Profiles: []profiles.Profile{
		{Agent: "pi", Match: "location", Location: "local",
			Wrapper: &profiles.WrapperSpec{Binary: "wt-test-definitely-missing-binary-xyz", ArgsTemplate: []string{"{{args}}"}}},
	}}}
	cfg := &config.Config{Providers: []config.Provider{{ID: "ollama", Location: config.LocationLocal}}}
	m := config.Model{ID: "ollama/x", ProviderID: "ollama", ModelName: "x"}

	sentinelDir := t.TempDir()
	sentinel := filepath.Join(sentinelDir, "should-not-exist")
	cmd := exec.Command("touch", sentinel)

	r, w, perr := os.Pipe()
	if perr != nil {
		t.Fatal(perr)
	}
	oldStdout := os.Stdout
	os.Stdout = w

	err := runAgentCmd(cmd, "pi", m, cfg, pp)

	w.Close()
	os.Stdout = oldStdout
	out, _ := io.ReadAll(r)

	if err == nil {
		t.Fatal("runAgentCmd() error = nil, want the wrapper-not-installed error")
	}
	if !strings.Contains(err.Error(), "not installed") {
		t.Errorf("error = %v, want a wrapper-not-installed error", err)
	}
	if _, statErr := os.Stat(sentinel); !os.IsNotExist(statErr) {
		t.Error("sentinel file exists — cmd.Run() executed despite the profile apply error")
	}
	if !released {
		t.Error("releaseSession() was not called on a pre-launch profile apply error")
	}
	if !strings.Contains(string(out), "wt: pi · ollama/x") {
		t.Errorf("stdout = %q, want the summary line printed", out)
	}
}

// TestPromptProfileNoTTYDefaultsToApply verifies promptProfile itself
// (not the confirmProfile seam other tests in this file swap out) applies
// automatically — returns (true, nil) — when /dev/tty cannot be opened.
// This is the actual behavior a non-interactive launch (script, wt smoke,
// CI) relies on; it's tested via the openTTY seam so the test is
// deterministic and never hangs waiting on real terminal input regardless
// of whether the test runner happens to have a controlling terminal.
func TestPromptProfileNoTTYDefaultsToApply(t *testing.T) {
	old := openTTY
	openTTY = func() (*os.File, error) { return nil, errors.New("no tty") }
	t.Cleanup(func() { openTTY = old })

	apply, err := promptProfile(profiles.ResolvedProfile{Sources: []string{"location=local"}})
	if err != nil {
		t.Fatalf("promptProfile() error = %v", err)
	}
	if !apply {
		t.Error("promptProfile() apply = false, want true (no TTY available → auto-apply)")
	}
}

// TestApplyProfileForLaunchDegradesOnConfirmError verifies the design's
// global constraint directly on applyProfileForLaunch (not routed through
// runAgentCmd/os.Exit): a confirmProfile error — e.g. a /dev/tty write
// failure inside the real promptProfile — must degrade to a normal,
// unprofiled launch (nil error, a noop cleanup, no env applied), never
// abort the agent launch outright. This is the regression lock for the
// PR-review finding that the original code returned the error and hard-
// failed the whole launch.
func TestApplyProfileForLaunchDegradesOnConfirmError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "agent-wt"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `
[[profiles]]
agent = "claude"
match = "location"
location = "local"
env = { WT_TEST_PROFILE_APPLIED = "1" }
`
	if err := os.WriteFile(filepath.Join(dir, "agent-wt", "profiles.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	oldConfirm := confirmProfile
	confirmProfile = func(profiles.ResolvedProfile) (bool, error) { return false, errors.New("tty write failed") }
	t.Cleanup(func() { confirmProfile = oldConfirm })

	cfg := &config.Config{Providers: []config.Provider{{ID: "ollama", Location: config.LocationLocal}}}
	m := config.Model{ID: "ollama/x", ProviderID: "ollama", ModelName: "x"}
	cmd := exec.Command("true")

	cleanup, err := applyProfileForLaunch(cmd, "claude", m, cfg, nil)
	if err != nil {
		t.Fatalf("applyProfileForLaunch() error = %v, want nil (a confirm-prompt error must degrade to an unprofiled launch, not fail it)", err)
	}
	if cerr := cleanup(); cerr != nil {
		t.Errorf("cleanup() error = %v, want nil (noop)", cerr)
	}
	for _, e := range cmd.Env {
		if e == "WT_TEST_PROFILE_APPLIED=1" {
			t.Errorf("cmd.Env = %v, profile env applied despite a confirmProfile error", cmd.Env)
		}
	}
}

// TestApplyProfileForLaunchUsesPrecomputedStoreWithoutReloading verifies a
// non-nil pp is used directly instead of calling loadProfileStore() again
// — newApp() already loaded and validated profiles.toml once at startup,
// and applyProfileForLaunch used to always reload/re-validate it a second
// time regardless. loadProfileStore is stubbed to fail the test if called
// at all, so this only passes if pp's store is what actually got resolved.
func TestApplyProfileForLaunchUsesPrecomputedStoreWithoutReloading(t *testing.T) {
	oldLoad := loadProfileStore
	loadProfileStore = func() (profiles.Store, error) {
		t.Fatal("loadProfileStore() called despite a precomputed pp being supplied")
		return profiles.Store{}, nil
	}
	t.Cleanup(func() { loadProfileStore = oldLoad })

	oldConfirm := confirmProfile
	confirmProfile = func(profiles.ResolvedProfile) (bool, error) { return true, nil }
	t.Cleanup(func() { confirmProfile = oldConfirm })

	pp := &precomputedProfiles{store: profiles.Store{Enabled: true, Profiles: []profiles.Profile{
		{Agent: "claude", Match: "location", Location: "local", Env: map[string]string{"WT_TEST_PRECOMPUTED": "1"}},
	}}}
	cfg := &config.Config{Providers: []config.Provider{{ID: "ollama", Location: config.LocationLocal}}}
	m := config.Model{ID: "ollama/x", ProviderID: "ollama", ModelName: "x"}
	cmd := exec.Command("true")

	cleanup, err := applyProfileForLaunch(cmd, "claude", m, cfg, pp)
	if err != nil {
		t.Fatalf("applyProfileForLaunch() error = %v, want nil", err)
	}
	t.Cleanup(func() { cleanup() })

	found := false
	for _, e := range cmd.Env {
		if e == "WT_TEST_PRECOMPUTED=1" {
			found = true
		}
	}
	if !found {
		t.Errorf("cmd.Env = %v, want the precomputed store's profile applied", cmd.Env)
	}
}

// TestApplyResolvedProfileRestoresConfigContentWhenWrapperMissing verifies
// the second PR-review finding: when ApplyWrapper fails on the one
// legitimately-fatal case (a wrapper profile naming a missing binary)
// AFTER ApplyConfigContent already wrote/backed-up a real config file,
// applyResolvedProfile must still invoke the already-obtained content
// cleanup before returning the error — otherwise a profile combining
// config_content and wrapper would leak the rewritten file on disk for
// this launch (it does self-heal on a later launch targeting the same
// path, but not this one). No Phase-1 agent declares both mechanisms
// together (so this can't be exercised through a real, Validate-passing
// profiles.toml entry), which is exactly why this test calls
// applyResolvedProfile directly with a hand-built ResolvedProfile.
func TestApplyResolvedProfileRestoresConfigContentWhenWrapperMissing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir) // profiles' backupDir() lives under config.Dir()
	worktree := t.TempDir()
	target := filepath.Join(worktree, ".claude", "settings.local.json")

	rp := profiles.ResolvedProfile{
		ConfigContent: map[string]any{"ANTHROPIC_DEFAULT_SONNET_MODEL": "test-model"},
		Wrapper:       &profiles.WrapperSpec{Binary: "wt-test-definitely-missing-binary-xyz", ArgsTemplate: []string{"{{args}}"}},
	}
	cmd := exec.Command("true")
	cmd.Dir = worktree

	cleanup, err := applyResolvedProfile(cmd, "claude", rp, nil)
	if err == nil {
		t.Fatal("applyResolvedProfile() error = nil, want an error (missing wrapper binary is the one legitimately-fatal case)")
	}
	if !strings.Contains(err.Error(), "not installed") {
		t.Errorf("error = %v, want a wrapper-not-installed error", err)
	}
	// The config file ApplyConfigContent wrote (it did not exist before)
	// must already be gone — proof that applyResolvedProfile invoked the
	// content cleanup itself before returning the wrapper error, rather
	// than discarding it.
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Errorf("target file %q still exists after a wrapper error — contentCleanup was not invoked", target)
	}
	if cerr := cleanup(); cerr != nil {
		t.Errorf("returned cleanup() error = %v, want nil (noop, since real cleanup already ran)", cerr)
	}
}

// TestApplyResolvedProfileExtraArgsPrecedeCodexProfileFlag is the
// regression lock for the code-review finding that a codex profile
// combining the args and config_file mechanisms (both declared by codex's
// ProfileMechanisms, and therefore Validate-legal) placed the profile's own
// ExtraArgs AFTER the config_content-driven "--profile agent-wt-profile"
// flag instead of before it. This hand-builds a ResolvedProfile with both
// (the only way to exercise it — no Phase-1 example profile combines them)
// and asserts the ExtraArgs appear before "--profile" in cmd.Args.
func TestApplyResolvedProfileExtraArgsPrecedeCodexProfileFlag(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", t.TempDir())

	rp := profiles.ResolvedProfile{
		ExtraArgs:     []string{"-c", "model_reasoning_effort=\"low\""},
		ConfigContent: map[string]any{"some_key": "some_value"},
	}
	cmd := exec.Command("codex", "--model", "x")

	cleanup, err := applyResolvedProfile(cmd, "codex", rp, nil)
	if err != nil {
		t.Fatalf("applyResolvedProfile() error = %v", err)
	}
	t.Cleanup(func() { _ = cleanup() })

	want := []string{"codex", "--model", "x", "-c", "model_reasoning_effort=\"low\"", "--profile", "agent-wt-profile"}
	if strings.Join(cmd.Args, "|") != strings.Join(want, "|") {
		t.Errorf("cmd.Args = %v, want %v (ExtraArgs before --profile)", cmd.Args, want)
	}
}

// TestApplyResolvedProfileOneShotArgsAfterProfileFlags is the regression
// lock for the wt-smoke codex bug (debugged 2026-09-25): wt smoke appends a
// codex profile's ExtraArgs ("-c model_reasoning_effort=...") via
// applyResolvedProfile, and separately appends the one-shot invocation
// ("exec <prompt>") via agents.OneShotRunner.OneShotArgs. codex's CLI
// silently drops the earlier "-c model_provider=..." override (falling back
// to its default "openai" provider, which then fails auth against the real
// OpenAI API) whenever ANY "-c"/"--profile" flag trails "exec <prompt>" in
// argv — confirmed by hand-reproducing the exact argv wt smoke built, with
// only the flag order changed. So oneShotArgs must be threaded through
// applyResolvedProfile and land AFTER every profile-injected flag
// (ExtraArgs, then config_content's "--profile" flag), never before.
func TestApplyResolvedProfileOneShotArgsAfterProfileFlags(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", t.TempDir())

	rp := profiles.ResolvedProfile{
		ExtraArgs:     []string{"-c", "model_reasoning_effort=\"low\""},
		ConfigContent: map[string]any{"some_key": "some_value"},
	}
	cmd := exec.Command("codex", "-c", "model_provider=agent-wt", "--model", "x")
	oneShotArgs := []string{"exec", "the prompt"}

	cleanup, err := applyResolvedProfile(cmd, "codex", rp, oneShotArgs)
	if err != nil {
		t.Fatalf("applyResolvedProfile() error = %v", err)
	}
	t.Cleanup(func() { _ = cleanup() })

	want := []string{
		"codex", "-c", "model_provider=agent-wt", "--model", "x",
		"-c", "model_reasoning_effort=\"low\"", "--profile", "agent-wt-profile",
		"exec", "the prompt",
	}
	if strings.Join(cmd.Args, "|") != strings.Join(want, "|") {
		t.Errorf("cmd.Args = %v, want %v (oneShotArgs after every profile-injected flag)", cmd.Args, want)
	}
}

// TestApplyResolvedProfileOneShotArgsSplicedIntoWrapper verifies the
// counterpart invariant: a profile using the wrapper mechanism (e.g. pi's
// little-coder) must still see the one-shot prompt args in its {{args}}
// splice, since little-coder "forwards unrecognized args straight to pi"
// (profiles.toml) — the prompt has to be part of what gets wrapped, not
// appended after the wrapped command.
func TestApplyResolvedProfileOneShotArgsSplicedIntoWrapper(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	rp := profiles.ResolvedProfile{
		Wrapper: &profiles.WrapperSpec{Binary: "true", ArgsTemplate: []string{"--wrapped", "{{args}}"}},
	}
	cmd := exec.Command("pi", "--model", "x")
	oneShotArgs := []string{"-p", "the prompt"}

	cleanup, err := applyResolvedProfile(cmd, "pi", rp, oneShotArgs)
	if err != nil {
		t.Fatalf("applyResolvedProfile() error = %v", err)
	}
	t.Cleanup(func() { _ = cleanup() })

	truePath, lookErr := exec.LookPath("true")
	if lookErr != nil {
		t.Fatalf("exec.LookPath(true) error = %v", lookErr)
	}
	// ApplyWrapper drops the pre-wrap argv[0] ("pi") by design (see its own
	// doc comment) — everything after it, oneShotArgs included, is spliced
	// into "{{args}}" verbatim.
	want := []string{truePath, "--wrapped", "--model", "x", "-p", "the prompt"}
	if strings.Join(cmd.Args, "|") != strings.Join(want, "|") {
		t.Errorf("cmd.Args = %v, want %v (oneShotArgs spliced inside the wrapper, not appended after it)", cmd.Args, want)
	}
}

// TestApplyResolvedProfileEnvArgsRevertedWhenConfigContentFails is the
// regression lock for the final-review finding on Task 7 (commit 124971d):
// reordering applyResolvedProfile to run ApplyEnvAndArgs BEFORE
// ApplyConfigContent (needed so codex's own ExtraArgs precede its
// config_content-driven "--profile" flag) introduced a side effect the plan
// didn't account for — if ApplyConfigContent then fails (unwritable config
// dir, a lock error, …), cmd.Env/cmd.Args already carry the profile's
// Env/ExtraArgs even though the caller is told "profiles disabled for this
// launch" and the launch is supposed to degrade to fully unprofiled. This
// forces ApplyConfigContent to fail deterministically by making claude's
// config_content target's parent directory (.claude) a plain file instead
// of a directory, so the write's MkdirAll fails with ENOTDIR — no
// permission tricks needed. Asserts the launch still degrades gracefully
// (err == nil) AND that cmd.Env/cmd.Args carry none of the profile's
// Env/ExtraArgs afterward — a half-profiled launch would be a real
// correctness bug, not a hypothetical one, since claude's own Phase-1
// profile example combines env and config_file.
func TestApplyResolvedProfileEnvArgsRevertedWhenConfigContentFails(t *testing.T) {
	worktree := t.TempDir()
	// Make .claude a regular file so ApplyConfigContent's write into
	// .claude/settings.local.json fails deterministically (MkdirAll on a
	// path that already exists as a non-directory returns ENOTDIR).
	if err := os.WriteFile(filepath.Join(worktree, ".claude"), []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("seed .claude as a file: %v", err)
	}

	rp := profiles.ResolvedProfile{
		Env:           map[string]string{"X": "1"},
		ExtraArgs:     []string{"--extra"},
		ConfigContent: map[string]any{"some_key": "some_value"},
	}
	cmd := exec.Command("true", "--model", "x")
	cmd.Dir = worktree
	origArgs := append([]string{}, cmd.Args...)

	cleanup, err := applyResolvedProfile(cmd, "claude", rp, nil)
	if err != nil {
		t.Fatalf("applyResolvedProfile() error = %v, want nil (a config_content failure must still degrade to an unprofiled launch)", err)
	}
	t.Cleanup(func() { _ = cleanup() })

	for _, e := range cmd.Env {
		if e == "X=1" {
			t.Errorf("cmd.Env = %v, contains profile Env \"X=1\" despite config_content failure — launch is half-profiled", cmd.Env)
		}
	}
	if strings.Join(cmd.Args, "|") != strings.Join(origArgs, "|") {
		t.Errorf("cmd.Args = %v, want unchanged %v (ExtraArgs must be reverted when config_content fails)", cmd.Args, origArgs)
	}
}

// TestApplyProfileForLaunchSelfHealsEvenWhenProfilesDisabled is the
// regression lock for Important finding #4 (partial fix): a config_content
// file left behind by a PRIOR session that never restored it (wt killed
// mid-launch, e.g. `kill -9`) must be self-healed on EVERY launch of that
// agent — not only the next launch that itself happens to resolve a
// matching profile for that exact target. This test seeds an orphaned
// backup and then calls applyProfileForLaunch with NO profiles.toml on
// disk at all (so nothing could possibly match for THIS launch), proving
// the self-heal runs unconditionally, before profiles.toml is even loaded.
func TestApplyProfileForLaunchSelfHealsEvenWhenProfilesDisabled(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	worktree := t.TempDir()
	target := filepath.Join(worktree, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	handEdited := []byte(`{"hooks":{"my-own":"thing"}}`)
	if err := os.WriteFile(target, handEdited, 0o644); err != nil {
		t.Fatal(err)
	}

	// A prior "session" wrote profile content over the hand-edited file and
	// never called cleanup — simulates `kill -9` before the restore ran.
	rp := profiles.ResolvedProfile{ConfigContent: map[string]any{"env": map[string]any{"X": "1"}}}
	priorCmd := exec.Command("claude")
	if _, err := profiles.ApplyConfigContent(priorCmd, "claude", worktree, rp); err != nil {
		t.Fatalf("seed orphaned backup: %v", err)
	}

	// No profiles.toml exists at all for THIS launch — nothing could match
	// even if the layer were enabled. The self-heal must still restore the
	// orphaned file.
	cfg := &config.Config{Providers: []config.Provider{{ID: "ollama", Location: config.LocationLocal}}}
	m := config.Model{ID: "ollama/x", ProviderID: "ollama", ModelName: "x"}
	cmd := exec.Command("true")
	cmd.Dir = worktree

	cleanup, err := applyProfileForLaunch(cmd, "claude", m, cfg, nil)
	if err != nil {
		t.Fatalf("applyProfileForLaunch() error = %v", err)
	}
	t.Cleanup(func() { _ = cleanup() })

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	var gotDoc, wantDoc map[string]any
	if err := json.Unmarshal(got, &gotDoc); err != nil {
		t.Fatalf("self-healed content is not valid JSON: %v (%s)", err, got)
	}
	if err := json.Unmarshal(handEdited, &wantDoc); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotDoc, wantDoc) {
		t.Errorf("target = %v, want the orphaned hand-edited content restored (self-heal must run unconditionally, even with profiles disabled/absent)", gotDoc)
	}
}

// TestApplyProfileForLaunchSelfHealsForNativeModelLaunch is the regression
// lock for the code-review finding that self-heal used to run AFTER the
// early-return guard for native models/command agents, contradicting its
// own doc comment ("runs UNCONDITIONALLY, on every launch of that agent").
// A native-model launch never resolves or applies a profile of its own
// (ResolveRoute returns a zero Route), but it must still self-heal an
// orphaned config_content backup left by a PRIOR session's non-native
// launch of the same agent.
func TestApplyProfileForLaunchSelfHealsForNativeModelLaunch(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	worktree := t.TempDir()
	target := filepath.Join(worktree, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	handEdited := []byte(`{"hooks":{"my-own":"thing"}}`)
	if err := os.WriteFile(target, handEdited, 0o644); err != nil {
		t.Fatal(err)
	}
	rp := profiles.ResolvedProfile{ConfigContent: map[string]any{"env": map[string]any{"X": "1"}}}
	priorCmd := exec.Command("claude")
	if _, err := profiles.ApplyConfigContent(priorCmd, "claude", worktree, rp); err != nil {
		t.Fatalf("seed orphaned backup: %v", err)
	}

	cfg := &config.Config{Providers: []config.Provider{{ID: "ollama", Location: config.LocationLocal}}}
	m := config.Model{ID: "claude/native", Native: true, ModelName: "native"}
	cmd := exec.Command("true")
	cmd.Dir = worktree

	cleanup, err := applyProfileForLaunch(cmd, "claude", m, cfg, nil)
	if err != nil {
		t.Fatalf("applyProfileForLaunch() error = %v", err)
	}
	t.Cleanup(func() { _ = cleanup() })

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	var gotDoc, wantDoc map[string]any
	if err := json.Unmarshal(got, &gotDoc); err != nil {
		t.Fatalf("self-healed content is not valid JSON: %v (%s)", err, got)
	}
	if err := json.Unmarshal(handEdited, &wantDoc); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotDoc, wantDoc) {
		t.Errorf("target = %v, want the orphaned hand-edited content restored on a native-model launch too", gotDoc)
	}
}

// TestApplyProfileForLaunchSelfHealPrintsNotice verifies a successful
// self-heal prints the one-line stderr notice the design calls for, so the
// user knows a file was restored from a previous session rather than
// silently changing under them.
func TestApplyProfileForLaunchSelfHealPrintsNotice(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	worktree := t.TempDir()
	target := filepath.Join(worktree, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(`{"hooks":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	rp := profiles.ResolvedProfile{ConfigContent: map[string]any{"env": map[string]any{"X": "1"}}}
	priorCmd := exec.Command("claude")
	if _, err := profiles.ApplyConfigContent(priorCmd, "claude", worktree, rp); err != nil {
		t.Fatalf("seed orphaned backup: %v", err)
	}

	cfg := &config.Config{Providers: []config.Provider{{ID: "ollama", Location: config.LocationLocal}}}
	m := config.Model{ID: "ollama/x", ProviderID: "ollama", ModelName: "x"}
	cmd := exec.Command("true")
	cmd.Dir = worktree

	oldStderr := os.Stderr
	r, w, perr := os.Pipe()
	if perr != nil {
		t.Fatal(perr)
	}
	os.Stderr = w
	cleanup, err := applyProfileForLaunch(cmd, "claude", m, cfg, nil)
	w.Close()
	os.Stderr = oldStderr
	if err != nil {
		t.Fatalf("applyProfileForLaunch() error = %v", err)
	}
	defer func() { _ = cleanup() }()

	out, _ := io.ReadAll(r)
	if !strings.Contains(string(out), "wt: restored a leftover profile-managed file from a previous session: "+target) {
		t.Errorf("stderr = %q, want the self-heal notice naming %q", string(out), target)
	}
}

// TestApplyProfileForLaunchSelfHealSkipsSilentlyOnTargetError verifies
// that when ConfigFileTarget itself fails (e.g. codex's $HOME can't be
// resolved), the self-heal step is skipped without failing the launch or
// printing a spurious notice — matching the graceful-degradation posture
// of every other profile step in applyProfileForLaunch.
func TestApplyProfileForLaunchSelfHealSkipsSilentlyOnTargetError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", "") // codex's ConfigFileTarget calls os.UserHomeDir(), which fails with $HOME unset

	cfg := &config.Config{Providers: []config.Provider{{ID: "ollama", Location: config.LocationLocal}}}
	m := config.Model{ID: "ollama/x", ProviderID: "ollama", ModelName: "x"}
	cmd := exec.Command("true")

	oldStderr := os.Stderr
	r, w, perr := os.Pipe()
	if perr != nil {
		t.Fatal(perr)
	}
	os.Stderr = w
	cleanup, err := applyProfileForLaunch(cmd, "codex", m, cfg, nil)
	w.Close()
	os.Stderr = oldStderr
	if err != nil {
		t.Fatalf("applyProfileForLaunch() error = %v, want nil (a self-heal ConfigFileTarget error must never fail the launch)", err)
	}
	if cerr := cleanup(); cerr != nil {
		t.Errorf("cleanup() error = %v, want nil", cerr)
	}
	out, _ := io.ReadAll(r)
	if strings.Contains(string(out), "restored a leftover") {
		t.Errorf("stderr = %q, want no self-heal notice when the target couldn't even be resolved", string(out))
	}
}
