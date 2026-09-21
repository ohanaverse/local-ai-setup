# wt-owned LiteLLM management

Status: draft for review (brainstorming session 2026-09-21). Source of truth
for the implementation plan; do not diverge without updating this doc.

Next phase of the "migrate away from modelman" effort (sub-projects 4a-4c
ported model start/stop to Go; see
`2026-09-20-wt-lifecycle-engine-design.md`).

**Supersedes** the ownership split in
`2026-09-10-litellm-control-design.md` ("modelman is the single control plane
for LiteLLM"). The protocol-negotiated routing half of that spec is unchanged.
**Closes** the known limitation in the 4a spec: "a started model works in
LiteLLM mode only if modelman already exposed it there."

## Problem

`wt start` / `wt stop` (#137, #138) start and stop the provider but never
touch LiteLLM. `modelman start` does three more things: it writes the model's
`model_list` row in `~/.config/litellm/config.yaml`, flips `exposed` in
`modelman.toml`, and restarts the proxy. A model started through wt therefore
has no route, and clients get:

```
400: Invalid model name passed in model=mtplx/Youssofal--Qwen3.6-35B-A3B-...
```

Observed 2026-09-21: MTPLX serving the 3.6-35B on :8003, `exposed = false`, no
row in `config.yaml`. The reverse also occurs: `Qwen3.8-27B` has a stale row
with `exposed = false`. wt cannot fix this itself today because wt never writes
`config.yaml`, `modelman.toml`, or restarts the proxy (`wt/CLAUDE.md`).

## Decisions

1. **Scope: A + C, and B for local models only.** wt owns the `config.yaml`
   sync and proxy restart (A) and the `[litellm]` routing state and its CLI
   (C). The per-model `exposed` flag for cloud models stays modelman-owned.
2. **Go is the only implementation.** modelman deletes its Python write path
   and calls wt primitives (`wt litellm ...`) via subprocess. No dual
   implementation, so no drift.
3. **`[litellm]` state moves to wt's `~/.config/agent-wt/config.toml`.**
   Avoids a comment-losing Go writer in the Python-owned `modelman.toml`.
4. **For local models, `config.yaml` membership is the source of truth.**
   wt start adds the row, wt stop removes it; wt never writes the
   `modelman.toml` flag. `wt litellm sync` reconciles.
5. **Cloud models keep their flow:** modelman toggles the flag, then calls
   `wt litellm expose|unexpose`.

## Design

### Package `wt/internal/litellm`

| Unit | Job | Ports from (`modelman/src/modelman/litellm.py`) |
|---|---|---|
| `policy` | Provider → LiteLLM mapping (prefix, fixed_model, api_key, secret_ref, cloud) | `PROVIDER_POLICIES`, `provider_policy` |
| `entry` | Registry model + provider → `model_list` row, incl. derived pricing `model_info` merged under the model's own `model_info` | `build_model_list_entry`, `_pricing_model_info` |
| `configfile` | Round-trip edit of `config.yaml` preserving comments and unknown keys: set/remove row by `model_name`, preserve `additional_drop_params` and `use_chat_completions_api` on replace, value-enforced settings, atomic write (temp + rename), lock file serializing read-modify-write | `set_exposed`, `remove_exposed`, `ensure_litellm_settings`, `save_litellm_config` |
| `restart` | `launchctl kickstart -k gui/$(id -u)/local.litellm.proxy` fallback; override via `WT_LITELLM_RESTART_CMD`, falling back to legacy `MODELMAN_LITELLM_RESTART_CMD`. Returns warnings, never errors. | `restart_litellm_proxy` |
| `reconcile` | Diff desired routed ids against current rows; one write and one restart, and none when nothing changed | `apply_expose_queue` |

New dependency: a Go YAML library with comment-preserving round trip
(`gopkg.in/yaml.v3` Node API). wt has only `BurntSushi/toml` today.

Exposure gate (ported from `_validated_entry`): unknown model, unknown
provider, native provider (never routed), unmapped provider, and not-ready
non-cloud model are all rejected. wt already reads the registry and
`modelman.toml` `ready` read-only, which is all the gate needs.

Idempotence: an expose or unexpose that changes nothing writes nothing and does
not restart the proxy.

### CLI (cobra, under `wt litellm`)

| Command | Behavior |
|---|---|
| `expose <id>...` / `unexpose <id>...` | Batch; one write and one restart. `--dry-run` runs the gate only. `--json` reports per-id outcomes and `warnings`. |
| `sync` | Make `model_list` match reality: routes for running local models added, routes for stopped local models removed. Cloud rows untouched. |
| `list [--json]` | Routed ids from `config.yaml`. |
| `status [--json]`, `on`, `off`, `set --url --api-key` | The `[litellm]` routing state (replaces `modelman litellm ...`). |

Exit codes: `0` when the config was written or was already correct, even if
the restart failed (warning on stderr / `warnings` array). `1` means nothing
was changed. Partial batches apply the valid ids and report the rest per id.

### wt integration

`wt start` calls `Expose(id)` after a successful start; `wt stop` calls
`Unexpose(id)` after a successful stop. The TUI start flow and `wt smoke` use
the same two calls. A LiteLLM failure prints a warning naming the manual fix
and does **not** fail the start or stop, because the model state is correct
and only the route is missing. One proxy restart per start or stop.

### Routing state migration

`[litellm]` (`enabled` / `url` / `api_key`) moves to wt's `config.toml`. On
first load, if `config.toml` has no `[litellm]` and `modelman.toml` has one,
wt copies it once. The migration is automatic and idempotent; the old table is
left in place and ignored. A malformed table or failed migration warns and
falls back to LiteLLM off; it never blocks launching an agent.
`config.Config` reads the value from wt's file instead of `modelman.toml`.

### modelman changes

- New `wt_bridge.py`: single subprocess wrapper for `wt litellm ...`. If `wt`
  is not on PATH, fail before any state change with "install wt (`make
  install`)".
- `litellm.py`: delete row building, YAML editing, and restart. `expose_model`,
  `unexpose_model`, `apply_expose_queue` keep their signatures and flip the
  `exposed` flag **only after** the `wt` call succeeds.
- The TUI expose gate calls `wt litellm expose --dry-run` instead of a Python
  copy of the policy table.
- `modelman litellm status|on|off|set` become passthroughs. Benchmark and
  `usage` `[litellm]` reads go through `wt litellm status --json`.
- The EXPOSED column for local models reads `wt litellm list --json`.
- `MODELMAN_LITELLM_RESTART_CMD` keeps working (wt honors it as a fallback).

### Cross-language contract

A golden fixture set in `docs/contracts/` (`config.yaml`, `registry.toml`,
expected rows). Go tests read it. Python tests read it only to assert what
modelman sends to wt. This matches the existing `docs/contracts/` pattern.

## Testing

Every test carries an intent comment (repo rule).

- Go, per unit, against the golden fixtures. The cases that matter: comment
  preservation; preserved `additional_drop_params` / `use_chat_completions_api`
  on re-expose; idempotent no-write / no-restart; native rejection; malformed
  `model_list` refused; unreadable `config.yaml` never overwritten; concurrent
  writers serialized by the lock.
- Go integration with a fake restart command; no test may kickstart the real
  proxy.
- `wt start/stop` against a stubbed lifecycle: expose/unexpose only after
  success; LiteLLM failure only warns.
- Python: stub the `wt` subprocess; assert argv and flag-after-success
  ordering. Existing `conftest.py` autouse fixtures extended to block real
  `wt` and `launchctl` calls.

## Phasing

Each phase is a mergeable PR. The reported bug is fixed after phase 2.

1. `internal/litellm` plus `wt litellm expose|unexpose|sync|list`, with the
   golden fixtures.
2. Wire `wt start/stop`, the TUI start flow, and `wt smoke`. **Fixes the bug.**
3. Move `[litellm]` state into wt's config with migration; add
   `wt litellm status|on|off|set`.
4. modelman delegates (`wt_bridge`, delete Python write path), and docs
   updates: `wt/CLAUDE.md` (ownership statement), `modelman/CLAUDE.md`, and the
   guides. Run `git grep -n "exposed = " docs/guides/` before and after, per
   the root `CLAUDE.md`.

## Out of scope

- Moving the cloud-model `exposed` flag or the rest of the model catalog into
  wt.
- Changing protocol negotiation or the Forced-route behavior.
- Starting or stopping the proxy outside the restart-after-write path.

## Open questions

None blocking. Decide during planning: exact JSON schema for `--json`
outputs; whether `sync` should also run automatically at wt startup (default:
no, explicit only).
