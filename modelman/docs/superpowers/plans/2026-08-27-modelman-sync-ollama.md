# `modelman sync` (ollama) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `modelman sync` CLI subcommand that discovers ollama models via `ollama list` and merges them into `registry.toml` (curated-wins) while updating `modelman.toml` state.

**Architecture:** A new `sync.py` module holds the provider-agnostic merge/state logic plus the ollama discoverer; a `sync` Typer command wires it into the CLI; the TUI's add/edit path flips `source` to `"curated"`.

**Tech Stack:** Python 3.13, Typer, Textual, pytest (pytest-asyncio), uv.

**Spec:** `docs/superpowers/specs/2026-08-27-modelman-sync-ollama-design.md`

---

## File Structure

- **Create: `src/modelman/sync.py`** — `SyncResult`, `SyncError`, `_parse_size`, `_parse_ollama_list`, `discover_ollama`, `merge`, `update_state`, `sync`.
- **Create: `tests/test_sync.py`** — unit tests for the above.
- **Create: `tests/commands/test_sync.py`** — CLI test for the `sync` command.
- **Create: `tests/screens/test_models.py`** — test that `_variant_to_model_entry` sets `source="curated"`.
- **Modify: `src/modelman/main.py`** — add the `sync` command.
- **Modify: `src/modelman/screens/models.py`** — set `source="curated"` in `_variant_to_model_entry`.

Files NOT touched: `registry.py`, `state.py`, `providers/*.py`, `ollama_caps.py` (reused as-is).

---

## Task 1: `_parse_ollama_list` + `_parse_size`

**Files:**
- Create: `src/modelman/sync.py`
- Test: `tests/test_sync.py`

- [ ] **Step 1: Write the failing test**

Create `tests/test_sync.py`:

```python
"""Provider sync — ollama discovery, curated-wins merge, state update."""

from modelman.sync import _parse_ollama_list


def test_parse_ollama_list_local_row():
    stdout = (
        "NAME              ID              SIZE      MODIFIED\n"
        "ornith-1.5:9b     e5df7dcdd8a2    6.6 GB    4 days ago\n"
    )
    models, sizes = _parse_ollama_list(stdout)
    assert len(models) == 1
    m = models[0]
    assert m.id == "ollama/ornith-1.5:9b"
    assert m.family == "ornith-1.5:9b"
    assert m.provider_id == "ollama"
    assert m.model_name == "ornith-1.5:9b"
    assert m.location == "local"
    assert m.source == "discovered"
    assert sizes == {"ornith-1.5:9b": int(6.6 * 1024**3)}


def test_parse_ollama_list_cloud_row():
    stdout = (
        "NAME              ID              SIZE      MODIFIED\n"
        "some-cloud        def456          -         3 days ago\n"
    )
    models, sizes = _parse_ollama_list(stdout)
    assert len(models) == 1
    assert models[0].location == "cloud"
    assert sizes == {}


def test_parse_ollama_list_skips_header_and_malformed():
    stdout = (
        "NAME              ID              SIZE      MODIFIED\n"
        "ornith-1.5:9b     e5df7dcdd8a2    6.6 GB    4 days ago\n"
        "\n"
        "short\n"
    )
    models, sizes = _parse_ollama_list(stdout)
    assert len(models) == 1
```

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/test_sync.py -v`
Expected: FAIL with `ModuleNotFoundError: No module named 'modelman.sync'`

- [ ] **Step 3: Write minimal implementation**

Create `src/modelman/sync.py`:

```python
"""Provider sync — refresh registry.toml + modelman.toml from live providers.

First provider: ollama (`ollama list`). See
docs/superpowers/specs/2026-08-27-modelman-sync-ollama-design.md.
"""

from __future__ import annotations

from .registry import ModelEntry

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


def _parse_ollama_list(stdout: str) -> tuple[list[ModelEntry], dict[str, int]]:
    """Parse `ollama list` output into (models, sizes).

    The SIZE column's first token is `-` for cloud models (not pulled
    locally) and a number for local models. `sizes` maps tag -> size_bytes
    for local models only.
    """
    models: list[ModelEntry] = []
    sizes: dict[str, int] = {}
    for line in stdout.splitlines():
        line = line.strip()
        if not line or line.startswith("NAME"):
            continue
        parts = line.split()
        if len(parts) < 3:
            continue
        name = parts[0]
        if parts[2] == "-":
            location = "cloud"
        else:
            location = "local"
            if len(parts) >= 4:
                size = _parse_size(parts[2], parts[3])
                if size is not None:
                    sizes[name] = size
        models.append(
            ModelEntry(
                id=f"ollama/{name}",
                family=name,
                provider_id="ollama",
                model_name=name,
                location=location,
                source="discovered",
            )
        )
    return models, sizes
