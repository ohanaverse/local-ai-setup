# modeltiers: capability tiers for routing — design

Date: 2026-10-12
Research: the owner's note "Routing coding-agent work to model tiers" (outside the repo; no issue tracks this work yet)

## Problem

wt offers every configured model in one list, ordered by price. Nothing in the repo says which models suit which kind of work.

- **No tier exists.** `registry.toml` and `~/.config/agent-wt/config.toml` hold no tier, class or role. Each launch pins one model.
- **The evidence is scattered.** Capability metadata and benchmark scores live in separate public sources, each with its own model names and its own scale.
- **Names do not join.** One model is `glm-5.3:cloud` in ollama, `z-ai/glm-5.3` on OpenRouter, `zhipuai/glm-5.3` in models.dev, `GLM 5.3 (Max)` on Arena and `GLM-5.3` at Epoch. Matching by name alone joins `deepseek-v4-pro:cloud` to the wrong model, with no error.
- **Brand order is not capability order.** On 2026-10-12 Claude Sonnet 5.5 sat above Claude Fable 5.1 on the Artificial Analysis intelligence index and on Epoch's capabilities index.

## Goal

A stand-alone tool, `modeltiers`, reads the wt inventory, models.dev and three public benchmark sources, and writes an advisory tier for every model with the evidence behind it. Nothing routes on the result in this phase.

**Success criteria**

1. `modeltiers run` writes `~/.config/local-ai/tiers.toml`. Every model `wt model list --json` reports, apart from the native agent rows, is in `[[models]]` or in `[[unmapped]]`.
2. Every `[[models]]` and `[[candidates]]` entry carries a tier, a confidence, the votes and the raw scores.
3. `modeltiers explain <model>` shows, from the output file alone, how the model was identified and why it has its tier.
4. Models that appear in a benchmark source and not in the inventory are listed as candidates with a tier.
5. A run in which a sentinel model's votes or tier differ from the pinned values leaves the existing output file untouched and exits 3.
6. A run with one source failing uses that source's last good snapshot, marks the source stale and exits 2.
7. No test touches the network, the real config home or the real cache.
8. The tool never writes `registry.toml`, `config.yaml` or any wt state.

On the 2026-10-12 inventory no model reaches `deep` or `marathon`: the highest intelligence index among inventory models is 44.8 against a deep floor of 49.0. The native agent rows that launch Claude and GPT models are not tiered. The top two tiers are therefore held by candidates only, which is what makes the candidate list the shortlist of models worth adding.

## Decisions

These were settled with the owner during design and are not open.

| Topic | Decision |
|---|---|
| Meaning of a tier | Advisory label. Nothing routes on it in this phase. |
| Coverage | Every inventory model, plus every model with at least one benchmark vote (the candidates). |
| Home | New top-level directory `modeltiers/`, a uv-managed Python package with its own tests and CI. |
| Sources | models.dev (metadata and identity), OpenRouter models API, Arena agent dataset, Epoch AI benchmark ZIP. All keyless. |
| Artificial Analysis API | Not used. Its indices arrive through OpenRouter's payload. |
| Tier rule | Numeric floors per signal in a committed file, guarded by sentinel models. |
| Voting signals | Three: the intelligence index, Arena's agent score, Epoch's capabilities index. The coding and agentic indices are recorded as evidence and do not vote. |
| Marathon | Reached only through Arena's agent score, tested on the lower bound of its confidence interval. The intelligence index and the Epoch index vote `deep` at most. |
| Local builds | Inherit the tier of the model they resolve to, marked `inherited`. |
| llmbench results | Not an input in this version. |
| Output | A sidecar file, `~/.config/local-ai/tiers.toml`. `registry.toml` is not touched. |

## Out of scope

- **Tiers in the wt model selector.** The next phase. This spec fixes the file wt will read.
- **llmbench results as an input.** One model has a usable local score today and no floors exist for llmbench categories.
- **Speed and cost as tier inputs.** No keyless source gives measured speed. Cost is already a selector column. A tier here is a capability band.
- **Arena's per-signal boards.** Tool hallucination, bash recovery and the other `agent_*` configs are not fetched. No calibrated cut-off exists to use them as gates, and each is another request that can fail.
- **Export to routing config.** No `ANTHROPIC_DEFAULT_*_MODEL` variables and no LiteLLM tier block.
- **Scheduling.** Runs are manual. The exit codes are designed so a later scheduled run can tell a clean result from one that needs a look.
- **The retired modelman "cost tier" TUI.** Unrelated; that concept sorted models by price.

## What the sources provide

Read from live fetches on 2026-10-12. All four answered without a key.

