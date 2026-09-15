# Final fix wave — whole-branch review of the bash→Python lifecycle port

Branch: `port-lifecycle-to-python`
Worktree: `/Users/keith/github/ohanaverse/local-ai-setup/.worktrees/port-lifecycle-to-python`
Date: 2026-09-15

All 8 items addressed. Full suite green (1259 passed, up from 1241 —
18 new tests). `ruff check`, `mypy`, and `bin/check-links` clean.

## Commits

| SHA | Subject | Items |
| --- | --- | --- |
| `0869599` | `fix(lifecycle): --draft lands in extra_args[1], --keep resolves to occupancy_key` | 1, 2, 8 |
| `d801300` | `fix(lifecycle): give LlamaCppBackend a real restore() so the runbook holds` | 4 |
| `5751bb2` | `test(conftest): intercept every live provider binary, drop the dead TODO` | 5, 6 |
| `06715cd` | `docs(lifecycle): drop stale "not wired" docstrings and internal task numbers` | 3, 7 |

Split rationale: the two CLI behavior fixes plus the cli.py cleanup share
one file and one test file, so they ride together; the llamacpp method +
its runbook paragraph are one change; the conftest hermeticity work and
its dead-branch removal are one change; the pure-documentation edits are
last so they can be read (or reverted) without touching behavior.

---

## 1. CRITICAL — `--draft` was silently swapped with the target

**File:** `modelman/src/modelman/providers/lifecycle/cli.py`

```diff
-    extra_args = (draft,) if draft else ()
+    extra_args: tuple[str, ...] = ("", draft) if draft else ()
```

with a comment block above it recording why index 0 is a deliberately
empty placeholder (`resolve()` reads `extra_args[0] if extra_args and
extra_args[0] else ""`, so an empty string falls through to `model`, then
to `LLM_ISOLATE_MLXLM_MODEL`).

**Error message** (`backends/mlx_lm_server.py`) — the old text pointed at
"positional args", a form that no longer exists anywhere in this
codebase:

```
mlx_lm_server requires target+draft: run `modelman provider isolate
mlx_lm_server <target> --draft <draft>`, or set
LLM_ISOLATE_MLXLM_MODEL/LLM_ISOLATE_MLXLM_DRAFT_MODEL (in-process callers
forward the pair as extra_args=(target, draft))
```

It names `--draft` for the CLI path, keeps both env vars, and names
`extra_args` for `local_control.py` / the benchmark runners.

### New tests — and where they live

Placed in **`modelman/tests/providers/lifecycle/test_cli.py`**, in a
clearly-marked section at the bottom, with the file's module docstring
updated to declare the exception to its "every `lifecycle.*` call is
patched" convention.

Reasoning: the defect is entirely in `cli.py`'s `extra_args`
construction. `backends/test_mlx_lm_server.py` calls `resolve()` directly
and never constructs a CLI invocation, so the bug is structurally
invisible from that file — a test there would have had to hand-build the
same tuple the CLI builds, i.e. re-assert the convention rather than
check it. In test_cli.py the test drives the real
`MlxLmServerBackend` through `orchestrate.isolate()`, patching only what
lives *below* the backend: `binaries.resolve_mlx_lm_bin`, `_PROC`
(spawn/stop), `probe.port_closed_within`, `probe.warmup`, and
`orchestrate._stop_others`. A `mlx_spawn_argv` fixture collects the argv
`mlx_lm.server` would have been spawned with, so assertions read directly
off the resolved target (`argv[2]`) and draft (`argv[4]`).

Coverage, matching the four cases in the brief:

| test | case |
| --- | --- |
| `test_cli_draft_reaches_backend_as_draft_not_target` | target positional + `--draft` |
| `test_cli_draft_with_target_from_env_var` | target from `LLM_ISOLATE_MLXLM_MODEL` + `--draft` |
| `test_cli_without_draft_falls_back_to_draft_env_var` | no `--draft`, draft from env var |
| `test_cli_missing_target_or_draft_fails_cleanly_without_spawning` (parametrized ×2) | draft missing / both missing, no env vars → requires-target+draft envelope, nothing spawned |

