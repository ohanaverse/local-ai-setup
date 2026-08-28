# Modelman LiteLLM Exposure Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a user toggle a model's `litellm_exposed` flag — via a `modelman expose`/`unexpose` CLI subcommand or an `l` key in the TUI — which writes/removes the model's `model_list` entry in LiteLLM's `config.yaml` and flips the flag in `modelman.toml`.

**Architecture:** A new `src/modelman/litellm.py` module owns all LiteLLM knowledge: the provider→`model` prefix mapping, `model_list` entry construction, config.yaml read/update/write (preserving unrecognized rows + `general_settings`), and the `expose_model`/`unexpose_model` orchestration used by both CLI and TUI. The CLI adds flat `expose`/`unexpose` subcommands. The TUI adds an `l` keybinding and an "EXPOSED" column to `ModelScreen`, queuing exposure changes that apply on exit alongside downloads/deletes via `PendingChanges`.

**Tech Stack:** Python 3.13, Typer, Textual, PyYAML (already a dependency), pytest.

**Spec:** `docs/superpowers/specs/2026-08-28-modelman-litellm-exposure-design.md`

---

## File Structure

- **Create: `src/modelman/litellm.py`** — prefix mapping, entry construction, config read/write, `expose_model`/`unexpose_model`.
- **Modify: `src/modelman/main.py`** — `expose`/`unexpose` subcommands.
- **Modify: `src/modelman/queue.py`** — `PendingChanges` gains `exposes` + `litellm_path`; `apply()` runs exposure changes.
- **Modify: `src/modelman/screens/forms.py`** — `ConfirmExitDialog` gains an `exposes` param.
- **Modify: `src/modelman/screens/models.py`** — `l` key, `queued_exposes`, "EXPOSED" column, apply wiring.
- **Modify: `docs/ROADMAP.md`** — mark Phase 3 done.
- **Create: `tests/test_litellm.py`** — entry construction + config read/write.
- **Create: `tests/test_expose.py`** — `expose_model`/`unexpose_model` orchestration.
- **Create: `tests/commands/test_expose.py`** — CLI wiring.
- **Modify: `tests/screens/test_app_navigation.py`** — TUI key/column/queue tests.

Files NOT touched: `registry.py`, `state.py`, `sync.py`, `providers/*`.

---

## Task 1: Add `litellm.py` — entry construction + config read/write

**Files:**
- Create: `src/modelman/litellm.py`
- Test: `tests/test_litellm.py`

- [ ] **Step 1: Write the failing tests**

Create `tests/test_litellm.py`:

