# MTPLX Code-Review Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix all 10 findings from the `/code-review` run on branch `mtplx-provider` (commits `6569129`..`7f263ca`): two model-name-forwarding gaps left over from that branch's own review-fix pass, one metadata-corruption bug in the Add-Model form, one docstring correctness claim, and six efficiency/duplication cleanups in the mtplx lifecycle code.

**Architecture:** Each finding is fixed in place with the minimal targeted change; no new abstractions except where the review explicitly asked for one (Task 10's provider capability flag, which replaces two independent hardcoded provider-id lists).

**Tech Stack:** Python 3.13 (modelman, pytest/ruff/mypy via `uv run`), Bash (benchmarks/, bin/ — validated with `bash -n` + `shellcheck`).

**Spec:** No separate spec doc — this plan's spec is the `/code-review` findings list already reported to the user in this conversation (10 findings, most-severe first: 4 correctness bugs, then 6 efficiency/duplication cleanups).

## Global Constraints

- Work happens in the current worktree (`mtplx-provider` branch) — this is a linked worktree, never the primary checkout, so no worktree-invariant concerns.
- Every Python change must pass `cd modelman && make check` (ruff + mypy) and the focused tests named in each task before commit.
- Every Bash change must pass `make lint-shell` (`bash -n` + `shellcheck --severity=error`) before commit.
- Follow existing docstring/comment density and style in each file (this repo writes explanatory "why" comments; match that, don't strip it).
- One commit per task, referencing the finding it fixes (not "completes plan item" language — these are post-execution review fixes, so use a scope tag per this user's CLAUDE.md guidance, e.g. `fix(mtplx): ...`).

---

### Task 1: Forward the mtplx model name through the bash benchmark's `isolate_one()`

**Finding:** `benchmarks/lib/benchmark-common.sh:14` — `isolate_one()` never forwards a model name for providers with no `ISOLATE_ENV` entry (mtplx), so the qwen3.8 benchmark's mtplx isolation silently ignores `DIRECT_MODELS[mtplx]` and falls back to whichever mtplx model is first in the registry.

**Files:**
- Modify: `benchmarks/lib/benchmark-common.sh:36-50` (`isolate_one`)
- Modify: `benchmarks/qwen3.8-benchmark:76-78` (stale comment about why `ISOLATE_ENV[mtplx]` is omitted)

**Interfaces:**
- Consumes: `bin/llm-isolate-provider`'s existing `mtplx` case branch, which already reads `"${2:-}"` as an optional model arg (see `bin/llm-isolate-provider:248-260` — no change needed there).
- Produces: no new interface; `isolate_one()`'s behavior for any future no-`ISOLATE_ENV` backend also gets a positional model arg, matching `modelman/src/modelman/benchmark/runner.py`'s `elif target.provider_id == "mtplx": extra_args = (target.model_name,)` pattern (the sibling this branch already fixed).

- [ ] **Step 1: Edit `isolate_one()` to pass the model positionally when there's no env var**

In `benchmarks/lib/benchmark-common.sh`, replace:

```bash
# Stop all local providers, then start+warmup only the requested backend's
# model (the helper polls until the model actually answers). A backend with
# no ISOLATE_ENV entry (e.g. mtplx — the lifecycle module resolves its model
# from the registry, so no LLM_ISOLATE_*_MODEL override is needed) is called
# with no env override rather than an empty-named `env` assignment.
isolate_one() {
    local key="$1"
    echo "  [isolation] isolating ${ISOLATE_ID[$key]} (${DIRECT_MODELS[$key]})..."
    if [ -n "${ISOLATE_ENV[$key]:-}" ]; then
        env "${ISOLATE_ENV[$key]}=${DIRECT_MODELS[$key]}" \
            "$ISOLATE_HELPER" "${ISOLATE_ID[$key]}" >/dev/null
    else
        "$ISOLATE_HELPER" "${ISOLATE_ID[$key]}" >/dev/null
    fi
}
```

with:

```bash
# Stop all local providers, then start+warmup only the requested backend's
# model (the helper polls until the model actually answers). A backend with
# no ISOLATE_ENV entry (e.g. mtplx) is single-model-per-process and takes its
# model as a positional arg instead of an env var (bin/llm-isolate-provider's
# mtplx case reads $2) — forward DIRECT_MODELS[$key] positionally so
# isolation selects the model this run actually requested, instead of
# falling back to whichever mtplx model happens to be first in the registry.
isolate_one() {
    local key="$1"
    echo "  [isolation] isolating ${ISOLATE_ID[$key]} (${DIRECT_MODELS[$key]})..."
    if [ -n "${ISOLATE_ENV[$key]:-}" ]; then
        env "${ISOLATE_ENV[$key]}=${DIRECT_MODELS[$key]}" \
            "$ISOLATE_HELPER" "${ISOLATE_ID[$key]}" >/dev/null
    else
        "$ISOLATE_HELPER" "${ISOLATE_ID[$key]}" "${DIRECT_MODELS[$key]}" >/dev/null
    fi
}
```

- [ ] **Step 2: Fix the now-stale comment in `qwen3.8-benchmark`**

In `benchmarks/qwen3.8-benchmark`, replace:

```bash
# Env var per backend (helper naming: LLM_ISOLATE_<PROVIDER>_MODEL).
# ISOLATE_ENV[mtplx] is intentionally omitted — the lifecycle module
# resolves the model name from the registry.
```

with:

```bash
# Env var per backend (helper naming: LLM_ISOLATE_<PROVIDER>_MODEL).
# ISOLATE_ENV[mtplx] is intentionally omitted — isolate_one() forwards its
# model as a positional arg instead (mtplx is single-model-per-process, like
# mlx_lm_server's target/draft positional args).
```

- [ ] **Step 3: Verify with shellcheck**

Run: `make lint-shell`
Expected: PASS, no new warnings for either file.

- [ ] **Step 4: Manual sanity check of the argv mtplx would receive**

Run: `bash -c 'source benchmarks/lib/benchmark-common.sh; declare -A ISOLATE_ENV; declare -A ISOLATE_ID=([mtplx]=mtplx); declare -A DIRECT_MODELS=([mtplx]="Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality"); ISOLATE_HELPER=echo; isolate_one mtplx'`
Expected output ends with: `mtplx Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality` (confirms the model is now passed as `$2`).

- [ ] **Step 5: Commit**

```bash
git add benchmarks/lib/benchmark-common.sh benchmarks/qwen3.8-benchmark
git commit -m "$(cat <<'EOF'
fix(benchmark): forward mtplx model name through isolate_one()

isolate_one() only set an env var for backends with an ISOLATE_ENV entry;
mtplx has none (it takes the model positionally) and was silently isolated
with no model arg, falling back to whichever mtplx model is first in the
registry instead of DIRECT_MODELS[mtplx].

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01FQhNTgSfPchXG52G9PLAs1
EOF
)"
```

---

### Task 2: Forward the mtplx model name through the agentic benchmark runner

**Finding:** `modelman/src/modelman/benchmark/agent/runner.py:505` — the agentic benchmark's `extra_args` resolution has no `mtplx` branch, unlike its sibling `modelman/src/modelman/benchmark/runner.py` (which this branch already fixed at line 173-176). Since `mtplx` is now in `SUPPORTED_PROVIDER_IDS` (aliased `ISOLATABLE_PROVIDERS`), an mtplx row is reachable and silently isolates with no model.

**Files:**
- Modify: `modelman/src/modelman/benchmark/agent/runner.py:503-516`
- Test: `modelman/tests/benchmark/agent/test_runner.py` (add a new test near `test_run_suite_reisolates_mlx_lm_server_between_pairings`)

**Interfaces:**
- Consumes: `registry: Registry` (already a parameter of `run_suite`, in scope at the edit site — see line 459/566); `registry.model(row.model_id).model_name: str`.
- Produces: no new interface; matches `modelman/src/modelman/benchmark/runner.py`'s existing `elif target.provider_id == "mtplx": extra_args = (target.model_name,)` branch exactly.

- [ ] **Step 1: Write the failing test**

Add to `modelman/tests/benchmark/agent/test_runner.py`, near `test_run_suite_reisolates_mlx_lm_server_between_pairings`:

```python
def test_run_suite_isolates_mtplx_with_model_name(tmp_path, monkeypatch):
    """An mtplx row must pass its registry repo id as the isolate extra_args,
    mirroring modelman.benchmark.runner's existing mtplx branch — mtplx is
    single-model-per-process and has no baked-in default, so an isolate call
    with no model arg silently serves whichever mtplx model happens to be
    first in the registry instead of the row's model."""
    calls: list[tuple] = []

    def _isolate(pid, *extra_args):
        calls.append((pid, *extra_args))

    monkeypatch.setattr(isolation_module, "isolate_provider", _isolate)
    monkeypatch.setattr(isolation_module, "restore_providers", lambda: None)
    monkeypatch.setattr(pidriver_module, "run_pi_process", _no_diff_run)

    registry = Registry(
        providers=[ProviderEntry(id="mtplx", name="MTPLX", location="local")],
        models=[
            ModelEntry(
                id="mtplx/model",
                family="f",
                provider_id="mtplx",
                model_name="Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality",
            ),
        ],
    )

    body = _suite_toml(MINI_DRIFT, models='["mtplx/model"]').replace(
        "[routes.direct.ollama]", "[routes.direct.mtplx]", 1
    )
    suite = load_suite(_write_suite(tmp_path, body), registry)
    run_suite(
        suite,
        registry,
        results_dir=tmp_path / "results",
        live_models_path=tmp_path / "missing.json",
        skip_judge=True,
    )
    assert calls == [("mtplx", "Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality")]
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd modelman && uv run pytest tests/benchmark/agent/test_runner.py::test_run_suite_isolates_mtplx_with_model_name -v`
Expected: FAIL — `calls == [("mtplx",)]` (no model arg), not `[("mtplx", "Youssofal/...")]`.

- [ ] **Step 3: Add the mtplx branch to `extra_args` resolution**

In `modelman/src/modelman/benchmark/agent/runner.py`, replace (lines 503-516):

```python
        for row in group_rows:
            try:
                # Resolve mlx_lm_server pairing args inline; other providers need none.
                extra_args: tuple[str, ...] = (
                    isolation.mlx_lm_server_pairing_args(
                        row.model_id,
                        row.target_local_path,
                        row.target_repo,
                        row.draft_local_path,
                        row.draft_repo,
                    )
                    if row.provider_id == "mlx_lm_server"
                    else ()
                )
            except BenchmarkError as exc:
```

with:

```python
        for row in group_rows:
            try:
                # Resolve provider-specific positional args inline, mirroring
                # modelman.benchmark.runner's Target-based resolution.
                extra_args: tuple[str, ...]
                if row.provider_id == "mlx_lm_server":
                    extra_args = isolation.mlx_lm_server_pairing_args(
                        row.model_id,
                        row.target_local_path,
                        row.target_repo,
                        row.draft_local_path,
                        row.draft_repo,
                    )
                elif row.provider_id == "mtplx":
                    # MTPLX is single-model-per-process; pass the repo id so
                    # bin/llm-isolate-provider starts the requested model.
                    extra_args = (registry.model(row.model_id).model_name,)
                else:
                    extra_args = ()
            except BenchmarkError as exc:
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd modelman && uv run pytest tests/benchmark/agent/test_runner.py::test_run_suite_isolates_mtplx_with_model_name -v`
Expected: PASS

- [ ] **Step 5: Run the full agent runner test file to check for regressions**

Run: `cd modelman && uv run pytest tests/benchmark/agent/test_runner.py -q`
Expected: PASS (all tests, including the two mlx_lm_server ones)

- [ ] **Step 6: Lint/typecheck**

Run: `cd modelman && make check`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add modelman/src/modelman/benchmark/agent/runner.py modelman/tests/benchmark/agent/test_runner.py
git commit -m "$(cat <<'EOF'
fix(agent-benchmark): forward mtplx model name during isolation

extra_args resolution had no mtplx branch even though mtplx became
reachable here the moment SUPPORTED_PROVIDER_IDS grew to include it — an
mtplx suite row isolated with no model arg, silently serving whichever
mtplx model is first in the registry. Mirrors the mtplx branch already
present in modelman.benchmark.runner.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01FQhNTgSfPchXG52G9PLAs1
EOF
)"
```

---

### Task 3: Stop the Add-Model form from mis-splitting multi-slash mtplx repo ids

**Finding:** `modelman/src/modelman/screens/forms.py:107` — `parse_model()`'s HF-repo path splits any 3rd+ `/`-separated segment off as a llamacpp-style filename. This now also applies to mtplx (in `HF_REPO_PROVIDERS`), corrupting the stored `repo`/`files` metadata for a multi-slash mtplx repo id (e.g. `org/sub/model` → `repo="org/sub"`, `files=["model"]` instead of the whole id).

**Files:**
- Modify: `modelman/src/modelman/screens/forms.py:104-114` (`parse_model`)
- Test: `modelman/tests/screens/test_forms.py`

**Interfaces:**
- Consumes: nothing new.
- Produces: no interface change — `parse_model()`'s return shape `(variant_name, repo_id, filename)` is unchanged; only the mtplx branch's values change.

- [ ] **Step 1: Find the existing parse_model test block for context**

Run: `grep -n "def test_parse_model" modelman/tests/screens/test_forms.py`

- [ ] **Step 2: Write the failing test**

Add to `modelman/tests/screens/test_forms.py` (same module `parse_model` is imported from — check the existing import line and match its style):

```python
def test_parse_model_mtplx_multi_slash_repo_not_split():
    """MTPLX caches a whole repo dir under <org>--<model> (mtplx.py's
    _dir_name/_repo_id); unlike llamacpp/omlx it has no concept of a single
    file within a repo, so a multi-slash repo id must be kept whole, not
    split into a 2-segment repo plus a filename tail."""
    name, repo, filename = parse_model("mtplx", "org/sub/model")
    assert name == "org/sub/model"
    assert repo == "org/sub/model"
    assert filename == ""


def test_parse_model_mtplx_two_segment_repo_unchanged():
    name, repo, filename = parse_model("mtplx", "Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality")
    assert name == "Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality"
    assert repo == "Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality"
    assert filename == ""
```

- [ ] **Step 3: Run tests to verify the multi-slash one fails**

Run: `cd modelman && uv run pytest tests/screens/test_forms.py::test_parse_model_mtplx_multi_slash_repo_not_split -v`
Expected: FAIL — current code returns `repo == "org/sub"`, `filename == "model"`.

- [ ] **Step 4: Fix `parse_model()`**

In `modelman/src/modelman/screens/forms.py`, replace:

```python
    # HF providers (llamacpp, omlx, and mlx_lm_server target side)
    if not model:
        raise ValueError(f"{provider} model is required")
    parts = model.split("/")
    if len(parts) < 2:
        raise ValueError(f"{provider} model must be 'org/repo' (or 'org/repo/file')")
    if not parts[0]:
        raise ValueError("repo org must not be empty")
    repo_id = "/".join(parts[:2])
    filename = "/".join(parts[2:])  # empty string if len == 2
    return (model, repo_id, filename)
```

with:

```python
    # HF providers (llamacpp, omlx, and mlx_lm_server target side)
    if not model:
        raise ValueError(f"{provider} model is required")
    parts = model.split("/")
    if len(parts) < 2:
        raise ValueError(f"{provider} model must be 'org/repo' (or 'org/repo/file')")
    if not parts[0]:
        raise ValueError("repo org must not be empty")
    if provider == "mtplx":
        # MTPLX caches a whole repo dir (mtplx.py's _dir_name/_repo_id), never
        # a single file within a repo — a multi-slash repo id (org/sub/model)
        # must round-trip whole, not get split into a 2-segment repo plus a
        # filename tail the way llamacpp/omlx repo+file inputs do.
        return (model, model, "")
    repo_id = "/".join(parts[:2])
    filename = "/".join(parts[2:])  # empty string if len == 2
    return (model, repo_id, filename)
```

- [ ] **Step 5: Update the function's docstring**

In the same file, in `parse_model()`'s docstring, replace:

```
    llamacpp / omlx: model is parsed on '/'.
      - 1 segment: invalid; HF repos are always 'org/name'.
      - 2 segments: whole repo. repo_id = full input, filename = "".
      - 3+ segments: one specific file. repo_id = first two joined
        by '/', filename = remaining segments joined by '/'.
```

with:

```
    llamacpp / omlx: model is parsed on '/'.
      - 1 segment: invalid; HF repos are always 'org/name'.
      - 2 segments: whole repo. repo_id = full input, filename = "".
      - 3+ segments: one specific file. repo_id = first two joined
        by '/', filename = remaining segments joined by '/'.

    mtplx: model is validated the same way (>= 2 '/'-separated segments,
      non-empty org) but is never split into repo+filename — MTPLX caches
      a whole repo dir, so repo_id = full input, filename = "" regardless
      of how many slashes the repo id contains.
```

- [ ] **Step 6: Run both new tests**

Run: `cd modelman && uv run pytest tests/screens/test_forms.py::test_parse_model_mtplx_multi_slash_repo_not_split tests/screens/test_forms.py::test_parse_model_mtplx_two_segment_repo_unchanged -v`
Expected: PASS

- [ ] **Step 7: Run the full forms test file for regressions**

Run: `cd modelman && uv run pytest tests/screens/test_forms.py -q`
Expected: PASS

- [ ] **Step 8: Lint/typecheck**

Run: `cd modelman && make check`
Expected: PASS

- [ ] **Step 9: Commit**

```bash
git add modelman/src/modelman/screens/forms.py modelman/tests/screens/test_forms.py
git commit -m "$(cat <<'EOF'
fix(forms): stop splitting multi-slash mtplx repo ids into repo+filename

parse_model()'s HF-repo branch split any 3rd+ '/' segment off as a
llamacpp-style filename. mtplx has no such concept — it caches a whole
repo dir — so a multi-slash repo id (org/sub/model) was corrupted into
repo="org/sub", files=["model"] instead of round-tripping whole.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01FQhNTgSfPchXG52G9PLAs1
EOF
)"
```

---

### Task 4: Correct the `_repo_id` docstring's false "exact inverse" claim

**Finding:** `modelman/src/modelman/providers/mtplx.py:30` — `_repo_id` does a global `dir_name.replace('--', '/')`, which is genuinely not an inverse of `_dir_name` when a repo/model segment itself contains a literal `--`. This is an inherent ambiguity in MTPLX's own `<org>--<model>` directory-naming convention (which modelman does not control — it only has to match it), not something fixable by changing the string algorithm; the actionable fix is to stop the docstring overclaiming.

**Files:**
- Modify: `modelman/src/modelman/providers/mtplx.py:30-35`

**Interfaces:** none — comment-only change.

- [ ] **Step 1: Fix the docstring**

In `modelman/src/modelman/providers/mtplx.py`, replace:

```python
def _repo_id(dir_name: str) -> str:
    """Map an MTPLX `<org>--<model>` dir name back to `org/model`.

    This is the exact inverse of `_dir_name`, so multi-slash repo ids
    round-trip correctly (`org/sub/model` <-> `org--sub--model`)."""
    return dir_name.replace("--", "/")
```

with:

```python
def _repo_id(dir_name: str) -> str:
    """Map an MTPLX `<org>--<model>` dir name back to `org/model`.

    Round-trips correctly for multi-slash repo ids (`org/sub/model` <->
    `org--sub--model`), the common case. NOT a true inverse of `_dir_name`
    when an org/model segment itself contains a literal `--`: MTPLX's own
    `<org>--<model>` directory-naming convention (which this function only
    matches, not defines) has no way to distinguish a `--` that came from a
    `/` from one that was already there, so e.g. `org/model--v2` round-trips
    to `org/model/v2`, not the original id. Harmless in practice — real HF
    repo ids essentially never contain a literal double-hyphen — but not the
    exact inverse the earlier version of this docstring claimed."""
    return dir_name.replace("--", "/")
```

- [ ] **Step 2: Confirm the existing round-trip test still documents only the supported case**

Run: `cd modelman && uv run pytest tests/test_providers/test_mtplx.py::test_dir_name_and_repo_id_round_trip_for_multi_slash_repo -v`
Expected: PASS (unchanged — this test only exercises the common, correctly-round-tripping case; no test asserts the false "exact inverse" claim, so nothing to update there).

- [ ] **Step 3: Commit**

```bash
git add modelman/src/modelman/providers/mtplx.py
git commit -m "$(cat <<'EOF'
fix(mtplx): correct _repo_id docstring's false exact-inverse claim

dir_name.replace('--', '/') is not a true inverse of _dir_name when a repo
segment contains a literal '--' — inherent to MTPLX's own <org>--<model>
directory convention, not fixable by changing the algorithm. Documents the
real (harmless in practice) limitation instead of overclaiming.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01FQhNTgSfPchXG52G9PLAs1
EOF
)"
```

---

### Task 5: Stop double-stopping mtplx on every isolate call

**Finding:** `modelman/src/modelman/providers/lifecycle.py:99` — `_start_mtplx_serve()` unconditionally calls `_stop_mtplx()` (a `mtplx stop --port 8003 --grace-seconds 10` subprocess, up to a 10s grace period) even though `isolate()`'s prior `_stop_others()` call already ran `llm-isolate-provider stop-all`, which stops mtplx too. Every mtplx isolation pays two full stop+poll cycles instead of one.

**Files:**
- Modify: `modelman/src/modelman/providers/lifecycle.py:92-104` (`_start_mtplx_serve`)
- Test: `modelman/tests/test_lifecycle.py` (update 3 existing tests that patch `_stop_mtplx` inside `_start_mtplx_serve` tests; add 1 new test)

**Interfaces:**
- `_start_mtplx_serve(model: str) -> None` — signature unchanged. Its only caller, `isolate()` (line 243), is unchanged: it still calls `_stop_others()` before `_start_mtplx_serve()`, which is what makes removing the internal stop safe.
- `_wait_for_port_closed()` is KEPT (not removed) — it's a fast poll (returns immediately if the port is already closed, which it will be after `_stop_others()`), and is a safety net for `_stop_others()`'s bash-side poll, which only retries 5 times before warning-and-continuing rather than blocking until closed.

- [ ] **Step 1: Write the failing test**

Add to `modelman/tests/test_lifecycle.py`, near `test_start_mtplx_serve_pins_model_id`:

```python
def test_start_mtplx_serve_does_not_stop_mtplx_again():
    """isolate() already stops mtplx (via _stop_others()'s stop-all) before
    calling _start_mtplx_serve(). A second _stop_mtplx() call here just pays
    an extra 10s-grace subprocess + port-poll for no behavioral benefit."""
    with (
        patch("modelman.providers.lifecycle.shutil.which", return_value="/usr/local/bin/mtplx"),
        patch("modelman.providers.lifecycle._stop_mtplx") as mock_stop,
        patch("modelman.providers.lifecycle._wait_for_port_closed"),
        patch("modelman.providers.lifecycle.subprocess.Popen") as mock_popen,
        patch("builtins.open", mock_open()),
        patch("modelman.providers.lifecycle.time.sleep"),
    ):
        mock_popen.return_value = MagicMock(pid=1234, poll=MagicMock(return_value=None))
        _start_mtplx_serve("org/repo")
    mock_stop.assert_not_called()
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd modelman && uv run pytest tests/test_lifecycle.py::test_start_mtplx_serve_does_not_stop_mtplx_again -v`
Expected: FAIL — `mock_stop` was called once.

- [ ] **Step 3: Remove the redundant stop call**

In `modelman/src/modelman/providers/lifecycle.py`, replace:

```python
def _start_mtplx_serve(model: str) -> None:
    """Start `mtplx serve` in the background, tracked by a pidfile."""
    bin_path = shutil.which("mtplx")
    if bin_path is None:
        raise LifecycleError("mtplx binary not found on PATH")
    # Stop any prior instance first so a re-start is idempotent (mirrors
    # mlx-lm-server.sh's unconditional-stop-before-spawn).
    _stop_mtplx()
    # Wait for the OS to reclaim port 8003 before spawning the new process.
    # Spawning into a still-held port can leave the old process answering
    # warmup, or the new process may die immediately and leave a stale pidfile.
    _wait_for_port_closed(f"{MTPLX_BASE}/v1/models")
