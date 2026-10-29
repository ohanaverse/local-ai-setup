#!/usr/bin/env python3
"""Estimate Claude Code tokens/second from local session transcripts (read-only).

Per API call (one api-request record paired with the assistant message that follows):
  request_ts  = api-request timestamp (client sent the request)
  first_ts    = first content block timestamp for that message
  last_ts     = last content block timestamp
  out_tokens  = final usage.output_tokens (includes thinking tokens)

  ttfb_s      = first_ts - request_ts          (queueing + prefill + first block; NOT a clean TTFT)
  total_s     = last_ts  - request_ts
  tps_total   = out_tokens / total_s           (lower bound: includes ttfb)
  tps_window  = out_tokens / (last_ts - first_ts)  (upper-ish bound: first block's tokens
                                                   were produced before first_ts; only when
                                                   the message has >1 block and window >= MIN_WINDOW_S)

  ctx_tokens  = usage.input_tokens + cache_read_input_tokens + cache_creation_input_tokens
                (what the model read for this call; None when the transcript has no input_tokens)
  cache_hit   = cache_read_input_tokens / ctx_tokens (None unless both cache fields are present;
                0.0 on a route that returns them as zero, e.g. a gateway that drops prompt caching)

Timestamps are client-side and written per content block, so treat results as estimates.

Usage: transcript_speed.py [--min-tokens N] [--csv | --json] PATH_OR_DIR...
       transcript_speed.py --compare A.json B.json
  PATH_OR_DIR: a .jsonl transcript, or a directory searched recursively (e.g. ~/.claude/projects)
  --json:      aggregate-only stats per (model, output-size band) and per (model, context-size
               band), session startup size and requested-vs-answered model counts; carries no
               session names or paths, so the file can be copied between machines
  --compare:   side-by-side of two --json files, with the percent difference B vs A
"""

import argparse
import csv
import json
import math
import re
import statistics
import sys
from collections import Counter, defaultdict
from datetime import datetime
from pathlib import Path

# datetime.fromisoformat only parses the transcripts' trailing "Z" from 3.11, and parse_ts's
# error handler skips every record it cannot parse, so an old interpreter reports "0 calls"
# instead of failing. pyproject.toml pins 3.13; uv run picks it up. Checked in main(), not at
# import, so otel_speed.py (which only imports percentile) is not blocked by it.
REQUIRED_PYTHON = (3, 11)


def require_python(version=None):
    version = version or sys.version_info
    if tuple(version[:2]) < REQUIRED_PYTHON:
        have = ".".join(str(v) for v in version[:3])
        want = ".".join(str(v) for v in REQUIRED_PYTHON)
        sys.exit(
            f"transcript_speed.py needs Python >= {want}, found {have}. "
            "From agent-utilities/claude-utilities, run: uv run python transcript_speed.py ..."
        )


MIN_WINDOW_S = 1.0

# tps_total includes prefill and network, so short outputs always look slower; comparing within
# a band keeps two environments with different output-length mixes from skewing the result.
BANDS = ((200, "<200"), (1000, "200-1000"), (3000, "1000-3000"))
TOP_BAND = "3000+"

# Context size bands answer "is it slower at the same prompt size?", which separates a bigger
# prompt (prefill) from a slower route (decode).
CTX_BANDS = ((10_000, "<10k"), (30_000, "10k-30k"), (100_000, "30k-100k"))
CTX_TOP_BAND = "100k+"

SCHEMA = 2


USAGE_FIELDS = (
    ("input", "input_tokens"),
    ("cache_read", "cache_read_input_tokens"),
    ("cache_create", "cache_creation_input_tokens"),
)


def parse_ts(s):
    return datetime.fromisoformat(s).timestamp()


