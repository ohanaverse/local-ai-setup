# Benchmark Tooling Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `modelman benchmark` subcommand that replaces `local-ai-setup`'s ad-hoc bash benchmark scripts with an extensible, registry-aware runner.

**Architecture:** A new `src/modelman/benchmark/` package owns workloads, runner, results, and CLI. Service isolation is delegated to small helpers in `local-ai-setup`. The runner consumes `registry.toml` + `modelman.toml`, targets local models, and writes JSON/Markdown artifacts to disk.

**Tech Stack:** Python 3.13, Typer, requests, standard library. Tests use pytest + unittest.mock. Helpers in `local-ai-setup` are bash scripts.

---

## File structure

**New files in `modelman`:**
- `src/modelman/benchmark/__init__.py`
- `src/modelman/benchmark/errors.py` — `BenchmarkError`
- `src/modelman/benchmark/workloads/__init__.py` — workload registry
- `src/modelman/benchmark/workloads/base.py` — `WorkloadSpec`, `RawRun`, `BenchmarkMetrics`, `Workload` protocol
- `src/modelman/benchmark/workloads/chat_streaming.py` — default `chat` workload
- `src/modelman/benchmark/workloads/short.py` — `short` workload
- `src/modelman/benchmark/workloads/long.py` — `long` workload
- `src/modelman/benchmark/workloads/code.py` — `code` workload
- `src/modelman/benchmark/isolation.py` — subprocess contract with `local-ai-setup`
- `src/modelman/benchmark/results.py` — JSON + Markdown report writers
- `src/modelman/benchmark/runner.py` — target discovery, pass loop, aggregation
- `src/modelman/benchmark/cli.py` — `modelman benchmark` typer subcommand
- `tests/benchmark/test_workloads.py`
- `tests/benchmark/test_isolation.py`
- `tests/benchmark/test_results.py`
- `tests/benchmark/test_runner.py`

**Modified files in `modelman`:**
- `pyproject.toml` — add `requests` dependency
- `src/modelman/main.py` — `app.add_typer(benchmark_app, name="benchmark")`

**New files in `local-ai-setup`:**
- `bin/llm-isolate-provider`
- `bin/llm-restore-providers`

---

## Task 1: Add `requests` dependency

**Files:**
- Modify: `pyproject.toml:10-17`

- [ ] **Step 1: Add `requests>=2.32.0` to dependencies**

```toml
dependencies = [
    "typer>=0.15.3",
    "rich>=14.0.0",
    "pyyaml>=6.0.2",
    "huggingface_hub>=1.0.0",
    "questionary>=2.0.0",
    "textual>=0.80.0",
    "tomli-w>=1.2.0",
    "requests>=2.32.0",
]
```

- [ ] **Step 2: Verify lock/sync**

Run: `uv lock`
Expected: lock file updates (or no error if project does not use a lock file).

- [ ] **Step 3: Commit**

```bash
git add pyproject.toml
git commit -m "deps: add requests for benchmark HTTP client"
```

---

## Task 2: Create benchmark error base class

**Files:**
- Create: `src/modelman/benchmark/__init__.py`
- Create: `src/modelman/benchmark/errors.py`

- [ ] **Step 1: Write the failing test**

`tests/benchmark/test_errors.py`:

```python
from modelman.benchmark.errors import BenchmarkError


def test_benchmark_error_is_exception():
    exc = BenchmarkError("boom")
    assert str(exc) == "boom"
    assert isinstance(exc, Exception)
```

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/benchmark/test_errors.py -v`
Expected: `ModuleNotFoundError` or `ImportError` for `modelman.benchmark.errors`.

- [ ] **Step 3: Write minimal implementation**

`src/modelman/benchmark/__init__.py`:

```python
"""Benchmark tooling for modelman."""
```

`src/modelman/benchmark/errors.py`:

```python
"""Benchmark-specific errors."""


class BenchmarkError(Exception):
    """Raised when a benchmark step cannot complete."""
```

- [ ] **Step 4: Run test to verify it passes**

Run: `uv run pytest tests/benchmark/test_errors.py -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add src/modelman/benchmark/__init__.py src/modelman/benchmark/errors.py tests/benchmark/test_errors.py
git commit -m "feat(benchmark): add error base class"
```

---

## Task 3: Create workload base types and protocol

**Files:**
- Create: `src/modelman/benchmark/workloads/__init__.py`
- Create: `src/modelman/benchmark/workloads/base.py`

- [ ] **Step 1: Write the failing test**

`tests/benchmark/test_workloads.py` (first test):

```python
from modelman.benchmark.workloads.base import BenchmarkMetrics, RawRun, WorkloadSpec


def test_raw_run_defaults():
    raw = RawRun(start_time=0.0, end_time=1.0, first_token_time=0.2, chunks=[])
    assert raw.completion_tokens is None
    assert raw.prompt_tokens is None
    assert raw.error is None


def test_metrics_computes_throughput():
    m = BenchmarkMetrics(ttft_ms=100, total_ms=500, completion_tokens=100, prompt_tokens=10)
    assert m.throughput_tok_s == 250.0
```

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/benchmark/test_workloads.py::test_raw_run_defaults -v`
Expected: `ModuleNotFoundError` for `modelman.benchmark.workloads.base`.

- [ ] **Step 3: Write minimal implementation**

`src/modelman/benchmark/workloads/__init__.py`:

```python
"""Built-in benchmark workloads."""
```

`src/modelman/benchmark/workloads/base.py`:

```python
"""Workload abstraction for benchmark runs."""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any, Protocol, runtime_checkable


@dataclass
class WorkloadSpec:
    name: str
    display_name: str
    prompt: str
    max_tokens: int
    temperature: float
    stream: bool
    stream_options: dict[str, Any] | None = None


@dataclass
class RawRun:
    start_time: float
    end_time: float
    first_token_time: float | None
    chunks: list[dict[str, Any]]
    completion_tokens: int | None = None
    prompt_tokens: int | None = None
    error: str | None = None


@dataclass
class BenchmarkMetrics:
    ttft_ms: int | None
    total_ms: int
    completion_tokens: int | None
    prompt_tokens: int | None
    throughput_tok_s: float | None = field(init=False)

    def __post_init__(self) -> None:
        gen_ms = self.total_ms - (self.ttft_ms or 0)
        if self.completion_tokens and gen_ms > 0:
            self.throughput_tok_s = round(self.completion_tokens / (gen_ms / 1000), 2)
        else:
            self.throughput_tok_s = None


@runtime_checkable
class Workload(Protocol):
    @property
    def spec(self) -> WorkloadSpec: ...

    def build_payload(self, model_id: str) -> dict[str, Any]: ...

    def run(self, session, url: str, payload: dict[str, Any]) -> RawRun: ...

    def metrics(self, raw: RawRun) -> BenchmarkMetrics: ...
```

- [ ] **Step 4: Run test to verify it passes**

Run: `uv run pytest tests/benchmark/test_workloads.py::test_raw_run_defaults tests/benchmark/test_workloads.py::test_metrics_computes_throughput -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add src/modelman/benchmark/workloads tests/benchmark/test_workloads.py
git commit -m "feat(benchmark): add workload base types and protocol"
```

