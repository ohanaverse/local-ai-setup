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
    AuthConfig,
    FamilyEntry,
    ModelEntry,
    ProviderEntry,
    Registry,
    load_registry,
    save_registry,
)
from modelman.state import (
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


def _entry(
    *,
    id: str,
    family: str,
    provider: str,
    name: str,
    repo: str | None = None,
    files: list[str] | None = None,
) -> ModelEntry:
    """Build a ModelEntry from a legacy VariantSpec-shaped dict."""
    from modelman.registry import Fetch

    fetch = None
    if repo or files:
        fetch = Fetch(repo=repo, files=files, quantizations=None)
    return ModelEntry(
        id=id,
        family=family,
        provider_id=provider,
        model_name=name,
        fetch=fetch,
    )


def _variant(
    *, id: str, provider: str, name: str, repo: str | None = None, files: list[str] | None = None
) -> dict:
    """The VariantSpec TypedDict the providers still consume."""
    return {
        "id": id,
        "provider": provider,
        "name": name,
        "repo": repo,
        "files": files,
        "quantizations": None,
    }


def _setup_apply_test(tmp_path: Path):
    """Standard fixture: registry with two ModelEntries, state with no
    downloads yet, two MagicMock providers. Returns (registry, state,
    registry_path, state_path, providers)."""
    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    a = _entry(id="ollama/a", family="f", provider="ollama", name="f:a")
    b = _entry(
        id="llamacpp/b",
        family="f",
        provider="llamacpp",
        name="f:b",
        repo="org/repo",
        files=["x.gguf"],
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

    return (
        reg,
        state,
        reg_path,
        state_path,
        {
            "ollama": provider_ollama,
            "llamacpp": provider_llama,
        },
        a,
        b,
    )


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
        ready=[
            (
                "llamacpp/b",
                _variant(
                    id="llamacpp/b",
                    provider="llamacpp",
                    name="f:b",
                    repo="org/repo",
                    files=["x.gguf"],
                ),
                True,
            )
        ],
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
    assert state.models["llamacpp/b"].ready is True
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
        ready=[("ollama/a", _variant(id="ollama/a", provider="ollama", name="f:a"), True)],
    )
    pending.apply()

    # Registry and state files were both written despite the failure (save runs unconditionally after the loop).
    assert reg_path.exists()
    assert state_path.exists()
    assert pending.failures
    assert "network down" in str(pending.failures[0])


def test_apply_empty_is_noop(tmp_path):
    """An empty PendingChanges must not touch the on-disk files."""
    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    reg = Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", auth=AuthConfig(type="none"))],
        models=[],
    )
    state = StateStore()

    pending = PendingChanges(
        registry=reg,
        state=state,
        family="f",
        registry_path=reg_path,
        state_path=state_path,
        providers={},
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
        id="llamacpp/b",
        family="f",
        provider="llamacpp",
        name="f:b",
        repo="org/repo",
        files=["x.gguf"],
    )
    reg = Registry(
        providers=[ProviderEntry(id="llamacpp", name="llama.cpp", auth=AuthConfig(type="none"))],
        models=[b],
    )
    # No pre-save: the test asserts neither file is created on cancel.
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
        ready=[
            (
                "llamacpp/b",
                _variant(
                    id="llamacpp/b",
                    provider="llamacpp",
                    name="f:b",
                    repo="org/repo",
                    files=["x.gguf"],
                ),
                True,
            )
        ],
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
        ready=[("ollama/a", _variant(id="ollama/a", provider="ollama", name="f:a"), True)],
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
            ready=[("ollama/a", _variant(id="ollama/a", provider="ollama", name="f:a"), True)],
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
        ready=[("ollama/a", _variant(id="ollama/a", provider="ollama", name="f:a"), True)],
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
        ready=[("ollama/a", _variant(id="ollama/a", provider="ollama", name="f:a"), True)],
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
        ready=[
            ("ollama/a", _variant(id="ollama/a", provider="ollama", name="f:a"), True),
            (
                "llamacpp/b",
                _variant(
                    id="llamacpp/b",
                    provider="llamacpp",
                    name="f:b",
                    repo="org/repo",
                    files=["x.gguf"],
                ),
                True,
            ),
        ],
    )
    pending.apply()

    # State on disk has both downloads recorded.
    reloaded_state = load_state(state_path)
    assert reloaded_state.get("ollama/a").ready is True
    assert reloaded_state.get("ollama/a").disk_path == str(tmp_path / "new-a")
    assert reloaded_state.get("llamacpp/b").ready is True
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
    state.set("ollama/a", ModelState(ready=True, disk_path="/old/path"))
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


