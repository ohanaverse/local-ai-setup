"""`llmbench probe thinking`: compare how long, and in what style, a model
thinks on different engines (issue #403).

Each target is an OpenAI-compatible server the owner has already started and
isolated. For every (target, prompt) the probe sends one non-streaming chat
completion and records the reasoning text's length and opening, the answer's
length and its markdown shape. It does not start, stop, isolate or warm any
provider, does not read the registry, and writes nothing outside the output
directory it is given (never the benchmark results dir or latest.toml).
"""

from __future__ import annotations

import json
import re
import tempfile
import time
from collections.abc import Callable, Mapping, Sequence
from dataclasses import asdict, dataclass, field
from datetime import UTC, datetime
from pathlib import Path
from typing import Any

import requests

from llmbench.benchmark.errors import BenchmarkError
from llmbench.benchmark.eval.category import load_category


class ProbeError(Exception):
    """A usage error found before any request is sent (bad target spec,
    unset key variable, missing prompt)."""


# --- targets ----------------------------------------------------------------

_NAME_RE = re.compile(r"[A-Za-z0-9._-]+")
_ENV_NAME_RE = re.compile(r"[A-Za-z_][A-Za-z0-9_]*")
TARGET_FORMAT = "NAME=BASE_URL,MODEL_ID[,API_KEY_ENV]"


@dataclass(frozen=True)
class Target:
    name: str
    base_url: str
    model: str
    # The NAME of the environment variable holding the key, never the key.
    api_key_env: str | None = None


def parse_target(spec: str) -> Target:
    """Parse `NAME=BASE_URL,MODEL_ID[,API_KEY_ENV]`."""

    def fail(why: str) -> ProbeError:
        return ProbeError(f"bad --target {spec!r}: {why} (expected {TARGET_FORMAT})")

    name, sep, rest = spec.partition("=")
    if not sep or not name:
        raise fail("it must start with NAME=")
    if not _NAME_RE.fullmatch(name):
        raise fail("the name may use only letters, digits, '.', '_' and '-'")
    parts = rest.split(",")
    if len(parts) > 3:
        raise fail("too many comma-separated fields")
    base_url = parts[0].strip()
    if not base_url.startswith(("http://", "https://")):
        raise fail("BASE_URL must start with http:// or https://")
    model = parts[1].strip() if len(parts) > 1 else ""
    if not model:
        raise fail("MODEL_ID is missing")
    key_env = parts[2].strip() if len(parts) > 2 else ""
    if key_env and not _ENV_NAME_RE.fullmatch(key_env):
        raise fail("API_KEY_ENV must be the name of an environment variable, not a key")
    return Target(
        name=name,
        base_url=base_url.rstrip("/"),
        model=model,
        api_key_env=key_env or None,
    )


def resolve_api_key(target: Target, environ: Mapping[str, str]) -> str | None:
    """The key from the variable the target names; None when it names none.
    A named variable that is unset or empty is an error, not a keyless call."""
    if target.api_key_env is None:
        return None
    value = environ.get(target.api_key_env, "")
    if not value:
        raise ProbeError(
            f"target {target.name!r} names API key variable {target.api_key_env}, "
            "which is unset or empty"
        )
    return value


def redact(text: str, key: str | None) -> str:
    """Replace the key wherever a server echoed it back."""
    return text.replace(key, "***") if key else text


# --- prompts ----------------------------------------------------------------

# The question from issue #403 that showed omlx's 24-token thinking.
ARITHMETIC_PROMPT = "What is 17*23? Answer briefly."
# The planning item whose answer shape differed most across engines (#403);
# read from benchmarks/tasks/eval/planning/items.toml through the eval
# benchmark's own loader, so the probe sends exactly what the suite sent.
PLANNING_CATEGORY = "planning"
PLANNING_ITEM_ID = "flaky-test-suite"


@dataclass(frozen=True)
class Prompt:
    id: str
    text: str
    source: str


