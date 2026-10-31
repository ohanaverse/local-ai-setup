"""Shared LLM-judge mechanics: strict-JSON rubric scoring, retry-on-malformed
reply, and multi-sample median-combine.

Generalized out of the agent benchmark's diff-fixing judge (originally
hardcoded to one rubric) so `benchmark.eval` can reuse the same mechanics
with a different Rubric per capability category. `benchmark.agent.judge`
wraps this module with its fixed diff-fixing Rubric and keeps everything
diff-specific (anonymization, overclaim detection) to itself.
"""

from __future__ import annotations

import contextlib
import json
import math
import os
import re
import signal
import subprocess
import tempfile
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
_BASE_ANSWER_KEYS = frozenset({"scores", "total"})


def _json_candidate(raw_text: str, rubric: Rubric) -> str:
    """The part of a reply that should be parsed as JSON.

    Scans every '{' for a parseable dict rather than stopping at the first
    one, because a reply can contain an incidental JSON-looking fragment
    before the judge's actual answer. The first candidate with all the
    answer keys wins; failing that, the first parseable dict is a fallback.
    "verdict" is only required among those keys when the rubric declares a
    closed verdict set — otherwise a decoy fragment that happens to echo
    just scores/total (e.g. the model restating the schema) would outrank
    the real, verdict-bearing answer for rubrics like AGENT_RUBRIC that do
    require one, while a rubric with verdicts=None never expects the key at
    all and must not require it."""
    answer_keys = _BASE_ANSWER_KEYS | ({"verdict"} if rubric.verdicts is not None else set())
    # Every fenced block is a candidate, then the raw reply: the first fence
    # is not necessarily the answer (a rationale can quote a ```python
    # snippet before an unfenced or later-fenced JSON answer, typical for
    # code_review items). An answer-keyed dict anywhere beats any fallback.
    candidates = [m.group(1) for m in _FENCE_RE.finditer(raw_text)] + [raw_text]
    decoder = json.JSONDecoder()
    fallback: str | None = None
    for text in candidates:
        idx = 0
        while idx < len(text):
            if text[idx] == "{":
                try:
                    obj, end = decoder.raw_decode(text, idx)
                    if isinstance(obj, dict):
                        if obj.keys() >= answer_keys:
                            return text[idx:end]
                        if fallback is None:
                            fallback = text[idx:end]
                except json.JSONDecodeError:
                    pass
            idx += 1
    return fallback if fallback is not None else raw_text


