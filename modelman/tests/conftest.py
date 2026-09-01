"""Shared pytest fixtures."""

from unittest.mock import MagicMock

import pytest


@pytest.fixture
def stub_ollama_caps(monkeypatch):
    """Make ModelForm submit / model-screen flows headless-portable.

    The form submit path calls auto_detect_model_info(name), which shells
    out to `ollama show <name>` to populate LiteLLM model_info. A clean CI
    runner has no ollama binary, so these pure form-submit tests fail on
    FileNotFoundError. None of the tests that hit this path assert on the
    model_info content, so stubbing it to {} keeps them meaningful without
    requiring ollama to be installed.
    """
    monkeypatch.setattr("modelman.screens.forms.auto_detect_model_info", lambda name: {})


@pytest.fixture
def mock_runner():
    """A factory that returns a fake subprocess runner.

    Usage:
        def test_x(mock_runner):
            runner = mock_runner(returncode=0, stdout="hello")
            # ... call code that uses `runner` ...
            runner.assert_called_with(["some", "command"])
    """

    def _factory(returncode: int = 0, stdout: str = "", stderr: str = ""):
        runner = MagicMock()
        result = MagicMock()
        result.returncode = returncode
        result.stdout = stdout
        result.stderr = stderr
        runner.return_value = result
        return runner

    return _factory
