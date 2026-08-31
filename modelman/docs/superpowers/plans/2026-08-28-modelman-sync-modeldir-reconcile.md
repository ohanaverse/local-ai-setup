# `modelman sync` (modeldir reconcile) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extend `modelman sync` to reconcile the downloaded state of configured llamacpp (HF cache) and omlx (`model_dir`) models, in addition to ollama, without adding new models.

**Architecture:** `sync.py` gains `list_modeldir` (per-model `is_downloaded`/`path_of`/`size_of` checks) and `reconcile` is generalized from ollama-only to provider-agnostic (keyed by model id). Providers gain a `path_of` method so path logic stays in the provider.

**Tech Stack:** Python 3.13, Typer, pytest, uv.

**Spec:** `docs/superpowers/specs/2026-08-28-modelman-sync-modeldir-reconcile-design.md`

---

## File Structure

- **Modify: `src/modelman/providers/llamacpp.py`** — add `path_of`.
- **Modify: `src/modelman/providers/omlx.py`** — add `path_of`.
- **Modify: `src/modelman/sync.py`** — refactor `reconcile`; add `_model_entry_to_variant`, `_ollama_downloaded`, `_modeldir_providers`, `list_modeldir`; update `sync`.
- **Modify: `src/modelman/main.py`** — new summary line.
- **Modify: `tests/test_providers/test_llamacpp.py`**, `tests/test_providers/test_omlx.py` — `path_of` tests.
- **Modify: `tests/test_sync.py`** — refactor reconcile tests; add helper/list_modeldir/sync tests.
- **Modify: `tests/commands/test_sync.py`** — new summary.

Files NOT touched: `registry.py`, `state.py`, `screens/models.py`.

---

## Task 1: Add `path_of` to llamacpp and omlx providers

**Files:**
- Modify: `src/modelman/providers/llamacpp.py`
- Modify: `src/modelman/providers/omlx.py`
- Test: `tests/test_providers/test_llamacpp.py`, `tests/test_providers/test_omlx.py`

- [ ] **Step 1: Write the failing tests**

Append to `tests/test_providers/test_llamacpp.py`:

```python
def test_path_of_returns_primary_file_path(tmp_path, monkeypatch):
    hf = tmp_path / "hf"
    snap = hf / "hub" / "models--ornith--test" / "snapshots" / "rev1"
    snap.mkdir(parents=True)
    f = snap / "model.gguf"
    f.write_bytes(b"x" * 100)

    monkeypatch.setenv("HF_HOME", str(hf))
    p = LlamaCppProvider({})
    path = p.path_of(
        {
            "id": "x",
            "provider": "llamacpp",
            "name": "x",
            "repo": "ornith/test",
            "files": ["model.gguf"],
        }
    )
    assert path == str(f)


def test_path_of_returns_none_when_not_in_cache(tmp_path, monkeypatch):
    monkeypatch.setenv("HF_HOME", str(tmp_path / "empty"))
    p = LlamaCppProvider({})
    assert (
        p.path_of(
            {
                "id": "x",
                "provider": "llamacpp",
                "name": "x",
                "repo": "ornith/missing",
                "files": ["model.gguf"],
            }
        )
        is None
    )
```

Append to `tests/test_providers/test_omlx.py`:

```python
def test_path_of_returns_model_dir(tmp_path):
    md = tmp_path / "models"
    target = md / "Ornith-1.5"
    target.mkdir(parents=True)
    (target / "config.json").write_text("{}")

    p = OMLXProvider({"model_dir": str(md)})
    path = p.path_of(
        {
            "id": "x",
            "provider": "omlx",
            "name": "x",
            "repo": "ornith/Ornith-1.5",
        }
    )
    assert path == str(target)


def test_path_of_returns_none_when_missing(tmp_path):
    p = OMLXProvider({"model_dir": str(tmp_path / "models")})
    assert (
        p.path_of(
            {
                "id": "x",
                "provider": "omlx",
                "name": "x",
                "repo": "ornith/missing",
            }
        )
        is None
    )
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `uv run pytest tests/test_providers/test_llamacpp.py::test_path_of_returns_primary_file_path tests/test_providers/test_omlx.py::test_path_of_returns_model_dir -v`
Expected: FAIL with `AttributeError: 'LlamaCppProvider' object has no attribute 'path_of'`

- [ ] **Step 3: Write minimal implementation**

In `src/modelman/providers/llamacpp.py`, add `path_of` after `size_of` (before `list_local`):

```python
    def path_of(self, variant: VariantSpec) -> str | None:
        repo = variant.get("repo")
        files = variant.get("files")
        if not repo or not files:
            return None
        hf_org, hf_name = repo.split("/", 1)
        repo_dir = _hf_cache_dir() / f"models--{hf_org}--{hf_name}" / "snapshots"
        if not repo_dir.exists():
            return None
        primary = files[0]
        for snap in repo_dir.iterdir():
            candidate = snap / primary
            if candidate.exists():
                return str(candidate)
        return None
