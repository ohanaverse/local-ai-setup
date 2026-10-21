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


def test_prompt_files_with_the_same_stem_are_a_usage_error(tmp_path):
    a, b = tmp_path / "a" / "p.txt", tmp_path / "b" / "p.txt"
    for f in (a, b):
        f.parent.mkdir()
        f.write_text("hi")
    result = _invoke(
        "--target", "a=http://x/v1,m", "--no-builtin",
        "--prompt-file", str(a), "--prompt-file", str(b), "--out", str(tmp_path / "o"),
    )  # fmt: skip
    assert result.exit_code == 2, result.output
    assert "duplicate prompt id" in result.output


def test_a_prompt_file_that_is_not_utf8_is_a_usage_error(tmp_path):
    bad = tmp_path / "bad.txt"
    bad.write_bytes(b"\xff\xfe\x00bad")
    result = _invoke(
        "--target", "a=http://x/v1,m", "--no-builtin",
        "--prompt-file", str(bad), "--out", str(tmp_path / "o"),
    )  # fmt: skip
    assert result.exit_code == 2, result.output
    assert "cannot read prompt file" in result.output


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


@pytest.fixture
def read_only_dir(tmp_path):
    path = tmp_path / "ro"
    path.mkdir()
    path.chmod(0o500)
    yield path
    path.chmod(0o700)


@pytest.mark.parametrize("kind", ["read-only dir", "under a read-only dir", "a file"])
def test_an_unwritable_out_exits_2_before_any_request(
    kind, fake_openai, tmp_path, read_only_dir, prompt_file
):
    if kind == "read-only dir":
        out = read_only_dir
    elif kind == "under a read-only dir":
        out = read_only_dir / "sub"
    else:
        out = tmp_path / "a-file"
        out.write_text("x", encoding="utf-8")
    result = _invoke(
        "--target",
        f"a={fake_openai.base_url},no-reasoning",
        "--no-builtin",
        "--prompt-file",
        str(prompt_file),
        "--out",
        str(out),
    )
    assert result.exit_code == 2, result.output
    assert "--out" in result.output
    assert fake_openai.requests == []


def test_results_are_written_after_each_request(fake_openai, tmp_path, prompt_file, monkeypatch):
    """After every finished request results.json and report.md already hold
    it, so a run that dies later keeps what it finished."""
    import llmbench.probe.thinking as thinking

    out = tmp_path / "out"
    seen_on_disk: list[int] = []
    real_probe_one = thinking.probe_one

    def probe_one_then_look(*args, **kwargs):
        if (out / "results.json").exists():
            doc = json.loads((out / "results.json").read_text(encoding="utf-8"))
            seen_on_disk.append(len(doc["results"]))
        else:
            seen_on_disk.append(0)
        return real_probe_one(*args, **kwargs)

    monkeypatch.setattr(thinking, "probe_one", probe_one_then_look)
    result = _invoke(
        "--target",
        f"a={fake_openai.base_url},no-reasoning",
        "--target",
        f"b={fake_openai.base_url},reasoning-field",
        "--no-builtin",
        "--prompt-file",
        str(prompt_file),
        "--out",
        str(out),
    )
    assert result.exit_code == 0, result.output
    assert seen_on_disk == [0, 1]
    assert [p.name for p in sorted(out.iterdir())] == ["report.md", "results.json"]


def test_a_report_write_failure_mid_run_warns_and_the_run_goes_on(
    fake_openai, tmp_path, prompt_file, monkeypatch
):
    import llmbench.probe.cli as probe_cli

    real_write = probe_cli.write_outputs
    calls = {"n": 0}

    def flaky(run, out_dir):
        calls["n"] += 1
        if calls["n"] == 2:  # the first request's write
            raise OSError(28, "No space left on device")
        return real_write(run, out_dir)

    monkeypatch.setattr(probe_cli, "write_outputs", flaky)
    result = _invoke(
        "--target",
        f"a={fake_openai.base_url},no-reasoning",
        "--no-builtin",
        "--prompt-file",
        str(prompt_file),
        "--out",
        str(tmp_path / "out"),
    )
    assert result.exit_code == 0, result.output
    assert "could not write the report" in result.output


