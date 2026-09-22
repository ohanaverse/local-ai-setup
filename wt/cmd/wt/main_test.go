package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/spf13/cobra"
)

// TestConfig_NoSubcommand_LaunchesEditor verifies that `wt config` with no
// args invokes the config editor TUI. We stub configeditorRun so the test
// does not require a TTY.
func TestConfig_NoSubcommand_LaunchesEditor(t *testing.T) {
	called := false
	old := configeditorRun
	configeditorRun = func(theme themes.Theme, cfg *config.Config, cfgErr error) error {
		called = true
		return nil
	}
	defer func() { configeditorRun = old }()

	var buf bytes.Buffer
	root := rootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"config"})
	err := root.Execute()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("expected configeditorRun to be called")
	}
}

// TestLegacyShortFlagRejected verifies that the old `-w` arity is captured
// and rejected with the migration message. Users coming from the bash
// engine expect `-w name`, `-wname`, and `-w=name` to all parse; if pflag
// treats them as unknown flags instead, the migration message never fires.
func TestLegacyShortFlagRejected(t *testing.T) {
	for _, args := range []struct {
		name string
		args []string
	}{
		{"space separated", []string{"-w", "my-branch"}},
		{"no space", []string{"-wmy-branch"}},
		{"equals", []string{"-w=my-branch"}},
	} {
		t.Run(args.name, func(t *testing.T) {
			var buf bytes.Buffer
			root := rootCmd()
			root.SetOut(&buf)
			root.SetErr(&buf)
			root.SetArgs(args.args)
			err := root.Execute()
			if err == nil {
				t.Fatal("expected error for legacy -w flag, got nil")
			}
			if !strings.Contains(err.Error(), "-w is removed") {
				t.Errorf("error %q does not contain migration message", err.Error())
			}
		})
	}
}

// TestPickerSkippedOnWorktreeFlag verifies that `wt -W foo` doesn't
// open the TUI. We can't easily test the TUI itself from a unit test;
// instead we verify the short-circuit path runs by checking that
// `wt -W foo -A claude` errors with "not in a git repo" or similar
// before any TUI is launched.
func TestPickerSkippedOnWorktreeFlag(t *testing.T) {
	// Run from a non-git directory.
	oldWd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeEmptyRegistry(t, home)
	var buf bytes.Buffer
	root := rootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"-W", "my-branch"})
	err := root.Execute()
	// Should error with "not in a git repo", not launch a TUI.
	if err == nil {
		t.Fatal("expected error for -W outside git repo")
	}
	if !strings.Contains(err.Error(), "git") {
		t.Errorf("error %q doesn't mention git", err.Error())
	}
}

// TestRemovedSubcommand_Rejected verifies that `wt models` and `wt agents`
// exit non-zero with a migration message pointing at `wt config`. Without
// this guard, cobra's ArbitraryArgs (required for shell-wt passthrough)
// silently swallows the unknown first positional and the root RunE
// creates a worktree literally named "models" or "agents" — a silent
// footgun for users with muscle-memory invocations or stale doc snippets.
func TestRemovedSubcommand_Rejected(t *testing.T) {
	cases := []struct {
		name        string
		args        []string
		wantInError string
	}{
		{"wt models", []string{"models"}, "wt models is removed"},
		{"wt models with flags", []string{"models", "-A", "claude"}, "wt models is removed"},
		{"wt agents", []string{"agents"}, "wt agents is removed"},
		{"wt agents with flags", []string{"agents", "-A", "codex"}, "wt agents is removed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			root := rootCmd()
			root.SetOut(&buf)
			root.SetErr(&buf)
			root.SetArgs(tc.args)
			err := root.Execute()
			if err == nil {
				t.Fatalf("expected error for %v, got nil (output: %q)", tc.args, buf.String())
			}
			if !strings.Contains(err.Error(), tc.wantInError) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantInError)
			}
			// Must point users at the replacement.
			if !strings.Contains(err.Error(), "wt config") {
				t.Errorf("error %q does not mention `wt config` as the replacement", err.Error())
			}
		})
	}
}

