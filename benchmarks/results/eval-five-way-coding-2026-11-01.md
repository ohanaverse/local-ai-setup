# Five local models on HumanEval (coding only) — 2026-11-01

One 64 GB M5 Max Mac, `llmbench eval run --suite benchmarks/suites/eval-five-way.toml`
at git `15675c3b` (the commit that disabled EvalPlus's memory rlimit, #510), recorded
at 2026-11-01 19:23 local. Only the `coding` category ran; the four judged categories
did not, so the suite's `claude-cli` judge never spawned and this run spent no Claude
quota. HumanEval, 164 problems, `max_new_tokens = 16384` (EvalPlus's 768-token cap is
spent on a thinking model's reasoning), one sample.

Run directory: `~/.config/local-ai/benchmarks/eval-20261101-210526`.
The suite was renamed `eval-four-way.toml` and lost its Sushi row the same evening
(see `benchmarks/suites/eval-four-way.toml`); this record keeps the five-row result.

## Capability matrix

| family | row | route | coding |
|---|---|---|---|
| flint | 03--flint--direct | direct | 92.1% |
| qwen3.6 | 05--Youssofal--Qwen3.6-35B-A3B-MTPLX-Optimized-Balance--direct | direct | 89.0% |
| qwen3.8 | 01--Qwen3.8-Flash-Next-Sushi-STQ-2.75bpw--direct | direct | 93.9% |
| qwen3.8 | 02--qwen3.8:27b-mlx--litellm | litellm | 93.9% |
| qwen3.8 | 04--Qwen3.8-27B-MTPLX-Optimized-Quality--direct | direct | 93.9% |

## Reading it

- **The top three are one score, not three.** 93.9% is 154 of 164 problems, and the
  three rows at that value passed the same set. The differences between ollama
  Qwen3.8, mtplx Quality and Sushi are inside a single problem; this run ranks
  neither of them.
- **This is the first valid coding column of the day.** Runs `eval-20261101-125555`
  and `eval-20261101-132429`, earlier the same afternoon, scored `0.0%` or
  `EVALPLUS_ERROR` on every row: EvalPlus's memory rlimit was rejected on macOS 27,
  so no solution executed. #510 fixed that before this run. A sweep taken before it
  reads as "every model is the same", which is what an all-error column looks like.
- **The one clear separation is the bottom.** mtplx Qwen3.6-35B-A3B (89.0%, 146 of
  164) trailed the Qwen3.8 rows by eight problems, consistent with its planning
  scores in the judged run below.
- **Sushi is not the laggard.** Its row tied the best score here. It left the suite
  because its pack was uninstalled and its registry row removed, not because it
  scored worse.

## Related

- `eval-20261101-125555` — the same three models plus flint on `code_review`,
  `doc_summary`, `planning` and `reasoning`, judged by `claude-opus-5-5` through
  `claude-cli`: Sushi 93.0 / 85.7 / 76.8 / 98.5, ollama qwen3.8:27b-mlx
  90.5 / 84.5 / 82.2 / 99.3, rec flint 86.5 / 90.2 / 70.0 / 97.5. Sushi led
  `code_review`, ollama led `planning` and `reasoning`, flint led `doc_summary`;
  that run's `coding` column is the broken one described above.
- `chat-throughput-2026-11-01.md` — the same models' speed, in this directory.
