# Cross-category capability benchmark — `llmbench eval`

> Use this to: score local models on reasoning/planning/coding/code_review/doc_summary,
> so a model/quant/engine choice can be made per use case instead of on
> throughput alone.

Design rationale: `docs/superpowers/specs/2026-09-17-capability-eval-benchmark-design.md`.

## Prerequisites

- Everything in [05-benchmarks](05-benchmarks.md)'s Prerequisites (no other local model loaded, backends healthy, llmbench runnable via `uv run` — the provider lifecycle runs in-process in `llmbench eval`, no isolation helpers on PATH needed).
- A working LiteLLM apiKey seeded into `~/.pi/agent/models.json` for any `route = "litellm"` row or judge — same requirement as [09-agent-benchmarks](09-agent-benchmarks.md).
- `OPENROUTER_API_KEY` available if the suite's `[judge]` or any row uses `route = "openrouter"`.
- `uv sync --extra eval` from `llmbench/` — the `coding` category needs EvalPlus, which is not installed by plain `make install`. `evalplus_runner` runs the `evalplus.evaluate` console script that sits next to the running interpreter (the venv's `bin/`), and falls back to a bare-name `PATH` lookup only when no such file exists — `uv sync --extra eval` puts it there (not `uvx`, which would bypass the extra entirely).

## TL;DR

```bash
# from: /Users/keith/github/ohanaverse/local-ai-setup/llmbench
uv run llmbench eval list-categories --root ../benchmarks/tasks/eval
uv run llmbench eval list-items --category doc_summary --root ../benchmarks/tasks/eval
uv run llmbench eval run --suite ../benchmarks/suites/eval-sweep.toml \
    --root ../benchmarks/tasks/eval --dry-run
```

## Steps

### 1. Pick or author categories

A category lives under `benchmarks/tasks/eval/<name>/` — `items.toml` (the fixed prompt set) plus `rubric.toml`/`rubric.md` (except `coding`, which is EvalPlus-graded and has no rubric). Five categories ship today: `reasoning`, `planning`, `coding`, `code_review`, `doc_summary`. A rubric's `[dimensions]` must sum to 100.

### 2. Write or reuse a suite

A suite (`benchmarks/suites/eval-*.toml`) picks a `[judge]`, an optional `[coding]` override (dataset/limit), an optional `[generation]` table (how the rows themselves are sampled: `temperature`, plus `top_p`/`top_k`, sent only when set; without it rows run greedy at temperature 0), and a `[[rows]]` list (model + route, optionally a per-row `categories =` narrowing). `eval-sweep.toml` is the reference suite. Always `--dry-run` a new/edited suite first.

### 3. Run it

```bash
uv run llmbench eval run --suite <path> --root ../benchmarks/tasks/eval \
    [--category <name>]... [--row <label-or-index>]...
```

There is no `--skip-judge` flag here (unlike `llmbench agent run`): `coding` has no judge to skip, and the four judged categories always judge. Rows are grouped by provider and isolated once per group, same as `llmbench`/`llmbench agent` — see [05-benchmarks](05-benchmarks.md) Step 1.

### 4. Read the report

```bash
uv run llmbench eval show --latest
```

`summary.md` has three tables: **Capability matrix** (rows grouped by registry family so quant/engine variants of the same base model sit adjacent, one column per category, composite /100 or EvalPlus pass@1%), **Per-category leaderboard** (top rows per category — the direct "best model for X" answer), and **Anomalies** (`JUDGE_FAIL`, isolation errors, EvalPlus execution errors).

### 5. Re-judge cheaply after a rubric edit

```bash
uv run llmbench eval judge --latest --root ../benchmarks/tasks/eval [--row <row-dir>]...
```

Re-scores every judged category's items from their persisted `response.txt` — no regeneration, no isolation. Coding has nothing to re-judge (EvalPlus's pass@1 is deterministic).

## Probing reasoning length across engines

