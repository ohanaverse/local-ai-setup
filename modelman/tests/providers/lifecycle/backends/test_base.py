import pytest

from modelman.providers.lifecycle.backends.base import Backend, StartPlan
from modelman.providers.lifecycle.envelope import LifecycleError


class _FakeBackend(Backend):
    """Minimal concrete Backend built only to exercise _resolve_model's
    precedence — the abstract methods are stubbed and never exercised by
    these tests."""

    id = "fake"
    occupancy_key = "fake"
    env_var = "FAKE_MODEL_ENV"
    default_model = "default-model"
    health_url = "http://localhost:9/health"
    chat_url = "http://localhost:9/v1/chat/completions"

    def check_available(self) -> str | None:
        return None

    def resolve(self, model, extra_args):
        return StartPlan(model=model or "", direct_url=self.chat_url)

    def start(self, plan: StartPlan) -> None:
        pass

    def stop_and_wait(self) -> str | None:
        return None


def test_resolve_model_explicit_wins_over_env_and_default(monkeypatch):
    """An explicit model argument must win over both the env var and the
    backend's default — this is the top of the precedence chain every
    backend's resolve() depends on."""
    monkeypatch.setenv("FAKE_MODEL_ENV", "env-model")
    backend = _FakeBackend()
    assert backend._resolve_model("explicit-model") == "explicit-model"


def test_resolve_model_env_var_wins_over_default(monkeypatch):
    """With no explicit model, the env var must win over the backend's
    default_model."""
    monkeypatch.setenv("FAKE_MODEL_ENV", "env-model")
    backend = _FakeBackend()
    assert backend._resolve_model(None) == "env-model"


def test_resolve_model_falls_back_to_default(monkeypatch):
    """With no explicit model and no env var set, the backend's
    default_model must be used."""
    monkeypatch.delenv("FAKE_MODEL_ENV", raising=False)
    backend = _FakeBackend()
    assert backend._resolve_model(None) == "default-model"


def test_resolve_model_empty_env_var_falls_through_to_default(monkeypatch):
    """An env var set to an empty string must be treated the same as
    unset, not as an explicit (empty) override."""
    monkeypatch.setenv("FAKE_MODEL_ENV", "")
    backend = _FakeBackend()
    assert backend._resolve_model(None) == "default-model"


def test_resolve_model_required_raises_when_nothing_resolves(monkeypatch):
    """required=True with no explicit arg, no env var, and no default
    must raise LifecycleError with the caller-supplied message — this is
    what lets each backend give its own actionable error text."""
    monkeypatch.delenv("FAKE_MODEL_ENV", raising=False)
    backend = _FakeBackend()
    backend.default_model = None
    with pytest.raises(LifecycleError, match="no model available"):
        backend._resolve_model(None, required=True, required_message="no model available")


def test_resolve_model_required_does_not_raise_when_default_resolves(monkeypatch):
    """required=True must not raise when a default_model resolves the
    model, even with no explicit arg or env var."""
    monkeypatch.delenv("FAKE_MODEL_ENV", raising=False)
    backend = _FakeBackend()
    assert backend._resolve_model(None, required=True, required_message="unused") == "default-model"