Plus two mock-level tests: `test_isolate_forwards_draft_into_extra_args_second_slot`
(asserts `extra_args=("", "draft-model")`) and
`test_isolate_without_draft_passes_empty_extra_args` (asserts `()`, not
`("",)` — a bare placeholder tuple would read as "a target was supplied
positionally" to any backend that checks length rather than truthiness).

### Verified the tests catch the original bug

Temporarily reverted the one-line fix and re-ran:

```
FAILED test_isolate_forwards_draft_into_extra_args_second_slot
FAILED test_cli_draft_reaches_backend_as_draft_not_target
FAILED test_cli_draft_with_target_from_env_var
3 failed, 18 passed
```

with the failure text being exactly the live symptom:
`"error": "mlx_lm_server requires target+draft: ..."`. Fix restored
afterwards.

### Live verification (requested)

```
$ env -u LLM_ISOLATE_MLXLM_MODEL -u LLM_ISOLATE_MLXLM_DRAFT_MODEL \
    uv run --directory modelman modelman provider isolate mlx_lm_server org/target --draft org/draft --json
warning: No MTPLX server is listening on port 8003.
warning: omlx still listening on port 8000
{"provider": "mlx_lm_server", "model": "org/target", "direct_url":
 "http://localhost:8001/v1/chat/completions", "ok": false,
 "error": "port 8001 still in use after stopping prior mlx_lm_server — refusing
 to spawn a replacement that cannot bind (is something else listening on 8001?)"}
```

**Observed failure mode: the pre-bind port check, NOT "requires
target+draft".** That is the expected post-fix result: resolution got all
the way past `resolve()` (which found the real `mlx_lm.server` binary and
built a valid plan with both halves) and failed at the port gate.

Root cause of the port conflict, investigated and confirmed
environmental/pre-existing: PID 74847, started 2026-09-14 23:55 (hours
before this session), is the user's MTPLX GUI app serving
`mtplx-qwen36-35b-a3b-optimized-balance` on **port 8001** — mlx_lm_server's
port. Nothing in this branch put it there, and `modelman provider`'s mtplx
stop targets port 8003.

**Machine left as found.** The isolate run did execute a real
`_stop_others` (that is the command's job). Checked afterwards:
`ollama ps` empty, all `running` flags in `~/.config/local-ai/modelman.toml`
already `false` before and after, LiteLLM on :4000 healthy (HTTP 401 =
up), mtplx :8003 was never up. The one loose end is the `omlx-server`
process (PID 29987, started 2026-09-15 00:35, also before this session):
it holds port 8000 but does not answer `/v1/models` within 8s. It survived
`omlx stop` (hence the "omlx still listening on port 8000" warning) and a
subsequent `modelman provider restore` reported `omlx did not come back up`.
That process predates every command run here — it was already wedged — but
flagging it so it is not mistaken for damage from this work.

## 2. IMPORTANT — `stop-all --keep <id>` did not resolve to `occupancy_key`

**File:** `cli.py::stop_all_cmd`

```python
backend = lifecycle.BACKENDS.get(keep) if keep else None
if keep and backend is None:
    result = LifecycleResult("stop-all", "", "", False, f"unknown provider for --keep: {keep}")
else:
    keep_key = backend.occupancy_key if backend is not None else ""
    result = _run_command(lambda: lifecycle.stop_all(keep_key), provider="stop-all")
```

Error path goes through the same `_emit` / `raise typer.Exit(1)` as every
other failure, so `--json` still emits the exact 5-key envelope.

**Tests** (`test_cli.py`):
- `test_stop_all_keep_resolves_provider_id_to_occupancy_key` — `--keep omlx-6bit`
  reaches `stop_all("omlx")`, i.e. both omlx variants survive.
- `test_stop_all_rejects_unknown_keep_id_instead_of_keeping_nothing` —
  `ok=False`, exit 1, error names the id, and `stop_all` is never called.
- Existing `test_stop_all_forwards_keep` (`--keep omlx`) and
  `test_stop_all_without_keep_passes_empty_string` unchanged and still pass.

**Live verification (requested):**

```
$ uv run --directory modelman modelman provider stop-all --keep bogus-id --json
{"provider": "stop-all", "model": "", "direct_url": "", "ok": false,
 "error": "unknown provider for --keep: bogus-id"}
exit=1
```

Nothing was stopped — the guard runs before `stop_all()`.

## 3. IMPORTANT — stale "not wired" docstrings

`backends/ollama.py`, `backends/omlx.py`, `backends/mlx_lm_server.py` all
carried "Not wired into any live isolate/stop path yet — ... nothing reads
that registry today." Replaced in each with:

> Live: registered in `backends.BACKENDS`, which `orchestrate.py` reads to
> drive `isolate()`/`stop()`/`stop_all()`/`restore()`, reached from
> `modelman provider ...`, `modelman benchmark`, and `local_control.py`.

Also in `omlx.py`, "so a later orchestration task can treat them as one
shared occupant" → names the actual implementation
(`orchestrate._distinct_backends`); in `mlx_lm_server.py`, "lets a later
orchestration task (Task 6) call it" → "lets `orchestrate.isolate()` call
it". `grep -rn "Task [0-9]" src/modelman/providers/lifecycle/` is now
empty.

`cli.py`'s module docstring, previously "This is the last piece before
`bin/llm-isolate-provider` ... can be retired: Task 8 rewrites the bash
benchmark scripts", now states the completed position: both scripts are
deleted, the benchmark scripts call this CLI via `uv run --directory
modelman`, and in-process callers skip the CLI entirely. The `_emit`
docstring's "Task 8's benchmark scripts" reference was reworded the same
way.

## 4. IMPORTANT — llamacpp re-enable runbook promised behavior the code lacked

**Code** (`backends/llamacpp.py`): added a real `restore()` override
mirroring `OmlxBackend.restore()` — guard on `restore_action != "restart"`,
one 2s `urllib.request.urlopen(LLAMACPP_HEALTH_URL)` probe (not a poll
loop), then `launchd.load(launchd.LLAMACPP_PLIST)` and
`probe.wait_for_port_open(..., timeout=probe.RESTORE_WAIT_TIMEOUT)`,
raising `LifecycleError("llamacpp did not come back up (<url>)")` if it
never answers.

**`restore_action` default is unchanged — still `"skip"`.** Its comment now
explains that the field is the documented one-field re-enable switch and
that `restore()` exists precisely so the flip works.

**Tests** (`test_llamacpp.py`), mirroring `test_omlx.py`'s four:
- `test_restore_no_ops_while_restore_action_is_skip` — the shipped default
  stays inert (no probe, no load, no wait).
- `test_restore_no_ops_when_already_up` — probe-up short-circuits.
- `test_restore_loads_plist_and_waits_when_down` — probe-down loads the
  plist and waits on the right URL.
- `test_restore_raises_lifecycle_error_when_it_never_comes_back`.

The last three use a `_restartable()` helper that constructs a
`LlamaCppBackend` with `restore_action = "restart"` — exactly what the
runbook's step 7 produces — rather than mutating the shared `LLAMACPP`
singleton.

**Doc** (`docs/reference/provider-artifacts.md`, step 7) now records that
`restore()` is already implemented and tested and gated on
`restore_action`, so the flip is genuinely all that is needed.

## 5. IMPORTANT — conftest hermeticity gap

**File:** `modelman/tests/conftest.py`

`_fake_launchctl_run` → `_fake_provider_run`, an allow-list wrapper:

```python
_FAKE_BINARIES = frozenset({"launchctl", "omlx", "mtplx", "ollama"})
_FAKE_BINARY_PREFIXES = ("mlx_lm.",)

def _should_fake(argv0: str) -> bool:
    name = os.path.basename(argv0)
    return name in _FAKE_BINARIES or name.startswith(_FAKE_BINARY_PREFIXES)
```

Matched on the **basename**, because `binaries.require_binary("mtplx")`
and `resolve_mlx_lm_bin("server")` hand the backends absolute paths
(`/opt/homebrew/Cellar/omlx/0.10.0/libexec/bin/mlx_lm.server`). Everything
else still delegates to the captured real `subprocess.run`, so git
(`benchmark/agent/workspace.py`), `pi --version`, and the gate runners are
untouched. Wired through the same autouse fixture
(`_never_touch_live_providers`) and the same dotted path as before.

**Deviation, deliberate:** the brief listed `ollama` among the commands to
keep delegating. Investigating the fixture ordering showed that would be a
live hole. Autouse fixtures run in definition order, so
`_never_touch_live_providers` (which patches the shared `subprocess.run`)
runs *after* `_never_call_real_ollama` and silently overwrites its
`backends.ollama.subprocess.run` patch — `subprocess` is one module
object. As shipped on `main`, a `backends/ollama.py` `subprocess.run` call
would have reached the real `ollama` binary; only the separate
`_loaded_model_names` stub kept that path unreachable. `ollama` is
therefore in the allow-list, and routed through the existing
`_fake_ollama_runner` (not the generic canned success) so it keeps the
closed "not found" semantics `_never_call_real_ollama` established. The
now-redundant `backends.ollama.subprocess.run` monkeypatch was dropped;
the `_loaded_model_names` stub is kept and is now unconditional.

**New guard tests:** `modelman/tests/providers/lifecycle/test_hermeticity.py`
- allow-listed argv (launchctl, `omlx stop`, `mtplx stop ...`,
  `/opt/homebrew/bin/mtplx stop`, an absolute Cellar `mlx_lm.server` path)
  must all return returncode 0 — impossible unless the wrapper
  short-circuited, since each would either exit non-zero or raise
  `FileNotFoundError` if really executed.
- `/bin/echo hermetic` must still round-trip through the real
  `subprocess.run`.

Verified meaningful: narrowing `_FAKE_BINARIES` back to `{"launchctl"}`
produced `4 failed, 2 passed`, including a real
`FileNotFoundError: .../mlx_lm.server`.

**Full suite re-run after the change: 1259 passed.** No test was relying
on the real `subprocess.run` reaching any of these binaries.

**Residual, out of scope and not fixed:** `pidproc.PidfileProcess.spawn`
uses `subprocess.Popen`, not `subprocess.run`, so it is outside this
wrapper. It is covered today by per-test patching (`test_pidproc.py`
patches `pidproc.subprocess.Popen`; backend tests patch `_PROC`). The
brief scoped this item to `subprocess.run`, and a global `Popen` patch
would collide with `test_pidproc.py`'s own patches, so it was left alone —
noting it here as the one remaining un-fenced process-spawn path.

## 6. IMPORTANT — dead TODO + unreachable except branch

Confirmed dead: `modelman/src/modelman/providers/lifecycle/backends/ollama.py`
exists and is imported by `backends/__init__.py`, so
`monkeypatch.setattr("...backends.ollama._loaded_model_names", ...)` can
never raise `ImportError`. Removed the `try:`/`except ImportError: pass`
wrapper and the entire 12-line TODO paragraph about pytest's
`derive_importpath`; kept the `_loaded_model_names` patch unconditional
(the sibling `subprocess.run` patch was folded into item 5's allow-list
instead, see the deviation above). No historical note retained — the
behavior it described is now documented where it matters, in
`_fake_provider_run`'s docstring, as the reason one global allow-list is
the only interception there is.

## 7. Bundled Minor — internal task numbers in user-facing guides

- `docs/guides/05-benchmarks.md`: "(Task 6/7's design keeps the env-var
  fallback)" → "(the CLI deliberately keeps the env-var fallback for
  compatibility with the old bash helpers)".
- `docs/guides/10-mlx-lm-quantization.md`: "since Task 7's port from bash"
  → "since the port from the old bash isolation helper".

`git grep -n "Task [0-9]" docs/guides/` is now empty. `bin/check-links`
reports `ALL LINKS OK`.

## 8. Bundled Minor — cli.py cleanup

- `_emit()`'s `success_message` is now a **required** keyword-only
  parameter (the `lambda r: "ok"` default was dead — all four call sites
  pass one). Docstring records why there is no default.
- New `_run_command(fn, *, provider, model="")` helper holds the single
  `try/except Exception` backstop; `isolate`/`stop`/`stop-all`/`restore`
  each call through it. Behavior is byte-identical (same
  `f"{type(exc).__name__}: {exc}"` envelope, same empty `direct_url`,
  same exit codes) — the two existing traceback-suppression tests
  (`test_isolate_exception_from_lifecycle_becomes_clean_envelope_not_traceback`,
  `test_isolate_exception_human_mode_also_clean`) pass unchanged. Removed
  ~20 lines of duplication.

---

## Verification

```
$ cd modelman
$ uv run pytest tests/ -q
1259 passed in 81.14s          # baseline 1241 + 18 new

$ uv run ruff check src/ tests/
All checks passed!

$ uv run mypy src/
Success: no issues found in 75 source files

$ cd .. && uv run bin/check-links
ALL LINKS OK
```

### `ruff format --check` — pre-existing repo-wide failure, unchanged

`uv run ruff format --check src/ tests/` reports **40 files would be
reformatted**, both before and after this work. This is a pre-existing
condition: the Makefile's gate is `make check` = `ruff check` + `mypy`
(see `modelman/Makefile`), and `ruff format --check` is not run by any
Makefile target or CI job — only `make format` (which writes) exists.

Checked each file this wave touched against `HEAD` before the fixes:

```
REFORMAT -> REFORMAT  src/modelman/providers/lifecycle/cli.py
OK       -> OK        src/modelman/providers/lifecycle/backends/llamacpp.py
OK       -> OK        src/modelman/providers/lifecycle/backends/mlx_lm_server.py
OK       -> OK        src/modelman/providers/lifecycle/backends/ollama.py
OK       -> OK        src/modelman/providers/lifecycle/backends/omlx.py
OK       -> OK        tests/conftest.py
REFORMAT -> REFORMAT  tests/providers/lifecycle/test_cli.py
REFORMAT -> REFORMAT  tests/providers/lifecycle/backends/test_llamacpp.py
OK       -> OK        tests/providers/lifecycle/backends/test_mlx_lm_server.py
```

No file changed status — **no new formatting drift was introduced.** Every
hunk this wave added that ruff would have reflowed was hand-adjusted to
ruff's preferred shape; the remaining diagnostics in those three files are
all on pre-existing lines. `tests/providers/lifecycle/test_hermeticity.py`
(new) is format-clean. Running `make format` to clear the other 37 files
was deliberately NOT done — it would bury this review's diff under an
unrelated repo-wide reformat.

## Deviations from the brief

1. **Item 5, `ollama` in the allow-list** rather than delegated — see the
   fixture-ordering analysis under item 5. Delegating it would have left a
   real hole the brief's premise (`_fake_ollama_runner` covers ollama) did
   not account for.
2. **Item 5, `subprocess.Popen` left un-fenced** — the brief scoped the fix
   to `subprocess.run`; noted above as a residual.
3. **Extra test file** (`test_hermeticity.py`) beyond what the brief asked
   for. Item 5 changes an invariant with no test coverage at all; without
   a guard, a future narrowing of the allow-list would go unnoticed until
   it tore down someone's live model.
4. **`ruff format --check` does not pass**, as documented above — it does
   not pass on `main` either, and nothing this wave did made it worse.
