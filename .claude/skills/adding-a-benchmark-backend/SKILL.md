---
name: adding-a-benchmark-backend
description: Steps to add a new local-model backend to the benchmark scripts and llmbench. Use when asked to add a new benchmark backend or wire a new provider into the isolation/benchmark tooling.
---

## Adding a New Benchmark Backend

Isolation logic lives in **one place**: `llmbench/src/llmbench/providers/lifecycle/`
(issue #79 ported this from the old bash `bin/llm-isolate-provider`/
`bin/llm-restore-providers` scripts, both deleted). `orchestrate.py` drives
isolate/stop/stop-all/restore over a `BACKENDS` registry. ollama, omlx
(both variants) and mlx_lm_server are hand-written `Backend` subclasses in
`backends/<id>.py` (daemon, pool and target+draft protocols); every
process-style provider (mtplx, rec) is an instance of the one spec-driven
`ProcessServerBackend` in `backends/process_server.py`, built from its
entry in `docs/contracts/process-providers.toml`. The bash benchmark scripts
call the `llmbench provider` CLI over this
(`uv run --directory llmbench llmbench provider isolate <id> <model>`, with
`LLM_ISOLATE_*_MODEL` env overrides still honored as a fallback), and the
benchmarks call `orchestrate.py` directly, in-process, via
`llmbench/src/llmbench/benchmark/isolation.py`. Paths below are relative
to `llmbench/` unless they start with another directory; a bare `backends/...` is under
`src/llmbench/providers/lifecycle/`.

### A process-style provider (a server binary llmbench spawns and stops)

No backend module. A provider that is one backgrounded process with a
spawn argv, a stop argv, a pidfile, a log and a readiness check is:

1. **A wt provider table row** with a `Process` spec, its family facts
   (`WtPort`, `Scope`, default origin, readiness) and a `Seed` in
   `wt/internal/provider/table.go` — the `adding-a-provider` skill in
   `wt/.claude/skills/`.
2. **An entry in `docs/contracts/process-providers.toml`** with the same
   values, in the same commit:
   `TestProcessProvidersContractMatchesTheProviderTable`
   (`wt/internal/config/process_providers_contract_test.go`) fails until
   the file and the Go table agree in both directions.
   `llmbench/src/llmbench/providers/process_spec.py` reads it and
   `backends/__init__.py` registers a `ProcessServerBackend` for every
   entry that has no instance yet (mtplx's lives in `backends/mtplx.py`
   only because it passes `cleanup_on_failure=True`). A registry row with
   `auth.secret_ref` gives every probe and the warmup a bearer key;
   readiness is `models-list` (`/v1/models` lists the model) or
   `command-json` (a status command's JSON, like `rec status`).
3. Then steps 3–9 below as they apply: `DEFAULT_PROVIDER_IDS`, the
   `_FAKE_BINARIES` guard in `tests/conftest.py` plus
   `test_hermeticity.py` (the suite must never run the real binary), the
   drift trip-wires, the benchmark scripts, LiteLLM, a smoke test. The
   benchmark runners pass a positional model only for mtplx
   (`benchmark/runner.py`); any other process provider resolves to its
   single registry model.

### A hand-written backend (a daemon, a pool, a pairing)


1. **Add the backend module.** Create
   `src/llmbench/providers/lifecycle/backends/<id>.py` subclassing
   `Backend` (see `backends/base.py` for the interface, and
   `backends/ollama.py` or `backends/mlx_lm_server.py` for a worked example) —
   implement start/stop/warmup and set `id`, `occupancy_key`, `env_var`,
   `default_model`, `health_url`, and `restore_action` ("restart", "stop",
   or "skip"; see `backends/base.py`'s docstring for what each means).
2. **Register it.** Add the module's singleton instance to `BACKENDS` in
   `backends/__init__.py`. `SUPPORTED_PROVIDER_IDS`, in the same file, is
   derived from `BACKENDS`: a registered backend is isolable and stoppable
   by name, by `llmbench provider isolate` and by the benchmarks alike
   (`llmbench.benchmark.isolation.SUPPORTED_PROVIDER_IDS` re-exports it).
3. **Add an env var if it needs one.** If the backend resolves its model
   from a single env var (like ollama/omlx do), add it to
   `ENV_VAR_BY_PROVIDER` in `src/llmbench/local_process.py`. A
   backend that takes a target+draft pairing (like `mlx_lm_server`) uses
   its own two env vars defined in its own backend module instead — see
   `backends/mlx_lm_server.py`'s `TARGET_ENV_VAR`/`DRAFT_ENV_VAR`.
4. **Add a test file.** Model it on
   `tests/providers/lifecycle/backends/test_mlx_lm_server.py`. Follow this
   package's established `unittest.mock.patch` convention: patch the
   module-attribute *as imported into your backend module's own
   namespace* (e.g. `patch("llmbench.providers.lifecycle.backends.<id>.subprocess.run")`,
   `patch("llmbench.providers.lifecycle.backends.<id>.probe.wait_for_port_closed")`),
   not the origin module (`probe.py`, `pidproc.py`) directly. If the
   backend shells out to a binary the suite must never run, add its
   basename to `_FAKE_BINARIES` in `tests/conftest.py` and to the
   parametrized list in `tests/providers/lifecycle/test_hermeticity.py`.
5. **Update the registry.** Add a provider entry to
   `~/.config/local-ai/registry.toml` (by hand, or with `wt model init` once wt
   seeds its row) and
   add the provider id to `DEFAULT_PROVIDER_IDS` in
   `src/llmbench/registry.py` (`LOCAL_PROVIDERS =
   set(DEFAULT_PROVIDER_IDS)` in `src/llmbench/benchmark/runner.py`) — a
   backend missing from that set is silently skipped by `llmbench run` —
   and, if `wt model init` should seed the provider's row, give the provider
   a `Seed` in `wt/internal/provider/table.go` (see the `adding-a-provider`
   skill in `wt/.claude/skills/`).
6. **Update the drift trip-wire.** `tests/benchmark/test_isolation.py`'s
   `test_supported_provider_ids_matches_the_backends_registry_documented_list`
   hand-writes a literal copy of the backend ids, so that registering a
   backend is written down in two places — update that literal.
   `tests/test_main.py`'s `test_provider_list_names_every_backend` lists
   every backend id too.
7. **Wire up the benchmark scripts.** Add entries to `DIRECT_URLS`,
   `DIRECT_MODELS`, `LITELLM_MODELS`, and `ISOLATE_ID` (the associative
   array mapping the script's own backend key to the `llmbench provider`
   CLI's provider id — see `benchmarks/qwen3.8-benchmark` and
   `benchmarks/lib/benchmark-common.sh`'s `isolate_one`/
   `ensure_all_local_started`) in the benchmark script(s) you want it to
   appear in.
8. **Route it through LiteLLM.** Never edit
   `~/.config/litellm/config.yaml` by hand: `wt` owns that file and derives
   the routes from `registry.toml` plus live probes. Once the model is in
   the registry and running, run `wt litellm sync` and confirm the route
   with `wt litellm list`.
9. **Smoke test:** `./benchmarks/qwen3.8-benchmark 30` and
   `uv run llmbench provider isolate <new-backend>` (from `llmbench/`, or
   `uv run --directory llmbench llmbench provider isolate <new-backend>`
   from the repo root).
10. Update the benchmark doc with new numbers.
