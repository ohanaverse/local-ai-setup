# mtplx-profile-matrix — 2026-10-26 (#393)

Does an mtplx `--profile` change how a Claude Code slot copes with prefill memory? One 64 GB
Mac, mtplx 2.12.2, `Qwen3.8-27B-MTPLX-Optimized-Quality` on the `sonnet` and `haiku` slots of a
slot profile (the other slots on cloud models), Claude Code run non-interactively. Nothing else
local was loaded and 87 to 90 percent of memory was free at the start of every run. Every run
started a cold mtplx server. Method and columns: [`../mtplx-profile-matrix.md`](../mtplx-profile-matrix.md).

These runs used earlier, uncommitted versions of the script that the committed one merges.
Two things differ from the committed version, both explained below: the first runs' request
columns had a time-zone bug (the numbers here are recomputed), and the heavy task's notes had a
different line structure.

## Light task (prompts of 15k to 23k tokens)

| run | profile | turn 1 / turn 2 (s) | tests pass | requests (failed) | largest prompt served | mtplx guard actions |
|---|---|---|---|---|---|---|
| sustained-1 | sustained | 101 / 29 | yes | 9 (0) | 22.8k | 1 prefill admission shed, 1 pressure trim |
| turbo-1 | turbo | 83 / 22 | yes | 9 (0) | 23.6k | 3 prefill admission sheds |
| stable-1 | stable | 274 / 186, both exit 1 | **no** | 20 (**20**) | none | 62 sheds, 14 allocation failures, 14 prefill aborts |
| stable (repeat) | stable | 215, exit 1; the script stopped | not run | not recorded | none | not recorded |

The `stable` repeat failed its first turn with `API Error: 507 … this prompt projects 48.4 GiB
against the engine's 48.0 GiB limit (0.4 GiB over) … (16640 prompt tokens, 16640 not cached) …
A retry cannot succeed at this memory limit: the model's weights plus this prompt alone exceed
it`. The script stops after a failed first turn, so it has no result line.

## Heavy task (prompts of 40k to 47k tokens), three interleaved rounds

| run | profile | turn 1 / turn 2 (s) | tests pass | requests (failed) | avg / max request (s) | largest prompt served | admission sheds |
|---|---|---|---|---|---|---|---|
| H-sustained-1 | sustained | 186 / 29 | yes | 9 (0) | 20.7 / 57.0 | 46.6k | 3 |
| H-turbo-1 | turbo | 155 / 34 | yes | 8 (0) | 20.6 / 40.6 | 45.6k | 6 |
| H-sustained-2 | sustained | 130 / 24 | yes | 10 (0) | 12.6 / 33.5 | 45.0k | 3 |
| H-turbo-2 | turbo | 150 / 22 | yes | 8 (0) | 18.5 / 39.9 | 44.8k | 4 |
| H-sustained-3 | sustained | 120 / 18 | yes | 7 (0) | 14.4 / 47.0 | 40.4k | 3 |
| H-turbo-3 | turbo | 117 / 16 | yes | 9 (0) | 11.9 / 30.2 | 40.8k | 4 |

No run had a failed request, an allocation failure, a prefill abort or a pressure trim.
Turn 1 averaged 145 s for `sustained` and 141 s for `turbo`. `turbo` was shed more often (14
sheds against 9), and every shed request still succeeded.

## Why `stable` fails: no chunked prefill

From mtplx's own log, grouped by the profile named under each `Runtime contract verified`
line, for the `prefill_admission_shed` entries:

| profile | prefill chunk | scratch memory | prompts |
|---|---|---|---|
| stable | none (`prefill_chunk_requested: null`) | 17.3, 17.4 and 18.5 GiB | 15.5k, 15.6k and 16.6k tokens |
| sustained | 2048 tokens | 0.4 to 0.7 GiB | 21.6k to 45.9k tokens |
| turbo | 2048 tokens | 0.5 to 1.2 GiB | 22.8k to 45.9k tokens |

With the weights at 27.9 GiB and the engine's budget at 48.0 GiB, an unchunked 16k-token
prefill leaves no room, which is the refusal quoted above.

## What this does and does not show

- `sustained` and `turbo` served up to about 46.6k tokens with no refusals on an idle machine.
  The refusals reported in #393 did **not** recur with `sustained`, at larger prompts than that
  spike's 30k to 41k. The likelier cause is low free memory from other things on the Mac at the
  time, but that is a hypothesis: nothing here put the machine under pressure, so these runs do
  not show whether `turbo` copes better than `sustained` when memory is tight.
- `stable` cannot serve a Claude Code slot with prompts of this size on this machine: two runs,
  both failed on the first turn.
- One machine, one model, a few runs; not a benchmark.
- **Notes format.** The heavy task's notes were generated with each file as a single line,
  sentences separated by a literal backslash-n (an escaping slip in the first generator). The
  words are identical to what the committed script writes, which uses real line breaks, so the
  committed heavy task may produce different prompt sizes than the 40k to 47k recorded here.
  Check the `max_ok_prompt_tokens` column of a new run before comparing prompt sizes.
- **Column corrections.** The first runs' request count, failure and timing columns compared a
  naive-UTC timestamp with local time and were wrong; the light-task rows above are recomputed
  from the proxy's log (and the largest prompts are 22.8k and 23.6k, not the 21k and 23k first
  reported). The heavy runs used the corrected query. The error-log `insufficient memory` line
  count overcounts (one failed request writes several lines) and is not shown.
