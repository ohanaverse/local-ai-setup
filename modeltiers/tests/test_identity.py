import pytest

from modeltiers import aliases as aliases_mod
from modeltiers import identity
from modeltiers.aliases import Alias, Aliases, Ignore
from modeltiers.identity import RowIdentity
from modeltiers.records import InventoryRow, ModelsDev, ModelsDevRow


def _md(
    provider: str, model_id: str, canonical: str | None, context: int = 1000000
) -> ModelsDevRow:
    return ModelsDevRow(provider, model_id, canonical, True, True, context)


GLM_HOST = _md("ollama-cloud", "glm-5.3", "zhipuai/glm-5.3", 1048576)
DEEPSEEK_HOST = _md("ollama-cloud", "deepseek-v4-pro", None, 1048576)
MODELSDEV = ModelsDev(
    rows=(
        GLM_HOST,
        DEEPSEEK_HOST,
        _md("ollama-cloud", "deepseek-v4-pro:0813", "deepseek/deepseek-v4-pro-0813"),
        _md("ollama-cloud", "gpt-oss:120b", None),
        _md("openrouter", "z-ai/glm-5.3", "zhipuai/glm-5.3"),
        _md("openrouter", "minimax/minimax-m3", "minimax/MiniMax-M3"),
        _md("openrouter", "nex-agi/nex-n2.5-pro", None),
        _md("zhipuai", "glm-5.3", "zhipuai/glm-5.3"),
        _md("deepinfra", "deepseek-ai/DeepSeek-V4-Pro", "deepseek/deepseek-v4-pro"),
        _md("deepinfra", "Qwen/Qwen3.8-27B", "alibaba/qwen3.8-27b"),
        _md("deepinfra", "openai/gpt-oss-120b", "openai/gpt-oss-120b"),
        _md("mistral", "mistral-large-latest", "mistral/mistral-large-latest"),
        # Two keys with one token sequence: `a/` and `b/` are both discarded.
        _md("vendor-a", "twin-1", "vendor-a/twin-1"),
        _md("vendor-b", "twin-1", "vendor-b/twin-1"),
    )
)
DEEPSEEK_ALIAS = Alias(
    "deepseek/deepseek-v4-pro-0813", inventory=(("ollama", "deepseek-v4-pro:cloud"),)
)
NO_ALIASES = Aliases()
INDEX = identity.build_index(MODELSDEV, NO_ALIASES)


def _cloud(provider_id: str, model_name: str) -> InventoryRow:
    return InventoryRow(provider_id, model_name, "cloud", True)


def _local(provider_id: str, model_name: str) -> InventoryRow:
    return InventoryRow(provider_id, model_name, "local", True)


# --- the index ---------------------------------------------------------------


def test_keys_keep_models_dev_spelling_and_are_found_in_any_case() -> None:
    assert INDEX.spelling("minimax/minimax-m3") == "minimax/MiniMax-M3"
    assert INDEX.spelling("MINIMAX/MINIMAX-M3") == "minimax/MiniMax-M3"
    assert INDEX.spelling("minimax/minimax-m4") is None


def test_rows_for_a_key_are_every_models_dev_row_that_names_it() -> None:
    assert [r.provider for r in INDEX.rows_for("ZhipuAI/GLM-5.3")] == [
        "ollama-cloud",
        "openrouter",
        "zhipuai",
    ]
    assert INDEX.rows_for("nobody/nothing") == ()


def test_a_latest_key_is_valid_and_left_out_of_the_normalization_index() -> None:
    assert INDEX.spelling("mistral/mistral-large-latest") == "mistral/mistral-large-latest"
    # `latest` is in the drop list, so without the exclusion this would match.
    assert INDEX.normalized("Mistral Large") is None
    assert all("latest" not in key for keys in INDEX.by_tokens.values() for key in keys)


def test_a_name_that_matches_two_keys_does_not_answer() -> None:
    assert INDEX.by_tokens[("twin", "1")] == ("vendor-a/twin-1", "vendor-b/twin-1")
    assert INDEX.normalized("Twin 1") is None


def test_a_name_with_no_tokens_does_not_answer() -> None:
    assert INDEX.normalized("mlx-4bit:latest") is None


