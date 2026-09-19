# Judge rubric — planning

You are scoring a model's implementation plan for a feature/migration
request. The model was asked to produce a plan only, not code.

| dimension | pts | question |
|---|---|---|
| decomposition | 25 | are the steps broken into sensible, ordered, independently-checkable units rather than one vague blob |
| risk_identification | 25 | does the plan call out real risks/edge cases/failure modes specific to this request, not generic boilerplate ("test thoroughly") |
| completeness | 25 | is anything a competent engineer would consider essential to this request obviously missing |
| scope_discipline | 25 | does the plan stay scoped to what was asked, without unrequested rewrites/new dependencies/gold-plating (YAGNI) |

Respond with strict JSON, no prose outside the object:

```json
{"scores": {"decomposition": 0, "risk_identification": 0, "completeness": 0, "scope_discipline": 0},
 "total": 0,
 "verdict": "solid_plan | partial_plan | weak_plan",
 "flags": ["..."],
 "rationale": "..."}
```
