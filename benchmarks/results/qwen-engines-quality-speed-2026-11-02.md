# Qwen3.6 / Qwen3.8 / Flint on ollama, mtplx, omlx+Splash and rec — setup, speed and quality (2026-11-02)

One 64 GB M5 Max Mac (omlx 0.7.3, Splash 1.5.0, ollama 0.40.13, mtplx 2.12.2). Every Qwen3.6 and
Qwen3.8 variant installed for ollama, mtplx and omlx (served through Splash), plus Flint on `rec`.
Part of this is new and part reuses earlier runs, marked **new** or **reused** below.

- **Quality** is `llmbench eval` on the **planning** (6 items) and **code_review** (6 items)
  categories, the two the 2026-10-16 calibration found separate model tiers
  (`eval-quant-tier-2026-10-16.md`). Sampled at temperature 1.0 / top_p 0.95 / top_k 20.
- **Speed** is `llmbench run` (chat workload, 200 streamed tokens, direct route, median of 3–5 passes).
- **Setup** is a judgment from the repo docs and from installing Splash today; it is not measured.

## At a glance

Ratings are 1–5, higher is better. Setup 5 = one install and one pull. Speed (decode tok/s):
5 ≥ 150, 4 = 90–150, 3 = 60–90, 2 = 40–60, 1 < 40. Quality (planning score, same judge):
5 ≥ 90, 4 = 82–90, 3 = 74–82. The bands are wide on purpose: planning scores are only reliable to
about ±6 points, so a neighbouring-band split is not a claim unless the section below says it is real.

| Model | Engine | Setup | Speed | Quality | tok/s | Planning | Code review |
|---|---|:-:|:-:|:-:|---:|---:|---:|
| **Qwen3.8-27B** | ollama 4-bit | 5 | 3 | **5** | 71.9 | 91 | 98 |
| | omlx+Splash 4-bit | 3 | **4** | 4 | 101.7 | 85.5 | 97.2 |
| | omlx+Splash 8-bit | 3 | 2 | 4 | 54.5 | 84.5 | 96.5 |
| | mtplx Quality (8-bit) | 3 | 2 | **5** | 44.4 | 92 | 99 |
| | mtplx Speed (mostly 4-bit) | 3 | 2 | **5** | 56 | 90 | 98.5 |
| **Qwen3.6-27B** | ollama 4-bit | 5 | 3 | 4 | 65.1 | 86.7 | 98 |
| | omlx+Splash 4-bit | 3 | 1 | 4 | 38.3 | 82.5 | 96.8 |
| | omlx+Splash 8-bit | 3 | 1 | 4 | 24.7 | 84.3 | 97.8 |
| | mtplx Quality | 3 | 2 | 4 | 47.1 | 83.3 | 97.5 |
| **Qwen3.6-35B-A3B** | ollama 4-bit | 5 | **5** | 3 | 167.5 | 78.2 | ~96 |
| | omlx+Splash 6-bit | 3 | **5** | 3 | 227.3 | 79.5 | ~95 |
| | mtplx Balance | 3 | 4 | 3 | 141.5 | 80.2 | ~96 |
| **Flint** | rec | 3 | **5** | 3 | 255.4 | 74.0 | 93.0 |

Code review for the 35B rows is the mean of four runs (see below); every other row is one run.

**Setup ratings**

| Engine | Rating | Why |
|---|---|---|
| ollama | 5 | `brew install ollama` (or the app), then `ollama pull <name>`. Models are plain names; the daemon runs on its own. |
| omlx + Splash | 3 | omlx alone is about 4: `brew install omlx`, with downloads placed flat in `~/.omlx/models`. Splash adds a tap (`brew install incoai/tap/splash`), a per-family DFlash2 draft download (about 4 GB), a per-model engine toggle (`external_engine`), and a 29–35 GB memory budget per model. Qwen3.6-27B has no draft of its own, so it borrows the Qwen3.8 one. |
| mtplx | 3 | Its own CLI and special "MTPLX-Optimized" builds fetched with `mtplx pull` (not every checkpoint works; the Qwen3.8 FP16 build is gone from disk). One model per process on port 8003, about 28 GB resident. No wt-managed model sets a `--profile`, and the repo's profile test (`mtplx-profile-matrix-2026-10-26.md`) shows some profiles refusing large prompts with HTTP 507. Rated from the docs; mtplx was not set up in this session. |
| rec (Flint) | 3 | A separate engine (`~/.local/bin/rec`) serving only Flint, one instance per machine on port 11500, with a key from `rec auth token`; registered with `wt model add rec flint`. |

