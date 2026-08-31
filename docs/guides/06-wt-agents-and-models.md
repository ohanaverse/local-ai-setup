# wt — pick a worktree, agent, and model, then launch

> Use this to: launch claude-wt (or any `*-wt` shim) in a git worktree on a chosen model, understand what the picker offers, where rotation state lives, and which flags skip which screens.
>
> Verified against: modelman 0.1.0, wt 0.1.0, LiteLLM 1.98.0, Ollama 0.33.2 on 2026-08-29

## Prerequisites

- **Registry has models.** `~/.config/local-ai/registry.toml` is wt's model source (read-only). If it is empty or missing, create it first — see [02-providers-and-models](02-providers-and-models.md).
- **`wt` built and on PATH** (`/Users/keith/.local/bin/wt`):

  ```bash
  cd /Users/keith/github/ohanaverse/agent-worktree && go build -o /Users/keith/.local/bin/wt ./cmd/wt
  ```

  **Caveat (2026-08-29):** the installed binary is STALE — built 2026-08-27 12:15, one commit before the registry-consumer merge (`79d620f`, 2026-08-28, "wt: consume modelman's registry.toml (Phase 4)"). The rebuild command above is the fix — it builds over the PATH copy at `~/.local/bin/wt`; a GOPATH build would be shadowed (see [08-maintenance-and-troubleshooting](08-maintenance-and-troubleshooting.md) §4). Differences are cataloged in Steps §3 and Gotchas.
- **Shims on PATH** (`claude-wt` … `shell-wt` in `/Users/keith/.local/bin/`). Missing? `make install` from `/Users/keith/github/ohanaverse/agent-worktree` — verified real (copies `bin/*-wt` to `/Users/keith/.local/bin/` and re-signs on macOS to dodge AMFI `SIGKILL`/exit 137). All 7 shims present here:

  ```
  /Users/keith/.local/bin/claude-wt  /Users/keith/.local/bin/codex-wt
  /Users/keith/.local/bin/pi-wt      /Users/keith/.local/bin/shell-wt
  /Users/keith/.local/bin/agy-wt     /Users/keith/.local/bin/copilot-wt
  /Users/keith/.local/bin/opencode-wt
  ```
- **Know the model shapes** the picker will offer: [03-model-families](03-model-families.md) explains `family` and `tags`, which decide picker contents and rotation order.

## TL;DR

```bash
# from: any git repo, e.g. ~/github/ohanaverse/local-ai-setup
claude-wt                    # TUI: pick worktree/branch + model (advances rotation)
claude-wt -W my-feature      # skip worktree picker
claude-wt -W my-feature -A claude -M ollama/qwen3.8:27b-mlx   # skip everything
```

`-W` (uppercase) is the real worktree flag — confirmed by `wt --help` (both the installed binary and repo source, which agree).

## Steps

### 1. Shims and agent support matrix

Every `*-wt` shim is one line forwarding to the unified binary (`exec wt --agent <name> "$@"`). Mapping and capabilities, copied verbatim from `/Users/keith/github/ohanaverse/agent-worktree/README.md` (all 7 launcher binaries verified present in `/Users/keith/.local/bin/`; matrix semantics verified against `internal/agents/` and `internal/session/` in source):

| Launcher | Agent | Model rotation | Session resume |
|---|---|---|---|
| `claude-wt` | Claude Code | Yes | Yes |
| `codex-wt` | OpenAI Codex CLI | Yes | No |
| `copilot-wt` | GitHub Copilot CLI | Yes | No |
| `opencode-wt` | OpenCode | Yes | Yes |
| `pi-wt` | pi-coding-agent | Yes | No |
| `agy-wt` | Antigravity CLI | No | No |
| `shell-wt` | Shell command | No | No |

Interactive behaviors (key feel, resume prompt) are not driven here — see the UNVERIFIED note in Verification.

### 2. TUI flow

Run a bare `wt` (from any git repo; shims pin the agent up front, skipping the agent screen). Phases in `internal/tui/app.go`: worktree list → agent/command picker (only when no `-A` was supplied) → agent+model screen → launch.