```python
"""Tests for LiteLLM model_list entry construction and config read/write."""

import pytest

from modelman.litellm import (
    LiteLLMConfigError,
    build_model_list_entry,
    load_litellm_config,
    remove_exposed,
    save_litellm_config,
    set_exposed,
)
from modelman.registry import AuthConfig, ModelEntry, ProviderEntry


def _provider(pid, *, base_url=None, secret_ref=None, auth_type="none"):
    return ProviderEntry(
        id=pid,
        name=pid,
        auth=AuthConfig(type=auth_type, base_url=base_url, secret_ref=secret_ref),
    )


def _model(mid, provider_id, model_name, model_info=None):
    return ModelEntry(
        id=mid,
        family="f",
        provider_id=provider_id,
        model_name=model_name,
        model_info=model_info or {},
    )


def test_build_entry_ollama():
    entry = build_model_list_entry(
        _model("ollama/qwen3.8:27b-mlx", "ollama", "qwen3.8:27b-mlx"),
        _provider("ollama", base_url="http://localhost:11434"),
    )
    assert entry == {
        "model_name": "ollama/qwen3.8:27b-mlx",
        "litellm_params": {
            "model": "ollama_chat/qwen3.8:27b-mlx",
            "api_base": "http://localhost:11434",
        },
    }


def test_build_entry_omlx():
    entry = build_model_list_entry(
        _model("omlx/Qwen3.8-27B-4bit", "omlx", "Qwen3.8-27B-4bit"),
        _provider("omlx", base_url="http://localhost:8000/v1"),
    )
    assert entry["litellm_params"]["model"] == "openai/Qwen3.8-27B-4bit"
    assert entry["litellm_params"]["api_key"] == "not-needed"


def test_build_entry_llamacpp_uses_fixed_model():
    entry = build_model_list_entry(
        _model("llamacpp/ornith-1.5-35b", "llamacpp", "ornith-1.5-35b"),
        _provider("llamacpp", base_url="http://localhost:8080/v1"),
    )
    assert entry["litellm_params"]["model"] == "openai/local-model"
    assert entry["litellm_params"]["api_key"] == "dummy-key"


def test_build_entry_openrouter_uses_secret_ref():
    entry = build_model_list_entry(
        _model("openrouter/qwen/qwen3.8-27b", "openrouter", "qwen/qwen3.8-27b"),
        _provider(
            "openrouter",
            base_url="https://openrouter.ai/api/v1",
            secret_ref="sk-or-v1-abc",
            auth_type="api_key",
        ),
    )
    assert entry["litellm_params"]["model"] == "openrouter/qwen/qwen3.8-27b"
    assert entry["litellm_params"]["api_key"] == "sk-or-v1-abc"


def test_build_entry_copies_model_info():
    entry = build_model_list_entry(
        _model(
            "ollama/x", "ollama", "x",
            model_info={"supports_function_calling": True},
        ),
        _provider("ollama", base_url="http://localhost:11434"),
    )
    assert entry["model_info"] == {"supports_function_calling": True}


def test_build_entry_unknown_provider_raises():
    from modelman.litellm import ExposeError

    with pytest.raises(ExposeError):
        build_model_list_entry(
            _model("foo/x", "foo", "x"), _provider("foo")
        )


def test_set_exposed_adds_new_row():
    config = {"model_list": [], "general_settings": {"database_url": "x"}}
    set_exposed(config, "ollama/a", {"model_name": "ollama/a"})
    assert config["model_list"] == [{"model_name": "ollama/a"}]
    assert config["general_settings"] == {"database_url": "x"}


def test_set_exposed_replaces_existing_row():
    config = {"model_list": [{"model_name": "ollama/a", "old": True}]}
    set_exposed(config, "ollama/a", {"model_name": "ollama/a", "new": True})
    assert config["model_list"] == [{"model_name": "ollama/a", "new": True}]


def test_remove_exposed_removes_row():
    config = {"model_list": [{"model_name": "ollama/a"}, {"model_name": "ollama/b"}]}
    remove_exposed(config, "ollama/a")
    assert config["model_list"] == [{"model_name": "ollama/b"}]


def test_remove_exposed_noop_when_absent():
    config = {"model_list": [{"model_name": "ollama/b"}]}
    remove_exposed(config, "ollama/a")
    assert config["model_list"] == [{"model_name": "ollama/b"}]


def test_load_litellm_config_missing_raises(tmp_path):
    with pytest.raises(LiteLLMConfigError):
        load_litellm_config(tmp_path / "nope.yaml")


def test_save_roundtrip_preserves_general_settings(tmp_path):
    path = tmp_path / "config.yaml"
    config = {
        "model_list": [{"model_name": "ollama/a", "litellm_params": {"model": "x"}}],
        "general_settings": {"database_url": "postgresql://x"},
    }
    save_litellm_config(config, path)
    loaded = load_litellm_config(path)
    assert loaded == config
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `uv run pytest tests/test_litellm.py -v`
Expected: FAIL with `ModuleNotFoundError: No module named 'modelman.litellm'`

- [ ] **Step 3: Write the implementation**

Create `src/modelman/litellm.py`:

```python
"""LiteLLM exposure — build model_list entries and update config.yaml.

modelman manages only the `model_list` section of LiteLLM's config.yaml,
keyed by registry model id as `model_name`. `general_settings` and any
unrecognized rows are preserved. See
docs/superpowers/specs/2026-08-28-modelman-litellm-exposure-design.md.
"""

from __future__ import annotations

import contextlib
import os
import tempfile
from pathlib import Path
from typing import Any

import yaml

from .registry import ModelEntry, ProviderEntry, Registry
from .state import ModelState, StateStore

# Provider -> LiteLLM `model` field prefix. llamacpp is special-cased to
# the fixed `openai/local-model` (its api_base points at the local server).
LITELLM_MODEL_PREFIXES = {
    "ollama": "ollama_chat/",
    "omlx": "openai/",
    "llamacpp": "openai/local-model",
    "openrouter": "openrouter/",
}


class LiteLLMConfigError(Exception):
    """Raised when LiteLLM's config.yaml is missing or malformed."""


class ExposeError(Exception):
    """Raised when a model cannot be exposed (not downloaded, unknown, etc.)."""


def default_litellm_config_path() -> Path:
    """Compute the LiteLLM config path lazily so env overrides work in tests."""
    return Path(
        os.environ.get("MODELMAN_LITELLM_CONFIG", "~/.config/litellm/config.yaml")
    ).expanduser()