def test_an_interrupt_keeps_the_finished_requests_and_exits_130(
    fake_openai, tmp_path, prompt_file, monkeypatch
):
    """Ctrl-C during the second request: the first request's row is on disk,
    the in-flight (target, prompt) is named, and the exit is 130."""
    import llmbench.probe.thinking as thinking

    out = tmp_path / "out"
    calls = []
    real_probe_one = thinking.probe_one

    def interrupted_on_second(*args, **kwargs):
        calls.append(args[0].name)
        if len(calls) == 2:
            raise KeyboardInterrupt
        return real_probe_one(*args, **kwargs)

    monkeypatch.setattr(thinking, "probe_one", interrupted_on_second)
    result = _invoke(
        "--target",
        f"a={fake_openai.base_url},no-reasoning",
        "--target",
        f"b={fake_openai.base_url},slow",
        "--target",
        f"c={fake_openai.base_url},no-reasoning",
        "--no-builtin",
        "--prompt-file",
        str(prompt_file),
        "--timeout",
        "0.5",
        "--out",
        str(out),
    )
    assert result.exit_code == 130, result.output
    assert calls == ["a", "b"]
    assert "interrupted" in result.output
    assert "b / custom" in result.output
    doc = json.loads((out / "results.json").read_text(encoding="utf-8"))
    assert [(r["target"], r["prompt_id"], r["answered"]) for r in doc["results"]] == [
        ("a", "custom", True)
    ]
    assert "## Prompt `custom`" in (out / "report.md").read_text(encoding="utf-8")
    assert [p.name for p in sorted(out.iterdir())] == ["report.md", "results.json"]


# --- sampling penalties (#403) ----------------------------------------------

# The request body's keys when no penalty flag is given: the same set as
# before the flags existed.
DEFAULT_BODY_KEYS = {"model", "messages", "temperature", "top_p", "top_k", "stream"}


def _penalty_run(fake_openai, tmp_path, prompt_file, *flags: str, model: str = "no-reasoning"):
    out = tmp_path / "out"
    result = _invoke(
        "--target",
        f"a={fake_openai.base_url},{model}",
        "--no-builtin",
        "--prompt-file",
        str(prompt_file),
        *flags,
        "--out",
        str(out),
    )
    return result, out


def test_no_penalty_flag_leaves_the_request_body_unchanged(fake_openai, tmp_path, prompt_file):
    result, _ = _penalty_run(fake_openai, tmp_path, prompt_file)
    assert result.exit_code == 0, result.output
    (request,) = fake_openai.requests
    assert set(request["body"]) == DEFAULT_BODY_KEYS


@pytest.mark.parametrize(
    ("flag", "value", "field", "sent"),
    [
        ("--presence-penalty", "1.5", "presence_penalty", 1.5),
        ("--presence-penalty", "-2.0", "presence_penalty", -2.0),
        ("--presence-penalty", "0", "presence_penalty", 0.0),
        ("--frequency-penalty", "2.0", "frequency_penalty", 2.0),
        ("--repetition-penalty", "1.1", "repetition_penalty", 1.1),
    ],
)
def test_each_penalty_flag_adds_exactly_its_field(
    flag, value, field, sent, fake_openai, tmp_path, prompt_file
):
    result, _ = _penalty_run(fake_openai, tmp_path, prompt_file, flag, value)
    assert result.exit_code == 0, result.output
    (request,) = fake_openai.requests
    body = request["body"]
    assert set(body) == DEFAULT_BODY_KEYS | {field}
    assert body[field] == sent


def test_all_three_penalties_are_sent_together(fake_openai, tmp_path, prompt_file):
    result, out = _penalty_run(
        fake_openai,
        tmp_path,
        prompt_file,
        "--presence-penalty",
        "1.5",
        "--frequency-penalty",
        "0.5",
        "--repetition-penalty",
        "1.05",
    )
    assert result.exit_code == 0, result.output
    (request,) = fake_openai.requests
    body = request["body"]
    assert set(body) == DEFAULT_BODY_KEYS | {
        "presence_penalty",
        "frequency_penalty",
        "repetition_penalty",
    }
    assert (body["presence_penalty"], body["frequency_penalty"], body["repetition_penalty"]) == (
        1.5,
        0.5,
        1.05,
    )
    sampling = json.loads((out / "results.json").read_text(encoding="utf-8"))["sampling"]
    assert sampling["presence_penalty"] == 1.5
    assert sampling["frequency_penalty"] == 0.5
    assert sampling["repetition_penalty"] == 1.05
    report = (out / "report.md").read_text(encoding="utf-8")
    assert "presence_penalty 1.5, frequency_penalty 0.5, repetition_penalty 1.05" in report, report


