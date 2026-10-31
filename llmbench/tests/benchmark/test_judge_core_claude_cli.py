"""ClaudeCliJudgeTransport: real-subprocess behaviour against a fake CLI.

The fake is a tmp python script (NOT named `claude`: the autouse guard fakes
that basename and would never spawn it) that records argv/env/cwd/stdin as a
JSON line per call and then behaves as scripted by MODE.
"""

from __future__ import annotations

import json
import os
import subprocess
import sys
import textwrap
import time
from pathlib import Path

import pytest

from llmbench.benchmark.judge_core import (
    CLAUDE_CLI_SYSTEM_PROMPT,
    ClaudeCliJudgeTransport,
    JudgeTransportError,
    claude_cli_argv,
    claude_cli_env,
    parse_claude_cli_json,
)

_SCRIPT = """\
#!{python}
import json, os, sys, time
MODE = {mode!r}
stdin = sys.stdin.read()
with open({log!r}, "a") as fh:
    fh.write(json.dumps({{"argv": sys.argv[1:], "env": dict(os.environ),
                          "cwd": os.getcwd(), "stdin": stdin,
                          "cwd_listing": os.listdir(".")}}) + "\\n")
ok = {{"type": "result", "is_error": False, "result": "ECHO:" + stdin[:40]}}
if MODE == "success":
    print(json.dumps(ok))
elif MODE == "noisy":
    print("warning: something\\n" + json.dumps(ok) + "\\ntrailing")
elif MODE == "is_error":
    print(json.dumps({{"is_error": True, "result": "model exploded " + "x" * 400}}))
    sys.exit(1)
elif MODE == "is_error_exit0":
    print(json.dumps({{"is_error": True, "result": "soft failure"}}))
elif MODE == "empty_result":
    print(json.dumps({{"is_error": False, "result": ""}}))
elif MODE == "badutf":
    sys.stdout.buffer.write(b"\\xff\\xfe" + json.dumps(ok).encode())
elif MODE == "nonzero":
    sys.stderr.write("boom on stderr")
    sys.exit(3)
elif MODE == "garbage":
    print("this is not json at all")
elif MODE == "not_logged_in":
    print(json.dumps({{"is_error": True, "result": "Not logged in \\u00b7 Please run /login"}}))
    sys.exit(1)
elif MODE == "slow":
    time.sleep(30)
elif MODE == "flaky":
    n = sum(1 for _ in open({log!r}))
    if n == 1:
        sys.exit(2)
    print(json.dumps(ok))
"""


def _fake(tmp_path: Path, mode: str) -> tuple[str, Path]:
    log = tmp_path / "calls.jsonl"
    script = tmp_path / "fake_cli.py"
    script.write_text(_SCRIPT.format(python=sys.executable, mode=mode, log=str(log)))
    script.chmod(0o755)
    return str(script), log


def _calls(log: Path) -> list[dict]:
    if not log.exists():
        return []
    return [json.loads(line) for line in log.read_text().splitlines()]


def _transport(tmp_path: Path, mode: str, **kw) -> tuple[ClaudeCliJudgeTransport, Path]:
    exe, log = _fake(tmp_path, mode)
    kw.setdefault("retry_backoff_s", 0)
    return ClaudeCliJudgeTransport("opus", executable=exe, **kw), log


def test_success_returns_result_and_sends_prompt_on_stdin(tmp_path):
    t, log = _transport(tmp_path, "success")
    assert t.complete("grade this", temperature=0.7) == "ECHO:grade this"
    (call,) = _calls(log)
    assert call["stdin"] == "grade this"
    # The prompt must never ride on argv.
    assert "grade this" not in call["argv"]
    assert call["argv"] == claude_cli_argv("opus")[1:]


def test_tolerates_stray_text_around_json(tmp_path):
    t, _ = _transport(tmp_path, "noisy")
    assert t.complete("p", temperature=0) == "ECHO:p"