def calls_from_transcript(path):
    pending_req = None
    cur = None  # current assistant message accumulator
    seen_main = set()  # models that have already answered a non-subagent call in this file

    def flush():
        nonlocal cur
        if cur:
            # Counted per model, and whether or not the call is paired: a small side call (a
            # title or topic check) must not stand in for the main model's startup, and a
            # resumed file whose first call lost its api-request must not promote a later one.
            first = not cur["sidechain"] and cur["model"] not in seen_main
            if not cur["sidechain"]:
                seen_main.add(cur["model"])
            if cur["req_ts"] is not None:
                cur["first_main"] = first
                yield cur
        cur = None

    with open(path, encoding="utf-8", errors="replace") as fh:
        for line in fh:
            try:
                d = json.loads(line)
            except ValueError:
                continue
            # A line can be valid JSON but not a record (null, a list), and Claude Code's
            # format is not a stable API, so skip anything we cannot read rather than crash.
            if not isinstance(d, dict):
                continue
            t = d.get("type")
            if t not in ("api-request", "assistant") or not d.get("timestamp"):
                continue
            try:
                ts = parse_ts(d["timestamp"])
            except (ValueError, TypeError):
                continue
            if t == "api-request":
                pending_req = ts
            else:
                m = d.get("message") or {}
                mid = m.get("id")
                if cur is None or cur["id"] != mid:
                    yield from flush()
                    cur = {
                        "id": mid,
                        "model": m.get("model", "?"),
                        "req_ts": pending_req,
                        "first_ts": ts,
                        "last_ts": ts,
                        "blocks": 0,
                        "out": 0,
                        "input": None,
                        "cache_read": None,
                        "cache_create": None,
                        "requested": d["requestedModel"]
                        if isinstance(d.get("requestedModel"), str)
                        else None,
                        "sidechain": bool(d.get("isSidechain")),
                        "session": path.name,
                    }
                    pending_req = None
                cur["last_ts"] = ts
                cur["blocks"] += 1
                usage = m.get("usage") or {}
                cur["out"] = usage.get("output_tokens") or cur["out"]
                for key, field in USAGE_FIELDS:
                    if isinstance(usage.get(field), int):
                        cur[key] = usage[field]
    yield from flush()


def percentile(sorted_vals, p):
    """Nearest-rank percentile of an ascending, non-empty list."""
    return sorted_vals[max(0, math.ceil(p * len(sorted_vals)) - 1)]


def derive(c):
    total = c["last_ts"] - c["req_ts"]
    window = c["last_ts"] - c["first_ts"]
    ctx = None
    if c["input"] is not None:
        ctx = c["input"] + (c["cache_read"] or 0) + (c["cache_create"] or 0)
    has_cache = c["cache_read"] is not None and c["cache_create"] is not None
    return {
        "session": c["session"],
        "model": c["model"],
        "out_tokens": c["out"],
        "ttfb_s": round(c["first_ts"] - c["req_ts"], 2),
        "total_s": round(total, 2),
        "tps_total": round(c["out"] / total, 1) if total > 0 else None,
        "tps_window": round(c["out"] / window, 1)
        if c["blocks"] > 1 and window >= MIN_WINDOW_S
        else None,
        "ctx_tokens": ctx,
        "ctx_band": ctx_band_of(ctx) if ctx is not None else None,
        "cache_hit": round(c["cache_read"] / ctx, 3) if has_cache and ctx else None,
        "first_main": c["first_main"],
        "requested_model": c["requested"],
    }


def _band(value, bands, top):
    for upper, name in bands:
        if value < upper:
            return name
    return top


def band_of(out_tokens):
    return _band(out_tokens, BANDS, TOP_BAND)


def ctx_band_of(ctx_tokens):
    return _band(ctx_tokens, CTX_BANDS, CTX_TOP_BAND)


def base_model(name):
    """Requested names can carry a context-window suffix (`claude-sonnet-5-5[1m]`) the answer lacks."""
    return re.sub(r"\[[^\]]*\]$", "", name)


def _canon(name):
    """Lower-case, no `[1m]` suffix, no `provider/` prefix, no trailing `-YYYYMMDD` date."""
    name = base_model(name).lower().rsplit("/", 1)[-1]
    return re.sub(r"-\d{8}$", "", name)


