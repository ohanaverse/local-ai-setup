# Agent coding benchmark — Phase 5: Judge (plan extract)

> Part of [the agent-coding-benchmark plan index](2026-09-04-agent-coding-benchmark.md). Shared Goal / Architecture / Tech Stack / Global Constraints and the phase order live there.


## Phase 5: Judge

### Task 16: Judge core — anonymization, prompt, contract validation, retries

**Files:**
- Create: `modelman/src/modelman/benchmark/agent/judge.py`
- Test: `modelman/tests/benchmark/agent/test_judge.py`

**Interfaces:**
- Produces: `DIMENSIONS`, `MAX_POINTS`, `VERDICTS`; `JudgeContractError`, `JudgeTransportError` (exceptions); `JudgeTransport` (Protocol: `complete(self, prompt: str, *, temperature: float) -> str`); `JudgeScore` (`scores, total, verdict, flags, rationale, raw_text`); `JudgeOutcome` (`status: "scored"|"judge_fail", samples, combined, attempts_used`); `anonymize_diff(diff_text, max_chars=20000) -> str`; `anonymize_message(message) -> str`; `build_prompt(task_md, seed_contents, diff_text, closing_message, rubric_md) -> str`; `parse_response(raw_text) -> JudgeScore`; `judge_row(transport, prompt, *, temperature, samples, max_attempts) -> JudgeOutcome`; `apply_cap(rubric_total, cap) -> int`; `detect_overclaim(closing_message, hidden_pass, hidden_total) -> bool`. Task 17 adds `LiteLLMJudgeTransport` (implements the `JudgeTransport` protocol) to this same module; Task 18 wires `judge_row`/`apply_cap`/`detect_overclaim` into `runner.py`.

A transport implementation signals a network/API failure by raising `JudgeTransportError`, never a bare `requests` exception — `judge_row`'s retry loop only knows about `JudgeContractError` (malformed JSON) and `JudgeTransportError` (transport failed after its own internal retry); this keeps the pure logic in this task decoupled from `requests` entirely, and Task 17's fake-transport tests can simulate a persistent network failure without mocking HTTP.

- [ ] **Step 1: Write the failing test**