```

In `src/modelman/providers/omlx.py`, add `path_of` after `size_of` (before `list_local`):

```python
    def path_of(self, variant: VariantSpec) -> str | None:
        repo = variant.get("repo")
        if not repo:
            return None
        target = _model_dir(self.config) / _basename(repo)
        if target.is_dir() and any(target.iterdir()):
            return str(target)
        return None
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `uv run pytest tests/test_providers/test_llamacpp.py tests/test_providers/test_omlx.py -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add src/modelman/providers/llamacpp.py src/modelman/providers/omlx.py tests/test_providers/test_llamacpp.py tests/test_providers/test_omlx.py
git commit -m "feat(providers): add path_of to llamacpp and omlx"
```

---

## Task 2: Add `_model_entry_to_variant` + `_ollama_downloaded` to sync.py

**Files:**
- Modify: `src/modelman/sync.py`
- Test: `tests/test_sync.py`

- [ ] **Step 1: Write the failing tests**

In `tests/test_sync.py`, update the import block to add `Fetch` and the two new functions:

```python
from modelman.registry import Fetch, ModelEntry, Registry
from modelman.state import ModelState, StateStore
from modelman.sync import (
    SyncError,
    _model_entry_to_variant,
    _ollama_downloaded,
    _parse_ollama_list_sizes,
    list_ollama,
    reconcile,
    sync,
)
```

Append these tests:

```python
def test_model_entry_to_variant():
    entry = ModelEntry(
        id="llamacpp/q4",
        family="f",
        provider_id="llamacpp",
        model_name="q4",
        fetch=Fetch(repo="ornith/test", files=["model.gguf"]),
    )
    assert _model_entry_to_variant(entry) == {
        "id": "llamacpp/q4",
        "provider": "llamacpp",
        "name": "q4",
        "repo": "ornith/test",
        "files": ["model.gguf"],
        "quantizations": None,
        "model_info": {},
    }


def test_ollama_downloaded_maps_name_to_id():
    registry = Registry(
        models=[
            ModelEntry(id="ollama/a", family="a", provider_id="ollama", model_name="a"),
            ModelEntry(id="ollama/b", family="b", provider_id="ollama", model_name="b"),
        ]
    )
    assert _ollama_downloaded(registry, {"a": 1024}) == {"ollama/a": ("ollama:a", 1024)}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `uv run pytest tests/test_sync.py::test_model_entry_to_variant tests/test_sync.py::test_ollama_downloaded_maps_name_to_id -v`
Expected: FAIL with `ImportError: cannot import name '_model_entry_to_variant' from 'modelman.sync'`

- [ ] **Step 3: Write minimal implementation**

In `src/modelman/sync.py`, update the imports to add `VariantSpec` and `ModelEntry`:

```python
from .providers.base import VariantSpec
from .registry import ModelEntry, Registry
from .state import ModelState, StateStore
```

Add these two functions after `list_ollama` (before `SyncResult`):

```python
def _model_entry_to_variant(entry: ModelEntry) -> VariantSpec:
    """Build a VariantSpec-shaped dict from a ModelEntry for provider APIs."""
    repo = entry.fetch.repo if entry.fetch else None
    files = entry.fetch.files if entry.fetch else None
    quantizations = entry.fetch.quantizations if entry.fetch else None
    return {
        "id": entry.id,
        "provider": entry.provider_id,
        "name": entry.model_name,
        "repo": repo,
        "files": files,
        "quantizations": quantizations,
        "model_info": dict(entry.model_info),
    }


