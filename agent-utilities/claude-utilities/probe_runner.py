#!/usr/bin/env python3
"""Controlled `claude -p` probes for comparing Claude Code across environments.

Runs a fixed list of cases through `claude -p` from one fixed empty directory, reads each run's
session transcript back with transcript_speed's call timing, and writes an aggregate-only JSON
that can be copied between machines and compared with `--compare`. Design:
docs/superpowers/specs/2026-10-29-claude-code-environment-probes-design.md

Usage: probe_runner.py run --label NAME [--runs N] [--out FILE]
       probe_runner.py --compare A.json B.json
"""

import argparse
import datetime
import json
import os
import random
import shutil
import statistics
import subprocess
import sys
import time
from collections import Counter
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

# Drops user-level plugins, MCP servers, skills and hooks. Managed settings still load, so the gateway
# route and credential helper survive (checked 2026-10-29 on a managed machine: same model and apiKeySource;
# plugins 18 -> 4, MCP servers 13 -> 0, slash commands 111 -> 0, tools 29 -> 20; see the README). Never
# --bare: it ignores OAuth and keychain credentials, so a subscription login could not authenticate. Never
# --no-session-persistence: the transcript is the measurement.
VARIANT_FLAGS = {
    "default": [],
    "lean": ["--setting-sources", "project", "--strict-mcp-config", "--disable-slash-commands"],
}

# Measured 2026-10-29: this numeric filler tokenizes at about 2.2 characters per token (80,000
# characters came to 36,080 tokens). The real size is always the ctx_tokens a run reports.
CHARS_PER_TOKEN = 2.2


def make_filler(tokens):
    """Deterministic synthetic text of about `tokens` tokens. Nothing in it is sensitive."""
    chars = round(tokens * CHARS_PER_TOKEN)
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


# --- aggregate -------------------------------------------------------------------------------

SCHEMA = 1
COLD_HIT_THRESHOLD = 0.5  # a warm run that read less than this from cache was not warm
FIELDS = (
    "wall_s",
    "api_s",
    "client_overhead_s",
    "startup_s",
    "ttft_s",
    "ttfb_s",
    "total_s",
    "tps_total",
    "ctx_tokens",
    "cache_hit",
)


def _rounded(field, value):
    """Token counts as whole numbers, everything else to 2 decimals; None stays None."""
    if value is None:
        return None
    return round(value) if field == "ctx_tokens" else round(value, 2)


def _percentiles(runs, field):
    """p50 (median) and p90 (nearest rank, as in transcript_speed) of a Run field, or None."""
    vals = sorted(v for r in runs if (v := getattr(r, field)) is not None)
    if not vals:
        return None
    return {
        "p50": _rounded(field, statistics.median(vals)),
        "p90": _rounded(field, ts.percentile(vals, 0.9)),
    }


def aggregate_runs(runs, meta):
    """Aggregate-only summary of a probe: carries no session ids, prompts or paths.

    Per (case, variant): run 0 is the cold run, runs 1.. are warm, and a warm run whose cache hit
    is under COLD_HIT_THRESHOLD is left out of the warm percentiles and counted. If the median
    warm hit is itself under the threshold, the prompt changes every session (a hook injecting
    the time, say) and nothing would be left, so every warm run is kept and the group is flagged
    `cache_partial`. `startup` is the context tokens a bare `tiny` call reads under each variant
    (the warm p50), i.e. how much the variant's configuration injects before any prompt.
    """
    groups = {}
    for r in runs:
        groups.setdefault((r.case, r.variant), []).append(r)
    cases = []
    for (case, variant), rs in groups.items():
        ok = [r for r in rs if r.discard_reason is None]
        cold = next((r for r in ok if r.index == 0), None)
        warm_all = [r for r in ok if r.index >= 1]
        hits = [r.cache_hit for r in warm_all if r.cache_hit is not None]
        partial = bool(hits) and statistics.median(hits) < COLD_HIT_THRESHOLD
        warm = warm_all
        if not partial:
            warm = [r for r in warm_all if r.cache_hit is None or r.cache_hit >= COLD_HIT_THRESHOLD]
        cases.append(
            {
                "case": case,
                "variant": variant,
                "runs_ok": len(ok),
                "discarded": dict(Counter(r.discard_reason for r in rs if r.discard_reason)),
                "cache_cold_warm": len(warm_all) - len(warm),
                "cache_partial": partial,
                "cold": {f: _rounded(f, getattr(cold, f)) for f in FIELDS} if cold else None,
                "warm": {f: _percentiles(warm, f) for f in FIELDS},
            }
        )
    startup = {}
    for variant in dict.fromkeys(r.variant for r in runs):
        tiny = next((c for c in cases if (c["case"], c["variant"]) == ("tiny", variant)), None)
        ctx = tiny["warm"]["ctx_tokens"] if tiny else None
        startup[variant] = ctx["p50"] if ctx else None
    return {"schema": SCHEMA, "meta": meta, "startup": startup, "cases": cases}


# --- compare ---------------------------------------------------------------------------------