## What stands out

- **ollama is the best all-rounder.** Easiest setup, and on the 35B and Qwen3.6-27B it matches or
  beats the other engines on speed class and quality. On Qwen3.8-27B it has the top quality but not
  the top speed.
- **Splash's one clear win is Qwen3.8-27B 4-bit decode speed**: 101.7 tok/s against ollama's 71.9
  (+41%), at a cost of 6–9 planning points and a harder setup. On the 35B it is 36% faster again
  (227 vs 168), but both rate 5 on the speed scale. On Qwen3.6-27B it is the slowest.
- **mtplx never wins at its default profile.** It is slower than ollama on all three models, with
  equal quality. A faster `turbo` profile exists but was not measured here (no model has one set).
- **Flint is the fastest model measured** (255 tok/s) and the weakest on planning (74.0); its
  quality is roughly the 35B's, well below Qwen3.8-27B.
- **Quality is mostly the model, not the engine.** The one engine effect is Qwen3.8 planning on
  omlx+Splash. Model choice matters more than engine choice: Qwen3.8-27B scores about 92 on
  planning, the 35B about 79, Flint 74.

## Quality detail (score /100)

| Model | Engine / build | Planning | Code review | Source |
|---|---|---:|---:|---|
| **Qwen3.8-27B** | ollama `qwen3.8:27b-mlx` (4-bit) | 90.3 | 98.0 | reused 10-16 |
| | mtplx Optimized-Quality (8-bit) | 91.0 | 99.2 | reused 10-16 |
| | mtplx Optimized-Speed (mostly 4-bit) | 90.0 | 98.5 | reused 10-16 |
| | mtplx Optimized-Quality-FP16 | 90.3 | 98.7 | reused 10-16; build no longer on disk |
| | omlx+Splash 4-bit | 85.5 | 97.2 | **new** |
| | omlx+Splash 8-bit | 84.5 | 96.5 | **new** |
| **Qwen3.6-27B** | ollama `qwen3.6:27b-mlx` (4-bit) | 86.7 | 98.0 | reused 10-16 |
| | mtplx Optimized-Quality | 83.3 | 97.5 | reused 10-16 |
| | omlx+Splash 4-bit | 82.5 | 96.8 | **new** |
| | omlx+Splash 8-bit | 84.3 | 97.8 | **new** |
| **Qwen3.6-35B-A3B** | ollama `qwen3.6:35b-mlx` (4-bit) | 78.2 | 98.2 (mean of 4 runs: 96.3) | **new** (re-run sampled) |
| | mtplx Optimized-Balance | 80.2 | 95.2 (mean of 4 runs: 95.8) | **new** (re-run sampled) |
| | omlx+Splash 6-bit | 79.5 | 90.7 (mean of 4 runs: 94.9) | **new** |
| **Flint** | rec | 74.0 | 93.0 | reused 11-01, re-judged (see Flint) |

The 2026-10-15 greedy 35B rows were ollama 76.7 / 97.2 and mtplx 78.5 / 96.3. They were re-run
sampled because a sampled Splash row isn't comparable to greedy ones, and greedy lets Qwen3.x loop
inside its thinking. Tier anchors from the 10-16 calibration: quick about 74, standard about 93 on planning.

## Speed detail (decode tok/s, first token ms)

