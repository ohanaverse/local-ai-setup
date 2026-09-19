"""Tests for modelman.benchmark.eval.report — the capability matrix (grouped
by registry family), per-category leaderboard, and anomalies table.
"""

import json
from pathlib import Path

from modelman.benchmark.eval.evalplus_runner import CodingResult
from modelman.benchmark.eval.judged_runner import CategoryRowResult, ItemResult
from modelman.benchmark.eval.report import render_summary, write_metrics_jsonl, write_row_artifacts
from modelman.benchmark.eval.runner import RowRunResult
from modelman.benchmark.eval.suite import RowConfig
from modelman.benchmark.judge_core import JudgeOutcome, JudgeScore
from modelman.registry import ModelEntry, ProviderEntry, Registry


def _registry() -> Registry:
    return Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", location="local")],
        models=[
            ModelEntry(
                id="ollama/qwen-27b", family="qwen3.8", provider_id="ollama", model_name="qwen-27b"
            ),
            ModelEntry(id="ollama/other", family="other", provider_id="ollama", model_name="other"),
        ],
    )


def _judged_result(score) -> CategoryRowResult:
    outcome = JudgeOutcome(
        status="scored" if score is not None else "judge_fail",
        samples=[],
        combined=(
            JudgeScore(
                scores={"a": int(score)},
                total=int(score),
                verdict="",
                flags=[],
                rationale="",
                raw_text="",
            )
            if score is not None
            else None
        ),
        attempts_used=1,
    )
    return CategoryRowResult(
        category="doc_summary",
        items=[ItemResult(item_id="i1", response_text="r", judge=outcome, score_100=score)],
        score_100=score,
    )


def test_render_summary_groups_capability_matrix_by_family():
    # The capability matrix groups rows by their model's registry family (not
    # by label), and renders both judged (0-100) and coding (pass@1 as a
    # percentage) category results in the same table — this pins both the
    # grouping and the two value-formatting branches at once.
    row_a = RowRunResult(
        row=RowConfig(label="a", model_id="ollama/qwen-27b", route="litellm", provider_id="ollama"),
        row_dir=Path("/tmp/a"),
        category_results={
            "doc_summary": _judged_result(80.0),
            "coding": CodingResult(dataset="humaneval", pass_at_1=0.5, raw_output=""),
        },
    )
    row_b = RowRunResult(
        row=RowConfig(label="b", model_id="ollama/other", route="litellm", provider_id="ollama"),
        row_dir=Path("/tmp/b"),
        category_results={"doc_summary": _judged_result(60.0)},
    )
    summary = render_summary("test-run", [row_a, row_b], _registry(), ["doc_summary", "coding"])
    assert "qwen3.8" in summary
    assert "80.0" in summary
    assert "50.0%" in summary  # 0.5 pass@1 rendered as a percentage


def test_render_summary_marks_judge_fail_rows():
    # A row whose judge outcome failed (no score) must surface as JUDGE_FAIL
    # in the matrix rather than silently rendering as N/A or a blank cell,
    # since that distinction matters for triaging a run.
    row = RowRunResult(
        row=RowConfig(label="a", model_id="ollama/qwen-27b", route="litellm", provider_id="ollama"),
        row_dir=Path("/tmp/a"),
        category_results={"doc_summary": _judged_result(None)},
    )
    summary = render_summary("test-run", [row], _registry(), ["doc_summary"])
    assert "JUDGE_FAIL" in summary


def test_render_summary_reports_isolation_errors():
    # A row that never ran because provider isolation failed must be called
    # out explicitly (ISOLATION_ERROR) instead of looking like a row with no
    # data, so a run's summary distinguishes "didn't run" from "scored 0".
    row = RowRunResult(
        row=RowConfig(label="a", model_id="ollama/qwen-27b", route="litellm", provider_id="ollama"),
        row_dir=Path("/tmp/a"),
        category_results={},
        error="isolation failed for ollama: not found",
    )
    summary = render_summary("test-run", [row], _registry(), ["doc_summary"])
    assert "ISOLATION_ERROR" in summary


