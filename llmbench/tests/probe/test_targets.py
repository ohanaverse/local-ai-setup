"""Target specs (`NAME=BASE_URL,MODEL_ID[,API_KEY_ENV]`), the key lookup, and
the prompt set."""

from pathlib import Path

import pytest

from llmbench.probe.thinking import (
    ARITHMETIC_PROMPT,
    PLANNING_ITEM_ID,
    ProbeError,
    Prompt,
    Target,
    builtin_prompts,
    load_prompt_file,
    parse_target,
    resolve_api_key,
)

# The repo's own eval categories (tests/probe/ -> parents[3] is the checkout).
REPO_CATEGORIES = Path(__file__).resolve().parents[3] / "benchmarks" / "tasks" / "eval"


def test_parse_target_without_key():
    assert parse_target("omlx=http://localhost:8000/v1,Qwen3.8-27B-4bit") == Target(
        name="omlx",
        base_url="http://localhost:8000/v1",
        model="Qwen3.8-27B-4bit",
        api_key_env=None,
    )


def test_parse_target_with_key_env_and_a_slash_in_the_model():
    target = parse_target("mtplx=http://localhost:8003/v1/,Org/Model-Q,MY_KEY")
    assert target.model == "Org/Model-Q"
    assert target.api_key_env == "MY_KEY"
    # A trailing slash is dropped so the endpoint is never `//chat/completions`.
    assert target.base_url == "http://localhost:8003/v1"


@pytest.mark.parametrize(
    ("spec", "fragment"),
    [
        ("http://localhost:8000/v1,model", "NAME="),
        ("=http://localhost:8000/v1,model", "NAME="),
        ("bad name=http://localhost:8000/v1,model", "name"),
        ("omlx=http://localhost:8000/v1", "MODEL_ID"),
        ("omlx=http://localhost:8000/v1,", "MODEL_ID"),
        ("omlx=localhost:8000/v1,model", "http"),
        ("omlx=http://localhost:8000/v1,model,KEY,extra", "too many"),
        ("omlx=http://localhost:8000/v1,model,not-an-env-name", "environment variable"),
    ],
)
def test_parse_target_rejects_malformed_specs(spec, fragment):
    with pytest.raises(ProbeError) as excinfo:
        parse_target(spec)
    message = str(excinfo.value)
    assert fragment in message
    assert spec in message  # the error names the spec it refused


def test_resolve_api_key_none_when_no_env_named():
    target = parse_target("omlx=http://localhost:8000/v1,m")
    assert resolve_api_key(target, {}) is None


def test_resolve_api_key_reads_the_named_variable():
    target = parse_target("omlx=http://localhost:8000/v1,m,PROBE_KEY")
    found = resolve_api_key(target, {"PROBE_KEY": "s3cret"}) == "s3cret"
    assert found


@pytest.mark.parametrize("environ", [{}, {"PROBE_KEY": ""}])
def test_resolve_api_key_named_but_unset_is_an_error(environ):
    target = parse_target("omlx=http://localhost:8000/v1,m,PROBE_KEY")
    with pytest.raises(ProbeError, match="PROBE_KEY"):
        resolve_api_key(target, environ)


def test_builtin_prompts_reuse_the_eval_planning_item():
    """The planning prompt comes from the eval category through its own loader,
    so the probe sends exactly the text the eval suite sent."""
    prompts = builtin_prompts(REPO_CATEGORIES)
    assert [p.text for p in prompts][0] == ARITHMETIC_PROMPT
    planning = prompts[1]
    assert planning.id == f"planning/{PLANNING_ITEM_ID}"
    items_toml = (REPO_CATEGORIES / "planning" / "items.toml").read_text(encoding="utf-8")
    assert "flaky" in planning.text
    assert planning.text.strip() in items_toml


def test_builtin_prompts_missing_category_is_a_probe_error(tmp_path):
    with pytest.raises(ProbeError, match="--no-builtin"):
        builtin_prompts(tmp_path)


def test_builtin_prompts_missing_item_is_a_probe_error(tmp_path):
    category = tmp_path / "planning"
    category.mkdir()
    (category / "items.toml").write_text(
        '[[items]]\nid = "other"\nprompt = "x"\n', encoding="utf-8"
    )
    (category / "rubric.toml").write_text("[dimensions]\na = 100\n", encoding="utf-8")
    (category / "rubric.md").write_text("rubric", encoding="utf-8")
    with pytest.raises(ProbeError, match=PLANNING_ITEM_ID):
        builtin_prompts(tmp_path)


def test_load_prompt_file(tmp_path):
    path = tmp_path / "my-prompt.txt"
    path.write_text("Explain X.\n", encoding="utf-8")
    assert load_prompt_file(path) == Prompt(id="my-prompt", text="Explain X.\n", source=str(path))


def test_load_prompt_file_missing_or_empty(tmp_path):
    with pytest.raises(ProbeError, match="not found"):
        load_prompt_file(tmp_path / "nope.txt")
    empty = tmp_path / "empty.txt"
    empty.write_text("  \n", encoding="utf-8")
    with pytest.raises(ProbeError, match="empty"):
        load_prompt_file(empty)
