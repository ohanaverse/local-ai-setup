import subprocess
from unittest.mock import patch

import pytest

from modelman.providers.lifecycle.backends import BACKENDS
from modelman.providers.lifecycle.backends.ollama import (
    DEFAULT_MODEL,
    ENV_VAR,
    OLLAMA,
    OLLAMA_CHAT_URL,
    OLLAMA_HEALTH_URL,
    UNLOAD_TRIES,
    OllamaBackend,
    _loaded_model_names,
)
from modelman.providers.lifecycle.envelope import LifecycleError
from modelman.providers.lifecycle.launchd import OLLAMA_LABEL


def test_ollama_registered_in_backends_registry():
    """The bottom-of-module `OLLAMA = OllamaBackend()` singleton must be the
    same object registered under "ollama" in BACKENDS — a copy-paste bug
    that registers a fresh instance would silently diverge from the one
    imported elsewhere."""
    assert BACKENDS["ollama"] is OLLAMA


# --- resolve() / model precedence -------------------------------------------


def test_resolve_uses_explicit_model_when_given(monkeypatch):
    """An explicit model argument must win over both the env var and the
    default — matches bash's positional-arg-not-applicable-to-ollama case,
    but the Python resolve() still supports it via the base precedence."""
    monkeypatch.setenv(ENV_VAR, "env-model")
    plan = OLLAMA.resolve("explicit-model", ())
    assert plan.model == "explicit-model"
    assert plan.direct_url == OLLAMA_CHAT_URL


def test_resolve_env_var_wins_over_default(monkeypatch):
    """With no explicit model, LLM_ISOLATE_OLLAMA_MODEL must win over the
    baked-in default — this is how bash's `OLLAMA_MODEL="${LLM_ISOLATE_OLLAMA_MODEL:-ornith-1.5:35b}"`
    behaves."""
    monkeypatch.setenv(ENV_VAR, "env-model")
    plan = OLLAMA.resolve(None, ())
    assert plan.model == "env-model"


def test_resolve_falls_back_to_default_model(monkeypatch):
    """With neither an explicit model nor the env var set, resolve() must
    use the hardcoded default "ornith-1.5:35b", matching bash's fallback."""
    monkeypatch.delenv(ENV_VAR, raising=False)
    plan = OLLAMA.resolve(None, ())
    assert plan.model == DEFAULT_MODEL


# --- _loaded_model_names() ---------------------------------------------------


def test_loaded_model_names_parses_multiline_output_dropping_header():
    """`ollama ps` output has a header row (NAME/ID/SIZE/...) that must be
    dropped, and only the first whitespace-separated field of each
    remaining line is the model name — this is the Python port of
    `tail -n +2 | awk 'NF {print $1}'`."""
    stdout = (
        "NAME                    ID              SIZE      PROCESSOR    UNTIL\n"
        "ornith-1.5:35b          abc123          20 GB     100% GPU     4 minutes\n"
        "qwen3.8:latest          def456          8 GB      100% GPU     3 minutes\n"
    )
    with (
        patch(
            "modelman.providers.lifecycle.backends.ollama.shutil.which",
            return_value="/usr/bin/ollama",
        ),
        patch("modelman.providers.lifecycle.backends.ollama.subprocess.run") as mock_run,
    ):
        mock_run.return_value = subprocess.CompletedProcess([], 0, stdout=stdout)
        names = _loaded_model_names()
    assert names == ["ornith-1.5:35b", "qwen3.8:latest"]


def test_loaded_model_names_returns_empty_when_ollama_missing_from_path():
    """No `ollama` binary on PATH must return [] without attempting to run
    a command — a down/missing daemon has nothing loaded."""
    with (
        patch("modelman.providers.lifecycle.backends.ollama.shutil.which", return_value=None),
        patch("modelman.providers.lifecycle.backends.ollama.subprocess.run") as mock_run,
    ):
        assert _loaded_model_names() == []
    mock_run.assert_not_called()


def test_loaded_model_names_returns_empty_on_nonzero_exit():
    """A non-zero `ollama ps` exit (e.g. daemon down) must read as "nothing
    loaded", not raise."""
    with (
        patch(
            "modelman.providers.lifecycle.backends.ollama.shutil.which",
            return_value="/usr/bin/ollama",
        ),
        patch("modelman.providers.lifecycle.backends.ollama.subprocess.run") as mock_run,
    ):
        mock_run.return_value = subprocess.CompletedProcess([], 1, stdout="")
        assert _loaded_model_names() == []


def test_loaded_model_names_returns_empty_on_oserror():
    """An OSError running `ollama ps` (e.g. race where the binary vanished
    after the PATH check) must also come back as [] rather than raise."""
    with (
        patch(
            "modelman.providers.lifecycle.backends.ollama.shutil.which",
            return_value="/usr/bin/ollama",
        ),
        patch(
            "modelman.providers.lifecycle.backends.ollama.subprocess.run",
            side_effect=OSError("boom"),
        ),
    ):
        assert _loaded_model_names() == []


# --- start() ------------------------------------------------------------


def test_start_kickstarts_when_health_probe_fails():
    """bash's start_ollama() only kickstarts the launchd service when the
    one-shot 2s health probe fails — a stale/wedged daemon should not be
    bounced just because it's slow to answer once."""
    plan = OLLAMA.resolve("m", ())
    with (
        patch(
            "modelman.providers.lifecycle.backends.ollama.urllib.request.urlopen",
            side_effect=OSError("connection refused"),
        ),
        patch("modelman.providers.lifecycle.backends.ollama.launchd.kickstart") as mock_kickstart,
    ):
        OllamaBackend().start(plan)
    mock_kickstart.assert_called_once_with(OLLAMA_LABEL)