def test_apply_delete_of_exposed_model_removes_litellm_entry(tmp_path):
    """Deleting a model that is exposed through LiteLLM must also remove
    its model_list row — otherwise LiteLLM keeps routing requests to a
    model whose file no longer exists (500s) and the stale row persists
    forever, since sync doesn't own litellm_exposed."""
    from modelman.litellm import load_litellm_config, save_litellm_config

    registry_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    litellm_path = tmp_path / "config.yaml"
    registry = Registry(
        providers=[
            ProviderEntry(
                id="ollama",
                name="Ollama",
                auth=AuthConfig(type="none", base_url="http://localhost:11434"),
            )
        ],
        models=[ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a")],
    )
    save_registry(registry, registry_path)
    state = StateStore()
    state.set("ollama/a", ModelState(ready=True, litellm_exposed=True))
    save_state(state, state_path)
    save_litellm_config(
        {"model_list": [{"model_name": "ollama/a"}], "general_settings": {}},
        litellm_path,
    )

    pending = PendingChanges(
        registry=registry,
        state=state,
        family="f",
        registry_path=registry_path,
        state_path=state_path,
        providers={"ollama": MagicMock()},
        deletes=[("ollama/a", _variant(id="ollama/a", provider="ollama", name="f:a"))],
        litellm_path=litellm_path,
    )
    pending.apply()

    config = load_litellm_config(litellm_path)
    assert config["model_list"] == []
    # The model is gone from registry and state, with no failure recorded.
    assert pending.failures == []
    assert all(m.id != "ollama/a" for m in load_registry(registry_path).models)
    assert "ollama/a" not in load_state(state_path).models


def test_apply_delete_not_downloaded_skips_provider_call(tmp_path):
    """When the artifact is already gone, delete still removes registry/state
    and emits lifecycle events, but does not call provider.delete()."""
    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    reg = Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", auth=AuthConfig(type="none"))],
        models=[ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a")],
    )
    save_registry(reg, reg_path)
    state = StateStore()
    state.set("ollama/a", ModelState(ready=True, disk_path="/old/a"))

    provider = MagicMock()
    provider.name = "ollama"
    provider.is_downloaded.return_value = False
    provider.delete.return_value = None

    events: list[str] = []
    pending = PendingChanges(
        registry=reg,
        state=state,
        family="f",
        registry_path=reg_path,
        state_path=state_path,
        providers={"ollama": provider},
        deletes=[("ollama/a", _variant(id="ollama/a", provider="ollama", name="a"))],
    )
    pending.apply(on_event=events.append)

    assert "delete:start|ollama/a|a" in events
    assert "delete:done|ollama/a|a" in events
    assert not provider.delete.called
    assert "ollama/a" not in [m.id for m in load_registry(reg_path).models]
    assert "ollama/a" not in load_state(state_path).models


def test_apply_delete_is_downloaded_exception_attempts_delete(tmp_path):
    """If is_downloaded() raises, we cannot know the artifact is absent,
    so we attempt provider.delete() and surface any failure normally."""
    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    reg = Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", auth=AuthConfig(type="none"))],
        models=[ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a")],
    )
    save_registry(reg, reg_path)
    state = StateStore()
    state.set("ollama/a", ModelState(ready=True))

    provider = MagicMock()
    provider.name = "ollama"
    provider.is_downloaded.side_effect = RuntimeError("stat failed")
    provider.delete.return_value = None

    events: list[str] = []
    pending = PendingChanges(
        registry=reg,
        state=state,
        family="f",
        registry_path=reg_path,
        state_path=state_path,
        providers={"ollama": provider},
        deletes=[("ollama/a", _variant(id="ollama/a", provider="ollama", name="a"))],
    )
    pending.apply(on_event=events.append)

    provider.delete.assert_called_once()
    assert "ollama/a" not in [m.id for m in load_registry(reg_path).models]


def test_apply_delete_is_downloaded_exception_failure_recorded(tmp_path):
    """If is_downloaded() raises and the subsequent delete also fails,
    the failure is recorded and registry/state cleanup is skipped."""
    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    reg = Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", auth=AuthConfig(type="none"))],
        models=[ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a")],
    )
    save_registry(reg, reg_path)
    state = StateStore()
    state.set("ollama/a", ModelState(ready=True))

    provider = MagicMock()
    provider.name = "ollama"
    provider.is_downloaded.side_effect = RuntimeError("stat failed")
    provider.delete.side_effect = PermissionError("read-only fs")

    events: list[str] = []
    pending = PendingChanges(
        registry=reg,
        state=state,
        family="f",
        registry_path=reg_path,
        state_path=state_path,
        providers={"ollama": provider},
        deletes=[("ollama/a", _variant(id="ollama/a", provider="ollama", name="a"))],
    )
    pending.apply(on_event=events.append)

    assert pending.failures
    assert "read-only fs" in str(pending.failures[0])
    assert "ollama/a" in [m.id for m in load_registry(reg_path).models]
    assert "ollama/a" in load_state(state_path).models


def test_apply_delete_overrides_queued_expose_for_same_model(tmp_path):
    """A queued delete wins over a queued expose toggle for the same
    model: the expose is dropped and the config row is removed instead."""
    from modelman.litellm import load_litellm_config, save_litellm_config

    registry_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    litellm_path = tmp_path / "config.yaml"
    registry = Registry(
        providers=[
            ProviderEntry(
                id="ollama",
                name="Ollama",
                auth=AuthConfig(type="none", base_url="http://localhost:11434"),
            )
        ],
        models=[ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a")],
    )
    save_registry(registry, registry_path)
    state = StateStore()
    state.set("ollama/a", ModelState(ready=True))
    save_state(state, state_path)
    save_litellm_config({"model_list": [], "general_settings": {}}, litellm_path)

    pending = PendingChanges(
        registry=registry,
        state=state,
        family="f",
        registry_path=registry_path,
        state_path=state_path,
        providers={"ollama": MagicMock()},
        deletes=[("ollama/a", _variant(id="ollama/a", provider="ollama", name="f:a"))],
        # The user queued expose-then-delete; exposing a deleted model
        # would fail, so the delete step replaces it with an unexpose.
        exposes=[("ollama/a", True)],
        litellm_path=litellm_path,
    )
    pending.apply()

    assert pending.failures == []
    config = load_litellm_config(litellm_path)
    assert config["model_list"] == []


def test_apply_batches_litellm_config_writes(tmp_path, monkeypatch):
    """Applying N queued exposes must parse and rewrite config.yaml once,
    not once per model — N full-file rewrites cost O(N) I/O and widen the
    window for a crash leaving the config half-updated."""
    import modelman.litellm as litellm_mod

    real_load = litellm_mod.load_litellm_config
    real_save = litellm_mod.save_litellm_config
    calls = {"load": 0, "save": 0}

    def counting_load(path):
        calls["load"] += 1
        return real_load(path)

    def counting_save(config, path):
        calls["save"] += 1
        real_save(config, path)

    monkeypatch.setattr(litellm_mod, "load_litellm_config", counting_load)
    monkeypatch.setattr(litellm_mod, "save_litellm_config", counting_save)

    registry_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    litellm_path = tmp_path / "config.yaml"
    registry = Registry(
        providers=[
            ProviderEntry(
                id="ollama",
                name="Ollama",
                auth=AuthConfig(type="none", base_url="http://localhost:11434"),
            )
        ],
        models=[
            ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a"),
            ModelEntry(id="ollama/b", family="f", provider_id="ollama", model_name="b"),
        ],
    )
    save_registry(registry, registry_path)
    state = StateStore()
    state.set("ollama/a", ModelState(ready=True))
    state.set("ollama/b", ModelState(ready=True))
    real_save({"model_list": [], "general_settings": {}}, litellm_path)

    pending = PendingChanges(
        registry=registry,
        state=state,
        family="f",
        registry_path=registry_path,
        state_path=state_path,
        providers={},
        exposes=[("ollama/a", True), ("ollama/b", True)],
        litellm_path=litellm_path,
    )
    pending.apply()

    # One parse + one atomic rewrite for the whole queue (plus the seed
    # write above, which used real_save directly).
    assert calls == {"load": 1, "save": 1}
    config = real_load(litellm_path)
    assert {r["model_name"] for r in config["model_list"]} == {"ollama/a", "ollama/b"}
    assert state.get("ollama/a").litellm_exposed is True
    assert state.get("ollama/b").litellm_exposed is True


def test_apply_expose_batch_keeps_valid_items_when_one_fails(tmp_path):
    """A per-model validation failure (not ready) must not block the
    other queued exposes, and must not prevent the config save."""
    registry_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    litellm_path = tmp_path / "config.yaml"
    registry = Registry(
        providers=[
            ProviderEntry(
                id="ollama",
                name="Ollama",
                auth=AuthConfig(type="none", base_url="http://localhost:11434"),
            )
        ],
        models=[
            ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a"),
            ModelEntry(id="ollama/b", family="f", provider_id="ollama", model_name="b"),
        ],
    )
    save_registry(registry, registry_path)
    state = StateStore()
    state.set("ollama/a", ModelState(ready=True))
    # ollama/b is NOT downloaded — its expose must fail per-item.
    state.set("ollama/b", ModelState(ready=False))
    from modelman.litellm import save_litellm_config

    save_litellm_config({"model_list": [], "general_settings": {}}, litellm_path)

    pending = PendingChanges(
        registry=registry,
        state=state,
        family="f",
        registry_path=registry_path,
        state_path=state_path,
        providers={},
        exposes=[("ollama/a", True), ("ollama/b", True)],
        litellm_path=litellm_path,
    )
    pending.apply()

    from modelman.litellm import load_litellm_config

    config = load_litellm_config(litellm_path)
    assert [r["model_name"] for r in config["model_list"]] == ["ollama/a"]
    assert state.get("ollama/a").litellm_exposed is True
    # The failed item got no flag flip and is reported with its reason.
    assert state.get("ollama/b").litellm_exposed is False
    assert any("ollama/b" in f and "not ready" in f for f in pending.failures)


def test_apply_asserts_ready_twin_keys_agree(tmp_path):
    """A queued (model_id, variant) pair must have matching ids — otherwise
    a future caller in Tasks 2-4 could desync them silently and emit the
    right event tag while writing to the wrong registry entry."""
    reg, state, reg_path, state_path, providers, a, b = _setup_apply_test(tmp_path)

    pending = PendingChanges(
        registry=reg,
        state=state,
        family="f",
        registry_path=reg_path,
        state_path=state_path,
        providers=providers,
        # queued model_id says "wrong-id" but the variant's own id is "ollama/a"
        ready=[("wrong-id", _variant(id="ollama/a", provider="ollama", name="f:a"), True)],
    )
    with pytest.raises(AssertionError, match="wrong-id"):
        pending.apply()


def test_apply_asserts_delete_twin_keys_agree(tmp_path):
    """Same invariant for the deletes loop."""
    reg, state, reg_path, state_path, providers, a, b = _setup_apply_test(tmp_path)

    pending = PendingChanges(
        registry=reg,
        state=state,
        family="f",
        registry_path=reg_path,
        state_path=state_path,
        providers=providers,
        deletes=[("wrong-id", _variant(id="ollama/a", provider="ollama", name="f:a"))],
    )
    with pytest.raises(AssertionError, match="wrong-id"):
        pending.apply()


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
                id="ollama",
                name="Ollama",
                auth=AuthConfig(type="none", base_url="http://localhost:11434"),
            )
        ],
        models=[ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a")],
    )
    save_registry(registry, registry_path)
    state = StateStore()
    state.set("ollama/a", ModelState(ready=True))
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
        "modelman.litellm.restart_litellm_proxy", lambda: calls.append("restart") or []
    )
    outcomes, warnings = apply_expose_queue(registry, state, [("ollama/a", True)], path)
    assert calls == ["restart"]
    assert warnings == []


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
        "modelman.litellm.restart_litellm_proxy", lambda: calls.append("restart") or []
    )
    outcomes, warnings = apply_expose_queue(registry, state, [], path)
    assert calls == []
    assert outcomes == []
    assert warnings == []