| Model | Engine / build | tok/s | TTFT ms | Source |
|---|---|---:|---:|---|
| **Qwen3.8-27B** | ollama 4-bit | 71.9 | 123 | reused 11-01 (3 passes) |
| | mtplx Optimized-Quality | 44.4 | 221 | reused 11-02 early (3 passes) |
| | mtplx Optimized-Speed | 56 | 177–276 | reused 10-16 (3 passes) |
| | omlx+Splash 4-bit | **101.7** | 97 | reused 11-02 (5 passes) |
| | omlx+Splash 8-bit | 54.5 | 100 | **new** (5 passes) |
| **Qwen3.6-27B** | ollama 4-bit | 65.1 | 119 | **new** (3 passes) |
| | mtplx Optimized-Quality | 47.1 | 297 | **new** (3 passes) |
| | omlx+Splash 4-bit | 38.3 | 87 | reused 11-02 (5 passes) |
| | omlx+Splash 8-bit | 24.7 | 103 | **new** (5 passes) |
| **Qwen3.6-35B-A3B** | ollama 4-bit | 167.5 | 75 | **new** (3 passes) |
| | mtplx Optimized-Balance | 141.5 | 131 | reused 11-02 early (3 passes) |
| | omlx+Splash 6-bit | **227.3** | 46 | reused 11-02 (5 passes) |
| **Flint** | rec | **255.4** | 219 | reused 11-01 (3 passes) |

### omlx in-process vs omlx+Splash (speed, 5 passes, thinking off)

Measured first, to see what Splash adds over omlx's own MLX engine on the same checkpoints:

| Model | in-process tok/s · TTFT ms | Splash tok/s · TTFT ms | Decode speedup |
|---|---|---|---:|
| Qwen3.8-27B-4bit (matched `Qwen3.8-27B-DFlash2` draft) | 31.83 · 173 | 101.73 · 97 | **3.20x** |
| Qwen3.6-27B-4bit (borrowed Qwen3.8 draft) | 31.22 · 178 | 38.29 · 87 | 1.23x |
| Qwen3.6-35B-A3B-6bit (matched `Qwen3.6-35B-A3B-DFlash2` draft) | 160.0 · 78 | 227.27 · 46 | 1.42x |

- There is no Qwen3.6-27B draft, so omlx pairs it with the Qwen3.8-27B one. A mismatched draft keeps
  the output exact (a dense target verifies every row) but loses most of the speedup.
- For MoE targets Splash verifies only a budgeted subset of draft rows (its log: "greedy and seeded
  outputs can differ from run to run"), so 35B output is not strictly the same as in-process output.
- Splash's memory budget counts as the model's size in omlx's memory guard: 29.2 GB (Qwen3.8-27B
  4-bit), 29.3 GB (Qwen3.6-27B 4-bit), 35.4 GB (35B 6-bit). Cold start was about 6 s for the 27Bs
  and about 2 minutes for the MoE; the first load hashes the weights once.
- TTFT is flattered on both sides: llmbench repeats a fixed prompt, so each engine's prefix cache
  applies. Treat TTFT as directional and decode tok/s as the real signal.

## Which differences are real

Per-item score differences between two rows scatter by about 4–6 points on planning (SD of the
paired differences), so a row mean moves by 2 points or more from noise alone. With 6 items, a
planning gap needs roughly 6 points to stand out (exact sign-flip test, p about 0.03 at best);
code review is near its ceiling (96–99) and nothing there separates.

| Difference | Verdict |
|---|---|
| Qwen3.8-27B planning, Splash (84.5–85.5) below ollama (91.8) and mtplx (93.5), same judge | **Real**: 6–9 points, p = 0.03–0.06; the same gap appeared on 10-16 with in-process omlx; matches the 2x longer answers |
| Qwen3.8-27B (ollama/mtplx) vs Qwen3.6-35B-A3B on planning (about 92 vs 79) | **Real**: 10+ points, seen in both campaigns |
| Speed differences (e.g. 101.7 vs 71.9 tok/s) | **Real**: passes vary by 1–2% |
| Splash 4-bit vs 8-bit (either model, either category) | Noise: 1–2 points, p >= 0.3 |
| ollama vs mtplx on Qwen3.8 (any build) | Noise: under 2 points |
| Qwen3.8 code review on every engine (96.5–99.2) | Noise (and at the ceiling) |
| Qwen3.6-35B planning across engines (78–80) | Noise |
| Qwen3.6-35B code review, Splash 90.7 vs ollama 98.2 | Noise: see the replicates below |
| Qwen3.6-27B planning across engines (82.5–86.7) | Likely noise: 4 points or less, no same-judge per-item data |

