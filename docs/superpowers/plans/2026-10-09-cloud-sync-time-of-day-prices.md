# `wt cloud-sync` Provider Flows, Time-of-Day Prices and the Price the Picker Shows (#322) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `wt cloud-sync` has one flow per provider, `openrouter` and `ollama`; the openrouter flow refreshes exactly the models whose `provider_id` is `openrouter` and stores a time-priced model's whole schedule (its dearest level as the model's price, each other level as a `cost.time_prices` row), the same way whatever hour it runs in; and the model picker shows, and sorts by, the price in force now for every model that has `cost.time_prices` rows, whoever wrote them.

**Architecture:** Four PR slices, each of which builds, passes and is documented alone, landing in the order A, B, C, D. (A) The flows are renamed `prices` → `openrouter` and `catalog` → `ollama` everywhere a user or a reader meets them, tests included, with no aliases. (B) The provider key `openrouter_priced` is removed and both readers of the rule (`config.Config.OpenRouterPriced`, `cloudsync.OpenRouterPriced`) become `provider_id == "openrouter"`, which also makes the two flows' sets of models disjoint by construction. (C) A pure resolver, `config.ModelCost.PriceAt(at time.Time)`, ported from modelman's `price_at`, returns the three prices in force and the row that supplied them; a row the registry's own validator refuses is passed over and named (`TimePrice.Problem`, `Model.Malformed`); the picker's table reads one clock per build (`pickerNow`, a seam) and uses the resolver for its COST column, its cost sort and a price on its mode line. It lands **before** D, so the picker already applies rows (ollama's `off-peak` row, rows written by hand) on the day the openrouter flow starts to write them. (D) A pure file, `wt/internal/cloudsync/timeofday.go`, reads OpenRouter's `pricing.overrides` into price levels over the UTC week; `ParseOpenRouter` stores the dearest as the flat price and `timeofday_rows.go` writes the others as rows labelled `openrouter`; nothing in the flow reads a clock, so the plan compared under the registry lock never depends on the hour. D closes #322.

**Tech Stack:** Go 1.26.7 (module root `wt/`), standard library only (`encoding/json`, `slices`, `maps`, `cmp`, `time`), `internal/tomlw` for the rows, Bubble Tea / bubbles (already dependencies) for the picker. One Perl script for the mechanical rename (Perl 5 ships with macOS). No new dependency. No Python changes.

**Spec:** GitHub issue [#322](https://github.com/ohanaverse/local-ai-setup/issues/322) (`gh issue view 322 --repo ohanaverse/local-ai-setup`) as extended and overridden by the owner's directions of 2026-10-09, quoted in full in "The Owner's Directions" below. There is no separate design document: "What Was Established", "The Design and Why" and "Decisions This Plan Makes" are the design. Where the issue's text, the directions and this plan differ, the directions win over the issue, and "What Was Established" says where the issue's premises were wrong.

**Verified against:** `main` at `63f63d5` (2026-10-09; `modelman/` is deleted, llmbench's llamacpp backend is gone, `wt start --json` is gone, wt no longer reads `modelman.toml`). This document's own text was executed on a clean copy of that commit (`git archive HEAD`), slice by slice in the order A, B, C, D: its 8 created files, 11 appended blocks, 99 find/replace edits and 2 commands, in the order they appear. At the end of each slice `gofmt -l .` printed nothing, and `go vet ./...`, `go test -count=1 ./...` (25 packages `ok`) and `make check` passed in `wt/`, and `bin/check-links` printed `ALL LINKS OK`. Each failing run quoted in a task was observed with that task's tests applied and its implementation not yet. Tasks 3 to 5 were also applied to `63f63d5` alone, without A and B, and passed the same checks. `make test-all` was not run (it also runs the llmbench suite, which nothing here touches). The picker of slice C was captured from a real pty at 80x24, 40x12 and 120x24 at three instants, with LiteLLM on and off, and Task 6 Step 9 was run as written; the captures are quoted in "What Was Established" (facts 34, 39 and 40). One `--dry-run` of the first revision's final binary against the live OpenRouter list and a made-up registry ran on Friday 2026-10-09 at 16:55 UTC (fact 36); the openrouter flow's code has not changed since. **If `main` has moved**, the "find" text of an edit may no longer match: re-read the file and apply the same change to what is there. Another workflow's worktree (`.worktrees/s6-6`) was changing test files and two wt docs when this was written; Task 1's rename is rule-based for that reason, and its Step 5 lists what must not remain.

## Global Constraints

- **The flows are `openrouter` and `ollama`, with no aliases.** `--only prices` and `--only catalog` are unknown flows (exit 1). Every output line of a flow starts `openrouter: ` or `ollama: `; the route sync's lines still start `routes: `.
- **A model is OpenRouter-priced exactly when its `provider_id` is `openrouter`.** `config.Config.OpenRouterPriced` and `cloudsync.OpenRouterPriced` are that one comparison, pinned to the same list by `docs/contracts/catalog-predicates.sample.toml` and `.expected.json`. The provider key `openrouter_priced` is read by nothing; a registry that carries it loads, and a write keeps it.
- **No registry row is in both flows' scope.** The openrouter flow plans from rows whose `provider_id` is `openrouter`; the ollama planner addresses only rows whose `provider_id` is `ollama`. `TestTheTwoFlowsNeverShareAModel` holds both rules to that.
- **Nothing in the openrouter flow reads a clock to decide a price.** `PlanPrices` and `PricePlan.Format` stay functions of the registry rows and the parsed response. The command plans again under the registry lock and compares the text (`cmd/wt/cloudsync.go`, `writeCloudSync`); a plan that depended on the hour could fail that compare with `openrouter: error: the registry changed after the plan was printed`.
- **The top-level `prompt` and `completion` of a model with a readable schedule are not read, not even to refuse them.** Neither is its top-level `input_cache_read` when the schedule's entries carry a cached-input price. The one top-level price that is read is `input_cache_read` when no time-of-day entry carries one (then it does not depend on the hour); a value there that is no price refuses the model, as it does for any model.
- **Only an `overrides` entry with `utc_start`, `utc_end` or `utc_days` is a time-of-day entry.** An entry without one (a prompt-size tier, `min_prompt_tokens`) is ignored: no price, no row, no warning.
- **A schedule that cannot be read with certainty leaves the model exactly as it is**, not stamped, with one `warning:` line whose text does not depend on the hour or on the top-level prices. It never falls back to the top-level price.
- **The flat price of a time-priced model is the dearest level of its schedule** (highest output price, then input, then cached input): `mainRate` in `timeofday.go` is the one place that says so. It is the price in force when no row is, the fallback, and what LiteLLM's route carries at every hour.
- **The picker (slice C) lands before the rows (slice D).** No task of slice C reads anything slice D adds, and slice D's docs are written against slice C's.
- **Row ownership:** the openrouter flow owns the `cost.time_prices` rows labelled exactly `openrouter` on the models it matched, and no other row. The ollama flow owns the row labelled `off-peak` on ollama cloud entries, as before. A flow leaves every row it does not own byte for byte what it was.
- **The plan's text shows what an apply writes** (`TestApplyWritesOnlyWhatThePlanShows` stays green): a plan line prints each `openrouter` row with its prices, its windows, its timezone when that is not UTC, and `+keys` when the row or a window has a key the sync does not write.
- Rows are built as `*tomlw.Table` with keys in schema order (`label`, `timezone`, `input_price_per_million`, `cache_price_per_million`, `output_price_per_million`, `windows`; a window: `days`, `start`, `end`) and written through the existing `costPatch` / `RegistryDoc.PatchModel`. Not `RegistryDoc.SetTimePrices`.
- **The resolver is pure.** `config.ModelCost.PriceAt(at time.Time) config.PriceInForce` takes the instant as an argument and reads no clock and no environment (the one file it reads is the host's zone database, for a row's timezone). It never fails and never panics.
- **A row is applied only when the registry's validator accepts it**, and the rule is per row, not per window: `TimePrice.Problem` asks `validateTimePrice` itself, so the reader and the writer cannot disagree. A row with a problem is passed over whole at every instant, is named by `Model.Malformed` (which `wt model list` prints), and does not make its model time-priced. A value of the wrong TOML type in a row fails the registry load, as anywhere else in the file; this plan does not change the loader.
- **A row means what modelman's `price_at` said it means:** rows are tried in file order; the first whose windows hold the instant wins and supplies each price it sets; a price it does not set stays the flat one; with no such row the flat prices are in force. A window is `[start, end)` to the minute, on the listed days, on the wall clock of the row's IANA `timezone`.
- **The picker reads one clock per table build**, through the package variable `pickerNow` in `wt/internal/tui/modelrows.go` (or `tableInput.now` when a caller sets it), and prices every row of the table at that one instant. Nothing reads a clock while a screen is drawn.
- **A price on the mode line is whole or absent.** `modeLine` is given the width the line has and never returns part of a price; the mark of a time-priced model comes before the numbers (`cost~`).
- **LiteLLM's route is priced from the flat price only**, at every hour (`TestPricingInfoReadsOnlyTheFlatPrices`). No task changes `wt/internal/litellm/entry.go`.
- **Every wt screen fits the terminal** at widths 40/80/120 and heights 12/24/50 (`wt/docs/internals/tui.md`, `internal/tuilayout`): a view is never taller or wider than the terminal. No task adds a line to a frame.
- The per-million conversion is left as it is (one float64 multiplication). It writes values such as `3.9600000000000004`; this plan does not round them.
- **No live calls in tests.** OpenRouter is reached through the existing `cloudFetch` seam; the tests read a saved response, `wt/internal/cloudsync/testdata/openrouter_models.json`, and fix the clock through `cloudSyncNow` (the command) and `pickerNow` / `tableInput.now` (the picker).
- Every `Test*` function has a top-level `//` comment saying what it tests and why a regression matters to a user.
- **Never, in any step of this plan:** read, write or print the developer's real `~/.config/local-ai/registry.toml`, `~/.config/agent-wt/config.toml` or `~/.config/litellm/config.yaml`; print an API key; run the real `ollama`; run `wt cloud-sync` without `--dry-run` outside a test. Any run of a built `wt` is under `env -i` with a throwaway `HOME` and `XDG_CONFIG_HOME`, `WT_REGISTRY` and `WT_LITELLM_CONFIG` naming scratch files, and `WT_LITELLM_RESTART_CMD=true`. The one exception is Task 10, which copies the real registry once, after the owner's OK, and which no implementer subagent runs.
- Guides never embed live model state: every example in a doc is labelled illustrative, and no count of models from OpenRouter's list goes into `docs/guides/` or `wt/docs/`.
- Run every Go command from `wt/`, never the monorepo root. A commit step says which directory its `git add` paths are relative to: the repo root.
- Before each PR, from `wt/`: `test -z "$(gofmt -l .)" && go vet ./... && go test -count=1 ./... && make check`; from the monorepo root: `make test-all` and `make check-links`.
- Commit messages follow the repo's conventional style (`fix(wt): …`, `feat(wt): …`, `refactor(wt): …`, `docs(wt): …`), with the attribution trailer your session is told to add, if any.
- Each PR is one feature branch off `main`. **Pushing a branch, opening a PR, commenting on or closing an issue, and filing a new issue all need the owner's OK.**
- Read `wt/CLAUDE.md` (the "Cloud sync" section), `wt/docs/wt-cloud-sync.md`, `wt/docs/internals/tui.md` and `wt/docs/internals/testing.md` before starting.

## Review Focus

1. **OpenRouter changes the override grammar: a new condition key on a time-of-day entry, a new price key, a schedule published with a gap, a window with no completion price; or the listed provider changes and a model's schedule comes and goes.** Expected: a new price key that the model's pricing object also has at its top level is read past; in every other grammar case the model keeps the price and rows it has, is not stamped, and one warning names it and the reason; the plan text is the same in every window; nothing is half applied and nothing falls back to the top-level price. A schedule that appears or goes is an update each time, and the plan says nothing false about it. Pinned in Task 7 (`TestParseOpenRouterTimeOfDayShapes`, the 22 refusal cases and the new-price-key case; `TestPlanPricesLeavesAModelWhoseScheduleItCannotRead`; `TestPlanPricesReportsAScheduleThatComesAndGoes`).
2. **The registry as the owner's really is today: a sync before the fix stored the dear rate (the 02:45 UTC apply in the issue), or the cheap one.** Expected after slice D: one update per time-priced model on the first sync, the same text in every window; when the dear rate was stored the line reads `1.32/0.044/3.96 -> 1.32/0.044/3.96 (openrouter …)`, `config.yaml` keeps its bytes and the proxy is not restarted; a second sync in another window changes no price and syncs no route. Pinned in Task 7 (`TestCloudSyncTimeOfDayPricesDoNotFollowTheClock`) and Task 8 (`TestPricePlanFormatShowsASchedule`, entries `or/low` and `or/high`; `TestCloudSyncRowsOnlyUpdateLeavesTheRoutesAlone`, which runs the real route sync; `TestPricesApplyStoresAScheduleOnce`, the file's bytes).
3. **The picker on the terminal the owner really has: 80 columns, a 41-character OpenRouter id in the table; and a narrower one.** At 80 columns the COST column is not drawn at all (a narrow table gives up whole columns, COST goes fifth, and with such an id it is drawn only from 111 columns: fact 42), so a fix that only changed that column would show the current price to nobody at that width. Expected: the price in force for the highlighted model is on the mode line under the table, `cost~` when it depends on the time, **whole or not at all**: at 80 and 120 columns beside the LiteLLM mode; at 40 columns beside `LiteLLM: on` when both fit in 36 columns and alone on the line when they do not, which is every price under `LiteLLM: off (direct)` and the longer prices under `LiteLLM: on`. No screen shows a price cut short or a time-priced price without its mark (the first revision did both: fact 39). The view fits at 40/80/120 by 12/24/50 with time-priced models in it; where the COST column is drawn it shows the price in force, marked, under a heading that explains the mark; a resize or a cursor move does not re-price an open picker. Pinned in Task 4 (`TestRenderTableShowsThePriceInForce`, `TestModelPickerWithTimePricedModelsFitsTheTerminal`, both pickers; `TestAnOpenPickerKeepsThePricesItOpenedWith`) and Task 5 (`TestModelPickerNamesTheHighlightedModelsPrice`, at 80x24, 40x12 and 120x50, LiteLLM on and off, the real models' longest prices); seen on a real pty (facts 34, 39, 40).
4. **A `cost.time_prices` row someone wrote or edited by hand.** There are three kinds, and they end differently. (a) **A row that decodes and keeps the rules**, in any zone: it is applied; in a zone with daylight saving time its windows follow the wall clock, the repeated hour is in the window both times and the skipped hour at no instant (Task 3: `TestPriceAtFollowsDaylightSavingTime`, `TestPriceAtReadsAWindowInItsRowsTimezone`). (b) **A row that decodes and breaks a rule** (an overnight window written `22:00` to `06:00`, a zone that is none, `Local`, a time that is no time, a negative price): it is not applied, whole, at any instant; the picker shows the flat price and no mark; `wt model list` names the row and the rule; a readable row after it still applies; and any registry write that touches that model is refused with the same reason, which for a model of the `openrouter` provider includes every `wt cloud-sync` apply, for all models, until the row is fixed (fact 40; that refusal is `main`'s). Pinned in Task 3 (`TestPriceAtIgnoresARowItCannotRead`, `TestTimePriceProblemIsTheValidatorsRefusal`, `TestLoadKeepsATimePricesRowTheValidatorRefusesAndNamesIt`, `TestModelListNamesATimePricesRowThatIsNotApplied`) and Task 4 (the unmarked cell in `TestRenderTableShowsThePriceInForce`). (c) **A value of the wrong TOML type** (`windows = "always"`, a price in quotes, `days = "mon"`): the registry does not load and every wt command stops with a parse error that names the line and the key; nothing reaches the resolver or the sync (fact 41; `main`'s behaviour, kept and pinned by `TestLoadStopsOnATimePricesValueOfTheWrongType`). So `TestPlanPricesReplacesAnOpenRouterRowWhateverItHolds` (Task 8) and `TestPriceAtIgnoresARowItCannotRead` are package-level tests of rows built in memory; through the real command only rows of kinds (a) and (b) arrive. For a row labelled `openrouter` on an openrouter model that decodes but is not what the sync writes (another timezone, an added key, other windows): the plan prints, shows the row as it found it including what differs from the sync's own, and replaces it. Not covered: a row that differs from the sync's only in the TOML type of a value under a key the sync writes (an integer where the sync writes a float) is replaced on a plan line that reads the same on both sides; a flat price typed by hand as `3.96` where the sync writes `3.9600000000000004` already does that on `main`.
5. **What the rename and the removed key leave behind: a script or a habit that still says `--only prices`, and a registry that still carries `openrouter_priced`.** Expected: the old flow name is refused with a line that lists the valid names (exit 1, nothing fetched), never run as something else; the registry loads, the key decides nothing in either direction (a provider marked `true` is not fetched for; the openrouter provider's own models are refreshed although their row says `false`), and a registry write leaves the key where it was. Pinned in Task 1 (`TestCloudSyncFlowsAreNamedForTheirProviders`) and Task 2 (`TestCloudSyncRefreshesOnlyTheOpenRouterProvidersModels`, the contract fixture, `TestHasOpenRouterPricedModel`).

---

## The Owner's Directions

Given on 2026-10-09, after the first version of this plan. They are binding, and they override the issue and the first version wherever those differ.

- **(D1)** "cloud-sync should do different things for the two providers: openrouter — just refresh the prices for cloud models that are configured; ollama — 1. sync the cloud models locally with those at ollama.com, 2. refresh prices for all cloud models similar to openrouter." and "the flows are specific to provider so it makes more sense to rename the flows as openrouter and ollama". The controller's reading, stated to the owner without objection: a plain rename with **no aliases** for the old names (the command is two days old and has one user); `--only prices` and `--only catalog` become usage errors like any unknown flow.
- **(D2)** "openrouter sync should implement time window pricing". So the openrouter flow stores each time-priced model's schedule as `cost.time_prices` rows; the rows are not optional and not a later PR.
- **(D3)** "the goal with cost.time_prices is to have the wt model picker show the current prices when user picks models". So wherever wt shows a model's price when the user picks a model, it shows the price in force now, resolved from `cost.time_prices`: for OpenRouter's schedule rows, for the `off-peak` rows the ollama flow already writes, and for rows a user wrote by hand. The owner did not ask for LiteLLM's route cost or spend to follow the clock: the route keeps the flat price, and this plan says so wherever it matters.
- **(D4)** "are any models using openrouter_priced? this was added when ollama was a pure subscription model, now that ollama publishes prices openrouter_priced is not necessary". The controller checked the owner's registry: no provider sets `openrouter_priced`, and all 13 OpenRouter-priced models are under the `openrouter` provider. So the key is removed, and the rule becomes "the openrouter flow refreshes the models whose `provider_id` is `openrouter`".

## What Was Established

Facts 1 to 28 were established by the first planning run, on `main` at `e61d989`, when the flows were still called `prices` and `catalog`; their wording is kept, and a fact this plan changes says so in a note after its table. Facts 29 to 38 were established by the revision run, on `main` at `63f63d5`, and facts 39 to 45 by the review of that revision and the run that applied it. Where facts 1 to 28 say "Task N" they mean the first version's tasks: its Task 1 is this plan's Task 7, its Task 4 is this plan's Task 8, its Tasks 2 and 5 are this plan's Task 9, and its Task 3 (two live dry runs) is this plan's Task 10 (one). In fact 20, "the Task 2 binary" is the code as it stands after this plan's Task 7 (no rows) and "the Task 5 binary" the code after this plan's Task 8 (rows), both before the rename, which is why their lines start `prices:`. Confidence is one of: **observed** (seen in bytes or in a run), **documented** (stated by the service's own docs), **inferred** (reasoned from source or from a stand-in, not seen live).

### About OpenRouter

| # | Fact | Evidence | Confidence |
|---|---|---|---|
| 1 | The issue's "85 of 469 models carry `overrides`" is true and misleading: **3** of the 85 are time-of-day schedules (`deepseek/deepseek-v4-pro-0813`, `tencent/hy3`, `tencent/hy4-preview`). The other 82 carry only `min_prompt_tokens`, a prompt-size tier whose top-level price does not move with the clock. No model mixed the two kinds. | The list fetched 2026-10-09 14:06:14 UTC (782,042 bytes), counted by condition key. | observed |
| 2 | `utc_start` / `utc_end` are HHMM clock numbers in UTC; the window is `[start, end)`; it wraps past midnight when the end is not after the start, so `0` as the end means end of day. `utc_days` lists full lowercase weekday names, scopes the window (or, with no window, the whole day), is tested at the instant of the request, and means every day when absent. Later entries win where entries overlap; a price key absent from an entry inherits the top-level price. | OpenRouter's Models guide, "Pricing Overrides" (`https://openrouter.ai/docs/guides/overview/models`), and the OpenAPI schema `PricingOverride`. Checked on the data at minute resolution: read as HHMM with wrap, each of the 3 models' entries covers each of the week's 10,080 minutes exactly once; read as minutes or as hours they do not. | documented + observed |
| 3 | For a time-priced model **the top-level price is defined as the price of the window in force when the response was generated.** It is not a list price. The `overrides` array "always lists every window (peak and off-peak), tiling the full 24-hour day (or, with `utc_days`, the full week) … the complete schedule is recoverable regardless of when the response was generated". | Same guide (its example carries the comment "Top-level prices always reflect the window that applies right now"). **The observations are thinner than a count of matches suggests:** at every fetch made before 16:00 UTC on 2026-10-09, and at the issue's 00:28 sample, each model was in the level that holds most of its week, which is also its first entry, so those matches fit "current window", "majority level" and "first entry" alike. **Two readings tell the rules apart:** the issue's 02:45 UTC one (deepseek at its 20.8% level), and this plan's own pair across 16:00 UTC, where both tencent models went from their 66.7% level (15:18) to their 33.3% level (16:09) with their `overrides` unchanged. deepseek was not watched across a boundary. | documented; observed for the tencent models at one boundary |
| 4 | Each of the 3 models has exactly two price levels. deepseek-v4-pro-0813: 1.32/0.044/3.96 on weekdays 01:00–04:00 and 06:00–10:00 UTC (35 of the week's 168 hours, 20.8%), 0.66/0.022/1.98 otherwise (79.2%). tencent/hy3: 0.132/0.033/0.528 from 00:00 to 16:00 (66.7%), 0.0825/0.020625/0.33 from 16:00 (33.3%). tencent/hy4-preview: 0.834/0.042/2.501 and 0.7506/0.0378/2.2509, same hours. Every time entry carries `prompt`, `completion` and `input_cache_read`, and in each model the dearer level is dearer on all three. | The same list, counted by the minute. These three models, as fetched, are this plan's test fixture. | observed |
| 5 | There is no time-independent price field anywhere in the list. A fixed rate has to be derived from the entries. | Key scan of all 469 models; `top_provider` holds only `context_length`, `max_completion_tokens`, `is_moderated`. | observed |
| 6 | OpenRouter tells consumers: "New condition fields may be added to the override grammar over time. Consumers should skip entries containing condition fields they do not recognize rather than apply their prices." An entry's prices have the "same keys and units as the base pricing object". | Same guide. | documented |
| 7 | **The three models without `overrides` that also changed price in the issue are a different cause, and it is the bigger one.** A model's top-level price is that of one provider endpoint ("Pricing from the top provider for this model"), and it moves within minutes. Between fetches at 14:06 and 14:15 UTC, 5 of 469 models changed a price; between 14:06 and 14:47, 8 did; none of them has `overrides`. Between 14:42 and 14:47 the listed output price of `~deepseek/deepseek-v4-flash-latest` went from 1.28 to 0.030672 per million (about 42 times) and its input price from 0.0137 to 0.01096. For `deepseek/deepseek-v4-flash-0731` one endpoint moved from 0.0046 to 0.018 per million and the top-level price followed it. | List snapshots at 14:06, 14:15, 14:42 and 14:47 UTC, and the endpoints API for that model. | observed (41 minutes of one afternoon) |
| 8 | The list is edge-cached, and **its top-level price trails a window boundary by minutes**: tencent's window turns at 16:00 UTC, the list fetched at 16:03 still showed both tencent models at their before-16:00 level, and the one fetched at 16:09 showed the after-16:00 level. | Response header `cache-control: public, max-age=120, stale-while-revalidate=3600, stale-if-error=3600`; the two fetches. | observed |
| 23 | **The time-priced models are served by several providers, and the schedule belongs to one of them.** `deepseek/deepseek-v4-pro-0813` had 21 endpoints: DeepSeek's own carries the six time-of-day entries the list shows and its prices are the list's; Alibaba's carries a schedule of its own (two entries) that the list does not show; StreamLake's is at 0.66/1.98 with no schedule; and two endpoints (Baidu, Alibaba) were cheaper than the listed price on both input and output. `tencent/hy3` had 5: Tencent's own carries the schedule and was the cheapest. `tencent/hy4-preview` was not fetched. So the listed provider was not simply the cheapest for deepseek, and how OpenRouter picks it is not known. | The endpoints API (`/api/v1/models/<id>/endpoints`) for those two models, saved 2026-10-09 about 14:10 UTC. | observed |
| 24 | The three models' `overrides` lists were the same bytes in every list fetched on 2026-10-09 between 14:06 and 16:09 UTC (16 saved responses), **on both sides of the tencent models' 16:00 boundary**. deepseek stayed in its cheap window the whole time, so whether the list shows it from the same provider in its peak hours rests on the issue's 02:45 sample. | Hash of each model's `overrides` per saved response. | observed (two hours of one afternoon) |
| 25 | The 469 models used 12 top-level price keys (`prompt`, `completion`, `input_cache_read`, `input_cache_write`, `input_cache_write_1h`, `web_search`, `image`, `image_output`, `audio`, `audio_output`, `input_audio_cache`, `internal_reasoning`), all strings. Every price key on any `overrides` entry is also a top-level key of the same model. The ten time-of-day entries carry only `prompt`, `completion` and `input_cache_read`. | Key count over the list of 14:47 UTC. | observed (review) |

### About what the stored price is used for

| # | Fact | Evidence | Confidence |
|---|---|---|---|
| 9 | `wt stats` does not compute spend from registry prices. It sums the `spend` column of LiteLLM's `LiteLLM_SpendLogs`. | `wt/internal/spend/spend.go:120-130`. | observed |
| 10 | The registry's flat price reaches LiteLLM as the route's `model_info` (`input_cost_per_token`, `output_cost_per_token`, the cache price on both cache keys). `time_prices` rows reach no route. | `wt/internal/litellm/entry.go:64-80` (`pricingInfo`). | observed |
| 11 | With the installed LiteLLM 1.103.1, a request to an `openrouter/*` route by chat completions or by `/v1/messages` (claude, pi, copilot) is logged at **OpenRouter's own cost figure**, not at the route's price. By the Responses API (codex) it was logged at the **route's price**. A reply with no cost figure is priced from the route on every path. | LiteLLM source (`llms/openrouter/chat/transformation.py`, `cost_calculator.py`), and an in-process `litellm.Router` run against a local stand-in for OpenRouter: stand-in cost 1.23, route price worth 6.00; logged 1.23 on the two chat paths, 6.00 on the Responses path. | observed against a stand-in, **not** against the live proxy or OpenRouter |
| 12 | Inside wt the flat price is shown in the model picker's COST column and is the cloud sort key (cost ascending: output price, then input price), and the first row of the sorted list is the default selection. So the stored price is used on every launch, whatever fact 11 turns out to be live. No agent config wt writes carries a price; llmbench reads none. | `wt/internal/tui/modelrows.go:72-100`, `model_list.go`; grep of `wt/internal/agents`; `llmbench/src/llmbench/registry.py`. | observed |
| 13 | Each flip of the stored price counts as a price update, so the run syncs the routes; the route's row changed, so `config.yaml` is rewritten and the proxy restarted. | `wt/cmd/wt/cloudsync.go:436-439`; `wt/internal/litellm/service.go:453`. | observed |
| 14 | LiteLLM 1.103.1 has its own time-of-day key for a route, `model_info.off_peak_pricing`: one standard rate plus **one** off-peak rate with UTC windows. Against the same stand-in it was applied on the Responses path. It refuses `24:00` in silence (the end of day is written `00:00`). A schedule with three levels does not fit it. | LiteLLM `types/utils.py:203-234`, `llm_cost_calc/utils.py`; one investigator's stand-in run. | observed against a stand-in |

- **Fact 12 today (63f63d5):** the code is `rowCostKey` and `sortRows` in `wt/internal/tui/modelrows.go` and `renderTable` in `modeltable.go`. Slice C changes what they read: the price in force (Tasks 4 and 5). And the default selection is the rotation's next model when the agent has rotation state; the first sorted row is the fallback (`docs/guides/06-wt-agents-and-models.md`).

### About the code

| # | Fact | Evidence | Confidence |
|---|---|---|---|
| 15 | `ParseOpenRouter` reads `prompt`, `completion`, `input_cache_read` and nothing else. `PlanPrices` copies `TimePrices` through untouched. Within one run the plan does not depend on the clock; across runs it flaps. | `wt/internal/cloudsync/openrouter.go:94-105`, `:190-197`; Task 1 Step 2's failing run. | observed |
| 16 | A `cost.time_prices` row needs an IANA `timezone`, a non-empty `windows`, and in each window `days` from `mon`..`sun` and `"HH:MM"` `start` < `end` with `end` up to `"24:00"`. No wrapping window. Unknown keys are allowed. | `wt/internal/config/registry_validate.go:277-352`. | observed |
| 17 | `sameCost` already compares the rows whole (`tomlw.Same` on each), `costPatch` already writes them only when they differ, and `PricePlan.Apply` already stamps every matched model. `formatCost` prints a row only when its label is `off-peak`. Prices are compared as floats, exactly. | `wt/internal/cloudsync/catalog.go:245-268`, `:461-474`; `apply.go:26-66`, `:195-210`; `pricingpage.go:62`. | observed |
| 18 | The catalog flow replaces the row labelled `off-peak` and keeps every other row; it only looks at ollama entries. When it changes a model's rows, `costPatch` writes the whole list it planned. | `wt/internal/cloudsync/catalog.go:209-243`, `:110`. | observed |
| 19 | Nothing in wt applies `time_prices`. | `wt/internal/config/config.go:449-453`; grep. | observed |
| 20 | **This plan's code, on the live list, in two windows.** The scratch-built binaries, run with `--only prices --dry-run` under `env -i` against one made-up registry (deepseek and hy3 each stored at their cheap level, hy4-preview and claude-haiku-5.5 with no cost), Friday 2026-10-09. At 15:18 UTC the list's top-level prices were hy3 0.132/0.033/0.528 and hy4-preview 0.834/0.042/2.501; at 16:09 they were 0.0825/0.020625/0.33 and 0.7506/0.0378/2.2509; deepseek's were 0.66/0.022/1.98 both times. **Both binaries printed the same bytes at both times.** The Task 2 binary: `openrouter/deepseek--deepseek-v4-pro-0813: 0.66/0.022/1.98 -> 1.32/0.044/3.96`, `openrouter/tencent--hy3: 0.0825/0.020625/0.33 -> 0.132/0.033/0.528`, `openrouter/tencent--hy4-preview: no cost -> 0.834/0.042/2.501` and `openrouter/anthropic--claude-haiku-5.5: no cost -> 0.1/0.01/0.5` (a tier model). The Task 5 binary: the same four lines with ` (openrouter 0.66/0.022/1.98 mon-fri 00:00-01:00 04:00-06:00 10:00-24:00, sat-sun 00:00-24:00)`, ` (openrouter 0.0825/0.020625/0.33 mon-sun 16:00-24:00)` and ` (openrouter 0.7506/0.0378/2.2509 mon-sun 16:00-24:00)` after the first three. At 16:09 `main`'s code would have found hy3 unchanged at its cheap level. Exit 0 each time, the registry file unchanged. Not seen: deepseek's peak window. | The runs themselves; the list saved beside each. | observed |
| 21 | The per-million conversion gives `3.9600000000000004` for `"0.00000396"`, `1.9800000000000002` for `"0.00000198"`, `0.13199999999999998` for `"0.000000132"`. The plan text hides it (six significant digits). The same text always converts to the same float, so two entries at one price are found equal. | Scratch run; `openrouter.go:55`. | observed |
| 22 | Thirteen mutations of the new code were each caught by the new tests: the top-level price kept; the cheapest level stored; a wrapping window read as empty; the first overlapping entry winning; a top-level `-1` still refusing a readable schedule; the two warnings tested in the other order; a top-level price key not accepted on an entry; the `overrides` key itself accepted as a price; levels ordered by input before output; the rows not written; the sync's rows moved to the end of the list; a row's timezone not printed; `+keys` not printed. | Scratch runs. | observed |
| 26 | **One write, both flows, one model** (review). `writeCloudSync` plans both flows from the same reading of the registry and applies prices, then catalog. For a model both flows change rows on, the catalog's write drops the `openrouter` rows the prices flow wrote a moment before; the next run's prices plan reports the same update and it holds. | `wt/cmd/wt/cloudsync.go:466-497`; `TestBothFlowsInOneWriteSettleOnTheNextRun` (Task 4): runs 1, 2, 3 change 1, 1, 0 prices and leave rows `weekend, off-peak`, then `weekend, off-peak, openrouter` twice. | observed |
| 27 | **An update that changes only rows does not touch the routes' bytes** (review). With the flat prices already at the schedule's level, the apply prints `2 price(s) changed`, the real route sync runs, `config.yaml` is byte-identical and the restart command is not run. | `TestCloudSyncRowsOnlyUpdateLeavesTheRoutesAlone` (Task 4). | observed |
| 28 | The command prints `prices: no model could be refreshed … set openrouter_priced = false on a provider whose model names are not OpenRouter ids` whenever there are candidates and nothing matched. A registry whose only OpenRouter-priced models have a refused schedule gets that line after the new warning, and for it the advice is wrong. The same already holds for a `-1` price. | `wt/cmd/wt/cloudsync.go:297-303`, read, not run. | inferred from source |

- **Fact 19 is what slice C changes** (D3): `config.ModelCost.PriceAt` applies the rows and the picker shows what it returns.
- **Fact 26 cannot happen after slice B** (D4): with the rule `provider_id == "openrouter"` no row is in both flows' scope (fact 30), so `TestBothFlowsInOneWriteSettleOnTheNextRun` and the two-run settling are gone from this plan.
- **Fact 28 is fixed by slice B**: the line no longer names the removed key; it ends `the warnings above say why for each model`, which is right for a refused schedule too.
- The line numbers in facts 9 to 18 are those of `e61d989`. `wt/internal/cloudsync/` and `wt/cmd/wt/cloudsync*.go` did not change between it and `63f63d5` except for two test files, so those still hold. In the other files a fact cites (`spend.go`, `litellm/entry.go`, `litellm/service.go`, `tui/modelrows.go`, `config/config.go`) the function it names was found again at `63f63d5` (`pricingInfo`, `rowCostKey`, `sortRows`, the comment on `TimePrice`), but the line numbers were not re-checked one by one: find it by name.

### Established by the revision run (63f63d5)

| # | Fact | Evidence | Confidence |
|---|---|---|---|
| 29 | **The provider key `openrouter_priced` has exactly these readers:** `config.Provider.OpenRouterPriced` (the typed field) and `config.Config.OpenRouterPriced` (the stale-price notice, through `agents.HasOpenRouterPricedModel` and `agents.LastPriceRefresh`); `cloudsync.Provider.OpenRouterPriced` and `cloudsync.OpenRouterPriced` (the flow); the writer's key-order list `providerSchemas` in `registry_doc.go`; the contract fixture and its two Go tests; and prose in guide 02, `wt-cloud-sync.md`, the skill, `wt/CLAUDE.md`, `wt/CHANGELOG.md`, `docs/internals/config-and-registry.md` and `launch-flow.md`. No Python reads it (llmbench has no hit). | `grep -rn "openrouter_priced\|OpenRouterPriced"` over the tree at `63f63d5`. | observed |
| 30 | **With the rule `provider_id == "openrouter"` the two flows can never own rows on the same model.** The ollama planner reaches an existing row only through `findEntry` (every branch requires `ProviderID == "ollama"`) and `isOllamaCloud` (the same), and an addition is a new row with `provider_id = "ollama"`. A row has one `provider_id`. The one way a model id could be addressed by both is one id on two rows, which both flows already refuse before planning (`doc.Model(id)`). | `wt/internal/cloudsync/catalog.go` (`isOllamaCloud`, `findEntry`, `PlanCatalog`), `apply.go`; `TestTheTwoFlowsNeverShareAModel` (Task 2), which builds every combination of provider, location and name and checks both the predicates and the two plans. | observed |
| 31 | **A registry that still carries `openrouter_priced` loads, and a write keeps the key.** wt's registry decoder ignores a key `config.Provider` does not model; the writer patches the rows it is asked to and leaves every other key of every row where it was. After a real apply through the command, both provider rows still held their `openrouter_priced` line, in place. | `TestCloudSyncRefreshesOnlyTheOpenRouterProvidersModels` (Task 2); the live dry run of fact 36, whose registry carried the key. | observed |
| 32 | **modelman's `price_at`, the resolver the migration left unported, has six rules**, all in `modelman/src/modelman/time_pricing.py` at `e61d989`: (1) the instant must be timezone-aware; (2) the flat prices are the default; (3) rows are tried in order and the first row with a window that contains the instant wins; (4) that row supplies each price it sets, and a price it omits falls back to the flat one; (5) a window contains an instant when the instant, converted to the row's `timezone`, falls on one of its `days` and its minute of the day is in `[start, end)`; (6) with no such row the flat prices apply. Its tests are five on `price_at` and four on row validation and serialization (`modelman/tests/test_time_pricing.py`). | `git show e61d989:modelman/src/modelman/time_pricing.py`, `…:modelman/tests/test_time_pricing.py`. | observed |
| 33 | **The ollama flow's `off-peak` row is `timezone = "UTC"`, windows Monday to Friday 00:00–12:00 and 18:00–24:00 and Saturday and Sunday 00:00–24:00**, written by `offpeakRow`, not parsed from the page. The entry's flat price is the peak price. So an ollama cloud model is at its flat price only on weekdays from 12:00 to 18:00 UTC, 30 of the week's 168 hours. | `wt/internal/cloudsync/catalog.go`, `offpeakRow`. | observed |
| 34 | **At 80 columns the picker's COST column is not drawn when the table holds an OpenRouter-length id.** With `openrouter/deepseek--deepseek-v4-pro-0813` (41 characters) in the table, the launcher's picker at 80x24 drew FAMILY, MODEL, STATUS and RUNNING; at 40x12 MODEL alone, cut; at 120x24 everything up to 7D, COST included. This is `main`'s behaviour (columns are dropped whole, in the order SURVEY, 30D, 7D, 1D, COST, LOC, FAMILY), not a regression, and it is why Task 5 exists. | pty captures of a scratch build under `env -i` with a throwaway home and a made-up registry and `config.toml`, a stub `pi` on `PATH`; saved as `captures.txt` in the revision run's scratch. | observed |
| 35 | **The pickers after slice C, on a real pty, at two instants.** A scratch build whose `pickerNow` was set from an environment variable by a build-tagged file that is not part of this plan. Registry: deepseek stored as slice D stores it (flat 1.32/0.044/3.96, one `openrouter` row 0.66/0.022/1.98), an ollama cloud model with flat 0.5/0.05/2 and `off-peak` 0.25/0.025/1, and `z-ai/glm-5.2` at 0.06/0.059/6 with no row. **Monday 02:45 UTC** (deepseek dear, ollama off-peak): rows in the order ollama, deepseek, glm-5.2; mode line `LiteLLM: on   cost 0.25/0.025/1 ~`, and after one `down` `LiteLLM: on   cost 1.32/0.044/3.96 ~`; at 120 columns the heading `COST (~ varies by time)` over ` 0.2500  0.0250  1.0000~`, ` 1.3200  0.0440  3.9600~` and ` 0.0600  0.0590  6.0000`. **Monday 13:00 UTC** (deepseek cheap, ollama peak): rows in the order deepseek, ollama, glm-5.2; mode line `LiteLLM: on   cost 0.66/0.022/1.98 ~`, after one `down` `LiteLLM: on   cost 0.5/0.05/2 ~`; at 120 columns ` 0.6600  0.0220  1.9800~` and ` 0.5000  0.0500  2.0000~`. At 80x24 and at 40x12 the mode line was whole both times (at 40 columns `LiteLLM: on   cost 1.32/0.044/3.96 ~` is exactly the 36 columns between the picker's padding) and no line reached the right edge. | The same captures. | observed |
| 36 | **The final binary on the live list, once.** `wt cloud-sync --only openrouter --dry-run` under `env -i`, made-up registry (deepseek at its cheap level with no row, hy3 and claude-haiku-5.5 with no cost; the provider row carrying `openrouter_priced = true`), Friday 2026-10-09 16:55 UTC, when hy3's listed price was its after-16:00 one. Output: `openrouter:   openrouter/deepseek--deepseek-v4-pro-0813: 0.66/0.022/1.98 -> 1.32/0.044/3.96 (openrouter 0.66/0.022/1.98 mon-fri 00:00-01:00 04:00-06:00 10:00-24:00, sat-sun 00:00-24:00)`, `openrouter:   openrouter/tencent--hy3: no cost -> 0.132/0.033/0.528 (openrouter 0.0825/0.020625/0.33 mon-sun 16:00-24:00)`, `openrouter:   openrouter/anthropic--claude-haiku-5.5: no cost -> 0.1/0.01/0.5`; exit 0; the registry file unchanged. `--only prices --dry-run` printed `wt: --only: unknown flow "prices" (valid: openrouter, ollama)`, exit 1. | The run. | observed |
| 37 | **Mutations.** Nine mutations of the resolver were each caught by Task 3's tests: `<` for `<=` at a window's end and the reverse at its start; the instant read in UTC instead of the row's zone; the weekday off by one; the search going on after the first match; a row's missing cache price blanking the flat one; `Local` accepted as a zone; an end past 24:00 accepted; the wrong row index reported. The thirteen of fact 22 were run on the first run's code, which Tasks 7 and 8 carry over unchanged but for names. | Scratch runs. | observed |
| 38 | **Where wt shows a model's price.** The two pickers (the launcher's and the standalone one `wt start` and `wt smoke` open), through `renderTable`'s COST column, and nowhere else: `wt model list` and its `--json` carry no price (its JSON keys are `family`, `id`, `location`, `malformed`, `model_name`, `path`, `provider_id`, `registered`, `running`, `size_bytes`, `status`, `tags`, plus `target` and `draft` for a pairing); the Models tab of `wt config` shows id, status, tags and path, no price; its model form shows the row's own three prices because it edits them; `wt stats` reads LiteLLM's spend. | `grep -rn "PricePerMillion" wt/` outside tests; `wt model list --json` on the scratch registry; `wt/internal/configeditor/models_tab.go`, `models_form.go`. | observed |
| 39 | **The first revision's mode line was cut at 40 columns, mid-number and with no sign of the cut.** The footer is clipped to the 36 columns between the picker's padding with no ellipsis. On a pty at 40x12, a scratch build of that revision: `LiteLLM: on   cost 0.0825/0.020625/0` for tencent/hy3's after-16:00 price (output 0.33 read as 0, the mark gone); `LiteLLM: on   cost 0.132/0.033/0.528` (the mark gone, so a time-priced model looked fixed); `LiteLLM: off (direct)   cost 1.32/0.` under direct mode, for every model. Two of the three real time-priced models were cut under `LiteLLM: on`, and at 40 columns that line is the only place a price is shown. **After Task 5 as it is now**, same registry plus hy3, scratch build with the same clock seam: at 40x12, Monday 17:00 UTC, the line is `cost~ 0.0825/0.020625/0.33` (alone, LiteLLM on or off), and after `down` `LiteLLM: on   cost~ 0.66/0.022/1.98` (35 columns: it fits); at 80x24 `LiteLLM: on   cost~ 0.0825/0.020625/0.33` and, with LiteLLM off, `LiteLLM: off (direct)   cost~ 0.0825/0.020625/0.33`; Monday 02:45 and 13:00 UTC likewise with `cost~ 0.132/0.033/0.528`, `cost~ 0.25/0.025/1`, `cost~ 1.32/0.044/3.96`, `cost~ 0.66/0.022/1.98`. No capture shows a price cut short. | The two reviews' pty captures and scratch tests; this run's 21 captures (`captures.txt` in its scratch) and Task 6 Step 9 run as written on Friday 17:57 UTC. | observed |
| 40 | **A hand-written row that breaks a rule: before and after.** Row on `openrouter/acme--night-owl`: `timezone = "America/New_York"`, output 10.0, one window on all days `22:00` to `06:00`; flat 12.5/1.25/75. It loads (the loader types a row's values and checks no rule; `validateTimePrice` runs only on a row a write touches). First revision, pty at 120x50, at an instant inside the window as meant: ` 12.5000  1.2500 75.0000~`, the flat price with a mark that promised a change. And a row with one wrapping window and one good one was applied in the good one, against that revision's own "a row the validator would refuse holds no instant". **Now** (Task 3): at 120x24 the cell is `12.5000  1.2500 75.0000` with no mark at every instant; `wt model list` prints `openrouter/acme--night-owl: cost.time_prices[0]: windows[0]: start must be before end; wt reads it as absent (fix the entry in <registry>)`. **Writes to that model are refused, as on `main`:** `wt model edit openrouter/acme--night-owl --family owls` printed `wt: invalid registry entry: model "openrouter/acme--night-owl": cost: time_prices[0]: windows[0]: start must be before end` and changed nothing; and `wt cloud-sync --only openrouter --yes` over a stubbed fetch, with an update pending for a model carrying such a row, printed the plan, then `openrouter: error: registry.toml was not changed: invalid registry entry: model "openrouter/vendor--gpt": cost: time_prices[0]: windows[0]: start must be before end`, exit 1, registry unchanged (the dry run printed the plan and exit 0). The openrouter flow stamps every model it matches, so for a model of the `openrouter` provider that refusal comes on every apply until the row is fixed. | The review's captures; this run's captures, the `wt model edit` run under `env -i` on a scratch registry, and a throwaway test that drove the command with `cloudFetch` stubbed (not part of the plan). | observed |
| 41 | **A value of the wrong TOML type in a row stops every wt command.** `windows = "always"`: `wt model list` and `wt cloud-sync --only openrouter --dry-run` both printed `wt: config error: parse …/reg.toml: toml: line 140 (last key "models.cost.time_prices.windows"): incompatible types: TOML value has type string; destination has type slice (fix that file by hand)`; likewise `input_price_per_million = "cheap"`, `timezone = 5`, `days = "mon"`. Nothing fetched, the file unchanged. This is the typed decode of `config.TimePrice`, and it is `main`'s behaviour for a wrong type anywhere in the registry (only `fetch`, `draft` and `pricing_updated_at` are decoded leniently). | The review's runs of the scratch binary under `env -i`; `TestLoadStopsOnATimePricesValueOfTheWrongType` (Task 3), four shapes. | observed |
| 42 | **Changing which column a narrow table gives up first does not bring COST back at 80 columns.** With the 41-character id and a marked table: `main`'s order (SURVEY, 30D, 7D, 1D, COST, LOC, FAMILY) draws COST from 111 columns; a scratch build that gives up LOC and FAMILY before COST draws it from 94, and at 80x24 drew `MODEL`, `STATUS`, `RUNNING` only: FAMILY lost and still no COST. MODEL, STATUS and RUNNING are never given up, and beside them a 41-character id leaves 12 columns at 80; the COST cell is 23 or 24. | pty captures of two scratch builds at 80, 88 to 94 and 100 to 114 columns. | observed |
| 43 | **Two things made the pty driver report a line past the right edge on a screen that fits.** (a) It decoded each read by itself, so a three-byte character cut between two reads (`↑`, `↓`) became replacement characters and the line grew. (b) The first wt command in a new home, and the first after `config.toml` is replaced, prints the one-time line `wt: migrated config to native-provider alignment (renamed google→agy, rewired opencode to ollama-only)`, 102 columns, before the picker draws: every first shot in a fresh home reported `True`, and no shot after a first `wt model list` did. | The review's instrumented run (a); this run's instrumented driver, four fresh homes (b). | observed |
| 44 | **After the first revision's rename, 19 test functions, the fixtures `catalogPage`, `catalogPages`, `catalogPulled`, `catalogPlanText`, the sentences in the command's tests that call the ollama flow "the catalog" and the two bullets of `internal/cloudsync/doc.go` still named the flows by the old words**, and `wt/CLAUDE.md` quoted two of the test names. Task 1's script now renames them (part 3c); after it the two greps of Step 5 print one changelog line and two comment lines that mean ollama's catalog. | `grep` over the replayed slice-A tree, before and after. | observed |
| 45 | **Slice C does not need A, B or D.** Tasks 3 to 5 applied to `63f63d5` alone: `gofmt -l .` empty, `go vet ./...` clean, 25 packages `ok`. Task 6's docs need A's names and B's changelog entry. With the slices replayed in the order A, B, C, D the final tree differs from the first revision's final tree only where this revision meant it to (the renamed tests, the resolver's row rule, `Model.Malformed` and its docs, `modeLine`, the two new tests, and the sentences the reviews corrected). | The replays; `git diff --stat` of the two final trees (23 files). | observed |

- **Fact 35's mode line is the first revision's** (` ~` after the numbers, appended to the mode and clipped). Its rows, their order and the COST cells stand; the line is now as fact 39 has it, and "at 40x12 the mode line was whole both times" was true only of the prices in that registry.
- **Fact 34's width** is measured in fact 42: with such an id and a marked row the COST column is drawn from 111 columns.
- **Fact 36's binary** is the first revision's; Tasks 7 and 8 are its openrouter flow unchanged.

### What is still unknown

- **Whether the live proxy's `spend` column follows the route price for any real request.** Fact 11 is a stand-in: the reply shape for OpenRouter's Responses endpoint was invented, no paid request was sent, and the developer's database was not read. One real codex request to an `openrouter/*` model, with the logged spend compared with OpenRouter's own figure, would settle it. This plan does not depend on the answer: the route carries the dearest level either way.
- **How OpenRouter picks the "top provider"**, and whether that choice ever moves a time-priced model to a provider with no schedule (facts 23 and 24). It did not for the tencent models across their 16:00 boundary; deepseek's peak window was not watched. If it does, the model's `overrides` come and go and the sync reports an update each time, between a provider's flat price and the schedule's level with its rows.
- **How often multi-provider prices move over hours or days** (fact 7 is 41 minutes). Why `glm-5.2` and `kimi-k3` moved between the issue's two runs was not observed.
- **What OpenRouter sends for a time-of-day entry that omits a price.** None exists in the list. The plan refuses such a schedule (the missing price would inherit the top-level one).
- **Whether a whole-day `utc_days` entry with no window is a stable part of the public format.** OpenRouter's provider-side docs call that shape not billable yet; the live list publishes one, and the public guide's example has one. The plan reads it.
- **Time-of-day pricing on endpoints that are not the listed provider** exists (Alibaba's endpoints for two deepseek models) and is invisible in the model list. Not handled.
- **The owner's terminal width.** Facts 34 and 42 matter only if it is under 111 columns; under that the price is on the mode line (Task 5), whole or not at all.
- **A window boundary crossed while a picker is open** was not captured on a pty; it is pinned by test only (`TestAnOpenPickerKeepsThePricesItOpenedWith`: the clock moves across both models' boundaries under an open picker that is resized and moved in, and is read once).
- **The Models tab and the model form on a row that breaks a rule** were read in source (`malformedLine`, `saveErrorText`), not captured; `wt model list` and `wt model edit` were run (fact 40).
- **The owner's own registry was not read.** Which of its models are time-priced, and at which level each is stored today, is Task 10's one dry run.
- **A pre-existing oddity seen in the captures and not investigated:** with `wt --cwd --agent <name>` the picker keeps a `loading worktrees...` status line above the table. It is not this plan's, and nothing here touches the status.
- `make test-all` was not run for this plan; neither was the installed `wt`.

## The Design and Why

**The four slices are four different kinds of change, and that is why they are four PRs.** A is a rename that touches some 450 places and changes no behaviour; B removes a key and narrows one rule; C is the reader the rows never had; D is the fix the issue asks for, extended by D2. Reviewing any two as one diff would hide the small one inside the large one.

**Slice A, the rename.** The flows are named for their providers in every place a user meets them (`--only`, the line prefixes, `--help`, every message, the docs, the skill) and in the identifiers of `cmd/wt/cloudsync*.go` that name a flow (`cloudSyncOpts.openrouter`, `planOllamaFlow`, `ollamaGate`, `cloudSyncOutcome.ollamaCode`, …), the tests among them: `TestCloudSyncPricesDryRun` is `TestCloudSyncOpenRouterDryRun`, `TestCloudSyncCatalog…` is `TestCloudSyncOllama…`, the fixtures `catalogPages` and `catalogPulled` are `ollamaPages` and `ollamaPulled`, and the test sentences that called the flow "the catalog" say "the ollama flow" (fact 44). Two things keep their names, because they name something else: `internal/cloudsync`'s exported API (`PlanPrices` plans prices; `PlanCatalog` plans the mirror of ollama's catalog; `ParsePricing` parses the pricing page) and the three file names `cloudsync_catalog.go`, `cloudsync_ollama.go` (the ollama CLI seam, a name already taken) and their tests. A rename of files that another in-flight worktree is editing would buy nothing a reader needs. One message is reworded, because the rename made it read badly: `catalog: ollama at <address>` would become `ollama: ollama at <address>`, and is `ollama: daemon at <address>`.

**Slice B, the key.** `openrouter_priced` existed to override an inference ("any cloud provider that is not an agent's native one is priced by OpenRouter") that was itself a guess from the days when ollama cloud models had no published price. D4 removes both the key and the inference. What a registry that still carries the key does is decided by two things already true of wt: its registry decoder ignores a key the typed struct does not model, and its writer is patch-shaped and never deletes a key it was not asked to. So the key is tolerated and ignored, a write preserves it, and nothing warns about it (the owner's registry has none; a warning for a key nobody has is noise). The advice line that told the user to set the key now points at the warnings, which also makes it right for a refused schedule (old fact 28).

**Slice D, the schedule.** Unchanged in design from the first run, which the directions did not touch: `parseSchedule` paints each time-of-day entry onto a week of 10,080 minutes in list order, so a later entry wins an overlap as OpenRouter specifies and a wrapping window needs no special case; a minute left unpainted makes the schedule unusable; the painted week is read back as price levels with their spans per day, which makes the result independent of the order, grouping and wrapping of OpenRouter's entries. `mainRate` picks the level that becomes the flat price (the dearest); every other level is one row labelled `openrouter`, `timezone = "UTC"`. What changed with the directions: the rows are part of the fix, not a second PR (D2); they have a reader (D3), so their order, their windows and their per-field fallback now decide what the owner sees; and the both-flows case is gone (fact 30).

**Why the picker lands before the rows (C before D).** The first revision landed the rows first and asked that the two PRs "merge close together", because between them the picker would have shown every time-priced OpenRouter model at its dearest level at all hours: double deepseek's going price for 79% of the week. Nothing forces that order. The resolver and the picker need nothing from the sync (fact 45), and they have rows to apply on `main` today (ollama's `off-peak` row), so slice C is useful the day it lands. With C first there is no interim: the first sync that writes an `openrouter` row writes it for a picker that already reads it. The cost is in the docs only: slice C's sentences speak of ollama's row and rows written by hand, and slice D adds OpenRouter's to four of them (Task 9). `Closes #322` stays on the PR that fixes what the issue reports, which is now the last one.

**Why the dearest level stays the flat price, now that the picker shows the level in force.** The flat price has two jobs left. It is the fallback when no row's window holds the instant, and for the rows this plan writes that is exactly the dearest level's hours, so the picker is right at every hour whichever level is flat. And it is what LiteLLM's route carries at every hour, where the dearest level is the one choice under which a request priced from the route is not logged below its cost (when the dearest level is the highest on every price, which holds for the three schedules published today and is pinned by a test where it does not). The first run's objection to "dearest" was that the picker showed double deepseek's going price for 79% of the week; the picker of slice C, already in place when slice D lands, removes that objection.

**Slice C, the resolver.** modelman's `price_at` is ported rule for rule into `config.ModelCost.PriceAt` (fact 32), in the package that already owns `ModelCost`, `TimePrice` and the validator of the rows. It returns the three prices and the index of the row that supplied them (`-1` for the flat ones). Carried over, each with its test: the flat default, first match wins, per-field fallback, the half-open window to the minute, the row's own timezone. Dropped: "the instant must be timezone-aware", because a Go `time.Time` always is an instant. Added, because the Go function runs while a screen is drawn: it never fails, and daylight saving time is pinned on both of its days. `time.LoadLocation` is called per row per call; the rows the sync writes are all `UTC`, which Go answers without reading a file.

**Slice C, rows written by hand.** modelman validated a row when it loaded it, so `price_at` never met a bad one. wt's loader only types a row's values; the rules (an IANA zone, `HH:MM`, `start` before `end`, no negative price) are the registry writer's, run on a row a write touches. D3 makes rows worth writing by hand, and the likeliest one, an overnight window written `22:00` to `06:00`, breaks a rule. Three ways to treat it were weighed.

- *Read it as wrapping past midnight.* Rejected. The validator would still refuse the row, so wt would show a price from a row it refuses to write back, and every sync that touched the model would fail on it (fact 40). Relaxing the validator is a change to the registry's schema, which this plan does not otherwise touch, and a wrapped window's `days` are ambiguous (the day it starts on, or each day it touches).
- *Pass it over in silence* (the first revision). Rejected: the picker marked the model `~` and showed the flat price for ever, and nothing said why (fact 40).
- *Pass it over and say so* (built). The resolver asks the validator itself (`TimePrice.Problem` calls `validateTimePrice` on the typed row), so the reader applies exactly the rows the writer accepts and the two cannot drift. The rule is per row: one bad window, or one negative price, and the whole row is out, because a row applied in some of its windows only shows a price its writer did not mean, and a row price of `-1` would otherwise sort the model first and make it the default selection. `Model.Malformed`, which already carries what the loader tolerated in `fetch` and `draft`, names the row and the rule; so `wt model list` prints it (stderr and `--json`), the Models tab shows it, and the model form's refused save points at the file. A model with no row that applies is not time-priced and gets no mark.

A value of the wrong TOML type is not a row that breaks a rule: it fails the typed decode and the whole load, with the line and the key named (fact 41). Teaching the loader to drop such a row and carry on, as it does for `fetch` and `draft`, was weighed and not done: those two tables are only shown, while a dropped price row would silently change what the picker sorts by, and a loud parse error at the line is the better answer to a typo in a price. It is pinned, and the docs say it.

**Slice C, the picker.** Three decisions, each forced by something observed.

- *One clock per build, not per draw.* The cost sort is also the default selection. A table re-priced on every redraw would reorder rows under the cursor at a window boundary, between the user reading a row and pressing Enter. So `buildRows` reads the clock once, gives every row that instant, and nothing re-reads it until the table is rebuilt (on opening the picker, and after a start attempt). A picker left open across a boundary keeps the prices it opened with; that is stated in the docs.
- *The mark is one character, after the cell.* The COST cell is three right-aligned numbers; its first character is a digit for a price of 10 or more, so the mark cannot go in front without shifting the numbers. `~` after the cell widens the column by one, only in a table that has a time-priced model, and the heading of such a table becomes `COST (~ varies by time)`, which is 23 characters in a 24-character column: it explains the mark on the screen that shows it and never widens the table. The mark says what the model is (it has rows), not what hour it is, so it is there in the dear window and in the cheap one alike: both prices will change.
- *The price is also on the mode line, whole or not at all.* Facts 34 and 42: at 80 columns, with an OpenRouter id in the table, there is no COST column to show a current price in, and no reordering of the columns brings it back. The line under the table already says `LiteLLM: on`; it now also names the highlighted model's price, `cost~ 0.66/0.022/1.98`. It costs no line, so no frame's height changes and the fit rules are untouched. The first revision appended the price and let the footer's clip cut it, which at 40 columns showed wrong-looking prices with no mark (fact 39). `modeLine` now takes the width: mode and price when both fit; the price alone when they do not, because the mode is the same on every row and the price is what is being chosen by; the mode alone when the price cannot fit even by itself. The mark is in front of the numbers (`cost~`), where no cut can part it from them. This is the one thing in the plan the owner did not ask for in so many words; it is question 1, and Task 5 is separable.

**What is deliberately not done.** The route does not follow the clock (the owner did not ask, and LiteLLM's own `off_peak_pricing` holds one off-peak rate, fact 14). `wt model list` gets no price column (it has none today, and it is not where a model is picked); what it gains is the line that names a row which is not applied. The registry's validator and loader are not changed. The floating top-level price of multi-provider models (fact 7) is untouched: it is the larger share of "the price changed between two syncs", and it is a proposed follow-up, not this plan.

**Rejected again: warn and document only** (the issue's option 3). The stored price would still follow the clock, which the issue rules out, and a warning keyed on "has overrides" would name 82 models wrongly (fact 1).

## Decisions This Plan Makes

Each choice is pinned by a test, so a reviewer who disagrees changes one place. Two are pinned by something else, and say so: decision 2's "does not reach" half by a grep and the build, and decision 7 by the build (a removed parameter).

| # | Decision | Why | Pinned by |
|---|---|---|---|
| 1 | The flows are `openrouter` and `ollama` in `--only`, in every line's prefix, in `--help` and in every message; the old names are unknown flows, with no alias and no deprecation notice. | D1, and the controller's reading of it. | `TestCloudSyncFlowsAreNamedForTheirProviders`; `TestSelectFlows`, `TestCloudSyncCommandRefusals` (renamed in place) |
| 2 | The rename reaches every identifier that names a flow in `cmd/wt/cloudsync*.go`, tests included (the `TestCloudSync…` names, the subtest names, the fixtures `ollamaPage`, `ollamaPages`, `ollamaPulled`, `ollamaPlanText`), the two bullets of `internal/cloudsync/doc.go`, and all prose. It does not reach `internal/cloudsync`'s exported names or any file name. | D1 says tests and code identifiers. The exported names say what is planned or parsed, not which flow (`PlanPrices` plans prices; `PlanCatalog` plans the mirror of ollama's catalog); a file rename would collide with a worktree in flight. | Task 1 Step 5's two greps, with their expected output; the build; the renamed tests still passing |
| 3 | `ollama: ollama at <address>` is `ollama: daemon at <address>`. | The only line the rename made read badly. | The existing ollama-work tests, whose expected text the script rewrites |
| 4 | A model is OpenRouter-priced when its `provider_id` is `openrouter`, also when the registry has no `openrouter` provider row; no other model is, whatever its provider's location, auth or name. | D4. The missing-row case keeps today's behaviour for that id. | `TestCatalogPredicatesFixture`, `TestOpenRouterPricedMatchesTheContract` (one list for both readers); `TestHasOpenRouterPricedModel`; `TestPlanPricesCandidatesAndWarnings` |
| 5 | A registry that still carries `openrouter_priced` loads; the key decides nothing; a write keeps it; nothing warns. | Fact 31. The owner's registry has none. | `TestCloudSyncRefreshesOnlyTheOpenRouterProvidersModels`; the contract fixture, which keeps both rows that carry the key |
| 6 | The line printed when no model could be refreshed ends `the warnings above say why for each model`. | It named the removed key; and it was wrong for a `-1` price and for a refused schedule. | `TestCloudSyncSaysWhenNoModelCouldBeRefreshed` |
| 7 | `cloudsync.PlanPrices` loses its `providers` parameter; `cloudsync.Provider` is reduced to its `ID`. | Nothing reads the rest. | The build |
| 8 | No row is in both flows' scope, and the plan relies on it: the one shared write applies two plans made from the same reading of the registry. | Fact 30. | `TestTheTwoFlowsNeverShareAModel` |
| 9 | The flat price of a time-priced model is the level with the highest output price, then input, then cached input. It is one level's three prices, not a per-field maximum. It is the fallback and what the route carries. | "The Design and Why". `mainRate` is the only place that says so. | `TestParseOpenRouterTimeOfDay`; `TestParseOpenRouterTimeOfDayShapes` (three levels; crossed levels) |
| 10 | The top-level price of a time-priced model is not read. For one registry and one published schedule the plan is the same text whenever the list was fetched, and after an apply no later fetch of that schedule finds a price to update. | The issue's requirement. | `TestPlanPricesDoesNotFollowTheClock` (336 fetch times across a week); `TestCloudSyncTimeOfDayPricesDoNotFollowTheClock` |
| 11 | The exception: when no time-of-day entry has a cached-input price, the top-level one is kept as the cache price, and refuses the model if it is no price. | Then it does not depend on the hour. | `TestParseOpenRouterTimeOfDayShapes` (the weekly example; the top-level `-1` cases) |
| 12 | An `overrides` entry is a time-of-day entry only when it has `utc_start`, `utc_end` or `utc_days`. Every other entry is ignored without a word, the 82 prompt-size tiers included. | Fact 1. | `TestParseOpenRouterTimeOfDay` (claude-haiku-5.5); `TestParseOpenRouterTimeOfDayShapes` (six ignored shapes) |
| 13 | Windows are HHMM, half open, wrapping when the end is not after the start; no window is the whole day; no `utc_days` is every day; a later entry wins an overlap. | OpenRouter's documented rules (fact 2). | `TestParseOpenRouterTimeOfDayShapes`; `TestFetchedAtIsWhatOpenRouterSent` |
| 14 | Refused as unusable: a gap in the week; a key on a time entry that is neither a time key, nor a documented price key, nor a key the model's pricing object has at its top level; `min_prompt_tokens` on a time entry; no prompt or no completion price; a price that is negative or not a number; a cached-input price on some windows only; one of `utc_start`/`utc_end` without the other; an HHMM or a day name that is not one. The model keeps its price, its rows and its stamp, with `warning: Could not use OpenRouter's time-of-day pricing for <id>: <reason>`. | Each leaves the price at some hour unknown; a fallback to the top-level price would bring the bug back. | `TestParseOpenRouterTimeOfDayShapes` (22 refusal cases); `TestPlanPricesLeavesAModelWhoseScheduleItCannotRead` |
| 15 | Every level other than the flat price is one row: `label = "openrouter"`, `timezone = "UTC"`, its prices, its windows. Days with the same times share windows; a window crossing midnight is two; the end of a day is `24:00`. Rows are in price order, dearest first; windows in day then time order. | The registry's vocabulary and validator (fact 16). The label is the flow's name, which D1 makes the natural word. | `TestScheduleRows`; the file's bytes in `TestPricesApplyStoresAScheduleOnce` |
| 16 | The openrouter flow owns the rows labelled `openrouter` on the models it matched: it replaces them where the first one stood, adds them at the end when there was none, and removes them when the model has no schedule. Every other row is the table it was read as. | Rows resolve first match wins, so a row is not moved. | `TestPlanPricesOwnsOnlyItsOwnRows`; `TestPricesApplyStoresAScheduleOnce` |
| 17 | A plan line prints each `openrouter` row after the flat price: ` (openrouter <in>/<cached>/<out> <days> <start>-<end> …, <days> …)`, with ` timezone="<zone>"` when the zone is not UTC and ` +keys` when the row or a window has a key the sync does not write. | Rows are compared whole, so whatever can make a row differ shows on the line. | `TestPricePlanFormatShowsASchedule`; `TestPlanPricesReplacesAnOpenRouterRowWhateverItHolds` |
| 18 | A change to the rows alone is a price update: listed, counted in `N price(s) changed`, the routes synced. That sync leaves `config.yaml` as it was and restarts nothing. | The existing `sameCost` rule; a route is built from the flat price only. | `TestPricingInfoReadsOnlyTheFlatPrices`; `TestCloudSyncRowsOnlyUpdateLeavesTheRoutesAlone` (the real route sync) |
| 19 | The tests' "fetched at another time" bodies are made from the one saved response by a helper written from OpenRouter's guide, not from the code under test. | A second, independent reading of the window rules; no made-up prices. | `TestFetchedAtIsWhatOpenRouterSent` |
| 20 | The resolver is `config.ModelCost.PriceAt(at) PriceInForce{Input, Cache, Output, Row}`: first matching row wins, per-field fallback to the flat price, windows half open to the minute on the row's wall clock. `Flat()` is the same shape with no row applied; `TimePriced()` is "has a row that is applied". | Fact 32, and "The Design and Why". | `TestPriceAtOffpeakBoundaries`, `…ReadsAWindowInItsRowsTimezone`, `…FollowsDaylightSavingTime`, `…FallsBackPerField`, `…FirstMatchingRowWins`, `…AnOpenRouterSchedule`, `TestFlatAndTimePriced` |
| 21 | Daylight saving time: a window is read on the wall clock of its row's zone. The repeated hour is in the window both times; the skipped hour at no instant. | What a user who writes `timezone = "America/New_York"` means; what modelman did. | `TestPriceAtFollowsDaylightSavingTime` |
| 22 | The picker's clock is `pickerNow` (a package variable, `time.Now`), read once per table build; `tableInput.now` overrides it. Every row of a table is priced at that one instant. A row built by hand with no instant is priced flat. | One instant per table keeps the sort consistent; the seam is how tests fix the hour. | `TestBuildRowsReadsThePickersClock` |
| 23 | The cost sort compares the price in force (output, then input), so a time-priced model's place, and the first row, can differ from hour to hour. Ties are broken as before (7-day usage, then id), so the order at one instant is total and stable. | D3: the order is how the owner finds the cheapest model now. | `TestSortRowsUsesThePriceInForce` |
| 24 | The table is priced when it is built: on opening the picker and when it is rebuilt after a start attempt. Not on a redraw, a resize or a cursor move. A picker open across a boundary keeps its prices and order; the docs say to reopen it. | Rows must not move under the cursor. | `TestBuildRowsReadsThePickersClock` (one reading per build); `TestAnOpenPickerKeepsThePricesItOpenedWith` (the clock crosses both boundaries under an open picker through four resizes and eight cursor moves: one reading, the same order, the same cells) |
| 25 | A COST cell of a model with a row that is applied ends `~`, in every window; a table with such a cell has the heading `COST (~ varies by time)` when the column is at least that wide; a model with one price has no mark, and neither has one whose only rows break a rule; a discovered row shows `-` and no mark. | "The Design and Why". | `TestRenderTableShowsThePriceInForce` |
| 26 | The launcher's mode line names the highlighted row's price in force, `cost <in>/<cached>/<out>` in short numbers (six significant digits), as `cost~ …` when the model is time-priced; nothing for a row with no per-token price in force or a discovered row. **The price is on the line whole or not at all:** with the mode when both fit the line's width, alone when they do not (the mode gives way), and not at all when it does not fit alone. At 40 columns (36 for the line) that is: beside `LiteLLM: on` for a price of up to 22 characters with its word, alone for a longer one and for every price under `LiteLLM: off (direct)`. The standalone picker (`wt start`, `wt smoke`) has no such line and gets the COST column only. | Facts 34, 39 and 42. A clipped price reads as another price. The standalone picker has no footer line to put it on, and `wt start` lists local models, which have no per-token price. | `TestModelPickerNamesTheHighlightedModelsPrice` (three sizes, LiteLLM on and off, two instants, five models with the real schedules' longest prices; any line that holds `cost` must be the whole expected line; `modeLine`'s nine cases) |
| 27 | `wt model list` and its `--json` get no price: they carry none today, so no field's meaning changes and none is added. Their one change is decision 30's: the `malformed` lines and array can now name a `cost.time_prices` row. The Models tab's table is not changed. The model form goes on showing and editing the row's own (flat) prices. | Fact 38. A form that showed the price in force would write a window's price over the flat one on save. | `TestModelFormEditsTheFlatPriceOfATimePricedModel`; `TestModelListNamesATimePricesRowThatIsNotApplied` |
| 28 | A LiteLLM route carries the flat price at every hour. | The owner did not ask for the route to follow the clock. | `TestPricingInfoReadsOnlyTheFlatPrices` |
| 29 | The float conversion is not changed. | Rounding would rewrite a price on every OpenRouter-priced model once. | The stored digits in `TestPricesApplyStoresAScheduleOnce` |
| 30 | A row is applied only when the registry's validator accepts it, and the rule is per row: one window that ends before it starts (an overnight `22:00` to `06:00` is two windows), a timezone that is no IANA name, a time that is not `HH:MM`, a price below zero or not finite, and the whole row is passed over at every instant, and the rows after it are still tried. `TimePrice.Problem` is the validator's own answer for the typed row. Such a row is named: `Model.Malformed` gives `cost.time_prices[<i>]: <reason>`, which `wt model list` prints on stderr and in `--json`, and the Models tab shows. A model none of whose rows is applied is not time-priced: flat price, no mark. | Fact 40, and "The Design and Why" (three ways weighed). A wrapping window is not read as wrapping because wt refuses to write such a row back. | `TestPriceAtIgnoresARowItCannotRead` (15 rows, one of them at an instant its writer meant); `TestTimePriceProblemIsTheValidatorsRefusal` (the words, and that they are `validateTimePrice`'s); `TestLoadKeepsATimePricesRowTheValidatorRefusesAndNamesIt`; `TestModelListNamesATimePricesRowThatIsNotApplied` (the real listing) |
| 31 | A value of the wrong TOML type in a `cost.time_prices` row fails the registry load, for every wt command, with the parse error that names the line and the key. The loader is not taught to drop such a row. | Fact 41. It is `main`'s behaviour for a wrong type anywhere in the registry; a dropped price row would change what the picker sorts by without a word, and the parse error is exact. | `TestLoadStopsOnATimePricesValueOfTheWrongType` (four shapes) |
| 32 | The validator is not relaxed, so a registry write that touches a model with a row that breaks a rule is refused, as on `main`: `wt model edit`, a save in `wt config`, and a `wt cloud-sync` apply with anything to write for that model (for a model of the `openrouter` provider, every apply, and the refusal stops the whole write). The docs say so where they give the rules for a hand-written row. | Fact 40. The refusal names the model, the row and the rule, and `wt model list` names it before any sync does. | The existing validator tests (not changed); the docs of Task 6 Step 1 |
| 33 | The slices land A, B, C, D: the picker (C) before the rows (D). `Closes #322` is on D, the last PR. | "The Design and Why": no interim in which the picker shows a time-priced OpenRouter model at its dearest level all week. | Fact 45 (Tasks 3 to 5 green on `main` alone); the replay in that order |

## Questions for the Owner

The directions settle the rest. Each recommended answer is what the tasks implement.

> **The owner's answers, 2026-10-09 (binding).**
>
> 1. **(a), as built:** the highlighted model's price is shown on the `LiteLLM:` line under the table.
> 2. **A window written with its end at or before its start runs past midnight.** This reverses the recommendation below: `22:00` to `06:00` is one window from 22:00 on the listed day to 06:00 the next day, accepted by the registry's validator and applied by the resolver. The tasks are being revised to this; until they are, Decisions and Tasks 3 and 8 still describe the old rule (such a row is not applied).
> 3. **Yes:** the controller posts the correcting comment on #322 when slice D's PR opens, and files the follow-up issue about listed prices that move within minutes.

1. **Where the current price is shown on a terminal too narrow for the COST column.** With an OpenRouter id in the table the COST column is drawn only from 111 columns (facts 34, 42), today as before. Three ways to show the price below that:

   - **(a) On the line that says `LiteLLM: on`, for the highlighted model (Task 5; built).** Captured at 80x24, Monday 13:00 UTC, illustrative registry:

     ```text
           FAMILY    MODEL                                      STATUS  RUNNING

           hy        openrouter/tencent--hy3                    ok      -
           deepseek  openrouter/deepseek--deepseek-v4-pro-0813  ok      -
           glm       ollama/glm-5.3:cloud                       ok      -

         ↑/k up • ↓/j down • / filter • q quit • ? more
       LiteLLM: on   cost~ 0.132/0.033/0.528
       [↑/↓] navigate   [enter] launch or start   [q] quit
     ```

     At 40 columns the mode and most prices do not fit one line, so the price takes it and `LiteLLM: on` is not shown while a priced model is highlighted (captured at 40x12, Monday 17:00 UTC):

     ```text
           MODEL                         …

           openrouter/tencent--hy3        …
           openrouter/deepseek--deepseek-v…

         ↑/k up • ↓/j down • / filter …
       cost~ 0.0825/0.020625/0.33
       [↑/↓] navigate   [enter] launch or s
     ```

   - **(b) The column only.** Drop Task 5. Under 111 columns the picker then shows no price for such a table, though it still sorts by the current one.
   - **(c) Keep a price column at 80 columns by changing the table.** Not built. Changing which column is given up first does not do it: a scratch build that gives up LOC and FAMILY before COST still drew no COST at 80 columns, and lost FAMILY (fact 42). Beside MODEL, STATUS and RUNNING a 41-character id leaves 12 columns, so it would take a different, narrower cell (the output price alone, say), which is a redesign of the table and of its sort legend.

   Where the COST column is drawn (120 columns here), it reads:

   ```text
   STATUS  RUNNING  COST (~ varies by time)   1D  7D
   ok      -         0.1320  0.0330  0.5280~  0   0
   ok      -         0.6600  0.0220  1.9800~  0   0
   ok      -         0.0600  0.0590  6.0000   0   0
   ```

   **Recommendation: (a)**, as built, with the mark and heading as shown. For (b), Task 5's preamble lists what to leave out. (c) would be a follow-up. If you want another mark or other words than `~`, `cost~` and `COST (~ varies by time)`, they are two constants in `wt/internal/tui/modeltable.go` and one word in `costNote`.
2. **A window you write as `22:00` to `06:00`.** The registry's rule is that a window starts before it ends, so an overnight window is two. wt does not apply a row that breaks the rule, and `wt model list` names it (decision 30). The other reading, "it wraps past midnight", would need the registry's validator relaxed too, or every write to that model would still be refused. **Recommendation: as built** (not applied, named, the docs give the two-window form).
3. **May the controller post the correcting comment on #322 and file the follow-up issue?** The comment (3 time-priced models, not 85; OpenRouter documents the window and top-level semantics; the other price changes are a different cause) is in Task 10. The follow-up is "the listed price of a model many providers serve moves within minutes" (fact 7: 8 of 469 models in 41 minutes, one by about 42 times in five), which a sync still reports as updates and can still restart the proxy for. **Recommendation: yes to the comment when slice D's PR is opened; file the follow-up, and do nothing about it in this plan.**

## File Structure

| File | Change | Task | Responsibility |
|---|---|---|---|
| `wt/cmd/wt/cloudsync_flownames_test.go` | create | 1 | The flows' names, through the command line: `--only`, the prefixes, `--help`, the old names refused |
| `wt/cmd/wt/cloudsync.go`, `cloudsync_catalog.go`, `cloudsync_ollama.go` and their three `_test.go` files | modify (script) | 1 | Flow names in strings and identifiers, test names and fixtures included |
| `wt/internal/cloudsync/*.go`, `wt/internal/config/config.go`, `wt/internal/agents/price_notice*.go`, `wt/cmd/wt/main.go`, `exitcode.go` | modify (script, comments only) | 1 | "the prices flow" / "the catalog flow" in comments; the two bullets of `doc.go` |
| `wt/docs/wt-cloud-sync.md`, `wt/.claude/skills/cloud-sync/SKILL.md`, `wt/CLAUDE.md`, `CLAUDE.md`, `wt/docs/internals/launch-flow.md`, `docs/guides/00-config-map.md`, `docs/guides/02-providers-and-models.md`, `wt/CHANGELOG.md` | modify | 1, 2, 6, 9 | The flows' names (1); the removed key (2); what the picker shows and what a hand-written row needs (6); what is stored (9) |
| `docs/contracts/catalog-predicates.sample.toml`, `.expected.json` | modify | 2 | The one list of OpenRouter-priced models both readers are held to |
| `wt/internal/config/config.go` | modify | 2, 3, 6, 9 | `Provider` loses `OpenRouterPriced`; `Config.OpenRouterPriced` is one comparison (2); `Model.Malformed` names a row that is not applied (3); the comment on `TimePrice` (6, 9) |
| `wt/internal/config/registry_doc.go` | modify | 2 | The removed key leaves the writer's key-order list |
| `wt/internal/cloudsync/entry.go`, `openrouter.go` | modify | 2, 7, 8 | `Provider{ID}`, `OpenRouterPriced(e)`, `PlanPrices(entries, api)` (2); `APIPrice.Rates`, `.Unusable`, the schedule in `ParseOpenRouter` and `PlanPrices` (7); the rows (8) |
| `wt/cmd/wt/cloudsync.go` | modify | 2 | The predicate's new signature; the advice line |
| `wt/internal/agents/price_notice.go`, `price_notice_test.go`, `wt/internal/cloudsync/openrouter_test.go`, `apply_test.go`, `wt/cmd/wt/cloudsync_test.go` | modify | 2, 7, 8 | The rule's tests (2); the command run in two windows (7) and with rows (8) |
| `wt/internal/config/price_at.go`, `price_at_test.go` | create | 3 | The resolver: `PriceInForce`, `ModelCost.PriceAt`, `.Flat`, `.TimePriced`, `TimePrice.Problem`; its tests, and the loader's two (a row that breaks a rule, a value of the wrong type) |
| `wt/cmd/wt/model_list.go`, `model_list_test.go` | modify (one help sentence; append) | 3 | `wt model list` names a row that is not applied |
| `wt/internal/tui/modelrows.go`, `modeltable.go` | modify | 4, 5 | `pickerNow`, `tableRow.at`, `tableRow.price`, `tableInput.now`, the sort key, `costCell`, the heading (4); `costNote` (5) |
| `wt/internal/tui/model_list.go`, `layout.go` | modify | 5 | `modelItem.cost`; `modeLine` and the mode line |
| `wt/internal/tui/modelrows_test.go`, `modeltable_test.go`, `layout_test.go` | modify (append) | 4, 5 | Sort, clock, cells, fit at every size, an open picker (4); the mode line (5) |
| `wt/internal/configeditor/models_form_test.go` | modify (append) | 5 | The model form edits the flat price |
| `wt/docs/wt-model.md`, `wt/docs/internals/local-models.md`, `tui.md`, `config-and-registry.md`, `docs/guides/06-wt-agents-and-models.md` | modify | 6 (guide 06 also 9) | The COST column, the sort, the clock, the mode line; the row that is not applied |
| `wt/internal/cloudsync/testdata/openrouter_models.json` | create | 7 | Five models of OpenRouter's public list as fetched 2026-10-09 14:06:14 UTC, cut to `id` and `pricing`: three time-priced, one tiered, one plain |
| `wt/internal/cloudsync/timeofday.go`, `timeofday_test.go` | create | 7 | Reading a schedule: `Span`, `Rate`, `parseSchedule`, `isPriceKey`, `mainRate`; the fixture helpers and the parse and plan tests |
| `wt/internal/cloudsync/timeofday_rows.go`, `timeofday_rows_test.go` | create | 8 | Storing a schedule: `OpenRouterLabel`, `rateRow`, `withOpenRouterRows`, `formatOpenRouterRow`, `formatWindows`, `formatDays`; row, ownership, plan-text and Apply tests |
| `wt/internal/cloudsync/catalog.go` | modify | 8 | `formatCost` prints the `openrouter` rows; the `OffpeakLabel` comment |
| `wt/internal/litellm/entry_test.go` | modify (append) | 8 | A route is priced from the flat price only |

Not changed by any task: `wt/internal/cloudsync/apply.go`, the registry validator (`registry_validate.go`; the resolver calls it), the registry loader, `wt/internal/litellm/entry.go`, `wt/internal/modeladmin`, `wt/internal/configeditor/*.go` outside one test, llmbench.

## PR Slices

| Slice | Branch | Tasks | Needs | Ships |
|---|---|---|---|---|
| A | `refactor/322-cloud-sync-flow-names` | 1 | nothing | The rename, its test, the docs, the skill, the changelog. `Refs #322`. |
| B | `refactor/322-drop-openrouter-priced` | 2 | A merged | The key removed, the rule simplified, the fixture, the docs. `Refs #322`. |
| C | `feat/322-picker-price-in-force` | 3, 4, 5, 6 | B merged for Task 6; nothing for 3 to 5 | The resolver and the picker: every `cost.time_prices` row is applied where a model is picked. `Refs #322`. |
| D | `fix/322-cloud-sync-time-of-day-prices` | 7, 8, 9, then 10 before it merges | C merged | The fix: a time-priced model is stored as its schedule, for a picker that already shows the level in force. `Closes #322`. |

**Order: A, B, C, D, one after the other.** A, B and D edit the same files (`cmd/wt/cloudsync.go`, `cloudsync_test.go`, `internal/cloudsync/openrouter.go`, `wt-cloud-sync.md`), and each one's "find" texts are the one before's output. C's docs (Task 6) are written against B's, and D's docs (Task 9) against C's. `main` is never half renamed: A is one commit, applied by a script and checked by two greps.

**Why C is before D.** So that there is no time in which `wt cloud-sync` has stored a model at its dearest level and the picker shows that level at every hour. With C merged first, ollama's `off-peak` rows and rows written by hand are applied at once, and OpenRouter's rows are read by the picker from the first sync that writes them. If D is ready first, it waits for C; do not merge D before C.

**What can be built in parallel.** Tasks 3, 4 and 5 (slice C's code) need nothing from A or B (fact 45) and can be built in a second worktree from `main` while A and B are in review, in the order 3, 4, 5. The one file they share with A and B is `wt/internal/config/config.go`, in different places (`Model.Malformed`; A changes comments, B the `Provider` struct), so the rebase onto B is clean. Task 6 (slice C's docs) is done after B has merged and the branch has been rebased onto it. Tasks 7 to 9 start from `main` after C has merged.

**Each slice is true by itself.** After A the docs name the flows. After B they say the key is gone. After C they say the picker applies `cost.time_prices` rows, which are then ollama's `off-peak` row and rows written by hand; nothing in them speaks of OpenRouter rows that do not exist yet (a few test fixtures use OpenRouter's published schedules as their example, and the pty step's made-up registry holds such rows, written by hand). After D they say the openrouter flow stores a schedule as rows and that the picker shows them.

Each task ends green and is committed by itself. Task 10 and every push and PR step are the controller's and the owner's.

---

### Task 1: Name the flows `openrouter` and `ollama` (slice A)

A rename and nothing else: after this task the command does exactly what it did, under the new names. It is one commit, so `main` is never half renamed.

Branch: `git switch -c refactor/322-cloud-sync-flow-names main` (from an up-to-date `main`).

**Files:**
- Create: `wt/cmd/wt/cloudsync_flownames_test.go`
- Modify, by the script of Step 3: `wt/cmd/wt/cloudsync.go`, `cloudsync_catalog.go`, `cloudsync_ollama.go`, `cloudsync_test.go`, `cloudsync_catalog_test.go`, `cloudsync_ollama_test.go` (strings and identifiers); `wt/cmd/wt/main.go`, `exitcode.go`, `wt/internal/cloudsync/*.go`, `wt/internal/config/config.go`, `wt/internal/config/registry_doc_test.go`, `wt/internal/agents/price_notice.go`, `price_notice_test.go` (comments); `wt/docs/wt-cloud-sync.md`, `wt/docs/internals/launch-flow.md`, `wt/.claude/skills/cloud-sync/SKILL.md`, `wt/CLAUDE.md`, `CLAUDE.md`, `docs/guides/00-config-map.md`, `docs/guides/02-providers-and-models.md`, `wt/CHANGELOG.md`
- Modify, by hand: `wt/CHANGELOG.md` (one entry)

**Interfaces:**
- Consumes (existing, `cmd/wt`): `cloudSyncCmd(a *app) *cobra.Command`; `type app struct{ cfg *config.Config; loadErr error; … }`; test helpers `cloudSyncHome(t, registry string) (path string, cfg *config.Config)`, `stubCloudFetch(t, pages map[string]string)`, `exitCodeOf(err error) int`; the constants `cloudSyncRegistry`, `openRouterBody`; `cloudsync.OpenRouterModelsURL`.
- Produces (every later task uses these names):
  - flow names `openrouter` and `ollama`: `--only openrouter,ollama`; line prefixes `openrouter: ` and `ollama: `; `cloudSyncFlows = []string{"openrouter", "ollama"}`
  - `cloudSyncOpts{openrouter, ollama bool; dryRun, yes bool; force bool; htmlFile, approve string}`
  - `planOpenRouterFlow(ctx, out, errOut, doc, res) *openRouterRun`, `planOllamaFlow(ctx, out, errOut, cfg, o, doc, res) *ollamaRun`
  - `type openRouterRun struct{ api map[string]cloudsync.APIPrice; plan *cloudsync.PricePlan; printed string }`; `type ollamaRun` (its parsed page is the field `page cloudsync.Catalog`)
  - `ollamaGate`, `ollamaTouchedIDs`, `cloudSyncOutcome.ollamaCode`, `.ollamaWhy`, `ollamaCodeMeaning`
  - `cloudSyncApplied{openrouter cloudsync.PricesApplied; ollama cloudsync.CatalogApplied; openrouterStale, ollamaStale bool}`
  - messages: `--only: unknown flow "<name>" (valid: openrouter, ollama)`; `--<flag> is for the ollama flow, which --only openrouter leaves out`; `wt: cloud-sync: the ollama flow changed nothing: <why>`; `ollama: daemon at <address>`; `Nothing was changed by the ollama flow.`; `ollama: error: the ollama flow's changes were not written: …`
  - test names: `TestCloudSyncPrices…` is `TestCloudSyncOpenRouter…` (four tests), `TestCloudSyncCatalog…` is `TestCloudSyncOllama…` (twelve), `TestCloudSyncCatalogOllamaFailuresExit1AndTheRestStillRuns` is `TestCloudSyncOllamaCLIFailuresExit1AndTheRestStillRuns`, `TestCloudSyncSkipsTheCatalog…` is `TestCloudSyncSkipsOllama…` (two); the test fixtures `catalogPage`, `catalogPages`, `catalogPulled`, `catalogPlanText` are `ollamaPage`, `ollamaPages`, `ollamaPulled`, `ollamaPlanText`
  - unchanged: every exported name of `wt/internal/cloudsync`, every file name

- [ ] **Step 1: Write the failing test**

Create `wt/cmd/wt/cloudsync_flownames_test.go`. It is a new file, not an addition to `cloudsync_test.go`, because it spells the old names on purpose and the script of Step 3 rewrites those in the files it is given.

```go
package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cloudsync"
)

// TestCloudSyncFlowsAreNamedForTheirProviders pins the flows' names: each is
// named for the provider it syncs, openrouter and ollama, in --only, in the
// prefix of every line and in --help. The names they had for the command's
// first two days, prices and catalog, are not aliases: they are unknown
// flows, an error that lists the valid names, so a script still using one
// stops instead of running a flow it did not ask for.
func TestCloudSyncFlowsAreNamedForTheirProviders(t *testing.T) {
	// A registry with an openrouter model and no ollama provider row: the
	// openrouter flow has a plan to print and the ollama flow its skip line,
	// so both prefixes are seen with one stubbed page.
	registry, _, _ := strings.Cut(cloudSyncRegistry, "[[models]]\nid = \"ollama/")
	registry = strings.Replace(registry, "[[providers]]\nid = \"ollama\"\nname = \"Ollama\"\nlocation = \"local\"\n\n[providers.auth]\ntype = \"none\"\nbase_url = \"http://127.0.0.1:11434\"\n\n", "", 1)
	if strings.Contains(registry, "ollama") || !strings.Contains(registry, "openrouter/vendor--gpt") {
		t.Fatalf("the fixture is not one openrouter model and no ollama row:\n%s", registry)
	}
	_, cfg := cloudSyncHome(t, registry)
	stubCloudFetch(t, map[string]string{cloudsync.OpenRouterModelsURL: openRouterBody})
	run := func(args ...string) (string, error) {
		cmd := cloudSyncCmd(&app{cfg: cfg})
		var out bytes.Buffer
		cmd.SetArgs(args)
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		err := cmd.Execute()
		return out.String(), err
	}

	out, err := run("--dry-run")
	want := "openrouter: openrouter.ai: 1 OpenRouter-priced models in the registry (prices are input/cached/output per million tokens)\n" +
		"openrouter: Price updates (1):\n" +
		"openrouter:   openrouter/vendor--gpt: 2.5/-/10 -> 3/-/15\n" +
		"openrouter: Unchanged prices: 0\n" +
		"ollama: no ollama provider in the registry; nothing to mirror\n"
	if err != nil || out != want {
		t.Errorf("wt cloud-sync --dry-run: err = %v, output:\n%s\nwant:\n%s", err, out, want)
	}
	if out, err := run("--only", "openrouter", "--dry-run"); err != nil || !strings.HasPrefix(out, "openrouter: ") || strings.Contains(out, "ollama: ") {
		t.Errorf("--only openrouter: err = %v, output:\n%s\nwant the openrouter lines alone", err, out)
	}
	if out, err := run("--only", "ollama", "--dry-run"); err != nil || out != "ollama: no ollama provider in the registry; nothing to mirror\n" {
		t.Errorf("--only ollama: err = %v, output %q, want the ollama flow's one line", err, out)
	}
	for _, old := range []string{"prices", "catalog", "prices,catalog"} {
		first, _, _ := strings.Cut(old, ",")
		if _, err := run("--only", old, "--dry-run"); err == nil || err.Error() != `--only: unknown flow "`+first+`" (valid: openrouter, ollama)` || exitCodeOf(err) != 1 {
			t.Errorf("--only %s: err = %v (exit %d), want it refused as an unknown flow", old, err, exitCodeOf(err))
		}
	}
	help, err := run("--help")
	if err != nil || !strings.Contains(help, "\n  openrouter  OpenRouter's per-token prices") || !strings.Contains(help, "\n  ollama      https://ollama.com/pricing, mirrored") ||
		!strings.Contains(help, "a comma list of openrouter, ollama") || strings.Contains(help, "catalog flow") || strings.Contains(help, "prices flow") {
		t.Errorf("--help does not name the flows openrouter and ollama (err %v):\n%s", err, help)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run (from `wt/`): `go test -count=1 ./cmd/wt -run TestCloudSyncFlowsAreNamedForTheirProviders 2>&1 | grep -v '^wt: migrated'`
Expected: FAIL, because the flows still have their old names:

```text
--- FAIL: TestCloudSyncFlowsAreNamedForTheirProviders (0.00s)
    cloudsync_flownames_test.go:45: wt cloud-sync --dry-run: err = <nil>, output:
        prices: openrouter.ai: 1 OpenRouter-priced models in the registry (prices are input/cached/output per million tokens)
        prices: Price updates (1):
        prices:   openrouter/vendor--gpt: 2.5/-/10 -> 3/-/15
        prices: Unchanged prices: 0
        catalog: no ollama provider in the registry; nothing to mirror
        …
    cloudsync_flownames_test.go:48: --only openrouter: err = --only: unknown flow "openrouter" (valid: prices, catalog), output:
        …
    cloudsync_flownames_test.go:51: --only ollama: err = --only: unknown flow "ollama" (valid: prices, catalog), output "", want the ollama flow's one line
    cloudsync_flownames_test.go:56: --only prices: err = <nil> (exit 1), want it refused as an unknown flow
    cloudsync_flownames_test.go:56: --only catalog: err = <nil> (exit 1), want it refused as an unknown flow
    cloudsync_flownames_test.go:56: --only prices,catalog: err = <nil> (exit 1), want it refused as an unknown flow
    cloudsync_flownames_test.go:62: --help does not name the flows openrouter and ollama (err <nil>):
        …
FAIL
```

- [ ] **Step 3: Rename, with a script**

The names are in some 450 places, most of them the expected output of existing tests. A script makes the same substitution everywhere and is the one way to review it: read the script, then the diff. Save this as `/tmp/rename-flows.sh` (outside the repo: it is not committed):

```bash
#!/bin/bash
# Rename wt cloud-sync's flows: prices -> openrouter, catalog -> ollama.
# Run from the repo root. It edits tracked files in place and prints nothing.
set -euo pipefail

# 1. The command's own Go files: flow names in strings, and the identifiers
#    that name a flow.
cmd=(wt/cmd/wt/cloudsync.go wt/cmd/wt/cloudsync_catalog.go wt/cmd/wt/cloudsync_ollama.go
     wt/cmd/wt/cloudsync_test.go wt/cmd/wt/cloudsync_catalog_test.go wt/cmd/wt/cloudsync_ollama_test.go)
perl -pi -e '
  # the parsed page keeps its name: catalogRun.catalog -> ollamaRun.page
  s/\bcatalogRun\{catalog: catalog,/ollamaRun{page: catalog,/g;
  s/^\tcatalog     cloudsync\.Catalog$/\tpage        cloudsync.Catalog/;
  s/\bc\.catalog\b/c.page/g;
  s/\brun\.catalog\b/run.page/g;
  # whole phrases first, before their words are renamed one by one
  s/the catalog\x27s changes were not written/the ollama flow\x27s changes were not written/g;
  s/= prices %v, catalog %v/= openrouter %v, ollama %v/;
  # line prefixes: at the start of a string or of a line of one, and after \n
  s/(?<![\w.\/-])(?<!Unchanged )(?<!OpenRouter\x27s )prices:(?=[ \\`"]|$)/openrouter:/g;
  s/(?<![\w.\/-])catalog:(?=[ \\`"]|$)/ollama:/g;
  s/\\nprices:(?=[ \\`"])/\\nopenrouter:/g;
  s/\\ncatalog:(?=[ \\`"])/\\nollama:/g;
  # a string that is nothing but flow names: "prices", " catalog , prices "
  s{"((?:[ ,]*(?:prices|catalog))+[ ,]*)"}{ my $x = $1; $x =~ s/prices/openrouter/g; $x =~ s/catalog/ollama/g; "\"$x\"" }ge;
  s/\(valid: prices, catalog\)/(valid: openrouter, ollama)/g;
  # identifiers
  s/\bplanPricesFlow\b/planOpenRouterFlow/g;
  s/\bplanCatalogFlow\b/planOllamaFlow/g;
  s/\bpricesRun\b/openRouterRun/g;
  s/\bcatalogRun\b/ollamaRun/g;
  s/\bcatalogGate\b/ollamaGate/g;
  s/\bcatalogTouchedIDs\b/ollamaTouchedIDs/g;
  s/\bcatalogCodeMeaning\b/ollamaCodeMeaning/g;
  s/\bcatalogCode\b/ollamaCode/g;
  s/\bcatalogWhy\b/ollamaWhy/g;
  s/\bpricesStale\b/openrouterStale/g;
  s/\bcatalogStale\b/ollamaStale/g;
  s/\bfreshPrices\b/freshOpenRouter/g;
  s/\bfreshCatalog\b/freshOllama/g;
  s/\b(o|opts|applied|alone|tc)\.prices\b/$1.openrouter/g;
  s/\b(o|opts|applied|alone|tc)\.catalog\b/$1.ollama/g;
  s/\bprices, catalog bool\b/openrouter, ollama bool/;
  s/\bprices, catalog (\*openRouterRun|\*ollamaRun)/openrouter, ollama $1/g;
' "${cmd[@]}"

# 1b. cloudsync.go alone: the applied struct's fields, the two runs' local
#     names from runCloudSync to the end of the file, and the help text.
perl -pi -e '
  s/^\tprices  cloudsync\.PricesApplied$/\topenrouter cloudsync.PricesApplied/;
  s/^\tcatalog cloudsync\.CatalogApplied$/\tollama     cloudsync.CatalogApplied/;
  if ($in ||= /^func runCloudSync\(/) {
    unless (m{^\s*//}) { s/\bprices\b/openrouter/g; s/\bcatalog\b/ollama/g; }
  }
  s/"  prices   OpenRouter\x27s per-token prices, for the models priced by OpenRouter\\n"/"  openrouter  OpenRouter\x27s per-token prices, for the models priced by OpenRouter\\n"/;
  s/"  catalog  https:\/\/ollama\.com\/pricing, mirrored: prices \(off-peak included\),\\n"/"  ollama      https:\/\/ollama.com\/pricing, mirrored: prices (off-peak included),\\n"/;
  s/"           new cloud models added and pulled, models the page no longer\\n"/"              new cloud models added and pulled, models the page no longer\\n"/;
  s/"           lists removed from the registry and from ollama\\n\\n"/"              lists removed from the registry and from ollama\\n\\n"/;
  s/"catalog, with --yes: the removal digest/"ollama, with --yes: the removal digest/;
  s/the registry is written once, the catalog\x27s pulls and/the registry is written once, the ollama flow\x27s pulls and/;
  s/run the catalog\x27s$/run the ollama flow\x27s/;
' wt/cmd/wt/cloudsync.go

# 2. Every file that talks about the flows: the phrases.
all=("${cmd[@]}"
     wt/cmd/wt/main.go wt/cmd/wt/exitcode.go
     wt/internal/cloudsync/*.go wt/internal/config/config.go wt/internal/config/registry_doc_test.go
     wt/internal/agents/price_notice.go wt/internal/agents/price_notice_test.go
     wt/docs/wt-cloud-sync.md wt/docs/internals/launch-flow.md wt/.claude/skills/cloud-sync/SKILL.md wt/CLAUDE.md CLAUDE.md
     docs/guides/00-config-map.md docs/guides/02-providers-and-models.md wt/CHANGELOG.md)
perl -pi -e '
  s/--only prices,catalog\b/--only openrouter,ollama/g;
  s/--only prices\b/--only openrouter/g;
  s/\(valid: prices, catalog\)/(valid: openrouter, ollama)/g;
  s/the catalog changed nothing/the ollama flow changed nothing/g;
  s/--only catalog\b/--only ollama/g;
  s/\bprices flow\b/openrouter flow/g;
  s/\bcatalog flow\b/ollama flow/g;
  s/\bPrices flow\b/Openrouter flow/g;
  s/\bCatalog flow\b/Ollama flow/g;
  s/\bprices plan\b/openrouter plan/g;
  s/\bcatalog plan\b/ollama plan/g;
  s/\bcatalog flag\b/ollama-flow flag/g;
  s/\bthe catalog\x27s changes were not written\b/the ollama flow\x27s changes were not written/g;
  s/Nothing was changed for the catalog\./Nothing was changed by the ollama flow./g;
  s/\bCatalog changed nothing\b/ollama changed nothing/g;
  s/\bcatalog changed nothing\b/ollama changed nothing/g;
  s/ollama: ollama at /ollama: daemon at /g;
  s/the catalog\x27s own flags/the ollama flow\x27s own flags/g;
  s/the catalog\x27s flags/the ollama flow\x27s flags/g;
  s/\b([Aa]) (`?)(ollama|openrouter)(:`| flow| plan|-flow)/$1n $2$3$4/g;
  s/catalog: ollama at /ollama: daemon at /g;
' "${all[@]}"

# 3. The docs and the skill quote the command's lines: the prefixes there.
perl -pi -e '
  s/(?<![\w.\/-])(?<!Unchanged )(?<!OpenRouter\x27s )prices:(?=[ `]|$)/openrouter:/g;
  s/(?<![\w.\/-])(?<!short )catalog:(?=[ `]|$)/ollama:/g;
' wt/docs/wt-cloud-sync.md wt/.claude/skills/cloud-sync/SKILL.md wt/CLAUDE.md

# 3b. What is left in the reference page, the skill, wt/CLAUDE.md and the
#     changelog: headings, table cells, sentences that name a flow by its
#     old word, and the identifiers wt/CLAUDE.md quotes. -0777 reads a file
#     whole, so a phrase that wraps onto the next line is still found.
perl -0777 -pi -e '
  s/^### prices$/### openrouter/m;
  s/^### catalog$/### ollama/m;
  s/^(wt cloud-sync --only openrouter) +# one flow$/$1 . (" " x 22) . "# one flow"/me;
  s/\bThe prices rule\b/The openrouter rule/g;
  s/\bThe catalog rule\b/The ollama rule/g;
  s/the row: for prices, on any/the row: for openrouter, on any/g;
  s/fetched; for the catalog, on a row/fetched; for ollama, on a row/g;
  s/\*\*Applies the catalog\x27s gates\*\*/**Applies the ollama flow\x27s gates**/g;
  s/refused the catalog\)/refused the ollama flow)/g;
  s/the catalog\x27s 4 or 5/the ollama flow\x27s 4 or 5/g;
  s/\*\*Runs the catalog\x27s `ollama pull`s/**Runs the ollama flow\x27s `ollama pull`s/g;
  s/a comma list of `prices`, `catalog`/a comma list of `openrouter`, `ollama`/g;
  s/\| catalog, with `--yes`:/| ollama, with `--yes`:/g;
  s/model \(prices\), or/model (openrouter), or/g;
  s/the catalog\x27s(\s+)changes(\s+)were(\s+)not(\s+)written/the ollama flow\x27s$1changes$2were$3not$4written/g;
  s/Nothing was changed for(\s+)the catalog\./Nothing was changed by$1the ollama flow./g;
  s/\(`prices`, `catalog`\), the installed/(`openrouter`, `ollama`), the installed/g;
  s/for the user\. Prices: each/for the user. openrouter: each/g;
  s/under `Price updates`\. Catalog: `Price updates`/under `Price updates`. ollama: `Price updates`/g;
  s/exit 0\. Prices: no OpenRouter-priced model/exit 0. openrouter: no OpenRouter-priced model/g;
  s/row\)\. Catalog: no `ollama` provider row/row). ollama: no `ollama` provider row/g;
  s/row of `cost\.time_prices` is the catalog\x27s;/row of `cost.time_prices` is the ollama flow\x27s;/g;
  s/`planPricesFlow`/`planOpenRouterFlow`/g;
  s/`planCatalogFlow`/`planOllamaFlow`/g;
  s/`catalogGate`/`ollamaGate`/g;
  s/`cloudSyncOutcome\.catalogWhy`/`cloudSyncOutcome.ollamaWhy`/g;
  s/gains its second flow, `catalog`,/gains its second flow, `ollama`,/g;
' wt/docs/wt-cloud-sync.md wt/.claude/skills/cloud-sync/SKILL.md wt/CLAUDE.md wt/CHANGELOG.md

# 3c. The command's test files: the tests, subtests and fixtures that are
#     named for a flow, and the sentences in them that call the ollama flow
#     "the catalog". ("no catalog to mirror" and "has no catalog" stay: they
#     mean ollama's catalog, not the flow.) Then the package comment and one
#     test comment in internal/cloudsync, and the test names wt/CLAUDE.md
#     quotes.
perl -pi -e '
  s/\bTestCloudSyncCatalogOllamaFailures/TestCloudSyncOllamaCLIFailures/g;
  s/\bTestCloudSyncSkipsTheCatalog/TestCloudSyncSkipsOllama/g;
  s/\bTestCloudSyncCatalog/TestCloudSyncOllama/g;
  s/\bTestCloudSyncPrices/TestCloudSyncOpenRouter/g;
' wt/cmd/wt/cloudsync_test.go wt/cmd/wt/cloudsync_catalog_test.go wt/cmd/wt/cloudsync_ollama_test.go wt/CLAUDE.md
perl -pi -e '
  s/\bcatalog(Pages?|Pulled|PlanText)\b/ollama$1/g;
  s/\bthe catalog\x27s/the ollama flow\x27s/g;
  s/\bthe catalog (is|alone|applied|untouched|written)\b/the ollama flow $1/g;
  s/\bevery way the catalog$/every way the ollama flow/;
  s/\brun whose catalog is refused\b/run whose ollama flow is refused/;
  s/\ba catalog code\b/an ollama-flow code/;
  s/\bcatalog-only\b/ollama-only/g;
  s/\bAn unstubbed catalog test\b/An unstubbed ollama-flow test/;
  s/\bprices first, then catalog\b/openrouter first, then ollama/;
  s/\bprices applied, catalog not\b/openrouter applied, ollama not/;
  s/\bcatalog applied, prices not\b/ollama applied, openrouter not/;
  s/\bno catalog change\b/no ollama-flow change/;
  s/\bthe prices fetch fails\b/the openrouter fetch fails/;
' wt/cmd/wt/cloudsync_test.go wt/cmd/wt/cloudsync_catalog_test.go wt/cmd/wt/cloudsync_ollama_test.go
perl -pi -e '
  s{^//   - Prices: OpenRouter\x27s model list}{//   - The openrouter flow: OpenRouter\x27s model list};
  s{^//   - Catalog: ollama\.com/pricing}{//   - The ollama flow: ollama.com/pricing};
  s{^// the catalog\x27s, a key wt does not model}{// the ollama flow\x27s, a key wt does not model};
' wt/internal/cloudsync/doc.go wt/internal/cloudsync/apply_test.go

# 4. The renames changed the width of aligned names.
(cd wt && gofmt -w cmd/wt/cloudsync.go cmd/wt/cloudsync_catalog.go cmd/wt/cloudsync_ollama.go cmd/wt/cloudsync_test.go cmd/wt/cloudsync_catalog_test.go cmd/wt/cloudsync_ollama_test.go)
```

Run it from the repo root: `bash /tmp/rename-flows.sh`

It prints nothing. What each part does: part 1 renames, in the command's six Go files, the line prefixes (`prices:` → `openrouter:`, `catalog:` → `ollama:`, but not the plan line `Unchanged prices:` and not `OpenRouter's prices:`), the flow names where a string is nothing but flow names, and the identifiers listed under "Produces"; part 1b renames the two runs' local variables in `runCloudSync` and `writeCloudSync` and rewrites the two help lines; part 2 rewrites the phrases ("the prices flow", `--only catalog`, …) in every file that has them; parts 3 and 3b do the prefixes, the headings, the table cells and the quoted identifiers in the reference page, the skill, `wt/CLAUDE.md` and the changelog; part 3c renames the tests, subtests and test fixtures that were named for a flow (and the two test names `wt/CLAUDE.md` quotes), rewrites the sentences in the command's tests that called the ollama flow "the catalog", and retitles the two bullets of the package comment in `wt/internal/cloudsync/doc.go`; part 4 runs `gofmt`, because the new names changed the width of aligned declarations.

If `main` has moved and the script leaves something behind (a build error, a failing test, or a line Step 5's grep prints), apply the same substitution by hand to that line.

- [ ] **Step 4: The changelog entry**

In `wt/CHANGELOG.md`, under `## Unreleased` / `### Changed`. (The script has already corrected the spellings in the `### Added` entries that introduce the command, which is unreleased.)

Find:

```markdown
### Changed

- **Breaking:** wt no longer reads `~/.config/local-ai/modelman.toml`.
```

Replace with:

```markdown
### Changed

- **Breaking:** `wt cloud-sync`'s two flows are named for the provider each
  one syncs: `prices` is now `openrouter` and `catalog` is now `ollama`.
  The names changed everywhere they appear: `--only openrouter`, `--only
  ollama`, the prefix of every output line (`openrouter:`, `ollama:`), the
  last error line (`wt: cloud-sync: the ollama flow changed nothing: …`) and
  `--help`. There are no aliases: `--only prices` and `--only catalog` are
  unknown flows (exit 1, `--only: unknown flow "prices" (valid: openrouter,
  ollama)`), so a script that names one stops instead of running something
  else. One line was reworded with the rename, because `ollama: ollama at
  <address>` read badly: it is `ollama: daemon at <address>`. What each
  flow does has not changed. Reference: `docs/wt-cloud-sync.md`.
- **Breaking:** wt no longer reads `~/.config/local-ai/modelman.toml`.
```

- [ ] **Step 5: Run the tests, and check that nothing was left behind**

Run (from `wt/`): `gofmt -l cmd internal && go vet ./cmd/wt ./internal/cloudsync ./internal/config ./internal/agents && go test -count=1 ./cmd/wt -run 'TestCloudSyncFlowsAreNamedForTheirProviders|TestSelectFlows|TestCloudSyncCommandRefusals' -v 2>&1 | grep -E '^(---|ok|FAIL)'`
Expected (`gofmt -l` prints nothing):

```text
--- PASS: TestCloudSyncFlowsAreNamedForTheirProviders (0.00s)
--- PASS: TestSelectFlows (0.00s)
--- PASS: TestCloudSyncCommandRefusals (0.00s)
ok  	github.com/ohanaverse/local-ai-setup/wt/cmd/wt	(time)
```

Run (from the repo root):

```bash
grep -rnE "\b(prices|catalog) flow|--only (prices|catalog)\b|planPricesFlow|planCatalogFlow|catalogGate|catalogWhy|catalogCode|\bcatalogRun|\bpricesRun|\ba (ollama|openrouter) (flow|plan)|TestCloudSync(Prices|Catalog|SkipsTheCatalog)|\bcatalog(Pages?|Pulled|PlanText)\b|^//   - (Prices|Catalog):" --include='*.go' --include='*.md' . \
  | grep -v "docs/superpowers\|docs/archive\|cloudsync_flownames_test.go"
```

Expected: exactly one line, the changelog sentence of Step 4 that names the old flows on purpose:

```text
./wt/CHANGELOG.md:178:  `--help`. There are no aliases: `--only prices` and `--only catalog` are
```

(The line number may differ.)

Run (from the repo root): `grep -n "catalog" wt/cmd/wt/cloudsync_test.go wt/cmd/wt/cloudsync_catalog_test.go wt/cmd/wt/cloudsync_ollama_test.go | grep -v "ollama cloud catalog"`
Expected: two lines, both about ollama's catalog and not about the flow:

```text
wt/cmd/wt/cloudsync_catalog_test.go:501:// notice tells its user to run. There is no catalog to mirror, so the
wt/cmd/wt/cloudsync_catalog_test.go:529:// ollama provider row has no catalog, whoever asks: the flow prints the one
```

Then read the diff of the three docs once, as prose: `git diff -- wt/docs/wt-cloud-sync.md wt/.claude/skills/cloud-sync/SKILL.md wt/CLAUDE.md`. The script works line by line; a sentence that reads oddly after it (an "a" before "ollama", a "catalog" that now means the flow and was left) is corrected by hand. What stays "catalog" on purpose: "the ollama cloud catalog", "a catalog that shrank", `CatalogPlan`, `cloudsync_catalog.go`.

- [ ] **Step 6: Verify the slice**

Run (from `wt/`): `test -z "$(gofmt -l .)" && go vet ./... && go test -count=1 ./... && make check`
Expected: every package `ok` (25 at `63f63d5`), and `make check` ends without an error.

Run (from the repo root): `make check-links`
Expected: `ALL LINKS OK`.

Run (from the repo root): `make test-all`
Expected: it ends without an error. (Not run while writing this plan.)

- [ ] **Step 7: Commit**

From the repo root:

```bash
git add -A wt/cmd/wt wt/internal wt/docs wt/.claude/skills/cloud-sync/SKILL.md wt/CLAUDE.md wt/CHANGELOG.md CLAUDE.md docs/guides/00-config-map.md docs/guides/02-providers-and-models.md
git status --short   # only the files the script and Steps 1 and 4 touched
git commit -m "refactor(wt)!: cloud-sync's flows are named for their providers: openrouter and ollama (#322)"
```

- [ ] **Step 8 (the controller, with the owner's OK): the PR**

```bash
git push -u origin refactor/322-cloud-sync-flow-names
gh pr create --repo ohanaverse/local-ai-setup --base main --title "refactor(wt)!: cloud-sync's flows are named for their providers: openrouter and ollama (#322)" --body-file - <<'EOF'
Refs #322. First of four PRs (plan: docs/superpowers/plans/2026-10-09-cloud-sync-time-of-day-prices.md).

`wt cloud-sync`'s two flows are renamed for the provider each one syncs: `prices` is `openrouter`, `catalog` is `ollama`. The names change in `--only`, in the prefix of every output line, in `--help`, in every message, and in the docs and the skill. There are no aliases: `--only prices` and `--only catalog` are unknown flows (exit 1), like any other unknown name.

No behaviour changes. One line is reworded because the rename made it read badly: `ollama: daemon at <address>` (was `catalog: ollama at <address>`).

The rename was made by the script in the plan's Task 1, not by hand. It reaches the tests too: the test functions, subtests and fixtures that were named for a flow carry the new names. `internal/cloudsync`'s exported names and all file names are unchanged.
EOF
```

Add the session's PR attribution line at the end of the body if your session is told to add one.

---

### Task 2: Remove `openrouter_priced`; a model is OpenRouter-priced when its provider is `openrouter` (slice B)

**Precondition:** slice A is merged. Branch: `git switch -c refactor/322-drop-openrouter-priced main`.

**Files:**
- Modify: `docs/contracts/catalog-predicates.sample.toml`, `docs/contracts/catalog-predicates.expected.json`
- Modify (tests): `wt/cmd/wt/cloudsync_test.go`, `wt/internal/cloudsync/openrouter_test.go`, `wt/internal/cloudsync/apply_test.go`, `wt/internal/agents/price_notice_test.go`
- Modify: `wt/internal/config/config.go`, `wt/internal/config/registry_doc.go`, `wt/internal/cloudsync/entry.go`, `wt/internal/cloudsync/openrouter.go`, `wt/cmd/wt/cloudsync.go`, `wt/internal/agents/price_notice.go`
- Modify (docs): `wt/docs/wt-cloud-sync.md`, `wt/.claude/skills/cloud-sync/SKILL.md`, `docs/guides/02-providers-and-models.md`, `wt/CLAUDE.md`, `wt/docs/internals/config-and-registry.md`, `wt/docs/internals/launch-flow.md`, `wt/CHANGELOG.md`

**Interfaces:**
- Consumes from Task 1: `cloudSyncOpts{openrouter: true, …}`; the prefix `openrouter: `; the line `openrouter: no OpenRouter-priced model in the registry; nothing to refresh` (unchanged by this task).
- Consumes (existing): in `cmd/wt` tests `cloudSyncHome`, `stubCloudFetch`, `stubRouteSync(t, warning string) *int`, `runCS(t, cfg, o) (stdout, stderr string, code int)`, `mustRead(t, path) string`, `cloudSyncStamp`; in `internal/cloudsync` tests `orEntry`, `apiOf`, `f(float64) *float64`, `wantIDs`, `cm(name string) CatalogModel`, `withOffpeak`, `catalogOf`, `isOllamaCloud(Entry) bool`, `PlanCatalog(entries, catalog, pulled, resolved)`.
- Produces:
  - `func (c *Config) OpenRouterPriced(m Model) bool` — now `m.ProviderID == "openrouter"`; `config.Provider` no longer has the field `OpenRouterPriced`
  - `const cloudsync.OpenRouterProvider = "openrouter"`; `func cloudsync.OpenRouterPriced(e Entry) bool` (one argument)
  - `func cloudsync.PlanPrices(entries []Entry, api map[string]APIPrice) *PricePlan` (two arguments; Tasks 7 and 8 call it this way)
  - `type cloudsync.Provider struct{ ID string }`
  - the line `openrouter: no model could be refreshed, so nothing is stamped and wt's stale-pricing notice is not cleared; the warnings above say why for each model`
  - `TestTheTwoFlowsNeverShareAModel`, which Task 8's comments cite
  - the test variable `priceProviders` is deleted

- [ ] **Step 1: Change the contract first**

`docs/contracts/catalog-predicates.expected.json` is the one list both readers of the rule are held to.

Find:

```json
  "openrouter_priced": ["openrouter/vendor-x", "acme/model-a", "local-gateway/vendor-y"],
```

Replace with:

```json
  "openrouter_priced": ["openrouter/vendor-x"],
```

The fixture keeps its two provider rows that carry the key, as a registry written before this change does; only what the comments say about them changes. Four edits to `docs/contracts/catalog-predicates.sample.toml`:

Find:

```toml
# A non-native cloud provider explicitly marked openrouter_priced = false:
# a corporate LiteLLM gateway whose model names are not OpenRouter ids.
```

Replace with:

```toml
# A provider row that still carries openrouter_priced, a key wt read until
# #322 and no longer does: the row loads and the key says nothing. (It was
# false here: a corporate LiteLLM gateway whose model names are not
# OpenRouter ids.)
```

Find:

```toml
# The override in the other direction: a local provider marked
# openrouter_priced = true (a local proxy serving OpenRouter ids). Both readers
# (the typed config and the cloud-sync rows) must honor true as well as false,
# or they disagree about this model.
```

Replace with:

```toml
# The same key the other way round, true on a local provider (a local proxy
# serving OpenRouter ids). It is ignored too: only a model whose provider_id
# is "openrouter" is OpenRouter-priced, in both readers (the typed config and
# the cloud-sync rows).
```

Find:

```toml
# Inherits "cloud" from the acme provider.
```

Replace with:

```toml
# Inherits "cloud" from the acme provider. A cloud provider that is not
# "openrouter": not OpenRouter-priced.
```

Find:

```toml
# corp-litellm has openrouter_priced = false: not OpenRouter-priced even
# though it is a non-native cloud provider.
[[models]]
id = "corp-litellm/claude-3"
family = "fixture"
provider_id = "corp-litellm"
model_name = "claude-3"

# local-gateway has openrouter_priced = true: OpenRouter-priced even though
# its provider is local. In the catalog as any local model is; not a cloud
# route.
```

Replace with:

```toml
# Not OpenRouter-priced: its provider is not "openrouter". (The provider
# row's openrouter_priced = false is not what decides it.)
[[models]]
id = "corp-litellm/claude-3"
family = "fixture"
provider_id = "corp-litellm"
model_name = "claude-3"

# Not OpenRouter-priced either, whatever its provider row's
# openrouter_priced = true once meant. In the catalog as any local model is;
# not a cloud route.
```

- [ ] **Step 2: Rewrite the tests that pinned the old rule**

(a) `wt/cmd/wt/cloudsync_test.go`: the test of the rule through the command is replaced whole. The new one also pins what a registry that still carries the key does.

Find:

```go
// TestCloudSyncJudgesOpenRouterPricingByTheModelsNotTheProviderRow pins what
// "OpenRouter-priced" means to the skip: the rule config.Config.OpenRouterPriced
// applies to each model, not whether a provider row is called openrouter. A
// registry with no openrouter row whose provider says openrouter_priced = true
// is refreshed, and one whose only cloud provider says openrouter_priced =
// false (a corporate gateway, #302) is skipped without a request. Judged by
// the row's name, the first would never be refreshed and the second would be
// fetched for and warned about on every run.
func TestCloudSyncJudgesOpenRouterPricingByTheModelsNotTheProviderRow(t *testing.T) {
	registry := func(priced string) string {
		return `[[providers]]
id = "relay"
name = "Relay"
location = "cloud"
openrouter_priced = ` + priced + `

[providers.auth]
type = "api_key"
secret_ref = "WT_TEST_RELAY_KEY"

[[models]]
id = "relay/vendor--gpt"
family = "gpt"
provider_id = "relay"
model_name = "vendor/gpt"
location = "cloud"
source = "curated"
tags = []
`
	}

	t.Run("openrouter_priced = true with no openrouter row: refreshed", func(t *testing.T) {
		text := registry("true")
		path, cfg := cloudSyncHome(t, text)
		if len(cfg.Models) != 1 || !cfg.OpenRouterPriced(cfg.Models[0]) {
			t.Fatalf("config.OpenRouterPriced is false for the fixture's model (%d models); the fixture no longer says what this test needs", len(cfg.Models))
		}
		got := stubCloudFetch(t, map[string]string{cloudsync.OpenRouterModelsURL: openRouterBody})
		synced := stubRouteSync(t, "")
		stdout, stderr, code := runCS(t, cfg, cloudSyncOpts{openrouter: true, yes: true})
		if code != 0 || stderr != "" || !strings.HasSuffix(stdout, "openrouter:   relay/vendor--gpt: no cost -> 3/-/15\nopenrouter: Unchanged prices: 0\nopenrouter: refreshed 1 model(s); 1 price(s) changed\n") {
			t.Errorf("stdout = %q\nstderr = %q, exit %d", stdout, stderr, code)
		}
		if urls := got.all(); !reflect.DeepEqual(urls, []string{cloudsync.OpenRouterModelsURL}) || *synced != 1 {
			t.Errorf("fetched %v, synced %d time(s); want one request and one sync", urls, *synced)
		}
		if !strings.Contains(mustRead(t, path), "pricing_updated_at = \""+cloudSyncStamp+"\"") {
			t.Error("the refreshed model was not stamped")
		}
	})

	t.Run("openrouter_priced = false on the only cloud provider: skipped", func(t *testing.T) {
		text := registry("false")
		path, cfg := cloudSyncHome(t, text)
		if len(cfg.Models) != 1 || cfg.OpenRouterPriced(cfg.Models[0]) {
			t.Fatalf("config.OpenRouterPriced is true for the fixture's model (%d models); the fixture no longer says what this test needs", len(cfg.Models))
		}
		got := stubCloudFetch(t, map[string]string{cloudsync.OpenRouterModelsURL: openRouterBody})
		synced := stubRouteSync(t, "")
		stdout, stderr, code := runCS(t, cfg, cloudSyncOpts{openrouter: true, yes: true})
		if stdout != "openrouter: no OpenRouter-priced model in the registry; nothing to refresh\n" || stderr != "" || code != 0 {
			t.Errorf("stdout = %q, stderr = %q, exit %d", stdout, stderr, code)
		}
		if len(got.all()) != 0 || *synced != 0 || mustRead(t, path) != text {
			t.Errorf("fetched %v, synced %d time(s) or changed the registry; want none of them", got.all(), *synced)
		}
	})
}
```

Replace with:

```go
// TestCloudSyncRefreshesOnlyTheOpenRouterProvidersModels pins who the
// openrouter flow refreshes: the models whose provider_id is "openrouter",
// and no other. The provider key openrouter_priced, which until #322 could
// put another provider's models in or take them out, is no longer read. A
// registry that still carries it must go on working: it loads, the key
// decides nothing either way (a cloud gateway marked true is not fetched
// for, and the openrouter provider's own models are refreshed although their
// row says false), and a write leaves the key in the file, because wt never
// deletes a key it does not model.
func TestCloudSyncRefreshesOnlyTheOpenRouterProvidersModels(t *testing.T) {
	const relay = `[[providers]]
id = "relay"
name = "Relay"
location = "cloud"
openrouter_priced = true

[providers.auth]
type = "api_key"
secret_ref = "WT_TEST_RELAY_KEY"

[[models]]
id = "relay/vendor--gpt"
family = "gpt"
provider_id = "relay"
model_name = "vendor/gpt"
location = "cloud"
source = "curated"
tags = []
`

	t.Run("another cloud provider's model, marked true: not refreshed", func(t *testing.T) {
		path, cfg := cloudSyncHome(t, relay)
		if len(cfg.Models) != 1 || cfg.OpenRouterPriced(cfg.Models[0]) {
			t.Fatalf("config.OpenRouterPriced is true for a model of provider relay (%d models)", len(cfg.Models))
		}
		got := stubCloudFetch(t, map[string]string{cloudsync.OpenRouterModelsURL: openRouterBody})
		synced := stubRouteSync(t, "")
		stdout, stderr, code := runCS(t, cfg, cloudSyncOpts{openrouter: true, yes: true})
		if stdout != "openrouter: no OpenRouter-priced model in the registry; nothing to refresh\n" || stderr != "" || code != 0 {
			t.Errorf("stdout = %q, stderr = %q, exit %d", stdout, stderr, code)
		}
		if len(got.all()) != 0 || *synced != 0 || mustRead(t, path) != relay {
			t.Errorf("fetched %v, synced %d time(s) or changed the registry; want none of them", got.all(), *synced)
		}
	})

	t.Run("the openrouter provider's model, marked false: refreshed, and the key kept", func(t *testing.T) {
		text := strings.Replace(cloudSyncRegistry, "id = \"openrouter\"\nname = \"OpenRouter\"\nlocation = \"cloud\"\n",
			"id = \"openrouter\"\nname = \"OpenRouter\"\nlocation = \"cloud\"\nopenrouter_priced = false\n", 1) + "\n" + relay
		if strings.Count(text, "openrouter_priced = ") != 2 {
			t.Fatal("the fixture does not carry the key on both provider rows")
		}
		path, cfg := cloudSyncHome(t, text)
		got := stubCloudFetch(t, map[string]string{cloudsync.OpenRouterModelsURL: openRouterBody})
		synced := stubRouteSync(t, "")
		stdout, stderr, code := runCS(t, cfg, cloudSyncOpts{openrouter: true, yes: true})
		const want = "openrouter: openrouter.ai: 1 OpenRouter-priced models in the registry (prices are input/cached/output per million tokens)\n" +
			"openrouter: Price updates (1):\n" +
			"openrouter:   openrouter/vendor--gpt: 2.5/-/10 -> 3/-/15\n" +
			"openrouter: Unchanged prices: 0\n" +
			"openrouter: refreshed 1 model(s); 1 price(s) changed\n"
		if stdout != want || stderr != "" || code != 0 {
			t.Errorf("stdout = %q\nstderr = %q, exit %d\nwant stdout %q", stdout, stderr, code, want)
		}
		if urls := got.all(); !reflect.DeepEqual(urls, []string{cloudsync.OpenRouterModelsURL}) || *synced != 1 {
			t.Errorf("fetched %v, synced %d time(s); want one request and one sync", urls, *synced)
		}
		after := mustRead(t, path)
		for _, kept := range []string{
			"id = \"openrouter\"\nname = \"OpenRouter\"\nlocation = \"cloud\"\nopenrouter_priced = false\n",
			"id = \"relay\"\nname = \"Relay\"\nlocation = \"cloud\"\nopenrouter_priced = true\n",
		} {
			if !strings.Contains(after, kept) {
				t.Errorf("the write did not keep a provider row as it was:\n%s\nfile:\n%s", kept, after)
			}
		}
		if strings.Contains(after, "id = \"relay/vendor--gpt\"\nfamily = \"gpt\"\nprovider_id = \"relay\"\nmodel_name = \"vendor/gpt\"\nlocation = \"cloud\"\nsource = \"curated\"\ntags = []\npricing_updated_at") {
			t.Error("the relay model was stamped: it is not the openrouter flow's")
		}
		if _, err := config.Load(); err != nil {
			t.Errorf("config.Load after the write: %v", err)
		}
	})
}
```

(b) The same file: the line a run prints when nothing could be refreshed.

Find:

```go
// cloud-sync` after every launch; without this line nothing says that
// running it again will not help, or what will (openrouter_priced = false).
// A run that matched a model does not print it.
func TestCloudSyncSaysWhenNoModelCouldBeRefreshed(t *testing.T) {
	const line = "openrouter: no model could be refreshed, so nothing is stamped and wt's stale-pricing notice is not cleared; " +
		"set openrouter_priced = false on a provider whose model names are not OpenRouter ids\n"
```

Replace with:

```go
// cloud-sync` after every launch; without this line nothing says that
// running it again will not help, or where to look (the warnings, each of
// which names a model and what is wrong with it). A run that matched a model
// does not print it.
func TestCloudSyncSaysWhenNoModelCouldBeRefreshed(t *testing.T) {
	const line = "openrouter: no model could be refreshed, so nothing is stamped and wt's stale-pricing notice is not cleared; " +
		"the warnings above say why for each model\n"
```

(c) `wt/internal/agents/price_notice_test.go`, four edits: the stale-price notice follows the same rule.

Find:

```go
// after every session. "OpenRouter-priced" is the registry's rule, not the
// presence of an `openrouter` provider row: a model that another provider
// marks openrouter_priced is reminded about, and an openrouter row with no
// model is not.
func TestPrintPriceNoticeSpeaksOnlyAboutOpenRouterPrices(t *testing.T) {
	yes := true
	fresh
```

Replace with:

```go
// after every session. "OpenRouter-priced" is a model whose provider_id is
// "openrouter": an openrouter provider row with no model under it is not
// reminded about, and neither is another cloud provider's model.
func TestPrintPriceNoticeSpeaksOnlyAboutOpenRouterPrices(t *testing.T) {
	fresh
```

Find:

```go
		{
			"no openrouter provider row, but a provider marked openrouter_priced",
			[]config.Provider{{ID: "gateway", Location: config.LocationLocal, OpenRouterPriced: &yes}},
			[]config.Model{{ID: "gateway/x", ProviderID: "gateway"}},
			never,
		},
```

Replace with:

```go
		{
			"another cloud provider's model, never stamped",
			[]config.Provider{{ID: "gateway", Location: config.LocationCloud}},
			[]config.Model{{ID: "gateway/x", ProviderID: "gateway"}},
			"",
		},
		{
			"an openrouter model with no openrouter provider row",
			nil,
			[]config.Model{{ID: "openrouter/x", ProviderID: "openrouter"}},
			never,
		},
```

Find:

```go
// TestHasOpenRouterPricedModel pins issue #151's rule, which mirrors
// modelman's _is_openrouter_priced: the stale-pricing notice is only worth
// printing when some model's price comes from OpenRouter — an openrouter
// model or a model of a non-native cloud provider. Ollama cloud models
// (location "cloud" on the local ollama provider, priced by ollama.com) and
// native agent models never count, or users with no OpenRouter models are
// nagged after every session about a refresh that has nothing to do. A
// provider's explicit openrouter_priced overrides the inference in both
// directions (a corporate gateway opts out, a local proxy opts in) but never
// makes a native provider count — modelman applies the same order, and a
// one-sided override would have wt nag about, or stay silent on, a refresh
// modelman sees differently.
func TestHasOpenRouterPricedModel(t *testing.T) {
	no, yes := false, true
	providers := []config.Provider{
		{ID: "ollama", Location: config.LocationLocal},
		{ID: "openrouter", Location: config.LocationCloud},
		{ID: "claude", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "native"}},
		{ID: "acme", Location: config.LocationCloud},
		{ID: "corp", Location: config.LocationCloud, OpenRouterPriced: &no},
		{ID: "gateway", Location: config.LocationLocal, OpenRouterPriced: &yes},
		{ID: "agent", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "native"}, OpenRouterPriced: &yes},
	}
```

Replace with:

```go
// TestHasOpenRouterPricedModel pins the rule the stale-pricing notice and
// `wt cloud-sync`'s openrouter flow share (#151, simplified in #322): the
// notice is only worth printing when some model's price comes from
// OpenRouter, which is a model whose provider_id is "openrouter". Ollama
// cloud models (priced by ollama.com), native agent models and the models of
// any other cloud provider never count, or users with no OpenRouter models
// are nagged after every session about a refresh that has nothing to do.
func TestHasOpenRouterPricedModel(t *testing.T) {
	providers := []config.Provider{
		{ID: "ollama", Location: config.LocationLocal},
		{ID: "openrouter", Location: config.LocationCloud},
		{ID: "claude", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "native"}},
		{ID: "acme", Location: config.LocationCloud},
	}
```

Find:

```go
		{"non-native cloud provider", &config.Config{}, []config.Model{{ID: "acme/x", ProviderID: "acme"}}, true},
		{"cloud provider marked openrouter_priced = false", &config.Config{}, []config.Model{{ID: "corp/x", ProviderID: "corp"}}, false},
		{"local provider marked openrouter_priced = true", &config.Config{}, []config.Model{{ID: "gateway/x", ProviderID: "gateway"}}, true},
		{"native provider marked openrouter_priced = true", &config.Config{}, []config.Model{{ID: "agent/x", ProviderID: "agent"}}, false},
```

Replace with:

```go
		{"another cloud provider, not native", &config.Config{}, []config.Model{{ID: "acme/x", ProviderID: "acme"}}, false},
		{"an openrouter model whose provider row is missing", &config.Config{}, []config.Model{{ID: "openrouter/x", ProviderID: "openrouter"}}, true},
```

- [ ] **Step 3: Run them to verify they fail**

Run (from `wt/`): `go test -count=1 ./cmd/wt ./internal/config ./internal/cloudsync ./internal/agents -run 'TestCloudSyncRefreshesOnlyTheOpenRouterProvidersModels|TestCloudSyncSaysWhenNoModelCouldBeRefreshed|TestCatalogPredicatesFixture|TestOpenRouterPricedMatchesTheContract|TestPrintPriceNoticeSpeaksOnlyAboutOpenRouterPrices|TestHasOpenRouterPricedModel' 2>&1 | grep -v '^wt: migrated'`
Expected: all six FAIL, each on the old rule still being in force:

```text
--- FAIL: TestCloudSyncRefreshesOnlyTheOpenRouterProvidersModels (0.00s)
    --- FAIL: TestCloudSyncRefreshesOnlyTheOpenRouterProvidersModels/another_cloud_provider's_model,_marked_true:_not_refreshed (0.00s)
        cloudsync_test.go:766: config.OpenRouterPriced is true for a model of provider relay (1 models)
    --- FAIL: TestCloudSyncRefreshesOnlyTheOpenRouterProvidersModels/the_openrouter_provider's_model,_marked_false:_refreshed,_and_the_key_kept (0.00s)
        cloudsync_test.go:795: stdout = "openrouter: openrouter.ai: 1 OpenRouter-priced models in the registry (prices are input/cached/output per million tokens)\nopenrouter: Price updates (1):\nopenrouter:   relay/vendor--gpt: no cost -> 3/-/15\n…
        cloudsync_test.go:810: the relay model was stamped: it is not the openrouter flow's
--- FAIL: TestCloudSyncSaysWhenNoModelCouldBeRefreshed (0.00s)
    cloudsync_test.go:839: OpenRouter does not list the model (…): exit 0, stderr "", stdout:
        …
        openrouter: no model could be refreshed, so nothing is stamped and wt's stale-pricing notice is not cleared; set openrouter_priced = false on a provider whose model names are not OpenRouter ids
        …
--- FAIL: TestCatalogPredicatesFixture (0.00s)
    catalog_predicates_fixture_test.go:58: openrouter_priced = [openrouter/vendor-x acme/model-a local-gateway/vendor-y], want [openrouter/vendor-x]
--- FAIL: TestOpenRouterPricedMatchesTheContract (0.00s)
    openrouter_test.go:147: openrouter_priced = [openrouter/vendor-x acme/model-a local-gateway/vendor-y], want [openrouter/vendor-x]
--- FAIL: TestPrintPriceNoticeSpeaksOnlyAboutOpenRouterPrices (0.00s)
    price_notice_test.go:174: another cloud provider's model, never stamped: PrintPriceNotice printed "wt: token pricing has never been refreshed — run 'wt cloud-sync'\n", want ""
--- FAIL: TestHasOpenRouterPricedModel (0.00s)
    price_notice_test.go:235: another cloud provider, not native: HasOpenRouterPricedModel = true, want false
```

- [ ] **Step 4: Write the implementation**

(a) `wt/internal/config/config.go`: the field goes.

Find:

```go
	ModelDir string `toml:"model_dir,omitempty"`
	// OpenRouterPriced, when set, overrides the inferred value for the
	// OpenRouterPriced model predicate. nil = infer from location/auth (the
	// default). false = this provider's models are not OpenRouter-priced even
	// though they appear on a non-native cloud provider (e.g. a corporate
	// LiteLLM gateway). true = they are, even though the provider is not a
	// cloud one. A native provider is never OpenRouter-priced, whatever this
	// says.
	OpenRouterPriced *bool `toml:"openrouter_priced,omitempty"`
}
```

Replace with:

```go
	ModelDir string `toml:"model_dir,omitempty"`
}
```

(b) The same file: the rule.

Find:

```go
// OpenRouterPriced reports whether m's price comes from OpenRouter — what
// `wt cloud-sync`'s openrouter flow refreshes: an openrouter model, or a model of a
// non-native cloud provider. Keyed on the provider's location, not the
// model's, so ollama cloud models don't count. Pinned by
// docs/contracts/catalog-predicates.sample.toml.
//
// A provider's explicit openrouter_priced in the registry overrides the
// inferred result in both directions, after the native check: false is for a
// non-native cloud provider that routes through a corporate LiteLLM gateway
// rather than OpenRouter, true puts a provider's models in. Both values are
// honored, as cloudsync.OpenRouterPriced honors them: honoring only false
// here would have the stale-price notice and the refresh disagree about a
// provider marked true.
func (c *Config) OpenRouterPriced(m Model) bool {
	if m.Native {
		return false
	}
	p := c.ProviderByID(m.ProviderID)
	if p != nil && p.Auth.Type == "native" {
		return false
	}
	if p != nil && p.OpenRouterPriced != nil {
		return *p.OpenRouterPriced
	}
	if m.ProviderID == "openrouter" {
		return true
	}
	return p != nil && p.Location == LocationCloud
}
```

Replace with:

```go
// OpenRouterPriced reports whether m's price comes from OpenRouter, which is
// what `wt cloud-sync`'s openrouter flow refreshes and what the stale-pricing
// notice watches: a model whose provider_id is "openrouter", and no other.
// cloudsync.OpenRouterPriced is the same rule over registry rows; both are
// pinned by docs/contracts/catalog-predicates.sample.toml.
//
// Until #322 a model of any non-native cloud provider counted too, and a
// provider's openrouter_priced key overrode the result. The key dates from
// when ollama's cloud models had no published prices; it is no longer read.
// A registry that still has it loads (the decoder ignores a key Provider
// does not model) and a write keeps it.
func (c *Config) OpenRouterPriced(m Model) bool {
	return m.ProviderID == "openrouter"
}
```

(c) `wt/internal/config/registry_doc.go`: the key leaves the list that orders the keys wt adds to a provider row. The list orders keys; it never decided what a row may hold, so a row that has the key keeps it where it is.

Find:

```go
		"":     {"id", "name", "location", "model_dir", "protocols", "openrouter_priced", "auth"},
```

Replace with:

```go
		"":     {"id", "name", "location", "model_dir", "protocols", "auth"},
```

(d) `wt/internal/cloudsync/entry.go`, two edits: what the planners read of a provider row. (The import of `tomlw` stays: `Entry.Cost` uses it.)

Find:

```go
// Provider is what the planners read of one provider row.
type Provider struct {
	ID, Location, AuthType string
	// OpenRouterPriced is the row's openrouter_priced override: nil when the
	// key is absent (or is not a boolean, which wt's loader refuses anyway).
	OpenRouterPriced *bool
}
```

Replace with:

```go
// Provider is what the command reads of one provider row: its id, which is
// how it knows the registry has an ollama row at all.
type Provider struct {
	ID string
}
```

Find:

```go
		p := Provider{ID: str(row, "id"), Location: str(row, "location")}
		if v, ok := row.Get("openrouter_priced"); ok {
			if b, isBool := v.(bool); isBool {
				p.OpenRouterPriced = &b
			}
		}
		if v, ok := row.Get("auth"); ok {
			if auth, isTable := v.(*tomlw.Table); isTable {
				p.AuthType = str(auth, "type")
			}
		}
		out = append(out, p)
```

Replace with:

```go
		out = append(out, Provider{ID: str(row, "id")})
```

(e) `wt/internal/cloudsync/openrouter.go`, two edits: the rule over registry rows, and `PlanPrices` without the provider list.

Find:

```go
// OpenRouterPriced reports whether e takes its price from OpenRouter: an
// openrouter model, or a model of a non-native cloud provider. It is
// config.Config.OpenRouterPriced over registry rows, keyed on the provider's
// location and not the model's, so an ollama cloud model (priced by
// ollama.com) does not count. A provider's openrouter_priced key overrides
// the inference in both directions, after the native check (#302): false
// takes a gateway whose model names are not OpenRouter ids out of the
// refresh, true puts a provider in. Both are pinned by
// docs/contracts/catalog-predicates.sample.toml.
func OpenRouterPriced(e Entry, providers []Provider) bool {
	var p *Provider
	for i := range providers {
		if providers[i].ID == e.ProviderID {
			p = &providers[i]
			break
		}
	}
	if p != nil && p.AuthType == "native" {
		return false
	}
	if p != nil && p.OpenRouterPriced != nil {
		return *p.OpenRouterPriced
	}
	if e.ProviderID == "openrouter" {
		return true
	}
	return p != nil && p.Location == "cloud"
}
```

Replace with:

```go
// OpenRouterProvider is the provider_id of the models the openrouter flow
// refreshes.
const OpenRouterProvider = "openrouter"

// OpenRouterPriced reports whether e takes its price from OpenRouter: a
// model whose provider_id is "openrouter", and no other. It is
// config.Config.OpenRouterPriced over registry rows, and both are pinned by
// docs/contracts/catalog-predicates.sample.toml. A provider row's
// openrouter_priced key, which overrode this until #322, is not read.
//
// The ollama flow addresses only rows whose provider_id is "ollama"
// (isOllamaCloud, findEntry), so no row is ever in both flows' scope:
// TestTheTwoFlowsNeverShareAModel.
func OpenRouterPriced(e Entry) bool { return e.ProviderID == OpenRouterProvider }
```

Find:

```go
func PlanPrices(entries []Entry, providers []Provider, api map[string]APIPrice) *PricePlan {
	plan := &PricePlan{}
	for _, e := range entries {
		if !OpenRouterPriced(e, providers) {
```

Replace with:

```go
func PlanPrices(entries []Entry, api map[string]APIPrice) *PricePlan {
	plan := &PricePlan{}
	for _, e := range entries {
		if !OpenRouterPriced(e) {
```

(f) `wt/cmd/wt/cloudsync.go`, six edits: the two callers of the predicate, the two callers of `PlanPrices`, the advice line and one line of `--help`.

Find:

```go
	entries, providers := cloudsync.Entries(doc.Models()), cloudsync.Providers(doc.Providers())
	if !slices.ContainsFunc(entries, func(e cloudsync.Entry) bool { return cloudsync.OpenRouterPriced(e, providers) }) {
```

Replace with:

```go
	entries := cloudsync.Entries(doc.Models())
	if !slices.ContainsFunc(entries, cloudsync.OpenRouterPriced) {
```

Find:

```go
		if !cloudsync.OpenRouterPriced(e, providers) {
			continue
		}
		if _, err := doc.Model(e.ID); err != nil {
```

Replace with:

```go
		if !cloudsync.OpenRouterPriced(e) {
			continue
		}
		if _, err := doc.Model(e.ID); err != nil {
```

Find:

```go
	run := &openRouterRun{api: api, plan: cloudsync.PlanPrices(entries, providers, api)}
```

Replace with:

```go
	run := &openRouterRun{api: api, plan: cloudsync.PlanPrices(entries, api)}
```

Find:

```go
			freshOpenRouter = cloudsync.PlanPrices(entries, providers, openrouter.api)
```

Replace with:

```go
			freshOpenRouter = cloudsync.PlanPrices(entries, openrouter.api)
```

Find:

```go
		// names this command) is not cleared by running it again.
		fmt.Fprintln(out, "openrouter: no model could be refreshed, so nothing is stamped and wt's stale-pricing notice is not cleared; "+
			"set openrouter_priced = false on a provider whose model names are not OpenRouter ids")
```

Replace with:

```go
		// names this command) is not cleared by running it again. Each
		// candidate has a warning in the plan above that says why.
		fmt.Fprintln(out, "openrouter: no model could be refreshed, so nothing is stamped and wt's stale-pricing notice is not cleared; "+
			"the warnings above say why for each model")
```

Find:

```go
			"  openrouter  OpenRouter's per-token prices, for the models priced by OpenRouter\n" +
```

Replace with:

```go
			"  openrouter  OpenRouter's per-token prices, for the models of the openrouter\n" +
			"              provider\n" +
```

(g) `wt/internal/agents/price_notice.go`: the comment.

Find:

```go
// from OpenRouter — what `wt cloud-sync`'s openrouter flow refreshes. It
// delegates to config.OpenRouterPriced: an openrouter model, or a model of a
// non-native cloud provider. Keyed on the provider's location, not the
// model's, so ollama cloud models (location "cloud" on the local ollama
// provider, priced by ollama.com) don't count. A model whose ProviderID has
// no registry provider doesn't count either (p == nil, location
// unresolvable). A nil cfg has no models.
```

Replace with:

```go
// from OpenRouter — what `wt cloud-sync`'s openrouter flow refreshes. It
// delegates to config.OpenRouterPriced: a model whose provider_id is
// "openrouter". Ollama cloud models (priced by ollama.com), native agent
// models and any other cloud provider's models don't count. A nil cfg has
// no models.
```

(h) `wt/internal/cloudsync/openrouter_test.go`, four edits that follow the new signatures and the new rule: the provider table goes, the contract test calls the predicate with one argument, and the candidates test expects one match.

Find:

```go
var (
	priced, notPriced = true, false

	priceProviders = []Provider{
		{ID: "openrouter", Location: "cloud", AuthType: "api_key"},
		{ID: "ollama", Location: "local", AuthType: "none"},
		{ID: "claude", Location: "cloud", AuthType: "native"},
		{ID: "acme", Location: "cloud", AuthType: "none"},
		// The two overrides (#302): a corporate gateway whose model names
		// are not OpenRouter ids opts out, a local proxy serving OpenRouter
		// ids opts in.
		{ID: "corp", Location: "cloud", AuthType: "api_key", OpenRouterPriced: &notPriced},
		{ID: "gateway", Location: "local", AuthType: "none", OpenRouterPriced: &priced},
	}
)

func orEntry(
```

Replace with:

```go
func orEntry(
```

Find:

```go
	providers := Providers(rows("providers"))
	var got []string
	for _, e := range Entries(rows("models")) {
		if OpenRouterPriced(e, providers) {
			got = append(got, e.ID)
		}
	}
```

Replace with:

```go
	var got []string
	for _, e := range Entries(rows("models")) {
		if OpenRouterPriced(e) {
			got = append(got, e.ID)
		}
	}
```

Find:

```go
// TestPlanPricesCandidatesAndWarnings pins who is refreshed and what is said
// about the rest: a model of a non-native cloud provider is a candidate like
// an openrouter one, and so is one whose provider says openrouter_priced =
// true; an ollama cloud model (priced by ollama.com), a native agent
// provider's model and a model of a provider that says openrouter_priced =
// false are not, and get no "no match" warning (#151, #302); a candidate
// OpenRouter does not list, lists with no price, prices at -1 or prices
// with text that is not a number is a warning and is left exactly as it is
// (a price nobody could read must not delete the one in the registry).
```

Replace with:

```go
// TestPlanPricesCandidatesAndWarnings pins who is refreshed and what is said
// about the rest. A candidate is a model whose provider_id is "openrouter",
// and nothing else: not another cloud provider's model whose name happens to
// be an OpenRouter id, not an ollama cloud model (priced by ollama.com), not
// a native agent provider's model. None of those is fetched for, changed or
// warned about. A candidate OpenRouter does not list, lists with no price,
// prices at -1 or prices with text that is not a number is a warning and is
// left exactly as it is (a price nobody could read must not delete the one
// in the registry).
```

Find:

```go
	wantIDs(t, "matched", matched, "openrouter/listed", "acme/model-a", "gateway/y")
	if plan.Candidates != 7 {
		t.Errorf("candidates = %d, want 7", plan.Candidates)
	}
```

Replace with:

```go
	wantIDs(t, "matched", matched, "openrouter/listed")
	if plan.Candidates != 5 {
		t.Errorf("candidates = %d, want 5", plan.Candidates)
	}
```

(i) Every other call of `PlanPrices` in the package's tests passes the deleted `priceProviders`, or `Providers(d.Providers())`. From the repo root:

```bash
perl -pi -e 's/PlanPrices\((.*?), priceProviders, /PlanPrices($1, /g; s/PlanPrices\(Entries\(d\.Models\(\)\), Providers\(d\.Providers\(\)\), api\)/PlanPrices(Entries(d.Models()), api)/g' \
  wt/internal/cloudsync/openrouter_test.go wt/internal/cloudsync/apply_test.go
```

(j) Append to `wt/internal/cloudsync/openrouter_test.go` (its imports are already there). This is the test the whole plan leans on for decision 8:

```go
// TestTheTwoFlowsNeverShareAModel pins what lets each flow write
// cost.time_prices rows without looking at the other's: no registry row is
// in both flows' scope. The openrouter flow takes the rows whose provider_id
// is "openrouter"; the ollama flow addresses only rows whose provider_id is
// "ollama". If a row could be in both, one run would have two plans made
// from the same reading of the registry, and the flow applied second would
// write back the rows it read, dropping the first flow's.
func TestTheTwoFlowsNeverShareAModel(t *testing.T) {
	var entries []Entry
	for _, provider := range []string{"openrouter", "ollama", "acme", ""} {
		for _, location := range []string{"cloud", "local", ""} {
			// A cloud tag, an OpenRouter id, and one name that is both a page
			// name's tag and listed by OpenRouter below.
			for _, name := range []string{"a:cloud", "vendor/a", "a"} {
				e := Entry{ID: provider + "/" + location + "/" + name, Family: "x", ProviderID: provider, ModelName: name, Location: location, CatalogName: "a", Cost: &Cost{Input: f(9)}}
				if isOllamaCloud(e) && OpenRouterPriced(e) {
					t.Errorf("%s is in both flows' scope", e.ID)
				}
				entries = append(entries, e)
			}
		}
	}
	api := apiOf(t, `{"data": [
		{"id": "a:cloud", "pricing": {"prompt": "0.000001", "completion": "0.000002"}},
		{"id": "vendor/a", "pricing": {"prompt": "0.000001", "completion": "0.000002"}},
		{"id": "a", "pricing": {"prompt": "0.000001", "completion": "0.000002"}}
	]}`)
	prices := PlanPrices(entries, api)
	catalog := PlanCatalog(entries, catalogOf(withOffpeak(cm("a"), f(0.5), nil, f(1))), []string{"a:cloud"}, nil)
	touched := map[string]string{}
	for _, m := range prices.Matched {
		touched[m.ModelID] = "openrouter"
	}
	if len(touched) != 9 {
		t.Fatalf("the openrouter plan matched %d rows, want the 9 openrouter ones:\n%s", len(touched), prices.Format())
	}
	var catalogIDs []string
	for _, u := range catalog.Updates {
		catalogIDs = append(catalogIDs, u.ModelID)
	}
	catalogIDs = append(catalogIDs, catalog.Removals...)
	for _, a := range catalog.Additions {
		catalogIDs = append(catalogIDs, a.ID)
		if a.CloneOf != "" {
			catalogIDs = append(catalogIDs, a.CloneOf)
		}
	}
	if len(catalogIDs) == 0 {
		t.Fatalf("the ollama plan addresses no row; the fixture proves nothing:\n%s", catalog.Format())
	}
	for _, id := range catalogIDs {
		if touched[id] != "" {
			t.Errorf("%s is changed by both plans:\n%s\n\n%s", id, prices.Format(), catalog.Format())
		}
		if !strings.HasPrefix(id, "ollama/") {
			t.Errorf("the ollama plan addresses %s, a row of another provider", id)
		}
	}
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run (from `wt/`): `gofmt -l cmd internal && go vet ./... && go test -count=1 ./cmd/wt ./internal/config ./internal/cloudsync ./internal/agents -run 'TestCloudSyncRefreshesOnlyTheOpenRouterProvidersModels|TestCloudSyncSaysWhenNoModelCouldBeRefreshed|TestCatalogPredicatesFixture|TestOpenRouterPricedMatchesTheContract|TestPrintPriceNoticeSpeaksOnlyAboutOpenRouterPrices|TestHasOpenRouterPricedModel|TestTheTwoFlowsNeverShareAModel|TestPlanPricesCandidatesAndWarnings' -v 2>&1 | grep -E '^(---|ok|FAIL)'`
Expected (`gofmt -l` prints nothing):

```text
--- PASS: TestCloudSyncRefreshesOnlyTheOpenRouterProvidersModels (0.00s)
--- PASS: TestCloudSyncSaysWhenNoModelCouldBeRefreshed (0.00s)
ok  	github.com/ohanaverse/local-ai-setup/wt/cmd/wt	(time)
--- PASS: TestCatalogPredicatesFixture (0.00s)
ok  	github.com/ohanaverse/local-ai-setup/wt/internal/config	(time)
--- PASS: TestOpenRouterPricedMatchesTheContract (0.00s)
--- PASS: TestPlanPricesCandidatesAndWarnings (0.00s)
--- PASS: TestTheTwoFlowsNeverShareAModel (0.00s)
ok  	github.com/ohanaverse/local-ai-setup/wt/internal/cloudsync	(time)
--- PASS: TestPrintPriceNoticeSpeaksOnlyAboutOpenRouterPrices (0.00s)
--- PASS: TestHasOpenRouterPricedModel (0.00s)
ok  	github.com/ohanaverse/local-ai-setup/wt/internal/agents	(time)
```

The second subtest of `TestCloudSyncRefreshesOnlyTheOpenRouterProvidersModels` is the one that answers "what does a write do with the key": after a real apply both provider rows still hold their `openrouter_priced` line, and the file loads.

- [ ] **Step 6: The docs**

(a) `wt/docs/wt-cloud-sync.md`, four edits.

Find:

```markdown
Fetches `https://openrouter.ai/api/v1/models` and re-prices the registry's
OpenRouter-priced models: an `openrouter` model, or a model of any other
cloud provider that is not an agent's native provider, unless the provider's
`openrouter_priced` key says otherwise
([guide 02](../../docs/guides/02-providers-and-models.md)). A model is
matched on its `model_name`.
```

Replace with:

```markdown
Fetches `https://openrouter.ai/api/v1/models` and re-prices the registry's
OpenRouter-priced models: the models whose `provider_id` is `openrouter`,
and no other ([guide 02](../../docs/guides/02-providers-and-models.md)). A
model is matched on its `model_name`. A model of any other provider is never
fetched for, changed or warned about, whatever its name is.
```

Find:

```markdown
  run says so: `openrouter: no model could be refreshed, so nothing is stamped
  and wt's stale-pricing notice is not cleared; set openrouter_priced =
  false on a provider whose model names are not OpenRouter ids`.
```

Replace with:

```markdown
  run says so: `openrouter: no model could be refreshed, so nothing is stamped
  and wt's stale-pricing notice is not cleared; the warnings above say why
  for each model`.
- **The provider key `openrouter_priced` is no longer read.** It used to put
  another provider's models into this flow (`true`) or take a provider's
  models out (`false`). A registry that still has it loads as before, the
  key decides nothing, and wt leaves it in the file when it writes; delete
  it by hand when you like.
- **A model under any other cloud provider is no longer refreshed**, whether
  or not that key was ever set. Until #322 a model of a cloud provider that
  is not an agent's native one (a gateway that serves OpenRouter ids, say)
  was refreshed from OpenRouter and counted for the stale-pricing notice.
  It now keeps the price it has, and the notice does not watch it. To have
  it refreshed, register it under the `openrouter` provider
  (`provider_id = "openrouter"`); it is then also reached through
  OpenRouter.
```

Find:

```markdown
- The openrouter rule is judged by the models, not by whether an `openrouter`
  provider row exists: a model another cloud provider prices through
  OpenRouter still counts.
```

Replace with:

```markdown
- The openrouter rule is judged by the models, not by whether an `openrouter`
  provider row exists: a row with no model under it is skipped, and a model
  whose `provider_id` is `openrouter` counts even when the row is missing.
```

Find:

```markdown
Running `wt cloud-sync` clears it as long as the openrouter flow matches at
least one model. To stop it for a provider whose models OpenRouter does not
price, set `openrouter_priced = false` on that provider row.
```

Replace with:

```markdown
Running `wt cloud-sync` clears it as long as the openrouter flow matches at
least one model. When it matches none, every model of the `openrouter`
provider has a `warning:` that says why (a `model_name` OpenRouter does not
list, most often); correct the name, or remove the model
(`wt model rm <id>`), and the notice goes with the last of them.
```

(b) `wt/.claude/skills/cloud-sync/SKILL.md`, two edits.

Find:

```markdown
- **`openrouter:`** re-prices the registry's OpenRouter-priced models from
  OpenRouter's public model list and stamps each one it matched. The stamps
```

Replace with:

```markdown
- **`openrouter:`** re-prices the registry's OpenRouter-priced models (the
  ones whose `provider_id` is `openrouter`, and no other) from
  OpenRouter's public model list and stamps each one it matched. The stamps
```

Find:

```markdown
     stamped and wt's stale-pricing notice is not cleared; set
     openrouter_priced = false on a provider whose model names are not
     OpenRouter ids`, tell the user: running the sync will not clear the
     notice, and that provider key is the fix. It is their decision.
```

Replace with:

```markdown
     stamped and wt's stale-pricing notice is not cleared; the warnings
     above say why for each model`, tell the user: running the sync again
     will not clear the notice. Read them the warnings: each names a model
     of the `openrouter` provider and what is wrong with it (most often a
     `model_name` OpenRouter does not list). Correcting the name or
     removing the model is their decision.
```

(c) `docs/guides/02-providers-and-models.md`: the paragraph about the key (one long line).

Find:

```markdown
A provider may also set `openrouter_priced` (`true` or `false`, unquoted). Left out, wt infers it: an `openrouter` model, or a model of a non-native cloud provider, takes its price from OpenRouter — `wt cloud-sync` refreshes it, and after a launch wt reminds you when the newest refresh is more than a week old. Set `openrouter_priced = false` on a cloud provider whose model names are not OpenRouter ids (a corporate LiteLLM gateway, say) to take its models out of the refresh and stop the reminder; `true` opts a provider in. This is a hand edit that wt keeps when it rewrites the file.
```

Replace with:

```markdown
The models of the `openrouter` provider, and no others, take their price from OpenRouter: `wt cloud-sync` refreshes it, and after a launch wt reminds you when the newest refresh is more than a week old. An older registry may carry a provider key `openrouter_priced` (`true` or `false`), which used to change that rule; wt no longer reads it. Such a registry still loads, the key decides nothing, and wt keeps it when it rewrites the file, so you can delete it by hand at any time.
```

(d) `wt/CHANGELOG.md`, two edits: a new entry under `## Unreleased` / `### Changed`, above Task 1's, and one sentence of an older unreleased entry that named the key.

Find:

```markdown
### Changed

- **Breaking:** `wt cloud-sync`'s two flows are named for the provider each
```

Replace with:

```markdown
### Changed

- **Breaking:** the provider key `openrouter_priced` is no longer read, and
  the rule it overrode is simpler: a model takes its price from OpenRouter
  when its `provider_id` is `openrouter`, and never otherwise. That one rule
  decides what `wt cloud-sync`'s openrouter flow refreshes and what the
  stale-pricing notice watches. Until now a model of any cloud provider that
  is not an agent's native one counted too, and the key could put a
  provider's models in (`true`) or take them out (`false`); it dates from
  when ollama's cloud models had no published prices. A registry that still
  has the key loads as before and wt keeps the key when it writes the file;
  it just decides nothing. So a model under a cloud provider other than
  `openrouter` (a gateway that serves OpenRouter ids, say) is no longer
  refreshed, and no longer watched by the notice, whether the key was `true`
  or was never set; register the model under the `openrouter` provider to
  have it refreshed. The line a run prints when no model could be refreshed
  no longer names the key: it ends `the warnings above say why for each
  model`. Reference: `docs/wt-cloud-sync.md`.
- **Breaking:** `wt cloud-sync`'s two flows are named for the provider each
```

Find:

```markdown
  matches none, `wt cloud-sync` says that the notice stays and how to stop it
  (`openrouter_priced = false`).
```

Replace with:

```markdown
  matches none, `wt cloud-sync` says that the notice stays.
```

(e) `wt/CLAUDE.md`, inside the bullet that begins "**A flow whose provider the registry does not use is skipped**" (one long line; the find text is part of it).

Find:

```markdown
openrouter: no OpenRouter-priced model (`cloudsync.OpenRouterPriced`, the same rule as `config.Config.OpenRouterPriced` — the models, not an `openrouter` row).
```

Replace with:

```markdown
openrouter: no OpenRouter-priced model (`cloudsync.OpenRouterPriced`, the same rule as `config.Config.OpenRouterPriced`: a model whose `provider_id` is `openrouter`, and no other — the models, not an `openrouter` row; the provider key `openrouter_priced` was removed in #322 and is ignored where a registry still has it).
```

(f) `wt/docs/internals/config-and-registry.md`, inside the paragraph that lists the decoded extra fields.

Find:

```markdown
`auth.base_url`, `auth.secret_ref`, and a provider's `openrouter_priced` (`Provider.OpenRouterPriced`, a `*bool`: unset infers, `true`/`false` override `Config.OpenRouterPriced` for that provider's models — never for a native provider), a model's
```

Replace with:

```markdown
`auth.base_url`, `auth.secret_ref`, a model's
```

(g) `wt/docs/internals/launch-flow.md`, inside the "Stale-pricing notice" bullet.

Find:

```markdown
(`agents.HasOpenRouterPricedModel`, #151: an openrouter model, or one whose provider is a non-native cloud provider, unless the provider's `openrouter_priced` says otherwise; ollama cloud models don't count)
```

Replace with:

```markdown
(`agents.HasOpenRouterPricedModel`, #151: a model whose `provider_id` is `openrouter`, and no other; ollama cloud models and other cloud providers' models don't count)
```

- [ ] **Step 7: Verify the slice**

Run (from the repo root): `grep -rn "openrouter_priced" --include='*.go' --include='*.md' --include='*.toml' . | grep -v "docs/superpowers\|docs/archive" | cut -c1-110`
Expected: only places that say the key is gone or pin that it is ignored: the two contract files, `TestCloudSyncRefreshesOnlyTheOpenRouterProvidersModels`, the two comments on `OpenRouterPriced`, the JSON key `openrouter_priced` of the expected lists in the two fixture tests, guide 02, `wt-cloud-sync.md`, `wt/CLAUDE.md` and the changelog entry. No line tells a user to set it.

Run (from `wt/`): `test -z "$(gofmt -l .)" && go vet ./... && go test -count=1 ./... && make check`
Expected: every package `ok` (25), and `make check` ends without an error.

Run (from the repo root): `make check-links`
Expected: `ALL LINKS OK`.

Run (from the repo root): `make test-all`
Expected: it ends without an error. (Not run while writing this plan.)

- [ ] **Step 8: Commit**

From the repo root:

```bash
git add docs/contracts/catalog-predicates.sample.toml docs/contracts/catalog-predicates.expected.json docs/guides/02-providers-and-models.md \
  wt/cmd/wt/cloudsync.go wt/cmd/wt/cloudsync_test.go wt/internal/cloudsync/entry.go wt/internal/cloudsync/openrouter.go wt/internal/cloudsync/openrouter_test.go wt/internal/cloudsync/apply_test.go \
  wt/internal/config/config.go wt/internal/config/registry_doc.go wt/internal/agents/price_notice.go wt/internal/agents/price_notice_test.go \
  wt/docs/wt-cloud-sync.md wt/docs/internals/config-and-registry.md wt/docs/internals/launch-flow.md wt/.claude/skills/cloud-sync/SKILL.md wt/CLAUDE.md wt/CHANGELOG.md
git commit -m "refactor(wt)!: drop the openrouter_priced provider key; a model is OpenRouter-priced when its provider is openrouter (#322)"
```

- [ ] **Step 9 (the controller, with the owner's OK): the PR**

```bash
git push -u origin refactor/322-drop-openrouter-priced
gh pr create --repo ohanaverse/local-ai-setup --base main --title "refactor(wt)!: drop the openrouter_priced provider key; a model is OpenRouter-priced when its provider is openrouter (#322)" --body-file - <<'EOF'
Refs #322. Second of four PRs (plan: docs/superpowers/plans/2026-10-09-cloud-sync-time-of-day-prices.md).

The provider key `openrouter_priced` is no longer read, and the rule it overrode is one comparison: a model takes its price from OpenRouter when its `provider_id` is `openrouter`. The same rule decides what `wt cloud-sync`'s openrouter flow refreshes and what the stale-pricing notice watches (`config.Config.OpenRouterPriced`, `cloudsync.OpenRouterPriced`, held to one list by `docs/contracts/catalog-predicates.*`).

- A registry that still carries the key loads as before; the key decides nothing; a registry write leaves it in the file (tested through the command).
- A model of another cloud provider is no longer refreshed from OpenRouter or watched by the stale-pricing notice, whatever its name and whether or not the key was set (until now a non-native cloud provider's models were, by inference). The reference page and the changelog say so and say what to do.
- No model is now in both flows' scope (the ollama flow addresses only `provider_id = "ollama"` rows): `TestTheTwoFlowsNeverShareAModel`.
- The line a run prints when no model could be refreshed no longer names the key: it ends `the warnings above say why for each model`.
EOF
```

Add the session's PR attribution line at the end of the body if your session is told to add one.

---

### Task 3: The resolver: the price in force at an instant (slice C)

A pure function in `wt/internal/config`, where `ModelCost`, `TimePrice` and the validator of the rows already live. It is modelman's `price_at` (`git show e61d989:modelman/src/modelman/time_pricing.py`), ported rule for rule, test first.

What is carried over from modelman, and what is not:

| modelman's rule or test | Here |
|---|---|
| The flat prices are the default (`price_at`) | `ModelCost.Flat`, and `PriceAt` with no matching row; `TestFlatAndTimePriced` |
| Rows in order, the first whose window contains the instant wins (`test_price_at_first_matching_row_wins`) | `TestPriceAtFirstMatchingRowWins`, with one more case: a row whose window does not hold the instant is passed over |
| A row supplies each price it sets; an omitted one falls back to the flat price (`test_price_at_per_field_fallback_to_default`) | `TestPriceAtFallsBackPerField`, with one more case: a model with rows and no flat price |
| A window is `[start, end)` on its `days`, to the minute (`test_price_at_offpeak_boundaries`, five instants) | `TestPriceAtOffpeakBoundaries`: the same five, plus `24:00` as the end of a day and an instant written in another zone |
| The instant is converted to the row's `timezone` (`test_price_at_non_utc_timezone`) | `TestPriceAtReadsAWindowInItsRowsTimezone`, with one more case: the day is the zone's day |
| The instant must be timezone-aware (`test_price_at_rejects_naive_datetime`) | **Dropped.** A Go `time.Time` is always an instant; there is no naive value to reject |
| Row validation and serialization (`test_window_validation`, `test_time_price_validation`, the round-trip and shape tests) | **Not ported: reused.** modelman validated a row when it loaded it, so `price_at` never met a bad one. wt's loader only types a row's values; the rules are the registry writer's (`validateTimePrice` in `wt/internal/config/registry_validate.go`, with its own tests), so a row written by hand can be in the file and break one. The resolver asks that same function (`TimePrice.Problem`) and passes over a row it refuses: `TestPriceAtIgnoresARowItCannotRead`, `TestTimePriceProblemIsTheValidatorsRefusal` |
| (not in modelman) | `TestPriceAtFollowsDaylightSavingTime`; `TestPriceAtAnOpenRouterSchedule` (the rows Task 8 writes); the index of the row that supplied the price; a row that is not applied is named (`Model.Malformed`, `wt model list`); a value of the wrong TOML type still stops the load (`TestLoadStopsOnATimePricesValueOfTheWrongType`) |

**A row written by hand, and what happens to one that breaks a rule** (decisions 30 and 31). The rules are the validator's: an IANA `timezone` (not empty, not `Local`); prices that are finite and not negative; at least one window; in each window `days` from `mon`..`sun` and `"HH:MM"` times with `start` before `end` and `end` up to `"24:00"`. A window does not wrap, so "22:00 to 06:00" is two windows. Observed on a pty before this task was rewritten: such a row (`start = "22:00"`, `end = "06:00"`, New York) loaded, was applied at no instant, and the picker still marked the model `~`, so nothing told its writer why the price never changed. Now:

- the row is passed over **whole** (one bad window is enough: the validator refuses the row, not the window);
- a model whose every row is passed over is **not** time-priced: the picker shows its flat price with no mark;
- `Model.Malformed()` names the row and the rule, so `wt model list` prints `<id>: cost.time_prices[0]: windows[0]: start must be before end; wt reads it as absent (fix the entry in <registry>)` on stderr and carries the phrase in `--json`'s `malformed` array, the Models tab shows it for the selected row, and the model form adds its "fix the entry" hint when a save of that row is refused (the registry writer already refuses to write a row whose cost table breaks a rule; that is `main`'s behaviour and is what a sync that touches such a model gets too);
- a value of the wrong TOML type (`windows = "always"`, a price in quotes) is different: the typed decode of the registry fails and **every wt command stops** with `wt: config error: parse <registry>: toml: line N (last key "models.cost.time_prices.windows"): incompatible types: … (fix that file by hand)`. That is `main`'s behaviour for a wrong type anywhere in the registry, it names the line and the key, and this plan keeps it and pins it rather than teach the loader to drop rows in silence.

**Precondition:** none for the code of Tasks 3 to 5: it needs nothing from slices A and B (fact 45) and can be started from `main` at any time, in a worktree of its own. Branch: `git switch -c feat/322-picker-price-in-force main`. Before Task 6, slice B must be merged and this branch rebased onto it.

**Files:**
- Create: `wt/internal/config/price_at_test.go`, `wt/internal/config/price_at.go`
- Modify: `wt/internal/config/config.go` (`Model.Malformed`), `wt/cmd/wt/model_list.go` (the help text)
- Modify (tests): `wt/cmd/wt/model_list_test.go` (append)

**Interfaces:**
- Consumes (existing, `wt/internal/config`): `type ModelCost struct{ InputPricePerMillion, CachePricePerMillion, OutputPricePerMillion, SubscriptionPrice *float64; SubscriptionPeriod string; TimePrices []TimePrice }`; `type TimePrice struct{ Label, Timezone string; InputPricePerMillion, CachePricePerMillion, OutputPricePerMillion *float64; Windows []CostWindow }`; `type CostWindow struct{ Days []string; Start, End string }`; the package variables `weekDays = []string{"mon", …, "sun"}` and `priceKeys = []string{"input_price_per_million", "cache_price_per_million", "output_price_per_million"}`, and `func validateTimePrice(row *tomlw.Table) error` (`registry_validate.go`); `func (m Model) Malformed() []string` and `func Load() (*Config, error)` (`config.go`); `tomlw.NewTable()`, `(*tomlw.Table).Set(key string, v any)`. Test helper: `writeRegistry(t, dir, content string)` (`registry_test.go`). In `cmd/wt` tests: `malformedRegistry(malformed bool) string`, `runModelListOver(t, registry string, asJSON bool) (stdout, stderr, path string)`.
- Produces (Tasks 4 and 5 call the first four):
  - `type PriceInForce struct{ Input, Cache, Output *float64; Row int }` (`Row` is the index in `TimePrices` of the row that supplied the prices, `-1` for the flat ones)
  - `func (c ModelCost) PriceAt(at time.Time) PriceInForce`
  - `func (c ModelCost) Flat() PriceInForce`
  - `func (c ModelCost) TimePriced() bool` (true when at least one row is applied)
  - `func (tp TimePrice) Problem() string` (the validator's refusal of the row, `""` for a row that is applied)
  - `Model.Malformed()` also returns `cost.time_prices[<i>]: <problem>` for each row with a problem, after the fetch and draft phrases

- [ ] **Step 1: Write the failing tests**

Create `wt/internal/config/price_at_test.go`:

```go
package config

import (
	"math"
	"slices"
	"strings"
	"testing"
	"time"
)

func pf(v float64) *float64 { return &v }

// offpeakPrice is the row `wt cloud-sync`'s ollama flow writes: ollama's
// off-peak window, outside 12:00 to 18:00 UTC on weekdays and all day at
// weekends.
func offpeakPrice(in, cache, out *float64) TimePrice {
	weekdays := []string{"mon", "tue", "wed", "thu", "fri"}
	return TimePrice{
		Label: "off-peak", Timezone: "UTC",
		InputPricePerMillion: in, CachePricePerMillion: cache, OutputPricePerMillion: out,
		Windows: []CostWindow{
			{Days: weekdays, Start: "00:00", End: "12:00"},
			{Days: weekdays, Start: "18:00", End: "24:00"},
			{Days: []string{"sat", "sun"}, Start: "00:00", End: "24:00"},
		},
	}
}

func peakCost(rows ...TimePrice) ModelCost {
	return ModelCost{InputPricePerMillion: pf(1.32), CachePricePerMillion: pf(0.044), OutputPricePerMillion: pf(3.96), TimePrices: rows}
}

// wantPrice fails unless got is the three prices given, from the row given
// (-1: the flat prices).
func wantPrice(t *testing.T, what string, got PriceInForce, in, cache, out *float64, row int) {
	t.Helper()
	same := func(a, b *float64) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }
	text := func(p *float64) any {
		if p == nil {
			return "-"
		}
		return *p
	}
	if !same(got.Input, in) || !same(got.Cache, cache) || !same(got.Output, out) || got.Row != row {
		t.Errorf("%s: %v/%v/%v from row %d, want %v/%v/%v from row %d", what,
			text(got.Input), text(got.Cache), text(got.Output), got.Row, text(in), text(cache), text(out), row)
	}
}

// TestPriceAtOffpeakBoundaries pins the price in force around each edge of
// ollama's off-peak window, which is what the model picker shows for an
// ollama cloud model: a window is [start, end) to the minute, so 11:59 is
// off-peak and 12:00 is not, and 17:59:59 is still the 17:59 minute. A
// boundary read one minute off shows the wrong price for a minute a day; a
// window read as closed at its end would never let "24:00" mean midnight.
// (The cases are modelman's test_price_at_offpeak_boundaries, the reference
// this was ported from.) 2026-09-28 is a Monday.
func TestPriceAtOffpeakBoundaries(t *testing.T) {
	cost := peakCost(offpeakPrice(pf(0.66), pf(0.022), pf(1.98)))
	for _, c := range []struct {
		at      time.Time
		offpeak bool
	}{
		{time.Date(2026, 9, 28, 11, 59, 0, 0, time.UTC), true},
		{time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC), false},
		{time.Date(2026, 9, 28, 17, 59, 59, 0, time.UTC), false},
		{time.Date(2026, 9, 28, 18, 0, 0, 0, time.UTC), true},
		{time.Date(2026, 10, 3, 14, 0, 0, 0, time.UTC), true}, // Saturday
		// "24:00" ends the day: 23:59 is inside, and the next minute is
		// Tuesday 00:00, inside Tuesday's first window.
		{time.Date(2026, 9, 28, 23, 59, 59, 0, time.UTC), true},
		{time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC), true},
		// The instant is what counts, not the zone it is written in: 05:00
		// PDT on Monday is 12:00 UTC.
		{time.Date(2026, 9, 28, 5, 0, 0, 0, time.FixedZone("PDT", -7*3600)), false},
	} {
		if c.offpeak {
			wantPrice(t, c.at.Format(time.RFC3339), cost.PriceAt(c.at), pf(0.66), pf(0.022), pf(1.98), 0)
		} else {
			wantPrice(t, c.at.Format(time.RFC3339), cost.PriceAt(c.at), pf(1.32), pf(0.044), pf(3.96), -1)
		}
	}
}

// TestPriceAtReadsAWindowInItsRowsTimezone pins that a row's windows are
// wall-clock times in the row's own IANA zone, whatever zone the machine is
// in: 09:00 to 10:00 on a Monday in Honolulu (UTC-10) is 19:00 to 20:00 UTC.
// A row a user writes for a provider that bills in its local time would
// otherwise be applied ten hours off. (modelman's
// test_price_at_non_utc_timezone.)
func TestPriceAtReadsAWindowInItsRowsTimezone(t *testing.T) {
	cost := peakCost(TimePrice{Timezone: "Pacific/Honolulu", InputPricePerMillion: pf(0.5),
		Windows: []CostWindow{{Days: []string{"mon"}, Start: "09:00", End: "10:00"}}})
	wantPrice(t, "Monday 19:30 UTC", cost.PriceAt(time.Date(2026, 9, 28, 19, 30, 0, 0, time.UTC)), pf(0.5), pf(0.044), pf(3.96), 0)
	wantPrice(t, "Monday 09:30 UTC", cost.PriceAt(time.Date(2026, 9, 28, 9, 30, 0, 0, time.UTC)), pf(1.32), pf(0.044), pf(3.96), -1)
	// The day is the zone's day too: Monday 23:30 in Honolulu is Tuesday
	// 09:30 UTC, and a Tuesday window in UTC terms must not catch it.
	late := peakCost(TimePrice{Timezone: "Pacific/Honolulu", InputPricePerMillion: pf(0.5),
		Windows: []CostWindow{{Days: []string{"mon"}, Start: "23:00", End: "24:00"}}})
	wantPrice(t, "Tuesday 09:30 UTC", late.PriceAt(time.Date(2026, 9, 29, 9, 30, 0, 0, time.UTC)), pf(0.5), pf(0.044), pf(3.96), 0)
	wantPrice(t, "Monday 09:30 UTC", late.PriceAt(time.Date(2026, 9, 28, 9, 30, 0, 0, time.UTC)), pf(1.32), pf(0.044), pf(3.96), -1)
}

// TestPriceAtFollowsDaylightSavingTime pins what a window means on the two
// days a year a zone's clock jumps: it is read on the wall clock, as the row
// says it. In New York on 2026-11-01 the hour from 01:00 to 02:00 happens
// twice (EDT, then EST) and a window over it holds both times; on 2026-03-08
// the hour from 02:00 to 03:00 does not happen, and a window over it holds
// no instant that day. A fixed-offset reading would move every window of a
// user-written row by an hour for half the year.
func TestPriceAtFollowsDaylightSavingTime(t *testing.T) {
	row := func(start, end string) ModelCost {
		return peakCost(TimePrice{Timezone: "America/New_York", OutputPricePerMillion: pf(1),
			Windows: []CostWindow{{Days: []string{"sun"}, Start: start, End: end}}})
	}
	back := row("01:00", "02:00")
	for at, inside := range map[time.Time]bool{
		time.Date(2026, 11, 1, 4, 30, 0, 0, time.UTC): false, // 00:30 EDT
		time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC): true,  // 01:30 EDT
		time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC): true,  // 01:30 EST, the hour again
		time.Date(2026, 11, 1, 7, 30, 0, 0, time.UTC): false, // 02:30 EST
	} {
		if got := back.PriceAt(at).Row == 0; got != inside {
			t.Errorf("clocks back, %s: in the 01:00-02:00 window = %v, want %v", at.Format(time.RFC3339), got, inside)
		}
	}
	forward := row("02:00", "03:00")
	for _, at := range []time.Time{
		time.Date(2026, 3, 8, 6, 30, 0, 0, time.UTC), // 01:30 EST
		time.Date(2026, 3, 8, 7, 0, 0, 0, time.UTC),  // 03:00 EDT: 02:00 never came
		time.Date(2026, 3, 8, 7, 30, 0, 0, time.UTC), // 03:30 EDT
	} {
		if forward.PriceAt(at).Row != -1 {
			t.Errorf("clocks forward, %s: in the 02:00-03:00 window, which that day does not have", at.Format(time.RFC3339))
		}
	}
	// The same window a week later, when the hour exists: 02:30 EDT.
	if forward.PriceAt(time.Date(2026, 3, 15, 6, 30, 0, 0, time.UTC)).Row != 0 {
		t.Error("the Sunday after the change, 02:30 EDT is not in the 02:00-03:00 window")
	}
}

// TestPriceAtFallsBackPerField pins that a row replaces only the prices it
// sets: an off-peak row with an input price and nothing else leaves the
// cached and output prices at the flat ones. A row that blanked what it does
// not set would show a model as having no output price for part of the day.
// (modelman's test_price_at_per_field_fallback_to_default.)
func TestPriceAtFallsBackPerField(t *testing.T) {
	cost := peakCost(offpeakPrice(pf(0.66), nil, nil))
	wantPrice(t, "Saturday", cost.PriceAt(time.Date(2026, 10, 3, 1, 0, 0, 0, time.UTC)), pf(0.66), pf(0.044), pf(3.96), 0)
	// A model with rows and no flat price at all: the row's prices in its
	// window, none outside it.
	rowsOnly := ModelCost{TimePrices: []TimePrice{offpeakPrice(pf(0.66), nil, pf(1.98))}}
	wantPrice(t, "rows only, Saturday", rowsOnly.PriceAt(time.Date(2026, 10, 3, 1, 0, 0, 0, time.UTC)), pf(0.66), nil, pf(1.98), 0)
	wantPrice(t, "rows only, Monday noon", rowsOnly.PriceAt(time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)), nil, nil, nil, -1)
}

// TestPriceAtFirstMatchingRowWins pins the order rule the two writers of
// rows rely on (each replaces its row where it stands, never moves it): when
// two rows' windows hold the instant, the first in the file supplies the
// price, whole, and the second is not consulted even for a price the first
// does not set. (modelman's test_price_at_first_matching_row_wins.)
func TestPriceAtFirstMatchingRowWins(t *testing.T) {
	weekend := []CostWindow{{Days: []string{"sat", "sun"}, Start: "00:00", End: "24:00"}}
	cost := peakCost(
		TimePrice{Timezone: "UTC", Windows: weekend, InputPricePerMillion: pf(0.1)},
		TimePrice{Timezone: "UTC", Windows: weekend, InputPricePerMillion: pf(0.2), OutputPricePerMillion: pf(0.3)},
	)
	wantPrice(t, "Saturday", cost.PriceAt(time.Date(2026, 10, 3, 1, 0, 0, 0, time.UTC)), pf(0.1), pf(0.044), pf(3.96), 0)
	// A row whose window does not hold the instant is passed over for the
	// next one that does.
	cost.TimePrices[0].Windows = []CostWindow{{Days: []string{"mon"}, Start: "00:00", End: "24:00"}}
	wantPrice(t, "Saturday, first row Monday only", cost.PriceAt(time.Date(2026, 10, 3, 1, 0, 0, 0, time.UTC)), pf(0.2), pf(0.044), pf(0.3), 1)
}

// TestPriceAtAnOpenRouterSchedule pins the resolver on the rows `wt
// cloud-sync`'s openrouter flow stores for deepseek/deepseek-v4-pro-0813
// (#322): the flat price is the dearest level, in force on weekdays from
// 01:00 to 04:00 and 06:00 to 10:00 UTC, and the one row is the cheaper
// level for the rest of the week. The picker must show the cheap price for
// the 79% of the week it is in force, not the flat one.
func TestPriceAtAnOpenRouterSchedule(t *testing.T) {
	weekdays := []string{"mon", "tue", "wed", "thu", "fri"}
	cost := peakCost(TimePrice{
		Label: "openrouter", Timezone: "UTC",
		InputPricePerMillion: pf(0.66), CachePricePerMillion: pf(0.022), OutputPricePerMillion: pf(1.98),
		Windows: []CostWindow{
			{Days: weekdays, Start: "00:00", End: "01:00"},
			{Days: weekdays, Start: "04:00", End: "06:00"},
			{Days: weekdays, Start: "10:00", End: "24:00"},
			{Days: []string{"sat", "sun"}, Start: "00:00", End: "24:00"},
		},
	})
	for _, c := range []struct {
		at   time.Time
		dear bool
	}{
		{time.Date(2026, 10, 12, 0, 59, 0, 0, time.UTC), false}, // Monday
		{time.Date(2026, 10, 12, 1, 0, 0, 0, time.UTC), true},
		{time.Date(2026, 10, 12, 2, 45, 0, 0, time.UTC), true},
		{time.Date(2026, 10, 12, 4, 0, 0, 0, time.UTC), false},
		{time.Date(2026, 10, 12, 6, 0, 0, 0, time.UTC), true},
		{time.Date(2026, 10, 12, 9, 59, 0, 0, time.UTC), true},
		{time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC), false},
		{time.Date(2026, 10, 10, 2, 45, 0, 0, time.UTC), false}, // Saturday
	} {
		if c.dear {
			wantPrice(t, c.at.Format("Mon 15:04"), cost.PriceAt(c.at), pf(1.32), pf(0.044), pf(3.96), -1)
		} else {
			wantPrice(t, c.at.Format("Mon 15:04"), cost.PriceAt(c.at), pf(0.66), pf(0.022), pf(1.98), 0)
		}
	}
}

// badRows are cost.time_prices rows the registry's validator refuses, each
// with the validator's reason. The loader lets every one of them into a
// Config (it only types the values), so they are what a hand edit can leave
// in the file: the overnight window is the likeliest, because it is how one
// would write "22:00 to 06:00" before learning that a window does not wrap.
func badRows() map[string]struct {
	row  TimePrice
	want string
} {
	always := []CostWindow{{Days: []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}, Start: "00:00", End: "24:00"}}
	return map[string]struct {
		row  TimePrice
		want string
	}{
		"no timezone":          {TimePrice{Windows: always}, `timezone "" is not a known IANA timezone`},
		"the host's own zone":  {TimePrice{Timezone: "Local", Windows: always}, `timezone "Local" is not a known IANA timezone`},
		"an unknown zone":      {TimePrice{Timezone: "Mars/Olympus", Windows: always}, `timezone "Mars/Olympus" is not a known IANA timezone`},
		"no windows":           {TimePrice{Timezone: "UTC"}, "windows must be a non-empty array of tables"},
		"a window with no day": {TimePrice{Timezone: "UTC", Windows: []CostWindow{{Start: "00:00", End: "24:00"}}}, "windows[0]: days must be a non-empty list"},
		"a day that is none": {TimePrice{Timezone: "UTC", Windows: []CostWindow{{Days: []string{"monday"}, Start: "00:00", End: "24:00"}}},
			"windows[0]: days must be drawn from [mon tue wed thu fri sat sun], got monday"},
		"a start with no zero": {TimePrice{Timezone: "UTC", Windows: []CostWindow{{Days: []string{"mon"}, Start: "9:00", End: "24:00"}}}, "windows[0]: start must be HH:MM, got 9:00"},
		"an end past 24:00": {TimePrice{Timezone: "UTC", Windows: []CostWindow{{Days: []string{"mon"}, Start: "00:00", End: "24:30"}}},
			`windows[0]: end must be HH:MM between 00:00 and 24:00, got "24:30"`},
		"75 minutes": {TimePrice{Timezone: "UTC", Windows: []CostWindow{{Days: []string{"mon"}, Start: "00:75", End: "24:00"}}},
			`windows[0]: start must be HH:MM between 00:00 and 24:00, got "00:75"`},
		"an end that is text": {TimePrice{Timezone: "UTC", Windows: []CostWindow{{Days: []string{"mon"}, Start: "00:00", End: "noon"}}}, "windows[0]: end must be HH:MM, got noon"},
		"an empty window":     {TimePrice{Timezone: "UTC", Windows: []CostWindow{{Days: []string{"mon"}, Start: "12:30", End: "12:30"}}}, "windows[0]: start must be before end"},
		// 22:00 to 06:00, meant as overnight. Monday 12:30 UTC is outside it
		// however it is read; TestPriceAtIgnoresARowItCannotRead also asks at
		// an instant its writer meant it to hold.
		"an overnight window": {TimePrice{Timezone: "America/New_York", Windows: []CostWindow{{Days: []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}, Start: "22:00", End: "06:00"}}},
			"windows[0]: start must be before end"},
		// One window that holds the instant beside one that is refused: the
		// row is refused, not the window.
		"a good window beside a bad one": {TimePrice{Timezone: "UTC", Windows: []CostWindow{always[0], {Days: []string{"mon"}, Start: "18:00", End: "13:00"}}},
			"windows[1]: start must be before end"},
		"a price below zero":           {TimePrice{Timezone: "UTC", Windows: always, OutputPricePerMillion: pf(-1)}, "output_price_per_million must be non-negative"},
		"a price that is not a number": {TimePrice{Timezone: "UTC", Windows: always, CachePricePerMillion: pf(math.NaN())}, "cache_price_per_million must be finite"},
	}
}

// TestPriceAtIgnoresARowItCannotRead pins the resolver on rows the
// registry's validator refuses, which a hand edit can leave in the file: the
// row is passed over whole, at every instant, and the flat price is in
// force. It must never panic (this runs while the picker draws, where a
// panic takes the terminal down mid-screen), never apply a row in some of
// its windows only, and never let a row price of -1 become the price in
// force, which would sort the model first and make it the default
// selection. A model whose only rows are such rows is not time-priced, so
// the picker does not mark a price that will never change.
func TestPriceAtIgnoresARowItCannotRead(t *testing.T) {
	always := []CostWindow{{Days: []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}, Start: "00:00", End: "24:00"}}
	at := time.Date(2026, 9, 28, 12, 30, 0, 0, time.UTC) // Monday
	for name, c := range badRows() {
		row := c.row
		row.InputPricePerMillion = pf(0.01)
		cost := peakCost(row)
		wantPrice(t, name, cost.PriceAt(at), pf(1.32), pf(0.044), pf(3.96), -1)
		if cost.TimePriced() {
			t.Errorf("%s: TimePriced() = true for a model whose only row is not applied", name)
		}
	}
	// The overnight row at an instant its writer meant it to hold: Monday
	// 02:45 UTC is Sunday 22:45 in New York.
	overnight := peakCost(badRows()["an overnight window"].row)
	wantPrice(t, "the overnight row, Sunday 22:45 in New York", overnight.PriceAt(time.Date(2026, 9, 28, 2, 45, 0, 0, time.UTC)), pf(1.32), pf(0.044), pf(3.96), -1)
	// A row that cannot be read does not hide a readable one after it, and
	// the model is then time-priced.
	cost := peakCost(TimePrice{Timezone: "Mars/Olympus", Windows: always, InputPricePerMillion: pf(0.01)},
		TimePrice{Timezone: "UTC", Windows: always, InputPricePerMillion: pf(0.5)})
	wantPrice(t, "an unreadable row first", cost.PriceAt(at), pf(0.5), pf(0.044), pf(3.96), 1)
	if !cost.TimePriced() {
		t.Error("TimePriced() = false for a model with one row that is applied")
	}
}

// TestTimePriceProblemIsTheValidatorsRefusal pins the words Problem gives
// for a row that is not applied. They are the registry validator's own (the
// refusal a write of that model gets), and `wt model list` prints them, so
// they are how a user learns why a row they wrote changes nothing: an
// overnight window reads "start must be before end". A row either flow's
// sync writes has no problem.
func TestTimePriceProblemIsTheValidatorsRefusal(t *testing.T) {
	for name, c := range badRows() {
		if got := c.row.Problem(); got != c.want {
			t.Errorf("%s: Problem() = %q, want %q", name, got, c.want)
		}
		// By construction, but pinned: Problem is the validator's answer for
		// the same row, so the two cannot come to disagree about a rule.
		if err := validateTimePrice(c.row.table()); err == nil || err.Error() != c.want {
			t.Errorf("%s: validateTimePrice = %v, want %q", name, err, c.want)
		}
	}
	if got := offpeakPrice(pf(0.66), pf(0.022), pf(1.98)).Problem(); got != "" {
		t.Errorf("ollama's off-peak row: Problem() = %q, want none", got)
	}
	if got := offpeakPrice(nil, nil, nil).Problem(); got != "" {
		t.Errorf("a row that sets no price: Problem() = %q, want none", got)
	}
}

// timedRegistry is a registry with one cloud model, its cost table holding
// the time_prices text given.
func timedRegistry(timePrices string) string {
	return `
[[providers]]
id = "openrouter"
location = "cloud"
[providers.auth]
type = "api_key"

[[models]]
id = "openrouter/night-owl"
family = "owl"
provider_id = "openrouter"
model_name = "acme/night-owl"
tags = ["code"]

[models.cost]
input_price_per_million = 12.5
cache_price_per_million = 1.25
output_price_per_million = 75.0
` + timePrices
}

// TestLoadKeepsATimePricesRowTheValidatorRefusesAndNamesIt pins what a
// hand-written row that breaks a rule does to a registry: nothing, loudly
// enough. The file still loads (a slip in one row must not stop every wt
// command), the row is not applied at any instant, the model's price is its
// flat one, and Model.Malformed names the row and the rule, which `wt model
// list` prints and the Models tab shows. A readable row beside it is still
// applied.
func TestLoadKeepsATimePricesRowTheValidatorRefusesAndNamesIt(t *testing.T) {
	t.Setenv("WT_REGISTRY", "")
	t.Setenv("MODELMAN_REGISTRY", "")
	writeRegistry(t, t.TempDir(), timedRegistry(`
[[models.cost.time_prices]]
label = "night"
timezone = "America/New_York"
output_price_per_million = 10.0
windows = [{ days = ["mon", "tue", "wed", "thu", "fri", "sat", "sun"], start = "22:00", end = "06:00" }]

[[models.cost.time_prices]]
label = "weekend"
timezone = "UTC"
output_price_per_million = 40.0
windows = [{ days = ["sat", "sun"], start = "00:00", end = "24:00" }]
`))
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() = %v, want the registry to load", err)
	}
	m := cfg.Models[0]
	if got, want := m.Malformed(), []string{"cost.time_prices[0]: windows[0]: start must be before end"}; !slices.Equal(got, want) {
		t.Errorf("Malformed() = %q, want %q", got, want)
	}
	// Monday 02:45 UTC is Sunday 22:45 in New York: inside the window as its
	// writer meant it, and a Monday in UTC, so outside the weekend row.
	wantPrice(t, "inside the overnight window", m.Cost.PriceAt(time.Date(2026, 9, 28, 2, 45, 0, 0, time.UTC)), pf(12.5), pf(1.25), pf(75), -1)
	wantPrice(t, "Saturday", m.Cost.PriceAt(time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)), pf(12.5), pf(1.25), pf(40), 1)
	if !m.Cost.TimePriced() {
		t.Error("TimePriced() = false, want true: the weekend row is applied")
	}
}

// TestLoadStopsOnATimePricesValueOfTheWrongType pins the one kind of slip in
// a cost.time_prices row that wt does not tolerate: a value of the wrong TOML
// type (windows that are a string, a price in quotes, days that are not a
// list). Like a wrong type anywhere else in the registry it fails the load,
// for every wt command, with an error that names the key; nothing reaches
// the resolver, so its tests cover only rows that decode. This is the
// loader's behaviour from before #322; it is pinned here because rows are
// now worth writing by hand, and the error is what such a slip gets.
func TestLoadStopsOnATimePricesValueOfTheWrongType(t *testing.T) {
	for name, c := range map[string]struct{ row, key string }{
		"windows that are a string":   {"timezone = \"UTC\"\nwindows = \"always\"", "models.cost.time_prices.windows"},
		"a price in quotes":           {"timezone = \"UTC\"\ninput_price_per_million = \"cheap\"\nwindows = []", "models.cost.time_prices.input_price_per_million"},
		"a timezone that is a number": {"timezone = 5\nwindows = []", "models.cost.time_prices.timezone"},
		"days that are a string":      {"timezone = \"UTC\"\nwindows = [{ days = \"mon\", start = \"00:00\", end = \"24:00\" }]", "models.cost.time_prices.windows.days"},
	} {
		t.Setenv("WT_REGISTRY", "")
		t.Setenv("MODELMAN_REGISTRY", "")
		writeRegistry(t, t.TempDir(), timedRegistry("\n[[models.cost.time_prices]]\n"+c.row+"\n"))
		_, err := Load()
		if err == nil || !strings.Contains(err.Error(), c.key) || !strings.Contains(err.Error(), "incompatible types") {
			t.Errorf("%s: Load() = %v, want a parse error that names %s", name, err, c.key)
		}
	}
}

// TestFlatAndTimePriced pins the two small readers the picker uses beside
// PriceAt: Flat is the cost table's own prices with no row applied (what a
// LiteLLM route carries), and TimePriced says whether the model has rows at
// all, which is what marks its price in the picker as one that changes.
func TestFlatAndTimePriced(t *testing.T) {
	plain := ModelCost{InputPricePerMillion: pf(1), OutputPricePerMillion: pf(2)}
	wantPrice(t, "Flat", plain.Flat(), pf(1), nil, pf(2), -1)
	wantPrice(t, "PriceAt with no rows", plain.PriceAt(time.Date(2026, 10, 3, 1, 0, 0, 0, time.UTC)), pf(1), nil, pf(2), -1)
	timed := peakCost(offpeakPrice(pf(0.66), pf(0.022), pf(1.98)))
	wantPrice(t, "Flat of a model with rows", timed.Flat(), pf(1.32), pf(0.044), pf(3.96), -1)
	if plain.TimePriced() || !timed.TimePriced() || (ModelCost{}).TimePriced() {
		t.Error("TimePriced: want true only for the cost table with a time_prices row that is applied")
	}
	// The zero time is an instant like any other (Monday 0001-01-01 00:00
	// UTC, inside the off-peak window): PriceAt has no "no clock" value.
	wantPrice(t, "the zero time", timed.PriceAt(time.Time{}), pf(0.66), pf(0.022), pf(1.98), 0)
}
```

Then append to `wt/cmd/wt/model_list_test.go` (its imports are already there). It runs the real listing over a registry file with the overnight row in it:

```go
// TestModelListNamesATimePricesRowThatIsNotApplied pins where a user learns
// that a cost.time_prices row they wrote is not in use (#322). The model
// picker shows the price in force from those rows, and passes over a row the
// registry's rules refuse; the likeliest is an overnight window written
// "22:00" to "06:00", which a window cannot be. The picker then shows the
// model's own price with no mark, as for any model with one price, so
// without this line nothing says why the row changes nothing. The listing
// names the row and the rule on stderr, and --json has the same phrase in
// the model's "malformed" array.
func TestModelListNamesATimePricesRowThatIsNotApplied(t *testing.T) {
	registry := malformedRegistry(false) + `
[[providers]]
id = "openrouter"
location = "cloud"
[providers.auth]
type = "api_key"

[[models]]
id = "openrouter/night-owl"
family = "owl"
provider_id = "openrouter"
model_name = "acme/night-owl"

[models.cost]
output_price_per_million = 75.0

[[models.cost.time_prices]]
timezone = "America/New_York"
output_price_per_million = 10.0
windows = [{ days = ["mon", "tue", "wed", "thu", "fri", "sat", "sun"], start = "22:00", end = "06:00" }]
`
	const problem = "cost.time_prices[0]: windows[0]: start must be before end"
	out, errOut, path := runModelListOver(t, registry, false)
	if want := "openrouter/night-owl: " + problem + "; wt reads it as absent (fix the entry in " + path + ")\n"; errOut != want {
		t.Errorf("stderr =\n%s\nwant\n%s", errOut, want)
	}
	if !strings.Contains(out, "openrouter/night-owl") || strings.Contains(out, "time_prices") {
		t.Errorf("stdout =\n%s\nwant the table alone, the model listed", out)
	}
	out, errOut, _ = runModelListOver(t, registry, true)
	if !strings.Contains(out, `"`+problem+`"`) || !strings.Contains(errOut, problem) {
		t.Errorf("--json: stdout lacks the phrase in a malformed array, or stderr the line:\n%s\n%s", out, errOut)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run (from `wt/`): `go test -count=1 ./internal/config -run 'TestPriceAt|TestFlatAndTimePriced|TestTimePriceProblem|TestLoadKeepsATimePrices|TestLoadStopsOnATimePrices'`
Expected: the package does not build.

```text
# github.com/ohanaverse/local-ai-setup/wt/internal/config [github.com/ohanaverse/local-ai-setup/wt/internal/config.test]
internal/config/price_at_test.go:35:47: undefined: PriceInForce
internal/config/price_at_test.go:78:49: cost.PriceAt undefined (type ModelCost has no field or method PriceAt)
…
FAIL	github.com/ohanaverse/local-ai-setup/wt/internal/config [build failed]
```

Run (from `wt/`): `go test -count=1 ./cmd/wt -run TestModelListNamesATimePricesRowThatIsNotApplied 2>&1 | grep -v '^wt: migrated'`
Expected: FAIL. The registry loads and the model is listed, and nothing names the row:

```text
--- FAIL: TestModelListNamesATimePricesRowThatIsNotApplied (0.00s)
    model_list_test.go:678: stderr =
        
        want
        openrouter/night-owl: cost.time_prices[0]: windows[0]: start must be before end; wt reads it as absent (fix the entry in /…/local-ai/registry.toml)
    model_list_test.go:685: --json: stdout lacks the phrase in a malformed array, or stderr the line:
        …
FAIL
```

- [ ] **Step 3: Write the implementation**

(a) The resolver.

Create `wt/internal/config/price_at.go`:

```go
package config

import (
	"slices"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/tomlw"
)

// PriceInForce is a model's three per-token prices, per million tokens, at
// one instant. A nil price is one the model does not have.
type PriceInForce struct {
	Input, Cache, Output *float64
	// Row is the index in ModelCost.TimePrices of the row whose window held
	// the instant, or -1 when no row's did and the prices are the flat ones.
	Row int
}

// TimePriced reports whether c has a time_prices row that PriceAt applies,
// which is to say whether its price depends on when it is asked for. A cost
// table whose every row has a Problem has one price, the flat one.
func (c ModelCost) TimePriced() bool {
	return slices.ContainsFunc(c.TimePrices, func(row TimePrice) bool { return row.Problem() == "" })
}

// Flat is c's own three prices with no time_prices row applied: the prices
// in force whenever no row's window holds the instant, and the ones a
// LiteLLM route carries.
func (c ModelCost) Flat() PriceInForce {
	return PriceInForce{Input: c.InputPricePerMillion, Cache: c.CachePricePerMillion, Output: c.OutputPricePerMillion, Row: -1}
}

// PriceAt is the prices in force for c at the instant at. The rows are tried
// in file order and the first whose windows hold the instant wins: it
// supplies each price it sets, and a price it does not set stays the flat
// one. With no such row the flat prices are in force.
//
// A window is the minutes [start, end) of the listed weekdays on the wall
// clock of its row's IANA timezone, so it follows that zone's daylight
// saving time: an hour the clock repeats is in the window both times, and an
// hour it skips is in it at no instant that day. Only at's instant counts,
// not the zone it is expressed in.
//
// A row with a Problem is passed over whole, at every instant, and the rows
// after it are still tried. PriceAt never fails: it runs while a screen is
// drawn.
//
// This is the reader of every cost.time_prices row, those `wt cloud-sync`
// writes and those written by hand. It is pure: the caller passes the
// clock.
func (c ModelCost) PriceAt(at time.Time) PriceInForce {
	p := c.Flat()
	for i, row := range c.TimePrices {
		if row.Problem() != "" || !row.holds(at) {
			continue
		}
		p.Row = i
		if row.InputPricePerMillion != nil {
			p.Input = row.InputPricePerMillion
		}
		if row.CachePricePerMillion != nil {
			p.Cache = row.CachePricePerMillion
		}
		if row.OutputPricePerMillion != nil {
			p.Output = row.OutputPricePerMillion
		}
		break
	}
	return p
}

// Problem says why PriceAt does not apply the row: the reason the registry's
// validator refuses it (validateTimePrice, the rule a registry write holds a
// row to), in the validator's words, or "" for a row it accepts. The loader
// only types a row's values, so a row written by hand can be in the file and
// break a rule: a window that runs past midnight ("22:00" to "06:00": write
// two windows, one to "24:00" and one from "00:00"), a timezone that is not
// an IANA name, a time that is not "HH:MM", a price below zero. One bad
// window is enough: the validator refuses the row, not the window, and a row
// applied in some of its windows only would show a price its writer did not
// mean. Model.Malformed names such a row, which is how `wt model list` and
// the Models tab say that it is in the file and not in use.
func (tp TimePrice) Problem() string {
	if err := validateTimePrice(tp.table()); err != nil {
		return err.Error()
	}
	return ""
}

// table is the row as the registry's validator reads one: the same keys,
// holding the values the typed decode gave.
func (tp TimePrice) table() *tomlw.Table {
	row := tomlw.NewTable()
	row.Set("timezone", tp.Timezone)
	for i, v := range []*float64{tp.InputPricePerMillion, tp.CachePricePerMillion, tp.OutputPricePerMillion} {
		if v != nil {
			row.Set(priceKeys[i], *v)
		}
	}
	windows := make([]any, len(tp.Windows))
	for i, w := range tp.Windows {
		days := make([]any, len(w.Days))
		for j, d := range w.Days {
			days[j] = d
		}
		window := tomlw.NewTable()
		window.Set("days", days)
		window.Set("start", w.Start)
		window.Set("end", w.End)
		windows[i] = window
	}
	row.Set("windows", windows)
	return row
}

// holds reports whether one of the row's windows holds the instant at. It is
// asked only of a row with no Problem, and still reads nothing it has not
// checked: a zone or a time it cannot read holds no instant.
func (tp TimePrice) holds(at time.Time) bool {
	// time.LoadLocation reads "" as UTC and "Local" as the host's zone;
	// neither is an IANA name, and the validator refuses both.
	if tp.Timezone == "" || tp.Timezone == "Local" {
		return false
	}
	zone, err := time.LoadLocation(tp.Timezone)
	if err != nil {
		return false
	}
	local := at.In(zone)
	// time.Weekday counts from Sunday; weekDays from Monday.
	day := weekDays[(int(local.Weekday())+6)%7]
	minute := local.Hour()*60 + local.Minute()
	for _, w := range tp.Windows {
		start, okStart := clockMinutes(w.Start)
		end, okEnd := clockMinutes(w.End)
		if okStart && okEnd && start <= minute && minute < end && slices.Contains(w.Days, day) {
			return true
		}
	}
	return false
}

// clockMinutes reads "HH:MM", from "00:00" to "24:00", as minutes of the
// day.
func clockMinutes(s string) (int, bool) {
	if len(s) != 5 || s[2] != ':' {
		return 0, false
	}
	var n [4]int
	for i, at := range []int{0, 1, 3, 4} {
		if s[at] < '0' || s[at] > '9' {
			return 0, false
		}
		n[i] = int(s[at] - '0')
	}
	minutes := n[2]*10 + n[3]
	total := (n[0]*10+n[1])*60 + minutes
	if minutes > 59 || total > 24*60 {
		return 0, false
	}
	return total, true
}
```

(b) `wt/internal/config/config.go`: `Model.Malformed` names a row that is not applied.

Find:

```go
// Malformed names what the loader tolerated in this row's fetch and draft
// instead of failing on it, as phrases for a person: "fetch is not a table",
// "draft.local_path is not a string". fetch comes before draft, and repo
// before local_path. nil for a row with nothing malformed, and for a Model
// that was not read from a registry file.
func (m Model) Malformed() []string {
	return append(m.Fetch.problems("fetch"), m.Draft.problems("draft")...)
}
```

Replace with:

```go
// Malformed names what the loader tolerated in this row instead of failing on
// it, as phrases for a person. In its fetch and draft: "fetch is not a
// table", "draft.local_path is not a string" (fetch before draft, repo
// before local_path); each reads as absent. Then each cost.time_prices row
// the registry's validator refuses (TimePrice.Problem), by its place in the
// file: "cost.time_prices[0]: windows[0]: start must be before end"; the
// model picker does not apply such a row. nil for a row with nothing
// malformed, and for a Model that was not read from a registry file.
func (m Model) Malformed() []string {
	out := append(m.Fetch.problems("fetch"), m.Draft.problems("draft")...)
	for i, row := range m.Cost.TimePrices {
		if problem := row.Problem(); problem != "" {
			out = append(out, fmt.Sprintf("cost.time_prices[%d]: %s", i, problem))
		}
	}
	return out
}
```

Nothing else that reads `Malformed` changes, and each reader is right as it stands: `wt model list` prints `<id>: <phrases>; wt reads it as absent (fix the entry in <registry>)` and puts the phrases in `--json`'s `malformed` array (`cmd/wt/model_list.go`, `noteMalformed`); the Models tab shows them under the selected row (`internal/configeditor/models_tab.go`, `malformedLine`); and the model form adds the "fix the entry" hint to a refused save of such a row (`models_form.go`, `saveErrorText`), which is right, because the registry writer refuses to write a row whose cost table breaks a rule and no field of the form can repair a window.

(c) `wt/cmd/wt/model_list.go`: the command's help says so.

Find:

```go
			"read as absent; a line on stderr names the row and the problem, and --json\n" +
			"has the same in each model's \"malformed\" array.",
```

Replace with:

```go
			"read as absent; a line on stderr names the row and the problem, and --json\n" +
			"has the same in each model's \"malformed\" array. A cost.time_prices row that\n" +
			"breaks the registry's rules (a window that ends before it starts, an unknown\n" +
			"timezone) is named the same way: the model picker does not apply such a row.",
```

- [ ] **Step 4: Run the tests to verify they pass**

Run (from `wt/`): `gofmt -l internal cmd && go vet ./internal/config ./cmd/wt && go test -count=1 ./internal/config -run 'TestPriceAt|TestFlatAndTimePriced|TestTimePriceProblem|TestLoadKeepsATimePrices|TestLoadStopsOnATimePrices' -v | grep -E '^(---|ok|FAIL)'`
Expected (`gofmt -l` prints nothing):

```text
--- PASS: TestPriceAtOffpeakBoundaries (0.00s)
--- PASS: TestPriceAtReadsAWindowInItsRowsTimezone (0.00s)
--- PASS: TestPriceAtFollowsDaylightSavingTime (0.00s)
--- PASS: TestPriceAtFallsBackPerField (0.00s)
--- PASS: TestPriceAtFirstMatchingRowWins (0.00s)
--- PASS: TestPriceAtAnOpenRouterSchedule (0.00s)
--- PASS: TestPriceAtIgnoresARowItCannotRead (0.00s)
--- PASS: TestTimePriceProblemIsTheValidatorsRefusal (0.00s)
--- PASS: TestLoadKeepsATimePricesRowTheValidatorRefusesAndNamesIt (0.00s)
--- PASS: TestLoadStopsOnATimePricesValueOfTheWrongType (0.00s)
--- PASS: TestFlatAndTimePriced (0.00s)
ok  	github.com/ohanaverse/local-ai-setup/wt/internal/config	(time)
```

Run (from `wt/`): `go test -count=1 ./cmd/wt -run 'TestModelList' -v 2>&1 | grep -E '^(--- FAIL|ok|FAIL)'`
Expected: `ok  	github.com/ohanaverse/local-ai-setup/wt/cmd/wt	(time)` and no `--- FAIL` line: the new test passes and the existing listing tests, whose rows have no `cost.time_prices`, are unchanged.

Run (from `wt/`): `go test -count=1 ./internal/config ./internal/modeladmin ./internal/configeditor`
Expected: three `ok`. `Model.Malformed` has readers in all three packages, and none of their fixtures holds a row with a problem.

The timezone tests read the host's zone database (`time.LoadLocation`), which macOS, wt's platform, ships; wt does not embed one (the validator's comment in `registry_validate.go` says the same of itself).

- [ ] **Step 5: Commit**

From the repo root:

```bash
git add wt/internal/config/price_at.go wt/internal/config/price_at_test.go wt/internal/config/config.go wt/cmd/wt/model_list.go wt/cmd/wt/model_list_test.go
git commit -m "feat(wt): config.ModelCost.PriceAt resolves cost.time_prices rows at an instant; a row that breaks a rule is passed over and named (#322)"
```

### Task 4: The picker's COST column and cost sort use the price in force (slice C)

**Files:**
- Modify: `wt/internal/tui/modelrows.go`, `wt/internal/tui/modeltable.go`
- Modify (tests): `wt/internal/tui/modelrows_test.go` (one import; append), `wt/internal/tui/modeltable_test.go` (append), `wt/internal/tui/layout_test.go` (two imports; append)

**Interfaces:**
- Consumes from Task 3: `config.PriceInForce`, `(config.ModelCost).PriceAt(time.Time)`, `.Flat()`, `.TimePriced()`.
- Consumes (existing, `wt/internal/tui`): `type tableRow struct{ catalog.Row; counts usage.UsageCounts; stats survey.Stats }`; `type tableInput struct{ cfg *config.Config; agent string; models []config.Model; inventory *localmodels.Snapshot; hideDiscovered, skipRoute bool; usage usage.Store; stats map[string]survey.Stats }`; `buildRows(in tableInput) []tableRow`; `rowCostKey(r tableRow) costKey`; `sortRows(rows []tableRow)`; `renderTable(rows []tableRow, cfg *config.Config, agent string, refs map[string]int, lastID string) modelTable`; `formatPerToken(cost config.ModelCost) string`; the column indexes `colCost`, `col1D`, …; `type modelItem struct{ model config.Model; line string; cells []string; cols *tuilayout.Columns; … }`. Test helpers: `f64(float64) *float64`, `rowsTestCfg()`, `rowIDs`, `tableTestRows()`, `modelTestConfig()`, `runningOmlxSnapshot()`, `flowEnter(t, m model, agent string) model`, `resized(t, m, width, height) model`, `updateMsg(m model, msg tea.Msg) (model, tea.Cmd)`, `itemIDs(m model) []string`, `assertFits(t, name, view string, width, height int)`, `tempStateDir`, `stubUsageStore`, `stubRefcountStore`, `stubInventory`, `newPickModel(cfg, models, theme, skipRoute) pickModel`, `layoutWidths`, `layoutHeights`.
- Produces (Task 5 uses these):
  - `var pickerNow = time.Now` (the picker's clock; a test replaces it)
  - `tableRow.at time.Time` and `func (r tableRow) price() config.PriceInForce`
  - `tableInput.now time.Time`
  - `const timePricedMark = "~"`, `const costLegend = "COST (~ varies by time)"`, `func costCell(r tableRow) string`
  - test fixtures: `timedTableRows(at time.Time) []tableRow`, `deepseekDear`, `ollamaPeak` (`modeltable_test.go`); `timedPickerConfig() *config.Config` (`layout_test.go`); `pickerMonday`, `pickerSaturday` (`modelrows_test.go`)
  - tests: `TestSortRowsUsesThePriceInForce`, `TestBuildRowsReadsThePickersClock`, `TestRenderTableShowsThePriceInForce`, `TestModelPickerWithTimePricedModelsFitsTheTerminal`, `TestAnOpenPickerKeepsThePricesItOpenedWith`

- [ ] **Step 1: Write the failing tests**

(a) Three import edits, for the tests that follow.

In `wt/internal/tui/modelrows_test.go`:

Find:

```go
import (
	"strings"
	"testing"
```

Replace with:

```go
import (
	"strings"
	"testing"
	"time"
```

In `wt/internal/tui/layout_test.go`, two:

Find:

```go
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/list"
```

Replace with:

```go
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/list"
```

Find:

```go
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
```

Replace with:

```go
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tuilayout"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
```

(b) Append to `wt/internal/tui/modelrows_test.go`:

```go
// timedRows are two cloud rows for the price tests: "timed" has a dear flat
// price (3.96 out) and a row that halves it at weekends, and "steady" costs
// 3 out all week. On a weekday steady is the cheaper one; at a weekend timed
// is.
func timedRows(at time.Time) []tableRow {
	weekend := config.TimePrice{Label: "openrouter", Timezone: "UTC",
		InputPricePerMillion: f64(0.66), CachePricePerMillion: f64(0.022), OutputPricePerMillion: f64(1.98),
		Windows: []config.CostWindow{{Days: []string{"sat", "sun"}, Start: "00:00", End: "24:00"}}}
	return []tableRow{
		{Row: catalog.Row{Location: config.LocationCloud, Model: config.Model{ID: "timed", Cost: config.ModelCost{
			InputPricePerMillion: f64(1.32), CachePricePerMillion: f64(0.044), OutputPricePerMillion: f64(3.96), TimePrices: []config.TimePrice{weekend}}}}, at: at},
		{Row: catalog.Row{Location: config.LocationCloud, Model: config.Model{ID: "steady", Cost: config.ModelCost{
			InputPricePerMillion: f64(1), OutputPricePerMillion: f64(3)}}}, at: at},
	}
}

var (
	pickerMonday   = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	pickerSaturday = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
)

// TestSortRowsUsesThePriceInForce pins the cost sort on a model priced by
// time of day: it sorts by the price in force when the table was built, not
// by its flat price. The sort order is also the picker's default selection,
// so a model that is the cheapest right now is what a bare Enter launches,
// and one in its dear window does not keep a cheap model's place. A row with
// no clock (one built by hand) sorts by its flat price, and sorting again at
// the same instant changes nothing.
func TestSortRowsUsesThePriceInForce(t *testing.T) {
	for _, c := range []struct {
		name string
		at   time.Time
		want string
	}{
		{"a weekday: the flat price is in force", pickerMonday, "steady,timed"},
		{"a weekend: the cheap row is in force", pickerSaturday, "timed,steady"},
		{"no clock: the flat price", time.Time{}, "steady,timed"},
	} {
		rows := timedRows(c.at)
		sortRows(rows)
		if got := strings.Join(rowIDs(rows), ","); got != c.want {
			t.Errorf("%s: order = %s, want %s", c.name, got, c.want)
		}
		sortRows(rows)
		if got := strings.Join(rowIDs(rows), ","); got != c.want {
			t.Errorf("%s: sorted a second time, order = %s, want it unchanged (%s)", c.name, got, c.want)
		}
	}
	// A model whose flat price is missing and whose row supplies one has cost
	// data while the row is in force, and none outside it.
	rowsOnly := func(at time.Time) []tableRow {
		rows := timedRows(at)
		rows[0].Model.Cost.InputPricePerMillion, rows[0].Model.Cost.CachePricePerMillion, rows[0].Model.Cost.OutputPricePerMillion = nil, nil, nil
		return rows
	}
	if k := rowCostKey(rowsOnly(pickerSaturday)[0]); k.noData || k.out != 1.98 || k.in != 0.66 {
		t.Errorf("rows only, at the weekend: cost key = %+v, want the row's prices", k)
	}
	if k := rowCostKey(rowsOnly(pickerMonday)[0]); !k.noData {
		t.Errorf("rows only, on a weekday: cost key = %+v, want no cost data", k)
	}
}

// TestBuildRowsReadsThePickersClock pins where the instant comes from: the
// table is given one (tableInput.now), and one left out is the picker's own
// clock, read once for the whole table. Every row is priced at that one
// instant, so two rows of a table are never priced on two sides of a window
// boundary.
func TestBuildRowsReadsThePickersClock(t *testing.T) {
	cfg := rowsTestCfg()
	models := []config.Model{{ID: "openrouter/a", ProviderID: "openrouter"}, {ID: "openrouter/b", ProviderID: "openrouter"}}
	rows := buildRows(tableInput{cfg: cfg, models: models, now: pickerSaturday})
	if len(rows) != 2 || !rows[0].at.Equal(pickerSaturday) || !rows[1].at.Equal(pickerSaturday) {
		t.Fatalf("rows built with a clock: %d rows, at %v", len(rows), rows)
	}
	calls := 0
	old := pickerNow
	pickerNow = func() time.Time { calls++; return pickerMonday }
	t.Cleanup(func() { pickerNow = old })
	rows = buildRows(tableInput{cfg: cfg, models: models})
	if calls != 1 || !rows[0].at.Equal(pickerMonday) || !rows[1].at.Equal(pickerMonday) {
		t.Errorf("rows built without a clock: the picker's clock was read %d time(s), rows at %v and %v; want one reading for both", calls, rows[0].at, rows[1].at)
	}
}
```

(c) Append to `wt/internal/tui/modeltable_test.go` (its imports, `strings` and `time` among them, are already there):

```go
// timedTableRows is a picker holding the two kinds of time-priced model wt
// knows, with the rows `wt cloud-sync` stores for each, and one model with a
// single price: an openrouter model whose flat price is its dear level and
// whose row is the cheap one (deepseek's real schedule: dear on weekdays
// 01:00-04:00 and 06:00-10:00 UTC), and an ollama cloud model with ollama's
// off-peak row (cheap outside 12:00-18:00 UTC on weekdays).
func timedTableRows(at time.Time) []tableRow {
	weekdays := []string{"mon", "tue", "wed", "thu", "fri"}
	openrouter := config.TimePrice{Label: "openrouter", Timezone: "UTC",
		InputPricePerMillion: f64(0.66), CachePricePerMillion: f64(0.022), OutputPricePerMillion: f64(1.98),
		Windows: []config.CostWindow{
			{Days: weekdays, Start: "00:00", End: "01:00"}, {Days: weekdays, Start: "04:00", End: "06:00"},
			{Days: weekdays, Start: "10:00", End: "24:00"}, {Days: []string{"sat", "sun"}, Start: "00:00", End: "24:00"},
		}}
	offpeak := config.TimePrice{Label: "off-peak", Timezone: "UTC",
		InputPricePerMillion: f64(0.25), CachePricePerMillion: f64(0.025), OutputPricePerMillion: f64(1),
		Windows: []config.CostWindow{
			{Days: weekdays, Start: "00:00", End: "12:00"}, {Days: weekdays, Start: "18:00", End: "24:00"},
			{Days: []string{"sat", "sun"}, Start: "00:00", End: "24:00"},
		}}
	return []tableRow{
		{Row: catalog.Row{Model: config.Model{ID: "openrouter/deepseek--deepseek-v4-pro-0813", Family: "deepseek", Cost: config.ModelCost{
			InputPricePerMillion: f64(1.32), CachePricePerMillion: f64(0.044), OutputPricePerMillion: f64(3.96), TimePrices: []config.TimePrice{openrouter}}},
			Location: config.LocationCloud, Status: catalog.StatusOK}, at: at},
		{Row: catalog.Row{Model: config.Model{ID: "ollama/glm-5.3:cloud", Family: "glm", Cost: config.ModelCost{
			InputPricePerMillion: f64(0.5), CachePricePerMillion: f64(0.05), OutputPricePerMillion: f64(2), TimePrices: []config.TimePrice{offpeak}}},
			Location: config.LocationCloud, Status: catalog.StatusOK}, at: at},
		{Row: catalog.Row{Model: config.Model{ID: "openrouter/z-ai--glm-5.2", Family: "glm", Cost: config.ModelCost{
			InputPricePerMillion: f64(0.06), CachePricePerMillion: f64(0.059), OutputPricePerMillion: f64(6)}},
			Location: config.LocationCloud, Status: catalog.StatusOK}, at: at},
	}
}

// The instants the time-priced tables are drawn at, all on Monday 2026-10-12
// UTC: 02:45 is in deepseek's dear window and in ollama's off-peak; 13:00 is
// in deepseek's cheap window and in ollama's peak.
var (
	deepseekDear = time.Date(2026, 10, 12, 2, 45, 0, 0, time.UTC)
	ollamaPeak   = time.Date(2026, 10, 12, 13, 0, 0, 0, time.UTC)
)

// costCellOf is the COST cell of the row with this id, as drawn.
func costCellOf(t *testing.T, tbl modelTable, id string) string {
	t.Helper()
	for _, it := range tbl.items {
		if it.model.ID == id {
			return it.cells[colCost]
		}
	}
	t.Fatalf("no row %s in the table", id)
	return ""
}

// TestRenderTableShowsThePriceInForce pins what the COST column shows for a
// model with cost.time_prices rows: the three prices in force at the instant
// the table was built, followed by "~", the mark that the price depends on
// the time. The owner picks a model by this column; before #322 it showed
// the flat price all week, which for a model OpenRouter prices by time of
// day is its dearest level (double what deepseek costs for 79% of the week)
// and for an ollama cloud model is the peak price, wrong for every hour
// outside 12:00-18:00 UTC. A model with one price has no mark, and when the
// table has a marked row its COST heading says what the mark means.
func TestRenderTableShowsThePriceInForce(t *testing.T) {
	const deepseek, ollama, plain = "openrouter/deepseek--deepseek-v4-pro-0813", "ollama/glm-5.3:cloud", "openrouter/z-ai--glm-5.2"
	for _, c := range []struct {
		name               string
		at                 time.Time
		deepseek, ollamaAt string
	}{
		{"deepseek dear, ollama off-peak", deepseekDear, " 1.3200  0.0440  3.9600~", " 0.2500  0.0250  1.0000~"},
		{"deepseek cheap, ollama peak", ollamaPeak, " 0.6600  0.0220  1.9800~", " 0.5000  0.0500  2.0000~"},
		// A row built by hand has no clock: the flat prices, still marked,
		// because the mark says what the model is, not what hour it is.
		{"no clock", time.Time{}, " 1.3200  0.0440  3.9600~", " 0.5000  0.0500  2.0000~"},
	} {
		tbl := renderTable(timedTableRows(c.at), nil, "", nil, "")
		if got := costCellOf(t, tbl, deepseek); got != c.deepseek {
			t.Errorf("%s: deepseek's COST = %q, want %q", c.name, got, c.deepseek)
		}
		if got := costCellOf(t, tbl, ollama); got != c.ollamaAt {
			t.Errorf("%s: the ollama cloud model's COST = %q, want %q", c.name, got, c.ollamaAt)
		}
		// One price all week: no mark, padded to the column like the rest.
		if got := costCellOf(t, tbl, plain); got != " 0.0600  0.0590  6.0000 " {
			t.Errorf("%s: the one-price model's COST = %q, want it unmarked", c.name, got)
		}
		if !strings.Contains(tbl.header, "  COST (~ varies by time)   1D") {
			t.Errorf("%s: header = %q, want the COST heading to explain the mark", c.name, tbl.header)
		}
		// The heading is no wider than the column it heads, and every cell
		// is padded to that column, so the columns after it still line up
		// under their own headings.
		if got := strings.Index(tbl.header, "1D") - strings.Index(tbl.header, "COST"); got != 24+2 {
			t.Errorf("%s: the 1D heading starts %d columns after COST, want 26 (a 24-column cell and the separator)", c.name, got)
		}
		for _, it := range tbl.items {
			if got := len(it.cells[colCost]); got != 24 {
				t.Errorf("%s: %s: the COST cell %q is %d columns, want 24", c.name, it.model.ID, it.cells[colCost], got)
			}
		}
	}

	// A table with no time-priced model is drawn as it always was.
	if tbl := renderTable(tableTestRows(), nil, "", nil, ""); strings.Contains(tbl.header, "~") || !strings.Contains(tbl.header, "  COST  ") {
		t.Errorf("header of a table with no time-priced model = %q, want a plain COST heading", tbl.header)
	}
	// A discovered row shows no price, and so no mark, whatever its cost
	// table holds.
	rows := timedTableRows(ollamaPeak)
	rows[0].Discovered = true
	if got := costCellOf(t, renderTable(rows, nil, "", nil, ""), deepseek); strings.TrimSpace(got) != "-" {
		t.Errorf("a discovered row's COST = %q, want -", got)
	}
	// A time-priced model with no price in force (rows only, outside their
	// windows) is "-~": too narrow a column for the legend, so the heading
	// stays COST and the column stays as wide as its cells.
	bare := timedTableRows(deepseekDear)[:1]
	bare[0].Model.Cost.InputPricePerMillion, bare[0].Model.Cost.CachePricePerMillion, bare[0].Model.Cost.OutputPricePerMillion = nil, nil, nil
	tbl := renderTable(bare, nil, "", nil, "")
	if got := costCellOf(t, tbl, deepseek); got != "-~  " || strings.Contains(tbl.header, "varies") {
		t.Errorf("rows only, outside their windows: COST = %q, header %q; want \"-~\" under a plain COST heading", got, tbl.header)
	}
	// A model whose only row breaks a rule (an overnight window, which a
	// window cannot be) has one price: its flat one, with no mark, under a
	// plain COST heading. A mark there would promise a change that never
	// comes; `wt model list` is where the row is named.
	odd := timedTableRows(deepseekDear)[:1]
	odd[0].Model.Cost.TimePrices[0].Windows = []config.CostWindow{{Days: []string{"mon"}, Start: "22:00", End: "06:00"}}
	tbl = renderTable(odd, nil, "", nil, "")
	if got := costCellOf(t, tbl, deepseek); got != " 1.3200  0.0440  3.9600" || strings.Contains(tbl.header, "~") {
		t.Errorf("a row that is not applied: COST = %q, header %q; want the flat price unmarked under a plain COST heading", got, tbl.header)
	}
}
```

(d) Append to `wt/internal/tui/layout_test.go`. This is the fit test the screen rule asks for: every supported size, both pickers, an instant on each side of both windows.

```go
// timedPickerConfig is modelTestConfig with the three models of
// timedTableRows added under the fixture's cloud provider: one priced by
// OpenRouter's schedule, one with ollama's off-peak row, one with a single
// price. The ids are the real ones' length, because the fit depends on it.
func timedPickerConfig() *config.Config {
	cfg := modelTestConfig()
	for _, r := range timedTableRows(time.Time{}) {
		_, name, _ := strings.Cut(r.Model.ID, "/")
		cfg.Models = append(cfg.Models, config.Model{ID: "claude/" + name, ProviderID: "claude", ModelName: name, Family: r.Model.Family, Tags: []string{"code"}, Cost: r.Model.Cost})
	}
	return cfg
}

// TestModelPickerWithTimePricedModelsFitsTheTerminal pins the model picker
// with time-priced models in it at every supported terminal size, built at
// an instant on each side of both models' windows. The COST cell of such a
// model is one column wider than a plain one (the "~" mark) and its heading
// is longer, so this is where a table could come out one column too wide for
// its list, which loses the header or the last column on a real screen. The
// view must fit; where the COST column is drawn it must show the price in
// force at that instant, marked; and at 120 columns it must be drawn, so the
// price is somewhere a user can see it. Both pickers are measured: the
// launcher's and the standalone one `wt start` and `wt smoke` open.
func TestModelPickerWithTimePricedModelsFitsTheTerminal(t *testing.T) {
	old := pickerNow
	t.Cleanup(func() { pickerNow = old })
	for _, c := range []struct {
		name             string
		at               time.Time
		deepseek, ollama string
	}{
		{"deepseek dear, ollama off-peak", deepseekDear, " 1.3200  0.0440  3.9600~", " 0.2500  0.0250  1.0000~"},
		{"deepseek cheap, ollama peak", ollamaPeak, " 0.6600  0.0220  1.9800~", " 0.5000  0.0500  2.0000~"},
	} {
		pickerNow = func() time.Time { return c.at }
		for _, width := range layoutWidths {
			for _, height := range layoutHeights {
				tempStateDir(t)
				stubUsageStore(t)
				stubRefcountStore(t)
				stubInventory(t, runningOmlxSnapshot())
				cfg := timedPickerConfig()
				check := func(picker, view string, cols *tuilayout.Columns) {
					t.Helper()
					assertFits(t, c.name+", "+picker, view, width, height)
					if width == 120 && !cols.Shown(colCost) {
						t.Errorf("%s, %s at %dx%d: the COST column is not drawn", c.name, picker, width, height)
					}
					if !cols.Shown(colCost) {
						return
					}
					// A short terminal shows one page of the rows; the two
					// time-priced models sort first among the priced ones, so
					// at 24 lines and more both are on screen.
					if !strings.Contains(view, "COST (~ varies by time)") {
						t.Errorf("%s, %s at %dx%d: the COST heading does not explain the mark:\n%s", c.name, picker, width, height, view)
					}
					if height >= 24 && (!strings.Contains(view, c.deepseek) || !strings.Contains(view, c.ollama)) {
						t.Errorf("%s, %s at %dx%d: the view lacks %q or %q:\n%s", c.name, picker, width, height, c.deepseek, c.ollama, view)
					}
				}

				m := flowEnter(t, model{cfg: cfg, agent: "claude", selectedPath: t.TempDir(), width: width, height: height}, "claude")
				m = resized(t, m, width, height)
				check("the launcher's picker", m.View(), m.models.Items()[0].(*modelItem).cols)

				pm := newPickModel(cfg, cfg.Models, themes.Default, false)
				next, _ := pm.Update(tea.WindowSizeMsg{Width: width, Height: height})
				standalone := next.(pickModel)
				check("the standalone picker", standalone.View(), standalone.list.Items()[0].(*modelItem).cols)
			}
		}
	}
}
```

(e) Then append to the same file. `TestBuildRowsReadsThePickersClock` pins one reading per build; this pins that an open picker is not built again by a resize, a redraw or a cursor move (decision 24), which is the regression that would move rows under the cursor:

```go
// TestAnOpenPickerKeepsThePricesItOpenedWith pins when the picker reads its
// clock: once, when the table is built, and not again while the picker is
// open. The cost sort is also the default selection, so a table that priced
// itself again on a resize or a cursor move would reorder its rows under the
// cursor at a window boundary, between the user reading a row and pressing
// Enter. Here the clock moves from deepseek's dear window to its cheap one
// (and from ollama's off-peak hours to its peak) while the picker is
// resized through every width, redrawn and moved in: the clock is read
// once, the rows keep their order, and the COST cells still show the prices
// of the instant the picker opened at.
func TestAnOpenPickerKeepsThePricesItOpenedWith(t *testing.T) {
	reads, now := 0, deepseekDear
	old := pickerNow
	pickerNow = func() time.Time { reads++; return now }
	t.Cleanup(func() { pickerNow = old })
	stubInventory(t, runningOmlxSnapshot())
	m := flowEnter(t, model{cfg: timedPickerConfig(), agent: "claude", selectedPath: t.TempDir(), width: 120, height: 24}, "claude")
	m = resized(t, m, 120, 24)
	order := strings.Join(itemIDs(m), ",")
	if view := m.View(); reads != 1 || !strings.Contains(view, " 1.3200  0.0440  3.9600~") || !strings.Contains(view, " 0.2500  0.0250  1.0000~") {
		t.Fatalf("on opening: the clock was read %d time(s), want 1, and the view must show deepseek's dear price and ollama's off-peak one:\n%s", reads, view)
	}

	now = ollamaPeak
	for _, size := range [][2]int{{40, 12}, {120, 50}, {80, 24}, {120, 24}} {
		m = resized(t, m, size[0], size[1])
		_ = m.View()
		m, _ = updateMsg(m, tea.KeyMsg{Type: tea.KeyDown})
		_ = m.View()
		m, _ = updateMsg(m, tea.KeyMsg{Type: tea.KeyUp})
		_ = m.View()
	}
	if reads != 1 {
		t.Errorf("after four resizes and eight cursor moves the clock was read %d time(s), want the one reading the table was built with", reads)
	}
	if got := strings.Join(itemIDs(m), ","); got != order {
		t.Errorf("the rows moved while the picker was open:\n%s\nwant the order it opened with:\n%s", got, order)
	}
	if view := m.View(); !strings.Contains(view, " 1.3200  0.0440  3.9600~") || strings.Contains(view, " 0.6600  0.0220  1.9800~") {
		t.Errorf("the open picker shows the new window's price, want the one it opened with:\n%s", view)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run (from `wt/`): `go test -count=1 ./internal/tui -run 'TestSortRowsUsesThePriceInForce|TestBuildRowsReadsThePickersClock|TestRenderTableShowsThePriceInForce|TestModelPickerWithTimePricedModelsFitsTheTerminal|TestAnOpenPickerKeepsThePricesItOpenedWith'`
Expected: the package does not build.

```text
# github.com/ohanaverse/local-ai-setup/wt/internal/tui [github.com/ohanaverse/local-ai-setup/wt/internal/tui.test]
internal/tui/layout_test.go:871:9: undefined: pickerNow
internal/tui/layout_test.go:872:21: undefined: pickerNow
internal/tui/layout_test.go:881:3: undefined: pickerNow
internal/tui/modelrows_test.go:169:149: unknown field at in struct literal of type tableRow
internal/tui/modelrows_test.go:171:68: unknown field at in struct literal of type tableRow
…
FAIL	github.com/ohanaverse/local-ai-setup/wt/internal/tui [build failed]
```

- [ ] **Step 3: Write the implementation**

Six edits to `wt/internal/tui/modelrows.go`.

(a) The import block gains `time`.

Find:

```go
import (
	"sort"
```

Replace with:

```go
import (
	"sort"
	"time"
```

(b) The clock, the row's instant and the row's price.

Find:

```go
// tableRow is one line of the selector table: the shared catalog rules
// (presence status, action, block reason) plus the picker-only decorations
// (agent-scoped usage counts, survey stats).
type tableRow struct {
	catalog.Row
	counts usage.UsageCounts
	stats  survey.Stats
}
```

Replace with:

```go
// pickerNow is the clock a model table reads its prices at. It is read once
// for each table that is built (buildRows), never while one is drawn. A test
// replaces it to draw the picker at a chosen instant.
var pickerNow = time.Now

// tableRow is one line of the selector table: the shared catalog rules
// (presence status, action, block reason) plus the picker-only decorations
// (agent-scoped usage counts, survey stats).
type tableRow struct {
	catalog.Row
	counts usage.UsageCounts
	stats  survey.Stats
	// at is the instant the row's price is read at: the one its table was
	// built at, the same for every row of the table. The zero time is a row
	// with no clock (one a test built by hand), which is priced flat.
	at time.Time
}

// price is the row's three prices in force at the instant its table was
// built: the model's flat prices, or those of the cost.time_prices row
// whose window holds that instant (config.ModelCost.PriceAt). It is what
// the COST column shows and what the cost sort compares.
func (r tableRow) price() config.PriceInForce {
	if r.at.IsZero() {
		return r.Model.Cost.Flat()
	}
	return r.Model.Cost.PriceAt(r.at)
}
```

(c) `tableInput` gains the instant.

Find:

```go
	usage     usage.Store
	stats     map[string]survey.Stats
}
```

Replace with:

```go
	usage     usage.Store
	stats     map[string]survey.Stats
	// now is the instant the rows are priced at. The zero time means the
	// picker's clock (pickerNow), which is what both pickers leave it at.
	now time.Time
}
```

(d) `buildRows` reads the clock once and gives every row that instant.

Find:

```go
	out := make([]tableRow, len(rows))
	ids := make([]string, len(rows))
	for i, r := range rows {
		out[i] = tableRow{Row: r}
		ids[i] = r.Model.ID
	}
```

Replace with:

```go
	// One reading of the clock for the whole table: the rows are sorted by
	// the price in force, and two rows priced on two sides of a window
	// boundary would be compared at different times.
	now := in.now
	if now.IsZero() {
		now = pickerNow()
	}
	out := make([]tableRow, len(rows))
	ids := make([]string, len(rows))
	for i, r := range rows {
		out[i] = tableRow{Row: r, at: now}
		ids[i] = r.Model.ID
	}
```

(e) The comment on `costKey`.

Find:

```go
// costKey is the sort key for "cost ascending": output price per million,
// then input price per million. Local models and subscription-only models
// (no per-token prices) count as $0; a model with no cost data at all sorts
// after every priced model.
```

Replace with:

```go
// costKey is the sort key for "cost ascending": output price per million,
// then input price per million, each the price in force when the table was
// built (tableRow.price), so a model priced by time of day sorts by what it
// costs now. Local models and subscription-only models (no per-token prices)
// count as $0; a model with no cost data at all sorts after every priced
// model.
```

(f) `rowCostKey` compares the price in force. The subscription check still reads the model's own cost table: a subscription is not a time price.

Find:

```go
	c := r.Model.Cost
	if c.InputPricePerMillion == nil && c.CachePricePerMillion == nil && c.OutputPricePerMillion == nil {
		if c.SubscriptionPrice != nil {
			return costKey{}
		}
		return costKey{noData: true}
	}
	var k costKey
	if c.OutputPricePerMillion != nil {
		k.out = *c.OutputPricePerMillion
	}
	if c.InputPricePerMillion != nil {
		k.in = *c.InputPricePerMillion
	}
	return k
```

Replace with:

```go
	p := r.price()
	if p.Input == nil && p.Cache == nil && p.Output == nil {
		if r.Model.Cost.SubscriptionPrice != nil {
			return costKey{}
		}
		return costKey{noData: true}
	}
	var k costKey
	if p.Output != nil {
		k.out = *p.Output
	}
	if p.Input != nil {
		k.in = *p.Input
	}
	return k
```

Four edits to `wt/internal/tui/modeltable.go`.

(g) The mark, the legend and the cell.

Find:

```go
const rowPrefixWidth = 4 // ref column (2) + rotation marker (2), composed by modelItem.Title()
```

Replace with:

```go
const rowPrefixWidth = 4 // ref column (2) + rotation marker (2), composed by modelItem.Title()

// timePricedMark follows the COST cell of a model that has cost.time_prices
// rows: the prices shown are the ones in force when the table was built, and
// they change with the time. costLegend is the COST heading of a table that
// has such a row; it says what the mark means. It is used only when the
// column is already that wide (a marked cell with three prices is one column
// wider), so it never widens the table.
const (
	timePricedMark = "~"
	costLegend     = "COST (~ varies by time)"
)

// costCell is a row's COST cell: the three prices in force for it
// (tableRow.price), marked when the model's price depends on the time.
func costCell(r tableRow) string {
	p := r.price()
	text := formatPerToken(config.ModelCost{InputPricePerMillion: p.Input, CachePricePerMillion: p.Cache, OutputPricePerMillion: p.Output})
	if r.Model.Cost.TimePriced() {
		text += timePricedMark
	}
	return text
}
```

(h) In `renderTable`, before the loop that measures the cells.

Find:

```go
	wS := len("STATUS")
	for i, r := range rows {
```

Replace with:

```go
	wS := len("STATUS")
	timePriced := false
	for i, r := range rows {
```

(i) In that loop, the cost cell.

Find:

```go
		if !r.Discovered {
			cost[i] = formatPerToken(r.Model.Cost)
		}
```

Replace with:

```go
		if !r.Discovered {
			cost[i] = costCell(r)
			timePriced = timePriced || r.Model.Cost.TimePriced()
		}
```

(j) The COST heading.

Find:

```go
	// One layout for the header and every row. It starts with every column
	// shown; whoever sizes the list narrows it (tuilayout.FitTo).
	cols := tuilayout.NewColumns(
		[]string{
			padRunes("FAMILY", famW), padRunes("MODEL", idW), padRunes("LOC", 5), padRunes("STATUS", wS),
			padRunes("RUNNING", 7), padRunes("COST", costW),
```

Replace with:

```go
	costHead := "COST"
	if timePriced && len(costLegend) <= costW {
		costHead = costLegend
	}
	// One layout for the header and every row. It starts with every column
	// shown; whoever sizes the list narrows it (tuilayout.FitTo).
	cols := tuilayout.NewColumns(
		[]string{
			padRunes("FAMILY", famW), padRunes("MODEL", idW), padRunes("LOC", 5), padRunes("STATUS", wS),
			padRunes("RUNNING", 7), padRunes(costHead, costW),
```

Neither picker's constructor changes: `tableFor` (`app.go`) and `newPickModel` (`pick_model.go`) leave `tableInput.now` zero, which is the picker's clock.

- [ ] **Step 4: Run the tests to verify they pass**

Run (from `wt/`): `gofmt -l internal && go vet ./internal/tui && go test -count=1 ./internal/tui -run 'TestSortRowsUsesThePriceInForce|TestBuildRowsReadsThePickersClock|TestRenderTableShowsThePriceInForce|TestModelPickerWithTimePricedModelsFitsTheTerminal|TestAnOpenPickerKeepsThePricesItOpenedWith' -v | grep -E '^(---|ok|FAIL)'`
Expected (`gofmt -l` prints nothing):

```text
--- PASS: TestModelPickerWithTimePricedModelsFitsTheTerminal (0.04s)
--- PASS: TestAnOpenPickerKeepsThePricesItOpenedWith (0.00s)
--- PASS: TestSortRowsUsesThePriceInForce (0.00s)
--- PASS: TestBuildRowsReadsThePickersClock (0.00s)
--- PASS: TestRenderTableShowsThePriceInForce (0.00s)
ok  	github.com/ohanaverse/local-ai-setup/wt/internal/tui	(time)
```

Then the whole package, which holds the existing layout tests (`TestEveryListPhaseFitsTheTerminal`, `TestModelTableUnchangedWhenItFits`, …): `go test -count=1 ./internal/tui`
Expected: `ok` (about 20 seconds). No existing test changes: a table with no time-priced model is drawn exactly as before, and a row built by hand has no clock and is priced flat.

- [ ] **Step 5: Commit**

From the repo root:

```bash
git add wt/internal/tui/modelrows.go wt/internal/tui/modeltable.go wt/internal/tui/modelrows_test.go wt/internal/tui/modeltable_test.go wt/internal/tui/layout_test.go
git commit -m "feat(wt): the model picker shows and sorts by the price in force now (#322)"
```

### Task 5: The mode line names the highlighted model's price (slice C)

On an 80-column terminal a table that holds an OpenRouter-length id has no COST column (fact 34), so Task 4 alone shows the current price only on a wide terminal. This task puts it on the line that already says `LiteLLM: on`, without adding a line.

**The price is on that line whole, or not at all** (decision 26). The footer is clipped at the terminal's edge with no ellipsis, and the first version of this task appended the price and let it be clipped. Seen on a pty at 40 columns (fact 39): `LiteLLM: on   cost 0.0825/0.020625/0` for an output price of 0.33; `LiteLLM: on   cost 0.132/0.033/0.528` with the mark gone, so a time-priced model looked fixed; `LiteLLM: off (direct)   cost 1.32/0.` under direct mode, for every model. So `modeLine` now decides what the line holds from the width it has: the mode and the price when both fit; the price alone when they do not (the mode gives way: it is the same on every row and the price is what the user is choosing by); the mode alone when the price does not fit even by itself, as for a row with no price. And the mark moved in front of the numbers, `cost~ 0.66/0.022/1.98`, so no cut of any kind can leave the numbers without it. At 40 columns the line has 36: `LiteLLM: on   cost~ 1.32/0.044/3.96` is 35 and fits; `cost~ 0.7506/0.0378/2.2509`, the longest price of the three real time-priced models, is 26 and takes the line alone.

**It is the subject of question 1.** If the owner answers "the column only": skip this task except its Step 5 and Step 6's second command, and in Task 6 leave out everything marked *(mode line)* there, which is: the bullet "**The line under the table names the highlighted model's price**" of the new section in `wt-cloud-sync.md` (Step 1); the sentence beginning "The line under the table names" in guide 06 (Step 2); the end of the `tui.md` note, from "The mode line under the launcher's table" (Step 4); the two sentences of the CHANGELOG entry from "The line under the launcher's table" to "as before." (Step 5); the bullet about the line in the PR body (Step 10); and, in Step 9, the check marked *(mode line)* and the shots with LiteLLM off. Step 9 then checks instead that at 80 columns the screen shows no price and at 120 columns the COST column is drawn, marked, under its heading. Task 6's Interfaces then consume nothing from this task.

**Files:**
- Modify: `wt/internal/tui/model_list.go` (`modelItem`), `wt/internal/tui/modeltable.go` (`costNote`; one import; the item), `wt/internal/tui/layout.go` (`modelFrames`)
- Modify (tests): `wt/internal/tui/layout_test.go` (append), `wt/internal/configeditor/models_form_test.go` (append)

**Interfaces:**
- Consumes from Task 4: `tableRow.price()`, `timePricedMark`, `pickerNow`, the fixtures `timedTableRows`, `timedPickerConfig`, `deepseekDear`, `ollamaPeak`. From Task 3: `(config.ModelCost).TimePriced()`.
- Consumes (existing): `modelFrames` in `layout.go`, whose footer is `clip(dimStyle.Render(fmt.Sprintf("%s\n[↑/↓] navigate   [enter] launch or start   [q] quit", mode)), inner)`; `m.models` (a `list.Model` whose items are `*modelItem`); `indexOfID(m model, id string) int`. In `internal/configeditor` tests: `tabRegistry`, `newTabMachine(t, registry)`, `modelsEditor(t, tm, width, height)`, `selectModel(t, m, id)`, `keys(t, m, …)`, `(*tabMachine).text(t)`, `modelFormLabels`, the field indexes `mfInput`, `mfCache`, `mfOutput`, `modelFormEdit`.
- Consumes (existing, tests): `(*config.Config).SetLitellmForTest(config.LitellmState)`, which the fixture's `modelTestConfig` calls with `Enabled: true`; `lipgloss.Width` (already imported by `layout.go`).
- Produces: `modelItem.cost string`; `func costNote(r tableRow) string` (`cost <in>/<cached>/<out>`, or `cost~ …` for a time-priced model); `func modeLine(mode, cost string, width int) string`; the mode line `LiteLLM: on   cost~ <in>/<cached>/<out>`, or the price alone when both do not fit; the test fixtures `tencentCheap` and `modeLinePickerConfig(litellm bool) *config.Config`.

- [ ] **Step 1: Write the failing test**

Append to `wt/internal/tui/layout_test.go` (the imports it needs are there since Task 4):

```go
// tencentCheap is Monday 2026-10-12 17:00 UTC: after 16:00, when the two
// tencent models are at their cheaper level; deepseek is in its cheap window
// and ollama in its peak hours.
var tencentCheap = time.Date(2026, 10, 12, 17, 0, 0, 0, time.UTC)

// modeLinePickerConfig is timedPickerConfig with the two tencent models
// OpenRouter prices by time of day, as `wt cloud-sync` stores them: the
// dearer level flat, the cheaper one a row from 16:00 UTC. Their prices are
// the longest of the real time-priced models', which is what the mode line
// has to fit at 40 columns.
func modeLinePickerConfig(litellm bool) *config.Config {
	cfg := timedPickerConfig()
	evening := []config.CostWindow{{Days: []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}, Start: "16:00", End: "24:00"}}
	for _, m := range []struct {
		name       string
		flat, late [3]float64
	}{
		{"hy3", [3]float64{0.132, 0.033, 0.528}, [3]float64{0.0825, 0.020625, 0.33}},
		{"hy4-preview", [3]float64{0.834, 0.042, 2.501}, [3]float64{0.7506, 0.0378, 2.2509}},
	} {
		cfg.Models = append(cfg.Models, config.Model{ID: "claude/" + m.name, ProviderID: "claude", ModelName: m.name, Family: "hy", Tags: []string{"code"}, Cost: config.ModelCost{
			InputPricePerMillion: f64(m.flat[0]), CachePricePerMillion: f64(m.flat[1]), OutputPricePerMillion: f64(m.flat[2]),
			TimePrices: []config.TimePrice{{Label: "openrouter", Timezone: "UTC", Windows: evening,
				InputPricePerMillion: f64(m.late[0]), CachePricePerMillion: f64(m.late[1]), OutputPricePerMillion: f64(m.late[2])}}}})
	}
	if !litellm {
		cfg.SetLitellmForTest(config.LitellmState{})
	}
	return cfg
}

// TestModelPickerNamesTheHighlightedModelsPrice pins the mode line under the
// table: beside the LiteLLM mode it names the price in force for the
// highlighted model, `cost <input>/<cached>/<output>` per million tokens,
// as `cost~` when that price depends on the time. The COST column is the
// first thing a narrow table gives up after usage and survey, and a table
// with an OpenRouter id in it gives it up at 80 columns, so without this
// line the price a user picks by is on screen only on a wide terminal.
//
// The price is on the line whole or not at all. The footer is clipped at the
// terminal's edge with no sign of the cut, so a price that did not fit used
// to lose its last digits and read as another price (output 0.33 as 0) and
// lose its mark; at 40 columns that was every price under "LiteLLM: off
// (direct)" and two of the three real time-priced models under "LiteLLM:
// on". Now the mode gives way to the price when both do not fit. The line
// follows the cursor, is the mode alone for a row with no per-token price,
// and costs no line.
func TestModelPickerNamesTheHighlightedModelsPrice(t *testing.T) {
	old := pickerNow
	t.Cleanup(func() { pickerNow = old })
	const deepseek, ollama, plain, hy3, hy4 = "claude/deepseek--deepseek-v4-pro-0813", "claude/glm-5.3:cloud", "claude/z-ai--glm-5.2", "claude/hy3", "claude/hy4-preview"
	// fits: the note fits beside "LiteLLM: on" in the 36 columns a 40-column
	// terminal leaves the line.
	type note struct {
		text string
		fits bool
	}
	for _, c := range []struct {
		name string
		at   time.Time
		want map[string]note
	}{
		{"deepseek dear, ollama off-peak, tencent dear", deepseekDear, map[string]note{
			deepseek: {"cost~ 1.32/0.044/3.96", true}, ollama: {"cost~ 0.25/0.025/1", true}, plain: {"cost 0.06/0.059/6", true},
			hy3: {"cost~ 0.132/0.033/0.528", false}, hy4: {"cost~ 0.834/0.042/2.501", false}}},
		{"deepseek cheap, ollama peak, tencent cheap", tencentCheap, map[string]note{
			deepseek: {"cost~ 0.66/0.022/1.98", true}, ollama: {"cost~ 0.5/0.05/2", true}, plain: {"cost 0.06/0.059/6", true},
			hy3: {"cost~ 0.0825/0.020625/0.33", false}, hy4: {"cost~ 0.7506/0.0378/2.2509", false}}},
	} {
		pickerNow = func() time.Time { return c.at }
		for _, litellm := range []bool{true, false} {
			mode := "LiteLLM: off (direct)"
			if litellm {
				mode = "LiteLLM: on"
			}
			for _, size := range [][2]int{{80, 24}, {40, 12}, {120, 50}} {
				width, height := size[0], size[1]
				tempStateDir(t)
				stubUsageStore(t)
				stubRefcountStore(t)
				stubInventory(t, runningOmlxSnapshot())
				m := flowEnter(t, model{cfg: modeLinePickerConfig(litellm), agent: "claude", selectedPath: t.TempDir(), width: width, height: height}, "claude")
				for id, n := range c.want {
					want := mode + "   " + n.text
					if width == 40 && !(litellm && n.fits) {
						want = n.text
					}
					m.models.Select(indexOfID(m, id))
					m = resized(t, m, width, height)
					view := m.View()
					assertFits(t, c.name+", on "+id, view, width, height)
					// Exactly this line and no other with a price on it: the
					// three numbers whole, the mark when the price depends on
					// the time, and nothing after the last number.
					found := false
					for _, line := range strings.Split(view, "\n") {
						switch {
						case strings.TrimSpace(line) == want:
							found = true
						case strings.Contains(line, "cost"):
							t.Errorf("%s, %s at %dx%d, on %s: the line %q names a price and is not %q", c.name, mode, width, height, id, strings.TrimSpace(line), want)
						}
					}
					if !found {
						t.Errorf("%s, %s at %dx%d, on %s: no line reads %q:\n%s", c.name, mode, width, height, id, want, view)
					}
				}
				// A row with no per-token price in force (the fixture's local
				// model, and its native one) leaves the line as it was.
				for _, id := range []string{"omlx/qwen3.8", "claude/opus"} {
					m.models.Select(indexOfID(m, id))
					m = resized(t, m, width, height)
					if view := m.View(); strings.Contains(view, "cost") || !strings.Contains(view, mode) {
						t.Errorf("%s, %s at %dx%d, on %s: the mode line names a price or lacks the mode:\n%s", c.name, mode, width, height, id, view)
					}
				}
			}
		}
	}

	// modeLine, which decides what the line holds: both when both fit, the
	// price alone when it fits alone, and the mode alone otherwise. Never a
	// part of a price.
	for _, c := range []struct {
		mode, cost string
		width      int
		want       string
	}{
		{"LiteLLM: on", "", 36, "LiteLLM: on"},
		{"LiteLLM: on", "cost~ 1.32/0.044/3.96", 36, "LiteLLM: on   cost~ 1.32/0.044/3.96"},
		{"LiteLLM: on", "cost~ 1.32/0.044/3.96", 35, "LiteLLM: on   cost~ 1.32/0.044/3.96"},
		{"LiteLLM: on", "cost~ 1.32/0.044/3.96", 34, "cost~ 1.32/0.044/3.96"},
		{"LiteLLM: on", "cost~ 0.0825/0.020625/0.33", 36, "cost~ 0.0825/0.020625/0.33"},
		{"LiteLLM: off (direct)", "cost 0.06/0.059/6", 36, "cost 0.06/0.059/6"},
		{"LiteLLM: off (direct)", "cost~ 0.66/0.022/1.98", 76, "LiteLLM: off (direct)   cost~ 0.66/0.022/1.98"},
		{"LiteLLM: on", "cost~ 0.000123456/0.000123456/0.000123456", 36, "LiteLLM: on"},
		{"LiteLLM: on", "cost~ 0.000123456/0.000123456/0.000123456", 41, "cost~ 0.000123456/0.000123456/0.000123456"},
	} {
		if got := modeLine(c.mode, c.cost, c.width); got != c.want {
			t.Errorf("modeLine(%q, %q, %d) = %q, want %q", c.mode, c.cost, c.width, got, c.want)
		}
	}

	// costNote, which the price is made from: every price that is missing is
	// a dash, a model with no price at all has no note, and neither has a
	// discovered row.
	row := timedTableRows(ollamaPeak)[0]
	row.Model.Cost.CachePricePerMillion, row.Model.Cost.TimePrices[0].CachePricePerMillion = nil, nil
	if got := costNote(row); got != "cost~ 0.66/-/1.98" {
		t.Errorf("costNote with no cached-input price = %q", got)
	}
	row.Discovered = true
	if got := costNote(row); got != "" {
		t.Errorf("costNote of a discovered row = %q, want none", got)
	}
	if got := costNote(tableRow{Row: catalog.Row{Model: config.Model{ID: "x", Cost: config.ModelCost{SubscriptionPrice: f64(20)}}}}); got != "" {
		t.Errorf("costNote of a subscription-only model = %q, want none", got)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run (from `wt/`): `go test -count=1 ./internal/tui -run TestModelPickerNamesTheHighlightedModelsPrice`
Expected: the package does not build.

```text
# github.com/ohanaverse/local-ai-setup/wt/internal/tui [github.com/ohanaverse/local-ai-setup/wt/internal/tui.test]
internal/tui/layout_test.go:1103:13: undefined: modeLine
internal/tui/layout_test.go:1113:12: undefined: costNote
internal/tui/layout_test.go:1117:12: undefined: costNote
internal/tui/layout_test.go:1120:12: undefined: costNote
FAIL	github.com/ohanaverse/local-ai-setup/wt/internal/tui [build failed]
```

- [ ] **Step 3: Write the implementation**

(a) `wt/internal/tui/model_list.go`: the item carries its note.

Find:

```go
	blocked   string // non-empty: the agent flow's Enter shows this instead of launching or starting
	start     bool   // Enter starts the model through the lifecycle engine
}
```

Replace with:

```go
	blocked   string // non-empty: the agent flow's Enter shows this instead of launching or starting
	start     bool   // Enter starts the model through the lifecycle engine
	// cost is the row's price in force as the launcher's mode line names it
	// for the highlighted row (costNote); "" for a row with no per-token
	// price.
	cost string
}
```

(b) `wt/internal/tui/modeltable.go`, three edits: the import, the note, and the item.

Find:

```go
	"errors"
	"fmt"
	"sync"
```

Replace with:

```go
	"errors"
	"fmt"
	"strconv"
	"sync"
```

Find:

```go
// costCell is a row's COST cell: the three prices in force for it
```

Replace with:

```go
// costNote is a row's price in force as the launcher's mode line names it
// when the row is highlighted: `cost 0.66/0.022/1.98`, input, cached input
// and output per million tokens, a missing one as a dash. When the model's
// price depends on the time the word is `cost~`: the mark comes before the
// numbers, so nothing that shortens the line can leave the numbers without
// it. It is "" for a row that shows no price (a discovered row) or has none
// in force. The numbers are written short (six significant digits, no
// padding) so that the note fits a 40-column terminal (modeLine); the COST
// column keeps its fixed width.
func costNote(r tableRow) string {
	p := r.price()
	if r.Discovered || p.Input == nil && p.Cache == nil && p.Output == nil {
		return ""
	}
	short := func(v *float64) string {
		if v == nil {
			return "-"
		}
		return strconv.FormatFloat(*v, 'g', 6, 64)
	}
	word := "cost"
	if r.Model.Cost.TimePriced() {
		word += timePricedMark
	}
	return word + " " + short(p.Input) + "/" + short(p.Cache) + "/" + short(p.Output)
}

// costCell is a row's COST cell: the three prices in force for it
```

Find:

```go
		it := &modelItem{model: r.Model, line: cols.Line(cells), cells: cells, cols: cols, marked: lastID != "" && r.Model.ID == lastID, ref: refs[r.Model.ID]}
```

Replace with:

```go
		it := &modelItem{model: r.Model, line: cols.Line(cells), cells: cells, cols: cols, marked: lastID != "" && r.Model.ID == lastID, ref: refs[r.Model.ID], cost: costNote(r)}
```

(c) `wt/internal/tui/layout.go`, two edits: `modeLine`, and its use in `modelFrames`.

Find:

```go
func clip(s string, width int) string { return tuilayout.Clip(s, width) }
```

Replace with:

```go
func clip(s string, width int) string { return tuilayout.Clip(s, width) }

// modeLine is the line under the launcher's model table: the LiteLLM mode
// and, after it, the highlighted row's price (costNote; "" for a row with
// none). A price is shown whole or not at all, because the footer is clipped
// at the terminal's edge with no sign of the cut, and a price cut there
// reads as another price (0.33 as 0). So when the two do not fit the width
// together the price takes the line and the mode gives way (at 40 columns:
// always under "LiteLLM: off (direct)", and under "LiteLLM: on" for a price
// with many digits); when the price does not fit even alone, the line is
// the mode, as for a row with no price.
func modeLine(mode, cost string, width int) string {
	switch {
	case cost == "":
		return mode
	case lipgloss.Width(mode)+3+lipgloss.Width(cost) <= width:
		return mode + "   " + cost
	case lipgloss.Width(cost) <= width:
		return cost
	}
	return mode
}
```

Find:

```go
			mode := "LiteLLM: off (direct)"
			if m.cfg != nil && m.cfg.IsLitellm() {
				mode = "LiteLLM: on"
			}
```

Replace with:

```go
			mode := "LiteLLM: off (direct)"
			if m.cfg != nil && m.cfg.IsLitellm() {
				mode = "LiteLLM: on"
			}
			// And the highlighted row's price in force: the COST column is
			// given up on a narrow terminal (a table with an OpenRouter id
			// gives it up at 80 columns), and this line is not. It is on
			// the mode line, not a line of its own, so the footer is as tall
			// as it was and nothing about the fit changes.
			if it, ok := m.models.SelectedItem().(*modelItem); ok {
				mode = modeLine(mode, it.cost, inner)
			}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run (from `wt/`): `gofmt -l internal && go vet ./internal/tui && go test -count=1 ./internal/tui -run 'TestModelPickerNamesTheHighlightedModelsPrice|TestModelPickerWithTimePricedModelsFitsTheTerminal|TestModelPickerFitsTheTerminal|TestEveryListPhaseFitsTheTerminal' -v | grep -E '^(---|ok|FAIL)'`
Expected:

```text
--- PASS: TestModelPickerFitsTheTerminal (0.12s)
--- PASS: TestEveryListPhaseFitsTheTerminal (0.09s)
--- PASS: TestModelPickerWithTimePricedModelsFitsTheTerminal (0.03s)
--- PASS: TestModelPickerNamesTheHighlightedModelsPrice (0.03s)
ok  	github.com/ohanaverse/local-ai-setup/wt/internal/tui	(time)
```

The test draws the picker at 80x24, 40x12 and 120x50, with LiteLLM on and off, at an instant on each side of every model's window, with each of the five priced models highlighted in turn, and fails on any line that holds `cost` and is not the whole expected line. Two mutations were tried and caught: `modeLine` always appending the price (the old behaviour) fails at 40x12 on `claude/hy3` with `the line "LiteLLM: on   cost~ 0.132/0.033/0.52" names a price and is not "cost~ 0.132/0.033/0.528"`; and a row priced from the clock while it is drawn fails `TestAnOpenPickerKeepsThePricesItOpenedWith` with `the clock was read 24 time(s), want 1`.

- [ ] **Step 5: Pin that the model form still edits the flat price**

Nothing in `wt config` changes (decision 27). This test keeps the model form from being "helped" into showing the price in force, which it would then write over the model's own price on save. Append to `wt/internal/configeditor/models_form_test.go` (its imports are already there):

```go
// TestModelFormEditsTheFlatPriceOfATimePricedModel pins what the model form
// shows for a model with cost.time_prices rows: the model's own (flat)
// prices, which are the keys the form edits, and never the price in force
// now. The picker shows the price in force (#322); if the form did too, a
// save made during a cheap window would write that window's price over the
// model's own, which is the price LiteLLM's route carries. The row here is
// in force at every instant, so the test does not depend on the clock.
func TestModelFormEditsTheFlatPriceOfATimePricedModel(t *testing.T) {
	registry := tabRegistry + `
[[models]]
id = "openrouter/timed"
family = "timed"
provider_id = "openrouter"
model_name = "vendor/timed"
tags = []

[models.cost]
input_price_per_million = 1.32
cache_price_per_million = 0.044
output_price_per_million = 3.96

[[models.cost.time_prices]]
label = "openrouter"
timezone = "UTC"
input_price_per_million = 0.66
cache_price_per_million = 0.022
output_price_per_million = 1.98

[[models.cost.time_prices.windows]]
days = [
    "mon",
    "tue",
    "wed",
    "thu",
    "fri",
    "sat",
    "sun",
]
start = "00:00"
end = "24:00"
`
	tm := newTabMachine(t, registry)
	m := keys(t, selectModel(t, modelsEditor(t, tm, 80, 24), "openrouter/timed"), "enter")
	f := m.models.form
	if f == nil || f.mode != modelFormEdit {
		t.Fatalf("enter did not open the edit form:\n%s", m.View())
	}
	for field, want := range map[int]string{mfInput: "1.32", mfCache: "0.044", mfOutput: "3.96"} {
		if got := f.value(field); got != want {
			t.Errorf("%s opens as %q, want the model's own price %q", modelFormLabels[field], got, want)
		}
	}
	m = keys(t, m, "ctrl+s")
	if tm.text(t) != registry || !strings.Contains(m.View(), "no change to openrouter/timed") {
		t.Errorf("saving the untouched form changed the registry or did not say it wrote nothing:\n%s", m.View())
	}
}
```

- [ ] **Step 6: Run it, then the whole of the two packages**

Run (from `wt/`): `gofmt -l internal && go test -count=1 ./internal/configeditor -run TestModelFormEditsTheFlatPriceOfATimePricedModel -v | grep -E '^(---|ok|FAIL)'`
Expected (it passes at once; it pins existing behaviour):

```text
--- PASS: TestModelFormEditsTheFlatPriceOfATimePricedModel (0.00s)
ok  	github.com/ohanaverse/local-ai-setup/wt/internal/configeditor	(time)
```

Run: `go test -count=1 ./internal/tui ./internal/configeditor`
Expected: both `ok`.

- [ ] **Step 7: Commit**

From the repo root:

```bash
git add wt/internal/tui/model_list.go wt/internal/tui/modeltable.go wt/internal/tui/layout.go wt/internal/tui/layout_test.go wt/internal/configeditor/models_form_test.go
git commit -m "feat(wt): the picker's mode line names the highlighted model's price in force (#322)"
```

### Task 6: The docs for the price the picker shows, a look at the real screen, and the PR (slice C)

**Precondition:** slice B is merged, and this branch is rebased onto it (`git fetch origin && git rebase origin/main`): the docs below use the flows' new names (slice A), and the CHANGELOG "find" text is the entry Task 2 wrote. Slice D is **not** needed: this slice lands before it, so that the picker already applies rows on the day the openrouter flow starts to write them. What these docs say about OpenRouter's rows is added by Task 9, with the rows.

**Files:**
- Modify: `wt/docs/wt-cloud-sync.md`, `wt/docs/wt-model.md`, `docs/guides/06-wt-agents-and-models.md`, `wt/docs/internals/local-models.md`, `wt/docs/internals/tui.md`, `wt/docs/internals/config-and-registry.md`, `wt/CLAUDE.md`, `wt/CHANGELOG.md`, `wt/internal/config/config.go` (the comment on `TimePrice`)

**Interfaces:**
- Consumes from Task 2: the first line of its CHANGELOG entry. From Task 3: the names `config.ModelCost.PriceAt`, `ModelCost.TimePriced`, `TimePrice.Problem`, `Model.Malformed`, and the phrase `cost.time_prices[<i>]: <problem>`. From Task 4: `tableRow.price`, `tableRow.at`, `tableInput.now`, `pickerNow`, `rowCostKey`, `costCell`, `costLegend`, `TestModelPickerWithTimePricedModelsFitsTheTerminal`, `TestAnOpenPickerKeepsThePricesItOpenedWith`; the mark `~`; the heading `COST (~ varies by time)`. From Task 5 (the sentences marked *(mode line)*): `costNote`, `modeLine`, `modelItem.cost`, `TestModelPickerNamesTheHighlightedModelsPrice`; the mode line `LiteLLM: on   cost~ <in>/<cached>/<out>`.
- Produces: sentences Task 9 extends when the openrouter flow starts to write rows. Four of Task 9's "find" texts are this task's "replace" texts: the ollama bullet's last sentences and the bullet "This holds for every row" in `wt-cloud-sync.md`, the clause "which `wt cloud-sync` writes for ollama's off-peak hours" in guide 06, the bracket "(ollama's off-peak row, or rows written by hand)" in the CHANGELOG entry, and the comment on `config.TimePrice`.

If a "find" text is not in the file, re-read the paragraph and make the same change to what is there.

- [ ] **Step 1: `wt/docs/wt-cloud-sync.md`, two edits**

(a) The ollama section, the end of the bullet "**Off-peak prices**":

Find:

```markdown
  from the page. Rows with any other label are yours and are kept. Nothing
  applies time prices at launch; they are stored.
```

Replace with:

```markdown
  from the page. Rows with any other label are yours and are kept. The
  model picker applies the row: outside ollama's peak hours it shows the
  off-peak price ([The price the picker
  shows](#the-price-the-picker-shows)). LiteLLM's route carries the peak
  price at every hour.
```

(b) A new section, before "The stale-pricing notice". The bullet "**The line under the table names the highlighted model's price**" is *(mode line)*: leave it out if Task 5 was not built.

Find:

```markdown
## The stale-pricing notice
```

Replace with:

````markdown
## The price the picker shows

`cost.time_prices` rows are what the sync stores; the model picker is what
reads them. For every model, the picker's COST column and its cost sort use
the price **in force at the moment the picker opens**:

- The rows are tried in file order. The first row with a window that holds
  the current instant supplies each price it sets; a price it does not set
  stays the model's own (flat) one. When no row's window holds the instant,
  the model's own price is in force.
- A window is `[start, end)` on the listed days, on the wall clock of the
  row's `timezone`, so a row in a zone with daylight saving time follows
  it. The rows the sync writes are all UTC.
- This holds for every row, whoever wrote it: the ollama flow's `off-peak`
  row (the model's own price is the peak one) and a row you wrote by hand.
- **A price that depends on the time is marked `~`** after its three
  numbers, and the column's heading then reads `COST (~ varies by time)`.
  The mark says the model has a row that applies, at any hour: the price
  beside it is the one in force now and will change.
- **The line under the table names the highlighted model's price** beside
  the LiteLLM mode: `LiteLLM: on   cost~ 0.66/0.022/1.98` (input, cached
  input and output per million tokens; `cost~` when the price depends on
  the time; illustrative). The COST column is not always drawn: a narrow
  table gives up whole columns, and one with an OpenRouter-length id in it
  has no COST column at 80 columns. This line is there when the column is
  not. The price on it is whole or absent, never cut short: on a terminal
  too narrow for the mode and the price together (at 40 columns, most
  prices) the price takes the line and the mode gives way, and a price too
  long even for that leaves the line as the mode alone.
- The price is read once, when the table is built: on opening the picker,
  and when it is rebuilt after a start attempt. A picker left open across a
  window boundary keeps the prices and the order it opened with; reopen it
  for the new ones.
- **Only the picker follows the clock.** LiteLLM's route, and so any spend
  LiteLLM computes from it, uses the model's own price at every hour, and
  `wt model list` shows no price at all.

### A row you write by hand

A row needs a `timezone` (an IANA name such as `America/New_York`; not
empty, not `Local`) and `windows`. The prices it sets (`input_`, `cache_`
and `output_price_per_million`, none below zero) replace the model's own
while one of its windows holds the instant. Each window has `days` (from
`mon` to `sun`) and `start` and `end` as `"HH:MM"`, with `start` before
`end`; `"24:00"` is the end of a day. **A window does not run past
midnight**: 22:00 to 06:00 is two windows (illustrative):

```toml
[[models.cost.time_prices]]
label = "night"
timezone = "America/New_York"
output_price_per_million = 10.0
windows = [
    { days = ["mon", "tue", "wed", "thu", "fri", "sat", "sun"], start = "22:00", end = "24:00" },
    { days = ["mon", "tue", "wed", "thu", "fri", "sat", "sun"], start = "00:00", end = "06:00" },
]
```

A row that breaks one of these rules is still loaded, and it is **not
applied**, whole, at any hour: the picker shows the model's own price, and
no `~` unless another row of the model applies. `wt model list` names the
row and the rule on stderr, `<id>: cost.time_prices[0]: windows[0]: start
must be before end; wt reads it as absent (fix the entry in <registry>)`,
and the Models tab of `wt config` shows the same under the selected model.
wt also refuses to write that model's entry until the row is fixed: `wt
model edit`, a save in `wt config`, or a `wt cloud-sync` with a change for
that model stops with `invalid registry entry: model "<id>": cost:
time_prices[0]: windows[0]: start must be before end`.

A value of the wrong TOML type is another matter. `windows = "always"`, or a
price in quotes, makes the registry unreadable, and every wt command stops
with `wt: config error: parse <registry>: toml: line N (last key
"models.cost.time_prices.windows"): incompatible types: …` until that line
is fixed, as for a wrong type anywhere else in the registry.

## The stale-pricing notice
````

(c) `wt/docs/wt-model.md`, three edits: where `wt model list` and the Models tab describe a malformed row.

Find:

```markdown
`--json` always has everything: `registry` (the file), `models` (each with
```

Replace with:

````markdown
A `cost.time_prices` row that breaks the registry's rules is named the same
way, after the `fetch` and `draft` phrases of its model:

```text
openrouter/mine: cost.time_prices[0]: windows[0]: start must be before end; wt reads it as absent (fix the entry in /Users/you/.config/local-ai/registry.toml)
```

The model picker does not apply such a row
([wt-cloud-sync.md](wt-cloud-sync.md#a-row-you-write-by-hand) has the rules).

`--json` always has everything: `registry` (the file), `models` (each with
````

Find:

```markdown
the row has no path, or reads `missing` when its weights are there.
```

Replace with:

```markdown
the row has no path, or reads `missing` when its weights are there. A
`cost.time_prices` row that breaks a rule is on that line too
(`cost.time_prices[0]: windows[0]: start must be before end; read as
absent`): the model picker does not apply it.
```

Find:

```markdown
  The form adds `(fix the entry in <registry path>)` only for such a row,
  where no field can repair what is wrong.
```

Replace with:

```markdown
  The form adds `(fix the entry in <registry path>)` only for such a row,
  where no field can repair what is wrong. A row with a `cost.time_prices`
  row that breaks a rule is refused the same way (`… cost: time_prices[0]:
  windows[0]: start must be before end`), with the same hint.
```

- [ ] **Step 2: `docs/guides/06-wt-agents-and-models.md`**

Inside the long paragraph that describes the picker's table (the find text is part of one line). The guide names no model and no price, as guides must not. The sentence beginning "The line under the table names" is *(mode line)*.

Find:

```markdown
and a model with no price data sorts last within that group), then by 7-day usage ascending, then by id;
```

Replace with:

```markdown
and a model with no price data sorts last within that group). COST is the price in force when the picker opens: for a model with time-of-day prices (`cost.time_prices` rows, which `wt cloud-sync` writes for ollama's off-peak hours) that is the current window's price, marked `~`, and the sort uses it too, so such a model's place in the list depends on the hour. The line under the table names the highlighted model's price (`LiteLLM: on   cost~ <in>/<cached>/<out>`, the `~` when it depends on the time), whole or not at all: on a terminal too narrow for both, the price takes the line. Ties are then broken by 7-day usage ascending, then by id;
```

- [ ] **Step 3: `wt/docs/internals/local-models.md`, two edits**

Find:

```markdown
- Columns: FAMILY, MODEL, LOC, STATUS, RUNNING, COST, 1D, 7D, 30D, SURVEY. RUNNING reads `run`, `load` (omlx is still loading the model, `runningText`) or `-`.
```

Replace with:

```markdown
- Columns: FAMILY, MODEL, LOC, STATUS, RUNNING, COST, 1D, 7D, 30D, SURVEY. RUNNING reads `run`, `load` (omlx is still loading the model, `runningText`) or `-`. COST is the price in force at the instant the table was built (`tableRow.price`, which is `config.ModelCost.PriceAt` over the model's `cost.time_prices` rows; `costCell`), followed by `~` when the model has such a row that is applied (`ModelCost.TimePriced`; a row the registry's validator refuses is passed over, `TimePrice.Problem`), and the heading is then `COST (~ varies by time)` (`costLegend`, used only when the column is already that wide).
```

Find:

```markdown
- Sort: native models first (#172); then cloud + running local by cost (output then input price; local/subscription-only = $0; no-data last), then 7-day usage;
```

Replace with:

```markdown
- Sort: native models first (#172); then cloud + running local by cost (output then input price, each the price in force when the table was built, so a time-priced model sorts by what it costs now; local/subscription-only = $0; no-data last), then 7-day usage;
```

- [ ] **Step 4: `wt/docs/internals/tui.md`**

A new note above the one about dropped columns (the find text is the start of that note's line). Its end, from "The mode line under the launcher's table", is *(mode line)*.

Find:

```markdown
> **The model table drops whole columns on a narrow terminal** (`modeltable.go`, `tuilayout.Columns`).
```

Replace with:

```markdown
> **The model table prices every row at one instant, read once per build** (`modelrows.go`). `buildRows` reads `tableInput.now`, or the package clock `pickerNow` when it is zero, and gives that instant to every row (`tableRow.at`); `tableRow.price` resolves the model's `cost.time_prices` rows at it (`config.ModelCost.PriceAt`, a pure function: the clock is always an argument). `rowCostKey` (the cost sort, and with it the default selection) and `costCell` (the COST column) both read `tableRow.price`, never `Model.Cost`'s flat fields. Nothing reads a clock while a screen is drawn: a resize or a cursor move redraws the same rows, so the prices and the order of an open picker do not change under the cursor at a window boundary (`TestAnOpenPickerKeepsThePricesItOpenedWith`); a rebuild (`refreshTable`, after a start attempt) reads the clock again. A row built by hand in a test has a zero `at` and is priced flat. To draw a picker at a chosen instant a test sets `tableInput.now`, or replaces `pickerNow` for a whole program (`TestModelPickerWithTimePricedModelsFitsTheTerminal`). The mode line under the launcher's table names the highlighted row's price (`modelItem.cost`, from `costNote`, joined to the `LiteLLM: …` text by `modeLine` in `modelFrames`): it adds no line, so the frames' heights are what they were. The price is on it whole or not at all, because `clip` cuts the footer at the edge with no ellipsis and a price cut there reads as another price: `modeLine` is given the inner width, drops the mode text when the two do not fit together, and drops the price when it does not fit alone (`TestModelPickerNamesTheHighlightedModelsPrice` pins it at 80x24, 40x12 and 120x50, with LiteLLM on and off).
>
> **The model table drops whole columns on a narrow terminal** (`modeltable.go`, `tuilayout.Columns`).
```

- [ ] **Step 5: `wt/CHANGELOG.md`, under `## Unreleased` / `### Changed`**

The two sentences from "The line under the launcher's table" to "as before." are *(mode line)*.

Find:

```markdown
### Changed

- **Breaking:** the provider key `openrouter_priced` is no longer read, and
```

Replace with:

```markdown
### Changed

- The model picker shows the price in force now. For a model with
  `cost.time_prices` rows (ollama's off-peak row, or rows written by hand),
  the COST column shows, and the cost sort uses, the prices of the row
  whose timezone and windows hold the current instant, and the model's own
  price when none does. It used to show the model's own price at every
  hour, which for an ollama cloud model is the peak price. Such a price is
  marked `~`, and the column heading then reads `COST (~ varies by time)`.
  The line under the launcher's table now names the highlighted model's
  price beside the LiteLLM mode, as `cost~ 0.66/0.022/1.98`, whole or not
  at all: on a terminal too narrow for both (40 columns, for most prices)
  the price takes the line and the mode gives way. The COST column itself
  is dropped on a narrow terminal, as before. Because the sort uses the
  current price, a time-priced model's place in the list, and so the first
  row, can differ from one hour to the next. The price is read when the
  picker opens, not while it is open. A row that breaks the registry's
  rules (a window written past midnight, such as 22:00 to 06:00; an unknown
  timezone; a negative price) is not applied, and `wt model list` now names
  it on stderr and in `--json`'s `malformed` array, as it does a malformed
  `fetch`. Not changed: LiteLLM's route, and any spend computed from it,
  uses the model's own price at every hour; `wt model list` and `wt
  config`'s Models tab show no price, and the model form edits the model's
  own price. Reference: `docs/wt-cloud-sync.md` ("The price the picker
  shows").
- **Breaking:** the provider key `openrouter_priced` is no longer read, and
```

- [ ] **Step 6: The comment on `config.TimePrice`, and two notes for maintainers**

(a) In `wt/internal/config/config.go`:

Find:

```go
// per-token prices. The rows are written by `wt cloud-sync`'s ollama flow
// (ollama's off-peak pricing) and applied by nothing: wt decodes them and
// shows the flat prices. What a row means: the first row whose window
// contains an instant wins, per field, falling back to the flat prices.
```

Replace with:

```go
// per-token prices. The rows are written by `wt cloud-sync`'s ollama flow
// (ollama's off-peak pricing); a user may write more by hand.
// ModelCost.PriceAt (price_at.go) applies them, and the model picker shows
// what it returns. A LiteLLM route carries the flat prices. What a row
// means: the first row whose window contains an instant wins, per field,
// falling back to the flat prices. A row the registry's validator refuses
// is not applied (TimePrice.Problem).
```

(b) `wt/docs/internals/config-and-registry.md`, inside the paragraph that begins "**The loader records what it tolerated**" (one long line; the find text is part of it).

Find:

```markdown
`modeladmin.Row.Malformed` carries them, for the Models tab to show.
```

Replace with:

```markdown
After them it names each `cost.time_prices` row the registry's validator refuses, `cost.time_prices[<i>]: <the validator's reason>` (`TimePrice.Problem`, which asks `validateTimePrice` about the typed row): the loader types such a row's values and checks no rule, `ModelCost.PriceAt` passes it over whole, and this is where its writer learns why it changes nothing. (A value of the wrong TOML type in a row is not tolerated: it fails the typed decode and the load, `TestLoadStopsOnATimePricesValueOfTheWrongType`.) `modeladmin.Row.Malformed` carries them, for the Models tab to show.
```

(c) `wt/CLAUDE.md`, the bullet that begins "**A malformed `fetch` or `draft` reads as absent and never fails the load**" (one long line; the find text is its end).

Find:

```markdown
the Models tab says so for the selected row (`Row.Malformed`), and the writer's touched-row check names it.
```

Replace with:

```markdown
the Models tab says so for the selected row (`Row.Malformed`), and the writer's touched-row check names it. `Model.Malformed()` also names a `cost.time_prices` row the validator refuses (`TimePrice.Problem`: a window past midnight, an unknown timezone, a negative price); such a row loads, `ModelCost.PriceAt` never applies it, and a model with no other row is not `TimePriced`. Keep `Problem` asking `validateTimePrice` itself, so the reader and the writer cannot come to disagree about a rule.
```

- [ ] **Step 7: Verify the slice**

Run (from `wt/`): `test -z "$(gofmt -l .)" && go vet ./... && go test -count=1 ./... && make check`
Expected: every package `ok` (25), and `make check` ends without an error.

Run (from the repo root): `make check-links`
Expected: `ALL LINKS OK`.

Run (from the repo root): `make test-all`
Expected: it ends without an error. (Not run while writing this plan.)

Run (from the repo root): `grep -rn "Nothing applies time prices\|applied by nothing" wt docs/guides | grep -v "docs/superpowers"`
Expected: no output (the sentences the resolver made untrue are gone).

- [ ] **Step 8: Commit**

From the repo root:

```bash
git add wt/docs/wt-cloud-sync.md wt/docs/wt-model.md docs/guides/06-wt-agents-and-models.md wt/docs/internals/local-models.md wt/docs/internals/tui.md wt/docs/internals/config-and-registry.md wt/CLAUDE.md wt/CHANGELOG.md wt/internal/config/config.go
git commit -m "docs(wt): the model picker shows the price in force; where, how it is marked, what a hand-written row needs, and what does not follow the clock (#322)"
```

- [ ] **Step 9 (the controller): look at the real screens**

A unit test of a view has passed here before while the real 80-column screen cut the line (#209), so the pickers are looked at on a pty before the PR. Nothing real is read: the registry and the config below are made up, the home is a temp directory, and the "agent" is a stub that is never started (do not press Enter). The registry holds two OpenRouter models with `openrouter` rows written as slice D will write them (this slice applies any row, whoever wrote it), an ollama cloud model with its `off-peak` row, a model with one price, and `openrouter/acme--night-owl`, whose one row is an overnight window that breaks the rule.

Save the pty driver as `/tmp/wt-drive.py`. It answers the two terminal queries a Bubble Tea program waits for, keeps a small screen model, and prints the last screen:

```python
"""Run a command under a pty of a given size, answer the terminal queries a
Bubble Tea program sends, optionally type keys, and print the final screen.

usage: drive.py COLSxROWS [--keys K1,K2,...] [--wait S] -- cmd args...
Keys: literal text, or the names down, up, enter, esc.
"""
import codecs, fcntl, os, pty, re, select, struct, sys, termios, time

KEYS = {"down": b"\x1b[B", "up": b"\x1b[A", "enter": b"\r", "esc": b"\x1b"}


class Screen:
    def __init__(self, cols, rows):
        self.cols, self.rows = cols, rows
        self.cells = [[" "] * cols for _ in range(rows)]
        self.r = self.c = 0
        self.overflow = False
        self.pending = ""

    def put(self, ch):
        if self.c >= self.cols:
            self.overflow = True
            return
        if 0 <= self.r < self.rows:
            self.cells[self.r][self.c] = ch
        self.c += 1

    def feed(self, data):
        # An escape sequence can be cut between two reads: keep an unfinished
        # one for the next call.
        data = self.pending + data
        self.pending = ""
        cut = data.rfind("\x1b")
        if cut >= 0 and not re.match(r"\x1b(\[[0-9;?]*[A-Za-z]|\][^\x07\x1b]*(\x07|\x1b\\)|[^\[\]])", data[cut:]):
            data, self.pending = data[:cut], data[cut:]
        i = 0
        while i < len(data):
            ch = data[i]
            if ch == "\x1b":
                m = re.match(r"\x1b\[([0-9;?]*)([A-Za-z])", data[i:])
                if m:
                    self.csi(m.group(1), m.group(2))
                    i += m.end()
                    continue
                m = re.match(r"\x1b\][^\x07\x1b]*(\x07|\x1b\\)", data[i:])
                if m:
                    i += m.end()
                    continue
                i += 2
                continue
            if ch == "\r":
                self.c = 0
            elif ch == "\n":
                if self.r < self.rows - 1:
                    self.r += 1
                else:
                    self.cells.pop(0)
                    self.cells.append([" "] * self.cols)
            elif ch == "\b":
                self.c = max(0, self.c - 1)
            elif ch >= " ":
                self.put(ch)
            i += 1

    def csi(self, args, cmd):
        nums = [int(x) if x.isdigit() else 0 for x in args.strip("?").split(";")] if args else []
        n = nums[0] if nums and nums[0] else 1
        if cmd == "A":
            self.r = max(0, self.r - n)
        elif cmd == "B":
            self.r = min(self.rows - 1, self.r + n)
        elif cmd == "C":
            self.c = min(self.cols - 1, self.c + n)
        elif cmd == "D":
            self.c = max(0, self.c - n)
        elif cmd == "G":
            self.c = n - 1
        elif cmd in "Hf":
            self.r = (nums[0] or 1) - 1 if nums else 0
            self.c = (nums[1] or 1) - 1 if len(nums) > 1 else 0
        elif cmd == "K":
            mode = nums[0] if nums else 0
            lo, hi = (self.c, self.cols) if mode == 0 else (0, self.c + 1) if mode == 1 else (0, self.cols)
            for x in range(lo, min(hi, self.cols)):
                self.cells[self.r][x] = " "
        elif cmd == "J":
            mode = nums[0] if nums else 0
            if mode == 2:
                self.cells = [[" "] * self.cols for _ in range(self.rows)]
            elif mode == 0:
                for x in range(self.c, self.cols):
                    self.cells[self.r][x] = " "
                for y in range(self.r + 1, self.rows):
                    self.cells[y] = [" "] * self.cols

    def text(self):
        return "\n".join("".join(row).rstrip() for row in self.cells)


def main():
    size, rest = sys.argv[1], sys.argv[2:]
    cols, rows = (int(x) for x in size.split("x"))
    keys, wait = [], 2.0
    while rest and rest[0] != "--":
        if rest[0] == "--keys":
            keys = rest[1].split(",")
        elif rest[0] == "--wait":
            wait = float(rest[1])
        rest = rest[2:]
    cmd = rest[1:]
    pid, fd = pty.fork()
    if pid == 0:
        os.execvp(cmd[0], cmd)
    fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0))
    screen = Screen(cols, rows)
    # One decoder for the whole session: a character of several bytes (the
    # arrows in the key hints) can be cut between two reads, and decoding
    # each read by itself would turn its halves into replacement characters
    # and make a line look wider than the screen.
    decoder = codecs.getincrementaldecoder("utf-8")("replace")

    def pump(seconds):
        end = time.time() + seconds
        while time.time() < end:
            ready, _, _ = select.select([fd], [], [], 0.1)
            if not ready:
                continue
            try:
                data = os.read(fd, 65536)
            except OSError:
                return
            if not data:
                return
            if b"\x1b]11;?" in data:
                os.write(fd, b"\x1b]11;rgb:0000/0000/0000\x1b\\")
            if b"\x1b[6n" in data:
                os.write(fd, b"\x1b[1;1R")
            screen.feed(decoder.decode(data))

    pump(wait)
    for key in keys:
        os.write(fd, KEYS.get(key, key.encode()))
        pump(0.6)
    print(screen.text())
    print(f"--- {cols}x{rows}; a line reached past the right edge: {screen.overflow}")
    try:
        os.write(fd, b"\x03")
        pump(0.5)
        os.kill(pid, 9)
    except OSError:
        pass


main()
```

Then, from `wt/` on the branch:

```bash
P="$(mktemp -d)"
go build -o "$P/wt" ./cmd/wt
mkdir -p "$P/home/.config/local-ai" "$P/home/.config/agent-wt" "$P/fakebin" "$P/repo"
printf '#!/bin/sh\nexit 0\n' > "$P/fakebin/pi" && chmod +x "$P/fakebin/pi"
git -C "$P/repo" init -q && git -C "$P/repo" -c user.email=s@example.invalid -c user.name=scratch commit -q --allow-empty -m init
cat > "$P/home/.config/agent-wt/config.toml" <<'EOF'
default_tag = "code"

[[agents]]
name = "pi"
supported_providers = ["openrouter", "ollama"]

[litellm]
enabled = true
url = "http://127.0.0.1:9"
api_key = "sk-scratch-not-a-key"
EOF
cat > "$P/home/.config/local-ai/registry.toml" <<'EOF'
[[providers]]
id = "openrouter"
name = "OpenRouter"
location = "cloud"
protocols = ["openai-chat"]

[providers.auth]
type = "api_key"
secret_ref = "WT_SCRATCH_NO_SUCH_KEY"
base_url = "https://openrouter.ai/api/v1"

[[providers]]
id = "ollama"
name = "Ollama"
location = "local"
protocols = ["anthropic", "openai-chat"]

[providers.auth]
type = "none"
base_url = "http://127.0.0.1:9"

[[models]]
id = "openrouter/deepseek--deepseek-v4-pro-0813"
family = "deepseek"
provider_id = "openrouter"
model_name = "deepseek/deepseek-v4-pro-0813"
location = "cloud"
source = "curated"
tags = ["code"]

[models.cost]
input_price_per_million = 1.32
cache_price_per_million = 0.044
output_price_per_million = 3.9600000000000004

[[models.cost.time_prices]]
label = "openrouter"
timezone = "UTC"
input_price_per_million = 0.66
cache_price_per_million = 0.022
output_price_per_million = 1.9800000000000002

[[models.cost.time_prices.windows]]
days = ["mon", "tue", "wed", "thu", "fri"]
start = "00:00"
end = "01:00"

[[models.cost.time_prices.windows]]
days = ["mon", "tue", "wed", "thu", "fri"]
start = "04:00"
end = "06:00"

[[models.cost.time_prices.windows]]
days = ["mon", "tue", "wed", "thu", "fri"]
start = "10:00"
end = "24:00"

[[models.cost.time_prices.windows]]
days = ["sat", "sun"]
start = "00:00"
end = "24:00"

[[models]]
id = "openrouter/z-ai--glm-5.2"
family = "glm"
provider_id = "openrouter"
model_name = "z-ai/glm-5.2"
location = "cloud"
source = "curated"
tags = ["code"]

[models.cost]
input_price_per_million = 0.06
cache_price_per_million = 0.059
output_price_per_million = 6.0

[[models]]
id = "ollama/glm-5.3:cloud"
family = "glm"
provider_id = "ollama"
model_name = "glm-5.3:cloud"
location = "cloud"
source = "curated"
tags = ["code"]
catalog_name = "glm-5.3"

[models.cost]
input_price_per_million = 0.5
cache_price_per_million = 0.05
output_price_per_million = 2.0
subscription_price = 100.0
subscription_period = "month"

[[models.cost.time_prices]]
label = "off-peak"
timezone = "UTC"
input_price_per_million = 0.25
cache_price_per_million = 0.025
output_price_per_million = 1.0

[[models.cost.time_prices.windows]]
days = ["mon", "tue", "wed", "thu", "fri"]
start = "00:00"
end = "12:00"

[[models.cost.time_prices.windows]]
days = ["mon", "tue", "wed", "thu", "fri"]
start = "18:00"
end = "24:00"

[[models.cost.time_prices.windows]]
days = ["sat", "sun"]
start = "00:00"
end = "24:00"

[[models]]
id = "openrouter/tencent--hy3"
family = "hy"
provider_id = "openrouter"
model_name = "tencent/hy3"
location = "cloud"
source = "curated"
tags = ["code"]

[models.cost]
input_price_per_million = 0.13199999999999998
cache_price_per_million = 0.033
output_price_per_million = 0.528

[[models.cost.time_prices]]
label = "openrouter"
timezone = "UTC"
input_price_per_million = 0.0825
cache_price_per_million = 0.020625
output_price_per_million = 0.33

[[models.cost.time_prices.windows]]
days = ["mon", "tue", "wed", "thu", "fri", "sat", "sun"]
start = "16:00"
end = "24:00"

[[models]]
id = "openrouter/acme--night-owl"
family = "owl"
provider_id = "openrouter"
model_name = "acme/night-owl"
location = "cloud"
source = "curated"
tags = ["code"]

[models.cost]
input_price_per_million = 12.5
cache_price_per_million = 1.25
output_price_per_million = 75.0

[[models.cost.time_prices]]
label = "night"
timezone = "America/New_York"
output_price_per_million = 10.0

[[models.cost.time_prices.windows]]
days = ["mon", "tue", "wed", "thu", "fri", "sat", "sun"]
start = "22:00"
end = "06:00"

[[providers]]
id = "agy"
name = "Agy"
location = "cloud"

[providers.auth]
type = "native"
EOF
run() { (cd "$P/repo" && env -i PATH="$P/fakebin:/usr/bin:/bin" TERM=xterm-256color HOME="$P/home" XDG_CONFIG_HOME="$P/home/.config" \
  WT_REGISTRY="$P/home/.config/local-ai/registry.toml" WT_LITELLM_CONFIG="$P/litellm.yaml" WT_LITELLM_RESTART_CMD=true "$@"); }
shot() { run python3 -I /tmp/wt-drive.py "$@" -- "$P/wt" --cwd --agent pi; }
run "$P/wt" model list
date -u '+%A %H:%M UTC'
shot 80x24 --wait 5
shot 80x24 --wait 5 --keys down
shot 40x12 --wait 5
shot 40x12 --wait 5 --keys down
shot 120x24 --wait 5
sed -i '' 's/enabled = true$/enabled = false/' "$P/home/.config/agent-wt/config.toml"   # wt has rewritten the file, indented
grep -c 'enabled = false' "$P/home/.config/agent-wt/config.toml"   # 1
run "$P/wt" model list > /dev/null 2>&1
shot 40x12 --wait 5
shot 80x24 --wait 5
rm -rf "$P"
```

**`wt model list` runs first, on purpose, each time the home is new or its `config.toml` was rewritten.** The first wt command in such a home prints a one-time line, `wt: migrated config to native-provider alignment (…)`, which is longer than 80 columns; if a shot is that first command the line lands on the pty before the picker and the driver reports `a line reached past the right edge: True` for a screen that fits. (Seen while this step was written: on every first shot in a fresh home and on the first after `config.toml` was replaced, and on no other.) The listing spends the notice, and it is also the check of the unapplied row. Its expected output:

```text
wt: migrated config to native-provider alignment (renamed google→agy, rewired opencode to ollama-only)
MODEL                                      FAMILY    LOC    STATUS  RUNNING  SIZE  PATH
openrouter/deepseek--deepseek-v4-pro-0813  deepseek  cloud  ok                  -  -
ollama/glm-5.3:cloud                       glm       cloud  ok                  -  -
openrouter/z-ai--glm-5.2                   glm       cloud  ok                  -  -
openrouter/tencent--hy3                    hy        cloud  ok                  -  -
openrouter/acme--night-owl                 owl       cloud  ok                  -  -
openrouter/acme--night-owl: cost.time_prices[0]: windows[0]: start must be before end; wt reads it as absent (fix the entry in <P>/home/.config/local-ai/registry.toml)
```

The prices on the screen depend on the hour the command runs in, which is the point. The price named on the mode line is the highlighted row's: the first row, and after `down` the second.

| When (UTC) | `openrouter/tencent--hy3` | `openrouter/deepseek--…` | `ollama/glm-5.3:cloud` | Order of the first three rows |
|---|---|---|---|---|
| Mon–Fri 01:00–04:00, 06:00–10:00 | `cost~ 0.132/0.033/0.528` | `cost~ 1.32/0.044/3.96` | `cost~ 0.25/0.025/1` | hy3, ollama, deepseek |
| Mon–Fri 12:00–16:00 | `cost~ 0.132/0.033/0.528` | `cost~ 0.66/0.022/1.98` | `cost~ 0.5/0.05/2` | hy3, deepseek, ollama |
| Mon–Fri 16:00–18:00 | `cost~ 0.0825/0.020625/0.33` | `cost~ 0.66/0.022/1.98` | `cost~ 0.5/0.05/2` | hy3, deepseek, ollama |
| any other time before 16:00 | `cost~ 0.132/0.033/0.528` | `cost~ 0.66/0.022/1.98` | `cost~ 0.25/0.025/1` | hy3, ollama, deepseek |
| any other time from 16:00 | `cost~ 0.0825/0.020625/0.33` | `cost~ 0.66/0.022/1.98` | `cost~ 0.25/0.025/1` | hy3, ollama, deepseek |

`openrouter/z-ai--glm-5.2` (`cost 0.06/0.059/6`) and `openrouter/acme--night-owl` (`cost 12.5/1.25/75`, no `~`: its row is not applied) are the fourth and fifth rows at every hour.

Expected at 80x24 on a Monday at 13:00 UTC, as it was captured for this plan (fact 39; the line `loading worktrees...` is a status wt leaves with `--cwd`, not this plan's):

```text

  loading worktrees...

  agent : pi
  tag   : code
      FAMILY    MODEL                                      STATUS  RUNNING

      hy        openrouter/tencent--hy3                    ok      -
      deepseek  openrouter/deepseek--deepseek-v4-pro-0813  ok      -
      glm       ollama/glm-5.3:cloud                       ok      -
      glm       openrouter/z-ai--glm-5.2                   ok      -
      owl       openrouter/acme--night-owl                 ok      -








    ↑/k up • ↓/j down • / filter • q quit • ? more
  LiteLLM: on   cost~ 0.132/0.033/0.528
  [↑/↓] navigate   [enter] launch or start   [q] quit

--- 80x24; a line reached past the right edge: False
```

and at 40x12 on a Monday at 17:00 UTC, with LiteLLM on. `LiteLLM: on` and this price together are 40 columns and the line has 36, so the price has the line to itself; with LiteLLM off the same screen was captured:

```text
  loading worktrees...

      MODEL                         …

      openrouter/tencent--hy3        …
      openrouter/deepseek--deepseek-v…

    •••

    ↑/k up • ↓/j down • / filter …
  cost~ 0.0825/0.020625/0.33
  [↑/↓] navigate   [enter] launch or s
--- 40x12; a line reached past the right edge: False
```

Check, for each shot:

- the last line says `False`;
- *(mode line)* a line names the highlighted row's price, whole: three numbers, with `cost~` for the three time-priced models and `cost` for the other two, as the table gives them for the hour. At 80 and 120 columns it follows the LiteLLM mode. At 40 columns it follows `LiteLLM: on` only when both fit in 36 columns (`LiteLLM: on   cost~ 0.66/0.022/1.98` does; hy3's price does not), and stands alone otherwise, as it does for every model once LiteLLM is off. No shot shows a price cut short;
- at 80 columns there is no COST column, and at 120 there is one, headed `COST (~ varies by time)`, with `~` after the cells of hy3, deepseek and the ollama model and none after `glm-5.2`'s or `night-owl`'s (` 12.5000  1.2500 75.0000`);
- the order of the rows is the one the table gives for the hour.

If a shot shows only `wt: agent "pi" is not installed`, the stub is not on the `PATH` the command was given.

- [ ] **Step 10 (the controller, with the owner's OK): the PR**

```bash
git push -u origin feat/322-picker-price-in-force
gh pr create --repo ohanaverse/local-ai-setup --base main --title "feat(wt): the model picker shows and sorts by the price in force now (#322)" --body-file - <<'EOF'
Refs #322 (the next PR closes it). Third of four PRs (plan: docs/superpowers/plans/2026-10-09-cloud-sync-time-of-day-prices.md).

`cost.time_prices` rows had a writer and no reader. This PR adds the reader and uses it where a model is picked. It lands before the PR that makes `wt cloud-sync` store OpenRouter's schedules as rows, so that the picker applies those rows from the first sync that writes them.

- `config.ModelCost.PriceAt(at)` is the resolver modelman had (`price_at`) and the migration to wt left unported: the first row whose timezone and windows hold the instant wins and supplies each price it sets; a price it does not set, and every price when no row matches, is the model's own. It is pure (the clock is an argument) and follows a row's zone through daylight saving time.
- A row that breaks one of the registry's rules (a window written past midnight, such as 22:00 to 06:00; an unknown timezone; a negative price) is not applied, whole, and its model is not marked. The rule is the registry writer's own validator, asked by the resolver, so the two cannot disagree. `wt model list` names such a row on stderr and in `--json`'s `malformed` array, and the Models tab shows it. A value of the wrong TOML type still fails the registry load, as anywhere else in the file (pinned).
- The picker's COST column shows, and its cost sort uses, the price in force when the table is built. That goes for ollama's `off-peak` rows, for rows written by hand, and for the `openrouter` rows the next PR stores. Such a price is marked `~`, under the heading `COST (~ varies by time)`.
- The line under the launcher's table names the highlighted model's price (`LiteLLM: on   cost~ 0.66/0.022/1.98`), and costs no line. A table with an OpenRouter-length id has no COST column at 80 columns, and none at 40; this line is where the price is then. The price is on it whole or not at all: where the mode and the price do not fit together (40 columns, for most prices) the price takes the line.
- The price is read once per table build, so rows do not move under the cursor at a window boundary (pinned across resizes and cursor moves).
- Not changed: LiteLLM's route carries the model's own price at every hour; `wt model list` and `wt config` show no price; the model form edits the model's own price.

Looked at on a real pty at 80x24, 40x12 and 120x24, with LiteLLM on and off: RESULT.
EOF
```

Before running it, replace `RESULT` with what Step 9 showed and when (for example: "Friday 17:10 UTC: the price whole at every size, alone on its line at 40 columns; hy3 first at its after-16:00 price; night-owl unmarked"). Add the session's PR attribution line at the end of the body if your session is told to add one.

---

### Task 7: Read the schedule, and store its dearest level as the price (slice D)

After this task a time-priced model's flat price no longer follows the clock. No row is written yet; Task 8 adds them, in the same PR.

**Precondition:** slice C is merged (the picker applies rows before this slice writes them; "PR Slices"). Branch: `git switch -c fix/322-cloud-sync-time-of-day-prices main`.

**Files:**
- Create: `wt/internal/cloudsync/testdata/openrouter_models.json`, `wt/internal/cloudsync/timeofday_test.go`, `wt/internal/cloudsync/timeofday.go`
- Modify: `wt/cmd/wt/cloudsync_test.go` (append), `wt/internal/cloudsync/openrouter.go` (imports; `APIPrice`; the end of `ParseOpenRouter`'s loop; `PlanPrices` before the `Skipped` check; the comment on `PlanPrices`)

**Interfaces:**
- Consumes (from Task 2, `wt/internal/cloudsync`): `PlanPrices(entries []Entry, api map[string]APIPrice) *PricePlan`. Existing: `perMillion(v any) (price *float64, skipped bool)`; `samePrice(a, b *float64) bool`; `type APIPrice struct{ Input, Cache, Output *float64; Skipped []string }`; `ParseOpenRouter(body []byte) (map[string]APIPrice, error)`; test helpers `apiOf(t, body string) map[string]APIPrice`, `orEntry(name string, cost *Cost) Entry`, `f(float64) *float64`, `num(*float64) string`, `wantTriple(t, what string, got PriceTriple, in, cache, out *float64)`, `readFile(t, path string) string`, `formatCost(*Cost) string`. In `cmd/wt`: `cloudSyncHome(t, registry string) (string, *config.Config)`, `stubCloudFetch`, `stubRouteSync(t, warning string) *int`, `runCS`, `mustRead`, the `cloudSyncNow` seam, `cloudSyncOpts{openrouter, dryRun, yes}` (Task 1's names).
- Produces:
  - `type Span struct{ Start, End int }` (minutes of a UTC day, `[Start, End)`, 0 to 1440)
  - `type Rate struct{ Input, Cache, Output *float64; Spans [7][]Span }` (index 0 is Monday)
  - `func parseSchedule(pricing map[string]any) (rates []Rate, unusable string)` (dearest first; `nil, ""` when the model has no time-of-day entry)
  - `func isPriceKey(pricing map[string]any, key string) bool`
  - `func mainRate(rates []Rate) int`
  - `APIPrice.Rates []Rate` (the levels other than the one stored as the price, dearest first; nil for every other model) and `APIPrice.Unusable string`
  - the warning `Could not use OpenRouter's time-of-day pricing for <id>: <reason>`
  - test helpers used by Task 8: `fixtureFetched time.Time`, `fetchedAt(t *testing.T, at time.Time) string`, `headline(t *testing.T, body, id string) string`, `pm(perToken string) *float64`, `one(pricing string) string`, `sameRates(a, b []Rate) bool`, `ratesText([]Rate) string`; in `cmd/wt`, the constant `timeOfDayRegistry` and `TestCloudSyncTimeOfDayPricesDoNotFollowTheClock`, which Task 8 edits

- [ ] **Step 1: Save the fixture**

Create `wt/internal/cloudsync/testdata/openrouter_models.json` with exactly this content. It is five models of `https://openrouter.ai/api/v1/models` as fetched on 2026-10-09 at 14:06:14 UTC, reduced to `id` and `pricing`; the values are OpenRouter's own (a public price list). Do not fetch it again: the tests pin these numbers.

```json
{"data": [
  {"id": "deepseek/deepseek-v4-pro-0813", "pricing": {"prompt": "0.00000066", "completion": "0.00000198", "input_cache_read": "0.000000022", "overrides": [
    {"utc_days": ["saturday", "sunday"], "prompt": "0.00000066", "completion": "0.00000198", "input_cache_read": "0.000000022"},
    {"utc_days": ["monday", "tuesday", "wednesday", "thursday", "friday"], "utc_start": 0, "utc_end": 100, "prompt": "0.00000066", "completion": "0.00000198", "input_cache_read": "0.000000022"},
    {"utc_days": ["monday", "tuesday", "wednesday", "thursday", "friday"], "utc_start": 100, "utc_end": 400, "prompt": "0.00000132", "completion": "0.00000396", "input_cache_read": "0.000000044"},
    {"utc_days": ["monday", "tuesday", "wednesday", "thursday", "friday"], "utc_start": 400, "utc_end": 600, "prompt": "0.00000066", "completion": "0.00000198", "input_cache_read": "0.000000022"},
    {"utc_days": ["monday", "tuesday", "wednesday", "thursday", "friday"], "utc_start": 600, "utc_end": 1000, "prompt": "0.00000132", "completion": "0.00000396", "input_cache_read": "0.000000044"},
    {"utc_days": ["monday", "tuesday", "wednesday", "thursday", "friday"], "utc_start": 1000, "utc_end": 0, "prompt": "0.00000066", "completion": "0.00000198", "input_cache_read": "0.000000022"}
  ]}},
  {"id": "tencent/hy3", "pricing": {"prompt": "0.000000132", "completion": "0.000000528", "input_cache_read": "0.000000033", "overrides": [
    {"utc_start": 0, "utc_end": 1600, "prompt": "0.000000132", "completion": "0.000000528", "input_cache_read": "0.000000033"},
    {"utc_start": 1600, "utc_end": 0, "prompt": "0.0000000825", "completion": "0.00000033", "input_cache_read": "0.000000020625"}
  ]}},
  {"id": "tencent/hy4-preview", "pricing": {"prompt": "0.000000834", "completion": "0.000002501", "input_cache_read": "0.000000042", "overrides": [
    {"utc_start": 0, "utc_end": 1600, "prompt": "0.000000834", "completion": "0.000002501", "input_cache_read": "0.000000042"},
    {"utc_start": 1600, "utc_end": 0, "prompt": "0.0000007506", "completion": "0.0000022509", "input_cache_read": "0.0000000378"}
  ]}},
  {"id": "anthropic/claude-haiku-5.5", "pricing": {"prompt": "0.0000001", "completion": "0.0000005", "web_search": "0.01", "input_cache_read": "0.00000001", "input_cache_write": "0.000000125", "input_cache_write_1h": "0.0000002", "overrides": [
    {"min_prompt_tokens": 100000, "prompt": "0.0000005", "completion": "0.0000025", "input_cache_read": "0.00000005", "input_cache_write": "0.000000625", "input_cache_write_1h": "0.000001"}
  ]}},
  {"id": "z-ai/glm-5.2", "pricing": {"prompt": "0.00000006", "completion": "0.000006", "input_cache_read": "0.000000059"}}
]}
```

- [ ] **Step 2: Write the failing command test, and see the bug**

Append to `wt/cmd/wt/cloudsync_test.go` (every import it needs is already in the file):

```go
// timeOfDayRegistry holds two models OpenRouter prices by time of day, as a
// sync before #322 was fixed left them: deepseek at the price of the window
// that sync ran in (the cheap one), hy3 not yet priced.
const timeOfDayRegistry = `[[providers]]
id = "openrouter"
name = "OpenRouter"
location = "cloud"

[providers.auth]
type = "api_key"
secret_ref = "WT_TEST_OPENROUTER_KEY"

[[models]]
id = "openrouter/deepseek--deepseek-v4-pro-0813"
family = "deepseek"
provider_id = "openrouter"
model_name = "deepseek/deepseek-v4-pro-0813"
location = "cloud"
source = "curated"
tags = []

[models.cost]
input_price_per_million = 0.66
cache_price_per_million = 0.022
output_price_per_million = 1.9800000000000002

[[models]]
id = "openrouter/tencent--hy3"
family = "hy"
provider_id = "openrouter"
model_name = "tencent/hy3"
location = "cloud"
source = "curated"
tags = []
`

// TestCloudSyncTimeOfDayPricesDoNotFollowTheClock is #322 end to end, on a
// saved OpenRouter response and a fixed clock. The same registry is planned
// from the list as OpenRouter serves it in deepseek's cheap window and in its
// dear one, at two clock times: the two dry runs print the same bytes. After
// one apply, a run on the other window's list finds no price to update,
// stamps the models and does not sync the routes — before the fix it
// reported an update each time the clock had crossed a window, and each one
// rewrote LiteLLM's config and could restart the proxy under running agents.
func TestCloudSyncTimeOfDayPricesDoNotFollowTheClock(t *testing.T) {
	// The fixture was fetched on a Friday at 14:06 UTC, in deepseek's cheap
	// window. In its dear window OpenRouter serves the same list with that
	// model's three top-level prices doubled, and nothing else different.
	raw, err := os.ReadFile("../../internal/cloudsync/testdata/openrouter_models.json")
	if err != nil {
		t.Fatal(err)
	}
	cheap := string(raw)
	const cheapTop = `"id": "deepseek/deepseek-v4-pro-0813", "pricing": {"prompt": "0.00000066", "completion": "0.00000198", "input_cache_read": "0.000000022",`
	const dearTop = `"id": "deepseek/deepseek-v4-pro-0813", "pricing": {"prompt": "0.00000132", "completion": "0.00000396", "input_cache_read": "0.000000044",`
	if strings.Count(cheap, cheapTop) != 1 {
		t.Fatal("the fixture does not hold deepseek's top-level prices as this test expects them")
	}
	dear := strings.Replace(cheap, cheapTop, dearTop, 1)

	path, _ := cloudSyncHome(t, timeOfDayRegistry)
	synced := stubRouteSync(t, "")
	run := func(body string, now time.Time, o cloudSyncOpts) string {
		t.Helper()
		stubCloudFetch(t, map[string]string{cloudsync.OpenRouterModelsURL: body})
		cloudSyncNow = func() time.Time { return now }
		cfg, err := config.Load()
		if err != nil {
			t.Fatalf("config.Load: %v", err)
		}
		o.openrouter = true
		stdout, stderr, code := runCS(t, cfg, o)
		if stderr != "" || code != 0 {
			t.Fatalf("stdout = %q\nstderr = %q, exit %d", stdout, stderr, code)
		}
		return stdout
	}
	inCheapWindow := time.Date(2026, 10, 9, 14, 6, 0, 0, time.UTC)
	inDearWindow := time.Date(2026, 10, 12, 2, 45, 0, 0, time.UTC)

	const plan = "openrouter: openrouter.ai: 2 OpenRouter-priced models in the registry (prices are input/cached/output per million tokens)\n" +
		"openrouter: Price updates (2):\n" +
		"openrouter:   openrouter/deepseek--deepseek-v4-pro-0813: 0.66/0.022/1.98 -> 1.32/0.044/3.96\n" +
		"openrouter:   openrouter/tencent--hy3: no cost -> 0.132/0.033/0.528\n" +
		"openrouter: Unchanged prices: 0\n"
	if got := run(cheap, inCheapWindow, cloudSyncOpts{dryRun: true}); got != plan {
		t.Errorf("dry run in the cheap window:\n%s\nwant:\n%s", got, plan)
	}
	if got := run(dear, inDearWindow, cloudSyncOpts{dryRun: true}); got != plan {
		t.Errorf("dry run in the dear window:\n%s\nwant the same plan:\n%s", got, plan)
	}
	if mustRead(t, path) != timeOfDayRegistry || *synced != 0 {
		t.Fatal("a dry run changed the registry or synced the routes")
	}

	if got := run(cheap, inCheapWindow, cloudSyncOpts{yes: true}); !strings.HasSuffix(got, "openrouter: refreshed 2 model(s); 2 price(s) changed\n") || *synced != 1 {
		t.Fatalf("the apply printed %q and synced %d time(s), want 2 prices changed and one sync", got, *synced)
	}
	applied := mustRead(t, path)
	for _, want := range []string{
		"input_price_per_million = 1.32\ncache_price_per_million = 0.044\noutput_price_per_million = 3.9600000000000004\n\n[[models]]\n",
		"pricing_updated_at = \"2026-10-09T14:06:00+00:00\"",
	} {
		if !strings.Contains(applied, want) {
			t.Errorf("registry.toml after the apply lacks:\n%s\n\nfile:\n%s", want, applied)
		}
	}

	const settled = "openrouter: openrouter.ai: 2 OpenRouter-priced models in the registry (prices are input/cached/output per million tokens)\n" +
		"openrouter: Price updates (0):\n" +
		"openrouter: Unchanged prices: 2\n"
	if got := run(dear, inDearWindow, cloudSyncOpts{dryRun: true}); got != settled {
		t.Errorf("dry run in the dear window after the apply:\n%s\nwant:\n%s", got, settled)
	}
	if got := run(dear, inDearWindow, cloudSyncOpts{yes: true}); got != settled+"openrouter: refreshed 2 model(s); 0 price(s) changed\n" {
		t.Errorf("apply in the dear window after the first apply:\n%s", got)
	}
	if *synced != 1 {
		t.Errorf("the routes were synced %d time(s) in all, want once: the second apply changed no price", *synced)
	}
	if got, want := mustRead(t, path), strings.ReplaceAll(applied, "2026-10-09T14:06:00+00:00", "2026-10-12T02:45:00+00:00"); got != want {
		t.Errorf("registry.toml after the second apply:\n%s\nwant the first apply's file with the new stamp and nothing else:\n%s", got, want)
	}
}
```

Run (from `wt/`): `go test -count=1 ./cmd/wt -run TestCloudSyncTimeOfDayPricesDoNotFollowTheClock 2>&1 | grep -v '^wt: migrated'`
Expected: FAIL. This is the bug: with the list as served in deepseek's cheap window, the model's stored cheap price is "unchanged".

```text
--- FAIL: TestCloudSyncTimeOfDayPricesDoNotFollowTheClock (0.01s)
    cloudsync_test.go:1000: dry run in the cheap window:
        openrouter: openrouter.ai: 2 OpenRouter-priced models in the registry (prices are input/cached/output per million tokens)
        openrouter: Price updates (1):
        openrouter:   openrouter/tencent--hy3: no cost -> 0.132/0.033/0.528
        openrouter: Unchanged prices: 1
        
        want:
        openrouter: openrouter.ai: 2 OpenRouter-priced models in the registry (prices are input/cached/output per million tokens)
        openrouter: Price updates (2):
        openrouter:   openrouter/deepseek--deepseek-v4-pro-0813: 0.66/0.022/1.98 -> 1.32/0.044/3.96
        openrouter:   openrouter/tencent--hy3: no cost -> 0.132/0.033/0.528
        openrouter: Unchanged prices: 0
    cloudsync_test.go:1010: the apply printed "openrouter: openrouter.ai: 2 OpenRouter-priced models in the registry (…)\nopenrouter: Price updates (1):\n…openrouter: refreshed 2 model(s); 1 price(s) changed\n" and synced 1 time(s), want 2 prices changed and one sync
FAIL
```

- [ ] **Step 3: Write the failing package tests**

Create `wt/internal/cloudsync/timeofday_test.go`:

```go
package cloudsync

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fixtureFetched is when testdata/openrouter_models.json was fetched from
// https://openrouter.ai/api/v1/models: a Friday afternoon, UTC. The file is
// five models of that list, cut down to their id and pricing: three priced
// by time of day (deepseek/deepseek-v4-pro-0813, tencent/hy3,
// tencent/hy4-preview), one with a prompt-size tier
// (anthropic/claude-haiku-5.5) and one with neither (z-ai/glm-5.2).
var fixtureFetched = time.Date(2026, 10, 9, 14, 6, 14, 0, time.UTC)

// fetchedAt is the fixture as OpenRouter serves it at another instant: for a
// model priced by time of day the top-level prompt, completion and
// input_cache_read are those of the overrides entry in force at that
// instant, and nothing else differs. The rule is written here from
// OpenRouter's Models guide and not from the code under test: an entry
// applies on its utc_days (every day when absent) inside [utc_start,
// utc_end), HHMM numbers compared as numbers, wrapping when the end is not
// after the start; the last entry that applies wins.
func fetchedAt(t *testing.T, at time.Time) string {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(readFile(t, "testdata/openrouter_models.json")))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		t.Fatal(err)
	}
	at = at.UTC()
	now := at.Hour()*100 + at.Minute()
	today := strings.ToLower(at.Weekday().String())
	for _, item := range doc["data"].([]any) {
		pricing := item.(map[string]any)["pricing"].(map[string]any)
		overrides, _ := pricing["overrides"].([]any)
		for _, raw := range overrides {
			entry := raw.(map[string]any)
			days, hasDays := entry["utc_days"].([]any)
			start, hasWindow := entry["utc_start"].(json.Number)
			if !hasDays && !hasWindow {
				continue
			}
			if hasDays && !slices.Contains(days, any(today)) {
				continue
			}
			if hasWindow {
				from, _ := start.Int64()
				to, _ := entry["utc_end"].(json.Number).Int64()
				inside := now >= int(from) && now < int(to)
				if to <= from {
					inside = now >= int(from) || now < int(to)
				}
				if !inside {
					continue
				}
			}
			for _, key := range []string{"prompt", "completion", "input_cache_read"} {
				pricing[key] = entry[key]
			}
		}
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

// headline is a model's top-level prompt/completion/input_cache_read in
// body, as OpenRouter wrote them.
func headline(t *testing.T, body, id string) string {
	t.Helper()
	var doc struct {
		Data []struct {
			ID      string
			Pricing struct {
				Prompt, Completion string
				Cache              string `json:"input_cache_read"`
			}
		}
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	for _, m := range doc.Data {
		if m.ID == id {
			return m.Pricing.Prompt + "/" + m.Pricing.Cache + "/" + m.Pricing.Completion
		}
	}
	t.Fatalf("no model %s in the body", id)
	return ""
}

// TestFetchedAtIsWhatOpenRouterSent holds the test helper to what was seen
// of the real service: the fixture itself (fetched Friday 14:06 UTC) and the
// two runs in issue #322 (deepseek at the lower rate at 00:28 UTC, at double
// at 02:45 UTC). Only the 02:45 reading tells "the window in force" from
// "the level that holds most of the week" or "the first entry": at the other
// instants all three rules give the same price. One more reading does, and
// it is why the 16:00 case below is the rule and not what the service sent
// at that minute: the same day both tencent models were still listed at
// their before-16:00 level at 16:03 UTC and at their after-16:00 level at
// 16:09, with the same overrides throughout. The list is cached, so its
// top-level price trails the window by some minutes. The rest of the helper
// is OpenRouter's documented rule, not an observation. Every test below that
// says "at another time of day" rests on this helper, so a helper that
// modelled the windows wrongly would make those tests prove nothing about
// the service; the code under test does not read the top-level price of a
// time-priced model at all, which is what keeps it right even if the rule
// is not exact.
func TestFetchedAtIsWhatOpenRouterSent(t *testing.T) {
	const deepseek, hy3 = "deepseek/deepseek-v4-pro-0813", "tencent/hy3"
	fixture := readFile(t, "testdata/openrouter_models.json")
	same := fetchedAt(t, fixtureFetched)
	for _, id := range []string{deepseek, hy3, "tencent/hy4-preview", "anthropic/claude-haiku-5.5", "z-ai/glm-5.2"} {
		if got, want := headline(t, same, id), headline(t, fixture, id); got != want {
			t.Errorf("%s at the fixture's own instant = %s, want the fixture's %s", id, got, want)
		}
	}
	for _, c := range []struct {
		at       time.Time
		id, want string
	}{
		{time.Date(2026, 10, 9, 0, 28, 0, 0, time.UTC), deepseek, "0.00000066/0.000000022/0.00000198"},
		{time.Date(2026, 10, 9, 2, 45, 0, 0, time.UTC), deepseek, "0.00000132/0.000000044/0.00000396"},
		// The half-open window: 03:59 is inside 01:00 to 04:00, 04:00 is not.
		{time.Date(2026, 10, 9, 3, 59, 0, 0, time.UTC), deepseek, "0.00000132/0.000000044/0.00000396"},
		{time.Date(2026, 10, 9, 4, 0, 0, 0, time.UTC), deepseek, "0.00000066/0.000000022/0.00000198"},
		// A Saturday at an hour that is dear on a weekday.
		{time.Date(2026, 10, 10, 2, 45, 0, 0, time.UTC), deepseek, "0.00000066/0.000000022/0.00000198"},
		{time.Date(2026, 10, 9, 15, 59, 0, 0, time.UTC), hy3, "0.000000132/0.000000033/0.000000528"},
		{time.Date(2026, 10, 9, 16, 0, 0, 0, time.UTC), hy3, "0.0000000825/0.000000020625/0.00000033"},
		// A clock in another zone is read as its UTC instant: 17:00 PDT is
		// 00:00 UTC the next day.
		{time.Date(2026, 10, 9, 17, 0, 0, 0, time.FixedZone("PDT", -7*3600)), hy3, "0.000000132/0.000000033/0.000000528"},
	} {
		if got := headline(t, fetchedAt(t, c.at), c.id); got != c.want {
			t.Errorf("%s at %s = %s, want %s", c.id, c.at.Format(time.RFC3339), got, c.want)
		}
	}
}

// TestParseOpenRouterTimeOfDay pins what is read for a model OpenRouter
// prices by time of day (#322), on a saved response. Its price is the
// dearest level of its schedule and not the top-level one, which is only the
// level in force when the list was fetched: here deepseek and both tencent
// models at their peak rate although the fixture was fetched in deepseek's
// cheap window. The other levels come back with the times of the UTC week
// they apply. A model with a prompt-size tier (min_prompt_tokens) is not a
// time-of-day model: its top-level price is its price, and the tier, which
// is 5 times dearer here, is not read.
func TestParseOpenRouterTimeOfDay(t *testing.T) {
	api := apiOf(t, readFile(t, "testdata/openrouter_models.json"))
	weekdays := []Span{{0, 60}, {240, 360}, {600, 1440}}
	evening := []Span{{960, 1440}}
	for id, want := range map[string]struct {
		in, cache, out string
		rates          []Rate
	}{
		"deepseek/deepseek-v4-pro-0813": {"0.00000132", "0.000000044", "0.00000396", []Rate{{
			Input: pm("0.00000066"), Cache: pm("0.000000022"), Output: pm("0.00000198"),
			Spans: [7][]Span{weekdays, weekdays, weekdays, weekdays, weekdays, {{0, 1440}}, {{0, 1440}}},
		}}},
		"tencent/hy3": {"0.000000132", "0.000000033", "0.000000528", []Rate{{
			Input: pm("0.0000000825"), Cache: pm("0.000000020625"), Output: pm("0.00000033"),
			Spans: [7][]Span{evening, evening, evening, evening, evening, evening, evening},
		}}},
		"tencent/hy4-preview": {"0.000000834", "0.000000042", "0.000002501", []Rate{{
			Input: pm("0.0000007506"), Cache: pm("0.0000000378"), Output: pm("0.0000022509"),
			Spans: [7][]Span{evening, evening, evening, evening, evening, evening, evening},
		}}},
		"anthropic/claude-haiku-5.5": {"0.0000001", "0.00000001", "0.0000005", nil},
		"z-ai/glm-5.2":               {"0.00000006", "0.000000059", "0.000006", nil},
	} {
		got := api[id]
		wantTriple(t, id, PriceTriple{Input: got.Input, Cache: got.Cache, Output: got.Output}, pm(want.in), pm(want.cache), pm(want.out))
		if got.Unusable != "" || got.Skipped != nil {
			t.Errorf("%s: unusable %q, skipped %v; want neither", id, got.Unusable, got.Skipped)
		}
		if !sameRates(got.Rates, want.rates) {
			t.Errorf("%s rates = %s\nwant %s", id, ratesText(got.Rates), ratesText(want.rates))
		}
	}
}

func sameRates(a, b []Rate) bool {
	return slices.EqualFunc(a, b, func(x, y Rate) bool {
		return samePrice(x.Input, y.Input) && samePrice(x.Cache, y.Cache) && samePrice(x.Output, y.Output) && reflect.DeepEqual(x.Spans, y.Spans)
	})
}

func ratesText(rates []Rate) string {
	var b strings.Builder
	for _, r := range rates {
		b.WriteString(num(r.Input) + "/" + num(r.Cache) + "/" + num(r.Output))
		for d, spans := range r.Spans {
			b.WriteString(" " + openRouterDays[d][:3] + ":")
			for _, s := range spans {
				b.WriteString(" " + strconv.Itoa(s.Start) + "-" + strconv.Itoa(s.End))
			}
		}
		b.WriteString("; ")
	}
	return b.String()
}

// pm is a per-token price as OpenRouter writes it, converted as the parser
// converts it: one float64 multiplication. (`0.00000396 * 1e6` written as a
// Go constant is exact arithmetic and gives 3.96; the conversion gives
// 3.9600000000000004, and that is what is stored.)
func pm(perToken string) *float64 {
	v, err := strconv.ParseFloat(perToken, 64)
	if err != nil {
		panic(err)
	}
	v *= 1_000_000
	return &v
}

// one is a response with one model, vendor/m, whose pricing object is the
// given JSON.
func one(pricing string) string { return `{"data": [{"id": "vendor/m", "pricing": ` + pricing + `}]}` }

// TestParseOpenRouterTimeOfDayShapes pins the override grammar on shapes the
// fixture does not hold, each a pricing object OpenRouter's Models guide
// describes or allows. The ones that matter most to a user: a window that
// wraps past midnight is read whole (half of it would leave part of the
// week unpriced), and entries wt cannot read with certainty are never
// guessed at.
func TestParseOpenRouterTimeOfDayShapes(t *testing.T) {
	// The guide's own example: half price from 16:30 to 00:30 UTC.
	wrap := apiOf(t, one(`{"prompt": "0.00000014", "completion": "0.00000021", "overrides": [
		{"utc_start": 30, "utc_end": 1630, "prompt": "0.00000028", "completion": "0.00000042"},
		{"utc_start": 1630, "utc_end": 30, "prompt": "0.00000014", "completion": "0.00000021"}
	]}`))["vendor/m"]
	night := []Span{{0, 30}, {990, 1440}}
	wantTriple(t, "wrap", PriceTriple{Input: wrap.Input, Cache: wrap.Cache, Output: wrap.Output}, pm("0.00000028"), nil, pm("0.00000042"))
	if want := []Rate{{Input: pm("0.00000014"), Output: pm("0.00000021"), Spans: [7][]Span{night, night, night, night, night, night, night}}}; !sameRates(wrap.Rates, want) {
		t.Errorf("wrap rates = %s\nwant %s", ratesText(wrap.Rates), ratesText(want))
	}

	// The guide's weekly example: the wrapping window is tested against the
	// day of the instant, so Monday 00:00 to 00:30 belongs to Monday's entry
	// and Saturday 00:00 to 00:30 to the weekend's.
	weekly := apiOf(t, one(`{"prompt": "0.00000028", "completion": "0.00000042", "input_cache_read": "0.00000007", "overrides": [
		{"utc_days": ["monday", "tuesday", "wednesday", "thursday", "friday"], "utc_start": 30, "utc_end": 1630, "prompt": "0.00000056", "completion": "0.00000084"},
		{"utc_days": ["monday", "tuesday", "wednesday", "thursday", "friday"], "utc_start": 1630, "utc_end": 30, "prompt": "0.00000028", "completion": "0.00000042"},
		{"utc_days": ["saturday", "sunday"], "prompt": "0.00000028", "completion": "0.00000042"}
	]}`))["vendor/m"]
	// No window has a cached-input price, so that price does not depend on
	// the hour and the top-level one is kept.
	wantTriple(t, "weekly", PriceTriple{Input: weekly.Input, Cache: weekly.Cache, Output: weekly.Output}, pm("0.00000056"), pm("0.00000007"), pm("0.00000084"))
	if want := []Rate{{Input: pm("0.00000028"), Output: pm("0.00000042"), Spans: [7][]Span{night, night, night, night, night, {{0, 1440}}, {{0, 1440}}}}}; !sameRates(weekly.Rates, want) {
		t.Errorf("weekly rates = %s\nwant %s", ratesText(weekly.Rates), ratesText(want))
	}

	// The same schedule with its entries in another order is the same
	// schedule: nothing may be written because OpenRouter reordered a list.
	reordered := apiOf(t, one(`{"prompt": "0.00000028", "completion": "0.00000042", "input_cache_read": "0.00000007", "overrides": [
		{"utc_days": ["sunday", "saturday"], "prompt": "0.00000028", "completion": "0.00000042"},
		{"utc_days": ["friday", "monday", "tuesday", "wednesday", "thursday"], "utc_start": 1630, "utc_end": 30, "prompt": "0.00000028", "completion": "0.00000042"},
		{"utc_days": ["monday", "tuesday", "wednesday", "thursday", "friday"], "utc_start": 30, "utc_end": 1630, "prompt": "0.00000056", "completion": "0.00000084"}
	]}`))["vendor/m"]
	if !sameRates(reordered.Rates, weekly.Rates) || !samePrice(reordered.Input, weekly.Input) {
		t.Errorf("reordered rates = %s\nwant %s", ratesText(reordered.Rates), ratesText(weekly.Rates))
	}

	// Three levels: the dearest is the price, the others follow dearest
	// first. Where entries overlap the later one wins, as the guide says.
	three := apiOf(t, one(`{"prompt": "0.000002", "completion": "0.000004", "overrides": [
		{"utc_start": 0, "utc_end": 0, "prompt": "0.000001", "completion": "0.000002"},
		{"utc_start": 800, "utc_end": 2000, "prompt": "0.000003", "completion": "0.000006"},
		{"utc_start": 1200, "utc_end": 1400, "prompt": "0.000002", "completion": "0.000004"}
	]}`))["vendor/m"]
	all := func(spans ...Span) [7][]Span { return [7][]Span{spans, spans, spans, spans, spans, spans, spans} }
	wantTriple(t, "three", PriceTriple{Input: three.Input, Cache: three.Cache, Output: three.Output}, f(3), nil, f(6))
	if want := []Rate{
		{Input: f(2), Output: f(4), Spans: all(Span{720, 840})},
		{Input: f(1), Output: f(2), Spans: all(Span{0, 480}, Span{1200, 1440})},
	}; !sameRates(three.Rates, want) {
		t.Errorf("three rates = %s\nwant %s", ratesText(three.Rates), ratesText(want))
	}

	// Levels that cross: one is dearer on output, the other on input. The
	// price is the level with the highest output price (mainRate), so here
	// the other level, in its hours, costs more on input than the price
	// says. No published schedule is like this; the case pins the rule so
	// that it is not changed unnoticed.
	crossed := apiOf(t, one(`{"prompt": "0.000009", "completion": "0.000004", "overrides": [
		{"utc_start": 0, "utc_end": 1200, "prompt": "0.000009", "completion": "0.000004"},
		{"utc_start": 1200, "utc_end": 0, "prompt": "0.000001", "completion": "0.000005"}
	]}`))["vendor/m"]
	wantTriple(t, "crossed", PriceTriple{Input: crossed.Input, Cache: crossed.Cache, Output: crossed.Output}, f(1), nil, f(5))
	if want := []Rate{{Input: f(9), Output: f(4), Spans: all(Span{0, 720})}}; !sameRates(crossed.Rates, want) {
		t.Errorf("crossed rates = %s\nwant %s", ratesText(crossed.Rates), ratesText(want))
	}

	// The top-level prices the schedule supplies are not read, not even to
	// refuse them: a top-level price that is no price ("-1" is OpenRouter's
	// "varies") does not stop a complete schedule from being used. If it
	// did, and the value were bad in one window only, the plan would depend
	// on the hour again.
	for name, top := range map[string]string{
		"a top-level prompt of -1":           `"prompt": "-1", "completion": "0.000004", "input_cache_read": "0.0000002"`,
		"a top-level completion of -1":       `"prompt": "0.000002", "completion": "-1", "input_cache_read": "0.0000002"`,
		"a top-level input_cache_read of -1": `"prompt": "0.000002", "completion": "0.000004", "input_cache_read": "-1"`,
	} {
		got := apiOf(t, one(`{`+top+`, "overrides": [
			{"utc_start": 0, "utc_end": 1200, "prompt": "0.000002", "completion": "0.000004", "input_cache_read": "0.0000002"},
			{"utc_start": 1200, "utc_end": 0, "prompt": "0.000001", "completion": "0.000002", "input_cache_read": "0.0000001"}
		]}`))["vendor/m"]
		wantTriple(t, name, PriceTriple{Input: got.Input, Cache: got.Cache, Output: got.Output}, f(2), pm("0.0000002"), f(4))
		if got.Skipped != nil || got.Unusable != "" || len(got.Rates) != 1 {
			t.Errorf("%s: skipped %v, unusable %q, %d other levels; want the schedule's prices and no refusal", name, got.Skipped, got.Unusable, len(got.Rates))
		}
	}
	// The one top-level price a schedule can leave to the top level is the
	// cached-input price, when no window has one. Then it is read, and a
	// value that is no price refuses the model as it does for any model.
	if got := apiOf(t, one(`{"prompt": "0.000002", "completion": "0.000004", "input_cache_read": "-1", "overrides": [
		{"utc_start": 0, "utc_end": 1200, "prompt": "0.000002", "completion": "0.000004"},
		{"utc_start": 1200, "utc_end": 0, "prompt": "0.000001", "completion": "0.000002"}
	]}`))["vendor/m"]; !slices.Equal(got.Skipped, []string{"input_cache_read"}) {
		t.Errorf("a top-level input_cache_read of -1 that no window replaces: skipped = %v, want it refused", got.Skipped)
	}

	// A price key wt has no list entry for is still a price key when the
	// model's own pricing object has it at the top level: OpenRouter says an
	// entry's prices have the base object's keys. So a price OpenRouter adds
	// later does not stop the schedule from being read.
	newKey := apiOf(t, one(`{"prompt": "0.000002", "completion": "0.000004", "input_cache_write_5m": "0.000003", "overrides": [
		{"utc_start": 0, "utc_end": 1200, "prompt": "0.000002", "completion": "0.000004", "input_cache_write_5m": "0.000003"},
		{"utc_start": 1200, "utc_end": 0, "prompt": "0.000001", "completion": "0.000002", "input_cache_write_5m": "0.0000015"}
	]}`))["vendor/m"]
	wantTriple(t, "a new price key", PriceTriple{Input: newKey.Input, Cache: newKey.Cache, Output: newKey.Output}, f(2), nil, f(4))
	if newKey.Unusable != "" || len(newKey.Rates) != 1 {
		t.Errorf("a new price key: unusable %q, %d other levels; want the schedule read", newKey.Unusable, len(newKey.Rates))
	}

	// A model with both kinds of entry: the windows are read, the prompt-size
	// tier is not. And one price written two ways (text, a JSON number in
	// exponent form) is one level, not two.
	mixed := apiOf(t, one(`{"prompt": "0.000001", "completion": "0.000002", "overrides": [
		{"min_prompt_tokens": 200000, "prompt": "0.00001", "completion": "0.00002"},
		{"utc_start": 0, "utc_end": 600, "prompt": "0.000001", "completion": "0.000002"},
		{"utc_start": 600, "utc_end": 1200, "prompt": "0.000003", "completion": "0.000006"},
		{"utc_start": 1200, "utc_end": 0, "prompt": 1e-6, "completion": 2e-6}
	]}`))["vendor/m"]
	wantTriple(t, "mixed", PriceTriple{Input: mixed.Input, Cache: mixed.Cache, Output: mixed.Output}, f(3), nil, f(6))
	if want := []Rate{{Input: f(1), Output: f(2), Spans: all(Span{0, 360}, Span{720, 1440})}}; !sameRates(mixed.Rates, want) {
		t.Errorf("mixed rates = %s\nwant %s", ratesText(mixed.Rates), ratesText(want))
	}

	// Every window at one price: a schedule in form only. One price, no
	// other level.
	flat := apiOf(t, one(`{"prompt": "0.000009", "completion": "0.000009", "overrides": [
		{"utc_start": 0, "utc_end": 1200, "prompt": "0.000001", "completion": "0.000002"},
		{"utc_start": 1200, "utc_end": 0, "prompt": "0.000001", "completion": "0.000002"}
	]}`))["vendor/m"]
	wantTriple(t, "flat", PriceTriple{Input: flat.Input, Cache: flat.Cache, Output: flat.Output}, f(1), nil, f(2))
	if flat.Rates != nil || flat.Unusable != "" {
		t.Errorf("flat = %+v, want no other level and nothing unusable", flat)
	}

	// An entry that is not a time-of-day entry is ignored, whatever it holds.
	for name, pricing := range map[string]string{
		"a prompt-size tier":           `{"prompt": "0.000001", "completion": "0.000002", "overrides": [{"min_prompt_tokens": 200000, "prompt": "0.000002", "completion": "0.000004"}]}`,
		"a condition wt does not know": `{"prompt": "0.000001", "completion": "0.000002", "overrides": [{"region": "eu", "prompt": "-1", "completion": "soon"}]}`,
		"an overrides that is no list": `{"prompt": "0.000001", "completion": "0.000002", "overrides": {"utc_start": 0}}`,
		"an entry that is not a table": `{"prompt": "0.000001", "completion": "0.000002", "overrides": ["utc_start", 7, null]}`,
		"an empty overrides list":      `{"prompt": "0.000001", "completion": "0.000002", "overrides": []}`,
		"a null overrides":             `{"prompt": "0.000001", "completion": "0.000002", "overrides": null}`,
	} {
		got := apiOf(t, one(pricing))["vendor/m"]
		wantTriple(t, name, PriceTriple{Input: got.Input, Cache: got.Cache, Output: got.Output}, f(1), nil, f(2))
		if got.Rates != nil || got.Unusable != "" || got.Skipped != nil {
			t.Errorf("%s = %+v, want the top-level price and nothing else", name, got)
		}
	}

	// A schedule that cannot be read with certainty is reported, with the
	// reason, and never half used.
	const day, night2 = `"prompt": "0.000002", "completion": "0.000004"`, `"prompt": "0.000001", "completion": "0.000002"`
	for name, c := range map[string]struct{ overrides, want string }{
		"a gap":                 {`[{"utc_start": 0, "utc_end": 1200, ` + day + `}, {"utc_start": 1300, "utc_end": 0, ` + night2 + `}]`, "its windows do not cover the whole week"},
		"a day with no entry":   {`[{"utc_days": ["monday", "tuesday", "wednesday", "thursday", "friday", "saturday"], ` + day + `}]`, "its windows do not cover the whole week"},
		"a skipped entry's gap": {`[{"utc_start": 0, "utc_end": 1200, ` + day + `}, {"region": "eu", ` + night2 + `}]`, "its windows do not cover the whole week"},
		"a time and size entry": {`[{"utc_start": 0, "utc_end": 0, "min_prompt_tokens": 1000, ` + day + `}]`, "an entry sets both a time window and min_prompt_tokens"},
		"an unknown key":        {`[{"utc_start": 0, "utc_end": 0, "utc_months": ["may"], ` + day + `}]`, `an entry has a key wt does not know ("utc_months")`},
		"a discount key":        {`[{"utc_start": 0, "utc_end": 0, "discount": 0.5, ` + day + `}]`, `an entry has a key wt does not know ("discount")`},
		"a key like a price":    {`[{"utc_start": 0, "utc_end": 0, "input_cache_write_5m": "0.000003", ` + day + `}]`, `an entry has a key wt does not know ("input_cache_write_5m")`},
		"an entry's overrides":  {`[{"utc_start": 0, "utc_end": 0, "overrides": [], ` + day + `}]`, `an entry has a key wt does not know ("overrides")`},
		"no completion price":   {`[{"utc_start": 0, "utc_end": 0, "prompt": "0.000002"}]`, "an entry has no prompt or no completion price"},
		"a null prompt price":   {`[{"utc_start": 0, "utc_end": 0, "prompt": null, "completion": "0.000004"}]`, "an entry has no prompt or no completion price"},
		"a price that varies":   {`[{"utc_start": 0, "utc_end": 0, "prompt": "-1", "completion": "0.000004"}]`, "its prompt price in one window is negative or not a number"},
		"a cache price in some": {`[{"utc_start": 0, "utc_end": 1200, "input_cache_read": "0.0000001", ` + day + `}, {"utc_start": 1200, "utc_end": 0, ` + night2 + `}]`, "some of its windows have a cached-input price and some do not"},
		"a start with no end":   {`[{"utc_start": 0, ` + day + `}]`, "an entry has only one of utc_start and utc_end"},
		"an end with no start":  {`[{"utc_days": ["monday"], "utc_end": 1200, ` + day + `}]`, "an entry has only one of utc_start and utc_end"},
		"75 minutes":            {`[{"utc_start": 1675, "utc_end": 0, ` + day + `}]`, "utc_start or utc_end is not an HHMM time"},
		"24:00":                 {`[{"utc_start": 0, "utc_end": 2400, ` + day + `}]`, "utc_start or utc_end is not an HHMM time"},
		"a time as text":        {`[{"utc_start": "0100", "utc_end": 0, ` + day + `}]`, "utc_start or utc_end is not an HHMM time"},
		"a fractional time":     {`[{"utc_start": 100.5, "utc_end": 0, ` + day + `}]`, "utc_start or utc_end is not an HHMM time"},
		"a negative time":       {`[{"utc_start": -100, "utc_end": 0, ` + day + `}]`, "utc_start or utc_end is not an HHMM time"},
		"a day wt cannot name":  {`[{"utc_days": ["monday", "holiday"], ` + day + `}]`, "utc_days is not a list of weekday names"},
		"days as text":          {`[{"utc_days": "monday", ` + day + `}]`, "utc_days is not a list of weekday names"},
		"no days":               {`[{"utc_days": [], ` + day + `}]`, "utc_days is not a list of weekday names"},
	} {
		got := apiOf(t, one(`{"prompt": "0.000001", "completion": "0.000002", "overrides": `+c.overrides+`}`))["vendor/m"]
		if got.Unusable != c.want || got.Rates != nil {
			t.Errorf("%s: unusable = %q, rates %s\nwant %q and no rates", name, got.Unusable, ratesText(got.Rates), c.want)
		}
	}
}

// TestPlanPricesDoesNotFollowTheClock is the rule #322 asks for, on the saved
// response as OpenRouter serves it at every half hour of a week: the plan
// reads the same whenever the list was fetched, and once it is applied no
// later run in any window finds a price to update. Before the fix the sync
// stored whichever window it ran in, so two runs hours apart each reported
// an update, and each update rewrote LiteLLM's routes.
func TestPlanPricesDoesNotFollowTheClock(t *testing.T) {
	entries := func(cost func(id string) *Cost) []Entry {
		var out []Entry
		for _, name := range []string{"deepseek/deepseek-v4-pro-0813", "tencent/hy3", "tencent/hy4-preview", "anthropic/claude-haiku-5.5", "z-ai/glm-5.2"} {
			id := "openrouter/" + strings.ReplaceAll(name, "/", "--")
			out = append(out, Entry{ID: id, Family: "x", ProviderID: "openrouter", ModelName: name, Location: "cloud", Cost: cost(id)})
		}
		return out
	}
	monday := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	first := PlanPrices(entries(func(string) *Cost { return nil }), apiOf(t, fetchedAt(t, monday)))
	if len(first.Matched) != 5 || len(first.Warnings) != 0 {
		t.Fatalf("first plan = %s", first.Format())
	}
	stored := map[string]*Cost{}
	for _, m := range first.Matched {
		stored[m.ModelID] = &m.After
	}
	headlines := map[string]bool{}
	for step := range 7 * 48 {
		at := monday.Add(time.Duration(step) * 30 * time.Minute)
		body := fetchedAt(t, at)
		headlines[headline(t, body, "deepseek/deepseek-v4-pro-0813")+" "+headline(t, body, "tencent/hy3")] = true
		api := apiOf(t, body)
		if got := PlanPrices(entries(func(string) *Cost { return nil }), api).Format(); got != first.Format() {
			t.Fatalf("the plan for a new registry, fetched %s:\n%s\n\nfetched Monday 00:00:\n%s", at.Format("Mon 15:04"), got, first.Format())
		}
		again := PlanPrices(entries(func(id string) *Cost { return stored[id] }), api)
		for _, m := range again.Matched {
			if m.Changed {
				t.Fatalf("fetched %s, %s is a price update: %s -> %s", at.Format("Mon 15:04"), m.ModelID, formatCost(m.Before), formatCost(&m.After))
			}
		}
		if want := "Price updates (0):\nUnchanged prices: 5"; !strings.HasSuffix(again.Format(), want) {
			t.Fatalf("fetched %s, the plan after an apply:\n%s\nwant it to end %q", at.Format("Mon 15:04"), again.Format(), want)
		}
	}
	// The sweep did cross windows: the response itself took three forms
	// (deepseek is dear only in hours when hy3 is too).
	if len(headlines) != 3 {
		t.Errorf("the week's responses had %d different pairs of deepseek and hy3 top-level prices, want 3", len(headlines))
	}
	for id, want := range map[string][3]string{
		"openrouter/deepseek--deepseek-v4-pro-0813": {"0.00000132", "0.000000044", "0.00000396"},
		"openrouter/tencent--hy3":                   {"0.000000132", "0.000000033", "0.000000528"},
		"openrouter/anthropic--claude-haiku-5.5":    {"0.0000001", "0.00000001", "0.0000005"},
	} {
		wantTriple(t, id, PriceTriple{Input: stored[id].Input, Cache: stored[id].Cache, Output: stored[id].Output}, pm(want[0]), pm(want[1]), pm(want[2]))
	}
}

// TestPlanPricesLeavesAModelWhoseScheduleItCannotRead pins the refusal: such
// a model keeps the price it has and is not counted as refreshed, and the
// warning names it and says why, in words that do not change with the hour
// (a warning is part of the plan's text, which is compared under the lock).
// Falling back to the top-level price would bring the bug back for exactly
// the models whose schedule changed shape.
func TestPlanPricesLeavesAModelWhoseScheduleItCannotRead(t *testing.T) {
	entries := []Entry{orEntry("m", &Cost{Input: f(5), Output: f(6)})}
	body := func(prompt string) string {
		return one(`{"prompt": "` + prompt + `", "completion": "0.000004", "overrides": [
			{"utc_start": 0, "utc_end": 1200, "prompt": "0.000002", "completion": "0.000004"},
			{"utc_start": 1300, "utc_end": 0, "prompt": "0.000001", "completion": "0.000002"}
		]}`)
	}
	const want = "openrouter.ai: 1 OpenRouter-priced models in the registry (prices are input/cached/output per million tokens)\n" +
		"Price updates (0):\n" +
		"Unchanged prices: 0\n" +
		"warning: Could not use OpenRouter's time-of-day pricing for openrouter/m: its windows do not cover the whole week"
	// The third top-level price is no price at all: the warning is still the
	// time-of-day one, not the one about a price that is not a number.
	for _, prompt := range []string{"0.000002", "0.000001", "-1"} {
		plan := PlanPrices(entries, apiOf(t, body(prompt)))
		if got := plan.Format(); got != want || plan.HasWork() {
			t.Errorf("top-level prompt %s: Format() =\n%s\n\nwant\n%s\nand nothing to apply", prompt, got, want)
		}
	}
}

// TestPlanPricesReportsAScheduleThatComesAndGoes pins what the rule above
// does not promise. The plan follows what OpenRouter publishes for a model,
// and OpenRouter's list gives each model the pricing of one provider (its
// "top provider"). If that provider changes between two syncs, from one that
// prices by time of day to one that does not and back, the model loses and
// regains its schedule, and each time the sync reports a price update: to
// the listed price, then to the schedule's level again. That is a change in
// what is published, not the clock, and nothing here can tell it from a
// re-price. (deepseek/deepseek-v4-pro-0813 had 21 provider endpoints on
// 2026-10-09, one of them at 0.66/1.98 with no schedule; the list was not
// seen to switch to it.)
func TestPlanPricesReportsAScheduleThatComesAndGoes(t *testing.T) {
	const id, name = "openrouter/deepseek--deepseek-v4-pro-0813", "deepseek/deepseek-v4-pro-0813"
	scheduled := apiOf(t, readFile(t, "testdata/openrouter_models.json"))
	// The same model as the list would show it from a provider with one
	// price all day.
	unscheduled := apiOf(t, `{"data": [{"id": "`+name+`", "pricing": {"prompt": "0.00000066", "completion": "0.00000198", "input_cache_read": "0.000000022"}}]}`)
	var stored *Cost
	for i, step := range []struct {
		api             map[string]APIPrice
		in, cache, out  string
		wantOtherLevels int
	}{
		{scheduled, "0.00000132", "0.000000044", "0.00000396", 1},
		{unscheduled, "0.00000066", "0.000000022", "0.00000198", 0},
		{scheduled, "0.00000132", "0.000000044", "0.00000396", 1},
	} {
		plan := PlanPrices([]Entry{{ID: id, ProviderID: "openrouter", ModelName: name, Location: "cloud", Cost: stored}}, step.api)
		if len(plan.Matched) != 1 || !plan.Matched[0].Changed {
			t.Fatalf("step %d: want one price update, got\n%s", i, plan.Format())
		}
		after := plan.Matched[0].After
		wantTriple(t, "the stored price", PriceTriple{Input: after.Input, Cache: after.Cache, Output: after.Output}, pm(step.in), pm(step.cache), pm(step.out))
		if got := len(step.api[name].Rates); got != step.wantOtherLevels {
			t.Errorf("step %d: %d other levels, want %d", i, got, step.wantOtherLevels)
		}
		stored = &after
	}
}
```

- [ ] **Step 4: Run them to verify they fail**

Run (from `wt/`): `go test -count=1 ./internal/cloudsync`
Expected: the package does not build.

```text
# github.com/ohanaverse/local-ai-setup/wt/internal/cloudsync [github.com/ohanaverse/local-ai-setup/wt/internal/cloudsync.test]
internal/cloudsync/timeofday_test.go:164:16: undefined: Span
internal/cloudsync/timeofday_test.go:165:15: undefined: Span
internal/cloudsync/timeofday_test.go:168:20: undefined: Rate
…
FAIL	github.com/ohanaverse/local-ai-setup/wt/internal/cloudsync [build failed]
```

- [ ] **Step 5: Write the implementation**

Create `wt/internal/cloudsync/timeofday.go`:

```go
package cloudsync

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
)

const (
	minutesPerDay = 24 * 60
	daysPerWeek   = 7
)

var (
	// openRouterDays are OpenRouter's utc_days names, Monday first.
	openRouterDays = []string{"monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"}

	// timeConditionKeys are the keys that make an overrides entry a
	// time-of-day entry.
	timeConditionKeys = []string{"utc_start", "utc_end", "utc_days"}

	// overridePriceKeys are the price keys OpenRouter documents for a pricing
	// object. A time-of-day entry may carry any of them; the openrouter flow
	// reads three. An entry's prices have "the same keys and units as the
	// base pricing object" (OpenRouter's Models guide), so a key the model's
	// own pricing object has at its top level is a price key too, whether or
	// not it is in this list: a price OpenRouter adds later does not stop a
	// schedule from being read. A key that is none of these and not a time
	// condition is a condition wt does not know, and OpenRouter's rule for a
	// consumer is not to apply such an entry.
	overridePriceKeys = []string{
		"prompt", "completion", "input_cache_read", "input_cache_write", "input_cache_write_1h",
		"audio", "audio_output", "input_audio_cache", "image", "image_output", "image_token",
		"internal_reasoning", "request", "web_search",
	}
)

// Span is the minutes [Start, End) of one UTC day, 0 to 1440.
type Span struct{ Start, End int }

// Rate is one price level of a model's time-of-day schedule and when in the
// UTC week it applies.
type Rate struct {
	Input, Cache, Output *float64
	// Spans are the times of each UTC weekday (index 0 is Monday) at this
	// price, in ascending order, adjoining minutes joined.
	Spans [daysPerWeek][]Span
}

// parseSchedule reads the time-of-day entries of a pricing object's
// `overrides` list into price levels, dearest first: by output price, then
// input, then cached-input. (The order is by one price at a time, so the
// first level is the highest on output and need not be the highest on the
// other two.) It returns nil, "" for a model with no
// time-of-day entry, and a reason when there are such entries and they
// cannot be used.
//
// OpenRouter's rules, from its Models guide: utc_start and utc_end are HHMM
// clock numbers in UTC, the window is [start, end) and wraps past midnight
// when the end is not after the start; utc_days names the UTC weekdays the
// window (or, with no window, the whole day) applies on, tested at the
// instant of the request, and every day when absent; where entries overlap,
// the later one wins. The time entries cover the whole week, which is what
// makes this a function of the response alone: the top-level prices of such
// a model are those of the window in force when the list was fetched, and
// are not read.
//
// An entry with no utc_ key is not a time-of-day entry (a prompt-size tier,
// min_prompt_tokens, or a condition wt does not know) and is ignored: the
// top-level price already is the price under default conditions.
func parseSchedule(pricing map[string]any) (rates []Rate, unusable string) {
	entries, _ := pricing["overrides"].([]any)
	var (
		levels    []level
		week      [daysPerWeek * minutesPerDay]int
		withCache int
	)
	for i := range week {
		week[i] = -1
	}
	for _, raw := range entries {
		entry, ok := raw.(map[string]any)
		if !ok || !slices.ContainsFunc(timeConditionKeys, func(k string) bool { _, has := entry[k]; return has }) {
			continue
		}
		for _, key := range slices.Sorted(maps.Keys(entry)) {
			switch {
			case key == "min_prompt_tokens":
				return nil, "an entry sets both a time window and min_prompt_tokens"
			case !slices.Contains(timeConditionKeys, key) && !isPriceKey(pricing, key):
				return nil, fmt.Sprintf("an entry has a key wt does not know (%q)", key)
			}
		}
		days, ok := entryDays(entry)
		if !ok {
			return nil, "utc_days is not a list of weekday names"
		}
		start, end, why := entryWindow(entry)
		if why != "" {
			return nil, why
		}
		var l level
		for _, f := range []struct {
			key  string
			into **float64
		}{{"prompt", &l.in}, {"completion", &l.out}, {"input_cache_read", &l.cache}} {
			v, skipped := perMillion(entry[f.key])
			if skipped {
				return nil, "its " + f.key + " price in one window is negative or not a number"
			}
			*f.into = v
		}
		if l.in == nil || l.out == nil {
			// The missing price would be inherited from the top level, which
			// is the price of whichever window is in force.
			return nil, "an entry has no prompt or no completion price"
		}
		if l.cache != nil {
			withCache++
		}
		levels = append(levels, l)
		for d := range daysPerWeek {
			if !days[d] {
				continue
			}
			for m := range minutesPerDay {
				if start < end && (m < start || m >= end) || start > end && m < start && m >= end {
					continue
				}
				week[d*minutesPerDay+m] = len(levels) - 1
			}
		}
	}
	if len(levels) == 0 {
		return nil, ""
	}
	if withCache != 0 && withCache != len(levels) {
		return nil, "some of its windows have a cached-input price and some do not"
	}
	if slices.Contains(week[:], -1) {
		return nil, "its windows do not cover the whole week"
	}
	for d := range daysPerWeek {
		for m := 0; m < minutesPerDay; {
			l, from := levels[week[d*minutesPerDay+m]], m
			for m < minutesPerDay && levels[week[d*minutesPerDay+m]].same(l) {
				m++
			}
			at := slices.IndexFunc(rates, func(r Rate) bool { return l.same(level{r.Input, r.Cache, r.Output}) })
			if at < 0 {
				rates = append(rates, Rate{Input: l.in, Cache: l.cache, Output: l.out})
				at = len(rates) - 1
			}
			rates[at].Spans[d] = append(rates[at].Spans[d], Span{from, m})
		}
	}
	slices.SortStableFunc(rates, func(a, b Rate) int {
		return cmp.Or(cmp.Compare(*b.Output, *a.Output), cmp.Compare(*b.Input, *a.Input), cmp.Compare(deref(b.Cache), deref(a.Cache)))
	})
	return rates, ""
}

// isPriceKey reports whether key, on an overrides entry of pricing, names a
// price: one of the documented price keys, or a key pricing itself has at
// its top level (other than the overrides list).
func isPriceKey(pricing map[string]any, key string) bool {
	if slices.Contains(overridePriceKeys, key) {
		return true
	}
	_, atTop := pricing[key]
	return atTop && key != "overrides"
}

// level is the three prices one overrides entry charges.
type level struct{ in, cache, out *float64 }

func (l level) same(o level) bool {
	return samePrice(l.in, o.in) && samePrice(l.cache, o.cache) && samePrice(l.out, o.out)
}

func deref(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}

// entryDays reads utc_days: the weekdays an entry applies on, every day when
// the key is absent.
func entryDays(entry map[string]any) (days [daysPerWeek]bool, ok bool) {
	raw, has := entry["utc_days"]
	if !has {
		for d := range days {
			days[d] = true
		}
		return days, true
	}
	list, isList := raw.([]any)
	if !isList || len(list) == 0 {
		return days, false
	}
	for _, item := range list {
		name, _ := item.(string)
		d := slices.Index(openRouterDays, name)
		if d < 0 {
			return days, false
		}
		days[d] = true
	}
	return days, true
}

// entryWindow reads utc_start and utc_end as minutes of the day. With
// neither key the window is the whole day, which is start == end here: a
// window whose end is not after its start wraps, and one that wraps onto
// its own start covers every minute.
func entryWindow(entry map[string]any) (start, end int, unusable string) {
	_, hasStart := entry["utc_start"]
	_, hasEnd := entry["utc_end"]
	if !hasStart && !hasEnd {
		return 0, 0, ""
	}
	if hasStart != hasEnd {
		return 0, 0, "an entry has only one of utc_start and utc_end"
	}
	start, okStart := hhmmMinutes(entry["utc_start"])
	end, okEnd := hhmmMinutes(entry["utc_end"])
	if !okStart || !okEnd {
		return 0, 0, "utc_start or utc_end is not an HHMM time"
	}
	return start, end, ""
}

// hhmmMinutes reads an HHMM clock number (1630 is 16:30, 30 is 00:30) as
// minutes of the day.
func hhmmMinutes(v any) (int, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	hhmm, err := strconv.Atoi(n.String())
	if err != nil || hhmm < 0 || hhmm/100 > 23 || hhmm%100 > 59 {
		return 0, false
	}
	return hhmm/100*60 + hhmm%100, true
}

// mainRate is which of a schedule's price levels (dearest first) is stored
// as the model's flat price: the first, the one with the highest output
// price. This function is the one place that says so. The other levels are
// stored as time_prices rows (timeofday_rows.go), so the flat price is the
// model's price at any time none of those rows is in force, the price
// LiteLLM's route carries, and what wt shows wherever it does not apply the
// rows. For a request LiteLLM prices from the route, the first level does
// not understate the cost as long as it is also the highest on input and
// cached input, which holds for every schedule OpenRouter published when
// this was written and is not checked: TestParseOpenRouterTimeOfDayShapes
// pins a schedule where it does not hold.
func mainRate(rates []Rate) int { return 0 }
```

Then five edits to `wt/internal/cloudsync/openrouter.go`.

(a) The import block gains `slices`.

Find:

```go
	"math"
	"strconv"
```

Replace with:

```go
	"math"
	"slices"
	"strconv"
```

(b) `APIPrice` gains two fields.

Find:

```go
	// refreshed.
	Skipped []string
}
```

Replace with:

```go
	// refreshed.
	Skipped []string
	// Rates are the other price levels of a model OpenRouter prices by time
	// of day, dearest first, each with the times of the UTC week it applies.
	// For such a model Input, Cache and Output are the level mainRate picks
	// (the dearest) and not the top-level prices, which are those of the
	// window in force when the list was fetched. Nil for every other model.
	Rates []Rate
	// Unusable says why a model's time-of-day entries could not be read; a
	// model with one is not refreshed. "" otherwise.
	Unusable string
}
```

(c) In `ParseOpenRouter`, the last statement of the loop over the items. `pricing` is the item's pricing object, already in scope. The loop above it has already put the top-level prices in `p` and listed in `p.Skipped` the ones that are no price; for a readable schedule the prices are replaced and the keys the schedule supplies are taken off that list.

Find:

```go
		out[id] = p
```

Replace with:

```go
		rates, unusable := parseSchedule(pricing)
		p.Unusable = unusable
		if len(rates) > 0 {
			// The schedule supplies these prices, so the top-level ones are
			// not read, not even to refuse them.
			main, supplied := rates[mainRate(rates)], []string{"prompt", "completion"}
			p.Input, p.Output = main.Input, main.Output
			if main.Cache != nil {
				p.Cache, supplied = main.Cache, append(supplied, "input_cache_read")
			}
			if p.Skipped = slices.DeleteFunc(p.Skipped, func(key string) bool { return slices.Contains(supplied, key) }); len(p.Skipped) == 0 {
				p.Skipped = nil
			}
			if rest := slices.Delete(rates, mainRate(rates), mainRate(rates)+1); len(rest) > 0 {
				p.Rates = rest
			}
		}
		out[id] = p
```

(d) In `PlanPrices`, before the `Skipped` check, so that a model with an unusable schedule gets the time-of-day warning whatever its top-level prices are.

Find:

```go
		if len(price.Skipped) > 0 {
```

Replace with:

```go
		if price.Unusable != "" {
			// Its top-level price is that of the window in force now. Stored,
			// it would change with the hour the sync runs in. This is tested
			// before Skipped, which is about those top-level prices: the
			// warning must not depend on the hour either.
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("Could not use OpenRouter's time-of-day pricing for %s: %s", e.ID, price.Unusable))
			continue
		}
		if len(price.Skipped) > 0 {
```

(e) The comment on `PlanPrices` gains a paragraph.

Find:

```go
// are never touched, so a refresh never clears a price set by hand.
func PlanPrices(entries []Entry, api map[string]APIPrice) *PricePlan {
```

Replace with:

```go
// are never touched, so a refresh never clears a price set by hand.
//
// It reads no clock, and api is a function of the response alone, a model
// priced by time of day included (parseSchedule): the same registry and
// response give the same plan at any hour. The command relies on that when
// it plans again under the registry lock and compares the text.
func PlanPrices(entries []Entry, api map[string]APIPrice) *PricePlan {
```

- [ ] **Step 6: Run the tests to verify they pass**

Run (from `wt/`): `gofmt -l internal cmd && go vet ./internal/cloudsync ./cmd/wt && go test -count=1 -v ./internal/cloudsync -run 'TestFetchedAtIsWhatOpenRouterSent|TestParseOpenRouterTimeOfDay|TestPlanPricesDoesNotFollowTheClock|TestPlanPricesLeavesAModelWhoseScheduleItCannotRead|TestPlanPricesReportsAScheduleThatComesAndGoes' | grep -E '^(---|ok|FAIL)'`
Expected (`gofmt -l` prints nothing):

```text
--- PASS: TestFetchedAtIsWhatOpenRouterSent (0.00s)
--- PASS: TestParseOpenRouterTimeOfDay (0.00s)
--- PASS: TestParseOpenRouterTimeOfDayShapes (0.00s)
--- PASS: TestPlanPricesDoesNotFollowTheClock (0.07s)
--- PASS: TestPlanPricesLeavesAModelWhoseScheduleItCannotRead (0.00s)
--- PASS: TestPlanPricesReportsAScheduleThatComesAndGoes (0.00s)
ok  	github.com/ohanaverse/local-ai-setup/wt/internal/cloudsync	(time)
```

Run: `go test -count=1 ./cmd/wt -run TestCloudSyncTimeOfDayPricesDoNotFollowTheClock -v | grep -E '^(---|ok|FAIL)'`
Expected:

```text
--- PASS: TestCloudSyncTimeOfDayPricesDoNotFollowTheClock (0.00s)
ok  	github.com/ohanaverse/local-ai-setup/wt/cmd/wt	(time)
```

Then the two packages whose existing tests read `ParseOpenRouter` and `PlanPrices`: `go test -count=1 ./internal/cloudsync ./cmd/wt`
Expected: both `ok`. (No existing test body has an `overrides` key, so none changes.)

- [ ] **Step 7: Commit**

From the repo root:

```bash
git add wt/internal/cloudsync/testdata/openrouter_models.json wt/internal/cloudsync/timeofday.go wt/internal/cloudsync/timeofday_test.go wt/internal/cloudsync/openrouter.go wt/cmd/wt/cloudsync_test.go
git commit -m "fix(wt): cloud-sync prices a time-of-day model from its schedule, not from the hour it ran in (#322)"
```

### Task 8: Store the other levels as the flow's own rows, and show them in the plan (slice D)

**Files:**
- Create: `wt/internal/cloudsync/timeofday_rows_test.go`, `wt/internal/cloudsync/timeofday_rows.go`
- Modify: `wt/cmd/wt/cloudsync_test.go` (two edits to Task 7's test; append), `wt/internal/litellm/entry_test.go` (append), `wt/internal/cloudsync/openrouter.go` (`PlanPrices`: one line and its comment), `wt/internal/cloudsync/catalog.go` (`formatCost`; the `OffpeakLabel` comment)

**Interfaces:**
- Consumes from Task 7: `type Rate struct{ Input, Cache, Output *float64; Spans [7][]Span }`, `type Span struct{ Start, End int }`, `APIPrice.Rates []Rate`, the test helpers `fixtureFetched`, `fetchedAt`, `headline`, `pm`, `one`, and in `cmd/wt` the constant `timeOfDayRegistry` and the test `TestCloudSyncTimeOfDayPricesDoNotFollowTheClock`. From Task 2: `PlanPrices(entries, api)`, `TestTheTwoFlowsNeverShareAModel`.
- Consumes (existing): `Cost.TimePrices []*tomlw.Table`; `str(t *tomlw.Table, key string) string`; `number(t *tomlw.Table, key string) *float64`; `formatPrice(*float64) string`; `tomlw.NewTable()`, `(*tomlw.Table).Set/Get/Keys`; test helpers `timeRow(label, day string) *tomlw.Table`, `labels([]*tomlw.Table) []string`, `wantIDs`, `cloudEntry`, `withCost`, `cm`, `withOffpeak`, `catalogOf`, `scratchRegistry(t, content string) string`, `userTimePrice`; in `cmd/wt`: `realRouteSync(t)`, `syncRoutesAfterWrite`, `probeInventory`, `localmodels.OnDiskSnapshotForTest`, `stubCloudFetch`, `runCS`, `mustRead`, the `cloudSyncNow` seam; in `internal/litellm`: `pricingInfo(c config.ModelCost) []kv`.
- Produces:
  - `const OpenRouterLabel = "openrouter"`
  - `func rateRow(r Rate) *tomlw.Table`
  - `func isOpenRouterRow(row *tomlw.Table) bool`
  - `func withOpenRouterRows(existing []*tomlw.Table, rates []Rate) []*tomlw.Table`
  - `func formatOpenRouterRow(row *tomlw.Table) string`, `func formatWindows(row *tomlw.Table) string`, `func formatDays(w *tomlw.Table) string`
  - the plan-line ending ` (openrouter <in>/<cached>/<out> <windows>)`, one per row
  - the registry shape Task 3's resolver and Task 4's fixtures read: a model whose flat price is the dearest level and whose `cost.time_prices` holds one row per cheaper level, `label = "openrouter"`, `timezone = "UTC"`

- [ ] **Step 1: Make the command test expect the rows, and add the rows-only test**

Two edits to `TestCloudSyncTimeOfDayPricesDoNotFollowTheClock` in `wt/cmd/wt/cloudsync_test.go`.

(a) The `plan` constant: each line gains its row.

Find:

```go
		"openrouter:   openrouter/deepseek--deepseek-v4-pro-0813: 0.66/0.022/1.98 -> 1.32/0.044/3.96\n" +
		"openrouter:   openrouter/tencent--hy3: no cost -> 0.132/0.033/0.528\n" +
```

Replace with:

```go
		"openrouter:   openrouter/deepseek--deepseek-v4-pro-0813: 0.66/0.022/1.98 -> 1.32/0.044/3.96 (openrouter 0.66/0.022/1.98 mon-fri 00:00-01:00 04:00-06:00 10:00-24:00, sat-sun 00:00-24:00)\n" +
		"openrouter:   openrouter/tencent--hy3: no cost -> 0.132/0.033/0.528 (openrouter 0.0825/0.020625/0.33 mon-sun 16:00-24:00)\n" +
```

(b) The first snippet the applied registry must hold: the cost table is now followed by the row.

Find:

```go
		"input_price_per_million = 1.32\ncache_price_per_million = 0.044\noutput_price_per_million = 3.9600000000000004\n\n[[models]]\n",
```

Replace with:

```go
		"input_price_per_million = 1.32\ncache_price_per_million = 0.044\noutput_price_per_million = 3.9600000000000004\n\n[[models.cost.time_prices]]\nlabel = \"openrouter\"\ntimezone = \"UTC\"\ninput_price_per_million = 0.66\n",
```

Then append to the same file (every import it needs is already there):

```go
// TestCloudSyncRowsOnlyUpdateLeavesTheRoutesAlone pins the first sync after
// the rows were added, for a user whose last sync before #322 was fixed ran
// in the models' dear windows: the flat prices are already the ones the
// schedule gives, so only the rows are new. That is a price update (it is
// listed and counted, and the routes are synced), but a route is priced from
// the flat prices alone, so the real route sync finds config.yaml as it
// should be: the file keeps its bytes and the proxy is not restarted under
// running agents.
func TestCloudSyncRowsOnlyUpdateLeavesTheRoutesAlone(t *testing.T) {
	// timeOfDayRegistry as that earlier sync left it: both models at their
	// dearest level, no time_prices row.
	dearStored := strings.Replace(timeOfDayRegistry,
		"input_price_per_million = 0.66\ncache_price_per_million = 0.022\noutput_price_per_million = 1.9800000000000002\n",
		"input_price_per_million = 1.32\ncache_price_per_million = 0.044\noutput_price_per_million = 3.9600000000000004\n", 1) +
		"\n[models.cost]\ninput_price_per_million = 0.13199999999999998\ncache_price_per_million = 0.032999999999999995\noutput_price_per_million = 0.5279999999999999\n"
	home := t.TempDir()
	registry, yaml, marker := filepath.Join(home, "scratch", "registry.toml"), filepath.Join(home, "scratch", "config.yaml"), filepath.Join(home, "restarted")
	if err := os.MkdirAll(filepath.Dir(registry), 0o700); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{registry: dearStored, yaml: "model_list: []\n"} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("MODELMAN_REGISTRY", "")
	t.Setenv("WT_REGISTRY", registry)
	t.Setenv("WT_LITELLM_CONFIG", yaml)
	t.Setenv("MODELMAN_LITELLM_CONFIG", "")
	t.Setenv("WT_TEST_OPENROUTER_KEY", "sk-test-not-a-real-key")
	realRouteSync(t)
	t.Setenv("WT_LITELLM_RESTART_CMD", "touch "+marker)
	// realRouteSync probes nothing; say instead that every provider
	// answered, so the sync has no probe failure to warn about.
	probeInventory = localmodels.OnDiskSnapshotForTest

	// The routes as that earlier sync left them. Writing them restarts the
	// proxy, which is how this test knows the marker would show a restart.
	var said bytes.Buffer
	if warning := syncRoutesAfterWrite(&said, &said); warning != "" {
		t.Fatalf("the first route sync warned: %s\n%s", warning, said.String())
	}
	routes := mustRead(t, yaml)
	if _, err := os.Stat(marker); err != nil || !strings.Contains(routes, "deepseek/deepseek-v4-pro-0813") {
		t.Fatalf("the first route sync did not write the routes and restart the proxy (marker: %v):\n%s\n%s", err, said.String(), routes)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile("../../internal/cloudsync/testdata/openrouter_models.json")
	if err != nil {
		t.Fatal(err)
	}
	stubCloudFetch(t, map[string]string{cloudsync.OpenRouterModelsURL: string(raw)})
	old := cloudSyncNow
	cloudSyncNow = func() time.Time { return time.Date(2026, 10, 9, 14, 6, 0, 0, time.UTC) }
	t.Cleanup(func() { cloudSyncNow = old })
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	stdout, stderr, code := runCS(t, cfg, cloudSyncOpts{openrouter: true, yes: true})
	const want = "openrouter: openrouter.ai: 2 OpenRouter-priced models in the registry (prices are input/cached/output per million tokens)\n" +
		"openrouter: Price updates (2):\n" +
		"openrouter:   openrouter/deepseek--deepseek-v4-pro-0813: 1.32/0.044/3.96 -> 1.32/0.044/3.96 (openrouter 0.66/0.022/1.98 mon-fri 00:00-01:00 04:00-06:00 10:00-24:00, sat-sun 00:00-24:00)\n" +
		"openrouter:   openrouter/tencent--hy3: 0.132/0.033/0.528 -> 0.132/0.033/0.528 (openrouter 0.0825/0.020625/0.33 mon-sun 16:00-24:00)\n" +
		"openrouter: Unchanged prices: 0\n" +
		"openrouter: refreshed 2 model(s); 2 price(s) changed\n"
	if stdout != want || stderr != "" || code != 0 {
		t.Errorf("stdout:\n%s\nstderr: %q, exit %d\nwant stdout:\n%s", stdout, stderr, code, want)
	}
	if got := mustRead(t, yaml); got != routes {
		t.Errorf("config.yaml after an update that changed only rows:\n%s\nwant it as it was:\n%s", got, routes)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the proxy was restarted after an update that changed only rows")
	}
	if got := mustRead(t, registry); strings.Count(got, "label = \"openrouter\"") != 2 {
		t.Errorf("registry.toml does not hold one openrouter row per model:\n%s", got)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run (from `wt/`): `gofmt -l cmd; go test -count=1 ./cmd/wt -run 'TestCloudSyncTimeOfDayPricesDoNotFollowTheClock|TestCloudSyncRowsOnlyUpdateLeavesTheRoutesAlone' 2>&1 | grep -v '^wt: migrated'`
Expected: both FAIL. What is missing is the rows:

```text
--- FAIL: TestCloudSyncTimeOfDayPricesDoNotFollowTheClock (0.00s)
    cloudsync_test.go:1000: dry run in the cheap window:
        openrouter: openrouter.ai: 2 OpenRouter-priced models in the registry (prices are input/cached/output per million tokens)
        openrouter: Price updates (2):
        openrouter:   openrouter/deepseek--deepseek-v4-pro-0813: 0.66/0.022/1.98 -> 1.32/0.044/3.96
        openrouter:   openrouter/tencent--hy3: no cost -> 0.132/0.033/0.528
        openrouter: Unchanged prices: 0
        
        want:
        openrouter: openrouter.ai: 2 OpenRouter-priced models in the registry (prices are input/cached/output per million tokens)
        openrouter: Price updates (2):
        openrouter:   openrouter/deepseek--deepseek-v4-pro-0813: 0.66/0.022/1.98 -> 1.32/0.044/3.96 (openrouter 0.66/0.022/1.98 mon-fri 00:00-01:00 04:00-06:00 10:00-24:00, sat-sun 00:00-24:00)
        openrouter:   openrouter/tencent--hy3: no cost -> 0.132/0.033/0.528 (openrouter 0.0825/0.020625/0.33 mon-sun 16:00-24:00)
        openrouter: Unchanged prices: 0
    cloudsync_test.go:1003: dry run in the dear window:
        …
    cloudsync_test.go:1018: registry.toml after the apply lacks:
        …
--- FAIL: TestCloudSyncRowsOnlyUpdateLeavesTheRoutesAlone (0.01s)
    cloudsync_test.go:1111: stdout:
        openrouter: openrouter.ai: 2 OpenRouter-priced models in the registry (prices are input/cached/output per million tokens)
        openrouter: Price updates (0):
        openrouter: Unchanged prices: 2
        openrouter: refreshed 2 model(s); 0 price(s) changed
        …
    cloudsync_test.go:1120: registry.toml does not hold one openrouter row per model:
        …
FAIL
```

- [ ] **Step 3: Write the failing package tests**

Create `wt/internal/cloudsync/timeofday_rows_test.go`:

```go
package cloudsync

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tomlw"
)

// scheduleRows is the cost.time_prices rows a plan gives the one model of a
// response whose pricing object is pricing.
func scheduleRows(t *testing.T, pricing string) []*tomlw.Table {
	t.Helper()
	plan := PlanPrices([]Entry{orEntry("m", nil)}, apiOf(t, one(pricing)))
	if len(plan.Matched) != 1 {
		t.Fatalf("plan = %s", plan.Format())
	}
	return plan.Matched[0].After.TimePrices
}

// TestScheduleRows pins how a schedule is written as cost.time_prices rows,
// in the registry's own vocabulary, which is not OpenRouter's: UTC, days as
// mon..sun, times as "HH:MM", the end of the day as "24:00", and no window
// that ends before it starts (the registry refuses one), so a window that
// wraps past midnight is two. A row that the registry's loader refused would
// make every launch fail until it was removed by hand.
func TestScheduleRows(t *testing.T) {
	// OpenRouter's own example of a wrapping window: 16:30 to 00:30 UTC.
	rows := scheduleRows(t, `{"prompt": "0.00000014", "completion": "0.00000021", "overrides": [
		{"utc_start": 30, "utc_end": 1630, "prompt": "0.00000028", "completion": "0.00000042"},
		{"utc_start": 1630, "utc_end": 30, "prompt": "0.00000014", "completion": "0.00000021"}
	]}`)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	// Keys in schema order: a table is written in the order its keys were
	// set. No cache key, because no window has a cached-input price.
	if got, want := rows[0].Keys(), []string{"label", "timezone", "input_price_per_million", "output_price_per_million", "windows"}; !reflect.DeepEqual(got, want) {
		t.Errorf("row keys = %v, want %v", got, want)
	}
	if str(rows[0], "label") != "openrouter" || str(rows[0], "timezone") != "UTC" ||
		!samePrice(number(rows[0], "input_price_per_million"), pm("0.00000014")) || !samePrice(number(rows[0], "output_price_per_million"), pm("0.00000021")) {
		t.Errorf("row = %v", rows[0])
	}
	raw, _ := rows[0].Get("windows")
	windows := raw.([]any)
	if len(windows) != 2 {
		t.Fatalf("windows = %d, want the wrapping window as two", len(windows))
	}
	if got, want := windows[0].(*tomlw.Table).Keys(), []string{"days", "start", "end"}; !reflect.DeepEqual(got, want) {
		t.Errorf("window keys = %v, want %v", got, want)
	}
	days, _ := windows[1].(*tomlw.Table).Get("days")
	if want := []any{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}; !reflect.DeepEqual(days, want) {
		t.Errorf("days = %v, want %v", days, want)
	}
	if got, want := formatWindows(rows[0]), "mon-sun 00:00-00:30 16:30-24:00"; got != want {
		t.Errorf("windows = %q, want %q", got, want)
	}

	// Days with different times get their own windows, grouped by the times
	// they share; a day with none of this price is not named.
	rows = scheduleRows(t, `{"prompt": "0.000002", "completion": "0.000004", "overrides": [
		{"utc_days": ["monday", "wednesday", "friday"], "utc_start": 900, "utc_end": 1700, "prompt": "0.000002", "completion": "0.000004"},
		{"utc_days": ["monday", "wednesday", "friday"], "utc_start": 1700, "utc_end": 900, "prompt": "0.000001", "completion": "0.000002"},
		{"utc_days": ["tuesday", "thursday"], "prompt": "0.000002", "completion": "0.000004"},
		{"utc_days": ["saturday", "sunday"], "prompt": "0.000001", "completion": "0.000002"}
	]}`)
	if len(rows) != 1 || formatWindows(rows[0]) != "mon,wed,fri 00:00-09:00 17:00-24:00, sat-sun 00:00-24:00" {
		t.Errorf("rows = %d, windows %q", len(rows), formatWindows(rows[0]))
	}

	// One row per level below the dearest, dearest first.
	rows = scheduleRows(t, `{"prompt": "0.000002", "completion": "0.000004", "overrides": [
		{"utc_start": 0, "utc_end": 0, "prompt": "0.000001", "completion": "0.000002"},
		{"utc_start": 800, "utc_end": 2000, "prompt": "0.000003", "completion": "0.000006"},
		{"utc_start": 1200, "utc_end": 1400, "prompt": "0.000002", "completion": "0.000004"}
	]}`)
	if len(rows) != 2 || formatWindows(rows[0]) != "mon-sun 12:00-14:00" || formatWindows(rows[1]) != "mon-sun 00:00-08:00 20:00-24:00" ||
		!samePrice(number(rows[0], "input_price_per_million"), f(2)) || !samePrice(number(rows[1], "input_price_per_million"), f(1)) {
		t.Errorf("three levels: %d rows", len(rows))
	}

	// Every window at one price, a prompt-size tier, no overrides: no row.
	for _, pricing := range []string{
		`{"prompt": "0.000001", "completion": "0.000002", "overrides": [{"utc_start": 0, "utc_end": 0, "prompt": "0.000001", "completion": "0.000002"}]}`,
		`{"prompt": "0.000001", "completion": "0.000002", "overrides": [{"min_prompt_tokens": 200000, "prompt": "0.000002", "completion": "0.000004"}]}`,
		`{"prompt": "0.000001", "completion": "0.000002"}`,
	} {
		if rows := scheduleRows(t, pricing); rows != nil {
			t.Errorf("%s: %d rows, want none", pricing, len(rows))
		}
	}
}

// TestPlanPricesOwnsOnlyItsOwnRows pins whose cost.time_prices rows are
// whose. The openrouter flow owns the rows labelled openrouter and no other:
// it replaces them where the first one stood (rows resolve first match wins,
// so moving one changes which price is in force) and drops them when the
// model no longer has a schedule. A row the user wrote, under any other
// label, is the very table it was read as, which is what lets the write
// leave its bytes alone. The ollama flow never plans from an openrouter
// model (TestTheTwoFlowsNeverShareAModel); what it does with a row labelled
// openrouter that someone wrote by hand on an ollama entry is keep it, as it
// keeps every row that is not its off-peak one.
func TestPlanPricesOwnsOnlyItsOwnRows(t *testing.T) {
	mine, offpeak := timeRow("mine", "sun"), timeRow("off-peak", "sat")
	rows := func() []*tomlw.Table {
		return []*tomlw.Table{mine, timeRow("openrouter", "mon"), offpeak, timeRow("openrouter", "tue")}
	}
	api := apiOf(t, readFile(t, "testdata/openrouter_models.json"))
	entry := func(name string, cost *Cost) Entry {
		return Entry{ID: "openrouter/" + name, ProviderID: "openrouter", ModelName: name, Location: "cloud", Cost: cost}
	}

	// A model with a schedule: the two stale rows become the one current row,
	// where the first stood.
	plan := PlanPrices([]Entry{entry("tencent/hy3", &Cost{TimePrices: rows()})}, api)
	if len(plan.Matched) != 1 {
		t.Fatalf("plan = %s", plan.Format())
	}
	after := plan.Matched[0].After.TimePrices
	wantIDs(t, "rows of a model with a schedule", labels(after), "mine", "openrouter", "off-peak")
	if after[0] != mine || after[2] != offpeak {
		t.Error("a row the openrouter flow does not own is not the table it was read as")
	}
	if got := formatWindows(after[1]); got != "mon-sun 16:00-24:00" {
		t.Errorf("the schedule row's windows = %q", got)
	}
	// Planned again from its own result, nothing is left to change.
	if again := PlanPrices([]Entry{entry("tencent/hy3", &plan.Matched[0].After)}, api); again.Matched[0].Changed {
		t.Errorf("a second plan still changes the model: %s", again.Format())
	}

	// A model with no schedule (any more): its openrouter rows go, and the
	// plan shows that they do.
	plan = PlanPrices([]Entry{entry("z-ai/glm-5.2", &Cost{Input: pm("0.00000006"), Cache: pm("0.000000059"), Output: pm("0.000006"), TimePrices: rows()})}, api)
	wantIDs(t, "rows of a model with no schedule", labels(plan.Matched[0].After.TimePrices), "mine", "off-peak")
	if want := "  openrouter/z-ai/glm-5.2: 0.06/0.059/6 (off-peak -/-/-) (openrouter -/-/- mon 00:00-24:00) (openrouter -/-/- tue 00:00-24:00) -> 0.06/0.059/6 (off-peak -/-/-)"; !strings.Contains(plan.Format(), want) {
		t.Errorf("Format() =\n%s\n\nwant the line\n%s", plan.Format(), want)
	}

	// A model with neither: the list is the one it had.
	kept := []*tomlw.Table{mine, offpeak}
	plan = PlanPrices([]Entry{entry("z-ai/glm-5.2", &Cost{TimePrices: kept})}, api)
	if got := plan.Matched[0].After.TimePrices; len(got) != 2 || got[0] != mine || got[1] != offpeak {
		t.Errorf("rows of a model with nothing of the flow's = %v", labels(got))
	}

	// A row labelled openrouter on an ollama cloud entry is not the
	// openrouter flow's (that flow never sees the entry): to the ollama flow
	// it is one more row that is not its off-peak one, and is kept.
	cost := Cost{Input: f(1), TimePrices: rows()}
	catalog := PlanCatalog([]Entry{cloudEntry("a:cloud", withCost(cost))}, catalogOf(withOffpeak(cm("a"), f(0.5), nil, f(1))), nil, nil)
	wantIDs(t, "rows after the ollama flow", labels(catalog.Updates[0].After.TimePrices), "mine", "openrouter", "off-peak", "openrouter")
}

// TestPricePlanFormatShowsASchedule pins the plan's text for a model with a
// schedule: the flat price, then each stored level with its windows. The
// windows are printed because they are written: a plan that changed only a
// row's hours would otherwise read `x -> x`, and the rule is that nothing is
// written that the printed plan does not show.
func TestPricePlanFormatShowsASchedule(t *testing.T) {
	api := apiOf(t, readFile(t, "testdata/openrouter_models.json"))
	entry := func(id, name string, cost *Cost) Entry {
		return Entry{ID: id, ProviderID: "openrouter", ModelName: name, Location: "cloud", Cost: cost}
	}
	const deepseek = "deepseek/deepseek-v4-pro-0813"
	// What a sync before the fix left, in each window.
	low := &Cost{Input: pm("0.00000066"), Cache: pm("0.000000022"), Output: pm("0.00000198")}
	high := &Cost{Input: pm("0.00000132"), Cache: pm("0.000000044"), Output: pm("0.00000396")}
	// A row an earlier sync wrote, for a schedule that has since moved.
	moved := *high
	moved.TimePrices = scheduleRows(t, `{"prompt": "0.00000066", "completion": "0.00000198", "overrides": [
		{"utc_start": 0, "utc_end": 1200, "prompt": "0.00000132", "completion": "0.00000396", "input_cache_read": "0.000000044"},
		{"utc_start": 1200, "utc_end": 0, "prompt": "0.00000066", "completion": "0.00000198", "input_cache_read": "0.000000022"}
	]}`)
	plan := PlanPrices([]Entry{
		entry("or/new", deepseek, nil),
		entry("or/low", deepseek, low),
		entry("or/high", deepseek, high),
		entry("or/moved", deepseek, &moved),
		entry("or/hy4", "tencent/hy4-preview", nil),
		entry("or/tier", "anthropic/claude-haiku-5.5", nil),
	}, api)
	const schedule = "1.32/0.044/3.96 (openrouter 0.66/0.022/1.98 mon-fri 00:00-01:00 04:00-06:00 10:00-24:00, sat-sun 00:00-24:00)"
	want := strings.Join([]string{
		"openrouter.ai: 6 OpenRouter-priced models in the registry (prices are input/cached/output per million tokens)",
		"Price updates (6):",
		"  or/new: no cost -> " + schedule,
		"  or/low: 0.66/0.022/1.98 -> " + schedule,
		"  or/high: 1.32/0.044/3.96 -> " + schedule,
		"  or/moved: 1.32/0.044/3.96 (openrouter 0.66/0.022/1.98 mon-sun 12:00-24:00) -> " + schedule,
		"  or/hy4: no cost -> 0.834/0.042/2.501 (openrouter 0.7506/0.0378/2.2509 mon-sun 16:00-24:00)",
		"  or/tier: no cost -> 0.1/0.01/0.5",
		"Unchanged prices: 0",
	}, "\n")
	if got := plan.Format(); got != want {
		t.Errorf("Format() =\n%s\n\nwant\n%s", got, want)
	}
}

// TestPlanPricesReplacesAnOpenRouterRowWhateverItHolds pins the plan for a
// registry someone edited by hand: a row labelled openrouter whose windows
// are not what the sync writes (not a list, a list of other things, days
// that are no days). The plan must still print, show the row as it found it
// and replace it; a plan that panicked here would leave the user with no way
// to refresh prices but to find the row themselves. And a row that is the
// sync's own but for its timezone, or for one added key, is replaced too
// (rows are compared whole), so the line must show that difference: a line
// that read the same on both sides would be an update the user cannot see.
func TestPlanPricesReplacesAnOpenRouterRowWhateverItHolds(t *testing.T) {
	noList, oddList, oddDays := tomlw.NewTable(), tomlw.NewTable(), tomlw.NewTable()
	noList.Set("label", "openrouter")
	noList.Set("windows", "all day")
	oddList.Set("label", "openrouter")
	oddList.Set("windows", []any{int64(7), "x"})
	w := tomlw.NewTable()
	w.Set("days", []any{"fri", int64(1), "mon"})
	w.Set("start", int64(9))
	oddDays.Set("label", "openrouter")
	oddDays.Set("input_price_per_million", "cheap")
	oddDays.Set("windows", []any{w})
	api := apiOf(t, readFile(t, "testdata/openrouter_models.json"))
	plan := PlanPrices([]Entry{{ID: "or/hy3", ProviderID: "openrouter", ModelName: "tencent/hy3", Location: "cloud",
		Cost: &Cost{TimePrices: []*tomlw.Table{noList, oddList, oddDays}}}}, api)
	want := "  or/hy3: -/-/- (openrouter -/-/- timezone=\"\" ?) (openrouter -/-/- timezone=\"\" ?, ?) (openrouter -/-/- timezone=\"\" fri,,mon -) -> " +
		"0.132/0.033/0.528 (openrouter 0.0825/0.020625/0.33 mon-sun 16:00-24:00)"
	if !strings.Contains(plan.Format(), want) {
		t.Errorf("Format() =\n%s\n\nwant the line\n%s", plan.Format(), want)
	}

	// The sync's own row, edited: another timezone on one model, a note on
	// a window of another. Each is a fresh copy of what the sync writes.
	synced := func() *Cost {
		return &PlanPrices([]Entry{{ID: "or/hy3", ProviderID: "openrouter", ModelName: "tencent/hy3", Location: "cloud"}}, api).Matched[0].After
	}
	zoned, noted := synced(), synced()
	zoned.TimePrices[0].Set("timezone", "America/New_York")
	windows, _ := noted.TimePrices[0].Get("windows")
	windows.([]any)[0].(*tomlw.Table).Set("note", "mine")
	plan = PlanPrices([]Entry{
		{ID: "or/zoned", ProviderID: "openrouter", ModelName: "tencent/hy3", Location: "cloud", Cost: zoned},
		{ID: "or/noted", ProviderID: "openrouter", ModelName: "tencent/hy3", Location: "cloud", Cost: noted},
		{ID: "or/synced", ProviderID: "openrouter", ModelName: "tencent/hy3", Location: "cloud", Cost: synced()},
	}, api)
	const row = "0.132/0.033/0.528 (openrouter 0.0825/0.020625/0.33 mon-sun 16:00-24:00)"
	want = strings.Join([]string{
		"Price updates (2):",
		"  or/zoned: 0.132/0.033/0.528 (openrouter 0.0825/0.020625/0.33 timezone=\"America/New_York\" mon-sun 16:00-24:00) -> " + row,
		"  or/noted: 0.132/0.033/0.528 (openrouter 0.0825/0.020625/0.33 mon-sun 16:00-24:00 +keys) -> " + row,
		"Unchanged prices: 1",
	}, "\n")
	if !strings.HasSuffix(plan.Format(), want) {
		t.Errorf("Format() =\n%s\n\nwant it to end\n%s", plan.Format(), want)
	}
}

// scheduleRegistry is the registry the schedule's Apply test starts from, in
// the layout wt's writer gives one: a model a sync before the fix left at
// its off-peak price, with a row the user wrote; a model with no cost; and a
// model with no schedule that holds a row of each owner, the openrouter flow's
// from a schedule OpenRouter has since dropped.
const scheduleRegistry = `[[providers]]
id = "openrouter"
name = "OpenRouter"
location = "cloud"

[providers.auth]
type = "api_key"
secret_ref = "OPENROUTER_API_KEY"

[[models]]
id = "openrouter/deepseek--deepseek-v4-pro-0813"
family = "deepseek"
provider_id = "openrouter"
model_name = "deepseek/deepseek-v4-pro-0813"
location = "cloud"
pricing_updated_at = "2026-10-09T00:28:00+00:00"

[models.cost]
input_price_per_million = 0.66
cache_price_per_million = 0.022
output_price_per_million = 1.9800000000000002

` + userTimePrice + `
[[models]]
id = "openrouter/tencent--hy3"
family = "hy"
provider_id = "openrouter"
model_name = "tencent/hy3"
location = "cloud"

[[models]]
id = "openrouter/z-ai--glm-5.2"
family = "glm"
provider_id = "openrouter"
model_name = "z-ai/glm-5.2"
location = "cloud"

[models.cost]
input_price_per_million = 0.06
cache_price_per_million = 0.059
output_price_per_million = 6.0

` + userTimePrice + `
[[models.cost.time_prices]]
label = "openrouter"
timezone = "UTC"
input_price_per_million = 0.03

[[models.cost.time_prices.windows]]
days = [
    "sat",
    "sun",
]
start = "00:00"
end = "24:00"

` + catalogTimePrice

// catalogTimePrice is an off-peak row as the ollama flow writes one.
const catalogTimePrice = `[[models.cost.time_prices]]
label = "off-peak"
timezone = "UTC"
input_price_per_million = 0.01

[[models.cost.time_prices.windows]]
days = [
    "mon",
    "tue",
    "wed",
    "thu",
    "fri",
]
start = "00:00"
end = "12:00"

[[models.cost.time_prices.windows]]
days = [
    "mon",
    "tue",
    "wed",
    "thu",
    "fri",
]
start = "18:00"
end = "24:00"

[[models.cost.time_prices.windows]]
days = [
    "sat",
    "sun",
]
start = "00:00"
end = "24:00"
`

// TestPricesApplyStoresAScheduleOnce runs the openrouter flow twice through the
// registry writer, on the saved response as OpenRouter serves it in two
// different windows, and reads the file's bytes. The first apply writes the
// peak price and the schedule row, drops the row of a schedule that is gone,
// and leaves the user's rows and the catalog's off-peak row byte for byte.
// The second, fetched when every one of these models is in its other window,
// changes no price (which is what #322 asks: no update because the clock
// moved) and writes only the stamp.
func TestPricesApplyStoresAScheduleOnce(t *testing.T) {
	path := scratchRegistry(t, scheduleRegistry)
	apply := func(fetched, now time.Time) PricesApplied {
		t.Helper()
		api := apiOf(t, fetchedAt(t, fetched))
		var done PricesApplied
		if _, err := config.UpdateRegistry(func(d *config.RegistryDoc) error {
			var err error
			done, err = PlanPrices(Entries(d.Models()), api).Apply(d, now)
			return err
		}); err != nil {
			t.Fatalf("UpdateRegistry: %v", err)
		}
		return done
	}

	// Friday 02:45 UTC: deepseek's dear window.
	first := time.Date(2026, 10, 9, 2, 45, 0, 0, time.UTC)
	if done := apply(first, first); done != (PricesApplied{Stamped: 3, Changed: 3}) {
		t.Errorf("first apply = %+v, want 3 stamped, 3 changed", done)
	}
	const stamp1, stamp2 = "2026-10-09T02:45:00+00:00", "2026-10-10T17:00:00+00:00"
	want := `[[providers]]
id = "openrouter"
name = "OpenRouter"
location = "cloud"

[providers.auth]
type = "api_key"
secret_ref = "OPENROUTER_API_KEY"

[[models]]
id = "openrouter/deepseek--deepseek-v4-pro-0813"
family = "deepseek"
provider_id = "openrouter"
model_name = "deepseek/deepseek-v4-pro-0813"
location = "cloud"
pricing_updated_at = "` + stamp1 + `"

[models.cost]
input_price_per_million = 1.32
cache_price_per_million = 0.044
output_price_per_million = 3.9600000000000004

` + userTimePrice + `
[[models.cost.time_prices]]
label = "openrouter"
timezone = "UTC"
input_price_per_million = 0.66
cache_price_per_million = 0.022
output_price_per_million = 1.9800000000000002

[[models.cost.time_prices.windows]]
days = [
    "mon",
    "tue",
    "wed",
    "thu",
    "fri",
]
start = "00:00"
end = "01:00"

[[models.cost.time_prices.windows]]
days = [
    "mon",
    "tue",
    "wed",
    "thu",
    "fri",
]
start = "04:00"
end = "06:00"

[[models.cost.time_prices.windows]]
days = [
    "mon",
    "tue",
    "wed",
    "thu",
    "fri",
]
start = "10:00"
end = "24:00"

[[models.cost.time_prices.windows]]
days = [
    "sat",
    "sun",
]
start = "00:00"
end = "24:00"

[[models]]
id = "openrouter/tencent--hy3"
family = "hy"
provider_id = "openrouter"
model_name = "tencent/hy3"
location = "cloud"
pricing_updated_at = "` + stamp1 + `"

[models.cost]
input_price_per_million = 0.13199999999999998
cache_price_per_million = 0.032999999999999995
output_price_per_million = 0.5279999999999999

[[models.cost.time_prices]]
label = "openrouter"
timezone = "UTC"
input_price_per_million = 0.0825
cache_price_per_million = 0.020625
output_price_per_million = 0.33

[[models.cost.time_prices.windows]]
days = [
    "mon",
    "tue",
    "wed",
    "thu",
    "fri",
    "sat",
    "sun",
]
start = "16:00"
end = "24:00"

[[models]]
id = "openrouter/z-ai--glm-5.2"
family = "glm"
provider_id = "openrouter"
model_name = "z-ai/glm-5.2"
location = "cloud"
pricing_updated_at = "` + stamp1 + `"

[models.cost]
input_price_per_million = 0.06
cache_price_per_million = 0.059
output_price_per_million = 6.0

` + userTimePrice + `
` + catalogTimePrice
	if got := readFile(t, path); got != want {
		t.Fatalf("registry.toml after the first apply:\n%s\n\nwant:\n%s", got, want)
	}

	// Saturday 17:00 UTC: deepseek and hy3 are both in their cheap window, so
	// the response's top-level prices are not the ones stored.
	second := time.Date(2026, 10, 10, 17, 0, 0, 0, time.UTC)
	if h := headline(t, fetchedAt(t, second), "tencent/hy3"); h != "0.0000000825/0.000000020625/0.00000033" {
		t.Fatalf("the second response is not from hy3's other window: %s", h)
	}
	if done := apply(second, second); done != (PricesApplied{Stamped: 3, Changed: 0}) {
		t.Errorf("second apply = %+v, want 3 stamped and no price changed", done)
	}
	if got, want := readFile(t, path), strings.ReplaceAll(want, stamp1, stamp2); got != want {
		t.Errorf("registry.toml after the second apply:\n%s\n\nwant the first apply's file with the new stamp and nothing else:\n%s", got, want)
	}
	// What was written loads: the registry's validator accepts the rows.
	if _, err := config.Load(); err != nil {
		t.Errorf("config.Load after the applies: %v", err)
	}
}
```

- [ ] **Step 4: Run them to verify they fail**

Run (from `wt/`): `go test -count=1 ./internal/cloudsync`
Expected: the package does not build.

```text
# github.com/ohanaverse/local-ai-setup/wt/internal/cloudsync [github.com/ohanaverse/local-ai-setup/wt/internal/cloudsync.test]
internal/cloudsync/timeofday_rows_test.go:60:18: undefined: formatWindows
internal/cloudsync/timeofday_rows_test.go:72:23: undefined: formatWindows
…
FAIL	github.com/ohanaverse/local-ai-setup/wt/internal/cloudsync [build failed]
```

- [ ] **Step 5: Write the implementation**

Create `wt/internal/cloudsync/timeofday_rows.go`:

```go
package cloudsync

import (
	"fmt"
	"slices"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/tomlw"
)

// OpenRouterLabel is the label of the time_prices rows the openrouter flow owns:
// one per price level of a model's time-of-day schedule other than the one
// stored as the flat price. Rows with any other label are never touched by
// the openrouter flow.
const OpenRouterLabel = "openrouter"

// registryDays are the registry's names for the days of the week, Monday
// first: the order of Rate.Spans.
var registryDays = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

// rateRow is one price level as a time_prices row, keys in schema order.
// Days with the same times share their windows, and a day's windows are in
// time order, so the row does not depend on the order of OpenRouter's list.
func rateRow(r Rate) *tomlw.Table {
	row := tomlw.NewTable()
	row.Set("label", OpenRouterLabel)
	row.Set("timezone", "UTC")
	for _, kv := range []struct {
		key string
		v   *float64
	}{{"input_price_per_million", r.Input}, {"cache_price_per_million", r.Cache}, {"output_price_per_million", r.Output}} {
		if kv.v != nil {
			row.Set(kv.key, *kv.v)
		}
	}
	var windows []any
	done := [daysPerWeek]bool{}
	for d := range daysPerWeek {
		if done[d] || len(r.Spans[d]) == 0 {
			continue
		}
		var days []any
		for e := d; e < daysPerWeek; e++ {
			if slices.Equal(r.Spans[e], r.Spans[d]) {
				days, done[e] = append(days, registryDays[e]), true
			}
		}
		for _, span := range r.Spans[d] {
			w := tomlw.NewTable()
			w.Set("days", slices.Clone(days))
			w.Set("start", clock(span.Start))
			w.Set("end", clock(span.End))
			windows = append(windows, w)
		}
	}
	row.Set("windows", windows)
	return row
}

func clock(minutes int) string { return fmt.Sprintf("%02d:%02d", minutes/60, minutes%60) }

func isOpenRouterRow(row *tomlw.Table) bool { return str(row, "label") == OpenRouterLabel }

// withOpenRouterRows is existing with the openrouter flow's rows replaced by
// those for rates, where the first of them stood (time_prices resolve first
// match wins, so they are not moved), or at the end when there were none.
// With no rates the flow's rows are dropped: the model no longer has a
// schedule. Every other row is kept, in place; when there is nothing to
// replace and nothing to add, existing itself is returned.
func withOpenRouterRows(existing []*tomlw.Table, rates []Rate) []*tomlw.Table {
	at := -1
	var rows []*tomlw.Table
	for _, row := range existing {
		if !isOpenRouterRow(row) {
			rows = append(rows, row)
		} else if at < 0 {
			at = len(rows)
		}
	}
	if at < 0 {
		if len(rates) == 0 {
			return existing
		}
		at = len(rows)
	}
	for i, r := range rates {
		rows = slices.Insert(rows, at+i, rateRow(r))
	}
	return rows
}

// rowKeys and windowKeys are the keys the sync writes on one of its rows and
// on each of the row's windows.
var (
	rowKeys    = []string{"label", "timezone", "input_price_per_million", "cache_price_per_million", "output_price_per_million", "windows"}
	windowKeys = []string{"days", "start", "end"}
)

// formatOpenRouterRow prints one of the flow's rows for a plan line:
// ` (openrouter 0.66/0.022/1.98 mon-fri 00:00-01:00, sat-sun 00:00-24:00)`.
// Rows are compared whole, so a row someone edited is replaced even when its
// prices and windows are the sync's; what else can differ is therefore
// printed too, or the line would read the same on both sides: a timezone
// that is not UTC, and ` +keys` for a row or a window with a key the sync
// does not write.
func formatOpenRouterRow(row *tomlw.Table) string {
	text := fmt.Sprintf(" (openrouter %s/%s/%s", formatPrice(number(row, "input_price_per_million")),
		formatPrice(number(row, "cache_price_per_million")), formatPrice(number(row, "output_price_per_million")))
	if tz := str(row, "timezone"); tz != "UTC" {
		text += fmt.Sprintf(" timezone=%q", tz)
	}
	text += " " + formatWindows(row)
	extra := slices.ContainsFunc(row.Keys(), func(k string) bool { return !slices.Contains(rowKeys, k) })
	raw, _ := row.Get("windows")
	list, _ := raw.([]any)
	for _, item := range list {
		if w, ok := item.(*tomlw.Table); ok && slices.ContainsFunc(w.Keys(), func(k string) bool { return !slices.Contains(windowKeys, k) }) {
			extra = true
		}
	}
	if extra {
		text += " +keys"
	}
	return text + ")"
}

// formatWindows prints a row's windows for a plan line: `mon-fri 00:00-01:00
// 04:00-06:00, sat-sun 00:00-24:00`. It prints whatever the row holds, so a
// change to the windows always changes the plan's text; `?` stands for a
// windows value that is not a list and for an entry that is not a table.
func formatWindows(row *tomlw.Table) string {
	raw, has := row.Get("windows")
	list, isList := raw.([]any)
	if has && !isList {
		return "?"
	}
	var groups []string
	last := ""
	for _, item := range list {
		w, ok := item.(*tomlw.Table)
		if !ok {
			groups, last = append(groups, "?"), ""
			continue
		}
		days := formatDays(w)
		span := str(w, "start") + "-" + str(w, "end")
		if len(groups) > 0 && days == last {
			groups[len(groups)-1] += " " + span
			continue
		}
		groups, last = append(groups, days+" "+span), days
	}
	return strings.Join(groups, ", ")
}

// formatDays prints a window's days: a run of consecutive days as
// `mon-fri`, anything else as written, joined with commas.
func formatDays(w *tomlw.Table) string {
	raw, _ := w.Get("days")
	list, _ := raw.([]any)
	var names []string
	run := len(list) > 1
	for i, item := range list {
		name, _ := item.(string)
		names = append(names, name)
		if d := slices.Index(registryDays, name); d < 0 || d != slices.Index(registryDays, names[0])+i {
			run = false
		}
	}
	if run {
		return names[0] + "-" + names[len(names)-1]
	}
	return strings.Join(names, ",")
}
```

Two edits to `wt/internal/cloudsync/openrouter.go`.

(a) In `PlanPrices`, after the cache price is merged.

Find:

```go
			after.Cache = price.Cache
		}
```

Replace with:

```go
			after.Cache = price.Cache
		}
		after.TimePrices = withOpenRouterRows(after.TimePrices, price.Rates)
```

(b) The comment on `PlanPrices`: its first paragraph no longer says the rows are never touched.

Find:

```go
// reported one (it usually does not), and the subscription and time_prices
// are never touched, so a refresh never clears a price set by hand.
```

Replace with:

```go
// reported one (it usually does not). The subscription is never touched,
// and of the time_prices rows only the flow's own (OpenRouterLabel), so a
// refresh never clears a price set by hand.
```

Two edits to `wt/internal/cloudsync/catalog.go`.

(c) The end of `formatCost`.

Find:

```go
			break
		}
	}
	return text
```

Replace with:

```go
			break
		}
	}
	for _, row := range c.TimePrices {
		if isOpenRouterRow(row) {
			text += formatOpenRouterRow(row)
		}
	}
	return text
```

(d) The comment on `OffpeakLabel`.

Find:

```go
// OffpeakLabel is the label of the time_prices row the ollama flow owns.
// Rows with any other label are the user's and are never touched.
```

Replace with:

```go
// OffpeakLabel is the label of the time_prices row the ollama flow owns, on
// ollama cloud entries. It touches a row with no other label. (The
// openrouter flow's rows, OpenRouterLabel, are on openrouter models, which
// the ollama flow never plans from.)
```

- [ ] **Step 6: Run the tests to verify they pass**

Run (from `wt/`): `gofmt -l internal cmd && go vet ./internal/cloudsync ./cmd/wt && go test -count=1 -v ./internal/cloudsync -run 'TestScheduleRows|TestPlanPricesOwnsOnlyItsOwnRows|TestPricePlanFormatShowsASchedule|TestPlanPricesReplacesAnOpenRouterRowWhateverItHolds|TestPricesApplyStoresAScheduleOnce' | grep -E '^(---|ok|FAIL)'`
Expected:

```text
--- PASS: TestScheduleRows (0.00s)
--- PASS: TestPlanPricesOwnsOnlyItsOwnRows (0.00s)
--- PASS: TestPricePlanFormatShowsASchedule (0.00s)
--- PASS: TestPlanPricesReplacesAnOpenRouterRowWhateverItHolds (0.00s)
--- PASS: TestPricesApplyStoresAScheduleOnce (0.00s)
ok  	github.com/ohanaverse/local-ai-setup/wt/internal/cloudsync	(time)
```

Run: `go test -count=1 ./cmd/wt -run 'TestCloudSyncTimeOfDayPricesDoNotFollowTheClock|TestCloudSyncRowsOnlyUpdateLeavesTheRoutesAlone' -v | grep -E '^(---|ok|FAIL)'`
Expected:

```text
--- PASS: TestCloudSyncTimeOfDayPricesDoNotFollowTheClock (0.01s)
--- PASS: TestCloudSyncRowsOnlyUpdateLeavesTheRoutesAlone (0.01s)
ok  	github.com/ohanaverse/local-ai-setup/wt/cmd/wt	(time)
```

Run: `go test -count=1 ./internal/cloudsync ./cmd/wt`
Expected: both `ok`. `TestPlanPricesMergeRules`, `TestApplyWritesOnlyWhatThePlanShows`, `TestTheTwoFlowsNeverShareAModel` and Task 7's `TestPlanPricesReportsAScheduleThatComesAndGoes` pass unchanged: a model with no schedule and no `openrouter` row gets back the very list of rows it had.

- [ ] **Step 7: Pin what a route is priced from**

Decisions 18 and 28 rest on the route's `model_info` not reading the rows. That is today's behaviour; this test keeps it from changing unnoticed, and names the reason beside the end-to-end test of Step 1. Append to `wt/internal/litellm/entry_test.go` (its imports are already there):

```go
// TestPricingInfoReadsOnlyTheFlatPrices pins what a route's model_info is
// priced from: the cost table's flat prices, never a cost.time_prices row.
// `wt cloud-sync` stores a model's time-of-day schedule as such rows (#322)
// and its dearest level as the flat price, so the route carries that level
// whenever the sync ran, and a sync that changes only the rows leaves
// config.yaml as it was (no proxy restart).
func TestPricingInfoReadsOnlyTheFlatPrices(t *testing.T) {
	in, cache, out, cheap := 1.32, 0.044, 3.96, 0.66
	flat := config.ModelCost{InputPricePerMillion: &in, CachePricePerMillion: &cache, OutputPricePerMillion: &out}
	withRow := flat
	withRow.TimePrices = []config.TimePrice{{
		Label: "openrouter", Timezone: "UTC", InputPricePerMillion: &cheap, CachePricePerMillion: &cheap, OutputPricePerMillion: &cheap,
		Windows: []config.CostWindow{{Days: []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}, Start: "00:00", End: "24:00"}},
	}}
	want := []kv{
		{"input_cost_per_token", in / 1e6}, {"output_cost_per_token", out / 1e6},
		{"cache_creation_input_token_cost", cache / 1e6}, {"cache_read_input_token_cost", cache / 1e6},
	}
	for name, c := range map[string]config.ModelCost{"flat": flat, "with a row": withRow} {
		if got := pricingInfo(c); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: pricingInfo = %v, want %v", name, got, want)
		}
	}
}
```

- [ ] **Step 8: Run it**

Run (from `wt/`): `gofmt -l internal && go test -count=1 ./internal/litellm -run TestPricingInfoReadsOnlyTheFlatPrices -v | grep -E '^(---|ok|FAIL)'`
Expected (it passes at once; it pins existing behaviour):

```text
--- PASS: TestPricingInfoReadsOnlyTheFlatPrices (0.00s)
ok  	github.com/ohanaverse/local-ai-setup/wt/internal/litellm	(time)
```

- [ ] **Step 9: Commit**

From the repo root:

```bash
git add wt/internal/cloudsync/timeofday_rows.go wt/internal/cloudsync/timeofday_rows_test.go wt/internal/cloudsync/openrouter.go wt/internal/cloudsync/catalog.go wt/cmd/wt/cloudsync_test.go wt/internal/litellm/entry_test.go
git commit -m "fix(wt): cloud-sync stores a time-of-day schedule's other levels as its own time_prices rows (#322)"
```

### Task 9: The reference page, the skill, `wt/CLAUDE.md`, the CHANGELOG (slice D)

`wt/CLAUDE.md` says: change the page and the skill with the behavior, because both quote the command's messages. One code comment changes; no code. The picker has applied `cost.time_prices` rows since slice C, so these docs say that the rows this slice stores are shown, and four sentences slice C wrote about "ollama's row" now name OpenRouter's too.

**Files:**
- Modify: `wt/docs/wt-cloud-sync.md`, `wt/.claude/skills/cloud-sync/SKILL.md`, `wt/CLAUDE.md`, `wt/CHANGELOG.md`, `docs/guides/06-wt-agents-and-models.md`, `wt/internal/config/config.go` (the comment on `TimePrice`)

**Interfaces:**
- Consumes from Tasks 7 and 8: the warning text `Could not use OpenRouter's time-of-day pricing for <id>: <reason>`; the label `openrouter`; the plan-line ending ` (openrouter <in>/<cached>/<out> <windows>)` with its `timezone="…"` and `+keys` marks; the names `parseSchedule`, `mainRate`, `withOpenRouterRows`, `OpenRouterLabel`, `OffpeakLabel`, `TestPlanPricesDoesNotFollowTheClock`, `TestPlanPricesReportsAScheduleThatComesAndGoes`, `TestPricingInfoReadsOnlyTheFlatPrices`. From Task 2: `TestTheTwoFlowsNeverShareAModel`.
- Consumes from Task 6: the section "The price the picker shows" of `wt-cloud-sync.md` and its anchor; and the four sentences named under its "Produces", which are "find" texts here (Steps 3, 7, 8 and 9).
- Produces: nothing a later task reads.

If a "find" text is not in the file, re-read the paragraph and make the same change to what is there.

- [ ] **Step 1: `wt/docs/wt-cloud-sync.md`, the openrouter section: what is stored**

Find:

```markdown
- Input and output prices always take OpenRouter's value. The cache price is
  replaced only when OpenRouter reports one. A subscription price and any
  `time_prices` row are never touched.
```

Replace with:

```markdown
- Input and output prices always take OpenRouter's value. The cache price is
  replaced only when OpenRouter reports one. A subscription price is never
  touched, and neither is a `time_prices` row, except the rows labelled
  `openrouter`, which are this flow's own (below).
- **A model OpenRouter prices by time of day** is stored whole, and the
  same way whatever hour the sync runs in. The price at the top of such a
  model's entry in OpenRouter's list is the one in force when the list is
  fetched; wt does not read it. It reads the model's time-of-day entries
  (`pricing.overrides`, the ones with `utc_start`, `utc_end` or
  `utc_days`), which give every window of the week in every response.
  **The dearest level** of that schedule, the one with the highest output
  price, is stored as the model's price, and **each cheaper level is a
  `[[models.cost.time_prices]]` row labelled `openrouter`**: its prices,
  `timezone = "UTC"`, and the windows it applies in. A window that crosses
  midnight is written as two, and the end of a day as `24:00`. So for as
  long as OpenRouter publishes the same schedule the plan and the registry
  are the same whenever the sync runs, and a second run in another window
  reports no update for the model.
- **The plan line shows the schedule**: the price, then each stored level
  with its windows, for example (illustrative) `<id>: 0.5/0.05/2 ->
  1/0.1/4 (openrouter 0.5/0.05/2 mon-fri 00:00-08:00 20:00-24:00, sat-sun
  00:00-24:00)`. If the price on the left is one of the levels in the
  brackets, an earlier sync stored the window it ran in; OpenRouter did not
  re-price the model.
- **The `openrouter` rows are the sync's.** On a model of the `openrouter`
  provider it replaces them where they stand and removes them when the
  model no longer has a schedule. A row with that label that is not
  exactly what the sync writes is replaced; the plan line prints its
  timezone when that is not UTC and `+keys` when it has a key the sync
  does not write. A row with any other label is kept as it is. (The ollama
  flow's `off-peak` rows are on ollama cloud entries, which this flow never
  touches: no model is in both flows.)
- **What uses which price.** LiteLLM's route carries the model's price,
  the dearest level, at every hour: a sync that changes only the rows
  leaves `config.yaml` as it was and restarts nothing. LiteLLM logs
  OpenRouter's own cost figure for a request where it reads one and prices
  the request from the route where it does not; for those requests, in the
  hours a cheaper level applies, the spend `wt stats` shows is higher than
  what was charged. It is not lower as long as the dearest level is also
  the highest on input and cached input, which wt does not check. (Which
  requests are priced from the route depends on the API the agent speaks
  and on the LiteLLM version. With LiteLLM 1.103.1 it was the Responses
  API, which codex speaks, measured against a stand-in for OpenRouter and
  not the live service.) The model picker applies the rows: it shows, and
  sorts by, the level in force when it opens ([The price the picker
  shows](#the-price-the-picker-shows)).
- **A price that depends on the size of the prompt** (an entry with
  `min_prompt_tokens`) is not a schedule. The model's listed price is
  stored, and the price for larger prompts is not stored anywhere.
- **A price can still differ between two syncs** for a reason that is not
  the clock. OpenRouter lists each model at the price of one of its
  providers, and for a model that many providers serve that price can move
  within minutes. If the provider it lists changes, a model can also gain
  or lose its schedule. wt stores what is listed, so each of these is
  reported as a price update.
```

- [ ] **Step 2: The same section: the new warning**

Find:

```markdown
  OpenRouter's pricing for …`; OpenRouter gives `-1` for a price that
  varies) is left exactly as it is, and is not stamped.
```

Replace with:

```markdown
  OpenRouter's pricing for …`; OpenRouter gives `-1` for a price that
  varies) is left exactly as it is, and is not stamped. So is a model whose
  time-of-day entries wt cannot read with certainty (`warning: Could not
  use OpenRouter's time-of-day pricing for <id>: <reason>`): windows that
  do not cover the whole week, an entry with a condition wt does not know,
  a window with no prompt or completion price. Its listed price would be
  that of the current window, so it is not used. The warning comes back on
  every run until OpenRouter's list or wt's parser
  (`wt/internal/cloudsync/timeofday.go`) changes; until then the model
  keeps the price and the rows it had, which may be the price of whichever
  window an earlier sync ran in.
```

- [ ] **Step 3: The ollama section (whose the other rows are) and the picker section (whose rows it applies)**

Find:

```markdown
  from the page. Rows with any other label are yours and are kept. The
  model picker applies the row: outside ollama's peak hours it shows the
  off-peak price ([The price the picker
  shows](#the-price-the-picker-shows)). LiteLLM's route carries the peak
  price at every hour.
```

Replace with:

```markdown
  from the page. Rows with any other label are not this flow's and are
  kept. The model picker applies the row: outside ollama's peak hours it
  shows the off-peak price ([The price the picker
  shows](#the-price-the-picker-shows)). LiteLLM's route carries the peak
  price at every hour.
```

Find:

```markdown
- This holds for every row, whoever wrote it: the ollama flow's `off-peak`
  row (the model's own price is the peak one) and a row you wrote by hand.
```

Replace with:

```markdown
- This holds for every row, whoever wrote it: the ollama flow's `off-peak`
  row (the model's own price is the peak one), the openrouter flow's
  `openrouter` rows (the model's own price is the dearest level), and a row
  you wrote by hand.
```

- [ ] **Step 4: "What it reads and writes"**

Find:

```markdown
  every key it was not asked to change. openrouter: the cost keys that moved,
  and `pricing_updated_at`. ollama: the same on ollama cloud entries, plus
```

Replace with:

```markdown
  every key it was not asked to change. openrouter: the cost keys that moved,
  the `openrouter` rows of `cost.time_prices` (on a model priced by time of
  day), and `pricing_updated_at`. ollama: the cost keys that moved and
  `pricing_updated_at` on ollama cloud entries, plus
```

- [ ] **Step 5: The skill, step 1: how to read a schedule line, and the new warning**

Two edits to `wt/.claude/skills/cloud-sync/SKILL.md`.

Find:

```markdown
   - Summarize **both** plans for the user. openrouter: each `id: old -> new`
     under `Price updates`. ollama: `Price updates`, `Registry additions`
```

Replace with:

```markdown
   - Summarize **both** plans for the user. openrouter: each `id: old -> new`
     under `Price updates`. A line that ends `(openrouter <prices>
     <windows>)` is a model OpenRouter prices by time of day: the price
     before the brackets is the dearest level of its schedule, which is
     what is stored as the model's price whatever hour the sync runs in,
     and each bracket is a cheaper level, stored with its UTC windows as a
     `cost.time_prices` row labelled `openrouter`. If the old price on such
     a line is one of the bracketed levels, tell the user that an earlier
     sync stored the window it ran in and OpenRouter did not re-price the
     model. ollama: `Price updates`, `Registry additions`
```

Find:

```markdown
   - Read out every `warning:` line: an OpenRouter-priced model with no
     match or no usable price, ollama cloud entries that disagree on
```

Replace with:

```markdown
   - Read out every `warning:` line: an OpenRouter-priced model with no
     match or no usable price, one whose time-of-day pricing could not be
     used (`Could not use OpenRouter's time-of-day pricing for <id>:
     <reason>`; it keeps its price and its rows and is not stamped, and the
     fix is in wt's parser, `wt/internal/cloudsync/timeofday.go`, not in
     the registry), ollama cloud entries that disagree on
```

- [ ] **Step 6: `wt/CLAUDE.md`, the "Cloud sync" section**

The find text is the last sentence of the bullet that begins "**The `Apply` methods are pure and write a diff**"; the replacement rewrites it and adds one bullet after it.

Find:

```markdown
 Only the `off-peak` row of `cost.time_prices` is the ollama flow's; every other row, the subscription and any key wt does not model are kept.
```

Replace with:

```markdown
 Of the `cost.time_prices` rows, the `off-peak` one is the ollama flow's (`OffpeakLabel`, on ollama cloud entries) and the `openrouter` ones are the openrouter flow's (`OpenRouterLabel`, `withOpenRouterRows` in `timeofday_rows.go`: one row per level of a schedule other than the flat price, on models of the `openrouter` provider); every other row, the subscription and any key wt does not model are kept. No row is ever in both flows' scope (`cloudsync.OpenRouterPriced` is `provider_id == "openrouter"`, the ollama planner addresses only `provider_id == "ollama"`; `TestTheTwoFlowsNeverShareAModel`), which is what lets the one shared write apply two plans made from the same reading of the registry. Keep it true when either rule changes.
- **A model OpenRouter prices by time of day is planned from its schedule, never from its top-level price or the clock** (#322). The top-level `prompt`/`completion`/`input_cache_read` of such a model are those of the window in force when the list was fetched. `parseSchedule` (`internal/cloudsync/timeofday.go`) reads the `pricing.overrides` entries that have a `utc_` key into price levels over the UTC week, by painting each entry onto the week's 10,080 minutes in list order (a later entry wins, a minute left unpainted makes the schedule unusable); `mainRate` is the one place that says which level becomes the flat price (the one with the highest output price), and the others become the `openrouter` rows. An entry with no `utc_` key (a `min_prompt_tokens` tier) is ignored, and a schedule that cannot be read with certainty is a warning that leaves the model alone, never a fall back to the top-level price. `TestPlanPricesDoesNotFollowTheClock` holds the plan to one text across a week of fetch times for one published schedule; a planner that read `time.Now` would also fail the compare under the lock. A schedule that appears or goes between two syncs is a real update (`TestPlanPricesReportsAScheduleThatComesAndGoes`). A route's `model_info` is priced from the flat price only (`TestPricingInfoReadsOnlyTheFlatPrices`), so a rows-only update rewrites no route.
```

- [ ] **Step 7: `wt/CHANGELOG.md`: the entry under `## Unreleased` / `### Fixed`, and one bracket of slice C's entry under `### Changed`**

Find:

```markdown
### Fixed

- `wt litellm sync` on a registry with one model id on two rows (which every
```

Replace with:

```markdown
### Fixed

- `wt cloud-sync` no longer stores, for a model OpenRouter prices by time of
  day, whichever price was in force when the sync ran (#322). Such a model
  used to flip between its rates from one sync to the next, each flip
  reported as a price update, written to LiteLLM's routes and able to
  restart the proxy. The openrouter flow now reads the model's whole
  schedule from OpenRouter's list. It stores the dearest level (the one
  with the highest output price) as the model's price, and each cheaper
  level, with its UTC windows, as a `[[models.cost.time_prices]]` row
  labelled `openrouter`; the plan line shows them: `<id>: <old> -> <price>
  (openrouter <level> <windows>)`. A sync run in another window reports no
  update. The first sync after this change shows one update for each such
  model; when the price itself does not move, LiteLLM's `config.yaml` is
  left as it is and the proxy is not restarted. The label is the sync's: a
  row labelled `openrouter` on a model of the `openrouter` provider is
  replaced or removed, and rows under any other label are kept as they
  are. A price that depends on prompt size (`min_prompt_tokens`) is not a
  schedule and is read as before. A schedule wt cannot read with certainty
  is a `warning: Could not use OpenRouter's time-of-day pricing for <id>:
  <reason>` and the model is left as it is. LiteLLM's route carries the
  model's price, the dearest level, at every hour; the model picker shows
  the level in force when it opens, marked `~` (the entry under Changed).
  Not changed: a model that several providers serve can still show a
  different price from one sync to the next, because OpenRouter lists the
  price of one of them and that price moves. Reference:
  `docs/wt-cloud-sync.md`.
- `wt litellm sync` on a registry with one model id on two rows (which every
```

Find:

```markdown
  `cost.time_prices` rows (ollama's off-peak row, or rows written by hand),
  the COST column shows, and the cost sort uses, the prices of the row
  whose timezone and windows hold the current instant, and the model's own
  price when none does. It used to show the model's own price at every
  hour, which for an ollama cloud model is the peak price. Such a price is
  marked `~`, and the column heading then reads `COST (~ varies by time)`.
```

Replace with:

```markdown
  `cost.time_prices` rows (ollama's off-peak row, the rows `wt cloud-sync`
  stores for a model OpenRouter prices by time of day, or rows written by
  hand), the COST column shows, and the cost sort uses, the prices of the
  row whose timezone and windows hold the current instant, and the model's
  own price when none does. It used to show the model's own price at every
  hour, which for an ollama cloud model is the peak price (and for a model
  OpenRouter prices by time of day would be its dearest level). Such a
  price is marked `~`, and the column heading then reads `COST (~ varies
  by time)`.
```

- [ ] **Step 8: The comment on `config.TimePrice`**

In `wt/internal/config/config.go`:

Find:

```go
// per-token prices. The rows are written by `wt cloud-sync`'s ollama flow
// (ollama's off-peak pricing); a user may write more by hand.
// ModelCost.PriceAt (price_at.go) applies them, and the model picker shows
// what it returns. A LiteLLM route carries the flat prices. What a row
// means: the first row whose window contains an instant wins, per field,
// falling back to the flat prices. A row the registry's validator refuses
// is not applied (TimePrice.Problem).
```

Replace with:

```go
// per-token prices. The rows are written by `wt cloud-sync`: the ollama
// flow's row labelled off-peak (ollama's off-peak pricing, on ollama cloud
// entries) and the openrouter flow's rows labelled openrouter (the cheaper
// levels of a model OpenRouter prices by time of day, whose flat price is
// the dearest level); a user may write more by hand. ModelCost.PriceAt
// (price_at.go) applies them, and the model picker shows what it returns.
// A LiteLLM route carries the flat prices. What a row means: the first row
// whose window contains an instant wins, per field, falling back to the
// flat prices. A row the registry's validator refuses is not applied
// (TimePrice.Problem).
```

- [ ] **Step 9: `docs/guides/06-wt-agents-and-models.md`**

Inside the long paragraph that describes the picker's table (the find text is part of one line).

Find:

```markdown
(`cost.time_prices` rows, which `wt cloud-sync` writes for ollama's off-peak hours)
```

Replace with:

```markdown
(`cost.time_prices` rows, which `wt cloud-sync` writes for ollama's off-peak hours and for the models OpenRouter prices by time of day)
```

- [ ] **Step 10: Verify the slice**

Run (from `wt/`): `test -z "$(gofmt -l .)" && go vet ./... && go test -count=1 ./... && make check`
Expected: every package `ok` (25), and `make check` ends without an error.

Run (from the repo root): `make check-links`
Expected: `ALL LINKS OK`.

Run (from the repo root): `make test-all`
Expected: it ends without an error. (Not run while writing this plan.)

Run (from the repo root): `grep -rn "time_prices. row are never touched\|are yours and are kept\|ollama's off-peak row, or rows written" wt/docs wt/.claude wt/CLAUDE.md wt/CHANGELOG.md`
Expected: no output (the sentences the rows made untrue or incomplete are gone).

- [ ] **Step 11: Commit**

From the repo root:

```bash
git add wt/docs/wt-cloud-sync.md wt/.claude/skills/cloud-sync/SKILL.md wt/CLAUDE.md wt/CHANGELOG.md docs/guides/06-wt-agents-and-models.md wt/internal/config/config.go
git commit -m "docs(wt): cloud-sync stores a time-of-day model as its schedule; the page, the skill and the changelog say how (#322)"
```

### Task 10 (owner-gated; run by the controller, not by an implementer subagent): one live dry run from a copy of the registry, then the PR that closes #322

**When:** after Task 9 is committed, before slice D's PR is opened.

**What it does:** one `wt cloud-sync --only openrouter --dry-run` of the built binary against the live OpenRouter list, from a copy of the registry. It reads the real registry once (to copy it) and one public URL once. **It changes nothing real**: a dry run writes nothing, and the run is under `env -i` with a throwaway home, so it cannot reach the real registry, `config.toml` or `config.yaml`, or restart the proxy. No `ollama` command runs (`--only openrouter`).

**Why one run is enough here.** That the plan does not follow the clock is proved offline, on the saved response as OpenRouter serves it at 336 instants of a week, and was seen live on both sides of a window boundary with a made-up registry (facts 20 and 36). What only the owner's registry can show is which of its models are time-priced, what the first real sync will change, and whether OpenRouter publishes a shape Task 7 refuses.

**Files:** none are changed in the repo.

- [ ] **Step 0: Get the owner's OK before reading anything real**

Tell the owner what this task reads: their `~/.config/local-ai/registry.toml` (copied once into a temp directory, never printed as a file) and `https://openrouter.ai/api/v1/models` (once). Tell them what its output contains: the plan lines name the registry's `openrouter` model ids and their prices. **Stop here until the owner says to go on.**

- [ ] **Step 1: Build, and make the scratch home**

From `wt/`, on the branch at the Task 9 commit:

```bash
LIVE="$(mktemp -d)"
go build -o "$LIVE/wt" ./cmd/wt
mkdir -p "$LIVE/home/.config/local-ai"
REG="$LIVE/registry.toml"
cp ~/.config/local-ai/registry.toml "$REG"
cp "$REG" "$LIVE/registry.before"
live() { env -i PATH=/usr/bin:/bin HOME="$LIVE/home" XDG_CONFIG_HOME="$LIVE/home/.config" WT_REGISTRY="$REG" \
  WT_LITELLM_CONFIG="$LIVE/litellm.yaml" WT_LITELLM_RESTART_CMD=true "$LIVE/wt" "$@"; echo "exit=$?"; }
```

The first `cp` is the one read of the real registry; nothing prints it. (If the owner's shell redirects the registry with `WT_REGISTRY`, copy that file instead.) These commands were run as written in scratch, with a made-up registry in place of the copy: fact 36.

- [ ] **Step 2: The dry run**

```bash
date -u '+%A %H:%M UTC' | tee "$LIVE/run.time"
live cloud-sync --only openrouter --dry-run | tee "$LIVE/dry.out"
cmp "$LIVE/registry.before" "$REG" && echo "registry copy unchanged"
```

Expected: `exit=0`; `registry copy unchanged`. For each registry model OpenRouter prices by time of day (on 2026-10-09 those were `deepseek/deepseek-v4-pro-0813`, `tencent/hy3` and `tencent/hy4-preview`), one line under `Price updates` whose right-hand side is the dearest level followed by its rows, whatever hour this runs in. For `openrouter/deepseek--deepseek-v4-pro-0813`, which the issue says the registry holds at the rate the 02:45 UTC apply stored:

```text
openrouter:   openrouter/deepseek--deepseek-v4-pro-0813: 1.32/0.044/3.96 -> 1.32/0.044/3.96 (openrouter 0.66/0.022/1.98 mon-fri 00:00-01:00 04:00-06:00 10:00-24:00, sat-sun 00:00-24:00)
```

and, if the registry holds the cheap rate instead, the same line starting `0.66/0.022/1.98 -> 1.32/0.044/3.96 (openrouter …`. (Unless OpenRouter has changed the schedule since.) Other models in the registry may show ordinary updates; those are the multi-provider prices of question 3, not this fix.

Note for the PR: every `warning:` line and whether it is true. A `Could not use OpenRouter's time-of-day pricing for …` warning means OpenRouter publishes a shape Task 7 refuses: stop and report it, with that model's `pricing` object fetched from the public list, before opening the PR.

- [ ] **Step 3: Show the owner, then the PR**

Give the owner `dry.out` and the time. With their OK, from the repo root:

```bash
rm -rf "$LIVE"
git push -u origin fix/322-cloud-sync-time-of-day-prices
gh pr create --repo ohanaverse/local-ai-setup --base main --title "fix(wt): cloud-sync stores a time-of-day model as its schedule, not as the hour it ran in (#322)" --body-file - <<'EOF'
Closes #322. Fourth of four PRs (plan: docs/superpowers/plans/2026-10-09-cloud-sync-time-of-day-prices.md).

For a model OpenRouter prices by time of day, the top-level price in its model list is the price of the window in force when the list is fetched (OpenRouter documents this), so `wt cloud-sync` stored whichever window it ran in and reported an update whenever the clock had crossed a window.

The openrouter flow now reads the model's whole schedule from `pricing.overrides` (every response carries it in full). It stores the dearest level as the model's price and each cheaper level, with its UTC windows, as a `[[models.cost.time_prices]]` row labelled `openrouter`; the plan line shows them. Nothing reads a clock, so for one published schedule the plan is the same text whenever it is made, and a second sync in another window changes no price and does not sync the routes.

- Entries that are prompt-size tiers (`min_prompt_tokens`, most of the models that carry `overrides`) are not schedules and are read as before.
- A schedule that cannot be read with certainty is a warning and the model is left alone.
- The label is the sync's: a row labelled `openrouter` on an openrouter model is replaced or removed; rows under any other label are untouched.
- The first sync after this lands shows one update per time-priced model. When the price itself does not move, `config.yaml` keeps its bytes and the proxy is not restarted (tested through the real route sync).
- LiteLLM's route carries the model's price, the dearest level, at every hour. The model picker (the PR before this one) applies the rows: it shows, and sorts by, the level in force when it opens.

Not fixed here, and the larger share of "the price changed between two syncs": OpenRouter lists each model at the price of one of its providers, and for models many providers serve that price moves within minutes. A sync still reports those.

Live check: one `--dry-run` from a copy of the registry, RUN UTC: RESULT.
EOF
```

Before running it, replace `RUN` with the time Step 2 printed and `RESULT` with what it showed, in one or two sentences (for example: "three time-priced models, each one update to its schedule; no warning"). Add the session's PR attribution line at the end of the body if your session is told to add one. Put the dry run's raw output in the PR only if the owner says so: it names the registry's models.

With the owner's OK (question 3), also post this on the issue, so its text is not read as fact later:

```bash
gh issue comment 322 --repo ohanaverse/local-ai-setup --body-file - <<'EOF'
Three corrections to the description, found while planning the fix:

- 85 models carried an `overrides` key, but only 3 of them are priced by time of day. The other 82 carry `min_prompt_tokens`, a price tier for large prompts, and their listed price does not move with the clock.
- `utc_start` / `utc_end` and the listed price are documented by OpenRouter (Models guide, "Pricing Overrides"): HHMM in UTC, start inclusive, end exclusive, wrapping past midnight when the end is not after the start; and for a time-priced model the top-level price is by definition the window in force when the response is generated.
- The three models without `overrides` that also changed price are a different cause, and the more frequent one: the listed price is that of one provider endpoint, and it moves within minutes (one model's listed output price moved about 42 times between two fetches five minutes apart on 2026-10-09). The fix for this issue does not change that.

Whether the stored price reaches a spend figure: with LiteLLM 1.103.1, requests by chat completions or `/v1/messages` were logged at OpenRouter's own cost figure, and requests by the Responses API at the route's price. That was measured against a stand-in for OpenRouter, not the live service. Inside wt the stored price is what the model picker shows and sorts on.
EOF
```

If the owner said yes to the follow-up (question 3), file it with the text of that question and facts 7 and 23. It is proposed here, not filed by this plan.

After slice D merges the owner runs the real sync themselves, with no redirects: `wt cloud-sync --only openrouter --dry-run`, then `wt cloud-sync --only openrouter`. Each time-priced model shows one update; the routes are synced once and, if a route's price changed, the proxy restarts once.

---

## Spec Coverage

The owner's directions:

| Direction | Where |
|---|---|
| D1: the flows are `openrouter` and `ollama`, in `--only`, the prefixes, `--help`, messages, docs, the skill, tests and code identifiers that name a flow; no aliases | Task 1 (its script's part 3c for the tests); decisions 1 to 3; fact 44 |
| D1: openrouter "just refresh the prices for cloud models that are configured"; ollama "sync the cloud models locally with those at ollama.com" and "refresh prices for all cloud models" | The openrouter flow after Task 2 (the models of the `openrouter` provider) and Tasks 7 and 8; the ollama flow is what `catalog` already did, unchanged but for its name (Task 1) |
| D2: the openrouter flow stores each time-priced model's schedule as `cost.time_prices` rows | Tasks 7 and 8, one PR; decisions 9 to 19 |
| D3: the picker shows the price in force now, for OpenRouter's rows, ollama's `off-peak` rows and hand-written rows | Tasks 3 to 5, landing before the rows (decision 33); decisions 20 to 26, and 30 to 32 for a hand-written row that breaks a rule or has a value of the wrong type |
| D3: each other place that shows a price, decided | Decision 27 and fact 38: `wt model list` and its `--json` (no price; they gain only the name of a row that is not applied), the Models tab (no price), the model form (the flat price it edits, pinned) |
| D3: the route cost stays the flat price, and that is stated | Decision 28; `TestPricingInfoReadsOnlyTheFlatPrices`; Tasks 6 and 9 (the page, the changelog) |
| D4: `openrouter_priced` removed; the rule is `provider_id == "openrouter"`; every reader traced; a registry that still carries the key | Task 2; decisions 4 to 7; facts 29 and 31 |

What the directions left this plan to settle:

| Consequence | Where |
|---|---|
| The flat price of a time-priced model, and what the picker shows | Decision 9; "The Design and Why"; `TestPriceAtAnOpenRouterSchedule` |
| The row label for OpenRouter's rows | Decision 15 (`openrouter`) |
| How a time-dependent price is marked without breaking the fit rules | Decisions 25 and 26 (`~` after the COST cell, `cost~` on the mode line, a price never cut); `TestModelPickerWithTimePricedModelsFitsTheTerminal`, `TestModelPickerNamesTheHighlightedModelsPrice`; question 1 |
| Sort stability | Decisions 22 and 23 |
| What clock the picker uses and how tests inject it | Decision 22 (`pickerNow`, `tableInput.now`) |
| DST and IANA timezones for user-written rows | Decision 21; fact 33 (ollama's rows are UTC); decisions 30 to 32 and question 2 for a row that breaks a rule |
| A window boundary while the picker is open | Decision 24 (re-resolved on open only); `TestAnOpenPickerKeepsThePricesItOpenedWith` |
| Both flows writing rows on one model in one write | Decision 8; fact 30: impossible, proved by rule and by test; the workaround and its test are deleted |

The issue:

| The issue asks | Where |
|---|---|
| Option 1: "Store the overrides. Write them as `cost.time_prices` rows … and keep one fixed rate as the entry's main price." | Tasks 7 and 8 |
| Option 2: "Always store one fixed rate, chosen by rule (for example the highest window …)" | Task 7; decision 9 |
| Option 3: "note in `wt/docs/wt-cloud-sync.md` … and print a `prices: warning:`" | Task 9 documents what is stored. No warning is printed for a schedule that was read; a warning names only a model whose schedule could not be (decision 14). A warning keyed on "has overrides" would name 82 models wrongly (fact 1). |
| "a sync should not report a price update for such a model only because the clock moved into another window" | Decision 10: `TestPlanPricesDoesNotFollowTheClock`, `TestCloudSyncTimeOfDayPricesDoNotFollowTheClock`, `TestPricesApplyStoresAScheduleOnce`. Scoped to one published schedule: `TestPlanPricesReportsAScheduleThatComesAndGoes`. |
| Not established: "Whether the stored price affects any spend figure" | Facts 9 to 12; the live answer is still unknown |
| Not established: "The exact meaning of `utc_start` / `utc_end` … and whether the headline price is defined as 'the current window'" | Facts 2 and 3: documented by OpenRouter |
| Not established: "Why three other models with no `overrides` … also changed price" | Fact 7: a different and larger cause; the proposed follow-up of question 3; not fixed here |

This plan's own requirements:

| Requirement | Where |
|---|---|
| Tests never touch the network: a saved OpenRouter response and a fixed clock | Task 7 Step 1 (the fixture); `cloudFetch` and `cloudSyncNow` stubbed in Tasks 1, 2, 7 and 8; `pickerNow` and `tableInput.now` in Tasks 4 and 5 |
| The same registry and response planned at two clock times give byte-identical plan text and an empty second apply | `TestCloudSyncTimeOfDayPricesDoNotFollowTheClock` |
| Rows other writers own are byte-identical after an apply | `TestPricesApplyStoresAScheduleOnce` (the whole file compared; the user's row twice and an `off-peak` row) |
| Every wt screen fits 40/80/120 by 12/24/50 | `TestModelPickerWithTimePricedModelsFitsTheTerminal` (both pickers, two instants); `TestModelPickerNamesTheHighlightedModelsPrice`; the existing `TestEveryListPhaseFitsTheTerminal`; Task 6 Step 9 on a pty |
| Each slice builds, passes and is documented alone | "PR Slices"; the last steps of Tasks 1, 2, 6 and 9 |
| A live check is one `--dry-run` from a copy of the registry, with the owner's OK | Task 10 |
| The PR that lands slice D, the last one, says `Closes #322`; the correcting comment and the follow-up need the owner's OK | Task 10 Step 3; question 3 |

## After This Plan

- The follow-up issue about listed prices that move within minutes (question 3), if the owner has it filed.
- A price column that survives at 80 columns (question 1, option (c)): a narrower cell, such as the output price alone. Not asked for, not planned.
- Reading a window that ends before it starts as one that runs past midnight (question 2) would be a change to the registry's validator first, and to every reader of the rows after it.
- If spend should follow the clock one day: LiteLLM 1.103.1's `model_info.off_peak_pricing` holds one standard rate and one off-peak rate with UTC windows (fact 14), so a two-level schedule in UTC rows would carry over and a three-level one would not. Its first step is one real codex request to learn whether any spend is route-priced at all (fact 11). Not asked for, not planned.
- A price column in `wt model list`, or a line in the Models tab's detail block, would use `config.ModelCost.PriceAt` and `Flat` as the picker does; a JSON form would carry both, under new keys.
- The `loading worktrees...` status that `wt --cwd --agent <name>` leaves above the picker (seen in the captures) is worth its own look.
