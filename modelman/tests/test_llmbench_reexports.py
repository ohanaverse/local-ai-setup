"""modelman and llmbench share one ProcessResult and one bridge-exception
hierarchy.

llmbench owns the provider lifecycle modelman's start and stop call. A second
copy of either class would make `except wt_bridge.WtBridgeError` in modelman
miss a failure llmbench raised, and turn a missing `wt` into a traceback.
Deleted with modelman.
"""

import llmbench.local_process
import llmbench.providers.mtplx
import llmbench.providers.registry
import llmbench.registry
import llmbench.wt_bridge
import pytest

from modelman import local_process, wt_bridge
from modelman import registry as mm_registry
from modelman.providers import mtplx as mm_mtplx
from modelman.providers import registry as mm_providers


def test_process_result_is_llmbenchs_class():
    assert local_process.ProcessResult is llmbench.local_process.ProcessResult


@pytest.mark.parametrize("name", ["WtBridgeError", "WtNotFoundError", "WtBridgeTimeoutError"])
def test_bridge_exception_is_llmbenchs_class(name):
    assert getattr(wt_bridge, name) is getattr(llmbench.wt_bridge, name)


def test_modelman_catches_a_bridge_failure_llmbench_raised(monkeypatch):
    """The real path: llmbench's `wt warm` with no wt on PATH, caught by the
    name modelman's callers use."""
    monkeypatch.setattr(llmbench.wt_bridge.shutil, "which", lambda name: None)
    with pytest.raises(wt_bridge.WtBridgeError, match="wt not found on PATH"):
        llmbench.wt_bridge.warm("omlx", "Qwen-4bit")


def test_modelmans_own_bridge_errors_stay_in_the_one_hierarchy():
    assert issubclass(wt_bridge.WtRegistryRedirectedError, llmbench.wt_bridge.WtBridgeError)


@pytest.mark.parametrize(
    ("name", "llmbench_module", "modelman_module"),
    [
        ("DEFAULT_PROVIDER_IDS", llmbench.registry, mm_registry),
        ("ENV_VAR_BY_PROVIDER", llmbench.local_process, local_process),
        ("MTPLX_PORT", llmbench.providers.mtplx, mm_mtplx),
        ("_ALIASES", llmbench.providers.registry, mm_providers),
        ("WARM_TIMEOUT", llmbench.wt_bridge, wt_bridge),
    ],
    ids=lambda value: value if isinstance(value, str) else "",
)
def test_duplicated_constants_agree(name, llmbench_module, modelman_module):
    """These are copies, not re-exports (modelman is frozen). `modelman sync`
    and the TUI seed provider rows from modelman's DEFAULT_PROVIDER_IDS while
    `llmbench run` filters on llmbench's: a backend added to one and not the
    other is silently skipped by one tool."""
    theirs, ours = getattr(llmbench_module, name), getattr(modelman_module, name)
    assert theirs == ours, (
        f"{name} differs: {llmbench_module.__name__} has {theirs!r}, "
        f"{modelman_module.__name__} has {ours!r}; change both copies"
    )
