# modelman start provider-backed discovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `modelman start` gains a live, provider-backed view of local models (registered+downloaded / registered-not-downloaded / discovered-but-unregistered) and can auto-register + expose + start a model found only through a provider's own tooling (`ollama list`, `~/.omlx/models`, `~/.mtplx/models`), without the user hand-editing `registry.toml` first.

**Architecture:** All new logic lives in `modelman/src/modelman/local_control.py` (already owns `modelman start`/`stop` orchestration) and is wired into the existing `start` Typer command in `main.py`. No new modules, no TUI changes — once a discovered model is written to `registry.toml` the existing TUI reads it like any other model.

**Tech Stack:** Python 3.13, Typer CLI, existing `Provider.list_local()` on `OllamaProvider`/`OMLXProvider`/`MTPLXProvider`, `pytest` + `typer.testing.CliRunner`.

**Spec:** `docs/superpowers/specs/2026-09-13-modelman-start-provider-discovery-design.md`

## Global Constraints

- In-scope providers for discovery/auto-register: `ollama`, `omlx`, `omlx-6bit`, `mtplx` only. `mlx_lm_server` and `llamacpp` are excluded (target+draft pairing and retired, respectively — see spec Non-goals).
- A discovered model, once auto-registered, gets `source="discovered"`, `location="local"`, `state.ready=True`, and is **exposed automatically** (`exposed=True` + a LiteLLM `model_list` entry) — per the approved design, starting a discovered model implies you want to use it.
- The family for an auto-registered model is **always collected via an interactive prompt** — never defaulted or guessed.
- `id = f"{provider_id}/{variant_id.replace('/', '--')}"` — the same scheme `screens/forms.py`'s `parse_model()` already uses for TUI-added models.
- No behavior change for any `start <model_id>` call where `model_id` already resolves via `registry.model()` — the existing 10+ tests in `tests/test_local_control.py` covering that path must keep passing unmodified.
- Full test suite command: `uv run pytest -q` (run from `modelman/`). Lint/typecheck: `make check`.

---

### Task 1: Live local-model inventory (`inventory_local_models`)

**Files:**
- Modify: `modelman/src/modelman/local_control.py`
- Test: `modelman/tests/test_local_control.py`

**Interfaces:**
- Consumes: `Provider.list_local() -> list[LocalModel]` (`modelman/src/modelman/providers/base.py`, `LocalModel = {"variant_id": str, "path": str, "size_bytes": int | None}`, already implemented on `OllamaProvider`/`OMLXProvider`/`MTPLXProvider`); `ProviderRegistry.get_class`/`ProviderRegistry.get` (`modelman/src/modelman/providers/registry.py`); `provider_config()` and `model_has_local_artifact()` (`modelman/src/modelman/registry.py`); `_probe_running()`/`base_origin()` (already in `local_control.py`).
- Produces: `DiscoveredModel(provider_id: str, variant_id: str, path: str, size_bytes: int | None)`, `InventoryEntry(model_id: str, running: bool, size_bytes: int | None)`, `LocalModelInventory(downloaded: list[InventoryEntry], not_downloaded: list[str], discovered: list[DiscoveredModel])`, `inventory_local_models(registry: Registry, state: StateStore) -> LocalModelInventory`, plus the private helpers `_provider_local_models(registry) -> dict[tuple[str, str], LocalModel]` and `_registered_pairs(registry) -> set[tuple[str, str]]` — both reused by Task 2.

This task **removes** `list_local_exposed_models` and its five dedicated tests: `inventory_local_models`'s `downloaded` bucket is a strict superset of what it computed (same running-probe logic, minus the exposed-only filter), and nothing outside `main.py`'s `start` command (updated in Task 3) called it.

- [ ] **Step 1: Write the failing tests**

Replace the `list_local_exposed_models` import and its five tests (`test_list_local_exposed_models_filters_to_local_and_exposed` through `test_list_local_exposed_models_sorted_by_id`, currently lines 404-452 of `tests/test_local_control.py`) with tests for `inventory_local_models`. Keep `_listing_registry`/`_listing_state` (lines 361-401) but extend `_listing_registry` with a third, not-on-disk local model and rename nothing else:

```python
def _listing_registry() -> Registry:
    """Local models covering all three inventory buckets, plus one exposed
    cloud model, for inventory_local_models tests."""
    return Registry(
        providers=[
            ProviderEntry(id="ollama", name="Ollama", location="local", auth=AuthConfig(type="none")),
            ProviderEntry(
                id="openrouter", name="OpenRouter", location="cloud", auth=AuthConfig(type="api_key")
            ),
        ],
        models=[
            ModelEntry(
                id="ollama/exposed-model",
                family="qwen3.8",
                provider_id="ollama",
                model_name="exposed-model",
            ),
            ModelEntry(
                id="ollama/unexposed-model",
                family="qwen3.8",
                provider_id="ollama",
                model_name="unexposed-model",
            ),
            ModelEntry(
                id="ollama/missing-model",
                family="qwen3.8",
                provider_id="ollama",
                model_name="missing-model",
            ),
            ModelEntry(
                id="openrouter/z-ai/glm-5.3-flash",
                family="glm",
                provider_id="openrouter",
                model_name="z-ai/glm-5.3-flash",
                location="cloud",
            ),
        ],
    )


def _listing_state(running_model: str | None = None) -> StateStore:
    store = StateStore()
    store.local.running_model = running_model
    store.set("ollama/exposed-model", ModelState(ready=True, exposed=True))
    store.set("ollama/unexposed-model", ModelState(ready=True, exposed=False))
    store.set("openrouter/z-ai/glm-5.3-flash", ModelState(ready=False, exposed=True))
    return store


def _patch_provider_local_models(mapping: dict[str, list[dict]]):
    """Patch ProviderRegistry so _provider_local_models(registry) sees
    `mapping` (provider_id -> list of LocalModel dicts) without shelling
    out to any real provider CLI or scanning a real directory."""

    def get_class(name):
        return object if name in mapping else None

    def get(name, config):
        stub = MagicMock()
        stub.list_local.return_value = mapping.get(name, [])
        return stub

    return patch.multiple(
        "modelman.local_control.ProviderRegistry",
        get_class=MagicMock(side_effect=get_class),
        get=MagicMock(side_effect=get),
    )


def test_inventory_downloaded_bucket_includes_size_and_running_marker():
    registry = _listing_registry()
    state = _listing_state(running_model="ollama/exposed-model")
    mapping = {
        "ollama": [
            {"variant_id": "exposed-model", "path": "ollama:exposed-model", "size_bytes": 4_900_000_000},
        ]
    }
    with _patch_provider_local_models(mapping), patch(
        "modelman.local_control._probe_running", return_value=True
    ):
        inventory = inventory_local_models(registry, state)
    assert inventory.downloaded == [
        InventoryEntry(model_id="ollama/exposed-model", running=True, size_bytes=4_900_000_000)
    ]


def test_inventory_not_downloaded_bucket_lists_registered_missing_artifacts():
    registry = _listing_registry()
    state = _listing_state()
    with _patch_provider_local_models({"ollama": []}):
        inventory = inventory_local_models(registry, state)
    assert inventory.not_downloaded == [
        "ollama/exposed-model",
        "ollama/missing-model",
        "ollama/unexposed-model",
    ]


def test_inventory_discovered_bucket_excludes_already_registered():
    registry = _listing_registry()
    state = _listing_state()
    mapping = {
        "ollama": [
            {"variant_id": "exposed-model", "path": "ollama:exposed-model", "size_bytes": 1},
            {"variant_id": "brand-new-model", "path": "ollama:brand-new-model", "size_bytes": 2_000_000_000},
        ]
    }
    with _patch_provider_local_models(mapping):
        inventory = inventory_local_models(registry, state)
    assert inventory.discovered == [
        DiscoveredModel(
            provider_id="ollama", variant_id="brand-new-model",
            path="ollama:brand-new-model", size_bytes=2_000_000_000,
        )
    ]


def test_inventory_tolerates_a_provider_list_local_failure():
    # A down ollama daemon or a provider whose list_local() raises must not
    # blank out the whole inventory — it just contributes nothing.
    registry = _listing_registry()
    state = _listing_state()

    def get(name, config):
        stub = MagicMock()
        stub.list_local.side_effect = RuntimeError("daemon unreachable")
        return stub

    with patch.multiple(
        "modelman.local_control.ProviderRegistry",
        get_class=MagicMock(return_value=object),
        get=MagicMock(side_effect=get),
    ):
        inventory = inventory_local_models(registry, state)
    assert inventory.discovered == []
    assert inventory.not_downloaded == [
        "ollama/exposed-model", "ollama/missing-model", "ollama/unexposed-model"
    ]


def test_inventory_skips_providers_with_no_registered_class():
    # A registry.toml entry for a provider id nothing registers under
    # ProviderRegistry (e.g. a hand-edited "omlx-6bit" row today) must be
    # skipped, not raise KeyError.
    registry = _listing_registry()
    registry.providers.append(
        ProviderEntry(id="omlx-6bit", name="oMLX 6-bit", location="local", auth=AuthConfig(type="none"))
    )
    state = _listing_state()
    with _patch_provider_local_models({"ollama": []}):
        inventory = inventory_local_models(registry, state)
    assert inventory.discovered == []
```

