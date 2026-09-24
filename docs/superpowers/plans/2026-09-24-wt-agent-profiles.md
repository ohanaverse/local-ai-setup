# wt Agent-Model Profiles (non-TUI) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give `wt` a profile layer that automatically overlays local-model
optimization tweaks (env vars, CLI args, a scoped config file, or a binary
wrapper) onto an agent launch, resolved from the same agent × model
location/provider/id that `wt` already computes — for the non-TUI launch
path (`wt -A <agent> -M <model>`, `wt --cwd -A <agent>`, etc.), plus the
`wt profile` CLI and driver capability declarations. The TUI launch path
(`internal/tui`) is an explicit follow-up plan, not covered here (see
"Non-goals").

**Architecture:** A new `internal/profiles` package owns the data model
(`profiles.toml`), tier matching + per-field merge, mechanism validation,
and application (env/args/wrapper mutate the already-built `*exec.Cmd`
in place; `config_content` writes a scoped file with snapshot/restore, or
merges into an agent's existing inline env-JSON). `internal/agents`'
four Phase-1 drivers (claude, pi, codex, opencode) each declare which
mechanisms they accept. `cmd/wt/launch.go`'s `runAgentCmd` — the single
choke point every non-TUI launch already funnels through — resolves,
TTY-confirms, applies, and restores around the existing `cmd.Run()` call,
with zero changes to `agents.BuildLaunchCmd`'s signature or behavior.

**Tech Stack:** Go 1.26 (wt module), `github.com/BurntSushi/toml` (matches
`internal/config`'s existing TOML handling), stdlib `os/exec`,
`encoding/json`.

**Spec:** `docs/superpowers/specs/2026-09-24-wt-agent-profiles-design.md`

## Non-goals (this plan)

- **TUI wiring.** `internal/tui/app.go`'s launch flow (`phaseResume`,
  `phaseReplaceConfirm`, etc.) needs a new phase for the same confirm
  prompt; that requires reading the rest of its ~1257-line state machine
  first and is a separate follow-up plan once this one lands.
- **No shipped default `profiles.toml` content.** Task 9 writes the four
  Phase-1 example profiles to a new docs page as copy-paste TOML — nothing
  writes to the user's actual `~/.config/agent-wt/profiles.toml` (a
  per-machine file, not part of this repo).
- **No `copilot`/`shell` support** — neither gets a `ProfileMechanisms()`
  method, so a hand-written profile naming either agent fails
  `profiles.Validate` at startup.

## Global Constraints

- `profiles.toml` lives at `internal/config.Dir()/profiles.toml`
  (`~/.config/agent-wt/profiles.toml`), parsed with `github.com/BurntSushi/toml`
  — the same library and directory convention `internal/config` already uses.
- Merge precedence across tiers is **location → provider → model**, later
  tier wins on a same-named `Env`/`ConfigContent` key; `Args` and `Wrapper`
  are whole-field replacement (the most specific tier that sets a non-empty
  value for that field replaces, not appends to, a less-specific tier's
  same field) — see spec §3.
- The two template placeholders are `{{model_name}}` (any string value in
  `Env`/`Args`/`ConfigContent`, substituted with `config.Model.ModelName`)
  and `{{args}}` (the `Wrapper.ArgsTemplate`-only splice point) — no other
  templating in Phase 1.
- Backups for `config_content` writes live at
  `internal/config.Dir()/profile-backups/<sha256(target-path)>.{present,absent}`
  — never a sibling file next to the target, so nothing lands in a repo or
  worktree.
- The confirm prompt defaults to **yes** on bare Enter and is skipped
  entirely (profile applies silently) when `/dev/tty` cannot be opened —
  matches the "TTY only, default toward the common case" decision in the
  spec, not the destructive-action default-no pattern `wt stop` uses.