`modelman/tests/benchmark/agent/test_judge.py`:
```python
"""Tests for modelman.benchmark.agent.judge — the blind LLM rubric judge.

The rubric score must stay independent of the gates (spec: "you do not
know whether this code passes tests; do not speculate" is what keeps
gate-vs-judge disagreement informative), so build_prompt is tested to
prove gate/hidden-test data never appears in what's sent to the judge —
not just that scores parse correctly.
"""

import json

import pytest

from modelman.benchmark.agent.judge import (
    DIMENSIONS,
    JudgeContractError,
    JudgeOutcome,
    JudgeScore,
    JudgeTransportError,
    anonymize_diff,
    anonymize_message,
    apply_cap,
    build_prompt,
    detect_overclaim,
    judge_row,
    parse_response,
)

VALID_RESPONSE = json.dumps(
    {
        "scores": {"root_cause": 25, "approach": 20, "test_quality": 15, "scope": 12, "coherence": 8},
        "total": 80,
        "verdict": "principled_fix",
        "flags": [],
        "rationale": "Looks correct.",
    }
)


def test_anonymize_diff_strips_temp_workspace_paths():
    diff = "diff --git a/tmp/agent-bench-xyz/kettlecomb/billing.py b/tmp/agent-bench-xyz/kettlecomb/billing.py\n--- a/tmp/agent-bench-xyz/kettlecomb/billing.py\n+++ b/tmp/agent-bench-xyz/kettlecomb/billing.py\n"
    anon = anonymize_diff(diff)
    assert "agent-bench-xyz" not in anon
    assert "diff --git a/<file> b/<file>" in anon


def test_anonymize_diff_truncates_oversized_diffs():
    diff = "x" * 100
    anon = anonymize_diff(diff, max_chars=10)
    assert anon.endswith("[TRUNCATED]\n")
    assert len(anon) < 100


def test_anonymize_message_redacts_model_provider_tokens():
    message = "Fixed using ollama/qwen3.8:27b-mlx, session_id: abc123"
    anon = anonymize_message(message)
    assert "qwen3.8" not in anon
    assert "abc123" not in anon


def test_build_prompt_never_includes_gate_or_hidden_test_data():
    """The judge must be blind to gate results — build_prompt's signature
    has no parameter for them, so this test asserts the invariant at the
    boundary: nothing gate/hidden-shaped ever appears in the rendered text."""
    prompt = build_prompt(
        task_md="Fix the bug.",
        seed_contents={"billing.py": "def prorate(): ..."},
        diff_text="diff --git a/<file> b/<file>\n+fixed",
        closing_message="Done, all tests pass.",
        rubric_md="score this",
    )
    assert "HIDDEN_TESTS_FAILED" not in prompt
    assert "gate" not in prompt.lower()
    assert "do not speculate" in prompt.lower()


def test_parse_response_accepts_valid_contract():
    score = parse_response(VALID_RESPONSE)
    assert score.total == 80
    assert score.verdict == "principled_fix"
    assert set(score.scores) == set(DIMENSIONS)


def test_parse_response_rejects_malformed_json():
    with pytest.raises(JudgeContractError):
        parse_response("not json at all")


def test_parse_response_rejects_missing_dimension():
    bad = json.dumps({"scores": {"root_cause": 10}, "total": 10, "verdict": "partial"})
    with pytest.raises(JudgeContractError, match="approach"):
        parse_response(bad)


def test_parse_response_rejects_unknown_verdict():
    bad = json.loads(VALID_RESPONSE)
    bad["verdict"] = "great_job"
    with pytest.raises(JudgeContractError, match="verdict"):
        parse_response(json.dumps(bad))


class _FakeTransport:
    def __init__(self, responses: list[str | Exception]):
        self.responses = list(responses)
        self.calls = 0

    def complete(self, prompt: str, *, temperature: float) -> str:
        self.calls += 1
        item = self.responses.pop(0)
        if isinstance(item, Exception):
            raise item
        return item


def test_judge_row_succeeds_on_first_valid_response():
    outcome = judge_row(_FakeTransport([VALID_RESPONSE]), "prompt", temperature=0.0, samples=1, max_attempts=2)
    assert outcome.status == "scored"
    assert outcome.combined.total == 80
    assert outcome.attempts_used == 1


def test_judge_row_retries_malformed_json_once_then_succeeds():
    transport = _FakeTransport(["not json", VALID_RESPONSE])
    outcome = judge_row(transport, "prompt", temperature=0.0, samples=1, max_attempts=2)
    assert outcome.status == "scored"
    assert outcome.attempts_used == 2


def test_judge_row_gives_up_after_max_attempts():
    transport = _FakeTransport(["not json", "still not json"])
    outcome = judge_row(transport, "prompt", temperature=0.0, samples=1, max_attempts=2)
    assert outcome.status == "judge_fail"
    assert outcome.combined is None


def test_judge_row_treats_transport_error_like_a_failed_attempt():
    transport = _FakeTransport([JudgeTransportError("network down"), VALID_RESPONSE])
    outcome = judge_row(transport, "prompt", temperature=0.0, samples=1, max_attempts=2)
    assert outcome.status == "scored"


def test_judge_row_samples_greater_than_one_takes_median_per_dimension():
    low = json.loads(VALID_RESPONSE)
    low["scores"] = {"root_cause": 10, "approach": 10, "test_quality": 10, "scope": 10, "coherence": 4}
    high = json.loads(VALID_RESPONSE)
    high["scores"] = {"root_cause": 30, "approach": 20, "test_quality": 18, "scope": 14, "coherence": 8}
    transport = _FakeTransport([json.dumps(low), VALID_RESPONSE, json.dumps(high)])
    outcome = judge_row(transport, "prompt", temperature=0.0, samples=3, max_attempts=1)
    assert outcome.status == "scored"
    assert outcome.combined.scores["root_cause"] == 25  # median of 10, 25, 30


def test_apply_cap_rounds_to_nearest_int():
    assert apply_cap(80, 0.70) == 56
    assert apply_cap(62, 0.25) == 16  # round(15.5) -> 16 (banker's rounding lands here for .5 cases in general; exact value pinned by this test)


def test_detect_overclaim_when_agent_claims_pass_but_hidden_tests_failed():
    assert detect_overclaim("All tests pass!", hidden_pass=3, hidden_total=6) is True


def test_detect_overclaim_false_when_claim_matches_reality():
    assert detect_overclaim("All tests pass!", hidden_pass=6, hidden_total=6) is False


def test_detect_overclaim_false_when_no_claim_made():
    assert detect_overclaim("I fixed the calendar arithmetic.", hidden_pass=0, hidden_total=6) is False
```

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/benchmark/agent/test_judge.py -v`
Expected: FAIL with `ModuleNotFoundError`

- [ ] **Step 3: Write the implementation**

`modelman/src/modelman/benchmark/agent/judge.py`:
```python
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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `uv run pytest tests/benchmark/agent/test_judge.py -v`
Expected: PASS (17 tests)

