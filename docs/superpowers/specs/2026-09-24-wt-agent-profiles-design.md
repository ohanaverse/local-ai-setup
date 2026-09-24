# wt agent-model profiles: local-model optimization tweaks per launch

## Problem

`docs/minimal-agent-harnesses/optimizing-coding-agents-for-local-models.md`
catalogs a large set of per-agent tweaks (env vars, config keys, and — for
`pi` — wrapping the launch with a different tool, `little-coder`) that make
coding agents noticeably more reliable against local models. Today applying
any of these means hand-editing agent config or remembering to set env vars
before every local-model session; nothing in `wt` knows that "claude, but
routed at an `ollama` model" should behave differently from "claude, cloud."

`wt` already resolves one `config.Route` per launch — agent, provider,
model, and whether the model is local or cloud — before building the
driver's `LaunchCmd` (`agents.BuildLaunchCmd`). This design adds a profile
layer that matches on that same resolution and overlays agent-specific
tweaks automatically, starting with local-model optimizations.

## Non-goals

- No new agent drivers. `little-coder` is invoked as a generic wrapper
  binary around the existing `pi` driver's `LaunchCmd`, not a new
  `internal/agents` driver.
- No built-in/shipped default profiles in this phase — `profiles.toml`
  starts empty; the four Phase 1 profiles (below) are authored by hand as
  the first real entries, validating the mechanism, not baked into wt's
  binary as defaults every install gets silently.
- No support for `copilot` or `shell` — copilot has no hook/config lever
  for this (per the optimization doc); shell is a command runner with no
  model/route to match against.
- No changes to `wt litellm ...` or the LiteLLM proxy — profiles are a
  launch-time overlay on top of whatever route LiteLLM/direct-mode
  resolution already produced; they never touch routing.

## Design

### 1. Data model — `~/.config/agent-wt/profiles.toml`

New file, wt-owned, loaded alongside `config.toml` (same `Dir()`/env-override
conventions as the rest of `internal/config`). One flat array; each entry
self-describes its match tier:

```toml
enabled = true   # global toggle — `wt profile on|off` flips this

[[profiles]]
agent = "claude"
match = "location"
location = "local"
env = { CLAUDE_CODE_ATTRIBUTION_HEADER = "0", MAX_THINKING_TOKENS = "4096" }
config_content = { env = { ANTHROPIC_DEFAULT_SONNET_MODEL = "{{model_name}}" } }

[[profiles]]
agent = "pi"
match = "location"
location = "local"
wrapper = { binary = "little-coder", args_template = ["--pi-args", "{{args}}"] }

[[profiles]]
agent = "codex"
match = "provider"
provider = "ollama"
args = ["-c", "model_reasoning_effort=\"low\"", "-c", "tool_output_token_limit=12000"]

[[profiles]]
agent = "opencode"
match = "model"
model = "ollama/qwen3.8:27b-mlx"
config_content = { compaction = { auto = true, reserved = 10000 } }
```

```go
// wt/internal/profiles
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

type WrapperSpec struct {
    Binary       string   `toml:"binary"`
    ArgsTemplate []string `toml:"args_template"` // "{{args}}" placeholder
}

type Store struct {
    Enabled  bool
    Profiles []Profile
}

func Load() (Store, error) // Dir()/profiles.toml, missing file = Store{Enabled: true}
```

`{{model_name}}` (and similarly `{{args}}` in a wrapper template) are the
only two template placeholders in Phase 1 — resolved from the same
`config.Route`/`config.Model` values already available at the call site, not
a general templating engine.

### 2. Capability gate per driver

Each agent driver declares which mechanisms it accepts, via a small
optional-capability interface (same pattern as `Seeder`/`Syncer`):

```go
type ProfileCapable interface {
    ProfileMechanisms() []Mechanism // subset of {Env, Args, ConfigFile, Wrapper}
}
```

`profiles.Load` (or a validation pass right after it) rejects — with a
clear error naming the offending profile — any entry whose mechanism isn't
in its agent's declared set (e.g. a `copilot` profile with `config_content`
fails at load, not silently at launch). This is a pure validation gate; the
mechanisms themselves are applied by shared, agent-agnostic code (below),
not per-driver logic.

