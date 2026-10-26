# llmbench reads the provider description; rec isolation backend (PR 4) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `llmbench provider isolate rec` (and a benchmark row on provider `rec`) works, driven by the same process-provider description wt uses, with mtplx moved onto the same generic backend so llmbench no longer hand-codes a Python copy of each process server's lifecycle.

**Architecture:** one contract file, `docs/contracts/process-providers.toml`, describes each process-style provider (binary, spawn/stop argv templates, pidfile/log, readiness check, scope, default origin). wt's Go table must equal it (a contract test, both directions); llmbench reads it at runtime. A generic Python `ProcessServerBackend` is built from an entry; mtplx and rec are two instances. Probes gain the provider's bearer key (resolved from the registry row's `secret_ref`) and a command-JSON readiness check, mirroring wt's PR 2/3 behavior.

**Tech Stack:** Python 3.13 (`llmbench/`, Typer, pytest, mypy, ruff), Go 1.26.7 for the contract test (`wt/`), TOML.

**Spec:** `docs/superpowers/specs/2026-10-25-provider-interface-and-rec-design.md` (PR 4 of its Delivery section: "llmbench reads the description; rec isolation backend"; "shared defaults file in `docs/contracts/` with a contract test").

**Base:** `main` at `00e404c9` (PRs 1–3 merged). Branch `feat/llmbench-rec`.

**Departures / decisions (mine; say so if you disagree):** (1) the contract file is a mirror of wt's Go table, not the source wt reads at runtime (wt's table stays Go data, pinned by a test), because wt is a single binary with no repo checkout at runtime; llmbench always runs from the checkout (`uv run --directory llmbench`). (2) llmbench implements the lifecycle in Python rather than shelling out to `wt start`/`wt stop`: a `wt start` writes LiteLLM routes and restarts the proxy, which a benchmark isolation must not do. (3) mlx_lm_server, ollama and omlx keep their hand-written backends (pairing, daemon and pool protocols). (4) llmbench's foreign-instance rule differs from wt's on purpose: an isolation already stops every other provider without asking (that is its contract), so a rec instance serving another model is stopped, not asked about; one serving the requested model at the registry's address is reused.

## Global Constraints

