# Modelman LiteLLM Proxy Reconcile Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** After a successful `config.yaml` write that changes the `model_list`, modelman reconciles the running LiteLLM proxy so it serves the new model list. The reconcile is best-effort and non-fatal: a failed restart warns but does not fail the expose operation.

**Architecture:** `src/modelman/litellm.py` (already the single owner of LiteLLM knowledge) gains `default_litellm_restart_cmd()` and `restart_litellm_proxy()`. The restart command is read from `MODELMAN_LITELLM_RESTART_CMD` (unset = no-op with a warning). `expose_model`/`unexpose_model` and `apply_expose_queue` call `restart_litellm_proxy()` after a successful write; the batch path calls it once, only when entries actually applied.

**Tech Stack:** Python 3.13, Typer, Textual, PyYAML, pytest.

**Spec:** `docs/superpowers/specs/2026-08-31-modelman-litellm-proxy-reconcile-design.md`

---

## File Structure

- **Modify: `src/modelman/litellm.py`** — `default_litellm_restart_cmd()`, `restart_litellm_proxy()`, and call sites in `expose_model`/`unexpose_model`/`apply_expose_queue`.
- **Modify: `tests/test_litellm.py`** — restart-cmd resolution + `restart_litellm_proxy` behavior.
- **Modify: `tests/test_expose.py`** — `expose_model`/`unexpose_model` call restart after a successful write; failing restart is non-fatal.
- **Modify: `tests/test_queue.py`** — `apply_expose_queue` restarts once when entries applied, not when empty/no-op.
- **Modify: `tests/commands/test_expose.py`** — CLI wiring: restart runs after success; failing restart still exits 0 with a warning.
- **Modify: `docs/ROADMAP.md`** — add a Phase 3 follow-up row.

Files NOT touched: `registry.py`, `state.py`, `sync.py`, `providers/*`, `queue.py` (the restart is triggered inside `apply_expose_queue`, which `queue.py` already calls).

---

## Task 1: Add `default_litellm_restart_cmd` + `restart_litellm_proxy`

**Files:**
- Modify: `src/modelman/litellm.py`
- Test: `tests/test_litellm.py`

- [ ] **Step 1: Write the failing tests**

Append to `tests/test_litellm.py`:

```python
def test_default_restart_cmd_unset(monkeypatch):
    monkeypatch.delenv("MODELMAN_LITELLM_RESTART_CMD", raising=False)
    from modelman.litellm import default_litellm_restart_cmd

    assert default_litellm_restart_cmd() is None


def test_default_restart_cmd_set(monkeypatch):
    monkeypatch.setenv("MODELMAN_LITELLM_RESTART_CMD", "echo restart")
    from modelman.litellm import default_litellm_restart_cmd

    assert default_litellm_restart_cmd() == "echo restart"


def test_restart_proxy_noop_when_unset(monkeypatch, capsys):
    monkeypatch.delenv("MODELMAN_LITELLM_RESTART_CMD", raising=False)
    from modelman.litellm import restart_litellm_proxy

    restart_litellm_proxy()
    err = capsys.readouterr().err
    assert "restart" in err.lower()  # warns that a manual restart is needed


def test_restart_proxy_runs_command(monkeypatch):
    monkeypatch.setenv("MODELMAN_LITELLM_RESTART_CMD", "echo restarted")
    import subprocess

    calls = []

    def fake_run(cmd, *, shell, check):
        calls.append((cmd, shell, check))

    monkeypatch.setattr(subprocess, "run", fake_run)

    from modelman.litellm import restart_litellm_proxy

    restart_litellm_proxy()
    assert calls == [("echo restarted", True, True)]


def test_restart_proxy_failure_is_nonfatal(monkeypatch, capsys):
    monkeypatch.setenv("MODELMAN_LITELLM_RESTART_CMD", "false")
    from modelman.litellm import restart_litellm_proxy

    # Must not raise.
    restart_litellm_proxy()
    err = capsys.readouterr().err
    assert "restart" in err.lower()
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `uv run pytest tests/test_litellm.py -k restart -v`
Expected: FAIL with `ImportError: cannot import name 'restart_litellm_proxy' from 'modelman.litellm'`

- [ ] **Step 3: Write the implementation**

Append to `src/modelman/litellm.py`:

```python
def default_litellm_restart_cmd() -> str | None:
    """The shell command used to restart the LiteLLM proxy, or None when
    unset (reconcile is a no-op with a warning). Read lazily so env
    overrides work in tests."""
    return os.environ.get("MODELMAN_LITELLM_RESTART_CMD")


def restart_litellm_proxy() -> None:
    """Best-effort reconcile of the running LiteLLM proxy after a config
    write. Runs the configured restart command; when unset, prints a
    warning that a manual restart is needed. Never raises: the config
    write is the source of truth, and a failed restart just leaves the
    proxy stale."""
    cmd = default_litellm_restart_cmd()
    if not cmd:
        print(
            "warning: LiteLLM config changed but MODELMAN_LITELLM_RESTART_CMD "
            "is unset; restart the proxy manually for the change to take effect.",
            file=sys.stderr,
        )
        return
    try:
        subprocess.run(cmd, shell=True, check=True)
    except Exception as exc:  # noqa: BLE001
        print(f"warning: failed to restart LiteLLM proxy ({exc}); restart it manually.", file=sys.stderr)
