# Model families — grouping, display names, tags, and wt rotation

> Use this to: set the `family` and `tags` of a model, and a family's display name, by hand in `registry.toml` (modelman's TUI, which used to edit families, is disabled), and understand how `family` and `tags` decide what the `wt` picker offers and which model it lands on next.
>
> Verified against: modelman 0.1.0, wt 0.1.0, LiteLLM 1.98.0, Ollama 0.33.2 on 2026-08-29

## Prerequisites

- [02-providers-and-models](02-providers-and-models.md) complete.
- ≥2 models in the registry:

```bash
grep -c '^\[\[models\]\]' ~/.config/local-ai/registry.toml
```

It prints the number of `[[models]]` entries in your registry; anything ≥2 is enough to follow along.

- modelman runnable from its repo, for the read-only check in Verification (not installed globally — see [02-providers-and-models](02-providers-and-models.md) Gotchas for why it must be run from the `modelman/` directory):

```bash
# from: ~/github/ohanaverse/local-ai-setup/modelman
uv sync
```

- A `wt` on PATH built before the 2026-08-28 registry-consumer merge serves a stale catalog — see Gotchas (stale-binary item); rebuild with `make install` if in doubt.

## TL;DR

| Knob | Lives in | Who reads it | How to change it |
|---|---|---|---|
| `family` per model | `~/.config/local-ai/registry.toml` (canonical) | `wt` (`-F` family filter) | Hand-edit the `family = "…"` line of the model's `[[models]]` block |
| display name per family | `~/.config/local-ai/registry.toml` `[[families]]` (canonical) | modelman's TUI only, which is disabled — **never read by `wt`**, so nothing shows it today | Hand-edit the `[[families]]` entry (Step 2) |
| `tags` per model (rotation groups, e.g. `code`/`design`) | `~/.config/local-ai/registry.toml` | `wt` (`-T` filter, tag rotation) | Hand-edit the `tags = […]` line of the model's `[[models]]` block (Step 3) |

```bash
"${EDITOR:-vi}" ~/.config/local-ai/registry.toml   # all three knobs are hand edits — the procedure is 02-providers-and-models Step 1
wt litellm sync --dry-run                          # read-only: `config error: parse …` if the file is no longer valid TOML
```

```bash
# from: ~
wt -F <family>[,<family>…]    # filter picker to families (OR within flag)
wt -T <tag>[,<tag>…]          # filter picker to tagged models (OR within flag)
```

`wt`'s TUI `d` toggle between code/design groups is documented in the wt README but has no key handler in the shipped wt 0.1.0 build (see Gotchas). Tags are empty (`tags = []`) on every row modelman's TUI wrote — see Step 3 to check yours.

## Steps

### 1. The family concept

A family groups variants of the same base model across providers (the data-model spec: all `gemma4` variants share `family = "gemma4"`; a model with an empty family "is treated as its own unique family").

```bash
grep -n -A3 '\[families' ~/.config/local-ai/modelman.toml
grep '^family = ' ~/.config/local-ai/registry.toml | sort -u
```

The first command shows the line number of the legacy `[families]` table (if present); the second lists each distinct `family = "…"` value once. Example (illustrative — your ids will differ):

```text
115:[families]
family = "ornith-1.5:35b"
family = "ornith-1.5:9b"
family = "qwen3.8:27b-mlx"
```

Compare the number of distinct family values with the model count from Prerequisites: if they are equal, every model is its own family (in the example above, `ornith-1.5:35b` and `ornith-1.5:9b` would be two families, not one).

### 2. Display names in `registry.toml` `[[families]]`

Display names were shown by modelman's TUI only — `wt` never reads them, and with that TUI disabled nothing displays them today. They are still kept: they live in `registry.toml`'s first-class `[[families]]` entries (the legacy `modelman.toml` `[families]` table is still loaded as a read-side fallback, and nothing writes it). Shape (from the modelman README):

```toml
[[families]]
name = "ornith"
display_name = "Ornith"   # optional; omitted when unset
```

Set or rename one by hand — add the entry, or change its `display_name` — following the hand-edit procedure of [02-providers-and-models](02-providers-and-models.md) Step 1. `name` is the family id, the same string the models' `family = "…"` lines carry; a family needs no `[[families]]` entry to exist, only a model that names it. To move a model to another family, edit its `family` line; a family is gone once no model names it, and `uv run modelman delete-family <name>` (from the `modelman/` directory) removes an empty family's leftover `[[families]]` entry — or delete the entry by hand.

