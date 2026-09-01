# `wt` Go vs. Bash Feature Parity

## Context

The `wt` binary (`cmd/wt/`) is the Go rewrite of the original bash engine (`bin/wt-core.sh`). The bash engine is now retained only for `shell-wt`, which has no Go equivalent yet. The per-agent wrappers (`claude-wt`, `codex-wt`, etc.) are thin shims that forward to `wt --agent <name>`.

This document records the current state of feature parity between the legacy bash implementation and the Go implementation. It is intended as a reference for maintainers and as the basis for closing the remaining gaps.

## Summary

The Go rewrite covers the primary interactive and non-interactive launch paths and adds significant new capabilities: a Bubble Tea TUI, a model registry with live discovery, tag-based rotation, and per-agent launch drivers. However, several guard-related and legacy CLI behaviors are not yet ported. The highest-priority gaps are:

1. Main guard is not auto-installed on normal launches.
2. `--init` ignores `--agent` and therefore does not seed agent-specific pointer files (`CLAUDE.md`, `.github/copilot-instructions.md`).
3. There is no default-branch safety nudge when the user launches on `main`/`master`.
4. `--no-guard` and `--check-guard` flags are missing.

## Fully implemented / improved

| Bash feature | Go status | Notes |
|---|---|---|
| Worktree/branch enumeration | ✅ | `internal/worktree.Enumerate` reproduces bash: current worktree, other worktrees, local branches, remote branches; default branch is excluded from bare-branch options. |
| `-w` / `--worktree` creation | ✅ | `worktree.EnsureForName` is idempotent: reuses an existing checkout, reuses an existing `.worktrees/<name>` path, or creates a new worktree/branch. Handles branch names with slashes. |
| `--cwd` launch | ✅ | `launch(agent, root, cfg, yolo)` in `cmd/wt/launch.go`. |
| Worktree creation from picked branch | ✅ | `worktree.EnsureForBranch` handles local branches, remote-tracking branches (creates a local tracking branch), and brand-new branches. |
| Agent command building | ✅ | `internal/agents` has drivers for claude, codex, copilot, pi, agy, opencode. Each implements `Build` and `YoloFlag`. |
| `--yolo` permission skip | ✅ | `agents.Command` prepends the per-agent skip-permissions flag when `yolo` is true. |
| Session resume (claude / opencode) | ✅ | `internal/session` detects the newest resumable session by mtime; `buildLaunch`/`launchAgent` append `--resume <id>` for claude and `--session <id>` for opencode. Both TUI and non-TUI paths use it. |
| Model rotation | ✅ improved | Tag-based rotation (`internal/rotation`) generalizes `--code`/`--design`, supports arbitrary tags, cross-tag skip, and persistent state in `rotation-<tag>.state`. |
| Model registry + live discovery | ✅ new | TOML config (`~/.config/agent-wt/config.toml`), Ollama CLI discovery, OpenRouter API discovery, `wt models`, `wt agents`. |
| Model browser | ✅ new | TUI `m` key opens a `bubbles/list` picker over curated + discovered models with tag (`f`) and source (`c`) filters. |
| Legacy config migration | ✅ | `internal/config.Migrate` converts `models.conf` to `config.toml` once, preserving rotation state and merging shared models. |
| `--version` | ✅ | `main.version` printed via cobra flag. |

## Partially implemented / behavior-changed

| Bash feature | Go status | Gap |
|---|---|---|
| `--init` seeding | ⚠️ partial | `AGENTS.md` is seeded, but `cmd/wt/main.go` calls `initseed.Seed("", root)` and ignores the `--agent` flag. `claude-wt --init` therefore does **not** create `CLAUDE.md`, and `copilot-wt --init` does **not** create `.github/copilot-instructions.md`. Bash seeded the pointer file based on the wrapper's `WT_AGENT_NAME`. |
| Auto-install main guard | ⚠️ | Guard is only auto-installed inside the `--init` handler. Normal launches (`-w`, `--cwd`, TUI) do **not** call `guard.Install()`. Bash installed the guard on every launch inside a repo. |
| Default-branch warning | ❌ | Bash forced the picker and showed a warning when the only pickable entry was the current worktree on `main`/`master`, then prompted the user to proceed or cancel based on guard status. The Go TUI has no equivalent safety nudge. |
| Single-entry auto-launch | ❌ | Bash auto-launched when only one entry existed and it was the current worktree (unless on the default branch). The Go TUI always shows the worktree list. |
| `--code` / `--design` / `--native` | ❌ intentionally | Documented as unsupported. Replaced by the TUI tag toggle (`d`) and rotation (`r`). The non-TUI default model is the agent's native model or the first model in `DefaultTag`. |
| Ollama availability check | ❌ | Bash skipped cloud models not present in `ollama list` output. Go rotation selects from config without verifying that the model is currently available. |
| Agent dependency pre-flight | ❌ | Bash wrappers defined `wt_check_deps()` to verify the agent binary with a custom install hint before launch. Go only checks `exec.LookPath` at command-build time. |

## Not implemented at all

| Bash feature | Notes |
|---|---|
| `--no-guard` | Remove the guard and exit. Bash delegated to `wt-install-guard --uninstall`. |
| `--check-guard` | Read-only guard status check; exit 0 if installed, 1 if not. |
| `shell-wt` | Still implemented in bash via `wt-core.sh`. No Go equivalent exists (acknowledged in `CLAUDE.md`). |
| fzf picker fallback | Go requires a TTY for Bubble Tea. Bash worked anywhere `fzf` was installed, including from scripts and editors. |
| Wrapper-specific defaults | Bash wrappers set `WT_DEFAULT_CODE`, `WT_DEFAULT_DESIGN`, and `WT_AGENT_NAME`. Go shims forward `--agent <name>` and rely entirely on `config.toml`. |

## Recommended priority order

1. **Auto-install main guard on every launch** — restores the safety invariant that any `wt` invocation in a repo protects `main`. This is a regression from the bash engine.
2. **Make `--init` respect `--agent`** — restores per-agent pointer-file seeding so `claude-wt --init` and `copilot-wt --init` behave as they did in bash.
3. **Default-branch safety nudge** — prevents accidental work on `main`/`master` when the picker collapses to a single current-worktree entry.
4. **Add `--no-guard` and `--check-guard` flags** — restores the legacy CLI surface for guard management.
5. **Verify cloud models against ollama** — avoids launching an agent with a model that is not currently pulled.
6. **Agent dependency pre-flight with hints** — gives users actionable errors when an agent binary is missing.
7. **`shell-wt` Go equivalent** — long-term; the bash engine can remain until this is needed.

## References

- `bin/wt-core.sh` — legacy bash engine
- `cmd/wt/main.go` — Go CLI entry point
- `cmd/wt/launch.go` — non-TUI launch path
- `internal/guard/guard.go` — guard install/check/uninstall
- `internal/initseed/initseed.go` — instruction-file seeding
- `internal/tui/app.go` — TUI shell and worktree picker
- `docs/go-course/` — lessons describing the Go rewrite