def same_model(requested, answered):
    """True when the answer is the requested model under another spelling.

    Slots routed through a gateway request an alias (`sonnet`) or a prefixed name and get a
    canonical id back, so one name's dash-separated parts being within the other's count as the same model.
    """
    a, b = set(_canon(requested).split("-")), set(_canon(answered).split("-"))
    return a <= b or b <= a


def _pct(rs, key, p, whole=False):
    """p50 is the true median and p90 nearest-rank, matching the summary view's numbers.

    `whole` rounds to an integer, for token counts."""
    vals = sorted(r[key] for r in rs if r.get(key) is not None)
    if not vals:
        return None
    v = statistics.median(vals) if p == 0.5 else percentile(vals, p)
    return round(v) if whole else round(v, 2)


def _group(rows, key):
    """Rows by (model, key(row)); rows whose key is None are left out. Sorted by that pair."""
    groups = defaultdict(list)
    for r in rows:
        k = key(r)
        if k is not None:
            groups[(r["model"], k)].append(r)
    return sorted(groups.items())


def _stats(rs, fields):
    """`{name: percentile}` for (name, source key, p, whole) specs over the group's rows."""
    return {name: _pct(rs, src, p, whole) for name, src, p, whole in fields}


def fallbacks(rows):
    """Calls answered by a different model than requested, allowing for aliases and id spellings.

    Only calls that carry a requestedModel count as checked; older transcripts have none, and
    `<synthetic>` messages (Claude Code's own error turns) were never answered by a model.
    """
    checked = [
        r for r in rows if r.get("requested_model") is not None and r["model"] != "<synthetic>"
    ]
    pairs = Counter(
        (base_model(r["requested_model"]), r["model"])
        for r in checked
        if not same_model(r["requested_model"], r["model"])
    )
    return {
        "checked": len(checked),
        "mismatched": sum(pairs.values()),
        "pairs": [
            {"requested": req, "answered": ans, "n": n}
            for (req, ans), n in sorted(pairs.items(), key=lambda kv: (-kv[1], kv[0]))
        ],
    }


def aggregate(rows, min_tokens, all_rows=None):
    """Aggregate-only stats. Deliberately drops session names.

    `rows` are the calls at or above min_tokens. `all_rows` is every paired call: the first call
    of a session is often a short tool-use turn, so startup size and fallbacks must not be
    filtered by output length.
    """
    all_rows = rows if all_rows is None else all_rows

    band_fields = (
        ("tps_total_p50", "tps_total", 0.5, False),
        ("tps_total_p90", "tps_total", 0.9, False),
        ("ttfb_s_p50", "ttfb_s", 0.5, False),
        ("ttfb_s_p90", "ttfb_s", 0.9, False),
        ("total_s_p50", "total_s", 0.5, False),
        ("total_s_p90", "total_s", 0.9, False),
        ("ctx_tokens_p50", "ctx_tokens", 0.5, True),
        ("ctx_tokens_p90", "ctx_tokens", 0.9, True),
        ("cache_hit_p50", "cache_hit", 0.5, False),
    )
    ctx_band_fields = (
        ("tps_total_p50", "tps_total", 0.5, False),
        ("tps_total_p90", "tps_total", 0.9, False),
        ("ttfb_s_p50", "ttfb_s", 0.5, False),
        ("total_s_p50", "total_s", 0.5, False),
    )
    startup_fields = (
        ("ctx_tokens_p50", "ctx_tokens", 0.5, True),
        ("ctx_tokens_p90", "ctx_tokens", 0.9, True),
    )
    bands = [
        {"model": model, "band": band, "n": len(rs)} | _stats(rs, band_fields)
        for (model, band), rs in _group(rows, lambda r: band_of(r["out_tokens"]))
    ]
    ctx_bands = [
        {"model": model, "band": band, "n": len(rs)} | _stats(rs, ctx_band_fields)
        for (model, band), rs in _group(rows, lambda r: r.get("ctx_band"))
    ]
    first = [r for r in all_rows if r.get("first_main") and r.get("ctx_tokens")]
    startup = [
        {"model": model, "n": len(rs)} | _stats(rs, startup_fields)
        for (model, _), rs in _group(first, lambda r: True)
    ]
    return {
        "schema": SCHEMA,
        "band_unit": "output_tokens",
        "min_tokens": min_tokens,
        "bands": bands,
        "ctx_bands": ctx_bands,
        "startup": startup,
        "fallbacks": fallbacks(all_rows),
    }