### 3. Resolution & merge

Inserted into `agents.BuildLaunchCmd`, right after `ResolveRoute` and right
after the driver's own `Build()` returns its base `LaunchCmd`:

```go
func Resolve(store profiles.Store, agent string, route config.Route, m config.Model) ResolvedProfile
```

Filters `store.Profiles` to entries whose `agent` matches and whose tier
condition matches the route (`location == route location`, `provider ==
route.ProviderID`, `model == m.ID`), then merges in tier order
location → provider → model — later tiers overwrite same-field values from
earlier ones (per-field merge, not single-winner), producing:

```go
type ResolvedProfile struct {
    Env           map[string]string
    ExtraArgs     []string
    ConfigContent map[string]any
    Wrapper       *WrapperSpec
    Sources       []string // profile "identity" strings, for the confirm prompt / `wt profile show`
}
```

An empty `ResolvedProfile` (no matches) short-circuits everything below —
no prompt, no file writes, no wrapper substitution.

### 4. Applying a resolved profile

- **`Env`** — merged into `LaunchCmd.Env`; profile values overwrite the
  driver's own same-key defaults (the point is overriding standard
  behavior).
- **`ExtraArgs`** — appended to the driver's own args, inserted *before*
  the launch's user-supplied passthrough args, so `claude-wt -W x --
  --foo` still lets `--foo` win on collision.
- **`ConfigContent`** — mechanically different per agent:
  - **claude** → written to `<worktree>/.claude/settings.local.json`
    (project/worktree-scoped, never the global `~/.claude/settings.json`).
  - **codex** → written to `~/.codex/agent-wt-profile.config.toml` (codex
    profiles are named global files — `~/.codex/<profile>.config.toml` —
    there is no project-scoped profile mechanism to use instead), launched
    with `--profile agent-wt-profile`.
  - **opencode** → *no file.* wt already injects opencode's entire config
    via the `OPENCODE_CONFIG_CONTENT` env var per launch
    (`internal/agents`'s opencode driver); `ConfigContent` for opencode
    merges extra keys into that same JSON payload before it's serialized —
    mechanically part of the `Env` step, not a file write.
- **`Wrapper`** — replaces `LaunchCmd.Path` (and `Args[0]`) with
  `Wrapper.Binary`; the original argv is substituted into
  `Wrapper.ArgsTemplate` at the `{{args}}` placeholder. The wrapper binary
  is checked on `$PATH` the same way agent binaries already are
  (`agents.Installed`) — missing `little-coder` is a launch error, not a
  silent fallback to unwrapped `pi`.

### 5. File ownership: snapshot / overwrite / restore

For the two real files (claude's `settings.local.json`, codex's
`agent-wt-profile.config.toml`):

1. Immediately before writing, if a leftover backup already exists for this
   exact target path (see step 4), restore it first and print a one-line
   notice — this self-heals after an unclean prior exit (`kill -9`, crash)
   before doing anything new.
2. Copy the target file's current content (or record "did not exist") into
   `~/.config/agent-wt/profile-backups/<sha256(target path)>` — wt's own
   state dir, never a sibling file in the repo/worktree, so it can't be
   committed or interfere with `.gitignore`.
3. Write the profile-generated content, replacing the file entirely.
4. When the launched agent subprocess exits — success, non-zero exit, or
   Ctrl+C — restore the original content from the backup (or delete the
   file if none existed), then remove the backup entry. This is a
   `defer`-guarded step around the exec call, run immediately on subprocess
   exit, before the existing post-exit sequence (refcount release → survey
   → stop picker → summary line) begins.

**Known limitation, same class as `config.toml`'s existing whole-file
last-writer-wins caveat (issue #143):** two concurrent launches into the
*same worktree* needing different `ConfigContent` for the *same* target
file will race — not solved differently here, just documented.

### 6. Activation: global toggle + per-launch TTY confirm

- `profiles.toml`'s top-level `enabled` flag is the global off-switch
  (`wt profile on|off`); `enabled = false` skips resolution entirely — no
  matching, no prompt, no file writes.
- When `enabled` and a launch resolves a non-empty `ResolvedProfile`, and
  stdin is a TTY: prompt before applying, once per matching launch (no
  remembered choice, no new persisted state) — same convention as the
  existing replace-confirm dialog and resume prompt. Default answer is
  **Yes** (use the profile), consistent with wt's existing confirm dialogs
  defaulting toward the common case. The prompt names what would change
  (`ResolvedProfile.Sources` plus a short summary — e.g. "2 env vars, 1
  config file"), not just yes/no blind.
- Non-TTY (scripts, `wt smoke`, CI): the profile applies automatically with
  no prompt, same as every other TTY-gated wt prompt today.
- Both the TUI and non-TUI launch paths need this step — placed right
  after model resolution, before the driver's `LaunchCmd` is handed to
  `exec`, mirroring where the resume prompt and replace-confirm dialog
  already sit in each path.

### 7. CLI

Mirrors `wt litellm`/`wt config theme`:

- `wt profile list` — print every defined profile (agent, tier, what it
  sets), sourced straight from `profiles.toml`.
- `wt profile show -A <agent> [-M <model>]` — dry-run resolution: prints
  the `ResolvedProfile` that would apply right now, without launching
  anything.
- `wt profile status|on|off` — reads/flips the `enabled` flag.

### 8. Phase 1 profile content

Concrete profiles authored as part of this work (not shipped as binary
defaults — hand-added to `profiles.toml`, informed by PART 3 of the
optimization doc):

- **pi** (`match = "location"`, `location = "local"`): wrap with
  `little-coder`.
- **claude** (`match = "location"`, `location = "local"`):
  `CLAUDE_CODE_ATTRIBUTION_HEADER=0`, a `MAX_THINKING_TOKENS` cap, and
  `ANTHROPIC_DEFAULT_SONNET_MODEL`/`_HAIKU_MODEL` mapped to the resolved
  model name via `settings.local.json`.
- **codex** (`match = "provider"`, tuned per provider since Codex's
  reasoning-effort/token-limit sweet spot varies by what's actually
  serving): `model_reasoning_effort = "low"`, `tool_output_token_limit`.
- **opencode** (`match = "location"`, `location = "local"`): compaction
  tuning (`auto`, `reserved`) merged into the existing
  `OPENCODE_CONFIG_CONTENT` payload.

## Testing

- `internal/profiles`: unit tests for `Load` (parse, missing file, invalid
  mechanism-vs-capability rejection), `Resolve` (tier filtering, per-field
  merge precedence — a model-tier value overriding a location-tier value
  for the same key, a location-tier value surviving untouched for keys the
  model tier doesn't set), and the snapshot/restore helper (backup written,
  restored on simulated exit, self-heal path when a stale backup is found).
- `internal/agents`: regression tests per driver confirming
  `ProfileMechanisms()` matches what `BuildLaunchCmd` actually accepts, and
  that env/args from a resolved profile land in the right position relative
  to the driver's own values and user passthrough args.
- `cmd/wt`: TTY-prompt behavior (accept/decline/non-TTY-skip) on both the
  non-TUI and TUI launch paths, using the same seam pattern as the existing
  replace-confirm/resume-prompt tests.
- No live-server or live-process tests — matches every other `wt` seam
  convention (`startModel`, `probeInventory`, etc. are stubbed in tests).

## Open follow-ups (explicitly deferred, not in this phase)

- Shipping any profile as a wt-binary default (currently: hand-authored
  entries only).
- A `provider`-tier or `model`-tier profile for agents beyond the four
  above, and any profile for `copilot`/`shell`.
- Anything from the optimization doc that isn't representable as env vars,
  CLI args, a small config-file patch, or a binary wrapper (e.g. Claude
  Code's PreToolUse/PostToolUse hook scripts for write-guard/read-guard —
  those would need wt to seed hook *scripts*, not just settings values, and
  are a natural but separate follow-up once this mechanism is proven).