// TestShellPassthrough_StillWorks guards against an over-tightened Args
// validator regressing shell-wt. `wt --agent shell ls -la` (the form the
// shell-wt shim produces) must NOT be rejected by the new guard.
func TestShellPassthrough_StillWorks(t *testing.T) {
	// We only need to assert the new guard does not fire for "ls" — the
	// rest of the launch path will fail in this unit test environment
	// (no TTY, no config) but that's after the guard.
	oldWd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("MODELMAN_REGISTRY", "")
	var buf bytes.Buffer
	root := rootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"--agent", "shell", "ls", "-la"})
	err := root.Execute()
	if err == nil {
		// Tolerated: would mean a full launch path ran (won't in this env).
		return
	}
	if strings.Contains(err.Error(), "is removed") {
		t.Fatalf("shell passthrough was incorrectly rejected as removed subcommand: %v", err)
	}
}

// initTestRepo creates a minimal git repo with one commit on the default
// branch so `git worktree add` (used by -W) succeeds. Returns the repo dir.
func initTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitInit(t, dir)
	if out, err := exec.Command("git", "-C", dir, "commit", "--allow-empty", "-m", "init").CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
	return dir
}

// TestWorktreeWithAgentWithoutModelShowsModelPicker verifies that
// `wt -W foo -A pi` (worktree and agent resolved, model NOT resolved) routes
// to the model picker instead of auto-launching. This is the fix for the bug
// where -W + -A (no -M) silently skipped the model selection screen. The test
// stubs tuiRun and stdinTTY so it runs in any environment — without the stub
// the real tuiRun would try to open /dev/tty and hang in an interactive shell.
func TestWorktreeWithAgentWithoutModelShowsModelPicker(t *testing.T) {
	dir := initTestRepo(t)
	oldWd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	home := t.TempDir()
	// pi must be CONFIGURED (a config.toml entry, zero eligible models) so
	// this test still exercises "configured agent, unresolved model → TUI",
	// not the new unconfigured-agent passthrough this task adds.
	writeConfiguredAgent(t, home, "pi", "ollama")

	var gotAgent, gotPinned string
	oldTuiRun := tuiRun
	tuiRun = func(yolo, allowReplace bool, agent, pinned, tags, family string, extraArgs []string, theme themes.Theme, prePath string, cfg *config.Config) error {
		gotAgent = agent
		gotPinned = pinned
		return nil
	}
	defer func() { tuiRun = oldTuiRun }()

	oldStdinTTY := stdinTTY
	stdinTTY = func() bool { return true }
	defer func() { stdinTTY = oldStdinTTY }()

	var buf bytes.Buffer
	root := rootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"-W", "my-feature", "-A", "pi"})
	if err := root.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotAgent != "pi" {
		t.Errorf("agent = %q, want pi (passed through to tuiRun)", gotAgent)
	}
	if gotPinned != "" {
		t.Errorf("pinned = %q, want empty (no -M supplied)", gotPinned)
	}
}

// TestReplaceFlagReachesTUIRun verifies `--replace` is forwarded from the CLI
// to tui.Run's allowReplace argument, and that omitting it forwards false.
// The parameter is compiler-checked but nothing asserted its value, so a
// dropped or hard-coded flag would silently make --replace a no-op for the
// TUI's -M start path.
func TestReplaceFlagReachesTUIRun(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
		want bool
	}{
		{"with --replace", []string{"-W", "my-feature", "-A", "pi", "--replace"}, true},
		{"without --replace", []string{"-W", "my-feature", "-A", "pi"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := initTestRepo(t)
			oldWd, _ := os.Getwd()
			t.Cleanup(func() { _ = os.Chdir(oldWd) })
			if err := os.Chdir(dir); err != nil {
				t.Fatalf("chdir: %v", err)
			}
			home := t.TempDir()
			// pi must be CONFIGURED (a config.toml entry, zero eligible
			// models) so this test still exercises "configured agent,
			// unresolved model → TUI", not the new unconfigured-agent
			// passthrough — an unconfigured pi would otherwise now launch
			// directly instead of reaching tuiRun.
			writeConfiguredAgent(t, home, "pi", "ollama")

			// allowReplace is process-wide; restore it so this test cannot leak
			// --replace into another test.
			t.Cleanup(func(prev bool) func() { return func() { allowReplace = prev } }(allowReplace))

			var got, called bool
			oldTuiRun := tuiRun
			tuiRun = func(yolo, allowReplace bool, agent, pinned, tags, family string, extraArgs []string, theme themes.Theme, prePath string, cfg *config.Config) error {
				got, called = allowReplace, true
				return nil
			}
			t.Cleanup(func() { tuiRun = oldTuiRun })
			oldStdinTTY := stdinTTY
			stdinTTY = func() bool { return true }
			t.Cleanup(func() { stdinTTY = oldStdinTTY })

			var buf bytes.Buffer
			root := rootCmd()
			root.SetOut(&buf)
			root.SetErr(&buf)
			root.SetArgs(c.args)
			if err := root.Execute(); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !called {
				t.Fatal("tuiRun was not called")
			}
			if got != c.want {
				t.Errorf("tuiRun allowReplace = %v, want %v", got, c.want)
			}
		})
	}
}

