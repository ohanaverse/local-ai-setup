# Shared Model Registry — Phase 1 (modelman: schema, state, migration) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give modelman a canonical `registry.toml` (shared providers/models) and `modelman.toml` (per-machine state) data layer, plus a one-time `modelman migrate` command that imports both modelman's legacy `config.yaml`+`families/*.yaml` and (optionally) agent-worktree's legacy `config.toml` into it.

**Architecture:** Two new small modules — `registry.py` (dataclasses + TOML load/save for the canonical registry) and `state.py` (dataclasses + TOML load/save for per-machine state) — sharing one atomic-write/None-pruning helper module (`_toml_io.py`). A `migrate.py` module builds a `Registry`+`StateStore` from the legacy sources with a "wt's curated fields win" merge rule, wired up via a new `modelman migrate` CLI command. Nothing in this phase touches the existing TUI, provider-download flow, or agent-worktree's Go code — this phase only stands up the new files and the importer; wiring the TUI to read/write them, provider-sync, and LiteLLM `model_list` management are follow-up phases/plans, as is agent-worktree switching to consume `registry.toml`. After this phase, existing modelman/wt behavior is completely unchanged; `registry.toml`/`modelman.toml` simply now exist and are populated.

**Tech Stack:** Python 3.13, dataclasses (no pydantic — matches this repo's existing `config.py`/`manifest.py`/`settings.py` style), stdlib `tomllib` for reading TOML, new `tomli-w` dependency for writing TOML, `pyyaml` (already a dependency) for reading legacy YAML, `typer`/`typer.testing.CliRunner` for the CLI command and its test.

**Spec:** `docs/superpowers/specs/2026-08-27-shared-model-registry-design.md`

## Global Constraints

- `requires-python = "==3.13.*"` (pyproject.toml) — no syntax/stdlib beyond 3.13.
- New files follow this repo's existing module style: a dataclass (or a few) plus plain `load_*`/`save_*` functions, an env-var override for the default path (pattern: `Path(os.environ.get("MODELMAN_<X>", "~/.config/local-ai/<file>")).expanduser()`, computed lazily inside the function so tests can `monkeypatch.setenv` before calling it), and a `*Error` exception class for malformed input. See `src/modelman/config.py` and `src/modelman/settings.py` for the two existing variants of this pattern (required-file-raises vs. optional-file-returns-defaults).
- All new on-disk writes go through atomic write (temp file in the same directory + `os.replace`) — never write the target path directly.
- TOML has no null type: every dataclass→dict conversion before writing must drop `None` values/keys recursively.
- Per the spec's collision policy: agent-worktree's legacy data (tags, cost, family, location) always wins over modelman's own legacy data for a model that exists in both; modelman's legacy data only adds new entries or fills in fields it uniquely owns (`fetch`, `model_info`, download state) — never overwrites tags/cost/family/location.
- Run `uv run pytest` (all tests) and `uv run ruff check .` before every commit in this plan; `make check` runs lint+typecheck without auto-fixing if you want a single combined command.
- No code comments beyond a short module-level docstring explaining *why* the file exists (matching existing style) — no per-line comments restating what the code does.

---

### Task 1: Shared TOML I/O helper

**Files:**
- Create: `src/modelman/_toml_io.py`
- Test: `tests/test_toml_io.py`
- Modify: `pyproject.toml` (add `tomli-w` dependency)

**Interfaces:**
- Produces: `drop_none(value: Any) -> Any`, `atomic_write_toml(payload: dict[str, Any], path: Path) -> None` in `modelman/_toml_io.py`. Both Task 2 and Task 3 import these.

- [ ] **Step 1: Add the `tomli-w` dependency**

```bash
uv add tomli-w
```

This adds `tomli-w>=1.x` to `pyproject.toml`'s `dependencies` and updates `uv.lock`. (`tomllib` for *reading* TOML is already available — it's stdlib since Python 3.11 — only writing needs a new package.)

- [ ] **Step 2: Write the failing tests**

Create `tests/test_toml_io.py`:

```python
"""Shared TOML I/O helpers used by registry.py and state.py: recursively
dropping None values (TOML has no null type) and atomic writes (temp file
+ rename) so a crash mid-write never leaves a corrupt config on disk."""

import tomllib

import pytest

from modelman._toml_io import atomic_write_toml, drop_none


def test_drop_none_strips_nested_none_values():
    payload = {"a": 1, "b": None, "c": {"d": None, "e": 2}, "f": [{"g": None, "h": 3}]}
    assert drop_none(payload) == {"a": 1, "c": {"e": 2}, "f": [{"h": 3}]}


def test_atomic_write_toml_round_trips_and_creates_parent_dirs(tmp_path):
    target = tmp_path / "nested" / "registry.toml"

    atomic_write_toml({"models": [{"id": "ollama/x"}]}, target)

    assert target.exists()
    with open(target, "rb") as f:
        assert tomllib.load(f) == {"models": [{"id": "ollama/x"}]}


def test_atomic_write_toml_leaves_no_tmp_file_on_dump_failure(tmp_path, monkeypatch):
    import modelman._toml_io as toml_io

    def _boom(*args, **kwargs):
        raise ValueError("dump failed")

    monkeypatch.setattr(toml_io.tomli_w, "dump", _boom)
    target = tmp_path / "registry.toml"

    with pytest.raises(ValueError, match="dump failed"):
        atomic_write_toml({"a": 1}, target)

    assert not target.exists()
    assert list(tmp_path.iterdir()) == []
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `uv run pytest tests/test_toml_io.py -v`
Expected: FAIL with `ModuleNotFoundError: No module named 'modelman._toml_io'`

- [ ] **Step 4: Implement `_toml_io.py`**

```python
"""Shared atomic TOML write + None-pruning helpers.

Both registry.py (registry.toml) and state.py (modelman.toml) write TOML
files that a single interactive process can still be interrupted mid-write
(crash, Ctrl-C) — never a concurrent-writer problem, since modelman is the
sole writer of both files. Atomic write (temp file + rename) is enough;
no locking needed.
"""