```

with:

```python
def _start_mtplx_serve(model: str) -> None:
    """Start `mtplx serve` in the background, tracked by a pidfile.

    Assumes the caller (isolate()) has already stopped any prior mtplx
    instance via _stop_others() — stopping it again here would pay a second
    full `mtplx stop --grace-seconds 10` for no benefit, since _stop_others()
    already tore it down as part of the same isolate call."""
    bin_path = shutil.which("mtplx")
    if bin_path is None:
        raise LifecycleError("mtplx binary not found on PATH")
    # Wait for the OS to reclaim port 8003 before spawning the new process.
    # Spawning into a still-held port can leave the old process answering
    # warmup, or the new process may die immediately and leave a stale
    # pidfile. This is a fast poll (returns immediately once the port is
    # closed, which _stop_others() should have already achieved) kept as a
    # safety net: the bash stop-all's own port-closed poll only retries 5
    # times before warning-and-continuing, rather than blocking until closed.
    _wait_for_port_closed(f"{MTPLX_BASE}/v1/models")
```

- [ ] **Step 4: Remove the now-unnecessary `_stop_mtplx` patches from the other two `_start_mtplx_serve` tests**

In `modelman/tests/test_lifecycle.py`, in `test_start_mtplx_serve_raises_when_port_still_held`, remove the line `patch("modelman.providers.lifecycle._stop_mtplx"),`. Do the same in `test_start_mtplx_serve_raises_when_process_exits_immediately`. Also remove it from `test_start_mtplx_serve_pins_model_id`.

- [ ] **Step 5: Run all four tests**

Run: `cd modelman && uv run pytest tests/test_lifecycle.py -k "start_mtplx_serve" -v`
Expected: PASS (all 4)

- [ ] **Step 6: Run the full lifecycle test file for regressions**

Run: `cd modelman && uv run pytest tests/test_lifecycle.py -q`
Expected: PASS

- [ ] **Step 7: Lint/typecheck**

Run: `cd modelman && make check`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add modelman/src/modelman/providers/lifecycle.py modelman/tests/test_lifecycle.py
git commit -m "$(cat <<'EOF'
fix(lifecycle): stop double-stopping mtplx on every isolate call

_start_mtplx_serve() unconditionally re-ran `mtplx stop --grace-seconds 10`
even though isolate()'s prior _stop_others() call already stopped mtplx as
part of the same teardown, adding a redundant subprocess + poll cycle to
every isolation. The port-closed poll stays as a safety net for
_stop_others()'s bounded bash-side poll.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01FQhNTgSfPchXG52G9PLAs1
EOF
)"
```

