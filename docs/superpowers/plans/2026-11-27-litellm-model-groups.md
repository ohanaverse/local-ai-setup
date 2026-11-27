# LiteLLM model groups (step A) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Create six sticky, randomly-pooled fallback groups (`alias/claude-fable|opus|sonnet|haiku`, `alias/open-pro|open-flash`) from registry models, with session affinity on.

**Architecture:** Aliases (`wt-model alias set`) hold the tiers: `order` is the tier, equal order is a random pool. Affinity is a hand-written `router_settings` block in `config.yaml` plus an `x-litellm-session-id` header that a shell function adds before `wt` launches claude. No Go code. The registry and `config.yaml` are machine state under `~/.config`; the repo gets only the docs.

**Tech Stack:** `wt-model` CLI, LiteLLM (installed, 1.109.x), zsh, Markdown guides.

**Spec:** `docs/superpowers/specs/2026-11-27-litellm-model-groups-design.md`

## Global Constraints

- Work in a worktree (`wt -W litellm-model-groups`); never commit in the primary checkout. `make pre-pr` before any PR (run with `run_in_background`, read the exit from the log).
- Claude goes through the subscription (`claude-subscription` provider, keyless), never `ANTHROPIC_API_KEY`. No Anthropic model through OpenRouter. No local models. No nested groups.
- `minimax-m3.1-flash` is in no pool (it fails tool calls).
- `config.yaml` is written only by `wt`: the only hand edit is one top-level `router_settings:` block; if `wt-model litellm sync` strips it, STOP and report (do not add Go code).
- Every `alias set` runs with `--dry-run` first. Give every `--target` an explicit `:ORDER` (left out, the order is the flag's position, which would make pools sequential).
- Do not remove `alias/fable-mixed`.
- A pool target whose window is under 1000000 is dropped from the pool, not accepted into a smaller alias window.
- Guides show the command that reveals state, never live counts or snapshots (CLAUDE.md "Guides never embed live model state").

## Review Focus

- The header function overwrites `ANTHROPIC_CUSTOM_HEADERS`, breaking subscription passthrough: the function must append (Task 4 pins it).
- `alias set` without `:ORDER` on every target silently makes pools sequential, not random (Task 2 asserts every target's order in `alias list --json`).
- A sync or `kickstart` restart drops the `router_settings` block or loses pins (Task 3 re-reads the file after a sync).
- A pool member that is not routed (a model removed from Ollama's catalog) leaves a tier empty (Task 2 checks ROUTED and each tier's row count in `litellm list`).
- Failover works for the first request only and the session then bounces back to the subscription (Task 5 checks the pin holds after failover).

## File Structure

- Machine state, not committed: `~/.config/local-ai/registry.toml` (sonnet model, six `[[aliases]]`), `~/.config/litellm/config.yaml` (`router_settings`), `~/.zshrc` (header function).
- Committed: `docs/superpowers/specs/2026-11-27-litellm-model-groups-design.md`, this plan, `docs/guides/04-litellm-config.md` (new worked example in section 7).

---

### Task 1: Preflight in a worktree

**Files:** none changed.

**Interfaces:**
- Produces: a worktree on a fresh branch holding the spec and plan; a known-good proxy and installed `wt-model`.

- [ ] **Step 1:** From the primary checkout run `wt -W litellm-model-groups`; copy the spec and this plan into the worktree if not present (they are untracked in the root); commit both as `docs: litellm model groups spec and plan`.
- [ ] **Step 2:** Confirm the install supports aliases: `wt-model alias list` exits 0 and shows `alias/fable-mixed`; `wt-model litellm status` shows the proxy answering. If the installed `wt-model` is older than `[[aliases]]` support, run `make install` and restart the `wt-model serve` LaunchAgent (`launchctl bootout` then `bootstrap`).
- [ ] **Step 3:** Confirm the twelve pool ids exist and every window is at least 1000000: `wt-model model list --json` filtered to the ids in the spec's pool table, plus `modeltiers show --wide` for the window column. Expected: twelve present, none under 1000000. Drop any that fail the window rule and note it.

### Task 2: Register sonnet and create the six aliases

**Files:** machine state only (`registry.toml` via `wt-model`).

**Interfaces:**
- Consumes: the pool ids and tiers in the spec (pro/flash pools; order 1 subscription, 2 Ollama pool, 3 OpenRouter pool; for `open-*`, 1 Ollama pool, 2 OpenRouter pool).
- Produces: aliases `alias/claude-fable`, `alias/claude-opus`, `alias/claude-sonnet`, `alias/claude-haiku`, `alias/open-pro`, `alias/open-flash`, each ROUTED `yes`, window 1000000.

- [ ] **Step 1:** `wt-model model add claude-subscription claude-sonnet-5-5 --family claude --context-window 1000000`; expect it listed in `wt-model model list`.
- [ ] **Step 2:** For `open-pro`, run `wt-model alias set alias/open-pro --dry-run` with the pro Ollama targets each `:1` and the pro OpenRouter targets each `:2`. Expected: the dry run lists one row per target with the right order and no warning that blocks it. Then run it without `--dry-run`.
- [ ] **Step 3:** Repeat step 2 for `open-flash` (flash pools).
- [ ] **Step 4:** Create `claude-fable` and `claude-opus` (`claude-fable-5-2:1` / `claude-opus-5-5:1`, pro Ollama pool `:2`, pro OpenRouter pool `:3`) and `claude-sonnet` and `claude-haiku` (`claude-sonnet-5-5:1` / `claude-haiku-5-5:1`, flash pools `:2` and `:3`), each dry run first.
- [ ] **Step 5:** Verify: `wt-model alias list --json` shows six new aliases with the exact targets and orders from the spec (assert pool members share an order, and every target has an explicit one), CONTEXT 1000000, `routed` true; `wt-model litellm list` shows the rows per alias. If the subscription rows are missing, the hazard check is blocking them: report it with the message `wt-model litellm sync` prints, do not edit around it.

### Task 3: Turn on session affinity in the proxy

**Files:** Modify `~/.config/litellm/config.yaml` (add a top-level `router_settings:` block).

**Interfaces:**
- Produces: a proxy that pins by `x-litellm-session-id` per model group and keeps the pin after failover.

- [ ] **Step 1:** Read the installed LiteLLM's option names for this feature in `deployment_affinity_check.py` (`~/.local/share/uv/tools/litellm/lib/python*/site-packages/litellm/router_utils/pre_call_checks/`) and `litellm/router.py`; confirm `optional_pre_call_checks: ["session_affinity"]`, `session_affinity_repin_on_failover` and the TTL option's exact name and unit. Write the three names you confirmed into the Task 6 guide text.
- [ ] **Step 2:** Add `router_settings:` with those options (affinity TTL 14400 seconds) after `litellm_settings:`. Do not add `routing_strategy` or `enable_priority_based_load_balancing` unless step 5 shows aliases need them.
- [ ] **Step 3:** Run `wt-model litellm sync --dry-run`, then `wt-model litellm sync`; re-read `config.yaml`. Expected: the `router_settings` block is still there byte for byte. If it is gone: STOP and report to the user (the spec says no Go code in step A).
- [ ] **Step 4:** Restart the proxy the documented way (guide 08 section 5) and wait for `/health/liveliness`; check `~/.litellm.err.log` for a startup error naming `router_settings`.
- [ ] **Step 5:** Check that order and affinity cooperate: send ten requests with one session header to `alias/open-flash` and ten with none (through the gateway, tiny prompts, see Task 5 for the request recipe); the first ten hit one deployment, the second ten more than one. If order alone does not give tiered selection, then set `enable_priority_based_load_balancing: true` and repeat; record which was needed.

### Task 4: Session header function for `wt` (skipped as built: LiteLLM reads Claude Code's own `x-claude-code-session-id`; see the spec, step 4)

**Files:** Modify `~/.zshrc` (machine state); document in Task 6.

**Interfaces:**
- Produces: a `wt` shell function that exports a per-launch `x-litellm-session-id` line, appended to any existing `ANTHROPIC_CUSTOM_HEADERS`, then runs the real `wt`.

- [ ] **Step 1:** Write the function in `~/.zshrc`: `wt()` sets `ANTHROPIC_CUSTOM_HEADERS` to the existing value (if any), a newline, then `x-litellm-session-id: cc-<lowercase uuid>` (header lines are newline-separated), and runs `command wt "$@"`. It never assigns without keeping the existing value.
- [ ] **Step 2:** Verify it in a new shell: with `ANTHROPIC_CUSTOM_HEADERS="x-litellm-tags: keep"` set first, a stub `command wt` that prints the variable shows both lines. Expected: the original line kept, one new session line.
- [ ] **Step 3:** Verify the gateway forwards it: send one request to the model server's gateway for `alias/open-flash` with the header and check LiteLLM's spend log row for the session id (`wt-model` spend log query in guide 07, or the Admin UI). Expected: the id appears; if absent, find where the gateway drops it (`wt/internal/modelgateway/gateway.go`) and report; do not patch Go.

### Task 5: Behaviour checks

**Files:** none (scratch notes in the scratchpad directory).

**Interfaces:**
- Consumes: Tasks 2-4.

- [ ] **Step 1 (pool spread):** 20 tiny requests to `alias/open-pro` with no session id; expected: more than one Ollama pro model served, none from OpenRouter while Ollama answers. Read the served model from the spend log.
- [ ] **Step 2 (affinity):** 5 requests to `alias/open-pro` with one session id; expected: one deployment for all five.
- [ ] **Step 3 (failover stand-in):** create a temporary `alias/zz-failover-test` with order 1 a registry model that is not running (a stopped local model) and order 2 one flash Ollama model; a request is answered by the order 2 model; a session id then stays on it. Remove the alias afterward with `wt-model alias rm alias/zz-failover-test --yes`.
- [ ] **Step 4 (launch):** `wt` launch of claude with the opus slot named `alias/claude-opus` (slot profile via `wt profile slots`, or the picker): one prompt answers, and the spend log shows the subscription model served it while the subscription is available. Do not force a real rate limit; record any real failover seen as an observation.
- [ ] **Step 5:** Write the observed results (served model per request, no keys, no tokens) into the scratchpad file and summarize in the final report.

### Task 6: Document and gate

**Files:**
- Modify: `docs/guides/04-litellm-config.md` (section 7, after the Install-order bullet)

**Interfaces:**
- Consumes: the confirmed option names from Task 3 and the function from Task 4.

- [ ] **Step 1:** Add a subsection "Worked example: tiered groups with session affinity" to section 7: the six alias names and their tiers as a table, one `alias set` example showing explicit `:ORDER` and equal-order pools, the `router_settings` block with the confirmed names, the `wt` header function (appending form), and the commands that show state (`wt-model alias list`, `wt-model litellm list`). State that pi and codex get affinity later. Name no live counts.
- [ ] **Step 2:** `make lint` (shell, skills, links). Expected: pass.
- [ ] **Step 3:** Commit (`docs(guides): tiered alias groups with session affinity`) and run `make pre-pr` in the background; read the exit from the log. Expected: exit 0.
- [ ] **Step 4:** Push and open the PR with `gh pr create --body-file`; the body says `Refs` (no closing keyword) for any issue it names, and lists what changed on the machine outside the repo (sonnet model, six aliases, `router_settings`, the `wt` function). Merging needs `gh pr merge --squash --admin`; wait for the user.

## Self-review

- Spec coverage: groups and tiers (Task 2), subscription not API (Constraints, Task 2 step 5), affinity and header (Tasks 3-4), `fable-mixed` kept (Constraints), minimax excluded (Constraints), failover stand-in and stop-if-stripped rules (Tasks 3, 5), docs (Task 6), step B excluded.
- Open item for the builder: whether order alone yields tiered selection (Task 3 step 5).
