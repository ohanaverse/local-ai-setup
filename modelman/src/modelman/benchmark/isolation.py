"""Subprocess contract with local-ai-setup isolation helpers."""

from __future__ import annotations

import json
import shutil
import subprocess
from dataclasses import dataclass

from modelman.benchmark.errors import BenchmarkError

# What bin/llm-isolate-provider can actually isolate, mirroring that script's
# own "Supported:" header comment — the shell script is the real source of
# truth (it owns the case statement), and this constant is the one place
# modelman code checks isolability, kept here next to the rest of the
# subprocess contract with that script rather than rebuilt ad hoc elsewhere.
SUPPORTED_PROVIDER_IDS: frozenset[str] = frozenset({"ollama", "llamacpp", "omlx", "omlx-6bit"})


@dataclass
class IsolateResult:
    provider: str
    model: str
    direct_url: str
    ok: bool
    error: str | None


def _helper_path(name: str) -> str:
    path = shutil.which(name)
    if path is None:
        raise BenchmarkError(
            f"isolation helper '{name}' not found on PATH. Ensure local-ai-setup/bin is on PATH."
        )
    return path


def isolate_provider(provider_id: str) -> IsolateResult:
    """Delegate service isolation to the local-ai-setup helper."""
    helper = _helper_path("llm-isolate-provider")
    result = subprocess.run(
        [helper, provider_id],
        capture_output=True,
        text=True,
        check=False,
    )
    if result.returncode != 0:
        raise BenchmarkError(
            f"isolation failed for {provider_id}: {result.stderr.strip() or result.stdout.strip()}"
        )
    try:
        data = json.loads(result.stdout)
    except json.JSONDecodeError as exc:
        raise BenchmarkError(
            f"isolation helper returned invalid JSON for {provider_id}: {exc}"
        ) from exc
    return IsolateResult(
        provider=data.get("provider", provider_id),
        model=data.get("model", ""),
        direct_url=data.get("direct_url", ""),
        ok=data.get("ok", False),
        error=data.get("error"),
    )


def restore_providers() -> None:
    """Restore all local providers via the local-ai-setup helper."""
    helper = _helper_path("llm-restore-providers")
    result = subprocess.run([helper], capture_output=True, text=True, check=False)
    if result.returncode != 0:
        raise BenchmarkError(
            f"failed to restore providers: {result.stderr.strip() or result.stdout.strip() or 'unknown error'}"
        )