// TestWorktreeWithAgentAndModelLaunches verifies that `wt -W foo -A pi -M
// claude/opus` (all three selections resolved) still auto-launches instead of
// showing the model picker. With an empty config the launch fails on model
// resolution, but it must NOT surface a picker TTY error.
func TestWorktreeWithAgentAndModelLaunches(t *testing.T) {
	dir := initTestRepo(t)
	oldWd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("MODELMAN_REGISTRY", "")

	var buf bytes.Buffer
	root := rootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"-W", "my-feature", "-A", "pi", "-M", "claude/opus"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected a launch error (empty config has no models), got nil")
	}
	if errors.Is(err, errModelPickerNeedsTTY) || errors.Is(err, errPickerNeedsTTY) {
		t.Fatalf("err = %v, want a launch error, not a picker TTY error", err)
	}
}

// TestWorktreeWithModelWithoutAgentPassesPinnedToTUI verifies that
// `wt -W foo -M claude/opus` (model pinned, agent NOT pinned) routes to the
// TUI with the pinned model threaded through, so the TUI can prompt for the
// agent and then validate the pin. Without this, the -M value was silently
// dropped on the unpinned-agent path.
func TestWorktreeWithModelWithoutAgentPassesPinnedToTUI(t *testing.T) {
	dir := initTestRepo(t)
	oldWd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("MODELMAN_REGISTRY", "")
	writeEmptyRegistry(t, home)

	var gotAgent, gotPinned string
	oldTuiRun := tuiRun
	tuiRun = func(yolo, allowReplace bool, agent, pinned, tags, family string, extraArgs []string, theme themes.Theme, prePath string, cfg *config.Config) error {
		gotAgent = agent
		gotPinned = pinned
		return nil
	}
	defer func() { tuiRun = oldTuiRun }()

	oldStdinTTY := stdinTTY
	stdinTTY = func() bool { return true }
	defer func() { stdinTTY = oldStdinTTY }()

	var buf bytes.Buffer
	root := rootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"-W", "my-feature", "-M", "claude/opus"})
	if err := root.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotAgent != "" {
		t.Errorf("agent = %q, want empty (agent picker should be shown)", gotAgent)
	}
	if gotPinned != "claude/opus" {
		t.Errorf("pinned = %q, want claude/opus", gotPinned)
	}
}

// TestCommandAgentWithoutModelLaunchesDirectly verifies that a command agent
// (shell) with no -M still launches directly rather than routing through the
// TUI model picker. Commands have no model layer, so the "model unresolved"
// condition must not apply to them; otherwise `shell-wt -W foo` would fail
// with a model-picker TTY error instead of running the command.
func TestCommandAgentWithoutModelLaunchesDirectly(t *testing.T) {
	oldWd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("MODELMAN_REGISTRY", "")
	writeEmptyRegistry(t, home)

	var gotAgent string
	var gotArgs []string
	oldLaunchFiltered := launchFiltered
	launchFiltered = func(agent, worktreePath string, cfg *config.Config, yolo bool, tags, family, pinned string, pinnedSupplied bool, extraArgs []string, eligible []config.Model) error {
		gotAgent = agent
		gotArgs = extraArgs
		return nil
	}
	defer func() { launchFiltered = oldLaunchFiltered }()

	var buf bytes.Buffer
	root := rootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"-A", "shell", "true"})
	if err := root.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotAgent != "shell" {
		t.Errorf("agent = %q, want shell", gotAgent)
	}
	if len(gotArgs) != 1 || gotArgs[0] != "true" {
		t.Errorf("extraArgs = %v, want [true]", gotArgs)
	}
}