| Source | URL | Used for | Size and shape |
|---|---|---|---|
| models.dev | `https://models.dev/api.json` | Identity spine; `tool_call`, `open_weights`, `limit.context` | 226 providers, 8,466 provider-model rows, 446 distinct `canonical_model_id` values on 65% of rows. Has `ollama-cloud` and `openrouter` providers keyed by the host's own id. |
| OpenRouter | `https://openrouter.ai/api/v1/models` | `benchmarks.artificial_analysis.{intelligence_index,coding_index,agentic_index}` | 458 models. Intelligence index on 284, coding on 169, agentic on 130. `:free` and `:batch` variants repeat their base model's scores. |
| Arena | Hugging Face rows API, dataset `lmarena-ai/leaderboard-dataset`, config `agent`, split `latest` | Agent score with a 95% confidence interval | 52 rows, 50 models. The score is centred near zero and relative to the pool; higher is better and 19 rows are negative. Effort is usually in the name: `Claude Opus 5.5 (High)`. CC BY 4.0. |
| Epoch AI | `https://epoch.ai/data/benchmark_data.zip`, file `epoch_capabilities_index/eci_scores.csv` | Capabilities index with a confidence interval | 274 models, one row per model, display names, no effort suffix. CC BY 4.0. |

- **Arena is read as JSON, not parquet.** `https://datasets-server.huggingface.co/rows?dataset=lmarena-ai/leaderboard-dataset&config=agent&split=latest&offset=N&length=100`, paged until `num_rows_total` rows are read. This avoids a parquet dependency. A JSON body with an `error` key is a failed download; the service returned "the dataset index is loading" and a 502 during design.
- **Not used:** LiteLLM's price JSON (no OpenRouter keys, a stale ollama list), Terminal-Bench (no machine-readable feed; Epoch's copy stops in May 2026).

**Why only three signals vote.** The coding and agentic indices are missing for the newest frontier models (Opus 5.5, Sonnet 5.5, GPT-6.1 Sol). The agentic index puts 37 of 92 models at 8.5 or below, including Gemini 3.1 Pro, and scores GLM-5.3-Flash (50.9) level with GPT-6 Astra (51.0). The coding index has no confidence intervals and its top band is one model.

**Why marathon tests a lower bound.** On Arena the lowest marathon model and the highest deep model are 0.007 apart on the point score (Fable 5.1 at 0.1266, Sonnet 5.5 at 0.1195), against intervals of ±0.02 to ±0.033. The lower bounds separate cleanly: 0.1215, 0.1073 and 0.1061, then 0.0868.

## Design

### Package and commands

`modeltiers/` holds `pyproject.toml`, `Makefile`, `CLAUDE.md`, `src/modeltiers/` and `tests/`. Python is `==3.13.*`, as in llmbench. The only runtime dependency is `tomli-w`; the CLI uses `argparse`. Development dependencies are pytest, pytest-cov, ruff and mypy.

It is run as `uv run --directory modeltiers modeltiers <command>`.

| Command | Effect |
|---|---|
| `modeltiers run [--dry-run] [--offline] [--inventory FILE]` | Fetch, resolve, assign, print the change report, write. `--dry-run` does everything except write the output file. `--offline` fetches nothing and reads each source's newest good snapshot. `--inventory FILE` reads saved `wt model list --json` output and does not run wt. |
| `modeltiers show [--candidates]` | Print the tier table from the output file: inventory models by default, candidates too with `--candidates`. Prints the file's age in days, with a warning line past 14 days, and the attribution lines. |
| `modeltiers explain <model>` | Print an entry's identity, votes, scores, efforts, gates, notes and the attribution lines, from the output file. |

`show` and `explain` read only the output file. They never fetch and never run wt.

**`explain` lookup.** The argument is tried as an exact `provider_id/model_name` first, then as a canonical key, compared without regard to case. A canonical key prints every `[[models]]` entry that resolved to it, or else the candidate. An unmapped row prints that it is unmapped and needs an alias. No match prints the file's generated date and exits 1.

**Change report.** `run` prints it before writing, and `--dry-run` prints the same. It compares the new result with the existing output file, keyed on (`provider_id`, `model_name`): tier changes as old → new, rows added and removed, rows newly unmapped or newly resolved, and each source's status. Confidence-only changes and candidate changes are printed as counts. With no existing file it says so and prints the full table.

### Paths

| File | Default | Override |
|---|---|---|
| Output | `$XDG_CONFIG_HOME/local-ai/tiers.toml`, else `~/.config/local-ai/tiers.toml` | `MODELTIERS_OUT` |
| Snapshots | `$XDG_CACHE_HOME/local-ai/modeltiers/`, else `~/.cache/local-ai/modeltiers/` | `MODELTIERS_CACHE` |
| Contracts | `modeltiers/src/modeltiers/data/contracts.toml` (committed) | none |
| Aliases | `modeltiers/src/modeltiers/data/aliases.toml` (committed) | none |

