# wt-agents reference docs — Design

**Date:** 2026-06-01
**Status:** Pending user review of written spec
**Scope:** New per-agent docs under `docs/wt-agents/`. One small additive line in `bin/README.md`. No changes to existing docs or code.

## Problem

`bin/README.md` documents the `*-wt` launchers' flags, rotation behavior, and install commands, but does not explain how each underlying agent is configured (where it reads credentials, where its model list lives, what auth model it uses). On this machine the pi setup is the most involved — pi runs through `~/.pi/agent/activate-litellm.sh`, fetching enterprise LiteLLM credentials and routing Anthropic models through `https://llm-gateway.nyt.net` — and that setup is currently undocumented in the repo. The Obsidian note `Pi Coding Agent with LiteLLM.md` is the only narrative source, and parts of it (generic `localhost:4000`, `anthropic/`-prefixed model IDs) describe a setup different from what is actually deployed.

## Goal

A permanent, dateless set of per-agent reference docs answering, for each `*-wt` agent: how is this agent configured, how does it authenticate, and how does the launcher invoke it. Include a verified worked example for the actual NYT LiteLLM pi setup on this machine.

## Non-goals

- Reorganize `bin/README.md`, `docs/agent-home-directories.md`, or `docs/configuration.md`. The new docs are additive.
- Change any `*-wt` script behavior. The sibling spec (`2026-06-01-pi-wt-litellm-wrapper-design.md`) handles the pi-wt LiteLLM auto-detection fix.
- Document `ai-shell`, `ai-code`, `ai-design`, or other non-`*-wt` tools.
- Introduce automated documentation tests beyond the existing marketplace validator.

## File layout

```
docs/wt-agents/
├── README.md         (index: per-agent one-liner, relevance/verification table, scope statement)
├── claude-wt.md      (verified section: this machine, dated)
├── codex-wt.md       (convention only; cites upstream Codex CLI docs)
├── copilot-wt.md     (convention only; "not installed on this machine")
├── pi-wt.md          (verified section: NYT LiteLLM worked example, dated)
└── agy-wt.md         (convention only; "not installed on this machine")
```

All filenames are dateless and stable. Date stamps live inside each file's "Verified on this machine" section so a re-verification is a single-file edit, not a rename.

## Per-file template (six sections)

1. **Overview** — what this agent is, who builds it, link to upstream docs.
2. **Installation** — install command (cite source). Same content as `bin/README.md`'s install column, but with one-paragraph context.
3. **Configuration files & locations** — where the agent reads from (`~/.pi/agent/`, `~/.claude/`, `~/.codex/`, `~/.copilot/`, `~/.antigravity/` or equivalent). Key files with one-line purpose each.
4. **Authentication & credentials** — how the agent obtains API keys / OAuth tokens. For pi: env-var substitution in `models.json`, the credential-helper pattern, `$ANTHROPIC_API_KEY` / `$LITELLM_API_KEY`.
5. **Model selection** — how the agent picks a model, including how `<name>-wt`'s passthrough interacts (`--model`, `--profile -m`, `COPILOT_PROVIDER_*` env vars, or none for agy). Per-agent quirks called out (e.g., pi requires the model ID to also exist in `~/.pi/agent/models.json` or the launcher silently falls back).
6. **Verified on this machine** — concrete observations from this machine, with date stamp `Verified on this machine, YYYY-MM-DD`. For unverified files, this section says "Not verified on the documenting machine; see upstream docs" with links.

Cross-references to `bin/README.md`, `docs/agent-home-directories.md`, `docs/configuration.md`, and upstream agent docs are inline within the relevant sections, not collected in a dedicated section.

## Verification matrix

| File | Verified section content |
|---|---|
| `claude-wt.md` | Verified. Binary path, `~/.claude/` directory layout snapshot. Auth specifics (OAuth internals in `~/.claude.json`) noted as observable-but-opaque. |
| `codex-wt.md` | Convention only. Body cites upstream Codex CLI docs. Verified section explicitly says "not verified on the documenting machine; codex is used on other machines." |
| `copilot-wt.md` | Convention only. Verified section says "not installed on this machine." |
| `pi-wt.md` | Verified. Full NYT LiteLLM worked example (see below). |
| `agy-wt.md` | Convention only. Verified section says "not installed on this machine." |

## `pi-wt.md` Verified section content (worked example)

Drawn from observations made during brainstorming (2026-06-01):

