"""Unit tests for modelman.local_control — the start/stop orchestration
behind `modelman start`/`modelman stop` (issue #65). Isolation subprocess
calls are mocked; these tests cover validation, probe-based idempotency,
stale-marker recovery, and marker mutation — not bin/llm-isolate-provider
itself (see tests/benchmark/test_isolation.py for that)."""

from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from modelman.benchmark.isolation import IsolateResult
from modelman.litellm import ExposeError
from modelman.local_control import (
    DiscoveredModel,
    DiscoveredModelNeedsFamily,
    InventoryEntry,
    LocalControlError,
    _name_matches,
    inventory_local_models,
    start_local_model,
    stop_local_model,
)
from modelman.registry import (
    AuthConfig,
    Fetch,
    ModelEntry,
    ProviderEntry,
    Registry,
    load_registry,
    locked_registry,
    save_registry,
)
from modelman.state import ModelState, StateStore, load_state, save_state


def _registry() -> Registry:
    return Registry(
        providers=[
            ProviderEntry(id="ollama", name="Ollama", location="local", auth=AuthConfig(type="none")),
            ProviderEntry(
                id="openrouter", name="OpenRouter", location="cloud", auth=AuthConfig(type="api_key")
            ),
            ProviderEntry(
                id="llamacpp", name="llama.cpp", location="local", auth=AuthConfig(type="none")
            ),
        ],
        models=[
            ModelEntry(
                id="ollama/qwen3.8:27b-mlx",
                family="qwen3.8",
                provider_id="ollama",
                model_name="qwen3.8:27b-mlx",
            ),
            ModelEntry(
                id="openrouter/z-ai/glm-5.3-flash",
                family="glm",
                provider_id="openrouter",
                model_name="z-ai/glm-5.3-flash",
                location="cloud",
            ),
            ModelEntry(
                id="llamacpp/retired-model",
                family="retired",
                provider_id="llamacpp",
                model_name="retired-model",
            ),
        ],
    )


def _state_path(tmp_path: Path, running_model: str | None = None) -> Path:
    """Write an initial modelman.toml with the given marker and return its
    path — local_control now owns the marker's on-disk lifecycle."""
    path = tmp_path / "modelman.toml"
    store = StateStore()
    store.local.running_model = running_model
    save_state(store, path)
    return path


def test_start_unknown_model_raises():
    with pytest.raises(LocalControlError, match="unknown model"):
        start_local_model(_registry(), "ollama/not-in-registry")


def test_start_cloud_model_rejected():
    # A cloud model can never be "running locally" - modelman start only
    # manages the local-process lifecycle.
    with pytest.raises(LocalControlError, match="cloud model"):
        start_local_model(_registry(), "openrouter/z-ai/glm-5.3-flash")


def test_start_unsupported_provider_rejected():
    # llamacpp is local but retired (issue #33) - not in SUPPORTED_PROVIDER_IDS,
    # so bin/llm-isolate-provider can't isolate it.
    with pytest.raises(LocalControlError, match="cannot be started"):
        start_local_model(_registry(), "llamacpp/retired-model")


def test_start_already_running_and_probed_serving_is_idempotent(tmp_path):
    # The marker alone is not enough to no-op: the marked model must also
    # answer its availability probe — `modelman start <id>` is the recovery
    # command wt's "not running" message prescribes, and a no-op on a dead
    # marker would deadlock that recovery loop.
    state_path = _state_path(tmp_path, "ollama/qwen3.8:27b-mlx")
    with (
        patch("modelman.local_control._probe_running", return_value=True) as mock_probe,
        patch("modelman.local_control.stop_all_local_providers") as mock_stop,
        patch("modelman.local_control.isolate_provider") as mock_isolate,
    ):
        result = start_local_model(_registry(), "ollama/qwen3.8:27b-mlx", state_path)
    assert result.already_running is True
    mock_probe.assert_called_once()
    mock_stop.assert_not_called()
    mock_isolate.assert_not_called()
    assert load_state(state_path).local.running_model == "ollama/qwen3.8:27b-mlx"


