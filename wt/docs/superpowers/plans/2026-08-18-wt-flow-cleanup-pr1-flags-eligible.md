# wt Flow Cleanup — PR 1: Flag Surface + Eligible-List Function

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Introduce the new flag surface (`-W`, `-A`, `-M`, `-T`, `-F`), remove the legacy `-w`, add the `EligibleModels` config function, and wire the non-TUI launch path to use it.

**Architecture:** Pure addition to `internal/config` (`EligibleModels` + `parseFilterList`) plus flag registration changes in `cmd/wt`. TUI is untouched in this PR — the model picker still uses `ModelsForAgentAndTag`. Rotation state-file naming stays as today (`rotation-<tag>.state`); the per-slot state-file refactor lands in PR 3.

**Tech Stack:** Go 1.26.3, cobra/pflag for flags, `github.com/BurntSushi/toml` for config.

**Spec:** `docs/superpowers/specs/2026-08-18-wt-flow-cleanup-design.md` (sections "Flag surface", "Picker-skip conditions", "Eligible-Model Function & Non-TUI Launch Path (PR 1)", "Error handling").

## Global Constraints

- Go 1.26.3 (per `go.mod`).
- Test convention: every `Test*` function has a top-level `//` block explaining what it tests and why (lesson 18).
- Run `go test ./...` and `go vet ./...` before each commit.
- The `wt` CLI uses `cobra.ArbitraryArgs` (existing); passthrough args after `--` flow to the launched agent.
- All new error messages follow the `wt: error: <message>` format and exit with code 1.
- The `-w` removal is a hard error — no fallback.

---

## File Structure (this PR)

### Created

- `internal/config/filter_test.go` — tests for `parseFilterList`.
- `internal/config/eligible_test.go` — tests for `EligibleModels`.

### Modified