from __future__ import annotations

import contextlib
import os
import tempfile
from pathlib import Path
from typing import Any

import tomli_w


def drop_none(value: Any) -> Any:
    """Recursively strip None values/keys — TOML has no null type."""
    if isinstance(value, dict):
        return {k: drop_none(v) for k, v in value.items() if v is not None}
    if isinstance(value, list):
        return [drop_none(v) for v in value]
    return value


def atomic_write_toml(payload: dict[str, Any], path: Path) -> None:
    """Write `payload` to `path` as TOML via temp file + rename.

    On any failure the temp file is removed and `path` is left untouched.
    """
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, tmp_name = tempfile.mkstemp(dir=path.parent, prefix=f".{path.name}.", suffix=".tmp")
    try:
        with os.fdopen(fd, "wb") as f:
            tomli_w.dump(payload, f)
        os.replace(tmp_name, path)
    except BaseException:
        with contextlib.suppress(OSError):
            os.unlink(tmp_name)
        raise
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `uv run pytest tests/test_toml_io.py -v`
Expected: PASS (3 tests)

- [ ] **Step 6: Lint and commit**

```bash
uv run ruff check src/modelman/_toml_io.py tests/test_toml_io.py
git add pyproject.toml uv.lock src/modelman/_toml_io.py tests/test_toml_io.py
git commit -m "feat: add shared atomic TOML write helper - completes plan item #1"
```

---

### Task 2: Canonical registry (`registry.toml`)

**Files:**
- Create: `src/modelman/registry.py`
- Test: `tests/test_registry.py`

**Interfaces:**
- Consumes: `drop_none`, `atomic_write_toml` from `modelman._toml_io` (Task 1).
- Produces: `RegistryError`, `AuthConfig`, `ProviderEntry`, `Cost`, `Fetch`, `ModelEntry`, `Registry` (with `.provider(provider_id: str) -> ProviderEntry` and `.model(model_id: str) -> ModelEntry`, both raising `KeyError` if not found), `load_registry(path: Path | None = None) -> Registry`, `save_registry(registry: Registry, path: Path | None = None) -> None` in `modelman/registry.py`. Task 4 (migration) and Task 5 (CLI) both import these.

- [ ] **Step 1: Write the failing tests**

Create `tests/test_registry.py`:

```python
"""registry.toml is the canonical, shared model/provider registry per the
2026-08-27 shared-model-registry design spec. These tests cover the load/
save round trip and the minimal validation that catches a hand-edited file
missing a field code elsewhere assumes exists (id, provider_id, model_name,
cost.kind)."""

import pytest

from modelman.registry import (
    AuthConfig,
    Cost,
    Fetch,
    ModelEntry,
    ProviderEntry,
    Registry,
    RegistryError,
    load_registry,
    save_registry,
)


def test_load_registry_missing_file_raises(tmp_path):
    with pytest.raises(RegistryError, match="not found"):
        load_registry(tmp_path / "nonexistent.toml")


def test_save_then_load_round_trips_providers_and_models(tmp_path):
    registry = Registry(
        providers=[
            ProviderEntry(
                id="ollama",
                name="Ollama",
                location="local",
                auth=AuthConfig(type="none", base_url="http://localhost:11434"),
            ),
            ProviderEntry(
                id="omlx",
                name="oMLX",
                location="local",
                model_dir="~/.omlx/models",
                auth=AuthConfig(type="none", base_url="http://localhost:8000"),
            ),
        ],
        models=[
            ModelEntry(
                id="ollama/qwen3.8:27b-mlx",
                family="qwen3.8",
                provider_id="ollama",
                model_name="qwen3.8:27b-mlx",
                location="local",
                source="discovered",
                tags=["code", "design"],
                cost=Cost(kind="free"),
                model_info={"supports_function_calling": True},
            ),
            ModelEntry(
                id="llamacpp/qwen3.8-27b-q4",
                family="qwen3.8",
                provider_id="llamacpp",
                model_name="qwen3.8-27b-q4",
                location="local",
                tags=["code"],
                cost=Cost(kind="free"),
                fetch=Fetch(
                    repo="unsloth/Qwen3.8-27B-GGUF",
                    files=["Qwen3.8-27B-UD-Q4_K_XL.gguf"],
                ),
            ),
        ],
    )
    path = tmp_path / "registry.toml"

    save_registry(registry, path)
    loaded = load_registry(path)

    assert loaded.provider("omlx").model_dir == "~/.omlx/models"
    assert loaded.model("ollama/qwen3.8:27b-mlx").tags == ["code", "design"]
    assert loaded.model("ollama/qwen3.8:27b-mlx").cost == Cost(kind="free")
    assert loaded.model("llamacpp/qwen3.8-27b-q4").fetch == Fetch(
        repo="unsloth/Qwen3.8-27B-GGUF", files=["Qwen3.8-27B-UD-Q4_K_XL.gguf"]
    )


def test_load_registry_missing_provider_id_raises(tmp_path):
    path = tmp_path / "registry.toml"
    path.write_text('[[providers]]\nname = "Ollama"\n')
    with pytest.raises(RegistryError, match="missing required `id`"):
        load_registry(path)


def test_load_registry_missing_required_model_field_raises(tmp_path):
    path = tmp_path / "registry.toml"
    path.write_text(
        '[[providers]]\nid = "ollama"\nname = "Ollama"\n'
        '[providers.auth]\ntype = "none"\n\n'
        '[[models]]\nid = "ollama/x"\nprovider_id = "ollama"\nmodel_name = "x"\n'
    )
    with pytest.raises(RegistryError, match="missing required fields"):
        load_registry(path)


def test_load_registry_missing_cost_kind_raises(tmp_path):
    path = tmp_path / "registry.toml"
    path.write_text(
        '[[providers]]\nid = "ollama"\nname = "Ollama"\n'
        '[providers.auth]\ntype = "none"\n\n'
        '[[models]]\nid = "ollama/x"\nfamily = "x"\nprovider_id = "ollama"\nmodel_name = "x"\n'
        "[models.cost]\nprice_per_million_tokens = 1.0\n"
    )
    with pytest.raises(RegistryError, match="cost missing required `kind`"):
        load_registry(path)


def test_registry_lookup_helpers_raise_keyerror_for_unknown_id():
    registry = Registry()
    with pytest.raises(KeyError):
        registry.provider("nope")
    with pytest.raises(KeyError):
        registry.model("nope")
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `uv run pytest tests/test_registry.py -v`
Expected: FAIL with `ModuleNotFoundError: No module named 'modelman.registry'`

- [ ] **Step 3: Implement `registry.py`**

```python
"""registry.toml — the canonical, shared model/provider registry.

Owned exclusively by modelman (see docs/superpowers/specs/2026-08-27-
shared-model-registry-design.md). agent-worktree reads this file
read-only; it never writes it.
"""

from __future__ import annotations

import os
import tomllib
from dataclasses import asdict, dataclass, field
from pathlib import Path
from typing import Any, Literal

from ._toml_io import atomic_write_toml, drop_none


class RegistryError(Exception):
    """Raised when registry.toml is missing or malformed."""


def _default_registry_path() -> Path:
    """Compute the registry path lazily so env overrides work in tests."""
    return Path(
        os.environ.get("MODELMAN_REGISTRY", "~/.config/local-ai/registry.toml")
    ).expanduser()


@dataclass
class AuthConfig:
    type: str  # "none" | "api_key" | "oauth" | "native"
    secret_ref: str | None = None
    base_url: str | None = None


@dataclass
class ProviderEntry:
    id: str
    name: str
    location: str | None = None  # "local" | "cloud"
    model_dir: str | None = None
    auth: AuthConfig = field(default_factory=lambda: AuthConfig(type="none"))


@dataclass
class Cost:
    kind: Literal["free", "per_token", "subscription"]
    price_per_million_tokens: float | None = None
    price_per_period: float | None = None
    period: str | None = None


@dataclass
class Fetch:
    repo: str | None = None
    files: list[str] | None = None
    quantizations: list[str] | None = None


@dataclass
class ModelEntry:
    id: str
    family: str
    provider_id: str
    model_name: str
    location: str | None = None
    source: str | None = None  # "curated" | "discovered"
    tags: list[str] = field(default_factory=list)
    cost: Cost | None = None
    model_info: dict[str, Any] = field(default_factory=dict)
    fetch: Fetch | None = None


@dataclass
class Registry:
    providers: list[ProviderEntry] = field(default_factory=list)
    models: list[ModelEntry] = field(default_factory=list)

    def provider(self, provider_id: str) -> ProviderEntry:
        for p in self.providers:
            if p.id == provider_id:
                return p
        raise KeyError(f"Unknown provider: {provider_id}")

    def model(self, model_id: str) -> ModelEntry:
        for m in self.models:
            if m.id == model_id:
                return m
        raise KeyError(f"Unknown model: {model_id}")


def load_registry(path: Path | None = None) -> Registry:
    registry_path = Path(path) if path else _default_registry_path()
    if not registry_path.exists():
        raise RegistryError(f"Registry file not found: {registry_path}")
    with open(registry_path, "rb") as f:
        raw = tomllib.load(f)
    return Registry(
        providers=[_parse_provider(p) for p in raw.get("providers", [])],
        models=[_parse_model(m) for m in raw.get("models", [])],
    )