A display name changes nothing in the `wt` picker, which groups and filters by the family id.

### 3. Tag groups (`code`/`design`) and how `wt` consumes them

Each model carries `tags` (list) in `registry.toml`. The wt data-model spec uses `code` and `design` as the canonical examples; *any* tag is a valid rotation group. Count the models that still have empty tags:

```bash
grep -c '^tags = \[\]' ~/.config/local-ai/registry.toml
```

If this equals the `[[models]]` count from Prerequisites, no model is tagged yet and tag filters/rotation have nothing to match.

Tags are assigned by hand, and always were — modelman's TUI never had a tag editor. To make `wt -T`/tag rotation meaningful, edit the model's `tags` line in `~/.config/local-ai/registry.toml` ([02-providers-and-models](02-providers-and-models.md) Step 1), e.g. `tags = ["code"]` or `tags = ["code", "design"]`. A modelman subcommand that rewrites the file keeps them. What wt does with them:

- `-T/--tags code,design` — model must have at least one matching tag (OR within the flag);
- `-F/--family` — model's `family` must equal one of the listed families (OR within the flag);
- both set → AND;
- neither set → **no filter** (the full agent-eligible catalog is listed);
- a discovered local model (on disk, no registry entry) has no family or tags, so any `-T`/`-F` hides it; with no filter every on-disk model of a provider the agent supports is listed;
- filter empties the list → picker status: `no models for agent "…" in tag "…" — edit your config`.

(Verified in wt source: `internal/config/config.go`, `EligibleModels`; the discovered-model rule is `internal/catalog/catalog.go` `Build`, `Input.HideDiscovered`.)

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

Model-picker screen keys (footer, current source): `[↑/↓] navigate   [enter] launch or start   [q] quit` — a non-running local row is *started* through the lifecycle engine, and the rotation advances on the launch that follows (see [06-wt-agents-and-models](06-wt-agents-and-models.md) §2). The header shows `agent : <agent>` / `tag   : <slot-tag>`, where the slot tag is the first `-T` value or `default_tag` from `~/.config/agent-wt/config.toml` — it labels the rotation slot, not necessarily an active filter (with no `-T`, `default_tag = "code"` is displayed while *all* agent-eligible models are listed). The picker cursor starts on the model **after** the last-launched one and `enter` launches + advances the rotation; there is no `r` re-roll key.

### 5. Rotation behavior

Rotation is implicit per launch. State lives in a single global file — one line, the last-launched model id. Example (illustrative — your ids will differ):

```text
# from: ~/.config/agent-wt/rotation.state
ollama/glm-5.3-flash:cloud
```

<!-- UNVERIFIED — naming claim for the legacy files is inferred from ls output + the migration source (internal/rotation/rotation.go reads any `rotation-*.state` glob); files of this shape were observed, but I did not reconstruct which tool wrote the `_` suffix. -->

Legacy per-slot files from the pre-global-rotation scheme (named like `rotation-<agent>-<tag>-_.state`, e.g. `rotation-claude-code-_.state`) may still sit in `~/.config/agent-wt/` but are **inert**: wt only reads/writes `rotation.state`. If `rotation.state` is ever missing, wt migrates once — takes the newest `rotation-*.state`, seeds the global file from its last line, then deletes the legacy files.

The debug helper prints the next model for a tag group (read-only; listed in `wt --help`):

```bash
wt rotate code
```

It prints one model id — the next model in the `code` rotation group. Example (illustrative — your ids will differ):

```text
ollama/kimi-k2.7-code:cloud
```

```bash
wt rotate kimi
```

```text
Error: no models tagged "kimi"
```

(A cobra usage block and a `wt: no models tagged "kimi"` stderr line follow in the real output; abbreviated here.)

Editing `family`/`tags` changes what `wt` offers on the **next launch** — the picker snapshot is rebuilt from the registry each run.

## Verification

Display-name round trip — Example (illustrative — your ids will differ; substitute one of your own family ids; historically live-verified 2026-08-29 with modelman.toml restored byte-exact afterwards):

```bash
# from: ~/github/ohanaverse/local-ai-setup/modelman
uv run python -c "from modelman.state import load_state; from pathlib import Path; s = load_state(Path.home() / '.config/local-ai/modelman.toml'); print(s.family_display_name('ornith-1.5:35b'))"
```