---

### Task 6: Make `stop()` honor the module's JSON-envelope contract for every provider

**Finding:** `modelman/src/modelman/providers/lifecycle.py:277` — `stop()` raises a bare `NotImplementedError` for any non-mtplx provider, breaking the module's own "always print a JSON envelope" contract that `_main()` otherwise guarantees for every other path.

**Files:**
- Modify: `modelman/src/modelman/providers/lifecycle.py:277-281` (`stop`)
- Test: `modelman/tests/test_lifecycle.py`

**Interfaces:**
- `stop(provider_id: str) -> LifecycleResult` — return type unchanged (was already declared `-> LifecycleResult`, so the raise was already a type-lie); no signature change.

- [ ] **Step 1: Write the failing test**

Add to `modelman/tests/test_lifecycle.py`, near `test_stop_mtplx_runs_mtplx_stop`:

```python
def test_stop_non_mtplx_returns_error_envelope_not_raise():
    """stop() must return a {"ok": false, "error": ...} LifecycleResult for
    any provider it doesn't (yet) implement, matching every other path in
    this module's JSON-envelope contract — not raise, which would crash
    _main() with an uncaught traceback instead of the envelope the CLI's
    usage string promises for `stop [provider]`."""
    result = stop("omlx")
    assert result.ok is False
    assert "omlx" in (result.error or "")
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd modelman && uv run pytest tests/test_lifecycle.py::test_stop_non_mtplx_returns_error_envelope_not_raise -v`
Expected: FAIL with `NotImplementedError` propagating out of the test instead of a normal assertion failure.