def _api_key(provider: ProviderEntry) -> str | None:
    """The api_key to write for a provider, or None to omit it.

    Cloud providers (openrouter) use the configured secret_ref; local
    OpenAI-compatible providers (omlx/llamacpp) use a placeholder the
    server ignores. ollama needs no key.
    """
    if provider.id == "openrouter":
        return provider.auth.secret_ref
    if provider.id == "omlx":
        return "not-needed"
    if provider.id == "llamacpp":
        return "dummy-key"
    return None


def build_model_list_entry(model: ModelEntry, provider: ProviderEntry) -> dict[str, Any]:
    """Build a LiteLLM `model_list` row for a registry model.

    `model_name` is the registry model id; `litellm_params.model` uses the
    provider's prefix; `api_base` comes from the provider's auth config;
    `model_info` is copied from the model.
    """
    prefix = LITELLM_MODEL_PREFIXES.get(provider.id)
    if prefix is None:
        raise ExposeError(f"provider {provider.id!r} has no LiteLLM mapping")
    if provider.id == "llamacpp":
        litellm_model = prefix  # fixed "openai/local-model"
    else:
        litellm_model = f"{prefix}{model.model_name}"
    params: dict[str, Any] = {
        "model": litellm_model,
        "api_base": provider.auth.base_url,
    }
    key = _api_key(provider)
    if key is not None:
        params["api_key"] = key
    entry: dict[str, Any] = {
        "model_name": model.id,
        "litellm_params": params,
    }
    if model.model_info:
        entry["model_info"] = dict(model.model_info)
    return entry


def load_litellm_config(path: Path) -> dict[str, Any]:
    """Read LiteLLM's config.yaml. Errors if missing or not a mapping."""
    if not path.exists():
        raise LiteLLMConfigError(f"LiteLLM config not found: {path}")
    with open(path) as f:
        data = yaml.safe_load(f)
    if not isinstance(data, dict):
        raise LiteLLMConfigError(f"LiteLLM config is not a mapping: {path}")
    return data


def set_exposed(config: dict[str, Any], model_id: str, entry: dict[str, Any]) -> None:
    """Add or replace the model_list row keyed by `model_id`."""
    rows = config.setdefault("model_list", [])
    for i, row in enumerate(rows):
        if row.get("model_name") == model_id:
            rows[i] = entry
            return
    rows.append(entry)


def remove_exposed(config: dict[str, Any], model_id: str) -> None:
    """Remove the model_list row keyed by `model_id` (no-op if absent)."""
    rows = config.get("model_list")
    if not rows:
        return
    config["model_list"] = [r for r in rows if r.get("model_name") != model_id]


def save_litellm_config(config: dict[str, Any], path: Path) -> None:
    """Write config.yaml atomically (temp file + rename).

    NOTE: PyYAML does not preserve comments/formatting on round-trip.
    Unrecognized rows and general_settings are preserved as data; comments
    are not. This is an accepted limitation of the current implementation.
    """
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, tmp_name = tempfile.mkstemp(dir=path.parent, prefix=f".{path.name}.", suffix=".tmp")
    try:
        with os.fdopen(fd, "w") as f:
            yaml.safe_dump(config, f, sort_keys=False, default_flow_style=False)
        os.replace(tmp_name, path)
    except BaseException:
        with contextlib.suppress(OSError):
            os.unlink(tmp_name)
        raise
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `uv run pytest tests/test_litellm.py -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add src/modelman/litellm.py tests/test_litellm.py
git commit -m "feat(litellm): add model_list entry construction and config read/write"
```

---

## Task 2: Add `expose_model`/`unexpose_model` orchestration

**Files:**
- Modify: `src/modelman/litellm.py`
- Test: `tests/test_expose.py`

- [ ] **Step 1: Write the failing tests**

Create `tests/test_expose.py`:

```python
"""Tests for expose_model/unexpose_model orchestration."""

import pytest

from modelman.litellm import (
    ExposeError,
    LiteLLMConfigError,
    expose_model,
    save_litellm_config,
    unexpose_model,
)
from modelman.registry import AuthConfig, ModelEntry, ProviderEntry, Registry
from modelman.state import ModelState, StateStore


def _registry(*, cloud=False):
    providers = [
        ProviderEntry(
            id="ollama", name="Ollama",
            auth=AuthConfig(type="none", base_url="http://localhost:11434"),
        ),
        ProviderEntry(
            id="openrouter", name="OpenRouter",
            auth=AuthConfig(
                type="api_key", base_url="https://openrouter.ai/api/v1",
                secret_ref="sk-or-v1-abc",
            ),
        ),
    ]
    models = [
        ModelEntry(
            id="ollama/a", family="f", provider_id="ollama", model_name="a",
        ),
        ModelEntry(
            id="openrouter/x", family="f", provider_id="openrouter", model_name="x",
        ),
    ]
    return Registry(providers=providers, models=models)


def _state(*, downloaded_a=True):
    store = StateStore()
    store.set("ollama/a", ModelState(downloaded=downloaded_a))
    return store


def _seed_config(tmp_path):
    path = tmp_path / "config.yaml"
    save_litellm_config({"model_list": [], "general_settings": {"database_url": "x"}}, path)
    return path


def test_expose_model_writes_entry_and_flag(tmp_path):
    registry = _registry()
    state = _state()
    path = _seed_config(tmp_path)
    expose_model(registry, state, "ollama/a", path)
    assert state.get("ollama/a").litellm_exposed is True
    from modelman.litellm import load_litellm_config

    config = load_litellm_config(path)
    assert config["model_list"][0]["model_name"] == "ollama/a"
    assert config["general_settings"] == {"database_url": "x"}


def test_expose_model_cloud_ok_without_download(tmp_path):
    registry = _registry()
    state = _state(downloaded_a=False)
    path = _seed_config(tmp_path)
    expose_model(registry, state, "openrouter/x", path)
    assert state.get("openrouter/x").litellm_exposed is True


def test_expose_model_not_downloaded_raises(tmp_path):
    registry = _registry()
    state = _state(downloaded_a=False)
    path = _seed_config(tmp_path)
    with pytest.raises(ExposeError, match="not downloaded"):
        expose_model(registry, state, "ollama/a", path)


def test_expose_model_unknown_raises(tmp_path):
    registry = _registry()
    state = _state()
    path = _seed_config(tmp_path)
    with pytest.raises(ExposeError, match="not found in registry"):
        expose_model(registry, state, "ollama/nope", path)


def test_expose_model_missing_config_raises(tmp_path):
    registry = _registry()
    state = _state()
    with pytest.raises(LiteLLMConfigError):
        expose_model(registry, state, "ollama/a", tmp_path / "missing.yaml")


def test_expose_model_idempotent(tmp_path):
    registry = _registry()
    state = _state()
    path = _seed_config(tmp_path)
    expose_model(registry, state, "ollama/a", path)
    expose_model(registry, state, "ollama/a", path)
    from modelman.litellm import load_litellm_config

    config = load_litellm_config(path)
    assert len(config["model_list"]) == 1


def test_unexpose_model_removes_entry_and_flag(tmp_path):
    registry = _registry()
    state = _state()
    path = _seed_config(tmp_path)
    expose_model(registry, state, "ollama/a", path)
    unexpose_model(registry, state, "ollama/a", path)
    assert state.get("ollama/a").litellm_exposed is False
    from modelman.litellm import load_litellm_config

    config = load_litellm_config(path)
    assert config["model_list"] == []


def test_unexpose_model_idempotent(tmp_path):
    registry = _registry()
    state = _state()
    path = _seed_config(tmp_path)
    unexpose_model(registry, state, "ollama/a", path)
    assert state.get("ollama/a").litellm_exposed is False
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `uv run pytest tests/test_expose.py -v`
Expected: FAIL with `ImportError: cannot import name 'expose_model' from 'modelman.litellm'`

- [ ] **Step 3: Write the implementation**

Append to `src/modelman/litellm.py`:

```python
def _is_cloud(provider_id: str) -> bool:
    return provider_id == "openrouter"


def _set_exposed_flag(state: StateStore, model_id: str, exposed: bool) -> None:
    existing = state.get(model_id)
    state.set(
        model_id,
        ModelState(
            downloaded=existing.downloaded,
            disk_path=existing.disk_path,
            size_bytes=existing.size_bytes,
            litellm_exposed=exposed,
        ),
    )


def expose_model(
    registry: Registry,
    state: StateStore,
    model_id: str,
    litellm_path: Path,
) -> None:
    """Expose a model through LiteLLM: write its model_list entry and flip
    the modelman.toml flag. Errors if the model is unknown, its provider
    has no LiteLLM mapping, or it isn't downloaded (unless cloud)."""
    try:
        model = registry.model(model_id)
    except KeyError:
        raise ExposeError(f"model {model_id!r} not found in registry") from None
    provider = registry.provider(model.provider_id)
    if model.provider_id not in LITELLM_MODEL_PREFIXES:
        raise ExposeError(f"provider {model.provider_id!r} has no LiteLLM mapping")
    if not _is_cloud(model.provider_id) and not state.get(model_id).downloaded:
        raise ExposeError(f"model {model_id!r} is not downloaded")
    entry = build_model_list_entry(model, provider)
    config = load_litellm_config(litellm_path)
    set_exposed(config, model_id, entry)
    save_litellm_config(config, litellm_path)
    _set_exposed_flag(state, model_id, True)