# ---------------------------------------------------------------------------
# moves (family reassignment)
# ---------------------------------------------------------------------------


def test_apply_writes_queued_move_to_registry(tmp_path):
    """A queued move sets ModelEntry.family and persists it to
    registry.toml (move-only queues are legal and must still save)."""
    reg, reg_path = _registry_with(
        tmp_path,
        _entry(id="m1", family="gemma4:26b-mlx", provider="ollama", name="gemma4:26b-mlx"),
    )
    state = _make_state()
    events: list[str] = []

    pending = PendingChanges(
        registry=reg,
        state=state,
        family="gemma4:26b-mlx",
        registry_path=reg_path,
        state_path=tmp_path / "modelman.toml",
        providers={},
        moves=[("m1", "gemma4")],
    )
    pending.apply(on_event=events.append)

    assert reg.model("m1").family == "gemma4"
    # Move-only queues must still trigger the save (early-return guard
    # previously checked only downloads/deletes/exposes).
    assert load_registry(reg_path).model("m1").family == "gemma4"
    assert "move:start|m1|gemma4:26b-mlx|gemma4" in events
    assert "move:done|m1|gemma4:26b-mlx|gemma4" in events
    # And they fire in order: start, done, then the final save.
    start = "move:start|m1|gemma4:26b-mlx|gemma4"
    done = "move:done|m1|gemma4:26b-mlx|gemma4"
    assert events.index(start) < events.index(done) < events.index("save:start")


