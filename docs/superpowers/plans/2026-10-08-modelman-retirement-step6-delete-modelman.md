# Modelman Retirement, Step 6 (Delete modelman) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `modelman/` is deleted, with everything that exists only for it (its CI job, its Makefile lines, the bridge-only `wt start --json`/`--plan`, wt's and llmbench's reads of `modelman.toml`, llmbench's retired llamacpp backend), and no guide, skill, `CLAUDE.md` or wt message tells anyone to run `modelman`.

**Architecture:** Eight PRs in a fixed order. Prose goes first (every replacement command already exists on `main`), so no reader is sent to something that is gone. Then the two tests whose only copy lives in modelman are ported to llmbench, then modelman is deleted, then the code that only modelman called is removed from wt and llmbench, one behaviour change per PR, each pinned by a test written first. Every PR ends with a proof: a grep with its exclusions spelled out and the output it must print, the build and test commands, and the link check.

**Tech Stack:** Go 1.26.7 (module root `wt/`), Python 3.13 with uv (`llmbench/`), GNU make, GitHub Actions, Markdown. No new dependency. No new command.

**Spec:** [docs/superpowers/specs/2026-10-06-modelman-retirement-design.md](../specs/2026-10-06-modelman-retirement-design.md), section "Step 6 — retirement" (the binding authority: the mapping table, the six PR slices, "What stays", "Left on disk", "Issues"), read with Decisions, End state, Testing and Risks. Steps 0, 1, 2, 3 and 5 are merged. Step 4's PRs 1 to 3 are merged; its PR 4 (docs and the skill) is in flight and must be merged before this plan starts: see "What Step 4's PR 4 Already Does".

**Verified against:** `main` at `544cc1e` (2026-10-08), and the docs branch `docs/wt-cloud-sync-skill` at `c935eb8` for every guide, `CLAUDE.md`, `wt/docs` and README line number below (those files are quoted as that branch leaves them; the branch was still open, so re-take a number that no longer matches from the text quoted beside it). The code blocks of Tasks 6, 8, 9, 10, 11 and 12 were applied in a scratch copy of `544cc1e` (git-tracked files only) and their build, lint and test runs observed there, under `env -i` with a throwaway `HOME`; Tasks 6, 8, 9 and 12 were applied a second time, in that order, when the plan was reviewed, and the counts under "What Was Measured" are from that run. The whole Go suite and `make test-all` were not run in scratch: the scratch runs cover the packages each task names. The prose edits of PR 1 were not applied to a tree: their expected counts are the lines each step lists as left, counted. If `main` has moved, the "find" text of an edit may no longer match: re-read the file and apply the same change.

## Global Constraints

