# Capability Eval Benchmark (`modelman benchmark eval`) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `modelman benchmark eval` — a third benchmark module tree that scores local models (ollama/omlx/mtplx) across five use-case categories (reasoning, planning, coding, code_review, doc_summary), so quant/engine choices can be made per use case instead of on throughput alone.

**Architecture:** A generalized LLM-judge core (`benchmark/judge_core.py`) extracted from the agent benchmark's rubric mechanics, reused by a new `benchmark/eval/` module tree that does single-turn generation + judging for four categories and shells out to EvalPlus for `coding`. Reuses existing provider isolation (`providers/lifecycle/orchestrate.py`) and the direct/litellm/openrouter route pattern unchanged.

**Tech Stack:** Python 3.13, typer, tomllib/tomli-w, requests, pytest, EvalPlus (optional extra).

**Spec:** `docs/superpowers/specs/2026-09-17-capability-eval-benchmark-design.md`

## Global Constraints

- Run everything from `/Users/keith/github/ohanaverse/local-ai-setup/modelman` with `uv run ...` — modelman is not installed globally.
- `agent/judge.py`'s existing public names and behavior must be unchanged after the `judge_core` extraction — its existing tests (`tests/benchmark/agent/test_judge.py`) must pass with zero edits.
- Every judged category's `rubric.toml` dimensions must sum to exactly 100.
- `coding` is graded by EvalPlus only — no judge, no rubric.toml/rubric.md for that category.
- EvalPlus is an optional dependency (`modelman[eval]`), never installed by plain `make install`/default `uv sync`.
- Every eval item's ground-truth/reference notes go in `meta` (never sent to the model or the judge as part of the scored prompt) — same convention as `day31-drift`'s `meta.toml`.
- Ruff (`line-length = 100`, target `py313`) and mypy must stay clean; run `uv run ruff check .` / `uv run ruff format .` / `uv run mypy src` before each commit that touches `src/`.

---

## Task 1: Extract `judge_core.py` — generalized rubric-judge mechanics

**Files:**
- Create: `src/modelman/benchmark/judge_core.py`
- Test: `tests/benchmark/test_judge_core.py`

**Interfaces:**
- Produces: `Rubric(dimensions: dict[str, int], verdicts: frozenset[str] | None = None)`, `JudgeScore`, `JudgeOutcome`, `JudgeContractError`, `JudgeTransportError`, `JudgeTransport` (Protocol with `complete(prompt, *, temperature) -> str`), `parse_response(raw_text: str, rubric: Rubric) -> JudgeScore`, `judge_row(transport, prompt, rubric, *, temperature, samples, max_attempts) -> JudgeOutcome`, `LiteLLMJudgeTransport(base_url, api_key, model, *, retry_backoff_s=2.0)`.

- [ ] **Step 1: Write the failing tests**

```python
# tests/benchmark/test_judge_core.py
"""Tests for modelman.benchmark.judge_core — the rubric-parameterized judge
mechanics shared by the agent and eval benchmarks.

A second, differently-shaped Rubric (3 dimensions, no verdict enum) is
tested alongside a verdict-bearing one to prove the module is genuinely
generalized, not just the agent benchmark's old constants renamed.
"""

import json

import pytest

from modelman.benchmark.judge_core import (
    JudgeContractError,
    Rubric,
    judge_row,
    parse_response,
)

VERDICT_RUBRIC = Rubric(
    dimensions={"a": 60, "b": 40}, verdicts=frozenset({"good", "bad"})
)
NO_VERDICT_RUBRIC = Rubric(dimensions={"x": 50, "y": 30, "z": 20}, verdicts=None)


def test_parse_response_accepts_rubric_without_verdicts():
    raw = json.dumps({"scores": {"x": 40, "y": 20, "z": 10}, "total": 70})
    score = parse_response(raw, NO_VERDICT_RUBRIC)
    assert score.total == 70
    assert score.verdict == ""


def test_parse_response_rejects_score_over_dimension_max():
    raw = json.dumps({"scores": {"x": 999, "y": 20, "z": 10}, "total": 70})
    with pytest.raises(JudgeContractError, match="x"):
        parse_response(raw, NO_VERDICT_RUBRIC)


def test_parse_response_validates_verdict_when_rubric_requires_it():
    raw = json.dumps({"scores": {"a": 50, "b": 30}, "total": 80, "verdict": "ok"})
    with pytest.raises(JudgeContractError, match="verdict"):
        parse_response(raw, VERDICT_RUBRIC)


class _StubTransport:
    def __init__(self, replies: list[str]):
        self.replies = list(replies)

    def complete(self, prompt: str, *, temperature: float) -> str:
        return self.replies.pop(0)


def test_judge_row_combines_multiple_samples_by_median():
    replies = [
        json.dumps({"scores": {"x": 40, "y": 20, "z": 10}, "total": 70}),
        json.dumps({"scores": {"x": 50, "y": 30, "z": 20}, "total": 100}),
        json.dumps({"scores": {"x": 30, "y": 10, "z": 0}, "total": 40}),
    ]
    outcome = judge_row(
        _StubTransport(replies),
        "prompt",
        NO_VERDICT_RUBRIC,
        temperature=0.0,
        samples=3,
        max_attempts=1,
    )
    assert outcome.status == "scored"
    assert outcome.combined.scores["x"] == 40  # median of 40/50/30


def test_judge_row_returns_judge_fail_after_exhausting_attempts():
    outcome = judge_row(
        _StubTransport(["not json", "still not json"]),
        "prompt",
        NO_VERDICT_RUBRIC,
        temperature=0.0,
        samples=1,
        max_attempts=2,
    )
    assert outcome.status == "judge_fail"
    assert outcome.combined is None
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `uv run pytest tests/benchmark/test_judge_core.py -v`
Expected: `ModuleNotFoundError: No module named 'modelman.benchmark.judge_core'`

- [ ] **Step 3: Write `judge_core.py`**

Copy the following logic verbatim from the current `src/modelman/benchmark/agent/judge.py` (lines 1-61: module docstring through `_PLUS_HEADER_RE`, minus the diff-specific regexes) plus the response-parsing/retry/transport machinery, generalized to take a `Rubric`:

```python
# src/modelman/benchmark/judge_core.py
"""Shared LLM-judge mechanics: strict-JSON rubric scoring, retry-on-malformed
reply, and multi-sample median-combine.

Generalized out of the agent benchmark's diff-fixing judge (originally
hardcoded to one rubric) so `benchmark.eval` can reuse the same mechanics
with a different Rubric per capability category. `benchmark.agent.judge`
wraps this module with its fixed diff-fixing Rubric and keeps everything
diff-specific (anonymization, overclaim detection) to itself.
"""

from __future__ import annotations

import json
import re
import time
from dataclasses import dataclass
from statistics import median
from typing import Protocol

import requests


class JudgeContractError(Exception):
    """The judge's response didn't satisfy the strict JSON score contract."""


class JudgeTransportError(Exception):
    """A transport failed to get a response at all (network/API failure).

    Transports raise this after exhausting their own internal retry, so
    judge_row's retry loop treats a network failure exactly like a
    malformed response: one more attempt, then judge_fail.
    """


class JudgeTransport(Protocol):
    def complete(self, prompt: str, *, temperature: float) -> str: ...


@dataclass(frozen=True)
class Rubric:
    """A judge's scoring contract: dimension name -> max points (must sum to
    however the caller defines "full marks" for its own categories — the
    agent benchmark and the eval benchmark both use 100), and an optional
    closed set of verdict labels. verdicts=None means the response need not
    (and is not required to) carry a "verdict" key at all."""

    dimensions: dict[str, int]
    verdicts: frozenset[str] | None = None


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
    error: str | None = None


_FENCE_RE = re.compile(r"```(?:json)?\s*(.*?)\s*```", re.DOTALL)
_ANSWER_KEYS = {"scores", "total"}


def _json_candidate(raw_text: str) -> str:
    """The part of a reply that should be parsed as JSON.

    Scans every '{' for a parseable dict rather than stopping at the first
    one, because a reply can contain an incidental JSON-looking fragment
    before the judge's actual answer. The first candidate with both answer
    keys wins; failing that, the first parseable dict is a fallback."""
    fenced = _FENCE_RE.search(raw_text)
    text = fenced.group(1) if fenced else raw_text
    decoder = json.JSONDecoder()
    idx = 0
    fallback: str | None = None
    while idx < len(text):
        if text[idx] == "{":
            try:
                obj, end = decoder.raw_decode(text, idx)
                if isinstance(obj, dict):
                    if obj.keys() >= _ANSWER_KEYS:
                        return text[idx:end]
                    if fallback is None:
                        fallback = text[idx:end]
            except json.JSONDecodeError:
                pass
        idx += 1
    return fallback if fallback is not None else raw_text


def parse_response(raw_text: str, rubric: Rubric) -> JudgeScore:
    try:
        data = json.loads(_json_candidate(raw_text))
    except json.JSONDecodeError as exc:
        raise JudgeContractError(f"response is not valid JSON: {exc}") from exc
    if not isinstance(data, dict):
        raise JudgeContractError("response is not a JSON object")

    missing = [k for k in ("scores", "total") if k not in data]
    if missing:
        raise JudgeContractError(f"response missing keys: {missing}")

    scores = data["scores"]
    missing_dims = [d for d in rubric.dimensions if d not in scores]
    if missing_dims:
        raise JudgeContractError(f"scores missing dimensions: {missing_dims}")
    for dim, max_points in rubric.dimensions.items():
        value = scores[dim]
        if isinstance(value, bool) or not isinstance(value, int) or not (0 <= value <= max_points):
            raise JudgeContractError(f"invalid score for {dim}: {value!r}")

    verdict = data.get("verdict", "")
    if rubric.verdicts is not None and verdict not in rubric.verdicts:
        raise JudgeContractError(f"unknown verdict: {verdict!r}")

    return JudgeScore(
        scores={d: scores[d] for d in rubric.dimensions},
        total=int(data["total"]),
        verdict=str(verdict),
        flags=list(data.get("flags", [])),
        rationale=str(data.get("rationale", "")),
        raw_text=raw_text,
    )


def judge_row(
    transport: JudgeTransport,
    prompt: str,
    rubric: Rubric,
    *,
    temperature: float,
    samples: int,
    max_attempts: int,
) -> JudgeOutcome:
    collected: list[JudgeScore] = []
    attempts_used = 0
    last_error: str | None = None
    for _ in range(samples):
        score: JudgeScore | None = None
        for _attempt in range(max_attempts):
            attempts_used += 1
            try:
                raw = transport.complete(prompt, temperature=temperature)
                score = parse_response(raw, rubric)
                last_error = None
                break
            except (JudgeContractError, JudgeTransportError) as exc:
                last_error = f"{type(exc).__name__}: {exc}"
                continue
        if score is None:
            return JudgeOutcome(
                status="judge_fail",
                samples=collected,
                combined=None,
                attempts_used=attempts_used,
                error=last_error,
            )
        collected.append(score)

    if len(collected) == 1:
        combined = collected[0]
    else:
        combined_scores = {
            dim: round(median(s.scores[dim] for s in collected)) for dim in rubric.dimensions
        }
        combined = JudgeScore(
            scores=combined_scores,
            total=sum(combined_scores.values()),
            verdict=collected[-1].verdict,
            flags=sorted({f for s in collected for f in s.flags}),
            rationale=collected[-1].rationale,
            raw_text="\n---\n".join(s.raw_text for s in collected),
        )
    return JudgeOutcome(
        status="scored", samples=collected, combined=combined, attempts_used=attempts_used
    )


class LiteLLMJudgeTransport:
    """A plain OpenAI-compatible chat-completions call at a fixed
    temperature. Generic enough to double as a row's own generation
    transport in the eval benchmark, not just the judge's."""

    def __init__(self, base_url: str, api_key: str, model: str, *, retry_backoff_s: float = 2.0):
        self.base_url = base_url.rstrip("/")
        self.api_key = api_key
        self.model = model
        self.retry_backoff_s = retry_backoff_s

    def complete(self, prompt: str, *, temperature: float) -> str:
        try:
            return self._post(prompt, temperature)
        except requests.RequestException:
            time.sleep(self.retry_backoff_s)
            try:
                return self._post(prompt, temperature)
            except requests.RequestException as exc:
                raise JudgeTransportError(f"judge transport failed after retry: {exc}") from exc

    def _post(self, prompt: str, temperature: float) -> str:
        response = requests.post(
            f"{self.base_url}/chat/completions",
            headers={"Authorization": f"Bearer {self.api_key}"},
            json={
                "model": self.model,
                "messages": [{"role": "user", "content": prompt}],
                "temperature": temperature,
                "stream": False,
            },
            timeout=120,
        )
        if response.status_code >= 400:
            raise JudgeTransportError(
                f"HTTP {response.status_code} from {self.base_url}/chat/completions: "
                f"{response.text[:200]}"
            )
        try:
            data = response.json()
            return data["choices"][0]["message"]["content"]
        except (json.JSONDecodeError, KeyError, IndexError, TypeError) as exc:
            raise JudgeTransportError(
                f"malformed 200 response from {self.base_url}/chat/completions: "
                f"{type(exc).__name__}: {response.text[:200]}"
            ) from exc
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `uv run pytest tests/benchmark/test_judge_core.py -v`
Expected: all 5 PASS

- [ ] **Step 5: Lint and commit**

```bash
uv run ruff check src/modelman/benchmark/judge_core.py tests/benchmark/test_judge_core.py
uv run ruff format src/modelman/benchmark/judge_core.py tests/benchmark/test_judge_core.py
git add src/modelman/benchmark/judge_core.py tests/benchmark/test_judge_core.py
git commit -m "feat(modelman): add judge_core with a rubric-parameterized LLM judge

Extracts the generalizable half of the agent benchmark's judge (strict-
JSON scoring, retry, multi-sample median) so a second rubric shape can
reuse it. completes plan item #1 of docs/superpowers/plans/2026-09-17-capability-eval-benchmark.md"
```

---

## Task 2: Rewire `agent/judge.py` onto `judge_core` (behavior-preserving)

**Files:**
- Modify: `src/modelman/benchmark/agent/judge.py`
- Test: `tests/benchmark/agent/test_judge.py` (must pass unmodified)

**Interfaces:**
- Consumes: `judge_core.Rubric`, `judge_core.parse_response`, `judge_core.judge_row`, `judge_core.JudgeScore/JudgeOutcome/JudgeContractError/JudgeTransportError/JudgeTransport/LiteLLMJudgeTransport`
- Produces: same public names as before (`DIMENSIONS`, `MAX_POINTS`, `VERDICTS`, `JudgeContractError`, `JudgeTransportError`, `JudgeTransport`, `JudgeScore`, `JudgeOutcome`, `LiteLLMJudgeTransport`, `anonymize_diff`, `anonymize_message`, `build_prompt`, `parse_response(raw_text) -> JudgeScore`, `judge_row(transport, prompt, *, temperature, samples, max_attempts) -> JudgeOutcome`, `apply_cap`, `detect_overclaim`) — identical signatures, so no other file in the repo needs to change.

- [ ] **Step 1: Run the existing test file first, to have a baseline**

Run: `uv run pytest tests/benchmark/agent/test_judge.py -v`
Expected: currently PASS against the old implementation (baseline before the refactor).

- [ ] **Step 2: Rewrite `agent/judge.py` to wrap `judge_core`**

```python
# src/modelman/benchmark/agent/judge.py
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
    return bool(_TEST_PASS_CLAIM_RE.search(closing_message)) and hidden_pass < hidden_total
```

- [ ] **Step 3: Run the existing test file again — zero edits, must still pass**

Run: `uv run pytest tests/benchmark/agent/test_judge.py -v`
Expected: all PASS, identical to Step 1's baseline (this is the whole point of the refactor)

- [ ] **Step 4: Run the full agent-benchmark test suite as a regression check**

Run: `uv run pytest tests/benchmark/agent/ -v`
Expected: all PASS (nothing outside judge.py touched, but `report.py`/`runner.py` import from `agent.judge`, so this confirms nothing broke transitively)

- [ ] **Step 5: Lint and commit**

```bash
uv run ruff check src/modelman/benchmark/agent/judge.py
uv run ruff format src/modelman/benchmark/agent/judge.py
git add src/modelman/benchmark/agent/judge.py
git commit -m "refactor(modelman): rewire agent/judge.py onto judge_core

Behavior-preserving: agent/judge.py keeps every public name and signature,
now as a thin wrapper around judge_core with its fixed AGENT_RUBRIC.
completes plan item #2 of docs/superpowers/plans/2026-09-17-capability-eval-benchmark.md"
```

---

## Task 3: `eval/category.py` — category directory loader