def test_apply_move_for_deleted_model_is_dropped(tmp_path):
    """Deletes run before moves by design: a move for a model deleted
    in the same apply is moot and is skipped without failing."""
    reg, reg_path = _registry_with(
        tmp_path,
        _entry(id="m1", family="gemma4:26b-mlx", provider="ollama", name="gemma4:26b-mlx"),
    )
    state = _make_state()
    provider = MagicMock()
    provider.name = "ollama"
    provider.delete.return_value = None
    events: list[str] = []

    pending = PendingChanges(
        registry=reg,
        state=state,
        family="gemma4:26b-mlx",
        registry_path=reg_path,
        state_path=tmp_path / "modelman.toml",
        providers={"ollama": provider},
        deletes=[("m1", _variant(id="m1", provider="ollama", name="gemma4:26b-mlx"))],
        moves=[("m1", "gemma4")],
    )
    pending.apply(on_event=events.append)

    assert reg.models == []
    # The delete fired; the move produced no events at all.
    assert "delete:done|m1|gemma4:26b-mlx" in events
    assert not any(e.startswith("move:") for e in events)
    assert "apply:done" in events


def test_apply_move_only_queue_emits_apply_done(tmp_path):
    """A move-only queue is not treated as empty; apply runs the save
    and reports done."""
    reg, reg_path = _registry_with(
        tmp_path,
        _entry(id="m1", family="a", provider="ollama", name="x"),
    )
    events: list[str] = []
    pending = PendingChanges(
        registry=reg,
        state=_make_state(),
        family="a",
        registry_path=reg_path,
        state_path=tmp_path / "modelman.toml",
        providers={},
        moves=[("m1", "b")],
    )
    pending.apply(on_event=events.append)
    assert "apply:done" in events
    assert "save:done" in events


