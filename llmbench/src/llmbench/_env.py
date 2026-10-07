"""Environment variables that were renamed when llmbench left modelman."""

from __future__ import annotations

import os


def env_first(*names: str) -> str | None:
    """The value of the first of `names` that is set and not empty.

    Callers list the LLMBENCH_ name first and the MODELMAN_ name it replaced
    second, so the old name keeps working and the new one wins when both are
    set.
    """
    for name in names:
        value = os.environ.get(name)
        if value:
            return value
    return None
