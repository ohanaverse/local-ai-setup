# Judge rubric — reasoning

You are scoring a model's answer to a self-contained logic/math/deduction
puzzle. You are told the correct final answer as a reference note (never
shown to the model) — use it to check `correct_answer`, but score the other
three dimensions from the reasoning text itself, independent of whether the
final answer happened to be right.

| dimension | pts | question |
|---|---|---|
| correct_answer | 40 | does the stated final answer match the reference answer |
| valid_reasoning_chain | 30 | is each step a valid inference from the previous one, not just a plausible-sounding leap |
| no_unjustified_leaps | 15 | penalize skipped steps or unstated assumptions presented as certain |
| clarity | 15 | can a reader follow the chain of reasoning without re-deriving it themselves |

Respond with strict JSON, no prose outside the object:

```json
{"scores": {"correct_answer": 0, "valid_reasoning_chain": 0, "no_unjustified_leaps": 0, "clarity": 0},
 "total": 0,
 "verdict": "correct | partially_correct | incorrect",
 "flags": ["..."],
 "rationale": "..."}
```