- mtplx behavior in llmbench is unchanged: spawn argv `mtplx serve --model M --port 8003 --host 127.0.0.1 --model-id M`, stop argv `mtplx stop --port 8003 --grace-seconds 10`, pidfile `/tmp/local-ai-setup-mtplx.pid`, log `/tmp/local-ai-setup-mtplx.log` (the pidfile is shared with wt), `restore_action = "stop"`, `cleanup_on_failure = True`, `occupancy_key = "mtplx"`, the ordered error texts existing tests pin. Existing mtplx tests change only at call sites (patch targets move from `backends.mtplx.*` to the generic backend's module), never their assertions.
- rec facts (verified live, rec 0.1.0): `rec serve --port N` (foreground; llmbench detaches it like mtplx), `rec stop` (exit 1 with `rec: no managed rec instance is reachable` when nothing runs), `rec status` one JSON line (`"ready":true`, `"model":"flint"`, `"endpoint":"http://127.0.0.1:11500/v1"`; exit 0 even when `ready:false`), one instance per machine, port closed ~10–12 s while loading, `/health` is 200 before ready, every route needs `Authorization: Bearer <key>`, the served model id is exactly `flint`, pidfile `/tmp/local-ai-setup-rec.pid`, log `/tmp/local-ai-setup-rec.log`, default origin `http://127.0.0.1:11500`.
- The registry's rec row (written by wt) carries `auth.base_url` and `auth.secret_ref = "exec:rec auth token"`; llmbench reads both, resolves the secret like wt's `config.ResolveSecret` (an `exec:` command, an environment-variable name, or a literal) and never prints, logs or stores a key; a failing assertion compares booleans, not values (`llmbench/CLAUDE.md`).
- A provider with no `secret_ref` sends no `Authorization` header on any probe (ollama, omlx, mtplx, mlx_lm_server probes stay as they are).
- `registry.py` stays read-only and accepts unknown keys. Tests run under the scratch `HOME` and guards in `tests/conftest.py`: `subprocess.run` never executes `launchctl`/`omlx`/`mtplx`/`ollama`/`wt`/`mlx_lm.*` (add `rec`), `urlopen` raises, no real port, no real rec.
- Python 3.13, ruff `B905` (`zip(strict=)`), `ruff format`, mypy: `make check` passes before any task is done (`llmbench/CLAUDE.md`). Every new Go `Test*` has a top-level `//` comment saying what and why. Commit messages end with `Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>`.
- Docs show commands, never live model state.

## Review Focus

1. **mtplx unchanged**: argv, pidfile, stop command, ordering of `cleanup_on_failure`, and every pinned error string. Pinned in Task 4.
2. **No key, no header** for key-less providers; a key never appears in an exception, log line, envelope or test failure. Pinned in Tasks 2 and 3.
3. **rec readiness**: `rec status` failing (exit 1, empty, bad JSON, missing path) is "not ready", never a crash or ready; the process dying fails fast with the log tail. Pinned in Task 3.
4. **Isolate rec**: stops the other providers first; a rec instance serving another model, or the right model on another address, is stopped before spawning; one serving the right model at the registry's address is reused (no spawn). Pinned in Task 5.
5. **Restore**: `restore` stops a running rec (never part of the standing baseline) and `stop_all` includes it. Pinned in Task 5.
6. **Contract drift**: changing a field in wt's Go table or in the contract file alone fails a test. Pinned in Task 1.
7. **Unreadable key**: `isolate rec` with a failing `rec auth token` returns an envelope with `ok=false` and a message naming the family, before spawning anything. Pinned in Task 5.

---

### Task 1: the contract file and wt's equality test

**Files:**
- Create: `docs/contracts/process-providers.toml`, `wt/internal/config/process_providers_contract_test.go`
- Modify: `docs/contracts/README` or the contracts list in `CLAUDE.md` only if one enumerates fixtures

**Interfaces:**
- Produces (exact): one `[[process]]` table per process family, keys: `family`, `provider` (the first provider id of the family), `binary`, `spawn` (array), `stop` (array), `pidfile`, `log`, `pidfile_env`, `subcommand`, `daemon_module`, `port_flag`, `model_flag` (empty string when absent), `scope` (`"port"` or `"machine"`), `default_origin`, `wt_port`, `restore_action` (`"stop"`), `ready` sub-table with `kind`, `command` (array), `path`, `equals`, `model_path`, `endpoint_path` (empty/absent when unused). Entries for `mtplx` and `rec` with the values in wt's table (`wt/internal/provider/table.go`: copy them exactly; do not invent).
- The Go test (in `internal/config`, which already depends on a TOML library and reads `docs/contracts` fixtures; follow `registry_fixture_test.go`'s path helper): `TestProcessProvidersContractMatchesTheProviderTable` parses the file and asserts, for every family in `provider.ProcessFamilies()`, a field-by-field match with `provider.ProcessFor(family)` and `provider.FamilyByName(family)` (origin, `WtPort`, `Scope`, `Ready`), and that the file has no entry for a family the table lacks (both directions).

- [ ] **Step 1: Write the failing test** (the file does not exist yet): `cd wt && go test ./internal/config -run TestProcessProvidersContract`. Expected: FAIL (file missing).
- [ ] **Step 2: Write the TOML file** from the Go values; run again. Expected: PASS.
- [ ] **Step 3: Prove it detects drift**: temporarily change one value in the TOML (for example rec's `spawn`), see the test fail naming the field, revert; say so in the report.
- [ ] **Step 4: Commit** — `test(wt): process-providers contract file and the table-equality test`.

### Task 2: Python spec loader, registry auth fields, secret resolver

**Files:**
- Create: `llmbench/src/llmbench/providers/process_spec.py`, `llmbench/src/llmbench/providers/secret.py`, `llmbench/tests/providers/test_process_spec.py`, `llmbench/tests/providers/test_secret.py`
- Modify: `llmbench/src/llmbench/registry.py` (`ProviderEntry.base_url`, `ProviderEntry.secret_ref`), `llmbench/tests/test_registry.py` (add cases)

**Interfaces:**
- Produces (exact):
  - `@dataclass(frozen=True) class Ready: kind: str; command: tuple[str, ...]; path: str; equals: str; model_path: str; endpoint_path: str`.
  - `@dataclass(frozen=True) class ProcessSpec: family: str; provider: str; binary: str; spawn: tuple[str, ...]; stop: tuple[str, ...]; pidfile: str; log: str; pidfile_env: str; subcommand: str; daemon_module: str; port_flag: str; model_flag: str; scope: str; default_origin: str; wt_port: int; restore_action: str; ready: Ready`.
  - `def process_specs() -> dict[str, ProcessSpec]` (keyed by provider id; cached; reads the contract file) and `def process_spec(provider_id: str) -> ProcessSpec | None`; `def contract_path() -> Path` (repo-relative from this module; environment override `LLMBENCH_PROCESS_PROVIDERS` for tests; the depth dependency is pinned by a test that the real file exists, and noted in `llmbench/CLAUDE.md`'s depth warning).
  - `def expand(template: tuple[str, ...], *, model: str = "", port: int = 0) -> list[str]` replacing `{model}` and `{port}`.
  - `ProviderEntry` gains `base_url: str = ""` and `secret_ref: str = ""` read from `auth.base_url` / `auth.secret_ref` (absent → empty).
  - `secret.py`: `def resolve_secret(ref: str) -> str` implementing wt's rules (`exec:<command with args split on whitespace>` run with a 15 s timeout and stdout stripped; a bare `^[A-Z][A-Z0-9_]*$` or `os.environ/NAME` reads the environment variable, missing → `""`; anything else is the literal), raising `SecretError` (a `LifecycleError` subclass) with a message that names the reference but never a value when an `exec:` command fails or times out.

- [ ] **Step 1: Write the failing tests**: `test_process_spec.py` loads the real contract file and asserts mtplx's spawn/stop/pidfile and rec's values from Global Constraints and `expand`; a missing key or unknown `ready.kind` in a synthetic file raises a clear error; `test_secret.py` covers the three forms with a fake command (a tiny script in `tmp_path`), a failing command (error names the ref, never output), a timeout (patch the timeout), the env-name form with the variable set and unset; `test_registry.py` adds a provider row with `auth.base_url`/`secret_ref` and one without.
- [ ] **Step 2: Run to verify failure** — `cd llmbench && uv run pytest tests/providers/test_process_spec.py tests/providers/test_secret.py tests/test_registry.py -q`. Expected: FAIL.
- [ ] **Step 3: Implement**; `uv run pytest -q` for the whole suite and `make check`. Expected: PASS.
- [ ] **Step 4: Commit** — `feat(llmbench): read the process-provider contract, the registry auth fields and the secret reference`.

### Task 3: keyed probes and command-JSON readiness

**Files:**
- Modify: `llmbench/src/llmbench/providers/lifecycle/probe.py` and its tests (`llmbench/tests/providers/lifecycle/test_probe.py`, plus `tests/probe/conftest.py` fake endpoint if it needs a bearer mode); `llmbench/tests/conftest.py` (add `rec` to the blocked-executable list of the autouse `subprocess.run` guard) and `llmbench/tests/providers/lifecycle/test_hermeticity.py`'s pinned allow-list, FIRST, so no later test can ever execute the real `rec`

**Interfaces:**
- Produces (exact): an optional keyword `key: str | None = None` on `port_closed_within`, `wait_for_port_open`, `wait_for_model`, `serving_model` and `warmup`; with a key every request carries `Authorization: Bearer <key>`; without one no header is sent (byte-identical to today). A 401/403 with a key present raises `WarmupRefusedError` from `warmup` (as today) and from `wait_for_model` immediately (no polling to the timeout). New `def command_json(argv: list[str], *, path: str, timeout: float = 5.0) -> object | None` (runs the command via `subprocess.run`, parses stdout as a JSON object, walks the dot path, returns the scalar or `None` for exit non-zero, empty or invalid output, a missing path, or a timeout; never raises) and `def wait_for_command_ready(argv, *, path, equals, proc=None, timeout) -> None` (polls every second; returns when the scalar rendered as text equals `equals` (a bool renders `"true"`/`"false"`); raises `LifecycleError("serve process exited during model load: …")` the moment `proc` has exited; raises a timeout `LifecycleError` naming the argv's binary at the deadline).

- [ ] **Step 1: Write the failing tests** (the fake endpoint in `tests/probe/conftest.py`; `urlopen` is patched to raise in tests, so reuse its mechanism as the existing probe tests do): no key → the endpoint sees no `Authorization` header; with a key → `Bearer k`; 401 with a key → `wait_for_model` raises at once; `command_json` for `{"ready":true}`, nonzero exit, empty, `not json`, `{}`, a list root, a timeout; `wait_for_command_ready` with a fake `subprocess.run` returning not-ready twice then ready; a dead `proc` fails fast; timeout.
- [ ] **Step 2: Run to verify failure**, **Step 3: Implement**, **Step 4: Run** — `cd llmbench && uv run pytest -q && make check`. Expected: PASS, all existing probe tests unchanged.
- [ ] **Step 5: Commit** — `feat(llmbench): probes carry the provider's key; command-json readiness`.

### Task 4: the generic ProcessServerBackend; mtplx moves onto it

**Files:**
- Create: `llmbench/src/llmbench/providers/lifecycle/backends/process_server.py`, `llmbench/tests/providers/lifecycle/backends/test_process_server.py`
- Modify: `llmbench/src/llmbench/providers/lifecycle/backends/mtplx.py` (becomes `MTPLX = ProcessServerBackend(process_spec("mtplx"))` plus nothing else), `backends/__init__.py` (register every process spec's backend), `llmbench/src/llmbench/providers/mtplx.py` and `llmbench/src/llmbench/registry.py` (`DEFAULT_PROVIDER_IDS` gains `rec`), and the existing mtplx backend tests (call sites only)

**Interfaces:**
- Produces (exact): `class ProcessServerBackend(PidfileTrackedBackend)` constructed from a `ProcessSpec`: `id = spec.provider`, `occupancy_key = spec.provider`, `env_var = None`, `default_model = None`, `restore_action = spec.restore_action`, `cleanup_on_failure = (spec.provider == "mtplx")` is NOT allowed — instead a spec-independent class attribute set by the constructor argument `cleanup_on_failure: bool` (True for mtplx, False for rec; the ledger of the old code says "the ONE backend that sets this True": keep mtplx the only True). `health_url`/`chat_url` come from the registry row's `auth.base_url` when the provider row names one, else `spec.default_origin`; the port passed to `{port}` is that URL's port (`spec.wt_port` when absent). `resolve`, `already_serving`, `replace_own_occupant`, `start`, `wait_ready`, `warm`, `stop_and_wait` as in today's `MtplxBackend` but spec-driven: `start` spawns `[binary] + expand(spec.spawn, model, port)` through a `PidfileProcess(name, pidfile, log)`; `wait_ready` uses `probe.wait_for_model` for `models-lists` and `probe.wait_for_command_ready` for `command-json`; `stop_and_wait` runs `[binary] + expand(spec.stop, port=port)` and then confirms with the port-closed poll (`models-lists`) or with the readiness command reporting not ready (`command-json`), returning the same warning strings as today for mtplx ("mtplx binary not found on PATH", the stop command's stderr/stdout else "mtplx stop failed", "mtplx still listening on port 8003" — derive the binary and port from the spec so the mtplx text is byte-identical); `already_serving` for `command-json` is true only when the status reports ready with the requested model and (when `endpoint_path` is set) an endpoint whose origin equals the backend's origin; the key (when the provider row names a `secret_ref`) is resolved once per call and passed to every probe; an unreadable secret raises `LifecycleError` naming the provider before anything is spawned.

- [ ] **Step 1: Write the failing tests** for the generic backend with a synthetic spec and fakes (patch the names imported into the new module, per `llmbench/CLAUDE.md`): the spawn argv expands `{model}`/`{port}`; `resolve` explicit-model / positional / single-registry-model / ambiguity errors identical in text to the old mtplx ones for provider `mtplx`; `already_serving` for both ready kinds; stop returns `None` on a clean stop and the exact legacy warnings; `command-json` stop confirmation; key passed to probes only when the registry row has a `secret_ref`; an unreadable secret raises before `spawn`. Then run the EXISTING mtplx tests unchanged to see them fail on the patch targets (`backends.mtplx.subprocess.run`, `.probe.wait_for_port_closed`, `._PROC`).
- [ ] **Step 2: Implement** `process_server.py`; reduce `mtplx.py`; register backends from `process_specs()` in `__init__.py` (keeping `SUPPORTED_PROVIDER_IDS = frozenset(BACKENDS)`); add `rec` to `DEFAULT_PROVIDER_IDS`. Update the existing mtplx tests' patch targets to the new module (call-site edits only) and the drift trip-wires named in the `adding-a-benchmark-backend` skill (`test_isolation.py`'s literal id list, `test_main.py`'s provider list) to include `rec`.
- [ ] **Step 3: Run** — `cd llmbench && uv run pytest -q && make check`. Expected: PASS; mtplx assertions untouched.
- [ ] **Step 4: Commit** — `refactor(llmbench): one spec-driven process-server backend; mtplx runs on it`.

### Task 5: rec through the orchestrator

**Files:**
- Create: `llmbench/tests/providers/lifecycle/test_rec.py`
- Modify: `llmbench/src/llmbench/benchmark/**/runner.py` only if a test shows a provider-name branch rejects `rec` (the `rec` execution guard in `tests/conftest.py` was added in Task 3)

**Interfaces:**
- Consumes: Tasks 1–4 (the rec backend is registered from the contract file; no rec-specific Python).

- [ ] **Step 1: Write the tests** (orchestrator-level, fakes for `subprocess`/probes/`rec status`; registry fixture with a `rec` provider row `auth.base_url` + `secret_ref = "k"` literal and a model `rec/flint` with `model_name = "flint"`): `test_isolate_rec_stops_others_then_spawns` (the other providers' `stop_and_wait` run first; spawn argv `rec serve --port 11500`; readiness polled until `{"ready":true,"model":"flint","endpoint":"http://127.0.0.1:11500/v1"}`; warmup carries `Bearer k` and model `flint`; envelope `ok=True`, model `flint`); `test_isolate_rec_reuses_a_right_instance` (status ready, model flint, endpoint matches: no spawn, no stop); `test_isolate_rec_replaces_another_model` and `test_isolate_rec_replaces_the_right_model_on_another_address` (stop command, status not ready, then spawn); `test_isolate_rec_unreadable_key` (`secret_ref = "exec:/nonexistent/k"`: `ok=False`, message names rec, no spawn, no key text anywhere); `test_isolate_rec_missing_binary`; `test_stop_rec` and `test_stop_all_includes_rec`; `test_restore_stops_a_running_rec` (restore_action `stop`); `test_rec_is_never_in_the_standing_baseline`; `test_rec_process_dying_fails_fast_with_log_tail`.
- [ ] **Step 2: Run to verify failure or prove-by-mutation**: tests that pass on first run are fine only if you state the regression each guards and show one scratch mutation failing it (key header dropped, `restore_action` changed, the endpoint compare removed).
- [ ] **Step 3: Run** — `cd llmbench && uv run pytest -q && make check`. Expected: PASS.
- [ ] **Step 4: Commit** — `test(llmbench): rec through the orchestrator`.

### Task 6: docs, skill and verification (including a live check)

**Files:**
- Modify: `docs/guides/05-benchmarks.md` (rec as a benchmark provider: `provider isolate rec`, `provider stop rec`, `restore` stops it; commands only), `docs/reference/provider-artifacts.md` (llmbench now reads the contract file; rec pidfile/log), `llmbench/docs/internals.md` (new modules), `llmbench/CLAUDE.md` (the contract file, the depth dependency of its path, the new key rule, the rec guard in `tests/conftest.py`), `.claude/skills/adding-a-benchmark-backend/SKILL.md` (a process-style provider is a contract-file entry plus a wt table row; no backend module), root `CLAUDE.md` (contracts list), `wt/docs/internals/local-models.md` (the contract file and its test; "llmbench has no rec backend yet" limit removed)

- [ ] **Step 1: Update the docs.** No live state in guides.
- [ ] **Step 2: Verify** — `make -C . lint`, `make test-all` (from the worktree root; both exit 0), `cd llmbench && make check`.
- [ ] **Step 3: Live check with the real rec** (allowed for testing; rec is stopped when you start — check `rec status`; if an instance is running, only test the reuse path and leave it running): from the worktree root with a scratch registry (`WT_REGISTRY` pointing at a scratch file created by the real flow `wt model init` + `wt model add rec flint --family flint`, all llmbench state redirected with `LLMBENCH_LATEST` and `--results-dir` under the scratchpad, `HOME` real so rec's data dir is found): `uv run --directory llmbench llmbench provider list` shows rec; `provider isolate rec` returns an `ok` envelope (record elapsed time; redact the key everywhere); a direct `curl` chat to `flint` with the key from `rec auth token` answers; a second `provider isolate rec` reuses the instance; `provider stop rec` leaves `rec status` exiting 1 with nothing on 11500; `provider isolate rec` then `provider restore` also leaves rec stopped. Leave rec stopped.
- [ ] **Step 4: Commit** — `docs: llmbench reads the process-provider contract; rec benchmark provider`.
- [ ] **Step 5: Push and open the PR only when the user asks.**

---

## Self-review

- **Spec coverage:** PR 4's list (llmbench reads the description; rec isolation backend; shared defaults file with a drift test) → Tasks 1–5; docs and skill rewrite → Task 6; the spec's open "how much of the Python lifecycle can be deleted versus wrapped" is answered in the Decisions header.
- **Type consistency:** `ProcessSpec`/`Ready`/`expand`/`process_spec` (Task 2) are used unchanged in Tasks 3–5; the probe `key`/`command_json`/`wait_for_command_ready` signatures (Task 3) are consumed by the backend (Task 4).
- **Proportion:** names, signatures, values and tests only; the old mtplx behavior is preserved by reference to its existing tests, not re-described.
