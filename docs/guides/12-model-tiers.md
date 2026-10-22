# Model tiers — `modeltiers`

> Use this to: see which of your models suit which kind of work. `modeltiers`
> reads the wt inventory and four public sources, gives every model an
> advisory capability tier with the evidence behind it, and writes
> `~/.config/local-ai/tiers.toml`, which wt's model picker shows as a TIER
> column. Nothing routes on a tier yet.
>
> Verified against: modeltiers 0.1.0 on 2026-10-12 (one live run against all four sources).

Design rationale: `docs/superpowers/specs/2026-10-12-model-tiers-design.md`.

## Prerequisites

- `wt` on PATH. `modeltiers` runs one wt command, `wt model list --json`, which only reads.
- `uv`, and the package installed: `make install` from the repo root, or `make -C modeltiers install`.
- Network access to `models.dev`, `openrouter.ai`, `datasets-server.huggingface.co` and `epoch.ai`. No API key is needed.
- For the evidence commands in steps 4 and 5: `curl`, `jq`, `shasum`, `unzip` and `python3`.

## TL;DR

```bash
# from: /Users/keith/github/ohanaverse/local-ai-setup
uv run --directory modeltiers modeltiers run --dry-run   # fetch, resolve, assign, report; no tiers.toml written
uv run --directory modeltiers modeltiers run             # the same, then write tiers.toml
uv run --directory modeltiers modeltiers show            # the tier table, from the file
uv run --directory modeltiers modeltiers show --html "$HOME/tiers.html"   # the same, with scores, to sort in a browser
uv run --directory modeltiers modeltiers explain <provider_id/model_name>
```

`modeltiers` never writes `registry.toml`, `config.yaml` or any wt state. It
writes `tiers.toml` and its own snapshot cache, and nothing else
([00-config-map](00-config-map.md)).

## What a tier means

A tier is a capability band, set from public benchmark scores. It is a label
to read. No launch, rotation or route looks at it.

| Code | Tier | Intended work |
|---|---|---|
| `M` | `marathon` | Long autonomous sessions |
| `D` | `deep` | Planning, architecture, hard debugging |
| `S` | `standard` | Everyday coding |
| `Q` | `quick` | Lookups, renames, background jobs |
| `-` | `unrated` | No tier; the entry says why |

- **Three signals vote.** The Artificial Analysis intelligence index (through OpenRouter), Arena's agent score and Epoch AI's capabilities index each vote a tier from a score floor, or abstain when they have no record of the model. The tier is the lower median of the votes: with two votes, the lower one.
- **`marathon` has one route.** A model whose votes give `deep` becomes `marathon` when the lower bound of its Arena interval clears the marathon floor.
- **Two gates can only lower a tier.** A model with no tool calling is `unrated`. A model whose context is under its tier's minimum drops to the highest tier it meets.
- **Confidence** says how far the votes agree: `high` (two or more, all equal), `medium` (one tier apart), `low` (one vote, or two or more tiers apart), `none` (no votes).
- **A local build inherits.** Every public score describes a hosted serving of a model. A local row carries the tier of the model it resolves to, marked `basis = "inherited"`: the quantized build on your machine was never measured.
- **`unrated` always has a reason:** `no_votes` (no source scores it), `below_floor` (the votes are under the quick floor), `gate_tool_call`, `gate_context`, or `not_applicable` (you listed it under `[[ignore]]`).
- **Candidates** are models a source scores that are not in your inventory. They are tiered the same way, which makes the candidate list the shortlist of models worth adding.

The floors, the context minimums and the pinned sentinel models are data, in
`modeltiers/src/modeltiers/data/contracts.toml`. Read the values there; this
guide does not repeat them.

## Steps

### 1. Run it

```bash
uv run --directory modeltiers modeltiers run --dry-run
```

`--dry-run` does everything except write `tiers.toml`, and returns the exit
code the real run would return. The one exception is a redirected registry
(see Gotchas): the real run is refused with exit 1, and the dry run reports
and warns. It still saves each download as a snapshot under
`~/.cache/local-ai/modeltiers/`. When the report is what you expect, run it
again without the flag.

The report, in order: the inventory line; one line per source; how every
inventory row was identified; the `unmapped:` list; the `review:` list of rows
matched by name alone; the `unresolved:` benchmark records; the sentinel
check; one `alignment:` line per signal fitted for the aligned score (step 2);
what changed since the last file (or the whole table when there is no
file yet); the attribution lines; `wrote <path>` or `dry run: <path> not
written`.

Illustrative example from the test fixtures (`modeltiers/tests/fixtures/`,
with the sample inventory `docs/contracts/model-list.sample.json`), not from
any machine: the first lines of the report, exactly as the tool
prints them for those files. Your models, counts and scores will differ.