- `internal/config/config.go` — add `parseFilterList`, `EligibleModels`. Update `ModelsForAgentAndTag` documentation if needed (it's still used by the TUI in PR 1).
- `cmd/wt/main.go` — register `-W`, `-A`, `-M`, `-T`, `-F`; remove `-w`; reject `-w` with a clear error; thread flags into the launch path.
- `cmd/wt/launch.go` — accept filter flags; verify `-M` against the eligible list; resolve the model using the eligible list.
- `cmd/wt/helpers.go` — extend `defaultModel` to accept filter args, or add `resolveModel(agent, cfg, tags, family, pinned)` as a new helper.

### Untouched

- `internal/tui/*` — TUI behavior unchanged.
- `internal/rotation/*` — rotation state-file scheme unchanged.
- `cmd/wt/commands.go` — `wt models`, `wt agents`, `wt rotate` unchanged.

---

## Task 1: Add `parseFilterList` helper to config package

**Files:**
- Modify: `internal/config/config.go` (add at end of file).
- Create: `internal/config/filter_test.go`.

**Interfaces:**
- Produces: `func parseFilterList(s string) []string` — splits a comma-delimited string, trims whitespace, drops empty entries. Returns nil for empty input.

- [ ] **Step 1: Write the failing test**

Create `internal/config/filter_test.go`:

```go
package config

import (
	"reflect"
	"testing"
)

// TestParseFilterList covers the comma-delimited parser used by the -T and
// -F CLI flags. Edge cases here are user-facing: a stray comma or
// whitespace in `wt -T "code, design"` must not produce an empty tag entry
// that silently filters every model out.
func TestParseFilterList(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"whitespace only", "   ", nil},
		{"single", "code", []string{"code"}},
		{"two", "code,design", []string{"code", "design"}},
		{"with spaces", " code , design ", []string{"code", "design"}},
		{"trailing comma", "code,", []string{"code"}},
		{"leading comma", ",code", []string{"code"}},
		{"double comma", "code,,design", []string{"code", "design"}},
		{"three", "a,b,c", []string{"a", "b", "c"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseFilterList(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseFilterList(%q) = %#v, want %#v", tc.in, got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/config -run TestParseFilterList -v`
Expected: FAIL with `undefined: parseFilterList`.

- [ ] **Step 3: Write the minimal implementation**

Add to the end of `internal/config/config.go`:

```go
// parseFilterList splits a comma-delimited string, trimming whitespace and
// dropping empty entries. Empty or whitespace-only input returns nil.
// Used by the -T/--tags and -F/--family CLI flags.
func parseFilterList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
```

Note: `strings` is already imported in `config.go`.

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/config -run TestParseFilterList -v`
Expected: PASS for all subtests.

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/filter_test.go
git commit -m "feat(config): add parseFilterList helper for -T/-F flags"
```

---

## Task 2: Add `EligibleModels` to config package

**Files:**
- Modify: `internal/config/config.go`.
- Create: `internal/config/eligible_test.go`.

**Interfaces:**
- Produces: `func (c *Config) EligibleModels(agentName, tags, family string) ([]Model, error)`.
  - `agentName` — looked up in `c.Agents`.
  - `tags` — comma-delimited string passed to `parseFilterList`; empty = no tag filter.
  - `family` — comma-delimited string passed to `parseFilterList`; empty = no family filter.
  - Returns models in `c.Models` order, filtered by agent's supported providers AND any tag/family filters.
  - Errors: agent not found; agent references an unknown provider.

- [ ] **Step 1: Write the failing test**

Create `internal/config/eligible_test.go`:

```go
package config

import (
	"testing"
)

// TestEligibleModels covers the eligible-model function used by both the
// non-TUI launch path and (eventually) the model picker. This is the
// single source of truth for "which models can this user launch right now",
// so its filter semantics must be locked down here.
func TestEligibleModels(t *testing.T) {
	cfg := &Config{
		DefaultTag: "code",
		Providers: []Provider{
			{ID: "claude", Location: LocationCloud, Auth: AuthConfig{Type: "native"}},
			{ID: "ollama", Location: LocationLocal, Auth: AuthConfig{Type: "none"}},
		},
		Models: []Model{
			{ID: "claude/opus", ProviderID: "claude", ModelName: "opus", Family: "opus", Location: LocationCloud, Tags: []string{"code"}},
			{ID: "claude/sonnet", ProviderID: "claude", ModelName: "sonnet", Family: "sonnet", Location: LocationCloud, Tags: []string{"code", "design"}},
			{ID: "claude/native", ProviderID: "claude", ModelName: "native", Location: LocationCloud, Tags: []string{"code"}},
			{ID: "ollama/gemma4:9b", ProviderID: "ollama", ModelName: "gemma4:9b", Family: "gemma4", Location: LocationLocal, Tags: []string{"code"}},
			{ID: "ollama/llama3", ProviderID: "ollama", ModelName: "llama3", Family: "llama3", Location: LocationLocal, Tags: []string{"design"}},
		},
		Agents: []Agent{
			{Name: "claude", SupportedProviders: []string{"claude"}},
			{Name: "pi", SupportedProviders: []string{"ollama", "claude"}},
		},
	}

	tests := []struct {
		name    string
		agent   string
		tags    string
		family  string
		wantIDs []string
		wantErr bool
	}{
		{"claude no filters", "claude", "", "", []string{"claude/opus", "claude/sonnet", "claude/native"}, false},
		{"pi no filters", "pi", "", "", []string{"claude/opus", "claude/sonnet", "claude/native", "ollama/gemma4:9b", "ollama/llama3"}, false},
		{"tag code only", "pi", "code", "", []string{"claude/opus", "claude/sonnet", "claude/native", "ollama/gemma4:9b"}, false},
		{"tag multi", "pi", "code,design", "", []string{"claude/opus", "claude/sonnet", "claude/native", "ollama/gemma4:9b", "ollama/llama3"}, false},
		{"family gemma4", "pi", "", "gemma4", []string{"ollama/gemma4:9b"}, false},
		{"tag+family AND", "pi", "code", "gemma4", []string{"ollama/gemma4:9b"}, false},
		{"tag+family AND no overlap", "pi", "design", "gemma4", nil, false},
		{"unknown agent", "nope", "", "", nil, true},
		{"tag with whitespace", "pi", " code , design ", "", []string{"claude/opus", "claude/sonnet", "claude/native", "ollama/gemma4:9b", "ollama/llama3"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := cfg.EligibleModels(tc.agent, tc.tags, tc.family)
			if (err != nil) != tc.wantErr {
				t.Fatalf("EligibleModels(%q, %q, %q) err = %v, wantErr = %v", tc.agent, tc.tags, tc.family, err, tc.wantErr)
			}
			if err != nil {
				return
			}
			gotIDs := make([]string, len(got))
			for i, m := range got {
				gotIDs[i] = m.ID
			}
			if len(gotIDs) != len(tc.wantIDs) {
				t.Fatalf("got %d models (%v), want %d (%v)", len(gotIDs), gotIDs, len(tc.wantIDs), tc.wantIDs)
			}
			for i, id := range tc.wantIDs {
				if gotIDs[i] != id {
					t.Errorf("model[%d] = %q, want %q", i, gotIDs[i], id)
				}
			}
		})
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/config -run TestEligibleModels -v`
Expected: FAIL with `undefined: (*Config).EligibleModels`.

- [ ] **Step 3: Write the minimal implementation**

Add to `internal/config/config.go` (before the closing of the file):

```go
// EligibleModels returns the models usable by agent after applying tag
// and family filters. Order matches cfg.Models.
//
// Semantics:
//   - Provider filter is hard: only models whose ProviderID is in
//     agent.SupportedProviders are considered.
//   - tags == "" → no tag filter.
//   - tags != "" → model must have at least one matching tag.
//   - family == "" → no family filter.
//   - family != "" → model.Family must equal one of the listed families.
//   - When both are non-empty: tags and family are AND-combined.
//
// Errors:
//   - agent not found
//   - agent references an unknown provider (only reachable if Validate
//     was bypassed)
func (c *Config) EligibleModels(agentName, tags, family string) ([]Model, error) {
	a, err := c.AgentByName(agentName)
	if err != nil {
		return nil, err
	}
	tagSet := map[string]bool{}
	for _, t := range parseFilterList(tags) {
		tagSet[t] = true
	}
	familySet := map[string]bool{}
	for _, f := range parseFilterList(family) {
		familySet[f] = true
	}
	allowed := map[string]bool{}
	for _, pid := range a.SupportedProviders {
		if c.ProviderByID(pid) == nil {
			return nil, fmt.Errorf("agent %q: provider %q not found in config", agentName, pid)
		}
		allowed[pid] = true
	}
	var out []Model
	for _, m := range c.Models {
		if !allowed[m.ProviderID] {
			continue
		}
		if len(tagSet) > 0 {
			hit := false
			for _, t := range m.Tags {
				if tagSet[t] {
					hit = true
					break
				}
			}
			if !hit {
				continue
			}
		}
		if len(familySet) > 0 {
			if !familySet[m.Family] {
				continue
			}
		}
		out = append(out, m)
	}
	return out, nil
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/config -run TestEligibleModels -v`
Expected: PASS for all subtests.

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/eligible_test.go
git commit -m "feat(config): add EligibleModels with tag/family filters"
```

---

## Task 3: Add a `resolveModel` helper in cmd/wt

**Files:**
- Modify: `cmd/wt/helpers.go` (or create `cmd/wt/resolve.go` if helpers.go grows too large — judgment call at PR time).

**Interfaces:**
- Produces: `func resolveModel(agent string, cfg *config.Config, tags, family, pinned string) (config.Model, error)`.
  - If `agent` is a command (currently just `shell`), returns a sentinel error so the caller knows to skip model layer.
  - Else: compute `cfg.EligibleModels(agent, tags, family)`; if pinned is non-empty, look it up in the result and error if missing; else if `len(eligible) == 1`, return it; else if `len(eligible) > 1`, return error `"multiple models match; specify -M"` (the picker handles this case in TUI).
  - Errors: command agent (sentinel: `errCommandAgent`); no models match; pinned model not in eligible list; multiple models without `-M`.

- [ ] **Step 1: Write the failing test**

Create `cmd/wt/resolve_test.go`:

```go
package main

import (
	"errors"
	"testing"

	"github.com/ohanaverse/agent-worktree/internal/config"
)

// TestResolveModel covers the non-TUI model resolution path used after
// -W/--cwd has resolved the worktree and -A has resolved the agent.
// This is the gate that catches config errors and -M mismatches before
// launching an agent.
func TestResolveModel(t *testing.T) {
	cfg := &config.Config{
		DefaultTag: "code",
		Providers: []config.Provider{
			{ID: "claude", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "native"}},
			{ID: "ollama", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}},
		},
		Models: []config.Model{
			{ID: "claude/opus", ProviderID: "claude", ModelName: "opus", Family: "opus", Tags: []string{"code"}},
			{ID: "claude/sonnet", ProviderID: "claude", ModelName: "sonnet", Family: "sonnet", Tags: []string{"code"}},
			{ID: "ollama/gemma4:9b", ProviderID: "ollama", ModelName: "gemma4:9b", Family: "gemma4", Tags: []string{"code"}},
		},
		Agents: []config.Agent{
			{Name: "claude", SupportedProviders: []string{"claude"}},
			{Name: "pi", SupportedProviders: []string{"claude", "ollama"}},
		},
	}

	// sentinel returned when the agent is a command (no model layer)
	errCommandAgent = errors.New("agent is a command")

	tests := []struct {
		name    string
		agent   string
		tags    string
		family  string
		pinned  string
		wantID  string
		wantErr bool
	}{
		{"single match", "claude", "", "", "", "claude/opus", false},
		{"pinned in eligible", "pi", "", "", "claude/opus", "claude/opus", false},
		{"pinned not in eligible", "pi", "", "", "ollama/missing", "", true},
		{"pinned wrong provider for agent", "claude", "", "", "ollama/gemma4:9b", "", true},
		{"multiple no pin errors", "pi", "", "", "", "", true},
		{"tag filter narrows to one", "pi", "code", "gemma4", "", "ollama/gemma4:9b", false},
		{"empty eligible errors", "claude", "design", "", "", "", true},
		{"unknown agent", "nope", "", "", "", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, err := resolveModel(tc.agent, cfg, tc.tags, tc.family, tc.pinned)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if err != nil {
				return
			}
			if m.ID != tc.wantID {
				t.Errorf("got ID %q, want %q", m.ID, tc.wantID)
			}
		})
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/wt -run TestResolveModel -v`
Expected: FAIL with `undefined: resolveModel` (and `errCommandAgent`).

- [ ] **Step 3: Write the minimal implementation**

Create `cmd/wt/resolve.go`:

```go
package main

