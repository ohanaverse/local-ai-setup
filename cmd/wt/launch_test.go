package main

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/agent-worktree/internal/config"
	"github.com/ohanaverse/agent-worktree/internal/initseed"
	"github.com/ohanaverse/agent-worktree/internal/rotation"
	"github.com/ohanaverse/agent-worktree/internal/session"
)

// TestDefaultAgentFromConfig asserts that when the config lists agents, the
// first one is the default. This is the non-TUI equivalent of the TUI's
// default-agent resolution: the launch path must pick a sensible agent
// without --agent.
func TestDefaultAgentFromConfig(t *testing.T) {
	cfg := &config.Config{
		Agents: []config.Agent{
			{Name: "codex"},
			{Name: "claude"},
		},
	}
	if got := cfg.DefaultAgent(); got != "codex" {
		t.Errorf("DefaultAgent = %q, want %q", got, "codex")
	}
}

// TestDefaultAgentFallback asserts that an empty or nil config falls back to
// "claude". Without this, a first run with no config would launch nothing.
func TestDefaultAgentFallback(t *testing.T) {
	if got := (&config.Config{}).DefaultAgent(); got != "claude" {
		t.Errorf("DefaultAgent(empty) = %q, want %q", got, "claude")
	}
	if got := (*config.Config)(nil).DefaultAgent(); got != "claude" {
		t.Errorf("DefaultAgent(nil) = %q, want %q", got, "claude")
	}
}

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
	cmd, err := buildLaunch("claude", config.Model{ID: "ollama/kimi-k2.7-code:cloud", ModelName: "kimi-k2.7-code:cloud"}, "/tmp/repo", false,
		&session.Session{ID: "abc-123", MTime: time.Now()}, nil, nil)
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
	cmd, err := buildLaunch("opencode", config.Model{ID: "ollama/gemma4:9b"}, "/tmp/repo", false,
		&session.Session{ID: "proj-123.json", MTime: time.Now()}, nil, nil)
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
	cmd, err := buildLaunch("claude", config.Model{ID: "ollama/kimi-k2.7-code:cloud", ModelName: "kimi-k2.7-code:cloud"}, "/tmp/repo", false, nil, nil, nil)
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
	cfg := &config.Config{Models: []config.Model{
		{ID: "ollama/deepseek-v4-pro:cloud", ModelName: "deepseek-v4-pro:cloud"},
	}}
	m := config.Model{ID: "ollama/deepseek-v4-pro:cloud", ModelName: "deepseek-v4-pro:cloud"}
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
		if !strings.Contains(got, "--model deepseek-v4-pro:cloud") {
			t.Errorf("args = %q, want --model deepseek-v4-pro:cloud (sync + verify)", got)
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

	// Two models, no -M → resolveModel errors with "multiple models match".
	// Any ambiguous eligible list errors so callers can route through
	// rotation rather than silently picking one.
	if _, err := resolveModel("claude", cfg, "", "", ""); err == nil {
		t.Fatal("expected error for ambiguous eligible list, got nil")
	}

	// With -M claude/opus → resolves correctly.
	m, err := resolveModel("claude", cfg, "", "", "claude/opus")
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
	if err := launchFiltered("shell", worktree, cfg, false, "", "", "", false, []string{recorder, out}); err != nil {
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
	if err := runAgentCmd(cmd, "claude", config.Model{ID: "claude/sonnet"}); err != nil {
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
	if err := runAgentCmd(cmd, "claude", config.Model{ID: "claude/sonnet"}); err != nil {
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

	// Two eligible models, no -M → resolveModel errors with "multiple models
	// match". The defaultModel fallback is gone, so any ambiguous eligible
	// list surfaces a clear error. The rotation advance in launchFiltered's
	// run path wraps resolveModel so callers see the rotated model instead.
	if _, err := resolveModel("claude", cfg, "", "", ""); err == nil {
		t.Fatal("expected error for ambiguous eligible list")
	}

	// Pinned → resolves to claude/opus.
	m, err := resolveModel("claude", cfg, "", "", "claude/opus")
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "claude/opus" {
		t.Errorf("got %q, want claude/opus", m.ID)
	}

	// Global rotation state lives at rotation.state under the config dir.
	expectedPath := filepath.Join(dir, "agent-wt", "rotation.state")
	gotPath := filepath.Join(rotation.New().StateDir(), "rotation.state")
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

	want := []string{"claude/a", "claude/b", "claude/c"}
	statePath := filepath.Join(dir, "agent-wt", "rotation.state")
	for i, id := range want {
		if err := launchFiltered("claude", worktree, cfg, false, "", "", "", false, nil); err != nil {
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

	if err := rotation.New().Record("claude/design-a"); err != nil {
		t.Fatalf("seed rotation state: %v", err)
	}

	if err := launchFiltered("claude", worktree, cfg, false, "design", "", "", false, nil); err != nil {
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
	_ = launchFiltered("shell", ".", cfg, false, "", "", "claude/opus", true, nil)

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
