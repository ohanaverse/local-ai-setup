from unittest.mock import patch

from modelman.benchmark.errors import BenchmarkError
from modelman.benchmark.isolation import isolate_provider, restore_providers


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
