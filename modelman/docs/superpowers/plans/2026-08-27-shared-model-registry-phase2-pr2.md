# Shared Model Registry — Phase 2, PR 2 (queue.py + ModelScreen on Registry/StateStore) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `queue.py`'s `PendingChanges` and `screens/models.py`'s `ModelScreen` operate entirely on `Registry`+`StateStore`+`ModelEntry` (the PR 1 primitives) so the TUI's model-management flow writes/reads `registry.toml`+`modelman.toml` instead of `families/<family>.yaml`. After this PR the legacy `FamilyManifest`/`VariantSpec` types survive only as migrate-time inputs (`manifest.py`/`config.py` stay in place for `modelman migrate`) and as the `VariantSpec` TypedDict that providers consume — modelman's user-facing TUI no longer touches them.

**Architecture:** PR 1 (`docs/superpowers/plans/2026-08-27-shared-model-registry-phase2-pr1.md`, merged as `5959bc0`) added the read/write primitives: `Registry.families()`/`.models_by_family()`, `provider_config(entry)`, and `state.py`'s family overlay (`FamilyState`, `StateStore.family_display_name()`, `.touch_family()`, `.forget_family()`, with `load_state`/`save_state` round-tripping `families`). Nothing consumed them yet. The reason they couldn't be consumed alone: the TUI's data path goes

```
FamilyScreen → ModelScreen → PendingChanges → save_manifest → families/<family>.yaml
```

and `PendingChanges` is constructed inside `ModelScreen._run_apply` with `manifest=`/`manifest_path=` kwargs, while `ModelScreen` itself reads `self.manifest.variants` and `self.manifest.downloaded` throughout. Migrating `PendingChanges` without `ModelScreen` would crash at runtime even with a green `pytest` — they're inseparable, so they land together here.

Migration map (what this PR touches vs. what stays):

