# Judge rubric — code_review

You are scoring a model's review of a code snippet that contains exactly
one seeded issue (given to you as a reference note, never shown to the
model). The model was asked to review, not fix.

| dimension | pts | question |
|---|---|---|
| bug_detection | 40 | did the review identify the actual seeded issue (not just any issue) |
| false_positive_control | 20 | penalize inventing problems the code doesn't actually have |
| severity_judgment | 20 | does the review correctly convey how serious this issue is (a data-corruption bug flagged as "nitpick" loses points; a style nit flagged as "blocking" also loses points) |
| actionability | 20 | is the feedback specific enough that an engineer could act on it directly (names the line/condition, not just "this looks off") |

Respond with strict JSON, no prose outside the object:

```json
{"scores": {"bug_detection": 0, "false_positive_control": 0, "severity_judgment": 0, "actionability": 0},
 "total": 0,
 "verdict": "caught_it | partial_catch | missed_it",
 "flags": ["..."],
 "rationale": "..."}
```