def _load(path):
    data = json.loads(Path(path).read_text())
    schema = data.get("schema") if isinstance(data, dict) else None
    if schema != SCHEMA:
        raise ValueError(f"{path}: not a probe_runner file (schema {schema}, expected {SCHEMA})")
    return data


def _num(v):
    return "n/a" if v is None else str(v)


def _delta(x, y):
    if not x or y is None:
        return "n/a"
    return f"{(y - x) / x * 100:+.1f}%"


def _warm_p50(data, case, variant, field):
    for c in data["cases"]:
        if (c["case"], c["variant"]) == (case, variant):
            return (c["warm"].get(field) or {}).get("p50")
    return None


def _config_cost(data):
    """default minus lean warm `wall_s` p50 for `tiny`: what the configuration costs per run."""
    default, lean = (_warm_p50(data, "tiny", v, "wall_s") for v in ("default", "lean"))
    return None if default is None or lean is None else default - lean


def _size_slope(data, variant):
    """Growth of `ttfb_s` per 10k context tokens between `tiny` and `fill80k`."""
    t0, t1 = (_warm_p50(data, c, variant, "ttfb_s") for c in ("tiny", "fill80k"))
    c0, c1 = (_warm_p50(data, c, variant, "ctx_tokens") for c in ("tiny", "fill80k"))
    if None in (t0, t1, c0, c1) or c1 <= c0:
        return None
    return (t1 - t0) / ((c1 - c0) / 10_000)


def compare(path_a, path_b):
    a, b = _load(path_a), _load(path_b)
    meta_a, meta_b = a["meta"], b["meta"]
    if meta_a.get("model") != meta_b.get("model"):
        raise ValueError(
            f"pinned models differ (A={meta_a.get('model')}, B={meta_b.get('model')}); "
            "compare runs of one model"
        )
    lines = []
    for key, label in (("claude_version", "Claude Code versions"), ("runs", "run counts")):
        if meta_a.get(key) != meta_b.get(key):
            lines.append(f"WARNING: {label} differ (A={meta_a.get(key)}, B={meta_b.get(key)})")
    for label, meta in (("A", meta_a), ("B", meta_b)):
        if meta.get("interrupted"):
            lines.append(
                f"WARNING: {label} was interrupted; it holds only the runs finished by then"
            )

    cells_a = {(c["case"], c["variant"]): c for c in a["cases"]}
    cells_b = {(c["case"], c["variant"]): c for c in b["cases"]}

    def order(key):
        known = list(CASES)
        return (known.index(key[0]) if key[0] in known else len(known), key[0], key[1])

    for key in sorted(cells_a.keys() & cells_b.keys(), key=order):
        x, y = cells_a[key], cells_b[key]
        header = (
            f"\n{key[0]} / {key[1]}   ok runs A {x['runs_ok']} B {y['runs_ok']}   "
            f"cache-cold warm A {x['cache_cold_warm']} B {y['cache_cold_warm']}"
        )
        if x.get("cache_partial") or y.get("cache_partial"):
            yes_no = {True: "yes", False: "no"}
            header += (
                f"   partial cache A {yes_no[bool(x.get('cache_partial'))]} "
                f"B {yes_no[bool(y.get('cache_partial'))]}"
            )
        lines.append(header)
        lines.append(
            f"  {'metric':<19}{'p50 A':>9}{'p50 B':>9}{'diff':>8}"
            f"{'p90 A':>9}{'p90 B':>9}{'diff':>8}{'cold A':>9}{'cold B':>9}"
        )
        for f in FIELDS:
            wa, wb = x["warm"].get(f) or {}, y["warm"].get(f) or {}
            ca, cb = (x["cold"] or {}).get(f), (y["cold"] or {}).get(f)
            lines.append(
                f"  {f:<19}{_num(wa.get('p50')):>9}{_num(wb.get('p50')):>9}"
                f"{_delta(wa.get('p50'), wb.get('p50')):>8}"
                f"{_num(wa.get('p90')):>9}{_num(wb.get('p90')):>9}"
                f"{_delta(wa.get('p90'), wb.get('p90')):>8}{_num(ca):>9}{_num(cb):>9}"
            )
    for label, only in (
        ("A", cells_a.keys() - cells_b.keys()),
        ("B", cells_b.keys() - cells_a.keys()),
    ):
        if only:
            lines.append(
                f"\nonly in {label}: " + ", ".join(f"{c}/{v}" for c, v in sorted(only, key=order))
            )

    def secs(v, unit="s"):
        return "n/a" if v is None else f"{v:.2f}{unit}"

    lines.append("")
    lines.append(
        "config cost (tiny wall_s, default - lean, estimate): "
        f"A {secs(_config_cost(a))} B {secs(_config_cost(b))}"
    )
    variants = sorted({v for _, v in cells_a.keys() | cells_b.keys()})
    for variant in variants:
        lines.append(
            f"prompt-size slope, {variant} (ttfb_s per 10k context tokens, tiny to fill80k, "
            f"estimate): A {secs(_size_slope(a, variant), 's/10k')} "
            f"B {secs(_size_slope(b, variant), 's/10k')}"
        )
    return "\n".join(lines)