// TestNeedsModelPicker is a table test for the predicate that decides whether
// the CLI routes to the model picker or to launchFiltered. The predicate is
// the single source of truth for picker routing across --cwd, -W, and the
// outside-repo path — getting it wrong causes either silent auto-launches
// the user didn't ask for, or TTY errors when they did.
//
// The predicate captures the CURRENT picker-routing rule. A subsequent fix
// layer (resolveModel short-circuit, added in a separate task) restores the
// pre-PR auto-launch behavior for the "pinned agent, no pin" case when the
// eligible model list has exactly one entry.
func TestNeedsModelPicker(t *testing.T) {
	tests := []struct {
		name          string
		agent, pinned string
		want          bool
	}{
		{"unpinned agent, no pin", "", "", true},
		{"pinned agent, no pin (routes to TUI; resolveModel may short-circuit)", "claude", "", true},
		{"pinned command, no pin", "shell", "", false},
		{"pinned agent, valid pin", "claude", "claude/opus", false},
		{"unpinned agent, valid pin", "", "claude/opus", true},
		{"pinned command, valid pin (no model layer)", "shell", "claude/opus", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := needsModelPicker(tt.agent, tt.pinned); got != tt.want {
				t.Errorf("needsModelPicker(%q, %q) = %v, want %v", tt.agent, tt.pinned, got, tt.want)
			}
		})
	}
}

// TestAgentWithOneEligibleModelAutoLaunches verifies that `wt --cwd -A claude`
// with exactly one eligible model in config auto-launches via launchFiltered
// instead of routing to the model picker. This restores the pre-PR-2 UX
// contract: `-A` alone is sufficient when EligibleModels resolves to a
// single entry. Without this fix, scripts and CI invocations of
// `wt --cwd -A <agent>` force an interactive picker or fail with a TTY error.
func TestAgentWithOneEligibleModelAutoLaunches(t *testing.T) {
	dir := initTestRepo(t)
	oldWd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("MODELMAN_REGISTRY", "")

	// Running state is live truth now: the localgate flag-and-probe dance
	// this test used to perform (modelman running flag + a fake `ollama`
	// binary on PATH) is gone. resolveModel consults the local-model
	// inventory directly, so the stub probe must report the model serving
	// or the row is a start row, not a launch row, and the auto-launch
	// short-circuit never fires.
	stubProbeInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "ollama", ModelID: "ollama/gemma4:9b", Artifact: "gemma4:9b", ModelName: "gemma4:9b", Registered: true, Running: true},
		},
	})

	// Write a minimal config with one ollama model so resolveModel returns
	// a single eligible entry, triggering the auto-launch short-circuit.
	cfgDir := filepath.Join(home, ".config", "agent-wt")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"),
		[]byte("default_tag = \"code\"\n[[agents]]\nname = \"claude\"\nsupported_providers = [\"ollama\"]\ndefault_provider = \"ollama\"\n"),
		0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	// Providers/models now live in modelman-owned registry.toml.
	regDir := filepath.Join(home, ".config", "local-ai")
	if err := os.MkdirAll(regDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(regDir, "registry.toml"),
		[]byte("[[providers]]\nid = \"ollama\"\nname = \"Ollama\"\nlocation = \"local\"\nauth = { type = \"none\", base_url = \"http://localhost:11434\" }\n[[providers]]\nid = \"agy\"\nname = \"Antigravity\"\nlocation = \"cloud\"\nauth = { type = \"native\" }\n[[models]]\nid = \"ollama/gemma4:9b\"\nprovider_id = \"ollama\"\nmodel_name = \"gemma4:9b\"\nlocation = \"local\"\ntags = [\"code\"]\n"),
		0o644); err != nil {
		t.Fatalf("write registry: %v", err)
	}
	// Expose the ollama model through the LiteLLM gateway so the
	// auto-launch short-circuit sees it as eligible. The modelman per-model
	// `running` flag is deliberately left false: wt decides running state
	// from the live inventory probe (stubbed above), never from
	// modelman-owned state.
	if err := os.WriteFile(filepath.Join(regDir, "modelman.toml"),
		[]byte("[model_state.\"ollama/gemma4:9b\"]\nlitellm_exposed = true\nready = true\n"),
		0o644); err != nil {
		t.Fatalf("write modelman state: %v", err)
	}

	called := false
	oldLaunchFiltered := launchFiltered
	launchFiltered = func(agent, worktreePath string, cfg *config.Config, yolo bool, tags, family, pinned string, pinnedSupplied bool, extraArgs []string, eligible []config.Model) error {
		called = true
		return nil
	}
	defer func() { launchFiltered = oldLaunchFiltered }()

	var buf bytes.Buffer
	root := rootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"--cwd", "-A", "claude"})
	if err := root.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("launchFiltered not called; -A claude should auto-launch when resolveModel returns a single eligible model")
	}
}

