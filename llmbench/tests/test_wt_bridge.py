"""The wt bridge: `wt-model warm`, `wt-model served` and `wt stop`, llmbench's calls into wt."""

import subprocess

import pytest

from llmbench import wt_bridge


def _cp(stdout="", returncode=0, stderr=""):
    return subprocess.CompletedProcess(args=[], returncode=returncode, stdout=stdout, stderr=stderr)


@pytest.fixture
def wt_on_path(monkeypatch):
    monkeypatch.setattr(wt_bridge.shutil, "which", lambda name: f"/usr/local/bin/{name}")


def test_warm_runs_wt_warm_and_reports_its_failure(monkeypatch, wt_on_path):
    """`wt-model warm <provider> <model>` is the keyed warmup llmbench cannot do
    itself (#256). A failure raises with wt's own line, which names the fix,
    and the timeout outlasts wt's warmup budget."""
    seen = []
    result = {"cp": _cp("Qwen-4bit is loaded on omlx\n")}

    def run(argv, **kwargs):
        seen.append((argv, kwargs.get("timeout")))
        return result["cp"]

    monkeypatch.setattr(wt_bridge.subprocess, "run", run)

    wt_bridge.warm("omlx", "Qwen-4bit")
    assert seen == [(["wt-model", "warm", "omlx", "Qwen-4bit"], wt_bridge.WARM_TIMEOUT)]
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
            subprocess.TimeoutExpired(cmd=["wt-model"], timeout=630),
            wt_bridge.WtBridgeTimeoutError,
            "wt-model warm timed out after 630s",
        ),
        (FileNotFoundError("wt-model"), wt_bridge.WtNotFoundError, "wt-model not found on PATH"),
        (PermissionError("denied"), wt_bridge.WtBridgeError, "wt-model warm could not run"),
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


# The two binaries are looked up separately: warm and served need wt-model,
# stop still runs wt, so a machine with only wt installed fails the first two
# with a wt-model message and still stops a model.
def test_warm_and_served_need_wt_model_on_path(monkeypatch):
    monkeypatch.setattr(
        wt_bridge.shutil, "which", lambda name: "/usr/local/bin/wt" if name == "wt" else None
    )
    seen = []

    def run(argv, **kwargs):
        seen.append(argv)
        return _cp()

    monkeypatch.setattr(wt_bridge.subprocess, "run", run)
    with pytest.raises(wt_bridge.WtNotFoundError, match="^wt-model not found on PATH"):
        wt_bridge.warm("omlx", "m")
    with pytest.raises(wt_bridge.WtNotFoundError, match="^wt-model not found on PATH"):
        wt_bridge.served("omlx")
    wt_bridge.stop("omlx/m")
    assert seen == [["wt", "stop", "omlx/m", "--yes"]]


# wt-model prints its refusals as `wt-model: <msg>`; _msg strips that prefix
# as it strips wt's own.
def test_msg_strips_the_wt_model_prefix():
    assert wt_bridge._msg(_cp(returncode=1, stderr="wt-model: x\n"), "f") == "x"


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


def test_served_lists_the_models_wt_reports(monkeypatch, wt_on_path):
    """`wt-model served <provider>` prints one served model id per line; a provider
    that is down (exit 1) raises, so a caller can tell "nothing loaded" from
    "could not ask"."""
    seen = []
    result = {"cp": _cp("Qwen3.6-35B-A3B-6bit\n\nQwen3.8-27B-8bit\n")}

    def run(argv, **kwargs):
        seen.append(argv)
        return result["cp"]

    monkeypatch.setattr(wt_bridge.subprocess, "run", run)
    assert wt_bridge.served("omlx") == ["Qwen3.6-35B-A3B-6bit", "Qwen3.8-27B-8bit"]
    assert seen == [["wt-model", "served", "omlx"]]

    result["cp"] = _cp(returncode=1, stderr="wt: omlx gave no usable answer\n")
    with pytest.raises(wt_bridge.WtBridgeError, match="^omlx gave no usable answer$"):
        wt_bridge.served("omlx")


