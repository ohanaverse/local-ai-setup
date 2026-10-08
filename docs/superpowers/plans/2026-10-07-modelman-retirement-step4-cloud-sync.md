# Modelman Retirement, Step 4 (`wt cloud-sync`) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** One wt command, `wt cloud-sync`, replaces `modelman refresh-prices` and `modelman ollama-catalog sync`: it refreshes OpenRouter prices and mirrors `ollama.com/pricing` (cloud models, prices, pulls and removals) into `registry.toml`, ollama and the LiteLLM routes, with the catalog's whole safety net ported and the stale-price notice derived from the registry instead of `modelman.toml`.

**Architecture:** A new pure package, `wt/internal/cloudsync`, holds everything that decides what a sync changes: the pricing-page parser (on `golang.org/x/net/html`'s tokenizer), cloud-tag resolution, the catalog planner with its mass-removal guard and removal digest, the OpenRouter price planner, and two `Apply` methods that change a `config.RegistryDoc` and nothing else. `cmd/wt` owns the I/O around it: the fetches, the `ollama` CLI pinned with `OLLAMA_HOST`, the confirmation on `/dev/tty` (opened through the existing `openTTY` seam), one `config.UpdateRegistry` that both flows share (each plan is made again under the lock and applied only if it still prints as it did), one route sync, and a typed exit-code error so this one command can exit 2 to 5. The stale-price notice reads the newest `pricing_updated_at` among OpenRouter-priced models; `config.PriceRefreshLastRun` is deleted.

**Tech Stack:** Go 1.26.7 (module root `wt/`), cobra, BurntSushi/toml through `internal/tomlw`, `net/http`, `os/exec` to `ollama`. **One new direct dependency: `golang.org/x/net` v0.59.0** (it raises `golang.org/x/sys` v0.38.0 → v0.48.0 and the indirect `golang.org/x/text` v0.3.8 → v0.42.0). No Python changes.

**Spec:** [docs/superpowers/specs/2026-10-06-modelman-retirement-design.md](../specs/2026-10-06-modelman-retirement-design.md), section "Step 4 — wt cloud-sync" (the binding authority), read with its Decisions, End state, Step 2 (the registry writer this builds on), Step 3 (so nothing here duplicates it), Testing, Risks and Step 6. This plan covers Step 4 only. Step 4 needs Step 2, which is merged. It does not need Step 3; "If Step 3 Lands First" below is the one place that says what changes if Step 3 merges before a slice of this plan.

**Verified against:** `origin/main` at `37acee8` (2026-10-07; it includes #302, the provider `openrouter_priced` override, which this plan honours). Every code block below was built in a scratch copy of that commit and the tasks were replayed there one at a time: each task's failing run, its passing run, and the wt verification at the end of each PR (`gofmt`, `go vet`, the full suite, `make check`) were observed, as were the two scratch runs of the built binary (Tasks 9 and 11). `make test-all` and `make check-links` were run on the tree as it stands at the end of each of the four PRs (after Tasks 5, 9, 11 and 12), under a throwaway `HOME` with a stand-in `ollama` first on `PATH`. If `origin/main` has moved, the "find" text of an edit block may no longer match: re-read the file and apply the same change.

## Global Constraints

- The command, exactly: `wt cloud-sync [--only prices,catalog] [--dry-run] [--html FILE] [--yes] [--approve-removals DIGEST] [--force]`.
- Exit codes, exactly:

  | Code | Meaning |
  |---|---|
  | 0 | Every selected flow finished or had nothing to do |
  | 1 | A step failed in either flow, or a usage error |
  | 2 | Catalog changed nothing: page fetch failed, `--html` unreadable, ollama tags unreadable, or no cloud tag resolved |
  | 3 | Catalog changed nothing: page shape changed; HTML saved |
  | 4 | Catalog changed nothing: mass removal refused without `--force` |
  | 5 | Catalog changed nothing: removals not approved |

  Codes 2 to 5 describe the catalog flow only and take precedence over 1. Every other wt command keeps exit 1.
- The flows are independent: a refused catalog plan does not stop the prices flow. Output lines are prefixed `prices:` or `catalog:`. `--html`, `--approve-removals` or `--force` with `--only prices` is a usage error.
- **Sequence:** fetch and plan both flows with no lock held; print both plans; gate (confirmation on `/dev/tty`, digest, `--force`); apply registry changes in one `UpdateRegistry`, re-planning under the lock and refusing if the plan changed; run `ollama pull` and `ollama rm`; one route sync.
- `ollama pull` and `ollama rm` go through the **ollama CLI pinned with `OLLAMA_HOST`** to the provider row's address, never the HTTP API.
- A registry entry is removed in the registry write and its tag removed afterwards. A tag is removed only when no remaining registry ollama entry names it. A failed `ollama rm` leaves a stray tag the next run removes.
- The pricing page is parsed with `golang.org/x/net/html`'s **Tokenizer** (not `html.Parse`). The parser keeps every fail-loud check and saves the raw HTML on a shape change.
- **Nothing new is stored for the last-refresh date.** The notice derives it from the newest `pricing_updated_at` among OpenRouter-priced models. The prices flow stamps every model it matched, including those whose price did not change. The route sync runs only when a price or the model set changed (this plan adds one case, argued in the decisions table: or when the catalog flow had a tag to pull or remove). The notice fires when the date is more than 7 days old, or absent, and names `wt cloud-sync`. `config.PriceRefreshLastRun` and its read of `modelman.toml` are removed in this step.
- Refresh is manual only. wt never fetches prices on a launch.
- `cost.time_prices` rows are still written by the catalog flow and preserved by every other write. Nothing applies them. The resolver `price_at` is not ported.
- **No code under `modelman/` changes.** `modelman refresh-prices` and `modelman ollama-catalog sync`, their code and their tests stay until Step 6. The only two files touched there are the skill, which moves, and the one line of `modelman/CLAUDE.md` that points at it (both in PR 4).
- The skill `modelman/.claude/skills/ollama-catalog` moves to `wt/.claude/skills/cloud-sync` and is rewritten, and the root `CLAUDE.md` pointer changes in the same PR.
- **Never, in any step of this plan:** run `ollama pull`, `ollama rm` or any command that changes the developer's ollama store; read, write or print the developer's real `~/.config/local-ai/registry.toml`, `modelman.toml`, `~/.config/agent-wt/config.toml` or `~/.config/litellm/config.yaml`; print an API key. Any run of a built `wt` (or of modelman) uses a throwaway `HOME` and `XDG_CONFIG_HOME`, `WT_LITELLM_CONFIG` naming a scratch file, `WT_LITELLM_RESTART_CMD=true`, and an `ollama` stand-in first on `PATH` that records its argv and environment. The one exception is Task 13, a dry run, which the controller runs only after the owner's OK (its Step 0) and which no implementer subagent runs: it copies the real registry once and runs the real `ollama list`, and changes nothing real.
- **No live calls in tests.** ollama, OpenRouter and `ollama.com` are reached through seams (`cloudFetch`, `ollamaCLI`, `confirmCloudSync`), and `cmd/wt`'s `TestMain` makes each fail closed. The real prompt behind `confirmCloudSync` opens the terminal through `openTTY`, which the tests that run it replace. The only network a test touches is an `httptest` server on loopback; the only process is a shell script in a temp directory on a temp `PATH`.
- Every `Test*` function has a top-level `//` comment saying what it tests and why a regression matters to a user.
- Tests never touch the developer's machine. A new package whose tests reach `config.UpdateRegistry` calls `config.IsolateConfigHomeForTest` from its `TestMain` and asserts `config.RegistryWriteGuardArmed()` in one test.
- Every wt command that writes the registry has a test under a redirected registry with and without `WT_LITELLM_CONFIG` (Task 9).
- Run every Go command from `wt/`, never the monorepo root. A commit step says which directory its `git add` paths are relative to: the repo root.
- Guides never embed live model state: every example in a doc is labelled illustrative.
- Per slice, from `wt/`: `test -z "$(gofmt -l .)" && go vet ./... && go test -count=1 ./... && make check`. At the end of each slice, from the monorepo root: `make test-all` and `make check-links`.
- Commit messages follow the repo's conventional style (`feat(wt): …`, `test(wt): …`, `docs(wt): …`), with the attribution trailer your session is told to add, if any.
- Work happens on a feature branch off `main`, one branch per PR slice. **Pushing a branch and opening a PR need the owner's OK.** Do not push, and do not run `gh pr create`, until the owner says so.
- Read `wt/CLAUDE.md`, `wt/docs/internals/config-and-registry.md` and `wt/docs/internals/testing.md` before starting.

## Review Focus

1. **`ollama.com/pricing` answers and `ollama.com/library` does not (an outage, a rate limit, one model's page gone).** Expected: an outage is never read as "these models are gone". A model whose tag could not be read keeps its entry and its prices, nothing is added or pulled under a guessed tag, and no entry or pulled stub that may be it is removed. Only when not one name resolves does the catalog flow stop (exit 2). Pinned in Task 3 (`TestPlanUnresolvedModelKeepsItsEntryButSkipsPullAndAdd`, `TestPlanUnresolvedModelProtectsItsPulledStubAndSizedEntry`) and Task 11 (`TestCloudSyncCatalogWithTheLibraryDown`, and the "no cloud tag resolves" case of `TestCloudSyncCatalogChangesNothingAndSaysWhy`).
2. **The command run from an agent's shell, or a script: no terminal, and `--yes` with a plan that deletes.** Expected: without `--yes` nothing is applied, exit 1, and the message names `--yes`; with `--yes` and no digest, or another plan's digest, the catalog changes nothing (exit 5) and prints the digest to use, while the prices flow in the same run is still applied. `--force` never stands in for the digest. Pinned in Task 9 (`TestCloudSyncAsksOnceAndTakesNoForAnAnswer`, whose no-terminal half runs the real `promptCloudSync` with `openTTY` failing, so the message asserted is the one the code produces; `TestPromptCloudSyncAsksOnTheTerminalAndDefaultsToNo`) and Task 11 (`TestCloudSyncCatalogChangesNothingAndSaysWhy`, `TestCloudSyncFlowsAreIndependent`, and the "no terminal" case of `TestCloudSyncBothFlowsInOneRun`).
3. **A trial run against a copy of the registry (`WT_REGISTRY` or `XDG_CONFIG_HOME` redirected), and a shell that exports another `OLLAMA_HOST`.** Expected: the copy is written; the real proxy's `config.yaml` is not touched unless `WT_LITELLM_CONFIG` names it (a warning, exit 0); and every `ollama` command goes to the daemon the registry's ollama provider row names, whatever the shell exports. Pinned in Task 9 (`TestCloudSyncUnderARedirectedRegistry`) and Task 10 (`TestRealOllamaCLIPinsTheDaemon`).
4. **A registry that hand edits or another tool left in a state the sync did not expect: a row it must change has lost a required key; `pricing_updated_at` is a TOML date-time or a typo; another program writes the file while the user reads the plan; there is no ollama provider row.** Expected: the flow that must change the broken row is not written at all, with the row named, and the other flow still is (exit 1); the odd stamp never fails a registry load, and one typed years ahead does not silence the notice; a plan that is no longer the one printed is not applied (prices: exit 1; catalog: exit 5) and the other program's change survives; with no ollama provider row the plain command skips the catalog flow and exits as the prices flow says, and the catalog asked for by name stops before fetching (exit 1). Pinned in Task 5 (`TestCatalogApplyRefusesARowThatWouldNotLoad`), Task 6 (`TestModelPricingUpdatedReadsEverySpelling`), Task 7 (`TestLastPriceRefresh`), Task 9 (`TestCloudSyncReportsARefusedRegistryWrite`, `TestCloudSyncPricesRefusesWhenTheRegistryChangedAfterThePlan`) and Task 11 (`TestCloudSyncWritesEachFlowAloneWhenARowWouldNotLoad`, `TestCloudSyncCatalogRefusesAPlanThatChangedUnderTheLock`, `TestCloudSyncSkipsTheCatalogOnARegistryWithoutOllama`, `TestCloudSyncCatalogNeedsAnOllamaProviderRow`).
5. **What the two services really send, beyond the happy path: OpenRouter prices of `"-1"` ("varies"), an error page instead of the model list, a price cell in a new format, a page with a column added, markup the old parser read in its own way.** Expected: a `-1` price is a warning and the model is left as it is, never minus one million in the registry and never a refused write; an error page is one error line, not a "no match" warning per model; one odd cell keeps the registry's price and warns; most cells odd, or a header gone, is exit 3 with the HTML saved; and the Go parser reads the fifteen markup cases Task 1 pins as modelman's did, a table inside `<noscript>` and a CDATA section among them (one input is known to be read differently and is not pinned; the porting table names it). Pinned in Task 1 (`TestParsePricingFailsLoudly`, `TestParsePricingUnknownCellWarns`, `TestCollectTablesReadsMarkupAsModelmanDid`), Task 4 (`TestParseOpenRouter`, `TestParseOpenRouterRejectsAnotherShape`, `TestPlanPricesCandidatesAndWarnings`) and Task 9 (`TestCloudSyncPricesFailuresExit1`).
6. **A run that dies half way, an `ollama rm` that fails, and a row that says `location = "cloud"` over a local tag.** Expected: the run after an interrupted one (Ctrl-C during a pull, after the registry write) finishes the pulls and removals and syncs the routes, although it changes no registry row; a failed `ollama rm` is an error line that says the tag is left pulled and that the next run needs a new dry run, because the digest may have changed; and a removed entry whose tag is not a cloud tag loses its registry row, with a warning in the plan, and its tag is never handed to `ollama rm`. Pinned in Task 3 (`TestPlanWarnsWhenARemovedEntrysTagIsNotACloudTag`), Task 5 (`TestCatalogApplyNeverRemovesALocalTag`) and Task 11 (`TestCloudSyncFinishesAnInterruptedRun`, `TestCloudSyncCatalogOllamaFailuresExit1AndTheRestStillRuns`).

---

## Decisions This Plan Makes

The spec states the behaviour; these are the choices it leaves open. Each is pinned by a test named below, so a reviewer who disagrees changes one place.

> **Owner amendment, 2026-10-08 (binding; overrides the table, the tasks and every sample text below where they differ).** The owner approved this plan's decisions with one change. `wt cloud-sync` is two syncs, one per provider, and a sync whose provider the registry does not use is skipped, never an error:
>
> - **No `ollama` provider row:** the catalog flow is skipped in every case, also when it was asked for by name (`--only catalog`, `--html`, `--approve-removals`, `--force`). It prints `catalog: no ollama provider in the registry; nothing to mirror` on stdout, fetches nothing, runs no `ollama` command, and contributes exit 0. There is no exit 1 refusal for this case: Task 11's `TestCloudSyncCatalogNeedsAnOllamaProviderRow` is rewritten to pin the skip for each by-name spelling (rename it to say so), and the `catalogNamed` plumbing goes if nothing else needs it. A usage error (a catalog flag with `--only prices`) is still a usage error.
> - **No OpenRouter-priced model:** the prices flow is skipped (`prices: no OpenRouter-priced model in the registry; nothing to refresh`, nothing fetched, exit 0) and the stale-price notice stays silent. The plan already does both; keep them and keep their tests. "OpenRouter-priced" is `config.Config.OpenRouterPriced`, not the presence of an `openrouter` provider row, so a model another provider prices through OpenRouter still counts.
> - **Neither:** `wt cloud-sync` prints the two lines, fetches nothing, asks nothing, writes nothing, runs no route sync and exits 0. Task 11 adds a test for exactly this registry (`TestCloudSyncWithNeitherProviderDoesNothing`), also under `--yes` and under `--dry-run`.
> - The skill, `wt/docs` command reference and guide text of Task 12 describe this rule and not the by-name refusal.

| Question | Decision | Why | Pinned by |
|---|---|---|---|
| What does "refusing if the plan changed" compare? | The printed text. Under the lock each flow is planned again from the file as it then is; if `Format()` differs from what was printed, that flow is not applied. Catalog: exit 5. Prices: exit 1. A change elsewhere in the file that leaves a plan as printed does not refuse it. | What the user approved is the text, so the text is the identity of a plan. modelman was looser: it applied a changed re-plan and refused only when it deleted more than was shown (`ollama_catalog_cli.py:155-171`). Both planners are deterministic, which is what makes the comparison safe (`TestPlanCatalogIsDeterministic`). | Task 9, `TestCloudSyncPricesRefusesWhenTheRegistryChangedAfterThePlan`; Task 11, `TestCloudSyncCatalogRefusesAPlanThatChangedUnderTheLock` |
| Must the removal digest equal modelman's? | Yes, byte for byte: first 12 hex digits of SHA-256 over the sorted removals then the sorted `rm <tag>` lines, newline-joined. | Until Step 6 a digest printed by one tool's dry run may be passed to the other's `--approve-removals`. Compared in scratch on the same inputs (see "What Was Measured"). | Task 3, `TestRemovalDigestMatchesModelman` (vectors computed with modelman) |
| How does the planner read the registry before the lock? | A new read-only `config.ReadRegistryDoc()`: the same `RegistryDoc` a write gets, no lock, no write, the same refusals; a missing registry is `ErrRegistryMissing`. | `RegistryDoc` could only be had inside `UpdateRegistry`, which takes the lock and creates a missing file, so a dry run would not be a dry run. The planners need keys `config.Model` does not model (`catalog_name`, the `time_prices` rows whole). | Task 6, `TestReadRegistryDocIsAReadOnlyView`, `TestReadRegistryDocRefusesWhatAWriteWouldRefuse` |
| How is `ollama list` read? | Through the same pinned CLI as pull and rm (`ollama list`, NAME column), not `/api/tags`. | One mechanism and one seam for everything the catalog does with ollama, and a straight port of `list_ollama_tags`. wt's inventory probe cannot serve: `ollamaModelNames` (`internal/localmodels/sources.go:19`, the filter at `:43`) drops every entry with a `remote_host`, because a cloud stub is not a local model, and cloud stubs are exactly what this flow lists. | Task 10, `TestOllamaTagsReadsTheNameColumn`; Task 11, `TestCloudSyncCatalogDryRun` |
| When is a removed entry's tag `ollama rm`'d? | Only when `ollama list` showed it and no remaining ollama entry names it. | The spec's rule, plus "was pulled": there is nothing to remove for a tag ollama never had. modelman ran `ollama rm` regardless and read "not found" as done. | Task 5, `TestCatalogApplyWritesWhatThePlanSays`, `TestCatalogApplyNeverRemovesATagARemainingEntryNames` |
| What does a re-tagged entry carry over? | Everything: it is `CloneModel` of the old row, then patched. Tags and any key wt does not model survive. | The spec gives `CloneModel` this job. modelman rebuilt the entry and kept family, extras, `model_info` and cost, but dropped `tags` (`ollama_catalog.py:570-585`); that is the one difference found when both tools applied the same plan. | Task 5, `TestCatalogApplyWritesWhatThePlanSays` |
| What does the confirmation default to? | No, always; only `y`/`yes` on `/dev/tty` applies. Declined: exit 0, nothing changed. No terminal: exit 1, naming `--yes`. A plan with nothing to apply is not asked about. The prompt opens the terminal through `openTTY` (`cmd/wt/launch.go:77`), the seam `promptProfile` already uses. | wt's rule for every prompt (`promptStop`, `cmd/wt/model_cmds.go:45`; `askYesNo`, `cmd/wt/start.go:152`). modelman defaulted to Yes when the plan deleted nothing. Through the seam the real prompt can be run by a test: without it the refusal could be tested only where there is no terminal, and would block where there is one. | Task 9, `TestPromptCloudSyncAsksOnTheTerminalAndDefaultsToNo` (the real prompt on a stand-in terminal), `TestCloudSyncAsksOnceAndTakesNoForAnAnswer` (the real refusal, `openTTY` failing); Task 11, `TestCloudSyncCatalogDeclinedChangesNothing` |
| How are the route sync's own lines shown? | Prefixed `routes:`, what it wrote to stdout on stdout and what it wrote to stderr (a model it could not route) on stderr; a warning is `routes: warning:` on stderr. A failed sync never changes the exit status. | The spec prefixes the flows' lines; the sync belongs to neither, and an unprefixed `id: routed` among prefixed lines reads as a third thing. Same contract as `wt model init`: the write succeeded. | Task 9, `TestCloudSyncPrefixesTheRouteSyncsLines`, `TestCloudSyncUnderARedirectedRegistry`; Task 11, `TestCloudSyncCatalogApplyUnderTheApprovedDigest` |
| When is the route sync skipped? | When no price changed, no model was added or removed, and the catalog flow had no tag to pull or remove: a stamp-only run, and a run with nothing to do. | The spec skips it for a stamp-only run. The third condition is this plan's. The sync is the run's last step, after pulls that may take minutes, so a run interrupted after its registry write leaves the routes behind the registry; the run that finishes it changes no registry row, and judged by the registry alone would exit 0 with `config.yaml` still stale. A sync that leaves `config.yaml` as it is does not restart the proxy (checked on the built binary: a second `wt litellm sync` left the file byte-identical and did not run the restart command), so the extra sync costs nothing. Not covered: a run interrupted after its last pull and before the sync; the skill says to run `wt litellm sync` after any interrupted run. modelman synced after every catalog run, even one that changed nothing (`test_sync_with_nothing_queued_still_syncs_routes`); that is still not ported. | Task 9, `TestCloudSyncStampOnlyRunSkipsTheRouteSync`; Task 11, `TestCloudSyncFinishesAnInterruptedRun`, second half of `TestCloudSyncCatalogApplyUnderTheApprovedDigest` |
| What if the registry has no `ollama` provider row? | Run by default (no `--only`, no catalog flag) the catalog flow has nothing to mirror: it prints `catalog: no ollama provider in the registry; nothing to mirror` on stdout, fetches nothing, and leaves the exit status to the prices flow. Asked for by name it is skipped the same way, exit 0 (owner amendment above; the plan first made this an exit 1 refusal). `wt cloud-sync` never seeds. | A new entry names provider `ollama`; with no such row `Config.Validate` then refuses the whole registry, and there is no daemon address to pin the CLI to. `wt model init` seeds the row only when a model references it, an agent lists it, or `ollama` is on `PATH` (`internal/config/registry_seed.go:147-150`, `:199-205`), so a machine that uses only OpenRouter never has it; that is the machine the stale-price notice sends to `wt cloud-sync`, and exit 1 there on every run would be a failure with no repair. Exit 0 is "every selected flow finished or had nothing to do". By name it is exit 1, not 2: nothing about the catalog's inputs was unreadable. The spec gives seeding exactly two callers (`wt model init`, `wt model add`), and a third would hide a setup step. | Task 11, `TestCloudSyncSkipsTheCatalogOnARegistryWithoutOllama`, `TestCloudSyncCatalogNeedsAnOllamaProviderRow`; observed on the built binary (Task 11, Step 8) |
| An OpenRouter price that is negative or not a number? | That model is not refreshed, with one warning. | OpenRouter lists `"-1"` for router models (seven of the 467 in the response saved on 2026-10-07). modelman got there by accident: `Cost()` raised on the negative value and `apply_prices` turned that into a warning. Written through, wt's own cost validation would refuse the whole registry write. | Task 4, `TestParseOpenRouter`, `TestPlanPricesCandidatesAndWarnings` |
| How is `pricing_updated_at` typed on `config.Model`? | `any`, read through `Model.PricingUpdated()`, which accepts the string both tools stamp, a TOML date-time, a date-time with no offset and a bare date. Anything else is "not said". | A `string` field would turn a hand-typed TOML date-time into a load error that stops every launch, over a value only the notice reads. modelman's reader accepts anything there. | Task 6, `TestModelPricingUpdatedReadsEverySpelling` |
| The notice's exact rule? | Speaks when `now - newest stamp > 7×24h` (exactly 7 days is quiet), or when no OpenRouter-priced model has a usable stamp. The date is shown in the reader's time zone. Stamps on ollama cloud models do not count. A stamp up to a day ahead of the clock counts, and is fresh; one further ahead is skipped like a malformed one. | "More than 7 days old, or absent." The catalog flow stamps ollama cloud models too; counting them would let a catalog-only run silence a notice about prices it never refreshed. Two machines' clocks differ by minutes, not days: a stamp far ahead is a typo (a year typed as 2062), and counted it would silence the notice until that date. | Task 7, `TestPriceNoticeThresholds`, `TestPriceNoticeShowsTheLocalDate`, `TestLastPriceRefresh` |
| What does `--only` accept in PR 2, before the catalog flow exists? | Only `prices`, and that is also the default. PR 3 adds `catalog` and makes the default both. | No flag that does nothing ships, and no "not implemented" path. | Task 9 and Task 11, `TestSelectFlows` |
| Which config errors stop the command? | Only a config that could not be loaded (`a.loadErr`), with `configError`'s hint. A validation gap does not. | The rule `wt litellm` follows. The registry itself is read again through `ReadRegistryDoc`, and a refresh may be what the user is doing about the gap. | Task 9, `TestCloudSyncCommandRefusals` |
| `--approve-removals` without `--yes`? `--force` without a digest? | The first is ignored and the question is asked (modelman's behaviour). The second passes the mass-removal gate only; the digest is still required under `--yes`. | The digest exists for the run nobody is watching. `--force` answers one question: is this many removals right? | Task 11, `TestCloudSyncCatalogAsksWhenADigestComesWithoutYes`; `TestCloudSyncCatalogChangesNothingAndSaysWhy` ("--force does not stand in for the digest") |
| Does a dry run of a mass removal fail? | No: exit 0. It only prints. Exit 4 is the apply's. | modelman's order (`ollama_catalog_cli.py:128-137`). The skill's flow is dry run, then decide. | Task 11, `TestCloudSyncCatalogForceAppliesAMassRemoval` |
| When does "no cloud tag resolved" fire? | Only when not one page name resolved. A name that pins its size (`gpt-oss:120b`) always resolves without a lookup, so with one on the page the flow goes on and the planner protects every unresolved model. | A faithful port (`ollama_catalog_cli.py:115`). The protection, not the exit code, is what keeps an outage from deleting anything; Review Focus 1. | Task 11, `TestCloudSyncCatalogWithTheLibraryDown` |
| What does a new entry's row hold? | `id`, `family`, `provider_id`, `model_name`, `location = "cloud"`, `source = "curated"`, `tags = []`, `pricing_updated_at`, `catalog_name`, and its cost. No empty `[models.model_info]`. | The spec: `model_info` is written only when a caller passes it. modelman writes the empty table; its next save would add it. | Task 5, `TestCatalogApplyWritesWhatThePlanSays` (the whole row is in the expected text) |
| Is a missing cost table the same as an empty one? | Yes, for "did anything change". | modelman called a missing table and an empty one different, so an entry with no cost and a page row with no prices was an "update" once. wt cannot write an empty table through `PatchModel`, and has no reason to. | Task 3, second half of `TestPlanUnchangedWhenPricesMatchAndNameRecorded` (an entry with no cost against a page row with no prices) |
| What does the final error line say? | `wt: cloud-sync: the catalog flow changed nothing: <why>` for 2 to 5, `wt: cloud-sync: a step failed; see the error lines above` for 1. The command sets `SilenceUsage` and `SilenceErrors`, so main prints it once. | Every other command prints its error twice (cobra's `Error:` and main's `wt:`) and dumps usage; for a command whose output is read by a skill, one line. | Task 9, `TestCloudSyncPricesFailuresExit1` (the exit 1 text); Task 11, `TestCloudSyncCatalogChangesNothingAndSaysWhy` (the text for each of 2 to 5); observed on the built binary (Task 11, Step 8) |
| Timeouts? | 30 s per HTTP fetch, 32 MiB per body; `ollama list` 30 s, `ollama rm` 60 s, `ollama pull` 10 min. Library tag lookups run 8 at a time. | modelman's 30/30/60 and its 8 workers. A cloud model's pull fetches a manifest, not weights. | Task 10 (constants) |
| Where is a refused page saved? | `$TMPDIR/ollama-pricing-<YYYYMMDD-HHMMSS>.html`, mode 0600, local time. | modelman's name (`save_failed_html`), so the skill's repair section reads the same. | Task 11, `TestCloudSyncCatalogChangesNothingAndSaysWhy` ("the page changed shape") |
| Do the guides change in this step? | Yes, where this step makes them wrong or leaves the new command unfindable: guide 00 (what wt reads of `modelman.toml`; who writes the registry), 02 and 04 (name `wt cloud-sync`). The full rewrite stays in Step 6. | The notice now tells people to run `wt cloud-sync`; a guide that still sends them only to `modelman refresh-prices` contradicts the tool. | Tasks 7, 9, 11, 12; `make check-links` |
| Does `cloudsync` honour a provider's `openrouter_priced`? | Yes, in both directions, after the native check, exactly as `config.Config.OpenRouterPriced` does (#302). | The prices flow and the notice must agree about which models are OpenRouter-priced. | Task 4, `TestOpenRouterPricedMatchesTheContract` (the shared contract fixture), `TestPlanPricesCandidatesAndWarnings` |
| Which registry operation writes `cost.time_prices`? | `PatchModel`, given the rows as the `*tomlw.Table`s they already are, with the off-peak row inserted or replaced among them. Not `RegistryDoc.SetTimePrices`, which Step 2 built for this. | `SetTimePrices` takes `[]map[string]any`, and a map carries no key order, so a row the user wrote could not come back as it was written. Handed back as the table it was read as, a row is found equal (`tomlw.Same`) and not rewritten: its keys, their order, an unknown key and an integer price all survive, in both flows. `SetTimePrices` is left with no production caller; see "After Step 4". | Task 5, `TestPricesApplyStampsEveryMatchedModel`, `TestCatalogApplyWritesWhatThePlanSays` (the `userTimePrice` row, byte for byte) |
| What if the one registry write is refused because a row would not load? | With one flow pending, that flow is not written (exit 1), as before. With both pending, each flow is then written on its own: the flow that owns the broken row reports it and is not written, the other is. | The spec's "one `UpdateRegistry`" holds for every run that succeeds; this is the failure path only. The spec also says the flows are independent, and without this one ollama entry that lost its `family` keeps every OpenRouter price stale on each combined run (and the reverse), with nothing saying that `--only prices` would have worked. The writer validates the rows a write touches, so the flow that does not touch the broken row goes through. | Task 11, `TestCloudSyncWritesEachFlowAloneWhenARowWouldNotLoad`; one flow: Task 9, `TestCloudSyncReportsARefusedRegistryWrite` |
| Is a removed entry's tag always handed to `ollama rm`? | Only when it is a cloud tag (`IsCloudTag`). An entry counts as an ollama cloud entry by `location = "cloud"` or by its tag, as in modelman, so a row marked cloud over a local tag is still removed from the registry when the page does not list it; the plan then carries a `warning:` naming the entry and saying its tag is left in ollama. The removal line itself and the digest are unchanged. | modelman ran `ollama rm` on whatever `model_name` the removed row held; for a mislabelled row that deletes real weights, under a command whose help says local models are never touched. What the user approves is an id line, so the tag has to be safe by rule, not by review. The line and the digest stay as they are so that both tools still print and approve the same plan. | Task 3, `TestPlanWarnsWhenARemovedEntrysTagIsNotACloudTag`; Task 5, `TestCatalogApplyNeverRemovesALocalTag` |
| What does a failed `ollama rm` leave the user with? | The error line, then `catalog:   <tag> is left pulled; the next run lists it as a stray tag — start again from --dry-run, since the removal digest may have changed`. Exit 1. Pending pulls are not run when a removal gate refuses: exit 4 and 5 mean the catalog changed nothing. | The tag's entry is already gone, so the next plan lists the tag as a stray and its digest is another one: re-running the same approved command would exit 5, with nothing having said why. | Task 11, `TestCloudSyncCatalogOllamaFailuresExit1AndTheRestStillRuns` |
| Which command does the stale-price notice name? | `wt cloud-sync`, with no flags, as the spec words it. | On a machine with no ollama provider row the plain command now refreshes the prices and exits 0. On a machine with the row and no daemon it exits 2, with the prices applied in the same run and the `prices:` lines saying so; naming `--only prices` instead would steer every user away from the catalog flow for that one case. | Task 7, `TestPriceNoticeThresholds` (the wording) |
| What does the live check (Task 13) run? | One `wt cloud-sync --dry-run` against the real services from a copy of the registry, compared with modelman's dry run on the same copy. No apply: no real `ollama pull` or `ollama rm`. | An apply from a scratch registry changes the real ollama store and not the real registry, and the only ways back are a second, real run or copying the scratch registry over the real one, which bypasses `UpdateRegistry`'s lock and discards whatever was written to the real registry in between. What an apply does is proven end to end with a stand-in `ollama` (Task 11, Step 8); what only the real services can show (the page, the tag lookups, OpenRouter's list, `ollama list`) is all in the dry run. The first real apply is the owner's own `wt cloud-sync` after the merge, which writes the real registry through the lock. | Task 13 |

## What Is Ported From Python

Read `modelman/src/modelman/ollama_catalog.py`, `ollama_catalog_cli.py`, `pricing.py` and `main.py:691-724` (`refresh_prices`) before starting. What moves and what does not:

| Python | Go | Notes |
|---|---|---|
| `ollama_catalog.py:127-164`, `_TableCollector` (`html.parser`) | `cloudsync.collectTables` | Same reading of the page, quirks included, on `x/net/html`'s Tokenizer. Fifteen markup cases pinned against the Python collector's output (Python 3.13.15 and 3.14.8 give the same). Two of them need the tokenizer told what Python's parser does by itself: `<noscript>` content is markup, not raw text (`NextIsNotRawText`; a pricing table in a `<noscript>` fallback would otherwise be "no table"), and a CDATA section is skipped whole (`AllowCDATA`). One input is read differently and is not pinned: a `<script>` holding a comment that holds another `<script>…</script>` (the tokenizer keeps the inner `</script>` as text, Python ends the script there). |
| `:166-178`, `_map_columns`; `:179-257`, `parse_pricing` | `mapColumns`, `ParsePricing` | Every check kept: no table, no header match, fewer than `MinRows` (5) rows, a short row, an empty or non-tag model name, a duplicate row, an orphan off-peak row, more than half the price cells unrecognized. `CatalogParseError.check` is `ParseError.Check`. |
| `:260-268`, `cloud_tag`, `is_cloud_tag` | `CloudTag`, `IsCloudTag` | Same. |
| `:272-293`, `verified_tags` | `VerifiedTags` | Same, including the two-entries-claim-one-name rule. |
| `:295-345`, `_lookup_cloud_tag`, `resolve_cloud_tags` | `lookupCloudTag`, `ResolveCloudTags` | Same rule and the same 8 workers. An unknown tag is `""` where Python had `None`. |
| `:354-394`, `SyncPlan`, `mass_removal`, `removal_digest` | `CatalogPlan`, `MassRemoval`, `RemovalDigest` | Digest byte-identical. `pulls` carry the tag as well as the id. |
| `:404-422`, `_find_entry`; `:424-461`, `_merged`, `_with_catalog_prices`; `:463-488`, `_family_for`, `_shared_subscription` | `findEntry`, `merged`, `withCatalogPrices`, `familyFor`, `sharedSubscription` | Same. The off-peak row is replaced where it stands. |
| `:490-597`, `plan_sync` | `PlanCatalog` | Same, line for line. `resolved=None` ("trust the guess") is a nil map; the command never passes one. |
| `:599-609`, `apply_sync`, plus the removals modelman did through `run_queued_ops` | `CatalogPlan.Apply` | On a `RegistryDoc`: `PatchModel` for updates, `AddModel` or `CloneModel` for additions, `RemoveModel` for removals, in that order. Removal is in the same write; the tag goes afterwards, and only when it is a cloud tag (modelman removed whatever tag the row held). |
| `:611-665`, `_fmt`, `format_plan` | `formatCost`, `CatalogPlan.Format` | Same text, byte for byte, except in two kinds of `warning:` line: a parser warning quotes its cell with Go's `%q`, where Python used `repr`; and wt adds a warning modelman does not have, for a removed entry whose tag is not a cloud tag. `{v:g}` is `strconv.FormatFloat(v, 'g', 6, 64)`. |
| `:667-694`, `list_ollama_tags`, `remove_ollama_tag` | `cmd/wt`: `ollamaTags`, `ollamaRemove` | Pinned with `OLLAMA_HOST`, which modelman did not do. |
| `:104-125`, `fetch_pricing_html`, `save_failed_html` | `cmd/wt`: `cloudFetch`, `saveFailedHTML` | One fetch helper for all three hosts. |
| `ollama_catalog_cli.py:51-204`, `sync` | `cmd/wt`: `planCatalogFlow`, `catalogGate`, `runCloudSync`, `runOllamaWork` | Exit codes 2 to 5 keep their meanings. Stricter re-plan rule; no sync when nothing changed; see the decisions table. |
| `pricing.py:32-68`, `_per_token_to_per_million`, `_cost_from_api_entry`; `:117-127`, `fetch_openrouter_pricing` | `perMillion`, `ParseOpenRouter` | Same arithmetic (`float(value) * 1_000_000`), so both tools write the same digits. |
| `:71-115`, `_is_openrouter_priced` | `cloudsync.OpenRouterPriced` (rows) beside the existing `config.Config.OpenRouterPriced` | Pinned by the same contract fixture. |
| `:129-150`, `_merge_api_cost`; `:153-187`, `apply_prices` | `PlanPrices`, `PricePlan.Apply` | Same merge. A plan and a dry run are new: modelman applied without showing anything. |
| `main.py:691-724`, `refresh_prices` | `planPricesFlow`, `runCloudSync` | modelman skipped the save and the stamp when nothing matched; so does wt (a plan with no matched model has no work). |
| `pricing.py:211-219`, `should_run_price_refresh`; `state.py`, `price_refresh_last_run` | not ported | The daily gate fed an automatic refresh wt does not have, and the date is derived. |
| `time_pricing.py:187-203`, `price_at`; `Window.contains`, `TimePrice.matches` | not ported | No production caller (spec, "Not ported"). The validators were ported in Step 2 (`internal/config/registry_validate.go:219-294`). |
| `ollama_catalog.py:80-97`, `offpeak_time_price` | `offpeakRow` | The same three windows, as a table. |
| Legacy `cost.kind` rows | not migrated | modelman rewrote them on every save, so a modelman-written registry has none. wt validates them (Step 2) and leaves them alone. |

**Tests ported, by Python name.** From `modelman/tests/test_ollama_catalog.py`: `test_parse_fixture_happy_path` → `TestParsePricingFixture`; `test_columns_found_by_header_not_position` → `TestParsePricingFindsColumnsByHeader`; `test_offpeak_suffix_variants_fold_into_base` → `TestParsePricingFoldsOffpeakRowsIntoTheirBase`; the seven `*_raises` tests and the four `test_unrecognized_model_name_raises` cases → `TestParsePricingFailsLoudly` (with two cases Python lacked: a short row, an empty name); `test_unknown_cell_text_warns_and_is_none` → `TestParsePricingUnknownCellWarns`; `test_cloud_tag_derivation` → `TestCloudTag`; the twenty-four `test_plan_*` tests → the `TestPlan*` tests of Task 3, one for one except where two halves of one rule were merged (rename: `…MatchesByCatalogNameAfterRename`; collisions: `TestPlanIDCollisionWarns`; re-tag: `TestPlanRetagsAnEntryUnderAGuessedTag`; unrecognized cells: `TestPlanUnrecognizedCellKeepsTheExistingPrice`); `test_plan_never_adds_the_same_id_twice` → `TestPlanNeverAddsTheSameIDTwice` (two names resolved to one tag, where Python monkeypatched `cloud_tag`); `test_removal_digest_tracks_removals_and_strays` → `TestRemovalDigestMatchesModelman`; `test_format_plan_mentions_every_section` → `TestCatalogPlanFormat` (the whole text); the two `test_resolve_cloud_tags_*` and two `test_verified_tags_*` → their namesakes in Task 2; `test_apply_sync_mutates_registry`, `test_apply_sync_stamps_utc` → `TestCatalogApplyWritesWhatThePlanSays`, `TestStamp`; `test_offpeak_time_price_window` → `TestOffpeakRowIsOllamasPublishedWindow` (the windows; its `matches()` half is dropped with the resolver); `test_list_ollama_tags_parses_and_handles_failure`, `test_remove_ollama_tag` → `TestOllamaTagsReadsTheNameColumn`, `TestOllamaRemoveReadsNotFoundAsDone`; `test_fetch_wraps_errors`, `test_fetch_returns_text`, `test_save_failed_html` → `TestRealCloudFetch` and the exit 2 and 3 cases of `TestCloudSyncCatalogChangesNothingAndSaysWhy`.

From `modelman/tests/commands/test_ollama_catalog.py`, all twenty-one:

| Python test | Go test |
|---|---|
| `test_dry_run_writes_nothing` | Task 11, `TestCloudSyncCatalogDryRun` |
| `test_sync_applies_updates_and_additions` | Task 11, `TestCloudSyncCatalogApplyUnderTheApprovedDigest` (the registry text) |
| `test_sync_pulls_missing_and_removes_off_page` | the same test (the ollama commands, in order) |
| `test_sync_queues_no_exposes` | dropped: about modelman's queue and its retired `exposed` flag |
| `test_sync_with_nothing_queued_still_syncs_routes` | reversed on purpose: second half of `TestCloudSyncCatalogApplyUnderTheApprovedDigest` (nothing changed, no sync) |
| `test_sync_plan_has_no_expose_section` | dropped: the same retired flag |
| `test_sync_retags_entry_under_the_published_tag` | Task 5, `TestCatalogApplyWritesWhatThePlanSays` (the re-tag through the registry writer); Task 3, `TestPlanRetagsAnEntryUnderAGuessedTag` |
| `test_sync_confirm_no_changes_nothing` | Task 11, `TestCloudSyncCatalogDeclinedChangesNothing`; the "declined" case of `TestCloudSyncBothFlowsInOneRun` |
| `test_sync_applies_against_fresh_registry` | second half of `TestCloudSyncCatalogRefusesAPlanThatChangedUnderTheLock` (an unrelated row added mid-run survives, and the plan is applied) |
| `test_parse_failure_exits_3_and_saves_html` | `TestCloudSyncCatalogChangesNothingAndSaysWhy`, "the page changed shape" |
| `test_fetch_failure_exits_2` | the same test, "the page cannot be fetched" |
| `test_sync_ollama_down_exits_2_and_changes_nothing` | the same test, "ollama list fails" |
| `test_stray_rm_failure_exits_1` | `TestCloudSyncCatalogOllamaFailuresExit1AndTheRestStillRuns` (the failed rm) |
| `test_queued_ops_failure_exits_1` | the same test (the failed pull) |
| `test_mass_removal_exits_4_before_writing` | `TestCloudSyncCatalogChangesNothingAndSaysWhy`, "more than half the cloud entries would go" |
| `test_mass_removal_force_applies` | `TestCloudSyncCatalogForceAppliesAMassRemoval` |
| `test_no_tag_resolves_exits_2_and_changes_nothing` | `TestCloudSyncCatalogChangesNothingAndSaysWhy`, "no cloud tag resolves" |
| `test_yes_without_approved_digest_refuses_removals` | the same test, "--yes with no digest" and "--yes with another plan's digest" |
| `test_confirm_defaults_to_no_when_plan_deletes` | no longer a separate rule (the default is always No): Task 9, `TestPromptCloudSyncAsksOnTheTerminalAndDefaultsToNo` |
| `test_replan_with_unreviewed_removal_changes_nothing` | first half of `TestCloudSyncCatalogRefusesAPlanThatChangedUnderTheLock` |
| `test_resolve_skips_verified_tags` | second half of `TestCloudSyncCatalogApplyUnderTheApprovedDigest` (the second run fetches the pricing page and no tags page) |

From `modelman/tests/commands/test_refresh_prices.py`, all seven:

| Python test | Go test |
|---|---|
| `test_refresh_prices_updates_registry_and_reports` | Task 9, `TestCloudSyncPricesApply` |
| `test_refresh_prices_syncs_routes` | `TestCloudSyncPricesApply` (one sync); `TestCloudSyncUnderARedirectedRegistry` (the real sync) |
| `test_refresh_prices_reports_api_error_and_exits` | `TestCloudSyncPricesFailuresExit1` |
| `test_refresh_prices_records_last_run_on_success` | `TestCloudSyncStampOnlyRunSkipsTheRouteSync` (the "last run" is now the stamp, read back through `agents.LastPriceRefresh`) |
| `test_refresh_prices_leaves_last_run_alone_on_api_error` | `TestCloudSyncPricesFailuresExit1` (the registry is byte-identical, so nothing was stamped) |
| `test_refresh_prices_no_match_leaves_last_run_alone` | last part of `TestCloudSyncAsksOnceAndTakesNoForAnAnswer` (no model matched: the warning, no question, registry byte-identical) |
| `test_refresh_prices_ollama_only_registry_is_quiet` | `TestCloudSyncWithNoOpenRouterModelFetchesNothing` |

From `modelman/tests/test_pricing.py`: the per-million mapping, no-match, API-failure, cloud-provider, native, `openrouter_priced` override, missing-cache, preserve-manual, overwrite-cache, time-prices and ollama-cloud cases → Task 4's `TestParseOpenRouter`, `TestPlanPricesMergeRules`, `TestPlanPricesCandidatesAndWarnings` and `TestOpenRouterPricedMatchesTheContract`. **Dropped:** all of `test_app_pricing.py` (`should_run_price_refresh`, the daily gate) and all of `test_time_pricing.py` (validators ported in Step 2; `price_at` not ported).

## What Was Measured

Done in a scratch copy on 2026-10-07; nothing here needs repeating, but a reviewer can.

- **The dependency.** `go get golang.org/x/net@v0.59.0 && go mod tidy && go build ./...` and the full wt suite (23 packages) pass. `go.mod` gains three lines and loses two, `go.sum` gains six and loses four; both diffs are in Task 1, Step 6. A build of `internal/cloudsync` links exactly two packages from it: `golang.org/x/net/html` and `golang.org/x/net/html/atom`.
- **Parser parity.** The Go parser and modelman's `parse_pricing` give the same catalog (names, order, every price, every unknown flag, off-peak rows, warning count) on modelman's fixture (17 models, 2 with off-peak rows) and on the live page fetched once that day (18 models, 2 with off-peak rows, no warnings).
- **Plan and digest parity.** On one registry (updates, a re-tag, a removal, a stray tag, an unresolved name, a local namesake) against both pages, `CatalogPlan.Format()` plus the digest and the mass-removal answer were byte-identical to modelman's `format_plan`, `removal_digest` and `mass_removal`. Example digest from that run: `b03f02e13d21` from both tools.
- **Apply parity.** Applying the same plan with both tools and decoding the two files gave the same data, with one difference: the re-tagged entry keeps its `tags` in wt (see the decisions table). The bytes differ in ways that are modelman's: it rewrites every integer price as a float, writes an empty `[models.model_info]` for a new row, and writes the keys of a cost table in an order that varies from run to run (`_COST_FIELDS` is a Python `set`, `registry.py:67`). modelman loads the wt-written file.
- **Prices parity.** On the live OpenRouter list (467 models) both tools wrote the same data, and both left the `-1` model alone with a warning.
- **Live tag resolution.** One `wt cloud-sync --only catalog --dry-run` of the built binary, from a scratch registry with a stand-in `ollama`, resolved all 16 bare names on the live page with no warning; two resolved to sized tags only (`mistral-large-3:675b-cloud`, `nemotron-3-nano:30b-cloud`). The stand-in recorded `argv: list` with `OLLAMA_HOST=http://localhost:11434`, the scratch registry's provider address.
- **The whole command, in scratch.** Dry run, apply under the digest, `--html` unreadable (exit 2), a page with no table (exit 3, HTML saved), `--force` with `--only prices` (exit 1), and no terminal without `--yes` (exit 1, in a shell that has none) were run on the built binary with the stand-in `ollama`; the pull and rm reached only the stand-in. The no-terminal case is not left to a hand run: the tests reach it through `openTTY` wherever they are run.
- **What the review of this plan found and what was measured in answer.** The collector differential: modelman's `_TableCollector` gives `[[["a"]]]`, `[[["x"]]]` and `[[["z"]]]` for the two `<noscript>` strings and the CDATA string of `TestCollectTablesReadsMarkupAsModelmanDid` under Python 3.13.15 and 3.14.8; the tokenizer without the two settings gave `[]`, `[[["an<td>x</td>b"]]]` and `[[["y]]>z"]]]`. A registry with no ollama provider row: the built binary's plain `cloud-sync` printed the nothing-to-mirror line and exited 0, and `--only catalog` exited 1 (Task 11, Step 8). A `cost.time_prices` row in the layout the registry writer emits came through both flows byte for byte, keys out of schema order, an unknown key and integer prices included; a row typed as an inline table is laid out again by any write (the Step 2 emitter has one layout), with the same keys in the same order. A second `wt litellm sync` on the built binary left `config.yaml` byte-identical and did not run the restart command. The tests for the three collector cases, the local tag, the interrupted run and the split write were each also run against the code without the fix, and failed there.
- **One thing to know about a trial apply against a copy of the registry.** The route sync at the end of a run probes the ollama daemon at the registry's address over HTTP, read-only, to decide which local models to route. In a scratch home whose registry names `localhost:11434`, that is the developer's real daemon: its local models were listed and routed into the *scratch* `config.yaml`. Nothing real was written. It is how `wt litellm sync` has always worked; it matters only for what a scratch `config.yaml` ends up holding.

## If Step 3 Lands First

Step 3 (`wt model` CLI, the Models tab, `internal/modeladmin`) is being built in parallel and is not on `origin/main`. This plan uses nothing from it. If it merges before a slice of this plan:

- **Reuse:** `syncRoutesAfterWrite` (`cmd/wt/model.go:25`) is already on main and is the one seam for "the route sync after a registry write"; Step 3 uses it too, and there must stay one. Step 3's plan moves `promptStop` onto `openTTY`, the seam `promptCloudSync` (Task 9) already opens the terminal through; if Step 3 also turned `promptStop` into a shared yes/no helper, make `promptCloudSync` call it, keeping its message and its two tests. If Step 3 added a read-only view of the registry document to `internal/config` with `ReadRegistryDoc`'s semantics, use that and drop Task 6's function, pointing its two tests at the survivor. If Step 3 exported a helper that runs `ollama` pinned to the provider row (it needs one for `ollama show`), have `realOllamaCLI` (Task 10) call it, and keep the `ollamaCLI` seam and its tests.
- **Do not duplicate, and do not touch:** adding, editing or removing a model by hand; provider seeding (`wt cloud-sync` never seeds); anything in `internal/modeladmin`; the Models tab. `wt model edit` not stamping `pricing_updated_at` is Step 3's rule and Step 3's test; if `wt model edit` does stamp it, the derived notice in Task 7 is wrong and that is a Step 3 bug.
- **Prose to update:** the skill's step 1 (Task 12) tells the operator to hand-edit `family` in `registry.toml` and then run `wt litellm sync`. Once `wt model edit` exists, say `wt model edit <id> --family <name>` there instead (it writes through the lock and syncs the routes itself); guide 02's own "by hand" list is Step 3's to change. Step 3's `wt model edit` does not stamp `pricing_updated_at`, so the edit does not disturb the stale-price notice.
- **Expect conflicts in:** `cmd/wt/main.go` (the `AddCommand` line and the examples), `cmd/wt/testmain_test.go` (the seam block), `wt/CLAUDE.md` (the file table and the Registry section), `wt/docs/internals/testing.md` (the seam list), guides 00 and 02. Rebase, then apply the same change to the text as it then reads.

## PR Slices

| PR | Branch | Tasks | Needs | Ships |
|---|---|---|---|---|
| 1 | `feat/wt-cloud-sync-core` | 1–5 | nothing (Step 2 is on main) | `internal/cloudsync`, the dependency. No command, no behaviour change. |
| 2 | `feat/wt-cloud-sync-prices` | 6–9 | PR 1 merged | `wt cloud-sync` with the prices flow, the exit-code error, the derived notice; `config.PriceRefreshLastRun` removed. |
| 3 | `feat/wt-cloud-sync-catalog` | 10–11 | PR 2 merged | The catalog flow: `--html`, `--approve-removals`, `--force`, exit 2 to 5. Task 13 (the live dry run) is run before this merges. |
| 4 | `docs/wt-cloud-sync-skill` | 12 | PR 3 merged | The skill moved and rewritten, the command reference, guide and `CLAUDE.md` text. |

Within PR 2, Tasks 6, 7 and 8 are independent of each other and Task 9 needs all three. PR 1's tasks are in dependency order. Between PR 2 and PR 4 the old skill still says `modelman refresh-prices` clears wt's notice by stamping `price_refresh_last_run`; it still clears it (modelman stamps `pricing_updated_at` on every model it matches, `pricing.py:183`), only the reason given is out of date until PR 4.

## File Structure

| File | Change | PR | Responsibility |
|---|---|---|---|
| `wt/go.mod`, `wt/go.sum` | modify | 1 | `golang.org/x/net` v0.59.0 |
| `wt/internal/cloudsync/doc.go` | create | 1 | Package comment: what is pure, what the caller owns |
| `wt/internal/cloudsync/pricingpage.go` | create | 1 | `ParsePricing`, `collectTables`, `mapColumns`, `ParseError`, `PriceTriple`, `CatalogModel`, `Catalog`, `MinRows`, `PricingURL` |
| `wt/internal/cloudsync/testdata/ollama_pricing.html` | create (copy) | 1 | The saved page modelman's test reads |
| `wt/internal/cloudsync/entry.go` | create | 1 | `Entry`, `Cost`, `Provider`: what the planners read of a registry row; `Entries`, `Providers` |
| `wt/internal/cloudsync/tags.go` | create | 1 | `CloudTag`, `IsCloudTag`, `VerifiedTags`, `ResolveCloudTags`, `Getter`, `CatalogNameKey`, `LibraryTagsURL` |
| `wt/internal/cloudsync/catalog.go` | create | 1 | `PlanCatalog`, `CatalogPlan` (`MassRemoval`, `RemovalDigest`, `HasWork`, `Format`), `offpeakRow`, the merge rules |
| `wt/internal/cloudsync/openrouter.go` | create | 1 | `ParseOpenRouter`, `OpenRouterPriced`, `PlanPrices`, `PricePlan` (`HasWork`, `Format`), `OpenRouterModelsURL` |
| `wt/internal/cloudsync/apply.go` | create | 1 | `Stamp`, `CatalogPlan.Apply`, `PricePlan.Apply`, `CatalogApplied`, `PricesApplied` |
| `wt/internal/cloudsync/*_test.go` | create | 1 | Their tests; `main_test.go` isolates the config home |
| `wt/internal/config/registry_write.go` | modify | 2 | `ReadRegistryDoc` |
| `wt/internal/config/config.go` | modify | 2 | `Model.PricingUpdatedAt`, `Model.PricingUpdated` |
| `wt/internal/config/registry_read_test.go` | create | 2 | Their tests |
| `wt/internal/config/modelman.go` | modify | 2 | `PriceRefreshLastRun` and its struct field deleted |
| `wt/internal/config/modelman_test.go`, `modelman_fixture_test.go`, `registry.go` | modify | 2 | Follow the deletion |
| `wt/internal/agents/price_notice.go` | rewrite | 2 | `LastPriceRefresh`, `PriceNotice` on a derived date, the 7-day rule |
| `wt/internal/agents/price_notice_test.go` | modify | 2 | Its tests |
| `wt/cmd/wt/exitcode.go`, `exitcode_test.go` | create | 2 | `exitCodeError`, `exitCodeOf` |
| `wt/cmd/wt/main.go` | modify | 2, 3 | Exit with `exitCodeOf(err)`; register the command; an example line |
| `wt/cmd/wt/cloudsync.go` | create (2), rewrite (3) | 2, 3 | The command, the prices flow, the one registry write, the route sync, the exit status |
| `wt/cmd/wt/cloudsync_test.go` | create (2), modify (3) | 2, 3 | Its tests and the shared test helpers |
| `wt/cmd/wt/testmain_test.go` | modify | 2, 3 | The new seams fail closed |
| `wt/cmd/wt/cloudsync_ollama.go`, `cloudsync_ollama_test.go` | create | 3 | The pinned ollama CLI: list, pull, rm |
| `wt/cmd/wt/cloudsync_catalog.go`, `cloudsync_catalog_test.go` | create | 3 | The catalog flow: page, tags, plan, gates, the ollama work |
| `wt/docs/wt-cloud-sync.md` | create | 4 | The command reference |
| `wt/.claude/skills/cloud-sync/SKILL.md` | create (moved from modelman) | 4 | The operator's procedure |
| `modelman/.claude/skills/ollama-catalog/SKILL.md` | delete (moved) | 4 | |
| `CLAUDE.md`, `wt/CLAUDE.md`, `wt/CHANGELOG.md`, `modelman/CLAUDE.md` (one line), `wt/docs/internals/{testing,launch-flow,config-and-registry}.md`, `docs/guides/{00,02,04}-*.md`, `docs/contracts/modelman.sample.toml` | modify | 1–4 | Kept true as each slice lands |

---

## PR 1 — the pure core and the dependency

Branch: `git switch -c feat/wt-cloud-sync-core main` (from an up-to-date `main`).

### Task 1: The pricing-page parser, and the `golang.org/x/net` dependency

**Files:**
- Create: `wt/internal/cloudsync/testdata/ollama_pricing.html` (a copy), `wt/internal/cloudsync/main_test.go`, `wt/internal/cloudsync/pricingpage_test.go`, `wt/internal/cloudsync/doc.go`, `wt/internal/cloudsync/pricingpage.go`
- Modify: `wt/go.mod`, `wt/go.sum`

**Interfaces:**
- Consumes: `config.IsolateConfigHomeForTest() (home string, cleanup func())` and `config.RegistryWriteGuardArmed() bool` (`internal/config/fortest.go:33`, `:113`), for the package's `TestMain`.
- Produces:
  - `const PricingURL = "https://ollama.com/pricing"`, `const MinRows = 5`
  - `type ParseError struct{ Check string }` (`*ParseError` is the error)
  - `type Unknown struct{ Input, Cache, Output bool }`
  - `type PriceTriple struct { Input, Cache, Output *float64; Unknown Unknown }`
  - `type CatalogModel struct { Name string; Prices PriceTriple; Offpeak *PriceTriple }`
  - `type Catalog struct { Models []CatalogModel; Warnings []string }`
  - `func ParsePricing(page string) (Catalog, error)`
  - unexported, used by later tasks' code and tests: `samePrice(a, b *float64) bool`; test helpers `f(float64) *float64`, `s(string) *string`, `num(*float64) string`, `page(head string, rows ...string) string`, `pageRow(name, in, cache, out string) string`, `pageHead`, `wantTriple(...)`

- [ ] **Step 1: Copy the fixture**

From the repo root:

```bash
mkdir -p wt/internal/cloudsync/testdata
cp modelman/tests/fixtures/ollama_pricing.html wt/internal/cloudsync/testdata/ollama_pricing.html
```

It is a copy, not a move: modelman's tests keep reading theirs until Step 6. `cmp` the two afterwards; they must be identical (50,904 bytes).

- [ ] **Step 2: Write the failing tests**

Create `wt/internal/cloudsync/main_test.go`:

```go
package cloudsync

import (
	"os"
	"strconv"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// TestMain keeps this package's tests off the developer's machine. The Apply
// tests go through config.UpdateRegistry, so the config home is a throwaway
// directory and the registry write guard is armed: a test that forgot to
// name its own registry fails instead of rewriting the real one.
func TestMain(m *testing.M) {
	_, cleanup := config.IsolateConfigHomeForTest()
	code := m.Run()
	cleanup()
	os.Exit(code)
}

// TestRegistryWriteGuardIsArmed pins the TestMain above: the guard is off in
// any test binary that did not arm it, and a package that reaches
// config.UpdateRegistry without it can rewrite the developer's registry.
func TestRegistryWriteGuardIsArmed(t *testing.T) {
	if !config.RegistryWriteGuardArmed() {
		t.Fatal("the registry write guard is not armed in this test binary")
	}
}

func f(v float64) *float64 { return &v }

func s(v string) *string { return &v }

// num prints a price for a failure message.
func num(v *float64) string {
	if v == nil {
		return "-"
	}
	return strconv.FormatFloat(*v, 'g', -1, 64)
}
```

Create `wt/internal/cloudsync/pricingpage_test.go`:

```go
package cloudsync

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

const pageHead = "<tr><th>Model</th><th>Input</th><th>Cached input</th><th>Output</th></tr>"

func pageRow(name, in, cache, out string) string {
	return fmt.Sprintf(`<tr><td><a href="/library/%s">%s</a></td><td>%s</td><td>%s</td><td>%s</td></tr>`, name, name, in, cache, out)
}

// page is a pricing page with five plain rows (m0 to m4) and the given rows
// after them, so a test adds the one row it is about and still clears MinRows.
func page(head string, rows ...string) string {
	var b strings.Builder
	b.WriteString("<html><table><thead>" + head + "</thead><tbody>")
	for i := range 5 {
		b.WriteString(pageRow(fmt.Sprintf("m%d", i), "$1.00", "$0.10", "$2.00"))
	}
	b.WriteString(strings.Join(rows, "") + "</tbody></table></html>")
	return b.String()
}

func parsed(t *testing.T, html string) map[string]CatalogModel {
	t.Helper()
	catalog, err := ParsePricing(html)
	if err != nil {
		t.Fatalf("ParsePricing: %v", err)
	}
	byName := map[string]CatalogModel{}
	for _, m := range catalog.Models {
		byName[m.Name] = m
	}
	return byName
}

func wantTriple(t *testing.T, what string, got PriceTriple, in, cache, out *float64) {
	t.Helper()
	if !samePrice(got.Input, in) || !samePrice(got.Cache, cache) || !samePrice(got.Output, out) {
		t.Errorf("%s = %s/%s/%s, want %s/%s/%s", what, num(got.Input), num(got.Cache), num(got.Output), num(in), num(cache), num(out))
	}
}

// TestParsePricingFixture reads a saved copy of ollama.com/pricing, the same
// file modelman's test reads, and pins what both tools take from it: the
// model count, base and off-peak prices, a "-" cell as no price, and no
// off-peak row passing as a model. If this fails after the page was replaced
// by a newer copy, the expected values are what to update; if it fails with
// the same file, the parser changed what a sync writes to the registry.
func TestParsePricingFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/ollama_pricing.html")
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := ParsePricing(string(data))
	if err != nil {
		t.Fatalf("ParsePricing: %v", err)
	}
	if len(catalog.Models) != 17 || len(catalog.Warnings) != 0 {
		t.Fatalf("models = %d, warnings = %q; want 17 models and no warnings", len(catalog.Models), catalog.Warnings)
	}
	byName := map[string]CatalogModel{}
	for _, m := range catalog.Models {
		byName[m.Name] = m
		if strings.Contains(strings.ToLower(m.Name), "off-peak") {
			t.Errorf("an off-peak row became a model: %q", m.Name)
		}
	}
	pro := byName["deepseek-v4-pro"]
	wantTriple(t, "deepseek-v4-pro", pro.Prices, f(1.32), f(0.044), f(3.96))
	if pro.Offpeak == nil {
		t.Fatal("deepseek-v4-pro has no off-peak prices")
	}
	wantTriple(t, "deepseek-v4-pro off-peak", *pro.Offpeak, f(0.66), f(0.022), f(1.98))
	if flash := byName["deepseek-v4.1-flash"]; flash.Offpeak == nil {
		t.Error("deepseek-v4.1-flash has no off-peak prices")
	} else {
		wantTriple(t, "deepseek-v4.1-flash off-peak", *flash.Offpeak, f(0.15), f(0.003), f(0.60))
	}
	if byName["gemma4"].Offpeak != nil {
		t.Error("gemma4 has off-peak prices; the page lists none")
	}
	if got := byName["gpt-oss:120b"].Prices.Input; !samePrice(got, f(0.15)) {
		t.Errorf("gpt-oss:120b input = %s, want 0.15", num(got))
	}
	if got := byName["mistral-large-3"].Prices; got.Cache != nil || got.Unknown.Cache {
		t.Errorf("mistral-large-3 cache = %s (unknown %v), want no price from its \"-\" cell", num(got.Cache), got.Unknown.Cache)
	}
}

// TestParsePricingFindsColumnsByHeader pins that prices are read by header
// text, not position. When ollama adds or reorders a column, a positional
// parser would write the output price into the input field of every entry
// without failing.
func TestParsePricingFindsColumnsByHeader(t *testing.T) {
	head := "<tr><th>Output</th><th>Model</th><th>Cached Input</th><th>Input</th></tr>"
	var rows strings.Builder
	for i := range 5 {
		fmt.Fprintf(&rows, "<tr><td>$2</td><td>m%d</td><td>$0.1</td><td>$1</td></tr>", i)
	}
	rows.WriteString(`<tr><td>$9.00</td><td><a href="#">zz</a></td><td>$0.50</td><td>$3.00</td></tr>`)
	got := parsed(t, "<table><thead>"+head+"</thead><tbody>"+rows.String()+"</tbody></table>")
	wantTriple(t, "zz", got["zz"].Prices, f(3), f(0.5), f(9))
}

// TestParsePricingFoldsOffpeakRowsIntoTheirBase pins that a "(off-peak)" row
// becomes its model's off-peak prices whatever its capitalisation and
// wherever it stands, here before its base row. Reading it as a second model
// would register a junk entry and try to pull a tag that does not exist.
func TestParsePricingFoldsOffpeakRowsIntoTheirBase(t *testing.T) {
	got := parsed(t, page(pageHead,
		pageRow("zz (off-peak)", "$0.50", "-", "$1.00"),
		pageRow("zz", "$1.00", "$0.10", "$2.00"),
		pageRow("yy", "$1,000.50", "$0.10", "$2.00"),
		pageRow("yy (Off-Peak)", "$ 0.25", "—", "$1.00"),
	))
	if len(got) != 7 {
		t.Fatalf("models = %d, want the 5 plain rows plus zz and yy", len(got))
	}
	if got["zz"].Offpeak == nil || got["yy"].Offpeak == nil {
		t.Fatalf("off-peak rows were not folded in: zz %v, yy %v", got["zz"].Offpeak, got["yy"].Offpeak)
	}
	wantTriple(t, "zz off-peak", *got["zz"].Offpeak, f(0.5), nil, f(1))
	wantTriple(t, "yy", got["yy"].Prices, f(1000.5), f(0.1), f(2))
	wantTriple(t, "yy off-peak", *got["yy"].Offpeak, f(0.25), nil, f(1))
}

// TestParsePricingFailsLoudly pins every check that turns a changed page
// into a *ParseError naming what failed, instead of a short catalog. This is
// the first half of the safety net: a catalog that silently lost its rows
// would plan the removal of every ollama cloud entry in the registry.
func TestParsePricingFailsLoudly(t *testing.T) {
	sixBad := ""
	for i := range 6 {
		sixBad += pageRow(fmt.Sprintf("m%d", i), "USD 1", "USD 1", "USD 1")
	}
	cases := []struct{ name, html, want string }{
		{"no table", "<html><div>pricing moved</div></html>", "no <table>"},
		{"a header renamed", page("<tr><th>Model</th><th>Prompt</th><th>Cached input</th><th>Output</th></tr>"), `"Prompt"`},
		{"too few rows", "<table>" + pageHead + pageRow("a", "$1", "$1", "$1") + "</table>", "expected at least 5"},
		{"off-peak row with no base row", page(pageHead, pageRow("ghost (Off-Peak)", "$1", "$1", "$1")), "no base row"},
		{"the same model twice", page(pageHead, pageRow("m0", "$1", "$1", "$1")), "duplicate"},
		{"most prices unreadable", "<table>" + pageHead + sixBad + "</table>", "unrecognized"},
		{"a row with too few cells", page(pageHead, "<tr><td>short</td><td>$1</td></tr>"), "row 6 has 2 cells"},
		{"an empty model name", page(pageHead, pageRow("", "$1", "$1", "$1")), "empty model name"},
		{"new off-peak wording", page(pageHead, pageRow("glm-5.3 (Off-peak hours)", "$1.00", "$0.10", "$2.00")), "not an ollama tag"},
		{"off-peak without parentheses", page(pageHead, pageRow("glm-5.3 off-peak", "$1.00", "$0.10", "$2.00")), "not an ollama tag"},
		{"a footnote mark", page(pageHead, pageRow("glm-5.3*", "$1.00", "$0.10", "$2.00")), "not an ollama tag"},
		{"a display name", page(pageHead, pageRow("GLM 5.3", "$1.00", "$0.10", "$2.00")), "not an ollama tag"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			catalog, err := ParsePricing(tc.html)
			var parseErr *ParseError
			if !errors.As(err, &parseErr) {
				t.Fatalf("err = %v (catalog of %d models), want a *ParseError", err, len(catalog.Models))
			}
			if !strings.Contains(parseErr.Check, tc.want) {
				t.Errorf("check = %q, want it to name %q", parseErr.Check, tc.want)
			}
		})
	}
}

// TestParsePricingUnknownCellWarns pins the middle ground: one price cell in
// a new format is a warning and an Unknown mark, not a failed page and not a
// cleared price. The planner keeps the registry's value for a field marked
// Unknown, so a single odd cell cannot erase a price.
func TestParsePricingUnknownCellWarns(t *testing.T) {
	catalog, err := ParsePricing(page(pageHead, pageRow("zz", "Free", "$0.10", "$2.00")))
	if err != nil {
		t.Fatalf("ParsePricing: %v", err)
	}
	zz := catalog.Models[len(catalog.Models)-1]
	if zz.Name != "zz" || zz.Prices.Input != nil || zz.Prices.Unknown != (Unknown{Input: true}) {
		t.Errorf("zz = %+v, want no input price, marked unknown", zz)
	}
	if want := []string{`zz input: unrecognized price "Free"; existing price kept`}; !reflect.DeepEqual(catalog.Warnings, want) {
		t.Errorf("warnings = %q, want %q", catalog.Warnings, want)
	}
}

// TestCollectTablesReadsMarkupAsModelmanDid pins the tokenizer against the
// outputs of modelman's HTMLParser-based collector on the same inputs (the
// expected values were produced by running that collector). The two tools
// share one fixture and, until modelman is deleted, one registry: a page one
// of them reads differently is a page they would sync differently.
func TestCollectTablesReadsMarkupAsModelmanDid(t *testing.T) {
	cases := []struct {
		name, html string
		want       [][][]string
	}{
		{"script text is cell text, its tags are not", "<table><tr><td>a<script>var x = '<td>no</td>';</script>b</td></tr></table>", [][][]string{{{"avar x = '<td>no</td>';b"}}}},
		{"a comment is skipped", "<table><tr><td>a<!-- <td>no</td> -->b</td></tr></table>", [][][]string{{{"ab"}}}},
		{"> inside a quoted attribute", `<table><tr><td title="x > y">a</td><td>b</td></tr></table>`, [][][]string{{{"a", "b"}}}},
		{"entities are decoded", "<table><tr><td>&amp;&nbsp;&lt;x&gt; &#36;1</td></tr></table>", [][][]string{{{"& <x> $1"}}}},
		{"a cell that reopens replaces the open one", "<table><tr><td>a<td>b</td></tr></table>", [][][]string{{{"b"}}}},
		{"a nested table takes the row", "<table><tr><td>outer<table><tr><td>inner</td></tr></table>tail</td></tr></table>", [][][]string{nil, {{"inner"}}}},
		{"uppercase tags", "<TABLE><TR><TH>Model</TH></TR><TR><TD>A</TD></TR></TABLE>", [][][]string{{{"Model"}, {"A"}}}},
		{"a self-closing cell is an empty cell", "<table><tr><td/><td>x</td></tr></table>", [][][]string{{{"", "x"}}}},
		{"a row outside any table is dropped", "<tr><td>stray</td></tr><table><tr><td>in</td></tr></table>", [][][]string{{{"in"}}}},
		{"whitespace is folded, no-break space included", "<table><tr><td>  a \n\t b\u00a0c  </td></tr></table>", [][][]string{{{"a b c"}}}},
		{"a row with no cell is dropped", "<table><tr></tr><tr><td></td></tr></table>", [][][]string{{{""}}}},
		{"a row that reopens replaces the open one", "<table><tr><td>lost</td><tr><td>kept</td></tr></table>", [][][]string{{{"kept"}}}},
		{"a table inside <noscript> is read", "<noscript><table><tr><td>a</td></tr></table></noscript>", [][][]string{{{"a"}}}},
		{"markup inside <noscript> is markup", "<table><tr><td>a<noscript>n<td>x</td></noscript>b</td></tr></table>", [][][]string{{{"x"}}}},
		{"a CDATA section is skipped whole", "<table><tr><td><![CDATA[x<td>y]]>z</td></tr></table>", [][][]string{{{"z"}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := collectTables(tc.html); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("collectTables(%q)\n got %q\nwant %q", tc.html, got, tc.want)
			}
		})
	}
}
```

The expected values in `TestCollectTablesReadsMarkupAsModelmanDid` were produced by running modelman's `_TableCollector` on the same fifteen strings (Python 3.13.15 and 3.14.8 give the same). The no-break space in the whitespace case is written `\u00a0`, so that no editor or formatter can turn it into a plain space and leave the test passing for the wrong reason. Do not "fix" one to what looks right: the point is that both tools read a page the same way.

- [ ] **Step 3: Run the tests to verify they fail**

Run (from `wt/`): `go test -count=1 ./internal/cloudsync`
Expected: a build failure naming the missing symbols:

```text
# github.com/ohanaverse/local-ai-setup/wt/internal/cloudsync [github.com/ohanaverse/local-ai-setup/wt/internal/cloudsync.test]
internal/cloudsync/pricingpage_test.go:30:51: undefined: CatalogModel
internal/cloudsync/pricingpage_test.go:32:18: undefined: ParsePricing
internal/cloudsync/pricingpage_test.go:36:23: undefined: CatalogModel
internal/cloudsync/pricingpage_test.go:43:48: undefined: PriceTriple
```

- [ ] **Step 4: Write the package comment and the parser**

Create `wt/internal/cloudsync/doc.go`:

```go
// Package cloudsync is the pure core of `wt cloud-sync`: it turns what two
// public services publish into changes to registry.toml.
//
//   - Prices: OpenRouter's model list (ParseOpenRouter, PlanPrices) refreshes
//     the per-token prices of the registry's OpenRouter-priced models.
//   - Catalog: ollama.com/pricing (ParsePricing), each model's cloud tag
//     (ResolveCloudTags) and the plan that makes the registry's ollama cloud
//     entries mirror the page (PlanCatalog), with its two safety gates: the
//     mass-removal guard and the removal digest.
//
// Nothing here opens a socket, runs a command, prints or reads the clock. A
// page arrives as text, a fetch as a function the caller passes, the time as
// an argument. The two Apply methods change a config.RegistryDoc and nothing
// else, so they are safe inside config.UpdateRegistry, which may run them
// more than once. cmd/wt/cloudsync.go owns the fetches, the ollama CLI, the
// confirmation and the exit codes.
//
// It is the Go port of modelman's pricing.py, ollama_catalog.py and the
// writing half of time_pricing.py. ParsePricing is the only code that knows
// the pricing page's HTML shape: when ollama changes the page,
// `wt cloud-sync` exits 3 and saves the raw HTML, and the repair is there
// and in testdata/ollama_pricing.html.
package cloudsync
```

Create `wt/internal/cloudsync/pricingpage.go`:

```go
package cloudsync

import (
	"bytes"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/net/html"
)

// PricingURL is the page the catalog flow mirrors.
const PricingURL = "https://ollama.com/pricing"

// MinRows is the fewest model rows a page may hold and still be believed. A
// page with fewer is a page that parsed wrong.
const MinRows = 5

var (
	offpeakSuffix = regexp.MustCompile(`(?i)\s*\(\s*off[\s-]*peak\s*\)\s*$`)
	priceCell     = regexp.MustCompile(`^\$\s*([0-9]+(?:\.[0-9]+)?)$`)
	// What an ollama model name looks like once the off-peak suffix is gone.
	// A cell that does not match (spaces, parentheses, a footnote mark) means
	// the page's wording changed: fail loudly instead of registering a junk
	// tag.
	modelName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]*$`)
)

// ParseError says the pricing page no longer has the shape ParsePricing
// expects. Check names the check that failed.
type ParseError struct{ Check string }

func (e *ParseError) Error() string { return e.Check }

func parseErrorf(format string, args ...any) error {
	return &ParseError{Check: fmt.Sprintf(format, args...)}
}

// Unknown marks the prices of a PriceTriple whose cell was there but was not
// recognized as a price. The price is nil, and a sync keeps the registry's
// value for it instead of clearing it: only a real "-" cell clears a price.
type Unknown struct{ Input, Cache, Output bool }

// PriceTriple is one row's prices per million tokens. nil is "no price".
type PriceTriple struct {
	Input, Cache, Output *float64
	Unknown              Unknown
}

// samePrice reports whether two optional prices are the same: both absent,
// or both the same number.
func samePrice(a, b *float64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// CatalogModel is one model the page lists, with its off-peak prices when
// the page has an off-peak row for it.
type CatalogModel struct {
	Name    string
	Prices  PriceTriple
	Offpeak *PriceTriple
}

// Catalog is the parsed page.
type Catalog struct {
	Models   []CatalogModel
	Warnings []string
}

// isSpace is Python's str.isspace for the characters str.split() splits on:
// Go's unicode.IsSpace plus the four ASCII separators U+001C to U+001F.
func isSpace(r rune) bool { return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f) }

// collectTables returns every <table> as a list of rows, each row a list of
// cell texts with runs of whitespace folded to one space. It is the port of
// modelman's _TableCollector, quirks included, so the two read a page the
// same way: a <tr> or <td> that opens while one is open replaces it, and a
// row lands in the innermost open table.
//
// It reads tokens, not a tree. html.Parse would move and close tags the way a
// browser does, and the checks in ParsePricing are about the page as written.
//
// Two settings make the bare tokenizer read what Python's parser read. It
// treats <noscript> content as raw text unless told otherwise (html.Parse
// tells it; a tokenizer on its own must), and a pricing table inside a
// <noscript> fallback would then be no table at all. And a CDATA section is
// skipped whole, as Python skips it, instead of being cut at its first ">".
// One case is still read differently and is not pinned: a <script> that
// nests a <script> inside a comment ends at another </script>.
func collectTables(page string) [][][]string {
	z := html.NewTokenizer(strings.NewReader(page))
	z.AllowCDATA(true)
	var (
		tables [][][]string
		open   []int
		row    []string
		inRow  bool
		cell   strings.Builder
		inCell bool
	)
	start := func(tag string) {
		switch {
		case tag == "table":
			tables = append(tables, nil)
			open = append(open, len(tables)-1)
		case tag == "tr" && len(open) > 0:
			row, inRow = nil, true
		case (tag == "td" || tag == "th") && inRow:
			cell.Reset()
			inCell = true
		}
	}
	end := func(tag string) {
		switch {
		case (tag == "td" || tag == "th") && inCell && inRow:
			row = append(row, strings.Join(strings.FieldsFunc(cell.String(), isSpace), " "))
			inCell = false
		case tag == "td" || tag == "th":
		case tag == "tr" && inRow && len(open) > 0:
			if len(row) > 0 {
				at := open[len(open)-1]
				tables[at] = append(tables[at], row)
			}
			row, inRow = nil, false
		case tag == "table" && len(open) > 0:
			open = open[:len(open)-1]
		}
	}
	for {
		switch z.Next() {
		case html.ErrorToken:
			return tables
		case html.StartTagToken:
			name, _ := z.TagName()
			if string(name) == "noscript" {
				z.NextIsNotRawText()
			}
			start(string(name))
		case html.EndTagToken:
			name, _ := z.TagName()
			end(string(name))
		case html.SelfClosingTagToken:
			name, _ := z.TagName()
			start(string(name))
			end(string(name))
		case html.TextToken:
			if inCell && !bytes.HasPrefix(z.Raw(), []byte("<![CDATA[")) {
				cell.Write(z.Text())
			}
		}
	}
}

// columns is where each of the four columns sits in a table.
type columns struct{ model, input, cache, output int }

func (c columns) width() int { return max(c.model, c.input, c.cache, c.output) + 1 }

// mapColumns finds the four columns by their header text, so a column the
// page adds or moves does not shift the prices.
func mapColumns(header []string) (columns, bool) {
	find := func(pred func(string) bool) int {
		for i, h := range header {
			if pred(strings.ToLower(h)) {
				return i
			}
		}
		return -1
	}
	c := columns{
		model:  find(func(h string) bool { return strings.Contains(h, "model") }),
		cache:  find(func(h string) bool { return strings.Contains(h, "cache") }),
		input:  find(func(h string) bool { return strings.Contains(h, "input") && !strings.Contains(h, "cache") }),
		output: find(func(h string) bool { return strings.Contains(h, "output") }),
	}
	return c, c.model >= 0 && c.cache >= 0 && c.input >= 0 && c.output >= 0
}

// ParsePricing reads the pricing page. Every way the page can stop looking
// like a price table is a *ParseError, never a short or empty catalog: a
// catalog that lost its rows would plan the removal of every entry.
func ParsePricing(page string) (Catalog, error) {
	tables := collectTables(page)
	if len(tables) == 0 {
		return Catalog{}, parseErrorf("no <table> found on the page")
	}
	var (
		cols  columns
		rows  [][]string
		found bool
	)
	for _, table := range tables {
		if len(table) == 0 {
			continue
		}
		if c, ok := mapColumns(table[0]); ok {
			cols, rows, found = c, table[1:], true
			break
		}
	}
	if !found {
		var headers [][]string
		for _, table := range tables {
			if len(table) > 0 {
				headers = append(headers, table[0])
			}
		}
		return Catalog{}, parseErrorf("no table has Model/Input/Cached/Output headers (found headers: %q)", headers)
	}
	if len(rows) < MinRows {
		return Catalog{}, parseErrorf("found %d model rows, expected at least %d", len(rows), MinRows)
	}

	var warnings []string
	seen, unrecognized := 0, 0
	price := func(cell, where string) (value *float64, unknown bool) {
		seen++
		text := strings.ReplaceAll(strings.TrimFunc(cell, isSpace), ",", "")
		switch text {
		case "", "-", "—", "–":
			return nil, false
		}
		if m := priceCell.FindStringSubmatch(text); m != nil {
			if f, err := strconv.ParseFloat(m[1], 64); err == nil {
				return &f, false
			}
		}
		unrecognized++
		warnings = append(warnings, fmt.Sprintf("%s: unrecognized price %q; existing price kept", where, cell))
		return nil, true
	}

	base := map[string]PriceTriple{}
	offpeak := map[string]PriceTriple{}
	var order []string
	for i, row := range rows {
		n := i + 1
		if len(row) < cols.width() {
			return Catalog{}, parseErrorf("row %d has %d cells, expected at least %d", n, len(row), cols.width())
		}
		raw := row[cols.model]
		name := strings.TrimFunc(offpeakSuffix.ReplaceAllString(raw, ""), isSpace)
		if name == "" {
			return Catalog{}, parseErrorf("row %d has an empty model name", n)
		}
		if !modelName.MatchString(name) {
			return Catalog{}, parseErrorf("row %d model name %q is not an ollama tag", n, raw)
		}
		var t PriceTriple
		t.Input, t.Unknown.Input = price(row[cols.input], name+" input")
		t.Cache, t.Unknown.Cache = price(row[cols.cache], name+" cached input")
		t.Output, t.Unknown.Output = price(row[cols.output], name+" output")
		target := base
		if name != strings.TrimFunc(raw, isSpace) {
			target = offpeak
		}
		if _, dup := target[name]; dup {
			return Catalog{}, parseErrorf("duplicate row for %q", raw)
		}
		target[name] = t
		if name == strings.TrimFunc(raw, isSpace) {
			order = append(order, name)
		}
	}

	if seen > 0 && unrecognized*2 > seen {
		return Catalog{}, parseErrorf("%d of %d price cells unrecognized — price format changed?", unrecognized, seen)
	}
	var orphans []string
	for name := range offpeak {
		if _, ok := base[name]; !ok {
			orphans = append(orphans, name)
		}
	}
	if len(orphans) > 0 {
		sort.Strings(orphans)
		return Catalog{}, parseErrorf("off-peak rows with no base row: %q", orphans)
	}
	models := make([]CatalogModel, 0, len(order))
	for _, name := range order {
		m := CatalogModel{Name: name, Prices: base[name]}
		if op, ok := offpeak[name]; ok {
			m.Offpeak = &op
		}
		models = append(models, m)
	}
	return Catalog{Models: models, Warnings: warnings}, nil
}
```

- [ ] **Step 5: Run the tests to see that the module is missing**

Run (from `wt/`): `go test -count=1 ./internal/cloudsync`
Expected:

```text
# github.com/ohanaverse/local-ai-setup/wt/internal/cloudsync
internal/cloudsync/pricingpage.go:12:2: no required module provides package golang.org/x/net/html; to add it:
	go get golang.org/x/net/html
FAIL	github.com/ohanaverse/local-ai-setup/wt/internal/cloudsync [setup failed]
FAIL
```

- [ ] **Step 6: Add the dependency, and check what it does to the build**

The spec requires this to be run and its result recorded before the dependency is accepted. From `wt/`:

```bash
go get golang.org/x/net@v0.59.0
go mod tidy
go build ./...
git diff go.mod go.sum
```

Expected from `go get`:

```text
go: added golang.org/x/net v0.59.0
go: upgraded golang.org/x/sys v0.38.0 => v0.48.0
go: upgraded golang.org/x/text v0.3.8 => v0.42.0
```

`go build ./...` prints nothing. Expected `git diff` (this is the diff to paste into the PR description):

```diff
--- a/wt/go.mod
+++ b/wt/go.mod
@@ -9,7 +9,8 @@ require (
 	github.com/charmbracelet/lipgloss v1.1.0
 	github.com/charmbracelet/x/term v0.2.2
 	github.com/spf13/cobra v1.10.2
-	golang.org/x/sys v0.38.0
+	golang.org/x/net v0.59.0
+	golang.org/x/sys v0.48.0
 	gopkg.in/yaml.v3 v3.0.1
 )
 
@@ -35,5 +36,5 @@ require (
 	github.com/sahilm/fuzzy v0.1.1 // indirect
 	github.com/spf13/pflag v1.0.9 // indirect
 	github.com/xo/terminfo v0.0.0-20220910002029-abceb7e1c41e // indirect
-	golang.org/x/text v0.3.8 // indirect
+	golang.org/x/text v0.42.0 // indirect
 )
--- a/wt/go.sum
+++ b/wt/go.sum
@@ -63,12 +63,14 @@ github.com/xo/terminfo v0.0.0-20220910002029-abceb7e1c41e/go.mod h1:RbqR21r5mrJu
 go.yaml.in/yaml/v3 v3.0.4/go.mod h1:DhzuOOF2ATzADvBadXxruRBLzYTpT36CKvDb3+aBEFg=
 golang.org/x/exp v0.0.0-20231006140011-7918f672742d h1:jtJma62tbqLibJ5sFQz8bKtEM8rJBtfilJ2qTU199MI=
 golang.org/x/exp v0.0.0-20231006140011-7918f672742d/go.mod h1:ldy0pHrwJyGW56pPQzzkH36rKxoZW1tw7ZJpeKx+hdo=
+golang.org/x/net v0.59.0 h1:5zfYln+w5XCxwrnMMJPufRgNoXEaGxl0wo5GqPXyues=
+golang.org/x/net v0.59.0/go.mod h1:2DA/G1UfVbCpQPeWTmMPGY7Cs2PkBkwu743bVX5PIVg=
 golang.org/x/sys v0.0.0-20210809222454-d867a43fc93e/go.mod h1:oPkhp1MJrh7nUepCBck5+mAzfO9JrbApNNgaTdGDITg=
 golang.org/x/sys v0.6.0/go.mod h1:oPkhp1MJrh7nUepCBck5+mAzfO9JrbApNNgaTdGDITg=
-golang.org/x/sys v0.38.0 h1:3yZWxaJjBmCWXqhN1qh02AkOnCQ1poK6oF+a7xWL6Gc=
-golang.org/x/sys v0.38.0/go.mod h1:OgkHotnGiDImocRcuBABYBEXf8A9a87e/uXjp9XT3ks=
-golang.org/x/text v0.3.8 h1:nAL+RVCQ9uMn3vJZbV+MRnydTJFPf8qqY42YiA6MrqY=
-golang.org/x/text v0.3.8/go.mod h1:E6s5w1FMmriuDzIBO73fBruAKo1PCIq6d2Q6DHfQ8WQ=
+golang.org/x/sys v0.48.0 h1:bbX/i/6MgT9BVLM9RT1thmxL04yeTAhbEz4SyadbXoo=
+golang.org/x/sys v0.48.0/go.mod h1:hNLxWAXmnKAxqDtdwIYC4bM9oQPEecfsnNMuSxOs3og=
+golang.org/x/text v0.42.0 h1:JbOZXgfeCPU9gacVtYliJqOhD+zhrEqK4LfdpmlUZqI=
+golang.org/x/text v0.42.0/go.mod h1:ojzP1Z+2QtioaF8DTtO8K5q7JWVVYwZKenzujK0Zd0E=
 gopkg.in/check.v1 v0.0.0-20161208181325-20d25e280405 h1:yhCVgyC4o1eVCa2tZl7eS0r+SDo693bJlVdllGtEeKM=
 gopkg.in/check.v1 v0.0.0-20161208181325-20d25e280405/go.mod h1:Co6ibVJAznAaIkqp8huTwlJQCZ016jof/cbN4VW5Yz0=
 gopkg.in/yaml.v3 v3.0.1 h1:fxVm/GzAzEWqLHuvctI91KS9hhNmmWOoWu0XTYJS7CA=
```

If `go get` picks a newer `x/net`, or the diff is anything else, stop and tell the owner: a different version is a different dependency decision. Run `go get` only after Step 4, not before: with nothing importing the package, `go mod tidy` removes it again.

Then run the whole suite on the raised `x/sys` and `x/text` (Bubble Tea and the terminal code sit on them):

```bash
test -z "$(gofmt -l .)" && go vet ./... && go test -count=1 ./... && make check
go list -deps ./internal/cloudsync | grep '^golang.org/x/net'
```

Expected: 23 `ok` lines and no `FAIL`; `make check` ends with the Go format check and no error; and the last command prints exactly

```text
golang.org/x/net/html/atom
golang.org/x/net/html
```

- [ ] **Step 7: Run the parser's tests to verify they pass**

Run (from `wt/`): `go test -count=1 ./internal/cloudsync`
Expected:

```text
ok  	github.com/ohanaverse/local-ai-setup/wt/internal/cloudsync	(time)
```

(`-v` lists 7 passing top-level tests.)

- [ ] **Step 8: Commit**

From the repo root:

```bash
git add wt/go.mod wt/go.sum wt/internal/cloudsync
git commit -m "feat(wt): cloudsync.ParsePricing, the ollama pricing-page parser, on golang.org/x/net/html"
```

### Task 2: Registry rows as the planners read them, and cloud-tag resolution

**Files:**
- Create: `wt/internal/cloudsync/tags_test.go`, `wt/internal/cloudsync/entry.go`, `wt/internal/cloudsync/tags.go`

**Interfaces:**
- Consumes: `tomlw.Table` (`Get`, `Has`, `Keys`; `internal/tomlw/table.go`). A table's values are `bool`, `int64`, `float64`, `string`, `time.Time`, `[]any` or `*tomlw.Table`.
- Produces:
  - `type Entry struct { ID, Family, ProviderID, ModelName, Location, CatalogName string; Cost *Cost }`
  - `type Cost struct { Input, Cache, Output, SubscriptionPrice *float64; SubscriptionPeriod *string; TimePrices []*tomlw.Table }`
  - `type Provider struct { ID, Location, AuthType string; OpenRouterPriced *bool }`
  - `func Entries(rows []*tomlw.Table) []Entry`, `func Providers(rows []*tomlw.Table) []Provider` (fed with `RegistryDoc.Models()` and `.Providers()`)
  - `const CatalogNameKey = "catalog_name"`, `const LibraryTagsURL = "https://ollama.com/library/%s/tags"`
  - `type Getter func(ctx context.Context, url string) ([]byte, error)`
  - `func CloudTag(name string) string`, `func IsCloudTag(tag string) bool`
  - `func VerifiedTags(entries []Entry, pulled []string) map[string]string`
  - `func ResolveCloudTags(ctx context.Context, get Getter, names []string, known map[string]string) (map[string]string, []string)`; `""` in the map is "tag unknown"
  - unexported, used by later tasks: `str(t *tomlw.Table, key string) string`, `number(t *tomlw.Table, key string) *float64`, `costOf(t *tomlw.Table) *Cost`, `ollamaProvider = "ollama"`

- [ ] **Step 1: Write the failing tests**

Create `wt/internal/cloudsync/tags_test.go`:

```go
package cloudsync

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// tagsPage is a library tags page listing the given tags for name, plus a
// link to another model whose name starts the same way.
func tagsPage(name string, tags ...string) string {
	var b strings.Builder
	b.WriteString("<html>")
	for _, t := range tags {
		fmt.Fprintf(&b, `<a href="/library/%s:%s">%s:%s</a>`, name, t, name, t)
	}
	fmt.Fprintf(&b, `<a href="/library/%s-other:cloud">x</a></html>`, name)
	return b.String()
}

// fakeLibrary serves pages by URL and records which URLs were asked for. A
// URL with no page is a 404.
type fakeLibrary struct {
	mu    sync.Mutex
	pages map[string]string
	asked []string
}

func (l *fakeLibrary) get(_ context.Context, url string) ([]byte, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.asked = append(l.asked, url)
	page, ok := l.pages[url]
	if !ok {
		return nil, errors.New("HTTP 404")
	}
	return []byte(page), nil
}

// TestCloudTag pins the guess and the cloud-tag test. IsCloudTag decides
// which pulled tags and registry entries the catalog flow may remove, so a
// local model such as gpt-oss:20b must never pass it.
func TestCloudTag(t *testing.T) {
	if got := CloudTag("glm-5.3"); got != "glm-5.3:cloud" {
		t.Errorf("CloudTag(glm-5.3) = %q", got)
	}
	if got := CloudTag("gpt-oss:120b"); got != "gpt-oss:120b-cloud" {
		t.Errorf("CloudTag(gpt-oss:120b) = %q", got)
	}
	if !IsCloudTag("glm-5.3:cloud") || !IsCloudTag("gpt-oss:120b-cloud") || IsCloudTag("gpt-oss:20b") {
		t.Error("IsCloudTag: want true for :cloud and -cloud tags, false for gpt-oss:20b")
	}
}

// TestResolveCloudTags pins how a page name becomes the tag ollama really
// publishes: the :cloud alias wins, else the single sized cloud tag; several
// sized tags, none, or an unreadable page give no tag and a warning. A
// guessed tag would be registered, fail to pull, and be removed and re-added
// on every run.
func TestResolveCloudTags(t *testing.T) {
	const base = "https://ollama.com/library/"
	lib := &fakeLibrary{pages: map[string]string{
		base + "glm-5.3/tags":         tagsPage("glm-5.3", "cloud", "latest"),
		base + "mistral-large-3/tags": tagsPage("mistral-large-3", "675b-cloud", "latest"),
		base + "two/tags":             tagsPage("two", "8b-cloud", "70b-cloud"),
		base + "none/tags":            tagsPage("none", "latest", "8b"),
	}}
	names := []string{"glm-5.3", "mistral-large-3", "two", "none", "gone", "gpt-oss:120b"}
	resolved, warnings := ResolveCloudTags(context.Background(), lib.get, names, nil)
	want := map[string]string{
		"glm-5.3":         "glm-5.3:cloud",
		"mistral-large-3": "mistral-large-3:675b-cloud",
		"two":             "",
		"none":            "",
		"gone":            "",
		// A page name that already names a size pins its tag; no lookup.
		"gpt-oss:120b": "gpt-oss:120b-cloud",
	}
	if !reflect.DeepEqual(resolved, want) {
		t.Errorf("resolved = %v\nwant %v", resolved, want)
	}
	wantWarnings := []string{
		"two: several cloud tags (70b-cloud, 8b-cloud) on ollama.com/library; skipped",
		"none: no cloud tag on ollama.com/library; skipped",
		"gone: could not read its ollama.com/library tags (HTTP 404); skipped",
	}
	if !reflect.DeepEqual(warnings, wantWarnings) {
		t.Errorf("warnings = %q\nwant %q", warnings, wantWarnings)
	}
	if len(lib.asked) != 5 {
		t.Errorf("looked up %d pages (%v), want 5: every bare name and not gpt-oss:120b", len(lib.asked), lib.asked)
	}
}

// TestResolveCloudTagsSkipsKnownNames pins that a name whose tag is already
// pulled is not looked up again. A routine sync would otherwise fetch one
// library page per model on every run, and fail as a whole when ollama.com's
// library is down although nothing needs resolving.
func TestResolveCloudTagsSkipsKnownNames(t *testing.T) {
	boom := func(_ context.Context, url string) ([]byte, error) {
		t.Errorf("fetched %s", url)
		return nil, errors.New("no")
	}
	resolved, warnings := ResolveCloudTags(context.Background(), boom, []string{"ml3"}, map[string]string{"ml3": "ml3:675b-cloud"})
	if resolved["ml3"] != "ml3:675b-cloud" || len(warnings) != 0 {
		t.Errorf("resolved = %v, warnings = %q", resolved, warnings)
	}
}

// TestResolveCloudTagsKeepsOrderUnderConcurrency pins that the lookups, which
// run several at a time, still give warnings in the order of the page. The
// plan prints them, and the command compares the printed plan with a re-plan:
// warnings in a varying order would make every apply look like a changed plan.
func TestResolveCloudTagsKeepsOrderUnderConcurrency(t *testing.T) {
	var names, want []string
	for i := range 40 {
		name := fmt.Sprintf("m%02d", i)
		names = append(names, name)
		want = append(want, name+": could not read its ollama.com/library tags (HTTP 404); skipped")
	}
	lib := &fakeLibrary{}
	for range 5 {
		if _, warnings := ResolveCloudTags(context.Background(), lib.get, names, nil); !reflect.DeepEqual(warnings, want) {
			t.Fatalf("warnings out of page order: %q", warnings)
		}
	}
}

// TestVerifiedTags pins which registry tags are trusted without a lookup:
// only an entry that records its catalog name and whose tag is pulled. An
// unpulled tag may be an earlier guess, and trusting it would keep the guess
// alive forever.
func TestVerifiedTags(t *testing.T) {
	entries := []Entry{
		{ID: "ollama/a:1t-cloud", ProviderID: "ollama", ModelName: "a:1t-cloud", CatalogName: "a"},
		{ID: "ollama/b:cloud", ProviderID: "ollama", ModelName: "b:cloud", CatalogName: "b"}, // not pulled
		{ID: "ollama/c:cloud", ProviderID: "ollama", ModelName: "c:cloud"},                   // no catalog name
		{ID: "other/d:cloud", ProviderID: "other", ModelName: "d:cloud", CatalogName: "d"},   // not ollama
	}
	got := VerifiedTags(entries, []string{"a:1t-cloud", "c:cloud", "d:cloud"})
	if want := map[string]string{"a": "a:1t-cloud"}; !reflect.DeepEqual(got, want) {
		t.Errorf("VerifiedTags = %v, want %v", got, want)
	}
}

// TestVerifiedTagsSkipsANameTwoPulledEntriesClaim pins the interrupted
// re-tag: the guessed tag and the real one are both pulled and both carry the
// name. Answering from the registry picks one by position; leaving it out
// hands the question to the library lookup, so the current entry is matched
// and the stale one is what gets re-tagged away.
func TestVerifiedTagsSkipsANameTwoPulledEntriesClaim(t *testing.T) {
	entries := []Entry{
		{ID: "ollama/foo:cloud", ProviderID: "ollama", ModelName: "foo:cloud", CatalogName: "foo"},
		{ID: "ollama/foo:675b-cloud", ProviderID: "ollama", ModelName: "foo:675b-cloud", CatalogName: "foo"},
		{ID: "ollama/bar:cloud", ProviderID: "ollama", ModelName: "bar:cloud", CatalogName: "bar"},
	}
	got := VerifiedTags(entries, []string{"foo:cloud", "foo:675b-cloud", "bar:cloud"})
	if want := map[string]string{"bar": "bar:cloud"}; !reflect.DeepEqual(got, want) {
		t.Errorf("VerifiedTags = %v, want %v", got, want)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run (from `wt/`): `go test -count=1 ./internal/cloudsync`
Expected:

```text
# github.com/ohanaverse/local-ai-setup/wt/internal/cloudsync [github.com/ohanaverse/local-ai-setup/wt/internal/cloudsync.test]
internal/cloudsync/tags_test.go:48:12: undefined: CloudTag
internal/cloudsync/tags_test.go:51:12: undefined: CloudTag
internal/cloudsync/tags_test.go:54:6: undefined: IsCloudTag
internal/cloudsync/tags_test.go:73:24: undefined: ResolveCloudTags
```

- [ ] **Step 3: Write the row views and the tag resolution**

Create `wt/internal/cloudsync/entry.go`:

```go
package cloudsync

import "github.com/ohanaverse/local-ai-setup/wt/internal/tomlw"

// Entry is what the planners read of one registry model row. It is built
// from the row as written, not from config.Model: the planners need keys
// wt's typed reader does not model (catalog_name, the time_prices rows with
// whatever keys they hold).
type Entry struct {
	ID, Family, ProviderID, ModelName, Location string
	// CatalogName is the page name an earlier sync recorded on the row, so a
	// model stays matched after its tag is renamed. "" when there is none.
	CatalogName string
	// Cost is nil when the row has no cost table.
	Cost *Cost
}

// Cost is a row's cost table as the planners read it. A nil price is a key
// that is not there.
type Cost struct {
	Input, Cache, Output *float64
	SubscriptionPrice    *float64
	// SubscriptionPeriod is nil when the key is absent.
	SubscriptionPeriod *string
	// TimePrices are the cost.time_prices rows, whole, in file order.
	TimePrices []*tomlw.Table
}

// Provider is what the planners read of one provider row.
type Provider struct {
	ID, Location, AuthType string
	// OpenRouterPriced is the row's openrouter_priced override: nil when the
	// key is absent (or is not a boolean, which wt's loader refuses anyway).
	OpenRouterPriced *bool
}

// Entries reads model rows (config.RegistryDoc.Models) into Entry values. A
// row with no string id is skipped: nothing can address it.
func Entries(rows []*tomlw.Table) []Entry {
	out := make([]Entry, 0, len(rows))
	for _, row := range rows {
		id := str(row, "id")
		if id == "" {
			continue
		}
		e := Entry{
			ID: id, Family: str(row, "family"), ProviderID: str(row, "provider_id"),
			ModelName: str(row, "model_name"), Location: str(row, "location"),
			CatalogName: str(row, CatalogNameKey),
		}
		if v, ok := row.Get("cost"); ok {
			if t, isTable := v.(*tomlw.Table); isTable {
				e.Cost = costOf(t)
			}
		}
		out = append(out, e)
	}
	return out
}

// Providers reads provider rows (config.RegistryDoc.Providers).
func Providers(rows []*tomlw.Table) []Provider {
	out := make([]Provider, 0, len(rows))
	for _, row := range rows {
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
	}
	return out
}

func costOf(t *tomlw.Table) *Cost {
	c := &Cost{
		Input:             number(t, "input_price_per_million"),
		Cache:             number(t, "cache_price_per_million"),
		Output:            number(t, "output_price_per_million"),
		SubscriptionPrice: number(t, "subscription_price"),
	}
	if v, ok := t.Get("subscription_period"); ok {
		if s, isString := v.(string); isString {
			c.SubscriptionPeriod = &s
		}
	}
	if v, ok := t.Get("time_prices"); ok {
		if arr, isArray := v.([]any); isArray {
			for _, item := range arr {
				if row, isTable := item.(*tomlw.Table); isTable {
					c.TimePrices = append(c.TimePrices, row)
				}
			}
		}
	}
	return c
}

func str(t *tomlw.Table, key string) string {
	v, _ := t.Get(key)
	s, _ := v.(string)
	return s
}

// number reads a price: a TOML integer or float. Anything else is no price.
func number(t *tomlw.Table, key string) *float64 {
	v, _ := t.Get(key)
	switch n := v.(type) {
	case int64:
		f := float64(n)
		return &f
	case float64:
		return &n
	}
	return nil
}
```

Create `wt/internal/cloudsync/tags.go`:

```go
package cloudsync

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
)

// CatalogNameKey is the model-row key that records which page name a row was
// matched to.
const CatalogNameKey = "catalog_name"

// LibraryTagsURL is the page that lists the tags ollama publishes for a
// model; %s is the page name.
const LibraryTagsURL = "https://ollama.com/library/%s/tags"

const ollamaProvider = "ollama"

// tagLookups is how many library pages are read at once.
const tagLookups = 8

// Getter fetches one URL's body. A non-2xx answer is an error.
type Getter func(ctx context.Context, url string) ([]byte, error)

// CloudTag is the guess at a page name's pulled tag: `glm-5.3` gives
// `glm-5.3:cloud`, `gpt-oss:120b` gives `gpt-oss:120b-cloud`. Only a name
// that already pins a size is sure to be right; ResolveCloudTags checks the
// rest.
func CloudTag(name string) string {
	if strings.Contains(name, ":") {
		return name + "-cloud"
	}
	return name + ":cloud"
}

// IsCloudTag reports whether tag names an ollama cloud model.
func IsCloudTag(tag string) bool {
	return strings.HasSuffix(tag, ":cloud") || strings.HasSuffix(tag, "-cloud")
}

// VerifiedTags maps a page name to its tag for the catalog entries whose tag
// `ollama list` shows: a pulled tag exists, so it need not be looked up
// again.
//
// A name two pulled entries claim (an earlier re-tag that saved its addition
// and was killed before it removed the entry it replaced) is left out. Which
// of the two the page publishes is what the library lookup answers; answering
// it from the registry would pick one by position.
func VerifiedTags(entries []Entry, pulled []string) map[string]string {
	seen := map[string]string{}
	ambiguous := map[string]bool{}
	for _, e := range entries {
		if e.ProviderID != ollamaProvider || e.CatalogName == "" || !slices.Contains(pulled, e.ModelName) {
			continue
		}
		if tag, ok := seen[e.CatalogName]; ok && tag != e.ModelName {
			ambiguous[e.CatalogName] = true
		}
		seen[e.CatalogName] = e.ModelName
	}
	for name := range ambiguous {
		delete(seen, name)
	}
	return seen
}

// lookupCloudTag reads a bare page name's cloud tag off its library page:
// `<name>:cloud` wins, else the single `<name>:*-cloud` tag.
func lookupCloudTag(ctx context.Context, get Getter, name string) (tag, warning string) {
	body, err := get(ctx, fmt.Sprintf(LibraryTagsURL, name))
	if err != nil {
		return "", fmt.Sprintf("%s: could not read its ollama.com/library tags (%v); skipped", name, err)
	}
	link := regexp.MustCompile(`href="/library/` + regexp.QuoteMeta(name) + `:([A-Za-z0-9._-]+)"`)
	found := map[string]bool{}
	for _, m := range link.FindAllStringSubmatch(string(body), -1) {
		found[m[1]] = true
	}
	if found["cloud"] {
		return name + ":cloud", ""
	}
	var sized []string
	for t := range found {
		if strings.HasSuffix(t, "-cloud") {
			sized = append(sized, t)
		}
	}
	sort.Strings(sized)
	switch len(sized) {
	case 1:
		return name + ":" + sized[0], ""
	case 0:
		return "", name + ": no cloud tag on ollama.com/library; skipped"
	}
	return "", fmt.Sprintf("%s: several cloud tags (%s) on ollama.com/library; skipped", name, strings.Join(sized, ", "))
}

// ResolveCloudTags maps each page name to the cloud tag ollama publishes for
// it. "" means the tag is unknown: no cloud tag, several, or a library page
// that could not be read. Each of those comes with a warning, and none is
// ever a guess.
//
// A name that pins a size (`gpt-oss:120b`) maps by CloudTag with no lookup.
// A name in known (VerifiedTags) keeps its pulled tag. The rest are looked
// up, tagLookups at a time; the warnings come back in the order of names.
func ResolveCloudTags(ctx context.Context, get Getter, names []string, known map[string]string) (map[string]string, []string) {
	resolved := make(map[string]string, len(names))
	var lookups []string
	for _, name := range names {
		switch {
		case strings.Contains(name, ":"):
			resolved[name] = CloudTag(name)
		case known[name] != "":
			resolved[name] = known[name]
		default:
			lookups = append(lookups, name)
		}
	}
	tags := make([]string, len(lookups))
	warns := make([]string, len(lookups))
	var wg sync.WaitGroup
	slots := make(chan struct{}, tagLookups)
	for i, name := range lookups {
		wg.Add(1)
		go func() {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			tags[i], warns[i] = lookupCloudTag(ctx, get, name)
		}()
	}
	wg.Wait()
	var warnings []string
	for i, name := range lookups {
		resolved[name] = tags[i]
		if warns[i] != "" {
			warnings = append(warnings, warns[i])
		}
	}
	return resolved, warnings
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run (from `wt/`): `go test -count=1 ./internal/cloudsync`
Expected:

```text
ok  	github.com/ohanaverse/local-ai-setup/wt/internal/cloudsync	(time)
```

(`-v` lists 13 passing top-level tests.) Run it once more with `-race`: `go test -count=1 -race ./internal/cloudsync`. The lookups run concurrently and `TestResolveCloudTagsKeepsOrderUnderConcurrency` is what would catch a shared-slice mistake.

- [ ] **Step 5: Commit**

From the repo root:

```bash
git add wt/internal/cloudsync
git commit -m "feat(wt): cloudsync reads registry rows and resolves ollama cloud tags"
```

### Task 3: The catalog planner, the mass-removal guard and the removal digest

**Files:**
- Create: `wt/internal/cloudsync/catalog_test.go`, `wt/internal/cloudsync/catalog.go`

**Interfaces:**
- Consumes: Task 1 (`Catalog`, `CatalogModel`, `PriceTriple`, `samePrice`) and Task 2 (`Entry`, `Cost`, `CloudTag`, `IsCloudTag`, `str`, `number`, `ollamaProvider`); `tomlw.NewTable`, `(*Table).Set`, `tomlw.Same(a, b any) bool`.
- Produces:
  - `const OffpeakLabel = "off-peak"`
  - `type CostUpdate struct { ModelID, CatalogName string; Before *Cost; After Cost }`
  - `type Addition struct { ID, Family, ModelName, CatalogName string; Cost Cost; CloneOf string }`
  - `type Pull struct{ ID, Tag string }`
  - `type CatalogPlan struct { CatalogSize, CloudEntries int; Updates []CostUpdate; Unchanged []string; Additions []Addition; Pulls []Pull; Removals, StrayTags []string; Replaced map[string]string; Warnings []string }`
  - `func PlanCatalog(entries []Entry, catalog Catalog, pulled []string, resolved map[string]string) *CatalogPlan`
  - `func (p *CatalogPlan) MassRemoval() bool`, `RemovalDigest() string` (`""` when the plan deletes nothing), `HasWork() bool`, `Format() string`
  - unexported, used by Tasks 4 and 5: `sameCost(before *Cost, after Cost) bool`, `formatCost(c *Cost) string`, `formatPrice(v *float64) string`, `offpeakRow(input, cache, output *float64) *tomlw.Table`; test helpers `cloudEntry`, `withCost`, `withName`, `withFamily`, `local`, `cm`, `withOffpeak`, `catalogOf`, `timeRow`, `wantIDs`

- [ ] **Step 1: Write the failing tests**

Create `wt/internal/cloudsync/catalog_test.go`:

```go
package cloudsync

import (
	"reflect"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/tomlw"
)

// cloudEntry is an ollama cloud entry under tag, as an earlier sync or a
// hand edit would have left it.
func cloudEntry(tag string, edit ...func(*Entry)) Entry {
	e := Entry{ID: "ollama/" + tag, Family: "fam", ProviderID: "ollama", ModelName: tag, Location: "cloud"}
	for _, fn := range edit {
		fn(&e)
	}
	return e
}

func withCost(c Cost) func(*Entry)        { return func(e *Entry) { e.Cost = &c } }
func withName(name string) func(*Entry)   { return func(e *Entry) { e.CatalogName = name } }
func withFamily(name string) func(*Entry) { return func(e *Entry) { e.Family = name } }
func local(e *Entry)                      { e.Location = "local" }

// cm is a page model at 1.0/0.1/2.0 unless prices are given.
func cm(name string, prices ...float64) CatalogModel {
	p := []float64{1.0, 0.1, 2.0}
	copy(p, prices)
	return CatalogModel{Name: name, Prices: PriceTriple{Input: f(p[0]), Cache: f(p[1]), Output: f(p[2])}}
}

func withOffpeak(m CatalogModel, in, cache, out *float64) CatalogModel {
	m.Offpeak = &PriceTriple{Input: in, Cache: cache, Output: out}
	return m
}

func catalogOf(models ...CatalogModel) Catalog { return Catalog{Models: models} }

// timeRow is a time_prices row with one window, for rows the user wrote.
func timeRow(label, day string) *tomlw.Table {
	w := tomlw.NewTable()
	w.Set("days", []any{day})
	w.Set("start", "00:00")
	w.Set("end", "24:00")
	row := tomlw.NewTable()
	row.Set("label", label)
	row.Set("timezone", "UTC")
	row.Set("windows", []any{w})
	return row
}

func updateIDs(p *CatalogPlan) []string {
	var ids []string
	for _, u := range p.Updates {
		ids = append(ids, u.ModelID)
	}
	return ids
}

func additionIDs(p *CatalogPlan) []string {
	var ids []string
	for _, a := range p.Additions {
		ids = append(ids, a.ID)
	}
	return ids
}

func pullIDs(p *CatalogPlan) []string {
	var ids []string
	for _, pull := range p.Pulls {
		ids = append(ids, pull.ID)
	}
	return ids
}

func labels(rows []*tomlw.Table) []string {
	var out []string
	for _, row := range rows {
		out = append(out, str(row, "label"))
	}
	return out
}

func wantIDs(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

// TestPlanUpdatesExistingAndRecordsTheCatalogName pins the everyday case: an
// entry under the page's tag takes the page's prices, keeps its subscription,
// and is recorded as that page model. Losing the subscription here would
// erase the plan price from every ollama cloud entry on each sync.
func TestPlanUpdatesExistingAndRecordsTheCatalogName(t *testing.T) {
	old := Cost{Input: f(9), SubscriptionPrice: f(100), SubscriptionPeriod: s("month")}
	plan := PlanCatalog([]Entry{cloudEntry("glm-5.3:cloud", withCost(old))}, catalogOf(cm("glm-5.3", 1.4, 0.26, 4.4)), nil, nil)
	if len(plan.Updates) != 1 || len(plan.Additions) != 0 {
		t.Fatalf("updates = %v, additions = %v; want one update", updateIDs(plan), additionIDs(plan))
	}
	u := plan.Updates[0]
	if u.ModelID != "ollama/glm-5.3:cloud" || u.CatalogName != "glm-5.3" || !samePrice(u.After.Input, f(1.4)) ||
		!samePrice(u.After.SubscriptionPrice, f(100)) {
		t.Errorf("update = %+v, want glm-5.3 at 1.4 with the subscription untouched", u)
	}
}

// TestPlanUnchangedWhenPricesMatchAndNameRecorded pins that a second sync of
// the same page plans nothing. An entry that looked "updated" on every run
// would rewrite the registry and restart the LiteLLM proxy each time.
func TestPlanUnchangedWhenPricesMatchAndNameRecorded(t *testing.T) {
	cost := Cost{Input: f(1), Cache: f(0.1), Output: f(2)}
	plan := PlanCatalog([]Entry{cloudEntry("glm-5.3:cloud", withCost(cost), withName("glm-5.3"))}, catalogOf(cm("glm-5.3")), []string{"glm-5.3:cloud"}, nil)
	wantIDs(t, "updates", updateIDs(plan))
	wantIDs(t, "unchanged", plan.Unchanged, "ollama/glm-5.3:cloud")
	if plan.HasWork() {
		t.Error("a plan with nothing to change reports work")
	}

	// An entry with no cost table against a page row with no prices: nothing
	// to write, so nothing changed. modelman called a missing table and an
	// empty one different and listed this entry as an update once.
	bare := PlanCatalog([]Entry{cloudEntry("free:cloud", withName("free"))}, catalogOf(CatalogModel{Name: "free"}), []string{"free:cloud"}, nil)
	wantIDs(t, "updates (no cost, no page prices)", updateIDs(bare))
	wantIDs(t, "unchanged (no cost, no page prices)", bare.Unchanged, "ollama/free:cloud")
	if bare.HasWork() {
		t.Error("an entry with no cost against a page row with no prices reports work")
	}
}

// TestOffpeakRowIsOllamasPublishedWindow pins the one row the catalog flow
// writes into cost.time_prices, as it lands in registry.toml: ollama's
// off-peak window (outside 12:00 to 18:00 UTC on weekdays, all day at
// weekends), keys in the order modelman writes them, and a price the page
// does not list left out. The window is not parsed from the page, so this is
// where a change to it shows.
func TestOffpeakRowIsOllamasPublishedWindow(t *testing.T) {
	doc := tomlw.NewTable()
	doc.Set("time_prices", []any{offpeakRow(f(0.66), nil, f(1.98))})
	got, err := tomlw.Encode(doc)
	if err != nil {
		t.Fatal(err)
	}
	const want = `[[time_prices]]
label = "off-peak"
timezone = "UTC"
input_price_per_million = 0.66
output_price_per_million = 1.98

[[time_prices.windows]]
days = [
    "mon",
    "tue",
    "wed",
    "thu",
    "fri",
]
start = "00:00"
end = "12:00"

[[time_prices.windows]]
days = [
    "mon",
    "tue",
    "wed",
    "thu",
    "fri",
]
start = "18:00"
end = "24:00"

[[time_prices.windows]]
days = [
    "sat",
    "sun",
]
start = "00:00"
end = "24:00"
`
	if string(got) != want {
		t.Errorf("off-peak row =\n%s\nwant\n%s", got, want)
	}
}

// TestPlanOffpeakSetReplaceRemoveKeepsOtherRows pins that the catalog flow
// owns exactly one time_prices row, the one labelled off-peak: it is set when
// the page has off-peak prices and removed when it does not, and a row the
// user wrote under another label survives both.
func TestPlanOffpeakSetReplaceRemoveKeepsOtherRows(t *testing.T) {
	cost := Cost{Input: f(1), TimePrices: []*tomlw.Table{timeRow("mine", "sun"), timeRow("off-peak", "sat")}}
	entries := []Entry{cloudEntry("a:cloud", withCost(cost)), cloudEntry("b:cloud", withCost(cost))}
	plan := PlanCatalog(entries, catalogOf(withOffpeak(cm("a"), f(0.5), nil, f(1)), cm("b")), nil, nil)
	after := map[string]Cost{}
	for _, u := range plan.Updates {
		after[u.ModelID] = u.After
	}
	a := after["ollama/a:cloud"].TimePrices
	wantIDs(t, "a's rows", labels(a), "mine", "off-peak")
	if !samePrice(number(a[1], "input_price_per_million"), f(0.5)) || a[1].Has("cache_price_per_million") {
		t.Errorf("a's off-peak row has the wrong prices: keys %v", a[1].Keys())
	}
	windows, _ := a[1].Get("windows")
	if n := len(windows.([]any)); n != 3 {
		t.Errorf("a's off-peak row has %d windows, want ollama's 3 (the stale Saturday-only row replaced)", n)
	}
	wantIDs(t, "b's rows", labels(after["ollama/b:cloud"].TimePrices), "mine")
}

// TestPlanOffpeakReplacedInPlaceAndUnchangedWhenSame pins two things about
// the off-peak row: it stays where it stands among the other rows (the first
// matching row wins, so moving it changes which price applies), and a row
// that already says what the page says is not an update.
func TestPlanOffpeakReplacedInPlaceAndUnchangedWhenSame(t *testing.T) {
	cost := Cost{Input: f(1), Cache: f(0.1), Output: f(2),
		TimePrices: []*tomlw.Table{offpeakRow(f(0.5), f(0.05), f(1)), timeRow("holiday", "sun")}}
	entries := []Entry{cloudEntry("a:cloud", withCost(cost), withName("a"))}

	same := PlanCatalog(entries, catalogOf(withOffpeak(cm("a"), f(0.5), f(0.05), f(1))), nil, nil)
	wantIDs(t, "updates when the row matches", updateIDs(same))
	wantIDs(t, "unchanged", same.Unchanged, "ollama/a:cloud")

	changed := PlanCatalog(entries, catalogOf(withOffpeak(cm("a"), f(0.4), f(0.04), f(0.8))), nil, nil)
	if len(changed.Updates) != 1 {
		t.Fatalf("updates = %v, want one", updateIDs(changed))
	}
	rows := changed.Updates[0].After.TimePrices
	wantIDs(t, "rows", labels(rows), "off-peak", "holiday")
	if !samePrice(number(rows[0], "input_price_per_million"), f(0.4)) {
		t.Error("the off-peak row was not given the page's new price")
	}
}

// TestPlanMatchesByCatalogNameAfterRename pins that an entry whose tag the
// user renamed is still that page model: it is updated, and neither it nor
// its pulled tag is treated as off the page and removed.
func TestPlanMatchesByCatalogNameAfterRename(t *testing.T) {
	entries := []Entry{cloudEntry("glm-5.3-renamed:cloud", withName("glm-5.3"))}
	plan := PlanCatalog(entries, catalogOf(cm("glm-5.3")), []string{"glm-5.3-renamed:cloud"}, nil)
	wantIDs(t, "updates", updateIDs(plan), "ollama/glm-5.3-renamed:cloud")
	wantIDs(t, "additions", additionIDs(plan))
	wantIDs(t, "pulls", pullIDs(plan))
	wantIDs(t, "removals", plan.Removals)
	wantIDs(t, "stray tags", plan.StrayTags)
}

// TestPlanCatalogNameMatchKeepsTheCanonicalTagListed pins the other half of
// a rename: with both the renamed tag and the page's own tag pulled, neither
// is a stray to `ollama rm`.
func TestPlanCatalogNameMatchKeepsTheCanonicalTagListed(t *testing.T) {
	entries := []Entry{cloudEntry("gpt-oss:120b-cloud-custom", withName("gpt-oss:120b"))}
	plan := PlanCatalog(entries, catalogOf(cm("gpt-oss:120b")), []string{"gpt-oss:120b-cloud", "gpt-oss:120b-cloud-custom"}, nil)
	wantIDs(t, "removals", plan.Removals)
	wantIDs(t, "stray tags", plan.StrayTags)
}

// TestPlanAdditionsFamilyAndSubscription pins what a new entry is given: the
// family of a namesake already in the registry (so the cloud and local sizes
// of one model group together in the picker), else the name's stem; and the
// subscription every existing ollama cloud entry shares.
func TestPlanAdditionsFamilyAndSubscription(t *testing.T) {
	sub := Cost{SubscriptionPrice: f(100), SubscriptionPeriod: s("month")}
	entries := []Entry{
		cloudEntry("gpt-oss:20b", withFamily("gpt-oss"), local),
		cloudEntry("glm-5.2:cloud", withFamily("glm"), withCost(sub)),
	}
	plan := PlanCatalog(entries, catalogOf(cm("glm-5.2"), cm("gpt-oss:120b"), cm("kimi-k3")), nil, nil)
	added := map[string]Addition{}
	for _, a := range plan.Additions {
		added[a.ID] = a
	}
	gpt := added["ollama/gpt-oss:120b-cloud"]
	if gpt.Family != "gpt-oss" || gpt.ModelName != "gpt-oss:120b-cloud" || gpt.CatalogName != "gpt-oss:120b" || gpt.CloneOf != "" {
		t.Errorf("gpt-oss:120b addition = %+v", gpt)
	}
	if !samePrice(gpt.Cost.SubscriptionPrice, f(100)) || gpt.Cost.SubscriptionPeriod == nil || *gpt.Cost.SubscriptionPeriod != "month" {
		t.Errorf("the new entry did not inherit the shared subscription: %+v", gpt.Cost)
	}
	if got := added["ollama/kimi-k3:cloud"].Family; got != "kimi-k3" {
		t.Errorf("kimi-k3 family = %q, want its own name", got)
	}
}

// TestPlanSubscriptionDisagreementWarns pins that when the existing entries
// disagree on the subscription, a new entry gets none and the plan says so:
// guessing one of two prices would show a wrong monthly cost in the picker.
func TestPlanSubscriptionDisagreementWarns(t *testing.T) {
	entries := []Entry{
		cloudEntry("a:cloud", withCost(Cost{SubscriptionPrice: f(100), SubscriptionPeriod: s("month")})),
		cloudEntry("b:cloud", withCost(Cost{SubscriptionPrice: f(20), SubscriptionPeriod: s("month")})),
	}
	plan := PlanCatalog(entries, catalogOf(cm("a"), cm("b"), cm("new")), nil, nil)
	if len(plan.Additions) != 1 || plan.Additions[0].Cost.SubscriptionPrice != nil {
		t.Fatalf("additions = %+v, want one with no subscription", plan.Additions)
	}
	if want := []string{"ollama cloud entries disagree on subscription pricing; new entries get none"}; !reflect.DeepEqual(plan.Warnings, want) {
		t.Errorf("warnings = %q, want %q", plan.Warnings, want)
	}
}

// TestPlanDoesNotTouchALocalNamesake pins the rule the whole flow rests on:
// a real local model is never updated, removed or `ollama rm`'d, even when
// the page lists a cloud model of the same name and size.
func TestPlanDoesNotTouchALocalNamesake(t *testing.T) {
	entries := []Entry{cloudEntry("gpt-oss:20b", withFamily("gpt-oss"), local)}
	plan := PlanCatalog(entries, catalogOf(cm("gpt-oss:20b")), []string{"gpt-oss:20b"}, nil)
	wantIDs(t, "updates", updateIDs(plan))
	wantIDs(t, "additions", additionIDs(plan), "ollama/gpt-oss:20b-cloud")
	wantIDs(t, "removals", plan.Removals)
	wantIDs(t, "stray tags", plan.StrayTags)
}

// TestPlanPullsPageModelsNotInOllamaList pins the pull list: every page
// model whose tag `ollama list` lacks, existing entry or new, and none that
// is already pulled.
func TestPlanPullsPageModelsNotInOllamaList(t *testing.T) {
	plan := PlanCatalog([]Entry{cloudEntry("a:cloud"), cloudEntry("b:cloud")}, catalogOf(cm("a"), cm("b"), cm("c")), []string{"a:cloud"}, nil)
	want := []Pull{{ID: "ollama/b:cloud", Tag: "b:cloud"}, {ID: "ollama/c:cloud", Tag: "c:cloud"}}
	if !reflect.DeepEqual(plan.Pulls, want) {
		t.Errorf("pulls = %v, want %v", plan.Pulls, want)
	}
}

// TestPlanRemovesOffPageCloudEntriesAndStrayStubs pins the mirror's other
// direction: a cloud entry the page no longer lists goes whether or not it is
// pulled, a pulled cloud stub with no entry is a stray to remove, and a local
// model is in neither list.
func TestPlanRemovesOffPageCloudEntriesAndStrayStubs(t *testing.T) {
	entries := []Entry{cloudEntry("old:cloud"), cloudEntry("gone:cloud"), cloudEntry("keep:cloud"), cloudEntry("ornith-1.5:35b", local)}
	plan := PlanCatalog(entries, catalogOf(cm("keep")), []string{"old:cloud", "stray:cloud", "ornith-1.5:35b", "keep:cloud"}, nil)
	wantIDs(t, "removals", plan.Removals, "ollama/old:cloud", "ollama/gone:cloud")
	wantIDs(t, "stray tags", plan.StrayTags, "stray:cloud")
	wantIDs(t, "pulls", pullIDs(plan))
}

// TestPlanWarnsWhenARemovedEntrysTagIsNotACloudTag pins what the plan says
// about an entry marked location = "cloud" whose model_name is a local tag
// (a mislabelled hand edit). The page does not list it, so the entry goes
// from the registry, but its tag is real weights, not a cloud stub: the plan
// must say, before anyone approves it, that the tag stays in ollama. The
// removal digest is unchanged by the warning, so it still equals modelman's.
func TestPlanWarnsWhenARemovedEntrysTagIsNotACloudTag(t *testing.T) {
	entries := []Entry{cloudEntry("keep:cloud"), cloudEntry("qwen3:8b"), cloudEntry("gone:cloud")}
	plan := PlanCatalog(entries, catalogOf(cm("keep")), []string{"keep:cloud", "qwen3:8b", "gone:cloud"}, nil)
	wantIDs(t, "removals", plan.Removals, "ollama/qwen3:8b", "ollama/gone:cloud")
	want := []string{`ollama/qwen3:8b: its tag "qwen3:8b" is not a cloud tag; the entry is removed from the registry and the tag is left in ollama`}
	if !reflect.DeepEqual(plan.Warnings, want) {
		t.Errorf("warnings = %q\nwant       %q", plan.Warnings, want)
	}
	if !strings.Contains(plan.Format(), "warning: "+want[0]) {
		t.Errorf("the printed plan lacks the warning:\n%s", plan.Format())
	}
	silent := &CatalogPlan{Removals: plan.Removals}
	if plan.RemovalDigest() != silent.RemovalDigest() {
		t.Error("the warning changed the removal digest")
	}
}

// TestPlanMassRemovalGuard pins the second half of the safety net: a plan
// that would remove more than half the ollama cloud entries is flagged, and
// exactly half is not. Such a plan is far more likely a page that parsed
// wrong than a catalog that shrank, and the command refuses it without
// --force.
func TestPlanMassRemovalGuard(t *testing.T) {
	entries := []Entry{cloudEntry("a:cloud"), cloudEntry("b:cloud"), cloudEntry("c:cloud"), cloudEntry("d:cloud")}
	if PlanCatalog(entries, catalogOf(cm("a"), cm("b")), nil, nil).MassRemoval() {
		t.Error("removing 2 of 4 entries was flagged; the guard is for more than half")
	}
	if !PlanCatalog(entries, catalogOf(cm("a")), nil, nil).MassRemoval() {
		t.Error("removing 3 of 4 entries was not flagged")
	}
}

// TestPlanIDCollisionWarns pins that an id the page would add but another
// row already holds is left alone with a warning that says who holds it. The
// alternative, a duplicate id, is a registry wt refuses to load.
func TestPlanIDCollisionWarns(t *testing.T) {
	squatter := Entry{ID: "ollama/x:cloud", Family: "f", ProviderID: "other", ModelName: "zzz"}
	plan := PlanCatalog([]Entry{squatter}, catalogOf(cm("x")), nil, nil)
	wantIDs(t, "additions", additionIDs(plan))
	if want := []string{"ollama/x:cloud already exists on another provider; not adding"}; !reflect.DeepEqual(plan.Warnings, want) {
		t.Errorf("warnings = %q, want %q", plan.Warnings, want)
	}

	renamed := Entry{ID: "ollama/foo:cloud", Family: "f", ProviderID: "ollama", ModelName: "foo:cloud-old"}
	plan = PlanCatalog([]Entry{renamed}, catalogOf(cm("foo")), nil, nil)
	wantIDs(t, "additions", additionIDs(plan))
	if want := []string{`ollama/foo:cloud already exists with model_name "foo:cloud-old"; not adding`}; !reflect.DeepEqual(plan.Warnings, want) {
		t.Errorf("warnings = %q, want %q", plan.Warnings, want)
	}
}

// TestPlanNeverAddsTheSameIDTwice pins the guard for two page rows that
// resolve to one tag: the second is a warning, not a second row with the
// same id.
func TestPlanNeverAddsTheSameIDTwice(t *testing.T) {
	plan := PlanCatalog(nil, catalogOf(cm("x"), cm("y")), nil, map[string]string{"x": "same:cloud", "y": "same:cloud"})
	wantIDs(t, "additions", additionIDs(plan), "ollama/same:cloud")
	if want := []string{"ollama/same:cloud already exists as an earlier addition from this page; not adding"}; !reflect.DeepEqual(plan.Warnings, want) {
		t.Errorf("warnings = %q, want %q", plan.Warnings, want)
	}
}

// TestPlanUnrecognizedCellKeepsTheExistingPrice pins the planner's half of
// the unknown-cell rule: one column changing format (below the whole-page
// threshold) must not overwrite a known price with nothing. Only a real "-"
// cell clears a price.
func TestPlanUnrecognizedCellKeepsTheExistingPrice(t *testing.T) {
	catalog, err := ParsePricing(page(pageHead,
		pageRow("zz", "$1.00", "$0.10 / 1M", "$2.00"),
		pageRow("yy", "$1.00", "-", "$2.00"),
		pageRow("ww", "$1.00", "$0.10", "$2.00"),
		pageRow("ww (Off-Peak)", "$0.50", "$0.05*", "$1.00"),
	))
	if err != nil {
		t.Fatal(err)
	}
	old := Cost{Input: f(9), Cache: f(0.5), Output: f(9)}
	withOld := Cost{Input: f(1), TimePrices: []*tomlw.Table{offpeakRow(f(0.4), f(0.04), f(0.8))}}
	entries := []Entry{cloudEntry("zz:cloud", withCost(old)), cloudEntry("yy:cloud", withCost(old)), cloudEntry("ww:cloud", withCost(withOld))}
	after := map[string]Cost{}
	for _, u := range PlanCatalog(entries, catalog, nil, nil).Updates {
		after[u.ModelID] = u.After
	}
	if zz := after["ollama/zz:cloud"]; !samePrice(zz.Cache, f(0.5)) || !samePrice(zz.Input, f(1)) {
		t.Errorf("zz = %s/%s, want the page's input and the kept cache price 0.5", num(zz.Input), num(zz.Cache))
	}
	if yy := after["ollama/yy:cloud"]; yy.Cache != nil {
		t.Errorf("yy cache = %s, want it cleared by the \"-\" cell", num(yy.Cache))
	}
	row := after["ollama/ww:cloud"].TimePrices[0]
	if !samePrice(number(row, "input_price_per_million"), f(0.5)) || !samePrice(number(row, "cache_price_per_million"), f(0.04)) {
		t.Errorf("ww off-peak row keys %v: want the page's input 0.5 and the kept cache 0.04", row.Keys())
	}
}

// TestPlanUsesTheResolvedTag pins that additions and pulls use the tag the
// library lookup found, not the :cloud guess.
func TestPlanUsesTheResolvedTag(t *testing.T) {
	plan := PlanCatalog(nil, catalogOf(cm("mistral-large-3")), nil, map[string]string{"mistral-large-3": "mistral-large-3:675b-cloud"})
	if len(plan.Additions) != 1 || plan.Additions[0].ID != "ollama/mistral-large-3:675b-cloud" || plan.Additions[0].ModelName != "mistral-large-3:675b-cloud" {
		t.Errorf("additions = %+v", plan.Additions)
	}
	if want := []Pull{{ID: "ollama/mistral-large-3:675b-cloud", Tag: "mistral-large-3:675b-cloud"}}; !reflect.DeepEqual(plan.Pulls, want) {
		t.Errorf("pulls = %v, want %v", plan.Pulls, want)
	}
}

// TestPlanRetagsAnEntryUnderAGuessedTag pins the re-tag: an entry an earlier
// sync filed under a tag ollama does not publish is removed and re-added
// under the real one, as a copy (CloneOf) that keeps its family and its
// subscription and takes the page's prices. It is one model changing id, so
// it must not count toward the mass-removal guard.
func TestPlanRetagsAnEntryUnderAGuessedTag(t *testing.T) {
	old := cloudEntry("mistral-large-3:cloud", withFamily("mistral"), withName("mistral-large-3"),
		withCost(Cost{Input: f(9), SubscriptionPrice: f(7), SubscriptionPeriod: s("month")}))
	plan := PlanCatalog([]Entry{old}, catalogOf(cm("mistral-large-3")), nil, map[string]string{"mistral-large-3": "mistral-large-3:675b-cloud"})
	wantIDs(t, "removals", plan.Removals, "ollama/mistral-large-3:cloud")
	wantIDs(t, "updates", updateIDs(plan))
	if len(plan.Additions) != 1 {
		t.Fatalf("additions = %+v, want one", plan.Additions)
	}
	add := plan.Additions[0]
	if add.ID != "ollama/mistral-large-3:675b-cloud" || add.CloneOf != "ollama/mistral-large-3:cloud" || add.Family != "mistral" {
		t.Errorf("addition = %+v", add)
	}
	if !samePrice(add.Cost.Input, f(1)) || !samePrice(add.Cost.SubscriptionPrice, f(7)) {
		t.Errorf("addition cost = %+v, want the page's price and the old entry's subscription", add.Cost)
	}
	if want := map[string]string{"ollama/mistral-large-3:cloud": "ollama/mistral-large-3:675b-cloud"}; !reflect.DeepEqual(plan.Replaced, want) {
		t.Errorf("replaced = %v, want %v", plan.Replaced, want)
	}
	if plan.MassRemoval() {
		t.Error("a re-tag of the only entry was flagged as a mass removal")
	}
}

// TestPlanRetagOfAnEntryWithNoCostTakesTheSharedSubscription pins the one
// case where a re-tagged entry has nothing to carry over: it gets what a new
// entry would.
func TestPlanRetagOfAnEntryWithNoCostTakesTheSharedSubscription(t *testing.T) {
	entries := []Entry{
		cloudEntry("x:cloud", withName("x")),
		cloudEntry("y:cloud", withName("y"), withCost(Cost{SubscriptionPrice: f(100), SubscriptionPeriod: s("month")})),
	}
	plan := PlanCatalog(entries, catalogOf(cm("x"), cm("y")), nil, map[string]string{"x": "x:675b-cloud", "y": "y:cloud"})
	wantIDs(t, "removals", plan.Removals, "ollama/x:cloud")
	if len(plan.Additions) != 1 || plan.Additions[0].CloneOf != "ollama/x:cloud" || plan.Additions[0].Cost.SubscriptionPrice != nil {
		t.Errorf("additions = %+v, want x re-tagged with no subscription (the entries disagree: one has none)", plan.Additions)
	}
}

// TestPlanUnresolvedModelKeepsItsEntryButSkipsPullAndAdd pins what "tag
// unknown" means: the model is still on the page, so its entry keeps getting
// prices and nothing with its name is removed, but nothing is added or pulled
// under a tag nobody verified.
func TestPlanUnresolvedModelKeepsItsEntryButSkipsPullAndAdd(t *testing.T) {
	entries := []Entry{cloudEntry("x:cloud", withName("x"))}
	plan := PlanCatalog(entries, catalogOf(cm("x", 5), cm("y")), nil, map[string]string{"x": "", "y": ""})
	wantIDs(t, "removals", plan.Removals)
	wantIDs(t, "additions", additionIDs(plan))
	wantIDs(t, "pulls", pullIDs(plan))
	wantIDs(t, "updates", updateIDs(plan), "ollama/x:cloud")
}

// TestPlanPrefersTheEntryAlreadyUnderTheResolvedTag pins that a guessed-tag
// entry carrying the catalog name does not shadow the entry already under the
// real tag: only the guessed one goes, and the real one is updated.
func TestPlanPrefersTheEntryAlreadyUnderTheResolvedTag(t *testing.T) {
	entries := []Entry{cloudEntry("ml3:cloud", withName("ml3")), cloudEntry("ml3:675b-cloud")}
	plan := PlanCatalog(entries, catalogOf(cm("ml3")), []string{"ml3:675b-cloud"}, map[string]string{"ml3": "ml3:675b-cloud"})
	wantIDs(t, "removals", plan.Removals, "ollama/ml3:cloud")
	wantIDs(t, "additions", additionIDs(plan))
	wantIDs(t, "updates", updateIDs(plan), "ollama/ml3:675b-cloud")
	wantIDs(t, "stray tags", plan.StrayTags)
}

// TestPlanUnresolvedModelProtectsItsPulledStubAndSizedEntry pins the
// protection around a failed lookup: neither a pulled stub with the model's
// name nor an entry under a sized tag may be removed, because either may be
// the model whose tag could not be read. A tag with another name still goes.
func TestPlanUnresolvedModelProtectsItsPulledStubAndSizedEntry(t *testing.T) {
	entries := []Entry{cloudEntry("foo:1t-cloud")}
	pulled := []string{"glm-5.3:cloud", "foo:1t-cloud", "other:cloud"}
	plan := PlanCatalog(entries, catalogOf(cm("foo"), cm("glm-5.3")), pulled, map[string]string{"foo": "", "glm-5.3": ""})
	wantIDs(t, "removals", plan.Removals)
	wantIDs(t, "stray tags", plan.StrayTags, "other:cloud")
	wantIDs(t, "updates", updateIDs(plan), "ollama/foo:1t-cloud")
}

// TestRemovalDigestMatchesModelman pins the digest to values computed with
// modelman's SyncPlan.removal_digest for the same removals and stray tags.
// Until modelman is deleted a digest printed by one tool's dry run may be
// passed to the other's --approve-removals, so the two must agree to the
// byte; the order the plan lists its removals in must not matter.
func TestRemovalDigestMatchesModelman(t *testing.T) {
	cases := []struct {
		removals, strays []string
		want             string
	}{
		{nil, nil, ""},
		{[]string{"ollama/b:8b-cloud", "ollama/a:cloud"}, []string{"z:cloud"}, "a4335c3c2fb0"},
		{[]string{"ollama/a:cloud", "ollama/b:8b-cloud"}, []string{"z:cloud"}, "a4335c3c2fb0"},
		{[]string{"ollama/gone:cloud"}, nil, "0bcc560b956e"},
		{nil, []string{"stray:cloud"}, "cedb1eab545d"},
		{[]string{"ollama/é:cloud", "ollama/Z:cloud", "ollama/a:cloud"}, nil, "ea46b1389797"},
	}
	for _, tc := range cases {
		plan := &CatalogPlan{Removals: tc.removals, StrayTags: tc.strays}
		if got := plan.RemovalDigest(); got != tc.want {
			t.Errorf("RemovalDigest(%v, %v) = %q, want %q", tc.removals, tc.strays, got, tc.want)
		}
	}
}

// TestFormatPriceMatchesPythonsG pins the price format to Python's `{v:g}`
// on values computed there. The plan's text is what the user approves and
// what the command compares before applying, and it is the same text
// modelman prints for the same plan.
func TestFormatPriceMatchesPythonsG(t *testing.T) {
	cases := map[float64]string{
		1.32: "1.32", 0.044: "0.044", 15: "15", 0: "0", 0.015: "0.015", 100000: "100000",
		1000000: "1e+06", 123456789: "1.23457e+08", 1234567: "1.23457e+06", 999999.5: "1e+06",
		0.0001: "0.0001", 0.00001: "1e-05", 1e16: "1e+16",
		2.4999999999999996: "2.5", 0.09999999999999999: "0.1",
	}
	for v, want := range cases {
		if got := formatPrice(&v); got != want {
			t.Errorf("formatPrice(%v) = %q, want %q", v, got, want)
		}
	}
	if got := formatPrice(nil); got != "-" {
		t.Errorf("formatPrice(nil) = %q, want -", got)
	}
}

// TestCatalogPlanFormat pins the whole printed plan for a run with one of
// everything. A user approves this text, the skill reads the digest line out
// of it, and it is the same text modelman prints for the same plan (compared
// in scratch when this was written).
func TestCatalogPlanFormat(t *testing.T) {
	entries := []Entry{
		cloudEntry("a:cloud", withCost(Cost{Input: f(9), TimePrices: []*tomlw.Table{offpeakRow(f(0.4), nil, nil)}})),
		cloudEntry("gone:cloud"),
		cloudEntry("m:cloud", withName("m")),
		cloudEntry("same:cloud", withName("same"), withCost(Cost{Input: f(1), Cache: f(0.1), Output: f(2)})),
	}
	catalog := catalogOf(withOffpeak(cm("a", 1.32, 0.044, 3.96), f(0.66), f(0.022), f(1.98)), cm("b"), cm("m"), cm("same"), cm("lost"))
	catalog.Warnings = []string{"from the page"}
	resolved := map[string]string{"a": "a:cloud", "b": "b:cloud", "m": "m:675b-cloud", "same": "same:cloud", "lost": ""}
	plan := PlanCatalog(entries, catalog, []string{"gone:cloud", "stray:cloud", "same:cloud"}, resolved)
	want := strings.Join([]string{
		"ollama.com/pricing: 5 models (prices are input/cached/output per million tokens)",
		"Price updates (1):",
		"  ollama/a:cloud: 9/-/- (off-peak 0.4/-/-) -> 1.32/0.044/3.96 (off-peak 0.66/0.022/1.98)",
		"Registry additions (2):",
		"  ollama/b:cloud [family b]: 1/0.1/2",
		"  ollama/m:675b-cloud [family fam]: 1/0.1/2",
		"Unchanged prices: 1",
		"ollama pull (3):",
		"  ollama/a:cloud",
		"  ollama/b:cloud",
		"  ollama/m:675b-cloud",
		"Registry removals — off ollama.com/pricing or under a tag ollama doesn't publish; `ollama rm` if pulled (2):",
		"  ollama/gone:cloud",
		"  ollama/m:cloud (re-tagged as ollama/m:675b-cloud)",
		"ollama rm — pulled, unregistered, off the page (1):",
		"  stray:cloud",
		"Removal digest: " + plan.RemovalDigest() + " (apply non-interactively with `--yes --approve-removals " + plan.RemovalDigest() + "`)",
		"warning: from the page",
	}, "\n")
	if got := plan.Format(); got != want {
		t.Errorf("Format() =\n%s\n\nwant\n%s", got, want)
	}
	if len(plan.RemovalDigest()) != 12 {
		t.Errorf("digest = %q, want 12 hex digits", plan.RemovalDigest())
	}
}

// TestPlanCatalogIsDeterministic pins what the apply step relies on: the
// same inputs give the same printed plan every time. The command re-plans
// under the registry lock and refuses when the text differs from what was
// printed, so a plan that varied with map order would refuse at random.
func TestPlanCatalogIsDeterministic(t *testing.T) {
	entries := []Entry{cloudEntry("a:cloud"), cloudEntry("b:cloud"), cloudEntry("c:cloud", withName("c")), cloudEntry("d:cloud")}
	catalog := catalogOf(cm("c"), cm("e"), cm("f"), cm("g"))
	resolved := map[string]string{"c": "c:9b-cloud", "e": "e:cloud", "f": "f:cloud", "g": ""}
	pulled := []string{"x:cloud", "y:cloud", "a:cloud"}
	first := PlanCatalog(entries, catalog, pulled, resolved).Format()
	for range 50 {
		if got := PlanCatalog(entries, catalog, pulled, resolved).Format(); got != first {
			t.Fatalf("the plan changed between two runs on the same inputs:\n%s\n\nvs\n%s", first, got)
		}
	}
}
```

The digests in `TestRemovalDigestMatchesModelman` and the strings in `TestFormatPriceMatchesPythonsG` were computed with modelman (`SyncPlan(...).removal_digest()`, `format(v, 'g')`). They are the contract with the other tool, not values to regenerate from the Go code.

- [ ] **Step 2: Run the tests to verify they fail**

Run (from `wt/`): `go test -count=1 ./internal/cloudsync`
Expected:

```text
# github.com/ohanaverse/local-ai-setup/wt/internal/cloudsync [github.com/ohanaverse/local-ai-setup/wt/internal/cloudsync.test]
internal/cloudsync/catalog_test.go:53:19: undefined: CatalogPlan
internal/cloudsync/catalog_test.go:61:21: undefined: CatalogPlan
internal/cloudsync/catalog_test.go:69:17: undefined: CatalogPlan
internal/cloudsync/catalog_test.go:101:10: undefined: PlanCatalog
```

- [ ] **Step 3: Write the planner**

Create `wt/internal/cloudsync/catalog.go`:

```go
package cloudsync

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/tomlw"
)

// OffpeakLabel is the label of the time_prices row the catalog flow owns.
// Rows with any other label are the user's and are never touched.
const OffpeakLabel = "off-peak"

// CostUpdate is a price change to one existing entry.
type CostUpdate struct {
	ModelID, CatalogName string
	// Before is nil when the entry had no cost table.
	Before *Cost
	After  Cost
}

// Addition is an entry the plan adds.
type Addition struct {
	ID, Family, ModelName, CatalogName string
	Cost                               Cost
	// CloneOf is the id of the entry this one replaces under the tag ollama
	// publishes, or "". The new row is a copy of that one, so its hand-set
	// keys, tags and model_info survive the id change.
	CloneOf string
}

// Pull is one `ollama pull`: the registry id the plan lists and the tag the
// CLI is given.
type Pull struct{ ID, Tag string }

// CatalogPlan is what a sync changes so that `ollama list`'s cloud stubs and
// the registry's ollama cloud entries both mirror the page.
type CatalogPlan struct {
	CatalogSize int
	// CloudEntries counts the registry's ollama cloud entries before the sync.
	CloudEntries int
	Updates      []CostUpdate
	Unchanged    []string
	Additions    []Addition
	// Pulls are the entries, existing or added, whose tag `ollama list` lacks.
	Pulls []Pull
	// Removals are the ids of ollama cloud entries the page no longer lists,
	// or lists under another tag. Each is removed from the registry whether or
	// not it is pulled; its tag is removed from ollama only when it is a cloud
	// tag (Apply), and a removal whose tag is not one carries a warning.
	Removals []string
	// StrayTags are pulled cloud tags neither on the page nor in the registry.
	StrayTags []string
	// Replaced maps a removed id to the id it is re-added under.
	Replaced map[string]string
	Warnings []string
}

// MassRemoval reports whether more than half the registry's ollama cloud
// entries would go, which is more likely a page that parsed wrong than a
// catalog that shrank. A re-tagged entry comes straight back, so it does not
// count.
func (p *CatalogPlan) MassRemoval() bool {
	gone := 0
	for _, id := range p.Removals {
		if _, retagged := p.Replaced[id]; !retagged {
			gone++
		}
	}
	return gone*2 > p.CloudEntries
}

// RemovalDigest fingerprints everything the plan deletes: registry removals
// and stray `ollama rm`s. It is "" when the plan deletes nothing. `--yes`
// applies deletions only under the digest a reviewed dry run printed, so a
// page that changed since cannot delete a model nobody saw.
//
// The value is byte-identical to modelman's SyncPlan.removal_digest for the
// same plan: the first 12 hex digits of the SHA-256 of the sorted removals
// followed by the sorted "rm <tag>" lines, joined with newlines.
func (p *CatalogPlan) RemovalDigest() string {
	items := slices.Clone(p.Removals)
	sort.Strings(items)
	strays := make([]string, len(p.StrayTags))
	for i, t := range p.StrayTags {
		strays[i] = "rm " + t
	}
	sort.Strings(strays)
	items = append(items, strays...)
	if len(items) == 0 {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.Join(items, "\n")))
	return hex.EncodeToString(sum[:])[:12]
}

// HasWork reports whether applying the plan would change anything, in the
// registry or in ollama.
func (p *CatalogPlan) HasWork() bool {
	return len(p.Updates)+len(p.Additions)+len(p.Pulls)+len(p.Removals)+len(p.StrayTags) > 0
}

func isOllamaCloud(e Entry) bool {
	return e.ProviderID == ollamaProvider && (e.Location == "cloud" || IsCloudTag(e.ModelName))
}

func stem(tag string) string {
	name, _, _ := strings.Cut(tag, ":")
	return name
}

// findEntry returns the entry for page model name whose cloud tag is tag
// ("": unknown), or nil. An entry already under the tag wins over one
// carrying the catalog name, so an entry an earlier sync added under a
// guessed tag cannot shadow it. With the tag unknown it falls back to the
// guessed tag, then to the only ollama cloud entry with the same name stem.
func findEntry(entries []Entry, name, tag string) *Entry {
	first := func(match func(Entry) bool) *Entry {
		for i := range entries {
			if entries[i].ProviderID == ollamaProvider && match(entries[i]) {
				return &entries[i]
			}
		}
		return nil
	}
	if tag != "" {
		if hit := first(func(e Entry) bool { return e.ModelName == tag }); hit != nil {
			return hit
		}
	}
	if hit := first(func(e Entry) bool { return e.CatalogName == name }); hit != nil {
		return hit
	}
	if tag != "" {
		return nil
	}
	guess := CloudTag(name)
	if hit := first(func(e Entry) bool { return e.ModelName == guess }); hit != nil {
		return hit
	}
	var kin []*Entry
	for i := range entries {
		if isOllamaCloud(entries[i]) && stem(entries[i].ModelName) == stem(name) {
			kin = append(kin, &entries[i])
		}
	}
	if len(kin) == 1 {
		return kin[0]
	}
	return nil
}

// merged is page with each unrecognized price replaced by the old one.
func merged(page PriceTriple, oldInput, oldCache, oldOutput *float64) (input, cache, output *float64) {
	input, cache, output = page.Input, page.Cache, page.Output
	if page.Unknown.Input {
		input = oldInput
	}
	if page.Unknown.Cache {
		cache = oldCache
	}
	if page.Unknown.Output {
		output = oldOutput
	}
	return input, cache, output
}

// offpeakRow is ollama's published off-peak window as a time_prices row:
// outside 12:00 to 18:00 UTC on weekdays, and all day at weekends. The keys
// are in the order modelman writes them. The window is not parsed from the
// page; if ollama changes it, change it here.
func offpeakRow(input, cache, output *float64) *tomlw.Table {
	window := func(start, end string, days ...string) *tomlw.Table {
		w := tomlw.NewTable()
		list := make([]any, len(days))
		for i, d := range days {
			list[i] = d
		}
		w.Set("days", list)
		w.Set("start", start)
		w.Set("end", end)
		return w
	}
	row := tomlw.NewTable()
	row.Set("label", OffpeakLabel)
	row.Set("timezone", "UTC")
	for _, kv := range []struct {
		key string
		v   *float64
	}{{"input_price_per_million", input}, {"cache_price_per_million", cache}, {"output_price_per_million", output}} {
		if kv.v != nil {
			row.Set(kv.key, *kv.v)
		}
	}
	row.Set("windows", []any{
		window("00:00", "12:00", "mon", "tue", "wed", "thu", "fri"),
		window("18:00", "24:00", "mon", "tue", "wed", "thu", "fri"),
		window("00:00", "24:00", "sat", "sun"),
	})
	return row
}

func isOffpeak(row *tomlw.Table) bool { return str(row, "label") == OffpeakLabel }

// withCatalogPrices is existing with the page's prices for cm. The off-peak
// row is replaced where it stands: time_prices resolve first match wins, so
// moving it would change which row wins an overlapping window. Every other
// row, the subscription and any key this package does not read are kept.
func withCatalogPrices(existing *Cost, cm CatalogModel) Cost {
	var base Cost
	if existing != nil {
		base = *existing
	}
	at, old := -1, (*tomlw.Table)(nil)
	var rows []*tomlw.Table
	for i, row := range base.TimePrices {
		if !isOffpeak(row) {
			rows = append(rows, row)
		} else if old == nil {
			at, old = i, row
		}
	}
	if cm.Offpeak != nil {
		var oldIn, oldCache, oldOut *float64
		if old != nil {
			oldIn, oldCache, oldOut = number(old, "input_price_per_million"), number(old, "cache_price_per_million"), number(old, "output_price_per_million")
		}
		if at < 0 || at > len(rows) {
			at = len(rows)
		}
		rows = slices.Insert(rows, at, offpeakRow(merged(*cm.Offpeak, oldIn, oldCache, oldOut)))
	}
	after := base
	after.Input, after.Cache, after.Output = merged(cm.Prices, base.Input, base.Cache, base.Output)
	after.TimePrices = rows
	return after
}

// sameCost reports whether a sync would leave the cost as it is. A missing
// cost table and one with no prices are the same thing here.
func sameCost(before *Cost, after Cost) bool {
	var b Cost
	if before != nil {
		b = *before
	}
	if !samePrice(b.Input, after.Input) || !samePrice(b.Cache, after.Cache) || !samePrice(b.Output, after.Output) ||
		len(b.TimePrices) != len(after.TimePrices) {
		return false
	}
	for i := range b.TimePrices {
		if !tomlw.Same(b.TimePrices[i], after.TimePrices[i]) {
			return false
		}
	}
	return true
}

func familyFor(entries []Entry, name string) string {
	for _, e := range entries {
		if e.ProviderID == ollamaProvider && stem(e.ModelName) == stem(name) {
			return e.Family
		}
	}
	return stem(name)
}

// sharedSubscription is the subscription every ollama cloud entry agrees on,
// which a new entry inherits. An entry with no cost table counts as "none".
// When they disagree a new entry gets none, and disagree is true.
func sharedSubscription(entries []Entry) (price *float64, period *string, disagree bool) {
	seen := false
	for _, e := range entries {
		if !isOllamaCloud(e) {
			continue
		}
		var p *float64
		var per *string
		if e.Cost != nil {
			p, per = e.Cost.SubscriptionPrice, e.Cost.SubscriptionPeriod
		}
		if !seen {
			price, period, seen = p, per, true
			continue
		}
		samePeriod := (per == nil) == (period == nil) && (per == nil || *per == *period)
		if !samePrice(p, price) || !samePeriod {
			return nil, nil, true
		}
	}
	return price, period, false
}

// PlanCatalog works out what a sync would change. entries are the registry's
// model rows, pulled is `ollama list`'s NAME column, and resolved is
// ResolveCloudTags' map.
//
// A nil resolved means "trust CloudTag": no tag was verified, so no entry is
// ever re-tagged. In a non-nil map, "" (or a missing name) means the model's
// cloud tag is unknown: an existing entry still gets its prices, nothing is
// added or pulled for it, and nothing that may be it (an entry or a pulled
// cloud tag with its name) is removed.
//
// It is pure and deterministic: the same arguments give the same plan, which
// is what lets the command re-plan under the registry lock and compare.
func PlanCatalog(entries []Entry, catalog Catalog, pulled []string, resolved map[string]string) *CatalogPlan {
	plan := &CatalogPlan{
		CatalogSize: len(catalog.Models),
		Replaced:    map[string]string{},
		Warnings:    slices.Clone(catalog.Warnings),
	}
	ids := map[string]bool{}
	for _, e := range entries {
		ids[e.ID] = true
		if isOllamaCloud(e) {
			plan.CloudEntries++
		}
	}
	isPulled := func(tag string) bool { return slices.Contains(pulled, tag) }
	matched := map[string]bool{}
	listed := map[string]bool{}
	subPrice, subPeriod, disagree := sharedSubscription(entries)
	if disagree {
		plan.Warnings = append(plan.Warnings, "ollama cloud entries disagree on subscription pricing; new entries get none")
	}

	for _, cm := range catalog.Models {
		tag := CloudTag(cm.Name)
		if resolved != nil {
			tag = resolved[cm.Name]
		}
		entry := findEntry(entries, cm.Name, tag)
		var retagged *Entry
		if resolved != nil && tag != "" && entry != nil && entry.ModelName != tag {
			// Its tag is not what ollama publishes (an earlier sync guessed):
			// left unmatched, so it is removed and re-added under the real
			// tag below.
			retagged, entry = entry, nil
		}
		if tag == "" {
			// Still on the page, just unresolved: protect whatever may be it.
			for _, e := range entries {
				if isOllamaCloud(e) && stem(e.ModelName) == stem(cm.Name) {
					matched[e.ID] = true
				}
			}
			listed[CloudTag(cm.Name)] = true
			for _, t := range pulled {
				if IsCloudTag(t) && stem(t) == stem(cm.Name) {
					listed[t] = true
				}
			}
		}
		if entry != nil {
			matched[entry.ID] = true
			// Both: the entry's tag and the canonical pulled tag are listed.
			listed[entry.ModelName] = true
			if tag != "" {
				listed[tag] = true
			}
			after := withCatalogPrices(entry.Cost, cm)
			if sameCost(entry.Cost, after) && entry.CatalogName == cm.Name {
				plan.Unchanged = append(plan.Unchanged, entry.ID)
			} else {
				plan.Updates = append(plan.Updates, CostUpdate{ModelID: entry.ID, CatalogName: cm.Name, Before: entry.Cost, After: after})
			}
			if tag != "" && !isPulled(entry.ModelName) {
				plan.Pulls = append(plan.Pulls, Pull{ID: entry.ID, Tag: entry.ModelName})
			}
			continue
		}
		if tag == "" {
			continue // unknown cloud tag; ResolveCloudTags warned
		}
		listed[tag] = true
		newID := ollamaProvider + "/" + tag
		if ids[newID] {
			where := "as an earlier addition from this page"
			for _, e := range entries {
				if e.ID != newID {
					continue
				}
				if e.ProviderID == ollamaProvider {
					where = fmt.Sprintf("with model_name %q", e.ModelName)
				} else {
					where = "on another provider"
				}
				break
			}
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("%s already exists %s; not adding", newID, where))
			continue
		}
		ids[newID] = true
		add := Addition{ID: newID, ModelName: tag, CatalogName: cm.Name}
		// A re-tag is the same model under the tag ollama publishes, so the
		// new entry is built off the one it replaces. (The LiteLLM route
		// name is the id, so that one cannot come across.)
		base := &Cost{SubscriptionPrice: subPrice, SubscriptionPeriod: subPeriod}
		if retagged != nil {
			plan.Replaced[retagged.ID] = newID
			add.CloneOf, add.Family = retagged.ID, retagged.Family
			if retagged.Cost != nil {
				base = retagged.Cost
			}
		} else {
			add.Family = familyFor(entries, cm.Name)
		}
		add.Cost = withCatalogPrices(base, cm)
		plan.Additions = append(plan.Additions, add)
		if !isPulled(tag) {
			plan.Pulls = append(plan.Pulls, Pull{ID: newID, Tag: tag})
		}
	}

	registered := map[string]bool{}
	for _, e := range entries {
		if e.ProviderID == ollamaProvider {
			registered[e.ModelName] = true
		}
		if isOllamaCloud(e) && !matched[e.ID] {
			plan.Removals = append(plan.Removals, e.ID)
			if !IsCloudTag(e.ModelName) {
				// An entry marked location = "cloud" over a tag that is not a
				// cloud stub: what `ollama rm` would delete is real weights.
				// Apply leaves the tag alone; the plan says so before anyone
				// approves it.
				plan.Warnings = append(plan.Warnings, fmt.Sprintf("%s: its tag %q is not a cloud tag; the entry is removed from the registry and the tag is left in ollama", e.ID, e.ModelName))
			}
		}
	}
	for _, t := range pulled {
		if IsCloudTag(t) && !listed[t] && !registered[t] {
			plan.StrayTags = append(plan.StrayTags, t)
		}
	}
	return plan
}

// formatPrice prints a price as Python's `{v:g}` does, so a plan reads the
// same from either tool: six significant digits, no trailing zeros, exponent
// form from 1e+06 up and below 0.0001.
func formatPrice(v *float64) string {
	if v == nil {
		return "-"
	}
	return strconv.FormatFloat(*v, 'g', 6, 64)
}

func formatCost(c *Cost) string {
	if c == nil {
		return "no cost"
	}
	text := formatPrice(c.Input) + "/" + formatPrice(c.Cache) + "/" + formatPrice(c.Output)
	for _, row := range c.TimePrices {
		if isOffpeak(row) {
			text += fmt.Sprintf(" (off-peak %s/%s/%s)", formatPrice(number(row, "input_price_per_million")),
				formatPrice(number(row, "cache_price_per_million")), formatPrice(number(row, "output_price_per_million")))
			break
		}
	}
	return text
}

// Format prints the plan, one section per kind of change, in the words
// modelman's format_plan uses. The command prints it for review, and compares
// it with the re-plan made under the registry lock: two plans that print the
// same are the same plan.
func (p *CatalogPlan) Format() string {
	lines := []string{fmt.Sprintf("ollama.com/pricing: %d models (prices are input/cached/output per million tokens)", p.CatalogSize)}
	lines = append(lines, fmt.Sprintf("Price updates (%d):", len(p.Updates)))
	for _, u := range p.Updates {
		lines = append(lines, fmt.Sprintf("  %s: %s -> %s", u.ModelID, formatCost(u.Before), formatCost(&u.After)))
	}
	lines = append(lines, fmt.Sprintf("Registry additions (%d):", len(p.Additions)))
	for _, a := range p.Additions {
		lines = append(lines, fmt.Sprintf("  %s [family %s]: %s", a.ID, a.Family, formatCost(&a.Cost)))
	}
	lines = append(lines, fmt.Sprintf("Unchanged prices: %d", len(p.Unchanged)))
	lines = append(lines, fmt.Sprintf("ollama pull (%d):", len(p.Pulls)))
	for _, pull := range p.Pulls {
		lines = append(lines, "  "+pull.ID)
	}
	lines = append(lines, fmt.Sprintf("Registry removals — off ollama.com/pricing or under a tag ollama doesn't publish; `ollama rm` if pulled (%d):", len(p.Removals)))
	for _, id := range p.Removals {
		line := "  " + id
		if to, ok := p.Replaced[id]; ok {
			line += " (re-tagged as " + to + ")"
		}
		lines = append(lines, line)
	}
	lines = append(lines, fmt.Sprintf("ollama rm — pulled, unregistered, off the page (%d):", len(p.StrayTags)))
	for _, tag := range p.StrayTags {
		lines = append(lines, "  "+tag)
	}
	if digest := p.RemovalDigest(); digest != "" {
		lines = append(lines, fmt.Sprintf("Removal digest: %s (apply non-interactively with `--yes --approve-removals %s`)", digest, digest))
	}
	for _, w := range p.Warnings {
		lines = append(lines, "warning: "+w)
	}
	return strings.Join(lines, "\n")
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run (from `wt/`): `go test -count=1 ./internal/cloudsync`
Expected:

```text
ok  	github.com/ohanaverse/local-ai-setup/wt/internal/cloudsync	(time)
```

(`-v` lists 40 passing top-level tests.)

- [ ] **Step 5: Commit**

From the repo root:

```bash
git add wt/internal/cloudsync
git commit -m "feat(wt): cloudsync.PlanCatalog, with the mass-removal guard and modelman's removal digest"
```

### Task 4: The OpenRouter price planner

**Files:**
- Create: `wt/internal/cloudsync/openrouter_test.go`, `wt/internal/cloudsync/openrouter.go`

**Interfaces:**
- Consumes: Task 2 (`Entry`, `Cost`, `Provider`, `Entries`, `Providers`), Task 3 (`sameCost`, `formatCost`, test helpers `timeRow`, `wantIDs`), Task 1 (`wantTriple`, `samePrice`, `f`, `s`, `num`); the contract fixture `docs/contracts/catalog-predicates.sample.toml` with `catalog-predicates.expected.json`.
- Produces:
  - `const OpenRouterModelsURL = "https://openrouter.ai/api/v1/models"`
  - `type APIPrice struct { Input, Cache, Output *float64; Skipped []string }`
  - `func ParseOpenRouter(body []byte) (map[string]APIPrice, error)`
  - `func OpenRouterPriced(e Entry, providers []Provider) bool`
  - `type PriceChange struct { ModelID string; Before *Cost; After Cost; Changed bool }`
  - `type PricePlan struct { Candidates int; Matched []PriceChange; Warnings []string }`
  - `func PlanPrices(entries []Entry, providers []Provider, api map[string]APIPrice) *PricePlan`
  - `func (p *PricePlan) HasWork() bool`, `Format() string`
  - test helper `apiOf(t, body) map[string]APIPrice`, used by Task 5

- [ ] **Step 1: Write the failing tests**

Create `wt/internal/cloudsync/openrouter_test.go`:

```go
package cloudsync

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/tomlw"
)

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

func orEntry(name string, cost *Cost) Entry {
	return Entry{ID: "openrouter/" + name, Family: "x", ProviderID: "openrouter", ModelName: "vendor/" + name, Location: "cloud", Cost: cost}
}

func apiOf(t *testing.T, body string) map[string]APIPrice {
	t.Helper()
	api, err := ParseOpenRouter([]byte(body))
	if err != nil {
		t.Fatalf("ParseOpenRouter: %v", err)
	}
	return api
}

// TestParseOpenRouter pins how the API's per-token prices become prices per
// million tokens: decimal strings (what OpenRouter sends) and JSON numbers
// alike, a missing field as no price, and OpenRouter's -1 ("varies") marked
// as not a price instead of written to the registry as minus one million.
func TestParseOpenRouter(t *testing.T) {
	api := apiOf(t, `{"data": [
		{"id": "a/strings", "pricing": {"prompt": "0.0000025", "completion": "0.0000100", "input_cache_read": "0.0000010"}},
		{"id": "a/numbers", "pricing": {"prompt": 0.000001, "completion": 2e-6}},
		{"id": "a/top-level", "prompt": "0.000003"},
		{"id": "a/varies", "pricing": {"prompt": "-1", "completion": "-1"}},
		{"id": "a/junk", "pricing": {"prompt": "soon", "completion": null, "input_cache_read": true}},
		{"id": "a/no-table", "pricing": "n/a"},
		{"pricing": {"prompt": "1"}},
		"not an object"
	]}`)
	if len(api) != 6 {
		t.Fatalf("ids = %d, want 6 (the two items with no id skipped)", len(api))
	}
	wantTriple(t, "strings", PriceTriple{Input: api["a/strings"].Input, Cache: api["a/strings"].Cache, Output: api["a/strings"].Output}, f(0.0000025*1e6), f(0.0000010*1e6), f(0.0000100*1e6))
	wantTriple(t, "numbers", PriceTriple{Input: api["a/numbers"].Input, Cache: api["a/numbers"].Cache, Output: api["a/numbers"].Output}, f(0.000001*1e6), nil, f(2e-6*1e6))
	if !samePrice(api["a/top-level"].Input, f(0.000003*1e6)) {
		t.Errorf("an item with no pricing table: input = %s, want it read from the item itself", num(api["a/top-level"].Input))
	}
	if v := api["a/varies"]; v.Input != nil || v.Output != nil || !reflect.DeepEqual(v.Skipped, []string{"prompt", "completion"}) {
		t.Errorf("a/varies = %+v, want no prices and both fields skipped", v)
	}
	for _, id := range []string{"a/junk", "a/no-table"} {
		if v := api[id]; v.Input != nil || v.Output != nil || v.Cache != nil || v.Skipped != nil {
			t.Errorf("%s = %+v, want no prices", id, v)
		}
	}
}

// TestParseOpenRouterRejectsAnotherShape pins that an answer that is not the
// model list (an error page, a changed API) is an error, so the prices flow
// fails as a step instead of reporting "no OpenRouter match" for every model.
func TestParseOpenRouterRejectsAnotherShape(t *testing.T) {
	for body, want := range map[string]string{
		`<html>rate limited</html>`: "not JSON",
		`[1, 2]`:                    "not a JSON object",
		`{"error": "nope"}`:         "missing 'data' list",
		`{"data": {"a": 1}}`:        "missing 'data' list",
	} {
		if _, err := ParseOpenRouter([]byte(body)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ParseOpenRouter(%s) err = %v, want one naming %q", body, err, want)
		}
	}
}

// TestOpenRouterPricedMatchesTheContract holds this package's row-based
// predicate to docs/contracts/catalog-predicates, the fixture that already
// pins config.Config.OpenRouterPriced and modelman's _is_openrouter_priced.
// If they drifted, the prices flow would refresh one set of models while the
// stale-price notice watched another.
func TestOpenRouterPricedMatchesTheContract(t *testing.T) {
	const dir = "../../../docs/contracts/"
	data, err := os.ReadFile(dir + "catalog-predicates.sample.toml")
	if err != nil {
		t.Fatal(err)
	}
	root, err := tomlw.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	rows := func(key string) []*tomlw.Table {
		v, _ := root.Get(key)
		var out []*tomlw.Table
		for _, item := range v.([]any) {
			out = append(out, item.(*tomlw.Table))
		}
		return out
	}
	raw, err := os.ReadFile(dir + "catalog-predicates.expected.json")
	if err != nil {
		t.Fatal(err)
	}
	var want struct {
		OpenRouterPriced []string `json:"openrouter_priced"`
	}
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	providers := Providers(rows("providers"))
	var got []string
	for _, e := range Entries(rows("models")) {
		if OpenRouterPriced(e, providers) {
			got = append(got, e.ID)
		}
	}
	if !reflect.DeepEqual(got, want.OpenRouterPriced) {
		t.Errorf("openrouter_priced = %v, want %v", got, want.OpenRouterPriced)
	}
}

// TestPlanPricesMergeRules pins what a refresh may and may not overwrite.
// Input and output are what it measures and always take the API's value. The
// cache price is replaced only when the API reports one, and the subscription
// and the time_prices rows are never touched, so a refresh cannot erase
// pricing the user entered by hand.
func TestPlanPricesMergeRules(t *testing.T) {
	mine := timeRow("mine", "sat")
	manual := &Cost{Input: f(2.5), Cache: f(1), Output: f(10), SubscriptionPrice: f(20), SubscriptionPeriod: s("month"), TimePrices: []*tomlw.Table{mine}}
	entries := []Entry{
		orEntry("manual", manual),
		orEntry("cache-reported", &Cost{Cache: f(9.9)}),
		orEntry("new", nil),
	}
	api := apiOf(t, `{"data": [
		{"id": "vendor/manual", "pricing": {"prompt": "0.000003", "completion": "0.000012"}},
		{"id": "vendor/cache-reported", "pricing": {"prompt": "0.000001", "completion": "0.000002", "input_cache_read": "0.0000005"}},
		{"id": "vendor/new", "pricing": {"prompt": "0.000001", "completion": "0.000002"}}
	]}`)
	plan := PlanPrices(entries, priceProviders, api)
	if plan.Candidates != 3 || len(plan.Matched) != 3 || len(plan.Warnings) != 0 {
		t.Fatalf("plan = %+v", plan)
	}
	m := plan.Matched[0].After
	if !samePrice(m.Input, f(0.000003*1e6)) || !samePrice(m.Output, f(0.000012*1e6)) || !samePrice(m.Cache, f(1)) ||
		!samePrice(m.SubscriptionPrice, f(20)) || *m.SubscriptionPeriod != "month" || len(m.TimePrices) != 1 || m.TimePrices[0] != mine {
		t.Errorf("manual after = %+v, want new input and output, everything else kept", m)
	}
	if got := plan.Matched[1].After.Cache; !samePrice(got, f(0.0000005*1e6)) {
		t.Errorf("cache-reported cache = %s, want the API's", num(got))
	}
	if got := plan.Matched[2]; got.Before != nil || got.After.Cache != nil || !got.Changed {
		t.Errorf("new = %+v, want a changed entry with no cache price", got)
	}
}

// TestPlanPricesCandidatesAndWarnings pins who is refreshed and what is said
// about the rest: a model of a non-native cloud provider is a candidate like
// an openrouter one, and so is one whose provider says openrouter_priced =
// true; an ollama cloud model (priced by ollama.com), a native agent
// provider's model and a model of a provider that says openrouter_priced =
// false are not, and get no "no match" warning (#151, #302); a candidate
// OpenRouter does not list, lists with no price, or prices at -1 is a
// warning and is left exactly as it is.
func TestPlanPricesCandidatesAndWarnings(t *testing.T) {
	entries := []Entry{
		orEntry("listed", nil),
		{ID: "acme/model-a", ProviderID: "acme", ModelName: "vendor/listed", Location: "cloud"},
		{ID: "ollama/glm:cloud", ProviderID: "ollama", ModelName: "vendor/listed", Location: "cloud", Cost: &Cost{Input: f(1)}},
		{ID: "claude/native", ProviderID: "claude", ModelName: "vendor/listed", Location: "cloud"},
		{ID: "dangling/x", ProviderID: "nowhere", ModelName: "vendor/listed", Location: "cloud"},
		{ID: "corp/claude-3", ProviderID: "corp", ModelName: "vendor/listed", Location: "cloud"},
		{ID: "gateway/y", ProviderID: "gateway", ModelName: "vendor/listed", Location: "local"},
		orEntry("missing", &Cost{Input: f(7)}),
		orEntry("unpriced", nil),
		orEntry("varies", nil),
	}
	api := apiOf(t, `{"data": [
		{"id": "vendor/listed", "pricing": {"prompt": "0.000001", "completion": "0.000002"}},
		{"id": "vendor/unpriced", "pricing": {}},
		{"id": "vendor/varies", "pricing": {"prompt": "-1", "completion": "0.000002"}}
	]}`)
	plan := PlanPrices(entries, priceProviders, api)
	var matched []string
	for _, m := range plan.Matched {
		matched = append(matched, m.ModelID)
	}
	wantIDs(t, "matched", matched, "openrouter/listed", "acme/model-a", "gateway/y")
	if plan.Candidates != 6 {
		t.Errorf("candidates = %d, want 6", plan.Candidates)
	}
	want := []string{
		"No OpenRouter match for openrouter/missing (vendor/missing)",
		"No pricing data for openrouter/unpriced",
		"Could not use OpenRouter's pricing for openrouter/varies: its prompt price is negative or not a number",
	}
	if !reflect.DeepEqual(plan.Warnings, want) {
		t.Errorf("warnings = %q\nwant %q", plan.Warnings, want)
	}
}

// TestPricePlanFormat pins the dry run's text: each changed model as
// `id: old -> new`, unchanged ones as a count, then the warnings.
func TestPricePlanFormat(t *testing.T) {
	entries := []Entry{
		orEntry("moved", &Cost{Input: f(2.5), Output: f(10)}),
		orEntry("same", &Cost{Input: f(1), Output: f(2)}),
		orEntry("new", nil),
		orEntry("missing", nil),
	}
	api := apiOf(t, `{"data": [
		{"id": "vendor/moved", "pricing": {"prompt": "0.000003", "completion": "0.000015", "input_cache_read": "0.0000003"}},
		{"id": "vendor/same", "pricing": {"prompt": "0.000001", "completion": "0.000002"}},
		{"id": "vendor/new", "pricing": {"prompt": "0.00000068", "completion": "0.00000209"}}
	]}`)
	plan := PlanPrices(entries, priceProviders, api)
	want := strings.Join([]string{
		"openrouter.ai: 4 OpenRouter-priced models in the registry (prices are input/cached/output per million tokens)",
		"Price updates (2):",
		"  openrouter/moved: 2.5/-/10 -> 3/0.3/15",
		"  openrouter/new: no cost -> 0.68/-/2.09",
		"Unchanged prices: 1",
		"warning: No OpenRouter match for openrouter/missing (vendor/missing)",
	}, "\n")
	if got := plan.Format(); got != want {
		t.Errorf("Format() =\n%s\n\nwant\n%s", got, want)
	}
	if !plan.HasWork() || (&PricePlan{Candidates: 1}).HasWork() {
		t.Error("HasWork: want true with a matched model, false with none")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run (from `wt/`): `go test -count=1 ./internal/cloudsync`
Expected:

```text
# github.com/ohanaverse/local-ai-setup/wt/internal/cloudsync [github.com/ohanaverse/local-ai-setup/wt/internal/cloudsync.test]
internal/cloudsync/openrouter_test.go:33:50: undefined: APIPrice
internal/cloudsync/openrouter_test.go:35:14: undefined: ParseOpenRouter
internal/cloudsync/openrouter_test.go:85:16: undefined: ParseOpenRouter
internal/cloudsync/openrouter_test.go:127:6: undefined: OpenRouterPriced
```

- [ ] **Step 3: Write the price planner**

Create `wt/internal/cloudsync/openrouter.go`:

```go
package cloudsync

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// OpenRouterModelsURL is the public model list the prices flow reads.
const OpenRouterModelsURL = "https://openrouter.ai/api/v1/models"

// APIPrice is what OpenRouter publishes for one model, per million tokens.
// A nil price is one the API did not report.
type APIPrice struct {
	Input, Cache, Output *float64
	// Skipped names the fields the API reported that are not prices: a
	// negative number (OpenRouter's -1 for "varies") or one that is not
	// finite. A model with any is not refreshed.
	Skipped []string
}

// perMillion converts one per-token price. OpenRouter sends decimal strings;
// a JSON number is accepted too. The arithmetic is modelman's
// (float(value) * 1_000_000), so both tools write the same digits.
func perMillion(v any) (price *float64, skipped bool) {
	var text string
	switch x := v.(type) {
	case nil:
		return nil, false
	case string:
		text = strings.TrimSpace(x)
	case json.Number:
		text = x.String()
	default:
		return nil, false
	}
	f, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return nil, false
	}
	f *= 1_000_000
	if f < 0 || math.IsNaN(f) || math.IsInf(f, 0) {
		return nil, true
	}
	return &f, false
}

// ParseOpenRouter reads the body of OpenRouterModelsURL into prices by model
// id. A body that is not `{"data": [...]}` is an error; an item with no
// string id is skipped.
func ParseOpenRouter(body []byte) (map[string]APIPrice, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("OpenRouter response is not JSON: %w", err)
	}
	obj, ok := doc.(map[string]any)
	if !ok {
		return nil, errors.New("OpenRouter response is not a JSON object")
	}
	items, ok := obj["data"].([]any)
	if !ok {
		return nil, errors.New("OpenRouter response missing 'data' list")
	}
	out := make(map[string]APIPrice, len(items))
	for _, item := range items {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id, ok := entry["id"].(string)
		if !ok {
			continue
		}
		pricing := entry
		if raw, has := entry["pricing"]; has {
			pricing, _ = raw.(map[string]any)
		}
		var p APIPrice
		for _, f := range []struct {
			key  string
			into **float64
		}{{"prompt", &p.Input}, {"completion", &p.Output}, {"input_cache_read", &p.Cache}} {
			v, skipped := perMillion(pricing[f.key])
			*f.into = v
			if skipped {
				p.Skipped = append(p.Skipped, f.key)
			}
		}
		out[id] = p
	}
	return out, nil
}

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

// PriceChange is one registry model OpenRouter has a price for.
type PriceChange struct {
	ModelID string
	// Before is nil when the entry had no cost table.
	Before *Cost
	After  Cost
	// Changed is false when the prices already match. The model is still
	// stamped: the stale-price notice reads the stamp.
	Changed bool
}

// PricePlan is what a price refresh changes.
type PricePlan struct {
	// Candidates counts the registry's OpenRouter-priced models.
	Candidates int
	Matched    []PriceChange
	Warnings   []string
}

// HasWork reports whether applying the plan would write anything. A matched
// model whose price is unchanged still gets its stamp.
func (p *PricePlan) HasWork() bool { return len(p.Matched) > 0 }

// PlanPrices matches the registry's OpenRouter-priced models to api on
// model_name. Input and output prices are always overwritten: they are what
// the refresh measures. The cache price is overwritten only when the API
// reported one (it usually does not), and the subscription and time_prices
// are never touched, so a refresh never clears a price set by hand.
func PlanPrices(entries []Entry, providers []Provider, api map[string]APIPrice) *PricePlan {
	plan := &PricePlan{}
	for _, e := range entries {
		if !OpenRouterPriced(e, providers) {
			continue
		}
		plan.Candidates++
		price, ok := api[e.ModelName]
		if !ok {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("No OpenRouter match for %s (%s)", e.ID, e.ModelName))
			continue
		}
		if len(price.Skipped) > 0 {
			// modelman refused the whole entry here too: a model whose price
			// "varies" has no per-token price to record.
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("Could not use OpenRouter's pricing for %s: its %s price is negative or not a number",
				e.ID, strings.Join(price.Skipped, " and ")))
			continue
		}
		if price.Input == nil && price.Output == nil && price.Cache == nil {
			plan.Warnings = append(plan.Warnings, "No pricing data for "+e.ID)
			continue
		}
		var after Cost
		if e.Cost != nil {
			after = *e.Cost
		}
		after.Input, after.Output = price.Input, price.Output
		if price.Cache != nil {
			after.Cache = price.Cache
		}
		plan.Matched = append(plan.Matched, PriceChange{
			ModelID: e.ID, Before: e.Cost, After: after, Changed: !sameCost(e.Cost, after),
		})
	}
	return plan
}

// Format prints the plan: each changed model as `id: old -> new`, then how
// many matched models already had the price. The command prints it for
// review and compares it with the re-plan made under the registry lock.
func (p *PricePlan) Format() string {
	var changed []string
	unchanged := 0
	for _, m := range p.Matched {
		if !m.Changed {
			unchanged++
			continue
		}
		changed = append(changed, fmt.Sprintf("  %s: %s -> %s", m.ModelID, formatCost(m.Before), formatCost(&m.After)))
	}
	lines := []string{fmt.Sprintf("openrouter.ai: %d OpenRouter-priced models in the registry (prices are input/cached/output per million tokens)", p.Candidates)}
	lines = append(lines, fmt.Sprintf("Price updates (%d):", len(changed)))
	lines = append(lines, changed...)
	lines = append(lines, fmt.Sprintf("Unchanged prices: %d", unchanged))
	for _, w := range p.Warnings {
		lines = append(lines, "warning: "+w)
	}
	return strings.Join(lines, "\n")
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run (from `wt/`): `go test -count=1 ./internal/cloudsync`
Expected:

```text
ok  	github.com/ohanaverse/local-ai-setup/wt/internal/cloudsync	(time)
```

(`-v` lists 46 passing top-level tests.)

- [ ] **Step 5: Commit**

From the repo root:

```bash
git add wt/internal/cloudsync
git commit -m "feat(wt): cloudsync.PlanPrices, the OpenRouter price refresh as a plan"
```

### Task 5: Applying both plans to the registry document

**Files:**
- Create: `wt/internal/cloudsync/apply_test.go`, `wt/internal/cloudsync/apply.go`
- Modify: `wt/CLAUDE.md`

**Interfaces:**
- Consumes: `config.UpdateRegistry(apply func(*RegistryDoc) error) (changed bool, err error)` (`internal/config/registry_write.go:68`); `(*RegistryDoc).PatchModel(id string, set map[string]any, unset []string) error` (`registry_doc.go:167`), `AddModel(table map[string]any) error` (`:198`), `CloneModel(fromID string, overrides map[string]any) error` (`:219`), `RemoveModel(id string) (*tomlw.Table, error)` (`:243`), `Models() []*tomlw.Table` (`:149`); `config.ErrRegistryInvalid` (`registry_validate.go:20`). Keys are dotted paths inside a row. `PatchModel` does not rewrite a value that is already there, which is why an integer price stays an integer.
- Produces:
  - `func Stamp(now time.Time) string` (`2006-01-02T15:04:05+00:00`, UTC)
  - `type CatalogApplied struct { Updated, Added, Removed int; Pulls, Removes []string }`, `func (a CatalogApplied) Changed() bool`. `Removes` holds cloud tags only: a removed entry's tag that is not one is never in it
  - `func (p *CatalogPlan) Apply(doc *config.RegistryDoc, pulled []string, now time.Time) (CatalogApplied, error)`
  - `type PricesApplied struct { Stamped, Changed int }`
  - `func (p *PricePlan) Apply(doc *config.RegistryDoc, now time.Time) (PricesApplied, error)`

Both `Apply` methods must stay pure: `UpdateRegistry` may run its apply function up to three times. They change the document they are given and nothing else, and they take the time as an argument.

- [ ] **Step 1: Write the failing tests**

Create `wt/internal/cloudsync/apply_test.go`:

```go
package cloudsync

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tomlw"
)

var applyNow = time.Date(2026, 10, 7, 14, 30, 5, 987, time.FixedZone("PDT", -7*3600))

const applyStamp = "2026-10-07T21:30:05+00:00"

// scratchRegistry writes content as this test's registry and returns its
// path.
func scratchRegistry(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "registry.toml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WT_REGISTRY", path)
	return path
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// modelRows decodes the registry at path into its model rows by id.
func modelRows(t *testing.T, path string) map[string]*tomlw.Table {
	t.Helper()
	root, err := tomlw.Decode([]byte(readFile(t, path)))
	if err != nil {
		t.Fatal(err)
	}
	v, _ := root.Get("models")
	rows := map[string]*tomlw.Table{}
	for _, item := range v.([]any) {
		row := item.(*tomlw.Table)
		rows[str(row, "id")] = row
	}
	return rows
}

// userTimePrice is a cost.time_prices row a user wrote: a label that is not
// the catalog's, a key wt does not model, integer prices, and keys in an
// order no schema gives. Neither flow owns it, so it must come through every
// write byte for byte. (It is in the layout the registry writer emits; a row
// written as an inline table is laid out again by any write, with the same
// keys in the same order.)
const userTimePrice = `[[models.cost.time_prices]]
timezone = "America/Los_Angeles"
note = "mine"
label = "weekend"
output_price_per_million = 1
input_price_per_million = 0.5

[[models.cost.time_prices.windows]]
start = "00:00"
days = [
    "sat",
    "sun",
]
end = "24:00"
`

// The registry the Apply tests start from, as modelman writes one: an entry
// to update (integer prices, a key in its cost table wt does not model, a
// hand-added key on the row, a time_prices row of the user's), one to
// re-tag, one off the page, and a local model that must come through
// untouched.
const catalogRegistry = `[[providers]]
id = "ollama"
name = "Ollama"
location = "local"

[providers.auth]
type = "none"

[[models]]
context_length = 131072
id = "ollama/deepseek-v4-pro:cloud"
family = "deepseek"
provider_id = "ollama"
model_name = "deepseek-v4-pro:cloud"
location = "cloud"
source = "curated"
tags = [
    "code",
]

[models.cost]
vendor_note = "kept"
input_price_per_million = 9
subscription_price = 100
subscription_period = "month"

` + userTimePrice + `
[[models]]
catalog_name = "mistral-large-3"
id = "ollama/mistral-large-3:cloud"
family = "mistral"
provider_id = "ollama"
model_name = "mistral-large-3:cloud"
location = "cloud"
source = "curated"
tags = [
    "design",
]

[models.cost]
input_price_per_million = 9.0
subscription_price = 100
subscription_period = "month"

[models.model_info]
supports_function_calling = true

[[models]]
id = "ollama/retired:cloud"
family = "retired"
provider_id = "ollama"
model_name = "retired:cloud"
location = "cloud"
source = "curated"
tags = []

[models.cost]
subscription_price = 100
subscription_period = "month"

[[models]]
id = "ollama/qwen3:8b"
family = "qwen3"
provider_id = "ollama"
model_name = "qwen3:8b"
location = "local"
source = "curated"
tags = []
`

func applyCatalog(t *testing.T, catalog Catalog, pulled []string, resolved map[string]string) (CatalogApplied, bool) {
	t.Helper()
	var done CatalogApplied
	changed, err := config.UpdateRegistry(func(d *config.RegistryDoc) error {
		var err error
		done, err = PlanCatalog(Entries(d.Models()), catalog, pulled, resolved).Apply(d, pulled, applyNow)
		return err
	})
	if err != nil {
		t.Fatalf("UpdateRegistry: %v", err)
	}
	return done, changed
}

// TestStamp pins the pricing_updated_at format to modelman's: UTC, whole
// seconds, a numeric +00:00 offset. Both tools write the key and the
// stale-price notice parses it, so a second spelling would be a second
// format to read forever.
func TestStamp(t *testing.T) {
	if got := Stamp(applyNow); got != applyStamp {
		t.Errorf("Stamp = %q, want %q", got, applyStamp)
	}
}

// TestCatalogApplyWritesWhatThePlanSays runs one sync with an update, a new
// model, a re-tag and a removal through the real registry writer and reads
// the file back. It pins what a user finds in registry.toml afterwards:
// prices and stamps on the rows the page lists, keys wt does not model still
// there, an integer the sync did not change still an integer, a time_prices
// row the user wrote untouched beside the off-peak row the catalog adds, a
// re-tagged entry's tags and model_info on its replacement, and the local
// model's row exactly as it was.
func TestCatalogApplyWritesWhatThePlanSays(t *testing.T) {
	path := scratchRegistry(t, catalogRegistry)
	catalog := catalogOf(withOffpeak(cm("deepseek-v4-pro", 1.32, 0.044, 3.96), f(0.66), f(0.022), f(1.98)),
		CatalogModel{Name: "mistral-large-3", Prices: PriceTriple{Input: f(0.5), Output: f(1.5)}}, cm("glm-5.3", 1.4, 0.26, 4.4))
	resolved := map[string]string{"deepseek-v4-pro": "deepseek-v4-pro:cloud", "mistral-large-3": "mistral-large-3:675b-cloud", "glm-5.3": "glm-5.3:cloud"}
	pulled := []string{"deepseek-v4-pro:cloud", "mistral-large-3:cloud", "stray:cloud", "qwen3:8b"}

	done, changed := applyCatalog(t, catalog, pulled, resolved)
	if !changed {
		t.Fatal("the registry was not written")
	}
	want := CatalogApplied{Updated: 1, Added: 2, Removed: 2,
		Pulls: []string{"mistral-large-3:675b-cloud", "glm-5.3:cloud"},
		// retired:cloud was never pulled, so there is nothing to rm for it.
		Removes: []string{"mistral-large-3:cloud", "stray:cloud"}}
	if !reflect.DeepEqual(done, want) {
		t.Errorf("applied = %+v\nwant      %+v", done, want)
	}

	text := readFile(t, path)
	for _, snippet := range []string{
		// The updated row: page prices in, everything else as it was.
		"context_length = 131072\ncatalog_name = \"deepseek-v4-pro\"\nid = \"ollama/deepseek-v4-pro:cloud\"",
		"vendor_note = \"kept\"\ninput_price_per_million = 1.32\ncache_price_per_million = 0.044\noutput_price_per_million = 3.96\nsubscription_price = 100\nsubscription_period = \"month\"\n",
		// The user's time_prices row byte for byte, and the off-peak row the
		// catalog owns added after it.
		"subscription_period = \"month\"\n\n" + userTimePrice + "\n[[models.cost.time_prices]]\nlabel = \"off-peak\"\ntimezone = \"UTC\"\ninput_price_per_million = 0.66\ncache_price_per_million = 0.022\noutput_price_per_million = 1.98\n",
		// The new row, whole, laid out as modelman lays one out.
		"[[models]]\ncatalog_name = \"glm-5.3\"\nid = \"ollama/glm-5.3:cloud\"\nfamily = \"glm-5.3\"\nprovider_id = \"ollama\"\nmodel_name = \"glm-5.3:cloud\"\nlocation = \"cloud\"\nsource = \"curated\"\ntags = []\npricing_updated_at = \"" + applyStamp + "\"\n\n" +
			"[models.cost]\ninput_price_per_million = 1.4\ncache_price_per_million = 0.26\noutput_price_per_million = 4.4\nsubscription_price = 100.0\nsubscription_period = \"month\"\n",
		// The local model, byte for byte.
		"[[models]]\nid = \"ollama/qwen3:8b\"\nfamily = \"qwen3\"\nprovider_id = \"ollama\"\nmodel_name = \"qwen3:8b\"\nlocation = \"local\"\nsource = \"curated\"\ntags = []\n",
	} {
		if !strings.Contains(text, snippet) {
			t.Errorf("registry.toml lacks:\n%s\n\nfile:\n%s", snippet, text)
		}
	}

	rows := modelRows(t, path)
	for _, gone := range []string{"ollama/retired:cloud", "ollama/mistral-large-3:cloud"} {
		if rows[gone] != nil {
			t.Errorf("%s is still in the registry", gone)
		}
	}
	retag := rows["ollama/mistral-large-3:675b-cloud"]
	if retag == nil {
		t.Fatalf("the re-tagged entry was not added; ids: %v", reflect.ValueOf(rows).MapKeys())
	}
	if str(retag, "model_name") != "mistral-large-3:675b-cloud" || str(retag, "family") != "mistral" || str(retag, "pricing_updated_at") != applyStamp {
		t.Errorf("re-tagged row keys: %v", retag.Keys())
	}
	if tags, _ := retag.Get("tags"); !reflect.DeepEqual(tags, []any{"design"}) {
		t.Errorf("re-tagged row tags = %v, want the old entry's [design]", tags)
	}
	if info, _ := retag.Get("model_info"); info == nil || !info.(*tomlw.Table).Has("supports_function_calling") {
		t.Error("re-tagged row lost the old entry's model_info")
	}
	cost, _ := retag.Get("cost")
	if c := costOf(cost.(*tomlw.Table)); !samePrice(c.Input, f(0.5)) || c.Cache != nil || !samePrice(c.Output, f(1.5)) || !samePrice(c.SubscriptionPrice, f(100)) {
		t.Errorf("re-tagged cost = %+v, want the page's 0.5/-/1.5 and the old subscription", c)
	}
	if str(rows["ollama/deepseek-v4-pro:cloud"], "catalog_name") != "deepseek-v4-pro" || str(rows["ollama/deepseek-v4-pro:cloud"], "pricing_updated_at") != applyStamp {
		t.Error("the updated row was not given its catalog name and stamp")
	}
	if rows["ollama/qwen3:8b"].Has("pricing_updated_at") {
		t.Error("the local model was stamped")
	}

	// The same page again: nothing to do, and the file is not touched.
	now := append(pulled, "mistral-large-3:675b-cloud", "glm-5.3:cloud")
	again, changed := applyCatalog(t, catalog, now, resolved)
	if changed || again.Changed() || readFile(t, path) != text {
		t.Errorf("a second sync of the same page wrote the registry again: %+v", again)
	}
}

// TestCatalogApplyNeverRemovesATagARemainingEntryNames pins the rule that
// protects a tag two rows share: when one row is removed and another still
// names its tag, the tag is not handed to `ollama rm`. Removing it would
// leave a registered model with nothing behind it.
func TestCatalogApplyNeverRemovesATagARemainingEntryNames(t *testing.T) {
	scratchRegistry(t, `[[models]]
id = "ollama/x:cloud"
family = "x"
provider_id = "ollama"
model_name = "x:cloud"
location = "cloud"

[[models]]
id = "ollama/x-second"
family = "x"
provider_id = "ollama"
model_name = "x:cloud"
location = "cloud"
`)
	pulled := []string{"x:cloud"}
	done, _ := applyCatalog(t, catalogOf(cm("x")), pulled, map[string]string{"x": "x:cloud"})
	if done.Removed != 1 || len(done.Removes) != 0 {
		t.Errorf("applied = %+v, want the second row removed from the registry and no tag to rm", done)
	}
}

// TestCatalogApplyNeverRemovesALocalTag pins the flow's promise that local
// models are never touched, for the one row that could break it: an entry
// marked location = "cloud" whose model_name is a local tag (a mislabelled
// hand edit). The page does not list it, so the entry leaves the registry,
// as the plan said; but its tag is real weights, and `ollama rm` of it would
// delete gigabytes nobody asked to lose. Only a cloud tag is ever handed to
// `ollama rm`.
func TestCatalogApplyNeverRemovesALocalTag(t *testing.T) {
	path := scratchRegistry(t, `[[models]]
id = "ollama/keep:cloud"
family = "keep"
provider_id = "ollama"
model_name = "keep:cloud"
location = "cloud"

[[models]]
id = "ollama/qwen3:8b"
family = "qwen3"
provider_id = "ollama"
model_name = "qwen3:8b"
location = "cloud"

[[models]]
id = "ollama/gone:cloud"
family = "gone"
provider_id = "ollama"
model_name = "gone:cloud"
location = "cloud"
`)
	pulled := []string{"keep:cloud", "qwen3:8b", "gone:cloud"}
	done, _ := applyCatalog(t, catalogOf(cm("keep")), pulled, map[string]string{"keep": "keep:cloud"})
	if done.Removed != 2 || !reflect.DeepEqual(done.Removes, []string{"gone:cloud"}) {
		t.Errorf("applied = %+v, want both entries removed from the registry and only gone:cloud to rm", done)
	}
	if rows := modelRows(t, path); rows["ollama/qwen3:8b"] != nil || rows["ollama/gone:cloud"] != nil || rows["ollama/keep:cloud"] == nil {
		t.Errorf("registry rows after the sync: %v", reflect.ValueOf(rows).MapKeys())
	}
}

// TestCatalogApplyRefusesARowThatWouldNotLoad pins what happens when a row
// the sync must change is already broken (here, a hand edit removed its
// family): the write is refused as a whole, the error names the row, and the
// file is untouched. The command reports that as a failed step; writing the
// rest would leave the registry half synced with nothing saying which half.
func TestCatalogApplyRefusesARowThatWouldNotLoad(t *testing.T) {
	const broken = `[[models]]
id = "ollama/x:cloud"
provider_id = "ollama"
model_name = "x:cloud"
location = "cloud"
`
	path := scratchRegistry(t, broken)
	_, err := config.UpdateRegistry(func(d *config.RegistryDoc) error {
		_, err := PlanCatalog(Entries(d.Models()), catalogOf(cm("x"), cm("y")), nil, map[string]string{"x": "x:cloud", "y": "y:cloud"}).Apply(d, nil, applyNow)
		return err
	})
	if !errors.Is(err, config.ErrRegistryInvalid) || !strings.Contains(err.Error(), `"ollama/x:cloud"`) {
		t.Errorf("err = %v, want ErrRegistryInvalid naming ollama/x:cloud", err)
	}
	if got := readFile(t, path); got != broken {
		t.Errorf("a refused write changed the file:\n%s", got)
	}
}

// TestPricesApplyStampsEveryMatchedModel runs a refresh through the real
// registry writer. It pins that every matched model is stamped, including
// one whose price did not move: the stale-price notice reads the newest
// stamp, and a run that stamped only changed models would leave the notice
// up after a refresh that found every price current. It also pins that a
// price that did not change is not rewritten (an integer stays an integer),
// that a cost.time_prices row the user wrote comes through byte for byte
// whether or not a price beside it moved (the prices flow owns no such row),
// and that an unmatched model is not stamped.
func TestPricesApplyStampsEveryMatchedModel(t *testing.T) {
	path := scratchRegistry(t, `[[providers]]
id = "openrouter"
name = "OpenRouter"
location = "cloud"

[providers.auth]
type = "api_key"
secret_ref = "OPENROUTER_API_KEY"

[[models]]
id = "openrouter/same"
family = "x"
provider_id = "openrouter"
model_name = "vendor/same"
location = "cloud"
pricing_updated_at = "2026-09-01T00:00:00+00:00"

[models.cost]
note = "kept"
input_price_per_million = 3
cache_price_per_million = 0.5
output_price_per_million = 15
subscription_price = 20
subscription_period = "month"

`+userTimePrice+`
[[models]]
id = "openrouter/moved"
family = "x"
provider_id = "openrouter"
model_name = "vendor/moved"
location = "cloud"

[[models]]
id = "openrouter/repriced"
family = "x"
provider_id = "openrouter"
model_name = "vendor/repriced"
location = "cloud"

[models.cost]
input_price_per_million = 7
output_price_per_million = 15

`+userTimePrice+`
[[models]]
id = "openrouter/missing"
family = "x"
provider_id = "openrouter"
model_name = "vendor/missing"
location = "cloud"
`)
	api := apiOf(t, `{"data": [
		{"id": "vendor/same", "pricing": {"prompt": "0.000003", "completion": "0.000015"}},
		{"id": "vendor/moved", "pricing": {"prompt": "0.000001", "completion": "0.000002"}},
		{"id": "vendor/repriced", "pricing": {"prompt": "0.000003", "completion": "0.000015"}}
	]}`)
	var done PricesApplied
	changed, err := config.UpdateRegistry(func(d *config.RegistryDoc) error {
		var err error
		done, err = PlanPrices(Entries(d.Models()), Providers(d.Providers()), api).Apply(d, applyNow)
		return err
	})
	if err != nil || !changed {
		t.Fatalf("UpdateRegistry: changed %v, err %v", changed, err)
	}
	if done != (PricesApplied{Stamped: 3, Changed: 2}) {
		t.Errorf("applied = %+v, want 3 stamped, 2 changed", done)
	}
	text := readFile(t, path)
	for _, snippet := range []string{
		"model_name = \"vendor/same\"\nlocation = \"cloud\"\npricing_updated_at = \"" + applyStamp + "\"\n\n[models.cost]\nnote = \"kept\"\ninput_price_per_million = 3\ncache_price_per_million = 0.5\noutput_price_per_million = 15\nsubscription_price = 20\nsubscription_period = \"month\"\n\n" + userTimePrice + "\n[[models]]\nid = \"openrouter/moved\"",
		// A cost block whose input price moved: that one value is rewritten,
		// the price that did not move is still an integer, and the user's
		// time_prices row is byte for byte what it was.
		"model_name = \"vendor/repriced\"\nlocation = \"cloud\"\npricing_updated_at = \"" + applyStamp + "\"\n\n[models.cost]\ninput_price_per_million = 3.0\noutput_price_per_million = 15\n\n" + userTimePrice + "\n[[models]]\nid = \"openrouter/missing\"",
		"model_name = \"vendor/moved\"\nlocation = \"cloud\"\npricing_updated_at = \"" + applyStamp + "\"\n\n[models.cost]\ninput_price_per_million = 1.0\noutput_price_per_million = 2.0\n",
		"id = \"openrouter/missing\"\nfamily = \"x\"\nprovider_id = \"openrouter\"\nmodel_name = \"vendor/missing\"\nlocation = \"cloud\"\n",
	} {
		if !strings.Contains(text, snippet) {
			t.Errorf("registry.toml lacks:\n%s\n\nfile:\n%s", snippet, text)
		}
	}
	if strings.Count(text, "pricing_updated_at") != 3 {
		t.Errorf("want exactly the three matched models stamped:\n%s", text)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run (from `wt/`): `go test -count=1 ./internal/cloudsync`
Expected:

```text
# github.com/ohanaverse/local-ai-setup/wt/internal/cloudsync [github.com/ohanaverse/local-ai-setup/wt/internal/cloudsync.test]
internal/cloudsync/apply_test.go:154:96: undefined: CatalogApplied
internal/cloudsync/apply_test.go:156:11: undefined: CatalogApplied
internal/cloudsync/apply_test.go:159:75: PlanCatalog(Entries(d.Models()), catalog, pulled, resolved).Apply undefined (type *CatalogPlan has no field or method Apply)
internal/cloudsync/apply_test.go:173:12: undefined: Stamp
```

- [ ] **Step 3: Write the two Apply methods**

Create `wt/internal/cloudsync/apply.go`:

```go
package cloudsync

import (
	"slices"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// Stamp formats a pricing_updated_at value the way modelman writes it: UTC,
// to the second, with a numeric offset.
func Stamp(now time.Time) string { return now.UTC().Format("2006-01-02T15:04:05+00:00") }

// costPatch is the PatchModel arguments that give a row the prices of c: a
// price that is nil is deleted, and the time_prices key goes when no row is
// left. A subscription is only ever set, never cleared: no flow changes one.
func costPatch(c Cost) (set map[string]any, unset []string) {
	set = map[string]any{}
	for _, kv := range []struct {
		key string
		v   *float64
	}{
		{"cost.input_price_per_million", c.Input},
		{"cost.cache_price_per_million", c.Cache},
		{"cost.output_price_per_million", c.Output},
		{"cost.subscription_price", c.SubscriptionPrice},
	} {
		switch {
		case kv.v != nil:
			set[kv.key] = *kv.v
		case kv.key != "cost.subscription_price":
			unset = append(unset, kv.key)
		}
	}
	if c.SubscriptionPeriod != nil {
		set["cost.subscription_period"] = *c.SubscriptionPeriod
	}
	if len(c.TimePrices) == 0 {
		unset = append(unset, "cost.time_prices")
	} else {
		rows := make([]any, len(c.TimePrices))
		for i, row := range c.TimePrices {
			rows[i] = row
		}
		set["cost.time_prices"] = rows
	}
	return set, unset
}

// CatalogApplied is what Apply did to the registry and what is left to do in
// ollama.
type CatalogApplied struct {
	Updated, Added, Removed int
	// Pulls are the tags to `ollama pull`, in plan order.
	Pulls []string
	// Removes are the tags to `ollama rm`: first those of the entries Apply
	// removed that were pulled, then the strays. A tag a remaining ollama
	// entry still names is never among them, and neither is a tag that is
	// not a cloud tag: this flow never removes local weights.
	Removes []string
}

// Changed reports whether Apply changed a price or the set of models, which
// is when LiteLLM's routes need a sync.
func (a CatalogApplied) Changed() bool { return a.Updated+a.Added+a.Removed > 0 }

// Apply makes the plan's registry changes on doc: prices and the catalog
// name on updated entries, the new entries, and the removals. It runs no
// command and reads no clock, so it is safe as (part of) a
// config.UpdateRegistry apply function. pulled is the `ollama list` the plan
// was made from.
//
// Every row it changes or adds is stamped with now in pricing_updated_at.
// An entry it removes is gone from the registry before its tag is removed
// from ollama: if the `ollama rm` then fails, the tag is a stray the next
// run removes.
func (p *CatalogPlan) Apply(doc *config.RegistryDoc, pulled []string, now time.Time) (CatalogApplied, error) {
	var done CatalogApplied
	stamp := Stamp(now)
	for _, u := range p.Updates {
		set, unset := costPatch(u.After)
		set[CatalogNameKey] = u.CatalogName
		set["pricing_updated_at"] = stamp
		if err := doc.PatchModel(u.ModelID, set, unset); err != nil {
			return CatalogApplied{}, err
		}
		done.Updated++
	}
	for _, a := range p.Additions {
		set, unset := costPatch(a.Cost)
		set[CatalogNameKey] = a.CatalogName
		set["pricing_updated_at"] = stamp
		if a.CloneOf != "" {
			if err := doc.CloneModel(a.CloneOf, map[string]any{
				"id": a.ID, "model_name": a.ModelName, "location": "cloud", "source": "curated",
			}); err != nil {
				return CatalogApplied{}, err
			}
		} else if err := doc.AddModel(map[string]any{
			"id": a.ID, "family": a.Family, "provider_id": ollamaProvider, "model_name": a.ModelName,
			"location": "cloud", "source": "curated", "tags": []string{},
		}); err != nil {
			return CatalogApplied{}, err
		}
		if err := doc.PatchModel(a.ID, set, unset); err != nil {
			return CatalogApplied{}, err
		}
		done.Added++
	}
	var removedTags []string
	for _, id := range p.Removals {
		row, err := doc.RemoveModel(id)
		if err != nil {
			return CatalogApplied{}, err
		}
		removedTags = append(removedTags, str(row, "model_name"))
		done.Removed++
	}

	named := map[string]bool{}
	for _, e := range Entries(doc.Models()) {
		if e.ProviderID == ollamaProvider {
			named[e.ModelName] = true
		}
	}
	for _, tag := range removedTags {
		// Only a cloud tag: a removed entry that was marked cloud over a
		// local tag (the plan warned) must not cost anyone their weights.
		if IsCloudTag(tag) && slices.Contains(pulled, tag) && !named[tag] && !slices.Contains(done.Removes, tag) {
			done.Removes = append(done.Removes, tag)
		}
	}
	for _, tag := range p.StrayTags {
		if !named[tag] && !slices.Contains(done.Removes, tag) {
			done.Removes = append(done.Removes, tag)
		}
	}
	for _, pull := range p.Pulls {
		done.Pulls = append(done.Pulls, pull.Tag)
	}
	return done, nil
}

// PricesApplied is what PricePlan.Apply did.
type PricesApplied struct {
	// Stamped counts the models whose pricing_updated_at was set: every
	// matched one. Changed counts those whose price moved, which is when
	// LiteLLM's routes need a sync.
	Stamped, Changed int
}

// Apply writes the plan's prices to doc and stamps every matched model with
// now, changed or not: the stale-price notice reads the newest stamp, so a
// refresh that found every price current must still leave its mark. A price
// that did not change is not rewritten (config.RegistryDoc.PatchModel skips a
// value that is already there), so an integer stays an integer. Like
// CatalogPlan.Apply it is safe inside config.UpdateRegistry.
func (p *PricePlan) Apply(doc *config.RegistryDoc, now time.Time) (PricesApplied, error) {
	var done PricesApplied
	stamp := Stamp(now)
	for _, m := range p.Matched {
		set, unset := costPatch(m.After)
		set["pricing_updated_at"] = stamp
		if err := doc.PatchModel(m.ModelID, set, unset); err != nil {
			return PricesApplied{}, err
		}
		done.Stamped++
		if m.Changed {
			done.Changed++
		}
	}
	return done, nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run (from `wt/`): `go test -count=1 ./internal/cloudsync`
Expected:

```text
ok  	github.com/ohanaverse/local-ai-setup/wt/internal/cloudsync	(time)
```

(`-v` lists 52 passing top-level tests.)

- [ ] **Step 5: Record the package in `wt/CLAUDE.md`**

In `wt/CLAUDE.md`, find:

```text
internal/{config,tomlw,rotation,usage,refcount,survey,agents,profiles,guard,worktree,initseed,themes,tui,configeditor,ollamacheck,catalog,localmodels,lifecycle,litellm,spend,smoke}`.
```

and replace it with:

```text
internal/{config,tomlw,cloudsync,rotation,usage,refcount,survey,agents,profiles,guard,worktree,initseed,themes,tui,configeditor,ollamacheck,catalog,localmodels,lifecycle,litellm,spend,smoke}`.
```

In `wt/CLAUDE.md`, find:

```text
| `internal/rotation/` | global rotation state (`rotation.state`) + next-model selection |
```

and replace it with:

```text
| `internal/cloudsync/` | the pure core of `wt cloud-sync`: `ParsePricing` (the only code that knows ollama.com/pricing's HTML; `golang.org/x/net/html` tokenizer, fail-loud `*ParseError`), `ResolveCloudTags`/`VerifiedTags`, `PlanCatalog` (+ `MassRemoval`, `RemovalDigest`, `Format`), `ParseOpenRouter`/`PlanPrices`, and the two `Apply` methods that change a `config.RegistryDoc`. No I/O, no clock: see [Cloud sync](#cloud-sync-wt-cloud-sync) |
| `internal/rotation/` | global rotation state (`rotation.state`) + next-model selection |
```

In `wt/CLAUDE.md`, find:

```text
## LiteLLM routes (`wt litellm`)
```

and replace it with:

```text
## Cloud sync (`wt cloud-sync`)

`internal/cloudsync` is the Go port of modelman's `pricing.py` and `ollama_catalog.py`: everything that decides what a sync changes, with no I/O. Its caller owns the fetches, the ollama CLI, the confirmation and the exit codes.

- **`ParsePricing` is the only code that knows the pricing page's HTML.** Every way the page can stop looking like a price table is a `*ParseError`, never a short catalog: a catalog that lost its rows would plan the removal of every ollama cloud entry. When ollama changes the page, fix it there and replace `internal/cloudsync/testdata/ollama_pricing.html`.
- **The plan's text is the plan.** `CatalogPlan.Format` and `PricePlan.Format` are what the user approves and what the command compares with the re-plan made under the registry lock; both planners are deterministic. A change to either `Format` changes what counts as "the same plan".
- **`RemovalDigest` is byte-identical to modelman's** for the same removals and stray tags (`TestRemovalDigestMatchesModelman`), so a digest from either tool's dry run approves the other's apply until modelman is deleted.
- **The `Apply` methods are pure** (they change the `RegistryDoc` they are given and nothing else), because `config.UpdateRegistry` may run them up to three times.
- **Only the row labelled `off-peak` in `cost.time_prices` is the catalog's.** Every other row, the subscription, and any key wt does not model are kept. Nothing applies time prices at launch; they are stored.

## LiteLLM routes (`wt litellm`)
```

- [ ] **Step 6: Verify the slice**

From `wt/`:

```bash
test -z "$(gofmt -l .)" && go vet ./... && go test -count=1 ./... && make check
```

Expected: 23 `ok` lines, no `FAIL`, and `make check` finishing with no error. Then from the monorepo root:

```bash
make test-all
make check-links
```

Expected: `make test-all` exits 0 and `make check-links` prints `ALL LINKS OK`. Nothing in PR 1 is read by Python, so llmbench's and modelman's suites are unchanged; the run is here because this is the slice that raises `golang.org/x/sys` and `golang.org/x/text`, and the spec asks for `make test-all` at the end of every step.

- [ ] **Step 7: Commit**

From the repo root:

```bash
git add wt/internal/cloudsync wt/CLAUDE.md
git commit -m "feat(wt): cloudsync applies a catalog plan and a price plan to the registry document"
```

PR 1 is ready. Its description carries the `go.mod` and `go.sum` diff from Task 1, Step 6 and the sentence "full wt suite passes on the raised x/sys and x/text". **Ask the owner before pushing or opening the PR.**

---

## PR 2 — `wt cloud-sync --only prices`, the exit-code error, the derived notice

Branch: `git switch -c feat/wt-cloud-sync-prices main`, after PR 1 has merged.

### Task 6: `config.ReadRegistryDoc` and `Model.PricingUpdated`

**Files:**
- Create: `wt/internal/config/registry_read_test.go`
- Modify: `wt/internal/config/registry_write.go`, `wt/internal/config/config.go`, `wt/docs/internals/config-and-registry.md`

**Interfaces:**
- Consumes (all unexported, same package): `readRegistryFile(path string) (target string, data []byte, exists bool, err error)` (`registry_write.go:104`), `newRegistryDoc(root *tomlw.Table) (*RegistryDoc, error)` (`registry_doc.go`), `registryFileError`, `ErrRegistryMissing`; the test helpers `scratchRegistry`, `readFile`, `dirNames` (`registry_write_test.go:15-53`) and `loadRegistry()` (`registry.go`).
- Produces:
  - `func ReadRegistryDoc() (*RegistryDoc, error)`
  - `Model.PricingUpdatedAt any` (TOML key `pricing_updated_at`)
  - `func (m Model) PricingUpdated() (time.Time, bool)`

- [ ] **Step 1: Write the failing tests**

Create `wt/internal/config/registry_read_test.go`:

```go
package config

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestReadRegistryDocIsAReadOnlyView pins the document a planner reads
// before anything is decided: it holds the rows as written (a key wt does
// not model included), and getting it leaves no trace — no lock file, no
// write — so a `--dry-run` really changes nothing. A change made to it goes
// nowhere.
func TestReadRegistryDocIsAReadOnlyView(t *testing.T) {
	const content = `# a comment a write would drop
[[models]]
id = "ollama/x:cloud"
family = "x"
provider_id = "ollama"
model_name = "x:cloud"
catalog_name = "x"
`
	path := scratchRegistry(t, content)
	doc, err := ReadRegistryDoc()
	if err != nil {
		t.Fatalf("ReadRegistryDoc: %v", err)
	}
	rows := doc.Models()
	if len(rows) != 1 {
		t.Fatalf("models = %d, want 1", len(rows))
	}
	if v, _ := rows[0].Get("catalog_name"); v != "x" {
		t.Errorf("catalog_name = %v, want the key wt's typed reader does not model", v)
	}
	if err := doc.PatchModel("ollama/x:cloud", map[string]any{"family": "changed"}, nil); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); got != content {
		t.Errorf("reading the registry changed it:\n%s", got)
	}
	if names := dirNames(t, filepath.Dir(path)); !reflect.DeepEqual(names, []string{"registry.toml"}) {
		t.Errorf("reading the registry left files behind: %v", names)
	}
}

// TestReadRegistryDocRefusesWhatAWriteWouldRefuse pins that a dry run meets
// the same refusals the apply would: a registry that is missing (with the
// command that creates one), one that is not TOML, and one with a top-level
// key a write will not accept. Without this a dry run would print a plan the
// real run can never apply.
func TestReadRegistryDocRefusesWhatAWriteWouldRefuse(t *testing.T) {
	cases := []struct {
		name, content string
		want          error
		says          string
	}{
		{"missing", "", ErrRegistryMissing, "wt model init"},
		{"not TOML", "not [ valid toml", ErrRegistryFile, "parse"},
		{"an unknown top-level key", "[[model]]\nid = \"x\"\n", ErrRegistryTopLevel, "`model`"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scratchRegistry(t, tc.content)
			_, err := ReadRegistryDoc()
			if !errors.Is(err, tc.want) || !strings.Contains(err.Error(), tc.says) {
				t.Errorf("err = %v, want %v naming %q", err, tc.want, tc.says)
			}
		})
	}
}

// TestModelPricingUpdatedReadsEverySpelling pins that pricing_updated_at
// never fails the registry load, whatever a hand edit left there. wt stamps
// a string; a TOML date-time or a typo in that key must not turn every
// launch into a config error over a value only the stale-price notice reads.
func TestModelPricingUpdatedReadsEverySpelling(t *testing.T) {
	scratchRegistry(t, `[[providers]]
id = "openrouter"
name = "OpenRouter"
location = "cloud"

[providers.auth]
type = "api_key"

[[models]]
id = "openrouter/stamped"
family = "x"
provider_id = "openrouter"
model_name = "vendor/stamped"
pricing_updated_at = "2026-10-04T02:31:25+00:00"

[[models]]
id = "openrouter/datetime"
family = "x"
provider_id = "openrouter"
model_name = "vendor/datetime"
pricing_updated_at = 2026-10-04T02:31:25Z

[[models]]
id = "openrouter/no-offset"
family = "x"
provider_id = "openrouter"
model_name = "vendor/no-offset"
pricing_updated_at = "2026-10-04T02:31:25"

[[models]]
id = "openrouter/date"
family = "x"
provider_id = "openrouter"
model_name = "vendor/date"
pricing_updated_at = "2026-10-04"

[[models]]
id = "openrouter/typo"
family = "x"
provider_id = "openrouter"
model_name = "vendor/typo"
pricing_updated_at = "yesterday"

[[models]]
id = "openrouter/number"
family = "x"
provider_id = "openrouter"
model_name = "vendor/number"
pricing_updated_at = 20261004

[[models]]
id = "openrouter/none"
family = "x"
provider_id = "openrouter"
model_name = "vendor/none"
`)
	_, models, err := loadRegistry()
	if err != nil {
		t.Fatalf("loadRegistry: %v", err)
	}
	at := time.Date(2026, 10, 4, 2, 31, 25, 0, time.UTC)
	want := map[string]*time.Time{
		"openrouter/stamped": &at, "openrouter/datetime": &at, "openrouter/no-offset": &at,
		"openrouter/date": ptrTime(time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)),
		"openrouter/typo": nil, "openrouter/number": nil, "openrouter/none": nil,
	}
	if len(models) != len(want) {
		t.Fatalf("models = %d, want %d", len(models), len(want))
	}
	for _, m := range models {
		got, ok := m.PricingUpdated()
		switch w := want[m.ID]; {
		case w == nil && ok:
			t.Errorf("%s: PricingUpdated = %v, want not said", m.ID, got)
		case w != nil && (!ok || !got.Equal(*w)):
			t.Errorf("%s: PricingUpdated = (%v, %v), want %v", m.ID, got, ok, *w)
		}
	}
}

func ptrTime(t time.Time) *time.Time { return &t }
```

- [ ] **Step 2: Run the tests to verify they fail**

Run (from `wt/`): `go test -count=1 ./internal/config -run 'TestReadRegistryDoc|TestModelPricingUpdated'`
Expected:

```text
# github.com/ohanaverse/local-ai-setup/wt/internal/config [github.com/ohanaverse/local-ai-setup/wt/internal/config.test]
internal/config/registry_read_test.go:27:14: undefined: ReadRegistryDoc
internal/config/registry_read_test.go:67:14: undefined: ReadRegistryDoc
internal/config/registry_read_test.go:150:16: m.PricingUpdated undefined (type Model has no field or method PricingUpdated)
FAIL	github.com/ohanaverse/local-ai-setup/wt/internal/config [build failed]
```

- [ ] **Step 3: Add `ReadRegistryDoc`**

In `wt/internal/config/registry_write.go`, find:

```go

// readRegistryFile resolves and reads the registry: the file to write, its
```

and replace it with:

```go

// ReadRegistryDoc reads registry.toml into a RegistryDoc without the lock and
// without writing: the document a planner looks at before anything is
// decided. It refuses what UpdateRegistry refuses (a broken link, a file that
// is not TOML, an unknown top-level key), so a dry run reports the problem a
// write would hit. A missing registry is ErrRegistryMissing: unlike a write,
// a read has nothing to create.
//
// The document is detached. Changing it changes nothing on disk; a write
// goes through UpdateRegistry, which reads the file again under the lock.
func ReadRegistryDoc() (*RegistryDoc, error) {
	path := RegistryPath()
	_, data, exists, err := readRegistryFile(path)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, fmt.Errorf("%w at %s — seed it with `wt model init`", ErrRegistryMissing, path)
	}
	root, err := tomlw.Decode(data)
	if err != nil {
		return nil, registryFileError(fmt.Errorf("parse %s: %w", path, err))
	}
	doc, err := newRegistryDoc(root)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return doc, nil
}

// readRegistryFile resolves and reads the registry: the file to write, its
```

- [ ] **Step 4: Add the field and its reader to `config.Model`**

In `wt/internal/config/config.go`, find:

```go
	ModelInfo map[string]any `toml:"model_info,omitempty"`
	Native    bool           `toml:"-"` // derived: provider auth.type == "native"; not persisted
}
```

and replace it with:

```go
	ModelInfo map[string]any `toml:"model_info,omitempty"`
	// PricingUpdatedAt is the registry's pricing_updated_at, as written:
	// the string `wt cloud-sync` and modelman stamp, or a TOML date-time a
	// hand edit left. It is `any` so that neither spelling can fail the
	// load; read it through PricingUpdated.
	PricingUpdatedAt any  `toml:"pricing_updated_at,omitempty"`
	Native           bool `toml:"-"` // derived: provider auth.type == "native"; not persisted
}

// pricingStampLayouts are the spellings of pricing_updated_at wt reads: the
// stamp both tools write, then what a hand edit plausibly leaves (a
// date-time with no offset, a bare date; both taken as UTC).
var pricingStampLayouts = []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"}

// PricingUpdated is when the model's token prices were last refreshed, and
// whether the registry says. A value that is not a time (a typo, a number)
// counts as not said.
func (m Model) PricingUpdated() (time.Time, bool) {
	switch v := m.PricingUpdatedAt.(type) {
	case time.Time:
		return v, true
	case string:
		for _, layout := range pricingStampLayouts {
			if t, err := time.Parse(layout, v); err == nil {
				return t, true
			}
		}
	}
	return time.Time{}, false
}
```

`config.go` already imports `time`. Adding a field to `Model` cannot change what a registry write touches: nothing writes a `config.Model` (`TestAWriteNeverTouchesAKeyItWasNotAskedTo` pins that).

- [ ] **Step 5: Run the tests to verify they pass**

Run (from `wt/`): `go test -count=1 ./internal/config -run 'TestReadRegistryDoc|TestModelPricingUpdated'`
Expected:

```text
ok  	github.com/ohanaverse/local-ai-setup/wt/internal/config	(time)
```

Then the whole package, since `Model` is compared in many tests: `go test -count=1 ./internal/config ./internal/agents ./internal/litellm ./cmd/wt`. Expected: four `ok` lines.

- [ ] **Step 6: Update the internals doc**

In `wt/docs/internals/config-and-registry.md`, find:

```text
never for a native provider). `fetch` is ignored.
```

and replace it with:

```text
never for a native provider), and a model's `pricing_updated_at` (`Model.PricingUpdatedAt`, typed `any` so that neither the string both tools stamp nor a TOML date-time a hand edit left can fail the load; read it through `Model.PricingUpdated()`). `fetch` is ignored.

**Reading the registry as a document.** `config.ReadRegistryDoc()` returns the same `RegistryDoc` `UpdateRegistry` hands to `apply`, read with no lock and never written: what a planner looks at before anything is decided (`wt cloud-sync`'s dry run). It refuses what a write refuses (a broken link, a file that is not TOML, an unknown top-level key) and a missing registry is `ErrRegistryMissing`. The document is detached; a write goes through `UpdateRegistry`, which reads the file again under the lock.
```

- [ ] **Step 7: Commit**

From the repo root:

```bash
git add wt/internal/config wt/docs/internals/config-and-registry.md
git commit -m "feat(wt): config.ReadRegistryDoc, and Model.PricingUpdated for pricing_updated_at"
```

### Task 7: The stale-price notice derives its date; `PriceRefreshLastRun` is removed

**Files:**
- Modify: `wt/internal/agents/price_notice_test.go`, `wt/internal/agents/price_notice.go` (rewritten), `wt/internal/config/modelman.go`, `wt/internal/config/modelman_test.go`, `wt/internal/config/modelman_fixture_test.go`, `wt/internal/config/registry.go`, `wt/internal/config/config.go`, `wt/docs/internals/launch-flow.md`, `wt/CHANGELOG.md`, `docs/guides/00-config-map.md`, `docs/contracts/modelman.sample.toml`, `CLAUDE.md`

**Interfaces:**
- Consumes: `Model.PricingUpdated()` (Task 6); `(*config.Config).OpenRouterPriced(m Model) bool` (`config.go:880`). The two callers of `agents.PrintPriceNotice(cfg)` (`cmd/wt/launch.go:29`, `internal/tui/survey.go:17`) do not change.
- Produces:
  - `func LastPriceRefresh(cfg *config.Config, now time.Time) (time.Time, bool)` (`now` is what a stamp is "too far ahead" of)
  - `func PriceNotice(last time.Time, present bool, now time.Time) string` (the signature changes: `last` was a `YYYY-MM-DD` string)
  - `func PrintPriceNotice(cfg *config.Config)` and `func HasOpenRouterPricedModel(cfg *config.Config) bool`, unchanged in signature
  - removed: `config.PriceRefreshLastRun`, `modelmanState.PriceRefreshLastRun`

The notice's two wordings, exactly: `wt: token pricing last refreshed <YYYY-MM-DD> — run 'wt cloud-sync'` and `wt: token pricing has never been refreshed — run 'wt cloud-sync'`.

- [ ] **Step 1: Rewrite the notice's tests**

In `wt/internal/agents/price_notice_test.go`, replace everything from the line that starts `// TestPriceNoticeTodaySilent guards` up to, but not including, the line that starts `// TestHasOpenRouterPricedModel pins` (47 lines) with:

```go
// TestPriceNoticeThresholds pins when the notice speaks. Refreshing is manual
// now, so the notice is the only reminder: it must stay quiet for a week
// after a refresh (a line on every launch trains people to ignore it), speak
// once the newest price is more than seven days old, and say "never" when no
// OpenRouter price carries a stamp. Both wordings name the command to run.
func TestPriceNoticeThresholds(t *testing.T) {
	now := time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		last    time.Time
		present bool
		want    string
	}{
		{"refreshed a minute ago", now.Add(-time.Minute), true, ""},
		{"six days ago", now.Add(-6 * 24 * time.Hour), true, ""},
		{"exactly seven days ago", now.Add(-7 * 24 * time.Hour), true, ""},
		{"seven days and a second", now.Add(-7*24*time.Hour - time.Second), true, "wt: token pricing last refreshed 2026-09-30 — run 'wt cloud-sync'"},
		{"a month ago", time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC), true, "wt: token pricing last refreshed 2026-09-01 — run 'wt cloud-sync'"},
		{"a stamp a few minutes ahead (a skewed clock)", now.Add(10 * time.Minute), true, ""},
		{"never", time.Time{}, false, "wt: token pricing has never been refreshed — run 'wt cloud-sync'"},
	}
	for _, tc := range cases {
		if got := PriceNotice(tc.last, tc.present, now); got != tc.want {
			t.Errorf("%s: PriceNotice = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestPriceNoticeShowsTheLocalDate pins that the date in the notice is the
// reader's, not UTC's: a refresh at 02:31 UTC on the 4th happened on the
// evening of the 3rd in California, and that is the day the user remembers
// running it.
func TestPriceNoticeShowsTheLocalDate(t *testing.T) {
	pacific := time.FixedZone("PDT", -7*3600)
	last := time.Date(2026, 10, 4, 2, 31, 25, 0, time.UTC)
	now := time.Date(2026, 10, 20, 9, 0, 0, 0, pacific)
	if got, want := PriceNotice(last, true, now), "wt: token pricing last refreshed 2026-10-03 — run 'wt cloud-sync'"; got != want {
		t.Errorf("PriceNotice = %q, want %q", got, want)
	}
}

// TestLastPriceRefresh pins where the date comes from now that nothing
// stores it: the newest pricing_updated_at among OpenRouter-priced models
// only. An ollama cloud model's stamp must not count (the catalog flow
// writes those, and a catalog-only run would silence a notice about prices
// it never refreshed), a model without a stamp or with a mistyped one is
// skipped, not fatal, and with no usable stamp the answer is "never". A
// stamp more than a day ahead of the clock is a typo and is skipped too:
// counted, a year typed as 2062 would silence the notice for good.
func TestLastPriceRefresh(t *testing.T) {
	providers := []config.Provider{
		{ID: "ollama", Location: config.LocationLocal},
		{ID: "openrouter", Location: config.LocationCloud},
	}
	or := func(id string, stamp any) config.Model {
		return config.Model{ID: id, ProviderID: "openrouter", PricingUpdatedAt: stamp}
	}
	ollamaCloud := config.Model{ID: "ollama/glm:cloud", ProviderID: "ollama", Location: config.LocationCloud, PricingUpdatedAt: "2026-10-07T00:00:00+00:00"}
	newest := time.Date(2026, 10, 4, 2, 31, 25, 0, time.UTC)
	now := time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)

	cfg := &config.Config{Providers: providers, Models: []config.Model{
		ollamaCloud,
		or("openrouter/old", "2026-09-01T00:00:00+00:00"),
		or("openrouter/newest", "2026-10-03T19:31:25-07:00"),
		or("openrouter/unstamped", nil),
		or("openrouter/typo", "last tuesday"),
		or("openrouter/number", int64(20261004)),
	}}
	if got, ok := LastPriceRefresh(cfg, now); !ok || !got.Equal(newest) {
		t.Errorf("LastPriceRefresh = (%v, %v), want (%v, true)", got, ok, newest)
	}

	// A date-time a hand edit left as a TOML value, and one with no offset.
	cfg.Models = []config.Model{or("openrouter/a", newest), or("openrouter/b", "2026-10-01T08:00:00")}
	if got, ok := LastPriceRefresh(cfg, now); !ok || !got.Equal(newest) {
		t.Errorf("with a TOML date-time: LastPriceRefresh = (%v, %v), want (%v, true)", got, ok, newest)
	}

	// A stamp far in the future is a typo, not a refresh: it is skipped, so
	// it cannot silence the notice until that date comes. One that is ahead
	// by less than a day is another machine's clock, and counts.
	typo := or("openrouter/typo-year", "2062-10-04T02:31:25+00:00")
	cfg.Models = []config.Model{typo, or("openrouter/a", newest)}
	if got, ok := LastPriceRefresh(cfg, now); !ok || !got.Equal(newest) {
		t.Errorf("with a stamp in 2062: LastPriceRefresh = (%v, %v), want (%v, true)", got, ok, newest)
	}
	cfg.Models = []config.Model{typo}
	if got, ok := LastPriceRefresh(cfg, now); ok {
		t.Errorf("with only a stamp in 2062: LastPriceRefresh = (%v, true), want never", got)
	}
	ahead := now.Add(23 * time.Hour)
	cfg.Models = []config.Model{or("openrouter/skewed", ahead), or("openrouter/a", newest)}
	if got, ok := LastPriceRefresh(cfg, now); !ok || !got.Equal(ahead) {
		t.Errorf("with a stamp 23 hours ahead: LastPriceRefresh = (%v, %v), want (%v, true)", got, ok, ahead)
	}

	cfg.Models = []config.Model{ollamaCloud, or("openrouter/unstamped", nil)}
	if got, ok := LastPriceRefresh(cfg, now); ok {
		t.Errorf("with no stamped OpenRouter model: LastPriceRefresh = (%v, true), want never", got)
	}
	if _, ok := LastPriceRefresh(nil, now); ok {
		t.Error("LastPriceRefresh(nil, now) reported a refresh")
	}
}
```

In `wt/internal/agents/price_notice_test.go`, find:

```go
import (
	"strings"
	"testing"
```

and replace it with:

```go
import (
	"testing"
```

`TestHasOpenRouterPricedModel`, below the replaced region, stays exactly as it is.

- [ ] **Step 2: Change the tests that pinned the old read of `modelman.toml`**

In `wt/internal/config/modelman_test.go`, delete everything from the line that starts `// TestPriceRefreshLastRun guards` up to, but not including, the line that starts `// TestModelmanPathHonorsXDG asserts` (71 lines).

In `wt/internal/config/modelman_test.go`, find:

```go
// TestModelmanStateReadsNoPerModelState pins #179 Phase B's boundary: wt
// reads only modelman.toml's legacy [litellm] table and the global
// price_refresh_last_run — no [model_state] field at all, so neither
// `ready`/`downloaded`, `running` nor the retired `exposed` flags can creep
// back into a routing or picker decision. Local presence and running state
// come from wt's live inventory. A file carrying every per-model key still
// loads.
func TestModelmanStateReadsNoPerModelState(t *testing.T) {
```

and replace it with:

```go
// TestModelmanStateReadsNoPerModelState pins #179 Phase B's boundary: wt
// reads only modelman.toml's legacy [litellm] table — no [model_state] field
// at all, so neither `ready`/`downloaded`, `running` nor the retired
// `exposed` flags can creep back into a routing or picker decision, and not
// price_refresh_last_run either (the stale-price notice reads the registry).
// Local presence and running state come from wt's live inventory. A file
// carrying every per-model key still loads.
func TestModelmanStateReadsNoPerModelState(t *testing.T) {
```

In `wt/internal/config/modelman_test.go`, find:

```go
	}
	if want := []string{"PriceRefreshLastRun", "Litellm"}; !reflect.DeepEqual(fields, want) {
		t.Fatalf("modelmanState fields = %v, want exactly %v", fields, want)
```

and replace it with:

```go
	}
	if want := []string{"Litellm"}; !reflect.DeepEqual(fields, want) {
		t.Fatalf("modelmanState fields = %v, want exactly %v", fields, want)
```

In `wt/internal/config/modelman_test.go`, find:

```go
	writeModelmanState(t, dir, `
[model_state."omlx/qwen3.8"]
```

and replace it with:

```go
	writeModelmanState(t, dir, `
price_refresh_last_run = "2026-09-14"

[model_state."omlx/qwen3.8"]
```

In `wt/internal/config/modelman_fixture_test.go`, find:

```go

// fixturePriceRefreshDate is the value of price_refresh_last_run in the
// shared modelman.toml contract fixture. Centralizing it makes the
// relationship between the fixture and the accessor tests explicit and
// avoids updating multiple literals when the fixture date changes.
const fixturePriceRefreshDate = "2026-09-14"

// TestLoadModelmanStateMatchesSharedFixture pins wt's read of the shared
// docs/contracts/modelman.sample.toml fixture. wt reads far less of it than
// modelman's Python test does: only the legacy [litellm] table and the
// top-level price_refresh_last_run are pinned here, by the same field names
// and values, and the [model_state] rows the Python side asserts on are
// merely tolerated (the file must load with them present). A drift in those
// two shared pieces would otherwise ship silently since each side's CI only
// runs its own language's tests.
func TestLoadModelmanStateMatchesSharedFixture(t *testing.T) {
```

and replace it with:

```go

// TestLoadModelmanStateMatchesSharedFixture pins wt's read of the shared
// docs/contracts/modelman.sample.toml fixture. wt reads far less of it than
// modelman's Python test does: only the legacy [litellm] table is pinned
// here, by the same field names and values. The [model_state] rows and the
// top-level price_refresh_last_run the Python side asserts on are merely
// tolerated (the file must load with them present). A drift in the shared
// table would otherwise ship silently since each side's CI only runs its own
// language's tests.
func TestLoadModelmanStateMatchesSharedFixture(t *testing.T) {
```

In `wt/internal/config/modelman_fixture_test.go`, find:

```go
	// takes local presence and running state from its live inventory, and
	// reads only the legacy [litellm] table and price_refresh_last_run
	// (pinned by TestModelmanStateReadsNoPerModelState). The file must still
	// load with them present.
```

and replace it with:

```go
	// takes local presence and running state from its live inventory, and
	// reads only the legacy [litellm] table (pinned by
	// TestModelmanStateReadsNoPerModelState). The file must still load with
	// them, and price_refresh_last_run, present.
```

In `wt/internal/config/modelman_fixture_test.go`, find:

```go
	}

	// Global price-refresh date (issue #69): wt's stale-pricing notice
	// reads this top-level key; a decode regression fails both CI jobs.
	if v, ok := PriceRefreshLastRun(); !ok || v != fixturePriceRefreshDate {
		t.Errorf("PriceRefreshLastRun() = (%q, %v), want (%q, true)", v, ok, fixturePriceRefreshDate)
	}
}
```

and replace it with:

```go
	}
}
```

The contract fixture keeps its `price_refresh_last_run` line: modelman still writes the key and its own test still reads it. wt's test now only proves a file carrying it loads.

- [ ] **Step 3: Run the tests to verify they fail**

Run (from `wt/`): `go test -count=1 ./internal/agents ./internal/config`
Expected: `internal/agents` does not build (`PriceNotice` still takes a string; `LastPriceRefresh` is undefined), and `internal/config` fails on the field list:

```text
# github.com/ohanaverse/local-ai-setup/wt/internal/agents [github.com/ohanaverse/local-ai-setup/wt/internal/agents.test]
internal/agents/price_notice_test.go:32:25: cannot use tc.last (variable of struct type "time".Time) as string value in argument to PriceNotice
internal/agents/price_notice_test.go:46:30: cannot use last (variable of struct type "time".Time) as string value in argument to PriceNotice
internal/agents/price_notice_test.go:79:16: undefined: LastPriceRefresh
internal/agents/price_notice_test.go:85:16: undefined: LastPriceRefresh
internal/agents/price_notice_test.go:94:16: undefined: LastPriceRefresh
internal/agents/price_notice_test.go:98:16: undefined: LastPriceRefresh
internal/agents/price_notice_test.go:103:16: undefined: LastPriceRefresh
internal/agents/price_notice_test.go:108:16: undefined: LastPriceRefresh
internal/agents/price_notice_test.go:111:14: undefined: LastPriceRefresh
FAIL	github.com/ohanaverse/local-ai-setup/wt/internal/agents [build failed]
--- FAIL: TestModelmanStateReadsNoPerModelState (0.00s)
```

- [ ] **Step 4: Rewrite the notice**

Replace the whole of `wt/internal/agents/price_notice.go` with:

```go
package agents

import (
	"fmt"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// priceStaleAfter is how old the newest OpenRouter price may be before the
// notice fires. Refreshing is manual (`wt cloud-sync`), so the notice is the
// only reminder; a week keeps it from nagging on every launch.
const priceStaleAfter = 7 * 24 * time.Hour

// priceStampSkew is how far ahead of the clock a stamp may be and still
// count. Two machines' clocks disagree by minutes; a stamp further ahead than
// a day is a typo (2062 for 2026), and counting it would silence the notice
// until that date came.
const priceStampSkew = 24 * time.Hour

// PrintPriceNotice prints the stale-pricing notice when the registry's
// OpenRouter prices are more than a week old or were never refreshed. It is
// the shared production emission helper used by both the TUI and non-TUI
// launch paths. Silent when cfg has no OpenRouter-priced model (#151):
// `wt cloud-sync` would have nothing to refresh.
func PrintPriceNotice(cfg *config.Config) {
	if !HasOpenRouterPricedModel(cfg) {
		return
	}
	now := time.Now()
	last, present := LastPriceRefresh(cfg, now)
	if notice := PriceNotice(last, present, now); notice != "" {
		fmt.Println(notice)
	}
}

// LastPriceRefresh is when OpenRouter prices were last refreshed, and whether
// they ever were: the newest pricing_updated_at among cfg's OpenRouter-priced
// models. Nothing stores the date. `wt cloud-sync` stamps every model it
// matches, whether or not its price moved, so the newest stamp is the last
// refresh. A model with no stamp, one that is not a time, or one more than a
// day ahead of now is skipped.
func LastPriceRefresh(cfg *config.Config, now time.Time) (time.Time, bool) {
	var newest time.Time
	found := false
	if cfg == nil {
		return newest, false
	}
	for _, m := range cfg.Models {
		if !cfg.OpenRouterPriced(m) {
			continue
		}
		at, ok := m.PricingUpdated()
		if !ok || at.Sub(now) > priceStampSkew {
			continue
		}
		if !found || at.After(newest) {
			newest, found = at, true
		}
	}
	return newest, found
}

// PriceNotice renders the post-run stale-pricing notice (issue #69), or ""
// when pricing is fresh: refreshed within the last seven days. wt only
// prints the reminder; refreshing is the user's to run (`wt cloud-sync`).
// last and present are LastPriceRefresh's answer. The date is shown in now's
// time zone.
func PriceNotice(last time.Time, present bool, now time.Time) string {
	if !present {
		return "wt: token pricing has never been refreshed — run 'wt cloud-sync'"
	}
	if now.Sub(last) <= priceStaleAfter {
		return ""
	}
	return fmt.Sprintf("wt: token pricing last refreshed %s — run 'wt cloud-sync'", last.In(now.Location()).Format("2006-01-02"))
}

// HasOpenRouterPricedModel reports whether any model in cfg takes its price
// from OpenRouter — what `wt cloud-sync`'s prices flow refreshes. It
// delegates to config.OpenRouterPriced: an openrouter model, or a model of a
// non-native cloud provider. Keyed on the provider's location, not the
// model's, so ollama cloud models (location "cloud" on the local ollama
// provider, priced by ollama.com) don't count. A model whose ProviderID has
// no registry provider doesn't count either (p == nil, location
// unresolvable). A nil cfg has no models.
func HasOpenRouterPricedModel(cfg *config.Config) bool {
	if cfg == nil {
		return false
	}
	for _, m := range cfg.Models {
		if cfg.OpenRouterPriced(m) {
			return true
		}
	}
	return false
}
```

- [ ] **Step 5: Remove `PriceRefreshLastRun`**

In `wt/internal/config/modelman.go`, find:

```go

// modelmanState mirrors the subset of ~/.config/local-ai/modelman.toml that
// wt needs read-only access to. The full file is owned by modelman.
//
// wt reads no per-model state at all (#179 Phase B): what is on disk and
// what is running come from its live inventory, never from modelman's
// `ready`/`downloaded`, `running` or retired `exposed` flags.
type modelmanState struct {
	// price_refresh_last_run is modelman's global "token pricing last
	// refreshed" date (YYYY-MM-DD), written by `modelman refresh-prices`.
	// wt reads it post-launch to print a stale-pricing notice.
	PriceRefreshLastRun string        `toml:"price_refresh_last_run"`
	Litellm             *LitellmState `toml:"litellm"`
}
```

and replace it with:

```go

// modelmanState mirrors the one table of ~/.config/local-ai/modelman.toml
// that wt still reads. The file is modelman's.
//
// wt reads no per-model state at all (#179 Phase B): what is on disk and
// what is running come from its live inventory, never from modelman's
// `ready`/`downloaded`, `running` or retired `exposed` flags. Nor does it
// read price_refresh_last_run any more: the stale-price notice takes its
// date from the registry's pricing_updated_at stamps (agents.LastPriceRefresh).
type modelmanState struct {
	Litellm *LitellmState `toml:"litellm"`
}
```

In `wt/internal/config/modelman.go`, find:

```go
}

// PriceRefreshLastRun returns modelman's global token-pricing refresh
// date (price_refresh_last_run in ~/.config/local-ai/modelman.toml) as
// (value, present). present is false when the file or key is missing or
// the file cannot be read/parsed — the notice must stay silent on errors,
// matching how loadModelmanState tolerates a missing file. wt is a
// read-only consumer; modelman owns the key.
func PriceRefreshLastRun() (string, bool) {
	path := ModelmanPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	var s modelmanState
	if err := toml.Unmarshal(data, &s); err != nil {
		return "", false
	}
	if s.PriceRefreshLastRun == "" {
		return "", false
	}
	return s.PriceRefreshLastRun, true
}
```

and replace it with:

```go
}
```

`modelman.go` still needs its `os` and `toml` imports: `loadModelmanState` uses both.

In `wt/internal/config/registry.go`, find:

```go
// the state file the way tests (or wt itself) redirect the registry. The
// subset wt reads (price_refresh_last_run and the legacy [litellm] table) is
// pinned by docs/contracts/modelman.sample.toml. wt reads this file read-only.
func ModelmanPath() string {
```

and replace it with:

```go
// the state file the way tests (or wt itself) redirect the registry. The
// one table wt reads (the legacy [litellm] table) is pinned by
// docs/contracts/modelman.sample.toml. wt reads this file read-only.
func ModelmanPath() string {
```

In `wt/internal/config/config.go`, find:

```go
// OpenRouterPriced reports whether m's price comes from OpenRouter — what
// `modelman refresh-prices` refreshes: an openrouter model, or a model of a
// non-native cloud provider. Keyed on the provider's location, not the
```

and replace it with:

```go
// OpenRouterPriced reports whether m's price comes from OpenRouter — what
// `wt cloud-sync`'s prices flow refreshes: an openrouter model, or a model of a
// non-native cloud provider. Keyed on the provider's location, not the
```

Confirm nothing else read it. From `wt/`: `grep -rn "PriceRefreshLastRun" . --include='*.go'` prints nothing.

- [ ] **Step 6: Run the tests to verify they pass**

Run (from `wt/`): `go test -count=1 ./internal/agents ./internal/config`
Expected:

```text
ok  	github.com/ohanaverse/local-ai-setup/wt/internal/agents	(time)
ok  	github.com/ohanaverse/local-ai-setup/wt/internal/config	(time)
```

Then `go build ./... && go vet ./...` (both silent), and `go test -count=1 ./cmd/wt ./internal/tui` (two `ok` lines): the launch paths call `PrintPriceNotice`.

- [ ] **Step 7: Keep the docs true**

In `wt/docs/internals/launch-flow.md`, find:

```text
- **Stale-pricing notice**: printed when modelman's top-level `price_refresh_last_run` in `modelman.toml` isn't today (absent or malformed → "never been refreshed" wording). wt only notifies; modelman owns the refresh. Parse errors stay silent. Skipped for command agents (`m.ID == ""`) and when the catalog has no OpenRouter-priced model (`agents.HasOpenRouterPricedModel`, #151). That function mirrors modelman's `_is_openrouter_priced` (an openrouter model, or one whose provider is a non-native cloud provider; ollama cloud models don't count), so change both together.
```

and replace it with:

```text
- **Stale-pricing notice**: printed when the registry's OpenRouter prices are more than 7 days old, or were never refreshed (`wt: token pricing last refreshed <date> — run 'wt cloud-sync'` / `… has never been refreshed …`). Nothing stores the date: `agents.LastPriceRefresh` takes the newest `pricing_updated_at` among the OpenRouter-priced models (`Model.PricingUpdated`), and `wt cloud-sync`'s prices flow stamps every model it matches, changed or not, so the newest stamp is the last refresh. A missing or mistyped stamp is skipped, never an error, and so is one more than a day ahead of the clock (a typo such as 2062, which would otherwise silence the notice until then). wt never refreshes on its own (a refresh is a network call, a registry write and possibly a proxy restart); the notice is the only reminder. Skipped for command agents (`m.ID == ""`) and when the catalog has no OpenRouter-priced model (`agents.HasOpenRouterPricedModel`, #151: an openrouter model, or one whose provider is a non-native cloud provider; ollama cloud models don't count). `internal/cloudsync.OpenRouterPriced` is the same rule over registry rows; `docs/contracts/catalog-predicates` pins both, and modelman's `_is_openrouter_priced` until it is deleted.
```

In `docs/guides/00-config-map.md`, find:

```text
`modelman` (writes), `wt` (read-only: the `price_refresh_last_run` date, and `[litellm]` only as a legacy fallback — no per-model key)
```

and replace it with:

```text
`modelman` (writes), `wt` (read-only: `[litellm]` only, as a legacy fallback — no per-model key, and not `price_refresh_last_run`)
```

In `docs/guides/00-config-map.md`, find:

```text
plus `wt` (read-only — it reads the global `price_refresh_last_run` date, and the legacy `[litellm]` table as a fallback for routing state wt has not migrated; it reads **no** `[model_state]` key,
```

and replace it with:

```text
plus `wt` (read-only — it reads the legacy `[litellm]` table as a fallback for routing state wt has not migrated, and nothing else: not `price_refresh_last_run` (wt's stale-pricing notice takes its date from the `pricing_updated_at` stamps in `registry.toml`), and **no** `[model_state]` key,
```

In `docs/contracts/modelman.sample.toml`, find:

```text
# read-only. Since #179 Phase B wt reads only two things here: the global
# `price_refresh_last_run` date and the `[litellm]` table below (a legacy
# fallback — the routing table is wt-owned in wt's config.toml). wt reads
```

and replace it with:

```text
# read-only. wt reads only one thing here: the `[litellm]` table below (a
# legacy fallback — the routing table is wt-owned in wt's config.toml). It
# stopped reading `price_refresh_last_run` when `wt cloud-sync` shipped: the
# stale-pricing notice takes its date from registry.toml. wt reads
```

In `docs/contracts/modelman.sample.toml`, find:

```text
# Global "token pricing last refreshed" date (YYYY-MM-DD), written by
# `modelman refresh-prices` (set_price_refresh_last_run in state.py).
# wt reads it after each launch to print a stale-pricing notice
# (see wt/internal/agents/price_notice.go). A missing key means
# pricing has never been refreshed.
```

and replace it with:

```text
# Global "token pricing last refreshed" date (YYYY-MM-DD), written by
# `modelman refresh-prices` (set_price_refresh_last_run in state.py).
# modelman's own; wt no longer reads it (the Go test only checks that a
# file carrying it still loads).
```

In `wt/CHANGELOG.md`, find:

```text
### Changed

- omlx is handled as the multi-model pool it is (#213).
```

and replace it with:

```text
### Changed

- The stale-pricing notice wt prints after a launch takes its date from
  `registry.toml`: the newest `pricing_updated_at` among the models priced by
  OpenRouter. It now speaks when that is more than 7 days old, or when no
  such model was ever refreshed, and it names `wt cloud-sync`; it used to
  speak whenever `modelman.toml`'s `price_refresh_last_run` was not today,
  and name `modelman refresh-prices`. wt no longer reads that key. Either
  tool's refresh still clears the notice, since both stamp the models.
- omlx is handled as the multi-model pool it is (#213).
```

In `CLAUDE.md`, find:

```text
from `modelman.toml` it reads only the global `price_refresh_last_run` date plus the `[litellm]` table as a legacy fallback — no per-model key is read at all
```

and replace it with:

```text
from `modelman.toml` it reads only the `[litellm]` table, as a legacy fallback — no per-model key is read at all, and not `price_refresh_last_run` (the stale-pricing notice takes its date from `registry.toml`'s `pricing_updated_at` stamps)
```

- [ ] **Step 8: Commit**

From the repo root:

```bash
git add wt/internal/agents wt/internal/config wt/docs/internals/launch-flow.md wt/CHANGELOG.md docs/guides/00-config-map.md docs/contracts/modelman.sample.toml CLAUDE.md
git commit -m "feat(wt): the stale-price notice reads registry stamps, fires after 7 days and names wt cloud-sync"
```

### Task 8: An error that carries the exit status

**Files:**
- Create: `wt/cmd/wt/exitcode_test.go`, `wt/cmd/wt/exitcode.go`
- Modify: `wt/cmd/wt/main.go:100-103`

**Interfaces:**
- Consumes: `main()` (`cmd/wt/main.go:91`), which today prints `wt: <err>` and calls `os.Exit(1)` after `lifecycle.WaitPendingRoutes()`.
- Produces:
  - `type exitCodeError struct { code int; err error }` with `Error()` and `Unwrap()`
  - `func exitCodeOf(err error) int`: the carried code when it is 1 to 255, else 1

- [ ] **Step 1: Write the failing test**

Create `wt/cmd/wt/exitcode_test.go`:

```go
package main

import (
	"errors"
	"fmt"
	"testing"
)

// TestExitCodeOf pins the mapping main uses: the code a cloud-sync error
// carries, however deeply wrapped, and 1 for every other error — so no other
// command's exit status moved when cloud-sync got its own codes, and a
// carried 0 can never turn a failure into a success.
func TestExitCodeOf(t *testing.T) {
	coded := &exitCodeError{code: 4, err: errors.New("refused")}
	cases := []struct {
		err  error
		want int
	}{
		{errors.New("plain"), 1},
		{coded, 4},
		{fmt.Errorf("wrapped: %w", coded), 4},
		{&exitCodeError{code: 0, err: errors.New("zero")}, 1},
		{&exitCodeError{code: 300, err: errors.New("too big")}, 1},
	}
	for _, tc := range cases {
		if got := exitCodeOf(tc.err); got != tc.want {
			t.Errorf("exitCodeOf(%v) = %d, want %d", tc.err, got, tc.want)
		}
	}
	if coded.Error() != "refused" || !errors.Is(fmt.Errorf("x: %w", coded), coded) {
		t.Error("an exitCodeError must print and unwrap as the error it carries")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run (from `wt/`): `go test -count=1 ./cmd/wt -run TestExitCodeOf`
Expected:

```text
# github.com/ohanaverse/local-ai-setup/wt/cmd/wt [github.com/ohanaverse/local-ai-setup/wt/cmd/wt.test]
cmd/wt/exitcode_test.go:14:12: undefined: exitCodeError
cmd/wt/exitcode_test.go:22:5: undefined: exitCodeError
cmd/wt/exitcode_test.go:23:5: undefined: exitCodeError
cmd/wt/exitcode_test.go:26:13: undefined: exitCodeOf
```

- [ ] **Step 3: Write the error type**

Create `wt/cmd/wt/exitcode.go`:

```go
package main

import "errors"

// exitCodeError is an error that says which status the process exits with.
// Every command exits 1 on an error; `wt cloud-sync` is the one whose codes
// mean something to a caller (2 to 5 say why the catalog flow changed
// nothing), so it wraps its error in this and main reads the code back with
// exitCodeOf. The message is what main prints after "wt:".
type exitCodeError struct {
	code int
	err  error
}

func (e *exitCodeError) Error() string { return e.err.Error() }
func (e *exitCodeError) Unwrap() error { return e.err }

// exitCodeOf is the status main exits with for err: the code an
// exitCodeError carries, anywhere in the chain, and 1 for every other error.
// A code outside 1 to 255 is 1: 0 would report a failure as success.
func exitCodeOf(err error) int {
	var coded *exitCodeError
	if errors.As(err, &coded) && coded.code >= 1 && coded.code <= 255 {
		return coded.code
	}
	return 1
}
```

- [ ] **Step 4: Have `main` exit with it**

In `wt/cmd/wt/main.go`, find:

```go
		fmt.Fprintln(os.Stderr, "wt:", err)
		os.Exit(1)
	}
```

and replace it with:

```go
		fmt.Fprintln(os.Stderr, "wt:", err)
		// 1 for every command but one: `wt cloud-sync` says with 2 to 5 why
		// its catalog flow changed nothing (exitCodeError).
		os.Exit(exitCodeOf(err))
	}
```

The exit still comes after `lifecycle.WaitPendingRoutes()`: a route sync's proxy restart must not be killed by the exit. The two other `os.Exit(1)` calls in `main.go` (`:189`, a failed `newApp`; `:252`, `--check-guard`) are not errors returned by a command and stay as they are.

- [ ] **Step 5: Run the test to verify it passes**

Run (from `wt/`): `go test -count=1 ./cmd/wt -run TestExitCodeOf`
Expected:

```text
ok  	github.com/ohanaverse/local-ai-setup/wt/cmd/wt	(time)
```

- [ ] **Step 6: Commit**

From the repo root:

```bash
git add wt/cmd/wt/exitcode.go wt/cmd/wt/exitcode_test.go wt/cmd/wt/main.go
git commit -m "feat(wt): exitCodeError, so one command can exit with a status other than 1"
```

### Task 9: `wt cloud-sync` with the prices flow

**Files:**
- Create: `wt/cmd/wt/cloudsync_test.go`, `wt/cmd/wt/cloudsync.go`
- Modify: `wt/cmd/wt/testmain_test.go`, `wt/cmd/wt/main.go`, `wt/CLAUDE.md`, `wt/CHANGELOG.md`, `wt/docs/internals/testing.md`, `CLAUDE.md`, `docs/guides/00-config-map.md`

**Interfaces:**
- Consumes: PR 1 (`cloudsync.Entries`, `Providers`, `OpenRouterPriced`, `OpenRouterModelsURL`, `ParseOpenRouter`, `PlanPrices`, `(*PricePlan).HasWork/Format/Apply`, `PricesApplied`); Task 6 (`config.ReadRegistryDoc`); Task 8 (`exitCodeError`, `exitCodeOf`); `config.UpdateRegistry`, `config.RegistryFixHint(err error) string` (`config.go:969`); `syncRoutesAfterWrite func(out, errOut io.Writer) string` (`cmd/wt/model.go:25`: `""` when the routes are in step, else a warning; it returns `litellm.ErrRegistryRedirected`'s text when the registry is redirected and nothing names `config.yaml`); `askYesNo(r io.Reader) (bool, error)` (`cmd/wt/start.go:152`); `var openTTY = func() (*os.File, error)` (`cmd/wt/launch.go:77`), the seam the controlling terminal is opened through; `configError(err error) error` (`cmd/wt/helpers.go:110`); `app.cfg`, `app.loadErr` (`cmd/wt/app.go`); test helpers `withCleanConfigEnv` (`helpers_test.go:53`), `stubRouteSync`, `realRouteSync` (`model_test.go:28`, `:44`), `probeInventory`, `localmodels.OnDiskSnapshotForTest`.
- Produces:
  - seams `cloudFetch func(ctx context.Context, url string) ([]byte, error)`, `confirmCloudSync func(question string) (bool, error)`, `cloudSyncNow func() time.Time`
  - `var cloudSyncFlows []string` (`{"prices"}` in this PR)
  - `type cloudSyncOpts struct { prices bool; dryRun, yes bool }` (Task 11 adds `catalog`, `force`, `htmlFile`, `approve`)
  - `func selectFlows(only string, o *cloudSyncOpts) error`
  - `func cloudSyncCmd(a *app) *cobra.Command`
  - `type cloudSyncOutcome struct { failed bool }` with `err() error` (Task 11 adds `catalogCode`)
  - `func prefixLines(w io.Writer, flow, text string)`
  - `type pricesRun struct { api map[string]cloudsync.APIPrice; plan *cloudsync.PricePlan; printed string }`
  - `func planPricesFlow(ctx, out, errOut, entries, providers, res *cloudSyncOutcome) *pricesRun`
  - `func runCloudSync(ctx context.Context, out, errOut io.Writer, cfg *config.Config, o cloudSyncOpts) error`
  - `func withRegistryHint(err error) error`
  - test helpers for Task 11: `cloudSyncRegistry`, `cloudSyncStamp`, `cloudSyncClock`, `openRouterBody`, `cloudSyncHome(t, registry) (path string, cfg *config.Config)`, `stubCloudFetch(t, pages) *fetched` (with `.all()`), `stubConfirm(t, answer, err, before) *int`, `runCS(t, cfg, opts) (stdout, stderr string, code int)`, `runCSFinal(t, cfg, opts) (stdout, stderr, final string, code int)` (`final` is the command's error text), `noTerminal(t)` (the real prompt with `openTTY` failing), `mustRead(t, path) string`

Output contract of this task, exactly: plan lines on stdout as `prices: <line>`; the result as `prices: refreshed N model(s); M price(s) changed`; errors on stderr as `prices: error: …`; the sync's lines as `routes: <line>` (what it wrote to stdout on stdout, what it wrote to stderr on stderr) and its warning as `routes: warning: …` on stderr.

- [ ] **Step 1: Make the new seams fail closed in `TestMain`**

In `wt/cmd/wt/testmain_test.go`, find:

```go
	}
	code := m.Run()
```

and replace it with:

```go
	}
	// `wt cloud-sync` seams: no test may fetch a public page or open the
	// terminal to ask. Tests use stubCloudFetch and stubConfirm
	// (cloudsync_test.go).
	cloudFetch = func(_ context.Context, url string) ([]byte, error) {
		return nil, errors.New("cloudFetch not stubbed in this test: " + url)
	}
	confirmCloudSync = func(string) (bool, error) {
		return false, errors.New("confirmCloudSync not stubbed in this test")
	}
	code := m.Run()
```

(`testmain_test.go` already imports `context`, `errors` and `io`.)

- [ ] **Step 2: Write the failing tests**

Create `wt/cmd/wt/cloudsync_test.go`:

```go
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/cloudsync"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// cloudSyncStamp is what a run at cloudSyncClock writes in pricing_updated_at.
const cloudSyncStamp = "2026-10-07T16:00:00+00:00"

var cloudSyncClock = time.Date(2026, 10, 7, 9, 0, 0, 0, time.FixedZone("PDT", -7*3600))

// cloudSyncRegistry is the registry the command tests start from: one
// OpenRouter-priced model, two ollama cloud entries (one the test page lists,
// one it does not) and a local ollama model that nothing may touch.
const cloudSyncRegistry = `[[providers]]
id = "ollama"
name = "Ollama"
location = "local"

[providers.auth]
type = "none"
base_url = "http://127.0.0.1:11434"

[[providers]]
id = "openrouter"
name = "OpenRouter"
location = "cloud"

[providers.auth]
type = "api_key"
secret_ref = "WT_TEST_OPENROUTER_KEY"

[[models]]
id = "openrouter/vendor--gpt"
family = "gpt"
provider_id = "openrouter"
model_name = "vendor/gpt"
location = "cloud"
source = "curated"
tags = []

[models.cost]
input_price_per_million = 2.5
output_price_per_million = 10

[[models]]
id = "ollama/deepseek-v4-pro:cloud"
family = "deepseek"
provider_id = "ollama"
model_name = "deepseek-v4-pro:cloud"
location = "cloud"
source = "curated"
tags = []

[models.cost]
input_price_per_million = 9
subscription_price = 100
subscription_period = "month"

[[models]]
id = "ollama/retired:cloud"
family = "retired"
provider_id = "ollama"
model_name = "retired:cloud"
location = "cloud"
source = "curated"
tags = []

[[models]]
id = "ollama/qwen3:8b"
family = "qwen3"
provider_id = "ollama"
model_name = "qwen3:8b"
location = "local"
source = "curated"
tags = []
`

const openRouterBody = `{"data": [{"id": "vendor/gpt", "pricing": {"prompt": "0.000003", "completion": "0.000015"}}]}`

// sameOpenRouterBody prices vendor/gpt exactly as cloudSyncRegistry has it.
const sameOpenRouterBody = `{"data": [{"id": "vendor/gpt", "pricing": {"prompt": "0.0000025", "completion": "0.00001"}}]}`

// cloudSyncHome gives the test its own home with registry as its registry,
// and returns the registry path and the loaded config.
func cloudSyncHome(t *testing.T, registry string) (string, *config.Config) {
	t.Helper()
	home := t.TempDir()
	withCleanConfigEnv(t, home)
	// Where a refused pricing page is saved.
	t.Setenv("TMPDIR", t.TempDir())
	path := filepath.Join(home, ".config", "local-ai", "registry.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(registry), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	old := cloudSyncNow
	cloudSyncNow = func() time.Time { return cloudSyncClock }
	t.Cleanup(func() { cloudSyncNow = old })
	return path, cfg
}

// fetched records the URLs a test's cloudFetch stub was asked for.
type fetched struct {
	mu   sync.Mutex
	urls []string
}

func (f *fetched) all() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.urls)
}

// stubCloudFetch serves pages by URL. A URL with no page is an HTTP 404.
func stubCloudFetch(t *testing.T, pages map[string]string) *fetched {
	t.Helper()
	rec := &fetched{}
	old := cloudFetch
	cloudFetch = func(_ context.Context, url string) ([]byte, error) {
		rec.mu.Lock()
		rec.urls = append(rec.urls, url)
		rec.mu.Unlock()
		page, ok := pages[url]
		if !ok {
			return nil, errors.New("HTTP 404")
		}
		return []byte(page), nil
	}
	t.Cleanup(func() { cloudFetch = old })
	return rec
}

// stubConfirm answers the confirmation and counts how often it was asked.
// before, when not nil, runs first: it plays whatever happens to the machine
// while the user reads the plan.
func stubConfirm(t *testing.T, answer bool, err error, before func()) *int {
	t.Helper()
	asked := 0
	old := confirmCloudSync
	confirmCloudSync = func(string) (bool, error) {
		asked++
		if before != nil {
			before()
		}
		return answer, err
	}
	t.Cleanup(func() { confirmCloudSync = old })
	return &asked
}

func runCS(t *testing.T, cfg *config.Config, o cloudSyncOpts) (stdout, stderr string, code int) {
	t.Helper()
	stdout, stderr, _, code = runCSFinal(t, cfg, o)
	return stdout, stderr, code
}

// runCSFinal is runCS with the command's error text as well: the one line
// main prints, after "wt: ", when the run did not exit 0.
func runCSFinal(t *testing.T, cfg *config.Config, o cloudSyncOpts) (stdout, stderr, final string, code int) {
	t.Helper()
	var out, errOut bytes.Buffer
	if err := runCloudSync(context.Background(), &out, &errOut, cfg, o); err != nil {
		final, code = err.Error(), exitCodeOf(err)
	}
	return out.String(), errOut.String(), final, code
}

// noTerminal takes the controlling terminal away, as under an agent's shell
// or cron, and puts the real prompt behind confirmCloudSync (TestMain fails
// that seam by default).
func noTerminal(t *testing.T) {
	t.Helper()
	oldTTY, oldConfirm := openTTY, confirmCloudSync
	openTTY = func() (*os.File, error) { return nil, errors.New("open /dev/tty: device not configured") }
	confirmCloudSync = promptCloudSync
	t.Cleanup(func() { openTTY, confirmCloudSync = oldTTY, oldConfirm })
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestCloudSyncSeamsFailClosed pins TestMain: with nothing stubbed, a
// cloud-sync test reaches neither the network nor a terminal. A test that
// forgot a stub fails with "not stubbed" instead of fetching a public page
// from whatever machine it runs on.
func TestCloudSyncSeamsFailClosed(t *testing.T) {
	if _, err := cloudFetch(context.Background(), cloudsync.OpenRouterModelsURL); err == nil || !strings.Contains(err.Error(), "not stubbed") {
		t.Errorf("cloudFetch err = %v, want a not-stubbed failure", err)
	}
	if ok, err := confirmCloudSync("?"); ok || err == nil {
		t.Errorf("confirmCloudSync = (%v, %v), want a refusal with an error", ok, err)
	}
}

// TestRealCloudFetch pins the one HTTP helper against a loopback server: a
// 2xx body is returned whole, and any other status is an error that names
// it. An error page read as a pricing page would be reported as "the page
// changed shape" and send someone to repair a parser that is not broken.
func TestRealCloudFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ok" {
			fmt.Fprint(w, "<html>page</html>")
			return
		}
		http.Error(w, "slow down", http.StatusTooManyRequests)
	}))
	defer srv.Close()
	if body, err := realCloudFetch(context.Background(), srv.URL+"/ok"); err != nil || string(body) != "<html>page</html>" {
		t.Errorf("realCloudFetch(/ok) = (%q, %v)", body, err)
	}
	if _, err := realCloudFetch(context.Background(), srv.URL+"/limited"); err == nil || err.Error() != "HTTP 429" {
		t.Errorf("realCloudFetch(/limited) err = %v, want HTTP 429", err)
	}
}

// TestSelectFlows pins --only: nothing selects every flow, a comma list
// selects those named, and an unknown name is an error that lists the valid
// ones instead of silently running nothing.
func TestSelectFlows(t *testing.T) {
	cases := []struct {
		only    string
		prices  bool
		wantErr string
	}{
		{"", true, ""},
		{"prices", true, ""},
		{" prices ", true, ""},
		{"price", false, `--only: unknown flow "price" (valid: prices)`},
		{"prices,", true, `--only: unknown flow "" (valid: prices)`},
	}
	for _, tc := range cases {
		var o cloudSyncOpts
		err := selectFlows(tc.only, &o)
		if tc.wantErr != "" {
			if err == nil || err.Error() != tc.wantErr {
				t.Errorf("selectFlows(%q) err = %v, want %q", tc.only, err, tc.wantErr)
			}
			continue
		}
		if err != nil || o.prices != tc.prices {
			t.Errorf("selectFlows(%q) = prices %v, err %v", tc.only, o.prices, err)
		}
	}
}

// TestCloudSyncCommandRefusals pins what the command refuses before it does
// anything: an unknown flow, a stray argument, and a config that could not
// be loaded, which gets the same repair hint every other command gives.
func TestCloudSyncCommandRefusals(t *testing.T) {
	_, cfg := cloudSyncHome(t, cloudSyncRegistry)
	run := func(a *app, args ...string) error {
		cmd := cloudSyncCmd(a)
		cmd.SetArgs(args)
		cmd.SetOut(new(bytes.Buffer))
		cmd.SetErr(new(bytes.Buffer))
		return cmd.Execute()
	}
	ok := &app{cfg: cfg}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--only", "routes"}, `--only: unknown flow "routes" (valid: prices)`},
		{[]string{"extra"}, `unknown command "extra" for "cloud-sync"`},
	} {
		err := run(ok, tc.args...)
		if err == nil || err.Error() != tc.want || exitCodeOf(err) != 1 {
			t.Errorf("wt cloud-sync %v: err = %v (exit %d), want %q and exit 1", tc.args, err, exitCodeOf(err), tc.want)
		}
	}

	broken := &app{cfg: &config.Config{}, loadErr: fmt.Errorf("%w at /x/registry.toml — seed it with `wt model init`", config.ErrRegistryMissing)}
	err := run(broken, "--dry-run")
	if err == nil || !strings.HasPrefix(err.Error(), "config error: model registry not found") || strings.Contains(err.Error(), "wt config") {
		t.Errorf("with an unloadable config: err = %v, want the config error with no second repair", err)
	}
}

// TestCloudSyncPricesDryRun pins the dry run of the prices flow: the plan as
// `id: old -> new` under the prices: prefix, and nothing else — the registry
// byte-identical, no lock file beside it, no question asked, no route sync.
func TestCloudSyncPricesDryRun(t *testing.T) {
	path, cfg := cloudSyncHome(t, cloudSyncRegistry)
	got := stubCloudFetch(t, map[string]string{cloudsync.OpenRouterModelsURL: openRouterBody})
	asked := stubConfirm(t, true, nil, nil)
	synced := stubRouteSync(t, "")

	stdout, stderr, code := runCS(t, cfg, cloudSyncOpts{prices: true, dryRun: true})
	want := "prices: openrouter.ai: 1 OpenRouter-priced models in the registry (prices are input/cached/output per million tokens)\n" +
		"prices: Price updates (1):\n" +
		"prices:   openrouter/vendor--gpt: 2.5/-/10 -> 3/-/15\n" +
		"prices: Unchanged prices: 0\n"
	if stdout != want || stderr != "" || code != 0 {
		t.Errorf("stdout = %q\nstderr = %q, exit %d\nwant stdout %q, no stderr, exit 0", stdout, stderr, code, want)
	}
	if mustRead(t, path) != cloudSyncRegistry {
		t.Error("a dry run changed the registry")
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Errorf("a dry run left %d files beside the registry, want only registry.toml", len(entries))
	}
	if *asked != 0 || *synced != 0 {
		t.Errorf("a dry run asked %d time(s) and synced %d time(s), want neither", *asked, *synced)
	}
	if urls := got.all(); !reflect.DeepEqual(urls, []string{cloudsync.OpenRouterModelsURL}) {
		t.Errorf("fetched %v, want only OpenRouter's model list", urls)
	}
}

// TestCloudSyncPricesApply pins the prices flow end to end with --yes: the
// new prices and the stamp are in registry.toml, the result line says how
// many moved, the routes are synced exactly once (LiteLLM gets a route's
// prices only through a sync, #179), and nothing was asked.
func TestCloudSyncPricesApply(t *testing.T) {
	path, cfg := cloudSyncHome(t, cloudSyncRegistry)
	stubCloudFetch(t, map[string]string{cloudsync.OpenRouterModelsURL: openRouterBody})
	asked := stubConfirm(t, false, nil, nil)
	synced := stubRouteSync(t, "")

	stdout, stderr, code := runCS(t, cfg, cloudSyncOpts{prices: true, yes: true})
	if code != 0 || stderr != "" || !strings.HasSuffix(stdout, "prices: refreshed 1 model(s); 1 price(s) changed\n") {
		t.Fatalf("stdout = %q\nstderr = %q, exit %d", stdout, stderr, code)
	}
	if *asked != 0 || *synced != 1 {
		t.Errorf("asked %d time(s), synced %d time(s); want no question under --yes and one sync", *asked, *synced)
	}
	wantRow := "model_name = \"vendor/gpt\"\nlocation = \"cloud\"\nsource = \"curated\"\ntags = []\npricing_updated_at = \"" + cloudSyncStamp + "\"\n\n" +
		"[models.cost]\ninput_price_per_million = 3.0\noutput_price_per_million = 15.0\n"
	if text := mustRead(t, path); !strings.Contains(text, wantRow) {
		t.Errorf("registry.toml lacks the refreshed row:\n%s\n\nfile:\n%s", wantRow, text)
	}
}

// TestCloudSyncStampOnlyRunSkipsTheRouteSync pins the run that finds every
// price current. The matched model is still stamped (that stamp is the
// stale-price notice's "last refreshed"), the unchanged price is not
// rewritten, and the routes are not synced: a sync can restart the LiteLLM
// proxy under running agents, and no route changed.
func TestCloudSyncStampOnlyRunSkipsTheRouteSync(t *testing.T) {
	path, cfg := cloudSyncHome(t, cloudSyncRegistry)
	stubCloudFetch(t, map[string]string{cloudsync.OpenRouterModelsURL: sameOpenRouterBody})
	synced := stubRouteSync(t, "")

	stdout, stderr, code := runCS(t, cfg, cloudSyncOpts{prices: true, yes: true})
	if code != 0 || stderr != "" || !strings.HasSuffix(stdout, "prices: Unchanged prices: 1\nprices: refreshed 1 model(s); 0 price(s) changed\n") {
		t.Fatalf("stdout = %q\nstderr = %q, exit %d", stdout, stderr, code)
	}
	if *synced != 0 {
		t.Errorf("a stamp-only run synced the routes %d time(s)", *synced)
	}
	want := strings.Replace(cloudSyncRegistry, "tags = []\n\n[models.cost]\ninput_price_per_million = 2.5\noutput_price_per_million = 10\n",
		"tags = []\npricing_updated_at = \""+cloudSyncStamp+"\"\n\n[models.cost]\ninput_price_per_million = 2.5\noutput_price_per_million = 10\n", 1)
	if got := mustRead(t, path); got != want {
		t.Errorf("registry.toml after a stamp-only run:\n%s\nwant the stamp added and nothing else:\n%s", got, want)
	}
	// The stamp is what the launch notice reads.
	loaded, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if at, ok := agents.LastPriceRefresh(loaded, cloudSyncClock); !ok || !at.Equal(cloudSyncClock) {
		t.Errorf("LastPriceRefresh after the run = (%v, %v), want %v", at, ok, cloudSyncClock)
	}
}

// TestCloudSyncPricesFailuresExit1 pins the prices flow's failures: a fetch
// that fails, and an answer that is not the model list. Each is one error
// line under the prices: prefix, exit 1, and the registry untouched — never
// a run of "no OpenRouter match" warnings for every model. The command's own
// error, the last line main prints, points back at those lines.
func TestCloudSyncPricesFailuresExit1(t *testing.T) {
	for name, pages := range map[string]map[string]string{
		"could not read OpenRouter's prices: HTTP 404":                                {},
		"could not read OpenRouter's prices: OpenRouter response missing 'data' list": {cloudsync.OpenRouterModelsURL: `{"error": "rate limited"}`},
	} {
		path, cfg := cloudSyncHome(t, cloudSyncRegistry)
		stubCloudFetch(t, pages)
		synced := stubRouteSync(t, "")
		stdout, stderr, final, code := runCSFinal(t, cfg, cloudSyncOpts{prices: true, yes: true})
		if want := "prices: error: " + name + "; no price was changed\n"; stderr != want || stdout != "" || code != 1 {
			t.Errorf("stdout = %q, stderr = %q, exit %d\nwant stderr %q and exit 1", stdout, stderr, code, want)
		}
		if want := "cloud-sync: a step failed; see the error lines above"; final != want {
			t.Errorf("the command's error = %q, want %q", final, want)
		}
		if mustRead(t, path) != cloudSyncRegistry || *synced != 0 {
			t.Error("a failed fetch changed the registry or synced the routes")
		}
	}
}

// TestCloudSyncWithNoOpenRouterModelFetchesNothing pins the registry that
// has only ollama models: the prices flow says there is nothing to refresh
// and makes no request (#151: nothing to fetch, nothing to warn about).
func TestCloudSyncWithNoOpenRouterModelFetchesNothing(t *testing.T) {
	registry := cloudSyncRegistry[:strings.Index(cloudSyncRegistry, "[[models]]\nid = \"openrouter/vendor--gpt\"")] +
		cloudSyncRegistry[strings.Index(cloudSyncRegistry, "[[models]]\nid = \"ollama/deepseek-v4-pro:cloud\""):]
	path, cfg := cloudSyncHome(t, registry)
	got := stubCloudFetch(t, nil)
	stdout, stderr, code := runCS(t, cfg, cloudSyncOpts{prices: true, yes: true})
	if stdout != "prices: no OpenRouter-priced model in the registry; nothing to refresh\n" || stderr != "" || code != 0 {
		t.Errorf("stdout = %q, stderr = %q, exit %d", stdout, stderr, code)
	}
	if len(got.all()) != 0 || mustRead(t, path) != registry {
		t.Errorf("fetched %v or changed the registry; want neither", got.all())
	}
}

// TestCloudSyncAsksOnceAndTakesNoForAnAnswer pins the confirmation without
// --yes: one question, a "no" leaves everything as it was and exits 0, and
// when the question cannot be asked (no terminal, as under an agent's shell)
// the run changes nothing, exits 1 and names --yes. The no-terminal half
// runs the real prompt with the terminal taken away, so the message is the
// one a user gets, not one the test supplied.
func TestCloudSyncAsksOnceAndTakesNoForAnAnswer(t *testing.T) {
	path, cfg := cloudSyncHome(t, cloudSyncRegistry)
	stubCloudFetch(t, map[string]string{cloudsync.OpenRouterModelsURL: openRouterBody})
	synced := stubRouteSync(t, "")

	asked := stubConfirm(t, false, nil, nil)
	stdout, stderr, code := runCS(t, cfg, cloudSyncOpts{prices: true})
	if *asked != 1 || code != 0 || stderr != "" || !strings.HasSuffix(stdout, "prices: not applied (declined)\n") {
		t.Errorf("declined: asked %d, exit %d, stdout %q, stderr %q", *asked, code, stdout, stderr)
	}

	noTerminal(t)
	_, stderr, code = runCS(t, cfg, cloudSyncOpts{prices: true})
	if want := "prices: error: not applied: there is no terminal to confirm on — rerun with --yes to apply without asking\n"; stderr != want || code != 1 {
		t.Errorf("no terminal: stderr = %q, exit %d; want %q and exit 1", stderr, code, want)
	}
	if mustRead(t, path) != cloudSyncRegistry || *synced != 0 {
		t.Error("an unconfirmed run changed the registry or synced the routes")
	}

	// A plan with nothing in it is not asked about. A fetch that matched no
	// model refreshed nothing, so it writes nothing: a stamp here would tell
	// the stale-price notice that prices are fresh when none was read.
	stubCloudFetch(t, map[string]string{cloudsync.OpenRouterModelsURL: `{"data": []}`})
	asked = stubConfirm(t, true, nil, nil)
	stdout, _, code = runCS(t, cfg, cloudSyncOpts{prices: true})
	if *asked != 0 || code != 0 || !strings.Contains(stdout, "prices: warning: No OpenRouter match for openrouter/vendor--gpt (vendor/gpt)\n") {
		t.Errorf("a plan that matches no model: asked %d time(s), exit %d; want no question, exit 0 and the no-match warning\n%s", *asked, code, stdout)
	}
	if mustRead(t, path) != cloudSyncRegistry || *synced != 0 {
		t.Error("a run that matched no model stamped the registry or synced the routes")
	}
}

// TestPromptCloudSyncAsksOnTheTerminalAndDefaultsToNo runs the real prompt
// against a stand-in terminal (one end of a socket pair). It pins the
// question as it is shown, with [y/N], and that only y or yes applies: a
// bare Enter, any other word, and a terminal that closes without an answer
// are all "no". A sync that deletes models must never be approved by
// accident.
func TestPromptCloudSyncAsksOnTheTerminalAndDefaultsToNo(t *testing.T) {
	for _, tc := range []struct {
		typed string
		want  bool
	}{
		{"y\n", true}, {"YES\n", true}, {"n\n", false}, {"\n", false}, {"sure\n", false}, {"", false},
	} {
		fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
		if err != nil {
			t.Fatal(err)
		}
		tty, user := os.NewFile(uintptr(fds[0]), "tty"), os.NewFile(uintptr(fds[1]), "user")
		old := openTTY
		openTTY = func() (*os.File, error) { return tty, nil }
		shown := make(chan string, 1)
		go func() {
			// The user reads the question, types the answer and leaves.
			buf := make([]byte, 256)
			n, _ := user.Read(buf)
			_, _ = user.WriteString(tc.typed)
			_ = user.Close()
			shown <- string(buf[:n])
		}()
		got, err := promptCloudSync("Apply these changes?")
		openTTY = old
		if err != nil || got != tc.want {
			t.Errorf("typed %q: promptCloudSync = (%v, %v), want (%v, nil)", tc.typed, got, err, tc.want)
		}
		if q := <-shown; q != "Apply these changes? [y/N] " {
			t.Errorf("the question shown = %q", q)
		}
	}
}

// TestCloudSyncPrefixesTheRouteSyncsLines pins how the route sync's own
// output reaches the user: its stdout lines and its stderr lines (a model
// that could not be routed) under routes:, like its warning. An unprefixed
// "<id>: …" between prices: and catalog: lines reads as a third flow, or as
// part of the one above it.
func TestCloudSyncPrefixesTheRouteSyncsLines(t *testing.T) {
	_, cfg := cloudSyncHome(t, cloudSyncRegistry)
	stubCloudFetch(t, map[string]string{cloudsync.OpenRouterModelsURL: openRouterBody})
	old := syncRoutesAfterWrite
	syncRoutesAfterWrite = func(out, errOut io.Writer) string {
		fmt.Fprintln(out, "openrouter/vendor--gpt: routed")
		fmt.Fprintln(errOut, "openrouter/other: secret_ref \"X\" resolved empty")
		return "LiteLLM routes not synced: one or more models could not be applied"
	}
	t.Cleanup(func() { syncRoutesAfterWrite = old })

	stdout, stderr, code := runCS(t, cfg, cloudSyncOpts{prices: true, yes: true})
	if code != 0 || !strings.HasSuffix(stdout, "prices: refreshed 1 model(s); 1 price(s) changed\nroutes: openrouter/vendor--gpt: routed\n") {
		t.Errorf("exit %d (a sync that warns does not fail the run); stdout:\n%s", code, stdout)
	}
	want := "routes: openrouter/other: secret_ref \"X\" resolved empty\n" +
		"routes: warning: LiteLLM routes not synced: one or more models could not be applied\n"
	if stderr != want {
		t.Errorf("stderr = %q\nwant     %q", stderr, want)
	}
}

// TestCloudSyncPricesRefusesWhenTheRegistryChangedAfterThePlan pins the
// re-plan under the registry lock: when the registry changes between the
// printed plan and the write (here, while the user reads the question), what
// would now be applied is not what was approved, so nothing is applied and
// the run says to run it again. The other writer's change survives.
func TestCloudSyncPricesRefusesWhenTheRegistryChangedAfterThePlan(t *testing.T) {
	path, cfg := cloudSyncHome(t, cloudSyncRegistry)
	stubCloudFetch(t, map[string]string{cloudsync.OpenRouterModelsURL: openRouterBody})
	synced := stubRouteSync(t, "")
	raced := strings.Replace(cloudSyncRegistry, "input_price_per_million = 2.5", "input_price_per_million = 7", 1)
	stubConfirm(t, true, nil, func() {
		if err := os.WriteFile(path, []byte(raced), 0o600); err != nil {
			t.Fatal(err)
		}
	})

	_, stderr, code := runCS(t, cfg, cloudSyncOpts{prices: true})
	if want := "prices: error: the registry changed after the plan was printed; no price was changed — run it again\n"; stderr != want || code != 1 {
		t.Errorf("stderr = %q, exit %d; want %q and exit 1", stderr, code, want)
	}
	if mustRead(t, path) != raced || *synced != 0 {
		t.Error("the refused run changed the registry or synced the routes")
	}
}

// TestCloudSyncReportsARefusedRegistryWrite pins what the user is told when
// the one registry write is refused: here a row the refresh must stamp has
// lost its family to a hand edit, so the writer will not write it. The run
// names the row, says the registry was not changed, exits 1 and syncs
// nothing. A write that skipped the bad row silently would leave a price
// stale with nothing saying why.
func TestCloudSyncReportsARefusedRegistryWrite(t *testing.T) {
	broken := strings.Replace(cloudSyncRegistry, "family = \"gpt\"\n", "", 1)
	path, cfg := cloudSyncHome(t, broken)
	stubCloudFetch(t, map[string]string{cloudsync.OpenRouterModelsURL: openRouterBody})
	synced := stubRouteSync(t, "")

	_, stderr, code := runCS(t, cfg, cloudSyncOpts{prices: true, yes: true})
	want := "prices: error: registry.toml was not changed: invalid registry entry: model \"openrouter/vendor--gpt\": family is required\n"
	if stderr != want || code != 1 {
		t.Errorf("stderr = %q, exit %d\nwant %q and exit 1", stderr, code, want)
	}
	if mustRead(t, path) != broken || *synced != 0 {
		t.Error("a refused write changed the registry or synced the routes")
	}
}

// TestCloudSyncUnderARedirectedRegistry pins the scratch-registry rule for
// this writer, with the real route sync: the registry write succeeds either
// way and the run exits 0; without WT_LITELLM_CONFIG the sync is refused
// with a warning and the default config.yaml is not touched, and with it the
// named file is the one synced. This is what keeps a trial run against a
// copy of the registry from rewriting the real proxy's routes.
func TestCloudSyncUnderARedirectedRegistry(t *testing.T) {
	setup := func(t *testing.T) (registry, defaultYAML string, cfg *config.Config) {
		home := t.TempDir()
		registry = filepath.Join(home, "scratch", "registry.toml")
		defaultYAML = filepath.Join(home, ".config", "litellm", "config.yaml")
		for path, content := range map[string]string{registry: cloudSyncRegistry, defaultYAML: "model_list: []\n"} {
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("MODELMAN_REGISTRY", "")
		t.Setenv("WT_REGISTRY", registry)
		t.Setenv("WT_LITELLM_CONFIG", "")
		t.Setenv("MODELMAN_LITELLM_CONFIG", "")
		t.Setenv("WT_TEST_OPENROUTER_KEY", "sk-test-not-a-real-key")
		realRouteSync(t)
		stubCloudFetch(t, map[string]string{cloudsync.OpenRouterModelsURL: openRouterBody})
		cfg, err := config.Load()
		if err != nil {
			t.Fatal(err)
		}
		// realRouteSync probes nothing; say instead that every provider
		// answered, so the sync has no probe failure to warn about.
		probeInventory = localmodels.OnDiskSnapshotForTest
		return registry, defaultYAML, cfg
	}

	t.Run("config.yaml not named: registry written, routes refused, exit 0", func(t *testing.T) {
		registry, defaultYAML, cfg := setup(t)
		_, stderr, code := runCS(t, cfg, cloudSyncOpts{prices: true, yes: true})
		if code != 0 {
			t.Fatalf("exit %d; the registry write succeeded, so the command must too. stderr: %s", code, stderr)
		}
		if !strings.Contains(mustRead(t, registry), "input_price_per_million = 3.0") {
			t.Error("the redirected registry was not written")
		}
		want := "routes: warning: LiteLLM routes not touched: the registry is " + registry + " but config.yaml is the default " + defaultYAML +
			" — set WT_LITELLM_CONFIG to the config.yaml that registry belongs to\n"
		if stderr != want {
			t.Errorf("stderr = %q\nwant %q", stderr, want)
		}
		if mustRead(t, defaultYAML) != "model_list: []\n" {
			t.Error("the default config.yaml was touched")
		}
	})

	t.Run("config.yaml named: that file synced, no warning", func(t *testing.T) {
		registry, defaultYAML, cfg := setup(t)
		named := filepath.Join(t.TempDir(), "scratch-config.yaml")
		if err := os.WriteFile(named, []byte("model_list: []\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("WT_LITELLM_CONFIG", named)
		stdout, stderr, code := runCS(t, cfg, cloudSyncOpts{prices: true, yes: true})
		if code != 0 || stderr != "" {
			t.Fatalf("exit %d, stderr %q", code, stderr)
		}
		if !strings.Contains(mustRead(t, registry), "input_price_per_million = 3.0") {
			t.Error("the redirected registry was not written")
		}
		if !strings.Contains(stdout, "routes: openrouter/vendor--gpt: ") {
			t.Errorf("stdout lacks the route sync's line for the repriced model, under routes:\n%s", stdout)
		}
		if yaml := mustRead(t, named); !strings.Contains(yaml, "openrouter/vendor--gpt") || !strings.Contains(yaml, "input_cost_per_token: 3e-06") {
			t.Errorf("the named config.yaml does not carry the route at its new price:\n%s", yaml)
		}
		if mustRead(t, defaultYAML) != "model_list: []\n" {
			t.Error("the default config.yaml was touched")
		}
	})
}
```

`WT_TEST_OPENROUTER_KEY` and its value are made up for the test: the route sync resolves the provider's `secret_ref` to build an OpenRouter row, and the test registry names a variable no real environment has. No test reads `OPENROUTER_API_KEY`.

`TestPromptCloudSyncAsksOnTheTerminalAndDefaultsToNo` hands the real prompt one end of a `syscall.Socketpair` as its terminal (a temp file will not do: the prompt writes its question to the same descriptor it then reads the answer from). `noTerminal` makes `openTTY` fail, which is what an agent's shell or cron looks like, so the refusal is tested on a developer's machine too, where a real `/dev/tty` would otherwise make the prompt wait for a key.

- [ ] **Step 3: Run the tests to verify they fail**

Run (from `wt/`): `go test -count=1 ./cmd/wt -run 'CloudSync|SelectFlows|RealCloudFetch'`
Expected:

```text
# github.com/ohanaverse/local-ai-setup/wt/cmd/wt [github.com/ohanaverse/local-ai-setup/wt/cmd/wt.test]
cmd/wt/cloudsync_test.go:123:9: undefined: cloudSyncNow
cmd/wt/cloudsync_test.go:124:2: undefined: cloudSyncNow
cmd/wt/cloudsync_test.go:125:21: undefined: cloudSyncNow
cmd/wt/cloudsync_test.go:145:9: undefined: cloudFetch
```

- [ ] **Step 4: Write the command**

Create `wt/cmd/wt/cloudsync.go`:

```go
// wt cloud-sync — refresh what public services publish into registry.toml.
// One flow so far: OpenRouter's prices. The planning is internal/cloudsync's;
// this file owns the fetch, the confirmation, the one registry write, the
// route sync and the exit code.
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cloudsync"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/spf13/cobra"
)

// Test seams. cmd/wt's TestMain makes all of them fail closed, so no test
// reaches the network or a terminal.
var (
	// cloudFetch GETs one public page: OpenRouter's model list.
	cloudFetch = realCloudFetch
	// confirmCloudSync asks the one question that covers both printed plans.
	confirmCloudSync = promptCloudSync
	// cloudSyncNow is the clock the stamps and the saved-page name read.
	cloudSyncNow = time.Now
)

// cloudSyncFlows are the flows `--only` selects among, in the order they are
// planned and reported.
var cloudSyncFlows = []string{"prices"}

var cloudHTTP = &http.Client{Timeout: 30 * time.Second}

// cloudFetchLimit bounds one response. OpenRouter's model list, the largest,
// is under 1 MiB.
const cloudFetchLimit = 32 << 20

func realCloudFetch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := cloudHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, cloudFetchLimit))
}

// promptCloudSync asks on the controlling terminal and defaults to No, like
// promptStop: piped input can never approve a sync. Without a terminal it
// names the flag that applies without asking. The terminal is opened through
// openTTY (launch.go), the seam a test takes it away with.
func promptCloudSync(question string) (bool, error) {
	f, err := openTTY()
	if err != nil {
		return false, errors.New("there is no terminal to confirm on — rerun with --yes to apply without asking")
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "%s [y/N] ", question); err != nil {
		return false, err
	}
	return askYesNo(f)
}

// cloudSyncOpts is the parsed command line.
type cloudSyncOpts struct {
	prices      bool
	dryRun, yes bool
}

// selectFlows reads --only: a comma list of flow names, every flow when it
// is empty.
func selectFlows(only string, o *cloudSyncOpts) error {
	if strings.TrimSpace(only) == "" {
		o.prices = true
		return nil
	}
	for _, name := range strings.Split(only, ",") {
		switch name = strings.TrimSpace(name); name {
		case "prices":
			o.prices = true
		default:
			return fmt.Errorf("--only: unknown flow %q (valid: %s)", name, strings.Join(cloudSyncFlows, ", "))
		}
	}
	return nil
}

func cloudSyncCmd(a *app) *cobra.Command {
	var (
		o    cloudSyncOpts
		only string
	)
	cmd := &cobra.Command{
		Use:   "cloud-sync",
		Short: "Refresh cloud prices (OpenRouter) into registry.toml",
		Long: "Bring registry.toml's prices up to date with what OpenRouter publishes, for\n" +
			"the models priced by OpenRouter.\n\n" +
			"The plan is printed first, each line prefixed with its flow (prices:).\n" +
			"--dry-run stops there. Otherwise one confirmation is asked on the terminal\n" +
			"(--yes skips it), the registry is written once, and the LiteLLM routes are\n" +
			"synced once if a price changed.\n\n" +
			"Exit status: 0 when the flow finished or had nothing to do, 1 when a step\n" +
			"failed or on a usage error.",
		Example: "  wt cloud-sync --dry-run\n" +
			"  wt cloud-sync --yes",
		Args: cobra.NoArgs,
		// A failed fetch or a refused plan is not a usage mistake, and main
		// prints the one error line.
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := selectFlows(only, &o); err != nil {
				return err
			}
			// Only a config that could not be loaded stops this: a.cfg is
			// then an empty default, and a sync planned against it would
			// find nothing to keep. A validation gap does not.
			if a.loadErr != nil {
				return configError(a.loadErr)
			}
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			return runCloudSync(ctx, cmd.OutOrStdout(), cmd.ErrOrStderr(), a.cfg, o)
		},
	}
	cmd.Flags().StringVar(&only, "only", "", "Run only these flows: a comma list of "+strings.Join(cloudSyncFlows, ", ")+" (default: all)")
	cmd.Flags().BoolVar(&o.dryRun, "dry-run", false, "Print the plan and change nothing")
	cmd.Flags().BoolVar(&o.yes, "yes", false, "Apply without asking")
	return cmd
}

// cloudSyncOutcome collects what the exit status is made of.
type cloudSyncOutcome struct {
	// failed: a step failed.
	failed bool
}

// err is the command's result: nil, or an exitCodeError.
func (r *cloudSyncOutcome) err() error {
	if r.failed {
		return &exitCodeError{code: 1, err: errors.New("cloud-sync: a step failed; see the error lines above")}
	}
	return nil
}

// prefixLines writes text to w with every line prefixed by "<flow>: ".
func prefixLines(w io.Writer, flow, text string) {
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		fmt.Fprintf(w, "%s: %s\n", flow, line)
	}
}

// pricesRun is the prices flow between its plan and its apply.
type pricesRun struct {
	api     map[string]cloudsync.APIPrice
	plan    *cloudsync.PricePlan
	printed string
}

// planPricesFlow fetches OpenRouter's list and prints the price plan. It
// returns nil when there is nothing to apply: no OpenRouter-priced model (no
// fetch is made), or a fetch that failed (reported, and res.failed set).
func planPricesFlow(ctx context.Context, out, errOut io.Writer, entries []cloudsync.Entry, providers []cloudsync.Provider, res *cloudSyncOutcome) *pricesRun {
	if !slices.ContainsFunc(entries, func(e cloudsync.Entry) bool { return cloudsync.OpenRouterPriced(e, providers) }) {
		fmt.Fprintln(out, "prices: no OpenRouter-priced model in the registry; nothing to refresh")
		return nil
	}
	body, err := cloudFetch(ctx, cloudsync.OpenRouterModelsURL)
	var api map[string]cloudsync.APIPrice
	if err == nil {
		api, err = cloudsync.ParseOpenRouter(body)
	}
	if err != nil {
		fmt.Fprintf(errOut, "prices: error: could not read OpenRouter's prices: %v; no price was changed\n", err)
		res.failed = true
		return nil
	}
	run := &pricesRun{api: api, plan: cloudsync.PlanPrices(entries, providers, api)}
	run.printed = run.plan.Format()
	prefixLines(out, "prices", run.printed)
	return run
}

// cloudSyncApplied is what the one registry write did. The apply function
// assigns it afresh on every run.
type cloudSyncApplied struct {
	prices      cloudsync.PricesApplied
	pricesStale bool
}

// runCloudSync is `wt cloud-sync`: plan with no lock held, print the plan,
// confirm, apply in one registry write, sync the routes once.
func runCloudSync(ctx context.Context, out, errOut io.Writer, _ *config.Config, o cloudSyncOpts) error {
	doc, err := config.ReadRegistryDoc()
	if err != nil {
		return withRegistryHint(err)
	}
	entries, providers := cloudsync.Entries(doc.Models()), cloudsync.Providers(doc.Providers())

	var res cloudSyncOutcome
	var prices *pricesRun
	if o.prices {
		prices = planPricesFlow(ctx, out, errOut, entries, providers, &res)
	}
	if o.dryRun {
		return res.err()
	}

	// What is left to apply. A plan with nothing in it is not asked about.
	if prices != nil && !prices.plan.HasWork() {
		prices = nil
	}
	if prices == nil {
		return res.err()
	}
	// The flows about to be applied, for the lines that concern all of them.
	pending := []string{"prices"}
	notApplied := func(w io.Writer, format string, args ...any) {
		for _, flow := range pending {
			fmt.Fprintf(w, "%s: %s\n", flow, fmt.Sprintf(format, args...))
		}
	}
	if !o.yes {
		ok, err := confirmCloudSync("Apply these changes?")
		if err != nil {
			notApplied(errOut, "error: not applied: %v", err)
			res.failed = true
			return res.err()
		}
		if !ok {
			notApplied(out, "not applied (declined)")
			return res.err()
		}
	}

	now := cloudSyncNow()
	var applied cloudSyncApplied
	_, err = config.UpdateRegistry(func(d *config.RegistryDoc) error {
		// Afresh on every run: apply may run more than once. The plan is
		// made again from the file as it is under the lock, and applied only
		// if it is still the plan that was printed.
		applied = cloudSyncApplied{}
		entries, providers := cloudsync.Entries(d.Models()), cloudsync.Providers(d.Providers())
		fresh := cloudsync.PlanPrices(entries, providers, prices.api)
		if applied.pricesStale = fresh.Format() != prices.printed; !applied.pricesStale {
			done, err := fresh.Apply(d, now)
			if err != nil {
				return err
			}
			applied.prices = done
		}
		return nil
	})
	if err != nil {
		notApplied(errOut, "error: registry.toml was not changed: %v", withRegistryHint(err))
		res.failed = true
		return res.err()
	}

	if applied.pricesStale {
		fmt.Fprintln(errOut, "prices: error: the registry changed after the plan was printed; no price was changed — run it again")
		res.failed = true
	} else {
		fmt.Fprintf(out, "prices: refreshed %d model(s); %d price(s) changed\n", applied.prices.Stamped, applied.prices.Changed)
	}

	// A run that only stamped models changed no route: skip the sync, which
	// could restart the proxy for nothing.
	if applied.prices.Changed > 0 {
		var routes, routeErrs bytes.Buffer
		warning := syncRoutesAfterWrite(&routes, &routeErrs)
		if routes.Len() > 0 {
			prefixLines(out, "routes", routes.String())
		}
		if routeErrs.Len() > 0 {
			prefixLines(errOut, "routes", routeErrs.String())
		}
		if warning != "" {
			fmt.Fprintf(errOut, "routes: warning: %s\n", warning)
		}
	}
	return res.err()
}

// withRegistryHint is err with the repair config names for it, when it names
// one.
func withRegistryHint(err error) error {
	if hint := config.RegistryFixHint(err); hint != "" {
		return fmt.Errorf("%w (%s)", err, hint)
	}
	return err
}
```

`runCloudSync` takes a `*config.Config` it does not use yet. Task 11 uses it for the ollama address, and keeping the signature now means the tests written here do not change then.

- [ ] **Step 5: Register the command**

In `wt/cmd/wt/main.go`, find:

```go
			"  wt stop [model|provider]     # stop a local model or provider (picker when omitted)\n" +
			"  wt smoke [model]             # smoke-test every agent that supports a model",
		// ArbitraryArgs overrides cobra's default legacyArgs validator, which
```

and replace it with:

```go
			"  wt stop [model|provider]     # stop a local model or provider (picker when omitted)\n" +
			"  wt smoke [model]             # smoke-test every agent that supports a model\n" +
			"  wt cloud-sync --dry-run      # plan a refresh of cloud prices",
		// ArbitraryArgs overrides cobra's default legacyArgs validator, which
```

In `wt/cmd/wt/main.go`, find:

```go

	cmd.AddCommand(rotateCmd(a), configCmd(a), statsCmd(a), smokeCmd(a), stopCmd(a), startCmd(a), servedCmd(a), warmCmd(a), litellmCmd(a), profileCmd(a), modelCmd(a))
	return cmd
```

and replace it with:

```go

	cmd.AddCommand(rotateCmd(a), configCmd(a), statsCmd(a), smokeCmd(a), stopCmd(a), startCmd(a), servedCmd(a), warmCmd(a), litellmCmd(a), profileCmd(a), modelCmd(a), cloudSyncCmd(a))
	return cmd
```

- [ ] **Step 6: Run the tests to verify they pass**

Run (from `wt/`): `go test -count=1 ./cmd/wt -run 'CloudSync|SelectFlows|RealCloudFetch'`
Expected:

```text
ok  	github.com/ohanaverse/local-ai-setup/wt/cmd/wt	(time)
```

(`-v` lists 15 passing top-level tests.)

- [ ] **Step 7: Run the built binary in a throwaway home**

This checks the pieces no test runs: the cobra wiring and `main`'s exit status and single error line. It does not reach the confirmation (none of the three commands has anything to apply); the prompt and its no-terminal refusal are pinned by the two tests above. It makes **no network call**: with a registry that has no OpenRouter-priced model the prices flow fetches nothing. The only `ollama` on its `PATH` is a stand-in that records what it is asked, and this slice asks it nothing. From `wt/`:

```bash
SCRATCH="$(mktemp -d)"
mkdir -p "$SCRATCH/bin" "$SCRATCH/home/.config/local-ai"
go build -o "$SCRATCH/bin/wt" ./cmd/wt
cat > "$SCRATCH/bin/ollama" <<EOF
#!/bin/sh
# Stand-in for ollama: records argv and OLLAMA_HOST, changes nothing.
echo "argv: \$* | OLLAMA_HOST=\$OLLAMA_HOST" >> "$SCRATCH/ollama-calls.log"
exit 0
EOF
chmod +x "$SCRATCH/bin/ollama"
cat > "$SCRATCH/home/.config/local-ai/registry.toml" <<'EOF'
[[providers]]
id = "ollama"
name = "Ollama"
location = "local"

[providers.auth]
type = "none"
EOF
run() { env -i PATH="$SCRATCH/bin:/usr/bin:/bin" HOME="$SCRATCH/home" XDG_CONFIG_HOME="$SCRATCH/home/.config" \
  WT_LITELLM_CONFIG="$SCRATCH/home/litellm.yaml" WT_LITELLM_RESTART_CMD=true "$SCRATCH/bin/wt" "$@" 2>&1; echo "exit=$?"; }
run cloud-sync --dry-run
run cloud-sync --only catalog
run cloud-sync extra
test -e "$SCRATCH/ollama-calls.log" || echo "no ollama command was run"
rm -rf "$SCRATCH"
```

Expected:

```text
prices: no OpenRouter-priced model in the registry; nothing to refresh
exit=0
wt: --only: unknown flow "catalog" (valid: prices)
exit=1
wt: unknown command "extra" for "wt cloud-sync"
exit=1
no ollama command was run
```

Each error is one line: no `Error:` line from cobra and no usage dump. The last line says the stand-in's log was never created.

- [ ] **Step 8: Keep the docs true**

In `wt/CLAUDE.md`, find:

```text
| `cmd/wt/model.go` | `wt model` group; `wt model init [--json]` — creates the registry and seeds provider rows, then one route sync (`syncRoutesAfterWrite`) |
```

and replace it with:

```text
| `cmd/wt/model.go` | `wt model` group; `wt model init [--json]` — creates the registry and seeds provider rows, then one route sync (`syncRoutesAfterWrite`) |
| `cmd/wt/cloudsync.go` | `wt cloud-sync` — the command, the prices flow's fetch, the confirmation, the one `config.UpdateRegistry` both flows share (re-plan under the lock, refuse a plan that changed), the route sync, the exit code; seams `cloudFetch`, `confirmCloudSync`, `cloudSyncNow` (the prompt opens the terminal through `openTTY`) |
| `cmd/wt/exitcode.go` | `exitCodeError` / `exitCodeOf` — how one command exits with a status other than 1 |
```

In `wt/CLAUDE.md`, find:

```text
with no I/O. Its caller owns the fetches, the ollama CLI, the confirmation and the exit codes.
```

and replace it with:

```text
with no I/O. The command (`cmd/wt/cloudsync*.go`) owns the fetches, the ollama CLI, the confirmation and the exit codes.
```

In `wt/CLAUDE.md`, find:

```text
`config.Load` never writes it; wt's one writer today is `wt model init` (`config.SeedRegistryDefaults` inside `config.UpdateRegistry`), and modelman still writes it too.
```

and replace it with:

```text
`config.Load` never writes it; wt's writers are `wt model init` (`config.SeedRegistryDefaults`) and `wt cloud-sync` (`internal/cloudsync`'s `Apply` methods), each inside one `config.UpdateRegistry`, and modelman still writes it too.
```

In `wt/docs/internals/testing.md`, find:

```text
`statsNow`, `seedEnv`, `syncRoutesAfterWrite`) — production code calls the var, tests swap it.
```

and replace it with:

```text
`statsNow`, `seedEnv`, `syncRoutesAfterWrite`, `cloudFetch`, `confirmCloudSync`, `cloudSyncNow`) — production code calls the var, tests swap it.
```

In `wt/docs/internals/testing.md`, find:

```text
Tests use `stubSeedEnv`, `stubRouteSync` and, for the real sync against a scratch `config.yaml`, `realRouteSync` (`cmd/wt/model_test.go`).
```

and replace it with:

```text
Tests use `stubSeedEnv`, `stubRouteSync` and, for the real sync against a scratch `config.yaml`, `realRouteSync` (`cmd/wt/model_test.go`). For `wt cloud-sync` it makes `cloudFetch` and `confirmCloudSync` fail with "not stubbed", so an unstubbed test fetches no public page and opens no terminal (`TestCloudSyncSeamsFailClosed`); tests use `stubCloudFetch` (pages by URL, anything else a 404), `stubConfirm` (the answer, and a hook that plays another program writing the registry while the user reads the plan), `noTerminal` (the real `promptCloudSync` with `openTTY` failing, as under an agent's shell) and `cloudSyncHome` (a home with the test's registry, and the clock pinned through `cloudSyncNow`). `internal/cloudsync` has its own `TestMain`: its `Apply` tests go through `config.UpdateRegistry`, so it calls `config.IsolateConfigHomeForTest` and asserts the write guard is armed.
```

In `wt/CHANGELOG.md`, find:

```text
### Added

- `WT_REGISTRY` names the model registry file.
```

and replace it with:

```text
### Added

- `wt cloud-sync [--only prices] [--dry-run] [--yes]` refreshes the per-token
  prices of the registry's OpenRouter-priced models from OpenRouter's public
  model list, replacing `modelman refresh-prices` (which still works). It
  prints its plan as `id: old -> new` first; `--dry-run` stops there, and
  otherwise it asks once on the terminal unless `--yes` is given. Input and
  output prices take OpenRouter's value; a cache price is replaced only when
  OpenRouter reports one, and a subscription or time-windowed price is never
  touched. Every matched model is stamped, changed or not, which is what the
  stale-pricing notice reads. The LiteLLM routes are synced once when a
  price changed, and not at all when none did. Nothing refreshes prices
  automatically.
- `WT_REGISTRY` names the model registry file.
```

In `CLAUDE.md`, find:

```text
whose only caller so far is `wt model init` (create the file, seed provider rows);
```

and replace it with:

```text
whose callers are `wt model init` (create the file, seed provider rows) and `wt cloud-sync` (cloud prices);
```

In `docs/guides/00-config-map.md`, find:

```text
`wt` — `wt model init` only, for now: it creates the file when it is missing and appends provider rows, never editing a row that exists.
```

and replace it with:

```text
`wt` — `wt model init` (it creates the file when it is missing and appends provider rows, never editing a row that exists) and `wt cloud-sync` (it refreshes cloud prices, and stamps `pricing_updated_at` on the models it priced).
```

- [ ] **Step 9: Verify the slice**

From `wt/`:

```bash
test -z "$(gofmt -l .)" && go vet ./... && go test -count=1 ./... && make check
```

Expected: 23 `ok` lines, no `FAIL`. Then from the monorepo root:

```bash
make test-all
make check-links
```

Expected: `make test-all` exits 0 (llmbench and modelman's suites pass: this slice changed a comment in a contract fixture modelman reads, and nothing else Python sees), and `ALL LINKS OK`.

- [ ] **Step 10: Commit**

From the repo root:

```bash
git add wt/cmd/wt/cloudsync.go wt/cmd/wt/cloudsync_test.go wt/cmd/wt/testmain_test.go wt/cmd/wt/main.go wt/CLAUDE.md wt/CHANGELOG.md wt/docs/internals/testing.md CLAUDE.md docs/guides/00-config-map.md
git commit -m "feat(wt): wt cloud-sync refreshes OpenRouter prices (the prices flow)"
```

PR 2 is ready. **Ask the owner before pushing or opening the PR.**

---

## PR 3 — the catalog flow

Branch: `git switch -c feat/wt-cloud-sync-catalog main`, after PR 2 has merged.

### Task 10: The ollama CLI, pinned to the registry's daemon

**Files:**
- Create: `wt/cmd/wt/cloudsync_ollama_test.go`, `wt/cmd/wt/cloudsync_ollama.go`
- Modify: `wt/cmd/wt/testmain_test.go`, `wt/CLAUDE.md`, `wt/docs/internals/testing.md`

**Interfaces:**
- Consumes: nothing from earlier tasks. The pattern is `ollamaBackend.stopModel` (`internal/lifecycle/ollama.go:37`), which runs `ollama stop` with `OLLAMA_HOST=<origin>` layered over the inherited environment (`:43`; `runCommandEnv`, `internal/lifecycle/env.go:80`). That helper is unexported and returns combined output; this task needs stdout and stderr apart (the tag list is on stdout, "not found" on stderr), so it has its own.
- Produces:
  - seam `ollamaCLI func(ctx context.Context, origin string, args ...string) (stdout, stderr string, err error)`
  - `func ollamaTags(ctx context.Context, origin string) ([]string, error)`
  - `func ollamaPull(ctx context.Context, origin, tag string) error`
  - `func ollamaRemove(ctx context.Context, origin, tag string) error` ("not found" on stderr is success)
  - test helpers for Task 11: `testOllamaOrigin`, `type fakeOllama` (fields `tags`, `fail`, `calls`; method `changes() []string`), `stubOllama(t, tags ...string) *fakeOllama`

- [ ] **Step 1: Make the seam fail closed in `TestMain`**

In `wt/cmd/wt/testmain_test.go`, find:

```go
	}
	// `wt cloud-sync` seams: no test may fetch a public page or open the
	// terminal to ask. Tests use stubCloudFetch and stubConfirm
	// (cloudsync_test.go).
	cloudFetch = func(_ context.Context, url string) ([]byte, error) {
		return nil, errors.New("cloudFetch not stubbed in this test: " + url)
	}
```

and replace it with:

```go
	}
	// `wt cloud-sync` seams: no test may fetch a public page, run the
	// developer's ollama (a pull or an rm there changes their machine), or
	// open the terminal to ask. Tests use stubCloudFetch, stubOllama and
	// stubConfirm (cloudsync_test.go).
	cloudFetch = func(_ context.Context, url string) ([]byte, error) {
		return nil, errors.New("cloudFetch not stubbed in this test: " + url)
	}
	ollamaCLI = func(_ context.Context, _ string, args ...string) (string, string, error) {
		return "", "", errors.New("ollamaCLI not stubbed in this test: ollama " + strings.Join(args, " "))
	}
```

(`testmain_test.go` already imports `strings`.)

- [ ] **Step 2: Write the failing tests**

Create `wt/cmd/wt/cloudsync_ollama_test.go`:

```go
package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// The ollama provider row of cloudSyncRegistry names this address, and every
// ollama command must be pinned to it.
const testOllamaOrigin = "http://127.0.0.1:11434"

// fakeOllama stands in for the ollama CLI. It records every command with the
// origin it was pinned to, and changes nothing anywhere.
type fakeOllama struct {
	mu    sync.Mutex
	tags  []string
	fail  map[string]string // "list", "pull <tag>" or "rm <tag>" -> stderr
	calls []string
}

func stubOllama(t *testing.T, tags ...string) *fakeOllama {
	t.Helper()
	fake := &fakeOllama{tags: tags, fail: map[string]string{}}
	old := ollamaCLI
	ollamaCLI = func(_ context.Context, origin string, args ...string) (string, string, error) {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		command := strings.Join(args, " ")
		fake.calls = append(fake.calls, origin+" "+command)
		if stderr, bad := fake.fail[command]; bad {
			return "", stderr, errors.New("exit status 1")
		}
		if args[0] != "list" {
			return "", "", nil
		}
		out := "NAME                    ID              SIZE      MODIFIED\n"
		for _, tag := range fake.tags {
			out += tag + "    0123456789ab    -         2 days ago\n"
		}
		return out, "", nil
	}
	t.Cleanup(func() { ollamaCLI = old })
	return fake
}

// changes are the recorded commands other than `list`, without the origin.
func (f *fakeOllama) changes() []string {
	var out []string
	for _, call := range f.calls {
		if command := strings.TrimPrefix(call, testOllamaOrigin+" "); command != "list" {
			out = append(out, command)
		}
	}
	return out
}

// TestOllamaSeamFailsClosed pins TestMain: with nothing stubbed, no test in
// this package runs the developer's ollama. An unstubbed catalog test fails
// with "not stubbed" instead of pulling a model onto, or removing one from,
// the machine it runs on.
func TestOllamaSeamFailsClosed(t *testing.T) {
	if _, _, err := ollamaCLI(context.Background(), testOllamaOrigin, "pull", "x:cloud"); err == nil || !strings.Contains(err.Error(), "not stubbed") {
		t.Errorf("ollamaCLI err = %v, want a not-stubbed failure", err)
	}
}

// TestOllamaRemoveReadsNotFoundAsDone pins the one failure of `ollama rm`
// that is not one: the tag is already gone. Anything else is an error that
// carries what ollama said, and `ollama pull` has no such exception.
func TestOllamaRemoveReadsNotFoundAsDone(t *testing.T) {
	ollama := stubOllama(t)
	ollama.fail["rm gone:cloud"] = "Error: model 'gone:cloud' not found"
	ollama.fail["rm locked:cloud"] = "Error: permission denied"
	ollama.fail["pull missing:cloud"] = "Error: pull model manifest: file does not exist"
	ctx := context.Background()
	if err := ollamaRemove(ctx, testOllamaOrigin, "gone:cloud"); err != nil {
		t.Errorf("rm of a tag that is not there: err = %v, want none", err)
	}
	if err := ollamaRemove(ctx, testOllamaOrigin, "locked:cloud"); err == nil || err.Error() != "`ollama rm locked:cloud` failed: Error: permission denied" {
		t.Errorf("rm that failed: err = %v", err)
	}
	if err := ollamaPull(ctx, testOllamaOrigin, "missing:cloud"); err == nil || err.Error() != "`ollama pull missing:cloud` failed: Error: pull model manifest: file does not exist" {
		t.Errorf("pull that failed: err = %v", err)
	}
	if err := ollamaPull(ctx, testOllamaOrigin, "fine:cloud"); err != nil {
		t.Errorf("pull that worked: err = %v", err)
	}
	want := []string{"rm gone:cloud", "rm locked:cloud", "pull missing:cloud", "pull fine:cloud"}
	if got := ollama.changes(); !reflect.DeepEqual(got, want) {
		t.Errorf("ollama commands = %q, want %q", got, want)
	}
}

// TestOllamaTagsReadsTheNameColumn pins the `ollama list` parser on real
// output shapes: the header and blank lines are skipped, and a size or date
// with spaces in it does not bleed into the name.
func TestOllamaTagsReadsTheNameColumn(t *testing.T) {
	old := ollamaCLI
	t.Cleanup(func() { ollamaCLI = old })
	ollamaCLI = func(context.Context, string, ...string) (string, string, error) {
		return "NAME                 ID      SIZE  MODIFIED\nglm-5.3:cloud  abc  -  2 days ago\nornith-1.5:35b  def  20 GB  1 week ago\n\n", "", nil
	}
	tags, err := ollamaTags(context.Background(), testOllamaOrigin)
	if want := []string{"glm-5.3:cloud", "ornith-1.5:35b"}; err != nil || !reflect.DeepEqual(tags, want) {
		t.Errorf("ollamaTags = (%q, %v), want %q", tags, err, want)
	}
	ollamaCLI = func(context.Context, string, ...string) (string, string, error) {
		return "", "", errors.New("the ollama command is not installed (not on PATH)")
	}
	if _, err := ollamaTags(context.Background(), testOllamaOrigin); err == nil || err.Error() != "the ollama command is not installed (not on PATH)" {
		t.Errorf("ollamaTags with no ollama: err = %v", err)
	}
}

// TestRealOllamaCLIPinsTheDaemon runs the real exec path against a stand-in
// `ollama` script and pins the one thing that matters about it: the command
// talks to the daemon the registry names, even when the shell exports
// another OLLAMA_HOST. Otherwise a pull or an rm could land on a daemon wt
// does not manage. It also pins that stdout and stderr come back apart, and
// that a missing ollama is said plainly.
func TestRealOllamaCLIPinsTheDaemon(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\necho \"host=$OLLAMA_HOST args=$*\"\necho \"to stderr\" >&2\n[ \"$1\" = rm ] && exit 3\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "ollama"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("OLLAMA_HOST", "http://elsewhere.invalid:1")

	stdout, stderr, err := realOllamaCLI(context.Background(), "http://127.0.0.1:11999", "pull", "x:cloud")
	if err != nil || stdout != "host=http://127.0.0.1:11999 args=pull x:cloud\n" || stderr != "to stderr\n" {
		t.Errorf("realOllamaCLI = (%q, %q, %v)", stdout, stderr, err)
	}
	if _, _, err := realOllamaCLI(context.Background(), "http://127.0.0.1:11999", "rm", "x:cloud"); err == nil {
		t.Error("a non-zero exit was not an error")
	}
	t.Setenv("PATH", t.TempDir())
	if _, _, err := realOllamaCLI(context.Background(), "http://127.0.0.1:11999", "list"); err == nil || err.Error() != "the ollama command is not installed (not on PATH)" {
		t.Errorf("with no ollama on PATH: err = %v", err)
	}
}
```

`TestRealOllamaCLIPinsTheDaemon` is the one test that starts a process: a four-line shell script it writes into a temp directory, found through a `PATH` that holds nothing else. It cannot reach a real `ollama`.

- [ ] **Step 3: Run the tests to verify they fail**

Run (from `wt/`): `go test -count=1 ./cmd/wt -run Ollama`
Expected:

```text
# github.com/ohanaverse/local-ai-setup/wt/cmd/wt [github.com/ohanaverse/local-ai-setup/wt/cmd/wt.test]
cmd/wt/cloudsync_ollama_test.go:30:9: undefined: ollamaCLI
cmd/wt/cloudsync_ollama_test.go:31:2: undefined: ollamaCLI
cmd/wt/cloudsync_ollama_test.go:48:21: undefined: ollamaCLI
cmd/wt/cloudsync_ollama_test.go:68:18: undefined: ollamaCLI
```

- [ ] **Step 4: Write the ollama helpers**

Create `wt/cmd/wt/cloudsync_ollama.go`:

```go
// The ollama CLI as wt cloud-sync's catalog flow uses it: list what is
// pulled, pull a tag, remove a tag. Always the CLI, never the HTTP API, and
// always pinned to the daemon the registry's ollama provider row names.
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ollamaCLI runs the ollama command against the daemon at origin and returns
// what it printed. A seam: cmd/wt's TestMain makes it fail, so no test runs
// the developer's ollama.
var ollamaCLI = realOllamaCLI

// How long one ollama command may take. A cloud model's pull fetches a
// manifest, not weights.
const (
	ollamaListTimeout = 30 * time.Second
	ollamaRmTimeout   = 60 * time.Second
	ollamaPullTimeout = 10 * time.Minute
)

// realOllamaCLI pins the CLI to origin with OLLAMA_HOST, as `wt stop` does
// (internal/lifecycle/ollama.go): the registry's ollama provider row says
// which daemon wt manages, and an OLLAMA_HOST inherited from the shell that
// pointed elsewhere would pull into, or remove from, a daemon the registry
// does not describe.
func realOllamaCLI(ctx context.Context, origin string, args ...string) (stdout, stderr string, err error) {
	bin, err := exec.LookPath("ollama")
	if err != nil {
		return "", "", errors.New("the ollama command is not installed (not on PATH)")
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(), "OLLAMA_HOST="+origin)
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err = cmd.Run()
	return so.String(), se.String(), err
}

// ollamaFailure words a failed ollama command: what it printed on stderr,
// else why it could not run.
func ollamaFailure(stderr string, err error) string {
	if msg := strings.TrimSpace(stderr); msg != "" {
		return msg
	}
	return err.Error()
}

// ollamaTags is `ollama list`'s NAME column: every pulled tag, cloud stubs
// included. (wt's inventory probe cannot serve here: it leaves cloud stubs
// out on purpose, since they are not local models.)
func ollamaTags(ctx context.Context, origin string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, ollamaListTimeout)
	defer cancel()
	stdout, stderr, err := ollamaCLI(ctx, origin, "list")
	if err != nil {
		return nil, errors.New(ollamaFailure(stderr, err))
	}
	var tags []string
	for _, line := range strings.Split(stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] == "NAME" {
			continue
		}
		tags = append(tags, fields[0])
	}
	return tags, nil
}

// ollamaRemove is `ollama rm`, reading "not found" as already done: a tag
// that vanished since `ollama list` (another `ollama rm`, ollama pruning a
// retired stub) is gone, which is what was wanted.
func ollamaRemove(ctx context.Context, origin, tag string) error {
	ctx, cancel := context.WithTimeout(ctx, ollamaRmTimeout)
	defer cancel()
	_, stderr, err := ollamaCLI(ctx, origin, "rm", tag)
	if err != nil && !strings.Contains(strings.ToLower(stderr), "not found") {
		return fmt.Errorf("`ollama rm %s` failed: %s", tag, ollamaFailure(stderr, err))
	}
	return nil
}

func ollamaPull(ctx context.Context, origin, tag string) error {
	ctx, cancel := context.WithTimeout(ctx, ollamaPullTimeout)
	defer cancel()
	if _, stderr, err := ollamaCLI(ctx, origin, "pull", tag); err != nil {
		return fmt.Errorf("`ollama pull %s` failed: %s", tag, ollamaFailure(stderr, err))
	}
	return nil
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run (from `wt/`): `go test -count=1 ./cmd/wt -run Ollama`
Expected:

```text
ok  	github.com/ohanaverse/local-ai-setup/wt/cmd/wt	(time)
```

- [ ] **Step 6: Keep the docs true**

In `wt/CLAUDE.md`, find:

```text
| `cmd/wt/exitcode.go` | `exitCodeError` / `exitCodeOf` — how one command exits with a status other than 1 |
```

and replace it with:

```text
| `cmd/wt/cloudsync_ollama.go` | the ollama CLI for the catalog flow: `ollamaTags` (`ollama list`, cloud stubs included), `ollamaPull`, `ollamaRemove` ("not found" is done), all through the `ollamaCLI` seam, pinned with `OLLAMA_HOST` to the registry's ollama origin |
| `cmd/wt/exitcode.go` | `exitCodeError` / `exitCodeOf` — how one command exits with a status other than 1 |
```

In `wt/docs/internals/testing.md`, find:

```text
`cloudFetch`, `confirmCloudSync`, `cloudSyncNow`) — production code calls the var, tests swap it.
```

and replace it with:

```text
`cloudFetch`, `confirmCloudSync`, `cloudSyncNow`, `ollamaCLI`) — production code calls the var, tests swap it.
```

In `wt/docs/internals/testing.md`, find:

```text
so an unstubbed test fetches no public page and opens no terminal (`TestCloudSyncSeamsFailClosed`);
```

and replace it with:

```text
so an unstubbed test fetches no public page and opens no terminal (`TestCloudSyncSeamsFailClosed`), and `ollamaCLI` fail the same way, so none runs the developer's ollama — a pull or an rm there changes their machine (`TestOllamaSeamFailsClosed`; `stubOllama` records each command with the origin it was pinned to, and `TestRealOllamaCLIPinsTheDaemon` runs the real exec path against a script in a temp `PATH`);
```

- [ ] **Step 7: Commit**

From the repo root:

```bash
git add wt/cmd/wt/cloudsync_ollama.go wt/cmd/wt/cloudsync_ollama_test.go wt/cmd/wt/testmain_test.go wt/CLAUDE.md wt/docs/internals/testing.md
git commit -m "feat(wt): the ollama CLI for cloud-sync, pinned to the registry's daemon"
```

### Task 11: The catalog flow in `wt cloud-sync`

**Files:**
- Create: `wt/cmd/wt/cloudsync_catalog_test.go`, `wt/cmd/wt/cloudsync_catalog.go`
- Modify: `wt/cmd/wt/cloudsync.go` (rewritten), `wt/cmd/wt/cloudsync_test.go`, `wt/cmd/wt/main.go`, `wt/CLAUDE.md`, `wt/CHANGELOG.md`, `CLAUDE.md`, `docs/guides/00-config-map.md`

**Interfaces:**
- Consumes: PR 1 (`cloudsync.PricingURL`, `ParsePricing`, `*ParseError`, `VerifiedTags`, `ResolveCloudTags`, `PlanCatalog`, `(*CatalogPlan).MassRemoval/RemovalDigest/HasWork/Format/Apply`, `CatalogApplied`); Task 9 (everything it produces); Task 10 (`ollamaTags`, `ollamaPull`, `ollamaRemove`, `stubOllama`); `localmodels.FamilyOrigin(cfg *config.Config, family string) (origin string, fromRegistry bool)` (`internal/localmodels/inventory.go:284`), the address of the registry's ollama provider row, `http://localhost:11434` when the row names none.
- Produces:
  - `cloudSyncFlows = {"prices", "catalog"}`; `cloudSyncOpts` gains `catalog bool`, `catalogNamed bool` (the catalog flow was asked for by name: `--only` names it, or a catalog flag was given), `force bool`, `htmlFile string`, `approve string`; `cloudSyncOutcome` gains `catalogCode int`
  - `type catalogRun struct { origin string; catalog cloudsync.Catalog; tags []string; resolved map[string]string; tagWarnings []string; plan *cloudsync.CatalogPlan; printed string }` with `replan(entries []cloudsync.Entry) *cloudsync.CatalogPlan`
  - `func planCatalogFlow(ctx, out, errOut, cfg, o, entries, providers, res) *catalogRun`
  - `func catalogGate(errOut io.Writer, c *catalogRun, o cloudSyncOpts, res *cloudSyncOutcome) bool`
  - `func runOllamaWork(ctx, out, errOut, origin string, done cloudsync.CatalogApplied, res *cloudSyncOutcome)`
  - `func saveFailedHTML(page string, now time.Time) (string, error)`
  - `func writeCloudSync(prices *pricesRun, catalog *catalogRun, now time.Time) (cloudSyncApplied, error)`: one `config.UpdateRegistry` for the flows it is given (either may be nil)
  - test helpers `bothPages() map[string]string` (the catalog's pages plus OpenRouter's list) and `noOllamaRegistry` (a registry with no ollama provider row)

Which code each stop sets, exactly: no ollama provider row → none when the flow runs by default (one stdout line, the flow is skipped), 1 when it was asked for by name; page fetch failed, `--html` unreadable, `ollama list` failed, no tag resolved → 2; `*ParseError` → 3; mass removal without `--force` → 4; under `--yes` a digest missing or not this plan's, or a re-plan under the lock that prints differently → 5. A failed pull or rm → 1, after the registry write. No terminal to confirm on → 1. A registry write refused because a row would not load → 1 for the flow that owns the row; the other flow is written on its own. `cloudSyncOutcome.err()` returns the catalog code when there is one, else 1 when a step failed.

When the route sync runs, exactly: a price changed (`PricesApplied.Changed > 0`), or the catalog changed the registry (`CatalogApplied.Changed()`), or the catalog had ollama work (`len(Pulls)+len(Removes) > 0`). Never for a stamp-only run.

- [ ] **Step 1: Write the failing tests**

Create `wt/cmd/wt/cloudsync_catalog_test.go`:

```go
package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cloudsync"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// catalogPage is a small ollama.com/pricing: five models, one with an
// off-peak row. Of cloudSyncRegistry's two ollama cloud entries it lists
// deepseek-v4-pro and not retired.
const catalogPage = `<html><table>
<thead><tr><th>Model</th><th>Input</th><th>Cached input</th><th>Output</th></tr></thead>
<tbody>
<tr><td><a href="/library/deepseek-v4-pro">deepseek-v4-pro</a></td><td>$1.32</td><td>$0.044</td><td>$3.96</td></tr>
<tr><td>deepseek-v4-pro (Off-Peak)</td><td>$0.66</td><td>$0.022</td><td>$1.98</td></tr>
<tr><td><a href="/library/glm-5.3">glm-5.3</a></td><td>$1.40</td><td>$0.26</td><td>$4.40</td></tr>
<tr><td><a href="/library/gemma4">gemma4</a></td><td>$0.14</td><td>$0.05</td><td>$0.40</td></tr>
<tr><td><a href="/library/kimi-k3">kimi-k3</a></td><td>$3.00</td><td>$0.30</td><td>$15.00</td></tr>
<tr><td><a href="/library/gpt-oss">gpt-oss:120b</a></td><td>$0.15</td><td>$0.014</td><td>$0.60</td></tr>
</tbody></table></html>`

// catalogPages is everything the catalog flow fetches for catalogPage: the
// page, and a library tags page for each bare name (gpt-oss:120b pins its
// size and needs none). kimi-k3 publishes only a sized cloud tag.
func catalogPages() map[string]string {
	lib := func(name string, tags ...string) string {
		var b strings.Builder
		for _, tag := range tags {
			fmt.Fprintf(&b, `<a href="/library/%s:%s">%s</a>`, name, tag, tag)
		}
		return b.String()
	}
	return map[string]string{
		cloudsync.PricingURL:                              catalogPage,
		"https://ollama.com/library/deepseek-v4-pro/tags": lib("deepseek-v4-pro", "cloud"),
		"https://ollama.com/library/glm-5.3/tags":         lib("glm-5.3", "cloud", "latest"),
		"https://ollama.com/library/gemma4/tags":          lib("gemma4", "cloud", "9b"),
		"https://ollama.com/library/kimi-k3/tags":         lib("kimi-k3", "1t-cloud"),
	}
}

// bothPages is catalogPages plus OpenRouter's model list, for a run of both
// flows.
func bothPages() map[string]string {
	pages := catalogPages()
	pages[cloudsync.OpenRouterModelsURL] = openRouterBody
	return pages
}

// catalogPulled is what `ollama list` shows in these tests: the two
// registered cloud stubs, a stray one, and a real local model.
var catalogPulled = []string{"deepseek-v4-pro:cloud", "retired:cloud", "stray:cloud", "qwen3:8b"}

func digestIn(t *testing.T, stdout string) string {
	t.Helper()
	m := regexp.MustCompile(`catalog: Removal digest: ([0-9a-f]{12}) `).FindStringSubmatch(stdout)
	if m == nil {
		t.Fatalf("no removal digest in:\n%s", stdout)
	}
	return m[1]
}

const catalogPlanText = `catalog: ollama.com/pricing: 5 models (prices are input/cached/output per million tokens)
catalog: Price updates (1):
catalog:   ollama/deepseek-v4-pro:cloud: 9/-/- -> 1.32/0.044/3.96 (off-peak 0.66/0.022/1.98)
catalog: Registry additions (4):
catalog:   ollama/glm-5.3:cloud [family glm-5.3]: 1.4/0.26/4.4
catalog:   ollama/gemma4:cloud [family gemma4]: 0.14/0.05/0.4
catalog:   ollama/kimi-k3:1t-cloud [family kimi-k3]: 3/0.3/15
catalog:   ollama/gpt-oss:120b-cloud [family gpt-oss]: 0.15/0.014/0.6
catalog: Unchanged prices: 0
catalog: ollama pull (4):
catalog:   ollama/glm-5.3:cloud
catalog:   ollama/gemma4:cloud
catalog:   ollama/kimi-k3:1t-cloud
catalog:   ollama/gpt-oss:120b-cloud
catalog: Registry removals — off ollama.com/pricing or under a tag ollama doesn't publish; ` + "`ollama rm`" + ` if pulled (1):
catalog:   ollama/retired:cloud
catalog: ollama rm — pulled, unregistered, off the page (1):
catalog:   stray:cloud
catalog: Removal digest: DIGEST (apply non-interactively with ` + "`--yes --approve-removals DIGEST`" + `)
catalog: warning: ollama cloud entries disagree on subscription pricing; new entries get none
`

// TestCloudSyncCatalogDryRun pins the catalog flow's dry run: the whole plan
// under the catalog: prefix, with the removal digest the real run will ask
// for, and nothing changed — the registry byte-identical, and ollama asked
// only for its list, pinned to the provider row's address.
func TestCloudSyncCatalogDryRun(t *testing.T) {
	path, cfg := cloudSyncHome(t, cloudSyncRegistry)
	stubCloudFetch(t, catalogPages())
	ollama := stubOllama(t, catalogPulled...)
	synced := stubRouteSync(t, "")

	stdout, stderr, code := runCS(t, cfg, cloudSyncOpts{catalog: true, dryRun: true})
	want := strings.ReplaceAll(catalogPlanText, "DIGEST", digestIn(t, stdout))
	if stdout != want || stderr != "" || code != 0 {
		t.Errorf("stdout:\n%s\nstderr: %q, exit %d\nwant stdout:\n%s", stdout, stderr, code, want)
	}
	if mustRead(t, path) != cloudSyncRegistry || *synced != 0 {
		t.Error("a dry run changed the registry or synced the routes")
	}
	if want := []string{testOllamaOrigin + " list"}; !reflect.DeepEqual(ollama.calls, want) {
		t.Errorf("ollama commands = %q, want only %q", ollama.calls, want)
	}
}

// TestCloudSyncCatalogApplyUnderTheApprovedDigest pins the path the skill
// takes: a dry run, then --yes with the digest it printed. The registry gets
// the page's prices, the new models and loses the retired one; then, and
// only then, ollama is asked to pull what is missing and to remove the
// retired stub and the stray one, every command pinned to the provider row's
// address; and the routes are synced once, at the end.
func TestCloudSyncCatalogApplyUnderTheApprovedDigest(t *testing.T) {
	path, cfg := cloudSyncHome(t, cloudSyncRegistry)
	fetches := stubCloudFetch(t, catalogPages())
	ollama := stubOllama(t, catalogPulled...)
	var order []string
	old := syncRoutesAfterWrite
	syncRoutesAfterWrite = func(out, _ io.Writer) string {
		order = append(order, fmt.Sprintf("sync after %d ollama commands", len(ollama.calls)))
		fmt.Fprintln(out, "ollama/glm-5.3:cloud: routed")
		return ""
	}
	t.Cleanup(func() { syncRoutesAfterWrite = old })

	dry, _, _ := runCS(t, cfg, cloudSyncOpts{catalog: true, dryRun: true})
	ollama.calls = nil
	stdout, stderr, code := runCS(t, cfg, cloudSyncOpts{catalog: true, yes: true, approve: digestIn(t, dry)})
	if code != 0 || stderr != "" {
		t.Fatalf("exit %d, stderr %q\nstdout:\n%s", code, stderr, stdout)
	}
	wantTail := "catalog: updated 1, added 4 and removed 1 model(s)\n" +
		"catalog: pulled glm-5.3:cloud\ncatalog: pulled gemma4:cloud\ncatalog: pulled kimi-k3:1t-cloud\ncatalog: pulled gpt-oss:120b-cloud\n" +
		"catalog: removed retired:cloud\ncatalog: removed stray:cloud\n" +
		"routes: ollama/glm-5.3:cloud: routed\n"
	if !strings.HasSuffix(stdout, wantTail) {
		t.Errorf("stdout ends:\n%s\nwant it to end:\n%s", stdout, wantTail)
	}
	wantCalls := []string{
		testOllamaOrigin + " list",
		testOllamaOrigin + " pull glm-5.3:cloud", testOllamaOrigin + " pull gemma4:cloud",
		testOllamaOrigin + " pull kimi-k3:1t-cloud", testOllamaOrigin + " pull gpt-oss:120b-cloud",
		testOllamaOrigin + " rm retired:cloud", testOllamaOrigin + " rm stray:cloud",
	}
	if !reflect.DeepEqual(ollama.calls, wantCalls) {
		t.Errorf("ollama commands:\n%q\nwant\n%q", ollama.calls, wantCalls)
	}
	if want := []string{"sync after 7 ollama commands"}; !reflect.DeepEqual(order, want) {
		t.Errorf("route syncs = %q, want one, after every ollama command", order)
	}
	text := mustRead(t, path)
	for _, snippet := range []string{
		"id = \"ollama/kimi-k3:1t-cloud\"\nfamily = \"kimi-k3\"\nprovider_id = \"ollama\"\nmodel_name = \"kimi-k3:1t-cloud\"\nlocation = \"cloud\"\nsource = \"curated\"\ntags = []\npricing_updated_at = \"" + cloudSyncStamp + "\"\n",
		"input_price_per_million = 1.32\ncache_price_per_million = 0.044\noutput_price_per_million = 3.96\nsubscription_price = 100\nsubscription_period = \"month\"\n",
		"label = \"off-peak\"\ntimezone = \"UTC\"\ninput_price_per_million = 0.66\n",
		"[[models]]\nid = \"ollama/qwen3:8b\"\nfamily = \"qwen3\"\nprovider_id = \"ollama\"\nmodel_name = \"qwen3:8b\"\nlocation = \"local\"\nsource = \"curated\"\ntags = []\n",
	} {
		if !strings.Contains(text, snippet) {
			t.Errorf("registry.toml lacks:\n%s\n\nfile:\n%s", snippet, text)
		}
	}
	if strings.Contains(text, "retired:cloud") {
		t.Error("ollama/retired:cloud is still in the registry")
	}

	// The mirror is now in step: the same page plans nothing and asks nothing.
	ollama.tags = []string{"deepseek-v4-pro:cloud", "qwen3:8b", "glm-5.3:cloud", "gemma4:cloud", "kimi-k3:1t-cloud", "gpt-oss:120b-cloud"}
	ollama.calls, order = nil, nil
	before := len(fetches.all())
	stdout, stderr, code = runCS(t, cfg, cloudSyncOpts{catalog: true, yes: true})
	if code != 0 || stderr != "" || !strings.Contains(stdout, "catalog: Unchanged prices: 5\n") || len(ollama.changes()) != 0 || len(order) != 0 || mustRead(t, path) != text {
		t.Errorf("a second run was not a no-op: exit %d, stderr %q, ollama %q, syncs %q\n%s", code, stderr, ollama.changes(), order, stdout)
	}
	// Every tag is now pulled and recorded, so none is looked up again: the
	// second run fetched the pricing page and nothing else.
	if again := fetches.all()[before:]; !reflect.DeepEqual(again, []string{cloudsync.PricingURL}) {
		t.Errorf("the second run fetched %v, want only the pricing page", again)
	}
}

// TestCloudSyncCatalogChangesNothingAndSaysWhy walks every way the catalog
// flow stops before changing anything, and pins the exit code a caller (the
// cloud-sync skill) branches on: 2 for an input that could not be read, 3
// for a page that changed shape (with its HTML saved for the repair), 4 for a
// mass removal, 5 for removals nobody approved. In each the registry is
// byte-identical, ollama was asked for nothing but its list, and the
// command's own error (the last line main prints) says the catalog flow
// changed nothing and why.
func TestCloudSyncCatalogChangesNothingAndSaysWhy(t *testing.T) {
	massRegistry := cloudSyncRegistry + `
[[models]]
id = "ollama/retired2:cloud"
family = "retired"
provider_id = "ollama"
model_name = "retired2:cloud"
location = "cloud"
source = "curated"
tags = []
`
	// A page of bare names only, with ollama.com/library unreachable.
	allBare := map[string]string{cloudsync.PricingURL: strings.Replace(catalogPage, ">gpt-oss:120b<", ">gpt-oss<", 1)}
	cases := []struct {
		name     string
		registry string
		pages    map[string]string
		listFail string
		opts     cloudSyncOpts
		code     int
		stderr   string
	}{
		{name: "the page cannot be fetched", pages: map[string]string{}, opts: cloudSyncOpts{yes: true}, code: 2,
			stderr: "catalog: error: could not fetch ollama.com/pricing: HTTP 404; nothing was changed\n"},
		{name: "--html names a file that is not there", pages: catalogPages(), opts: cloudSyncOpts{yes: true, htmlFile: "/nonexistent/page.html"}, code: 2,
			stderr: "catalog: error: cannot read /nonexistent/page.html: open /nonexistent/page.html: no such file or directory; nothing was changed\n"},
		{name: "ollama list fails", pages: catalogPages(), listFail: "Error: could not connect to ollama server", opts: cloudSyncOpts{yes: true}, code: 2,
			stderr: "catalog: error: could not run `ollama list` against " + testOllamaOrigin + " (is the ollama daemon up?): Error: could not connect to ollama server; nothing was changed\n"},
		{name: "no cloud tag resolves", pages: allBare, opts: cloudSyncOpts{yes: true}, code: 2,
			stderr: "catalog:   deepseek-v4-pro: could not read its ollama.com/library tags (HTTP 404); skipped\n" +
				"catalog:   glm-5.3: could not read its ollama.com/library tags (HTTP 404); skipped\n" +
				"catalog:   gemma4: could not read its ollama.com/library tags (HTTP 404); skipped\n" +
				"catalog:   kimi-k3: could not read its ollama.com/library tags (HTTP 404); skipped\n" +
				"catalog:   gpt-oss: could not read its ollama.com/library tags (HTTP 404); skipped\n" +
				"catalog: error: could not resolve a cloud tag for any model on ollama.com/library; nothing was changed\n"},
		{name: "the page changed shape", pages: map[string]string{cloudsync.PricingURL: "<html><p>pricing moved</p></html>"}, opts: cloudSyncOpts{yes: true}, code: 3,
			stderr: "catalog: error: could not parse ollama.com/pricing: no <table> found on the page\n" +
				"catalog: raw HTML saved to TMPDIR/ollama-pricing-20261007-090000.html — the parser to update is wt/internal/cloudsync/pricingpage.go; nothing was changed\n"},
		{name: "more than half the cloud entries would go", registry: massRegistry, pages: catalogPages(), opts: cloudSyncOpts{yes: true, approve: "anything"}, code: 4,
			stderr: "catalog: error: 2 of 3 ollama cloud entries would be removed — check the page parsed correctly, then re-run with --force. Nothing was changed for the catalog.\n"},
		{name: "--yes with no digest", pages: catalogPages(), opts: cloudSyncOpts{yes: true}, code: 5,
			stderr: "catalog: error: the plan deletes models — review a --dry-run, then re-run with `--yes --approve-removals DIGEST`. Nothing was changed for the catalog.\n"},
		{name: "--yes with another plan's digest", pages: catalogPages(), opts: cloudSyncOpts{yes: true, approve: "000000000000"}, code: 5,
			stderr: "catalog: error: the removals are not the ones digest 000000000000 approved — review a --dry-run, then re-run with `--yes --approve-removals DIGEST`. Nothing was changed for the catalog.\n"},
		{name: "--force does not stand in for the digest", registry: massRegistry, pages: catalogPages(), opts: cloudSyncOpts{yes: true, force: true}, code: 5,
			stderr: "catalog: error: the plan deletes models — review a --dry-run, then re-run with `--yes --approve-removals DIGEST`. Nothing was changed for the catalog.\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			registry := tc.registry
			if registry == "" {
				registry = cloudSyncRegistry
			}
			path, cfg := cloudSyncHome(t, registry)
			stubCloudFetch(t, tc.pages)
			ollama := stubOllama(t, catalogPulled...)
			if tc.listFail != "" {
				ollama.fail["list"] = tc.listFail
			}
			synced := stubRouteSync(t, "")
			tc.opts.catalog = true

			stdout, stderr, final, code := runCSFinal(t, cfg, tc.opts)
			if want := "cloud-sync: the catalog flow changed nothing: " + map[int]string{
				2: "an input could not be read", 3: "the pricing page changed shape", 4: "mass removal refused", 5: "removals not approved",
			}[tc.code]; final != want {
				t.Errorf("the command's error = %q, want %q", final, want)
			}
			want := strings.ReplaceAll(tc.stderr, "TMPDIR", os.TempDir())
			if strings.Contains(want, "DIGEST") {
				want = strings.ReplaceAll(want, "DIGEST", digestIn(t, stdout))
			}
			if code != tc.code || stderr != want {
				t.Errorf("exit %d, stderr:\n%s\nwant exit %d, stderr:\n%s", code, stderr, tc.code, want)
			}
			if mustRead(t, path) != registry || len(ollama.changes()) != 0 || *synced != 0 {
				t.Errorf("the refused run changed something: ollama %q, syncs %d", ollama.changes(), *synced)
			}
			if tc.code == 3 {
				saved := filepath.Join(os.TempDir(), "ollama-pricing-20261007-090000.html")
				if got := mustRead(t, saved); got != tc.pages[cloudsync.PricingURL] {
					t.Errorf("the saved page is not the page that was served: %q", got)
				}
			}
		})
	}
}

// TestCloudSyncCatalogWithTheLibraryDown pins the run where ollama.com's
// pricing page answers and its library pages do not. A name that pins its
// size still resolves, so the flow goes on, and every model whose tag could
// not be read is treated as still on the page: its entry keeps getting
// prices, nothing is added or pulled under a guessed tag, and nothing that
// may be it is removed. An outage must never be read as "these models are
// gone".
func TestCloudSyncCatalogWithTheLibraryDown(t *testing.T) {
	path, cfg := cloudSyncHome(t, cloudSyncRegistry)
	stubCloudFetch(t, map[string]string{cloudsync.PricingURL: catalogPage})
	stubOllama(t, "deepseek-v4-pro:cloud", "glm-5.3:9b-cloud", "stray:cloud")

	stdout, stderr, code := runCS(t, cfg, cloudSyncOpts{catalog: true, dryRun: true})
	if code != 0 || stderr != "" {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	for _, line := range []string{
		"catalog: Price updates (1):\ncatalog:   ollama/deepseek-v4-pro:cloud: ",
		"catalog: Registry additions (1):\ncatalog:   ollama/gpt-oss:120b-cloud ",
		"catalog: ollama pull (1):\ncatalog:   ollama/gpt-oss:120b-cloud\n",
		// retired is on no page and still goes; glm-5.3's pulled stub, whose
		// name is on the page, is not a stray.
		"`ollama rm` if pulled (1):\ncatalog:   ollama/retired:cloud\n",
		"off the page (1):\ncatalog:   stray:cloud\n",
		"catalog: warning: glm-5.3: could not read its ollama.com/library tags (HTTP 404); skipped\n",
	} {
		if !strings.Contains(stdout, line) {
			t.Errorf("the plan lacks %q:\n%s", line, stdout)
		}
	}
	if mustRead(t, path) != cloudSyncRegistry {
		t.Error("a dry run changed the registry")
	}
}

// TestCloudSyncCatalogForceAppliesAMassRemoval pins the way through exit 4:
// with --force and the digest, the plan that removes most cloud entries is
// applied. --force is an answer to one question only (is this many removals
// right?) and the digest is still needed.
func TestCloudSyncCatalogForceAppliesAMassRemoval(t *testing.T) {
	registry := strings.Replace(cloudSyncRegistry, "id = \"ollama/deepseek-v4-pro:cloud\"", "id = \"ollama/also-retired:cloud\"", 1)
	registry = strings.Replace(registry, "model_name = \"deepseek-v4-pro:cloud\"", "model_name = \"also-retired:cloud\"", 1)
	path, cfg := cloudSyncHome(t, registry)
	stubCloudFetch(t, catalogPages())
	ollama := stubOllama(t)
	stubRouteSync(t, "")

	dry, _, code := runCS(t, cfg, cloudSyncOpts{catalog: true, dryRun: true})
	if code != 0 {
		t.Fatalf("a dry run of a mass removal exits %d, want 0: it only prints", code)
	}
	_, stderr, code := runCS(t, cfg, cloudSyncOpts{catalog: true, yes: true, force: true, approve: digestIn(t, dry)})
	if code != 0 || stderr != "" {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if text := mustRead(t, path); strings.Contains(text, "retired") || !strings.Contains(text, "ollama/deepseek-v4-pro:cloud") {
		t.Errorf("the forced plan was not applied:\n%s", text)
	}
	// Neither removed entry was pulled, so there is nothing to rm.
	if got := ollama.changes(); len(got) != 5 || strings.Contains(strings.Join(got, " "), "rm ") {
		t.Errorf("ollama commands = %q, want the five pulls and no rm", got)
	}
}

// TestCloudSyncFlowsAreIndependent pins that neither flow's trouble stops
// the other, and that a catalog code wins the exit status: the catalog is
// refused for want of a digest (5) while the prices flow, in the same run,
// is applied and its routes synced; and when the prices fetch fails (1) in a
// run whose catalog is refused, the exit status is still the catalog's.
func TestCloudSyncFlowsAreIndependent(t *testing.T) {
	path, cfg := cloudSyncHome(t, cloudSyncRegistry)
	pages := catalogPages()
	pages[cloudsync.OpenRouterModelsURL] = openRouterBody
	stubCloudFetch(t, pages)
	ollama := stubOllama(t, catalogPulled...)
	synced := stubRouteSync(t, "")

	stdout, _, code := runCS(t, cfg, cloudSyncOpts{prices: true, catalog: true, yes: true})
	if code != 5 || !strings.HasSuffix(stdout, "prices: refreshed 1 model(s); 1 price(s) changed\n") {
		t.Errorf("exit %d, want 5 with the prices applied; stdout ends %q", code, stdout[max(0, len(stdout)-80):])
	}
	text := mustRead(t, path)
	if !strings.Contains(text, "input_price_per_million = 3.0") || !strings.Contains(text, "ollama/retired:cloud") || strings.Contains(text, "glm-5.3") {
		t.Errorf("want the OpenRouter price applied and the catalog untouched:\n%s", text)
	}
	if *synced != 1 || len(ollama.changes()) != 0 {
		t.Errorf("syncs = %d, ollama = %q; want one sync for the price and no ollama command", *synced, ollama.changes())
	}
	if strings.Index(stdout, "prices: openrouter.ai:") > strings.Index(stdout, "catalog: ollama.com/pricing:") {
		t.Error("the plans are not printed prices first, then catalog")
	}

	delete(pages, cloudsync.OpenRouterModelsURL)
	_, stderr, code := runCS(t, cfg, cloudSyncOpts{prices: true, catalog: true, yes: true})
	if code != 5 || !strings.HasPrefix(stderr, "prices: error: could not read OpenRouter's prices: HTTP 404") {
		t.Errorf("exit %d, stderr %q; want the catalog's 5 over the prices flow's 1", code, stderr)
	}
}

// TestCloudSyncCatalogRefusesAPlanThatChangedUnderTheLock pins the last
// gate. The plan is made again under the registry lock, and if it is no
// longer the plan that was printed and approved (here another program adds a
// cloud entry, which the re-plan would remove, while the user reads the
// question) nothing is applied and the exit status is 5. Without this a
// model nobody saw in the plan could be removed from the registry and from
// ollama.
func TestCloudSyncCatalogRefusesAPlanThatChangedUnderTheLock(t *testing.T) {
	path, cfg := cloudSyncHome(t, cloudSyncRegistry)
	stubCloudFetch(t, catalogPages())
	ollama := stubOllama(t, catalogPulled...)
	synced := stubRouteSync(t, "")
	raced := cloudSyncRegistry + `
[[models]]
id = "ollama/late:cloud"
family = "late"
provider_id = "ollama"
model_name = "late:cloud"
location = "cloud"
`
	stubConfirm(t, true, nil, func() {
		if err := os.WriteFile(path, []byte(raced), 0o600); err != nil {
			t.Fatal(err)
		}
	})

	_, stderr, code := runCS(t, cfg, cloudSyncOpts{catalog: true})
	want := "catalog: error: the registry changed after the plan was printed, so this is no longer the plan that was approved; nothing was changed — run it again\n"
	if code != 5 || stderr != want {
		t.Errorf("exit %d, stderr %q\nwant exit 5, stderr %q", code, stderr, want)
	}
	if mustRead(t, path) != raced || len(ollama.changes()) != 0 || *synced != 0 {
		t.Errorf("the refused run changed something: ollama %q, syncs %d", ollama.changes(), *synced)
	}

	// A change that leaves the plan as it was (a local model added) is not a
	// reason to refuse, and the other program's row survives the write.
	path, cfg = cloudSyncHome(t, cloudSyncRegistry)
	harmless := cloudSyncRegistry + "\n[[models]]\nid = \"ollama/late:7b\"\nfamily = \"late\"\nprovider_id = \"ollama\"\nmodel_name = \"late:7b\"\nlocation = \"local\"\n"
	stubConfirm(t, true, nil, func() {
		if err := os.WriteFile(path, []byte(harmless), 0o600); err != nil {
			t.Fatal(err)
		}
	})
	if _, stderr, code = runCS(t, cfg, cloudSyncOpts{catalog: true}); code != 0 || stderr != "" {
		t.Fatalf("with an unrelated row added: exit %d, stderr %q", code, stderr)
	}
	if text := mustRead(t, path); !strings.Contains(text, "ollama/late:7b") || !strings.Contains(text, "ollama/glm-5.3:cloud") || strings.Contains(text, "retired") {
		t.Errorf("want the plan applied and the other program's row kept:\n%s", text)
	}
}

// TestCloudSyncCatalogOllamaFailuresExit1AndTheRestStillRuns pins what a
// failed pull or rm costs: an error line and exit 1, with every other
// command still run and the routes still synced. The registry is already
// written by then, so stopping at the first failure would leave more out of
// step, not less; the next run redoes only what is still missing. A tag that
// is already gone ("not found") is not a failure. A tag whose rm failed is
// named as left behind, with the way back in: a new dry run, because the
// digest that approved this run may not be the next run's.
func TestCloudSyncCatalogOllamaFailuresExit1AndTheRestStillRuns(t *testing.T) {
	_, cfg := cloudSyncHome(t, cloudSyncRegistry)
	stubCloudFetch(t, catalogPages())
	ollama := stubOllama(t, catalogPulled...)
	ollama.fail["pull gemma4:cloud"] = "Error: pull model manifest: 401 unauthorized"
	ollama.fail["rm retired:cloud"] = "Error: model 'retired:cloud' not found"
	ollama.fail["rm stray:cloud"] = "Error: permission denied"
	synced := stubRouteSync(t, "")
	stubConfirm(t, true, nil, nil)

	stdout, stderr, code := runCS(t, cfg, cloudSyncOpts{catalog: true})
	wantErr := "catalog: error: `ollama pull gemma4:cloud` failed: Error: pull model manifest: 401 unauthorized\n" +
		"catalog: error: `ollama rm stray:cloud` failed: Error: permission denied\n" +
		"catalog:   stray:cloud is left pulled; the next run lists it as a stray tag — start again from --dry-run, since the removal digest may have changed\n"
	if code != 1 || stderr != wantErr {
		t.Errorf("exit %d, stderr:\n%s\nwant exit 1, stderr:\n%s", code, stderr, wantErr)
	}
	for _, line := range []string{"catalog: pulled glm-5.3:cloud\n", "catalog: pulled gpt-oss:120b-cloud\n", "catalog: removed retired:cloud\n"} {
		if !strings.Contains(stdout, line) {
			t.Errorf("stdout lacks %q:\n%s", line, stdout)
		}
	}
	if got := ollama.changes(); len(got) != 6 || *synced != 1 {
		t.Errorf("ollama commands = %q, syncs = %d; want all six commands attempted and one sync", got, *synced)
	}
}

// noOllamaRegistry is a registry that does not use ollama: the openrouter
// provider and its one model.
var noOllamaRegistry = cloudSyncRegistry[strings.Index(cloudSyncRegistry, "[[providers]]\nid = \"openrouter\""):strings.Index(cloudSyncRegistry, "[[models]]\nid = \"ollama/deepseek-v4-pro:cloud\"")]

// TestCloudSyncSkipsTheCatalogOnARegistryWithoutOllama pins the plain
// `wt cloud-sync` on a machine that has no ollama provider row, which is
// every machine that uses only OpenRouter, and the command the stale-price
// notice tells its user to run. There is no catalog to mirror, so the
// catalog flow says so and is skipped: the prices are refreshed, nothing is
// fetched from ollama.com, no ollama command runs, and the exit status is 0.
// A run that "failed" every time for want of a provider nobody uses would
// teach people to ignore the exit status.
func TestCloudSyncSkipsTheCatalogOnARegistryWithoutOllama(t *testing.T) {
	path, cfg := cloudSyncHome(t, noOllamaRegistry)
	got := stubCloudFetch(t, bothPages())
	ollama := stubOllama(t)
	synced := stubRouteSync(t, "")

	stdout, stderr, code := runCS(t, cfg, cloudSyncOpts{prices: true, catalog: true, yes: true})
	wantTail := "catalog: no ollama provider in the registry; nothing to mirror\n" +
		"prices: refreshed 1 model(s); 1 price(s) changed\n"
	if code != 0 || stderr != "" || !strings.HasSuffix(stdout, wantTail) {
		t.Errorf("exit %d, stderr %q, stdout:\n%s\nwant exit 0 and stdout ending:\n%s", code, stderr, stdout, wantTail)
	}
	if !strings.Contains(mustRead(t, path), "input_price_per_million = 3.0") || *synced != 1 {
		t.Errorf("the prices flow was not applied (syncs = %d)", *synced)
	}
	if urls := got.all(); !reflect.DeepEqual(urls, []string{cloudsync.OpenRouterModelsURL}) || len(ollama.calls) != 0 {
		t.Errorf("fetched %v and ran %q; want OpenRouter's list only and no ollama command", urls, ollama.calls)
	}
}

// TestCloudSyncCatalogNeedsAnOllamaProviderRow pins the same registry when
// the catalog flow was asked for by name (--only catalog, or one of its
// flags): now the run cannot do what was asked, so it is an error, exit 1,
// before anything is fetched. A new entry would name a provider that has no
// row, and wt refuses to load such a registry at all. The message says how
// the row comes to exist; `wt model init` alone adds it only on a machine
// that has ollama.
func TestCloudSyncCatalogNeedsAnOllamaProviderRow(t *testing.T) {
	path, cfg := cloudSyncHome(t, noOllamaRegistry)
	got := stubCloudFetch(t, bothPages())
	ollama := stubOllama(t)
	const refusal = "catalog: error: registry.toml has no ollama provider row, and every catalog entry needs one — add the row (`wt model init` adds it once `ollama` is on PATH), then run this again; nothing was changed\n"

	_, stderr, code := runCS(t, cfg, cloudSyncOpts{catalog: true, catalogNamed: true, yes: true})
	if code != 1 || stderr != refusal {
		t.Errorf("exit %d, stderr %q\nwant exit 1, stderr %q", code, stderr, refusal)
	}

	// The command line decides "by name": --only naming the flow, or any
	// flag that belongs to it. Plain `wt cloud-sync` does not.
	for _, tc := range []struct {
		args []string
		code int
	}{
		{[]string{"--dry-run"}, 0},
		{[]string{"--dry-run", "--only", "catalog"}, 1},
		{[]string{"--dry-run", "--only", "prices,catalog"}, 1},
		{[]string{"--dry-run", "--force"}, 1},
		{[]string{"--dry-run", "--html", "x.html"}, 1},
		{[]string{"--dry-run", "--approve-removals", "abc"}, 1},
	} {
		cmd := cloudSyncCmd(&app{cfg: cfg})
		var out, errOut bytes.Buffer
		cmd.SetArgs(tc.args)
		cmd.SetOut(&out)
		cmd.SetErr(&errOut)
		code := 0
		if err := cmd.Execute(); err != nil {
			code = exitCodeOf(err)
		}
		if code != tc.code {
			t.Errorf("wt cloud-sync %v: exit %d, want %d; stderr %q", tc.args, code, tc.code, errOut.String())
		} else if tc.code == 1 && errOut.String() != refusal {
			t.Errorf("wt cloud-sync %v: stderr %q, want the refusal", tc.args, errOut.String())
		} else if tc.code == 0 && !strings.Contains(out.String(), "catalog: no ollama provider in the registry; nothing to mirror\n") {
			t.Errorf("wt cloud-sync %v: stdout %q lacks the nothing-to-mirror line", tc.args, out.String())
		}
	}
	for _, url := range got.all() {
		if url != cloudsync.OpenRouterModelsURL {
			t.Errorf("the refused catalog flow fetched %s", url)
		}
	}
	if len(ollama.calls) != 0 || mustRead(t, path) != noOllamaRegistry {
		t.Errorf("the refused flow ran %q or changed the registry", ollama.calls)
	}
}

// TestCloudSyncBothFlowsInOneRun pins the default invocation, the one the
// spec's sequence describes: both plans printed, one question for the two of
// them, both applied to the registry, the catalog's ollama work, and one
// route sync at the end. Run with a terminal and no --yes, so the question
// is the gate (the digest is for the run nobody watches).
func TestCloudSyncBothFlowsInOneRun(t *testing.T) {
	both := cloudSyncOpts{prices: true, catalog: true}
	start := func(t *testing.T, pages map[string]string) (string, *config.Config, *fakeOllama, *int) {
		path, cfg := cloudSyncHome(t, cloudSyncRegistry)
		stubCloudFetch(t, pages)
		return path, cfg, stubOllama(t, catalogPulled...), stubRouteSync(t, "")
	}

	t.Run("approved: one question, both applied, one sync", func(t *testing.T) {
		path, cfg, ollama, synced := start(t, bothPages())
		asked := stubConfirm(t, true, nil, nil)
		stdout, stderr, code := runCS(t, cfg, both)
		if code != 0 || stderr != "" || *asked != 1 || *synced != 1 {
			t.Fatalf("exit %d, stderr %q, asked %d, syncs %d; want exit 0, one question, one sync\n%s", code, stderr, *asked, *synced, stdout)
		}
		// Both plans come before either result.
		plans := strings.Index(stdout, "catalog: ollama.com/pricing:")
		results := strings.Index(stdout, "prices: refreshed 1 model(s); 1 price(s) changed\ncatalog: updated 1, added 4 and removed 1 model(s)\n")
		if strings.Index(stdout, "prices: openrouter.ai:") != 0 || plans < 0 || results < plans {
			t.Errorf("want the prices plan, the catalog plan, then the two result lines:\n%s", stdout)
		}
		text := mustRead(t, path)
		if !strings.Contains(text, "input_price_per_million = 3.0") || !strings.Contains(text, "ollama/glm-5.3:cloud") || strings.Contains(text, "retired:cloud") {
			t.Errorf("want the OpenRouter price and the catalog's additions and removal in one registry:\n%s", text)
		}
		if got := ollama.changes(); len(got) != 6 {
			t.Errorf("ollama commands = %q, want the four pulls and the two removals", got)
		}
	})

	t.Run("declined: both flows say so, nothing changes, exit 0", func(t *testing.T) {
		path, cfg, ollama, synced := start(t, bothPages())
		stubConfirm(t, false, nil, nil)
		stdout, stderr, code := runCS(t, cfg, both)
		if code != 0 || stderr != "" || !strings.HasSuffix(stdout, "prices: not applied (declined)\ncatalog: not applied (declined)\n") {
			t.Errorf("exit %d, stderr %q, stdout ends %q", code, stderr, stdout[max(0, len(stdout)-90):])
		}
		if mustRead(t, path) != cloudSyncRegistry || len(ollama.changes()) != 0 || *synced != 0 {
			t.Errorf("a declined run changed something: ollama %q, syncs %d", ollama.changes(), *synced)
		}
	})

	t.Run("no terminal: both flows say so, nothing changes, exit 1", func(t *testing.T) {
		path, cfg, ollama, synced := start(t, bothPages())
		noTerminal(t)
		_, stderr, code := runCS(t, cfg, both)
		want := "prices: error: not applied: there is no terminal to confirm on — rerun with --yes to apply without asking\n" +
			"catalog: error: not applied: there is no terminal to confirm on — rerun with --yes to apply without asking\n"
		if code != 1 || stderr != want {
			t.Errorf("exit %d, stderr %q\nwant exit 1, stderr %q", code, stderr, want)
		}
		if mustRead(t, path) != cloudSyncRegistry || len(ollama.changes()) != 0 || *synced != 0 {
			t.Errorf("an unconfirmed run changed something: ollama %q, syncs %d", ollama.changes(), *synced)
		}
	})

	t.Run("the prices plan went stale, the catalog is applied: exit 1", func(t *testing.T) {
		path, cfg, ollama, synced := start(t, bothPages())
		stubConfirm(t, true, nil, func() {
			raced := strings.Replace(cloudSyncRegistry, "input_price_per_million = 2.5", "input_price_per_million = 7", 1)
			if err := os.WriteFile(path, []byte(raced), 0o600); err != nil {
				t.Fatal(err)
			}
		})
		stdout, stderr, code := runCS(t, cfg, both)
		if want := "prices: error: the registry changed after the plan was printed; no price was changed — run it again\n"; code != 1 || stderr != want {
			t.Errorf("exit %d, stderr %q\nwant exit 1, stderr %q", code, stderr, want)
		}
		text := mustRead(t, path)
		if !strings.Contains(stdout, "catalog: updated 1, added 4 and removed 1 model(s)\n") || !strings.Contains(text, "ollama/glm-5.3:cloud") || !strings.Contains(text, "input_price_per_million = 7\n") {
			t.Errorf("want the catalog applied and the other writer's price kept:\n%s\n%s", stdout, text)
		}
		if len(ollama.changes()) != 6 || *synced != 1 {
			t.Errorf("ollama %q, syncs %d; want the catalog's six commands and one sync", ollama.changes(), *synced)
		}
	})

	t.Run("the catalog plan went stale, the prices are applied: exit 5", func(t *testing.T) {
		path, cfg, ollama, synced := start(t, bothPages())
		stubConfirm(t, true, nil, func() {
			raced := cloudSyncRegistry + "\n[[models]]\nid = \"ollama/late:cloud\"\nfamily = \"late\"\nprovider_id = \"ollama\"\nmodel_name = \"late:cloud\"\nlocation = \"cloud\"\n"
			if err := os.WriteFile(path, []byte(raced), 0o600); err != nil {
				t.Fatal(err)
			}
		})
		stdout, stderr, code := runCS(t, cfg, both)
		if code != 5 || !strings.HasPrefix(stderr, "catalog: error: the registry changed after the plan was printed") {
			t.Errorf("exit %d, stderr %q; want the catalog's 5", code, stderr)
		}
		text := mustRead(t, path)
		if !strings.Contains(stdout, "prices: refreshed 1 model(s); 1 price(s) changed\n") || !strings.Contains(text, "input_price_per_million = 3.0") ||
			!strings.Contains(text, "ollama/late:cloud") || !strings.Contains(text, "ollama/retired:cloud") || strings.Contains(text, "glm-5.3") {
			t.Errorf("want the price applied, the other writer's row kept and the catalog untouched:\n%s\n%s", stdout, text)
		}
		if len(ollama.changes()) != 0 || *synced != 1 {
			t.Errorf("ollama %q, syncs %d; want no ollama command and one sync for the price", ollama.changes(), *synced)
		}
	})

	t.Run("OpenRouter is down, the catalog is applied: exit 1", func(t *testing.T) {
		path, cfg, ollama, synced := start(t, catalogPages())
		asked := stubConfirm(t, true, nil, nil)
		stdout, stderr, code := runCS(t, cfg, both)
		if want := "prices: error: could not read OpenRouter's prices: HTTP 404; no price was changed\n"; code != 1 || stderr != want || *asked != 1 {
			t.Errorf("exit %d, asked %d, stderr %q\nwant exit 1, one question, stderr %q", code, *asked, stderr, want)
		}
		text := mustRead(t, path)
		if !strings.Contains(stdout, "catalog: updated 1, added 4 and removed 1 model(s)\n") || !strings.Contains(text, "ollama/glm-5.3:cloud") || !strings.Contains(text, "input_price_per_million = 2.5\n") {
			t.Errorf("want the catalog applied and the OpenRouter price as it was:\n%s\n%s", stdout, text)
		}
		if len(ollama.changes()) != 6 || *synced != 1 {
			t.Errorf("ollama %q, syncs %d; want the catalog's six commands and one sync", ollama.changes(), *synced)
		}
	})
}

// TestCloudSyncCatalogDeclinedChangesNothing pins a "no" to a catalog plan
// that deletes: the registry is byte-identical, ollama is asked for nothing
// but its list, the routes are not synced, and the exit status is 0 (the
// user's answer is not a failure).
func TestCloudSyncCatalogDeclinedChangesNothing(t *testing.T) {
	path, cfg := cloudSyncHome(t, cloudSyncRegistry)
	stubCloudFetch(t, catalogPages())
	ollama := stubOllama(t, catalogPulled...)
	synced := stubRouteSync(t, "")
	asked := stubConfirm(t, false, nil, nil)

	stdout, stderr, code := runCS(t, cfg, cloudSyncOpts{catalog: true})
	if *asked != 1 || code != 0 || stderr != "" || !strings.HasSuffix(stdout, "catalog: not applied (declined)\n") {
		t.Errorf("asked %d, exit %d, stderr %q, stdout ends %q", *asked, code, stderr, stdout[max(0, len(stdout)-60):])
	}
	if mustRead(t, path) != cloudSyncRegistry || len(ollama.changes()) != 0 || *synced != 0 {
		t.Errorf("a declined run changed something: ollama %q, syncs %d", ollama.changes(), *synced)
	}
}

// TestCloudSyncCatalogAsksWhenADigestComesWithoutYes pins
// --approve-removals without --yes: the digest is ignored (even a wrong
// one), the question is asked, and the answer decides. The digest exists
// for the run nobody is watching; with a person at the terminal, the person
// is the gate.
func TestCloudSyncCatalogAsksWhenADigestComesWithoutYes(t *testing.T) {
	path, cfg := cloudSyncHome(t, cloudSyncRegistry)
	stubCloudFetch(t, catalogPages())
	stubOllama(t, catalogPulled...)
	stubRouteSync(t, "")
	asked := stubConfirm(t, true, nil, nil)

	_, stderr, code := runCS(t, cfg, cloudSyncOpts{catalog: true, approve: "000000000000"})
	if *asked != 1 || code != 0 || stderr != "" {
		t.Fatalf("asked %d, exit %d, stderr %q; want one question and the plan applied", *asked, code, stderr)
	}
	if text := mustRead(t, path); !strings.Contains(text, "ollama/glm-5.3:cloud") || strings.Contains(text, "retired:cloud") {
		t.Errorf("the approved plan was not applied:\n%s", text)
	}
}

// TestCloudSyncFinishesAnInterruptedRun pins the run after one that died
// between its registry write and its route sync (Ctrl-C during a slow pull,
// a crash). The registry already holds the new entries, so this run plans
// no registry change at all, only the pulls and removals still owed. It
// must still sync the routes: nothing else will, and without it LiteLLM
// keeps the old prices and lacks the new models until some other command
// happens to sync.
func TestCloudSyncFinishesAnInterruptedRun(t *testing.T) {
	path, cfg := cloudSyncHome(t, cloudSyncRegistry)
	stubCloudFetch(t, catalogPages())
	ollama := stubOllama(t, catalogPulled...)
	stubRouteSync(t, "")
	stubConfirm(t, true, nil, nil)
	if _, stderr, code := runCS(t, cfg, cloudSyncOpts{catalog: true}); code != 0 {
		t.Fatalf("the first run: exit %d, stderr %q", code, stderr)
	}
	written := mustRead(t, path)

	// As if that run had died before any ollama command and before its
	// sync: ollama is as it was, the registry is as the run left it.
	ollama.calls = nil
	synced := stubRouteSync(t, "")
	stdout, stderr, code := runCS(t, cfg, cloudSyncOpts{catalog: true})
	if code != 0 || stderr != "" || !strings.Contains(stdout, "catalog: updated 0, added 0 and removed 0 model(s)\n") {
		t.Fatalf("exit %d, stderr %q\n%s", code, stderr, stdout)
	}
	want := []string{"pull glm-5.3:cloud", "pull gemma4:cloud", "pull kimi-k3:1t-cloud", "pull gpt-oss:120b-cloud", "rm retired:cloud", "rm stray:cloud"}
	if got := ollama.changes(); !reflect.DeepEqual(got, want) {
		t.Errorf("ollama commands = %q\nwant            %q", got, want)
	}
	if *synced != 1 {
		t.Errorf("the finishing run synced the routes %d time(s), want once", *synced)
	}
	if mustRead(t, path) != written {
		t.Error("the finishing run rewrote a registry that was already in step")
	}
}

// TestCloudSyncWritesEachFlowAloneWhenARowWouldNotLoad pins what a broken
// row costs: the flow that must change it, and only that flow. The shared
// registry write is refused whole when a row it touches would not load
// (here a hand edit removed a family); the command then writes each flow on
// its own, so a broken ollama entry does not keep OpenRouter prices stale,
// nor the reverse. The broken flow's error names the row, and the exit
// status is 1.
func TestCloudSyncWritesEachFlowAloneWhenARowWouldNotLoad(t *testing.T) {
	both := cloudSyncOpts{prices: true, catalog: true}

	t.Run("a broken ollama entry: prices applied, catalog not", func(t *testing.T) {
		broken := strings.Replace(cloudSyncRegistry, "family = \"deepseek\"\n", "", 1)
		path, cfg := cloudSyncHome(t, broken)
		stubCloudFetch(t, bothPages())
		ollama := stubOllama(t, catalogPulled...)
		synced := stubRouteSync(t, "")
		stubConfirm(t, true, nil, nil)

		stdout, stderr, code := runCS(t, cfg, both)
		want := "catalog: error: the catalog's changes were not written: invalid registry entry: model \"ollama/deepseek-v4-pro:cloud\": family is required\n"
		if code != 1 || stderr != want {
			t.Errorf("exit %d, stderr %q\nwant exit 1, stderr %q", code, stderr, want)
		}
		text := mustRead(t, path)
		if !strings.HasSuffix(stdout, "prices: refreshed 1 model(s); 1 price(s) changed\n") || !strings.Contains(text, "input_price_per_million = 3.0") ||
			strings.Contains(text, "glm-5.3") || !strings.Contains(text, "ollama/retired:cloud") {
			t.Errorf("want the price written and no catalog change:\n%s\n%s", stdout, text)
		}
		if len(ollama.changes()) != 0 || *synced != 1 {
			t.Errorf("ollama %q, syncs %d; want no ollama command and one sync for the price", ollama.changes(), *synced)
		}
	})

	t.Run("a broken OpenRouter model: catalog applied, prices not", func(t *testing.T) {
		broken := strings.Replace(cloudSyncRegistry, "family = \"gpt\"\n", "", 1)
		path, cfg := cloudSyncHome(t, broken)
		stubCloudFetch(t, bothPages())
		ollama := stubOllama(t, catalogPulled...)
		synced := stubRouteSync(t, "")
		stubConfirm(t, true, nil, nil)

		stdout, stderr, code := runCS(t, cfg, both)
		want := "prices: error: the price changes were not written: invalid registry entry: model \"openrouter/vendor--gpt\": family is required\n"
		if code != 1 || stderr != want {
			t.Errorf("exit %d, stderr %q\nwant exit 1, stderr %q", code, stderr, want)
		}
		text := mustRead(t, path)
		if !strings.Contains(stdout, "catalog: updated 1, added 4 and removed 1 model(s)\n") || !strings.Contains(text, "ollama/glm-5.3:cloud") ||
			!strings.Contains(text, "input_price_per_million = 2.5\n") {
			t.Errorf("want the catalog written and the OpenRouter price as it was:\n%s\n%s", stdout, text)
		}
		if len(ollama.changes()) != 6 || *synced != 1 {
			t.Errorf("ollama %q, syncs %d; want the catalog's six commands and one sync", ollama.changes(), *synced)
		}
	})
}

// TestCloudSyncCatalogReadsASavedPage pins --html: the page comes from the
// file and ollama.com/pricing is not fetched (the library tag lookups still
// are). It is how a repaired parser is tried against the page that broke it.
func TestCloudSyncCatalogReadsASavedPage(t *testing.T) {
	_, cfg := cloudSyncHome(t, cloudSyncRegistry)
	saved := filepath.Join(t.TempDir(), "saved.html")
	if err := os.WriteFile(saved, []byte(catalogPage), 0o600); err != nil {
		t.Fatal(err)
	}
	pages := catalogPages()
	delete(pages, cloudsync.PricingURL)
	got := stubCloudFetch(t, pages)
	stubOllama(t, catalogPulled...)

	stdout, stderr, code := runCS(t, cfg, cloudSyncOpts{catalog: true, dryRun: true, htmlFile: saved})
	if code != 0 || stderr != "" || !strings.HasPrefix(stdout, "catalog: ollama.com/pricing: 5 models") {
		t.Errorf("exit %d, stderr %q, stdout:\n%s", code, stderr, stdout)
	}
	for _, url := range got.all() {
		if url == cloudsync.PricingURL {
			t.Error("--html still fetched ollama.com/pricing")
		}
	}
}
```

In `wt/cmd/wt/cloudsync_test.go`, replace everything from the line that starts `// TestSelectFlows pins --only` up to, but not including, the line that starts `// TestCloudSyncPricesDryRun pins` (63 lines) with:

```go
// TestSelectFlows pins --only: nothing selects every flow, a comma list
// selects those named, and an unknown name is an error that lists the valid
// ones instead of silently running nothing.
func TestSelectFlows(t *testing.T) {
	cases := []struct {
		only            string
		prices, catalog bool
		wantErr         string
	}{
		{"", true, true, ""},
		{"prices", true, false, ""},
		{"catalog", false, true, ""},
		{" catalog , prices ", true, true, ""},
		{"price", false, false, `--only: unknown flow "price" (valid: prices, catalog)`},
		{"prices,", true, false, `--only: unknown flow "" (valid: prices, catalog)`},
	}
	for _, tc := range cases {
		var o cloudSyncOpts
		err := selectFlows(tc.only, &o)
		if tc.wantErr != "" {
			if err == nil || err.Error() != tc.wantErr {
				t.Errorf("selectFlows(%q) err = %v, want %q", tc.only, err, tc.wantErr)
			}
			continue
		}
		if err != nil || o.prices != tc.prices || o.catalog != tc.catalog {
			t.Errorf("selectFlows(%q) = prices %v, catalog %v, err %v", tc.only, o.prices, o.catalog, err)
		}
	}
}

// TestCloudSyncCommandRefusals pins what the command refuses before it does
// anything: a catalog-only flag with the catalog flow left out (the flag
// would be silently ignored, and --force or a digest ignored is a user
// believing a gate was passed), an unknown flow, and a config that could not
// be loaded, which gets the same repair hint every other command gives.
func TestCloudSyncCommandRefusals(t *testing.T) {
	_, cfg := cloudSyncHome(t, cloudSyncRegistry)
	run := func(a *app, args ...string) error {
		cmd := cloudSyncCmd(a)
		cmd.SetArgs(args)
		cmd.SetOut(new(bytes.Buffer))
		cmd.SetErr(new(bytes.Buffer))
		return cmd.Execute()
	}
	ok := &app{cfg: cfg}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--only", "prices", "--force"}, "--force is for the catalog flow, which --only prices leaves out"},
		{[]string{"--only", "prices", "--html", "x.html"}, "--html is for the catalog flow, which --only prices leaves out"},
		{[]string{"--only", "prices", "--approve-removals", "abc"}, "--approve-removals is for the catalog flow, which --only prices leaves out"},
		{[]string{"--only", "routes"}, `--only: unknown flow "routes" (valid: prices, catalog)`},
		{[]string{"extra"}, `unknown command "extra" for "cloud-sync"`},
	} {
		err := run(ok, tc.args...)
		if err == nil || err.Error() != tc.want || exitCodeOf(err) != 1 {
			t.Errorf("wt cloud-sync %v: err = %v (exit %d), want %q and exit 1", tc.args, err, exitCodeOf(err), tc.want)
		}
	}

	broken := &app{cfg: &config.Config{}, loadErr: fmt.Errorf("%w at /x/registry.toml — seed it with `wt model init`", config.ErrRegistryMissing)}
	err := run(broken, "--dry-run")
	if err == nil || !strings.HasPrefix(err.Error(), "config error: model registry not found") || strings.Contains(err.Error(), "wt config") {
		t.Errorf("with an unloadable config: err = %v, want the config error with no second repair", err)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run (from `wt/`): `go test -count=1 ./cmd/wt -run 'CloudSync|SelectFlows'`
Expected:

```text
# github.com/ohanaverse/local-ai-setup/wt/cmd/wt [github.com/ohanaverse/local-ai-setup/wt/cmd/wt.test]
cmd/wt/cloudsync_catalog_test.go:105:54: unknown field catalog in struct literal of type cloudSyncOpts
cmd/wt/cloudsync_catalog_test.go:137:43: unknown field catalog in struct literal of type cloudSyncOpts
cmd/wt/cloudsync_catalog_test.go:139:54: unknown field catalog in struct literal of type cloudSyncOpts
cmd/wt/cloudsync_catalog_test.go:139:80: unknown field approve in struct literal of type cloudSyncOpts
```

- [ ] **Step 3: Write the catalog flow**

Create `wt/cmd/wt/cloudsync_catalog.go`:

```go
// The catalog flow of wt cloud-sync: mirror https://ollama.com/pricing into
// the registry's ollama cloud entries and into ollama itself. Everything that
// decides what changes is internal/cloudsync's; this file reads the page and
// `ollama list`, gates the plan, and runs the pulls and removals the registry
// write leaves (through cloudsync_ollama.go).
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cloudsync"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// saveFailedHTML keeps a page ParsePricing refused, so the parser can be
// repaired against exactly what ollama served.
func saveFailedHTML(page string, now time.Time) (string, error) {
	path := filepath.Join(os.TempDir(), "ollama-pricing-"+now.Format("20060102-150405")+".html")
	return path, os.WriteFile(path, []byte(page), 0o600)
}

// catalogRun is the catalog flow between its plan and its apply: everything
// the plan was made from, so it can be made again under the registry lock.
type catalogRun struct {
	origin      string
	catalog     cloudsync.Catalog
	tags        []string
	resolved    map[string]string
	tagWarnings []string
	plan        *cloudsync.CatalogPlan
	printed     string
}

// replan makes the plan again from entries, with the same page, tags and
// resolved names.
func (c *catalogRun) replan(entries []cloudsync.Entry) *cloudsync.CatalogPlan {
	plan := cloudsync.PlanCatalog(entries, c.catalog, c.tags, c.resolved)
	plan.Warnings = append(plan.Warnings, c.tagWarnings...)
	return plan
}

// planCatalogFlow reads the page and `ollama list`, resolves the cloud tags
// and prints the catalog plan. It returns nil, with res saying why, when the
// flow cannot go on; in every such case it has changed nothing.
func planCatalogFlow(ctx context.Context, out, errOut io.Writer, cfg *config.Config, o cloudSyncOpts,
	entries []cloudsync.Entry, providers []cloudsync.Provider, res *cloudSyncOutcome) *catalogRun {
	stop := func(code int, format string, args ...any) *catalogRun {
		fmt.Fprintf(errOut, "catalog: error: "+format+"\n", args...)
		if code == 1 {
			res.failed = true
		} else {
			res.catalogCode = code
		}
		return nil
	}
	// A new entry is a row with provider_id "ollama". With no such provider
	// row the registry would stop loading the moment one was added, and there
	// is no daemon address to pin the CLI to. A registry that does not use
	// ollama has nothing to mirror, which is not a failure of the default
	// run; asked for by name, the flow cannot do what was asked.
	if !slices.ContainsFunc(providers, func(p cloudsync.Provider) bool { return p.ID == "ollama" }) {
		if !o.catalogNamed {
			fmt.Fprintln(out, "catalog: no ollama provider in the registry; nothing to mirror")
			return nil
		}
		return stop(1, "registry.toml has no ollama provider row, and every catalog entry needs one — add the row (`wt model init` adds it once `ollama` is on PATH), then run this again; nothing was changed")
	}

	var page string
	if o.htmlFile != "" {
		data, err := os.ReadFile(o.htmlFile)
		if err != nil {
			return stop(2, "cannot read %s: %v; nothing was changed", o.htmlFile, err)
		}
		page = string(data)
	} else {
		data, err := cloudFetch(ctx, cloudsync.PricingURL)
		if err != nil {
			return stop(2, "could not fetch ollama.com/pricing: %v; nothing was changed", err)
		}
		page = string(data)
	}
	catalog, err := cloudsync.ParsePricing(page)
	if err != nil {
		saved, saveErr := saveFailedHTML(page, cloudSyncNow())
		where := "raw HTML saved to " + saved
		if saveErr != nil {
			where = "the raw HTML could not be saved: " + saveErr.Error()
		}
		return stop(3, "could not parse ollama.com/pricing: %v\ncatalog: %s — the parser to update is wt/internal/cloudsync/pricingpage.go; nothing was changed", err, where)
	}

	run := &catalogRun{catalog: catalog}
	run.origin, _ = localmodels.FamilyOrigin(cfg, "ollama")
	// Without it, what to pull and what to rm is unknowable: refuse instead
	// of mirroring half the plan.
	if run.tags, err = ollamaTags(ctx, run.origin); err != nil {
		return stop(2, "could not run `ollama list` against %s (is the ollama daemon up?): %v; nothing was changed", run.origin, err)
	}
	names := make([]string, len(catalog.Models))
	for i, m := range catalog.Models {
		names[i] = m.Name
	}
	run.resolved, run.tagWarnings = cloudsync.ResolveCloudTags(ctx, cloudFetch, names, cloudsync.VerifiedTags(entries, run.tags))
	if !slices.ContainsFunc(names, func(n string) bool { return run.resolved[n] != "" }) {
		for _, w := range run.tagWarnings {
			fmt.Fprintf(errOut, "catalog:   %s\n", w)
		}
		return stop(2, "could not resolve a cloud tag for any model on ollama.com/library; nothing was changed")
	}

	run.plan = run.replan(entries)
	run.printed = run.plan.Format()
	prefixLines(out, "catalog", run.printed)
	return run
}

// catalogGate decides whether a printed catalog plan may be applied: not a
// mass removal without --force, and under --yes not a plan that deletes
// anything without the digest of a reviewed dry run. It reports false, with
// the reason printed and res.catalogCode set, when it may not.
func catalogGate(errOut io.Writer, c *catalogRun, o cloudSyncOpts, res *cloudSyncOutcome) bool {
	if c.plan.MassRemoval() && !o.force {
		fmt.Fprintf(errOut, "catalog: error: %d of %d ollama cloud entries would be removed — check the page parsed correctly, then re-run with --force. Nothing was changed for the catalog.\n",
			len(c.plan.Removals), c.plan.CloudEntries)
		res.catalogCode = 4
		return false
	}
	if digest := c.plan.RemovalDigest(); o.yes && digest != "" && o.approve != digest {
		reason := "the plan deletes models"
		if o.approve != "" {
			reason = "the removals are not the ones digest " + o.approve + " approved"
		}
		fmt.Fprintf(errOut, "catalog: error: %s — review a --dry-run, then re-run with `--yes --approve-removals %s`. Nothing was changed for the catalog.\n", reason, digest)
		res.catalogCode = 5
		return false
	}
	return true
}

// runOllamaWork does what the registry write left for ollama: the pulls,
// then the removals. Each failure is reported and makes the run exit 1, and
// the rest still run. A tag whose `ollama rm` failed is by now a stray (its
// entry is gone from the registry), which the next run removes; that run's
// removals are not this run's, so the failure line says a new digest is
// needed.
func runOllamaWork(ctx context.Context, out, errOut io.Writer, origin string, done cloudsync.CatalogApplied, res *cloudSyncOutcome) {
	for _, tag := range done.Pulls {
		if err := ollamaPull(ctx, origin, tag); err != nil {
			fmt.Fprintf(errOut, "catalog: error: %v\n", err)
			res.failed = true
			continue
		}
		fmt.Fprintf(out, "catalog: pulled %s\n", tag)
	}
	for _, tag := range done.Removes {
		if err := ollamaRemove(ctx, origin, tag); err != nil {
			fmt.Fprintf(errOut, "catalog: error: %v\n", err)
			fmt.Fprintf(errOut, "catalog:   %s is left pulled; the next run lists it as a stray tag — start again from --dry-run, since the removal digest may have changed\n", tag)
			res.failed = true
			continue
		}
		fmt.Fprintf(out, "catalog: removed %s\n", tag)
	}
}
```

- [ ] **Step 4: Give the command both flows**

Replace the whole of `wt/cmd/wt/cloudsync.go` with the following. Against Task 9's file it changes: the header comment; `cloudSyncFlows`; `cloudSyncOpts`; `selectFlows`; the command's `Short`, `Long`, `Example`, the usage check and the three new flags; `cloudSyncOutcome` and its `err`; `cloudSyncApplied`; and `runCloudSync`, which now plans, gates, applies and reports the catalog beside the prices; the registry write moves out of it into `writeCloudSync`, so that it can be made once for both flows and, when that is refused because a row would not load, once for each. `realCloudFetch`, `promptCloudSync`, `prefixLines`, `pricesRun`, `planPricesFlow` and `withRegistryHint` are unchanged.

```go
// wt cloud-sync — refresh what two public services publish into
// registry.toml: OpenRouter's prices (the prices flow) and ollama.com's cloud
// catalog (the catalog flow, cloudsync_catalog.go). The planning is
// internal/cloudsync's; this file owns the fetches, the confirmation, the one
// registry write, the route sync and the exit code.
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cloudsync"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/spf13/cobra"
)

// Test seams. cmd/wt's TestMain makes all of them fail closed, so no test
// reaches the network, a terminal or the developer's ollama.
var (
	// cloudFetch GETs one public page: OpenRouter's model list,
	// ollama.com/pricing, an ollama.com/library tags page.
	cloudFetch = realCloudFetch
	// confirmCloudSync asks the one question that covers both printed plans.
	confirmCloudSync = promptCloudSync
	// cloudSyncNow is the clock the stamps and the saved-page name read.
	cloudSyncNow = time.Now
)

// cloudSyncFlows are the flows `--only` selects among, in the order they are
// planned and reported.
var cloudSyncFlows = []string{"prices", "catalog"}

var cloudHTTP = &http.Client{Timeout: 30 * time.Second}

// cloudFetchLimit bounds one response. OpenRouter's model list, the largest,
// is under 1 MiB.
const cloudFetchLimit = 32 << 20

func realCloudFetch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := cloudHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, cloudFetchLimit))
}

// promptCloudSync asks on the controlling terminal and defaults to No, like
// promptStop: piped input can never approve a sync. Without a terminal it
// names the flag that applies without asking. The terminal is opened through
// openTTY (launch.go), the seam a test takes it away with.
func promptCloudSync(question string) (bool, error) {
	f, err := openTTY()
	if err != nil {
		return false, errors.New("there is no terminal to confirm on — rerun with --yes to apply without asking")
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "%s [y/N] ", question); err != nil {
		return false, err
	}
	return askYesNo(f)
}

// cloudSyncOpts is the parsed command line.
type cloudSyncOpts struct {
	prices, catalog bool
	dryRun, yes     bool
	// catalogNamed: the user asked for the catalog flow in so many words
	// (--only names it, or a catalog flag was given), as against getting it
	// because both flows are the default.
	catalogNamed bool
	// Catalog flow only.
	force    bool
	htmlFile string
	approve  string
}

// selectFlows reads --only: a comma list of flow names, every flow when it
// is empty.
func selectFlows(only string, o *cloudSyncOpts) error {
	if strings.TrimSpace(only) == "" {
		o.prices, o.catalog = true, true
		return nil
	}
	for _, name := range strings.Split(only, ",") {
		switch name = strings.TrimSpace(name); name {
		case "prices":
			o.prices = true
		case "catalog":
			o.catalog, o.catalogNamed = true, true
		default:
			return fmt.Errorf("--only: unknown flow %q (valid: %s)", name, strings.Join(cloudSyncFlows, ", "))
		}
	}
	return nil
}

func cloudSyncCmd(a *app) *cobra.Command {
	var (
		o    cloudSyncOpts
		only string
	)
	cmd := &cobra.Command{
		Use:   "cloud-sync",
		Short: "Refresh cloud prices (OpenRouter) and the ollama cloud catalog into registry.toml",
		Long: "Bring registry.toml up to date with what two public services publish. Two\n" +
			"independent flows, both run unless --only picks one:\n\n" +
			"  prices   OpenRouter's per-token prices, for the models priced by OpenRouter\n" +
			"  catalog  https://ollama.com/pricing, mirrored: prices (off-peak included),\n" +
			"           new cloud models added and pulled, models the page no longer\n" +
			"           lists removed from the registry and from ollama\n\n" +
			"Both plans are printed first, each line prefixed with its flow. --dry-run\n" +
			"stops there. Otherwise one confirmation covers both (asked on the terminal;\n" +
			"--yes skips it), the registry is written once, the catalog's pulls and\n" +
			"removals run, and the LiteLLM routes are synced once if a price, the set of\n" +
			"models or a pulled tag changed.\n\n" +
			"--yes never deletes on its own: a catalog plan that removes anything is\n" +
			"applied only with --approve-removals and the digest a dry run printed.\n" +
			"Local models are never touched. A registry with no ollama provider has no\n" +
			"catalog to mirror: the catalog flow says so and is skipped, unless it was\n" +
			"asked for by name.\n\n" +
			"Exit status:\n" +
			"  0  every selected flow finished, or had nothing to do\n" +
			"  1  a step failed in either flow, or a usage error\n" +
			"  2  catalog changed nothing: the page, --html or `ollama list` could not\n" +
			"     be read, or no cloud tag resolved\n" +
			"  3  catalog changed nothing: the pricing page changed shape (its HTML is\n" +
			"     saved, and the path printed)\n" +
			"  4  catalog changed nothing: it would remove more than half the ollama\n" +
			"     cloud entries, and --force was not given\n" +
			"  5  catalog changed nothing: its removals were not approved\n" +
			"2 to 5 are about the catalog flow only and win over 1; the prices flow may\n" +
			"have been applied in the same run.",
		Example: "  wt cloud-sync --dry-run\n" +
			"  wt cloud-sync --yes --approve-removals 0bcc560b956e\n" +
			"  wt cloud-sync --only prices --yes",
		Args: cobra.NoArgs,
		// A failed fetch or a refused plan is not a usage mistake, and main
		// prints the one error line.
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := selectFlows(only, &o); err != nil {
				return err
			}
			for _, flag := range []string{"html", "approve-removals", "force"} {
				if !cmd.Flags().Changed(flag) {
					continue
				}
				if !o.catalog {
					return fmt.Errorf("--%s is for the catalog flow, which --only %s leaves out", flag, only)
				}
				o.catalogNamed = true
			}
			// Only a config that could not be loaded stops this: a.cfg is
			// then an empty default, and a sync planned against it would
			// find nothing to keep. A validation gap does not.
			if a.loadErr != nil {
				return configError(a.loadErr)
			}
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			return runCloudSync(ctx, cmd.OutOrStdout(), cmd.ErrOrStderr(), a.cfg, o)
		},
	}
	cmd.Flags().StringVar(&only, "only", "", "Run only these flows: a comma list of "+strings.Join(cloudSyncFlows, ", ")+" (default: all)")
	cmd.Flags().BoolVar(&o.dryRun, "dry-run", false, "Print the plans and change nothing")
	cmd.Flags().BoolVar(&o.yes, "yes", false, "Apply without asking (removals still need --approve-removals)")
	cmd.Flags().StringVar(&o.htmlFile, "html", "", "catalog: parse this saved pricing page instead of fetching it")
	cmd.Flags().StringVar(&o.approve, "approve-removals", "", "catalog, with --yes: the removal digest a reviewed --dry-run printed")
	cmd.Flags().BoolVar(&o.force, "force", false, "catalog: apply even if more than half the ollama cloud entries would be removed")
	return cmd
}

// cloudSyncOutcome collects what the exit status is made of.
type cloudSyncOutcome struct {
	// failed: a step failed in either flow.
	failed bool
	// catalogCode is 2 to 5 when the catalog flow changed nothing, else 0.
	catalogCode int
}

var catalogCodeMeaning = map[int]string{
	2: "an input could not be read",
	3: "the pricing page changed shape",
	4: "mass removal refused",
	5: "removals not approved",
}

// err is the command's result: nil, or an exitCodeError. A catalog code wins
// over a failed step, as the codes are documented.
func (r *cloudSyncOutcome) err() error {
	switch {
	case r.catalogCode != 0:
		return &exitCodeError{code: r.catalogCode, err: fmt.Errorf("cloud-sync: the catalog flow changed nothing: %s", catalogCodeMeaning[r.catalogCode])}
	case r.failed:
		return &exitCodeError{code: 1, err: errors.New("cloud-sync: a step failed; see the error lines above")}
	}
	return nil
}

// prefixLines writes text to w with every line prefixed by "<flow>: ".
func prefixLines(w io.Writer, flow, text string) {
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		fmt.Fprintf(w, "%s: %s\n", flow, line)
	}
}

// pricesRun is the prices flow between its plan and its apply.
type pricesRun struct {
	api     map[string]cloudsync.APIPrice
	plan    *cloudsync.PricePlan
	printed string
}

// planPricesFlow fetches OpenRouter's list and prints the price plan. It
// returns nil when there is nothing to apply: no OpenRouter-priced model (no
// fetch is made), or a fetch that failed (reported, and res.failed set).
func planPricesFlow(ctx context.Context, out, errOut io.Writer, entries []cloudsync.Entry, providers []cloudsync.Provider, res *cloudSyncOutcome) *pricesRun {
	if !slices.ContainsFunc(entries, func(e cloudsync.Entry) bool { return cloudsync.OpenRouterPriced(e, providers) }) {
		fmt.Fprintln(out, "prices: no OpenRouter-priced model in the registry; nothing to refresh")
		return nil
	}
	body, err := cloudFetch(ctx, cloudsync.OpenRouterModelsURL)
	var api map[string]cloudsync.APIPrice
	if err == nil {
		api, err = cloudsync.ParseOpenRouter(body)
	}
	if err != nil {
		fmt.Fprintf(errOut, "prices: error: could not read OpenRouter's prices: %v; no price was changed\n", err)
		res.failed = true
		return nil
	}
	run := &pricesRun{api: api, plan: cloudsync.PlanPrices(entries, providers, api)}
	run.printed = run.plan.Format()
	prefixLines(out, "prices", run.printed)
	return run
}

// cloudSyncApplied is what the registry write did. The apply function
// assigns it afresh on every run.
type cloudSyncApplied struct {
	prices       cloudsync.PricesApplied
	pricesStale  bool
	catalog      cloudsync.CatalogApplied
	catalogStale bool
}

// runCloudSync is `wt cloud-sync`: plan both flows with no lock held, print
// both plans, gate, apply both in one registry write, run the catalog's
// ollama work, sync the routes once. The flows are independent: one that
// fails or is refused does not stop the other. (That is also the one case
// with a second write: when the shared write is refused because a row would
// not load, each flow is written alone, so only the flow that owns the row
// is held up.)
func runCloudSync(ctx context.Context, out, errOut io.Writer, cfg *config.Config, o cloudSyncOpts) error {
	doc, err := config.ReadRegistryDoc()
	if err != nil {
		return withRegistryHint(err)
	}
	entries, providers := cloudsync.Entries(doc.Models()), cloudsync.Providers(doc.Providers())

	var res cloudSyncOutcome
	var prices *pricesRun
	if o.prices {
		prices = planPricesFlow(ctx, out, errOut, entries, providers, &res)
	}
	var catalog *catalogRun
	if o.catalog {
		catalog = planCatalogFlow(ctx, out, errOut, cfg, o, entries, providers, &res)
	}
	if o.dryRun {
		return res.err()
	}

	// What is left to apply. A plan with nothing in it is not asked about.
	if prices != nil && !prices.plan.HasWork() {
		prices = nil
	}
	if catalog != nil && (!catalog.plan.HasWork() || !catalogGate(errOut, catalog, o, &res)) {
		catalog = nil
	}
	if prices == nil && catalog == nil {
		return res.err()
	}
	// The flows about to be applied, for the lines that concern all of them.
	var pending []string
	if prices != nil {
		pending = append(pending, "prices")
	}
	if catalog != nil {
		pending = append(pending, "catalog")
	}
	notApplied := func(w io.Writer, format string, args ...any) {
		for _, flow := range pending {
			fmt.Fprintf(w, "%s: %s\n", flow, fmt.Sprintf(format, args...))
		}
	}
	if !o.yes {
		ok, err := confirmCloudSync("Apply these changes?")
		if err != nil {
			notApplied(errOut, "error: not applied: %v", err)
			res.failed = true
			return res.err()
		}
		if !ok {
			notApplied(out, "not applied (declined)")
			return res.err()
		}
	}

	applied, err := writeCloudSync(prices, catalog, cloudSyncNow())
	switch {
	case err == nil:
	case prices != nil && catalog != nil && errors.Is(err, config.ErrRegistryInvalid):
		// A row one flow must change would not load, and the writer refused
		// the write whole. The flows are independent: write each alone, so
		// the broken row holds up only the flow that owns it.
		alone, perr := writeCloudSync(prices, nil, cloudSyncNow())
		applied.prices, applied.pricesStale = alone.prices, alone.pricesStale
		if perr != nil {
			fmt.Fprintf(errOut, "prices: error: the price changes were not written: %v\n", withRegistryHint(perr))
			res.failed, prices = true, nil
		}
		alone, cerr := writeCloudSync(nil, catalog, cloudSyncNow())
		applied.catalog, applied.catalogStale = alone.catalog, alone.catalogStale
		if cerr != nil {
			fmt.Fprintf(errOut, "catalog: error: the catalog's changes were not written: %v\n", withRegistryHint(cerr))
			res.failed, catalog = true, nil
		}
	default:
		notApplied(errOut, "error: registry.toml was not changed: %v", withRegistryHint(err))
		res.failed = true
		return res.err()
	}

	if prices != nil {
		if applied.pricesStale {
			fmt.Fprintln(errOut, "prices: error: the registry changed after the plan was printed; no price was changed — run it again")
			res.failed = true
		} else {
			fmt.Fprintf(out, "prices: refreshed %d model(s); %d price(s) changed\n", applied.prices.Stamped, applied.prices.Changed)
		}
	}
	if catalog != nil {
		if applied.catalogStale {
			fmt.Fprintln(errOut, "catalog: error: the registry changed after the plan was printed, so this is no longer the plan that was approved; nothing was changed — run it again")
			res.catalogCode = 5
		} else {
			fmt.Fprintf(out, "catalog: updated %d, added %d and removed %d model(s)\n", applied.catalog.Updated, applied.catalog.Added, applied.catalog.Removed)
			runOllamaWork(ctx, out, errOut, catalog.origin, applied.catalog, &res)
		}
	}

	// A run that only stamped models changed no route: skip the sync, which
	// could restart the proxy for nothing. A run with ollama work syncs even
	// when the registry did not change: it may be finishing a run that was
	// interrupted after its registry write and before its sync, and the
	// registry alone no longer shows that the routes are behind.
	ollamaWork := len(applied.catalog.Pulls)+len(applied.catalog.Removes) > 0
	if applied.prices.Changed > 0 || applied.catalog.Changed() || ollamaWork {
		var routes, routeErrs bytes.Buffer
		warning := syncRoutesAfterWrite(&routes, &routeErrs)
		if routes.Len() > 0 {
			prefixLines(out, "routes", routes.String())
		}
		if routeErrs.Len() > 0 {
			prefixLines(errOut, "routes", routeErrs.String())
		}
		if warning != "" {
			fmt.Fprintf(errOut, "routes: warning: %s\n", warning)
		}
	}
	return res.err()
}

// withRegistryHint is err with the repair config names for it, when it names
// one.
func withRegistryHint(err error) error {
	if hint := config.RegistryFixHint(err); hint != "" {
		return fmt.Errorf("%w (%s)", err, hint)
	}
	return err
}

// writeCloudSync is the registry write: one config.UpdateRegistry for the
// flows it is given (either may be nil). Each plan is made again from the
// file as it is under the lock, and applied only if it is still the plan
// that was printed.
func writeCloudSync(prices *pricesRun, catalog *catalogRun, now time.Time) (cloudSyncApplied, error) {
	var applied cloudSyncApplied
	_, err := config.UpdateRegistry(func(d *config.RegistryDoc) error {
		// Afresh on every run: apply may run more than once.
		applied = cloudSyncApplied{}
		entries, providers := cloudsync.Entries(d.Models()), cloudsync.Providers(d.Providers())
		if prices != nil {
			fresh := cloudsync.PlanPrices(entries, providers, prices.api)
			if applied.pricesStale = fresh.Format() != prices.printed; !applied.pricesStale {
				done, err := fresh.Apply(d, now)
				if err != nil {
					return err
				}
				applied.prices = done
			}
		}
		if catalog != nil {
			fresh := catalog.replan(entries)
			if applied.catalogStale = fresh.Format() != catalog.printed; !applied.catalogStale {
				done, err := fresh.Apply(d, catalog.tags, now)
				if err != nil {
					return err
				}
				applied.catalog = done
			}
		}
		return nil
	})
	if err != nil {
		return cloudSyncApplied{}, err
	}
	return applied, nil
}
```

- [ ] **Step 5: Update the example in `wt --help`**

In `wt/cmd/wt/main.go`, find:

```go
			"  wt smoke [model]             # smoke-test every agent that supports a model\n" +
			"  wt cloud-sync --dry-run      # plan a refresh of cloud prices",
		// ArbitraryArgs overrides cobra's default legacyArgs validator, which
```

and replace it with:

```go
			"  wt smoke [model]             # smoke-test every agent that supports a model\n" +
			"  wt cloud-sync --dry-run      # plan a refresh of cloud prices and the ollama catalog",
		// ArbitraryArgs overrides cobra's default legacyArgs validator, which
```

- [ ] **Step 6: Run the tests to verify they pass**

Run (from `wt/`): `go test -count=1 ./cmd/wt -run 'CloudSync|SelectFlows'`
Expected:

```text
ok  	github.com/ohanaverse/local-ai-setup/wt/cmd/wt	(time)
```

(`-v` lists 30 passing top-level tests.)

- [ ] **Step 7: Keep the docs true**

In `wt/CLAUDE.md`, find:

```text
| `cmd/wt/cloudsync_ollama.go` |
```

and replace it with:

```text
| `cmd/wt/cloudsync_catalog.go` | the catalog flow: `planCatalogFlow` (page or `--html`, `ollama list`, tag resolution, the printed plan; exit 2 and 3), `catalogGate` (mass removal, the removal digest; exit 4 and 5), `runOllamaWork` (pulls, then removals, after the registry write), `saveFailedHTML` |
| `cmd/wt/cloudsync_ollama.go` |
```

In `wt/CLAUDE.md`, find:

```text
- **Only the row labelled `off-peak` in `cost.time_prices` is the catalog's.**
```

and replace it with:

```text
- **Exit codes 2 to 5 are the catalog flow's and win over 1** (`cloudSyncOutcome.err`); every one means "the catalog changed nothing". The prices flow may have been applied in the same run. The catalog flow also has stops that are exit 1 and not one of those: asked for by name on a registry with no ollama provider row, no terminal to confirm on, and a refused registry write. A new stop gets one of 2 to 5 only when it fits that code's documented meaning.
- **A registry with no ollama provider row has no catalog**: the default run says so and skips the flow (exit unaffected); only `cloudSyncOpts.catalogNamed` (`--only` names it, or a catalog flag was given) makes that an error.
- **One registry write, except when it is refused as invalid**: `writeCloudSync` is called once for both flows; on `config.ErrRegistryInvalid` with both pending it is called once per flow, so a row that would not load holds up only the flow that owns it (`TestCloudSyncWritesEachFlowAloneWhenARowWouldNotLoad`).
- **The route sync runs when a price or the model set changed, or ollama had work** (`CatalogApplied.Pulls`/`Removes`): a run that finishes an interrupted one changes no registry row and must still sync (`TestCloudSyncFinishesAnInterruptedRun`). A stamp-only run never syncs.
- **A registry entry goes in the registry write; its tag goes afterwards**, and only when it is a cloud tag, `ollama list` showed it and no remaining ollama entry names it (`CatalogApplied.Removes`). A failed `ollama rm` leaves a stray tag, which the next run removes.
- **Only the row labelled `off-peak` in `cost.time_prices` is the catalog's.**
```

In `wt/CHANGELOG.md`, find:

```text
- `wt cloud-sync [--only prices] [--dry-run] [--yes]` refreshes the per-token
  prices of the registry's OpenRouter-priced models from OpenRouter's public
  model list, replacing `modelman refresh-prices` (which still works). It
```

and replace it with:

```text
- `wt cloud-sync` gains its second flow, `catalog`, replacing
  `modelman ollama-catalog sync` (which still works): it mirrors
  <https://ollama.com/pricing> into the registry's ollama cloud entries
  (prices, off-peak included), into ollama (`ollama pull` for new cloud
  models, `ollama rm` for retired ones, both pinned to the registry's ollama
  address) and, through one route sync, into LiteLLM. Both flows run unless
  `--only prices` or `--only catalog` picks one, and neither stops the
  other. `--yes` never deletes on its own: a plan that removes anything
  needs `--approve-removals` with the digest a dry run printed (the same
  digest modelman prints), and a plan that removes more than half the cloud
  entries needs `--force` as well. Exit codes 2 to 5 say why the catalog
  flow changed nothing (inputs unreadable, page shape changed with its HTML
  saved, mass removal refused, removals not approved); every other wt
  command still exits 1 on an error. See `docs/wt-cloud-sync.md`.
- `wt cloud-sync [--only prices] [--dry-run] [--yes]` refreshes the per-token
  prices of the registry's OpenRouter-priced models from OpenRouter's public
  model list, replacing `modelman refresh-prices` (which still works). It
```

In `CLAUDE.md`, find:

```text
and `wt cloud-sync` (cloud prices);
```

and replace it with:

```text
and `wt cloud-sync` (cloud prices and the ollama cloud catalog);
```

In `docs/guides/00-config-map.md`, find:

```text
and `wt cloud-sync` (it refreshes cloud prices, and stamps `pricing_updated_at` on the models it priced).
```

and replace it with:

```text
and `wt cloud-sync` (it refreshes cloud prices and mirrors ollama's cloud catalog: it re-prices, adds and removes ollama cloud entries, and stamps `pricing_updated_at` on the models it priced).
```

- [ ] **Step 8: Run the built binary in a throwaway home, with a stand-in `ollama`**

This is the scratch run of the whole flow. It makes **no network call** (`--html` supplies the page, and every name on the test page either pins its size or is already "pulled" and recorded) and the only `ollama` it can find is a script that writes down what it was asked. From `wt/`:

```bash
SCRATCH="$(mktemp -d)"
mkdir -p "$SCRATCH/bin" "$SCRATCH/home/.config/local-ai"
go build -o "$SCRATCH/bin/wt" ./cmd/wt
cat > "$SCRATCH/bin/ollama" <<EOF
#!/bin/sh
# Stand-in for ollama: records argv and OLLAMA_HOST, changes nothing.
echo "argv: \$* | OLLAMA_HOST=\$OLLAMA_HOST" >> "$SCRATCH/ollama-calls.log"
[ "\$1" = list ] && printf 'NAME ID SIZE MODIFIED\nkeep-a:cloud abc - 1 day ago\nstray:cloud abc - 1 day ago\n'
exit 0
EOF
chmod +x "$SCRATCH/bin/ollama"
cat > "$SCRATCH/home/.config/local-ai/registry.toml" <<'EOF'
[[providers]]
id = "ollama"
name = "Ollama"
location = "local"

[providers.auth]
type = "none"
base_url = "http://127.0.0.1:11999"

[[models]]
catalog_name = "keep-a"
id = "ollama/keep-a:cloud"
family = "keep-a"
provider_id = "ollama"
model_name = "keep-a:cloud"
location = "cloud"

[[models]]
id = "ollama/retired:cloud"
family = "retired"
provider_id = "ollama"
model_name = "retired:cloud"
location = "cloud"
EOF
cat > "$SCRATCH/page.html" <<'EOF'
<table><tr><th>Model</th><th>Input</th><th>Cached input</th><th>Output</th></tr>
<tr><td>keep-a</td><td>$1.00</td><td>$0.10</td><td>$2.00</td></tr>
<tr><td>new-b:9b</td><td>$1.00</td><td>-</td><td>$2.00</td></tr>
<tr><td>new-c:9b</td><td>$1.00</td><td>-</td><td>$2.00</td></tr>
<tr><td>new-d:9b</td><td>$1.00</td><td>-</td><td>$2.00</td></tr>
<tr><td>new-e:9b</td><td>$1.00</td><td>-</td><td>$2.00</td></tr></table>
EOF
run() { env -i PATH="$SCRATCH/bin:/usr/bin:/bin" HOME="$SCRATCH/home" XDG_CONFIG_HOME="$SCRATCH/home/.config" \
  WT_LITELLM_CONFIG="$SCRATCH/home/litellm.yaml" WT_LITELLM_RESTART_CMD=true OLLAMA_HOST=http://elsewhere.invalid:1 \
  "$SCRATCH/bin/wt" cloud-sync --only catalog --html "$SCRATCH/page.html" "$@" 2>&1; echo "exit=$?"; }
run --dry-run | tail -4
run --yes | tail -3
run --yes --approve-removals 745cee18467a | tail -7
cat "$SCRATCH/ollama-calls.log"
grep -c 'ollama/new-' "$SCRATCH/home/.config/local-ai/registry.toml"
# A registry that does not use ollama: the plain command, then the catalog by name.
cat > "$SCRATCH/home/.config/local-ai/registry.toml" <<'EOF'
[[providers]]
id = "claude"
name = "Claude"
location = "cloud"

[providers.auth]
type = "native"
EOF
: > "$SCRATCH/ollama-calls.log"
plain() { env -i PATH="$SCRATCH/bin:/usr/bin:/bin" HOME="$SCRATCH/home" XDG_CONFIG_HOME="$SCRATCH/home/.config" \
  WT_LITELLM_CONFIG="$SCRATCH/home/litellm.yaml" WT_LITELLM_RESTART_CMD=true "$SCRATCH/bin/wt" cloud-sync "$@" 2>&1; echo "exit=$?"; }
plain --yes
plain --yes --only catalog
test -s "$SCRATCH/ollama-calls.log" || echo "no ollama command was run"
rm -rf "$SCRATCH"
```

The digest in the third command, `745cee18467a`, is not a guess: it is what this registry and this page give (one removal, `ollama/retired:cloud`, and one stray tag, `stray:cloud`), and the dry run prints it.

Expected:

```text
catalog: ollama rm — pulled, unregistered, off the page (1):
catalog:   stray:cloud
catalog: Removal digest: 745cee18467a (apply non-interactively with `--yes --approve-removals 745cee18467a`)
exit=0
catalog: error: the plan deletes models — review a --dry-run, then re-run with `--yes --approve-removals 745cee18467a`. Nothing was changed for the catalog.
wt: cloud-sync: the catalog flow changed nothing: removals not approved
exit=5
catalog: updated 1, added 4 and removed 1 model(s)
catalog: pulled new-b:9b-cloud
catalog: pulled new-c:9b-cloud
catalog: pulled new-d:9b-cloud
catalog: pulled new-e:9b-cloud
catalog: removed stray:cloud
exit=0
argv: list | OLLAMA_HOST=http://127.0.0.1:11999
argv: list | OLLAMA_HOST=http://127.0.0.1:11999
argv: list | OLLAMA_HOST=http://127.0.0.1:11999
argv: pull new-b:9b-cloud | OLLAMA_HOST=http://127.0.0.1:11999
argv: pull new-c:9b-cloud | OLLAMA_HOST=http://127.0.0.1:11999
argv: pull new-d:9b-cloud | OLLAMA_HOST=http://127.0.0.1:11999
argv: pull new-e:9b-cloud | OLLAMA_HOST=http://127.0.0.1:11999
argv: rm stray:cloud | OLLAMA_HOST=http://127.0.0.1:11999
4
prices: no OpenRouter-priced model in the registry; nothing to refresh
catalog: no ollama provider in the registry; nothing to mirror
exit=0
catalog: error: registry.toml has no ollama provider row, and every catalog entry needs one — add the row (`wt model init` adds it once `ollama` is on PATH), then run this again; nothing was changed
wt: cloud-sync: a step failed; see the error lines above
exit=1
no ollama command was run
```

`retired:cloud` leaves the registry and is not removed from ollama: the stand-in's list never showed it. Every recorded command ends `OLLAMA_HOST=http://127.0.0.1:11999`, the registry's address, not the `elsewhere.invalid` the environment exported: three `list` lines (one per run), then four pulls and one `rm stray:cloud`. The line `4` is the number of new entries in the registry. No `routes:` line appears because the scratch `litellm.yaml` does not exist, which is the "no LiteLLM here" case.

The last block is the registry that does not use ollama (one native provider, no ollama row). The plain command has nothing to do in either flow and exits 0; the catalog asked for by name is an error, exit 1, with one line from the flow and one from the command; and neither run asked the stand-in for anything.

- [ ] **Step 9: Verify the slice**

From `wt/`:

```bash
test -z "$(gofmt -l .)" && go vet ./... && go test -count=1 ./... && make check
```

Expected: 23 `ok` lines, no `FAIL`. Then from the monorepo root: `make test-all` (exit 0) and `make check-links` (`ALL LINKS OK`).

- [ ] **Step 10: Commit**

From the repo root:

```bash
git add wt/cmd/wt/cloudsync.go wt/cmd/wt/cloudsync_test.go wt/cmd/wt/cloudsync_catalog.go wt/cmd/wt/cloudsync_catalog_test.go wt/cmd/wt/main.go wt/CLAUDE.md wt/CHANGELOG.md CLAUDE.md docs/guides/00-config-map.md
git commit -m "feat(wt): wt cloud-sync mirrors ollama.com/pricing (the catalog flow, exit codes 2 to 5)"
```

PR 3 is ready for review, but **it does not merge until Task 13 has been run** and its result is in the PR. **Ask the owner before pushing or opening the PR.**

---

## PR 4 — the skill and the docs

Branch: `git switch -c docs/wt-cloud-sync-skill main`, after PR 3 has merged.

### Task 12: Move and rewrite the skill; the command reference; the guides

**Files:**
- Move and rewrite: `modelman/.claude/skills/ollama-catalog/SKILL.md` → `wt/.claude/skills/cloud-sync/SKILL.md`
- Create: `wt/docs/wt-cloud-sync.md`
- Modify: `CLAUDE.md`, `wt/CLAUDE.md`, `modelman/CLAUDE.md` (one line), `docs/guides/02-providers-and-models.md`, `docs/guides/04-litellm-config.md`

**Interfaces:**
- Consumes: the command as PR 3 left it. Everything written here must be true of that binary; check a doubtful sentence against `wt cloud-sync --help` and the tests, not against memory.
- Produces: no code.

There is no test to write first for prose. The checks are `make check-links`, the skill's front matter loading, and a read of each exit code and flag against the command's `--help`.

- [ ] **Step 1: Move the skill**

From the repo root:

```bash
mkdir -p wt/.claude/skills/cloud-sync
git mv modelman/.claude/skills/ollama-catalog/SKILL.md wt/.claude/skills/cloud-sync/SKILL.md
rmdir modelman/.claude/skills/ollama-catalog
```

One skill, moved, not copied: two skills with the same trigger would both load. modelman's two commands keep working without a skill until Step 6 deletes them.

- [ ] **Step 2: Rewrite it**

Replace the whole of `wt/.claude/skills/cloud-sync/SKILL.md` with:

```markdown
---
name: cloud-sync
description: Refresh cloud model prices and the ollama cloud catalog with `wt cloud-sync` — OpenRouter prices into registry.toml, and https://ollama.com/pricing mirrored into the registry, ollama and LiteLLM (pull and register new cloud models, rm and unregister ones Ollama no longer lists, prices including off-peak). Use when asked to update or refresh prices, ollama cloud models or ollama pricing, when wt says token pricing is stale, or when the ollama pricing scrape breaks (exit 3).
---

# Cloud sync

`wt cloud-sync` runs two independent flows and prefixes every line with the
one it is about:

- **`prices:`** re-prices the registry's OpenRouter-priced models from
  OpenRouter's public model list and stamps each one it matched. The stamp
  is what clears wt's "token pricing last refreshed … — run 'wt cloud-sync'"
  notice.
- **`catalog:`** makes three things match <https://ollama.com/pricing>: the
  ollama cloud entries in `registry.toml` with their prices (off-peak
  included), the cloud tags in `ollama list`, and, through one route sync at
  the end, LiteLLM's routes. It adds and `ollama pull`s new page models,
  removes entries the page no longer lists and `ollama rm`s their tags, and
  `ollama rm`s pulled cloud stubs that have no entry.

Things to know before reading a plan:

- **Real local models are never touched.** Only ollama cloud entries and
  `*:cloud` / `*-cloud` tags are in scope. An entry marked
  `location = "cloud"` whose tag is not a cloud tag is still removed from
  the registry when the page does not list it, with a `warning:` saying its
  tag is left in ollama; show the user that line.
- **No ollama provider row, no catalog.** On a registry that does not use
  ollama, plain `wt cloud-sync` prints `catalog: no ollama provider in the
  registry; nothing to mirror` and goes on with the prices. Asked for by
  name (`--only catalog`, or a catalog flag) it is an error, exit 1.
- **Real cloud tags.** A page name does not determine its tag: each bare
  name is resolved from `https://ollama.com/library/<name>/tags`. A model
  with no cloud tag, or several, is skipped with a `warning:` (nothing is
  added or pulled for it, and nothing with its name is removed); ask the
  user before hand-editing anything for it. An existing entry under the
  wrong tag is replaced (`re-tagged as …`); the replacement keeps the old
  entry's family, tags, extras and `model_info`, but its LiteLLM route name
  changes, because the route name is the model id.
- **Off-peak prices** are the `[[models.cost.time_prices]]` row labelled
  `off-peak`. Other rows are the user's and are kept.
- **Routes follow the registry.** There is no per-model routing step: the
  run ends with one sync (lines prefixed `routes:`) when a price or the set
  of models changed, or a tag was pulled or removed. A failed sync is a
  `routes: warning:`, never a failed run; re-run `wt litellm sync`. The sync
  is the last thing a run does: after a run that was interrupted (Ctrl-C, a
  crash) run `wt litellm sync` yourself.

Full reference: `wt/docs/wt-cloud-sync.md`.

## Steps

1. Dry run: `wt cloud-sync --dry-run`
   - Summarize **both** plans for the user. Prices: each `id: old -> new`.
     Catalog: price updates, registry additions (id and family; each also
     gets a route), pulls, registry removals (each also loses its route),
     and stray `ollama rm`s.
   - Point out every `warning:` line: an OpenRouter model with no match, a
     subscription disagreement, an unrecognized price cell, a model whose
     cloud tag did not resolve, a removed entry whose tag is not a cloud
     tag.
   - Ask whether any added model's family should be changed. If so, edit
     `family` in `registry.toml` after the sync, then `wt litellm sync`.
   - A non-zero exit here is one of the codes below. Nothing was changed.
2. Get the user's go-ahead for the whole of both plans, the removals above
   all.
3. Apply: `wt cloud-sync --yes --approve-removals <digest>`, with the
   `catalog: Removal digest:` the dry run printed. Omit `--approve-removals`
   if it printed none (the plan deletes nothing). Your Bash tool has no
   terminal, so the command's one confirmation cannot be answered; without
   `--yes` it stops with exit 1 and says so. Pass `--yes` only once the user
   has approved the dry run's plans.
   - Exit 5: the deletions are not the ones approved, or the registry
     changed after the plan was printed. The catalog changed nothing. Start
     again from step 1 and show the user the new plan.
   - Exit 4: more than half the ollama cloud entries would be removed. The
     catalog changed nothing. Check the dry run's page parse with the user,
     and add `--force` only if they confirm the removals are real. `--force`
     does not replace the digest.
   - With exit 4 or 5 the `prices:` flow may still have been applied. Read
     its lines before telling the user that nothing changed.
4. Check the routes with `wt litellm list`. After exit 1, a `routes:
   warning:` or an interrupted run, run `wt litellm sync` first if a new
   model has no route or a removed one still has one.

To run one flow only: `--only prices` or `--only catalog`.

## Exit codes

| Code | Meaning | Do |
|---|---|---|
| 0 | every selected flow finished, or had nothing to do | — |
| 1 | a step failed in either flow: the OpenRouter fetch, the registry write, a pull, an `ollama rm`, no terminal to confirm on, or the catalog asked for by name with no ollama provider row (a failed route sync is only a `routes: warning:`) | read the `error:` lines, then start again from the dry run (step 1): the next run only redoes what is still out of step, but a tag whose `ollama rm` failed is now a stray, so the removal digest may have changed. Then check `wt litellm list` |
| 2 | catalog changed nothing: page fetch failed, `--html` unreadable, `ollama list` could not run, or no cloud tag resolved | check the network, start ollama, retry. `--html <saved page>` replaces only the pricing-page fetch; the library tag lookups still need ollama.com |
| 3 | catalog changed nothing: the page changed shape | follow "Repairing the parser" |
| 4 | catalog changed nothing: mass removal refused | verify the parse, then `--force` with the user's OK |
| 5 | catalog changed nothing: removals not approved (no digest, another plan's digest, or the registry changed mid-run) | run the dry run again, get the user's OK for the new plan, use its digest |

Codes 2 to 5 are about the catalog flow only and win over 1.

## Repairing the parser (exit 3)

The error names the check that failed and a saved file
(`$TMPDIR/ollama-pricing-<timestamp>.html`). Work from `wt/`.

1. Open the saved HTML and find the pricing table.
2. Update **only** `internal/cloudsync/pricingpage.go`: `ParsePricing`, and
   `collectTables` / `mapColumns` if the structure moved. Keep every check:
   columns found by header text, at least `MinRows` rows, a model name that
   is an ollama tag, no duplicate row, every off-peak row has a base row,
   and most price cells parse. A check that fires is the parser working.
3. Replace `internal/cloudsync/testdata/ollama_pricing.html` with the saved
   page, update `TestParsePricingFixture`'s expected count and prices, and
   run `go test ./internal/cloudsync && make check`.
4. Try it on the page that broke it: `go run ./cmd/wt cloud-sync --only
   catalog --dry-run --html <saved page>`, then run the steps above.

If the page's off-peak **window** wording changes (it is not parsed), update
`offpeakRow` in `internal/cloudsync/catalog.go` and its test.
```

- [ ] **Step 3: Write the command reference**

Create `wt/docs/wt-cloud-sync.md`:

````markdown
# wt cloud-sync

Reference for `wt cloud-sync`, reached from [`wt/CLAUDE.md`](../CLAUDE.md). It brings `registry.toml` up to date with what two public services publish, and replaces `modelman refresh-prices` and `modelman ollama-catalog sync`.

```
wt cloud-sync [--only prices,catalog] [--dry-run] [--html FILE] [--yes]
              [--approve-removals DIGEST] [--force]
```

## The two flows

Both run unless `--only` names one. They are independent: a flow that fails or is refused does not stop the other, and every output line says which flow it is about (`prices:`, `catalog:`; the route sync's lines are `routes:`).

**prices** fetches `https://openrouter.ai/api/v1/models` and re-prices the registry's OpenRouter-priced models: an `openrouter` model, or a model of any other cloud provider that is not an agent's native provider. A model is matched on its `model_name`.

- Input and output prices always take OpenRouter's value. The cache price is replaced only when OpenRouter reports one. The subscription and any `time_prices` rows are never touched.
- Every matched model is stamped (`pricing_updated_at`), whether or not its price moved. That stamp is what wt's stale-pricing notice reads: after a launch wt says `token pricing last refreshed <date> — run 'wt cloud-sync'` once the newest stamp is more than 7 days old.
- A model OpenRouter does not list, lists with no price, or prices at `-1` ("varies") is a `warning:` and is left as it is.
- With no OpenRouter-priced model in the registry, nothing is fetched.

**catalog** mirrors `https://ollama.com/pricing` into three places: the registry's ollama cloud entries, the cloud tags `ollama list` shows, and (through the route sync) LiteLLM's routes.

- It updates prices, including the off-peak row; adds an entry for each new page model and `ollama pull`s it; removes entries the page no longer lists and `ollama rm`s their tags; and `ollama rm`s pulled cloud tags that have no entry.
- A page name does not determine its tag: each bare name is resolved from `https://ollama.com/library/<name>/tags` (`<name>:cloud`, else the single `<name>:<size>-cloud`). A model whose tag cannot be resolved is skipped with a `warning:` and nothing with its name is removed. An entry under a tag ollama does not publish is replaced by one under the real tag (`re-tagged as …`); the replacement is a copy, but its id, and so its LiteLLM route name, changes.
- Off-peak prices are stored as the `[[models.cost.time_prices]]` row labelled `off-peak` (UTC, weekdays outside 12:00–18:00, all day at weekends). Rows with any other label are yours and are kept. Nothing applies time prices at launch.
- Local models are never touched: only ollama cloud entries and `*:cloud` / `*-cloud` tags are in scope. An entry marked `location = "cloud"` whose `model_name` is not a cloud tag is removed from the registry like any other entry the page does not list, but its tag is never handed to `ollama rm`; the plan carries a `warning:` that says so.
- A registry with no `ollama` provider row has no catalog to mirror. Plain `wt cloud-sync` prints `catalog: no ollama provider in the registry; nothing to mirror` and the run goes on; the exit status is the prices flow's. When the catalog was asked for by name (`--only catalog`, `--only prices,catalog`, or any of `--html`, `--approve-removals`, `--force`) that is an error, exit 1, before anything is fetched. `wt model init` adds the row on a machine where `ollama` is on `PATH`.
- `ollama` is run as a command, pinned with `OLLAMA_HOST` to the address of the registry's ollama provider row.

## What a run does

1. Fetches and plans both flows. Nothing is locked or written.
2. Prints both plans. **`--dry-run` stops here.**
3. Applies the catalog's gates (below), then asks one question on the terminal, `Apply these changes? [y/N]`. Only `y` or `yes` applies. `--yes` skips the question; with no terminal and no `--yes` the run stops and says so.
4. Writes `registry.toml` once, for both flows. Under the file's lock each plan is made again from the file as it then is; a plan that is no longer the one printed is not applied (`… changed after the plan was printed … run it again`). One exception to "once": when the write is refused because a row one flow must change would not load (`invalid registry entry: model "<id>": …`), each flow is written on its own, so the broken row holds up only the flow that owns it; that flow's `error:` line names the row, and the run exits 1.
5. Runs the catalog's `ollama pull`s, then its `ollama rm`s. A failure is an `error:` line and exit 1, and the rest still run. A tag is removed only if it is a cloud tag, `ollama list` showed it and no remaining registry entry names it. A tag whose `ollama rm` failed is left pulled; its entry is already gone, so the next run lists it as a stray tag, and that run's removal digest may not be this one's: start again from `--dry-run`.
6. Syncs the LiteLLM routes once, if a price or the set of models changed or a tag was pulled or removed. A run that only re-stamped current prices does not sync, so it cannot restart the proxy. A sync that fails is a `routes: warning:`, never a failed run; re-run `wt litellm sync`. The sync is the last step: a run interrupted before it (Ctrl-C during a pull) leaves the routes behind the registry, and the next `wt cloud-sync` syncs them only if it still has a tag to pull or remove, so after an interrupted run, run `wt litellm sync`.

## Removals need approval

`--yes` never deletes on its own. When the catalog plan removes anything, the dry run prints

```
catalog: Removal digest: 0bcc560b956e (apply non-interactively with `--yes --approve-removals 0bcc560b956e`)
```

(an illustrative digest), and `--yes` applies that plan only with `--approve-removals` and that digest. The digest covers exactly the registry removals and the stray `ollama rm`s, so a page that changed since the dry run cannot delete a model nobody reviewed. The digest is the same one `modelman ollama-catalog sync` prints for the same plan.

A plan that would remove more than half the ollama cloud entries is refused outright unless `--force` is given: it is more likely a page that parsed wrong than a catalog that shrank. `--force` does not replace the digest.

## Exit status

| Code | Meaning |
|---|---|
| 0 | Every selected flow finished, or had nothing to do |
| 1 | A step failed in either flow (the OpenRouter fetch, the registry write, a pull, an rm, no terminal to confirm on, the catalog asked for by name on a registry with no ollama provider row), or a usage error |
| 2 | Catalog changed nothing: the page fetch failed, `--html` could not be read, `ollama list` could not be read, or no cloud tag resolved |
| 3 | Catalog changed nothing: the pricing page changed shape; its HTML is saved to `$TMPDIR/ollama-pricing-<timestamp>.html` and the path printed |
| 4 | Catalog changed nothing: mass removal refused without `--force` |
| 5 | Catalog changed nothing: removals not approved (no digest, another plan's digest, or the registry changed after the plan was printed) |

Codes 2 to 5 describe the catalog flow only, and win over 1. The prices flow may have been applied in the same run; its lines say so.

## Flags

| Flag | Meaning |
|---|---|
| `--only prices,catalog` | Run only the named flows (default: both) |
| `--dry-run` | Print the plans and change nothing |
| `--yes` | Apply without asking |
| `--approve-removals DIGEST` | catalog, with `--yes`: the digest a reviewed dry run printed |
| `--force` | catalog: allow a plan that removes more than half the ollama cloud entries |
| `--html FILE` | catalog: parse a saved pricing page instead of fetching it (the library tag lookups still need ollama.com) |

`--html`, `--approve-removals` or `--force` with `--only prices` is a usage error.

## What it reads and writes

- **Writes** `registry.toml` (prices, `cost.time_prices`, `catalog_name`, `pricing_updated_at`; ollama cloud rows added and removed) through wt's one registry writer, which keeps every key it was not asked to change. `config.yaml` only through the route sync. The ollama store through `ollama pull` and `ollama rm`.
- **Reads** the three public pages above, `ollama list`, and the registry.
- **A trial run against a copy of the registry** (`WT_REGISTRY=/path/to/copy`) must also set `WT_LITELLM_CONFIG`, or the route sync is refused with a warning (the registry write still succeeds). It still runs the real `ollama`: use `--dry-run`, or put a stand-in `ollama` first on `PATH`.
- There is no automatic refresh. wt never fetches prices on a launch.
````

- [ ] **Step 4: Point the rest of the docs at the command**

In `CLAUDE.md`, find:

```text
- `uv run --directory modelman modelman ollama-catalog sync [--dry-run]` — sync ollama cloud models + prices (incl. off-peak) from ollama.com/pricing; see the `ollama-catalog` skill in `modelman/.claude/skills/`
```

and replace it with:

```text
- `wt cloud-sync [--only prices,catalog] [--dry-run] [--yes --approve-removals DIGEST] [--force] [--html FILE]` — refresh OpenRouter prices and mirror ollama.com/pricing (cloud models, prices incl. off-peak, pulls and removals) into `registry.toml`, ollama and the LiteLLM routes; exit codes 0–5; see the `cloud-sync` skill in `wt/.claude/skills/` and `wt/docs/wt-cloud-sync.md`. (`modelman refresh-prices` and `modelman ollama-catalog sync` still work until modelman is deleted; a removal digest from either tool is good in the other.)
```

In `modelman/CLAUDE.md`, find:

```text
Flags, confirmation and exit codes 1–5: the `ollama-catalog` skill (`.claude/skills/ollama-catalog/SKILL.md`)
```

and replace it with:

```text
Flags, confirmation and exit codes 1–5 are the ones `wt cloud-sync` took over: the `cloud-sync` skill (`../wt/.claude/skills/cloud-sync/SKILL.md`), which replaced the `ollama-catalog` skill
```

In `wt/CLAUDE.md`, find:

```text
- `docs/wt-smoke.md`, `docs/wt-start-stop.md`, `docs/wt-stats.md` — command references
```

and replace it with:

```text
- `docs/wt-smoke.md`, `docs/wt-start-stop.md`, `docs/wt-stats.md`, `docs/wt-cloud-sync.md` — command references
```

In `wt/CLAUDE.md`, find:

```text
`internal/cloudsync` is the Go port of modelman's `pricing.py` and `ollama_catalog.py`: everything that decides what a sync changes, with no I/O. The command (`cmd/wt/cloudsync*.go`) owns the fetches, the ollama CLI, the confirmation and the exit codes.
```

and replace it with:

```text
`internal/cloudsync` is the Go port of modelman's `pricing.py` and `ollama_catalog.py`: everything that decides what a sync changes, with no I/O. The command (`cmd/wt/cloudsync*.go`) owns the fetches, the ollama CLI, the confirmation and the exit codes. User-facing behavior: [docs/wt-cloud-sync.md](docs/wt-cloud-sync.md); the operator's procedure, including the parser repair after an exit 3, is the `cloud-sync` skill (`.claude/skills/cloud-sync/`).
```

In `docs/guides/02-providers-and-models.md`, find:

```text
**wt** = `wt model init` (the file and its provider rows), `wt litellm …` (routes),
```

and replace it with:

```text
**wt** = `wt model init` (the file and its provider rows), `wt cloud-sync` (OpenRouter prices, and ollama's cloud models with their prices), `wt litellm …` (routes),
```

In `docs/guides/02-providers-and-models.md`, find:

```text
The first tool that changes the file — `wt model init`, or a modelman subcommand
```

and replace it with:

```text
The first tool that changes the file — `wt model init`, `wt cloud-sync`, or a modelman subcommand
```

In `docs/guides/02-providers-and-models.md`, find:

```text
for OpenRouter models leave it out and run `uv run modelman refresh-prices`, which fetches the per-token prices and writes them. Ollama's `:cloud` models and their prices are added and removed by `uv run modelman ollama-catalog sync` (the `ollama-catalog` skill) — no hand edit.
```

and replace it with:

```text
for OpenRouter models leave it out and run `wt cloud-sync --only prices`, which fetches the per-token prices and writes them. Ollama's `:cloud` models and their prices are added and removed by `wt cloud-sync`'s catalog flow (the `cloud-sync` skill; start with `wt cloud-sync --dry-run`) — no hand edit. wt reminds you after a launch when the OpenRouter prices are more than a week old.
```

In `docs/guides/02-providers-and-models.md`, find:

```text
takes its price from OpenRouter — `modelman refresh-prices` refreshes it, and wt reminds you when that refresh is stale.
```

and replace it with:

```text
takes its price from OpenRouter — `wt cloud-sync` refreshes it (`modelman refresh-prices` still does too), and wt reminds you when the newest refresh is more than a week old.
```

In `docs/guides/02-providers-and-models.md`, find:

```text
Pulling/removing ollama `:cloud` stubs is the job of `modelman ollama-catalog sync` (see the `ollama-catalog` skill), not `modelman sync`.
```

and replace it with:

```text
Pulling/removing ollama `:cloud` stubs is the job of `wt cloud-sync` (see the `cloud-sync` skill), not `modelman sync`.
```

In `docs/guides/02-providers-and-models.md`, find:

```text
`wt start`, `wt stop` and `wt model init` update the routes themselves,
```

and replace it with:

```text
`wt start`, `wt stop`, `wt model init` and `wt cloud-sync` update the routes themselves,
```

In `docs/guides/02-providers-and-models.md`, find:

```text
ollama `:cloud` stubs are managed by `modelman ollama-catalog sync`, not `sync`.
```

and replace it with:

```text
ollama `:cloud` stubs are managed by `wt cloud-sync`, not `sync`.
```

In `docs/guides/04-litellm-config.md`, find:

```text
- **modelman** runs one `wt litellm sync` after each subcommand that can change routing
```

and replace it with:

```text
- **`wt cloud-sync`** ends with one sync when a price or the set of models changed or it pulled or removed an ollama tag, and with none when it only re-stamped prices that were already current (a sync can restart the proxy). Its sync lines are prefixed `routes:`.
- **modelman** runs one `wt litellm sync` after each subcommand that can change routing
```

- [ ] **Step 5: Check the prose against the binary**

From `wt/`: `go run ./cmd/wt cloud-sync --help`. Read the exit-status table and the six flags there against `wt/docs/wt-cloud-sync.md` and the skill's table; they must say the same thing. Then, from the repo root:

```bash
make check-links
grep -rn "skills/ollama-catalog\|ollama-catalog. skill" --include='*.md' . | grep -v "docs/superpowers\|modelman/docs\|docs/archive"
```

Expected: `ALL LINKS OK`, and the `grep` prints nothing (no document still points at the old skill; mentions of the `modelman ollama-catalog sync` command itself are fine and remain).

- [ ] **Step 6: Verify the slice**

From the monorepo root: `make test-all` (exit 0; this slice changes no code) and `make lint` (exit 0).

- [ ] **Step 7: Commit**

From the repo root:

```bash
git add -A modelman/.claude/skills modelman/CLAUDE.md wt/.claude/skills wt/docs/wt-cloud-sync.md CLAUDE.md wt/CLAUDE.md docs/guides/02-providers-and-models.md docs/guides/04-litellm-config.md
git commit -m "docs(wt): the cloud-sync skill (moved from modelman's ollama-catalog), the command reference, the guides"
```

PR 4 is ready. **Ask the owner before pushing or opening the PR.**

---

### Task 13 (owner-gated; run by the controller, not by an implementer subagent): one live dry run from a scratch registry

**When:** after Task 11 is committed and before PR 3 merges. This is the spec's "Live checks before merge, done by hand: one `wt cloud-sync` against the real services from a scratch registry".

**What it does:** one `wt cloud-sync --dry-run` of the built binary against the real services, from a copy of the registry, and modelman's dry run on the same copy for comparison. It reads the real registry once (to copy it), three public sites, and the real ollama daemon's list. **It changes nothing real:** no `ollama pull`, no `ollama rm`, no write to the real registry, `config.toml` or `config.yaml`. The decisions table says why the apply is not run here: Task 11, Step 8 proves the apply end to end with a stand-in `ollama`, and the first real apply is the owner's own `wt cloud-sync` after the merge, which writes the real registry through its lock and so keeps the registry and ollama in step.

**Files:** none are changed in the repo.

- [ ] **Step 0: Get the owner's OK before reading anything real**

Tell the owner what this task reads: their `~/.config/local-ai/registry.toml` (copied once into a temp directory, never printed as a file), the output of `ollama list` on their daemon, and three public sites. Tell them what its output contains: the plan lines name the registry's model ids and prices. **Stop here until the owner says to go on.** Nothing below runs without that.

- [ ] **Step 1: Build, and make the scratch home**

From `wt/`, in the checkout of the Task 11 commit:

```bash
LIVE="$(mktemp -d)"
go build -o "$LIVE/wt" ./cmd/wt
mkdir -p "$LIVE/home/.config/local-ai" "$LIVE/home/.config/litellm"
REG="$LIVE/home/.config/local-ai/registry.toml"
cp ~/.config/local-ai/registry.toml "$REG"
cp "$REG" "$LIVE/registry.before"
printf 'model_list: []\n' > "$LIVE/home/.config/litellm/config.yaml"
ORIGIN="$(sed -n '/^id = "ollama"$/,/^\[\[/s/^base_url = "\(.*\)"$/\1/p' "$REG" | head -1)"
ORIGIN="${ORIGIN:-http://localhost:11434}"
export UV_CACHE_DIR="$(uv cache dir)" UV_PYTHON_INSTALL_DIR="$(uv python dir)"
live() { HOME="$LIVE/home" XDG_CONFIG_HOME="$LIVE/home/.config" WT_REGISTRY="$REG" \
  WT_LITELLM_CONFIG="$LIVE/home/.config/litellm/config.yaml" WT_LITELLM_RESTART_CMD=true "$LIVE/wt" "$@"; echo "exit=$?"; }
mm() { HOME="$LIVE/home" XDG_CONFIG_HOME="$LIVE/home/.config" MODELMAN_REGISTRY="$REG" MODELMAN_STATE="$LIVE/home/modelman.toml" \
  WT_LITELLM_CONFIG="$LIVE/home/.config/litellm/config.yaml" WT_LITELLM_RESTART_CMD=true OLLAMA_HOST="$ORIGIN" \
  uv run --directory modelman modelman "$@"; echo "exit=$?"; }
```

The first `cp` is the one read of the real registry; nothing prints it. `live` and `mm` both run under the throwaway `HOME` and `XDG_CONFIG_HOME` with `WT_LITELLM_CONFIG` naming the scratch file and `WT_LITELLM_RESTART_CMD=true`, as the Global Constraints require of every run of wt or modelman, so neither can reach the real registry, state file or `config.yaml`, or restart the proxy. `PATH` is the real one: `ollama` is the real ollama, and the only subcommand a dry run gives it is `list`.

`ORIGIN` is the address in the copy's ollama provider row (the default when the row names none); it is read into a variable and not printed. wt pins `OLLAMA_HOST` to that address itself. modelman does not, so `mm` sets it: without that, in a shell that exports another `OLLAMA_HOST`, the two tools would list two different daemons and their plans would differ for a reason that is not a bug. `UV_CACHE_DIR` and `UV_PYTHON_INSTALL_DIR` keep `uv` on its real cache and its installed Pythons under the scratch `HOME`; without them it downloads both again into the temp directory.

These commands were rehearsed in scratch with a stand-in `ollama` and a made-up registry and page (`--html`): both tools printed the same plan and the same digest, the stand-in recorded `list` twice with `OLLAMA_HOST` equal to the registry row's address, the copy was unchanged, and modelman created no state file.

- [ ] **Step 2: The live dry run**

```bash
live cloud-sync --dry-run | tee "$LIVE/dry.out"
cmp "$LIVE/registry.before" "$REG" && echo "registry copy unchanged"
```

Expected: `exit=0`; a `prices:` plan and a `catalog:` plan; `registry copy unchanged`. It fetches `openrouter.ai/api/v1/models`, `ollama.com/pricing`, and one `ollama.com/library/<name>/tags` page for each bare page name whose tag is not already pulled and recorded, and it runs the real `ollama list`. (If the copy has no ollama provider row, the catalog plan is the one line `catalog: no ollama provider in the registry; nothing to mirror`, and there is no catalog to compare below.)

Then modelman's plan from the same copy, the same minute, from the repo root:

```bash
mm ollama-catalog sync --dry-run > "$LIVE/mm.out"
diff <(sed -n 's/^catalog: //p' "$LIVE/dry.out") <(grep -v '^exit=' "$LIVE/mm.out") && echo "the two plans are the same text"
```

Expected: `the two plans are the same text`, **the removal digest included**. (A dry run writes nothing, and `mm` reads only the copy.) Three differences are this plan's own and are not porting bugs; each is in the decisions or porting table:

- an entry with no cost table whose page row has no prices: modelman lists it under `Price updates`, wt counts it unchanged;
- a `warning:` about an unrecognized price cell quotes the cell as Go's `%q` does in wt and as Python's `repr` does in modelman;
- a `warning:` in wt alone, for a removed entry whose tag is not a cloud tag.

Any other difference is a porting bug: stop and report it.

Check, and note for the PR:

- Any `warning:` line, and whether it is true.
- Exit 3 here means the page changed shape since 2026-10-07: follow the skill's "Repairing the parser" and re-run. Exit 2 means a fetch or `ollama list` failed.

- [ ] **Step 3: Show the owner, record the result, clean up**

Give the owner `dry.out` and the result of the comparison. In PR 3's description record: that the live dry run exited 0; the number of models on the page and in each section of both plans; that the two tools' plans and digests were the same text (or which of the three known differences appeared); and every warning. Put the raw output in the PR only if the owner says so: it names the registry's models.

```bash
rm -rf "$LIVE"
```

The real apply is not part of this plan's tasks. After PR 3 merges the owner runs it on their own machine with no redirects: `wt cloud-sync --dry-run`, then `wt cloud-sync` (answering the question) or `wt cloud-sync --yes --approve-removals <digest>`. One thing no scratch run could establish, because none may touch the real ollama: whether `ollama pull` of a cloud model needs the user to be signed in. If it does, the pulls fail with ollama's message, the run exits 1 with the registry already written, and the next run retries only the pulls.

---

## Spec Coverage

Checked against the spec's Step 4 section with the finished plan in hand.

| Spec requirement | Where |
|---|---|
| The command line and its flags | Task 9 (`--only`, `--dry-run`, `--yes`), Task 11 (`--html`, `--approve-removals`, `--force`) |
| New pure package `wt/internal/cloudsync` | Tasks 1–5 |
| Prices: fetch, match on `model_name`, plan, `--dry-run` lists `id: old -> new` | Task 4 (`PlanPrices`, `Format`), Task 9 (`TestCloudSyncPricesDryRun`) |
| Catalog: parser on the tokenizer, every fail-loud check, HTML saved | Task 1; Task 11 (exit 3 case) |
| Cloud-tag resolution against `ollama.com/library` | Task 2 |
| The planner, the mass-removal guard, the removal digest | Task 3; gates in Task 11 |
| Sequence: plan unlocked, print, gate, one `UpdateRegistry` with re-plan and refusal, ollama, one sync | `runCloudSync`, Tasks 9 and 11; both flows in one run: Task 11, `TestCloudSyncBothFlowsInOneRun` (one question, both applied, one sync; one plan stale and the other applied, in both directions; OpenRouter down and the catalog applied) |
| CLI pinned with `OLLAMA_HOST`, not the HTTP API | Task 10 |
| Entry removed in the write, tag afterwards; only if no remaining entry names it; a failed rm leaves a stray | Task 5 (`CatalogApplied.Removes`), Task 11 (`runOllamaWork`, `…OllamaFailuresExit1AndTheRestStillRuns`) |
| Exit codes 0 to 5; 2 to 5 catalog only and over 1; flows independent; prefixes; the usage error | Task 11 (`…ChangesNothingAndSaysWhy`, `TestCloudSyncFlowsAreIndependent`, `TestCloudSyncBothFlowsInOneRun`, `TestCloudSyncWritesEachFlowAloneWhenARowWouldNotLoad`), Task 9 (`TestCloudSyncCommandRefusals`, `TestCloudSyncPrefixesTheRouteSyncsLines`) |
| A typed exit-code error; every other command keeps 1 | Task 8 |
| Nothing stored; notice from the newest `pricing_updated_at` among OpenRouter-priced models | Tasks 6 and 7 |
| Prices flow stamps every matched model | Task 5 (`TestPricesApplyStampsEveryMatchedModel`), Task 9 (`…StampOnlyRun…`) |
| `wt model edit` does not stamp | Step 3's; noted under "If Step 3 Lands First" |
| Sync only when a price or the model set changed | Tasks 9 and 11 (`…StampOnlyRun…`, second half of `…ApplyUnderTheApprovedDigest`); the one added case, a run with ollama work, is in the decisions table (`TestCloudSyncFinishesAnInterruptedRun`) |
| Notice after more than 7 days or when absent, naming `wt cloud-sync` | Task 7 |
| `config.PriceRefreshLastRun` and its read removed | Task 7 |
| `time_prices` written by the catalog, preserved by every write, applied by nothing | Task 3 (`offpeakRow`, the in-place rule), Task 4 (`TestPlanPricesMergeRules`, the plan keeps the rows), Task 5 through the registry writer: `TestPricesApplyStampsEveryMatchedModel` (a user's row byte for byte beside a price that moved and one that did not) and `TestCatalogApplyWritesWhatThePlanSays` (the same row byte for byte, the off-peak row added after it) |
| `golang.org/x/net` direct; tidy, build, full suite; the diff recorded | Task 1, Step 6 |
| Skill moved and rewritten; root `CLAUDE.md` in the same PR | Task 12 |
| Four PR slices | "PR Slices" |
| Testing: ported behaviour test-first from the Python tests | "What Is Ported From Python"; every task writes its tests first |
| Testing: redirected registry with and without `WT_LITELLM_CONFIG` | Task 9 (`TestCloudSyncUnderARedirectedRegistry`) |
| Testing: no live calls; seams | Tasks 9 and 10 (`TestMain`, the two fail-closed tests) |
| Testing: one live run from a scratch registry | Task 13 (a dry run; the decisions table says why the apply is not run there) |
| Risk "scraped pages": the parser and the gates ported in full | Tasks 1, 3, 11 |
| Risk "dependency bump" verified on a branch | Task 1, Step 6 |

What the spec does not ask for and this plan adds, each in the decisions table: `config.ReadRegistryDoc`; the rule for a registry with no ollama provider row (skipped by default, an error by name); writing each flow on its own when the shared write is refused because a row would not load; the route sync after a run that had only ollama work; never handing a tag that is not a cloud tag to `ollama rm`; and skipping a `pricing_updated_at` more than a day ahead of the clock.

Types are consistent across tasks. `cloudSyncOpts`, `cloudSyncOutcome` and `cloudSyncApplied` are defined in Task 9 and redefined, whole, in Task 11's replacement of the file; the tests of Task 9 use only the fields Task 9 defines (`prices`, `dryRun`, `yes`) and keep compiling after Task 11. `runCloudSync` has one signature throughout. `writeCloudSync(prices, catalog, now)` exists from Task 11 only; Task 9 writes inside `runCloudSync`. `withRegistryHint`, `runCSFinal` and `noTerminal` are defined in Task 9 and used unchanged in Task 11. `agents.LastPriceRefresh` takes `(cfg, now)` in Task 7 and is called so in Task 9's test. `CatalogPlan.Apply` takes `(doc, pulled, now)` in Task 5 and is called so in Task 11; `PricePlan.Apply` takes `(doc, now)`. `ResolveCloudTags` returns `(map[string]string, []string)`, which `catalogRun` keeps as `resolved` and `tagWarnings`. `ollamaCLI` returns `(stdout, stderr string, err error)` in the seam, in `stubOllama` and in `TestMain`.

## After Step 4

Nothing in this plan deletes Python. Step 6's deletion PR removes `modelman/src/modelman/pricing.py`, `ollama_catalog.py`, `ollama_catalog_cli.py`, the `refresh-prices` command and their tests, and with them the reason `RemovalDigest` must match modelman's: `TestRemovalDigestMatchesModelman` then pins a format wt alone owns, and its comment should say so. The same PR deletes `internal/config/modelman.go`, which after this step reads only the legacy `[litellm]` table. `cloudsync.OpenRouterPriced` and `config.Config.OpenRouterPriced` stay two functions over two views of the registry (rows and the typed config), held together by the contract fixture. `RegistryDoc.SetTimePrices` (Step 2) has no production caller after this step: `cloudsync` writes `cost.time_prices` through `PatchModel` (see the decisions table) and Step 3's plan does not call it either. It is left in place, with its tests; if nothing has come to use it by Step 6, that PR removes it.

Left for whoever touches this next, and deliberately not done here: `wt cloud-sync` has no `--json` (nothing reads it; the skill reads the text); time-windowed prices are stored and not applied; and the route sync at the end of a run probes the ollama daemon the registry names, so a trial run against a copy of the registry still reads the real daemon's model list.