### The Qwen3.8 planning gap is the omlx padding (#403), and it follows omlx, not MLX

Median planning answer length: omlx+Splash Qwen3.8 20–24 KB (max 38 KB), against about 11 KB for
ollama and mtplx on the same prompts. Qwen3.6 answers are the same length on every engine (about
6–7 KB) and show no gap. Splash replaced the inference engine and the padding stayed, so it points
at omlx's request or chat-template handling for Qwen3.8, not at MLX. This is a strong hint from
lengths and scores, not a proof.

Same-judge paired comparison (`claude-opus-5`; ollama and mtplx rows are the saved 2026-11-01
answers, re-judged; negative means Splash is lower):

| Splash row | vs ollama | vs mtplx Quality |
|---|---|---|
| 4-bit, planning | -6.3 (p = 0.031) | -8.0 (p = 0.031) |
| 8-bit, planning | -7.3 (p = 0.062) | -9.0 (p = 0.031) |
| 4-bit, code review | +0.2 (p = 1.0) | -0.5 (p = 0.91) |
| 8-bit, code review | -0.5 (p = 0.72) | -1.2 (p = 0.50) |

Re-judged means: ollama planning 91.8 / code review 97.0; mtplx Quality planning 93.5 / 97.7.

### Qwen3.6-35B-A3B code review, four runs per engine

The Splash row's 90.7 was one bad `exception-swallowing` answer (66). Three fresh generations per
engine (same judge, same sampling) say it was a bad draw, not an engine effect:

| Engine | Run means (orig, rep1, rep2, rep3) | Mean | SD between runs | `exception-swallowing` |
|---|---|---:|---:|---|
| omlx+Splash 6-bit | 90.7, 97.3, 96.5, 95.2 | 94.9 | 3.0 | 66, 93, 94, 92 |
| ollama 4-bit | 98.2, 97.0, 94.0, 96.0 | 96.3 | 1.8 | 99, 91, 80, 92 |
| mtplx Balance | 95.2, 95.3, 95.8, 96.8 | 95.8 | 0.7 | 84, 84, 90, 96 |

Engine differences are 0.5–1.4 points, smaller than the spread within each engine (run-level exact
permutation test: p = 0.51 Splash vs ollama, 0.77 Splash vs mtplx, 0.63 mtplx vs ollama). Every
engine has at least one bad run on `exception-swallowing`, ollama as low as 80.

## Flint (rec)

- **Speed:** 255.4 tok/s, 219 ms first token, 995 ms for 200 tokens (`chat-throughput-2026-11-01.md`).
- **Quality:** the 2026-11-01 run was judged by Opus 5.5, which scores about 4–9 points lower than
  the judge used everywhere else here (70.0 planning, 86.5 code review). Flint's saved answers were
  re-judged with `claude-opus-5`: planning 74.0 (items 69, 68, 87, 64, 74, 82), code review 93.0
  (items 78, 98, 96, 95, 93, 98). One generation per category, so read planning as ±6.
  Against Qwen3.8-27B (about 92) the planning gap is real; against the 35B (about 79) it is within noise.
- **Coding:** 92.1% on EvalPlus HumanEval (`eval-20261101-210526`), against 93.9% for ollama
  Qwen3.8-27B and mtplx Qwen3.8-27B Quality, and 89.0% for the mtplx 35B. That score uses no judge.
- **Not measured:** `ollama/flint:40b` is on disk (22.6 GB) but unregistered and never benchmarked.

## Caveats

