# Agent coding benchmarks — `modelman benchmark agent`

> Use this to: run a real coding task through the `pi` agent across a matrix of model/thinking/route configurations, and read a report that separates speed from quality instead of ranking on tokens/sec alone.

Design rationale, gate taxonomy, and scoring rules: `docs/superpowers/specs/2026-09-04-agent-coding-benchmark-design.md`. This guide is the day-to-day usage doc; the spec is the source of truth for *why* each rule exists.

This guide, unlike 00/02/04/05/08, embeds no `litellm_exposed` snapshots — nothing here goes stale when a model is exposed/unexposed.

## Prerequisites

- Everything in [05-benchmarks](05-benchmarks.md)'s Prerequisites (no other local model loaded, backends healthy, isolation helpers on `PATH`).
- `pi` installed and on `PATH` — this harness drives `pi --mode json`, not a direct HTTP request, for the agent rows.
- A working LiteLLM apiKey already seeded into `~/.pi/agent/models.json` — launch any `wt` agent in litellm gateway mode once if you've never done so; the harness reads that key rather than storing its own.
- `OPENROUTER_API_KEY` available (via `~/Library/LaunchAgents/local.litellm.proxy.plist` or the env) if your suite's `[judge]` model is an OpenRouter model — preflight checks this before running any agent row.

## TL;DR

```bash
# from: /Users/keith/github/ohanaverse/local-ai-setup/modelman
uv run modelman benchmark agent list-tasks --root ../benchmarks/tasks
uv run modelman benchmark agent list-suites --root ../benchmarks/suites
uv run modelman benchmark agent run --suite ../benchmarks/suites/smoke.toml --dry-run
```

<!-- UNVERIFIED — a live run spawns a real pi agent process against a real (cloud) model and costs real judge tokens; capture this output the first time the smoke suite is actually run end-to-end. -->
```bash
uv run modelman benchmark agent run --suite ../benchmarks/suites/smoke.toml
# → Agent benchmark complete: 1 row(s), 1 ran without an isolation error
#   Results: /Users/keith/.config/local-ai/benchmarks/<run-id>

uv run modelman benchmark agent show --latest
```

## Steps

### 1. Pick or author a task

A task bundle lives under `benchmarks/tasks/<task-id>/` and needs `task.md`, `visible/`, `hidden/`, `gates.toml`, `rubric.md`, `meta.toml`. `day31-drift` (a calendar-arithmetic bug in an invented billing domain, `kettlecomb`) ships as the first task — see its `task.md` for the exact bug report an agent receives, and `meta.toml` for the tier expectations a run's results should be read against. Task-authoring rules are in the spec's "The first task" section: bespoke domain (no public repo an LLM might have memorized), a deterministic gate, and a plausible wrong answer that isn't just "said no."

### 2. Write or reuse a suite

A suite (`benchmarks/suites/*.toml`) picks a task and a `[[rows]]` matrix (model × thinking × route, cartesian-expanded). `smoke.toml` is a one-row, fast-cloud-model suite for plumbing checks; `q4-agent-sweep.toml` is the fuller local-model matrix. `--dry-run` prints the resolved row list without running anything — always dry-run a new or edited suite before a real run, since a mistyped model id loads a 27B model for nothing.

### 3. Run it

```bash
uv run modelman benchmark agent run --suite <path> [--row <label-or-index>]... [--passes N] [--skip-judge] [--dry-run]
```

Local rows are grouped by provider and isolated once per group (stop-others, start, warmup) via the same `bin/llm-isolate-provider`/`bin/llm-restore-providers` helpers `modelman benchmark` uses — see [05-benchmarks](05-benchmarks.md) Step 1 for exactly what isolation does per backend. Judging always runs *after* `restore_providers()`, so a cloud judge call never contends with a loaded local model.

### 4. Read the report

```bash
uv run modelman benchmark agent show --latest
uv run modelman benchmark agent show --run-id <run-id>
```