```

- [ ] **Step 4: Run test to verify it passes**

Run: `uv run pytest tests/test_sync.py -v`
Expected: PASS (3 tests)

- [ ] **Step 5: Commit**

```bash
git add src/modelman/sync.py tests/test_sync.py
git commit -m "feat(sync): parse ollama list into ModelEntry candidates"
```

---

## Task 2: `discover_ollama`

**Files:**
- Modify: `src/modelman/sync.py`
- Test: `tests/test_sync.py`

- [ ] **Step 1: Write the failing test**

Append to `tests/test_sync.py`:

```python
import pytest

from modelman.sync import SyncError, discover_ollama


def test_discover_ollama_runs_ollama_list(mock_runner):
    runner = mock_runner(
        returncode=0,
        stdout="NAME  ID  SIZE  MODIFIED\nornith-1.5:9b  abc  6.6 GB  4 days ago\n",
    )
    models, sizes = discover_ollama(runner)
    runner.assert_called_with(["ollama", "list"], capture_output=True, text=True)
    assert len(models) == 1
    assert models[0].id == "ollama/ornith-1.5:9b"
    assert sizes == {"ornith-1.5:9b": int(6.6 * 1024**3)}


def test_discover_ollama_raises_on_failure(mock_runner):
    runner = mock_runner(returncode=1, stdout="", stderr="ollama not found")
    with pytest.raises(SyncError, match="ollama list"):
        discover_ollama(runner)
```

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/test_sync.py -v`
Expected: FAIL with `ImportError: cannot import name 'discover_ollama'`

- [ ] **Step 3: Write minimal implementation**

Add to `src/modelman/sync.py` (update the imports and append):

```python
import subprocess
from typing import Any, Protocol

from .registry import ModelEntry


class SyncError(Exception):
    """Raised when a provider's discovery command fails."""


class _Runner(Protocol):
    def __call__(self, args: list[str], **kwargs: Any) -> Any: ...


def _default_runner(args: list[str], **kwargs: Any):
    return subprocess.run(args, **kwargs)


def discover_ollama(
    runner: _Runner | None = None,
) -> tuple[list[ModelEntry], dict[str, int]]:
    """Run `ollama list` and return (models, sizes)."""
    r = (runner or _default_runner)(["ollama", "list"], capture_output=True, text=True)
    if r.returncode != 0:
        raise SyncError(f"`ollama list` failed (exit {r.returncode})")
    return _parse_ollama_list(r.stdout)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `uv run pytest tests/test_sync.py -v`
Expected: PASS (5 tests)

- [ ] **Step 5: Commit**

```bash
git add src/modelman/sync.py tests/test_sync.py
git commit -m "feat(sync): discover ollama models via ollama list"
```

---

## Task 3: `merge` + `SyncResult`

**Files:**
- Modify: `src/modelman/sync.py`
- Test: `tests/test_sync.py`

- [ ] **Step 1: Write the failing test**

Append to `tests/test_sync.py`:

```python
from modelman.registry import ModelEntry, Registry
from modelman.sync import merge


def test_merge_skips_curated():
    registry = Registry(
        models=[
            ModelEntry(
                id="ollama/a", family="a", provider_id="ollama",
                model_name="a", source="curated", tags=["code"],
            ),
        ]
    )
    discovered = [
        ModelEntry(
            id="ollama/a", family="a", provider_id="ollama",
            model_name="a", source="discovered", location="cloud",
        ),
    ]
    result = merge(registry, discovered)
    assert result.skipped == ["ollama/a"]
    assert result.added == []
    assert result.refreshed == []
    # curated row untouched: tags preserved, location NOT refreshed
    assert registry.models[0].tags == ["code"]
    assert registry.models[0].location is None