@pytest.mark.parametrize(
    ("args", "fragment"),
    [
        (["--presence-penalty", "2.5"], "--presence-penalty"),
        (["--presence-penalty", "-2.01"], "--presence-penalty"),
        (["--presence-penalty", "nan"], "--presence-penalty"),
        (["--presence-penalty", "inf"], "--presence-penalty"),
        (["--presence-penalty", "abc"], "--presence-penalty"),
        (["--frequency-penalty", "3"], "--frequency-penalty"),
        (["--frequency-penalty", "-inf"], "--frequency-penalty"),
        (["--frequency-penalty", "NaN"], "--frequency-penalty"),
        (["--repetition-penalty", "0"], "--repetition-penalty"),
        (["--repetition-penalty", "-1"], "--repetition-penalty"),
        (["--repetition-penalty", "nan"], "--repetition-penalty"),
        (["--repetition-penalty", "infinity"], "--repetition-penalty"),
        (["--repetition-penalty", "x1.1"], "--repetition-penalty"),
    ],
)
def test_bad_penalty_values_exit_2_and_send_nothing(
    args, fragment, fake_openai, tmp_path, prompt_file
):
    result, out = _penalty_run(fake_openai, tmp_path, prompt_file, *args)
    assert result.exit_code == 2, result.output
    assert fragment in result.output
    assert fake_openai.requests == []
    assert not out.exists()


def test_a_server_that_rejects_a_penalty_is_an_error_row_and_the_run_goes_on(
    fake_openai, tmp_path, prompt_file
):
    out = tmp_path / "out"
    result = _invoke(
        "--target",
        f"strict={fake_openai.base_url},rejects-penalties",
        "--target",
        f"lenient={fake_openai.base_url},no-reasoning",
        "--no-builtin",
        "--prompt-file",
        str(prompt_file),
        "--presence-penalty",
        "1.5",
        "--out",
        str(out),
    )
    assert result.exit_code == 0, result.output
    assert len(fake_openai.requests) == 2
    doc = json.loads((out / "results.json").read_text(encoding="utf-8"))
    strict, lenient = doc["results"]
    assert strict["answered"] is False
    assert strict["status"] == 400
    assert strict["error"].startswith("HTTP 400:")
    assert "Unrecognized request argument supplied: presence_penalty" in strict["error"]
    assert lenient["answered"] is True
    report = (out / "report.md").read_text(encoding="utf-8")
    assert "Unrecognized request argument supplied: presence_penalty" in report


def test_the_report_and_json_record_only_the_penalties_sent(fake_openai, tmp_path, prompt_file):
    result, out = _penalty_run(fake_openai, tmp_path, prompt_file, "--presence-penalty", "1.5")
    assert result.exit_code == 0, result.output
    report = (out / "report.md").read_text(encoding="utf-8")
    (sampling_line,) = [line for line in report.splitlines() if line.startswith("- Sampling:")]
    assert "presence_penalty 1.5" in sampling_line
    assert "frequency_penalty" not in sampling_line
    assert "repetition_penalty" not in sampling_line
    sampling = json.loads((out / "results.json").read_text(encoding="utf-8"))["sampling"]
    assert sampling == {
        "temperature": 1.0,
        "top_p": 0.95,
        "top_k": 20,
        "max_tokens": None,
        "presence_penalty": 1.5,
    }


def test_without_penalties_the_report_and_json_name_none(fake_openai, tmp_path, prompt_file):
    result, out = _penalty_run(fake_openai, tmp_path, prompt_file)
    assert result.exit_code == 0, result.output
    report = (out / "report.md").read_text(encoding="utf-8")
    assert "penalty" not in report
    sampling = json.loads((out / "results.json").read_text(encoding="utf-8"))["sampling"]
    assert set(sampling) == {"temperature", "top_p", "top_k", "max_tokens"}


def test_help_lists_the_penalty_flags_and_the_silent_ignore_caveat():
    result = _invoke("--help")
    assert result.exit_code == 0, result.output
    for flag in ("--presence-penalty", "--frequency-penalty", "--repetition-penalty"):
        assert flag in result.output
    # Rich wraps the help inside a box: drop its borders and rejoin the words.
    text = " ".join(result.output.replace("│", " ").split())
    assert "ignored silently" in text
    assert "not sent" in text
