# Open issues: triage and delivery plan (2026-10-26)

Source: all 14 open GitHub issues, each checked against `main` (0d923877) by a read-only investigator, then cross-checked by a critic. Nothing was run live and no code was changed. Line numbers are approximate; the builder confirms them first and runs the targeted Go tests on a clean `main` before starting, because every "a test already exists" claim below is by inspection.

## Summary

| # | Issue | Verdict | PR |
|---|-------|---------|----|
| 389 | picker shows old PR for a reused branch name | **Already fixed** by #450 (`PR.Stale`); still open | close |
| 423 | family with no registry rows invisible to others check | **Already fixed** (`UnregisteredAnswering` + `env.residents`, start path only); still open | close |
| 458 | command-json readiness reads stdout+stderr | accurate | PR-1 |
| 457 | stop / stop --all / picker ignore machine-scope rec | accurate | PR-2 |
| 459 | `bin/update/rec --check` exits 3 not `stale` | accurate | PR-3 |
| 451 | picker `prFor` forks git per row in Update | accurate | PR-4 |
| 405 | declare Claude Code context window | accurate | PR-5 (+ optional PR-5b) |
| 447 | dedupe cloud-sync gates / graduation / format | partly done in #450; remainder is smaller | PR-6 |
| 449 | reprice/classify OpenRouter variant and alias rows | mostly implemented in #450; wording, tests, docs remain | PR-7 |
| 432 | per-model mtplx serve flags | accurate, but issue's file names are stale (spawn is now in `lifecycle/server.go`) | PR-8 |
| 453 | cloud-sync onto Provider `Catalog` capability | **premise is stale**: provider-interface shipped a facts table, not a Go interface | PR-9 (reduced) |
| 403 | omlx Qwen3.8 answer shape | owner at the machine | runbook |
| 393 | mtplx 507 in slot profile | owner at the machine; turbo/stable waits on PR-8 | runbook |
| 399 | omlx abandoned generations | owner at the machine; no buildable work until measured | runbook |

## Order and why

0. **Housekeeping, no code.** Run `cd wt && go test ./internal/lifecycle ./internal/localmodels -run 'Unregistered|ResidentOthers'`, and `go test ./internal/worktree -run PRStale`. If green, close #423 and #389 with comments citing #450. Closing is an action on shared state, so it waits for the owner's yes.
1. **PR-1 #458** then **PR-2 #457**, stacked (both edit `lifecycle/server.go` around `foreignInstance`; #457's new probe goes through the readiness check #458 fixes).
2. **PR-3 #459**, independent, any time.
3. **PR-4 #451**, independent. Heavy edits to `tui/app.go`, so no other PR touches that file concurrently.
4. **PR-5 #405**, independent.
5. **Cloud-sync chain, strictly sequential** (same files): PR-6 #447, then PR-7 #449, then PR-9 #453.
6. **PR-8 #432** after PR-2 (shares `lifecycle/server.go` and `cmd/wt/model_cmds.go`). It also edits `internal/provider/table.go`, so it lands before any #453 provider change (the reduced #453 plan avoids that file).
7. **Owner runbooks** for #403, #393, #399. #393's turbo/stable measurement waits on PR-8.

Rules every PR keeps: contract fixtures change in the same PR as the format they pin; `docs/guides/` never embeds live model state; `make test-all` and `make lint` green; ad-hoc `wt` runs against a scratch registry set `WT_LITELLM_CONFIG` (and `MODELTIERS_OUT`/`MODELTIERS_CACHE` for modeltiers); no live providers touched in tests.

## PR-1: #458 stdout-only readiness (S)

- `lifecycle/env.go`: add a `runOut` seam defaulting to a `runStdout` (`cmd.Output()` with `WaitDelay`, stderr discarded); set it in `defaultEnv()` and in `testEnv()` so unstubbed tests never shell out.
- `lifecycle/ready.go`: the command-json branch uses `e.runOut`. A failed command, empty or invalid output still means not ready. `e.run` stays combined for start/stop.
- Migrate status stubs in `ready_test.go`, `machine_scope_test.go`, `rec_test.go`, `server_test.go`. New test: clean JSON on stdout with stderr noise reads ready, and the `run` stub is never consulted. A real `runStdout` test with a throwaway script (stderr warning + JSON).
- Docs: mark known limit #4 fixed in `wt/docs/internals/local-models.md`. No contract fixture.

## PR-2: #457 stop side uses machine-scope discovery (S-M)

Root cause: `survey.stopState` builds candidates only from registry-port probes, so a rec on another port yields no candidate and a Down family, and `haltsWhole`/`runStop`/`runStopAll`/the picker all conclude "nothing running". The stop backend itself (`stopMachineInstance`) is already machine-aware.

