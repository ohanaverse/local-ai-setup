# Can a local Qwen build fill a Claude tier? (2026-10-15 to 2026-10-16)

A dated record of the `eval-quant-tier` runs (`benchmarks/suites/eval-quant-tier.toml`). They compare the downloaded Qwen3.8 and Qwen3.6 builds on ollama, omlx and mtplx against Claude models whose tier is known, and then compare the Qwen3.8 builds' speed. The numbers are from this machine on these dates. Re-run the suite to get current ones.

## Conclusions

- **Qwen3.8-27B on ollama or mtplx sits just below standard.** It scored 90–91 on planning, against 93–94 for the standard anchors and about 74 for the quick ones. The engine, the quantization (4-bit or 8-bit) and the mtplx build (Quality, FP16, Speed) made no measurable difference.
- **Qwen3.6-27B (dense) falls between the tiers**, at 80–87. With six planning items, that can't be narrowed further.
- **Qwen3.6-35B-A3B is quick**, at 77–79 on every engine.
- **Avoid Qwen3.8 on omlx for now.** It scored 82–85, 5–8 points below the same model elsewhere, because it writes padded, checklist-style answers. The cause is open in #403. omlx is not behind on the Qwen3.6 models.
- **For Qwen3.8, ollama is the build to use.** Its quality matches mtplx, and it generates fastest: 64–83 tokens/s, against 45–63 for the mtplx builds.
- **Code review doesn't separate anything:** every row scored 96–99, anchors included.

## Setup

| | |
|---|---|
| Engines | omlx 0.7.0, then 0.7.1rc1 (the 27B run); ollama 0.40.5; mtplx 2.12.2 |
| Categories | planning (6 items) and code_review (6 items) |
| Judge | `anthropic/claude-opus-5` through OpenRouter, temperature 0, 1 sample (3 for the calibration) |
| Generation | Greedy (temperature 0) for the calibration and the first local run. Temperature 1.0 / top_p 0.95 / top_k 20 (the Qwen models' `generation_config.json`) for the 27B run |
| Isolation | One local model loaded at a time; each one is unloaded when its row is done |
| omlx limits | `sampling.max_tokens` and `max_context_window` raised from 32768 to 65536 before the 27B run |

## Calibration: which categories separate the tiers (greedy, cloud)

| Anchor | Tier | Planning | Code review |
|---|---|---|---|
| Claude Haiku 4.5 | quick | 74.7 | 92.8 |
| Claude Sonnet 4.5 | quick | 73.5 | 98.0 |
| Claude Haiku 5.5 | standard | 93.8 | 99.0 |
| Claude Sonnet 5 | standard | 93.0 | 98.7 |

Reasoning and doc_summary scored near 100 for every model, and code review barely separates. Only planning splits quick from standard, so the local rows ran planning and code review only. OpenRouter's hosted Qwen models were dropped as references because OpenRouter spread them across fp4 and fp8 hosts, so they weren't full precision.

## Run 1: first local run (greedy, 2026-10-15)

| Model | Engine / build | Planning | Code review |
|---|---|---|---|
| Qwen3.8-27B | ollama `qwen3.8:27b-mlx` | 91.0 | 97.8 |
| | omlx 8-bit | 80.5 | 98.2 (5/6 items) |
| | mtplx Optimized-Quality | 91.8 | 98.8 |
| Qwen3.6-35B-A3B | ollama `qwen3.6:35b-mlx` | 76.7 | 97.2 |
| | omlx 6-bit | 78.2 | 97.0 |
| | mtplx Optimized-Balance | 78.5 | 96.3 |

On omlx 8-bit, one code-review answer (39 KB) is thinking that looped and never closed, so the thinking itself came back as the answer. The judge failed on it here, so that code-review score is a mean over five items. A greedy re-run of the row reproduced the low planning score (81.0) and scored that item 34 (code review 87.7). Greedy decoding is what lets a Qwen3.x model loop inside its thinking, so the 27B run used the models' own sampling settings instead.

## Run 2: the 27B builds (sampled, 2026-10-16)

| Model | Engine / build | Planning | Code review |
|---|---|---|---|
| Qwen3.8-27B | ollama `qwen3.8:27b-mlx` (4-bit) | 90.3 | 98.0 |
| | omlx 4-bit | 80.8 (82.5 re-judged) | 97.3 |
| | omlx 8-bit | 84.3 (85.0 re-judged) | 97.8 |
| | mtplx Optimized-Quality (8-bit) | 91.0 | 99.2 |
| | mtplx Optimized-Quality-FP16 | 90.3 | 98.7 |
| | mtplx Optimized-Speed (mostly 4-bit) | 90.0 | 98.5 |
| Qwen3.6-27B | ollama `qwen3.6:27b-mlx` | 86.7 | 98.0 |
| | omlx 4-bit | 80.0 | 96.8 |
| | omlx 8-bit | 84.0 | 95.8 |
| | mtplx Optimized-Quality | 83.3 | 97.5 |

The FP16 build has the same 8-bit weights as Quality, with only the bf16 tensors cast to fp16 for M1/M2 chips. As expected, it scored the same.

### The judge cap

The judge used to see at most 20,000 characters of an answer. Seven of the twelve omlx Qwen3.8 planning answers were longer than that (20–33 KB), so they were cut, and the judge marked six of them incomplete. The cap is now 60,000. The saved answers of rows 01–03 were re-judged with it:

| Row | Planning before → after |
|---|---|
| ollama (control: no answer was over the cap) | 90.3 → 89.8 |
| omlx 4-bit | 80.8 → 82.5 |
| omlx 8-bit | 84.3 → 85.0 |

After the re-judge, no verdict calls an answer cut off, and mentions of verbosity rose from 28 to 40. Most of the omlx gap is the padding, not the cap. No other row had an answer over 20,000 characters, so no other score changes.

## Speed: Qwen3.8-27B (2026-10-16)

`llmbench run --direct --passes 3`: a 200-token completion at temperature 0, one model loaded at a time. Each figure is the range over the three passes. Tokens/s is the generation speed after the first token, thinking tokens included.

| Build | Weights | `chat` tok/s | `code` tok/s | First token (ms) |
|---|---|---|---|---|
| ollama `qwen3.8:27b-mlx` | 4-bit | 64–72 | 71–83 | 97–194 |
| mtplx Optimized-Speed | mostly 4-bit | 56 | 63 | 177–276 |
| mtplx Optimized-Quality-FP16 | 8-bit | 47–48 | 51–52 | 314–367 |
| mtplx Optimized-Quality | 8-bit | 45 | 53 | 227–349 |

ollama's first pass is the slowest each time. These are short completions; a long answer may not scale the same way.

## Open questions

- **#403:** why omlx's Qwen3.8 answers are padded. The template, thinking mode, sampling, omlx version and quantization are ruled out.
- **Anchor sampling:** the anchors were scored greedy and the 27B rows sampled. Re-run the anchors with the suite's `[generation]` table for exact parity.
- **Sample size:** six planning items, one generation each. Re-judging alone moved a score by about half a point; a fresh generation probably moves it more. Read gaps of 1–3 points as ties.
