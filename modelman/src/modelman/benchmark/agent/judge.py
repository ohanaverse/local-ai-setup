"""Blind LLM judge for agent-produced diffs."""

from __future__ import annotations

import json
import re
from dataclasses import dataclass
from statistics import median
from typing import Protocol

DIMENSIONS = ("root_cause", "approach", "test_quality", "scope", "coherence")
MAX_POINTS = {"root_cause": 30, "approach": 25, "test_quality": 20, "scope": 15, "coherence": 10}
VERDICTS = {"symptom_patch", "partial", "principled_fix", "no_useful_change"}


class JudgeContractError(Exception):
    """The judge's response didn't satisfy the strict JSON score contract."""


class JudgeTransportError(Exception):
    """A transport failed to get a response at all (network/API failure).

    Transports raise this after exhausting their own internal retry, so
    judge_row's retry loop treats a network failure exactly like a
    malformed response: one more attempt, then JUDGE_FAIL.
    """


class JudgeTransport(Protocol):
    def complete(self, prompt: str, *, temperature: float) -> str: ...


@dataclass
class JudgeScore:
    scores: dict[str, int]
    total: int
    verdict: str
    flags: list[str]
    rationale: str
    raw_text: str


@dataclass
class JudgeOutcome:
    status: str  # "scored" | "judge_fail"
    samples: list[JudgeScore]
    combined: JudgeScore | None
    attempts_used: int


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
    task_md: str, seed_contents: dict[str, str], diff_text: str, closing_message: str, rubric_md: str
) -> str:
    """Everything the judge sees. No gate results, no hidden tests, no
    meta.toml, no config label, no timing/token stats — the rubric itself
    states the judge must not speculate about test results."""
    seed_section = (
        "\n\n".join(f"--- {path} (baseline) ---\n{content}" for path, content in seed_contents.items())
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
    try:
        data = json.loads(raw_text)
    except json.JSONDecodeError as exc:
        raise JudgeContractError(f"response is not valid JSON: {exc}") from exc

    if not isinstance(data, dict):
        raise JudgeContractError("response is not a JSON object")

    missing = [k for k in ("scores", "total", "verdict") if k not in data]
    if missing:
        raise JudgeContractError(f"response missing keys: {missing}")

    scores = data["scores"]
    missing_dims = [d for d in DIMENSIONS if d not in scores]
    if missing_dims:
        raise JudgeContractError(f"scores missing dimensions: {missing_dims}")
    for dim in DIMENSIONS:
        value = scores[dim]
        if not isinstance(value, int) or not (0 <= value <= MAX_POINTS[dim]):
            raise JudgeContractError(f"invalid score for {dim}: {value!r}")

    if data["verdict"] not in VERDICTS:
        raise JudgeContractError(f"unknown verdict: {data['verdict']!r}")

    return JudgeScore(
        scores={d: scores[d] for d in DIMENSIONS},
        total=int(data["total"]),
        verdict=data["verdict"],
        flags=list(data.get("flags", [])),
        rationale=str(data.get("rationale", "")),
        raw_text=raw_text,
    )


def judge_row(
    transport: JudgeTransport, prompt: str, *, temperature: float, samples: int, max_attempts: int
) -> JudgeOutcome:
    collected: list[JudgeScore] = []
    attempts_used = 0
    for _ in range(samples):
        score: JudgeScore | None = None
        for _attempt in range(max_attempts):
            attempts_used += 1
            try:
                raw = transport.complete(prompt, temperature=temperature)
                score = parse_response(raw)
                break
            except (JudgeContractError, JudgeTransportError):
                continue
        if score is None:
            return JudgeOutcome(status="judge_fail", samples=collected, combined=None, attempts_used=attempts_used)
        collected.append(score)

    if len(collected) == 1:
        combined = collected[0]
    else:
        combined_scores = {dim: round(median(s.scores[dim] for s in collected)) for dim in DIMENSIONS}
        combined = JudgeScore(
            scores=combined_scores,
            total=sum(combined_scores.values()),
            verdict=collected[-1].verdict,
            flags=sorted({f for s in collected for f in s.flags}),
            rationale=collected[-1].rationale,
            raw_text="\n---\n".join(s.raw_text for s in collected),
        )
    return JudgeOutcome(status="scored", samples=collected, combined=combined, attempts_used=attempts_used)


def apply_cap(rubric_total: int, cap: float) -> int:
    return round(rubric_total * cap)


_TEST_PASS_CLAIM_RE = re.compile(r"\b(all tests? pass|tests? (?:are )?passing|tests? succeeded)\b", re.IGNORECASE)


def detect_overclaim(closing_message: str, hidden_pass: int, hidden_total: int) -> bool:
    """Free signal: grep the closing message for a test-passing claim and
    compare against the actual hidden-test ratio — a computed column, not
    a judge dimension."""
    return bool(_TEST_PASS_CLAIM_RE.search(closing_message)) and hidden_pass < hidden_total