def unexpose_model(
    registry: Registry,
    state: StateStore,
    model_id: str,
    litellm_path: Path,
) -> None:
    """Remove a model's model_list entry and clear its modelman.toml flag."""
    try:
        registry.model(model_id)
    except KeyError:
        raise ExposeError(f"model {model_id!r} not found in registry") from None
    config = load_litellm_config(litellm_path)
    remove_exposed(config, model_id)
    save_litellm_config(config, litellm_path)
    _set_exposed_flag(state, model_id, False)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `uv run pytest tests/test_expose.py -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add src/modelman/litellm.py tests/test_expose.py
git commit -m "feat(litellm): add expose_model/unexpose_model orchestration"
```

---

## Task 3: Add `expose`/`unexpose` CLI subcommands

**Files:**
- Modify: `src/modelman/main.py`
- Test: `tests/commands/test_expose.py`

- [ ] **Step 1: Write the failing tests**

Create `tests/commands/test_expose.py`:

```python
"""`modelman expose`/`unexpose` write/remove a LiteLLM model_list entry and
flip the modelman.toml flag. The orchestration is covered by
tests/test_expose.py; this covers the command wiring (load -> validate ->
write -> save state -> report)."""

from typer.testing import CliRunner

from modelman.litellm import save_litellm_config
from modelman.main import app
from modelman.registry import AuthConfig, ModelEntry, ProviderEntry, Registry, save_registry
from modelman.state import ModelState, StateStore, save_state


def _seed(tmp_path, monkeypatch, *, downloaded=True):
    registry_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    litellm_path = tmp_path / "litellm" / "config.yaml"
    save_registry(
        Registry(
            providers=[
                ProviderEntry(
                    id="ollama", name="Ollama",
                    auth=AuthConfig(type="none", base_url="http://localhost:11434"),
                )
            ],
            models=[
                ModelEntry(
                    id="ollama/a", family="f", provider_id="ollama", model_name="a",
                )
            ],
        ),
        registry_path,
    )
    store = StateStore()
    store.set("ollama/a", ModelState(downloaded=downloaded))
    save_state(store, state_path)
    save_litellm_config({"model_list": [], "general_settings": {}}, litellm_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(registry_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    monkeypatch.setenv("MODELMAN_LITELLM_CONFIG", str(litellm_path))
    return litellm_path


def test_expose_command_writes_and_reports(tmp_path, monkeypatch):
    litellm_path = _seed(tmp_path, monkeypatch)
    runner = CliRunner()
    result = runner.invoke(app, ["expose", "ollama/a"])
    assert result.exit_code == 0
    assert "Exposed ollama/a" in result.stdout
    from modelman.litellm import load_litellm_config

    config = load_litellm_config(litellm_path)
    assert config["model_list"][0]["model_name"] == "ollama/a"


def test_expose_command_errors_on_not_downloaded(tmp_path, monkeypatch):
    _seed(tmp_path, monkeypatch, downloaded=False)
    runner = CliRunner()
    result = runner.invoke(app, ["expose", "ollama/a"])
    assert result.exit_code == 1
    assert "not downloaded" in result.output


def test_unexpose_command_writes_and_reports(tmp_path, monkeypatch):
    litellm_path = _seed(tmp_path, monkeypatch)
    runner = CliRunner()
    runner.invoke(app, ["expose", "ollama/a"])
    result = runner.invoke(app, ["unexpose", "ollama/a"])
    assert result.exit_code == 0
    assert "Unexposed ollama/a" in result.stdout
    from modelman.litellm import load_litellm_config

    config = load_litellm_config(litellm_path)
    assert config["model_list"] == []
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `uv run pytest tests/commands/test_expose.py -v`
Expected: FAIL with `UsageError: No such command 'expose'`

- [ ] **Step 3: Write the implementation**

In `src/modelman/main.py`, update the imports:

```python
from .litellm import (
    ExposeError,
    LiteLLMConfigError,
    default_litellm_config_path,
    expose_model,
    unexpose_model,
)
```

Add these two commands after the `sync` command:

```python
@app.command()
def expose(
    model_id: str = typer.Argument(..., help="Registry model id to expose"),
) -> None:
    """Expose a model through LiteLLM (writes a model_list entry)."""
    registry = load_registry()
    state = load_state()
    try:
        expose_model(registry, state, model_id, default_litellm_config_path())
    except (ExposeError, LiteLLMConfigError) as exc:
        typer.echo(f"error: {exc}", err=True)
        raise typer.Exit(1) from exc
    save_state(state)
    typer.echo(f"Exposed {model_id} through LiteLLM.")


