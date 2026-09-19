"""Blind LLM judge for agent-produced diffs.

The rubric-scoring mechanics (strict-JSON parsing, retry, multi-sample
median) now live in modelman.benchmark.judge_core, generalized for reuse by
modelman.benchmark.eval's per-category rubrics. This module keeps only what
is specific to scoring a diff: the fixed diff-fixing Rubric, diff/message
anonymization, and overclaim detection. Every function below keeps its old
signature so no caller needs to change.
"""

from __future__ import annotations

import re

import requests  # noqa: F401 (imported for test patching)

from modelman.benchmark.judge_core import (
    JudgeContractError,
    JudgeOutcome,
    JudgeScore,
    JudgeTransport,
    JudgeTransportError,
    LiteLLMJudgeTransport,
    Rubric,
)
from modelman.benchmark.judge_core import judge_row as _judge_row
from modelman.benchmark.judge_core import parse_response as _parse_response

DIMENSIONS = ("root_cause", "approach", "test_quality", "scope", "coherence")
MAX_POINTS = {"root_cause": 30, "approach": 25, "test_quality": 20, "scope": 15, "coherence": 10}
VERDICTS = {"symptom_patch", "partial", "principled_fix", "no_useful_change"}

AGENT_RUBRIC = Rubric(dimensions=dict(MAX_POINTS), verdicts=frozenset(VERDICTS))

__all__ = [
    "DIMENSIONS",
    "MAX_POINTS",
    "VERDICTS",
    "AGENT_RUBRIC",
    "JudgeContractError",
    "JudgeTransportError",
    "JudgeTransport",
    "JudgeScore",
    "JudgeOutcome",
    "LiteLLMJudgeTransport",
    "anonymize_diff",
    "anonymize_message",
    "build_prompt",
    "parse_response",
    "judge_row",
    "apply_cap",
    "detect_overclaim",
]

_DIFF_HEADER_RE = re.compile(r"^diff --git a/\S+ b/\S+$", re.MULTILINE)
_MINUS_HEADER_RE = re.compile(r"^--- a/\S+$", re.MULTILINE)
_PLUS_HEADER_RE = re.compile(r"^\+\+\+ b/\S+$", re.MULTILINE)


def anonymize_diff(diff_text: str, max_chars: int = 20000) -> str:
    """Normalize diff headers (they embed temp workspace paths carrying the
    run id) and truncate oversized diffs with an explicit marker."""
    text = _DIFF_HEADER_RE.sub("diff --git a/<file> b/<file>", diff_text)
    text = _MINUS_HEADER_RE.sub("--- a/<file>", text)
    text = _PLUS_HEADER_RE.sub("+++ b/<file>", text)
    if len(text) > max_chars:
        text = text[:max_chars] + "\n[TRUNCATED]\n"
    return text


_MODEL_TOKEN_RE = re.compile(r"\b(?:ollama|omlx|llamacpp|openrouter|litellm)/[\w.\-:]+\b")
_TIMESTAMP_RE = re.compile(r"\b\d{4}-\d{2}-\d{2}T[\d:.]+Z?\b")
_SESSION_TOKEN_RE = re.compile(r"\b(session|token)[-_ ]?id\s*[:=]\s*\S+", re.IGNORECASE)


def anonymize_message(message: str) -> str:
    """Strip model/provider/session/token strings and timestamps."""
    text = _MODEL_TOKEN_RE.sub("<model>", message)
    text = _TIMESTAMP_RE.sub("<timestamp>", text)
    text = _SESSION_TOKEN_RE.sub(lambda m: f"{m.group(1)}: <redacted>", text)
    return text


def build_prompt(
    task_md: str,
    seed_contents: dict[str, str],
    diff_text: str,
    closing_message: str,
    rubric_md: str,
) -> str:
    """Everything the judge sees. No gate results, no hidden tests, no
    meta.toml, no config label, no timing/token stats — the rubric itself
    states the judge must not speculate about test results."""
    seed_section = (
        "\n\n".join(
            f"--- {path} (baseline) ---\n{content}" for path, content in seed_contents.items()
        )
        or "(no baseline files touched)"
    )
    return (
        f"{rubric_md}\n\n"
        f"## Task\n{task_md}\n\n"
        f"## Baseline contents of touched files\n{seed_section}\n\n"
        f"## Diff\n```diff\n{diff_text}\n```\n\n"
        f"## Agent's closing message\n{anonymize_message(closing_message)}\n\n"
        "Respond with strict JSON matching the schema above. No prose outside the JSON object."
    )


def parse_response(raw_text: str) -> JudgeScore:
    return _parse_response(raw_text, AGENT_RUBRIC)


def judge_row(
    transport: JudgeTransport, prompt: str, *, temperature: float, samples: int, max_attempts: int
) -> JudgeOutcome:
    return _judge_row(
        transport,
        prompt,
        AGENT_RUBRIC,
        temperature=temperature,
        samples=samples,
        max_attempts=max_attempts,
    )


def apply_cap(rubric_total: int, cap: float) -> int:
    return round(rubric_total * cap)


_TEST_PASS_CLAIM_RE = re.compile(
    r"\b(all tests? pass|tests? (?:are )?passing|tests? succeeded)\b", re.IGNORECASE
)


def detect_overclaim(closing_message: str, hidden_pass: int, hidden_total: int) -> bool:
    """Free signal: grep the closing message for a test-passing claim and
    compare against the actual hidden-test ratio — a computed column, not
    a judge dimension."""
    return bool(_TEST_PASS_CLAIM_RE.search(closing_message)) and hidden_pass < hidden_total