def test_render_summary_shows_partial_results_alongside_an_error():
    # A row can carry BOTH an error and partial category_results (a later
    # category failed after an earlier one already scored) — the matrix
    # must render the completed category's real score, not blanket every
    # cell with ISOLATION_ERROR just because the row also has an error.
    row = RowRunResult(
        row=RowConfig(label="a", model_id="ollama/qwen-27b", route="litellm", provider_id="ollama"),
        row_dir=Path("/tmp/a"),
        category_results={"doc_summary": _judged_result(80.0)},
        error="coding: evalplus timed out",
    )
    summary = render_summary("test-run", [row], _registry(), ["doc_summary", "coding"])
    assert "80.0" in summary
    assert "ISOLATION_ERROR" in summary  # the coding cell, which never ran


def test_write_row_artifacts_persists_partial_results_and_the_error(tmp_path):
    # Regression test: write_row_artifacts used to return immediately when
    # `error` was set, before writing any category artifacts, discarding a
    # category's real (possibly API-billed) result whenever a row also
    # carried an error for a later, failed category. Both must be written.
    row = RowRunResult(
        row=RowConfig(label="a", model_id="ollama/qwen-27b", route="litellm", provider_id="ollama"),
        row_dir=tmp_path / "01--a",
        category_results={"doc_summary": _judged_result(80.0)},
        error="coding: evalplus timed out",
    )
    write_row_artifacts(row)
    item_dir = tmp_path / "01--a" / "doc_summary" / "i1"
    assert (item_dir / "response.txt").is_file()
    assert (tmp_path / "01--a" / "error.txt").read_text() == "coding: evalplus timed out"


def test_write_row_artifacts_writes_response_and_judge_json(tmp_path):
    # Per-item artifacts (response text + judge JSON) must land under
    # row_dir/<category>/<item_id>/ so later tooling (and humans) can inspect
    # exactly what a model said and how the judge scored it.
    row = RowRunResult(
        row=RowConfig(label="a", model_id="ollama/qwen-27b", route="litellm", provider_id="ollama"),
        row_dir=tmp_path / "01--a",
        category_results={"doc_summary": _judged_result(80.0)},
    )
    write_row_artifacts(row)
    item_dir = tmp_path / "01--a" / "doc_summary" / "i1"
    assert (item_dir / "response.txt").read_text() == "r"
    judge_data = json.loads((item_dir / "judge.json").read_text())
    assert judge_data["status"] == "scored"


def test_write_row_artifacts_writes_score_json_for_coding_category(tmp_path):
    # The coding category (a CodingResult, not a CategoryRowResult) must also
    # get a score.json, normalized to the same /100 scale as judged
    # categories' score.json — otherwise a per-row rollup that reads
    # score.json across categories would silently skip coding rows.
    row = RowRunResult(
        row=RowConfig(label="a", model_id="ollama/qwen-27b", route="litellm", provider_id="ollama"),
        row_dir=tmp_path / "01--a",
        category_results={
            "coding": CodingResult(dataset="humaneval", pass_at_1=0.5, raw_output="")
        },
    )
    write_row_artifacts(row)
    score_data = json.loads((tmp_path / "01--a" / "coding" / "score.json").read_text())
    assert score_data["score_100"] == 50.0


def test_write_run_toml_persists_judge_config_for_rejudging(tmp_path):
    # run.toml is the suite snapshot a later rejudge_run reads back to
    # reconstruct a judge transport with no Suite object in hand — it must
    # carry the git sha plus enough of [judge]/[coding] to do that.
    from modelman.benchmark.eval.suite import CodingConfig, JudgeConfig, Suite

    suite = Suite(
        name="t",
        cooldown_s=15.0,
        judge=JudgeConfig(model="j", temperature=0.1, samples=2, max_attempts=2, route="litellm"),
        coding=CodingConfig(dataset="humaneval", limit=10),
        rows=[],
    )
    from modelman.benchmark.eval.report import write_run_toml

    out_path = tmp_path / "run.toml"
    write_run_toml(out_path, suite, git_sha="abc123")
    import tomllib

    with out_path.open("rb") as f:
        data = tomllib.load(f)
    assert data["run"]["git_sha"] == "abc123"
    assert data["suite"]["judge"]["model"] == "j"
    assert data["suite"]["judge"]["samples"] == 2
    assert data["suite"]["coding"]["dataset"] == "humaneval"