---

## Task 4: Implement built-in workloads

**Files:**
- Create: `src/modelman/benchmark/workloads/chat_streaming.py`
- Create: `src/modelman/benchmark/workloads/short.py`
- Create: `src/modelman/benchmark/workloads/long.py`
- Create: `src/modelman/benchmark/workloads/code.py`

Each workload shares a streaming request helper. First create a shared `_streaming_run` in `base.py`, then each workload module defines its spec and wraps the helper.

- [ ] **Step 1: Add shared streaming request helper to `base.py`**

Modify `src/modelman/benchmark/workloads/base.py` to add:

```python
import json
import time
from typing import Any

import requests


def _streaming_run(
    session: requests.Session,
    url: str,
    payload: dict[str, Any],
) -> RawRun:
    """Execute a streaming chat request and return raw timing + chunks."""
    start = time.perf_counter()
    first_token: float | None = None
    chunks: list[dict[str, Any]] = []
    completion_tokens: int | None = None
    prompt_tokens: int | None = None
    error: str | None = None

    try:
        with session.post(url, json=payload, stream=True, timeout=600) as resp:
            resp.raise_for_status()
            for line in resp.iter_lines():
                if not line:
                    continue
                decoded = line.decode("utf-8")
                if not decoded.startswith("data: "):
                    continue
                data = decoded[6:]
                if data == "[DONE]":
                    break
                try:
                    chunk = json.loads(data)
                except json.JSONDecodeError:
                    continue
                chunks.append(chunk)
                if first_token is None and _chunk_has_content(chunk):
                    first_token = time.perf_counter()
                usage = chunk.get("usage")
                if isinstance(usage, dict):
                    if "completion_tokens" in usage:
                        completion_tokens = usage["completion_tokens"]
                    if "prompt_tokens" in usage:
                        prompt_tokens = usage["prompt_tokens"]
    except requests.RequestException as exc:
        error = str(exc)
        start = start or time.perf_counter()

    end = time.perf_counter()
    return RawRun(
        start_time=start,
        end_time=end,
        first_token_time=first_token,
        chunks=chunks,
        completion_tokens=completion_tokens,
        prompt_tokens=prompt_tokens,
        error=error,
    )


def _chunk_has_content(chunk: dict[str, Any]) -> bool:
    choices = chunk.get("choices")
    if not isinstance(choices, list) or not choices:
        return False
    delta = choices[0].get("delta", {})
    return bool(delta.get("content"))
```

- [ ] **Step 2: Implement `chat` workload**

`src/modelman/benchmark/workloads/chat_streaming.py`:

```python
"""Default chat streaming workload."""

from __future__ import annotations

from typing import Any

import requests

from .base import (
    BenchmarkMetrics,
    RawRun,
    WorkloadSpec,
    _streaming_run,
)

SPEC = WorkloadSpec(
    name="chat",
    display_name="Chat (REST vs GraphQL)",
    prompt=(
        "Explain in detail the differences between REST and GraphQL APIs, "
        "including trade-offs in caching, partial responses, and tooling. Be thorough."
    ),
    max_tokens=200,
    temperature=0.0,
    stream=True,
    stream_options={"include_usage": True},
)


class ChatStreamingWorkload:
    spec = SPEC

    def build_payload(self, model_id: str) -> dict[str, Any]:
        return {
            "model": model_id,
            "messages": [{"role": "user", "content": self.spec.prompt}],
            "max_tokens": self.spec.max_tokens,
            "temperature": self.spec.temperature,
            "stream": self.spec.stream,
            "stream_options": self.spec.stream_options,
        }

    def run(self, session: requests.Session, url: str, payload: dict[str, Any]) -> RawRun:
        return _streaming_run(session, url, payload)

    def metrics(self, raw: RawRun) -> BenchmarkMetrics:
        if raw.error:
            return BenchmarkMetrics(
                ttft_ms=None,
                total_ms=int((raw.end_time - raw.start_time) * 1000),
                completion_tokens=None,
                prompt_tokens=None,
            )
        ttft_ms = (
            int((raw.first_token_time - raw.start_time) * 1000)
            if raw.first_token_time
            else None
        )
        total_ms = int((raw.end_time - raw.start_time) * 1000)
        return BenchmarkMetrics(
            ttft_ms=ttft_ms,
            total_ms=total_ms,
            completion_tokens=raw.completion_tokens,
            prompt_tokens=raw.prompt_tokens,
        )


__all__ = ["ChatStreamingWorkload"]
```

- [ ] **Step 3: Implement `short`, `long`, `code` workloads**

`src/modelman/benchmark/workloads/short.py`:

```python
"""Short warmup / TTFT workload."""

from __future__ import annotations

from typing import Any

import requests

from .base import BenchmarkMetrics, RawRun, WorkloadSpec, _streaming_run

SPEC = WorkloadSpec(
    name="short",
    display_name="Short (hi)",
    prompt="hi",
    max_tokens=1,
    temperature=0.0,
    stream=True,
    stream_options={"include_usage": True},
)


class ShortStreamingWorkload:
    spec = SPEC

    def build_payload(self, model_id: str) -> dict[str, Any]:
        return {
            "model": model_id,
            "messages": [{"role": "user", "content": self.spec.prompt}],
            "max_tokens": self.spec.max_tokens,
            "temperature": self.spec.temperature,
            "stream": self.spec.stream,
            "stream_options": self.spec.stream_options,
        }

    def run(self, session: requests.Session, url: str, payload: dict[str, Any]) -> RawRun:
        return _streaming_run(session, url, payload)

    def metrics(self, raw: RawRun) -> BenchmarkMetrics:
        if raw.error:
            return BenchmarkMetrics(
                ttft_ms=None,
                total_ms=int((raw.end_time - raw.start_time) * 1000),
                completion_tokens=None,
                prompt_tokens=None,
            )
        ttft_ms = (
            int((raw.first_token_time - raw.start_time) * 1000)
            if raw.first_token_time
            else None
        )
        total_ms = int((raw.end_time - raw.start_time) * 1000)
        return BenchmarkMetrics(
            ttft_ms=ttft_ms,
            total_ms=total_ms,
            completion_tokens=raw.completion_tokens,
            prompt_tokens=raw.prompt_tokens,
        )


__all__ = ["ShortStreamingWorkload"]
```

`src/modelman/benchmark/workloads/long.py`:

```python
"""Long sustained-throughput workload."""

from __future__ import annotations

from typing import Any

import requests

from .chat_streaming import ChatStreamingWorkload


class LongStreamingWorkload(ChatStreamingWorkload):
    spec = WorkloadSpec(  # type: ignore[assignment]
        name="long",
        display_name="Long (REST vs GraphQL, 1024 tokens)",
        prompt=(
            "Explain in detail the differences between REST and GraphQL APIs, "
            "including trade-offs in caching, partial responses, and tooling. Be thorough."
        ),
        max_tokens=1024,
        temperature=0.0,
        stream=True,
        stream_options={"include_usage": True},
    )


__all__ = ["LongStreamingWorkload"]
```

Wait — `WorkloadSpec` is not imported in `long.py`. Fix:

```python
from .base import WorkloadSpec
from .chat_streaming import ChatStreamingWorkload
```

`src/modelman/benchmark/workloads/code.py`:

```python
"""Code generation workload."""

from __future__ import annotations

from typing import Any

import requests

from .base import BenchmarkMetrics, RawRun, WorkloadSpec, _streaming_run

SPEC = WorkloadSpec(
    name="code",
    display_name="Code (merge sorted lists)",
    prompt="Write a Python function that merges two sorted lists into one sorted list. Do not use built-in sort.",
    max_tokens=200,
    temperature=0.0,
    stream=True,
    stream_options={"include_usage": True},
)


class CodeStreamingWorkload:
    spec = SPEC

    def build_payload(self, model_id: str) -> dict[str, Any]:
        return {
            "model": model_id,
            "messages": [{"role": "user", "content": self.spec.prompt}],
            "max_tokens": self.spec.max_tokens,
            "temperature": self.spec.temperature,
            "stream": self.spec.stream,
            "stream_options": self.spec.stream_options,
        }

    def run(self, session: requests.Session, url: str, payload: dict[str, Any]) -> RawRun:
        return _streaming_run(session, url, payload)

    def metrics(self, raw: RawRun) -> BenchmarkMetrics:
        if raw.error:
            return BenchmarkMetrics(
                ttft_ms=None,
                total_ms=int((raw.end_time - raw.start_time) * 1000),
                completion_tokens=None,
                prompt_tokens=None,
            )
        ttft_ms = (
            int((raw.first_token_time - raw.start_time) * 1000)
            if raw.first_token_time
            else None
        )
        total_ms = int((raw.end_time - raw.start_time) * 1000)
        return BenchmarkMetrics(
            ttft_ms=ttft_ms,
            total_ms=total_ms,
            completion_tokens=raw.completion_tokens,
            prompt_tokens=raw.prompt_tokens,
        )


__all__ = ["CodeStreamingWorkload"]
```

- [ ] **Step 4: Write tests for workloads**

`tests/benchmark/test_workloads.py` (append):

```python
from unittest.mock import MagicMock

from modelman.benchmark.workloads.chat_streaming import ChatStreamingWorkload
from modelman.benchmark.workloads.code import CodeStreamingWorkload
from modelman.benchmark.workloads.long import LongStreamingWorkload
from modelman.benchmark.workloads.short import ShortStreamingWorkload


def _fake_response(lines: list[str]):
    resp = MagicMock()
    resp.status_code = 200
    resp.raise_for_status = MagicMock()
    resp.iter_lines.return_value = [line.encode("utf-8") for line in lines]
    resp.__enter__ = MagicMock(return_value=resp)
    resp.__exit__ = MagicMock(return_value=False)
    return resp


def test_chat_workload_builds_payload():
    workload = ChatStreamingWorkload()
    payload = workload.build_payload("ollama/ornith-1.5:35b")
    assert payload["model"] == "ollama/ornith-1.5:35b"
    assert payload["max_tokens"] == 200
    assert payload["temperature"] == 0.0


def test_chat_workload_run_counts_tokens():
    workload = ChatStreamingWorkload()
    session = MagicMock()
    lines = [
        'data: {"choices":[{"delta":{"content":"Hello"}}]}',
        'data: {"choices":[],"usage":{"completion_tokens":50,"prompt_tokens":10}}',
        "data: [DONE]",
    ]
    session.post.return_value = _fake_response(lines)

    raw = workload.run(session, "http://localhost:4000/v1/chat/completions", workload.build_payload("x"))

    assert raw.error is None
    assert raw.completion_tokens == 50
    assert raw.prompt_tokens == 10
    assert raw.first_token_time is not None


def test_short_workload_one_token():
    workload = ShortStreamingWorkload()
    assert workload.spec.max_tokens == 1


def test_code_workload_prompt():
    workload = CodeStreamingWorkload()
    assert "merge two sorted lists" in workload.spec.prompt


def test_long_workload_inherits_chat():
    workload = LongStreamingWorkload()
    assert workload.spec.max_tokens == 1024
```

- [ ] **Step 5: Run tests**

Run: `uv run pytest tests/benchmark/test_workloads.py -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add src/modelman/benchmark/workloads tests/benchmark/test_workloads.py
git commit -m "feat(benchmark): add short/chat/long/code streaming workloads"
```

---

## Task 5: Create workload registry

**Files:**
- Modify: `src/modelman/benchmark/workloads/__init__.py`

- [ ] **Step 1: Write the failing test**

`tests/benchmark/test_workload_registry.py`:

```python
from modelman.benchmark.workloads import get_workload, list_workloads


def test_list_workloads_includes_defaults():
    names = list_workloads()
    assert set(names) >= {"short", "chat", "long", "code"}


def test_get_workload_chat():
    workload = get_workload("chat")
    assert workload.spec.name == "chat"


def test_get_workload_unknown_raises():
    from modelman.benchmark.errors import BenchmarkError

    try:
        get_workload("nonexistent")
        raise AssertionError("expected BenchmarkError")
    except BenchmarkError:
        pass
```

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/benchmark/test_workload_registry.py -v`
Expected: `AttributeError` for `get_workload` / `list_workloads`.

- [ ] **Step 3: Write implementation**

`src/modelman/benchmark/workloads/__init__.py`:

```python
"""Built-in benchmark workloads."""

from __future__ import annotations

from typing import Any

from modelman.benchmark.errors import BenchmarkError

from .base import Workload
from .chat_streaming import ChatStreamingWorkload
from .code import CodeStreamingWorkload
from .long import LongStreamingWorkload
from .short import ShortStreamingWorkload

_WORKLOADS: dict[str, Any] = {
    "short": ShortStreamingWorkload,
    "chat": ChatStreamingWorkload,
    "long": LongStreamingWorkload,
    "code": CodeStreamingWorkload,
}


def list_workloads() -> list[str]:
    """Return the names of all registered workloads."""
    return sorted(_WORKLOADS.keys())


def get_workload(name: str) -> Workload:
    """Return a workload instance by name."""
    cls = _WORKLOADS.get(name)
    if cls is None:
        raise BenchmarkError(f"unknown workload: {name}")
    return cls()


__all__ = ["get_workload", "list_workloads", "Workload"]
```

- [ ] **Step 4: Run test to verify it passes**

Run: `uv run pytest tests/benchmark/test_workload_registry.py -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add src/modelman/benchmark/workloads/__init__.py tests/benchmark/test_workload_registry.py
git commit -m "feat(benchmark): add workload registry"
```

---

## Task 6: Create isolation contract

**Files:**
- Create: `src/modelman/benchmark/isolation.py`
- Create: `tests/benchmark/test_isolation.py`

- [ ] **Step 1: Write the failing test**

`tests/benchmark/test_isolation.py`:

```python
from unittest.mock import patch

from modelman.benchmark.errors import BenchmarkError
from modelman.benchmark.isolation import isolate_provider, restore_providers