```text
inventory: 8 models (1 native rows dropped), registry /home/owner/.config/local-ai/registry.toml
modelsdev: ok, 12 records, fetched 2026-10-12T15:58:00Z
openrouter: ok, 6 records, fetched 2026-10-12T15:58:00Z
arena: ok, 6 records, fetched 2026-10-12T15:58:00Z
epoch: ok, 6 records, fetched 2026-10-12T15:58:00Z
identity: 8 rows: 6 resolved, 1 ignored, 1 unmapped
  ollama/glm-5.3:cloud            host        zhipuai/glm-5.3
  ollama/deepseek-v4-pro:cloud    alias       deepseek/deepseek-v4-pro-0813
  openrouter/z-ai/glm-5.3         host        zhipuai/glm-5.3
  ollama/qwen3.8:27b-mlx          normalized  alibaba/qwen3.8-27b  [inherited]
  ollama/medgemma:27b             unmapped
  ollama/minimax-m3:cloud         normalized  minimax/MiniMax-M3  [host row has no canonical id; matched by name]
  ollama/nomic-embed-text:latest  ignored     embedding model
  ollama/gemma4:31b-mlx           normalized  google/gemma-4-31b-it  [inherited]
unmapped: 1 (each needs an [[alias]] or an [[ignore]] in aliases.toml, or is left open until evidence identifies it)
  ollama/medgemma:27b
review: 3 matched by name alone (check each)
  ollama/qwen3.8:27b-mlx -> alibaba/qwen3.8-27b
  ollama/minimax-m3:cloud -> minimax/MiniMax-M3
  ollama/gemma4:31b-mlx -> google/gemma-4-31b-it
unresolved: 1 at or above a standard floor (each needs an [[alias]] or an [[ignore]] in aliases.toml, or is left open until evidence identifies it)
  arena  Qwen3.8 Max  0.0235
  openrouter: 1 of 6 records unresolved, 0 ignored, 4 keys resolved
  arena: 1 of 6 records unresolved, 0 ignored, 5 keys resolved
  epoch: 0 of 6 records unresolved, 1 ignored, 5 keys resolved
sentinels: 4 of 4 hold
```

After a `changes:` line the same fixture run prints the table, which is also
what `show` prints (illustrative example from the test fixtures):

```text
T  tier      conf    model                           canonical
S  standard  high    ollama/glm-5.3:cloud            zhipuai/glm-5.3
S  standard  high    ollama/deepseek-v4-pro:cloud    deepseek/deepseek-v4-pro-0813
S  standard  high    openrouter/z-ai/glm-5.3         zhipuai/glm-5.3
S  standard  low     ollama/qwen3.8:27b-mlx          alibaba/qwen3.8-27b
Q  quick     high    ollama/minimax-m3:cloud         minimax/MiniMax-M3
-  unrated   none    ollama/nomic-embed-text:latest  -  (not_applicable)
-  unrated   none    ollama/gemma4:31b-mlx           google/gemma-4-31b-it  (no_votes)
unmapped: 1 (unrated until aliased or ignored)
  ollama/medgemma:27b
candidates: 2 (list them with --candidates)
```

| Exit | Meaning | What to do |
|---|---|---|
| 0 | Written (or would be, under `--dry-run`) | Read the lists; see step 3 |
| 1 | Not written: an invalid `aliases.toml` or `contracts.toml`, wt missing or failing, a redirected registry (a run without `--dry-run`; see Gotchas), or a source with no usable data and no good snapshot | The `modeltiers:` line on stderr names the entry, the file or the source |
| 2 | Written, with a source that fell back to its last good snapshot | The source's line says `stale`. Run again later; the tiers from that source are as old as its `fetched` time |
| 3 | Not written: a sentinel model's votes or tier moved | Step 5 |

Two more flags:

- `--offline` fetches nothing and reads each source's newest good snapshot. Every source is then `stale` and the exit code is still 0.
- `--inventory FILE` reads saved `wt model list --json` output and does not run wt.

### 2. Read the result

```bash
uv run --directory modeltiers modeltiers show                             # inventory models
uv run --directory modeltiers modeltiers show --candidates                # and the candidates
uv run --directory modeltiers modeltiers show --wide --sort eci           # more columns, ordered by one of them
uv run --directory modeltiers modeltiers show --html "$HOME/tiers.html"   # the whole table as a page for a browser
uv run --directory modeltiers modeltiers explain <provider>/<model>       # one row, by provider_id/model_name
uv run --directory modeltiers modeltiers explain <canonical key>          # every row of a canonical key, or the candidate
```

Both read `tiers.toml` and nothing else: no fetch, no wt. `show` starts with
the file's age and warns on stderr once it is more than 14 days old. Both end
with the attribution lines of the sources whose data they print.

`<provider>/<model>` is a row as `show` prints it in its `model` column;
`<canonical key>` is the value in its `canonical` column.

`show` has three ways to show more:

- **`--wide`** adds nine columns to the table: `open` (open weights: `T`,
  `F`, or `-` when models.dev has no row for the key or the row is ignored), `context`, the
  scores `intel`, `arena`, `eci`, `coding` and `agentic`, then `aligned` and
  `src` (below). A `-` is a value the file does not have. The table is then
  wider than most terminal windows: widen yours, or use the page.
- **`--sort COLUMN`** orders the rows by `tier`, `model`, `context`,
  `intelligence_index`, `arena_agent`, `eci`, `coding_index`,
  `agentic_index` or `aligned`. `model` is alphabetical; every other column puts the
  highest value first and the rows with no value last. In the terminal your
  models and the candidates are sorted separately; the page sorts them
  together. Without it the order is the file's.