@app.command()
def unexpose(
    model_id: str = typer.Argument(..., help="Registry model id to stop exposing"),
) -> None:
    """Remove a model's LiteLLM model_list entry."""
    registry = load_registry()
    state = load_state()
    try:
        unexpose_model(registry, state, model_id, default_litellm_config_path())
    except (ExposeError, LiteLLMConfigError) as exc:
        typer.echo(f"error: {exc}", err=True)
        raise typer.Exit(1) from exc
    save_state(state)
    typer.echo(f"Unexposed {model_id}.")
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `uv run pytest tests/commands/test_expose.py -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add src/modelman/main.py tests/commands/test_expose.py
git commit -m "feat(cli): add expose/unexpose subcommands"
```

---

## Task 4: Extend `PendingChanges` + `ConfirmExitDialog` for exposure changes

**Files:**
- Modify: `src/modelman/queue.py`
- Modify: `src/modelman/screens/forms.py`
- Test: `tests/test_queue.py`

- [ ] **Step 1: Write the failing tests**

Append to `tests/test_queue.py`:

```python
def test_apply_runs_expose_changes(tmp_path):
    from modelman.litellm import load_litellm_config, save_litellm_config
    from modelman.registry import AuthConfig, ModelEntry, ProviderEntry, Registry, save_registry
    from modelman.state import ModelState, StateStore, save_state

    registry_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    litellm_path = tmp_path / "config.yaml"
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
    save_registry(registry, registry_path)
    state = StateStore()
    state.set("ollama/a", ModelState(downloaded=True))
    save_state(state, state_path)
    save_litellm_config({"model_list": [], "general_settings": {}}, litellm_path)

    pending = PendingChanges(
        registry=registry,
        state=state,
        family="f",
        registry_path=registry_path,
        state_path=state_path,
        providers={},
        exposes=[("ollama/a", True)],
        litellm_path=litellm_path,
    )
    pending.apply()
    assert state.get("ollama/a").litellm_exposed is True
    config = load_litellm_config(litellm_path)
    assert config["model_list"][0]["model_name"] == "ollama/a"
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `uv run pytest tests/test_queue.py::test_apply_runs_expose_changes -v`
Expected: FAIL with `TypeError: PendingChanges.__init__() got an unexpected keyword argument 'exposes'`

- [ ] **Step 3: Write the implementation**

In `src/modelman/queue.py`, update the imports:

```python
from .litellm import expose_model, unexpose_model
from .providers._progress import DownloadCancelled
from .registry import save_registry
from .state import ModelState, save_state
```

Add two fields to the `PendingChanges` dataclass (after `deletes`):

```python
    deletes: list[tuple[str, VariantSpec]] = field(default_factory=list)
    # (model_id, target_exposed) pairs applied after downloads, before save.
    exposes: list[tuple[str, bool]] = field(default_factory=list)
    litellm_path: Path = field(default_factory=Path)
    failures: list[str] = field(default_factory=list)
```

Update the empty-queue early return in `apply()`:

```python
        if not self.downloads and not self.deletes and not self.exposes:
            emit("apply:done")
            return
```

Add the exposure loop after the downloads loop and before the `if aborted():` / `emit("save:start")` block:

```python
        for model_id, exposed in self.exposes:
            if aborted():
                return
            verb = "expose" if exposed else "unexpose"
            emit(f"{verb}:start|{model_id}|{model_id}")
            try:
                if exposed:
                    expose_model(self.registry, self.state, model_id, self.litellm_path)
                else:
                    unexpose_model(self.registry, self.state, model_id, self.litellm_path)
            except Exception as exc:  # noqa: BLE001
                reason = _reason(exc)
                self.failures.append(f"{verb} {model_id}: {exc}")
                emit(f"{verb}:fail|{model_id}|{model_id}|{reason}")
                continue
            emit(f"{verb}:done|{model_id}|{model_id}")
```

In `src/modelman/screens/forms.py`, update `ConfirmExitDialog`:

```python
    def __init__(
        self,
        downloads: list,
        deletes: list,
        exposes: list[tuple[str, bool]] | None = None,
    ) -> None:
        super().__init__()
        self._downloads = downloads
        self._deletes = deletes
        self._exposes = exposes or []