- **Worktree picker:** scroll with `j`/`k` (or arrows), `/` to filter, `n` or selecting `+ New worktree…` to create a branch, `enter` to select, `q` or `esc` to quit.
- **Agent+model screen header** shows the agent and the active tag slot (`internal/tui/model_list.go`):

  ```
  agent : claude
  tag   : code
  ```

  The tag slot defaults to `default_tag = "code"` (see §6) unless `-T` narrows it. Model rows carry usage badges (launch counts from `usage.jsonl`), and the cursor starts on the rotation's next model (see §5).
- **On the model screen:** `j`/`k`/arrows navigate (with wrap-around), `enter` launches, `q` quits, `esc` pops back. Footer reads `[↑/↓] navigate   [enter] launch   [q] quit`.
- **Session resume:** for agents with resume (claude, opencode) the picker offers to resume the newest session or go fresh; `esc`/cancel returns to the model screen without launching or advancing rotation.
- **There is no `d` tag-toggle key.** Use `-T code` / `-T design` instead; picker footer is `[↑/↓] navigate [enter] launch [q] quit`.

### 3. Model selection

Non-interactive pinning and filtering (all verified against live `wt --help` and `cmd/wt/main.go`):

| Flag | Effect |
|---|---|
| `-M, --model <provider>/<name>` | Pin the exact model, e.g. `ollama/qwen3.8:27b-mlx` |
| `-T, --tags a,b` | Keep only models with ANY of these tags (OR within the flag) |
| `-F, --family f1,f2` | Keep only models whose family matches any listed (OR within the flag) |