def test_an_alias_declares_a_key_and_models_dev_spelling_wins() -> None:
    declared = aliases_mod.build(
        [
            Alias("google/gemini-4-argon", arena=("Gemini 4 Argon",)),
            Alias("MINIMAX/minimax-m3", arena=("Minimax M3",)),
        ],
        [],
    )
    index = identity.build_index(MODELSDEV, declared)
    assert index.spelling("google/gemini-4-argon") == "google/gemini-4-argon"
    assert index.rows_for("google/gemini-4-argon") == ()
    assert index.normalized("Gemini 4 Argon") == "google/gemini-4-argon"
    assert index.spelling("minimax/minimax-m3") == "minimax/MiniMax-M3"


# --- the host lookup ---------------------------------------------------------


@pytest.mark.parametrize(
    ("host", "model_id", "found"),
    [
        ("ollama", "glm-5.3:cloud", ("ollama-cloud", "glm-5.3")),
        ("ollama", "gpt-oss:120b-cloud", ("ollama-cloud", "gpt-oss:120b")),
        ("ollama", "deepseek-v4-pro:0813-cloud", ("ollama-cloud", "deepseek-v4-pro:0813")),
        ("ollama", "glm-5.3", ("ollama-cloud", "glm-5.3")),
        ("openrouter", "z-ai/glm-5.3", ("openrouter", "z-ai/glm-5.3")),
        ("openrouter", "z-ai/glm-5.3:free", ("openrouter", "z-ai/glm-5.3")),
        ("openrouter", "z-ai/glm-5.3:batch", ("openrouter", "z-ai/glm-5.3")),
        ("openrouter", "z-ai/glm-5.3:cloud", None),  # not an OpenRouter suffix
        ("openrouter", "glm-5.3", None),  # an ollama id, not an OpenRouter one
        ("omlx", "glm-5.3", None),  # models.dev lists no such host
        ("arena", "glm-5.3", None),
    ],
)
def test_host_row(host: str, model_id: str, found: tuple[str, str] | None) -> None:
    row = identity.host_row(INDEX, host, model_id)
    assert (None if row is None else (row.provider, row.model_id)) == found


# --- inventory rows ----------------------------------------------------------


def test_a_cloud_row_resolves_through_its_host_row() -> None:
    row = _cloud("ollama", "glm-5.3:cloud")
    assert identity.resolve_row(row, INDEX, NO_ALIASES) == RowIdentity(
        row=row, canonical="zhipuai/glm-5.3", resolved_by="host", host_row=GLM_HOST
    )


def test_the_stored_key_is_models_dev_spelling_whatever_answered() -> None:
    by_host = identity.resolve_row(_cloud("openrouter", "minimax/minimax-m3"), INDEX, NO_ALIASES)
    assert (by_host.canonical, by_host.resolved_by) == ("minimax/MiniMax-M3", "host")
    by_name = identity.resolve_row(_cloud("ollama", "minimax-m3:cloud"), INDEX, NO_ALIASES)
    assert (by_name.canonical, by_name.resolved_by) == ("minimax/MiniMax-M3", "normalized")
    lower = aliases_mod.build([Alias("minimax/minimax-m3", inventory=(("omlx", "M3-4bit"),))], [])
    by_alias = identity.resolve_row(_local("omlx", "M3-4bit"), INDEX, lower)
    assert (by_alias.canonical, by_alias.resolved_by) == ("minimax/MiniMax-M3", "alias")


def test_deepseek_cloud_reaches_the_april_key_without_its_alias() -> None:
    # models.dev's ollama-cloud row for the bare tag has no canonical id, so
    # the host step does not answer and the name matches the April key.
    row = _cloud("ollama", "deepseek-v4-pro:cloud")
    assert identity.resolve_row(row, INDEX, NO_ALIASES) == RowIdentity(
        row=row,
        canonical="deepseek/deepseek-v4-pro",
        resolved_by="normalized",
        host_row=DEEPSEEK_HOST,
        notes=("host row has no canonical id; matched by name",),
    )


