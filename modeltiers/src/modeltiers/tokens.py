"""The token function: a model name or a canonical key reduced to the pieces
two spellings of one model share."""

import re

# Pieces that say where or how a model is served, never which model it is.
DROP = frozenset(
    {
        "cloud",
        "free",
        "batch",
        "latest",
        "it",
        "mlx",
        "4bit",
        "6bit",
        "8bit",
        "nvfp4",
        "fp4",
        "fp8",
        "fp16",
        "bf16",
    }
)
_SEPARATORS = re.compile(r"[ \-_/:()]+")
# Between a letter and a digit, and at a `.` that has a digit on each side.
_INNER = re.compile(r"(?<=[a-z])(?=[0-9])|(?<=[0-9])\.(?=[0-9])")


def tokens(name: str) -> tuple[str, ...]:
    """The token sequence of a name or a canonical key.

    1. Lowercase; discard everything up to and including the first `/`.
    2. Split on spaces, `-`, `_`, `/`, `:`, `(` and `)`.
    3. Drop every piece in DROP.
    4. Split each piece left where a letter is followed by a digit and at
       every `.` between digits. A digit followed by letters stays whole.

    `Qwen3.8-27B-4bit`, `qwen3.8:27b-mlx` and `alibaba/qwen3.8-27b` all give
    `("qwen", "3", "8", "27b")`; `qwen3-8b` gives `("qwen", "3", "8b")`.
    """
    lowered = name.lower()
    _, slash, rest = lowered.partition("/")
    body = rest if slash else lowered
    out: list[str] = []
    for piece in _SEPARATORS.split(body):
        if not piece or piece in DROP:
            continue
        out.extend(part for part in _INNER.split(piece) if part)
    return tuple(out)


def is_floating(key: str) -> bool:
    """A canonical key that ends in `-latest` names whatever is newest, not
    one model, and is left out of the normalization index."""
    return key.lower().endswith("-latest")
