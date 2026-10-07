---
name: adding-a-benchmark-backend
description: Steps to add a new local-model backend to the benchmark scripts and llmbench. Use when asked to add a new benchmark backend or wire a new provider into the isolation/benchmark tooling.
---

## Adding a New Benchmark Backend

Isolation logic lives in **one place**: `llmbench/src/llmbench/providers/lifecycle/`
(issue #79 ported this from the old bash `bin/llm-isolate-provider`/
`bin/llm-restore-providers` scripts, both deleted). `orchestrate.py` drives
isolate/stop/stop-all/restore over a `BACKENDS` registry; each provider is
one `Backend` subclass in `backends/<id>.py`. The bash benchmark scripts
call the `llmbench provider` CLI over this
(`uv run --directory llmbench llmbench provider isolate <id> <model>`, with
`LLM_ISOLATE_*_MODEL` env overrides still honored as a fallback), and the
benchmarks call `orchestrate.py` directly, in-process, via
`llmbench/src/llmbench/benchmark/isolation.py`. `modelman start`/`stop` reach
the same code through modelman's path dependency on llmbench, so run
modelman's suite too. Paths below are relative to `llmbench/` unless they
start with another directory. Adding a backend:

1. **Add the backend module.** Create
   `src/llmbench/providers/lifecycle/backends/<id>.py` subclassing
   `Backend` (see `backends/base.py` for the interface, and
   `backends/mtplx.py` or `backends/ollama.py` for a worked example) —
   implement start/stop/warmup and set `id`, `occupancy_key`, `env_var`,
   `default_model`, `health_url`, and `restore_action` ("restart", "stop",
   or "skip"; see `backends/base.py`'s docstring for what each means).
2. **Register it.** Add the module's singleton instance to `BACKENDS` in
   `backends/__init__.py`, and — if it's a fully-supported (not
   retired-only) backend — add its id to `SUPPORTED_PROVIDER_IDS` in the
   same file. This is the one place both `llmbench provider isolate` and
   the benchmarks check isolability from
   (`llmbench.benchmark.isolation.SUPPORTED_PROVIDER_IDS` just re-exports
   it).
3. **Add an env var if it needs one.** If the backend resolves its model
   from a single env var (like ollama/omlx do), add it to
   `ENV_VAR_BY_PROVIDER` in `src/llmbench/local_process.py`. A
   backend that takes a target+draft pairing (like `mlx_lm_server`) uses
   its own two env vars defined in its own backend module instead — see
   `backends/mlx_lm_server.py`'s `TARGET_ENV_VAR`/`DRAFT_ENV_VAR`.
4. **Add a test file.** Model it on
   `tests/providers/lifecycle/backends/test_mtplx.py`. Follow this
   package's established `unittest.mock.patch` convention: patch the
   module-attribute *as imported into your backend module's own
   namespace* (e.g. `patch("llmbench.providers.lifecycle.backends.<id>.subprocess.run")`,
   `patch("llmbench.providers.lifecycle.backends.<id>.probe.wait_for_port_closed")`),
   not the origin module (`probe.py`, `pidproc.py`) directly. If the
   backend shells out to a binary the suite must never run, add its
   basename to `_FAKE_BINARIES` in `tests/conftest.py` **and** in
   `modelman/tests/conftest.py`, and to the parametrized list in
   `tests/providers/lifecycle/test_hermeticity.py` **and** its copy,
   `modelman/tests/test_hermeticity.py`.
5. **Update the registry.** Add a provider entry to
   `~/.config/local-ai/registry.toml` (via `modelman sync` or the TUI) and
   add the provider id to `DEFAULT_PROVIDER_IDS` in
   `src/llmbench/registry.py` (`LOCAL_PROVIDERS =
   set(DEFAULT_PROVIDER_IDS)` in `src/llmbench/benchmark/runner.py`) — a
   backend missing from that set is silently skipped by `llmbench run` —
   and, until modelman is retired, in `modelman/src/modelman/registry.py`,
   which `modelman sync` and the TUI seed provider rows from
   (`modelman/tests/test_llmbench_reexports.py` fails if the two differ).
6. **Update the drift trip-wire.** `tests/benchmark/test_isolation.py`'s
   `test_supported_provider_ids_matches_the_backends_registry_documented_list`
   hand-writes a literal copy of `SUPPORTED_PROVIDER_IDS` specifically so a
   backend added to `BACKENDS` without an isolability decision fails a
   test instead of silently running unisolated — update that literal
   alongside `backends/__init__.py`'s constant. `tests/test_main.py`'s
   `test_provider_list_names_every_backend` lists every backend id too.
7. **Wire up the benchmark scripts.** Add entries to `DIRECT_URLS`,
   `DIRECT_MODELS`, `LITELLM_MODELS`, and `ISOLATE_ID` (the associative
   array mapping the script's own backend key to the `llmbench provider`
   CLI's provider id — see `benchmarks/qwen3.8-benchmark` and
   `benchmarks/lib/benchmark-common.sh`'s `isolate_one`/
   `ensure_all_local_started`) in the benchmark script(s) you want it to
   appear in.
8. Add the model to `~/.config/litellm/config.yaml`.
9. **Smoke test:** `./benchmarks/qwen3.8-benchmark 30` and
   `uv run llmbench provider isolate <new-backend>` (from `llmbench/`, or
   `uv run --directory llmbench llmbench provider isolate <new-backend>`
   from the repo root).
10. Update the benchmark doc with new numbers.
