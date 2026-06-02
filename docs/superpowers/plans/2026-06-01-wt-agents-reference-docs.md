# wt-agents Reference Docs Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Create a permanent, dateless set of per-agent reference docs under `docs/wt-agents/` that document, for each `*-wt` launcher, how the underlying agent is configured, how it authenticates, and how the launcher invokes it — including a verified worked example for the NYT LiteLLM `pi` setup on this machine.

**Architecture:** Six new Markdown files in a new directory (`docs/wt-agents/`): one index plus one per agent. Each per-agent file follows the same six-section template (Overview, Installation, Configuration, Authentication, Model selection, Verified). `pi-wt.md` and `claude-wt.md` carry first-hand "Verified on this machine, YYYY-MM-DD" sections; `codex-wt.md`, `copilot-wt.md`, `agy-wt.md` are convention-only with explicit "not verified" disclosure. One additive line in `bin/README.md` cross-links the new directory. No code changes in this plan; the sibling spec `2026-06-01-pi-wt-litellm-wrapper-design.md` covers the `pi-wt` script change.

**Tech Stack:** Markdown. No build, no tests beyond manual link checking and `python3 scripts/validate_marketplace.py`. Files live in `docs/wt-agents/` and are tracked in git.

**Spec source:** `docs/superpowers/specs/2026-06-01-wt-agents-reference-docs-design.md`

**Conventions for this plan:**
- Each doc-writing task ends with the same verification recipe (link check, marketplace validator).
- Tasks are independent — each commits a single self-contained file (or single file edit). The order is "heaviest verified content first" so that any drift caught later costs less.
- All paths are relative to the repo root (`/Volumes/scratch/github/ohanaverse/agent-toolkit/.worktrees/pi-wt-litellm/`).
- All commits go on the current branch (`pi-wt-litellm`); this branch is shared with the sibling pi-wt code-change work and ships together.

---

## File Structure

| Path | Status | Purpose |
|---|---|---|
| `docs/wt-agents/README.md` | Create | Index. One-liner per agent + verification-status table + scope statement. |
| `docs/wt-agents/pi-wt.md` | Create | **Verified.** Full NYT LiteLLM worked example (heaviest content). |
| `docs/wt-agents/claude-wt.md` | Create | **Verified.** Binary path, `~/.claude/` layout snapshot. |
| `docs/wt-agents/codex-wt.md` | Create | Convention only; cites upstream Codex CLI docs. |
| `docs/wt-agents/copilot-wt.md` | Create | Convention only; "not installed on this machine." |
| `docs/wt-agents/agy-wt.md` | Create | Convention only; "not installed on this machine." |
| `bin/README.md` | Modify (1 line) | Add cross-link near top of `*-wt` section. |

Per-file responsibility: each agent file owns the full six-section narrative for that agent. The index owns navigation and verification-status disclosure only — no agent-specific content, so adding/removing an agent stays a one-row edit.

---

## Per-file template (six sections, used by every per-agent file)

Every per-agent file uses these section headings, in this order:

```markdown
# <agent>-wt

## Overview
## Installation
## Configuration files & locations
## Authentication & credentials
## Model selection
## Verified on this machine
```

**Section content rules:**

1. **Overview** — what the underlying agent is, who builds it, link to upstream docs. 1–3 sentences.
2. **Installation** — install command (cite source). Same content as `bin/README.md`'s install column, plus one-paragraph context.
3. **Configuration files & locations** — where the agent reads from (`~/.pi/agent/`, `~/.claude/`, `~/.codex/`, `~/.copilot/`, `~/.antigravity/` or equivalent). Key files with one-line purpose each.
4. **Authentication & credentials** — how the agent obtains API keys / OAuth tokens.
5. **Model selection** — how the agent picks a model, including how `<name>-wt`'s passthrough interacts (`--model`, `--profile -m`, `COPILOT_PROVIDER_*` env vars, or none for agy). Per-agent quirks called out.
6. **Verified on this machine** — for verified files: dated `Verified on this machine, YYYY-MM-DD` heading, then concrete observations. For convention-only files: a single short sentence stating non-verification, plus links to upstream docs.

Cross-references to `bin/README.md`, `docs/agent-home-directories.md`, `docs/configuration.md`, and upstream agent docs are inline within the relevant sections, not collected in a dedicated section.

---

## Task 1: Create `docs/wt-agents/pi-wt.md` (verified, heaviest content)

Done first because: largest, most fact-dense, and the single most error-prone file (drift risk). Producing it first surfaces template issues before they propagate to four other files.

**Files:**
- Create: `docs/wt-agents/pi-wt.md`

- [ ] **Step 1: Create the directory**

```bash
mkdir -p docs/wt-agents
```

