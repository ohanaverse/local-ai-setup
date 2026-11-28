# Model families — grouping, display names, and tags

> Use this to: set the `family` and `tags` of a model with `wt-model model edit` or the Models tab of `wt-model model`, and a family's display name by hand in `registry.toml`, and understand how `family` and `tags` decide what the `wt` picker offers.
>
> Verified against: wt 0.1.0, LiteLLM 1.98.0, Ollama 0.33.2 on 2026-08-29 · revised 2026-10 (commands checked against `wt --help` and `llmbench --help`, not re-run live)

## Prerequisites

- [02-providers-and-models](02-providers-and-models.md) complete.
- ≥2 models in the registry:

```bash
grep -c '^\[\[models\]\]' ~/.config/local-ai/registry.toml
```

It prints the number of `[[models]]` entries in your registry; anything ≥2 is enough to follow along.

- A `wt` on PATH built before the 2026-08-28 registry-consumer merge serves a stale catalog — see Gotchas (stale-binary item); rebuild with `make install` if `wt --help` lacks a flag this guide names.

## TL;DR

| Knob | Lives in | Who reads it | How to change it |
|---|---|---|---|
| `family` per model | `~/.config/local-ai/registry.toml` (canonical) | `wt` (`-F` family filter) | `wt-model model edit <id> --family <name>`, or the Family field of the Models tab's form |
| display name per family | `~/.config/local-ai/registry.toml` `[[families]]` (canonical) | nothing: wt never reads it | Hand-edit the `[[families]]` entry (Step 2) |
| `tags` per model (tag groups, e.g. `code`/`design`) | `~/.config/local-ai/registry.toml` | `wt` (`-T` filter) | `wt-model model edit <id> --tags code,design`, or the Tags field of the Models tab's form (Step 3) |

```bash
wt-model model edit <id> --family <name> --tags code,design   # family and tags (or: `wt-model model`, then enter on the row)
"${EDITOR:-vi}" ~/.config/local-ai/registry.toml          # a [[families]] display name is the one hand edit here
wt-model litellm sync --dry-run                           # read-only: `config error: parse …` if the file is no longer valid TOML
```

```bash
# from: ~
wt -F <family>[,<family>…]    # filter picker to families (OR within flag)
wt -T <tag>[,<tag>…]          # filter picker to tagged models (OR within flag)
```

`wt`'s TUI `d` toggle between code/design groups is documented in the wt README but has no key handler in the shipped wt 0.1.0 build (see Gotchas). Tags are empty (`tags = []`) on many older rows — see Step 3 to check yours.

## Steps

### 1. The family concept

A family groups variants of the same base model across providers (the data-model spec: all `gemma4` variants share `family = "gemma4"`; a model with an empty family "is treated as its own unique family").

```bash
grep '^family = ' ~/.config/local-ai/registry.toml | sort -u
```

It lists each distinct `family = "…"` value once. Example (illustrative — your ids will differ):

```text
family = "ornith-1.5:35b"
family = "ornith-1.5:9b"
family = "qwen3.8:27b-mlx"
```

Compare the number of distinct family values with the model count from Prerequisites: if they are equal, every model is its own family (in the example above, `ornith-1.5:35b` and `ornith-1.5:9b` would be two families, not one).

### 2. Display names in `registry.toml` `[[families]]`

`registry.toml` may hold `[[families]]` entries with a `display_name`. Nothing reads them: wt shows families by their id. wt preserves the entries on every write. Set or rename one by hand (guide 02 Step 1, *A few things stay a hand edit*) if you keep them for your own reference.

### 3. Tag groups (`code`/`design`) and how `wt` consumes them

Each model carries `tags` (list) in `registry.toml`. The wt data-model spec uses `code` and `design` as the canonical examples; *any* tag is a valid filter group for `-T`. Count the models that still have empty tags:

```bash
grep -c '^tags = \[\]' ~/.config/local-ai/registry.toml
```

If this equals the `[[models]]` count from Prerequisites, no model is tagged yet and tag filters have nothing to match.

Tags are set with `wt-model model edit <id> --tags code` (or `--tags code,design`; `--tags ""` clears them), or in the Tags field of the Models tab's form ([02-providers-and-models](02-providers-and-models.md) Step 1). They are stored as the model's `tags` line in `~/.config/local-ai/registry.toml`, e.g. `tags = ["code"]`. What wt does with them:

- `-T/--tags code,design` — model must have at least one matching tag (OR within the flag);
- `-F/--family` — model's `family` must equal one of the listed families (OR within the flag);
- both set → AND;
- neither set → **no filter** (the full agent-eligible catalog is listed);
- a discovered local model (on disk, no registry entry) has no family or tags, so any `-T`/`-F` hides it; with no filter every on-disk model of a provider the agent supports is listed;
- filter empties the list → picker status: `no models for agent "…" in tag "…" — edit your config`.

(Verified in wt source: the discovered-model rule is `internal/localmodels/catalog.go` `CatalogRows`, `CatalogInput.HideDiscovered`.)

### 4. How the `wt` picker derives its options