def test_isolate_provider_success():
    with patch("modelman.benchmark.isolation.subprocess.run") as mock_run:
        mock_run.return_value.returncode = 0
        mock_run.return_value.stdout = '{"provider":"ollama","model":"ornith-1.5:35b","direct_url":"http://localhost:11434/v1/chat/completions","ok":true,"error":null}\n'
        mock_run.return_value.stderr = ""
        result = isolate_provider("ollama")
        assert result.provider == "ollama"
        assert result.direct_url == "http://localhost:11434/v1/chat/completions"
        assert result.ok is True


def test_isolate_provider_failure_raises():
    with patch("modelman.benchmark.isolation.subprocess.run") as mock_run:
        mock_run.return_value.returncode = 1
        mock_run.return_value.stdout = ""
        mock_run.return_value.stderr = "ollama not reachable"
        try:
            isolate_provider("ollama")
            raise AssertionError("expected BenchmarkError")
        except BenchmarkError as exc:
            assert "ollama not reachable" in str(exc)


def test_restore_providers_success():
    with patch("modelman.benchmark.isolation.subprocess.run") as mock_run:
        mock_run.return_value.returncode = 0
        restore_providers()
        mock_run.assert_called_once()
```

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/benchmark/test_isolation.py -v`
Expected: `ModuleNotFoundError` for `modelman.benchmark.isolation`.

- [ ] **Step 3: Write implementation**

`src/modelman/benchmark/isolation.py`:

```python
"""Subprocess contract with local-ai-setup isolation helpers."""

from __future__ import annotations

import json
import shutil
import subprocess
from dataclasses import dataclass

from modelman.benchmark.errors import BenchmarkError


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
            f"isolation helper '{name}' not found on PATH. "
            "Ensure local-ai-setup/bin is on PATH."
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
    subprocess.run([helper], capture_output=True, text=True, check=False)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `uv run pytest tests/benchmark/test_isolation.py -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add src/modelman/benchmark/isolation.py tests/benchmark/test_isolation.py
git commit -m "feat(benchmark): add isolation subprocess contract"
```

---

## Task 7: Create results writer

**Files:**
- Create: `src/modelman/benchmark/results.py`
- Create: `tests/benchmark/test_results.py`

- [ ] **Step 1: Write the failing test**

`tests/benchmark/test_results.py`:

```python
import json
from datetime import datetime, timezone
from pathlib import Path

from modelman.benchmark.results import BenchmarkRun, TargetResult, write_results
from modelman.benchmark.workloads.base import BenchmarkMetrics


def test_write_results_creates_json_and_markdown(tmp_path):
    run = BenchmarkRun(
        run_id="20260905-143200",
        workload_name="chat",
        started_at=datetime(2026, 8, 28, 14, 32, 0, tzinfo=timezone.utc),
        results=[
            TargetResult(
                model_id="ollama/ornith-1.5:35b",
                provider_id="ollama",
                route="direct",
                pass_number=1,
                metrics=BenchmarkMetrics(ttft_ms=100, total_ms=500, completion_tokens=100, prompt_tokens=10),
            )
        ],
    )
    write_results(run, tmp_path)

    json_path = tmp_path / "20260905-143200" / "results.json"
    md_path = tmp_path / "20260905-143200" / "summary.md"
    payload_path = tmp_path / "20260905-143200" / "payload.json"
    assert json_path.exists()
    assert md_path.exists()
    assert payload_path.exists()

    data = json.loads(json_path.read_text())
    assert data["run_id"] == "20260905-143200"
    assert data["results"][0]["route"] == "direct"

    md = md_path.read_text()
    assert "ollama/ornith-1.5:35b" in md
    assert "100" in md
```

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/benchmark/test_results.py -v`
Expected: `ModuleNotFoundError`.

- [ ] **Step 3: Write implementation**

`src/modelman/benchmark/results.py`:

```python
"""Benchmark result writers."""

from __future__ import annotations

import json
from dataclasses import asdict, dataclass, field
from datetime import datetime
from pathlib import Path
from statistics import median
from typing import Any

from modelman.benchmark.workloads.base import BenchmarkMetrics


@dataclass
class TargetResult:
    model_id: str
    provider_id: str
    route: str  # "direct" or "litellm"
    pass_number: int
    metrics: BenchmarkMetrics
    error: str | None = None


@dataclass
class BenchmarkRun:
    run_id: str
    workload_name: str
    started_at: datetime
    results: list[TargetResult]
    metadata: dict[str, Any] = field(default_factory=dict)


def _metrics_to_dict(m: BenchmarkMetrics) -> dict[str, Any]:
    return {
        "ttft_ms": m.ttft_ms,
        "total_ms": m.total_ms,
        "completion_tokens": m.completion_tokens,
        "prompt_tokens": m.prompt_tokens,
        "throughput_tok_s": m.throughput_tok_s,
    }


def _target_to_dict(t: TargetResult) -> dict[str, Any]:
    return {
        "model_id": t.model_id,
        "provider_id": t.provider_id,
        "route": t.route,
        "pass_number": t.pass_number,
        "metrics": _metrics_to_dict(t.metrics),
        "error": t.error,
    }


def _median(values: list[float | int]) -> float | None:
    if not values:
        return None
    return float(median(values))


def _aggregate(results: list[TargetResult]) -> dict[str, dict[str, Any]]:
    groups: dict[tuple[str, str], list[TargetResult]] = {}
    for r in results:
        key = (r.model_id, r.route)
        groups.setdefault(key, []).append(r)

    summary: dict[str, dict[str, Any]] = {}
    for (model_id, route), records in sorted(groups.items()):
        valid = [r for r in records if r.error is None]
        summary[f"{model_id} ({route})"] = {
            "passes": len(records),
            "successful": len(valid),
            "ttft_ms": _median([r.metrics.ttft_ms for r in valid if r.metrics.ttft_ms is not None]),
            "total_ms": _median([r.metrics.total_ms for r in valid]),
            "throughput_tok_s": _median(
                [r.metrics.throughput_tok_s for r in valid if r.metrics.throughput_tok_s is not None]
            ),
        }
    return summary


def _render_markdown(run: BenchmarkRun) -> str:
    lines: list[str] = [
        f"# Benchmark Results — {run.run_id}",
        "",
        f"- **Workload:** {run.workload_name}",
        f"- **Started:** {run.started_at.isoformat()}",
        f"- **Records:** {len(run.results)}",
        "",
        "## Summary (median per model / route)",
        "",
        "| Model (route) | Passes | TTFT (ms) | Total (ms) | Throughput (tok/s) |",
        "|---------------|-------:|----------:|-----------:|-------------------:|",
    ]
    for key, vals in _aggregate(run.results).items():
        lines.append(
            f"| {key} | {vals['passes']} | "
            f"{vals['ttft_ms'] if vals['ttft_ms'] is not None else 'N/A'} | "
            f"{vals['total_ms'] if vals['total_ms'] is not None else 'N/A'} | "
            f"{vals['throughput_tok_s'] if vals['throughput_tok_s'] is not None else 'N/A'} |"
        )
    lines.append("")
    lines.append("## Raw per-pass records")
    lines.append("")
    lines.append("| Model | Route | Pass | TTFT (ms) | Total (ms) | Tokens | Throughput |")
    lines.append("|-------|-------|-----:|----------:|-----------:|-------:|-----------:|")
    for r in sorted(run.results, key=lambda x: (x.model_id, x.route, x.pass_number)):
        m = r.metrics
        lines.append(
            f"| {r.model_id} | {r.route} | {r.pass_number} | "
            f"{m.ttft_ms if m.ttft_ms is not None else 'N/A'} | "
            f"{m.total_ms} | "
            f"{m.completion_tokens if m.completion_tokens is not None else 'N/A'} | "
            f"{m.throughput_tok_s if m.throughput_tok_s is not None else 'N/A'} |"
        )
    lines.append("")
    return "\n".join(lines)


def write_results(run: BenchmarkRun, base_dir: Path) -> Path:
    """Write JSON, Markdown, and payload artifacts for a run."""
    run_dir = base_dir / run.run_id
    run_dir.mkdir(parents=True, exist_ok=True)

    payload = {
        "run_id": run.run_id,
        "workload_name": run.workload_name,
        "started_at": run.started_at.isoformat(),
        "metadata": run.metadata,
    }
    (run_dir / "payload.json").write_text(json.dumps(payload, indent=2), encoding="utf-8")

    results = {
        "run_id": run.run_id,
        "workload_name": run.workload_name,
        "started_at": run.started_at.isoformat(),
        "results": [_target_to_dict(r) for r in run.results],
        "summary": _aggregate(run.results),
        "metadata": run.metadata,
    }
    (run_dir / "results.json").write_text(json.dumps(results, indent=2), encoding="utf-8")

    (run_dir / "summary.md").write_text(_render_markdown(run), encoding="utf-8")

    return run_dir
```

