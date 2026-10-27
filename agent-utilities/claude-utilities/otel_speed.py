#!/usr/bin/env python3
"""Tokens/second and time-to-first-token from Claude Code OTLP trace exports (read-only).

Reads the JSONL written by otel_receiver.py. Each `claude_code.llm_request` span is one API call:

  ttft_s      = ttft_ms / 1000                    (client-measured time to first token)
  total_s     = duration_ms / 1000                (whole call, including retries)
  tps_total   = output_tokens / total_s           (lower bound: includes ttft)
  tps_decode  = output_tokens / (total_s - ttft_s)  (closer to decode speed; the first token
                                                   is counted without its time)

Failed calls and calls that needed a retry (attempt > 1) are skipped: their duration covers
more than one request. Compare with transcript_speed.py to see how far its estimates are off.

Usage: otel_speed.py [--min-tokens N] [--csv] FILE_OR_DIR...
"""

import argparse
import csv
import json
import statistics
import sys
from collections import defaultdict
from pathlib import Path

from transcript_speed import percentile

SPAN_NAME = "claude_code.llm_request"


def attr_value(v):
    """Unwrap an OTLP/JSON AnyValue; 64-bit ints arrive as strings."""
    if not isinstance(v, dict):
        return None
    for key in ("stringValue", "boolValue", "doubleValue"):
        if key in v:
            return v[key]
    if "intValue" in v:
        try:
            return int(v["intValue"])
        except (TypeError, ValueError):
            return None
    return None


def number(x):
    return x if isinstance(x, int | float) and not isinstance(x, bool) else None


def llm_request_spans(path):
    with open(path, encoding="utf-8", errors="replace") as fh:
        for line in fh:
            try:
                payload = json.loads(line)
            except ValueError:
                continue
            if not isinstance(payload, dict):
                continue
            for rs in payload.get("resourceSpans") or []:
                for ss in (rs or {}).get("scopeSpans") or []:
                    for span in (ss or {}).get("spans") or []:
                        if isinstance(span, dict) and span.get("name") == SPAN_NAME:
                            yield {
                                a["key"]: attr_value(a.get("value"))
                                for a in span.get("attributes") or []
                                if isinstance(a, dict) and "key" in a
                            }


def derive(attrs, session):
    """One row per usable call, or None when the span cannot give a clean timing."""
    out = number(attrs.get("output_tokens"))
    dur = number(attrs.get("duration_ms"))
    ttft = number(attrs.get("ttft_ms"))
    if attrs.get("success") is False or (number(attrs.get("attempt")) or 1) > 1:
        return None
    if out is None or dur is None or dur <= 0:
        return None
    total = dur / 1000
    decode = (dur - ttft) / 1000 if ttft is not None else None
    return {
        "session": session,
        "model": attrs.get("model") or "?",
        "out_tokens": out,
        "ttft_s": round(ttft / 1000, 2) if ttft is not None else None,
        "total_s": round(total, 2),
        "tps_total": round(out / total, 1),
        "tps_decode": round(out / decode, 1) if decode and decode >= 1 else None,
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
        if not p.exists():
            ap.error(f"no such file or directory: {p}")
        files += sorted(p.rglob("*.jsonl")) if p.is_dir() else [p]

    rows = []
    for f in files:
        for attrs in llm_request_spans(f):
            row = derive(attrs, f.name)
            if row and row["out_tokens"] >= args.min_tokens:
                rows.append(row)

    if args.csv:
        if rows:
            w = csv.DictWriter(sys.stdout, fieldnames=list(rows[0]))
            w.writeheader()
            w.writerows(rows)
        return

    by_model = defaultdict(list)
    for r in rows:
        by_model[r["model"]].append(r)
    print(f"{len(files)} file(s), {len(rows)} calls with >= {args.min_tokens} output tokens\n")

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
        print(f"  tps_decode  {stat([r['tps_decode'] for r in rs])}")
        print(f"  ttft_s      {stat([r['ttft_s'] for r in rs])}")
        print(f"  total_s     {stat([r['total_s'] for r in rs])}")
        print()


if __name__ == "__main__":
    main()