def test_start_marker_names_dead_model_clears_it_and_restarts(tmp_path):
    # Marker matches but the probe says nothing is serving (crash, reboot,
    # `omlx stop`): the marker must be cleared and the full start must run —
    # this is the recovery path wt's fatal message prescribes.
    state_path = _state_path(tmp_path, "ollama/qwen3.8:27b-mlx")
    with (
        patch("modelman.local_control._probe_running", return_value=False),
        patch("modelman.local_control.stop_all_local_providers") as mock_stop,
        patch("modelman.local_control.isolate_provider") as mock_isolate,
    ):
        mock_isolate.return_value = IsolateResult(
            provider="ollama",
            model="qwen3.8:27b-mlx",
            direct_url="http://localhost:11434/v1/chat/completions",
            ok=True,
            error=None,
        )
        result = start_local_model(_registry(), "ollama/qwen3.8:27b-mlx", state_path)
    assert result.already_running is False
    mock_stop.assert_called_once()
    assert load_state(state_path).local.running_model == "ollama/qwen3.8:27b-mlx"


def test_start_stops_current_then_starts_requested(tmp_path):
    state_path = _state_path(tmp_path, "some/other-model")
    with (
        patch("modelman.local_control.stop_all_local_providers") as mock_stop,
        patch("modelman.local_control.isolate_provider") as mock_isolate,
    ):
        mock_isolate.return_value = IsolateResult(
            provider="ollama",
            model="qwen3.8:27b-mlx",
            direct_url="http://localhost:11434/v1/chat/completions",
            ok=True,
            error=None,
        )
        result = start_local_model(_registry(), "ollama/qwen3.8:27b-mlx", state_path)
    mock_stop.assert_called_once()
    mock_isolate.assert_called_once_with(
        "ollama", env={"LLM_ISOLATE_OLLAMA_MODEL": "qwen3.8:27b-mlx"}
    )
    assert result.already_running is False
    assert result.direct_url == "http://localhost:11434/v1/chat/completions"
    assert load_state(state_path).local.running_model == "ollama/qwen3.8:27b-mlx"


def test_start_isolate_failure_raises_and_does_not_write_marker(tmp_path):
    # No prior marker: a failed start must not fabricate one.
    state_path = _state_path(tmp_path)
    with (
        patch("modelman.local_control.stop_all_local_providers"),
        patch("modelman.local_control.isolate_provider") as mock_isolate,
    ):
        mock_isolate.return_value = IsolateResult(
            provider="ollama", model="", direct_url="", ok=False, error="warmup timed out"
        )
        with pytest.raises(LocalControlError, match="warmup timed out"):
            start_local_model(_registry(), "ollama/qwen3.8:27b-mlx", state_path)
    assert load_state(state_path).local.running_model is None


def test_start_isolate_failure_after_stopall_clears_stale_marker(tmp_path):
    # stop-all succeeded (the previously marked model is gone), then the
    # start failed: leaving the old marker would make wt fatal on every
    # launch — including pure-cloud ones — pointing at a model that is no
    # longer serving. The failure path must clear it.
    state_path = _state_path(tmp_path, "some/other-model")
    with (
        patch("modelman.local_control.stop_all_local_providers"),
        patch("modelman.local_control.isolate_provider") as mock_isolate,
    ):
        mock_isolate.return_value = IsolateResult(
            provider="ollama", model="", direct_url="", ok=False, error="warmup timed out"
        )
        with pytest.raises(LocalControlError, match="cleared the stale"):
            start_local_model(_registry(), "ollama/qwen3.8:27b-mlx", state_path)
    assert load_state(state_path).local.running_model is None


