"""Provider sync — reconcile configured models against provider state."""

from unittest.mock import MagicMock, patch

import pytest

from modelman.providers.base import Provider, VariantSpec
from modelman.providers.ollama import _parse_ollama_list_sizes
from modelman.registry import (
    AuthConfig,
    Cost,
    DraftSpec,
    Fetch,
    ModelEntry,
    ProviderEntry,
    Registry,
)
from modelman.registry import model_entry_to_variant as registry_model_entry_to_variant
from modelman.state import ModelState, StateStore
from modelman.sync import (
    SyncError,
    _ensure_provider_entries,
    _model_entry_to_variant,
    _modeldir_providers,
    _ollama_downloaded,
    backfill_provider_defaults,
    list_modeldir,
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


def test_model_entry_to_variant_builds_spec_from_fetch():
    """sync's adapter builds the provider-only subset; cost is UI metadata
    and is omitted from provider calls."""
    entry = ModelEntry(
        id="llamacpp/q4",
        family="ornith-1.5",
        provider_id="llamacpp",
        model_name="Ornith-1.5-35B-Q4_K_M.gguf",
        fetch=Fetch(repo="ornith-ai/Ornith-1.5-35B-A3B-GGUF", files=["Ornith-1.5-35B-Q4_K_M.gguf"]),
        model_info={"supports_function_calling": True},
    )
    spec = _model_entry_to_variant(entry)
    assert spec == {
        "id": "llamacpp/q4",
        "provider": "llamacpp",
        "name": "Ornith-1.5-35B-Q4_K_M.gguf",
        "repo": "ornith-ai/Ornith-1.5-35B-A3B-GGUF",
        "files": ["Ornith-1.5-35B-Q4_K_M.gguf"],
        "quantizations": None,
        "local_path": None,
        "draft_repo": None,
        "draft_local_path": None,
        "location": None,
        "model_info": {"supports_function_calling": True},
        "quantization": None,
    }
    assert "cost" not in spec


def test_model_entry_to_variant_handles_empty_fetch():
    entry = ModelEntry(
        id="ollama/a",
        family="a",
        provider_id="ollama",
        model_name="a",
    )
    spec = _model_entry_to_variant(entry)
    assert spec["repo"] is None
    assert spec["files"] is None
    assert spec["quantizations"] is None
    assert spec["model_info"] == {}


def test_model_entry_to_variant_omits_cost():
    """Provider APIs do not consume cost, and the UI layer serializes Cost as
    a plain dict; sync's adapter omits the field entirely to keep the
    provider contract lean."""
    entry = ModelEntry(
        id="ollama/glm-5.3:cloud",
        family="glm",
        provider_id="ollama",
        model_name="glm-5.3:cloud",
        cost=Cost(subscription_price=20.0, subscription_period="month"),
    )
    spec = _model_entry_to_variant(entry)
    assert "cost" not in spec


def test_sync_and_registry_adapters_agree_on_keys():
    """Highest-risk hazard in the mlx-lm-quantization plan: registry.py's
    model_entry_to_variant() and sync.py's _model_entry_to_variant() are
    two independent, not-shared adapters. If only one of them is updated
    to carry local_path/draft_repo/draft_local_path, the TUI/queue path
    would see a locally-produced target+draft pairing while `modelman
    sync` silently sees local_path=None/draft_repo=None — wrong
    is_downloaded results and a delete path that thinks the entry is
    repo-based. This test fails immediately if the two adapters' key sets
    (modulo sync's documented `cost` omission) ever diverge again."""
    entry = ModelEntry(
        id="mlx_lm_server/target",
        family="target",
        provider_id="mlx_lm_server",
        model_name="target",
        fetch=Fetch(repo="org/target-repo", local_path="/models/target"),
        draft=DraftSpec(repo="org/draft-repo", local_path="/models/draft"),
        cost=Cost(input_price_per_million=1.0),
    )
    registry_spec = registry_model_entry_to_variant(entry)
    sync_spec = _model_entry_to_variant(entry)

    # sync's adapter deliberately omits `cost` (provider-only subset); every
    # other key must be identical, including local_path/draft_repo/
    # draft_local_path values, not just their presence.
    registry_keys_minus_cost = set(registry_spec) - {"cost"}
    assert registry_keys_minus_cost == set(sync_spec)
    for key in registry_keys_minus_cost:
        assert registry_spec[key] == sync_spec[key], f"adapters disagree on {key!r}"


def test_ollama_downloaded_maps_configured_models():
    registry = Registry(
        models=[
            ModelEntry(id="ollama/a", family="a", provider_id="ollama", model_name="a"),
            ModelEntry(id="ollama/b", family="b", provider_id="ollama", model_name="b"),
        ]
    )
    result = _ollama_downloaded(registry, {"a": 1024, "c": 2048})
    assert result == {"ollama/a": ("ollama:a", 1024)}


def test_ollama_downloaded_ignores_unconfigured_models():
    registry = Registry(models=[])
    assert _ollama_downloaded(registry, {"a": 1024}) == {}


def test_reconcile_downloaded_model():
    registry = Registry(
        models=[
            ModelEntry(
                id="ollama/a",
                family="a",
                provider_id="ollama",
                model_name="a",
            ),
        ]
    )
    state = StateStore()
    result = reconcile(registry, state, {"ollama/a": ("ollama:a", 1024)})
    assert result.downloaded == ["ollama/a"]
    assert result.not_downloaded == []
    s = state.get("ollama/a")
    assert s.ready is True
    assert s.disk_path == "ollama:a"
    assert s.size_bytes == 1024


def test_reconcile_not_downloaded_model():
    registry = Registry(
        models=[
            ModelEntry(
                id="ollama/a",
                family="a",
                provider_id="ollama",
                model_name="a",
            ),
        ]
    )
    state = StateStore()
    result = reconcile(registry, state, {})
    assert result.downloaded == []
    assert result.not_downloaded == ["ollama/a"]
    s = state.get("ollama/a")
    assert s.ready is False
    assert s.disk_path is None
    assert s.size_bytes is None


def test_reconcile_skips_non_reconcilable_models():
    registry = Registry(
        models=[
            ModelEntry(
                id="openrouter/x",
                family="x",
                provider_id="openrouter",
                model_name="x",
            ),
        ]
    )
    state = StateStore()
    result = reconcile(registry, state, {"openrouter/x": ("path", 1024)})
    assert result.downloaded == []
    assert result.not_downloaded == []
    assert state.get("openrouter/x").ready is False


def test_reconcile_preserves_litellm_exposed():
    registry = Registry(
        models=[
            ModelEntry(
                id="ollama/a",
                family="a",
                provider_id="ollama",
                model_name="a",
            ),
        ]
    )
    state = StateStore()
    state.set("ollama/a", ModelState(ready=False, exposed=True))
    reconcile(registry, state, {"ollama/a": ("ollama:a", 1024)})
    assert state.get("ollama/a").exposed is True


def test_reconcile_handles_modeldir_providers():
    registry = Registry(
        models=[
            ModelEntry(
                id="omlx/4bit",
                family="ornith-1.5",
                provider_id="omlx",
                model_name="4bit",
                fetch=Fetch(repo="foo/MLX"),
            ),
        ]
    )
    state = StateStore()
    result = reconcile(
        registry,
        state,
        {
            "omlx/4bit": ("/models/MLX", 200),
        },
    )
    assert sorted(result.downloaded) == ["omlx/4bit"]
    assert result.not_downloaded == []
    assert state.get("omlx/4bit").size_bytes == 200


class _FakeProvider(Provider):
    name = "fake"

    def __init__(self, downloaded: bool, path: str | None, size: int | None):
        super().__init__({})
        self._downloaded = downloaded
        self._path = path
        self._size = size

    def is_downloaded(self, variant: VariantSpec) -> bool:
        return self._downloaded

    def path_of(self, variant: VariantSpec) -> str | None:
        return self._path

    def size_of(self, variant: VariantSpec) -> int | None:
        return self._size

    def download(self, variant: VariantSpec) -> str:
        return self._path or ""

    def list_local(self) -> list[dict]:
        return []


def test_list_modeldir_records_downloaded_models():
    registry = Registry(
        models=[
            ModelEntry(
                id="llamacpp/q4",
                family="f",
                provider_id="llamacpp",
                model_name="q4.gguf",
                fetch=Fetch(repo="foo/bar", files=["q4.gguf"]),
            ),
        ]
    )
    providers = {"llamacpp": _FakeProvider(True, "/cache/q4.gguf", 100)}
    result = list_modeldir(registry, providers)
    assert result == {"llamacpp/q4": ("/cache/q4.gguf", 100)}


def test_list_modeldir_skips_not_downloaded_models():
    registry = Registry(
        models=[
            ModelEntry(
                id="llamacpp/q4",
                family="f",
                provider_id="llamacpp",
                model_name="q4.gguf",
                fetch=Fetch(repo="foo/bar", files=["q4.gguf"]),
            ),
        ]
    )
    providers = {"llamacpp": _FakeProvider(False, None, None)}
    result = list_modeldir(registry, providers)
    assert result == {}


def test_list_modeldir_requires_path_and_size():
    registry = Registry(
        models=[
            ModelEntry(
                id="llamacpp/q4",
                family="f",
                provider_id="llamacpp",
                model_name="q4.gguf",
                fetch=Fetch(repo="foo/bar", files=["q4.gguf"]),
            ),
        ]
    )
    providers = {"llamacpp": _FakeProvider(True, None, 100)}
    result = list_modeldir(registry, providers)
    assert result == {}


def test_modeldir_providers_builds_configured_providers(tmp_path, monkeypatch):
    monkeypatch.setenv("HOME", str(tmp_path))
    registry = Registry(
        providers=[
            ProviderEntry(id="llamacpp", name="Llama.cpp"),
            ProviderEntry(id="omlx", name="oMLX", model_dir=str(tmp_path / "models")),
        ]
    )
    providers = _modeldir_providers(registry)
    assert set(providers) == {"llamacpp", "omlx"}
    assert providers["llamacpp"].name == "llamacpp"
    assert providers["omlx"].name == "omlx"


def test_modeldir_providers_omits_missing_providers():
    registry = Registry(providers=[])
    assert _modeldir_providers(registry) == {}


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
                id="ollama/ornith-1.5:9b",
                family="ornith-1.5:9b",
                provider_id="ollama",
                model_name="ornith-1.5:9b",
            ),
            ModelEntry(
                id="ollama/other",
                family="other",
                provider_id="ollama",
                model_name="other",
            ),
        ]
    )
    state = StateStore()

    result = sync(registry, state, runner)

    assert result.downloaded == ["ollama/ornith-1.5:9b"]
    assert result.not_downloaded == ["ollama/other"]
    assert state.get("ollama/ornith-1.5:9b").ready is True
    assert state.get("ollama/other").ready is False
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