def builtin_prompts(categories_root: Path) -> list[Prompt]:
    category_dir = Path(categories_root) / PLANNING_CATEGORY
    hint = "pass --root, or --no-builtin with --prompt-file"
    try:
        category = load_category(category_dir)
    except BenchmarkError as exc:
        raise ProbeError(f"cannot load the built-in planning prompt: {exc}; {hint}") from exc
    for item in category.items:
        if item.id == PLANNING_ITEM_ID:
            return [
                Prompt(id="arithmetic", text=ARITHMETIC_PROMPT, source="builtin"),
                Prompt(
                    id=f"{PLANNING_CATEGORY}/{item.id}",
                    text=item.prompt,
                    source=str(category_dir / "items.toml"),
                ),
            ]
    raise ProbeError(f"{category_dir} has no item {PLANNING_ITEM_ID!r}; {hint}")


def load_prompt_file(path: Path) -> Prompt:
    path = Path(path)
    if not path.is_file():
        raise ProbeError(f"prompt file not found: {path}")
    text = path.read_text(encoding="utf-8")
    if not text.strip():
        raise ProbeError(f"prompt file is empty: {path}")
    return Prompt(id=path.stem, text=text, source=str(path))


# --- answer shape -----------------------------------------------------------
#
# Lines inside a fenced code block are not counted as headings, bullets,
# numbered items or table lines: they are code (`# comment`, `a | b`), and a
# markdown renderer shows them as code. A code fence counts once per block.
# As in CommonMark, a block closes only on a bare run (no info string) of the
# opening fence's character at least as long as the opening run, indented by
# at most three spaces; a fence never closed hides every later line.

_HEADING_RE = re.compile(r"#{1,6}[ \t]")
_BULLET_RE = re.compile(r"\s*[-*+][ \t]")
_NUMBERED_RE = re.compile(r"\s*\d+[.)][ \t]")
_TABLE_RE = re.compile(r"\s*\|")
_FENCE_RE = re.compile(r"\s*(`{3,}|~{3,})")
_CLOSING_FENCE_RE = re.compile(r" {0,3}(`{3,}|~{3,})[ \t]*")
# A thematic break (`* * *`, `- - -`, `***`): three or more of one of -, *, _
# with only spaces or tabs between. A horizontal rule, not a bullet.
_THEMATIC_BREAK_RE = re.compile(r" {0,3}([-*_])(?:[ \t]*\1){2,}[ \t]*")


def is_heading(line: str) -> bool:
    return _HEADING_RE.match(line) is not None


def is_bullet(line: str) -> bool:
    return _BULLET_RE.match(line) is not None and _THEMATIC_BREAK_RE.fullmatch(line) is None


def is_numbered(line: str) -> bool:
    return _NUMBERED_RE.match(line) is not None


def is_table_line(line: str) -> bool:
    return _TABLE_RE.match(line) is not None


def is_fence(line: str) -> bool:
    return _FENCE_RE.match(line) is not None


@dataclass(frozen=True)
class AnswerShape:
    headings: int
    bullets: int
    numbered: int
    table_lines: int
    code_fences: int


def answer_shape(text: str) -> AnswerShape:
    counts = dict.fromkeys(("headings", "bullets", "numbered", "table_lines", "code_fences"), 0)
    # The opening run (e.g. "````") of the block the line is in, or None.
    open_fence: str | None = None
    for line in text.splitlines():
        if open_fence is not None:
            closing = _CLOSING_FENCE_RE.fullmatch(line)
            if (
                closing is not None
                and closing.group(1)[0] == open_fence[0]
                and len(closing.group(1)) >= len(open_fence)
            ):
                open_fence = None
            continue
        fence = _FENCE_RE.match(line)
        if fence is not None:
            open_fence = fence.group(1)
            counts["code_fences"] += 1
        elif is_heading(line):
            counts["headings"] += 1
        elif is_bullet(line):
            counts["bullets"] += 1
        elif is_numbered(line):
            counts["numbered"] += 1
        elif is_table_line(line):
            counts["table_lines"] += 1
    return AnswerShape(**counts)


