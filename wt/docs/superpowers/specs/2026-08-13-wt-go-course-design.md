# Design: Go Learning Course — Rebuilding the `*-wt` launchers as a Go TUI tool

**Date:** 2026-08-13
**Status:** Approved

## Purpose

Teach intermediate Go + TUI development by progressively rebuilding the
existing bash `*-wt` launchers (in this repo, `agent-worktree`) as a single
unified Go tool named `wt`. The course is a set of markdown lesson docs the
learner reads and types code from themselves — the agent does not write the
Go code. The learner is already comfortable with Go, so lessons focus on the
new domain: Bubble Tea, config/model-registry modeling, and git/agent
integration.

Source app analyzed: the bash wrappers in `bin/` of this repo — the shared
engine `wt-core.sh` (~29KB) plus 7 per-agent wrappers (`claude-wt`, `codex-wt`,
`copilot-wt`, `pi-wt`, `agy-wt`, `opencode-wt`, `shell-wt`), the
`wt-install-guard` hook installer, and the `~/.config/agent-wt/models.conf`
rotation config + state files.

## Scope

- **One unified binary `wt`** replacing the per-agent launchers. Behavior
  preserved; flags reorganized under a cleaner CLI (cobra subcommands).
- **Hybrid model registry:** a curated, tagged list of known models (each
  with provider, location local/cloud, tags) plus live discovery from
  connected providers (e.g. `ollama list`, OpenRouter API).
- **Hybrid interactive flow:** after picking a worktree, land on an
  agent+model screen where one key rotates to the next model in the active
  tag group (default `code`) and another opens the full model browser.
- **Preserved features:** worktree/branch picker, worktree creation (incl.
  remote-tracking branches), main guard (`block-main-commit` hook),
  `--init` seeding, session resume, `--cwd`, `-w`, `--yolo`.
- **Migration** from legacy `models.conf` + rotation state files.
- **Legacy shims:** `*-wt` become thin `exec wt` shims.
- **20 leaner lessons**, focused walkthrough + small challenge each, each
  compiling at the end, committed and git-tagged.

## New Go / TUI Concepts

- cobra subcommands & flag parsing
- BurntSushi/toml config loading + typed structs + validation
- charmbracelet/bubbletea (Model/Update/View), bubbles list, lipgloss
- tag-based rotation state machine + cross-tag skip
- agent-driver interface (env-var vs `--model` model passthrough)
- shelling out to `git` (porcelain parsing) and to agent binaries
- `exec.Command` with env/args and TUI→subprocess handoff
- one-time config migration, slug computation, pre-commit hook management
- table-driven unit tests + temp-repo integration tests

## Repo Structure

```
cmd/wt/main.go          — entry point, cobra subcommand dispatch
internal/config/        — load/save config; migration from legacy files
internal/registry/      — model registry: curated + live discovery + tags
internal/agents/        — agent-driver interface + per-agent impls
internal/rotation/      — per-tag rotation state + cross-tag skip
internal/worktree/      — git worktree/branch enumeration + creation
internal/tui/           — bubbletea app: screens + keybinds
internal/guard/         — block-main-commit hook install/status/uninstall
internal/initseed/      — --init AGENTS.md / agent-pointer seeding
internal/session/       — session-resume detection
bin/*-wt                — legacy shims (exec wt ...)
docs/go-course/         — the lesson docs
```

## Lesson Plan

Full table in [`docs/go-course/00-syllabus.md`](../../docs/go-course/00-syllabus.md).
Sequence (20 lessons): CLI skeleton → config/registry → migration → live
discovery → tag rotation → agent drivers → worktree enumeration → worktree
creation → main guard → `--init` → session resume → TUI intro → worktree
list screen → agent+model screen → model browser → launch → end-to-end +
shims → testing → module split/polish → final integration.

## Design Decisions

- **Shell out to `git`** rather than using `go-git` — matches existing
  behavior, avoids re-implementing porcelain semantics.
- **Bubble Tea over raw tcell/fzf** — first-class multi-screen state machine,
  rich keybinds, no single-list limitation.
- **Config migrates once**; legacy `models.conf` remains readable but new
  format is the source of truth after first run.
- **Model invocation abstracted** behind an `agents.Driver` interface
  because each agent passes models differently (env vs `--model` vs none).

## Workflow

After each lesson:
```bash
git add -A && git commit -m "lesson NN: <title>" && git tag lesson-NN
```