def test_sync_includes_modeldir_results():
    runner = MagicMock()
    runner.side_effect = [
        _result(0, "NAME  ID  SIZE  MODIFIED\n"),
    ]
    registry = Registry(
        models=[
            ModelEntry(
                id="omlx/4bit",
                family="ornith-1.5",
                provider_id="omlx",
                model_name="4bit",
                fetch=Fetch(repo="foo/MLX"),
            ),
        ],
        providers=[ProviderEntry(id="omlx", name="oMLX")],
    )
    state = StateStore()

    with patch("modelman.sync.list_modeldir") as mock_modeldir:
        mock_modeldir.return_value = {"omlx/4bit": ("/models/MLX", 200)}
        result = sync(registry, state, runner)

    assert result.downloaded == ["omlx/4bit"]
    assert result.not_downloaded == []
    assert state.get("omlx/4bit").ready is True
    assert state.get("omlx/4bit").disk_path == "/models/MLX"
    assert state.get("omlx/4bit").size_bytes == 200


def test_sync_combines_ollama_and_modeldir_results():
    runner = MagicMock()
    runner.side_effect = [
        _result(0, "NAME  ID  SIZE  MODIFIED\nollama-model  abc  6.6 GB  4 days ago\n"),
    ]
    registry = Registry(
        models=[
            ModelEntry(
                id="ollama/ollama-model",
                family="ollama-family",
                provider_id="ollama",
                model_name="ollama-model",
            ),
            ModelEntry(
                id="omlx/4bit",
                family="ornith-1.5",
                provider_id="omlx",
                model_name="4bit",
                fetch=Fetch(repo="foo/MLX"),
            ),
        ],
        providers=[ProviderEntry(id="omlx", name="oMLX")],
    )
    state = StateStore()

    with patch("modelman.sync.list_modeldir") as mock_modeldir:
        mock_modeldir.return_value = {"omlx/4bit": ("/models/MLX", 200)}
        result = sync(registry, state, runner)

    assert sorted(result.downloaded) == ["ollama/ollama-model", "omlx/4bit"]
    assert result.not_downloaded == []
    assert state.get("ollama/ollama-model").size_bytes == int(6.6 * 1024**3)
    assert state.get("omlx/4bit").disk_path == "/models/MLX"