def test_start_isolate_failure_does_not_clear_foreign_marker(tmp_path):
    # A concurrent writer moved the marker on between our failed start and
    # the cleanup: cleanup only clears markers naming what WE tore down,
    # never a marker a concurrent start just wrote.
    state_path = _state_path(tmp_path, "some/other-model")

    def concurrent_writer_and_fail(*args, **kwargs):
        concurrent = StateStore()
        concurrent.local.running_model = "concurrent/new-model"
        save_state(concurrent, state_path)
        return IsolateResult(
            provider="ollama", model="", direct_url="", ok=False, error="warmup timed out"
        )

    with (
        patch("modelman.local_control.stop_all_local_providers"),
        patch("modelman.local_control.isolate_provider", side_effect=concurrent_writer_and_fail),
        pytest.raises(LocalControlError, match="warmup timed out"),
    ):
        start_local_model(_registry(), "ollama/qwen3.8:27b-mlx", state_path)
    assert load_state(state_path).local.running_model == "concurrent/new-model"


def test_stop_clears_marker_and_stops(tmp_path):
    state_path = _state_path(tmp_path, "ollama/qwen3.8:27b-mlx")
    with patch("modelman.local_control.stop_all_local_providers") as mock_stop:
        result = stop_local_model(state_path)
    mock_stop.assert_called_once()
    assert result.stopped_model_id == "ollama/qwen3.8:27b-mlx"
    assert load_state(state_path).local.running_model is None


def test_stop_noop_when_nothing_running():
    with patch("modelman.local_control.stop_all_local_providers") as mock_stop:
        result = stop_local_model()
    mock_stop.assert_not_called()
    assert result.stopped_model_id is None


def test_stop_does_not_clobber_concurrent_new_marker(tmp_path):
    # stop-all is done; a concurrent `modelman start` already wrote a new
    # marker before our cleanup ran: the cleanup must not clear the new
    # model's (still-true) marker.
    state_path = _state_path(tmp_path, "ollama/qwen3.8:27b-mlx")

    def concurrent_writer(*args, **kwargs):
        concurrent = StateStore()
        concurrent.local.running_model = "concurrent/new-model"
        save_state(concurrent, state_path)

    with patch("modelman.local_control.stop_all_local_providers", side_effect=concurrent_writer):
        result = stop_local_model(state_path)
    assert result.stopped_model_id == "ollama/qwen3.8:27b-mlx"
    assert load_state(state_path).local.running_model == "concurrent/new-model"


def test_start_mlx_lm_server_resolves_pairing_args(tmp_path):
    from modelman.registry import DraftSpec, Fetch

    registry = _registry()
    registry.providers.append(
        ProviderEntry(id="mlx_lm_server", name="mlx-lm server", location="local", auth=AuthConfig(type="none"))
    )
    registry.models.append(
        ModelEntry(
            id="mlx_lm_server/target-repo",
            family="pair",
            provider_id="mlx_lm_server",
            model_name="target-repo",
            fetch=Fetch(repo="org/target-repo"),
            draft=DraftSpec(repo="org/draft-repo"),
        )
    )
    state_path = _state_path(tmp_path)
    with (
        patch("modelman.local_control.stop_all_local_providers"),
        patch("modelman.local_control.isolate_provider") as mock_isolate,
    ):
        mock_isolate.return_value = IsolateResult(
            provider="mlx_lm_server", model="org/target-repo", direct_url="http://localhost:8001/v1/chat/completions",
            ok=True, error=None,
        )
        start_local_model(registry, "mlx_lm_server/target-repo", state_path)
    mock_isolate.assert_called_once_with("mlx_lm_server", "org/target-repo", "org/draft-repo", env=None)


def test_start_broken_mlx_lm_server_pairing_fails_before_teardown(tmp_path):
    # A missing target/draft pairing must fail fast: the previous model's
    # stop-all runs only after the isolate arguments resolve, so a pairing
    # error can never tear down a healthy model and then leave the GPU empty.
    from modelman.registry import DraftSpec, Fetch

    registry = _registry()
    registry.providers.append(
        ProviderEntry(id="mlx_lm_server", name="mlx-lm server", location="local", auth=AuthConfig(type="none"))
    )
    registry.models.append(
        ModelEntry(
            id="mlx_lm_server/target-repo",
            family="pair",
            provider_id="mlx_lm_server",
            model_name="target-repo",
            fetch=Fetch(repo="org/target-repo"),
            draft=DraftSpec(repo="org/draft-repo"),
        )
    )
    state_path = _state_path(tmp_path, "ollama/qwen3.8:27b-mlx")
    with (
        patch("modelman.local_control.stop_all_local_providers") as mock_stop,
        patch("modelman.local_control.isolate_provider") as mock_isolate,
    ):
        # Strip the draft so pairing resolution fails.
        registry.models[-1].draft = None
        with pytest.raises(LocalControlError, match="missing a target or"):
            start_local_model(registry, "mlx_lm_server/target-repo", state_path)
    mock_stop.assert_not_called()
    mock_isolate.assert_not_called()
    # No teardown happened, so the previously marked model's marker stays.
    assert load_state(state_path).local.running_model == "ollama/qwen3.8:27b-mlx"


