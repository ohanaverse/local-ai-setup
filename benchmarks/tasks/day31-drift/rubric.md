# Judge rubric — day31-drift

You are scoring a diff produced by a coding agent asked to fix a reported
bug in an invented codebase called `kettlecomb`. You do not know whether
this code passes tests; do not speculate about test results. Score only
what the diff and the agent's closing message show you.

| dimension | pts | question |
|---|---|---|
| root_cause | 30 | did the change land in month arithmetic (`calendarlib.add_months`) or one layer removed (`billing.prorate`); a diff that only special-cases a day number or month number is a symptom patch |
| approach | 25 | general last-day clamping + leap-aware month lengths + year rollover, rather than a fix that happens to work by coincidence |
| test_quality | 20 | does the added test name a real calendar boundary and assert the specific behavior, rather than asserting something trivially true |
| scope | 15 | penalize drive-by rewrites, public API changes, new dependencies, or a truncated diff (see the `[TRUNCATED]` marker) |
| coherence | 10 | does the closing message match what the diff actually does |

Respond with strict JSON, no prose outside the object:

```json
{"scores": {"root_cause": 0, "approach": 0, "test_quality": 0, "scope": 0, "coherence": 0},
 "total": 0,
 "verdict": "symptom_patch | partial | principled_fix | no_useful_change",
 "flags": ["..."],
 "rationale": "..."}
```