- `lifecycle`: export `MachineScoped(family)` and `MachineEntry(cfg, snap, family) (localmodels.Entry, bool)` (wraps `lookPath`, `FamilyAPIKeyErr`, `foreignInstance`, `serverEntry`; false when binary or key is missing, mirroring `machineOccupant`).
- `survey/stop.go`: add a `machine` field to `stopDeps` (default `lifecycle.MachineEntry`). For a machine-scoped family with no running snapshot candidate, query it, append a Candidate (with session count) and clear Down. Skip the call when the snapshot already lists the instance.
- Tests: instance on another port yields an entry; port-scope family never calls it; `wt stop rec` stops it instead of printing "nothing running"; `wt stop --all` includes it; picker lists it.
- Risk to resolve while building: the extra `rec status` spawn on each stop-state read for idle machine-scoped families (the picker refreshes), and whether `routeAfterStop` and the picker handle a discovered, unregistered entry. Check that `survey` can obtain `snap` in the `stopDeps` seam.
- Docs: `local-models.md`, `wt/CLAUDE.md` lifecycle bullet if it describes stop-side probing.

## PR-3: #459 `bin/update/rec --check` reports `stale` (XS)

- `bin/update/rec`: `--check` ends `stale` (exit 0) when an update is available. Keep `manual` for the running-server refusal and an unknown latest. Update usage text and header comment.
- `docs/guides/08-maintenance-and-troubleshooting.md` §4 rec paragraph (~line 394): `--check` reports current or stale. Confirm the trailer vocabulary list (~line 312) needs no change.
- No automated tests exist for `bin/update`: run `make lint-shell`, `bin/check-links`, and a manual `bin/update/rec --check --no-network`.

## PR-4: #451 take git out of the picker's Update path (M)

Decision (design by the investigator, rejecting a cache because the first miss still forks git inside Update and keying by tip needs a git call per row): resolve staleness off the Update goroutine and make `prFor` pure.

- `tui/worktree_list.go`: `staleKey{dir, branch, head}`, `staleSet map[staleKey]bool`. Split `prFor` into `prLookup` (the `ForRemote`/`ForBranch` part) and `prFor(ei, prs, own, stale)`, which returns nil when `!ei.remote && stale[key]`. `applyPRs` and `prsCurrent` take the same table, so they agree on tags by construction.
- New `resolveStaleCmd(jobs)` runs `PR.Stale` per distinct triple (local and worktree rows, merged or closed PR, non-empty `HeadRefOid`, not already resolved) and returns `staleResolvedMsg`. Built from value copies, never reading the model after launch.
- `tui/app.go`: `model.stale`; launch the command after `entriesLoadedMsg` and `prsLoadedMsg` (not on the `FilterMatchesMsg` path); on the result, merge verdicts and re-`applyPRs` only if a tag changes.
- Defaults (mine): unresolved key reads as not stale, so a reused-name branch briefly shows the old tag until the result lands. Hiding unresolved ended PRs was rejected because every normal merged tag would flicker in late. The critic's correction is adopted: on an entries reload, **keep** the table and drop only keys whose branch/head no longer appear, instead of clearing it, so reloads do not re-flash stale tags.
- Mechanical cost: about 20 test call sites pass a new argument (nil for the old behavior). Bundling `PRSet` and `stale` into one struct argument would shrink that diff; the builder's call.
- Tests: pure-`prFor` tests with a seeded table, `TestStaleFilterRunAfterPRsKeepsTheTags` and siblings unchanged in intent, a resolve-then-apply test, a dedupe test. Docs: `wt/docs/internals/tui.md` (lines ~9, ~60), `wt/CLAUDE.md` (~line 248).

## PR-5: #405 declare Claude Code's context window (S, two PRs)