- **`--html FILE`** writes one page and prints only `wrote <path>`. The page
  lists your models, the unmapped rows and every candidate together, with
  every `--wide` column, the canonical key and the reason an unrated row is
  unrated. Click a column heading to sort by it, and again to reverse; the
  filters narrow the rows by name, tier, and yours or candidates. `--sort`
  sets the order the page opens in. The file holds everything it needs and
  requests nothing, so it opens straight from disk (`open "$HOME/tiers.html"`).
  It is a copy of `tiers.toml` at that moment: write it again after a run.
  `FILE` cannot be `tiers.toml` itself.

**One ranking: the aligned score.** The three scores that vote are on
different scales, and each source scores a different set of models, so
neither the raw scores nor a rank within one source compares two models that
different sources scored. `aligned` is the one column that does:

- Each run fits a straight line from Arena's score to the intelligence index,
  and another from Epoch's, over the models that have both. `run` prints each
  fit: `alignment: <signal> on N shared models, typical miss M points`.
- A model's aligned score is the plain average of its own intelligence index
  and of each of its other voting scores put through its line. It is in
  intelligence-index points. A line's answer is never lower than the lowest
  index any model has in that run, nor higher than the highest.
- `src` is how many scores it rests on, from 1 to 3. `explain` prints the
  parts: `arena_agent <score> as <index points>`.

Read it with these cautions:

- **It is an estimate.** Two models closer than the typical miss the run
  printed are not told apart by it. The page says the figure under its table.
- **One source is thin.** A score with `src` 1 that is not the model's own
  index is a single source's score put through a line.
- **It decides nothing.** No tier, gate or sentinel reads it, and the lines
  are fitted again on every run, so a score can move a little when nothing
  about the model changed.
- **A local build has its hosted model's score.** No source scores your
  quantized copy. Like its tier, its aligned score is the one the model's key
  has, and `explain` says `basis inherited`.
- **Both ends of the scale are flat.** The weakest models all come out near
  the lowest index of the run, and a model whose line would put it above the
  highest index comes out at that index, so the strongest can tie: the lines
  are not read past either end.

A signal is left out of every aligned score when the run cannot fit it, and
`run` says so on stderr: `warning: alignment: <signal> is in no aligned
score: <why>`. The reasons: fewer than 20 models have both it and the
intelligence index; those models do not vary in one of the two; no finite
line fits them; or the line does not slope up, which means the source ranks
the shared models against the index. None of these needs an action from you
and none changes a tier. Models with their own index keep it as their score;
a model that source alone scored has none until a later run fits it.

Illustrative example from the test fixtures: what `explain` prints for one
fixture row, before its attribution lines.

```text
ollama/minimax-m3:cloud
  identity:    minimax/MiniMax-M3, resolved by normalized
  tier:        quick (Q)
  confidence:  high, basis measured
  votes:       arena_agent quick, eci quick
  scores:      arena_agent -0.0695, arena_agent_ci_lower -0.0786, eci 146.95
  aligned:     34.7 (arena_agent -0.0695 as 34.7; eci 146.95 as 34.8)
  efforts:     none
  tool_call:   true
  context:     512000
  open_weights: true
  note:        host row has no canonical id; matched by name
```

- `identity` is the canonical key (models.dev's spelling) and how the row reached it: `alias` (you said so in `aliases.toml`), `host` (models.dev lists the host's own id beside the key) or `normalized` (the name alone matched exactly one key).
- `efforts` names the reasoning effort of the Arena row that was used. A tier describes the best-scoring effort row, which may not be what a default launch gives you.
- `coding_index` and `agentic_index` appear under `scores` as evidence. They do not vote, and they are not part of `aligned`.
- `aligned` is the aligned score and what it is the average of: the index as it is, and each other score with the index points its line gives.

To read the file directly, its format is
[`docs/contracts/tiers.sample.toml`](../contracts/tiers.sample.toml). A model
absent from the file, or listed under `[[unmapped]]`, is `unrated`.

#### See tiers in the wt picker

wt's model picker reads the same file each time it builds its table and shows
it in a TIER column after MODEL: the tier's letter (`M` marathon, `D` deep,
`S` standard, `Q` quick) and the aligned score rounded to a whole number,
its digits right-aligned down the column. The
highlighted row's tier is spelt out on the line under the table, after the
price, when there is room. An illustrative picker, not a live one (and not
a sample the modeltiers tests pin):

```
    FAMILY   MODEL                     TIER  LOC    STATUS  RUNNING  COST
    glm-5.3  ollama/glm-5.3:cloud      S44   cloud  ok      -        ...
    qwen3.8  ollama/qwen3.8:27b-mlx    S34   local  ok      -        ...
    gpt-oss  ollama/gpt-oss:20b-cloud   12   cloud  ok      -        ...
    -        ollama/gemma4:31b-mlx     -     local  new     -        -

  LiteLLM: on   cost 0.6/0.06/2.2   tier standard · score 44 (3 sources) · high confidence
```