def test_name_matches_lenient_prefix_strict_variant_tail():
    # Lenient on prefix: a server may report a path-ish spelling of the same
    # model. Strict on the tail: omlx's 4-bit and 6-bit variants share port
    # 8000 and differ exactly there — a different tail is a different model.
    assert _name_matches("qwen3.8:27b-mlx", "qwen3.8:27b-mlx")
    assert _name_matches("models/qwen3.8:27b-mlx", "qwen3.8:27b-mlx")
    assert _name_matches("org/repo", "repo")
    assert _name_matches("repo", "org/repo")
    assert not _name_matches("ornith-1.5-35b-a3b-mlx-4bit", "ornith-1.5-35b-a3b-mlx-6bit")
    assert not _name_matches("other-model", "qwen3.8:27b-mlx")


def test_start_mtplx_isolates_without_env_var(tmp_path):
    """start_local_model() must call isolate_provider('mtplx', ..., env=None)
    rather than mapping mtplx through an env-var override like ollama/omlx
    do, and must persist the running-model marker in modelman.toml's
    [local] table on success. Passing an env var here would misroute mtplx
    isolation through the wrong bash-shim mechanism; skipping the marker
    write would leave wt's local-model gate pointing at a stale model."""
    registry = _registry()
    registry.providers.append(
        ProviderEntry(id="mtplx", name="MTPLX", location="local", auth=AuthConfig(type="none", base_url="http://localhost:8003/v1"))
    )
    registry.models.append(
        ModelEntry(
            id="mtplx/Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality",
            family="qwen3.8",
            provider_id="mtplx",
            model_name="Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality",
        )
    )
    state_path = _state_path(tmp_path)
    with (
        patch("modelman.local_control.stop_all_local_providers"),
        patch("modelman.local_control.isolate_provider") as mock_isolate,
    ):
        mock_isolate.return_value = IsolateResult(
            provider="mtplx",
            model="Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality",
            direct_url="http://localhost:8003/v1/chat/completions",
            ok=True,
            error=None,
        )
        result = start_local_model(registry, "mtplx/Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality", state_path)
    mock_isolate.assert_called_once_with(
        "mtplx",
        "Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality",
        env=None,
    )
    assert result.direct_url == "http://localhost:8003/v1/chat/completions"
    assert load_state(state_path).local.running_model == "mtplx/Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality"