```

Update its `compose`:

```python
    def compose(self) -> ComposeResult:
        with Vertical():
            yield Label(
                f"Pending: download {len(self._downloads)} · delete {len(self._deletes)}"
                f" · expose {len(self._exposes)}"
            )
            for v in self._downloads:
                yield Label(f"  ↓ {v['id']} ({v['provider']})")
            for v in self._deletes:
                yield Label(f"  × {v['id']} ({v['provider']})")
            for model_id, exposed in self._exposes:
                mark = "L" if exposed else "–"
                yield Label(f"  {mark} {model_id}")
            yield Label("Apply, cancel, or discard these changes?")
            with Horizontal():
                yield Button("Cancel", id="cancel", variant="default")
                yield Button("Discard", id="discard", variant="warning")
                yield Button("Apply", id="apply", variant="primary")
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `uv run pytest tests/test_queue.py -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add src/modelman/queue.py src/modelman/screens/forms.py tests/test_queue.py
git commit -m "feat(queue): apply litellm exposure changes on exit"
```

---

## Task 5: Add `l` key, `queued_exposes`, and EXPOSED column to `ModelScreen`

**Files:**
- Modify: `src/modelman/screens/models.py`
- Test: `tests/screens/test_app_navigation.py`

- [ ] **Step 1: Write the failing tests**

Append to `tests/screens/test_app_navigation.py`:

```python
@pytest.mark.asyncio
async def test_l_key_queues_expose_and_column_renders(tmp_path, monkeypatch):
    """Pressing `l` on a downloaded model queues an exposure change and the
    EXPOSED column reflects the queued target state."""
    from textual.widgets import DataTable

    from modelman.app import ModelmanApp
    from modelman.registry import ModelEntry
    from modelman.screens.models import ModelScreen

    reg_path, state_path = _seed_registry_and_state(
        tmp_path,
        monkeypatch,
        models=(
            ModelEntry(
                id="ollama/a", family="f", provider_id="ollama", model_name="a",
            ),
        ),
        downloaded={"ollama/a": "ollama:a"},
    )
    from modelman.registry import load_registry
    from modelman.state import load_state

    ms = ModelScreen(
        registry=load_registry(),
        state=load_state(),
        family="f",
        registry_path=reg_path,
        state_path=state_path,
        available_providers=["ollama"],
    )
    app = ModelmanApp()
    async with app.run_test() as pilot:
        pilot.app.push_screen(ms)
        await pilot.pause()
        mt = ms.query_one("#model-table", DataTable)
        # Focus the model table so `l` targets a model row.
        mt.focus()
        await pilot.press("l")
        await pilot.pause()
        assert "ollama/a" in ms.queued_exposes
        assert ms.queued_exposes["ollama/a"] is True
        # EXPOSED column exists (NAME, STATUS, SIZE, PATH, EXPOSED).
        assert len(mt.columns) == 5
        # pending bar reflects the queued expose
        bar = ms.query_one("#pending-bar")
        assert "expose 1" in str(bar.renderable)
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `uv run pytest tests/screens/test_app_navigation.py::test_l_key_queues_expose_and_column_renders -v`
Expected: FAIL with `AttributeError: 'ModelScreen' object has no attribute 'queued_exposes'`

- [ ] **Step 3: Write the implementation**

In `src/modelman/screens/models.py`, update the imports:

```python
from ..litellm import default_litellm_config_path
from ..queue import PendingChanges
```

Add the `l` binding to `BINDINGS`:

```python
        ("r", "reconcile", "Reconcile"),
        ("l", "toggle_expose", "Toggle LiteLLM"),
```

In `__init__`, add the `queued_exposes` dict after `queued_deletes`:

```python
        self.queued_downloads: dict[str, VariantSpec] = {}
        self.queued_deletes: dict[str, VariantSpec] = {}
        # model_id -> target exposed state (True to expose, False to unexpose).
        self.queued_exposes: dict[str, bool] = {}
```

In `on_mount`, add the EXPOSED column:

```python
        mt.add_columns("NAME", "STATUS", "SIZE", "PATH", "EXPOSED")
```

In `_load_models_for_provider`, compute the exposed cell and pass it to `add_row`:

```python
            exposed = self.state.get(m.id).litellm_exposed
            if m.id in self.queued_exposes:
                exposed = self.queued_exposes[m.id]
            exposed_str = "L" if exposed else "–"
            mt.add_row(m.model_name, status, size_str, path, exposed_str, key=m.id)
```

Add the `action_toggle_expose` method (after `action_toggle_download`):

```python
    def action_toggle_expose(self) -> None:
        mt = self.query_one("#model-table", DataTable)
        if mt.row_count == 0:
            return
        row_key = list(mt.rows.keys())[mt.cursor_row]
        mid = str(row_key.value)
        entry = next((m for m in self.registry.models if m.id == mid), None)
        if entry is None:
            return
        is_cloud = entry.provider_id == "openrouter"
        if not is_cloud and not self._is_downloaded(mid):
            self.app.notify("Model must be downloaded before exposing")
            return
        current = self.state.get(mid).litellm_exposed
        if mid in self.queued_exposes:
            current = self.queued_exposes[mid]
        self.queued_exposes[mid] = not current
        self._refresh_pending_bar()
        if self.selected_provider is not None:
            self._load_models_for_provider(self.selected_provider)