def test_merge_adds_new():
    registry = Registry(models=[])
    discovered = [
        ModelEntry(
            id="ollama/a", family="a", provider_id="ollama",
            model_name="a", source="discovered", location="local",
        ),
    ]
    result = merge(registry, discovered)
    assert result.added == ["ollama/a"]
    assert len(registry.models) == 1


def test_merge_refreshes_discovered_location():
    registry = Registry(
        models=[
            ModelEntry(
                id="ollama/a", family="a", provider_id="ollama",
                model_name="a", source="discovered", location="local",
            ),
        ]
    )
    discovered = [
        ModelEntry(
            id="ollama/a", family="a", provider_id="ollama",
            model_name="a", source="discovered", location="cloud",
        ),
    ]
    result = merge(registry, discovered)
    assert result.refreshed == ["ollama/a"]
    assert registry.models[0].location == "cloud"
```

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/test_sync.py -v`
Expected: FAIL with `ImportError: cannot import name 'merge'`

- [ ] **Step 3: Write minimal implementation**

Add to `src/modelman/sync.py` (update imports and append):

```python
from dataclasses import dataclass, field

from .registry import ModelEntry, Registry


@dataclass
class SyncResult:
    added: list[str] = field(default_factory=list)
    refreshed: list[str] = field(default_factory=list)
    skipped: list[str] = field(default_factory=list)


def merge(registry: Registry, discovered: list[ModelEntry]) -> SyncResult:
    """Apply curated-wins merge in place. Returns a SyncResult.

    - id absent -> append (added).
    - id present and source == "curated" -> skip (skipped).
    - id present and source == "discovered" -> refresh location (refreshed).

    tags/cost/family are never touched.
    """
    result = SyncResult()
    by_id = {m.id: m for m in registry.models}
    for d in discovered:
        existing = by_id.get(d.id)
        if existing is None:
            registry.models.append(d)
            by_id[d.id] = d
            result.added.append(d.id)
        elif existing.source == "curated":
            result.skipped.append(d.id)
        else:
            existing.location = d.location
            result.refreshed.append(d.id)
    return result
```

- [ ] **Step 4: Run test to verify it passes**

Run: `uv run pytest tests/test_sync.py -v`
Expected: PASS (8 tests)

- [ ] **Step 5: Commit**

```bash
git add src/modelman/sync.py tests/test_sync.py
git commit -m "feat(sync): curated-wins merge"
```

---

## Task 4: `update_state`

**Files:**
- Modify: `src/modelman/sync.py`
- Test: `tests/test_sync.py`

- [ ] **Step 1: Write the failing test**

Append to `tests/test_sync.py`:

```python
from modelman.state import ModelState, StateStore
from modelman.sync import update_state


def test_update_state_local_model():
    state = StateStore()
    models = [
        ModelEntry(
            id="ollama/a", family="a", provider_id="ollama",
            model_name="a", location="local",
        ),
    ]
    update_state(state, models, {"a": 1024})
    s = state.get("ollama/a")
    assert s.downloaded is True
    assert s.disk_path == "ollama:a"
    assert s.size_bytes == 1024


def test_update_state_cloud_model():
    state = StateStore()
    models = [
        ModelEntry(
            id="ollama/a", family="a", provider_id="ollama",
            model_name="a", location="cloud",
        ),
    ]
    update_state(state, models, {})
    s = state.get("ollama/a")
    assert s.downloaded is False
    assert s.disk_path is None
    assert s.size_bytes is None


def test_update_state_preserves_litellm_exposed():
    state = StateStore()
    state.set("ollama/a", ModelState(downloaded=False, litellm_exposed=True))
    models = [
        ModelEntry(
            id="ollama/a", family="a", provider_id="ollama",
            model_name="a", location="local",
        ),
    ]
    update_state(state, models, {"a": 1024})
    assert state.get("ollama/a").litellm_exposed is True
```

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/test_sync.py -v`
Expected: FAIL with `ImportError: cannot import name 'update_state'`

- [ ] **Step 3: Write minimal implementation**

Add to `src/modelman/sync.py` (update imports and append):

```python
from .state import ModelState, StateStore