def test_apply_move_for_unknown_model_records_failure(tmp_path):
    """A move whose model id was never in the registry (no delete
    queued) is a failure, not a silent drop."""
    reg, reg_path = _registry_with(
        tmp_path,
        _entry(id="m1", family="a", provider="ollama", name="x"),
    )
    events: list[str] = []
    pending = PendingChanges(
        registry=reg,
        state=_make_state(),
        family="a",
        registry_path=reg_path,
        state_path=tmp_path / "modelman.toml",
        providers={},
        moves=[("ghost", "b")],
    )
    pending.apply(on_event=events.append)

    assert pending.failures == ["move ghost: Unknown model: ghost"]
    assert "move:fail|ghost|ghost|Unknown model" in events
    # The known model is untouched; apply still completes and saves.
    assert "apply:done" in events
    assert reg.model("m1").family == "a"


def test_apply_cancelled_before_moves_skips_them(tmp_path):
    """A pre-cancelled apply stops before the moves loop: no move
    events, and (per existing cancel semantics) registry/state are
    not saved."""
    reg, reg_path = _registry_with(
        tmp_path,
        _entry(id="m1", family="a", provider="ollama", name="x"),
    )
    events: list[str] = []
    pending = PendingChanges(
        registry=reg,
        state=_make_state(),
        family="a",
        registry_path=reg_path,
        state_path=tmp_path / "modelman.toml",
        providers={},
        moves=[("m1", "b")],
    )
    pending.cancel()
    pending.apply(on_event=events.append)

    assert "apply:cancelled" in events
    assert not any(e.startswith("move:") for e in events)
    # Cancel semantics: nothing persisted.
    assert load_registry(reg_path).model("m1").family == "a"