```

Update `_refresh_pending_bar`:

```python
    def _refresh_pending_bar(self) -> None:
        bar = self.query_one("#pending-bar", Static)
        bar.update(
            f"Pending: download {len(self.queued_downloads)} · delete {len(self.queued_deletes)}"
            f" · expose {len(self.queued_exposes)}"
        )
```

Update `action_back` to include `queued_exposes`:

```python
    def action_back(self) -> None:
        if not self.queued_downloads and not self.queued_deletes and not self.queued_exposes:
            self.app.pop_screen()
            return
        from .forms import ConfirmExitDialog

        self.app.push_screen(
            ConfirmExitDialog(
                downloads=list(self.queued_downloads.values()),
                deletes=list(self.queued_deletes.values()),
                exposes=list(self.queued_exposes.items()),
            ),
            self._on_exit_confirm,
        )
```

Update `_on_exit_confirm` discard path to clear `queued_exposes`:

```python
        if choice == "discard":
            self._restore_snapshot()
            self.queued_downloads.clear()
            self.queued_deletes.clear()
            self.queued_exposes.clear()
            self.app.pop_screen()
            return
```

Update `_run_apply` to pass `exposes` + `litellm_path` to `PendingChanges`:

```python
        pending = PendingChanges(
            registry=self.registry,
            state=self.state,
            family=self.family,
            registry_path=self.registry_path,
            state_path=self.state_path,
            providers=providers,
            downloads=[(mid, spec) for mid, spec in self.queued_downloads.items()],
            deletes=[(mid, spec) for mid, spec in self.queued_deletes.items()],
            exposes=list(self.queued_exposes.items()),
            litellm_path=default_litellm_config_path(),
        )
        register(pending)
        pending.apply(on_event=on_event, on_progress=on_progress)
        # The closure runs on the StatusScreen's worker thread; mutate
        # in-memory queue state from here too so subsequent opens of this
        # screen see an empty queue.
        self.queued_downloads.clear()
        self.queued_deletes.clear()
        self.queued_exposes.clear()
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `uv run pytest tests/screens/test_app_navigation.py -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add src/modelman/screens/models.py tests/screens/test_app_navigation.py
git commit -m "feat(tui): add l key, queued_exposes, and EXPOSED column to ModelScreen"
```

---

## Task 6: Update ROADMAP

**Files:**
- Modify: `docs/ROADMAP.md`

- [ ] **Step 1: Update the ROADMAP**

In `docs/ROADMAP.md`, change the Phase 3 row from "next" to done:

```markdown
## Phase 3 — LiteLLM exposure

| PR | Scope | Status |
|---|---|---|
| — | `expose`/`unexpose` CLI + TUI `l` key; write/remove `model_list` entries | ✅ done |

Spec: `docs/superpowers/specs/2026-08-28-modelman-litellm-exposure-design.md`
```

- [ ] **Step 2: Commit**

```bash
git add docs/ROADMAP.md
git commit -m "docs: mark Phase 3 (LiteLLM exposure) done"
```

---

## Final verification

- [ ] Run the full suite: `uv run pytest -q`
- [ ] Run lint + typecheck: `make check`
- [ ] Run the CLI smoke test: `uv run modelman expose ollama/qwen3.8:27b-mlx` (expect "Exposed ... through LiteLLM." and a `model_list` entry in `~/.config/litellm/config.yaml`)

---

## Self-review notes

- **Spec coverage:** `litellm.py` prefix mapping + entry construction (Task 1), config read/write preserving `general_settings` (Task 1), `expose_model`/`unexpose_model` validation + atomic writes (Task 2), CLI subcommands (Task 3), TUI `l` key + EXPOSED column + queued apply (Tasks 4-5), ROADMAP (Task 6). All spec sections map to a task.
- **Type consistency:** `PendingChanges.exposes` is `list[tuple[str, bool]]` in both its definition (Task 4) and its callers (`_run_apply` in Task 5, `ConfirmExitDialog` in Task 4). `expose_model`/`unexpose_model` take `(registry, state, model_id, litellm_path)` consistently across CLI (Task 3), queue (Task 4), and tests.
- **Comment preservation:** PyYAML does not preserve comments on round-trip. This is documented in `save_litellm_config` and accepted per the design's "where feasible" — unrecognized rows and `general_settings` are preserved as data. If comment preservation is required, a follow-up would swap in `ruamel.yaml`.
- **No placeholders:** every code step contains complete code; every command has an expected result.