- [ ] **Step 3: Fix `stop()`**

In `modelman/src/modelman/providers/lifecycle.py`, replace:

```python
def stop(provider_id: str) -> LifecycleResult:
    """Stop one provider."""
    if provider_id == "mtplx":
        return _stop_mtplx()
    raise NotImplementedError(f"stop not implemented for {provider_id}")
```

with:

```python
def stop(provider_id: str) -> LifecycleResult:
    """Stop one provider."""
    if provider_id == "mtplx":
        return _stop_mtplx()
    return LifecycleResult(provider_id, "", "", False, f"stop not implemented for {provider_id}")
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd modelman && uv run pytest tests/test_lifecycle.py::test_stop_non_mtplx_returns_error_envelope_not_raise -v`
Expected: PASS

- [ ] **Step 5: Run the full lifecycle test file for regressions**

Run: `cd modelman && uv run pytest tests/test_lifecycle.py -q`
Expected: PASS

- [ ] **Step 6: Lint/typecheck**

Run: `cd modelman && make check`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add modelman/src/modelman/providers/lifecycle.py modelman/tests/test_lifecycle.py
git commit -m "$(cat <<'EOF'
fix(lifecycle): return an error envelope from stop(), don't raise

stop() raised a bare NotImplementedError for any non-mtplx provider,
breaking this module's own "always print a JSON envelope" contract that
_main() otherwise guarantees for every other path — a future caller passing
a non-mtplx provider would get an uncaught traceback instead of
{"ok": false, "error": ...}.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01FQhNTgSfPchXG52G9PLAs1
EOF
)"
```

---

### Task 7: Dedupe `_ENV_VAR_BY_PROVIDER` and `_http_models_ids` between `lifecycle.py` and `local_control.py`

**Finding:** `modelman/src/modelman/providers/lifecycle.py:35` — `_ENV_VAR_BY_PROVIDER` and `_http_models_ids` are duplicated verbatim between this file and `modelman/src/modelman/local_control.py`.

**Files:**
- Modify: `modelman/src/modelman/local_control.py:43-62,106-121` (remove the duplicate definitions, import from `lifecycle.py` instead)
- No change needed to `lifecycle.py` (it becomes the sole owner)

**Interfaces:**
- `local_control.py` will import `_ENV_VAR_BY_PROVIDER` and `_http_models_ids` from `modelman.providers.lifecycle` instead of defining its own copies. No behavior change — the two implementations are byte-identical today.

- [ ] **Step 1: Confirm there's no circular import risk**

Run: `grep -n "^from\|^import" modelman/src/modelman/providers/lifecycle.py`
Expected: only stdlib + `from ..registry import load_registry` — nothing that imports `local_control.py`, so `local_control.py` importing `providers.lifecycle` is safe.

- [ ] **Step 2: Remove the duplicate definitions from `local_control.py`**

In `modelman/src/modelman/local_control.py`, remove:

```python
# Maps a registry provider_id to the LLM_ISOLATE_*_MODEL env var
# bin/llm-isolate-provider reads for that provider's model name (see that
# script's header comment). mlx_lm_server is absent here — it takes
# target/draft as positional args (mlx_lm_server_pairing_args), not an env
# var.
_ENV_VAR_BY_PROVIDER = {
    "ollama": "LLM_ISOLATE_OLLAMA_MODEL",
    "omlx": "LLM_ISOLATE_OMLX_4BIT_MODEL",
    "omlx-6bit": "LLM_ISOLATE_OMLX_6BIT_MODEL",
}
```

and remove:

```python
def _http_models_ids(url: str, timeout: float = 2.0) -> list[str]:
    """Model ids from an OpenAI-compatible /v1/models response, or [] on any
    error (connection refused, timeout, non-JSON body)."""
    try:
        with urllib.request.urlopen(url, timeout=timeout) as resp:  # noqa: S310 — localhost probe
            data = json.loads(resp.read().decode())
    except (OSError, ValueError):
        return []
    items = data.get("data") if isinstance(data, dict) else None
    if not isinstance(items, list):
        return []
    return [
        item["id"]
        for item in items
        if isinstance(item, dict) and isinstance(item.get("id"), str)
    ]