- A malformed `profiles.toml`, a `Validate` failure, or any profile
  resolution/application error must degrade to a stderr warning and a
  normal (unprofiled) launch — **never** block or fail an agent launch.
  (Exception: a genuinely missing wrapper binary, e.g. `little-coder` not
  on `$PATH`, is a real launch error the same way a missing agent binary
  is — the user asked for that tool and it isn't there.)

## Review Focus

- **Malformed/unparseable `profiles.toml`** must not break a normal
  `wt -A claude -M ...` launch — a user typo in a TOML file they hand-edit
  should never brick the launcher. (Task 7's tests)
- **wt killed mid-session (`kill -9`) after writing a profile's config file**
  leaves an orphaned backup; the *next* launch that would write the same
  target path must restore the original content first, before writing new
  content — otherwise a hand-edited `.claude/settings.local.json` is lost
  forever. (Task 5's tests)
- **Non-TTY launches (scripts, `wt smoke`, CI, redirected stdin)** must
  apply a matching profile silently with no prompt and no hang waiting on
  input that will never arrive. (Task 7's tests)
- **The global `enabled = false` switch** must fully short-circuit
  resolution — no file write, no wrapper substitution, no prompt — even
  when a profile would otherwise match; `wt profile off` must be a true
  kill switch. (Task 7's tests)
- **Env/Args precedence:** a profile's `Env` value for a key the driver
  itself already set (e.g. hypothetically overriding `ANTHROPIC_BASE_URL`)
  must win, matching Go's own last-value-wins `exec.Cmd.Env` semantics —
  verified explicitly, not just assumed. (Task 4's tests)

---

## File Structure

New package `wt/internal/profiles/`:

| File | Responsibility |
|---|---|
| `profiles.go` | `Profile`, `WrapperSpec`, `Store`, `Mechanism`, `ProfileCapable`; `Path()`, `Load()` |
| `resolve.go` | `ResolvedProfile`; `Resolve()` (tier filter + per-field merge + `{{model_name}}` substitution) |
| `validate.go` | `Validate()` (capability gate) |
| `apply.go` | `ApplyToCmd()` (env/args/wrapper mutate `*exec.Cmd`) |
| `configcontent.go` | `ConfigFileTarget()`, `ApplyConfigContent()`, snapshot/restore/self-heal |

Modified:

| File | Change |
|---|---|
| `wt/internal/agents/claude.go` | `ProfileMechanisms() []profiles.Mechanism` |
| `wt/internal/agents/pi.go` | `ProfileMechanisms() []profiles.Mechanism` |
| `wt/internal/agents/codex.go` | `ProfileMechanisms() []profiles.Mechanism` |
| `wt/internal/agents/opencode.go` | `ProfileMechanisms() []profiles.Mechanism` |
| `wt/cmd/wt/app.go` | `app` gains `profiles profiles.Store` + `profilesErr error`, loaded+validated in `newApp()` |
| `wt/cmd/wt/launch.go` | `runAgentCmd` resolves/confirms/applies/restores a profile around `cmd.Run()`; new `confirmProfile`/`loadProfileStore` seams |
| `wt/cmd/wt/profile.go` (new) | `wt profile list\|show\|status\|on\|off` |
| `wt/cmd/wt/main.go` | register `profileCmd(a)` |
| `wt/docs/wt-agents/profiles.md` (new) | Phase-1 example profiles + mechanism reference |
| `wt/CLAUDE.md` | one-line pointer to the new doc |

---

## Task 1: `internal/profiles` data model + `Load()`

**Files:**
- Create: `wt/internal/profiles/profiles.go`
- Test: `wt/internal/profiles/profiles_test.go`

**Interfaces:**
- Produces: `type Mechanism string` with consts `MechanismEnv`, `MechanismArgs`,
  `MechanismConfigFile`, `MechanismWrapper`; `type ProfileCapable interface { ProfileMechanisms() []Mechanism }`;
  `type WrapperSpec struct { Binary string; ArgsTemplate []string }`;
  `type Profile struct { Agent, Match, Location, Provider, Model string; Env map[string]string; Args []string; ConfigContent map[string]any; Wrapper *WrapperSpec }`;
  `type Store struct { Enabled bool; Profiles []Profile }`; `func Path() string`;
  `func Load() (Store, error)`.

- [ ] **Step 1: Write the failing tests**

```go
// wt/internal/profiles/profiles_test.go
package profiles

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadMissingFileReturnsEnabledEmptyStore verifies that a fresh
// install (no profiles.toml yet) is not an error and defaults to
// enabled=true with no profiles — matching config.Load's "empty Config
// if config.toml does not exist yet" convention, so `wt` never fails to
// launch just because no one has authored a profile yet.
func TestLoadMissingFileReturnsEnabledEmptyStore(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	store, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if !store.Enabled {
		t.Errorf("store.Enabled = false, want true (default)")
	}
	if len(store.Profiles) != 0 {
		t.Errorf("store.Profiles = %v, want empty", store.Profiles)
	}
}

// TestLoadParsesProfilesAndEnabledFlag verifies the on-disk TOML shape
// from the design spec round-trips into Store correctly, including a
// profile using every field (env, args, config_content, wrapper) so a
// schema typo in any one field is caught here rather than later.
func TestLoadParsesProfilesAndEnabledFlag(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	body := `
enabled = false

[[profiles]]
agent = "claude"
match = "location"
location = "local"
env = { CLAUDE_CODE_ATTRIBUTION_HEADER = "0" }

[[profiles]]
agent = "pi"
match = "location"
location = "local"
wrapper = { binary = "little-coder", args_template = ["--pi-args", "{{args}}"] }

[[profiles]]
agent = "codex"
match = "provider"
provider = "ollama"
args = ["-c", "model_reasoning_effort=\"low\""]
`
	if err := os.MkdirAll(filepath.Join(dir, "agent-wt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if store.Enabled {
		t.Errorf("store.Enabled = true, want false (explicit in file)")
	}
	if len(store.Profiles) != 3 {
		t.Fatalf("len(store.Profiles) = %d, want 3", len(store.Profiles))
	}
	if store.Profiles[0].Env["CLAUDE_CODE_ATTRIBUTION_HEADER"] != "0" {
		t.Errorf("profile[0].Env = %v, want CLAUDE_CODE_ATTRIBUTION_HEADER=0", store.Profiles[0].Env)
	}
	if store.Profiles[1].Wrapper == nil || store.Profiles[1].Wrapper.Binary != "little-coder" {
		t.Errorf("profile[1].Wrapper = %+v, want little-coder", store.Profiles[1].Wrapper)
	}
	if len(store.Profiles[2].Args) != 2 {
		t.Errorf("profile[2].Args = %v, want 2 elements", store.Profiles[2].Args)
	}
}

// TestLoadMalformedTOMLReturnsError verifies a syntax error surfaces as a
// real error rather than a silently empty store — callers (Task 7) decide
// to degrade to "profiles disabled for this launch" with a warning, but
// Load itself must not swallow the problem.
func TestLoadMalformedTOMLReturnsError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "agent-wt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte("not [ valid toml"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want a parse error")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd wt && go test ./internal/profiles/... -run TestLoad -v`
Expected: FAIL — package `profiles` (and `Load`/`Path`/`Store`) does not exist yet.

- [ ] **Step 3: Write the implementation**

```go
// wt/internal/profiles/profiles.go
package profiles

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// Mechanism names one way a profile can change a launch. Drivers declare
// which ones they accept via ProfileCapable; Validate rejects a profile
// using one its agent does not declare.
type Mechanism string

const (
	MechanismEnv        Mechanism = "env"
	MechanismArgs        Mechanism = "args"
	MechanismConfigFile Mechanism = "config_file"
	MechanismWrapper    Mechanism = "wrapper"
)

// ProfileCapable is implemented by agents.Driver values that accept
// profile overlays. Defined here — not in internal/agents — so this
// package never imports internal/agents; the dependency runs the other
// way (a driver imports profiles for the Mechanism type).
type ProfileCapable interface {
	ProfileMechanisms() []Mechanism
}

// WrapperSpec replaces the launched binary with Binary, substituting the
// original argv into ArgsTemplate wherever the literal element "{{args}}"
// appears (see apply.go).
type WrapperSpec struct {
	Binary       string   `toml:"binary"`
	ArgsTemplate []string `toml:"args_template"`
}

// Profile is one entry in profiles.toml. Match selects which of
// Location/Provider/Model is the tier condition; the other two are
// ignored. See the design spec §1 for the on-disk shape.
type Profile struct {
	Agent         string            `toml:"agent"`
	Match         string            `toml:"match"` // "location" | "provider" | "model"
	Location      string            `toml:"location,omitempty"`
	Provider      string            `toml:"provider,omitempty"`
	Model         string            `toml:"model,omitempty"`
	Env           map[string]string `toml:"env,omitempty"`
	Args          []string          `toml:"args,omitempty"`
	ConfigContent map[string]any    `toml:"config_content,omitempty"`
	Wrapper       *WrapperSpec      `toml:"wrapper,omitempty"`
}

// fileSchema is the raw on-disk shape; Enabled is a pointer so an absent
// key defaults to true (profiles on) while an explicit `enabled = false`
// is distinguishable from "not set".
type fileSchema struct {
	Enabled  *bool     `toml:"enabled"`
	Profiles []Profile `toml:"profiles"`
}

// Store is the in-memory result of Load.
type Store struct {
	Enabled  bool
	Profiles []Profile
}

// Path returns the profiles.toml location, alongside config.Dir()'s
// config.toml.
func Path() string { return filepath.Join(config.Dir(), "profiles.toml") }

// Load reads profiles.toml. A missing file is not an error: it returns
// Store{Enabled: true} (profiles on, none defined yet), matching
// config.Load's "empty Config if config.toml does not exist" convention.
func Load() (Store, error) {
	data, err := os.ReadFile(Path())
	if os.IsNotExist(err) {
		return Store{Enabled: true}, nil
	}
	if err != nil {
		return Store{}, err
	}
	var fs fileSchema
	if _, err := toml.Decode(string(data), &fs); err != nil {
		return Store{}, fmt.Errorf("parse profiles.toml: %w", err)
	}
	enabled := true
	if fs.Enabled != nil {
		enabled = *fs.Enabled
	}
	return Store{Enabled: enabled, Profiles: fs.Profiles}, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd wt && go test ./internal/profiles/... -run TestLoad -v`
Expected: PASS (3 tests)

- [ ] **Step 5: Commit**

```bash
cd wt && git add internal/profiles/profiles.go internal/profiles/profiles_test.go
git commit -m "feat(wt): add profiles.toml data model and Load()"
```

---

## Task 2: `Resolve()` — tier matching + per-field merge

**Files:**
- Create: `wt/internal/profiles/resolve.go`
- Test: `wt/internal/profiles/resolve_test.go`

**Interfaces:**
- Consumes: `Store`, `Profile`, `Mechanism` (Task 1); `config.Model`, `config.Config`,
  `config.Location`, `config.LocationLocal`/`LocationCloud`, `(*config.Config).ResolveLocation(m) (config.Location, error)` (existing).
- Produces: `type ResolvedProfile struct { Env map[string]string; ExtraArgs []string; ConfigContent map[string]any; Wrapper *WrapperSpec; Sources []string }`
  with method `(r ResolvedProfile) Empty() bool`; `func Resolve(store Store, agent string, cfg *config.Config, m config.Model) ResolvedProfile`.

- [ ] **Step 1: Write the failing tests**

```go
// wt/internal/profiles/resolve_test.go
package profiles

import (
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

func testCfg() *config.Config {
	return &config.Config{
		Providers: []config.Provider{
			{ID: "ollama", Location: config.LocationLocal},
			{ID: "claude", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "native"}},
		},
	}
}

// TestResolveNoMatchReturnsEmpty verifies an agent/model with no matching
// profile resolves to an empty ResolvedProfile, so callers can skip the
// confirm prompt and every application step entirely on the common
// (no profile authored) path.
func TestResolveNoMatchReturnsEmpty(t *testing.T) {
	store := Store{Enabled: true, Profiles: []Profile{
		{Agent: "claude", Match: "location", Location: "local", Env: map[string]string{"X": "1"}},
	}}
	m := config.Model{ID: "claude/opus", ProviderID: "claude", ModelName: "opus"}
	rp := Resolve(store, "claude", testCfg(), m)
	if !rp.Empty() {
		t.Errorf("Resolve() = %+v, want Empty() (cloud model, local-only profile)", rp)
	}
}

// TestResolveLocationTierMatches verifies a location-tier profile applies
// to a local model for the matching agent, and substitutes {{model_name}}
// in Env values — the mechanism the claude Phase-1 profile depends on.
func TestResolveLocationTierMatches(t *testing.T) {
	store := Store{Enabled: true, Profiles: []Profile{
		{Agent: "claude", Match: "location", Location: "local", Env: map[string]string{
			"ANTHROPIC_DEFAULT_SONNET_MODEL": "{{model_name}}",
		}},
	}}
	m := config.Model{ID: "ollama/qwen3.8:27b-mlx", ProviderID: "ollama", ModelName: "qwen3.8:27b-mlx"}
	rp := Resolve(store, "claude", testCfg(), m)
	if rp.Env["ANTHROPIC_DEFAULT_SONNET_MODEL"] != "qwen3.8:27b-mlx" {
		t.Errorf("Env = %v, want substituted model name", rp.Env)
	}
}

// TestResolveModelTierOverridesLocationTierSameKey verifies the
// most-specific-wins merge rule for a colliding Env key: a model-tier
// profile's value for a key must win over a location-tier profile's value
// for the same key, while a location-tier key the model tier never
// touches survives untouched.
func TestResolveModelTierOverridesLocationTierSameKey(t *testing.T) {
	store := Store{Enabled: true, Profiles: []Profile{
		{Agent: "claude", Match: "location", Location: "local", Env: map[string]string{
			"MAX_THINKING_TOKENS": "4096", "CLAUDE_CODE_ATTRIBUTION_HEADER": "0",
		}},
		{Agent: "claude", Match: "model", Model: "ollama/qwen3.8:27b-mlx", Env: map[string]string{
			"MAX_THINKING_TOKENS": "8192",
		}},
	}}
	m := config.Model{ID: "ollama/qwen3.8:27b-mlx", ProviderID: "ollama", ModelName: "qwen3.8:27b-mlx"}
	rp := Resolve(store, "claude", testCfg(), m)
	if rp.Env["MAX_THINKING_TOKENS"] != "8192" {
		t.Errorf("MAX_THINKING_TOKENS = %q, want model-tier value 8192", rp.Env["MAX_THINKING_TOKENS"])
	}
	if rp.Env["CLAUDE_CODE_ATTRIBUTION_HEADER"] != "0" {
		t.Errorf("CLAUDE_CODE_ATTRIBUTION_HEADER = %q, want untouched location-tier value 0", rp.Env["CLAUDE_CODE_ATTRIBUTION_HEADER"])
	}
}

// TestResolveArgsIsWholeFieldReplace verifies Args (a list, not a keyed
// map) follows whole-field replacement: a more specific tier's Args fully
// replaces a less specific tier's Args rather than concatenating, per the
// design spec's merge rule.
func TestResolveArgsIsWholeFieldReplace(t *testing.T) {
	store := Store{Enabled: true, Profiles: []Profile{
		{Agent: "codex", Match: "location", Location: "local", Args: []string{"-c", "a=1"}},
		{Agent: "codex", Match: "provider", Provider: "ollama", Args: []string{"-c", "b=2"}},
	}}
	m := config.Model{ID: "ollama/qwen3.8:27b-mlx", ProviderID: "ollama", ModelName: "qwen3.8:27b-mlx"}
	rp := Resolve(store, "codex", testCfg(), m)
	if len(rp.ExtraArgs) != 2 || rp.ExtraArgs[1] != "b=2" {
		t.Errorf("ExtraArgs = %v, want [-c b=2] (provider tier replaces location tier)", rp.ExtraArgs)
	}
}

// TestResolveProviderTierUsesModelProviderIDFallback verifies provider-tier
// matching falls back to the segment before "/" in Model.ID when
// Model.ProviderID is empty, mirroring config.ResolveRoute's own fallback
// — a discovered model (registry gap) must still match a provider-tier
// profile.
func TestResolveProviderTierUsesModelProviderIDFallback(t *testing.T) {
	store := Store{Enabled: true, Profiles: []Profile{
		{Agent: "codex", Match: "provider", Provider: "ollama", Args: []string{"-c", "x=1"}},
	}}
	m := config.Model{ID: "ollama/some-discovered-model", ModelName: "some-discovered-model"} // ProviderID intentionally empty
	rp := Resolve(store, "codex", testCfg(), m)
	if len(rp.ExtraArgs) == 0 {
		t.Errorf("ExtraArgs empty, want provider-tier match via ID fallback")
	}
}

// TestResolveDisabledAgentSkipsOtherAgentsProfiles verifies a profile for
// a different agent never leaks into another agent's resolution.
func TestResolveDisabledAgentSkipsOtherAgentsProfiles(t *testing.T) {
	store := Store{Enabled: true, Profiles: []Profile{
		{Agent: "pi", Match: "location", Location: "local", Wrapper: &WrapperSpec{Binary: "little-coder", ArgsTemplate: []string{"{{args}}"}}},
	}}
	m := config.Model{ID: "ollama/qwen3.8:27b-mlx", ProviderID: "ollama", ModelName: "qwen3.8:27b-mlx"}
	rp := Resolve(store, "claude", testCfg(), m)
	if !rp.Empty() {
		t.Errorf("Resolve() for claude = %+v, want Empty() (profile is for pi)", rp)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd wt && go test ./internal/profiles/... -run TestResolve -v`
Expected: FAIL — `Resolve`/`ResolvedProfile` undefined.

- [ ] **Step 3: Write the implementation**

```go
// wt/internal/profiles/resolve.go
package profiles

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// ResolvedProfile is the merged result of every profile matching one
// agent/model launch, ready to apply.
type ResolvedProfile struct {
	Env           map[string]string
	ExtraArgs     []string
	ConfigContent map[string]any
	Wrapper       *WrapperSpec
	Sources       []string // "tier/value" strings naming every profile that contributed, for the confirm prompt
}

// Empty reports whether nothing would change about the launch.
func (r ResolvedProfile) Empty() bool {
	return len(r.Env) == 0 && len(r.ExtraArgs) == 0 && len(r.ConfigContent) == 0 && r.Wrapper == nil
}

var tierRank = map[string]int{"location": 0, "provider": 1, "model": 2}

// Resolve filters store.Profiles to those matching agent and m's resolved
// route conditions, then merges them in tier order (location, provider,
// model) so a more specific tier overwrites a less specific tier's
// same-named Env/ConfigContent key; Args and Wrapper are whole-field
// replacement (see design spec §3). An unresolvable location (registry
// gap) is treated conservatively as non-local, mirroring
// Config.IsExposed's own fail-closed treatment.
func Resolve(store Store, agent string, cfg *config.Config, m config.Model) ResolvedProfile {
	type tiered struct {
		rank int
		p    Profile
	}
	var matches []tiered
	for _, p := range store.Profiles {
		if p.Agent != agent {
			continue
		}
		rank, ok := tierRank[p.Match]
		if !ok || !matchesTier(p, cfg, m) {
			continue
		}
		matches = append(matches, tiered{rank: rank, p: p})
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].rank < matches[j].rank })

	var out ResolvedProfile
	for _, t := range matches {
		p := t.p
		out.Sources = append(out.Sources, fmt.Sprintf("%s=%s", p.Match, matchValue(p)))
		for k, v := range p.Env {
			if out.Env == nil {
				out.Env = map[string]string{}
			}
			out.Env[k] = substitute(v, m)
		}
		for k, v := range p.ConfigContent {
			if out.ConfigContent == nil {
				out.ConfigContent = map[string]any{}
			}
			out.ConfigContent[k] = substituteAny(v, m)
		}
		if len(p.Args) > 0 {
			out.ExtraArgs = substituteAll(p.Args, m)
		}
		if p.Wrapper != nil {
			out.Wrapper = &WrapperSpec{Binary: p.Wrapper.Binary, ArgsTemplate: substituteAll(p.Wrapper.ArgsTemplate, m)}
		}
	}
	return out
}

func matchesTier(p Profile, cfg *config.Config, m config.Model) bool {
	switch p.Match {
	case "location":
		loc, err := cfg.ResolveLocation(m)
		return err == nil && string(loc) == p.Location
	case "provider":
		return providerID(m) == p.Provider
	case "model":
		return m.ID == p.Model
	default:
		return false
	}
}

// providerID mirrors config.ResolveRoute's own provider-id fallback:
// m.ProviderID when set, else the segment before the first "/" in m.ID.
func providerID(m config.Model) string {
	if m.ProviderID != "" {
		return m.ProviderID
	}
	if i := strings.Index(m.ID, "/"); i >= 0 {
		return m.ID[:i]
	}
	return ""
}

func matchValue(p Profile) string {
	switch p.Match {
	case "location":
		return p.Location
	case "provider":
		return p.Provider
	default:
		return p.Model
	}
}

func substitute(s string, m config.Model) string {
	return strings.ReplaceAll(s, "{{model_name}}", m.ModelName)
}

func substituteAll(ss []string, m config.Model) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = substitute(s, m)
	}
	return out
}

func substituteAny(v any, m config.Model) any {
	switch val := v.(type) {
	case string:
		return substitute(val, m)
	case map[string]any:
		out := make(map[string]any, len(val))
		for k, vv := range val {
			out[k] = substituteAny(vv, m)
		}
		return out
	case []any:
		out := make([]any, len(val))
		for i, vv := range val {
			out[i] = substituteAny(vv, m)
		}
		return out
	default:
		return v
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd wt && go test ./internal/profiles/... -run TestResolve -v`
Expected: PASS (6 tests)

- [ ] **Step 5: Commit**

```bash
cd wt && git add internal/profiles/resolve.go internal/profiles/resolve_test.go
git commit -m "feat(wt): add profile tier matching and per-field merge"
```

---

## Task 3: `Validate()` — mechanism capability gate

**Files:**
- Create: `wt/internal/profiles/validate.go`
- Test: `wt/internal/profiles/validate_test.go`

**Interfaces:**
- Consumes: `Store`, `Profile`, `Mechanism` (Task 1).
- Produces: `func Validate(store Store, mechanismsFor func(agent string) []Mechanism) error`.

- [ ] **Step 1: Write the failing tests**

```go
// wt/internal/profiles/validate_test.go
package profiles

import "testing"

// TestValidateRejectsUnsupportedMechanism verifies a profile using a
// mechanism its agent does not declare (e.g. config_content for an agent
// that only declares wrapper) fails loudly at validation time, naming the
// offending profile — so a bad hand-edit to profiles.toml is caught at
// startup, not silently misapplied (or silently dropped) at launch time.
func TestValidateRejectsUnsupportedMechanism(t *testing.T) {
	store := Store{Profiles: []Profile{
		{Agent: "pi", Match: "location", Location: "local", ConfigContent: map[string]any{"x": "1"}},
	}}
	mechs := func(agent string) []Mechanism {
		if agent == "pi" {
			return []Mechanism{MechanismWrapper}
		}
		return nil
	}
	err := Validate(store, mechs)
	if err == nil {
		t.Fatal("Validate() = nil, want an error naming the config_content/pi mismatch")
	}
}

// TestValidateAcceptsDeclaredMechanisms verifies a profile using only
// mechanisms its agent declares passes cleanly, so a correctly authored
// profiles.toml never gets a spurious startup warning.
func TestValidateAcceptsDeclaredMechanisms(t *testing.T) {
	store := Store{Profiles: []Profile{
		{Agent: "claude", Match: "location", Location: "local", Env: map[string]string{"X": "1"}},
	}}
	mechs := func(agent string) []Mechanism { return []Mechanism{MechanismEnv, MechanismConfigFile} }
	if err := Validate(store, mechs); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

// TestValidateUnknownAgentRejectsAnyMechanism verifies a profile naming an
// agent with no declared mechanisms at all (e.g. copilot, or a typo)
// fails the same way an explicitly-unsupported mechanism does.
func TestValidateUnknownAgentRejectsAnyMechanism(t *testing.T) {
	store := Store{Profiles: []Profile{
		{Agent: "copilot", Match: "location", Location: "local", Env: map[string]string{"X": "1"}},
	}}
	mechs := func(agent string) []Mechanism { return nil }
	if err := Validate(store, mechs); err == nil {
		t.Fatal("Validate() = nil, want an error for copilot (no declared mechanisms)")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd wt && go test ./internal/profiles/... -run TestValidate -v`
Expected: FAIL — `Validate` undefined.

- [ ] **Step 3: Write the implementation**

```go
// wt/internal/profiles/validate.go
package profiles

import (
	"errors"
	"fmt"
	"strings"
)

// Validate checks that every profile's mechanisms are ones its agent
// declares support for via mechanismsFor (typically backed by
// agents.ByName(agent).(profiles.ProfileCapable)). It returns one
// combined error naming every offending profile, or nil if all profiles
// pass.
func Validate(store Store, mechanismsFor func(agent string) []Mechanism) error {
	var problems []string
	for i, p := range store.Profiles {
		allowed := map[Mechanism]bool{}
		for _, mech := range mechanismsFor(p.Agent) {
			allowed[mech] = true
		}
		for _, mech := range usedMechanisms(p) {
			if !allowed[mech] {
				problems = append(problems, fmt.Sprintf(
					"profiles.toml[%d] (agent=%s, match=%s): agent does not support mechanism %q",
					i, p.Agent, p.Match, mech))
			}
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return errors.New(strings.Join(problems, "; "))
}

func usedMechanisms(p Profile) []Mechanism {
	var out []Mechanism
	if len(p.Env) > 0 {
		out = append(out, MechanismEnv)
	}
	if len(p.Args) > 0 {
		out = append(out, MechanismArgs)
	}
	if len(p.ConfigContent) > 0 {
		out = append(out, MechanismConfigFile)
	}
	if p.Wrapper != nil {
		out = append(out, MechanismWrapper)
	}
	return out
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd wt && go test ./internal/profiles/... -run TestValidate -v`
Expected: PASS (3 tests)

- [ ] **Step 5: Commit**

```bash
cd wt && git add internal/profiles/validate.go internal/profiles/validate_test.go
git commit -m "feat(wt): validate profile mechanisms against agent capabilities"
```

---

## Task 4: `ApplyToCmd()` — env/args/wrapper mutation

**Files:**
- Create: `wt/internal/profiles/apply.go`
- Test: `wt/internal/profiles/apply_test.go`

**Interfaces:**
- Consumes: `ResolvedProfile`, `WrapperSpec` (Task 2).
- Produces: `func ApplyToCmd(cmd *exec.Cmd, rp ResolvedProfile) error`.

- [ ] **Step 1: Write the failing tests**

```go
// wt/internal/profiles/apply_test.go
package profiles

import (
	"os/exec"
	"strings"
	"testing"
)

// TestApplyToCmdEnvWinsOnCollision verifies a profile's Env value for a
// key the command already carries wins — exec.Cmd.Env is documented to
// use the LAST value for a duplicate key, so ApplyToCmd must APPEND
// (never prepend or dedupe) so profile values always land after
// driver-set ones.
func TestApplyToCmdEnvWinsOnCollision(t *testing.T) {
	cmd := exec.Command("true")
	cmd.Env = []string{"FOO=driver-value"}
	rp := ResolvedProfile{Env: map[string]string{"FOO": "profile-value"}}
	if err := ApplyToCmd(cmd, rp); err != nil {
		t.Fatalf("ApplyToCmd() error = %v", err)
	}
	if cmd.Env[len(cmd.Env)-1] != "FOO=profile-value" {
		t.Errorf("cmd.Env = %v, want profile value appended last", cmd.Env)
	}
}

// TestApplyToCmdExtraArgsAppended verifies ExtraArgs land at the end of
// cmd.Args, after whatever BuildLaunchCmd already assembled (driver args,
// user passthrough, resume flag) — see the plan's ordering note in Task 7.
func TestApplyToCmdExtraArgsAppended(t *testing.T) {
	cmd := exec.Command("codex", "--model", "x")
	rp := ResolvedProfile{ExtraArgs: []string{"-c", "model_reasoning_effort=\"low\""}}
	if err := ApplyToCmd(cmd, rp); err != nil {
		t.Fatalf("ApplyToCmd() error = %v", err)
	}
	want := []string{"codex", "--model", "x", "-c", "model_reasoning_effort=\"low\""}
	if strings.Join(cmd.Args, "|") != strings.Join(want, "|") {
		t.Errorf("cmd.Args = %v, want %v", cmd.Args, want)
	}
}

// TestApplyToCmdWrapperReplacesBinaryAndSplicesArgs verifies a Wrapper
// swaps cmd.Path/Args[0] for the wrapper binary and splices the ORIGINAL
// argv (everything after the old argv[0]) into the "{{args}}" template
// slot, dropping the old binary name — the little-coder-wraps-pi case.
func TestApplyToCmdWrapperReplacesBinaryAndSplicesArgs(t *testing.T) {
	cmd := exec.Command("pi", "--model", "ollama/qwen3.8:27b-mlx")
	rp := ResolvedProfile{Wrapper: &WrapperSpec{
		Binary:       "true", // a binary guaranteed to exist on PATH for the test
		ArgsTemplate: []string{"--pi-args", "{{args}}"},
	}}
	if err := ApplyToCmd(cmd, rp); err != nil {
		t.Fatalf("ApplyToCmd() error = %v", err)
	}
	if !strings.HasSuffix(cmd.Path, "true") {
		t.Errorf("cmd.Path = %q, want the resolved `true` binary", cmd.Path)
	}
	want := []string{"--pi-args", "--model", "ollama/qwen3.8:27b-mlx"}
	got := cmd.Args[1:]
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("cmd.Args[1:] = %v, want %v", got, want)
	}
}

// TestApplyToCmdWrapperMissingBinaryErrors verifies a wrapper binary that
// isn't on PATH is a real launch error (not a silent fallback to
// unwrapped) — the user asked for little-coder and it must actually run.
func TestApplyToCmdWrapperMissingBinaryErrors(t *testing.T) {
	cmd := exec.Command("pi")
	rp := ResolvedProfile{Wrapper: &WrapperSpec{Binary: "definitely-not-a-real-binary-xyz"}}
	if err := ApplyToCmd(cmd, rp); err == nil {
		t.Fatal("ApplyToCmd() error = nil, want an error for a missing wrapper binary")
	}
}

// TestApplyToCmdEmptyIsNoop verifies an empty ResolvedProfile changes
// nothing, so calling ApplyToCmd unconditionally (Task 7 does) is always
// safe on the "no profile matched" path.
func TestApplyToCmdEmptyIsNoop(t *testing.T) {
	cmd := exec.Command("pi", "--model", "x")
	before := append([]string{}, cmd.Args...)
	if err := ApplyToCmd(cmd, ResolvedProfile{}); err != nil {
		t.Fatalf("ApplyToCmd() error = %v", err)
	}
	if strings.Join(cmd.Args, "|") != strings.Join(before, "|") {
		t.Errorf("cmd.Args changed on empty ResolvedProfile: %v -> %v", before, cmd.Args)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd wt && go test ./internal/profiles/... -run TestApplyToCmd -v`
Expected: FAIL — `ApplyToCmd` undefined.

- [ ] **Step 3: Write the implementation**

```go
// wt/internal/profiles/apply.go
package profiles

import (
	"fmt"
	"os/exec"
)

// ApplyToCmd mutates cmd in place per rp: Env is appended (so it wins on
// a duplicate key, matching exec.Cmd.Env's documented last-value-wins
// behavior), ExtraArgs are appended after everything cmd.Args already
// carries, and a Wrapper — applied last — replaces cmd.Path/Args[0] with
// the wrapper binary, splicing the command's existing argv (built so far,
// including any ExtraArgs just appended) into the "{{args}}" slot in
// ArgsTemplate. A no-op ResolvedProfile changes nothing.
func ApplyToCmd(cmd *exec.Cmd, rp ResolvedProfile) error {
	if rp.Empty() {
		return nil
	}
	for k, v := range rp.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	if len(rp.ExtraArgs) > 0 {
		cmd.Args = append(cmd.Args, rp.ExtraArgs...)
	}
	if rp.Wrapper != nil {
		if err := applyWrapper(cmd, rp.Wrapper); err != nil {
			return err
		}
	}
	return nil
}

func applyWrapper(cmd *exec.Cmd, w *WrapperSpec) error {
	binPath, err := exec.LookPath(w.Binary)
	if err != nil {
		return fmt.Errorf("profile wrapper %q not installed: %w", w.Binary, err)
	}
	original := cmd.Args[1:] // drop the old argv[0] (the pre-wrap binary name)
	var newArgs []string
	for _, tok := range w.ArgsTemplate {
		if tok == "{{args}}" {
			newArgs = append(newArgs, original...)
			continue
		}
		newArgs = append(newArgs, tok)
	}
	cmd.Path = binPath
	cmd.Args = append([]string{binPath}, newArgs...)
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd wt && go test ./internal/profiles/... -run TestApplyToCmd -v`
Expected: PASS (5 tests)

- [ ] **Step 5: Commit**

```bash
cd wt && git add internal/profiles/apply.go internal/profiles/apply_test.go
git commit -m "feat(wt): apply resolved profile env/args/wrapper to a launch cmd"
```

---

## Task 5: `config_content` — per-agent target, snapshot/restore/self-heal

**Files:**
- Create: `wt/internal/profiles/configcontent.go`
- Test: `wt/internal/profiles/configcontent_test.go`

**Interfaces:**
- Consumes: `ResolvedProfile` (Task 2); `config.WriteFileAtomic` (existing).
- Produces: `func ConfigFileTarget(agent, worktreePath string) (path string, isTOML bool, ok bool)`;
  `func ApplyConfigContent(cmd *exec.Cmd, agent, worktreePath string, rp ResolvedProfile) (cleanup func() error, err error)`;
  unexported `restoreIfBackedUp(target string) (restored bool, err error)` (also exercised directly by tests for the self-heal case).

- [ ] **Step 1: Write the failing tests**

```go
// wt/internal/profiles/configcontent_test.go
package profiles

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestConfigFileTargetClaudeIsWorktreeScoped verifies claude's target is
// inside the launch's own worktree (.claude/settings.local.json), never
// the global ~/.claude/settings.json — the whole point of the
// worktree-scoped design (spec §4).
func TestConfigFileTargetClaudeIsWorktreeScoped(t *testing.T) {
	path, isTOML, ok := ConfigFileTarget("claude", "/tmp/some-worktree")
	if !ok || isTOML {
		t.Fatalf("ConfigFileTarget(claude) = (%q, %v, %v), want (path, false, true)", path, isTOML, ok)
	}
	want := filepath.Join("/tmp/some-worktree", ".claude", "settings.local.json")
	if path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
}

// TestConfigFileTargetOpenCodeHasNoFile verifies opencode reports ok=false
// — its config_content goes through OPENCODE_CONFIG_CONTENT env merging
// instead (ApplyConfigContent's opencode branch), never a file.
func TestConfigFileTargetOpenCodeHasNoFile(t *testing.T) {
	if _, _, ok := ConfigFileTarget("opencode", "/tmp/wt"); ok {
		t.Error("ConfigFileTarget(opencode) ok = true, want false (env-merged, no file)")
	}
}

// TestApplyConfigContentWritesAndBacksUpExistingFile verifies an existing
// target file's content is snapshotted before being overwritten, and that
// the returned cleanup restores it exactly — the "save, overwrite for the
// session, restore on exit" behavior the design commits to.
func TestApplyConfigContentWritesAndBacksUpExistingFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg"))
	worktree := filepath.Join(dir, "worktree")
	target := filepath.Join(worktree, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"env":{"MY_OWN_SETTING":"keep-me"}}`)
	if err := os.WriteFile(target, original, 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("claude")
	rp := ResolvedProfile{ConfigContent: map[string]any{"env": map[string]any{"MAX_THINKING_TOKENS": "4096"}}}
	cleanup, err := ApplyConfigContent(cmd, "claude", worktree, rp)
	if err != nil {
		t.Fatalf("ApplyConfigContent() error = %v", err)
	}
	written, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(written, &doc); err != nil {
		t.Fatalf("written file is not valid JSON: %v", err)
	}
	if _, has := doc["env"]; !has {
		t.Errorf("written content = %s, want an env key", written)
	}

	if err := cleanup(); err != nil {
		t.Fatalf("cleanup() error = %v", err)
	}
	restored, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != string(original) {
		t.Errorf("restored content = %s, want original %s", restored, original)
	}
}

// TestApplyConfigContentDeletesFileThatDidNotExistBefore verifies that
// when the target had no prior file, cleanup deletes it rather than
// leaving the wt-written file behind — the "restore, or delete if none
// existed" half of the design.
func TestApplyConfigContentDeletesFileThatDidNotExistBefore(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg"))
	worktree := filepath.Join(dir, "worktree")
	target := filepath.Join(worktree, ".claude", "settings.local.json")

	cmd := exec.Command("claude")
	rp := ResolvedProfile{ConfigContent: map[string]any{"env": map[string]any{"X": "1"}}}
	cleanup, err := ApplyConfigContent(cmd, "claude", worktree, rp)
	if err != nil {
		t.Fatalf("ApplyConfigContent() error = %v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("target not written: %v", err)
	}
	if err := cleanup(); err != nil {
		t.Fatalf("cleanup() error = %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("target still exists after cleanup, want deleted (stat err = %v)", err)
	}
}

// TestApplyConfigContentSelfHealsOrphanedBackup verifies that if a
// previous session's backup for this exact target was never restored
// (simulating wt being killed mid-session), the NEXT write for that same
// target restores the orphaned original content first, before writing
// new content — so a hand-edited file is never permanently lost to an
// unclean exit.
func TestApplyConfigContentSelfHealsOrphanedBackup(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg"))
	worktree := filepath.Join(dir, "worktree")
	target := filepath.Join(worktree, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	handEdited := []byte(`{"hooks":{"my-own":"thing"}}`)
	if err := os.WriteFile(target, handEdited, 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("claude")
	rp := ResolvedProfile{ConfigContent: map[string]any{"env": map[string]any{"X": "1"}}}
	// First "session": write profile content, but DO NOT call cleanup —
	// simulates `kill -9` before the restore could run.
	if _, err := ApplyConfigContent(cmd, "claude", worktree, rp); err != nil {
		t.Fatalf("first ApplyConfigContent() error = %v", err)
	}

	// Second "session" for the same target: the leftover backup from the
	// first session must be restored (self-heal) before the new write.
	restored, err := restoreIfBackedUp(target)
	if err != nil {
		t.Fatalf("restoreIfBackedUp() error = %v", err)
	}
	if !restored {
		t.Fatal("restoreIfBackedUp() restored = false, want true (orphaned backup from first session)")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(handEdited) {
		t.Errorf("self-healed content = %s, want original hand-edited content %s", got, handEdited)
	}
}

// TestApplyConfigContentOpenCodeMergesIntoEnv verifies opencode's
// config_content merges into the existing OPENCODE_CONFIG_CONTENT env
// entry (already set by the opencode driver's Build()) rather than
// writing any file, and that the merge is additive — existing top-level
// keys (model, small_model, provider) survive alongside the new one.
func TestApplyConfigContentOpenCodeMergesIntoEnv(t *testing.T) {
	cmd := exec.Command("opencode")
	cmd.Env = []string{`OPENCODE_CONFIG_CONTENT={"model":"agent-wt/x","small_model":"agent-wt/x"}`}
	rp := ResolvedProfile{ConfigContent: map[string]any{"compaction": map[string]any{"auto": true}}}
	cleanup, err := ApplyConfigContent(cmd, "opencode", "/tmp/wt", rp)
	if err != nil {
		t.Fatalf("ApplyConfigContent() error = %v", err)
	}
	defer cleanup()
	var merged string
	for _, e := range cmd.Env {
		if len(e) > len("OPENCODE_CONFIG_CONTENT=") && e[:len("OPENCODE_CONFIG_CONTENT=")] == "OPENCODE_CONFIG_CONTENT=" {
			merged = e[len("OPENCODE_CONFIG_CONTENT="):]
		}
	}
	if merged == "" {
		t.Fatal("OPENCODE_CONFIG_CONTENT not found in cmd.Env")
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(merged), &doc); err != nil {
		t.Fatalf("merged env is not valid JSON: %v (%s)", err, merged)
	}
	if doc["model"] != "agent-wt/x" {
		t.Errorf("merged doc lost existing key: %v", doc)
	}
	if _, has := doc["compaction"]; !has {
		t.Errorf("merged doc missing new key: %v", doc)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd wt && go test ./internal/profiles/... -run 'TestConfigFileTarget|TestApplyConfigContent' -v`
Expected: FAIL — `ConfigFileTarget`/`ApplyConfigContent`/`restoreIfBackedUp` undefined.

- [ ] **Step 3: Write the implementation**

```go
// wt/internal/profiles/configcontent.go
package profiles

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// ConfigFileTarget returns the file profile config_content should be
// written to for agent, or ok=false if the agent takes config_content via
// an inline env var instead (opencode). claude's target is worktree-
// scoped (never the user's global ~/.claude/settings.json); codex's is a
// dedicated wt-owned global profile file, since codex has no
// project-scoped profile mechanism to use instead.
func ConfigFileTarget(agent, worktreePath string) (path string, isTOML bool, ok bool) {
	switch agent {
	case "claude":
		return filepath.Join(worktreePath, ".claude", "settings.local.json"), false, true
	case "codex":
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".codex", "agent-wt-profile.config.toml"), true, true
	default:
		return "", false, false
	}
}

const openCodeConfigEnvPrefix = "OPENCODE_CONFIG_CONTENT="

// ApplyConfigContent writes rp.ConfigContent for agent: a real,
// snapshot/restored file for claude and codex, or a merge into cmd.Env's
// existing OPENCODE_CONFIG_CONTENT entry for opencode. It returns a
// cleanup function the caller MUST call after the launched process exits
// (success or failure) to restore whatever the write touched.
func ApplyConfigContent(cmd *exec.Cmd, agent, worktreePath string, rp ResolvedProfile) (cleanup func() error, err error) {
	noop := func() error { return nil }
	if len(rp.ConfigContent) == 0 {
		return noop, nil
	}
	if agent == "opencode" {
		if err := mergeOpenCodeEnv(cmd, rp.ConfigContent); err != nil {
			return nil, err
		}
		return noop, nil
	}
	path, isTOML, ok := ConfigFileTarget(agent, worktreePath)
	if !ok {
		return nil, fmt.Errorf("profile: agent %q has no config_content target", agent)
	}
	var data []byte
	if isTOML {
		var buf bytes.Buffer
		if err := toml.NewEncoder(&buf).Encode(rp.ConfigContent); err != nil {
			return nil, fmt.Errorf("encode profile config_content: %w", err)
		}
		data = buf.Bytes()
		if agent == "codex" {
			cmd.Args = append(cmd.Args, "--profile", "agent-wt-profile")
		}
	} else {
		data, err = json.MarshalIndent(rp.ConfigContent, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("encode profile config_content: %w", err)
		}
	}
	if err := snapshotAndWrite(path, data, 0o644); err != nil {
		return nil, err
	}
	return func() error {
		_, err := restoreIfBackedUp(path)
		return err
	}, nil
}

func mergeOpenCodeEnv(cmd *exec.Cmd, content map[string]any) error {
	for i, e := range cmd.Env {
		if !strings.HasPrefix(e, openCodeConfigEnvPrefix) {
			continue
		}
		var doc map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(e, openCodeConfigEnvPrefix)), &doc); err != nil {
			return fmt.Errorf("profile: existing OPENCODE_CONFIG_CONTENT is not valid JSON: %w", err)
		}
		for k, v := range content {
			doc[k] = v
		}
		merged, err := json.Marshal(doc)
		if err != nil {
			return fmt.Errorf("profile: re-encode OPENCODE_CONFIG_CONTENT: %w", err)
		}
		cmd.Env[i] = openCodeConfigEnvPrefix + string(merged)
		return nil
	}
	return fmt.Errorf("profile: opencode launch has no OPENCODE_CONFIG_CONTENT to merge into")
}

// backupDir holds pre-write snapshots of files config_content rewrites,
// never a sibling file next to the target (so nothing lands in a repo or
// worktree).
func backupDir() string { return filepath.Join(config.Dir(), "profile-backups") }

func backupKey(target string) string {
	sum := sha256.Sum256([]byte(target))
	return filepath.Join(backupDir(), hex.EncodeToString(sum[:]))
}

func backupPresentPath(target string) string { return backupKey(target) + ".present" }
func backupAbsentPath(target string) string  { return backupKey(target) + ".absent" }

// snapshotAndWrite self-heals any leftover backup for target first (see
// restoreIfBackedUp), then snapshots target's current state (content, or
// "did not exist") before writing data.
func snapshotAndWrite(target string, data []byte, perm os.FileMode) error {
	if _, err := restoreIfBackedUp(target); err != nil {
		return err
	}
	existing, err := os.ReadFile(target)
	switch {
	case os.IsNotExist(err):
		if err := config.WriteFileAtomic(backupAbsentPath(target), []byte{}, 0o600); err != nil {
			return err
		}
	case err != nil:
		return err
	default:
		if err := config.WriteFileAtomic(backupPresentPath(target), existing, 0o600); err != nil {
			return err
		}
	}
	return config.WriteFileAtomic(target, data, perm)
}

// restoreIfBackedUp restores target from whatever snapshotAndWrite backed
// up, removing the backup marker, and reports whether it found one to
// restore. It is safe to call with no backup present (a no-op) — this is
// the SAME operation for the normal post-launch restore and for
// self-healing an orphaned backup before a fresh write; the two are not
// distinguished by the function, only by when the caller invokes it.
func restoreIfBackedUp(target string) (restored bool, err error) {
	if _, err := os.Stat(backupAbsentPath(target)); err == nil {
		if rmErr := os.Remove(target); rmErr != nil && !os.IsNotExist(rmErr) {
			return false, rmErr
		}
		return true, os.Remove(backupAbsentPath(target))
	}
	if data, err := os.ReadFile(backupPresentPath(target)); err == nil {
		if werr := config.WriteFileAtomic(target, data, 0o644); werr != nil {
			return false, werr
		}
		return true, os.Remove(backupPresentPath(target))
	}
	return false, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd wt && go test ./internal/profiles/... -run 'TestConfigFileTarget|TestApplyConfigContent' -v`
Expected: PASS (6 tests)

- [ ] **Step 5: Commit**

```bash
cd wt && git add internal/profiles/configcontent.go internal/profiles/configcontent_test.go
git commit -m "feat(wt): write/backup/restore profile config_content per agent"
```

---

## Task 6: Driver capability declarations

**Files:**
- Modify: `wt/internal/agents/claude.go`, `wt/internal/agents/pi.go`, `wt/internal/agents/codex.go`, `wt/internal/agents/opencode.go`
- Modify (tests): `wt/internal/agents/claude_test.go`, `wt/internal/agents/pi_models_test.go` (or a new `wt/internal/agents/pi_test.go` if none exists for the driver itself), `wt/internal/agents/codex_test.go`, `wt/internal/agents/opencode_test.go`

**Interfaces:**
- Consumes: `profiles.Mechanism`, `profiles.MechanismEnv/Args/ConfigFile/Wrapper` (Task 1).
- Produces: each driver type now implements `profiles.ProfileCapable`.

- [ ] **Step 1: Write the failing tests**

Append to each existing driver test file (the exact append target is each file's end; each test constructs the driver value directly, matching how `claudeDriver{}`/`piDriver{}`/etc. are already used elsewhere in these files):

```go
// wt/internal/agents/claude_test.go — append
// TestClaudeDriverDeclaresEnvAndConfigFileProfileMechanisms verifies
// claude's ProfileMechanisms matches the Phase-1 design (env for the
// attribution header/thinking cap, config_content for the
// ANTHROPIC_DEFAULT_*_MODEL mapping via settings.local.json) — a profile
// using any other mechanism for claude must fail Validate.
func TestClaudeDriverDeclaresEnvAndConfigFileProfileMechanisms(t *testing.T) {
	mechs := claudeDriver{}.ProfileMechanisms()
	want := map[profiles.Mechanism]bool{profiles.MechanismEnv: true, profiles.MechanismConfigFile: true}
	if len(mechs) != len(want) {
		t.Fatalf("ProfileMechanisms() = %v, want exactly %v", mechs, want)
	}
	for _, m := range mechs {
		if !want[m] {
			t.Errorf("unexpected mechanism %q", m)
		}
	}
}
```

```go
// wt/internal/agents/pi_models_test.go — append
// TestPiDriverDeclaresWrapperProfileMechanism verifies pi's
// ProfileMechanisms is exactly {wrapper} — the little-coder integration —
// and nothing else, so a hand-written pi profile using env/args/config_file
// fails Validate rather than silently doing nothing.
func TestPiDriverDeclaresWrapperProfileMechanism(t *testing.T) {
	mechs := piDriver{}.ProfileMechanisms()
	if len(mechs) != 1 || mechs[0] != profiles.MechanismWrapper {
		t.Errorf("ProfileMechanisms() = %v, want exactly [wrapper]", mechs)
	}
}
```

```go
// wt/internal/agents/codex_test.go — append
// TestCodexDriverDeclaresEnvArgsConfigFileProfileMechanisms verifies
// codex accepts env, args, and config_file — the Phase-1 codex profile
// uses args (-c overrides) only, but config_file is declared as
// available capability per the design's generic mechanism model.
func TestCodexDriverDeclaresEnvArgsConfigFileProfileMechanisms(t *testing.T) {
	mechs := codexDriver{}.ProfileMechanisms()
	want := map[profiles.Mechanism]bool{
		profiles.MechanismEnv: true, profiles.MechanismArgs: true, profiles.MechanismConfigFile: true,
	}
	if len(mechs) != len(want) {
		t.Fatalf("ProfileMechanisms() = %v, want exactly %v", mechs, want)
	}
	for _, m := range mechs {
		if !want[m] {
			t.Errorf("unexpected mechanism %q", m)
		}
	}
}
```

```go
// wt/internal/agents/opencode_test.go — append
// TestOpenCodeDriverDeclaresEnvAndConfigFileProfileMechanisms verifies
// opencode accepts env and config_file — config_file mechanically merges
// into the OPENCODE_CONFIG_CONTENT env entry (internal/profiles handles
// that dispatch), but from the capability-declaration side it is still
// the config_file mechanism the compaction-tuning profile uses.
func TestOpenCodeDriverDeclaresEnvAndConfigFileProfileMechanisms(t *testing.T) {
	mechs := opencodeDriver{}.ProfileMechanisms()
	want := map[profiles.Mechanism]bool{profiles.MechanismEnv: true, profiles.MechanismConfigFile: true}
	if len(mechs) != len(want) {
		t.Fatalf("ProfileMechanisms() = %v, want exactly %v", mechs, want)
	}
	for _, m := range mechs {
		if !want[m] {
			t.Errorf("unexpected mechanism %q", m)
		}
	}
}
```

Add the import `"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"` to each of the four test files (alongside their existing imports).

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd wt && go test ./internal/agents/... -run ProfileMechanisms -v`
Expected: FAIL — `ProfileMechanisms` undefined on each driver type.

- [ ] **Step 3: Write the implementation**

Add to each driver file (alongside the existing `import` block and method set):

Add `"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"` to each
driver file's import block, then append the method after `Build()` in each
file:

```go
// wt/internal/agents/claude.go
// and, after Build():

// ProfileMechanisms declares which profile mechanisms claude accepts:
// env (the attribution-header/thinking-token tweaks) and config_file
// (the ANTHROPIC_DEFAULT_*_MODEL mapping via .claude/settings.local.json).
func (claudeDriver) ProfileMechanisms() []profiles.Mechanism {
	return []profiles.Mechanism{profiles.MechanismEnv, profiles.MechanismConfigFile}
}
```

```go
// wt/internal/agents/pi.go — add the same import, and:

// ProfileMechanisms declares that pi only accepts a wrapper profile — the
// little-coder integration; pi has no env/config-file lever of its own
// for this.
func (piDriver) ProfileMechanisms() []profiles.Mechanism {
	return []profiles.Mechanism{profiles.MechanismWrapper}
}
```

```go
// wt/internal/agents/codex.go — add the same import, and:

// ProfileMechanisms declares codex accepts env, args (inline -c
// overrides — what the Phase-1 codex profile uses), and config_file (a
// dedicated ~/.codex/agent-wt-profile.config.toml, available but unused
// by the Phase-1 example).
func (codexDriver) ProfileMechanisms() []profiles.Mechanism {
	return []profiles.Mechanism{profiles.MechanismEnv, profiles.MechanismArgs, profiles.MechanismConfigFile}
}
```

```go
// wt/internal/agents/opencode.go — add the same import, and:

// ProfileMechanisms declares opencode accepts env and config_file —
// config_file is merged into the OPENCODE_CONFIG_CONTENT env payload
// (internal/profiles' ApplyConfigContent), not written as a separate
// file.
func (opencodeDriver) ProfileMechanisms() []profiles.Mechanism {
	return []profiles.Mechanism{profiles.MechanismEnv, profiles.MechanismConfigFile}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd wt && go test ./internal/agents/... -run ProfileMechanisms -v`
Expected: PASS (4 tests)

- [ ] **Step 5: Run the full agents package test suite to check for regressions**

Run: `cd wt && go test ./internal/agents/... -v`
Expected: PASS (all existing tests, unaffected by the new methods)

- [ ] **Step 6: Commit**

```bash
cd wt && git add internal/agents/claude.go internal/agents/claude_test.go \
  internal/agents/pi.go internal/agents/pi_models_test.go \
  internal/agents/codex.go internal/agents/codex_test.go \
  internal/agents/opencode.go internal/agents/opencode_test.go
git commit -m "feat(wt): declare per-agent profile mechanism capabilities"
```

---

## Task 7: Non-TUI wiring — `app` + `runAgentCmd`

**Files:**
- Modify: `wt/cmd/wt/app.go`
- Modify: `wt/cmd/wt/launch.go`
- Test: `wt/cmd/wt/app_test.go` (create if it doesn't already exist as a distinct file — check first: `ls wt/cmd/wt/app_test.go`; if `newApp` is instead tested inline in `main_test.go`, add there instead)
- Test: `wt/cmd/wt/launch_test.go` (existing)

**Interfaces:**
- Consumes: `profiles.Load`, `profiles.Validate`, `profiles.Resolve`, `profiles.ApplyToCmd`, `profiles.ApplyConfigContent` (Tasks 1–5); `agents.ByName`, `profiles.ProfileCapable` (Task 6).
- Produces: `app.profiles profiles.Store`, `app.profilesErr error`; package-level seams `loadProfileStore func() (profiles.Store, error)`, `confirmProfile func(profiles.ResolvedProfile) (bool, error)`, and `openTTY func() (*os.File, error)` in `cmd/wt` (tests swap all three); `runAgentCmd`'s behavior (signature unchanged).

- [ ] **Step 1: Check whether `newApp` already has a dedicated test file**

Run: `cd wt && ls cmd/wt/app_test.go 2>/dev/null || grep -l "func TestNewApp\|func newApp" cmd/wt/*_test.go`

Use whichever file already covers `newApp` (likely `main_test.go`); if none exists, create `cmd/wt/app_test.go`. The steps below assume the latter — adjust the file path if an existing one is found.

- [ ] **Step 2: Write the failing test for `newApp` profile loading**

```go
// wt/cmd/wt/app_test.go
package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestNewAppLoadsProfilesStore verifies newApp populates a.profiles from
// profiles.toml (via the same XDG_CONFIG_HOME resolution config.Dir()
// uses) so `wt profile ...` commands and the launch path share one
// loaded-once-per-invocation copy — a missing file must not be an error.
func TestNewAppLoadsProfilesStore(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	a, err := newApp()
	if err != nil {
		t.Fatalf("newApp() error = %v", err)
	}
	if !a.profiles.Enabled {
		t.Errorf("a.profiles.Enabled = false, want true (default, no profiles.toml yet)")
	}
	if a.profilesErr != nil {
		t.Errorf("a.profilesErr = %v, want nil", a.profilesErr)
	}
}

// TestNewAppSurfacesProfilesValidateError verifies a profiles.toml entry
// using a mechanism its agent doesn't declare is caught at startup
// (a.profilesErr set), not silently accepted — mirrors how a.cfgErr
// surfaces a bad config.toml.
func TestNewAppSurfacesProfilesValidateError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "agent-wt"), 0o755); err != nil {
		t.Fatal(err)
	}
	bad := `
[[profiles]]
agent = "pi"
match = "location"
location = "local"
config_content = { x = "1" }
`
	if err := os.WriteFile(filepath.Join(dir, "agent-wt", "profiles.toml"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := newApp()
	if err != nil {
		t.Fatalf("newApp() error = %v, want nil (profilesErr carries the problem, like cfgErr does)", err)
	}
	if a.profilesErr == nil {
		t.Error("a.profilesErr = nil, want an error (pi does not accept config_content)")
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `cd wt && go test ./cmd/wt/... -run TestNewAppLoadsProfiles -v && go test ./cmd/wt/... -run TestNewAppSurfacesProfiles -v`
Expected: FAIL — `a.profiles`/`a.profilesErr` do not exist on `app` yet.

- [ ] **Step 4: Wire `profiles.Store` into `app`**

```go
// wt/cmd/wt/app.go — replace the whole file
package main

import (
	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// app holds shared dependencies loaded once at startup.
type app struct {
	cfg         *config.Config
	cfgErr      error        // config load/validation error, if any; surfaced by commands that can repair it
	loadErr     error        // config.Load error ONLY (parse/IO/registry missing); gates commands that just rewrite wt's own [litellm] state
	theme       themes.Theme // active theme; populated by newApp()
	profiles    profiles.Store
	profilesErr error // profiles.toml load or Validate error, if any; surfaced by `wt profile` commands
}

// agentProfileMechanisms looks up the profile mechanisms agent's driver
// declares via profiles.ProfileCapable, or nil for an unregistered agent
// or one that implements no mechanisms at all (copilot, shell).
func agentProfileMechanisms(agent string) []profiles.Mechanism {
	d := agents.ByName(agent)
	if d == nil {
		return nil
	}
	pc, ok := d.(profiles.ProfileCapable)
	if !ok {
		return nil
	}
	return pc.ProfileMechanisms()
}

// newApp loads the config (best-effort), the active theme, and the
// profiles store. Config and profiles validation errors are stored in the
// returned app rather than returned as a fatal error, so `wt config`/
// `wt profile` can still launch and let the user repair a broken file.
// Live model discovery is deferred to the `models` subcommand so
// flag-only paths (--version, --init, -w, --cwd) don't shell out to
// ollama or hit the OpenRouter API.
func newApp() (*app, error) {
	cfg, cfgErr := config.Load()
	loadErr := cfgErr
	if cfg == nil {
		cfg = &config.Config{DefaultTag: "code"}
	}
	if cfgErr == nil {
		cfgErr = cfg.Validate()
	}
	// Load the active theme. I/O and TOML parse errors are hard failures.
	// An unknown or empty theme name falls back to Default so a typo in
	// themes.toml never crashes the launcher — the user can still run
	// `wt config theme set` or `wt config theme unset` to repair it.
	theme, _, err := themes.Load()
	if err != nil && !themes.IsThemeNameError(err) {
		return nil, err
	}
	store, profilesErr := profiles.Load()
	if profilesErr == nil {
		profilesErr = profiles.Validate(store, agentProfileMechanisms)
	}
	return &app{
		cfg: cfg, cfgErr: cfgErr, loadErr: loadErr, theme: theme,
		profiles: store, profilesErr: profilesErr,
	}, nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd wt && go test ./cmd/wt/... -run 'TestNewAppLoadsProfiles|TestNewAppSurfacesProfiles' -v`
Expected: PASS (2 tests)

- [ ] **Step 6: Write the failing tests for `runAgentCmd`'s profile behavior**

```go
// wt/cmd/wt/launch_test.go — append
// TestRunAgentCmdAppliesConfirmedProfile verifies a matching, TTY-confirmed
// profile's Env lands on the launched process's environment — the
// end-to-end path from resolution through ApplyToCmd, exercised through
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
	if err := runAgentCmd(cmd, "claude", m, cfg); err != nil {
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
	if err := runAgentCmd(cmd, "claude", m, cfg); err != nil {
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
	if err := runAgentCmd(cmd, "claude", m, cfg); err != nil {
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
	if err := runAgentCmd(exec.Command("true"), "shell", config.Model{}, cfg); err != nil {
		t.Fatalf("runAgentCmd() error = %v", err)
	}
	if promptCalled {
		t.Error("confirmProfile called for a command agent (m.ID == \"\")")
	}
	if err := runAgentCmd(exec.Command("true"), "claude", config.Model{ID: "claude/native", Native: true, ModelName: "native"}, cfg); err != nil {
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
	if err := runAgentCmd(exec.Command("true"), "claude", m, cfg); err != nil {
		t.Fatalf("runAgentCmd() error = %v, want a normal launch despite the malformed file", err)
	}
}
```

```go
// wt/cmd/wt/launch_test.go — append
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
```

Add imports `"errors"`, `"os"`, `"path/filepath"`, `"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"` to `launch_test.go` if not already present (check the existing import block first — `errors` is likely already imported since `launch.go` itself uses `errors.As`, but `launch_test.go` may not).

- [ ] **Step 7: Run tests to verify they fail**

Run: `cd wt && go test ./cmd/wt/... -run 'TestRunAgentCmd|TestPromptProfile' -v`
Expected: FAIL — `confirmProfile`/`openTTY` undefined, and `runAgentCmd` does not yet apply any profile.

- [ ] **Step 8: Implement the `promptProfile`/`confirmProfile` seam and TTY prompt**

```go
// wt/cmd/wt/launch.go — add to the import block: "bufio", "io", "strings",
// "github.com/ohanaverse/local-ai-setup/wt/internal/profiles" (the file
// does not currently import "io" or "strings" — check the existing block
// first so nothing is double-added)
// then add near the other seam vars at the top of the file:

// loadProfileStore is a seam for tests: production reads
// ~/.config/agent-wt/profiles.toml (via profiles.Load); tests swap it so
// no test depends on the developer's real file.
var loadProfileStore = profiles.Load

// confirmProfile is a seam for tests: production asks on /dev/tty whether
// to apply a resolved profile, defaulting to yes on a bare Enter or when
// no TTY is available (non-interactive launches — scripts, wt smoke, CI —
// must never block on input that can't arrive); tests swap it so nothing
// blocks on real terminal input.
var confirmProfile = promptProfile

// openTTY is a seam for tests: production opens the controlling terminal
// directly; tests swap it so a non-TTY (or TTY-available-but-must-not-
// block) scenario is deterministic regardless of whether the test runner
// itself happens to have a real /dev/tty attached.
var openTTY = func() (*os.File, error) { return os.OpenFile("/dev/tty", os.O_RDWR, 0) }

func promptProfile(rp profiles.ResolvedProfile) (bool, error) {
	f, err := openTTY()
	if err != nil {
		return true, nil
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "wt: local-model profile available for this launch (%s) — apply? [Y/n] ", strings.Join(rp.Sources, ", ")); err != nil {
		return false, err
	}
	return askYesNoDefault(f, true)
}

func askYesNoDefault(r io.Reader, def bool) (bool, error) {
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && line == "" {
		return def, nil
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	case "n", "no":
		return false, nil
	default:
		return def, nil
	}
}
```

- [ ] **Step 9: Implement `applyProfileForLaunch` and wire it into `runAgentCmd`**

```go
// wt/cmd/wt/launch.go — add:

// applyProfileForLaunch resolves and, on confirmation, applies a
// local-model profile for agent/m to cmd before it runs. It returns a
// cleanup func that MUST be called after cmd.Run() returns (success or
// failure) to restore any config file a profile's config_content
// mechanism rewrote — called explicitly, never via defer, since the
// os.Exit branch in runAgentCmd below would otherwise skip a deferred
// cleanup (the same reasoning that already makes
// lifecycle.WaitPendingRoutes an explicit call there, not a defer).
// Command agents (m.ID == "") and native models never match a profile —
// ResolveRoute returns a zero Route for both — so this no-ops for them
// without even loading profiles.toml.
func applyProfileForLaunch(cmd *exec.Cmd, agent string, m config.Model, cfg *config.Config) (cleanup func() error, err error) {
	noop := func() error { return nil }
	if m.ID == "" || m.Native || cfg == nil {
		return noop, nil
	}
	store, err := loadProfileStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "wt: profiles.toml: %v (profiles disabled for this launch)\n", err)
		return noop, nil
	}
	if verr := profiles.Validate(store, agentProfileMechanisms); verr != nil {
		fmt.Fprintf(os.Stderr, "wt: profiles.toml: %v (profiles disabled for this launch)\n", verr)
		return noop, nil
	}
	if !store.Enabled {
		return noop, nil
	}
	rp := profiles.Resolve(store, agent, cfg, m)
	if rp.Empty() {
		return noop, nil
	}
	apply, err := confirmProfile(rp)
	if err != nil {
		return noop, err
	}
	if !apply {
		return noop, nil
	}
	// config_content first, so a codex profile's "--profile
	// agent-wt-profile" arg it appends is captured by a later wrapper's
	// {{args}} splice (no Phase-1 profile combines the two, but this
	// keeps the ordering correct if one ever does).
	contentCleanup, err := profiles.ApplyConfigContent(cmd, agent, cmd.Dir, rp)
	if err != nil {
		return noop, err
	}
	if err := profiles.ApplyToCmd(cmd, rp); err != nil {
		return noop, err
	}
	return contentCleanup, nil
}
```

Now wire it into `runAgentCmd` — modify the existing function body (`wt/cmd/wt/launch.go`, the function already shown in exploration above):

```go
// wt/cmd/wt/launch.go — replace runAgentCmd's body
func runAgentCmd(cmd *exec.Cmd, agent string, m config.Model, cfg *config.Config) error {
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	profileCleanup, perr := applyProfileForLaunch(cmd, agent, m, cfg)
	if perr != nil {
		return perr
	}

	start := time.Now()
	err := cmd.Run()
	if cerr := profileCleanup(); cerr != nil {
		fmt.Fprintf(os.Stderr, "warning: profile cleanup failed: %v\n", cerr)
	}
	summary := agents.Summary(agent, m, time.Since(start))

	releaseSession()
	stats := survey.PromptRun(os.Stdin, os.Stdout, survey.NewStore(), agent, m)
	if m.ID != "" && !m.Native {
		runStopPicker(cfg)
	}

	fmt.Println("\n" + summary)
	if stats != "" {
		fmt.Println(stats)
	}
	if m.ID != "" {
		emitPriceNotice()
	}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			lifecycle.WaitPendingRoutes()
			os.Exit(ee.ExitCode())
		}
		return err
	}
	return nil
}
```

(Everything from `releaseSession()` onward is unchanged from the existing function — only the new profile block at the top and the explicit `profileCleanup()` call right after `cmd.Run()` are new.)

- [ ] **Step 10: Run tests to verify they pass**

Run: `cd wt && go test ./cmd/wt/... -run 'TestRunAgentCmd|TestPromptProfile' -v`
Expected: PASS (6 tests)

- [ ] **Step 11: Run the full `cmd/wt` test suite to check for regressions**

Run: `cd wt && go build ./... && go vet ./... && go test ./cmd/wt/... -v`
Expected: PASS (all existing tests unaffected — `runAgentCmd`'s signature and every non-profile behavior is unchanged)

- [ ] **Step 12: Commit**

```bash
cd wt && git add cmd/wt/app.go cmd/wt/app_test.go cmd/wt/launch.go cmd/wt/launch_test.go
git commit -m "feat(wt): resolve/confirm/apply/restore profiles around non-TUI launches"
```

---

## Task 8: `wt profile` CLI

**Files:**
- Create: `wt/cmd/wt/profile.go`
- Test: `wt/cmd/wt/profile_test.go`
- Modify: `wt/cmd/wt/main.go:438` (register the command)

**Interfaces:**
- Consumes: `app.profiles`, `app.profilesErr` (Task 7); `profiles.Resolve`, `profiles.Store` (Tasks 1–2).
- Produces: `wt profile list|show|status|on|off` cobra commands; `profileCmd(a *app) *cobra.Command`.

- [ ] **Step 1: Write the failing tests**

```go
// wt/cmd/wt/profile_test.go
package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)

// TestProfileListPrintsEveryProfile verifies `wt profile list` prints one
// line per profiles.toml entry, naming its agent and match tier — the
// simplest possible smoke test that the command reads a.profiles rather
// than reloading the file itself.
func TestProfileListPrintsEveryProfile(t *testing.T) {
	a := &app{profiles: profiles.Store{Enabled: true, Profiles: []profiles.Profile{
		{Agent: "claude", Match: "location", Location: "local"},
		{Agent: "pi", Match: "location", Location: "local"},
	}}}
	var out bytes.Buffer
	cmd := profileCmd(a)
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"list"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute error = %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "claude") || !strings.Contains(got, "pi") {
		t.Errorf("output = %q, want both claude and pi listed", got)
	}
}

// TestProfileShowResolvesForAgentAndModel verifies `wt profile show -A
// claude -M <id>` dry-runs Resolve() and reports what would apply,
// without launching anything.
func TestProfileShowResolvesForAgentAndModel(t *testing.T) {
	a := &app{
		cfg: &config.Config{
			Providers: []config.Provider{{ID: "ollama", Location: config.LocationLocal}},
			Models:    []config.Model{{ID: "ollama/x", ProviderID: "ollama", ModelName: "x"}},
		},
		profiles: profiles.Store{Enabled: true, Profiles: []profiles.Profile{
			{Agent: "claude", Match: "location", Location: "local", Env: map[string]string{"X": "1"}},
		}},
	}
	var out bytes.Buffer
	cmd := profileCmd(a)
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"show", "-A", "claude", "-M", "ollama/x"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute error = %v", err)
	}
	if !strings.Contains(out.String(), "X=1") {
		t.Errorf("output = %q, want the resolved env var shown", out.String())
	}
}

// TestProfileStatusOnOffTogglesEnabledFlag verifies `wt profile off` then
// `wt profile status` reflects the change by writing/reading the same
// profiles.toml, and `wt profile on` reverts it — the global kill switch
// the design commits to.
func TestProfileStatusOnOffTogglesEnabledFlag(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	a := &app{profiles: profiles.Store{Enabled: true}}

	off := profileCmd(a)
	off.SetArgs([]string{"off"})
	if err := off.Execute(); err != nil {
		t.Fatalf("off: execute error = %v", err)
	}
	reloaded, err := profiles.Load()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Enabled {
		t.Error("after `wt profile off`, profiles.toml still has enabled=true")
	}

	a2 := &app{profiles: reloaded}
	var statusOut bytes.Buffer
	status := profileCmd(a2)
	status.SetOut(&statusOut)
	status.SetArgs([]string{"status"})
	if err := status.Execute(); err != nil {
		t.Fatalf("status: execute error = %v", err)
	}
	if !strings.Contains(statusOut.String(), "off") {
		t.Errorf("status output = %q, want it to report off", statusOut.String())
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd wt && go test ./cmd/wt/... -run TestProfile -v`
Expected: FAIL — `profileCmd` undefined.

- [ ] **Step 3: Write the implementation**

```go
// wt/cmd/wt/profile.go
// wt profile — inspect and toggle the local-model profile layer
// (internal/profiles). See docs/wt-agents/profiles.md.
package main

import (
	"fmt"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
	"github.com/spf13/cobra"
)

func matchValueForDisplay(p profiles.Profile) string {
	switch p.Match {
	case "location":
		return "location=" + p.Location
	case "provider":
		return "provider=" + p.Provider
	default:
		return "model=" + p.Model
	}
}

func profileCmd(a *app) *cobra.Command {
	c := &cobra.Command{
		Use:   "profile",
		Short: "Inspect and toggle local-model launch profiles (internal/profiles)",
	}

	listC := &cobra.Command{
		Use: "list", Short: "List every defined profile", Args: cobra.NoArgs, SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if len(a.profiles.Profiles) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "(no profiles defined)")
				return nil
			}
			for _, p := range a.profiles.Profiles {
				fmt.Fprintf(cmd.OutOrStdout(), "%s: match=%s %s\n", p.Agent, p.Match, matchValueForDisplay(p))
			}
			return nil
		},
	}

	var showAgent, showModel string
	showC := &cobra.Command{
		Use: "show", Short: "Dry-run profile resolution for an agent/model, without launching", Args: cobra.NoArgs, SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if showAgent == "" {
				return fmt.Errorf("-A/--agent is required")
			}
			if a.profilesErr != nil {
				return fmt.Errorf("profiles.toml error: %w", a.profilesErr)
			}
			m, err := findModelByID(a.cfg, showModel)
			if err != nil {
				return err
			}
			rp := profiles.Resolve(a.profiles, showAgent, a.cfg, m)
			if rp.Empty() {
				fmt.Fprintln(cmd.OutOrStdout(), "(no matching profile)")
				return nil
			}
			for k, v := range rp.Env {
				fmt.Fprintf(cmd.OutOrStdout(), "env: %s=%s\n", k, v)
			}
			if len(rp.ExtraArgs) > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "args: %v\n", rp.ExtraArgs)
			}
			if len(rp.ConfigContent) > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "config_content: %v\n", rp.ConfigContent)
			}
			if rp.Wrapper != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "wrapper: %s %v\n", rp.Wrapper.Binary, rp.Wrapper.ArgsTemplate)
			}
			return nil
		},
	}
	showC.Flags().StringVarP(&showAgent, "agent", "A", "", "agent to resolve for")
	showC.Flags().StringVarP(&showModel, "model", "M", "", "model id to resolve for")

	statusC := &cobra.Command{
		Use: "status", Short: "Show whether the profile layer is enabled", Args: cobra.NoArgs, SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if a.profiles.Enabled {
				fmt.Fprintln(cmd.OutOrStdout(), "profiles: on")
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "profiles: off")
			}
			return nil
		},
	}

	toggle := func(use, short string, enabled bool) *cobra.Command {
		return &cobra.Command{
			Use: use, Short: short, Args: cobra.NoArgs, SilenceUsage: true,
			RunE: func(cmd *cobra.Command, _ []string) error {
				store := a.profiles
				store.Enabled = enabled
				if err := writeProfilesEnabled(store); err != nil {
					return err
				}
				a.profiles = store
				fmt.Fprintf(cmd.OutOrStdout(), "profiles: %s\n", map[bool]string{true: "on", false: "off"}[enabled])
				return nil
			},
		}
	}

	c.AddCommand(listC, showC, statusC, toggle("on", "Enable the profile layer", true), toggle("off", "Disable the profile layer", false))
	return c
}

