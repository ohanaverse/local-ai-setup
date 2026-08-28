# `modelman sync` (ollama reconcile-only) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Revise `modelman sync` so it reconciles the downloaded state of configured ollama models against `ollama list`, instead of discovering and adding all ollama models.

**Architecture:** `sync.py` is rewritten around two functions — `list_ollama` (downloaded name→size) and `reconcile` (update `downloaded`/`disk_path`/`size_bytes` for configured ollama models) — with `SyncResult` redefined to `downloaded`/`not_downloaded`. `registry.toml` becomes read-only input; only `modelman.toml` is written.

**Tech Stack:** Python 3.13, Typer, pytest (pytest-asyncio), uv.

**Spec:** `docs/superpowers/specs/2026-08-28-modelman-sync-ollama-reconcile-design.md`

---

## File Structure

- **Modify: `src/modelman/sync.py`** — replace `_parse_ollama_list`/`discover_ollama`/`merge`/`update_state` with `_parse_ollama_list_sizes`/`list_ollama`/`reconcile`; redefine `SyncResult`; update `sync`.
- **Modify: `src/modelman/main.py`** — drop `save_registry` from the `sync` command; new summary line.
- **Modify: `tests/test_sync.py`** — rewrite for reconcile-only.
- **Modify: `tests/commands/test_sync.py`** — new summary, no registry save.

Files NOT touched: `registry.py`, `state.py`, `providers/*.py`, `screens/models.py`.

---

## Task 1: Rewrite `sync.py` + `tests/test_sync.py` (reconcile-only)

**Files:**
- Modify: `src/modelman/sync.py`
- Test: `tests/test_sync.py`

- [ ] **Step 1: Write the failing test**

Overwrite `tests/test_sync.py`:

```python
"""Provider sync — reconcile configured ollama models against `ollama list`."""

from unittest.mock import MagicMock

import pytest

from modelman.registry import ModelEntry, Registry
from modelman.state import ModelState, StateStore
from modelman.sync import (
    SyncError,
    _parse_ollama_list_sizes,
    list_ollama,
    reconcile,
    sync,
)


def test_parse_ollama_list_sizes_local_row():
    stdout = (
        "NAME              ID              SIZE      MODIFIED\n"
        "ornith-1.5:9b     e5df7dcdd8a2    6.6 GB    4 days ago\n"
    )
    assert _parse_ollama_list_sizes(stdout) == {"ornith-1.5:9b": int(6.6 * 1024**3)}


def test_parse_ollama_list_sizes_skips_cloud_row():
    stdout = (
        "NAME              ID              SIZE      MODIFIED\n"
        "some-cloud        def456          -         3 days ago\n"
    )
    assert _parse_ollama_list_sizes(stdout) == {}


def test_parse_ollama_list_sizes_skips_header_and_malformed():
    stdout = (
        "NAME              ID              SIZE      MODIFIED\n"
        "ornith-1.5:9b     e5df7dcdd8a2    6.6 GB    4 days ago\n"
        "\n"
        "short\n"
    )
    assert _parse_ollama_list_sizes(stdout) == {"ornith-1.5:9b": int(6.6 * 1024**3)}


def test_list_ollama_runs_ollama_list(mock_runner):
    runner = mock_runner(
        returncode=0,
        stdout="NAME  ID  SIZE  MODIFIED\nornith-1.5:9b  abc  6.6 GB  4 days ago\n",
    )
    sizes = list_ollama(runner)
    runner.assert_called_with(["ollama", "list"], capture_output=True, text=True)
    assert sizes == {"ornith-1.5:9b": int(6.6 * 1024**3)}


def test_list_ollama_raises_on_failure(mock_runner):
    runner = mock_runner(returncode=1, stdout="", stderr="ollama not found")
    with pytest.raises(SyncError, match="ollama list"):
        list_ollama(runner)


def test_reconcile_downloaded_model():
    registry = Registry(
        models=[
            ModelEntry(
                id="ollama/a", family="a", provider_id="ollama", model_name="a",
            ),
        ]
    )
    state = StateStore()
    result = reconcile(registry, state, {"a": 1024})
    assert result.downloaded == ["ollama/a"]
    assert result.not_downloaded == []
    s = state.get("ollama/a")
    assert s.downloaded is True
    assert s.disk_path == "ollama:a"
    assert s.size_bytes == 1024


def test_reconcile_not_downloaded_model():
    registry = Registry(
        models=[
            ModelEntry(
                id="ollama/a", family="a", provider_id="ollama", model_name="a",
            ),
        ]
    )
    state = StateStore()
    result = reconcile(registry, state, {})
    assert result.downloaded == []
    assert result.not_downloaded == ["ollama/a"]
    s = state.get("ollama/a")
    assert s.downloaded is False
    assert s.disk_path is None
    assert s.size_bytes is None


def test_reconcile_skips_non_ollama_models():
    registry = Registry(
        models=[
            ModelEntry(
                id="openrouter/x", family="x", provider_id="openrouter", model_name="x",
            ),
        ]
    )
    state = StateStore()
    result = reconcile(registry, state, {"x": 1024})
    assert result.downloaded == []
    assert result.not_downloaded == []
    assert state.get("openrouter/x").downloaded is False


def test_reconcile_preserves_litellm_exposed():
    registry = Registry(
        models=[
            ModelEntry(
                id="ollama/a", family="a", provider_id="ollama", model_name="a",
            ),
        ]
    )
    state = StateStore()
    state.set("ollama/a", ModelState(downloaded=False, litellm_exposed=True))
    reconcile(registry, state, {"a": 1024})
    assert state.get("ollama/a").litellm_exposed is True


def _result(returncode: int, stdout: str) -> MagicMock:
    r = MagicMock()
    r.returncode = returncode
    r.stdout = stdout
    r.stderr = ""
    return r


def test_sync_reconciles_configured_models():
    runner = MagicMock()
    runner.side_effect = [
        _result(0, "NAME  ID  SIZE  MODIFIED\nornith-1.5:9b  abc  6.6 GB  4 days ago\n"),
    ]
    registry = Registry(
        models=[
            ModelEntry(
                id="ollama/ornith-1.5:9b", family="ornith-1.5:9b",
                provider_id="ollama", model_name="ornith-1.5:9b",
            ),
            ModelEntry(
                id="ollama/other", family="other",
                provider_id="ollama", model_name="other",
            ),
        ]
    )
    state = StateStore()

    result = sync(registry, state, runner)

    assert result.downloaded == ["ollama/ornith-1.5:9b"]
    assert result.not_downloaded == ["ollama/other"]
    assert state.get("ollama/ornith-1.5:9b").downloaded is True
    assert state.get("ollama/other").downloaded is False
    # registry is untouched (no new models added)
    assert len(registry.models) == 2


def test_sync_ignores_ollama_models_not_in_registry():
    runner = MagicMock()
    runner.side_effect = [
        _result(0, "NAME  ID  SIZE  MODIFIED\nunconfigured  abc  6.6 GB  4 days ago\n"),
    ]
    registry = Registry(models=[])
    state = StateStore()

    result = sync(registry, state, runner)

    assert result.downloaded == []
    assert result.not_downloaded == []
    assert len(registry.models) == 0
    assert state.models == {}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/test_sync.py -v`
Expected: FAIL with `ImportError: cannot import name '_parse_ollama_list_sizes' from 'modelman.sync'`

- [ ] **Step 3: Write minimal implementation**

Overwrite `src/modelman/sync.py`:

```python
"""Provider sync — reconcile configured models against provider state.

First provider: ollama (`ollama list`). Sync updates the downloaded state
of models already in registry.toml; it never adds new models. See
docs/superpowers/specs/2026-08-28-modelman-sync-ollama-reconcile-design.md.
"""

from __future__ import annotations

import subprocess
from dataclasses import dataclass, field
from typing import Any, Protocol

from .registry import Registry
from .state import ModelState, StateStore

_SIZE_UNITS = {"B": 1, "KB": 1024, "MB": 1024**2, "GB": 1024**3, "TB": 1024**4}


def _parse_size(num_str: str, unit: str) -> int | None:
    """Parse a `<number> <UNIT>` size pair into bytes, or None if invalid."""
    unit = unit.upper()
    if unit not in _SIZE_UNITS:
        return None
    try:
        return int(float(num_str) * _SIZE_UNITS[unit])
    except ValueError:
        return None


def _parse_ollama_list_sizes(stdout: str) -> dict[str, int]:
    """Parse `ollama list` into {model_name: size_bytes} for downloaded models.

    Cloud rows (SIZE column `-`) are skipped — they are not downloaded.
    """
    sizes: dict[str, int] = {}
    for line in stdout.splitlines():
        line = line.strip()
        if not line or line.startswith("NAME"):
            continue
        parts = line.split()
        if len(parts) < 4:
            continue
        name = parts[0]
        if parts[2] == "-":
            continue
        size = _parse_size(parts[2], parts[3])
        if size is not None:
            sizes[name] = size
    return sizes


class SyncError(Exception):
    """Raised when a provider's discovery command fails."""


class _Runner(Protocol):
    def __call__(self, args: list[str], **kwargs: Any) -> Any: ...


def _default_runner(args: list[str], **kwargs: Any):
    return subprocess.run(args, **kwargs)


def list_ollama(runner: _Runner | None = None) -> dict[str, int]:
    """Run `ollama list` and return {model_name: size_bytes} for downloaded models."""
    r = (runner or _default_runner)(["ollama", "list"], capture_output=True, text=True)
    if r.returncode != 0:
        raise SyncError(f"`ollama list` failed (exit {r.returncode})")
    return _parse_ollama_list_sizes(r.stdout)


@dataclass
class SyncResult:
    downloaded: list[str] = field(default_factory=list)
    not_downloaded: list[str] = field(default_factory=list)


def reconcile(
    registry: Registry, state: StateStore, downloaded: dict[str, int]
) -> SyncResult:
    """Update downloaded/disk_path/size_bytes for configured ollama models.

    litellm_exposed is preserved (owned by the LiteLLM feature, not sync).
    Non-ollama models are untouched.
    """
    result = SyncResult()
    for m in registry.models:
        if m.provider_id != "ollama":
            continue
        existing = state.get(m.id)
        if m.model_name in downloaded:
            state.set(
                m.id,
                ModelState(
                    downloaded=True,
                    disk_path=f"ollama:{m.model_name}",
                    size_bytes=downloaded[m.model_name],
                    litellm_exposed=existing.litellm_exposed,
                ),
            )
            result.downloaded.append(m.id)
        else:
            state.set(
                m.id,
                ModelState(
                    downloaded=False,
                    litellm_exposed=existing.litellm_exposed,
                ),
            )
            result.not_downloaded.append(m.id)
    return result


def sync(
    registry: Registry,
    state: StateStore,
    runner: _Runner | None = None,
) -> SyncResult:
    """Reconcile configured ollama models against `ollama list`."""
    downloaded = list_ollama(runner)
    return reconcile(registry, state, downloaded)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `uv run pytest tests/test_sync.py -v`
Expected: PASS (11 tests)

- [ ] **Step 5: Commit**

```bash
git add src/modelman/sync.py tests/test_sync.py
git commit -m "feat(sync): reconcile configured ollama models instead of discovering"
```

---

## Task 2: Update `sync` command + CLI test

**Files:**
- Modify: `src/modelman/main.py`
- Test: `tests/commands/test_sync.py`

- [ ] **Step 1: Write the failing test**

Overwrite `tests/commands/test_sync.py`:

```python
"""`modelman sync` reconciles configured ollama models against
`ollama list` and writes modelman.toml. The sync logic itself is covered
by tests/test_sync.py; this covers the command wiring (load -> sync ->
save state -> report)."""