- **Two judge routes.** Reused Qwen rows were judged by `anthropic/claude-opus-5` through OpenRouter
  (temperature 0); new rows by `claude-opus-5` through the `claude-cli` route (subscription), which
  ignores temperature. Re-judging the saved 2026-11-01 ollama and mtplx Qwen3.8 answers with the new
  judge gave planning 91.8 and 93.5, against 90.3 and 91.0 from 10-16, so the routes agree within
  about 2 points. **Do not mix in anything judged by Opus 5.5**: it scored the same Qwen3.8 rows
  82.2 and 83.8, about 9 points lower. The re-judged copies were made in a scratch directory and are
  not kept; the scores are recorded in this document.
- **Dates and versions.** Reused quality is from 2026-10-15/16 (ollama was 0.40.5, now 0.40.13; mtplx
  unchanged at 2.12.2). Reused speed runs are from 2026-10-16 and 2026-11-01/02.
- **Speed conditions are not identical.** The Splash speed runs set omlx `enable_thinking=false` and
  used 5 passes; ollama, mtplx and rec ran at their defaults with 3 passes. Decode tok/s is largely
  unaffected. The mtplx rows use the default profile.
- **One sample per row** (6 items, one generation each) except the four-run 35B code review.
- **Splash eval runs are labeled `omlx` in their `run.toml`**, because Splash is an omlx per-model
  setting, not an llmbench row. The run directories below are the Splash ones.
- **Do not use omlx's built-in admin benchmark for this.** `POST /admin/api/bench/start` uploads every
  completed result to `https://omlx.ai/api/benchmarks` (hardcoded in `omlx/admin/benchmark.py`, no
  setting or environment variable turns it off). llmbench was used instead and publishes nothing.
- **Left out:** the mtplx Qwen3.8 FP16 build (not on disk; its 10-16 scores are kept above), an
  unregistered `Qwen3.6-27B-AEON-Ultimate-Uncensored` finetune sitting in omlx, `ollama/flint:40b`,
  and coding plus the other eval categories for the Qwen rows.

## Run directories (`~/.config/local-ai/benchmarks/`)

| Run | What |
|---|---|
| `20261102-030951`, `-031411` | speed: Qwen3.8-27B 4-bit, omlx in-process / Splash |
| `20261102-031602`, `-031820` | speed: Qwen3.6-27B 4-bit, omlx in-process / Splash |
| `20261102-032029`, `-032412` | speed: Qwen3.6-35B-A3B 6-bit, omlx in-process / Splash |
| `20261102-033247` | speed: ollama 3.6-27B and 3.6-35B, mtplx 3.6-27B |
| `20261102-043038`, `-045341` | speed, Splash: Qwen3.8-27B 8-bit, Qwen3.6-27B 8-bit |
| `eval-20261102-033510` | quality: ollama 35B and mtplx 35B (sampled) |
| `eval-20261102-034845`, `-040609`, `-042528` | quality, Splash: Qwen3.8-27B 4-bit, Qwen3.6-27B 4-bit, Qwen3.6-35B 6-bit |
| `eval-20261102-043229`, `-045546` | quality, Splash: Qwen3.8-27B 8-bit, Qwen3.6-27B 8-bit |
| `eval-20261102-125312`, `-125506`, `-125704` | 35B code review replicates, Splash |
| `eval-20261102-125900`, `-130138`, `-130621` | 35B code review replicates, ollama |
| `eval-20261102-130952`, `-131306`, `-131621` | 35B code review replicates, mtplx |
| `eval-20261101-125555` (row 03), `-142647` (row 04), `-210526` | Flint, mtplx Qwen3.8 and coding runs reused or re-judged |

## How it was run

Quality used `benchmarks/suites/eval-quant-tier.toml` with one change, the judge:
`route = "claude-cli"`, `model = "claude-opus-5"`, categories `planning` and `code_review`
(`uv run --directory llmbench llmbench eval run --suite <suite> --root ../benchmarks/tasks/eval
--category planning --category code_review --row <index>`). Splash was turned on per model with
`PUT /admin/api/models/<id>/settings {"external_engine": "splash"}` and off again afterwards, one
model at a time, with thinking at its default for quality runs and `enable_thinking=false` for the
speed runs. All five omlx models are back at their defaults. Speed used
`uv run --directory llmbench llmbench run --model <id> --direct --passes N`.