def test_apply_move_emptying_family_creates_family_entry(tmp_path):
    """Moving the last model out of a family must leave a first-class
    [[families]] entry with the legacy display name promoted, and drop
    the legacy state.families entry."""
    reg, reg_path = _registry_with(
        tmp_path,
        _entry(id="m1", family="gemma4:26b-mlx", provider="ollama", name="gemma4:26b-mlx"),
    )
    state = _make_state()
    state.touch_family("gemma4:26b-mlx", display_name="Gemma4 26B MLX")

    pending = PendingChanges(
        registry=reg,
        state=state,
        family="gemma4:26b-mlx",
        registry_path=reg_path,
        state_path=tmp_path / "modelman.toml",
        providers={},
        moves=[("m1", "gemma4")],
    )
    pending.apply()

    reloaded = load_registry(reg_path)
    assert reloaded.family("gemma4:26b-mlx").display_name == "Gemma4 26B MLX"
    assert "gemma4:26b-mlx" not in load_state(tmp_path / "modelman.toml").families


def test_apply_delete_emptying_family_creates_family_entry(tmp_path):
    """Deleting the last model of a family must also leave a lingering
    [[families]] entry (no display name — there was none to promote)."""
    reg, reg_path = _registry_with(
        tmp_path,
        _entry(id="m1", family="solo", provider="ollama", name="solo"),
    )
    state = _make_state()
    provider = MagicMock()
    provider.name = "ollama"
    provider.delete.return_value = None

    pending = PendingChanges(
        registry=reg,
        state=state,
        family="solo",
        registry_path=reg_path,
        state_path=tmp_path / "modelman.toml",
        providers={"ollama": provider},
        deletes=[("m1", _variant(id="m1", provider="ollama", name="solo"))],
    )
    pending.apply()

    reloaded = load_registry(reg_path)
    assert reloaded.family("solo") is not None
    assert reloaded.family("solo").display_name is None
    assert reloaded.models == []