Precedence when `-M` is combined with `-T`/`-F`: the tags/family filters define the *eligible list*, and the pin must be inside it — otherwise the launch errors ("model \"…\" is not in the eligible list for agent \"…\"") instead of silently ignoring the filters. So `wt -W x -T design -M <some only-code-tagged model>` fails, while a pin that satisfies the filters wins. With no `-M`: one eligible model → that model; several eligible → the rotation cursor decides (§5). This is all in `cmd/wt/resolve.go` (`resolveModelFromEligible`) and `cmd/wt/launch.go` (source-verified; this machine's catalog tags every legacy model for both code and design, so try it once tags diverge).

**Where the models come from:** `~/.config/local-ai/registry.toml` (modelman-owned) and `~/.config/agent-wt/config.toml` (wt-owned) are joined in memory by `internal/config/config.go` `Load()`. Registry providers/models are loaded **last and overwrite anything pre-existing in config.toml — registry.toml is the source of truth**, and wt never writes providers or models back (see §6). Concretely on this machine: registry has 22 models (`grep -c '^\[\[models\]\]' ~/.config/local-ai/registry.toml` → `22`), all `tags = []` so far.

**Stale-binary delta:** the installed 2026-08-27 build predates the merge and still serves its catalog from `~/.config/agent-wt/config.toml` `[[models]]` blocks (which still sit on disk as one-time migration output), not from `registry.toml`. Its picker therefore misses registry-only entries (`medgemma:27b`, `nomic-embed-text:latest`, `gpt-oss:20b`). Same flags, different catalog — rebuild (Prerequisites) before trusting the picker.

### 4. LiteLLM gateway mode

By default wt agents talk directly to Ollama (`localhost:11434`). To route
non-native traffic through the LiteLLM proxy (`localhost:4000`) and populate
the dashboard, add `[gateway]` to `~/.config/agent-wt/config.toml`:

```toml
[gateway]
mode    = "litellm"
url     = "http://localhost:4000"
api_key = "sk-…"
```

In this mode:
- wt only shows models with `litellm_exposed = true` in `modelman.toml`.
- The model name passed to agents is the registry id (e.g.
  `ollama/qwen3.8:27b-mlx`), matching LiteLLM's `model_list` entries.
- Native models (`claude/native`, `copilot/native`) still use their own
  subscriptions and are always shown.
- OpenCode continues to use Ollama directly until its OpenAI-compatible
  provider block is confirmed.

### 5. Rotation

- Launching a model **advances rotation**: the picker cursor (and any model-less launch) lands on the next eligible model after the last-launched one. There is no key or command to advance manually.
- State is a **single global file**, `~/.config/agent-wt/rotation.state`, one line, bare model id (`internal/rotation/rotation.go`: `Rotation reads and writes the single global rotation.state file.`). Old per-tag/per-agent `rotation-*.state` files are one-shot migration inputs — the newest one is folded in once and the files deleted; migration only runs when `rotation.state` is missing — it exists, so the legacy files simply remain (two legacy leftovers, `rotation-claude-code-_.state` and `rotation-pi-code-_.state`, still sat in `~/.config/agent-wt/` on 2026-08-29). See [03-model-families](03-model-families.md).
- Hidden read-only probe:

  ```bash
  wt rotate code        # from: any dir, read-only
  ```

  Observed output: `ollama/kimi-k2.7-code:cloud` — i.e. "the model after the last-launched one, walking the global list filtered to tag `code`". It prints only; it never writes state.
- `rotation.state` is written by the TUI **launch** path (`Rotation.Record()`), which also appends to `usage.jsonl` (§8). Canceled resume prompts and `esc` leave rotation untouched.

### 6. `wt config`

Live `wt config --help` lists three subcommands (plus an interactive editor when run bare):

```text
Available Commands:
  ollama      Sync ollama models between config.toml and `ollama list`
  path        Print the config directory
  theme       Manage the active color theme
```

- `wt config theme` → `list` / `show [name]` / `set <name>` / `unset`; stored in `~/.config/agent-wt/themes.toml`, effective on next launch.
- `wt config path` → prints `/Users/keith/.config/agent-wt`.
- `wt config ollama` → interactive TUI syncing models with the local Ollama instance.

`~/.config/agent-wt/config.toml` shape (wt-owned): a `default_tag = "code"` line plus `[[agents]]` entries (launch names, per-agent provider availability). **wt NEVER writes providers/models** — `wt config`'s editor only touches agents and the default tag; registry data is overwritten in-memory on every load and never persisted back (the `Load()` comment in `internal/config/config.go`: "registry.toml is the source of truth"). The `[[providers]]`/`[[models]]` blocks still present on disk are inert migration exchange output (see [00-config-map](00-config-map.md)).

### 7. Extras

- `--init` — seed `AGENTS.md` (if missing) plus an agent-specific pointer file, then exit (`internal/initseed/`).
- `--check-guard` / `--no-guard` — inspect/remove the `block-main-commit` pre-commit hook that every launch auto-installs in a git repo (blocks commits to `main`/`master`; `git commit --no-verify` is the emergency bypass). Live read-only run in `wt`-managed repo:

  ```bash
  wt --check-guard        # from: /Users/keith/github/ohanaverse/local-ai-setup
  ```

  Observed: `wt: main guard is installed in this repo.`
- `--yolo` — exists in 0.1.0, verified in `wt --help` and `cmd/wt/main.go`; prepends the agent's skip-permissions flag (claude: `--dangerously-skip-permissions`). Source-verified caveat: pi has no such flag, so `--yolo` is a no-op there (`internal/agents/agents_test.go`: "Pi has no yolo flag").
- `--cwd` — launch in the current repo root with no pickers; it skips the TUI (and its interactive resume prompt), but a prior resume-capable session for the directory is still resumed automatically at launch — `cmd/wt/launch.go:28-44` looks up the newest session for the worktree (unless the model is native) and `buildLaunch` appends the resume flag when one exists.
- `--debug-worktrees` / `--debug-session <agent>` — test helpers printing worktrees/branches and the newest claude/opencode session respectively; not for daily use, one line, moving on.
- Removed subcommands now fail loudly instead of creating a worktree named "models": `wt models` → error "wt models is removed; use wt config to view models", same shape for `wt agents` (source-verified guard in `cmd/wt/main.go`).

### 8. Launch records

Every launch appends one line to `~/.config/agent-wt/usage.jsonl` (`Rotation.Record()` → `usage.Store.Record()`). Last pre-existing record on this machine (from a launch run earlier on 2026-08-29 — note that *I did not launch anything during this guide's preparation*):

```json
{"model_id":"ollama/glm-5.3-flash:cloud","timestamp":"2026-08-29T18:33:05.297099Z"}
```

## Verification

Live-ran (2026-08-29, all read-only):

- `wt --version` → `wt 0.1.0`
- `wt --help` → full flags: `-A/-W/-M/-T/-F`, `--yolo`, `--cwd`, `--init`, `--check-guard`, `--no-guard`, `--debug-*` (output quoted throughout this guide)
- `wt --check-guard` → `wt: main guard is installed in this repo.`
- `wt rotate code` → `ollama/kimi-k2.7-code:cloud`, exit 0, no files touched
- `wt -w foo` → `wt: -w is removed; use -W or --worktree`, exit 1, and `git worktree list` confirmed nothing was created
- `wt config --help` / `wt config theme --help` / `wt config ollama --help` → subcommands as listed in §6
- Registry models present: 22 `[[models]]` entries, all `tags = []`

Model pin dry explanation (no agent launch required): `-M ollama/qwen3.8:27b-mlx` matches that exact `[[models]]` id in `registry.toml`; the join in `internal/config/config.go` makes it eligible for claude/codex/copilot/pi/opencode (it's an ollama provider model), so `claude-wt -W my-feature -M ollama/qwen3.8:27b-mlx` resolves deterministically and skips the model screen.

<!-- UNVERIFIED — interactive only; I did not launch an agent or drive the TUI during this guide. Specifically: screen-to-screen key feel, resume prompt behavior with a real claude session, `--yolo` argv actually reaching the agent, `theme set` persisting a launch, and a usage.jsonl line appended by *my* launch are code-referenced but not exercised. -->

## Gotchas

- **Model visibility is changed in modelman, not here.** wt reads `registry.toml` read-only and `wt config` never writes providers/models — add, retag, or retire models via `modelman` ([02-providers-and-models](02-providers-and-models.md)).
- **`agy-wt` and `shell-wt` have no rotation** (no model layer at all); `--yolo` is a no-op for pi.
- **codex, copilot, and pi have no session resume** — only claude-wt and opencode-wt offer the resume/fresh prompt.
- **Tag rotation is currently latent:** every registry model has `tags = []`, so `-T`-less rotation walks the whole list and the header's tag slot shows `code` without meaning a filter is active. Wire tags in modelman first ([03-model-families](03-model-families.md)).
- **Stale PATH binary:** `/Users/keith/.local/bin/wt` (2026-08-27) reads its model catalog from `config.toml`, missing registry-only models; rebuild with the Prerequisites command. After a rebuild, expect `wt rotate code`'s pair-check behavior to change until tags exist in the registry (also flagged in [03-model-families](03-model-families.md)).
- **Three hand-installed `dsh-*` shims sit alongside the wt ones:** `/Users/keith/.local/bin/` also has `dsh-headless-wt`, `dsh-tui-wt`, and `dsh-webui-wt` — unknown to wt 0.1.0, so launching one errors with `unknown agent "dsh-…"`. Present on disk but not wt-documented.
- **`-W` is the real flag.** The `-w` short form was removed and now errors with `-w is removed; use -W or --worktree`.
- **`rotation.state` is written by launches only** (`Record()`); the `wt rotate <tag>` probe and `esc`/canceled prompts never touch it. One global slot — per-tag/agent state files are legacy migration inputs.

## Going deeper

- `/Users/keith/github/ohanaverse/agent-worktree/README.md` — agents, install, guard, copilot passthrough env vars
- `/Users/keith/github/ohanaverse/agent-worktree/docs/configuration.md` and `/Users/keith/github/ohanaverse/agent-worktree/docs/wt-config.md` — config.toml and themes.toml reference
- Specs (all verified present): `/Users/keith/github/ohanaverse/agent-worktree/docs/superpowers/specs/2026-08-28-wt-registry-consumer-design.md`, `/Users/keith/github/ohanaverse/agent-worktree/docs/superpowers/specs/2026-08-14-model-registry-data-model-design.md`, `/Users/keith/github/ohanaverse/agent-worktree/docs/superpowers/specs/2026-08-28-native-unification-design.md`
- [00-config-map](00-config-map.md) — who owns which `~/.config` file; [02-providers-and-models](02-providers-and-models.md) — registry content; [03-model-families](03-model-families.md) — families/tags that feed the picker
- Launch-log consumers and spend: [07-usage-and-spend](07-usage-and-spend.md)
