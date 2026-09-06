# llama.cpp Retirement Design — issue #33

**Date:** 2026-09-07
**Input:** GitHub issue #33 (`local.llamacpp.server` points at a GGUF that no
longer exists — `llm-restore-providers` can never succeed); Milestone A /
PR A1 Option 2 of `docs/superpowers/plans/2026-09-06-issue-roadmap.md`
**Approach (user-approved):** retire llama.cpp as a provider (the roadmap's
Option 2), extended with three user decisions:

1. **Restoration model — B (remove from active lists, restore from artifact
   doc):** llamacpp is dropped from all active wiring (benchmark arrays,
   isolation allowlist, canonical provider tuple, litellm rows, registry)
   rather than commented out or feature-flagged. Restoration is a
   copy-paste exercise from the artifact doc.
2. **Artifact format — B (real artifact files + companion guide):** the
   plist, litellm rows, and registry block are captured as real files under
   `docs/reference/artifacts/`, with one guide holding provenance, setup,
   and restore steps.
3. **Host cleanup — A (remove everything):** crash-looping LaunchAgent
   booted out and its plist moved into the repo, both llama.cpp log files
   deleted, and the Homebrew `llama.cpp` formula uninstalled. The artifact
   guide documents reinstalling the formula.

LiteLLM and oMLX remain active providers; their artifacts and setup steps
are captured in the same guide for future reference. The modelman llama.cpp
provider *code* is kept but marked unused.

**Hard rule:** no secret value may be committed. The litellm LaunchAgent
plist carries `OPENROUTER_API_KEY`, `LITELLM_MASTER_KEY`, `LITELLM_SALT_KEY`,
`UI_PASSWORD`, and `DATABASE_URL` — the artifact copy redacts all of them
with named placeholders, and the guide lists which values to re-fill from
the live plist at restore time.

---

## Findings from exploration

- The plist `local.llamacpp.server` is loaded but crash-looping (exit 1);
  its GGUF snapshot path no longer exists. `~/.llamacpp.err.log` holds
  ~90k "failed to load model" lines. This matches issue #33.
- Two hand-managed llama.cpp rows exist in `~/.config/litellm/config.yaml`
  (`llama.cpp/local-llama`, `llama.cpp/ornith-1.5-35b`), both pointing at
  `http://localhost:8080/v1`.
- `~/.config/local-ai/registry.toml` has a `llamacpp` `[[providers]]` block
  and **zero** `llamacpp/*` model rows. modelman has no `enabled = false`
  flag for providers, so "disable" means removing the block.
- `DEFAULT_PROVIDER_IDS = ("ollama", "llamacpp", "omlx")` in
  `modelman/src/modelman/registry.py` is the canonical support tuple. It
  feeds `sync.py` (`RECONCILABLE_PROVIDERS`), the TUI Add dialog
  (`screens/models.py`), `migrate.py`, and `benchmark/runner.py`
  (`LOCAL_PROVIDERS`). With no llamacpp models in registry/state, removing
  llamacpp from the tuple is a no-op for sync today and stops the TUI from
  offering llama.cpp for new models.
- `SUPPORTED_PROVIDER_IDS` in `benchmark/isolation.py` gates which providers
  `modelman benchmark agent` may isolate.
- `wt` reads `registry.toml` read-only and its gateway-matrix test fixtures
  deliberately keep llamacpp-shaped data (slash-containing ModelNames) —
  **no wt changes**.
- `docs/contracts/modelman.sample.toml` keeps a `llamacpp/legacy-contract-
  fixture` model_state row as a format fixture — **no contract changes**
  (the TOML format must still be able to express llamacpp-shaped data for
  when it returns).

---

## Phase 1 — Capture artifacts into the repo (before touching the host)

New files, all under `docs/reference/`:

| Path | Content |
|---|---|
| `artifacts/llamacpp/local.llamacpp.server.plist` | verbatim copy of the live (crash-looping) plist |
| `artifacts/llamacpp/local.llamacpp.server.plist.qwen3.8.bak` | verbatim copy of the stale sibling backup |
| `artifacts/litellm/llamacpp-model-rows.yaml` | the two llama.cpp rows removed from the live config in Phase 2 |
| `artifacts/litellm/local.litellm.proxy.plist` | copy of the live plist with all five secret env values replaced by `REDACTED-<NAME>` placeholders |
| `artifacts/omlx/homebrew.mxcl.omlx.plist` | verbatim copy (contains no secrets) |
| `artifacts/registry/llamacpp-provider.toml` | the `[[providers]]` llamacpp block removed from the live registry in Phase 2 |

New guide `docs/reference/provider-artifacts.md`, one section per provider:

