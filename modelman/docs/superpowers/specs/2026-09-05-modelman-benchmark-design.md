# Sub-project 2 — Benchmark Tooling Design

**Scope:** Add a `modelman benchmark` subcommand that replaces `local-ai-setup`'s
ad-hoc bash benchmark scripts with an extensible, registry-aware runner.

**Status:** Design approved in chat; awaiting implementation plan.

**Companion tracker:**
`agent-worktree/docs/superpowers/plans/2026-08-27-model-management-consolidation-status.md`

---

## Background

`local-ai-setup` currently uses one-off bash scripts (`qwen3.8-benchmark`,
`ornith-1.5-benchmark`, etc.) to compare local LLM backends side-by-side.
Those scripts:

- Run the same prompt against Ollama, oMLX, llama.cpp, and OpenRouter.
- Manage service isolation (stop all local services, start only the one being
  tested, warm it up, run, restore).
- Measure TTFT, total time, tokens, and throughput.
- Compare direct backend access against LiteLLM-routed access.
- Write a Markdown report to `/tmp` per run.

They work, but they are hard to extend, not portable, and duplicate model
metadata that already lives in `registry.toml`.

Sub-project 1 established `modelman` as the owner of the shared model registry
and `wt` as a read-only consumer. It makes sense for the benchmark tooling to
live in `modelman` so it can reuse `registry.toml` and the `litellm_exposed`
state stored in `modelman.toml`.

---

## Goals

1. Replace the ad-hoc bash benchmark scripts with a single `modelman benchmark`
   subcommand.
2. Use `registry.toml` and `modelman.toml` to discover which models are
   available and which are exposed through LiteLLM.
3. Keep service-management details in `local-ai-setup` via a small helper
   contract.
4. Support pluggable benchmark workloads with a few built-in defaults.
5. Write machine-readable results (JSON) and human-readable reports (Markdown)
   to disk; avoid bloating `modelman.toml` with benchmark history.
6. Preserve the existing comparison: direct backend access vs LiteLLM-routed
   access.

---

## Non-goals

- Re-implement `llm-restart` or LaunchAgent management inside `modelman`.
- Support arbitrary cloud APIs beyond what LiteLLM already routes.
- Build a web UI or database-backed history system.
- Add per-token cost analysis or spend tracking (that is sub-project 3).

---

## Architecture

### Module layout (modelman)

```
src/modelman/
├── benchmark/
│   ├── __init__.py
│   ├── cli.py            # typer wiring for `modelman benchmark`
│   ├── runner.py         # orchestration: isolation, loops, aggregation
│   ├── results.py        # JSON + Markdown report writers
│   ├── workloads/
│   │   ├── __init__.py
│   │   ├── base.py       # Workload protocol + WorkloadSpec dataclass
│   │   └── chat_streaming.py
│   ├── isolation.py      # subprocess contract with local-ai-setup helpers
│   └── errors.py         # BenchmarkError
├── litellm.py            # reuse for LiteLLM config / model_list lookup
├── registry.py           # discover models + providers
└── state.py              # read litellm_exposed flags
```

### Companion helpers (local-ai-setup)

```
bin/
├── llm-isolate-provider <provider-id>
└── llm-restore-providers
```

`modelman` will call these helpers as subprocesses. The helpers own the
macOS/Apple-Silicon-specific service details; `modelman` owns the benchmark
protocol and metrics.

---

## CLI surface

```bash
modelman benchmark run                       # all exposed local models, default workload
modelman benchmark run --workload short      # use a named built-in workload
modelan benchmark run --model ollama/ornith-1.5:35b --model omlx/...
modelman benchmark run --family qwen3.8        # all models in family
modelman benchmark run --direct                # skip LiteLLM routing
modelman benchmark run --litellm               # only via LiteLLM
modelman benchmark run --passes 5 --cooldown 20
modelman benchmark list-workloads
modelman benchmark show-results --latest
```

### Defaults

- **Targets:** every model in `registry.toml` whose `provider_id` is a
  local provider (`ollama`, `llamacpp`, `omlx`) and whose `litellm_exposed`
  flag in `modelman.toml` is `True`.
- **Route:** both direct and via LiteLLM, producing a comparison table.
- **Workload:** `chat`.
- **Passes:** 1 (user can opt into multi-pass with `--passes N`).
- **Cooldown:** 15 seconds between passes.
- **Results directory:** `~/.config/local-ai/benchmarks/<run_id>/`.

### Selection order for workload

1. `--workload <name>` if given.
2. `MODELMAN_BENCHMARK_WORKLOAD` environment variable.
3. `chat` as the built-in default.

---

## Workload abstraction

A workload defines a prompt, request shape, and how to extract metrics from the
response.

```python
@dataclass
class WorkloadSpec:
    name: str
    display_name: str
    prompt: str
    max_tokens: int
    temperature: float
    stream: bool
    stream_options: dict[str, Any] | None = None
```