`wt` picks worktree → agent → model from the joined catalog (registry-consumer design: `~/.config/local-ai/registry.toml` read for Providers/Models, joined in memory with `~/.config/agent-wt/config.toml` for Agents + `default_tag`; a missing/malformed registry fails closed). Local rows come from wt's live inventory, not from the registry: a local entry only adds family, tags and cost to a model wt finds on disk or running, and an entry whose artifact is missing is not offered (see [02-providers-and-models](02-providers-and-models.md) Step 3).

```bash
wt --help
```

```text
  -F, --family string          Comma-delimited model families to filter models (OR within flag)
  -M, --model string           Pin the model as <provider>/<name>
  -T, --tags string            Comma-delimited tags to filter models (OR within flag)
  -A, --agent string           Agent or command to launch (claude, codex, copilot, pi, agy, opencode, shell)
```

Model-picker screen keys (footer, current source): `[↑/↓] navigate   [enter] launch or start   [q] quit` — a non-running local row is *started* through the lifecycle engine. The header shows `agent : <agent>` / `tag   : <slot-tag>`, where the slot tag is the first `-T` value or `default_tag` from `~/.config/agent-wt/config.toml` — it labels the tag slot, not necessarily an active filter (with no `-T`, `default_tag = "code"` is displayed while *all* agent-eligible models are listed). The picker cursor starts on the `-M` pin, else on the row launched last (the `> ` marker's row) when it is shown and not blocked, else on the first row that is not blocked, in the picker's sort order ([06-wt-agents-and-models](06-wt-agents-and-models.md) §2); `enter` launches or starts; there is no `r` re-roll key.

Editing `family`/`tags` changes what `wt` offers on the **next launch** — the picker snapshot is rebuilt from the registry each run.

## Verification

Display-name check — read the `[[families]]` entries back from `registry.toml`, where Step 2 put the name:

```bash
grep -n -A2 '^\[\[families\]\]' ~/.config/local-ai/registry.toml
```

Each entry prints its `name` and, when one is set, its `display_name`. Example (illustrative — your ids and line numbers will differ):

```text
12:[[families]]
13-name = "ornith"
14-display_name = "Ornith"
```

No output means the registry has no `[[families]]` entry, which is valid — a family exists as soon as a model names it. That grep is the whole check: nothing displays the name today (Step 2), so there is no screen to confirm it on.

<!-- UNVERIFIED — interactive TUI. The wt picker regrouping after a family edit was not driven from this session. -->

Check which models a tag filter will list, without launching anything:

```bash
wt-model model list --json | python3 -c 'import json,sys; [print(m["id"]) for m in json.load(sys.stdin)["models"] if "code" in (m["tags"] or [])]'
```

It prints the id of every model tagged `code`, which is what `wt -T code` offers; no output means no registry model carries the tag. A current wt reads tags only from `registry.toml`: if `wt -T code` lists tagged models although the registry has none, the `wt` on PATH is a stale pre-registry-consumer build (see Gotchas).

## Gotchas

- **Tags and families drive what `wt` offers — editing them changes the next launch.** Narrow the tag/family sets too far and you get `no models for agent "…" in tag "…" — edit your config`. Under `-T` or `-F` slot-profile rows are hidden: a profile has no tags or family of its own.
- **Display names are read by nothing.** They never change what `wt` shows.
- **Family/tag structure is canonical in `registry.toml`.** Change a model's family and tags with `wt-model model edit` or the Models tab ([02-providers-and-models](02-providers-and-models.md) Step 1); a `[[families]]` display name is a hand edit, and nothing reads it today.
- **`~/.config/local-ai/families/` is LEGACY** (per-family YAML manifests such as `ornith-1.5.yaml` — migration inputs only; legacy manifests did carry `display_name`, e.g. `Qwen 3.8`). Per [00-config-map](00-config-map.md): don't resurrect it.
- **TUI behavior:** there is no `d` tag-toggle key. The `> ` marker in the launcher's picker is read from `last-picked.state` (one line, the row launched last).
- **A `wt` binary built before the registry-consumer merge (2026-08-28) serves a stale catalog.** Symptom: the picker lists tagged models (or `-T code` narrows the list) although `registry.toml` has no tags: the old build serves tagged models from `~/.config/agent-wt/config.toml` `[[models]]` blocks, so its catalog can be missing models the registry has (and list ones it doesn't). Rebuild with `make install` from `wt/` (it builds over the PATH copy at `~/.local/bin/wt`, not GOPATH, which `~/.local/bin` shadows; see [08-maintenance-and-troubleshooting](08-maintenance-and-troubleshooting.md) §4); a current build makes `registry.toml` authoritative.

## Going deeper

- wt README (flags, TUI keys, config split): `~/github/ohanaverse/local-ai-setup/wt/README.md`
- wt registry-consumer design (fail-closed load, joined Config, tag/family filter semantics): `~/github/ohanaverse/local-ai-setup/wt/docs/superpowers/specs/2026-08-28-wt-registry-consumer-design.md`
- Model registry data model (family/tags/agents, cascading filters): `~/github/ohanaverse/local-ai-setup/wt/docs/superpowers/specs/2026-08-14-model-registry-data-model-design.md`
- Previous/next in this set: [02-providers-and-models](02-providers-and-models.md), [04-litellm-config](04-litellm-config.md)
