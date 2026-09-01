# Shared Model Registry — Phase 2, PR 1 (family/state foundation for TUI wiring) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add the read/write primitives the TUI will need to run entirely off `registry.toml` + `modelman.toml` — family enumeration over the flat model list, a family display-name/"known empty family" overlay, and a provider-config adapter — without touching `app.py`, any screen, or `queue.py` yet. After this PR, `uv run pytest` is green and modelman's on-disk behavior is completely unchanged; the new code is exercised only by its own unit tests.

**Architecture:** Phase 1 (`docs/superpowers/plans/2026-08-27-shared-model-registry-phase1.md`, merged as `e880aa6`) gave modelman `registry.toml` (`registry.py`) and `modelman.toml` (`state.py`), but nothing reads or writes them except `modelman migrate`. Wiring the TUI to them is Phase 2 of the same effort. Investigating that work (this plan's own research pass, not a prior spec — the governing spec `docs/superpowers/specs/2026-08-27-shared-model-registry-design.md` explicitly stops at the four-file architecture and migration semantics, and says nothing about post-migration TUI behavior) surfaced that it's too large and too interdependent for one plan:

- `queue.py`'s `PendingChanges` and `screens/models.py`'s `ModelScreen` are inseparable — `ModelScreen._run_apply` is `PendingChanges`'s only real caller, constructing it with `manifest=`/`manifest_path=` kwargs. Changing `PendingChanges`'s shape without updating `ModelScreen` in the same change would leave the app crashing at runtime even though `queue.py`'s own unit tests passed — so they must land together, in a later PR.
- `screens/families.py`'s `FamilyScreen` currently derives the whole family list by globbing `~/.config/local-ai/families/*.yaml` — one file per family, each carrying a `display_name`. `registry.toml` has no such concept: family membership is just a `family: str` field repeated on each flat `ModelEntry`, and there is no "empty family" placeholder or display name anywhere in the schema. Re-opening `registry.toml`'s schema to add either isn't an option here — it's the cross-repo file (`agent-worktree` is a documented future read-only consumer per the spec), already committed. The fix that doesn't touch the shared schema: extend **`modelman.toml`** — modelman's own file, read only by modelman, never by `agent-worktree` — with a `families` overlay (`[families.<id>]` → optional `display_name`). A family becomes "known" either by having ≥1 `ModelEntry.family == id` in `registry.toml`, or by having an entry in this overlay (covers "Add Family" before its first model exists); `FamilyScreen`'s add/edit/delete-family actions become overlay writes instead of manifest-file CRUD. This is designed in this PR (`state.py`) but not yet consumed by any screen — that's PR 3.
- `providers/*.py` read a plain `dict` (from `config.yaml`'s per-provider block) for constructor options; only `OMLXProvider` actually reads a key from it (`model_dir`). `registry.toml`'s `ProviderEntry` dataclass replaces that dict as the provider-config source of truth once the TUI switches over, so a small adapter (`provider_config()`) is needed to keep `ProviderRegistry.get(name, config)`'s existing signature working unchanged.

Given that, Phase 2 is split into four PRs, each independently shippable and each leaving the full test suite green:

1. **PR 1 (this plan)** — pure additive foundation in `registry.py`/`state.py`: `Registry.families()`/`.models_by_family()`, `provider_config()`, and `state.py`'s family overlay (`FamilyState`, `StateStore.family_display_name()`/`.touch_family()`/`.forget_family()`, `load_state`/`save_state` round-tripping `families`). Zero consumers changed.
2. **PR 2 (future)** — rewrite `queue.py`'s `PendingChanges` to operate on `Registry`/`StateStore`/`ModelEntry` instead of `FamilyManifest`/`VariantSpec`, bundled with the `ModelScreen` changes that are its only caller (`_run_apply`, `_run_reconcile`, `_load_models_for_provider`, `_is_downloaded`, `action_toggle_download`, `action_delete_model`, `_on_add_model`, `_on_edit_model`). `screens/forms.py`'s `ModelForm` keeps emitting its existing `VariantSpec`-shaped dict (add-model UX is unrelated to storage — see `docs/superpowers/specs/2026-09-02-modelman-add-dialog-simplification.md`); `ModelScreen` gains a small dict→`ModelEntry` adapter at the two call sites that currently append to `self.manifest.variants`. Rewrites `tests/test_queue.py` and the model-screen portions of `tests/screens/test_app_navigation.py`/`tests/screens/test_status.py`.
3. **PR 3 (future)** — migrate `FamilyScreen`/`app.py` to enumerate families via `Registry.families()` + this PR's `StateStore` overlay instead of globbing `family_dir`; `AddFamilyModal`/`EditFamilyModal` write the overlay instead of `save_manifest`. Rewrites the family-listing portions of `tests/screens/test_app_navigation.py` and `tests/screens/test_family_edit.py`.
4. **PR 4 (future)** — cleanup: confirm `config.py`/`manifest.py` are now migrate-only consumers (they must stay — `modelman migrate` still reads legacy `config.yaml`/`families/*.yaml` on demand), update `tests/commands/test_download.py` so the TUI-launch tests no longer depend on `MODELMAN_CONFIG`/`MODELMAN_FAMILY_DIR`, refresh `README.md`'s TUI-facing docs.

**Tech Stack:** Python 3.13, dataclasses (no pydantic — matches `registry.py`/`state.py`'s existing style), stdlib `tomllib`, `tomli-w` (already a dependency as of Phase 1).

**Spec:** `docs/superpowers/specs/2026-08-27-shared-model-registry-design.md` (schema/ownership — this plan's family-overlay and provider-config-adapter designs are new, scoped entirely to `modelman.toml`/`registry.py`'s existing read-helper surface, and don't require a spec amendment since neither touches the committed cross-repo schema).

## Global Constraints

- `requires-python = "==3.13.*"` (pyproject.toml) — no syntax/stdlib beyond 3.13.
- New code follows `registry.py`/`state.py`'s existing style: dataclasses, `load_*`/`save_*` functions, atomic write via `atomic_write_toml`, `drop_none` before every TOML write (TOML has no null type).
- Run `uv run pytest` (all tests) and `uv run ruff check .` before every commit in this plan; `make check` runs lint+typecheck without auto-fixing if you want a single combined command.
- No code comments beyond a short module-level docstring / a one-line non-obvious-rationale comment (matching existing style) — no per-line comments restating what the code does.

---

## File Structure

- **Modify** `src/modelman/registry.py` — add `Registry.families()`, `Registry.models_by_family()`, `provider_config()`.
- **Modify** `tests/test_registry.py` — tests for the above.
- **Modify** `src/modelman/state.py` — add `FamilyState`, extend `StateStore` with `families`, `family_display_name()`, `touch_family()`, `forget_family()`; extend `load_state`/`save_state` to round-trip `families`.
- **Modify** `tests/test_state.py` — tests for the above.

---

### Task 1: `Registry.families()` and `Registry.models_by_family()`

**Files:**
- Modify: `src/modelman/registry.py` (the `Registry` dataclass, `registry.py:75-90`)
- Test: `tests/test_registry.py`

**Interfaces:**
- Consumes: nothing new — operates on `Registry.models` (existing field).
- Produces: `Registry.families() -> list[str]` (sorted, deduplicated), `Registry.models_by_family(family: str) -> list[ModelEntry]` (input order preserved). PR 3 imports both; not consumed elsewhere in this PR.

- [ ] **Step 1: Write the failing tests**

Append to `tests/test_registry.py` (after `test_registry_lookup_helpers_raise_keyerror_for_unknown_id`):

```python
def test_families_returns_sorted_distinct_family_names():
    registry = Registry(
        models=[
            ModelEntry(id="ollama/a", family="zeta", provider_id="ollama", model_name="a"),
            ModelEntry(id="ollama/b", family="alpha", provider_id="ollama", model_name="b"),
            ModelEntry(id="llamacpp/c", family="alpha", provider_id="llamacpp", model_name="c"),
        ]
    )
    assert registry.families() == ["alpha", "zeta"]


def test_families_on_empty_registry_returns_empty_list():
    assert Registry().families() == []


def test_models_by_family_filters_and_preserves_order():
    a = ModelEntry(id="ollama/a", family="alpha", provider_id="ollama", model_name="a")
    z = ModelEntry(id="ollama/z", family="zeta", provider_id="ollama", model_name="z")
    b = ModelEntry(id="llamacpp/b", family="alpha", provider_id="llamacpp", model_name="b")
    registry = Registry(models=[a, z, b])
    assert registry.models_by_family("alpha") == [a, b]


def test_models_by_family_unknown_family_returns_empty_list():
    assert Registry().models_by_family("nope") == []
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `uv run pytest tests/test_registry.py -k families -v`
Expected: FAIL with `AttributeError: 'Registry' object has no attribute 'families'`

- [ ] **Step 3: Implement**

In `src/modelman/registry.py`, add these two methods to the `Registry` dataclass, right after `model()` (line 90, before the blank line at 91):

```python
    def families(self) -> list[str]:
        return sorted({m.family for m in self.models})

    def models_by_family(self, family: str) -> list[ModelEntry]:
        return [m for m in self.models if m.family == family]
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `uv run pytest tests/test_registry.py -v`
Expected: PASS (10 tests — 6 existing + 4 new)

- [ ] **Step 5: Lint and commit**

```bash
uv run ruff check src/modelman/registry.py tests/test_registry.py
git add src/modelman/registry.py tests/test_registry.py \
  docs/superpowers/plans/2026-08-27-shared-model-registry-phase2-pr1.md
git commit -m "feat: add Registry family-grouping helpers - completes plan item #1"
```

(This plan doc is committed to git per this project's convention — specs/plans for the model-management consolidation effort are tracked, not left as untracked PR-comment attachments — bundled into the first implementation commit, matching Phase 1's precedent.)

---

### Task 2: `provider_config()` adapter

**Files:**
- Modify: `src/modelman/registry.py`
- Test: `tests/test_registry.py`

**Interfaces:**
- Consumes: `ProviderEntry` (existing).
- Produces: `provider_config(entry: ProviderEntry) -> dict[str, Any]` in `modelman/registry.py`. PR 2 imports this to build the dict `ProviderRegistry.get(provider_id, config)` expects, in place of `config.provider(provider_id)`.

- [ ] **Step 1: Write the failing tests**

Append to `tests/test_registry.py`:

```python
def test_provider_config_includes_model_dir_when_set():
    entry = ProviderEntry(
        id="omlx", name="oMLX", model_dir="~/.omlx/models", auth=AuthConfig(type="none")
    )
    assert provider_config(entry) == {"model_dir": "~/.omlx/models"}


def test_provider_config_omits_model_dir_when_unset():
    entry = ProviderEntry(id="ollama", name="Ollama", auth=AuthConfig(type="none"))
    assert provider_config(entry) == {}
```

Add `provider_config` to the existing `from modelman.registry import (...)` block at the top of the file.

- [ ] **Step 2: Run tests to verify they fail**

Run: `uv run pytest tests/test_registry.py -k provider_config -v`
Expected: FAIL with `ImportError: cannot import name 'provider_config'`

- [ ] **Step 3: Implement**

In `src/modelman/registry.py`, add after `models_by_family()` (still inside file scope, but as a module-level function — place it directly after the `Registry` class, before `load_registry`):

```python
def provider_config(entry: ProviderEntry) -> dict[str, Any]:
    """Build the config dict `ProviderRegistry.get()` expects from a
    registry ProviderEntry. Only `model_dir` is read by any provider today
    (OMLXProvider) — kept minimal rather than mirroring every ProviderEntry
    field so providers stay decoupled from the registry schema.
    """
    config: dict[str, Any] = {}
    if entry.model_dir is not None:
        config["model_dir"] = entry.model_dir
    return config
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `uv run pytest tests/test_registry.py -v`
Expected: PASS (12 tests)

- [ ] **Step 5: Lint and commit**

```bash
uv run ruff check src/modelman/registry.py tests/test_registry.py
git add src/modelman/registry.py tests/test_registry.py
git commit -m "feat: add provider_config adapter for ProviderRegistry.get - completes plan item #2"
```

---

### Task 3: `StateStore` family overlay

**Files:**
- Modify: `src/modelman/state.py`
- Test: `tests/test_state.py`

**Interfaces:**
- Consumes: `drop_none`, `atomic_write_toml` from `modelman._toml_io` (existing import).
- Produces: `FamilyState` (dataclass, field: `display_name: str | None = None`); `StateStore.families: dict[str, FamilyState]` (new field); `StateStore.family_display_name(family: str) -> str` (falls back to `family` itself); `StateStore.touch_family(family: str, display_name: str | None = None) -> None`; `StateStore.forget_family(family: str) -> None`. `load_state`/`save_state` round-trip `families` under a new top-level `[families.<id>]` TOML table. PR 3 imports `FamilyState` and the three new `StateStore` methods.

- [ ] **Step 1: Write the failing tests**

Append to `tests/test_state.py`:

```python
def test_family_display_name_falls_back_to_family_id():
    store = StateStore()
    assert store.family_display_name("qwen3.8") == "qwen3.8"


def test_touch_family_sets_display_name():
    store = StateStore()
    store.touch_family("qwen3.8", display_name="Qwen 3.8")
    assert store.family_display_name("qwen3.8") == "Qwen 3.8"


def test_touch_family_without_display_name_still_marks_family_known():
    store = StateStore()
    store.touch_family("qwen3.8")
    assert "qwen3.8" in store.families
    assert store.family_display_name("qwen3.8") == "qwen3.8"


def test_touch_family_preserves_existing_display_name_when_not_given():
    store = StateStore()
    store.touch_family("qwen3.8", display_name="Qwen 3.8")
    store.touch_family("qwen3.8")
    assert store.family_display_name("qwen3.8") == "Qwen 3.8"


def test_forget_family_removes_known_entry():
    store = StateStore()
    store.touch_family("qwen3.8", display_name="Qwen 3.8")
    store.forget_family("qwen3.8")
    assert "qwen3.8" not in store.families
    assert store.family_display_name("qwen3.8") == "qwen3.8"


def test_forget_family_unknown_family_is_a_noop():
    store = StateStore()
    store.forget_family("nope")  # must not raise
    assert store.families == {}


def test_save_then_load_round_trips_family_overlay(tmp_path):
    store = StateStore()
    store.touch_family("qwen3.8", display_name="Qwen 3.8")
    store.touch_family("empty-family")  # known, no display name yet
    path = tmp_path / "modelman.toml"

    save_state(store, path)
    loaded = load_state(path)

    assert loaded.family_display_name("qwen3.8") == "Qwen 3.8"
    assert "empty-family" in loaded.families
    assert loaded.family_display_name("empty-family") == "empty-family"
```

Add `FamilyState` to the existing `from modelman.state import (...)` block at the top of the file.

- [ ] **Step 2: Run tests to verify they fail**

Run: `uv run pytest tests/test_state.py -v`
Expected: FAIL — `ImportError: cannot import name 'FamilyState'` (collection error affecting the whole file)

- [ ] **Step 3: Implement**

Replace the full contents of `src/modelman/state.py`:

```python
"""modelman.toml — modelman's per-machine mutable state overlay.

Owner: modelman only. See registry.py for the canonical, shared model/
provider definitions this state is keyed against, and
docs/superpowers/specs/2026-08-27-shared-model-registry-design.md for the
ownership split.

The `families` table is a modelman-only addition on top of that spec: it
holds a family's display name and marks a family "known" before it has any
models (mirroring the legacy per-family manifest file's existence). It is
NOT part of the shared registry.toml schema and is never read by
agent-worktree.
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
class FamilyState:
    display_name: str | None = None


@dataclass
class StateStore:
    models: dict[str, ModelState] = field(default_factory=dict)
    families: dict[str, FamilyState] = field(default_factory=dict)

    def get(self, model_id: str) -> ModelState:
        return self.models.get(model_id, ModelState())

    def set(self, model_id: str, state: ModelState) -> None:
        self.models[model_id] = state

    def family_display_name(self, family: str) -> str:
        entry = self.families.get(family)
        if entry is not None and entry.display_name:
            return entry.display_name
        return family

    def touch_family(self, family: str, display_name: str | None = None) -> None:
        """Mark `family` as known, optionally setting its display name.

        Used both for "Add Family" (before any model exists) and to record
        a display name for a family that already has models. A call with
        no `display_name` preserves whatever name was set previously.
        """
        existing = self.families.get(family, FamilyState())
        self.families[family] = FamilyState(display_name=display_name or existing.display_name)

    def forget_family(self, family: str) -> None:
        self.families.pop(family, None)


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
    families = {
        family: FamilyState(display_name=entry.get("display_name"))
        for family, entry in raw.get("families", {}).items()
    }
    return StateStore(models=models, families=families)


def save_state(store: StateStore, path: Path | None = None) -> None:
    state_path = Path(path) if path else _default_state_path()
    payload = {
        "model_state": {model_id: drop_none(asdict(s)) for model_id, s in store.models.items()},
        "families": {family: drop_none(asdict(s)) for family, s in store.families.items()},
    }
    atomic_write_toml(payload, state_path)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `uv run pytest tests/test_state.py -v`
Expected: PASS (10 tests — 3 existing + 7 new)

- [ ] **Step 5: Lint and commit**

```bash
uv run ruff check src/modelman/state.py tests/test_state.py
git add src/modelman/state.py tests/test_state.py
git commit -m "feat: add family display-name/known-family overlay to modelman.toml - completes plan item #3"
```

---

### Task 4: Full verification

- [ ] **Step 1: Run the full test suite**

Run: `uv run pytest`
Expected: PASS (all tests, including the pre-existing suite untouched by this plan)

- [ ] **Step 2: Lint and typecheck**

Run: `uv run ruff check . && uv run mypy src/modelman`
Expected: no errors

- [ ] **Step 3: Confirm no behavior change**

Run: `uv run modelman` (interactive; needs a TTY) or skip if none available — either way, no code path added in this plan is reachable from `app.py`/any screen yet, so this is a formality, not a real risk. Sanity-check instead by grepping for any accidental new call site:

```bash
grep -rn "families()\|models_by_family\|provider_config\|touch_family\|forget_family\|family_display_name" src/modelman/app.py src/modelman/screens/
```

Expected: no matches — confirms this PR is additive-only, consistent with the Goal statement above.

---

## What's Next (not this plan)

See the numbered PR list in the Architecture section above. PR 2 (queue.py + ModelScreen rewrite) is the next one to plan — it's the largest of the four (rewrites `tests/test_queue.py` in full and touches the model-screen portions of `tests/screens/test_app_navigation.py`, a 1262-line file) and should get its own dedicated plan rather than being folded into this one.