# --- requests ---------------------------------------------------------------

REASONING_HEAD_CHARS = 200
_ERROR_BODY_CHARS = 300


@dataclass(frozen=True)
class Sampling:
    """What the request body carries beside the prompt. A top_p or top_k of 0
    (or None) is left out, so the server's own default applies; max_tokens
    None is left out too."""

    temperature: float
    top_p: float | None
    top_k: int | None
    max_tokens: int | None = None

    def body_fields(self) -> dict[str, float | int]:
        fields: dict[str, float | int] = {"temperature": self.temperature}
        if self.top_p:
            fields["top_p"] = self.top_p
        if self.top_k:
            fields["top_k"] = self.top_k
        if self.max_tokens:
            fields["max_tokens"] = self.max_tokens
        return fields


@dataclass
class ProbeResult:
    target: str
    prompt_id: str
    status: int | None = None
    error: str | None = None
    elapsed_s: float = 0.0
    reasoning: str = ""
    # Which message field held the reasoning (None: the reply had none).
    reasoning_field: str | None = None
    reasoning_tokens: int | None = None
    answer: str = ""
    completion_tokens: int | None = None
    finish_reason: str | None = None

    @property
    def answered(self) -> bool:
        return self.error is None and self.status == 200

    @property
    def reasoning_chars(self) -> int:
        return len(self.reasoning)

    @property
    def answer_chars(self) -> int:
        return len(self.answer)

    @property
    def reasoning_head(self) -> str:
        return self.reasoning[:REASONING_HEAD_CHARS]

    @property
    def shape(self) -> AnswerShape:
        return answer_shape(self.answer)


_REASONING_FIELDS = ("reasoning_content", "reasoning")


def _int_or_none(value: Any) -> int | None:
    return value if isinstance(value, int) and not isinstance(value, bool) else None


def _fill_from_completion(result: ProbeResult, data: Any) -> None:
    """Read one chat.completion body into `result`, or set its error."""
    try:
        choice = data["choices"][0]
        message = choice["message"]
        if not isinstance(message, dict):
            raise TypeError("message is not an object")
    except (KeyError, IndexError, TypeError) as exc:
        result.error = f"unexpected response shape ({type(exc).__name__}: {exc})"
        return
    # The message's own field first, then the same names on the choice (some
    # servers put it there).
    for holder in (message, choice):
        for name in _REASONING_FIELDS:
            value = holder.get(name)
            if isinstance(value, str) and value:
                result.reasoning, result.reasoning_field = value, name
                break
        if result.reasoning_field is not None:
            break
    content = message.get("content")
    result.answer = content if isinstance(content, str) else ""
    finish = choice.get("finish_reason")
    result.finish_reason = finish if isinstance(finish, str) else None
    usage = data.get("usage") if isinstance(data, dict) else None
    if isinstance(usage, dict):
        result.completion_tokens = _int_or_none(usage.get("completion_tokens"))
        details = usage.get("completion_tokens_details")
        if isinstance(details, dict):
            result.reasoning_tokens = _int_or_none(details.get("reasoning_tokens"))