```

- [ ] **Step 3: Import both names from `lifecycle.py`, and drop now-unused imports**

In `modelman/src/modelman/local_control.py`, replace the import block:

```python
from __future__ import annotations

import json
import subprocess
import urllib.request
from dataclasses import dataclass
from pathlib import Path

from .benchmark.errors import BenchmarkError
from .benchmark.isolation import (
    SUPPORTED_PROVIDER_IDS,
    isolate_provider,
    mlx_lm_server_pairing_args,
    stop_all_local_providers,
)
from .registry import Registry, base_origin, model_has_local_artifact
from .state import load_state, locked_state
```

with:

```python
from __future__ import annotations

import subprocess
from dataclasses import dataclass
from pathlib import Path

from .benchmark.errors import BenchmarkError
from .benchmark.isolation import (
    SUPPORTED_PROVIDER_IDS,
    isolate_provider,
    mlx_lm_server_pairing_args,
    stop_all_local_providers,
)
from .providers.lifecycle import _ENV_VAR_BY_PROVIDER, _http_models_ids
from .registry import Registry, base_origin, model_has_local_artifact
from .state import load_state, locked_state
```

(`json` and `urllib.request` are only used inside the removed `_http_models_ids`, so they become unused imports — dropped. `subprocess` is still used by `_ollama_loaded_names`, so it stays.)

- [ ] **Step 4: Run the full local_control test file**

Run: `find modelman/tests -iname "*local_control*"` to locate the test file, then `cd modelman && uv run pytest tests/<that file> -q`
Expected: PASS — no test should reference `local_control._http_models_ids` or `local_control._ENV_VAR_BY_PROVIDER` as module-private attributes in a way that breaks (they're still accessible under the same names via the import, just re-exported from a different module).

- [ ] **Step 5: Lint/typecheck (this step catches unused-import mistakes)**

Run: `cd modelman && make check`
Expected: PASS — ruff will flag it immediately if `json`/`urllib.request` are still imported-but-unused, or if either name isn't actually used.

- [ ] **Step 6: Run the full modelman suite once (this task touches a widely-imported module)**

Run: `cd modelman && make test`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add modelman/src/modelman/local_control.py
git commit -m "$(cat <<'EOF'
fix(local_control): dedupe env-var map and models-parsing from lifecycle.py

_ENV_VAR_BY_PROVIDER and _http_models_ids were duplicated verbatim between
local_control.py and providers/lifecycle.py — a future provider addition or
a fix to /v1/models parsing applied to one copy would silently miss the
other. lifecycle.py is now the sole owner; local_control.py imports both.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01FQhNTgSfPchXG52G9PLAs1
EOF
)"
```

---

### Task 8: Dedupe `LifecycleResult` / `IsolateResult`

**Finding:** `modelman/src/modelman/providers/lifecycle.py:43` — `LifecycleResult` is a field-for-field duplicate of `IsolateResult` (`modelman/src/modelman/benchmark/isolation.py`): same `provider`/`model`/`direct_url`/`ok`/`error` shape, maintained as two independent dataclasses.