def save_registry(registry: Registry, path: Path | None = None) -> None:
    registry_path = Path(path) if path else _default_registry_path()
    payload = {
        "providers": [drop_none(asdict(p)) for p in registry.providers],
        "models": [drop_none(asdict(m)) for m in registry.models],
    }
    atomic_write_toml(payload, registry_path)


def _parse_provider(raw: dict[str, Any]) -> ProviderEntry:
    if "id" not in raw:
        raise RegistryError(f"Provider entry missing required `id` field: {raw}")
    auth_raw = raw.get("auth", {})
    if "type" not in auth_raw:
        raise RegistryError(f"Provider `{raw['id']}` auth missing required `type` field")
    return ProviderEntry(
        id=raw["id"],
        name=raw.get("name", raw["id"]),
        location=raw.get("location"),
        model_dir=raw.get("model_dir"),
        auth=AuthConfig(
            type=auth_raw["type"],
            secret_ref=auth_raw.get("secret_ref"),
            base_url=auth_raw.get("base_url"),
        ),
    )


def _parse_model(raw: dict[str, Any]) -> ModelEntry:
    required = {"id", "family", "provider_id", "model_name"}
    missing = required - set(raw.keys())
    if missing:
        raise RegistryError(f"Model entry missing required fields {missing}: {raw}")
    cost_raw = raw.get("cost")
    cost = None
    if cost_raw is not None:
        if "kind" not in cost_raw:
            raise RegistryError(f"Model `{raw['id']}` cost missing required `kind` field")
        cost = Cost(
            kind=cost_raw["kind"],
            price_per_million_tokens=cost_raw.get("price_per_million_tokens"),
            price_per_period=cost_raw.get("price_per_period"),
            period=cost_raw.get("period"),
        )
    fetch_raw = raw.get("fetch")
    fetch = (
        Fetch(
            repo=fetch_raw.get("repo"),
            files=fetch_raw.get("files"),
            quantizations=fetch_raw.get("quantizations"),
        )
        if fetch_raw is not None
        else None
    )
    return ModelEntry(
        id=raw["id"],
        family=raw["family"],
        provider_id=raw["provider_id"],
        model_name=raw["model_name"],
        location=raw.get("location"),
        source=raw.get("source"),
        tags=list(raw.get("tags", [])),
        cost=cost,
        model_info=dict(raw.get("model_info", {})),
        fetch=fetch,
    )
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `uv run pytest tests/test_registry.py -v`
Expected: PASS (6 tests)

- [ ] **Step 5: Lint and commit**

```bash
uv run ruff check src/modelman/registry.py tests/test_registry.py
git add src/modelman/registry.py tests/test_registry.py
git commit -m "feat: add canonical registry.toml schema and load/save - completes plan item #2"
```

---

### Task 3: Per-machine state (`modelman.toml`)

**Files:**
- Create: `src/modelman/state.py`
- Test: `tests/test_state.py`

**Interfaces:**
- Consumes: `drop_none`, `atomic_write_toml` from `modelman._toml_io` (Task 1).
- Produces: `ModelState`, `StateStore` (with `.get(model_id: str) -> ModelState`, defaulting to `ModelState()` for an unknown id, and `.set(model_id: str, state: ModelState) -> None`), `load_state(path: Path | None = None) -> StateStore`, `save_state(store: StateStore, path: Path | None = None) -> None` in `modelman/state.py`. Task 4 and Task 5 import these.

- [ ] **Step 1: Write the failing tests**

Create `tests/test_state.py`:

```python
"""modelman.toml — modelman's own per-machine state overlay. Holds only
per-machine mutable state (downloaded/disk_path/size_bytes/litellm_exposed)
keyed by registry model id; registry.toml holds everything that describes
the model itself (see 2026-08-27-shared-model-registry-design.md). The
file is optional: a fresh install has no state yet, so a missing file
returns an empty store rather than raising (matching settings.py's
existing convention for optional per-machine files)."""

from modelman.state import ModelState, StateStore, load_state, save_state


def test_load_state_missing_file_returns_empty_store(tmp_path):
    store = load_state(tmp_path / "nonexistent.toml")
    assert store.models == {}


def test_state_store_get_returns_default_for_unknown_model():
    store = StateStore()
    assert store.get("ollama/x") == ModelState()


def test_save_then_load_round_trips_model_state(tmp_path):
    store = StateStore()
    store.set(
        "llamacpp/qwen3.8-27b-q4",
        ModelState(
            downloaded=True,
            disk_path="/models/qwen3.8-27b-q4.gguf",
            size_bytes=17179869184,
            litellm_exposed=True,
        ),
    )
    path = tmp_path / "modelman.toml"

    save_state(store, path)
    loaded = load_state(path)

    assert loaded.get("llamacpp/qwen3.8-27b-q4") == ModelState(
        downloaded=True,
        disk_path="/models/qwen3.8-27b-q4.gguf",
        size_bytes=17179869184,
        litellm_exposed=True,
    )
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `uv run pytest tests/test_state.py -v`
Expected: FAIL with `ModuleNotFoundError: No module named 'modelman.state'`

- [ ] **Step 3: Implement `state.py`**

```python
"""modelman.toml — modelman's per-machine mutable state overlay.

Owner: modelman only. See registry.py for the canonical, shared model/
provider definitions this state is keyed against, and
docs/superpowers/specs/2026-08-27-shared-model-registry-design.md for the
ownership split.
"""

