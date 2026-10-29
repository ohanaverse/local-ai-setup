#!/usr/bin/env python3
"""Controlled `claude -p` probes for comparing Claude Code across environments.

Runs a fixed list of cases through `claude -p` from one fixed empty directory, reads each run's
session transcript back with transcript_speed's call timing, and writes an aggregate-only JSON
that can be copied between machines and compared with `--compare`. Design:
docs/superpowers/specs/2026-10-29-claude-code-environment-probes-design.md

Usage: probe_runner.py run --label NAME [--runs N] [--out FILE]
       probe_runner.py --compare A.json B.json
"""

import json
import random
import subprocess
import sys
import time
from dataclasses import dataclass, replace
from pathlib import Path

import transcript_speed as ts

# --- cases and prompts -----------------------------------------------------------------------


@dataclass(frozen=True)
class Case:
    name: str
    body: str
    filler_tokens: int


CASES = {
    c.name: c
    for c in (
        Case("tiny", "Reply with the single word OK.", 0),
        Case("count", "Count from 1 to 300, one number per line. No other text.", 0),
        Case("fill20k", "Reply with the single word DONE.", 20_000),
        Case("fill80k", "Reply with the single word DONE.", 80_000),
    )
}

# Drops user-level plugins, MCP servers and skills. Never --bare: it ignores OAuth and keychain
# credentials, so a subscription login could not authenticate. Never --no-session-persistence: the
# transcript is the measurement.
VARIANT_FLAGS = {
    "default": [],
    "lean": ["--setting-sources", "project", "--strict-mcp-config", "--disable-slash-commands"],
}

CHARS_PER_TOKEN = 4  # rough; the real size is the ctx_tokens a run reports


def make_filler(tokens):
    """Deterministic synthetic text of about `tokens` tokens. Nothing in it is sensitive."""
    chars = tokens * CHARS_PER_TOKEN
    lines = []
    size = 0
    i = 0
    while size < chars:
        line = f"record {i:06d}: alpha beta gamma delta epsilon zeta\n"
        lines.append(line)
        size += len(line)
        i += 1
    return "".join(lines)[:chars]


def build_prompt(case, nonce):
    """Filler, then the case body, then the per-run nonce last.

    The nonce comes after everything fixed so the full request is unique (the gateway's response
    cache cannot answer it) while the prefix stays identical and can still be prompt-cached.
    """
    parts = [make_filler(case.filler_tokens), case.body] if case.filler_tokens else [case.body]
    return "\n\n".join(parts) + f"\n(ref {nonce})"


# --- one run ---------------------------------------------------------------------------------

DISCARD_REASONS = ("error", "bad-json", "timeout", "no-transcript", "no-call", "extra-calls")
TRANSCRIPT_POLL_S = 0.1


@dataclass
class Run:
    case: str
    variant: str
    index: int
    wall_s: float | None = None  # the whole `claude -p` process
    api_s: float | None = None  # duration_api_ms
    client_overhead_s: float | None = None  # duration_ms - duration_api_ms: per-turn, not startup
    startup_s: float | None = None  # wall_s - duration_ms: process start, hooks, plugin/MCP load
    ttft_s: float | None = None  # ttft_ms, as measured by Claude Code itself
    ttfb_s: float | None = None  # first block, from the transcript (see transcript_speed)
    total_s: float | None = None
    tps_total: float | None = None
    ctx_tokens: int | None = None
    cache_hit: float | None = None
    discard_reason: str | None = None


def _seconds(ms):
    return ms / 1000 if isinstance(ms, int | float) else None


def _find_transcript(projects_dir, session_id, wait_s):
    deadline = time.monotonic() + wait_s
    while True:
        found = next(Path(projects_dir).glob(f"*/{session_id}.jsonl"), None)
        if found or time.monotonic() >= deadline:
            return found
        time.sleep(TRANSCRIPT_POLL_S)


def run_once(
    case,
    variant,
    index,
    *,
    model,
    claude_bin,
    probe_dir,
    projects_dir,
    timeout_s=600.0,
    transcript_wait_s=10.0,
):
    """One `claude -p` run. Any problem with the run itself comes back as a Run with a
    discard_reason; a missing `claude` binary or probe directory raises, since no run can follow.
    """
    run = Run(case.name, variant, index)
    cmd = [claude_bin, "-p", "--output-format", "json", "--model", model, *VARIANT_FLAGS[variant]]
    prompt = build_prompt(case, f"{random.randrange(10**8):08d}")
    start = time.monotonic()
    try:
        proc = subprocess.run(
            cmd, input=prompt, capture_output=True, text=True, cwd=probe_dir, timeout=timeout_s
        )
    except subprocess.TimeoutExpired:
        return replace(run, discard_reason="timeout")
    wall_s = time.monotonic() - start
    try:
        out = json.loads(proc.stdout)
    except ValueError:
        out = None
    if not isinstance(out, dict):
        return replace(run, discard_reason="bad-json")
    if out.get("is_error") or proc.returncode != 0:
        return replace(run, discard_reason="error")

    duration, api = _seconds(out.get("duration_ms")), _seconds(out.get("duration_api_ms"))
    run = replace(
        run,
        wall_s=wall_s,
        api_s=api,
        client_overhead_s=duration - api if duration is not None and api is not None else None,
        startup_s=wall_s - duration if duration is not None else None,
        ttft_s=_seconds(out.get("ttft_ms")),
    )
    path = _find_transcript(projects_dir, out.get("session_id"), transcript_wait_s)
    if path is None:
        return replace(run, discard_reason="no-transcript")
    rows = [ts.derive(c) for c in ts.calls_from_transcript(path)]
    measured = [r for r in rows if ts.same_model(model, r["model"])]
    if not measured:
        return replace(run, discard_reason="no-call")
    if len(measured) > 1:
        return replace(run, discard_reason="extra-calls")
    (r,) = measured
    return replace(
        run,
        ttfb_s=r["ttfb_s"],
        total_s=r["total_s"],
        tps_total=r["tps_total"],
        ctx_tokens=r["ctx_tokens"],
        cache_hit=r["cache_hit"],
    )


# --- rounds ----------------------------------------------------------------------------------

CACHE_LIFETIME_S = 300.0  # prompt-cache lifetime we assume; a longer gap makes a "warm" run cold


def run_probe(cases, variants, runs, *, run_fn=run_once, clock=time.monotonic, **run_kwargs):
    """Run every case under every variant `runs` times, interleaved as round -> case -> variant.

    Returns all Runs and the largest start-to-start gap in seconds between two runs of the same
    (case, variant). A gap over CACHE_LIFETIME_S warns on stderr and the probe carries on; the
    aggregate step excludes warm runs that turned out cache-cold. `clock` is read once per run.
    """
    results = []
    last_start = {}
    max_gap = 0.0
    for index in range(runs):
        for case in cases:
            for variant in variants:
                now = clock()
                key = (case.name, variant)
                if key in last_start:
                    gap = now - last_start[key]
                    max_gap = max(max_gap, gap)
                    if gap > CACHE_LIFETIME_S:
                        print(
                            f"warning: {gap:.0f}s between runs of {case.name}/{variant}, over the "
                            f"{CACHE_LIFETIME_S:.0f}s prompt-cache lifetime; this run may be cold",
                            file=sys.stderr,
                        )
                last_start[key] = now
                results.append(run_fn(case, variant, index, **run_kwargs))
    return results, max_gap