```

Add `import subprocess` and `import sys` to the module's imports.

- [ ] **Step 4: Run tests to verify they pass**

Run: `uv run pytest tests/test_litellm.py -k restart -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add src/modelman/litellm.py tests/test_litellm.py
git commit -m "feat(litellm): add configurable proxy restart helper"
```

---

## Task 2: Wire restart into `expose_model`/`unexpose_model`

**Files:**
- Modify: `src/modelman/litellm.py`
- Test: `tests/test_expose.py`

- [ ] **Step 1: Write the failing tests**

Append to `tests/test_expose.py`:

```python
def test_expose_model_restarts_proxy(tmp_path, monkeypatch):
    registry = _registry()
    state = _state()
    path = _seed_config(tmp_path)
    calls = []
    monkeypatch.setattr(
        "modelman.litellm.restart_litellm_proxy", lambda: calls.append("restart")
    )
    expose_model(registry, state, "ollama/a", path)
    assert calls == ["restart"]


def test_unexpose_model_restarts_proxy(tmp_path, monkeypatch):
    registry = _registry()
    state = _state()
    path = _seed_config(tmp_path)
    expose_model(registry, state, "ollama/a", path)
    calls = []
    monkeypatch.setattr(
        "modelman.litellm.restart_litellm_proxy", lambda: calls.append("restart")
    )
    unexpose_model(state, "ollama/a", path)
    assert calls == ["restart"]


def test_expose_model_restart_failure_nonfatal(tmp_path, monkeypatch):
    registry = _registry()
    state = _state()
    path = _seed_config(tmp_path)

    def boom(*args, **kwargs):
        raise RuntimeError("restart failed")

    monkeypatch.setattr("modelman.litellm.subprocess.run", boom)
    # Must not raise.
    expose_model(registry, state, "ollama/a", path)
    assert state.get("ollama/a").litellm_exposed is True
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `uv run pytest tests/test_expose.py -k restart -v`
Expected: FAIL — `expose_model` does not yet call `restart_litellm_proxy` (the `calls` list stays empty).

- [ ] **Step 3: Write the implementation**

In `src/modelman/litellm.py`, add a call to `restart_litellm_proxy()` at the end of `expose_model` (after `_set_exposed_flag(state, model_id, True)`) and at the end of `unexpose_model` (after `_set_exposed_flag(state, model_id, False)`).

- [ ] **Step 4: Run tests to verify they pass**

Run: `uv run pytest tests/test_expose.py -k restart -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add src/modelman/litellm.py tests/test_expose.py
git commit -m "feat(litellm): restart proxy after expose/unexpose"
```

---

## Task 3: Wire restart into `apply_expose_queue` (once per batch)

**Files:**
- Modify: `src/modelman/litellm.py`
- Test: `tests/test_queue.py`

- [ ] **Step 1: Write the failing tests**

Append to `tests/test_queue.py`:

```python
def test_apply_expose_queue_restarts_once_when_applied(tmp_path, monkeypatch):
    from modelman.litellm import apply_expose_queue, save_litellm_config
    from modelman.registry import AuthConfig, ModelEntry, ProviderEntry, Registry
    from modelman.state import ModelState, StateStore

    registry = Registry(
        providers=[
            ProviderEntry(
                id="ollama", name="Ollama",
                auth=AuthConfig(type="none", base_url="http://localhost:11434"),
            )
        ],
        models=[
            ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a")
        ],
    )
    state = StateStore()
    state.set("ollama/a", ModelState(ready=True))
    path = tmp_path / "config.yaml"
    save_litellm_config({"model_list": [], "general_settings": {}}, path)

    calls = []
    monkeypatch.setattr(
        "modelman.litellm.restart_litellm_proxy", lambda: calls.append("restart")
    )
    apply_expose_queue(registry, state, [("ollama/a", True)], path)
    assert calls == ["restart"]


def test_apply_expose_queue_no_restart_when_empty(tmp_path, monkeypatch):
    from modelman.litellm import apply_expose_queue, save_litellm_config
    from modelman.registry import AuthConfig, ModelEntry, ProviderEntry, Registry
    from modelman.state import StateStore

    registry = Registry(
        providers=[
            ProviderEntry(
                id="ollama", name="Ollama",
                auth=AuthConfig(type="none", base_url="http://localhost:11434"),
            )
        ],
        models=[
            ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a")
        ],
    )
    state = StateStore()
    path = tmp_path / "config.yaml"
    save_litellm_config({"model_list": [], "general_settings": {}}, path)

    calls = []
    monkeypatch.setattr(
        "modelman.litellm.restart_litellm_proxy", lambda: calls.append("restart")
    )
    apply_expose_queue(registry, state, [], path)
    assert calls == []
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `uv run pytest tests/test_queue.py -k restart -v`
Expected: FAIL — `apply_expose_queue` does not yet call `restart_litellm_proxy`.

- [ ] **Step 3: Write the implementation**

In `src/modelman/litellm.py`, in `apply_expose_queue`, after the `if succeeded:` block that saves the config and flips flags, add a single `restart_litellm_proxy()` call guarded by `if succeeded:`:

```python
    if succeeded:
        save_litellm_config(config, litellm_path)
        for model_id, target in succeeded:
            _set_exposed_flag(state, model_id, target)
        restart_litellm_proxy()
    return outcomes
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `uv run pytest tests/test_queue.py -k restart -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add src/modelman/litellm.py tests/test_queue.py
git commit -m "feat(litellm): restart proxy once per applied expose batch"
```