# --- command line ----------------------------------------------------------------------------

DEFAULT_MODEL = "claude-sonnet-5-5"
# The directory name carries transcript_speed.PROBE_DIR_MARKER, so organic stats skip these sessions.
DEFAULT_PROBE_DIR = "~/.cache/claude-utilities/probe-cwd"
DEFAULT_PROJECTS_DIR = "~/.claude/projects"


def _claude_version(claude_bin):
    try:
        words = subprocess.run(
            [claude_bin, "--version"], capture_output=True, text=True, timeout=30
        ).stdout.split()
    except (OSError, subprocess.TimeoutExpired):
        return "unknown"
    return words[0] if words else "unknown"


def _logged_run(case, variant, index, **kw):
    r = run_once(case, variant, index, **kw)
    outcome = f"discarded ({r.discard_reason})" if r.discard_reason else f"ok, {r.total_s:.1f}s"
    print(f"{r.case}/{r.variant} run {r.index}: {outcome}", file=sys.stderr)
    return r


def main(argv=None):
    ts.require_python()
    ap = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter
    )
    ap.add_argument(
        "--compare",
        nargs=2,
        metavar=("A.json", "B.json"),
        help="compare two probe files instead of running",
    )
    run = ap.add_subparsers(dest="command").add_parser("run", help="run the probe")
    run.add_argument("--label", required=True, help="names the output file and its meta")
    run.add_argument("--runs", type=int, default=10, help="runs per case and variant (default 10)")
    run.add_argument(
        "--model", default=DEFAULT_MODEL, help=f"pinned model (default {DEFAULT_MODEL})"
    )
    run.add_argument("--variants", default=",".join(VARIANT_FLAGS), help="default: all")
    run.add_argument("--cases", default=",".join(CASES), help="default: all")
    run.add_argument("--out", help="output file (default probe-<label>.json)")
    run.add_argument("--claude-bin", default="claude")
    run.add_argument("--probe-dir", default=DEFAULT_PROBE_DIR)
    run.add_argument("--projects-dir", default=DEFAULT_PROJECTS_DIR)
    args = ap.parse_args(argv)

    if args.compare:
        if args.command:
            ap.error("--compare takes only the two files")
        try:
            print(compare(*args.compare))
        except (OSError, ValueError, KeyError) as e:
            ap.error(str(e))
        return 0
    if args.command != "run":
        ap.error("give the run command, or --compare A.json B.json")

    names = [n for n in args.cases.split(",") if n]
    variants = [v for v in args.variants.split(",") if v]
    for name in names:
        if name not in CASES:
            ap.error(f"unknown case {name!r} (choose from {', '.join(CASES)})")
    for variant in variants:
        if variant not in VARIANT_FLAGS:
            ap.error(f"unknown variant {variant!r} (choose from {', '.join(VARIANT_FLAGS)})")
    if args.runs < 1:
        ap.error("--runs must be at least 1")
    if shutil.which(args.claude_bin) is None:
        sys.exit(
            f"probe_runner: cannot find '{args.claude_bin}'; install Claude Code or pass --claude-bin"
        )

    out = Path(args.out) if args.out else Path(f"probe-{args.label}.json")
    # A paid run must not be lost to a typo found only at the end, so check the output first.
    if not out.parent.is_dir() or not os.access(out.parent, os.W_OK):
        ap.error(f"cannot write {out}: directory {out.parent} is missing or not writable")
    probe_dir = Path(args.probe_dir).expanduser()
    probe_dir.mkdir(parents=True, exist_ok=True)

    version = _claude_version(args.claude_bin)  # before the loop: an auto-update must not change it
    done = []

    def record(case, variant, index, **kw):
        done.append(_logged_run(case, variant, index, **kw))
        return done[-1]

    def write(runs, max_gap, interrupted):
        meta = {
            "label": args.label,
            "claude_version": version,
            "model": args.model,
            "runs": args.runs,
            "variants": variants,
            "date": datetime.date.today().isoformat(),
            "max_gap_s": None if max_gap is None else round(max_gap, 1),
        }
        if interrupted:
            meta["interrupted"] = True
        out.write_text(json.dumps(aggregate_runs(runs, meta), indent=2) + "\n")

    try:
        runs, max_gap = run_probe(
            [CASES[n] for n in names],
            variants,
            args.runs,
            run_fn=record,
            model=args.model,
            claude_bin=args.claude_bin,
            probe_dir=probe_dir,
            projects_dir=Path(args.projects_dir).expanduser(),
        )
    except KeyboardInterrupt:
        write(done, None, True)
        print(f"interrupted: wrote the {len(done)} finished runs to {out}", file=sys.stderr)
        return 130
    except Exception:
        write(done, None, True)
        print(f"failed: wrote the {len(done)} finished runs to {out}", file=sys.stderr)
        raise
    write(runs, max_gap, False)
    discarded = sum(1 for r in runs if r.discard_reason)
    print(f"wrote {out}: {len(runs)} runs, {discarded} discarded")
    return 0


if __name__ == "__main__":
    sys.exit(main())
