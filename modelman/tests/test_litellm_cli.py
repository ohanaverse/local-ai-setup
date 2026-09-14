"""modelman litellm CLI: on/off/set/status must mutate only the [litellm]
table via locked_state, never touch model_state or restart the proxy — a
routing toggle that also restarted the shared LiteLLM service would turn a
config change into a brief service outage for every other user of it."""
from typer.testing import CliRunner

from modelman.main import app
from modelman.state import load_state

runner = CliRunner()


def test_litellm_on_sets_enabled_true(tmp_path, monkeypatch):
    state_path = tmp_path / "modelman.toml"
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    result = runner.invoke(app, ["litellm", "on"])
    assert result.exit_code == 0
    assert load_state(path=state_path).litellm.enabled is True


def test_litellm_off_sets_enabled_false(tmp_path, monkeypatch):
    state_path = tmp_path / "modelman.toml"
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    runner.invoke(app, ["litellm", "on"])
    result = runner.invoke(app, ["litellm", "off"])
    assert result.exit_code == 0
    assert load_state(path=state_path).litellm.enabled is False


def test_litellm_off_warns_when_unconfigured(tmp_path, monkeypatch):
    # Turning litellm off must not imply "litellm no longer matters" — a
    # protocol-forced pairing (e.g. claude+openrouter) routes through it
    # regardless of this toggle, so an unconfigured url/api_key is still a
    # real problem. `on` already warned about this; `off` silently didn't,
    # which is exactly how this state went unnoticed.
    state_path = tmp_path / "modelman.toml"
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    result = runner.invoke(app, ["litellm", "off"])
    assert result.exit_code == 0
    assert "litellm.url or litellm.api_key is not set" in result.output


def test_litellm_off_no_warning_when_configured(tmp_path, monkeypatch):
    # Counterpart to test_litellm_off_warns_when_unconfigured: once url/api_key
    # are set, `off` must not print a false-positive warning — a regression
    # here would train users to ignore the warning entirely.
    state_path = tmp_path / "modelman.toml"
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    runner.invoke(app, ["litellm", "set", "--url", "http://localhost:4000", "--api-key", "sk-test"])
    result = runner.invoke(app, ["litellm", "off"])
    assert result.exit_code == 0
    assert "warning" not in result.output


def test_litellm_set_writes_url_and_key(tmp_path, monkeypatch):
    state_path = tmp_path / "modelman.toml"
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    result = runner.invoke(
        app,
        ["litellm", "set", "--url", "http://localhost:4000", "--api-key", "sk-test"],
    )
    assert result.exit_code == 0
    state = load_state(path=state_path)
    assert state.litellm.url == "http://localhost:4000"
    assert state.litellm.api_key == "sk-test"


def test_litellm_status_redacts_key(tmp_path, monkeypatch):
    # `litellm status` is a routine debugging/screen-share command; it must
    # never print the raw proxy API key to stdout, only the redacted
    # `***<last4>` form main.py's litellm_status computes.
    state_path = tmp_path / "modelman.toml"
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    runner.invoke(
        app,
        ["litellm", "set", "--url", "http://localhost:4000", "--api-key", "sk-secret-value"],
    )
    result = runner.invoke(app, ["litellm", "status"])
    assert "sk-secret-value" not in result.stdout


def test_litellm_commands_never_restart_proxy(tmp_path, monkeypatch):
    state_path = tmp_path / "modelman.toml"
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    calls = []
    monkeypatch.setattr(
        "modelman.litellm.restart_litellm_proxy", lambda *a, **k: calls.append(1)
    )
    runner.invoke(app, ["litellm", "on"])
    runner.invoke(app, ["litellm", "off"])
    runner.invoke(app, ["litellm", "set", "--url", "http://x", "--api-key", "k"])
    assert calls == []
