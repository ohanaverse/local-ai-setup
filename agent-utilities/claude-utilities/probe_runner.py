#!/usr/bin/env python3
"""Controlled `claude -p` probes for comparing Claude Code across environments.

Runs a fixed list of cases through `claude -p` from one fixed empty directory, reads each run's
session transcript back with transcript_speed's call timing, and writes an aggregate-only JSON
that can be copied between machines and compared with `--compare`. Design:
docs/superpowers/specs/2026-10-29-claude-code-environment-probes-design.md

Usage: probe_runner.py run --label NAME [--runs N] [--out FILE]
       probe_runner.py --compare A.json B.json
"""

from dataclasses import dataclass

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