def probe_one(
    target: Target,
    prompt: Prompt,
    sampling: Sampling,
    *,
    api_key: str | None = None,
    timeout_s: float,
) -> ProbeResult:
    """One non-streaming chat completion. Never raises for a server problem:
    unreachable, timeout, non-200 and a body that is not a completion each
    come back as a result with `error` set."""
    result = ProbeResult(target=target.name, prompt_id=prompt.id)
    headers = {"Authorization": f"Bearer {api_key}"} if api_key else {}
    body = {
        "model": target.model,
        "messages": [{"role": "user", "content": prompt.text}],
        **sampling.body_fields(),
        "stream": False,
    }
    url = f"{target.base_url}/chat/completions"
    started = time.monotonic()
    # trust_env=False: no proxy and no .netrc credentials; the probe talks to
    # the URL it was given with the key it was given, and nothing else.
    with requests.Session() as session:
        session.trust_env = False
        try:
            response = session.post(url, json=body, headers=headers, timeout=timeout_s)
        except requests.Timeout:
            result.error = f"timed out after {timeout_s:g}s"
        except requests.ConnectionError as exc:
            result.error = redact(f"unreachable: {exc}", api_key)
        except requests.RequestException as exc:
            result.error = redact(f"request failed: {exc}", api_key)
        else:
            result.status = response.status_code
            text = response.text
            if response.status_code != 200:
                result.error = redact(
                    f"HTTP {response.status_code}: {text[:_ERROR_BODY_CHARS]}", api_key
                )
            else:
                try:
                    data = json.loads(text)
                except json.JSONDecodeError:
                    result.error = redact(f"non-JSON response: {text[:_ERROR_BODY_CHARS]}", api_key)
                else:
                    _fill_from_completion(result, data)
                    if result.error is not None:
                        result.error = redact(result.error, api_key)
    result.elapsed_s = time.monotonic() - started
    return result


def run_probe(
    targets: Sequence[tuple[Target, str | None]],
    prompts: Sequence[Prompt],
    sampling: Sampling,
    *,
    timeout_s: float,
    on_result: Callable[[ProbeResult], None] | None = None,
) -> list[ProbeResult]:
    """Every prompt against every target, target by target (the owner isolates
    one engine at a time). A failing target never stops the run."""
    results: list[ProbeResult] = []
    for target, api_key in targets:
        for prompt in prompts:
            result = probe_one(target, prompt, sampling, api_key=api_key, timeout_s=timeout_s)
            results.append(result)
            if on_result is not None:
                on_result(result)
    return results


# --- report -----------------------------------------------------------------


@dataclass
class ProbeRun:
    started_at: str
    targets: list[Target]
    prompts: list[Prompt]
    sampling: Sampling
    timeout_s: float
    results: list[ProbeResult] = field(default_factory=list)


READING_NOTE = "\n".join(
    [
        "- **Reasoning length is comparable only between engines that render the same chat "
        "template for the same model.** A different template, or a different model, changes "
        "what thinking looks like, so compare rows within one model and one template only.",
        "- *Reasoning tokens* is shown only when the server reports "
        "`usage.completion_tokens_details.reasoning_tokens`; `—` means not reported, not zero. "
        "*Reasoning chars* is the length of `reasoning_content` (or `reasoning`); 0 means the "
        "reply carried no reasoning field.",
        "- The shape columns count lines of the answer outside fenced code blocks (a `# comment` "
        "inside a code block is not a heading); *code fences* counts each fenced block once. "
        "A fence that is never closed hides every later line, so an answer cut off inside a "
        "code block (*Finish* `length`) can show low counts for that reason alone.",
        "- Each cell is one sample at the sampling above. Repeat the run before reading much "
        "into a small difference.",
    ]
)

_COLUMNS = (
    "Target",
    "Status",
    "Reasoning chars",
    "Reasoning tokens",
    "Answer chars",
    "Completion tokens",
    "Finish",
    "Headings",
    "Bullets",
    "Numbered",
    "Table lines",
    "Code fences",
    "Time (s)",
)
_MISSING = "—"


def _cell(value: object) -> str:
    if value is None:
        return _MISSING
    return str(value).replace("|", "\\|").replace("\n", " ")


def _status_cell(result: ProbeResult) -> str:
    if result.answered:
        return str(result.status)
    if result.status is None:
        return "error"
    if result.status == 200:
        return "bad reply"
    return f"HTTP {result.status}"


def _row(result: ProbeResult) -> list[str]:
    if result.answered:
        shape = result.shape
        metrics: list[object] = [
            result.reasoning_chars,
            result.reasoning_tokens,
            result.answer_chars,
            result.completion_tokens,
            result.finish_reason,
            shape.headings,
            shape.bullets,
            shape.numbered,
            shape.table_lines,
            shape.code_fences,
        ]
    else:
        metrics = [None] * 10
    return [
        _cell(result.target),
        _status_cell(result),
        *(_cell(m) for m in metrics),
        f"{result.elapsed_s:.1f}",
    ]


