# MTPLX Provider Code-Review Fixes

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development (recommended) or superpowers:executing-plans. Steps use checkbox syntax for tracking.

**Goal:** Fix the concrete functional bugs found by the `/code-review` pass on the `mtplx-provider` branch vs `main`: model-name forwarding through the isolation stack, MTPLX teardown in restore, lifecycle startup/warmup hardening, MTPLX registry/UI round-trips, and the provider→env mapping.

**Spec:** `docs/superpowers/specs/2026-09-10-mtplx-provider-design.md`

**Tech Stack:** Python (modelman), bash (bin helpers), pytest, ruff, `make lint-shell`, `make check-links`.

**Worktree:** `/Users/keith/github/ohanaverse/local-ai-setup/.worktrees/mtplx-provider` (branch `mtplx-provider`). All Python commands use `uv run` from `modelman/`; root commands use the worktree root.

---

## Global Constraints

- One local model at a time must remain invariant; `llm-restore-providers` must tear MTPLX down exactly like `mlx_lm_server`.
- The `bin/llm-isolate-provider` JSON stdout contract must not change.
- `modelman start mtplx/<model>` must start the requested model, not the first mtplx model in the registry.
- MTPLX is an HF-repo-style local provider: model input is `org/repo`, location is locked to `local`, and modelman never runs a download for it.
- No PR is created without explicit user approval.

---

### Task 1: Forward `model_name` through the MTPLX isolation stack

**Files:**
- Modify: `bin/llm-isolate-provider` (mtplx branch)
- Modify: `modelman/src/modelman/providers/lifecycle.py` (`_delegate_isolate` env mapping)
- Modify: `modelman/src/modelman/local_control.py` (mtplx branch in `start_local_model`)
- Modify: `modelman/src/modelman/benchmark/runner.py` (mtplx extra_args)

**Background:**
The lifecycle module already accepts a positional `model` argument, and the bash shim already delegates to it, but the shim currently calls `python3 -m modelman.providers.lifecycle isolate mtplx` with no model. That makes every call fall back to the first mtplx model in the registry, so `modelman start mtplx/Second-Model` and benchmark sweeps load the wrong weights while writing the right marker.

- [ ] **Step 1: Update the bash shim to forward the optional model arg**

  In `bin/llm-isolate-provider`, change the `mtplx)` branch so it passes a second positional argument when present:

  ```bash
  mtplx)
      # MTPLX lifecycle is implemented in Python; pass an explicit model arg
      # through so callers (modelman start, modelman benchmark) can select a
      # specific mtplx model instead of always defaulting to the first one in
      # the registry.
      python3 -m modelman.providers.lifecycle isolate mtplx "${2:-}"
      exit $?
      ;;
  ```

- [ ] **Step 2: Replace the magic env-var formula in `_delegate_isolate` with an explicit mapping**

  In `modelman/src/modelman/providers/lifecycle.py`, define an explicit mapping and use it only for mapped providers:

  ```python
  # Provider ids that use an LLM_ISOLATE_*_MODEL env var in bin/llm-isolate-provider.
  # mlx_lm_server is deliberately absent: it takes target+draft as positional args.
  _ENV_VAR_BY_PROVIDER = {
      "ollama": "LLM_ISOLATE_OLLAMA_MODEL",
      "omlx": "LLM_ISOLATE_OMLX_4BIT_MODEL",
      "omlx-6bit": "LLM_ISOLATE_OMLX_6BIT_MODEL",
  }
  ```

  Replace the body of `_delegate_isolate` so it builds the env var from this mapping instead of `provider_id.upper().replace('-', '_')`. If `model` is given but the provider is not in the mapping, do not set an env var (MTPLX uses the positional arg; mlx_lm_server uses extra_args).

- [ ] **Step 3: Make `start_local_model` pass the MTPLX model name as an extra arg**

  In `modelman/src/modelman/local_control.py`, in the `start_local_model` arg-resolution block, change the `elif model.provider_id == "mtplx":` branch to set `extra_args = (model.model_name,)` instead of `env = None`. Keep `env = None` (do not pass an env override for MTPLX). The call to `isolate_provider` already accepts `*extra_args`.

