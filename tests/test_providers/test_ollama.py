import pytest

from modelman.providers.base import VariantSpec
from modelman.providers.ollama import OllamaProvider
from modelman.providers.registry import ProviderRegistry


@pytest.fixture
def provider(mock_runner):
    return OllamaProvider({})


def test_registered():
    assert "ollama" in ProviderRegistry.available()


def test_is_downloaded_true(provider, mock_runner):
    runner = mock_runner(returncode=0, stdout="", stderr="")
    variant: VariantSpec = {"id": "x", "provider": "ollama", "name": "ornith-1.5:9b"}
    assert provider.is_downloaded(variant, runner=runner) is True
    runner.assert_called_with(["ollama", "show", "ornith-1.5:9b"], capture_output=True, text=True)


def test_is_downloaded_false(provider, mock_runner):
    runner = mock_runner(returncode=1, stdout="", stderr="model not found")
    variant: VariantSpec = {"id": "x", "provider": "ollama", "name": "missing:7b"}
    assert provider.is_downloaded(variant, runner=runner) is False


def test_download_calls_pull(provider, mock_runner):
    runner = mock_runner(returncode=0, stdout="pulling...\n", stderr="")
    variant: VariantSpec = {"id": "x", "provider": "ollama", "name": "ornith-1.5:9b"}
    path = provider.download(variant, runner=runner)
    assert path == "ollama:ornith-1.5:9b"
    runner.assert_called_with(["ollama", "pull", "ornith-1.5:9b"])


def test_download_failure_raises(provider, mock_runner):
    runner = mock_runner(returncode=1, stdout="", stderr="error")
    variant: VariantSpec = {"id": "x", "provider": "ollama", "name": "fail:7b"}
    with pytest.raises(RuntimeError, match="failed"):
        provider.download(variant, runner=runner)


def test_list_local_parses_output(provider, mock_runner, tmp_path):
    with open("tests/fixtures/ollama_list_output.txt") as f:
        fixture = f.read()
    runner = mock_runner(returncode=0, stdout=fixture, stderr="")
    models = provider.list_local(runner=runner)
    assert len(models) == 3
    assert models[0]["variant_id"] == "ornith-1.5:9b"
    assert models[1]["variant_id"] == "qwen3.8:27b-mlx"
    assert models[2]["variant_id"] == "gemma4:26b-mlx"
    runner.assert_called_with(["ollama", "list"], capture_output=True, text=True)


def test_size_of_parses_ollama_list(mock_runner):
    from modelman.providers.ollama import OllamaProvider

    stdout = (
        "NAME                       ID           SIZE      MODIFIED\n"
        "ornith-1.5:35b             abc123       21 GB     2 days ago\n"
        "ornith-1.5:8b              def456       5.2 GB    3 days ago\n"
    )
    runner = mock_runner(returncode=0, stdout=stdout)
    p = OllamaProvider({})

    assert (
        p.size_of(
            {"id": "x", "provider": "ollama", "name": "ornith-1.5:35b"},
            runner=runner,
        )
        == 21 * 1024**3
    )
    assert p.size_of(
        {"id": "x", "provider": "ollama", "name": "ornith-1.5:8b"},
        runner=runner,
    ) == int(5.2 * 1024**3)


def test_size_of_returns_none_when_not_in_list(mock_runner):
    from modelman.providers.ollama import OllamaProvider

    runner = mock_runner(returncode=0, stdout="NAME ID SIZE MODIFIED\n")
    p = OllamaProvider({})
    assert (
        p.size_of(
            {"id": "x", "provider": "ollama", "name": "missing:tag"},
            runner=runner,
        )
        is None
    )


def test_cancel_current_terminates_running_proc():
    """cancel_current() must terminate the active Popen without raising."""
    from unittest.mock import MagicMock

    from modelman.providers.ollama import OllamaProvider

    p = OllamaProvider({})
    # Fake Popen that just exposes poll()/terminate().
    fake_proc = MagicMock()
    fake_proc.poll.return_value = None  # still running
    fake_proc.terminate = MagicMock()
    p._current_proc = fake_proc  # type: ignore[assignment]

    p.cancel_current()

    fake_proc.terminate.assert_called_once()
    assert isinstance(p._current_proc, MagicMock)  # not auto-cleared; cleared by _tracked_popen_runner


def test_cancel_current_kills_proc_that_ignores_sigterm():
    """If the proc doesn't exit within ~1s after SIGTERM, cancel_current()
    must escalate to kill() (SIGKILL)."""
    import time
    from unittest.mock import MagicMock

    from modelman.providers.ollama import OllamaProvider

    p = OllamaProvider({})
    fake_proc = MagicMock()
    # Simulate a proc that ignores SIGTERM: poll() returns None forever.
    fake_proc.poll.return_value = None
    fake_proc.terminate = MagicMock()
    fake_proc.kill = MagicMock()
    p._current_proc = fake_proc  # type: ignore[assignment]

    p.cancel_current()
    # Wait long enough for the watchdog to escalate.
    time.sleep(1.5)
    fake_proc.kill.assert_called_once()


def test_cancel_current_noop_when_proc_already_finished():
    """cancel_current() on a proc that already exited is a no-op."""
    from unittest.mock import MagicMock

    from modelman.providers.ollama import OllamaProvider

    p = OllamaProvider({})
    fake_proc = MagicMock()
    fake_proc.poll.return_value = 0  # already exited
    fake_proc.terminate = MagicMock()
    p._current_proc = fake_proc  # type: ignore[assignment]

    p.cancel_current()

    fake_proc.terminate.assert_not_called()
