# Test fixtures

Every file here is a trimmed copy of a public download made on 2026-10-12, cut down to the rows the tests name, or a hand-made broken variant of one. None is a complete or current copy of its source, and no score here should be read as that source's present value.

| Files | Source | Licence and credit |
|---|---|---|
| `modelsdev.json`, `modelsdev_too_few.json`, `modelsdev_malformed.json` | models.dev, `https://models.dev/api.json` | MIT. models.dev, `https://github.com/anomalyco/models.dev` |
| `openrouter.json`, `openrouter_too_few.json`, `openrouter_malformed.json` | OpenRouter models API, `https://openrouter.ai/api/v1/models` | Source: Artificial Analysis (artificialanalysis.ai) via OpenRouter (openrouter.ai/rankings). |
| `arena_page1.json`, `arena_page2.json`, `arena_too_few.json`, `arena_malformed.json`, `arena_error.json` | Arena agent leaderboard, Hugging Face dataset `lmarena-ai/leaderboard-dataset`, config `agent`, split `latest`, read through the dataset viewer's rows API | CC BY 4.0. Arena (arena.ai), dataset lmarena-ai/leaderboard-dataset |
| `eci_scores.csv`, `eci_scores_too_few.csv`, `eci_scores_malformed.csv` | Epoch AI benchmark data, `https://epoch.ai/data/benchmark_data.zip`, member `epoch_capabilities_index/eci_scores.csv` | CC BY 4.0. Epoch AI, 'Capabilities & benchmarking', https://epoch.ai/benchmarks |
| `inventory.json` | `wt model list --json` on the owner's machine | This repository. The registry path is made up; `size_bytes` and `path` are nulled. |

Changes made to the copies:

- **Rows removed.** Each file keeps between one and eight rows.
- **Fields removed.** models.dev rows keep `id`, `name`, `tool_call`, `open_weights`, `limit` and `canonical_model_id`. OpenRouter rows keep `id`, `name`, `context_length` and `benchmarks.artificial_analysis`. Arena rows keep `model_name`, `organization`, `score`, `score_ci_lower`, `score_ci_upper`, `rank` and `category`, and the page's `features` list is dropped. The Epoch CSV keeps every column.
- **Page counts rewritten.** `num_rows_total` in the Arena pages is the fixture's own row count (5, or 1 in `arena_too_few.json`), and `row_idx` is renumbered.
- **Broken on purpose.** The `*_malformed.*` files are in a shape the parser must refuse. `arena_error.json` is the error body the rows API returned during design, unchanged.

The Epoch fixtures are CSV text so they can be read and diffed; the `epoch_zip` fixture in `tests/conftest.py` puts one in a ZIP archive in memory.
