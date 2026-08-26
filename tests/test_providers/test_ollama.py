import pytest
from modelman.providers.ollama import OllamaProvider
from modelman.providers.registry import ProviderRegistry
from modelman.providers.base import VariantSpec


@pytest.fixture
def provider(mock_runner):
    from modelman.providers import ollama as _ollama_mod  # triggers register()
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


def test_list_local_parses_output(provider, mock_runner, tmp_path):
    fixture = open("tests/fixtures/ollama_list_output.txt").read()
    runner = mock_runner(returncode=0, stdout=fixture, stderr="")
    models = provider.list_local(runner=runner)
    assert len(models) == 3
    assert models[0]["variant_id"] == "ornith-1.5:9b"
    assert models[1]["variant_id"] == "qwen3.8:27b-mlx"
    assert models[2]["variant_id"] == "gemma4:26b-mlx"
    runner.assert_called_with(["ollama", "list"], capture_output=True, text=True)