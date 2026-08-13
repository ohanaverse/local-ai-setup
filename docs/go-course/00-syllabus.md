# Go Course: Rebuilding the `*-wt` launchers as a unified `wt` TUI tool

This course teaches intermediate Go and TUI development by progressively
rebuilding the bash `*-wt` launchers (worktree/branch pickers that launch AI
coding agents) as a single unified Go binary called `wt`. It is written for
someone who already knows Go, so each lesson focuses on the *new* domain —
Bubble Tea, config/model-registry modeling, git integration, and launching
agent subprocesses — rather than basic Go syntax.

**How it works:** each lesson is a markdown doc with a worked code walkthrough
you type into your own module, plus a small optional challenge. At the end of
each lesson you commit and tag your work, so you always have a checkpoint to
fall back to.

**Setup:** before lesson 1, this repo has the existing `bin/` bash wrappers
and `docs/`. Lesson 1 runs `go mod init` at the repo root to create the Go
module; the bash wrappers remain in place until lesson 17 turns them into
thin `exec wt` shims.

**Source material:** the existing bash engine lives in `bin/wt-core.sh`
(~29KB) with per-agent wrappers in `bin/`. The legacy model config is at
`~/.config/agent-wt/models.conf` plus `rotation-{code,design}.state` files.
You can read those files as the "reference implementation" while porting each
feature to Go.

## Lessons

| # | Lesson | New/Review Concepts | Maps to feature |
|---|---|---|---|
| 1 | [Module init & CLI skeleton](lesson-01-module-init-and-cli.md) | `go mod init`, cobra subcommands, flag parsing, exit codes | `wt` / `wt models` / `wt agents` dispatch |
| 2 | [Config & model registry data model](lesson-02-config-and-registry.md) | BurntSushi/toml, typed structs (provider, location, tags), validation | config + registry core |
| 3 | [Migration from legacy config](lesson-03-legacy-migration.md) | reading `models.conf` + rotation state files, one-time migrate | backward compat |
| 4 | [Live model discovery](lesson-04-live-discovery.md) | `exec` to `ollama list`, HTTP fetch to OpenRouter, merge curated+discovered | hybrid registry |
| 5 | [Tag-based rotation](lesson-05-tag-rotation.md) | rotation state per tag group, cross-tag skip, `Rotation.Next()` | `--code`/`--design` |
| 6 | [Agent driver abstraction](lesson-06-agent-drivers.md) | interface, per-agent model passthrough (env vs `--model`), default map | launch engine |
| 7 | [Worktree & branch enumeration](lesson-07-worktree-enumeration.md) | `git worktree list --porcelain`, `for-each-ref`, local/remote dedup | picker data |
| 8 | [Worktree creation](lesson-08-worktree-creation.md) | `git worktree add`, remote-tracking branches, safe dir names | `-w`, branch pick |
| 9 | [Main guard](lesson-09-main-guard.md) | pre-commit hook install/status/uninstall, `--git-common-dir` | `--no-guard`/`--check-guard` |
| 10 | [`--init` seeding](lesson-10-init-seeding.md) | AGENTS.md + agent-pointer files, skip-if-exists | `--init` |
| 11 | [Session resume](lesson-11-session-resume.md) | claude/opencode session detection, slug computation, mtime ranking | resume |
| 12 | [TUI intro](lesson-12-tui-intro.md) | bubbletea Model/Update/View, messages, keybinds | app shell |
| 13 | [Worktree list screen](lesson-13-worktree-screen.md) | bubbles list, columns, filtering, Enter to select | port of picker |
| 14 | [Agent+model screen](lesson-14-agent-model-screen.md) | rotate-by-tag (one key), agent switching, "launch" | hybrid flow |
| 15 | [Model browser](lesson-15-model-browser.md) | filterable/taggable list, metadata columns, search | interactive browse |
| 16 | [Launching the agent](lesson-16-launch-agent.md) | `exec.Command` with env/args, yolo flag, TUI→subprocess handoff | the actual exec |
| 17 | [End-to-end flow + legacy shims](lesson-17-end-to-end-shims.md) | wiring screens, `*-wt` → `exec wt` shims | unified tool |
| 18 | [Testing](lesson-18-testing.md) | unit tests (registry/rotation/config), temp-repo integration tests, TUI test helpers | quality |
| 19 | [Module split & polish](lesson-19-module-split.md) | `internal/` layout, error handling, help text | maintainability |
| 20 | [Final integration](lesson-20-final-integration.md) | config-edit flow, real `ollama`/agent run, docs | done |

## Dependencies

| Purpose | Module |
|---|---|
| CLI subcommands | `github.com/spf13/cobra` |
| Config parsing | `github.com/BurntSushi/toml` |
| TUI | `github.com/charmbracelet/bubbletea` |
| TUI components | `github.com/charmbracelet/bubbles` |
| Styling | `github.com/charmbracelet/lipgloss` |
| Test temp dirs | `github.com/google/go-cmp` (optional, for diffs) |

## Workflow

After finishing each lesson:
```bash
git add -A && git commit -m "lesson NN: <title>" && git tag lesson-NN
```
