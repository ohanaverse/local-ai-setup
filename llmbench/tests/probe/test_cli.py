"""`llmbench probe thinking` through Typer's CliRunner, against the fake
endpoint in conftest.py. Every run passes --out under tmp_path."""

import json
from pathlib import Path

import pytest
from typer.testing import CliRunner

from llmbench.main import app

runner = CliRunner()
REPO_CATEGORIES = Path(__file__).resolve().parents[3] / "benchmarks" / "tasks" / "eval"
SECRET = "sk-probe-not-a-real-key-7f3a"


@pytest.fixture
def prompt_file(tmp_path):
    path = tmp_path / "custom.txt"
    path.write_text("Explain caching.", encoding="utf-8")
    return path


def _invoke(*args: str):
    return runner.invoke(app, ["probe", "thinking", *args])


def test_probe_is_mounted():
    result = runner.invoke(app, ["probe", "--help"])
    assert result.exit_code == 0, result.output
    assert "thinking" in result.output


def test_help_states_the_sampling_defaults():
    result = _invoke("--help")
    assert result.exit_code == 0, result.output
    assert "generation_config" in result.output
    assert "1.0" in result.output
    assert "0.95" in result.output
    assert "20" in result.output


def test_builtin_run_writes_report_and_json(fake_openai, tmp_path, monkeypatch):
    monkeypatch.setenv("PROBE_TEST_KEY", SECRET)
    out = tmp_path / "out"
    result = _invoke(
        "--target",
        f"a={fake_openai.base_url},reasoning-content,PROBE_TEST_KEY",
        "--target",
        f"b={fake_openai.base_url},reasoning-tokens",
        "--root",
        str(REPO_CATEGORIES),
        "--out",
        str(out),
    )
    assert result.exit_code == 0, result.output
    report = (out / "report.md").read_text(encoding="utf-8")
    doc = json.loads((out / "results.json").read_text(encoding="utf-8"))
    assert [(r["target"], r["prompt_id"]) for r in doc["results"]] == [
        ("a", "arithmetic"),
        ("a", "planning/flaky-test-suite"),
        ("b", "arithmetic"),
        ("b", "planning/flaky-test-suite"),
    ]
    assert "## Prompt `arithmetic`" in report
    assert str(out / "report.md") in result.output
    # Two prompts x two targets, each with the sampling the issue used.
    assert len(fake_openai.requests) == 4
    for request in fake_openai.requests:
        body = request["body"]
        assert (body["temperature"], body["top_p"], body["top_k"]) == (1.0, 0.95, 20)
        assert "max_tokens" not in body
    sent_key = [
        r["headers"].get("Authorization") == f"Bearer {SECRET}" for r in fake_openai.requests
    ]
    assert sent_key == [True, True, False, False]
    # The key reaches the header and nothing else: no report file, no output.
    leaked = [p.name for p in out.iterdir() if SECRET in p.read_text(encoding="utf-8")]
    assert leaked == []
    assert (SECRET in result.output) is False


def test_sampling_flags_override_the_defaults(fake_openai, tmp_path, prompt_file):
    result = _invoke(
        "--target",
        f"a={fake_openai.base_url},no-reasoning",
        "--no-builtin",
        "--prompt-file",
        str(prompt_file),
        "--temperature",
        "0.6",
        "--top-p",
        "0.8",
        "--top-k",
        "0",
        "--max-tokens",
        "2048",
        "--out",
        str(tmp_path / "out"),
    )
    assert result.exit_code == 0, result.output
    (request,) = fake_openai.requests
    body = request["body"]
    assert body["messages"][0]["content"] == "Explain caching."
    assert (body["temperature"], body["top_p"], body["max_tokens"]) == (0.6, 0.8, 2048)
    assert "top_k" not in body


def test_exit_1_when_no_target_answered(fake_openai, closed_port_url, tmp_path, prompt_file):
    out = tmp_path / "out"
    result = _invoke(
        "--target",
        f"down={closed_port_url},m",
        "--target",
        f"bad={fake_openai.base_url},server-error",
        "--no-builtin",
        "--prompt-file",
        str(prompt_file),
        "--out",
        str(out),
    )
    assert result.exit_code == 1, result.output
    # The report is still written: the error rows are the finding.
    report = (out / "report.md").read_text(encoding="utf-8")
    assert "unreachable" in report
    assert "HTTP 500" in report


def test_exit_0_when_one_target_answered(fake_openai, closed_port_url, tmp_path, prompt_file):
    result = _invoke(
        "--target",
        f"down={closed_port_url},m",
        "--target",
        f"up={fake_openai.base_url},reasoning-field",
        "--no-builtin",
        "--prompt-file",
        str(prompt_file),
        "--timeout",
        "2",
        "--out",
        str(tmp_path / "out"),
    )
    assert result.exit_code == 0, result.output


@pytest.mark.parametrize(
    ("args", "fragment"),
    [
        ([], "--target"),
        (["--target", "nonsense"], "NAME="),
        (["--target", "a=http://x/v1,m", "--target", "a=http://y/v1,m"], "duplicate"),
        (["--target", "a=http://x/v1,m,PROBE_UNSET_KEY"], "PROBE_UNSET_KEY"),
        (["--target", "a=http://x/v1,m", "--no-builtin"], "no prompts"),
    ],
)
def test_usage_errors_exit_2_before_any_request(args, fragment, tmp_path, monkeypatch):
    monkeypatch.delenv("PROBE_UNSET_KEY", raising=False)
    out = tmp_path / "out"
    result = _invoke(*args, "--out", str(out))
    assert result.exit_code == 2, result.output
    assert fragment in result.output
    assert not out.exists()


def test_missing_planning_category_is_a_usage_error(tmp_path):
    result = _invoke(
        "--target", "a=http://x/v1,m", "--root", str(tmp_path), "--out", str(tmp_path / "o")
    )
    assert result.exit_code == 2, result.output
    assert "--no-builtin" in result.output


def test_default_out_is_a_new_directory_under_the_temp_dir(
    fake_openai, tmp_path, monkeypatch, prompt_file
):
    """Not ~/.config/local-ai/benchmarks: the probe keeps no benchmark state."""
    monkeypatch.setattr("tempfile.tempdir", str(tmp_path))
    result = _invoke(
        "--target",
        f"a={fake_openai.base_url},no-reasoning",
        "--no-builtin",
        "--prompt-file",
        str(prompt_file),
    )
    assert result.exit_code == 0, result.output
    (made,) = [p for p in tmp_path.iterdir() if p.name.startswith("llmbench-probe-thinking-")]
    assert (made / "report.md").is_file()
    assert (made / "results.json").is_file()
    assert not (Path.home() / ".config" / "local-ai").exists()