**Files:**
- Create: `src/modelman/benchmark/eval/__init__.py` (empty)
- Create: `src/modelman/benchmark/eval/category.py`
- Test: `tests/benchmark/eval/__init__.py` (empty)
- Test: `tests/benchmark/eval/test_category.py`
- Test fixtures: `tests/benchmark/eval/fixtures/categories/mini_review/{items.toml,rubric.toml,rubric.md,meta.toml}`, `tests/benchmark/eval/fixtures/categories/coding/items.toml` (both under `fixtures/categories/`, which `list_categories` is tested against), plus `tests/benchmark/eval/fixtures/categories_invalid/bad_sum/{items.toml,rubric.toml,rubric.md}` in a **separate** fixture root (a malformed category must not be a child of the root `list_categories` iterates in its own test, since `list_categories` — like `agent/task.py`'s `list_task_bundles` — deliberately does not swallow a load error)

**Interfaces:**
- Produces: `CODING_CATEGORY = "coding"`, `Item(id, prompt, meta)`, `Category(name, path, items, rubric, rubric_md, coding_dataset, coding_limit)`, `load_category(path) -> Category`, `list_categories(root) -> list[Category]`. Raises `BenchmarkError` on missing/malformed content.

- [ ] **Step 1: Write the fixture files**

```toml
# tests/benchmark/eval/fixtures/categories/mini_review/items.toml
[[items]]
id = "off-by-one"
prompt = "Review this loop for correctness:\nfor i in range(len(items) + 1):\n    total += items[i]"

[items.meta]
seeded_issue = "range(len(items) + 1) reads one past the end"
```

```toml
# tests/benchmark/eval/fixtures/categories/mini_review/rubric.toml
[dimensions]
bug_detection = 60
actionability = 40
```

```markdown
<!-- tests/benchmark/eval/fixtures/categories/mini_review/rubric.md -->
# Judge rubric — mini_review (test fixture)

| dimension | pts |
|---|---|
| bug_detection | 60 |
| actionability | 40 |
```

```toml
# tests/benchmark/eval/fixtures/categories/mini_review/meta.toml
[meta]
note = "test fixture only"
```

```toml
# tests/benchmark/eval/fixtures/categories/coding/items.toml
dataset = "humaneval"
limit = 5
```

```toml
# tests/benchmark/eval/fixtures/categories_invalid/bad_sum/items.toml
[[items]]
id = "x"
prompt = "irrelevant"
```

```toml
# tests/benchmark/eval/fixtures/categories_invalid/bad_sum/rubric.toml
[dimensions]
a = 60
b = 60
```

```markdown
<!-- tests/benchmark/eval/fixtures/categories_invalid/bad_sum/rubric.md -->
irrelevant
```

- [ ] **Step 2: Write the failing tests**

```python
# tests/benchmark/eval/test_category.py
"""Tests for modelman.benchmark.eval.category — loading a category
directory (items.toml + rubric.toml/rubric.md + meta.toml, except `coding`
which is EvalPlus-config-only)."""

from pathlib import Path

import pytest

from modelman.benchmark.eval.category import CODING_CATEGORY, load_category, list_categories
from modelman.benchmark.errors import BenchmarkError

FIXTURE_ROOT = Path(__file__).parent / "fixtures" / "categories"
INVALID_FIXTURE_ROOT = Path(__file__).parent / "fixtures" / "categories_invalid"


def test_load_category_reads_items_and_rubric():
    category = load_category(FIXTURE_ROOT / "mini_review")
    assert category.name == "mini_review"
    assert len(category.items) == 1
    assert category.items[0].id == "off-by-one"
    assert category.items[0].meta["seeded_issue"].startswith("range(")
    assert category.rubric is not None
    assert category.rubric.dimensions == {"bug_detection": 60, "actionability": 40}
    assert "mini_review" in category.rubric_md


def test_load_category_coding_has_no_rubric():
    category = load_category(FIXTURE_ROOT / "coding")
    assert category.name == CODING_CATEGORY
    assert category.rubric is None
    assert category.coding_dataset == "humaneval"
    assert category.coding_limit == 5


def test_load_category_rejects_rubric_not_summing_to_100():
    with pytest.raises(BenchmarkError, match="sum to 100"):
        load_category(INVALID_FIXTURE_ROOT / "bad_sum")


def test_list_categories_finds_every_subdirectory():
    names = {c.name for c in list_categories(FIXTURE_ROOT)}
    assert names == {"mini_review", "coding"}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `uv run pytest tests/benchmark/eval/test_category.py -v`
Expected: `ModuleNotFoundError: No module named 'modelman.benchmark.eval'`

- [ ] **Step 4: Write `category.py`**

```python
# src/modelman/benchmark/eval/category.py
"""Category directory loading for `modelman benchmark eval`.

A category lives under benchmarks/tasks/eval/<name>/: items.toml (the fixed
prompt set) plus rubric.toml/rubric.md (except `coding`, which EvalPlus
grades and which instead uses items.toml to hold dataset/limit config).
"""

from __future__ import annotations

import tomllib
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

from modelman.benchmark.errors import BenchmarkError
from modelman.benchmark.judge_core import Rubric

CODING_CATEGORY = "coding"


@dataclass
class Item:
    id: str
    prompt: str
    meta: dict[str, Any] = field(default_factory=dict)


@dataclass
class Category:
    name: str
    path: Path
    items: list[Item]
    rubric: Rubric | None
    rubric_md: str | None
    coding_dataset: str | None = None
    coding_limit: int | None = None


def _load_coding_category(name: str, path: Path, items_raw: dict[str, Any]) -> Category:
    return Category(
        name=name,
        path=path,
        items=[],
        rubric=None,
        rubric_md=None,
        coding_dataset=items_raw.get("dataset", "humaneval"),
        coding_limit=items_raw.get("limit"),
    )


def load_category(path: Path) -> Category:
    path = Path(path)
    name = path.name
    if not path.is_dir():
        raise BenchmarkError(f"category not found: {path}")

    items_path = path / "items.toml"
    if not items_path.is_file():
        raise BenchmarkError(f"category {path} missing items.toml")
    with items_path.open("rb") as f:
        items_raw = tomllib.load(f)

    if name == CODING_CATEGORY:
        return _load_coding_category(name, path, items_raw)

    rubric_toml_path = path / "rubric.toml"
    rubric_md_path = path / "rubric.md"
    missing = [
        n
        for n, p in (("rubric.toml", rubric_toml_path), ("rubric.md", rubric_md_path))
        if not p.is_file()
    ]
    if missing:
        raise BenchmarkError(f"category {path} missing required entries: {', '.join(missing)}")

    with rubric_toml_path.open("rb") as f:
        rubric_raw = tomllib.load(f)
    dimensions = rubric_raw.get("dimensions", {})
    if not dimensions:
        raise BenchmarkError(f"category {path} rubric.toml has no [dimensions]")
    total_points = sum(dimensions.values())
    if total_points != 100:
        raise BenchmarkError(
            f"category {path} rubric dimensions sum to {total_points}, must sum to 100"
        )
    verdicts = rubric_raw.get("verdicts")
    rubric = Rubric(dimensions=dict(dimensions), verdicts=frozenset(verdicts) if verdicts else None)

    items = [
        Item(id=raw["id"], prompt=raw["prompt"], meta=raw.get("meta", {}))
        for raw in items_raw.get("items", [])
    ]
    if not items:
        raise BenchmarkError(f"category {path} items.toml has no [[items]]")

    return Category(
        name=name,
        path=path,
        items=items,
        rubric=rubric,
        rubric_md=rubric_md_path.read_text(encoding="utf-8"),
    )


def list_categories(root: Path) -> list[Category]:
    root = Path(root)
    if not root.is_dir():
        return []
    return [
        load_category(child)
        for child in sorted(root.iterdir())
        if child.is_dir() and (child / "items.toml").is_file()
    ]
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `uv run pytest tests/benchmark/eval/test_category.py -v`
Expected: all PASS

- [ ] **Step 6: Lint and commit**

```bash
uv run ruff check src/modelman/benchmark/eval/ tests/benchmark/eval/
uv run ruff format src/modelman/benchmark/eval/ tests/benchmark/eval/
git add src/modelman/benchmark/eval/__init__.py src/modelman/benchmark/eval/category.py \
    tests/benchmark/eval/__init__.py tests/benchmark/eval/test_category.py \
    tests/benchmark/eval/fixtures/categories/ tests/benchmark/eval/fixtures/categories_invalid/
git commit -m "feat(modelman): add eval.category directory loader

completes plan item #3 of docs/superpowers/plans/2026-09-17-capability-eval-benchmark.md"
```

---

## Task 4: `eval/suite.py` — suite TOML, row expansion, route resolution

**Files:**
- Create: `src/modelman/benchmark/eval/suite.py`
- Test: `tests/benchmark/eval/test_suite.py`

**Interfaces:**
- Consumes: `modelman.registry.Registry`, `modelman.providers.lifecycle.BACKENDS`
- Produces: `DirectRouteConfig(base_url)`, `JudgeConfig(model, temperature, samples, max_attempts, route)`, `CodingConfig(dataset, limit)`, `RowConfig(label, model_id, route, provider_id, direct_model, categories, target_local_path, target_repo, draft_local_path, draft_repo, mtplx_model_name)`, `Suite(name, cooldown_s, judge, coding, routes_direct, rows)`, `LITELLM_PLIST`, `LIVE_PI_MODELS_PATH`, `OPENROUTER_BASE_URL`, `load_suite(path, registry) -> Suite`, `load_live_models(path) -> dict`, `openrouter_key(plist_path=LITELLM_PLIST) -> str | None`, `resolve_row_endpoint(row, model_name, routes_direct, live_models_path=LIVE_PI_MODELS_PATH, plist_path=LITELLM_PLIST) -> tuple[str, str, str]` (base_url, model_to_send, api_key), `preflight(suite, registry) -> None`.

- [ ] **Step 1: Write the failing tests**

```python
# tests/benchmark/eval/test_suite.py
"""Tests for modelman.benchmark.eval.suite — suite TOML parsing, row
expansion, and (model, route) -> endpoint resolution.
"""

import json
from pathlib import Path

import pytest

from modelman.benchmark.errors import BenchmarkError
from modelman.benchmark.eval.suite import (
    DirectRouteConfig,
    RowConfig,
    load_suite,
    preflight,
    resolve_row_endpoint,
)
from modelman.registry import ModelEntry, ProviderEntry, Registry


def _registry() -> Registry:
    return Registry(
        providers=[
            ProviderEntry(id="ollama", name="Ollama", location="local"),
            ProviderEntry(id="omlx", name="oMLX", location="local"),
        ],
        models=[
            ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a"),
            ModelEntry(id="omlx/b", family="f", provider_id="omlx", model_name="b-real-name"),
        ],
    )


def _write(tmp_path: Path, body: str) -> Path:
    path = tmp_path / "suite.toml"
    path.write_text(body, encoding="utf-8")
    return path


SUITE_BODY = """
name = "eval-test"
cooldown_s = 5

[judge]
model = "anthropic/claude-opus-5"
temperature = 0.0
samples = 1
max_attempts = 2
route = "openrouter"

[coding]
dataset = "humaneval"
limit = 5

[routes.direct.omlx]
base_url = "http://localhost:8000/v1"

[[rows]]
model = "ollama/a"
route = "litellm"

[[rows]]
model = "omlx/b"
route = "direct"
direct_model = "b-server-name"
categories = ["coding"]
"""


def test_load_suite_expands_rows_and_categories(tmp_path):
    suite = load_suite(_write(tmp_path, SUITE_BODY), _registry())
    assert suite.name == "eval-test"
    assert suite.coding.dataset == "humaneval"
    assert suite.coding.limit == 5
    assert len(suite.rows) == 2
    row_b = next(r for r in suite.rows if r.model_id == "omlx/b")
    assert row_b.categories == ["coding"]
    assert row_b.direct_model == "b-server-name"


def test_load_suite_rejects_unknown_model(tmp_path):
    body = SUITE_BODY.replace('model = "ollama/a"', 'model = "ollama/nope"')
    with pytest.raises(BenchmarkError, match="unknown model"):
        load_suite(_write(tmp_path, body), _registry())


def test_preflight_rejects_direct_row_missing_route_block(tmp_path):
    body = SUITE_BODY.replace("[routes.direct.omlx]\n", "").replace(
        'base_url = "http://localhost:8000/v1"\n', ""
    )
    suite = load_suite(_write(tmp_path, body), _registry())
    with pytest.raises(BenchmarkError, match="routes.direct.omlx"):
        preflight(suite, _registry())


def test_resolve_row_endpoint_direct_route_uses_direct_model_override():
    row = RowConfig(
        label="r", model_id="omlx/b", route="direct", provider_id="omlx", direct_model="server-name"
    )
    base_url, model, api_key = resolve_row_endpoint(
        row, "b-real-name", {"omlx": DirectRouteConfig(base_url="http://localhost:8000/v1")}
    )
    assert base_url == "http://localhost:8000/v1"
    assert model == "server-name"
    assert api_key == "ollama"


def test_resolve_row_endpoint_litellm_route_reads_live_models_json(tmp_path):
    live_path = tmp_path / "models.json"
    live_path.write_text(
        json.dumps(
            {"providers": {"litellm": {"baseUrl": "http://localhost:4000/v1", "apiKey": "sk-x"}}}
        ),
        encoding="utf-8",
    )
    row = RowConfig(label="r", model_id="ollama/a", route="litellm", provider_id="ollama")
    base_url, model, api_key = resolve_row_endpoint(row, "a", {}, live_models_path=live_path)
    assert base_url == "http://localhost:4000/v1"
    assert model == "ollama/a"
    assert api_key == "sk-x"
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `uv run pytest tests/benchmark/eval/test_suite.py -v`
Expected: `ModuleNotFoundError: No module named 'modelman.benchmark.eval.suite'`

- [ ] **Step 3: Write `suite.py`**

```python
# src/modelman/benchmark/eval/suite.py
"""Suite TOML parsing, row expansion, and (model, route) -> endpoint
resolution for `modelman benchmark eval`.

Deliberately parallel to (not shared with) modelman.benchmark.agent.suite:
eval rows carry no `thinking` (no pi driver here) and instead carry an
optional per-row `categories` override; the direct-route config needs no
`api` protocol discriminator because every eval transport speaks the same
OpenAI-compatible chat-completions wire format.
"""

from __future__ import annotations

import json
import os
import plistlib
import tomllib
from dataclasses import dataclass, field
from pathlib import Path

from modelman.benchmark.errors import BenchmarkError
from modelman.providers import lifecycle
from modelman.registry import Registry

LITELLM_PLIST = Path.home() / "Library" / "LaunchAgents" / "local.litellm.proxy.plist"
LIVE_PI_MODELS_PATH = Path.home() / ".pi" / "agent" / "models.json"
JUDGE_ROUTES = ("litellm", "openrouter")
ROW_ROUTES = ("direct", "litellm", "openrouter")
OPENROUTER_BASE_URL = "https://openrouter.ai/api/v1"
DEFAULT_CODING_DATASET = "humaneval"


@dataclass
class DirectRouteConfig:
    base_url: str


@dataclass
class JudgeConfig:
    model: str
    temperature: float
    samples: int
    max_attempts: int
    route: str


@dataclass
class CodingConfig:
    dataset: str | None = None
    limit: int | None = None


@dataclass
class RowConfig:
    label: str
    model_id: str
    route: str
    provider_id: str
    direct_model: str | None = None
    categories: list[str] | None = None
    target_local_path: str | None = None
    target_repo: str | None = None
    draft_local_path: str | None = None
    draft_repo: str | None = None
    mtplx_model_name: str | None = None


@dataclass
class Suite:
    name: str
    cooldown_s: float
    judge: JudgeConfig
    coding: CodingConfig
    routes_direct: dict[str, DirectRouteConfig] = field(default_factory=dict)
    rows: list[RowConfig] = field(default_factory=list)


def _short_model(model_id: str) -> str:
    return model_id.split("/")[-1]


def _provider_for(model_id: str, registry: Registry) -> str:
    try:
        return registry.model(model_id).provider_id
    except KeyError as exc:
        raise BenchmarkError(f"suite row references unknown model: {model_id}") from exc


def _expand_rows(raw_rows: list[dict], registry: Registry) -> list[RowConfig]:
    rows: list[RowConfig] = []
    for index, raw in enumerate(raw_rows, start=1):
        model_id = raw.get("model")
        route = raw.get("route")
        if not model_id or not route:
            missing = [k for k in ("model", "route") if not raw.get(k)]
            raise BenchmarkError(
                f"suite row {index} is missing required key(s): {', '.join(missing)}"
            )
        if route not in ROW_ROUTES:
            raise BenchmarkError(f"suite row {index} has unknown route: {route!r}")
        provider_id = raw.get("provider") or _provider_for(model_id, registry)
        model_entry = registry.model(model_id)
        label = raw.get("label") or f"{index:02d}--{_short_model(model_id)}--{route}"
        rows.append(
            RowConfig(
                label=label,
                model_id=model_id,
                route=route,
                provider_id=provider_id,
                direct_model=raw.get("direct_model"),
                categories=raw.get("categories"),
                target_local_path=model_entry.fetch.local_path if model_entry.fetch else None,
                target_repo=model_entry.fetch.repo if model_entry.fetch else None,
                draft_local_path=model_entry.draft.local_path if model_entry.draft else None,
                draft_repo=model_entry.draft.repo if model_entry.draft else None,
                mtplx_model_name=model_entry.model_name,
            )
        )
    return rows


def load_suite(path: Path, registry: Registry) -> Suite:
    path = Path(path)
    with path.open("rb") as f:
        raw = tomllib.load(f)

    judge_raw = raw["judge"]
    if judge_raw["route"] not in JUDGE_ROUTES:
        raise BenchmarkError(
            f"[judge] route must be one of {list(JUDGE_ROUTES)}, got {judge_raw['route']!r}"
        )
    judge = JudgeConfig(
        model=judge_raw["model"],
        temperature=judge_raw["temperature"],
        samples=judge_raw.get("samples", 1),
        max_attempts=judge_raw.get("max_attempts", 2),
        route=judge_raw["route"],
    )
    coding_raw = raw.get("coding", {})
    coding = CodingConfig(dataset=coding_raw.get("dataset"), limit=coding_raw.get("limit"))
    routes_direct = {
        provider_id: DirectRouteConfig(base_url=cfg["base_url"])
        for provider_id, cfg in raw.get("routes", {}).get("direct", {}).items()
    }
    return Suite(
        name=raw["name"],
        cooldown_s=raw.get("cooldown_s", 15.0),
        judge=judge,
        coding=coding,
        routes_direct=routes_direct,
        rows=_expand_rows(raw.get("rows", []), registry),
    )


def load_live_models(path: Path) -> dict:
    if not path.exists():
        return {}
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except json.JSONDecodeError:
        return {}


def openrouter_key(plist_path: Path = LITELLM_PLIST) -> str | None:
    env_key = os.environ.get("OPENROUTER_API_KEY")
    if env_key:
        return env_key
    if not plist_path.exists():
        return None
    try:
        with plist_path.open("rb") as f:
            data = plistlib.load(f)
    except Exception:
        return None
    key = data.get("EnvironmentVariables", {}).get("OPENROUTER_API_KEY")
    return str(key) if key else None


def resolve_row_endpoint(
    row: RowConfig,
    model_name: str,
    routes_direct: dict[str, DirectRouteConfig],
    live_models_path: Path = LIVE_PI_MODELS_PATH,
    plist_path: Path = LITELLM_PLIST,
) -> tuple[str, str, str]:
    """(base_url, model_to_send, api_key) for one eval row.

    litellm keys by the full registry model id (the LiteLLM model_list is
    keyed on it); direct keys by the backend's own bare model_name unless
    overridden by direct_model (needed for omlx, whose registry ids are
    org-prefixed while the server knows only the basename); openrouter
    strips any leading 'openrouter/' prefix, since OpenRouter itself does
    not know that prefix.
    """
    if row.route == "litellm":
        live = load_live_models(live_models_path)
        litellm_entry = live.get("providers", {}).get("litellm", {})
        api_key = litellm_entry.get("apiKey")
        if not api_key:
            raise BenchmarkError(
                "no LiteLLM apiKey found in ~/.pi/agent/models.json; launch a wt "
                "pi session in litellm mode at least once to seed it"
            )
        base_url = litellm_entry.get("baseUrl", "http://localhost:4000/v1")
        return base_url, row.model_id, api_key
    if row.route == "direct":
        direct_cfg = routes_direct.get(row.provider_id)
        if direct_cfg is None:
            raise BenchmarkError(
                f"row {row.label!r} uses route=direct for provider {row.provider_id!r} "
                f"but no [routes.direct.{row.provider_id}] block is configured"
            )
        return direct_cfg.base_url, (row.direct_model or model_name), "ollama"
    if row.route == "openrouter":
        key = openrouter_key(plist_path)
        if not key:
            raise BenchmarkError(
                f"row {row.label!r} uses route=openrouter but OPENROUTER_API_KEY "
                f"was not found (environment or {plist_path})"
            )
        model = row.model_id
        if model.startswith("openrouter/"):
            model = model[len("openrouter/") :]
        return OPENROUTER_BASE_URL, model, key
    raise BenchmarkError(f"row {row.label!r} has unknown route: {row.route!r}")


def preflight(suite: Suite, registry: Registry) -> None:
    unavailable = []
    for provider_id in dict.fromkeys(row.provider_id for row in suite.rows):
        backend = lifecycle.BACKENDS.get(provider_id)
        if backend is None:
            continue
        reason = backend.check_available()
        if reason is not None:
            unavailable.append(f"{provider_id}: {reason}")
    if unavailable:
        raise BenchmarkError(f"provider(s) unavailable: {'; '.join(unavailable)}")

    for row in suite.rows:
        if row.route == "direct" and row.provider_id not in suite.routes_direct:
            raise BenchmarkError(
                f"row {row.label!r} uses route=direct for provider {row.provider_id!r} "
                f"but no [routes.direct.{row.provider_id}] block is configured"
            )

    needs_openrouter = suite.judge.route == "openrouter" or any(
        row.route == "openrouter" for row in suite.rows
    )
    if needs_openrouter and openrouter_key() is None:
        raise BenchmarkError(
            f"OPENROUTER_API_KEY not found (environment or {LITELLM_PLIST}); "
            "needed for the judge and/or an openrouter row"
        )
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `uv run pytest tests/benchmark/eval/test_suite.py -v`
Expected: all PASS

- [ ] **Step 5: Lint and commit**

```bash
uv run ruff check src/modelman/benchmark/eval/suite.py tests/benchmark/eval/test_suite.py
uv run ruff format src/modelman/benchmark/eval/suite.py tests/benchmark/eval/test_suite.py
git add src/modelman/benchmark/eval/suite.py tests/benchmark/eval/test_suite.py
git commit -m "feat(modelman): add eval.suite TOML parsing and route resolution

completes plan item #4 of docs/superpowers/plans/2026-09-17-capability-eval-benchmark.md"
```

---

## Task 5: `eval/judged_runner.py` — single-turn generation + judge scoring

**Files:**
- Create: `src/modelman/benchmark/eval/judged_runner.py`
- Test: `tests/benchmark/eval/test_judged_runner.py`

**Interfaces:**
- Consumes: `eval.category.Category/Item`, `judge_core.JudgeTransport/judge_row/JudgeOutcome`
- Produces: `ItemResult(item_id, response_text, judge, score_100)`, `CategoryRowResult(category, items, score_100)`, `run_judged_category(category, row_transport, judge_transport, *, temperature, judge_temperature, judge_samples, judge_max_attempts) -> CategoryRowResult`.

- [ ] **Step 1: Write the failing tests**

```python
# tests/benchmark/eval/test_judged_runner.py
"""Tests for modelman.benchmark.eval.judged_runner — one generation call per
item, scored by the shared judge_core mechanics, aggregated to a /100
category score.
"""

import json

from modelman.benchmark.eval.category import Category, Item
from modelman.benchmark.eval.judged_runner import run_judged_category
from modelman.benchmark.judge_core import Rubric

RUBRIC = Rubric(dimensions={"bug_detection": 60, "actionability": 40})
CATEGORY = Category(
    name="mini_review",
    path=None,
    items=[
        Item(id="i1", prompt="review this", meta={"seeded_issue": "off by one"}),
        Item(id="i2", prompt="review that", meta={}),
    ],
    rubric=RUBRIC,
    rubric_md="score bug_detection (60) and actionability (40)",
)


class _FakeRowTransport:
    def __init__(self):
        self.prompts_seen: list[str] = []

    def complete(self, prompt: str, *, temperature: float) -> str:
        self.prompts_seen.append(prompt)
        return "There is an off-by-one error in the loop bound."


class _FakeJudgeTransport:
    def __init__(self, totals: list[int]):
        self.totals = list(totals)

    def complete(self, prompt: str, *, temperature: float) -> str:
        if not self.totals:
            return "not json"
        total = self.totals.pop(0)
        return json.dumps({"scores": {"bug_detection": total, "actionability": 0}, "total": total})


def test_run_judged_category_sends_item_prompt_verbatim_to_row_transport():
    row_transport = _FakeRowTransport()
    run_judged_category(
        CATEGORY,
        row_transport,
        _FakeJudgeTransport([60, 40]),
        temperature=0.0,
        judge_temperature=0.0,
        judge_samples=1,
        judge_max_attempts=1,
    )
    assert row_transport.prompts_seen == ["review this", "review that"]


def test_run_judged_category_scores_as_mean_of_item_totals():
    result = run_judged_category(
        CATEGORY,
        _FakeRowTransport(),
        _FakeJudgeTransport([60, 40]),
        temperature=0.0,
        judge_temperature=0.0,
        judge_samples=1,
        judge_max_attempts=1,
    )
    assert result.score_100 == 50.0
    assert len(result.items) == 2


def test_run_judged_category_ignores_judge_fail_items_in_the_mean():
    result = run_judged_category(
        CATEGORY,
        _FakeRowTransport(),
        _FakeJudgeTransport([80]),  # only one valid reply; item 2's judge call fails to parse
        temperature=0.0,
        judge_temperature=0.0,
        judge_samples=1,
        judge_max_attempts=1,
    )
    assert result.items[1].judge.status == "judge_fail"
    assert result.items[1].score_100 is None
    assert result.score_100 == 80.0
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `uv run pytest tests/benchmark/eval/test_judged_runner.py -v`
Expected: `ModuleNotFoundError: No module named 'modelman.benchmark.eval.judged_runner'`

- [ ] **Step 3: Write `judged_runner.py`**

```python
# src/modelman/benchmark/eval/judged_runner.py
"""Single-turn generation + judge scoring for the judged eval categories
(reasoning, planning, code_review, doc_summary).
"""

from __future__ import annotations

from dataclasses import dataclass
from statistics import mean

from modelman.benchmark.eval.category import Category, Item
from modelman.benchmark.judge_core import JudgeOutcome, JudgeTransport, judge_row


@dataclass
class ItemResult:
    item_id: str
    response_text: str
    judge: JudgeOutcome
    score_100: float | None


@dataclass
class CategoryRowResult:
    category: str
    items: list[ItemResult]
    score_100: float | None


def _build_judge_prompt(item: Item, category: Category, response_text: str) -> str:
    meta_note = item.meta.get("reference_notes") or item.meta.get("seeded_issue") or ""
    meta_section = (
        f"\n\n## Reference notes (never shown to the model)\n{meta_note}" if meta_note else ""
    )
    return (
        f"{category.rubric_md}\n\n"
        f"## Prompt given to the model\n{item.prompt}\n\n"
        f"## Model's response\n{response_text}{meta_section}\n\n"
        "Respond with strict JSON matching the schema above. No prose outside the JSON object."
    )


def run_judged_category(
    category: Category,
    row_transport: JudgeTransport,
    judge_transport: JudgeTransport,
    *,
    temperature: float,
    judge_temperature: float,
    judge_samples: int,
    judge_max_attempts: int,
) -> CategoryRowResult:
    """One generation call per item, then a judge call scoring it. A row's
    category score is the mean of its scored items' totals — every judged
    category's rubric sums to 100, so this is already a /100 value."""
    assert category.rubric is not None, "run_judged_category is not for the coding category"
    item_results: list[ItemResult] = []
    for item in category.items:
        response_text = row_transport.complete(item.prompt, temperature=temperature)
        judge_prompt = _build_judge_prompt(item, category, response_text)
        outcome = judge_row(
            judge_transport,
            judge_prompt,
            category.rubric,
            temperature=judge_temperature,
            samples=judge_samples,
            max_attempts=judge_max_attempts,
        )
        score = (
            float(outcome.combined.total)
            if outcome.status == "scored" and outcome.combined is not None
            else None
        )
        item_results.append(
            ItemResult(item_id=item.id, response_text=response_text, judge=outcome, score_100=score)
        )

    scored = [r.score_100 for r in item_results if r.score_100 is not None]
    category_score = round(mean(scored), 2) if scored else None
    return CategoryRowResult(category=category.name, items=item_results, score_100=category_score)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `uv run pytest tests/benchmark/eval/test_judged_runner.py -v`
Expected: all PASS

- [ ] **Step 5: Lint and commit**

```bash
uv run ruff check src/modelman/benchmark/eval/judged_runner.py tests/benchmark/eval/test_judged_runner.py
uv run ruff format src/modelman/benchmark/eval/judged_runner.py tests/benchmark/eval/test_judged_runner.py
git add src/modelman/benchmark/eval/judged_runner.py tests/benchmark/eval/test_judged_runner.py
git commit -m "feat(modelman): add eval.judged_runner (single-turn generation + judge)

completes plan item #5 of docs/superpowers/plans/2026-09-17-capability-eval-benchmark.md"
```

---

## Task 6: `eval/evalplus_runner.py` — EvalPlus subprocess wrapper for `coding`

**Files:**
- Create: `src/modelman/benchmark/eval/evalplus_runner.py`
- Test: `tests/benchmark/eval/test_evalplus_runner.py`

**Interfaces:**
- Produces: `CodingResult(dataset, pass_at_1, raw_output, error)`, `run_coding_category(*, base_url, model, api_key, dataset, limit, run_cmd=subprocess.run) -> CodingResult`.

- [ ] **Step 1: Write the failing tests**

```python
# tests/benchmark/eval/test_evalplus_runner.py
"""Tests for modelman.benchmark.eval.evalplus_runner — the subprocess
wrapper around EvalPlus for the `coding` category. No real EvalPlus
invocation here (see plan Task 11 for the live-verification step); `run_cmd`
is injected so these tests exercise only the command construction and
pass@1 parsing.
"""

from modelman.benchmark.eval.evalplus_runner import run_coding_category


class _FakeCompletedProcess:
    def __init__(self, returncode: int, stdout: str, stderr: str = ""):
        self.returncode = returncode
        self.stdout = stdout
        self.stderr = stderr


def test_run_coding_category_parses_pass_at_1_from_stdout():
    def fake_run(cmd, **kwargs):
        assert "--base-url" in cmd
        assert "http://localhost:8000/v1" in cmd
        assert "--model" in cmd
        assert "server-name" in cmd
        return _FakeCompletedProcess(0, "humaneval (base tests)\npass@1: 0.732\n")

    result = run_coding_category(
        base_url="http://localhost:8000/v1",
        model="server-name",
        api_key="ollama",
        dataset="humaneval",
        limit=5,
        run_cmd=fake_run,
    )
    assert result.pass_at_1 == 0.732
    assert result.error is None


def test_run_coding_category_reports_nonzero_exit_as_error():
    def fake_run(cmd, **kwargs):
        return _FakeCompletedProcess(1, "", "connection refused")

    result = run_coding_category(
        base_url="http://localhost:8000/v1",
        model="m",
        api_key="k",
        dataset="humaneval",
        limit=None,
        run_cmd=fake_run,
    )
    assert result.pass_at_1 is None
    assert "exited 1" in result.error


def test_run_coding_category_reports_unparseable_output_as_error():
    def fake_run(cmd, **kwargs):
        return _FakeCompletedProcess(0, "no pass rate printed here")

    result = run_coding_category(
        base_url="http://localhost:8000/v1",
        model="m",
        api_key="k",
        dataset="humaneval",
        limit=None,
        run_cmd=fake_run,
    )
    assert result.pass_at_1 is None
    assert "could not parse" in result.error
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `uv run pytest tests/benchmark/eval/test_evalplus_runner.py -v`
Expected: `ModuleNotFoundError: No module named 'modelman.benchmark.eval.evalplus_runner'`

- [ ] **Step 3: Write `evalplus_runner.py`**

```python
# src/modelman/benchmark/eval/evalplus_runner.py
"""Subprocess wrapper around EvalPlus for the `coding` eval category.

The exact CLI invocation here is the research doc's cited
`--backend openai --base-url` form, NOT yet verified against a real EvalPlus
install (see plan Task 11) — treat `_build_command`'s flags as the first
draft, and update them there (one place) once verified. EvalPlus itself does
both generation and grading and runs the generated code locally, same trust
model the agent benchmark already applies to a model's diff via gates.py.
"""

from __future__ import annotations

import re
import subprocess
from dataclasses import dataclass
from tempfile import TemporaryDirectory

_PASS_AT_1_RE = re.compile(r"pass@1[^\d]*([\d.]+)", re.IGNORECASE)


@dataclass
class CodingResult:
    dataset: str
    pass_at_1: float | None
    raw_output: str
    error: str | None = None


def _build_command(
    *, dataset: str, base_url: str, model: str, limit: int | None, workdir: str
) -> list[str]:
    cmd = [
        "uvx",
        "--from",
        "evalplus",
        "evalplus.evaluate",
        "--dataset",
        dataset,
        "--backend",
        "openai",
        "--base-url",
        base_url,
        "--model",
        model,
        "--root",
        workdir,
    ]
    if limit is not None:
        cmd += ["--n-samples", str(limit)]
    return cmd


def _parse_pass_at_1(stdout: str) -> float | None:
    match = _PASS_AT_1_RE.search(stdout)
    if not match:
        return None
    try:
        return float(match.group(1))
    except ValueError:
        return None


def run_coding_category(
    *,
    base_url: str,
    model: str,
    api_key: str,
    dataset: str,
    limit: int | None,
    run_cmd=subprocess.run,
) -> CodingResult:
    """Invoke EvalPlus against the row's resolved OpenAI-compatible endpoint
    and parse its pass@1 result. Runs in a scratch directory per call so
    concurrent/sequential rows never collide on EvalPlus's own output files.
    """
    with TemporaryDirectory(prefix="modelman-evalplus-") as tmp:
        cmd = _build_command(dataset=dataset, base_url=base_url, model=model, limit=limit, workdir=tmp)
        result = run_cmd(
            cmd,
            capture_output=True,
            text=True,
            timeout=1800,
            env={"OPENAI_API_KEY": api_key},
        )
        if result.returncode != 0:
            return CodingResult(
                dataset=dataset,
                pass_at_1=None,
                raw_output=result.stdout + result.stderr,
                error=f"evalplus exited {result.returncode}: {result.stderr[:300]}",
            )
        pass_at_1 = _parse_pass_at_1(result.stdout)
        if pass_at_1 is None:
            return CodingResult(
                dataset=dataset,
                pass_at_1=None,
                raw_output=result.stdout,
                error="could not parse pass@1 from evalplus output",
            )
        return CodingResult(dataset=dataset, pass_at_1=pass_at_1, raw_output=result.stdout)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `uv run pytest tests/benchmark/eval/test_evalplus_runner.py -v`
Expected: all PASS

- [ ] **Step 5: Lint and commit**

```bash
uv run ruff check src/modelman/benchmark/eval/evalplus_runner.py tests/benchmark/eval/test_evalplus_runner.py
uv run ruff format src/modelman/benchmark/eval/evalplus_runner.py tests/benchmark/eval/test_evalplus_runner.py
git add src/modelman/benchmark/eval/evalplus_runner.py tests/benchmark/eval/test_evalplus_runner.py
git commit -m "feat(modelman): add eval.evalplus_runner subprocess wrapper

Command/parsing not yet verified against a real EvalPlus install (Task 11).
completes plan item #6 of docs/superpowers/plans/2026-09-17-capability-eval-benchmark.md"
```

---

## Task 7: `eval/runner.py` — suite orchestration (isolation, dispatch, restore)

**Files:**
- Create: `src/modelman/benchmark/eval/runner.py`
- Test: `tests/benchmark/eval/test_runner.py`

**Interfaces:**
- Consumes: `isolation.isolate_provider/restore_providers/mlx_lm_server_pairing_args/SUPPORTED_PROVIDER_IDS`, `eval.suite.{Suite,RowConfig,preflight,resolve_row_endpoint,load_live_models,openrouter_key,LITELLM_PLIST,LIVE_PI_MODELS_PATH,OPENROUTER_BASE_URL}`, `eval.category.{Category,CODING_CATEGORY}`, `eval.judged_runner.run_judged_category`, `eval.evalplus_runner.run_coding_category`, `judge_core.LiteLLMJudgeTransport`
- Produces: `RowRunResult(row, row_dir, category_results, error)`, `DEFAULT_RESULTS_DIR`, `RunSavedButRestoreFailed`, `run_suite(suite, registry, categories, *, row_filter=None, results_dir=None, judge_transport_factory=None) -> tuple[Path, list[RowRunResult]]`. `judge_transport_factory`, if given, is called with a single `JudgeConfig` argument (not the full `Suite`) — Task 9's `rejudge_run` reuses the same default factory with a `JudgeConfig` reconstructed from a persisted `run.toml`, where no `Suite` exists.

- [ ] **Step 1: Write the failing tests**

```python
# tests/benchmark/eval/test_runner.py
"""Tests for modelman.benchmark.eval.runner — provider-grouped isolation,
per-row category dispatch (judged vs. EvalPlus), and restore-on-finally.

Isolation and generation are faked throughout: these tests pin the
orchestration logic (grouping, dispatch, restore-failure handling), not real
network/process calls.
"""

from pathlib import Path
from unittest.mock import patch

import pytest

from modelman.benchmark.errors import BenchmarkError
from modelman.benchmark.eval.category import Category, Item
from modelman.benchmark.eval.runner import RunSavedButRestoreFailed, run_suite
from modelman.benchmark.eval.suite import CodingConfig, JudgeConfig, RowConfig, Suite
from modelman.benchmark.judge_core import JudgeOutcome, JudgeScore, Rubric
from modelman.registry import ModelEntry, ProviderEntry, Registry


def _registry() -> Registry:
    return Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", location="local")],
        models=[ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a")],
    )


def _suite(row_categories=None) -> Suite:
    return Suite(
        name="t",
        cooldown_s=0,
        judge=JudgeConfig(model="j", temperature=0.0, samples=1, max_attempts=1, route="litellm"),
        coding=CodingConfig(),
        rows=[
            RowConfig(
                label="row1", model_id="ollama/a", route="litellm", provider_id="ollama",
                categories=row_categories,
            )
        ],
    )


def _mini_review_category() -> Category:
    return Category(
        name="mini_review",
        path=Path("."),
        items=[Item(id="i1", prompt="p", meta={})],
        rubric=Rubric(dimensions={"a": 100}),
        rubric_md="score a (100)",
    )


@patch("modelman.benchmark.eval.runner.judged_runner.run_judged_category")
@patch("modelman.benchmark.eval.runner.resolve_row_endpoint")
@patch("modelman.benchmark.eval.runner.isolation")
def test_run_suite_isolates_once_per_provider_group_and_restores(
    mock_isolation, mock_resolve, mock_run_judged, tmp_path
):
    # ISOLATABLE_PROVIDERS is bound at runner.py import time from the real
    # lifecycle module, not from this mock, so it already contains "ollama"
    # (a real local provider) — nothing to set on mock_isolation for that.
    mock_resolve.return_value = ("http://localhost:4000/v1", "ollama/a", "sk-x")
    mock_run_judged.return_value = _fake_category_result(50.0)

    run_dir, results = run_suite(
        _suite(),
        _registry(),
        [_mini_review_category()],
        results_dir=tmp_path,
        judge_transport_factory=lambda suite: object(),
    )

    mock_isolation.isolate_provider.assert_called_once_with("ollama")
    mock_isolation.restore_providers.assert_called_once()
    assert len(results) == 1
    assert results[0].category_results["mini_review"].score_100 == 50.0
    assert run_dir.is_dir()


@patch("modelman.benchmark.eval.runner.judged_runner.run_judged_category")
@patch("modelman.benchmark.eval.runner.resolve_row_endpoint")
@patch("modelman.benchmark.eval.runner.isolation")
def test_run_suite_row_categories_override_narrows_dispatch(
    mock_isolation, mock_resolve, mock_run_judged, tmp_path
):
    mock_resolve.return_value = ("http://localhost:4000/v1", "ollama/a", "sk-x")
    mock_run_judged.return_value = _fake_category_result(50.0)

    other_category = Category(
        name="other", path=Path("."), items=[], rubric=Rubric(dimensions={"a": 100}), rubric_md="x"
    )
    _, results = run_suite(
        _suite(row_categories=["mini_review"]),
        _registry(),
        [_mini_review_category(), other_category],
        results_dir=tmp_path,
        judge_transport_factory=lambda suite: object(),
    )
    assert set(results[0].category_results) == {"mini_review"}


@patch("modelman.benchmark.eval.runner.judged_runner.run_judged_category")
@patch("modelman.benchmark.eval.runner.resolve_row_endpoint")
@patch("modelman.benchmark.eval.runner.isolation")
def test_run_suite_raises_run_saved_but_restore_failed_on_restore_error(
    mock_isolation, mock_resolve, mock_run_judged, tmp_path
):
    mock_isolation.restore_providers.side_effect = BenchmarkError("wedged")
    mock_resolve.return_value = ("http://localhost:4000/v1", "ollama/a", "sk-x")
    mock_run_judged.return_value = _fake_category_result(50.0)

    with pytest.raises(RunSavedButRestoreFailed) as exc_info:
        run_suite(
            _suite(),
            _registry(),
            [_mini_review_category()],
            results_dir=tmp_path,
            judge_transport_factory=lambda suite: object(),
        )
    assert exc_info.value.run_dir.is_dir()
    assert len(exc_info.value.results) == 1


def _fake_category_result(score: float):
    from modelman.benchmark.eval.judged_runner import CategoryRowResult, ItemResult

    outcome = JudgeOutcome(
        status="scored",
        samples=[],
        combined=JudgeScore(scores={"a": int(score)}, total=int(score), verdict="", flags=[], rationale="", raw_text=""),
        attempts_used=1,
    )
    return CategoryRowResult(
        category="mini_review",
        items=[ItemResult(item_id="i1", response_text="r", judge=outcome, score_100=score)],
        score_100=score,
    )
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `uv run pytest tests/benchmark/eval/test_runner.py -v`
Expected: `ModuleNotFoundError: No module named 'modelman.benchmark.eval.runner'`

- [ ] **Step 3: Write `runner.py`**

```python
# src/modelman/benchmark/eval/runner.py
"""Suite orchestration for `modelman benchmark eval`: group rows by
provider, isolate once per group, run every row's categories, restore.

Mirrors modelman.benchmark.agent.runner's isolation-grouping loop (same
(provider, extra_args) keying for mlx_lm_server's per-pairing re-isolation)
but with no pi process, no workspace, and no gates — each row's "run" is a
handful of direct HTTP calls per category instead of a full agent session.
"""

from __future__ import annotations

import itertools
import subprocess
from dataclasses import dataclass, field
from datetime import UTC, datetime
from pathlib import Path

from modelman.benchmark import isolation
from modelman.benchmark.errors import BenchmarkError
from modelman.benchmark.eval import evalplus_runner, judged_runner, report
from modelman.benchmark.eval.category import CODING_CATEGORY, Category
from modelman.benchmark.eval.suite import (
    LITELLM_PLIST,
    LIVE_PI_MODELS_PATH,
    OPENROUTER_BASE_URL,
    RowConfig,
    Suite,
    load_live_models,
    openrouter_key,
    preflight,
    resolve_row_endpoint,
)
from modelman.benchmark.judge_core import JudgeTransport, LiteLLMJudgeTransport
from modelman.registry import Registry

DEFAULT_RESULTS_DIR = Path.home() / ".config" / "local-ai" / "benchmarks"
ISOLATABLE_PROVIDERS = isolation.SUPPORTED_PROVIDER_IDS
DEFAULT_CODING_DATASET = "humaneval"


@dataclass
class RowRunResult:
    row: RowConfig
    row_dir: Path
    category_results: dict[str, object] = field(default_factory=dict)
    error: str | None = None


class RunSavedButRestoreFailed(BenchmarkError):
    """Every row completed and is on disk; only putting the backends back
    failed. Carries run_dir/results so the CLI can still record --latest."""

    def __init__(self, message: str, *, run_dir: Path, results: list[RowRunResult]) -> None:
        super().__init__(message)
        self.run_dir = run_dir
        self.results = results


def _row_dir(run_dir: Path, index: int, row: RowConfig) -> Path:
    return run_dir / f"{index:02d}--{row.label}"


def _default_judge_transport_factory(judge_cfg) -> JudgeTransport:
    """Takes a JudgeConfig (not a full Suite) so both run_suite (passes
    suite.judge) and rejudge_run (passes a JudgeConfig reconstructed from a
    persisted run.toml, with no Suite in hand) can share this."""
    if judge_cfg.route == "openrouter":
        key = openrouter_key(LITELLM_PLIST)
        if not key:
            raise BenchmarkError("judge route=openrouter needs OPENROUTER_API_KEY")
        model = judge_cfg.model
        if model.startswith("openrouter/"):
            model = model[len("openrouter/") :]
        return LiteLLMJudgeTransport(base_url=OPENROUTER_BASE_URL, api_key=key, model=model)
    live = load_live_models(LIVE_PI_MODELS_PATH)
    litellm_entry = live.get("providers", {}).get("litellm", {})
    api_key = litellm_entry.get("apiKey")
    if not api_key:
        raise BenchmarkError(
            "no LiteLLM apiKey found in ~/.pi/agent/models.json for the judge transport"
        )
    base_url = litellm_entry.get("baseUrl", "http://localhost:4000/v1")
    return LiteLLMJudgeTransport(base_url=base_url, api_key=api_key, model=judge_cfg.model)


def _git_sha() -> str:
    try:
        result = subprocess.run(
            ["git", "rev-parse", "HEAD"], capture_output=True, text=True, check=False
        )
        return result.stdout.strip() or "unknown"
    except OSError:
        return "unknown"


def _row_categories(row: RowConfig, categories: list[Category]) -> list[Category]:
    if row.categories is None:
        return categories
    wanted = set(row.categories)
    return [c for c in categories if c.name in wanted]


def _isolation_extra_args(row: RowConfig) -> tuple[str, ...]:
    if row.provider_id == "mlx_lm_server":
        return isolation.mlx_lm_server_pairing_args(
            row.model_id, row.target_local_path, row.target_repo, row.draft_local_path, row.draft_repo
        )
    if row.provider_id == "mtplx":
        assert row.mtplx_model_name is not None
        return (row.mtplx_model_name,)
    return ()


def _select_rows(rows: list[RowConfig], row_filter: list[str] | None) -> list[RowConfig]:
    if not row_filter:
        return list(rows)
    wanted = set(row_filter)
    return [r for i, r in enumerate(rows, start=1) if r.label in wanted or str(i) in wanted]


def _run_row(
    row: RowConfig,
    categories: list[Category],
    suite: Suite,
    registry: Registry,
    judge_transport: JudgeTransport,
) -> dict[str, object]:
    model = registry.model(row.model_id)
    base_url, model_name, api_key = resolve_row_endpoint(row, model.model_name, suite.routes_direct)
    row_transport = LiteLLMJudgeTransport(base_url=base_url, api_key=api_key, model=model_name)

    results: dict[str, object] = {}
    for category in _row_categories(row, categories):
        if category.name == CODING_CATEGORY:
            dataset = suite.coding.dataset or category.coding_dataset or DEFAULT_CODING_DATASET
            limit = suite.coding.limit if suite.coding.limit is not None else category.coding_limit
            results[category.name] = evalplus_runner.run_coding_category(
                base_url=base_url, model=model_name, api_key=api_key, dataset=dataset, limit=limit
            )
        else:
            results[category.name] = judged_runner.run_judged_category(
                category,
                row_transport,
                judge_transport,
                temperature=0.0,
                judge_temperature=suite.judge.temperature,
                judge_samples=suite.judge.samples,
                judge_max_attempts=suite.judge.max_attempts,
            )
    return results


def run_suite(
    suite: Suite,
    registry: Registry,
    categories: list[Category],
    *,
    row_filter: list[str] | None = None,
    results_dir: Path | None = None,
    judge_transport_factory=None,
) -> tuple[Path, list[RowRunResult]]:
    preflight(suite, registry)
    rows = _select_rows(suite.rows, row_filter)

    results_dir = results_dir or DEFAULT_RESULTS_DIR
    run_id = "eval-" + datetime.now(UTC).strftime("%Y%m%d-%H%M%S")
    run_dir = results_dir / run_id
    run_dir.mkdir(parents=True, exist_ok=True)

    judge_transport = (judge_transport_factory or _default_judge_transport_factory)(suite.judge)

    results: list[RowRunResult] = []
    index = 0
    isolated_any = False
    for provider_id, group in itertools.groupby(
        sorted(rows, key=lambda r: (r.provider_id, r.model_id)), key=lambda r: r.provider_id
    ):
        prev_extra: tuple[str, ...] | None = None
        for row in group:
            try:
                extra_args = _isolation_extra_args(row)
            except BenchmarkError as exc:
                index += 1
                results.append(RowRunResult(row=row, row_dir=_row_dir(run_dir, index, row), error=str(exc)))
                continue

            if provider_id in ISOLATABLE_PROVIDERS and extra_args != prev_extra:
                try:
                    isolation.isolate_provider(provider_id, *extra_args)
                    isolated_any = True
                except BenchmarkError as exc2:
                    index += 1
                    results.append(
                        RowRunResult(row=row, row_dir=_row_dir(run_dir, index, row), error=str(exc2))
                    )
                    continue
                prev_extra = extra_args

            index += 1
            row_dir = _row_dir(run_dir, index, row)
            try:
                category_results = _run_row(row, categories, suite, registry, judge_transport)
                results.append(RowRunResult(row=row, row_dir=row_dir, category_results=category_results))
            except BenchmarkError as exc3:
                results.append(RowRunResult(row=row, row_dir=row_dir, error=str(exc3)))

    restore_error: str | None = None
    if isolated_any:
        try:
            isolation.restore_providers()
        except BenchmarkError as exc:
            restore_error = str(exc)

    for result in results:
        report.write_row_artifacts(result)
    (run_dir / "summary.md").write_text(
        report.render_summary(run_id, results, registry, [c.name for c in categories]),
        encoding="utf-8",
    )
    report.write_metrics_jsonl(run_dir / "metrics.jsonl", results)
    report.write_run_toml(run_dir / "run.toml", suite, git_sha=_git_sha())

    if restore_error is not None:
        raise RunSavedButRestoreFailed(
            f"providers failed to restore after the run (all results were saved to {run_dir}): "
            f"{restore_error}",
            run_dir=run_dir,
            results=results,
        )
    return run_dir, results
```

Note: `run_suite` calls `report.write_row_artifacts`/`report.render_summary`/`report.write_metrics_jsonl`, which Task 8 creates — this task's tests patch `judged_runner`/`resolve_row_endpoint`/`isolation` but let `report` run for real against `tmp_path`, so Task 8 must land before this task's tests can pass. Do Task 8's module first if working strictly test-by-test; the plan orders them 7-then-8 for narrative clarity, but implement `report.py`'s four functions (even as this task's own throwaway stubs) before running Task 7's tests, then let Task 8 replace the stubs with the real implementation.

- [ ] **Step 3b: Add minimal `report.py` stubs so Task 7's tests can import it**

```python
# src/modelman/benchmark/eval/report.py (stub — replaced fully in Task 8)
from __future__ import annotations

from pathlib import Path


def write_row_artifacts(result) -> None:
    result.row_dir.mkdir(parents=True, exist_ok=True)


def render_summary(run_id, results, registry, category_names) -> str:
    return f"# eval run {run_id}\n"


def write_metrics_jsonl(path: Path, results) -> None:
    path.write_text("", encoding="utf-8")


def write_run_toml(path: Path, suite, *, git_sha: str) -> None:
    path.write_text("", encoding="utf-8")
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `uv run pytest tests/benchmark/eval/test_runner.py -v`
Expected: all PASS

- [ ] **Step 5: Lint and commit**

```bash
uv run ruff check src/modelman/benchmark/eval/runner.py src/modelman/benchmark/eval/report.py tests/benchmark/eval/test_runner.py
uv run ruff format src/modelman/benchmark/eval/runner.py src/modelman/benchmark/eval/report.py tests/benchmark/eval/test_runner.py
git add src/modelman/benchmark/eval/runner.py src/modelman/benchmark/eval/report.py tests/benchmark/eval/test_runner.py
git commit -m "feat(modelman): add eval.runner suite orchestration

report.py lands here as a stub; Task 8 replaces it with the real summary.md
renderer. completes plan item #7 of docs/superpowers/plans/2026-09-17-capability-eval-benchmark.md"
```

---

## Task 8: `eval/report.py` — capability matrix, leaderboard, anomalies

**Files:**
- Modify: `src/modelman/benchmark/eval/report.py` (replace the Task 7 stub)
- Test: `tests/benchmark/eval/test_report.py`

**Interfaces:**
- Consumes: `eval.runner.RowRunResult`, `eval.evalplus_runner.CodingResult`, `eval.judged_runner.CategoryRowResult`, `modelman.registry.Registry`
- Produces: `write_row_artifacts(result) -> None`, `render_summary(run_id, results, registry, category_names) -> str`, `write_metrics_jsonl(path, results) -> None`, `write_run_toml(path, suite, *, git_sha) -> None` (the suite snapshot Task 9's `rejudge_run` reads back to reconstruct a judge transport with no `Suite` in hand).

- [ ] **Step 1: Write the failing tests**

```python
# tests/benchmark/eval/test_report.py
"""Tests for modelman.benchmark.eval.report — the capability matrix (grouped
by registry family), per-category leaderboard, and anomalies table.
"""

import json
from pathlib import Path

from modelman.benchmark.eval.evalplus_runner import CodingResult
from modelman.benchmark.eval.judged_runner import CategoryRowResult, ItemResult
from modelman.benchmark.eval.report import render_summary, write_metrics_jsonl, write_row_artifacts
from modelman.benchmark.eval.runner import RowRunResult
from modelman.benchmark.eval.suite import RowConfig
from modelman.benchmark.judge_core import JudgeOutcome, JudgeScore
from modelman.registry import ModelEntry, ProviderEntry, Registry


def _registry() -> Registry:
    return Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", location="local")],
        models=[
            ModelEntry(id="ollama/qwen-27b", family="qwen3.8", provider_id="ollama", model_name="qwen-27b"),
            ModelEntry(id="ollama/other", family="other", provider_id="ollama", model_name="other"),
        ],
    )


def _judged_result(score) -> CategoryRowResult:
    outcome = JudgeOutcome(
        status="scored" if score is not None else "judge_fail",
        samples=[],
        combined=(
            JudgeScore(scores={"a": int(score)}, total=int(score), verdict="", flags=[], rationale="", raw_text="")
            if score is not None
            else None
        ),
        attempts_used=1,
    )
    return CategoryRowResult(
        category="doc_summary",
        items=[ItemResult(item_id="i1", response_text="r", judge=outcome, score_100=score)],
        score_100=score,
    )


def test_render_summary_groups_capability_matrix_by_family():
    row_a = RowRunResult(
        row=RowConfig(label="a", model_id="ollama/qwen-27b", route="litellm", provider_id="ollama"),
        row_dir=Path("/tmp/a"),
        category_results={"doc_summary": _judged_result(80.0), "coding": CodingResult(dataset="humaneval", pass_at_1=0.5, raw_output="")},
    )
    row_b = RowRunResult(
        row=RowConfig(label="b", model_id="ollama/other", route="litellm", provider_id="ollama"),
        row_dir=Path("/tmp/b"),
        category_results={"doc_summary": _judged_result(60.0)},
    )
    summary = render_summary("test-run", [row_a, row_b], _registry(), ["doc_summary", "coding"])
    assert "qwen3.8" in summary
    assert "80.0" in summary
    assert "50.0%" in summary  # 0.5 pass@1 rendered as a percentage


def test_render_summary_marks_judge_fail_rows():
    row = RowRunResult(
        row=RowConfig(label="a", model_id="ollama/qwen-27b", route="litellm", provider_id="ollama"),
        row_dir=Path("/tmp/a"),
        category_results={"doc_summary": _judged_result(None)},
    )
    summary = render_summary("test-run", [row], _registry(), ["doc_summary"])
    assert "JUDGE_FAIL" in summary


def test_render_summary_reports_isolation_errors():
    row = RowRunResult(
        row=RowConfig(label="a", model_id="ollama/qwen-27b", route="litellm", provider_id="ollama"),
        row_dir=Path("/tmp/a"),
        category_results={},
        error="isolation failed for ollama: not found",
    )
    summary = render_summary("test-run", [row], _registry(), ["doc_summary"])
    assert "ISOLATION_ERROR" in summary


def test_write_row_artifacts_writes_response_and_judge_json(tmp_path):
    row = RowRunResult(
        row=RowConfig(label="a", model_id="ollama/qwen-27b", route="litellm", provider_id="ollama"),
        row_dir=tmp_path / "01--a",
        category_results={"doc_summary": _judged_result(80.0)},
    )
    write_row_artifacts(row)
    item_dir = tmp_path / "01--a" / "doc_summary" / "i1"
    assert (item_dir / "response.txt").read_text() == "r"
    judge_data = json.loads((item_dir / "judge.json").read_text())
    assert judge_data["status"] == "scored"


def test_write_run_toml_persists_judge_config_for_rejudging(tmp_path):
    from modelman.benchmark.eval.suite import CodingConfig, JudgeConfig, Suite

    suite = Suite(
        name="t",
        cooldown_s=15.0,
        judge=JudgeConfig(model="j", temperature=0.1, samples=2, max_attempts=2, route="litellm"),
        coding=CodingConfig(dataset="humaneval", limit=10),
        rows=[],
    )
    from modelman.benchmark.eval.report import write_run_toml

    out_path = tmp_path / "run.toml"
    write_run_toml(out_path, suite, git_sha="abc123")
    import tomllib

    with out_path.open("rb") as f:
        data = tomllib.load(f)
    assert data["run"]["git_sha"] == "abc123"
    assert data["suite"]["judge"]["model"] == "j"
    assert data["suite"]["judge"]["samples"] == 2
    assert data["suite"]["coding"]["dataset"] == "humaneval"


def test_write_metrics_jsonl_writes_one_line_per_row(tmp_path):
    row = RowRunResult(
        row=RowConfig(label="a", model_id="ollama/qwen-27b", route="litellm", provider_id="ollama"),
        row_dir=tmp_path / "01--a",
        category_results={"doc_summary": _judged_result(80.0)},
    )
    out_path = tmp_path / "metrics.jsonl"
    write_metrics_jsonl(out_path, [row])
    lines = out_path.read_text().splitlines()
    assert len(lines) == 1
    record = json.loads(lines[0])
    assert record["label"] == "a"
    assert record["categories"]["doc_summary"] == 80.0
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `uv run pytest tests/benchmark/eval/test_report.py -v`
Expected: FAIL — stub `render_summary`/`write_row_artifacts`/`write_metrics_jsonl` don't produce the expected content

- [ ] **Step 3: Replace the `report.py` stub with the real implementation**

```python
# src/modelman/benchmark/eval/report.py
"""summary.md rendering and per-row artifact writes for
`modelman benchmark eval` runs."""

from __future__ import annotations

import json
from dataclasses import asdict
from pathlib import Path

from modelman._toml_io import atomic_write_toml
from modelman.benchmark.eval.evalplus_runner import CodingResult
from modelman.benchmark.eval.judged_runner import CategoryRowResult
from modelman.benchmark.eval.suite import Suite
from modelman.registry import Registry


def _family_of(model_id: str, registry: Registry) -> str:
    try:
        return registry.model(model_id).family
    except KeyError:
        return model_id


def _cell(value: object) -> str:
    if value is None:
        return "N/A"
    if isinstance(value, CodingResult):
        if value.pass_at_1 is not None:
            return f"{value.pass_at_1 * 100:.1f}%"
        return "JUDGE_FAIL" if value.error else "N/A"
    if isinstance(value, CategoryRowResult):
        return f"{value.score_100:.1f}" if value.score_100 is not None else "JUDGE_FAIL"
    return "N/A"


def _capability_matrix(results: list, registry: Registry, category_names: list[str]) -> str:
    header = "| family | row | route | " + " | ".join(category_names) + " |"
    sep = "|---|---|---|" + "---|" * len(category_names)
    lines = [header, sep]
    for r in sorted(results, key=lambda r: (_family_of(r.row.model_id, registry), r.row.label)):
        family = _family_of(r.row.model_id, registry)
        if r.error:
            cells = ["ISOLATION_ERROR"] * len(category_names)
        else:
            cells = [_cell(r.category_results.get(name)) for name in category_names]
        lines.append(f"| {family} | {r.row.label} | {r.row.route} | " + " | ".join(cells) + " |")
    return "\n".join(lines)


def _score_of(result, category_name: str) -> float | None:
    value = result.category_results.get(category_name)
    if isinstance(value, CodingResult):
        return value.pass_at_1 * 100 if value.pass_at_1 is not None else None
    if isinstance(value, CategoryRowResult):
        return value.score_100
    return None


def _leaderboard(results: list, category_names: list[str]) -> str:
    lines = ["| category | rank | row | score |", "|---|---|---|---|"]
    for name in category_names:
        scored = sorted(
            ((r, _score_of(r, name)) for r in results if _score_of(r, name) is not None),
            key=lambda pair: -pair[1],
        )
        for rank, (r, score) in enumerate(scored[:3], start=1):
            lines.append(f"| {name} | {rank} | {r.row.label} | {score:.1f} |")
        if not scored:
            lines.append(f"| {name} | — | — | no scored rows |")
    return "\n".join(lines)


def _anomalies(results: list, category_names: list[str]) -> str:
    lines = ["| label | anomaly |", "|---|---|"]
    for r in results:
        if r.error:
            lines.append(f"| {r.row.label} | ISOLATION_ERROR: {r.error[:200]} |")
            continue
        for name in category_names:
            value = r.category_results.get(name)
            if isinstance(value, CategoryRowResult):
                for item in value.items:
                    if item.judge.status == "judge_fail":
                        lines.append(f"| {r.row.label} | JUDGE_FAIL ({name}/{item.item_id}) |")
            if isinstance(value, CodingResult) and value.error:
                lines.append(f"| {r.row.label} | evalplus error ({name}): {value.error[:150]} |")
    if len(lines) == 2:
        lines.append("| — | none |")
    return "\n".join(lines)


def render_summary(run_id: str, results: list, registry: Registry, category_names: list[str]) -> str:
    return (
        f"# Eval benchmark run {run_id}\n\n"
        f"## Capability matrix\n\n{_capability_matrix(results, registry, category_names)}\n\n"
        f"## Per-category leaderboard\n\n{_leaderboard(results, category_names)}\n\n"
        f"## Anomalies\n\n{_anomalies(results, category_names)}\n"
    )


def write_row_artifacts(result) -> None:
    result.row_dir.mkdir(parents=True, exist_ok=True)
    if result.error is not None:
        (result.row_dir / "error.txt").write_text(result.error, encoding="utf-8")
        return
    for name, cat_result in result.category_results.items():
        cat_dir = result.row_dir / name
        cat_dir.mkdir(parents=True, exist_ok=True)
        if isinstance(cat_result, CodingResult):
            (cat_dir / "evalplus_result.json").write_text(
                json.dumps(asdict(cat_result), indent=2), encoding="utf-8"
            )
        elif isinstance(cat_result, CategoryRowResult):
            for item_result in cat_result.items:
                item_dir = cat_dir / item_result.item_id
                item_dir.mkdir(parents=True, exist_ok=True)
                (item_dir / "response.txt").write_text(item_result.response_text, encoding="utf-8")
                (item_dir / "judge.json").write_text(
                    json.dumps(asdict(item_result.judge), indent=2), encoding="utf-8"
                )
            (cat_dir / "score.json").write_text(
                json.dumps({"score_100": cat_result.score_100}), encoding="utf-8"
            )


def write_run_toml(path: Path, suite: Suite, *, git_sha: str) -> None:
    payload = {
        "run": {"git_sha": git_sha},
        "suite": {
            "name": suite.name,
            "judge": {
                "model": suite.judge.model,
                "temperature": suite.judge.temperature,
                "samples": suite.judge.samples,
                "max_attempts": suite.judge.max_attempts,
                "route": suite.judge.route,
            },
            # TOML has no null literal — omit an unset [coding] override
            # entirely rather than writing dataset/limit as None (tomli-w
            # cannot serialize None).
            "coding": {
                k: v
                for k, v in {"dataset": suite.coding.dataset, "limit": suite.coding.limit}.items()
                if v is not None
            },
        },
    }
    atomic_write_toml(payload, path)


def write_metrics_jsonl(path: Path, results: list) -> None:
    with path.open("w", encoding="utf-8") as f:
        for r in results:
            categories = {}
            for name, value in r.category_results.items():
                if isinstance(value, CodingResult):
                    categories[name] = value.pass_at_1 * 100 if value.pass_at_1 is not None else None
                elif isinstance(value, CategoryRowResult):
                    categories[name] = value.score_100
            f.write(
                json.dumps(
                    {
                        "label": r.row.label,
                        "model_id": r.row.model_id,
                        "route": r.row.route,
                        "error": r.error,
                        "categories": categories,
                    }
                )
                + "\n"
            )
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `uv run pytest tests/benchmark/eval/test_report.py -v`
Expected: all PASS

- [ ] **Step 5: Re-run Task 7's tests to confirm the real `report.py` didn't break orchestration**

Run: `uv run pytest tests/benchmark/eval/test_runner.py tests/benchmark/eval/test_report.py -v`
Expected: all PASS

- [ ] **Step 6: Lint and commit**

```bash
uv run ruff check src/modelman/benchmark/eval/report.py tests/benchmark/eval/test_report.py
uv run ruff format src/modelman/benchmark/eval/report.py tests/benchmark/eval/test_report.py
git add src/modelman/benchmark/eval/report.py tests/benchmark/eval/test_report.py
git commit -m "feat(modelman): implement eval.report summary.md rendering

Replaces the Task 7 stub with the real capability matrix / leaderboard /
anomalies renderer. completes plan item #8 of docs/superpowers/plans/2026-09-17-capability-eval-benchmark.md"
```

---

## Task 9: `eval/runner.py` — `rejudge_run` for cheap re-scoring

**Files:**
- Modify: `src/modelman/benchmark/eval/runner.py`
- Test: `tests/benchmark/eval/test_rejudge.py`

**Interfaces:**
- Produces: `rejudge_run(run_dir, categories, *, row_filter=None, samples_override=None, judge_transport_factory=None) -> list[dict]` — re-scores every judged-category item from its persisted `response.txt`, skipping `coding` (nothing to re-judge; EvalPlus's pass@1 is deterministic). Reads the judge config back from the run's persisted `run.toml` (Task 8's `write_run_toml`) — there is no `Suite` in hand at rejudge time, only what the original run wrote to disk.

- [ ] **Step 1: Write the failing test**

```python
# tests/benchmark/eval/test_rejudge.py
"""Tests for eval.runner.rejudge_run — re-scoring persisted responses
without regenerating them, using the judge config persisted in run.toml."""

import json
from pathlib import Path

from modelman.benchmark.eval.category import Category, Item
from modelman.benchmark.eval.runner import rejudge_run
from modelman.benchmark.judge_core import Rubric


def _seed_run(tmp_path: Path) -> Path:
    row_dir = tmp_path / "01--row1"
    item_dir = row_dir / "mini_review" / "i1"
    item_dir.mkdir(parents=True)
    (item_dir / "response.txt").write_text("off by one in the loop", encoding="utf-8")
    (tmp_path / "run.toml").write_text(
        """
[run]
git_sha = "abc123"

[suite.judge]
model = "j"
temperature = 0.0
samples = 1
max_attempts = 2
route = "litellm"

[suite.coding]
""",
        encoding="utf-8",
    )
    return tmp_path


class _FakeJudgeTransport:
    def complete(self, prompt: str, *, temperature: float) -> str:
        return json.dumps({"scores": {"a": 90}, "total": 90})


def test_rejudge_run_rescores_from_persisted_response(tmp_path):
    run_dir = _seed_run(tmp_path)
    category = Category(
        name="mini_review",
        path=Path("."),
        items=[Item(id="i1", prompt="review this", meta={})],
        rubric=Rubric(dimensions={"a": 100}),
        rubric_md="score a (100)",
    )
    outcomes = rejudge_run(
        run_dir,
        [category],
        judge_transport_factory=lambda judge_cfg: _FakeJudgeTransport(),
    )
    assert outcomes == [{"row": "01--row1", "category": "mini_review", "item": "i1", "total": 90}]
    judge_data = json.loads((run_dir / "01--row1" / "mini_review" / "i1" / "judge.json").read_text())
    assert judge_data["combined"]["total"] == 90


def test_rejudge_run_samples_override_takes_precedence_over_run_toml(tmp_path):
    run_dir = _seed_run(tmp_path)
    category = Category(
        name="mini_review",
        path=Path("."),
        items=[Item(id="i1", prompt="review this", meta={})],
        rubric=Rubric(dimensions={"a": 100}),
        rubric_md="score a (100)",
    )
    calls = []

    class _CountingTransport:
        def complete(self, prompt: str, *, temperature: float) -> str:
            calls.append(1)
            return json.dumps({"scores": {"a": 90}, "total": 90})

    rejudge_run(
        run_dir,
        [category],
        samples_override=3,
        judge_transport_factory=lambda judge_cfg: _CountingTransport(),
    )
    assert len(calls) == 3  # one sample per configured sample count, not run.toml's samples=1
```

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/benchmark/eval/test_rejudge.py -v`
Expected: `AttributeError: module 'modelman.benchmark.eval.runner' has no attribute 'rejudge_run'`

- [ ] **Step 3: Append `rejudge_run` to `runner.py`**

Add `import json` and `import tomllib` and `from dataclasses import asdict` to the existing import block at the top of `src/modelman/benchmark/eval/runner.py`; add `JudgeConfig` to the existing `from modelman.benchmark.eval.suite import (...)` line; add `judge_row` to the existing `from modelman.benchmark.judge_core import JudgeTransport, LiteLLMJudgeTransport` line. Then append the following to the end of the file:

```python
# append to src/modelman/benchmark/eval/runner.py


def _judge_config_from_run_toml(run_dir: Path, samples_override: int | None) -> JudgeConfig:
    with (run_dir / "run.toml").open("rb") as f:
        run_data = tomllib.load(f)
    judge_raw = run_data["suite"]["judge"]
    return JudgeConfig(
        model=judge_raw["model"],
        temperature=judge_raw["temperature"],
        samples=samples_override if samples_override is not None else judge_raw["samples"],
        max_attempts=judge_raw["max_attempts"],
        route=judge_raw["route"],
    )


def rejudge_run(
    run_dir: Path,
    categories: list[Category],
    *,
    row_filter: list[str] | None = None,
    samples_override: int | None = None,
    judge_transport_factory=None,
) -> list[dict]:
    """Re-score every judged-category item from its persisted response.txt,
    without regenerating anything. `coding` has nothing to re-judge —
    EvalPlus's pass@1 is deterministic and not touched here. There is no
    Suite in hand at rejudge time — the judge config comes back from the
    run's own persisted run.toml (Task 8's write_run_toml)."""
    by_name = {c.name: c for c in categories}
    judge_cfg = _judge_config_from_run_toml(run_dir, samples_override)
    transport = (judge_transport_factory or _default_judge_transport_factory)(judge_cfg)

    outcomes: list[dict] = []
    for row_dir in sorted(p for p in run_dir.iterdir() if p.is_dir()):
        if row_filter and row_dir.name not in row_filter:
            continue
        for category_dir in sorted(p for p in row_dir.iterdir() if p.is_dir()):
            category = by_name.get(category_dir.name)
            if category is None or category.rubric is None:
                continue  # coding, or a category this rejudge call wasn't given
            for item_dir in sorted(p for p in category_dir.iterdir() if p.is_dir()):
                response_path = item_dir / "response.txt"
                if not response_path.is_file():
                    continue
                item = next((i for i in category.items if i.id == item_dir.name), None)
                if item is None:
                    continue
                response_text = response_path.read_text(encoding="utf-8")
                prompt = judged_runner._build_judge_prompt(item, category, response_text)
                outcome = judge_row(
                    transport,
                    prompt,
                    category.rubric,
                    temperature=judge_cfg.temperature,
                    samples=judge_cfg.samples,
                    max_attempts=judge_cfg.max_attempts,
                )
                (item_dir / "judge.json").write_text(
                    json.dumps(asdict(outcome), indent=2), encoding="utf-8"
                )
                outcomes.append(
                    {
                        "row": row_dir.name,
                        "category": category.name,
                        "item": item.id,
                        "total": outcome.combined.total if outcome.combined else None,
                    }
                )
    return outcomes
```

- [ ] **Step 4: Run test to verify it passes**

Run: `uv run pytest tests/benchmark/eval/test_rejudge.py -v`
Expected: PASS

- [ ] **Step 5: Run the full eval test package as a regression check**

Run: `uv run pytest tests/benchmark/eval/ -v`
Expected: all PASS

- [ ] **Step 6: Lint and commit**

```bash
uv run ruff check src/modelman/benchmark/eval/runner.py tests/benchmark/eval/test_rejudge.py
uv run ruff format src/modelman/benchmark/eval/runner.py tests/benchmark/eval/test_rejudge.py
git add src/modelman/benchmark/eval/runner.py tests/benchmark/eval/test_rejudge.py
git commit -m "feat(modelman): add eval.runner.rejudge_run for cheap re-scoring

completes plan item #9 of docs/superpowers/plans/2026-09-17-capability-eval-benchmark.md"
```

---

## Task 10: `eval/cli.py` — CLI surface, wired into `modelman benchmark`

**Files:**
- Create: `src/modelman/benchmark/eval/cli.py`
- Modify: `src/modelman/benchmark/cli.py:1-24` (add `add_typer(eval_app, name="eval")`)
- Test: `tests/benchmark/eval/test_cli.py`

**Interfaces:**
- Produces: `eval_app` (typer.Typer), commands `list-categories`, `list-items`, `run`, `show`, `judge`.
- Consumes: `eval.category.list_categories`, `eval.suite.load_suite`, `eval.runner.{run_suite, rejudge_run, RunSavedButRestoreFailed, DEFAULT_RESULTS_DIR}`, `modelman.registry.load_registry`, `modelman.state.{load_state, save_state}`.

- [ ] **Step 1: Write the failing tests**

```python
# tests/benchmark/eval/test_cli.py
"""Tests for modelman.benchmark.eval.cli — the `modelman benchmark eval`
command surface, exercised through typer's CliRunner."""

from pathlib import Path
from unittest.mock import patch

from typer.testing import CliRunner

from modelman.benchmark.eval.cli import eval_app

runner = CliRunner()

FIXTURE_CATEGORIES = Path(__file__).parent / "fixtures" / "categories"


def test_list_categories_cmd_prints_every_category():
    result = runner.invoke(eval_app, ["list-categories", "--root", str(FIXTURE_CATEGORIES)])
    assert result.exit_code == 0
    assert "mini_review" in result.stdout
    assert "coding" in result.stdout


def test_list_items_cmd_prints_item_ids():
    result = runner.invoke(
        eval_app, ["list-items", "--category", "mini_review", "--root", str(FIXTURE_CATEGORIES)]
    )
    assert result.exit_code == 0
    assert "off-by-one" in result.stdout


def test_list_items_cmd_unknown_category_errors():
    result = runner.invoke(
        eval_app, ["list-items", "--category", "nope", "--root", str(FIXTURE_CATEGORIES)]
    )
    assert result.exit_code == 1


@patch("modelman.benchmark.eval.cli.load_registry")
def test_run_cmd_dry_run_prints_resolved_rows_without_running(mock_load_registry, tmp_path):
    from modelman.registry import ModelEntry, ProviderEntry, Registry

    mock_load_registry.return_value = Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", location="local")],
        models=[ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a")],
    )
    suite_path = tmp_path / "suite.toml"
    suite_path.write_text(
        """
name = "t"
[judge]
model = "j"
temperature = 0.0
samples = 1
max_attempts = 1
route = "litellm"

[[rows]]
model = "ollama/a"
route = "litellm"
""",
        encoding="utf-8",
    )
    result = runner.invoke(
        eval_app,
        ["run", "--suite", str(suite_path), "--root", str(FIXTURE_CATEGORIES), "--dry-run"],
    )
    assert result.exit_code == 0
    assert "ollama/a" in result.stdout
    assert "dry run" in result.stdout
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `uv run pytest tests/benchmark/eval/test_cli.py -v`
Expected: `ModuleNotFoundError: No module named 'modelman.benchmark.eval.cli'`

- [ ] **Step 3: Write `cli.py`**

```python
# src/modelman/benchmark/eval/cli.py
"""CLI for `modelman benchmark eval`."""

from __future__ import annotations

from pathlib import Path

import typer

from modelman.benchmark.errors import BenchmarkError
from modelman.benchmark.eval.category import list_categories, load_category
from modelman.benchmark.eval.runner import (
    DEFAULT_RESULTS_DIR,
    RunSavedButRestoreFailed,
    rejudge_run,
    run_suite,
)
from modelman.benchmark.eval.suite import load_suite
from modelman.registry import load_registry
from modelman.state import load_state, save_state

eval_app = typer.Typer(help="Cross-category capability benchmark (reasoning/planning/coding/"
    "code_review/doc_summary), single-turn, coding graded by EvalPlus.")

DEFAULT_CATEGORIES_ROOT = Path("benchmarks/tasks/eval")
DEFAULT_SUITES_DIR = Path("benchmarks/suites")


@eval_app.command("list-categories")
def list_categories_cmd(
    root: Path = typer.Option(DEFAULT_CATEGORIES_ROOT, "--root"),  # noqa: B008
) -> None:
    for category in list_categories(root):
        count = len(category.items) if category.rubric is not None else 0
        typer.echo(f"{category.name}  (items: {count})")


@eval_app.command("list-items")
def list_items_cmd(
    category: str = typer.Option(..., "--category"),
    root: Path = typer.Option(DEFAULT_CATEGORIES_ROOT, "--root"),  # noqa: B008
) -> None:
    try:
        loaded = load_category(root / category)
    except BenchmarkError as exc:
        typer.echo(f"error: {exc}", err=True)
        raise typer.Exit(1) from exc
    for item in loaded.items:
        typer.echo(item.id)


@eval_app.command("run")
def run_cmd(
    suite: Path = typer.Option(..., "--suite"),  # noqa: B008
    root: Path = typer.Option(DEFAULT_CATEGORIES_ROOT, "--root"),  # noqa: B008
    category: list[str] = typer.Option([], "--category"),  # noqa: B008
    row: list[str] = typer.Option([], "--row"),  # noqa: B008
    results_dir: Path | None = typer.Option(None, "--results-dir"),  # noqa: B008
    dry_run: bool = typer.Option(False, "--dry-run"),
) -> None:
    registry = load_registry()
    try:
        loaded_suite = load_suite(suite, registry)
    except BenchmarkError as exc:
        typer.echo(f"error: {exc}", err=True)
        raise typer.Exit(1) from exc

    categories = list_categories(root)
    if category:
        wanted = set(category)
        categories = [c for c in categories if c.name in wanted]

    rows = loaded_suite.rows
    if row:
        wanted_rows = set(row)
        rows = [r for i, r in enumerate(rows, start=1) if r.label in wanted_rows or str(i) in wanted_rows]

    if dry_run:
        for i, r in enumerate(rows, start=1):
            row_categories = r.categories or [c.name for c in categories]
            typer.echo(f"{i:02d}  {r.label}  model={r.model_id}  route={r.route}  categories={row_categories}")
        typer.echo(f"{len(rows)} row(s), {len(categories)} categorie(s) resolved, dry run — nothing executed")
        return

    try:
        run_dir, results = run_suite(
            loaded_suite, registry, categories, row_filter=row or None, results_dir=results_dir
        )
    except RunSavedButRestoreFailed as exc:
        _record_run(exc.run_dir)
        typer.echo(f"error: {exc}", err=True)
        raise typer.Exit(1) from None
    except BenchmarkError as exc:
        typer.echo(f"error: {exc}", err=True)
        raise typer.Exit(1) from exc

    _record_run(run_dir)
    ok = sum(1 for r in results if r.error is None)
    typer.echo(f"Eval benchmark complete: {len(results)} row(s), {ok} ran without an isolation error")
    typer.echo(f"Results: {run_dir}")


def _record_run(run_dir: Path) -> None:
    state = load_state()
    benchmarks = state.extra.setdefault("benchmarks", {})
    benchmarks["eval_last_run"] = str(run_dir)
    save_state(state)


@eval_app.command("show")
def show_cmd(
    latest: bool = typer.Option(False, "--latest"),
    run_id: str | None = typer.Option(None, "--run-id"),  # noqa: B008
    results_dir: Path = typer.Option(DEFAULT_RESULTS_DIR, "--results-dir"),  # noqa: B008
) -> None:
    if not latest and not run_id:
        typer.echo("error: specify --latest or --run-id", err=True)
        raise typer.Exit(1)
    if latest:
        state = load_state()
        run_dir_str = state.extra.get("benchmarks", {}).get("eval_last_run")
        if not run_dir_str:
            typer.echo("error: no latest eval run recorded", err=True)
            raise typer.Exit(1)
        md_path = Path(run_dir_str) / "summary.md"
    else:
        md_path = results_dir / str(run_id) / "summary.md"
    if not md_path.exists():
        typer.echo(f"error: results not found: {md_path}", err=True)
        raise typer.Exit(1)
    typer.echo(md_path.read_text(encoding="utf-8"))


@eval_app.command("judge")
def judge_cmd(
    latest: bool = typer.Option(False, "--latest"),
    run_id: str | None = typer.Option(None, "--run-id"),  # noqa: B008
    root: Path = typer.Option(DEFAULT_CATEGORIES_ROOT, "--root"),  # noqa: B008
    row: list[str] = typer.Option([], "--row"),  # noqa: B008
    samples: int | None = typer.Option(None, "--samples"),
    results_dir: Path = typer.Option(DEFAULT_RESULTS_DIR, "--results-dir"),  # noqa: B008
) -> None:
    if not latest and not run_id:
        typer.echo("error: specify --latest or --run-id", err=True)
        raise typer.Exit(1)
    if latest:
        state = load_state()
        run_dir_str = state.extra.get("benchmarks", {}).get("eval_last_run")
        if not run_dir_str:
            typer.echo("error: no latest eval run recorded", err=True)
            raise typer.Exit(1)
        target_dir = Path(run_dir_str)
    else:
        target_dir = results_dir / str(run_id)

    categories = list_categories(root)
    try:
        outcomes = rejudge_run(target_dir, categories, row_filter=row or None, samples_override=samples)
    except (BenchmarkError, FileNotFoundError) as exc:
        typer.echo(f"error: {exc}", err=True)
        raise typer.Exit(1) from exc
    for outcome in outcomes:
        typer.echo(f"{outcome['row']}/{outcome['category']}/{outcome['item']}: total={outcome['total']}")


__all__ = ["eval_app"]
```

- [ ] **Step 4: Wire `eval_app` into `benchmark_app`**

In `src/modelman/benchmark/cli.py`, add the import and mount alongside the existing `agent_app`:

```python
# src/modelman/benchmark/cli.py — add this import near the top
from modelman.benchmark.eval.cli import eval_app
```

```python
# and this line right after the existing agent_app mount
benchmark_app.add_typer(eval_app, name="eval")
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `uv run pytest tests/benchmark/eval/test_cli.py -v`
Expected: all PASS

- [ ] **Step 6: Confirm the mount works end-to-end**

Run: `uv run modelman benchmark eval list-categories --root ../benchmarks/tasks/eval`
Expected: prints nothing yet (categories directory doesn't exist until Tasks 13-17) or a clean empty run — not a crash. If it crashes, fix the CLI wiring before proceeding.

- [ ] **Step 7: Lint and commit**

```bash
uv run ruff check src/modelman/benchmark/eval/cli.py src/modelman/benchmark/cli.py tests/benchmark/eval/test_cli.py
uv run ruff format src/modelman/benchmark/eval/cli.py src/modelman/benchmark/cli.py tests/benchmark/eval/test_cli.py
git add src/modelman/benchmark/eval/cli.py src/modelman/benchmark/cli.py tests/benchmark/eval/test_cli.py
git commit -m "feat(modelman): add modelman benchmark eval CLI

completes plan item #10 of docs/superpowers/plans/2026-09-17-capability-eval-benchmark.md"
```

---

## Task 11: EvalPlus as an optional `modelman[eval]` extra

**Files:**
- Modify: `pyproject.toml`

**Interfaces:** none (packaging only)

- [ ] **Step 1: Add the optional-dependencies group**

In `modelman/pyproject.toml`, add after the existing `dependencies = [...]` block:

```toml
[project.optional-dependencies]
eval = [
    "evalplus>=0.3.1",
]
```

- [ ] **Step 2: Verify plain install does NOT pull EvalPlus**

Run: `uv sync && uv pip show evalplus`
Expected: `uv sync` succeeds; `uv pip show evalplus` prints "Package(s) not found"

- [ ] **Step 3: Verify the extra installs it**

Run: `uv sync --extra eval && uv pip show evalplus`
Expected: succeeds; `uv pip show evalplus` prints a version line

- [ ] **Step 4: Commit**

```bash
git add pyproject.toml
git commit -m "build(modelman): add EvalPlus as an optional [eval] extra

Not installed by plain uv sync/make install — only the coding eval category
needs it, and it executes model-generated code locally.
completes plan item #11 of docs/superpowers/plans/2026-09-17-capability-eval-benchmark.md"
```

---

## Task 12: Verify the EvalPlus CLI invocation against a real install

**Files:**
- Modify: `src/modelman/benchmark/eval/evalplus_runner.py` (only if the real CLI surface differs from Task 6's draft)
- Modify: `tests/benchmark/eval/test_evalplus_runner.py` (only if command construction changes)

This is the spec's flagged risk: Task 6's `_build_command` copies the research doc's cited flags, unverified. This task drives a real EvalPlus install once and corrects the wrapper if needed — the "UNVERIFIED" convention this repo's guides already use for anything not driven live.

- [ ] **Step 1: Install the extra and confirm the CLI exists**

```bash
uv sync --extra eval
uv run --extra eval evalplus.evaluate --help
```

Expected: prints EvalPlus's own help text (confirms the entry point name and flag names). Record the actual flag names for `--dataset`, `--backend`, `--base-url`, `--model`, output-directory override, and sample-count limiting in a note here (edit this file with what you found before continuing) if any differ from Task 6's `--root`/`--n-samples` guesses.

- [ ] **Step 2: Isolate a local provider and run one real row**

```bash
uv run modelman provider isolate ollama
uv run --extra eval evalplus.evaluate --dataset humaneval --backend openai \
    --base-url http://localhost:11434/v1 --model qwen3.8:27b-mlx
uv run modelman provider restore
```

Expected: EvalPlus runs generation + grading against the local ollama server and prints a pass@1 line. Note the exact line format it prints (this is what `_parse_pass_at_1`'s regex must match).

- [ ] **Step 3: Update `evalplus_runner.py` if Steps 1-2 revealed a mismatch**

If any flag name or output format differed from Task 6's draft, edit `_build_command` and/or `_PASS_AT_1_RE` in `src/modelman/benchmark/eval/evalplus_runner.py` to match what Step 2 actually printed, and update the corresponding fake-`stdout` strings in `tests/benchmark/eval/test_evalplus_runner.py` to match.

- [ ] **Step 4: Re-run the unit tests**

Run: `uv run pytest tests/benchmark/eval/test_evalplus_runner.py -v`
Expected: all PASS (unchanged if Step 2 matched the draft, updated-and-passing otherwise)

- [ ] **Step 5: Commit (only if Step 3 changed anything)**

```bash
git add src/modelman/benchmark/eval/evalplus_runner.py tests/benchmark/eval/test_evalplus_runner.py
git commit -m "fix(modelman): correct evalplus_runner's CLI invocation against a live install

Verified live 2026-09-17 (see plan Task 12): <note what changed, or 'no
changes needed — draft flags matched exactly'>.
completes plan item #12 of docs/superpowers/plans/2026-09-17-capability-eval-benchmark.md"
```

---

## Task 13: Author the `reasoning` category

**Files:**
- Create: `benchmarks/tasks/eval/reasoning/items.toml`
- Create: `benchmarks/tasks/eval/reasoning/rubric.toml`
- Create: `benchmarks/tasks/eval/reasoning/rubric.md`
- Create: `benchmarks/tasks/eval/reasoning/meta.toml`
- Test: `tests/benchmark/eval/test_content_reasoning.py`

- [ ] **Step 1: Write the failing smoke test**

```python
# tests/benchmark/eval/test_content_reasoning.py
"""Smoke test: the real reasoning category content loads and its rubric
sums to 100. Real content quality (are these puzzles actually hard/fair) is
a human judgment call, not something a unit test can verify — this test
only pins the load-time contract."""

from pathlib import Path

from modelman.benchmark.eval.category import load_category

CATEGORY_ROOT = Path(__file__).parent.parent.parent.parent / "benchmarks" / "tasks" / "eval"


def test_reasoning_category_loads_and_sums_to_100():
    category = load_category(CATEGORY_ROOT / "reasoning")
    assert category.rubric is not None
    assert sum(category.rubric.dimensions.values()) == 100
    assert len(category.items) >= 6
    assert len({item.id for item in category.items}) == len(category.items)
```

- [ ] **Step 2: Run to verify it fails**

Run: `uv run pytest tests/benchmark/eval/test_content_reasoning.py -v`
Expected: FAIL — `benchmarks/tasks/eval/reasoning` doesn't exist yet

- [ ] **Step 3: Write the category content**

```toml
# benchmarks/tasks/eval/reasoning/rubric.toml
# verdicts must come before [dimensions] — TOML does not close a table on a
# blank line, so a bare `verdicts = [...]` written after [dimensions] would
# parse as dimensions.verdicts instead of a top-level key.
verdicts = ["correct", "partially_correct", "incorrect"]

[dimensions]
correct_answer = 40
valid_reasoning_chain = 30
no_unjustified_leaps = 15
clarity = 15
```

```markdown
<!-- benchmarks/tasks/eval/reasoning/rubric.md -->
# Judge rubric — reasoning

You are scoring a model's answer to a self-contained logic/math/deduction
puzzle. You are told the correct final answer as a reference note (never
shown to the model) — use it to check `correct_answer`, but score the other
three dimensions from the reasoning text itself, independent of whether the
final answer happened to be right.

| dimension | pts | question |
|---|---|---|
| correct_answer | 40 | does the stated final answer match the reference answer |
| valid_reasoning_chain | 30 | is each step a valid inference from the previous one, not just a plausible-sounding leap |
| no_unjustified_leaps | 15 | penalize skipped steps or unstated assumptions presented as certain |
| clarity | 15 | can a reader follow the chain of reasoning without re-deriving it themselves |

Respond with strict JSON, no prose outside the object:

```json
{"scores": {"correct_answer": 0, "valid_reasoning_chain": 0, "no_unjustified_leaps": 0, "clarity": 0},
 "total": 0,
 "verdict": "correct | partially_correct | incorrect",
 "flags": ["..."],
 "rationale": "..."}
```
```

```toml
# benchmarks/tasks/eval/reasoning/items.toml

[[items]]
id = "age-riddle"
prompt = """
In five years, Mira will be twice as old as she was three years ago.
How old is Mira now? Show your reasoning, then state the final answer clearly.
"""
[items.meta]
reference_notes = "correct answer: 11. Let x = current age. x+5 = 2*(x-3) -> x+5 = 2x-6 -> x = 11."

[[items]]
id = "logic-grid-3"
prompt = """
Three friends -- Ada, Bo, and Cy -- each own exactly one pet from
{cat, dog, fish} and live on a different one of floors {1, 2, 3}.
- Ada does not live on floor 1.
- The dog owner lives on floor 2.
- Cy owns the fish.
- Bo lives on a higher floor than the dog owner.
Who owns the dog, and what floor do they live on? Show your reasoning.
"""
[items.meta]
reference_notes = "correct answer: Ada owns the dog and lives on floor 2. Cy owns fish (given). Bo owns cat (remaining) or dog -- dog owner is floor 2, Bo lives higher than floor 2 so Bo is floor 3 and does not own the dog; Ada is left with the dog, and since Ada isn't floor 1, Ada is floor 2, matching the dog-owner-is-floor-2 clue. Bo is floor 3 with the cat, Cy is floor 1 with the fish."

[[items]]
id = "rate-problem"
prompt = """
Pipe A fills a tank in 6 hours. Pipe B fills the same tank in 4 hours.
If both pipes run together starting from an empty tank, how long until the
tank is full? Show your work.
"""
[items.meta]
reference_notes = "correct answer: 2.4 hours (12/5 hours). Combined rate = 1/6 + 1/4 = 5/12 tank/hour; time = 12/5 = 2.4 hours."

[[items]]
id = "sequence-rule"
prompt = """
A sequence begins 2, 6, 12, 20, 30, ...
What is the 8th term? Explain the pattern you found before giving the answer.
"""
[items.meta]
reference_notes = "correct answer: 72. The nth term is n*(n+1); 8th term = 8*9 = 72."

[[items]]
id = "overlap-sets"
prompt = """
In a class of 30 students, 18 play soccer, 15 play basketball, and 7 play
neither sport. How many students play both soccer and basketball?
"""
[items.meta]
reference_notes = "correct answer: 10. Students playing at least one sport = 30-7=23. 18+15-23=10 play both."

[[items]]
id = "truth-tellers"
prompt = """
On an island, every person is either a truth-teller (always tells the
truth) or a liar (always lies). You meet two people, P and Q.
P says: 'Q is a liar.'
Q says: 'P and I are both truth-tellers.'
What is P, and what is Q? Show your reasoning.
"""
[items.meta]
reference_notes = "correct answer: P is a truth-teller, Q is a liar. If Q were a truth-teller, Q's statement (both are truth-tellers) would be true, meaning P is a truth-teller, so P's statement ('Q is a liar') would have to be true -- contradiction (Q can't be both liar and truth-teller). So Q is a liar; Q's statement is false, meaning not both are truth-tellers. Since Q is a liar, P's statement 'Q is a liar' is true, so P is a truth-teller. Consistent."
```

```toml
# benchmarks/tasks/eval/reasoning/meta.toml
[meta]
intent = "Six self-contained puzzles (arithmetic word problem, logic grid, rate problem, sequence pattern, set overlap, truth-teller riddle) chosen to need a real multi-step chain rather than a memorized fact. Answers are non-obvious but each is verifiable by hand in under a minute, so the reference_notes are exact and short."
```

- [ ] **Step 4: Run to verify it passes**

Run: `uv run pytest tests/benchmark/eval/test_content_reasoning.py -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add benchmarks/tasks/eval/reasoning/ tests/benchmark/eval/test_content_reasoning.py
git commit -m "content(eval): author the reasoning category

Six hand-authored multi-step puzzles (arithmetic, logic grid, rate, sequence,
set overlap, truth-teller) with exact reference answers.
completes plan item #13 of docs/superpowers/plans/2026-09-17-capability-eval-benchmark.md"
```

---

## Task 14: Author the `planning` category

**Files:**
- Create: `benchmarks/tasks/eval/planning/{items.toml,rubric.toml,rubric.md,meta.toml}`
- Test: `tests/benchmark/eval/test_content_planning.py`

- [ ] **Step 1: Write the failing smoke test**

```python
# tests/benchmark/eval/test_content_planning.py
"""Smoke test for the planning category — see test_content_reasoning.py's
docstring for what this does and doesn't verify."""

from pathlib import Path

from modelman.benchmark.eval.category import load_category

CATEGORY_ROOT = Path(__file__).parent.parent.parent.parent / "benchmarks" / "tasks" / "eval"


def test_planning_category_loads_and_sums_to_100():
    category = load_category(CATEGORY_ROOT / "planning")
    assert category.rubric is not None
    assert sum(category.rubric.dimensions.values()) == 100
    assert len(category.items) >= 6
```

- [ ] **Step 2: Run to verify it fails**

Run: `uv run pytest tests/benchmark/eval/test_content_planning.py -v`
Expected: FAIL — directory doesn't exist

- [ ] **Step 3: Write the category content**

```toml
# benchmarks/tasks/eval/planning/rubric.toml
verdicts = ["solid_plan", "partial_plan", "weak_plan"]

[dimensions]
decomposition = 25
risk_identification = 25
completeness = 25
scope_discipline = 25
```

```markdown
<!-- benchmarks/tasks/eval/planning/rubric.md -->
# Judge rubric — planning

You are scoring a model's implementation plan for a feature/migration
request. The model was asked to produce a plan only, not code.

| dimension | pts | question |
|---|---|---|
| decomposition | 25 | are the steps broken into sensible, ordered, independently-checkable units rather than one vague blob |
| risk_identification | 25 | does the plan call out real risks/edge cases/failure modes specific to this request, not generic boilerplate ("test thoroughly") |
| completeness | 25 | is anything a competent engineer would consider essential to this request obviously missing |
| scope_discipline | 25 | does the plan stay scoped to what was asked, without unrequested rewrites/new dependencies/gold-plating (YAGNI) |

Respond with strict JSON, no prose outside the object:

```json
{"scores": {"decomposition": 0, "risk_identification": 0, "completeness": 0, "scope_discipline": 0},
 "total": 0,
 "verdict": "solid_plan | partial_plan | weak_plan",
 "flags": ["..."],
 "rationale": "..."}
```
```

```toml
# benchmarks/tasks/eval/planning/items.toml

[[items]]
id = "rate-limit-api"
prompt = """
Our public REST API (Node/Express) has no rate limiting. We're seeing
occasional abuse from a small number of IPs hammering the /search endpoint.
Write an implementation plan to add rate limiting. Do not write code --
just the plan.
"""
[items.meta]
reference_notes = "A solid plan names a concrete algorithm (token bucket/sliding window), picks a storage backend appropriate for a multi-instance deployment (Redis, not in-memory, if the service scales horizontally), covers response shape (429 + Retry-After), covers per-IP vs per-API-key limiting, and flags the risk of shared-NAT users being over-limited. A weak plan says 'add express-rate-limit middleware' with no further detail."

[[items]]
id = "sqlite-to-postgres"
prompt = """
A small internal tool currently stores its data in a single SQLite file.
It's outgrowing SQLite (concurrent writers, growing dataset) and needs to
move to Postgres. Write an implementation plan for the migration. Do not
write code -- just the plan.
"""
[items.meta]
reference_notes = "A solid plan covers: schema translation (SQLite type quirks), a data migration/backfill step with verification (row counts, spot checks), a cutover strategy (dual-write window, or downtime window, explicitly chosen and justified), rollback plan if the cutover fails, and connection/config changes. A weak plan just says 'export to postgres' with no cutover or rollback thinking."

[[items]]
id = "add-dark-mode"
prompt = """
A React web app has no dark mode. Users have asked for one. Write an
implementation plan to add a dark mode toggle that persists across
sessions. Do not write code -- just the plan.
"""
[items.meta]
reference_notes = "A solid plan covers: a token/CSS-variable-based theming approach (not a page-by-page repaint), persistence mechanism (localStorage + respecting prefers-color-scheme as a default), where the toggle lives in the UI, and a check for any hardcoded colors that would break under the new theme. Scope discipline: it should not propose a full design-system rewrite when a toggle was asked for."

[[items]]
id = "flaky-test-suite"
prompt = """
A CI test suite has become flaky: about 1 in 20 runs fails on a test
unrelated to the code change, and re-running usually passes. The team
wants this fixed before it erodes trust in CI. Write an investigation +
remediation plan. Do not write code -- just the plan.
"""
[items.meta]
reference_notes = "A solid plan separates investigation from remediation: first, identify which specific tests flake and gather evidence (timing, shared state, external dependencies, test order sensitivity) before proposing a fix; considers common root causes (shared mutable fixtures, real network calls, race conditions, time-based assertions); proposes a way to track flake rate going forward, not just a one-time fix. A weak plan jumps straight to 'add retries' without investigating root cause."

[[items]]
id = "webhook-retry"
prompt = """
Our system sends webhooks to customer-provided URLs. Currently, if a
webhook delivery fails (customer server is down, times out, or returns a
5xx), we drop it silently. Customers want reliable delivery. Write an
implementation plan to add retry logic. Do not write code -- just the plan.
"""
[items.meta]
reference_notes = "A solid plan covers: retry policy (backoff strategy, max attempts, what counts as retryable vs. not -- e.g. a 4xx should probably not retry the same way a 5xx does), a dead-letter/failure-visibility mechanism so customers can see permanently-failed deliveries, idempotency considerations (the customer's endpoint may receive the same webhook twice), and ordering guarantees (or explicit lack thereof). A weak plan just says 'retry 3 times' with no backoff or idempotency thinking."

[[items]]
id = "deprecate-endpoint"
prompt = """
An old API endpoint (/v1/users/lookup) is used by a handful of external
integration partners and needs to be deprecated in favor of /v2/users. You
don't control the partners' code. Write a deprecation plan. Do not write
code -- just the plan.
"""
[items.meta]
reference_notes = "A solid plan covers: a communication timeline (advance notice, not a surprise cutoff), a way to detect who's still calling the old endpoint (usage metrics/logging) before removing it, a deprecation-warning mechanism (response header, or a grace-period behavior) rather than an instant hard cutover, and a concrete sunset date tied to actual usage dropping rather than an arbitrary one. A weak plan just says 'turn it off and tell people'."
```

```toml
# benchmarks/tasks/eval/planning/meta.toml
[meta]
intent = "Six realistic engineering-planning prompts (rate limiting, DB migration, a UI feature, a CI flakiness investigation, webhook reliability, an API deprecation) chosen so a shallow answer is easy to spot: each has 2-4 non-obvious concerns a competent engineer would raise unprompted."
```

- [ ] **Step 4: Run to verify it passes**

Run: `uv run pytest tests/benchmark/eval/test_content_planning.py -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add benchmarks/tasks/eval/planning/ tests/benchmark/eval/test_content_planning.py
git commit -m "content(eval): author the planning category

completes plan item #14 of docs/superpowers/plans/2026-09-17-capability-eval-benchmark.md"
```

---

## Task 15: Author the `code_review` category

**Files:**
- Create: `benchmarks/tasks/eval/code_review/{items.toml,rubric.toml,rubric.md,meta.toml}`
- Test: `tests/benchmark/eval/test_content_code_review.py`

- [ ] **Step 1: Write the failing smoke test**

```python
# tests/benchmark/eval/test_content_code_review.py
"""Smoke test for the code_review category — see
test_content_reasoning.py's docstring for scope."""

from pathlib import Path

from modelman.benchmark.eval.category import load_category

CATEGORY_ROOT = Path(__file__).parent.parent.parent.parent / "benchmarks" / "tasks" / "eval"


def test_code_review_category_loads_and_sums_to_100():
    category = load_category(CATEGORY_ROOT / "code_review")
    assert category.rubric is not None
    assert sum(category.rubric.dimensions.values()) == 100
    assert len(category.items) >= 6
    for item in category.items:
        assert "seeded_issue" in item.meta
```

- [ ] **Step 2: Run to verify it fails**

Run: `uv run pytest tests/benchmark/eval/test_content_code_review.py -v`
Expected: FAIL

- [ ] **Step 3: Write the category content**

```toml
# benchmarks/tasks/eval/code_review/rubric.toml
verdicts = ["caught_it", "partial_catch", "missed_it"]

[dimensions]
bug_detection = 40
false_positive_control = 20
severity_judgment = 20
actionability = 20
```

```markdown
<!-- benchmarks/tasks/eval/code_review/rubric.md -->
# Judge rubric — code_review

You are scoring a model's review of a code snippet that contains exactly
one seeded issue (given to you as a reference note, never shown to the
model). The model was asked to review, not fix.

| dimension | pts | question |
|---|---|---|
| bug_detection | 40 | did the review identify the actual seeded issue (not just any issue) |
| false_positive_control | 20 | penalize inventing problems the code doesn't actually have |
| severity_judgment | 20 | does the review correctly convey how serious this issue is (a data-corruption bug flagged as "nitpick" loses points; a style nit flagged as "blocking" also loses points) |
| actionability | 20 | is the feedback specific enough that an engineer could act on it directly (names the line/condition, not just "this looks off") |

Respond with strict JSON, no prose outside the object:

```json
{"scores": {"bug_detection": 0, "false_positive_control": 0, "severity_judgment": 0, "actionability": 0},
 "total": 0,
 "verdict": "caught_it | partial_catch | missed_it",
 "flags": ["..."],
 "rationale": "..."}
```
```

```toml
# benchmarks/tasks/eval/code_review/items.toml

[[items]]
id = "off-by-one-slice"
prompt = """
Review this Python function for correctness issues:

```python
def last_n_items(items, n):
    return items[len(items) - n - 1:]
```
"""
[items.meta]
seeded_issue = "Off-by-one: should be items[len(items) - n:] to return exactly the last n items; the '- 1' includes one extra element."

[[items]]
id = "sql-injection"
prompt = """
Review this Python function for correctness and security issues:

```python
def find_user(conn, username):
    query = f"SELECT * FROM users WHERE username = '{username}'"
    return conn.execute(query).fetchone()
```
"""
[items.meta]
seeded_issue = "SQL injection: username is interpolated directly into the query string instead of using a parameterized query. This is the primary issue; severity should be flagged as high/blocking, not a style nit."

[[items]]
id = "race-condition-counter"
prompt = """
Review this Python class for correctness issues in a multi-threaded context
(it is documented as being shared across worker threads):

```python
class RequestCounter:
    def __init__(self):
        self.count = 0

    def increment(self):
        self.count = self.count + 1
        return self.count
```
"""
[items.meta]
seeded_issue = "Race condition: increment() is not atomic (read-modify-write across three bytecode ops), so concurrent threads can lose updates. Needs a lock (threading.Lock) or an atomic primitive."

[[items]]
id = "resource-leak"
prompt = """
Review this Python function for correctness issues:

```python
def read_config(path):
    f = open(path)
    data = f.read()
    return json.loads(data)
```
"""
[items.meta]
seeded_issue = "Resource leak: the file handle f is never closed (no with-statement, no f.close()), and if json.loads raises, the leak is guaranteed on every call path, not just the happy path."

[[items]]
id = "mutable-default-arg"
prompt = """
Review this Python function for correctness issues:

```python
def add_tag(tag, tags=[]):
    tags.append(tag)
    return tags
```
"""
[items.meta]
seeded_issue = "Mutable default argument: tags=[] is created once at function definition time and shared/mutated across all calls that don't pass their own list, causing tags to accumulate across unrelated calls."

[[items]]
id = "exception-swallowing"
prompt = """
Review this Python function for correctness issues:

```python
def parse_amount(raw):
    try:
        return float(raw)
    except:
        return 0
```
"""
[items.meta]
seeded_issue = "Bare except silently swallows every exception (including KeyboardInterrupt/SystemExit) and returns a default of 0, which is indistinguishable from a legitimately-parsed zero -- masking malformed input as a valid zero amount rather than surfacing or logging the parse failure."
```

```toml
# benchmarks/tasks/eval/code_review/meta.toml
[meta]
intent = "Six snippets, one seeded issue each, spanning severity levels (off-by-one and mutable-default are subtle-but-real correctness bugs; SQL injection is a security-critical bug; race condition needs concurrency reasoning; resource leak and exception-swallowing are reliability issues). Chosen so a model that only pattern-matches on 'SQL string formatting = bad' without understanding severity still gets penalized on severity_judgment elsewhere in the set."
```

- [ ] **Step 4: Run to verify it passes**

Run: `uv run pytest tests/benchmark/eval/test_content_code_review.py -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add benchmarks/tasks/eval/code_review/ tests/benchmark/eval/test_content_code_review.py
git commit -m "content(eval): author the code_review category

Six snippets (off-by-one, SQL injection, race condition, resource leak,
mutable default arg, exception swallowing), one seeded issue each.
completes plan item #15 of docs/superpowers/plans/2026-09-17-capability-eval-benchmark.md"
```

---

## Task 16: Author the `doc_summary` category

**Files:**
- Create: `benchmarks/tasks/eval/doc_summary/{items.toml,rubric.toml,rubric.md,meta.toml}`
- Test: `tests/benchmark/eval/test_content_doc_summary.py`

- [ ] **Step 1: Write the failing smoke test**

```python
# tests/benchmark/eval/test_content_doc_summary.py
"""Smoke test for the doc_summary category — see
test_content_reasoning.py's docstring for scope."""

from pathlib import Path

from modelman.benchmark.eval.category import load_category

CATEGORY_ROOT = Path(__file__).parent.parent.parent.parent / "benchmarks" / "tasks" / "eval"


def test_doc_summary_category_loads_and_sums_to_100():
    category = load_category(CATEGORY_ROOT / "doc_summary")
    assert category.rubric is not None
    assert sum(category.rubric.dimensions.values()) == 100
    assert len(category.items) >= 6
    for item in category.items:
        assert len(item.prompt) > 200  # a real excerpt, not a stub sentence
```

- [ ] **Step 2: Run to verify it fails**

Run: `uv run pytest tests/benchmark/eval/test_content_doc_summary.py -v`
Expected: FAIL

- [ ] **Step 3: Write the category content**

```toml
# benchmarks/tasks/eval/doc_summary/rubric.toml
verdicts = ["excellent", "adequate", "inadequate", "unusable"]

[dimensions]
factual_accuracy = 35
coverage = 30
conciseness = 20
structure = 15
```

```markdown
<!-- benchmarks/tasks/eval/doc_summary/rubric.md -->
# Judge rubric — doc_summary

You are scoring a model's summary of a document excerpt. You are given the
original excerpt and the model's summary.

| dimension | pts | question |
|---|---|---|
| factual_accuracy | 35 | does every claim in the summary actually appear in (or follow directly from) the source; penalize hallucinated details heavily |
| coverage | 30 | does the summary capture the excerpt's key points, not just the first paragraph or an arbitrary subset |
| conciseness | 20 | is the summary actually shorter and denser than the source, or does it pad with restated filler |
| structure | 15 | is the summary organized and readable (not a single unbroken run-on) |

Respond with strict JSON, no prose outside the object:

```json
{"scores": {"factual_accuracy": 0, "coverage": 0, "conciseness": 0, "structure": 0},
 "total": 0,
 "verdict": "excellent | adequate | inadequate | unusable",
 "flags": ["..."],
 "rationale": "..."}
```
```

```toml
# benchmarks/tasks/eval/doc_summary/items.toml

[[items]]
id = "changelog-excerpt"
prompt = """
Summarize the following changelog excerpt in a few sentences, for someone
deciding whether to upgrade:

## v3.4.0
- BREAKING: `Client.connect()` now requires an explicit `timeout` argument;
  the previous default of 30s has been removed. Callers relying on the
  default will raise `TypeError` until updated.
- Fixed a memory leak in `StreamReader` that occurred when a connection was
  closed mid-read; long-running processes using streaming reads should see
  reduced memory growth.
- The `retry_on_failure` flag, deprecated since v3.1.0, has been removed.
  Use `RetryPolicy` instead.
- Performance: batch inserts are now ~2x faster due to a new bulk-encoding
  path; no API changes required to benefit.
- Fixed: `Client.close()` could hang indefinitely if called during an
  in-flight request; it now respects a 5s grace period before force-closing.
"""
[items.meta]
reference_notes = "Key facts to cover: (1) breaking change -- Client.connect() timeout arg now required, no default, will raise TypeError; (2) memory leak fix in StreamReader on mid-read close; (3) removal of long-deprecated retry_on_failure flag (use RetryPolicy); (4) ~2x faster batch inserts, no API change needed; (5) Client.close() hang fix with 5s grace period. A good summary flags the breaking change prominently since that's the actionable item for an upgrader."

[[items]]
id = "readme-excerpt"
prompt = """
Summarize the following README excerpt for a new contributor:

## Development setup

This project uses a monorepo layout with three independently-versioned
packages: `core/`, `api/`, and `worker/`. Each has its own `package.json`
and test suite, but they share a single `node_modules/` at the repo root
via npm workspaces -- do not run `npm install` inside a subpackage
directory, always run it from the repo root.

Tests are split into unit tests (`npm test`, fast, no external services
required) and integration tests (`npm run test:integration`, requires a
local Postgres and Redis, started via `docker-compose up -d`). CI runs both;
locally, most contributors only run unit tests during development and let
CI catch integration failures.

The `worker/` package polls a queue and has no HTTP server, so `npm run
dev` in that directory starts a polling loop with verbose logging instead
of a dev server -- this trips up contributors coming from `api/`, where
`npm run dev` does start a server on port 3000.
"""
[items.meta]
reference_notes = "Key facts: (1) three packages (core/api/worker) in one npm-workspaces monorepo, install only from repo root; (2) two test tiers -- unit tests need nothing extra, integration tests need Postgres+Redis via docker-compose; (3) most contributors run only unit tests locally, CI covers integration; (4) worker's `npm run dev` behaves differently from api's -- it polls with logging instead of serving HTTP, which is the excerpt's explicit 'trips up contributors' warning and should be called out."

[[items]]
id = "policy-excerpt"
prompt = """
Summarize the following internal policy excerpt for an engineer who just
joined the on-call rotation:

## Incident severity and paging policy

Sev1 (full outage or data loss risk): page immediately, 24/7, no exceptions.
Acknowledge within 5 minutes or escalation pages the secondary on-call.

Sev2 (significant degradation, one region or one major feature down):
page during business hours (9am-6pm local); outside those hours, post to
#incidents and page only if it's still unresolved after 30 minutes.

Sev3 (minor/cosmetic issue, workaround exists): no paging; file a ticket
and mention it in the next team standup.

Anyone -- not just on-call -- may declare a Sev1 if they believe one is
occurring; a declared Sev1 that turns out to be a false alarm is never held
against the person who declared it. The opposite failure (staying quiet
about a real Sev1) is the one this policy is designed to prevent.
"""
[items.meta]
reference_notes = "Key facts: (1) three severities with different paging rules -- Sev1 always pages immediately with 5-min ack SLA and secondary escalation; Sev2 pages during business hours only, otherwise waits 30 min after posting to #incidents; Sev3 never pages, just a ticket; (2) anyone can declare Sev1, not just on-call; (3) the policy explicitly protects false-alarm Sev1 declarations from blame, and explicitly states the goal is to avoid under-reporting real incidents -- this intent is worth including since it explains the other rules."

[[items]]
id = "api-doc-excerpt"
prompt = """
Summarize the following API documentation excerpt for someone about to
integrate against this endpoint:

## POST /v2/payments

Idempotent via the required `Idempotency-Key` header: retrying the same
request with the same key within 24 hours returns the original response
without creating a duplicate payment. Keys are scoped per API key -- two
different callers can reuse the same idempotency key value without
colliding.

Requests are processed asynchronously: a successful call returns `202
Accepted` with a `status: "pending"` payment object, not a final result.
Poll `GET /v2/payments/{id}` or subscribe to the `payment.completed`
webhook to learn the final status (`succeeded`, `failed`, or
`requires_action` for 3D Secure flows).

Rate limit: 100 requests/minute per API key, returned as `429` with a
`Retry-After` header in seconds when exceeded. This limit applies to this
endpoint specifically and is separate from the account-wide rate limit
documented elsewhere.
"""
[items.meta]
reference_notes = "Key facts: (1) idempotency via required Idempotency-Key header, scoped per API key, 24-hour window; (2) the endpoint is async -- 202 + pending status is returned immediately, final status comes via polling GET /v2/payments/{id} or the payment.completed webhook, with three possible final states including a 3D-Secure case; (3) endpoint-specific rate limit of 100 req/min with 429 + Retry-After, explicitly separate from the account-wide limit. A summary that says '202 means success' is a factual error worth penalizing under factual_accuracy."

[[items]]
id = "design-doc-excerpt"
prompt = """
Summarize the following design-doc excerpt for a reviewer who has 5
minutes before a review meeting:

## Problem

Our search index rebuild currently takes 6 hours and locks writes for the
full duration, which means every rebuild is a maintenance-window event. As
data volume grows, this window keeps getting longer and is now colliding
with usage in other timezones.

## Proposed approach

Build the new index in the background (`_v2` suffix) while the old index
(`_v1`) continues serving reads and accepting writes. Once the `_v2` build
finishes, do an atomic alias swap so `search` points to `_v2` instead of
`_v1`, then decommission `_v1` after a 24-hour safety window (in case the
swap needs to be reverted). Writes during the build are dual-written to
both indices to keep `_v2` current by the time it's ready to swap.

## Risks

Dual-writing doubles write load on the indexing service during the build
window (estimated 6 hours), which could saturate it under peak traffic;
mitigation is to throttle non-critical background reindex jobs during that
window. There's also a data-consistency risk if a write to `_v2` fails
silently during dual-write -- we need write-failure alerting specifically
on the `_v2` path, not just the aggregate write-failure rate.
"""
[items.meta]
reference_notes = "Key facts: (1) problem is a 6-hour write-locking rebuild that's outgrowing its maintenance window; (2) proposed fix is blue-green style: build _v2 in background while _v1 serves live, dual-write during the build, atomic alias swap at the end, 24h safety window before decommissioning _v1; (3) two named risks: doubled write load during the ~6h dual-write window (mitigated by throttling background jobs) and silent dual-write failure risk to _v2 specifically requiring dedicated alerting. Coverage should include both the mechanism (blue-green + dual-write + alias swap) and at least the write-load risk, not just the happy-path description."

[[items]]
id = "onboarding-excerpt"
prompt = """
Summarize the following excerpt for a new hire's first-week reading:

## How we handle production access

Nobody has standing production database access, including senior
engineers and the CTO. Access is granted per-incident or per-approved-task
through a break-glass tool that requires a second approver and
auto-revokes after 4 hours. Every grant and every query run during a grant
is logged and reviewed weekly by the security team, not just at grant time.

Read replicas are the exception: any engineer can query a read replica at
any time with no approval, since replicas contain no write path and are
already 5-15 minutes stale by design, making them unsuitable for anything
that needs current or mutable data anyway.

The one deliberate gap in this policy is local development: engineers run
production-shaped (not production-copy) seed data locally, and there is
currently no mechanism preventing an engineer from manually copying
production data into a local environment -- this is called out explicitly
in the doc as a known gap under active discussion, not an oversight.
"""
[items.meta]
reference_notes = "Key facts: (1) no standing prod DB access for anyone, including leadership; break-glass access needs a second approver, auto-revokes in 4 hours, and is logged + reviewed weekly (not just at grant time); (2) read replicas are a no-approval exception because they're inherently unsuitable for anything needing current/mutable data (5-15 min staleness, no write path); (3) the doc explicitly names a known policy gap around local dev seed data being production-shaped with no technical control preventing manual copying of real prod data -- and explicitly says this is a known open gap, not an oversight, which is worth preserving since dropping that framing would misrepresent the doc's own stance."
```

```toml
# benchmarks/tasks/eval/doc_summary/meta.toml
[meta]
intent = "Six excerpts across domains (changelog, README, on-call policy, API docs, design doc, security/onboarding doc) of varying structure (bulleted vs. prose vs. mixed), each with 2-4 specific facts a summary must preserve to be useful and at least one fact that's easy to drop or garble if skimmed."
```

- [ ] **Step 4: Run to verify it passes**

Run: `uv run pytest tests/benchmark/eval/test_content_doc_summary.py -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add benchmarks/tasks/eval/doc_summary/ tests/benchmark/eval/test_content_doc_summary.py
git commit -m "content(eval): author the doc_summary category

Six excerpts (changelog, README, on-call policy, API docs, design doc,
onboarding/security doc) with fact checklists in reference_notes.
completes plan item #16 of docs/superpowers/plans/2026-09-17-capability-eval-benchmark.md"
```

---

## Task 17: Configure the `coding` category

**Files:**
- Create: `benchmarks/tasks/eval/coding/items.toml`
- Create: `benchmarks/tasks/eval/coding/meta.toml`
- Test: `tests/benchmark/eval/test_content_coding.py`

- [ ] **Step 1: Write the failing smoke test**

```python
# tests/benchmark/eval/test_content_coding.py
"""Smoke test for the coding category's EvalPlus config (no rubric — see
plan Task 6/12 for how it's graded)."""

from pathlib import Path

from modelman.benchmark.eval.category import CODING_CATEGORY, load_category

CATEGORY_ROOT = Path(__file__).parent.parent.parent.parent / "benchmarks" / "tasks" / "eval"


def test_coding_category_loads_with_dataset_and_limit():
    category = load_category(CATEGORY_ROOT / "coding")
    assert category.name == CODING_CATEGORY
    assert category.rubric is None
    assert category.coding_dataset in ("humaneval", "mbpp")
    assert category.coding_limit is not None and category.coding_limit > 0
```

- [ ] **Step 2: Run to verify it fails**

Run: `uv run pytest tests/benchmark/eval/test_content_coding.py -v`
Expected: FAIL

- [ ] **Step 3: Write the config**

```toml
# benchmarks/tasks/eval/coding/items.toml
# EvalPlus dataset config for the coding category — no hand-authored items,
# no rubric/judge. `limit` keeps a multi-model x multi-engine sweep
# tractable; a suite's own [coding] block can override this per run (see
# eval/suite.py's CodingConfig).
dataset = "humaneval"
limit = 40
```

```toml
# benchmarks/tasks/eval/coding/meta.toml
[meta]
intent = "Graded entirely by EvalPlus's HumanEval+ pass@1, not the in-house judge — see docs/superpowers/specs/2026-09-17-capability-eval-benchmark-design.md's Content plan table for why coding is the one category with a real community-maintained grader instead of a hand-authored rubric."
```

- [ ] **Step 4: Run to verify it passes**

Run: `uv run pytest tests/benchmark/eval/test_content_coding.py -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add benchmarks/tasks/eval/coding/ tests/benchmark/eval/test_content_coding.py
git commit -m "content(eval): configure the coding category (EvalPlus, no rubric)

completes plan item #17 of docs/superpowers/plans/2026-09-17-capability-eval-benchmark.md"
```

---

## Task 18: Author a real suite and dry-run it

**Files:**
- Create: `benchmarks/suites/eval-sweep.toml`

**No new test** — this task's verification is a real `--dry-run` invocation against real registry models, the same kind of manual verification `05-benchmarks.md`/`09-agent-benchmarks.md` already document.

- [ ] **Step 1: Write the suite**

```toml
# benchmarks/suites/eval-sweep.toml
name = "eval-sweep"
cooldown_s = 15.0

[judge]
model = "anthropic/claude-opus-5"
temperature = 0.0
samples = 3
max_attempts = 2
route = "openrouter"

[coding]
dataset = "humaneval"
limit = 40

[routes.direct.omlx]
base_url = "http://localhost:8000/v1"

[[rows]]
model = "ollama/qwen3.8:27b-mlx"
route = "litellm"

[[rows]]
model = "omlx/mlx-community--Qwen3.8-27B-4bit"
provider = "omlx"
route = "direct"
direct_model = "Qwen3.8-27B-4bit"

[[rows]]
model = "ollama/glm-5.3-flash:cloud"
route = "litellm"
categories = ["coding", "reasoning"]
```

- [ ] **Step 2: Dry-run it against the real registry**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup/modelman
uv run modelman benchmark eval run --suite ../benchmarks/suites/eval-sweep.toml \
    --root ../benchmarks/tasks/eval --dry-run
```

Expected: prints 3 resolved rows (labels, models, routes, per-row categories — the third row showing only `["coding", "reasoning"]`), then "3 row(s), 5 categorie(s) resolved, dry run — nothing executed". If any model id 404s against the live registry (a model since renamed/removed), fix the suite's model ids to match `uv run modelman benchmark eval run --suite ... --dry-run`'s error before proceeding — don't guess a replacement id without checking `registry.toml`.

- [ ] **Step 3: Commit**

```bash
git add benchmarks/suites/eval-sweep.toml
git commit -m "content(eval): add the eval-sweep suite

Dry-run verified live against the current registry (plan Task 18).
completes plan item #18 of docs/superpowers/plans/2026-09-17-capability-eval-benchmark.md"
```

---

## Task 19: Guide doc

**Files:**
- Create: `docs/guides/11-capability-eval-benchmark.md`
- Modify: `docs/guides/00-config-map.md` (only if this guide needs a link added to its "Going deeper"-style index — check whether 00 lists all guides; if so add a line for 11, otherwise skip)

- [ ] **Step 1: Check whether guide 00 (or the root README/CLAUDE.md) indexes every guide by number**

Run: `grep -rn "09-agent-benchmarks\|10-mlx-lm-quantization" docs/ README.md CLAUDE.md 2>/dev/null`

If this finds a list that should include the new guide, add one line for `11-capability-eval-benchmark.md` there, matching the existing line format exactly.

- [ ] **Step 2: Write the guide**

```markdown
# Cross-category capability benchmark — `modelman benchmark eval`

> Use this to: score local models on reasoning/planning/coding/code_review/doc_summary,
> so a model/quant/engine choice can be made per use case instead of on
> throughput alone.

Design rationale: `docs/superpowers/specs/2026-09-17-capability-eval-benchmark-design.md`.

## Prerequisites

- Everything in [05-benchmarks](05-benchmarks.md)'s Prerequisites (no other local model loaded, backends healthy, isolation helpers on PATH).
- A working LiteLLM apiKey seeded into `~/.pi/agent/models.json` for any `route = "litellm"` row or judge — same requirement as [09-agent-benchmarks](09-agent-benchmarks.md).
- `OPENROUTER_API_KEY` available if the suite's `[judge]` or any row uses `route = "openrouter"`.
- `uv sync --extra eval` from `modelman/` — the `coding` category needs EvalPlus, which is not installed by plain `make install`.

## TL;DR

```bash
# from: /Users/keith/github/ohanaverse/local-ai-setup/modelman
uv run modelman benchmark eval list-categories --root ../benchmarks/tasks/eval
uv run modelman benchmark eval list-items --category doc_summary --root ../benchmarks/tasks/eval
uv run modelman benchmark eval run --suite ../benchmarks/suites/eval-sweep.toml \
    --root ../benchmarks/tasks/eval --dry-run
```

## Steps

### 1. Pick or author categories

A category lives under `benchmarks/tasks/eval/<name>/` — `items.toml` (the fixed prompt set) plus `rubric.toml`/`rubric.md` (except `coding`, which is EvalPlus-graded and has no rubric). Five categories ship today: `reasoning`, `planning`, `coding`, `code_review`, `doc_summary`. A rubric's `[dimensions]` must sum to 100.

### 2. Write or reuse a suite

A suite (`benchmarks/suites/eval-*.toml`) picks a `[judge]`, an optional `[coding]` override (dataset/limit), and a `[[rows]]` list (model + route, optionally a per-row `categories =` narrowing). `eval-sweep.toml` is the reference suite. Always `--dry-run` a new/edited suite first.

### 3. Run it

```bash
uv run modelman benchmark eval run --suite <path> --root ../benchmarks/tasks/eval \
    [--category <name>]... [--row <label-or-index>]...
```

There is no `--skip-judge` flag here (unlike `modelman benchmark agent run`): `coding` has no judge to skip, and the four judged categories always judge. Rows are grouped by provider and isolated once per group, same as `modelman benchmark`/`modelman benchmark agent` — see [05-benchmarks](05-benchmarks.md) Step 1.

### 4. Read the report

```bash
uv run modelman benchmark eval show --latest
```

`summary.md` has three tables: **Capability matrix** (rows grouped by registry family so quant/engine variants of the same base model sit adjacent, one column per category, composite /100 or EvalPlus pass@1%), **Per-category leaderboard** (top rows per category — the direct "best model for X" answer), and **Anomalies** (`JUDGE_FAIL`, isolation errors, EvalPlus execution errors).

### 5. Re-judge cheaply after a rubric edit

```bash
uv run modelman benchmark eval judge --latest --root ../benchmarks/tasks/eval [--row <row-dir>]...
```

Re-scores every judged category's items from their persisted `response.txt` — no regeneration, no isolation. Coding has nothing to re-judge (EvalPlus's pass@1 is deterministic).

## Gotchas

- **`coding` needs `uv sync --extra eval`** — a plain `make install`/`uv sync` does not pull EvalPlus, since it executes model-generated code and only this one category needs it.
- **`omlx` 4-bit/6-bit disambiguation is the same as every other suite here** — use a row's `provider =` override, and `direct_model =` for the server-side basename. See [05-benchmarks](05-benchmarks.md) Gotchas.
- **A row's `categories =` narrows which categories that row runs**, independent of `--category`'s CLI-level narrowing — both apply, intersected.
- **Judging (and coding's EvalPlus grading) both cost real time/spend per item** — a 5-category sweep across many rows adds up; use `--category`/`--row` to scope a suite down while iterating on content or rubrics.

## Going deeper

- Design spec: `docs/superpowers/specs/2026-09-17-capability-eval-benchmark-design.md`
- Module map: `modelman/CLAUDE.md`
- Source: `modelman/src/modelman/benchmark/eval/`, `modelman/src/modelman/benchmark/judge_core.py`
- Speed-only local benchmarks: [05-benchmarks](05-benchmarks.md)
- Agentic repo-level coding benchmark: [09-agent-benchmarks](09-agent-benchmarks.md)
```

- [ ] **Step 3: Validate links**

Run: `uv run bin/check-links` (from repo root)
Expected: no broken links reported for the new guide

- [ ] **Step 4: Commit**

```bash
git add docs/guides/11-capability-eval-benchmark.md
git commit -m "docs: add the capability eval benchmark guide

completes plan item #19 of docs/superpowers/plans/2026-09-17-capability-eval-benchmark.md"
```

---

## Task 20: Full verification pass

**Files:** none (verification only)

- [ ] **Step 1: Full modelman test suite**

Run: `cd modelman && uv run pytest -q`
Expected: all PASS, including every test added in Tasks 1-17

- [ ] **Step 2: Lint**

Run: `uv run ruff check . && uv run ruff format --check .`
Expected: clean

- [ ] **Step 3: Types**

Run: `uv run mypy src`
Expected: clean (fix any new-code type errors before proceeding — do not add `# type: ignore` without a specific reason)

- [ ] **Step 4: Repo-wide checks**

Run: `cd .. && make check-links && make lint-shell`
Expected: clean (these don't touch Python but confirm the new guide/doc links and no shell files were accidentally broken)

- [ ] **Step 5: Manual end-to-end smoke (documented, not scripted)**

```bash
cd modelman
uv sync --extra eval
uv run modelman provider isolate ollama
uv run modelman benchmark eval run --suite ../benchmarks/suites/eval-sweep.toml \
    --root ../benchmarks/tasks/eval --row 01 --category doc_summary
uv run modelman benchmark eval show --latest
uv run modelman provider restore
```

Expected: one row, one category, a `summary.md` with a real doc_summary score for `ollama/qwen3.8:27b-mlx`. This is the first real end-to-end proof the whole pipeline (isolation -> generation -> judge -> report) works together, not just each unit in isolation — record the result here or in `benchmarks/results/` if you want it kept.

- [ ] **Step 6: Final commit if Steps 1-4 required any fixes**

```bash
git add -A
git commit -m "fix(modelman): address full verification pass findings

completes plan item #20 of docs/superpowers/plans/2026-09-17-capability-eval-benchmark.md"
```

(Skip this commit if Steps 1-4 were already clean.)