// findModelByID looks m up in cfg.Models by exact id, for `wt profile
// show -M`. Unlike the launch path this does not consult live/discovered
// inventory — it is a dry-run tool over the registry only.
func findModelByID(cfg *config.Config, id string) (config.Model, error) {
	if cfg == nil {
		return config.Model{}, fmt.Errorf("no config loaded")
	}
	for _, m := range cfg.Models {
		if m.ID == id {
			return m, nil
		}
	}
	return config.Model{}, fmt.Errorf("model %q not found in registry", id)
}

// writeProfilesEnabled rewrites the top-level `enabled` key in
// profiles.toml, preserving every existing [[profiles]] entry exactly —
// it re-encodes the whole Store, matching config.Save's whole-file
// atomic-write convention (config.WriteFileAtomic) rather than a
// partial/line-level edit.
func writeProfilesEnabled(store profiles.Store) error {
	return profiles.Save(store)
}

var _ = sort.Strings // keep sort imported if a later revision needs deterministic show ordering
```

`profileCmd` above references `profiles.Save`, which does not exist yet —
add it to `internal/profiles/profiles.go` (Task 1's file) as part of this
step, since the CLI is its only caller and it belongs with `Load`:

```go
// wt/internal/profiles/profiles.go — add near Load()

// Save writes store to profiles.toml as a whole-file atomic write
// (config.WriteFileAtomic), the same convention config.Save uses for
// config.toml. Used by `wt profile on|off`.
func Save(store Store) error {
	var buf bytes.Buffer
	fs := fileSchema{Enabled: &store.Enabled, Profiles: store.Profiles}
	if err := toml.NewEncoder(&buf).Encode(&fs); err != nil {
		return err
	}
	return config.WriteFileAtomic(Path(), buf.Bytes(), 0o644)
}
```

(add `"bytes"` to `profiles.go`'s import block). The final `profile.go`
is exactly `profileCmd`, `matchValueForDisplay`, `findModelByID`, and
`writeProfilesEnabled`, with imports `fmt`,
`github.com/ohanaverse/local-ai-setup/wt/internal/config`,
`github.com/ohanaverse/local-ai-setup/wt/internal/profiles`, and
`github.com/spf13/cobra`.

- [ ] **Step 4: Register the command**

```go
// wt/cmd/wt/main.go:438 — change
cmd.AddCommand(rotateCmd(a), configCmd(a), statsCmd(a), smokeCmd(a), stopCmd(a), startCmd(a), litellmCmd(a))
// to
cmd.AddCommand(rotateCmd(a), configCmd(a), statsCmd(a), smokeCmd(a), stopCmd(a), startCmd(a), litellmCmd(a), profileCmd(a))
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd wt && go test ./cmd/wt/... -run TestProfile -v`
Expected: PASS (3 tests)

- [ ] **Step 6: Full build + vet + test**

Run: `cd wt && go build ./... && go vet ./... && go test ./...`
Expected: PASS, no regressions anywhere in the module

- [ ] **Step 7: Commit**

```bash
cd wt && git add cmd/wt/profile.go cmd/wt/profile_test.go cmd/wt/main.go internal/profiles/profiles.go
git commit -m "feat(wt): add wt profile list|show|status|on|off CLI"
```

---

## Task 9: Documentation

**Files:**
- Create: `wt/docs/wt-agents/profiles.md`
- Modify: `wt/CLAUDE.md`

**Interfaces:**
- Consumes: nothing (docs only).
- Produces: nothing consumed by later tasks (this is the last task).

- [ ] **Step 1: Write the docs page**

```markdown
<!-- wt/docs/wt-agents/profiles.md -->
# wt profiles: local-model launch tweaks

