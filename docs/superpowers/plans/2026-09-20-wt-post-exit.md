# wt Post-Exit Flow Implementation Plan (revised after review)

> **For agentic workers:** Use superpowers:executing-plans or subagent-driven-development. Steps use `- [ ]` checkboxes.

**Goal:** (#115) offer to stop running local models nobody else is using when wt exits; (#116) skip the survey for native models.

**Spec:** `docs/superpowers/specs/2026-09-20-wt-post-exit-design.md` (see its "Revisions" section — this plan supersedes the spec where they differ).

**Decisions (confirmed with the user):**
1. Order: `survey → stop picker → summary → survey stats → pricing notice` (summary deferred until after interaction).
2. Native = existing `config.Model.Native` (registry-derived: provider `auth.type == "native"`). No new predicate, no ID string parsing.
3. Picker offers only running local models with **refcount zero**, using a line-typed prompt (numbers toggle, `all`/`none`, blank = confirm, `q`/`esc` = skip). A Bubble Tea arrow-key UI is a follow-up.

**Corrections to the first draft of this plan:** `IsNativeModel` dropped; refcount filtering added; the wt process's *own* refcount entry is released before the picker (otherwise the just-used model always counts ≥1); one consistent `PromptRun` signature (`… ) string`); injectable deps so the picker is testable; omlx/mtplx `stopModel` only ever receives a currently-running model because the picker lists running entries.

**Tasks / commits (one commit each, referencing the plan item):**

- [ ] **T1 `refcount.Release(pid)`** — remove all entries for a pid (best-effort session end). Test: releases only that pid; missing file is a no-op.
- [ ] **T2 `lifecycle.StopModel`** — `stopModel` on `backend`; ollama runs `ollama stop <name>`; omlx/mtplx delegate to `stop`. Tests via injected `env.run`/`lookPath`; update `fakeBackend`.
- [ ] **T3 `survey.PromptRun` returns the stats string and skips native models** — `func PromptRun(...) string`; `m.Native` → `""` without prompting. Update existing tests; add native test.
- [ ] **T4 `survey/stop.go` picker** — `Picker(r, w, cfg)` (TTY-guarded) over `runStopPicker(r, w, cfg, deps)` with `stopDeps{inventory, counts, stop}`. Lists `Running` entries with `counts == 0`; empty list ⇒ silent no-op. Tests for: no running, all in use, esc skip, toggle+confirm stops selected only, stop failure continues.
- [ ] **T5 non-TUI reorder** — `runAgentCmd(cmd, agent, m, cfg)`: Release own pid → survey → picker → summary → stats → pricing; exit-code behavior unchanged.
- [ ] **T6 TUI reorder** — `pendingSurvey` gains `cfg`; `runSurvey` seam returns stats; new `runStopPhase` seam; `printPendingSummaryAndSurvey` follows the same order. Update tests.
- [ ] **T7 verify** — `go build`, `go vet`, `go test ./...`, `make test-all` (from repo root), guide docs grep for stale post-exit descriptions (`docs/guides/`, `wt/CLAUDE.md`).

**Out of scope:** raw-mode arrow-key UI; stopping models on non-clean exits beyond the existing flow.
