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

Usage: transcript_speed.py [--min-tokens N] [--csv] PATH_OR_DIR...
  PATH_OR_DIR: a .jsonl transcript, or a directory searched recursively (e.g. ~/.claude/projects)
"""

import argparse
import csv
import json
import statistics
import sys
from collections import defaultdict
from datetime import datetime
from pathlib import Path

MIN_WINDOW_S = 1.0


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

    with open(path, errors="replace") as fh:
        for line in fh:
            try:
                d = json.loads(line)
            except ValueError:
                continue
            t = d.get("type")
            if t == "api-request" and d.get("timestamp"):
                pending_req = parse_ts(d["timestamp"])
            elif t == "assistant" and d.get("timestamp"):
                m = d.get("message", {})
                mid = m.get("id")
                ts = parse_ts(d["timestamp"])
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


def main():
    ap = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter
    )
    ap.add_argument("paths", nargs="+")
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

    files = []
    for p in map(Path, args.paths):
        files += sorted(p.rglob("*.jsonl")) if p.is_dir() else [p]

    rows = [
        derive(c) for f in files for c in calls_from_transcript(f) if c["out"] >= args.min_tokens
    ]

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

        def q(p):
            return vals[min(len(vals) - 1, int(p * len(vals)))]

        return f"p50 {statistics.median(vals):7.1f}  p90 {q(0.9):7.1f}  (n={len(vals)})"

    for model, rs in sorted(by_model.items()):
        print(model)
        print(f"  tps_total   {stat([r['tps_total'] for r in rs])}")
        print(f"  tps_window  {stat([r['tps_window'] for r in rs])}")
        print(f"  ttfb_s      {stat([r['ttfb_s'] for r in rs])}")
        print(f"  total_s     {stat([r['total_s'] for r in rs])}")
        print()


if __name__ == "__main__":
    main()
