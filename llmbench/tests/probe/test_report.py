"""Report rendering: the markdown tables and the JSON document."""

import json

from llmbench.probe.thinking import (
    ProbeResult,
    ProbeRun,
    Prompt,
    Sampling,
    Target,
    render_markdown,
    run_json,
    write_outputs,
)

TARGETS = [
    Target(name="omlx", base_url="http://localhost:8000/v1", model="Q-4bit", api_key_env="OMLX_K"),
    Target(name="mtplx", base_url="http://localhost:8003/v1", model="Org/Q", api_key_env=None),
]
PROMPTS = [
    Prompt(id="arith", text="What is 17*23?", source="builtin"),
    Prompt(id="plan", text="Plan it.", source="/tmp/plan.txt"),
]


def _run() -> ProbeRun:
    return ProbeRun(
        started_at="2026-10-21T12:00:00Z",
        targets=TARGETS,
        prompts=PROMPTS,
        sampling=Sampling(temperature=1.0, top_p=0.95, top_k=20, max_tokens=None),
        timeout_s=900,
        results=[
            ProbeResult(
                target="omlx",
                prompt_id="arith",
                status=200,
                reasoning="We need answer | simple\nmultiplication.",
                reasoning_field="reasoning_content",
                reasoning_tokens=24,
                answer="391",
                completion_tokens=30,
                finish_reason="stop",
                elapsed_s=1.234,
            ),
            ProbeResult(
                target="mtplx",
                prompt_id="arith",
                status=500,
                error="HTTP 500: boom | bad",
                elapsed_s=0.1,
            ),
            ProbeResult(
                target="omlx",
                prompt_id="plan",
                status=200,
                reasoning="",
                answer="# H\n- a\n```\n# c\n```\n",
                finish_reason="length",
            ),
            ProbeResult(target="mtplx", prompt_id="plan", error="timed out after 900s"),
        ],
    )


def _table_rows(markdown: str, prompt_id: str) -> list[str]:
    section = markdown.split(f"## Prompt `{prompt_id}`", 1)[1].split("\n## ", 1)[0]
    return [line for line in section.splitlines() if line.startswith("| ")]


def test_one_table_per_prompt_one_row_per_target():
    markdown = render_markdown(_run())
    for prompt in PROMPTS:
        rows = _table_rows(markdown, prompt.id)
        # header + one row per target (the |---| separator starts with "|-")
        assert len(rows) == 1 + len(TARGETS)
        assert rows[1].startswith("| omlx |")
        assert rows[2].startswith("| mtplx |")


def test_row_values_and_missing_values():
    markdown = render_markdown(_run())
    omlx, mtplx = _table_rows(markdown, "arith")[1:]
    cells = [c.strip() for c in omlx.strip("|").split("|")]
    assert cells == ["omlx", "200", "39", "24", "3", "30", "stop", "0", "0", "0", "0", "0", "1.2"]
    mt_cells = [c.strip() for c in mtplx.strip("|").split("|")]
    assert mt_cells[1] == "HTTP 500"
    assert mt_cells[2:] == ["—"] * 10 + ["0.1"]


def test_shape_columns_skip_fenced_code():
    markdown = render_markdown(_run())
    omlx = _table_rows(markdown, "plan")[1]
    cells = [c.strip() for c in omlx.strip("|").split("|")]
    # headings, bullets, numbered, table, fences
    assert cells[7:12] == ["1", "1", "0", "0", "1"]
    assert cells[6] == "length"


def test_timeout_row_status_and_error_list():
    markdown = render_markdown(_run())
    mtplx = _table_rows(markdown, "plan")[2]
    assert mtplx.split("|")[2].strip() == "error"
    assert "timed out after 900s" in markdown
    # An error text with a pipe cannot break a table: errors are listed below it.
    assert "HTTP 500: boom | bad" in markdown


def test_reasoning_heads_are_shown_for_style_comparison():
    markdown = render_markdown(_run())
    assert "We need answer | simple\nmultiplication." in markdown


def test_header_names_sampling_and_targets_but_not_keys():
    markdown = render_markdown(_run())
    assert "temperature 1.0" in markdown
    assert "top_p 0.95" in markdown
    assert "top_k 20" in markdown
    assert "http://localhost:8000/v1" in markdown
    assert "Org/Q" in markdown
    # The env var's NAME is fine to show; its value is never known to the report.
    assert "OMLX_K" in markdown


def test_reading_note_says_reasoning_length_compares_within_one_template():
    markdown = render_markdown(_run())
    assert "## Reading this" in markdown
    assert "same chat template" in markdown


def test_json_has_every_result_with_metrics():
    doc = run_json(_run())
    assert doc["sampling"] == {"temperature": 1.0, "top_p": 0.95, "top_k": 20, "max_tokens": None}
    assert doc["timeout_s"] == 900
    assert [t["name"] for t in doc["targets"]] == ["omlx", "mtplx"]
    assert doc["prompts"][0] == {"id": "arith", "source": "builtin", "text": "What is 17*23?"}
    first = doc["results"][0]
    assert first["target"] == "omlx"
    assert first["answered"] is True
    assert first["reasoning_chars"] == 39
    assert first["reasoning_tokens"] == 24
    assert first["reasoning_head"].startswith("We need answer")
    assert first["shape"] == {
        "headings": 0,
        "bullets": 0,
        "numbered": 0,
        "table_lines": 0,
        "code_fences": 0,
    }
    assert doc["results"][1]["answered"] is False
    assert doc["results"][1]["error"] == "HTTP 500: boom | bad"


def test_write_outputs_writes_both_files(tmp_path):
    out = tmp_path / "nested" / "out"
    md_path, json_path = write_outputs(_run(), out)
    assert md_path == out / "report.md"
    assert json_path == out / "results.json"
    assert md_path.read_text(encoding="utf-8").startswith("# Thinking probe")
    assert json.loads(json_path.read_text(encoding="utf-8"))["results"][0]["target"] == "omlx"