**Redirected registry.** wt's inventory follows `WT_REGISTRY`; the output path does not. `wt model list --json` reports the registry path it read. When that path is not `registry.toml` in the output file's directory and `MODELTIERS_OUT` is unset, `run` refuses to write and exits 1. A scratch inventory can then never replace the real `tiers.toml`.

### Pipeline

Each stage is one module with plain data in and out.

1. **Inventory** (`inventory.py`). Runs `wt model list --json`, or reads `--inventory FILE`. The document is an object with `models`, `registry` and `providers`. From each row it keeps `provider_id`, `model_name`, `location` and `registered`. Rows whose `model_name` is `native` are dropped. Discovered rows (`registered = false`) and rows whose weights are missing are tiered like any other. The join key for everything downstream is the pair (`provider_id`, `model_name`), never the registry `id`. A provider whose probe status is not `ok` is named in a warning, because its discovered rows may be absent from this run.
2. **Fetch** (`fetch.py`). One function per source behind a `Fetcher` seam (`Callable[[str], bytes]`). A request times out after 30 seconds and is retried once after 2 seconds on a timeout, a 5xx status or an Arena `error` body.
3. **Parse** (`sources/modelsdev.py`, `openrouter.py`, `arena.py`, `epoch.py`). Pure functions from snapshot bytes to records. A shape the parser does not recognise raises `SourceParseError`; a parser never returns a short list. Each parser has a minimum row count: models.dev 1,000, OpenRouter 100, Arena 20, Epoch 100.
4. **Resolve** (`identity.py`). Maps inventory rows and source records to canonical keys.
5. **Assign** (`assign.py`). Votes, base tier, marathon promotion, sentinels, gates, confidence.
6. **Write** (`output.py`). Builds the document and writes it atomically (temporary file in the same directory, then rename).

### Snapshots

Each source has its own directory: `<cache>/<source>/<UTC stamp>.<ext>`. The fetched bytes are saved before parsing. Arena's pages are saved as one JSON document holding each page body unparsed.

- **Good and bad.** A snapshot that parses and meets its row minimum is good. One that does not is renamed `<UTC stamp>.bad.<ext>`, its path is printed, and it is never read again.
- **Fallback.** When a source's download or parse fails, the run uses that source's newest good snapshot and marks the source `stale`, with the snapshot's own `fetched_at`. With no good snapshot the source is `unavailable`.
- **`--offline`** reads each source's newest good snapshot, marks it `stale`, saves nothing and prunes nothing.
- **Pruning.** After a successful write, each source keeps its four newest good snapshots and its two newest bad ones.

### Identity

**Canonical key.** `vendor/model[-snapshot]` exactly as models.dev spells it in `canonical_model_id`, for example `zhipuai/glm-5.3`, `anthropic/claude-opus-5-5` and `minimax/MiniMax-M3`. Keys are stored in that spelling and compared without regard to case. Host, quantization and reasoning effort are attributes of a record, never part of the key. The valid keys are every `canonical_model_id` in the models.dev snapshot, plus any key an alias declares.

**Resolution order.** The same three steps apply to an inventory row and to a source record. The first that answers wins, and the entry records which one in `resolved_by`.

1. **Alias** (`alias`). An exact match on the inventory pair, or on the source's own id, in `aliases.toml`. An Arena id is matched after its effort parenthesis is removed, so one alias covers every effort row of a model.
2. **Host row** (`host`). models.dev's `ollama-cloud` and `openrouter` providers list host ids beside a `canonical_model_id`. An ollama cloud row is looked up by its tag with the `:cloud` or `-cloud` suffix removed; an OpenRouter row or record by its id with `:free` or `:batch` removed. A host row with no `canonical_model_id` does not answer.
3. **Normalization** (`normalized`). The name and every canonical key are reduced to a token sequence by the same function, and the name must equal exactly one key's sequence. Zero or several matches do not answer.

**The token function**, in order:

1. Lowercase. Discard everything up to and including the first `/`. This removes a vendor (`z-ai/`), an uploader (`mlx-community/`) or nothing.
2. Split on spaces, `-`, `_`, `/`, `:`, `(` and `)`.
3. Drop any piece in the drop list: `cloud`, `free`, `batch`, `latest`, `it`, `mlx`, `4bit`, `6bit`, `8bit`, `nvfp4`, `fp4`, `fp8`, `fp16`, `bf16`.
4. Split each remaining piece where a letter is followed by a digit, and at every `.` between digits. A digit followed by letters stays one piece: `27b`, `8b`.