from __future__ import annotations

import os
import tomllib
from dataclasses import asdict, dataclass, field
from pathlib import Path

from ._toml_io import atomic_write_toml, drop_none


def _default_state_path() -> Path:
    """Compute the state path lazily so env overrides work in tests."""
    return Path(os.environ.get("MODELMAN_STATE", "~/.config/local-ai/modelman.toml")).expanduser()


@dataclass
class ModelState:
    downloaded: bool = False
    disk_path: str | None = None
    size_bytes: int | None = None
    litellm_exposed: bool = False


@dataclass
class StateStore:
    models: dict[str, ModelState] = field(default_factory=dict)

    def get(self, model_id: str) -> ModelState:
        return self.models.get(model_id, ModelState())

    def set(self, model_id: str, state: ModelState) -> None:
        self.models[model_id] = state


def load_state(path: Path | None = None) -> StateStore:
    """Load modelman.toml. Missing file returns an empty store — this file
    is optional, unlike registry.toml, since a fresh install has no
    per-machine download state yet."""
    state_path = Path(path) if path else _default_state_path()
    if not state_path.exists():
        return StateStore()
    with open(state_path, "rb") as f:
        raw = tomllib.load(f)
    models = {
        model_id: ModelState(
            downloaded=entry.get("downloaded", False),
            disk_path=entry.get("disk_path"),
            size_bytes=entry.get("size_bytes"),
            litellm_exposed=entry.get("litellm_exposed", False),
        )
        for model_id, entry in raw.get("model_state", {}).items()
    }
    return StateStore(models=models)


def save_state(store: StateStore, path: Path | None = None) -> None:
    state_path = Path(path) if path else _default_state_path()
    payload = {
        "model_state": {model_id: drop_none(asdict(s)) for model_id, s in store.models.items()}
    }
    atomic_write_toml(payload, state_path)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `uv run pytest tests/test_state.py -v`
Expected: PASS (3 tests)

- [ ] **Step 5: Lint and commit**

```bash
uv run ruff check src/modelman/state.py tests/test_state.py
git add src/modelman/state.py tests/test_state.py
git commit -m "feat: add modelman.toml per-machine state schema and load/save - completes plan item #3"
```

---

### Task 4: Migration importer

**Files:**
- Create: `src/modelman/migrate.py`
- Test: `tests/test_migrate.py`

**Interfaces:**
- Consumes: `Registry`, `ProviderEntry`, `ModelEntry`, `AuthConfig`, `Fetch` from `modelman.registry` (Task 2); `StateStore`, `ModelState` from `modelman.state` (Task 3); the existing `load_manifest(family: str, family_dir: Path | None = None) -> FamilyManifest` from `modelman.manifest`.
- Produces: `MigrationResult` (fields: `registry: Registry`, `state: StateStore`, `warnings: list[str]`), `migrate(legacy_config_path: Path, legacy_family_dir: Path, wt_config_path: Path | None = None) -> MigrationResult` in `modelman/migrate.py`. Task 5 imports `migrate` and `MigrationResult`.

- [ ] **Step 1: Write the failing tests**

Create `tests/test_migrate.py`:

```python
"""Migration from legacy modelman config.yaml + families/*.yaml (and,
optionally, agent-worktree's config.toml) into registry.toml +
modelman.toml. Exercises the "One-time migration" collision policy from
docs/superpowers/specs/2026-08-27-shared-model-registry-design.md: wt's
curated tags/cost/family/location always win for a model that exists in
both sources; modelman's legacy data only adds new entries or fills in
fields it uniquely owns (fetch, model_info, download state)."""

from pathlib import Path

from modelman.manifest import FamilyManifest, save_manifest
from modelman.migrate import migrate
from modelman.providers.base import VariantSpec


def _write_modelman_config(path: Path) -> None:
    path.write_text(
        "providers:\n"
        "  ollama:\n"
        "    type: ollama\n"
        "  llamacpp:\n"
        "    type: llamacpp\n"
        "  omlx:\n"
        "    type: omlx\n"
        "    model_dir: ~/.omlx/models\n"
    )


def _write_wt_config(path: Path) -> None:
    path.write_text(
        '[[providers]]\n'
        '  id = "ollama"\n'
        '  name = "Ollama"\n'
        '  location = "local"\n'
        "  [providers.auth]\n"
        '    type = "none"\n'
        '    base_url = "http://localhost:11434"\n'
        "\n"
        "[[models]]\n"
        '  id = "ollama/qwen3.8:27b-mlx"\n'
        '  family = "qwen3.8"\n'
        '  provider_id = "ollama"\n'
        '  model_name = "qwen3.8:27b-mlx"\n'
        '  location = "local"\n'
        '  tags = ["code", "design"]\n'
    )


def test_migrate_modelman_only_when_wt_config_absent(tmp_path):
    config_path = tmp_path / "config.yaml"
    family_dir = tmp_path / "families"
    family_dir.mkdir()
    _write_modelman_config(config_path)
    save_manifest(
        FamilyManifest(
            family="qwen3.8",
            variants=[
                VariantSpec(
                    id="q1",
                    provider="llamacpp",
                    name="qwen3.8-27b-q4",
                    repo="unsloth/Qwen3.8-27B-GGUF",
                    files=["Qwen3.8-27B-UD-Q4_K_XL.gguf"],
                ),
            ],
        ),
        family_dir / "qwen3.8.yaml",
    )

    result = migrate(config_path, family_dir, wt_config_path=tmp_path / "no-such-wt-config.toml")

    assert any("wt config not found" in w for w in result.warnings)
    assert result.registry.provider("llamacpp").id == "llamacpp"
    model = result.registry.model("llamacpp/qwen3.8-27b-q4")
    assert model.tags == []
    assert model.fetch.repo == "unsloth/Qwen3.8-27B-GGUF"


def test_migrate_imports_wt_providers_and_models(tmp_path):
    config_path = tmp_path / "config.yaml"
    family_dir = tmp_path / "families"
    family_dir.mkdir()
    _write_modelman_config(config_path)
    wt_config_path = tmp_path / "wt-config.toml"
    _write_wt_config(wt_config_path)

    result = migrate(config_path, family_dir, wt_config_path=wt_config_path)

    model = result.registry.model("ollama/qwen3.8:27b-mlx")
    assert model.tags == ["code", "design"]
    assert model.family == "qwen3.8"
    assert model.location == "local"


def test_migrate_merges_wt_tags_with_modelman_model_info(tmp_path):
    config_path = tmp_path / "config.yaml"
    family_dir = tmp_path / "families"
    family_dir.mkdir()
    _write_modelman_config(config_path)
    wt_config_path = tmp_path / "wt-config.toml"
    _write_wt_config(wt_config_path)
    save_manifest(
        FamilyManifest(
            family="qwen3.8",
            variants=[
                VariantSpec(
                    id="q2",
                    provider="ollama",
                    name="qwen3.8:27b-mlx",
                    model_info={"supports_function_calling": True},
                ),
            ],
        ),
        family_dir / "qwen3.8.yaml",
    )

    result = migrate(config_path, family_dir, wt_config_path=wt_config_path)

    model = result.registry.model("ollama/qwen3.8:27b-mlx")
    assert model.tags == ["code", "design"]  # untouched — came from wt
    assert model.model_info == {"supports_function_calling": True}  # filled in by modelman


def test_migrate_records_downloaded_state_from_modelman_manifest(tmp_path):
    config_path = tmp_path / "config.yaml"
    family_dir = tmp_path / "families"
    family_dir.mkdir()
    _write_modelman_config(config_path)
    manifest = FamilyManifest(
        family="qwen3.8",
        variants=[
            VariantSpec(
                id="q1",
                provider="llamacpp",
                name="qwen3.8-27b-q4",
                repo="unsloth/Qwen3.8-27B-GGUF",
                files=["Qwen3.8-27B-UD-Q4_K_XL.gguf"],
            ),
        ],
    )
    manifest.mark_downloaded("q1", "/models/qwen3.8-27b-q4.gguf")
    save_manifest(manifest, family_dir / "qwen3.8.yaml")

    result = migrate(config_path, family_dir, wt_config_path=tmp_path / "absent.toml")

    state = result.state.get("llamacpp/qwen3.8-27b-q4")
    assert state.downloaded is True
    assert state.disk_path == "/models/qwen3.8-27b-q4.gguf"
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `uv run pytest tests/test_migrate.py -v`
Expected: FAIL with `ModuleNotFoundError: No module named 'modelman.migrate'`

- [ ] **Step 3: Implement `migrate.py`**

```python
"""One-time migration from legacy modelman (config.yaml + families/*.yaml)
and, optionally, agent-worktree's config.toml into the new canonical
registry.toml + modelman.toml. See the "One-time migration" section of
docs/superpowers/specs/2026-08-27-shared-model-registry-design.md.

Collision policy: agent-worktree's config.toml is imported first, so its
curated tags/cost/family/location win for any model that also appears in
a modelman family manifest. modelman's own legacy providers/variants only
add new entries or fill in fields it uniquely owns (fetch, model_info,
download state) — they never overwrite tags/cost/family/location.
"""

from __future__ import annotations

import tomllib
from dataclasses import dataclass, field
from pathlib import Path

import yaml

from .manifest import load_manifest
from .registry import AuthConfig, Fetch, ModelEntry, ProviderEntry, Registry
from .state import ModelState, StateStore


@dataclass
class MigrationResult:
    registry: Registry
    state: StateStore
    warnings: list[str] = field(default_factory=list)


def migrate(
    legacy_config_path: Path,
    legacy_family_dir: Path,
    wt_config_path: Path | None = None,
) -> MigrationResult:
    warnings: list[str] = []
    registry = Registry()
    state = StateStore()

    if wt_config_path is not None and wt_config_path.exists():
        _import_wt_config(wt_config_path, registry)
    else:
        warnings.append(
            f"wt config not found at {wt_config_path}; skipped "
            "(fine for a modelman-only install)"
        )

    _import_modelman_providers(legacy_config_path, registry)
    _import_modelman_families(legacy_family_dir, registry, state)

    return MigrationResult(registry=registry, state=state, warnings=warnings)