When one engine's answers come out a different shape from another's for the same model (issue #403), `llmbench probe thinking` sends the same prompts to each engine and puts the length and opening of each reply's reasoning (`reasoning_content` or `reasoning`, else a leading `<think>` block in the content) beside the answer's length and markdown shape. By default it sends two prompts: `What is 17*23? Answer briefly.` and the `planning` category's `flaky-test-suite` item, read from `benchmarks/tasks/eval/planning/items.toml`. `--prompt-file FILE` (repeatable) adds more; `--no-builtin` leaves the two out.

**It does not manage servers.** It sends chat completions to the URL each `--target` names, nothing else: it never starts, stops, isolates or warms a provider, and never reads the registry. Isolate each engine yourself first, and probe one engine at a time. Two 27B-class models resident at once is a memory problem on Apple Silicon, so isolate the next engine (which stops the last one) before probing it, and restore at the end:

```bash
# from: /Users/keith/github/ohanaverse/local-ai-setup/llmbench
uv run llmbench provider isolate omlx <omlx-model-a>
uv run llmbench probe thinking \
    --target <name-a>=http://localhost:8000/v1,<omlx-model-a> \
    --out <results-dir>/<name-a>

uv run llmbench provider isolate omlx <omlx-model-b>
uv run llmbench probe thinking \
    --target <name-b>=http://localhost:8000/v1,<omlx-model-b> \
    --out <results-dir>/<name-b>

uv run llmbench provider isolate mtplx <mtplx-repo-id>
uv run llmbench probe thinking \
    --target mtplx=http://localhost:8003/v1,<mtplx-repo-id> \
    --out <results-dir>/mtplx

uv run llmbench provider restore --unload
```

`<omlx-model-…>` is the name omlx serves (its model directory's basename, as `direct_model` in a suite row) and `<mtplx-repo-id>` the repo id mtplx serves under; `wt model list` shows what your registry holds. Illustrative only, as of issue #403 (2026-10): the answer-shape difference there came from the omlx 8-bit build of Qwen3.8-27B and the short thinking from the 4-bit build, so that issue's run took both omlx builds and mtplx's Quality build, with the names `eval-quant-tier.toml` uses:

```bash
# from: /Users/keith/github/ohanaverse/local-ai-setup/llmbench (illustrative, issue #403)
uv run llmbench provider isolate omlx Qwen3.8-27B-8bit
uv run llmbench probe thinking --target omlx8=http://localhost:8000/v1,Qwen3.8-27B-8bit --out <results-dir>/omlx8
uv run llmbench provider isolate omlx Qwen3.8-27B-4bit
uv run llmbench probe thinking --target omlx4=http://localhost:8000/v1,Qwen3.8-27B-4bit --out <results-dir>/omlx4
uv run llmbench provider isolate mtplx Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality
uv run llmbench probe thinking --target mtplx=http://localhost:8003/v1,Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality --out <results-dir>/mtplx
uv run llmbench provider restore --unload
```

- `--target` is `NAME=BASE_URL,MODEL_ID[,API_KEY_ENV]`. `API_KEY_ENV` is the *name* of an environment variable that holds the key, never the key; leave it out for a keyless server (mtplx, and an omlx configured to allow keyless inference, answer without one). A row with `HTTP 401` means the server wants a key: put it in an environment variable and pass that variable's NAME as `API_KEY_ENV` (never the key). A named variable that is unset is an error before anything is sent. The key goes only into the `Authorization` header; it is never printed or written.
- Sampling defaults to temperature 1.0, top_p 0.95 and top_k 20, as in the Qwen3.x `generation_config.json` and `eval-quant-tier.toml`. Override with `--temperature`, `--top-p`, `--top-k` (`0` leaves top_p or top_k out of the request) and `--max-tokens` (unset by default, so the engine's own cap applies, as in `eval run`). `--timeout` (900 s) bounds the wait per connect and per read, not the whole request: a reply that arrives in pieces can take longer.
- Results go to `--out`: `report.md` and `results.json` (full reasoning and answer text included). Without `--out` the probe makes a new directory under the system temp dir. It never writes to `~/.config/local-ai/benchmarks/` or `latest.toml`. The directory is created and checked before the first request (one it cannot write is a usage error), and both files are rewritten after every finished request, so Ctrl-C keeps what finished: the probe names the request it was waiting on and exits 130.
- An engine that is down, times out, or answers non-200 or non-JSON gets an error row and the run goes on. Exit 0 when at least one target answered, 1 when none did, 2 on a usage error, 130 when interrupted.

Reading `report.md`: one table per prompt, one row per target, then the first 200 characters of each target's reasoning, to compare style. An illustrative row (not a real measurement):

```text
| Target | Status | Reasoning chars | Reasoning tokens | Answer chars | Completion tokens | Finish | Headings | Bullets | Numbered | Table lines | Code fences | Time (s) |
| omlx   | 200    | 412             | —                | 3            | 120               | stop   | 0        | 0       | 0        | 0           | 0           | 4.2      |
```

- **Reasoning length is comparable only between engines that render the same chat template for the same model.** Check the templates match before reading a difference as the engine's.
- `—` under *Reasoning tokens* means the engine did not report `usage.completion_tokens_details.reasoning_tokens`, not zero. *Reasoning chars* 0 means the reply carried neither a reasoning field nor a leading `<think>` block (a `<think>` block is taken out of the answer, so the shape columns count only the answer).
- The shape columns count answer lines outside fenced code blocks; a `# comment` inside a code block is not a heading. *Code fences* counts each block once. A block that is never closed hides every line after it, so a truncated answer's low counts may be only that: check the *Finish* column for `length`.
- Each cell is one sample at temperature 1.0: run the probe more than once before trusting a small difference.

## Gotchas

- **`coding` needs `uv sync --extra eval`** — a plain `make install`/`uv sync` does not pull EvalPlus, since it executes model-generated code and only this one category needs it.
- **`omlx` 4-bit/6-bit disambiguation is the same as every other suite here** — use a row's `provider =` override, and `direct_model =` for the server-side basename. See [05-benchmarks](05-benchmarks.md) Gotchas.
- **A row's `categories =` narrows which categories that row runs**, independent of `--category`'s CLI-level narrowing — both apply, intersected.
- **Thinking models can loop under greedy decoding.** Rows default to temperature 0; a Qwen3.x row can then repeat itself inside its thinking until the server's token cap cuts it off, and the judge scores the leftover thinking. When comparing such models, set the vendor's recommended sampling in `[generation]` for every row of the suite, and compare runs only when their `run.toml` `[suite.generation]` match. Engines also cap a request at different lengths (omlx: `sampling.max_tokens` and `max_context_window` in `~/.omlx/settings.json`), and the thinking counts against the cap.
- **Judging (and coding's EvalPlus grading) both cost real time/spend per item** — a 5-category sweep across many rows adds up; use `--category`/`--row` to scope a suite down while iterating on content or rubrics.
- **`coding`'s shipped config runs the FULL dataset — keep it that way (or set `limit` to the full dataset size).** EvalPlus 0.3.1's grading step requires every problem in the FULL dataset (164 for humaneval) to have a sample before it computes pass@k — `--id-range`/`limit` only bounds what gets *generated*, not what grading demands — so a `limit` smaller than the dataset size makes the `coding` category fail cleanly with a `CodingResult.error` every time, not a score. The reference suite and the coding category both ship with no `limit` for exactly this reason; only set one (a follow-up may address the gap) if you accept an EVALPLUS_ERROR cell in the report.

## Going deeper

- Design spec: `docs/superpowers/specs/2026-09-17-capability-eval-benchmark-design.md`
- Module map: `llmbench/CLAUDE.md`
- Source: `llmbench/src/llmbench/benchmark/eval/`, `llmbench/src/llmbench/benchmark/judge_core.py`
- Speed-only local benchmarks: [05-benchmarks](05-benchmarks.md)
- Agentic repo-level coding benchmark: [09-agent-benchmarks](09-agent-benchmarks.md)