- **llama.cpp (retired 2026-09-07):** why it was retired (issue #33), the
  artifact inventory, the original setup steps (from guide 01 §3), and the
  full restore procedure: `brew install llama.cpp` → `cp` plist back to
  `~/Library/LaunchAgents/` → `launchctl load -w` → re-download the pinned
  GGUF (`unsloth/Qwen3.8-27B-GGUF`, snapshot `4ca72078…`, file
  `Qwen3.8-27B-UD-Q4_K_M.gguf`) to the exact path the plist names → re-add
  the registry block, litellm rows, and code wiring (each with a pointer to
  the artifact file and the code locations that were changed in Phase 3).
- **LiteLLM (active):** artifact inventory (redacted plist + the hand-
  managed llama.cpp rows now removed from the live config), pointer to
  guide 01/04 for setup, and the redaction list.
- **oMLX (active):** artifact inventory, pointer to guide 01 and the oMLX
  reference doc.

Each section carries a "captured <date> from this host" provenance line.

## Phase 2 — Retire on the host

Run with modelman not running (no pending TUI queue):

1. `launchctl bootout gui/$(id -u)/local.llamacpp.server`
2. Move both plists into the repo artifact paths from Phase 1 (they are
   already captured; this removes them from `~/Library/LaunchAgents/` so
   login stops re-loading the broken agent).
3. `rm ~/.llamacpp.log ~/.llamacpp.err.log`
4. `brew uninstall llama.cpp`
5. Remove the two llama.cpp rows from `~/.config/litellm/config.yaml`,
   then `launchctl kickstart -k gui/$(id -u)/local.litellm.proxy`.
6. Remove the `llamacpp` `[[providers]]` block from
   `~/.config/local-ai/registry.toml`.

## Phase 3 — Disable the wiring in code (keep the implementation)

- `modelman/src/modelman/registry.py`: `DEFAULT_PROVIDER_IDS` →
  `("ollama", "omlx")`, with a comment naming the retirement date and
  pointing at `docs/reference/provider-artifacts.md`.
- `modelman/src/modelman/benchmark/isolation.py`:
  `SUPPORTED_PROVIDER_IDS` → `{"ollama", "omlx", "omlx-6bit"}`, same
  comment style.
- `modelman/src/modelman/providers/llamacpp.py`: module docstring gains an
  "UNUSED — provider disabled 2026-09-07" header pointing at the artifact
  guide. The import/registration in `providers/__init__.py` **stays** so
  the class remains loadable and its tests pass.
- `modelman/src/modelman/benchmark/runner.py`: `LOCAL_PROVIDERS` follows
  the tuple automatically — no edit beyond keeping its existing comment
  accurate.
- `benchmarks/qwen3.8-benchmark` and `benchmarks/ornith-1.5-benchmark`:
  remove llamacpp entries from `DIRECT_URLS`, `DIRECT_MODELS`,
  `LITELLM_MODELS`, `ISOLATE_ID`, `ISOLATE_ENV`, and the llamacpp
  special-case in the model-check skip. Benchmarks become three local
  backends (ollama, omlx, omlx-6bit) + OpenRouter rows.
- `bin/llm-restore-providers`: remove the llamacpp `restart_launchd` line —
  this is the change that makes the helper exit 0 again (issue #33's
  complaint).
- `bin/llm-isolate-provider`: **keep** the llamacpp branch (it is code we
  are keeping); update the header comment to mark llamacpp unused.
- `benchmarks/suites/q4-agent-sweep.toml`: remove `[routes.direct.llamacpp]`.
- Tests: update `modelman/tests/benchmark/test_isolation.py` and any test
  pinning the provider tuple; **keep**
  `modelman/tests/test_providers/test_llamacpp.py` unchanged.

## Phase 4 — Documentation

- Root `CLAUDE.md`: LaunchAgents line (drop `local.llamacpp.server.plist`),
  "Stop mechanisms" gotcha (drop the llama.cpp unload note), benchmark
  command descriptions ("4 backends" → "3"), isolation-helper description.
- `docs/guides/00-config-map.md`: TL;DR table (drop the llamacpp plist
  row), litellm config description (11 hand-managed rows → 9), the
  `launchctl list` verification output, and a pointer to the artifact
  guide. Mind the exposure-snapshot gotcha: `git grep -n "litellm_exposed" docs/guides/`
  before/after — this change removes litellm rows, so row counts in guides
  00/02/04/05/08 that mention them go stale together.
- `docs/guides/01-initial-setup.md`: §3 llama.cpp replaced by a short
  retirement note pointing at the artifact guide; title, prerequisites,
  and brew install line adjusted.
- `docs/guides/02-providers-and-models.md`, `04-litellm-config.md`,
  `05-benchmarks.md`, `08-maintenance-and-troubleshooting.md`: remove or
  mark-retire llama.cpp references; point at the artifact guide.
- `benchmarks/qwen3.8-benchmark.md`, `benchmarks/ornith-1.5-benchmark.md`,
  `benchmarks/README.md`: backend lists and example outputs updated to
  three backends; `benchmarks/results/` untouched (historical record).
- `modelman/CLAUDE.md`, `modelman/README.md`: provider-support mentions
  updated to match.
- `docs/superpowers/plans/2026-09-06-issue-roadmap.md`: Milestone A marked
  resolved — decision was retire (Option 2), pointer to the artifact guide.

## Error handling / edge cases

- `launchctl bootout` on a crash-looping label is safe: the service is not
  running, so there is nothing to interrupt; bootout only unloads the
  definition (and `RunAtLoad` would otherwise resurrect it at next login).
- LiteLLM keeps serving during the config edit; the rows being removed
  point at a dead port and cannot be serving traffic. The kickstart in
  step 5 of Phase 2 reloads the config.
- Registry edit happens with modelman closed so no pending TUI queue can
  write the llamacpp block back.

## Verification

```bash
launchctl list | grep llamacpp            # empty
ls ~/Library/LaunchAgents | grep llamacpp # empty
brew list --versions llama.cpp            # empty
ls ~/.llamacpp*.log                       # No such file
curl -s http://localhost:4000/v1/models | grep -c llama.cpp  # 0
PATH=$PWD/bin:$PATH llm-restore-providers # "providers restored", exit 0
make lint-shell                           # changed scripts parse/clean
make check-links                          # artifact guide links resolve
make test-all                             # modelman + wt green
./benchmarks/qwen3.8-benchmark 30         # smoke: 3 backends, no llamacpp rows
```

## Out of scope

- Historical `benchmarks/results/`, `docs/archive/`, and past
  plans/specs — untouched historical record.
- wt fixtures that mirror llamacpp-shaped registry data (deliberate test
  coverage for slash-containing ModelNames).
- The contract fixture's `llamacpp/legacy-contract-fixture` row.
- Issue #32 (benchmark hardening) and the rest of the roadmap — separate
  milestones.