def test_start_unregistered_name_with_no_family_raises_needs_family(tmp_path):
    registry = _registry()
    registry_path = tmp_path / "registry.toml"
    save_registry(registry, registry_path)
    mapping = {"ollama": [{"variant_id": "llama3.2:3b", "path": "ollama:llama3.2:3b", "size_bytes": 2_000_000_000}]}
    with _patch_provider_local_models(mapping), pytest.raises(DiscoveredModelNeedsFamily) as excinfo:
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
    assert registry.model("ollama/llama3.2:3b") == entry

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
    with _patch_provider_local_models(mapping), pytest.raises(LocalControlError, match="multiple providers"):
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
    """Patch ProviderRegistry so local_control sees `mapping` (provider_id ->
    list of LocalModel dicts) without shelling out to any real provider CLI or
    scanning a real directory.

    The stub answers BOTH questions local_control asks a provider: what is on
    disk (`list_local`) and whether one registered variant is on disk
    (`resolve_local`/`is_downloaded`/`size_of`). The per-variant answers are
    keyed on the variant's `name` (i.e. ModelEntry.model_name), so this helper
    is ollama-shaped by construction — the provider spelling and the registry
    spelling agree. omlx, where they don't, is covered by the real-provider
    fixtures further down.
    """

    def get_class(name):
        return object if name in mapping else None

    def get(name, config):
        by_name = {lm["variant_id"]: lm for lm in mapping.get(name, [])}
        stub = MagicMock()
        stub.list_local.return_value = mapping.get(name, [])
        # None = no batch implementation; the caller falls back to the
        # per-variant methods below (a bare MagicMock would be a truthy
        # non-list and silently take the same fallback).
        stub.resolve_local.return_value = None
        stub.is_downloaded.side_effect = lambda spec, *a, **k: spec.get("name") in by_name
        stub.size_of.side_effect = lambda spec, *a, **k: by_name.get(spec.get("name"), {}).get(
            "size_bytes"
        )
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
    # A down ollama daemon fails every provider call: the inventory must
    # still be produced (no exception escaping to the CLI), with the
    # unanswerable models listed rather than dropped — the ambiguity is
    # surfaced separately via unqueryable_providers, not by crashing.
    registry = _listing_registry()
    state = _listing_state()

    def get(name, config):
        stub = MagicMock()
        stub.list_local.side_effect = RuntimeError("daemon unreachable")
        stub.resolve_local.return_value = None
        stub.is_downloaded.side_effect = RuntimeError("daemon unreachable")
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
    # skipped, not raise KeyError — and reported as unqueryable rather than
    # silently contributing an empty (i.e. "nothing on disk") answer.
    registry = _listing_registry()
    registry.providers.append(
        ProviderEntry(id="omlx-6bit", name="oMLX 6-bit", location="local", auth=AuthConfig(type="none"))
    )
    state = _listing_state()
    with _patch_provider_local_models({"ollama": []}):
        inventory = inventory_local_models(registry, state)
    assert inventory.discovered == []
    assert inventory.unqueryable_providers == ["omlx-6bit"]


# --- omlx join-mismatch regression tests -----------------------------------
#
# Every other fixture in this file is ollama-shaped, where a provider's
# list_local() variant_id is byte-identical to the registered
# ModelEntry.model_name. omlx is the counter-example that broke the original
# implementation: list_local() reports the model DIRECTORY BASENAME
# ("Qwen3.8-27B-4bit") while the registry entry holds the full HuggingFace
# repo id ("mlx-community/Qwen3.8-27B-4bit"). These tests drive the REAL
# OMLXProvider against a temp model dir so the mismatch is genuine rather
# than a stub's idea of it.


def _omlx_model_dir(tmp_path: Path, basename: str = "Qwen3.8-27B-4bit") -> Path:
    """Create ~/.omlx/models-style <model_dir>/<repo basename>/ with a file
    in it and return the model_dir."""
    model_dir = tmp_path / "omlx-models"
    (model_dir / basename).mkdir(parents=True)
    (model_dir / basename / "weights.safetensors").write_bytes(b"x" * 2048)
    return model_dir


_OMLX_MODEL_ID = "omlx/mlx-community--Qwen3.8-27B-4bit"


def _omlx_registry(model_dir: Path) -> Registry:
    return Registry(
        providers=[
            ProviderEntry(
                id="omlx",
                name="oMLX",
                location="local",
                model_dir=str(model_dir),
                auth=AuthConfig(type="none"),
            )
        ],
        models=[
            ModelEntry(
                id=_OMLX_MODEL_ID,
                family="qwen3.8",
                provider_id="omlx",
                model_name="mlx-community/Qwen3.8-27B-4bit",
                location="local",
                fetch=Fetch(repo="mlx-community/Qwen3.8-27B-4bit"),
            )
        ],
    )