BAND_ORDER = {name: i for i, name in enumerate([n for _, n in BANDS] + [TOP_BAND])}
CTX_BAND_ORDER = {name: i for i, name in enumerate([n for _, n in CTX_BANDS] + [CTX_TOP_BAND])}
BAND_FIELDS = ("model", "band", "n", "tps_total_p50", "tps_total_p90")


def load_aggregate(path):
    with open(path, encoding="utf-8") as fh:
        data = json.load(fh)
    if not isinstance(data, dict) or not isinstance(data.get("bands"), list):
        raise ValueError(f"{path}: not a transcript_speed --json file")
    for b in data["bands"]:
        if not isinstance(b, dict) or any(f not in b for f in BAND_FIELDS):
            raise ValueError(f"{path}: not a transcript_speed --json file (bad band entry)")
    return data


def _index(data, section, *keys):
    """Section entries keyed by `keys`; a missing or malformed section is empty (older files)."""
    entries = data.get(section)
    if not isinstance(entries, list):
        return {}
    return {tuple(e.get(k) for k in keys): e for e in entries if isinstance(e, dict)}


def _delta(x, y):
    if not x or y is None:
        return "n/a"
    return f"{(y - x) / x * 100:+.1f}%"


def _num(v):
    # A band whose calls all lacked a usable duration has a null percentile.
    return "n/a" if v is None else str(v)


def _has(index, *fields):
    return any(e.get(f) is not None for e in index.values() for f in fields)


