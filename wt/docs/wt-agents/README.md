# wt-agents reference

Per-agent reference docs for the agents launched by `wt` (via the `*-wt` shims in [`bin/`](../../bin/)). Each file documents how the underlying agent is configured on disk and how it authenticates. The `*-wt` files are now thin shims that forward to `wt` (e.g. `claude-wt` → `wt --agent claude`); the launch logic lives in the Go tool (`cmd/wt/`, `internal/agents/`).

## Migrating to modelman exposure

After this update, `wt` only shows non-native models that have `litellm_exposed = true`
in `~/.config/local-ai/modelman.toml`. To make a model available in `wt`, run:

```bash
uv run modelman expose ollama/<model>
```

Native models (`claude/native`, `copilot/native`) are always shown and do not need to be exposed.

## LiteLLM proxy lifecycle

In gateway mode, `wt` routes non-native models through the LiteLLM proxy at
`:4000`. The proxy loads its model list from `~/.config/litellm/config.yaml`
**only at startup** — editing that file does not take effect until the proxy is
restarted; until then it serves a stale model list and returns
`400 Invalid model name passed in model=…` for any newly added model.

**modelman owns reconciliation.** `modelman` is the writer of `config.yaml`
(`expose`/`unexpose`, TUI `l` key) and restarts the proxy after a successful
write via the `MODELMAN_LITELLM_RESTART_CMD` env var (e.g.
`launchctl kickstart -k gui/$(id -u)/local.litellm.proxy`). `wt` does not
detect or restart the proxy — it is a launcher, not the owner of the shared
service. If a model was added by hand (or the env var is unset), restart the
proxy manually:

```bash
launchctl kickstart -k gui/$(id -u)/local.litellm.proxy
```

This affects every gateway-routed agent (claude, codex, copilot, opencode, pi),
not just one launcher — and the failure modes differ per driver: model-id
grammar mismatches, strict `ollama_chat` param mapping, and litellm bridge
bugs all surface as *agent-side* errors ("Invalid model name", "high demand",
"Model not found", …). Per-driver causes, fixes, and a debugging playbook
live in [litellm-troubleshooting.md](litellm-troubleshooting.md).

## Scope

These docs cover the agents launched by `claude-wt`, `codex-wt`, `copilot-wt`, `pi-wt`, `agy-wt`, `opencode-wt`, and `shell-wt`. The launcher contract (flags, rotation, install) lives in the Go tool — see the root [`CLAUDE.md`](../../CLAUDE.md). These per-agent docs add per-agent context (config files, auth, model selection) that does not fit there.

Gateway-mode launches have their own failure modes (model-id grammar mismatches, litellm bridge bugs) — see [litellm-troubleshooting.md](litellm-troubleshooting.md) for the driver matrix, known litellm issues, and the debugging playbook.

## Agents

Quick reference (homepage, GitHub, install): [supported-agents.md](supported-agents.md)

| Agent | File | Verification status |
|---|---|---|
| Claude Code | [claude-wt.md](claude-wt.md) | Verified on this machine, 2026-06-01 |
| OpenAI Codex CLI | [codex-wt.md](codex-wt.md) | Convention only (used on other machines) |
| GitHub Copilot CLI | [copilot-wt.md](copilot-wt.md) | Convention only (not installed on this machine) |
| pi-coding-agent | [pi-wt.md](pi-wt.md) | Verified on this machine, 2026-06-01 — includes NYT LiteLLM worked example |
| Antigravity CLI | [agy-wt.md](agy-wt.md) | Convention only (not installed on this machine) |
| OpenCode | [opencode-wt.md](opencode-wt.md) | Verified on this machine, 2026-09-02 |
| Shell command | [shell-wt.md](shell-wt.md) | Verified on this machine, 2026-07-22 |

## Verification convention

Each per-agent file ends with a "Verified on this machine" section. Verified files carry a `Verified on this machine, YYYY-MM-DD` stamp inside that section; convention-only files state non-verification explicitly. Re-verifying a file is a single-file edit, not a rename.

## Common launcher flags

The `wt` tool (and therefore every `*-wt` shim) needs three inputs to launch:
a directory, an agent or command, and (for agents) a model. Each can be
supplied via flag or picked from a TUI screen; only the agent is never
defaulted (it always comes from `-A` or the agent+command picker).