def test_stop_runs_wt_stop_and_reports_its_failure(monkeypatch, wt_on_path):
    """`wt stop <provider>/<name> --yes` unloads one model; on omlx it leaves
    the service and its other models up. `--yes` is what keeps a model a live
    wt session holds from turning the call into a /dev/tty prompt nothing here
    can answer (it would block until STOP_TIMEOUT). The timeout outlasts the
    LiteLLM proxy restart wt may wait for after a route change."""
    seen = []
    result = {"cp": _cp("Stopping omlx/Qwen... done\n")}

    def run(argv, **kwargs):
        seen.append((argv, kwargs.get("timeout")))
        return result["cp"]

    monkeypatch.setattr(wt_bridge.subprocess, "run", run)
    wt_bridge.stop("omlx/Qwen3.6-35B-A3B-6bit")
    assert seen == [(["wt", "stop", "omlx/Qwen3.6-35B-A3B-6bit", "--yes"], wt_bridge.STOP_TIMEOUT)]
    assert wt_bridge.STOP_TIMEOUT > 180

    result["cp"] = _cp(returncode=1, stderr="Error: not running\n")
    with pytest.raises(wt_bridge.WtBridgeError, match="^not running$"):
        wt_bridge.stop("omlx/Qwen3.6-35B-A3B-6bit")


# The refusal texts stop() may get: wt's three not-configured leads
# (wt/internal/modelclient/endpoint.go), one per missing setting — the fourth
# case puts one on stdout alone, since detection reads both streams — plus an
# unrelated refusal as the negative case.
_STOP_REFUSAL_CASES = [
    ("Error: no model server is configured; [litellm] is no longer used by wt\n", "", True),
    ("Error: the model server url is not configured\n", "", True),
    ("Error: the model server token is not configured\n", "", True),
    ("", "wt: the model server token is not configured\n", True),
    (
        "Error: no model server is configured — native mode has no model server; change the mode in wt config\n",
        "",
        True,
    ),
    ("Error: not running\n", "", False),
]


@pytest.mark.parametrize(
    ("stderr", "stdout", "want_hint"),
    _STOP_REFUSAL_CASES,
    ids=[
        "both-missing",
        "url-missing",
        "token-missing",
        "stdout-refusal",
        "native-mode",
        "other-refusal",
    ],
)
def test_stop_not_configured_refusal_hints_at_the_model_server_settings(
    monkeypatch, wt_on_path, stderr, stdout, want_hint
):
    """Since the wt-model split a model-id stop goes through the model
    server, so with no [model-server] endpoint configured every unload is
    refused and benchmark isolation fails. When the refusal is wt's
    not-configured one (its pinned leads, on either output stream),
    stop()'s failure output must carry the hint that names the settings —
    a local or remote mode chosen in wt config (Mode tab), or
    WT_MODEL_URL and WT_MODEL_TOKEN_FILE — right for an unset mode and for
    native mode alike (native ignores a url and token_file in config.toml,
    so the hint never tells the user to set them there); any other refusal
    keeps its own message alone."""

    def run(argv, **kwargs):
        return _cp(stdout=stdout, returncode=1, stderr=stderr)

    monkeypatch.setattr(wt_bridge.subprocess, "run", run)
    with pytest.raises(wt_bridge.WtBridgeError) as excinfo:
        wt_bridge.stop("omlx/Qwen3.6-35B-A3B-6bit")
    msg = str(excinfo.value)
    line = (stderr or stdout).strip()
    for prefix in ("Error: ", "wt: "):
        if line.startswith(prefix):
            line = line[len(prefix) :]
    assert line in msg  # wt's own line is kept
    for name in (
        "choose local or remote in wt config (Mode tab)",
        "WT_MODEL_URL",
        "WT_MODEL_TOKEN_FILE",
    ):
        assert (name in msg) == want_hint, f"{name} hint presence != {want_hint}"
    assert "set url and token_file" not in msg


def test_stop_url_credentials_refusal_gets_no_settings_hint(monkeypatch, wt_on_path):
    """wt refuses a model-server url containing an `@` (wt issue #639) with a
    fixed line that is not one of the not-configured leads: the server is
    configured, its url is what to fix. stop() must surface wt's own line and
    must not append the hint that tells the user to set the url and token."""
    line = (
        "the model server url contains an @ (read as a user:password); wt does not "
        "use credentials in the url: use a token file (WT_MODEL_TOKEN_FILE or "
        "token_file under [model-server]); nothing was stopped"
    )

    def run(argv, **kwargs):
        return _cp(returncode=1, stderr="wt: " + line + "\n")

    monkeypatch.setattr(wt_bridge.subprocess, "run", run)
    with pytest.raises(wt_bridge.WtBridgeError) as excinfo:
        wt_bridge.stop("omlx/Qwen3.6-35B-A3B-6bit")
    msg = str(excinfo.value)
    assert line in msg
    assert wt_bridge._NOT_CONFIGURED_HINT not in msg