def _has_provider(registry: Registry, provider_id: str) -> bool:
    return any(p.id == provider_id for p in registry.providers)


def _find_model(registry: Registry, model_id: str) -> ModelEntry | None:
    for m in registry.models:
        if m.id == model_id:
            return m
    return None


def _import_wt_config(wt_config_path: Path, registry: Registry) -> None:
    with open(wt_config_path, "rb") as f:
        raw = tomllib.load(f)
    for p in raw.get("providers", []):
        if _has_provider(registry, p["id"]):
            continue
        auth_raw = p.get("auth", {})
        registry.providers.append(
            ProviderEntry(
                id=p["id"],
                name=p.get("name", p["id"]),
                location=p.get("location"),
                auth=AuthConfig(
                    type=auth_raw.get("type", "none"),
                    secret_ref=auth_raw.get("secret_ref"),
                    base_url=auth_raw.get("base_url"),
                ),
            )
        )
    for m in raw.get("models", []):
        registry.models.append(
            ModelEntry(
                id=m["id"],
                family=m.get("family", m["id"]),
                provider_id=m["provider_id"],
                model_name=m["model_name"],
                location=m.get("location"),
                source=m.get("source"),
                tags=list(m.get("tags", [])),
            )
        )


def _import_modelman_providers(config_path: Path, registry: Registry) -> None:
    if not config_path.exists():
        return
    with open(config_path) as f:
        raw = yaml.safe_load(f) or {}
    for provider_id, cfg in (raw.get("providers") or {}).items():
        if _has_provider(registry, provider_id):
            continue
        registry.providers.append(
            ProviderEntry(
                id=provider_id,
                name=provider_id.title(),
                location="local",
                model_dir=cfg.get("model_dir"),
                auth=AuthConfig(type="none"),
            )
        )


def _import_modelman_families(family_dir: Path, registry: Registry, state: StateStore) -> None:
    if not family_dir.exists():
        return
    for manifest_path in sorted(family_dir.glob("*.yaml")):
        manifest = load_manifest(manifest_path.stem, family_dir=family_dir)
        for variant in manifest.variants:
            model_id = f"{variant['provider']}/{variant['name']}"
            existing = _find_model(registry, model_id)
            if existing is None:
                existing = ModelEntry(
                    id=model_id,
                    family=manifest.family,
                    provider_id=variant["provider"],
                    model_name=variant["name"],
                )
                registry.models.append(existing)
            if variant.get("model_info"):
                existing.model_info = {**existing.model_info, **variant["model_info"]}
            if variant.get("repo") or variant.get("files") or variant.get("quantizations"):
                existing.fetch = Fetch(
                    repo=variant.get("repo"),
                    files=variant.get("files"),
                    quantizations=variant.get("quantizations"),
                )
            downloaded_info = manifest.downloaded.get(variant["id"])
            if downloaded_info is not None:
                state.set(
                    model_id,
                    ModelState(downloaded=True, disk_path=downloaded_info.get("local_path")),
                )
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `uv run pytest tests/test_migrate.py -v`
Expected: PASS (4 tests)

- [ ] **Step 5: Lint and commit**

```bash
uv run ruff check src/modelman/migrate.py tests/test_migrate.py
git add src/modelman/migrate.py tests/test_migrate.py
git commit -m "feat: add legacy-to-registry migration importer - completes plan item #4"
```

---

### Task 5: `modelman migrate` CLI command

**Files:**
- Modify: `src/modelman/config.py` (rename `_default_config_path` → `default_config_path`, its one internal call site)
- Modify: `src/modelman/main.py` (add the `migrate` command)
- Test: `tests/commands/test_migrate.py`

**Interfaces:**
- Consumes: `migrate`, `MigrationResult` from `modelman.migrate` (Task 4); `save_registry` from `modelman.registry` (Task 2); `save_state` from `modelman.state` (Task 3); `default_config_path` (renamed, this task) from `modelman.config`; the existing `get_family_dir` from `modelman.manifest`.
- Produces: a `modelman migrate` Typer command, invocable via `CliRunner().invoke(app, ["migrate"])` and reachable as `MODELMAN_WT_CONFIG` env var / `--wt-config` option.

- [ ] **Step 1: Rename `_default_config_path` to `default_config_path`**

In `src/modelman/config.py`, rename the function and its one call site:

```python
def default_config_path() -> Path:
    """Compute the config path lazily so env overrides work in tests."""
    return Path(os.environ.get("MODELMAN_CONFIG", "~/.config/local-ai/config.yaml")).expanduser()
```

And in `load_config`, change `_default_config_path()` to `default_config_path()`. (It's being promoted to public because `main.py`'s new `migrate` command needs the same default-path logic as `load_config` — no other behavior changes.)

- [ ] **Step 2: Run the existing config test suite to confirm the rename didn't break anything**

Run: `uv run pytest tests/test_config.py -v`
Expected: PASS (all pre-existing tests, unchanged)