def test_inventory_registered_omlx_model_is_downloaded_and_not_rediscovered(tmp_path):
    # The bug this branch's final review found: an omlx model registered
    # under its full repo id but reported on disk under its directory
    # basename was bucketed BOTH as "registered, not downloaded" and as a
    # brand-new "discovered" artifact. It must be neither: it is registered
    # and present, with a real size (which list_local() never reports for
    # omlx, but size_of() does).
    registry = _omlx_registry(_omlx_model_dir(tmp_path))
    inventory = inventory_local_models(registry, StateStore())
    assert inventory.downloaded == [
        InventoryEntry(model_id=_OMLX_MODEL_ID, running=False, size_bytes=2048)
    ]
    assert inventory.not_downloaded == []
    assert inventory.discovered == []
    assert inventory.unqueryable_providers == []


def test_start_omlx_directory_basename_resolves_the_registered_full_repo_entry(tmp_path):
    # The duplicate-registration bug: the old "discovered" listing told the
    # user to type the on-disk basename, but the native-name match compared
    # it verbatim against model_name (the full repo id), missed, and fell
    # through to auto-registration — writing a SECOND registry.toml entry and
    # a SECOND LiteLLM model_list row for an already-registered, already-
    # exposed artifact.
    registry = _omlx_registry(_omlx_model_dir(tmp_path))
    registry_path = tmp_path / "registry.toml"
    save_registry(registry, registry_path)
    state_path = _state_path(tmp_path)
    litellm_path = tmp_path / "config.yaml"
    litellm_path.write_text("model_list: []\n")

    with (
        patch("modelman.local_control.stop_all_local_providers"),
        patch("modelman.local_control.isolate_provider") as mock_isolate,
    ):
        mock_isolate.return_value = IsolateResult(
            provider="omlx", model="mlx-community/Qwen3.8-27B-4bit",
            direct_url="http://localhost:8000/v1/chat/completions", ok=True, error=None,
        )
        result = start_local_model(
            registry, "Qwen3.8-27B-4bit", state_path,
            registry_path=registry_path, litellm_path=litellm_path,
        )

    assert result.model_id == _OMLX_MODEL_ID
    mock_isolate.assert_called_once_with(
        "omlx", env={"LLM_ISOLATE_OMLX_4BIT_MODEL": "mlx-community/Qwen3.8-27B-4bit"}
    )
    # Singular registry entry and an untouched LiteLLM config: nothing was
    # re-registered or re-exposed.
    assert [m.id for m in load_registry(registry_path).models] == [_OMLX_MODEL_ID]
    assert litellm_path.read_text() == "model_list: []\n"


def test_find_discovered_omlx_basename_does_not_rediscover_registered_model(tmp_path):
    # The same join, exercised through start_local_model's resolution path
    # with no family: an already-registered omlx model must never raise
    # DiscoveredModelNeedsFamily just because its on-disk spelling differs.
    registry = _omlx_registry(_omlx_model_dir(tmp_path))
    registry_path = tmp_path / "registry.toml"
    save_registry(registry, registry_path)
    state_path = _state_path(tmp_path)

    with (
        patch("modelman.local_control.stop_all_local_providers"),
        patch("modelman.local_control.isolate_provider") as mock_isolate,
    ):
        mock_isolate.return_value = IsolateResult(
            provider="omlx", model="mlx-community/Qwen3.8-27B-4bit",
            direct_url=None, ok=True, error=None,
        )
        # The full repo id must keep resolving too (registry-id lookup misses
        # it; the native-name match is what catches it).
        result = start_local_model(
            registry, "mlx-community/Qwen3.8-27B-4bit", state_path, registry_path=registry_path
        )
    assert result.model_id == _OMLX_MODEL_ID


def test_inventory_flags_providers_whose_presence_check_raises():
    # "Couldn't ask the provider" must be distinguishable from "confirmed not
    # downloaded": a stopped ollama daemon makes is_downloaded() raise, and
    # silently relabelling every registered ollama model as missing would send
    # the user off to re-download models they already have.
    registry = _listing_registry()
    state = _listing_state()

    def get(name, config):
        stub = MagicMock()
        # Enumeration still works; only the per-model presence check fails.
        stub.list_local.return_value = [
            {"variant_id": "brand-new-model", "path": "ollama:brand-new-model", "size_bytes": 1}
        ]
        stub.resolve_local.return_value = None
        stub.is_downloaded.side_effect = RuntimeError("daemon unreachable")
        return stub

    with patch.multiple(
        "modelman.local_control.ProviderRegistry",
        get_class=MagicMock(return_value=object),
        get=MagicMock(side_effect=get),
    ):
        inventory = inventory_local_models(registry, state)
    assert inventory.downloaded == []
    assert inventory.unqueryable_providers == ["ollama"]
    # The discovery half is unaffected — the two questions are asked
    # separately, so one failing does not blind the other.
    assert [d.variant_id for d in inventory.discovered] == ["brand-new-model"]