| Flag | Description |
|------|-------------|
| `-W <name>`, `--worktree <name>` | Use or create a worktree for the given branch name. For branches with slashes (e.g., `feature/my-branch`, `origin/feature`), the last path component is used as the worktree directory name (`.worktrees/my-branch`, `.worktrees/feature`). Remote tracking branches are checked out as new local branches. Skips the worktree picker. The agent+command picker still appears when `-A` is omitted, and the model picker still appears when `-M` is omitted. |
| `-A <name>`, `--agent <name>` | Pin the agent (`claude`, `codex`, `copilot`, `pi`, `agy`, `opencode`) or command (`shell`) to launch. The agent is never defaulted: when `-A` is omitted the agent+command picker is always shown. Supplying `-A` skips the picker. |
| `-M <id>`, `--model <id>` | Pin the model as `<provider>/<name>` (e.g. `claude/opus`, `ollama/gemma4:9b`). Errors if not in the eligible list. Skips the model picker. Without `-A`, the agent+command picker is shown first, then the pin is validated against the chosen agent. |
| `-T <tags>`, `--tags <tags>` | Filter the model list by tag (comma-delimited, OR within flag). |
| `-F <family>`, `--family <family>` | Filter the model list by model family (comma-delimited, OR within flag). |
| `--cwd` | Launch in the current repo root; skip the worktree picker. The agent+command picker still appears when `-A` is omitted, and the model picker still appears when `-M` is omitted. |
| `--yolo` | Skip permission prompts (agent-specific). |
| `--init` | Seed agent instruction files (AGENTS.md + agent-specific pointer if applicable) and exit. |

With no flags, `wt` presents the worktree picker, then the agent+command
picker, then the model picker (for agents). Multiple eligible models
rotate on successive launches via the slot state file. Each picker is
skipped only when its selection is already resolved: `-W`/`--cwd` skip the
worktree picker, `-A` skips the agent+command picker, and `-M` skips the
model picker. So `-W foo -A pi` (no `-M`) still shows the model picker, and
`-W foo -M claude/opus` (no `-A`) shows the agent+command picker first and
then validates the pin against the chosen agent.

The agent+command picker lists every registered agent and command. Agents
that cannot launch carry an inline indication — "not configured" (missing
from `config.toml`) or "not installed" (no binary on PATH) — and selecting
one is blocked with a clear error rather than advancing to a model screen
that can never succeed.

## Post-run summary line

After the launched subprocess exits, `wt` prints a single summary line to
**stdout** so the user can see what just ran and how long it took. The
summary fires on both success and non-zero exit; it is informational and
never changes the exit code. Both the TUI and non-TUI launch paths emit it
through a shared `agents.Summary` formatter so the two paths can't drift.

```
wt: <agent> · <model-id> · <duration>
```

- `<agent>` — the `-A` value (`claude`, `codex`, `copilot`, `pi`, `agy`,
  `opencode`, `shell`, …).
- `<model-id>` — the model's full ID (e.g. `claude/sonnet`,
  `ollama/gemma4:9b`). Omitted for command agents (`shell`), which have no
  model layer.
- `<duration>` — subprocess wall-clock time, rounded to seconds when ≥1s
  (e.g. `3m42s`, `850ms` for sub-second runs).

Examples:

```
wt: claude · claude/sonnet · 3m42s
wt: shell · 1s
```

On the TUI path the summary prints **after** the alt-screen is restored,
so the line lands on a clean line in the parent terminal rather than inside
the Bubble Tea frame.

### Legacy bash flags

The original bash launchers supported `-w`/`--worktree`, `--code`,
`--design`, and `--native`. The short flag `-w` has been removed in favor
of `-W`; the others are not supported by `wt`. The `--no-guard` and
`--check-guard` flags ARE supported by `wt` (see
[shell-wt.md](./shell-wt.md#key-flags)) — they were bash-only originally
and now work via the Go binary. Model rotation is now slot-based
(`(agent, tag, family)` — implicit on launch in the TUI, or
`wt rotate <tag>` for debugging); the main guard is managed by
`internal/guard`. The bash model-rotation and pi `models.json` auto-sync
behavior described in earlier versions of this doc has been ported to Go.
