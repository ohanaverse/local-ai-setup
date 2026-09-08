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


def test_is_downloaded_raises_on_daemon_error(provider, mock_runner):
    """A non-zero exit that is NOT 'not found' (e.g. the daemon is down)
    must raise, not return False — otherwise the delete step would treat a
    transient outage as 'artifact absent' and orphan the on-disk model."""
    runner = mock_runner(returncode=1, stdout="", stderr="could not connect to ollama app")
    variant: VariantSpec = {"id": "x", "provider": "ollama", "name": "ornith-1.5:9b"}
    with pytest.raises(RuntimeError, match="failed"):
        provider.is_downloaded(variant, runner=runner)


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
    assert isinstance(
        p._current_proc, MagicMock
    )  # not auto-cleared; cleared by _tracked_popen_runner


def test_resolve_local_batches_variants_with_one_list(mock_runner):
    """resolve_local answers presence/path/size for the whole batch from ONE
    `ollama list` call — that single-subprocess property is why
    reconcile_model_state prefers it over the per-model
    is_downloaded/size_of path (which would be N+1 subprocesses for N
    models)."""
    from modelman.providers.ollama import OllamaProvider

    stdout = (
        "NAME                          ID            SIZE      MODIFIED\n"
        "ornith-1.5:35b                abc123        21 GB     2 days ago\n"
    )
    runner = mock_runner(returncode=0, stdout=stdout)
    p = OllamaProvider({})
    results = p.resolve_local(
        [
            {"id": "x", "provider": "ollama", "name": "ornith-1.5:35b"},
            {"id": "y", "provider": "ollama", "name": "missing:7b"},
        ],
        runner=runner,
    )
    assert results is not None
    assert len(results) == 2
    assert results[0] == {
        "variant_id": "ornith-1.5:35b",
        "path": "ollama:ornith-1.5:35b",
        "size_bytes": 21 * 1024**3,
    }
    assert results[1] is None
    runner.assert_called_once_with(["ollama", "list"], capture_output=True, text=True)


def test_resolve_local_tagless_name_resolves_latest(mock_runner):
    """A tagless registry name (`ollama show x` resolves x -> x:latest) must
    be found in `ollama list` output via the :latest fallback — list_local
    reports the literal tag, and without this a tagless model would read as
    absent and ready-off/delete flows would disagree with reconcile."""
    from modelman.providers.ollama import OllamaProvider

    stdout = (
        "NAME                          ID            SIZE      MODIFIED\n"
        "gemma4:latest                 abc123        17 GB     1 day ago\n"
    )
    runner = mock_runner(returncode=0, stdout=stdout)
    p = OllamaProvider({})
    results = p.resolve_local(
        [{"id": "x", "provider": "ollama", "name": "gemma4"}], runner=runner
    )
    assert results is not None
    assert results[0] is not None
    assert results[0]["size_bytes"] == 17 * 1024**3


def test_resolve_local_tagged_name_never_double_falls_back(mock_runner):
    """A name that already carries a tag must be looked up exactly — no
    :latest fallback — so a stale `x:latest` row can never answer for an
    explicitly-tagged `x:9b` query."""
    from modelman.providers.ollama import OllamaProvider

    stdout = (
        "NAME                          ID            SIZE      MODIFIED\n"
        "gemma4:latest                 abc123        17 GB     1 day ago\n"
    )
    runner = mock_runner(returncode=0, stdout=stdout)
    p = OllamaProvider({})
    results = p.resolve_local(
        [{"id": "x", "provider": "ollama", "name": "gemma4:9b"}], runner=runner
    )
    assert results is not None
    assert results[0] is None


def test_base_provider_resolve_local_is_none():
    """The base Provider must not implement resolve_local: a default
    'loop over the per-variant methods' implementation would defeat the
    batching point (the caller falls back to those methods anyway), and
    returning None is how a provider reports 'no batch support'."""
    from modelman.providers.base import Provider

    class Minimal(Provider):
        def is_downloaded(self, variant):
            return False

        def download(self, variant):
            return ""

        def list_local(self):
            return []

    assert Minimal({}).resolve_local([]) is None


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


def test_delete_runs_ollama_rm(provider, mock_runner):
    runner = mock_runner(returncode=0, stdout="", stderr="")
    variant: VariantSpec = {"id": "x", "provider": "ollama", "name": "glm-5.2:cloud"}
    provider.delete(variant, runner=runner)
    runner.assert_called_with(["ollama", "rm", "glm-5.2:cloud"], capture_output=True, text=True)


def test_delete_failure_raises(provider, mock_runner):
    runner = mock_runner(returncode=1, stdout="", stderr="no such model")
    variant: VariantSpec = {"id": "x", "provider": "ollama", "name": "missing:7b"}
    with pytest.raises(RuntimeError, match="failed"):
        provider.delete(variant, runner=runner)