Suppose a temporary `[families."ornith-1.5:35b"]` → `display_name = "Ornith 1.5"` is appended to `~/.config/local-ai/modelman.toml`:

```text
Ornith 1.5
```

After restoring the file (no display name for that family), the fallback returns the raw id:

```text
ornith-1.5:35b
```

<!-- UNVERIFIED — interactive TUI. The wt picker regrouping after a family edit was not driven from this session. -->

Check what the picker's tag filter resolves to without launching anything:

```bash
wt rotate design && wt rotate code
```

Each command prints the next model id for that tag group, or errors with `no models tagged "…"` if no registry model carries the tag. A current wt reads tags only from `registry.toml`, so with every model at `tags = []` both commands error; if they instead print model ids while your registry has no tags, the `wt` on PATH is a stale pre-registry-consumer build (see Gotchas). Confirm the on-disk rotation cursor and any legacy files:

```bash
for f in ~/.config/agent-wt/rotation*.state; do echo "$f:"; cat "$f"; done
```

Each file's path followed by its single model-id line; `rotation.state` is the live cursor, any `rotation-*.state` files are inert legacy. Example (illustrative — your ids will differ):

```text
/Users/keith/.config/agent-wt/rotation-claude-code-_.state:
ollama/gemma4:9b

/Users/keith/.config/agent-wt/rotation.state:
ollama/glm-5.3-flash:cloud
```

## Gotchas

- **Tags and families drive agent rotation — editing them changes what `wt` offers next launch.** The picker cursor starts after `rotation.state`'s last-launched id; narrow the tag/family sets too far and you get `no models for agent "…" in tag "…" — edit your config`.
- **Display names are consumed by modelman's TUI only, which is disabled.** They never change what `wt` shows or rotates, and nothing displays them today.
- **Family/tag structure is canonical in `registry.toml`.** `wt` never edits a family or a tag there (its one write, `wt model init`, adds provider rows); change families/tags by hand-editing the file ([02-providers-and-models](02-providers-and-models.md) Step 1) — modelman's TUI is disabled, and its subcommands keep what you wrote when they rewrite the file.
- **`~/.config/local-ai/families/` is LEGACY** (per-family YAML manifests such as `ornith-1.5.yaml` — migration inputs only; legacy manifests did carry `display_name`, e.g. `Qwen 3.8`). Per [00-config-map](00-config-map.md): don't resurrect it.
- **TUI behavior:** there is no `d` tag-toggle key, `rotation.state` is a single global slot, and per-tag `rotation-<tag>.state` files are legacy migration inputs that are deleted after migration.
- **A `wt` binary built before the registry-consumer merge (2026-08-28) serves a stale catalog.** Symptom: `wt rotate code`/`design` return models although `registry.toml` has no tags — the old build still serves tagged models from `~/.config/agent-wt/config.toml` `[[models]]` blocks, so its catalog can be missing models the registry has (and list ones it doesn't). A rebuild from `~/github/ohanaverse/local-ai-setup/wt` (`go build -o /Users/keith/.local/bin/wt ./cmd/wt` — build over the PATH copy, not GOPATH, which `~/.local/bin` shadows; see [08-maintenance-and-troubleshooting](08-maintenance-and-troubleshooting.md) §4) makes `registry.toml` authoritative — expected to fail the `wt rotate code/design` pair-check above until tags exist.

## Going deeper

- wt README (flags, TUI keys, rotation, config split): `/Users/keith/github/ohanaverse/local-ai-setup/wt/README.md`
- wt registry-consumer design (fail-closed load, joined Config, tag/family filter semantics): `/Users/keith/github/ohanaverse/local-ai-setup/wt/docs/superpowers/specs/2026-08-28-wt-registry-consumer-design.md`
- Model registry data model (family/tags/agents, cascading filters): `/Users/keith/github/ohanaverse/local-ai-setup/wt/docs/superpowers/specs/2026-08-14-model-registry-data-model-design.md`
- modelman README (`modelman.toml` shapes; its TUI section describes the disabled screen): `/Users/keith/github/ohanaverse/local-ai-setup/modelman/README.md`
- Previous/next in this set: [02-providers-and-models](02-providers-and-models.md), [04-litellm-config](04-litellm-config.md)