- [ ] **Step 5: Commit**

```bash
git add modelman/src/modelman/benchmark/agent/judge.py \
        modelman/tests/benchmark/agent/test_judge.py
git commit -m "feat(agent-bench): add judge contract, anonymization, retries - completes plan item #16"
```

### Task 17: LiteLLM HTTP transport with one retry on transport failure

**Files:**
- Modify: `modelman/src/modelman/benchmark/agent/judge.py`
- Modify: `modelman/tests/benchmark/agent/test_judge.py`

**Interfaces:**
- Produces: `LiteLLMJudgeTransport(base_url, api_key, model, *, retry_backoff_s=2.0)` implementing `JudgeTransport.complete`. `runner.py` (Task 18) constructs this for the judge phase.

Reuses the `requests` plumbing already in `benchmark/workloads/base.py`'s pattern (direct `requests` calls, no session pooling needed for one request per judge call) rather than a pi subprocess — pi exposes no temperature control, and a fresh HTTP request gives the same context isolation a fresh process would, more cheaply.

- [ ] **Step 1: Write the failing test**

Append to `modelman/tests/benchmark/agent/test_judge.py`:
```python
import requests

import modelman.benchmark.agent.judge as judge_module
from modelman.benchmark.agent.judge import LiteLLMJudgeTransport


class _FakeResponse:
    def __init__(self, status=200, payload=None):
        self.status_code = status
        self._payload = payload or {"choices": [{"message": {"content": "hi"}}]}

    def raise_for_status(self):
        if self.status_code >= 400:
            raise requests.HTTPError(f"status {self.status_code}")

    def json(self):
        return self._payload


def test_litellm_transport_posts_expected_payload(monkeypatch):
    captured = {}

    def fake_post(url, headers=None, json=None, timeout=None):
        captured["url"] = url
        captured["headers"] = headers
        captured["json"] = json
        return _FakeResponse(payload={"choices": [{"message": {"content": '{"total": 1}'}}]})

    monkeypatch.setattr(judge_module.requests, "post", fake_post)
    transport = LiteLLMJudgeTransport(
        base_url="http://localhost:4000/v1", api_key="sk-test", model="openrouter/anthropic/claude-opus-4"
    )
    result = transport.complete("prompt text", temperature=0.0)

    assert result == '{"total": 1}'
    assert captured["url"] == "http://localhost:4000/v1/chat/completions"
    assert captured["headers"]["Authorization"] == "Bearer sk-test"
    assert captured["json"]["model"] == "openrouter/anthropic/claude-opus-4"
    assert captured["json"]["temperature"] == 0.0


def test_litellm_transport_retries_once_then_succeeds(monkeypatch):
    calls = {"n": 0}

    def flaky_post(url, headers=None, json=None, timeout=None):
        calls["n"] += 1
        if calls["n"] == 1:
            raise requests.ConnectionError("boom")
        return _FakeResponse(payload={"choices": [{"message": {"content": "ok"}}]})

    monkeypatch.setattr(judge_module.requests, "post", flaky_post)
    transport = LiteLLMJudgeTransport(base_url="http://localhost:4000/v1", api_key="k", model="m", retry_backoff_s=0.0)
    assert transport.complete("p", temperature=0.0) == "ok"
    assert calls["n"] == 2


def test_litellm_transport_raises_judge_transport_error_after_retry_exhausted(monkeypatch):
    def always_fails(url, headers=None, json=None, timeout=None):
        raise requests.ConnectionError("still down")

    monkeypatch.setattr(judge_module.requests, "post", always_fails)
    transport = LiteLLMJudgeTransport(base_url="http://localhost:4000/v1", api_key="k", model="m", retry_backoff_s=0.0)
    with pytest.raises(JudgeTransportError):
        transport.complete("p", temperature=0.0)
```

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/benchmark/agent/test_judge.py -v`
Expected: FAIL — `LiteLLMJudgeTransport` doesn't exist yet

- [ ] **Step 3: Write the implementation**

Add `import time` and `import requests` to the top of `modelman/src/modelman/benchmark/agent/judge.py`, then append:
```python
class LiteLLMJudgeTransport:
    """Direct chat-completions call through LiteLLM at a fixed temperature.
    Not a pi subprocess: pi has no temperature flag, and a fresh HTTP
    request gives the same context isolation a fresh process would, more
    cheaply."""

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
        response.raise_for_status()
        data = response.json()
        return data["choices"][0]["message"]["content"]