- An `unrated` model that still has an aligned score shows the score alone,
  with the letter's place left blank (the `gpt-oss` row above): a low score is a reason
  to pass a model over too. The line under the table reads
  `unrated · score 12 …` for it.
- `-` means no score at all: the entry is `unrated` with no aligned score, the model is not in the file
  (added since the last `modeltiers run`), or there is no file or it cannot
  be parsed. The picker never warns about any of these.
- wt finds a model's entry by `provider_id` and `model_name`, the fields
  `wt model list --json` gives modeltiers, so a model renamed in the registry
  reads `-` until the next run.
- A slot-profile row's TIER is `-`; the line under the table, which lists the
  profile's slots, puts each rated slot model's letter and score (`S44`)
  after its name.
- On a narrow terminal TIER is dropped after the usage columns and COST, and
  before LOC and FAMILY. When no model in the table has a tier or a score
  (no file yet), TIER is all dashes and is dropped first. The column is advisory: the score orders the
  picker's model rows (higher first; models with no score last), but no route depends on it.
- wt reads the file at `MODELTIERS_OUT` when it is set, exactly as
  `modeltiers run` writes it, so a scratch run (step 6) shows in a picker
  started with the same variable.

### 3. Decide what the report asks of you

| The report shows | It means | Do |
|---|---|---|
| `unmapped: N` with N above 0 | N inventory rows reached no canonical key and have no tier | Step 4, once per row |
| `unresolved: N at or above a standard floor` with N above 0 | N benchmark records that would vote `standard` or higher matched no key, so a model is missing a vote or a candidate is missing | Step 4, once per record |
| A row on the `review:` list you have not checked before | Its name alone picked the key. That can be wrong without failing | Check it with step 4's evidence commands. Right: nothing to do. Wrong: add an `[[alias]]` to the right key, or an `[[ignore]]` when there is none |
| `warning: alias for <key> names <source> id '<id>', which is not in this run's <source> data` | A model you aliased has left that leaderboard | Nothing, unless the id was only renamed: then add the new id to the same entry |
| `warning: provider <name> probe status is ...` | wt could not fully probe a provider, so its discovered models may be missing from this run | Fix the provider and run again, or accept the smaller inventory |
| A source line that says `stale` and exit 2 | That source's download or parse failed and its last good snapshot was used | Run again later. A file under the cache named `<stamp>.bad.<ext>` is the download that failed to parse |
| `sentinels: F of N FAILED` and exit 3 | A pinned model's score crossed a floor | Step 5 |

A run with `unmapped: 0`, `unresolved: 0`, every sentinel holding and exit 0
needs nothing from you. A row or a record that stays listed because step 4
found no evidence for it is not a fault: it is `unrated`, or a missing vote,
and the list says so on every run until someone finds the evidence.

### 4. Add an alias or an ignore

The file is `modeltiers/src/modeltiers/data/aliases.toml`. It is committed, so
a change is a commit.

```toml
# An inventory row, or a source's own id, that means this key.
[[alias]]
canonical = "<vendor/model as models.dev spells canonical_model_id>"
note = "<the evidence, and the date you read it>"
inventory = [{ provider_id = "<provider_id>", model_name = "<model_name>" }]
openrouter = ["<OpenRouter id>"]
arena = ["<Arena name without its effort parenthesis>"]
epoch = ["<Epoch display name>"]

# A row or a record that has nothing to resolve to (a finding; see (f)).
[[ignore]]
inventory = { provider_id = "<provider_id>", model_name = "<model_name>" }
reason = "<why, and what you checked>"

[[ignore]]
source = "<openrouter | arena | epoch>"
id = "<the source's own id>"
reason = "<why>"
```

An `[[alias]]` needs `canonical` and at least one of the four lists. The
`inventory` pair is exactly the `provider_id` and `model_name` that
`wt model list --json` prints; ids are exactly as the source spells them,
except that an Arena name is written without `(High)`, `(Max)` and the like.

**The rule: an alias needs evidence that identifies the model, and its `note`
says what the evidence is and the date you read it.** Evidence that identifies
is an id that the model's publisher, its distributor or the benchmark source
itself gives, and that leads to exactly one key. These identify nothing,
alone or together, because a fine-tune, a sibling and a successor share them:

- a name that looks like the key (name matching is what the tool already does, and its matches are on the `review:` list because a name can be wrong);
- a release date that equals the key's;
- an architecture, a layer count, a vocabulary size or a parameter count, from a registry config or a `config.json`.

When you find no identifying evidence, write nothing. The row stays under
`unmapped:` and the record under `unresolved:`, and the model is `unrated` or
short of a vote. That is the honest state. A guess is worse: it gives a model
another model's tier and nothing fails. Do not write an `[[ignore]]` to empty
a list either; an ignore is also a claim (see (f)).

Set these up once per shell. They read the snapshots the last run saved:

```bash
CACHE="${MODELTIERS_CACHE:-${XDG_CACHE_HOME:-$HOME/.cache}/local-ai/modeltiers}"
newest() { ls "$CACHE/$1"/*."$2" | grep -v '\.bad\.' | tail -1; }   # newest good snapshot: newest modelsdev json
ollama_manifest() { curl -fsS -H 'Accept: application/vnd.docker.distribution.manifest.v2+json' "https://registry.ollama.ai/v2/library/$1/manifests/$2"; }
ollama_digest() { ollama_manifest "$1" "$2" | shasum -a 256 | cut -c1-12 | grep -v '^e3b0c44298fc$'; }   # e3b0c44298fc is the digest of no bytes: a fetch that failed prints nothing
ollama_config() { curl -fsSL "https://registry.ollama.ai/v2/library/$1/blobs/$(ollama_manifest "$1" "$2" | jq -r .config.digest)"; }
# a Hugging Face repo's base_model and how the repo relates to it (the card's base_model_relation, else the relation in the tag Hugging Face derives)
hf_base() { curl -fsS "https://huggingface.co/api/models/$1" | jq -r '"\([.cardData.base_model // "(none)"] | flatten | join(" ")), relation \((.cardData.base_model_relation // ([.tags[]? | select(startswith("base_model:")) | split(":") | select(length == 3) | .[1]] | unique | join(" "))) | if . == "" then "(not stated)" else . end)"'; }
# every key whose id or display name matches a pattern
keys() { jq -r --arg re "$1" '[.[] | .models[]? | select(.canonical_model_id != null) | select((.canonical_model_id + " " + (.name // "")) | test($re; "i")) | .canonical_model_id] | unique | join(" ")' "$(newest modelsdev json)"; }
# the keys that models.dev rows with exactly this model id carry (a vendor prefix before the last "/" is not compared)
idkeys() { jq -r --arg id "$1" '[.[] | .models[]? | select((.id | ascii_downcase | split("/") | last) == ($id | ascii_downcase | split("/") | last)) | .canonical_model_id // "(no key)"] | group_by(.) | map("\(.[0]) x\(length)") | join(", ")' "$(newest modelsdev json)"; }
# the model ids Epoch ran under one display name, with the number of benchmark runs under each
epoch_ids() { unzip -p "$(newest epoch zip)" epoch_capabilities_index/processed_data_for_eci.csv | python3 -c 'import collections, csv, sys; runs = collections.Counter(row["model_version"] for row in csv.DictReader(sys.stdin) if row["Model"] == sys.argv[1]); print(", ".join(f"{version} x{count}" for version, count in sorted(runs.items())))' "$1"; }
```

Then pick the case. `<name>` and `<tag>` are the two halves of an ollama
model name, `<name>:<tag>`.

**(a) An ollama cloud tag.** The registry says which model the tag serves:

```bash
ollama_config <name> <tag> | jq -r .remote_model
```

The answer is a size-specific or dated tag: the distributor's own statement
of the model. Find its key with (d).

