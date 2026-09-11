from unittest.mock import patch

from modelman.benchmark import isolation
from modelman.benchmark.errors import BenchmarkError
from modelman.benchmark.isolation import (
    isolate_provider,
    restore_providers,
    stop_all_local_providers,
)


def test_isolate_provider_success():
    with (
        patch("modelman.benchmark.isolation.subprocess.run") as mock_run,
        patch(
            "modelman.benchmark.isolation.shutil.which",
            return_value="/usr/local/bin/llm-isolate-provider",
        ),
    ):
        mock_run.return_value.returncode = 0
        mock_run.return_value.stdout = '{"provider":"ollama","model":"ornith-1.5:35b","direct_url":"http://localhost:11434/v1/chat/completions","ok":true,"error":null}\n'
        mock_run.return_value.stderr = ""
        result = isolate_provider("ollama")
        assert result.provider == "ollama"
        assert result.direct_url == "http://localhost:11434/v1/chat/completions"
        assert result.ok is True


def test_isolate_provider_failure_raises():
    with (
        patch("modelman.benchmark.isolation.subprocess.run") as mock_run,
        patch(
            "modelman.benchmark.isolation.shutil.which",
            return_value="/usr/local/bin/llm-isolate-provider",
        ),
    ):
        mock_run.return_value.returncode = 1
        mock_run.return_value.stdout = ""
        mock_run.return_value.stderr = "ollama not reachable"
        try:
            isolate_provider("ollama")
            raise AssertionError("expected BenchmarkError")
        except BenchmarkError as exc:
            assert "ollama not reachable" in str(exc)


def test_supported_provider_ids_matches_llm_isolate_providers_documented_list():
    """bin/llm-isolate-provider's own header comment ("Supported: ollama,
    omlx, omlx-6bit, mlx_lm_server") is the real source of truth for what
    this helper may isolate on behalf of modelman benchmark agent. The
    llamacpp case branch is retained in the shell script but retired (issue
    #33, 2026-09-07 — see docs/reference/provider-artifacts.md), so it is
    not in this set; a backend added to the shell script's case statement
    without updating this constant is caught here rather than silently
    running unisolated. This is a deliberate drift trip-wire: the frozenset
    below is a literal copy of the documented list, not a reference to
    SUPPORTED_PROVIDER_IDS, so it must be hand-updated (together with
    isolation.py's SUPPORTED_PROVIDER_IDS and the shell header comment) any
    time the supported set changes."""
    assert frozenset({"ollama", "omlx", "omlx-6bit", "mlx_lm_server", "mtplx"}) == isolation.SUPPORTED_PROVIDER_IDS


def test_isolate_provider_forwards_extra_args():
    """isolate_provider() must forward extra positional args (e.g. the
    target/draft model pair mlx_lm_server needs) straight through to the
    llm-isolate-provider subprocess invocation, after the provider id. This
    is what lets modelman benchmark agent isolate a target+draft pairing
    that has no default in the shell script — without this, callers could
    only ever isolate providers whose model choice is a shell-script
    default, which mlx_lm_server deliberately has none of."""
    with (
        patch("modelman.benchmark.isolation.subprocess.run") as mock_run,
        patch(
            "modelman.benchmark.isolation.shutil.which",
            return_value="/usr/local/bin/llm-isolate-provider",
        ),
    ):
        mock_run.return_value.returncode = 0
        mock_run.return_value.stdout = '{"provider":"mlx_lm_server","model":"target-repo (+draft draft-repo)","direct_url":"http://localhost:8001/v1/chat/completions","ok":true,"error":null}\n'
        mock_run.return_value.stderr = ""
        result = isolate_provider("mlx_lm_server", "target-repo", "draft-repo")
        mock_run.assert_called_once_with(
            ["/usr/local/bin/llm-isolate-provider", "mlx_lm_server", "target-repo", "draft-repo"],
            capture_output=True,
            text=True,
            check=False,
        )
        assert result.provider == "mlx_lm_server"
        assert result.ok is True


def test_restore_providers_success():
    with (
        patch("modelman.benchmark.isolation.subprocess.run") as mock_run,
        patch(
            "modelman.benchmark.isolation.shutil.which",
            return_value="/usr/local/bin/llm-restore-providers",
        ),
    ):
        mock_run.return_value.returncode = 0
        restore_providers()
        mock_run.assert_called_once()


def test_stop_all_local_providers_success():
    """`modelman stop` delegates to this to tear down whatever local model
    is running, regardless of which provider it's on — without a stop-all
    mode, modelman would have to know and stop each provider individually,
    duplicating bin/llm-isolate-provider's own stop_all_local logic."""
    with (
        patch("modelman.benchmark.isolation.subprocess.run") as mock_run,
        patch(
            "modelman.benchmark.isolation.shutil.which",
            return_value="/usr/local/bin/llm-isolate-provider",
        ),
    ):
        mock_run.return_value.returncode = 0
        mock_run.return_value.stdout = '{"provider":null,"model":null,"direct_url":null,"ok":true,"error":null}\n'
        mock_run.return_value.stderr = ""
        result = stop_all_local_providers()
        mock_run.assert_called_once_with(
            ["/usr/local/bin/llm-isolate-provider", "stop-all"],
            capture_output=True,
            text=True,
            check=False,
        )
        assert result.ok is True


def test_stop_all_local_providers_failure_raises():
    with (
        patch("modelman.benchmark.isolation.subprocess.run") as mock_run,
        patch(
            "modelman.benchmark.isolation.shutil.which",
            return_value="/usr/local/bin/llm-isolate-provider",
        ),
    ):
        mock_run.return_value.returncode = 1
        mock_run.return_value.stdout = ""
        mock_run.return_value.stderr = "stop failed"
        try:
            stop_all_local_providers()
            raise AssertionError("expected BenchmarkError")
        except BenchmarkError as exc:
            assert "stop failed" in str(exc)


def test_isolate_provider_passes_env_override():
    """`modelman start` sets LLM_ISOLATE_*_MODEL to the exact requested
    model name so the isolation helper warms up THAT model rather than its
    baked-in benchmark default. Without an env override, isolate_provider
    always warmed up whichever model the shell script's own default (or an
    externally-exported env var) named, regardless of the caller's target."""
    with (
        patch("modelman.benchmark.isolation.subprocess.run") as mock_run,
        patch(
            "modelman.benchmark.isolation.shutil.which",
            return_value="/usr/local/bin/llm-isolate-provider",
        ),
        patch.dict("modelman.benchmark.isolation.os.environ", {"EXISTING": "kept"}, clear=True),
    ):
        mock_run.return_value.returncode = 0
        mock_run.return_value.stdout = '{"provider":"ollama","model":"custom:tag","direct_url":"http://localhost:11434/v1/chat/completions","ok":true,"error":null}\n'
        mock_run.return_value.stderr = ""
        isolate_provider("ollama", env={"LLM_ISOLATE_OLLAMA_MODEL": "custom:tag"})
        called_env = mock_run.call_args.kwargs["env"]
        assert called_env["LLM_ISOLATE_OLLAMA_MODEL"] == "custom:tag"
        assert called_env["EXISTING"] == "kept"