---

## Task 4: CLI wiring — restart runs after success, failure is non-fatal

**Files:**
- Modify: `src/modelman/main.py`
- Test: `tests/commands/test_expose.py`

- [ ] **Step 1: Write the failing tests**

Append to `tests/commands/test_expose.py`:

```python
def test_expose_command_restarts_proxy(tmp_path, monkeypatch):
    _seed(tmp_path, monkeypatch)
    calls = []
    monkeypatch.setattr(
        "modelman.litellm.restart_litellm_proxy", lambda: calls.append("restart")
    )
    runner = CliRunner()
    result = runner.invoke(app, ["expose", "ollama/a"])
    assert result.exit_code == 0
    assert calls == ["restart"]


def test_expose_command_restart_failure_still_exits_zero(tmp_path, monkeypatch):
    _seed(tmp_path, monkeypatch)

    def boom(*args, **kwargs):
        raise RuntimeError("restart failed")

    monkeypatch.setattr("modelman.litellm.subprocess.run", boom)
    runner = CliRunner()
    result = runner.invoke(app, ["expose", "ollama/a"])
    assert result.exit_code == 0
    assert "Exposed ollama/a" in result.stdout
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `uv run pytest tests/commands/test_expose.py -k restart -v`
Expected: FAIL — `calls` stays empty / the command path does not yet restart.

- [ ] **Step 3: Write the implementation**

No change to `main.py` is required: `expose`/`unexpose` already call
`expose_model`/`unexpose_model`, which now call `restart_litellm_proxy()`
(Task 2). If the tests fail because the monkeypatch target is wrong, confirm
the patch target is `modelman.litellm.restart_litellm_proxy` (the module where
the call site lives), not `modelman.main`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `uv run pytest tests/commands/test_expose.py -k restart -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add tests/commands/test_expose.py
git commit -m "test(cli): expose/unexpose restart proxy and stay non-fatal"
```

---

## Task 5: Update ROADMAP

**Files:**
- Modify: `docs/ROADMAP.md`

- [ ] **Step 1: Update the ROADMAP**

In `docs/ROADMAP.md`, under Phase 3, add a follow-up row:

```markdown
| — | reconcile running proxy after expose changes (restart via `MODELMAN_LITELLM_RESTART_CMD`) | planned |

Spec: `docs/superpowers/specs/2026-08-31-modelman-litellm-proxy-reconcile-design.md`
```

- [ ] **Step 2: Commit**

```bash
git add docs/ROADMAP.md
git commit -m "docs: add LiteLLM proxy reconcile follow-up to ROADMAP"
```

---

## Final verification

- [ ] Run the full suite: `uv run pytest -q`
- [ ] Run lint + typecheck: `make check`
- [ ] CLI smoke test with the command unset: `uv run modelman expose <model-id>` — expect a warning that a manual restart is needed, exit 0.
- [ ] CLI smoke test with the command set: `MODELMAN_LITELLM_RESTART_CMD="launchctl kickstart -k gui/$(id -u)/local.litellm.proxy" uv run modelman expose <model-id>` — expect the proxy to restart and `/v1/models` to include the model. Use a model id that exists in your registry.

---

## Self-review notes

- **Spec coverage:** `default_litellm_restart_cmd` + `restart_litellm_proxy` (Task 1), `expose_model`/`unexpose_model` call sites (Task 2), `apply_expose_queue` once-per-batch (Task 3), CLI wiring (Task 4), ROADMAP (Task 5). All spec sections map to a task.
- **Non-fatal contract:** `restart_litellm_proxy` never raises; every call site is after a successful write, so a failed restart cannot roll back or fail the expose operation. Tests assert this explicitly.
- **Once-per-batch:** `apply_expose_queue` guards the restart with `if succeeded:`, so the empty queue does not bounce the proxy. Idempotent re-exposes are still considered "applied" and will restart the proxy; per the spec, only failed-validation items are skipped.
- **Configurable, not hardcoded:** the restart command comes from `MODELMAN_LITELLM_RESTART_CMD`; unset is a no-op with a warning, so other environments are not broken. No `launchctl` string is hardcoded in code.
- **No placeholders:** every code step contains complete code; every command has an expected result.
