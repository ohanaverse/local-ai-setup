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

Timestamps are client-side and written per content block, so treat results as estimates.

Usage: transcript_speed.py [--min-tokens N] [--csv | --json] PATH_OR_DIR...
       transcript_speed.py --compare A.json B.json
  PATH_OR_DIR: a .jsonl transcript, or a directory searched recursively (e.g. ~/.claude/projects)
  --json:      aggregate-only stats per (model, output-size band); carries no session names or
               paths, so the file can be copied between machines
  --compare:   side-by-side of two --json files, with the percent difference B vs A
"""

import sys

# datetime.fromisoformat only parses the transcripts' trailing "Z" from 3.11, and parse_ts's
# error handler skips every record it cannot parse, so an old interpreter reports "0 calls"
# instead of failing. pyproject.toml pins 3.13; uv run picks it up.
REQUIRED_PYTHON = (3, 11)


def require_python(version=None):
    version = version or sys.version_info
    if tuple(version[:2]) < REQUIRED_PYTHON:
        have = ".".join(str(v) for v in version[:3])
        want = ".".join(str(v) for v in REQUIRED_PYTHON)
        sys.exit(
            f"transcript_speed.py needs Python >= {want}, found {have}. "
            "Run it with uv: uv run --directory claude-utilities python transcript_speed.py ..."
        )


require_python()

import argparse  # noqa: E402
import csv  # noqa: E402
import json  # noqa: E402
import math  # noqa: E402
import statistics  # noqa: E402
from collections import defaultdict  # noqa: E402
from datetime import datetime  # noqa: E402
from pathlib import Path  # noqa: E402

MIN_WINDOW_S = 1.0

# tps_total includes prefill and network, so short outputs always look slower; comparing within
# a band keeps two environments with different output-length mixes from skewing the result.
BANDS = ((200, "<200"), (1000, "200-1000"))
TOP_BAND = "1000+"


def parse_ts(s):
    return datetime.fromisoformat(s).timestamp()


def calls_from_transcript(path):
    pending_req = None
    cur = None  # current assistant message accumulator

    def flush():
        nonlocal cur
        if cur and cur["req_ts"] is not None:
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
                        "session": path.name,
                    }
                    pending_req = None
                cur["last_ts"] = ts
                cur["blocks"] += 1
                cur["out"] = (m.get("usage") or {}).get("output_tokens") or cur["out"]
    yield from flush()


def percentile(sorted_vals, p):
    """Nearest-rank percentile of an ascending, non-empty list."""
    return sorted_vals[max(0, math.ceil(p * len(sorted_vals)) - 1)]


def derive(c):
    total = c["last_ts"] - c["req_ts"]
    window = c["last_ts"] - c["first_ts"]
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
    }


def band_of(out_tokens):
    for upper, name in BANDS:
        if out_tokens < upper:
            return name
    return TOP_BAND


def aggregate(rows, min_tokens):
    """Aggregate-only stats per (model, band). Deliberately drops session names."""
    groups = defaultdict(list)
    for r in rows:
        groups[(r["model"], band_of(r["out_tokens"]))].append(r)

    # p50 is the true median and p90 nearest-rank, matching the summary view's numbers.
    def pct(rs, key, p):
        vals = sorted(r[key] for r in rs if r[key] is not None)
        if not vals:
            return None
        return round(statistics.median(vals) if p == 0.5 else percentile(vals, p), 2)

    bands = [
        {
            "model": model,
            "band": band,
            "n": len(rs),
            "tps_total_p50": pct(rs, "tps_total", 0.5),
            "tps_total_p90": pct(rs, "tps_total", 0.9),
            "ttfb_s_p50": pct(rs, "ttfb_s", 0.5),
            "total_s_p50": pct(rs, "total_s", 0.5),
        }
        for (model, band), rs in sorted(groups.items())
    ]
    return {"band_unit": "output_tokens", "min_tokens": min_tokens, "bands": bands}


BAND_ORDER = {name: i for i, name in enumerate([n for _, n in BANDS] + [TOP_BAND])}
BAND_FIELDS = ("model", "band", "n", "tps_total_p50", "tps_total_p90")


def load_aggregate(path):
    with open(path, encoding="utf-8") as fh:
        data = json.load(fh)
    if not isinstance(data, dict) or not isinstance(data.get("bands"), list):
        raise ValueError(f"{path}: not a transcript_speed --json file")
    for b in data["bands"]:
        if not isinstance(b, dict) or any(f not in b for f in BAND_FIELDS):
            raise ValueError(f"{path}: not a transcript_speed --json file (bad band entry)")
    return data.get("min_tokens"), {(b["model"], b["band"]): b for b in data["bands"]}


def compare(path_a, path_b):
    (min_a, a), (min_b, b) = load_aggregate(path_a), load_aggregate(path_b)

    def delta(x, y):
        if not x or y is None:
            return "n/a"
        return f"{(y - x) / x * 100:+.1f}%"

    def num(v):
        # A band whose calls all lacked a usable duration has a null percentile.
        return "n/a" if v is None else str(v)

    def order(key):
        return (key[0], BAND_ORDER.get(key[1], len(BAND_ORDER)), key[1])

    both = sorted(a.keys() & b.keys(), key=order)
    model_w = max([len("model"), *(len(m) for m, _ in both)]) + 2
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
    lines.append(
        f"{'model':<{model_w}}{'band (out tok)':<16}{'n A':>6}{'n B':>6}"
        f"{'p50 A':>8}{'p50 B':>8}{'diff':>8}{'p90 A':>8}{'p90 B':>8}{'diff':>8}"
    )
    for key in both:
        x, y = a[key], b[key]
        lines.append(
            f"{key[0]:<{model_w}}{key[1]:<16}{x['n']:>6}{y['n']:>6}"
            f"{num(x['tps_total_p50']):>8}{num(y['tps_total_p50']):>8}"
            f"{delta(x['tps_total_p50'], y['tps_total_p50']):>8}"
            f"{num(x['tps_total_p90']):>8}{num(y['tps_total_p90']):>8}"
            f"{delta(x['tps_total_p90'], y['tps_total_p90']):>8}"
        )
    for label, only in (("A", a.keys() - b.keys()), ("B", b.keys() - a.keys())):
        if only:
            lines.append(
                f"\nonly in {label}: "
                + ", ".join(f"{m} [{bd}]" for m, bd in sorted(only, key=order))
            )
    return "\n".join(lines)


def main():
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

    rows = [
        derive(c) for f in files for c in calls_from_transcript(f) if c["out"] >= args.min_tokens
    ]

    if args.json:
        print(json.dumps(aggregate(rows, args.min_tokens), indent=2))
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
        print()


if __name__ == "__main__":
    main()