def test_runs_in_fresh_empty_cwd_that_is_removed_after(tmp_path):
    t, log = _transport(tmp_path, "success")
    t.complete("p", temperature=0)
    t.complete("p", temperature=0)
    first, second = _calls(log)
    assert first["cwd_listing"] == []
    assert first["cwd"] != second["cwd"]
    assert not Path(first["cwd"]).exists()


def test_env_is_scrubbed_of_anthropic_and_claude_code_but_keeps_the_rest(tmp_path, monkeypatch):
    monkeypatch.setenv("ANTHROPIC_API_KEY", "sk-secret")
    monkeypatch.setenv("ANTHROPIC_BASE_URL", "http://localhost:4000")
    monkeypatch.setenv("CLAUDE_CODE_USE_BEDROCK", "1")
    monkeypatch.setenv("KEEP_ME", "yes")
    t, log = _transport(tmp_path, "success")
    t.complete("p", temperature=0)
    env = _calls(log)[0]["env"]
    assert not [k for k in env if k.startswith(("ANTHROPIC_", "CLAUDE_CODE_"))]
    assert env["KEEP_ME"] == "yes"
    assert env["HOME"] == os.environ["HOME"]
    assert env["PATH"] == os.environ["PATH"]


def test_claude_cli_env_helper_is_a_copy(monkeypatch):
    monkeypatch.setenv("ANTHROPIC_AUTH_TOKEN", "t")
    env = claude_cli_env()
    assert "ANTHROPIC_AUTH_TOKEN" not in env
    assert os.environ["ANTHROPIC_AUTH_TOKEN"] == "t"


@pytest.mark.parametrize(
    ("model", "expected"),
    [("opus", "opus"), ("anthropic/claude-opus-5-5", "claude-opus-5-5"), ("sonnet", "sonnet")],
)
def test_argv_shape_and_model_normalisation(model, expected):
    argv = claude_cli_argv(model, executable="/x/claude")
    assert argv[:2] == ["/x/claude", "-p"]
    assert argv[argv.index("--model") + 1] == expected
    assert "--bare" not in argv
    assert argv[argv.index("--tools") + 1] == ""
    assert argv[argv.index("--system-prompt") + 1] == CLAUDE_CLI_SYSTEM_PROMPT
    assert argv[argv.index("--output-format") + 1] == "json"
    for flag in (
        "--strict-mcp-config",
        "--disable-slash-commands",
        "--no-session-persistence",
    ):
        assert flag in argv
    assert argv[argv.index("--setting-sources") + 1] == "local"


def test_temperature_is_ignored(tmp_path):
    t, log = _transport(tmp_path, "success")
    t.complete("p", temperature=0.0)
    t.complete("p", temperature=1.0)
    a, b = _calls(log)
    assert a["argv"] == b["argv"]


def test_large_and_non_ascii_prompts_round_trip(tmp_path):
    t, log = _transport(tmp_path, "success")
    prompt = "héllo wörld 日本語 🙂 " * 20000
    t.complete(prompt, temperature=0)
    assert _calls(log)[0]["stdin"] == prompt


def test_is_error_json_raises_with_truncated_cli_text_after_one_retry(tmp_path):
    t, log = _transport(tmp_path, "is_error")
    with pytest.raises(JudgeTransportError, match=r"after retry.*exited 1: model exploded") as ei:
        t.complete("SECRET-PROMPT", temperature=0)
    assert len(_calls(log)) == 2
    assert "SECRET-PROMPT" not in str(ei.value)
    assert len(str(ei.value)) < 400


def test_is_error_with_exit_zero_is_still_a_failure(tmp_path):
    t, _ = _transport(tmp_path, "is_error_exit0")
    with pytest.raises(JudgeTransportError, match="soft failure"):
        t.complete("p", temperature=0)