```python
class Workload(Protocol):
    @property
    def spec(self) -> WorkloadSpec: ...

    def build_payload(self, model_id: str) -> dict[str, Any]: ...

    def run(
        self,
        session: requests.Session,
        url: str,
        payload: dict[str, Any],
    ) -> RawRun: ...

    def metrics(self, raw: RawRun) -> BenchmarkMetrics: ...
```

### Built-in workloads (v1)

| Name    | Purpose | Prompt | `max_tokens` |
|---------|---------|--------|--------------|
| `short` | warmup / TTFT micro-benchmark | `"hi"` | 1 |
| `chat`  | the existing default comparison | REST vs GraphQL explain | 200 |
| `long`  | sustained throughput test | REST vs GraphQL explain | 1024 |
| `code`  | code generation | "Write a Python function that merges two sorted lists." | 200 |

New workloads are added by creating a module under
`src/modelman/benchmark/workloads/` and registering the exported `WorkloadSpec`
name in a known registry list.

---

## Isolation contract

`modelman` will not manage LaunchAgents, `omlx start`, or `ollama stop` directly.
It will call helpers owned by `local-ai-setup`.

### `llm-isolate-provider <provider-id>`

Stop all other local providers and ensure `<provider-id>` is running, loaded,
and warmed up.

Supported provider-ids: `ollama`, `llamacpp`, `omlx`.

On success, print to stdout:

```json
{
  "provider": "ollama",
  "model": "ornith-1.5:35b",
  "direct_url": "http://localhost:11434/v1/chat/completions",
  "ok": true,
  "error": null
}
```

On failure, exit non-zero and print a human-readable error on stderr.

### `llm-restore-providers`

Bring all local providers back up. Called at the end of the run regardless of
success or failure. Idempotent.

### Runner behavior

- If isolation fails for a model, log it, skip that model, and continue.
- Always call `llm-restore-providers` in a `finally` block.
- Allow `--no-isolate` to bypass isolation for non-macOS/non-managed-service
  environments (future work; v1 can document the limitation).

---

## Metrics

Recorded per `(model, provider, route, workload, pass)`:

- `ttft_ms` — time from request start to first streamed token.
- `total_ms` — wall-clock time from request start to final token.
- `completion_tokens` — `usage.completion_tokens` from the last streaming
  chunk, or parsed visible content chunks as a fallback.
- `prompt_tokens` — `usage.prompt_tokens` from the last streaming chunk.
- `throughput_tok_s` — `completion_tokens / (total_ms - ttft_ms) * 1000`.

For multi-pass runs, the Markdown summary reports the median per model/route.
JSON artifacts keep every raw pass for downstream analysis.

---

## Results artifacts

Each run gets a directory:

```
~/.config/local-ai/benchmarks/20260905-143200/
├── payload.json      # workload spec used
├── results.json      # full raw per-pass records + derived medians
└── summary.md        # human-readable comparison table(s)
```

- `payload.json` makes the run reproducible.
- `results.json` is machine-readable so other tools can aggregate, plot, or
  diff runs.
- `summary.md` preserves the format users already expect from the bash scripts.

## State in modelman.toml

Only a pointer to the latest run. No per-model history.

```toml
[benchmarks]
last_run = "2026-09-05T14:32:00Z"
last_run_dir = "/Users/keith/.config/local-ai/benchmarks/20260905-143200"
```

If users want historical analysis, they consume the `~/.config/local-ai/benchmarks/`
directory directly.

---

## Error handling

- `BenchmarkError` is the base exception.
- Failed isolation for one model skips that model; the run continues.
- A failed HTTP request for one model/route skips that record and logs the
  error.
- The CLI exits with code `1` if zero successful records were collected.
- Service restoration is always attempted in a `finally` block.

---

## Testing strategy

- Unit test the workload payload builders and metrics parser with canned
  streaming chunks.
- Unit test the runner's aggregation logic with fake `RawRun` inputs.
- Use subprocess mocks for `llm-isolate-provider` / `llm-restore-providers`.
- Integration-style tests can write a run to a temp `--results-dir` and assert
  the JSON schema.
- The bash helpers in `local-ai-setup` are tested manually; they are thin
  wrappers around the existing service-management commands.

---

## Migration from bash scripts

The old `ornith-1.5-benchmark` and `qwen3.8-benchmark` scripts are not
deleted in v1. After `modelman benchmark` is verified against the same
workloads, the scripts can be retired in a follow-up PR. Their Markdown
history under `local-ai-setup/benchmarks/results/` is left untouched.

---

## Future extensions (out of scope for v1)

- Non-streaming request workloads.
- Batch / concurrent request workloads.
- Tool-calling workloads.
- `--no-isolate` for Linux/cloud-only runs.
- Diffing two benchmark runs.
- Exporting results to sub-project 3's usage/spend tracking.

---

## Dependencies

- `requests` for HTTP (likely already present transitively; add explicitly).
- `typer` for CLI (already a dependency).
- `tomli` / `tomllib` for state/registry (already used).