**(b) An ollama local tag with a short name** (a tag that leaves out part of
the model's name). Two tags of one library with one digest are one build, so
the short tag is the fully spelled one:

```bash
ollama_digest <name> <tag>
ollama_digest <name> <the fully spelled tag you think it equals>
```

Equal digests: find the fully spelled tag's key with (d). A command that
prints no digest could not fetch its tag (curl says why) and compares with
nothing. Different digests, or no fully spelled tag: read the library's own page
(`https://ollama.com/library/<name>`) for a statement of which model each tag
is. A `model_family` or a parameter count in the registry config is not such
a statement.

**(c) A Hugging Face repo** (an mtplx or omlx row named `<owner>/<repo>`):

```bash
hf_base <owner>/<repo>
```

It prints two things: the card's `base_model`, which is the publisher saying
what the repo was made from, and the relation between the two, which is the
card's `base_model_relation` or, when the card gives none, the relation in the
`base_model:<relation>:<repo>` tag Hugging Face derives. The relation is
`quantized`, `finetune`, `merge` or `adapter`.

**Only a quantization inherits the base model's key.** A quantized copy is the
same model in another encoding, so `relation quantized` sends you to (d) with
the `base_model` it printed. A fine-tune, a merge and an adapter are other
models that share a base: they do not take its key, however close the name.
With `relation (not stated)` the repo inherits only when one of the
publisher's own files says in so many words that it is a conversion of the
base (a quantization recipe, a "converted from" line); with no such statement
it does not.

When it prints `(none)`, look for the same two statements in the publisher's
other files: list them and read any manifest or runtime file for the repo it
was built from and for how it was built, then ask that repo. Every link of
the chain has to be a quantization or a stated conversion.

```bash
curl -fsS "https://huggingface.co/api/models/<owner>/<repo>" | jq -r '.siblings[].rfilename'
curl -fsS "https://huggingface.co/<owner>/<repo>/raw/main/<a manifest or runtime json>" | jq .
hf_base <the repo that file names>
```

If nothing in the repo names where it came from, or a link of the chain is a
fine-tune, a merge or an adapter, the base model's key is not this model's:
leave the row unmapped, or go to (f). The repo's name, and a `config.json`
that matches another model's, do not count.

**(d) Find the key in models.dev.** Start from the id the evidence gave you,
not from a name:

```bash
idkeys '<the id: an upstream repo, a dated tag, a source's model id>'
keys '<regex for the model's name>'
```

`idkeys` prints the keys that rows with that model id carry. One key (beside
`(no key)`, the hosts that give none): that is the `canonical`. `keys` then
shows whether another key could be meant; if one could, say in the note why
it is a different product, or go to (f). `idkeys` printing nothing, or only
`(no key)`: use `keys`, and accept its answer only when it prints exactly one
key for the full designation the evidence gave (name and size).

**(e) A benchmark record** from the `unresolved:` list. Ask the source which
model id it ran:

```bash
# Epoch: the versioned model ids behind a display name, then the key each belongs to
epoch_ids '<display name>'
idkeys '<a model id it printed, without its _high / _low effort suffix>'
# Arena: name, organization, score, lower bound (Arena publishes no model id)
jq -r '.pages[] | fromjson | .rows[].row | select(.model_name | test("<name>"))
  | [.model_name, .organization, .score, .score_ci_lower] | @tsv' "$(newest arena json)"
# OpenRouter: id, the upstream repo it names, and the intelligence index
jq -r '.data[] | select(.id | test("<id>"))
  | [.id, (.hugging_face_id // ""), (.benchmarks.artificial_analysis.intelligence_index // "no score")] | @tsv' "$(newest openrouter json)"
```

Alias the record when every id the source ran belongs to one key, and say in
the note what the ids were. When the source ran the name under ids of two
keys, or under an id no models.dev row has, the record does not identify one
key: go to (f).

Arena publishes no model id, so the label is the only identifier it gives and
an Arena record has its own test. Alias it in one case: the label itself
carries a designation (a snapshot date or a version) that exactly one key
carries in its own id, among every key `keys '<name>'` prints for that name.
The note names the designation and the keys it was told apart from, and cites
any id that agrees with it (another source's runs of the same designation).
This is not the release-date comparison the rule above excludes: the
designation is part of the name Arena gives the row and part of the key's id,
not a date looked up beside them. A label with no such designation, where two
or more keys share the name, identifies none of them: write nothing. With one
key of that name, name matching has already resolved the record and it needs
no alias. When no key could be the model, go to (f) and declare one.

**(f) Nothing fits.** Five outcomes, in this order:

- **The model is not in models.dev at all and no source scores it:** `[[ignore]]`, with a reason that says what you checked and ends `(checked <date>)`. This is a finding (there is nothing to resolve to), not a way to shorten the list. An embedding model or another model that takes no chat work is ignored the same way.
- **A source scores the model and no models.dev key or row could be it:** declare a key. Write an `[[alias]]` whose `canonical` is a new `vendor/model` and which lists the source id. The model becomes a candidate; both gates are skipped, with a note, because models.dev knows nothing about it. Replace the entry with models.dev's key once it has one.
- **A source scores the model, and models.dev has a row for the id the source ran but gives that row no key:** `[[ignore]]` with `source` and `id`. models.dev knows the model and has no key for it, so there is nothing to resolve to, and a declared key would stand beside whatever key the row gains later. The reason names the ids the source ran, says their row has no key, and ends `(checked <date>)`. Write it whenever the record's name is a bare family name that other keys also carry (dated or versioned ones, or a `-latest`): without the ignore, a later change to those keys could let name matching give the record's score to a different model. This is the one case in which a record with a score is ignored.
- **The evidence leaves two readings** (two keys the id could mean, a source that ran one name under two models, a repo that names no origin): write nothing. Leave the row or the record listed, and say in the commit or the PR what you found, so the next person starts from it.
- **The owner knows.** The person who installed or built the model may simply know what it is. That is evidence: write the alias with the note `owner-confirmed <date>` and what they confirmed.

Then check and commit:

```bash
uv run --directory modeltiers modeltiers run --dry-run   # the row has left unmapped: or unresolved:
make -C modeltiers test                                  # the committed file still loads, and every alias has a note
git add modeltiers/src/modeltiers/data/aliases.toml
git commit -m "chore(modeltiers): alias <model> to <key>"
```

`aliases.toml` is checked before wt is run and before any fetch. Two entries
that claim one inventory pair or one source id, an `[[ignore]]` without a
reason, and an unknown key in the file each stop the run with exit 1 and a
message that names the entry.

### 5. Re-calibrate after a sentinel failure

`contracts.toml` pins a handful of sentinel models to the votes and the tier
they had when the floors were set. When one differs, `run` prints every
sentinel, writes nothing and exits 3. The existing `tiers.toml` stays as it
was. There is no flag that overrides this: the fix is an edit to
`modeltiers/src/modeltiers/data/contracts.toml`.

Illustrative example from the test fixtures: the sentinel table of a fixture
run in which every intelligence index was lowered by a fifth.

```text
sentinels: 2 of 4 FAILED
  FAIL  anthropic/claude-opus-5-5
        scores:  intelligence_index 46.08, arena_agent 0.1433, arena_agent_ci_lower 0.1215, eci 167.33
        votes:   intelligence_index standard, arena_agent deep, eci deep; tier before the gates marathon
        pinned:  intelligence_index deep, arena_agent deep, eci deep; tier marathon
        differs: intelligence_index voted standard, pinned deep
  FAIL  anthropic/claude-fable-5
        scores:  intelligence_index 39.68, coding_index 76.5, agentic_index 50.7, arena_agent 0.0894, arena_agent_ci_lower 0.0767, eci 162.06
        votes:   intelligence_index standard, arena_agent deep, eci deep; tier before the gates deep
        pinned:  intelligence_index deep, arena_agent deep, eci deep; tier deep
        differs: intelligence_index voted standard, pinned deep
  ok    zhipuai/glm-5.3
        scores:  intelligence_index 35.84, coding_index 74.8, agentic_index 53.1, arena_agent 0.0215, arena_agent_ci_lower 0.0143, eci 155.61
        votes:   intelligence_index standard, arena_agent standard, eci standard; tier before the gates standard
        pinned:  intelligence_index standard, arena_agent standard, eci standard; tier standard
  ok    minimax/MiniMax-M3
        scores:  arena_agent -0.0695, arena_agent_ci_lower -0.0786, eci 146.95
        votes:   arena_agent quick, eci quick; tier before the gates quick
        pinned:  arena_agent quick, eci quick; tier quick
```

Read the `differs:` lines, then decide which of three things happened:

| What you see | What happened | The edit |
|---|---|---|
| One sentinel differs, on one signal | That model was re-scored by that source | Change that sentinel's pinned vote (and its `tier`, if the tier before the gates moved too) to what the run printed |
| Several sentinels differ on the same signal, all in the same direction | The source's scale moved. Arena's score is relative to its pool and drifts when the pool changes | Move that signal's floors, one floor at a time, with the procedure below |
| `differs: <signal> voted nothing, pinned <tier>` | The model left that source | Remove that signal from the sentinel's `votes`, or replace the sentinel with a model all three sources score |

**Moving a floor.** A floor is the lowest score that votes its tier. Do this
for each floor of the signal that a `differs:` line crosses (the floor between
the vote printed and the vote pinned). `marathon_ci_lower` is a floor on the
Arena lower bound and is moved the same way, as signal `arena_agent_ci_lower`.

`modeltiers ladder <signal>` prints a signal's resolved scores between two
bounds, highest first: the best record of every key, which is what votes. It
reads the newest snapshots of models.dev and of the signal's own source,
fetches nothing and does not run wt. Set up one helper beside it: `tierdump`
prints one line per inventory model and per candidate of a `tiers.toml`, with
its tier, sorted, so that two files can be compared with `diff`.

```bash
tierdump() { uv run --directory modeltiers python -c 'import sys, tomllib
doc = tomllib.load(open(sys.argv[1], "rb"))
for model in doc.get("models", []):
    print("model", model["provider_id"] + "/" + model["model_name"], model["tier"])
for row in doc.get("unmapped", []):
    print("model", row["provider_id"] + "/" + row["model_name"], "unmapped")
for candidate in doc.get("candidates", []):
    print("candidate", candidate["canonical"], candidate["tier"])' "$1" | sort; }
```

1. **Find the window the sentinels allow.** From the `scores:` lines of the failed report, take the signal's new score for every sentinel that pins a vote on it. `<low>` is the highest score among the sentinels pinned *below* the floor's tier; `<high>` is the lowest score among those pinned *at or above* it. Any floor above `<low>` and at or under `<high>` restores every pinned vote across this floor. When no sentinel is pinned on one side, leave that side's flag out in the next step. When `<low>` is not under `<high>`, the sentinels changed order: this is not a scale move, so re-pin the votes that differ instead (the first row of the table).
2. **List the resolved scores in the window, with the gaps between them.**

   ```bash
   uv run --directory modeltiers modeltiers ladder <signal> --low <low> --high <high>
   ```

   The first two lines name the snapshots that were read; their `fetched` times are the ones the failed run printed for those sources, unless something was downloaded since. Each line after them is a score, the gap to the next score down, and that gap's midpoint. The first is the sentinel at `<high>` and the last is the one at `<low>`. The report rounds scores to four places, so pass `<low>` a little lower and `<high>` a little higher than printed. A window with no resolved score in it is exit 1 with a message.
3. **Choose the midpoint of the widest gap.** Two gaps of one width: take the one nearer the old floor. Round the midpoint to the precision the file already uses for that signal, and check that the rounded value is still above the lower score of its gap and not above the upper one. Write it as the floor.
4. **Accept the edit only when all of these hold,** on the same downloads. The run that failed wrote nothing, so the table to compare with cannot come from it: the baseline is the `tiers.toml` the last passing run wrote. Write the new rule's table to a scratch file and compare the two, key by key, inventory models and candidates alike (the change report names only the inventory models that moved and gives the candidates as a count):

   ```bash
   BEFORE="${MODELTIERS_OUT:-${XDG_CONFIG_HOME:-$HOME/.config}/local-ai/tiers.toml}"   # the file the last passing run wrote
   AFTER="$(mktemp -d)/tiers.toml"
   MODELTIERS_OUT="$AFTER" uv run --directory modeltiers modeltiers run --offline; echo "exit $?"   # the same downloads, the new rule, a scratch file
   diff <(tierdump "$BEFORE") <(tierdump "$AFTER")
   ```

   - the run prints `sentinels: N of N hold` and `exit 0` (so every pinned vote is back, on every signal). `exit 3` means a sentinel still differs; nothing was written and there is nothing to compare yet;
   - it does not print `exit 1` with `contracts.toml:` on stderr (the floors still rise with the tier);
   - you have read every line `diff` prints. A `<` line is a key's tier in the last passing file and a `>` line is its tier under the new rule; a key with only one of the two joined or left. Each line has to be one you can account for: a model its source re-scored, a model that joined or left a source or your inventory, or a model whose score lies next to the floor you moved. Many keys of one tier moving together means the floor is in the wrong gap. Keys leaving a tier whose floor you did not touch mean the scale moved under that floor too, without carrying a sentinel across it: move that floor the same way;
   - with no file at `$BEFORE` (nothing has passed on this machine yet) there is no baseline: read the whole of `tierdump "$AFTER"` instead;
   - `make -C modeltiers test` passes.

Change `version` at the top of `contracts.toml` to the day's date:
`tiers.toml` records it as `contracts_version`, so a reader can tell which
rule set made a file. Then `modeltiers run`, and commit `contracts.toml`.

The file is validated on load. Floors that do not rise with the tier, a
sentinel that names an unknown tier or signal, or `marathon_ci_lower` on a
number of signals other than one stops the run with exit 1. A sentinel whose
key is in neither models.dev nor `aliases.toml` is also exit 1, not a
sentinel failure.

### 6. Try something without touching the real file

Point both paths somewhere else. With `MODELTIERS_OUT` set, an inventory from
any registry is accepted:

```bash
SCRATCH="$(mktemp -d)"
MODELTIERS_OUT="$SCRATCH/tiers.toml" MODELTIERS_CACHE="$SCRATCH/cache" \
  uv run --directory modeltiers modeltiers run
MODELTIERS_OUT="$SCRATCH/tiers.toml" uv run --directory modeltiers modeltiers show
```

## Gotchas

- **Only the wt picker reads the file,** and only to show it (the TIER column and the line under the table). The score orders the picker's model rows, so it also sets the default row; no route depends on a tier, and a missing file just leaves every model unscored.
- **A tier is a capability band, not a price or a speed.** Cost is a column in the wt picker beside it. No keyless source gives measured speed.
- **Two rows of one model can differ.** The context gate uses the context the row's own host serves. Two hosts of one key can therefore end in different tiers.
- **A local row's context is unknown,** so the context gate is skipped for it and the entry says so in a note.
- **Low floors are rough.** Sources often disagree by one tier near a floor, which is what `medium` confidence shows. A two-vote model takes the lower vote, so thin evidence cannot promote a model.
- **A redirected registry stops the write.** wt's inventory follows `WT_REGISTRY`; the output path does not. When the registry wt read is not `registry.toml` beside the output file and `MODELTIERS_OUT` is unset, `run` exits 1 before any fetch, so a scratch inventory can never replace the real `tiers.toml`. `run --dry-run` writes nothing, so it goes on: it prints a `warning:` that the same run without the flag is refused, then its whole report, and its exit code is the one the report gives (0, 2 or 3) where the real run's is 1. This is the only case in which `--dry-run` does not return the real run's code. Set `MODELTIERS_OUT` (step 6).
- **`--dry-run` still writes snapshots.** It skips `tiers.toml` only. Use `MODELTIERS_CACHE` to keep a trial run out of the real cache.
- **`--directory modeltiers` moves the working directory.** A relative `--inventory`, `--html` or `MODELTIERS_OUT` path resolves from `modeltiers/`. Use absolute paths. `show --html` prints the full path it wrote.
- **An ignore is never re-examined.** `[[ignore]]` is checked before any other step, so an ignored row stays `unrated` after models.dev gains a key for it. When you re-calibrate, re-check each entry whose reason ends `(checked <date>)` with step 4 (d).
- **A row left listed is not forgotten.** A row under `unmapped:` or a record under `unresolved:` that step 4 found no evidence for is printed by every run. It needs nothing until evidence turns up; do not alias or ignore it to make the list empty.
- **An alias for a model you removed is silent.** An `inventory` pair that is no longer in the inventory is not reported. Delete the pair when you notice it; it does no harm.
- **Runs are manual.** The sources' terms for automated fetching have not been read. Read them before scheduling this.
- **Credit the sources** when you quote a score or a tier outside this machine. `show` and `explain` print the lines.

## Going deeper

- Design spec: `docs/superpowers/specs/2026-10-12-model-tiers-design.md`
- Module map, test seams and the data files: `modeltiers/CLAUDE.md` (rules) and `modeltiers/docs/internals.md` (detail)
- The output contract: [`docs/contracts/tiers.sample.toml`](../contracts/tiers.sample.toml)
- The inventory contract, a sample of what `wt model list --json` prints (rows recorded from one machine; the registry path in it is made up and `size_bytes` and `path` are nulled): [`docs/contracts/model-list.sample.json`](../contracts/model-list.sample.json)
- Who owns `tiers.toml` and the snapshot cache: [00-config-map](00-config-map.md)
- Source: `modeltiers/src/modeltiers/`