**Files:**
- Modify: `modelman/src/modelman/providers/lifecycle.py:23-49` (remove the duplicate dataclass, alias the name to `IsolateResult`)

**Interfaces:**
- `LifecycleResult` becomes `IsolateResult` under an import alias (`from ..benchmark.isolation import IsolateResult as LifecycleResult`). Every existing call site (`LifecycleResult("mtplx", "", ..., True, None)`, `asdict(result)`, `result.ok`, `result.error`, etc.) is unaffected — same field names, same order, same positional-construction shape. Tests importing `from modelman.providers.lifecycle import LifecycleResult` keep working unchanged since the name is still exported from that module.

- [ ] **Step 1: Confirm the two dataclasses really are field-for-field identical**

Run: `grep -n "class IsolateResult" -A 6 modelman/src/modelman/benchmark/isolation.py` and compare against the current `LifecycleResult` (lines 42-48 of `lifecycle.py`, already read above) — both are `provider: str`, `model: str`, `direct_url: str`, `ok: bool`, `error: str | None`, in that order.

- [ ] **Step 2: Replace the dataclass definition with an import alias**

In `modelman/src/modelman/providers/lifecycle.py`, replace:

```python
import json
import os
import shutil
import subprocess
import sys
import time
import urllib.request
from dataclasses import asdict, dataclass

from ..registry import load_registry

MTPLX_PORT = 8003
MTPLX_BASE = "http://localhost:8003"
MTPLX_DIRECT_URL = "http://localhost:8003/v1/chat/completions"
MTPLX_PIDFILE = "/tmp/local-ai-setup-mtplx.pid"
MTPLX_LOG = "/tmp/local-ai-setup-mtplx.log"

# Provider ids that use an LLM_ISOLATE_*_MODEL env var in bin/llm-isolate-provider.
# mlx_lm_server is deliberately absent: it takes target+draft as positional args.
_ENV_VAR_BY_PROVIDER = {
    "ollama": "LLM_ISOLATE_OLLAMA_MODEL",
    "omlx": "LLM_ISOLATE_OMLX_4BIT_MODEL",
    "omlx-6bit": "LLM_ISOLATE_OMLX_6BIT_MODEL",
}


@dataclass
class LifecycleResult:
    provider: str
    model: str
    direct_url: str
    ok: bool
    error: str | None
```

with:

```python
import json
import os
import shutil
import subprocess
import sys
import time
import urllib.request
from dataclasses import asdict

from ..benchmark.isolation import IsolateResult as LifecycleResult
from ..registry import load_registry

MTPLX_PORT = 8003
MTPLX_BASE = "http://localhost:8003"
MTPLX_DIRECT_URL = "http://localhost:8003/v1/chat/completions"
MTPLX_PIDFILE = "/tmp/local-ai-setup-mtplx.pid"
MTPLX_LOG = "/tmp/local-ai-setup-mtplx.log"

# Provider ids that use an LLM_ISOLATE_*_MODEL env var in bin/llm-isolate-provider.
# mlx_lm_server is deliberately absent: it takes target+draft as positional args.
_ENV_VAR_BY_PROVIDER = {
    "ollama": "LLM_ISOLATE_OLLAMA_MODEL",
    "omlx": "LLM_ISOLATE_OMLX_4BIT_MODEL",
    "omlx-6bit": "LLM_ISOLATE_OMLX_6BIT_MODEL",
}
```

(Note: this reorders relative to Task 7 — Task 7 already changed `local_control.py` to import `_ENV_VAR_BY_PROVIDER` FROM `lifecycle.py`, so `lifecycle.py` must keep its own definition here; only the dataclass is removed, aliased from `..benchmark.isolation` instead.)

- [ ] **Step 3: Confirm no circular import**

Run: `grep -n "^from\|^import" modelman/src/modelman/benchmark/isolation.py`
Expected: only `modelman.benchmark.errors` — nothing importing `providers.lifecycle`, so `lifecycle.py` importing `..benchmark.isolation` is safe.

- [ ] **Step 4: Run the full lifecycle test file**

Run: `cd modelman && uv run pytest tests/test_lifecycle.py -q`
Expected: PASS — every existing `LifecycleResult(...)` construction and `asdict(result)` call in the test file keeps working unchanged.

- [ ] **Step 5: Lint/typecheck**

Run: `cd modelman && make check`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add modelman/src/modelman/providers/lifecycle.py
git commit -m "$(cat <<'EOF'
fix(lifecycle): alias LifecycleResult to IsolateResult instead of duplicating

LifecycleResult was a field-for-field duplicate of
modelman.benchmark.isolation.IsolateResult (provider/model/direct_url/ok/
error, same order). A future field added to one wouldn't automatically
appear on the other. lifecycle.py now imports IsolateResult under the
LifecycleResult name — no call-site changes needed, since construction and
attribute access are identical.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01FQhNTgSfPchXG52G9PLAs1
EOF
)"
```

---

### Task 9: Share the mtplx stop logic between `llm-isolate-provider` and `llm-restore-providers`

**Finding:** `bin/llm-restore-providers:50` — `stop_mtplx()` duplicates the mtplx-stop block already inlined in `bin/llm-isolate-provider`'s `stop_all_local()`, instead of following this codebase's own established pattern of sharing one stop function (`mlx_lm_server_stop` lives once in `bin/lib/mlx-lm-server.sh` and is called from both scripts).

**Files:**
- Create: `bin/lib/mtplx.sh`
- Modify: `bin/llm-isolate-provider:14-18,106-113` (source the new lib, replace the inline block with a call)
- Modify: `bin/llm-restore-providers:9-11,50-54,73-74` (source the new lib, remove the local function, call the shared one)

**Interfaces:**
- Produces: `mtplx_stop()` — a shell function with no args, side effect only (stops mtplx, warns on a stuck port). Both scripts already source `bin/lib/poll.sh` for `silence_stdout`/`wait_for_port_closed`, which `mtplx.sh` also needs — sourced from within `mtplx.sh` itself (mirroring `mlx-lm-server.sh`'s own `source .../poll.sh` line), so it's self-contained regardless of what the caller has already sourced.

- [ ] **Step 1: Create `bin/lib/mtplx.sh`**

```bash
#!/bin/bash
# mtplx.sh — shared MTPLX stop helper for llm-isolate-provider and
# llm-restore-providers (mirrors mlx-lm-server.sh's mlx_lm_server_stop: one
# stop implementation shared by both scripts instead of two copies that can
# silently drift — a change to the stop invocation, e.g. grace-seconds or
# port, previously had to be made in both places).
#
# Usage:
#   source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/mtplx.sh"
#   mtplx_stop

# shellcheck source=poll.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/poll.sh"

MTPLX_STOP_PORT=8003