```

- [ ] **Step 4: Run test to verify it passes**

Run: `uv run pytest tests/benchmark/agent/test_judge.py -v`
Expected: PASS (20 tests)

- [ ] **Step 5: Commit**

```bash
git add modelman/src/modelman/benchmark/agent/judge.py \
        modelman/tests/benchmark/agent/test_judge.py
git commit -m "feat(agent-bench): add LiteLLM judge transport with retry - completes plan item #17"
```

### Task 18: Wire judging into the runner (phase 2) + `--skip-judge`

**Files:**
- Modify: `modelman/src/modelman/benchmark/agent/runner.py`
- Modify: `modelman/tests/benchmark/agent/test_runner.py`
- Modify: `modelman/src/modelman/benchmark/agent/cli.py`
- Modify: `modelman/tests/benchmark/agent/test_cli.py`

**Interfaces:**
- Consumes: everything in `judge.py` (Tasks 16–17).
- Produces: `RowRunResult` gains `seed_contents: dict[str, str]`, `closing_message: str`, `judge: JudgeOutcome | None`, `composite: int | None`; `run_suite` gains `skip_judge: bool = False` and `judge_transport_factory: Callable[[JudgeConfig, Path], JudgeTransport] | None = None` (defaults to a real `LiteLLMJudgeTransport` builder — the injection point is what makes this task's tests hermetic). `report.py` (Task 22) reads all four new `RowRunResult` fields by these exact names.

Judging runs **after** `isolation.restore_providers()` on purpose (spec): the judge is a cloud model and must not contend with a loaded local model, and its latency must stay off the measured clock. The `agent judge --run-id/--latest` CLI subcommand that re-judges **persisted** artifacts without re-running any agent is deferred to Phase 6 (Task 24) — it needs `report.py`'s on-disk format to read back from, which doesn't exist yet.

- [ ] **Step 1: Write the failing tests**

Append to `modelman/tests/benchmark/agent/test_runner.py` (add `import json` to the existing import block):
```python
def test_run_suite_judges_rows_after_restore_and_sets_composite(tmp_path, monkeypatch):
    """Judging happens after restore_providers, on every row with gates
    evaluated, and the composite is rubric_total x cap (spec Scoring)."""
    order = []
    monkeypatch.setattr(isolation_module, "isolate_provider", lambda pid: order.append(("isolate", pid)))
    monkeypatch.setattr(isolation_module, "restore_providers", lambda: order.append(("restore",)))
    monkeypatch.setattr(pidriver_module, "run_pi_process", _no_diff_run)

    class _FakeJudgeTransport:
        def complete(self, prompt, *, temperature):
            order.append(("judge",))
            return json.dumps(
                {
                    "scores": {"root_cause": 30, "approach": 25, "test_quality": 20, "scope": 15, "coherence": 10},
                    "total": 100,
                    "verdict": "principled_fix",
                    "flags": [],
                    "rationale": "ok",
                }
            )

    suite = load_suite(_write_suite(tmp_path, _suite_toml(MINI_DRIFT)), _registry())
    run_dir, results = run_suite(
        suite,
        _registry(),
        results_dir=tmp_path / "results",
        live_models_path=tmp_path / "missing.json",
        judge_transport_factory=lambda judge_cfg, path: _FakeJudgeTransport(),
    )

    assert order == [("isolate", "ollama"), ("restore",), ("judge",)]
    assert results[0].judge is not None
    assert results[0].judge.status == "scored"
    # This row is NO_DIFF (cap x0.00), so the composite is 0 regardless of
    # the (fake, maximal) rubric score — proving the cap is actually applied.
    assert results[0].composite == 0


