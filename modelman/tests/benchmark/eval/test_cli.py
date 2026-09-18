"""Tests for modelman.benchmark.eval.cli — the `modelman benchmark eval`
command surface, exercised through typer's CliRunner."""

from pathlib import Path
from unittest.mock import patch

from typer.testing import CliRunner

from modelman.benchmark.eval.cli import eval_app

runner = CliRunner()

FIXTURE_CATEGORIES = Path(__file__).parent / "fixtures" / "categories"


def test_list_categories_cmd_prints_every_category():
    # list-categories must surface every category directory under --root, including
    # both the rubric-graded (mini_review) and EvalPlus-graded (coding) kinds.
    result = runner.invoke(eval_app, ["list-categories", "--root", str(FIXTURE_CATEGORIES)])
    assert result.exit_code == 0
    assert "mini_review" in result.stdout
    assert "coding" in result.stdout


def test_list_items_cmd_prints_item_ids():
    # list-items must load a single category's items.toml and print each item id,
    # so users can pick specific items to target when iterating on a category.
    result = runner.invoke(
        eval_app, ["list-items", "--category", "mini_review", "--root", str(FIXTURE_CATEGORIES)]
    )
    assert result.exit_code == 0
    assert "off-by-one" in result.stdout


def test_list_items_cmd_unknown_category_errors():
    # An unknown category name should fail loudly (exit 1) rather than silently
    # printing nothing, since that usually means a typo in --category.
    result = runner.invoke(
        eval_app, ["list-items", "--category", "nope", "--root", str(FIXTURE_CATEGORIES)]
    )
    assert result.exit_code == 1


@patch("modelman.benchmark.eval.cli.load_registry")
def test_run_cmd_dry_run_prints_resolved_rows_without_running(mock_load_registry, tmp_path):
    # --dry-run must resolve and print every suite row (model/route/categories) without
    # touching providers or the network, so users can sanity-check a suite before a real run.
    from modelman.registry import ModelEntry, ProviderEntry, Registry

    mock_load_registry.return_value = Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", location="local")],
        models=[ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a")],
    )
    suite_path = tmp_path / "suite.toml"
    suite_path.write_text(
        """
name = "t"
[judge]
model = "j"
temperature = 0.0
samples = 1
max_attempts = 1
route = "litellm"

[[rows]]
model = "ollama/a"
route = "litellm"
""",
        encoding="utf-8",
    )
    result = runner.invoke(
        eval_app,
        ["run", "--suite", str(suite_path), "--root", str(FIXTURE_CATEGORIES), "--dry-run"],
    )
    assert result.exit_code == 0
    assert "ollama/a" in result.stdout
    assert "dry run" in result.stdout


@patch("modelman.benchmark.eval.cli.load_registry")
def test_run_cmd_rejects_row_naming_unknown_category(mock_load_registry, tmp_path):
    # A row's `categories =` must be validated against the known category
    # names: a typo ("reasning") previously filtered every category out
    # silently, and the row "ran" with zero results — a wasted, possibly
    # billed run with nothing but N/A cells in the report.
    from modelman.registry import ModelEntry, ProviderEntry, Registry

    mock_load_registry.return_value = Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", location="local")],
        models=[ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a")],
    )
    suite_path = tmp_path / "suite.toml"
    suite_path.write_text(
        """
name = "t"
[judge]
model = "j"
temperature = 0.0
samples = 1
max_attempts = 1
route = "litellm"

[[rows]]
model = "ollama/a"
route = "litellm"
categories = ["reasning"]
""",
        encoding="utf-8",
    )
    result = runner.invoke(
        eval_app,
        ["run", "--suite", str(suite_path), "--root", str(FIXTURE_CATEGORIES), "--dry-run"],
    )
    assert result.exit_code == 1
    assert "reasning" in result.output
    assert "unknown categor" in result.output
