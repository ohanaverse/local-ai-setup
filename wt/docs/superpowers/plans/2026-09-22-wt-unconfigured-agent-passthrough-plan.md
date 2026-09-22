# wt: pass through to an unconfigured agent Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A model-driven agent (claude, codex, copilot, opencode, pi, agy) with no `config.toml` entry — including the extreme case of a wholly missing `registry.toml` — launches its installed binary directly, with no model routing, instead of failing closed or requiring `-M`.

**Architecture:** A single predicate, `agents.IsConfigured`, decides "does this agent have a model catalog to resolve against". When false, both launch paths (`cmd/wt`'s non-TUI path and `internal/tui`) skip the model layer entirely and build a bare launch command through `agents.BuildPassthroughCmd`, which reuses every driver's existing "native model" branch via a sentinel `config.Model{Native: true, ModelName: "native"}`. The only driver gap is `opencode`, whose `Build` never checks `m.Native`; it gets the one-line guard every other driver already has.

**Tech Stack:** Go 1.26 (module root `wt/`), cobra, Bubble Tea. Run every command from `wt/` unless stated.

**Spec:** `wt/docs/superpowers/specs/2026-09-22-wt-unconfigured-agent-passthrough-design.md`

## Global Constraints

- Every `Test*` gets a top-level `//` comment stating what it tests and why it matters (wt/CLAUDE.md, user rule).
- Never probe live servers, start real processes, or depend on the host's installed binaries in tests: use the existing seams (`tuiRun`, `stdinTTY`, `launchFiltered`, `installed`) and the new ones this plan adds (`installed` in `cmd/wt`, `buildPassthrough` in `internal/tui`, `launchPassthrough` in `cmd/wt`).
- `cmd/wt`'s `TestMain` (`cmd/wt/testmain_test.go`) stubs every seam to a safe default so unmodified tests are unaffected; a test that needs different behavior stubs it explicitly and restores it via `t.Cleanup`.
- `-M` on an unconfigured agent is always an error (never silently dropped), on both the non-TUI and TUI paths.
- The `agent != ""` guard matters: an unpinned launch (`-W`/`--cwd` with no `-A`) must keep routing to the agent/command picker, not be misread as "the empty agent is unconfigured".
- Commits during execution end with `Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>` and reference the plan item (`- completes plan item #N`). Never push or open a PR without asking.
- Mark each task in TaskCreate/TaskUpdate as you go (in_progress → completed).
- Run `go test ./...` and `go vet ./...` from `wt/` after every task; a task is not done until both are clean.

---

### Task 1: `internal/agents` — `IsConfigured`, the passthrough builder, and the opencode native gap

**Files:**
- Create: `internal/agents/passthrough.go`
- Create: `internal/agents/passthrough_test.go`
- Modify: `internal/agents/opencode.go`
- Modify: `internal/agents/opencode_test.go`

**Interfaces:**
- Produces:
  - `func IsConfigured(cfg *config.Config, name string) bool` — true for commands (`IsCommand`) always; for everything else, true iff `cfg != nil && cfg.AgentByName(name)` succeeds.
  - `func BuildPassthroughCmd(agent, worktreePath string, yolo bool, extraArgs []string) (*exec.Cmd, error)` — builds a bare launch command via `BuildLaunchCmd` with a nil `cfg`, nil `*session.Session`, and the unexported `passthroughModel()` sentinel.
- Consumes: `BuildLaunchCmd`, `IsCommand`, `config.Model`, `config.Config` (all already in package `agents`/`config`).

- [ ] **Step 1: Write the failing tests**

Append to `internal/agents/opencode_test.go` (uses the existing `nativeModel` helper and `directRoute` helper from `agents_test.go`, same package):

```go
// OpenCode was the one driver whose Build ignored the Native model bit —
// opencode is ollama-only in normal use, so it never received a Native
// model before the unconfigured-agent passthrough sentinel (issue #147).
// Without this guard, a native launch would still emit
// OPENCODE_CONFIG_CONTENT pointing at an empty gateway base URL instead of a
// bare `opencode` command.
func TestOpenCodeNativeBypass(t *testing.T) {
	d := ByName("opencode")
	if d == nil {
		t.Fatal("opencode driver not registered")
	}
	m := nativeModel("opencode")
	lc := d.Build(m, false, directRoute(m))
	if lc.Bin != "opencode" || len(lc.Args) != 0 || len(lc.Env) != 0 {
		t.Errorf("native build = %+v, want bare opencode (no args, no env)", lc)
	}
}
```

Create `internal/agents/passthrough_test.go`:

```go
package agents

import (
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// IsConfigured is the single source of truth the launch paths use to decide
// whether an agent has a model catalog to resolve against. Commands are
// always configured (no model layer to catalog); a model-driven agent needs
// a config.toml entry, and a nil cfg (a caller working around a config load
// failure) is never configured.
func TestIsConfigured(t *testing.T) {
	cfg := &config.Config{
		Agents: []config.Agent{
			{Name: "claude", SupportedProviders: []string{"ollama"}},
		},
	}
	cases := []struct {
		name  string
		cfg   *config.Config
		agent string
		want  bool
	}{
		{"command is always configured", cfg, "shell", true},
		{"command is configured even with nil cfg", nil, "shell", true},
		{"configured model-driven agent", cfg, "claude", true},
		{"unconfigured model-driven agent", cfg, "opencode", false},
		{"nil cfg is never configured for a real agent", nil, "opencode", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsConfigured(c.cfg, c.agent); got != c.want {
				t.Errorf("IsConfigured(cfg, %q) = %v, want %v", c.agent, got, c.want)
			}
		})
	}
}

// Every model-driven driver, given the passthrough sentinel (Native: true,
// ModelName: "native"), must add no --model flag and no model-routing env
// var. This is what makes the unconfigured-agent passthrough (issue #147)
// launch every agent bare; a new driver that skips this convention would
// silently route through a stale or empty gateway URL instead of the
// agent's own default config or subscription.
func TestPassthroughModelProducesNoModelOverride(t *testing.T) {
	for _, agent := range []string{"claude", "codex", "copilot", "opencode", "pi", "agy"} {
		t.Run(agent, func(t *testing.T) {
			d := ByName(agent)
			if d == nil {
				t.Fatalf("%s driver not registered", agent)
			}
			lc := d.Build(passthroughModel(), false, Route{})
			for _, a := range lc.Args {
				if a == "--model" {
					t.Errorf("%s: Args = %v, want no --model flag for the passthrough sentinel", agent, lc.Args)
				}
			}
			if len(lc.Env) != 0 {
				t.Errorf("%s: Env = %v, want no env vars for the passthrough sentinel", agent, lc.Env)
			}
		})
	}
}

// BuildPassthroughCmd must build a bare launch command per driver: correct
// binary resolution, the requested worktree as cmd.Dir, no --model flag, and
// extraArgs appended. Skipped per-agent when the binary is not on PATH
// (mirrors the skip pattern cmd/wt/launch_test.go and internal/agents'
// resume tests already use for real-binary launcher tests).
func TestBuildPassthroughCmd(t *testing.T) {
	for _, agent := range []string{"claude", "codex", "copilot", "opencode", "pi", "agy"} {
		t.Run(agent, func(t *testing.T) {
			if !Installed(agent) {
				t.Skipf("%s not installed on PATH; skipping launcher test", agent)
			}
			cmd, err := BuildPassthroughCmd(agent, "/tmp/repo", false, nil)
			if err != nil {
				t.Fatalf("BuildPassthroughCmd: %v", err)
			}
			if cmd.Dir != "/tmp/repo" {
				t.Errorf("cmd.Dir = %q, want /tmp/repo", cmd.Dir)
			}
			for _, a := range cmd.Args {
				if a == "--model" {
					t.Errorf("args %v contain --model, want a bare launch", cmd.Args)
				}
			}
		})
	}
}

// BuildPassthroughCmd must thread yolo and extraArgs through to the built
// command, and must not panic on the nil cfg/session it passes to
// BuildLaunchCmd. Uses a test-only driver (bash-backed, always on PATH)
// instead of a real agent so the assertion is deterministic in any
// environment, unlike TestBuildPassthroughCmd's per-driver skips.
func TestBuildPassthroughCmdUsesSentinelAndExtraArgs(t *testing.T) {
	register("_test_passthrough", func() Driver { return regularTestDriver{} })
	t.Cleanup(func() { delete(registry, "_test_passthrough") })

	cmd, err := BuildPassthroughCmd("_test_passthrough", "/tmp", true, []string{"--foo"})
	if err != nil {
		t.Fatalf("BuildPassthroughCmd: %v", err)
	}
	if len(cmd.Args) == 0 || cmd.Args[len(cmd.Args)-1] != "--foo" {
		t.Errorf("cmd.Args = %v, want --foo appended", cmd.Args)
	}
}

// An unknown agent name must return the same "unknown agent" error
// BuildLaunchCmd already produces — BuildPassthroughCmd is a thin wrapper,
// not a second source of validation.
func TestBuildPassthroughCmdUnknownAgent(t *testing.T) {
	_, err := BuildPassthroughCmd("not-an-agent", "/tmp", false, nil)
	if err == nil || !strings.Contains(err.Error(), "unknown agent") {
		t.Errorf("err = %v, want it to contain \"unknown agent\"", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/agents/... -run 'TestOpenCodeNativeBypass|TestIsConfigured|TestPassthroughModelProducesNoModelOverride|TestBuildPassthroughCmd' -v`
Expected: FAIL — `TestOpenCodeNativeBypass` fails on assertion (opencode still emits env for the native model); the `passthrough_test.go` tests fail to compile (`IsConfigured`, `passthroughModel`, `BuildPassthroughCmd` undefined).

- [ ] **Step 3: Implement `IsConfigured` and `BuildPassthroughCmd`**

Create `internal/agents/passthrough.go`:

```go
package agents

import (
	"os/exec"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// IsConfigured reports whether name has an agent entry in cfg, and therefore
// a model catalog to resolve a launch against. Commands (e.g. shell) are
// always "configured": they have no model layer and need no entry. A nil
// cfg (only ever produced by a caller working around a config load failure)
// is never configured for a model-driven agent.
func IsConfigured(cfg *config.Config, name string) bool {
	if IsCommand(name) {
		return true
	}
	if cfg == nil {
		return false
	}
	_, err := cfg.AgentByName(name)
	return err == nil
}

// passthroughModel is the sentinel model for a bare launch. Every
// model-driven driver's native branch treats it as "no model override": no
// --model, no gateway env. ModelName "native" is the value drivers already
// use to mean "launch bare" (claude/copilot add --model or COPILOT_MODEL
// only for a specific *named* native model, e.g. claude/opus).
func passthroughModel() config.Model {
	return config.Model{Native: true, ModelName: "native"}
}

// BuildPassthroughCmd builds the command that launches agent with no model
// routing — equivalent to running the installed binary directly, plus the
// agent's yolo flag and the passthrough args. Used when an agent has no
// config.toml entry (issue #147): the whole config being absent
// (config.ErrRegistryMissing) is just the extreme case of "no entry".
//
// Passing nil cfg is safe: BuildLaunchCmd substitutes an empty Config,
// ResolveRoute early-returns for a Native model, and a Syncer driver (pi)
// syncs against zero models — a no-op that does not create its models.json
// when the file does not already exist and LiteLLM routing is off.
func BuildPassthroughCmd(agent, worktreePath string, yolo bool, extraArgs []string) (*exec.Cmd, error) {
	return BuildLaunchCmd(agent, passthroughModel(), worktreePath, yolo, nil, nil, extraArgs)
}
```

- [ ] **Step 4: Add the native guard to opencode**

Edit `internal/agents/opencode.go`'s `Build` method:

```go
func (opencodeDriver) Build(m config.Model, yolo bool, r Route) LaunchCmd {
	lc := LaunchCmd{Bin: "opencode"}
	if yolo {
		lc.Args = append(lc.Args, opencodeDriver{}.YoloFlag())
	}
	// Native dispatch: opencode is ollama-only in normal use, so it never
	// received a Native model before the unconfigured-agent passthrough
	// sentinel (issue #147). Without this guard, a native launch would
	// still emit OPENCODE_CONFIG_CONTENT pointing at an empty gateway base
	// URL instead of a bare `opencode` command.
	if m.Native {
		return lc
	}
	baseURL := r.BaseOrigin + "/v1"
	modelRef := opencodeGatewayProviderID + "/" + r.ModelRef
	lc.Env = append(lc.Env, "OPENCODE_CONFIG_CONTENT="+fmt.Sprintf(
		`{"model":%q,"small_model":%q,"provider":{%q:{"npm":"@ai-sdk/openai-compatible","name":"Agent WT Gateway","options":{"baseURL":%q,"apiKey":%q},"models":{%q:{"name":%q}}}}}`,
		modelRef, modelRef, opencodeGatewayProviderID, baseURL, r.APIKey, r.ModelRef, r.Display,
	))
	return lc
}
```

(Only the new `if m.Native { return lc }` block is added; the rest of the function is unchanged.)

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/agents/... -v`
Expected: PASS (all tests in the package, including the pre-existing ones — this step must not regress `TestOpenCode`/`TestOpenCodeOllamaPrefix`, which exercise the non-native branch and are unaffected by the new early return).

- [ ] **Step 6: Commit**

```bash
git add internal/agents/passthrough.go internal/agents/passthrough_test.go \
  internal/agents/opencode.go internal/agents/opencode_test.go
git commit -m "$(cat <<'EOF'
agents: add IsConfigured and BuildPassthroughCmd for unconfigured-agent launches

completes plan item #1

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: `cmd/wt` — tolerate a missing registry for every agent, fail fast on a missing binary

**Files:**
- Modify: `cmd/wt/main.go`
- Modify: `cmd/wt/testmain_test.go`
- Modify: `cmd/wt/main_test.go`

**Interfaces:**
- Consumes: `agents.IsCommand`, `agents.ByName`, `agents.Names`, `config.ErrRegistryMissing` (all pre-existing).
- Produces: `var installed = agents.Installed` (test seam in `cmd/wt`, mirroring `internal/tui`'s `installed` seam) — Task 3 and Task 4 do not depend on this signature changing again.

- [ ] **Step 1: Write the failing tests**

In `cmd/wt/main_test.go`, delete `TestNonCommandAgentStillFailsWithoutRegistry` (lines ~598–622) — its premise (a missing registry still fails closed for a real agent) is exactly what this feature removes; a passthrough-specific replacement is added in Task 3 once `launchPassthrough` exists. Replace it with:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/wt/... -run 'TestMalformedRegistryStillFailsClosed|TestPinnedAgentNotInstalledErrorsBeforeWorktreeWork|TestPinnedCommandAgentSkipsInstalledCheck' -v`
Expected: FAIL — `TestPinnedAgentNotInstalledErrorsBeforeWorktreeWork` and `TestPinnedCommandAgentSkipsInstalledCheck` fail to compile (`installed` undefined in package `main`); `TestMalformedRegistryStillFailsClosed` currently passes already by coincidence of the old gate, but must be re-verified after Step 3's gate rewrite in Step 5.

- [ ] **Step 3: Add the `installed` seam and relax the cfgErr gate**

In `cmd/wt/main.go`, add the seam right after the `tuiRun` var declaration:

```go
// tuiRun is the entry point for the interactive TUI. It is a package-level
// variable so tests can stub it (see TestWorktreeWithAgentWithoutModelShowsModelPicker)
// instead of opening a real /dev/tty.
var tuiRun = tui.Run

// installed is a test seam wrapping agents.Installed for the pinned-agent
// binary check in RunE (issue #147): a pinned agent whose binary is missing
// must fail before any worktree work, and tests stub this so that check does
// not depend on the host's installed binaries. cmd/wt's TestMain defaults it
// to true so existing tests are unaffected unless they opt into stubbing it.
var installed = agents.Installed
```

Replace the cfgErr gate (currently):

```go
			// Launch paths require a valid config. The `wt config` subcommand
			// bypasses this so it can repair a broken config.toml. Command
			// agents (shell, etc.) have no model layer, so a missing modelman
			// registry is tolerated for them — only real agents need the
			// registry, and they still fail closed with the migrate hint.
			if a.cfgErr != nil && !(agent != "" && agents.IsCommand(agent) && errors.Is(a.cfgErr, config.ErrRegistryMissing)) {
				return fmt.Errorf("config error: %w (run `wt config` to repair)", a.cfgErr)
			}
```

with:

```go
			// Launch paths require a valid config. The `wt config` subcommand
			// bypasses this so it can repair a broken config.toml. A missing
			// modelman registry is tolerated for every agent (issue #147): a
			// command agent has no model layer, and a model-driven agent with
			// no config.toml entry falls back to a bare passthrough launch
			// (see agents.IsConfigured and runLaunchPath, below). Anything
			// else — a config.toml/registry.toml that exists but does not
			// parse — still fails closed with the repair hint.
			if a.cfgErr != nil && !errors.Is(a.cfgErr, config.ErrRegistryMissing) {
				return fmt.Errorf("config error: %w (run `wt config` to repair)", a.cfgErr)
			}
```

Then, immediately after the unknown-agent fast-fail block (currently):

```go
			if agent != "" && agents.ByName(agent) == nil {
				return fmt.Errorf("unknown agent %q (known: %s)", agent, strings.Join(agents.Names(), ", "))
			}
```

add:

```go

			// A pinned model-driven agent whose binary is missing can never
			// launch, configured or not; fail before any worktree work rather
			// than deep inside BuildLaunchCmd after a worktree may already
			// have been created. Commands (e.g. shell) have no binary
			// requirement of their own — ListEntries/IssueFor already treat
			// them as always-installed.
			if agent != "" && !agents.IsCommand(agent) && !installed(agent) {
				return fmt.Errorf("agent %q is not installed — install the %s binary", agent, agent)
			}
```

- [ ] **Step 4: Default the seam in `TestMain`**

In `cmd/wt/testmain_test.go`, add one line to `TestMain` (package `main`, no new import needed — `installed` is a same-package identifier):

```go
func TestMain(m *testing.M) {
	probeInventory = func(*config.Config) localmodels.Snapshot { return localmodels.Snapshot{} }
	// A pinned agent's binary-presence check (issue #147) defaults to
	// "installed" so existing tests that pin an agent are unaffected; tests
	// that need to exercise the not-installed path stub this explicitly.
	installed = func(string) bool { return true }
	startModel = func(*config.Config, catalog.Row, bool) error {
		return errors.New("startModel not stubbed in this test")
	}
	...
```

(Insert the new line and its comment right after the `probeInventory` assignment; the rest of `TestMain` is unchanged.)

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./cmd/wt/... -v`
Expected: PASS for the whole package — this includes every pre-existing test (`TestCommandAgentSkipsMissingRegistryGate`, `TestAgentWithOneEligibleModelAutoLaunches`, `TestWorktreeWithAgentWithoutModelShowsModelPicker`, etc.), none of which pin an agent that is both real and installed-check-relevant in a way this task's changes touch (all default through the new `installed = true` stub). If any pre-existing test fails, it is because it pins a real agent whose flow now hits the new fast-fail path unexpectedly — re-read that test's fixture before changing production code.

- [ ] **Step 6: Commit**

```bash
git add cmd/wt/main.go cmd/wt/testmain_test.go cmd/wt/main_test.go
git commit -m "$(cat <<'EOF'
cmd/wt: tolerate a missing registry for every agent, fail fast on a missing binary

completes plan item #2

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: `cmd/wt` — launch unconfigured agents through the passthrough builder

**Files:**
- Modify: `cmd/wt/launch.go`
- Modify: `cmd/wt/main.go`
- Modify: `cmd/wt/helpers_test.go`
- Modify: `cmd/wt/main_test.go`

**Interfaces:**
- Consumes: `agents.IsConfigured`, `agents.BuildPassthroughCmd` (Task 1), `agents.IsCommand`, `runAgentCmd`, `yolo(cmd)` (all pre-existing in this package or Task 1).
- Produces: `var launchPassthrough = launchPassthroughImpl` — `func launchPassthroughImpl(agent, worktreePath string, yolo bool, extraArgs []string, cfg *config.Config) error`, a test seam mirroring `launchFiltered`. Task 4 (TUI) does not consume this — the TUI has its own `buildPassthrough` seam — but both call the same `agents.BuildPassthroughCmd`.

- [ ] **Step 1: Write the failing tests**

Add a test helper to `cmd/wt/helpers_test.go` (used by the new fixture below and by Task 3's `TestWorktreeWithAgentWithoutModelShowsModelPicker` fixture change):

```go
// writeConfiguredAgent writes config.toml with one agent entry (name,
// providerID) and a registry.toml with a matching provider and no models, so
// the agent is "configured" (has a config.toml entry) with zero eligible
// models — used by tests that need a configured model-driven agent whose
// model resolution still falls through to the picker, distinct from an
// unconfigured agent that falls through to passthrough.
func writeConfiguredAgent(t *testing.T, home, agentName, providerID string) {
	t.Helper()
	withCleanConfigEnv(t, home)
	cfgDir := filepath.Join(home, ".config", "agent-wt")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgToml := "default_tag = \"code\"\n[[agents]]\nname = \"" + agentName + "\"\nsupported_providers = [\"" + providerID + "\"]\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(cfgToml), 0o644); err != nil {
		t.Fatal(err)
	}
	regDir := filepath.Join(home, ".config", "local-ai")
	if err := os.MkdirAll(regDir, 0o755); err != nil {
		t.Fatal(err)
	}
	regToml := "[[providers]]\nid = \"" + providerID + "\"\nname = \"" + providerID + "\"\nlocation = \"local\"\nauth = { type = \"none\", base_url = \"http://localhost:11434\" }\n"
	if err := os.WriteFile(filepath.Join(regDir, "registry.toml"), []byte(regToml), 0o644); err != nil {
		t.Fatal(err)
	}
}
```

In `cmd/wt/main_test.go`, update `TestWorktreeWithAgentWithoutModelShowsModelPicker` (the fixture only — the test body and assertions are unchanged) by replacing:

```go
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("MODELMAN_REGISTRY", "")
	writeEmptyRegistry(t, home)
```

(in `TestWorktreeWithAgentWithoutModelShowsModelPicker`, immediately before `var gotAgent, gotPinned string`) with:

```go
	home := t.TempDir()
	// pi must be CONFIGURED (a config.toml entry, zero eligible models) so
	// this test still exercises "configured agent, unresolved model → TUI",
	// not the new unconfigured-agent passthrough this task adds.
	writeConfiguredAgent(t, home, "pi", "ollama")
```

Add new tests to `cmd/wt/main_test.go`:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/wt/... -run 'TestModelDrivenAgentPassesThroughWithoutRegistry|TestPinnedModelWithUnconfiguredAgentErrors|TestUnpinnedWorktreeSkipsPassthroughGuard|TestWorktreeWithAgentWithoutModelShowsModelPicker' -v`
Expected: FAIL — the three new tests fail to compile (`launchPassthrough` undefined); `TestWorktreeWithAgentWithoutModelShowsModelPicker` currently still passes on the old fixture (Task 2 didn't touch `runLaunchPath`), so re-run it after Step 3 to confirm it still passes on the new fixture too.

- [ ] **Step 3: Add `launchPassthrough` and wire it into `runLaunchPath`**

In `cmd/wt/launch.go`, add near `launchFiltered` (after its declaration and `launchFilteredImpl`, before `runAgentCmd`):

```go
// launchPassthrough builds and runs the bare-agent command for an
// unconfigured model-driven agent (agents.IsConfigured false) — no model
// routing, equivalent to running the installed binary directly. The zero
// config.Model passed to runAgentCmd makes the post-exit flow behave exactly
// like a command-agent launch: no survey, no stop picker, no price notice,
// and a summary line without a model segment.
//
// launchPassthrough is a package-level variable so tests can stub it,
// mirroring launchFiltered above.
var launchPassthrough = launchPassthroughImpl

func launchPassthroughImpl(agent, worktreePath string, yolo bool, extraArgs []string, cfg *config.Config) error {
	cmd, err := agents.BuildPassthroughCmd(agent, worktreePath, yolo, extraArgs)
	if err != nil {
		return err
	}
	return runAgentCmd(cmd, agent, config.Model{}, cfg)
}
```

In `cmd/wt/main.go`'s `runLaunchPath`, insert the new branch after `pinnedSupplied := cmd.Flags().Changed("model")` and before `if needsModelPicker(agent, pinned) {`:

```go
	pinnedSupplied := cmd.Flags().Changed("model")

	// An unconfigured model-driven agent (no config.toml entry — including
	// the extreme case of a wholly missing registry) has no model catalog
	// to resolve a launch against; launch it bare instead of routing
	// through the model layer. Guarded on agent != "" so an unpinned launch
	// (-W with no -A) still falls through to needsModelPicker's
	// agent-selection path below — cfg.AgentByName("") always fails too,
	// and an empty agent name is not "unconfigured", it's "not chosen yet".
	if agent != "" && !agents.IsCommand(agent) && !agents.IsConfigured(a.cfg, agent) {
		if pinned != "" {
			return fmt.Errorf("agent %q is not configured; cannot pin model %q", agent, pinned)
		}
		fmt.Fprintf(os.Stderr, "wt: %s is not configured — launching it directly without a model\n", agent)
		return launchPassthrough(agent, launchPath, yolo(cmd), args, a.cfg)
	}

	if needsModelPicker(agent, pinned) {
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/wt/... -v`
Expected: PASS for the whole package.

- [ ] **Step 5: Commit**

```bash
git add cmd/wt/launch.go cmd/wt/main.go cmd/wt/helpers_test.go cmd/wt/main_test.go
git commit -m "$(cat <<'EOF'
cmd/wt: launch unconfigured model-driven agents through the passthrough builder

completes plan item #3

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: `internal/tui` — passthrough rows in the agent picker and the pinned-agent path

**Files:**
- Modify: `internal/tui/agent_picker.go`
- Modify: `internal/tui/launch.go`
- Modify: `internal/tui/app.go`
- Modify: `internal/tui/agent_picker_test.go`

**Interfaces:**
- Consumes: `agents.IsConfigured` (Task 1, though `buildAgentList` uses `AgentListEntry.Configured` directly — see Step 3), `agents.BuildPassthroughCmd` (Task 1).
- Produces:
  - `agentItem.passthrough bool` — new field; `buildAgentList` sets it for an installed, unconfigured, non-command row.
  - `var buildPassthrough = agents.BuildPassthroughCmd` — test seam in `internal/tui`, mirroring `launchAgent`.
  - `func (m model) launchPassthrough(name string) (model, tea.Cmd)` — mirrors `launchCommand`.

- [ ] **Step 1: Write the failing tests**

Add `"os/exec"` to the import block of `internal/tui/agent_picker_test.go` (it currently imports `os`, `path/filepath`, `strings`, `testing`, `list`, `tea`, `config`, `session`, `worktree`).

Append to `internal/tui/agent_picker_test.go`:

```go
// An agent with no config.toml entry but a real binary on PATH is marked
// passthrough (launchable directly, no model) rather than blocked with a
// "not configured" issue — the fix for issue #147 (a fresh machine with no
// modelman/wt configuration must still be able to launch a real agent).
func TestBuildAgentListMarksInstalledUnconfiguredAsPassthrough(t *testing.T) {
	cfg := &config.Config{
		Agents: []config.Agent{
			{Name: "claude", SupportedProviders: []string{"ollama"}},
		},
	}
	t.Cleanup(stubInstalled("claude", "opencode"))

	items := buildAgentList(cfg)
	var oc agentItem
	found := false
	for _, it := range items {
		if ai, ok := it.(agentItem); ok && ai.name == "opencode" {
			oc, found = ai, true
		}
	}
	if !found {
		t.Fatal("missing opencode entry")
	}
	if !oc.passthrough {
		t.Error("opencode should be marked passthrough (installed, no config.toml entry)")
	}
	if oc.issue != "" {
		t.Errorf("opencode issue = %q, want empty for a passthrough row", oc.issue)
	}
}

// An uninstalled, unconfigured agent must stay blocked (its existing "not
// configured"/"not installed" issue text), never marked passthrough — there
// is no binary to launch bare.
func TestBuildAgentListUninstalledStaysBlockedNotPassthrough(t *testing.T) {
	cfg := &config.Config{
		Agents: []config.Agent{
			{Name: "claude", SupportedProviders: []string{"ollama"}},
		},
	}
	t.Cleanup(stubInstalled("claude")) // opencode is not installed

	items := buildAgentList(cfg)
	for _, it := range items {
		if ai, ok := it.(agentItem); ok && ai.name == "opencode" {
			if ai.passthrough {
				t.Error("uninstalled opencode must not be marked passthrough")
			}
			if ai.issue == "" {
				t.Error("uninstalled opencode should carry a non-empty issue")
			}
			return
		}
	}
	t.Fatal("missing opencode entry")
}

// Selecting a passthrough row in phaseAgent calls the buildPassthrough seam
// and launches with no model — the picker half of issue #147's fix.
func TestPhaseAgentEnterLaunchesPassthroughForUnconfiguredAgent(t *testing.T) {
	cfg := &config.Config{
		Agents: []config.Agent{
			{Name: "claude", SupportedProviders: []string{"ollama"}},
		},
	}
	t.Cleanup(stubInstalled("claude", "opencode"))

	var gotAgent, gotPath string
	oldBuildPassthrough := buildPassthrough
	buildPassthrough = func(agent, worktreePath string, yolo bool, extraArgs []string) (*exec.Cmd, error) {
		gotAgent, gotPath = agent, worktreePath
		return exec.Command("true"), nil
	}
	t.Cleanup(func() { buildPassthrough = oldBuildPassthrough })

	m := model{cfg: cfg, phase: phaseAgent, width: 80, height: 24, selectedPath: "/tmp/repo"}
	m.agentList = list.New(buildAgentList(cfg), list.NewDefaultDelegate(), 78, 22)
	for i, it := range m.agentList.Items() {
		if ai, ok := it.(agentItem); ok && ai.name == "opencode" {
			m.agentList.Select(i)
			break
		}
	}

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if gotAgent != "opencode" {
		t.Errorf("buildPassthrough agent = %q, want opencode", gotAgent)
	}
	if gotPath != "/tmp/repo" {
		t.Errorf("buildPassthrough worktreePath = %q, want /tmp/repo", gotPath)
	}
	if cmd == nil {
		t.Fatal("expected a launch cmd")
	}
}

// The pinned-agent CLI path (`-A <installed-but-unconfigured-agent>`, no
// -M) must also launch via the buildPassthrough seam instead of blocking on
// "not configured" or building a model catalog that doesn't exist.
func TestProceedFromSelectedPathPinnedUnconfiguredAgentPassesThrough(t *testing.T) {
	t.Cleanup(stubInstalled("opencode"))

	var called bool
	oldBuildPassthrough := buildPassthrough
	buildPassthrough = func(agent, worktreePath string, yolo bool, extraArgs []string) (*exec.Cmd, error) {
		called = true
		return exec.Command("true"), nil
	}
	t.Cleanup(func() { buildPassthrough = oldBuildPassthrough })

	m := model{cfg: &config.Config{}, phase: phaseList, width: 80, height: 24, initialAgent: "opencode"}
	m.selectedPath = t.TempDir()

	gotModel, cmd := m.Update(selectedEntryMsg{entry: worktree.Entry{Branch: "feature"}})
	nm := gotModel.(model)
	if !called {
		t.Fatal("expected buildPassthrough to be called")
	}
	if cmd == nil {
		t.Fatal("expected a launch cmd")
	}
	if nm.status != "" {
		t.Errorf("status = %q, want empty", nm.status)
	}
}

// `-A <unconfigured-agent> -M <id>` must error rather than silently
// dropping the pin or passing through — decision #5 of the passthrough
// design.
func TestProceedFromSelectedPathPinnedUnconfiguredAgentWithModelErrors(t *testing.T) {
	t.Cleanup(stubInstalled("opencode"))

	m := model{
		cfg: &config.Config{}, phase: phaseList, width: 80, height: 24,
		initialAgent: "opencode", pinnedModel: "ollama/foo",
	}
	m.selectedPath = t.TempDir()

	gotModel, cmd := m.Update(selectedEntryMsg{entry: worktree.Entry{Branch: "feature"}})
	nm := gotModel.(model)
	if cmd != nil {
		t.Errorf("expected no launch cmd, got %v", cmd)
	}
	if !strings.Contains(nm.status, "not configured") || !strings.Contains(nm.status, "ollama/foo") {
		t.Errorf("status = %q, want it to mention not configured and the pinned model", nm.status)
	}
}

// A pinned agent that is not installed must still be blocked with a clear
// status, not attempted as a passthrough (there is no binary to launch).
func TestProceedFromSelectedPathPinnedUninstalledAgentBlocked(t *testing.T) {
	t.Cleanup(stubInstalled()) // nothing installed

	m := model{cfg: &config.Config{}, phase: phaseList, width: 80, height: 24, initialAgent: "opencode"}
	m.selectedPath = t.TempDir()

	gotModel, cmd := m.Update(selectedEntryMsg{entry: worktree.Entry{Branch: "feature"}})
	nm := gotModel.(model)
	if cmd != nil {
		t.Errorf("expected no launch cmd, got %v", cmd)
	}
	if !strings.Contains(nm.status, "not installed") {
		t.Errorf("status = %q, want it to mention not installed", nm.status)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tui/... -run 'TestBuildAgentListMarksInstalledUnconfiguredAsPassthrough|TestBuildAgentListUninstalledStaysBlockedNotPassthrough|TestPhaseAgentEnterLaunchesPassthroughForUnconfiguredAgent|TestProceedFromSelectedPathPinnedUnconfiguredAgent|TestProceedFromSelectedPathPinnedUninstalledAgentBlocked' -v`
Expected: FAIL — compile errors (`agentItem.passthrough`, `buildPassthrough` undefined) and/or assertion failures once it compiles against a partial implementation.

- [ ] **Step 3: Add `agentItem.passthrough` and reclassify `buildAgentList`**

In `internal/tui/agent_picker.go`, replace the `agentItem` struct and its `Description` method:

```go
// agentItem is one row in the phaseAgent picker. command distinguishes
// agents (require model layer) from commands (launch directly). issue is a
// short, human-readable problem that prevents launch ("" = launchable).
// passthrough marks an agent that is installed but has no config.toml
// entry: Enter launches it directly with no model (issue #147). issue and
// passthrough are mutually exclusive — buildAgentList sets at most one.
type agentItem struct {
	name        string
	command     bool
	issue       string
	passthrough bool
}

func (a agentItem) FilterValue() string { return a.name }

func (a agentItem) Title() string {
	if a.command {
		return a.name + "  (command)"
	}
	return a.name + "  (agent)"
}

func (a agentItem) Description() string {
	if a.issue != "" {
		return a.issue
	}
	if a.command {
		return "no model; runs passthrough commands or interactive shell"
	}
	if a.passthrough {
		return "not configured — launches directly with no model"
	}
	return "agent: launches with a model"
}
```

Replace `buildAgentList`:

```go
// buildAgentList constructs the agent+command picker rows from the shared
// agents.ListEntries helper. Each configured agent and registered command
// appears once, sorted alphabetically; command classification and issue
// text are preserved on each row. An installed agent with no config.toml
// entry is marked passthrough (issue #147) instead of carrying
// ListEntries' "not configured" issue, so the row is launchable — Enter
// runs it bare, with no model. An uninstalled agent stays blocked with its
// issue regardless of configured state: there is no binary to launch
// either way. The installed check is threaded through the tui.installed
// seam so tests can stub it deterministically.
func buildAgentList(cfg *config.Config) []list.Item {
	entries := agents.ListEntries(cfg, installed)
	items := make([]list.Item, 0, len(entries))
	for _, e := range entries {
		it := agentItem{name: e.Name, command: e.Command}
		switch {
		case e.Command:
			// No issue, no passthrough — commands always launch directly.
		case !e.Installed:
			it.issue = e.Issue
		case !e.Configured:
			it.passthrough = true
		default:
			it.issue = e.Issue
		}
		items = append(items, it)
	}
	return items
}
```

- [ ] **Step 4: Add the `buildPassthrough` seam and `model.launchPassthrough`**

In `internal/tui/launch.go`, add near `launchAgent`:

```go
// launchAgent builds the command for agent/model in worktreePath, optionally
// appending passthrough args and a resume flag for claude or opencode. It
// delegates to agents.BuildLaunchCmd so the launch construction logic lives
// in one place.
func launchAgent(agent string, m config.Model, worktreePath string, yolo bool, sess *session.Session, cfg *config.Config, extraArgs []string) (*exec.Cmd, error) {
	return agents.BuildLaunchCmd(agent, m, worktreePath, yolo, sess, cfg, extraArgs)
}

// buildPassthrough is a test seam wrapping agents.BuildPassthroughCmd,
// mirroring launchAgent above — used for an agent with no config.toml entry
// (issue #147).
var buildPassthrough = agents.BuildPassthroughCmd
```

In `internal/tui/app.go`, add `launchPassthrough` near `launchCommand`:

```go
// launchPassthrough builds and runs a model-driven agent that has no
// config.toml entry, via the buildPassthrough seam — no model routing,
// equivalent to running the installed binary directly. Mirrors
// launchCommand's shape; the zero config.Model passed to runAndWaitCmd
// produces the same command-agent-like post-exit behavior (no survey, no
// stop picker, no price notice).
func (m model) launchPassthrough(name string) (model, tea.Cmd) {
	cmd, err := buildPassthrough(name, m.selectedPath, m.yolo, m.extraArgs)
	if err != nil {
		m.status = "launch failed: " + err.Error()
		return m, nil
	}
	return m, runAndWaitCmd(cmd, name, config.Model{})
}
```

Update the `phaseAgent` Enter handler in `internal/tui/app.go`'s `Update` (inside the `case "enter":` → `switch m.phase { case phaseAgent:` block), inserting the passthrough branch between the issue check and the model-catalog validation:

```go
				case phaseAgent:
					item, ok := m.agentList.SelectedItem().(agentItem)
					if !ok {
						return m, nil
					}
					m.agent = item.name
					if item.command {
						// Command (e.g. shell): no model layer — launch directly
						// by the picked driver's name, not a hardcoded "shell".
						return m.launchCommand(item.name)
					}
					// An agent that is not configured or not installed cannot
					// launch. Surface the reason inline instead of letting the
					// user advance to a model screen that can never succeed.
					if item.issue != "" {
						m.status = "cannot launch " + item.name + ": " + item.issue
						return m, nil
					}
					if item.passthrough {
						// Installed but no config.toml entry (issue #147):
						// launch directly, no model layer to resolve.
						return m.launchPassthrough(item.name)
					}
					// Agent: validate the model catalog for the agent + active
```

(Everything after this point in the `case phaseAgent:` block is unchanged.)

Update `proceedFromSelectedPath` in `internal/tui/app.go`, replacing:

```go
	if m.initialAgent != "" {
		m.agent = m.initialAgent
		if agents.IsCommand(m.agent) {
			return m.launchCommand(m.agent)
		}
		// A pinned agent that is not configured or not installed cannot
		// launch; surface the reason instead of a cryptic "agent not found"
		// from EligibleModels or a late "not installed" from the launch path.
		if issue := agents.IssueFor(m.cfg, m.agent, installed); issue != "" {
			m.status = "cannot launch " + m.agent + ": " + issue
			return m, nil
		}
		// Pinned agent: skip the picker, run the same model setup
		// that phaseAgent Enter would have run for an agent item.
```

with:

```go
	if m.initialAgent != "" {
		m.agent = m.initialAgent
		if agents.IsCommand(m.agent) {
			return m.launchCommand(m.agent)
		}
		// The installed check runs first, regardless of configured state:
		// there is no binary to launch bare or otherwise (decision #3 of the
		// passthrough design — the installed check is early and
		// pinned-agent-only).
		if !installed(m.agent) {
			m.status = "cannot launch " + m.agent + ": not installed — install the binary"
			return m, nil
		}
		if !agents.IsConfigured(m.cfg, m.agent) {
			// Installed but no config.toml entry (issue #147): launch
			// directly, unless the user pinned a model that cannot be
			// honored — an explicit pin must be surfaced, never silently
			// dropped (decision #5).
			if m.pinnedModel != "" {
				m.status = fmt.Sprintf("agent %q is not configured; cannot pin model %q", m.agent, m.pinnedModel)
				return m, nil
			}
			return m.launchPassthrough(m.agent)
		}
		// Pinned agent: skip the picker, run the same model setup
		// that phaseAgent Enter would have run for an agent item.
```

(The `firstTag := ...` line and everything after it in this function is unchanged.)

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/tui/... -v`
Expected: PASS for the whole package — including the pre-existing `TestPhaseAgentEnterBlocksUnconfiguredAgent` (still passes: it selects `opencode` with only `claude` stubbed installed, so `opencode` is uninstalled and stays in the `issue` branch, whose text is still `agents.IssueFor`'s "not configured" wording) and `TestBuildAgentListShowsIssues` (same reasoning).

- [ ] **Step 6: Commit**

```bash
git add internal/tui/agent_picker.go internal/tui/launch.go internal/tui/app.go internal/tui/agent_picker_test.go
git commit -m "$(cat <<'EOF'
tui: launch unconfigured agents through the passthrough builder

completes plan item #4

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: Docs

**Files:**
- Modify: `CLAUDE.md` (the `wt/` package-level one, not the monorepo root)
- Modify: `docs/wt-config.md`
- Modify: `docs/superpowers/specs/2026-09-04-shell-wt-no-registry-design.md`

**Interfaces:** None (documentation only).

- [ ] **Step 1: Update `wt/CLAUDE.md`'s Registry section**

Find this paragraph (under `## Registry (modelman-owned)`):

```
wt loads it read-only via `config.Load` (fail-closed: missing/malformed
registry is an error; seed with `modelman migrate`) and joins it in memory
with its own `config.toml`, which now holds only Agents + DefaultTag. `Save`
```

Insert a new paragraph immediately after it (before the `**Read-side schemas are pinned...` callout that follows):

```markdown

**Unconfigured-agent passthrough (issue #147).** A missing registry
(`config.ErrRegistryMissing`) is tolerated by the launch-path gate for
*every* agent, not just commands: `config.Load` still fails closed the same
way (this paragraph is otherwise unchanged), but `cmd/wt`'s `rootCmd().RunE`
now only re-raises the error when it is *not* `ErrRegistryMissing`. A
model-driven agent with no `config.toml` entry — the missing-registry case
included, since `a.cfg` falls back to an empty `Config` with no agents —
reads as unconfigured via `agents.IsConfigured(cfg, name)` and launches its
installed binary directly with no model routing, through
`agents.BuildPassthroughCmd`. `-M` against an unconfigured agent is still an
error. A genuinely malformed `config.toml`/`registry.toml` (a real parse
error, not `ErrRegistryMissing`) still fails closed with the repair hint.
See `docs/superpowers/specs/2026-09-22-wt-unconfigured-agent-passthrough-design.md`.
```

- [ ] **Step 2: Update `wt/CLAUDE.md`'s Agents section**

Find this paragraph (under `## Agents (Go)`, the first paragraph):

```
Each agent registers a `Driver` (`Build(m config.Model, yolo bool, r config.Route) LaunchCmd`, `YoloFlag() string`). `BuildLaunchCmd(agent, m, worktreePath, yolo, sess, cfg, extraArgs)` is the shared constructor used by both TUI and non-TUI launch paths — it resolves one `config.Route` per launch via `(*config.Config).ResolveRoute(m, agents.ProtocolsFor(agent))` and hands it to `Build`; drivers should not bypass it.
```

Insert a new paragraph immediately after it:

```markdown

**Unconfigured-agent passthrough.** `agents.IsConfigured(cfg, name)` is the
single source of truth for "does this agent have a model catalog to resolve
against" — commands are always configured; a model-driven agent needs a
`config.toml` entry. When false, both launch paths skip the model layer and
call `agents.BuildPassthroughCmd(agent, worktreePath, yolo, extraArgs)`,
which reuses every driver's existing native-model branch via the sentinel
`config.Model{Native: true, ModelName: "native"}` — no `--model`, no gateway
env, the same env-clearing (`claude` clears `ANTHROPIC_*`, `copilot` clears
`COPILOT_*`) a real native launch gets. `opencode`'s `Build` is the one
driver that gained a native-model guard for this (it was previously
ollama-only and never saw a native model). The TUI marks an installed,
unconfigured agent row `passthrough` in the agent+command picker instead of
the usual "not configured" issue; an uninstalled agent stays blocked either
way, since a passthrough launch still needs a binary to exec.
```

- [ ] **Step 3: Update `docs/wt-config.md`**

Find (under `### Agent list`):

```
The scrollable list includes commands first, then regular agents, both
groups sorted by name. Each row shows the agent's supported providers and
one of `✓ installed`, `✗ not installed`, or `✗ not configured`.
```

Insert a note immediately after it:

```markdown

> **`wt config`'s "not configured" marker is not the same as launch
> eligibility.** This editor exists to add the missing `config.toml` entry,
> so a `✗ not configured` row here is exactly what it says. But in the
> worktree-launcher's own agent+command picker (`wt`, not `wt config`), an
> installed agent with no `config.toml` entry is launchable anyway — Enter
> launches it directly with no model routing (issue #147). Only an
> uninstalled agent is truly blocked in the launcher.
```

- [ ] **Step 4: Add the spec pointer to the 2026-09-04 design doc**

In `docs/superpowers/specs/2026-09-04-shell-wt-no-registry-design.md`, add a one-line pointer right after its `## Summary` heading (before the `shell-wt (and any command agent)...` paragraph):

```markdown
## Summary

> **Extended 2026-09-22:** [`2026-09-22-wt-unconfigured-agent-passthrough-design.md`](2026-09-22-wt-unconfigured-agent-passthrough-design.md) generalizes the missing-registry tolerance this spec introduced for command agents to model-driven agents too. The command-agent carve-out below is unchanged; only the "real agents always fail closed" half was replaced.

`shell-wt` (and any command agent) should work on a fresh machine where the
```

- [ ] **Step 5: Verify links**

Run: `make check-links` (from the monorepo root, `cd ..`) or `uv run bin/check-links` from the repo root.
Expected: no broken-link errors introduced by the new cross-reference in Step 4.

- [ ] **Step 6: Commit**

```bash
git add CLAUDE.md docs/wt-config.md docs/superpowers/specs/2026-09-04-shell-wt-no-registry-design.md
git commit -m "$(cat <<'EOF'
docs: document unconfigured-agent passthrough (issue #147)

completes plan item #5

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: Full verification

**Files:** None (verification only).

- [ ] **Step 1: Run the full Go test suite**

Run: `go test ./... -v 2>&1 | tail -100` (from `wt/`)
Expected: PASS, 0 failures.

- [ ] **Step 2: Run `go vet`**

Run: `go vet ./...` (from `wt/`)
Expected: clean, no output.

- [ ] **Step 3: Run the repo's full check target**

Run: `make check` (from `wt/`) — shellcheck lint + shfmt format-check + `go-format-check` (`gofmt -l`).
Expected: clean, no output (no unformatted files).

- [ ] **Step 4: Run the monorepo-wide verification**

Run: `make test-all` (from the monorepo root, `cd ..`)
Expected: PASS — lint + modelman `make check`/`make test` + wt `go build`/`vet`/`test`. This is the closest local equivalent to CI and is the final gate before considering the plan complete.

- [ ] **Step 5: Manual smoke check (optional but recommended)**

Build the binary and exercise the new path by hand in a scratch directory with no `~/.config/agent-wt` or `~/.config/local-ai`:

```bash
go build -o /tmp/wt-verify ./cmd/wt
mkdir -p /tmp/wt-passthrough-check && cd /tmp/wt-passthrough-check && git init -q && git commit --allow-empty -q -m init
HOME=/tmp/wt-fake-home XDG_CONFIG_HOME=/tmp/wt-fake-home/.config /tmp/wt-verify --cwd -A claude
```

Expected: a stderr line `wt: claude is not configured — launching it directly without a model`, followed by an attempt to exec the real `claude` binary (fails with "not installed" if claude isn't on the machine running this check — that failure mode itself is the confirmation that passthrough construction succeeded and only the exec step failed for an unrelated, expected reason).

No commit for this task — it is verification of Tasks 1–5, not new production code.

---

## Self-Review

**Spec coverage:**
- Decision 1 (passthrough for unconfigured agents) → Tasks 1, 3, 4.
- Decision 2 (configured = config.toml entry; zero eligible models still errors) → Task 3's `TestWorktreeWithAgentWithoutModelShowsModelPicker` fixture (configured pi, 0 models, still routes to picker) plus the pre-existing (unmodified) "no models for agent" error paths in `cmd/wt/resolve.go` and `internal/tui/app.go`.
- Decision 3 (installed check early, pinned-agent-only) → Task 2 (`cmd/wt`) and Task 4's `proceedFromSelectedPath` rewrite (`internal/tui`); the unpinned agent picker's inline issue is untouched (`buildAgentList`'s uninstalled branch).
- Decision 4 (provider-count does not gate the picker) → no code changes needed; out of scope, not touched by any task.
- Decision 5 (`-M` on unconfigured agent errors) → `TestPinnedModelWithUnconfiguredAgentErrors` (Task 3) and `TestProceedFromSelectedPathPinnedUnconfiguredAgentWithModelErrors` (Task 4).
- Decision 6 (sentinel model reuses native branches; opencode gap) → Task 1.
- Decision 7 (fail closed on malformed config) → `TestMalformedRegistryStillFailsClosed` (Task 2).
- Non-TUI wiring (`cmd/wt`) → Tasks 2–3.
- TUI wiring (`internal/tui`) → Task 4.
- Testing section's bullet list → every bullet maps to a named test above (internal/agents: opencode native, convention test, BuildPassthroughCmd shape; cmd/wt: missing-registry fixture, -M error, pinned-uninstalled error, malformed-registry fail, configured-agent-unchanged; internal/tui: buildAgentList marking, phaseAgent Enter seam call, pinned-unconfigured with/without -M).
- Docs → Task 5 (with one deliberate deviation: `docs/configuration.md`, named in the spec, documents Claude Code/Codex CLI settings and has no registry/agent-config content to amend; `docs/wt-config.md` is the doc that actually covers the `wt config` "not configured" marker the spec's docs bullet is about, so the note goes there instead).

**Placeholder scan:** no TBD/TODO, no "add appropriate handling", no "similar to Task N" — every step has literal code or an exact `go test`/`make` command.

**Type consistency:** `IsConfigured(cfg *config.Config, name string) bool`, `BuildPassthroughCmd(agent, worktreePath string, yolo bool, extraArgs []string) (*exec.Cmd, error)`, `launchPassthrough` (both the `cmd/wt` var `func(agent, worktreePath string, yolo bool, extraArgs []string, cfg *config.Config) error` and the unrelated `internal/tui` method `func (m model) launchPassthrough(name string) (model, tea.Cmd)`), and `buildPassthrough var = agents.BuildPassthroughCmd` are each used identically at every call site across Tasks 1–4.
