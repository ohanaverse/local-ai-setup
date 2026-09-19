"""CLI wiring tests for `modelman provider`.

These mostly cover argument parsing, the --json envelope contract, and
exit codes — the orchestration logic itself (isolate/stop/stop_all/
restore) is covered by tests/providers/lifecycle/test_orchestrate.py.
Every `lifecycle.*` call is patched at the module-attribute level
(`modelman.providers.lifecycle.isolate`, etc.) so no real backend is
invoked.

ONE deliberate exception, at the bottom of this file: the mlx_lm_server
target/draft tests drive the REAL `MlxLmServerBackend` through
`orchestrate.isolate()`, patching only the process-touching primitives
BELOW the backend (binary resolution, the pidfile process, probes). A
mocked `lifecycle.isolate` cannot catch a wrong `extra_args` SHAPE,
because only the real `resolve()` knows which slot means what — and that
mismatch was a live bug (every `--draft` invocation failed with
"requires target+draft"). They live here rather than in
backends/test_mlx_lm_server.py because the defect being guarded is
cli.py's `extra_args` construction; that file's tests call `resolve()`
directly and never see the CLI at all.
"""

from __future__ import annotations

import json
from unittest.mock import MagicMock, patch

import pytest
from typer.testing import CliRunner

from modelman.main import app
from modelman.providers.lifecycle.backends.mlx_lm_server import DRAFT_ENV_VAR, TARGET_ENV_VAR
from modelman.providers.lifecycle.envelope import LifecycleResult

runner = CliRunner()

LIFECYCLE = "modelman.providers.lifecycle"
MLX_MODULE = "modelman.providers.lifecycle.backends.mlx_lm_server"


def test_isolate_json_success_prints_exact_five_key_envelope():
    # Task 8's benchmark scripts (and anything else scripting this CLI)
    # parse stdout as JSON and expect EXACTLY these 5 keys — an extra key
    # (or a stray print polluting stdout) would silently break that
    # contract without any test ever exercising the real parse step.
    result = LifecycleResult(
        "ollama", "llama3.2:3b", "http://localhost:11434/v1/chat/completions", True, None
    )
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
    result = LifecycleResult(
        "ollama", "llama3.2:3b", "http://localhost:11434/v1/chat/completions", True, None
    )
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


def test_isolate_forwards_draft_into_extra_args_second_slot():
    # extra_args is the POSITIONAL PAIR (target, draft) everywhere in this
    # codebase, so --draft must land at index 1. The CLI's own `model`
    # positional already carries the target, so index 0 is an empty
    # placeholder. Passing ("draft-model",) instead — as an earlier version
    # did — silently fed the DRAFT repo id into the backend's target slot
    # and left the draft unset, so every --draft invocation failed with
    # "requires target+draft".
    result = LifecycleResult(
        "mlx_lm_server", "target-model", "http://localhost:8001/v1/chat/completions", True, None
    )
    with patch(f"{LIFECYCLE}.isolate", return_value=result) as mock_isolate:
        invoked = runner.invoke(
            app, ["provider", "isolate", "mlx_lm_server", "target-model", "--draft", "draft-model"]
        )
    assert invoked.exit_code == 0, invoked.stderr
    mock_isolate.assert_called_once_with(
        "mlx_lm_server", "target-model", extra_args=("", "draft-model"), solo=False
    )


def test_isolate_without_draft_passes_empty_extra_args():
    # No --draft must mean NO placeholder at all — an ("",) tuple would be
    # indistinguishable from "a target was supplied positionally" for any
    # backend that reads extra_args[0] by length rather than truthiness.
    result = LifecycleResult(
        "mlx_lm_server", "target-model", "http://localhost:8001/v1/chat/completions", True, None
    )
    with patch(f"{LIFECYCLE}.isolate", return_value=result) as mock_isolate:
        invoked = runner.invoke(app, ["provider", "isolate", "mlx_lm_server", "target-model"])
    assert invoked.exit_code == 0, invoked.stderr
    mock_isolate.assert_called_once_with("mlx_lm_server", "target-model", extra_args=(), solo=False)


def test_isolate_forwards_solo():
    # --solo must reach orchestrate.isolate() as solo=True — this is what
    # restricts teardown to the backend's own occupant (modelman start's
    # same-provider-only lifecycle) instead of full exclusivity.
    result = LifecycleResult(
        "ollama", "llama3.2:3b", "http://localhost:11434/v1/chat/completions", True, None
    )
    with patch(f"{LIFECYCLE}.isolate", return_value=result) as mock_isolate:
        invoked = runner.invoke(app, ["provider", "isolate", "ollama", "llama3.2:3b", "--solo"])
    assert invoked.exit_code == 0, invoked.stderr
    mock_isolate.assert_called_once_with("ollama", "llama3.2:3b", extra_args=(), solo=True)