def _ollama_downloaded(
    registry: Registry, sizes: dict[str, int]
) -> dict[str, tuple[str, int]]:
    """Map `ollama list`'s {name: size} to {model_id: (disk_path, size)}."""
    result: dict[str, tuple[str, int]] = {}
    for m in registry.models:
        if m.provider_id == "ollama" and m.model_name in sizes:
            result[m.id] = (f"ollama:{m.model_name}", sizes[m.model_name])
    return result
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `uv run pytest tests/test_sync.py -v`
Expected: PASS (existing tests still pass; new tests pass)

- [ ] **Step 5: Commit**

```bash
git add src/modelman/sync.py tests/test_sync.py
git commit -m "feat(sync): add model-entry-to-variant and ollama-downloaded helpers"
```

---

## Task 3: Add `list_modeldir` + `_modeldir_providers` to sync.py

**Files:**
- Modify: `src/modelman/sync.py`
- Test: `tests/test_sync.py`

- [ ] **Step 1: Write the failing tests**

In `tests/test_sync.py`, update the import block to add `AuthConfig`, `ProviderEntry`, `list_modeldir`, `_modeldir_providers`:

```python
from modelman.registry import AuthConfig, Fetch, ModelEntry, ProviderEntry, Registry
from modelman.state import ModelState, StateStore
from modelman.sync import (
    SyncError,
    _model_entry_to_variant,
    _modeldir_providers,
    _ollama_downloaded,
    _parse_ollama_list_sizes,
    list_modeldir,
    list_ollama,
    reconcile,
    sync,
)
```

Append these tests:

```python
def test_list_modeldir_downloaded():
    registry = Registry(
        models=[
            ModelEntry(
                id="llamacpp/a", family="a", provider_id="llamacpp", model_name="a",
                fetch=Fetch(repo="o/r", files=["a.gguf"]),
            ),
            ModelEntry(
                id="omlx/b", family="b", provider_id="omlx", model_name="b",
                fetch=Fetch(repo="o/b"),
            ),
        ]
    )
    llamacpp = MagicMock()
    llamacpp.is_downloaded.return_value = True
    llamacpp.path_of.return_value = "/cache/a.gguf"
    llamacpp.size_of.return_value = 100
    omlx = MagicMock()
    omlx.is_downloaded.return_value = False
    providers = {"llamacpp": llamacpp, "omlx": omlx}

    result = list_modeldir(registry, providers)

    assert result == {"llamacpp/a": ("/cache/a.gguf", 100)}
    llamacpp.is_downloaded.assert_called_once()
    omlx.is_downloaded.assert_called_once()


def test_modeldir_providers_builds_instances():
    registry = Registry(
        providers=[
            ProviderEntry(id="llamacpp", name="llama.cpp", auth=AuthConfig(type="none")),
            ProviderEntry(
                id="omlx", name="oMLX", model_dir="/models", auth=AuthConfig(type="none")
            ),
        ],
        models=[
            ModelEntry(id="llamacpp/a", family="a", provider_id="llamacpp", model_name="a"),
            ModelEntry(id="omlx/b", family="b", provider_id="omlx", model_name="b"),
        ],
    )

    providers = _modeldir_providers(registry)

    assert set(providers) == {"llamacpp", "omlx"}
    assert providers["omlx"].config == {"model_dir": "/models"}
    assert providers["llamacpp"].config == {}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `uv run pytest tests/test_sync.py::test_list_modeldir_downloaded tests/test_sync.py::test_modeldir_providers_builds_instances -v`
Expected: FAIL with `ImportError: cannot import name 'list_modeldir' from 'modelman.sync'`

- [ ] **Step 3: Write minimal implementation**

In `src/modelman/sync.py`, update the imports to add `Any`, `ProviderRegistry`, `provider_config`:

```python
from .providers.base import VariantSpec
from .providers.registry import ProviderRegistry
from .registry import ModelEntry, Registry, provider_config
from .state import ModelState, StateStore
```

Add these two functions after `_ollama_downloaded` (before `SyncResult`):

```python
def _modeldir_providers(registry: Registry) -> dict[str, Any]:
    """Build llamacpp/omlx provider instances from registry provider entries."""
    providers: dict[str, Any] = {}
    for m in registry.models:
        if m.provider_id in ("llamacpp", "omlx") and m.provider_id not in providers:
            entry = registry.provider(m.provider_id)
            providers[m.provider_id] = ProviderRegistry.get(
                m.provider_id, provider_config(entry)
            )
    return providers