So `Qwen3.8-27B-4bit`, `qwen3.8:27b-mlx` and `alibaba/qwen3.8-27b` all give `qwen, 3, 8, 27b`, and `Claude Opus 5.5` and `anthropic/claude-opus-5-5` both give `claude, opus, 5, 5`. `qwen3-8b` gives `qwen, 3, 8b` and cannot match. On the 2026-10-12 snapshot the 446 keys give 446 distinct sequences. A canonical key that ends in `-latest` is a floating alias and is left out of the normalization index.

**Names matched by guesswork are visible.** A cloud inventory row whose host row exists without a `canonical_model_id`, and which then resolves by normalization, gets the note `host row has no canonical id; matched by name`. `run` prints every inventory row with `resolved_by = "normalized"` as a review list.

**Why an alias is still needed.** models.dev's `ollama-cloud` row for the bare tag `deepseek-v4-pro` has no canonical id and April dates, while ollama.com served the 0813 snapshot under that tag on 2026-10-12 (the `:cloud` and `:0813-cloud` tags shared a digest). Normalization joins it, uniquely and wrongly, to the April key `deepseek/deepseek-v4-pro`.

**Unmapped and unresolved.**

- An inventory row that no step answers goes to `[[unmapped]]` and gets no tier.
- A source record that no step answers is not a candidate. Records at or above their signal's standard floor are written to `[[unresolved]]` with the source, the source's id and the score. The rest are counted per source.
- With the rules above and the one alias shown below, 35 of the 46 inventory rows resolved on 2026-10-12 and none resolved wrongly. Six of Arena's 52 rows and 172 of Epoch's 274 did not resolve before the dot rule in step 4 was added; that rule recovers the Anthropic rows, and the remainder is calibration work.

**Records that share a key.** When several records in one source resolve to one key, whether effort rows, `:free` variants or dated and undated names, the best-scoring record is used. Its effort and its confidence interval come from that same record.