- [ ] **Step 4: Run test to verify it passes**

Run: `uv run pytest tests/benchmark/test_results.py -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add src/modelman/benchmark/results.py tests/benchmark/test_results.py
git commit -m "feat(benchmark): add JSON and Markdown result writers"
```

---

## Task 8: Create benchmark runner

**Files:**
- Create: `src/modelman/benchmark/runner.py`
- Create: `tests/benchmark/test_runner.py`

- [ ] **Step 1: Write the failing test**

`tests/benchmark/test_runner.py`:

```python
from unittest.mock import MagicMock, patch

from modelman.benchmark.runner import discover_targets
from modelman.registry import ModelEntry, ProviderEntry, Registry
from modelman.state import ModelState, StateStore


def test_discover_targets_defaults_to_exposed_local_models():
    registry = Registry(
        providers=[
            ProviderEntry(id="ollama", name="Ollama", location="local"),
            ProviderEntry(id="openrouter", name="OpenRouter", location="remote"),
        ],
        models=[
            ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a"),
            ModelEntry(id="ollama/b", family="f", provider_id="ollama", model_name="b"),
            ModelEntry(id="openrouter/c", family="f", provider_id="openrouter", model_name="c"),
        ],
    )
    state = StateStore()
    state.set("ollama/a", ModelState(litellm_exposed=True))
    state.set("ollama/b", ModelState(litellm_exposed=False))

    targets = discover_targets(registry, state)
    assert [t.model_id for t in targets] == ["ollama/a"]


def test_discover_targets_by_family_overrides_exposed():
    registry = Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", location="local")],
        models=[
            ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a"),
            ModelEntry(id="ollama/b", family="g", provider_id="ollama", model_name="b"),
        ],
    )
    state = StateStore()
    targets = discover_targets(registry, state, family="f")
    assert [t.model_id for t in targets] == ["ollama/a"]


def test_discover_targets_by_model_ids():
    registry = Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", location="local")],
        models=[ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a")],
    )
    state = StateStore()
    targets = discover_targets(registry, state, model_ids=["ollama/a"])
    assert [t.model_id for t in targets] == ["ollama/a"]
```

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/benchmark/test_runner.py -v`
Expected: `ModuleNotFoundError`.

- [ ] **Step 3: Write implementation**

`src/modelman/benchmark/runner.py`:

```python
"""Benchmark runner orchestration."""

from __future__ import annotations

import time
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path

import requests

from modelman.benchmark.errors import BenchmarkError
from modelman.benchmark.isolation import isolate_provider, restore_providers
from modelman.benchmark.results import BenchmarkRun, TargetResult, write_results
from modelman.benchmark.workloads import Workload, get_workload
from modelman.registry import Registry
from modelman.state import StateStore


LOCAL_PROVIDERS = {"ollama", "llamacpp", "omlx"}
LITELLM_URL = "http://localhost:4000/v1/chat/completions"


@dataclass
class Target:
    model_id: str
    provider_id: str
    model_name: str
    family: str


def discover_targets(
    registry: Registry,
    state: StateStore,
    model_ids: list[str] | None = None,
    family: str | None = None,
) -> list[Target]:
    """Return benchmark targets based on registry + state + CLI filters."""
    targets: list[Target] = []
    for model in registry.models:
        if model.provider_id not in LOCAL_PROVIDERS:
            continue
        if family is not None and model.family != family:
            continue
        if model_ids is not None and model.id not in model_ids:
            continue
        if model_ids is None and family is None:
            # Default: only exposed local models.
            if not state.get(model.id).litellm_exposed:
                continue
        targets.append(
            Target(
                model_id=model.id,
                provider_id=model.provider_id,
                model_name=model.model_name,
                family=model.family,
            )
        )
    return targets


def _run_route(
    session: requests.Session,
    target: Target,
    route: str,
    url: str,
    workload: Workload,
    pass_number: int,
) -> TargetResult:
    payload = workload.build_payload(
        target.model_name if route == "direct" else target.model_id
    )
    raw = workload.run(session, url, payload)
    metrics = workload.metrics(raw)
    return TargetResult(
        model_id=target.model_id,
        provider_id=target.provider_id,
        route=route,
        pass_number=pass_number,
        metrics=metrics,
        error=raw.error,
    )


def run_benchmark(
    registry: Registry,
    state: StateStore,
    workload: Workload,
    *,
    model_ids: list[str] | None = None,
    family: str | None = None,
    passes: int = 1,
    cooldown_seconds: float = 15.0,
    routes: list[str] | None = None,
    results_dir: Path | None = None,
) -> BenchmarkRun:
    """Run a benchmark across targets and write results."""
    routes = routes or ["direct", "litellm"]
    results_dir = results_dir or Path.home() / ".config" / "local-ai" / "benchmarks"

    targets = discover_targets(registry, state, model_ids=model_ids, family=family)
    if not targets:
        raise BenchmarkError("no benchmark targets found")

    run_id = datetime.now(timezone.utc).strftime("%Y%m%d-%H%M%S")
    run = BenchmarkRun(
        run_id=run_id,
        workload_name=workload.spec.name,
        started_at=datetime.now(timezone.utc),
        results=[],
    )

    session = requests.Session()
    try:
        for target in targets:
            try:
                isolate = isolate_provider(target.provider_id)
                direct_url = isolate.direct_url
            except BenchmarkError as exc:
                run.results.append(
                    TargetResult(
                        model_id=target.model_id,
                        provider_id=target.provider_id,
                        route="direct",
                        pass_number=0,
                        metrics=workload.metrics(None),  # type: ignore[arg-type]
                        error=str(exc),
                    )
                )
                continue

            for pass_number in range(1, passes + 1):
                for route in routes:
                    url = direct_url if route == "direct" else LITELLM_URL
                    result = _run_route(session, target, route, url, workload, pass_number)
                    run.results.append(result)
                if pass_number < passes:
                    time.sleep(cooldown_seconds)
    finally:
        restore_providers()

    write_results(run, results_dir)
    return run