// TestUnknownAgentFailsFast verifies that `wt -A <typo>` errors immediately
// with "unknown agent" rather than returning a misleading model-picker TTY
// error. Without this, a user who typos `-A claud` is told to add `-M`,
// adds it, and gets the same error again — the real problem is the agent
// name, and the CLI must surface that.
func TestUnknownAgentFailsFast(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeEmptyRegistry(t, home)

	var buf bytes.Buffer
	root := rootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"-A", "claud"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected unknown-agent error, got nil")
	}
	if !strings.Contains(err.Error(), "unknown agent") {
		t.Errorf("err = %v, want an error containing \"unknown agent\"", err)
	}
	if errors.Is(err, errModelPickerNeedsTTY) {
		t.Errorf("err = %v, want a fast-fail error, not the model-picker TTY error", err)
	}
}

// A command agent (shell) must launch even when registry.toml is missing:
// commands have no model layer, so the missing-registry config error must
// not block the worktree picker. Regression guard for the a.cfgErr gate.
func TestCommandAgentSkipsMissingRegistryGate(t *testing.T) {
	home := t.TempDir()
	withCleanConfigEnv(t, home)
	dir := initTestRepo(t)
	oldWd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	oldInstallGuard := maybeInstallGuard
	maybeInstallGuard = func() {}
	defer func() { maybeInstallGuard = oldInstallGuard }()

	var called bool
	oldTuiRun := tuiRun
	tuiRun = func(yolo, allowReplace bool, agent, pinned, tags, family string, extraArgs []string, theme themes.Theme, prePath string, cfg *config.Config) error {
		called = true
		return nil
	}
	defer func() { tuiRun = oldTuiRun }()
	oldStdinTTY := stdinTTY
	stdinTTY = func() bool { return true }
	defer func() { stdinTTY = oldStdinTTY }()

	var buf bytes.Buffer
	root := rootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"--agent", "shell"})
	if err := root.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("expected tuiRun to be called (gate should be skipped for command agent)")
	}
}

// A malformed (not merely missing) registry.toml must still fail closed with
// the repair hint, for every agent — only config.ErrRegistryMissing is
// tolerated by the launch-path gate, never a real parse error. Regression
// guard for decision #7 of the passthrough design (fail closed on malformed
// config).
func TestMalformedRegistryStillFailsClosed(t *testing.T) {
	home := t.TempDir()
	withCleanConfigEnv(t, home)
	dir := initTestRepo(t)
	oldWd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	regDir := filepath.Join(home, ".config", "local-ai")
	if err := os.MkdirAll(regDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(regDir, "registry.toml"), []byte("not valid toml [[["), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	root := rootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"-A", "claude", "-W", "my-feature"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected a config error for a malformed registry.toml")
	}
	if !strings.Contains(err.Error(), "wt config") {
		t.Errorf("err = %v, want it to mention `wt config` as the repair path", err)
	}
}

