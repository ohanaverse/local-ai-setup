"""Make EvalPlus's memory limits best-effort instead of all-or-nothing.

EvalPlus's `reliability_guard` calls `setrlimit(RLIMIT_AS, ...)` in each test
subprocess. macOS 27 rejects that call (`ValueError: current limit exceeds
maximum limit`), so every solution failed and `coding` scored 0.000. This
module replaces `reliability_guard` with one that tries each memory limit and
skips only the ones the OS refuses, then runs EvalPlus's own guard without a
limit for everything else.

It is a `sitecustomize` because EvalPlus runs each solution in a spawned child
on macOS, which re-imports everything: only a module on `PYTHONPATH` reaches
those children. `evalplus_runner` puts this directory on `PYTHONPATH` for the
EvalPlus subprocess only.
"""

import contextlib
import platform


def make_guard(original):  # type: ignore[no-untyped-def]
    """Return a `reliability_guard` that tolerates refused memory limits."""

    def reliability_guard(maximum_memory_bytes=None):  # type: ignore[no-untyped-def]
        if maximum_memory_bytes is not None:
            import resource

            names = ["RLIMIT_AS", "RLIMIT_DATA"]
            if platform.uname().system != "Darwin":
                names.append("RLIMIT_STACK")
            for name in names:
                with contextlib.suppress(ValueError, OSError):
                    resource.setrlimit(
                        getattr(resource, name), (maximum_memory_bytes, maximum_memory_bytes)
                    )
        original(None)

    return reliability_guard


def _install() -> None:
    try:
        import evalplus.eval as evalplus_eval
        import evalplus.eval.utils as evalplus_utils
    except ImportError:
        return
    guard = make_guard(evalplus_utils.reliability_guard)
    evalplus_utils.reliability_guard = guard
    evalplus_eval.reliability_guard = guard


_install()
