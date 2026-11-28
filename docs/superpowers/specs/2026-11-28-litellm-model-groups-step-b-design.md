# LiteLLM model groups, Step B: `wt` owns session affinity, and the groups become reproducible

Date: 2026-11-28. Follows `2026-11-27-litellm-model-groups-design.md` (Step A, PR #751), whose "Step B" section asked for its own design pass. Size: a project, shipped as two PRs (B1, then B2).

## Intent and why

Step A built six groups (`alias/claude-fable|opus|sonnet|haiku`, `alias/open-pro|open-flash`) and turned on LiteLLM's session affinity with a hand-written `router_settings` block in `~/.config/litellm/config.yaml`. That left two things hand-made on one machine:

1. the affinity settings, which `wt` does not know about (a fresh `config.yaml` has none, so a fresh machine has no sticky routing and the first `wt` write is silent about it);
2. the six aliases themselves, each a separate `wt-model alias set` run.

Step B makes both reproducible: `wt` writes the affinity settings itself, and the aliases can be created from one reviewable file.

## What the recon found (it changes the original Step B)

The original Step B said "`wt` ... sets the session header when it launches an agent". The recon over the installed LiteLLM 1.109.1 and the repo shows that is mostly not needed, and for claude it would do harm:

- claude, codex and opencode already send a stable per-conversation id that LiteLLM reads (spend logs: claude 10,916 requests in 265 sessions). The gateway forwards every non-credential header unchanged (`modelgateway/gateway.go`, deny list only), but no test pins that.
- LiteLLM ranks `x-litellm-session-id` above `x-claude-code-session-id`, so a wt-set header would pin a whole launch to one deployment instead of each conversation.
- pi sends no id (1,597 requests, 1,597 "sessions"). Fixing that needs a change in how `wt` writes pi's config (`piCompat` has no headers or affinity field), which is the open deferred item #6. copilot's behavior is undetermined; agy never reaches the gateway.
- `deployment_affinity_ttl_seconds` and `session_affinity_repin_on_failover` are startup-only router options, and LiteLLM lets a key in `config.yaml` shadow the database copy, so the mechanism is a file write plus a proxy restart, not the model API.
- A pin beats `order`; after a 429 the pin moves to the fallback only when the original deployment is actually in cooldown, and with `repin_on_failover` it then stays there for up to the TTL (sliding), even after the cooldown (5 s default) ends. That matches "one session, one model" and is the existing Step A behavior; B1 keeps those values.

## Goals, non-goals, anti-goals

Goals:
- (B1) `wt` writes the three affinity keys into `config.yaml`'s `router_settings` on every config write, only where absent; a hand-written value is the opt-out.
- (B1) a test pins that the gateway forwards non-credential session headers, and one pins that a sync keeps a hand-written `router_settings`.
- (B2) `wt-model alias apply <file>` creates or updates many aliases from one file in one registry write and one route sync, with a dry run.
- (B2) `wt-model alias list --toml` prints the existing aliases in exactly the shape `apply` reads, so a machine's groups can be captured and replayed.

Non-goals:
- No session header for claude, codex or opencode (they already send their own). No pi session id (deferred behind #6). No copilot work.
- `apply` creates no models, no slot profile and no provider rows, and removes no alias absent from the file (no prune).
- No change to alias window rules, `order` semantics, or the failover table.
- No export/import of `config.yaml` itself.

Anti-goals (a failure even if it technically works):
- never overwrite or extend a value or list the user wrote in `config.yaml`; never delete `router_settings` keys wt does not name;
- never put anything identifying the user or project into a header;
- `apply` must never leave the registry half-written: all aliases or none.

## Constraints

- `config.yaml` is written only by `wt` through `litellm.Open`/`Save`; `registry.toml` only through `config.UpdateRegistry` (its apply is pure and may run three times). `wt/CLAUDE.md` "LiteLLM routes" and "Registry" rules bind.
- A sync with nothing to change must not touch the file (`Changed()` is the test), so on a machine whose block already matches (this one) nothing changes and nothing restarts.
- Test isolation: ad-hoc and test runs must name config.yaml (`WT_LITELLM_CONFIG` or `litellm.Options.Path`) or are refused with `ErrRegistryRedirected`.
- Gate: `make pre-pr` green on an idle machine before each push; never push on red.

## Approaches

For the whole of Step B the user chose, from three scopes: B1 alone; **B1 plus `alias apply`** (chosen); B1 plus documented commands only. Rejected inside B1: value-enforcing the keys (leaves no opt-out), and writing the settings through the model API (the TTL and repin keys are startup-only and the file would shadow it anyway). Rejected inside B2: seeding aliases from `wt-model model init` (hard-codes the user's pool choice in Go and breaks init's provider-rows-only contract), and an export/import of a rich JSON shape (the existing `alias list --json` carries live fields).

## B1: affinity settings (PR 1)

Behavior, in `internal/litellm/configfile.go` beside `ensureCancelOnDisconnect` / `ensureStoreModelInDB`, called from `EnsureSettings`:

- **Block:** `router_settings` absent or null: created as a mapping at the end of the file. A whole-value alias or a root merge key: skipped (nothing is written through it). The sync warning (the `cancelOnDisconnectSkipped` / `settingsWarnings` pattern) is conditional (#758): it fires only when the shared block, resolved as LiteLLM's loader sees it (the alias's anchor, or the block the root merge supplies), is not a mapping (fail closed) or leaves at least one of the three keys absent or null; any non-null value, `false` included, counts as set. A shared block that sets all three draws no warning. `general_settings`' `cancel_on_disconnect` skip warning follows the same rule, and the affinity-off warning below judges the resolved block in every shape (literal, aliased, root-merged).
- **`optional_pre_call_checks`:** absent or null: set to the list `[session_affinity]`. Present: never touched. If it is present and has no `session_affinity`, that is the user's choice (the opt-out); one warning says affinity is off.
- **`deployment_affinity_ttl_seconds`:** absent or null: `14400` (4 h, the Step A value; LiteLLM's own default is 3600). Present: untouched.
- **`session_affinity_repin_on_failover`:** absent or null: `true`. Present: untouched (`false` is the opt-out).
- **Only the file is read.** wt does not look at `router_settings` kept in LiteLLM's database (settings made through its UI or API): `EnsureSettings` does no I/O today and stays that way. A key in `config.yaml` shadows the database copy, so once wt writes a key, a database value for that key stops applying, and an opt-out must be written in the file. Guide 04 says so. (Checked 2026-11-28: this machine's database holds no `router_settings` row, only `general_settings` and the auto-router baselines.)
- **Effects:** the first write after install on a machine lacking any key changes the file and restarts the proxy once (the existing path). The dry run reports `restart: true` and, new here, says why in one plain text line (like the `EnableDB` line in `report.go`); there is **no new JSON field**, so `docs/contracts/litellm-cli.sample.json` and its contract test do not change. A machine whose block already matches sees no write.
- **Warnings** (skipped block, affinity off) are said by `Sync` and the dry run only, never by start/stop jobs (the `dbRoutesOffWarning` rule).
- **Tests:** a unit test for each rule above (absent, null, present, `false` opt-out, list without `session_affinity` untouched, list never extended, alias and merge-key skip with nothing written, and for both the alias and the merge-key shape a with-keys case (no warning), missing-keys and null-key cases (warning) and an unreadable shared block (warning), created block placement), a test that a sync with a hand-written block and nothing else to change does not rewrite the file, a test that comments and key order of a hand-written `router_settings` survive a save, and the fixture updates every "settled config" test needs. A `modelgateway` test pins that `x-claude-code-session-id`, `x-litellm-session-id`, `x-session-id`, `session-id` and `User-Agent` reach the upstream unchanged while credential headers are still replaced.
- **Docs:** guide 04 §7 (the "wt does not write this setting yet" paragraph becomes the rule above, with the opt-out), `wt/CLAUDE.md` and `docs/internals/litellm-routes.md` (`EnsureSettings` bullet), `wt/CHANGELOG.md` Unreleased.

## B2: `wt-model alias apply` and `alias list --toml` (PR 2)

**File format.** `[[aliases]]` tables exactly as in `registry.toml` (`id = "alias/<name>"`, `targets = [{ model = "...", order = N }]`, optional `notes`), nothing else. Any other top-level key or table is refused (a typo guard). `notes` is accepted and ignored, as the registry reader already does (#691); `order` omitted means the target's position, as in `alias set`. The shape is judged by the registry's own reader (`config.Alias.UnmarshalTOML`), so `apply` and `alias set` refuse the same things.

**`wt-model alias apply <file> [--dry-run] [--accept-smaller-window]`.**
- Validates every alias with the same checks as `alias set` (`CheckAlias`/`config.AliasFault`): targets must be registered models, no alias or native target, no two local targets of one exclusive family. An alias id that is currently a redirect is refused (as `alias set` does). Any failure refuses the whole file and changes nothing.
- Writes all changed aliases in **one** `config.UpdateRegistry`, re-checking each (and its window guard) inside the write, then runs **one** route sync (`syncAndWarn`), instead of one per alias.
- Idempotent: an alias is "no change" when its **effective targets** already match, judged by meaning and not by how the registry spells them: the same set of (model, effective order) pairs as `Config.AliasTargets` gives (an omitted order is the target's 1-based position; the order of the list among targets that share an order does not matter). An equal alias is not rewritten, so the registry keeps its own spelling (an omitted `order` stays omitted). A malformed or redirect registry alias is never equal (amended in the final review: the reader keeps a malformed alias's readable targets, so a plain comparison skipped the rewrite that repairs it). A fault in the targets is repaired by that rewrite; a fault elsewhere in the alias table (an unknown key, a non-string `notes`) survives it, so `apply` refuses the file naming the fault and the remedy (hand-edit the row, or `alias rm` and apply again), and the dry run, which runs the same apply over a detached copy of the registry, refuses it in the same words. `alias set`'s own change test is left as it is. `apply` never removes an alias that is not in the file.
- Output per alias as `alias set` words it (`set alias: X` / `no change: X`), then the sync's own lines. `--dry-run` prints each alias's dry-run block (`printAliasDryRun`) and changes nothing.
- Shrink approval: if any alias's window would get smaller or unknown, ask **once**, listing each such alias with `ShrinkSentence`; `--accept-smaller-window` answers yes. No terminal, or the answer no: exit 3 and nothing changed.
- Exit codes as `alias set`: 0 done (also when routes could not be written; a warning says so), 1 a plain error or a refused file, 3 approval missing.

**`wt-model alias list --toml`.** Prints the aliases as `[[aliases]]` tables with explicit `order` for every target, in the same order as `alias list`. It prints only aliases `apply` would accept: redirects, malformed aliases and aliases that fail `config.AliasFault` (for example a target that names no model) are skipped, with one stderr note naming how many and why, so the output always replays. Exclusive with `--json`. Because "no change" is judged by effective targets (above), `apply` of the output is "no change" on any registry, whatever spelling it uses.

**Where the pieces go.** A batch setter `modeladmin.SetAliases(reqs []AliasRequest) (changed []string, err error)` (one `UpdateRegistry`, `CheckAlias` and `Guard` per request against `d.RefConfig()`); the command in `cmd/wt-model/alias.go`; a sample under `docs/contracts/` held by a test (the file `apply` reads, a fixture for the TOML printer). Docs: `wt/docs/wt-model.md` (the alias section; also correct its stale "at most one target is local" line since #690), guide 04 §7 (a fresh-machine sequence: models first via `model add` / `cloud-sync`, then `alias apply`, then `wt profile slots add`, then check commands), the root and `wt/CLAUDE.md` command lists, CHANGELOG.

**Known gap, documented not fixed:** alias windows on a fresh machine are unknown until `tiers.toml` exists or the pool models carry `--context-window` (a `model edit` per model); the guide says so.

## Left to the builder

Exact wording of the two new warnings and the dry-run explanation line; how the single-question shrink prompt words several aliases; whether `alias list --toml` shares the printer with `alias list` or is its own small function; the sample file's content (generic ids, not the user's pool); test names; how to split B1's fixture updates across commits (they all change together).

## Open items recorded, not decided here

Whether LiteLLM keeps affinity pins in Redis (`coordination_redis` is set on this machine) or only in memory (a proxy restart would then drop pins; the guide should say so once it is known); copilot's session header; the pi session id after #6.