def test_inventory_falls_back_to_cached_size_when_provider_reports_none():
    # A present model whose provider cannot size it (omlx's list_local(), any
    # provider without size_of()) must still show the size modelman.toml
    # already cached from an earlier reconcile, not "—".
    registry = _listing_registry()
    state = _listing_state()
    state.set("ollama/exposed-model", ModelState(ready=True, exposed=True, size_bytes=4_900_000_000))
    mapping = {"ollama": [{"variant_id": "exposed-model", "path": "ollama:exposed-model", "size_bytes": None}]}
    with _patch_provider_local_models(mapping):
        inventory = inventory_local_models(registry, state)
    assert InventoryEntry(
        model_id="ollama/exposed-model", running=False, size_bytes=4_900_000_000
    ) in inventory.downloaded


def test_register_discovered_model_keeps_registry_and_state_when_expose_fails(tmp_path):
    # expose_model() runs AFTER the registry entry and ready=True state are
    # persisted, so an ExposeError leaves a registered-but-unexposed model.
    # That partial state is deliberate (a retry resolves the entry by its
    # native name instead of registering a duplicate) — this test pins both
    # the persistence and the error.
    registry = _registry()
    registry_path = tmp_path / "registry.toml"
    save_registry(registry, registry_path)
    state_path = _state_path(tmp_path)
    mapping = {"ollama": [{"variant_id": "llama3.2:3b", "path": "ollama:llama3.2:3b", "size_bytes": 7}]}

    with (
        _patch_provider_local_models(mapping),
        patch("modelman.local_control.expose_model", side_effect=ExposeError("no litellm config")),
        pytest.raises(LocalControlError, match="registered"),
    ):
        start_local_model(
            registry, "llama3.2:3b", state_path,
            family="discovered", registry_path=registry_path,
        )

    entry = load_registry(registry_path).model("ollama/llama3.2:3b")
    assert entry.model_name == "llama3.2:3b"
    persisted = load_state(state_path).get("ollama/llama3.2:3b")
    assert persisted.ready is True
    assert persisted.exposed is False
    # Nothing was started: the failure happens during resolution.
    assert load_state(state_path).local.running_model is None


def test_register_discovered_model_refuses_an_id_that_already_exists(tmp_path):
    # Defense-in-depth against a race or a hand-edited registry.toml: the id
    # is derived from (provider, native name), so a colliding id means the
    # model is already registered — appending a second entry would duplicate
    # it in registry.toml and in LiteLLM's model_list.
    registry = _registry()
    registry_path = tmp_path / "registry.toml"
    save_registry(registry, registry_path)
    # On-disk registry already has the entry; the in-memory copy passed in
    # does not (the race this guards against).
    with locked_registry(registry_path) as fresh:
        fresh.models.append(
            ModelEntry(
                id="ollama/llama3.2:3b", family="other", provider_id="ollama",
                model_name="llama3.2:3b", location="local", source="discovered",
            )
        )
    state_path = _state_path(tmp_path)
    mapping = {"ollama": [{"variant_id": "llama3.2:3b", "path": "ollama:llama3.2:3b", "size_bytes": 7}]}

    with (
        _patch_provider_local_models(mapping),
        pytest.raises(LocalControlError, match="already registered"),
    ):
        start_local_model(
            registry, "llama3.2:3b", state_path,
            family="discovered", registry_path=registry_path,
        )
    assert [m.id for m in load_registry(registry_path).models].count("ollama/llama3.2:3b") == 1