def test_run_suite_skip_judge_leaves_composite_none(tmp_path, monkeypatch):
    monkeypatch.setattr(isolation_module, "isolate_provider", lambda pid: None)
    monkeypatch.setattr(isolation_module, "restore_providers", lambda: None)
    monkeypatch.setattr(pidriver_module, "run_pi_process", _no_diff_run)

    suite = load_suite(_write_suite(tmp_path, _suite_toml(MINI_DRIFT)), _registry())
    run_dir, results = run_suite(
        suite,
        _registry(),
        results_dir=tmp_path / "results",
        live_models_path=tmp_path / "missing.json",
        skip_judge=True,
    )
    assert results[0].judge is None
    assert results[0].composite is None
```

Append to `modelman/tests/benchmark/agent/test_cli.py`:
```python
def test_run_passes_skip_judge_through_to_run_suite(tmp_path, monkeypatch):
    captured = {}

    def _fake_run_suite(suite_obj, registry_obj, **kwargs):
        captured.update(kwargs)
        return tmp_path / "results" / "run1", []

    monkeypatch.setattr(cli_module, "load_registry", lambda: _registry())
    monkeypatch.setattr(cli_module, "run_suite", _fake_run_suite)
    monkeypatch.setenv("MODELMAN_STATE", str(tmp_path / "modelman.toml"))

    suite_path = tmp_path / "suite.toml"
    suite_path.write_text(
        """
name = "s"
task = "some/task"

[judge]
model = "x"
thinking = "low"
temperature = 0.0
samples = 1
max_attempts = 2
route = "litellm"

[[rows]]
models = ["ollama/a"]
thinking = ["off"]
routes = ["direct"]
""",
        encoding="utf-8",
    )
    result = runner.invoke(agent_app, ["run", "--suite", str(suite_path), "--skip-judge"])
    assert result.exit_code == 0
    assert captured["skip_judge"] is True
```

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/benchmark/agent/test_runner.py tests/benchmark/agent/test_cli.py -v`
Expected: FAIL — `run_suite` doesn't accept `judge_transport_factory`/`skip_judge` yet, and `run_cmd` has no `--skip-judge` flag

- [ ] **Step 3: Extend `runner.py`**

Add these imports to `modelman/src/modelman/benchmark/agent/runner.py` (alongside the existing ones): `import json`, `from collections.abc import Callable`, `from modelman.benchmark.agent import judge`, `from modelman.benchmark.agent.suite import JudgeConfig` (add to the existing `from modelman.benchmark.agent.suite import ...` line).

Change the `RowRunResult` dataclass to:
```python
@dataclass
class RowRunResult:
    row: RowConfig
    pass_number: int
    row_dir: Path
    gates: GatesReport | None
    metrics: pidriver.RowMetrics | None
    diff_raw: str
    seed_contents: dict[str, str] = field(default_factory=dict)
    closing_message: str = ""
    judge: judge.JudgeOutcome | None = None
    composite: int | None = None
    error: str | None = None
```
(add `field` to the existing `from dataclasses import dataclass` line: `from dataclasses import dataclass, field`)

In `_run_single_row`, replace:
```python
        diff_raw = workspace.diff()
        return RowRunResult(
            row=row, pass_number=pass_number, row_dir=row_dir, gates=gates, metrics=metrics, diff_raw=diff_raw
        )
```
with:
```python
        diff_raw = workspace.diff()
        touched = workspace.modified_or_deleted_since_baseline() + workspace.new_files_since_baseline()
        seed_contents = {}
        for path in touched:
            rel = str(path.relative_to(workspace.root))
            content = workspace.file_at_baseline(rel)
            if content is not None:
                seed_contents[rel] = content
        closing_message = _closing_message(run_result)
        return RowRunResult(
            row=row,
            pass_number=pass_number,
            row_dir=row_dir,
            gates=gates,
            metrics=metrics,
            diff_raw=diff_raw,
            seed_contents=seed_contents,
            closing_message=closing_message,
        )
```