def _fenced(text: str) -> str:
    """A code block that the text itself cannot close."""
    longest = max((len(run) for run in re.findall(r"`+", text)), default=0)
    fence = "`" * max(3, longest + 1)
    return f"{fence}text\n{text}\n{fence}"


def _sampling_line(sampling: Sampling) -> str:
    parts = [f"temperature {sampling.temperature}"]
    parts.append(f"top_p {sampling.top_p}" if sampling.top_p else "top_p not sent")
    parts.append(f"top_k {sampling.top_k}" if sampling.top_k else "top_k not sent")
    parts.append(
        f"max_tokens {sampling.max_tokens}" if sampling.max_tokens else "max_tokens not sent"
    )
    return ", ".join(parts)


def render_markdown(run: ProbeRun) -> str:
    lines = [
        "# Thinking probe",
        "",
        f"- Started: {run.started_at}",
        f"- Sampling: {_sampling_line(run.sampling)}",
        f"- Timeout per request: {run.timeout_s:g}s",
        "",
        "| Target | Base URL | Model | Key variable |",
        "|---|---|---|---|",
    ]
    for target in run.targets:
        lines.append(
            f"| {_cell(target.name)} | {_cell(target.base_url)} | {_cell(target.model)} "
            f"| {_cell(target.api_key_env or 'none')} |"
        )
    lines += ["", "## Reading this", "", READING_NOTE.rstrip()]
    for prompt in run.prompts:
        rows = [r for r in run.results if r.prompt_id == prompt.id]
        lines += [
            "",
            f"## Prompt `{prompt.id}`",
            "",
            f"Source: {prompt.source} ({len(prompt.text)} chars)",
            "",
            "| " + " | ".join(_COLUMNS) + " |",
            "|" + "---|" * len(_COLUMNS),
        ]
        lines += ["| " + " | ".join(_row(r)) + " |" for r in rows]
        answered = [r for r in rows if r.answered]
        if answered:
            lines += ["", f"### Reasoning heads (first {REASONING_HEAD_CHARS} chars)"]
            for r in answered:
                head = r.reasoning_head or "(no reasoning field)"
                lines += ["", f"**{r.target}**", "", _fenced(head)]
        failed = [r for r in rows if not r.answered]
        if failed:
            lines += ["", "### Errors", ""]
            lines += [f"- **{r.target}**: {r.error}" for r in failed]
    return "\n".join(lines) + "\n"


def _result_json(result: ProbeResult) -> dict[str, Any]:
    doc = asdict(result)
    doc.update(
        answered=result.answered,
        reasoning_chars=result.reasoning_chars,
        answer_chars=result.answer_chars,
        reasoning_head=result.reasoning_head,
        shape=asdict(result.shape),
    )
    return doc


def run_json(run: ProbeRun) -> dict[str, Any]:
    return {
        "started_at": run.started_at,
        "sampling": asdict(run.sampling),
        "timeout_s": run.timeout_s,
        "targets": [asdict(t) for t in run.targets],
        "prompts": [{"id": p.id, "source": p.source, "text": p.text} for p in run.prompts],
        "results": [_result_json(r) for r in run.results],
    }


def write_outputs(run: ProbeRun, out_dir: Path) -> tuple[Path, Path]:
    out_dir.mkdir(parents=True, exist_ok=True)
    md_path = out_dir / "report.md"
    json_path = out_dir / "results.json"
    md_path.write_text(render_markdown(run), encoding="utf-8")
    json_path.write_text(json.dumps(run_json(run), indent=2) + "\n", encoding="utf-8")
    return md_path, json_path


def default_out_dir() -> Path:
    """A new timestamped directory under the platform temp dir."""
    stamp = datetime.now(UTC).strftime("%Y%m%dT%H%M%SZ")
    return Path(tempfile.mkdtemp(prefix=f"llmbench-probe-thinking-{stamp}-"))