- [ ] **Step 4: Make `run_benchmark` pass the MTPLX model name to `isolate_provider`**

  In `modelman/src/modelman/benchmark/runner.py`, when resolving `extra_args`, add a branch for `provider_id == "mtplx"` that returns `(target.model_name,)`. Keep the existing `mlx_lm_server` branch. This makes the isolation key include the model name, so two different mtplx targets get separate isolations.

- [ ] **Step 5: Run focused tests**

  ```bash
  cd modelman && uv run pytest tests/test_lifecycle.py tests/test_benchmark_runner.py tests/test_local_control.py -q
  ```

  Add or update tests as needed to cover the new forwarding paths:
  - `lifecycle._delegate_isolate` with `model="foo"` sets the right env var for ollama and no env var for mtplx.
  - `local_control.start_local_model` for mtplx calls `isolate_provider("mtplx", model.model_name, env=None)`.
  - `runner.run_benchmark` discovers two mtplx models and isolates each with its own model_name.

- [ ] **Step 6: Commit**

  ```bash
  git add -A && git commit -m "fix(lifecycle): forward mtplx model_name through isolation stack"
  ```

---

### Task 2: Harden MTPLX lifecycle startup and teardown

**Files:**
- Modify: `modelman/src/modelman/providers/lifecycle.py` (`_start_mtplx_serve`, `_warmup`, add port-closed helper)
- Modify: `bin/llm-restore-providers`

**Background:**
`_start_mtplx_serve` writes a pidfile without checking that the new process is alive or that the old port was released. If `mtplx serve` crashes immediately or a stale process holds 8003, the user gets a 300s `_wait_for_model` timeout or may warm up against a stale model. `llm-restore-providers` also has no MTPLX branch, so after an MTPLX benchmark the GPU stays loaded.

- [ ] **Step 1: Wait for the port to be released before starting a new MTPLX server**

  Add a helper `_wait_for_port_closed(url, timeout)` in `lifecycle.py` that polls the URL and returns when the connection is refused (or any OSError). Use it in `_start_mtplx_serve` immediately after `_stop_mtplx()` and before spawning the new process, with a short deadline (e.g. 10s). On timeout, raise `LifecycleError("port 8003 still held by a previous mtplx process")`.

- [ ] **Step 2: Verify the spawned process is alive before waiting for the model**

  After `subprocess.Popen(...)` and writing the pidfile, check `proc.poll()` is `None`. If it is not `None`, raise `LifecycleError` with the exit status (and ideally the last lines of `MTPLX_LOG`).

- [ ] **Step 3: Tolerate spaced JSON in `_warmup`**

  Change the success check from:
  ```python
  if '"object":"chat.completion"' in body:
  ```
  to:
  ```python
  if '"chat.completion"' in body:
  ```
  This matches both compact (`"object":"chat.completion"`) and spaced (`"object": "chat.completion"`) serializations.

- [ ] **Step 4: Add MTPLX teardown to `llm-restore-providers`**

  In `bin/llm-restore-providers`, add a `stop_mtplx()` function that runs unconditionally (MTPLX is not part of the standing baseline) and mirrors the `mlx_lm_server_stop` pattern:

  ```bash
  stop_mtplx() {
      silence_stdout mtplx stop --port 8003 --grace-seconds 10 || true
      wait_for_port_closed http://localhost:8003/v1/models 5 \
          || echo "warning: mtplx still listening on port 8003" >&2
  }
  ```

  Add `stop_mtplx & p5=$!` in parallel with the other restore steps and `wait $p5 || FAILED=1`. Update the comment block above the parallel section to mention MTPLX alongside mlx_lm_server.

- [ ] **Step 5: Run tests and shell lint**

  ```bash
  cd modelman && uv run pytest tests/test_lifecycle.py -q
  cd .. && make lint-shell
  ```

  Add/update tests:
  - `_warmup` succeeds against a body containing `"object": "chat.completion"`.
  - `_start_mtplx_serve` raises `LifecycleError` when the port is still held (mock `_wait_for_port_closed` to time out).
  - `_start_mtplx_serve` raises when `Popen.poll()` is not `None` immediately after spawn.

- [ ] **Step 6: Commit**

  ```bash
  git add -A && git commit -m "fix(lifecycle): harden mtplx startup and add restore teardown"
  ```

---

### Task 3: Fix MTPLX registry/UI round-trip and download behavior