def test_deepseek_cloud_reaches_the_0813_key_through_its_alias() -> None:
    row = _cloud("ollama", "deepseek-v4-pro:cloud")
    resolved = identity.resolve_row(row, INDEX, aliases_mod.build([DEEPSEEK_ALIAS], []))
    assert resolved == RowIdentity(
        row=row,
        canonical="deepseek/deepseek-v4-pro-0813",
        resolved_by="alias",
        # The host row is still found: it carries the context the host serves.
        host_row=DEEPSEEK_HOST,
    )
    assert resolved.basis == "measured"


def test_an_alias_wins_over_a_host_row_that_has_a_canonical_id() -> None:
    pinned = aliases_mod.build(
        [Alias("deepseek/deepseek-v4-pro", inventory=(("ollama", "glm-5.3:cloud"),))], []
    )
    resolved = identity.resolve_row(_cloud("ollama", "glm-5.3:cloud"), INDEX, pinned)
    assert (resolved.canonical, resolved.resolved_by) == ("deepseek/deepseek-v4-pro", "alias")


def test_a_local_build_resolves_by_name_and_is_inherited() -> None:
    row = _local("ollama", "qwen3.8:27b-mlx")
    resolved = identity.resolve_row(row, INDEX, NO_ALIASES)
    assert resolved == RowIdentity(
        row=row, canonical="alibaba/qwen3.8-27b", resolved_by="normalized"
    )
    assert resolved.basis == "inherited"
    assert resolved.notes == ()
    for name in ("mlx-community/Qwen3.8-27B-4bit", "Qwen3.8-27B-nvfp4"):
        assert identity.resolve_row(_local("omlx", name), INDEX, NO_ALIASES).canonical == (
            "alibaba/qwen3.8-27b"
        )


def test_a_local_ollama_row_never_takes_the_cloud_host_row() -> None:
    # `glm-5.3` is an ollama-cloud id; a local tag of that name is another
    # thing, so it resolves by name and has no host row.
    resolved = identity.resolve_row(_local("ollama", "glm-5.3"), INDEX, NO_ALIASES)
    assert (resolved.resolved_by, resolved.host_row) == ("normalized", None)


def test_an_ambiguous_name_stays_unmapped() -> None:
    resolved = identity.resolve_row(_local("ollama", "twin:1"), INDEX, NO_ALIASES)
    assert resolved == RowIdentity(row=_local("ollama", "twin:1"))
    assert resolved.unmapped


def test_a_name_no_key_matches_stays_unmapped() -> None:
    assert identity.resolve_row(_local("ollama", "medgemma:27b"), INDEX, NO_ALIASES).unmapped
    # A size the key does not have is another model.
    assert identity.resolve_row(_local("ollama", "qwen3.8:9b"), INDEX, NO_ALIASES).unmapped


def test_a_host_row_without_a_canonical_id_falls_through_to_the_name() -> None:
    resolved = identity.resolve_row(_cloud("ollama", "gpt-oss:120b-cloud"), INDEX, NO_ALIASES)
    assert resolved.canonical == "openai/gpt-oss-120b"
    assert resolved.resolved_by == "normalized"
    assert resolved.notes == (identity.HOST_NOTE,)


def test_an_unmapped_cloud_row_keeps_its_host_row() -> None:
    resolved = identity.resolve_row(
        _cloud("openrouter", "nex-agi/nex-n2.5-pro:free"), INDEX, NO_ALIASES
    )
    assert resolved.unmapped
    assert resolved.host_row is not None
    assert resolved.host_row.model_id == "nex-agi/nex-n2.5-pro"
    assert resolved.notes == ()


def test_an_ignored_row_is_neither_resolved_nor_unmapped() -> None:
    row = _local("ollama", "nomic-embed-text:latest")
    ignoring = aliases_mod.build(
        [], [Ignore("embedding model", inventory=(row.provider_id, row.model_name))]
    )
    resolved = identity.resolve_row(row, INDEX, ignoring)
    assert resolved == RowIdentity(row=row, ignore_reason="embedding model")
    assert not resolved.unmapped


def test_an_ignore_wins_over_a_name_that_would_resolve() -> None:
    row = _local("ollama", "qwen3.8:27b-mlx")
    ignoring = aliases_mod.build(
        [], [Ignore("not for coding", inventory=(row.provider_id, row.model_name))]
    )
    assert identity.resolve_row(row, INDEX, ignoring).canonical is None