`internal/profiles` overlays agent-specific tweaks onto a launch, resolved
from the same agent × model location/provider/id `wt` already computes
(`config.Route`/`config.ResolveLocation`). See
`docs/superpowers/specs/2026-09-24-wt-agent-profiles-design.md` (monorepo
root) for the full design; this page is the operator-facing reference.

**Nothing is enabled out of the box.** `~/.config/agent-wt/profiles.toml`
starts absent (equivalent to `enabled = true`, zero profiles) — the four
examples below are Phase-1 recipes, drawn from
`docs/minimal-agent-harnesses/optimizing-coding-agents-for-local-models.md`,
meant to be copied into that file by hand and adjusted, not shipped
defaults.

## Commands

```bash
wt profile list                       # every defined profile
wt profile show -A claude -M <id>     # dry-run resolution, no launch
wt profile status                     # on/off
wt profile on / wt profile off        # global kill switch
```

## Mechanisms by agent

| Agent | Mechanisms | Notes |
|---|---|---|
| claude | env, config_file | config_file → `<worktree>/.claude/settings.local.json`, never your global `~/.claude/settings.json` |
| pi | wrapper | swaps the launched binary (e.g. `little-coder`) around pi's own argv |
| codex | env, args, config_file | config_file → `~/.codex/agent-wt-profile.config.toml` (`--profile agent-wt-profile`); codex has no project-scoped profile mechanism, so this one file is global and wt-owned |
| opencode | env, config_file | config_file merges into the `OPENCODE_CONFIG_CONTENT` env payload wt already sets — no file at all |