def test_ensure_provider_entries_repairs_empty_providers():
    registry = Registry(
        providers=[],
        models=[ModelEntry(id="ollama/x", family="x", provider_id="ollama", model_name="x")],
    )
    added = _ensure_provider_entries(registry)
    assert added == ["ollama"]
    assert [p.id for p in registry.providers] == ["ollama"]
    assert registry.providers[0].auth.base_url == "http://localhost:11434"


def test_ensure_provider_entries_keeps_existing():
    registry = Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama")],
        models=[ModelEntry(id="ollama/x", family="x", provider_id="ollama", model_name="x")],
    )
    assert _ensure_provider_entries(registry) == []
    assert len(registry.providers) == 1


def test_ensure_provider_entries_ignores_non_reconcilable():
    registry = Registry(
        providers=[],
        models=[
            ModelEntry(id="openrouter/x", family="x", provider_id="openrouter", model_name="x")
        ],
    )
    assert _ensure_provider_entries(registry) == []
    assert registry.providers == []


def test_ensure_provider_entries_returns_fresh_instances():
    # Each repaired registry must get its own ProviderEntry (and nested
    # AuthConfig) instance; mutating one registry's entry must not corrupt
    # the shared default used by the next sync.
    registry1 = Registry(
        providers=[],
        models=[ModelEntry(id="ollama/x", family="x", provider_id="ollama", model_name="x")],
    )
    registry2 = Registry(
        providers=[],
        models=[ModelEntry(id="ollama/y", family="y", provider_id="ollama", model_name="y")],
    )
    _ensure_provider_entries(registry1)
    _ensure_provider_entries(registry2)
    registry1.providers[0].auth.base_url = "http://mutated:9999"
    assert registry2.providers[0].auth.base_url == "http://localhost:11434"


