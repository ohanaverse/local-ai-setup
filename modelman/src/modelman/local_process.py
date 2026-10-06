"""Shared local-provider process types and HTTP probe helper.

Neutral home for code that both `modelman.benchmark.isolation` (isolating a
provider for a benchmark run) and `modelman.providers.lifecycle` (isolating
a provider for `modelman start`/`modelman stop`) need. Neither concept
belongs to one side more than the other, so living under `benchmark/` or
`providers/` made the other package's import backwards — this module has no
dependency on either.
"""

from __future__ import annotations

import json
import urllib.error
import urllib.request
from dataclasses import dataclass


@dataclass
class ProcessResult:
    provider: str
    model: str
    direct_url: str
    ok: bool
    error: str | None


# Provider ids whose lifecycle backend resolves its model from a single
# LLM_ISOLATE_*_MODEL env var (backends/base.py's _resolve_model).
# mlx_lm_server is deliberately absent: it takes target+draft as positional args.
ENV_VAR_BY_PROVIDER = {
    "ollama": "LLM_ISOLATE_OLLAMA_MODEL",
    "omlx": "LLM_ISOLATE_OMLX_4BIT_MODEL",
    "omlx-6bit": "LLM_ISOLATE_OMLX_6BIT_MODEL",
}


def http_models_ids(url: str, timeout: float = 2.0) -> list[str]:
    """Model ids from an OpenAI-compatible /v1/models response, or [] on any
    error (connection refused, timeout, non-JSON body)."""
    try:
        with urllib.request.urlopen(url, timeout=timeout) as resp:  # noqa: S310 — localhost probe
            data = json.loads(resp.read().decode())
    except (OSError, ValueError):
        return []
    items = data.get("data") if isinstance(data, dict) else None
    if not isinstance(items, list):
        return []
    return [
        item["id"] for item in items if isinstance(item, dict) and isinstance(item.get("id"), str)
    ]


def http_json(url: str, timeout: float = 2.0) -> dict | None:
    """The JSON object `url` answers with, or None on any error (connection
    refused, timeout, non-JSON or non-object body). A non-2xx answer's body is
    still read: omlx's /health carries its pool counts on a 503 too."""
    try:
        with urllib.request.urlopen(url, timeout=timeout) as resp:  # noqa: S310 — localhost probe
            raw = resp.read()
    except urllib.error.HTTPError as exc:
        try:
            raw = exc.read()
        except OSError:
            return None
    except (OSError, ValueError):
        return None
    try:
        data = json.loads(raw.decode())
    except ValueError:
        return None
    return data if isinstance(data, dict) else None


def http_answers(url: str, timeout: float = 2.0) -> bool:
    """Whether `url` answered at all: any HTTP status counts as an answer, a
    refused connection / unresolvable host / timeout does not.

    The three-state sibling of http_models_ids(), whose [] cannot tell "the
    server answered and has nothing" from "the server was never reached"."
    """
    try:
        with urllib.request.urlopen(url, timeout=timeout) as resp:  # noqa: S310 — localhost probe
            resp.read()
    except urllib.error.HTTPError:
        return True  # an error status is still the server answering
    except (OSError, ValueError):
        return False
    return True


def connection_refused(url: str, timeout: float = 2.0) -> bool:
    """Whether the connection to `url` was positively REFUSED: nothing is
    listening on that host and port.

    True for that one failure only. A timeout, a reset, an unresolvable host
    or any answer at all (an error status included) is False: a server that
    is slow or mid-load is still a server, so those say nothing about whether
    one is there. The caller that wants "is it down?" as a fact, not a guess,
    asks this instead of reading http_json()'s or http_answers()'s failure,
    which fold every one of those cases together.
    """
    try:
        with urllib.request.urlopen(url, timeout=timeout) as resp:  # noqa: S310 — localhost probe
            resp.read()
    except urllib.error.HTTPError:
        return False  # an error status is the server answering
    except urllib.error.URLError as exc:
        # urlopen wraps a failed connect: the socket error is `.reason`.
        return isinstance(exc.reason, ConnectionRefusedError)
    except ConnectionRefusedError:
        return True
    except (OSError, ValueError):
        return False
    return False