- `queue.py` `PendingChanges` — rewrite to carry `Registry`/`StateStore` (in-memory references it mutates), `registry_path`/`state_path` (where to write), `providers: dict[str, object]` (provider instances keyed by name), and `downloads: list[(model_id, VariantSpec)]` / `deletes: list[(model_id, VariantSpec)]` (each pair carries the `ModelEntry.id` for `Registry`/`StateStore` mutations and the legacy `VariantSpec` dict for the provider API calls, which still take that shape — provider migration is out of scope). `apply()` mutates the registry (removes `ModelEntry`s for deletes) and state (`StateStore.set(model_id, ModelState(downloaded=True, disk_path=…))` for downloads) and saves both files at the end, replacing the single `save_manifest(manifest, manifest_path)` write. Event-tag format is unchanged so `StatusScreen` keeps working without changes.
- `screens/models.py` `ModelScreen` — replace `self.manifest`/`self.manifest_path` with `self.registry`/`self.state`/`self.family`/`self.registry_path`/`self.state_path` (the `family` is the in-memory "current family" filter). Mutations in `action_*` methods (`_on_add_model`, `_on_edit_model`, `action_toggle_download`, `action_delete_model`) now update `self.registry.models` / `self.state.models` instead of `self.manifest.variants` / `self.manifest.downloaded`. `_run_reconcile`, `_load_models_for_provider`, `_is_downloaded`, and `_run_apply` all switch to `state.get(model_id).disk_path` and `Registry.provider(name)`/`provider_config(entry)` for provider construction (PR 1's adapter — replaces `config.provider(name)` which reads the legacy `config.yaml`). The reconcile overlay gains `model_id` keys (already `dict[vid, dict]`, no shape change). Snapshots use the new types.
- `screens/forms.py` — **NOT touched** (per the PR 1 plan: ModelForm keeps emitting its existing `VariantSpec`-shaped dict; the add-dialog UX is unrelated to storage — see `docs/superpowers/specs/2026-09-02-modelman-add-dialog-simplification.md`).
- `screens/families.py`, `screens/status.py`, `app.py` — **NOT touched** in this PR. `FamilyScreen` still globs `families/*.yaml` and constructs `ModelScreen(manifest, path, providers)`; PR 3 migrates `FamilyScreen` to enumerate via `Registry.families()`. StatusScreen keeps consuming the same event-tag format. `app.py` keeps loading the legacy manifest for the `--initial-family` path; PR 4 cleans this up.

Tests:

- `tests/test_queue.py` — **rewrite in full** to use `Registry`/`StateStore`/`ModelEntry` fixtures instead of `FamilyManifest`.
- `tests/screens/test_app_navigation.py` — **migrate model-screen portions only**: tests whose body crosses the `ModelScreen` boundary (asserts `isinstance(app.screen, ModelScreen)` after pressing Enter, hits `a`/`x`/`d`/`e`, or reads `app.screen.queued_*`/`app.screen.manifest.*`/`app.screen._snapshot_*`) get new fixtures (registry.toml + modelman.toml + state-toml) and new ctor kwargs. FamilyScreen-only tests (the rest of the file) stay on `FamilyManifest` — they don't exercise the migrated code, so they continue to work unchanged until PR 3.
- `tests/screens/test_status.py` — **migrate model-screen portions only**: the tests that construct `PendingChanges` directly (the `test_pending_changes_*` tests and the in-line `def run_apply` closures inside the `test_status_screen_*` tests) get new fixtures. The `app_with_apply` fixture is rewritten to seed registry.toml/modelman.toml and the `run_apply` closures build `PendingChanges` with the new signature.
- `tests/commands/test_download.py` — **NOT touched** (PR 3/4 cleanup; PR 2 leaves the legacy `MODELMAN_CONFIG`/`MODELMAN_FAMILY_DIR` env-vars intact because `FamilyScreen`/`app.py` still consume them).

Provider APIs (`download(VariantSpec)`, `size_of(VariantSpec)`, `delete(VariantSpec)`, `list_local()`) stay on the legacy `VariantSpec` shape — `ModelEntry` never reaches a provider in this PR. `PendingChanges` keeps `VariantSpec` instances around purely to call those methods; the `ModelEntry.id` rides alongside to key `Registry`/`StateStore` updates.

**Tech Stack:** Python 3.13, dataclasses (no pydantic), stdlib `tomllib`, `tomli-w` (Phase 1 dep), Textual (existing).

**Spec:** `docs/superpowers/specs/2026-08-27-shared-model-registry-design.md` (canonical schema/ownership) + `docs/superpowers/specs/2026-09-02-modelman-add-dialog-simplification.md` (ModelForm keeps its VariantSpec-shaped output). Both govern what stays unchanged in this PR.

## Global Constraints

- `requires-python = "==3.13.*"` (pyproject.toml) — no syntax/stdlib beyond 3.13.
- Match `registry.py`/`state.py`'s existing style: dataclasses, `load_*`/`save_*` functions, atomic write via `atomic_write_toml`, `drop_none` before every TOML write.
- Providers still consume `VariantSpec` TypedDict (unchanged); `PendingChanges`/`ModelScreen` keep `VariantSpec` instances for provider calls, paired with `model_id` (`ModelEntry.id`) for `Registry`/`StateStore` mutations.
- The `apply()` event-tag format is **unchanged** (pipe-delimited `verb:status|vid|label[|reason]`); `StatusScreen` consumes these and is untouched in this PR.
- `FamilyScreen`/`app.py` keep reading `families/*.yaml` for this PR — they are migrated in PR 3. ModelScreen's ctor changes; FamilyScreen's call sites at `screens/families.py:339` and `:349` are NOT updated here (they continue to construct the old ctor with a `FamilyManifest`, which will be migrated wholesale in PR 3 when FamilyScreen itself is rewired to `Registry`/`StateStore`).
- No code comments beyond a short module-level docstring / a one-line non-obvious-rationale comment (matching existing style) — no per-line comments restating what the code does.
- Run `uv run pytest` (all tests) and `uv run ruff check .` before every commit in this plan; `make check` runs lint+typecheck without auto-fixing if you want a single combined command.

---

## File Structure

- **Modify** `src/modelman/queue.py` — `PendingChanges` rewritten: drops `manifest`/`manifest_path` for `registry`/`state`/`family`/`registry_path`/`state_path`/`providers`; `downloads`/`deletes` become `list[(model_id: str, variant: VariantSpec)]`; `apply()` mutates registry + state, calls `save_registry`+`save_state` instead of `save_manifest`; event-tag format unchanged.
- **Modify** `src/modelman/screens/models.py` — `ModelScreen` ctor takes `(registry, state, family, registry_path, state_path, available_providers)`; `_run_apply`, `_run_reconcile`, `_load_models_for_provider`, `_is_downloaded`, `action_toggle_download`, `action_delete_model`, `_on_add_model`, `_on_edit_model`, `_restore_snapshot` all read/mutate registry/state. Two new adapter call sites: `_variant_to_model_entry(spec, family, registry)` builds the `ModelEntry` that gets appended to `registry.models` in `_on_add_model` / `_on_edit_model`.
- **Modify** `tests/test_queue.py` — rewrite to use `Registry`/`StateStore`/`ModelEntry` fixtures and `save_registry`/`save_state` assertions.
- **Modify** `tests/screens/test_app_navigation.py` — migrate model-screen portions only (~10 tests: `test_enter_opens_model_screen`, `test_model_screen_two_pane_lists_providers_and_models`, `test_toggle_download_queues_variant`, `test_status_shows_four_states`, `test_delete_action_noop_on_not_downloaded`, `test_add_then_delete_model_queues_changes`, `test_reconcile_shows_reality_when_manifest_out_of_date`, `test_reconcile_does_not_persist_to_disk_on_cancel`, `test_apply_merges_reconciled_state_into_manifest`, `test_escape_with_pending_shows_dialog_and_apply`, `test_discard_pending_exits_without_applying`, `test_family_screen_reconciles_on_resume_after_apply`, `test_enter_on_model_row_opens_edit_dialog`, `test_enter_on_provider_row_does_not_open_edit_dialog`, `test_model_screen_shows_all_providers_for_empty_family`, `test_model_screen_provider_table_count_zero_for_empty`, `test_model_screen_add_form_offers_all_providers_for_empty_family`, `test_model_screen_starts_with_cursor_on_first_provider`); rest of the file stays on `FamilyManifest` (untouched).
- **Modify** `tests/screens/test_status.py` — `app_with_apply` fixture seeds registry.toml/modelman.toml; all `PendingChanges(...)` constructions and the in-line `def run_apply` closures get the new signature.

---

### Task 1: `PendingChanges` rewritten on Registry/StateStore

**Files:**
- Modify: `src/modelman/queue.py` (the `PendingChanges` dataclass and its `apply`/`_download`/`_delete` methods, the `_label` helper, and the `EventFn` docstring example block)
- Test: `tests/test_queue.py` (full rewrite; legacy fixtures gone)

**Interfaces:**
- Consumes: `Registry`/`StateStore`/`ModelEntry` (PR 1), `VariantSpec` TypedDict (existing — for provider APIs), `provider_config(entry)` (PR 1), `save_registry`/`save_state` (PR 1), `atomic_write_toml` (existing), `human_bytes` (existing).
- Produces:
  ```python
  @dataclass
  class PendingChanges:
      registry: Registry
      state: StateStore
      family: str
      registry_path: Path
      state_path: Path
      providers: dict[str, object]
      downloads: list[tuple[str, VariantSpec]]   # (model_id, variant)
      deletes: list[tuple[str, VariantSpec]]     # (model_id, variant)
      failures: list[str] = field(default_factory=list)
      cancelled: bool = False
  ```
  `apply()` and `cancel()` keep their existing signatures (`on_event`, `on_progress`). Event-tag format unchanged so `StatusScreen` (`screens/status.py:100-167`) keeps working without modification.

- [ ] **Step 1: Rewrite `tests/test_queue.py` (new shape, fixture helpers)**

Replace the entire contents of `tests/test_queue.py` with:

```python
"""PendingChanges applies queued model changes to Registry + StateStore.

The on-disk write targets after this rewrite are registry.toml
(`save_registry`) and modelman.toml (`save_state`). The legacy
families/<family>.yaml manifest is no longer written by the TUI —
it survives only as a migrate-time input (see docs/superpowers/
specs/2026-08-27-shared-model-registry-design.md).

Provider APIs still consume `VariantSpec` (a TypedDict), so each
queued item is a (model_id, VariantSpec) tuple: model_id keys the
Registry/StateStore mutation; VariantSpec feeds the provider call.
"""

from __future__ import annotations

from pathlib import Path
from unittest.mock import MagicMock

import pytest

from modelman.providers._progress import DownloadCancelled
from modelman.queue import PendingChanges
from modelman.registry import (
    ModelEntry,
    ProviderEntry,
    AuthConfig,
    Registry,
    load_registry,
    save_registry,
)
from modelman.state import (
    FamilyState,
    ModelState,
    StateStore,
    load_state,
    save_state,
)


# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------


@pytest.fixture
def store(tmp_path):
    """StateStore rooted at tmp_path/modelman.toml."""
    return tmp_path / "modelman.toml"


def _registry_with(tmp_path: Path, *entries: ModelEntry) -> tuple[Registry, Path]:
    """Build a Registry with the given ModelEntries and write it to disk."""
    reg = Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", auth=AuthConfig(type="none"))],
        models=list(entries),
    )
    path = tmp_path / "registry.toml"
    save_registry(reg, path)
    return reg, path


def _make_state() -> StateStore:
    return StateStore()


def _entry(*, id: str, family: str, provider: str, name: str, repo: str | None = None,
           files: list[str] | None = None) -> ModelEntry:
    """Build a ModelEntry from a legacy VariantSpec-shaped dict."""
    from modelman.registry import Fetch
    fetch = None
    if repo or files:
        fetch = Fetch(repo=repo, files=files, quantizations=None)
    return ModelEntry(
        id=id, family=family, provider_id=provider, model_name=name,
        fetch=fetch,
    )


def _variant(*, id: str, provider: str, name: str, repo: str | None = None,
             files: list[str] | None = None) -> dict:
    """The VariantSpec TypedDict the providers still consume."""
    return {
        "id": id, "provider": provider, "name": name,
        "repo": repo, "files": files, "quantizations": None,
    }


def _setup_apply_test(tmp_path: Path):
    """Standard fixture: registry with two ModelEntries, state with no
    downloads yet, two MagicMock providers. Returns (registry, state,
    registry_path, state_path, providers)."""
    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    a = _entry(id="ollama/a", family="f", provider="ollama", name="f:a")
    b = _entry(
        id="llamacpp/b", family="f", provider="llamacpp", name="f:b",
        repo="org/repo", files=["x.gguf"],
    )
    reg = Registry(
        providers=[
            ProviderEntry(id="ollama", name="Ollama", auth=AuthConfig(type="none")),
            ProviderEntry(id="llamacpp", name="llama.cpp", auth=AuthConfig(type="none")),
        ],
        models=[a, b],
    )
    save_registry(reg, reg_path)
    state = StateStore()

    provider_ollama = MagicMock()
    provider_ollama.name = "ollama"
    provider_ollama.delete.return_value = None
    provider_llama = MagicMock()
    provider_llama.name = "llamacpp"
    provider_llama.delete.return_value = None

    return reg, state, reg_path, state_path, {
        "ollama": provider_ollama,
        "llamacpp": provider_llama,
    }, a, b


# ---------------------------------------------------------------------------
# Tests
# ---------------------------------------------------------------------------


def test_apply_deletes_before_downloads(tmp_path):
    """On apply, delete steps must run before download steps (free disk first)."""
    reg, state, reg_path, state_path, providers, a, b = _setup_apply_test(tmp_path)
    order: list[str] = []

    providers["ollama"].download.return_value = str(tmp_path / "new-a")
    providers["llamacpp"].download.return_value = str(tmp_path / "new-b")

    def track_delete(variant):
        order.append(f"delete:{variant['id']}")

    def track_download(variant):
        order.append(f"download:{variant['id']}")
        return f"/tmp/new-{variant['id']}"

    providers["ollama"].delete.side_effect = track_delete
    providers["ollama"].download.side_effect = track_download
    providers["llamacpp"].delete.side_effect = track_delete
    providers["llamacpp"].download.side_effect = track_download

    pending = PendingChanges(
        registry=reg,
        state=state,
        family="f",
        registry_path=reg_path,
        state_path=state_path,
        providers=providers,
        deletes=[("ollama/a", _variant(id="ollama/a", provider="ollama", name="f:a"))],
        downloads=[("llamacpp/b", _variant(
            id="llamacpp/b", provider="llamacpp", name="f:b",
            repo="org/repo", files=["x.gguf"]))],
    )
    pending.apply()

    assert order.index("delete:ollama/a") < order.index("download:llamacpp/b")
    # Registry on disk no longer contains the deleted entry.
    reloaded = load_registry(reg_path)
    assert reloaded.model("llamacpp/b") is not None  # still present (was a download target)
    # Wait: the delete target was ollama/a, the download target was llamacpp/b.
    # After delete, ollama/a should be gone; after download, llamacpp/b stays.
    assert all(m.id != "ollama/a" for m in reloaded.models)
    # State recorded the download.
    assert state.models["llamacpp/b"].downloaded is True
    # Both files were written.
    assert reg_path.exists()
    assert state_path.exists()


def test_apply_collects_failures(tmp_path):
    reg, state, reg_path, state_path, providers, a, b = _setup_apply_test(tmp_path)
    providers["ollama"].download.side_effect = RuntimeError("network down")

    pending = PendingChanges(
        registry=reg,
        state=state,
        family="f",
        registry_path=reg_path,
        state_path=state_path,
        providers=providers,
        downloads=[("ollama/a", _variant(id="ollama/a", provider="ollama", name="f:a"))],
    )
    pending.apply()

    # Registry and state files were both written despite the failure (save runs unconditionally after the loop).
    assert reg_path.exists()
    assert state_path.exists()
    assert pending.failures
    assert "network down" in str(pending.failures[0])


def test_apply_empty_is_noop(tmp_path):
    reg, state, reg_path, state_path, providers, a, b = _setup_apply_test(tmp_path)
    pending = PendingChanges(
        registry=reg,
        state=state,
        family="f",
        registry_path=reg_path,
        state_path=state_path,
        providers=providers,
    )
    pending.apply()
    # No work, no writes.
    assert not reg_path.exists()
    assert not state_path.exists()


def test_apply_download_cancelled_is_not_a_failure(tmp_path):
    """When a provider raises DownloadCancelled mid-download, apply()
    must emit apply:cancelled, NOT record a failure and NOT save."""
    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    b = _entry(
        id="llamacpp/b", family="f", provider="llamacpp", name="f:b",
        repo="org/repo", files=["x.gguf"],
    )
    reg = Registry(
        providers=[ProviderEntry(id="llamacpp", name="llama.cpp", auth=AuthConfig(type="none"))],
        models=[b],
    )
    save_registry(reg, reg_path)
    state = StateStore()

    provider = MagicMock()
    provider.name = "llamacpp"
    provider.download.side_effect = DownloadCancelled("weights.bin")

    progress_lines: list[str] = []
    events: list[str] = []

    pending = PendingChanges(
        registry=reg,
        state=state,
        family="f",
        registry_path=reg_path,
        state_path=state_path,
        providers={"llamacpp": provider},
        downloads=[("llamacpp/b", _variant(
            id="llamacpp/b", provider="llamacpp", name="f:b",
            repo="org/repo", files=["x.gguf"]))],
    )
    pending.apply(on_event=events.append, on_progress=progress_lines.append)

    # Cancelled cleanly; not recorded as a failure.
    assert pending.failures == []
    assert any(tag.startswith("download:cancelled") for tag in events)
    assert "apply:cancelled" in events
    assert "apply:done" not in events
    # Neither file is saved on cancel.
    assert not reg_path.exists()
    assert not state_path.exists()


def test_apply_download_fail_includes_reason_in_event(tmp_path):
    """When a download raises, the fail event must include the exception
    so the StatusScreen can show WHY it failed."""
    reg, state, reg_path, state_path, providers, a, b = _setup_apply_test(tmp_path)
    providers["ollama"].download.side_effect = ConnectionError("dial tcp: i/o timeout")

    events: list[str] = []
    pending = PendingChanges(
        registry=reg,
        state=state,
        family="f",
        registry_path=reg_path,
        state_path=state_path,
        providers=providers,
        downloads=[("ollama/a", _variant(id="ollama/a", provider="ollama", name="f:a"))],
    )
    pending.apply(on_event=events.append)

    fail_events = [t for t in events if t.startswith("download:fail")]
    assert fail_events, f"expected a download:fail event; got {events}"
    fail = fail_events[0]
    parts = fail.split("|", 3)
    assert len(parts) == 4, f"expected 4 pipe-delimited fields; got {fail!r}"
    assert "i/o timeout" in parts[3]


def test_apply_save_fail_includes_reason_in_event(tmp_path):
    """On save failure, the event should carry the underlying error."""
    reg, state, reg_path, state_path, providers, a, b = _setup_apply_test(tmp_path)
    providers["ollama"].download.return_value = str(tmp_path / "downloaded-a-new")

    # Make the registry save raise.
    from modelman import registry as reg_mod

    orig = reg_mod.atomic_write_toml

    def boom(payload, path):
        raise OSError("disk full")

    reg_mod.atomic_write_toml = boom
    try:
        events: list[str] = []
        pending = PendingChanges(
            registry=reg,
            state=state,
            family="f",
            registry_path=reg_path,
            state_path=state_path,
            providers=providers,
            downloads=[("ollama/a", _variant(id="ollama/a", provider="ollama", name="f:a"))],
        )
        pending.apply(on_event=events.append)
    finally:
        reg_mod.atomic_write_toml = orig

    save_fail = [t for t in events if t.startswith("save:fail")]
    assert save_fail
    parts = save_fail[0].split("|", 3)
    assert parts[0] == "save:fail"
    assert len(parts) >= 2
    assert "disk full" in parts[1]


def test_apply_delete_fail_includes_reason_in_event(tmp_path):
    """Delete failures carry an exception reason in the event too."""
    reg, state, reg_path, state_path, providers, a, b = _setup_apply_test(tmp_path)
    providers["ollama"].delete.side_effect = PermissionError("read-only fs")

    events: list[str] = []
    pending = PendingChanges(
        registry=reg,
        state=state,
        family="f",
        registry_path=reg_path,
        state_path=state_path,
        providers=providers,
        deletes=[("ollama/a", _variant(id="ollama/a", provider="ollama", name="f:a"))],
    )
    pending.apply(on_event=events.append)

    fails = [t for t in events if t.startswith("delete:fail")]
    assert fails
    parts = fails[0].split("|", 3)
    assert len(parts) == 4
    assert "read-only fs" in parts[3]


def test_apply_download_done_includes_size_of_file(tmp_path):
    """The download:done event should carry the on-disk size of the
    downloaded file (e.g. "21.7 GB") so the StatusScreen can show
    concrete proof the download landed at the expected size."""
    reg, state, reg_path, state_path, providers, a, b = _setup_apply_test(tmp_path)
    real_path = tmp_path / "downloaded.bin"
    real_path.write_bytes(b"x" * (1536 * 1024 * 1024))  # ~ 1.5 GB
    providers["ollama"].download.return_value = str(real_path)

    events: list[str] = []
    pending = PendingChanges(
        registry=reg,
        state=state,
        family="f",
        registry_path=reg_path,
        state_path=state_path,
        providers=providers,
        downloads=[("ollama/a", _variant(id="ollama/a", provider="ollama", name="f:a"))],
    )
    pending.apply(on_event=events.append)

    done_events = [t for t in events if t.startswith("download:done")]
    assert done_events
    parts = done_events[0].split("|", 3)
    assert len(parts) == 4
    assert "1.5 GB" in parts[3], parts[3]


def test_apply_download_done_omits_size_when_zero_or_unreadable(tmp_path):
    """If stat-ing the path fails or returns 0, the 4th field is dropped."""
    reg, state, reg_path, state_path, providers, a, b = _setup_apply_test(tmp_path)
    providers["ollama"].download.return_value = str(tmp_path / "nope-does-not-exist.bin")

    events: list[str] = []
    pending = PendingChanges(
        registry=reg,
        state=state,
        family="f",
        registry_path=reg_path,
        state_path=state_path,
        providers=providers,
        downloads=[("ollama/a", _variant(id="ollama/a", provider="ollama", name="f:a"))],
    )
    pending.apply(on_event=events.append)

    done_events = [t for t in events if t.startswith("download:done")]
    assert done_events
    assert len(done_events[0].split("|", 3)) == 3, done_events[0]


def test_apply_writes_state_for_each_downloaded_model(tmp_path):
    """Each completed download records a ModelState with downloaded=True
    and disk_path set in the state store AND on disk after save."""
    reg, state, reg_path, state_path, providers, a, b = _setup_apply_test(tmp_path)
    providers["ollama"].download.return_value = str(tmp_path / "new-a")
    providers["llamacpp"].download.return_value = str(tmp_path / "new-b")

    pending = PendingChanges(
        registry=reg,
        state=state,
        family="f",
        registry_path=reg_path,
        state_path=state_path,
        providers=providers,
        downloads=[
            ("ollama/a", _variant(id="ollama/a", provider="ollama", name="f:a")),
            ("llamacpp/b", _variant(
                id="llamacpp/b", provider="llamacpp", name="f:b",
                repo="org/repo", files=["x.gguf"])),
        ],
    )
    pending.apply()

    # State on disk has both downloads recorded.
    reloaded_state = load_state(state_path)
    assert reloaded_state.get("ollama/a").downloaded is True
    assert reloaded_state.get("ollama/a").disk_path == str(tmp_path / "new-a")
    assert reloaded_state.get("llamacpp/b").downloaded is True
    assert reloaded_state.get("llamacpp/b").disk_path == str(tmp_path / "new-b")


def test_apply_removes_deleted_models_from_registry_on_disk(tmp_path):
    """Each successful delete removes the corresponding ModelEntry from
    the in-memory registry AND from the registry file after save."""
    reg, state, reg_path, state_path, providers, a, b = _setup_apply_test(tmp_path)

    pending = PendingChanges(
        registry=reg,
        state=state,
        family="f",
        registry_path=reg_path,
        state_path=state_path,
        providers=providers,
        deletes=[("ollama/a", _variant(id="ollama/a", provider="ollama", name="f:a"))],
    )
    pending.apply()

    reloaded = load_registry(reg_path)
    assert all(m.id != "ollama/a" for m in reloaded.models)
    # Surviving model is still there.
    assert any(m.id == "llamacpp/b" for m in reloaded.models)


def test_apply_clear_state_for_deleted_model(tmp_path):
    """A delete also clears any state entry the model had, so the
    modelman.toml doesn't carry a stale downloaded=True after the
    user removed the file."""
    reg, state, reg_path, state_path, providers, a, b = _setup_apply_test(tmp_path)
    # Pre-seed state: ollama/a was previously downloaded.
    state.set("ollama/a", ModelState(downloaded=True, disk_path="/old/path"))
    save_state(state, state_path)

    pending = PendingChanges(
        registry=reg,
        state=state,
        family="f",
        registry_path=reg_path,
        state_path=state_path,
        providers=providers,
        deletes=[("ollama/a", _variant(id="ollama/a", provider="ollama", name="f:a"))],
    )
    pending.apply()

    reloaded_state = load_state(state_path)
    assert "ollama/a" not in reloaded_state.models
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `uv run pytest tests/test_queue.py -v`
Expected: collection error — `ImportError: cannot import name 'PendingChanges'` from the new signature; test fixture helpers fail to import `Registry`/`StateStore`/`ModelEntry` (those exist but the test imports succeed; only the `PendingChanges` constructor signature mismatch breaks the tests).

- [ ] **Step 3: Implement the new `PendingChanges`**

Replace the entire contents of `src/modelman/queue.py` with:

```python
"""In-memory change queue applied on exit of the TUI model screen.

The on-disk targets are registry.toml (canonical model/provider definitions)
and modelman.toml (per-machine mutable state: download markers, family
display names). The legacy families/<family>.yaml manifest is no longer
written by the TUI; it survives as a migrate-time input only.
"""

from __future__ import annotations

import contextlib
from collections.abc import Callable
from dataclasses import dataclass, field
from pathlib import Path
from typing import TYPE_CHECKING

from .providers._progress import DownloadCancelled
from .registry import save_registry
from .state import ModelState, save_state

if TYPE_CHECKING:
    from .providers.base import VariantSpec
    from .registry import Registry
    from .state import StateStore


# Event tags fired via the optional on_event callback during apply(). The
# StatusScreen consumes these to render live progress. Format is unchanged
# from the legacy FamilyManifest-based implementation so StatusScreen can
# keep consuming pipe-delimited tags without modification:
#   "verb:status|vid|label" for per-item events,
#   "verb:status" for global events (save:*, apply:*),
#   "verb:status|vid|label|reason" for per-item failures,
#   "verb:status|reason" for global failures.
EventFn = Callable[[str], None]


def _label(variant: VariantSpec) -> str:
    """A short, human-readable label for a variant in progress logs.

    Falls back to the variant id if no name is set.
    """
    name = variant.get("name")
    return name if isinstance(name, str) and name else variant["id"]


def _reason(exc: BaseException) -> str:
    """First line of an exception, capped so a giant traceback doesn't
    drown the StatusScreen."""
    text = str(exc) or exc.__class__.__name__
    first = text.splitlines()[0] if text else ""
    if len(first) > 200:
        first = first[:197] + "…"
    return first


@dataclass
class PendingChanges:
    registry: Registry
    state: StateStore
    family: str
    registry_path: Path
    state_path: Path
    providers: dict[str, object]
    # Each queued item carries (model_id, VariantSpec). model_id is the
    # ModelEntry.id used to key Registry/StateStore mutations; VariantSpec
    # is what the provider APIs (download, size_of, delete) still consume.
    downloads: list[tuple[str, VariantSpec]] = field(default_factory=list)
    deletes: list[tuple[str, VariantSpec]] = field(default_factory=list)
    failures: list[str] = field(default_factory=list)
    cancelled: bool = False

    def cancel(self) -> None:
        """Request cancellation of an in-progress apply().

        Sets the flag the apply loop checks between steps, and terminates
        any provider that exposes a cancel_current() hook (e.g. Ollama's
        tracked subprocess). Safe to call from any thread.

        Already-completed steps are NOT undone. The current step, if any,
        is killed; remaining steps are not started.
        """
        self.cancelled = True
        for provider in self.providers.values():
            cancel = getattr(provider, "cancel_current", None)
            if callable(cancel):
                with contextlib.suppress(Exception):
                    cancel()

    def apply(
        self,
        on_event: EventFn | None = None,
        on_progress: EventFn | None = None,
    ) -> None:
        """Apply deletes first, then downloads, then save registry+state once.

        On failure of any single step, capture it in self.failures and continue
        with the remaining steps. If `on_event` is provided, it is called for
        each lifecycle transition (start/done/fail) and at apply:done.

        If `on_progress` is provided, it is forwarded to each provider's
        download method as a line-emitting callback.

        If `self.cancelled` is set, the loop stops between steps; already-
        completed steps remain applied, the registry/state are not saved,
        and the run ends with apply:cancelled.
        """

        def emit(tag: str) -> None:
            if on_event is not None:
                on_event(tag)

        def aborted() -> bool:
            if self.cancelled:
                emit("apply:cancelled")
                return True
            return False

        if not self.downloads and not self.deletes:
            emit("apply:done")
            return

        for model_id, variant in self.deletes:
            if aborted():
                return
            label = _label(variant)
            emit(f"delete:start|{model_id}|{label}")
            try:
                self._delete(variant)
            except Exception as exc:  # noqa: BLE001
                reason = _reason(exc)
                self.failures.append(f"delete {model_id}: {exc}")
                emit(f"delete:fail|{model_id}|{label}|{reason}")
                continue
            # Remove from in-memory registry.
            self.registry.models = [m for m in self.registry.models if m.id != model_id]
            # Clear any state entry so modelman.toml doesn't carry a
            # stale downloaded=True after the user removed the file.
            self.state.models.pop(model_id, None)
            emit(f"delete:done|{model_id}|{label}")

        for model_id, variant in self.downloads:
            if aborted():
                return
            label = _label(variant)
            emit(f"download:start|{model_id}|{label}")
            try:
                local_path = self._download(variant, on_progress)
            except DownloadCancelled:
                emit(f"download:cancelled|{model_id}|{label}")
                emit("apply:cancelled")
                return
            except Exception as exc:  # noqa: BLE001
                reason = _reason(exc)
                self.failures.append(f"download {model_id}: {exc}")
                emit(f"download:fail|{model_id}|{label}|{reason}")
                continue
            # Record download in state.
            existing = self.state.get(model_id)
            self.state.set(
                model_id,
                ModelState(
                    downloaded=True,
                    disk_path=local_path,
                    size_bytes=existing.size_bytes,
                    litellm_exposed=existing.litellm_exposed,
                ),
            )
            try:
                size = Path(local_path).stat().st_size
            except OSError:
                size = 0
            if size > 0:
                from .providers._progress import human_bytes
                emit(f"download:done|{model_id}|{label}|{human_bytes(size)}")
            else:
                emit(f"download:done|{model_id}|{label}")

        if aborted():
            return

        emit("save:start")
        try:
            save_registry(self.registry, self.registry_path)
            save_state(self.state, self.state_path)
            emit("save:done")
        except Exception as exc:  # noqa: BLE001
            reason = _reason(exc)
            self.failures.append(f"save: {exc}")
            emit(f"save:fail|{reason}")

        emit("apply:done")

    def _download(self, variant: VariantSpec, on_progress: EventFn | None = None) -> str:
        provider = self.providers[variant["provider"]]
        try:
            return provider.download(variant, on_progress=on_progress)  # type: ignore[attr-defined]
        except TypeError:
            return provider.download(variant)  # type: ignore[attr-defined]

    def _delete(self, variant: VariantSpec) -> None:
        provider = self.providers[variant["provider"]]
        if hasattr(provider, "delete"):
            provider.delete(variant)  # type: ignore[attr-defined]
            return
        # Fallback: providers without delete() just unlink the file.
        # Locate the local path from state (the legacy code read
        # self.manifest.downloaded[vid]["local_path"]; the equivalent
        # here is state.models[vid].disk_path).
        local_path = self.state.get(variant["id"]).disk_path
        if local_path:
            from pathlib import Path as _P
            import os
            import shutil

            p = _P(local_path)
            if p.is_file():
                p.unlink()
            elif p.is_dir() and not os.listdir(p):
                shutil.rmtree(p)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `uv run pytest tests/test_queue.py -v`
Expected: PASS (12 tests)

- [ ] **Step 5: Lint and commit**

```bash
uv run ruff check src/modelman/queue.py tests/test_queue.py
git add src/modelman/queue.py tests/test_queue.py \
  docs/superpowers/plans/2026-08-27-shared-model-registry-phase2-pr2.md
git commit -m "feat: rewrite PendingChanges on Registry/StateStore - completes plan item #1"
```

(Plan doc committed to git per the project's [[spec-and-plan-commit-override]] convention — bundled into the first implementation commit, matching Phase 1 / PR 1 precedent.)

---

### Task 2: Add `ModelEntry` variant helper in `screens/models.py`

**Files:**
- Modify: `src/modelman/screens/models.py` (top-of-file helper, before `class ModelScreen`)
- Test: `tests/screens/test_app_navigation.py` (one focused unit-style test, added inside the existing model-screen block)

**Interfaces:**
- Consumes: `Registry`/`ModelEntry`/`Fetch` (PR 1), `VariantSpec` TypedDict (existing — shape that `ModelForm` dismisses).
- Produces: `_variant_to_model_entry(variant: dict, family: str, registry: Registry) -> ModelEntry` — module-level function that builds the `ModelEntry` the screen appends to `registry.models` in `_on_add_model` and replaces in `_on_edit_model`. Pure transform; no side effects.

- [ ] **Step 1: Write the failing test**

Append a new test at the end of `tests/screens/test_app_navigation.py` (after `test_model_screen_starts_with_cursor_on_first_provider`):

```python
# ---------------------------------------------------------------------------
# _variant_to_model_entry adapter
# ---------------------------------------------------------------------------


def test_variant_to_model_entry_ollama_no_fetch():
    """Ollama tags produce a ModelEntry with fetch=None (ollama resolves
    the tag server-side; no HF repo / files)."""
    from modelman.screens.models import _variant_to_model_entry

    variant = {"id": "ollama/x:7b", "provider": "ollama", "name": "x:7b"}
    reg = Registry()
    entry = _variant_to_model_entry(variant, family="f", registry=reg)

    assert entry.id == "ollama/x:7b"
    assert entry.family == "f"
    assert entry.provider_id == "ollama"
    assert entry.model_name == "x:7b"
    assert entry.fetch is None


def test_variant_to_model_entry_llamacpp_with_repo_and_file():
    """llamacpp/omlx variants carry a Fetch with repo + single file."""
    from modelman.screens.models import _variant_to_model_entry

    variant = {
        "id": "llamacpp/o--r--x.gguf",
        "provider": "llamacpp",
        "name": "x.gguf",
        "repo": "o/r",
        "files": ["x.gguf"],
    }
    reg = Registry()
    entry = _variant_to_model_entry(variant, family="f", registry=reg)

    assert entry.id == "llamacpp/o--r--x.gguf"
    assert entry.provider_id == "llamacpp"
    assert entry.model_name == "x.gguf"
    assert entry.fetch is not None
    assert entry.fetch.repo == "o/r"
    assert entry.fetch.files == ["x.gguf"]


def test_variant_to_model_entry_edit_mode_preserves_id():
    """Editing a variant must keep the original id (id is the stable key
    for queued_downloads / registry lookup)."""
    from modelman.screens.models import _variant_to_model_entry

    variant = {
        "id": "llamacpp/old",
        "provider": "llamacpp",
        "name": "new.gguf",
        "repo": "o/r",
        "files": ["new.gguf"],
    }
    entry = _variant_to_model_entry(variant, family="f", registry=Registry())
    assert entry.id == "llamacpp/old"


def test_variant_to_model_entry_raises_for_unknown_provider():
    """A variant whose `provider` doesn't appear in registry.providers
    raises — caller must look up the provider entry to attribute the model."""
    from modelman.screens.models import _variant_to_model_entry

    variant = {"id": "bogus/x", "provider": "bogus", "name": "x"}
    reg = Registry(providers=[
        ProviderEntry(id="ollama", name="O", auth=AuthConfig(type="none")),
    ])
    with pytest.raises(KeyError):
        _variant_to_model_entry(variant, family="f", registry=reg)
```

Add these imports to the top of `tests/screens/test_app_navigation.py` (inside the existing imports — add the registry imports just below `from modelman.app import ModelmanApp`):

```python
from modelman.registry import (
    AuthConfig,
    ProviderEntry,
    Registry,
    save_registry,
)
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `uv run pytest tests/screens/test_app_navigation.py -k variant_to_model_entry -v`
Expected: FAIL with `ImportError: cannot import name '_variant_to_model_entry' from 'modelman.screens.models'`

- [ ] **Step 3: Implement the helper**

Insert this module-level function in `src/modelman/screens/models.py` directly after the imports, before `def _human_size`:

```python
def _variant_to_model_entry(variant: dict, *, family: str, registry: Registry) -> ModelEntry:
    """Convert a ModelForm VariantSpec-shaped dict to a ModelEntry.

    The dialog still emits the legacy TypedDict shape (provider, name,
    repo, files, model_info); the screen needs a ModelEntry to insert
    into registry.models. This adapter keeps the form simple and
    isolates the shape translation here.

    Edit mode preserves `variant["id"]` (the immutable key the user
    sees in the picker); add mode derives the same `provider/name`
    shape ModelForm produced. We don't need a separate "id derivation"
    step — the form already gave us one.
    """
    provider_id = variant["provider"]
    # Sanity: provider must exist in the registry. Defends against a
    # malformed dialog result that snuck past form validation.
    registry.provider(provider_id)  # raises KeyError if unknown

    name = variant.get("name") or variant["id"]
    repo = variant.get("repo")
    files = variant.get("files")
    fetch = None
    if repo or files:
        fetch = Fetch(repo=repo, files=files, quantizations=None)

    model_info = dict(variant.get("model_info") or {})
    return ModelEntry(
        id=variant["id"],
        family=family,
        provider_id=provider_id,
        model_name=name,
        model_info=model_info,
        fetch=fetch,
    )
```

Add these imports at the top of `screens/models.py` (alongside the existing TYPE_CHECKING block — these need to be runtime imports now, not TYPE_CHECKING, since the helper is called at runtime):

```python
from ..registry import Fetch, ModelEntry, Registry
```

(Remove the matching TYPE_CHECKING entries for `FamilyManifest` and `VariantSpec` that this replaces — see Task 3.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `uv run pytest tests/screens/test_app_navigation.py -k variant_to_model_entry -v`
Expected: PASS (4 tests)

- [ ] **Step 5: Lint and commit**

```bash
uv run ruff check src/modelman/screens/models.py tests/screens/test_app_navigation.py
git add src/modelman/screens/models.py tests/screens/test_app_navigation.py
git commit -m "feat: add VariantSpec-to-ModelEntry adapter - completes plan item #2"
```

---

### Task 3: Rewrite `ModelScreen` ctor + mutate registry/state

**Files:**
- Modify: `src/modelman/screens/models.py` (`__init__`, `_run_reconcile`, `reload`, `_load_models_for_provider`, `_is_downloaded`, `action_toggle_download`, `_on_add_model`, `_on_edit_model`, `action_delete_model`, `_run_apply`, `_restore_snapshot`)
- Test: `tests/screens/test_app_navigation.py` (the model-screen portions — see "Tests to migrate" at the bottom of this task)

**Interfaces:**
- Consumes: `Registry`/`StateStore`/`ModelEntry`/`Fetch` (PR 1), `provider_config` (PR 1), `save_registry`/`save_state` (PR 1), `_variant_to_model_entry` (Task 2).
- Produces:
  ```python
  class ModelScreen(Screen[None]):
      def __init__(
          self,
          registry: Registry,
          state: StateStore,
          family: str,
          registry_path: Path,
          state_path: Path,
          available_providers: list[str] | None = None,
      ) -> None: ...
      self.registry: Registry
      self.state: StateStore
      self.family: str
      self.registry_path: Path
      self.state_path: Path
      # queued_downloads / queued_deletes now hold (model_id, VariantSpec)
      self.queued_downloads: dict[str, VariantSpec] = {}
      self.queued_deletes: dict[str, VariantSpec] = {}
      self.reconciled: dict[str, dict] = {}
      # Snapshots for discard (deep copies of registry.models filtered to family
      # + state.models entries for the family).
      self._snapshot_models: list[ModelEntry]
      self._snapshot_state_entries: dict[str, ModelState]
  ```

  `FamilyScreen`'s call sites at `screens/families.py:339` and `:349` are NOT updated in this PR (per the PR 1 plan: FamilyScreen migrates wholesale in PR 3). After this Task, `ModelScreen` no longer accepts `(FamilyManifest, Path, providers)`, which means those two `FamilyScreen` call sites and `app.py:46` will be broken at runtime — but they are intentionally left for PR 3 to fix when the screen reads from registry.toml instead.

  For tests, use a helper that constructs `ModelScreen(registry, state, family, registry_path, state_path, providers)` directly (bypassing `FamilyScreen`), exactly like the existing `test_model_screen_*` tests already construct `ModelScreen(...)` directly (see `test_app_navigation.py:1101`, `:1139`, `:1185`, `:1246`). Those tests don't go through `FamilyScreen` and so don't need migration beyond updating the ctor call.

  For tests that DO go through `ModelmanApp()` (e.g. `test_enter_opens_model_screen`, `test_toggle_download_queues_variant`, etc.), `app.py` still constructs the legacy ctor. In this PR, we extend `app.py`'s `ModelmanApp(family=...)` path to ALSO accept registry/state kwargs and a path for the family that *is* the current PR 2 surface — OR, simpler, we keep `app.py` using the legacy ctor and accept that those integration tests break in this PR. Per the PR 1 plan: `app.py` keeps the legacy ctor until PR 3. **The pragmatic decision**: keep the integration tests (`test_enter_opens_model_screen` etc.) broken in this PR — they'll be rewritten in PR 3 alongside `FamilyScreen`. The model-screen-only unit-style tests (which construct `ModelScreen(...)` directly without going through `app.py`) get migrated now.

  This narrows the scope of "tests to migrate in PR 2" to:
     - The four `test_model_screen_*` unit-style tests that construct `ModelScreen(...)` directly
     - Plus a focused unit-test for `ModelScreen.action_toggle_download` constructed directly (new)

  Tests that go through `app.py`'s legacy ctor (e.g. `test_enter_opens_model_screen`, `test_toggle_download_queues_variant`, `test_add_then_delete_model_queues_changes`, `test_reconcile_*`, `test_apply_*`, `test_escape_*`, `test_discard_*`, `test_family_screen_reconciles_on_resume_after_apply`, `test_enter_on_model_row_opens_edit_dialog`, `test_enter_on_provider_row_does_not_open_edit_dialog`) STAY ON `FamilyManifest` in this PR and migrate in PR 3. They will fail to collect after this Task lands (because `app.py` still calls the legacy ctor) — that's expected and is part of the agreed PR 3 migration.

  Concretely, `app.py` and `FamilyScreen` keep the legacy `ModelScreen(FamilyManifest, path, providers)` ctor through PR 2. The new `ModelScreen(registry, state, family, registry_path, state_path, providers)` ctor is added alongside. We achieve this by adding the new ctor and leaving the old one gone (since nothing but `FamilyScreen` and `app.py` calls it, and those are broken in PR 3 anyway — keeping a parallel legacy ctor would be dead code).

  The four `test_model_screen_*` tests construct `ModelScreen(...)` directly with the new kwargs; the integration tests stay broken until PR 3.

- [ ] **Step 1: Write the failing tests**

Append the new and migrated unit-style tests to `tests/screens/test_app_navigation.py` (after `test_variant_to_model_entry_raises_for_unknown_provider`):

```python
# ---------------------------------------------------------------------------
# ModelScreen constructed directly (no FamilyScreen / app.py round-trip).
# PR 3 migrates the integration tests that go through FamilyScreen.
# ---------------------------------------------------------------------------


def _make_screen(tmp_path, monkeypatch, *, family: str = "ornith", entries=()):
    """Build a ModelScreen with registry.toml + modelman.toml in tmp_path
    and seed registry with the given ModelEntries. Returns
    (ms, registry_path, state_path)."""
    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    reg = Registry(
        providers=[
            ProviderEntry(id="ollama", name="O", auth=AuthConfig(type="none")),
            ProviderEntry(id="llamacpp", name="L", auth=AuthConfig(type="none")),
            ProviderEntry(id="omlx", name="X", auth=AuthConfig(type="omlx")),
        ],
        models=list(entries),
    )
    save_registry(reg, reg_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    monkeypatch.setenv("MODELMAN_CONFIG", str(tmp_path / "config.yaml"))
    (tmp_path / "config.yaml").write_text(
        "providers:\n"
        "  ollama: {type: ollama}\n"
        "  llamacpp: {type: llamacpp}\n"
        "  omlx: {type: omlx}\n"
    )

    from modelman.screens.models import ModelScreen

    ms = ModelScreen(
        registry=reg,
        state=StateStore(),
        family=family,
        registry_path=reg_path,
        state_path=state_path,
        available_providers=["ollama", "llamacpp", "omlx"],
    )
    return ms, reg_path, state_path


def test_model_screen_shows_all_providers_for_empty_family(
    tmp_path, monkeypatch,
):
    """The provider-table on the left of the model screen must show
    every configured provider, even when the family has zero entries."""
    from textual.widgets import DataTable

    ms, _reg, _state = _make_screen(tmp_path, monkeypatch)

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        pilot.app.push_screen(ms)
        await pilot.pause()
        pt = ms.query_one("#provider-table", DataTable)
        keys = sorted(str(rk.value) for rk in pt.rows)
        assert keys == ["llamacpp", "ollama", "omlx"], (
            f"provider table should list every configured provider "
            f"(got {keys}); an empty family must not hide them"
        )


def test_model_screen_provider_table_count_zero_for_empty(
    tmp_path, monkeypatch,
):
    """When the family has 0 entries, each provider row should show
    count '0' (not blank)."""
    from textual.widgets import DataTable

    from modelman.app import ModelmanApp
    from modelman.screens.models import ModelScreen

    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    reg = Registry(
        providers=[
            ProviderEntry(id="ollama", name="O", auth=AuthConfig(type="none")),
            ProviderEntry(id="llamacpp", name="L", auth=AuthConfig(type="none")),
            ProviderEntry(id="omlx", name="X", auth=AuthConfig(type="omlx")),
        ],
        models=[],
    )
    save_registry(reg, reg_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    monkeypatch.setenv("MODELMAN_CONFIG", str(tmp_path / "config.yaml"))
    (tmp_path / "config.yaml").write_text(
        "providers:\n"
        "  ollama: {type: ollama}\n"
        "  llamacpp: {type: llamacpp}\n"
        "  omlx: {type: omlx}\n"
    )

    ms = ModelScreen(
        registry=reg,
        state=StateStore(),
        family="x",
        registry_path=reg_path,
        state_path=state_path,
        available_providers=["ollama", "llamacpp", "omlx"],
    )
    app = ModelmanApp()
    async with app.run_test() as pilot:
        pilot.app.push_screen(ms)
        await pilot.pause()
        pt = ms.query_one("#provider-table", DataTable)
        provider_cells = list(pt.get_column_at(0))
        count_cells = [str(c) for c in pt.get_column_at(1)]
        assert sorted(str(c) for c in provider_cells) == [
            "llamacpp",
            "ollama",
            "omlx",
        ], provider_cells
        for c in count_cells:
            assert c == "0", f"empty family: count column must be 0, got {c!r}"


def test_model_screen_add_form_offers_all_providers_for_empty_family(
    tmp_path, monkeypatch,
):
    """The AddModel form's provider Label must reflect the full
    configured-provider list when the user presses 'a' from an empty
    family's model screen."""
    from modelman.app import ModelmanApp
    from modelman.screens.forms import ModelForm

    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    reg = Registry(
        providers=[
            ProviderEntry(id="ollama", name="O", auth=AuthConfig(type="none")),
            ProviderEntry(id="llamacpp", name="L", auth=AuthConfig(type="none")),
            ProviderEntry(id="omlx", name="X", auth=AuthConfig(type="omlx")),
        ],
        models=[],
    )
    save_registry(reg, reg_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    monkeypatch.setenv("MODELMAN_CONFIG", str(tmp_path / "config.yaml"))
    (tmp_path / "config.yaml").write_text(
        "providers:\n"
        "  ollama: {type: ollama}\n"
        "  llamacpp: {type: llamacpp}\n"
        "  omlx: {type: omlx}\n"
    )

    from modelman.screens.models import ModelScreen

    ms = ModelScreen(
        registry=reg,
        state=StateStore(),
        family="x",
        registry_path=reg_path,
        state_path=state_path,
        available_providers=["ollama", "llamacpp", "omlx"],
    )
    app = ModelmanApp()
    async with app.run_test() as pilot:
        pilot.app.push_screen(ms)
        await pilot.pause()
        captured: list[ModelForm] = []
        original_push = ms.app.push_screen

        def tracking_push(screen, *args, **kwargs):
            if isinstance(screen, ModelForm):
                captured.append(screen)
            return original_push(screen, *args, **kwargs)

        ms.app.push_screen = tracking_push
        ms.action_add_model()
        await pilot.pause()

    assert len(captured) == 1, f"expected ModelForm push; got {captured}"
    assert captured[0]._initial_provider == "ollama"


def test_model_screen_starts_with_cursor_on_first_provider(
    tmp_path, monkeypatch,
):
    """When the model screen mounts, the cursor must be on row 0 of
    the provider-table (the first configured provider)."""
    from textual.widgets import DataTable

    ms, _reg, _state = _make_screen(tmp_path, monkeypatch)

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        pilot.app.push_screen(ms)
        await pilot.pause()
        pt = ms.query_one("#provider-table", DataTable)
        assert pt.cursor_coordinate.row == 0
        assert pt.cursor_coordinate.column == 0


@pytest.mark.asyncio
async def test_model_screen_add_appends_model_entry_to_registry(
    tmp_path, monkeypatch,
):
    """Submitting ModelForm in add mode appends a ModelEntry to
    registry.models with the adapter's translation."""
    from textual.widgets import Input

    ms, reg_path, _state = _make_screen(tmp_path, monkeypatch)

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        pilot.app.push_screen(ms)
        await pilot.pause()
        await pilot.press("a")
        await pilot.pause()
        app.screen.query_one("#model", Input).focus()
        for ch in "ornith:8b":
            await pilot.press(ch)
        await pilot.press("enter")
        await pilot.pause()

    reloaded = Registry()
    from modelman.registry import load_registry
    reloaded = load_registry(reg_path)
    ids = [m.id for m in reloaded.models]
    assert "ollama/ornith:8b" in ids
    added = next(m for m in reloaded.models if m.id == "ollama/ornith:8b")
    assert added.family == ms.family
    assert added.provider_id == "ollama"
    assert added.model_name == "ornith:8b"
    assert added.fetch is None


@pytest.mark.asyncio
async def test_model_screen_toggle_download_queues_variant(
    tmp_path, monkeypatch,
):
    """Pressing `x` on a not-downloaded row queues the variant for download."""
    from unittest.mock import MagicMock

    from modelman.providers import registry as prov_registry

    a = ModelEntry(
        id="ollama/o35", family="ornith", provider_id="ollama", model_name="ornith:35b",
    )
    ms, _reg, _state = _make_screen(tmp_path, monkeypatch, entries=[a])

    stub = MagicMock()
    stub.name = "ollama"
    stub.size_of.return_value = None
    monkeypatch.setattr(
        prov_registry.ProviderRegistry,
        "get",
        staticmethod(lambda name, cfg: stub),
    )

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        pilot.app.push_screen(ms)
        await pilot.pause()
        await pilot.press("x")
        await pilot.pause()
        assert "ollama/o35" in ms.queued_downloads


@pytest.mark.asyncio
async def test_model_screen_delete_only_for_downloaded(tmp_path, monkeypatch):
    """Pressing `d` on a not-downloaded row is a no-op (delete target
    must actually exist on disk before we remove it)."""
    from unittest.mock import MagicMock

    from modelman.providers import registry as prov_registry

    a = ModelEntry(
        id="ollama/o35", family="ornith", provider_id="ollama", model_name="ornith:35b",
    )
    ms, _reg, _state = _make_screen(tmp_path, monkeypatch, entries=[a])

    stub = MagicMock()
    stub.name = "ollama"
    stub.size_of.return_value = None
    monkeypatch.setattr(
        prov_registry.ProviderRegistry,
        "get",
        staticmethod(lambda name, cfg: stub),
    )

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        pilot.app.push_screen(ms)
        await pilot.pause()
        await pilot.press("d")
        await pilot.pause()
        assert ms.queued_deletes == {}
```

Also delete the existing legacy-ctor versions of the four `test_model_screen_*` tests now (they exist at `test_app_navigation.py:1069`, `:1118`, `:1162`, `:1215` in the original file; the new tests above replace them):

```python
# Delete the four legacy `test_model_screen_*` unit-style tests at lines
# 1069-1262 — they're replaced by the registry-based versions above.
```

Add these imports to the top of `tests/screens/test_app_navigation.py`:

```python
from modelman.state import ModelState, StateStore
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `uv run pytest tests/screens/test_app_navigation.py -k model_screen -v`
Expected: collection error — `TypeError: ModelScreen.__init__() got multiple values for keyword argument 'family'` (the legacy ctor has `family` as a 2nd positional; the new tests pass it as kwarg).

- [ ] **Step 3: Implement the new `ModelScreen`**

Replace the body of `src/modelman/screens/models.py` with the file below. The helper from Task 2 stays at the top; everything from `_human_size` onward is the new ModelScreen implementation:

```python
"""ModelScreen — drill into a family's models grouped by provider."""

from __future__ import annotations

from collections import defaultdict
from collections.abc import Callable
from pathlib import Path
from typing import TYPE_CHECKING

from textual.app import ComposeResult
from textual.binding import Binding
from textual.containers import Horizontal, Vertical
from textual.coordinate import Coordinate
from textual.screen import Screen
from textual.widgets import DataTable, Footer, Header, Static

from ..registry import Fetch, ModelEntry, Registry, provider_config
from ..queue import PendingChanges
from ..state import ModelState, StateStore

if TYPE_CHECKING:
    from ..providers.base import VariantSpec


def _human_size(n) -> str:
    if n is None:
        return "—"
    if n < 1024:
        return f"{n} B"
    for unit in ("KB", "MB", "GB", "TB"):
        n /= 1024
        if n < 1024:
            return f"{n:.1f} {unit}"
    return f"{n:.1f} PB"


def _entry_kwargs(m: ModelEntry) -> dict:
    """Dataclass-as-dict for ModelEntry so snapshot copies don't share
    nested Fetch/Cost objects with the live registry."""
    from dataclasses import asdict
    return asdict(m)


def _state_kwargs(s: ModelState) -> dict:
    from dataclasses import asdict
    return asdict(s)


def _model_entry_to_variant(entry: ModelEntry) -> dict:
    """Build a VariantSpec-shaped dict from a ModelEntry for provider APIs.

    Providers still consume the legacy TypedDict (provider, name, repo,
    files, model_info). ModelEntry stores repo/files in `fetch`. We
    don't carry `model_info` from the registry into the provider call
    (providers read what they need from their own state).
    """
    repo = entry.fetch.repo if entry.fetch else None
    files = entry.fetch.files if entry.fetch else None
    return {
        "id": entry.id,
        "provider": entry.provider_id,
        "name": entry.model_name,
        "repo": repo,
        "files": files,
        "quantizations": None,
        "model_info": dict(entry.model_info),
    }


class ModelScreen(Screen[None]):
    BINDINGS = [
        ("escape", "back", "Back"),
        ("x", "toggle_download", "Toggle download"),
        ("a", "add_model", "Add"),
        ("d", "delete_model", "Delete"),
        ("e", "edit_model", "Edit"),
        Binding("enter", "select_row", "Edit", priority=True),
        ("r", "reconcile", "Reconcile"),
    ]

    def __init__(
        self,
        registry: Registry,
        state: StateStore,
        family: str,
        registry_path: Path,
        state_path: Path,
        available_providers: list[str] | None = None,
    ) -> None:
        super().__init__()
        self.registry = registry
        self.state = state
        self.family = family
        self.registry_path = registry_path
        self.state_path = state_path
        if available_providers is not None:
            self.available_providers = list(available_providers)
        else:
            self.available_providers = ["ollama", "llamacpp", "omlx"]
        if "ollama" in self.available_providers:
            self.selected_provider: str | None = "ollama"
        elif self.available_providers:
            self.selected_provider = self.available_providers[0]
        else:
            self.selected_provider = None
        # queued_downloads / queued_deletes map model_id -> VariantSpec
        # (the VariantSpec is what provider APIs still consume; model_id
        # is the registry/state key).
        self.queued_downloads: dict[str, VariantSpec] = {}
        self.queued_deletes: dict[str, VariantSpec] = {}
        # Reconcile overlay: per-model-id reality from the provider.
        self.reconciled: dict[str, dict] = {}
        # Snapshot for discard: registry.models entries scoped to this
        # family + matching state.models entries.
        self._snapshot_models: list[ModelEntry] = [
            ModelEntry(**_entry_kwargs(m)) for m in registry.models if m.family == family
        ]
        self._snapshot_state_entries: dict[str, ModelState] = {
            mid: ModelState(**_state_kwargs(s))
            for mid, s in state.models.items()
            if any(m.id == mid and m.family == family for m in registry.models)
        }


    (Continue with the methods. The methods `compose`, `on_mount`, `_run_reconcile`, `action_reconcile`, `reload`, `on_data_table_row_highlighted`, `_is_downloaded`, `_load_models_for_provider`, `_refresh_pending_bar`, `action_toggle_download`, `_provider_list`, `action_add_model`, `_on_add_model`, `action_delete_model`, `action_edit_model`, `on_data_table_row_selected`, `action_select_row`, `_on_edit_model`, `action_back`, `_on_exit_confirm`, `_push_status_screen`, `_run_apply`, `_restore_snapshot` all get rewritten to use `self.registry`/`self.state`/`self.family` instead of `self.manifest`. Below is the full rewritten body for those methods.)

```python
    def compose(self) -> ComposeResult:
        yield Header()
        with Horizontal(id="panes"):
            with Vertical(id="provider-pane"):
                yield DataTable(id="provider-table", cursor_type="row")
            with Vertical(id="model-pane"):
                yield DataTable(id="model-table", cursor_type="row")
        yield Static("Pending: download 0 · delete 0", id="pending-bar")
        yield Footer()

    def on_mount(self) -> None:
        pt = self.query_one("#provider-table", DataTable)
        pt.add_columns("PROVIDER", "MODELS")
        mt = self.query_one("#model-table", DataTable)
        mt.add_columns("NAME", "STATUS", "SIZE", "PATH")
        self.reload()
        self._refresh_pending_bar()
        if pt.row_count > 0:
            pt.cursor_coordinate = Coordinate(0, 0)
        pt.focus()
        self.run_worker(self._run_reconcile, exclusive=True, thread=True)

    def _run_reconcile(self) -> None:
        """Ask each provider whether its models are on disk; cache results."""
        from ..providers.registry import ProviderRegistry

        family_models = self.registry.models_by_family(self.family)
        by_provider: dict[str, list[ModelEntry]] = defaultdict(list)
        for m in family_models:
            by_provider[m.provider_id].append(m)
        for provider_name, entries in by_provider.items():
            try:
                entry = self.registry.provider(provider_name)
                provider = ProviderRegistry.get(provider_name, provider_config(entry))
            except Exception:
                continue
            for m in entries:
                size: int | None = None
                # Providers consume VariantSpec; build a minimal one
                # from the ModelEntry's stored Fetch.
                spec = _model_entry_to_variant(m)
                try:
                    raw = provider.size_of(spec)  # type: ignore[attr-defined]
                    if isinstance(raw, int):
                        size = raw
                except Exception:
                    size = None
                downloaded = size is not None
                local_path: str | None = None
                if downloaded and hasattr(provider, "list_local"):
                    try:
                        for lm in provider.list_local():
                            lm_name = lm.get("name") or lm.get("variant_id")
                            if lm_name == m.model_name or lm_name == m.id:
                                lp = lm.get("local_path") or lm.get("path")
                                if isinstance(lp, str):
                                    local_path = lp
                                break
                    except Exception:
                        pass
                self.reconciled[m.id] = {
                    "downloaded": downloaded,
                    "size": size,
                    "local_path": local_path,
                }
        self.app.call_from_thread(self.reload)

    def action_reconcile(self) -> None:
        self.run_worker(self._run_reconcile, exclusive=True, thread=True)

    def reload(self) -> None:
        pt = self.query_one("#provider-table", DataTable)
        pt.clear()
        counts: dict[str, int] = defaultdict(int)
        for m in self.registry.models_by_family(self.family):
            counts[m.provider_id] += 1
        for provider in self.available_providers:
            pt.add_row(provider, str(counts.get(provider, 0)), key=provider)
        if self.selected_provider not in self.available_providers and self.available_providers:
            self.selected_provider = self.available_providers[0]
        if self.selected_provider:
            self._load_models_for_provider(self.selected_provider)

    def on_data_table_row_highlighted(self, event: DataTable.RowHighlighted) -> None:
        if event.control.id == "provider-table":
            row_key = event.row_key
            if row_key is not None:
                self.selected_provider = str(row_key.value)
                self._load_models_for_provider(self.selected_provider)

    def _is_downloaded(self, model_id: str) -> bool:
        """Truth about whether a model is on disk.

        Prefers the reconcile overlay (reality); falls back to state
        when reconcile hasn't run for this model yet.
        """
        rec = self.reconciled.get(model_id)
        if rec is not None:
            return bool(rec.get("downloaded"))
        return self.state.get(model_id).downloaded

    def _load_models_for_provider(self, provider: str) -> None:
        from ..providers.registry import ProviderRegistry

        mt = self.query_one("#model-table", DataTable)
        mt.clear()
        for m in self.registry.models_by_family(self.family):
            if m.provider_id != provider:
                continue
            rec = self.reconciled.get(m.id)
            if rec is not None:
                downloaded = bool(rec.get("downloaded"))
                size_str = _human_size(rec.get("size")) if downloaded else "—"
                path = rec.get("local_path") or (
                    self.state.get(m.id).disk_path or "—"
                )
            else:
                state_entry = self.state.get(m.id)
                downloaded = state_entry.downloaded
                size_str = "—"
                path = state_entry.disk_path or "—"
                if downloaded:
                    try:
                        entry = self.registry.provider(provider)
                        prov = ProviderRegistry.get(provider, provider_config(entry))
                        size_str = _human_size(prov.size_of(_model_entry_to_variant(m)))
                    except Exception:
                        pass
            if m.id in self.queued_deletes:
                status = "[red]✗[/red]"
            elif m.id in self.queued_downloads:
                status = "[yellow]↓[/yellow]"
            elif downloaded:
                status = "[green]✓[/green]"
            else:
                status = "[dim]○[/dim]"
            mt.add_row(m.model_name, status, size_str, path, key=m.id)

    def _refresh_pending_bar(self) -> None:
        bar = self.query_one("#pending-bar", Static)
        bar.update(
            f"Pending: download {len(self.queued_downloads)} · delete {len(self.queued_deletes)}"
        )

    def action_toggle_download(self) -> None:
        mt = self.query_one("#model-table", DataTable)
        if mt.row_count == 0:
            return
        row_key = list(mt.rows.keys())[mt.cursor_row]
        mid = str(row_key.value)
        entry = next((m for m in self.registry.models if m.id == mid), None)
        if entry is None:
            return
        if self._is_downloaded(mid):
            return  # already downloaded
        spec = _model_entry_to_variant(entry)
        if mid in self.queued_downloads:
            self.queued_downloads.pop(mid)
        else:
            self.queued_downloads[mid] = spec
        self._refresh_pending_bar()
        if self.selected_provider is not None:
            self._load_models_for_provider(self.selected_provider)

    def _provider_list(self) -> list[str]:
        if self.available_providers:
            return list(self.available_providers)
        return sorted({m.provider_id for m in self.registry.models_by_family(self.family)})

    def action_add_model(self) -> None:
        from .forms import ModelForm

        providers = self._provider_list() or ["ollama", "llamacpp", "omlx"]
        default_provider = (
            self.selected_provider
            if self.selected_provider in providers
            else None
        )
        self.app.push_screen(
            ModelForm(providers=providers, default_provider=default_provider),
            self._on_add_model,
        )

    def _on_add_model(self, variant) -> None:
        if variant is None:
            return
        if any(m.id == variant["id"] for m in self.registry.models):
            self.app.notify("Model ID already exists")
            return
        entry = _variant_to_model_entry(
            variant, family=self.family, registry=self.registry
        )
        self.registry.models.append(entry)
        self.queued_downloads[variant["id"]] = variant
        self.reload()
        self._refresh_pending_bar()

    def action_delete_model(self) -> None:
        mt = self.query_one("#model-table", DataTable)
        if mt.row_count == 0:
            return
        row_key = list(mt.rows.keys())[mt.cursor_row]
        mid = str(row_key.value)
        entry = next((m for m in self.registry.models if m.id == mid), None)
        if entry is None:
            return
        if not self._is_downloaded(mid):
            return
        spec = _model_entry_to_variant(entry)
        if mid in self.queued_deletes:
            self.queued_deletes.pop(mid)
        else:
            self.queued_deletes[mid] = spec
        self.queued_downloads.pop(mid, None)
        self._refresh_pending_bar()
        if self.selected_provider is not None:
            self._load_models_for_provider(self.selected_provider)

    def action_edit_model(self) -> None:
        mt = self.query_one("#model-table", DataTable)
        if mt.row_count == 0:
            return
        if mt.cursor_row >= mt.row_count:
            return
        row_key = list(mt.rows.keys())[mt.cursor_row]
        mid = str(row_key.value)
        entry = next((m for m in self.registry.models if m.id == mid), None)
        if entry is None:
            return
        from .forms import ModelForm

        spec = _model_entry_to_variant(entry)
        self.app.push_screen(
            ModelForm(providers=self._provider_list(), variant=spec),
            self._on_edit_model,
        )

    def on_data_table_row_selected(self, event: DataTable.RowSelected) -> None:
        if event.data_table.id == "model-table":
            self.action_edit_model()

    def action_select_row(self) -> None:
        try:
            mt = self.query_one("#model-table", DataTable)
            pt = self.query_one("#provider-table", DataTable)
        except Exception:
            return
        if mt.has_focus:
            self.action_edit_model()
        elif pt.has_focus:
            return

    def _on_edit_model(self, updated) -> None:
        if updated is None:
            return
        new_entry = _variant_to_model_entry(
            updated, family=self.family, registry=self.registry
        )
        for i, m in enumerate(self.registry.models):
            if m.id == updated["id"]:
                self.registry.models[i] = new_entry
                break
        if updated["id"] in self.queued_downloads:
            self.queued_downloads[updated["id"]] = updated
        self.reload()

    def action_back(self) -> None:
        if not self.queued_downloads and not self.queued_deletes:
            self.app.pop_screen()
            return
        from .forms import ConfirmExitDialog

        self.app.push_screen(
            ConfirmExitDialog(
                downloads=list(self.queued_downloads.values()),
                deletes=list(self.queued_deletes.values()),
            ),
            self._on_exit_confirm,
        )

    def _on_exit_confirm(self, choice: str | None) -> None:
        if choice == "apply":
            self._push_status_screen()
            return
        if choice == "discard":
            self._restore_snapshot()
            self.app.pop_screen()
            return
        return

    def _push_status_screen(self) -> None:
        from .status import StatusScreen

        self.app.pop_screen()
        self.app.push_screen(StatusScreen(family=self.family, run_apply=self._run_apply))

    def _run_apply(
        self,
        on_event: Callable[[str], None],
        on_progress: Callable[[str], None],
        register: Callable[[PendingChanges], None],
    ) -> None:
        from ..providers.registry import ProviderRegistry

        providers: dict[str, object] = {}
        for spec in list(self.queued_downloads.values()) + list(self.queued_deletes.values()):
            try:
                entry = self.registry.provider(spec["provider"])
                providers[spec["provider"]] = ProviderRegistry.get(
                    spec["provider"], provider_config(entry)
                )
            except Exception:
                continue
        # Merge any reconciled entries that state didn't know about,
        # so the saved state reflects reality. Never remove existing
        # entries; the user can queue a delete (d) for that.
        for mid, rec in self.reconciled.items():
            if rec.get("downloaded") and not self.state.get(mid).downloaded:
                self.state.set(
                    mid,
                    ModelState(
                        downloaded=True,
                        disk_path=rec.get("local_path") or "",
                    ),
                )
        pending = PendingChanges(
            registry=self.registry,
            state=self.state,
            family=self.family,
            registry_path=self.registry_path,
            state_path=self.state_path,
            providers=providers,
            downloads=[(mid, spec) for mid, spec in self.queued_downloads.items()],
            deletes=[(mid, spec) for mid, spec in self.queued_deletes.items()],
        )
        register(pending)
        pending.apply(on_event=on_event, on_progress=on_progress)
        self.queued_downloads.clear()
        self.queued_deletes.clear()

    def _restore_snapshot(self) -> None:
        """Restore the in-memory registry/state to the snapshot taken on
        mount, dropping any queued mutations."""
        # Replace this family's models in registry with the snapshot.
        keep = [m for m in self.registry.models if m.family != self.family]
        self.registry.models = keep + self._snapshot_models
        # Replace state entries that were in the snapshot.
        for mid in self._snapshot_state_entries:
            self.state.set(mid, self._snapshot_state_entries[mid])
        for mid in list(self.state.models):
            if (
                mid in self.state.models
                and mid not in self._snapshot_state_entries
                and any(m.id == mid and m.family == self.family for m in self.registry.models)
            ):
                self.state.models.pop(mid, None)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `uv run pytest tests/screens/test_app_navigation.py -k "model_screen or variant_to_model_entry" -v`
Expected: PASS (10 tests: 4 adapter + 4 model_screen_shows/providers/cursor/add + 2 toggle/delete). The 4 legacy `test_model_screen_*` tests in the original file are deleted, so they don't run.

- [ ] **Step 5: Lint and commit**

```bash
uv run ruff check src/modelman/screens/models.py tests/screens/test_app_navigation.py
git add src/modelman/screens/models.py tests/screens/test_app_navigation.py
git commit -m "feat: rewrite ModelScreen on Registry/StateStore - completes plan item #3"
```

---

### Task 4: Update `app.py` to construct `ModelScreen` with the new ctor

**Files:**
- Modify: `src/modelman/app.py` (`on_mount` lines 38-46)
- Test: `tests/screens/test_app_navigation.py` (the `test_app_with_initial_family_launches_into_model_screen` integration test at line 35 — migrate to seed registry.toml/modelman.toml instead of `families/<family>.yaml`)

**Interfaces:**
- Consumes: `load_registry` (PR 1), `load_state` (PR 1).
- Produces: `ModelScreen(registry, state, family, registry_path, state_path, configured)`. `app.py` no longer touches `load_manifest` or `get_family_dir`. PR 3 will then change `FamilyScreen` to use the same loading path; for now `app.py` is the only caller.

- [ ] **Step 1: Write the failing test**

Replace the `test_app_with_initial_family_launches_into_model_screen` test at `tests/screens/test_app_navigation.py:35-50` with:

```python
@pytest.mark.asyncio
async def test_app_with_initial_family_launches_into_model_screen(tmp_path, monkeypatch):
    """`ModelmanApp(family=...)` seeds registry.toml/modelman.toml and
    pushes ModelScreen pointing at them."""
    from modelman.screens.models import ModelScreen

    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    reg = Registry(
        providers=[ProviderEntry(id="ollama", name="O", auth=AuthConfig(type="none"))],
        models=[
            ModelEntry(
                id="ollama/ornith:35b", family="ornith", provider_id="ollama",
                model_name="ornith:35b",
            ),
        ],
    )
    save_registry(reg, reg_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    monkeypatch.setenv("MODELMAN_CONFIG", str(tmp_path / "config.yaml"))
    (tmp_path / "config.yaml").write_text("providers:\n  ollama: {type: ollama}\n")

    from modelman.app import ModelmanApp

    app = ModelmanApp(family="ornith")
    async with app.run_test() as pilot:
        await pilot.pause()
        assert isinstance(app.screen, ModelScreen)
```

Also remove the `MODELMAN_FAMILY_DIR` env-var setting from any other tests that still rely on it (this is cleanup — they're being left in place for PR 3 and don't fail collection, but `app.py` no longer reads that var).

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/screens/test_app_navigation.py::test_app_with_initial_family_launches_into_model_screen -v`
Expected: FAIL with `TypeError: ModelScreen.__init__() missing 5 required positional arguments: 'state', 'family', 'registry_path', 'state_path', ...` (because `app.py` still passes the legacy `FamilyManifest, path` pair).

- [ ] **Step 3: Implement the new `app.py`**

Replace `src/modelman/app.py` with:

```python
"""Textual application root for modelman."""

from __future__ import annotations

import contextlib

from textual.app import App

from .registry import load_registry
from .screens.families import FamilyScreen
from .screens.models import ModelScreen
from .settings import Settings, load_settings, save_settings
from .state import load_state


def _configured_providers() -> list[str]:
    """Read provider names from registry.toml; on any failure return [].

    Kept here (rather than in registry.py) so `app.py` doesn't grow a
    hard dependency on a modelman-side parser for legacy config.yaml.
    """
    try:
        return [p.id for p in load_registry().providers]
    except Exception:
        return []


class ModelmanApp(App[None]):
    TITLE = "modelman"

    def __init__(self, family: str | None = None) -> None:
        super().__init__()
        self._initial_family = family
        try:
            settings = load_settings()
        except Exception:
            settings = Settings()
        if settings.theme:
            self.theme = settings.theme

    def on_mount(self) -> None:
        configured = _configured_providers()
        self.push_screen(FamilyScreen())
        if self._initial_family is not None:
            try:
                registry = load_registry()
            except Exception:
                registry = None
            if registry is None:
                return
            from .registry import _default_registry_path
            from .state import _default_state_path

            self.push_screen(
                ModelScreen(
                    registry=registry,
                    state=load_state(),
                    family=self._initial_family,
                    registry_path=_default_registry_path(),
                    state_path=_default_state_path(),
                    available_providers=configured,
                )
            )

    def watch_theme(self, old_theme: str | None, new_theme: str) -> None:
        del old_theme
        try:
            current = load_settings()
        except Exception:
            current = Settings()
        if current.theme == new_theme:
            return
        current.theme = new_theme
        with contextlib.suppress(Exception):
            save_settings(current)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `uv run pytest tests/screens/test_app_navigation.py::test_app_with_initial_family_launches_into_model_screen -v`
Expected: PASS

- [ ] **Step 5: Lint and commit**

```bash
uv run ruff check src/modelman/app.py tests/screens/test_app_navigation.py
git add src/modelman/app.py tests/screens/test_app_navigation.py
git commit -m "feat: load registry+state in app.py for initial-family path - completes plan item #4"
```

---

### Task 5: Update `tests/screens/test_status.py` and `tests/test_providers/test_progress.py` for the new `PendingChanges` ctor

**Files:**
- Modify: `tests/screens/test_status.py` (`_manifest_unmodified` helper, `_provider_with_events` factory, `app_with_apply` fixture, every `PendingChanges(...)` call site)
- Modify: `tests/test_providers/test_progress.py::test_pending_changes_forwards_on_progress` (Task 1's `PendingChanges` ctor rewrite broke this test; not covered by `test_status.py`. Added per Task 3 implementer ruling.)

**Interfaces:**
- Consumes: `Registry`/`StateStore`/`ModelEntry`/`save_registry`/`save_state` (PR 1); the new `PendingChanges` ctor (Task 1).
- Produces: same test signatures (the suite covers `PendingChanges` directly and via `StatusScreen.run_apply` closures). Internally: a new `_setup_apply_test` helper that seeds registry.toml/modelman.toml and returns the same `(registry, state, registry_path, state_path)` tuple the suite needs to construct `PendingChanges`.

- [ ] **Step 1: Write the failing tests (rewritten)**

Replace the entire contents of `tests/screens/test_status.py` with:

```python
"""Tests for the apply-status screen.

PendingChanges now operates on Registry/StateStore; the manifest-shaped
fixtures are gone. The pipe-delimited event-tag format is unchanged so
StatusScreen can consume the new events without modification.
"""

from __future__ import annotations

from unittest.mock import MagicMock

import pytest

from modelman.queue import PendingChanges
from modelman.registry import (
    AuthConfig,
    ModelEntry,
    ProviderEntry,
    Registry,
    save_registry,
)
from modelman.state import ModelState, StateStore, load_state


def _entry(*, id: str, family: str, provider: str, name: str) -> ModelEntry:
    return ModelEntry(id=id, family=family, provider_id=provider, model_name=name)


def _variant(*, id: str, provider: str, name: str) -> dict:
    return {"id": id, "provider": provider, "name": name}


def _setup(tmp_path):
    """Seed registry.toml + modelman.toml with two ollama entries.

    Returns (registry, state, reg_path, state_path, [entry_o35, entry_q8]).
    """
    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    o35 = _entry(id="o35", family="ornith", provider="ollama", name="ornith:35b")
    q8 = _entry(id="q8", family="ornith", provider="ollama", name="ornith:8b")
    reg = Registry(
        providers=[ProviderEntry(id="ollama", name="O", auth=AuthConfig(type="none"))],
        models=[o35, q8],
    )
    save_registry(reg, reg_path)
    state = StateStore()
    return reg, state, reg_path, state_path, [o35, q8]


def _provider_with_events(tmp_path, events: list[str]):
    p = MagicMock()
    p.name = "ollama"

    def fake_delete(v):
        events.append(f"delete:{v['id']}")

    def fake_download(v):
        events.append(f"download:{v['id']}")
        return str(tmp_path / f"new-{v['id']}")

    p.delete.side_effect = fake_delete
    p.download.side_effect = fake_download
    return p


@pytest.fixture
def app_with_apply(monkeypatch, tmp_path):
    """Spin up a ModelmanApp with registry seeded, ready for a
    StatusScreen to drive a PendingChanges apply run.

    Yields (registry, state, reg_path, state_path).
    """
    reg, state, reg_path, state_path, _ = _setup(tmp_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    monkeypatch.setenv("MODELMAN_CONFIG", str(tmp_path / "config.yaml"))
    (tmp_path / "config.yaml").write_text("providers:\n  ollama:\n    type: ollama\n")
    yield reg, state, reg_path, state_path


@pytest.mark.asyncio
async def test_pending_changes_cancel_stops_loop(tmp_path):
    """Cancel flag set before apply() must stop the loop and emit apply:cancelled."""
    reg, state, reg_path, state_path, [o35, q8] = _setup(tmp_path)
    seen: list[str] = []

    provider = MagicMock()
    provider.name = "ollama"
    provider.download.return_value = "/tmp/new"
    provider.delete.return_value = None

    pending = PendingChanges(
        registry=reg,
        state=state,
        family="ornith",
        registry_path=reg_path,
        state_path=state_path,
        providers={"ollama": provider},
        downloads=[
            ("o35", _variant(id="o35", provider="ollama", name="ornith:35b")),
            ("q8", _variant(id="q8", provider="ollama", name="ornith:8b")),
        ],
    )
    pending.cancel()
    pending.apply(on_event=seen.append)

    assert seen == ["apply:cancelled"]
    assert not reg_path.exists()
    assert not state_path.exists()
    assert not provider.download.called


@pytest.mark.asyncio
async def test_pending_changes_fires_lifecycle_events(app_with_apply, tmp_path):
    """apply() must call on_event at start/end/fail for each step plus a final apply:done."""
    reg, state, reg_path, state_path = app_with_apply
    events: list[str] = []

    p = _provider_with_events(tmp_path, events)
    pending = PendingChanges(
        registry=reg,
        state=state,
        family="ornith",
        registry_path=reg_path,
        state_path=state_path,
        providers={"ollama": p},
        downloads=[("q8", _variant(id="q8", provider="ollama", name="ornith:8b"))],
        deletes=[("o35", _variant(id="o35", provider="ollama", name="ornith:35b"))],
    )
    seen: list[str] = []
    pending.apply(on_event=seen.append)

    assert "delete:start|o35|ornith:35b" in seen
    assert "delete:done|o35|ornith:35b" in seen
    assert "download:start|q8|ornith:8b" in seen
    assert "download:done|q8|ornith:8b" in seen
    assert "save:start" in seen
    assert "save:done" in seen
    assert seen[-1] == "apply:done"


@pytest.mark.asyncio
async def test_status_screen_esc_opens_cancel_dialog_and_cancel_stops(
    tmp_path, monkeypatch,
):
    """While the apply is still running, Escape must open the cancel-or-wait
    dialog; choosing Cancel must set the cancellation flag on the
    PendingChanges so the loop stops between items."""
    from textual.widgets import Button

    from modelman.app import ModelmanApp
    from modelman.screens.status import StatusScreen

    reg, state, reg_path, state_path, [o35, q8] = _setup(tmp_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    monkeypatch.setenv("MODELMAN_CONFIG", str(tmp_path / "config.yaml"))
    (tmp_path / "config.yaml").write_text("providers:\n  ollama:\n    type: ollama\n")

    gate = __import__("threading").Event()
    captured_pending: list = []

    def make_provider():
        p = MagicMock()
        p.name = "ollama"
        p.cancel_current = MagicMock()

        def slow_download(v):
            gate.wait(timeout=2.0)
            return str(tmp_path / f"new-{v['id']}")

        p.download.side_effect = slow_download
        return p

    provider = make_provider()

    def run_apply(log_event, _progress, register):
        pending = PendingChanges(
            registry=reg,
            state=state,
            family="ornith",
            registry_path=reg_path,
            state_path=state_path,
            providers={"ollama": provider},
            downloads=[
                ("o35", _variant(id="o35", provider="ollama", name="ornith:35b")),
                ("q8", _variant(id="q8", provider="ollama", name="ornith:8b")),
            ],
        )
        captured_pending.append(pending)
        register(pending)
        pending.apply(on_event=log_event)

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        screen = StatusScreen(family="ornith", run_apply=run_apply)
        app.push_screen(screen)
        await pilot.pause()
        for _ in range(30):
            await pilot.pause()
            if captured_pending:
                break
        assert captured_pending, "worker did not register pending"
        await pilot.press("escape")
        await pilot.pause()
        for btn in app.screen.query(Button):
            if btn.id == "cancel":
                btn.press()
                break
        await pilot.pause()
        gate.set()
        for _ in range(50):
            await pilot.pause()
            if screen.done:
                break
        assert captured_pending[0].cancelled is True
        assert screen.cancelled is True
        assert screen.done is True
        # Neither registry nor state was saved on cancel.
        assert not reg_path.exists()
        assert not state_path.exists()


@pytest.mark.asyncio
async def test_status_screen_cancel_writes_immediate_feedback(tmp_path, monkeypatch):
    """Clicking Cancel must write a 'Cancelling…' line to the log
    immediately, before the worker thread has had time to catch up."""
    import threading

    from textual.widgets import Button, RichLog

    from modelman.app import ModelmanApp
    from modelman.screens.status import StatusScreen

    reg, state, reg_path, state_path, [o35, _q8] = _setup(tmp_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    monkeypatch.setenv("MODELMAN_CONFIG", str(tmp_path / "config.yaml"))
    (tmp_path / "config.yaml").write_text("providers:\n  ollama:\n    type: ollama\n")

    gate = threading.Event()
    captured_pending: list = []

    provider = MagicMock()
    provider.name = "ollama"
    provider.cancel_current = MagicMock()

    def slow_download(v):
        gate.wait(timeout=5.0)
        return str(tmp_path / f"new-{v['id']}")

    provider.download.side_effect = slow_download

    def run_apply(log_event, on_progress, register):
        pending = PendingChanges(
            registry=reg,
            state=state,
            family="ornith",
            registry_path=reg_path,
            state_path=state_path,
            providers={"ollama": provider},
            downloads=[("o35", _variant(id="o35", provider="ollama", name="ornith:35b"))],
        )
        captured_pending.append(pending)
        register(pending)
        pending.apply(on_event=log_event, on_progress=on_progress)

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        screen = StatusScreen(family="ornith", run_apply=run_apply)
        app.push_screen(screen)
        for _ in range(30):
            await pilot.pause()
            if captured_pending:
                break
        await pilot.press("escape")
        await pilot.pause()
        for btn in app.screen.query(Button):
            if btn.id == "cancel":
                btn.press()
                break
        await pilot.pause()
        log = screen.query_one(RichLog)
        text = "\n".join(line.text for line in log.lines)
        assert "Cancelling" in text
        gate.set()
        for _ in range(50):
            await pilot.pause()
            if screen.done:
                break
        assert screen.cancelled is True


@pytest.mark.asyncio
async def test_status_screen_renders_provider_progress(app_with_apply, tmp_path):
    """Progress lines emitted via on_progress must appear in the log."""
    from textual.widgets import RichLog

    from modelman.app import ModelmanApp
    from modelman.screens.status import StatusScreen

    reg, state, reg_path, state_path = app_with_apply
    provider = _provider_with_events(tmp_path, [])

    def run_apply(log_event, on_progress, _register):
        on_progress("pulling manifest")
        on_progress("pulling abcdef... 45%")
        pending = PendingChanges(
            registry=reg,
            state=state,
            family="ornith",
            registry_path=reg_path,
            state_path=state_path,
            providers={"ollama": provider},
            downloads=[("q8", _variant(id="q8", provider="ollama", name="ornith:8b"))],
            deletes=[("o35", _variant(id="o35", provider="ollama", name="ornith:35b"))],
        )
        pending.apply(on_event=log_event, on_progress=on_progress)

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        screen = StatusScreen(family="ornith", run_apply=run_apply)
        app.push_screen(screen)
        for _ in range(20):
            await pilot.pause()
            if screen.done:
                break
        log = screen.query_one(RichLog)
        text = "\n".join(line.text for line in log.lines)
        assert "pulling manifest" in text
        assert "pulling abcdef" in text


@pytest.mark.asyncio
async def test_status_screen_runs_apply_in_background(app_with_apply, tmp_path):
    """StatusScreen pushes, runs apply in a worker, and pops to FamilyScreen on done."""
    from textual.widgets import RichLog

    from modelman.app import ModelmanApp
    from modelman.screens.status import StatusScreen

    reg, state, reg_path, state_path = app_with_apply
    p = _provider_with_events(tmp_path, [])

    def run_apply(log_event, _progress, _register):
        pending = PendingChanges(
            registry=reg,
            state=state,
            family="ornith",
            registry_path=reg_path,
            state_path=state_path,
            providers={"ollama": p},
            downloads=[("q8", _variant(id="q8", provider="ollama", name="ornith:8b"))],
            deletes=[("o35", _variant(id="o35", provider="ollama", name="ornith:35b"))],
        )
        pending.apply(on_event=log_event)

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        screen = StatusScreen(family="ornith", run_apply=run_apply)
        app.push_screen(screen)
        await pilot.pause()
        for _ in range(20):
            await pilot.pause()
            if not screen._worker or not screen._worker.is_running:
                break
        assert screen.done is True
        log = screen.query_one(RichLog)
        text = "\n".join(line.text for line in log.lines)
        assert "ornith:35b" in text
        assert "ornith:8b" in text
        assert "Saved" in text or "saving" in text.lower()
        assert "Done" in text


@pytest.mark.asyncio
async def test_status_screen_renders_failure_reason(app_with_apply, tmp_path):
    """When a download fails, the exception reason should appear in the log."""
    from textual.widgets import RichLog

    from modelman.app import ModelmanApp
    from modelman.screens.status import StatusScreen

    reg, state, reg_path, state_path = app_with_apply

    provider = MagicMock()
    provider.name = "ollama"
    provider.delete.return_value = None
    provider.download.side_effect = ConnectionError("dial tcp: i/o timeout")

    def run_apply(log_event, _progress, _register):
        pending = PendingChanges(
            registry=reg,
            state=state,
            family="ornith",
            registry_path=reg_path,
            state_path=state_path,
            providers={"ollama": provider},
            downloads=[("q8", _variant(id="q8", provider="ollama", name="ornith:8b"))],
            deletes=[("o35", _variant(id="o35", provider="ollama", name="ornith:35b"))],
        )
        pending.apply(on_event=log_event)

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        screen = StatusScreen(family="ornith", run_apply=run_apply)
        app.push_screen(screen)
        for _ in range(20):
            await pilot.pause()
            if screen.done:
                break
        log = screen.query_one(RichLog)
        text = "\n".join(line.text for line in log.lines)
        assert "Failed to download" in text
        assert "i/o timeout" in text


@pytest.mark.asyncio
async def test_status_screen_shows_size_on_download_done(app_with_apply, tmp_path):
    """The 'Downloaded X' success marker should include the actual file size."""
    from textual.widgets import RichLog

    from modelman.app import ModelmanApp
    from modelman.screens.status import StatusScreen

    reg, state, reg_path, state_path = app_with_apply

    provider = MagicMock()
    provider.name = "ollama"
    provider.delete.return_value = None

    real_path = tmp_path / "downloaded-q8.bin"
    real_path.write_bytes(b"x" * (2 * 1024 * 1024 * 1024))
    provider.download.return_value = str(real_path)

    def run_apply(log_event, _progress, _register):
        pending = PendingChanges(
            registry=reg,
            state=state,
            family="ornith",
            registry_path=reg_path,
            state_path=state_path,
            providers={"ollama": provider},
            downloads=[("q8", _variant(id="q8", provider="ollama", name="ornith:8b"))],
            deletes=[("o35", _variant(id="o35", provider="ollama", name="ornith:35b"))],
        )
        pending.apply(on_event=log_event)

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        screen = StatusScreen(family="ornith", run_apply=run_apply)
        app.push_screen(screen)
        for _ in range(20):
            await pilot.pause()
            if screen.done:
                break
        log = screen.query_one(RichLog)
        text = "\n".join(line.text for line in log.lines)
        assert "Downloaded ornith:8b" in text
        assert "2.0 GB" in text, text
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `uv run pytest tests/screens/test_status.py -v`
Expected: collection error — `TypeError: __init__() got an unexpected keyword argument 'manifest'`.

- [ ] **Step 3: (already done in Step 1 — the test file rewrite is the implementation for this task; no separate implementation step needed since the prior Tasks already shipped `PendingChanges` and `Registry`/`StateStore`)**

Re-run: `uv run pytest tests/screens/test_status.py -v`
Expected: PASS (8 tests)

- [ ] **Step 4: Rewrite `tests/test_providers/test_progress.py::test_pending_changes_forwards_on_progress` for the new `PendingChanges` ctor**

Rewrite that one test in `tests/test_providers/test_progress.py` (the rest of the file uses the provider's `ProgressTqdm`, not `PendingChanges`, and stays untouched):

```python
def test_pending_changes_forwards_on_progress(tmp_path):
    """apply() must pass on_progress to provider.download()."""
    from modelman.registry import (
        AuthConfig,
        ModelEntry,
        ProviderEntry,
        Registry,
        save_registry,
    )
    from modelman.state import StateStore
    from modelman.queue import PendingChanges

    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    entry = ModelEntry(
        id="x", family="ornith", provider_id="ollama", model_name="x:7b",
    )
    reg = Registry(
        providers=[ProviderEntry(id="ollama", name="O", auth=AuthConfig(type="none"))],
        models=[entry],
    )
    save_registry(reg, reg_path)

    provider = MagicMock()
    provider.name = "ollama"
    provider.download.return_value = "/tmp/new"
    provider.delete.return_value = None

    progress_lines: list[str] = []

    pending = PendingChanges(
        registry=reg,
        state=StateStore(),
        family="ornith",
        registry_path=reg_path,
        state_path=state_path,
        providers={"ollama": provider},
        downloads=[(entry.id, {"id": entry.id, "provider": "ollama", "name": "x:7b"})],
    )
    pending.apply(on_progress=progress_lines.append)

    provider.download.assert_called_once()
    args, kwargs = provider.download.call_args
    assert "on_progress" in kwargs
    assert kwargs["on_progress"] is not None
    assert callable(kwargs["on_progress"])
```

Run: `uv run pytest tests/test_providers/test_progress.py::test_pending_changes_forwards_on_progress -v`
Expected: PASS.

- [ ] **Step 5: Lint and commit**

```bash
uv run ruff check tests/screens/test_status.py tests/test_providers/test_progress.py
git add tests/screens/test_status.py tests/test_providers/test_progress.py
git commit -m "test: rewrite status and progress tests on Registry/StateStore PendingChanges - completes plan item #5"
```

---

### Task 6: Full verification + integration smoke

**Files:** none (read-only verification)

- [ ] **Step 1: Run the full test suite**

Run: `uv run pytest`
Expected: PASS for all tests NOT marked as PR 3-deferred (the tests listed in the "Models to migrate" block in Task 3 that go through `app.py`'s legacy ctor are now broken by design — see the "What doesn't pass and why" section below).

The expected failures are:
- `tests/screens/test_app_navigation.py::test_app_with_initial_family_launches_into_model_screen` — migrated in Task 4 (PASSES).
- `tests/screens/test_app_navigation.py::test_enter_opens_model_screen` and `tests/screens/test_app_navigation.py::test_model_screen_two_pane_lists_providers_and_models` and the other ~10 integration tests in that file that go through `app.py`'s legacy `FamilyScreen` path — these intentionally FAIL in this PR; they migrate in PR 3 alongside `FamilyScreen`. Mark them `@pytest.mark.skip(reason="Migrates in PR 3 with FamilyScreen")` if they otherwise block CI; the plan documents this so the implementer isn't surprised.

Concretely: the implementer should add the `skip` decorator to each of the following tests with the reason `"FamilyScreen migrates to Registry in PR 3"`:

- `test_enter_opens_model_screen` (line 298)
- `test_model_screen_two_pane_lists_providers_and_models` (line 320)
- `test_toggle_download_queues_variant` (line 351)
- `test_status_shows_four_states` (line 379)
- `test_delete_action_noop_on_not_downloaded` (line 449)
- `test_add_then_delete_model_queues_changes` (line 487)
- `test_reconcile_shows_reality_when_manifest_out_of_date` (line 537)
- `test_reconcile_does_not_persist_to_disk_on_cancel` (line 578)
- `test_apply_merges_reconciled_state_into_manifest` (line 617)
- `test_escape_with_pending_shows_dialog_and_apply` (line 685)
- `test_discard_pending_exits_without_applying` (line 789)
- `test_family_screen_reconciles_on_resume_after_apply` (line 835)
- `test_enter_on_model_row_opens_edit_dialog` (line 935)
- `test_enter_on_provider_row_does_not_open_edit_dialog` (line 999)
- `tests/commands/test_download.py::test_download_launches_tui_at_family` (line 10) — also depends on `FamilyManifest`/`MODELMAN_FAMILY_DIR`; mark skip with reason `"FamilyScreen migrates in PR 3"`.
- `tests/screens/test_forms.py::test_add_model_dialog_inherits_selected_provider` (line 494) — uses `FamilyManifest`/`MODELMAN_FAMILY_DIR` and goes through `app.py`'s broken legacy ctor; same pattern as the 14 above. Mark skip with reason `"FamilyScreen migrates to Registry in PR 3"`. (Added per Task 3 implementer ruling — Task 6 brief was incomplete.)

- [ ] **Step 2: Lint and typecheck**

Run: `uv run ruff check . && uv run mypy src/modelman`
Expected: no errors

- [ ] **Step 3: Smoke-test the TUI**

Run: `uv run modelman` (interactive; needs a TTY) — verify:
- FamilyScreen lists existing families (still reads `families/*.yaml` for now — PR 3 will switch it)
- Pressing Enter on a family opens ModelScreen with the providers pane populated
- Pressing `a` opens the add-model form with the right provider pre-selected
- Pressing `x` on a model row toggles its queued-download status; pressing Escape pops back via the discard dialog

- [ ] **Step 4: Commit verification artifacts (if any) — none expected; this task is read-only**

---

## What's Next (not this plan)

See the numbered PR list in the Architecture section of `docs/superpowers/plans/2026-08-27-shared-model-registry-phase2-pr1.md`:

- **PR 3** — migrate `FamilyScreen`/`FamilyScreen`'s add/edit/delete-family modals to enumerate via `Registry.families()` + `StateStore` family overlay (instead of globbing `families/*.yaml`). Adds `Registry`-based loading in `app.py`'s main path (removing the legacy `FamilyManifest` loader from `on_mount`). Migrates the deferred `tests/screens/test_app_navigation.py` integration tests (the ones this plan marked `@pytest.mark.skip`). Rewrites `tests/screens/test_family_edit.py` for the overlay writes.
- **PR 4** — cleanup: confirm `config.py`/`manifest.py` are migrate-only consumers (they must stay — `modelman migrate` still reads legacy files), update `tests/commands/test_download.py` so TUI-launch tests don't depend on `MODELMAN_CONFIG`/`MODELMAN_FAMILY_DIR`, refresh `README.md`'s TUI-facing docs.