def test_write_metrics_jsonl_writes_one_line_per_row(tmp_path):
    # metrics.jsonl is the machine-readable counterpart to summary.md: one
    # JSON line per row with its label and per-category scores, for
    # downstream tooling that shouldn't have to parse markdown tables.
    row = RowRunResult(
        row=RowConfig(label="a", model_id="ollama/qwen-27b", route="litellm", provider_id="ollama"),
        row_dir=tmp_path / "01--a",
        category_results={"doc_summary": _judged_result(80.0)},
    )
    out_path = tmp_path / "metrics.jsonl"
    write_metrics_jsonl(out_path, [row])
    lines = out_path.read_text().splitlines()
    assert len(lines) == 1
    record = json.loads(lines[0])
    assert record["label"] == "a"
    assert record["categories"]["doc_summary"] == 80.0


def test_render_summary_escapes_pipes_and_newlines_in_table_cells():
    # Error text and labels are free-form: a `|` would add a column and a
    # newline would end the row, corrupting the summary tables. Both must
    # be neutralized so every table line keeps its column count.
    row = RowRunResult(
        row=RowConfig(
            label="a|b", model_id="ollama/qwen-27b", route="litellm", provider_id="ollama"
        ),
        row_dir=Path("/tmp/a"),
        category_results={},
        error="boom | bad\nsecond line",
    )
    summary = render_summary("test-run", [row], _registry(), ["doc_summary"])
    assert "a\\|b" in summary
    assert "boom \\| bad second line" in summary
    anomaly_line = next(line for line in summary.splitlines() if "ISOLATION_ERROR: boom" in line)
    assert anomaly_line.count("|") - anomaly_line.count("\\|") == 3


def test_partially_judged_category_is_marked_and_ranked_below_a_complete_one():
    # A category where only 1 of 5 items survived judging has a score that
    # is a mean over the survivor; it must be flagged in the matrix and must
    # not outrank a row that scored every item, even at a higher number.
    def result(scores: list[float | None]) -> CategoryRowResult:
        items = []
        for i, score in enumerate(scores):
            base = _judged_result(score).items[0]
            base.item_id = f"i{i}"
            items.append(base)
        scored = [s for s in scores if s is not None]
        return CategoryRowResult(
            category="doc_summary", items=items, score_100=sum(scored) / len(scored)
        )

    partial = RowRunResult(
        row=RowConfig(
            label="partial", model_id="ollama/qwen-27b", route="litellm", provider_id="ollama"
        ),
        row_dir=Path("/tmp/p"),
        category_results={"doc_summary": result([95.0, None, None, None, None])},
    )
    full = RowRunResult(
        row=RowConfig(label="full", model_id="ollama/other", route="litellm", provider_id="ollama"),
        row_dir=Path("/tmp/f"),
        category_results={"doc_summary": result([88.0] * 5)},
    )
    summary = render_summary("test-run", [partial, full], _registry(), ["doc_summary"])
    assert "95.0 (1/5 items)" in summary
    leaderboard = summary.split("## Per-category leaderboard")[1].split("## Anomalies")[0]
    assert leaderboard.index("full") < leaderboard.index("partial")


def test_category_aborted_mid_generation_is_marked_partial():
    # A category whose generation aborted after 1 of 5 items keeps only that
    # one item; if that item scores fully, scored == len(items) and the
    # score used to read as a complete run. expected_items must expose the
    # 4 items that were never generated, or the leaderboard misranks it.
    item = _judged_result(95.0).items[0]
    aborted = RowRunResult(
        row=RowConfig(
            label="aborted", model_id="ollama/qwen-27b", route="litellm", provider_id="ollama"
        ),
        row_dir=Path("/tmp/a"),
        category_results={
            "doc_summary": CategoryRowResult(
                category="doc_summary", items=[item], score_100=95.0, expected_items=5
            )
        },
    )
    summary = render_summary("test-run", [aborted], _registry(), ["doc_summary"])
    assert "95.0 (1/5 items)" in summary