# Stop MTPLX via `mtplx stop --grace-seconds 10`, tolerating "nothing
# running" (mtplx stop exits 0 either way), then poll (bounded, 5 tries) for
# the port to actually close — a warning, not a failure, if it doesn't:
# neither caller can afford to abort its whole run over a stuck mtplx stop.
mtplx_stop() {
    silence_stdout mtplx stop --port "$MTPLX_STOP_PORT" --grace-seconds 10 || true
    wait_for_port_closed "http://localhost:$MTPLX_STOP_PORT/v1/models" 5 \
        || echo "warning: mtplx still listening on port $MTPLX_STOP_PORT" >&2
}
```

- [ ] **Step 2: Wire it into `bin/llm-isolate-provider`**

In `bin/llm-isolate-provider`, replace the source block:

```bash
# shellcheck source=lib/poll.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/poll.sh"
# lib/mlx-lm-resolve.sh is not sourced here: this script no longer resolves
# mlx_lm.* binaries itself — mlx-lm-server.sh (sourced below) does that, and
# sources the resolver for its own scope.
# shellcheck source=lib/mlx-lm-server.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/mlx-lm-server.sh"
```

with:

```bash
# shellcheck source=lib/poll.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/poll.sh"
# lib/mlx-lm-resolve.sh is not sourced here: this script no longer resolves
# mlx_lm.* binaries itself — mlx-lm-server.sh (sourced below) does that, and
# sources the resolver for its own scope.
# shellcheck source=lib/mlx-lm-server.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/mlx-lm-server.sh"
# shellcheck source=lib/mtplx.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/mtplx.sh"
```

Then replace the inline mtplx-stop block inside `stop_all_local()`:

```bash
    if [ "$keep" != "mtplx" ]; then
        (
            silence_stdout mtplx stop --port 8003 --grace-seconds 10 || true
            wait_for_port_closed http://localhost:8003/v1/models 5 \
                || echo "warning: mtplx still listening on port 8003" >&2
        ) &
        pids+=("$!")
    fi
```

with:

```bash
    if [ "$keep" != "mtplx" ]; then
        mtplx_stop &
        pids+=("$!")
    fi
```

- [ ] **Step 3: Wire it into `bin/llm-restore-providers`**

In `bin/llm-restore-providers`, replace the source block:

```bash
# shellcheck source=lib/poll.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/poll.sh"
# shellcheck source=lib/mlx-lm-server.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/mlx-lm-server.sh"
```

with:

```bash
# shellcheck source=lib/poll.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/poll.sh"
# shellcheck source=lib/mlx-lm-server.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/mlx-lm-server.sh"
# shellcheck source=lib/mtplx.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/mtplx.sh"
```

Then remove the local function:

```bash
stop_mtplx() {
    silence_stdout mtplx stop --port 8003 --grace-seconds 10 || true
    wait_for_port_closed http://localhost:8003/v1/models 5 \
        || echo "warning: mtplx still listening on port 8003" >&2
}
```

Then update the call site (in the parallel-restart block):

```bash
mlx_lm_server_stop & p4=$!
stop_mtplx & p5=$!
```

with:

```bash
mlx_lm_server_stop & p4=$!
mtplx_stop & p5=$!
```

- [ ] **Step 4: Verify with shellcheck**

Run: `make lint-shell`
Expected: PASS, no new warnings, for `bin/lib/mtplx.sh`, `bin/llm-isolate-provider`, and `bin/llm-restore-providers`.

- [ ] **Step 5: `bash -n` syntax-check all three files explicitly**

Run: `bash -n bin/lib/mtplx.sh && bash -n bin/llm-isolate-provider && bash -n bin/llm-restore-providers`
Expected: no output, exit 0.

- [ ] **Step 6: Commit**

```bash
git add bin/lib/mtplx.sh bin/llm-isolate-provider bin/llm-restore-providers
git commit -m "$(cat <<'EOF'
fix(bin): share mtplx stop logic instead of duplicating it in two scripts

stop_mtplx() in llm-restore-providers duplicated the mtplx-stop block
already inlined in llm-isolate-provider's stop_all_local(). Extracted to
bin/lib/mtplx.sh's mtplx_stop(), mirroring how mlx-lm-server.sh already
shares mlx_lm_server_stop() between the same two scripts — a future change
to the stop invocation now only needs to happen in one place.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01FQhNTgSfPchXG52G9PLAs1
EOF
)"
```

---

### Task 10: Replace the hardcoded mtplx/local-path provider carve-outs with a Provider capability flag

**Finding:** `modelman/src/modelman/screens/models.py:640` — `_provider_can_download()` hardcodes `if provider_id == "mtplx": return False` ahead of the general `ProviderRegistry`-based check, and `forms.py`'s `LOCAL_PATH_PROVIDERS` is a second hand-maintained provider-id tuple for a related distinction — both are special cases layered on top of the `Provider` base class rather than a capability the provider itself declares.

**Files:**
- Modify: `modelman/src/modelman/providers/base.py` (add `manages_own_cache: bool = False` class attribute to `Provider`)
- Modify: `modelman/src/modelman/providers/mtplx.py` (set `manages_own_cache = True` on `MTPLXProvider`)
- Modify: `modelman/src/modelman/providers/registry.py` (add `ProviderRegistry.get_class()`)
- Modify: `modelman/src/modelman/screens/models.py:630-648` (`_provider_can_download` reads the flag instead of hardcoding `"mtplx"`)
- Test: `modelman/tests/test_providers/test_mtplx.py`, `modelman/tests/screens/test_models.py` (or wherever `_provider_can_download` is already tested — locate first)

**Interfaces:**
- `Provider.manages_own_cache: bool = False` — a class attribute (not instance state), `True` on `MTPLXProvider`. Any future flag-only local provider (the review's stated concern: "the next flag-only local provider needs another `if provider_id == "X"` edit") sets this once on its own class instead of touching `models.py`.
- `ProviderRegistry.get_class(name: str) -> type[Provider] | None` — new classmethod, mirrors the existing `get()`/`available()` shape; returns the registered class without instantiating it (unlike `get()`, which requires a `config` dict and constructs an instance).

**Note on scope:** `forms.py`'s `LOCAL_PATH_PROVIDERS` (a distinct capability — "does this provider's form show a local-path override input" — not the same question as "can DownloadManager download this") is left as-is in this task. Folding it into the same flag would conflate two different capabilities (llamacpp/omlx support local-path override AND can-download; mtplx supports neither) into one boolean, which is worse than two clearly-named tuples/flags. If a second flag-only provider is added later and this tuple needs another entry, that's the tuple doing its job, not a bug — the review finding's core complaint (`models.py`'s *hardcoded provider-id check*, sitting awkwardly next to a *registry-based* check) is what this task fixes.

- [ ] **Step 1: Locate existing tests for `_provider_can_download`**

Run: `grep -rn "_provider_can_download" modelman/tests/`
Read whatever test file(s) that finds to match existing fixture/mocking style before writing the new test.

- [ ] **Step 2: Add the capability flag to the `Provider` base class**

In `modelman/src/modelman/providers/base.py`, in the `Provider` class, replace:

```python
class Provider(ABC):
    """Base class for all model providers."""

    name: str = ""

    def __init__(self, config: dict):
        self.config = config
