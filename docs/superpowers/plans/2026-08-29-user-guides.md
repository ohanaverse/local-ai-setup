# User Guide Playbook Set Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Produce a nine-file task-oriented user guide set under `docs/guides/` in `local-ai-setup`, restructure `README.md` as a thin index, relocate stale docs, and point `CLAUDE.md` at the new playbooks.

**Architecture:** Docs-only change on branch `docs/user-guides`. Each guide is compiled from named source files, verified against installed tools during drafting (run `--help`, read real config, `ls` real paths), and committed atomically. Every guide follows the fixed seven-section skeleton from the spec.

**Tech Stack:** Markdown, git, modelman (Python/uv, in `~/github/ohanaverse/modelman`), agent-worktree (`wt`, Go, in `~/github/ohanaverse/agent-worktree`), LiteLLM proxy, Ollama, oMLX, llama.cpp, macOS LaunchAgents.

**Spec:** `docs/superpowers/specs/2026-08-29-user-guides-design.md`

**Repo root (all paths relative to it):** `/Users/keith/github/ohanaverse/local-ai-setup`

## Guide skeleton (binding for every guide file)

Every file in `docs/guides/` uses exactly these top-level sections, in order:

```markdown
# <Guide Title>

> Use this to: <one-line statement of the operation>
>
> Verified against: <modelman version>, <wt version>, LiteLLM <version> on <date>

## Prerequisites
## TL;DR
## Steps
## Verification
## Gotchas
## Going deeper
```

Conventions (from spec, mandatory):

- Absolute paths always (`~/.config/...`, `/Users/keith/github/ohanaverse/...`). Never "the file from step 2" without the path.
- Every command in a fenced block. Blocks that must run from a specific directory start with a comment like `# from: ~/github/ohanaverse/modelman`.
- Every non-trivial command is followed by *expected output* ("You should see …").
- Commands that could not be verified safely are marked with `<!-- UNVERIFIED -->` directly above the block.
- Discrepancies found between source docs and reality go into **Gotchas**, never silently corrected.
- Prose-only instructions are forbidden: anything to be done appears as a command, file path, or keypress.

## Verification tooling

**Link check** — run after any task that creates or moves files (also used in the final task). From repo root:

Run `make check-links` from repo root. Expected: `ALL LINKS OK`.

Expected: `ALL LINKS OK`. If broken links print, fix paths (URL-encode spaces as `%20` in markdown links) and rerun.

**Version stamp** — collect once in Task 0, reuse in every guide header:

```bash
wt --version
ollama --version
uv tool list | grep -i litellm
grep -m1 '^version' ~/github/ohanaverse/modelman/pyproject.toml
```

Expected: version strings for `wt`, ollama, litellm, modelman. Record exactly.

**Cold read** — after writing each guide, read its TL;DR block end-to-end as if executing cold on a machine where nothing local is running; every command must define its own directory and prerequisites, either inline or via a Prerequisites link to an earlier guide. Fix gaps before committing.

---

### Task 0: Preflight context and version stamp

**Files:**
- Read only. No files created.

- [ ] **Step 1: Read the spec**

Read `docs/superpowers/specs/2026-08-29-user-guides-design.md`. Confirm branch:

```bash
# from: /Users/keith/github/ohanaverse/local-ai-setup
git rev-parse --abbrev-ref HEAD
```

Expected: `docs/user-guides`. If not, `git switch docs/user-guides`.

- [ ] **Step 2: Gather source docs**

Read (skim OK, note anything that contradicts the spec):

- `README.md`, `CLAUDE.md` (this repo)
- `docs/reference/LiteLLM Proxy on macOS_ Unifying Ollama, llama_cpp, and OpenRouter.md`
- `docs/reference/Adding MLX (Apple Silicon) as a Fourth Backend to Your LiteLLM macOS Proxy.md`
- `docs/reference/oMLX Download and Run.md`
- `docs/reference/Downloading and Managing Hugging Face Models on macOS for Local LLM Inference (2026).md`
- `docs/litellm-admin-ui-setup.md`
- `docs/Local AI Setup 2026-08-25.md`
- `~/github/ohanaverse/modelman/README.md`
- `~/github/ohanaverse/agent-worktree/README.md` and `~/github/ohanaverse/agent-worktree/docs/configuration.md`
- `~/github/ohanaverse/modelman/docs/superpowers/specs/2026-09-05-modelman-benchmark-design.md`
- `~/github/ohanaverse/modelman/docs/superpowers/specs/2026-08-28-modelman-usage-design.md`
- `~/github/ohanaverse/agent-worktree/docs/superpowers/specs/2026-08-28-wt-registry-consumer-design.md`

If a listed file does not exist, note it and proceed — the guide that needed it records the gap in Gotchas.