def compare(path_a, path_b):
    data_a, data_b = load_aggregate(path_a), load_aggregate(path_b)
    min_a, min_b = data_a.get("min_tokens"), data_b.get("min_tokens")
    a, b = _index(data_a, "bands", "model", "band"), _index(data_b, "bands", "model", "band")
    ctx_a = _index(data_a, "ctx_bands", "model", "band")
    ctx_b = _index(data_b, "ctx_bands", "model", "band")
    start_a, start_b = _index(data_a, "startup", "model"), _index(data_b, "startup", "model")

    def order(key):
        return (key[0], BAND_ORDER.get(key[1], len(BAND_ORDER)), key[1])

    def ctx_order(key):
        return (key[0], CTX_BAND_ORDER.get(key[1], len(CTX_BAND_ORDER)), key[1])

    both = sorted(a.keys() & b.keys(), key=order)
    ctx_both = sorted(ctx_a.keys() & ctx_b.keys(), key=ctx_order)
    start_both = sorted(start_a.keys() & start_b.keys())
    models = {k[0] for k in [*both, *ctx_both, *start_both]}
    model_w = max([len("model"), *(len(m) for m in models)]) + 2
    lines = []
    if min_a is None or min_b is None:
        lines.append(
            "note: a file has no min_tokens (older --json output); "
            "the bands are comparable only if both used the same --min-tokens\n"
        )
    elif min_a != min_b:
        lines.append(
            f"WARNING: --min-tokens differs (A={min_a}, B={min_b}); "
            "the smallest band covers different token ranges and is not comparable\n"
        )
    for label, data in (("A", data_a), ("B", data_b)):
        schema = data.get("schema")
        if not isinstance(schema, int) or schema < SCHEMA:
            lines.append(
                f"note: {label} predates the context columns (older --json output); its context, "
                "startup and fallback figures show n/a and its output bands may not match. "
                "Regenerate it to compare them.\n"
            )
        elif schema > SCHEMA:
            lines.append(
                f"note: {label} is schema {schema}, newer than this script's {SCHEMA}; "
                "update this script, as fields it does not know are ignored.\n"
            )
    lines.append(
        f"{'model':<{model_w}}{'band (out tok)':<16}{'n A':>6}{'n B':>6}"
        f"{'p50 A':>8}{'p50 B':>8}{'diff':>8}{'p90 A':>8}{'p90 B':>8}{'diff':>8}"
    )
    for key in both:
        x, y = a[key], b[key]
        lines.append(
            f"{key[0]:<{model_w}}{key[1]:<16}{x.get('n', 0):>6}{y.get('n', 0):>6}"
            f"{_num(x.get('tps_total_p50')):>8}{_num(y.get('tps_total_p50')):>8}"
            f"{_delta(x.get('tps_total_p50'), y.get('tps_total_p50')):>8}"
            f"{_num(x.get('tps_total_p90')):>8}{_num(y.get('tps_total_p90')):>8}"
            f"{_delta(x.get('tps_total_p90'), y.get('tps_total_p90')):>8}"
        )
    for label, only in (("A", a.keys() - b.keys()), ("B", b.keys() - a.keys())):
        if only:
            lines.append(
                f"\nonly in {label}: "
                + ", ".join(f"{m} [{bd}]" for m, bd in sorted(only, key=order))
            )

    if _has(a, "ctx_tokens_p50", "cache_hit_p50") or _has(b, "ctx_tokens_p50", "cache_hit_p50"):
        lines.append(
            "\ncontext tokens read per call (input + cache read + cache creation), "
            "and the share served from cache"
        )
        lines.append(
            f"{'model':<{model_w}}{'band (out tok)':<16}"
            f"{'ctx A':>9}{'ctx B':>9}{'diff':>8}{'p90 A':>9}{'p90 B':>9}{'hit A':>8}{'hit B':>8}"
        )
        for key in both:
            x, y = a[key], b[key]
            lines.append(
                f"{key[0]:<{model_w}}{key[1]:<16}"
                f"{_num(x.get('ctx_tokens_p50')):>9}{_num(y.get('ctx_tokens_p50')):>9}"
                f"{_delta(x.get('ctx_tokens_p50'), y.get('ctx_tokens_p50')):>8}"
                f"{_num(x.get('ctx_tokens_p90')):>9}{_num(y.get('ctx_tokens_p90')):>9}"
                f"{_num(x.get('cache_hit_p50')):>8}{_num(y.get('cache_hit_p50')):>8}"
            )

    if (ctx_a or ctx_b) and not ctx_both:
        lines.append("\nspeed by context size: n/a (no model and context band in both files)")
    if ctx_both:
        lines.append(
            "\nspeed by context size (tokens/s): slower at equal context points at the route"
        )
        lines.append(
            f"{'model':<{model_w}}{'context':<12}{'n A':>6}{'n B':>6}"
            f"{'p50 A':>8}{'p50 B':>8}{'diff':>8}"
        )
        for key in ctx_both:
            x, y = ctx_a[key], ctx_b[key]
            lines.append(
                f"{key[0]:<{model_w}}{key[1]:<12}{x.get('n', 0):>6}{y.get('n', 0):>6}"
                f"{_num(x.get('tps_total_p50')):>8}{_num(y.get('tps_total_p50')):>8}"
                f"{_delta(x.get('tps_total_p50'), y.get('tps_total_p50')):>8}"
            )

    if (start_a or start_b) and not start_both:
        lines.append("\nsession startup: n/a (no model in both files)")
    if start_both:
        lines.append("\nsession startup (context tokens of each session's first main-thread call)")
        lines.append(
            f"{'model':<{model_w}}{'n A':>6}{'n B':>6}"
            f"{'p50 A':>9}{'p50 B':>9}{'diff':>8}{'p90 A':>9}{'p90 B':>9}{'diff':>8}"
        )
        for key in start_both:
            x, y = start_a[key], start_b[key]
            lines.append(
                f"{key[0]:<{model_w}}{x.get('n', 0):>6}{y.get('n', 0):>6}"
                f"{_num(x.get('ctx_tokens_p50')):>9}{_num(y.get('ctx_tokens_p50')):>9}"
                f"{_delta(x.get('ctx_tokens_p50'), y.get('ctx_tokens_p50')):>8}"
                f"{_num(x.get('ctx_tokens_p90')):>9}{_num(y.get('ctx_tokens_p90')):>9}"
                f"{_delta(x.get('ctx_tokens_p90'), y.get('ctx_tokens_p90')):>8}"
            )

    fb = {"A": data_a.get("fallbacks"), "B": data_b.get("fallbacks")}
    if any(isinstance(v, dict) for v in fb.values()):
        lines.append("\nrequested != answered (calls that recorded a requested model)")
        for label, v in fb.items():
            if not isinstance(v, dict):
                lines.append(f"  {label}: n/a")
                continue
            lines.append(f"  {label}: {v.get('mismatched', 0)} of {v.get('checked', 0)}")
            for p in v.get("pairs") or []:
                if isinstance(p, dict):
                    lines.append(
                        f"      {p.get('requested', '?')} -> {p.get('answered', '?')} "
                        f"({p.get('n', '?')})"
                    )
    return "\n".join(lines)