```

Note: `workload.metrics(None)` will fail because it expects `RawRun`. We need to handle isolation failure more cleanly. Fix:

```python
            except BenchmarkError as exc:
                for route in routes:
                    for pass_number in range(1, passes + 1):
                        run.results.append(
                            TargetResult(
                                model_id=target.model_id,
                                provider_id=target.provider_id,
                                route=route,
                                pass_number=pass_number,
                                metrics=BenchmarkMetrics(
                                    ttft_ms=None,
                                    total_ms=0,
                                    completion_tokens=None,
                                    prompt_tokens=None,
                                ),
                                error=str(exc),
                            )
                        )
                continue
```

Replace the broken block with this.

- [ ] **Step 4: Run test to verify it passes**

Run: `uv run pytest tests/benchmark/test_runner.py -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add src/modelman/benchmark/runner.py tests/benchmark/test_runner.py
git commit -m "feat(benchmark): add target discovery and runner"
```

---

## Task 9: Create CLI and wire into main

**Files:**
- Create: `src/modelman/benchmark/cli.py`
- Modify: `src/modelman/main.py`

- [ ] **Step 1: Write the failing test**

`tests/benchmark/test_cli.py`:

```python
from click.testing import CliRunner

from modelman.main import app


def test_benchmark_list_workloads():
    runner = CliRunner()
    result = runner.invoke(app, ["benchmark", "list-workloads"])
    assert result.exit_code == 0
    assert "chat" in result.output
```

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/benchmark/test_cli.py -v`
Expected: `UsageError` or exit code 2 because `benchmark` subcommand does not exist.

- [ ] **Step 3: Write implementation**

`src/modelman/benchmark/cli.py`:

```python
"""CLI for modelman benchmark subcommand."""

from __future__ import annotations

import os
from pathlib import Path

import typer

from modelman.benchmark.errors import BenchmarkError
from modelman.benchmark.runner import run_benchmark
from modelman.benchmark.workloads import get_workload, list_workloads
from modelman.registry import load_registry
from modelman.state import load_state, save_state

benchmark_app = typer.Typer(help="Benchmark local LLM models.")


@benchmark_app.command("list-workloads")
def list_workloads_cmd() -> None:
    """List built-in benchmark workloads."""
    for name in list_workloads():
        typer.echo(name)


@benchmark_app.command("run")
def run_cmd(
    workload: str = typer.Option("chat", "--workload", help="Workload name"),
    model: list[str] = typer.Option([], "--model", help="Registry model id(s) to benchmark"),
    family: str | None = typer.Option(None, "--family", help="Benchmark all models in a family"),
    direct: bool = typer.Option(False, "--direct", help="Only benchmark direct backend access"),
    litellm: bool = typer.Option(False, "--litellm", help="Only benchmark via LiteLLM"),
    passes: int = typer.Option(1, "--passes", min=1, help="Number of passes per target"),
    cooldown: float = typer.Option(15.0, "--cooldown", help="Seconds between passes"),
    results_dir: Path | None = typer.Option(None, "--results-dir", help="Directory for result artifacts"),
) -> None:
    """Run a benchmark workload against local models."""
    routes = ["direct", "litellm"]
    if direct and litellm:
        typer.echo("error: --direct and --litellm are mutually exclusive", err=True)
        raise typer.Exit(1)
    if direct:
        routes = ["direct"]
    if litellm:
        routes = ["litellm"]

    workload_name = os.environ.get("MODELMAN_BENCHMARK_WORKLOAD", workload)
    try:
        workload_obj = get_workload(workload_name)
    except BenchmarkError as exc:
        typer.echo(f"error: {exc}", err=True)
        raise typer.Exit(1) from exc

    registry = load_registry()
    state = load_state()
    try:
        run = run_benchmark(
            registry,
            state,
            workload_obj,
            model_ids=model or None,
            family=family,
            passes=passes,
            cooldown_seconds=cooldown,
            routes=routes,
            results_dir=results_dir,
        )
    except BenchmarkError as exc:
        typer.echo(f"error: {exc}", err=True)
        raise typer.Exit(1) from exc

    # Record latest run pointer in state.
    from modelman.benchmark.results import BenchmarkRun
    state.extra["benchmarks"] = {
        "last_run": run.started_at.isoformat(),
        "last_run_dir": str(results_dir / run.run_id) if results_dir else None,
    }
    save_state(state)

    typer.echo(f"Benchmark complete: {run.run_id}")
    typer.echo(f"Results: {results_dir / run.run_id}" if results_dir else f"Results: ~/.config/local-ai/benchmarks/{run.run_id}")


@benchmark_app.command("show-results")
def show_results_cmd(
    latest: bool = typer.Option(False, "--latest", help="Show the latest run"),
    run_id: str | None = typer.Option(None, "--run-id", help="Run id to show"),
) -> None:
    """Print the Markdown summary for a benchmark run."""
    if not latest and not run_id:
        typer.echo("error: specify --latest or --run-id", err=True)
        raise typer.Exit(1)

    if latest:
        state = load_state()
        info = state.extra.get("benchmarks", {})
        run_dir = info.get("last_run_dir")
        if not run_dir:
            typer.echo("error: no latest run recorded", err=True)
            raise typer.Exit(1)
        md_path = Path(run_dir) / "summary.md"
    else:
        base = Path.home() / ".config" / "local-ai" / "benchmarks"
        md_path = base / run_id / "summary.md"

    if not md_path.exists():
        typer.echo(f"error: results not found: {md_path}", err=True)
        raise typer.Exit(1)

    typer.echo(md_path.read_text(encoding="utf-8"))


__all__ = ["benchmark_app"]
```

There's a bug: `state.extra["benchmarks"]` may already exist and we'd overwrite other keys. Use update:

```python
    benchmarks = state.extra.setdefault("benchmarks", {})
    benchmarks["last_run"] = run.started_at.isoformat()
    benchmarks["last_run_dir"] = str(results_dir / run.run_id) if results_dir else None
```

Also the echo line is too long. Split it. Use a variable for `run_dir`.

Fix the show-results default base path to match the runner default. Extract a constant `DEFAULT_RESULTS_DIR` in runner.py and use it in both runner.py and cli.py.

Add to `runner.py`:

```python
DEFAULT_RESULTS_DIR = Path.home() / ".config" / "local-ai" / "benchmarks"
```

Then import it in cli.py.

Modify `run_benchmark`:

```python
    results_dir = results_dir or DEFAULT_RESULTS_DIR
```

Modify CLI `show_results_cmd`:

```python
from modelman.benchmark.runner import DEFAULT_RESULTS_DIR, run_benchmark
...
    else:
        md_path = DEFAULT_RESULTS_DIR / run_id / "summary.md"
```

Modify CLI run_cmd result echo:

```python
    output_dir = results_dir / run.run_id if results_dir else DEFAULT_RESULTS_DIR / run.run_id
    typer.echo(f"Benchmark complete: {run.run_id}")
    typer.echo(f"Results: {output_dir}")
```

Now modify `src/modelman/main.py`:

```python
from .benchmark.cli import benchmark_app
...
app = typer.Typer(help="Manage local LLM model families across providers.")
...
app.add_typer(benchmark_app, name="benchmark")
```

Insert after `app = typer.Typer(...)`.

- [ ] **Step 4: Run test to verify it passes**

Run: `uv run pytest tests/benchmark/test_cli.py -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add src/modelman/benchmark/cli.py src/modelman/main.py tests/benchmark/test_cli.py
git commit -m "feat(benchmark): add CLI subcommand and wire into modelman"
```

---

## Task 10: Add state pointer persistence test

**Files:**
- Create: `tests/benchmark/test_state_pointer.py`

- [ ] **Step 1: Write the failing test**

```python
from pathlib import Path
from unittest.mock import patch

from click.testing import CliRunner

from modelman.main import app


def test_run_saves_latest_state_pointer(tmp_path):
    with patch("modelman.benchmark.cli.load_registry") as mock_registry, \
         patch("modelman.benchmark.cli.load_state") as mock_state, \
         patch("modelman.benchmark.cli.save_state") as mock_save, \
         patch("modelman.benchmark.cli.run_benchmark") as mock_run:
        from modelman.benchmark.results import BenchmarkRun
        from datetime import datetime, timezone
        mock_run.return_value = BenchmarkRun(
            run_id="20260905-143200",
            workload_name="chat",
            started_at=datetime(2026, 8, 28, 14, 32, 0, tzinfo=timezone.utc),
            results=[],
        )
        state = mock_state.return_value
        state.extra = {}

        runner = CliRunner()
        result = runner.invoke(
            app,
            ["benchmark", "run", "--results-dir", str(tmp_path)],
        )
        assert result.exit_code == 0, result.output
        assert mock_save.called
        assert state.extra["benchmarks"]["last_run"] == "2026-08-28T14:32:00+00:00"
```

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/benchmark/test_state_pointer.py -v`
Expected: FAIL because state pointer not yet saved in CLI (if previous task was already committed, this may pass; write the test to lock the behavior).

- [ ] **Step 3: Ensure implementation already exists from Task 9**

The CLI already updates `state.extra["benchmarks"]`. If the test passes, that's the intended behavior.

- [ ] **Step 4: Run test to verify it passes**

Run: `uv run pytest tests/benchmark/test_state_pointer.py -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add tests/benchmark/test_state_pointer.py
git commit -m "test(benchmark): verify latest-run state pointer is saved"
```

---

## Task 11: Create local-ai-setup isolation helpers

**Files:**
- Create: `local-ai-setup/bin/llm-isolate-provider`
- Create: `local-ai-setup/bin/llm-restore-providers`

- [ ] **Step 1: Write helpers**

`local-ai-setup/bin/llm-isolate-provider`:

```bash
#!/opt/homebrew/bin/bash
# llm-isolate-provider — stop other local providers, start+warmup one.
# Usage: llm-isolate-provider <provider-id>
# Supported: ollama, llamacpp, omlx
# On success, print JSON with provider/model/direct_url.

set -e

PROVIDER="$1"
if [ -z "$PROVIDER" ]; then
    echo "usage: llm-isolate-provider <provider-id>" >&2
    exit 1
fi

# Default model names / URLs. Override via env for non-standard setups.
OLLAMA_MODEL="${LLM_ISOLATE_OLLAMA_MODEL:-ornith-1.5:35b}"
LLAMACPP_MODEL="${LLM_ISOLATE_LLAMACPP_MODEL:-local-llama}"
OMLX_4BIT_MODEL="${LLM_ISOLATE_OMLX_4BIT_MODEL:-Ornith-1.5-35B-A3B-MLX-4bit}"
OMLX_6BIT_MODEL="${LLM_ISOLATE_OMLX_6BIT_MODEL:-Ornith-1.5-35B-A3B-MLX-6bit}"
OMLX_MODEL="$OMLX_4BIT_MODEL"

warmup_payload() {
    local model="$1"
    python3 - "$model" <<'PYEOF'
import json, sys
m = sys.argv[1]
print(json.dumps({"model": m, "messages": [{"role": "user", "content": "hi"}], "max_tokens": 1, "temperature": 0, "stream": False}))
PYEOF
}

stop_all_local() {
    ollama stop "$OLLAMA_MODEL" 2>/dev/null || true
    omlx stop 2>/dev/null || true
    if [ -f ~/Library/LaunchAgents/local.llamacpp.server.plist ]; then
        launchctl unload ~/Library/LaunchAgents/local.llamacpp.server.plist 2>/dev/null || true
    fi
    sleep 3
}

start_ollama() {
    if ! curl -s -m 2 http://localhost:11434/api/tags >/dev/null 2>&1; then
        launchctl kickstart -k "gui/$(id -u)/com.ollama.ollama" 2>/dev/null || true
    fi
    local payload
    payload=$(warmup_payload "$OLLAMA_MODEL")
    local resp
    resp=$(curl -s -m 120 http://localhost:11434/v1/chat/completions \
        -H "Content-Type: application/json" -d "$payload" 2>/dev/null || true)
    if ! echo "$resp" | grep -q '"object":"chat.completion"'; then
        echo "failed to warm up ollama model $OLLAMA_MODEL" >&2
        exit 1
    fi
    echo "ollama|$OLLAMA_MODEL|http://localhost:11434/v1/chat/completions"
}

start_llamacpp() {
    if [ -f ~/Library/LaunchAgents/local.llamacpp.server.plist ]; then
        launchctl load -w ~/Library/LaunchAgents/local.llamacpp.server.plist 2>/dev/null || true
    fi
    local payload
    payload=$(warmup_payload "$LLAMACPP_MODEL")
    for i in {1..90}; do
        if curl -s -m 2 http://localhost:8080/v1/models >/dev/null 2>&1; then
            local resp
            resp=$(curl -s -m 120 http://localhost:8080/v1/chat/completions \
                -H "Content-Type: application/json" -d "$payload" 2>/dev/null || true)
            if echo "$resp" | grep -q '"object":"chat.completion"'; then
                echo "llamacpp|$LLAMACPP_MODEL|http://localhost:8080/v1/chat/completions"
                return 0
            fi
        fi
        sleep 1
    done
    echo "failed to warm up llamacpp model $LLAMACPP_MODEL" >&2
    exit 1
}

start_omlx() {
    omlx start >/dev/null 2>&1 || true
    local payload
    payload=$(warmup_payload "$OMLX_MODEL")
    for i in {1..90}; do
        if curl -s -m 2 http://localhost:8000/v1/models >/dev/null 2>&1; then
            local resp
            resp=$(curl -s -m 120 http://localhost:8000/v1/chat/completions \
                -H "Content-Type: application/json" -d "$payload" 2>/dev/null || true)
            if echo "$resp" | grep -q '"object":"chat.completion"'; then
                echo "omlx|$OMLX_MODEL|http://localhost:8000/v1/chat/completions"
                return 0
            fi
        fi
        sleep 1
    done
    echo "failed to warm up omlx model $OMLX_MODEL" >&2
    exit 1
}