Add this helper above `_run_single_row`:
```python
def _closing_message(run_result: pidriver.PiRunResult) -> str:
    """Concatenate text_delta content from the agent's final message only —
    resets on every message_start, so only the last assistant message's
    text survives."""
    parts: list[str] = []
    capturing = False
    for entry in run_result.events:
        ev = entry["event"]
        etype = ev.get("type")
        if etype == "message_start":
            parts = []
            capturing = True
        elif etype == "message_update" and capturing:
            delta = ev.get("delta", {})
            if delta.get("type") == "text_delta":
                parts.append(delta.get("text", ""))
        elif etype == "message_end":
            capturing = False
    return "".join(parts)


def _build_judge_transport(judge_cfg: JudgeConfig, live_models_path: Path) -> judge.JudgeTransport:
    try:
        live = json.loads(live_models_path.read_text(encoding="utf-8"))
    except (FileNotFoundError, json.JSONDecodeError):
        live = {}
    litellm_entry = live.get("providers", {}).get("litellm", {})
    api_key = litellm_entry.get("apiKey")
    if not api_key:
        raise BenchmarkError("no LiteLLM apiKey found in ~/.pi/agent/models.json for the judge transport")
    base_url = litellm_entry.get("baseUrl", "http://localhost:4000/v1")
    return judge.LiteLLMJudgeTransport(base_url=base_url, api_key=api_key, model=judge_cfg.model)


def _judge_all(
    suite: Suite, task: TaskBundle, results: list[RowRunResult], live_models_path: Path, factory
) -> None:
    transport = factory(suite.judge, live_models_path)
    for result in results:
        if result.error is not None or result.gates is None:
            continue
        prompt = judge.build_prompt(
            task_md=task.task_md,
            seed_contents=result.seed_contents,
            diff_text=judge.anonymize_diff(result.diff_raw),
            closing_message=result.closing_message,
            rubric_md=task.rubric_md,
        )
        outcome = judge.judge_row(
            transport,
            prompt,
            temperature=suite.judge.temperature,
            samples=suite.judge.samples,
            max_attempts=suite.judge.max_attempts,
        )
        result.judge = outcome
        result.composite = judge.apply_cap(outcome.combined.total, result.gates.cap) if outcome.status == "scored" else None
```

Change the `run_suite` signature and its tail to:
```python
def run_suite(
    suite: Suite,
    registry: Registry,
    *,
    row_filter: list[str] | None = None,
    results_dir: Path | None = None,
    live_models_path: Path = pidriver.LIVE_PI_MODELS_PATH,
    skip_judge: bool = False,
    judge_transport_factory: Callable[[JudgeConfig, Path], judge.JudgeTransport] | None = None,
) -> tuple[Path, list[RowRunResult]]:
```
and, replacing the final two lines (`isolation.restore_providers()` / `return run_dir, results`):
```python
    isolation.restore_providers()

    if not skip_judge:
        _judge_all(suite, task, results, live_models_path, judge_transport_factory or _build_judge_transport)

    return run_dir, results
```

- [ ] **Step 4: Extend `cli.py`**

In `modelman/src/modelman/benchmark/agent/cli.py`, add a `--skip-judge` option to `run_cmd` and pass it through:
```python
    skip_judge: bool = typer.Option(False, "--skip-judge", help="Skip the judge phase"),
```
(add as a parameter, and change the `run_suite(...)` call to `run_suite(loaded_suite, registry, row_filter=row or None, results_dir=results_dir, skip_judge=skip_judge)`).

- [ ] **Step 5: Run test to verify it passes**

Run: `uv run pytest tests/benchmark/agent/test_runner.py tests/benchmark/agent/test_cli.py -v`
Expected: PASS (all tests, including the two new ones per file)

- [ ] **Step 6: Run the full Phase 5 slice and commit**

Run: `uv run pytest tests/benchmark/agent/ -v`
Expected: PASS (all tests from Tasks 1–18)

```bash
git add modelman/src/modelman/benchmark/agent/runner.py \
        modelman/src/modelman/benchmark/agent/cli.py \
        modelman/tests/benchmark/agent/test_runner.py \
        modelman/tests/benchmark/agent/test_cli.py
git commit -m "feat(agent-bench): wire judging into the runner + --skip-judge - completes plan item #18"
```

---