def test_apply_move_does_not_create_entry_when_family_survives(tmp_path):
    """A family that still has models after the apply must NOT gain an
    entry — only families emptied to zero models linger."""
    reg, reg_path = _registry_with(
        tmp_path,
        _entry(id="m1", family="a", provider="ollama", name="x"),
        _entry(id="m2", family="a", provider="ollama", name="y"),
    )
    state = _make_state()

    pending = PendingChanges(
        registry=reg,
        state=state,
        family="a",
        registry_path=reg_path,
        state_path=tmp_path / "modelman.toml",
        providers={},
        moves=[("m1", "b")],
    )
    pending.apply()

    assert load_registry(reg_path).family("a") is None


def test_apply_move_emptying_family_does_not_duplicate_existing_entry(tmp_path):
    """If a [[families]] entry already exists, emptying the family must
    not append a duplicate — the existing entry is left untouched."""
    reg, reg_path = _registry_with(
        tmp_path,
        _entry(id="m1", family="a", provider="ollama", name="x"),
    )
    reg.families.append(FamilyEntry(name="a", display_name="Already"))
    save_registry(reg, reg_path)
    state = _make_state()

    pending = PendingChanges(
        registry=reg,
        state=state,
        family="a",
        registry_path=reg_path,
        state_path=tmp_path / "modelman.toml",
        providers={},
        moves=[("m1", "b")],
    )
    pending.apply()

    entries = [f for f in load_registry(reg_path).families if f.name == "a"]
    assert len(entries) == 1
    assert entries[0].display_name == "Already"


def test_apply_promotes_legacy_display_into_existing_entry_without_display(tmp_path):
    """An existing entry with no display name gains the legacy state
    display name before the state entry is dropped."""
    reg, reg_path = _registry_with(
        tmp_path,
        _entry(id="m1", family="a", provider="ollama", name="x"),
    )
    reg.families.append(FamilyEntry(name="a", display_name=None))
    save_registry(reg, reg_path)
    state = _make_state()
    state.touch_family("a", display_name="Legacy Name")

    pending = PendingChanges(
        registry=reg,
        state=state,
        family="a",
        registry_path=reg_path,
        state_path=tmp_path / "modelman.toml",
        providers={},
        moves=[("m1", "b")],
    )
    pending.apply()

    assert load_registry(reg_path).family("a").display_name == "Legacy Name"
    assert "a" not in load_state(tmp_path / "modelman.toml").families


def test_apply_cancelled_persists_no_family_entry(tmp_path):
    """A cancelled apply saves nothing, so the stickiness entry is not
    written and the move is not applied."""
    reg, reg_path = _registry_with(
        tmp_path,
        _entry(id="m1", family="a", provider="ollama", name="x"),
    )
    state = _make_state()

    pending = PendingChanges(
        registry=reg,
        state=state,
        family="a",
        registry_path=reg_path,
        state_path=tmp_path / "modelman.toml",
        providers={},
        moves=[("m1", "b")],
    )
    pending.cancel()
    pending.apply()

    reloaded = load_registry(reg_path)
    assert reloaded.family("a") is None
    assert reloaded.model("m1").family == "a"


def test_apply_ready_true_reconcilable_downloads(tmp_path):
    """target=True for a provider present in self.providers behaves
    exactly like today's download step."""
    reg, state, reg_path, state_path, providers, a, b = _setup_apply_test(tmp_path)
    providers["ollama"].download.return_value = str(tmp_path / "new-a")

    pending = PendingChanges(
        registry=reg,
        state=state,
        family="f",
        registry_path=reg_path,
        state_path=state_path,
        providers=providers,
        ready=[("ollama/a", _variant(id="ollama/a", provider="ollama", name="f:a"), True)],
    )
    pending.apply()

    assert state.models["ollama/a"].ready is True
    assert state.models["ollama/a"].disk_path == str(tmp_path / "new-a")
    providers["ollama"].download.assert_called_once()


