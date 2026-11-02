# Chat throughput: ollama, rec and Sushi — 2026-11-01

One 64 GB M5 Max Mac, `llmbench run` with the `chat` workload (200 completion tokens
per pass), three passes per model on the direct routes, started
2026-11-01T14:03:12Z. Medians below; the run directory does not record a git sha.

Run directory: `~/.config/local-ai/benchmarks/20261101-140312`.
Sushi's pack has since been uninstalled, so the Sushi rows cannot be reproduced on
this machine without reinstalling it.

## Summary (median per model / route)

| Model (route) | Passes | TTFT (ms) | Total (ms) | Throughput (tok/s) |
|---------------|-------:|----------:|-----------:|-------------------:|
| ollama/qwen3.8:27b-mlx (direct) | 3 | 123.0 | 2903.0 | 71.94 |
| rec/flint (direct) | 3 | 219.0 | 995.0 | 255.43 |
| sushi/beamster/Qwen3.8-Flash-Next-Sushi-STQ-2.75bpw (direct) | 3 | 257.0 | 2165.0 | 104.17 |

## Reading it

- **rec/flint is 2.5x the fastest and the shortest total**, at 255.4 tok/s and 995 ms
  for 200 tokens. Its slower first token (219 ms) never shows at this length.
- **Sushi beats ollama on decode** (104.2 against 71.9 tok/s) and is slower to first
  token (257 against 123 ms). For a 200-token reply it wins on total time too
  (2165 against 2903 ms).
- **The `litellm` rows carry no numbers, by design of the bug, not the run.** Every
  litellm-routed request came back in under 20 ms with
  `401 Client Error: Unauthorized for url: http://localhost:4000/v1/chat/completions`
  — the throughput route sent no proxy key. PR #511 fixed that (a seeded LiteLLM
  key is now sent); a single later pass measured ollama at 69 tok/s through litellm
  against 62 direct, which is not comparable to these medians.
- **mtplx was not in this run.** A later run the same night measured
  `Youssofal--Qwen3.6-35B-A3B-MTPLX-Optimized-Balance` at 141.5 tok/s and
  `Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality` at 44.4 tok/s
  (`~/.config/local-ai/benchmarks/20261102-002335`).

## Raw per-pass records

| Model | Route | Pass | TTFT (ms) | Total (ms) | Tokens | Throughput |
|-------|-------|-----:|----------:|-----------:|-------:|-----------:|
| ollama/qwen3.8:27b-mlx | direct | 1 | 189 | 3290 | 200 | 64.5 |
| ollama/qwen3.8:27b-mlx | direct | 2 | 123 | 2903 | 200 | 71.94 |
| ollama/qwen3.8:27b-mlx | direct | 3 | 102 | 2876 | 200 | 72.1 |
| rec/flint | direct | 1 | 163 | 946 | 200 | 255.43 |
| rec/flint | direct | 2 | 219 | 1036 | 200 | 244.8 |
| rec/flint | direct | 3 | 219 | 995 | 200 | 257.73 |
| sushi/beamster/Qwen3.8-Flash-Next-Sushi-STQ-2.75bpw | direct | 1 | 282 | 2364 | 200 | 96.06 |
| sushi/beamster/Qwen3.8-Flash-Next-Sushi-STQ-2.75bpw | direct | 2 | 257 | 2153 | 200 | 105.49 |
| sushi/beamster/Qwen3.8-Flash-Next-Sushi-STQ-2.75bpw | direct | 3 | 245 | 2165 | 200 | 104.17 |

## Related

- `eval-five-way-coding-2026-11-01.md` — the same models' quality, in this directory.
