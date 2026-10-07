"""modelman and llmbench share one ProcessResult and one bridge-exception
hierarchy.

llmbench owns the provider lifecycle modelman's start and stop call. A second
copy of either class would make `except wt_bridge.WtBridgeError` in modelman
miss a failure llmbench raised, and turn a missing `wt` into a traceback.
Deleted with modelman.
"""

import inspect
import subprocess

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


# One argv for every _msg case, carrying what _msg exists to keep out of a
# traceback and the TUI. The value is fake.
_FAKE_KEY = "sk-FAKE-not-a-real-key-0000"
_ARGV_WITH_KEY = ["wt", "litellm", "set", "--api-key", _FAKE_KEY]

# (case id, stdout, stderr, the one line both copies must produce)
_MSG_CASES = [
    ("cobra Error: prefix", "", "Error: no such model\n", "no such model"),
    ("wt: prefix", "", "wt: no such model\n", "no such model"),
    ("both prefixes on one line", "", "Error: wt: no such model\n", "no such model"),
    (
        "cobra's line and wt's own line collapse to one",
        "",
        "Error: no such model\nwt: no such model\n",
        "no such model",
    ),
    (
        "distinct lines join with '; '",
        "",
        "Error: first\nwt: second\n",
        "first; second",
    ),
    ("blank lines and padding are dropped", "", "\n  Error:   padded  \n\n", "padded"),
    ("an unprefixed line passes through", "", "plain failure\n", "plain failure"),
    (
        "a prefix is stripped only at the start of a line",
        "",
        "saw Error: x\n",
        "saw Error: x",
    ),
    ("stdout is used when stderr is empty", "wt: from stdout\n", "", "from stdout"),
    ("stdout is used when stderr is only whitespace", "wt: from stdout\n", "  \n", "from stdout"),
    ("stderr wins over stdout", "wt: from stdout\n", "wt: from stderr\n", "from stderr"),
    ("no output at all gives the fallback", "", "", "the fallback"),
    ("only whitespace gives the fallback", " \n", "\n\n", "the fallback"),
]


@pytest.mark.parametrize(
    ("stdout", "stderr", "want"),
    [case[1:] for case in _MSG_CASES],
    ids=[case[0] for case in _MSG_CASES],
)
def test_msg_copies_agree_and_never_show_argv(stdout, stderr, want):
    """#276: `_msg` is what keeps `--api-key <value>` out of every error wt's
    bridge raises, and it exists twice (modelman is frozen, so it is a copy,
    not a re-export). A prefix one copy learns to strip, or an argv field one
    copy starts interpolating, has to fail here rather than drift silently."""
    proc = subprocess.CompletedProcess(
        args=_ARGV_WITH_KEY, returncode=1, stdout=stdout, stderr=stderr
    )
    theirs = llmbench.wt_bridge._msg(proc, "the fallback")
    ours = wt_bridge._msg(proc, "the fallback")
    both = f"{llmbench.wt_bridge.__name__}._msg and {wt_bridge.__name__}._msg"
    assert theirs == ours, (
        f"_msg differs: {llmbench.wt_bridge.__name__} returned {theirs!r}, "
        f"{wt_bridge.__name__} returned {ours!r}; change both copies"
    )
    for secret in ("--api-key", _FAKE_KEY):
        assert secret not in theirs, (
            f"_msg put argv in its message: {both} must build it from wt's output only; "
            "change both copies"
        )
    assert theirs == want, (
        f"_msg changed: {both} now return {theirs!r} where this table expects {want!r}; "
        "change both copies, then this case"
    )


@pytest.mark.parametrize(
    ("name", "llmbench_module", "modelman_module"),
    [
        ("ensure_wt", llmbench.wt_bridge, wt_bridge),
        ("_msg", llmbench.wt_bridge, wt_bridge),
        ("http_models_ids", llmbench.local_process, local_process),
    ],
    ids=lambda value: value if isinstance(value, str) else "",
)
def test_duplicated_functions_are_the_same_source(name, llmbench_module, modelman_module):
    """#276: the constants above fail loudly when they drift; these three
    functions are copies too, and nothing compared them. Byte-for-byte, with
    nothing normalised: an edit to one copy is an edit to both."""
    theirs = inspect.getsource(getattr(llmbench_module, name))
    ours = inspect.getsource(getattr(modelman_module, name))
    assert theirs == ours, (
        f"{name}() differs between {llmbench_module.__name__} and "
        f"{modelman_module.__name__}; change both copies"
    )
