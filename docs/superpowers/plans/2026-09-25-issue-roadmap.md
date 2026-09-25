# Issue roadmap — GitHub issue triage and fix sequence

Date: 2026-09-25  
Context: `local-ai-setup` GitHub repo, 8 open issues. `issues.md` historical items are all FIXED (last updated 2026-08-29, plus one deferred item #6 from 2026-09-23).

## Executive summary

This doc maps the current open issues into a sequence of concrete milestones and pull requests. The ordering follows a **data integrity → CI reliability → polish** hierarchy: issues that can silently corrupt state or lose data are addressed first, then flaky tests, then cosmetic/performance tweaks.

The resulting order is:

1. **Milestone A** — Fix modelman EXPOSED column reading stale flag (#145) — **root cause: stranded LiteLLM routes on model delete**.
2. **Milestone B** — Fix wt config.toml whole-file last-writer-wins race (#143) — **root cause: silent overwrite of LiteLLM routing state and credentials**.
3. **Milestone C** — Fix flaky wt lifecycle stop tests (#123) — **root cause: ephemeral port reuse window in tests**.
4. **Milestone D** — Suppress ollama warnings on `modelman refresh-prices` (#151) — **cosmetic noise fix**.
5. **Milestone E** — Batch LiteLLM proxy restarts on multi-stop (#142) — **performance tweak for multi-select**.
6. **Milestone F** — wt stop picker: multi-number toggles + `;` (#139) — **UX feature**.
7. **Milestone G** — Replace speed survey with measured stats (#136) — **vague; needs scoping**.
8. **Milestone H** — mitmproxy to capture all sessions (#117) — **investigation/experiment**.

The deferred item from `issues.md` (#6 — `piCompat` full-pointer-replacement risk) is tracked separately and not in this roadmap; it remains deferred until `piCompat` gains a second field.

---

## Milestone A — Fix modelman EXPOSED column / stranded routes (#145)

> **Priority: highest.** Not just cosmetic — this issue creates a real data-loss path where deleting a model that wt has routed leaves a dead route stranded in LiteLLM's `config.yaml`.

**Goal:** modelman's EXPOSED column for local models reads wt's live routes (`wt litellm list`) instead of the stale `exposed` flag in `modelman.toml`. The delete cascade never strands a route.

### Background

Since Phase 4 (issues #140–#144), `wt` owns LiteLLM routing: `wt start`/`wt stop` add and remove `config.yaml` routes directly, and `wt litellm sync` can change routes without touching modelman's `exposed` flag. Modelman's TUI still reads the stale flag, so:

- A model started with `wt start` is genuinely routed through LiteLLM, but modelman's EXPOSED column shows `–`.
- **Critical:** if modelman deletes a model that wt has routed (the model's `exposed` flag is `false`, so modelman's delete cascade thinks it doesn't need to unexpose anything), the route is left stranded in `config.yaml`. `wt litellm sync` does not clean it up — only a manual `wt litellm unexpose <id>` does.

### PR A1: Wire EXPOSED column to `wt_bridge.routed_ids()`

Changes in `modelman/src/modelman/`:

- `wt_bridge.py` already has `routed_ids()` (added in Task 9 of Phase 4, kept as the hook for this work). It currently has no production caller.
- In the TUI's models screen (`modelman/src/modelman/screens/models.py` or equivalent), for local model rows, read the EXPOSED state from `wt_bridge.routed_ids()` instead of `modelman.toml`'s `exposed` flag.
- Add a per-refresh cache (mirroring the existing `_litellm_status` caching pattern) so the TUI doesn't spawn `wt` once per row per refresh.
- Add a stateful test fake for `wt litellm list` (today's autouse fake always returns `{"routed": []}`).

### PR A2: Fix the delete cascade to always call `wt litellm unexpose`

Changes in `modelman/src/modelman/queue.py` (or the relevant delete handler):

- Replace the stale `exposed` flag check in the delete cascade with an unconditional `wt litellm unexpose <id>` call. This is a safe no-op if the model was never routed by wt, and it correctly cleans up routes that wt added.
- Alternatively: check `wt_bridge.routed_ids()` before deciding whether to unexpose, but the unconditional call is simpler and safer.

### Verification for Milestone A

```bash
# 1. Start a local model with wt, confirm modelman TUI shows it as exposed
wt start ollama/qwen3.8:27b-mlx
uv run modelman list   # EXPOSED column should show Y

# 2. Delete the model through modelman, confirm route is cleaned up
modelman delete ollama/qwen3.8:27b-mlx
# Verify: no stranded entry in ~/.config/litellm/config.yaml model_list

# 3. Run modelman tests
cd modelman && make test
```

### Dependency

None — this is the first milestone and can start immediately.

---

## Milestone B — Fix wt config.toml concurrent write race (#143)

> **Priority: high.** Since Phase 3, `config.toml` holds LiteLLM routing state and credentials. A silent overwrite can lose routing mode and a secret key.

**Goal:** concurrent writes to `~/.config/agent-wt/config.toml` are serialized or conflict-detected so no change is silently lost.

### Background

`config.toml` is written whole-file (`config.Save` re-encodes the entire Config) with no lock and no re-read. Two writers can silently overwrite each other:

- `wt config` (the config editor) loads a Config when it opens and saves that whole snapshot (`internal/configeditor/save.go`).
- A `wt litellm on|off|set` run from another terminal while the editor is open is reverted when the editor saves.
- The reverse also holds: `wt litellm set` can clobber agent edits made in an editor session that saved earlier from a stale snapshot.

The temp-file collision that could tear or empty `config.toml` under concurrent writers was already fixed in Phase 3 (unique temp file per write). That makes each write atomic but does not make read-modify-write sequences safe.

### PR B1: Add flock-based serialization to config writes

Recommended approach: Option 1 (serialize with `flock`) — smallest correct change.

Changes in `wt/internal/`:

- Add `internal/config/flock.go` (or extend `internal/litellm` which already does `flock` for `config.yaml`):
  - `func WithLock(path string, fn func() error) error` — acquires `flock` on `path + ".lock"`, runs `fn`, releases.
  - Every `config.Save` call wraps its write in `WithLock`.
  - The editor save and `wt litellm` commands both go through the same `config.Save` path, so the lock is automatically shared.
- Alternatively, if we want patch-style writes: add `UpdateLitellm()` that reads the full config under lock, merges only `[litellm]` fields, and writes back. The editor save would merge only agent/prefs fields. This is more complex but allows finer concurrency.

### PR B2: Add a conflict-detection guard for the editor (optional, cheap)

As an additional safety net for the editor:

- Record the file's mtime (or a simple hash) at load time.
- On save, if the file changed underneath, refuse to save with a clear message: "config.toml was modified by another process since you opened it. Save aborted."
- This is cheaper than flock for the editor path and catches the common case.

### Verification for Milestone B

```bash
# Go tests
cd wt && go test ./internal/configeditor ./internal/litellm ./...

# Manual: open wt config editor in one terminal, run `wt litellm on` in another,
# save in the editor — neither change should be lost (or the stale save should
# be refused with a clear error message).

# Verify the lock file is cleaned up even on interrupt
ls ~/.config/agent-wt/config.toml.lock   # should not exist after normal exit
```

### Dependency

Can proceed in parallel with Milestone A (different codebases: modelman vs wt).

---

## Milestone C — Fix flaky wt lifecycle stop tests (#123)

> **Priority: medium.** Two tests fail intermittently in CI (~1.01s timeout hit). Pre-existing, not caused by recent changes.

**Goal:** `TestOmlxStopWaitsForPortClose` and `TestMtplxStopRunsMtplxStopAndConfirmsPortClosed` pass reliably in CI.

### Background

Every failure is at ~1.01s — one full 1s probe timeout in `portClosedWithin` (`probe.go`), not a refused connection. The suspected cause:

- The tests call `freeAddr`, which binds `:0`, records the port and closes the listener, then rebinds it in `serveAt`.
- Between those steps, and after the test's own `srv.Close()`, another package's test binary (packages run in parallel) could take the same ephemeral port with a listener that accepts and stalls.
- The stop check then sees a stalled listener (`timedOut`), which counts as "still holds the port."

### PR C1: Eliminate the free-then-rebind window

Changes in `wt/internal/lifecycle/`:

- Instead of `freeAddr` + `serveAt`, hand the already-bound listener directly to the server (use `http.Server{Handler: ..., BaseContext: ...}` with a pre-bound `net.Listener`).
- This eliminates the window where another process can grab the ephemeral port.
- Alternatively: use a fixed port range for tests (e.g., `12345-12399`) and track allocation with a test-global mutex, so ports never collide.

### PR C2: Tighten the probe (if C1 is not feasible)

- Consider whether `portClosedWithin` should treat a stalled listener (connect succeeds but no response within Nms) as "closed" rather than "still listening."
- Or: use a much shorter probe timeout so a foreign listener cannot consume the whole `stopTimeout`.

### Verification for Milestone C

```bash
# Reproduce under load (this should fail on main, pass after fix)
cd wt && go test -count=50 ./internal/lifecycle

# Run with parallel packages (current CI behavior)
cd wt && go test -p 4 ./...

# CI: verify the wt-ci test job stops failing across repeated runs
```

### Dependency

Can proceed in parallel with Milestones A and B (purely a wt test fix).

---

## Milestone D — Suppress ollama warnings on refresh-prices (#151)

> **Priority: low.** Cosmetic noise — ollama has no API pricing, warnings are useless.

**Goal:** `modelman refresh-prices` does not show warnings for ollama models.

### PR D1: Skip ollama models in refresh-prices

Changes in `modelman/src/modelman/`:

- In the `refresh-prices` command, filter out models where the provider is `ollama` (no OpenRouter API, no pricing to fetch).
- Alternatively: only show warnings if there are OpenRouter models in the config that couldn't be refreshed (i.e., suppress the warning entirely when no OR models exist).
- The issue also mentions adding a skill/script to fetch ollama pricing from `https://ollama.com/pricing` — defer that as a separate enhancement; it's not a bug fix.

### Verification for Milestone D

```bash
cd modelman && make test
uv run modelman refresh-prices   # should complete without ollama warnings
```

### Dependency

None.

---

## Milestone E — Batch LiteLLM proxy restarts on multi-stop (#142)

> **Priority: low.** Performance tweak — single-stop is correct; only multi-select stops are slow.

**Goal:** stopping N models through the wt stop picker bounces the LiteLLM proxy once instead of N times.

### PR E1: Add a `StopModels` batch API

Changes in `wt/internal/lifecycle/`:

- Add `StopModels(ctx, cfg, []Model)` that:
  1. Stops each model in the list.
  2. Writes all route removals with `litellm.Options{NoRestart: true}`.
  3. Does a single settling restart + wait (`ForceRestart`).
- The mechanism already exists from the replace-start fix (`restartDeferred` / `restartForced` in `wt/internal/lifecycle/routes.go`).
- Switch the stop picker to call `StopModels` instead of looping `StopModel`.

### Verification for Milestone E

```bash
cd wt && go test ./internal/lifecycle ./...

# Manual: stop 3+ models through the TUI picker — proxy should restart once,
# not three times. Timing should be noticeably faster.
```

### Dependency

None. Depends on the Phase 3 restart mechanism being in place (it is).

---

## Milestone F — wt stop picker: multi-number toggles + `;` (#139)

> **Priority: low.** UX feature — nice to have, not blocking.

**Goal:** the stop picker accepts multi-number toggles (e.g., `1,3,5` or `1-3,5`) and `;` to run immediately.

### PR F1: Extend the stop picker input handling

Changes in `wt/internal/`:

- In the stop picker TUI, extend the input handler to parse comma-separated numbers and ranges.
- Add `;` as a shortcut key to confirm and run immediately (bypassing the default confirm flow).
- Add/update Go tests for the input parsing.

### Verification for Milestone F

```bash
cd wt && go test ./...
# Manual: open stop picker, type "1,3,5" and confirm — models 1, 3, 5 should be stopped.
```

### Dependency

None.

---

## Milestone G — Replace speed survey with measured stats (#136)

> **Priority: low / vague.** No spec provided; needs scoping before implementation.

**Goal:** TBD — the issue title suggests replacing a "speed survey" with measured performance stats. Needs clarification on what the survey is, what stats should replace it, and where this appears (TUI? docs? benchmarks?).

### PR G1: Scope the requirement

- Identify what "speed survey" refers to (search the codebase for "survey" or "speed").
- Determine what measured stats should replace it (tokens/sec? latency? p50/p95?).
- Draft a short spec and create a detailed implementation plan.

### Dependency

None, but this should be scoped before any implementation work.

---

## Milestone H — mitmproxy to capture all sessions (#117)

> **Priority: low / investigation.** Not a bug — an experiment to capture all HTTP sessions through mitmproxy.

**Goal:** TBD — the issue asks whether mitmproxy can be set up to capture all sessions. This is an investigation/experiment, not a code fix.

### PR H1: Set up mitmproxy capture

- Configure mitmproxy as a system-wide transparent proxy or per-application proxy.
- Capture and review the sessions to understand what's flowing through the stack.
- Document findings and decide if any follow-up fixes are needed.

### Dependency

None. This is a standalone investigation.

---

## Sequence summary

| Order | Milestone | Issue | PR(s) | Status | Why this order |
|-------|-----------|-------|-------|--------|----------------|
| 1 | A | #145 | A1, A2 | OPEN | Data-loss path: stranded LiteLLM routes on model delete. Highest impact. |
| 2 | B | #143 | B1, B2 | OPEN | Silent data-loss race on config.toml. Can corrupt routing state and credentials. |
| 3 | C | #123 | C1, C2 | OPEN | CI flakiness wastes cycles and hides real regressions. |
| 4 | D | #151 | D1 | OPEN | Cosmetic noise, quick win. |
| 5 | E | #142 | E1 | OPEN | Performance tweak for multi-stop. |
| 6 | F | #139 | F1 | OPEN | UX feature. |
| 7 | G | #136 | G1 | OPEN | Vague; needs scoping first. |
| 8 | H | #117 | H1 | OPEN | Investigation/experiment. |

---

## Cross-cutting concerns

1. **Tests must pass at every step.** Use `make test-all` at repo root as the gate before each PR merge. For modelman: `cd modelman && make check && make test`. For wt: `cd wt && go build ./... && go vet ./... && go test ./...`.

2. **Docs drift.** Any change to registry model rows, provider availability, exposure semantics, or config file ownership must update `docs/guides/00-config-map.md`, `docs/guides/08-maintenance-and-troubleshooting.md`, and the relevant package CLAUDE.md files (`modelman/CLAUDE.md`, `wt/CLAUDE.md`). Use `make check-links` after doc edits.

3. **Config file ownership.** Milestone A touches `modelman.toml` semantics and `wt litellm list` output. Milestone B touches `config.toml` write paths. Both are documented in `docs/guides/00-config-map.md` — update the "Owner (writes)" and "Consumers" columns where semantics change.

4. **No secret leaks.** If `config.toml` or `config.yaml` are shown in tests, docs, or issue comments, redact real `api_key` values. Follow the secret-handling rules in `docs/guides/00-config-map.md` (Gotchas, "Secrets on disk").

5. **Milestones A and B are independent.** They touch different packages (modelman vs wt) and can be worked on in parallel by different agents. Milestones C, D, E, F are also independent of each other and of A/B.

6. **Deferred item (#6 from issues.md).** The `piCompat` full-pointer-replacement risk in `pi_models.go` remains deferred until `piCompat` gains a second field. Not in this roadmap.

---

## Open questions before starting

1. **Milestone A:** Does the host have any hand-managed entries in `config.yaml` that modelman's delete cascade should preserve? (The config-map says 9 entries are hand-managed — the delete cascade should not unexpose those.)
2. **Milestone B:** Which approach does the user prefer — flock serialization (Option 1, smallest correct change) or patch-style writes with conflict detection (Option 3, more complex but finer concurrency)?
3. **Milestone G:** What exactly is the "speed survey" that needs replacing? Needs investigation.