// A pinned agent whose binary is missing must fail before any worktree work
// — creating a worktree for an agent that can never launch is wasted work
// (and, for -W, a wasted `git worktree add`). Decision #3 of the passthrough
// design: the installed check is early and pinned-agent-only.
func TestPinnedAgentNotInstalledErrorsBeforeWorktreeWork(t *testing.T) {
	dir := initTestRepo(t)
	oldWd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	home := t.TempDir()
	writeEmptyRegistry(t, home)

	oldInstalled := installed
	installed = func(name string) bool { return false }
	defer func() { installed = oldInstalled }()

	var buf bytes.Buffer
	root := rootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"-A", "claude", "-W", "my-feature"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected a not-installed error")
	}
	if !strings.Contains(err.Error(), "not installed") {
		t.Errorf("err = %v, want it to mention \"not installed\"", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".worktrees", "my-feature")); !os.IsNotExist(statErr) {
		t.Errorf("worktree should not have been created before the installed check (stat err = %v)", statErr)
	}
}

// A pinned command agent (shell) has no binary requirement of its own — the
// new pinned-installed fast-fail must not block it even when the installed
// seam reports false for every name.
func TestPinnedCommandAgentSkipsInstalledCheck(t *testing.T) {
	oldWd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	home := t.TempDir()
	writeEmptyRegistry(t, home)

	oldInstalled := installed
	installed = func(name string) bool { return false }
	defer func() { installed = oldInstalled }()

	var called bool
	oldLaunchFiltered := launchFiltered
	launchFiltered = func(agent, worktreePath string, cfg *config.Config, yolo bool, tags, family, pinned string, pinnedSupplied bool, extraArgs []string, eligible []config.Model) error {
		called = true
		return nil
	}
	defer func() { launchFiltered = oldLaunchFiltered }()

	var buf bytes.Buffer
	root := rootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"-A", "shell", "true"})
	if err := root.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("expected launchFiltered to be called for a command agent")
	}
}

// TestCommandAgentDirectLaunchSkipsMissingRegistry guards the non-TUI
// launch path: wt --cwd --agent shell on a fresh machine must skip the
// missing-registry gate and proceed to launchFiltered.
func TestCommandAgentDirectLaunchSkipsMissingRegistry(t *testing.T) {
	dir := initTestRepo(t)
	oldWd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	withCleanConfigEnv(t, t.TempDir())

	var called bool
	var gotAgent string
	var gotPath string
	oldLaunchFiltered := launchFiltered
	launchFiltered = func(agent, worktreePath string, cfg *config.Config, yolo bool, tags, family, pinned string, pinnedSupplied bool, extraArgs []string, eligible []config.Model) error {
		called = true
		gotAgent = agent
		gotPath = worktreePath
		return nil
	}
	defer func() { launchFiltered = oldLaunchFiltered }()
	oldMaybeInstallGuard := maybeInstallGuard
	maybeInstallGuard = func() {}
	defer func() { maybeInstallGuard = oldMaybeInstallGuard }()

	var buf bytes.Buffer
	root := rootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"--cwd", "--agent", "shell"})
	if err := root.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("expected launchFiltered to be called (gate should be skipped for command agent)")
	}
	if gotAgent != "shell" {
		t.Errorf("agent = %q, want shell", gotAgent)
	}
	// macOS /var is a symlink to /private/var; normalize before comparing.
	realGot, errGot := filepath.EvalSymlinks(gotPath)
	realWant, errWant := filepath.EvalSymlinks(dir)
	if errGot != nil || errWant != nil {
		if gotPath != dir {
			t.Errorf("worktreePath = %q, want %q", gotPath, dir)
		}
	} else if realGot != realWant {
		t.Errorf("worktreePath = %q, want %q", gotPath, dir)
	}
}

