"""CLI wiring tests for `modelman provider` (Task 7).

These only cover argument parsing, the --json envelope contract, and exit
codes — the orchestration logic itself (isolate/stop/stop_all/restore) is
covered by tests/providers/lifecycle/test_orchestrate.py. Every
`lifecycle.*` call is patched at the module-attribute level
(`modelman.providers.lifecycle.isolate`, etc.) so no real backend is ever
invoked here.
"""

from __future__ import annotations

import json
from unittest.mock import patch

from typer.testing import CliRunner

from modelman.main import app
from modelman.providers.lifecycle.envelope import LifecycleResult

runner = CliRunner()

LIFECYCLE = "modelman.providers.lifecycle"


def test_isolate_json_success_prints_exact_five_key_envelope():
    # Task 8's benchmark scripts (and anything else scripting this CLI)
    # parse stdout as JSON and expect EXACTLY these 5 keys — an extra key
    # (or a stray print polluting stdout) would silently break that
    # contract without any test ever exercising the real parse step.
    result = LifecycleResult("ollama", "llama3.2:3b", "http://localhost:11434/v1/chat/completions", True, None)
    with patch(f"{LIFECYCLE}.isolate", return_value=result) as mock_isolate:
        invoked = runner.invoke(app, ["provider", "isolate", "ollama", "llama3.2:3b", "--json"])
    assert invoked.exit_code == 0, invoked.stderr
    parsed = json.loads(invoked.stdout)
    assert set(parsed.keys()) == {"provider", "model", "direct_url", "ok", "error"}
    assert parsed == {
        "provider": "ollama",
        "model": "llama3.2:3b",
        "direct_url": "http://localhost:11434/v1/chat/completions",
        "ok": True,
        "error": None,
    }
    mock_isolate.assert_called_once_with("ollama", "llama3.2:3b", extra_args=(), solo=False)


def test_isolate_json_failure_prints_clean_envelope_and_exits_nonzero():
    # ok=False must still be a valid, parseable envelope on stdout — not a
    # traceback and not an error message mixed into the JSON stream.
    result = LifecycleResult("ollama", "", "", False, "ollama binary not found")
    with patch(f"{LIFECYCLE}.isolate", return_value=result):
        invoked = runner.invoke(app, ["provider", "isolate", "ollama", "--json"])
    assert invoked.exit_code == 1
    parsed = json.loads(invoked.stdout)
    assert set(parsed.keys()) == {"provider", "model", "direct_url", "ok", "error"}
    assert parsed["ok"] is False
    assert parsed["error"] == "ollama binary not found"


def test_isolate_human_output_success_goes_to_stdout():
    # Without --json, a human line describing the isolated provider/model
    # is the whole contract — this is what a person watching the terminal
    # actually reads.
    result = LifecycleResult("ollama", "llama3.2:3b", "http://localhost:11434/v1/chat/completions", True, None)
    with patch(f"{LIFECYCLE}.isolate", return_value=result):
        invoked = runner.invoke(app, ["provider", "isolate", "ollama", "llama3.2:3b"])
    assert invoked.exit_code == 0
    assert "ollama" in invoked.stdout
    assert "llama3.2:3b" in invoked.stdout
    assert invoked.stderr == ""


def test_isolate_human_output_failure_goes_to_stderr_not_stdout():
    # A failure's error text must land on stderr, not stdout — a caller
    # piping stdout elsewhere (or a future --json consumer switching modes)
    # must never see error text mixed into normal output.
    result = LifecycleResult("ollama", "", "", False, "ollama binary not found")
    with patch(f"{LIFECYCLE}.isolate", return_value=result):
        invoked = runner.invoke(app, ["provider", "isolate", "ollama"])
    assert invoked.exit_code == 1
    assert invoked.stdout == ""
    assert "ollama binary not found" in invoked.stderr


def test_isolate_forwards_draft_into_extra_args():
    # mlx_lm_server's target+draft pairing is passed positionally via
    # extra_args — --draft must land there as a one-element tuple, not be
    # dropped or passed as a bare string.
    result = LifecycleResult("mlx_lm_server", "target-model", "http://localhost:8001/v1/chat/completions", True, None)
    with patch(f"{LIFECYCLE}.isolate", return_value=result) as mock_isolate:
        invoked = runner.invoke(
            app, ["provider", "isolate", "mlx_lm_server", "target-model", "--draft", "draft-model"]
        )
    assert invoked.exit_code == 0, invoked.stderr
    mock_isolate.assert_called_once_with(
        "mlx_lm_server", "target-model", extra_args=("draft-model",), solo=False
    )


