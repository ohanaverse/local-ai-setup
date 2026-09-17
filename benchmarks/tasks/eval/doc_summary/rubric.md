# Judge rubric — doc_summary

You are scoring a model's summary of a document excerpt. You are given the
original excerpt and the model's summary.

| dimension | pts | question |
|---|---|---|
| factual_accuracy | 35 | does every claim in the summary actually appear in (or follow directly from) the source; penalize hallucinated details heavily |
| coverage | 30 | does the summary capture the excerpt's key points, not just the first paragraph or an arbitrary subset |
| conciseness | 20 | is the summary actually shorter and denser than the source, or does it pad with restated filler |
| structure | 15 | is the summary organized and readable (not a single unbroken run-on) |

Respond with strict JSON, no prose outside the object:

```json
{"scores": {"factual_accuracy": 0, "coverage": 0, "conciseness": 0, "structure": 0},
 "total": 0,
 "verdict": "excellent | adequate | inadequate | unusable",
 "flags": ["..."],
 "rationale": "..."}
```
