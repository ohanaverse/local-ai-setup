# Issue roadmap — #179 follow-ups, stop picker, measured speed

Date: 2026-10-03
Context: `local-ai-setup` GitHub repo, 7 open issues. #179 (local models are discovered) closed today; #192–#195 are its follow-ups. #139 and #136 carry over from the [2026-09-25 roadmap](2026-09-25-issue-roadmap.md) (its milestones F and G), whose other milestones have shipped. #117 is deferred.

## Executive summary

This doc sequences the open issues into milestones and pull requests. The order is **live failure → safety → cleanup → features → investigation**. Every PR is small and mergeable on its own.

1. **Milestone A** — wt adds the missing LiteLLM route at launch (#192) — **the only live user-facing failure**.
2. **Milestone B** — `wt smoke` runs in a temp dir; sentinel check cannot pass on a prompt echo (#193) — **safety and a false PASS**.
3. **Milestone C** — wt edge cases, docs wording, test gaps (#195) — **cleanup, three PRs**.
4. **Milestone D** — modelman discovered-model ids and small fixes (#194) — **cleanup**.
5. **Milestone E** — mlx_lm_server pairing identity (#194, items #19a/b) — **changes how "running" is decided**.
6. **Milestone F** — stop picker: multi-number toggles and `;` (#139) — **UX feature**.
7. **Milestone G** — can measured stats replace the speed survey (#136) — **spike, no code kept**.

**Deferred:** #117 (mitmproxy capture). Not planned now, by decision on 2026-10-03. The issue stays open.

## How each milestone is run

Each milestone names a path. The path sets how much design happens before code:

- **Short spec** — its own brainstorm, a written spec in `docs/superpowers/specs/`, then an implementation plan.
- **Bounded** — a short design agreed in chat, then implementation. No spec file.
- **Spike** — a question and a probe. The output is a recommendation.

This roadmap fixes order and PR boundaries. It does not approve any milestone's design.

**Verification for every PR:** `make test-all`. Live checks are listed per milestone. The `wt start` picker, the stop picker and modelman's TUI cannot be driven by an agent, so those checks need the user.

**Issue housekeeping:** #194 and #195 are not split into per-PR issues. Each PR references its parent issue, and the parent closes with its last PR.

---

## Milestone A — route at launch (#192)

> **Priority: highest.** Path: short spec.

**Problem.** A local model that is running but that wt did not start has no LiteLLM route until something runs `wt litellm sync`. Since #188 wt no longer refuses such a model, so the launch goes ahead and the proxy answers `Invalid model name`. `wt start <id>` does not help: for a running model it prints `<id> is already running` and writes no route (`wt/cmd/wt/model_cmds.go:288`).

**Goal.** Launching a running local model through wt always works, whoever started it.

### PR A1: ensure the route before a local launch

- Before a local launch row is handed to the agent, and when its route goes through LiteLLM, write the same change the start hook writes: `lifecycle.StartRouteChange` → `litellm.ApplyChange`, then `lifecycle.WaitPendingRoutes()`.
- Apply it on every launch path: the non-TUI launch (`resolveModelForLaunch` in `wt/cmd/wt/main.go`), the TUI launch (`wt/internal/tui/start_flow.go`), and `wt smoke`.
- `wt start <running id>` repairs the route, and no longer only prints "already running".
- Carry the row's model id in `lifecycle.Target`, so the route write uses the id the picker resolved and does not re-derive it through `litellm.ModelFor`'s fuzzy fallback. This also closes #195's "two artifacts name-matching one registry entry" item.

**Constraints (from the issue).**

- A discovered route never replaces a hand-written (unmarked) row. `ApplyChange` already enforces this.
- At most one proxy restart, and none when the route is already present.
- No write when the provider's probe is untrusted.

**Live check.** Start an omlx or mtplx model by hand, launch it through wt, and confirm the agent gets a reply and not `Invalid model name`. Launch it again and confirm the proxy does not restart.

### Dependency

None. B and C1 follow it.

---

## Milestone B — smoke safety and sentinel (#193)

> **Priority: high.** Path: bounded.

**Problem 1.** `wt smoke` forces each agent's skip-permissions flag and runs in the current directory (`wt/cmd/wt/smoke.go:121`). In the #179 acceptance run, a 1B model under claude wrote a junk file into the repo root.

**Problem 2.** The row passes when the sentinel appears anywhere in the output (`wt/internal/smoke/smoke.go:462`), and the default prompt contains the sentinel verbatim (`smoke.go:486`). An agent that echoes the prompt passes whatever the model replied. Seen on codex in the acceptance run; not yet confirmed by a controlled run.

### PR B1: temp dir by default, echo-proof sentinel

- **Confirm first.** Run the codex row against a model that cannot answer. If it passes, problem 2 is confirmed. If it fails, drop the sentinel change from this PR and record the finding on the issue.
- Run each row in a fresh temporary directory, with a flag to opt back into the cwd. codex needs a git repo there: `git init` the directory, or pass `--skip-git-repo-check`.
- Build the sentinel so the prompt never contains it verbatim (for example, ask the model to join two halves). This fixes every agent at once and needs no per-agent output parsing.
- Update `wt/docs/wt-smoke.md`.

**Live check.** `wt smoke` from the repo root leaves `git status` clean. After the PR, run opencode on a discovered ollama model that is strong enough to complete the task (item 3 of the issue).

### Dependency

After A: both edit `wt/cmd/wt/smoke.go`.

---

## Milestone C — wt cleanup (#195)

> **Priority: medium.** Path: bounded, one design per PR.

### PR C1: code

- Reject or warn on a provider row whose `location` is mistyped (`"Local"`), so its routes are not removed silently.
- Remove dead code: `litellm.Apply` and `lookup` (no production caller), and `routeRemove`'s unreachable multi-tenant default branch.
- A family clear no longer reports `unrouted` for registry ids that had no row. `Remove: [""]` no longer reports an outcome.
- Warnings: a refused daemon whose only routes are discovered gets the "treated as stale" warning; the registry-gap warning names a missing provider row when that is the cause.
- **Sibling flip-flop:** not fixed. It needs a single-model server that lists more than one model, which no supported provider does. Pin the current behavior with a test and a comment.
- **opencode passthrough path after the yolo flag:** verify with a real run. Fix only if it fails.

### PR C2: docs

- Guide 02 and `modelman/CLAUDE.md`: the add form's `--` spelling applies to every provider except mtplx and native. If D1 changes the spelling, fold this item into D1.
- Guides 04 and 02: note the mlx_lm_server exception to "deleting a registry entry does not unroute".
- Guide 01: say how the local `ollama` provider row is created on a fresh machine.
- `wt/docs/wt-start-stop.md`: restore the note that a registry model of an unprobed provider shows as an unselectable row.

Guides stay state-independent: commands that reveal state, no inventories.

### PR C3: tests

- modelman: per-id dedup of discovered models (both omlx rows listing one artifact).
- wt: make `TestSyncModelsTargetAlreadyCoveredChangesNothing` fail when the "covered" loop is removed.
- wt: the discovered-id test reads the contract fixture in `docs/contracts/` and stops pinning literals.

### Dependency

C1 after A (both touch the route-writing code in `internal/litellm`, and A closes one of C1's items). C2 and C3 are independent.

---

## Milestone D — modelman discovered-model ids (#194)

> **Priority: medium.** Path: bounded.

### PR D1: ids, discovery and small fixes

- **TUI `+` form** (`screens/forms.py`, `_submit_discovered`): write the discovered id (`mtplx/org/name`), not the `--` spelling, so registering a model does not start a new stats key.
- **ollama name clash:** try the exact discovered id before the lenient tail match, so `ollama/someuser/qwen3:8b` starts that model and not the registered `ollama/qwen3:8b`.
- **Provider-prefix check:** count local families only, so a bare repo id whose org equals a cloud provider id is accepted.
- **#17:** Discard after add-and-start also removes the route, or asks wt for a sync.
- **#18:** `modelman start ollama/<x>` on an unpulled model reports `not pulled — ollama pull <name>`.
- **Small:** `modelman start --help` opening line; the all-default `[model_state."<id>"]` row left after stopping a discovered id; a registered artifact listed under Discovered when its id collides.

**Open design question — omlx-6bit.** modelman has no Provider class for `omlx-6bit`, so with only that provider row it cannot list or start unregistered omlx artifacts, while wt can. Adding a Provider class grows modelman, against the direction of moving logic into wt. Recommended: modelman asks wt for the discovered list. Settle this in D1's design; if it grows, it becomes its own PR.

**Live check.** The `+` form and Discard need the user's eyes.

### Dependency

None.

---

## Milestone E — mlx_lm_server pairing identity (#194, #19a/b)

> **Priority: medium-low.** Path: short spec.

**Problem.** A pairing named after an HF-cache repo id can read as running when it is not. The single-registered-pairing fallback also routes that pairing when an unregistered ad-hoc pairing is what is serving.

**Goal.** "Running" for mlx_lm_server is decided by what the process is serving, read from the process (for example the pidfile's argv), not inferred from names.

### PR E1: identity from the process

Scope set by the spec. Both wt (live probe, routing) and modelman (`providers/lifecycle/pidproc.py`, `backends/mlx_lm_server.py`) are likely to change, so the spec decides which side owns the identity read and whether the pidfile format changes. A pidfile format change is a cross-language contract and belongs in `docs/contracts/`.

### Dependency

None. Closes #194 together with D1.

---

## Milestone F — stop picker input (#139)

> **Priority: low.** Path: short spec.

**Goal.** The stop picker (`runStopPickerWith` in `wt/internal/survey/stop.go`, reached from `wt stop` and the post-exit stop phase) accepts several numbers in one input and a trailing `;` to run at once.

### PR F1: multi-number toggles and `;`

Proposed rules, to be confirmed in the spec:

- Numbers separated by spaces or commas toggle each named line: `1 2 3`, `1,2,3`, `1,3 5`.
- A trailing `;` applies that line's toggles and then runs the stop. `all;` stops everything; plain `all` toggles all and waits.
- An invalid or out-of-range number rejects the whole line and changes nothing.
- No ranges.

**Live check.** Needs the user at the picker.

### Dependency

None.

---

## Milestone G — measured speed (#136)

> **Priority: low.** Path: spike.

**Question.** Can a measured figure replace the 1–5 speed rating typed after each session (`wt/internal/survey/prompt.go:66`), shown as the `s<n>` cell in the picker and the SPEED column of `wt stats`?

**Probe.** wt is not in the request path, so a measured figure has to come from elsewhere. For each candidate source, find what it records and what share of real sessions it covers:

- `LiteLLM_SpendLogs` — start and end time plus completion tokens per request. Covers only sessions routed through the proxy, and reading it gives wt a Postgres dependency.
- `modelman benchmark` results — synthetic runs, benchmarked models only.
- `wt smoke` timings — one short prompt per row.

Also weigh what the typed rating captures that tok/s does not: agent overhead and tool round-trips.

**Output.** A recommendation posted on #136: replace, supplement, or keep the survey as it is. Any code written is throwaway. Building the chosen option is a new task with its own classification.

### Dependency

None.

---

## Sequence summary

| Order | Milestone | Issue | PR(s) | Path | Why this order |
|-------|-----------|-------|-------|------|----------------|
| 1 | A | #192 | A1 | Short spec | Live failure: a launch that answers `Invalid model name`. |
| 2 | B | #193 | B1 | Bounded | Unsupervised agents writing into the cwd; a check that can pass falsely. |
| 3 | C | #195 | C1, C2, C3 | Bounded | Rare or cosmetic. C1 waits for A. |
| 4 | D | #194 | D1 | Bounded | Stats-key split and wrong-model start are real but narrow. |
| 5 | E | #194 | E1 | Short spec | Needs design; no reported failure on the host. |
| 6 | F | #139 | F1 | Short spec | UX feature. |
| 7 | G | #136 | — | Spike | A question, not a build. |
| — | — | #117 | — | — | Deferred. |

D, E, F and G depend on nothing above them and can move earlier.