def test_isolate_forwards_solo():
    # --solo must reach orchestrate.isolate() as solo=True — this is what
    # restricts teardown to the backend's own occupant (modelman start's
    # same-provider-only lifecycle) instead of full exclusivity.
    result = LifecycleResult("ollama", "llama3.2:3b", "http://localhost:11434/v1/chat/completions", True, None)
    with patch(f"{LIFECYCLE}.isolate", return_value=result) as mock_isolate:
        invoked = runner.invoke(app, ["provider", "isolate", "ollama", "llama3.2:3b", "--solo"])
    assert invoked.exit_code == 0, invoked.stderr
    mock_isolate.assert_called_once_with("ollama", "llama3.2:3b", extra_args=(), solo=True)


def test_stop_all_forwards_keep():
    # --keep must reach orchestrate.stop_all() verbatim so the named
    # occupancy domain's models are left loaded.
    result = LifecycleResult("stop-all", "", "", True, None)
    with patch(f"{LIFECYCLE}.stop_all", return_value=result) as mock_stop_all:
        invoked = runner.invoke(app, ["provider", "stop-all", "--keep", "omlx"])
    assert invoked.exit_code == 0, invoked.stderr
    mock_stop_all.assert_called_once_with("omlx")


def test_stop_all_without_keep_passes_empty_string():
    # Omitting --keep must reach orchestrate.stop_all() as "" (its default,
    # meaning "keep nothing"), matching orchestrate.py's own signature.
    result = LifecycleResult("stop-all", "", "", True, None)
    with patch(f"{LIFECYCLE}.stop_all", return_value=result) as mock_stop_all:
        invoked = runner.invoke(app, ["provider", "stop-all"])
    assert invoked.exit_code == 0, invoked.stderr
    mock_stop_all.assert_called_once_with("")


def test_stop_command_success():
    result = LifecycleResult("mtplx", "", "http://localhost:8003/v1/chat/completions", True, None)
    with patch(f"{LIFECYCLE}.stop", return_value=result) as mock_stop:
        invoked = runner.invoke(app, ["provider", "stop", "mtplx", "--json"])
    assert invoked.exit_code == 0, invoked.stderr
    parsed = json.loads(invoked.stdout)
    assert set(parsed.keys()) == {"provider", "model", "direct_url", "ok", "error"}
    mock_stop.assert_called_once_with("mtplx")


def test_restore_command_success():
    result = LifecycleResult("restore", "", "", True, None)
    with patch(f"{LIFECYCLE}.restore", return_value=result) as mock_restore:
        invoked = runner.invoke(app, ["provider", "restore", "--json"])
    assert invoked.exit_code == 0, invoked.stderr
    parsed = json.loads(invoked.stdout)
    assert set(parsed.keys()) == {"provider", "model", "direct_url", "ok", "error"}
    mock_restore.assert_called_once_with()


def test_isolate_exception_from_lifecycle_becomes_clean_envelope_not_traceback():
    # orchestrate.isolate() is documented to never raise, but this CLI's
    # own try/except is a defensive backstop for exactly the case where
    # that contract is violated (a bug, or a future backend that doesn't
    # honor it) — a raw traceback on stdout/stderr must never happen here.
    with patch(f"{LIFECYCLE}.isolate", side_effect=RuntimeError("boom")):
        invoked = runner.invoke(app, ["provider", "isolate", "ollama", "--json"])
    assert invoked.exit_code == 1
    assert "Traceback" not in invoked.stdout
    assert "Traceback" not in invoked.stderr
    parsed = json.loads(invoked.stdout)
    assert set(parsed.keys()) == {"provider", "model", "direct_url", "ok", "error"}
    assert parsed["ok"] is False
    assert "RuntimeError: boom" in parsed["error"]


def test_isolate_exception_human_mode_also_clean():
    with patch(f"{LIFECYCLE}.isolate", side_effect=RuntimeError("boom")):
        invoked = runner.invoke(app, ["provider", "isolate", "ollama"])
    assert invoked.exit_code == 1
    assert "Traceback" not in invoked.stdout
    assert "Traceback" not in invoked.stderr
    assert invoked.stdout == ""
    assert "RuntimeError: boom" in invoked.stderr


def test_list_command_lists_every_registered_id():
    # No strict format contract for `list` — just confirm every backend id
    # modelman knows about actually shows up somewhere in the output, so a
    # human (or Task 8's author) can see the full set at a glance.
    from modelman.providers.lifecycle import BACKENDS

    invoked = runner.invoke(app, ["provider", "list"])
    assert invoked.exit_code == 0, invoked.stderr
    for provider_id in BACKENDS:
        assert provider_id in invoked.stdout
