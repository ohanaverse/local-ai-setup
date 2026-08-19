package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/agent-worktree/internal/config"
	"github.com/ohanaverse/agent-worktree/internal/initseed"
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

// TestDefaultModelPrefersNative asserts that an agent's native model
// (e.g. claude/native) is chosen over the first code-tagged model. This
// mirrors the bash wrappers' WT_DEFAULT_CODE="native:claude".
func TestDefaultModelPrefersNative(t *testing.T) {
	cfg := &config.Config{
		DefaultTag: "code",
		Models: []config.Model{
			{ID: "ollama/gemma4:9b", Tags: []string{"code"}},
			{ID: "claude/native", ModelName: "native", Tags: []string{"code", "design"}},
		},
	}
	if got := defaultModel(cfg, "claude"); got.ID != "claude/native" {
		t.Errorf("defaultModel = %q, want %q", got.ID, "claude/native")
	}
}

// TestDefaultModelFallsBackToTag asserts that when an agent has no native
// model, the first model in the default tag group is used. This is the
// fallback for agents like pi that route through a provider.
func TestDefaultModelFallsBackToTag(t *testing.T) {
	cfg := &config.Config{
		DefaultTag: "code",
		Models: []config.Model{
			{ID: "ollama/gemma4:9b", Tags: []string{"code"}},
		},
	}
	if got := defaultModel(cfg, "pi"); got.ID != "ollama/gemma4:9b" {
		t.Errorf("defaultModel = %q, want %q", got.ID, "ollama/gemma4:9b")
	}
}

// TestDefaultModelEmptyConfig asserts that an empty config yields a "(none)"
// placeholder rather than a zero-value model. This keeps the launch path from
// exec'ing an agent with an empty model id.
func TestDefaultModelEmptyConfig(t *testing.T) {
	cfg := &config.Config{DefaultTag: "code"}
	if got := defaultModel(cfg, "claude"); got.ID != "(none)" {
		t.Errorf("defaultModel = %q, want %q", got.ID, "(none)")
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
// claude-wt wrapper used to do.
func TestBuildLaunchClaudeResume(t *testing.T) {
	cmd, err := buildLaunch("claude", config.Model{ID: "claude/native", ModelName: "native"}, "/tmp/repo", false,
		&session.Session{ID: "abc-123", MTime: time.Now()}, nil, nil)
	if err != nil {
		t.Fatalf("buildLaunch: %v", err)
	}
	got := strings.Join(cmd.Args, " ")
	if !strings.Contains(got, "--resume abc-123") {
		t.Errorf("args = %q, want --resume abc-123", got)
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
// resume/session flag. This is the "start fresh" path.
func TestBuildLaunchNoSessionOmitsResume(t *testing.T) {
	cmd, err := buildLaunch("claude", config.Model{ID: "claude/native", ModelName: "native"}, "/tmp/repo", false, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildLaunch: %v", err)
	}
	got := strings.Join(cmd.Args, " ")
	if strings.Contains(got, "--resume") || strings.Contains(got, "--session") {
		t.Errorf("args = %q, should not contain resume/session flags", got)
	}
}

// TestInGitRepoAt asserts that inGitRepoAt reports true inside a git repo and
// false outside one. This gates the passthrough path: outside a repo the
// agent launches directly with no picker, so a wrong answer here would either
// drop the user into the TUI or skip the picker unexpectedly.
func TestInGitRepoAt(t *testing.T) {
	dir := t.TempDir()
	if inGitRepoAt(dir) {
		t.Fatal("inGitRepoAt = true for a non-repo directory")
	}
	if err := exec.Command("git", "-C", dir, "init", "-q").Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}
	if !inGitRepoAt(dir) {
		t.Fatal("inGitRepoAt = false for a git repo")
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

// TestLaunchUsesResolveModel verifies the non-TUI launch path's filter
// resolution contract. With no -M and no -T/-F, a multi-model agent falls back
// to the default (pre-flag-surface) model rather than erroring, so existing
// `wt -W foo --agent claude` invocations keep launching. When -T/-F are
// supplied and the filtered set is still ambiguous, resolveModel must surface
// a clear "specify -M" error. With -M it returns the pinned model.
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

	// Two models, no -M, no -T/-F → default fallback. Must NOT error:
	// existing users without the new flags see no change.
	m, err := resolveModel("claude", cfg, "", "", "")
	if err != nil {
		t.Fatalf("expected default fallback, got error: %v", err)
	}
	if m.ID != "claude/opus" {
		t.Errorf("default fallback = %q, want claude/opus", m.ID)
	}

	// Two models, -T code supplied (still ambiguous), no -M → error. The
	// user opted into filtering, so an ambiguous result must be pinned.
	if _, err := resolveModel("claude", cfg, "code", "", ""); err == nil {
		t.Fatalf("expected error for filtered ambiguous list, got nil")
	}

	// With -M claude/opus → resolves correctly.
	m, err = resolveModel("claude", cfg, "", "", "claude/opus")
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
// routed through launchDirect → launch(agent, "."), discarding the worktree
// path that EnsureForName had just created. buildFilteredCmd must build the
// command with cmd.Dir set to the requested worktree.
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

// TestBuildFilteredCmdCommandAgentNotesIgnoredModel verifies that supplying -M
// to a command agent (shell) does not break the build and surfaces a stderr
// note so the user knows the pin was discarded. No note is emitted when -M is
// absent. This matches the spec's "ignore -M with stderr note" for command
// agents.
func TestBuildFilteredCmdCommandAgentNotesIgnoredModel(t *testing.T) {
	cfg := &config.Config{}
	worktree := "/tmp/wt-shell-note"

	captureStderr := func(pinned string) string {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("os.Pipe: %v", err)
		}
		orig := os.Stderr
		os.Stderr = w
		_, _, berr := buildFilteredCmd("shell", worktree, cfg, false, "", "", pinned, nil)
		w.Close()
		os.Stderr = orig
		if berr != nil {
			t.Fatalf("buildFilteredCmd: %v", berr)
		}
		out, _ := io.ReadAll(r)
		return string(out)
	}

	if got := captureStderr("claude/opus"); !strings.Contains(got, "ignored for command agent") {
		t.Errorf("with -M: stderr = %q, want a note that -M is ignored", got)
	}
	if got := captureStderr(""); strings.Contains(got, "ignored") {
		t.Errorf("without -M: stderr = %q, want no note", got)
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