def update_state(
    state: StateStore, models: list[ModelEntry], sizes: dict[str, int]
) -> None:
    """Update downloaded/disk_path/size_bytes for discovered models.

    local -> downloaded=True + disk_path + size_bytes; cloud ->
    downloaded=False. litellm_exposed is preserved (owned by the LiteLLM
    feature, not sync).
    """
    for m in models:
        existing = state.get(m.id)
        if m.location == "local":
            state.set(
                m.id,
                ModelState(
                    downloaded=True,
                    disk_path=f"ollama:{m.model_name}",
                    size_bytes=sizes.get(m.model_name),
                    litellm_exposed=existing.litellm_exposed,
                ),
            )
        else:
            state.set(
                m.id,
                ModelState(
                    downloaded=False,
                    litellm_exposed=existing.litellm_exposed,
                ),
            )
```

- [ ] **Step 4: Run test to verify it passes**

Run: `uv run pytest tests/test_sync.py -v`
Expected: PASS (11 tests)

- [ ] **Step 5: Commit**

```bash
git add src/modelman/sync.py tests/test_sync.py
git commit -m "feat(sync): update downloaded state from ollama list"
```

---

## Task 5: `sync` orchestrator

**Files:**
- Modify: `src/modelman/sync.py`
- Test: `tests/test_sync.py`

- [ ] **Step 1: Write the failing test**

Append to `tests/test_sync.py`:

```python
from unittest.mock import MagicMock

from modelman.sync import sync


def _result(returncode: int, stdout: str) -> MagicMock:
    r = MagicMock()
    r.returncode = returncode
    r.stdout = stdout
    r.stderr = ""
    return r


def test_sync_full_flow():
    runner = MagicMock()
    runner.side_effect = [
        _result(0, "NAME  ID  SIZE  MODIFIED\nornith-1.5:9b  abc  6.6 GB  4 days ago\n"),
        _result(0, "Capabilities\n    tools\n"),
    ]
    registry = Registry(models=[])
    state = StateStore()

    result = sync(registry, state, runner)

    assert result.added == ["ollama/ornith-1.5:9b"]
    assert registry.models[0].model_info == {"supports_function_calling": True}
    assert state.get("ollama/ornith-1.5:9b").downloaded is True


def test_sync_does_not_rerun_ollama_show_for_existing_model():
    # Existing discovered rows keep their model_info; only new models
    # trigger `ollama show`.
    runner = MagicMock()
    runner.side_effect = [
        _result(0, "NAME  ID  SIZE  MODIFIED\nornith-1.5:9b  abc  6.6 GB  4 days ago\n"),
    ]
    registry = Registry(
        models=[
            ModelEntry(
                id="ollama/ornith-1.5:9b", family="ornith-1.5:9b",
                provider_id="ollama", model_name="ornith-1.5:9b",
                source="discovered", location="local",
                model_info={"supports_vision": True},
            ),
        ]
    )
    state = StateStore()

    sync(registry, state, runner)

    # Only one subprocess call (ollama list); no ollama show.
    assert runner.call_count == 1
    assert registry.models[0].model_info == {"supports_vision": True}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/test_sync.py -v`
Expected: FAIL with `ImportError: cannot import name 'sync'`

- [ ] **Step 3: Write minimal implementation**

Add to `src/modelman/sync.py` (update imports and append):

```python
from .ollama_caps import auto_detect_model_info


def sync(
    registry: Registry,
    state: StateStore,
    runner: _Runner | None = None,
) -> SyncResult:
    """Discover ollama models, merge into the registry, update state."""
    models, sizes = discover_ollama(runner)
    existing_ids = {m.id for m in registry.models}
    for m in models:
        if m.id not in existing_ids:
            m.model_info = auto_detect_model_info(m.model_name, runner)
    result = merge(registry, models)
    update_state(state, models, sizes)
    return result
```

- [ ] **Step 4: Run test to verify it passes**

Run: `uv run pytest tests/test_sync.py -v`
Expected: PASS (13 tests)

- [ ] **Step 5: Commit**

```bash
git add src/modelman/sync.py tests/test_sync.py
git commit -m "feat(sync): orchestrate discover + merge + state update"
```

---

## Task 6: `sync` command in `main.py`

**Files:**
- Modify: `src/modelman/main.py`
- Test: `tests/commands/test_sync.py`

- [ ] **Step 1: Write the failing test**

Create `tests/commands/test_sync.py`:

```python
"""`modelman sync` discovers ollama models and merges them into
registry.toml + modelman.toml. The sync logic itself is covered by
tests/test_sync.py; this covers the command wiring (load -> sync ->
save -> report)."""

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