A profile using a mechanism its agent doesn't declare fails
`wt profile status` (and every launch) with a clear error at startup —
`copilot`/`shell` accept none.

## File safety

Any real file a profile's `config_content` writes
(`.claude/settings.local.json`, `agent-wt-profile.config.toml`) is
snapshotted before the write and restored the moment the launched agent
exits — success, failure, or Ctrl+C. If `wt` itself is killed
(`kill -9`) before it can restore, the *next* launch that would write the
same file restores the orphaned original first. Don't hand-edit these two
specific files while a profiled session might be running; every other
file a profile touches (env vars, CLI args) leaves nothing behind.

## Phase-1 example profiles

```toml
enabled = true

[[profiles]]
agent = "pi"
match = "location"
location = "local"
wrapper = { binary = "little-coder", args_template = ["--pi-args", "{{args}}"] }

[[profiles]]
agent = "claude"
match = "location"
location = "local"
env = { CLAUDE_CODE_ATTRIBUTION_HEADER = "0", MAX_THINKING_TOKENS = "4096" }
config_content = { env = { ANTHROPIC_DEFAULT_SONNET_MODEL = "{{model_name}}", ANTHROPIC_DEFAULT_HAIKU_MODEL = "{{model_name}}" } }

[[profiles]]
agent = "codex"
match = "provider"
provider = "ollama"
args = ["-c", "model_reasoning_effort=\"low\"", "-c", "tool_output_token_limit=12000"]

[[profiles]]
agent = "opencode"
match = "location"
location = "local"
config_content = { compaction = { auto = true, reserved = 10000 } }
```