- Success criteria, from the spec: (1) `modelman/` no longer exists, and no guide, skill, `CLAUDE.md` or wt message tells anyone to run `modelman`; (3) every modelman command has a named replacement or is listed as dropped; (5) `make test-all` passes at the end of every step.
- **The tree builds and its tests pass at every commit**, not only at the end of a PR. A task that deletes a file names, in the same step, the test, the doc section, the fixture, the Makefile line and the CI job that go with it.
- No `modelman` shim is added. `modelman` was reachable only through `uv run --directory modelman modelman`, which fails by itself once the directory is gone.
- **What stays, permanently:** `wt warm` and `wt served`; the env aliases `MODELMAN_REGISTRY`, `MODELMAN_LITELLM_CONFIG`, `MODELMAN_LITELLM_RESTART_CMD`, `MODELMAN_LITELLM_DATABASE_URL`, each read after its `WT_` name; the contract fixtures `registry.sample.toml` and `registry.written.sample.toml` as cross-language (Go and llmbench). The other fixtures become Go-only, with their headers corrected.
- **Left on disk, never deleted by wt:** `~/.config/local-ai/modelman.toml` (its legacy `[litellm]` table holds an API key), its `.bak-*` copies, `settings.yaml`, legacy `config.yaml` and `families/`. Guide 08 documents the manual removal.
- The mapping table in guide 08 is the spec's, row for row, with the additions Decisions 3, 4 and 27 name (two rows added, `stop --all` given a row of its own, and three cells that say more than the spec's). It is the only source for a command substitution in any guide.
- Guides never embed live model state: no counts, no routing snapshots, no "today that's X". Show the command that reveals the state; label any captured output as illustrative or dated.
- **Never, in any step:** read, write or print the developer's real `~/.config/local-ai/registry.toml`, `modelman.toml`, `~/.config/agent-wt/config.toml` or `~/.config/litellm/config.yaml`; print an API key; start, stop, isolate or probe a real provider. Any run of a built `wt` or of `llmbench` outside its test suite uses a throwaway `HOME` and `XDG_CONFIG_HOME`, `WT_REGISTRY` and `WT_LITELLM_CONFIG` on scratch files, `WT_LITELLM_RESTART_CMD=true` and a stand-in `ollama` first on `PATH`.
- No live calls in tests. Every `Test*` function (Go) has a top-level `//` comment, and every new Python test a docstring, saying what it tests and why a regression matters to a user.
- Run every Go command from `wt/`, every `uv` command from `llmbench/`, and every `make lint`, `make test-all`, `git` and `bin/check-links` command from the monorepo root.
- Per PR that touches Go, from `wt/`: `test -z "$(gofmt -l .)" && go vet ./... && go test -count=1 ./... && make check`. Per PR that touches llmbench, from `llmbench/`: `make check && make test` (coverage floor 90%). At the end of every PR, from the root: `make test-all` and `make lint`.
- Two known test-environment hazards, neither a Step 6 defect (#317, #318): `internal/tui`'s `TestOllamaWarnShownWhenUnavailable` runs the real `ollama list`, and `internal/configeditor`'s `TestModelFormRefusesAnEditOfADuplicatedID` fails under a long `TMPDIR`. **Which rule wins:** "never probe a real provider" holds for everything an implementer runs by hand. The Go suite's own `ollama list` is the one named exception: it is read-only (it lists pulled models and starts nothing), `make test-all` is criterion 5, and #317 is the issue that removes it. An implementer who must not reach the daemon at all puts a stand-in `ollama` (a script that exits 1) first on `PATH` and expects exactly one failure, `TestOllamaWarnShownWhenUnavailable` with `expected phaseOllamaWarn, got 2` (observed in scratch). Use the default `TMPDIR` either way.
- Commit messages follow the repo's conventional style (`docs: …`, `refactor(wt): …`, `chore: …`), with the attribution trailer your session is told to add, if any.
- One branch per PR, off an up-to-date `main`. **Pushing a branch, opening a PR, merging a PR, closing or commenting on an issue need the owner's OK.** Do not push, do not run `gh pr create`, `gh pr merge`, `gh issue close` or `gh issue comment` until the owner says so. An OK to open a PR is not an OK to merge it: the owner merges each PR, or says so for that PR. Those steps are the controller's, never an implementer subagent's.
- **Commits name their paths.** The checkout is shared with other sessions: before every commit run `git status --short` and stop on any path the task did not create or edit. No step uses a bare `git add -A`.
- Read `CLAUDE.md`, `wt/CLAUDE.md` and `llmbench/CLAUDE.md` before starting.

## Review Focus

1. **A machine with no `~/.config/agent-wt/config.toml`, whose LiteLLM routing state lives only in `modelman.toml`.** Today wt uses that state in memory on every run. After PR 5 it does not. Expected: routing reads as off (`wt litellm status` says so), nothing is written, no key is copied anywhere, and the changelog and guide 08 say what to run (`wt litellm set …`, `wt litellm on`). Pinned in Task 11, `TestLoadNeverReadsModelmanToml` ("no config.toml").
2. **A `modelman.toml` that is not valid TOML, or not readable.** Today every wt command fails with `parse modelman.toml: …`. Expected after PR 5: the file is not opened, and wt runs. Pinned in Task 11, `TestLoadNeverReadsModelmanToml` ("modelman.toml is not TOML").
3. **A script that still passes `wt start <id> --json` or `--plan`.** Expected after PR 4: exit non-zero with `unknown flag: --json` (or `--plan`) and nothing started or unloaded; a flag accepted and ignored would start a model where the script asked for a dry run. Pinned in Task 10, `TestStartHasNoJSONOrPlanFlag`.
4. **A script or habit that still names the retired backend: `llmbench provider isolate llamacpp`, `llmbench provider stop llamacpp`, `LLM_ISOLATE_LLAMACPP_MODEL`.** Expected after PR 3c: `unknown provider: llamacpp`, before any other provider is stopped. Pinned in Task 9, `test_isolate_rejects_the_retired_llamacpp_id` and `test_stop_rejects_an_id_no_backend_has`.
5. **`XDG_CONFIG_HOME` set, with the registry still at the pre-XDG `~/.config/local-ai/registry.toml`.** wt has never read that file in this case; llmbench did, to agree with modelman. Expected after PR 3b: llmbench reports `Registry file not found: <the XDG path>`, the same file wt names, instead of benchmarking a registry wt cannot see. Pinned in Task 8, `test_read_path_has_no_second_place_to_look`.
6. **A user who ran benchmarks only through modelman and has no `~/.config/local-ai/benchmarks/latest.toml`.** Expected after PR 5: `--latest` says there is no latest run (results stay on disk), a stale pointer in `modelman.toml` never comes back, and guide 08 says how to carry the four keys over before deleting the file. Pinned in Task 12, `test_modelman_toml_is_never_read`.
7. **An error from `wt warm` that carries a secret on its command line.** llmbench's `_msg` builds every bridge error from wt's output and never from argv. Its only table of cases lives in modelman's tests. Expected: the table lives in llmbench before modelman is deleted. Pinned in Task 6, `test_msg_is_one_clean_line_and_never_shows_argv`.

---

## Decisions This Plan Makes

The spec states the end state; these are the choices it leaves open and the places the inventory found something it did not foresee. Each has a recommendation, which is what the tasks implement. The ones that need the owner's answer first are repeated under "Questions for the Owner".

| # | Question | Decision | Why |
|---|---|---|---|
| 1 | The spec's slice 3 is one PR. | Three: **3a** ports two tests into llmbench, **3b** deletes modelman, **3c** removes llmbench's retired backend and the API only modelman called. | 3a must merge before the deletion so nothing is protected only by modelman's suite. 3c changes behaviour (`stop-all`, `provider list`) and deserves a diff a reviewer can read; 3b's diff is 170 deleted files. |
| 2 | Slice 1 (docs) lands before slices 4 and 5, but some sentences only become true then. | PR 1 changes every sentence that is about **which command to run**. A sentence about a fact a later PR changes is edited in that PR: the `--plan`/`--json` text in PR 4; the `modelman.toml` `[litellm]` fallback text and guide 08's removal section in PR 5; the `modelman/` tree, CI, Makefile and llamacpp text in PRs 3b and 3c. Each task lists what it leaves and for which PR. | The other order (write the end state in PR 1) would have guide 08 tell a reader to delete a file wt still reads a key from. |
| 3 | The mapping table has no row for stopping an `mlx_lm_server` pairing. | Add: `stop <mlx_lm_server pairing>` → `llmbench provider stop mlx_lm_server`. | `wt stop --help` already says wt does not stop a pairing and names that command. |
| 4 | `usage report --days N`, the Markdown output, the Reconciliation sections and the "Last wt launch" line are listed as dropped only in Step 5. | Add a table row: dropped, with `wt stats --window 1d\|7d\|30d` and `wt stats --json` named as what is left. | Criterion 3: every command has a replacement or is listed as dropped, in one table. |
| 5 | Slice 5 says "delete `internal/config/modelman.go`", but `LitellmState` (wt-owned, used across wt) is defined there. | Move the type into `config.go` beside `UpdateLitellm`, then delete the file. | A plain deletion does not compile. |
| 6 | Should wt warn a user whose routing state is only in `modelman.toml`? | No. wt stops opening the file. `CHANGELOG.md` and guide 08 say what to do. | wt has migrated the table into `config.toml` on every load since 2026-09-21 whenever `config.toml` exists, so only a machine with no `config.toml` at all is affected; a warning would need the read this slice removes. |
| 7 | `llmbench/src/llmbench/state.py` still reads `modelman.toml`'s `[benchmarks]` table (via `MODELMAN_STATE`) while `latest.toml` is absent. The spec says the file is "read by nothing". | Remove the fallback and `MODELMAN_STATE` in PR 5, with wt's. | Makes the spec's claim, and guide 08's "safe to delete", true. Cost: a user with no `latest.toml` loses the `--latest` pointer to a pre-carve-out run; the results stay on disk and guide 08 says how to copy the four keys. |
| 8 | `MODELMAN_BENCHMARK_WORKLOAD` and `MODELMAN_AGENT_DEBUG` are not among the four permanent aliases. | Keep both, permanently, as Step 1 says ("kept as aliases"). Guide 05 and `llmbench/CLAUDE.md` keep naming them. | Two `env_first` calls; removing them breaks a shell profile for nothing. |
| 9 | Slice 4 names `start_json.go` and its test; two more things break. | The `sessionCounts` seam moves to `start.go` (the interactive replace prompt uses it), and `TestStartJSONOnALoadingModelPlansNothingAndWaits` in `loading_test.go` is deleted. | Verified in scratch. #259 for the interactive path stays covered by `TestStartOnALoadingModelWaitsInsteadOfSayingAlreadyRunning`. |
| 10 | After slice 4 the exported `lifecycle.Evictions` has no production caller; eleven test assertions call it. | Move the eight-line function, unchanged in name and body, into `internal/lifecycle/evictions_test.go`. | No dead exported function ships, and no test call site changes. Verified in scratch. |
| 11 | `wt served` and `wt litellm providers` (and the `--json` forms of `wt litellm`) have no caller in the repo once modelman is gone. | Keep all of them. The docs describe `wt served` and `wt litellm providers` as diagnostics; comments stop naming modelman as their reader. | The spec keeps `wt served` by name and lists `litellm-cli.sample.json` among the fixtures that stay. A CLI removal is a breaking change nobody asked for. |
| 12 | `saveFull` in `wt/internal/config/migrate.go` writes provider and model sections into `config.toml` during the `models.conf` migration "so `modelman migrate` can import them". | Leave the behaviour; reword the two comments to say the sections are inert and nothing reads them. | Changing a legacy-of-legacy migration is not Step 6's job. Guide 00 already says to treat those blocks as inert. |
| 13 | `docs/contracts/rotation.sample.state` is read only by a modelman test. | Delete it in PR 3b. | It cannot "become Go-only": no Go test reads it. |
| 14 | No slice owns the roughly 280 comment lines and five test names in wt's test files that describe modelman as live. | PR 3b fixes the ones that cite a path it deletes (eight files). The rest go in PR 6, which becomes "the test-file sweep": the env move and the comments. | Slice 2 is non-test Go by the spec's own closing grep. |
| 15 | Slice 6: most `t.Setenv("MODELMAN_REGISTRY", "")` calls clear an inherited value. | In a package whose `TestMain` calls `config.IsolateConfigHomeForTest` (which unsets both names for the process) the clear is deleted. Elsewhere it stays and gains a `WT_REGISTRY` clear beside it. A call that **sets** a path switches to `WT_REGISTRY`. | The alias stays readable forever, so a test that clears only `WT_REGISTRY` no longer clears the alias. |
| 16 | No slice sweeps llmbench's comments (about 25 lines in 16 source files), `llmbench/CLAUDE.md`, `bin/` comments, `wt/docs/**`, `wt/CLAUDE.md`, `docs/reference/**` or the `adding-a-benchmark-backend` skill. | Prose that tells a reader what to run: PR 1. Comments about code a PR changes: that PR. llmbench source comments: PR 3c. `bin/` comments: PR 3b. | Criterion 1 covers every `CLAUDE.md` and skill. |
| 17 | With llamacpp gone, `BACKENDS` and `SUPPORTED_PROVIDER_IDS` hold the same ids, `Backend.respects_solo` is always true and `stop()`'s "provider not supported for stop" branch is unreachable. | Remove the attribute and the branch; define `SUPPORTED_PROVIDER_IDS = frozenset(BACKENDS)` and keep the name. | The name is re-exported and filtered on by the benchmark runners. One test pins that the two sets are one. |
| 18 | `benchmark/isolation.py`'s `stop_all_local_providers()`, `stop_provider()` and `isolate_provider(solo=…)` have no caller left. | Delete them and their six tests. `llmbench provider isolate --solo` is untouched: it calls `lifecycle.isolate` directly. | Their only caller was `modelman/src/modelman/local_control.py`. |
| 19 | Removing the llamacpp backend stops `stop-all` and `isolate` from unloading a `local.llamacpp.server` LaunchAgent whose plist still exists. | Accept, and say so in `docs/reference/provider-artifacts.md`, whose re-enable procedure now starts with "restore the backend module from git history". | The provider was retired on 2026-09-07 and its plist archived. Question 2 asks the owner to confirm no such plist is installed. |
| 20 | modelman's tracked docs outside `docs/superpowers` (README, `CLAUDE.md`, `issues.md`, `docs/ROADMAP.md`, five `docs/internals` files) are not moved by the spec. | Delete them with the tree. | They describe deleted code; git history keeps them. The 39 plan and spec files move, as the spec says. |
| 21 | `git rm -r modelman/` leaves the directory on disk: `.venv` (about 149M) and caches are git-ignored. | Task 7 ends with a manual `rm -r modelman` of the leftovers in the working checkout, and guide 08's removal section says the same for every other checkout. | Criterion 1 says the directory no longer exists. |
| 22 | #260 is a defect of `runStartJSON`, which survives PR 3b. | Close #260 when PR 4 merges, #267 and #268 when PR 3b merges. | The spec says "when modelman is deleted"; the code path #260 describes is still in wt until PR 4. |
| 23 | Nine guides (00 to 08) carry "Verified against: modelman 0.1.0, wt 0.1.0, … on 2026-08-29". | Drop `modelman 0.1.0, ` from the line and append ` · revised 2026-10 (commands checked against `wt --help` and `llmbench --help`, not re-run live)`. The tail does not name modelman. | The date of the last live verification stays honest, and a guide that no longer mentions modelman has no line the proofs count. |
| 24 | Provenance comments in non-test Go ("the Go port of modelman's `pricing.py`"). | Reword every one so the comment states wt's own rule and names no deleted file, and no test whose name contains "Modelman" (two tests have such names until PR 6: a comment cites their file, `catalog_test.go` or `pricingpage_test.go`, instead). The closing grep for `modelman\|Modelman` in non-test Go then prints nothing, with no allow-list. | A grep with exceptions is a grep nobody trusts. Git history holds the provenance. PR 6 is optional, so no proof of PRs 1 to 5 may depend on its renames. |
| 25 | `test_llmbench_never_imports_modelman` passes trivially once modelman is gone. | Delete it in PR 3c. | An import of a package that does not exist fails every test in the suite by itself. |
| 26 | After `modelman-ci` goes, nothing in Python asserts that `tomli_w` reproduces `registry.written.sample.toml`, and seven fixtures are exercised only by `wt-ci` on macOS. | Accept. | The spec's Risks section accepts the lost Linux run. wt is the only writer; its decode-then-emit fixed point is the property that matters. |
| 27 | Guide 08's table says more than the spec's in four places. | (a) `stop --all` gets its own row: `wt stop --all`, plus `llmbench provider stop mlx_lm_server` for a running pairing. (b) The pairing start and stop rows end "then `wt litellm sync`". (c) The last row adds "download with the provider's own tool (guide 02 Step 5)". (d) One paragraph under the table says a printed `llmbench provider …` command needs `uv run --directory llmbench` in front. | (a) modelman's `stop --all` stopped a pairing too (`stop_all_local_providers()`); `wt stop --all` leaves one running, as `wt stop --help` says. (b) modelman's start synced the routes after the isolate; llmbench does not. (c) TUI `r` was the only download path. (d) llmbench has no console script on `PATH` (`make install` runs `uv sync`). |
| 28 | wt prints `llmbench provider isolate --solo mlx_lm_server …` (`catalog.PairingStartCommand`) and `wt stop --help` prints `llmbench provider stop mlx_lm_server`; neither can be pasted as it is. | Leave both strings; guide 08 says how to run them (Decision 27d). | Changing a user-facing string is not in the spec's slice 2 ("comments and stragglers only"), and the right spelling depends on where the repo is checked out. Worth an issue against wt; not Step 6's. |
| 29 | PR 6 also changes llmbench's conftest to name the scratch registry through `WT_REGISTRY` (Task 14, Step 3). The spec's slice 6 says "Move Go tests". | Keep it in PR 6, as the same move in the other test suite. | Without it llmbench's suite is the last place that redirects the registry through the alias. It goes or stays with PR 6 (question 3). |
| 30 | Guide 08's removal table lists two things the spec's "Left on disk" does not: `.modelman.toml.*.tmp` and `.registry.toml.*.tmp`. | Keep both rows, and say whose they are: dot-prefixed temp files are modelman's (`tempfile.mkstemp(prefix=f".{path.name}.", suffix=".tmp")` in its `_toml_io.py`). wt's own temp file has no leading dot (`registry.toml.<random>.tmp`, from `os.CreateTemp(dir, base+".*.tmp")`) and is not listed. | A crashed modelman write of `modelman.toml` leaves a copy of the key. The row must not tell a reader to delete a file a running wt write is using. |
| 31 | Between PR 1 and PR 3b `main` is briefly ahead of itself: guide 08 says `modelman/` is gone while it still exists, and four frozen files under `modelman/` point at two skills PR 1 moved or deleted. | Accept the window; merge PR 3b as soon after PR 1 as the owner allows. | Writing "will be deleted" in PR 1 and correcting it in PR 3b is two edits of the same sentence for a window of days, and modelman's own files are frozen. |
| 32 | `llmbench provider list` tags each row `[supported]` or `[retired-only]`. With llamacpp gone every row is `[supported]`. | Drop the tag in PR 3c: a row is `<id>`, a tab, then `occupancy=…` (when the id shares another id's server), `health=…`, `default_model=…`, `env_var=…`. | A tag with one value says nothing. The only thing that matched the old text was a modelman test. It is a change to printed output, so Task 9 names it under "What changes for a user". |
| 33 | After Task 12, `state.py`'s `_pointers` has a branch nothing reaches (`if not isinstance(table, dict)`: its argument is always the dict `_read_toml` returns). | Delete the branch and type the parameter `dict[str, Any]`. | Observed: with it `state.py` is at 97% coverage, without it 100%. |
| 34 | Is `modelman-ci` a required status check on `main`? It could not be read (no network). | The controller checks before PR 3b is opened and removes it from the required checks if it is listed, with the owner's OK. | A required check that no workflow reports leaves every later PR waiting forever. |

## Questions for the Owner

Answer these before execution starts. Each has the recommendation the tasks assume. Everything else this plan decides is in the table above and needs no answer.

1. **PR #165 (`cc-session-transcripts`)** touches root `CLAUDE.md`, `Makefile`, `litellm-session-logs/CLAUDE.md` and `litellm-session-logs/session-log-sources.md`, four files PRs 1 and 3b edit. The spec says to merge or close it before Step 6. Which? Recommendation: merge it first if it is ready, otherwise close it and reopen after PR 3b; either way before PR 1. (It was still open, last updated 2026-09-26, when the inventory was taken.) **Owner's answer, 2026-10-09: leave #165 open and untouched; it will be cleaned up later. Step 6 does not wait for it.** PRs 1 and 3b edit those four files as planned, and whoever brings #165 up to date later resolves the conflicts there.
2. **Is `~/Library/LaunchAgents/local.llamacpp.server.plist` installed on any machine you benchmark on?** If yes, PR 3c makes `llmbench provider stop-all` leave it loaded (Decision 19); unload it by hand first. Recommendation: check with `ls`, and go ahead either way. **Owner's answer, 2026-10-09: it is not installed now. Go ahead with PR 3c.**
3. **PR 6 (the test-file sweep)** is optional in the spec. Do it, or stop after PR 5? Recommendation: do it. It is what removes the last descriptions of modelman as a live tool from the Go tests, and no proof of PRs 1 to 5 depends on it. **Owner's answer, 2026-10-09: do PR 6.**

Two choices are made here and only need a veto: llmbench's fallback read of `modelman.toml` is removed in PR 5 (Decision 7; without it guide 08 could not call the file safe to delete), and modelman's own README, `CLAUDE.md`, `issues.md`, ROADMAP and internals docs are deleted with the tree, not moved (Decision 20).

## What Step 4's PR 4 Already Does

Branch `docs/wt-cloud-sync-skill`, four commits on top of `544cc1e` when this plan was last checked (`7c24e57`, `38f13f6`, `c1928d6`, `c935eb8`; the last two changed `wt/docs/wt-cloud-sync.md`, the `cloud-sync` skill and one line of root `CLAUDE.md`, and no guide). **It must be merged before PR 1 of this plan starts.** It:

- moves `modelman/.claude/skills/ollama-catalog/SKILL.md` to `wt/.claude/skills/cloud-sync/SKILL.md` and rewrites it. Step 6 does not move it again.
- creates `wt/docs/wt-cloud-sync.md`.
- names `wt cloud-sync` in root `CLAUDE.md` (the Commands line), `README.md` (the `modelman/` table row), guides 02, 04 and 08 (one line), `wt/CLAUDE.md`, `wt/CHANGELOG.md` and `modelman/CLAUDE.md`.

It does not reduce Step 6's work: the count of lines naming modelman in tracked files outside `modelman/`, the superpowers directories, `docs/archive`, Go, `llmbench/src`, `llmbench/tests`, `wt/CHANGELOG.md` and `issues.md` goes from 367 lines in 47 files on `main` to 375 lines in 49 files on that branch (at `c935eb8`). It adds five things Step 6 removes (Task 3, Task 4 and Task 11):

- `## While modelman still exists` at the end of `wt/.claude/skills/cloud-sync/SKILL.md` (line 226 to the end);
- `## Beside modelman` at the end of `wt/docs/wt-cloud-sync.md` (line 402 to the end), and the sentence at lines 15–17 of that file that links to it (`[Beside modelman](#beside-modelman)`);
- the bullet at lines 368–371 of that file ("It never writes `modelman.toml` and takes nothing for the sync from it: … loading wt's config reads that file's legacy `[litellm]` table as a fallback."), true until PR 5;
- the sentence in `wt/CLAUDE.md` (line 261) that the removal digest "approves the other's apply until modelman is deleted";
- the parenthesis at the end of root `CLAUDE.md`'s `wt cloud-sync` line (line 19): "(`modelman refresh-prices` and `modelman ollama-catalog sync` still work, frozen, until modelman is deleted; …)".

**If it merges in another shape,** re-check before Task 1: (a) `git ls-files modelman/.claude/skills` prints `adding-a-provider` and `adding-a-tui-screen` only; (b) `git grep -n 'modelman' wt/docs/wt-cloud-sync.md wt/.claude/skills/cloud-sync/SKILL.md` to find the sections above by their text, not their line numbers; (c) every "find" text quoted in Tasks 1 to 4 for guides 02, 04 and 08, root `CLAUDE.md`, `README.md` and `wt/CLAUDE.md`.

## What Was Measured

In a scratch copy of `544cc1e` on 2026-10-08, under `env -i` with a throwaway `HOME`, `GOPROXY=off` and `UV_OFFLINE=1`. These are the starting counts for the proofs below.

| Proof | Command (from the monorepo root unless said) | Prints now |
|---|---|---|
| G1: lowercase `modelman` in non-test Go | `grep -rn 'modelman\|Modelman' wt --include='*.go' \| grep -v _test.go \| wc -l` | `118` (35 files) |
| G2: the alias names in non-test Go | `grep -rho 'MODELMAN_[A-Z_]*' $(grep -rl MODELMAN_ wt --include='*.go' \| grep -v _test.go) \| sort \| uniq -c` | `MODELMAN_` 1, `MODELMAN_LITELLM_CONFIG` 2, `MODELMAN_LITELLM_DATABASE_URL` 3, `MODELMAN_LITELLM_RESTART_CMD` 2, `MODELMAN_REGISTRY` 9, `MODELMAN_STATE` 1 |
| G3: lowercase `modelman` in Go tests | `grep -rn 'modelman\|Modelman' wt --include='*_test.go' \| wc -l` | `283` (61 files) |
| G4: `MODELMAN_REGISTRY` in Go tests | `grep -rn MODELMAN_REGISTRY wt --include='*_test.go' \| wc -l` | `111` (31 files) |
| P1: lowercase `modelman` in llmbench source | `grep -rn 'modelman\|Modelman' llmbench/src \| wc -l` | `35` |
| P2: `MODELMAN_` names in llmbench source | `grep -rho 'MODELMAN_[A-Z_]*' llmbench/src \| sort \| uniq -c` | `MODELMAN_` 1, `MODELMAN_AGENT_DEBUG` 1, `MODELMAN_BENCHMARK_WORKLOAD` 1, `MODELMAN_LITELLM_RESTART_CMD` 1, `MODELMAN_REGISTRY` 4, `MODELMAN_STATE` 2 |
| P3: `modelman` in llmbench tests | `grep -rn -i modelman llmbench/tests \| wc -l` | `75` |
| P4: `llamacpp` in llmbench | `grep -rn -i llamacpp llmbench/src \| wc -l`; same for `llmbench/tests` | `46`; `93` |
| D1: prose and config naming modelman | the `D1` command below, `\| wc -l` | `367` on `main`; `375` (49 files) on the docs branch |
| D2: prose telling a reader to run it | the `D2` command below, `\| wc -l` | `33` on `main`; `31` on the docs branch (guides 00: 1, 01: 5, 02: 10, 03: 1, 04: 1, 07: 7, 08: 6) |
| Links | `bin/check-links` | `ALL LINKS OK` |
| llmbench suite | from `llmbench/`: `make check && make test` | 684 passed, 93.23% coverage, mypy 59 files (the inventory's run) |

`D1` and `D2`, used by every docs proof (bash; from the monorepo root):

```bash
EX=(-- ':!modelman' ':!docs/superpowers' ':!wt/docs/superpowers' ':!docs/archive' ':!*.go' ':!llmbench/src' ':!llmbench/tests' ':!wt/CHANGELOG.md' ':!issues.md')
git grep -n 'modelman\|Modelman' "${EX[@]}"                       # D1
git grep -n 'uv run.*modelman\|--directory modelman' "${EX[@]}"   # D2
```

The exclusions: `modelman/` itself; the two superpowers directories and `docs/archive` (dated plans and specs); Go and llmbench code (their own proofs); `wt/CHANGELOG.md` and `issues.md` (history: old entries are not edited).

What the scratch runs showed, task by task:

- **Task 6.** The 13-case `_msg` table and the ten `http_models_ids` tests pass against today's llmbench (29 tests in the two files), `ruff check` and `ruff format --check` clean.
- **Task 8.** Removing the pre-XDG fallback fails exactly `test_read_path_falls_back_to_the_pre_xdg_registry` and `test_read_path_refuses_a_dangling_legacy_symlink`. The new test, run against today's source, fails with `Failed: DID NOT RAISE RegistryError`.
- **Task 9.** Deleting the backend, its test and its two registry lines fails eight tests: seven in `tests/providers/lifecycle/test_orchestrate.py` and `test_provider_list_names_every_backend`. Removing `solo=` from `isolate_provider` fails two more (`test_isolate_provider_success`, `test_isolate_provider_forwards_extra_args`, which assert `solo=False` in the call). Deleting `test_llmbench_never_imports_modelman` and only its `ast` import fails `make check` with ruff `F401 pathlib.Path imported but unused`; both imports go.
- **The llmbench suite, slice by slice** (the plan's own steps applied in order, `make check && make test` after each): after Task 6, **707 passed**, 93.46%; after Task 8, **706 passed**, 93.45%; after Task 9, **683 passed**, 93.36%, mypy 58 files; after Task 12, **676 passed**, 93.34% with `state.py` at 100% (93.32% and `state.py` at 97% before the unreachable branch of Decision 33 is removed).
- **Task 5.** `TestNoHelpTextNamesModelman` as written below walks 38 commands, fails only on `wt warm` before the string fix and passes after it; `gofmt -l cmd/wt` prints nothing with the `cobra` import placed last in the second group.
- **Guide 07's Verification.** Against an empty scratch home and no database: `wt stats --window 1d` exits 0 and prints `no survey data` and `no usage data`; `wt stats --json` has the keys `['as_of', 'survey', 'usage', 'window']`.
- **Task 10.** After moving `sessionCounts`, deleting the two files and the one test, moving `Evictions` into its test file and adding `TestStartHasNoJSONOrPlanFlag`: `go build ./...` and `go vet ./cmd/wt ./internal/lifecycle` clean, `go test ./cmd/wt ./internal/lifecycle` ok.
- **Task 11.** After the edits and the rewritten tests: `go build ./...` and `go vet ./...` clean, `go test ./internal/config ./cmd/wt ./internal/litellm` ok. A first draft of `TestLoadNeverReadsModelmanToml` asserted `config.toml` byte-identical and failed: `Load`'s schema migration rewrites a `config.toml` that has no `agy` agent. The test below asserts what matters instead (no `litellm`, no key in the file).
- **Deleting `modelman/` alone breaks nothing in llmbench** (the inventory's run: `uv sync --locked`, `make check`, `make test`, 684 passed).

## PR Slices

| PR | Spec slice | Branch | Tasks | Needs | Ships |
|---|---|---|---|---|---|
| 1 | 1 | `docs/modelman-retirement-guides` | 1–4 | Step 4 PR 4 merged; question 1 answered | Guides 00–11, README, the `CLAUDE.md` files, `wt/docs`, the skills. No code. |
| 2 | 2 | `refactor/wt-strings-sweep` | 5 | nothing (may run beside PR 1) | Non-test Go comments and the `wt warm` help string. No behaviour change. |
| 3a | 3 | `test/llmbench-takes-over-modelman-pins` | 6 | nothing (may run beside PRs 1 and 2) | Two test files in llmbench. No source change. |
| 3b | 3 | `chore/delete-modelman` | 7–8 | PRs 1, 2, 3a merged; `modelman-ci` not a required check (Decision 34) | `modelman/` gone, `modelman-ci` gone, Makefile, fixture headers, the wording pin, llmbench's path parity. |
| 3c | 3 | `refactor/llmbench-retire-llamacpp` | 9 | PR 3b merged; question 2 answered | The llamacpp backend, `LLM_ISOLATE_LLAMACPP_MODEL`, the dead wrappers, llmbench's comment sweep. |
| 4 | 4 | `refactor/wt-start-drop-json` | 10 | PR 3b merged | `wt start --json` and `--plan` removed. |
| 5 | 5 | `refactor/stop-reading-modelman-toml` | 11–13 | PR 3b merged | wt and llmbench stop reading `modelman.toml`; guide 08's removal section. |
| 6 | 6 | `test/wt-registry-env-sweep` | 14 | PRs 3c, 4 and 5 merged; question 3 answered yes | Test files only. |
| — | Issues | — | 15 | per row | The controller's issue work. |

PRs 3c, 4 and 5 are independent of each other and may be built in parallel after 3b: they change different code (a review build of the three, one after another, was green in scratch; each on 3b alone was not tried), and they share only `wt/CHANGELOG.md`, `wt/CLAUDE.md`, `llmbench/CLAUDE.md` and `llmbench/tests/test_conftest_guards.py`, on different lines. The proofs inside each of those three PRs are written for that PR alone. **The closing proofs of the whole plan (Task 13, Step 5) are run once, on `main`, after 3c, 4 and 5 have all merged.** Why this order: PR 1 changes every pointer into `modelman/` before PR 3b removes what it pointed at; PR 3a moves the two pins before PR 3b deletes their only copy; PR 4 follows 3b because modelman's bridge is the only caller of `wt start --json` and its contract test reads `wt-start-cli.sample.json`; PR 5 follows 3b because modelman's contract test reads `modelman.sample.toml`.

## File Structure

| File | Change | PR | Responsibility |
|---|---|---|---|
| `docs/guides/00`–`08`, `10` | modify | 1 (and 5 for 00, 04, 06, 08) | Commands from the mapping table; guide 08 gets the table, and in PR 5 the removal section |
| `docs/guides/07-usage-and-spend.md` | rewrite | 1 | A `wt stats` guide |
| `README.md`, `CLAUDE.md`, `wt/CLAUDE.md`, `wt/README.md`, `llmbench/CLAUDE.md`, `litellm-session-logs/CLAUDE.md` | modify | 1, 3b, 3c, 4, 5 | Kept true as each PR lands |
| `wt/docs/**` (14 files) | modify | 1, 3b, 4, 5 | Same |
| `docs/reference/provider-artifacts.md`, `glm-5.3-flash-openrouter.md` | modify | 1, 3c | Same |
| `.claude/skills/mlx-lm-quantization/SKILL.md`, `.claude/skills/adding-a-benchmark-backend/SKILL.md`, `wt/.claude/skills/cloud-sync/SKILL.md` | modify | 1 | Skills name wt and llmbench only |
| `wt/.claude/skills/adding-a-provider/SKILL.md` | create (moved from modelman, rewritten) | 1 | Adding a provider to wt |
| `modelman/.claude/skills/adding-a-provider/`, `adding-a-tui-screen/` | delete | 1 | |
| `wt/**/*.go` (non-test, 35 files) | modify comments | 2 | No description of modelman as live |
| `wt/cmd/wt/model_cmds.go` | modify | 2, 3b, 4 | `wt warm` help; the pin comment; `startCmd` |
| `llmbench/tests/test_wt_bridge.py` | modify | 3a | The `_msg` table |
| `llmbench/tests/test_local_process.py` | create | 3a | `http_models_ids` |
| `modelman/**` (170 files) | delete; `modelman/docs/superpowers` (39 files) moved to `docs/superpowers/modelman/` | 3b | |
| `.github/workflows/modelman-ci.yml` | delete | 3b | |
| `Makefile` | modify | 3b | `test-all`, `install` |
| `wt/cmd/wt/modelman_wording_test.go` | delete | 3b | The Step 0 pin |
| `docs/contracts/rotation.sample.state` | delete | 3b | |
| `docs/contracts/{registry.sample.toml,catalog-predicates.sample.toml,discovered-ids.sample.json,litellm-cli.sample.json,omlx-model-dirs.sample.json,wt-start-cli.sample.json,modelman.sample.toml}` | modify headers | 3b | Go-only, or Go and llmbench (the last two live until PRs 4 and 5) |
| `bin/mlx-quantize`, `bin/check-config-dirs-untouched`, `bin/lib/mlx-lm-resolve.sh` | modify comments | 3b | |
| `llmbench/src/llmbench/registry.py`, `llmbench/tests/test_registry.py` | modify | 3b | One place to look for the registry |
| `llmbench/src/llmbench/providers/lifecycle/backends/llamacpp.py`, `llmbench/tests/providers/lifecycle/backends/test_llamacpp.py` | delete | 3c | |
| `llmbench/src/llmbench/providers/lifecycle/{backends/__init__.py,backends/base.py,orchestrate.py,launchd.py,cli.py}`, `benchmark/isolation.py` and their tests | modify | 3c | One set of backends; no dead wrappers; `provider list` without a status tag |
| `wt/cmd/wt/start_json.go`, `start_json_test.go`, `docs/contracts/wt-start-cli.sample.json` | delete | 4 | |
| `wt/cmd/wt/start.go`, `loading_test.go`, `start_flags_test.go` (new), `wt/internal/lifecycle/evictions.go`, `evictions_test.go` | modify / create | 4 | The seam moves; the removal is pinned |
| `wt/internal/config/modelman.go`, `modelman_fixture_test.go`, `docs/contracts/modelman.sample.toml` | delete | 5 | |
| `wt/internal/config/modelman_test.go` | rename to `catalog_membership_test.go`, six tests deleted | 5 | |
| `wt/internal/config/{config.go,registry.go,litellm_state_test.go,lock_test.go}`, `wt/cmd/wt/main_test.go`, `wt/scripts/agents-smoke.sh` | modify | 5 | No read of `modelman.toml` |
| `llmbench/src/llmbench/state.py`, `llmbench/tests/{test_state.py,conftest.py,test_conftest_guards.py}` | modify | 5 | No read of `modelman.toml` |
| `wt/CHANGELOG.md` | modify (new entries only) | 4, 5 | The two breaking changes |
| `wt/**/*_test.go` (about 60 files), `llmbench/tests/{conftest.py,test_conftest_guards.py}` | modify | 6 | Env move; comments; five test names |

---

## PR 1 — docs and skills

Branch: `git switch -c docs/modelman-retirement-guides main`, after Step 4's PR 4 has merged.

There is no test to write first for prose. The checks are `D1`, `D2`, `bin/check-links`, and a read of every command written against `wt --help`, `wt <cmd> --help` and `uv run --directory llmbench llmbench --help`. Line numbers are those of the docs branch at `c935eb8`; each edit is also identified by the text it starts with. Re-take a number that does not match from the merged PR 4, by that text.

**The substitutions, used in every task of this PR** (the spec's mapping table plus Decisions 3, 4 and 27; llmbench is always spelled `uv run --directory llmbench llmbench …` in a command a reader copies):

| modelman | Replacement |
|---|---|
| TUI (`a`, `e`, `d` keys) | `wt config` Models tab, or `wt model add\|edit\|rm` |
| `start` with no argument | `wt model list` |
| `start <id>`, `stop <id>` | `wt start <id>`, `wt stop <id>` |
| `stop --all` | `wt stop --all`, plus `llmbench provider stop mlx_lm_server` for a running `mlx_lm_server` pairing, which wt does not stop |
| `start <mlx_lm_server pairing>` | `llmbench provider isolate --solo mlx_lm_server <target> --draft <draft>`, then `wt litellm sync` |
| `stop <mlx_lm_server pairing>` | `llmbench provider stop mlx_lm_server`, then `wt litellm sync` |
| `sync` | `wt model init` for provider rows; nothing for state (wt probes disk and servers live) |
| `refresh-prices`, `ollama-catalog sync` | `wt cloud-sync` |
| `usage report` | `wt stats` |
| `usage report --days N`, its Markdown output, Reconciliation sections, "Last wt launch" line | dropped; `wt stats --window 1d\|7d\|30d` and `wt stats --json` are what is left |
| `litellm …` | `wt litellm …` |
| `benchmark …`, `provider …` | `llmbench …`, `llmbench provider …` |
| `migrate`, `delete-family`, TUI `r` (download, delete), `l`, `s` | dropped; `wt litellm on\|off`, `wt start`, `wt stop` cover the last two |

**Rules for every file in this PR:**

- Delete every "Run modelman from the `modelman/` directory" gotcha, every "modelman runnable from its repo" prerequisite and every `# from: …/modelman` line with the command under it, replacing the command by its row above (run from anywhere; `wt` needs `make install`).
- Where a sentence lists "wt and modelman" as two tools that do something, say wt alone. Where it says "outside wt and modelman", say "outside wt".
- State once, in guide 08 only, that modelman existed and was retired. Other guides do not explain history; they name the command.
- The header line of guides 00 to 08: apply Decision 23.
- Leave for a later PR, untouched, exactly the lines each task lists under "Left for".

### Task 1: Guides 00 to 04

**Files:**
- Modify: `docs/guides/00-config-map.md`, `01-initial-setup.md`, `02-providers-and-models.md`, `03-model-families.md`, `04-litellm-config.md`

**Interfaces:**
- Consumes: the substitution table above.
- Produces: guide 02's steps renumbered (old Step 7 "Routing" becomes Step 6). Tasks 2 and 3 and every other guide cite it as "guide 02 Step 6" from now on.

Every line of these five files that names modelman is listed below with what happens to it. A line that is not listed and still names modelman after the step is a line `main` gained since `c935eb8`: apply the rules above to it.

- [ ] **Step 1: Guide 00 (`00-config-map.md`)**

1. Line 5: Decision 23.
2. TL;DR table. Row `registry.toml` (line 15): the writer is wt alone; delete "`modelman` (non-interactive commands only — its TUI is disabled), " and, in the consumers cell, "`modelman`, ". Rows `settings.yaml`, legacy `config.yaml`, `families/*.yaml` (18–20): delete the three rows. Row `modelman.toml` (16): **left for PR 5.** Row `~/.config/litellm/config.yaml` (21): delete "; `modelman` asks wt to sync". Rows `usage.jsonl` and `rotation.state` (25–26): the consumer is `wt stats` for `usage.jsonl`, and `wt` alone for `rotation.state`. Row `homebrew.mxcl.omlx.plist` (28): "wt/modelman lifecycle backends" becomes "wt's and llmbench's lifecycle backends".
3. `### registry.toml`. **Owner** (38): the bullet becomes "**Owner:** `wt`." followed by the text the line already has from "`wt model add`, `edit` and `rm`, and the Models tab" up to "(a provider row wt has no default for, a `local_path` entry, `[[families]]`).", then "wt takes a lock (`registry.toml.lock`, beside the file) and re-checks the file before replacing it." Delete the opening sentence about two writers, the sentence listing modelman's commands, and the closing clause "modelman refuses to save over a file another program changed … run the command again". **Purpose** (40): "(entries modelman registered from an on-disk artifact — distinct from" becomes "(entries registered from an on-disk artifact, on older rows — distinct from". **Env override** (41): "wt and llmbench both honor both."
4. Sections `### modelman.toml` (58–96), `### settings.yaml` (97–107), `### config.yaml (legacy)` (108–125), `### families/*.yaml (legacy)` (126–148): **left for PR 5**, which replaces all four with one section. The lines in them that name modelman are 58, 60, 61, 62, 65, 70, 71, 99, 100, 110, 111, 128, 129. Change nothing in them now (their `grep` commands run on the reader's own file, which still exists).
5. `### ~/.config/litellm/config.yaml`. **Owner** (151): delete the clause that says `modelman` asks for a sync after its own state changes. Lines 155–156: unchanged (they name the aliases, in capitals).
6. `### ~/.config/agent-wt/config.toml`. Line 185: delete " (`modelman litellm ...` passes through to the same commands)"; the next sentence ("On the first wt load where this file already exists, wt copies modelman.toml's legacy `[litellm]` table in once; afterwards wt's copy wins and modelman.toml's is ignored.") is **left for PR 5**. Line 186: "wrote them as an exchange format for `modelman migrate`" becomes "wrote them for a migration tool that no longer exists"; keep "treat those blocks as inert". Line 188 (`MODELMAN_WT_CONFIG`): delete the bullet.
7. Lines 235 and 248: `modelman usage` becomes `wt stats` at 235 ("**Consumers:** `wt stats` (launch counts per model)"); at 248 the consumer is "`wt` (rotation cursor)" alone.
8. `## Verification` (281 and 286, the `ls` line and its sample output): **left for PR 5** (they list `modelman.toml`).
9. `## Gotchas`. Line 312: the bullet becomes "**Models change through `wt model`.** A model is added, edited or removed with `wt model add|edit|rm` or on the Models tab of `wt config` (`wt model`), and the routes follow — [02-providers-and-models](02-providers-and-models.md) Step 1. Downloads are the provider's own tool (guide 02 Step 5), and routes change through `wt litellm ...`." Line 315: "are read only by `modelman migrate` / wt's first-run migration" becomes "are read by nothing but wt's first-run `models.conf` migration". Line 317 ("Run modelman from…"): delete.
10. `## Going deeper`, line 324 (`modelman/README.md`): delete the bullet.

- [ ] **Step 2: Guide 01 (`01-initial-setup.md`)**

1. Line 5: Decision 23 (the `· **rebuild-verified …` tail stays, before the new tail).
2. TL;DR block, lines 65–73. Line 65 becomes `# 4. registry + start a model (which routes it through LiteLLM)`; line 66 "modelman and wt both need" becomes "wt needs"; delete lines 70 and 72; line 73 becomes `wt start ollama/qwen3.8:27b-mlx` followed by the comment the line already has, from "# example id" on.
3. Line 233: "since the wt/modelman lifecycle engines" becomes "since wt's lifecycle engine"; in the list "`wt start`, `modelman start`, `llmbench provider isolate omlx`" delete "`modelman start`, ".
4. Line 308: "the modelman registry references" becomes "`registry.toml` references".
5. Gotcha at 540–546 ("Run modelman from…", with its code block: lines 540, 543, 544, 545, 546 name it): delete the bullet and the block.

- [ ] **Step 3: Guide 02 (`02-providers-and-models.md`), header and Prerequisites (lines 1–57)**

1. Title (line 1): `# Providers and models — wt model, registry.toml, and LiteLLM routing`. Line 3: delete the sentence "modelman's TUI, which used to do both, is disabled." Line 5: Decision 23.
2. Prerequisites, lines 11–18 (the modelman bullet at 11, its code block with line 14, and the sentence at 18): delete.
3. The file table (lines 25–35). Line 25 becomes "wt reads one file under `~/.config/local-ai/`:" and the table keeps the `registry.toml` row only (`WT_REGISTRY`, alias `MODELMAN_REGISTRY`). Delete the `modelman.toml` row (30) and the `settings.yaml` row.
4. TL;DR, lines 53–54 (`# from: …/modelman`, `uv run modelman sync`): delete both lines. Line 50's reference "Step 7" becomes "Step 6".
5. Line 57 ("Who does what now"): "(verified via `uv run modelman --help` and `wt --help`)" becomes "(verified via `wt --help`)"; delete the sentence "**modelman subcommands** = `sync`, `start`/`stop`, `migrate`, `usage`, and `refresh-prices` and `ollama-catalog sync` (both replaced by `wt cloud-sync`) — all still work; bare `modelman` (the TUI) does not."; at the end of the paragraph "(Step 7)" becomes "(Step 6)".

- [ ] **Step 4: Guide 02, Steps 1 to 5 (lines 59–252)**

1. `### 1.` Line 63: replace the first two sentences ("modelman's TUI used to add, edit and delete models. It is disabled: bare `modelman` prints where to go and exits 1.") and the "wt does it now, " that follows by "A model is added, edited and removed with wt, two ways that write the same rows:". Line 81: "pins for both wt and modelman" becomes "pins for wt and llmbench". Line 82: "a `wt model` command, `wt cloud-sync`, or a modelman subcommand that writes it (`migrate`, `ollama-catalog sync`, `refresh-prices`, `sync` when it repairs a provider row)" becomes "a `wt model` command or `wt cloud-sync`"; "Keys neither tool knows are kept." becomes "Keys wt does not know are kept." Line 83 ("Do not edit while a modelman subcommand is running"): delete the bullet. Line 84 ("**What else the TUI did:** …"), which names no tool but is about modelman's TUI: it becomes "**Downloads, starts and stops:** downloads are the provider's own tool (Step 5), starting and stopping is `wt start`/`wt stop` (Step 6), and nothing records whether a model is ready — wt probes the providers for what is on disk."
2. `### 2.` Line 88: "Documented TOML shape, copied from the modelman README:" becomes "The TOML shape:". Line 103: "(`wt start`, which `modelman start` runs for an omlx model, and `wt warm`, which" becomes "(`wt start`, and `wt warm`, which". Line 115: delete "; a modelman subcommand that rewrites the file keeps the line". Line 119: "modelman and wt infer it" becomes "wt infers it"; delete " (`modelman refresh-prices` still does too)". Lines 124–125 (the `# from:` line and the `uv run python -c 'from modelman.registry …'` check): replace the two lines by `grep -A1 '^\[\[providers\]\]' ~/.config/local-ai/registry.toml | grep '^id'`, and the sentence above the block by "List the provider ids in the file:".
3. `### 3.` Line 153: "fills `model_info` from `ollama show <name>` as the TUI's add dialog did:" becomes "fills `model_info` from `ollama show <name>`:" (the TUI meant is modelman's). Line 171: delete the last sentence ("modelman does not check the value: it reads anything that is not exactly `local` as not local."). Line 173: "Rows modelman's TUI wrote also carry `source = "discovered"`, which marks an entry modelman registered from an on-disk artifact" becomes "An older row may also carry `source = "discovered"`, which marks an entry registered from an on-disk artifact". Line 177: "; `uv run modelman start` with no argument still lists it, as registered but not downloaded." becomes "; `wt model list` still shows it, with STATUS `missing`." Line 179: "the shape modelman's TUI derived for a name containing `/`" becomes "the shape older registries hold for a name containing `/`" (the rest of the sentence, through "so an older registry may hold some", stands).
4. `### 4.` Line 195, the comment in the TOML block: `# optional; wt shows the path (wt model list --json) and never deletes it`.
5. `### 5. Downloads`. Line 233: "Neither tool downloads a model now. wt has no download command, and modelman's — a queue in its TUI, applied on exit — went with the TUI; there is no `modelman download` subcommand." becomes "wt does not download models and has no download command." Line 252: delete the sentence that starts "`uv run modelman sync` (Step 6) brings"; "(Step 7)" becomes "(Step 6)".

- [ ] **Step 5: Guide 02, the deleted Step 6 and the renumbering (lines 254–315)**

1. `### 6. Reconcile: sync` (lines 254–271; lines 257, 258, 267, 268, 269, 271 name modelman): **delete the whole section.** Two of its facts move, as one sentence each, into Step 1's list of things to know: "Cloud-hosted rows (`location = "cloud"`, ollama `:cloud` models included) are never treated as downloads; `wt cloud-sync` pulls and removes the ollama cloud stubs." and "wt stores no per-model state: `wt model list` shows what is on disk (STATUS) and what is running (RUNNING) from a live probe."
2. `### 7.` becomes `### 6. Routing: what is routed, and when`. Line 275 (the HTML comment): "the success line is from the command source (`src/modelman/main.py`)" becomes "the success line is from `wt/cmd/wt/start.go`". Line 283: delete from ", and modelman runs one sync after each subcommand that can change routing" through "every `start`/`stop`" (the sentence then runs "…update the routes themselves. `wt model add`, `edit` and `rm` sync once after…").
3. Line 296: the last sentence, "modelman starts one too, and also still starts the others:", becomes "wt starts the others:". Lines 298–301: the code block becomes one line, `wt start ollama/gpt-oss:20b` (the `# from:` line at 299 and the `uv run modelman start` line at 300 go).
4. Line 311: "`modelman start <name>` also takes an on-disk model that has **no registry entry** — by its native name, or by its discovered id" becomes "`wt start <id>` also takes an on-disk model that has **no registry entry**, by its discovered id" (keep the parenthesis listing the three id shapes). `wt start --help` says "Start a local model (<provider>/<name>, as with -M)": it takes the id, so the "by its native name" spelling goes. Reword the rest of the sentence so that the subject is `wt start` and "the closing sync" is "the start" ("It starts the model without registering it and routes it under that id").
5. Line 313: keep the paragraph with wt as its subject and wt's own message: "`wt start` refuses up front when the provider's daemon is not answering (`<provider> is not answering at <origin> — start the <provider> app/service first`): a route sync after a refused probe would otherwise read it as "nothing is pulled" and prune every ollama route." (The message is `DaemonDownError` in `wt/internal/lifecycle/lifecycle.go`.)
6. Line 315 ("**Taking a model off the proxy**"): replace everything from "; `modelman stop <omlx model>` does what `wt stop <omlx model>` does" through "the sync that follows drops its route once a probe no longer finds it running" by: "; `wt stop --all` stops every running local model and then halts the omlx service. When wt cannot tell which omlx model is loaded — the server wants its API key and the registry names no `auth.secret_ref` (Step 2) — `wt stop <omlx model>` says so and changes nothing; set `auth.secret_ref` (or run `wt stop omlx`); an `mlx_lm_server` pairing has no wt stop hook, and `wt stop --all` leaves it running: `uv run --directory llmbench llmbench provider stop mlx_lm_server` stops it, and the next `wt litellm sync` drops its route". The clauses before (the cloud model, `wt stop <omlx model>`, `wt stop omlx`, mtplx) and after (the pulled ollama model) stand.
7. Renumbering. Guide 02 cites its own old Step 7 at lines 50, 57, 76, 84, 149, 252 and 374: each "(Step 7)" becomes "(Step 6)" (50, 57, 84 and 252 are also edited above). Its one citation of old Step 6 (`sync`), at line 252, goes with its sentence. No other guide, `CLAUDE.md`, `wt/docs` page or skill cites guide 02's Step 6 or 7 by number at `c935eb8`. Afterwards `grep -n 'Step 7' docs/guides/02-providers-and-models.md` must print nothing.

- [ ] **Step 6: Guide 02, Verification, Gotchas and Going deeper (lines 319–383)**

1. `## Verification`. Lines 337–351 ("There is deliberately **no second check in `modelman.toml`**" at 337, the `grep -A4` block with line 340, and the paragraph at 351): replace by one paragraph: "There is no second place to check. Routing is derived: `wt litellm list` is the only answer to 'is it routed?', and `wt model list` shows whether a local model is on disk and running. A local model with no line in `wt litellm list` is stopped, or was started outside wt and no sync has run since (`wt litellm sync`)." Line 353: "(after `wt model add` — `modelman sync` never adds model ids)" becomes "(after `wt model add`)".
2. `## Gotchas`. Line 370: delete everything from "`modelman.toml` is per-machine state" to the end of the bullet, and put in its place "wt stores no per-model state and no routing flag: `wt model list` and `wt litellm list` read both live." Lines 371 ("Run modelman from…") and 372 ("**`sync` semantics:**…", the bullet after it, which describes the deleted command without naming modelman): delete both.
3. `## Going deeper`. Line 380 (modelman README): delete. Lines 381, 382 and 383 each hold a path into `modelman/docs/superpowers/specs/` (382 inside its parenthesis): **left for PR 3b**, which rewrites the three paths when the directory moves. Change nothing else in them.

- [ ] **Step 7: Guide 03 (`03-model-families.md`)**

1. Line 5: Decision 23. Prerequisites, lines 18–21 (modelman runnable, with the `# from:` line at 21 and its command): delete.
2. TL;DR row at line 32: the consumer of a family display name is "nothing: wt never reads it"; keep "Hand-edit the `[[families]]` entry (Step 2)". Line 47: "on every row modelman's TUI wrote" becomes "on many older rows".
3. Step 1, line 56 (`grep … modelman.toml`): delete the command and the sentence introducing it (the legacy `[families]` table in `modelman.toml`).
4. `### 2. Display names in registry.toml [[families]]` (lines 71–84; 73 and 81 name modelman): shrink to one paragraph: "`registry.toml` may hold `[[families]]` entries with a `display_name`. Nothing reads them: wt shows and rotates families by their id. wt preserves the entries on every write. Set or rename one by hand (guide 02 Step 1, *A few things stay a hand edit*) if you keep them for your own reference." Keep the heading, so Steps 3 to 5 keep their numbers.
5. Line 95: delete the sentence "A modelman subcommand that rewrites the file keeps them."
6. Gotchas. Line 205 becomes "**Display names are read by nothing.** They never change what `wt` shows or rotates." Line 206: delete the last sentence ("modelman's TUI is disabled, and its subcommands keep what is there when they rewrite the file."). Line 216 (modelman README): delete the bullet.

- [ ] **Step 8: Guide 04 (`04-litellm-config.md`)**

1. Line 5: Decision 23. Line 10: delete from "modelman is runnable from its repo" through "and requires `wt` on PATH." (the sentence that follows, "Registry context …", stays).
2. Line 31: delete ", and modelman runs it after its own changes (`start`/`stop`, `sync`, …)" (a semicolon then joins "update the routes themselves" to "run `wt litellm sync` yourself …"). Line 55: the parenthesis becomes "(`wt stop <omlx model>` unroutes just that model; mtplx and mlx_lm_server unroute the whole provider; `wt stop omlx` and `wt stop --all` halt the omlx service and unroute every omlx model)". Line 61: delete "; modelman only reads it (for `modelman usage`)" and end the sentence after the closing parenthesis.
3. Line 186 (the "**modelman** runs one `wt litellm sync`…" bullet): delete. Line 189: "outside wt and modelman" becomes "outside wt".
4. Line 191 ("**Upgrading from a pre-#179 wt.**"): "as soon as any `modelman` command changes state (`start`/`stop`, `sync`, `refresh-prices`, …)" becomes "on the first `wt start`, `wt stop` or launch through LiteLLM"; at the end of the same line, "before any modelman command:" becomes "before the first `wt start` or `wt stop`:". Line 202: "; `modelman start` with no argument still lists it, as not downloaded." becomes "; `wt model list` still shows it, with STATUS `missing`." Line 203: "the first modelman command that changes state triggers that sync" becomes "the first `wt start`, `wt stop` or launch through LiteLLM triggers that sync". Line 204: the bullet's subject becomes "**`wt start <artifact>` does not register the model or ask for a family.**"; the rest of the bullet stands.
5. Line 215: "`modelman.toml` is untouched by all of this — no field there records routing." becomes "No file records routing." Line 227: delete "modelman's own changes and ". Line 360: "so that family's routes move only when `modelman` starts or stops a pairing and its sync runs" becomes "so that family's routes move only when a `wt litellm sync` runs after llmbench has started or stopped the pairing (`uv run --directory llmbench llmbench provider isolate --solo mlx_lm_server …` or `… provider stop mlx_lm_server`), or when wt launches it". Line 370: "so there is nothing in `modelman.toml` to display" becomes "so there is nothing stored to display". Line 374: "after a `modelman start`/`stop`" becomes "after a `wt start`/`wt stop`".
6. Line 362 ("**Routing state ownership.**"): delete the sentence "`modelman litellm status|on|off|set` pass through to these commands." The clause ", copied once from modelman.toml's legacy `[litellm]` on first load" is **left for PR 5**.
7. Line 388 (`modelman/docs/superpowers/specs/2026-08-28-modelman-litellm-exposure-design.md`): **left for PR 3b.**

- [ ] **Step 9: Check and commit**

From the monorepo root:

```bash
bin/check-links
git grep -n 'uv run.*modelman\|--directory modelman' -- docs/guides/00-config-map.md docs/guides/01-initial-setup.md docs/guides/02-providers-and-models.md docs/guides/03-model-families.md docs/guides/04-litellm-config.md
git grep -c 'modelman\|Modelman' -- docs/guides/00-config-map.md docs/guides/01-initial-setup.md docs/guides/02-providers-and-models.md docs/guides/03-model-families.md docs/guides/04-litellm-config.md
```

Expected: `ALL LINKS OK`. The second command prints nothing (it prints 18 lines before this task). The third prints three lines and no line for guides 01 and 03:

```
docs/guides/00-config-map.md:17
docs/guides/02-providers-and-models.md:3
docs/guides/04-litellm-config.md:2
```

Guide 00's 17 are the lines left for PR 5 (16; 58, 60, 61, 62, 65, 70, 71, 99, 100, 110, 111, 128, 129; 185; 281, 286, by their numbers before this task). Guide 02's three are the spec paths of Going deeper. Guide 04's two are line 362's clause and line 388's spec path. A higher count means an edit above was missed: `git grep -n 'modelman\|Modelman' -- <file>` shows which line.

```bash
git add docs/guides/00-config-map.md docs/guides/01-initial-setup.md docs/guides/02-providers-and-models.md docs/guides/03-model-families.md docs/guides/04-litellm-config.md
git commit -m "docs: guides 00-04 name wt for every job modelman did"
```

### Task 2: Guides 05 to 11

**Files:**
- Modify: `docs/guides/05-benchmarks.md`, `06-wt-agents-and-models.md`, `08-maintenance-and-troubleshooting.md`, `10-mlx-lm-quantization.md`
- Rewrite: `docs/guides/07-usage-and-spend.md`
- Unchanged: `docs/guides/09-agent-benchmarks.md`, `11-capability-eval-benchmark.md` (no mention of modelman; guide 09's `llamacpp` lines are a dated capture and stay)

**Interfaces:**
- Consumes: the substitution table; guide 02's new step numbers (Task 1).
- Produces: guide 08's section `### 6. Where modelman's commands went`, anchor `#6-where-modelmans-commands-went`. PR 5 (Task 13) adds `### 7. Files modelman left behind` after it.

- [ ] **Step 1: Guide 05**

Line 5: Decision 23. Line 100: unchanged (Decision 8; the alias is in capitals). Line 138: unchanged in this PR. Its clause "unless modelman recorded a run earlier: while `latest.toml` is absent, the pointers are read from the `[benchmarks]` table of `~/.config/local-ai/modelman.toml`, and the next recorded run copies them into `latest.toml`" is true until Task 12 and is **left for PR 5**. Line 209 (`modelman/docs/superpowers/specs/2026-09-05-modelman-benchmark-design.md`): **left for PR 3b.**

- [ ] **Step 2: Guide 06**

Line 5: Decision 23. Line 16 (the 2026-08-29 caveat quoting a commit subject): unchanged, it is dated history. Line 68: delete the parenthesis " (modelman's TUI still lists it as downloadable)". Line 96: delete " — modelman's TUI is disabled". Line 107: delete the sentence "`modelman litellm ...` passes through to the same commands:" and end the previous sentence ("…on first load)") with a colon so the code block still follows. Line 106's "copied once from modelman.toml's legacy table on first" is **left for PR 5.** Line 135: "`wt start` (or the sync after `modelman start`)" becomes "`wt start`". Line 279: delete "; modelman's TUI is disabled" inside the parenthesis.

- [ ] **Step 3: Guide 07, rewritten as a `wt stats` guide**

Replace the whole of `docs/guides/07-usage-and-spend.md` with the text below. Every flag in it was read off `wt stats --help` at `544cc1e`, and the two Verification outputs were observed there against an empty scratch home (`no usage data`, and the four JSON keys). The one line **left for PR 3b** is the design-spec path in Going deeper.

````markdown
# Usage and spend — launches and LiteLLM spend per model with `wt stats`

> Use this to: see which models wt launched, and what LiteLLM logged for them in requests, tokens and dollars, over one window.
>
> Verified against: wt 0.1.0, LiteLLM 1.98.0, Ollama 0.33.2 on 2026-08-29 · revised 2026-10 (commands checked against `wt --help` and `llmbench --help`, not re-run live)

## Prerequisites

- **LiteLLM proxy running with Postgres spend logging** — the stack from [01-initial-setup](01-initial-setup.md) / [04-litellm-config](04-litellm-config.md). Spend rows land in the Postgres `LiteLLM_SpendLogs` table; without it the REQUESTS, PROMPT, COMPLETION and SPEND columns have nothing to read (launch counts still work).
- **wt launch history exists**: `~/.config/agent-wt/usage.jsonl` — wt appends one JSON line per TUI launch (guide 06 §7). Example line (captured 2026-08-29 — your model ids will differ):
  ```json
  {"model_id":"ollama/gemma4:9b","timestamp":"2026-08-22T15:00:03.102105Z"}
  ```
- `wt` on PATH (`make install` from the repo root) and `psql` on PATH.
- Everything in this guide is **read-only**: it reads `usage.jsonl` and the Postgres spend table; it mutates nothing.

## TL;DR

```bash
wt stats --window 7d
```

Two tables for one window: the survey table, then the usage table (launches and LiteLLM spend per model). For a copy to keep, `wt stats --json` prints the same report as one JSON line; append it to a file to build a history:

```bash
wt stats --json >> ~/notes/wt-stats.jsonl
```

## Steps

### 1. Run it

```bash
wt stats --window 7d
```

`--window` is `1d`, `7d` or `30d` (default `30d`); there are no other lengths. Example usage table (illustrative values — your models and numbers will differ; the survey table above it is left out here):

```
MODEL                        LAUNCHES  REQUESTS     PROMPT  COMPLETION    SPEND
ollama/gemma4:9b                    2     1,204  1,234,567         630  $0.0000
openrouter/qwen/qwen3.8-27b         0         5        517       3,135  $0.0085
```

### 2. Read the table

One row per model seen on either side, launch history or LiteLLM spend. Both columns cover the same window: after `as_of - window`, up to and including `as_of`.

- **Launches and requests both above 0**: agent traffic went through the LiteLLM proxy. Normal for a model routed through LiteLLM.
- **Launches with `0` requests**: the launch bypassed the proxy. A native or direct launch (ollama `:11434` or its cloud endpoints, oMLX `:8000`) never reaches LiteLLM and logs nothing to Postgres. When most of your launches are native, most rows look like this; it is not a bug.
- **Requests with `0` launches**: something used the proxy that wt did not launch in the window — `curl`, a script, another client. A session launched just before the window starts shows the same row.

After `wt litellm on`, non-native launches route through LiteLLM, so rows with both numbers become the norm.

### 3. Filters

| Flag | Narrows |
|---|---|
| `--model <id>` | both tables to one model id |
| `--family <family>` | the usage table to one model family |
| `--agent <agent>` | the survey table and the launch counts to one agent; spend is then not shown, and a note on stderr says so |

### 4. Where the data comes from

- `~/.config/agent-wt/usage.jsonl` — one JSON object per wt TUI launch, `model_id` + `timestamp` only (no tokens, no cost, no keys). Sample line quoted in Prerequisites.
- Postgres table `LiteLLM_SpendLogs` — LiteLLM's standard spend log (one row per proxy request: model, tokens, cost). Local Postgres allows passwordless access; the table lives in the `litellm` database, not the default `postgres` one. Probe:

  ```bash
  psql litellm -tAc 'select count(*) from "LiteLLM_SpendLogs"'
  ```

  Expected: a single integer, the number of logged proxy requests. `0` (or a `relation does not exist` error) means spend logging is not wired up. Stack setup: [01-initial-setup](01-initial-setup.md), [04-litellm-config](04-litellm-config.md).

wt finds the database with nothing exported. The connection string is the first of `WT_LITELLM_DATABASE_URL`; `MODELMAN_LITELLM_DATABASE_URL` (the older name, still read); `general_settings.database_url` in LiteLLM's `config.yaml`; and `DATABASE_URL`, when `config.yaml` names none. `DATABASE_URL`, and a `config.yaml` value written `os.environ/NAME`, are looked up in your shell and then in the proxy's LaunchAgent plist, as the proxy itself sees them.

Without `psql`, a reachable database or a configured URL, the launch counts still print, the spend cells show `-`, one note on stderr says why (naming the host and port wt tried, when it tried one), and the exit code is 0. Requests the proxy logged with no model are counted in a note on stderr. With a missing or unreadable `registry.toml` the report still prints; a model's family is then its id's provider prefix.

## What `modelman usage report` had that `wt stats` does not

`wt stats` replaced `modelman usage report` ([08-maintenance-and-troubleshooting](08-maintenance-and-troubleshooting.md), "Where modelman's commands went"). Four things were dropped:

- **An arbitrary `--days N`.** The windows are `1d`, `7d` and `30d`.
- **Markdown output.** `wt stats` prints a plain table, or one JSON document with `--json`.
- **The Reconciliation sections.** Read them off the table (Step 2).
- **The "Last wt launch" line.** `cat ~/.config/agent-wt/rotation.state` shows it: one global slot, the last TUI launch and nothing more (guide 06).

Totals can differ at the edges of a window from a report made with the old command: wt compares the proxy's timestamps as UTC, and leaves out a request logged exactly at the start of the window, as it leaves out a launch at that instant.

## Verification

- `wt stats --window 1d` exits 0. With launches or spend in the window, the usage table's header row starts `MODEL`; with none, it prints `no usage data`.
- The JSON form has four top-level keys:

  ```bash
  wt stats --json | python3 -c 'import json,sys; print(sorted(json.load(sys.stdin)))'
  ```

  Expected: `['as_of', 'survey', 'usage', 'window']`.
- No mutations: a run only reads `usage.jsonl` and Postgres.

## Gotchas

- **Only LiteLLM-routed traffic produces spend.** Native and direct launches (ollama cloud, oMLX `:8000`) never appear in LiteLLM spend: they are rows with launches and `0` requests. Expect many if most of your launches are native.
- **`rotation.state` is the *last* TUI launch, nothing more** — one global slot; `esc`/canceled prompts never touch it. It is not a usage summary (guide 06), and `wt stats` does not print it.
- **Point-in-time snapshot.** Every launch appends to `usage.jsonl` and LiteLLM logs to Postgres asynchronously — rerun tomorrow (or in a minute) and the numbers shift. There is no live/budget dashboard here.

## Going deeper

- Full reference for the command: [wt/docs/wt-stats.md](../../wt/docs/wt-stats.md).
- Design of the report `wt stats` replaced (history): `~/github/ohanaverse/local-ai-setup/modelman/docs/superpowers/specs/2026-08-28-modelman-usage-design.md` — data sources, window rules, and the SQL it ran against `LiteLLM_SpendLogs`.
- Launch/rotation side of the data: [06-wt-agents-and-models](06-wt-agents-and-models.md) (picker, `rotation.state` life cycle, `usage.jsonl` writer).
- LiteLLM wiring and spend logging setup: [01-initial-setup](01-initial-setup.md), [04-litellm-config](04-litellm-config.md).
- Raw request/response text for one specific session (not aggregate spend): `litellm-session-logs/` at the repo root — a standalone pipeline that pulls a session's rows from `LiteLLM_SpendLogs` and rebuilds a readable chat transcript; see `litellm-session-logs/CLAUDE.md`.
- This is a leaf guide — nothing further builds on it in `docs/guides/`.
````

Then confirm nothing else cites the uv `VIRTUAL_ENV` warning this rewrite drops: `git grep -n 'VIRTUAL_ENV' -- docs/guides` must print lines only inside the block of guide 08 that Step 4 deletes (its lines 303–326).

- [ ] **Step 4: Guide 08**

1. Line 5: Decision 23. Line 9: "wt/modelman lifecycle backends" becomes "wt's and llmbench's lifecycle backends". Line 10 (modelman runnable): delete.
2. Section 2, the debug flow. **Step 3** (lines 140–159; 140, 143, 156, 158, 159 name modelman): the question becomes "is the model on disk and running?", the command is `wt model list` (no `grep` on `modelman.toml`, no sample `[model_state]` block), read as: STATUS `ok` means on disk, `missing` means the registry names it and no probe finds it, `new` means on disk with no registry entry; RUNNING `run` or `load` means it has a route. Keep the two bullets (local model: start it; ollama model: routed while pulled) with `wt start <model-id>` in the first, and without "(needs the `wt` binary on PATH)", "started outside wt and modelman" (say "outside wt"), the parenthesis about modelman's TUI queueing downloads, the sentence about `ready` and modelman.toml's `running` flag, and the closing "`uv run modelman start <model-id>` also works, but refuses rather than guess when the daemon isn't answering". **Step 4** (162–175; 162, 165, 166, 175 name modelman): at 162, "(`modelman expose` and `wt litellm expose` were removed in #179)" becomes "(`wt litellm expose` was removed in #179)"; the code block is `wt start <model-id>` and `wt litellm sync` (no `# from:` line); at 175, "started by its native name or its discovered id `<family>/<artifact>` (`modelman start` with no argument lists them under `Discovered`)" becomes "started by its discovered id `<family>/<artifact>` (`wt model list` shows them as `new`)". **Step 9** (232–239; 232, 235, 236, 239 name modelman): the title becomes "**Step 9 — omlx/mtplx/mlx_lm_server route dropped by `wt litellm sync`? Start the model.**"; in the body, "Downloading an omlx/mtplx artifact + `modelman sync` flips `ready = true` in `modelman.toml` but does not route it." becomes "Downloading an omlx/mtplx artifact does not route it."; the code block is `wt start <model-id>` for omlx and mtplx and, for an `mlx_lm_server` pairing, `uv run --directory llmbench llmbench provider isolate --solo mlx_lm_server <target> --draft <draft>` followed by `wt litellm sync`; delete "(Nothing in `modelman.toml` records routing, so there is no flag that could disagree with sync.)". Line 251: delete the sentence "Any modelman command that ends in a sync (`modelman sync`, `start`, `stop`) writes it too."
3. Section 4, Upgrades: delete the **modelman** block (lines 303–326; 303, 308, 315, 316, 317, 322 name it). Line 340: "before any modelman command triggers the first real sync" becomes "before the first `wt start` or `wt stop` triggers the first real sync".
4. Gotchas: delete 477 ("Run modelman from…") and 482 ("Bare `modelman` prints…"). Line 481: "wt/modelman's lifecycle backends" becomes "wt's and llmbench's lifecycle backends".
5. Add, as a new last section of `## Steps`, before `## Verification`:

````markdown
### 6. Where modelman's commands went

modelman, the Python model manager this repo used to ship, was retired into
`wt` and deleted. `uv run --directory modelman modelman` fails because the
directory is gone; that is expected. Each thing it did:

| modelman | Now |
|---|---|
| TUI (`a`, `e`, `d` keys) | `wt config` Models tab, or `wt model add\|edit\|rm` |
| `start` with no argument | `wt model list` |
| `start <id>`, `stop <id>` | `wt start <id>`, `wt stop <id>` |
| `stop --all` | `wt stop --all`, and for a running `mlx_lm_server` pairing, which wt does not stop, `uv run --directory llmbench llmbench provider stop mlx_lm_server` |
| `start <mlx_lm_server pairing>` | `uv run --directory llmbench llmbench provider isolate --solo mlx_lm_server <target> --draft <draft>`, then `wt litellm sync` to route it |
| `stop <mlx_lm_server pairing>` | `uv run --directory llmbench llmbench provider stop mlx_lm_server`, then `wt litellm sync` to drop its route |
| `sync` | `wt model init` for provider rows; nothing for state — wt probes disk and servers each time |
| `refresh-prices`, `ollama-catalog sync` | `wt cloud-sync` |
| `usage report` | `wt stats` |
| `usage report --days N`, the Markdown report, its Reconciliation sections and "Last wt launch" line | dropped — `wt stats --window 1d\|7d\|30d`, `wt stats --json` ([07-usage-and-spend](07-usage-and-spend.md)) |
| `litellm …` | `wt litellm …` |
| `benchmark …`, `provider …` | `uv run --directory llmbench llmbench …`, `… llmbench provider …` |
| `migrate`, `delete-family`, TUI `r` (download, delete), `l`, `s` | dropped — download with the provider's own tool (guide 02 Step 5); `wt litellm on\|off`, `wt start` and `wt stop` cover `l` and `s` |

llmbench is not installed on `PATH`. Where wt prints a command that starts
with `llmbench provider …` (the start hint of a stopped pairing, the text of
`wt stop --help`), run it from the repo root with
`uv run --directory llmbench` in front, as in the table.

`MODELMAN_REGISTRY`, `MODELMAN_LITELLM_CONFIG`, `MODELMAN_LITELLM_RESTART_CMD`
and `MODELMAN_LITELLM_DATABASE_URL` still work: wt reads each after its `WT_`
name. `MODELMAN_BENCHMARK_WORKLOAD` and `MODELMAN_AGENT_DEBUG` still work in
llmbench, after `LLMBENCH_WORKLOAD` and `LLMBENCH_AGENT_DEBUG`. No other
`MODELMAN_*` variable is read by anything.
````

The heading is `### 6.` because the guide's sections under `## Steps` are numbered `### 1.` to `### 5.`. Add one line to the end of the TL;DR's "Reading it fast" list: "- Looking for a `modelman` command? §6 has the `wt` or `llmbench` command for each." The removal of the files modelman left behind is **left for PR 5** (Decision 2): it is not safe to follow while wt still reads `modelman.toml`.

Between this PR and PR 3b the sentence "the directory is gone" is ahead of the tree: `modelman/` still exists and still runs, frozen. That window is accepted (Decision 31); the controller merges PR 3b as soon after PR 1 as the owner allows.

- [ ] **Step 5: Guide 10**

wt starts an omlx model by the name omlx itself lists it under (`omlxLoad(…, t.ModelName)` in `wt/internal/lifecycle/omlx.go`), and offers it only when its live inventory finds that name in omlx's model directory or already served (`scanOmlxModels` in `wt/internal/localmodels/sources.go`; nothing in `internal/lifecycle` or `internal/localmodels` reads `local_path`). So `wt start` cannot start a `local_path` entry whose directory is outside omlx's model directory: it answers `<id> is not on disk — pull or download it first` (`catalog.MissingReason`). Guide 10 and the `mlx-lm-quantization` skill (Task 4, Step 3) say the same thing:

1. Line 7 (the guide has no "Verified against" header, so Decision 23 does not apply): delete the last sentence ("modelman deliberately never runs these tools itself — it's register-only.") and put "wt never runs these tools itself." in its place. In the same bullet, reorder the two ways so the one wt can start comes first: "move the output directory into omlx's model directory and `wt model add omlx <directory name> --family <family>` (Step 2), which is the way `wt start` can load it; or leave it where it is and record it as a `local_path` on the `omlx` provider by hand-editing `registry.toml` — a `local_path` entry is one of the few things wt has no command for, and wt lists it but cannot start it from there".
2. Line 51 (the paragraph that opens Step 2): after "takes the model's presence from a stat of it." add: "wt does not start it from there: `wt start` loads an omlx model by the name omlx lists, so a `local_path` directory outside omlx's model directory answers `<id> is not on disk — pull or download it first`. To run the model through wt, use the other way (move the directory into omlx's model directory and `wt model add` it)."
3. Line 58, the TOML comment: `# basename you'll recognize in wt model list`.
4. Line 65 becomes: "Then `wt litellm sync`, because no tool saw the hand edit. `wt model list` shows the entry with its PATH, and STATUS from a stat of that path. To load it, serve the directory with omlx (its model directory is where omlx looks) and run `wt start <id>` — there is no separate routing step: a local model is routed while it runs. From there it's usable through `wt` and `llmbench` exactly like any other omlx model."
5. Line 86: "is the one modelman's TUI used, so the pairing reads clearly" becomes "reads clearly".
6. Line 100: delete " (or the `modelman start` you'd use for a registered pairing)"; `wt litellm sync` is the command.
7. Line 119: "**wt never deletes a `local_path` artifact.** A directory from `mlx_lm.convert`/`dwq` is yours (possibly hours of GPU time): `wt model rm` removes the registry entry and prints the path. Clean up failed experiments with a manual `rm -rf`."
8. Line 128: "Module map: `wt/CLAUDE.md` (Local-model lifecycle), `llmbench/CLAUDE.md` (Provider lifecycle, Benchmark subsystem)".

- [ ] **Step 6: Check and commit**

```bash
bin/check-links
git grep -n 'uv run.*modelman\|--directory modelman' -- docs/guides
git grep -c 'modelman\|Modelman' -- docs/guides
```

Expected: `ALL LINKS OK`. The second command prints exactly one line, in guide 08's new section (the sentence that says `uv run --directory modelman modelman` fails). The third prints these seven lines, and no line for guides 01, 03, 09, 10 and 11:

```
docs/guides/00-config-map.md:17
docs/guides/02-providers-and-models.md:3
docs/guides/04-litellm-config.md:2
docs/guides/05-benchmarks.md:2
docs/guides/06-wt-agents-and-models.md:2
docs/guides/07-usage-and-spend.md:3
docs/guides/08-maintenance-and-troubleshooting.md:5
```

Guides 00, 02 and 04 are as after Task 1. Guide 05's two are line 138's clause (PR 5) and the spec path (PR 3b). Guide 06's two are the dated caveat at 16 and line 106's clause (PR 5). Guide 07's three are the heading of its "What `modelman usage report` had" section, the sentence under it, and the spec path. Guide 08's five are the new section's heading, the first two lines of its opening paragraph and the table's header row, plus the TL;DR pointer.

```bash
git add -u docs/guides
git commit -m "docs: guides 05-10 on wt and llmbench; guide 07 is a wt stats guide; guide 08 maps modelman's commands"
```

### Task 3: README, the `CLAUDE.md` files, `wt/docs`, the reference docs

**Files:**
- Modify: `CLAUDE.md`, `wt/CLAUDE.md`, `wt/README.md`, `llmbench/CLAUDE.md`, `litellm-session-logs/CLAUDE.md`, `wt/docs/wt-cloud-sync.md`, `wt/docs/wt-start-stop.md`, `wt/docs/wt-smoke.md`, `wt/docs/wt-model.md`, `wt/docs/wt-agents/README.md`, `wt/docs/wt-agents/litellm-troubleshooting.md`, `wt/docs/internals/config-and-registry.md`, `wt/docs/internals/local-models.md`, `wt/docs/internals/launch-flow.md`, `wt/docs/internals/litellm-routes.md`, `wt/docs/configuration.md`, `docs/reference/glm-5.3-flash-openrouter.md`, `docs/reference/provider-artifacts.md`
- Unchanged in this PR: `README.md` (PR 3b), `wt/docs/internals/testing.md` (PR 3b), `wt/docs/wt-config.md` (PR 5), `wt/docs/wt-agents/codex-wt.md` and `litellm-session-logs/session-log-sources.md` (dated history)

**Interfaces:**
- Consumes: the substitution table.
- Produces: the heading `## Registry` in `wt/CLAUDE.md` and in `wt/docs/internals/config-and-registry.md` (anchor `#registry`), replacing `## Registry (shared with modelman until it is retired)`. The link at `wt/CLAUDE.md:186` is changed to match in this task.

- [ ] **Step 1: Root `CLAUDE.md`**

Line numbers are those of `c935eb8`. Change now:

- Line 6 (Docs): delete "`modelman/CLAUDE.md` (Python CLI; its TUI is disabled), " from the package-level list.
- Line 19 (the `wt cloud-sync` command): delete the closing parenthesis, from " (`modelman refresh-prices` and `modelman ollama-catalog sync` still work, frozen," through "for the same plan.)".
- Line 30 (`litellm-session-logs/`): "not wired into modelman/wt" becomes "not wired into wt".
- Line 31 (`llmbench/`): delete ", carved out of modelman" and the closing sentence "Imports nothing from modelman". (llmbench's own `CLAUDE.md` keeps the one history sentence; the root file names no retired tool.)
- Line 33 (`wt/`): "from `modelman.toml` it reads only the `[litellm]` table, as a legacy fallback — no per-model key is read at all, and not `price_refresh_last_run` (the stale-pricing notice takes its date from `registry.toml`'s `pricing_updated_at` stamps) (since #179 Phase B not even `ready`: what is on disk and what is running come from live probes of the providers)" becomes "what is on disk and what is running come from live probes of the providers, never from a stored flag, and the stale-pricing notice takes its date from `registry.toml`'s `pricing_updated_at` stamps (the legacy `[litellm]` table of `modelman.toml` is still read as a fallback)". That parenthesis is **left for PR 5.**
- Line 37 (LiteLLM config): "any ad-hoc `modelman`/`wt` run" becomes "any ad-hoc `wt` run".
- Line 39 (`mlx_lm_server` provider): "modelman never deletes them" becomes "wt never deletes them (`wt model rm` prints the path)".
- Line 42 (Benchmark isolation): "via `wt start`/`modelman start`" becomes "via `wt start`"; delete ", which modelman starts and stops through `wt`"; "(see `modelman/CLAUDE.md`'s "Local-model lifecycle" section)" becomes "(see `wt/CLAUDE.md`, "Local-model lifecycle")".
- Line 43 (Stop mechanisms): delete " — and `modelman stop <omlx model>`, which runs it —"; delete " / `modelman stop --all`" (the list already has `wt stop --all`).
- Line 48 (Guides never embed live model state): the second example command, `grep -c '^running = true' ~/.config/local-ai/modelman.toml`, becomes `wt model list`.
- Line 49 (Routing is derived): "nothing in `modelman.toml` marks a model as routed" becomes "no file marks a model as routed"; the last two sentences ("`modelman` responds to its own state changes by asking `wt` for one sync. modelman dropped its `exposed` flag in #179, and `wt` does not read the legacy key either.") are deleted, and the sentence before them ends with a full stop after "change".

**Left for PR 3b:** lines 23–24 (`make test-all`, `make install`), 29 (the `bin/` bullet's `modelman-ci` and "as do `modelman start`/`stop`"), 32 (the whole `modelman/` bullet), 34–36 (Makefile, contracts readers, workflows), 55–56 (the modelman quick-test block), 64. **Left for PR 3c:** line 15's "llamacpp is retired-only, present in `BACKENDS` but excluded from `SUPPORTED_PROVIDER_IDS`". **Left for PR 5:** the parenthesis written into line 33 above.

- [ ] **Step 2: `wt/README.md`, `litellm-session-logs/CLAUDE.md`**

`wt/README.md`: line 92: "so that a subsequent `modelman migrate` can import them into `registry.toml`" becomes "which nothing reads back (the sections are inert)"; line 193: "joined from modelman's `registry.toml`" becomes "joined from `registry.toml`". `litellm-session-logs/CLAUDE.md`: line 6 "into modelman or wt" becomes "into wt"; line 114 "via `modelman usage report`" becomes "via `wt stats`". Root `README.md` lines 10, 81, 84, 87, 103 all describe the tree and CI: **left for PR 3b**, nothing changes there now.

- [ ] **Step 3: `wt/CLAUDE.md`**

- Line 79: "(`registry.toml` has two writers until modelman is retired: modelman, and wt — the `wt model` commands, the Models tab of `wt config` and `wt cloud-sync`; modelman owns `modelman.toml`; wt owns `~/.config/agent-wt/config.toml`)" becomes "(wt is the only writer of `registry.toml` — the `wt model` commands, the Models tab of `wt config` and `wt cloud-sync`; wt owns `~/.config/agent-wt/config.toml`)".
- Line 132: "what lets wt write `registry.toml` beside modelman without rewriting it" becomes "what lets a wt write leave every line it did not change as it was".
- Line 145: delete " (replaced modelman's Python writer)".
- Line 170: the heading becomes `## Registry`.
- Line 172: delete "; modelman still writes it too" and replace "modelman's `_default_registry_path` and llmbench's `registry_path` use the same order — keep the three in sync; each has a precedence test" by "llmbench's `registry_path` uses the same order — keep the two in sync; each has a precedence test".
- Line 178: delete the last sentence ("modelman's loader crashes on a `fetch` that is not a table.").
- Line 179: "loaded by `internal/config` tests and modelman's `tests/contracts/` — a schema change updates both sides" becomes "loaded by wt's Go tests (and, for the two `registry*.toml` fixtures, llmbench's)".
- Line 186: the link's fragment becomes `#registry`.
- Line 192: "and ignores modelman's `running` flag" becomes "and reads no stored flag".
- Line 201: "Go port of modelman's start/warmup:" becomes "The start/stop engine:"; delete the sentence "Writes nothing to modelman state."
- Line 256: "is the Go port of modelman's `pricing.py` and `ollama_catalog.py`:" becomes "holds".
- Line 261: the bullet becomes "**`RemovalDigest` is a fixed format** (first 12 hex digits of SHA-256 over the sorted removals, then the sorted `rm <tag>` lines; the vectors are pinned in `internal/cloudsync/catalog_test.go`), so a digest printed by one run approves the same plan in the next." It names the test file, not the test: the test's present name contains "Modelman" and is renamed only in PR 6.
- Line 273: delete " (modelman parses them)".
- Line 333: "(keyed warmup; modelman's fallback)" becomes "(keyed warmup; llmbench's omlx backend calls it)".

**Left for PR 3b:** line 99 (`make test-all … + modelman`). **Left for PR 4:** line 332 (`wt start <id> --plan --json`) and the word `Evictions` in line 201's list. **Left for PR 5:** line 168 (the fallback sentence).

- [ ] **Step 4: `wt/docs`**

- `wt-cloud-sync.md`: delete the three-line sentence at lines 15–17 ("It replaces `modelman refresh-prices` and `modelman ollama-catalog sync`. Both still work until modelman is deleted; see [Beside modelman](#beside-modelman).") and the section `## Beside modelman`, from its heading (line 402) to the end of the file (line 412). The bullet at lines 368–371 ("It never writes `modelman.toml` and takes nothing for the sync from it: … (Like every wt command, loading wt's config reads that file's legacy `[litellm]` table as a fallback.)") is true until Task 11 and is **left for PR 5.**
- `wt-start-stop.md`: lines 224–225, under `## wt served <provider>`: "`modelman` asks this when its own keyless probe of a partly loaded omlx pool is refused, since only wt resolves a `secret_ref`." becomes "It is a diagnostic: it answers with the key the registry names, so it works where a keyless probe of a partly loaded omlx pool is refused." Line 238, under `## wt warm`: "`modelman start` asks this when omlx refuses its own keyless warmup" becomes "llmbench's omlx backend asks this when omlx refuses its keyless warmup". Lines 9, 147 and 172–198: **left for PR 4.**
- `wt-smoke.md`: line 222 "Use `wt stop`/`modelman stop`" becomes "Use `wt stop`"; line 225 "touches `registry.toml` or modelman's `modelman.toml`" becomes "touches `registry.toml`".
- `wt-model.md`: line 193: "a row still in modelman's old cost layout" becomes "a row still in the old cost layout"; lines 196–197: "modelman reads such a table by its old keys alone, so a new key beside them would be a price it ignores" becomes "a table that held both layouts would carry two prices for one model".
- `wt-agents/README.md`: lines 53–55 (the three-line sentence "`modelman` no longer routes models itself (#179): … (no wait)."): delete. Line 41: **left for PR 5.** Line 47: unchanged (alias, in capitals).
- `wt-agents/litellm-troubleshooting.md`: lines 341 and 377 give `modelman/src/modelman/litellm.py` as the place of a fix: add " (deleted with modelman; the enforcement is `wt/internal/litellm`)" after each path, and change nothing else in these dated notes (lines 34, 340, 376, 388 stay). `wt-agents/codex-wt.md`: unchanged (lines 32 and 51 name a dated cutover).
- `internals/config-and-registry.md`:
  - Line 41: the heading becomes `## Registry`.
  - Line 43: "`WT_REGISTRY` is the name the three tools share" becomes "`WT_REGISTRY` is the name wt and llmbench share"; "modelman's `_default_registry_path` and llmbench's `registry_path` use the same order, and each of the three has a precedence test — keep them in sync" becomes "llmbench's `registry_path` uses the same order, and each of the two has a precedence test — keep them in sync".
  - Line 45: "since llmbench and modelman each resolve a relative `local_path` against their own working directory" becomes "since llmbench resolves a relative `local_path` against its own working directory".
  - Line 49: "(a registry may hold either from modelman's form or an editor)" becomes "(a registry may hold either from an older tool's form or an editor)".
  - Line 62: delete the sentence that begins "**modelman is stricter on the first row of the table**" (through "(a non-string `repo` or `local_path` it loads without checking).").
  - Line 66: "wt's typed decode plus modelman's required-field and cost rules" becomes "wt's typed decode plus the required-field and cost rules".
  - Lines 71–73: "**New keys go where modelman writes them**" becomes "**New keys go at their schema position**"; ", as every modelman save does" is deleted; "modelman's loader refuses any other" becomes "wt's own writer refuses a file that has any other (#247)".
  - Line 81: "with keys neither tool models at every level below the top (both writers refuse an unknown top-level key)" becomes "with keys wt does not model at every level below the top (wt's writer refuses an unknown top-level key)". The rest of the line names modelman's `tests/contracts/test_registry_written_fixture.py`, which exists until PR 3b: **left for PR 3b.**
  - Line 83: "modelman's `exposed`/`ready` flags no longer gate wt's lists" becomes "no stored flag gates wt's lists".
  - Line 85: delete the parenthesis " (modelman reads anything that is not exactly `local` as not local)".
  - Line 87: "wt does not read modelman's retired `exposed`/`litellm_exposed` keys, nor any other per-model `[model_state]` key (`ready`, legacy `downloaded`, `running`):" becomes "wt stores no per-model state:" keeping the rest.
  - Line 91: "the Go port of modelman's `_ensure_provider_entries` and `sync_agent_providers` plus one trigger modelman lacks" becomes "with one trigger beyond the default-provider rule"; "It never edits a row that exists; modelman's `backfill_provider_defaults` is deliberately not ported." becomes "It never edits a row that exists."
  - Lines 37 and 79: **left for PR 5** and **PR 3b** (37 is the fallback; 79 names modelman's contract tests, which PR 3b deletes, and links `docs/contracts/modelman.sample.toml`, which PR 5 deletes).
- `internals/local-models.md`:
  - Line 14: "pins this scan and modelman's `omlx_model_dirs` to the same names for the same trees" becomes "pins the names this scan gives for a set of trees"; "**Never reads modelman's `running` flag.**" becomes "**Never reads a stored flag.**"
  - Line 16: "modelman's omlx probe calls it when omlx refuses its keyless status request, because only wt resolves a `secret_ref`." becomes "It has no caller in this repo; it is a diagnostic that answers with the key the registry names."
  - Line 18: delete the last sentence ("modelman's `_probe_running` applies the keyless half of the same rule (`_omlx_loaded`).").
  - Line 28: "Go port of modelman's start/warmup:" becomes "The engine:"; "with modelman's pidfile/log paths" becomes "with the pidfile and log paths llmbench's mtplx backend also uses"; delete the sentence "Writes nothing to modelman state."
  - Line 30: "is that warmup alone, omlx only, for modelman's fallback" becomes "is that warmup alone, omlx only, which llmbench's omlx backend calls". (The `wt start --plan` clause in the same line is **left for PR 4.**)
  - Line 61 (the `wt start <id> --json` bullet, deleted whole in PR 4): **left for PR 3b**, which rewords its parenthesis about modelman's contract test, and **PR 4**. Lines 20 and 31: **left for PR 4.**
- `internals/launch-flow.md`, line 12: delete the whole parenthesis after "so the newest stamp is the last refresh", from " (`modelman refresh-prices` stamps the same key the same way until it is deleted," through "`price_refresh_last_run`)"; and delete the trailing ", and modelman's `_is_openrouter_priced` until it is deleted" so the sentence ends "`docs/contracts/catalog-predicates` pins both."
- `internals/litellm-routes.md`, line 7: delete the parenthesis " (modelman parses them — `providers` replaced modelman's own provider table)".
- `configuration.md`, line 116: delete " (modelman's TUI is disabled)".
- `internals/testing.md` line 37: **left for PR 3b.** Line 11: unchanged (alias).

- [ ] **Step 5: `llmbench/CLAUDE.md` and the reference docs**

`llmbench/CLAUDE.md`: line 73: delete from "modelman mounts the same app as `benchmark`; " to the end of the line, and put "Do not add `provider` to `benchmark_app` itself: it is mounted beside it." in its place. Lines 24–27, 51, 63, 78: **left for PR 3b**; 59, 64: **left for PR 3c**; 52 and the "and modelman.toml" of 78: **left for PR 5.** Line 7: unchanged (the one history sentence, and a link to the design spec).

`docs/reference/glm-5.3-flash-openrouter.md`: line 53, the heading `**2. ~/.config/local-ai/modelman.toml** — Model state tracking`, becomes `**2. No state file** — routing is derived`; in the paragraph under it (lines 55–61) the closing sentence "No TOML to copy here — the state file is machine state, not a config to hand-edit." becomes "No TOML to copy here: wt stores no per-model state (`wt model list` shows what is on disk and running)."; the rest of the paragraph stands; line 119: "modelman's TUI, which used to list the model, is disabled. Ask the files and wt instead:" becomes "Ask the files and wt:"; line 277: "(modelman.toml stores no routing state)" becomes "(nothing stores routing state)".

`docs/reference/provider-artifacts.md`: lines 145–146: "(`_DEFAULT_PROVIDER_TEMPLATES` in `modelman/src/modelman/registry.py`)" becomes "(`defaultProviderRow` in `wt/internal/config/registry_seed.go`)". Lines 148–149: "`DEFAULT_PROVIDER_IDS` (`modelman/src/modelman/registry.py` and `llmbench/src/llmbench/registry.py`)" becomes "`defaultProviderIDs` (`wt/internal/config/registry_seed.go`) and `DEFAULT_PROVIDER_IDS` (`llmbench/src/llmbench/registry.py`)". Line 152: delete "; modelman reads it via `wt litellm providers`". Lines 160–167: everything from "is user-produced, not something modelman downloaded." through "no file on disk.)" becomes "is user-produced. wt never deletes weights: `wt model rm <id>`, or `d` on its row in the Models tab of `wt config`, removes the registry entry, prints where the weights are and touches no file on disk."; the last sentence of the bullet ("Cleanup of an abandoned experiment is a manual `rm -rf`.") stands. The llamacpp sections (lines 36, 38, 73, 76, 100 name modelman): **left for PR 3c.**

- [ ] **Step 6: Check and commit**

```bash
bin/check-links
git grep -n 'registry-shared-with-modelman\|beside-modelman' -- '*.md' ':!docs/superpowers' ':!wt/docs/superpowers'
git grep -c 'modelman\|Modelman' -- CLAUDE.md wt/CLAUDE.md llmbench/CLAUDE.md wt/README.md litellm-session-logs/CLAUDE.md
```

Expected: `ALL LINKS OK`; the second prints nothing; the third prints `CLAUDE.md:11`, `wt/CLAUDE.md:2`, `llmbench/CLAUDE.md:9` and nothing for the other two. Root `CLAUDE.md`'s eleven are lines 23, 24, 29, 32, 33, 34, 35, 36, 55, 56, 64 (numbers before this task; all PR 3b's but 33's parenthesis, PR 5's). `wt/CLAUDE.md`'s two are 99 and 168. `llmbench/CLAUDE.md`'s nine are 7, 24, 25, 26, 27, 51, 52, 63, 78.

```bash
git add -u CLAUDE.md wt/CLAUDE.md wt/README.md llmbench/CLAUDE.md litellm-session-logs/CLAUDE.md wt/docs docs/reference
git commit -m "docs: CLAUDE.md files, wt/docs and the reference docs stop describing modelman as a live tool"
```

### Task 4: The skills

**Files:**
- Move and rewrite: `modelman/.claude/skills/adding-a-provider/SKILL.md` → `wt/.claude/skills/adding-a-provider/SKILL.md`
- Delete: `modelman/.claude/skills/adding-a-tui-screen/SKILL.md`
- Modify: `.claude/skills/mlx-lm-quantization/SKILL.md`, `.claude/skills/adding-a-benchmark-backend/SKILL.md`, `wt/.claude/skills/cloud-sync/SKILL.md`

**Interfaces:**
- Consumes: Task 2, Step 5's finding about `local_path` and `wt start`.
- Produces: no code.

- [ ] **Step 1: Move `adding-a-provider` and delete `adding-a-tui-screen`**

From the monorepo root (`git mv` of a tracked file works under the global `.claude/` ignore; a new file there would need `git add -f`):

```bash
mkdir -p wt/.claude/skills/adding-a-provider
git mv modelman/.claude/skills/adding-a-provider/SKILL.md wt/.claude/skills/adding-a-provider/SKILL.md
git rm modelman/.claude/skills/adding-a-tui-screen/SKILL.md
```

Until PR 3b deletes them, four frozen files under `modelman/` still point at these two skills (`modelman/CLAUDE.md`, `modelman/README.md`, `modelman/docs/internals/providers.md`). They are not edited: modelman is frozen and goes in PR 3b (Decision 31).

- [ ] **Step 2: Rewrite it for wt**

Replace the whole of `wt/.claude/skills/adding-a-provider/SKILL.md` with:

```markdown
---
name: adding-a-provider
description: Steps to add a new model provider to wt (LiteLLM policy, default registry row, live inventory probe, start/stop backend). Use when asked to add support for a new local or cloud model provider to wt.
---

## Adding a new provider

A cloud provider needs steps 1 and 2 only. A local provider that wt should
list, start and stop needs all of them.

1. **LiteLLM mapping.** Add the provider to `policies` in
   `internal/litellm/policy.go` (prefix, api-key rule, `Cloud`, `V1Base`).
   That table is the single source of truth for routing: a model of an
   unmapped provider is never routed (`provider "<id>" has no LiteLLM
   mapping`, and the picker refuses the row as "(not in LiteLLM)"). A native
   provider (`auth.type = "native"`) gets no entry: it never routes through
   LiteLLM.
2. **Registry row.** A model names its provider with `provider_id`, and the
   registry needs a `[[providers]]` row with that id. For a provider wt has
   no default for, the row is a hand edit of `registry.toml` (guide 02,
   Step 2). To have `wt model init` and `wt model add` write the row
   themselves, add the id to `defaultProviderIDs` and a row to
   `defaultProviderRow` in `internal/config/registry_seed.go`, and, if the
   provider has a command whose presence on PATH means "installed", an entry
   in `installedProviderCommands`.
3. **Inventory (local only).** `internal/localmodels` decides what is on disk
   and what is running. Map the provider id to a probe family in `familyOf`
   (`inventory.go`) and add the family's source in `sources.go` (what is on
   disk, what the server says it is serving). Without this the provider's
   registry models are rows wt cannot probe: they are listed and never
   startable.
4. **Lifecycle (local only).** Add a backend to `backendsByFamily` in
   `internal/lifecycle/lifecycle.go` and give it a tenancy in `tenancy.go`:
   `Exclusive` (one model per process, a start replaces the occupant),
   `Shared` (models load on request) or `Pool` (models load side by side
   under a memory ceiling). If the server can say what it is serving, add the
   family to `servedFamilies` in `cmd/wt/model_cmds.go` so `wt served`
   answers for it.
5. **Tests and docs.** Each of the tables above has a test that enumerates
   it; run `go test ./...` from `wt/` and extend the ones that fail. Add the
   provider to `docs/internals/local-models.md` and, for the user, to
   `../docs/guides/02-providers-and-models.md`.

For the benchmarks (`llmbench provider isolate <id>`), a local provider also
needs an llmbench backend: see the `adding-a-benchmark-backend` skill at the
repo root.
```

Then confirm every symbol the skill names exists, from `wt/`:

```bash
grep -n 'var policies' internal/litellm/policy.go
grep -n 'var defaultProviderIDs\|var installedProviderCommands\|func defaultProviderRow' internal/config/registry_seed.go
grep -n 'func familyOf' internal/localmodels/inventory.go
grep -n 'var backendsByFamily' internal/lifecycle/lifecycle.go
grep -n 'func TenancyOf' internal/lifecycle/tenancy.go
grep -n 'var servedFamilies' cmd/wt/model_cmds.go
```

Expected: eight lines in all, one from each command but the second, which prints three (at `544cc1e`: `policy.go:28`, `registry_seed.go:73`, `:79`, `:85`, `inventory.go:168`, `lifecycle.go:199`, `tenancy.go:26`, `model_cmds.go:413`). If a name differs, use the name the code has.

- [ ] **Step 3: `mlx-lm-quantization`**

Step 2 of the skill lists the two ways to register; put the one wt can start first and say what the other cannot do: "Register it. Move the output directory into omlx's model directory (`~/.omlx/models/` by default) and run `wt model add omlx <directory name> --family <family>`: that is the way `wt start` can load it. Or leave it where it is and hand-edit `registry.toml` — a `local_path` entry is one of the few things wt has no command for: add an `[[models]]` entry with `provider_id = "omlx"` and a `[models.fetch]` `local_path = "<out-dir>"` (absolute path); see `docs/guides/10-mlx-lm-quantization.md` for the exact snippet. wt lists such an entry (`wt model list` shows its path) and cannot start it from there."

Step 3 of the skill ("`modelman sync` to reconcile the new entry's ready flag, then `modelman start <id>` — there is no separate routing step: …") becomes: "`wt start <id>` — there is no separate routing step: a local model is routed while it runs. After a hand edit run `wt litellm sync` first, because no tool saw the edit; a `wt model add` needs none (the add syncs). `wt start` loads an omlx model by the name omlx lists, so a `local_path` directory outside omlx's model directory answers `<id> is not on disk — pull or download it first`."

The gotcha "modelman never deletes a `local_path` artifact (it didn't create it, possibly hours of GPU time) — cleaning up a failed experiment is a manual `rm -rf`." becomes "wt never deletes a `local_path` artifact (possibly hours of GPU time): `wt model rm` removes the registry entry and prints the path — cleaning up a failed experiment is a manual `rm -rf`."

- [ ] **Step 4: `adding-a-benchmark-backend`**

- Lines 17–19: delete the sentence "`modelman start`/`stop` reach the same code through modelman's path dependency on llmbench, so run modelman's suite too."
- Step 3 (lines 37–45): keep the step; delete only ", until modelman is retired, to its copy in `modelman/src/modelman/local_process.py` (`modelman/tests/test_llmbench_reexports.py` fails if the two differ)" and the "and" before it, so the sentence ends "add it to `ENV_VAR_BY_PROVIDER` in `src/llmbench/local_process.py`."
- Step 4 (lines 53–57): "add its basename to `_FAKE_BINARIES` in `tests/conftest.py` **and** in `modelman/tests/conftest.py`, and to the parametrized list in `tests/providers/lifecycle/test_hermeticity.py` **and** its copy, `modelman/tests/test_hermeticity.py`" becomes "add its basename to `_FAKE_BINARIES` in `tests/conftest.py` and to the parametrized list in `tests/providers/lifecycle/test_hermeticity.py`".
- Step 5 (lines 58–70): "(by hand, or via `modelman sync`)" becomes "(by hand, or with `wt model init` once wt seeds its row)"; replace everything from "and, until modelman is retired, in `modelman/src/modelman/registry.py`" to the end of the step by "and, if `wt model init` should seed the provider's row, add the id to `defaultProviderIDs` and a row to `defaultProviderRow` in `wt/internal/config/registry_seed.go` (see the `adding-a-provider` skill in `wt/.claude/skills/`)."
- Steps 2 and 6 (lines 30–36 and 71–77, the `SUPPORTED_PROVIDER_IDS` wording): **left for PR 3c.**

- [ ] **Step 5: `cloud-sync`**

Delete the section `## While modelman still exists` at the end of `wt/.claude/skills/cloud-sync/SKILL.md` (from its heading, line 226, to the end of the file).

- [ ] **Step 6: The PR's proof**

From the monorepo root (bash):

```bash
EX=(-- ':!modelman' ':!docs/superpowers' ':!wt/docs/superpowers' ':!docs/archive' ':!*.go' ':!llmbench/src' ':!llmbench/tests' ':!wt/CHANGELOG.md' ':!issues.md')
git grep -n 'uv run.*modelman\|--directory modelman' "${EX[@]}"
git grep -c 'modelman\|Modelman' -- .claude wt/.claude
git ls-files modelman/.claude
bin/check-links
make lint
```

Expected: the first command prints exactly one line (guide 08's sentence that the command fails; it printed 31 before this PR). The second prints nothing (no skill names modelman). The third prints nothing. `ALL LINKS OK`. `make lint` exits 0. Then, to see what is left for later PRs and nothing else:

```bash
git grep -l 'modelman\|Modelman' "${EX[@]}"
```

Expected: these 34 files and no others (it printed 49 before this PR): `.github/workflows/modelman-ci.yml`, `CLAUDE.md`, `Makefile`, `README.md`, `bin/check-config-dirs-untouched`, `bin/lib/mlx-lm-resolve.sh`, `bin/mlx-quantize`, the seven `docs/contracts/` files (`catalog-predicates.sample.toml`, `discovered-ids.sample.json`, `litellm-cli.sample.json`, `modelman.sample.toml`, `omlx-model-dirs.sample.json`, `registry.sample.toml`, `wt-start-cli.sample.json`), `docs/guides/00-config-map.md`, `02`, `04`, `05`, `06`, `07`, `08`, `docs/reference/provider-artifacts.md`, `litellm-session-logs/session-log-sources.md`, `llmbench/CLAUDE.md`, `wt/CLAUDE.md`, `wt/docs/internals/config-and-registry.md`, `wt/docs/internals/local-models.md`, `wt/docs/internals/testing.md`, `wt/docs/wt-agents/README.md`, `wt/docs/wt-agents/codex-wt.md`, `wt/docs/wt-agents/litellm-troubleshooting.md`, `wt/docs/wt-cloud-sync.md`, `wt/docs/wt-config.md`, `wt/scripts/agents-smoke.sh`. Each remaining line is one this PR's tasks listed as "left for" a later PR, or dated history. A file outside this list means an edit was missed. The fifteen that left the list: the two root skills and the `cloud-sync` skill, guides 01, 03 and 10, `docs/reference/glm-5.3-flash-openrouter.md`, `litellm-session-logs/CLAUDE.md`, `wt/README.md`, and under `wt/docs/`: `configuration.md`, `internals/launch-flow.md`, `internals/litellm-routes.md`, `wt-model.md`, `wt-smoke.md`, `wt-start-stop.md`.

- [ ] **Step 7: Verify and commit**

From the monorepo root: `make test-all` (exit 0; this PR changes no code).

```bash
git add -u .claude wt/.claude
git status --short .claude wt/.claude modelman/.claude
git commit -m "docs: skills name wt and llmbench; adding-a-provider moves to wt, adding-a-tui-screen is deleted"
```

`git add -u` stages edits of tracked files only, so nothing untracked under a `.claude` directory is swept in; the moved file and the deleted one were staged by `git mv` and `git rm` in Step 1. `modelman/.claude` is not named in the `git add`: it holds no file after Step 1, and when its empty directories are gone too, naming it stops git with `fatal: pathspec 'modelman/.claude' did not match any files` (exit 128) and nothing is staged (observed in a throwaway repo). Before the commit, the `git status --short` above must show only staged lines (a letter in the first column, a blank in the second): `M` for the three edited skills, `A` or `R` for the moved one, `D` for the two paths under `modelman/.claude`.

PR 1 is ready. **Ask the owner before pushing or opening the PR.**

---

## PR 2 — the wt strings sweep

Branch: `git switch -c refactor/wt-strings-sweep main`.

### Task 5: Non-test Go says nothing about modelman as a live tool

**Files:**
- Modify (comments, and one help string): the 35 non-test Go files that `grep -rln 'modelman\|Modelman' wt --include='*.go' | grep -v _test.go` lists, except `internal/config/modelman.go` (PR 5).
- Test: `wt/cmd/wt/model_cmds_test.go` (one test added)

**Interfaces:**
- Consumes: nothing.
- Produces: no identifier changes. `ModelmanPath`, `loadModelmanState`, `modelmanState` and `migratedLitellm` are left for PR 5; the two pin comments for PR 3b; `start_json.go` for PR 4.

- [ ] **Step 1: Write the failing test for the one user-facing string**

`wt warm --help` still says `modelman start` asks for it. Add to `wt/cmd/wt/model_cmds_test.go`:

```go
// TestNoHelpTextNamesModelman pins success criterion 1 of the retirement for
// wt's own help: no command's help may send a reader to modelman, which no
// longer exists. It walks the whole command tree, so a command added later is
// covered too, and reads each command's long text and its usage (the short
// line, the examples and every flag's description). The check is for the
// lowercase tool name: the env alias names (MODELMAN_REGISTRY and the three
// MODELMAN_LITELLM_ ones) are uppercase, are read forever, and may be named.
func TestNoHelpTextNamesModelman(t *testing.T) {
	seen := 0
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		seen++
		for _, text := range []string{c.Long, c.UsageString()} {
			if strings.Contains(text, "modelman") {
				t.Errorf("%s: help names modelman:\n%s", c.CommandPath(), text)
			}
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(rootCmd())
	if seen < 20 {
		t.Fatalf("walked %d commands, want the whole tree (wt has more than 20)", seen)
	}
}
```

Add `"github.com/spf13/cobra"` to the file's import block as the **last** line of the second group, after `"github.com/ohanaverse/local-ai-setup/wt/internal/themes"`: the file does not import it today, and gofmt sorts a group, so any other place fails `gofmt -l`. `rootCmd()` builds the real tree (38 commands at `544cc1e`); other tests of this package call it the same way, under the `TestMain` that isolates the config home.

- [ ] **Step 2: Run it to see it fail**

Run, from `wt/`: `go test ./cmd/wt -run TestNoHelpTextNamesModelman -count=1`
Expected: FAIL with one error, `wt warm: help names modelman:` followed by the Long text. No other command is named.

- [ ] **Step 3: Fix the string**

In `wt/cmd/wt/model_cmds.go`, in `warmCmd`'s `Long`, replace:

```go
			"Nothing is started, stopped or routed: this is the warmup step of `wt start`\n" +
			"by itself, which `modelman start` asks for when omlx refuses its keyless\n" +
			"request. To start a model, use `wt start`.",
```

with:

```go
			"Nothing is started, stopped or routed: this is the warmup step of `wt start`\n" +
			"by itself, which llmbench's omlx backend asks for when omlx refuses its\n" +
			"keyless request. To start a model, use `wt start`.",
```

Run the test again. Expected: PASS.

- [ ] **Step 4: Sweep the comments**

Three rules decide every line. **(R1) Provenance** ("the Go port of modelman's X", "as modelman's Y does", "mirrors modelman's Z"): state the rule as wt's own and drop the name and any Python file or function name. **(R2) Sharing** ("shared with modelman", "wt, llmbench and modelman", "modelman still reads / writes / owns"): if llmbench is a real sharer, name llmbench; otherwise say wt alone. **(R3) A cross-tool reason for a rule that stays** ("so modelman's next save does not move it", "because modelman still reads the file wt writes"): give the reason that survives (a stable layout, byte-identical no-op writes, llmbench reads the file), or none.

File by file (line numbers at `544cc1e`):

| File | Lines | What the comment says afterwards |
|---|---|---|
| `cmd/wt/stats_usage.go` | 38–39 | R1: the family of a model that has left the registry is its id's provider prefix, so it still answers to `--family`; an empty registry is not an error. |
| `cmd/wt/model_write.go` | 127 | R2: "llmbench resolves a relative `local_path`…" (drop modelman). |
| `cmd/wt/litellm.go` | 2–3 | "wt owns this"; delete "modelman shells out to these commands". |
| `cmd/wt/main.go` | 331 | "A missing registry is tolerated…" (drop "modelman"). |
| `internal/tomlw/encode.go` 19; `table.go` 4–5, 82 | | R3: the layout is tomli-w's, kept so a write changes only the lines it means to and the fixture `registry.written.sample.toml` stays byte-stable; "where unlisted keys go" with no author. |
| `internal/cloudsync/doc.go` | 18 | R1: delete the sentence naming `pricing.py`, `ollama_catalog.py`, `time_pricing.py`. |
| `internal/cloudsync/catalog.go` | 83, 175, 476 | 83: "The digest is a fixed format: a printed digest approves the same plan on a later run (the vectors are in `catalog_test.go`)." 175: "in schema order". 476: "the text the command prints for review". |
| `internal/cloudsync/openrouter.go` | 28, 36, 145, 179 | R1 throughout: "the arithmetic is `float(value) * 1_000_000`"; "an unparsable price leaves the model alone with a warning"; "every matched model is stamped"; "the whole entry is refused". |
| `internal/cloudsync/apply.go` | 10, 188 | "the format wt stamps: UTC…"; "changed or not". |
| `internal/cloudsync/pricingpage.go` | 29, 89, 113, 122, 134 | R1: keep each rule (ASCII digits only; the `repr`-style quoting of a cell in a message; the list-of-lists rendering; the collector's quirks) and say it is the pinned reading of the page ("pinned by the cases in `pricingpage_test.go`"), not another tool's. Name the test file, never the test: its name contains "Modelman" until PR 6 (Decision 24). |
| `internal/config/config.go` | 45, 263, 450, 453, 499, 574, 646–650, 661, 689, 1002, 1009, 1161–1163 | 45: "wt owns this state." 263, 1002, 1009: R1 (the `openrouter_priced` rule, stated). 450, 453: "`time_prices` rows are written by `wt cloud-sync`'s catalog flow and applied by nothing." 499: "the string `wt cloud-sync` stamps". 574: delete the comparison. 646–650: "wt decides which local models are running from the live inventory, never from a stored flag." 661: "matching `Path.expanduser()`, which llmbench uses". 689: "legacy provider/model sections survive on disk; nothing reads them" (Decision 12). 1161–1163: "The registry is read by llmbench too, so any other value…". **Leave** 710, 744, 761, 773, 796, 797 (PR 5). |
| `internal/config/migrate.go` | 192 | Decision 12: "the sections it seeds stay on disk; nothing reads them back." |
| `internal/config/registry_seed.go` | 51, 72, 81, 151–152, 263, 266 | R1: "tolerates an absent wt setup"; "the order the rows are added"; "the default row for a provider"; "the default-provider rule plus one trigger: a provider a configured agent lists"; `pyTitle` is "Python's `str.title()`, the casing older rows carry, kept so a row seeded today reads like one seeded before". |
| `internal/config/registry_doc.go` | 34–38, 465, 532 | R3: "in schema order, so a row wt edits keeps a stable layout"; "an empty table is kept"; "unmodelled keys first, sorted". |
| `internal/config/registry.go` | 19, 25–27, 62 | R2: "the name wt and llmbench share"; "llmbench's `registry_path` uses the same precedence, so the two tools agree"; "matching `Path.expanduser()` as llmbench's `registry_path` does". **Leave** 48–57 (PR 5). |
| `internal/config/registry_validate.go` | 17–19, 77, 96, 128, 146, 187, 191, 280 | R3: the rules are wt's registry schema: "a row that would not load in wt's own typed reader, or that breaks the required-field and cost rules below"; drop `_parse_provider`, `_parse_model`, `_parse_cost`; 191: "the legacy `cost.kind` shape, validated and left alone". |
| `internal/config/registry_write.go` | 13, 62 | "another program (an editor) kept…"; "which drops comments". |
| `internal/config/fortest.go` | 18 | unchanged (names the alias only). |
| `internal/tui/model_list.go` | 130–131 | Describe the COST column format; delete the path into `modelman/src`. |
| `internal/lifecycle/env.go` 2; `pidproc.go` 14, 88 | | "the start/stop/warmup lifecycle"; pidproc: R2, "where llmbench also records it"; "the log is shared with llmbench". |
| `internal/lifecycle/omlx.go` | 81–82 | "It exists for llmbench, whose own warmup is keyless and resolves no `secret_ref`." |
| `internal/ollamacheck/ollamacheck.go` | 31 | "not model discovery (`internal/localmodels` does that)". |
| `internal/localmodels/sources.go` 223–224; `match.go` 4 | | "every `--` becomes `/`"; delete the path into `modelman/src`; match.go: "never a stored flag". |
| `internal/modeladmin/fields.go` 141, 173, 176; `ollama.go` 47; `apply.go` 102, 200, 359, 418–425 | | R1: "the rule for a cloud id (`/` becomes `--`), kept so ids and history keep matching"; "the `model_info` keys LiteLLM reads"; 102: `// every row has the key`; 200: "named as `<target>+draft-<draft>`"; 359, 418–425: "the old cost layout (`cost.kind`)" with no reader named. |
| `internal/litellm/service.go` 43, 483, 986–987; `policy.go` 3, 26; `proxyenv.go` 61 | | "never because of a stored flag"; "what `wt litellm sync` prints"; "`wt litellm providers` prints this" (Decision 11); policy.go: delete "It ports modelman's litellm.py" and "(modelman consults it through `wt litellm providers`)"; proxyenv.go: unchanged (alias). |
| `internal/configeditor/models_form.go` | 502 | Drop "in modelman's form". |

`cmd/wt/stats.go:63`, `internal/litellm/configfile.go:40, 52, 66`, `restart.go:36, 38`, `dburl.go:20–21, 50` name the permanent aliases and do not change. `configfile.go:66–67` says config.yaml follows "only `WT_LITELLM_CONFIG`": add "(or its alias)".

- [ ] **Step 5: The proof**

From the monorepo root:

```bash
grep -rn 'modelman\|Modelman' wt --include='*.go' | grep -v _test.go
```

Expected, and nothing else (26 lines; it printed 118 before this task; no line in `internal/cloudsync`, whose comments name test files and no test): 11 lines in `wt/internal/config/modelman.go`; 5 in `wt/internal/config/registry.go` (the `ModelmanPath` block); 6 in `wt/internal/config/config.go` (the two `loadModelmanState()` calls, the notice, three comment lines of the fallback); `wt/cmd/wt/start_json.go:45`; `wt/cmd/wt/model_cmds.go` (the two pin-comment lines); `wt/internal/litellm/configfile.go` (the one pin-comment line). PR 3b removes 3 of these, PR 4 one, PR 5 the other 22.

```bash
grep -rho 'MODELMAN_[A-Z_]*' $(grep -rl MODELMAN_ wt --include='*.go' | grep -v _test.go) | sort | uniq -c
```

Expected: the four alias names with the counts they had (`MODELMAN_LITELLM_CONFIG` 2, `MODELMAN_LITELLM_DATABASE_URL` 3, `MODELMAN_LITELLM_RESTART_CMD` 2, `MODELMAN_REGISTRY` 9, give or take a comment this task reworded), plus `MODELMAN_STATE` 1, which is in the `ModelmanPath` comment PR 5 deletes. No new name.

- [ ] **Step 6: Verify and commit**

From `wt/`: `test -z "$(gofmt -l .)" && go vet ./... && go test -count=1 ./... && make check`. From the root: `make test-all`.

```bash
git add -u wt
git commit -m "refactor(wt): comments and the wt warm help say what wt does, not what modelman did"
```

PR 2 is ready. **Ask the owner before pushing or opening the PR.**

---

## PR 3a — llmbench takes over the two pins modelman carried

Branch: `git switch -c test/llmbench-takes-over-modelman-pins main`.

### Task 6: The `_msg` table and `http_models_ids`

**Files:**
- Modify: `llmbench/tests/test_wt_bridge.py`
- Create: `llmbench/tests/test_local_process.py`

**Interfaces:**
- Consumes: `llmbench.wt_bridge._msg(proc: subprocess.CompletedProcess[str], fallback: str) -> str`; `llmbench.local_process.http_models_ids(url: str, timeout: float = 2.0) -> list[str]`. Neither changes.
- Produces: no source change. After this task, deleting `modelman/tests/test_llmbench_reexports.py` loses no coverage of llmbench.

These are pins of existing behaviour, so they pass on the first run. Step 3 proves each one bites.

- [ ] **Step 1: Add the `_msg` table**

Append to `llmbench/tests/test_wt_bridge.py`:

```python


# One argv for every _msg case, carrying what _msg exists to keep out of a
# traceback. The value is fake.
_FAKE_KEY = "sk-FAKE-not-a-real-key-0000"
_ARGV_WITH_KEY = ["wt", "litellm", "set", "--api-key", _FAKE_KEY]

# (case id, stdout, stderr, the one line _msg must produce)
_MSG_CASES = [
    ("cobra Error: prefix", "", "Error: no such model\n", "no such model"),
    ("wt: prefix", "", "wt: no such model\n", "no such model"),
    ("both prefixes on one line", "", "Error: wt: no such model\n", "no such model"),
    (
        "cobra's line and wt's own line collapse to one",
        "",
        "Error: no such model\nwt: no such model\n",
        "no such model",
    ),
    ("distinct lines join with '; '", "", "Error: first\nwt: second\n", "first; second"),
    ("blank lines and padding are dropped", "", "\n  Error:   padded  \n\n", "padded"),
    ("an unprefixed line passes through", "", "plain failure\n", "plain failure"),
    ("a prefix is stripped only at the start of a line", "", "saw Error: x\n", "saw Error: x"),
    ("stdout is used when stderr is empty", "wt: from stdout\n", "", "from stdout"),
    ("stdout is used when stderr is only whitespace", "wt: from stdout\n", "  \n", "from stdout"),
    ("stderr wins over stdout", "wt: from stdout\n", "wt: from stderr\n", "from stderr"),
    ("no output at all gives the fallback", "", "", "the fallback"),
    ("only whitespace gives the fallback", " \n", "\n\n", "the fallback"),
]


@pytest.mark.parametrize(
    ("stdout", "stderr", "want"),
    [case[1:] for case in _MSG_CASES],
    ids=[case[0] for case in _MSG_CASES],
)
def test_msg_is_one_clean_line_and_never_shows_argv(stdout, stderr, want):
    """#276: `_msg` builds the text of every error the bridge raises from wt's
    output alone. It must strip cobra's `Error: ` and wt's own `wt: ` prefix,
    say a repeated line once, prefer stderr, and never interpolate argv: a
    caller that one day passes `--api-key <value>` would otherwise print the
    key in a traceback or a benchmark report."""
    proc = subprocess.CompletedProcess(
        args=_ARGV_WITH_KEY, returncode=1, stdout=stdout, stderr=stderr
    )
    got = wt_bridge._msg(proc, "the fallback")
    for secret in ("--api-key", _FAKE_KEY):
        assert secret not in got, "_msg put argv in its message; build it from wt's output only"
    assert got == want
```

- [ ] **Step 2: Add the `http_models_ids` tests**

Create `llmbench/tests/test_local_process.py`:

```python
"""`http_models_ids`: the /v1/models probe every lifecycle backend asks
"is it serving, and what?" through. Every other test replaces it."""

import io
import urllib.error

import pytest

from llmbench import local_process


def _serve(monkeypatch, body: bytes, seen: list | None = None):
    def urlopen(url, timeout):
        if seen is not None:
            seen.append((url, timeout))
        return io.BytesIO(body)

    monkeypatch.setattr(local_process.urllib.request, "urlopen", urlopen)


def test_http_models_ids_reads_the_ids_of_a_models_listing(monkeypatch):
    """The ids, in the server's order, and the caller's timeout reaches the
    request: a probe that ignored it would hang a `provider isolate` on a
    server that accepts the connection and never answers."""
    seen: list = []
    _serve(monkeypatch, b'{"object": "list", "data": [{"id": "a"}, {"id": "b/c"}]}', seen)
    assert local_process.http_models_ids("http://127.0.0.1:8000/v1/models", timeout=0.5) == [
        "a",
        "b/c",
    ]
    assert seen == [("http://127.0.0.1:8000/v1/models", 0.5)]


@pytest.mark.parametrize(
    "body",
    [
        b"<html>502 Bad Gateway</html>",
        b"[]",
        b'{"data": "nope"}',
        b'{"error": {"message": "no key"}}',
        b"\xff\xfe",
    ],
    ids=["not-json", "a-list", "data-not-a-list", "an-error-body", "not-utf8"],
)
def test_http_models_ids_reads_an_unusable_body_as_nothing_serving(monkeypatch, body):
    """Anything that is not a models listing is "no models", never an
    exception: the backends call this in a wait loop, and a traceback there
    would replace the lifecycle's own "did not come up" error."""
    _serve(monkeypatch, body)
    assert local_process.http_models_ids("http://127.0.0.1:8000/v1/models") == []


def test_http_models_ids_skips_entries_without_a_string_id(monkeypatch):
    """One malformed entry must not hide the models listed beside it."""
    _serve(monkeypatch, b'{"data": [{"id": "ok"}, {"id": 7}, "x", {"name": "no-id"}]}')
    assert local_process.http_models_ids("http://127.0.0.1:8000/v1/models") == ["ok"]


@pytest.mark.parametrize(
    "exc",
    [urllib.error.URLError("refused"), ConnectionRefusedError(), TimeoutError()],
    ids=["urlerror", "refused", "timeout"],
)
def test_http_models_ids_reads_a_dead_server_as_nothing_serving(monkeypatch, exc):
    """A server that is down, refusing or too slow is "nothing serving": the
    wait loops poll through exactly these while a provider starts."""

    def urlopen(url, timeout):
        raise exc

    monkeypatch.setattr(local_process.urllib.request, "urlopen", urlopen)
    assert local_process.http_models_ids("http://127.0.0.1:8000/v1/models") == []
```

The test replaces `urlopen` itself, on the one `urllib.request` module object, so nothing reaches a socket; conftest's own `urlopen` patch is overridden for the test and restored after it.

- [ ] **Step 3: Run them, then prove they bite**

Run, from `llmbench/`: `uv run pytest tests/test_wt_bridge.py tests/test_local_process.py -q --no-cov`
Expected: `29 passed`.

Then, without committing, make each of these two edits in turn, run the same command, see it fail, and undo the edit:

1. In `src/llmbench/wt_bridge.py`, change `for prefix in ("Error: ", "wt: "):` to `for prefix in ("Error: ",):`. Expected: at least `wt: prefix` and `stderr wins over stdout` fail.
2. In `src/llmbench/local_process.py`, change `except (OSError, ValueError):` to `except OSError:`. Expected: `not-json` and `not-utf8` fail with a decode error.

`git diff --stat src` must print nothing before the next step.

- [ ] **Step 4: Verify and commit**

From `llmbench/`: `make check && make test`. Expected: exit 0; 707 or more passed (684 before, 23 added); `local_process.py` and `wt_bridge.py` at 100% in the coverage table. From the root: `make test-all`.

```bash
git add llmbench/tests/test_wt_bridge.py llmbench/tests/test_local_process.py
git commit -m "test(llmbench): pin wt_bridge._msg and http_models_ids here, where they will outlive modelman's copy"
```

PR 3a is ready. **Ask the owner before pushing or opening the PR.**

---

## PR 3b — the deletion

Branch: `git switch -c chore/delete-modelman main`, after PRs 1, 2 and 3a have merged.

Before starting, the controller checks one thing outside the tree (Decision 34): whether `modelman-ci` is a required status check on `main` (`gh api repos/{owner}/{repo}/branches/main/protection --jq '.required_status_checks.contexts'`, or the branch-protection page). If it is listed, it is removed, with the owner's OK, before this PR is opened: once the workflow file is deleted no run reports that check, and every later PR would wait on it.

Then confirm nothing outside modelman still needs it. From the monorepo root:

```bash
git grep -nE '^\s*(from|import) modelman' -- '*.py' ':!modelman'
git grep -n '"wt", "start"\|wt start.*--json\|wt start.*--plan' -- llmbench benchmarks bin wt/scripts
git ls-files modelman/.claude
```

Expected: all three print nothing (observed at `544cc1e` for the first two: exit 1, no output). The first is anchored to the start of a line and limited to Python files because the looser `'from modelman\|import modelman'` also matches 25 lines of prose and comments ("moved from modelman.toml", "ported from modelman's …") that later PRs own. llmbench imports nothing from modelman; only modelman called `wt start --json`; PR 1 emptied `modelman/.claude`.

### Task 7: Delete `modelman/` and everything that names it as present

**Files:**
- Move: `modelman/docs/superpowers/` (39 files) → `docs/superpowers/modelman/`
- Delete: the rest of `modelman/` (128 tracked files: 170 at `544cc1e`, less the 39 moved and the three skills that Step 4's PR 4 and this plan's PR 1 moved or deleted), `.github/workflows/modelman-ci.yml`, `wt/cmd/wt/modelman_wording_test.go`, `docs/contracts/rotation.sample.state`
- Modify: `Makefile`, `CLAUDE.md`, `README.md`, `wt/CLAUDE.md`, `wt/docs/internals/testing.md`, `wt/docs/internals/config-and-registry.md`, `wt/docs/internals/local-models.md`, `docs/guides/02-providers-and-models.md`, `04-litellm-config.md`, `05-benchmarks.md`, `07-usage-and-spend.md`, `docs/contracts/registry.sample.toml`, `catalog-predicates.sample.toml`, `discovered-ids.sample.json`, `litellm-cli.sample.json`, `omlx-model-dirs.sample.json`, `wt-start-cli.sample.json`, `modelman.sample.toml`, `bin/mlx-quantize`, `bin/check-config-dirs-untouched`, `bin/lib/mlx-lm-resolve.sh`, `wt/cmd/wt/model_cmds.go`, `wt/internal/litellm/configfile.go`, and the ten Go test files of Step 6

**Interfaces:**
- Consumes: PR 3a's tests (they replace `modelman/tests/test_llmbench_reexports.py`).
- Produces: `docs/superpowers/modelman/{plans,specs}/…`, the paths the guides cite from now on.

All of Steps 1 to 7 go in **one commit**: between any two of them `make test-all`, `make install` or `bin/check-links` is broken.

- [ ] **Step 1: Move the plans and specs first, then delete the tree**

The move must come before the removal. From the monorepo root:

```bash
test ! -e docs/superpowers/modelman && git mv modelman/docs/superpowers docs/superpowers/modelman
git rm -r -q modelman
git rm -q .github/workflows/modelman-ci.yml wt/cmd/wt/modelman_wording_test.go docs/contracts/rotation.sample.state
git ls-files modelman | wc -l
git ls-files docs/superpowers/modelman | wc -l
```

Expected: `0` and `39`. `bin/check-links` skips every path with a `superpowers` part, so the moved files' relative links are not checked and need no edit.

- [ ] **Step 2: `Makefile`**

Delete the line `	cd modelman && uv sync && make check && make test` from `test-all` and the line `	cd modelman && make install` from `install`, and change the help comment to `install: ## Install all monorepo components (wt + llmbench).`

- [ ] **Step 3: The two comments that pointed at the wording pin**

In `wt/cmd/wt/model_cmds.go`, delete the two comment lines above `if config.IndexModelByID(cfg.Models, arg) >= 0 {`:

```go
			// modelman matches the text of these two refusals: reword them
			// only together with it (TestRefusalsKeepTheWordingModelmanMatches).
```

In `wt/internal/litellm/configfile.go`, in the comment at lines 34–35, delete the sentence that begins "modelman's wt_bridge matches this text". The refusal itself stays covered by `internal/litellm/service_test.go` (the redirected-registry tests at 1272–1350) and the two `wt stop` refusals by `cmd/wt/model_cmds_test.go`; confirm with `grep -rn 'is not running\|unknown model' wt/cmd/wt/*_test.go | grep -v modelman_wording | head -3` (it must print at least one line; if it prints none, keep the three assertions by moving `TestRefusalsKeepTheWordingModelmanMatches` into `model_cmds_test.go` under the name `TestStopRefusalsSayWhy`, with a comment that says a user reads these, instead of deleting it).

- [ ] **Step 4: The contract fixture headers**

- `docs/contracts/registry.sample.toml`: line 2 "variant modelman writes and wt reads" becomes "variant wt writes and reads"; delete the reader line `#   - modelman/tests/contracts/test_registry_fixture.py (Python)` and add `#   - wt/internal/config/model_artifact_test.go (Go)`; "(wt-ci, modelman-ci, llmbench-ci)" becomes "(wt-ci, llmbench-ci)"; lines 68–69 "Mirrors the default template modelman writes (registry.py `_DEFAULT_PROVIDER_TEMPLATES`)" becomes "Mirrors the default row wt seeds (`defaultProviderRow`, wt/internal/config/registry_seed.go)"; 149 "modelman writes for mlx_lm_server models" becomes "written for mlx_lm_server models"; 175: drop the modelman clause.
- `docs/contracts/catalog-predicates.sample.toml`: the header becomes "Contract for the catalog / pricing / routing predicates (#179, #180). … read by: wt/internal/config/catalog_predicates_fixture_test.go (in_catalog, openrouter_priced), wt/internal/litellm/catalog_predicates_fixture_test.go (routable_cloud), wt/internal/cloudsync/openrouter_test.go (openrouter_priced, over registry rows). Go only." Delete the sentences about "both jobs" and "until the Python side grows them". Lines 59–60: "Both sides must honor true as well as false, or they disagree" becomes "Both readers (the typed config and the cloud-sync rows) must honor true as well as false, or they disagree".
- `docs/contracts/discovered-ids.sample.json`, `litellm-cli.sample.json`, `omlx-model-dirs.sample.json`: rewrite `_comment` only, never `cases` or any other key. discovered-ids: "… Read by wt/internal/litellm/discovered_ids_fixture_test.go (litellm.DiscoveredModel). The LiteLLM route wt writes and wt's usage and survey history key on this string. mlx_lm_server has no case because a pairing is never discovered." litellm-cli: "Fixture for the shapes `wt litellm ... --json` prints. Read by wt/cmd/wt/litellm_test.go." omlx-model-dirs: keep the description of the directory rule, with "wt (wt/internal/localmodels/sources.go, scanOmlxModels) must list these model names for this tree", "Read by a Go test in wt/internal/localmodels", and no mention of a second scanner or "both CI jobs".

- Two fixtures that later PRs delete still name a modelman test as their second reader. Correct them here too, so that no file left in the tree points into `modelman/tests`; **do not delete them in this PR** (the Go tests that read them stay until PRs 4 and 5):
  - `docs/contracts/wt-start-cli.sample.json` (deleted in PR 4): `_comment` becomes "Fixture for the shapes `wt start <id> --json` prints. Read by wt/cmd/wt/start_json_test.go."
  - `docs/contracts/modelman.sample.toml` (deleted in PR 5): in the header, lines 1–2 "Shared fixture for cross-language contract tests of modelman.toml — the per-machine mutable state file modelman writes and wt reads read-only." become "Fixture for modelman.toml, the state file modelman wrote, of which wt reads one table."; delete the reader line `#   - modelman/tests/contracts/test_modelman_fixture.py (Python)` (line 13); and the closing two lines "The point of this file is that a schema change on either side (e.g. modelman moving model_state) makes both CI jobs fail in the same PR." become "It goes when wt stops reading the file." The body below the header does not change.

After the JSON edits: `python3 -c 'import json,sys; [json.load(open(f)) for f in sys.argv[1:]]' docs/contracts/*.json` must exit 0.

- [ ] **Step 5: Prose that named the tree, the CI job or a moved path**

- Root `CLAUDE.md` (line numbers at `c935eb8`): line 23 `make test-all` — "lint + llmbench `make check`/`make test` + wt `go build`/`vet`/`test`"; line 24 `make install` — "(wt binary + shims via `wt/make install`, llmbench into its own venv)"; line 29 (the `bin/` bullet): "the CI step llmbench-ci and wt-ci run after their tests"; delete ", as do `modelman start`/`stop`"; delete the whole `modelman/` Architecture bullet (line 32); line 34 `Makefile` — "(aggregates llmbench + wt)"; line 35 `docs/` — "read by wt's Go contract tests; llmbench reads `registry.sample.toml` and `registry.written.sample.toml`"; line 36 workflows — "shell-ci (root lint), wt-ci (Go + wt lint), llmbench-ci (Python)"; delete the modelman block of "Quick test commands" (the comment line, the `cd modelman && …` line and the blank line after them: 55–57) and "`modelman/CLAUDE.md`, " from line 64. Afterwards `grep -n modelman CLAUDE.md` prints one line, the parenthesis in the `wt/` bullet that PR 5 removes.
- `README.md`: delete the `modelman/` table row (line 10) and tree entry (84); line 81 "read by wt Go and llmbench Python tests"; line 87 "CI: shell-ci, wt-ci, llmbench-ci"; line 103 "(`wt` Go tests, `llmbench` Python tests)".
- `wt/CLAUDE.md` line 99 and `wt/docs/internals/testing.md` line 37: "(root lint + llmbench + wt build/vet/test)".
- `wt/docs/internals/config-and-registry.md`. Line 79: "loaded by `internal/config` contract tests and modelman's `tests/contracts/` — a schema change must update both sides or both CI jobs fail" becomes "loaded by `internal/config` contract tests"; at the end of the line, "and modelman's `test_discovered_ids_fixture.py`" is deleted. (The link to `modelman.sample.toml` in the same line stays until PR 5.) Line 81: delete "modelman's `tests/contracts/test_registry_written_fixture.py` (`tomli_w` gives the same bytes, and modelman's reader loads them), "; "The three reader tests" becomes "The two reader tests"; "a hand edit that is not in tomli-w form fails the modelman test" becomes "a hand edit that is not in tomli-w form fails `TestWrittenFixtureIsAFixedPoint`".
- `wt/docs/internals/local-models.md` line 61 (the `wt start <id> --json` bullet, which PR 4 deletes whole): delete the parenthesis " (modelman's contract test is `modelman/tests/contracts/test_wt_start_cli_fixture.py`)".
- The moved paths. `docs/guides/02-providers-and-models.md` (three paths in Going deeper: one in each of two bullets, and one inside the parenthesis of the bullet between them), `04-litellm-config.md` (one, in Going deeper), `05-benchmarks.md` (one, in Going deeper), `07-usage-and-spend.md` (one, in Going deeper): replace `modelman/docs/superpowers/specs/` by `docs/superpowers/modelman/specs/` in each path, keeping the file name. Find them all with `git grep -n 'modelman/docs/superpowers' -- '*.md' ':!docs/superpowers' ':!wt/docs/superpowers'`; it must print nothing afterwards. Then `for f in $(git grep -oh 'docs/superpowers/modelman/[a-z]*/[A-Za-z0-9._-]*\.md' -- docs/guides | sort -u); do test -f "$f" || echo "MISSING $f"; done` must print nothing.
- `bin/mlx-quantize` lines 11–12: "modelman deliberately does not run these tools itself" becomes "wt deliberately does not run these tools itself". `bin/check-config-dirs-untouched`: line 4 "(llmbench-ci, wt-ci)"; line 14 "that has never run wt or llmbench". `bin/lib/mlx-lm-resolve.sh` line 12: "(no mention in `llmbench/pyproject.toml` or elsewhere)".

- [ ] **Step 6: Go comments that cite a deleted path**

Each of these names a file under `modelman/tests` or `modelman/src` as a second reader. Reword the header to say the fixture is read by this Go test (and, for the two `registry*.toml` fixtures, by `llmbench/tests/test_registry.py`), and delete the sentence about a rule change failing "both CI jobs": `wt/internal/tomlw/fixture_test.go:11`, `wt/internal/usage/usage_fixture_test.go:12–14`, `wt/internal/localmodels/omlx_model_dirs_fixture_test.go:21–22`, `wt/internal/litellm/discovered_ids_fixture_test.go:13–17`, `wt/internal/catalog/registry_fixture_test.go:17–24`, `wt/internal/config/registry_fixture_test.go:11–13` and `155`, `wt/internal/config/catalog_predicates_fixture_test.go:39`, `wt/cmd/wt/litellm_test.go:668–669`. Also `wt/internal/cloudsync/pricingpage_test.go:50–51` ("the same file modelman's test reads" becomes "a page saved on 2026-10-07"), and `wt/internal/config/registry_env_test.go:12–14`: "modelman (test_registry.py) and llmbench (test_registry.py) pin the same order. If wt alone resolved a different file, a scratch run would have modelman saving one registry while wt read another." becomes "llmbench (tests/test_registry.py) pins the same order. If wt alone resolved a different file, a scratch run would have llmbench benchmarking one registry while wt wrote another."

Proof:

```bash
git grep -n 'modelman/\(tests\|src\)' -- wt llmbench docs/contracts bin ':!wt/docs/superpowers' ':!wt/docs/wt-agents/litellm-troubleshooting.md'
```

Expected: exactly four lines, all under `llmbench/` (Task 8 removes them): `llmbench/CLAUDE.md` three times (the bullets about the five constants, `registry_read_path()` and the autouse guards) and `llmbench/src/llmbench/registry.py` once (the docstring of `registry_read_path`). The two exclusions are dated text that keeps its paths on purpose: wt's own plans and specs, and the troubleshooting notes Task 3 annotated. Any other line is a pointer into the deleted tree: fix its comment, and never delete a fixture to make the line go away.

- [ ] **Step 7: Verify, then commit**

From `wt/`: `test -z "$(gofmt -l .)" && go vet ./... && go test -count=1 ./... && make check`. From the root:

```bash
make lint
make test-all
make -n install
```

Expected: exit 0 for the first two; `make -n install` prints two `cd` lines (`wt`, `llmbench`) and no `modelman`. (`make install` itself rebuilds and installs the developer's `wt`; `-n` is enough here.)

```bash
git add -u
git status --short | grep -v '^[MADR]  ' | grep -v '^?? modelman/$'
git commit -m "chore: delete modelman

Its plans and specs move to docs/superpowers/modelman. modelman-ci, the
Makefile lines, the wording pin and the rotation fixture go with it; the
contract fixtures that had a Python reader in modelman are Go-only now."
```

`git add -u` stages every edit and deletion of a tracked file and no untracked file; this task creates no new file (the 39 moved ones were staged by `git mv`). The second command must print nothing before the commit: a line it prints is an unstaged or untracked path this task did not make (`?? modelman/` alone is expected: see Step 8).

- [ ] **Step 8: Remove the leftovers in this checkout**

`git rm` leaves untracked files behind: `modelman/.venv` (about 149M), `__pycache__` directories, tool caches, a `.coverage` file, the empty `modelman/.claude` directories. They were ignored by `modelman/.gitignore`, which is deleted with the tree, so `git status` now shows them as `?? modelman/` (or as ignored, on a machine whose global gitignore covers them). Do not go by that output. From the monorepo root, after the commit:

```bash
git ls-files modelman | wc -l
```

If it prints `0` (nothing under `modelman/` is tracked), remove the directory by hand: `rm -r modelman`. Then `test ! -e modelman && echo gone` prints `gone`. Every other checkout and worktree needs the same once it has this commit; guide 08 says so after PR 5.

### Task 8: llmbench looks for the registry in one place

**Files:**
- Modify: `llmbench/src/llmbench/registry.py`
- Test: `llmbench/tests/test_registry.py`
- Modify: `llmbench/CLAUDE.md`

**Interfaces:**
- Consumes: nothing.
- Produces: `registry_read_path(path: Path | None = None) -> Path` with the same signature; it no longer falls back to `~/.config/local-ai/registry.toml` when `XDG_CONFIG_HOME` names a directory with no registry.

**What changes for a user:** with `XDG_CONFIG_HOME` set and the registry only at `~/.config/local-ai/registry.toml`, llmbench used to read it. Now it says `Registry file not found: $XDG_CONFIG_HOME/local-ai/registry.toml`, the file wt already names in that case. The fix is to move the file there, or set `WT_REGISTRY`.

- [ ] **Step 1: Write the failing test**

In `llmbench/tests/test_registry.py`, replace the whole of `test_read_path_falls_back_to_the_pre_xdg_registry` with:

```python
def test_read_path_has_no_second_place_to_look(home, monkeypatch, tmp_path):
    """With XDG_CONFIG_HOME set, the registry is under it and nowhere else.
    llmbench used to fall back to a pre-XDG ~/.config registry, to agree with
    modelman. wt never did, so with modelman gone the fallback would have
    llmbench benchmark a registry wt cannot see. The error names the path
    that was looked at."""
    _write(home / ".config" / "local-ai" / "registry.toml")
    monkeypatch.setenv("XDG_CONFIG_HOME", str(tmp_path / "xdg"))
    with pytest.raises(
        RegistryError, match="Registry file not found: .*xdg/local-ai/registry.toml"
    ):
        registry_read_path()

    canonical = _write(tmp_path / "xdg" / "local-ai" / "registry.toml")
    assert registry_read_path() == canonical
    assert [m.id for m in load_registry().models] == ["ollama/a"]
```

and delete `test_read_path_refuses_a_dangling_legacy_symlink` (the file it guards is no longer read). `test_read_path_refuses_a_dangling_symlink`, `test_read_path_does_not_fall_back_past_a_named_registry`, `test_read_path_does_not_fall_back_past_wt_registry` and `test_registry_path_precedence` stay as they are.

- [ ] **Step 2: Run it to see it fail**

Run, from `llmbench/`: `uv run pytest tests/test_registry.py::test_read_path_has_no_second_place_to_look -q --no-cov`
Expected: FAIL with `Failed: DID NOT RAISE RegistryError`.

- [ ] **Step 3: Remove the fallback**

In `llmbench/src/llmbench/registry.py`, replace the whole of `registry_read_path` with:

```python
def registry_read_path(path: Path | None = None) -> Path:
    """The file load_registry reads: `path`, or registry_path(). A dangling
    symlink is refused (#248); a file that is not there is an error naming
    it. The same file wt's config.RegistryPath names: there is no second
    place to look."""
    wanted = Path(path) if path else registry_path()
    _refuse_dangling_symlink(wanted)
    if wanted.exists():
        return wanted
    raise RegistryError(f"Registry file not found: {wanted}")
```

In the same file: the module docstring's "wt owns the file; modelman still writes it until it is retired." becomes "wt owns and writes the file."; the comment above `_REGISTRY_ENV_NAMES` becomes "WT_REGISTRY is the name wt and llmbench share"; `registry_path`'s docstring ends "The same precedence as wt's config.RegistryPath."

- [ ] **Step 4: Run the tests**

Run, from `llmbench/`: `uv run pytest tests/test_registry.py -q --no-cov`
Expected: all pass.

In `tests/test_registry.py`, reword the docstrings and comments that describe modelman as a reader (lines 19, 53–54, 133, 166 at `544cc1e`): "the precedence wt uses, so the two tools never read two files"; "llmbench never writes the registry, so a top-level table it does not know costs it nothing"; drop "until it is retired, modelman's".

One more comment cites a modelman test this PR deletes. In `tests/providers/lifecycle/backends/test_mtplx.py`, the docstring of `test_resolve_uses_positional_extra_args_over_registry` (line 121 on): "local_control.py forwards mtplx's desired model through extra_args, not the `model` param — isolate_provider() only ever populates `model` from an env-var lookup, and mtplx is deliberately excluded from that mapping (test_local_control.py's test_start_mtplx_isolates_without_env_var)." becomes "a caller forwards mtplx's desired model through extra_args, not the `model` param — isolate_provider() only ever populates `model` from an env-var lookup, and mtplx is deliberately excluded from that mapping." The rest of the docstring stands.

- [ ] **Step 5: `llmbench/CLAUDE.md`**

Delete the four bullets at lines 24–27 (modelman depends on llmbench; the re-exports; the five duplicated constants; the never-imports test — the test itself is deleted in PR 3c, where nothing else mentions it). Line 51: delete the sentence "**`registry_read_path()` must resolve the same file as modelman's `_registry_read_path()`** … pins it." and say "`registry_read_path()` reads the file `registry_path()` names and nowhere else". Line 63: delete ", which is why the registry path rule above matters to `modelman start <mtplx model>`". Line 78: delete the parenthesis " (modelman keeps a copy, `../modelman/tests/test_hermeticity.py`, for the wrapper in its own conftest)"; the words "and modelman.toml" earlier in the same line stay until Task 12.

- [ ] **Step 6: Verify and commit**

From `llmbench/`: `make check && make test`. From the root: `make test-all` and `make lint`.

```bash
git add -u llmbench
git status --short llmbench
git commit -m "refactor(llmbench): the registry is read from one place, the one wt names"
```

(The task creates no file; `git status --short llmbench` must show only staged lines.)

- [ ] **Step 7: The PR's proof**

From the monorepo root:

```bash
test ! -e modelman && echo "no modelman directory"
git ls-files | grep -c '^modelman/'
grep -n modelman Makefile .github/workflows/*.yml
git grep -n 'modelman-ci\|cd modelman\|modelman/\(src\|tests\|README\|CLAUDE\|pyproject\)' -- ':!docs/superpowers' ':!wt/docs/superpowers' ':!docs/archive' ':!wt/CHANGELOG.md' ':!llmbench/src' ':!llmbench/tests' ':!docs/reference/provider-artifacts.md' ':!wt/docs/wt-agents/litellm-troubleshooting.md'
bin/check-links
```

Expected: `no modelman directory`; `0`; the third and fourth print nothing (at the docs branch tip, before PR 1, the fourth printed 44 lines in 27 files; the two excluded prose files are PR 3c's `provider-artifacts.md` and the dated troubleshooting notes); `ALL LINKS OK`. The fourth prints nothing because Task 7, Step 4 corrected the headers of the two fixtures that PRs 4 and 5 delete, and Step 5 the `local-models.md` bullet PR 4 deletes. If one of those three prints here, correct its text; do not delete the fixture, which a Go test still reads.

PR 3b is ready. **Ask the owner before pushing or opening the PR.** After it merges, the controller does the #267, #268 and #299 rows of Task 15.

---

## PR 3c — llmbench retires the llamacpp backend and the API only modelman called

Branch: `git switch -c refactor/llmbench-retire-llamacpp main`, after PR 3b has merged and question 2 is answered.

### Task 9: One set of backends, no dead wrappers, no description of modelman

**Files:**
- Delete: `llmbench/src/llmbench/providers/lifecycle/backends/llamacpp.py`, `llmbench/tests/providers/lifecycle/backends/test_llamacpp.py`
- Modify: `llmbench/src/llmbench/providers/lifecycle/backends/__init__.py`, `backends/base.py`, `orchestrate.py`, `launchd.py`, `cli.py`, `llmbench/src/llmbench/benchmark/isolation.py`
- Test: `llmbench/tests/providers/lifecycle/test_orchestrate.py`, `llmbench/tests/benchmark/test_isolation.py`, `llmbench/tests/test_main.py`, `llmbench/tests/test_conftest_guards.py`
- Modify (comments and prose): the llmbench source files of Step 7, `llmbench/CLAUDE.md`, `CLAUDE.md`, `docs/reference/provider-artifacts.md`, `docs/guides/05-benchmarks.md`, `.claude/skills/adding-a-benchmark-backend/SKILL.md`

**Interfaces:**
- Consumes: nothing.
- Produces: `SUPPORTED_PROVIDER_IDS == frozenset(BACKENDS) == {"ollama", "omlx", "omlx-6bit", "mlx_lm_server", "mtplx"}`; `isolate_provider(provider_id: str, *extra_args: str, env: dict[str, str] | None = None) -> IsolateResult` (no `solo`); `benchmark.isolation` no longer exports `stop_provider` or `stop_all_local_providers`. `lifecycle.isolate(..., solo=...)` and `llmbench provider isolate --solo` are unchanged.

**What changes for a user:** `llmbench provider list` loses its `llamacpp` row and the `[supported]` tag on the other five (Decision 32): a row is now `<id>`, a tab, then `occupancy=…` (only for an id that shares another id's server), `health=…`, `default_model=…`, `env_var=…`. `llmbench provider isolate llamacpp` and `stop llamacpp` say `unknown provider: llamacpp` (stop used to say `provider not supported for stop: llamacpp`). `LLM_ISOLATE_LLAMACPP_MODEL` is read by nothing. `stop-all` and `isolate` no longer unload a `local.llamacpp.server` LaunchAgent whose plist is installed; unload it by hand (`launchctl unload ~/Library/LaunchAgents/local.llamacpp.server.plist`).

- [ ] **Step 1: Write the failing tests**

In `llmbench/tests/providers/lifecycle/test_orchestrate.py`:

Delete `test_isolate_solo_ignored_for_llamacpp` whole. Replace `test_stop_rejects_provider_outside_supported_ids` (with its `parametrize` line) by:

```python
@pytest.mark.parametrize("provider_id", ["llamacpp", "totally-made-up"])
def test_stop_rejects_an_id_no_backend_has(stub_stops, provider_id):
    """stop() refuses an id that names no backend, and stops nothing. The
    retired `llamacpp` id is one of those now: its backend is gone, so it is
    refused like a typo instead of reviving a retired provider's control
    path."""
    result = orchestrate.stop(provider_id)
    assert result.ok is False
    assert result.error == f"unknown provider: {provider_id}"
    for backend_id, stop_and_wait in stub_stops.items():
        assert not stop_and_wait.called, backend_id


def test_isolate_rejects_the_retired_llamacpp_id(stub_stops):
    """`llmbench provider isolate llamacpp` used to start the retired
    backend. With the backend removed it must fail before any teardown: a
    script that still names it must not stop every other provider first."""
    result = orchestrate.isolate("llamacpp", "local-llama")
    assert result.ok is False
    assert result.error == "unknown provider: llamacpp"
    for backend_id, stop_and_wait in stub_stops.items():
        assert not stop_and_wait.called, backend_id


def test_every_backend_is_supported():
    """BACKENDS and SUPPORTED_PROVIDER_IDS used to differ by the retired
    llamacpp entry. They are one set now; a backend registered without being
    isolable and stoppable by name would be a third state nothing handles."""
    assert frozenset(BACKENDS) == orchestrate.SUPPORTED_PROVIDER_IDS
```

Replace `test_stop_others_covers_every_other_backend_including_llamacpp` by:

```python
def test_stop_others_covers_every_other_backend(stub_stops):
    """Every backend is torn down, not only the common ones: a provider left
    holding its port and GPU/RAM would skew every benchmark that followed."""
    orchestrate._stop_others()
    for provider_id in ("ollama", "mlx_lm_server", "mtplx"):
        assert stub_stops[provider_id].called, provider_id
```

In `test_stop_all_keep_excludes_by_occupancy_key`, the last line `assert stub_stops["llamacpp"].called` becomes `assert stub_stops["mtplx"].called`. In `test_stop_all_rejects_unknown_keep_id_instead_of_keeping_nothing`, the tuple loses `"llamacpp"`. In `test_restore_runs_restart_and_stop_actions_but_never_skip_actions`, delete the two `patch.object(BACKENDS["llamacpp"], …)` lines and the two `mock_llamacpp_*.assert_not_called()` lines, and the docstring's "(retired llamacpp, the duplicate omlx-6bit id)" becomes "(the duplicate omlx-6bit id)". Reword the comment near line 685 that contrasts "llamacpp's START path".

In `llmbench/tests/test_main.py`, delete the line `        "llamacpp",` from `test_provider_list_names_every_backend`, and delete `test_llmbench_never_imports_modelman` whole (Decision 25). It was the only user of two imports: delete both `import ast` and `from pathlib import Path` (leaving `Path` fails `make check` with ruff `F401 pathlib.Path imported but unused`). The module docstring `"""The `llmbench` command tree, and the one rule about what it imports."""` becomes `"""The `llmbench` command tree."""`. `test_provider_list_names_every_backend` reads only the text before each row's tab, so the tag removed in Step 3 does not touch it.

In `llmbench/tests/benchmark/test_isolation.py`: delete `test_isolate_provider_solo_forwards_flag`, `test_isolate_provider_default_is_not_solo`, and the whole block from the comment `# --- stop_provider / stop_all_local_providers` down to (not including) `# --- restore_providers`; drop `stop_all_local_providers` and `stop_provider` from the import; in `test_isolate_provider_success` the assertion becomes `mock_isolate.assert_called_once_with("ollama", None, extra_args=())`, and in `test_isolate_provider_forwards_extra_args` the call's last argument `, solo=False` is deleted; in the docstring of `test_supported_provider_ids_matches_the_backends_registry_documented_list` replace the two sentences about llamacpp being "registered in BACKENDS but retired … deliberately absent" by "Every registered backend is in it." The literal set in that test does not change.

- [ ] **Step 2: Run them to see them fail**

Run, from `llmbench/`: `uv run pytest tests/providers/lifecycle/test_orchestrate.py tests/benchmark/test_isolation.py tests/test_main.py -q --no-cov`
Expected: `6 failed, 50 passed`. The six: `test_stop_rejects_an_id_no_backend_has[llamacpp]` (the error is `provider not supported for stop: llamacpp`), `test_isolate_rejects_the_retired_llamacpp_id` (isolate proceeds, and its error is the missing plist), `test_every_backend_is_supported`, `test_provider_list_names_every_backend`, and `test_isolate_provider_success` and `test_isolate_provider_forwards_extra_args` (`expected call not found`).

- [ ] **Step 3: Remove the backend**

```bash
git rm -q llmbench/src/llmbench/providers/lifecycle/backends/llamacpp.py llmbench/tests/providers/lifecycle/backends/test_llamacpp.py
```

In `backends/__init__.py`: delete `from .llamacpp import LLAMACPP` and `BACKENDS[LLAMACPP.id] = LLAMACPP`; delete the docstring's last paragraph ("`llamacpp` is deliberately IN `BACKENDS`…"); replace the comment block and the constant at the end of the file by:

```python
# What llmbench may isolate on a caller's behalf, and the set
# `orchestrate.stop()` accepts: every backend. It keeps its own name because
# `llmbench.benchmark.isolation.SUPPORTED_PROVIDER_IDS` re-exports it and the
# benchmark runners filter on it.
SUPPORTED_PROVIDER_IDS: frozenset[str] = frozenset(BACKENDS)
```

In `launchd.py`: delete the line `LLAMACPP_PLIST = Path.home() / "Library/LaunchAgents/local.llamacpp.server.plist"`. In `tests/test_conftest_guards.py`: delete the line `    "launchd.LLAMACPP_PLIST": launchd.LLAMACPP_PLIST,` (in the same step: the constant without the table entry, or the reverse, fails at import).

In `backends/base.py`: delete the `respects_solo` paragraph of the docstring and the field `    respects_solo: bool = True`; reword the comments at lines 3 and 141 that list llamacpp among the backends.

In `orchestrate.py`: `if solo and backend.respects_solo:` becomes `if solo:`; in `stop()`, replace the `if provider_id not in SUPPORTED_PROVIDER_IDS:` block by:

```python
    if provider_id not in SUPPORTED_PROVIDER_IDS:
        return LifecycleResult(provider_id, "", "", False, f"unknown provider: {provider_id}")
```

In `cli.py`, replace the whole of `list_cmd` (from `@provider_app.command("list")` to the blank lines before `__all__`) with:

```python
@provider_app.command("list")
def list_cmd() -> None:
    """Print every lifecycle provider id with its occupancy key (when it
    shares another id's server), health URL, default model and env var."""
    for provider_id in sorted(lifecycle.BACKENDS):
        backend = lifecycle.BACKENDS[provider_id]
        occupancy = (
            "" if backend.occupancy_key == provider_id else f"occupancy={backend.occupancy_key} "
        )
        default_model = backend.default_model or "none"
        env_var = backend.env_var or "none"
        typer.echo(
            f"{provider_id}\t{occupancy}health={backend.health_url} "
            f"default_model={default_model} env_var={env_var}"
        )
```

The `supported`/`retired-only` status and the docstring's sentence about it are gone: with one set of backends the branch that printed `retired-only` cannot run (Decision 32).

In `orchestrate.py`, also reword the four docstrings that explain the llamacpp exception: `_stop_others` (delete the sentence "`llamacpp` IS included here even though…"), `stop` (delete the paragraph "Gated on `SUPPORTED_PROVIDER_IDS`, not `BACKENDS`…"), `stop_all` (delete "`keep` is checked against `BACKENDS`, not `SUPPORTED_PROVIDER_IDS` — llamacpp is…"), `_restore_litellm` (delete "— unlike llamacpp's START path, which fails loudly on a missing plist").

- [ ] **Step 4: Remove the wrappers nothing calls**

In `llmbench/src/llmbench/benchmark/isolation.py`: delete `stop_all_local_providers` and `stop_provider` whole; in `isolate_provider`, the signature becomes

```python
def isolate_provider(
    provider_id: str, *extra_args: str, env: dict[str, str] | None = None
) -> IsolateResult:
```

its call becomes `result = lifecycle.isolate(provider_id, model, extra_args=extra_args)`, the docstring's last paragraph (`solo=True` skips…) is deleted, and in the `env` paragraph "this is how `modelman start` warms up a *specific* model" becomes "this is how the eval runner warms up a *specific* model". In the module docstring, "The five public names and their signatures are unchanged, so no caller (`local_control.py`, `benchmark/runner.py`, `benchmark/agent/runner.py`) needed edits" becomes "Its callers are the three benchmark runners"; the comment above `SUPPORTED_PROVIDER_IDS` drops `local_control.py`.

- [ ] **Step 5: Run the tests**

Run, from `llmbench/`: `make check && make test`
Expected: exit 0; mypy `58 source files`; `683 passed`; coverage 93.36% (floor 90%). `uv run llmbench provider list` (it prints the registry of backends and touches nothing) prints five rows, in this order and shape:

```
mlx_lm_server	health=http://localhost:8001/v1/models default_model=none env_var=none
mtplx	health=http://localhost:8003/v1/models default_model=none env_var=none
ollama	health=http://localhost:11434/api/tags default_model=ornith-1.5:35b env_var=LLM_ISOLATE_OLLAMA_MODEL
omlx	health=http://localhost:8000/v1/models default_model=Ornith-1.5-35B-A3B-MLX-4bit env_var=LLM_ISOLATE_OMLX_4BIT_MODEL
omlx-6bit	occupancy=omlx health=http://localhost:8000/v1/models default_model=Ornith-1.5-35B-A3B-MLX-6bit env_var=LLM_ISOLATE_OMLX_6BIT_MODEL
```

- [ ] **Step 6: Commit**

```bash
git add -u llmbench
git status --short llmbench
git commit -m "refactor(llmbench): remove the retired llamacpp backend and the isolation wrappers only modelman called"
```

(No file is created in Steps 1 to 5, and the two deleted ones were staged by `git rm`; `git status --short llmbench` must show only staged lines.)

- [ ] **Step 7: Sweep llmbench's comments, and the prose about the backend**

Every edit in this step is to a comment or a docstring; no executable line changes. Three rules decide a line that is not listed. **(R1) Provenance** ("mirrors modelman's X", "as modelman's Y does"): state the rule as llmbench's own and drop the name and any modelman file or function name. **(R2) Sharing** ("reached from the benchmarks and modelman's `local_control.py`", "wt, modelman and llmbench share"): name the sharers that are left (wt, llmbench). **(R3) A cross-tool reason for a rule that stays** ("because `modelman start` may have loaded something else"): give the reason that survives, or none.

Source files, under `llmbench/src/llmbench/` (line numbers at `544cc1e`):

| File | Lines | Before | After |
|---|---|---|---|
| `_env.py` | 1 | `"""Environment variables that were renamed when llmbench left modelman."""` | `"""Environment variables llmbench reads under two names: its own, and an older one kept as an alias."""` (line 11's `MODELMAN_` is in capitals and stays) |
| `wt_bridge.py` | 3–6 | "wt resolves the registry's secret_ref, which an omlx with an API key wants before it loads a model. modelman's own bridge (`modelman/wt_bridge.py`, the `wt litellm ...`, `wt start` and `wt stop` calls) re-exports the exception classes below, so one `except WtBridgeError` catches a failure from either." | "wt resolves the registry's secret_ref, which an omlx with an API key wants before it loads a model." |
| `main.py` | 11–13 | "…sit at the top level here. modelman mounts the same sub-app under `benchmark` until it is retired." | "…sit at the top level here." |
| `benchmark/eval/evalplus_runner.py` | 113–114 | "a console script started by its own path (the installed `modelman` shim, which mounts these commands)" | "a console script started by its own path (an installed `llmbench` entry point)" (`[project.scripts]` in `pyproject.toml` defines `llmbench`) |
| `benchmark/eval/runner.py` | 111 | "Mirrors `modelman start`'s in-process pattern (local_control.py):" | "A backend takes its model through one of two channels:" |
| `benchmark/eval/runner.py` | 706 | "or a file written by a newer/older modelman with different fields" | "or a file written by a newer or older llmbench with different fields" |
| `providers/mtplx.py` | 3–5 | "modelman keeps its own copy in `modelman/providers/mtplx.py` until it is retired; wt has one in `wt/internal/localmodels` (FamilyOrigin's default-port table). The three agree through registry.toml's `auth.base_url`, not imports." | "wt has its own copy in `wt/internal/localmodels` (FamilyOrigin's default-port table). The two agree through registry.toml's `auth.base_url`, not imports." |
| `providers/lifecycle/orchestrate.py` | 145–148 | "sibling providers alone — modelman's same-provider-only local-model lifecycle (`local_control.py`). Never passed by `llmbench`, which still needs full exclusivity for clean measurement." | "sibling providers alone — what `llmbench provider isolate --solo` asks for: starting a pairing beside other models. The benchmarks never pass it; they need full exclusivity for clean measurement." |
| `providers/lifecycle/orchestrate.py` | 21 | "Callers (`llmbench.benchmark.isolation`, `local_control.py`) branch on `ok`." | "Callers (`llmbench.benchmark.isolation`, `cli.py`) branch on `ok`." |
| `providers/lifecycle/backends/mtplx.py` | 56 | "(how local_control.py forwards it — isolate_provider() only ever populates…" | "(how the runners forward it — isolate_provider() only ever populates…" |
| `providers/lifecycle/cli.py` | 8–12 | "In-process callers (`llmbench.benchmark.isolation`, `local_control.py`) skip this layer…" and "Follows the same sub-app pattern as `llmbench.benchmark.cli.benchmark_app` and `modelman.usage.cli.usage_app`: one `typer.Typer()` per concern" | "In-process callers (`llmbench.benchmark.isolation`) skip this layer…" and "Follows the same sub-app pattern as `llmbench.benchmark.cli.benchmark_app`: one `typer.Typer()` per concern" |
| `providers/lifecycle/backends/omlx.py` 18; `backends/mlx_lm_server.py` 19; `backends/ollama.py` 7 | | "reached from `llmbench provider ...`, the benchmarks, and modelman's `local_control.py`." | "reached from `llmbench provider ...` and the benchmarks." |
| `providers/lifecycle/backends/ollama.py` | 82–84 | "(not just the default model — modelman may have loaded something else via `modelman start`)" | "(not just the default model — something else may have been loaded, with `wt start` or `ollama run`)" |
| `providers/lifecycle/backends/mtplx.py` | 150 | "which is exactly the state on a machine's first `modelman start` of an mtplx model" | "which is exactly the state on a machine's first start of an mtplx model" |

`providers/lifecycle/launchd.py:6` names the alias `MODELMAN_LITELLM_RESTART_CMD` and does not change. `registry.py` was done in Task 8, `benchmark/isolation.py` in Step 4, `state.py` is PR 5's.

Test docstrings and comments, under `llmbench/tests/`:

| File | Lines | After |
|---|---|---|
| `benchmark/test_isolation.py` | 97 (its number before Step 1) | "`modelman start` passes env=…" becomes "The eval runner passes env=…" |
| `providers/lifecycle/test_orchestrate.py` | 213 | "a model modelman already has loaded" becomes "a model that is already loaded" |
| `providers/lifecycle/test_orchestrate.py` | 553 | "`modelman stop --all` must not error out" becomes "`llmbench provider stop-all` must not error out" |
| `providers/lifecycle/test_cli.py` | 140–141 | "(modelman start's same-provider-only lifecycle)" becomes "(a start beside the other providers)" |
| `providers/lifecycle/backends/test_ollama.py` | 178–179 | "modelman may have loaded a model other than the default via `modelman start`" becomes "A model other than the default may be loaded (`wt start`, `ollama run`)" |
| `providers/lifecycle/backends/test_mtplx.py` | 382 | "a machine's first `modelman start` of an mtplx model" becomes "a machine's first start of an mtplx model" |

`tests/benchmark/test_cli.py:118–122` and `tests/benchmark/agent/test_runner.py:747` say the alias is "the name it had under modelman": that is history a reader of an alias test needs, and it stays.

Prose:

- `llmbench/CLAUDE.md` line 59:Prose:

- `llmbench/CLAUDE.md` line 59: "with `SUPPORTED_PROVIDER_IDS` (`ollama`, `omlx`, `omlx-6bit`, `mlx_lm_server`, `mtplx`) excluding the retired-only `llamacpp`" becomes "and `SUPPORTED_PROVIDER_IDS` holds the same ids (`ollama`, `omlx`, `omlx-6bit`, `mlx_lm_server`, `mtplx`)". Line 64: "Artifacts and the retired llamacpp provider".
- Root `CLAUDE.md` line 15: "(for the benchmarks; llama.cpp was retired on 2026-09-07 and its backend removed — see `docs/reference/provider-artifacts.md`)".
- `docs/guides/05-benchmarks.md` line 106: unchanged ("llamacpp retired 2026-09-07" is still true).
- `.claude/skills/adding-a-benchmark-backend/SKILL.md`. Step 2 (lines 30–36) becomes: "**Register it.** Add the module's singleton instance to `BACKENDS` in `backends/__init__.py`. `SUPPORTED_PROVIDER_IDS`, in the same file, is derived from `BACKENDS`: a registered backend is isolable and stoppable by name, by `llmbench provider isolate` and by the benchmarks alike (`llmbench.benchmark.isolation.SUPPORTED_PROVIDER_IDS` re-exports it)." Step 6 (lines 71–77): "hand-writes a literal copy of `SUPPORTED_PROVIDER_IDS` specifically so a backend added to `BACKENDS` without an isolability decision fails a test instead of silently running unisolated — update that literal alongside `backends/__init__.py`'s constant" becomes "hand-writes a literal copy of the backend ids, so that registering a backend is written down in two places — update that literal". The sentence about `test_provider_list_names_every_backend` stays.
- `docs/reference/provider-artifacts.md`. Lines 34–41 ("Disabled in the repo … Kept:"): the provider implementation in modelman and the `LlamaCppBackend` are both **gone**; say "Removed from the repo in the modelman retirement (Step 6): modelman's `providers/llamacpp.py` with modelman itself, and llmbench's `backends/llamacpp.py`. Both are in git history; the last commit that has them is the parent of the commit that deleted `modelman/` (`git log --diff-filter=D --format=%H -1 -- modelman/pyproject.toml`) and of the one that deleted the backend (`git log --diff-filter=D --format=%H -1 -- llmbench/src/llmbench/providers/lifecycle/backends/llamacpp.py`)." The re-enable procedure, step 7 (lines 73–101): replace its sub-bullets by: "restore `llmbench/src/llmbench/providers/lifecycle/backends/llamacpp.py` and its test from git history (`git show <that commit>^:<path> > <path>`); register it in `backends/__init__.py` (`from .llamacpp import LLAMACPP`, `BACKENDS[LLAMACPP.id] = LLAMACPP`); restore `LLAMACPP_PLIST` in `launchd.py` and its entry in `tests/test_conftest_guards.py`; set its `restore_action` to `"restart"`; add `llamacpp` to `defaultProviderIDs` in `wt/internal/config/registry_seed.go` if wt should seed its row. wt has no lifecycle backend for it and never had: `wt start` cannot start it." Delete the two sub-bullets that edit files under `modelman/`. Add one sentence under the stop-all description: "`llmbench provider stop-all` no longer unloads the LaunchAgent; if the plist is installed, `launchctl unload` it by hand."

- [ ] **Step 8: The PR's proof**

From the monorepo root:

```bash
grep -rn 'modelman\|Modelman' llmbench/src
grep -rn 'LLM_ISOLATE_LLAMACPP_MODEL\|LLAMACPP_PLIST\|respects_solo' llmbench
grep -rn -i llamacpp llmbench/src
grep -rn 'stop_all_local_providers\|stop_provider\b' llmbench .claude docs/guides CLAUDE.md
git grep -n 'present in `BACKENDS`\|excluded from `SUPPORTED_PROVIDER_IDS`' -- ':!docs/superpowers'
git grep -n 'retired-only\|local_control' -- llmbench/src .claude CLAUDE.md llmbench/CLAUDE.md
```

Expected: the first prints only the 11 lines of `llmbench/src/llmbench/state.py` (the `modelman.toml` fallback, PR 5; 35 lines in 15 files before PR 3b), or nothing if PR 5 merged first. The second and fourth print nothing. The third prints exactly one line, `benchmark/agent/judge.py`'s `_MODEL_TOKEN_RE` (it redacts model ids in old transcripts and is not the backend; 46 lines before). The fifth and sixth print nothing.

From `llmbench/`: `make check && make test`. From the root: `make test-all`, `make lint`.

```bash
git add -u
git status --short | grep -v '^[MADR]  '
git commit -m "docs(llmbench): comments and the llama.cpp reference say what is in the tree now"
```

(Step 7 creates no file. The second command must print nothing: a line it prints is a path this task did not edit.)

PR 3c is ready. **Ask the owner before pushing or opening the PR.**

---

## PR 4 — remove `wt start --json` and `--plan`

Branch: `git switch -c refactor/wt-start-drop-json main`, after PR 3b has merged.

### Task 10: The flags, their code, their fixture and their docs

**Files:**
- Create: `wt/cmd/wt/start_flags_test.go`
- Delete: `wt/cmd/wt/start_json.go`, `wt/cmd/wt/start_json_test.go`, `docs/contracts/wt-start-cli.sample.json`
- Modify: `wt/cmd/wt/start.go`, `wt/cmd/wt/model_cmds.go`, `wt/cmd/wt/loading_test.go`, `wt/internal/lifecycle/evictions.go`, `wt/internal/lifecycle/evictions_test.go`, `wt/internal/lifecycle/omlxpool_test.go`, `wt/internal/localmodels/inventory.go`, `wt/docs/wt-start-stop.md`, `wt/docs/internals/local-models.md`, `wt/CLAUDE.md`, `wt/CHANGELOG.md`

**Interfaces:**
- Consumes: nothing.
- Produces: `startCmd(a *app) *cobra.Command` with no local flags (`--replace` stays: it is a persistent root flag). `sessionCounts` (`var sessionCounts = func(ids []string) map[string]int`) is defined in `start.go`. `lifecycle.Evictions` is no longer exported from non-test code; `Evictions(t Target, snap localmodels.Snapshot) (victims []localmodels.Entry, known bool)` exists in `evictions_test.go` for the package's tests only.

**What changes for a user:** `wt start <id> --json` and `wt start <id> --plan --json` fail with cobra's `unknown flag: --json` (or `--plan`), exit 1, and start nothing. Nothing in this repo called them but modelman. A script that used the plan should call `wt model list --json` (STATUS and RUNNING per model) before `wt start <id> --replace`; there is no dry run of a start any more.

- [ ] **Step 1: Write the failing test**

Create `wt/cmd/wt/start_flags_test.go`:

```go
package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestStartHasNoJSONOrPlanFlag pins the removal of `wt start --json` and
// `--plan` (modelman was their only caller). A script that still passes one
// must be told so by name and exit non-zero with nothing started: a flag that
// was silently accepted and ignored would start a model, and possibly unload
// another, where the script asked for a dry run.
func TestStartHasNoJSONOrPlanFlag(t *testing.T) {
	for _, flag := range []string{"--json", "--plan"} {
		t.Run(flag, func(t *testing.T) {
			cfg := loadingFixture(t)
			started := stubLifecycleStart(t, nil)
			driver := stubStartDriver(t, nil)
			c := startCmd(&app{cfg: cfg})
			var out, errOut bytes.Buffer
			c.SetOut(&out)
			c.SetErr(&errOut)
			c.SetArgs([]string{"omlx/c", flag})
			err := c.Execute()
			if err == nil || !strings.Contains(err.Error(), "unknown flag: "+flag) {
				t.Fatalf("err = %v, want cobra's unknown flag: %s", err, flag)
			}
			if len(*started) != 0 || driver.called || out.Len() != 0 {
				t.Errorf("starts = %d, driver called = %v, stdout = %q; want nothing done", len(*started), driver.called, out.String())
			}
		})
	}
}
```

`loadingFixture` is in `loading_test.go`, `stubLifecycleStart` in `start_test.go`, `stubStartDriver` in `testmain_test.go`; all three stay.

- [ ] **Step 2: Run it to see it fail**

Run, from `wt/`: `go test ./cmd/wt -run TestStartHasNoJSONOrPlanFlag -count=1`
Expected: FAIL in both subtests with `err = <nil>, want cobra's unknown flag: --json` (the flag exists and a plan is printed) and `err = --plan needs --json, want cobra's unknown flag: --plan`.

- [ ] **Step 3: Move the seam the interactive start uses**

`sessionCounts` is defined in `start_json.go` and used by `runStart`'s replace prompt (`start.go`, the `errors.As(err, &occ)` arm) and stubbed in `testmain_test.go`. In `wt/cmd/wt/start.go`, add `"github.com/ohanaverse/local-ai-setup/wt/internal/refcount"` to the import block (after `…/internal/lifecycle`), and add after the imports:

```go
// sessionCounts is the number of live wt sessions using each model id. A test
// seam; production sweeps dead sessions first, as the stop picker does.
var sessionCounts = func(ids []string) map[string]int {
	store := refcount.NewStore()
	_ = store.Sweep()
	return store.Counts(ids)
}
```

Do not build yet: the name is now declared twice until the next step.

- [ ] **Step 4: Delete the JSON form**

```bash
git rm -q wt/cmd/wt/start_json.go wt/cmd/wt/start_json_test.go docs/contracts/wt-start-cli.sample.json
```

In `wt/cmd/wt/model_cmds.go`, replace the whole of `startCmd` with:

```go
func startCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "start [model]",
		Short: "Start a local model",
		Long: "Start a local model (<provider>/<name>, as with -M). With no argument, shows\n" +
			"the full model picker over every local model that is on disk or running,\n" +
			"registered or detected (requires a TTY).\n\n" +
			"A model that is already running is left running; its LiteLLM route is\n" +
			"written if it is missing. If omlx is still loading the model, wt waits for\n" +
			"the load.\n\n" +
			"On a provider that serves one model (mtplx), starting another replaces it.\n" +
			"On omlx a model loads beside the ones already loaded; when it does not fit,\n" +
			"omlx unloads the least recently used. wt asks before either; --replace skips\n" +
			"the question.",
		Example: "  wt start ollama/qwen3.8:27b-mlx\n  wt start",
		Args:    cobra.MaximumNArgs(1),
		// A refused or failed start is not a usage mistake.
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if a.cfgErr != nil {
				return configError(a.cfgErr)
			}
			id := ""
			if len(args) > 0 {
				id = args[0]
			}
			replace, _ := cmd.Flags().GetBool("replace")
			return runStart(cmd.OutOrStdout(), a.cfg, a.theme, id, replace)
		},
	}
}
```

(`errors` and `encoding/json` stay imported in `model_cmds.go`: `wt served` uses both.)

In `wt/cmd/wt/loading_test.go`, delete `TestStartJSONOnALoadingModelPlansNothingAndWaits` with its comment, and the `"encoding/json"` import. `TestStartOnALoadingModelWaitsInsteadOfSayingAlreadyRunning` and `TestALoadingModelIsNotLaunchableButAPinStartsIt` stay.

- [ ] **Step 5: Move `Evictions` into the test file**

In `wt/internal/lifecycle/evictions.go`, cut the function `Evictions` with its comment (from `// Evictions reports the running models` to the closing brace before `func evictions(`). In `wt/internal/lifecycle/evictions_test.go`, paste it above `func ids(`, with its comment's first sentence changed:

```go
// Evictions is the tests' way to ask the eviction plan by provider id: it
// looks up the family's tenancy, as the engine does, and returns what
// evictions says. It reports the running models that starting t is expected to
// displace: an Exclusive server's one occupant, nobody on a Shared server, and
// on a Pool the models the plan says omlx would unload to make room. known is
// false when the snapshot's probe for the family cannot be trusted — a caller
// acting on "nobody" there would displace a model it never saw. A family whose
// server refused the connection is the exception: that is known, and empty.
// So is a target that is itself mid-load: it displaces nobody new.
func Evictions(t Target, snap localmodels.Snapshot) (victims []localmodels.Entry, known bool) {
	family := localmodels.Family(t.ProviderID)
	b := backendsByFamily[family]
	if b == nil {
		return nil, true
	}
	return evictions(b.tenancy(), family, t, snap)
}
```

Reword three comments that name the removed flags: `evictions_test.go` near line 98 ("`--plan --json` answer unknown, and a scripted start fail" becomes "a start that names `--replace` fail"; read the sentence and keep its point), `omlxpool_test.go` near 691 (the message "the plan `wt start --plan` prints must agree with the start" becomes "the plan must agree with the start"), `internal/localmodels/inventory.go` near 143.

- [ ] **Step 6: Build and run**

Run, from `wt/`: `test -z "$(gofmt -l .)" && go build ./... && go vet ./... && go test -count=1 ./cmd/wt ./internal/lifecycle ./internal/localmodels`
Expected: exit 0; `TestStartHasNoJSONOrPlanFlag` passes.

- [ ] **Step 7: The docs**

- `wt/docs/wt-start-stop.md`: delete line 9 (`wt start <model> --plan --json   # what a start would unload; changes nothing`); at line 147 delete ", or `wt start --plan` (which answers `unknown`)" so the list ends at `wt stop`; delete the whole section `### --plan and --json` (lines 172–198, through "Both shapes are pinned by `docs/contracts/wt-start-cli.sample.json`").
- `wt/docs/internals/local-models.md`: delete the bullet at line 61 ("`wt start <id> --json` (`start_json.go`, `runStartJSON`) never prompts…"); at 20 and 31 "`wt start <id> --plan --json` answers `fits`" becomes "a start of it plans nothing"; at 30 "or `wt start --plan` (`unknown`)" is deleted; in line 28's list, `Evictions` is deleted (the engine's `resolveEvictions` is what a reader should look for).
- `wt/CLAUDE.md`: delete the example line `wt start <id> --plan --json          # dry run: …` (line 332) and `Evictions` from the list in the lifecycle section.
- `wt/CHANGELOG.md`: add under the newest unreleased heading (old entries are not edited):

```markdown
- **Removed:** `wt start --json` and `wt start --plan`. modelman, their only
  caller, is deleted. Either flag is now `unknown flag`. `wt model list --json`
  shows what is on disk and running before a `wt start <id> --replace`.
```

- [ ] **Step 8: The proof**

From the monorepo root:

```bash
git grep -n 'start_json\|runStartJSON\|startPlanJSON\|startResultJSON\|wt-start-cli' -- ':!docs/superpowers' ':!wt/docs/superpowers'
git grep -n -e '--plan' -- wt docs/guides CLAUDE.md ':!wt/docs/superpowers' ':!wt/CHANGELOG.md'
grep -rn 'lifecycle\.Evictions' wt --include='*.go'
bin/check-links
```

Expected: the first prints nothing. The second prints nothing (it printed lines in `wt/docs/wt-start-stop.md`, `wt/docs/internals/local-models.md`, `wt/CLAUDE.md`, `wt/cmd/wt/model_cmds.go`, `start_json.go` and four test files before). The third prints nothing. `ALL LINKS OK`.

From `wt/`: `go test -count=1 ./... && make check`. From the root: `make test-all`, `make lint`.

- [ ] **Step 9: Commit**

```bash
git add wt/cmd/wt/start_flags_test.go
git add -u wt docs/contracts
git status --short wt docs/contracts
git commit -m "refactor(wt)!: remove wt start --json and --plan

modelman's bridge was their only caller. The session-count seam moves to
start.go, where the replace prompt uses it; lifecycle.Evictions becomes a
test helper."
```

PR 4 is ready. **Ask the owner before pushing or opening the PR.** After it merges, the controller does the #260 row of Task 15.

---

## PR 5 — nothing reads `modelman.toml`

Branch: `git switch -c refactor/stop-reading-modelman-toml main`, after PR 3b has merged. If the owner vetoed Decision 7 (llmbench keeps its fallback), skip Task 12 and write Task 13's removal section with the sentence "llmbench still reads the `[benchmarks]` table of this file once, while `benchmarks/latest.toml` does not exist".

### Task 11: wt stops reading `modelman.toml`

**Files:**
- Delete: `wt/internal/config/modelman.go`, `wt/internal/config/modelman_fixture_test.go`, `docs/contracts/modelman.sample.toml`
- Rename and trim: `wt/internal/config/modelman_test.go` → `wt/internal/config/catalog_membership_test.go`
- Modify: `wt/internal/config/config.go`, `wt/internal/config/registry.go`, `wt/internal/config/litellm_state_test.go`, `wt/internal/config/lock_test.go`, `wt/cmd/wt/main_test.go`, `wt/scripts/agents-smoke.sh`, `wt/CHANGELOG.md`, and the doc lines of Step 7

**Interfaces:**
- Consumes: nothing.
- Produces: `type LitellmState struct { Enabled bool; URL string; APIKey string }` defined in `config.go` (same fields and tags). `finalizeCfg(cfg *Config, providers []Provider, models []Model) *Config` (was four parameters and `(*Config, error)`). `config.ModelmanPath` no longer exists. `Config.migratedLitellm` no longer exists.

**What changes for a user:**
- wt no longer opens `~/.config/local-ai/modelman.toml`. A broken or unreadable one no longer fails every wt command with `parse modelman.toml: …`.
- A machine with a `config.toml`: nothing changes. wt copied the `[litellm]` table into `config.toml` on its first load after 2026-09-21 and has read its own copy since.
- A machine with **no** `~/.config/agent-wt/config.toml` whose routing state was only in `modelman.toml`: routing now reads as off (`wt litellm status`). To restore it: `wt litellm set --url <url> --api-key <key>` with the values from the old file's `[litellm]` table, then `wt litellm on`. Nothing warns about this at run time (Decision 6); the changelog and guide 08 say it.

- [ ] **Step 1: Write the failing test**

In `wt/internal/config/litellm_state_test.go`, replace everything from `func litellmStateEnv(` down to (not including) the comment `// TestUpdateLitellmPersists pins the setter` with:

```go
func litellmStateEnv(t *testing.T, wtToml string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("WT_REGISTRY", "")
	t.Setenv("MODELMAN_REGISTRY", "")
	writeUnder(t, home, "local-ai/registry.toml", "providers = []\nmodels = []\n")
	if wtToml != "" {
		writeUnder(t, home, "agent-wt/config.toml", wtToml)
	}
	return home
}

// writeUnder writes body to home/rel, creating the directories.
func writeUnder(t *testing.T, home, rel, body string) {
	t.Helper()
	p := filepath.Join(home, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestLoadNeverReadsModelmanToml pins the end of the legacy fallback: wt used
// to take LiteLLM routing state from modelman.toml's [litellm] table when its
// own config.toml had none, and to copy it in. modelman is gone, and the file
// is one the user is told to delete, so it must change nothing: routing stays
// off, config.toml is not created, the key in the old file is not copied
// into it, and a file that is not even TOML no longer stops every wt command
// with "parse modelman.toml".
func TestLoadNeverReadsModelmanToml(t *testing.T) {
	const populated = "[litellm]\nenabled = true\nurl = \"http://localhost:4000\"\napi_key = \"sk-legacy\"\n"
	const ownConfig = "default_tag = \"code\"\n"
	for _, tc := range []struct {
		name, wtToml, modelmanToml string
	}{
		{"config.toml without [litellm]", ownConfig, populated},
		{"no config.toml", "", populated},
		{"modelman.toml is not TOML", ownConfig, "this is not toml {{{"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := litellmStateEnv(t, tc.wtToml)
			writeUnder(t, home, "local-ai/modelman.toml", tc.modelmanToml)
			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load = %v, want modelman.toml ignored", err)
			}
			if cfg.IsLitellm() || cfg.LitellmBaseURL() != "" || cfg.LitellmAPIKey() != "" || cfg.LitellmTable != nil {
				t.Fatalf("routing state = %+v (table %+v), want none: modelman.toml was read", cfg.litellm, cfg.LitellmTable)
			}
			got, err := os.ReadFile(filepath.Join(home, "agent-wt", "config.toml"))
			switch {
			case tc.wtToml == "" && !os.IsNotExist(err):
				t.Fatalf("config.toml was created by Load (err=%v):\n%s", err, got)
			case tc.wtToml != "" && (strings.Contains(string(got), "litellm") || strings.Contains(string(got), "sk-legacy")):
				t.Fatalf("modelman.toml's [litellm] was copied into config.toml:\n%s", got)
			}
		})
	}
}

```

This deletes `legacyLitellm`, `TestLitellmStateMigratesFromModelmanOnce` and `TestLitellmStateNoConfigTomlDoesNotCreateIt`. The assertion on `config.toml` is "no `litellm`, no key", not "byte-identical": `Load`'s schema migration adds an `agy` agent to this fixture, which is not this test's subject.

Then replace the whole of `TestLitellmOwnEmptyTableWinsOverLegacy` (with its comment) by:

```go
// TestLitellmOwnEmptyTableIsATableNotAbsence pins that a config.toml with its
// own zero-valued [litellm] (the user turned routing off in wt), or a bare
// [litellm] header, decodes to a non-nil table with routing off. `wt litellm
// status` and UpdateLitellm tell "off" from "never set" by that pointer.
func TestLitellmOwnEmptyTableIsATableNotAbsence(t *testing.T) {
	for _, body := range []string{
		"default_tag = \"code\"\n[litellm]\nenabled = false\nurl = \"\"\napi_key = \"\"\n",
		"default_tag = \"code\"\n[litellm]\n",
	} {
		litellmStateEnv(t, body)
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.LitellmTable == nil {
			t.Fatalf("[litellm] in %q must decode to a non-nil pointer", body)
		}
		if cfg.IsLitellm() || cfg.LitellmAPIKey() != "" || cfg.LitellmBaseURL() != "" {
			t.Fatalf("empty [litellm] turned routing on: %+v", cfg.litellm)
		}
	}
}

```

In the rest of the file, the two remaining calls `litellmStateEnv(t, "default_tag = \"code\"\n", "")` (in `TestUpdateLitellmPersists` and near line 244) lose their third argument. The file's imports stay as they are (`bytes`, `fmt`, `os`, `path/filepath`, `strings`, `sync`, `testing` are all still used).

- [ ] **Step 2: Run it to see it fail**

Run, from `wt/`: `go test ./internal/config -run 'TestLoadNeverReadsModelmanToml' -count=1 2>&1 | grep -v '^wt: '`
Expected: FAIL in all three subtests: `routing state = {Enabled:true URL:http://localhost:4000 APIKey:sk-legacy} …, want none: modelman.toml was read` for the first two, and `Load = parse modelman.toml: …, want modelman.toml ignored` for the third. (`TestLitellmOwnEmptyTableIsATableNotAbsence` passes already.)

- [ ] **Step 3: Move `LitellmState`, delete the reader**

In `wt/internal/config/config.go`, directly under the banner comment `// ── LiteLLM routing state (wt-owned) ─────────────────────`, add:

```go
// LitellmState is the `[litellm]` table of wt's config.toml. It controls
// whether agents dial providers directly or route through the LiteLLM proxy.
// wt reads it from config.toml and nowhere else.
type LitellmState struct {
	Enabled bool   `toml:"enabled"`
	URL     string `toml:"url"`
	APIKey  string `toml:"api_key"`
}
```

Then:

```bash
git rm -q wt/internal/config/modelman.go wt/internal/config/modelman_fixture_test.go docs/contracts/modelman.sample.toml
```

In `wt/internal/config/registry.go`, delete `ModelmanPath` and its eight-line comment.

- [ ] **Step 4: Remove the fallback from `Load`**

In `wt/internal/config/config.go`:

In the `Config` struct, replace

```go
	LitellmTable    *LitellmState `toml:"litellm,omitempty"`
	migratedLitellm bool          `toml:"-"`
	litellm         LitellmState  `toml:"-"` // runtime copy of LitellmTable (or the legacy fallback)
```

with

```go
	LitellmTable *LitellmState `toml:"litellm,omitempty"`
	litellm      LitellmState  `toml:"-"` // runtime copy of LitellmTable
```

In `Load`, in the `if !exists {` branch, replace

```go
		legacy, err := loadModelmanState()
		if err != nil {
			return nil, err
		}
		return finalizeCfg(cfg, providers, models, legacy)
```

with

```go
		return finalizeCfg(cfg, providers, models), nil
```

and replace everything from `	legacy, err := loadModelmanState()` (the second one, after the schema-migration block) to the end of `Load` with:

```go
	// Join the registry LAST so wt never mutates registry data
	// in memory (schema fixups above only ever see wt-owned config.toml
	// content). Providers/Models from a pre-Phase-4 config.toml are
	// overwritten here — registry.toml is the source of truth.
	return finalizeCfg(fresh, providers, models), nil
}
```

Replace `finalizeCfg` and its comment with:

```go
// finalizeCfg joins registry providers/models into cfg, derives native-ness
// from provider auth types, and copies config.toml's [litellm] table, when it
// has one, into the runtime routing state. With no table, routing is off.
func finalizeCfg(cfg *Config, providers []Provider, models []Model) *Config {
	cfg.Providers, cfg.Models = providers, models
	deriveNative(cfg)
	if cfg.LitellmTable != nil {
		cfg.litellm = *cfg.LitellmTable
	}
	return cfg
}
```

Three comments: above `UpdateLitellm`, "wt owns this state (moved from modelman.toml 2026-09-21)." becomes "wt owns this state." if PR 2 has not already said so; above `SetLitellmForTest`, "(config.toml / legacy fallback)" becomes "(config.toml)"; and above `lockedApply`, "Load's version of PatchSave's re-read-fresh pattern for its two self-persisting migrations (schema fixups, legacy-litellm import): it takes" becomes "Load's version of PatchSave's re-read-fresh pattern for its self-persisting schema fixups: it takes" (the second caller, the legacy import, went with the code above; the rest of the comment stands).

- [ ] **Step 5: The tests that go with the reader**

1. `wt/internal/config/lock_test.go`: delete `TestLockedApplySkipsLegacyLitellmImportWhenConcurrentlySet` with its comment. It re-implements the closure Step 4 removed and would still pass, so nothing else will prompt its deletion.
2. `git mv wt/internal/config/modelman_test.go wt/internal/config/catalog_membership_test.go`, then in the new file: delete `writeModelmanState`, `writeLitellmState`, `TestLoadModelmanStateMissingFile`, `TestLoadModelmanStateHonorsXDG`, `TestLoadModelmanStateMalformedTOMLError`, `TestModelmanStateReadsNoPerModelState`, `TestModelmanPathHonorsXDG` and `TestModelmanPathExpandsTildeInXDG`. In the two tests that stay, `TestLoadModelExposureAcrossNativeLocalCloud` and `TestInCatalogPredicate`, replace each call ``writeModelmanState(t, dir, `…`)`` (the whole call, through its closing `` `) ``) by `t.Setenv("XDG_CONFIG_HOME", dir)`: the helper also set that variable, and the state it wrote is read by nothing. The import block becomes `"os"` and `"testing"`. Reword the two tests' comments so they do not say flags "in modelman.toml" gate anything: "no stored flag gates catalog membership".
3. `wt/cmd/wt/main_test.go`: delete the block that writes `modelman.toml` (the `// Mark the ollama model ready.` comment and the `os.WriteFile(filepath.Join(regDir, "modelman.toml"), …)` statement, lines 498–505 at `544cc1e`); reword the comment above `stubProbeInventory` (464–469) to "Running state comes from the live inventory, so the stub probe must report the model serving or the row is a start row, not a launch row"; line 488's "modelman-owned registry.toml" becomes "registry.toml".
4. `wt/scripts/agents-smoke.sh` line 171–172: delete "(modelman.toml's `[litellm]` is only a legacy fallback that wt copies in once)".

- [ ] **Step 6: Run the tests**

Run, from `wt/`: `test -z "$(gofmt -l .)" && go build ./... && go vet ./... && go test -count=1 ./internal/config ./cmd/wt ./internal/litellm 2>&1 | grep -v '^wt: '`
Expected: three `ok` lines. Then the whole suite: `go test -count=1 ./... && make check`.

- [ ] **Step 7: The docs and the changelog**

Every sentence that says `modelman.toml`'s `[litellm]` is a fallback or was "copied once" now says where the state lives and nothing about a fallback:

- `wt/CLAUDE.md` (line 168), `wt/docs/internals/config-and-registry.md` (37), `wt/docs/wt-config.md` (101), `wt/docs/wt-agents/README.md` (41), `docs/guides/04-litellm-config.md` (the "Routing state ownership" paragraph), `docs/guides/06-wt-agents-and-models.md` (106), `docs/guides/00-config-map.md` (the `config.toml` section's `[litellm]` bullet), root `CLAUDE.md` (the parenthesis Task 3 left in the `wt/` bullet): the `[litellm]` table lives in wt's `config.toml` (0600 when a key is stored) and is set with `wt litellm status|on|off|set`. Delete "copied once from modelman.toml's legacy table on first load" and every "legacy fallback" clause.
- `wt/docs/internals/config-and-registry.md` line 79: delete " and [../docs/contracts/modelman.sample.toml](../../../docs/contracts/modelman.sample.toml)"; the sentence names `registry.sample.toml` only (PR 3b already removed the clause about modelman's tests). This is a checked link: `bin/check-links` fails until it is gone.
- `wt/docs/wt-cloud-sync.md`: delete the bullet "It never writes `modelman.toml` and takes nothing for the sync from it: not `price_refresh_last_run`, and no per-model key. (Like every wt command, loading wt's config reads that file's legacy `[litellm]` table as a fallback.)" (four lines, 368–371 before PR 1). Nothing reads the file now, so there is nothing to say about it here.
- `wt/CHANGELOG.md`, under the newest unreleased heading:

```markdown
- **Changed:** wt no longer reads `~/.config/local-ai/modelman.toml`. Its
  `[litellm]` table was a fallback for routing state; wt has kept that state
  in its own `config.toml` since 2026-09-21 and copied it there on first
  load. On a machine that has never had a `config.toml`, routing now reads as
  off: run `wt litellm set --url <url> --api-key <key>` and `wt litellm on`.
  A malformed `modelman.toml` no longer stops wt.
```

- [ ] **Step 8: Commit**

```bash
git add -u wt docs CLAUDE.md
git status --short wt docs CLAUDE.md
git commit -m "refactor(wt)!: stop reading modelman.toml

The legacy [litellm] fallback and its one-time copy into config.toml go;
LitellmState moves into config.go. A broken modelman.toml no longer fails
every wt command."
```

### Task 12: llmbench stops reading `modelman.toml`

**Files:**
- Modify: `llmbench/src/llmbench/state.py`
- Test: `llmbench/tests/test_state.py`, `llmbench/tests/conftest.py`, `llmbench/tests/test_conftest_guards.py`
- Modify: `llmbench/CLAUDE.md`, `docs/guides/05-benchmarks.md`

**Interfaces:**
- Consumes: nothing.
- Produces: `load_state(path: Path | None = None) -> StateStore` reads `latest.toml` (or `path`) only. `_modelman_state_path` and `_modelman_pointers` no longer exist. `_pointers(table: dict[str, Any]) -> dict[str, str]` (was `table: Any`). `MODELMAN_STATE` is read by nothing.

**What changes for a user:** with no `~/.config/local-ai/benchmarks/latest.toml`, `--latest` used to find the run recorded in `modelman.toml`'s `[benchmarks]` table. Now it says there is no latest run. The results are still on disk; name the run directory, or copy the four keys (`last_run`, `last_run_dir`, `agent_last_run`, `eval_last_run`) from that table into `latest.toml` as top-level keys.

- [ ] **Step 1: Write the failing tests**

In `llmbench/tests/test_state.py`:

The module docstring becomes `"""The latest-run pointers: one flat file, and nothing read from anywhere else."""`. Replace the `paths` fixture with:

```python
@pytest.fixture
def paths(monkeypatch, tmp_path):
    """(latest.toml, modelman.toml) under tmp_path, neither written yet.
    HOME and XDG_CONFIG_HOME point at tmp_path too, so the second path is
    where modelman kept its state file by default, and MODELMAN_STATE, the
    override it honoured, names it as well."""
    latest = tmp_path / "benchmarks" / "latest.toml"
    legacy = tmp_path / "local-ai" / "modelman.toml"
    legacy.parent.mkdir(parents=True)
    monkeypatch.setenv("LLMBENCH_LATEST", str(latest))
    monkeypatch.setenv("XDG_CONFIG_HOME", str(tmp_path))
    monkeypatch.setenv("MODELMAN_STATE", str(legacy))
    return latest, legacy
```

In `test_latest_path_ignores_xdg_config_home`, delete the docstring's last line ("(The fallback read of modelman.toml does honour XDG: see below.)"). Replace `test_first_read_falls_back_to_modelmans_benchmarks_table`, `test_the_fallback_is_read_once` and `test_an_unusable_modelman_toml_reads_as_no_pointers` (with its `parametrize`) by:

```python
def test_modelman_toml_is_never_read(paths):
    """llmbench used to read the pointers from modelman.toml's [benchmarks]
    table while latest.toml did not exist. modelman is gone and the file is
    one the maintenance guide says to delete, so it must not matter whether
    it is there: with no latest.toml there is no latest run, wherever the old
    file sits and whatever MODELMAN_STATE names, and a read writes nothing."""
    latest, legacy = paths
    legacy.write_text(MODELMAN_TOML, encoding="utf-8")

    assert load_state() == StateStore()
    assert not latest.exists()
    assert legacy.read_text(encoding="utf-8") == MODELMAN_TOML


def test_a_recorded_run_is_all_that_latest_toml_holds(paths):
    """The first recorded run writes its own pointer and no other: nothing
    is carried over from modelman.toml, so a stale pointer there cannot come
    back as "the latest run"."""
    latest, legacy = paths
    legacy.write_text(MODELMAN_TOML, encoding="utf-8")

    store = load_state()
    store.extra.setdefault("benchmarks", {})["eval_last_run"] = "/results/eval-2"
    save_state(store)

    assert tomllib.loads(latest.read_text(encoding="utf-8")) == {"eval_last_run": "/results/eval-2"}
    assert load_state().extra["benchmarks"] == {"eval_last_run": "/results/eval-2"}
```

In `_assert_replaced`, delete the trailing comment `# present: no fallback either`. Replace `test_the_fallback_finds_modelman_toml_where_modelman_kept_it` and `test_an_explicit_path_never_falls_back` by:

```python
def test_an_explicit_path_reads_only_that_file(paths, tmp_path):
    """A caller that names the pointer file gets that file or nothing."""
    _, legacy = paths
    legacy.write_text(MODELMAN_TOML, encoding="utf-8")
    assert load_state(tmp_path / "elsewhere.toml") == StateStore()
```

`MODELMAN_TOML`, `test_latest_path_default_and_override`, `test_pointers_round_trip_through_a_flat_file`, `test_a_corrupt_latest_toml_reads_as_no_pointers_and_is_replaced` and `test_latest_toml_yields_only_string_pointers` stay.

- [ ] **Step 2: Run them to see them fail**

Run, from `llmbench/`: `uv run pytest tests/test_state.py -q --no-cov`
Expected: `test_modelman_toml_is_never_read` and `test_a_recorded_run_is_all_that_latest_toml_holds` FAIL (the store holds the four pointers from `modelman.toml`).

- [ ] **Step 3: Remove the fallback**

In `llmbench/src/llmbench/state.py`: delete `_modelman_state_path` and `_modelman_pointers` whole. Replace `load_state` with:

```python
def load_state(path: Path | None = None) -> StateStore:
    """The recorded pointers; an empty store when none are recorded."""
    pointers = _pointers(_read_toml(Path(path) if path else latest_path()))
    return StateStore(extra={"benchmarks": pointers} if pointers else {})
```

and the last paragraph of the module docstring with:

```python
`StateStore` keeps the pointers under `extra["benchmarks"]`, which is how the
three CLIs read and write them. latest.toml is the only file read: no pointer
comes from anywhere else.
```

`_pointers` loses a branch nothing reaches any more (Decision 33): its only caller now passes the dict `_read_toml` returns. Replace it with:

```python
def _pointers(table: dict[str, Any]) -> dict[str, str]:
    """The pointer keys of `table` that hold a string. Anything else in a
    file a person can edit by hand (another key, `last_run_dir = 7`) is not
    a pointer, and the CLIs pass these values straight to Path()."""
    return {k: table[k] for k in _POINTER_KEYS if isinstance(table.get(k), str)}
```

(`os` and `Any` stay imported: `latest_path` uses the first, `_pointers` and `_read_toml` the second.)

In `llmbench/tests/conftest.py`, in `_no_real_config`, delete the two lines

```python
    # ...and the one-time fallback read of modelman.toml's [benchmarks] table.
    monkeypatch.setenv("MODELMAN_STATE", str(tmp_path / "no-modelman.toml"))
```

In `llmbench/tests/test_conftest_guards.py`, in `test_default_config_paths_are_under_the_scratch_home`: the docstring becomes "Even with every override removed, the registry and the pointer file resolve under the scratch home." and the tuple becomes `("WT_REGISTRY", "MODELMAN_REGISTRY", "LLMBENCH_LATEST")`.

- [ ] **Step 4: Run the tests**

Run, from `llmbench/`: `make check && make test`
Expected: exit 0; seven fewer tests pass than before this task (`676 passed` when PR 3c has merged, which is the order that was run in scratch); `src/llmbench/state.py` at 100% in the coverage table; total 93.34% (floor 90%).

- [ ] **Step 5: Prose**

`llmbench/CLAUDE.md` line 52: "`state.py` holds the four pointers (`last_run`, `last_run_dir`, `agent_last_run`, `eval_last_run`) in `latest.toml`, and reads them from nowhere else." (keep the sentence about a corrupt file that follows). Line 78: "the registry, the pointer file and modelman.toml are redirected into `tmp_path`" becomes "the registry and the pointer file are redirected into `tmp_path`". `docs/guides/05-benchmarks.md` line 138: delete " — unless modelman recorded a run earlier: while `latest.toml` is absent, the pointers are read from the `[benchmarks]` table of `~/.config/local-ai/modelman.toml`, and the next recorded run copies them into `latest.toml`", so the sentence ends "…and `--latest` errors as shown above."

- [ ] **Step 6: Commit**

```bash
git add -u llmbench docs/guides/05-benchmarks.md
git status --short llmbench docs/guides
git commit -m "refactor(llmbench)!: the latest-run pointers come from latest.toml alone"
```

### Task 13: Guide 08 documents the manual removal; guide 00 stops describing files nothing reads

**Files:**
- Modify: `docs/guides/08-maintenance-and-troubleshooting.md`, `docs/guides/00-config-map.md`

**Interfaces:**
- Consumes: Tasks 11 and 12 (nothing reads the files any more); guide 08's `### 6. Where modelman's commands went` (Task 2).
- Produces: guide 08's `### 7. Files modelman left behind`.

- [ ] **Step 1: Guide 08, the removal section**

Add after `### 6. Where modelman's commands went`:

````markdown
### 7. Files modelman left behind

Nothing reads these any more, and wt never deletes them. Remove them by hand
when you are ready. **Two of them hold secrets:** do not `cat` them in a
shared terminal or paste them into an issue.

| File | Holds a secret? | What it was |
|---|---|---|
| `~/.config/local-ai/modelman.toml` | **Yes** — its `[litellm]` table has the LiteLLM API key | modelman's per-machine state |
| copies of it you or an old runbook made (`modelman.toml.bak-*`, any other `*.bak-*` beside it) | **Yes**, the same key | hand-made backups; modelman never wrote one, so check each name before deleting |
| `~/.config/local-ai/config.yaml` — modelman's legacy file, **not** LiteLLM's `~/.config/litellm/config.yaml` | Check: `grep -c -i 'api_key\|secret' ~/.config/local-ai/config.yaml` (a number above 0 means treat it as one) | provider types from before `registry.toml` |
| `~/.config/local-ai/settings.yaml` | No | the TUI's theme |
| `~/.config/local-ai/families/` | No | per-family variants and download markers from before `registry.toml` |
| `.modelman.toml.*.tmp` and `.registry.toml.*.tmp` beside them (the names start with a dot) | The first: **yes** | left by a modelman write that crashed; usually absent |

Not on this list, because they are still in use: `registry.toml`,
`registry.toml.lock` (wt's lock file), `benchmarks/` and
`benchmarks/latest.toml` in the same directory. A file named
`registry.toml.<random>.tmp`, with no leading dot, is a wt write in progress:
leave it.

**Where the files are.** The paths above are the defaults. If you ran
modelman with `XDG_CONFIG_HOME` set, `modelman.toml` (and `registry.toml`)
are under `$XDG_CONFIG_HOME/local-ai/` instead, while `settings.yaml`,
`config.yaml`, `families/` and `benchmarks/` stay under `~/.config/local-ai/`.
A `MODELMAN_STATE` export in your shell profile named yet another place for
`modelman.toml`: look there too, then delete the export.

Before deleting anything, three checks:

```bash
ls ~/.config/local-ai/registry.toml             # the model list wt uses
wt litellm status                               # routing state wt holds in its own config.toml
ls ~/.config/local-ai/benchmarks/latest.toml    # llmbench's latest-run pointers
```

- If there is no `registry.toml` (a machine that never ran `modelman
  migrate`), modelman's `config.yaml` and `families/` are the only record of
  your models, and nothing imports them any more. Read the model names out
  of them and add each with `wt model add`
  ([02-providers-and-models](02-providers-and-models.md) Step 1) before you
  delete the two.
- If `wt litellm status` shows routing off and you do route through LiteLLM,
  wt never had its own copy (a machine with no `config.toml`). Take the URL
  and key from the `[litellm]` table of `modelman.toml` and run
  `wt litellm set --url <url> --api-key <key>`, then `wt litellm on`. The key
  is on the command line, the only form wt offers, so it lands in your shell
  history: start the line with a space if your shell is set to skip such
  lines (`HISTCONTROL=ignorespace` in bash, `setopt HIST_IGNORE_SPACE` in
  zsh), or remove the line from the history file afterwards.
- If `latest.toml` does not exist and you want `--latest` to keep finding a
  run you made through modelman, copy the four keys of the `[benchmarks]`
  table (`last_run`, `last_run_dir`, `agent_last_run`, `eval_last_run`) into
  `latest.toml` as top-level keys. The results themselves are untouched
  either way.

Then:

```bash
ls -la ~/.config/local-ai/        # see what is there first
rm ~/.config/local-ai/modelman.toml ~/.config/local-ai/settings.yaml
rm -r ~/.config/local-ai/families
rm ~/.config/local-ai/config.yaml       # modelman's legacy file, after the check above
```

Delete each backup copy and each dot-prefixed `.tmp` file by its own name
once you have read the listing; do not use a wildcard in this directory,
which also holds `registry.toml`.

In each checkout of this repo, `git pull` removes modelman's tracked files
and leaves the untracked ones (`modelman/.venv`, caches). If
`git ls-files modelman | wc -l` prints `0`, remove the directory:
`rm -r modelman`.
````

Where each addition to the spec's "Left on disk" list comes from: the temp-file row and the wt temp name, Decision 30; the `XDG_CONFIG_HOME` and `MODELMAN_STATE` paragraph, `modelman/src/modelman/state.py` (`MODELMAN_STATE`, then `XDG_CONFIG_HOME`) against `settings.py`, `config.py` and `manifest.py`, whose defaults are literal `~/.config/local-ai/` paths; `registry.toml.lock`, the spec's Step 2 ("One write path"); the no-registry caveat, the spec's "Not ported: `modelman migrate`"; the shell-history note, `wt litellm set --help`, whose only inputs are `--url` and `--api-key`.

Add one line to the TL;DR's "Reading it fast" list, after the one Task 2 added: "- Cleaning up after modelman? §7 lists the files it left and what to check first."

- [ ] **Step 2: Guide 00**

Replace the four sections `### ~/.config/local-ai/modelman.toml`, `### ~/.config/local-ai/settings.yaml`, `### ~/.config/local-ai/config.yaml (legacy)` and `### ~/.config/local-ai/families/*.yaml (legacy)` (lines 58–148 at the docs branch) with one:

```markdown
### Left on disk, read by nothing

`~/.config/local-ai/modelman.toml`, `settings.yaml`, `config.yaml` and
`families/` were modelman's. modelman is retired and deleted; neither wt nor
llmbench opens any of them. `modelman.toml` holds an API key in its
`[litellm]` table. How to remove them, and what to check first:
[08-maintenance-and-troubleshooting](08-maintenance-and-troubleshooting.md),
"Files modelman left behind".
```

In the TL;DR table, replace the `modelman.toml` row by one row: `~/.config/local-ai/modelman.toml`, `settings.yaml`, `config.yaml`, `families/` | nobody | nothing | Left by modelman; safe to remove (guide 08). In `## Verification`, the `ls` line and its sample output lose `~/.config/local-ai/modelman.toml`. In the `config.toml` section, the `[litellm]` bullet was fixed in Task 11.

- [ ] **Step 3: Check this PR**

Before the commit of Step 4, from the monorepo root: `bin/check-links` (`ALL LINKS OK`), `make test-all`, `make lint`, and

```bash
git grep -n 'modelman.sample\|ModelmanPath\|loadModelmanState\|migratedLitellm\|MODELMAN_STATE' -- ':!docs/superpowers' ':!wt/docs/superpowers' ':!llmbench/tests/test_state.py' ':!docs/guides/08-maintenance-and-troubleshooting.md'
```

which must print nothing (before PR 5 it prints lines in `wt/internal/config`, `llmbench/src/llmbench/state.py`, two llmbench test files, `llmbench/CLAUDE.md`, guide 00 and `wt/docs/internals/config-and-registry.md`).

- [ ] **Step 4: Commit**

```bash
git add -u docs/guides
git status --short docs/guides
git commit -m "docs: guide 08 says how to remove the files modelman left; guide 00 stops describing them"
```

PR 5 is ready. **Ask the owner before pushing or opening the PR.**

- [ ] **Step 5: The closing proofs of the whole plan (controller; on `main`, after PRs 3c, 4 and 5 have all merged)**

These prove the end state of slices 1 to 5, so they are run once all of them are in, on an up-to-date `main`, whatever order 3c, 4 and 5 merged in. Run before the three are all in, the first prints `start_json.go:45` (PR 4's), the third 14 llmbench comment lines (PR 3c's) and `state.py`'s 11 (PR 5's), the sixth extra files. They do not depend on PR 6. From the monorepo root (bash):

```bash
grep -rn 'modelman\|Modelman' wt --include='*.go' | grep -v _test.go
grep -rho 'MODELMAN_[A-Z_]*' $(grep -rl MODELMAN_ wt --include='*.go' | grep -v _test.go) | sort | uniq -c
grep -rn 'modelman\|Modelman' llmbench/src
grep -rho 'MODELMAN_[A-Z_]*' llmbench/src | sort | uniq -c
git grep -n 'modelman.sample\|ModelmanPath\|loadModelmanState\|migratedLitellm\|MODELMAN_STATE' -- ':!docs/superpowers' ':!wt/docs/superpowers' ':!llmbench/tests/test_state.py' ':!docs/guides/08-maintenance-and-troubleshooting.md'
EX=(-- ':!modelman' ':!docs/superpowers' ':!wt/docs/superpowers' ':!docs/archive' ':!*.go' ':!llmbench/src' ':!llmbench/tests' ':!wt/CHANGELOG.md' ':!issues.md')
git grep -l 'modelman\|Modelman' "${EX[@]}"
git grep -n 'uv run.*modelman\|--directory modelman' "${EX[@]}"
bin/check-links
```

Expected:

1. Nothing (118 lines at the start of this plan).
2. Exactly the four permanent aliases and nothing else: `MODELMAN_LITELLM_CONFIG`, `MODELMAN_LITELLM_DATABASE_URL`, `MODELMAN_LITELLM_RESTART_CMD`, `MODELMAN_REGISTRY` (a bare `MODELMAN_` may remain where a comment names the family, as in "the `MODELMAN_LITELLM_*` names").
3. Nothing (35 lines at the start).
4. `MODELMAN_` 1 (the sentence in `_env.py` that names the family), `MODELMAN_AGENT_DEBUG` 1, `MODELMAN_BENCHMARK_WORKLOAD` 1, `MODELMAN_LITELLM_RESTART_CMD` 1, `MODELMAN_REGISTRY` 3 or fewer (Decision 8; the counts are those observed in scratch after the code edits of Tasks 8, 9 and 12; no `MODELMAN_STATE`).
5. Nothing.
6. Exactly these twelve files, each for a stated reason, and no other: `docs/guides/00-config-map.md` (the "Left on disk" section and its TL;DR row), `docs/guides/02-providers-and-models.md`, `docs/guides/04-litellm-config.md` and `docs/guides/05-benchmarks.md` (Going deeper only: the path of a design spec, which now reads `docs/superpowers/modelman/specs/…-modelman-….md`), `docs/guides/06-wt-agents-and-models.md` (the dated 2026-08-29 caveat), `docs/guides/07-usage-and-spend.md` (the section on what `modelman usage report` had, and a spec path), `docs/guides/08-maintenance-and-troubleshooting.md` (the mapping and the removal), `docs/reference/provider-artifacts.md` (what was removed, and where in git history), `litellm-session-logs/session-log-sources.md` (a dated count of sessions per project name), `llmbench/CLAUDE.md` (line 7: "carved out of modelman", and the link to the design spec), `wt/docs/wt-agents/codex-wt.md` and `wt/docs/wt-agents/litellm-troubleshooting.md` (dated notes). It printed 49 files before PR 1. `docs/contracts/`, every skill, and every `CLAUDE.md` other than llmbench's (root and `wt/` included) are absent. In guides 02, 04 and 05 confirm with `git grep -n 'modelman\|Modelman' -- docs/guides/02-providers-and-models.md docs/guides/04-litellm-config.md docs/guides/05-benchmarks.md` that every line printed is a `docs/superpowers/modelman/` path (five lines: three, one, one).
7. Exactly one line, in guide 08: the sentence saying `uv run --directory modelman modelman` fails. (31 before PR 1.)
8. `ALL LINKS OK`.

A proof that prints more than it should names the PR whose edit was missed: fix it in a follow-up PR against that slice, with the owner's OK.

---

## PR 6 — the test-file sweep (optional; question 3)

Branch: `git switch -c test/wt-registry-env-sweep main`, after PRs 3c, 4 and 5 have merged (it edits two llmbench test files that 3c and 5 also edit).

### Task 14: Tests name `WT_REGISTRY`, and stop describing modelman as a live tool

**Files:**
- Modify: the Go test files under `wt/` that `grep -rln 'MODELMAN_REGISTRY\|modelman\|Modelman' wt --include='*_test.go'` lists (the tables in Steps 1 and 2 name them by package); `llmbench/tests/conftest.py`, `llmbench/tests/test_conftest_guards.py` (Decision 29)

**Interfaces:**
- Consumes: `config.IsolateConfigHomeForTest()` (unsets both registry names for the process; called from the `TestMain` of `cmd/wt`, `internal/config`, `internal/tui`, `internal/modeladmin`, `internal/configeditor`, `internal/cloudsync`).
- Produces: no behaviour change; five test functions renamed.

No test is written first: this task changes how tests set up their environment, and the suite is the check. Work package by package, running that package's tests after each.

- [ ] **Step 1: The env move, by three rules**

For each line that `grep -rn 'MODELMAN_REGISTRY' wt --include='*_test.go'` prints (111 at `544cc1e`; fewer after PR 3b and PR 5):

1. **A set** (`t.Setenv("MODELMAN_REGISTRY", <a path>)`): becomes `t.Setenv("WT_REGISTRY", <the same path>)`. `WT_REGISTRY` outranks the alias, so the test is no weaker.
2. **A clear** (`t.Setenv("MODELMAN_REGISTRY", "")`) **in a package whose `TestMain` isolates the config home** (`cmd/wt`, `internal/config`, `internal/configeditor`, `internal/tui`, `internal/modeladmin`, `internal/cloudsync`): delete the line. `IsolateConfigHomeForTest` already unset both names for the whole process, and `t.Setenv` only restores what it found.
   **2a. The one exception: a clear that follows a set in the same test.** There the clear is not about an inherited value: it undoes the test's own set, and rule 1 has just moved that set to `WT_REGISTRY`. It becomes `t.Setenv("WT_REGISTRY", "")`, never a deleted line. There is exactly one such test: `TestModelDecodesFetchAndDraft` in `internal/config/model_artifact_test.go`, which sets the fixture path near line 20 and clears it at line 53 so that the registry `writeRegistry` just wrote is read. With the clear deleted the fixture stays selected and the test fails: `Fetch = {Repo: LocalPath: …}, want the local path to win over the repo`. To find any other after `main` has moved: `for f in $(grep -rl 'Setenv("WT_REGISTRY", [^"]' wt --include='*_test.go'); do grep -n 'Setenv("MODELMAN_REGISTRY", "")' "$f" /dev/null; done` after rule 1 and before rule 2, and read each hit's function.
3. **A clear in any other package** (`internal/litellm`, `internal/lifecycle`): keep it and make sure the line beside it clears `WT_REGISTRY` too. Both names must be cleared: the alias is read forever.

At `544cc1e` the 111 lines are 26 sets, 65 clears and 20 mentions in comments, test names and table rows, in these files (the number is the file's line count for `MODELMAN_REGISTRY`): `cmd/wt`: `launch_test.go` 15, `main_test.go` 5, `helpers_test.go` 3, `model_list_test.go` 2, `model_test.go` 1, `model_write_test.go` 1, `cloudsync_test.go` 1, `commands_config_test.go` 1, `modelman_wording_test.go` 1 (deleted in PR 3b). `internal/config`: `registry_test.go` 15, `config_test.go` 8, `registry_env_test.go` 7, `registry_fixture_test.go` 7, `modelman_test.go` 5 (renamed and trimmed in PR 5), `migrate_test.go` 4, `model_artifact_test.go` 4, `lock_test.go` 4, `location_hint_test.go` 3, `registry_write_test.go` 3, `registry_seed_test.go` 2, `registry_link_test.go` 2, `load_fix_hint_test.go` 2, `catalog_predicates_fixture_test.go` 1, `testmain_test.go` 1, `litellm_state_test.go` 1, `modelman_fixture_test.go` 1 (deleted in PR 5). `internal/configeditor`: `save_test.go` 4, `editor_test.go` 2, `models_tab_test.go` 1. `internal/litellm`: `service_test.go` 3. `internal/lifecycle`: `routes_test.go` 1. Work one package at a time and run `go test -count=1 ./<package>` after each.

**Do not convert** these, which are about the alias itself: `internal/config/registry_env_test.go` (the precedence table: "MODELMAN_REGISTRY beats XDG", "WT_REGISTRY beats MODELMAN_REGISTRY", and the loop over both names near line 78); the two shared helpers that clear both names on purpose, `withCleanConfigEnv` in `cmd/wt/helpers_test.go` and `litellmStateEnv` in `internal/config/litellm_state_test.go`. In `internal/config/registry_test.go`, `TestRegistryPathHonorsModelmanRegistryOverride` and `TestRegistryPathExpandsTildeInModelmanRegistryOverride` become tests of `WT_REGISTRY` (rename them `…HonorsTheRegistryOverride`, `…ExpandsTildeInTheRegistryOverride`); the alias keeps its coverage in the precedence table. The `registryRedirected` table near line 352 switches its case names and its `Setenv` to `WT_REGISTRY`. `internal/config/location_hint_test.go` near line 41 sets a tilde path with `HOME` unset and may assert on the message `cannot expand MODELMAN_REGISTRY`: read the assertion, and switch variable and expected name together. Where a comment beside a converted line names the variable the test no longer sets (`registry_test.go` lines 58–59, 71, 75, 251, 286 and 352, `location_hint_test.go:41`), reword it to name `WT_REGISTRY`; `internal/config/testmain_test.go:10` ("a test may set XDG_CONFIG_HOME or MODELMAN_REGISTRY and still win") names both names as a fact about the guard and may stay.

The seven `Setenv("MODELMAN_LITELLM_CONFIG", "")` clears stay: that alias is read too, and those tests need it empty.

- [ ] **Step 2: Comments and five names**

Reword the test comments that describe modelman as a reader, writer or caller. Three rules decide every line. **(R1) Provenance** ("the Go port of modelman's X", "as modelman's Y does", "mirrors modelman's Z"): state the rule as wt's own and drop the name and any Python file or function name. **(R2) Sharing** ("shared with modelman", "wt, llmbench and modelman", "modelman still reads / writes / owns"): if llmbench is a real sharer, name llmbench; otherwise say wt alone. **(R3) A cross-tool reason for a rule that stays** ("so modelman's next save does not move it", "because modelman still reads the file wt writes"): give the reason that survives (a stable layout, byte-identical no-op writes, llmbench reads the file), or none.

The lines are those `grep -rn 'modelman\|Modelman' wt --include='*_test.go'` prints: 283 in 61 files at `544cc1e`. By package, with each file's count (a file marked † is deleted or already swept by an earlier PR of this plan, so it has fewer lines, or none, by now):

| Package | Lines | Files |
|---|---|---|
| `cmd/wt` | 58 | `litellm_test.go` 12 †, `modelman_wording_test.go` 9 †, `load_error_hint_test.go` 6 (assertions: keep), `main_test.go` 6 †, `stats_usage_rows_test.go` 5, `start_json_test.go` 4 †, `helpers_test.go` 3, `stats_usage_test.go` 3, `commands_config_test.go` 2, `smoke_test.go` 2, `model_cmds_test.go` 2 (plus the test Task 5 added: keep), `resolve_test.go` 2, `stats_usage_table_test.go` 1, `loading_test.go` 1 † |
| `internal/config` | 142 | `modelman_test.go` 52 †, `registry_test.go` 12, `litellm_state_test.go` 12 †, `registry_doc_test.go` 11, `registry_fixture_test.go` 10 †, `modelman_fixture_test.go` 10 †, `config_test.go` 9, `registry_seed_test.go` 7, `registry_write_test.go` 6, `registry_env_test.go` 4 †, `registry_validate_test.go` 3, `migrate_test.go` 2, `lock_test.go` 2 †, `catalog_predicates_fixture_test.go` 1 †, `model_artifact_test.go` 1 |
| `internal/cloudsync` | 24 | `catalog_test.go` 10, `pricingpage_test.go` 7 †, `apply_test.go` 6, `openrouter_test.go` 1 |
| `internal/modeladmin` | 11 | `apply_test.go` 11 |
| `internal/lifecycle` | 10 | `mtplx_test.go` 7, `backends_test.go` 1, `lifecycle_test.go` 1, `omlxpool_test.go` 1 |
| `internal/litellm` | 10 | `dburl_test.go` 4, `discovered_ids_fixture_test.go` 3 †, `service_test.go` 2, `restart_test.go` 1 |
| `internal/tui` | 7 | `agent_model_test.go` 2, `model_list_test.go` 2, `agent_picker_test.go` 1, `pin_model_test.go` 1 (assertion: keep), `start_flow_test.go` 1 |
| `internal/tomlw` | 5 | `fixture_test.go` 2 †, `decode_test.go` 1, `encode_test.go` 1, `table_test.go` 1 |
| `internal/agents` | 3 | `price_notice_test.go` 3 |
| `internal/catalog` | 3 | `registry_fixture_test.go` 2 †, `catalog_test.go` 1 |
| `internal/configeditor` | 3 | `editor_test.go` 1, `models_form_test.go` 1, `models_pairing_test.go` 1 |
| `internal/localmodels` | 3 | `omlx_model_dirs_fixture_test.go` 2 †, `inventory_test.go` 1 |
| `internal/spend` | 2 | `spend_test.go` 2 |
| `internal/usage` | 2 | `usage_fixture_test.go` 2 † |

One package is one checkable unit: reword its files, then `grep -rn 'modelman\|Modelman' wt/<package> --include='*_test.go'` must print only lines Step 4 allows, and `go test -count=1 ./<package>` must pass. Rename:

| Old | New |
|---|---|
| `TestSeedWritesTheRowsModelmanWrites` | `TestSeedWritesTheDefaultRows` |
| `TestPatchModelPutsNewKeysWhereModelmanWould` | `TestPatchModelPutsNewKeysAtTheirSchemaPosition` |
| `TestAddWritesOneRowInModelmansLayout` | `TestAddWritesOneRowInSchemaOrder` |
| `TestRemovalDigestMatchesModelman` | `TestRemovalDigestIsAFixedFormat` |
| `TestCollectTablesReadsMarkupAsModelmanDid` | `TestCollectTablesReadsMarkupAsPinned` |

The vectors and cases inside them do not change. After the renames, `git grep -n 'Test[A-Za-z]*Modelman' -- wt ':!wt/docs/superpowers' ':!wt/CHANGELOG.md'` must print only `TestNoHelpTextNamesModelman` (Task 5) and tests whose subject is an alias or a "does not say modelman" assertion. Nothing outside the test files cites the five old names: PR 1 and PR 2 wrote `wt/CLAUDE.md` and the `internal/cloudsync` comments to name the test files instead (Decision 24).

**Keep** every assertion that a message does *not* contain `modelman`: `cmd/wt/load_error_hint_test.go` (lines 51, 68, 77, 88, 98), `internal/config/registry_test.go:135–136`, `cmd/wt/smoke_test.go:106`, `cmd/wt/resolve_test.go:256`, `internal/tui/pin_model_test.go:173`, and `TestNoHelpTextNamesModelman` (Task 5). They enforce success criterion 1.

- [ ] **Step 3: llmbench's conftest**

In `llmbench/tests/conftest.py`, `_no_real_config`: replace

```python
    # Both registry names: WT_REGISTRY outranks MODELMAN_REGISTRY, so one
    # inherited from the developer's shell would beat the scratch path below.
    monkeypatch.delenv("WT_REGISTRY", raising=False)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(tmp_path / "no-registry.toml"))
```

with

```python
    # WT_REGISTRY outranks everything, so naming the scratch path through it
    # beats whatever the developer's shell exports. The alias is cleared too:
    # a test that removes WT_REGISTRY must not fall through to an inherited one.
    monkeypatch.setenv("WT_REGISTRY", str(tmp_path / "no-registry.toml"))
    monkeypatch.delenv("MODELMAN_REGISTRY", raising=False)
```

In `llmbench/tests/test_conftest_guards.py`: `test_conftest_clears_an_inherited_wt_registry` and its fixture pinned the old shape (conftest removes an exported `WT_REGISTRY`). Replace the fixture's `exported.setenv("WT_REGISTRY", …)` by `exported.setenv("MODELMAN_REGISTRY", …)`, rename fixture and test to `…an_inherited_modelman_registry`, and the body becomes:

```python
    """conftest must not let a MODELMAN_REGISTRY the shell exported survive:
    a test that removes WT_REGISTRY would fall through to it and read the
    developer's real registry."""
    assert "MODELMAN_REGISTRY" not in os.environ
    assert registry.registry_path() == Path(os.environ["WT_REGISTRY"])
```

Then check every test under `llmbench/tests` that did `monkeypatch.delenv("MODELMAN_REGISTRY", …)` expecting "no override": `grep -rn 'MODELMAN_REGISTRY\|WT_REGISTRY' llmbench/tests`. A test that needs no override at all must now remove `WT_REGISTRY` (tests in `test_registry.py` already remove both).

- [ ] **Step 4: The proof**

From the monorepo root:

```bash
grep -rln MODELMAN_REGISTRY wt --include='*_test.go'
grep -rn 'modelman\|Modelman' wt --include='*_test.go' | grep -v '"modelman\|modelman"\|Modelman(' 
```

Expected: the first prints at most these files (31 before). Files that use the alias on purpose: `wt/internal/config/registry_env_test.go` (the precedence table), `wt/cmd/wt/helpers_test.go` and `wt/internal/config/litellm_state_test.go` (the helpers that clear both names), `wt/internal/litellm/service_test.go` and `wt/internal/lifecycle/routes_test.go` (rule 3). Files that only name it in a comment: `wt/cmd/wt/model_cmds_test.go` (the comment of `TestNoHelpTextNamesModelman`, which says the alias may appear in help), `wt/internal/config/testmain_test.go`, and `wt/internal/config/registry_test.go` if a comment there still states the full precedence chain. `git grep -n MODELMAN_REGISTRY -- <file>` on any other file shows a line a rule above missed. The second prints only comment lines that explain a "does not contain modelman" assertion; read each and confirm it describes an assertion, not a live tool (283 lines before).

From `wt/`: `test -z "$(gofmt -l .)" && go vet ./... && go test -count=1 ./... && GOMAXPROCS=1 go test -count=1 ./... && make check`. From `llmbench/`: `make check && make test`. From the root: `make test-all`.

- [ ] **Step 5: Commit**

```bash
git add -u wt llmbench/tests
git status --short wt llmbench/tests
git commit -m "test: tests redirect the registry through WT_REGISTRY and stop describing modelman as a live tool"
```

PR 6 is ready. **Ask the owner before pushing or opening the PR.**

---

### Task 15 (controller only; every row needs the owner's OK first): the issues

No implementer subagent runs any of this. Before each row, re-read the issue (`gh issue view <n>`): the inventory's reading of them was taken on 2026-10-08 and they may have moved. A row whose issue does not say what the row assumes is skipped and reported to the owner, not adapted on the spot.

| When | Issue | Action | Comment to post |
|---|---|---|---|
| Before PR 1 | PR #165 | Nothing: the owner said to leave it as it is (2026-10-09). Do not merge, close, rebase or comment on it. | none from this plan |
| Before PR 3b | branch protection of `main` | If `modelman-ci` is a required status check, remove it from the list (Decision 34). | none |
| Before PR 1 | #194, #258, #259, #266 | Verify they are closed (`gh issue view <n> --json state`). The spec closes #258 and #259 by their Step 0 fixes and #194 and #266 as obsolete; the inventory found none of them open. If #194 is open, confirm #299 is its refiled live item before closing it. | For #194 if still open: "Closed as obsolete: modelman is deleted. Its one live item, mlx_lm_server pairing identity, is #299." |
| PR 3b merged | #267 | Close as obsolete. | "Closed as obsolete: modelman, the only caller that matched `wt stop`'s error text, is deleted in <PR 3b>, and the wording-pin test with it. `wt stop` still has no machine-readable result; reopen or refile if a script needs one." |
| PR 3b merged | #268 | Close as obsolete. | "Closed as obsolete: `modelman/src/modelman/wt_bridge.py` is deleted in <PR 3b>. llmbench's bridge has one wt call (`wt warm`) and one wrapper." |
| PR 3b merged | #299 | Leave open; comment. | "modelman is deleted (<PR 3b>), so the half of this about `modelman/src/modelman/local_control.py` is gone. What remains is wt's `source.isRunning` and llmbench's pidproc, as described above." |
| PR 3b merged | #320 | **Only if `gh issue view 320` shows an open issue that cites `modelman/src/modelman/ollama_catalog_cli.py`.** This number is in the inventory's notes and nowhere in the repo (the newest number in git history is PR #319), and it could not be read when the plan was reviewed. If it is that issue: leave open; comment. Otherwise drop this row. | "The file this cites, `modelman/src/modelman/ollama_catalog_cli.py`, is deleted (<PR 3b>). The message and the digest format are wt's alone now, so they can be changed without keeping them equal to modelman's." |
| PR 4 merged | #260 | Read it first. Close as obsolete if everything it describes is the `--json`/`--plan` path (Decision 22: not at PR 3b, the code path lives until PR 4). If it also describes a failed **interactive** `wt start`, which PR 4 does not touch, leave it open and comment with what remains. | "Closed as obsolete: `wt start --json` and `--plan` are removed in <PR 4>; modelman was their only caller." |
| — | #275, #307, #308, #311, #317, #318 | Leave open, untouched: all are wt-only and unaffected. #307 and #308 are defects in `wt stop` / `wt stop --all`, the commands guide 08's table names for `modelman stop --all`; #317 and #318 are the test-environment hazards named in Global Constraints. | none |

---

## Spec Coverage

Checked against the spec's Step 6 section with the finished plan in hand.

| Spec requirement | Where |
|---|---|
| No `modelman` shim | Global Constraints; guide 08's section says the command fails by design (Task 2) |
| Mapping table in guide 08, every row | Task 2, Step 4 (the spec's ten rows, plus Decisions 3, 4 and 27) |
| Slice 1: guides 00 to 11, root README and `CLAUDE.md`, `litellm-session-logs/CLAUDE.md` | Tasks 1–3 (guides 09 and 11 need no edit) |
| Slice 1: `adding-a-provider` rewritten for wt and moved; `adding-a-tui-screen` deleted; `mlx-lm-quantization` updated (hand-edit `local_path`, or place the output in omlx's model directory and `wt model add` it by name) | Task 4; the second way is put first because it is the one `wt start` can load (Task 2, Step 5) |
| Slice 2: comments and stragglers; a one-time grep in non-test Go | Task 5 (its grep, with the 26 lines later PRs own); the grep that prints nothing is Task 13, Step 5 |
| Slice 3: `git rm -r modelman/`, `git mv` of the plans and specs | Task 7, Step 1 (the move first) |
| Slice 3: remove `modelman-ci`; `Makefile` `install` and `test-all` | Task 7, Steps 1–2 |
| Slice 3: llmbench drops its registry-path parity | Task 8 |
| Slice 3: the retired llamacpp backend entry and `LLM_ISOLATE_LLAMACPP_MODEL` | Task 9 |
| Slice 3: delete the wording-pin test | Task 7, Steps 1 and 3 |
| Slice 4: `wt start --json` and `--plan`, `start_json.go`, its test, the fixture, the two doc sections | Task 10 |
| Slice 5: `internal/config/modelman.go`, the legacy `[litellm]` fallback, `modelman.sample.toml` | Task 11 |
| Slice 6: Go tests move to `WT_REGISTRY`, one alias-precedence test left | Task 14 |
| What stays: `wt warm`, `wt served` | Untouched; docs reworded (Task 3), help pinned (Task 5) |
| What stays: the four env aliases, each after its `WT_` name | Untouched; proved by the second and fourth greps of Task 13, Step 5 |
| What stays: `registry.sample.toml`, `registry.written.sample.toml` cross-language; the others Go-only with headers corrected | Task 7, Step 4 |
| Left on disk; guide 08 documents the manual removal | Task 13 |
| Issues | Task 15 |
| Criterion 5: `make test-all` at the end of every step | Every PR's last verification step |
| Testing: scratch-registry safety, `bin/check-config-dirs-untouched` in `wt-ci` and `llmbench-ci` | Both workflows are untouched; only the script's comments change (Task 7) |
| Risk: lost Linux coverage | Decision 26 |

What the spec does not ask for and this plan adds, each in the decisions table: the two ported llmbench tests (PR 3a); llmbench's own read of `modelman.toml` removed (Task 12); the dead llmbench wrappers and `respects_solo` removed (Task 9); `lifecycle.Evictions` moved into a test file (Task 10); `docs/contracts/rotation.sample.state` deleted (Task 7); the status tag dropped from `llmbench provider list` (Task 9, Decision 32); guide 08's additions to the mapping table and to the "Left on disk" list (Decisions 27 and 30); llmbench's conftest moved to `WT_REGISTRY` in PR 6 (Decision 29); the prose outside slice 1's list (`wt/CLAUDE.md`, `llmbench/CLAUDE.md`, `wt/docs`, the reference docs, the `adding-a-benchmark-backend` skill, `bin/` comments).

Names are consistent across tasks: `litellmStateEnv(t, wtToml)` and `writeUnder(t, home, rel, body)` are defined in Task 11 and used by Task 14 unchanged; `finalizeCfg` has three parameters and one result from Task 11 on; `sessionCounts` keeps its name and type when it moves (Task 10); `SUPPORTED_PROVIDER_IDS` keeps its name (Task 9) and the literal in `test_isolation.py` still equals it; guide 08's sections are `### 6.` (Task 2) and `### 7.` (Task 13), and guide 00 cites the second by its title; `TestNoHelpTextNamesModelman` is defined in Task 5 and kept by Task 14; the five test renames are Task 14's alone, and no text written by Tasks 1 to 13 names any of the five; guide 02's old Step 7 is "Step 6" in every task after Task 1; the closing proofs are Task 13, Step 5.

## Inventory Items Not Placed

Things the four sweeps found that this plan deliberately leaves alone, or could not settle from the tree:

- **`RegistryDoc.SetTimePrices`** (Step 2) has no production caller; Step 4's plan said Step 6 would remove it if nothing came to use it. It is not in the inventory's Step 6 sweep and is not removed here. Check with `grep -rn 'SetTimePrices' wt --include='*.go' | grep -v _test.go` and file it or fold it into PR 2 at the owner's word.
- **`modelman.toml.bak-*` has no pattern in code.** No tool wrote such a file; the copies come from manual steps in two older plans. Guide 08 therefore tells the reader to check each name.
- **`<tempdir>/ollama-pricing-<stamp>.html`**: modelman wrote it on a page-shape change, and `wt cloud-sync` writes the same name. Not listed for removal: it is in a temp directory and wt still produces it.
- **Model weights modelman downloaded** stay where the providers keep them; wt lists them from the live probe. Nothing to do.
- **`wt-ci` has no Linux job**, and after PR 3b seven fixtures are exercised on macOS only. Accepted by the spec's Risks; no task.
- **shell-ci's path filter** has no `README.md`, `CLAUDE.md`, `wt/**` or `llmbench/**`, so `bin/check-links` does not run in CI for a PR that touches only those (PR 1's `CLAUDE.md` and `wt/docs` edits). Every task runs it locally; widening the filter is not Step 6's.
- **`llmbench-ci` does not trigger on `wt/**`** although llmbench shells out to `wt warm`. Unchanged.
- **wt cannot start an omlx `local_path` entry whose directory is outside omlx's model directory.** Settled from the code (Task 2, Step 5); guide 10 and the `mlx-lm-quantization` skill say so and put the route that works first. No task gives wt that ability: the spec keeps `local_path` a hand edit that wt "preserves and shows".
- **wt prints two `llmbench provider …` commands that cannot be pasted** (Decision 28). Guide 08 says how to run them. Worth an issue against wt at the owner's word.
- **Whether omlx itself follows a symlink in its model directory** was not checked. wt's scan does (`scanModelDirs`), and its comment says omlx 0.7.0's discovery does; the guides say "move", not "link".
- **The state of PR #165 and of every issue** was read on 2026-10-08 through `gh`, not re-read while writing this plan (no network). Task 15 re-reads each before acting. #320 in particular could not be confirmed to exist; its row is conditional. Whether `modelman-ci` is a required status check was not readable either (Decision 34).