- [ ] **Step 2: Write `docs/wt-agents/pi-wt.md`**

Full content to write (all facts sourced from the spec's "`pi-wt.md` Verified section content" block; cross-links resolved):

```markdown
# pi-wt

## Overview

`pi-wt` is the worktree launcher for [pi](https://github.com/mariozechner/pi-coding-agent), a provider-agnostic AI coding agent built by Mario Zechner. pi reads its provider list and model catalogue from `~/.pi/agent/models.json` at startup and presents whichever models are reachable with the current credentials. See the [bin/README.md `*-wt` launcher table](../../bin/README.md) for the launcher's flags and rotation behavior.

## Installation

```bash
npm install -g @mariozechner/pi-coding-agent
```

This installs the `pi` binary globally. The `pi-wt` launcher in this repo wraps `pi` with worktree-aware FZF selection and code/design model rotation. Install `pi-wt` itself by copying `bin/pi-wt` (and the rest of `bin/`) into `~/.local/bin/` per [bin/README.md](../../bin/README.md).

## Configuration files & locations

pi reads from `~/.pi/agent/`. Key files:

| File | Purpose |
|---|---|
| `~/.pi/agent/models.json` | Provider and model catalogue. pi only shows models declared here. |
| `~/.pi/agent/settings.json` | Non-credential UI state (last-seen pi version, theme, installed packages). |
| `~/.pi/agent/activate-litellm.sh` | **NYT-specific wrapper.** Fetches LiteLLM credentials, exports them, and `exec`s `pi`. Optional — vanilla pi users do not have this file. |

`models.json` supports environment-variable substitution at runtime: `apiKey: $ANTHROPIC_API_KEY` reads the env var when pi launches, so credentials never have to live on disk inside `models.json`.

## Authentication & credentials

pi obtains API keys from environment variables referenced in `~/.pi/agent/models.json`. The credential-refresh-on-launch pattern is: a wrapper script fetches a fresh key (typically via a credential helper that talks to a vault or SSO endpoint), exports the key into the environment, and `exec`s `pi`. pi then resolves `$ANTHROPIC_API_KEY` (or whichever variable the provider references) when it parses `models.json`.

In the NYT LiteLLM deployment on this machine, the wrapper is `~/.pi/agent/activate-litellm.sh`. It calls `/usr/local/bin/litellm-credential-helper.sh` to fetch a LiteLLM key, exports `ANTHROPIC_API_KEY` and `LITELLM_API_KEY`, sets `ANTHROPIC_DEFAULT_OPUS_MODEL=claude-opus-4-7` and `ANTHROPIC_CUSTOM_HEADERS="x-litellm-tags: cc_settings:6ebaf51"`, then `exec`s `pi "$@"`. The wrapper also injects `--provider anthropic --model claude-opus-4-7` when neither `--model` nor `--provider` is already in `"$@"`.

## Model selection

pi picks a model from `~/.pi/agent/models.json` based on `--model <id>` and `--provider <name>` arguments. With no flags, pi prompts the user (or, in the LiteLLM wrapper, falls back to the injected default).

`pi-wt` interacts with this via `--model` passthrough. The launcher's rotation chooses a model name from `~/.config/ai-shell/models.conf`; if that name also exists as a model ID in `~/.pi/agent/models.json`, `pi-wt` execs `pi --model <id>`. **If the model ID is not present in `models.json`, `pi-wt` silently falls back to pi's default** — this is documented in [bin/README.md](../../bin/README.md) and is intentional, not a bug. Add the model to `models.json` to enable rotation for it.

For `native:pi` (the launcher's "use pi's own default" sentinel), `pi-wt` execs `pi` with no `--model` flag and lets pi (or the LiteLLM wrapper, if present) choose.

## Verified on this machine

**Verified on this machine, 2026-06-01.**

Concrete observations from this machine:

- **Binary:** `~/.asdf/installs/nodejs/25.8.0/bin/pi` (npm-installed, raw).
- **Wrapper script:** `~/.pi/agent/activate-litellm.sh`. Fetches LiteLLM key via `/usr/local/bin/litellm-credential-helper.sh`, exports `ANTHROPIC_API_KEY` and `LITELLM_API_KEY`, sets `ANTHROPIC_DEFAULT_OPUS_MODEL=claude-opus-4-7` and `ANTHROPIC_CUSTOM_HEADERS="x-litellm-tags: cc_settings:6ebaf51"`. Injects `--provider anthropic --model claude-opus-4-7` only when neither `--model` nor `--provider` is present in the args, then `exec pi "$@"`.
- **Interactive entry point:** `pi()` zsh function in `~/.zshrc` (lines 161–165), calling `activate-litellm.sh "$@"`.
- **Provider config:** `~/.pi/agent/models.json` declares one provider, `anthropic`, named "Anthropic (LiteLLM proxy)", with:
  - `baseUrl: https://llm-gateway.nyt.net`
  - `api: openai-completions` (an OpenAI-compatible API surface routed through LiteLLM to Anthropic)
  - `apiKey: $ANTHROPIC_API_KEY` (env-var substitution at runtime)
  - Four model IDs: `claude-opus-4-7`, `claude-opus-4-6`, `claude-sonnet-4-6`, `claude-haiku-4-5`
  - Bare model IDs (no `anthropic/` prefix), differing from the Obsidian note's generic example.
- **Credential helper:** `/usr/local/bin/litellm-credential-helper.sh` (root-owned, executable, ~5.9KB). Implementation details out of scope; treated as opaque dependency.
- **Settings:** `~/.pi/agent/settings.json` records last-seen pi version (`0.78.0`), theme, and one installed package (`https://github.com/obra/superpowers`).

### Documented gotcha (pi-wt-specific)

`pi-wt` is a bash script. Bash scripts do not inherit zsh shell functions, so `pi-wt`'s `exec pi …` call performs PATH lookup and finds the raw npm binary, not the wrapper. The result: pi launches without LiteLLM credentials and reports "No models available."

This is fixed by the sibling spec [`2026-06-01-pi-wt-litellm-wrapper-design.md`](../superpowers/specs/2026-06-01-pi-wt-litellm-wrapper-design.md), which auto-detects `~/.pi/agent/activate-litellm.sh` and execs it instead of `pi` when present.

### Background context (provider system)

pi's provider system is provider-agnostic: any provider declared in `models.json` (or via an extension that overrides selection) can serve models, as long as its API surface matches one of pi's supported types (`openai-completions`, `anthropic`, etc.). The env-var substitution syntax (`$NAME` in any string field) is resolved at launch. The credential-refresh-on-launch pattern — fetch a fresh key in a wrapper, then `exec pi` — keeps short-lived enterprise credentials working without persisting them to disk.

The Obsidian note `Pi Coding Agent with LiteLLM.md` describes a generic LiteLLM topology with `localhost:4000` and `anthropic/`-prefixed model IDs. The deployed reality on this machine differs (real gateway URL `https://llm-gateway.nyt.net`, bare model IDs). This file records the deployed reality; the Obsidian note remains the broader narrative reference for users with a different LiteLLM topology.
```

- [ ] **Step 3: Verify the file is well-formed Markdown**

```bash
ls -l docs/wt-agents/pi-wt.md
wc -l docs/wt-agents/pi-wt.md
```

Expected: file exists, ~80–100 lines.

- [ ] **Step 4: Verify all relative links resolve**

```bash
grep -oE '\]\([^)]+\)' docs/wt-agents/pi-wt.md | sed 's/](//;s/)$//' | grep -v '^http' | while read link; do
  target="docs/wt-agents/${link%%#*}"
  # resolve ../ etc. relative to the file's directory
  resolved=$(cd docs/wt-agents && readlink -f "${link%%#*}" 2>/dev/null || python3 -c "import os; print(os.path.realpath('${link%%#*}'))")
  if [ ! -e "$resolved" ]; then echo "BROKEN: $link → $resolved"; fi
done
```

Expected: no `BROKEN:` output. (The link to the sibling spec resolves to `docs/superpowers/specs/2026-06-01-pi-wt-litellm-wrapper-design.md`, which exists per `git status`.)

- [ ] **Step 5: Re-verify the worked-example facts against the live machine**

Spot-check each verified claim:

```bash
ls -l ~/.pi/agent/activate-litellm.sh ~/.pi/agent/models.json ~/.pi/agent/settings.json
ls -l /usr/local/bin/litellm-credential-helper.sh
sed -n '155,170p' ~/.zshrc | grep -A4 'pi()'
grep -E 'baseUrl|api|apiKey|claude-opus-4-7|claude-opus-4-6|claude-sonnet-4-6|claude-haiku-4-5' ~/.pi/agent/models.json
which pi
```

Expected: every fact in the Verified section matches what the commands report. If anything has drifted, fix the file before committing.

- [ ] **Step 6: Run the marketplace validator**

```bash
python3 scripts/validate_marketplace.py
```

Expected: `OK` (no plugin changes, but standard practice).

- [ ] **Step 7: Commit**

```bash
git add docs/wt-agents/pi-wt.md
git commit -m "docs(wt-agents): add pi-wt reference with NYT LiteLLM verified example"
```

---

## Task 2: Create `docs/wt-agents/claude-wt.md` (verified)

**Files:**
- Create: `docs/wt-agents/claude-wt.md`

- [ ] **Step 1: Verify the claude-wt facts on this machine before drafting**

```bash
which claude
ls ~/.claude/ | head -20
ls ~/.claude/skills/ 2>/dev/null | head -5
test -f ~/.claude.json && echo "~/.claude.json exists" || echo "missing"
test -f ~/.claude/CLAUDE.md && echo "~/.claude/CLAUDE.md exists" || echo "missing"
test -f ~/.claude/settings.json && echo "settings.json exists" || echo "missing"
```

Note the binary path and which subdirectories exist for use in the Verified section below.

- [ ] **Step 2: Write `docs/wt-agents/claude-wt.md`**

```markdown
# claude-wt

## Overview

`claude-wt` is the worktree launcher for [Claude Code](https://claude.ai/code), Anthropic's official CLI. Claude Code reads global identity, skills, and session state from `~/.claude/` and tracks OAuth state in `~/.claude.json`. See the [bin/README.md `*-wt` launcher table](../../bin/README.md) for the launcher's flags and rotation behavior, and [docs/agent-home-directories.md](../agent-home-directories.md) for the `~/.claude/` layout in detail.

## Installation

```bash
curl -fsSL https://claude.ai/install.sh | bash
```

This installs the `claude` binary. The `claude-wt` launcher in this repo wraps `claude` with worktree-aware FZF selection and code/design model rotation. Install `claude-wt` itself by copying `bin/claude-wt` (and the rest of `bin/`) into `~/.local/bin/` per [bin/README.md](../../bin/README.md).

## Configuration files & locations

Claude Code reads from `~/.claude/`. Full layout in [docs/agent-home-directories.md](../agent-home-directories.md). Briefly:

| File / directory | Purpose |
|---|---|
| `~/.claude/CLAUDE.md` | Global persistent instructions for every session. |
| `~/.claude/settings.json` | Permissions, theme, default model. |
| `~/.claude/rules/` | Modular path-scoped rules. |
| `~/.claude/skills/` | Reusable workflows (each becomes a slash command). |
| `~/.claude/subagents/` | Specialized agents with scoped tool access. |
| `~/.claude.json` | OAuth sessions, MCP server config, internal caches. |

## Authentication & credentials

Claude Code uses OAuth, with state stored in `~/.claude.json`. The internals of that file are observable but should be treated as opaque — the supported way to (re-)authenticate is `claude /login`, not editing `~/.claude.json` by hand. There is no environment-variable credential pattern equivalent to pi's `$ANTHROPIC_API_KEY`-via-wrapper setup; `claude-wt` does not need a credential wrapper.

## Model selection

Claude Code picks a model from `--model <name>` or, absent that, from `~/.claude/settings.json`'s default. `claude-wt` passes the rotation-selected model directly through with `--model`. The model name format is whatever Claude Code accepts (e.g., `claude-opus-4-7`); see [bin/README.md](../../bin/README.md) for the rotation contract and the `native:claude` sentinel.

## Verified on this machine

**Verified on this machine, 2026-06-01.**

- **Binary:** `<output of `which claude` from Step 1>`.
- **Home directory:** `~/.claude/` exists with `CLAUDE.md`, `settings.json`, `rules/`, `skills/`, `subagents/` (whichever of these are present per Step 1).
- **OAuth state:** `~/.claude.json` exists. Internals are an OAuth-token cache; treat as opaque.
- **No credential wrapper.** `claude-wt`'s `exec claude …` works directly against the installed `claude` binary; no equivalent to pi's `activate-litellm.sh` is needed in this deployment.
```

> **Note for the implementer:** replace the `<output of …>` and "whichever of these are present" placeholders with the actual values from Step 1 before saving. Do not commit the bracketed prose.

- [ ] **Step 3: Re-run the link check (same recipe as Task 1, Step 4) on `claude-wt.md`**

Adjust the path:

```bash
grep -oE '\]\([^)]+\)' docs/wt-agents/claude-wt.md | sed 's/](//;s/)$//' | grep -v '^http' | while read link; do
  resolved=$(cd docs/wt-agents && python3 -c "import os; print(os.path.realpath('${link%%#*}'))")
  if [ ! -e "$resolved" ]; then echo "BROKEN: $link → $resolved"; fi
done
```

Expected: no `BROKEN:` output.

- [ ] **Step 4: Run the marketplace validator**

```bash
python3 scripts/validate_marketplace.py
```

Expected: `OK`.

- [ ] **Step 5: Commit**

```bash
git add docs/wt-agents/claude-wt.md
git commit -m "docs(wt-agents): add claude-wt reference"
```

---

## Task 3: Create `docs/wt-agents/codex-wt.md` (convention only)

**Files:**
- Create: `docs/wt-agents/codex-wt.md`

- [ ] **Step 1: Write `docs/wt-agents/codex-wt.md`**

```markdown
# codex-wt

## Overview

`codex-wt` is the worktree launcher for [OpenAI Codex CLI](https://github.com/openai/codex). Codex CLI uses TOML for configuration and a profiles system to switch between provider/model combinations. See the [bin/README.md `*-wt` launcher table](../../bin/README.md) for the launcher's flags and rotation behavior, and [docs/agent-home-directories.md](../agent-home-directories.md) for the `~/.codex/` layout.

## Installation

```bash
npm install -g @openai/codex
```

This installs the `codex` binary. The `codex-wt` launcher in this repo wraps `codex` with worktree-aware FZF selection and code/design model rotation. Install `codex-wt` itself by copying `bin/codex-wt` into `~/.local/bin/` per [bin/README.md](../../bin/README.md).

## Configuration files & locations

Codex reads from `~/.codex/`. Full layout in [docs/agent-home-directories.md](../agent-home-directories.md). Briefly:

| File | Purpose |
|---|---|
| `~/.codex/config.toml` | Primary entry point. Global settings: `sandbox_mode`, `approval_policy`, default model, profiles. |
| `~/.codex/AGENTS.md` | Universal instructions applied to every Codex project. |
| `~/.codex/AGENTS.override.md` | Local-only override file with absolute precedence. |
| `~/.codex/auth.json` | API keys and organization identifiers. |
| `~/.codex/multi-auth/` | Multiple-account identity management. |

## Authentication & credentials

Codex stores API keys in `~/.codex/auth.json`. See the [Codex CLI authentication docs](https://github.com/openai/codex#authentication) for setup. Profiles in `config.toml` can reference different credentials, enabling provider switching.

## Model selection

Codex picks a model from a `--profile <name>` and `-m <model>` flag combination. `codex-wt` passes the rotation-selected model with `--profile ollama-launch -m <model>`. **A profile named `ollama-launch` must exist in `~/.codex/config.toml` for rotation to work**; without it, codex errors out. Define the profile to point at whichever provider/endpoint the rotation models live behind. See the [Codex profiles docs](https://github.com/openai/codex#profiles) for syntax.

The launcher's `native:codex` sentinel maps to codex's own default model for that profile.

## Verified on this machine

**Convention only; codex is used on other machines, not this one.** Statements above are sourced from the upstream Codex CLI repository and docs. Re-verify against the local machine before relying on details for credentials or profile syntax.
```

- [ ] **Step 2: Link check**

```bash
grep -oE '\]\([^)]+\)' docs/wt-agents/codex-wt.md | sed 's/](//;s/)$//' | grep -v '^http' | while read link; do
  resolved=$(cd docs/wt-agents && python3 -c "import os; print(os.path.realpath('${link%%#*}'))")
  if [ ! -e "$resolved" ]; then echo "BROKEN: $link → $resolved"; fi
done
```

Expected: no `BROKEN:` output.

- [ ] **Step 3: Run the marketplace validator**

```bash
python3 scripts/validate_marketplace.py
```

Expected: `OK`.

- [ ] **Step 4: Commit**

```bash
git add docs/wt-agents/codex-wt.md
git commit -m "docs(wt-agents): add codex-wt reference (convention only)"
```

---

## Task 4: Create `docs/wt-agents/copilot-wt.md` (convention only)

**Files:**
- Create: `docs/wt-agents/copilot-wt.md`

- [ ] **Step 1: Write `docs/wt-agents/copilot-wt.md`**

```markdown
# copilot-wt

## Overview

`copilot-wt` is the worktree launcher for [GitHub Copilot CLI](https://github.com/github/copilot-cli). Copilot CLI authenticates against the GitHub Copilot subscription and supports routing to alternative providers via env-var configuration (e.g., the [Ollama-Copilot integration](https://docs.ollama.com/integrations/copilot-cli)). See the [bin/README.md `*-wt` launcher table](../../bin/README.md) for the launcher's flags and rotation behavior.

## Installation

```bash
npm install -g @github/copilot
```

This installs the `copilot` binary. The `copilot-wt` launcher in this repo wraps `copilot` with worktree-aware FZF selection and code/design model rotation. Install `copilot-wt` itself by copying `bin/copilot-wt` into `~/.local/bin/` per [bin/README.md](../../bin/README.md).

## Configuration files & locations

Copilot CLI reads its primary state from `~/.copilot/` (per upstream Copilot CLI docs). Auth state is tied to the user's GitHub session. See the [Copilot CLI docs](https://github.com/github/copilot-cli) for the full file layout.

## Authentication & credentials

Native auth: `gh auth login` plus a Copilot subscription. Provider override: set `COPILOT_PROVIDER_BASE_URL`, `COPILOT_PROVIDER_API_KEY`, and `COPILOT_PROVIDER_WIRE_API` to point Copilot at a non-GitHub endpoint (Ollama, OpenAI-compatible proxies). See the [Ollama integration docs](https://docs.ollama.com/integrations/copilot-cli) for the canonical example.

## Model selection

Copilot picks a model from `--model <name>` or via the `COPILOT_MODEL` environment variable. `copilot-wt` does not pass `--model` directly; instead it sets the `COPILOT_PROVIDER_*` env vars when the rotation selects an Ollama-style local model, and maps `native:copilot` to `--model auto` so Copilot uses its own model selection logic. See [bin/README.md](../../bin/README.md) for the full passthrough table.

## Verified on this machine

**Not installed on this machine.** Statements above are sourced from the upstream GitHub Copilot CLI docs and the linked Ollama integration page. Re-verify before relying on details for env-var names or auth flow.
```

- [ ] **Step 2: Link check, marketplace validator, commit (same recipe as Task 3)**

```bash
grep -oE '\]\([^)]+\)' docs/wt-agents/copilot-wt.md | sed 's/](//;s/)$//' | grep -v '^http' | while read link; do
  resolved=$(cd docs/wt-agents && python3 -c "import os; print(os.path.realpath('${link%%#*}'))")
  if [ ! -e "$resolved" ]; then echo "BROKEN: $link → $resolved"; fi
done
python3 scripts/validate_marketplace.py
git add docs/wt-agents/copilot-wt.md
git commit -m "docs(wt-agents): add copilot-wt reference (convention only)"
```

Expected: no `BROKEN:` output, validator `OK`, commit succeeds.

---

## Task 5: Create `docs/wt-agents/agy-wt.md` (convention only)

**Files:**
- Create: `docs/wt-agents/agy-wt.md`

- [ ] **Step 1: Write `docs/wt-agents/agy-wt.md`**

```markdown
# agy-wt

## Overview

`agy-wt` is the worktree launcher for [Antigravity CLI](https://antigravity.google/cli) (`agy`), Google's AI coding agent CLI. See the [bin/README.md `*-wt` launcher table](../../bin/README.md) for the launcher's flags and rotation behavior.

## Installation

```bash
curl -fsSL https://antigravity.google/cli/install.sh | bash
```

This installs the `agy` binary. The `agy-wt` launcher in this repo wraps `agy` with worktree-aware FZF selection. Install `agy-wt` itself by copying `bin/agy-wt` into `~/.local/bin/` per [bin/README.md](../../bin/README.md).

## Configuration files & locations

Antigravity CLI reads from `~/.antigravity/` (per upstream docs). See the [Antigravity CLI documentation](https://antigravity.google/cli) for the full layout.

## Authentication & credentials

Antigravity CLI authenticates against a Google account; see the [Antigravity CLI docs](https://antigravity.google/cli) for the supported flow.

## Model selection

`agy-wt` does **not** pass a `--model` flag through to `agy`. The launcher rotates state (so the next launcher in the rotation cycle sees the advance) but lets `agy` choose its own model. This is intentional and called out in the [bin/README.md `*-wt` launcher table](../../bin/README.md) ("Model passthrough: *none*").

## Verified on this machine

**Not installed on this machine.** Statements above are sourced from the upstream Antigravity CLI install page. Re-verify before relying on details for the home directory layout or auth flow.
```

- [ ] **Step 2: Link check, marketplace validator, commit**

```bash
grep -oE '\]\([^)]+\)' docs/wt-agents/agy-wt.md | sed 's/](//;s/)$//' | grep -v '^http' | while read link; do
  resolved=$(cd docs/wt-agents && python3 -c "import os; print(os.path.realpath('${link%%#*}'))")
  if [ ! -e "$resolved" ]; then echo "BROKEN: $link → $resolved"; fi
done
python3 scripts/validate_marketplace.py
git add docs/wt-agents/agy-wt.md
git commit -m "docs(wt-agents): add agy-wt reference (convention only)"
```

Expected: no `BROKEN:` output, validator `OK`, commit succeeds.

---

## Task 6: Create `docs/wt-agents/README.md` (index)

Done after all five per-agent files exist so every link in the index resolves on first write.

**Files:**
- Create: `docs/wt-agents/README.md`

- [ ] **Step 1: Write `docs/wt-agents/README.md`**

```markdown
# wt-agents reference

Per-agent reference docs for the `*-wt` launchers in [`bin/`](../../bin/). Each file documents how the underlying agent is configured on disk, how it authenticates, and how the launcher invokes it.

## Scope

These docs cover the agents launched by `claude-wt`, `codex-wt`, `copilot-wt`, `pi-wt`, and `agy-wt`. They do **not** cover `ai-shell`, `ai-code`, `ai-design`, or other non-`*-wt` tools — see [bin/README.md](../../bin/README.md) for those.

The launcher contract itself (flags, rotation behavior, install commands) lives in [bin/README.md](../../bin/README.md). These docs add per-agent context that does not fit in that table.

## Agents

| Agent | File | Verification status |
|---|---|---|
| Claude Code | [claude-wt.md](claude-wt.md) | Verified on this machine, 2026-06-01 |
| OpenAI Codex CLI | [codex-wt.md](codex-wt.md) | Convention only (used on other machines) |
| GitHub Copilot CLI | [copilot-wt.md](copilot-wt.md) | Convention only (not installed on this machine) |
| pi-coding-agent | [pi-wt.md](pi-wt.md) | Verified on this machine, 2026-06-01 — includes NYT LiteLLM worked example |
| Antigravity CLI | [agy-wt.md](agy-wt.md) | Convention only (not installed on this machine) |

## Verification convention

Each per-agent file ends with a "Verified on this machine" section. Verified files carry a `Verified on this machine, YYYY-MM-DD` stamp inside that section; convention-only files state non-verification explicitly. Re-verifying a file is a single-file edit, not a rename.

## Related docs

- [bin/README.md](../../bin/README.md) — launcher flags, rotation, install commands.
- [docs/agent-home-directories.md](../agent-home-directories.md) — `~/.claude/` and `~/.codex/` directory layout.
- [docs/configuration.md](../configuration.md) — broader agent-toolkit configuration.
```

- [ ] **Step 2: Verify every link resolves**

```bash
grep -oE '\]\([^)]+\)' docs/wt-agents/README.md | sed 's/](//;s/)$//' | grep -v '^http' | while read link; do
  resolved=$(cd docs/wt-agents && python3 -c "import os; print(os.path.realpath('${link%%#*}'))")
  if [ ! -e "$resolved" ]; then echo "BROKEN: $link → $resolved"; fi
done
```

Expected: no `BROKEN:` output. Every per-agent link points at a file created in Tasks 1–5.

- [ ] **Step 3: Run the marketplace validator**

```bash
python3 scripts/validate_marketplace.py
```

Expected: `OK`.

- [ ] **Step 4: Commit**

```bash
git add docs/wt-agents/README.md
git commit -m "docs(wt-agents): add index"
```

---

## Task 7: Add cross-link to `bin/README.md`

**Files:**
- Modify: `bin/README.md` (one new line near the top of the `*-wt` section, just below the `### claude-wt, codex-wt, copilot-wt, pi-wt, agy-wt` heading on line 56)

- [ ] **Step 1: Re-read the current top of the `*-wt` section**

```bash
sed -n '55,70p' bin/README.md
```

Expected output begins with:

```
### claude-wt, codex-wt, copilot-wt, pi-wt, agy-wt

Interactive worktree and branch launchers for multiple AI coding agents.
```

- [ ] **Step 2: Edit `bin/README.md` to insert the cross-link**

Use the `Edit` tool with this exact replacement:

- `old_string`:
  ```
  ### claude-wt, codex-wt, copilot-wt, pi-wt, agy-wt

  Interactive worktree and branch launchers for multiple AI coding agents.
  ```

- `new_string`:
  ```
  ### claude-wt, codex-wt, copilot-wt, pi-wt, agy-wt

  Interactive worktree and branch launchers for multiple AI coding agents.

  > For per-agent credentials, configuration files, and model setup, see [`docs/wt-agents/`](../docs/wt-agents/README.md).
  ```

The new line is a blockquote (one line) so it visually separates from the surrounding prose without changing the section's structure. The path `../docs/wt-agents/README.md` is correct because `bin/README.md` lives in `bin/`.

- [ ] **Step 3: Verify the new link resolves from `bin/README.md`'s directory**

```bash
test -f bin/../docs/wt-agents/README.md && echo OK || echo BROKEN
```

Expected: `OK`.

- [ ] **Step 4: Run the marketplace validator**

```bash
python3 scripts/validate_marketplace.py
```

Expected: `OK`.

- [ ] **Step 5: Commit**

```bash
git add bin/README.md
git commit -m "docs(bin): cross-link to docs/wt-agents/ from *-wt section"
```

---

## Task 8: Final verification sweep

A single check across the whole new docs tree, run after all per-file commits, to catch anything missed by per-task checks.

**Files:** none (verification only)

- [ ] **Step 1: Confirm every new file exists and is committed**

```bash
git ls-files docs/wt-agents/ bin/README.md
```

Expected: lists `bin/README.md` plus all six new files in `docs/wt-agents/`.

- [ ] **Step 2: Run the link checker across all new files at once**

```bash
for f in docs/wt-agents/*.md; do
  echo "=== $f ==="
  grep -oE '\]\([^)]+\)' "$f" | sed 's/](//;s/)$//' | grep -v '^http' | while read link; do
    resolved=$(cd "$(dirname "$f")" && python3 -c "import os; print(os.path.realpath('${link%%#*}'))")
    if [ ! -e "$resolved" ]; then echo "BROKEN: $link → $resolved"; fi
  done
done
```

Expected: each file's section header followed by no `BROKEN:` output.

- [ ] **Step 3: Confirm no relative link in the new docs escapes the repo root**

```bash
REPO_ROOT="$(git rev-parse --show-toplevel)"
for f in docs/wt-agents/*.md; do
  grep -oE '\]\([^)]+\)' "$f" | sed 's/](//;s/)$//' | grep -v '^http' | while read link; do
    resolved=$(cd "$(dirname "$f")" && python3 -c "import os; print(os.path.realpath('${link%%#*}'))")
    case "$resolved" in
      "$REPO_ROOT"*) ;;
      *) echo "ESCAPES REPO: $f → $link → $resolved" ;;
    esac
  done
done
```

Expected: no output.

- [ ] **Step 4: Run the marketplace validator one last time**

```bash
python3 scripts/validate_marketplace.py
```

Expected: `OK`.

- [ ] **Step 5: Confirm convention-only files honestly disclose verification status**

```bash
grep -H 'Verified on this machine\|Not installed on this machine\|Convention only' docs/wt-agents/codex-wt.md docs/wt-agents/copilot-wt.md docs/wt-agents/agy-wt.md
```

Expected: each of `codex-wt.md`, `copilot-wt.md`, `agy-wt.md` matches one of "Convention only" or "Not installed on this machine" — no false "Verified on this machine" stamp.

- [ ] **Step 6: Confirm verified files carry the dated stamp**

```bash
grep -H 'Verified on this machine, 2026-06-01' docs/wt-agents/pi-wt.md docs/wt-agents/claude-wt.md
```

Expected: both files match exactly once.

- [ ] **Step 7: No commit needed**

This task only verifies. If any check above fails, return to the relevant earlier task and fix the file in place.

---

## Self-review (run by the plan author after writing, before handoff)

**1. Spec coverage:** Walk the spec section by section and confirm a task implements each.

| Spec section | Plan task |
|---|---|
| File layout (six files in `docs/wt-agents/`) | Tasks 1–6 |
| Per-file template (six sections) | Template block above; applied identically in Tasks 1–5 |
| Verification matrix | Per-task content (verified vs convention only); Task 8 Steps 5–6 enforce honesty |
| `pi-wt.md` Verified section content (worked example) | Task 1 Step 2 (full content) |
| Convention-only files — content sourcing | Tasks 3, 4, 5 |
| Updates to existing docs (`bin/README.md` cross-link) | Task 7 |
| Verification (manual) | Per-task link checks + Task 8 |
| Risks (drift, mis-statement, index maintenance) | Mitigations baked into the dated-stamp convention, "convention only" disclosure, and Task 8 Steps 5–6 |
| Cross-references to other in-flight work (sibling pi-wt-litellm spec) | Task 1 Step 2 (gotcha section links to it) |
| Out-of-scope follow-ups | Not implemented (explicit non-goals from spec) |

No gaps.

**2. Placeholder scan:** The only bracketed placeholders are in Task 2 Step 2 (`<output of `which claude` from Step 1>` and "whichever of these are present per Step 1"). These are explicitly flagged with an implementer note to replace before saving — they are not "TBD" but instructions to substitute observed values. Acceptable.

**3. Type/name consistency:** File names match across plan (e.g., `pi-wt.md` everywhere, never `piwt.md` or `pi.md`). The "Verified on this machine, 2026-06-01" date stamp is identical in pi-wt.md, claude-wt.md, and the index. The cross-link path `../docs/wt-agents/README.md` from `bin/README.md` is consistent with the `bin/` location.

---

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-06-01-wt-agents-reference-docs.md`. Two execution options:

**1. Subagent-Driven (recommended)** — I dispatch a fresh subagent per task, review between tasks, fast iteration. Each task is independent and writes one file (Tasks 1–6) or makes one small edit (Task 7), so subagent decomposition fits cleanly.

**2. Inline Execution** — Execute tasks in this session using `superpowers:executing-plans`, batch execution with checkpoints for review.

Which approach?
