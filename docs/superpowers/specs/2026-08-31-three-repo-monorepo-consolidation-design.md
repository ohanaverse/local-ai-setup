# Three-Repo Monorepo Consolidation

**Cross-repo design.** Affects all three repos in the local-ai-setup system:
`local-ai-setup` (becomes the surviving monorepo), `modelman` (merged in as a
subdirectory, then archived), and `agent-worktree` (merged in as a
subdirectory, then archived). This spec should be read before opening any PR
in `modelman` or `agent-worktree` once the migration starts — those repos are
going away as independent push targets.

## Why

Model management across this system spans three repos with a deliberate
split of responsibility (see `modelman/docs/superpowers/specs/2026-08-27-shared-model-registry-design.md`
and the cross-repo tracker at
`agent-worktree/docs/superpowers/plans/2026-08-27-model-management-consolidation-status.md`):
`modelman` (Python) owns the model/provider registry and LiteLLM exposure,
`wt`/`agent-worktree` (Go) reads that registry read-only and owns worktree/agent
launch, and `local-ai-setup` (bash) owns infra (LaunchAgents, LiteLLM proxy,
benchmarks) plus the user-facing docs spanning all three.

That split is sound and is **not** changing. The problem is narrower: when a
shared file format changes (`registry.toml`, `usage.jsonl`/`rotation.state`,
LiteLLM's `model_list`), there is no mechanism that tells you the change has
a consequence in another repo. Today that signal exists only as a
hand-maintained tracker doc and human memory. None of the three repos has
CI, so even within one repo nothing catches a break automatically — across
repos it's worse, since a Go build breaking because a Python-owned schema
changed is invisible until someone runs `go test` in the other repo by hand.

External constraints ruled out during brainstorming: none of the three repos
is used or published independently of the other two, so there is no
audience to preserve by keeping them separate.

## Non-goals

- **Not** rewriting `wt` or `modelman` into the same language. Both TUIs are
  mature (Textual screens/queue/forms/benchmark/usage in modelman; cobra/TUI/
  codesign/rotation/guard in wt) and a rewrite of either is a much larger
  cost than the problem being solved. If the dual-implementation of shared
  schemas becomes the dominant pain later, revisit as its own spec.
- **Not** changing the ownership split (modelman owns registry/exposure, wt
  is a read-only consumer, local-ai-setup owns infra). That boundary is
  working; only the lack of a cross-boundary safety net is being fixed.
- **Not** deleting the old repos. They are archived (read-only) so existing
  PR/issue history and any external links stay resolvable.

## Architecture

Target repo: `local-ai-setup` (already frames itself as the system's entry
point). `modelman` and `agent-worktree` merge into it as subdirectories,
each keeping its own toolchain root:

```
local-ai-setup/                (existing repo)
├── modelman/                   ← was modelman repo root (pyproject.toml, src/, tests/)
├── wt/                         ← was agent-worktree repo root (go.mod, cmd/, internal/)
├── docs/
│   ├── contracts/               ← NEW: shared fixture files both languages test against
│   ├── guides/, reference/, archive/, superpowers/   (unchanged)
├── .github/workflows/           ← NEW: path-filtered CI
├── bin/, benchmarks/            (unchanged)
├── Makefile                     (unchanged; gains optional aggregate targets)
└── CLAUDE.md
```

`modelman/pyproject.toml` and `wt/go.mod` remain valid project roots for
`uv`/`go` from their subdirectory — neither toolchain requires living at a
git repo root. The one required rename: the Go module path
`github.com/ohanaverse/agent-worktree` → `github.com/ohanaverse/local-ai-setup/wt`,
which touches every internal import under `wt/**/*.go`.

## Migration mechanics

From a fresh clone of `local-ai-setup`, one `git subtree add` per source
repo (built into git — no `git-filter-repo` install needed):

```bash
git subtree add --prefix=modelman git@github.com:ohanaverse/modelman.git main
git subtree add --prefix=wt        git@github.com:ohanaverse/agent-worktree.git main
```

Each is a single merge commit with full source history underneath it
(`git log --follow modelman/src/modelman/registry.py` still walks back
through modelman's original commit history).

Follow-up mechanical steps, as separate commits after both subtree merges:

1. **Go module rename** — `wt/go.mod` module path plus every `internal/...`
   import across `wt/**/*.go`. Verify with `go build ./... && go test ./...`
   from `wt/`.
2. **Path-sensitive docs/scripts** — the `go build -o ~/.local/bin/wt ./cmd/wt`
   rebuild command in guides 03/06/08 (per `issues.md` item 5) becomes
   `./wt/cmd/wt`. Grep both merged trees for any other repo-root-relative
   assumption (runtime config dirs like `~/.config/agent-wt` are unaffected —
   they're not source-tree-relative — but confirm during implementation).
3. **Archive old repos** — mark `modelman` and `agent-worktree` read-only on
   GitHub; replace each README with a one-line pointer to
   `local-ai-setup`'s new subdirectory.

Pre-merge checks on each source repo (see Risks below): no unmerged
branches, no open `.worktrees/`, no uncommitted changes, and a quick
`git log -p` scan for anything secret that hasn't already been through the
same rotate/accept decision `local-ai-setup` made in `issues.md` item 1.

## CI and the contract-test mechanism

New `.github/workflows/`, each path-filtered so it only runs when relevant:

| Workflow | Triggers on | Runs |
|---|---|---|
| `modelman-ci.yml` | `modelman/**`, `docs/contracts/**` | `uv sync && make check && make test` in `modelman/` |
| `wt-ci.yml` | `wt/**`, `docs/contracts/**` | `go build ./... && go vet ./... && go test ./...` in `wt/`, on `macos-latest` (codesign step is macOS-only) |
| `shell-ci.yml` | everything else (`bin/**`, `benchmarks/**`, `docs/**`) | `make lint` (shellcheck + check-links) |

The load-bearing piece is `docs/contracts/`: fixture files exercising every
schema variant in the shared formats —

- `registry.sample.toml` — both provider auth types, all three `Cost` kinds,
  `model_info`, a per-model `location` override.
- `usage.sample.jsonl` + `rotation.sample.state` — the wt-write/modelman-read
  usage format.

Each side gets a test that loads the *same file* by relative path and
asserts specific decoded values (not just "parses without error"):

- `wt/internal/config/registry_fixture_test.go` reads `../docs/contracts/registry.sample.toml`
- `modelman/tests/contracts/test_registry_fixture.py` reads `../../docs/contracts/registry.sample.toml`

Because `docs/contracts/**` is in both `modelman-ci` and `wt-ci`'s trigger
paths, editing a fixture (which you'd do when changing the schema) runs
*both* test suites in the same PR. That's the mechanism that replaces the
hand-maintained cross-repo tracker doc: the signal is now automatic and
enforced, not remembered.

## Risks and edge cases

- **In-flight work in source repos** — both `modelman` and `agent-worktree`
  have `.worktrees/` directories from active use of `wt` to develop them.
  Before subtree-merging: `git status`, `git worktree list`, and
  `git branch -vv` (for unmerged branches) in each, and land or explicitly
  document anything outstanding first.
- **In-flight cross-repo feature** — `agent-worktree/docs/superpowers/plans/2026-08-30-wt-litellm-gateway.md`
  may still be active; confirm it's landed or explicitly paused before
  merging, since mid-merge is a bad time to also be mid-feature across the
  boundary being removed.
- **Secrets in history** — `local-ai-setup` already has an accepted
  (rotated, not scrubbed) secret exposure from `issues.md` item 1. Unrelated
  to this merge, but do a quick history scan on `modelman`/`agent-worktree`
  before pulling their history in, so nothing new gets merged in unexamined.
- **macOS CI runner cost** — `wt-ci.yml` needs `macos-latest`, billed at a
  higher per-minute multiplier than Linux runners. Non-issue at solo/personal
  usage volume, but worth knowing.
- **No auto-redirect on archived repos** — GitHub doesn't redirect an
  archived repo's clone URL. Local directories keep working (just stop
  pushing); a fresh clone of the old URL lands on a frozen snapshot with a
  pointer README.

## Testing / verification plan

1. After each `git subtree add`: diff the new subdirectory's tree against
   the source repo's original HEAD (`git diff <source-tip> HEAD:modelman/`)
   — must be identical before any renaming touches it.
2. After the Go module rename: `cd wt && go build ./... && go vet ./... && go test ./...` — full pass required.
3. After Python-side path settling: `cd modelman && uv sync && make check && make test` — full pass required.
4. Root: `make lint` (shellcheck + check-links) — confirms no doc links broke.
5. New contract tests: add fixtures + both sides' tests; confirm both fail
   identically when the fixture is deliberately corrupted, proving the test
   reads the shared file rather than a stale copy.
6. End-to-end smoke test: rebuild `wt` from its new path, confirm
   `claude-wt --version` and a real launch still work; launch the `modelman`
   TUI; run the guides' 60-second health check block unchanged.
7. CI smoke test: open a throwaway PR touching only
   `docs/contracts/registry.sample.toml` and confirm both `modelman-ci` and
   `wt-ci` actually trigger on it.
