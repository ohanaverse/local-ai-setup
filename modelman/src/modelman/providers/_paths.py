"""Directory identity and nesting for the delete paths.

One rule for "would removing this directory take that one with it", shared by
the shared-artifact guard (registry.find_shared_artifact_owner), which asks it
of two registry entries, and by a provider's own delete, which asks it of one
entry's download and its own `local_path` (mlx_lm_server). Lives under
providers/ because registry.py imports this package, never the reverse.
"""

from __future__ import annotations

import contextlib
import os
from pathlib import Path
from typing import Any

# (keys of the paths themselves, keys of every directory above them) — see
# overlap_keys().
OverlapKeys = tuple[frozenset[Any], frozenset[Any]]


def canonical_paths(paths: Any) -> frozenset[Any]:
    """Keys for `paths` such that two spellings of one directory share a key
    (#235): each path resolved — `~` expanded, relative and `..` segments and
    a trailing slash collapsed, symlinks followed — and, for one that exists,
    its (device, inode) as well. A `local_path` is stored as typed, and the
    providers hand it over that way.

    The (device, inode) key is what catches a spelling realpath leaves alone:
    realpath keeps the letter case a path was typed in, and on a
    case-insensitive volume (the macOS default) `models/Qwen` and
    `models/qwen` are one directory. Anything that is not a string is passed
    through untouched."""
    keys: set[Any] = set()
    for p in paths:
        if not isinstance(p, str):
            keys.add(p)
            continue
        real = os.path.realpath(os.path.expanduser(p))
        keys.add(real)
        with contextlib.suppress(OSError):
            st = os.stat(real)
            keys.add((st.st_dev, st.st_ino))
    return frozenset(keys)


def _parents(paths: Any) -> list[str]:
    """Every directory above each of `paths`, resolved. Non-strings (a test
    double's stand-in for a path) have none."""
    found: list[str] = []
    for p in paths:
        if isinstance(p, str):
            real = Path(os.path.realpath(os.path.expanduser(p)))
            found.extend(str(parent) for parent in real.parents)
    return found


def overlap_keys(paths: Any) -> OverlapKeys:
    """What keys_overlap() compares, for one side. Resolving a path and each
    of its parents costs a realpath and a stat apiece, so a caller holding one
    side fixed across many comparisons (the guard: what is removed, against
    every other entry) computes that side once."""
    return canonical_paths(paths), canonical_paths(_parents(paths))


def keys_overlap(removable: OverlapKeys, theirs: OverlapKeys) -> bool:
    """Whether removing the directories behind `removable` would take anything
    behind `theirs` with it: one of theirs is the same directory as one
    removed, inside one (a quantize output written beside its source), or
    contains one (#241). The last is the cautious reading — an entry naming a
    parent loses part of what it points at, and a refused delete can be undone
    where an rmtree cannot.

    Nesting is tested on the same keys as equality (canonical_paths) for a
    path and each of its parents, never on string prefixes: `models/M2` is
    not inside `models/M`, and on a case-insensitive volume `models/m/out` is
    inside `models/M`."""
    removable_keys, removable_parents = removable
    their_keys, their_parents = theirs
    return bool(
        removable_keys & their_keys
        or removable_keys & their_parents
        or their_keys & removable_parents
    )


def paths_overlap(removable: Any, theirs: Any) -> bool:
    """keys_overlap() for two sets of paths compared once."""
    return keys_overlap(overlap_keys(removable), overlap_keys(theirs))