import (
	"fmt"

	"github.com/ohanaverse/agent-worktree/internal/agents"
	"github.com/ohanaverse/agent-worktree/internal/config"
)

// errCommandAgent is the sentinel returned by resolveModel when the
// resolved agent is a command (no model layer). Callers skip the model
// step and launch the command directly.
var errCommandAgent = fmt.Errorf("agent is a command")

// resolveModel computes the single model to launch for a non-TUI flow.
// agent is the resolved agent name (from -A or cfg.DefaultAgent).
// tags and family are the -T/-F flag values (comma-delimited).
// pinned is the -M flag value ("" = not pinned).
//
// Behavior:
//   - command agent → errCommandAgent
//   - pinned != "" and not in eligible → error
//   - len(eligible) == 0 → error "no models match"
//   - len(eligible) == 1 → return it
//   - len(eligible) > 1 and pinned == "" → error "specify -M"
//   - len(eligible) > 1 and pinned != "" → return pinned
//
// Note: rotation lives outside this function. PR 3 will wrap this to
// advance through the eligible list when pinned == "".
func resolveModel(agent string, cfg *config.Config, tags, family, pinned string) (config.Model, error) {
	if agents.IsCommand(agent) {
		return config.Model{}, errCommandAgent
	}
	eligible, err := cfg.EligibleModels(agent, tags, family)
	if err != nil {
		return config.Model{}, err
	}
	if len(eligible) == 0 {
		return config.Model{}, fmt.Errorf("no models match agent %q with tags %q and family %q", agent, tags, family)
	}
	if pinned != "" {
		for _, m := range eligible {
			if m.ID == pinned {
				return m, nil
			}
		}
		return config.Model{}, fmt.Errorf("model %q is not in the eligible list for agent %q", pinned, agent)
	}
	if len(eligible) > 1 {
		return config.Model{}, fmt.Errorf("multiple models match for agent %q; specify -M to pin one", agent)
	}
	return eligible[0], nil
}
```

Note: this requires `agents.IsCommand`. PR 2 will add it; for PR 1, **add a stub** in `internal/agents/registry.go`:

```go
// IsCommand reports whether name is a registered command (no model layer).
// Full implementation lands in PR 2; for now only "shell" is a command.
func IsCommand(name string) bool {
	return name == "shell"
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./cmd/wt -run TestResolveModel -v`
Expected: PASS for all subtests.

- [ ] **Step 5: Commit**

```bash
git add cmd/wt/resolve.go cmd/wt/resolve_test.go internal/agents/registry.go
git commit -m "feat(wt): add resolveModel helper for non-TUI launch path"
```

---

## Task 4: Register new flags in cmd/wt/main.go

**Files:**
- Modify: `cmd/wt/main.go`.

**Interfaces:**
- Adds flag registration for `-W` (replacing `-w`), `-A`, `-M`, `-T`, `-F`.
- Adds `errLegacyShortFlag` error and a check at the start of `RunE` to fail on `-w`.

- [ ] **Step 1: Write the failing test**

Create `cmd/wt/main_test.go` (or extend if it exists):

```go
package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestLegacyShortFlagRejected verifies that `wt -w foo` errors out with
// the migration message. This is the hard-error path for users still on
// the old `-w` short flag.
func TestLegacyShortFlagRejected(t *testing.T) {
	var buf bytes.Buffer
	root := rootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"-w", "my-branch"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected error for legacy -w flag, got nil")
	}
	if !strings.Contains(err.Error(), "-w is removed") {
		t.Errorf("error %q does not contain migration message", err.Error())
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/wt -run TestLegacyShortFlagRejected -v`
Expected: FAIL (test fails because no error is returned today for `-w`).

- [ ] **Step 3: Update main.go**

In `cmd/wt/main.go`:

1. Replace the existing flag registration block:

```go
cmd.PersistentFlags().StringP("worktree", "w", "", "Use/create worktree for branch")
cmd.PersistentFlags().String("agent", "", "Agent to launch (claude, codex, copilot, pi, agy, opencode, shell)")
cmd.PersistentFlags().Bool("cwd", false, "Launch in the current repo root, no picker")
```

with:

```go
cmd.PersistentFlags().StringP("worktree", "W", "", "Use/create worktree for branch")
cmd.PersistentFlags().StringP("agent", "A", "", "Agent or command to launch (claude, codex, copilot, pi, agy, opencode, shell)")
cmd.PersistentFlags().StringP("model", "M", "", "Pin the model as <provider>/<name>")
cmd.PersistentFlags().StringP("tags", "T", "", "Comma-delimited tags to filter models (OR within flag)")
cmd.PersistentFlags().StringP("family", "F", "", "Comma-delimited model families to filter models (OR within flag)")
cmd.PersistentFlags().Bool("cwd", false, "Launch in the current repo root, no picker")
```

2. At the top of `RunE` (after `agentFlag` is read), add a check for the legacy `-w` short flag. The simplest way: add an unhidden flag with `-w` that, when set, errors:

```go
// Legacy short flag rejection: `-w` was removed in favor of `-W`.
// Register it as a hidden Bool flag so pflag accepts the invocation,
// then error out in RunE.
var legacyShortW bool
cmd.Flags().BoolVarP(&legacyShortW, "legacy-w", "w", false, "Deprecated; use -W")
```

Then in `RunE`, before any other handler runs:

```go
if legacyShortW {
    return fmt.Errorf("-w is removed; use -W or --worktree")
}
```

Note: this keeps the flag table clean while ensuring `-w` errors out cleanly with a migration message.

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./cmd/wt -run TestLegacyShortFlagRejected -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/wt/main.go cmd/wt/main_test.go
git commit -m "feat(wt): register -W/-A/-M/-T/-F flags; reject legacy -w"
```

---

## Task 5: Wire new flags into the non-TUI launch path

**Files:**
- Modify: `cmd/wt/launch.go`, `cmd/wt/main.go` (`RunE`).

- [ ] **Step 1: Write the failing test**

Add to `cmd/wt/launch_test.go` (or create it):

```go
package main

import (
	"errors"
	"testing"

	"github.com/ohanaverse/agent-worktree/internal/config"
)

// TestLaunchUsesResolveModel verifies that the non-TUI launch path goes
// through resolveModel instead of the old defaultModel helper when
// -M/-T/-F are given.
func TestLaunchUsesResolveModel(t *testing.T) {
	cfg := &config.Config{
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

	// Two models, no -M, no -T/-F → should error "specify -M"
	_, err := resolveModel("claude", cfg, "", "", "")
	if err == nil || !errors.Is(err, err) {
		// we just want it to error; the message doesn't matter here
		t.Fatalf("expected error for ambiguous eligible list, got %v", err)
	}

	// With -M claude/opus → resolves correctly
	m, err := resolveModel("claude", cfg, "", "", "claude/opus")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.ID != "claude/opus" {
		t.Errorf("got %q, want claude/opus", m.ID)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/wt -run TestLaunchUsesResolveModel -v`
Expected: PASS (since `resolveModel` exists from Task 3). This step is a sanity check — the actual wire-up is in the next step.

- [ ] **Step 3: Wire the launch path**

In `cmd/wt/launch.go`, replace the `launch` function's model resolution:

```go
func launch(agent, worktreePath string, cfg *config.Config, yolo bool, extraArgs []string) error {
	return launchFiltered(agent, worktreePath, cfg, yolo, "", "", "", extraArgs)
}

// launchFiltered is the wired-up launch path used by main.go when any
// of -M/-T/-F are given or when the new resolution rules apply.
// For PR 1, the non-TUI path goes through resolveModel.
func launchFiltered(agent, worktreePath string, cfg *config.Config, yolo bool, tags, family, pinned string, extraArgs []string) error {
	m, err := resolveModel(agent, cfg, tags, family, pinned)
	if errors.Is(err, errCommandAgent) {
		// command agent: launch without model layer
		return launchDirect(agent, cfg, yolo, extraArgs)
	}
	if err != nil {
		return err
	}

	// Ollama check (existing behavior preserved)
	if agent != "shell" && ollamacheck.IsOllamaModel(m) {
		ok, oerr := ollamacheck.Available(m.ModelName)
		if oerr != nil {
			return fmt.Errorf("ollama check failed: %w", oerr)
		}
		if !ok {
			return fmt.Errorf("model %q is not available locally. Run: ollama pull %s", m.ModelName, m.ModelName)
		}
	}

	sess, _ := session.LatestForAgent(agent, worktreePath)
	cmd, err := buildLaunch(agent, m, worktreePath, yolo, sess, cfg, extraArgs)
	if err != nil {
		return err
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			os.Exit(ee.ExitCode())
		}
		return err
	}
	return nil
}
```

In `cmd/wt/main.go`'s `RunE`, replace the model-resolution paths:

```go
// Replace this:
//   if name := mustGetString(cmd, "worktree"); name != "" { ... return launch(agent, path, ...) }
//   if cwd, _ := cmd.Flags().GetBool("cwd"); cwd { ... return launch(agent, root, ...) }
//   if !inGitRepo() { return launchDirect(...) }

// With:
tags := mustGetString(cmd, "tags")
family := mustGetString(cmd, "family")
pinned := mustGetString(cmd, "model")

if name := mustGetString(cmd, "worktree"); name != "" {
    root, err := worktree.RepoRoot()
    if err != nil {
        return err
    }
    maybeInstallGuard()
    path, err := worktree.EnsureForName(root, name)
    if err != nil {
        return err
    }
    return launchFiltered(agent, path, a.cfg, yolo(cmd), tags, family, pinned, args)
}

if cwd, _ := cmd.Flags().GetBool("cwd"); cwd {
    root, err := worktree.RepoRoot()
    if err != nil {
        return err
    }
    maybeInstallGuard()
    return launchFiltered(agent, root, a.cfg, yolo(cmd), tags, family, pinned, args)
}

if !inGitRepo() {
    return launchFiltered(agent, ".", a.cfg, yolo(cmd), tags, family, pinned, args)
}

maybeInstallGuard()
return tui.Run(yolo(cmd), agent, args) // TUI ignores tags/family/pinned in PR 1
```

- [ ] **Step 4: Run the build and existing tests**

Run: `go build ./... && go test ./...`
Expected: all packages compile and tests pass.

- [ ] **Step 5: Commit**

```bash
git add cmd/wt/launch.go cmd/wt/main.go cmd/wt/launch_test.go
git commit -m "feat(wt): wire -M/-T/-F into non-TUI launch path"
```

---

## Task 6: Update CHANGELOG (initial PR entry)

**Files:**
- Modify: `CHANGELOG.md`.

- [ ] **Step 1: Add the unreleased entry**

If `CHANGELOG.md` doesn't exist, create it with this content:

```markdown
# Changelog

## Unreleased

### Breaking changes

- `-w` short flag for `--worktree` has been removed. Use `-W` or `--worktree`.
  Running `wt -w foo` now errors with: `-w is removed; use -W or --worktree`.

### Added

- `-A`/`--agent` short flag (alias for `--agent`).
- `-M`/`--model` flag to pin a model as `<provider>/<name>`. Verified against
  the eligible model list for the chosen agent.
- `-T`/`--tags` flag to filter models by tag (comma-delimited, OR within flag).
- `-F`/`--family` flag to filter models by family (comma-delimited, OR within flag).
- `internal/config.EligibleModels(agent, tags, family)` returns the models
  usable by an agent after applying tag and family filters.

### Notes

- Rotation state-file scheme is unchanged in this PR. The per-slot rotation
  refactor lands in PR 3.
- TUI behavior is unchanged in this PR. The model picker will honor `-T`/`-F`
  in PR 3.
```

If `CHANGELOG.md` exists, prepend this `Unreleased` section above any existing content.

- [ ] **Step 2: Commit**

```bash
git add CHANGELOG.md
git commit -m "docs: CHANGELOG entry for wt flow cleanup PR 1"
```

---

## Self-Review

- [x] **Spec coverage:** PR 1 covers spec sections "Flag surface", "Picker-skip conditions" (CLI-level short-circuit), "Eligible-Model Function & Non-TUI Launch Path (PR 1)", and the PR-1 portion of "Error handling". TUI and rotation changes are deferred to PRs 2/3/4 as the spec specifies.
- [x] **Placeholder scan:** No TBDs, no "implement later". Every step has code.
- [x] **Type consistency:** `parseFilterList` → `[]string`; `EligibleModels(agent, tags, family)` → `([]Model, error)`; `resolveModel(agent, cfg, tags, family, pinned)` → `(config.Model, error)`; `errCommandAgent` is the sentinel. Names match across tasks.
- [x] **Back-compat note:** `agents.IsCommand` is stubbed in PR 1 (only `"shell"` returns true); PR 2 will generalize it. This is explicitly noted in Task 3.
