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


@patch("modelman.benchmark.eval.cli.load_registry")
def test_run_cmd_dry_run_prints_full_suite_row_indexes(mock_load_registry, tmp_path):
    # --row N selects by FULL-suite index (run_suite matches the same way),
    # so the dry run must print each selected row's full-suite index —
    # renumbering the filtered list would show "01" for a row the user
    # selected as 3, defeating the dry run's purpose.
    from modelman.registry import ModelEntry, ProviderEntry, Registry

    mock_load_registry.return_value = Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", location="local")],
        models=[ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a")],
    )
    suite_path = tmp_path / "suite.toml"
    rows = "\n".join(
        f'[[rows]]\nmodel = "ollama/a"\nroute = "litellm"\nlabel = "row{i}"\n' for i in range(1, 4)
    )
    suite_path.write_text(
        'name = "t"\n[judge]\nmodel = "j"\ntemperature = 0.0\nsamples = 1\n'
        'max_attempts = 1\nroute = "litellm"\n\n' + rows,
        encoding="utf-8",
    )
    result = runner.invoke(
        eval_app,
        [
            "run",
            "--suite",
            str(suite_path),
            "--root",
            str(FIXTURE_CATEGORIES),
            "--dry-run",
            "--row",
            "3",
        ],
    )
    assert result.exit_code == 0
    assert "03" in result.stdout  # full-suite index, not the renumbered "01"
    assert "row3" in result.stdout
    assert "1 of 3 row(s)" in result.stdout


@patch("modelman.benchmark.eval.cli.load_registry")
def test_run_cmd_category_filter_allows_row_naming_other_known_categories(
    mock_load_registry, tmp_path
):
    # --category narrows what RUNS, but a row's `categories =` must be
    # validated against every category that EXISTS under --root, not the
    # already-narrowed set: eval-sweep row 3 declares ["coding",
    # "reasoning"] and `--category coding` should legitimately run just its
    # coding cell, not be rejected as naming an "unknown" category.
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
categories = ["coding", "mini_review"]
""",
        encoding="utf-8",
    )
    result = runner.invoke(
        eval_app,
        [
            "run",
            "--suite",
            str(suite_path),
            "--root",
            str(FIXTURE_CATEGORIES),
            "--category",
            "coding",
            "--dry-run",
        ],
    )
    assert result.exit_code == 0
    assert "coding" in result.stdout


@patch("modelman.benchmark.eval.cli.load_registry")
def test_run_cmd_dry_run_notes_rows_disjoint_from_the_category_selection(
    mock_load_registry, tmp_path
):
    # A --category value valid overall but disjoint from a row's own
    # `categories =` must not silently run that row with zero categories:
    # the dry run (the guide's recommended pre-check) marks the row as
    # skipped and reports the would-run count, matching what run_suite
    # actually does at execution time. Without this, the exact scenario —
    # `--category doc_summary` against a row declaring ["coding",
    # "reasoning"] — isolated a provider and produced an empty row dir, an
    # all-N/A matrix row with no anomaly, and "categories": {} in
    # metrics.jsonl.
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
categories = ["coding"]

[[rows]]
model = "ollama/a"
route = "litellm"
""",
        encoding="utf-8",
    )
    result = runner.invoke(
        eval_app,
        [
            "run",
            "--suite",
            str(suite_path),
            "--root",
            str(FIXTURE_CATEGORIES),
            "--category",
            "mini_review",
            "--dry-run",
        ],
    )
    assert result.exit_code == 0
    # Row 1 (coding-only) is marked skipped in both the per-row line and
    # the trailing note; row 2 (no override) runs.
    assert "(skipped: no category overlap with selection)" in result.stdout
    assert "1 of 2 row(s)" in result.stdout
    assert "1 row(s) would be skipped" in result.stdout


_SUITE_BODY = """
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
"""