- [ ] **Step 3: Write the failing test**

Create `tests/commands/test_migrate.py`:

```python
"""`modelman migrate` is the one-time CLI entry point for importing legacy
config.yaml + families/*.yaml (and, optionally, agent-worktree's
config.toml) into the new registry.toml + modelman.toml. This covers the
command actually writing both output files and reporting what it
imported — the underlying merge logic is covered by tests/test_migrate.py."""

from typer.testing import CliRunner

from modelman.main import app
from modelman.registry import load_registry


def test_migrate_command_writes_registry_and_reports_counts(tmp_path, monkeypatch):
    config_path = tmp_path / "config.yaml"
    config_path.write_text("providers:\n  ollama:\n    type: ollama\n")
    family_dir = tmp_path / "families"
    family_dir.mkdir()
    registry_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    monkeypatch.setenv("MODELMAN_CONFIG", str(config_path))
    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(family_dir))
    monkeypatch.setenv("MODELMAN_REGISTRY", str(registry_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    monkeypatch.setenv("MODELMAN_WT_CONFIG", str(tmp_path / "no-wt-config.toml"))

    runner = CliRunner()
    result = runner.invoke(app, ["migrate"])

    assert result.exit_code == 0
    assert "1 providers" in result.stdout
    assert "wt config not found" in result.stdout
    assert registry_path.exists()
    assert load_registry(registry_path).provider("ollama").id == "ollama"
```

- [ ] **Step 4: Run test to verify it fails**

Run: `uv run pytest tests/commands/test_migrate.py -v`
Expected: FAIL — `migrate` is not a registered command (typer reports "No such command 'migrate'")

- [ ] **Step 5: Add the `migrate` command to `main.py`**

Replace the full contents of `src/modelman/main.py` with:

```python
"""modelman CLI entry point."""

from __future__ import annotations

from pathlib import Path

import typer

# Import providers package to trigger registration of all providers.
from . import providers  # noqa: F401
from .app import ModelmanApp
from .config import default_config_path
from .manifest import get_family_dir
from .migrate import migrate as run_migration
from .registry import save_registry
from .state import save_state

app = typer.Typer(help="Manage local LLM model families across providers.")


def run_tui(family: str | None) -> None:
    """Launch the Textual TUI, optionally starting at a family's model screen."""
    ModelmanApp(family=family).run()


@app.callback(invoke_without_command=True)
def _main(ctx: typer.Context) -> None:
    """Run `modelman` with no args to open the TUI."""
    if ctx.invoked_subcommand is None:
        run_tui(None)


@app.command()
def download(
    family: str = typer.Argument(..., help="Family name (filename under families dir)"),
):
    """Open the TUI at a family's model screen (queued downloads on exit)."""
    run_tui(family)


@app.command()
def migrate(
    wt_config: str = typer.Option(
        "~/.config/agent-wt/config.toml",
        envvar="MODELMAN_WT_CONFIG",
        help="Path to agent-worktree's config.toml to import from (skipped if missing)",
    ),
) -> None:
    """One-time import of legacy config.yaml + families/*.yaml (and,
    optionally, agent-worktree's config.toml) into registry.toml +
    modelman.toml."""
    result = run_migration(
        default_config_path(),
        get_family_dir(),
        wt_config_path=Path(wt_config).expanduser(),
    )

    save_registry(result.registry)
    save_state(result.state)

    for warning in result.warnings:
        typer.echo(f"warning: {warning}")
    typer.echo(
        f"Migrated {len(result.registry.providers)} providers and "
        f"{len(result.registry.models)} models."
    )


if __name__ == "__main__":
    app()
```

- [ ] **Step 6: Run test to verify it passes**

Run: `uv run pytest tests/commands/test_migrate.py -v`
Expected: PASS

- [ ] **Step 7: Run the full test suite**

Run: `uv run pytest`
Expected: PASS (all tests, including the pre-existing suite untouched by this plan)

- [ ] **Step 8: Lint, typecheck, and commit**

```bash
uv run ruff check .
uv run mypy src/modelman
git add src/modelman/config.py src/modelman/main.py tests/commands/test_migrate.py
git commit -m "feat: add modelman migrate CLI command - completes plan item #5"
```

---

## What's Next (not this plan)

Once this phase is merged, `registry.toml`/`modelman.toml` exist and are fully populated from both legacy sources, but nothing reads or writes them except `modelman migrate` itself — the TUI still uses the old `config.py`/`manifest.py` path, and agent-worktree is completely unaffected. Follow-up work, each its own future plan:

1. Wire modelman's TUI (families/models screens, queue) to read/write `registry.toml`/`modelman.toml` instead of `config.yaml`/`families/*.yaml`.
2. Provider-sync capability (absorbing wt's `internal/registry` live discovery + `wt config ollama`) — ollama/openrouter/llamacpp/omlx discovery writing new `source = "discovered"` rows.
3. LiteLLM `model_list` management (add/remove entries keyed by registry model id, preserving `general_settings`).
4. agent-worktree switches to read `registry.toml` read-only, drops its own `internal/registry` package and `wt config ollama`, slims `config.toml` to just Agents + prefs.