def main():
    require_python()
    ap = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter
    )
    ap.add_argument("paths", nargs="*")
    ap.add_argument(
        "--json", action="store_true", help="print aggregate-only stats per model and size band"
    )
    ap.add_argument(
        "--compare",
        nargs=2,
        metavar=("A.json", "B.json"),
        help="compare two --json files instead of reading transcripts",
    )
    ap.add_argument(
        "--min-tokens",
        type=int,
        default=50,
        help="skip calls with fewer output tokens (default 50; tiny calls are latency-dominated)",
    )
    ap.add_argument(
        "--csv", action="store_true", help="print per-call rows as CSV instead of the summary"
    )
    args = ap.parse_args()

    if args.json and args.csv:
        ap.error("--json and --csv are mutually exclusive")
    if args.compare:
        if args.paths or args.json or args.csv:
            ap.error("--compare takes only the two --json files, no other paths or output flags")
        try:
            print(compare(*args.compare))
        except (OSError, ValueError, KeyError) as e:
            ap.error(str(e))
        return
    if not args.paths:
        ap.error("give at least one transcript path, or use --compare A.json B.json")

    files = []
    for p in map(Path, args.paths):
        if not p.exists():
            ap.error(f"no such file or directory: {p}")
        files += sorted(p.rglob("*.jsonl")) if p.is_dir() else [p]

    all_rows = [derive(c) for f in files for c in calls_from_transcript(f)]
    rows = [r for r in all_rows if r["out_tokens"] >= args.min_tokens]

    if args.json:
        print(json.dumps(aggregate(rows, args.min_tokens, all_rows=all_rows), indent=2))
        return

    if args.csv:
        if rows:
            w = csv.DictWriter(sys.stdout, fieldnames=list(rows[0]))
            w.writeheader()
            w.writerows(rows)
        return

    by_model = defaultdict(list)
    for r in rows:
        by_model[r["model"]].append(r)
    print(
        f"{len(files)} transcript(s), {len(rows)} calls with >= {args.min_tokens} output tokens\n"
    )

    def stat(vals):
        vals = sorted(v for v in vals if v is not None)
        if not vals:
            return "n/a"

        return (
            f"p50 {statistics.median(vals):7.1f}  p90 {percentile(vals, 0.9):7.1f}  (n={len(vals)})"
        )

    for model, rs in sorted(by_model.items()):
        print(model)
        print(f"  tps_total   {stat([r['tps_total'] for r in rs])}")
        print(f"  tps_window  {stat([r['tps_window'] for r in rs])}")
        print(f"  ttfb_s      {stat([r['ttfb_s'] for r in rs])}")
        print(f"  total_s     {stat([r['total_s'] for r in rs])}")
        print(f"  ctx_tokens  {stat([r['ctx_tokens'] for r in rs])}")
        print()


if __name__ == "__main__":
    main()