- **Binary:** `~/.asdf/installs/nodejs/25.8.0/bin/pi` (npm-installed, raw).
- **Wrapper script:** `~/.pi/agent/activate-litellm.sh`. Fetches LiteLLM key via `/usr/local/bin/litellm-credential-helper.sh`, exports `ANTHROPIC_API_KEY` and `LITELLM_API_KEY`, sets `ANTHROPIC_DEFAULT_OPUS_MODEL=claude-opus-4-7` and `ANTHROPIC_CUSTOM_HEADERS="x-litellm-tags: cc_settings:6ebaf51"`. Injects `--provider anthropic --model claude-opus-4-7` only when neither `--model` nor `--provider` is present in the args. Then `exec pi "$@"`.
- **Interactive entry point:** `pi()` zsh function in `~/.zshrc` (lines 161–165) calling `activate-litellm.sh "$@"`.
- **Provider config:** `~/.pi/agent/models.json` declares one provider, `anthropic`, named "Anthropic (LiteLLM proxy)", with `baseUrl: https://llm-gateway.nyt.net`, `api: openai-completions`, `apiKey: $ANTHROPIC_API_KEY` (env-var substitution at runtime), and four model IDs: `claude-opus-4-7`, `claude-opus-4-6`, `claude-sonnet-4-6`, `claude-haiku-4-5`. Note the bare model IDs (no `anthropic/` prefix), differing from the Obsidian note's generic example.
- **Credential helper:** `/usr/local/bin/litellm-credential-helper.sh` (root-owned, executable, ~5.9KB). Implementation details out of scope; treated as opaque dependency.
- **Settings:** `~/.pi/agent/settings.json` records last-seen pi version (`0.78.0`), theme, and one installed package (`https://github.com/obra/superpowers`).
- **Documented gotcha (pi-wt-specific):** `pi-wt` is a bash script. Bash scripts do not inherit zsh shell functions, so `pi-wt`'s `exec pi …` call performs PATH lookup and finds the raw npm binary, not the wrapper. Result: pi launches without LiteLLM credentials and reports "No models available." This is fixed by the sibling spec `2026-06-01-pi-wt-litellm-wrapper-design.md` (auto-detect `~/.pi/agent/activate-litellm.sh` and exec it instead). Cross-link from `pi-wt.md` to that spec.
- **Background context to include (from Obsidian note):** brief paragraph on pi's provider system (provider-agnostic, configurable via `models.json` or extension), env-var substitution syntax, the credential-refresh-on-launch pattern, and why an OpenAI-compatible (`openai-completions`) API surface routes through LiteLLM to Anthropic. Anywhere the Obsidian note's generic example diverges from the deployed reality (URL, model-ID prefix, optional extension-based override), the doc records the deployed reality and notes the alternative as background.

## Convention-only files — content sourcing

For `codex-wt.md`, `copilot-wt.md`, `agy-wt.md` the body sections (1–5) are written from upstream sources only:

- **codex:** OpenAI Codex CLI repo and docs. Mention `~/.codex/config.toml`, profiles, `--profile` and `-m` flag behavior. Per-agent quirk to call out: `codex-wt` passes `--profile ollama-launch -m <model>`, which means a profile named `ollama-launch` must exist in `~/.codex/config.toml` for rotation to work; without it, codex errors out.
- **copilot:** GitHub Copilot CLI docs and the Ollama-Copilot integration page (already linked from `bin/README.md`). Cover the `COPILOT_PROVIDER_BASE_URL` / `COPILOT_PROVIDER_API_KEY` / `COPILOT_PROVIDER_WIRE_API` / `COPILOT_MODEL` env-var passthrough and the `native:copilot → --model auto` mapping.
- **agy:** Antigravity CLI install page. Note the absence of `*-wt` model passthrough (agy-wt rotates state but doesn't pass a model flag).

Each file's Verified section explicitly states verification status: codex says "convention only; used on other machines, not this one"; copilot and agy say "not installed on this machine."

## Updates to existing docs

- **`bin/README.md`:** Add one short line near the top of the `*-wt` section pointing at the new directory: *"For per-agent credentials, configuration files, and model setup, see [`docs/wt-agents/`](../docs/wt-agents/README.md)."* No content moved out of `bin/README.md` (per the additive policy from brainstorming).

## Verification (manual)

Per `CLAUDE.md` ("No formal test suite — validate structure + manual verification only"):

1. **Structural lint:** All cross-links in new docs resolve to existing files. No relative links escape the repo root.
2. **Marketplace validator:** `python3 scripts/validate_marketplace.py` — passes (no plugin changes, but standard practice).
3. **Spot-check the pi-wt verified section:** every claim about this machine matches `~/.pi/agent/`, `~/.zshrc`, `/usr/local/bin/litellm-credential-helper.sh` as observed during brainstorming. Re-verify before commit.
4. **Spot-check the claude-wt verified section:** confirm binary path, `~/.claude/` layout against this machine.
5. **Confirm convention-only files honestly disclose verification status.**

## Risks

- **Drift.** The pi-wt and claude-wt verified sections will go stale as the LiteLLM gateway URL, the zsh function, the credential helper path, or the Claude Code home layout evolve. Mitigation: dated "Verified on this machine, YYYY-MM-DD" stamp inside the section. Updating is a single-file edit.
- **Mis-statement risk for convention-only docs.** Writing about copilot and agy without running them invites errors. Mitigation: keep convention-only sections short, cite upstream docs at every claim, label the verified section unambiguously.
- **Index file maintenance.** A new agent (or removal of one) requires editing `docs/wt-agents/README.md`. Acceptable — `bin/README.md` already has this maintenance burden in its agent-flags table.

## Cross-references to other in-flight work

- `docs/superpowers/specs/2026-06-01-pi-wt-litellm-wrapper-design.md` (sibling spec) — referenced from `pi-wt.md`'s Verified section under the documented gotcha. The two pieces of work are tightly coupled but independently shippable; this spec does not block on the code change landing.

## Out-of-scope follow-ups

- If `claude-wt`'s `--model` passthrough or `~/.claude/` auth model changes substantially in a future Claude Code version, that's a verified-section refresh.
- If a future agent (e.g., `gemini-wt`) joins, add a sixth file to `docs/wt-agents/` and update the index.
- The Obsidian note `Pi Coding Agent with LiteLLM.md` lives outside this repo and is not synced. The pi-wt.md doc captures the parts relevant to this machine; the Obsidian note remains the broader narrative reference for users with a different LiteLLM topology.