Facts: `tiers.toml` carries `context` for cloud rows only (local builds deliberately have none), and `internal/tiers` does not expose it yet. `CLAUDE_CODE_DISABLE_1M_CONTEXT` for opusplan (#391) is the precedent for setting a context var.

**PR-5 (cloud/gateway slots from tiers.toml):**
- `tiers.Entry.Context` decoded from `context` (int64 or float64; drop non-positive or invalid). Confirm `tiers` imports nothing from `agents` (the plan's cycle check was asserted, not verified).
- `agents`: `var loadTiers = tiers.Load` (stubbed in tests so the real `~/.config` is never read), `claudeWindow(m, ix)`.
- Single-model launch (`claude.go`): append `CLAUDE_CODE_MAX_CONTEXT_TOKENS` and add it to `ClearEnv` only when injecting. Slot profile (`claudeSlotSettings`): the default slot's window (opusplan: min of opus and sonnet), set only when absent from profile env and `config_content.env`, so the user's value always wins. Not in `ReservedEnvKeys`.
- Value is the models.dev context as declared. Risk: for ollama.com cloud routes the served window may be smaller than the declared one, so state this in the docs.
- Extend `tiers/contract_test.go` (context read for cloud rows, absent for the local-build row). `tiers.sample.toml` unchanged.
- Docs: `slot-profiles.md`, `profiles.md`, `claude-wt.md`, `wt/CLAUDE.md`, root `CLAUDE.md` wording about who reads `tiers.toml`.

**PR-5b (optional, local slots):** `claudeWindow` checks the existing registry `model_info.max_input_tokens` first (LiteLLM's own key, no schema change), then tiers. Not `max_tokens` (that is the output limit). No disable-enforcement var injected by default.

## PR-6: #447 cloud-sync dedupe (S-M, no behavior change)

Gates are mostly done already: `removalGate` is shared, `openrouterGate` and `ollamaGate` are thin wrappers. Remaining:
- Rename `ollamaCodeMeaning` to a flow-neutral `cloudCodeMeaning` (ollama wording currently leaks into openrouter exits); fold `ollamaCode`/`ollamaWhy`/`openrouterCode` into one per-flow outcome with unchanged precedence.
- `applyGraduations(doc, ids)` in `apply.go` for both `Apply` methods (keep Graduate-before-Remove order); new `internal/cloudsync/format.go` with graduated/kept/removals helpers used by both `Format` methods; one helper for the "graduated N manual model(s)" summary.
- Default: shared free helpers, not an embedded plan struct (smaller, keeps exported plan shapes). Printed plan text stays byte-identical, because it is compared with the re-plan made under the registry lock. Existing tests pass unmodified.

## PR-7: #449 variant and alias rows (M)

#450 already keeps `:variant` rows and reprices ordinary suffixes from the base id; `:free`, `:nitro`, `:floor` keep their stored price. What is left:
- Item 1: no new behavior. Make the dry-run say "kept; priced from <base>" or "kept at its current price: <suffix> bills at its own rate". Document that `:online` adds a per-request search fee the base price understates.
- Item 2 (aliases with no `:`): do not guess. Leave them to the approval gate and add an alias hint under `Registry removals`, outside the digested text so digests do not change.
- Item 3 (suffix OpenRouter dropped): **owner question below.** Default is to leave it undetected.
- Item 4: unit tests for dropped suffix, routing-only suffix, unlisted base, no-colon alias, manual alias; e2e tests through `cloud-sync --yes --approve-removals` (variant alone applies with no digest; mixed plan without digest exits 5; with it, applies) using the `cloudFetch`/`confirmCloudSync` seams and a scratch registry with `WT_LITELLM_CONFIG`.
- Docs: `wt/docs/wt-cloud-sync.md` variants subsection, the `cloud-sync` skill paragraph.

## PR-8: #432 per-model mtplx serve flags (M-L)

The issue is stale on locations: `lifecycle/mtplx.go` is gone; the argv is the template `families["mtplx"].Process.Spawn` (`internal/provider/table.go`), spawned in `processServer.start` (`lifecycle/server.go`). `isMtplxServer` is now `serverArgvMatches` (`procinfo.go`), positional, so extra flags do not break it, but a test must pin that. `argAfter(argv, "--model-id")` lives in `server_loading.go`.

Design (flat list, mtplx only): `serve_args = ["--profile", "turbo"]` on the model row, appended after wt's own args.
- `provider`: `ProcessSpec.OwnedFlags()` derived from the spec (flag tokens only, excluding values like `127.0.0.1`) and `SpawnArgs(model, port, extra)`; keep `expandArgs` for stop. One function builds argv for spawn and anything that prints or compares it.
- `config.Model`: `ServeArgs []string` with a tolerant `UnmarshalTOML` (never fails, records faults); `Model.Malformed()` names a bad value; shared `ServeArgsProblems(family, value)` used by loader, writer (`validateModelRow`) and `start`. Refuse non-mtplx rows and any wt-owned flag, including `--flag=value` forms, naming it. `start` fails before spawning on a malformed row.
- CLI: `wt model add|edit --serve-arg` (repeatable, one argv element each) and `--clear-serve-args`; shown in `wt model list` text and JSON (`serve_args`, omitempty). Row lookup in `start` goes through `t.ModelID` with the overlay matching so the legacy id spelling works. Models-tab form is out of scope.
- Changed `serve_args` on a running mtplx: ignored until the next start, with a one-line note from `wt model edit` when the model is running (wt cannot see a running server's flags and avoids stored per-start state).
- **Same PR:** `docs/contracts/model-list.sample.json` (a key added to `modelListRow` must reach the fixture). Registry fixtures (`registry.sample.toml`, `registry.written.sample.toml`) are optional; the critic says keep churn minimal. `docs/contracts/process-providers.toml` does not change, since `serve_args` is per-model and `Spawn` is untouched.
- Check, not assume: llmbench's `registry.py` must tolerate the unknown key (add an llmbench test and a docstring line that it is deliberately ignored). llmbench-isolated mtplx runs will not apply `serve_args`; note this in the PR or file a tracking issue.
- Docs: guide 02 (key, CLI, next-start note, and that wt's own `--model-id` makes mtplx skip its per-model turbo promotion, so ask for `--profile turbo` explicitly), `wt/CLAUDE.md` Registry rules, `wt` model docs, guide 00 config map if the key counts as new.

## PR-9: #453 reduced (S-M)

The issue assumes a Go `Provider` interface with `Local`/`Catalog` capabilities. The provider-interface project shipped a **facts table** in `internal/provider` and listed `Catalog` as a non-goal. So the default is: **do not touch `internal/provider`**; make cloud-sync's flows table-driven inside `cmd/wt/cloudsync*`, keyed by provider id, with a conformance test that every flow has a provider and vice versa. `--only` names, output and exit codes stay byte-identical. Add a note to the provider-interface spec that Catalog stayed a table fact. If the owner wants the full Go interface instead, #453 goes on hold as its own project. Runs after PR-7.

## Owner runbooks (no PRs until results)

Nothing here is run by the builder. Post these as issue comments, not as repo docs (guides never embed live state).

**#403** (from `llmbench/`, one big model loaded at a time, free memory between models). A first probe on 2026-10-21 refuted the "short thinking" lead (omlx 34-53 tokens vs mtplx 35 on `17*23`; on planning omlx thinks more) but reproduced the shape difference (omlx about 2x the words as 500-620 short bullets, 0 tables, vs mtplx 53 bullets and 24 table lines). Penalty flags on the probe already exist (#435).
1. Two more repeats of the three isolate+probe pairs (`provider isolate omlx Qwen3.8-27B-8bit`, then `probe thinking --target omlx8=http://localhost:8000/v1,Qwen3.8-27B-8bit --out /tmp/probe-403/r2/omlx8`; same for omlx4 and mtplx `Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality` on 8003), ending with `provider restore --unload`.
2. The same probe with `--repetition-penalty 1.05` and `--presence-penalty 0.5`, one flag per run.
3. Decide: gaps larger than the repeat spread and moved by a penalty gives a doc note plus an upstream omlx report; not moved means weights or template, so serve the same weights on both engines (owner's call); inside the spread, close as not reproducible. Reply to the third-party commenter either way; share a saved response only if `/tmp/probe-403` still exists and the owner agrees.

**#393.** Without #432: confirm which log holds the 507 text, correct the log name in `wt/docs/wt-agents/slot-profiles.md`, and check read-only whether `wt stats` shows refusals. After PR-8: run `--profile turbo` and `stable` for Qwen3.8-27B Quality through `wt start` and the slot-profile path, then decide whether the #431 advisory softens. Posting the drafted upstream mtplx report needs the owner's approval.

**#399.** Hop tests A/B to find where the cancel is lost (Claude Code to LiteLLM, or LiteLLM to omlx), including whether LiteLLM's `timeout`/`stream_timeout` or retries resend the request. Candidate wt-side fix, speculative until measured: a per-route `timeout`/`stream_timeout` via `EnsureSettings` in `wt/internal/litellm/configfile.go`, written only when absent so a hand-written value wins. Otherwise: an upstream omlx report plus a maintenance-guide note.

## Questions for the owner

None blocks PR-1 through PR-6. Defaults are stated above; say if you disagree.

1. **Close #389 and #423** with comments citing #450 once the confirming tests pass? (Default: I propose the comments and you approve.)
2. **#449 item 3:** a `:free` variant whose full id vanished from OpenRouter's list would become a gated removal, reversing part of the #450 decision. Default: leave it undetected. Opt in if you want it.
3. **#453:** reduced, table-driven flow registry inside `cmd/wt` (default), or the full Go `Provider` interface first?
4. **#405 local slots (PR-5b):** `model_info.max_input_tokens` (default), a dedicated `context` field, or injecting `CLAUDE_CODE_DISABLE_UNKNOWN_MODEL_WINDOW_ENFORCEMENT=1`? Also: inject the declared tiers window as is (default) or with a safety margin?
5. **#432:** flat `serve_args` (default) vs a typed table; and "apply on next start" (default) vs "restart needed".

## Not planned

- No tightening of `ProbeTrusted` (#423): the stop picker shares it.
- #399 and #403 get no code until the owner's measurements pick an outcome.
- Optional follow-ups with no issue yet: check that every family in `localmodels.Families()` has a correct `FamilyOrigin` default (#423); a repeat-3gram probe metric (#403, only if wanted).
