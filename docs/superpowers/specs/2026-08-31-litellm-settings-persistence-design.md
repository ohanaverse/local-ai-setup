# Modelman LiteLLM Settings Persistence — Design

**Status:** Proposed (2026-08-31). Not yet planned or implemented.

**Scope:** modelman side only. Protect the non-`model_list` sections of
`~/.config/litellm/config.yaml` (`litellm_settings`, `general_settings`,
`router_settings`) from being lost by modelman's own writes, so
agent-launcher-required settings survive `expose` / `unexpose` (and the
apply-queue path behind the TUI).

Companion to `2026-08-31-modelman-litellm-proxy-reconcile-design.md`, which
covers restarting the proxy after model_list changes. That spec restarts the
proxy; this one makes sure the config it restarts with is still correct.

## Problem

Routing wt-managed agents through the LiteLLM proxy requires settings that
wt cannot set itself — they must live in `config.yaml`:

- `litellm_settings.drop_params: true` — required for copilot, which sends
  `parallel_tool_calls` (and other params) that the ollama backend rejects
  with `400 UnsupportedParamsError`.
- `litellm_params.additional_drop_params: ["reasoning_effort"]` (per model) —
  the only workaround on LiteLLM 1.98.0 for codex, whose `/v1/responses`
  `reasoning` object crashes the responses→`ollama_chat` bridge with
  `TypeError: unhashable type: 'dict'` (BerriAI/litellm#37452; fix PRs
  #37465/#37467 not yet released).

Both were applied by hand on 2026-08-31 to unblock the launch matrix
(see wt's `docs/wt-agents/litellm-troubleshooting.md`); the codex workaround
was live-verified the same day (direct `/v1/responses` probe 200 where it
crashed, `codex exec` one-shot through wt green). But only the one tested
entry carries `additional_drop_params`; **every** `ollama_chat/*` entry
needs it for codex to work with that model, and hand-maintaining settings
across modelman's expose / unexpose rewrites is exactly the job
modelman should own. The live config confirms the drift: 6 of 7
`ollama_chat/*` entries are missing the key. If any modelman code path rewrites the file from a
partial in-memory shape — dropping keys it does not know about — these
settings vanish silently and the affected agents break again on next use,
with no link back to a modelman action.

## Goal

A modelman write to `config.yaml` may change only what it intends to change
(usually rows in `model_list`). All other top-level sections and unknown keys
pass through untouched. modelman additionally **owns** a small curated set of
launcher-required settings and ensures their presence on every write
(idempotently), so launchers never break because a hand-applied setting was
regenerated away.

## Decisions

1. **Read-modify-write, never regenerate.** Every modelman write to
   `config.yaml` starts from the parsed existing document and mutates only
   targeted keys. No code path serializes a config built from modelman's own
   state alone. A round-trip test pins this: write config with foreign
   settings → run modelman write → foreign settings still present.
   *Audit (2026-08-31): the three existing writers (`expose_model`,
   `unexpose_model`, `apply_expose_queue`) already follow this pattern —
   load, mutate `model_list` only, save the whole parsed document back.
   Decisions 1/2 therefore already hold semantically; the new work is the
   ensure (Decision 3), its wiring (Decision 4), no-op save/restart skips,
   and comment preservation (Decision 5).*

2. **Preserve unknown keys verbatim.** `litellm_settings`,
   `general_settings`, `router_settings`, and any other top-level mapping
   modelman does not own are carried through as-is. modelman claims only
   `model_list` (and whatever flag files/keys it explicitly documents).

3. **Curated settings are owned, not just preserved.** modelman knows the
   launcher-required settings and *ensures* them (add when missing, leave
   alone when correct, never "helpfully" delete what it did not write):

   | Setting | Where | Why | Rule |
   |---|---|---|---|
   | `drop_params: true` | `litellm_settings` (global) | copilot sends `parallel_tool_calls` → `400 UnsupportedParamsError` without it | **value-enforced**: set to `true` when missing or not `true` |
   | `additional_drop_params: ["reasoning_effort"]` | entry `litellm_params` | codex crashes the responses→`ollama_chat` bridge (`BerriAI/litellm#37452`) | **presence-based**: add when missing; an existing key (any value, incl. an extended list or explicit `[]`) is left untouched |

   The two rules differ deliberately. `drop_params` exists solely for the
   copilot failure mode, so any other value is a launcher break waiting to
   happen — modelman enforces the value. `additional_drop_params` may
   legitimately carry more (or fewer, deliberately) entries than the
   workaround requires, so the key's presence marks the entry as
   user-managed and modelman adds only what's missing. An escape hatch for a
   deliberate `drop_params: false` is deferred (YAGNI): today the working
   value is unconditionally `true`, and anyone wanting strict param errors
   can revisit this rule then.

   The codex rule is **derived from the bridge prefix**, not from a registry
   field: the crash is a property of the `ollama_chat/*` transformation
   itself, so every entry routed that way needs the key — a per-model
   registry field would force modelman to know what users already expressed
   via the prefix. Entries modelman did not write are covered too: the
   ensure runs over the whole parsed `model_list`, not just newly created
   rows. Idempotent: matching entries are left untouched, and a run that
   changes nothing performs no save (and no proxy bounce).

   Lifecycle: when a litellm release ships the #37452 fix (PRs #37465/#37467
   — as of 2026-08-31 still open, PyPI latest = the crashing 1.98.0), the
   workaround can be retired by deleting the rule + stripping the key on
   ensure. Until then it stays. Dropping `reasoning_effort` is harmless
   after the fix (thinking just reverts to the model default), so removal
   is cleanup, not urgency.

4. **Ensure runs inside the existing write paths.** A single
   `ensure_litellm_settings(config) -> bool` (parsed-document-in,
   changed-out) is called by every path that saves `config.yaml`. That is
   exactly three writers, all in `litellm.py`: `expose_model`,
   `unexpose_model`, and `apply_expose_queue` (`sync` and the TUI's
   background reconcile write state/registry only, never config.yaml).
   It returns whether it changed anything, feeding the changed-detection
   below.

   **Changed-detection and restart wiring.** "Changed" means the parsed
   document is deep-unequal before vs. after the operation + ensure
   combined. Save runs iff changed (an idempotent re-expose of a byte-
   identical entry no longer rewrites the file); the sibling spec's proxy
   restart runs iff changed **or** a state flag flipped. This also fixes a
   latent gap: today a re-expose that rewrites an entry with *new* content
   (e.g. a changed `api_base`) but leaves the flag set skips the restart,
   leaving the proxy stale. In `apply_expose_queue`, save and restart run
   iff any queue item succeeded **or** the ensure changed the document — a
   fully-failed queue still persists the settings fix.

5. **Comments/formatting are preserved via `ruamel.yaml` round-trip.** The
   live config carries hand-written "why" comments (e.g. the three-line
   `drop_params` explanation) that a PyYAML re-serialize would silently
   destroy — the same silent-loss failure mode this spec exists to prevent.
   `load_litellm_config` / `save_litellm_config` switch to ruamel's
   round-trip load/dump (new pinned dependency); the atomic temp-file +
   rename + permission-preserving logic is unchanged. Untouched entries and
   foreign sections come back byte-identical, which makes the
   "unchanged ⇒ no save" tests literally checkable.

## Testing

- **Round-trip preservation** — config containing foreign settings →
  modelman write → foreign settings still present, and hand-written
  comments survive the write byte-identical (pins Decisions 1/2/5).
- **Ensure rules** — entry with `ollama_chat/` prefix gains
  `additional_drop_params` when missing; entry already carrying the key is
  left untouched and reports no change (byte-identical under ruamel);
  non-`ollama_chat` entries are never touched; `litellm_settings.drop_params`
  set to `true` when missing, corrected when `false`; a second ensure run
  is a no-op (no save, no restart).
- **Wiring** — expose / unexpose / apply_expose_queue each call the ensure
  before save; a no-op operation on an unchanged config performs no save and
  no restart; a config-changing ensure alone (fully-failed queue) still
  saves and restarts; an entry rewrite with new content restarts the proxy
  even when the flag doesn't flip.

## Explicitly Out of Scope

- Proxy restart/reconciliation after writes — covered by the sibling spec
  (`2026-08-31-modelman-litellm-proxy-reconcile-design.md`). Note the two
  interact: a settings-changing write may also warrant a reconcile.
- wt-side changes. wt already treats `config.yaml` as read-only and documents
  the settings it depends on; enforcement lives in modelman.
- Upgrading or patching LiteLLM to remove the need for
  `additional_drop_params` (tracked upstream in BerriAI/litellm#37452).

## Resolved Questions

- **Does any current modelman path actually regenerate (rather than mutate)
  `config.yaml`?** No — audited 2026-08-31: all three writers load, mutate
  `model_list`, and save the parsed document back. The remaining gaps the
  ensure and changed-detection close: unconditional saves on no-op
  unexposes, restarts keyed only on flag flips, and comment loss via
  PyYAML.
- **Should the global `drop_params` ever need to be `false`?** Deferred. The
  value is enforced as `true` until a real need appears; revisit the rule
  (and add an escape hatch) if someone wants strict param errors.