- [ ] **Step 3: Probe the installed tools**

```bash
# from: /Users/keith/github/ohanaverse/local-ai-setup
wt --help
ls bin/
launchctl list | grep -iE 'llm|ollama|omlx|litellm'
ls ~/.config/local-ai/ ~/.config/agent-wt/ ~/.config/litellm/ 2>/dev/null
```

Expected: wt usage text; `bin/llm-isolate-provider`, `bin/llm-restore-providers` listed; LaunchAgent/job names for litellm, ollama, omlx (record exact job names); the three config directories listed (record exact filenames present — this feeds Task 1).

```bash
# from: ~/github/ohanaverse/modelman
uv run modelman --help
uv run modelman sync --help
uv run modelman benchmark --help
uv run modelman usage --help
```

Expected: help text for each. Record the real subcommand set and flags — every guide's TL;DR must use the flags that actually exist. If a subcommand is missing or has different flags than the spec's examples, reality wins: write the guide against actual behavior and add a Gotcha.

- [ ] **Step 4: Collect version stamp**

Run the **Version stamp** commands from Verification tooling. Save the outputs — every guide header in later tasks uses them.

- [ ] **Step 5: Commit (no-op check)**

Only if files were created (they shouldn't be):

```bash
# from: /Users/keith/github/ohanaverse/local-ai-setup
git status --short
```

Expected: empty (clean tree).

---

### Task 1: `docs/guides/00-config-map.md`

**Files:**
- Create: `docs/guides/00-config-map.md`

- [ ] **Step 1: Inspect every config file**

```bash
# from: /Users/keith/github/ohanaverse/local-ai-setup
ls -la ~/.config/local-ai/ ~/.config/agent-wt/ ~/.config/litellm/
head -40 ~/.config/local-ai/registry.toml
head -40 ~/.config/local-ai/modelman.toml
head -20 ~/.config/litellm/config.yaml
head -30 ~/.config/agent-wt/config.toml 2>/dev/null || echo "no config.toml"
ls ~/Library/LaunchAgents/ | grep -iE 'llm|ollama|omlx|litellm|redis|postgres'
```

Expected: real files with real content. Record each file's actual top-level sections.

- [ ] **Step 2: Write the config map**

Guide structure with this content:

- **Prerequisites:** none (this is a reference doc, note that).
- **TL;DR:** the single table below (no commands needed).
- **Steps → replaced by "The files":** one subsection per file with: absolute path, owner (which tool writes it), consumers, short purpose, a minimal real excerpt (≤15 lines), env-var overrides (`MODELMAN_REGISTRY`, `MODELMAN_STATE`, `MODELMAN_SETTINGS` from modelman README).
- Required table (rows: registry.toml, modelman.toml, settings.yaml, litellm/config.yaml, litellm database/credentials if present, agent-wt/config.toml, agent-wt/themes.toml, agent-wt/usage.jsonl, agent-wt/rotation.state, LaunchAgent plists):

```markdown
| File | Owner (writes) | Readers | Purpose |
|---|---|---|---|
| `~/.config/local-ai/registry.toml` | `modelman` (TUI add/edit, `modelman migrate`) | `wt` (read-only), LiteLLM exposure | Canonical providers + models |
| `~/.config/local-ai/modelman.toml` | `modelman` | `modelman` | Per-machine state: downloads, LiteLLM exposure flags, family display names |
| `~/.config/local-ai/settings.yaml` | `modelman` | `modelman` | User preferences (theme) |
| `~/.config/litellm/config.yaml` | `modelman` (expose toggle), you by hand | LiteLLM proxy | `model_list`, router settings |
| `~/.config/agent-wt/config.toml` | `wt` | `wt` | Agents, default rotation tag (NO providers/models — those live in registry.toml) |
| `~/.config/agent-wt/usage.jsonl` | `wt` | `modelman usage` | Launch log |
| `~/.config/agent-wt/rotation.state` | `wt` | `wt`, `modelman usage` | Rotation position |
| `~/Library/LaunchAgents/*.plist` | you (setup guide 01) | launchd | litellm/ollama/llama.cpp/omlx/redis services |
```

(Fill the `~/Library/LaunchAgents/` row with the actual plist filenames recorded in Task 0/Step 1, row per service.)

- **Gotchas:** registry.toml is read-only to `wt` — never hand-edit it to change what wt sees; exposure lives in modelman.toml, the LiteLLM entries are generated; if a file listed in Task 0 doesn't exist yet, note when it's created (first run of the owning tool).
- **Going deeper:** links to `01-initial-setup.md`, `../reference/oMLX Download and Run.md`, modelman README, wt registry-consumer spec (exact paths from Task 0).

- [ ] **Step 3: Link check** — run link check (Verification tooling). Expected: `ALL LINKS OK`.

- [ ] **Step 4: Commit**

```bash
# from: /Users/keith/github/ohanaverse/local-ai-setup
git add docs/guides/00-config-map.md
git commit -m "docs(guides): add config file map"
```

---

### Task 2: `docs/guides/01-initial-setup.md` + archive the old setup doc

**Files:**
- Create: `docs/guides/01-initial-setup.md`
- Modify: none
- Move: `docs/Local AI Setup 2026-08-25.md` → `docs/archive/`

- [ ] **Step 1: Verify what a fresh install requires**

```bash
# from: /Users/keith/github/ohanaverse/local-ai-setup
uv tool list | grep -i litellm
ollama --version
brew list --versions 2>/dev/null | grep -E 'llama.cpp|redis|postgresql'
which omlx || ls ~/Library/LaunchAgents/ | grep -i omlx
launchctl list | grep -iE 'llm|ollama|omlx|litellm|redis|postgres'
```

Expected: installed versions for litellm/ollama; brew entries for llama.cpp, redis, postgresql (record real names); LaunchAgent job names.

- [ ] **Step 2: Write the guide**

Content requirements, condensed from `docs/Local AI Setup 2026-08-25.md` into verified steps:

- **Prerequisites:** macOS (Apple Silicon), Homebrew, `uv`, Go (for wt, see 06), OpenRouter API key, HuggingFace account if pulling HF models.
- **TL;DR:** the full install as one block, ordered: brew installs → `uv tool install 'litellm[proxy]'` → ollama pull → modelman install/first run → expose models → launchctl restart litellm → smoke `curl`.

```bash
# TL;DR block must contain, at minimum (flags verified in Step 1):
brew install llama.cpp redis postgresql@16   # adjust to actual installed versions
uv tool install 'litellm[proxy]'
ollama pull <smoke-model>                    # use a model already in the registry
cd ~/github/ohanaverse/modelman && uv sync
launchctl kickstart -k gui/$(id -u)/<litellm-job-label-from-launchctl-list>
curl -s http://localhost:4000/v1/models | head -20
```

- **Steps:** one section per backend — LiteLLM proxy, Ollama, llama.cpp, oMLX, OpenRouter/HF auth, PostgreSQL + Redis, LaunchAgents. Each: install command, verification command, expected output. Pull specifics (ports, plist paths, env vars, admin UI URL) from the old setup doc and reference article `docs/reference/LiteLLM Proxy on macOS_….md`.
- **Verification:** end-to-end smoke test — `curl http://localhost:4000/v1/models` lists an exposed model; `claude-wt -W smoke-test -M <model>` (or `wt --cwd`) actually streams a reply.
- **Gotchas:** oMLX serves both 4-bit and 6-bit variants — name the exact one; per-backend stop mechanics differ (from CLAUDE.md); Postgres creds the proxy needs live in the LiteLLM config, not in this repo.
- **Going deeper:** link `../archive/Local%20AI%20Setup%202026-08-25.md`, `../reference/Downloading and Managing Hugging Face Models on macOS for Local LLM Inference (2026).md`, `02-providers-and-models.md`.

- [ ] **Step 3: Archive the old doc**

```bash
# from: /Users/keith/github/ohanaverse/local-ai-setup
mkdir -p docs/archive
git mv "docs/Local AI Setup 2026-08-25.md" docs/archive/
```

- [ ] **Step 4: Link check + cold read** — expected `ALL LINKS OK`; fix any dangling refs to the moved file (only Task-1 files exist so far, so target: `docs/guides/01-initial-setup.md` itself must not link to `README.md` sections that still exist).

- [ ] **Step 5: Commit**

```bash
# from: /Users/keith/github/ohanaverse/local-ai-setup
git add docs/guides/01-initial-setup.md docs/archive/
git commit -m "docs(guides): add initial setup guide; archive dated setup doc"
```

---

### Task 3: `docs/guides/02-providers-and-models.md`

**Files:**
- Create: `docs/guides/02-providers-and-models.md`

- [ ] **Step 1: Verify modelman surface**

```bash
# from: ~/github/ohanaverse/modelman
uv run modelman --help
uv run modelman sync --help
```

Record subcommands/flags. Determine which operations are TUI-only vs CLI (from `--help` output + `~/github/ohanaverse/modelman/README.md`). The guide must state explicitly which operations are TUI-only.

- [ ] **Step 2: Write the guide**

- **Prerequisites:** `01-initial-setup.md` complete; modelman runnable (`cd ~/github/ohanaverse/modelman && uv sync`).
- **TL;DR:** launch TUI (`uv run modelman`), plus working CLI lines: `uv run modelman sync`, and any real non-interactive add/edit commands found in Step 1. If adds are TUI-only, TL;DR says so in one line.
- **Steps:**
  1. Launch the TUI; keybindings table from `~/github/ohanaverse/modelman/README.md` (verify keys against the actual TUI help screen if reachable non-interactively; else copy from README and do not invent).
  2. Add a cloud provider (OpenRouter): fields, auth `type = "api_key"`, `secret_ref`. Show the resulting `registry.toml` snippet.
  3. Add a local model (Ollama) — note auto-population of `model_info` via `ollama show`.
  4. Add an HF-backed model with `[models.fetch]` (`repo`, `files`, `quantizations`) — copy the exact TOML shape from `modelman/README.md`.
  5. Download models (TUI queue → apply on exit; or CLI if it exists).
  6. Reconcile state: `uv run modelman sync` (flag set from Step 1), expected output.
- **Verification:** `grep -A3 'id = "<new-model>"' ~/.config/local-ai/registry.toml` shows the model; `modelman.toml` gains a `[models.<id>]` block after download.
- **Gotchas:** `wt` reads `registry.toml` read-only — model visibility for agents is changed here, not in wt; sync reconciles but does not delete unless told (verify actual behavior, write what it does).
- **Going deeper:** `03-model-families.md`, `modelman/README.md`, sync design specs (paths from Task 0 list).

- [ ] **Step 3: Link check** — expected `ALL LINKS OK`.

- [ ] **Step 4: Commit**

```bash
# from: /Users/keith/github/ohanaverse/local-ai-setup
git add docs/guides/02-providers-and-models.md
git commit -m "docs(guides): add provider and model management guide"
```

---

### Task 4: `docs/guides/03-model-families.md`

**Files:**
- Create: `docs/guides/03-model-families.md`

- [ ] **Step 1: Verify family data**

```bash
grep -n -A3 '\[families' ~/.config/local-ai/modelman.toml
grep -n 'family' ~/.config/local-ai/registry.toml | head -10
grep -n -E 'code|design|tag' ~/.config/local-ai/registry.toml | head -20
```

Expected: real family names, tags (`code`, `design`) actually present. Record real examples for the guide.

- [ ] **Step 2: Write the guide**

- **Prerequisites:** `02-providers-and-models.md` done; at least two models in the registry.
- **TL;DR:** rename/organize families via TUI or direct `modelman.toml` `[families]` edit (whichever Task-3 findings confirm is real); view families in `wt` picker with `d` toggle.
- **Steps:** family concept (grouping across providers); display names (`[families.<id>] display_name` in `modelman.toml`); code/design tag groups and how `wt` shows them (`d` toggles groups, up/down picks, launching advances rotation — key table from `agent-worktree/README.md`); how the picker derives options from `registry.toml`.
- **Verification:** after renaming a family display name, `uv run modelman` shows the new name; `claude-wt` picker groups models under it.
- **Gotchas:** tags are consumed by `wt` rotation — editing tags in `registry.toml` changes agent rotation groups (this is the one place hand-editing registry has a legitimate use, only via modelman); display names are per-machine state (`modelman.toml`), not shared.
- **Going deeper:** `06-wt-agents-and-models.md`, wt registry-consumer spec.

- [ ] **Step 3: Link check** — expected `ALL LINKS OK`.

- [ ] **Step 4: Commit**

```bash
# from: /Users/keith/github/ohanaverse/local-ai-setup
git add docs/guides/03-model-families.md
git commit -m "docs(guides): add model families guide"
```

---

### Task 5: `docs/guides/04-litellm-config.md` + move admin-ui doc

**Files:**
- Create: `docs/guides/04-litellm-config.md`
- Move: `docs/litellm-admin-ui-setup.md` → `docs/reference/`

- [ ] **Step 1: Inspect the real proxy state**

```bash
cat ~/.config/litellm/config.yaml
launchctl list | grep -i litellm
tail -5 ~/.config/litellm/*.log 2>/dev/null || ls ~/.config/litellm/
```

Expected: full `config.yaml` (record its real top-level sections: `model_list` entries, database_url, general_settings), the litellm LaunchAgent label.

- [ ] **Step 2: Write the guide**

- **Prerequisites:** `01-initial-setup.md` complete.
- **TL;DR:** expose a model via modelman (`l` key), restart proxy, confirm via curl; plus hand-edit fallback with exact YAML snippet shape.
- **Steps:**
  1. `config.yaml` anatomy — explain each real section found in Step 1, especially how a `model_list` entry maps to `registry.toml` (`model_name`, `litellm_params.model`, api_base per provider).
  2. The modelman exposure workflow: TUI `l` on a model → writes `model_list` entry + sets `litellm_exposed = true` in `modelman.toml`. Show before/after diff of both files.
  3. Restart the proxy: `launchctl kickstart -k gui/$(id -u)/<label>` (label from Step 1).
  4. Admin UI (web dashboard): condensed from `docs/litellm-admin-ui-setup.md` — URL, how to reach it, what it controls; full detail stays in the moved reference doc.
  5. Hand-editing: when it's appropriate (proxy-only settings: database, logging), when to use modelman instead (anything `model_list`-related).
- **Verification:** `curl -s http://localhost:4000/v1/models | python3 -m json.tool | grep '<model-name>'`.
- **Gotchas:** hand-edited `model_list` entries are overwritten by modelman exposure toggles only for models it knows — document the actual interaction from the litellm-exposure design spec; Postgres/Redis must be up or the proxy fails to boot (how to tell from logs).
- **Going deeper:** `docs/reference/` (all four articles after the move), `models.conf`→registry note.

- [ ] **Step 3: Move the admin doc**

```bash
# from: /Users/keith/github/ohanaverse/local-ai-setup
git mv docs/litellm-admin-ui-setup.md docs/reference/
```

- [ ] **Step 4: Link check + cold read** — expected `ALL LINKS OK`.

- [ ] **Step 5: Commit**

```bash
# from: /Users/keith/github/ohanaverse/local-ai-setup
git add docs/guides/04-litellm-config.md docs/reference/
git commit -m "docs(guides): add LiteLLM config guide; move admin-ui doc to reference"
```

---

### Task 6: `docs/guides/05-benchmarks.md`

**Files:**
- Create: `docs/guides/05-benchmarks.md`

- [ ] **Step 1: Verify benchmark tooling**

```bash
# from: /Users/keith/github/ohanaverse/local-ai-setup
ls benchmarks/ benchmarks/results/ bin/
head -30 bin/llm-isolate-provider
bin/llm-isolate-provider 2>&1 | head -5   # expect usage error, safe
# from: ~/github/ohanaverse/modelman
uv run modelman benchmark --help
```

Expected: `bin/llm-isolate-provider` prints usage on missing arg (exit non-zero, nothing started); `modelman benchmark` help text with real flags (`--family`, `--model` per README).

- [ ] **Step 2: Write the guide**

- **Prerequisites:** models exposed (`04`); **no other local model loaded** (isolation is mandatory).
- **TL;DR:**

```bash
# from: /Users/keith/github/ohanaverse/local-ai-setup
bin/llm-isolate-provider omlx          # pick one: ollama | llamacpp | omlx | omlx-6bit
# from: ~/github/ohanaverse/modelman
uv run modelman benchmark run --family qwen3.8
# from: /Users/keith/github/ohanaverse/local-ai-setup
bin/llm-restore-providers
```

- **Steps:** when isolation is needed (local models share Apple Silicon GPU/RAM — benchmarking two loaded models gives garbage); isolation helpers and their exact args; running single/multi passes (`modelman benchmark run --model …`, multi-pass flags from `--help`); archiving results from `/tmp/<benchmark>-<timestamp>.md` into `benchmarks/results/` with the exact `cp` command; reading the JSON+MD artifacts and the latest-run pointer in `modelman.toml`.
- **Verification:** results file exists in `benchmarks/results/`; `bin/llm-restore-providers` then `launchctl list` shows all four services back.
- **Gotchas:** per-backend stop mechanics (ollama stop vs `omlx stop` vs launchctl unload — table from CLAUDE.md); `omlx` vs `omlx-6bit` variant naming; shebang difference between `benchmarks/*` (Homebrew bash) and `bin/*` (`/bin/bash`); legacy bash scripts in `benchmarks/` still work but modelman benchmark is canonical.
- **Going deeper:** benchmark results dir, modelman benchmark design spec path, `../CLAUDE.md`… wait — CLAUDE.md link is `../../CLAUDE.md` from `docs/guides/`. Use `../../CLAUDE.md`.

- [ ] **Step 3: Link check + cold read** — expected `ALL LINKS OK`.

- [ ] **Step 4: Commit**

```bash
# from: /Users/keith/github/ohanaverse/local-ai-setup
git add docs/guides/05-benchmarks.md
git commit -m "docs(guides): add benchmark operations guide"
```

---

### Task 7: `docs/guides/06-wt-agents-and-models.md`

**Files:**
- Create: `docs/guides/06-wt-agents-and-models.md`

- [ ] **Step 1: Verify wt behavior**

```bash
wt --help
wt --version
ls ~/.config/agent-wt/
head -30 ~/.config/agent-wt/config.toml 2>/dev/null
```

Expected: help text with real flag spellings (`-w`, `--cwd`, `--agent`, `--yolo`, `--init`, `--check-guard`); config.toml with `[[agents]]` entries and default rotation tag.

- [ ] **Step 2: Write the guide**

- **Prerequisites:** wt built and on PATH (build: `# from: ~/github/ohanaverse/agent-worktree` `go build -o "$(go env GOPATH)/bin/wt" ./cmd/wt`); registry has models (`02`); shims installed (from `agent-worktree` README `make install` if shims missing).
- **TL;DR:**

```bash
claude-wt                    # TUI: pick worktree, agent-agnostic model rotation
claude-wt -W my-feature      # skip worktree picker
claude-wt -W my-feature -A claude -M ollama/qwen3.8:27b-mlx   # skip everything
wt --check-guard
```

- **Steps:** the `*-wt` shim table (which agents, model rotation yes/no, session resume yes/no — copy the table from `agent-worktree/README.md`); TUI keys (`enter`, `j`/`k`, `d`, `q`/`esc`); how the model list is derived from `registry.toml` joined with `config.toml` defaults; rotation behavior (launching advances rotation; state in `rotation.state`); `wt config` subcommands (theme etc., from README + `wt config --help`); `--init` in a new repo; launch logging to `usage.jsonl`.
- **Verification:** `wt --check-guard` reports guard state; after a launch, `tail -1 ~/.config/agent-wt/usage.jsonl` shows the launch record.
- **Gotchas:** `wt` never writes providers/models — registry is modelman-owned; `agy-wt` and `shell-wt` have no model rotation; codex/copilot/pi don't support resume.
- **Going deeper:** `agent-worktree/README.md`, `docs/configuration.md` (full path `~/github/ohanaverse/agent-worktree/docs/configuration.md`), registry-consumer spec, `00-config-map.md` relative link `00-config-map.md`.

- [ ] **Step 3: Link check + cold read** — expected `ALL LINKS OK`.

- [ ] **Step 4: Commit**

```bash
# from: /Users/keith/github/ohanaverse/local-ai-setup
git add docs/guides/06-wt-agents-and-models.md
git commit -m "docs(guides): add wt agent and model guide"
```

---

### Task 8: `docs/guides/07-usage-and-spend.md`

**Files:**
- Create: `docs/guides/07-usage-and-spend.md`

- [ ] **Step 1: Verify usage tooling**

```bash
# from: ~/github/ohanaverse/modelman
uv run modelman usage --help
uv run modelman usage report --days 7
```

Expected: real flags; report renders as Markdown with per-model WT launch counts, LiteLLM token/spend columns, reconciliation sections. Capture the actual section headings to describe them accurately.

- [ ] **Step 2: Write the guide**

- **Prerequisites:** LiteLLM running with Postgres spend logging (link `01`), `wt` launches recorded in `usage.jsonl`.
- **TL;DR:** `uv run modelman usage report --days 7    # from: ~/github/ohanaverse/modelman`
- **Steps:** what the report contains (launch counts 1d/7d/30d; LiteLLM requests/tokens/spend; reconciliation: matched / WT-only launches / LiteLLM-only spend; last-launched from `rotation.state`); interpreting mismatches (WT-only = native or non-LiteLLM launch: e.g., a model launched directly; LiteLLM-only spend = ad-hoc API traffic); cadence suggestion (weekly report into a notes file — exact `tee` command):

```bash
# from: ~/github/ohanaverse/modelman
uv run modelman usage report --days 7 | tee /tmp/usage-$(date +%F).md
```

- **Verification:** report sections present as found in Step 1; totals change after a live request through LiteLLM (`curl -s http://localhost:4000/v1/messages ...` then re-run).
- **Gotchas:** reconciliation gaps are expected (native launches bypass LiteLLM); spend accuracy depends on Postgres spend logging being enabled (check in `config.yaml`).
- **Going deeper:** modelman usage-design spec path, `04-litellm-config.md`.

- [ ] **Step 3: Link check + cold read** — expected `ALL LINKS OK`.

- [ ] **Step 4: Commit**

```bash
# from: /Users/keith/github/ohanaverse/local-ai-setup
git add docs/guides/07-usage-and-spend.md
git commit -m "docs(guides): add usage and spend guide"
```

---

### Task 9: `docs/guides/08-maintenance-and-troubleshooting.md`

**Files:**
- Create: `docs/guides/08-maintenance-and-troubleshooting.md`

- [ ] **Step 1: Verify service inventory and log locations**

```bash
launchctl list | grep -iE 'llm|ollama|omlx|litellm|redis|postgres'
ls ~/Library/Logs/ 2>/dev/null | grep -iE 'llm|ollama|omlx|litellm'
grep -i -A2 'StandardOutPath\|StandardErrorPath' ~/Library/LaunchAgents/*.plist | head -20
curl -s -o /dev/null -w '%{http_code}' http://localhost:4000/v1/models
curl -s -o /dev/null -w '%{http_code}' http://localhost:11434/api/tags
```

Expected: running job labels, real log paths, `200` for running services (record actual codes/ports).

- [ ] **Step 2: Write the guide**

- **Prerequisites:** none; this guide assumes the stack was set up per `01`.
- **TL;DR:** the full health check as one block:

```bash
# from: /Users/keith/github/ohanaverse/local-ai-setup
curl -s -o /dev/null -w 'litellm: %{http_code}\n' http://localhost:4000/health || echo "litellm: DOWN"
curl -s -o /dev/null -w 'ollama: %{http_code}\n' http://localhost:11434/api/tags || echo "ollama: DOWN"
launchctl list | grep -iE 'llm|ollama|omlx|litellm|redis|postgres'
```

(Confirm the real health endpoints first — LiteLLM `/health/liveliness` vs `/v1/models` — from Step 1 results and the admin-ui reference doc.)

- **Steps:**
  1. Post-reboot: what starts automatically (LaunchAgent) vs what you must start; exact `kickstart`/`bootout` commands per service with real labels.
  2. Service restart table — one row per backend: check command, restart command, log path:

| Backend | Check | Restart | Logs |
|---|---|---|---|
| LiteLLM | `curl localhost:4000/v1/models` | `launchctl kickstart -k gui/$(id -u)/<label>` | path from Step 1 |
| Ollama | `curl localhost:11434/api/tags` | `ollama stop <model>` / macOS menu bar app | `~/.ollama/logs/` |
| llama.cpp | (port from Step 1) | `launchctl kickstart -k … <label>` | plist StandardErrorPath |
| oMLX | `omlx status` or port probe (verify) | `omlx stop` / `omlx serve` (verify flags) | path from Step 1 |
| Redis / Postgres | `redis-cli ping` / `pg_isready` | `brew services restart …` (verify) | brew logs |

  3. "Model missing from LiteLLM" flow: check `litellm_exposed` in `modelman.toml` → check `model_list` in `config.yaml` → restart proxy → check proxy logs for the model name. Written as an ordered checklist with exact commands at each branch.
  4. Upgrades: modelman (`cd ~/github/ohanaverse/modelman && git pull && uv sync`), wt (`git pull && go build -o "$(go env GOPATH)/bin/wt" ./cmd/wt`), LiteLLM (`uv tool upgrade 'litellm[proxy]'`) — verify each command's validity (`uv tool upgrade --help`; if tool-upgrade syntax differs use `uv tool install --force --reinstall 'litellm[proxy]'` and mark which one works).
- **Verification:** the TL;DR block run after intentionally stopping one service shows the failure; after restart shows healthy.
- **Gotchas:** per-backend stop mechanics differ (from CLAUDE.md); don't `launchctl unload` unless replacing the plist; Postgres must run before LiteLLM.
- **Going deeper:** `01-initial-setup.md`, `05-benchmarks.md`, admin-ui reference doc.

- [ ] **Step 3: Link check + cold read** — expected `ALL LINKS OK`.

- [ ] **Step 4: Commit**

```bash
# from: /Users/keith/github/ohanaverse/local-ai-setup
git add docs/guides/08-maintenance-and-troubleshooting.md
git commit -m "docs(guides): add maintenance and troubleshooting guide"
```

---

### Task 10: README index rewrite + CLAUDE.md pointer

**Files:**
- Modify: `README.md` (full rewrite, thin index)
- Modify: `CLAUDE.md` (add guides pointer)

- [ ] **Step 1: Rewrite README.md**

New README structure (prose kept to one-liners):

```markdown
# local-ai-setup

Entry point for running local and routed LLMs on macOS.
Orchestrates three repos:

| Repo | Role |
|---|---|
| `local-ai-setup` (this) | backends + LiteLLM proxy, benchmarks, guides |
| `modelman` | model registry TUI/CLI; source of truth for models |
| `agent-worktree` (`wt`) | worktree agent launcher with model rotation |

## User guides (docs/guides/)

| Guide | Use when you need to… |
|---|---|
| [00-config-map](docs/guides/00-config-map.md) | find which tool owns which config file |
| [01-initial-setup](docs/guides/01-initial-setup.md) | set up Ollama, LiteLLM, llama.cpp, oMLX from scratch |
| [02-providers-and-models](docs/guides/02-providers-and-models.md) | add/edit providers + models, download, sync |
| [03-model-families](docs/guides/03-model-families.md) | organize models into families/tags for rotation |
| [04-litellm-config](docs/guides/04-litellm-config.md) | expose models, edit proxy config, admin UI |
| [05-benchmarks](docs/guides/05-benchmarks.md) | benchmark models with provider isolation |
| [06-wt-agents-and-models](docs/guides/06-wt-agents-and-models.md) | launch/rotate coding agents with `wt` |
| [07-usage-and-spend](docs/guides/07-usage-and-spend.md) | report WT launches vs LiteLLM spend |
| [08-maintenance-and-troubleshooting](docs/guides/08-maintenance-and-troubleshooting.md) | restart services, debug missing models, upgrade |

## 60-second smoke check
<the four-curl/launchctl block from guide 08's TL;DR, six lines max>

## Deep-dive reference
- docs/reference/ list (five entries after Task 5's move, URL-encoded)

## Repo layout
<current tree updated: + docs/guides, docs/archive>
```

Remove from README everything the guides now own (modelman workflow prose, wt flags, benchmark commands, usage report description). Keep: the stack framing, cross-repo tracker link in a Status line, one-off benchmark write-up links in a Legacy line.

- [ ] **Step 2: Update CLAUDE.md**

Add directly under the `# CLAUDE.md` title:

```markdown
## Docs
- User playbooks: `docs/guides/` — canonical task guides (setup, models, LiteLLM, benchmarks, wt, usage, troubleshooting). Read `00-config-map.md` first for config locations.
```

- [ ] **Step 3: Link check** — expected `ALL LINKS OK` (README now links all ten guide+reference files).

- [ ] **Step 4: Commit**

```bash
# from: /Users/keith/github/ohanaverse/local-ai-setup
git add README.md CLAUDE.md
git commit -m "docs: restructure README as guides index; point CLAUDE.md at playbooks"
```

---

### Task 11: Final verification sweep

**Files:**
- Modify: only files with failures found (guides, README, CLAUDE.md)

- [ ] **Step 1: Full link check**

Run the link-check script (Verification tooling). Expected: `ALL LINKS OK`.

- [ ] **Step 2: Coverage check against spec inventory**

```bash
# from: /Users/keith/github/ohanaverse/local-ai-setup
ls docs/guides/
```

Expected output (exactly ten files): `00-config-map.md` `01-initial-setup.md` `02-providers-and-models.md` `03-model-families.md` `04-litellm-config.md` `05-benchmarks.md` `06-wt-agents-and-models.md` `07-usage-and-spend.md` `08-maintenance-and-troubleshooting.md` (nine guides; ten entries only if directory listing includes others — investigate any extras).

- [ ] **Step 3: Verify migrations happened**

```bash
# from: /Users/keith/github/ohanaverse/local-ai-setup
git log --diff-filter=R --name-status --oneline | grep -E '^R100' | sort -u
ls docs/archive/ docs/reference/
git grep -n "Local AI Setup" README.md CLAUDE.md docs/guides/ || true
```

Expected: `R100` renames for the setup doc (→ `docs/archive/`) and admin-ui doc (→ `docs/reference/)`; no stale references in README/CLAUDE.md/guides to `docs/litellm-admin-ui-setup.md` or the old `docs/Local AI Setup…` path.

- [ ] **Step 4: Cold-read the guide set**

Read every TL;DR block in files `00`–`08` in one pass. Fix any implicit prerequisite (state that references another guide without linking it). No commit unless fixes were made.

- [ ] **Step 5: Commit fixes (if any) + final commit message check**

```bash
# from: /Users/keith/github/ohanaverse/local-ai-setup
git status --short
git log --oneline main..HEAD
```

Expected: clean tree; ~10 commits ahead of `main`, all prefixed `docs`. If fixes were made:

```bash
git add -A && git commit -m "docs(guides): final sweep fixes from verification"
```

---

## Self-review notes (per writing-plans skill)

1. **Spec coverage:** all nine guides → Tasks 1–9; README index → Task 10; CLAUDE.md pointer → Task 10; doc migrations (archive + reference moves) → Tasks 2 and 5; verification policy (`--help`, UNVERIFIED marking, Gotchas discrepancies) → embedded in every task's Step 1 + skeleton conventions; testing trio (verification pass / link check / cold read) → per-task steps + Task 11. No spec requirement lacks a task.
2. **Placeholder scan:** guide *content* steps give exact structure, required commands, and expected outputs; prose is written during execution per the accuracy policy (content depends on live verification output — the plan defines how to verify, not the final sentences). No "TBD"/"similar to Task N" references; cross-references reuse explicit paths or the defined Verification tooling script.
3. **Consistency:** guide file names match spec inventory table and README table in Task 10; skeleton section names match spec; commit messages follow `docs(guides): …` convention throughout.