def _mock_registry(mock_load_registry):
    from modelman.registry import ModelEntry, ProviderEntry, Registry

    mock_load_registry.return_value = Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", location="local")],
        models=[ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a")],
    )


@patch("modelman.benchmark.eval.cli.load_registry")
@patch("modelman.benchmark.eval.cli.run_suite")
def test_run_cmd_rejects_unknown_cli_category(mock_run_suite, mock_load_registry, tmp_path):
    # A typo'd --category must exit 1 with a named error: it previously
    # filtered every category out silently, producing a zero-category
    # "run" that still isolated providers, wrote an empty summary, and
    # repointed eval_last_run at it. run_suite is patched to fail the test
    # if the validation ever lets execution through.
    _mock_registry(mock_load_registry)
    suite_path = tmp_path / "suite.toml"
    suite_path.write_text(_SUITE_BODY, encoding="utf-8")
    mock_run_suite.side_effect = AssertionError("run_suite must not be called")

    result = runner.invoke(
        eval_app,
        [
            "run",
            "--suite",
            str(suite_path),
            "--root",
            str(FIXTURE_CATEGORIES),
            "--category",
            "reasning",
        ],
    )
    assert result.exit_code == 1
    assert "reasning" in result.output
    assert "unknown categor" in result.output


@patch("modelman.benchmark.eval.cli.load_registry")
@patch("modelman.benchmark.eval.cli.run_suite")
def test_run_cmd_rejects_empty_category_root(mock_run_suite, mock_load_registry, tmp_path):
    # A typo'd --root makes list_categories return [] for a nonexistent
    # directory; with rows in the suite that must exit 1 rather than run a
    # zero-category sweep — the empty-root case of the same silent-empty
    # failure the --category typo check guards against.
    _mock_registry(mock_load_registry)
    suite_path = tmp_path / "suite.toml"
    suite_path.write_text(_SUITE_BODY, encoding="utf-8")
    mock_run_suite.side_effect = AssertionError("run_suite must not be called")

    result = runner.invoke(
        eval_app,
        [
            "run",
            "--suite",
            str(suite_path),
            "--root",
            str(tmp_path / "no-such-root"),
        ],
    )
    assert result.exit_code == 1
    assert "no categories found" in result.output


@patch("modelman.benchmark.eval.cli.load_registry")
def test_run_cmd_rejects_row_filter_matching_nothing(mock_load_registry, tmp_path):
    # A --row value matching no suite row (stale label after a suite edit,
    # or an index past the end) must exit 1 with a named error AND create
    # no run directory: the old silent no-op still created a run dir, wrote
    # empty artifacts, and repointed eval_last_run at it while printing
    # "complete: 0 row(s)". run_suite's selection raises before preflight,
    # so the real thing runs here and no provider is ever touched.
    _mock_registry(mock_load_registry)
    suite_path = tmp_path / "suite.toml"
    suite_path.write_text(
        _SUITE_BODY + '\n[[rows]]\nmodel = "ollama/a"\nroute = "litellm"\n', encoding="utf-8"
    )

    result = runner.invoke(
        eval_app,
        [
            "run",
            "--suite",
            str(suite_path),
            "--root",
            str(FIXTURE_CATEGORIES),
            "--row",
            "no-such-row",
        ],
    )
    assert result.exit_code == 1
    assert "matched no suite rows" in result.output
    assert "no-such-row" in result.output


def test_list_categories_cmd_malformed_category_is_a_clean_error(tmp_path):
    # A category whose items.toml is malformed raises BenchmarkError inside
    # list_categories; the CLI must print `error: ...` and exit 1 like every
    # other malformation, not dump a Python traceback.
    (tmp_path / "broken").mkdir()
    (tmp_path / "broken" / "items.toml").write_text("not [valid toml", encoding="utf-8")
    result = runner.invoke(eval_app, ["list-categories", "--root", str(tmp_path)])
    assert result.exit_code == 1
    assert "error:" in result.output
    assert "Traceback" not in result.output