`summary.md` (copy it into `benchmarks/results/` yourself if you want it version-controlled, same as the bash benchmarks' results) has four tables: **Quality** (outcome code, hidden n/m, rubric, cap, composite, verdict), **Speed** (`wall_s`, `gen_s`, first/median TTFT, and `gen_tok_s`/`e2e_tok_s` derived from `output_tok` over each denominator, plus input/output/cache-read/cache-write/reasoning tokens, tool-call count and request count), **Two-axis** (sorted by composite, Pareto-nondominated rows starred — "fastest config at ≥ this quality"), and **Anomalies** (every cap applied, `VACUOUS_TEST`, `thinking=off but N reasoning tokens`, the `CACHE_ANOMALY`/`COLD_FIRST_TOKEN`/`REPEATED_FAILURE` string metrics derived, overclaim, and any row scoring ≥70 rubric points despite failing every hidden test). There is no per-row "thinking no-op" flag: whether `--thinking` changes anything is a comparison across a suite's off/high rows, not a property of one run's stream. A `JUDGE_FAIL` row keeps its gates/speed data and shows `N/A` for quality — it never voids the row, and it never earns a Pareto star.

Per-row artifacts live under `~/.config/local-ai/benchmarks/<run-id>/<row-dir>/`: `agent.jsonl.gz` (raw event stream), pi's own session file (`*.jsonl`, named by pi — this is gate 1's evidence that a session really ran), `diff.raw.patch`/`diff.patch` (as-produced / anonymized), `gates.json`, `metrics.json`, `judge.json`, `row.json` (what `agent judge` needs to re-score later).

### 5. Re-judge cheaply after a rubric edit

```bash
uv run modelman benchmark agent judge --latest [--row <row-dir>]... [--samples N]
```

Re-scores from each row's persisted `row.json`/`diff.patch`/`gates.json` — no agent re-run, no isolation. It rewrites each row's `judge.json` and prints the new rubric total/composite/verdict per row; it does **not** regenerate `summary.md` — re-read the individual `judge.json` files (or `agent show --latest`, which still reflects the *original* judge pass) until a future enhancement adds full summary regeneration.

## Verification

<!-- UNVERIFIED — requires a completed agent run. -->
```bash
ls ~/.config/local-ai/benchmarks/<run-id>/
# → run.toml  summary.md  metrics.jsonl  <row-dir>/...
```

## Gotchas

- **`pi` needs no permission-bypass flag** for this harness — tool execution works under `--no-approve` in `--mode json` because non-interactive modes never prompt; see the spec's "Verified against the live setup" for why `--no-approve` is passed anyway (pinning project-trust behavior, not enabling tools).
- **`route = "direct"` requires a matching `[routes.direct.<provider>]` block** in the suite — preflight fails immediately, naming the missing provider, rather than after an agent has already run.
- **`omlx` 4-bit and 6-bit share one provider.** Use a row's `provider =` override (not just the model id) to isolate the exact variant — same rule as `modelman benchmark`'s own isolation, see [05-benchmarks](05-benchmarks.md) Gotchas.
- **`route = "direct"` sends the registry `model_name`, which is not always what the backend serves.** True for `ollama` (bare names), not for `omlx`, whose registry entries are org-prefixed (`mlx-community/X`) while the server knows `X`. Set the row's `direct_model = "X"` (check `curl -s localhost:8000/v1/models`) or the row's pi requests will 400. A misaddressed row does not look like a configuration error here — it looks like a model that failed the task, which is the one misreading this harness cannot allow.
- **`thinking = "off"` is a request, not a guarantee.** Observed live: `--thinking off` against `ollama/glm-5.3-flash:cloud` still returned `usage.reasoning = 11` and a thinking block. The Anomalies table flags those rows (`reasoning emitted with thinking=off`); treat an off/high pair as uncomparable when either is flagged.
- **`repair_rounds` is accepted but rejected if non-zero.** The seam exists (per-turn retry after a failed run) but is disabled in v1 — see the spec's "Deferred: the repair round."
- **Local-model timeouts are a config input, not a bug.** A 27B model in a six-round agentic task can exceed `agent_timeout_s`; `TIMEOUT` caps the composite at 0 but is reported as its own outcome class — raise `agent_timeout_s` per-backend rather than reading a timeout as "this model can't do it."
- **Judging costs cloud API spend on every row.** `--skip-judge` exists for plumbing checks; `agent judge --row` re-scores a subset of an existing run without re-running agents.

## Going deeper

- Full design: `docs/superpowers/specs/2026-09-04-agent-coding-benchmark-design.md`
- Implementation plan: `docs/superpowers/plans/2026-09-04-agent-coding-benchmark.md`
- Module map: `modelman/CLAUDE.md` (Benchmark subsystem)
- Single-turn speed benchmarks (the fast triage tool this harness doesn't replace): [05-benchmarks](05-benchmarks.md)