// TestRunLaunchPath verifies the shared dispatcher routes each entry point
// to the right outcome: inside-repo branches install the guard and launch;
// the outside-repo branch skips the guard; and the TUI branch
// (launchPath == "") always shows the worktree picker — even when the agent
// and model are already pinned, a pinned agent or command must still pick a
// worktree. The guard is stubbed because the real guard.Install operates on
// the test process's cwd and would install the hook into the repo under test.
func TestRunLaunchPath(t *testing.T) {
	repo := t.TempDir()
	if err := exec.Command("git", "-C", repo, "init", "-q").Run(); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Agents: []config.Agent{{Name: "shell", SupportedProviders: nil}},
	}
	a := &app{cfg: cfg}

	cases := []struct {
		name       string
		agent      string
		pinned     string
		launchPath string
		root       string
		wantPath   string
		wantTUI    bool // true: expect tuiRun; false: expect launchFiltered
		wantGuard  bool // true: expect maybeInstallGuard called
	}{
		{"cwd", "shell", "", repo, repo, repo, false, true},
		{"outside", "shell", "", ".", "", ".", false, false},
		{"tui", "shell", "", "", "", "", true, false},
		{"tui-pinned-model", "claude", "ollama/x", "", "", "", true, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Stub tuiRun, launchFiltered, maybeInstallGuard, and
			// sweepRefcounts to capture the dispatched path, guard
			// behavior, and refcount sweep — the sweep must fire on every
			// branch (including "shell", which never builds a picker),
			// since it is the single funnel every launch goes through.
			oldTUI := tuiRun
			oldLaunch := launchFiltered
			oldGuard := maybeInstallGuard
			oldSweep := sweepRefcounts
			var gotPath string
			var gotTUI, gotLaunch, gotGuard, gotSweep bool
			tuiRun = func(bool, bool, string, string, string, string, []string, themes.Theme, string, *config.Config) error {
				gotTUI = true
				gotPath = c.launchPath
				return nil
			}
			launchFiltered = func(agent, worktreePath string, cfg *config.Config, yolo bool, tags, family, pinned string, pinnedSupplied bool, extraArgs []string, eligible []config.Model) error {
				gotLaunch = true
				gotPath = worktreePath
				return nil
			}
			maybeInstallGuard = func() { gotGuard = true }
			sweepRefcounts = func() { gotSweep = true }
			defer func() {
				tuiRun = oldTUI
				launchFiltered = oldLaunch
				maybeInstallGuard = oldGuard
				sweepRefcounts = oldSweep
			}()

			cmd := &cobra.Command{}
			cmd.Flags().StringP("model", "M", "", "")
			cmd.Flags().Bool("yolo", false, "")

			err := runLaunchPath(cmd, a, c.agent, c.pinned, "", "", nil, c.launchPath, c.root)
			if err != nil {
				t.Fatalf("runLaunchPath: %v", err)
			}
			if gotPath != c.wantPath {
				t.Errorf("dispatched path = %q, want %q", gotPath, c.wantPath)
			}
			if gotTUI != c.wantTUI {
				t.Errorf("tuiRun called = %v, want %v", gotTUI, c.wantTUI)
			}
			if gotLaunch != !c.wantTUI {
				t.Errorf("launchFiltered called = %v, want %v", gotLaunch, !c.wantTUI)
			}
			if gotGuard != c.wantGuard {
				t.Errorf("maybeInstallGuard called = %v, want %v", gotGuard, c.wantGuard)
			}
			if !gotSweep {
				t.Error("sweepRefcounts not called; the sweep must run on every runLaunchPath branch, including shell-wt")
			}
		})
	}
}

// TestResolveModelForLaunchCloudOnlyResolves verifies the auto-launch
// short-circuit still fires for a cloud-only config, so `wt -A claude` with
// one eligible model launches without a picker. The old drifted-marker test
// this replaces asserted gate semantics that no longer exist.
func TestResolveModelForLaunchCloudOnlyResolves(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "claude", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "native"}}},
		Models:    []config.Model{{ID: "claude/opus", ProviderID: "claude", ModelName: "opus", Family: "opus", Tags: []string{"code"}}},
		Agents:    []config.Agent{{Name: "claude", SupportedProviders: []string{"claude"}}},
	}
	cfg.ExposeAllForTest()

	resolved, m, _, err := resolveModelForLaunch("claude", cfg, "", "", "")
	if err != nil {
		t.Fatalf("resolveModelForLaunch error = %v, want nil", err)
	}
	if !resolved || m.ID != "claude/opus" {
		t.Errorf("resolveModelForLaunch() = (resolved=%v, m=%v), want (true, claude/opus)", resolved, m)
	}
}

// TestRootHelpListsModelSubcommands verifies `wt --help` documents start, stop
// and smoke with usage examples, so the model subcommands are discoverable
// without reading the docs.
func TestRootHelpListsModelSubcommands(t *testing.T) {
	var out bytes.Buffer
	root := rootCmd()
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"start", "stop", "smoke", "wt start", "wt stop", "wt smoke"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("help output missing %q:\n%s", want, out.String())
		}
	}
}