def parse_response(raw_text: str, rubric: Rubric) -> JudgeScore:
    if not isinstance(raw_text, str):
        raise JudgeContractError(f"response is not text: {type(raw_text).__name__}")
    try:
        data = json.loads(_json_candidate(raw_text, rubric))
    except json.JSONDecodeError as exc:
        raise JudgeContractError(f"response is not valid JSON: {exc}") from exc
    if not isinstance(data, dict):
        raise JudgeContractError("response is not a JSON object")

    if "scores" not in data:
        raise JudgeContractError("response missing keys: ['scores']")

    scores = data["scores"]
    if not isinstance(scores, dict):
        raise JudgeContractError(f"scores must be an object, got {type(scores).__name__}")
    missing_dims = [d for d in rubric.dimensions if d not in scores]
    if missing_dims:
        raise JudgeContractError(f"scores missing dimensions: {missing_dims}")
    for dim, max_points in rubric.dimensions.items():
        value = scores[dim]
        # An integral float ("25.0") is the same score spelled differently;
        # rejecting it would fail identical temperature-0 retries the same
        # way. Normalize to int so `total` and the stored scores stay ints.
        if isinstance(value, float) and value.is_integer():
            value = int(value)
            scores[dim] = value
        if isinstance(value, bool) or not isinstance(value, int) or not (0 <= value <= max_points):
            raise JudgeContractError(f"invalid score for {dim}: {value!r}")

    verdict = data.get("verdict", "")
    if rubric.verdicts is not None:
        # isinstance guard first: a list/dict verdict is unhashable, so the
        # `in` check on a tuple/set of verdicts must not see one.
        if not isinstance(verdict, str) or verdict not in rubric.verdicts:
            raise JudgeContractError(f"unknown verdict: {verdict!r}")
    elif verdict is not None and not isinstance(verdict, str):
        raise JudgeContractError(f"verdict must be a string, got {verdict!r}")
    verdict = verdict or ""

    # The judge's own `total` is ignored: the validated per-dimension
    # scores are the source of truth, and an LLM's arithmetic (or a
    # "90.0" / "90" spelling) is not worth failing an otherwise good reply
    # over — identical retries at the same temperature would just fail the
    # same way. Deriving it also means a fabricated total can never inflate
    # the score.
    total = sum(scores[d] for d in rubric.dimensions)

    flags = data.get("flags", [])
    if not isinstance(flags, list):
        raise JudgeContractError(f"flags must be a list, got {type(flags).__name__}")

    return JudgeScore(
        scores={d: scores[d] for d in rubric.dimensions},
        total=total,
        verdict=str(verdict),
        flags=[str(f) for f in flags],
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
    if samples < 1 or max_attempts < 1:
        raise ValueError(f"samples and max_attempts must be >= 1, got {samples}/{max_attempts}")
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
        # floor(x + 0.5), not round(): an even sample count can give a .5
        # median, and round() would send 2.5 -> 2 but 3.5 -> 4.
        combined_scores = {
            dim: math.floor(median(s.scores[dim] for s in collected) + 0.5)
            for dim in rubric.dimensions
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

    def __init__(
        self,
        base_url: str,
        api_key: str,
        model: str,
        *,
        retry_backoff_s: float = 2.0,
        timeout_s: float = 120,
        fail_fast_on_read_timeout: bool = False,
        extra_params: dict[str, float | int] | None = None,
    ):
        # Request-body fields sent beside temperature (an eval row's top_p /
        # top_k); the judge sends none.
        self.extra_params = dict(extra_params or {})
        self.timeout_s = timeout_s
        self.fail_fast_on_read_timeout = fail_fast_on_read_timeout
        self.base_url = base_url.rstrip("/")
        self.api_key = api_key
        self.model = model
        self.retry_backoff_s = retry_backoff_s

    def complete(self, prompt: str, *, temperature: float) -> str:
        try:
            return self._post(prompt, temperature)
        except requests.RequestException as exc:
            if isinstance(exc, requests.ReadTimeout) and self.fail_fast_on_read_timeout:
                # A read timeout already burned the full timeout_s (900s for
                # row generation); retrying would double the stall on a model
                # that is likely looping, so generation transports fail fast.
                raise JudgeTransportError(
                    f"transport timed out after {self.timeout_s}s: {exc}"
                ) from exc
            time.sleep(self.retry_backoff_s)
            try:
                return self._post(prompt, temperature)
            except requests.RequestException as retry_exc:
                raise JudgeTransportError(
                    f"judge transport failed after retry: {retry_exc}"
                ) from retry_exc

    def _post(self, prompt: str, temperature: float) -> str:
        response = requests.post(
            f"{self.base_url}/chat/completions",
            headers={"Authorization": f"Bearer {self.api_key}"},
            json={
                "model": self.model,
                "messages": [{"role": "user", "content": prompt}],
                "temperature": temperature,
                **self.extra_params,
                "stream": False,
            },
            timeout=self.timeout_s,
        )
        if response.status_code >= 400:
            raise JudgeTransportError(
                f"HTTP {response.status_code} from {self.base_url}/chat/completions: "
                f"{response.text[:200]}"
            )
        try:
            data = response.json()
            content = data["choices"][0]["message"]["content"]
        except (json.JSONDecodeError, KeyError, IndexError, TypeError) as exc:
            raise JudgeTransportError(
                f"malformed 200 response from {self.base_url}/chat/completions: "
                f"{type(exc).__name__}: {response.text[:200]}"
            ) from exc
        # A 200 with content null (common for reasoning models that spend
        # the budget on hidden thinking) is a failed call, not empty text.
        if not isinstance(content, str):
            raise JudgeTransportError(
                f"200 response from {self.base_url}/chat/completions had non-text content: "
                f"{type(content).__name__}"
            )
        return content


CLAUDE_CLI_SYSTEM_PROMPT = (
    "You are a strict grading assistant. Follow the instructions in the user "
    "message exactly and reply with only what they ask for."
)

# Env var prefixes removed before spawning the claude CLI: a stray
# ANTHROPIC_API_KEY / ANTHROPIC_BASE_URL (wt sets the latter when it launches
# claude through LiteLLM) or CLAUDE_CODE_* provider switch would divert the
# call away from the logged-in subscription.
_CLAUDE_CLI_ENV_DENY_PREFIXES = ("ANTHROPIC_", "CLAUDE_CODE_")

_CLAUDE_CLI_NOT_LOGGED_IN = "Not logged in"


# Seam for tests/conftest.py: its autouse guard swaps this for a wrapper that
# refuses blocked binaries (`claude`), since subprocess.run's fake never sees
# a Popen.
_popen = subprocess.Popen


def claude_cli_env() -> dict[str, str]:
    """os.environ minus ANTHROPIC_* and CLAUDE_CODE_*. A denylist, not an
    allowlist: the CLI reads its OAuth credentials from the macOS keychain,
    which needs HOME/USER/LOGNAME/TMPDIR/PATH to stay in place (an empty
    env reports "Not logged in")."""
    return {k: v for k, v in os.environ.items() if not k.startswith(_CLAUDE_CLI_ENV_DENY_PREFIXES)}


def claude_cli_argv(model: str, *, executable: str = "claude") -> list[str]:
    """Headless judge invocation; the prompt goes on stdin, never argv.

    No --bare (it never reads OAuth/keychain, so it can't use a
    subscription). --tools "" keeps the judge from acting and cuts the input
    from ~18k to well under 1k tokens per call; the other flags stop the
    CLI loading settings, MCP servers, skills or a session from the host."""
    if model.startswith("anthropic/"):
        model = model[len("anthropic/") :]
    return [
        executable,
        "-p",
        "--model",
        model,
        "--output-format",
        "json",
        "--setting-sources",
        "local",
        "--strict-mcp-config",
        "--disable-slash-commands",
        "--no-session-persistence",
        "--tools",
        "",
        "--system-prompt",
        CLAUDE_CLI_SYSTEM_PROMPT,
    ]


def parse_claude_cli_json(text: str) -> dict | None:
    """The CLI's single JSON object, tolerating stray text around it."""
    decoder = json.JSONDecoder()
    start = text.find("{")
    while start >= 0:
        try:
            obj, _ = decoder.raw_decode(text[start:])
        except json.JSONDecodeError:
            obj = None
        if isinstance(obj, dict):
            return obj
        start = text.find("{", start + 1)
    return None


class ClaudeCliJudgeTransport:
    """Judge on the user's Claude subscription via headless `claude -p`
    (a subscription can't be used as an API key). The prompt is sent on
    stdin from a fresh empty temp directory with a scrubbed env.

    `temperature` is accepted for JudgeTransport compatibility and IGNORED:
    the CLI has no temperature control. The constructor does no I/O; login
    is checked by eval preflight, not here."""

    def __init__(
        self,
        model: str,
        *,
        timeout_s: float = 300,
        retry_backoff_s: float = 2.0,
        executable: str = "claude",
    ):
        self.model = model
        self.timeout_s = timeout_s
        self.retry_backoff_s = retry_backoff_s
        self.executable = executable

    def complete(self, prompt: str, *, temperature: float) -> str:
        try:
            return self._call(prompt)
        except _ClaudeCliFatal as exc:
            raise JudgeTransportError(str(exc)) from exc
        except _ClaudeCliRetryable:
            time.sleep(self.retry_backoff_s)
        try:
            return self._call(prompt)
        except _ClaudeCliError as exc:
            raise JudgeTransportError(f"claude CLI judge failed after retry: {exc}") from exc

    def _run(self, argv: list[str], prompt: str, cwd: str) -> tuple[str, str, int]:
        # Own session so a timeout kills the whole process group: the CLI may
        # sit behind a launcher, and an orphan holding the pipes would make
        # communicate() block for the child's full runtime.
        with _popen(
            argv,
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            encoding="utf-8",
            errors="replace",  # lone surrogates / non-UTF-8 output must not escape as UnicodeError
            cwd=cwd,
            env=claude_cli_env(),
            start_new_session=True,
        ) as proc:
            try:
                stdout, stderr = proc.communicate(prompt, timeout=self.timeout_s)
            except subprocess.TimeoutExpired:
                with contextlib.suppress(ProcessLookupError):
                    os.killpg(proc.pid, signal.SIGKILL)
                proc.communicate()
                raise
            return stdout, stderr, proc.returncode

    def _call(self, prompt: str) -> str:
        argv = claude_cli_argv(self.model, executable=self.executable)
        with tempfile.TemporaryDirectory(prefix="llmbench-judge-") as cwd:
            try:
                stdout, stderr, returncode = self._run(argv, prompt, cwd)
            except FileNotFoundError as exc:
                raise _ClaudeCliFatal(
                    f"claude CLI not found (executable {self.executable!r}); "
                    "install Claude Code and put it on PATH"
                ) from exc
            except subprocess.TimeoutExpired as exc:
                raise _ClaudeCliRetryable(f"claude CLI timed out after {self.timeout_s}s") from exc
        data = parse_claude_cli_json(stdout or "")
        result = data.get("result") if data else None
        if returncode == 0 and data and data.get("is_error") is False:
            if isinstance(result, str) and result:
                return result
            raise _ClaudeCliRetryable("claude CLI returned an empty or non-text result")
        detail = result if isinstance(result, str) and result else None
        if detail is None:
            detail = (stderr or "").strip() or (stdout or "").strip()
        detail = detail[:200]
        message = f"claude CLI exited {returncode}: {detail}"
        if _CLAUDE_CLI_NOT_LOGGED_IN in detail:
            raise _ClaudeCliFatal(f"{message} (run: claude auth login)")
        raise _ClaudeCliRetryable(message)


class _ClaudeCliError(Exception):
    """Internal: one failed claude CLI call."""


class _ClaudeCliRetryable(_ClaudeCliError):
    pass


class _ClaudeCliFatal(_ClaudeCliError):
    pass
