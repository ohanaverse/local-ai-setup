# Follow-up issues

Collected at final review of the user-guide set, 2026-08-29. Items are also
documented in context in `docs/guides/` (references below); this file tracks
the actions. Check off and add notes as items close.

---

## 1. Rotate the LiteLLM master key (security) — FIXED (2026-08-29)

`sk-litellm-<rotate-me>` in `docs/reference/litellm-admin-ui-setup.md`
(§"what was actually set") **is the live proxy master key** — verified against
the plist's `LITELLM_MASTER_KEY` during final review. Committed since the
initial commit; present in git history. Localhost-bound, but anyone with repo
read access can control the proxy.

- [x] Rotate: new key value in `~/Library/LaunchAgents/local.litellm.proxy.plist`
      (`LITELLM_MASTER_KEY`; also `UI_PASSWORD`/`LITELLM_SALT_KEY` worth rotating),
      then `launchctl kickstart -k gui/$(id -u)/local.litellm.proxy`
- [x] Redact `docs/reference/litellm-admin-ui-setup.md` (placeholders, not real values)
- [x] Decide on history: rewrite (git filter-repo) or accept (localhost exposure)
- [x] Audit other docs copied from that session for the same class of leak

Rotated 2026-08-29; redacted in docs; history accepted.

Secret-handling rules live in: `docs/guides/00-config-map.md` (Gotchas,
"Secrets on disk"), `docs/guides/01-initial-setup.md:509`, `04-litellm-config.md`
(literal api_key gotcha).

## 2. Fix agent-worktree README drift — FIXED (2026-08-29)

Three spots in `~/github/ohanaverse/agent-worktree/README.md` are stale vs
wt 0.1.0 (both the installed binary and repo source at `bf98d14`):

- `:75` — `--cwd` claims "skip TUI and session resume"; actually skips only the
  interactive prompt — a prior session still auto-resumes (`cmd/wt/launch.go:28-44`,
  resume flag appended in `internal/agents/agents.go:287-288`)
- `:119` — `d` "toggle between code and design tag groups" has no handler in any
  TUI source version; picker footer has no `[d]`
- `:120` — rotation state is the single global `rotation.state`; per-tag
  `rotation-<tag>.state` files are legacy migration inputs (deleted after)
- also `:47`/`:74` — `-w` short form removed; `-W` is the real flag

- [x] Fix the README lines in the agent-worktree repo
- [x] Then prune the corresponding caveats: `docs/guides/03-model-families.md:233`,
      `docs/guides/06-wt-agents-and-models.md:36, :71, :130, :171`

README fixed in agent-worktree PR; caveats pruned in commit 271455e.

## 3. Converge `litellm_exposed` bookkeeping — FIXED (2026-08-29)

All 11 `model_list` entries in `~/.config/litellm/config.yaml` predate
modelman's flag bookkeeping; `modelman.toml` has zero `litellm_exposed = true`.
The guides treat `config.yaml` as routing truth and document the drift
(`02:264`, `04:286`, `08:176`).

- [x] Re-register the 9 non-registry models in modelman (or decide they stay
      hand-managed) — currently `registry.toml` carries only the `ollama` provider
- [x] `uv run modelman expose <id>` for each in-registry model (`ollama/qwen3.8:27b-mlx`,
      `ollama/ornith-1.5:35b`) so flag state matches disk
- [x] Downshift the drift notes in guides 02/04/08 to a historical footnote

Two in-registry ollama models exposed; 9 foreign entries declared hand-managed; drift notes downshifted.

## 4. Fix broken links from the archive move — FIXED (links); script widening FIXED (2026-08-29)

The move of `docs/Local AI Setup 2026-08-25.md` → `docs/archive/` (commit
`5980f0a`) left five stale references. The plan's link-check script only
scanned `README.md`, `CLAUDE.md`, and `docs/guides/*.md`, so these were missed:

- `benchmarks/README.md:60` — `../docs/Local AI Setup 2026-08-25.md` (404)
- `benchmarks/ornith-1.5-benchmark.md:158` — same link (404)
- `benchmarks/qwen3.8-benchmark.md:314` — same link (404)
- `docs/archive/Local AI Setup 2026-08-25.md:639` — `../benchmarks/qwen3.8-benchmark.md`
  now resolves to `docs/benchmarks/` (nonexistent); needs `../../benchmarks/`
- `docs/reference/litellm-admin-ui-setup.md:4` — plain-text path
  `docs/Local AI Setup 2026-08-25.md` (not a link, so the checker never flagged it)

- [x] Repoint the three `benchmarks/` links to `../docs/archive/Local AI Setup 2026-08-25.md`
- [x] Fix the archive doc's own `../benchmarks/` → `../../benchmarks/`
- [x] Update the plain-text path in `litellm-admin-ui-setup.md:4`
- [x] Widen the link-check script to cover `benchmarks/`, `docs/reference/`, `docs/archive/`
      (the script is an inline snippet in `docs/superpowers/plans/2026-08-29-user-guides.md`
      §"Verification tooling" — no standalone file; widen its `targets` list or extract it)

Link-check script widened to benchmarks/docs/reference/docs/archive.

## 5. Reconcile the wt rebuild command across guides — FIXED (2026-08-29)

Guides 03 and 06 present `go build -o "$(go env GOPATH)/bin/wt"` as "the fix"
for the stale wt binary, but guide 08 §4 (verified live) shows that command
leaves the stale binary in charge: `go env GOPATH` is asdf-managed
(`/Users/keith/.asdf/installs/golang/1.26.7/packages`), its `bin/` holds no
`wt`, and `~/.local/bin` shadows it on PATH. The real fix is
`go build -o /Users/keith/.local/bin/wt ./cmd/wt`.

- `docs/guides/06-wt-agents-and-models.md:13` — "The rebuild command above is the fix"
- `docs/guides/03-model-families.md:234` — same ineffective command
- `docs/guides/08-maintenance-and-troubleshooting.md:345, :473` — the correct story

- [x] Rewrite the rebuild command in guides 03/06 to build over `~/.local/bin/wt`
      (or point at guide 08 §4 instead of restating the GOPATH command) — also
      fixed guide 08's own code block (`:332`) and intro (`:326`), which still
      showed the GOPATH command; the gotchas at `:345`/`:473` now read as the
      "why" behind the corrected command.

## Minor / already handled notes

- wt PATH shadowing (stale `~/.local/bin/wt` hides GOPATH builds): documented in
  `docs/guides/08` §4 — close by rebuilding over the PATH copy or aliasing.
- Machine-specific expected outputs (model counts, PIDs, checkout SHAs) in the
  guides are dated stamps and hedged as drift-aware — accepted format property.
- Health-check endpoint drift: the canonical block (README:37-38, guide 08:20-21)
  probes omlx/llama.cpp via `/health`, while guides 01/04/05 use `/v1/models` for
  the same backends. Both return 200, but the two blocks converged on one
  endpoint. [x] Fixed 2026-08-29.
- Guide 05:96 wording: "they currently mirror per-model tags" was changed to
  "per-model ids" — the examples (`qwen3.8:27b-mlx`, `ornith-1.5:35b`) are model
  ids, not the separate `tags` registry field. [x] Fixed 2026-08-29.Skipped Task 7: agent-worktree README PR not yet merged into main.