## Confirm prompt

An interactive launch (`wt -A <agent> -M <model>`, or the picker once the
TUI follow-up lands) that resolves a non-empty profile asks before
applying it — default **yes** on a bare Enter. A non-interactive launch
(no controlling terminal: scripts, `wt smoke`, CI) applies automatically
with no prompt.
```

- [ ] **Step 2: Add the pointer to `wt/CLAUDE.md`**

Find the `## Docs` section (near the top of `wt/CLAUDE.md`, listing
`docs/configuration.md`, `docs/wt-config.md`, `docs/wt-agents/`,
`docs/superpowers/specs/`, `docs/superpowers/plans/`, `../CLAUDE.md`) and
add one line:

```markdown
- `docs/wt-agents/profiles.md` — local-model launch profiles (`wt profile ...`, `internal/profiles`)
```

- [ ] **Step 3: Check links**

Run: `cd /Users/keith/github/ohanaverse/local-ai-setup && make check-links`
Expected: PASS (no broken relative links introduced)

- [ ] **Step 4: Commit**

```bash
cd wt && git add docs/wt-agents/profiles.md CLAUDE.md
git commit -m "docs(wt): add profiles reference and Phase-1 example profiles"
```

---

## Final verification

- [ ] `cd wt && go build ./... && go vet ./... && go test ./...` — full module, no regressions
- [ ] `cd wt && make check` (shellcheck + shfmt + `go-format-check`) — if the plan's edits touched any shell, otherwise `gofmt -l .` returns nothing
- [ ] `make check-links` from the monorepo root — the new docs page and CLAUDE.md pointer resolve
- [ ] Manual smoke check (requires a local `ollama` model configured in your own `registry.toml`/`config.toml` — skip if none is available in this environment): add the pi + little-coder example from Task 9's docs to your real `~/.config/agent-wt/profiles.toml`, run `wt -A pi -M <a local ollama model>` on a TTY, confirm the prompt appears and names the pi profile, and (separately) confirm `wt profile show -A pi -M <that id>` reports the wrapper without launching anything.
