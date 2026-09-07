# Issue roadmap — GitHub issue triage and PR groupings

Date: 2026-09-06  
Context: `local-ai-setup` GitHub repo, 5 open issues (`issues.md` historical items are all FIXED).

## Executive summary

This doc maps the current open issues into a sequence of concrete milestones and pull requests.

User priority: **address #33 first** (root-cause the broken `local.llamacpp.server`), then continue in impact order while respecting dependencies. The resulting order is:

1. **Milestone A** — Restore llama.cpp as a working backend (#33).
2. **Milestone B** — Harden `modelman benchmark` so a completed run survives provider-restore failures (#32).
3. **Milestone C** — Decide and implement a single cross-tool exposure predicate (#28).
4. **Milestone D** — Extract `is_effectively_exposed()` helper in `modelman/litellm.py` (#29).
5. **Milestone E** — Add a last-model indicator to the `wt` picker (#35).

Each milestone is small enough for its own PR (or a tightly-coupled pair of PRs). The doc also flags blockers, verification steps, and what should *not* be done together.

Status (2026-09-12): all five milestones are resolved — A in #37, B in #38,
C in #39, D in #40, E in #43. Per-milestone decision records follow.

---

## Milestone A — Repair `local.llamacpp.server` (#33)

> **RESOLVED 2026-09-07 — decision: Option 2 (retire).** llama.cpp was
> disabled with artifacts preserved instead of repaired. See
> `docs/superpowers/specs/2026-09-07-llamacpp-retirement-design.md` and
> `docs/reference/provider-artifacts.md`. The text below is kept as the
> decision record.

Goal: the llama.cpp LaunchAgent starts successfully and answers `http://localhost:8080/v1/models` after `llm-restore-providers`.

### Current facts

- The plist points at a GGUF snapshot path that no longer exists:
  `~/.cache/huggingface/hub/models--unsloth--Qwen3.8-27B-GGUF/snapshots/4ca720788d1e01f1bff70c033e0d0028fd02e502/Qwen3.8-27B-UD-Q4_K_M.gguf`.
- `~/.cache/huggingface/hub` is essentially empty on this host.
- `registry.toml` has a `llamacpp` provider entry, but **no `llamacpp/*` model rows**.
- `bin/llm-restore-providers` currently hard-fails if any backend does not come back up; that behavior is intentionally left untouched in this milestone (see #32).

### PR A1: Decide and document the backend’s future

Because llama.cpp has no model rows and the GGUF is gone, simply pointing the plist at a missing file is not enough. Two options:

1. **Re-enable llama.cpp** — re-download the exact GGUF (or update the plist to an existing local GGUF) and add at least one `llamacpp/*` model row to `registry.toml` so the rest of the stack can target it.
2. **Retire llama.cpp** — remove or disable the `llamacpp` provider and the LaunchAgent, update `docs/guides/00-config-map.md` and `01-initial-setup.md`, and make `llm-restore-providers` skip an *absent* (not merely down) LaunchAgent gracefully.

Recommended: **Option 1 if the host still has disk/budget for a 16 GB GGUF; otherwise Option 2.**

### PR A2: Update the LaunchAgent plist and registry

If Option 1:

- Download or locate a working Qwen3.8-27B GGUF for llama.cpp (e.g. `unsloth/Qwen3.8-27B-GGUF:Qwen3.8-27B-UD-Q4_K_M.gguf` as originally used, or another compatible file already on disk).
- Update `~/Library/LaunchAgents/local.llamacpp.server.plist` `ProgramArguments` `-m` path to that file.
- Add a `llamacpp` model row to `~/.config/local-ai/registry.toml` (e.g. `llamacpp/qwen3.8:27b-gguf`) with `provider_id = "llamacpp"` and the correct `model_name`.
- Document the exact GGUF and registry row in `docs/guides/01-initial-setup.md` §3 and `docs/guides/00-config-map.md`.
- Add a contract test / fixture update if the modelman registry contract changes.

If Option 2:

- `launchctl bootout gui/$(id -u)/local.llamacpp.server.plist` and remove or disable the plist file.
- Remove the `llamacpp` provider from `~/.config/local-ai/registry.toml` **or** mark it `enabled = false` if modelman supports that flag.
- Update `modelman/src/modelman/benchmark/isolation.py` `SUPPORTED_PROVIDER_IDS` and `LOCAL_PROVIDERS` in `runner.py` to drop `llamacpp`.
- Update benchmark scripts in `benchmarks/` to remove the `llamacpp` backend (or mark it N/A).
- Update `docs/guides/00-config-map.md` and `01-initial-setup.md` to reflect that llama.cpp is no longer active.

### Verification for Milestone A

```bash
# 1. Plist syntax / launchd state
launchctl list | grep llamacpp

# 2. llama.cpp answers health endpoint
launchctl kickstart -k "gui/$(id -u)/local.llamacpp.server"
curl -s --max-time 5 http://localhost:8080/v1/models | head -c 200

# 3. Restore helper no longer fails on llama.cpp
PATH=$PWD/bin:$PATH llm-restore-providers   # must print "providers restored", exit 0

# 4. Any new registry row appears in modelman / wt
uv run modelman list
wt list --models   # or equivalent
```

### Out of scope for Milestone A

- Do **not** change `llm-restore-providers`’s failure behavior; that is #32.
- Do **not** add new benchmark features; only ensure llama.cpp is a *viable* backend again.

---

## Milestone B — Never discard a completed benchmark run (#32)

> **RESOLVED 2026-09-12 — PR #38.** The save-then-surface pattern was ported
> to the single-turn runner: results are written before provider restore, and
> a restore failure raises `RunSavedButRestoreFailed` so the run file and the
> `--latest` pointer survive. Text below is kept as the decision record.

Goal: if a benchmark workload finishes but provider restore fails, the run file is still written and a clear error names the surviving directory.

### Rationale

A completed measurement is more valuable than a housekeeping step. The current `modelman/src/modelman/benchmark/runner.py` calls `restore_providers()` inside the `finally` and then writes results *after* it, so a restore exception discards the run. The agent-benchmark runner already solved this (`RunSavedButRestoreFailed`).

### PR B1: Port the agent runner’s save-then-surface pattern to the single-turn runner

Changes in `modelman/src/modelman/benchmark/runner.py`:

- Run `restore_providers()` after writing results.
- If restore fails, capture the error, not raise immediately.
- Write results via `write_results(run, results_dir)` unconditionally (or within the original try before restore).
- After results are persisted, if restore failed, raise a `RunSavedButRestoreFailed` exception carrying `run_dir` so the CLI can still update its `--latest` pointer and report the row count.
- Update CLI handling to catch `RunSavedButRestoreFailed`, print the directory, and exit non-zero.

### Verification for Milestone B

```bash
# Repro: intentionally break a provider restore
launchctl unload ~/Library/LaunchAgents/local.llamacpp.server.plist
uv run modelman benchmark run --suite <some-short-suite>
# Expect: results file exists, exit non-zero with message naming the run directory.

# Then restore real state and confirm normal path still works
launchctl load -w ~/Library/LaunchAgents/local.llamacpp.server.plist
PATH=$PWD/bin:$PATH llm-restore-providers
uv run modelman benchmark run --suite <suite>   # exit 0, latest pointer updated
```

### Dependency

Milestone A is not strictly required for the code fix, but it is required for *verifying* the fix on this host. Sequence: A first, then B.

---

## Milestone C — Resolve wt-vs-TUI exposure divergence (#28)

> **RESOLVED 2026-09-12 — PR #39, Option 1 (align `wt` to the TUI).** The
> predicate is implemented in Go (`wt/internal/config`) and pinned by
> contract fixtures; the divergence note in `docs/guides/00-config-map.md`
> was removed in the same PR. Text below is kept as the decision record.

Goal: pick one definition of “is this model exposed/available?” and apply it consistently across `modelman` TUI and `wt`.

### The decision

Three options from the issue:

1. **Align `wt` to the TUI** — `wt` filters by `litellm_exposed AND (ready OR cloud)`. Requires a Go implementation of the predicate (mirroring #29) and a contract fixture in `docs/contracts/`.
2. **Align the TUI to `wt`** — TUI `EXPOSED` goes back to flag-only. Loses the ready signal.
3. **Keep divergence documented** — no code change beyond cross-references.

Recommended: **Option 1** if llama.cpp/oMLX/ollama readiness is important for `wt` callers (it is). A user invoking a model through `wt` should not be offered a local model whose files are not actually present.

### PR C1: Add a Go contract for the effective-exposure predicate

- Define the predicate in `wt/internal/config/exposed.go` or similar.
- Source the cloud-provider list from `docs/contracts/modelman.sample.toml` and/or `registry.toml` `location = "cloud"`.
- Update `wt` picker filtering to use it.
- Update `docs/contracts/modelman.sample.toml` and `modelman/CLAUDE.md` to cross-reference the rule.
- Add/update Go tests in `wt/internal/config` and picker tests.

### PR C2 (parallel): Keep the TUI rule intact but remove the divergence note

- Once `wt` matches the TUI, remove the “deliberate divergence” note from `docs/guides/00-config-map.md`.
- Optionally rename the column/help text to clarify it means “effectively exposed.”

### Verification for Milestone C

```bash
# Go tests
cd wt && go test ./internal/config ./cmd/wt

# Manual: set a local model exposed but not ready, ensure wt no longer offers it
uv run modelman expose <local-model-id> --no-apply   # or equivalent
wt list --models   # model should not appear in active list
```

---

## Milestone D — Extract `is_effectively_exposed()` helper (#29)

> **RESOLVED 2026-09-12 — PR #40.** `is_effectively_exposed` is the single
> canonical predicate in `modelman/src/modelman/litellm.py`, used by the TUI
> EXPOSED column and the validation gate. See also
> `docs/superpowers/specs/2026-09-12-is-effectively-exposed-design.md`. Text
> below is kept as the decision record.

Goal: one canonical Python implementation of the predicate, used by the TUI, apply gate, and CLI expose/unexpose paths.

### Rationale

This refactor should follow Milestone C so the helper encodes the agreed semantics, including the cloud exemption and the queued-exposure handling.

### PR D1: Extract and wire up the helper

- Add `is_effectively_exposed(model_id, registry, state, queued_exposed=None)` in `modelman/src/modelman/litellm.py`.
  - Base predicate: `litellm_exposed` and (`ready` or `is_cloud(provider_id)`).
  - `queued_exposed` optional: if provided, use it instead of the persisted flag (for the TUI live display before apply).
  - Return `False` for unknown ids (loose variant); let `_validated_entry` raise `ExposeError` where strictness is needed.
- Replace the ad-hoc predicates in:
  - `modelman/src/modelman/screens/models.py` EXPOSED column display.
  - `modelman/src/modelman/litellm.py` `_validated_entry`.
  - `apply_expose_queue` / CLI expose/unexpose paths.
- Add/extend unit tests; ensure the existing `test_exposed_column_requires_ready_but_exempts_cloud` still passes unchanged.
- Run the full suite: `cd modelman && make test`.

### Verification for Milestone D

```bash
cd modelman
make check
make test
# confirm 597 existing tests still pass
```

---

## Milestone E — Last-model-selected indicator in `wt` (#35)

> **RESOLVED 2026-09-12 — PR #43.** The picker marks the last-launched row
> with a `▶` prefix, reusing `rotation.state` instead of the sketched
> `last-model` file. See
> `docs/superpowers/specs/2026-09-12-wt-last-model-indicator-design.md`.
> Text below is kept as the decision record.

Goal: in the `wt` model picker, visually indicate which model was used in the previous session.

### PR E1: Persist and display the last selected model

- Add a small state file (e.g. `~/.config/agent-wt/last-model`) written when a model is selected and launched.
- In the picker, mark the matching row with an indicator (e.g. `*` or `>` in the table, or footer text).
- If no prior selection exists, show nothing.
- Add/update Go tests for the state read/write and picker rendering.

### Verification for Milestone E

```bash
cd wt && go test ./...
# Manual: launch wt, pick a model, quit, reopen picker — selected model is indicated.
```

---

## Sequence summary

| Order | Milestone | Issue | PR(s) | Landed | Why this order |
|------|-----------|-------|-------|--------|----------------|
| 1 | A | #33 | A1, A2 | #37 | User-requested root cause. Blocks reliable verification of B. |
| 2 | B | #32 | B1 | #38 | Data-loss bug; safe to implement once A removes the environmental trigger. |
| 3 | C | #28 | C1, C2 | #39 | Semantic decision must precede helper extraction. |
| 4 | D | #29 | D1 | #40 | Refactor that encodes the agreed semantics. |
| 5 | E | #35 | E1 | #43 | Pure UX polish, no dependencies. |

## Cross-cutting concerns

1. **Tests must pass at every step.** Use `make test-all` at repo root as the gate before each PR merge.
2. **Docs drift.** Any change to registry model rows, provider availability, or exposure semantics must update `docs/guides/00-config-map.md`, `01-initial-setup.md`, and the relevant package CLAUDE.md files. Use `make check-links` after doc edits.
3. **Isolation contract.** If `llamacpp` is retired (Option 2 in Milestone A), `bin/llm-isolate-provider` and `modelman/src/modelman/benchmark/isolation.py` must be updated in the same PR so modelman and the bash benchmarks agree on supported backends.
4. **No secret leaks.** If the plist is edited or shown in docs, redact real keys; follow the secret-handling rules in `docs/guides/00-config-map.md` and `01-initial-setup.md:509`.

## Open questions before starting Milestone A

1. Does this host still want llama.cpp as a first-class backend (Option 1), or should it be retired (Option 2)?
2. If re-enabling, which exact GGUF file should the plist reference (download `unsloth/Qwen3.8-27B-GGUF` again, or use an existing local GGUF)?
3. What should the `llamacpp/*` model id and `model_name` be in `registry.toml` so it matches the LiteLLM `model_list` entry?

Once these three questions are answered, Milestone A can proceed.