def test_empty_result_is_a_failure(tmp_path):
    t, log = _transport(tmp_path, "empty_result")
    with pytest.raises(JudgeTransportError, match="empty or non-text"):
        t.complete("p", temperature=0)
    assert len(_calls(log)) == 2


def test_nonzero_exit_reports_code_and_stderr(tmp_path):
    t, _ = _transport(tmp_path, "nonzero")
    with pytest.raises(JudgeTransportError, match=r"exited 3: boom on stderr"):
        t.complete("p", temperature=0)


def test_garbage_output_reports_stdout_head(tmp_path):
    t, log = _transport(tmp_path, "garbage")
    with pytest.raises(JudgeTransportError, match="this is not json"):
        t.complete("p", temperature=0)
    assert len(_calls(log)) == 2


def test_retry_recovers_from_a_transient_failure(tmp_path):
    t, log = _transport(tmp_path, "flaky")
    assert t.complete("p", temperature=0) == "ECHO:p"
    assert len(_calls(log)) == 2


def test_not_logged_in_fails_without_retry_and_points_at_login(tmp_path):
    t, log = _transport(tmp_path, "not_logged_in")
    with pytest.raises(JudgeTransportError, match="claude auth login"):
        t.complete("p", temperature=0)
    assert len(_calls(log)) == 1


def test_missing_executable_fails_without_retry(tmp_path):
    t = ClaudeCliJudgeTransport("opus", executable=str(tmp_path / "nope"), retry_backoff_s=0)
    with pytest.raises(JudgeTransportError, match="claude CLI not found"):
        t.complete("p", temperature=0)


def test_timeout_kills_child_and_mentions_timeout_s(tmp_path):
    t, log = _transport(tmp_path, "slow", timeout_s=1)
    start = time.monotonic()
    with pytest.raises(JudgeTransportError, match=r"timed out after 1s"):
        t.complete("p", temperature=0)
    # Two attempts at ~1s each; the 30s sleep was cut short by the kill.
    assert time.monotonic() - start < 15
    assert len(_calls(log)) == 2


def test_constructor_does_no_io():
    t = ClaudeCliJudgeTransport("opus", executable="/definitely/not/here")
    assert t.model == "opus"


@pytest.mark.parametrize(
    ("text", "expected"),
    [
        ('{"a": 1}', {"a": 1}),
        ('noise {"a": 1} tail', {"a": 1}),
        ('warn {a} oops\n{"is_error": false, "result": "hi"}', {"is_error": False, "result": "hi"}),
        ("no braces", None),
        ("{broken", None),
        ("{} ", {}),
    ],
)
def test_parse_claude_cli_json(text, expected):
    assert parse_claude_cli_json(text) == expected


def test_fake_script_really_spawns_a_child(tmp_path):
    """Guards the guard: a fake not named like a blocked binary must run."""
    exe, log = _fake(tmp_path, "success")
    subprocess.run([exe], input="x", text=True, capture_output=True, check=True)
    assert len(_calls(log)) == 1


def test_default_executable_name_is_claude():
    assert claude_cli_argv("opus")[0] == "claude"
    assert textwrap.dedent(CLAUDE_CLI_SYSTEM_PROMPT).startswith("You are a strict grading")


def test_default_claude_executable_is_refused_by_the_hermetic_guard():
    """No test may ever reach the real CLI through the default executable."""
    t = ClaudeCliJudgeTransport("opus", retry_backoff_s=0)
    with pytest.raises(AssertionError, match="blocked binary"):
        t.complete("p", temperature=0)


def test_lone_surrogate_prompt_does_not_raise_unicode_error(tmp_path):
    t, _ = _transport(tmp_path, "success")
    assert t.complete("a\ud800b", temperature=0).startswith("ECHO:a")


def test_non_utf8_output_is_still_parsed(tmp_path):
    t, _ = _transport(tmp_path, "badutf")
    assert t.complete("p", temperature=0) == "ECHO:p"
