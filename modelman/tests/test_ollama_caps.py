from modelman.ollama_caps import auto_detect_model_info, parse_ollama_show


def test_parse_capabilities_tools_to_function_calling():
    stdout = (
        "Model\n"
        "  architecture    qwen2\n"
        "  parameters      8.2B\n"
        "  context length  32768\n"
        "  embedding length 4096\n"
        "Capabilities\n"
        "    completion\n"
        "    tools\n"
    )
    info = parse_ollama_show(stdout)
    assert info == {"supports_function_calling": True}


def test_parse_no_capabilities_section():
    stdout = "Model\n  architecture llama\n"
    assert parse_ollama_show(stdout) == {}


def test_auto_detect_runs_runner_and_returns_info(mock_runner):
    runner = mock_runner(
        returncode=0,
        stdout="Capabilities\n    tools\n    vision\n",
    )
    info = auto_detect_model_info("foo:1b", runner=runner)
    runner.assert_called_with(["ollama", "show", "foo:1b"], capture_output=True, text=True)
    assert info.get("supports_function_calling") is True


def test_auto_detect_returns_empty_on_failure(mock_runner):
    runner = mock_runner(returncode=1, stdout="", stderr="not found")
    assert auto_detect_model_info("missing:tag", runner=runner) == {}