// A model-driven agent with no config.toml entry launches via the bare
// passthrough path when registry.toml is missing entirely — the extreme
// case of "no config.toml entry" (issue #147). Before this feature a
// missing registry failed closed for every non-command agent (see the now-
// deleted TestNonCommandAgentStillFailsWithoutRegistry); a genuinely
// malformed config.toml/registry.toml still fails
// (TestMalformedRegistryStillFailsClosed).
func TestModelDrivenAgentPassesThroughWithoutRegistry(t *testing.T) {
	dir := initTestRepo(t)
	oldWd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	home := t.TempDir()
	withCleanConfigEnv(t, home)
	// No registry.toml written: config.Load fails closed with
	// ErrRegistryMissing, a.cfg falls back to an empty Config with no
	// agents, and claude reads as unconfigured.

	var called bool
	var gotAgent, gotPath string
	oldLaunchPassthrough := launchPassthrough
	launchPassthrough = func(agent, worktreePath string, yolo bool, extraArgs []string, cfg *config.Config) error {
		called, gotAgent, gotPath = true, agent, worktreePath
		return nil
	}
	defer func() { launchPassthrough = oldLaunchPassthrough }()

	var buf bytes.Buffer
	root := rootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"-A", "claude", "-W", "my-feature"})
	if err := root.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("expected launchPassthrough to be called")
	}
	if gotAgent != "claude" {
		t.Errorf("agent = %q, want claude", gotAgent)
	}
	if !strings.Contains(gotPath, filepath.Join(".worktrees", "my-feature")) {
		t.Errorf("worktreePath = %q, want it to contain .worktrees/my-feature", gotPath)
	}
}

// `-M` on an unconfigured agent must error, never silently pass through or
// drop the pin — decision #5 of the passthrough design.
func TestPinnedModelWithUnconfiguredAgentErrors(t *testing.T) {
	dir := initTestRepo(t)
	oldWd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	home := t.TempDir()
	writeEmptyRegistry(t, home) // registry exists but claude has no config.toml entry

	var buf bytes.Buffer
	root := rootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"-A", "claude", "-W", "my-feature", "-M", "ollama/foo"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected an error for -M against an unconfigured agent")
	}
	if !strings.Contains(err.Error(), "not configured") || !strings.Contains(err.Error(), "ollama/foo") {
		t.Errorf("err = %v, want it to mention \"not configured\" and the pinned model", err)
	}
}

// An unpinned launch (`-W` with no `-A`) must still route to the TUI's
// agent/command picker, not be misread as an unconfigured-agent passthrough
// for an empty agent name. Without the `agent != ""` guard on the new
// branch, cfg.AgentByName("") always fails too, which would otherwise
// short-circuit straight into agents.BuildPassthroughCmd("", ...).
func TestUnpinnedWorktreeSkipsPassthroughGuard(t *testing.T) {
	dir := initTestRepo(t)
	oldWd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	home := t.TempDir()
	writeEmptyRegistry(t, home)

	var tuiCalled bool
	oldTuiRun := tuiRun
	tuiRun = func(yolo, allowReplace bool, agent, pinned, tags, family string, extraArgs []string, theme themes.Theme, prePath string, cfg *config.Config) error {
		tuiCalled = true
		return nil
	}
	defer func() { tuiRun = oldTuiRun }()
	oldStdinTTY := stdinTTY
	stdinTTY = func() bool { return true }
	defer func() { stdinTTY = oldStdinTTY }()

	var passthroughCalled bool
	oldLaunchPassthrough := launchPassthrough
	launchPassthrough = func(agent, worktreePath string, yolo bool, extraArgs []string, cfg *config.Config) error {
		passthroughCalled = true
		return nil
	}
	defer func() { launchPassthrough = oldLaunchPassthrough }()

	var buf bytes.Buffer
	root := rootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"-W", "my-feature"})
	if err := root.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !tuiCalled {
		t.Error("expected tuiRun to be called for an unpinned -W launch")
	}
	if passthroughCalled {
		t.Error("launchPassthrough must not be called when no agent is pinned")
	}
}