def test_sync_registers_agent_providers(tmp_path, monkeypatch):
    # _default_wt_config_path() joins <MODELMAN_WT_DIR>/config.toml exactly
    # — the fixture file must be named "config.toml", not anything else.
    wt_config = tmp_path / "config.toml"
    wt_config.write_text('[[agents]]\nname = "claude"\n')
    monkeypatch.setenv("MODELMAN_WT_DIR", str(tmp_path))
    registry = Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", auth=AuthConfig(type="none"))]
    )
    state = StateStore()

    def fake_runner(args, **kwargs):
        from unittest.mock import MagicMock

        result = MagicMock()
        result.returncode = 0
        result.stdout = "NAME    ID    SIZE    MODIFIED\n"
        return result

    result = sync(registry, state, runner=fake_runner)

    assert "claude" in result.providers_added
    registry.provider("claude")  # does not raise


def test_sync_backfills_missing_omlx_base_url(tmp_path):
    """omlx's registry template historically had no auth.base_url at all,
    so every omlx model was unroutable in direct mode with no clear error.
    sync must fill the gap for an existing provider without disturbing a
    base_url or protocols value the user has already set by hand."""
    registry = Registry(
        providers=[
            ProviderEntry(id="omlx", name="oMLX", location="local", auth=AuthConfig(type="none"))
        ]
    )
    result = backfill_provider_defaults(registry)
    omlx = next(p for p in result.providers if p.id == "omlx")
    assert omlx.auth.base_url == "http://localhost:8000"
    assert omlx.protocols == ["openai-chat"]


def test_sync_backfill_preserves_user_set_base_url(tmp_path):
    """A user-configured base_url (e.g. omlx moved to a non-default port)
    must never be overwritten by the backfill."""
    registry = Registry(
        providers=[
            ProviderEntry(
                id="omlx",
                name="oMLX",
                location="local",
                auth=AuthConfig(type="none", base_url="http://localhost:9999"),
            )
        ]
    )
    result = backfill_provider_defaults(registry)
    omlx = next(p for p in result.providers if p.id == "omlx")
    assert omlx.auth.base_url == "http://localhost:9999"


def test_sync_backfills_missing_protocols(tmp_path):
    """A provider entry that predates the protocols field (protocols = [])
    gets the template's protocols; a non-empty list is never touched."""
    registry = Registry(
        providers=[
            ProviderEntry(
                id="omlx",
                name="oMLX",
                location="local",
                protocols=[],
                auth=AuthConfig(type="none", base_url="http://localhost:8000"),
            ),
            ProviderEntry(
                id="openrouter",
                name="OpenRouter",
                location="cloud",
                protocols=["openai-chat"],
                auth=AuthConfig(type="secret_ref", secret_ref="sk-or"),
            ),
        ]
    )
    result = backfill_provider_defaults(registry)
    omlx = next(p for p in result.providers if p.id == "omlx")
    assert omlx.protocols == ["openai-chat"]
    # openrouter has no default template — untouched either way.
    openrouter = next(p for p in result.providers if p.id == "openrouter")
    assert openrouter.protocols == ["openai-chat"]
    assert openrouter.auth.base_url is None