def test_stop_all_forwards_keep():
    # --keep must reach orchestrate.stop_all() as the raw provider id —
    # stop_all() itself resolves that to the right occupancy_key (and
    # validates it), matching how isolate_cmd/stop_cmd forward their
    # provider ids straight through without any CLI-side translation. The
    # id->occupancy_key resolution and unknown-id rejection are covered at
    # the orchestrate.py layer, in test_orchestrate.py.
    result = LifecycleResult("stop-all", "", "", True, None)
    with patch(f"{LIFECYCLE}.stop_all", return_value=result) as mock_stop_all:
        invoked = runner.invoke(app, ["provider", "stop-all", "--keep", "omlx-6bit"])
    assert invoked.exit_code == 0, invoked.stderr
    mock_stop_all.assert_called_once_with("omlx-6bit")


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


# --- CLI -> orchestrate -> REAL MlxLmServerBackend ------------------------
# See the module docstring: these deliberately do NOT patch
# `lifecycle.isolate`, so cli.py's extra_args construction is checked
# against the backend that actually consumes it.


@pytest.fixture
def mlx_spawn_argv(monkeypatch):
    """Let the real MlxLmServerBackend run, stubbing only the primitives
    that would touch this machine: binary resolution, the pidfile-tracked
    process, the port probes, warmup, and orchestrate's teardown of the
    other providers. Yields the list that collects the argv
    `mlx_lm.server` would have been spawned with — argv[2] is the resolved
    target and argv[4] the resolved draft."""
    monkeypatch.delenv(TARGET_ENV_VAR, raising=False)
    monkeypatch.delenv(DRAFT_ENV_VAR, raising=False)

    spawned: list[list[str]] = []
    proc = MagicMock()
    proc.spawn.side_effect = spawned.append

    monkeypatch.setattr(
        f"{MLX_MODULE}.binaries.resolve_mlx_lm_bin", lambda tool: "/bin/mlx_lm.server"
    )
    monkeypatch.setattr(f"{MLX_MODULE}._PROC", proc)
    monkeypatch.setattr(f"{MLX_MODULE}.probe.port_closed_within", lambda *a, **k: True)
    monkeypatch.setattr(f"{MLX_MODULE}.probe.wait_for_model", lambda *a, **k: None)
    monkeypatch.setattr(f"{MLX_MODULE}.probe.warmup", lambda *a, **k: None)
    monkeypatch.setattr(f"{LIFECYCLE}.orchestrate._stop_others", lambda keep="": None)
    return spawned


def test_cli_draft_reaches_backend_as_draft_not_target(mlx_spawn_argv):
    """`isolate mlx_lm_server <target> --draft <draft>` must resolve to
    exactly that target/draft pair inside the real backend. The regression:
    cli.py packed --draft into extra_args[0], the TARGET slot, so the
    backend read the draft repo id as a candidate target and never saw a
    draft at all — every --draft invocation died with "requires
    target+draft"."""
    invoked = runner.invoke(
        app,
        ["provider", "isolate", "mlx_lm_server", "org/target", "--draft", "org/draft", "--json"],
    )
    assert invoked.exit_code == 0, invoked.stdout + invoked.stderr
    assert mlx_spawn_argv == [
        [
            "/bin/mlx_lm.server",
            "--model",
            "org/target",
            "--draft-model",
            "org/draft",
            "--port",
            "8001",
        ]
    ]
    assert json.loads(invoked.stdout)["model"] == "org/target (+draft org/draft)"


def test_cli_draft_with_target_from_env_var(mlx_spawn_argv, monkeypatch):
    """The empty placeholder cli.py puts at extra_args[0] must be FALSY, so
    target resolution still falls through to LLM_ISOLATE_MLXLM_MODEL when
    no target positional is given."""
    monkeypatch.setenv(TARGET_ENV_VAR, "org/target-env")
    invoked = runner.invoke(
        app, ["provider", "isolate", "mlx_lm_server", "--draft", "org/draft", "--json"]
    )
    assert invoked.exit_code == 0, invoked.stdout + invoked.stderr
    assert mlx_spawn_argv[0][2] == "org/target-env"
    assert mlx_spawn_argv[0][4] == "org/draft"


def test_cli_without_draft_falls_back_to_draft_env_var(mlx_spawn_argv, monkeypatch):
    """Omitting --draft is not an error when the env var supplies one — the
    documented compatibility path."""
    monkeypatch.setenv(DRAFT_ENV_VAR, "org/draft-env")
    invoked = runner.invoke(app, ["provider", "isolate", "mlx_lm_server", "org/target", "--json"])
    assert invoked.exit_code == 0, invoked.stdout + invoked.stderr
    assert mlx_spawn_argv[0][2] == "org/target"
    assert mlx_spawn_argv[0][4] == "org/draft-env"


@pytest.mark.parametrize(
    "argv",
    [
        ["provider", "isolate", "mlx_lm_server", "org/target", "--json"],  # draft missing
        ["provider", "isolate", "mlx_lm_server", "--json"],  # both missing
    ],
)
def test_cli_missing_target_or_draft_fails_cleanly_without_spawning(argv, mlx_spawn_argv):
    """With neither flag nor env var supplying the missing half, the run
    must fail with the requires-target+draft envelope and spawn nothing —
    resolve() is pure and runs before any teardown, so a bad request costs
    nothing."""
    invoked = runner.invoke(app, argv)
    assert invoked.exit_code == 1
    parsed = json.loads(invoked.stdout)
    assert parsed["ok"] is False
    assert "requires target+draft" in parsed["error"]
    assert mlx_spawn_argv == []