```

with:

```python
class Provider(ABC):
    """Base class for all model providers."""

    name: str = ""

    # True for a provider that manages its own on-disk cache outside
    # modelman's control (its own CLI populates the cache; modelman only
    # discovers what's already there — see download()'s NotImplementedError
    # on such providers). A ready-on for one of these is a flag flip, never
    # a real DownloadManager download; ModelScreen._provider_can_download()
    # reads this instead of hardcoding provider ids.
    manages_own_cache: bool = False

    def __init__(self, config: dict):
        self.config = config
```

- [ ] **Step 3: Set the flag on `MTPLXProvider`**

In `modelman/src/modelman/providers/mtplx.py`, replace:

```python
class MTPLXProvider(Provider):
    name = "mtplx"
```

with:

```python
class MTPLXProvider(Provider):
    name = "mtplx"
    manages_own_cache = True
```

- [ ] **Step 4: Add `ProviderRegistry.get_class()`**

In `modelman/src/modelman/providers/registry.py`, replace:

```python
    @classmethod
    def available(cls) -> list[str]:
        return sorted(cls._providers)
```

with:

```python
    @classmethod
    def available(cls) -> list[str]:
        return sorted(cls._providers)

    @classmethod
    def get_class(cls, name: str) -> type[Provider] | None:
        """The registered Provider class for `name`, or None if unregistered.

        Unlike get(), this doesn't require a config dict or construct an
        instance — for callers that only need to read a class-level
        capability flag (e.g. Provider.manages_own_cache)."""
        return cls._providers.get(name)
```

- [ ] **Step 5: Write the failing test for the new capability-based check**

Add to `modelman/tests/test_providers/test_mtplx.py`:

```python
def test_mtplx_declares_manages_own_cache():
    """MTPLXProvider must declare manages_own_cache = True — this is what
    ModelScreen._provider_can_download() reads instead of hardcoding the
    provider id, so a missing flag here would silently make MTPLX
    downloadable through DownloadManager (which raises NotImplementedError,
    per test_download_raises above)."""
    assert MTPLXProvider.manages_own_cache is True
```

Add near the top of that file: `from modelman.providers.mtplx import MTPLXProvider` (may already be imported — check first).

Run: `cd modelman && uv run pytest tests/test_providers/test_mtplx.py::test_mtplx_declares_manages_own_cache -v`
Expected: FAIL — `Provider` (and therefore `MTPLXProvider`) has no `manages_own_cache` attribute yet if Steps 2-3 haven't landed; run this AFTER Steps 2-3 to confirm PASS instead (this is the one step in this task where doing it test-first is impractical, since the attribute must exist before it can be asserted True vs. inherited-False — verify by temporarily reverting Step 3 if you want to see it fail on `False` instead of `AttributeError`).

- [ ] **Step 6: Fix `_provider_can_download()`**

In `modelman/src/modelman/screens/models.py`, replace:

```python
    def _provider_can_download(self, provider_id: str) -> bool:
        """True when a ready-on against this provider is a real
        download/pull and must go through DownloadManager: the provider
        has a registered Provider class (ollama/omlx/llamacpp). This is
        deliberately NOT model_has_local_artifact — an ollama *cloud*
        model has no local artifact but its ready-on still runs a real
        `ollama pull` (that's what registers the tag), so it must route
        through DownloadManager too; native/unmapped providers have no
        Provider class and keep the queued flag flip. MTPLX has a Provider
        class but manages its own cache via the `mtplx` CLI, so it is also
        treated as flag-only. Mirrors _run_apply's try/except-KeyError
        flag-only rule."""
        if provider_id == "mtplx":
            return False
        if self._provider_entry_or_none(provider_id) is None:
            return False
        from ..providers.registry import ProviderRegistry

        return provider_id in ProviderRegistry.available()
```

with:

```python
    def _provider_can_download(self, provider_id: str) -> bool:
        """True when a ready-on against this provider is a real
        download/pull and must go through DownloadManager: the provider
        has a registered Provider class (ollama/omlx/llamacpp) that does NOT
        declare manages_own_cache. This is deliberately NOT
        model_has_local_artifact — an ollama *cloud* model has no local
        artifact but its ready-on still runs a real `ollama pull` (that's
        what registers the tag), so it must route through DownloadManager
        too; native/unmapped providers have no Provider class and keep the
        queued flag flip. A provider that manages its own cache outside
        modelman's control (MTPLX via the `mtplx` CLI) declares
        manages_own_cache = True and is also treated as flag-only. Mirrors
        _run_apply's try/except-KeyError flag-only rule."""
        if self._provider_entry_or_none(provider_id) is None:
            return False
        from ..providers.registry import ProviderRegistry

        provider_cls = ProviderRegistry.get_class(provider_id)
        if provider_cls is None:
            return False
        return not provider_cls.manages_own_cache
```

- [ ] **Step 7: Run the capability test**

Run: `cd modelman && uv run pytest tests/test_providers/test_mtplx.py::test_mtplx_declares_manages_own_cache -v`
Expected: PASS

- [ ] **Step 8: Run whatever test(s) Step 1 found for `_provider_can_download`, to confirm the mtplx-returns-False behavior is unchanged**

Run: `cd modelman && uv run pytest tests/screens/test_models.py -k "can_download or mtplx" -v` (adjust path/pattern to match what Step 1 actually found)
Expected: PASS — behavior for mtplx (`False`) and for ollama/omlx/llamacpp (`True`) is identical to before; only the code path changed.

- [ ] **Step 9: Run the full test suite for both touched packages**

Run: `cd modelman && make test`
Expected: PASS

- [ ] **Step 10: Lint/typecheck**

Run: `cd modelman && make check`
Expected: PASS

- [ ] **Step 11: Commit**

```bash
git add modelman/src/modelman/providers/base.py modelman/src/modelman/providers/mtplx.py modelman/src/modelman/providers/registry.py modelman/src/modelman/screens/models.py modelman/tests/test_providers/test_mtplx.py
git commit -m "$(cat <<'EOF'
fix(providers): replace hardcoded mtplx download carve-out with a capability flag

_provider_can_download() special-cased `if provider_id == "mtplx": return
False` ahead of its general ProviderRegistry check. Added
Provider.manages_own_cache (declared True on MTPLXProvider) and
ProviderRegistry.get_class(), so the next flag-only local provider declares
the capability on its own class instead of requiring another edit here.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01FQhNTgSfPchXG52G9PLAs1
EOF
)"
```

---

### Final Task: Whole-branch verification

- [ ] **Step 1: Full modelman suite**

Run: `cd modelman && make all`
Expected: PASS (format + test + check)

- [ ] **Step 2: Shell lint across the whole repo**

Run: `make lint-shell`
Expected: PASS

- [ ] **Step 3: Link check (docs weren't touched, but cheap to confirm nothing broke)**

Run: `make check-links`
Expected: PASS

- [ ] **Step 4: Review the full diff for this plan's 10 commits**

Run: `git log --oneline -11` and `git diff 7f263ca..HEAD --stat`
Expected: exactly the files listed across Tasks 1-10, no stray changes.

- [ ] **Step 5: Report back to the user**

Summarize: all 10 findings fixed, tests added per finding, full suite green. Ask whether to run `/code-review` again on the new diff before considering this done (this branch's own history shows a review-fix pass can itself introduce or miss things — worth a second pass here too).