**Files:**
- Modify: `modelman/src/modelman/providers/mtplx.py`
- Modify: `modelman/src/modelman/screens/forms.py`
- Modify: `modelman/src/modelman/screens/models.py`

**Background:**
- `_repo_id` replaces only the first `--`, so multi-slash repo ids round-trip incorrectly.
- `mtplx` is missing from `HF_REPO_PROVIDERS`, so the Add dialog locks location to `cloud-only` and produces entries that `start`/`sync` reject.
- `_provider_can_download` returns true for mtplx because it has a registered `Provider` class, but `MTPLXProvider.download` raises `NotImplementedError`, so toggling Ready on an uncached mtplx model fails forever in the DownloadManager.

- [ ] **Step 1: Make `_repo_id` the exact inverse of `_dir_name`**

  In `modelman/src/modelman/providers/mtplx.py`:

  ```python
  def _dir_name(model_name: str) -> str:
      """Map an upstream `org/model` repo id to MTPLX's `<org>--<model>` dir."""
      return model_name.replace("/", "--")

  def _repo_id(dir_name: str) -> str:
      """Map an MTPLX `<org>--<model>` dir name back to `org/model`."""
      return dir_name.replace("--", "/")
  ```

  Add a unit test that round-trips `org/sub/model` and `org/model`.

- [ ] **Step 2: Add `mtplx` to `HF_REPO_PROVIDERS`**

  In `modelman/src/modelman/screens/forms.py`, change `HF_REPO_PROVIDERS` to include `"mtplx"`:

  ```python
  HF_REPO_PROVIDERS: tuple[str, ...] = ("llamacpp", "omlx", "mlx_lm_server", "mtplx")
  ```

  With this change, `default_form_kind("mtplx")` returns `"local-only"`, so the Add/Edit dialog uses the HF repo input, locks location to `local`, and stores `repo`/`files` correctly.

- [ ] **Step 3: Prevent DownloadManager from trying to download MTPLX models**

  In `modelman/src/modelman/screens/models.py`, modify `_provider_can_download` to return `False` for `mtplx`:

  ```python
  def _provider_can_download(self, provider_id: str) -> bool:
      if provider_id == "mtplx":
          return False
      if self._provider_entry_or_none(provider_id) is None:
          return False
      from ..providers.registry import ProviderRegistry
      return provider_id in ProviderRegistry.available()
  ```

  Update the docstring to note that MTPLX manages its own cache and is treated as flag-only.

- [ ] **Step 4: Run focused tests**

  ```bash
  cd modelman && uv run pytest tests/test_mtplx_provider.py tests/test_forms.py tests/test_models_screen.py -q
  ```

  Add/update tests:
  - `_repo_id(_dir_name("org/sub/model")) == "org/sub/model"`.
  - `default_form_kind("mtplx") == "local-only"`.
  - `parse_model("mtplx", "org/sub/model")` returns the expected repo/files.
  - `_provider_can_download("mtplx")` is `False` even though `ProviderRegistry.available()` contains it.

- [ ] **Step 5: Commit**

  ```bash
  git add -A && git commit -m "fix(mtplx): round-trip multi-slash repo ids and treat as flag-only local provider"
  ```

---

### Task 4: Full verification

**Files:** none to modify; run aggregate checks.

- [ ] **Step 1: Run modelman test suite**

  ```bash
  cd modelman && uv run make check && uv run pytest tests/ -q
  ```

- [ ] **Step 2: Run wt tests and root lint**

  ```bash
  cd wt && go test ./... && go vet ./...
  cd .. && make lint-shell && make check-links
  ```

- [ ] **Step 3: Commit only if fixes were needed**

  If any test or lint fix was required, commit it with a clear scope tag. If everything is clean, no commit is needed for this task.

---

## Completion Criteria

- `modelman start mtplx/<repo>` starts the requested repo.
- `modelman benchmark` with multiple mtplx models isolates each with its own repo.
- `llm-restore-providers` stops MTPLX after a benchmark.
- `_warmup` accepts both compact and spaced JSON.
- `_start_mtplx_serve` fails fast on port conflicts / crashed launches.
- The Add Model dialog treats MTPLX as a local-only HF-repo provider.
- Toggling Ready on an MTPLX model does not spawn a DownloadManager thread.
- All automated checks (`make test-all` equivalent) pass.