Add `from unittest.mock import MagicMock, patch` to the existing `from unittest.mock import patch` import line, and `from modelman.local_control import DiscoveredModel, InventoryEntry, inventory_local_models` alongside the existing `local_control` import (removing `list_local_exposed_models` from it).

- [ ] **Step 2: Run tests to verify they fail**

Run: `uv run pytest tests/test_local_control.py -k inventory -v`
Expected: FAIL with `ImportError: cannot import name 'inventory_local_models'` (or `DiscoveredModel`/`InventoryEntry`).

- [ ] **Step 3: Implement `inventory_local_models` and remove `list_local_exposed_models`**

In `modelman/src/modelman/local_control.py`:

1. Expand the import block (current lines 25-43) — add the providers-registration import and `ProviderRegistry`/`provider_config`:

```python
from __future__ import annotations

import subprocess
from dataclasses import dataclass, field
from pathlib import Path

# Import the providers package to ensure ProviderRegistry is populated —
# mirrors sync.py's identical defensive import.
from . import providers  # noqa: F401
from .benchmark.errors import BenchmarkError
from .benchmark.isolation import (
    SUPPORTED_PROVIDER_IDS,
    isolate_provider,
    mlx_lm_server_pairing_args,
    stop_all_local_providers,
)
from .litellm import ExposeError, default_litellm_config_path, expose_model, is_effectively_exposed
from .local_process import ENV_VAR_BY_PROVIDER as _ENV_VAR_BY_PROVIDER
from .local_process import http_models_ids as _http_models_ids
from .providers.mtplx import MTPLX_BASE
from .providers.registry import ProviderRegistry
from .registry import (
    LOCATION_LOCAL,
    ModelEntry,
    Registry,
    base_origin,
    known_families,
    locked_registry,
    model_has_local_artifact,
    provider_config,
)
from .state import ModelState, StateStore, load_state, locked_state
```

   (`ExposeError`/`expose_model`/`default_litellm_config_path`/`ModelEntry`/`locked_registry`/`known_families`/`LOCATION_LOCAL`/`ModelState` are unused until Task 2 — importing them together now avoids re-touching this block twice; `ruff` won't flag them since Task 2 lands in the same PR before any lint run.)

2. Add the providers in scope for discovery, right after `_default_runner` (current line 57):

```python
# Providers modelman can both list a live on-disk catalog for and start via
# bin/llm-isolate-provider. mlx_lm_server is a target+draft pairing chosen at
# start time (not a single downloaded artifact) and llamacpp is retired —
# neither maps onto "discover one artifact, register it" (see the design's
# Non-goals).
DISCOVERY_PROVIDER_IDS: frozenset[str] = frozenset({"ollama", "omlx", "omlx-6bit", "mtplx"})
```

3. Add `DiscoveredModel`/`InventoryEntry`/`LocalModelInventory` dataclasses right after `LocalModelStatus` (current lines 77-80):

```python
@dataclass
class DiscoveredModel:
    """An artifact a provider reports on disk with no matching registry.toml
    entry (no ModelEntry sharing its (provider_id, model_name))."""

    provider_id: str
    variant_id: str
    path: str
    size_bytes: int | None


@dataclass
class InventoryEntry:
    model_id: str
    running: bool
    size_bytes: int | None


@dataclass
class LocalModelInventory:
    downloaded: list[InventoryEntry]
    not_downloaded: list[str]
    discovered: list[DiscoveredModel]
```

4. Replace `list_local_exposed_models` (current lines 155-181) with:

```python
def _provider_local_models(registry: Registry) -> dict[tuple[str, str], dict]:
    """(provider_id, variant_id) -> LocalModel for every artifact every
    in-scope, registered, live provider currently reports on disk.

    Tolerant by design: a provider id with no registered Provider class
    (e.g. a hand-edited "omlx-6bit" registry.toml row today — see
    DISCOVERY_PROVIDER_IDS's docstring) or whose list_local() raises
    contributes nothing rather than failing the whole call — this backs
    both the `start` listing and the start-by-native-name fallback, and
    neither should go blind because one provider is unreachable.
    """
    found: dict[tuple[str, str], dict] = {}
    for entry in registry.providers:
        if entry.id not in DISCOVERY_PROVIDER_IDS:
            continue
        if ProviderRegistry.get_class(entry.id) is None:
            continue
        provider = ProviderRegistry.get(entry.id, provider_config(entry))
        try:
            local_models = provider.list_local()
        except Exception:
            local_models = []
        for local_model in local_models:
            found[(entry.id, local_model["variant_id"])] = local_model
    return found


def _registered_pairs(registry: Registry) -> set[tuple[str, str]]:
    return {(m.provider_id, m.model_name) for m in registry.models}


def inventory_local_models(registry: Registry, state: StateStore) -> LocalModelInventory:
    """Live, three-way view of local models: registered models split into
    downloaded/not-downloaded by cross-referencing each in-scope provider's
    list_local() against registry.toml, plus on-disk artifacts with no
    registry.toml entry at all ("discovered").

    `modelman start` (no model_id) prints this. Unlike the exposed-only
    listing this replaces, `downloaded`/`not_downloaded` include every
    local-artifact registered model regardless of its exposed flag — this
    is a full local inventory, not just "what can I start right now".
    """
    local_map = _provider_local_models(registry)
    registered_pairs = _registered_pairs(registry)
    running_marker = state.local.running_model

    local_models = [
        model
        for model in registry.models
        if model_has_local_artifact(
            model, next((p for p in registry.providers if p.id == model.provider_id), None)
        )
    ]

    downloaded: list[InventoryEntry] = []
    not_downloaded: list[str] = []
    for model in sorted(local_models, key=lambda m: m.id):
        hit = local_map.get((model.provider_id, model.model_name))
        if hit is None:
            not_downloaded.append(model.id)
            continue
        running = False
        if model.id == running_marker:
            provider = next((p for p in registry.providers if p.id == model.provider_id), None)
            probe_origin = base_origin(provider.auth.base_url) if provider and provider.auth else None
            running = _probe_running(model.provider_id, model.model_name, probe_origin)
        downloaded.append(
            InventoryEntry(model_id=model.id, running=running, size_bytes=hit.get("size_bytes"))
        )

    discovered = [
        DiscoveredModel(
            provider_id=provider_id,
            variant_id=variant_id,
            path=local_model["path"],
            size_bytes=local_model.get("size_bytes"),
        )
        for (provider_id, variant_id), local_model in sorted(local_map.items())
        if (provider_id, variant_id) not in registered_pairs
    ]
    return LocalModelInventory(downloaded=downloaded, not_downloaded=not_downloaded, discovered=discovered)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `uv run pytest tests/test_local_control.py -v`
Expected: PASS — the new `inventory_*`/`DISCOVERY_PROVIDER_IDS` tests pass, and every pre-existing test in the file (start/stop orchestration) still passes unmodified.

- [ ] **Step 5: Commit**

```bash
git add modelman/src/modelman/local_control.py modelman/tests/test_local_control.py
git commit -m "feat(modelman): replace exposed-only start listing with a live provider inventory

Adds inventory_local_models(), cross-referencing each in-scope local
provider's list_local() against registry.toml to produce three buckets
(downloaded, not-downloaded, discovered-but-unregistered) instead of the
old state-only, exposed-only list_local_exposed_models().

Completes plan item docs/superpowers/plans/2026-09-13-modelman-start-provider-discovery.md#task-1"
```

---

### Task 2: Auto-register + expose a discovered model on `start <name>`

**Files:**
- Modify: `modelman/src/modelman/local_control.py`
- Test: `modelman/tests/test_local_control.py`

**Interfaces:**
- Consumes: `_provider_local_models`, `_registered_pairs`, `DiscoveredModel` (Task 1); `expose_model(registry, state, model_id, litellm_path) -> list[str]` and `ExposeError` (`modelman/src/modelman/litellm.py`); `locked_registry(path) -> Generator[Registry]` and `known_families(registry, state) -> list[str]` (`modelman/src/modelman/registry.py`); `locked_state`/`ModelState` (`modelman/src/modelman/state.py`, already used).
- Produces: `DiscoveredModelNeedsFamily` (exception, carries `provider_id`/`variant_id`/`suggested_families`), `StartResult.warnings: list[str]` (new field), and `start_local_model(registry, model_id, state_path=None, *, family=None, litellm_path=None, registry_path=None) -> StartResult` — same call signature for every existing positional caller (`state_path` stays the third positional arg), three new keyword-only params.

- [ ] **Step 1: Write the failing tests**

Add to `tests/test_local_control.py` (after the mlx_lm_server/mtplx tests, before `_listing_registry`):

```python
def test_start_unregistered_name_with_no_family_raises_needs_family(tmp_path):
    registry = _registry()
    registry_path = tmp_path / "registry.toml"
    save_registry(registry, registry_path)
    mapping = {"ollama": [{"variant_id": "llama3.2:3b", "path": "ollama:llama3.2:3b", "size_bytes": 2_000_000_000}]}
    with _patch_provider_local_models(mapping):
        with pytest.raises(DiscoveredModelNeedsFamily) as excinfo:
            start_local_model(registry, "llama3.2:3b", registry_path=registry_path)
    assert excinfo.value.provider_id == "ollama"
    assert excinfo.value.variant_id == "llama3.2:3b"
    # No side effects: nothing is written and nothing is started when the
    # call can't proceed without a family.
    assert load_registry(registry_path).models == registry.models


def test_start_unregistered_name_with_family_registers_exposes_and_starts(tmp_path):
    registry = _registry()
    registry_path = tmp_path / "registry.toml"
    save_registry(registry, registry_path)
    state_path = _state_path(tmp_path)
    litellm_path = tmp_path / "config.yaml"
    litellm_path.write_text("model_list: []\n")
    mapping = {"ollama": [{"variant_id": "llama3.2:3b", "path": "ollama:llama3.2:3b", "size_bytes": 2_000_000_000}]}

    with (
        _patch_provider_local_models(mapping),
        patch("modelman.local_control.stop_all_local_providers") as mock_stop,
        patch("modelman.local_control.isolate_provider") as mock_isolate,
    ):
        mock_isolate.return_value = IsolateResult(
            provider="ollama", model="llama3.2:3b",
            direct_url="http://localhost:11434/v1/chat/completions", ok=True, error=None,
        )
        result = start_local_model(
            registry, "llama3.2:3b", state_path,
            family="discovered", registry_path=registry_path, litellm_path=litellm_path,
        )

    mock_stop.assert_called_once()
    assert result.already_running is False
    assert result.model_id == "ollama/llama3.2:3b"

    on_disk = load_registry(registry_path)
    entry = on_disk.model("ollama/llama3.2:3b")
    assert entry.family == "discovered"
    assert entry.provider_id == "ollama"
    assert entry.model_name == "llama3.2:3b"
    assert entry.source == "discovered"
    assert entry.location == "local"
    # The in-memory registry the caller passed in must reflect the write too
    # (start_local_model keeps using it for the rest of this call, and a CLI
    # process only has this one in-memory copy for the whole invocation).
    assert registry.model("ollama/llama3.2:3b") is entry

    state_on_disk = load_state(state_path)
    model_state = state_on_disk.get("ollama/llama3.2:3b")
    assert model_state.ready is True
    assert model_state.exposed is True
    assert model_state.size_bytes == 2_000_000_000
    assert load_state(state_path).local.running_model == "ollama/llama3.2:3b"


def test_start_unregistered_name_ambiguous_across_providers_raises(tmp_path):
    registry = _registry()
    registry.providers.append(
        ProviderEntry(id="mtplx", name="MTPLX", location="local", auth=AuthConfig(type="none"))
    )
    registry_path = tmp_path / "registry.toml"
    save_registry(registry, registry_path)
    mapping = {
        "ollama": [{"variant_id": "shared-name", "path": "ollama:shared-name", "size_bytes": None}],
        "mtplx": [{"variant_id": "shared-name", "path": "/mtplx/shared-name", "size_bytes": None}],
    }
    with _patch_provider_local_models(mapping):
        with pytest.raises(LocalControlError, match="multiple providers"):
            start_local_model(registry, "shared-name", registry_path=registry_path)


def test_start_native_name_resolves_existing_registered_model_without_reregistering(tmp_path):
    # A model already registered (auto or curated) under its native
    # provider-side name must resolve idempotently by that name too - a
    # user who auto-registered "llama3.2:3b" and starts it again by the
    # same bare name must not see "unknown model" just because the id it
    # was registered under is "ollama/llama3.2:3b", not the bare name.
    registry = _registry()
    registry.models.append(
        ModelEntry(
            id="ollama/llama3.2:3b", family="discovered", provider_id="ollama",
            model_name="llama3.2:3b", location="local", source="discovered",
        )
    )
    registry_path = tmp_path / "registry.toml"
    save_registry(registry, registry_path)
    state_path = _state_path(tmp_path)

    with (
        patch("modelman.local_control.stop_all_local_providers"),
        patch("modelman.local_control.isolate_provider") as mock_isolate,
    ):
        mock_isolate.return_value = IsolateResult(
            provider="ollama", model="llama3.2:3b",
            direct_url="http://localhost:11434/v1/chat/completions", ok=True, error=None,
        )
        result = start_local_model(registry, "llama3.2:3b", state_path, registry_path=registry_path)
    assert result.model_id == "ollama/llama3.2:3b"
    mock_isolate.assert_called_once_with("ollama", env={"LLM_ISOLATE_OLLAMA_MODEL": "llama3.2:3b"})
```

Add the new imports these tests need at the top of `tests/test_local_control.py`:

```python
from modelman.local_control import (
    DiscoveredModelNeedsFamily,
    LocalControlError,
    LocalModelStatus,
    _name_matches,
    start_local_model,
    stop_local_model,
)
from modelman.registry import AuthConfig, ModelEntry, ProviderEntry, Registry, load_registry, save_registry
```

(merge with the existing `local_control`/`registry` import lines rather than duplicating them).

- [ ] **Step 2: Run tests to verify they fail**

Run: `uv run pytest tests/test_local_control.py -k "unregistered_name or native_name" -v`
Expected: FAIL with `ImportError: cannot import name 'DiscoveredModelNeedsFamily'`.

- [ ] **Step 3: Implement the auto-register-on-start flow**

In `modelman/src/modelman/local_control.py`:

1. Add the exception near `LocalControlError` (current lines 60-61):

```python
class LocalControlError(Exception):
    """Raised for user-facing `modelman start`/`modelman stop` failures."""


class DiscoveredModelNeedsFamily(LocalControlError):
    """Raised by start_local_model() when `model_id` matches an on-disk,
    unregistered artifact but no `family` was supplied. The CLI catches
    this, prompts the user, and retries with `family` set — nothing is
    written to registry.toml/modelman.toml/LiteLLM before this is raised.
    """

    def __init__(self, provider_id: str, variant_id: str, suggested_families: list[str]):
        self.provider_id = provider_id
        self.variant_id = variant_id
        self.suggested_families = suggested_families
        super().__init__(
            f"{provider_id}/{variant_id} is on disk but not registered — a family is required"
        )
```

2. Add `warnings` to `StartResult` (current lines 64-68):

```python
@dataclass
class StartResult:
    model_id: str
    already_running: bool
    direct_url: str | None = None
    warnings: list[str] = field(default_factory=list)
```

3. Add the discovery-matching and registration helpers right after `_registered_pairs` (Task 1):

```python
def _find_discovered(registry: Registry, name: str) -> list[DiscoveredModel]:
    """Unregistered on-disk artifacts, across every in-scope provider,
    whose native name is exactly `name`."""
    local_map = _provider_local_models(registry)
    registered_pairs = _registered_pairs(registry)
    return [
        DiscoveredModel(
            provider_id=provider_id, variant_id=variant_id,
            path=local_model["path"], size_bytes=local_model.get("size_bytes"),
        )
        for (provider_id, variant_id), local_model in local_map.items()
        if variant_id == name and (provider_id, variant_id) not in registered_pairs
    ]


def _register_discovered_model(
    registry: Registry,
    state: StateStore,
    match: DiscoveredModel,
    family: str,
    registry_path: Path | None,
    state_path: Path | None,
    litellm_path: Path | None,
) -> tuple[ModelEntry, list[str]]:
    """Write a new registry.toml entry for `match`, mark it ready+exposed
    in modelman.toml, and add its LiteLLM model_list row. Mutates `registry`
    and `state` in place (the caller's in-memory copies) so the rest of
    start_local_model's flow sees the new model immediately.
    """
    model_id = f"{match.provider_id}/{match.variant_id.replace('/', '--')}"
    entry = ModelEntry(
        id=model_id,
        family=family,
        provider_id=match.provider_id,
        model_name=match.variant_id,
        location=LOCATION_LOCAL,
        source="discovered",
    )
    with locked_registry(registry_path) as fresh:
        fresh.models.append(entry)
    registry.models.append(entry)

    model_state = ModelState(ready=True, disk_path=match.path, size_bytes=match.size_bytes)
    with locked_state(state_path) as fresh_state:
        fresh_state.set(model_id, model_state)
    state.set(model_id, model_state)

    try:
        warnings = expose_model(registry, state, model_id, litellm_path or default_litellm_config_path())
    except ExposeError as exc:
        raise LocalControlError(f"discovered model {model_id} could not be exposed: {exc}") from exc
    # expose_model() mutates state.models[model_id] in place (sets exposed)
    # but never persists it - merge just this one key back, mirroring
    # main.py's `expose` command.
    with locked_state(state_path) as fresh_state:
        fresh_state.models[model_id] = state.models[model_id]
    return entry, warnings


def _resolve_or_register(
    registry: Registry,
    state: StateStore,
    model_id: str,
    family: str | None,
    registry_path: Path | None,
    state_path: Path | None,
    litellm_path: Path | None,
) -> tuple[ModelEntry, list[str]]:
    """Resolve `model_id` to a ModelEntry, trying — in order — a registry
    id, an existing model's native provider-side name (so a discovered
    model that was auto-registered under `<provider>/<name>` still
    resolves when the user re-types its bare native name), and finally an
    on-disk-but-unregistered artifact (which requires `family` and
    registers it). Raises LocalControlError('unknown model: ...') if none
    match, or DiscoveredModelNeedsFamily if the third case needs a family.
    """
    try:
        return registry.model(model_id), []
    except KeyError:
        pass

    native_matches = [
        m
        for m in registry.models
        if m.model_name == model_id
        and model_has_local_artifact(
            m, next((p for p in registry.providers if p.id == m.provider_id), None)
        )
    ]
    if len(native_matches) == 1:
        return native_matches[0], []
    if len(native_matches) > 1:
        ids = ", ".join(sorted(m.id for m in native_matches))
        raise LocalControlError(f"{model_id!r} matches multiple registered models ({ids}) — use the full model id")

    discovered = _find_discovered(registry, model_id)
    if not discovered:
        raise LocalControlError(f"unknown model: {model_id}")
    if len(discovered) > 1:
        providers = ", ".join(sorted(m.provider_id for m in discovered))
        raise LocalControlError(
            f"{model_id!r} matches on-disk models from multiple providers ({providers}) — "
            "register one manually to disambiguate"
        )
    if family is None:
        match = discovered[0]
        raise DiscoveredModelNeedsFamily(match.provider_id, match.variant_id, known_families(registry, state))
    return _register_discovered_model(
        registry, state, discovered[0], family, registry_path, state_path, litellm_path
    )
```

4. Replace the top of `start_local_model` (current lines 184-208) — signature and the initial lookup/validation — with:

```python
def start_local_model(
    registry: Registry,
    model_id: str,
    state_path: Path | None = None,
    *,
    family: str | None = None,
    registry_path: Path | None = None,
    litellm_path: Path | None = None,
) -> StartResult:
    """Stop whatever local model is running (if any) and start model_id,
    recording it as the new `[local].running_model` marker.

    `model_id` may be a registry id, an existing model's native
    provider-side name, or (with `family` set) the native name of an
    on-disk artifact with no registry.toml entry yet — see
    _resolve_or_register. Raises DiscoveredModelNeedsFamily when the third
    case needs a family the caller hasn't supplied yet.

    Raises LocalControlError when model_id is unknown, not a local model,
    its provider cannot be isolated by bin/llm-isolate-provider, or the
    mlx_lm_server pairing can't be resolved.

    Idempotent: when model_id is already the running marker, the marked
    model is PROBED before trusting the marker — `modelman start` is the
    recovery command wt's own "not running" message prescribes, so it must
    not no-op on a marker whose process died (crash, reboot, omlx stop). A
    dead marker is cleared and the full start runs. A false probe negative
    only costs a stop+reload of an already-serving model.
    """
    state = load_state(state_path)
    model, registration_warnings = _resolve_or_register(
        registry, state, model_id, family, registry_path, state_path, litellm_path
    )
    resolved_id = model.id

    provider = next((p for p in registry.providers if p.id == model.provider_id), None)
    if not model_has_local_artifact(model, provider):
        raise LocalControlError(f"{resolved_id} is a cloud model — modelman start only runs local models")

    if model.provider_id not in SUPPORTED_PROVIDER_IDS:
        raise LocalControlError(
            f"provider {model.provider_id!r} cannot be started/stopped by modelman "
            f"(supported: {sorted(SUPPORTED_PROVIDER_IDS)})"
        )
```

   Every reference to `model_id` further down in the function (the rest of `start_local_model` is unchanged) must now use `resolved_id` instead, since the argument the user typed (a bare native name) is no longer necessarily the registry id. Update the remaining body (current lines 216-278) by replacing each bare `model_id` with `resolved_id`:

```python
    # Resolve the isolate arguments BEFORE any teardown so a broken mlx_lm_server
    # pairing fails fast — stopping the running model first would tear down a
    # healthy model and then leave the GPU empty when this raises.
    extra_args: tuple[str, ...] = ()
    env: dict[str, str] | None = None
    if model.provider_id == "mlx_lm_server":
        try:
            target, draft = mlx_lm_server_pairing_args(
                model.id,
                model.fetch.local_path if model.fetch else None,
                model.fetch.repo if model.fetch else None,
                model.draft.local_path if model.draft else None,
                model.draft.repo if model.draft else None,
            )
        except BenchmarkError as exc:
            raise LocalControlError(str(exc)) from exc
        extra_args = (target, draft)
    elif model.provider_id == "mtplx":
        extra_args = (model.model_name,)
        env = None
    else:
        env = {_ENV_VAR_BY_PROVIDER[model.provider_id]: model.model_name}

    probe_origin = base_origin(provider.auth.base_url) if provider and provider.auth else None
    current = load_state(state_path).local.running_model
    if current == resolved_id:
        if _probe_running(model.provider_id, model.model_name, probe_origin):
            return StartResult(model_id=resolved_id, already_running=True, warnings=registration_warnings)
        _clear_stale_marker((resolved_id,), state_path)

    try:
        stop_all_local_providers()
    except BenchmarkError as exc:
        raise LocalControlError(f"failed to stop the currently-running local model: {exc}") from exc

    try:
        result = isolate_provider(model.provider_id, *extra_args, env=env)
    except BenchmarkError as exc:
        _clear_stale_marker((current or "", resolved_id), state_path)
        raise LocalControlError(
            f"failed to start {resolved_id}: {exc} — cleared the stale "
            "[local].running_model marker (the previously running model was "
            "already stopped)"
        ) from exc
    if not result.ok:
        _clear_stale_marker((current or "", resolved_id), state_path)
        raise LocalControlError(
            f"failed to start {resolved_id}: {result.error or 'unknown error'} — "
            "cleared the stale [local].running_model marker (the previously "
            "running model was already stopped)"
        )

    with locked_state(state_path) as fresh:
        fresh.local.running_model = resolved_id
    return StartResult(
        model_id=resolved_id,
        already_running=False,
        direct_url=result.direct_url or None,
        warnings=registration_warnings,
    )
```

   Note `state = load_state(state_path)` now runs once at the top (needed by `_resolve_or_register` for `known_families`); the old code loaded it again later as `current = load_state(state_path).local.running_model` — that second load is kept as-is (it must re-read after `_register_discovered_model` may have written a fresh state file), just re-pointed at `resolved_id`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `uv run pytest tests/test_local_control.py -v`
Expected: PASS — every test in the file, old and new.

- [ ] **Step 5: Commit**

```bash
git add modelman/src/modelman/local_control.py modelman/tests/test_local_control.py
git commit -m "feat(modelman): auto-register+expose a discovered model on start <name>

start_local_model() now resolves model_id against the registry id, an
existing model's native provider name, and finally an on-disk-but-
unregistered artifact (via the new DISCOVERY_PROVIDER_IDS/list_local()
scan) — the last case requires a family and registers+exposes the model
before starting it, raising DiscoveredModelNeedsFamily when family is
still unset so the CLI can prompt for one.

Completes plan item docs/superpowers/plans/2026-09-13-modelman-start-provider-discovery.md#task-2"
```

---

### Task 3: Wire the new listing and family prompt into `modelman start`

**Files:**
- Modify: `modelman/src/modelman/main.py`
- Test: `modelman/tests/commands/test_local_control.py`

**Interfaces:**
- Consumes: `inventory_local_models`, `DiscoveredModelNeedsFamily`, `StartResult.warnings` (Task 1/2).
- Produces: the updated `start` command's stdout format (three sections) and its family-prompt retry loop — no new importable symbols.

- [ ] **Step 1: Write the failing tests**

Add to `tests/commands/test_local_control.py`:

```python
def test_start_command_no_args_shows_three_sections(tmp_path, monkeypatch):
    registry_path = tmp_path / "registry.toml"
    registry_path.write_text(
        '[[providers]]\nid = "ollama"\nname = "Ollama"\nlocation = "local"\n'
        'auth = { type = "none" }\n\n'
        '[[models]]\nid = "ollama/x"\nfamily = "x"\nprovider_id = "ollama"\nmodel_name = "x"\n\n'
        '[[models]]\nid = "ollama/y"\nfamily = "y"\nprovider_id = "ollama"\nmodel_name = "y"\n'
    )
    state_path = tmp_path / "modelman.toml"
    state_path.write_text(
        '[model_state."ollama/x"]\nready = true\nexposed = true\n'
    )
    monkeypatch.setenv("MODELMAN_REGISTRY", str(registry_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))

    from unittest.mock import MagicMock

    def get_class(name):
        return object if name == "ollama" else None

    def get(name, config):
        stub = MagicMock()
        stub.list_local.return_value = [
            {"variant_id": "x", "path": "ollama:x", "size_bytes": 4_900_000_000},
            {"variant_id": "z", "path": "ollama:z", "size_bytes": 1_073_741_824},
        ]
        return stub

    with (
        patch("modelman.local_control.ProviderRegistry.get_class", side_effect=get_class),
        patch("modelman.local_control.ProviderRegistry.get", side_effect=get),
        patch("modelman.local_control._probe_running", return_value=False),
    ):
        result = runner.invoke(app, ["start"])
    assert result.exit_code == 0, result.stdout
    assert "Registered, on disk:" in result.stdout
    assert "ollama/x" in result.stdout and "4.6 GB" in result.stdout
    assert "Registered, not downloaded:" in result.stdout
    assert "ollama/y" in result.stdout
    assert "Discovered" in result.stdout
    # 1_073_741_824 B = 1 GiB exactly, chosen so _format_size's repeated
    # /1024 division lands on a clean "1.0 GB" instead of a rounding-prone
    # value (e.g. 1_000_000_000 B formats as "953.7 MB", not "1000.0 MB").
    assert "ollama:z" in result.stdout and "1.0 GB" in result.stdout


def test_start_command_discovers_and_prompts_for_family(tmp_path, monkeypatch):
    registry_path = tmp_path / "registry.toml"
    registry_path.write_text(
        '[[providers]]\nid = "ollama"\nname = "Ollama"\nlocation = "local"\n'
        'auth = { type = "none" }\n\n'
    )
    state_path = tmp_path / "modelman.toml"
    litellm_path = tmp_path / "config.yaml"
    litellm_path.write_text("model_list: []\n")
    monkeypatch.setenv("MODELMAN_REGISTRY", str(registry_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    monkeypatch.setenv("MODELMAN_LITELLM_CONFIG", str(litellm_path))

    from unittest.mock import MagicMock

    from modelman.benchmark.isolation import IsolateResult

    def get_class(name):
        return object if name == "ollama" else None

    def get(name, config):
        stub = MagicMock()
        stub.list_local.return_value = [
            {"variant_id": "llama3.2:3b", "path": "ollama:llama3.2:3b", "size_bytes": 2_000_000_000}
        ]
        return stub

    with (
        patch("modelman.local_control.ProviderRegistry.get_class", side_effect=get_class),
        patch("modelman.local_control.ProviderRegistry.get", side_effect=get),
        patch("modelman.local_control.stop_all_local_providers"),
        patch("modelman.local_control.isolate_provider") as mock_isolate,
    ):
        mock_isolate.return_value = IsolateResult(
            provider="ollama", model="llama3.2:3b",
            direct_url="http://localhost:11434/v1/chat/completions", ok=True, error=None,
        )
        result = runner.invoke(app, ["start", "llama3.2:3b"], input="general\n")

    assert result.exit_code == 0, result.stdout
    assert "isn't registered yet" in result.stdout
    assert "Started llama3.2:3b" in result.stdout or "Started ollama/llama3.2:3b" in result.stdout
    assert load_state(path=state_path).local.running_model == "ollama/llama3.2:3b"


def test_start_command_empty_family_reprompts(tmp_path, monkeypatch):
    registry_path = tmp_path / "registry.toml"
    registry_path.write_text(
        '[[providers]]\nid = "ollama"\nname = "Ollama"\nlocation = "local"\n'
        'auth = { type = "none" }\n\n'
    )
    state_path = tmp_path / "modelman.toml"
    litellm_path = tmp_path / "config.yaml"
    litellm_path.write_text("model_list: []\n")
    monkeypatch.setenv("MODELMAN_REGISTRY", str(registry_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    monkeypatch.setenv("MODELMAN_LITELLM_CONFIG", str(litellm_path))

    from unittest.mock import MagicMock

    from modelman.benchmark.isolation import IsolateResult

    def get_class(name):
        return object if name == "ollama" else None

    def get(name, config):
        stub = MagicMock()
        stub.list_local.return_value = [
            {"variant_id": "llama3.2:3b", "path": "ollama:llama3.2:3b", "size_bytes": None}
        ]
        return stub

    with (
        patch("modelman.local_control.ProviderRegistry.get_class", side_effect=get_class),
        patch("modelman.local_control.ProviderRegistry.get", side_effect=get),
        patch("modelman.local_control.stop_all_local_providers"),
        patch("modelman.local_control.isolate_provider") as mock_isolate,
    ):
        mock_isolate.return_value = IsolateResult(
            provider="ollama", model="llama3.2:3b",
            direct_url="http://localhost:11434/v1/chat/completions", ok=True, error=None,
        )
        result = runner.invoke(app, ["start", "llama3.2:3b"], input="\ngeneral\n")

    assert result.exit_code == 0, result.stdout
    assert "cannot be empty" in result.stdout
    assert load_state(path=state_path).local.running_model == "ollama/llama3.2:3b"
```

Add `from unittest.mock import patch` to the top of `tests/commands/test_local_control.py` (it currently only has `from unittest.mock import patch` — confirm it's already imported; it is, per the existing `test_start_command_success_writes_marker`).

- [ ] **Step 2: Run tests to verify they fail**

Run: `uv run pytest tests/commands/test_local_control.py -v`
Expected: FAIL — `test_start_command_no_args_lists_exposed_local_models`/`_indicates_running_model`/`_no_exposed_models` fail (old assertions on the old single-list format) and the three new tests fail (new behavior not wired up yet).

- [ ] **Step 3: Update the `start` command**

In `modelman/src/modelman/main.py`:

1. Update the `local_control` import (current lines 20-25):

```python
from .local_control import (
    DiscoveredModelNeedsFamily,
    LocalControlError,
    inventory_local_models,
    start_local_model,
    stop_local_model,
)
```

2. Add a tiny size formatter near the top of the file (after the `app`/`litellm_app` setup, e.g. right before the `start` command at current line 261) — deliberately not reusing `screens/models.py::_human_size` (private to that module; this is a 6-line duplicate, not worth a shared import across the TUI/CLI boundary):

```python
def _format_size(n: int | None) -> str:
    if n is None:
        return "—"
    value = float(n)
    if value < 1024:
        return f"{int(value)} B"
    for unit in ("KB", "MB", "GB", "TB"):
        value /= 1024
        if value < 1024:
            return f"{value:.1f} {unit}"
    return f"{value:.1f} PB"
```

3. Replace the whole `start` command (current lines 261-300) with:

```python
@app.command()
def start(
    model_id: str | None = typer.Argument(
        None,
        help="Registry model id, or a provider-native model name, to run locally. "
        "Omit to list local models.",
    ),
) -> None:
    """Stop any running local model and start model_id, recording it as
    the single local model wt's picker may offer. Idempotent when
    model_id's marker still matches a probe of the running process.

    model_id may be a registry id, an existing model's native
    provider-side name, or the native name of a model a provider has on
    disk but that has no registry.toml entry yet — the last case prompts
    for a family, then registers, exposes, and starts it in one step.

    Omit model_id to print a live inventory: models registered and on
    disk, models registered but missing their artifact, and on-disk
    models with no registry.toml entry yet.
    """
    registry = load_registry()
    if model_id is None:
        state = load_state()
        inventory = inventory_local_models(registry, state)
        if not (inventory.downloaded or inventory.not_downloaded or inventory.discovered):
            typer.echo("No local models found. `modelman start <name>` will register one it finds on disk.")
            return
        if inventory.downloaded:
            typer.echo("Registered, on disk:")
            for entry in inventory.downloaded:
                marker = "*" if entry.running else " "
                suffix = " (running)" if entry.running else ""
                typer.echo(f"{marker} {entry.model_id}\t{_format_size(entry.size_bytes)}{suffix}")
            typer.echo()
        if inventory.not_downloaded:
            typer.echo("Registered, not downloaded:")
            for model_id_str in inventory.not_downloaded:
                typer.echo(f"  {model_id_str}")
            typer.echo()
        if inventory.discovered:
            typer.echo("Discovered (not in registry.toml — `modelman start <name>` to add):")
            for disc in inventory.discovered:
                typer.echo(f"  {disc.provider_id}:{disc.variant_id}\t{_format_size(disc.size_bytes)}")
            typer.echo()
        typer.echo("Run `modelman start <model_id>` to start one.")
        return

    family: str | None = None
    while True:
        try:
            # start_local_model owns the marker read/write (short locked_state
            # transactions around it); the stop-all/warmup subprocesses must run
            # outside any state lock.
            result = start_local_model(registry, model_id, family=family)
            break
        except DiscoveredModelNeedsFamily as exc:
            hint = f" (existing: {', '.join(exc.suggested_families)})" if exc.suggested_families else ""
            typer.echo(f"{exc.provider_id}/{exc.variant_id} was found on disk but isn't registered yet.{hint}")
            answer = typer.prompt("Family name for this model")
            while not answer.strip():
                typer.echo("Family name cannot be empty.", err=True)
                answer = typer.prompt("Family name for this model")
            family = answer.strip()
        except LocalControlError as exc:
            typer.echo(f"error: {exc}", err=True)
            raise typer.Exit(1) from exc

    if result.already_running:
        typer.echo(f"{result.model_id} is already running.")
    else:
        typer.echo(f"Started {result.model_id}.")
    for warning in result.warnings:
        typer.echo(f"warning: {warning}", err=True)
```

4. Update the three pre-existing no-arg tests in `tests/commands/test_local_control.py` (`test_start_command_no_args_lists_exposed_local_models`, `_indicates_running_model`, `_no_exposed_models`) to match the new format — they assert on the old "Local models available to start:" / "No local models are exposed." strings, which no longer exist:

```python
def test_start_command_no_args_lists_registered_downloaded_models(tmp_path, monkeypatch):
    registry_path = tmp_path / "registry.toml"
    registry_path.write_text(
        '[[providers]]\nid = "ollama"\nname = "Ollama"\nlocation = "local"\n'
        'auth = { type = "none" }\n\n'
        '[[models]]\nid = "ollama/x"\nfamily = "x"\nprovider_id = "ollama"\nmodel_name = "x"\n'
    )
    state_path = tmp_path / "modelman.toml"
    state_path.write_text('[model_state."ollama/x"]\nready = true\nexposed = true\n')
    monkeypatch.setenv("MODELMAN_REGISTRY", str(registry_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))

    with (
        patch("modelman.local_control.ProviderRegistry.get_class", return_value=object),
        patch(
            "modelman.local_control.ProviderRegistry.get",
            return_value=_stub_provider([{"variant_id": "x", "path": "ollama:x", "size_bytes": None}]),
        ),
        patch("modelman.local_control._probe_running", return_value=False),
    ):
        result = runner.invoke(app, ["start"])
    assert result.exit_code == 0, result.stdout
    assert "ollama/x" in result.stdout
    assert "Run `modelman start <model_id>` to start one." in result.stdout


def test_start_command_no_args_indicates_running_model(tmp_path, monkeypatch):
    registry_path = tmp_path / "registry.toml"
    registry_path.write_text(
        '[[providers]]\nid = "ollama"\nname = "Ollama"\nlocation = "local"\n'
        'auth = { type = "none" }\n\n'
        '[[models]]\nid = "ollama/x"\nfamily = "x"\nprovider_id = "ollama"\nmodel_name = "x"\n'
    )
    state_path = tmp_path / "modelman.toml"
    state_path.write_text(
        '[local]\nrunning_model = "ollama/x"\n\n'
        '[model_state."ollama/x"]\nready = true\nexposed = true\n'
    )
    monkeypatch.setenv("MODELMAN_REGISTRY", str(registry_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))

    with (
        patch("modelman.local_control.ProviderRegistry.get_class", return_value=object),
        patch(
            "modelman.local_control.ProviderRegistry.get",
            return_value=_stub_provider([{"variant_id": "x", "path": "ollama:x", "size_bytes": None}]),
        ),
        patch("modelman.local_control._probe_running", return_value=True),
    ):
        result = runner.invoke(app, ["start"])
    assert result.exit_code == 0, result.stdout
    assert "ollama/x" in result.stdout and "(running)" in result.stdout


def test_start_command_no_args_nothing_found(tmp_path, monkeypatch):
    registry_path = tmp_path / "registry.toml"
    registry_path.write_text("")
    monkeypatch.setenv("MODELMAN_REGISTRY", str(registry_path))
    monkeypatch.setenv("MODELMAN_STATE", str(tmp_path / "modelman.toml"))

    result = runner.invoke(app, ["start"])
    assert result.exit_code == 0
    assert "No local models found." in result.stdout
```

Add a small module-level helper next to `runner = CliRunner()` at the top of the test file for the stub used above and by the three new Step-1 tests:

```python
def _stub_provider(local_models: list[dict]):
    from unittest.mock import MagicMock

    stub = MagicMock()
    stub.list_local.return_value = local_models
    return stub
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `uv run pytest tests/commands/test_local_control.py -v`
Expected: PASS — all tests in the file, including the rewritten no-arg ones and the three new discovery/prompt tests.

- [ ] **Step 5: Commit**

```bash
git add modelman/src/modelman/main.py modelman/tests/commands/test_local_control.py
git commit -m "feat(modelman): print a three-section local inventory and prompt for a
family when starting a discovered model

modelman start (no args) now prints registered+on-disk, registered-not-
downloaded, and discovered sections with sizes instead of the old
exposed-only list; modelman start <name> prompts once for a family when
<name> resolves to an on-disk-but-unregistered artifact, then registers,
exposes, and starts it.

Completes plan item docs/superpowers/plans/2026-09-13-modelman-start-provider-discovery.md#task-3"
```

---

### Task 4: Docs + full verification

**Files:**
- Modify: `modelman/CLAUDE.md`
- Modify: `CLAUDE.md` (monorepo root, if its `start` summary needs updating — check first)

**Interfaces:**
- Consumes: nothing new — this task is documentation + a full-suite regression check, no production code changes.
- Produces: nothing new — updates the prose describing `start`'s behavior to match Tasks 1-3.

- [ ] **Step 1: Update `modelman/CLAUDE.md`**

In `modelman/CLAUDE.md`'s "Project overview" paragraph, the `start [model_id]`/`stop` bullet currently reads (approximately):

> `start [model_id]`/`stop` (issue #65 — the single local model wt's picker may offer; delegates to bin/llm-isolate-provider; `start` with no `model_id` lists local models with expose on, indicating which one is running).

Replace with:

```markdown
`start [model_id]`/`stop` (issue #65 — the single local model wt's picker may offer; delegates to bin/llm-isolate-provider; `start` with no `model_id` prints a live three-way inventory — registered+on-disk, registered-but-missing, and discovered-but-unregistered, each cross-referenced against every in-scope provider's `Provider.list_local()` rather than trusting only cached state; `start <name>` accepts a registry id, an existing model's native provider-side name, or the native name of a discovered artifact, auto-registering+exposing the last case after an interactive family prompt — see `docs/superpowers/specs/2026-09-13-modelman-start-provider-discovery-design.md`).
```

Also update the "Local-model lifecycle (issue #65)" section near the bottom of `modelman/CLAUDE.md` to add one sentence after its existing description, before the `wt` cross-reference:

```markdown
`modelman start`'s no-arg listing and its discovered-model auto-register
path (`DISCOVERY_PROVIDER_IDS` in `local_control.py`) query each
in-scope local provider's `list_local()` live rather than trusting only
`modelman.toml`'s cached `ready` flag — see
`docs/superpowers/specs/2026-09-13-modelman-start-provider-discovery-design.md`.
```

- [ ] **Step 2: Check whether the monorepo-root `CLAUDE.md` needs updating**

Run: `grep -n "modelman start" /Users/keith/github/ohanaverse/local-ai-setup/CLAUDE.md`

If it only names the command (not its listing behavior), no change is needed there — the root `CLAUDE.md`'s per-package pointer already sends readers to `modelman/CLAUDE.md` for detail. Only edit it if it currently asserts something about `start`'s output that Task 3 changed.

- [ ] **Step 3: Run the full modelman suite and quality gates**

Run: `cd modelman && uv run pytest -q`
Expected: PASS, full suite (no regressions in `sync`, TUI reconcile, or any other consumer of `registry.py`/`state.py`/`litellm.py` helpers touched here).

Run: `cd modelman && make check`
Expected: PASS (ruff lint + format check + mypy/typecheck). Fix any findings inline (e.g. unused-import flags on the Task 1 import block if Task 2's code didn't land as expected — re-verify every import added in Task 1 is used after Task 2).

- [ ] **Step 4: Commit**

```bash
git add modelman/CLAUDE.md
git commit -m "docs(modelman): document the provider-backed start inventory and auto-register flow

Completes plan item docs/superpowers/plans/2026-09-13-modelman-start-provider-discovery.md#task-4"
```

---

## Self-Review Notes

- **Spec coverage:** §1 (classification helper) → Task 1's `_provider_local_models`/`_registered_pairs`/`inventory_local_models`. §2 (three-section listing) → Task 3 Step 3.2. §3 (auto-register fallback, including the family prompt, id scheme, exposure ordering, ambiguous-match error) → Task 2. §4 (error handling — provider failure tolerance, empty family re-prompt) → Task 1's `test_inventory_tolerates_a_provider_list_local_failure` and Task 3's `test_start_command_empty_family_reprompts`. §5 (testing) → covered throughout. The "Open risk" note (omlx `list_local()` losing the org prefix) is inherent to the existing `OMLXProvider.list_local()` and needed no new task — Task 1/2's tests use ollama fixtures precisely to avoid conflating that pre-existing limitation with new code.
- **Extra correctness fix beyond the spec's literal text:** the native-provider-name resolution step in `_resolve_or_register` (Task 2) wasn't in the original spec wording but is required for idempotency — without it, re-running `modelman start <same-bare-name>` after auto-registration would regress to "unknown model" because the registered id (`<provider>/<name>`) differs from the bare name the user typed. Covered by `test_start_native_name_resolves_existing_registered_model_without_reregistering`.
- **Placeholder scan:** no TBD/TODO; every step has real code.
- **Type consistency:** `DiscoveredModel`/`InventoryEntry`/`LocalModelInventory` (Task 1) are reused verbatim by `_find_discovered`/`_register_discovered_model` (Task 2) and `inventory_local_models`'s printing (Task 3). `StartResult.warnings` (Task 2) is read in Task 3's CLI loop. `start_local_model`'s new keyword-only params (`family`, `registry_path`, `litellm_path`) match across Task 2's implementation and Task 3's call site.