def test_sync_command_saves_and_reports(tmp_path, monkeypatch):
    registry_path, state_path = _seed_registry(tmp_path, monkeypatch)
    with patch("modelman.main.run_sync") as run_sync:
        run_sync.return_value = SyncResult(
            added=["ollama/x"], refreshed=[], skipped=[]
        )
        runner = CliRunner()
        result = runner.invoke(app, ["sync"])
        assert result.exit_code == 0
        assert "added 1" in result.stdout
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
Expected: FAIL with `Error: No such command 'sync'`

- [ ] **Step 3: Write minimal implementation**

In `src/modelman/main.py`, change the import block:

```python
from .registry import load_registry, save_registry
from .state import load_state, save_state
from .sync import SyncError, sync as run_sync
```

Then add the command after the `migrate` command:

```python
@app.command()
def sync() -> None:
    """Discover ollama models and merge them into registry.toml."""
    registry = load_registry()
    state = load_state()
    try:
        result = run_sync(registry, state)
    except SyncError as exc:
        typer.echo(f"error: {exc}", err=True)
        raise typer.Exit(1)
    save_registry(registry)
    save_state(state)
    typer.echo(
        f"Synced ollama: added {len(result.added)}, "
        f"refreshed {len(result.refreshed)}, skipped {len(result.skipped)}."
    )
```

- [ ] **Step 4: Run test to verify it passes**

Run: `uv run pytest tests/commands/test_sync.py -v`
Expected: PASS (2 tests)

- [ ] **Step 5: Commit**

```bash
git add src/modelman/main.py tests/commands/test_sync.py
git commit -m "feat(sync): add modelman sync command"
```

---

## Task 7: `source="curated"` in the TUI add/edit path

**Files:**
- Modify: `src/modelman/screens/models.py`
- Test: `tests/screens/test_models.py`

- [ ] **Step 1: Write the failing test**

Create `tests/screens/test_models.py`:

```python
"""Tests for ModelScreen helpers (the add/edit adapter)."""

from modelman.registry import AuthConfig, ProviderEntry, Registry
from modelman.screens.models import _variant_to_model_entry


def test_variant_to_model_entry_sets_source_curated():
    registry = Registry(
        providers=[
            ProviderEntry(id="ollama", name="Ollama", auth=AuthConfig(type="none"))
        ]
    )
    variant = {"id": "ollama/x", "provider": "ollama", "name": "x"}
    entry = _variant_to_model_entry(variant, family="x", registry=registry)
    assert entry.source == "curated"
```

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/screens/test_models.py -v`
Expected: FAIL with `AssertionError: assert None == 'curated'`

- [ ] **Step 3: Write minimal implementation**

In `src/modelman/screens/models.py`, in `_variant_to_model_entry`, add
`source="curated"` to the `ModelEntry(...)` constructor:

```python
    return ModelEntry(
        id=variant["id"],
        family=family,
        provider_id=provider_id,
        model_name=name,
        source="curated",
        model_info=model_info,
        fetch=fetch,
    )
```

- [ ] **Step 4: Run test to verify it passes**

Run: `uv run pytest tests/screens/test_models.py -v`
Expected: PASS (1 test)

- [ ] **Step 5: Commit**

```bash
git add src/modelman/screens/models.py tests/screens/test_models.py
git commit -m "feat(tui): mark add/edit models as curated"
```

---

## Final verification

- [ ] Run the full suite: `uv run pytest -q`
- [ ] Run lint + typecheck: `make check`
- [ ] Run the CLI smoke test: `uv run modelman sync` (with ollama installed; expect a summary line)

---

## Self-review notes

- **Spec coverage:** entry point (Task 6), discovery (Tasks 1-2), curated-wins merge (Task 3), state update (Task 4), model_info new-only (Task 5), promote-on-edit (Task 7). All spec sections map to a task.
- **Type consistency:** `SyncResult` fields `added`/`refreshed`/`skipped` are used identically in Tasks 3, 5, 6. `discover_ollama` returns `(models, sizes)` consistently in Tasks 2 and 5. `update_state(state, models, sizes)` signature is consistent in Tasks 4 and 5.
- **No placeholders:** every code step contains complete code; every command has an expected result.