def list_modeldir(
    registry: Registry, providers: dict[str, Any]
) -> dict[str, tuple[str, int]]:
    """Return {model_id: (disk_path, size_bytes)} for downloaded llamacpp/omlx models."""
    result: dict[str, tuple[str, int]] = {}
    for m in registry.models:
        if m.provider_id not in ("llamacpp", "omlx"):
            continue
        provider = providers.get(m.provider_id)
        if provider is None:
            continue
        variant = _model_entry_to_variant(m)
        if provider.is_downloaded(variant):
            result[m.id] = (provider.path_of(variant), provider.size_of(variant))
    return result
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `uv run pytest tests/test_sync.py -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add src/modelman/sync.py tests/test_sync.py
git commit -m "feat(sync): add list_modeldir and modeldir provider builder"
```

---

## Task 4: Refactor `reconcile` + update `sync`

**Files:**
- Modify: `src/modelman/sync.py`
- Test: `tests/test_sync.py`

- [ ] **Step 1: Write the failing tests**

In `tests/test_sync.py`, update the import block to add `patch`:

```python
from unittest.mock import MagicMock, patch
```

Replace the four `reconcile` tests with these (keyed by model id, provider-agnostic):

```python
def test_reconcile_downloaded_model():
    registry = Registry(
        models=[
            ModelEntry(
                id="ollama/a", family="a", provider_id="ollama", model_name="a",
            ),
        ]
    )
    state = StateStore()
    result = reconcile(registry, state, {"ollama/a": ("ollama:a", 1024)})
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


def test_reconcile_modeldir_model():
    registry = Registry(
        models=[
            ModelEntry(
                id="llamacpp/a", family="a", provider_id="llamacpp", model_name="a",
            ),
        ]
    )
    state = StateStore()
    result = reconcile(registry, state, {"llamacpp/a": ("/cache/a.gguf", 100)})
    assert result.downloaded == ["llamacpp/a"]
    s = state.get("llamacpp/a")
    assert s.downloaded is True
    assert s.disk_path == "/cache/a.gguf"
    assert s.size_bytes == 100


def test_reconcile_skips_non_reconcilable_models():
    registry = Registry(
        models=[
            ModelEntry(
                id="openrouter/x", family="x", provider_id="openrouter", model_name="x",
            ),
        ]
    )
    state = StateStore()
    result = reconcile(registry, state, {"openrouter/x": ("/x", 1)})
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
    reconcile(registry, state, {"ollama/a": ("ollama:a", 1024)})
    assert state.get("ollama/a").litellm_exposed is True
```

Append a modeldir sync test (after the existing `test_sync_ignores_ollama_models_not_in_registry`):

