"""The `wt warm` bridge: the one wt call llmbench makes."""

import subprocess

import pytest

from llmbench import wt_bridge


def _cp(stdout="", returncode=0, stderr=""):
    return subprocess.CompletedProcess(args=[], returncode=returncode, stdout=stdout, stderr=stderr)


@pytest.fixture
def wt_on_path(monkeypatch):
    monkeypatch.setattr(wt_bridge.shutil, "which", lambda name: "/usr/local/bin/wt")


def test_warm_runs_wt_warm_and_reports_its_failure(monkeypatch, wt_on_path):
    """`wt warm <provider> <model>` is the keyed warmup llmbench cannot do
    itself (#256). A failure raises with wt's own line, which names the fix,
    and the timeout outlasts wt's warmup budget."""
    seen = []
    result = {"cp": _cp("Qwen-4bit is loaded on omlx\n")}

    def run(argv, **kwargs):
        seen.append((argv, kwargs.get("timeout")))
        return result["cp"]

    monkeypatch.setattr(wt_bridge.subprocess, "run", run)

    wt_bridge.warm("omlx", "Qwen-4bit")
    assert seen == [(["wt", "warm", "omlx", "Qwen-4bit"], wt_bridge.WARM_TIMEOUT)]
    assert wt_bridge.WARM_TIMEOUT > 600

    result["cp"] = _cp(returncode=1, stderr="Error: x wants an API key\nwt: x wants an API key\n")
    with pytest.raises(wt_bridge.WtBridgeError, match="^x wants an API key$"):
        wt_bridge.warm("omlx", "Qwen-4bit")


def test_warm_without_wt_on_path(monkeypatch):
    monkeypatch.setattr(wt_bridge.shutil, "which", lambda name: None)
    with pytest.raises(wt_bridge.WtNotFoundError, match="make install"):
        wt_bridge.warm("omlx", "Qwen-4bit")


@pytest.mark.parametrize(
    ("exc", "want", "message"),
    [
        (
            subprocess.TimeoutExpired(cmd=["wt"], timeout=630),
            wt_bridge.WtBridgeTimeoutError,
            "wt warm timed out after 630s",
        ),
        (FileNotFoundError("wt"), wt_bridge.WtNotFoundError, "wt not found on PATH"),
        (PermissionError("denied"), wt_bridge.WtBridgeError, "wt warm could not run"),
    ],
    ids=["timeout", "vanished", "not-executable"],
)
def test_warm_turns_a_subprocess_failure_into_a_bridge_error(
    monkeypatch, wt_on_path, exc, want, message
):
    """Every way the subprocess can fail reaches the caller as a
    WtBridgeError: the omlx backend catches that one type and turns it into
    the lifecycle's error envelope instead of a traceback."""

    def run(argv, **kwargs):
        raise exc

    monkeypatch.setattr(wt_bridge.subprocess, "run", run)
    with pytest.raises(want, match=message):
        wt_bridge.warm("omlx", "Qwen-4bit")


def test_msg_falls_back_when_wt_printed_nothing():
    assert wt_bridge._msg(_cp(returncode=3), "wt exited 3") == "wt exited 3"
    assert wt_bridge._msg(_cp(stdout="only stdout\n", returncode=1), "x") == "only stdout"


# One argv for every _msg case, carrying what _msg exists to keep out of a
# traceback. The value is fake.
_FAKE_KEY = "sk-FAKE-not-a-real-key-0000"
_ARGV_WITH_KEY = ["wt", "litellm", "set", "--api-key", _FAKE_KEY]

# (case id, stdout, stderr, the one line _msg must produce)
_MSG_CASES = [
    ("cobra Error: prefix", "", "Error: no such model\n", "no such model"),
    ("wt: prefix", "", "wt: no such model\n", "no such model"),
    ("both prefixes on one line", "", "Error: wt: no such model\n", "no such model"),
    (
        "cobra's line and wt's own line collapse to one",
        "",
        "Error: no such model\nwt: no such model\n",
        "no such model",
    ),
    ("distinct lines join with '; '", "", "Error: first\nwt: second\n", "first; second"),
    ("blank lines and padding are dropped", "", "\n  Error:   padded  \n\n", "padded"),
    # The whole output is stripped before it is split, so only a line after the
    # first can show that each line is stripped too, before its prefix is looked for.
    ("an indented later line still loses its prefix", "", "Error: a\n  wt: b\n", "a; b"),
    # The only case with a blank line between two others, which is what
    # `if line` is there for.
    ("a blank line between two lines is dropped", "", "Error: a\n\nwt: b\n", "a; b"),
    ("an unprefixed line passes through", "", "plain failure\n", "plain failure"),
    ("a prefix is stripped only at the start of a line", "", "saw Error: x\n", "saw Error: x"),
    ("stdout is used when stderr is empty", "wt: from stdout\n", "", "from stdout"),
    ("stdout is used when stderr is only whitespace", "wt: from stdout\n", "  \n", "from stdout"),
    ("stderr wins over stdout", "wt: from stdout\n", "wt: from stderr\n", "from stderr"),
    ("no output at all gives the fallback", "", "", "the fallback"),
    ("only whitespace gives the fallback", " \n", "\n\n", "the fallback"),
]


@pytest.mark.parametrize(
    ("stdout", "stderr", "want"),
    [case[1:] for case in _MSG_CASES],
    ids=[case[0] for case in _MSG_CASES],
)
def test_msg_is_one_clean_line_and_never_shows_argv(stdout, stderr, want):
    """#276: `_msg` builds the text of every error the bridge raises from wt's
    output alone. It must strip cobra's `Error: ` and wt's own `wt: ` prefix,
    say a repeated line once, prefer stderr, and never interpolate argv: a
    caller that one day passes `--api-key <value>` would otherwise print the
    key in a traceback or a benchmark report."""
    proc = subprocess.CompletedProcess(
        args=_ARGV_WITH_KEY, returncode=1, stdout=stdout, stderr=stderr
    )
    got = wt_bridge._msg(proc, "the fallback")
    for secret in ("--api-key", _FAKE_KEY):
        assert secret not in got, "_msg put argv in its message; build it from wt's output only"
    assert got == want