def test_apply_ready_false_reconcilable_clears_without_removing_registry_entry(tmp_path):
    """target=False for a reconcilable, currently-ready model calls
    provider.delete() but must NOT remove the ModelEntry — only the
    full `deletes` list does that."""
    reg, state, reg_path, state_path, providers, a, b = _setup_apply_test(tmp_path)
    state.set("ollama/a", ModelState(ready=True, disk_path="/old/a"))
    save_state(state, state_path)

    pending = PendingChanges(
        registry=reg,
        state=state,
        family="f",
        registry_path=reg_path,
        state_path=state_path,
        providers=providers,
        ready=[("ollama/a", _variant(id="ollama/a", provider="ollama", name="f:a"), False)],
    )
    pending.apply()

    providers["ollama"].delete.assert_called_once()
    assert state.models["ollama/a"].ready is False
    assert state.models["ollama/a"].disk_path is None
    assert state.models["ollama/a"].size_bytes is None
    reloaded = load_registry(reg_path)
    assert reloaded.model("ollama/a") is not None  # NOT removed
    reloaded_state = load_state(state_path)
    assert reloaded_state.models["ollama/a"].disk_path is None
    assert reloaded_state.models["ollama/a"].size_bytes is None


def test_apply_ready_true_flag_only_sets_flag_no_provider_call(tmp_path):
    """A provider with no entry in self.providers (flag-only: native or
    openrouter) just flips the state flag; no download call is made."""
    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    native_model = ModelEntry(
        id="claude/native", family="f", provider_id="claude", model_name="native"
    )
    reg = Registry(
        providers=[
            ProviderEntry(
                id="claude", name="Claude", location="cloud", auth=AuthConfig(type="native")
            )
        ],
        models=[native_model],
    )
    save_registry(reg, reg_path)
    state = StateStore()

    pending = PendingChanges(
        registry=reg,
        state=state,
        family="f",
        registry_path=reg_path,
        state_path=state_path,
        providers={},  # no Provider instance for "claude" — flag-only
        ready=[
            ("claude/native", _variant(id="claude/native", provider="claude", name="native"), True)
        ],
    )
    pending.apply()

    assert state.get("claude/native").ready is True
    assert pending.failures == []


def test_apply_delete_flag_only_native_model_removes_entry(tmp_path):
    """Deleting a model whose provider is flag-only (no Provider instance)
    must remove the registry entry and state without crashing. Previously
    _delete raised KeyError looking up the missing provider."""
    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    native_model = ModelEntry(
        id="claude/native", family="f", provider_id="claude", model_name="native"
    )
    reg = Registry(
        providers=[
            ProviderEntry(
                id="claude", name="Claude", location="cloud", auth=AuthConfig(type="native")
            )
        ],
        models=[native_model],
    )
    save_registry(reg, reg_path)
    state = StateStore()
    state.set("claude/native", ModelState(ready=True, litellm_exposed=False))

    pending = PendingChanges(
        registry=reg,
        state=state,
        family="f",
        registry_path=reg_path,
        state_path=state_path,
        providers={},  # no Provider instance for "claude"
        deletes=[("claude/native", _variant(id="claude/native", provider="claude", name="native"))],
    )
    pending.apply()

    assert pending.failures == []
    assert all(m.id != "claude/native" for m in load_registry(reg_path).models)
    assert "claude/native" not in load_state(state_path).models


def test_apply_ready_false_flag_only_clears_flag_and_cascades_unexpose(tmp_path):
    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    native_model = ModelEntry(
        id="claude/native", family="f", provider_id="claude", model_name="native"
    )
    reg = Registry(
        providers=[
            ProviderEntry(
                id="claude", name="Claude", location="cloud", auth=AuthConfig(type="native")
            )
        ],
        models=[native_model],
    )
    save_registry(reg, reg_path)
    state = StateStore()
    state.set("claude/native", ModelState(ready=True, litellm_exposed=True))

    pending = PendingChanges(
        registry=reg,
        state=state,
        family="f",
        registry_path=reg_path,
        state_path=state_path,
        providers={},
        ready=[
            ("claude/native", _variant(id="claude/native", provider="claude", name="native"), False)
        ],
    )
    pending.apply()

    assert state.get("claude/native").ready is False
    # Cascade: was exposed, so an unexpose must have been queued and run.
    assert state.get("claude/native").litellm_exposed is False