from unittest.mock import patch

from typer.testing import CliRunner

from modelman.main import app
from modelman.registry import AuthConfig, ProviderEntry, Registry, save_registry
from modelman.sync import SyncError, SyncResult


def _seed_registry(tmp_path, monkeypatch):
    registry_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    save_registry(
        Registry(
            providers=[
                ProviderEntry(id="ollama", name="Ollama", auth=AuthConfig(type="none"))
            ]
        ),
        registry_path,
    )
    monkeypatch.setenv("MODELMAN_REGISTRY", str(registry_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    return registry_path, state_path


def test_sync_command_saves_state_and_reports(tmp_path, monkeypatch):
    registry_path, state_path = _seed_registry(tmp_path, monkeypatch)
    with patch("modelman.main.run_sync") as run_sync:
        run_sync.return_value = SyncResult(
            downloaded=["ollama/x"], not_downloaded=["ollama/y"]
        )
        runner = CliRunner()
        result = runner.invoke(app, ["sync"])
        assert result.exit_code == 0
        assert "1 downloaded, 1 not downloaded" in result.stdout
        assert state_path.exists()  # modelman.toml written


def test_sync_command_reports_error_on_failure(tmp_path, monkeypatch):
    _seed_registry(tmp_path, monkeypatch)
    with patch("modelman.main.run_sync") as run_sync:
        run_sync.side_effect = SyncError("`ollama list` failed (exit 1)")
        runner = CliRunner()
        result = runner.invoke(app, ["sync"])
        assert result.exit_code == 1
        assert "ollama list" in result.output
```

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/commands/test_sync.py -v`
Expected: FAIL with `AssertionError: assert '1 downloaded, 1 not downloaded' in 'Synced ollama: added 0, refreshed 0, skipped 0.\n'`

- [ ] **Step 3: Write minimal implementation**

In `src/modelman/main.py`, replace the `sync` command body:

```python
@app.command()
def sync() -> None:
    """Reconcile configured ollama models against `ollama list`."""
    registry = load_registry()
    state = load_state()
    try:
        result = run_sync(registry, state)
    except SyncError as exc:
        typer.echo(f"error: {exc}", err=True)
        raise typer.Exit(1) from exc
    save_state(state)
    typer.echo(
        f"Synced ollama: {len(result.downloaded)} downloaded, "
        f"{len(result.not_downloaded)} not downloaded."
    )
```

Note: `save_registry` is still imported and used by the `migrate` command — do not remove the import.

- [ ] **Step 4: Run test to verify it passes**

Run: `uv run pytest tests/commands/test_sync.py -v`
Expected: PASS (2 tests)

- [ ] **Step 5: Commit**

```bash
git add src/modelman/main.py tests/commands/test_sync.py
git commit -m "feat(sync): report downloaded/not-downloaded, stop writing registry"
```

---

## Final verification

- [ ] Run the full suite: `uv run pytest -q`
- [ ] Run lint + typecheck: `make check`
- [ ] Run the CLI smoke test: `uv run modelman sync` (with ollama installed; expect a "downloaded / not downloaded" summary)

---

## Self-review notes

- **Spec coverage:** reconcile-not-discover (Task 1 `reconcile`/`sync`), `list_ollama` (Task 1), `SyncResult` redefinition (Task 1), command saves state only + new summary (Task 2), CLI-not-package (no code change — decision documented in spec). All spec sections map to a task.
- **Type consistency:** `list_ollama` returns `dict[str, int]` in both its definition and `sync`'s use; `reconcile(registry, state, downloaded)` signature matches `sync`'s call; `SyncResult.downloaded`/`not_downloaded` are used identically in `reconcile`, the `sync` tests, and the CLI test.
- **No placeholders:** every code step contains complete code; every command has an expected result.