```python
def test_sync_reconciles_modeldir_models():
    runner = MagicMock()
    runner.side_effect = [_result(0, "NAME  ID  SIZE  MODIFIED\n")]
    registry = Registry(
        models=[
            ModelEntry(
                id="llamacpp/a", family="a", provider_id="llamacpp", model_name="a",
                fetch=Fetch(repo="o/r", files=["a.gguf"]),
            ),
        ]
    )
    state = StateStore()
    stub = MagicMock()
    stub.is_downloaded.return_value = True
    stub.path_of.return_value = "/cache/a.gguf"
    stub.size_of.return_value = 100

    with patch("modelman.sync._modeldir_providers", return_value={"llamacpp": stub}):
        result = sync(registry, state, runner)

    assert result.downloaded == ["llamacpp/a"]
    assert result.not_downloaded == []
    assert state.get("llamacpp/a").downloaded is True
    assert state.get("llamacpp/a").disk_path == "/cache/a.gguf"
    assert state.get("llamacpp/a").size_bytes == 100
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `uv run pytest tests/test_sync.py -v`
Expected: FAIL — `test_reconcile_downloaded_model` fails with `TypeError` (old `reconcile` expects `dict[str, int]`, gets a tuple value)

- [ ] **Step 3: Write minimal implementation**

In `src/modelman/sync.py`, add the `RECONCILABLE_PROVIDERS` constant after `_SIZE_UNITS`:

```python
RECONCILABLE_PROVIDERS = ("ollama", "llamacpp", "omlx")
```

Replace `reconcile` with the provider-agnostic version:

```python
def reconcile(
    registry: Registry, state: StateStore, downloaded: dict[str, tuple[str, int]]
) -> SyncResult:
    """Update downloaded/disk_path/size_bytes for configured reconcilable models.

    `downloaded` maps model_id -> (disk_path, size_bytes). litellm_exposed
    is preserved (owned by the LiteLLM feature, not sync). Non-reconcilable
    providers (e.g. openrouter) are untouched.
    """
    result = SyncResult()
    for m in registry.models:
        if m.provider_id not in RECONCILABLE_PROVIDERS:
            continue
        existing = state.get(m.id)
        if m.id in downloaded:
            disk_path, size = downloaded[m.id]
            state.set(
                m.id,
                ModelState(
                    downloaded=True,
                    disk_path=disk_path,
                    size_bytes=size,
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
```

Replace `sync` with the multi-provider orchestrator:

```python
def sync(
    registry: Registry,
    state: StateStore,
    runner: _Runner | None = None,
) -> SyncResult:
    """Reconcile configured ollama + llamacpp/omlx models against provider state."""
    downloaded = _ollama_downloaded(registry, list_ollama(runner))
    downloaded.update(list_modeldir(registry, _modeldir_providers(registry)))
    return reconcile(registry, state, downloaded)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `uv run pytest tests/test_sync.py -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add src/modelman/sync.py tests/test_sync.py
git commit -m "feat(sync): reconcile all providers (ollama + modeldir)"
```

---

## Task 5: Update `sync` command summary

**Files:**
- Modify: `src/modelman/main.py`
- Test: `tests/commands/test_sync.py`

- [ ] **Step 1: Write the failing test**

In `tests/commands/test_sync.py`, update the docstring and the summary assertion:

```python
"""`modelman sync` reconciles configured ollama + llamacpp/omlx models
against provider state and writes modelman.toml. The sync logic itself is
covered by tests/test_sync.py; this covers the command wiring (load ->
sync -> save state -> report)."""
```

In `test_sync_command_saves_state_and_reports`, change the assertion:

```python
        assert "1 downloaded, 1 not downloaded" in result.stdout
```

to:

```python
        assert "Synced: 1 downloaded, 1 not downloaded." in result.stdout
```

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/commands/test_sync.py::test_sync_command_saves_state_and_reports -v`
Expected: FAIL with `AssertionError` (old summary is `Synced ollama: 1 downloaded, 1 not downloaded.`)

- [ ] **Step 3: Write minimal implementation**

In `src/modelman/main.py`, replace the `sync` command body:

```python
@app.command()
def sync() -> None:
    """Reconcile configured ollama + llamacpp/omlx models against provider state."""
    registry = load_registry()
    state = load_state()
    try:
        result = run_sync(registry, state)
    except SyncError as exc:
        typer.echo(f"error: {exc}", err=True)
        raise typer.Exit(1) from exc
    save_state(state)
    typer.echo(
        f"Synced: {len(result.downloaded)} downloaded, "
        f"{len(result.not_downloaded)} not downloaded."
    )
```

- [ ] **Step 4: Run test to verify it passes**

Run: `uv run pytest tests/commands/test_sync.py -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add src/modelman/main.py tests/commands/test_sync.py
git commit -m "feat(sync): report multi-provider summary"
```

---

## Final verification

- [ ] Run the full suite: `uv run pytest -q`
- [ ] Run lint + typecheck: `make check`
- [ ] Run the CLI smoke test: `uv run modelman sync` (expect a "Synced: N downloaded, M not downloaded." summary)

---

## Self-review notes

- **Spec coverage:** `path_of` (Task 1), `list_modeldir` (Task 3), `reconcile` refactor (Task 4), `_ollama_downloaded`/`_model_entry_to_variant` (Task 2), `_modeldir_providers` (Task 3), `sync` (Task 4), command summary (Task 5). All spec sections map to a task.
- **Type consistency:** `reconcile` takes `dict[str, tuple[str, int]]` in both its definition (Task 4) and its callers (`sync` in Task 4); `list_modeldir` returns `dict[str, tuple[str, int]]` matching `_ollama_downloaded`'s return type; `_modeldir_providers` returns `dict[str, Any]` matching `list_modeldir`'s `providers` param.
- **No placeholders:** every code step contains complete code; every command has an expected result.