stop_all_local

case "$PROVIDER" in
    ollama)    OUT=$(start_ollama) ;;
    llamacpp)  OUT=$(start_llamacpp) ;;
    omlx)      OUT=$(start_omlx) ;;
    *)
        echo "unknown provider: $PROVIDER" >&2
        exit 1
        ;;
esac

IFS='|' read -r provider model direct_url <<< "$OUT"
python3 - "$provider" "$model" "$direct_url" <<'PYEOF'
import json, sys
provider, model, direct_url = sys.argv[1:4]
print(json.dumps({"provider": provider, "model": model, "direct_url": direct_url, "ok": True, "error": None}))
PYEOF
```

`local-ai-setup/bin/llm-restore-providers`:

```bash
#!/bin/bash
# llm-restore-providers — bring all local providers back up after a benchmark.

set -e

LOG_PREFIX="[llm-restore-providers]"

if ! curl -s -m 2 http://localhost:11434/api/tags >/dev/null 2>&1; then
    echo "$LOG_PREFIX restarting ollama..."
    launchctl kickstart -k "gui/$(id -u)/com.ollama.ollama" 2>/dev/null || true
fi

if ! curl -s -m 2 http://localhost:8000/v1/models >/dev/null 2>&1; then
    echo "$LOG_PREFIX restarting omlx..."
    omlx start >/dev/null 2>&1 || true
fi

if ! curl -s -m 2 http://localhost:8080/v1/models >/dev/null 2>&1; then
    if [ -f ~/Library/LaunchAgents/local.llamacpp.server.plist ]; then
        echo "$LOG_PREFIX restarting llama.cpp..."
        launchctl load -w ~/Library/LaunchAgents/local.llamacpp.server.plist 2>/dev/null || true
    fi
fi

if ! curl -s -m 2 http://localhost:4000/v1/models >/dev/null 2>&1; then
    if [ -f ~/Library/LaunchAgents/local.litellm.proxy.plist ]; then
        echo "$LOG_PREFIX restarting litellm proxy..."
        launchctl load -w ~/Library/LaunchAgents/local.litellm.proxy.plist 2>/dev/null || true
    fi
fi

echo "$LOG_PREFIX providers restored"
```

- [ ] **Step 2: Make helpers executable and smoke-test syntax**

Run:
```bash
cd /Users/keith/github/ohanaverse/local-ai-setup
chmod +x bin/llm-isolate-provider bin/llm-restore-providers
bash -n bin/llm-isolate-provider
bash -n bin/llm-restore-providers
```
Expected: no syntax errors.

- [ ] **Step 3: Commit**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup
git add bin/llm-isolate-provider bin/llm-restore-providers
git commit -m "feat(benchmark): add service isolation helpers for modelman benchmark"
```

---

## Task 12: Full test suite and lint/typecheck

**Files:**
- All benchmark files

- [ ] **Step 1: Run the full modelman test suite**

Run: `uv run pytest tests/benchmark -q`
Expected: all tests pass.

- [ ] **Step 2: Run the entire project test suite**

Run: `uv run pytest -q`
Expected: all tests pass.

- [ ] **Step 3: Run lint and typecheck**

Run: `make check`
Expected: ruff and mypy clean.

- [ ] **Step 4: Fix any issues**

If mypy complains about `BenchmarkMetrics` field init=False, adjust. If ruff complains about long lines, format. Iterate until clean.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "test(benchmark): full test suite + lint/typecheck clean"
```

---

## Task 13: Update documentation

**Files:**
- Modify: `modelman/README.md`
- Modify: `local-ai-setup/README.md` or `local-ai-setup/docs/Local AI Setup 2026-08-25.md`
- Modify: `agent-worktree/docs/superpowers/plans/2026-08-27-model-management-consolidation-status.md`

- [ ] **Step 1: Add benchmark section to modelman README**

Add a new section near the CLI commands:

```markdown
## Benchmarking

Compare local model backends side-by-side:

```bash
modelman benchmark run
modelman benchmark run --workload short
modelman benchmark run --model ollama/ornith-1.5:35b --direct
modelman benchmark list-workloads
modelman benchmark show-results --latest
```

Results are written to `~/.config/local-ai/benchmarks/<run_id>/` as JSON and Markdown.
```

- [ ] **Step 2: Update local-ai-setup docs**

Add a note that `bin/llm-isolate-provider` and `bin/llm-restore-providers` are used by `modelman benchmark` to manage service isolation.

- [ ] **Step 3: Update cross-repo status tracker**

In `agent-worktree/docs/superpowers/plans/2026-08-27-model-management-consolidation-status.md`, mark Sub-project 2 as **implementation in progress / merged** once PR lands.

- [ ] **Step 4: Commit**

```bash
git add -A
git commit -m "docs(benchmark): add CLI usage and status tracker updates"
```

---

## Task 14: Open and merge PR

**Files:**
- All changed files in `modelman`
- Helpers in `local-ai-setup`
- Tracker update in `agent-worktree`

- [ ] **Step 1: Push modelman branch**

```bash
cd /Users/keith/github/ohanaverse/modelman
git push -u origin feat/modelman-benchmark
```

- [ ] **Step 2: Open modelman PR**

Title: `feat: add benchmark subcommand (sub-project 2)`

Body: summarize the design, link to PR #14 spec, and note the companion helpers in `local-ai-setup`.

- [ ] **Step 3: Push local-ai-setup helpers branch**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup
git checkout -b feat/benchmark-isolation-helpers
git push -u origin feat/benchmark-isolation-helpers
```

Open a separate PR in `local-ai-setup`.

- [ ] **Step 4: Update tracker**

Open a small PR in `agent-worktree` to mark Sub-project 2 implementation merged, pointing to the modelman and local-ai-setup PRs.

- [ ] **Step 5: Merge**

Merge `local-ai-setup` helpers PR first (no modelman dependency). Then merge `modelman` benchmark PR. Then merge tracker update.

Use admin bypass if review is blocked and user approves.

---

## Self-review

**Spec coverage:**
- Registry-aware target discovery: Task 8.
- Delegated isolation: Tasks 6 and 11.
- Pluggable workloads: Tasks 3-5.
- Disk artifacts (JSON + Markdown) + latest-run pointer: Tasks 7, 9, 10.
- Direct vs LiteLLM comparison: Tasks 8-9.

**Placeholder scan:**
- No TBDs or TODOs.
- Every step contains exact code, commands, and expected output.
- Code snippets for new files are complete.

**Type consistency:**
- `BenchmarkMetrics` fields match between `base.py` and consumers.
- `TargetResult` dataclass is used consistently in `runner.py` and `results.py`.
- `DEFAULT_RESULTS_DIR` is defined in `runner.py` and imported in `cli.py`.

**Potential issues found and fixed inline:**
- `runner.py` originally called `workload.metrics(None)` on isolation failure; replaced with explicit `BenchmarkMetrics(...)`.
- `cli.py` originally overwrote `state.extra["benchmarks"]"; replaced with `setdefault` + key update.
- `long.py` originally imported `WorkloadSpec` implicitly; added explicit import.
