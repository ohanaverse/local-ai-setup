"""Guards on tests/conftest.py's scratch home.

The moved code computes its machine-level inputs from `Path.home()` at import
time: the LiteLLM LaunchAgent plist (it holds the OpenRouter key),
`~/.pi/agent/models.json` (it holds the LiteLLM key), the results directory
and the latest-run pointers. Several are also bound as default arguments, so
no per-test monkeypatch can redirect them. conftest.py points HOME at a
scratch directory before llmbench is imported; these tests fail if it stops.
"""

from __future__ import annotations

import inspect
import os
import pwd
import tempfile
from pathlib import Path

import pytest

from llmbench import registry, state
from llmbench.benchmark import _routes
from llmbench.benchmark import runner as workload_runner
from llmbench.benchmark.agent import pidriver
from llmbench.benchmark.agent import runner as agent_runner
from llmbench.benchmark.agent import suite as agent_suite
from llmbench.benchmark.eval import runner as eval_runner
from llmbench.benchmark.eval import suite as eval_suite
from llmbench.providers.lifecycle import launchd

REAL_HOME = Path(pwd.getpwuid(os.getuid()).pw_dir)

HOME_PATHS = {
    "_routes.LITELLM_PLIST": _routes.LITELLM_PLIST,
    "_routes.LIVE_PI_MODELS_PATH": _routes.LIVE_PI_MODELS_PATH,
    "agent.suite.LITELLM_PLIST": agent_suite.LITELLM_PLIST,
    "agent.pidriver.LIVE_PI_MODELS_PATH": pidriver.LIVE_PI_MODELS_PATH,
    "eval.suite.LITELLM_PLIST": eval_suite.LITELLM_PLIST,
    "eval.suite.LIVE_PI_MODELS_PATH": eval_suite.LIVE_PI_MODELS_PATH,
    "eval.runner.LITELLM_PLIST": eval_runner.LITELLM_PLIST,
    "eval.runner.LIVE_PI_MODELS_PATH": eval_runner.LIVE_PI_MODELS_PATH,
    "launchd.LITELLM_PLIST": launchd.LITELLM_PLIST,
    "launchd.LLAMACPP_PLIST": launchd.LLAMACPP_PLIST,
    "runner.DEFAULT_RESULTS_DIR": workload_runner.DEFAULT_RESULTS_DIR,
    "agent.runner.DEFAULT_RESULTS_DIR": agent_runner.DEFAULT_RESULTS_DIR,
    "eval.runner.DEFAULT_RESULTS_DIR": eval_runner.DEFAULT_RESULTS_DIR,
}

# Parameters whose default was bound to one of those paths when the function
# was defined: a monkeypatch of the module constant does not reach them.
BOUND_DEFAULTS = {
    "_routes.openrouter_key": (_routes.openrouter_key, "plist_path"),
    "_routes.load_live_models": (_routes.load_live_models, "path"),
    "_routes.litellm_credentials": (_routes.litellm_credentials, "live_models_path"),
    "agent.suite.preflight": (agent_suite.preflight, "plist_path"),
    "agent.pidriver.resolve_pi_target": (pidriver.resolve_pi_target, "live_models_path"),
    "agent.runner.run_suite": (agent_runner.run_suite, "live_models_path"),
    "agent.runner._build_judge_transport": (agent_runner._build_judge_transport, "plist_path"),
    "eval.suite.resolve_row_endpoint": (eval_suite.resolve_row_endpoint, "live_models_path"),
    "eval.suite.resolve_row_endpoint[plist]": (eval_suite.resolve_row_endpoint, "plist_path"),
}


def test_home_is_a_scratch_directory():
    """The directory conftest.py made, not the account's home. Named by its
    mkdtemp prefix and not by "outside the real home": TMPDIR may sit inside
    the home directory (`~/tmp`, some sandboxed runners), and a scratch
    directory there is still a scratch directory."""
    assert Path.home() != REAL_HOME
    assert Path.home().name.startswith("llmbench-test-home-")
    assert Path.home().parent == Path(tempfile.gettempdir())
    assert "XDG_CONFIG_HOME" not in os.environ


@pytest.mark.parametrize("name", sorted(HOME_PATHS))
def test_import_time_path_is_under_the_scratch_home(name):
    assert HOME_PATHS[name].is_relative_to(Path.home()), HOME_PATHS[name]


@pytest.mark.parametrize("name", sorted(BOUND_DEFAULTS))
def test_bound_default_is_under_the_scratch_home(name):
    func, parameter = BOUND_DEFAULTS[name]
    default = inspect.signature(func).parameters[parameter].default
    assert default.is_relative_to(Path.home()), default


def test_credentials_are_not_read_from_this_machine(monkeypatch):
    """With nothing passed and nothing patched, the two credential readers
    see an empty home: no OpenRouter key from the LaunchAgent plist, no
    LiteLLM key from pi's models.json."""
    monkeypatch.delenv("OPENROUTER_API_KEY", raising=False)
    # Booleans, so a failure never prints a real key into the test log.
    found_openrouter_key = _routes.openrouter_key() is not None
    found_live_models = bool(_routes.load_live_models())
    assert not found_openrouter_key, "read an OpenRouter key from the real LaunchAgent plist"
    assert not found_live_models, "read the real ~/.pi/agent/models.json"


@pytest.fixture(scope="module")
def wt_registry_exported_in_the_shell(tmp_path_factory):
    """WT_REGISTRY as a developer's shell would export it. Module scope, so it
    is set before conftest's function-scoped autouse fixtures run."""
    exported = pytest.MonkeyPatch()
    exported.setenv("WT_REGISTRY", str(tmp_path_factory.mktemp("shell") / "exported.toml"))
    yield
    exported.undo()


def test_conftest_clears_an_inherited_wt_registry(wt_registry_exported_in_the_shell):
    """conftest must remove a WT_REGISTRY the shell exported: it outranks the
    scratch MODELMAN_REGISTRY conftest sets, so every test that loads the
    registry would read the developer's real one."""
    assert "WT_REGISTRY" not in os.environ
    assert registry.registry_path() == Path(os.environ["MODELMAN_REGISTRY"])


def test_default_config_paths_are_under_the_scratch_home(monkeypatch):
    """Even with every override removed, the registry and the pointer file
    resolve under the scratch home."""
    for name in ("WT_REGISTRY", "MODELMAN_REGISTRY", "LLMBENCH_LATEST"):
        monkeypatch.delenv(name, raising=False)
    assert registry.registry_path().is_relative_to(Path.home())
    assert state.latest_path().is_relative_to(Path.home())
    found_pointers = bool(state.load_state().extra)
    assert not found_pointers, "read latest-run pointers from the real config home"
    with pytest.raises(registry.RegistryError, match="Registry file not found"):
        registry.load_registry()