**Effort.** Arena usually puts reasoning effort in a trailing parenthesis. The parser removes a parenthesis whose content is in the effort vocabulary (`none`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max`, compared without regard to case) and records it. Other parentheses, such as `(0813)`, stay in the name. `max` outside a parenthesis is part of the name, which keeps `Qwen3.8 Max` a model.

**Local builds.** An inventory row with `location = "local"` resolves like any other row. Its entry is marked `basis = "inherited"`, because every public score describes a hosted serving of the model and never the local build. It inherits the votes, the tier and the tool-calling flag. Its served context is unknown, so the context gate is skipped.

**`aliases.toml`**

```toml
[[alias]]
canonical = "deepseek/deepseek-v4-pro-0813"
note = "ollama :cloud and :0813-cloud shared digest 6ed7420bc3a4 on 2026-10-12"
inventory = [{ provider_id = "ollama", model_name = "deepseek-v4-pro:cloud" }]
arena = ["DeepSeek V4 Pro (0813)"]
epoch = ["DeepSeek V4 Pro 0813"]

[[ignore]]
inventory = { provider_id = "ollama", model_name = "nomic-embed-text:latest" }
reason = "embedding model"

[[ignore]]
source = "epoch"
id = "Mistral Large"
reason = "floating name; no fixed model"
```

- **`[[alias]]`** source keys are `inventory`, `openrouter`, `arena` and `epoch`. Source ids are matched exactly as the source spells them, apart from Arena's effort parenthesis.
- **`[[ignore]]`** acknowledges a row that will never resolve. An ignored inventory row is written to `[[models]]` as `unrated` with `reason = "not_applicable"` and stays out of `[[unmapped]]`. An ignored source record stays out of `[[unresolved]]`. A non-empty `[[unmapped]]` or `[[unresolved]]` therefore always means work to do.
- **Validation before any fetch:** two entries that claim the same inventory pair or the same source id are an error, and so is an `[[ignore]]` without a reason.
- **Validation once models.dev is parsed:** an alias `canonical` that is not a models.dev key must list at least one source id, or it is an error.
- An alias that names a source id absent from this run's snapshot is a warning. Models leave leaderboards.

### Tiers

| Rank | Tier | Code | Intended work |
|---|---|---|---|
| 4 | `marathon` | `M` | Long autonomous sessions |
| 3 | `deep` | `D` | Planning, architecture, hard debugging |
| 2 | `standard` | `S` | Everyday coding |
| 1 | `quick` | `Q` | Lookups, renames, background jobs |
| 0 | `unrated` | `-` | See the reasons below |

The one-character code exists for the wt selector, which has almost no spare width at 80 columns.

An `unrated` entry always carries a `reason`:

| `reason` | Meaning |
|---|---|
| `no_votes` | No source has a record for the model |
| `below_floor` | The votes place it under the quick floor |
| `gate_tool_call` | No tool calling |
| `gate_context` | Context below the quick minimum |
| `not_applicable` | Listed under `[[ignore]]` |

### Assignment

The steps run in this order for every resolved key.

1. **Votes.** Each of the three signals casts a tier, or abstains when its source has no record for the model. A score below the signal's quick floor is a vote of `unrated`. No vote is higher than the signal's `max_vote`.
2. **Base tier.** The votes are sorted by rank, lowest first, and the base tier is the element at index `(n-1)//2`. With two votes it is the lower; with one it is that vote. With none the tier is `unrated` with reason `no_votes`, and the remaining steps are skipped.
3. **Marathon promotion.** A model whose base tier is `deep` becomes `marathon` when its Arena record's `score_ci_lower` is at or above the marathon floor. No other route to `marathon` exists. The result is the pre-gate tier.
4. **Sentinel check**, on the votes and the pre-gate tier.
5. **Tool-calling gate.** If the key has models.dev rows and none reports `tool_call = true`, the tier is `unrated` with reason `gate_tool_call`.
6. **Context gate.** A model whose context is below its tier's minimum drops to the highest tier whose minimum it meets, or to `unrated` with reason `gate_context`. A lowered entry records `gate_context` in its notes.
7. **Final tier.**

| Signal | Source | `max_vote` |
|---|---|---|
| `intelligence_index` | OpenRouter | `deep` |
| `arena_agent` | Arena | `deep` |
| `eci` | Epoch | `deep` |

**Context used by the gate.**

| Entry | Context |
|---|---|
| Cloud inventory row | `limit.context` of the models.dev host row found by the step-2 lookup, whether or not that row has a canonical id |
| Cloud inventory row with no host row, and every candidate | The largest `limit.context` among the key's models.dev rows, with a note |
| Local inventory row | Unknown; the gate is skipped, with a note |
| A key with no models.dev rows (alias-declared) | Unknown; both gates are skipped, with a note |

Two inventory rows for one key can end in different tiers because their hosts serve different context limits. That is intended.

**Confidence** is computed from the votes alone and is not changed by promotion or gates.

| Value | Condition |
|---|---|
| `high` | Two or more votes, all equal |
| `medium` | Two or more votes whose ranks span one |
| `low` | One vote, or votes whose ranks span two or more |
| `none` | No votes |

**Evidence only.** `coding_index` and `agentic_index` are written to the entry's scores and shown by `explain`. They do not vote.

### Starting floors

Set from the 2026-10-12 snapshots, at gaps in each distribution. They live in `contracts.toml` and are data, not code.

| Signal | Quick floor | Standard floor | Deep floor | Marathon floor |
|---|---|---|---|---|
| `intelligence_index` | 10.6 | 33.0 | 49.0 | none |
| `arena_agent` (`score`) | -0.090 | -0.029 | 0.056 | 0.097 on `score_ci_lower` |
| `eci` | 137.1 | 150.7 | 160.6 | none |

| Tier | Minimum context |
|---|---|
| `quick` | 32,000 |
| `standard` | 128,000 |
| `deep` | 200,000 |
| `marathon` | 400,000 |

A throwaway implementation of these rules, run over the 2026-10-12 downloads, gave:

- `marathon`: Claude Opus 5.5, Claude Fable 5.1, GPT-6 Astra.
- `deep`: Claude Sonnet 5.5, GPT-6.1 Sol, Claude Opus 5, Claude Fable 5.
- `standard`: GLM-5.3, GLM-5.3-Flash, GLM-5.2, Kimi K3, DeepSeek V4.1 Flash, DeepSeek V4 Pro 0813, Qwen3.8 27B.
- `quick`: MiniMax M3, Gemma 4 31B, gpt-oss-120b, Qwen3.6 27B.
- About 190 keys had at least one vote. Neither gate changed any model named above; the tool-calling gate moved two candidates from `quick` to `unrated`.

**Known weaknesses, accepted.**

- **The low floors are arbitrary.** The intelligence index has no gap near 10.6 and the Epoch index has none near 137.1 or 150.7. Epoch's intervals (half-width 2.4 in the top 40) are wider than its gaps. `gpt-oss:20b` at 10.0 is `unrated` while `gpt-oss:120b` at 11.6 is `quick`.
- **Sources often differ by one tier.** On the 2026-10-12 data no model's source votes differed by two tiers. The floors were fitted on that same data, so this is optimistic.
- **Arena's score is relative to its pool.** Its floors drift when the pool changes. The sentinels pin Arena votes so the drift is caught.
- **A tier describes the best-scoring effort row,** not what a default-effort launch delivers. The effort is recorded so the reader can see it.
- **A two-vote model takes the lower vote.** Thin evidence cannot promote a model.

### Sentinels

`contracts.toml` pins these models to their votes and tier. All were unanimous across their sources on 2026-10-12.

| Canonical key | `intelligence_index` | `arena_agent` | `eci` | Tier |
|---|---|---|---|---|
| `anthropic/claude-fable-5-1` | deep | deep | deep | `marathon` |
| `anthropic/claude-opus-5-5` | deep | deep | deep | `marathon` |
| `anthropic/claude-fable-5` | deep | deep | deep | `deep` |
| `anthropic/claude-opus-5` | deep | deep | deep | `deep` |
| `anthropic/claude-sonnet-5` | standard | standard | standard | `standard` |
| `zhipuai/glm-5.2` | standard | standard | standard | `standard` |
| `minimax/MiniMax-M3` | quick | quick | quick | `quick` |
| `google/gemma-4-31b-it` | quick | none | quick | `quick` |

"none" means no vote is pinned for that signal; Gemma 4 has no Arena row. A sentinel fails when any pinned vote differs or is missing, or when its pre-gate tier differs. Pinning the votes matters: under the lower median, one source drifting cannot move a three-vote model's tier, so a check on the tier alone would miss it.

On failure the run prints every sentinel's scores, votes and pinned values, writes nothing and exits 3. There is no override. When a sentinel has genuinely moved, the owner edits `contracts.toml`, which is the re-calibration.

A sentinel whose `canonical` is not a known key is a configuration error (exit 1), not a sentinel failure.

### `contracts.toml`

```toml
version = "2026-10-12"

[tiers.deep]
code = "D"
context_min = 200000

[signals.arena_agent]
source = "arena"
max_vote = "deep"
floors = { quick = -0.090, standard = -0.029, deep = 0.056 }
marathon_ci_lower = 0.097

[sources.arena]
attribution = "Arena (arena.ai), dataset lmarena-ai/leaderboard-dataset, CC BY 4.0"

[[sentinels]]
canonical = "anthropic/claude-fable-5-1"
tier = "marathon"
votes = { intelligence_index = "deep", arena_agent = "deep", eci = "deep" }
```

- The file has a `[tiers.*]` table for each of the four rated tiers, a `[signals.*]` table for each of the three signals and a `[sources.*]` table for each of the four sources.
- It is validated on load: floors rise with the tier, every sentinel names a known tier and known signals, and exactly one signal carries `marathon_ci_lower`.
- **Attribution strings.** One per source. OpenRouter's is the text its benchmark reference gives: "Source: Artificial Analysis (artificialanalysis.ai) via OpenRouter (openrouter.ai/rankings)." Epoch's is "Epoch AI, 'Capabilities & benchmarking', https://epoch.ai/benchmarks, CC BY 4.0". `run`, `show` and `explain` print the lines for the sources whose data they display.

### Output file

```toml
generated_at = "2026-10-12T16:00:00Z"
contracts_version = "2026-10-12"

[inventory]
registry = "/Users/owner/.config/local-ai/registry.toml"
providers = { ollama = "ok", omlx = "ok", mtplx = "partial" }

[sources.arena]
status = "ok"                       # ok | stale
fetched_at = "2026-10-12T15:58:00Z"
records = 52
unresolved = 1
attribution = "Arena (arena.ai), dataset lmarena-ai/leaderboard-dataset, CC BY 4.0"

[[models]]
provider_id = "ollama"
model_name = "glm-5.3:cloud"
registered = true
canonical = "zhipuai/glm-5.3"
resolved_by = "host"                # alias | host | normalized
tier = "standard"
code = "S"
confidence = "high"
basis = "measured"                  # measured | inherited
tool_call = true
context = 1048576
votes = { intelligence_index = "standard", arena_agent = "standard", eci = "standard" }
efforts = { arena_agent = "max" }
notes = []

[models.scores]
intelligence_index = 44.8
coding_index = 74.8
agentic_index = 53.1
arena_agent = 0.0215
arena_agent_ci_lower = 0.0143
eci = 155.61

[[candidates]]
canonical = "xiaomi/mimo-v2.6-pro"
resolved_by = "host"
tier = "standard"
code = "S"
confidence = "high"
open_weights = true
tool_call = true
context = 1048576
votes = { intelligence_index = "standard", arena_agent = "standard" }
efforts = {}
notes = []

[candidates.scores]
intelligence_index = 46.3
arena_agent = 0.0352
arena_agent_ci_lower = 0.0161

[[unmapped]]
provider_id = "ollama"
model_name = "medgemma:27b"

[[unresolved]]
source = "arena"
id = "Gemini 4 Argon"
score = 0.0926
```

| Table | Contents | Order |
|---|---|---|
| `[inventory]` | The registry path and provider probe statuses wt reported | — |
| `[sources.*]` | One table each for `modelsdev`, `openrouter`, `arena` and `epoch` | — |
| `[[models]]` | One entry per resolved or ignored inventory row | As wt lists them |
| `[[candidates]]` | One entry per key with at least one vote and no inventory row | Rank descending, then key |
| `[[unmapped]]` | Inventory rows no step resolved | As wt lists them |
| `[[unresolved]]` | Unresolved source records at or above their signal's standard floor | Source, then score descending |

- `[[models]]` plus `[[unmapped]]` equals the inventory without its native rows.
- Optional fields: `reason` (present when the tier is `unrated`), `context` (absent when unknown), `efforts` entries (present only for signals whose record named an effort), and any score the source did not provide. `tool_call` is absent for a key with no models.dev rows.
- `[[candidates]]` entries have the fields of `[[models]]` without `provider_id`, `model_name`, `registered` and `basis`, plus `open_weights`.
- A status of `unavailable` never appears, because such a run writes nothing.
- **Contract for readers.** A model absent from the file, or in `[[unmapped]]`, is `unrated`. The file is advisory; a reader that cannot parse it behaves as if it were absent.
- A sample lands in `docs/contracts/tiers.sample.toml` and the output tests compare against it.

## Error handling

| Situation | Result |
|---|---|
| `aliases.toml` or `contracts.toml` is invalid | Message names the entry. Nothing written. Exit 1. |
| `wt` is not on PATH, or `wt model list --json` fails, and no `--inventory` is given | Message. Nothing written. Exit 1. |
| The inventory's registry is not beside the output file and `MODELTIERS_OUT` is unset | Message. Nothing written. Exit 1. |
| A source fails to download, fails to parse or is under its row minimum, and has a good snapshot | That snapshot is used. Source marked `stale`. Exit 2. |
| A source fails and has no good snapshot | Message names the source. Nothing written. Exit 1. |
| A sentinel fails | Sentinel table printed. Nothing written. Exit 3. |
| `--offline` and a source has no good snapshot | As for a source with no good snapshot. |
| The output file is missing when `show` or `explain` runs | Message naming `modeltiers run`. Exit 1. |

| Exit code | Meaning |
|---|---|
| 0 | Written. Every source `ok`, or `stale` because `--offline` was given. |
| 1 | Not written: configuration, inventory or source error |
| 2 | Written, with a source that fell back to a snapshot |
| 3 | Not written: blocked by sentinels |

When several apply, 1 takes precedence over 3 and 3 over 2. `--dry-run` writes no output file and returns the code the run would have returned.

## Testing

- **Parsers.** One golden fixture per source under `modeltiers/tests/fixtures/`, trimmed from the 2026-10-12 downloads to the rows the tests name. Each parser also has a malformed fixture and a too-few-rows fixture, and both must raise `SourceParseError`. The Arena parser reads a two-page fixture to `num_rows_total` and treats an `error` body as a failed download.
- **Token function.** A table-driven test covers: `qwen3.8` never matching `qwen3-8b`; `Claude Opus 5.5 (High)` matching `anthropic/claude-opus-5-5`; `(Max)` removed from `GLM 5.3 (Max)` and kept in `Qwen3.8 Max`; `(0813)` kept; `z-ai/`, `zhipuai/` and `mlx-community/` prefixes discarded; `nvfp4` and `bf16` dropped before the letter-digit split; `it` dropped from both `Gemma 4 31B IT` and `google/gemma-4-31b-it`; a `-latest` key left out of the index.
- **Resolution.** `deepseek-v4-pro:cloud` reaches the 0813 key through its alias and the April key without it; `qwen3.8:27b-mlx` reaches the 27B key and is marked inherited; an ambiguous name stays unmapped; a host row without a canonical id falls through and carries the note; `:free` and `:batch` collapse to the base; two records for one key keep the better score with its own interval and effort.
- **Alias validation.** Duplicate inventory pair, duplicate source id, an unanchored canonical key, an `[[ignore]]` without a reason, and a stale source id as a warning. An ignored row is `unrated` with reason `not_applicable` and absent from `[[unmapped]]`.
- **Assignment.** Hand-built evidence covers each floor boundary, the lower median with one, two and three votes, `max_vote` capping, marathon promotion on `score_ci_lower` and its refusal when the base tier is `standard`, each `unrated` reason, both gates, each row of the context table, and each confidence value.
- **Sentinels.** A fixture with every intelligence index scaled by 0.8 changes a pinned vote of `anthropic/claude-fable-5` from deep to standard while its tier stays deep; the run writes nothing and exits 3. A sentinel that names an unknown key exits 1.
- **Snapshots.** A failed download falls back to the newest good snapshot, marks the source stale and exits 2. A snapshot that fails to parse is renamed `.bad` and is skipped by a later `--offline`. A source with no good snapshot exits 1. Pruning keeps four good and two bad per source.
- **End to end.** `run --inventory` over the fixtures with a fake `Fetcher` produces a file equal to `docs/contracts/tiers.sample.toml` apart from timestamps. Variants cover `--dry-run` (no file, same exit code), `--offline`, the redirected-registry refusal, and the change report against an existing file.
- **Fixture-local data.** Every test uses the `contracts.toml` and `aliases.toml` under `tests/fixtures/`. The committed data files are covered by one test that loads and validates them. Calibration can then change the committed values without changing a test.
- **No live calls.** An autouse fixture makes `urllib.request.urlopen` raise and points `MODELTIERS_OUT`, `MODELTIERS_CACHE`, `HOME`, `XDG_CONFIG_HOME` and `XDG_CACHE_HOME` at a temporary directory. `subprocess.run` is stubbed, so no test runs wt.
- **Quality bar.** ruff, mypy and a coverage floor of 90, as in llmbench.
- **Live**, by hand: `modeltiers run --dry-run` against the real sources and the real inventory. Aliases and ignores are added until `[[unmapped]]` and `[[unresolved]]` are empty and every row in the review list of name-matched models has been checked. The sentinel table and the "run over the 2026-10-12 downloads" list above are confirmed or corrected against that run.

## Docs

- `modeltiers/CLAUDE.md`: commands, pipeline, the two data files, the test seams.
- `modeltiers/tests/fixtures/README.md`: credits each source with its licence and states that the files are trimmed copies.
- Root `CLAUDE.md`: a Commands line, an Architecture bullet, the package-level context list, and a `tiers.toml` note under the config paths.
- `docs/guides/12-model-tiers.md`: what a tier means, how to run and read it, how to add an alias or an ignore, how to re-calibrate after a sentinel failure. Sample output is labelled illustrative. No live model state is embedded.
- `docs/guides/00-config-map.md`: `tiers.toml` and the snapshot directory, each with its owner.
- `docs/contracts/tiers.sample.toml`: the output contract.
- Root `Makefile`: `install` and `test-all` call the package's own `make install`, `make check` and `make test`, as they do for llmbench.
- `.github/workflows/modeltiers-ci.yml`: lint, type-check and tests, then `bin/check-config-dirs-untouched`. It triggers on changes under `modeltiers/`, to `docs/contracts/tiers.sample.toml`, to `bin/check-config-dirs-untouched` and to the workflow file.

## Risks

- **models.dev is the only identity source.** If it changes shape, nothing can be written until the parser is fixed; the last good snapshot keeps runs working meanwhile. Accepted.
- **The Hugging Face rows API is unreliable.** It returned a 502 and an "index is loading" error during design. Mitigated by one retry and the snapshot fallback.
- **Terms for automated fetching are unread.** The licences were checked (CC BY 4.0 for Arena and Epoch); the sites' terms of use for scheduled fetching were not. To be read before any scheduled run is set up. Manual weekly runs are four requests each.
- **`.cache` is not covered by the CI guard.** `bin/check-config-dirs-untouched` checks config directories only. The autouse fixture is the only protection for the real cache.
- **Normalization can join wrongly without failing.** Mitigated by `resolved_by`, the review list and the note on rows that passed a host row with no canonical id. Not eliminated.

## Delivery

1. **Skeleton, inventory, snapshots and parsers.** The package, its CI and Makefile wiring, the inventory reader, the snapshot store with fallback, and the four parsers with fixtures. Complete by itself: `modeltiers run --dry-run` prints each source's status and parsed record count.
2. **Identity.** The token function, resolution, alias and ignore loading and validation, the unmapped, unresolved and review lists. Complete by itself: `run --dry-run` prints how every inventory row resolved.
3. **Assignment, output and commands.** Votes, promotion, sentinels, gates, confidence, `tiers.toml`, the change report, `show`, `explain`, exit codes, the contract sample. The committed `contracts.toml` carries the starting floors and sentinels above. Complete by itself: `modeltiers run` writes a file, with whatever the uncalibrated aliases leave unmapped.
4. **Calibration and docs.** The live run, the aliases and ignores it calls for, any floor or sentinel corrections, the guide and the `CLAUDE.md` updates. Complete by itself: `[[unmapped]]` and `[[unresolved]]` are empty on the owner's machine.