def test_start_does_not_kickstart_when_health_probe_succeeds():
    """When the daemon already answers within 2s, bash's `curl ... ||
    launchctl kickstart` short-circuits and never kickstarts — a healthy
    daemon must not be needlessly restarted."""
    plan = OLLAMA.resolve("m", ())
    with (
        patch(
            "modelman.providers.lifecycle.backends.ollama.urllib.request.urlopen"
        ) as mock_urlopen,
        patch("modelman.providers.lifecycle.backends.ollama.launchd.kickstart") as mock_kickstart,
    ):
        mock_urlopen.return_value.__enter__ = lambda self: self
        mock_urlopen.return_value.__exit__ = lambda *a: None
        OllamaBackend().start(plan)
    mock_kickstart.assert_not_called()


def test_start_does_not_warm():
    """warm() is orchestrate.isolate()'s job (called once after
    start()+wait_ready() for every backend) — start() calling it too would
    warm the model twice per isolate(). Regression test for that bug."""
    plan = OLLAMA.resolve("m", ())
    with (
        patch("modelman.providers.lifecycle.backends.ollama.urllib.request.urlopen"),
        patch("modelman.providers.lifecycle.backends.base.warmup") as mock_warmup,
    ):
        OllamaBackend().start(plan)
    mock_warmup.assert_not_called()


# --- stop_and_wait() ------------------------------------------------------


def test_stop_and_wait_stops_every_loaded_model_and_returns_none_on_clean_stop():
    """modelman may have loaded a model other than the default via
    `modelman start`, so stop_and_wait() must stop EVERY model `ollama ps`
    reports loaded, not just DEFAULT_MODEL — this is the whole reason bash
    reads `ollama ps` live instead of stopping a baked-in name."""
    with (
        patch(
            "modelman.providers.lifecycle.backends.ollama._loaded_model_names",
            side_effect=[["m1", "m2"], []],
        ),
        patch("modelman.providers.lifecycle.backends.ollama.subprocess.run") as mock_run,
        patch("modelman.providers.lifecycle.backends.ollama.time.sleep") as mock_sleep,
    ):
        result = OllamaBackend().stop_and_wait()
    assert result is None
    stop_calls = [c.args[0] for c in mock_run.call_args_list]
    assert stop_calls == [["ollama", "stop", "m1"], ["ollama", "stop", "m2"]]
    mock_sleep.assert_not_called()


def test_stop_and_wait_returns_warning_when_models_still_loaded_after_retries():
    """If a model is still loaded after UNLOAD_TRIES polls, stop_and_wait()
    must return a warning string (not raise) — matching bash's
    `wait_for_ollama_unloaded 5 || echo "ollama still has models loaded"`,
    which warns rather than fails the whole isolation."""
    with (
        patch(
            "modelman.providers.lifecycle.backends.ollama._loaded_model_names",
            return_value=["stuck-model"],
        ),
        patch("modelman.providers.lifecycle.backends.ollama.subprocess.run") as mock_run,
        patch("modelman.providers.lifecycle.backends.ollama.time.sleep") as mock_sleep,
    ):
        result = OllamaBackend().stop_and_wait()
    assert result == "ollama still has models loaded"
    assert mock_sleep.call_count == UNLOAD_TRIES
    mock_run.assert_called_once_with(
        ["ollama", "stop", "stuck-model"], capture_output=True, check=False
    )


# --- restore() ------------------------------------------------------------


def test_restore_no_ops_when_already_up():
    """restore() must not touch launchd at all when ollama already answers
    its health check — a no-op restart would be pointless churn."""
    with (
        patch(
            "modelman.providers.lifecycle.backends.base.urllib.request.urlopen"
        ) as mock_urlopen,
        patch("modelman.providers.lifecycle.backends.ollama.launchd.kickstart") as mock_kickstart,
        patch("modelman.providers.lifecycle.backends.base.wait_for_port_open") as mock_wait,
    ):
        mock_urlopen.return_value.__enter__ = lambda self: self
        mock_urlopen.return_value.__exit__ = lambda *a: None
        OllamaBackend().restore()
    mock_kickstart.assert_not_called()
    mock_wait.assert_not_called()


def test_restore_kickstarts_and_waits_when_down():
    """When the health probe fails, restore() must kickstart the launchd
    service and then poll for it to come back up, matching bash's
    `wait_for()` fallback path."""
    with (
        patch(
            "modelman.providers.lifecycle.backends.base.urllib.request.urlopen",
            side_effect=OSError("refused"),
        ),
        patch("modelman.providers.lifecycle.backends.ollama.launchd.kickstart") as mock_kickstart,
        patch(
            "modelman.providers.lifecycle.backends.base.wait_for_port_open",
            return_value=True,
        ) as mock_wait,
    ):
        OllamaBackend().restore()
    mock_kickstart.assert_called_once_with(OLLAMA_LABEL)
    mock_wait.assert_called_once()
    assert mock_wait.call_args.args[0] == OLLAMA_HEALTH_URL


def test_restore_raises_lifecycle_error_when_it_never_comes_back():
    """If the service never answers within RESTORE_WAIT_TIMEOUT after a
    kickstart, restore() must raise LifecycleError with a message naming
    the health URL — matching bash's `wait_for()` failure message shape."""
    with (
        patch(
            "modelman.providers.lifecycle.backends.base.urllib.request.urlopen",
            side_effect=OSError("refused"),
        ),
        patch("modelman.providers.lifecycle.backends.ollama.launchd.kickstart"),
        patch(
            "modelman.providers.lifecycle.backends.base.wait_for_port_open",
            return_value=False,
        ),
        pytest.raises(LifecycleError, match="ollama did not come back up"),
    ):
        OllamaBackend().restore()
