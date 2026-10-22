# Modelman Retirement, Step 3 (Model CLI and the Models Tab) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give wt the commands and the screen that replace modelman's model TUI — `wt model list|add|edit|rm`, a Models tab in `wt config` that bare `wt model` opens, and `wt stop --all` — on top of the registry writer Step 2 shipped.

**Architecture:** A new package with no UI import, `wt/internal/modeladmin`, holds the rows both listings show, the validation and id rules, and the three registry writes, each one `config.UpdateRegistry`. The `wt model` verbs in `cmd/wt` call it and then run one route sync through the existing `syncRoutesAfterWrite` seam; the Models tab lives in `internal/configeditor` beside the Agents tab, calls the same core from `tea.Cmd`s, writes each change at once, and leaves one route sync to `cmd/wt` when the editor exits. The sizing helpers the launcher's pickers use (`fitTo`, `listFrame`, the column-dropping table) move to `wt/internal/tuilayout`, which both `tui` and `configeditor` import.

**Tech Stack:** Go 1.26.7 (module root `wt/`), cobra v1.10.2, Bubble Tea v1.3.10, bubbles v1.0.0 (`list`, `textinput`), lipgloss v1.1.0, BurntSushi/toml v1.6.0 (through `internal/tomlw`). Python 3 with uv for the one modelman change. No new dependency in either.

**Spec:** [docs/superpowers/specs/2026-10-06-modelman-retirement-design.md](../specs/2026-10-06-modelman-retirement-design.md), section "Step 3 — model CLI and the Models tab", read with its Decisions, End state, Step 2's "Interim: two writers", Testing, and Step 6's mapping table. That section is the authority: where this plan and the spec differ, the spec wins and the plan is the bug. This plan covers Step 3 only. Steps 0, 1, 2 and 5 have merged; Step 4 (`wt cloud-sync`) and Step 6 (the retirement) get their own plans.

## Global Constraints

- `wt/internal/modeladmin` imports no UI package (no Bubble Tea, bubbles, lipgloss or cobra). The CLI and the tab both go through it, so they cannot drift.
- Rows are every registry model plus every discovered inventory entry, from one live probe. Status is `ok`, `missing`, `unknown` (provider unreachable), `new` (discovered, unregistered) or `-` (an mlx_lm_server pairing). Running is `run`, `load`, blank or `?`.
- `localmodels.Entry` gains `Path` and `Size`, filled only from data the probe already touches: the omlx and mtplx directory, and ollama's size from `/api/tags`. A row with `fetch.local_path` takes its path from that key and its presence from a stat.
- A local add takes the artifact name exactly as the provider lists it, and its id is `config.DiscoveredModelID(provider, name)`. A cloud id keeps modelman's rule (`/` in the name becomes `--`). `--id` overrides, with an id of the shape wt takes for granted everywhere else: `<provider>/<name>`, no spaces.
- Immutable after creation: id, provider, model name. Editable: family, tags, location, the three per-token prices, subscription price and period.
- Adding an ollama model runs a best-effort `ollama show`, pinned to the provider row's address, and records `model_info.supports_function_calling` and `supports_vision`. A failed lookup adds the model without them and says so.
- Each writing verb does one `UpdateRegistry` and then one route sync. Exit 0 when the write succeeds, even if the sync only warned; exit 1 on a validation error, an unknown or duplicate id, an unreadable registry, or a declined removal.
- `apply` functions handed to `config.UpdateRegistry` are pure: they may run up to three times, so they do not print, run a command or call a server, and a result handed back through a captured variable is assigned afresh on each run. The `ollama show` lookup therefore happens before the write.
- Nothing writes a `config.Model` into the registry. Every write is `RegistryDoc.AddModel`, `PatchModel`, `RemoveModel` or `SeedRegistryDefaults`.
- `wt model add` calls `config.SeedRegistryDefaults` in the same locked write. One seeding rule changes in this step: openrouter's default row is seeded when a model references it, as the local providers' rows are, so that `wt model add openrouter …` works on a registry with no openrouter row (the spec's CLI table: add "seeds a missing default provider row in the same write"). Step 2 seeded it only when an agent lists it and pinned that in `TestSeedAddsOnlyWhatTheMachineNeeds`; turning that subtest round needs the owner's OK **before PR C starts** (see "Before PR C" under Branches and PRs).
- Removal is registry only. wt never deletes weights; it prints the path.
- No Hugging Face download. `local_path` entries and `[[families]]` stay a hand edit of `registry.toml`; wt preserves the keys and shows the path.
- `wt model edit` does not stamp `pricing_updated_at` (the spec's Step 4 owns that date).
- The Models tab: keys `enter` (edit; register on a `new` row), `n` add, `d` remove, `r` re-probe, `/` filter, `tab` switch tab. No start/stop key in this step. Each saved form and each confirmed removal writes the registry at once. The Agents tab keeps its buffer and `ctrl+s`; its quit prompt concerns agent edits only. `ctrl+s` does nothing on the Models tab. `esc` on either tab's list never ends the editor: only `q` and `ctrl+c` do, by way of the quit prompt.
- The tab does not sync routes per change. It marks routes pending on the status line and one sync runs when the editor exits, only if the registry changed. A quit asked for while a registry write is still in its command waits for that write's result, so the editor never reports "nothing changed" for a registry that did. The CLI verbs sync immediately. A sync that leaves `config.yaml` byte-identical does not restart the proxy (verified by a test in this step).
- Probing, and every registry write the tab makes, runs in a `tea.Cmd`, never on the update loop. The update goroutine never waits.
- The form is hand-built on `bubbles/textinput` like the agent form. Fields: provider (choice; mlx_lm_server excluded), model name, family (suggestions from existing families; the accept key fires only with the cursor at the end of the text), tags, location, three per-token prices, subscription price and period. `ctrl+s` saves, `esc` cancels.
- Every screen fits 40, 80 and 120 columns by 12, 24 and 50 rows: the view is never taller or wider than the terminal. The fit tests assert that the header row and the focused form field are on screen, that STATUS and RUNNING are on every row from 80 columns up whatever the longest id, and that the selected row's whole id and its status are on screen at every size. Nothing is cut without a sign of it: an id too long for its column loses its middle to an ellipsis, and text that does not fit a line wraps or has a shorter form. Real screens are captured through a pty at 80x24 and 40x12 before the tab PR and the form PR hand off.
- The text listing is borderless and width-aware, tested at 80 columns; size and path that do not fit are left to `--json`. It reuses `wt stats`' renderer, generalised — not a third table renderer.
- mlx_lm_server pairings are created with `wt model add mlx_lm_server <target> --draft <draft>`, shown as rows, and their metadata is editable in the tab. wt cannot start one. `catalog.MissingReason` and `BlockReason` name `llmbench provider isolate --solo mlx_lm_server <target> --draft <draft>`.
- modelman is frozen to bug fixes. Its one source change in this step is the text bare `modelman` prints, which names `wt model` and the Models tab, with its test.
- Scratch-registry safety: every wt command that writes the registry has a test under a redirected registry with and without `WT_LITELLM_CONFIG`.
- Tests never touch the developer's machine: no real registry, no real `config.yaml`, no live provider, no `ollama`, nothing under `~/.config`, and no decision taken from the developer's `PATH`. A package whose tests reach `config.UpdateRegistry` has a `TestMain` that calls `config.IsolateConfigHomeForTest`, and one test asserting `config.RegistryWriteGuardArmed()`.
- Any run of a built `wt` while executing this plan uses a throwaway `HOME` and `XDG_CONFIG_HOME`, `WT_LITELLM_CONFIG` pointing at a scratch file, and `WT_LITELLM_RESTART_CMD=true`. It gets them from `env -i`, which empties the environment and sets exactly those — inline in Task 5, and through a wrapper script, `run.sh`, that Tasks 9, 13, 16 and 18 each write beside their scratch home — never from variables exported in a shell: a step must work when it is run in a fresh shell, and must not be able to reach the real `HOME` when it is. Never start, stop or pull a model on a live provider, and never point a capture at a real provider's port.
- Never print, copy or commit a real `registry.toml`, `config.toml` or LiteLLM `config.yaml`: they can hold API keys.
- Run every Go command from `wt/`, never the monorepo root.
- Every `Test*` function has a top-level `//` comment saying what it tests and why a regression matters to a user.
- Before each Go commit: `test -z "$(gofmt -l .)"` (it fails when `gofmt -l` prints anything; `gofmt -l` alone exits 0) and `go vet ./...`.
- Python changes pass `make check` in `modelman/` (ruff, ruff format, mypy; lines of at most 100 characters).
- `make test-all` (monorepo root) passes at the end of every PR, with modelman still working. `make check-links` passes whenever a Markdown file changed.
- Guides never embed live model state: no counts or inventories. Show the command that reveals the state.
- Commit messages follow the repo's conventional style (`feat(wt): …`, `refactor(wt): …`, `docs: …`), with the attribution trailer your session is told to add, if any.
- Work happens on a feature branch off `main`, one branch per PR. **Pushing a branch and opening a PR need the owner's OK.** Do not push, and do not run `gh pr create`, until the owner says so.
- Read `wt/CLAUDE.md`, and `wt/docs/internals/tui.md`, `config-and-registry.md`, `local-models.md` and `testing.md`, before starting.

## Review Focus

The inputs and conditions the spec implies but does not spell out, most likely first, each with the test that pins it.

1. **A model id wider than the terminal.** A 48-column and a 72-column id, at 80 columns and at 40. Expected, for the text listing: it never cuts an id — it moves the id to a line of its own, above its cells — and drops PATH, then SIZE, to make room. That listing is fitted at 80 columns and up, which is what the spec asks for; narrower than that a long family name can carry a line past the edge, because MODEL, FAMILY, LOC, STATUS and RUNNING are never dropped (32 columns plus the family's width). Expected, for the tab: the view is inside the terminal at every size; STATUS and RUNNING stay on every row; an id that does not fit beside them is cut in its middle, and the detail block under the table shows the selected row's id whole. Pinned in Task 5 (`TestModelListFitsEightyColumns`, `TestModelListDropsSizeBeforeItCutsAnId`) and Task 11 (`TestModelsTabFitsTheTerminal`, `TestModelsTabKeepsStatusAndRunning`).
2. **A registry that is already damaged**: a model whose provider has no row, or a mistyped location, which makes every other wt command report a config error. Expected: `wt model list` still lists, an edit of another row succeeds, the bad row can be removed, and only an edit that leaves a touched row bad is refused. Pinned in Task 6 (`TestUpdateRegistryValidatesAModelAgainstItsProviderRow`, "an untouched model with a gap does not block the write"), Task 7 (`TestARowWithAGapCanBeRepairedOrRemoved`), Task 8 (`TestModelRmStillWorksWhenTheConfigDoesNotLoad`) and Task 11 (`TestModelsTabShowsARegistryThatDoesNotLoad`).
3. **An edit that changes nothing, or one field.** Expected: saving an untouched form writes no byte; an edit of one field leaves `time_prices`, `model_info`, `fetch`, unknown keys and integer prices exactly as they were. Pinned in Task 7 (`TestEditPatchesOnlyWhatWasGiven`) and Task 14 (`TestModelFormEditSendsOnlyWhatChanged`).
4. **A redirected registry**, with and without `WT_LITELLM_CONFIG`. Expected: add, edit and rm each write the scratch registry; unnamed, the default `config.yaml` is not touched and the user is told why; named, that file is synced. Exit 0 either way. Pinned in Task 8 (`TestModelWritesUnderARedirectedRegistry`).
5. **`ollama show` cannot run** (ollama not installed, the daemon down, the model not pulled yet). Expected: the model is added without `model_info`, and a warning says what is missing and why. Pinned in Task 7 (`TestOllamaCapabilitiesReportsAFailedLookup`), Task 8 (`TestModelAddAsksOllamaWhatTheModelCanDo`) and Task 14 (`TestModelFormRefusalStaysOnTheFieldAtFault`, its last part).
6. **The registry changes under the open tab, or the user is faster than a write** (another program removes a row while the remove prompt is up; a slow probe lands after a newer one; `d`, `y`, `q` typed in one breath, or `ctrl+c` while a form's save is still running `ollama show`). Expected: the refusal is on the status line and the routes are not marked pending; the stale rows are dropped; a quit waits for the write in flight, so the exit sync runs for it — and is called off when the write is refused, because the refusal has to be read. Pinned in Task 11 (`TestModelsTabRemoveRefusals`, `TestModelsTabDropsAStaleProbe`, `TestQuitWaitsForARegistryWriteInFlight`) and Task 14 (`TestQuitWaitsForAFormSaveInFlight`).
7. **The same thing asked twice, and two things that only look the same**: an id that exists, an artifact that is already registered under another id, `wt model rm a a`, the same pairing again; and a second pairing whose target and draft end in the same names as a registered one's (a locally quantized `/quant/Big-4bit` beside `mlx-community/Big-4bit`). Expected: the first four are refused (or, for the repeated id on `rm`, removed once), with nothing half-written; the last is a different pairing, refused only for the id its names make, with `--id` named as the way in. Pinned in Task 7 (`TestAddRefusals`, `TestRemoveIsAllOrNothing`), Task 8 (`TestModelCommandsThroughTheCommandLine`) and Task 18 (`TestAddAPairing`).
8. **Keys that mean something else where they are typed**: `q`, `d`, `n`, `r` in the filter and in the form, right-arrow in the middle of the Family field, `esc` on a list (the list's own quit key, and the cancel key of the two screens the list opens), `QW` completed to a family spelled `qwen3.8`. Expected: text or cursor movement, never quit, remove or an accepted suggestion; `esc` never ends the editor; an accepted suggestion replaces what was typed. And the filter really filters: the rows `d` and `enter` act on are the rows on screen. Pinned in Task 11 (`TestModelsTabFilterTakesTheTabsKeys`, `TestModelsTabFilterNarrowsTheTable`, `TestAgentsFilterNarrowsTheList`, `TestEscOnAListDoesNotQuit`) and Task 14 (`TestModelFormKeysAreTextNotCommands`, `TestModelFormFamilySuggestions`, `TestModelFormPrefilledFamilyDoesNotPanic`).
9. **`wt stop --all` while another wt session is using a model, or when one stop fails.** Expected: one question with the total; declining stops nothing; a failed stop does not leave the rest running, and the exit code is 1. Pinned in Task 1 (`TestStopAllInUseConfirms`, `TestStopAllKeepsGoingAfterAFailure`).

## Decisions This Plan Makes

The spec states the behaviour; these are the choices it leaves open. Each is pinned by a test named below, so a reviewer who disagrees changes one place. The exceptions say so in their last column.

| Question | Decision | Why | Pinned by |
|---|---|---|---|
| What is the shared layout package called, and what does `internal/tui` keep? | `wt/internal/tuilayout`: `ListFrame`, `Clip`, `ListExtent`, `FitList`, `FrameSides`, `FitTo`, `DrawnFrame`, `SizeList`, and `Columns`. `internal/tui` keeps its own frames and one-line forwarders under the old lowercase names (`listFrame`, `fitTo`, `clip`, …). | `configeditor` already imports `tui`, so the helpers could not stay there without the spec's "a package both import". The forwarders let every existing tui test pass unedited, which is the refactor's proof. | Task 10: the whole existing `internal/tui` suite, unedited, including `TestEveryListPhaseFitsTheTerminal` and `TestModelTableUnchangedWhenItFits` |
| How is `tableColumns` generalised? | `tuilayout.Columns`: `Heads`, `Widths` as slices, `Tail`, `Prefix` (the columns a row prints before its cells), and `DropOrder`, the table's own list of columns to give up. A list item joins a table through `TableItem.TableColumns()`, which is how `FitTo` finds it. | The picker and the Models tab protect different columns. An interface on the item keeps `tuilayout` ignorant of both tables. | Task 10, `TestColumnsDropInTheTablesOwnOrder`, `TestColumnsLineMatchesTheHeader`, `TestFitToFitsATableInsideItsFrame` |
| Where are the tab's fit tests? | In `internal/configeditor`: `TestModelsTabFitsTheTerminal` and `TestModelFormFitsTheTerminal`, at the same nine sizes as `TestEveryListPhaseFitsTheTerminal`. | That test is in `internal/tui` and builds `tui`'s unexported model; `configeditor`'s model is unexported too. | Tasks 11 and 14 |
| Which columns does the tab show, and which does it drop? | FAMILY, MODEL, LOC, STATUS, RUNNING, SIZE. Dropped, in order: SIZE, LOC, FAMILY. | The picker's order, so the two tables read alike. MODEL, STATUS and RUNNING say what a row is. | Task 11, `TestModelsTabKeepsStatusAndRunning` |
| What is under the table? | A detail block for the selected row: its id, whole, then its status, `running`/`loading`/`running?`, its tags (a pairing's target and draft), wrapped between its parts; and its path on a line of its own, written from `~`. Under it, the key hints. | The table cuts a long id and has no column for the rest; the path is what a user copies, so it is never broken at a hyphen and never mixed into a sentence. | Task 11, `TestModelsTabFitsTheTerminal` |
| What does a short terminal give up, and in what order? | The blank lines round the status, then the path, both as soon as they would leave the table fewer than three rows; then table rows down to one; then the key hints; last the id line. The tab bar, the status and the table are never given up. | A table of one row cannot be navigated; the id is what says which model the cursor is on. | Task 11, `TestModelsTabFitsTheTerminal` ("one key after a removal": three rows and the hints at 40x12) |
| What does the tab do with an id too wide for its column? | Columns are dropped first, with MODEL as wide as the longest id. When MODEL, STATUS and RUNNING alone are still wider than the list, MODEL is cut to what is left (never under 12 columns) and a longer id loses its middle to an ellipsis: `omlx/Qwen3.8-35B-A3B-Instruct-abliterat…-dynamic-quant-6bit`. The detail block shows the selected id whole. The picker's table (`internal/tui`) is not changed: it still never abbreviates. | One 72-column id must not push STATUS and RUNNING off every row at 80 columns. The middle goes because a provider is at the start of an id and what tells two variants apart (`-4bit`, `-6bit`) at its end. | Task 11, `TestModelsTabKeepsStatusAndRunning`, `TestModelsTabFitsTheTerminal` (STATUS and RUNNING from 80 columns up, with a 72-column id) |
| Which columns does `wt model list` print, and what does a narrow terminal lose? | MODEL, FAMILY, LOC, STATUS, RUNNING, SIZE, PATH. PATH is printed only when every row fits on one line with it; SIZE while it leaves MODEL at least 20 columns; a longer id gets a line of its own. A note on stderr names what was left out. Into a pipe (width 0) every column is printed, one model per line. | "Size and path that do not fit are left to `--json`." The id is what a reader copies. | Task 5, `TestModelListFitsEightyColumns`, `TestModelListDropsSizeBeforeItCutsAnId`, `TestModelListWithoutAWidthLimit` |
| How is `wt stats`' renderer reused? | `renderUsageTable`'s layout becomes `renderPlainTable(cols, rows, width)` in `cmd/wt/plain_table.go`, with a per-column alignment; `renderUsageTable` builds its cells and calls it. | One renderer for both listings. | Task 5, `TestPlainTableAlignsBothKindsOfColumn`, and the existing `TestRenderUsageTable*` tests, unedited |
| What is in `wt model list --json`? | One object: `registry`, `models` (each `id`, `family`, `provider_id`, `model_name`, `location`, `tags`, `registered`, `status`, `running`, `size_bytes`, `path`, and `target`/`draft` for a pairing), `providers` (probe status per family). `running` is the text table's string. `size_bytes` and `path` are `null` when not known; `tags` is never `null`. | The spec names the flag, not the shape. | Task 5, `TestModelListJSON` |
| What is `Entry.Size` for omlx and mtplx? | 0 (not known). No directory is walked. | "Filled from data the probe already touches." A walk of tens of gigabytes on every probe is not that. | Task 2, `TestInventoryEntriesCarryTheModelDirectory` |
| What status has a registry row whose location does not resolve, or whose provider wt has no probe for? | `unknown`, with its location blank in the first case and running `?` in the second. | Both are rows the user has to see to repair. | Task 4, `TestRowsListEveryModelWithItsStatus` |
| When is running `?` rather than blank? | When the family's probe is not trusted (`lifecycle.ProbeTrusted`), unless the server refused the connection, where "not running" is a fact. | A blank cell reads as "stopped". | Task 4, `TestRowsMarkAnUntrustedProbe` |
| In what order are rows listed? | Registry rows by family, then id; discovered rows after them, by id. | Families group; `new` rows are the ones to act on last. | Task 4, `TestRowsListEveryModelWithItsStatus` |
| How does wt read `fetch` and `draft`? | `config.Model` gains read-only `Fetch` and `Draft` (`ModelArtifact{Repo, LocalPath}`). | The rows need `fetch.local_path`; the pairing hint needs both sides. Nothing encodes a `Model` into the registry, so a write is unaffected. | Task 3, `TestModelDecodesFetchAndDraft`; the existing `TestAWriteNeverTouchesAKeyItWasNotAskedTo` |
| What does an add write? | `id`, `family`, `provider_id`, `model_name`, `tags` (always, `[]` when none), and `location`, `cost.*` and `model_info` only when given. No `source`, and no `fetch` for a single model. A new price is a float. | The keys wt and llmbench read. modelman writes `tags` on every row. | Task 7, `TestAddWritesOneRowInModelmansLayout`, `TestAddSeedsTheProviderRowInTheSameWrite` |
| May two rows name one local artifact? | No: an add whose provider family and `model_name` match an existing row is refused, naming that row. A cloud provider may list one model under two ids. | The inventory gives an artifact to the first row that matches it; the second would read `missing` for ever. | Task 7, `TestAddRefusals` |
| Does `wt model add openrouter …` seed the default openrouter row? **(owner decision, before PR C)** | Yes. `config.SeedRegistryDefaults` gives a cloud provider its default row when a model references it or an agent lists it (never from PATH: it has no command). `wt model init` therefore also repairs a registry that already holds an openrouter model and no openrouter row. | The spec's CLI table: add "seeds a missing default provider row in the same write", and openrouter is a provider wt has a default row for. Step 2 seeded it for an agent only, which made the command this plan's own help and guides lead with fail on a fresh registry. It reverses a subtest Step 2 pinned, which is why it is the owner's to confirm. | Task 6, `TestSeedAddsOnlyWhatTheMachineNeeds` ("a model that references openrouter gets the row"); Task 7, `TestAddSeedsTheProviderRowInTheSameWrite` ("openrouter") |
| What happens when the provider has no row and wt has no default row for it? | The add is refused (`provider_id "x" names no provider row`), naming the file to add the row to and the five providers wt does add a row for (ollama, omlx, mtplx, mlx_lm_server, openrouter). `omlx-6bit` is one of the refused: its row is a variant of omlx's that only the user can describe. | A model row wt's own validation refuses must not be written, and wt has no command that writes a provider row. | Task 7, `TestAddRefusals` |
| What may `--id` be? | `<provider>/<name>`: something on each side of the first `/`, and no space or control character. Anything else is refused on the `id` field. A derived id is not checked: it comes from a name the provider lists. | `wt stop <arg>` reads an argument with no `/` as a provider, `-M` is documented as `<provider>/<name>`, and an id with a space cannot be typed unquoted. | Task 7, `TestAddRefusals`; Task 8, `TestModelCommandsThroughTheCommandLine` |
| Does a derived cloud id match modelman's in every case? | In every case but one. modelman's form kept the model name of a *native* provider (`auth.type = "native"`: an agent's own row, such as `claude`) as it was; wt writes `/` as `--` for every provider it has no probe for, native ones included, so a derived cloud id always has exactly one `/`. | The spec's reason for modelman's rule is that ids agree across tools; they do wherever a native model name has no `/`, which is every one in use. Where one has, `--id` gives modelman's spelling. | Task 7, `TestDeriveID` (its last case) |
| In what order does an add check and look up? | What needs no registry is checked first (`modeladmin.CheckAdd`: required fields, each value, the subscription rule, the id's shape), then `ollama show` runs, then the locked write, which alone can refuse a taken id or an artifact that is already registered. | A mistyped price must not wait on `ollama show`, up to its ten-second timeout when the daemon is down. | Task 7, `TestAddRefusals` (`CheckAdd` beside every case); Task 8, `TestModelAddAsksOllamaWhatTheModelCanDo` ("an add that is going to be refused does not ask first"); Task 14, `TestModelFormRefusesBeforeItAsksOllama` |
| What does an add under an `api_key` provider with no `secret_ref` do? | It succeeds, with a warning that the route will carry an empty key. | Step 2 left this to Step 3. The model is valid; the provider row is what needs the key. | Task 7, `TestAddWarnsAboutAKeylessProvider` |
| What does an empty value do on edit? | It clears the key (`--tags ""` writes `tags = []`; an empty `--location` deletes the key, so the provider's applies). An empty `--family` is refused. When clearing a price empties the row's `[models.cost]` table, the table goes too; an empty one that was there before the edit is left alone. | A model must have a family. A model whose prices were all cleared should read like one that never had any, which is how modelman writes a model with no cost. | Task 7, `TestEditPatchesOnlyWhatWasGiven`, `TestEditRefusals`, `TestEditClearingTheLastPriceDropsTheCostTable` |
| What does an edit of a price do to a row still in modelman's old cost layout (`cost.kind`)? | It moves the whole table to the current layout, as modelman's own loader reads it: a `per_token` price becomes the input and the output price, a `subscription`'s price and period take their current names, and the old keys go — never over a key the edit itself sets or clears. An edit that touches no price leaves the old layout exactly as it was. | modelman reads a table that has `kind` by its old keys alone, so a current key written beside them would be a price wt shows and modelman ignores until Step 6. Refusing instead would leave the user a hand edit. | Task 7, `TestEditMovesALegacyCostTable` |
| Does `wt model edit` sync when nothing changed? | No: it prints `no change: <id>` and runs no route sync. The spec's "one `UpdateRegistry` and then one route sync" is read as "one sync per write"; there was no write. | A sync probes every local provider and may restart the proxy. | Task 8, `TestModelCommandsThroughTheCommandLine` (the repeated edit: `no change`, and still two syncs) |
| Which rule joins the subscription fields? | A subscription price needs a period of `month` or `year`, in the same edit or already in the row. A period with no price is allowed. | modelman's cost rule, as `registry_validate.go` already has it; named per field so the form can focus it. | Task 7, `TestEditRefusals` |
| Is `wt model rm` all or nothing, and how does it ask? | One write removes every id, or none when one is missing. A repeated id is removed once. It asks `y/N` on `/dev/tty` with the prompt `wt stop` already has (`promptStop`, which now opens the terminal through the existing `openTTY` seam); with no terminal and no `--yes` it refuses. | A half-applied removal leaves the user to work out what went. Piped input must not authorise it. One prompt, not a copy of it; and a seam, so that the refusal is tested on a developer's machine, where the test run has a terminal. | Task 7, `TestRemoveIsAllOrNothing`; Task 8, `TestModelCommandsThroughTheCommandLine`, `TestModelRmWithoutATerminalNeedsYes` |
| When does `rm` probe the providers? | Only when a target is a local registry model, and before the write, to learn where its weights are. | A cloud removal should not wait on local servers. | Task 8, `TestModelCommandsThroughTheCommandLine` pins the weights line; the cloud-only shortcut is read off `runModelRm` and not pinned |
| Which commands need a config that loads? | `wt model list` needs the load to succeed (not validation). `add`, `edit` and `rm` need neither: `UpdateRegistry` is the judge. `rm` without a loaded config prints no weights line. | They are the commands that repair a registry. | Task 8, `TestModelRmStillWorksWhenTheConfigDoesNotLoad`; list's gate is read off `modelListCmd` and not pinned |
| How does touched-row validation judge location and provider? | `validateModelRefs`: the provider row must exist, then `Config.ResolveLocation` is asked over the document's provider rows as they are after `apply`. | "wt's real rule is `ResolveLocation`." Asking after `apply` lets an add seed and validate in one write. | Task 6, `TestUpdateRegistryValidatesAModelAgainstItsProviderRow` |
| What does `wt litellm sync` say about a provider with no row? | One warning per provider, with its models. It is left out when the existing "no provider entry" probe warning already names the provider's family. | Step 2 left the silence to Step 3. Two lines about one gap would be noise. | Task 9, `TestLitellmSyncWarnsAboutAProviderWithNoRow` |
| What exactly does `wt stop --all` stop? | Every candidate of a non-pool provider through the stop loop, then each pool service once per family (`omlx` and `omlx-6bit` are one), also when nothing is loaded. One confirmation for all in-use models. Every stop is attempted; failures are joined and the exit is 1. An argument is refused. | It is what `wt stop omlx` already does for one provider, and what "free the machine" means. | Task 1, all six `TestStopAll*` tests |
| When does the tab probe? | On its first showing (at once when the editor opens on it), and on `r`. A probe's result carries a generation number; a stale one is dropped. The cursor stays on its row. While the first probe is out, `tab` still switches tabs and `q` still quits. | A probe per tab press would dial every local server each time; and a provider that does not answer must not hold the user on the tab for its timeout. | Task 11, `TestTabKeySwitchesTabsAndProbesOnce`, `TestModelsTabDropsAStaleProbe`, `TestModelsTabKeepsTheCursorAcrossARefresh` |
| How does the filter's result reach the right list? | bubbles ranks a list's items in a command and sends the result back as a `list.FilterMatchesMsg`, which does not say which list it is for. Every command a list returns is wrapped (`tagFilter`) so that the result comes back as `filterMatchesMsg{tab, built, matches}`, and is handed to that tab's list — for the Models tab, only if the table has not been rebuilt since. | The editor dropped every message that was not a key, so `/` narrowed nothing on either tab (on `main` too, for the Agents tab), and `d` acted on a row the user was not looking at. Forwarding the bare message would apply one tab's matches to the other's list. | Task 11, `TestModelsTabFilterNarrowsTheTable`, `TestAgentsFilterNarrowsTheList` |
| Does `esc` quit from a list? | No, on either tab: both lists have bubbles' own quit keys turned off (`DisableQuitKeybindings`). `esc` clears a filter and does nothing else; `q` and `ctrl+c` quit through the editor's `quit`. This changes the Agents tab, where `esc` on the list ended wt without the unsaved-changes prompt. | `esc` cancels the form and the remove prompt, so one `esc` too many threw away every unsaved agent edit. `wt/docs/wt-config.md` never listed `esc` as a quit key. | Task 11, `TestEscOnAListDoesNotQuit` |
| Does `ctrl+s` do anything on the Models tab? | No. It saves the Agents tab's buffer from the Agents tab only. | There is nothing to save on the Models tab, and a save started from it reported its outcome on the tab the user was not looking at. The quit prompt still offers the save. | Task 11, `TestCtrlSOnTheModelsTabDoesNothing` |
| What happens to a quit while a registry write is in flight? | It waits. `leave` records it (`quitPending`), the status says so, and `applyModelRemoved` / `applyModelSaved` issue the quit once the write has reported and `registryChanged` is set. A write that was refused calls the quit off. A probe in flight holds nothing up. | The write runs in a command; a program that ended before its message was handled returned `Result{RegistryChanged: false}` for a registry that had changed, and `cmd/wt` skipped the sync it owed (six runs in eight, with `y` and `q` a millisecond apart). | Task 11, `TestQuitWaitsForARegistryWriteInFlight`; Task 14, `TestQuitWaitsForAFormSaveInFlight` |
| How long does the tab's status stay? | What the last action said stays until the next key on the table. The routes-pending note (`LiteLLM routes pending (sync on quit)`) stays until the editor closes, and why the registry did not load stays until a probe reads it again. Each part is on a line of its own. | A status that never cleared kept three to six lines from a 12-line terminal's table for the rest of the session. | Task 11, `TestModelsTabStatusClearsOnTheNextKey` |
| How does a path appear on the tab? | With `~` for the home directory, and on a line of its own in the detail block, the remove prompt and the status after a removal; a line is broken between words only, so a path is cut at the terminal's edge or not at all. `wt model rm` and `wt model list` print the path in full. | A real model directory is `/Users/<name>/.omlx/models/<directory>`: on one clipped line at 80 columns it lost the directory's name, and wrapped by lipgloss it broke at a hyphen. | Task 11, `TestModelsTabRemove`, `TestModelsTabFitsTheTerminal` ("local model selected") |
| What do the key hints do on a narrow terminal? | Each screen has a short form used when the full one does not fit: `enter · n · d · r · / · tab · q quit` for the table, `^s save · esc · ↑/↓ · ←/→ change` for the form. | A hint line cut at the edge lost its last keys, among them the only mention of `←/→`. | Task 11, `TestModelsTabFitsTheTerminal`; Task 14, `TestModelFormShowsWhatDoesNotFit` |
| How does the tab reach the machine? | Through `configeditor.Options.Models` (`ModelsDeps`: load, probe, seeding environment, ollama lookup), which `cmd/wt` fills from its own seams. `Run` fills any left nil with the real functions. | The repo's seams are package variables; a struct handed in lets a configeditor test own a whole machine and keeps `cmd/wt`'s stubs in force for the tab. | Task 11, every test built on `tabMachine`; Task 12, `TestBareModelOpensTheModelsTab` |
| Does the exit sync run when the editor ends with an error? | Yes, whenever the registry changed. Its warning goes to stderr. | The write already happened. | Task 12, `TestEditorSyncsRoutesOnceOnExit` |
| How is "byte-identical `config.yaml` does not restart the proxy" verified? | A `cmd/wt` test runs the real sync twice with a restart command that appends to a file: one restart, not two. | The spec asks for it to be verified in this step; `internal/litellm` already has `TestSyncUnchangedDoesNotRestart` one level down. | Task 12, `TestRouteSyncThatChangesNothingDoesNotRestartTheProxy` |
| What does the tab bar look like, and where does the Agents tab name `tab`? | The first line, `[Agents]   Models` or ` Agents   [Models]`: brackets, so the current tab reads as current without colour. It replaces the old title line, which was also what told a user where models live; so the Agents list's help line gains `q quit` and `tab models` (in that order: a 40-column terminal shows the line as far as `q quit`). | Same line count as before, so the Agents list's sizing is unchanged. `q quit` has to be added back because turning off the list's own quit keys takes it out of the list's help. | Task 11, `TestTabKeySwitchesTabsAndProbesOnce` |
| How are the form's three fixed-value fields edited? | As choices changed with left and right (and space): Provider, Location (`(the provider's)`, `local`, `cloud`), Subscription period (`(none)`, `month`, `year`). The provider starts on `ollama`. | A typed location or period is one more thing to get wrong. | Task 14, `TestModelFormAddWritesAtOnce`, `TestModelFormEditSendsOnlyWhatChanged` |
| Which providers does the form offer? | Every registry provider and the four wt seeds a row for when a model names them (ollama, omlx, mtplx, openrouter), sorted, without mlx_lm_server. | The spec excludes mlx_lm_server; the four make the form usable before a registry exists. | Task 14, `TestModelFormOnAnEmptyRegistry`; Task 18, `TestModelsTabShowsAndEditsAPairing` |
| What does an edit from the form send? | Only the fields whose value differs from what the form opened with. | Sending all of them would add `tags = []` to a row that had no such key, on every save. | Task 14, `TestModelFormEditSendsOnlyWhatChanged` |
| What is the Family field's accept key, and what does accepting do? | Right, and only while the cursor is at the end of the text. The form does the accepting itself and bubbles' own accept binding is off: the field's value becomes the suggestion, whole. `ctrl+n` and `ctrl+p` (bubbles' own keys) move between suggestions when several match. | Tab is "next field". bubbles v1.0.0 accepts wherever the cursor is, panics when a value is set after a suggestion matched, and only appends the rest of the suggestion: `QW` became `QWen3.8`, a family that does not group with `qwen3.8`. | Task 14, `TestModelFormFamilySuggestions`, `TestModelFormPrefilledFamilyDoesNotPanic` |
| What does the form do on a terminal with fewer rows than fields? | It draws as many fields as fit, always including the focused one, and gives its first and last row to `↑ N more` and `↓ N more` when fields are above and below (with fewer than three rows there is no room for a marker, and the fields are drawn alone). | Bubble Tea drops a too-tall view's top lines; and a field off the screen must not be a field the user does not know of. | Task 14, `TestModelFormFitsTheTerminal`, `TestModelFormShowsWhatDoesNotFit` |
| What does the form do with text wider than the terminal? | The title wraps. A fixed value (Provider and Model name, when editing or registering) loses its middle to an ellipsis; it is not wrapped, because the scrolling above counts one row per field. A field that is not being edited shows the start of its value and an ellipsis; the one being edited scrolls under the cursor. Each input is as wide as the room beside its own label. | Every one of these was cut at the edge with no sign of it, and every input was sized for the longest label: sixteen columns of a 40-column terminal. | Task 14, `TestModelFormShowsWhatDoesNotFit` |
| What does bare `wt model` do without a terminal? | It fails, naming `wt model list`, `add`, `edit` and `rm`. | The spec: "needs a TTY". | Task 12, `TestModelCommandGroup` |
| Does the removed-name message for `wt models` change? | No: `wt models is removed; use wt config to view models` is true again once the tab ships. | The spec does not ask for a change, and `TestRemovedSubcommand_Rejected` pins the text. | Not changed |
| How is a pairing named, and how are its two sides stored? | `model_name` is `<target's last segment>+draft-<draft's last segment>`, the id that with `mlx_lm_server/` in front (modelman's form did the same). A side that starts `/`, `~`, `./` or `../` is a `local_path`; anything else is a `repo`. | The Python tools read `fetch.repo`, `fetch.local_path`, `draft.repo`, `draft.local_path`. A Hugging Face repo id never starts with one of those. | Task 18, `TestAddAPairing`, `TestPairingName` |
| When are two pairings the same? | When their two sides are: the same `repo` or the same `local_path` on the target and on the draft (a trailing `/` aside). Not when their names are: `sameArtifact`, which compares `model_name`, is not asked about an mlx_lm_server row. A different pairing whose names make an id that is taken is refused for the id only, and the message says to pass `--id`; with one, it is added, and the two rows then share a `model_name`. | The name is only the last segment of each side, so a model quantized locally and the Hugging Face original make the same name with one draft. Nothing in wt keys on a pairing's `model_name`: no inventory lists pairings, and which one is running is already a guess with two registered (the spec's Step 6 refiles that). | Task 18, `TestAddAPairing` |
| What does `wt model add … --draft` print besides the spec's lines? | `start it with: <the llmbench command>`, after `added model:`. | wt registers a pairing and has no engine for one; without the line the user has a row and no way to find out how to run it. | Task 18, `TestModelAddAPairing` |
| What does `MissingReason` need to name a pairing's target and draft? | A new first parameter, the config: `MissingReason(cfg, snap, id)`. With no config, or a row with no `fetch`/`draft`, the command shows `<target>` and `<draft>`. | The snapshot entry carries neither side. | Task 17, `TestMissingReason`, `TestPairingStartCommand` |
| What does `BlockReason` say for a provider that is not mlx_lm_server and has no engine in wt? | `… wt cannot start provider "<id>" — start it with that provider's own tool`. | `modelman start` was the old answer for every such provider, and modelman is going. | Task 17, `TestBlockReasonNamesTheFix` |
| What does bare `modelman` print now? | The Step 2 notice with one bullet changed: add, edit or remove a model is `wt model` (the Models tab of `wt config`), or `wt model add`, `edit`, `rm`, with `wt model list` to see them. | The spec names what the notice must name. The other bullets are still true. | Task 15, `test_no_args_does_not_open_the_tui_and_says_where_to_go` |

## What the Code Looks Like Today

Line numbers are as of `main` at `a722ddb`. (This plan was written on a checkout at `baca280`. `main` was two `wt stats` PRs ahead of it then, #293 and #294, and a third, #300, has landed since, at `d4a9ccf`. The three touch none of the files below except the `stats` ones: at `d4a9ccf`, `stdoutWidth` is line 224 of `stats_usage.go`, and every diff in this plan applies there as it stands.) Each step also names the function or quotes the text, and that is what to match on.

| Where | What is there |
|---|---|
| `wt/cmd/wt/main.go:235` | The removed-name guard in the root `RunE`: `wt models` errors with ``wt models is removed; use `wt config` to view models``. A real `model` subcommand is dispatched before it |
| `wt/cmd/wt/main.go:431` | `cmd.AddCommand(rotateCmd(a), …, profileCmd(a), modelCmd(a))` |
| `wt/cmd/wt/model.go:19`, `:25`, `:33` | `var seedEnv = config.DefaultSeedEnv`; `var syncRoutesAfterWrite = realSyncRoutesAfterWrite`; `func realSyncRoutesAfterWrite(out, errOut io.Writer) string` — reloads the config, runs `runLitellmSync`, returns a warning or `""` |
| `wt/cmd/wt/model.go:54`, `:109` | `func modelCmd(_ *app) *cobra.Command` — bare `wt model` prints help; `func runModelInit(out, errOut io.Writer, asJSON bool) error` |
| `wt/cmd/wt/model_cmds.go:30-41`, `:45` | The stop seams `stopCandidates`, `stopEntries`, `stopPickerAll`, `confirmStop`, `stopProvider = lifecycle.Stop`; `func promptStop(question string) (bool, error)` asks on `/dev/tty`, which it opens itself |
| `wt/cmd/wt/launch.go:77` | `var openTTY = func() (*os.File, error)` — the seam `promptProfile` opens the terminal through; `promptStop` and `promptReplace` (`start.go:136`) do not use it |
| `wt/cmd/wt/model_cmds.go:57`, `:89`, `:101`, `:209` | `stopCmd`, `stoppableProviders(cfg) []string`, `runStop(out, cfg, arg, yes)`, `stopImpact(targets) (sessions int, users []string)` |
| `wt/cmd/wt/commands_config.go:26`, `:33` | `var configeditorRun = func(theme, cfg, cfgErr) error`; `func configCmd(a *app)` |
| `wt/cmd/wt/helpers.go:38`, `:97` | `renderTable` — a bordered lipgloss table with a rule between rows and no width limit (not used for the new listing); `var stdinTTY = isStdinTTY` |
| `wt/cmd/wt/stats_usage_table.go:47`, `:102` | `func visibleID(id string) string`; `func renderUsageTable(rows []usageRow, width int) string` — the borderless table this plan generalises |
| `wt/cmd/wt/stats_usage.go:225`, `wt/cmd/wt/stats.go:134` | `var stdoutWidth = realStdoutWidth`; `cmd.Flags().String("family", …)`, a local flag that shadows the root's persistent `-F/--family` — the precedent for `wt model add --family` and `--tags` |
| `wt/cmd/wt/litellm.go:130`, `:191`, `:418` | `runLitellmSync`, `syncUntouchedAndWarnings`, `gapReason` — the last already says "the registry has models for it but no provider entry" for a probe family |
| `wt/cmd/wt/testmain_test.go:27`, `:98` | `TestMain` (stubs the probe, starts, stops, `seedEnv`, `syncRoutesAfterWrite`); `stubProbeInventory(t, snap)` |
| `wt/cmd/wt/model_test.go:19`, `:28`, `:44`; `helpers_test.go:53` | `stubSeedEnv`, `stubRouteSync(t, warning) *int`, `realRouteSync(t)`; `withCleanConfigEnv(t, home)` |
| `wt/internal/config/registry_doc.go:149`-`:243` | `Models()`, `Providers()`, `PatchModel(id, set, unset)`, `AddModel(table)`, `CloneModel`, `RemoveModel(id) (*tomlw.Table, error)` |
| `wt/internal/config/registry_write.go:68` | `func UpdateRegistry(apply func(*RegistryDoc) error) (changed bool, err error)` |
| `wt/internal/config/registry_validate.go:26`, `:61` | `validateTouched`, `validateModelRow` — no check of `location` or of the provider row |
| `wt/internal/config/registry_seed.go:15`, `:178`, `:225` | `type SeedEnv`, `func SeedRegistryDefaults(d *RegistryDoc, env SeedEnv) (added, unseeded []string, err error)`; its cloud loop, `if existing[id] \|\| !listed[id]` — openrouter is seeded for an agent only (`registry_seed_test.go:178`, "openrouter is not seeded for a model or from PATH") |
| `wt/internal/config/config.go:466`, `:789`, `:949` | `type Model struct` (no `fetch`, no `draft`); `DiscoveredModelID(providerID, artifactName)`; `(*Config).ResolveLocation(m Model) (Location, error)` |
| `wt/internal/config/fortest.go:33`, `:113` | `IsolateConfigHomeForTest() (home string, cleanup func())`; `RegistryWriteGuardArmed() bool` |
| `wt/internal/localmodels/inventory.go:54`, `:164`, `:350`, `:448` | `type Entry struct` (no path, no size); `RunningOnly(providerID)`; `probeFamily`; `inventory` |
| `wt/internal/localmodels/sources.go:19`, `:62`, `:117` | `ollamaModelNames` (decodes `name` and `remote_host` only), `scanModelDirs`, `scanOmlxModels` — all return names only |
| `wt/internal/catalog/catalog.go:174`, `:240` | `MissingReason(snap, id)`, `(Row).BlockReason()` — both end ``start it with `modelman start <id>` `` |
| `wt/internal/tui/layout.go:41`, `:117`, `:284`, `:348` | `type listFrame`, `fitList`, `fitTo` (which calls `fitTableColumns`), `sizeList` |
| `wt/internal/tui/modeltable.go:49`, `:55`; `model_list.go:175`, `:182` | `colDropOrder`, `type tableColumns struct` with `[numCols]` arrays; `tableTitleRoom`, `fitTableColumns` |
| `wt/internal/tui/layout_test.go:576` | `TestEveryListPhaseFitsTheTerminal` — widths 40/80/120 by heights 12/24/50 |
| `wt/internal/configeditor/editor.go:37`, `:142`, `:220`, `:312`, `:346` | `type model struct`; `fitList` (the Agents list's own arithmetic); `update`'s `if msg, ok := msg.(tea.KeyMsg); ok {` — every message that is not a key is dropped, the list's filter results among them, so `/` narrows nothing; the title line `"Agents (providers/models: edit registry.toml by hand)"`; `func Run(theme, cfg, cfgErr, opts ...tea.ProgramOption) error` |
| `wt/internal/configeditor/agents_tab.go:58` | `buildAgentsList` — a bubbles list with its own quit keys on: `esc` on it ends wt without the editor's unsaved-changes prompt |
| `wt/internal/configeditor/agent_form.go:15`, `:52`; `form_view.go:19`; `delete.go:23`; `save.go:23` | `newTextInput`, `handleAgentFormUpdate`; `renderFormFields`; `handleDeleteUpdate` (the y/N prompt); `var saveCmd` (a save in a `tea.Cmd`) |
| `wt/internal/configeditor/` tests | No `TestMain`: nothing there writes the registry yet |
| `wt/internal/lifecycle/lifecycle.go:156`, `:382`; `tenancy.go:26` | `ProbeTrusted(snap, family)`, `Stop(ctx, cfg, providerID)`; `TenancyOf(providerID)` |
| `wt/internal/survey/stop.go:96`, `:330` | `StopCandidates(cfg) []Candidate`, `StopEntries(w, cfg, entries) error` (one `SettleRoutes` per batch) |
| `wt/internal/litellm/service_test.go:877` | `TestSyncUnchangedDoesNotRestart` |
| `modelman/src/modelman/main.py:437` | `TUI_DISABLED_MESSAGE`, whose second bullet says to edit `registry.toml` by hand |
| `modelman/src/modelman/screens/forms.py:201`, `:209`, `:246`, `:1172` | `_basename_of`, `_parse_price` (empty is none; finite; not negative), `parse_subscription_fields`, `_submit_dual_model` (the pairing's name and id) |
| `modelman/src/modelman/ollama_caps.py:19` | `parse_ollama_show` — the Capabilities section, `tools` and `vision` |

## File Structure

| File | Change | PR | Responsibility |
|---|---|---|---|
| `wt/cmd/wt/model_cmds.go`, `model_cmds_test.go` | modify | A, C, G | A: `wt stop --all` (`runStopAll`, `poolProviders`). C: `promptStop` opens the terminal through `openTTY`. G: `MissingReason`'s new argument |
| `wt/internal/localmodels/sources.go`, `inventory.go` | modify | B | `Entry.Path`, `Entry.Size`; the scans return directories, `/api/tags` its sizes |
| `wt/internal/localmodels/inventory_path_test.go` | create | B | Their tests |
| `wt/internal/config/config.go`, `model_artifact_test.go` | modify, create | B | `Model.Fetch`, `Model.Draft`, `ModelArtifact` |
| `wt/internal/modeladmin/rows.go`, `rows_test.go`, `testmain_test.go` | create | B | `Row`, `Rows`, `WeightsNote`, `FormatSize`; the package's isolating `TestMain` |
| `wt/cmd/wt/plain_table.go` | create | B | `renderPlainTable`, the one borderless text table |
| `wt/cmd/wt/stats_usage_table.go` | modify | B | `renderUsageTable` calls it |
| `wt/cmd/wt/model_list.go`, `model_list_test.go` | create | B | `wt model list [--json]` |
| `wt/cmd/wt/model.go` | modify | B, C, E | Registers `list`; then `add`, `edit`, `rm`; then bare `wt model` opens the tab |
| `wt/internal/config/registry_validate.go` | modify | C | `validateModelRefs`: provider row and location |
| `wt/internal/config/registry_validate_test.go`, `registry_write_test.go` | modify | C | Its tests; one fixture gains a provider row |
| `wt/internal/config/registry_seed.go`, `registry_seed_test.go` | modify | C | openrouter's default row for a model that references it |
| `wt/internal/modeladmin/fields.go`, `apply.go`, `ollama.go` | create | C, G | `Fields`, `FieldError`, `ParsePrice`, `DeriveID`; `CheckAdd`, `Add`, `Edit`, `Remove`; `OllamaCapabilities`. G: pairings |
| `wt/internal/modeladmin/apply_test.go`, `ollama_test.go` | create | C, G | Their tests |
| `wt/cmd/wt/model_write.go`, `model_write_test.go` | create | C, G | `wt model add`, `edit`, `rm`; the `ollamaCaps` and `confirmRemove` seams. G: `--draft` |
| `wt/cmd/wt/model.go` (`wt model init`'s help) | modify | C | openrouter: "when a model or a configured agent uses it" |
| `wt/cmd/wt/litellm.go`, `litellm_test.go` | modify | C | `missingProviderWarnings` |
| `wt/cmd/wt/testmain_test.go` | modify | C | Fails the two new seams by default |
| `wt/internal/tuilayout/layout.go`, `columns.go`, `columns_test.go` | create | D | The shared sizing helpers and `Columns` |
| `wt/internal/tui/layout.go`, `modeltable.go`, `model_list.go` | modify | D | Forwarders; the picker's table on `tuilayout.Columns` |
| `wt/internal/configeditor/tabs.go` | create | E | `Tab`, `Options`, `ModelsDeps`, `Result`, `tabBar` |
| `wt/internal/configeditor/models_tab.go` | create | E, F | The Models tab: rows, frames, keys, probe, remove; `flow`, `wrapText`, `middleCut`, `tildePath`, `fitHints`; `tagFilter` and `filterMatchesMsg`. F: `n` and `enter` |
| `wt/internal/configeditor/editor.go` | modify | E, F | Tabs, `quit` and `leave`, the filter results' routing, `Run`'s new signature. F: the form's messages |
| `wt/internal/configeditor/agents_tab.go` | modify | E | The Agents list: no quit keys of its own; `q quit` and `tab models` in its help |
| `wt/internal/configeditor/testmain_test.go`, `models_tab_test.go`, `editor_test.go` | create, create, modify | E, F | The package's isolating `TestMain`; the tab's tests |
| `wt/cmd/wt/commands_config.go`, `commands_config_test.go`, `main_test.go`, `model_test.go` | modify | E | Task 11: `configeditorRun` calls the new `Run`. Task 12: `runConfigEditor` and the exit sync; `stubConfigEditor`; bare `wt model` |
| `wt/internal/configeditor/models_form.go`, `models_form_test.go` | create | F | The add, register and edit form |
| `modelman/src/modelman/main.py`, `modelman/tests/commands/test_run_tui.py` | modify | F | The notice bare `modelman` prints |
| `wt/internal/catalog/catalog.go`, `catalog_test.go` | modify | G | `PairingStartCommand`, the two hints |
| `wt/cmd/wt/resolve.go`, `start_json.go`, `smoke.go`, `wt/internal/tui/app.go` and three test files | modify | G | `MissingReason`'s callers; tests that pinned the old hint |
| `wt/internal/configeditor/models_pairing_test.go` | create | G | A pairing on the tab |
| `wt/docs/wt-model.md` | create | B–G | The command reference, grown by each PR |
| `wt/CLAUDE.md`, `wt/CHANGELOG.md`, `wt/docs/wt-config.md`, `wt/docs/wt-start-stop.md`, `wt/docs/configuration.md`, `wt/docs/internals/{tui,config-and-registry,local-models,testing}.md` | modify | A–G | Each PR documents what it ships |
| `CLAUDE.md`, `README.md`, `wt/README.md`, `modelman/README.md`, `docs/guides/{00,01,02,03,04,06,08,10}-*.md`, `docs/reference/provider-artifacts.md`, `.claude/skills/mlx-lm-quantization/SKILL.md`, `bin/mlx-quantize`, `docs/contracts/registry.sample.toml` | modify | A, B, F, G | The "edit `registry.toml` by hand" wording Step 2 wrote becomes `wt model` and the tab; `local_path` and `[[families]]` stay hand edits |

## Branches and PRs

Seven PRs, the spec's slices A to G. Each is one branch off an up-to-date `main`.

| PR | Tasks | Branch | Needs merged first |
|---|---|---|---|
| A | 1 | `feat/wt-stop-all` | nothing |
| B | 2, 3, 4, 5 | `feat/wt-model-list` | nothing |
| C | 6, 7, 8, 9 | `feat/wt-model-add-edit-rm` | B (`modeladmin`, `Model.Fetch`, `model.go`) |
| D | 10 | `refactor/wt-tuilayout` | nothing |
| E | 11, 12, 13 | `feat/wt-models-tab` | C and D |
| F | 14, 15, 16 | `feat/wt-models-tab-form` | E |
| G | 17, 18 | `feat/wt-model-pairings` | F (its tab test uses the form) |

**What can run in parallel.** A, B and D are independent of each other and can be built at the same time in separate worktrees; C starts when B has merged; E when C and D have; then F, then G. A touches `cmd/wt/model_cmds.go`, which G also edits (one line, elsewhere in the file). Every PR but D adds a bullet at the top of `wt/CHANGELOG.md`'s "Added" list, and most edit `wt/CLAUDE.md`; whichever of two parallel PRs lands second resolves those by keeping both.

**Before PR C: one owner decision.** PR C makes `wt model add openrouter <model> --family <f>` seed the default openrouter row, which reverses a subtest Step 2 pinned ("openrouter is not seeded for a model or from PATH"). The spec's CLI table asks for it; the Decisions table has the row. Ask the owner before cutting the branch. If the answer is no, do not start PR C from this text: the plan has to be amended first — Task 6's Steps 5 to 7 go, and with them the "openrouter" subtest of `TestAddSeedsTheProviderRowInTheSameWrite`, the fourth form provider, and every line of help and guide that shows `wt model add openrouter …` without saying it needs an openrouter `[[providers]]` row.

**Before PR A:** this plan is on the branch `docs/modelman-retirement-step3-plan`, not on `main`. Merge that branch first, with the owner's OK for the push and the merge. Until it has merged, a branch cut from `main` does not have this file; read it with:

```bash
git show docs/modelman-retirement-step3-plan:docs/superpowers/plans/2026-10-07-modelman-retirement-step3-model-cli-and-tab.md
```

Start each branch from the remote's `main`. This form also works inside a git worktree, where `git switch main` fails because `main` is checked out elsewhere:

```bash
git fetch origin
git switch -c <branch> origin/main
```

**Verification at the end of every PR**, as its last task says:

```bash
cd wt && test -z "$(gofmt -l .)" && go vet ./... && go test -count=1 ./... && make check
```

Expected: every package `ok`, and `make check` ends without an error. Then `make check && make test` in `modelman/` when the PR touched it (PR F), and from the monorepo root:

```bash
make test-all && make check-links
```

Expected: exit 0.

**Pushing and opening a PR need the owner's OK.** Each PR's last step is "hand off": stop, say what was verified, and wait.

**A flaky test that is not this work's.** `TestEnsureModelRouteToSendsItsOutputToTheCaller` in `wt/internal/lifecycle` fails now and then on `main` itself (Step 2's plan recorded it). If it is the only failure, run the suite again. Do not change it in these PRs; tell the owner.

**How this plan's code was checked.** Every listing below was built and run in a scratch copy of the repository before it was written here, as eight commits: one per PR, and PR E's two code tasks one each. Each task's failing state was observed from a tree holding the PR's earlier tasks and that task's tests only; the output shown under "Run it to see it fail" is what that run printed. Each PR was run on the stated prerequisites alone (A, B and D each on `main`; C on B; E on B, C and D; F and G on top — C to G without A), with `gofmt`, `go vet` and `go test -count=1 ./...` passing every time. The plan's own text was then replayed — every new file written and every diff applied, in the order given — onto `main` at `a722ddb`, where the result is byte for byte the tree that was tested, and onto `main` at `d4a9ccf`, where `make check` in `wt/`, `make check && make test` in `modelman/` and, with the documentation edits applied too, `make test-all` and `make check-links` from the root all passed. The documentation edits (110 of them) were replayed in task order over the files as they are on `main` at `d4a9ccf`, each "find" text matching exactly once. The binaries built from PRs C, E, F and G were run in a throwaway home, through the `run.sh` wrapper the tasks print: the round trips of Tasks 9 and 18, and — driven in a pty at 80x24 and 40x12, against a fake ollama and a fake omlx on scratch ports — the screens in Tasks 13 and 16, which are those captures. What was not verified is listed at the end, under "What was not verified".

## How to Read the Code Steps

A new file is shown whole. A change to a file that exists is shown as a unified diff against the PR's starting point, with paths from the monorepo root. Apply a diff either way:

- by hand: the lines starting `-` are the text to find, the lines starting `+` replace them, and the lines starting with a space are context that does not change; or
- with git, from the monorepo root: save the block's contents to a file and run `git apply <file>`. Go source is indented with tabs; copy the block exactly.

The line numbers in a diff's `@@` headers are those of `main` at `a722ddb` for a PR's first change to a file, and of the file as the plan's earlier tasks left it afterwards.

A documentation step gives each edit as the text to find and the text that replaces it. Every "find" text occurs exactly once in its file at that point in the plan.

---

## PR A — `wt stop --all`

Branch `feat/wt-stop-all`. Independent of every other PR in this plan.

modelman's `stop --all` stops every local provider. wt has `wt stop <model>`, `wt stop <provider>` and a picker, and nothing that stops everything. `wt stop omlx` already halts the omlx service as a whole (`stopProvider`, which is `lifecycle.Stop`); `--all` is that for every pool provider, after the stop loop has dealt with every other running model.

Create the branch from the monorepo root:

```bash
git fetch origin
git switch -c feat/wt-stop-all origin/main
```

### Task 1: `wt stop --all [--yes]`

**Files:**
- Modify: `wt/cmd/wt/model_cmds.go` (`stopCmd` at line 57; new `poolProviders` and `runStopAll` after it)
- Modify: `wt/cmd/wt/model_cmds_test.go` (append)
- Modify: `wt/docs/wt-start-stop.md`, `wt/CLAUDE.md`, `CLAUDE.md`, `wt/CHANGELOG.md`

**Interfaces:**
- Consumes (existing, `cmd/wt/model_cmds.go`):
  - `stopCandidates func(*config.Config) []survey.Candidate` — every running, trusted, stoppable model, in-use ones included
  - `stopEntries func(io.Writer, *config.Config, []localmodels.Entry) error` — the stop loop; one proxy restart per batch
  - `stopProvider func(context.Context, *config.Config, string) error` — halts a provider's server and clears its routes
  - `confirmStop func(question string) (bool, error)`, `stopImpact(targets []survey.Candidate) (sessions int, users []string)`, `stoppableProviders(cfg) []string`, `startSignalCtx() (context.Context, context.CancelFunc)`
  - `lifecycle.TenancyOf(providerID) lifecycle.Tenancy` (`lifecycle.Pool` for omlx and omlx-6bit), `localmodels.Family(providerID) string`
  - test helpers in `model_cmds_test.go`: `modelCmdConfig() *config.Config`, `cand(provider, id, name string, sessions int) survey.Candidate`, `stubStop(t, cands) *[]localmodels.Entry`
- Produces:
  - `func poolProviders(cfg *config.Config) []string` — one configured provider id per pool server
  - `func runStopAll(out io.Writer, cfg *config.Config, yes bool) error`
  - the `--all` flag of `wt stop`
  - test helper `stubHalt(t *testing.T, fail ...string) *[]string`

- [ ] **Step 1: Write the failing tests**

Append to `wt/cmd/wt/model_cmds_test.go`:

```diff
--- a/wt/cmd/wt/model_cmds_test.go
+++ b/wt/cmd/wt/model_cmds_test.go
@@ -614,3 +614,147 @@ func TestStopBareOmlxHaltsTheService(t *testing.T) {
 		t.Errorf("halted = %v, per-model stops = %v; want [omlx] and none", halted, *stopped)
 	}
 }
+
+// stubHalt records every provider `wt stop` halts as a whole, and fails the
+// ones named in fail.
+func stubHalt(t *testing.T, fail ...string) *[]string {
+	t.Helper()
+	var halted []string
+	old := stopProvider
+	stopProvider = func(_ context.Context, _ *config.Config, id string) error {
+		halted = append(halted, id)
+		if slices.Contains(fail, id) {
+			return errors.New("still listening")
+		}
+		return nil
+	}
+	t.Cleanup(func() { stopProvider = old })
+	return &halted
+}
+
+// TestStopAllStopsEveryModelThenHaltsThePool verifies `wt stop --all` stops
+// each running model of a non-pool provider through the stop loop and halts
+// the omlx service once, without first unloading its models one by one. A
+// user frees the machine with one command; unloading omlx's models would
+// leave the server and its memory in place.
+func TestStopAllStopsEveryModelThenHaltsThePool(t *testing.T) {
+	stopped := stubStop(t, []survey.Candidate{cand("ollama", "ollama/a:1", "a:1", 0), cand("omlx", "omlx/c", "c", 0)})
+	halted := stubHalt(t)
+	var out bytes.Buffer
+	if err := runStopAll(&out, modelCmdConfig(), false); err != nil {
+		t.Fatal(err)
+	}
+	if len(*stopped) != 1 || (*stopped)[0].ModelID != "ollama/a:1" {
+		t.Errorf("per-model stops = %v, want only ollama/a:1 (the omlx model goes down with its service)", *stopped)
+	}
+	if !slices.Equal(*halted, []string{"omlx"}) {
+		t.Errorf("halted = %v, want [omlx]", *halted)
+	}
+	if !strings.Contains(out.String(), "Stopping omlx... done") {
+		t.Errorf("out = %q, want the halt reported", out.String())
+	}
+}
+
+// TestStopAllHaltsOneServicePerPool verifies a registry with both an omlx
+// and an omlx-6bit row halts the one omlx service once. Two halts would run
+// `omlx stop` twice and report the second, on a dead port, as a failure.
+func TestStopAllHaltsOneServicePerPool(t *testing.T) {
+	stubStop(t, nil)
+	halted := stubHalt(t)
+	cfg := &config.Config{Providers: []config.Provider{
+		{ID: "omlx", Location: config.LocationLocal}, {ID: "omlx-6bit", Location: config.LocationLocal},
+	}}
+	if err := runStopAll(io.Discard, cfg, false); err != nil {
+		t.Fatal(err)
+	}
+	if !slices.Equal(*halted, []string{"omlx"}) {
+		t.Errorf("halted = %v, want [omlx] once", *halted)
+	}
+}
+
+// TestStopAllWithNothingToStop verifies `wt stop --all` on a machine with no
+// running model and no pool provider says so and exits 0, so a cleanup script
+// can call it unconditionally.
+func TestStopAllWithNothingToStop(t *testing.T) {
+	stopped := stubStop(t, nil)
+	halted := stubHalt(t)
+	cfg := &config.Config{Providers: []config.Provider{{ID: "ollama", Location: config.LocationLocal}}}
+	var out bytes.Buffer
+	if err := runStopAll(&out, cfg, false); err != nil {
+		t.Fatal(err)
+	}
+	if len(*stopped) != 0 || len(*halted) != 0 || !strings.Contains(out.String(), "no running local models") {
+		t.Errorf("stopped = %v halted = %v out = %q; want nothing stopped and a note", *stopped, *halted, out.String())
+	}
+}
+
+// TestStopAllInUseConfirms verifies `wt stop --all` asks once, with the total
+// session count, before stopping models other wt sessions are using; declining
+// stops nothing at all, and --yes skips the question. Without the question a
+// cleanup in one terminal kills the agent running in another.
+func TestStopAllInUseConfirms(t *testing.T) {
+	stopped := stubStop(t, []survey.Candidate{cand("ollama", "ollama/a:1", "a:1", 2), cand("omlx", "omlx/c", "c", 1)})
+	halted := stubHalt(t)
+	old := confirmStop
+	t.Cleanup(func() { confirmStop = old })
+
+	var asked string
+	confirmStop = func(q string) (bool, error) { asked = q; return false, nil }
+	err := runStopAll(io.Discard, modelCmdConfig(), false)
+	if err == nil || !strings.Contains(err.Error(), "nothing was stopped") {
+		t.Fatalf("declined: err = %v, want a cancellation", err)
+	}
+	if !strings.Contains(asked, "3 live wt session(s)") || !strings.Contains(asked, "ollama/a:1, omlx/c") {
+		t.Errorf("asked = %q, want the total and both models", asked)
+	}
+	if len(*stopped) != 0 || len(*halted) != 0 {
+		t.Fatalf("declined: stopped = %v halted = %v, want nothing", *stopped, *halted)
+	}
+
+	confirmStop = func(string) (bool, error) { t.Fatal("--yes must not prompt"); return false, nil }
+	if err := runStopAll(io.Discard, modelCmdConfig(), true); err != nil {
+		t.Fatal(err)
+	}
+	if len(*stopped) != 1 || !slices.Equal(*halted, []string{"omlx"}) {
+		t.Errorf("--yes: stopped = %v halted = %v, want the ollama model and the omlx halt", *stopped, *halted)
+	}
+}
+
+// TestStopAllKeepsGoingAfterAFailure verifies a failed model stop does not
+// stop `wt stop --all` from halting the pool service, and that the command
+// still exits non-zero naming what failed. "Stop everything" that gives up at
+// the first failure leaves the largest server running.
+func TestStopAllKeepsGoingAfterAFailure(t *testing.T) {
+	stubStop(t, []survey.Candidate{cand("ollama", "ollama/a:1", "a:1", 0)})
+	old := stopEntries
+	t.Cleanup(func() { stopEntries = old })
+	stopEntries = func(io.Writer, *config.Config, []localmodels.Entry) error { return errors.New("1 of 1 stops failed") }
+	halted := stubHalt(t, "omlx")
+	var out bytes.Buffer
+	err := runStopAll(&out, modelCmdConfig(), false)
+	if err == nil || !strings.Contains(err.Error(), "1 of 1 stops failed") || !strings.Contains(err.Error(), "omlx: still listening") {
+		t.Fatalf("err = %v, want both failures", err)
+	}
+	if !slices.Equal(*halted, []string{"omlx"}) || !strings.Contains(out.String(), "Stopping omlx... failed") {
+		t.Errorf("halted = %v out = %q, want the halt attempted and reported", *halted, out.String())
+	}
+}
+
+// TestStopAllTakesNoArgument verifies `wt stop --all <something>` is refused
+// before anything is probed or stopped: a user who meant one provider must
+// not have every model stopped instead.
+func TestStopAllTakesNoArgument(t *testing.T) {
+	stopped := stubStop(t, []survey.Candidate{cand("ollama", "ollama/a:1", "a:1", 0)})
+	halted := stubHalt(t)
+	cmd := stopCmd(&app{cfg: modelCmdConfig()})
+	cmd.SetArgs([]string{"--all", "ollama"})
+	cmd.SetOut(io.Discard)
+	cmd.SetErr(io.Discard)
+	err := cmd.Execute()
+	if err == nil || !strings.Contains(err.Error(), "takes no model or provider") {
+		t.Fatalf("err = %v, want the --all usage error", err)
+	}
+	if len(*stopped) != 0 || len(*halted) != 0 {
+		t.Errorf("stopped = %v halted = %v, want nothing", *stopped, *halted)
+	}
+}
```

- [ ] **Step 2: Run them to see them fail**

Run, from `wt/`: `go test -count=1 ./cmd/wt -run 'TestStopAll'`

Expected: the package does not build.

```text
FAIL	github.com/ohanaverse/local-ai-setup/wt/cmd/wt [build failed]
FAIL
# github.com/ohanaverse/local-ai-setup/wt/cmd/wt [github.com/ohanaverse/local-ai-setup/wt/cmd/wt.test]
cmd/wt/model_cmds_test.go:644:12: undefined: runStopAll
cmd/wt/model_cmds_test.go:667:12: undefined: runStopAll
```

- [ ] **Step 3: Implement `--all`**

```diff
--- a/wt/cmd/wt/model_cmds.go
+++ b/wt/cmd/wt/model_cmds.go
@@ -57,15 +57,17 @@ func promptStop(question string) (bool, error) {
 func stopCmd(a *app) *cobra.Command {
 	cmd := &cobra.Command{
 		Use:   "stop [model|provider]",
-		Short: "Stop a running local model, or every model of a provider",
+		Short: "Stop a running local model, every model of a provider, or all of them",
 		Long: "Stop a running local model (<provider>/<name>) or every running model of a\n" +
 			"provider (ollama, omlx, omlx-6bit, mtplx). With no argument, shows the\n" +
 			"stop picker (requires a TTY), which also lists models other wt sessions\n" +
 			"are using, marked with their session count.\n\n" +
 			"On omlx, stopping a model unloads that model and leaves the service and its other\n" +
 			"models up; \"wt stop omlx\" stops the service.\n\n" +
+			"--all stops every running local model and then the omlx service, on every\n" +
+			"provider at once. It takes no argument.\n\n" +
 			"Stopping a model a live wt session uses asks for confirmation; --yes skips it.",
-		Example: "  wt stop ollama/qwen3.8:27b-mlx\n  wt stop ollama\n  wt stop",
+		Example: "  wt stop ollama/qwen3.8:27b-mlx\n  wt stop ollama\n  wt stop\n  wt stop --all --yes",
 		Args:    cobra.MaximumNArgs(1),
 		RunE: func(cmd *cobra.Command, args []string) error {
 			if a.cfgErr != nil {
@@ -76,13 +78,85 @@ func stopCmd(a *app) *cobra.Command {
 				arg = args[0]
 			}
 			yes, _ := cmd.Flags().GetBool("yes")
+			if all, _ := cmd.Flags().GetBool("all"); all {
+				if arg != "" {
+					return fmt.Errorf("wt stop --all takes no model or provider (got %q)", arg)
+				}
+				return runStopAll(cmd.OutOrStdout(), a.cfg, yes)
+			}
 			return runStop(cmd.OutOrStdout(), a.cfg, arg, yes)
 		},
 	}
 	cmd.Flags().Bool("yes", false, "Skip the confirmation when a target is in use by a live wt session")
+	cmd.Flags().Bool("all", false, "Stop every running local model, then the omlx service")
 	return cmd
 }
 
+// poolProviders lists one configured provider id per pool server (omlx):
+// "omlx" and "omlx-6bit" are one service, and it is halted once.
+func poolProviders(cfg *config.Config) []string {
+	var ids []string
+	seen := map[string]bool{}
+	for _, id := range stoppableProviders(cfg) {
+		fam := localmodels.Family(id)
+		if lifecycle.TenancyOf(id) != lifecycle.Pool || seen[fam] {
+			continue
+		}
+		seen[fam] = true
+		ids = append(ids, id)
+	}
+	return ids
+}
+
+// runStopAll implements `wt stop --all`: every running local model wt can
+// stop, then each pool service as a whole. A pool's models go down with their
+// service, so they are not unloaded one by one first. Like `wt stop omlx`, it
+// halts a configured pool service even when no model is loaded: that is what
+// frees the server's memory. Every stop is attempted; a failure does not
+// leave the rest running, and the command then exits non-zero.
+func runStopAll(out io.Writer, cfg *config.Config, yes bool) error {
+	cands := stopCandidates(cfg)
+	if inUse, users := stopImpact(cands); inUse > 0 && !yes {
+		ok, err := confirmStop(fmt.Sprintf("running local models are in use by %d live wt session(s) (%s); stop them all?", inUse, strings.Join(users, ", ")))
+		if err != nil {
+			return err
+		}
+		if !ok {
+			return errors.New("cancelled — nothing was stopped")
+		}
+	}
+	var entries []localmodels.Entry
+	for _, c := range cands {
+		if lifecycle.TenancyOf(c.Entry.ProviderID) != lifecycle.Pool {
+			entries = append(entries, c.Entry)
+		}
+	}
+	pools := poolProviders(cfg)
+	if len(entries) == 0 && len(pools) == 0 {
+		fmt.Fprintln(out, "wt: no running local models")
+		return nil
+	}
+	var failures []error
+	if len(entries) > 0 {
+		if err := stopEntries(out, cfg, entries); err != nil {
+			failures = append(failures, err)
+		}
+	}
+	for _, id := range pools {
+		ctx, cancel := startSignalCtx()
+		fmt.Fprintf(out, "Stopping %s... ", id)
+		err := stopProvider(ctx, cfg, id)
+		cancel()
+		if err != nil {
+			fmt.Fprintln(out, "failed")
+			failures = append(failures, fmt.Errorf("%s: %w", id, err))
+			continue
+		}
+		fmt.Fprintln(out, "done")
+	}
+	return errors.Join(failures...)
+}
+
 // stoppableProviders lists, sorted, the configured provider ids wt can stop.
 // The bare-provider check and its error message both read it, so they cannot
 // disagree about what is valid.
```

- [ ] **Step 4: Run the tests**

Run, from `wt/`: `go test -count=1 ./cmd/wt -run 'TestStop'`

Expected: `ok  	github.com/ohanaverse/local-ai-setup/wt/cmd/wt` — the six new tests and every existing `TestStop*` test.

- [ ] **Step 5: Document it**

In `wt/docs/wt-start-stop.md`, find:

```text
wt stop <target> --yes           # skip the in-use confirmation
```

and replace it with:

```text
wt stop <target> --yes           # skip the in-use confirmation
wt stop --all                    # stop every running local model, then the omlx service
```

In `wt/docs/wt-start-stop.md`, find:

```text
- No argument: the stop picker (needs a TTY).
```

and replace it with:

```text
- `--all` stops every running local model wt can stop, on every provider,
  and then halts the omlx service as `wt stop omlx` does — also when omlx has
  nothing loaded, since that is what frees its memory. omlx's models are not
  unloaded one by one first. It asks once when any of the models is in use by
  a live wt session (`--yes` skips the question). Every stop is attempted: if
  one fails the others still run, and the exit code is 1. It takes no
  argument.
- No argument: the stop picker (needs a TTY).
```

In `wt/CLAUDE.md`, find:

```text
wt start [<id>] / wt stop [<id>|<provider>]   # local-model lifecycle (routes follow automatically)
```

and replace it with:

```text
wt start [<id>] / wt stop [<id>|<provider>|--all]   # local-model lifecycle (routes follow automatically)
```

In `CLAUDE.md`, find:

```text
`omlx stop` / `wt stop omlx` / `modelman stop --all` halts the service
```

and replace it with:

```text
`omlx stop` / `wt stop omlx` / `wt stop --all` (every running local model first) / `modelman stop --all` halts the service
```

In `wt/CHANGELOG.md`, find:

```text
## Unreleased

### Added
```

and replace it with:

```text
## Unreleased

### Added

- `wt stop --all [--yes]` stops every running local model and then halts the
  omlx service. It asks once when a live wt session is using one of them,
  keeps going when one stop fails (and then exits 1), and takes no argument.
```

- [ ] **Step 6: Verify the PR and commit**

From `wt/`:

```bash
test -z "$(gofmt -l .)" && go vet ./... && go test -count=1 ./... && make check
```

Expected: every package `ok`; `make check` ends without an error. From the monorepo root: `make test-all && make check-links`. Expected: exit 0.

```bash
git add wt/cmd/wt/model_cmds.go wt/cmd/wt/model_cmds_test.go wt/docs/wt-start-stop.md wt/CLAUDE.md CLAUDE.md wt/CHANGELOG.md
git commit -m "feat(wt): wt stop --all stops every running local model, then the omlx service"
```

- [ ] **Step 7: Hand off**

Stop here. Tell the owner the branch is ready and what `make test-all` printed. `--all` was exercised against stubs only: no model was stopped on a live provider. Push and open the PR only after the owner's OK. Suggested title: `feat(wt): wt stop --all`.

---

## PR B — `Entry.Path`/`Size`, the rows, `wt model list`

Branch `feat/wt-model-list`. Read-only: nothing in this PR writes the registry. Independent of PR A and PR D.

The pickers' rows (`internal/catalog`) hide a registered model that is not on disk, a stopped pairing, and a row whose location does not resolve. Model management has to show exactly those, because it is where they are repaired or removed. So this PR adds a registry-centric row builder, `modeladmin.Rows`, beside `catalog.Build`, both fed by the same `localmodels.Snapshot`.

Create the branch from the monorepo root:

```bash
git fetch origin
git switch -c feat/wt-model-list origin/main
```

### Task 2: The inventory says where a model is, and how big

**Files:**
- Create: `wt/internal/localmodels/inventory_path_test.go`
- Modify: `wt/internal/localmodels/sources.go:16-48` (`ollamaModelNames`), `:117-172` (`scanOmlxModels`)
- Modify: `wt/internal/localmodels/inventory.go:54-78` (`Entry`), `:173-182` (`source`), `:350-446` (`probeFamily`), `:510-576` (`inventory`'s two loops over entries)

**Interfaces:**
- Consumes (existing): `scanModelDirs(dir) ([]string, error)`, `mtplxRepoID(dirName) string`, the test helpers `mkOmlxModels`, `mkdirs`, `modelsServer`, `localProvider`, `byModelID`, `fakeOmlx`, `testClient`
- Produces:
  - `Entry.Path string` — the artifact's directory (omlx, mtplx); `""` otherwise
  - `Entry.Size int64` — bytes, from ollama's `/api/tags`; `0` when not known
  - `ollamaModels(ctx, client, url) ([]ollamaModel, error)` and `scanOmlxModelPaths(dir) (names []string, paths map[string]string, err error)`; `ollamaModelNames` and `scanOmlxModels` keep their signatures and call them

- [ ] **Step 1: Write the failing tests**

Create `wt/internal/localmodels/inventory_path_test.go`:

```go
package localmodels

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// TestInventoryEntriesCarryTheModelDirectory verifies an omlx model (flat, and
// inside an organization folder) and an mtplx model each report the directory
// the scan found them in, for a registered row and a discovered one alike.
// `wt model rm` prints that path as "the weights are still here"; a wrong or
// empty one sends the user to delete the wrong directory, or none.
func TestInventoryEntriesCarryTheModelDirectory(t *testing.T) {
	omlxDir, mtplxDir := t.TempDir(), t.TempDir()
	mkOmlxModels(t, omlxDir, "Flat-4bit", "mlx-community/Nested-6bit")
	mkdirs(t, mtplxDir, "Org--Model")
	omlx := &fakeOmlx{listed: []string{"Flat-4bit", "Nested-6bit"}, pool: map[string]bool{"Flat-4bit": false, "Nested-6bit": false}}
	mtplx := modelsServer(t)
	cfg := &config.Config{
		Providers: []config.Provider{
			localProvider("omlx", omlx.serve(t), omlxDir),
			localProvider("mtplx", mtplx.URL+"/v1", mtplxDir),
		},
		Models: []config.Model{{ID: "omlx/flat", ProviderID: "omlx", ModelName: "Flat-4bit"}},
	}
	snap := inventory(cfg, testClient)
	want := map[string]string{
		"omlx/flat":        filepath.Join(omlxDir, "Flat-4bit"),
		"omlx/Nested-6bit": filepath.Join(omlxDir, "mlx-community", "Nested-6bit"),
		"mtplx/Org/Model":  filepath.Join(mtplxDir, "Org--Model"),
	}
	for id, path := range want {
		e, ok := byModelID(snap, id)
		if !ok || e.Path != path {
			t.Errorf("%s: Path = %q ok=%v, want %q", id, e.Path, ok, path)
		}
		if e.Size != 0 {
			t.Errorf("%s: Size = %d, want 0 (no directory is walked)", id, e.Size)
		}
	}
}

// TestInventoryOllamaEntriesCarryTheirSize verifies an ollama entry's Size is
// the `size` /api/tags reports and its Path is empty: ollama keeps blobs, not
// a directory per model. `wt model list --json` reports both, and a made-up
// path would name a directory that does not exist.
func TestInventoryOllamaEntriesCarryTheirSize(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tags", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"models": []map[string]any{
			{"name": "qwen3:8b", "size": 5225388164},
			{"name": "nosize:1b"},
		}})
	})
	mux.HandleFunc("/api/ps", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"models": []any{}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	cfg := &config.Config{
		Providers: []config.Provider{localProvider("ollama", srv.URL, "")},
		Models:    []config.Model{{ID: "ollama/qwen3:8b", ProviderID: "ollama", ModelName: "qwen3:8b"}},
	}
	snap := inventory(cfg, testClient)
	if e, ok := byModelID(snap, "ollama/qwen3:8b"); !ok || e.Size != 5225388164 || e.Path != "" {
		t.Errorf("registered = %+v ok=%v, want Size 5225388164 and no Path", e, ok)
	}
	if e, ok := byModelID(snap, "ollama/nosize:1b"); !ok || e.Size != 0 {
		t.Errorf("no size reported = %+v ok=%v, want Size 0", e, ok)
	}
}

// TestInventoryMissingModelHasNoPath verifies a registered model the scan did
// not find has no Path. A path guessed from the model directory and the name
// would tell the user weights exist where there are none.
func TestInventoryMissingModelHasNoPath(t *testing.T) {
	dir := t.TempDir()
	omlx := &fakeOmlx{}
	cfg := &config.Config{
		Providers: []config.Provider{localProvider("omlx", omlx.serve(t), dir)},
		Models:    []config.Model{{ID: "omlx/gone", ProviderID: "omlx", ModelName: "Gone-4bit"}},
	}
	e, ok := byModelID(inventory(cfg, testClient), "omlx/gone")
	if !ok || e.Path != "" || e.Artifact != "" || !e.ArtifactKnown {
		t.Errorf("entry = %+v ok=%v, want a known-missing model with no Path", e, ok)
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run, from `wt/`: `go test -count=1 ./internal/localmodels -run 'TestInventory.*(Directory|Size|NoPath)'`

Expected: the package does not build.

```text
FAIL	github.com/ohanaverse/local-ai-setup/wt/internal/localmodels [build failed]
FAIL
# github.com/ohanaverse/local-ai-setup/wt/internal/localmodels [github.com/ohanaverse/local-ai-setup/wt/internal/localmodels.test]
internal/localmodels/inventory_path_test.go:39:15: e.Path undefined (type Entry has no field or method Path)
internal/localmodels/inventory_path_test.go:40:51: e.Path undefined (type Entry has no field or method Path)
```

- [ ] **Step 3: Return directories and sizes from the scans**

```diff
--- a/wt/internal/localmodels/sources.go
+++ b/wt/internal/localmodels/sources.go
@@ -13,10 +13,17 @@ import (
 	"strings"
 )
 
-// ollamaModelNames returns the names in an ollama {"models":[...]} response
-// (/api/tags: pulled models; /api/ps: loaded models). Entries with a non-empty
-// remote_host are ollama.com cloud models, not local ones, and are skipped.
-func ollamaModelNames(ctx context.Context, client *http.Client, url string) ([]string, error) {
+// ollamaModel is one entry of an ollama {"models":[...]} response.
+type ollamaModel struct {
+	Name string
+	Size int64 // bytes on disk, as /api/tags reports it; 0 when absent
+}
+
+// ollamaModels returns the local models in an ollama {"models":[...]}
+// response (/api/tags: pulled models; /api/ps: loaded models). Entries with a
+// non-empty remote_host are ollama.com cloud models, not local ones, and are
+// skipped.
+func ollamaModels(ctx context.Context, client *http.Client, url string) ([]ollamaModel, error) {
 	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
 	if err != nil {
 		return nil, err
@@ -33,17 +40,31 @@ func ollamaModelNames(ctx context.Context, client *http.Client, url string) ([]s
 		Models []struct {
 			Name       string `json:"name"`
 			RemoteHost string `json:"remote_host"`
+			Size       int64  `json:"size"`
 		} `json:"models"`
 	}
 	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
 		return nil, fmt.Errorf("decode %s: %w", url, err)
 	}
-	names := make([]string, 0, len(body.Models))
+	models := make([]ollamaModel, 0, len(body.Models))
 	for _, m := range body.Models {
 		if m.Name != "" && m.RemoteHost == "" {
-			names = append(names, m.Name)
+			models = append(models, ollamaModel{Name: m.Name, Size: m.Size})
 		}
 	}
+	return models, nil
+}
+
+// ollamaModelNames is ollamaModels' names alone.
+func ollamaModelNames(ctx context.Context, client *http.Client, url string) ([]string, error) {
+	models, err := ollamaModels(ctx, client, url)
+	if err != nil {
+		return nil, err
+	}
+	names := make([]string, len(models))
+	for i, m := range models {
+		names[i] = m.Name
+	}
 	return names, nil
 }
 
@@ -115,15 +136,22 @@ func scanModelDirs(dir string) ([]string, error) {
 // since Pool.Find, matchArtifact and the load and unload requests match on
 // them.
 func scanOmlxModels(dir string) ([]string, error) {
+	names, _, err := scanOmlxModelPaths(dir)
+	return names, err
+}
+
+// scanOmlxModelPaths is scanOmlxModels plus where each model is: paths maps
+// a listed name to its directory. A name found twice keeps the first
+// directory, the one the name was listed for.
+func scanOmlxModelPaths(dir string) (names []string, paths map[string]string, err error) {
 	tops, err := scanModelDirs(dir)
 	if err != nil {
-		return nil, err
+		return nil, nil, err
 	}
-	seen := map[string]bool{}
-	var names []string
-	add := func(name string) {
-		if !seen[name] {
-			seen[name] = true
+	paths = map[string]string{}
+	add := func(name, path string) {
+		if _, seen := paths[name]; !seen {
+			paths[name] = path
 			names = append(names, name)
 		}
 	}
@@ -142,7 +170,7 @@ func scanOmlxModels(dir string) ([]string, error) {
 		switch {
 		case adapter:
 		case model:
-			add(top)
+			add(top, p)
 		case isHFCacheEntry(p):
 			sawHFCache = true
 		default:
@@ -154,7 +182,7 @@ func scanOmlxModels(dir string) ([]string, error) {
 			}
 			for _, child := range children {
 				if model, _ := isModel(filepath.Join(p, child)); model {
-					add(child)
+					add(child, filepath.Join(p, child))
 				}
 			}
 		}
@@ -164,11 +192,11 @@ func scanOmlxModels(dir string) ([]string, error) {
 	// listing nothing is the safe answer.
 	if len(names) == 0 && !sawHFCache {
 		if model, _ := isModel(dir); model {
-			return []string{filepath.Base(dir)}, nil
+			return []string{filepath.Base(dir)}, map[string]string{filepath.Base(dir): dir}, nil
 		}
 	}
 	sort.Strings(names)
-	return names, nil
+	return names, paths, nil
 }
 
 func fileExists(p string) bool {
```

- [ ] **Step 4: Carry them on the entry**

```diff
--- a/wt/internal/localmodels/inventory.go
+++ b/wt/internal/localmodels/inventory.go
@@ -7,6 +7,7 @@ import (
 	"net"
 	"net/http"
 	"net/url"
+	"path/filepath"
 	"sort"
 	"strconv"
 	"sync"
@@ -79,6 +80,15 @@ type Entry struct {
 	// isn't there" — a transient probe failure would then hide a model that is
 	// pulled and launchable.
 	ArtifactKnown bool
+	// Path is where the artifact is on disk: the model's directory for omlx
+	// and mtplx. "" when the probe does not learn it — ollama keeps its
+	// models in a blob store with no per-model path, an mlx_lm_server pairing
+	// is never enumerated, and a model that is not on disk has none.
+	Path string
+	// Size is the artifact's size in bytes when the probe's own answer
+	// carries it: ollama's /api/tags. 0 means not known, never "empty": no
+	// directory is walked to fill it.
+	Size int64
 }
 
 // Snapshot is one inventory round. Providers is keyed by provider family
@@ -173,12 +183,14 @@ func RoutesFollowArtifact(family string) bool { return family == "ollama" }
 type source struct {
 	family     string
 	status     Status
-	artifacts  []string // discovered names, provider spelling (mtplx: repo id form)
-	loaded     []string // names serving right now
-	registered int      // local registry models in this family
-	down       bool     // the server refused the connection (nothing listening)
-	probeErr   error    // the probe failure, for a caller that shows the reason
-	pool       *Pool    // omlx only: the reading loaded came from
+	artifacts  []string          // discovered names, provider spelling (mtplx: repo id form)
+	loaded     []string          // names serving right now
+	paths      map[string]string // artifact -> its directory (omlx, mtplx)
+	sizes      map[string]int64  // artifact -> bytes (ollama)
+	registered int               // local registry models in this family
+	down       bool              // the server refused the connection (nothing listening)
+	probeErr   error             // the probe failure, for a caller that shows the reason
+	pool       *Pool             // omlx only: the reading loaded came from
 }
 
 func (s *source) matchArtifact(artifact, modelName string) bool {
@@ -352,13 +364,17 @@ func probeFamily(cfg *config.Config, client *http.Client, family string) *source
 	origin := familyOrigin(cfg, family)
 	switch family {
 	case "ollama":
-		names, err := ollamaModelNames(context.Background(), client, origin+"/api/tags")
+		models, err := ollamaModels(context.Background(), client, origin+"/api/tags")
 		if err != nil {
 			s.status = StatusUnreachable
 			s.down = refused(err)
 			return s
 		}
-		s.artifacts = names
+		s.sizes = map[string]int64{}
+		for _, m := range models {
+			s.artifacts = append(s.artifacts, m.Name)
+			s.sizes[m.Name] = m.Size
+		}
 		// Same endpoint construction as the lifecycle re-probe's OllamaLoaded,
 		// so the two always describe the same server.
 		if loaded, err := OllamaLoaded(context.Background(), client, origin); err == nil {
@@ -414,21 +430,24 @@ func probeFamily(cfg *config.Config, client *http.Client, family string) *source
 			return s
 		}
 		// omlx discovers models two levels deep; mtplx's directory is flat.
-		scan := scanOmlxModels
+		var names []string
+		var paths map[string]string
 		if family == "mtplx" {
-			scan = scanModelDirs
+			var dirs []string
+			dirs, err = scanModelDirs(dir)
+			paths = map[string]string{}
+			for _, n := range dirs {
+				names = append(names, mtplxRepoID(n))
+				paths[mtplxRepoID(n)] = filepath.Join(dir, n)
+			}
+		} else {
+			names, paths, err = scanOmlxModelPaths(dir)
 		}
-		names, err := scan(dir)
 		if err != nil {
 			s.status = StatusUnreachable
 			return s
 		}
-		if family == "mtplx" {
-			for i, n := range names {
-				names[i] = mtplxRepoID(n)
-			}
-		}
-		s.artifacts = names
+		s.artifacts, s.paths = names, paths
 	case "mlx_lm_server":
 		// Running state only: a target+draft pairing cannot be enumerated
 		// (knowsArtifacts stays false), but /v1/models still says whether the
@@ -520,6 +539,7 @@ func inventory(cfg *config.Config, client *http.Client) Snapshot {
 				if !consumed[key] && src.matchArtifact(a, m.ModelName) {
 					consumed[key] = true
 					e.Artifact = a
+					e.Path, e.Size = src.paths[a], src.sizes[a]
 					break
 				}
 			}
@@ -571,6 +591,8 @@ func inventory(cfg *config.Config, client *http.Client) Snapshot {
 				Running:       running,
 				Loading:       running && src.isLoading(a),
 				ArtifactKnown: true,
+				Path:          src.paths[a],
+				Size:          src.sizes[a],
 			})
 		}
 	}
```

- [ ] **Step 5: Run the package**

Run, from `wt/`: `go test -count=1 ./internal/localmodels`

Expected: `ok  	github.com/ohanaverse/local-ai-setup/wt/internal/localmodels` — the three new tests and every existing inventory and scan test, unedited.

- [ ] **Step 6: Commit**

```bash
git add wt/internal/localmodels
git commit -m "feat(wt): the local inventory reports a model's directory and its size"
```

### Task 3: wt reads a model's `fetch` and `draft`

**Files:**
- Create: `wt/internal/config/model_artifact_test.go`
- Modify: `wt/internal/config/config.go:466-482` (`Model`)
- Modify: `docs/contracts/registry.sample.toml` (one comment)

**Interfaces:**
- Consumes (existing): `loadRegistry() ([]Provider, []Model, error)`, `IndexModelByID`, the test helper `writeRegistry(t, dir, content)` in `registry_test.go`
- Produces:
  - `type ModelArtifact struct { Repo string; LocalPath string }` with `func (a ModelArtifact) Target() string` — the local path when there is one, else the repo
  - `Model.Fetch ModelArtifact` (`toml:"fetch,omitempty"`), `Model.Draft ModelArtifact` (`toml:"draft,omitempty"`)

- [ ] **Step 1: Write the failing test**

Create `wt/internal/config/model_artifact_test.go`:

```go
package config

import "testing"

// TestModelDecodesFetchAndDraft verifies wt reads a model's fetch and draft
// tables: the repo of each side of the shared fixture's mlx_lm_server pairing,
// and a local_path. `wt model list` shows a local_path as the model's path and
// the pairing hint names the target and the draft; decoded as empty, the hint
// would tell the user to start a pairing with no target.
func TestModelDecodesFetchAndDraft(t *testing.T) {
	t.Setenv("WT_REGISTRY", "")
	t.Setenv("MODELMAN_REGISTRY", "../../../docs/contracts/registry.sample.toml")
	_, models, err := loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	i := IndexModelByID(models, "mlx_lm_server/contract-fixture:pair")
	if i < 0 {
		t.Fatal("the fixture has no mlx_lm_server pairing")
	}
	if got := models[i].Fetch.Target(); got != "org/contract-fixture-target" {
		t.Errorf("Fetch.Target() = %q, want the fixture's target repo", got)
	}
	if got := models[i].Draft.Target(); got != "org/contract-fixture-draft" {
		t.Errorf("Draft.Target() = %q, want the fixture's draft repo", got)
	}

	writeRegistry(t, t.TempDir(), `
[[providers]]
id = "omlx"
location = "local"
[providers.auth]
type = "none"

[[models]]
id = "omlx/mine"
family = "qwen"
provider_id = "omlx"
model_name = "mine-4bit"
[models.fetch]
repo = "org/base"
local_path = "~/models/mine-4bit"
files = ["a", "b"]
`)
	t.Setenv("MODELMAN_REGISTRY", "")
	_, models, err = loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if got := models[0].Fetch; got.LocalPath != "~/models/mine-4bit" || got.Target() != "~/models/mine-4bit" || got.Repo != "org/base" {
		t.Errorf("Fetch = %+v, want the local path to win over the repo", got)
	}
	if got := models[0].Draft.Target(); got != "" {
		t.Errorf("Draft.Target() = %q on a model with no draft, want \"\"", got)
	}
}
```

- [ ] **Step 2: Run it to see it fail**

Run, from `wt/`: `go test -count=1 ./internal/config -run TestModelDecodesFetchAndDraft`

Expected: the package does not build.

```text
FAIL	github.com/ohanaverse/local-ai-setup/wt/internal/config [build failed]
FAIL
# github.com/ohanaverse/local-ai-setup/wt/internal/config [github.com/ohanaverse/local-ai-setup/wt/internal/config.test]
internal/config/model_artifact_test.go:21:22: models[i].Fetch undefined (type Model has no field or method Fetch)
internal/config/model_artifact_test.go:24:22: models[i].Draft undefined (type Model has no field or method Draft)
```

- [ ] **Step 3: Add the two fields**

```diff
--- a/wt/internal/config/config.go
+++ b/wt/internal/config/config.go
@@ -476,7 +476,32 @@ type Model struct {
 	// LiteLLM rows merge it over the derived pricing keys, so hand-written
 	// keys (context windows, capability flags) reach the proxy.
 	ModelInfo map[string]any `toml:"model_info,omitempty"`
-	Native    bool           `toml:"-"` // derived: provider auth.type == "native"; not persisted
+	// Fetch and Draft say where a local model's weights come from: the
+	// registry's [models.fetch] table, and [models.draft] for the draft half
+	// of an mlx_lm_server pairing. wt only reads them — to show a local_path
+	// and to name the pairing in a hint; llmbench acts on them. Nothing
+	// encodes a Model back into the registry (RegistryDoc is patch-shaped),
+	// so decoding two more keys cannot change what a write touches.
+	Fetch  ModelArtifact `toml:"fetch,omitempty"`
+	Draft  ModelArtifact `toml:"draft,omitempty"`
+	Native bool          `toml:"-"` // derived: provider auth.type == "native"; not persisted
+}
+
+// ModelArtifact is one side of a model's weights: a Hugging Face repo, or a
+// directory the user produced (bin/mlx-quantize). The other keys a fetch
+// table can hold (files, quantizations) are not decoded.
+type ModelArtifact struct {
+	Repo      string `toml:"repo,omitempty"`
+	LocalPath string `toml:"local_path,omitempty"`
+}
+
+// Target is the artifact as a user names it on a command line: the local
+// path when there is one, else the repo. "" when the table is empty.
+func (a ModelArtifact) Target() string {
+	if a.LocalPath != "" {
+		return a.LocalPath
+	}
+	return a.Repo
 }
 
 // ── Agent ─────────────────────────────────────────────────
```

- [ ] **Step 4: Run the package**

Run, from `wt/`: `go test -count=1 ./internal/config`

Expected: `ok  	github.com/ohanaverse/local-ai-setup/wt/internal/config`. `TestAWriteNeverTouchesAKeyItWasNotAskedTo` is among the tests that pass: it builds a row from `Model`'s struct tags, so it now covers `fetch` and `draft` without an edit.

- [ ] **Step 5: Correct the fixture's comment**

The shared fixture says wt ignores the two tables. It is a comment only; no test reads it.

In `docs/contracts/registry.sample.toml`, find:

```text
# rows (fetch/draft are modelman-only and ignored by wt's parser).
```

and replace it with:

```text
# rows, and reads `repo` and `local_path` of fetch and of draft to show them
# and to name the pairing in a hint; it never writes either table.
```

- [ ] **Step 6: Commit**

```bash
git add wt/internal/config/config.go wt/internal/config/model_artifact_test.go docs/contracts/registry.sample.toml
git commit -m "feat(wt): config.Model reads a model's fetch and draft tables"
```

### Task 4: `modeladmin.Rows`

**Files:**
- Create: `wt/internal/modeladmin/testmain_test.go`, `wt/internal/modeladmin/rows_test.go`
- Create: `wt/internal/modeladmin/rows.go`

**Interfaces:**
- Consumes: `localmodels.Snapshot` and `Entry` (with `Path`, `Size` from Task 2), `config.Model.Fetch`/`Draft` (Task 3), `(*config.Config).ResolveLocation`, `config.ExpandHome`, `lifecycle.ProbeTrusted(snap, family) bool`, `localmodels.Family`, `localmodels.RunningOnly`
- Produces (package `modeladmin`):
  - `type Status string` with `StatusOK = "ok"`, `StatusMissing = "missing"`, `StatusUnknown = "unknown"`, `StatusNew = "new"`, `StatusNone = "-"`
  - `RunningRun = "run"`, `RunningLoad = "load"`, `RunningNo = ""`, `RunningUnknown = "?"`
  - `type Row struct { ID, Family, ProviderID, ModelName string; Tags []string; Location string; Registered bool; Status Status; Running string; Path string; Size int64; Cost config.ModelCost; Target, Draft string }` and `func (r Row) Pairing() bool`
  - `func Rows(cfg *config.Config, snap localmodels.Snapshot) []Row`
  - `func WeightsNote(r Row) string` — where a removed row's weights are; `""` for a cloud row
  - `func FormatSize(n int64) string` — `"5.2 GB"`, `"-"` for 0
  - the seam `statLocalPath func(path string) bool`

- [ ] **Step 1: Write the package's `TestMain` and the failing tests**

Create `wt/internal/modeladmin/testmain_test.go`. The package writes the registry from Task 7 on, so it is isolated from the start:

```go
package modeladmin

import (
	"os"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// TestMain keeps this package's tests off the developer's machine: the
// writes it tests go through config.UpdateRegistry, and without the throwaway
// config home (and the write guard it arms) a test that forgot to redirect
// the registry would edit the real one.
func TestMain(m *testing.M) {
	_, cleanup := config.IsolateConfigHomeForTest()
	code := m.Run()
	cleanup()
	os.Exit(code)
}

// TestRegistryWriteGuardIsArmedHere verifies this test binary cannot write
// the developer's registry. The guard is off unless a TestMain arms it, so a
// package that reaches the writer and forgot would fail open.
func TestRegistryWriteGuardIsArmedHere(t *testing.T) {
	if !config.RegistryWriteGuardArmed() {
		t.Fatal("the registry write guard is not armed: TestMain must call config.IsolateConfigHomeForTest")
	}
}
```

Create `wt/internal/modeladmin/rows_test.go`:

```go
package modeladmin

import (
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

func local(id string) config.Provider {
	return config.Provider{ID: id, Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}}
}

// rowsConfig is a registry with one model of each kind model management
// lists: cloud, omlx on disk, omlx missing, ollama, a local_path model, a
// pairing, a model of a provider wt has no probe for, and one whose location
// does not resolve.
func rowsConfig() *config.Config {
	return &config.Config{
		Providers: []config.Provider{
			local("ollama"), local("omlx"), local("mlx_lm_server"), local("llamacpp"),
			{ID: "openrouter", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "api_key"}},
			{ID: "nowhere", Auth: config.AuthConfig{Type: "none"}},
		},
		Models: []config.Model{
			{ID: "openrouter/qwen--qwen3.8", Family: "qwen", ProviderID: "openrouter", ModelName: "qwen/qwen3.8", Tags: []string{"code"}},
			{ID: "omlx/Qwen-4bit", Family: "qwen", ProviderID: "omlx", ModelName: "Qwen-4bit"},
			{ID: "omlx/Gone-4bit", Family: "qwen", ProviderID: "omlx", ModelName: "Gone-4bit"},
			{ID: "ollama/gemma4:9b", Family: "gemma", ProviderID: "ollama", ModelName: "gemma4:9b"},
			{ID: "omlx/mine", Family: "qwen", ProviderID: "omlx", ModelName: "mine-4bit", Fetch: config.ModelArtifact{LocalPath: "/models/mine-4bit"}},
			{ID: "mlx_lm_server/T+draft-D", Family: "qwen", ProviderID: "mlx_lm_server", ModelName: "T+draft-D",
				Fetch: config.ModelArtifact{Repo: "org/T"}, Draft: config.ModelArtifact{LocalPath: "/models/D"}},
			{ID: "llamacpp/old", Family: "old", ProviderID: "llamacpp", ModelName: "old.gguf"},
			{ID: "nowhere/x", Family: "zz", ProviderID: "nowhere", ModelName: "x"},
		},
	}
}

func rowsSnapshot() localmodels.Snapshot {
	return localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK, "ollama": localmodels.StatusOK, "mlx_lm_server": localmodels.StatusOK},
		Down:      map[string]bool{}, Ambiguous: map[string]bool{},
		Entries: []localmodels.Entry{
			{ProviderID: "omlx", ModelID: "omlx/Qwen-4bit", ModelName: "Qwen-4bit", Artifact: "Qwen-4bit", Registered: true, ArtifactKnown: true, Running: true, Path: "/omlx/Qwen-4bit"},
			{ProviderID: "omlx", ModelID: "omlx/Gone-4bit", ModelName: "Gone-4bit", Registered: true, ArtifactKnown: true},
			{ProviderID: "omlx", ModelID: "omlx/mine", ModelName: "mine-4bit", Registered: true, ArtifactKnown: true, Running: true, Loading: true},
			{ProviderID: "ollama", ModelID: "ollama/gemma4:9b", ModelName: "gemma4:9b", Artifact: "gemma4:9b", Registered: true, ArtifactKnown: true, Size: 5_000_000_000},
			{ProviderID: "mlx_lm_server", ModelID: "mlx_lm_server/T+draft-D", ModelName: "T+draft-D", Registered: true},
			{ProviderID: "ollama", ModelID: "ollama/found:1b", ModelName: "found:1b", Artifact: "found:1b", ArtifactKnown: true, Size: 7},
			{ProviderID: "omlx", ModelID: "omlx/Found-8bit", ModelName: "Found-8bit", Artifact: "Found-8bit", ArtifactKnown: true, Path: "/omlx/Found-8bit"},
		},
	}
}

func rowByID(t *testing.T, rows []Row, id string) Row {
	t.Helper()
	for _, r := range rows {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("no row %q in %d rows", id, len(rows))
	return Row{}
}

// TestRowsListEveryModelWithItsStatus verifies one row per registry model
// and per discovered model, each with the status, running state, path and
// size the spec gives it. This table is all `wt model list` and the Models
// tab show; a row left out here is a model the user cannot see, edit or
// remove.
func TestRowsListEveryModelWithItsStatus(t *testing.T) {
	old := statLocalPath
	statLocalPath = func(p string) bool { return p == "/models/mine-4bit" }
	t.Cleanup(func() { statLocalPath = old })

	rows := Rows(rowsConfig(), rowsSnapshot())
	if len(rows) != 10 {
		t.Fatalf("%d rows, want the 8 registry models and the 2 discovered ones", len(rows))
	}
	cases := []struct {
		id      string
		loc     string
		status  Status
		running string
		path    string
		size    int64
	}{
		{"openrouter/qwen--qwen3.8", "cloud", StatusOK, RunningNo, "", 0},
		{"omlx/Qwen-4bit", "local", StatusOK, RunningRun, "/omlx/Qwen-4bit", 0},
		{"omlx/Gone-4bit", "local", StatusMissing, RunningNo, "", 0},
		{"ollama/gemma4:9b", "local", StatusOK, RunningNo, "", 5_000_000_000},
		// A local_path row: the scan cannot find it, so presence is a stat
		// and the path is the registry's own.
		{"omlx/mine", "local", StatusOK, RunningLoad, "/models/mine-4bit", 0},
		{"mlx_lm_server/T+draft-D", "local", StatusNone, RunningNo, "", 0},
		// No probe exists for llamacpp: nothing can vouch for it.
		{"llamacpp/old", "local", StatusUnknown, RunningUnknown, "", 0},
		// The location does not resolve: listed so it can be repaired.
		{"nowhere/x", "", StatusUnknown, RunningNo, "", 0},
		{"ollama/found:1b", "local", StatusNew, RunningNo, "", 7},
		{"omlx/Found-8bit", "local", StatusNew, RunningNo, "/omlx/Found-8bit", 0},
	}
	for _, c := range cases {
		r := rowByID(t, rows, c.id)
		if r.Location != c.loc || r.Status != c.status || r.Running != c.running || r.Path != c.path || r.Size != c.size {
			t.Errorf("%s = loc %q status %q running %q path %q size %d; want %q %q %q %q %d",
				c.id, r.Location, r.Status, r.Running, r.Path, r.Size, c.loc, c.status, c.running, c.path, c.size)
		}
		if want := c.status != StatusNew; r.Registered != want {
			t.Errorf("%s: Registered = %v, want %v", c.id, r.Registered, want)
		}
	}
	pair := rowByID(t, rows, "mlx_lm_server/T+draft-D")
	if !pair.Pairing() || pair.Target != "org/T" || pair.Draft != "/models/D" {
		t.Errorf("pairing = %+v, want target org/T and draft /models/D", pair)
	}
	if rows[0].Family != "gemma" || rows[len(rows)-2].ID != "ollama/found:1b" {
		t.Errorf("order: first family %q, second-last id %q; want registry rows by family, discovered rows last", rows[0].Family, rows[len(rows)-2].ID)
	}
}

// TestRowsMarkAnUntrustedProbe verifies the rows of a family whose probe
// failed: status unknown when discovery failed, and running "?" unless the
// server refused the connection. A blank RUNNING cell there would say
// "stopped" about a model that may be serving.
func TestRowsMarkAnUntrustedProbe(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.Provider{local("ollama"), local("omlx"), local("mlx_lm_server")},
		Models: []config.Model{
			{ID: "ollama/a:1", Family: "a", ProviderID: "ollama", ModelName: "a:1"},
			{ID: "omlx/b", Family: "b", ProviderID: "omlx", ModelName: "b"},
			{ID: "mlx_lm_server/p", Family: "p", ProviderID: "mlx_lm_server", ModelName: "p"},
		},
	}
	snap := localmodels.Snapshot{
		Providers: map[string]localmodels.Status{
			"ollama": localmodels.StatusUnreachable, "omlx": localmodels.StatusPartial, "mlx_lm_server": localmodels.StatusPartial,
		},
		Down:      map[string]bool{"omlx": true},
		Ambiguous: map[string]bool{"mlx_lm_server": true},
		Entries: []localmodels.Entry{
			{ProviderID: "ollama", ModelID: "ollama/a:1", ModelName: "a:1", Registered: true},
			{ProviderID: "omlx", ModelID: "omlx/b", ModelName: "b", Artifact: "b", Registered: true, ArtifactKnown: true},
			{ProviderID: "mlx_lm_server", ModelID: "mlx_lm_server/p", ModelName: "p", Registered: true},
		},
	}
	rows := Rows(cfg, snap)
	if r := rowByID(t, rows, "ollama/a:1"); r.Status != StatusUnknown || r.Running != RunningUnknown {
		t.Errorf("unreachable ollama = %q / %q, want unknown / ?", r.Status, r.Running)
	}
	if r := rowByID(t, rows, "omlx/b"); r.Status != StatusOK || r.Running != RunningNo {
		t.Errorf("omlx that refused the connection = %q / %q, want ok and not running", r.Status, r.Running)
	}
	if r := rowByID(t, rows, "mlx_lm_server/p"); r.Status != StatusNone || r.Running != RunningUnknown {
		t.Errorf("ambiguous pairing = %q / %q, want - / ?", r.Status, r.Running)
	}
}

// TestWeightsNote verifies what `wt model rm` tells the user is left behind
// for each kind of row. wt never deletes weights, so this line is the only
// thing between a removed row and tens of gigabytes nobody remembers.
func TestWeightsNote(t *testing.T) {
	old := statLocalPath
	statLocalPath = func(string) bool { return false }
	t.Cleanup(func() { statLocalPath = old })
	rows := Rows(rowsConfig(), rowsSnapshot())
	want := map[string]string{
		"openrouter/qwen--qwen3.8": "",
		"omlx/Qwen-4bit":           "weights are still at /omlx/Qwen-4bit",
		"omlx/Gone-4bit":           "no weights were found on disk",
		"ollama/gemma4:9b":         "still pulled in ollama (`ollama rm gemma4:9b` deletes it)",
		"omlx/mine":                "nothing was found at /models/mine-4bit",
		"mlx_lm_server/T+draft-D":  "target org/T and draft /models/D are untouched",
		"llamacpp/old":             "wt could not tell where its weights are",
		"nowhere/x":                "",
	}
	for id, note := range want {
		if got := WeightsNote(rowByID(t, rows, id)); got != note {
			t.Errorf("WeightsNote(%s) = %q, want %q", id, got, note)
		}
	}
}

// TestFormatSize pins the sizes the listing and the Models tab print:
// decimal units as `ollama list` shows them, and "-" for a size wt does not
// know, never "0 B".
func TestFormatSize(t *testing.T) {
	for n, want := range map[int64]string{0: "-", -1: "-", 7: "7 B", 4_200: "4 kB", 734_000_000: "734 MB", 5_225_388_164: "5.2 GB", 27_000_000_000: "27.0 GB"} {
		if got := FormatSize(n); got != want {
			t.Errorf("FormatSize(%d) = %q, want %q", n, got, want)
		}
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run, from `wt/`: `go test -count=1 ./internal/modeladmin`

Expected: the package does not build.

```text
FAIL	github.com/ohanaverse/local-ai-setup/wt/internal/modeladmin [build failed]
FAIL
# github.com/ohanaverse/local-ai-setup/wt/internal/modeladmin [github.com/ohanaverse/local-ai-setup/wt/internal/modeladmin.test]
internal/modeladmin/rows_test.go:55:35: undefined: Row
internal/modeladmin/rows_test.go:63:9: undefined: Row
internal/modeladmin/rows_test.go:72:9: undefined: statLocalPath
```

- [ ] **Step 3: Write the rows**

Create `wt/internal/modeladmin/rows.go`:

```go
// Package modeladmin is the core of model management, shared by the `wt
// model` commands and the Models tab of `wt config`: the rows both list, the
// rules both validate with, and the registry writes both make. It imports no
// UI package, so the two cannot drift apart.
package modeladmin

import (
	"fmt"
	"os"
	"sort"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// Status says whether a row's model is there to be used.
type Status string

const (
	// StatusOK: a cloud model, or a local model whose weights the probe found.
	StatusOK Status = "ok"
	// StatusMissing: a registered local model the probe looked for and did
	// not find, or a local_path that is not there.
	StatusMissing Status = "missing"
	// StatusUnknown: the probe could not say — the provider was unreachable,
	// wt has no probe for it, or the row's location does not resolve.
	StatusUnknown Status = "unknown"
	// StatusNew: on disk (or pulled) and not in the registry.
	StatusNew Status = "new"
	// StatusNone: an mlx_lm_server pairing, which no probe can enumerate.
	StatusNone Status = "-"
)

// The values of Row.Running.
const (
	RunningRun     = "run"  // serving now
	RunningLoad    = "load" // omlx is still loading it (#259)
	RunningNo      = ""     // not running, or a cloud model
	RunningUnknown = "?"    // the probe could not say
)

// Row is one model as model management lists it: a registry model, or a
// local model the probe found that the registry does not have.
type Row struct {
	ID         string
	Family     string
	ProviderID string
	ModelName  string
	Tags       []string
	// Location is "local" or "cloud", or "" for a registry row whose location
	// does not resolve (a gap the user repairs with `wt model edit`).
	Location   string
	Registered bool
	Status     Status
	Running    string
	// Path is where the weights are: the model's directory (omlx, mtplx), or
	// the row's fetch.local_path. "" when wt does not know one.
	Path string
	// Size is the weights' size in bytes when the probe reports one (ollama);
	// 0 when it does not.
	Size int64
	Cost config.ModelCost
	// Target and Draft are the two sides of an mlx_lm_server pairing, each a
	// repo or a path; both "" for any other model.
	Target string
	Draft  string
}

// Pairing reports whether the row is an mlx_lm_server target+draft pairing.
func (r Row) Pairing() bool { return localmodels.RunningOnly(r.ProviderID) }

// statLocalPath reports whether a fetch.local_path is there. A seam: the
// answer is the file system's, and tests describe one.
var statLocalPath = func(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Rows lists every registry model and every discovered local model, from one
// inventory snapshot. Unlike the pickers' rows (internal/catalog) nothing is
// hidden: a registered model that is not on disk, a stopped pairing and a row
// whose location does not resolve are all listed, because this is where the
// user repairs or removes them. Registry rows come first, by family and id;
// discovered rows follow, by id.
func Rows(cfg *config.Config, snap localmodels.Snapshot) []Row {
	entries := map[string]localmodels.Entry{}
	for _, e := range snap.Entries {
		if e.Registered {
			entries[e.ModelID] = e
		}
	}
	var rows []Row
	for _, m := range cfg.Models {
		r := Row{
			ID: m.ID, Family: m.Family, ProviderID: m.ProviderID, ModelName: m.ModelName,
			Tags: m.Tags, Registered: true, Cost: m.Cost,
		}
		loc, err := cfg.ResolveLocation(m)
		switch {
		case err != nil:
			r.Status = StatusUnknown
		case loc == config.LocationCloud:
			r.Location, r.Status = string(loc), StatusOK
		default:
			r.Location = string(loc)
			e, probed := entries[m.ID]
			r.Running = running(snap, e, probed)
			switch {
			case r.Pairing():
				r.Status = StatusNone
				r.Target, r.Draft = m.Fetch.Target(), m.Draft.Target()
			case m.Fetch.LocalPath != "":
				// The user's own directory, outside the provider's model
				// directory: the scan cannot find it, a stat can.
				r.Path = m.Fetch.LocalPath
				if p, err := config.ExpandHome(m.Fetch.LocalPath); err == nil {
					r.Path = p
				}
				r.Status = StatusMissing
				if statLocalPath(r.Path) {
					r.Status = StatusOK
				}
			case !probed, !e.ArtifactKnown:
				r.Status = StatusUnknown
			case e.Artifact == "":
				r.Status = StatusMissing
			default:
				r.Status, r.Path, r.Size = StatusOK, e.Path, e.Size
			}
		}
		rows = append(rows, r)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Family != rows[j].Family {
			return rows[i].Family < rows[j].Family
		}
		return rows[i].ID < rows[j].ID
	})
	var found []Row
	for _, e := range snap.Entries {
		if e.Registered {
			continue
		}
		found = append(found, Row{
			ID: e.ModelID, ProviderID: e.ProviderID, ModelName: e.ModelName,
			Location: string(config.LocationLocal), Status: StatusNew,
			Running: running(snap, e, true), Path: e.Path, Size: e.Size,
		})
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].ID < found[j].ID })
	return append(rows, found...)
}

// running is a local row's RUNNING cell. A family whose probe did not fully
// succeed reports Running flags nobody confirmed, so the cell is "?" rather
// than a blank that reads as "stopped" — except a server that refused the
// connection, where nothing is listening and "not running" is a fact.
func running(snap localmodels.Snapshot, e localmodels.Entry, probed bool) string {
	if !probed {
		return RunningUnknown
	}
	fam := localmodels.Family(e.ProviderID)
	switch {
	case e.Running && e.Loading:
		return RunningLoad
	case e.Running:
		return RunningRun
	case !lifecycle.ProbeTrusted(snap, fam) && !snap.Down[fam]:
		return RunningUnknown
	}
	return RunningNo
}

// WeightsNote says where a row's weights are, for the command that just
// removed it from the registry: wt never deletes weights, so the user is told
// what is left. "" for a row with nothing on this machine (a cloud model).
func WeightsNote(r Row) string {
	switch {
	case r.Location != string(config.LocationLocal):
		return ""
	case r.Pairing():
		return "target " + orDash(r.Target) + " and draft " + orDash(r.Draft) + " are untouched"
	case r.Path != "" && r.Status == StatusMissing:
		return "nothing was found at " + r.Path
	case r.Path != "":
		return "weights are still at " + r.Path
	case localmodels.Family(r.ProviderID) == "ollama" && r.Status != StatusMissing:
		return "still pulled in ollama (`ollama rm " + r.ModelName + "` deletes it)"
	case r.Status == StatusMissing:
		return "no weights were found on disk"
	}
	return "wt could not tell where its weights are"
}

// FormatSize renders a byte count the way `ollama list` does: decimal units,
// one decimal place from a gigabyte up. 0 (not known) is "-".
func FormatSize(n int64) string {
	switch {
	case n <= 0:
		return "-"
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.1f GB", float64(n)/1e9)
	case n >= 1_000_000:
		return fmt.Sprintf("%.0f MB", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.0f kB", float64(n)/1e3)
	}
	return fmt.Sprintf("%d B", n)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
```

- [ ] **Step 4: Run the package**

Run, from `wt/`: `go test -count=1 ./internal/modeladmin`

Expected: `ok  	github.com/ohanaverse/local-ai-setup/wt/internal/modeladmin`

- [ ] **Step 5: Commit**

```bash
git add wt/internal/modeladmin
git commit -m "feat(wt): modeladmin.Rows lists every registry model and every discovered one"
```

### Task 5: One plain table, and `wt model list [--json]`

**Files:**
- Create: `wt/cmd/wt/model_list_test.go`
- Create: `wt/cmd/wt/plain_table.go`, `wt/cmd/wt/model_list.go`
- Modify: `wt/cmd/wt/stats_usage_table.go:13` (`usageHeaders`), `:86-143` (`renderUsageTable`)
- Modify: `wt/cmd/wt/model.go:54` (`modelCmd`), `:92-93` (where `init` is added to the group)
- Create: `wt/docs/wt-model.md`
- Modify: `wt/CLAUDE.md`, `wt/docs/internals/local-models.md`, `wt/CHANGELOG.md`

**Interfaces:**
- Consumes: `modeladmin.Rows`, `modeladmin.FormatSize` (Task 4); existing in `cmd/wt`: `probeInventory func(*config.Config) localmodels.Snapshot`, `stdoutWidth func() int`, `visibleID(id) string`, `configError(err) error`, `app.loadErr`, the test helper `stubProbeInventory(t, snap)`
- Produces:
  - `type plainColumn struct { head string; right bool }`, `const plainGap = "  "`
  - `func renderPlainTable(cols []plainColumn, rows [][]string, width int) string` — borderless; the first column is the row's key and is never truncated
  - `func plainTableWidth(cols []plainColumn, rows [][]string) (key, rest int)`
  - `func runModelList(out, errOut io.Writer, cfg *config.Config, asJSON bool, width int) error`
  - `func modelListCmd(a *app) *cobra.Command`, registered under `wt model`
  - `modelCmd`'s parameter is now named `a`

- [ ] **Step 1: Write the failing tests**

Create `wt/cmd/wt/model_list_test.go`:

```go
package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// modelListConfig is a registry with the row shapes a real one has: a long
// cloud id, a running omlx model, a missing one, an ollama model with a size,
// and a pairing.
func modelListConfig() *config.Config {
	local := func(id string) config.Provider {
		return config.Provider{ID: id, Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}}
	}
	return &config.Config{
		Providers: []config.Provider{
			local("ollama"), local("omlx"), local("mlx_lm_server"),
			{ID: "openrouter", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "api_key"}},
		},
		Models: []config.Model{
			{ID: "openrouter/anthropic--claude-sonnet-4.5-thinking", Family: "sonnet", ProviderID: "openrouter", ModelName: "anthropic/claude-sonnet-4.5-thinking", Tags: []string{"code"}},
			{ID: "omlx/Qwen3.8-27B-Instruct-MLX-6bit", Family: "qwen3.8", ProviderID: "omlx", ModelName: "Qwen3.8-27B-Instruct-MLX-6bit"},
			{ID: "omlx/Gone-4bit", Family: "qwen3.8", ProviderID: "omlx", ModelName: "Gone-4bit"},
			{ID: "ollama/gemma4:9b", Family: "gemma4", ProviderID: "ollama", ModelName: "gemma4:9b"},
			{ID: "mlx_lm_server/Qwen3.8-27B+draft-Qwen3.8-4B", Family: "qwen3.8", ProviderID: "mlx_lm_server", ModelName: "Qwen3.8-27B+draft-Qwen3.8-4B",
				Fetch: config.ModelArtifact{Repo: "mlx-community/Qwen3.8-27B"}, Draft: config.ModelArtifact{Repo: "mlx-community/Qwen3.8-4B"}},
		},
	}
}

func modelListSnapshot() localmodels.Snapshot {
	return localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK, "ollama": localmodels.StatusOK, "mlx_lm_server": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "omlx", ModelID: "omlx/Qwen3.8-27B-Instruct-MLX-6bit", ModelName: "Qwen3.8-27B-Instruct-MLX-6bit", Artifact: "Qwen3.8-27B-Instruct-MLX-6bit",
				Registered: true, ArtifactKnown: true, Running: true, Path: "/Users/dev/.omlx/models/Qwen3.8-27B-Instruct-MLX-6bit"},
			{ProviderID: "omlx", ModelID: "omlx/Gone-4bit", ModelName: "Gone-4bit", Registered: true, ArtifactKnown: true},
			{ProviderID: "ollama", ModelID: "ollama/gemma4:9b", ModelName: "gemma4:9b", Artifact: "gemma4:9b", Registered: true, ArtifactKnown: true, Size: 5_800_000_000},
			{ProviderID: "mlx_lm_server", ModelID: "mlx_lm_server/Qwen3.8-27B+draft-Qwen3.8-4B", ModelName: "Qwen3.8-27B+draft-Qwen3.8-4B", Registered: true},
			{ProviderID: "ollama", ModelID: "ollama/qwen3:8b", ModelName: "qwen3:8b", Artifact: "qwen3:8b", ArtifactKnown: true, Size: 5_200_000_000},
		},
	}
}

// TestModelListFitsEightyColumns is the spec's measurement: at 80 columns
// the listing has no borders, no line is wider than the terminal, no id is
// cut (an id too long for the MODEL column gets a line of its own, above its
// cells), the five columns that say what a row is are all there with SIZE,
// and PATH, which does not fit, is left to --json and named on stderr. A
// bordered table of these rows is over 150 columns wide and wraps into an
// unreadable grid.
func TestModelListFitsEightyColumns(t *testing.T) {
	stubProbeInventory(t, modelListSnapshot())
	var out, errOut bytes.Buffer
	if err := runModelList(&out, &errOut, modelListConfig(), false, 80); err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"MODEL                                   FAMILY   LOC    STATUS   RUNNING    SIZE",
		"ollama/gemma4:9b                        gemma4   local  ok                5.8 GB",
		"mlx_lm_server/Qwen3.8-27B+draft-Qwen3.8-4B",
		"                                        qwen3.8  local  -                      -",
		"omlx/Gone-4bit                          qwen3.8  local  missing                -",
		"omlx/Qwen3.8-27B-Instruct-MLX-6bit      qwen3.8  local  ok       run           -",
		"openrouter/anthropic--claude-sonnet-4.5-thinking",
		"                                        sonnet   cloud  ok                     -",
		"ollama/qwen3:8b                         -        local  new               5.2 GB",
		"",
	}, "\n")
	if got := out.String(); got != want {
		t.Errorf("listing at 80 columns =\n%s\nwant\n%s", got, want)
	}
	for _, line := range strings.Split(out.String(), "\n") {
		if w := lipgloss.Width(line); w > 80 {
			t.Errorf("line is %d columns wide, want at most 80: %q", w, line)
		}
		if strings.HasSuffix(line, " ") {
			t.Errorf("line ends in a space: %q", line)
		}
	}
	for _, m := range modelListConfig().Models {
		if !strings.Contains(out.String(), m.ID) {
			t.Errorf("model id %s is missing or cut", m.ID)
		}
	}
	if got := errOut.String(); got != "(PATH not shown at this width; `wt model list --json` has every column)\n" {
		t.Errorf("stderr = %q, want the note naming the dropped column", got)
	}
}

// TestModelListDropsSizeBeforeItCutsAnId verifies that at 60 columns SIZE
// goes too, because keeping it would leave MODEL under 20 columns, and that
// at 40 the five kept columns stay whole with every id on a line of its own.
// The id is what the user copies into `wt model edit` or `wt -M`, so it is
// moved, never cut.
func TestModelListDropsSizeBeforeItCutsAnId(t *testing.T) {
	stubProbeInventory(t, modelListSnapshot())
	var out, errOut bytes.Buffer
	if err := runModelList(&out, &errOut, modelListConfig(), false, 60); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "SIZE") || !strings.Contains(errOut.String(), "SIZE and PATH not shown") {
		t.Errorf("at 60 columns: out =\n%s\nstderr = %q; want no SIZE column and a note", out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), "ollama/gemma4:9b            gemma4   local  ok\n") {
		t.Errorf("at 60 columns a short id is not beside its cells:\n%s", out.String())
	}
	out.Reset()
	if err := runModelList(&out, &errOut, modelListConfig(), false, 40); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(out.String(), "\n")
	if lines[0] != "MODEL   FAMILY   LOC    STATUS   RUNNING" {
		t.Errorf("header at 40 columns = %q", lines[0])
	}
	if !strings.Contains(out.String(), "openrouter/anthropic--claude-sonnet-4.5-thinking\n        sonnet   cloud  ok\n") {
		t.Errorf("at 40 columns the long id is not on a line of its own above its cells:\n%s", out.String())
	}
	for _, line := range lines {
		if !strings.Contains(line, "/") && lipgloss.Width(line) > 40 {
			t.Errorf("a line of cells is %d columns wide at 40: %q", lipgloss.Width(line), line)
		}
	}
}

// TestModelListWithoutAWidthLimit verifies that into a pipe (width 0) every
// column is printed and every model is one line, so `wt model list | grep`
// works and the path is there to cut out.
func TestModelListWithoutAWidthLimit(t *testing.T) {
	stubProbeInventory(t, modelListSnapshot())
	var out, errOut bytes.Buffer
	if err := runModelList(&out, &errOut, modelListConfig(), false, 0); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 7 {
		t.Fatalf("%d lines, want a header and six rows:\n%s", len(lines), out.String())
	}
	if !strings.HasSuffix(lines[0], "SIZE  PATH") {
		t.Errorf("header = %q, want SIZE and PATH", lines[0])
	}
	if !strings.HasSuffix(lines[4], "run           -  /Users/dev/.omlx/models/Qwen3.8-27B-Instruct-MLX-6bit") || !strings.HasPrefix(lines[4], "omlx/Qwen3.8-27B-Instruct-MLX-6bit  ") {
		t.Errorf("running omlx row = %q, want its path last", lines[4])
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr = %q, want nothing when no column is dropped", errOut.String())
	}
}

// TestModelListJSON verifies the --json document: every row with its status
// and running state, size_bytes and path as values or null, an empty tags
// array (never null), and each probed provider's status. Scripts read this;
// the text table drops columns to fit.
func TestModelListJSON(t *testing.T) {
	stubProbeInventory(t, modelListSnapshot())
	var out, errOut bytes.Buffer
	if err := runModelList(&out, &errOut, modelListConfig(), true, 80); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Registry string `json:"registry"`
		Models   []struct {
			ID         string   `json:"id"`
			Tags       []string `json:"tags"`
			Registered bool     `json:"registered"`
			Status     string   `json:"status"`
			Running    string   `json:"running"`
			SizeBytes  *int64   `json:"size_bytes"`
			Path       *string  `json:"path"`
			Target     string   `json:"target"`
			Draft      string   `json:"draft"`
		} `json:"models"`
		Providers map[string]string `json:"providers"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out.String())
	}
	if doc.Registry != config.RegistryPath() || len(doc.Models) != 6 || doc.Providers["omlx"] != "ok" {
		t.Fatalf("doc = %+v", doc)
	}
	byID := map[string]int{}
	for i, m := range doc.Models {
		byID[m.ID] = i
	}
	omlx := doc.Models[byID["omlx/Qwen3.8-27B-Instruct-MLX-6bit"]]
	if omlx.Running != "run" || omlx.Path == nil || *omlx.Path != "/Users/dev/.omlx/models/Qwen3.8-27B-Instruct-MLX-6bit" || omlx.SizeBytes != nil {
		t.Errorf("omlx row = %+v, want running, its path, and a null size", omlx)
	}
	found := doc.Models[byID["ollama/qwen3:8b"]]
	if found.Registered || found.Status != "new" || found.SizeBytes == nil || *found.SizeBytes != 5_200_000_000 || found.Path != nil {
		t.Errorf("discovered row = %+v, want unregistered, new, its size and a null path", found)
	}
	pair := doc.Models[byID["mlx_lm_server/Qwen3.8-27B+draft-Qwen3.8-4B"]]
	if pair.Status != "-" || pair.Target != "mlx-community/Qwen3.8-27B" || pair.Draft != "mlx-community/Qwen3.8-4B" {
		t.Errorf("pairing = %+v, want status - with its target and draft", pair)
	}
	if !strings.Contains(out.String(), `"tags": []`) || strings.Contains(out.String(), `"tags": null`) {
		t.Errorf("a row with no tags must print an empty array:\n%s", out.String())
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr = %q, want nothing in JSON mode", errOut.String())
	}
}

// TestModelListWithNothingToList verifies an empty registry on a machine
// with no local model prints one line that says so, not a lone header row.
func TestModelListWithNothingToList(t *testing.T) {
	stubProbeInventory(t, localmodels.Snapshot{})
	var out, errOut bytes.Buffer
	if err := runModelList(&out, &errOut, &config.Config{}, false, 80); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "no models: ") || strings.Contains(out.String(), "MODEL") {
		t.Errorf("out = %q, want a one-line note and no table", out.String())
	}
}

// TestPlainTableAlignsBothKindsOfColumn verifies the renderer the usage and
// model listings share: text columns are padded on the right, number columns
// on the left, and no line ends in a space. A table that padded numbers on
// the right would misalign every SIZE and SPEND value.
func TestPlainTableAlignsBothKindsOfColumn(t *testing.T) {
	cols := []plainColumn{{head: "KEY"}, {head: "TEXT"}, {head: "NUM", right: true}, {head: "LAST"}}
	got := renderPlainTable(cols, [][]string{{"a", "long text", "7", "x"}, {"bb", "t", "1234", ""}}, 0)
	want := "KEY  TEXT        NUM  LAST\n" +
		"a    long text     7  x\n" +
		"bb   t          1234"
	if got != want {
		t.Errorf("table =\n%s\nwant\n%s", got, want)
	}
	if key, rest := plainTableWidth(cols, [][]string{{"a", "long text", "7", "x"}}); key != 3 || rest != 2+9+2+3+2+4 {
		t.Errorf("plainTableWidth = %d, %d; want 3 and 22", key, rest)
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run, from `wt/`: `go test -count=1 ./cmd/wt -run 'TestModelList|TestPlainTable'`

Expected: the package does not build.

```text
FAIL	github.com/ohanaverse/local-ai-setup/wt/cmd/wt [build failed]
FAIL
# github.com/ohanaverse/local-ai-setup/wt/cmd/wt [github.com/ohanaverse/local-ai-setup/wt/cmd/wt.test]
cmd/wt/model_list_test.go:61:12: undefined: runModelList
cmd/wt/model_list_test.go:105:12: undefined: runModelList
cmd/wt/model_list_test.go:115:12: undefined: runModelList
```

- [ ] **Step 3: Write the plain table**

Create `wt/cmd/wt/plain_table.go`. It is `renderUsageTable`'s layout with the column count and each column's alignment as parameters:

```go
// The borderless, width-aware text table `wt stats` and `wt model list`
// print. One renderer, so the two listings cannot drift apart.
package main

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// plainColumn is one column of a plain table: its heading, and whether its
// cells are right-aligned (numbers) or left-aligned (text).
type plainColumn struct {
	head  string
	right bool
}

// plainGap separates two columns.
const plainGap = "  "

// plainTableWidth is what renderPlainTable needs to print every row on one
// line: key is the first column at its widest cell, rest is every other
// column at its widest with the gaps between them (the gap after the key
// included). A caller that can drop columns compares key+rest, or rest plus
// the least key width it will accept, with the terminal's width.
func plainTableWidth(cols []plainColumn, rows [][]string) (key, rest int) {
	widths := plainWidths(cols, rows)
	for _, w := range widths[1:] {
		rest += len(plainGap) + w
	}
	return widths[0], rest
}

func plainWidths(cols []plainColumn, rows [][]string) []int {
	widths := make([]int, len(cols))
	for i, c := range cols {
		widths[i] = lipgloss.Width(c.head)
	}
	for _, r := range rows {
		for i, c := range r {
			widths[i] = max(widths[i], lipgloss.Width(c))
		}
	}
	return widths
}

// renderPlainTable lays rows out without borders, each column sized to its
// widest cell. The first column is the row's key (a model id): it is never
// truncated, because it is what a reader copies into another command.
//
// width is the terminal's width, or 0 for no limit. When the key column and
// the others do not fit together, the key column is narrowed to what is
// left. A key wider than that runs on into the blank space left of a
// right-aligned second cell when there is room for it and a gap; otherwise it
// is printed on a line of its own, with the rest of the row on the next line
// under its headers — what df does with a long device name.
//
// When the other columns alone are wider than the terminal, the lines are
// longer than the terminal and it wraps them; a caller that can drop columns
// does so first (plainTableWidth). No line ends in a space.
func renderPlainTable(cols []plainColumn, rows [][]string, width int) string {
	widths := plainWidths(cols, rows)
	rest := 0
	for _, w := range widths[1:] {
		rest += len(plainGap) + w
	}
	if width > 0 && widths[0]+rest > width {
		widths[0] = max(width-rest, lipgloss.Width(cols[0].head))
	}

	var b strings.Builder
	line := func(c []string) {
		var l strings.Builder
		// first is the column the second cell starts at; the key may use
		// everything left of it but the gap.
		first := widths[0]
		if len(c) > 1 {
			first += len(plainGap)
			if cols[1].right {
				first += widths[1] - lipgloss.Width(c[1])
			}
		}
		key := c[0]
		if len(c) > 1 && lipgloss.Width(key)+len(plainGap) > first {
			b.WriteString(key + "\n")
			key = ""
		}
		l.WriteString(key)
		for j := 1; j < len(c); j++ {
			pad := widths[j] - lipgloss.Width(c[j])
			if j == 1 {
				l.WriteString(strings.Repeat(" ", first-lipgloss.Width(key)))
			} else {
				l.WriteString(plainGap)
				if cols[j].right {
					l.WriteString(strings.Repeat(" ", pad))
				}
			}
			l.WriteString(c[j])
			if !cols[j].right {
				l.WriteString(strings.Repeat(" ", pad))
			}
		}
		b.WriteString(strings.TrimRight(l.String(), " ") + "\n")
	}
	heads := make([]string, len(cols))
	for i, c := range cols {
		heads[i] = c.head
	}
	line(heads)
	for _, r := range rows {
		line(r)
	}
	return strings.TrimRight(b.String(), "\n")
}
```

- [ ] **Step 4: Make the usage table use it**

```diff
--- a/wt/cmd/wt/stats_usage_table.go
+++ b/wt/cmd/wt/stats_usage_table.go
@@ -6,14 +6,14 @@ import (
 	"strconv"
 	"strings"
 	"unicode"
-
-	"github.com/charmbracelet/lipgloss"
 )
 
-var usageHeaders = [6]string{"MODEL", "LAUNCHES", "REQUESTS", "PROMPT", "COMPLETION", "SPEND"}
-
-// usageGap separates two columns.
-const usageGap = "  "
+// usageColumns are the usage table's columns: the model id, then five
+// right-aligned numbers.
+var usageColumns = []plainColumn{
+	{head: "MODEL"}, {head: "LAUNCHES", right: true}, {head: "REQUESTS", right: true},
+	{head: "PROMPT", right: true}, {head: "COMPLETION", right: true}, {head: "SPEND", right: true},
+}
 
 // usageCells are one row's six cells. The four spend cells are "-" when
 // the report has no spend data.
@@ -83,16 +83,9 @@ func formatCount(n int64) string {
 	return b.String()
 }
 
-// renderUsageTable lays the rows out without borders: MODEL left-aligned,
-// the five number columns right-aligned and sized to their widest cell.
-//
-// width is the terminal's width, or 0 for no limit. When the longest model
-// id and the number columns do not fit together, the MODEL column is
-// narrowed to what is left. A model id wider than that runs on into the
-// blank space left of its own LAUNCHES value when there is room for it and
-// a gap; otherwise it is printed on a line of its own, with its numbers on
-// the next line under their headers — what df does with a long device
-// name. No id is ever truncated: the id is what a reader copies into
+// renderUsageTable lays the rows out with renderPlainTable: MODEL
+// left-aligned, the five number columns right-aligned and sized to their
+// widest cell. No id is ever truncated: the id is what a reader copies into
 // `wt -M` or `--model`. (visibleID spells out control and invisible
 // characters; that is the only change an id undergoes.)
 //
@@ -100,44 +93,10 @@ func formatCount(n int64) string {
 // counts; on a terminal narrower than those plus "MODEL", the lines are
 // longer than the terminal and it wraps them.
 func renderUsageTable(rows []usageRow, width int) string {
-	cells := make([][6]string, len(rows))
-	widths := [6]int{}
-	for i, h := range usageHeaders {
-		widths[i] = lipgloss.Width(h)
-	}
+	cells := make([][]string, len(rows))
 	for i, r := range rows {
-		cells[i] = usageCells(r)
-		for j, c := range cells[i] {
-			widths[j] = max(widths[j], lipgloss.Width(c))
-		}
-	}
-	numbers := 0
-	for _, w := range widths[1:] {
-		numbers += len(usageGap) + w
-	}
-	if width > 0 && widths[0]+numbers > width {
-		widths[0] = max(width-numbers, lipgloss.Width(usageHeaders[0]))
-	}
-
-	var b strings.Builder
-	line := func(c [6]string) {
-		// first is the column the LAUNCHES cell starts at; the model id may
-		// use everything left of it but the gap.
-		first := widths[0] + len(usageGap) + widths[1] - lipgloss.Width(c[1])
-		model := c[0]
-		if lipgloss.Width(model)+len(usageGap) > first {
-			b.WriteString(model + "\n")
-			model = ""
-		}
-		b.WriteString(model + strings.Repeat(" ", first-lipgloss.Width(model)) + c[1])
-		for j := 2; j < 6; j++ {
-			b.WriteString(usageGap + strings.Repeat(" ", widths[j]-lipgloss.Width(c[j])) + c[j])
-		}
-		b.WriteString("\n")
-	}
-	line(usageHeaders)
-	for _, c := range cells {
-		line(c)
+		c := usageCells(r)
+		cells[i] = c[:]
 	}
-	return strings.TrimRight(b.String(), "\n")
+	return renderPlainTable(usageColumns, cells, width)
 }
```

- [ ] **Step 5: Write the listing**

Create `wt/cmd/wt/model_list.go`:

```go
// wt model list — every registry model and every discovered local model,
// with live status. Read-only.
package main

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modeladmin"
	"github.com/spf13/cobra"
)

// modelListColumns are the text listing's columns. SIZE and PATH are the two
// a narrow terminal gives up (fitModelList).
var modelListColumns = []plainColumn{
	{head: "MODEL"}, {head: "FAMILY"}, {head: "LOC"}, {head: "STATUS"}, {head: "RUNNING"},
	{head: "SIZE", right: true}, {head: "PATH"},
}

func modelListCmd(a *app) *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "list",
		Short: "List every registry model and every local model found on this machine",
		Long: "List every model in the registry, and every local model the providers have\n" +
			"that the registry does not (STATUS new), from one live probe.\n\n" +
			"  STATUS   ok       a cloud model, or local weights the probe found\n" +
			"           missing  a registered local model that is not on disk\n" +
			"           unknown  the provider could not be asked\n" +
			"           new      on disk or pulled, not in the registry\n" +
			"           -        an mlx_lm_server pairing (never enumerated)\n" +
			"  RUNNING  run, load (omlx is still loading it), blank, or ? (could not tell)\n\n" +
			"SIZE and PATH are shown when the terminal is wide enough for them; --json\n" +
			"always has them (size_bytes, path).",
		Example:      "  wt model list\n  wt model list --json",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// The load error only: a registry with a gap in it (a model
			// whose provider has no row) is exactly what this list is for.
			if a.loadErr != nil {
				return configError(a.loadErr)
			}
			return runModelList(cmd.OutOrStdout(), cmd.ErrOrStderr(), a.cfg, asJSON, stdoutWidth())
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "machine-readable output, with size_bytes and path for every row")
	return c
}

// modelListJSON is `wt model list --json`'s one document.
type modelListJSON struct {
	Registry string         `json:"registry"`
	Models   []modelListRow `json:"models"`
	// Providers is each probed provider family's probe status: ok, partial
	// (running state not known) or unreachable.
	Providers map[string]string `json:"providers"`
}

type modelListRow struct {
	ID         string   `json:"id"`
	Family     string   `json:"family"`
	ProviderID string   `json:"provider_id"`
	ModelName  string   `json:"model_name"`
	Location   string   `json:"location"`
	Tags       []string `json:"tags"`
	Registered bool     `json:"registered"`
	Status     string   `json:"status"`
	// Running is "run", "load", "" or "?", as the text table prints it.
	Running string `json:"running"`
	// SizeBytes and Path are null when wt does not know them.
	SizeBytes *int64  `json:"size_bytes"`
	Path      *string `json:"path"`
	Target    string  `json:"target,omitempty"`
	Draft     string  `json:"draft,omitempty"`
}

func runModelList(out, errOut io.Writer, cfg *config.Config, asJSON bool, width int) error {
	snap := probeInventory(cfg)
	rows := modeladmin.Rows(cfg, snap)
	if asJSON {
		doc := modelListJSON{Registry: config.RegistryPath(), Models: []modelListRow{}, Providers: map[string]string{}}
		for f, st := range snap.Providers {
			doc.Providers[f] = string(st)
		}
		for _, r := range rows {
			jr := modelListRow{
				ID: r.ID, Family: r.Family, ProviderID: r.ProviderID, ModelName: r.ModelName, Location: r.Location,
				Tags: append([]string{}, r.Tags...), Registered: r.Registered, Status: string(r.Status), Running: r.Running,
				Target: r.Target, Draft: r.Draft,
			}
			if r.Size > 0 {
				jr.SizeBytes = &r.Size
			}
			if r.Path != "" {
				jr.Path = &r.Path
			}
			doc.Models = append(doc.Models, jr)
		}
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(doc)
	}
	if len(rows) == 0 {
		fmt.Fprintln(out, "no models: the registry has none, and no local model was found on this machine")
		return nil
	}
	cols, cells, dropped := fitModelList(rows, width)
	fmt.Fprintln(out, renderPlainTable(cols, cells, width))
	if dropped != "" {
		fmt.Fprintf(errOut, "(%s not shown at this width; `wt model list --json` has every column)\n", dropped)
	}
	return nil
}

// modelListMinKey is the least width the MODEL column is given before SIZE
// is dropped to make room: enough for a short id beside its cells. A longer
// id goes on a line of its own (renderPlainTable), never cut.
const modelListMinKey = 20

// fitModelList picks the columns that fit width (0: no limit) and returns
// them with the rows' cells. PATH is shown only when every row fits on one
// line with it. SIZE stays as long as the other columns leave MODEL at least
// modelListMinKey columns. The five columns that say what a row is are never
// dropped. dropped names what was left out, "" when nothing was.
func fitModelList(rows []modeladmin.Row, width int) (cols []plainColumn, cells [][]string, dropped string) {
	full := make([][]string, len(rows))
	for i, r := range rows {
		full[i] = []string{
			visibleID(r.ID), dash(r.Family), dash(r.Location), string(r.Status), r.Running,
			modeladmin.FormatSize(r.Size), dash(r.Path),
		}
	}
	take := func(keep int) {
		cols = modelListColumns[:keep]
		cells = make([][]string, len(full))
		for i := range full {
			cells[i] = full[i][:keep]
		}
	}
	take(7)
	if width <= 0 {
		return cols, cells, ""
	}
	if key, rest := plainTableWidth(cols, cells); key+rest <= width {
		return cols, cells, ""
	}
	take(6)
	if _, rest := plainTableWidth(cols, cells); modelListMinKey+rest <= width {
		return cols, cells, "PATH"
	}
	take(5)
	return cols, cells, "SIZE and PATH"
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
```

- [ ] **Step 6: Register the command**

```diff
--- a/wt/cmd/wt/model.go
+++ b/wt/cmd/wt/model.go
@@ -51,7 +51,7 @@ func realSyncRoutesAfterWrite(out, errOut io.Writer) string {
 
 // modelCmd is the `wt model` group: the commands that write registry.toml.
 // This step ships `init`; add, edit, rm, list and the Models tab follow.
-func modelCmd(_ *app) *cobra.Command {
+func modelCmd(a *app) *cobra.Command {
 	c := &cobra.Command{
 		Use:   "model",
 		Short: "Manage the model registry (registry.toml)",
@@ -90,7 +90,7 @@ func modelCmd(_ *app) *cobra.Command {
 		},
 	}
 	initC.Flags().BoolVar(&initJSON, "json", false, "machine-readable output")
-	c.AddCommand(initC)
+	c.AddCommand(initC, modelListCmd(a))
 	return c
 }
 
```

- [ ] **Step 7: Run the tests**

Run, from `wt/`: `go test -count=1 ./cmd/wt -run 'TestModelList|TestPlainTable|TestRenderUsageTable|TestStats|TestModelCommandGroup'`

Expected: `ok  	github.com/ohanaverse/local-ai-setup/wt/cmd/wt`. The existing `TestRenderUsageTable*` tests pass unedited: they pin the usage table's exact text at 80 columns, which is the proof that moving its layout into `renderPlainTable` changed no output.

- [ ] **Step 8: Document it**

Create `wt/docs/wt-model.md`:

````text
# `wt model`

The models in the registry (`~/.config/local-ai/registry.toml`): list them,
add, edit and remove them. wt never downloads a model and never deletes
weights; it registers what a provider already has, or will have.

```bash
wt model list [--json]     # every registry model, and every local model found on this machine
wt model init [--json]     # create the registry if it is missing; add the default provider rows
```

## `wt model list [--json]`

One row per registry model, then one per local model a provider has that the
registry does not (`STATUS new`), from one live probe of the providers.

| Column | Values |
|---|---|
| `STATUS` | `ok` — a cloud model, or local weights the probe found; `missing` — a registered local model that is not on disk; `unknown` — the provider could not be asked, wt has no probe for it, or the row's location does not resolve; `new` — on disk or pulled, not in the registry; `-` — an mlx_lm_server pairing, which is never enumerated |
| `RUNNING` | `run`; `load` (omlx is still loading it); blank; `?` when the probe could not tell |
| `SIZE` | the weights' size when the probe reports it (ollama); `-` otherwise |
| `PATH` | the model's directory (omlx, mtplx) or its `fetch.local_path`; `-` otherwise |

The text table has no borders and fits the terminal: `PATH` is shown only when
every row fits on one line with it, and `SIZE` only while it leaves `MODEL` at
least 20 columns. A model id is never cut — one too long for its column gets a
line of its own, above its cells. When a column is left out, a note on stderr
says so. Into a pipe every column is printed, one model per line.

`--json` always has everything: `registry` (the file), `models` (each with
`id`, `family`, `provider_id`, `model_name`, `location`, `tags`,
`registered`, `status`, `running`, `size_bytes` and `path` — the last two
`null` when wt does not know them — and `target` and `draft` for a pairing),
and `providers` (each probed provider's status: `ok`, `partial`,
`unreachable`).

It runs on a registry that has a gap in it (a model whose provider has no
row, a mistyped location): that is what the list is for. Only a registry that
cannot be read at all stops it.

## `wt model init [--json]`

Creates the registry when it is missing and adds the provider rows wt needs
and the registry lacks. It never changes a row that exists. `wt model init
--help` lists which rows, and why.
````

In `wt/CLAUDE.md`, find:

```text
- `docs/wt-smoke.md`, `docs/wt-start-stop.md`, `docs/wt-stats.md` — command references
```

and replace it with:

```text
- `docs/wt-smoke.md`, `docs/wt-start-stop.md`, `docs/wt-stats.md`, `docs/wt-model.md` — command references
```

In `wt/CLAUDE.md`, find:

```text
ollamacheck,catalog,localmodels,lifecycle,litellm,spend,smoke}`.
```

and replace it with:

```text
ollamacheck,catalog,localmodels,modeladmin,lifecycle,litellm,spend,smoke}`.
```

In `wt/CLAUDE.md`, find:

```text
| `cmd/wt/stats_usage_table.go` | `renderUsageTable` — the borderless, width-aware usage table (an id is never truncated) |
```

and replace it with:

```text
| `cmd/wt/stats_usage_table.go` | `renderUsageTable` — the usage table's cells, laid out by `renderPlainTable` |
| `cmd/wt/plain_table.go` | `renderPlainTable` — the one borderless, width-aware text table (`wt stats`, `wt model list`): the first column is the row's key and is never truncated |
```

In `wt/CLAUDE.md`, find:

```text
| `cmd/wt/model.go` | `wt model` group; `wt model init [--json]` — creates the registry and seeds provider rows, then one route sync (`syncRoutesAfterWrite`) |
```

and replace it with:

```text
| `cmd/wt/model.go` | `wt model` group; `wt model init [--json]` — creates the registry and seeds provider rows, then one route sync (`syncRoutesAfterWrite`) |
| `cmd/wt/model_list.go` | `wt model list [--json]` — `modeladmin.Rows` over one probe; `fitModelList` drops PATH, then SIZE, on a narrow terminal |
```

In `wt/CLAUDE.md`, find:

```text
| `internal/localmodels/` | local inventory — see [Local-model resolution](#local-model-resolution) |
```

and replace it with:

```text
| `internal/localmodels/` | local inventory — see [Local-model resolution](#local-model-resolution) |
| `internal/modeladmin/` | the core of model management, shared by the `wt model` commands and the Models tab, with no UI import: `Rows` (every registry model and every discovered one, with status, running, path and size — nothing hidden, unlike `catalog.Build`), `WeightsNote`, `FormatSize` |
```

In `wt/CLAUDE.md`, find:

```text
wt model init [--json]               # create registry.toml if missing; add default provider rows (safe to re-run)
```

and replace it with:

```text
wt model init [--json]               # create registry.toml if missing; add default provider rows (safe to re-run)
wt model list [--json]               # every registry model and every local model found, with live status
```

In `wt/docs/internals/local-models.md`, find:

```text
`Entry.ArtifactKnown` distinguishes "confirmed missing" from "could not determine".
```

and replace it with:

```text
`Entry.ArtifactKnown` distinguishes "confirmed missing" from "could not determine". `Entry.Path` is the artifact's directory (omlx and mtplx: where the scan found it) and `Entry.Size` its size in bytes (ollama: the `size` of `/api/tags`); each is zero when the probe's own answer does not carry it, and nothing is walked or stat'ed to fill them. `internal/modeladmin.Rows` builds the registry-centric list `wt model list` and the Models tab show from the same snapshot: unlike `catalog.Build` it hides nothing — a registered model that is not on disk is a `missing` row, a stopped pairing a `-` row, and a row with `fetch.local_path` takes its path from that key and its presence from a stat.
```

In `wt/CHANGELOG.md`, find:

```text
## Unreleased

### Added
```

and replace it with:

```text
## Unreleased

### Added

- `wt model list [--json]` lists every model in the registry and every local
  model the providers have that the registry does not, with live status
  (`ok`, `missing`, `unknown`, `new`, `-`) and running state (`run`, `load`,
  blank, `?`). The text table has no borders and fits the terminal; `--json`
  adds each model's size and path. Reference: `docs/wt-model.md`.
```

- [ ] **Step 9: Look at the real command**

Build the branch and run it in a throwaway home, never the real one. With no registry it must refuse and name `wt model init`; after `wt model init` it must list nothing and say so. The block is one command and stands on its own: wt gets its environment from `env -i`, which empties it first, not from anything exported in the shell. From the monorepo root:

```bash
D="$(cd /tmp && pwd -P)/wt-step3-list"
rm -rf "$D" && mkdir -p "$D/home"
(cd wt && go build -o "$D/wt" ./cmd/wt)
wt() { env -i HOME="$D/home" XDG_CONFIG_HOME="$D/home/.config" WT_LITELLM_CONFIG="$D/home/litellm.yaml" WT_LITELLM_RESTART_CMD=true PATH=/usr/bin:/bin "$D/wt" "$@"; }
wt model list; echo "exit $?"
wt model init
wt model list; echo "exit $?"
rm -rf "$D"
```

Expected, as observed (`$D` stands for the scratch directory: `/private/tmp/wt-step3-list` on macOS, where `/tmp` is a link):

```text
Error: config error: model registry not found at $D/home/.config/local-ai/registry.toml — seed it with `wt model init`
wt: config error: model registry not found at $D/home/.config/local-ai/registry.toml — seed it with `wt model init`
exit 1
registry: $D/home/.config/local-ai/registry.toml (created)
nothing to add
no models: the registry has none, and no local model was found on this machine
exit 0
```

(`PATH=/usr/bin:/bin` hides any provider the machine has installed, so nothing is seeded and nothing is probed. The error is printed twice, once by cobra and once by wt, as for every wt command.)

- [ ] **Step 10: Verify the PR and commit**

From `wt/`:

```bash
test -z "$(gofmt -l .)" && go vet ./... && go test -count=1 ./... && make check
```

Expected: every package `ok`; `make check` ends without an error. From the monorepo root: `make test-all && make check-links`. Expected: exit 0 (`docs/contracts/registry.sample.toml` changed, so the modelman and llmbench contract tests run too; they read the file's values, not its comments).

```bash
git add wt/cmd/wt wt/docs/wt-model.md wt/CLAUDE.md wt/docs/internals/local-models.md wt/CHANGELOG.md
git commit -m "feat(wt): wt model list, on one borderless table shared with wt stats"
```

- [ ] **Step 11: Hand off**

Stop here. Tell the owner the branch is ready, what `make test-all` printed, and what Step 9 showed. Say that the listing was run against an empty registry only; the 80-column layout is pinned by `TestModelListFitsEightyColumns` with realistic rows. Push and open the PR only after the owner's OK. Suggested title: `feat(wt): wt model list, and where a local model's weights are`.

---

## PR C — `wt model add`, `edit`, `rm`

Branch `feat/wt-model-add-edit-rm`. Needs PR B on `main`, and the owner's answer to the one question under "Before PR C" (Branches and PRs): whether an add seeds openrouter's row. This is the PR that makes wt a writer of model rows, so it starts by closing the gap Step 2 left in the writer's validation.

Create the branch from the monorepo root:

```bash
git fetch origin
git switch -c feat/wt-model-add-edit-rm origin/main
```

### Task 6: A written model names a provider row and a location that resolves

Step 2's `validateTouched` checks a touched model row by itself. It does not check the two `Config.Validate` rules that need other rows, so `PatchModel` with `location = "mars"` or `provider_id = "nope"` is written, and every wt command then reports a config error. Step 2's only caller added provider rows; this PR's callers write model rows.

The same task gives a model's reference to openrouter the effect a reference to a local provider already has: the default row is seeded. With the new check an add under a provider with no row is refused, and openrouter — the one cloud provider wt has a default row for — would be refused on every registry whose agents do not list it.

**Files:**
- Modify: `wt/internal/config/registry_validate_test.go` (two cases in each table of `TestUpdateRegistryValidatesOnlyTheRowsItTouched`; one new test)
- Modify: `wt/internal/config/registry_write_test.go:494` (`TestUpdateRegistryWritesARedirectedRegistry`'s `apply`)
- Modify: `wt/internal/config/registry_validate.go:26-39` (`validateTouched`)
- Modify: `wt/internal/config/registry_seed_test.go:128-136`, `:178` (`TestSeedAddsOnlyWhatTheMachineNeeds`)
- Modify: `wt/internal/config/registry_seed.go:152-154` (the comment), `:226` (the cloud loop's condition)

**Interfaces:**
- Consumes (existing): `(*RegistryDoc).rows(key)`, `rowID(row)`, `(*Config).ResolveLocation(m Model) (Location, error)`, `(*Config).ProviderByID(id) *Provider`, `ErrRegistryInvalid`, the test helpers `scratchRegistry`, `readFile`, `setFamily`, `docRegistry`
- Produces:
  - `func (d *RegistryDoc) validateModelRefs(row *tomlw.Table) error`, called by `validateTouched` for each touched model
  - Error text, each wrapped in `ErrRegistryInvalid`: `model "<id>": provider_id "<p>" names no provider row`; `model "<id>" has location "<l>"; expected "local" or "cloud"`; `model "<id>": no location on model or provider "<p>"`; `model "<id>": provider "<p>" has location "<l>"; expected "local" or "cloud"`
  - `SeedRegistryDefaults` adds openrouter's default row when a model row references `openrouter` or an agent lists it (its signature does not change)

- [ ] **Step 1: Write the failing tests**

```diff
--- a/wt/internal/config/registry_validate_test.go
+++ b/wt/internal/config/registry_validate_test.go
@@ -90,6 +90,8 @@ input_price_per_million = -1
 		{"a window on an unknown day", map[string]any{"cost.time_prices": []map[string]any{{"timezone": "UTC", "windows": []map[string]any{{"days": []string{"funday"}, "start": "00:00", "end": "01:00"}}}}}, nil, "days must be drawn from"},
 		{"a window that ends before it starts", map[string]any{"cost.time_prices": []map[string]any{{"timezone": "UTC", "windows": []map[string]any{{"days": []string{"mon"}, "start": "12:00", "end": "09:00"}}}}}, nil, "start must be before end"},
 		{"a window time that is not HH:MM", map[string]any{"cost.time_prices": []map[string]any{{"timezone": "UTC", "windows": []map[string]any{{"days": []string{"mon"}, "start": "9am", "end": "12:00"}}}}}, nil, "start must be HH:MM"},
+		{"a location that is neither local nor cloud", map[string]any{"location": "mars"}, nil, `has location "mars"; expected "local" or "cloud"`},
+		{"a provider with no row", map[string]any{"provider_id": "nope"}, nil, `provider_id "nope" names no provider row`},
 		{"a window past midnight", map[string]any{"cost.time_prices": []map[string]any{{"timezone": "UTC", "windows": []map[string]any{{"days": []string{"mon"}, "start": "00:00", "end": "24:30"}}}}}, nil, "end must be HH:MM between 00:00 and 24:00"},
 	}
 	for _, c := range invalid {
@@ -128,6 +130,7 @@ input_price_per_million = -1
 		{"the legacy free kind", map[string]any{"cost.kind": "free"}, `kind = "free"`},
 		{"a legacy per-token price", map[string]any{"cost.kind": "per_token", "cost.price_per_million_tokens": 2.5}, "price_per_million_tokens = 2.5"},
 		{"a legacy subscription", map[string]any{"cost.kind": "subscription", "cost.price_per_period": 20, "cost.period": "year"}, `period = "year"`},
+		{"a location of its own", map[string]any{"location": "cloud"}, `location = "cloud"`},
 		{"fetch and draft tables", map[string]any{"fetch.repo": "org/beta", "draft": map[string]any{"repo": "org/draft"}}, "[models.draft]"},
 	}
 	for _, c := range valid {
@@ -165,3 +168,87 @@ input_price_per_million = -1
 		}
 	})
 }
+
+// TestUpdateRegistryValidatesAModelAgainstItsProviderRow covers the two rules
+// that need another row, which ResolveLocation judges everywhere else in wt:
+// a model with no location of its own inherits its provider's, so a provider
+// with none (or a mistyped one) makes the model unusable. A write that leaves
+// a touched model in that state is refused; a provider row added in the same
+// write counts, which is what lets `wt model add` seed and add at once.
+func TestUpdateRegistryValidatesAModelAgainstItsProviderRow(t *testing.T) {
+	const gaps = docRegistry + `
+[[providers]]
+id = "bare"
+
+[providers.auth]
+type = "none"
+
+[[providers]]
+id = "typo"
+location = "Local"
+
+[providers.auth]
+type = "none"
+`
+	add := func(provider string, extra map[string]any) func(*RegistryDoc) error {
+		return func(d *RegistryDoc) error {
+			row := map[string]any{"id": provider + "/m", "family": "f", "provider_id": provider, "model_name": "m"}
+			for k, v := range extra {
+				row[k] = v
+			}
+			return d.AddModel(row)
+		}
+	}
+	refused := []struct {
+		name, provider, want string
+	}{
+		{"a provider row with no location", "bare", `model "bare/m": no location on model or provider "bare"`},
+		{"a provider row with a mistyped location", "typo", `provider "typo" has location "Local"`},
+		{"no provider row at all", "ghost", `model "ghost/m": provider_id "ghost" names no provider row`},
+	}
+	for _, c := range refused {
+		t.Run(c.name, func(t *testing.T) {
+			path := scratchRegistry(t, gaps)
+			changed, err := UpdateRegistry(add(c.provider, nil))
+			if !errors.Is(err, ErrRegistryInvalid) || changed || !strings.Contains(err.Error(), c.want) {
+				t.Fatalf("UpdateRegistry = (%v, %v), want ErrRegistryInvalid mentioning %q", changed, err, c.want)
+			}
+			if strings.Count(err.Error(), `model "`+c.provider+`/m"`) != 1 {
+				t.Errorf("error %q should name the model once", err)
+			}
+			if got := readFile(t, path); got != gaps {
+				t.Error("a refused write changed the file")
+			}
+		})
+	}
+	t.Run("the model's own location stands in for the provider's", func(t *testing.T) {
+		scratchRegistry(t, gaps)
+		if changed, err := UpdateRegistry(add("bare", map[string]any{"location": "local"})); err != nil || !changed {
+			t.Fatalf("UpdateRegistry = (%v, %v), want (true, nil)", changed, err)
+		}
+	})
+	t.Run("a provider row added in the same write counts", func(t *testing.T) {
+		scratchRegistry(t, gaps)
+		changed, err := UpdateRegistry(func(d *RegistryDoc) error {
+			if err := add("fresh", nil)(d); err != nil {
+				return err
+			}
+			return d.AddProvider(map[string]any{"id": "fresh", "location": "cloud", "auth": map[string]any{"type": "none"}})
+		})
+		if err != nil || !changed {
+			t.Fatalf("UpdateRegistry = (%v, %v), want (true, nil)", changed, err)
+		}
+	})
+	t.Run("an untouched model with a gap does not block the write", func(t *testing.T) {
+		scratchRegistry(t, gaps+`
+[[models]]
+id = "ghost/old"
+family = "f"
+provider_id = "ghost"
+model_name = "old"
+`)
+		if changed, err := UpdateRegistry(setFamily("ollama/beta", "fine")); err != nil || !changed {
+			t.Fatalf("UpdateRegistry = (%v, %v), want (true, nil)", changed, err)
+		}
+	})
+}
```

`TestUpdateRegistryWritesARedirectedRegistry` adds a model to an empty registry, with no provider row. That is now refused, so its `apply` adds the row the model names:

```diff
--- a/wt/internal/config/registry_write_test.go
+++ b/wt/internal/config/registry_write_test.go
@@ -491,6 +491,10 @@ func TestUpdateRegistryWritesARedirectedRegistry(t *testing.T) {
 		t.Fatal("fixture error: the registry should read as redirected")
 	}
 	changed, err := UpdateRegistry(func(d *RegistryDoc) error {
+		// A model row is written only beside the provider row it names.
+		if err := d.AddProvider(map[string]any{"id": "ollama", "location": "local", "auth": map[string]any{"type": "none"}}); err != nil {
+			return err
+		}
 		return d.AddModel(map[string]any{"id": "ollama/a", "family": "f", "provider_id": "ollama", "model_name": "a"})
 	})
 	if err != nil || !changed {
```

- [ ] **Step 2: Run them to see them fail**

Run, from `wt/`: `go test -count=1 ./internal/config -run TestUpdateRegistry`

Expected: the two new table cases and three subtests of the new test fail, because the rows are written.

```text
--- FAIL: TestUpdateRegistryValidatesOnlyTheRowsItTouched (0.04s)
    --- FAIL: TestUpdateRegistryValidatesOnlyTheRowsItTouched/a_location_that_is_neither_local_nor_cloud (0.00s)
        registry_validate_test.go:104: UpdateRegistry = (true, <nil>), want ErrRegistryInvalid mentioning "has location \"mars\"; expected \"local\" or \"cloud\""
    --- FAIL: TestUpdateRegistryValidatesOnlyTheRowsItTouched/a_provider_with_no_row (0.00s)
        registry_validate_test.go:104: UpdateRegistry = (true, <nil>), want ErrRegistryInvalid mentioning "provider_id \"nope\" names no provider row"
--- FAIL: TestUpdateRegistryValidatesAModelAgainstItsProviderRow (0.01s)
    --- FAIL: TestUpdateRegistryValidatesAModelAgainstItsProviderRow/a_provider_row_with_no_location (0.00s)
        registry_validate_test.go:214: UpdateRegistry = (true, <nil>), want ErrRegistryInvalid mentioning "model \"bare/m\": no location on model or provider \"bare\""
    --- FAIL: TestUpdateRegistryValidatesAModelAgainstItsProviderRow/a_provider_row_with_a_mistyped_location (0.00s)
        registry_validate_test.go:214: UpdateRegistry = (true, <nil>), want ErrRegistryInvalid mentioning "provider \"typo\" has location \"Local\""
    --- FAIL: TestUpdateRegistryValidatesAModelAgainstItsProviderRow/no_provider_row_at_all (0.00s)
```

- [ ] **Step 3: Check a touched model against the provider rows**

```diff
--- a/wt/internal/config/registry_validate.go
+++ b/wt/internal/config/registry_validate.go
@@ -33,10 +33,40 @@ func (d *RegistryDoc) validateTouched() error {
 		if err := validateModelRow(row); err != nil {
 			return fmt.Errorf("%w: model %q: %w", ErrRegistryInvalid, rowID(row), err)
 		}
+		// Its message names the model itself, as Config.Validate's does.
+		if err := d.validateModelRefs(row); err != nil {
+			return fmt.Errorf("%w: %w", ErrRegistryInvalid, err)
+		}
 	}
 	return nil
 }
 
+// validateModelRefs checks the two rules of Config.Validate that need other
+// rows: the model's provider_id names a provider row, and its location — its
+// own, or the one it inherits from that provider — resolves to "local" or
+// "cloud". Without them `wt model edit --location mars` would be written, and
+// every wt command would then refuse to run until the file was fixed by hand.
+// ResolveLocation is the judge, as everywhere else; it is asked over the
+// document's provider rows as they are after apply, so a provider row seeded
+// in the same write counts.
+func (d *RegistryDoc) validateModelRefs(row *tomlw.Table) error {
+	str := func(t *tomlw.Table, key string) string {
+		v, _ := t.Get(key)
+		s, _ := v.(string)
+		return s
+	}
+	cfg := &Config{}
+	for _, p := range d.rows("providers") {
+		cfg.Providers = append(cfg.Providers, Provider{ID: rowID(p), Location: Location(str(p, "location"))})
+	}
+	m := Model{ID: rowID(row), ProviderID: str(row, "provider_id"), Location: Location(str(row, "location"))}
+	if cfg.ProviderByID(m.ProviderID) == nil {
+		return fmt.Errorf("model %q: provider_id %q names no provider row", m.ID, m.ProviderID)
+	}
+	_, err := cfg.ResolveLocation(m)
+	return err
+}
+
 // validateProviderRow is modelman's _parse_provider (an id, and an auth table
 // with a type) plus wt's typed decode.
 func validateProviderRow(row *tomlw.Table) error {
```

- [ ] **Step 4: Run the package**

Run, from `wt/`: `go test -count=1 ./internal/config`

Expected: `ok  	github.com/ohanaverse/local-ai-setup/wt/internal/config`

- [ ] **Step 5: Write the failing test for openrouter's row**

The subtest Step 2 wrote for the opposite ("openrouter is not seeded for a model or from PATH") becomes two: PATH still seeds nothing for openrouter, and a model that references it now does.

```diff
--- a/wt/internal/config/registry_seed_test.go
+++ b/wt/internal/config/registry_seed_test.go
@@ -132,8 +132,9 @@ model_name = "org/target"
 // server, but a model that references `omlx`, or an agent that lists it,
 // still gets its row. A provider an agent lists gets its default row whether
 // or not it is installed, because wt refuses a config whose agent lists a
-// provider with no row; openrouter comes from an agent only, never from a
-// model or from PATH.
+// provider with no row; openrouter comes from a model that references it or
+// an agent that lists it, never from PATH — `wt model add openrouter …` on a
+// registry with no openrouter row must not be refused for a row wt can write.
 func TestSeedAddsOnlyWhatTheMachineNeeds(t *testing.T) {
 	const sixBit = `[[providers]]
 id = "omlx-6bit"
@@ -175,7 +176,8 @@ model_name = "org/m"
 		{"an agent lists omlx beside an omlx-6bit row", sixBit, SeedEnv{AgentProviders: []string{"omlx"}}, []string{"omlx"}},
 		{"an agent lists openrouter", "", SeedEnv{AgentProviders: []string{"openrouter"}}, []string{"openrouter"}},
 		{"an agent lists a provider that has its row", sixBit, SeedEnv{AgentProviders: []string{"omlx-6bit"}}, nil},
-		{"openrouter is not seeded for a model or from PATH", openrouterModel, SeedEnv{OnPath: func(string) bool { return true }}, []string{"ollama", "omlx", "mtplx"}},
+		{"openrouter is not seeded from PATH", "", SeedEnv{OnPath: func(string) bool { return true }}, []string{"ollama", "omlx", "mtplx"}},
+		{"a model that references openrouter gets the row", openrouterModel, SeedEnv{}, []string{"openrouter"}},
 	}
 	for _, c := range cases {
 		t.Run(c.name, func(t *testing.T) {
```

- [ ] **Step 6: Run it to see it fail**

Run, from `wt/`: `go test -count=1 ./internal/config -run TestSeedAddsOnlyWhatTheMachineNeeds`

Expected: the new subtest fails; no row is added.

```text
--- FAIL: TestSeedAddsOnlyWhatTheMachineNeeds (0.01s)
    --- FAIL: TestSeedAddsOnlyWhatTheMachineNeeds/a_model_that_references_openrouter_gets_the_row (0.00s)
        registry_seed_test.go:187: added = [], want [openrouter]
FAIL
```

- [ ] **Step 7: Seed a cloud provider's row for a model that references it**

```diff
--- a/wt/internal/config/registry_seed.go
+++ b/wt/internal/config/registry_seed.go
@@ -149,8 +149,9 @@ func cloudProviderRow(id string) map[string]any {
 //     for the three that have a command — when that command is on PATH. An
 //     installed omlx gets no row when an `omlx-6bit` row exists: the two are
 //     one server, and a second row would change which one discovery uses.
-//   - A cloud provider with a default row (openrouter) gets it when an agent
-//     lists it. The row holds no key, only the name of the environment
+//   - A cloud provider with a default row (openrouter) gets it when a model
+//     references it or an agent lists it — never from PATH: it has no
+//     command. The row holds no key, only the name of the environment
 //     variable the key is read from (OpenRouterKeyEnv).
 //   - Every agent in env.Agents gets a native cloud provider row under its
 //     own name — unless that name is one of the providers above, which then
@@ -223,7 +224,7 @@ func SeedRegistryDefaults(d *RegistryDoc, env SeedEnv) (added, unseeded []string
 		}
 	}
 	for _, id := range cloudProviderIDs {
-		if existing[id] || !listed[id] {
+		if existing[id] || !(wanted[id] || listed[id]) {
 			continue
 		}
 		if err := add(cloudProviderRow(id)); err != nil {
```

Run, from `wt/`: `go test -count=1 ./internal/config`

Expected: `ok  	github.com/ohanaverse/local-ai-setup/wt/internal/config`

- [ ] **Step 8: Commit**

```bash
git add wt/internal/config/registry_validate.go wt/internal/config/registry_validate_test.go wt/internal/config/registry_write_test.go wt/internal/config/registry_seed.go wt/internal/config/registry_seed_test.go
git commit -m "feat(wt): a registry write refuses a model whose provider or location does not resolve; openrouter's row is seeded for a model"
```

### Task 7: `modeladmin`: fields, ids, `Add`, `Edit`, `Remove`, and what ollama says a model can do

**Files:**
- Modify: `wt/internal/modeladmin/testmain_test.go` (fail `runOllamaShow` by default)
- Create: `wt/internal/modeladmin/apply_test.go`, `wt/internal/modeladmin/ollama_test.go`
- Create: `wt/internal/modeladmin/fields.go`, `wt/internal/modeladmin/apply.go`, `wt/internal/modeladmin/ollama.go`

**Interfaces:**
- Consumes: `config.UpdateRegistry`, `(*config.RegistryDoc).AddModel`, `PatchModel`, `RemoveModel`, `Models()`, `Providers()`, `config.SeedRegistryDefaults(d, env) (added, unseeded []string, err error)` (with Task 6's openrouter rule), `config.SeedEnv`, `config.DiscoveredModelID`, `config.ParseFilterList`, `config.ErrModelExists`, `config.ErrModelNotFound`, `config.ErrRegistryInvalid` (with Task 6's provider check), `localmodels.Family`, `localmodels.RunningOnly`, `localmodels.FamilyOrigin(cfg, family) (origin string, fromRegistry bool)`
- Produces (package `modeladmin`):
  - Field names: `FieldProvider = "provider"`, `FieldName = "name"`, `FieldID = "id"`, `FieldFamily = "family"`, `FieldTags = "tags"`, `FieldLocation = "location"`, `FieldInputPrice = "input-price"`, `FieldCachePrice = "cache-price"`, `FieldOutputPrice = "output-price"`, `FieldSubscriptionPrice = "subscription-price"`, `FieldSubscriptionPeriod = "subscription-period"`
  - `type FieldError struct { Field, Msg string }` (`Error()` returns `Msg`)
  - `type Fields struct { Family, Tags, Location, InputPrice, CachePrice, OutputPrice, SubscriptionPrice, SubscriptionPeriod *string }` — nil is "not given"; `func (f Fields) Empty() bool`
  - `func ParsePrice(field, text string) (price float64, given bool, err error)`
  - `func DeriveID(providerID, modelName string) string` — `config.DiscoveredModelID` for a provider wt probes; otherwise `<provider>/<name>` with each `/` in the name written `--`, native providers included
  - `type AddRequest struct { ProviderID, ModelName, ID string; Fields; ModelInfo map[string]any }`
  - `type AddResult struct { ID string; ProvidersAdded, ProvidersUnseeded, Warnings []string }`
  - `func CheckAdd(req AddRequest) error` — what `Add` refuses without reading the registry (a missing provider, name or family; a value that cannot be used; a subscription price with no period; a given id that is not `<provider>/<name>` with no spaces, as a `FieldError` on `FieldID`). For a caller with something slow to do before the write
  - `func Add(req AddRequest, env config.SeedEnv) (AddResult, error)` — makes `CheckAdd`'s checks itself, then the locked write
  - `func Edit(id string, f Fields) (changed bool, err error)` — an edit of a price or a subscription field also moves a `cost.kind` table to the current layout, and removes a `[models.cost]` table it emptied
  - unexported, for Task 18: `func sameArtifact(models []*tomlw.Table, providerID, modelName string) string` — the id of a row with the same provider family and `model_name`, or `""` (always `""` for a provider wt has no probe for); `type addPlan struct { req AddRequest; id string; set map[string]any }` and `func planAdd(req AddRequest) (addPlan, error)`, the checks `CheckAdd` and `Add` share; `func mustGet(t *tomlw.Table, key string) any`, `func tableString(t *tomlw.Table, key string) string`
  - `func Remove(ids []string) error`
  - `func UsesOllama(providerID string) bool`
  - `func OllamaCapabilities(cfg *config.Config, name string) (map[string]any, error)` — `cfg` may be nil
  - the seam `runOllamaShow func(ctx context.Context, host, name string) (string, error)`
  - In this PR `Add` refuses an mlx_lm_server provider (`FieldProvider`); Task 18 replaces that with pairing creation.

- [ ] **Step 1: Keep the tests off the developer's ollama**

```diff
--- a/wt/internal/modeladmin/testmain_test.go
+++ b/wt/internal/modeladmin/testmain_test.go
@@ -1,6 +1,8 @@
 package modeladmin
 
 import (
+	"context"
+	"errors"
 	"os"
 	"testing"
 
@@ -13,6 +15,10 @@ import (
 // the registry would edit the real one.
 func TestMain(m *testing.M) {
 	_, cleanup := config.IsolateConfigHomeForTest()
+	// No test may run the developer's ollama; stubOllamaShow supplies output.
+	runOllamaShow = func(context.Context, string, string) (string, error) {
+		return "", errors.New("runOllamaShow not stubbed in this test")
+	}
 	code := m.Run()
 	cleanup()
 	os.Exit(code)
```

- [ ] **Step 2: Write the failing tests**

Create `wt/internal/modeladmin/apply_test.go`:

```go
package modeladmin

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// baseRegistry is a registry in the form wt and modelman write it: an ollama
// and an openrouter provider, and one model with keys wt does not model.
const baseRegistry = `[[providers]]
id = "ollama"
name = "Ollama"
location = "local"

[providers.auth]
type = "none"
base_url = "http://localhost:11434"

[[providers]]
id = "openrouter"
name = "OpenRouter"
location = "cloud"

[providers.auth]
type = "api_key"
secret_ref = "OPENROUTER_API_KEY"
base_url = "https://openrouter.ai/api/v1"

[[models]]
catalog_name = "kept"
id = "ollama/gemma4:9b"
family = "gemma4"
provider_id = "ollama"
model_name = "gemma4:9b"
tags = [
    "code",
]

[models.cost]
input_price_per_million = 3
output_price_per_million = 15

[[models.cost.time_prices]]
label = "off-peak"
timezone = "UTC"
input_price_per_million = 1

[[models.cost.time_prices.windows]]
days = [
    "sat",
    "sun",
]
start = "00:00"
end = "24:00"

[models.model_info]
supports_function_calling = true

[models.fetch]
repo = "org/gemma4"
`

// scratchRegistry points wt at a registry file under a temp directory,
// holding content ("" for no file), and returns its path.
func scratchRegistry(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "local-ai", "registry.toml")
	t.Setenv("WT_REGISTRY", path)
	if content != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func ptr(s string) *string { return &s }

// TestDeriveID pins the ids `wt model add` gives: a local model gets the id
// wt already routes and keeps history under for that artifact (so
// registering a model that is on disk does not rename it), and a cloud model
// gets modelman's spelling, "/" in the name as "--", so ids agree across the
// two tools while both exist. The last case is the one place the two differ:
// modelman's form kept a native provider's model name as it was, and wt
// writes "--" there as for any cloud provider, so a derived cloud id always
// has one "/".
func TestDeriveID(t *testing.T) {
	cases := []struct{ provider, name, want string }{
		{"ollama", "gemma4:9b", "ollama/gemma4:9b"},
		{"omlx", "Qwen3.8-27B-4bit", "omlx/Qwen3.8-27B-4bit"},
		{"omlx-6bit", "Qwen3.8-27B-6bit", "omlx/Qwen3.8-27B-6bit"},
		{"mtplx", "Youssofal/Qwen3.8-MTPLX", "mtplx/Youssofal/Qwen3.8-MTPLX"},
		{"openrouter", "qwen/qwen3.8-27b", "openrouter/qwen--qwen3.8-27b"},
		{"claude", "opus", "claude/opus"},
		{"claude", "org/model", "claude/org--model"},
	}
	for _, c := range cases {
		if got := DeriveID(c.provider, c.name); got != c.want {
			t.Errorf("DeriveID(%q, %q) = %q, want %q", c.provider, c.name, got, c.want)
		}
	}
	// A registered local model keeps the id of the discovered row it was.
	if got, want := DeriveID("omlx-6bit", "X"), config.DiscoveredModelID("omlx-6bit", "X"); got != want {
		t.Errorf("DeriveID = %q, config.DiscoveredModelID = %q; they must agree", got, want)
	}
}

// TestAddWritesOneRowInModelmansLayout verifies an add appends exactly the
// row asked for, with its keys where modelman writes them, and leaves every
// other byte of the file alone. A row laid out differently would be moved by
// modelman's next save, and a rewritten neighbour is a lost hand edit.
func TestAddWritesOneRowInModelmansLayout(t *testing.T) {
	path := scratchRegistry(t, baseRegistry)
	res, err := Add(AddRequest{
		ProviderID: "openrouter", ModelName: "qwen/qwen3.8-27b",
		Fields: Fields{
			Family: ptr("qwen3.8"), Tags: ptr("code, design"), InputPrice: ptr("0.5"), OutputPrice: ptr("2"),
			SubscriptionPrice: ptr("20"), SubscriptionPeriod: ptr("month"),
		},
	}, config.SeedEnv{})
	if err != nil {
		t.Fatal(err)
	}
	if res.ID != "openrouter/qwen--qwen3.8-27b" || len(res.ProvidersAdded) != 0 || len(res.Warnings) != 0 {
		t.Errorf("result = %+v", res)
	}
	want := baseRegistry + `
[[models]]
id = "openrouter/qwen--qwen3.8-27b"
family = "qwen3.8"
provider_id = "openrouter"
model_name = "qwen/qwen3.8-27b"
tags = [
    "code",
    "design",
]

[models.cost]
input_price_per_million = 0.5
output_price_per_million = 2.0
subscription_price = 20.0
subscription_period = "month"
`
	if got := read(t, path); got != want {
		t.Errorf("registry after the add =\n%s\nwant\n%s", got, want)
	}
}

// TestAddSeedsTheProviderRowInTheSameWrite verifies the first add on a
// machine with no registry creates the file with the model and the default
// row of the provider it names — one write, and a registry wt can load. An
// add that left the provider out would be refused by its own validation.
func TestAddSeedsTheProviderRowInTheSameWrite(t *testing.T) {
	path := scratchRegistry(t, "")
	res, err := Add(AddRequest{
		ProviderID: "omlx", ModelName: "Qwen3.8-27B-4bit", Fields: Fields{Family: ptr("qwen3.8")},
		ModelInfo: map[string]any{"supports_function_calling": true},
	}, config.SeedEnv{})
	if err != nil {
		t.Fatal(err)
	}
	if res.ID != "omlx/Qwen3.8-27B-4bit" || strings.Join(res.ProvidersAdded, ",") != "omlx" {
		t.Errorf("result = %+v, want the discovered id and the omlx row seeded", res)
	}
	want := `[[providers]]
id = "omlx"
name = "oMLX"
location = "local"

[providers.auth]
type = "none"
base_url = "http://localhost:8000"

[[models]]
id = "omlx/Qwen3.8-27B-4bit"
family = "qwen3.8"
provider_id = "omlx"
model_name = "Qwen3.8-27B-4bit"
tags = []

[models.model_info]
supports_function_calling = true
`
	if got := read(t, path); got != want {
		t.Errorf("new registry =\n%s\nwant\n%s", got, want)
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, err := config.Load(); err != nil {
		t.Errorf("wt cannot load the registry it just wrote: %v", err)
	}

	// openrouter is the one cloud provider wt has a default row for, and an
	// add that names it gets the row too: `wt model add openrouter …` is the
	// first thing a user with a key runs.
	t.Run("openrouter", func(t *testing.T) {
		path := scratchRegistry(t, "")
		res, err := Add(AddRequest{ProviderID: "openrouter", ModelName: "qwen/qwen3.8-27b", Fields: Fields{Family: ptr("qwen3.8")}}, config.SeedEnv{})
		if err != nil {
			t.Fatal(err)
		}
		if res.ID != "openrouter/qwen--qwen3.8-27b" || strings.Join(res.ProvidersAdded, ",") != "openrouter" || len(res.Warnings) != 0 {
			t.Errorf("result = %+v, want the openrouter row seeded and no warning", res)
		}
		if got := read(t, path); !strings.Contains(got, "id = \"openrouter\"\nname = \"OpenRouter\"\nlocation = \"cloud\"\n\n[providers.auth]\ntype = \"api_key\"\nsecret_ref = \"OPENROUTER_API_KEY\"\n") {
			t.Errorf("the seeded row should name the key's environment variable, never a key:\n%s", got)
		}
	})
}

// TestAddRefusals verifies each thing an add refuses, that the refusal names
// the field at fault (the form puts its cursor there), and that a refused add
// writes nothing.
func TestAddRefusals(t *testing.T) {
	ok := func() AddRequest {
		return AddRequest{ProviderID: "openrouter", ModelName: "a/b", Fields: Fields{Family: ptr("f")}}
	}
	cases := []struct {
		name   string
		change func(*AddRequest)
		field  string
		want   string
	}{
		{"no provider", func(r *AddRequest) { r.ProviderID = " " }, FieldProvider, "a provider is required"},
		{"no name", func(r *AddRequest) { r.ModelName = "" }, FieldName, "a model name is required"},
		{"no family", func(r *AddRequest) { r.Family = nil }, FieldFamily, "family is required"},
		{"a blank family", func(r *AddRequest) { r.Family = ptr("  ") }, FieldFamily, "family is required"},
		{"a location that is not one", func(r *AddRequest) { r.Location = ptr("mars") }, FieldLocation, `location must be local or cloud, got "mars"`},
		{"a price that is not a number", func(r *AddRequest) { r.InputPrice = ptr("cheap") }, FieldInputPrice, `input-price must be a number, got "cheap"`},
		{"a negative price", func(r *AddRequest) { r.OutputPrice = ptr("-1") }, FieldOutputPrice, "output-price must not be negative"},
		{"an infinite price", func(r *AddRequest) { r.CachePrice = ptr("inf") }, FieldCachePrice, "cache-price must be finite"},
		{"a price too large to hold", func(r *AddRequest) { r.CachePrice = ptr("1e999") }, FieldCachePrice, "cache-price must be finite"},
		{"NaN", func(r *AddRequest) { r.SubscriptionPrice = ptr("nan") }, FieldSubscriptionPrice, "subscription-price must be finite"},
		{"a subscription with no period", func(r *AddRequest) { r.SubscriptionPrice = ptr("20") }, FieldSubscriptionPeriod, "a subscription price needs a subscription period (month or year)"},
		{"a period that is not one", func(r *AddRequest) { r.SubscriptionPeriod = ptr("week") }, FieldSubscriptionPeriod, `subscription period must be month or year, got "week"`},
		{"a pairing", func(r *AddRequest) { r.ProviderID = "mlx_lm_server" }, FieldProvider, "target+draft pairing"},
		{"an id with no slash", func(r *AddRequest) { r.ID = "noslash" }, FieldID, `an id is <provider>/<name> with no spaces, got "noslash"`},
		{"an id with a space", func(r *AddRequest) { r.ID = "openrouter/has space" }, FieldID, `an id is <provider>/<name> with no spaces, got "openrouter/has space"`},
		{"an id with nothing after the slash", func(r *AddRequest) { r.ID = "openrouter/" }, FieldID, "an id is <provider>/<name>"},
		{"an id with a control character", func(r *AddRequest) { r.ID = "openrouter/a\tb" }, FieldID, "an id is <provider>/<name>"},
		{"an artifact that is already registered", func(r *AddRequest) { r.ProviderID, r.ModelName, r.ID = "ollama", "gemma4:9b", "ollama/again" }, FieldName,
			`model "ollama/gemma4:9b" already registers gemma4:9b on ollama`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := scratchRegistry(t, baseRegistry)
			req := ok()
			c.change(&req)
			_, err := Add(req, config.SeedEnv{})
			var fe *FieldError
			if !errors.As(err, &fe) || fe.Field != c.field || !strings.Contains(fe.Msg, c.want) {
				t.Fatalf("err = %v, want a FieldError on %q containing %q", err, c.field, c.want)
			}
			if got := read(t, path); got != baseRegistry {
				t.Error("a refused add changed the registry")
			}
			// CheckAdd refuses the same request without the registry, except
			// the one refusal that needs it.
			if check := CheckAdd(req); (check == nil) != (c.name == "an artifact that is already registered") {
				t.Errorf("CheckAdd = %v; it must refuse what Add refuses without reading the registry, and nothing else", check)
			}
		})
	}
	if err := CheckAdd(ok()); err != nil {
		t.Errorf("CheckAdd of a good request = %v", err)
	}
	t.Run("an id that is taken", func(t *testing.T) {
		path := scratchRegistry(t, baseRegistry)
		req := ok()
		req.ID = "ollama/gemma4:9b"
		if _, err := Add(req, config.SeedEnv{}); !errors.Is(err, config.ErrModelExists) {
			t.Fatalf("err = %v, want config.ErrModelExists", err)
		}
		if got := read(t, path); got != baseRegistry {
			t.Error("a refused add changed the registry")
		}
	})
	t.Run("a provider wt has no row for", func(t *testing.T) {
		path := scratchRegistry(t, baseRegistry)
		req := ok()
		req.ProviderID = "corp-gateway"
		_, err := Add(req, config.SeedEnv{})
		if !errors.Is(err, config.ErrRegistryInvalid) || !strings.Contains(err.Error(), `provider_id "corp-gateway" names no provider row`) ||
			!strings.Contains(err.Error(), `add a [[providers]] block for "corp-gateway" to `+path+" and run this again") {
			t.Fatalf("err = %v, want the refusal and what to do about it", err)
		}
		if got := read(t, path); got != baseRegistry {
			t.Error("a refused add changed the registry")
		}
	})
}

// TestAddWarnsAboutAKeylessProvider verifies an add under an api_key provider
// row with no secret_ref succeeds and says the route will carry an empty key.
// The model is in the registry either way; without the warning its first
// request fails with a 401 nobody can trace to the registry.
func TestAddWarnsAboutAKeylessProvider(t *testing.T) {
	path := scratchRegistry(t, strings.Replace(baseRegistry, "secret_ref = \"OPENROUTER_API_KEY\"\n", "", 1))
	res, err := Add(AddRequest{ProviderID: "openrouter", ModelName: "a/b", Fields: Fields{Family: ptr("f")}}, config.SeedEnv{})
	if err != nil {
		t.Fatal(err)
	}
	want := `provider "openrouter" has no auth.secret_ref, so its LiteLLM routes carry an empty api_key: set it in ` + path
	if len(res.Warnings) != 1 || res.Warnings[0] != want {
		t.Errorf("warnings = %q, want [%q]", res.Warnings, want)
	}
}

// TestEditPatchesOnlyWhatWasGiven verifies an edit changes the named keys and
// nothing else in the row or the file: time_prices, model_info, fetch and a
// key wt does not model all survive, an integer price that was not edited
// stays an integer, and pricing_updated_at is not stamped (that date is `wt
// cloud-sync`'s). An edit that rewrote the row would drop the off-peak prices
// the catalog mirror wrote.
func TestEditPatchesOnlyWhatWasGiven(t *testing.T) {
	path := scratchRegistry(t, baseRegistry)
	changed, err := Edit("ollama/gemma4:9b", Fields{Family: ptr("gemma"), Tags: ptr(""), Location: ptr("cloud"), OutputPrice: ptr("12.5"), CachePrice: ptr("0.3")})
	if err != nil || !changed {
		t.Fatalf("Edit = (%v, %v), want (true, nil)", changed, err)
	}
	want := baseRegistry
	for _, r := range [][2]string{
		{"family = \"gemma4\"", "family = \"gemma\""},
		{"model_name = \"gemma4:9b\"\ntags = [\n    \"code\",\n]\n", "model_name = \"gemma4:9b\"\nlocation = \"cloud\"\ntags = []\n"},
		{"input_price_per_million = 3\noutput_price_per_million = 15\n", "input_price_per_million = 3\ncache_price_per_million = 0.3\noutput_price_per_million = 12.5\n"},
	} {
		if !strings.Contains(want, r[0]) {
			t.Fatalf("fixture error: %q is not in the registry", r[0])
		}
		want = strings.Replace(want, r[0], r[1], 1)
	}
	if got := read(t, path); got != want {
		t.Errorf("registry after the edit =\n%s\nwant\n%s", got, want)
	}

	// The same edit again changes nothing and writes nothing.
	changed, err = Edit("ollama/gemma4:9b", Fields{Family: ptr("gemma"), OutputPrice: ptr("12.5")})
	if err != nil || changed {
		t.Errorf("a repeated edit = (%v, %v), want (false, nil)", changed, err)
	}
	// An empty value clears the key; an empty location inherits the provider's.
	if _, err := Edit("ollama/gemma4:9b", Fields{Location: ptr(""), InputPrice: ptr(" ")}); err != nil {
		t.Fatal(err)
	}
	got := read(t, path)
	if !strings.Contains(got, "model_name = \"gemma4:9b\"\ntags = []\n") || !strings.Contains(got, "[models.cost]\ncache_price_per_million = 0.3\n") {
		t.Errorf("an empty value did not clear its key:\n%s", got)
	}
}

// TestEditClearingTheLastPriceDropsTheCostTable verifies that clearing every
// price of a model leaves no empty [models.cost] header behind, so the row
// reads like one that never had a price — and that an edit which does not
// touch a price leaves an empty table that was already there alone.
func TestEditClearingTheLastPriceDropsTheCostTable(t *testing.T) {
	const model = `[[models]]
id = "openrouter/a--b"
family = "f"
provider_id = "openrouter"
model_name = "a/b"
tags = []
`
	providers := baseRegistry[:strings.Index(baseRegistry, "[[models]]")]
	path := scratchRegistry(t, providers+model+"\n[models.cost]\ninput_price_per_million = 1\noutput_price_per_million = 2\n")
	if _, err := Edit("openrouter/a--b", Fields{InputPrice: ptr("")}); err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); !strings.Contains(got, "[models.cost]\noutput_price_per_million = 2\n") {
		t.Fatalf("one price cleared, one left: the table stays\n%s", got)
	}
	if _, err := Edit("openrouter/a--b", Fields{OutputPrice: ptr("")}); err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); got != providers+model {
		t.Errorf("the last price cleared should take the table with it:\n%s", got)
	}

	path = scratchRegistry(t, providers+model+"\n[models.cost]\n")
	if _, err := Edit("openrouter/a--b", Fields{Family: ptr("g"), InputPrice: ptr("")}); err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); !strings.Contains(got, "family = \"g\"") || !strings.HasSuffix(got, "\n[models.cost]\n") {
		t.Errorf("an empty cost table that was there before the edit is not this edit's to remove:\n%s", got)
	}
}

// TestEditMovesALegacyCostTable verifies an edit of a price on a row still in
// modelman's old cost layout (cost.kind) moves the whole table to the current
// one: modelman reads a table that has `kind` by its old keys only, so a
// current key written beside them is a price wt shows and modelman ignores.
// An edit that touches no price leaves the old layout exactly as it was.
func TestEditMovesALegacyCostTable(t *testing.T) {
	providers := baseRegistry[:strings.Index(baseRegistry, "[[models]]")]
	row := func(cost string) string {
		return providers + "[[models]]\nid = \"openrouter/a--b\"\nfamily = \"f\"\nprovider_id = \"openrouter\"\nmodel_name = \"a/b\"\ntags = []\n" + cost
	}
	const perToken = "\n[models.cost]\nkind = \"per_token\"\nprice_per_million_tokens = 2.5\n"
	cases := []struct {
		name   string
		before string
		edit   Fields
		after  string
	}{
		{"a per-token price is the input and the output price; the edit sets one", perToken, Fields{InputPrice: ptr("9")},
			"\n[models.cost]\ninput_price_per_million = 9.0\noutput_price_per_million = 2.5\n"},
		{"clearing one side keeps the other", perToken, Fields{OutputPrice: ptr("")},
			"\n[models.cost]\ninput_price_per_million = 2.5\n"},
		{"a subscription keeps its price and period under their new names", "\n[models.cost]\nkind = \"subscription\"\nprice_per_period = 20\nperiod = \"month\"\n", Fields{SubscriptionPeriod: ptr("year")},
			"\n[models.cost]\nsubscription_price = 20\nsubscription_period = \"year\"\n"},
		{"a free model gets its first price", "\n[models.cost]\nkind = \"free\"\n", Fields{CachePrice: ptr("0.1")},
			"\n[models.cost]\ncache_price_per_million = 0.1\n"},
		{"an edit of the family leaves the old layout alone", perToken, Fields{Family: ptr("f")}, perToken},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := scratchRegistry(t, row(c.before))
			if _, err := Edit("openrouter/a--b", c.edit); err != nil {
				t.Fatal(err)
			}
			if got := read(t, path); got != row(c.after) {
				t.Errorf("registry after the edit =\n%s\nwant\n%s", got, row(c.after))
			}
		})
	}
	// The subscription rule is judged over the carried values too.
	scratchRegistry(t, row("\n[models.cost]\nkind = \"subscription\"\nprice_per_period = 20\nperiod = \"month\"\n"))
	var fe *FieldError
	if _, err := Edit("openrouter/a--b", Fields{SubscriptionPeriod: ptr("")}); !errors.As(err, &fe) || fe.Field != FieldSubscriptionPeriod {
		t.Errorf("clearing the period under a carried price: err = %v, want a FieldError on the period", err)
	}
}

// TestEditRefusals verifies an edit of a model that is not in the registry,
// a value that cannot be used, and a subscription price left without a
// period are each refused with nothing written.
func TestEditRefusals(t *testing.T) {
	path := scratchRegistry(t, baseRegistry)
	if _, err := Edit("ollama/nope", Fields{Family: ptr("x")}); !errors.Is(err, config.ErrModelNotFound) {
		t.Errorf("unknown id: err = %v, want config.ErrModelNotFound", err)
	}
	var fe *FieldError
	if _, err := Edit("ollama/gemma4:9b", Fields{Family: ptr("")}); !errors.As(err, &fe) || fe.Field != FieldFamily {
		t.Errorf("empty family: err = %v, want a FieldError on family", err)
	}
	if _, err := Edit("ollama/gemma4:9b", Fields{SubscriptionPrice: ptr("20")}); !errors.As(err, &fe) || fe.Field != FieldSubscriptionPeriod {
		t.Errorf("subscription with no period: err = %v, want a FieldError on the period", err)
	}
	if got := read(t, path); got != baseRegistry {
		t.Error("a refused edit changed the registry")
	}
	// With the period given in the same edit, or already in the row, it is fine.
	if _, err := Edit("ollama/gemma4:9b", Fields{SubscriptionPrice: ptr("20"), SubscriptionPeriod: ptr("year")}); err != nil {
		t.Fatal(err)
	}
	if _, err := Edit("ollama/gemma4:9b", Fields{SubscriptionPrice: ptr("25")}); err != nil {
		t.Errorf("a new price beside the period already there: %v", err)
	}
	// And the period cannot then be cleared from under the price.
	if _, err := Edit("ollama/gemma4:9b", Fields{SubscriptionPeriod: ptr("")}); !errors.As(err, &fe) || fe.Field != FieldSubscriptionPeriod {
		t.Errorf("clearing the period under a price: err = %v, want a FieldError on the period", err)
	}
}

// TestRemoveIsAllOrNothing verifies a removal deletes exactly the named rows,
// and that one unknown id removes none of them: `wt model rm a b` must not
// half-apply and leave the user to work out which rows went.
func TestRemoveIsAllOrNothing(t *testing.T) {
	two := baseRegistry + `
[[models]]
id = "openrouter/a--b"
family = "f"
provider_id = "openrouter"
model_name = "a/b"
tags = []
`
	path := scratchRegistry(t, two)
	if err := Remove([]string{"openrouter/a--b", "ollama/nope"}); !errors.Is(err, config.ErrModelNotFound) {
		t.Fatalf("err = %v, want config.ErrModelNotFound", err)
	}
	if got := read(t, path); got != two {
		t.Error("a refused removal changed the registry")
	}
	if err := Remove([]string{"openrouter/a--b"}); err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); got != baseRegistry {
		t.Errorf("after the removal =\n%s\nwant the registry without the row", got)
	}
}

// TestARowWithAGapCanBeRepairedOrRemoved verifies that a registry holding a
// model whose provider has no row — which makes every other wt command report
// a config error — can still be fixed with an edit of another row, an edit
// that repairs the bad row, or its removal. The commands that repair a
// registry must not be blocked by the damage they are there to repair.
func TestARowWithAGapCanBeRepairedOrRemoved(t *testing.T) {
	gap := baseRegistry + `
[[models]]
id = "ghost/old"
family = "f"
provider_id = "ghost"
model_name = "old"
tags = []
`
	scratchRegistry(t, gap)
	if _, err := Edit("ollama/gemma4:9b", Fields{Family: ptr("gemma")}); err != nil {
		t.Errorf("an edit of a good row beside the bad one: %v", err)
	}
	if _, err := Edit("ghost/old", Fields{Family: ptr("g")}); !errors.Is(err, config.ErrRegistryInvalid) {
		t.Errorf("an edit that leaves the bad row bad: err = %v, want config.ErrRegistryInvalid", err)
	}
	if err := Remove([]string{"ghost/old"}); err != nil {
		t.Errorf("removing the bad row: %v", err)
	}
}
```

Create `wt/internal/modeladmin/ollama_test.go`:

```go
package modeladmin

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// ollamaShowText is `ollama show` output in the shape ollama prints it:
// indented sections, each ended by a blank line.
const ollamaShowText = `  Model
    architecture        qwen3
    parameters          8.2B
    context length      40960

  Capabilities
    completion
    tools
    vision

  Parameters
    stop    "<|im_end|>"

  License
    Apache License
`

// stubOllamaShow answers `ollama show` with text (or err) and records the
// host and the model it was asked about.
func stubOllamaShow(t *testing.T, text string, err error) *[2]string {
	t.Helper()
	asked := &[2]string{}
	old := runOllamaShow
	runOllamaShow = func(_ context.Context, host, name string) (string, error) {
		asked[0], asked[1] = host, name
		return text, err
	}
	t.Cleanup(func() { runOllamaShow = old })
	return asked
}

// TestOllamaCapabilitiesReadsToolsAndVision verifies the two capabilities
// LiteLLM has keys for are recorded, nothing else is (completion, and "tools"
// under another heading), and the daemon asked is the registry row's, not the
// shell's OLLAMA_HOST. Without supports_function_calling in the route,
// LiteLLM drops an agent's tool definitions and the agent cannot edit files.
func TestOllamaCapabilitiesReadsToolsAndVision(t *testing.T) {
	asked := stubOllamaShow(t, ollamaShowText+"\n  Notes\n    tools\n", nil)
	cfg := &config.Config{Providers: []config.Provider{
		{ID: "ollama", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://gpu-box:11500"}},
	}}
	info, err := OllamaCapabilities(cfg, "qwen3:8b")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"supports_function_calling": true, "supports_vision": true}
	if !reflect.DeepEqual(info, want) {
		t.Errorf("info = %v, want %v", info, want)
	}
	if asked[0] != "http://gpu-box:11500" || asked[1] != "qwen3:8b" {
		t.Errorf("asked %q about %q, want the registry row's address and the model", asked[0], asked[1])
	}
}

// TestOllamaCapabilitiesWithoutAnyAndWithoutARegistry verifies a model with
// neither capability gives an empty result (no model_info is written), and
// that with no config loaded the default ollama address is asked. The first
// add on a new machine runs before any registry exists.
func TestOllamaCapabilitiesWithoutAnyAndWithoutARegistry(t *testing.T) {
	asked := stubOllamaShow(t, "  Capabilities\n    completion\n", nil)
	info, err := OllamaCapabilities(nil, "tiny:1b")
	if err != nil || len(info) != 0 {
		t.Fatalf("OllamaCapabilities = (%v, %v), want an empty map", info, err)
	}
	if asked[0] != config.OllamaBaseURL {
		t.Errorf("asked %q, want the default %q", asked[0], config.OllamaBaseURL)
	}
}

// TestOllamaCapabilitiesReportsAFailedLookup verifies a failed `ollama show`
// is an error that names the command and the address, so the caller can add
// the model anyway and tell the user what was not recorded and why.
func TestOllamaCapabilitiesReportsAFailedLookup(t *testing.T) {
	stubOllamaShow(t, "", errors.New("exit status 1"))
	_, err := OllamaCapabilities(nil, "nope:1b")
	if err == nil || !strings.Contains(err.Error(), "`ollama show nope:1b` (at "+config.OllamaBaseURL+") failed: exit status 1") {
		t.Fatalf("err = %v, want the command, the address and the cause", err)
	}
}

// TestUsesOllama verifies which providers get the lookup: ollama, and no
// other local or cloud provider.
func TestUsesOllama(t *testing.T) {
	for id, want := range map[string]bool{"ollama": true, "omlx": false, "omlx-6bit": false, "openrouter": false, "": false} {
		if got := UsesOllama(id); got != want {
			t.Errorf("UsesOllama(%q) = %v, want %v", id, got, want)
		}
	}
}
```

- [ ] **Step 3: Run them to see them fail**

Run, from `wt/`: `go test -count=1 ./internal/modeladmin`

Expected: the package does not build.

```text
FAIL	github.com/ohanaverse/local-ai-setup/wt/internal/modeladmin [build failed]
FAIL
# github.com/ohanaverse/local-ai-setup/wt/internal/modeladmin [github.com/ohanaverse/local-ai-setup/wt/internal/modeladmin.test]
internal/modeladmin/apply_test.go:115:13: undefined: DeriveID
internal/modeladmin/apply_test.go:120:18: undefined: DeriveID
internal/modeladmin/apply_test.go:131:14: undefined: Add
```

- [ ] **Step 4: Write the fields and the id rule**

Create `wt/internal/modeladmin/fields.go`. The price rule is modelman's `_parse_price` (`screens/forms.py:209`): empty is "no price", otherwise a finite number that is not negative.

```go
package modeladmin

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// The names of a model's inputs, as FieldError reports them. They are the
// CLI's flag names, so an error reads the same from a flag and from the form.
const (
	FieldProvider           = "provider"
	FieldName               = "name"
	FieldID                 = "id"
	FieldFamily             = "family"
	FieldTags               = "tags"
	FieldLocation           = "location"
	FieldInputPrice         = "input-price"
	FieldCachePrice         = "cache-price"
	FieldOutputPrice        = "output-price"
	FieldSubscriptionPrice  = "subscription-price"
	FieldSubscriptionPeriod = "subscription-period"
)

// FieldError is a value the user gave that cannot be used. Field names the
// input, so the form can put the cursor on it.
type FieldError struct {
	Field string
	Msg   string
}

func (e *FieldError) Error() string { return e.Msg }

func fieldErr(field, format string, args ...any) error {
	return &FieldError{Field: field, Msg: fmt.Sprintf(format, args...)}
}

// Fields are a model's editable fields as the user typed them: a flag's
// value or a form input. A nil field was not given and is left alone. An
// empty one clears the key, except Family, which a model must have.
type Fields struct {
	Family             *string
	Tags               *string // comma-separated
	Location           *string // local, cloud, or "" to inherit the provider's
	InputPrice         *string // $ per million tokens
	CachePrice         *string
	OutputPrice        *string
	SubscriptionPrice  *string
	SubscriptionPeriod *string // month or year
}

// Empty reports whether no field was given.
func (f Fields) Empty() bool {
	return f.Family == nil && f.Tags == nil && f.Location == nil && f.InputPrice == nil && f.CachePrice == nil &&
		f.OutputPrice == nil && f.SubscriptionPrice == nil && f.SubscriptionPeriod == nil
}

// priceKeys maps each price field to its key in the model row.
var priceKeys = []struct{ field, key string }{
	{FieldInputPrice, "cost.input_price_per_million"},
	{FieldCachePrice, "cost.cache_price_per_million"},
	{FieldOutputPrice, "cost.output_price_per_million"},
	{FieldSubscriptionPrice, "cost.subscription_price"},
}

func (f Fields) price(field string) *string {
	switch field {
	case FieldInputPrice:
		return f.InputPrice
	case FieldCachePrice:
		return f.CachePrice
	case FieldOutputPrice:
		return f.OutputPrice
	}
	return f.SubscriptionPrice
}

// patch turns the fields into the keys to set and the keys to delete on a
// model row (config.RegistryDoc.PatchModel's arguments). It checks each field
// by itself; the one rule that needs two fields is checkSubscription's.
func (f Fields) patch() (set map[string]any, unset []string, err error) {
	set = map[string]any{}
	if f.Family != nil {
		family := strings.TrimSpace(*f.Family)
		if family == "" {
			return nil, nil, fieldErr(FieldFamily, "family is required")
		}
		set["family"] = family
	}
	if f.Tags != nil {
		tags := config.ParseFilterList(*f.Tags)
		if tags == nil {
			tags = []string{}
		}
		set["tags"] = tags
	}
	if f.Location != nil {
		switch loc := strings.TrimSpace(*f.Location); loc {
		case "":
			unset = append(unset, "location")
		case string(config.LocationLocal), string(config.LocationCloud):
			set["location"] = loc
		default:
			return nil, nil, fieldErr(FieldLocation, "location must be local or cloud, got %q", loc)
		}
	}
	for _, p := range priceKeys {
		text := f.price(p.field)
		if text == nil {
			continue
		}
		price, given, err := ParsePrice(p.field, *text)
		if err != nil {
			return nil, nil, err
		}
		if given {
			set[p.key] = price
		} else {
			unset = append(unset, p.key)
		}
	}
	if f.SubscriptionPeriod != nil {
		switch period := strings.TrimSpace(*f.SubscriptionPeriod); period {
		case "":
			unset = append(unset, "cost.subscription_period")
		case "month", "year":
			set["cost.subscription_period"] = period
		default:
			return nil, nil, fieldErr(FieldSubscriptionPeriod, "subscription period must be month or year, got %q", period)
		}
	}
	return set, unset, nil
}

// ParsePrice reads a price a user typed: empty is "no price" (given false);
// otherwise a finite number that is not negative. modelman's rule
// (screens/forms.py _parse_price), with the field named as the flag is.
func ParsePrice(field, text string) (price float64, given bool, err error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0, false, nil
	}
	price, perr := strconv.ParseFloat(text, 64)
	switch {
	case perr != nil && !math.IsInf(price, 0):
		return 0, false, fieldErr(field, "%s must be a number, got %q", field, text)
	case math.IsNaN(price) || math.IsInf(price, 0):
		return 0, false, fieldErr(field, "%s must be finite", field)
	case price < 0:
		return 0, false, fieldErr(field, "%s must not be negative", field)
	}
	return price, true, nil
}

// checkSubscription is the one rule over two fields: a subscription price
// needs a period. price and period are the row's values once the patch is
// applied.
func checkSubscription(hasPrice bool, period string) error {
	if hasPrice && period != "month" && period != "year" {
		return fieldErr(FieldSubscriptionPeriod, "a subscription price needs a subscription period (month or year)")
	}
	return nil
}

// DeriveID is the id `wt model add` gives a model when --id does not. A model
// of a local provider gets config.DiscoveredModelID: the id wt already lists,
// routes and keeps history under for that artifact, so registering a model
// that is on disk does not change its id. Any other provider gets modelman's
// rule for a cloud gateway, the provider and the name with each "/" in the
// name spelled "--", so ids agree across the two tools and existing history
// keeps matching. One case differs from modelman: its form kept the name of
// a native provider's model (auth.type "native": an agent's own provider
// row, such as claude) as it was, slashes and all. wt writes "--" there too,
// so that every derived cloud id has one "/"; --id gives any other spelling.
func DeriveID(providerID, modelName string) string {
	if localmodels.Family(providerID) != "" {
		return config.DiscoveredModelID(providerID, modelName)
	}
	return providerID + "/" + strings.ReplaceAll(modelName, "/", "--")
}
```

- [ ] **Step 5: Write the three writes**

Create `wt/internal/modeladmin/apply.go`. Each of `Add`, `Edit` and `Remove` is one `config.UpdateRegistry`. `Add` adds the model row first and seeds second, so that seeding sees the provider the model references; Task 6's check then runs over the document as it is after both. Everything `Add` can refuse without the registry is in `planAdd`, which `CheckAdd` exposes. `Edit`'s `apply` copies what the user gave before it adds to it: `apply` can run three times, and each run starts from the same request.

```go
package modeladmin

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tomlw"
)

// AddRequest is one model to add to the registry.
type AddRequest struct {
	ProviderID string
	// ModelName is the name as the provider lists it: an ollama tag, an omlx
	// model directory's name, an mtplx org/name, a cloud provider's model id.
	ModelName string
	// ID overrides the derived id (DeriveID) when not empty.
	ID string
	Fields
	// ModelInfo is written as the row's model_info table when not empty: the
	// capabilities looked up before the write (OllamaCapabilities).
	ModelInfo map[string]any
}

// AddResult is what an add did.
type AddResult struct {
	ID string
	// ProvidersAdded are the provider rows seeding added in the same write;
	// ProvidersUnseeded the providers a configured agent lists that wt has no
	// default row for (config.SeedRegistryDefaults).
	ProvidersAdded    []string
	ProvidersUnseeded []string
	Warnings          []string
}

// addPlan is an add that passed every check that needs no registry: the
// request with its text trimmed, the id the row gets, and the row's keys.
type addPlan struct {
	req AddRequest
	id  string
	set map[string]any
}

// planAdd checks a request by itself — what is required, each field's value,
// the subscription rule, the shape of a given id — and works out the row.
func planAdd(req AddRequest) (addPlan, error) {
	req.ProviderID, req.ModelName, req.ID = strings.TrimSpace(req.ProviderID), strings.TrimSpace(req.ModelName), strings.TrimSpace(req.ID)
	switch {
	case req.ProviderID == "":
		return addPlan{}, fieldErr(FieldProvider, "a provider is required")
	case req.ModelName == "":
		return addPlan{}, fieldErr(FieldName, "a model name is required")
	case req.Family == nil:
		return addPlan{}, fieldErr(FieldFamily, "family is required")
	case localmodels.RunningOnly(req.ProviderID):
		return addPlan{}, fieldErr(FieldProvider, "an mlx_lm_server model is a target+draft pairing, which this command cannot add yet: add it to %s by hand", config.RegistryPath())
	}
	if err := checkID(req.ID); err != nil {
		return addPlan{}, err
	}
	set, _, err := req.Fields.patch()
	if err != nil {
		return addPlan{}, err
	}
	_, hasPrice := set["cost.subscription_price"]
	period, _ := set["cost.subscription_period"].(string)
	if err := checkSubscription(hasPrice, period); err != nil {
		return addPlan{}, err
	}
	if _, ok := set["tags"]; !ok {
		set["tags"] = []string{} // every row modelman writes has the key
	}
	if len(req.ModelInfo) > 0 {
		set["model_info"] = req.ModelInfo
	}
	id := req.ID
	if id == "" {
		id = DeriveID(req.ProviderID, req.ModelName)
	}
	return addPlan{req: req, id: id, set: set}, nil
}

// checkID refuses a given id that is not <provider>/<name>: a part on each
// side of the first "/", and no space or control character anywhere. The
// rest of wt takes that shape for granted — `wt stop <arg>` reads an argument
// with no "/" as a provider, and an id with a space cannot be passed to -M
// unquoted. "" (derive the id) is fine.
func checkID(id string) error {
	if id == "" {
		return nil
	}
	provider, name, ok := strings.Cut(id, "/")
	bad := strings.ContainsFunc(id, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })
	if !ok || provider == "" || name == "" || bad {
		return fieldErr(FieldID, "an id is <provider>/<name> with no spaces, got %q", id)
	}
	return nil
}

// CheckAdd reports what Add would refuse without reading the registry: a
// missing provider, name or family, a value that cannot be used, a
// subscription price with no period, an id of the wrong shape. Add makes the
// same checks; this is for a caller that has something slow to do before the
// write (the `ollama show` lookup), so that a request that is going to be
// refused anyway does not wait on it first. A taken id and an artifact that
// is already registered need the registry, and are Add's to refuse.
func CheckAdd(req AddRequest) error {
	_, err := planAdd(req)
	return err
}

// Add appends one model row and seeds any missing default provider row, in
// one locked registry write. It runs no route sync: the caller does, once.
// The registry is created when it is missing.
func Add(req AddRequest, env config.SeedEnv) (AddResult, error) {
	plan, err := planAdd(req)
	if err != nil {
		return AddResult{}, err
	}
	req, id, set := plan.req, plan.id, plan.set
	res := AddResult{ID: id}
	_, err = config.UpdateRegistry(func(d *config.RegistryDoc) error {
		// Everything assigned here is assigned afresh: apply may run again.
		res = AddResult{ID: id}
		if other := sameArtifact(d.Models(), req.ProviderID, req.ModelName); other != "" {
			return fieldErr(FieldName, "model %q already registers %s on %s; edit that one (wt model edit %s)", other, req.ModelName, req.ProviderID, other)
		}
		if err := d.AddModel(map[string]any{
			"id": id, "family": set["family"], "provider_id": req.ProviderID, "model_name": req.ModelName,
		}); err != nil {
			return err
		}
		rest := map[string]any{}
		for k, v := range set {
			if k != "family" {
				rest[k] = v
			}
		}
		if err := d.PatchModel(id, rest, nil); err != nil {
			return err
		}
		// After the model is in: seeding adds the default row of a provider
		// a model references.
		var err error
		res.ProvidersAdded, res.ProvidersUnseeded, err = config.SeedRegistryDefaults(d, env)
		if err != nil {
			return err
		}
		res.Warnings = providerWarnings(d.Providers(), req.ProviderID)
		return nil
	})
	if err != nil {
		return AddResult{}, describe(err, req.ProviderID)
	}
	return res, nil
}

// sameArtifact returns the id of a model row that already stands for the
// same local artifact: the same provider family and model_name. The
// inventory gives an artifact to the first row that matches it, so a second
// row would read as "missing" for ever. "" for a provider with no probe (a
// cloud provider may list one model under two ids, at two prices).
func sameArtifact(models []*tomlw.Table, providerID, modelName string) string {
	family := localmodels.Family(providerID)
	if family == "" {
		return ""
	}
	for _, m := range models {
		if localmodels.Family(tableString(m, "provider_id")) == family && tableString(m, "model_name") == modelName {
			return tableString(m, "id")
		}
	}
	return ""
}

// providerWarnings names what is wrong with the provider row a model was
// just added under, when wt can route the model only once it is repaired.
func providerWarnings(providers []*tomlw.Table, providerID string) []string {
	for _, p := range providers {
		if tableString(p, "id") != providerID {
			continue
		}
		auth, _ := p.Get("auth")
		at, _ := auth.(*tomlw.Table)
		if at != nil && tableString(at, "type") == "api_key" && tableString(at, "secret_ref") == "" {
			return []string{fmt.Sprintf("provider %q has no auth.secret_ref, so its LiteLLM routes carry an empty api_key: set it in %s", providerID, config.RegistryPath())}
		}
	}
	return nil
}

func mustGet(t *tomlw.Table, key string) any {
	v, _ := t.Get(key)
	return v
}

func tableString(t *tomlw.Table, key string) string {
	v, _ := t.Get(key)
	s, _ := v.(string)
	return s
}

// describe adds what a user can do about a registry refusal that names a
// provider with no row. Seeding added none: a model's reference seeds only
// the providers wt has a default row for, and wt has no command that writes
// any other provider row.
func describe(err error, providerID string) error {
	if errors.Is(err, config.ErrRegistryInvalid) && strings.Contains(err.Error(), "names no provider row") {
		return fmt.Errorf("%w: add a [[providers]] block for %q to %s and run this again (wt adds a default row by itself only for ollama, omlx, mtplx, mlx_lm_server and openrouter)",
			err, providerID, config.RegistryPath())
	}
	return err
}

// Edit patches the given fields of model id in one locked registry write and
// reports whether the file changed. The id, the provider and the model name
// are not editable: usage history, rotation and launch profiles key on the
// id, and the inventory matches a row to its weights by provider and name.
// It does not stamp pricing_updated_at; `wt cloud-sync` owns that date.
//
// Two things about the row's cost table follow from an edit of a price or of
// a subscription field, and only from one: a table still in modelman's old
// layout is moved to the current one (legacyCost), and a table the edit
// emptied goes with its last key, so a model whose prices were all cleared
// reads like one that never had any.
func Edit(id string, f Fields) (changed bool, err error) {
	given, givenUnset, err := f.patch()
	if err != nil {
		return false, err
	}
	return config.UpdateRegistry(func(d *config.RegistryDoc) error {
		// Copies: apply may run again, and each run starts from what the
		// user gave.
		set, unset := maps.Clone(given), slices.Clone(givenUnset)
		cost := tomlw.NewTable()
		for _, m := range d.Models() {
			if tableString(m, "id") == id {
				if t, ok := mustGet(m, "cost").(*tomlw.Table); ok {
					cost = t
				}
			}
		}
		costKey := func(k string) bool { return strings.HasPrefix(k, "cost.") }
		setsCost := func() bool { return slices.ContainsFunc(slices.Collect(maps.Keys(set)), costKey) }
		if setsCost() || slices.ContainsFunc(unset, costKey) {
			unset = append(unset, legacyCost(cost, set, unset)...)
			// The subscription rule, over the row as the patch leaves it.
			after := cost.Clone()
			for _, k := range unset {
				after.Delete(strings.TrimPrefix(k, "cost."))
			}
			hasPrice, period := after.Has("subscription_price"), tableString(after, "subscription_period")
			if _, ok := set["cost.subscription_price"]; ok {
				hasPrice = true
			}
			if p, ok := set["cost.subscription_period"].(string); ok {
				period = p
			}
			if err := checkSubscription(hasPrice, period); err != nil {
				return err
			}
			if cost.Len() > 0 && after.Len() == 0 && !setsCost() {
				unset = append(unset, "cost")
			}
		}
		return d.PatchModel(id, set, unset)
	})
}

// legacyCost moves a cost table in modelman's old layout (cost.kind, with
// the price under price_per_million_tokens or price_per_period and the
// period under period) to the current one, as modelman's own loader reads it:
// a per-token price is the input and the output price, a subscription's
// price and period take their current names. It adds the carried values to
// set — never over a key this edit sets or clears — and returns the old keys
// to delete. modelman reads a table that has `kind` by its old keys alone, so
// a current key written beside them would be a price wt shows and modelman
// ignores. Nothing for a table that is not in the old layout.
func legacyCost(cost *tomlw.Table, set map[string]any, unset []string) []string {
	kind, isLegacy := cost.Get("kind")
	if !isLegacy {
		return nil
	}
	carry := func(from string, to ...string) {
		v, ok := cost.Get(from)
		if !ok {
			return
		}
		for _, key := range to {
			if _, given := set[key]; !given && !slices.Contains(unset, key) {
				set[key] = v
			}
		}
	}
	switch kind {
	case "per_token":
		carry("price_per_million_tokens", "cost.input_price_per_million", "cost.output_price_per_million")
	case "subscription":
		carry("price_per_period", "cost.subscription_price")
		carry("period", "cost.subscription_period")
	}
	return []string{"cost.kind", "cost.price_per_million_tokens", "cost.price_per_period", "cost.period"}
}

// Remove deletes the model rows ids from the registry in one locked write:
// all of them, or — when one is not there — none. It touches nothing else:
// no weights, no [[families]] entry, no route (the caller syncs).
func Remove(ids []string) error {
	_, err := config.UpdateRegistry(func(d *config.RegistryDoc) error {
		for _, id := range ids {
			if _, err := d.RemoveModel(id); err != nil {
				return err
			}
		}
		return nil
	})
	return err
}
```

- [ ] **Step 6: Write the capability lookup**

Create `wt/internal/modeladmin/ollama.go`. It is modelman's `ollama_caps.py`, with the daemon pinned by `OLLAMA_HOST`:

```go
package modeladmin

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// ollamaShowTimeout bounds the one `ollama show` an add runs.
const ollamaShowTimeout = 10 * time.Second

// runOllamaShow runs `ollama show <name>` against the daemon at host and
// returns its text. A seam: tests never run the developer's ollama.
var runOllamaShow = func(ctx context.Context, host, name string) (string, error) {
	cmd := exec.CommandContext(ctx, "ollama", "show", name)
	// Pinned: without it the CLI asks whatever OLLAMA_HOST the shell has, or
	// the default port, which need not be the daemon the registry names.
	cmd.Env = append(os.Environ(), "OLLAMA_HOST="+host)
	out, err := cmd.Output()
	return string(out), err
}

// ollamaCapabilityKeys maps a capability `ollama show` lists to the
// model_info key LiteLLM reads (modelman's ollama_caps.py).
var ollamaCapabilityKeys = map[string]string{
	"tools":  "supports_function_calling",
	"vision": "supports_vision",
}

// UsesOllama reports whether a provider's models are ollama's, so an add of
// one can ask ollama what the model can do.
func UsesOllama(providerID string) bool { return localmodels.Family(providerID) == "ollama" }

// OllamaCapabilities asks ollama what model name can do and returns the
// model_info keys to record: supports_function_calling and supports_vision,
// each present (true) only when ollama lists the capability. wt copies
// model_info into the LiteLLM route, and an agent that sends tools to a
// route without supports_function_calling has them dropped. The daemon asked
// is the one the registry's ollama provider row names (cfg may be nil: the
// default address). An error means the lookup did not happen; the caller
// adds the model without the keys and says so.
func OllamaCapabilities(cfg *config.Config, name string) (map[string]any, error) {
	if cfg == nil {
		cfg = &config.Config{}
	}
	host, _ := localmodels.FamilyOrigin(cfg, "ollama")
	ctx, cancel := context.WithTimeout(context.Background(), ollamaShowTimeout)
	defer cancel()
	out, err := runOllamaShow(ctx, host, name)
	if err != nil {
		return nil, fmt.Errorf("`ollama show %s` (at %s) failed: %w", name, host, err)
	}
	return parseOllamaShow(out), nil
}

// parseOllamaShow reads the Capabilities section of `ollama show`'s text: a
// line reading "Capabilities", then one capability per line until a blank
// line.
func parseOllamaShow(text string) map[string]any {
	info := map[string]any{}
	inCaps := false
	for _, line := range strings.Split(text, "\n") {
		word := strings.TrimSpace(line)
		switch {
		case word == "":
			inCaps = false
		case strings.EqualFold(word, "capabilities"):
			inCaps = true
		case inCaps:
			if key, ok := ollamaCapabilityKeys[word]; ok {
				info[key] = true
			}
		}
	}
	return info
}
```

- [ ] **Step 7: Run the package**

Run, from `wt/`: `go test -count=1 ./internal/modeladmin`

Expected: `ok  	github.com/ohanaverse/local-ai-setup/wt/internal/modeladmin`

- [ ] **Step 8: Commit**

```bash
git add wt/internal/modeladmin
git commit -m "feat(wt): modeladmin adds, edits and removes registry models"
```

### Task 8: The commands

**Files:**
- Modify: `wt/cmd/wt/testmain_test.go` (fail the two new seams by default)
- Create: `wt/cmd/wt/model_write_test.go`
- Modify: `wt/cmd/wt/model_list_test.go` (`TestModelListWithNothingToList`)
- Create: `wt/cmd/wt/model_write.go`
- Modify: `wt/cmd/wt/model_cmds.go:43-46` (`promptStop` opens the terminal through `openTTY`)
- Modify: `wt/cmd/wt/model.go` (register the three commands; `wt model init`'s help), `wt/cmd/wt/model_list.go` (the empty listing names `wt model add`)

**Interfaces:**
- Consumes: Task 7's `modeladmin` API; `modeladmin.Rows`, `modeladmin.WeightsNote` (Task 4); existing in `cmd/wt`: `seedEnv func() config.SeedEnv`, `syncRoutesAfterWrite func(out, errOut io.Writer) string`, `probeInventory`, `promptStop(question string) (bool, error)` (`model_cmds.go`), `var openTTY func() (*os.File, error)` (`launch.go`), `app.cfg`, `app.loadErr`, the test helpers `withCleanConfigEnv`, `stubSeedEnv`, `stubRouteSync`, `realRouteSync`, `stubProbeInventory`
- Produces:
  - seams `ollamaCaps = modeladmin.OllamaCapabilities`, `confirmRemove = promptStop` — the prompt `wt stop` asks with, not a copy of it
  - `func modelFieldFlags(c *cobra.Command) func() modeladmin.Fields` — registers `--family`, `--tags`, `--location` and the five price flags (named by the `modeladmin.Field…` constants) and reads only the ones passed
  - `func modelAddCmd(a *app)`, `modelEditCmd(_ *app)`, `modelRmCmd(a *app) *cobra.Command`
  - `func runModelAdd(out, errOut io.Writer, cfg *config.Config, req modeladmin.AddRequest) error` (`cfg` may be nil) — `modeladmin.CheckAdd`, then the ollama lookup, then `modeladmin.Add`
  - `func runModelEdit(out, errOut io.Writer, id string, f modeladmin.Fields) error`
  - `func runModelRm(out, errOut io.Writer, cfg *config.Config, ids []string, yes bool) error` (`cfg` nil: no weights lines)
  - `func syncAndWarn(out, errOut io.Writer, warnings []string) error`
  - test helpers `modelHome(t, content) string`, `mustRead(t, path) string`, `stubOllamaCaps(t, info, err) *int`, `stubConfirmRemove(t, answer) *string`, `sp(s) *string`, `runWT(t, args...) (string, error)`, and the fixture `writeRegistry`
  - Output: `added model: <id>`, `added provider: <id>`, `updated model: <id>`, `no change: <id>`, `removed model: <id>` followed by an indented weights line; warnings on stderr as `warning: …`

- [ ] **Step 1: Fail the new seams by default, and write the failing tests**

```diff
--- a/wt/cmd/wt/testmain_test.go
+++ b/wt/cmd/wt/testmain_test.go
@@ -77,6 +77,12 @@ func TestMain(m *testing.M) {
 	// what to seed, and none may go on from a registry write to the real
 	// route sync (which probes providers and can restart the proxy). Tests of
 	// `wt model init` call stubSeedEnv, and realRouteSync for the sync itself.
+	// `wt model add` seams: no test may run the developer's ollama or wait on
+	// their terminal. Tests call stubOllamaCaps and stubConfirmRemove.
+	ollamaCaps = func(*config.Config, string) (map[string]any, error) {
+		return nil, errors.New("ollamaCaps not stubbed in this test")
+	}
+	confirmRemove = func(string) (bool, error) { return false, errors.New("confirmRemove not stubbed in this test") }
 	seedEnv = func() config.SeedEnv { return config.SeedEnv{} }
 	syncRoutesAfterWrite = func(io.Writer, io.Writer) string {
 		return "syncRoutesAfterWrite not stubbed in this test"
```

Create `wt/cmd/wt/model_write_test.go`:

```go
package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modeladmin"
)

// writeRegistry is a registry with an ollama and an openrouter provider and
// one model of each.
const writeRegistry = `[[providers]]
id = "ollama"
name = "Ollama"
location = "local"

[providers.auth]
type = "none"
base_url = "http://localhost:11434"

[[providers]]
id = "openrouter"
name = "OpenRouter"
location = "cloud"

[providers.auth]
type = "api_key"
secret_ref = "OPENROUTER_API_KEY"
base_url = "https://openrouter.ai/api/v1"

[[models]]
id = "ollama/gemma4:9b"
family = "gemma4"
provider_id = "ollama"
model_name = "gemma4:9b"
tags = []

[[models]]
id = "openrouter/a--b"
family = "f"
provider_id = "openrouter"
model_name = "a/b"
tags = []
`

// modelHome is a throwaway home whose registry holds content ("" for none).
// It returns the registry's path.
func modelHome(t *testing.T, content string) string {
	t.Helper()
	home := t.TempDir()
	withCleanConfigEnv(t, home)
	registry := filepath.Join(home, ".config", "local-ai", "registry.toml")
	if content != "" {
		if err := os.MkdirAll(filepath.Dir(registry), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(registry, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return registry
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// stubOllamaCaps answers the capability lookup and counts its calls.
func stubOllamaCaps(t *testing.T, info map[string]any, err error) *int {
	t.Helper()
	calls := 0
	old := ollamaCaps
	ollamaCaps = func(*config.Config, string) (map[string]any, error) {
		calls++
		return info, err
	}
	t.Cleanup(func() { ollamaCaps = old })
	return &calls
}

// stubConfirmRemove answers `wt model rm`'s question and records it.
func stubConfirmRemove(t *testing.T, answer bool) *string {
	t.Helper()
	asked := new(string)
	old := confirmRemove
	confirmRemove = func(q string) (bool, error) { *asked = q; return answer, nil }
	t.Cleanup(func() { confirmRemove = old })
	return asked
}

func sp(s string) *string { return &s }

// runWT runs the wt command line in-process and returns stdout+stderr.
func runWT(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	root := rootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs(args)
	err := root.Execute()
	return buf.String(), err
}

// TestModelAddOnAFreshMachine verifies the first `wt model add` on a machine
// with no registry: one write creates the file with the model and the default
// row of its provider, both are named in the output, and the routes are
// synced exactly once. This is the first thing a new user does after `wt
// model init`, or instead of it.
func TestModelAddOnAFreshMachine(t *testing.T) {
	registry := modelHome(t, "")
	stubSeedEnv(t, config.SeedEnv{})
	syncs := stubRouteSync(t, "")
	var out, errOut bytes.Buffer
	req := modeladmin.AddRequest{ProviderID: "omlx", ModelName: "Qwen3.8-27B-4bit", Fields: modeladmin.Fields{Family: sp("qwen3.8"), Tags: sp("code")}}
	if err := runModelAdd(&out, &errOut, nil, req); err != nil {
		t.Fatal(err)
	}
	if want := "added model: omlx/Qwen3.8-27B-4bit\nadded provider: omlx\n"; out.String() != want {
		t.Errorf("stdout = %q, want %q", out.String(), want)
	}
	if errOut.Len() != 0 || *syncs != 1 {
		t.Errorf("stderr = %q, syncs = %d; want no warning and one sync", errOut.String(), *syncs)
	}
	got := mustRead(t, registry)
	if !strings.Contains(got, "id = \"omlx/Qwen3.8-27B-4bit\"\nfamily = \"qwen3.8\"\nprovider_id = \"omlx\"") || !strings.Contains(got, "[[providers]]\nid = \"omlx\"") {
		t.Errorf("registry =\n%s", got)
	}
}

// TestModelAddAsksOllamaWhatTheModelCanDo verifies an ollama add records the
// capabilities `ollama show` reports, that a failed lookup still adds the
// model and warns about exactly what is missing, and that a non-ollama add
// never asks. A route without supports_function_calling has an agent's tools
// dropped by LiteLLM.
func TestModelAddAsksOllamaWhatTheModelCanDo(t *testing.T) {
	t.Run("capabilities recorded", func(t *testing.T) {
		registry := modelHome(t, writeRegistry)
		stubRouteSync(t, "")
		calls := stubOllamaCaps(t, map[string]any{"supports_function_calling": true, "supports_vision": true}, nil)
		var out, errOut bytes.Buffer
		req := modeladmin.AddRequest{ProviderID: "ollama", ModelName: "qwen3:8b", Fields: modeladmin.Fields{Family: sp("qwen3")}}
		if err := runModelAdd(&out, &errOut, nil, req); err != nil {
			t.Fatal(err)
		}
		if *calls != 1 || errOut.Len() != 0 {
			t.Errorf("lookups = %d, stderr = %q; want one and no warning", *calls, errOut.String())
		}
		if got := mustRead(t, registry); !strings.Contains(got, "[models.model_info]\nsupports_function_calling = true\nsupports_vision = true\n") {
			t.Errorf("the capabilities are not in the registry:\n%s", got)
		}
	})
	t.Run("lookup failed", func(t *testing.T) {
		registry := modelHome(t, writeRegistry)
		stubRouteSync(t, "")
		stubOllamaCaps(t, nil, errors.New("`ollama show qwen3:8b` (at http://localhost:11434) failed: exit status 1"))
		var out, errOut bytes.Buffer
		req := modeladmin.AddRequest{ProviderID: "ollama", ModelName: "qwen3:8b", Fields: modeladmin.Fields{Family: sp("qwen3")}}
		if err := runModelAdd(&out, &errOut, nil, req); err != nil {
			t.Fatalf("a failed lookup must not fail the add: %v", err)
		}
		want := "warning: added without model_info.supports_function_calling / supports_vision: `ollama show qwen3:8b` (at http://localhost:11434) failed: exit status 1\n"
		if errOut.String() != want {
			t.Errorf("stderr = %q\nwant %q", errOut.String(), want)
		}
		if got := mustRead(t, registry); !strings.Contains(got, `id = "ollama/qwen3:8b"`) || strings.Count(got, "model_info") != 0 {
			t.Errorf("want the model added with no model_info:\n%s", got)
		}
	})
	t.Run("an add that is going to be refused does not ask first", func(t *testing.T) {
		registry := modelHome(t, writeRegistry)
		calls := stubOllamaCaps(t, nil, errors.New("must not be called"))
		var out, errOut bytes.Buffer
		req := modeladmin.AddRequest{ProviderID: "ollama", ModelName: "qwen3:8b", Fields: modeladmin.Fields{Family: sp("qwen3"), InputPrice: sp("cheap")}}
		err := runModelAdd(&out, &errOut, nil, req)
		if err == nil || !strings.Contains(err.Error(), `input-price must be a number, got "cheap"`) || *calls != 0 {
			t.Errorf("err = %v, lookups = %d; want the price refused before `ollama show` runs (it can take its whole timeout)", err, *calls)
		}
		if got := mustRead(t, registry); got != writeRegistry {
			t.Error("a refused add changed the registry")
		}
	})
	t.Run("not an ollama model", func(t *testing.T) {
		modelHome(t, writeRegistry)
		stubRouteSync(t, "")
		calls := stubOllamaCaps(t, nil, errors.New("must not be called"))
		var out, errOut bytes.Buffer
		req := modeladmin.AddRequest{ProviderID: "openrouter", ModelName: "c/d", Fields: modeladmin.Fields{Family: sp("f")}}
		if err := runModelAdd(&out, &errOut, nil, req); err != nil {
			t.Fatal(err)
		}
		if *calls != 0 || errOut.Len() != 0 {
			t.Errorf("lookups = %d, stderr = %q; want none", *calls, errOut.String())
		}
	})
}

// TestModelCommandsThroughTheCommandLine drives add, edit and rm as a user
// types them: the flags reach the registry, a flag left out changes nothing,
// each failure is a non-zero exit that leaves the file alone, and each
// success is followed by exactly one sync.
func TestModelCommandsThroughTheCommandLine(t *testing.T) {
	registry := modelHome(t, writeRegistry)
	stubSeedEnv(t, config.SeedEnv{})
	syncs := stubRouteSync(t, "")
	stubProbeInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK},
		Entries:   []localmodels.Entry{{ProviderID: "ollama", ModelID: "ollama/gemma4:9b", ModelName: "gemma4:9b", Artifact: "gemma4:9b", Registered: true, ArtifactKnown: true}},
	})

	out, err := runWT(t, "model", "add", "openrouter", "qwen/qwen3.8-27b", "--family", "qwen3.8", "--tags", "code,design",
		"--input-price", "0.5", "--output-price", "2", "--id", "openrouter/qwen")
	if err != nil || out != "added model: openrouter/qwen\n" {
		t.Fatalf("add = %q, %v", out, err)
	}
	if got := mustRead(t, registry); !strings.Contains(got, "id = \"openrouter/qwen\"\nfamily = \"qwen3.8\"\nprovider_id = \"openrouter\"\nmodel_name = \"qwen/qwen3.8-27b\"\ntags = [\n    \"code\",\n    \"design\",\n]\n\n[models.cost]\ninput_price_per_million = 0.5\noutput_price_per_million = 2.0\n") {
		t.Errorf("the added row is not as asked:\n%s", got)
	}

	out, err = runWT(t, "model", "edit", "openrouter/qwen", "--tags", "", "--output-price", "2.5")
	if err != nil || out != "updated model: openrouter/qwen\n" {
		t.Fatalf("edit = %q, %v", out, err)
	}
	if got := mustRead(t, registry); !strings.Contains(got, "model_name = \"qwen/qwen3.8-27b\"\ntags = []\n\n[models.cost]\ninput_price_per_million = 0.5\noutput_price_per_million = 2.5\n") {
		t.Errorf("the edit did not change exactly the two fields:\n%s", got)
	}
	if out, err = runWT(t, "model", "edit", "openrouter/qwen", "--output-price", "2.5"); err != nil || out != "no change: openrouter/qwen\n" {
		t.Errorf("a repeated edit = %q, %v; want `no change`", out, err)
	}
	if *syncs != 2 {
		t.Errorf("syncs = %d after an add, an edit and a no-op; want 2", *syncs)
	}

	before := mustRead(t, registry)
	failures := []struct {
		args []string
		want string
	}{
		{[]string{"model", "add", "openrouter", "x/y"}, `required flag(s) "family" not set`},
		{[]string{"model", "add", "openrouter"}, "accepts 2 arg(s), received 1"},
		{[]string{"model", "add", "openrouter", "qwen/qwen3.8-27b", "--family", "f", "--id", "openrouter/qwen"}, `model already exists: "openrouter/qwen"`},
		{[]string{"model", "add", "corp", "m", "--family", "f"}, `provider_id "corp" names no provider row`},
		{[]string{"model", "add", "openrouter", "x/y", "--family", "f", "--input-price", "cheap"}, `input-price must be a number, got "cheap"`},
		{[]string{"model", "add", "openrouter", "x/y", "--family", "f", "--id", "has space"}, `an id is <provider>/<name> with no spaces, got "has space"`},
		{[]string{"model", "edit", "openrouter/qwen"}, "nothing to change"},
		{[]string{"model", "edit", "openrouter/nope", "--family", "f"}, "model not found: \"openrouter/nope\" in the registry (`wt model list` shows every id)"},
		{[]string{"model", "edit", "openrouter/qwen", "--location", "mars"}, `location must be local or cloud, got "mars"`},
		{[]string{"model", "rm", "openrouter/qwen", "openrouter/nope", "--yes"}, `model not found: "openrouter/nope"`},
		{[]string{"model", "rm"}, "requires at least 1 arg(s)"},
	}
	for _, f := range failures {
		if _, err := runWT(t, f.args...); err == nil || !strings.Contains(err.Error(), f.want) {
			t.Errorf("wt %s: err = %v, want it to contain %q", strings.Join(f.args, " "), err, f.want)
		}
	}
	if got := mustRead(t, registry); got != before {
		t.Error("a failed command changed the registry")
	}
	if *syncs != 2 {
		t.Errorf("syncs = %d, want none for a failed command", *syncs)
	}

	asked := stubConfirmRemove(t, false)
	if _, err := runWT(t, "model", "rm", "ollama/gemma4:9b"); err == nil || !strings.Contains(err.Error(), "cancelled — nothing was removed") {
		t.Errorf("declined rm: err = %v, want a cancellation", err)
	}
	if want := "Remove 1 model(s) from the registry (ollama/gemma4:9b)? Weights are not deleted."; *asked != want {
		t.Errorf("asked %q, want %q", *asked, want)
	}
	if got := mustRead(t, registry); got != before {
		t.Error("a declined removal changed the registry")
	}

	out, err = runWT(t, "model", "rm", "ollama/gemma4:9b", "openrouter/qwen", "ollama/gemma4:9b", "--yes")
	want := "removed model: ollama/gemma4:9b\n  still pulled in ollama (`ollama rm gemma4:9b` deletes it)\nremoved model: openrouter/qwen\n"
	if err != nil || out != want {
		t.Fatalf("rm = %q, %v\nwant %q", out, err, want)
	}
	if got := mustRead(t, registry); strings.Contains(got, "gemma4") || strings.Contains(got, "openrouter/qwen") || !strings.Contains(got, `id = "openrouter/a--b"`) {
		t.Errorf("after rm:\n%s", got)
	}
	if *syncs != 3 {
		t.Errorf("syncs = %d, want one more for the removal", *syncs)
	}
}

// TestModelRmWithoutATerminalNeedsYes verifies `wt model rm` in a script
// (no terminal to ask on) refuses, names --yes and removes nothing, instead
// of removing unasked or hanging. The terminal is taken away through the
// openTTY seam, so the refusal is tested on a developer's machine too, where
// the test run has one.
func TestModelRmWithoutATerminalNeedsYes(t *testing.T) {
	registry := modelHome(t, writeRegistry)
	syncs := stubRouteSync(t, "")
	oldTTY, oldConfirm := openTTY, confirmRemove
	openTTY = func() (*os.File, error) { return nil, errors.New("no tty") }
	confirmRemove = promptStop // the real prompt; TestMain fails the seam by default
	t.Cleanup(func() { openTTY, confirmRemove = oldTTY, oldConfirm })

	var out, errOut bytes.Buffer
	err := runModelRm(&out, &errOut, nil, []string{"openrouter/a--b"}, false)
	want := "Remove 1 model(s) from the registry (openrouter/a--b)? Weights are not deleted. — rerun with --yes to confirm"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
	if got := mustRead(t, registry); got != writeRegistry || *syncs != 0 || out.Len() != 0 {
		t.Errorf("a refused removal must change nothing: syncs = %d, stdout = %q", *syncs, out.String())
	}
}

// TestModelRmStillWorksWhenTheConfigDoesNotLoad verifies a removal goes
// through when wt could not load its config, with no weights line: removing
// a bad row is one of the ways to repair a registry, and it must not need a
// registry that already loads.
func TestModelRmStillWorksWhenTheConfigDoesNotLoad(t *testing.T) {
	registry := modelHome(t, writeRegistry)
	stubRouteSync(t, "")
	var out, errOut bytes.Buffer
	if err := runModelRm(&out, &errOut, nil, []string{"ollama/gemma4:9b"}, true); err != nil {
		t.Fatal(err)
	}
	if out.String() != "removed model: ollama/gemma4:9b\n" {
		t.Errorf("stdout = %q, want the removal and no weights line", out.String())
	}
	if strings.Contains(mustRead(t, registry), "gemma4") {
		t.Error("the row is still in the registry")
	}
}

// TestModelWritesUnderARedirectedRegistry is the scratch-registry safety
// test for add, edit and rm. With the registry redirected the write always
// succeeds. LiteLLM's config.yaml follows neither registry variable, so
// unnamed, the default file must not be touched and the user is told why;
// named, that file is the one synced. The exit status is 0 either way.
func TestModelWritesUnderARedirectedRegistry(t *testing.T) {
	setup := func(t *testing.T) (registry, defaultYAML string) {
		home := t.TempDir()
		registry = filepath.Join(home, "scratch", "registry.toml")
		defaultYAML = filepath.Join(home, ".config", "litellm", "config.yaml")
		for path, content := range map[string]string{registry: writeRegistry, defaultYAML: "model_list: []\n"} {
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
		t.Setenv("OPENROUTER_API_KEY", "sk-test-not-a-key")
		stubSeedEnv(t, config.SeedEnv{})
		realRouteSync(t)
		// ollama answers its probe and has nothing pulled, so the sync has
		// no provider to warn about.
		stubProbeInventory(t, localmodels.Snapshot{Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK}})
		return registry, defaultYAML
	}
	verbs := []struct {
		name string
		run  func(out, errOut *bytes.Buffer) error
		in   string // what the registry holds afterwards
		out  string // and what it no longer does
	}{
		{"add", func(out, errOut *bytes.Buffer) error {
			return runModelAdd(out, errOut, nil, modeladmin.AddRequest{ProviderID: "openrouter", ModelName: "c/d", Fields: modeladmin.Fields{Family: sp("f")}})
		}, `id = "openrouter/c--d"`, ""},
		{"edit", func(out, errOut *bytes.Buffer) error {
			return runModelEdit(out, errOut, "openrouter/a--b", modeladmin.Fields{Family: sp("renamed")})
		}, `family = "renamed"`, ""},
		{"rm", func(out, errOut *bytes.Buffer) error {
			return runModelRm(out, errOut, nil, []string{"openrouter/a--b"}, true)
		}, `id = "ollama/gemma4:9b"`, `id = "openrouter/a--b"`},
	}
	for _, v := range verbs {
		t.Run(v.name+": config.yaml not named", func(t *testing.T) {
			registry, defaultYAML := setup(t)
			var out, errOut bytes.Buffer
			if err := v.run(&out, &errOut); err != nil {
				t.Fatalf("the registry write succeeded, so the command must too: %v", err)
			}
			got := mustRead(t, registry)
			if !strings.Contains(got, v.in) || (v.out != "" && strings.Contains(got, v.out)) {
				t.Errorf("the redirected registry was not written:\n%s", got)
			}
			wantWarn := "warning: LiteLLM routes not touched: the registry is " + registry +
				" but config.yaml is the default " + defaultYAML +
				" — set WT_LITELLM_CONFIG to the config.yaml that registry belongs to\n"
			if errOut.String() != wantWarn {
				t.Errorf("stderr = %q\nwant %q", errOut.String(), wantWarn)
			}
			if got := mustRead(t, defaultYAML); got != "model_list: []\n" {
				t.Errorf("the default config.yaml was touched:\n%s", got)
			}
		})
		t.Run(v.name+": config.yaml named", func(t *testing.T) {
			registry, defaultYAML := setup(t)
			named := filepath.Join(t.TempDir(), "scratch-config.yaml")
			if err := os.WriteFile(named, []byte("model_list: []\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("WT_LITELLM_CONFIG", named)
			var out, errOut bytes.Buffer
			if err := v.run(&out, &errOut); err != nil {
				t.Fatal(err)
			}
			if errOut.Len() != 0 {
				t.Errorf("stderr = %q, want no warning when config.yaml is named", errOut.String())
			}
			if got := mustRead(t, registry); !strings.Contains(got, v.in) {
				t.Errorf("the redirected registry was not written:\n%s", got)
			}
			if got := mustRead(t, named); got == "model_list: []\n" {
				t.Error("the named config.yaml was not synced")
			}
			if got := mustRead(t, defaultYAML); got != "model_list: []\n" {
				t.Errorf("the default config.yaml was touched:\n%s", got)
			}
		})
	}
}
```

With `wt model add` shipping, the empty listing can name it:

```diff
--- a/wt/cmd/wt/model_list_test.go
+++ b/wt/cmd/wt/model_list_test.go
@@ -209,15 +209,16 @@ func TestModelListJSON(t *testing.T) {
 }
 
 // TestModelListWithNothingToList verifies an empty registry on a machine
-// with no local model prints one line that says so, not a lone header row.
+// with no local model prints one line that says so and names the command
+// that adds one, not a lone header row.
 func TestModelListWithNothingToList(t *testing.T) {
 	stubProbeInventory(t, localmodels.Snapshot{})
 	var out, errOut bytes.Buffer
 	if err := runModelList(&out, &errOut, &config.Config{}, false, 80); err != nil {
 		t.Fatal(err)
 	}
-	if !strings.HasPrefix(out.String(), "no models: ") || strings.Contains(out.String(), "MODEL") {
-		t.Errorf("out = %q, want a one-line note and no table", out.String())
+	if !strings.HasPrefix(out.String(), "no models: ") || !strings.Contains(out.String(), "`wt model add`") || strings.Contains(out.String(), "MODEL") {
+		t.Errorf("out = %q, want a one-line note naming `wt model add`, and no table", out.String())
 	}
 }
 
```

- [ ] **Step 2: Run them to see them fail**

Run, from `wt/`: `go test -count=1 ./cmd/wt -run TestModel`

Expected: the package does not build.

```text
FAIL	github.com/ohanaverse/local-ai-setup/wt/cmd/wt [build failed]
FAIL
# github.com/ohanaverse/local-ai-setup/wt/cmd/wt [github.com/ohanaverse/local-ai-setup/wt/cmd/wt.test]
cmd/wt/model_write_test.go:83:9: undefined: ollamaCaps
cmd/wt/model_write_test.go:84:2: undefined: ollamaCaps
cmd/wt/model_write_test.go:88:21: undefined: ollamaCaps
```

- [ ] **Step 3: Write the commands**

Create `wt/cmd/wt/model_write.go`:

```go
// wt model add / edit / rm — the commands that change the registry's model
// rows. Each does one locked registry write (internal/modeladmin) and then
// one LiteLLM route sync.
package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modeladmin"
	"github.com/spf13/cobra"
)

// Test seams: production asks ollama and the user's terminal; cmd/wt's
// TestMain stubs both.
var (
	// ollamaCaps is the best-effort capability lookup an add of an ollama
	// model runs before it writes.
	ollamaCaps = modeladmin.OllamaCapabilities
	// confirmRemove asks before `wt model rm` removes rows: the y/N prompt
	// `wt stop` asks with, on the controlling terminal, so piped input can
	// never authorise a removal.
	confirmRemove = promptStop
)

// modelFieldFlags registers the flags that carry a model's editable fields
// and returns a function that reads the ones the user actually passed: a
// flag left out is a nil field, which an edit leaves alone.
func modelFieldFlags(c *cobra.Command) func() modeladmin.Fields {
	f := c.Flags()
	// Local flags: --family and --tags here set one model's value, and shadow
	// the root's persistent -F/--family and -T/--tags filters.
	f.String(modeladmin.FieldFamily, "", "model family, e.g. qwen3.8 (what -F filters on)")
	f.String(modeladmin.FieldTags, "", "comma-separated tags, e.g. code,design (what -T filters on)")
	f.String(modeladmin.FieldLocation, "", "local or cloud; empty inherits the provider's")
	f.String(modeladmin.FieldInputPrice, "", "input price, $ per million tokens")
	f.String(modeladmin.FieldCachePrice, "", "cached-input price, $ per million tokens")
	f.String(modeladmin.FieldOutputPrice, "", "output price, $ per million tokens")
	f.String(modeladmin.FieldSubscriptionPrice, "", "subscription price, $ per period")
	f.String(modeladmin.FieldSubscriptionPeriod, "", "month or year")
	return func() modeladmin.Fields {
		get := func(name string) *string {
			if !f.Changed(name) {
				return nil
			}
			v, _ := f.GetString(name)
			return &v
		}
		return modeladmin.Fields{
			Family: get(modeladmin.FieldFamily), Tags: get(modeladmin.FieldTags), Location: get(modeladmin.FieldLocation),
			InputPrice: get(modeladmin.FieldInputPrice), CachePrice: get(modeladmin.FieldCachePrice), OutputPrice: get(modeladmin.FieldOutputPrice),
			SubscriptionPrice: get(modeladmin.FieldSubscriptionPrice), SubscriptionPeriod: get(modeladmin.FieldSubscriptionPeriod),
		}
	}
}

func modelAddCmd(a *app) *cobra.Command {
	var id string
	c := &cobra.Command{
		Use:   "add <provider> <name> --family <family>",
		Short: "Add a model to the registry",
		Long: "Add one model to the registry.\n\n" +
			"<name> is the model as its provider lists it: an ollama tag (qwen3:8b), the\n" +
			"name of an omlx model directory, an mtplx org/name, or a cloud provider's\n" +
			"model id. wt downloads nothing: pull or download a local model with its\n" +
			"provider's own tool, before or after adding it (`wt model list` shows what\n" +
			"is on disk and not yet registered as STATUS new).\n\n" +
			"The id is <provider-family>/<name> for a local provider — the id wt already\n" +
			"lists a discovered model under — and <provider>/<name> with each \"/\" in the\n" +
			"name written \"--\" for any other provider. --id overrides it, with an id\n" +
			"of that shape: <provider>/<name>, no spaces. The id, the provider and the\n" +
			"name cannot be changed afterwards.\n\n" +
			"A missing default provider row (ollama, omlx, mtplx, openrouter) is added\n" +
			"in the same write; a provider wt has no default row for must have its\n" +
			"[[providers]] block in registry.toml first. For an ollama model wt runs\n" +
			"`ollama show` once and records whether the model supports tools and\n" +
			"vision; if that fails the model is added without them and a warning says\n" +
			"so.\n\n" +
			"The LiteLLM routes are synced once afterwards; a sync that cannot run is a\n" +
			"warning, and the exit status is still 0.",
		Example: "  wt model add ollama qwen3:8b --family qwen3 --tags code\n" +
			"  wt model add openrouter qwen/qwen3.8-27b --family qwen3.8 --input-price 0.5 --output-price 2",
		Args:         cobra.ExactArgs(2),
		SilenceUsage: true,
	}
	fields := modelFieldFlags(c)
	c.Flags().StringVar(&id, "id", "", "the model's id, instead of the derived one")
	_ = c.MarkFlagRequired(modeladmin.FieldFamily)
	c.RunE = func(cmd *cobra.Command, args []string) error {
		req := modeladmin.AddRequest{ProviderID: args[0], ModelName: args[1], ID: id, Fields: fields()}
		return runModelAdd(cmd.OutOrStdout(), cmd.ErrOrStderr(), a.cfg, req)
	}
	return c
}

// runModelAdd implements `wt model add`. cfg is the config as loaded, or nil
// when there is no registry yet; it is read only for the ollama address.
func runModelAdd(out, errOut io.Writer, cfg *config.Config, req modeladmin.AddRequest) error {
	// What can be refused without the registry is refused first: a mistyped
	// price must not wait on `ollama show`, up to its timeout when the
	// daemon is down.
	if err := modeladmin.CheckAdd(req); err != nil {
		return err
	}
	var warnings []string
	if modeladmin.UsesOllama(req.ProviderID) {
		// Before the write: the registry's apply function must not run a
		// command.
		info, err := ollamaCaps(cfg, req.ModelName)
		if err != nil {
			warnings = append(warnings, "added without model_info.supports_function_calling / supports_vision: "+err.Error())
		}
		req.ModelInfo = info
	}
	res, err := modeladmin.Add(req, seedEnv())
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "added model: %s\n", res.ID)
	for _, p := range res.ProvidersAdded {
		fmt.Fprintf(out, "added provider: %s\n", p)
	}
	for _, p := range res.ProvidersUnseeded {
		fmt.Fprintf(out, "no default row for provider: %s (an agent lists it; add it to registry.toml by hand)\n", p)
	}
	warnings = append(warnings, res.Warnings...)
	return syncAndWarn(out, errOut, warnings)
}

// syncAndWarn runs the one route sync that follows a registry write, prints
// its lines, and prints every warning on stderr. It never fails: the write
// succeeded, so the command exits 0.
func syncAndWarn(out, errOut io.Writer, warnings []string) error {
	var syncOut bytes.Buffer
	if w := syncRoutesAfterWrite(&syncOut, errOut); w != "" {
		warnings = append(warnings, w)
	}
	_, _ = io.Copy(out, &syncOut)
	for _, w := range warnings {
		fmt.Fprintf(errOut, "warning: %s\n", w)
	}
	return nil
}

func modelEditCmd(_ *app) *cobra.Command {
	c := &cobra.Command{
		Use:   "edit <id> [flags]",
		Short: "Change a registry model's family, tags, location or prices",
		Long: "Change the named fields of one registry model. A flag that is not passed\n" +
			"leaves its field alone; an empty value clears it (--tags \"\", --input-price\n" +
			"\"\"), and an empty --location makes the model inherit its provider's.\n" +
			"Every other key of the row is kept as it is.\n\n" +
			"The id, the provider and the model name cannot be edited: usage history,\n" +
			"rotation and launch profiles key on the id. Remove the model and add it\n" +
			"again to change one.\n\n" +
			"The LiteLLM routes are synced once when something changed.",
		Example:      "  wt model edit ollama/qwen3:8b --tags code,design\n  wt model edit openrouter/qwen--qwen3.8-27b --output-price 2.5",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
	}
	fields := modelFieldFlags(c)
	c.RunE = func(cmd *cobra.Command, args []string) error {
		f := fields()
		if f.Empty() {
			// A usage mistake, unlike every other failure here: show the flags.
			cmd.SilenceUsage = false
			return errors.New("nothing to change: pass at least one of --family, --tags, --location or a price flag")
		}
		return runModelEdit(cmd.OutOrStdout(), cmd.ErrOrStderr(), args[0], f)
	}
	return c
}

func runModelEdit(out, errOut io.Writer, id string, f modeladmin.Fields) error {
	changed, err := modeladmin.Edit(id, f)
	if err != nil {
		return unknownModelHint(err)
	}
	if !changed {
		fmt.Fprintf(out, "no change: %s\n", id)
		return nil
	}
	fmt.Fprintf(out, "updated model: %s\n", id)
	return syncAndWarn(out, errOut, nil)
}

// unknownModelHint says where the ids are when one was not found.
func unknownModelHint(err error) error {
	if errors.Is(err, config.ErrModelNotFound) {
		return fmt.Errorf("%w in the registry (`wt model list` shows every id)", err)
	}
	return err
}

func modelRmCmd(a *app) *cobra.Command {
	var yes bool
	c := &cobra.Command{
		Use:   "rm <id>...",
		Short: "Remove models from the registry (never their weights)",
		Long: "Remove one or more models from the registry. Nothing else is touched: wt\n" +
			"never deletes weights, and it prints where each removed model's are so you\n" +
			"can delete them yourself. A local model that is still on disk shows up\n" +
			"again in `wt model list` as STATUS new.\n\n" +
			"It asks for confirmation on the terminal; --yes skips it. When one id is\n" +
			"not in the registry, nothing is removed.\n\n" +
			"The LiteLLM routes are synced once afterwards.",
		Example:      "  wt model rm ollama/qwen3:8b\n  wt model rm openrouter/a--b openrouter/c--d --yes",
		Args:         cobra.MinimumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := a.cfg
			if a.loadErr != nil {
				cfg = nil // the removal may be the repair; only the weights lines are lost
			}
			return runModelRm(cmd.OutOrStdout(), cmd.ErrOrStderr(), cfg, args, yes)
		},
	}
	c.Flags().BoolVar(&yes, "yes", false, "do not ask for confirmation")
	return c
}

// runModelRm implements `wt model rm`. cfg is nil when the config did not
// load; the rows are then removed without the lines that say where their
// weights are.
func runModelRm(out, errOut io.Writer, cfg *config.Config, ids []string, yes bool) error {
	ids = uniqueInOrder(ids)
	// Where the weights are is learned before the write: afterwards the rows
	// are gone, and a local model's path comes from the probe.
	notes := map[string]string{}
	if cfg != nil {
		local := slices.ContainsFunc(cfg.Models, func(m config.Model) bool {
			loc, err := cfg.ResolveLocation(m)
			return slices.Contains(ids, m.ID) && err == nil && loc == config.LocationLocal
		})
		if local {
			for _, r := range modeladmin.Rows(cfg, probeInventory(cfg)) {
				if r.Registered && slices.Contains(ids, r.ID) {
					notes[r.ID] = modeladmin.WeightsNote(r)
				}
			}
		}
	}
	if !yes {
		ok, err := confirmRemove(fmt.Sprintf("Remove %d model(s) from the registry (%s)? Weights are not deleted.", len(ids), strings.Join(ids, ", ")))
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("cancelled — nothing was removed")
		}
	}
	if err := modeladmin.Remove(ids); err != nil {
		return unknownModelHint(err)
	}
	for _, id := range ids {
		fmt.Fprintf(out, "removed model: %s\n", id)
		if note := notes[id]; note != "" {
			fmt.Fprintf(out, "  %s\n", note)
		}
	}
	return syncAndWarn(out, errOut, nil)
}

// uniqueInOrder drops repeated ids, keeping the order they were given in: an
// id named twice would otherwise fail the second removal and undo the first.
func uniqueInOrder(ids []string) []string {
	var out []string
	for _, id := range ids {
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}
```

`wt model rm` asks with `promptStop`. That function opens `/dev/tty` itself, so its refusal without a terminal could only be tested where there is none; it now goes through `openTTY`, the seam `promptProfile` already uses (`launch.go:77`):

```diff
--- a/wt/cmd/wt/model_cmds.go
+++ b/wt/cmd/wt/model_cmds.go
@@ -40,10 +40,12 @@ var (
 	stopProvider = lifecycle.Stop
 )
 
-// promptStop is promptReplace's twin for stopping an in-use model: y/N on the
-// controlling terminal, default No, so piped input can never authorise it.
+// promptStop is promptReplace's twin for stopping an in-use model, and what
+// `wt model rm` asks with too: y/N on the controlling terminal, default No,
+// so piped input can never authorise it. The terminal is opened through the
+// openTTY seam, so a test can take it away.
 func promptStop(question string) (bool, error) {
-	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
+	f, err := openTTY()
 	if err != nil {
 		return false, fmt.Errorf("%s — rerun with --yes to confirm", question)
 	}
```

- [ ] **Step 4: Register them, and name `wt model add` in the empty listing**

The first hunk is `wt model init`'s help, which lists when each provider row is added: openrouter's line now says what Task 6 made true.

```diff
--- a/wt/cmd/wt/model.go
+++ b/wt/cmd/wt/model.go
@@ -49,8 +49,9 @@ func realSyncRoutesAfterWrite(out, errOut io.Writer) string {
 	return "LiteLLM routes not synced: " + err.Error()
 }
 
-// modelCmd is the `wt model` group: the commands that write registry.toml.
-// This step ships `init`; add, edit, rm, list and the Models tab follow.
+// modelCmd is the `wt model` group: list (model_list.go), add, edit and rm
+// (model_write.go), and init, which creates the registry and seeds its
+// provider rows.
 func modelCmd(a *app) *cobra.Command {
 	c := &cobra.Command{
 		Use:   "model",
@@ -67,10 +68,11 @@ func modelCmd(a *app) *cobra.Command {
 			"  - ollama, omlx, mtplx: when the command is installed, or a model or a\n" +
 			"                         configured agent uses it\n" +
 			"  - mlx_lm_server:       when a model or a configured agent uses it\n" +
-			"  - openrouter:          when a configured agent uses it; the row holds no key,\n" +
-			"                         only auth.secret_ref = \"OPENROUTER_API_KEY\", the\n" +
-			"                         environment variable wt reads the key from (edit\n" +
-			"                         it in registry.toml if you keep the key elsewhere)\n" +
+			"  - openrouter:          when a model or a configured agent uses it; the row\n" +
+			"                         holds no key, only auth.secret_ref =\n" +
+			"                         \"OPENROUTER_API_KEY\", the environment variable wt\n" +
+			"                         reads the key from (edit it in registry.toml if\n" +
+			"                         you keep the key elsewhere)\n" +
 			"  - each configured agent: a native provider row under the agent's name\n\n" +
 			"A provider an agent lists that wt has no default row for is named in the\n" +
 			"output and left for you to add to registry.toml.\n\n" +
@@ -90,7 +92,7 @@ func modelCmd(a *app) *cobra.Command {
 		},
 	}
 	initC.Flags().BoolVar(&initJSON, "json", false, "machine-readable output")
-	c.AddCommand(initC, modelListCmd(a))
+	c.AddCommand(initC, modelListCmd(a), modelAddCmd(a), modelEditCmd(a), modelRmCmd(a))
 	return c
 }
 
```

```diff
--- a/wt/cmd/wt/model_list.go
+++ b/wt/cmd/wt/model_list.go
@@ -104,7 +104,7 @@ func runModelList(out, errOut io.Writer, cfg *config.Config, asJSON bool, width
 		return enc.Encode(doc)
 	}
 	if len(rows) == 0 {
-		fmt.Fprintln(out, "no models: the registry has none, and no local model was found on this machine")
+		fmt.Fprintln(out, "no models: the registry has none, and no local model was found on this machine (add one with `wt model add`)")
 		return nil
 	}
 	cols, cells, dropped := fitModelList(rows, width)
```

- [ ] **Step 5: Run the tests**

Run, from `wt/`: `go test -count=1 ./cmd/wt -run TestModel`

Expected: `ok  	github.com/ohanaverse/local-ai-setup/wt/cmd/wt`. `TestModelRmWithoutATerminalNeedsYes` runs on every machine: it takes the terminal away through `openTTY`.

- [ ] **Step 6: Commit**

```bash
git add wt/cmd/wt
git commit -m "feat(wt): wt model add, edit and rm"
```

### Task 9: `wt litellm sync` names a provider that has no row

A registry model whose `provider_id` names no `[[providers]]` row is a registry gap (`litellm.RegistryGap`): sync neither routes it nor removes a route it has. For a probe family there is already a line about it, when `config.yaml` holds one of the family's routes. For any other provider — a cloud gateway whose row was never written — sync says nothing at all.

**Files:**
- Modify: `wt/cmd/wt/litellm_test.go` (`TestLitellmSyncKeepsRoutesOfRegistryGapFamily`'s table; one new test)
- Modify: `wt/cmd/wt/litellm.go:130-160` (`runLitellmSync`); new `missingProviderWarnings`
- Modify: `wt/docs/wt-model.md`, `wt/docs/internals/config-and-registry.md`, `wt/docs/internals/testing.md`, `wt/CLAUDE.md`, `wt/CHANGELOG.md`

**Interfaces:**
- Consumes (existing): `syncUntouchedAndWarnings(cfg, snap, routed) (untouched, warnings []string)`, `config.RegistryPath()`, `localmodels.Family`, the test helpers `litellmTestConfig`, `litellmEnv`, `stubProbeInventory`
- Produces: `func missingProviderWarnings(cfg *config.Config, said []string) []string` — one line per provider id with no row, in registry order: ``provider "<id>" has no [[providers]] row in <registry path>, so wt cannot route <ids>; add the row (`wt model init` adds the default ones) or fix the provider_id``. A provider whose family already has the "no provider entry" probe line in `said` is left out.

- [ ] **Step 1: Write the failing tests**

```diff
--- a/wt/cmd/wt/litellm_test.go
+++ b/wt/cmd/wt/litellm_test.go
@@ -1450,14 +1450,18 @@ litellm_settings:
 		name string
 		cfg  *config.Config
 		want string
+		// also is a further warning the case earns: with no provider rows at
+		// all, ollama's model has no row either, and no probe line says so.
+		also []string
 	}{
-		{"provider row without a location", noLocation, `provider "omlx" could not be probed (its registry entry has no location)` + tail},
-		{"providers empty", noProviders, `provider "omlx" could not be probed (the registry has models for it but no provider entry)` + tail},
-		{"provider row with a mistyped location", badLocation, `provider "omlx" could not be probed (its registry entry has location "Local"; expected "local" or "cloud")` + tail},
-		{"model with a mistyped location of its own", badModelLocation, `provider "omlx" could not be probed (model "omlx/reg" has location "Local"; expected "local" or "cloud")` + tail},
+		{"provider row without a location", noLocation, `provider "omlx" could not be probed (its registry entry has no location)` + tail, nil},
+		{"providers empty", noProviders, `provider "omlx" could not be probed (the registry has models for it but no provider entry)` + tail,
+			[]string{`provider "ollama" has no [[providers]] row in ` + config.RegistryPath() + ", so wt cannot route ollama/gemma:9b; add the row (`wt model init` adds the default ones) or fix the provider_id"}},
+		{"provider row with a mistyped location", badLocation, `provider "omlx" could not be probed (its registry entry has location "Local"; expected "local" or "cloud")` + tail, nil},
+		{"model with a mistyped location of its own", badModelLocation, `provider "omlx" could not be probed (model "omlx/reg" has location "Local"; expected "local" or "cloud")` + tail, nil},
 	} {
 		t.Run(tc.name, func(t *testing.T) {
-			want := tc.want
+			want := append([]string{tc.want}, tc.also...)
 			p := litellmEnv(t, body)
 			restarts := filepath.Join(t.TempDir(), "restarts")
 			t.Setenv("WT_LITELLM_RESTART_CMD", "echo restart >> "+restarts)
@@ -1470,7 +1474,7 @@ litellm_settings:
 			if err := json.Unmarshal(out.Bytes(), &dry); err != nil {
 				t.Fatalf("dry run: not JSON: %q", out.String())
 			}
-			if len(dry.Plan.Add)+len(dry.Plan.Remove) != 0 || !slices.Equal(dry.Warnings, []string{want}) {
+			if len(dry.Plan.Add)+len(dry.Plan.Remove) != 0 || !slices.Equal(dry.Warnings, want) {
 				t.Errorf("dry run: add %v remove %v warnings %q; want no change and only %q", dry.Plan.Add, dry.Plan.Remove, dry.Warnings, want)
 			}
 			out.Reset()
@@ -1908,3 +1912,37 @@ func TestLitellmSyncWarnsAboutAnOllamaServeRow(t *testing.T) {
 		t.Errorf("sync changed the row it only warns about:\n%s", after)
 	}
 }
+
+// TestLitellmSyncWarnsAboutAProviderWithNoRow verifies sync names a provider
+// id that models reference and no [[providers]] row defines, once, with the
+// models, in the real run and the dry run alike. The model is neither routed
+// nor (if it had a route) unrouted, and without this line the user sees a
+// registry model with no route and no explanation.
+func TestLitellmSyncWarnsAboutAProviderWithNoRow(t *testing.T) {
+	cfg := litellmTestConfig()
+	cfg.Models = append(cfg.Models,
+		config.Model{ID: "corp/a", ProviderID: "corp", ModelName: "a", Location: config.LocationCloud},
+		config.Model{ID: "corp/b", ProviderID: "corp", ModelName: "b"},
+	)
+	want := `provider "corp" has no [[providers]] row in ` + config.RegistryPath() + ", so wt cannot route corp/a, corp/b; add the row (`wt model init` adds the default ones) or fix the provider_id"
+	for _, dryRun := range []bool{true, false} {
+		litellmEnv(t, "model_list: []\n")
+		stubProbeInventory(t, localmodels.Snapshot{Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK}})
+		var out, errOut bytes.Buffer
+		if err := runLitellmSync(&out, &errOut, cfg, false, dryRun); err != nil {
+			t.Fatalf("dry run %v: %v", dryRun, err)
+		}
+		if got := errOut.String(); got != "warning: "+want+"\n" {
+			t.Errorf("dry run %v: stderr = %q\nwant the one warning %q", dryRun, got, want)
+		}
+		if strings.Contains(out.String(), "corp/") {
+			t.Errorf("dry run %v: a model with no provider row was routed:\n%s", dryRun, out.String())
+		}
+	}
+	// A registry with no gap says nothing.
+	litellmEnv(t, "model_list: []\n")
+	var out, errOut bytes.Buffer
+	if err := runLitellmSync(&out, &errOut, litellmTestConfig(), false, false); err != nil || errOut.Len() != 0 {
+		t.Errorf("no gap: err = %v, stderr = %q; want silence", err, errOut.String())
+	}
+}
```

- [ ] **Step 2: Run them to see them fail**

Run, from `wt/`: `go test -count=1 ./cmd/wt -run TestLitellmSync`

Expected: the "providers empty" case and the new test fail: no warning is printed.

```text
--- FAIL: TestLitellmSyncKeepsRoutesOfRegistryGapFamily (0.00s)
    --- FAIL: TestLitellmSyncKeepsRoutesOfRegistryGapFamily/providers_empty (0.00s)
        litellm_test.go:1478: dry run: add [] remove [] warnings ["provider \"omlx\" could not be probed (the registry has models for it but no provider entry); its model routes were left unchanged"]; want no change a...
--- FAIL: TestLitellmSyncWarnsAboutAProviderWithNoRow (0.01s)
    litellm_test.go:1936: dry run true: stderr = ""
        want the one warning "provider \"corp\" has no [[providers]] row in <tmp>/wt-test-config-N/local-ai/registry.toml, so wt cannot route corp/a, corp/b; add the row (`wt model init` adds the default ones) or fix ...
    litellm_test.go:1936: dry run false: stderr = ""
```

- [ ] **Step 3: Add the warning**

```diff
--- a/wt/cmd/wt/litellm.go
+++ b/wt/cmd/wt/litellm.go
@@ -10,6 +10,7 @@ import (
 	"io"
 	"slices"
 	"sort"
+	"strings"
 
 	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
 	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
@@ -138,6 +139,7 @@ func runLitellmSync(out, errOut io.Writer, cfg *config.Config, asJSON, dryRun bo
 	// provider stopped outside wt leaves behind).
 	desired := desiredLocalModels(cfg, snap)
 	untouched, probeWarns := syncUntouchedAndWarnings(cfg, snap, routedIDSet())
+	probeWarns = append(probeWarns, missingProviderWarnings(cfg, probeWarns)...)
 	o := litellm.Options{
 		Untouched:         untouched,
 		UntouchedFamilies: untrustedFamilies(cfg, snap),
@@ -163,6 +165,38 @@ func runLitellmSync(out, errOut io.Writer, cfg *config.Config, asJSON, dryRun bo
 	return reportLitellm(out, errOut, res, asJSON)
 }
 
+// missingProviderWarnings names every provider id a registry model references
+// that has no [[providers]] row, with the models that reference it. Such a
+// model is a registry gap (litellm.RegistryGap): sync neither routes it nor
+// removes a route it already has, and before this warning it could do so
+// without a word — the user saw a model in the registry, no route for it,
+// and no reason. One line per provider, in registry order. A provider whose
+// family already has the "no provider entry" probe warning in said is left
+// out: that line names the same gap.
+func missingProviderWarnings(cfg *config.Config, said []string) []string {
+	models := map[string][]string{}
+	var order []string
+	for _, m := range cfg.Models {
+		if m.Native || cfg.ProviderByID(m.ProviderID) != nil {
+			continue
+		}
+		if _, seen := models[m.ProviderID]; !seen {
+			order = append(order, m.ProviderID)
+		}
+		models[m.ProviderID] = append(models[m.ProviderID], m.ID)
+	}
+	var warnings []string
+	for _, id := range order {
+		covered := fmt.Sprintf("provider %q could not be probed (the registry has models for it but no provider entry)", localmodels.Family(id))
+		if slices.ContainsFunc(said, func(w string) bool { return strings.HasPrefix(w, covered) }) {
+			continue
+		}
+		warnings = append(warnings, fmt.Sprintf("provider %q has no [[providers]] row in %s, so wt cannot route %s; add the row (`wt model init` adds the default ones) or fix the provider_id",
+			id, config.RegistryPath(), strings.Join(models[id], ", ")))
+	}
+	return warnings
+}
+
 // syncUntouchedAndWarnings derives both sync modes' view of the provider
 // probes: the ids whose routes must not move (families the probe could not
 // vouch for), and the warnings the run reports. One helper for the real sync
```

- [ ] **Step 4: Run the tests**

Run, from `wt/`: `go test -count=1 ./cmd/wt -run TestLitellm`

Expected: `ok  	github.com/ohanaverse/local-ai-setup/wt/cmd/wt`

- [ ] **Step 5: Document the PR**

In `wt/docs/wt-model.md`, find:

````text
wt model init [--json]     # create the registry if it is missing; add the default provider rows
```
````

and replace it with:

````text
wt model init [--json]     # create the registry if it is missing; add the default provider rows
wt model add <provider> <name> --family F [--tags a,b] [--location local|cloud] [price flags] [--id ID]
wt model edit <id> [--family F] [--tags a,b] [--location local|cloud] [price flags]
wt model rm <id>... [--yes]
```

Each of the three writing commands makes one locked write of the registry and
then syncs the LiteLLM routes once. The exit code is 0 when the write
succeeded, even if the sync could only warn; it is 1 for a value that cannot
be used, an unknown or duplicate id, a registry that cannot be read, or a
removal that was declined.
````

In `wt/docs/wt-model.md`, add at the end of the file (after one blank line):

```text
## `wt model add <provider> <name> --family <family>`

`<name>` is the model as its provider lists it: an ollama tag (`qwen3:8b`), the
name of an omlx model directory, an mtplx `org/name`, or a cloud provider's
model id. Pull or download a local model with its provider's own tool, before
or after adding it; `wt model list` shows what is on disk and unregistered as
`STATUS new`.

| Flag | Meaning |
|---|---|
| `--family` | required; what `-F` filters on |
| `--tags a,b` | what `-T` filters on |
| `--location local\|cloud` | only when the model differs from its provider's location |
| `--input-price`, `--cache-price`, `--output-price` | $ per million tokens |
| `--subscription-price`, `--subscription-period month\|year` | a subscription price needs a period |
| `--id` | the id, instead of the derived one: `<provider>/<name>`, with no spaces |

The id is derived: for a local provider it is the id wt already lists the
model under when it finds it unregistered (`ollama/qwen3:8b`,
`omlx/<directory>`, `mtplx/<org>/<name>`; an `omlx-6bit` model is `omlx/…`), so
registering a model does not rename it. For any other provider it is
`<provider>/<name>` with each `/` in the name written `--`
(`openrouter/qwen--qwen3.8-27b`). The id, the provider and the name cannot be
changed afterwards: usage history, rotation and launch profiles key on the id.

In the same write wt adds the default provider row the model needs when it is
missing — for ollama, omlx, mtplx and openrouter (openrouter's holds no key,
only `auth.secret_ref = "OPENROUTER_API_KEY"`, the environment variable wt
reads the key from) — and any other row `wt model init` would add. A provider
wt has no default row for is refused: add its `[[providers]]` block to
`registry.toml` first. A second entry for an artifact that is already
registered is refused too. A value that cannot be used is refused before
anything else happens.

For an ollama model wt runs `ollama show <name>` once, against the address of
the registry's ollama provider, and records `model_info.supports_function_calling`
and `model_info.supports_vision` when ollama lists the capability; wt copies
`model_info` into the model's LiteLLM route. If the lookup fails the model is
added without them and a warning says so.

## `wt model edit <id> [flags]`

Changes the fields named by the flags, and nothing else in the row: every
other key (off-peak prices, `model_info`, `fetch`, keys wt does not know) is
kept as it is. A flag left out leaves its field alone; an empty value clears
it (`--tags ""`, `--input-price ""`), and an empty `--location` makes the
model inherit its provider's. No flag at all is a usage error. It does not
change `pricing_updated_at`. When nothing changes it says `no change` and
syncs nothing.

Two things follow from an edit of a price or of a subscription field, and
only from one. Clearing a model's last price removes its `[models.cost]`
table with it. And a row still in modelman's old cost layout (`kind =
"per_token"` with `price_per_million_tokens`, or `kind = "subscription"` with
`price_per_period` and `period`) is moved to the current keys in the same
write: modelman reads such a table by its old keys alone, so a new key
beside them would be a price it ignores.

## `wt model rm <id>... [--yes]`

Removes the rows from the registry, and nothing else: it prints where each
removed model's weights are, because wt deletes none. A local model that is
still on disk shows up again in `wt model list` as `STATUS new`. It asks on
the terminal first (`--yes` skips; with no terminal and no `--yes` it refuses).
When one id is not in the registry, none is removed.

## What stays a hand edit of `registry.toml`

- A `[[providers]]` row wt has no default for (a cloud provider other than
  openrouter, a gateway of your own).
- A model served from a directory of your own, `[models.fetch] local_path =
  "…"` (the `bin/mlx-quantize` workflow). wt keeps the key, shows the path, and
  takes the model's presence from a stat of it.
- `[[families]]` display names. wt reads only each model's `family`.

Run `wt litellm sync` after a hand edit: no tool saw it.
```

In `wt/docs/internals/config-and-registry.md`, find:

```text
not yet the two `Config.Validate` rules that need other rows, a resolvable `location` and a `provider_id` that names a provider
```

and replace it with:

```text
and, for a model row, the two `Config.Validate` rules that need other rows: its `provider_id` names a provider row, and its `location` — its own, or the one it inherits from that row — resolves to `local` or `cloud` (`validateModelRefs`, which asks `ResolveLocation` over the document's provider rows as they are after `apply`, so a row seeded in the same write counts)
```

In `wt/docs/internals/config-and-registry.md`, find:

```text
`wt model init` today, `wt model add` next
```

and replace it with:

```text
`wt model init`, and `wt model add` and the Models tab's add (`modeladmin.Add`), which add the model row first so that seeding sees the provider it references
```

In `wt/docs/internals/config-and-registry.md`, find:

```text
a default row for openrouter when an agent lists it, or is itself named `openrouter`
```

and replace it with:

```text
a default row for openrouter when a model references it, when an agent lists it, or when an agent is itself named `openrouter`
```

In `wt/CHANGELOG.md`, find:

```text
model or listed by an agent; openrouter when an agent lists it, with
```

and replace it with:

```text
model or listed by an agent; openrouter when used by a model or listed by an agent, with
```

In `wt/CLAUDE.md`, find:

```text
(`registry.toml` has two writers until modelman is retired: modelman, and wt's `wt model init`;
```

and replace it with:

```text
(`registry.toml` has two writers until modelman is retired: modelman, and wt's `wt model` commands;
```

In `wt/CLAUDE.md`, find:

```text
wt's one writer today is `wt model init` (`config.SeedRegistryDefaults` inside `config.UpdateRegistry`), and modelman still writes it too.
```

and replace it with:

```text
wt writes it through `config.UpdateRegistry` alone: `wt model init` (provider rows, `config.SeedRegistryDefaults`) and `wt model add|edit|rm` (model rows, `internal/modeladmin`); modelman still writes it too.
```

In `wt/CLAUDE.md`, find:

```text
| `cmd/wt/model_list.go` | `wt model list [--json]`
```

and replace it with:

```text
| `cmd/wt/model_write.go` | `wt model add`, `edit`, `rm` — each one `modeladmin` write, then one route sync (`syncAndWarn`); the `ollamaCaps` seam, and `confirmRemove`, which is `wt stop`'s `promptStop` (it opens the terminal through `openTTY`) |
| `cmd/wt/model_list.go` | `wt model list [--json]`
```

In `wt/CLAUDE.md`, find:

```text
path and size — nothing hidden, unlike `catalog.Build`), `WeightsNote`, `FormatSize` |
```

and replace it with:

```text
path and size — nothing hidden, unlike `catalog.Build`), `WeightsNote`, `FormatSize`; `Add`, `Edit`, `Remove` (each one `config.UpdateRegistry`, no route sync), `CheckAdd` (what an add refuses without the registry, for a caller with a slow lookup to run first), `Fields` and `FieldError` (the editable fields as typed, and which one is wrong), `DeriveID`, `OllamaCapabilities` |
```

In `wt/CLAUDE.md`, find:

```text
wt model list [--json]               # every registry model and every local model found, with live status
```

and replace it with:

```text
wt model list [--json]               # every registry model and every local model found, with live status
wt model add <provider> <name> --family F   # register a model (seeds a missing default provider row; one route sync)
wt model edit <id> --tags code       # change family, tags, location or prices; nothing else in the row moves
wt model rm <id> [--yes]             # registry only; prints where the weights are
```

In `wt/docs/internals/testing.md`, find:

```text
`statsNow`, `seedEnv`, `syncRoutesAfterWrite`)
```

and replace it with:

```text
`statsNow`, `seedEnv`, `syncRoutesAfterWrite`, `ollamaCaps`, `confirmRemove`)
```

In `wt/docs/internals/testing.md`, find:

```text
**Registry writes.**
```

and replace it with:

```text
**Registry writes.** `internal/modeladmin` writes the registry in its tests, so it has an isolating `TestMain`, which also fails the package's `runOllamaShow` seam: no test runs the developer's `ollama`. `cmd/wt`'s `TestMain` fails `ollamaCaps` and `confirmRemove` for the same reason; tests use `stubOllamaCaps` and `stubConfirmRemove` (`cmd/wt/model_write_test.go`). A test of what a y/N prompt does with no terminal stubs `openTTY` to fail: `promptStop`, and through it `wt model rm`, opens `/dev/tty` only through that seam, so the refusal is tested where the test run has a terminal too.
```

In `wt/CHANGELOG.md`, find:

```text
## Unreleased

### Added
```

and replace it with:

```text
## Unreleased

### Added

- `wt model add <provider> <name> --family F`, `wt model edit <id>` and
  `wt model rm <id>...` register, change and remove models in the registry.
  Each makes one locked write and then syncs the LiteLLM routes once. `add`
  derives the id (the discovered id for a local model, `/` as `--` for a
  cloud one; `--id` overrides), seeds a missing default provider row —
  openrouter's too, which `wt model init` now also adds for a model that
  references it — and for an ollama model records what `ollama show` says it
  supports. `edit` changes only the named fields; an edit of a price also
  moves a row out of modelman's old cost layout. `rm` removes registry rows
  only and prints where the weights are. Reference: `docs/wt-model.md`.
- `wt litellm sync` warns when a registry model names a provider that has no
  `[[providers]]` row; it used to leave such a model unrouted without a word.
```

- [ ] **Step 6: A scratch round trip — `wt model add`, `edit`, `list` and `rm`, in a throwaway home**

This is not the live check the spec asks for before merge: that one touches a real provider and is the owner's (Step 8 hands it over). This one shows the built commands end to end without touching anything real. The registry's one provider row points at a port nothing listens on, so no server is probed. wt is run only through `run.sh`, which empties the environment and sets the throwaway home, a scratch LiteLLM config and a restart command that does nothing; `PATH` is the system's alone, so no installed provider is found.

Each block below is one command and stands on its own: none relies on a variable or a directory change left by another.

Create the scratch directory, build into it, and write the home. From the monorepo root:

```bash
D=/tmp/wt-step3-roundtrip
rm -rf "$D" && mkdir -p "$D/home/.config/local-ai" "$D/home/.omlx/models/Some-Model-4bit"
(cd wt && go build -o "$D/wt" ./cmd/wt)
echo '{}' > "$D/home/.omlx/models/Some-Model-4bit/config.json"
printf 'model_list: []\n' > "$D/home/litellm.yaml"
cat > "$D/home/.config/local-ai/registry.toml" <<'TOML'
[[providers]]
id = "omlx"
name = "oMLX"
location = "local"
model_dir = "~/.omlx/models"

[providers.auth]
type = "none"
base_url = "http://127.0.0.1:9"
TOML
```

Create `/tmp/wt-step3-roundtrip/run.sh`:

```bash
#!/bin/bash
# Run a command in the throwaway home that is beside this script, and in no
# other: the environment is emptied first, so nothing of the caller's — HOME,
# XDG_CONFIG_HOME, a registry or LiteLLM override, an API key — reaches wt,
# and wt reaches nothing of the caller's. PATH is the system's alone, so no
# installed provider (ollama, omlx, mtplx) is found. Usage: run.sh <command>
# [args...]
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd -P)"
cd "$here/home"
exec env -i HOME="$here/home" XDG_CONFIG_HOME="$here/home/.config" \
  WT_LITELLM_CONFIG="$here/home/litellm.yaml" WT_LITELLM_RESTART_CMD=true \
  OPENROUTER_API_KEY=sk-not-a-key PATH=/usr/bin:/bin TERM=xterm-256color "$@"
```

Then the round trip:

```bash
D=/tmp/wt-step3-roundtrip
chmod +x "$D/run.sh"
wt() { "$D/run.sh" "$D/wt" "$@"; }
wt model add openrouter qwen/qwen3.8-27b --family qwen3.8 --input-price 0.5 --output-price 2; echo "exit $?"
wt model add corp-gateway some/model --family f; echo "exit $?"
wt model add omlx Some-Model-4bit --family some --tags code; echo "exit $?"
wt model edit omlx/Some-Model-4bit --tags code,design
wt model edit omlx/Some-Model-4bit --tags code,design
wt model list
wt model rm omlx/Some-Model-4bit --yes
ls "$D/home/.omlx/models"
wt model list
grep -o 'model_name: [^,]*' "$D/home/litellm.yaml"
```

Expected, as observed. `$D` stands for the scratch directory as wt prints it — `run.sh` resolves any link in it, so on macOS, where `/tmp` is a link, that is `/private/tmp/wt-step3-roundtrip`. A refusal is printed twice, once by cobra and once by wt.

```text
added model: openrouter/qwen--qwen3.8-27b
added provider: openrouter
openrouter/qwen--qwen3.8-27b: routed
exit 0
Error: invalid registry entry: model "corp-gateway/some--model": provider_id "corp-gateway" names no provider row: add a [[providers]] block for "corp-gateway" to $D/home/.config/local-ai/registry.toml and run this again (wt adds a default row by itself only for ollama, omlx, mtplx, mlx_lm_server and openrouter)
wt: invalid registry entry: model "corp-gateway/some--model": provider_id "corp-gateway" names no provider row: add a [[providers]] block for "corp-gateway" to $D/home/.config/local-ai/registry.toml and run this again (wt adds a default row by itself only for ollama, omlx, mtplx, mlx_lm_server and openrouter)
exit 1
added model: omlx/Some-Model-4bit
exit 0
updated model: omlx/Some-Model-4bit
no change: omlx/Some-Model-4bit
MODEL                         FAMILY   LOC    STATUS  RUNNING  SIZE  PATH
openrouter/qwen--qwen3.8-27b  qwen3.8  cloud  ok                  -  -
omlx/Some-Model-4bit          some     local  ok                  -  $D/home/.omlx/models/Some-Model-4bit
removed model: omlx/Some-Model-4bit
  weights are still at $D/home/.omlx/models/Some-Model-4bit
Some-Model-4bit
MODEL                         FAMILY   LOC    STATUS  RUNNING  SIZE  PATH
openrouter/qwen--qwen3.8-27b  qwen3.8  cloud  ok                  -  -
omlx/Some-Model-4bit          -        local  new                 -  $D/home/.omlx/models/Some-Model-4bit
model_name: openrouter/qwen--qwen3.8-27b
```

What to check: the first add seeds openrouter's row in the same write and its route is synced at once; a provider wt has no default row for is refused with the file to add it to; the second, identical edit says `no change` (and syncs nothing); after `rm` the weights directory is still there and the model is back as a `new` row; the LiteLLM config holds the cloud model's route only.

Then remove the scratch directory: `rm -rf /tmp/wt-step3-roundtrip`

- [ ] **Step 7: Verify the PR and commit**

From `wt/`:

```bash
test -z "$(gofmt -l .)" && go vet ./... && go test -count=1 ./... && make check
```

Expected: every package `ok`; `make check` ends without an error. From the monorepo root: `make test-all && make check-links`. Expected: exit 0.

```bash
git add wt/cmd/wt/litellm.go wt/cmd/wt/litellm_test.go wt/docs wt/CLAUDE.md wt/CHANGELOG.md
git commit -m "feat(wt): wt litellm sync names a provider that has no row; docs for wt model add, edit and rm"
```

- [ ] **Step 8: Hand off**

Stop here. Tell the owner the branch is ready, what `make test-all` printed, and what Step 6 printed. Say what was not done live: no add of an ollama model against a running daemon, so `ollama show`'s real output has only been read through the fixture in `ollama_test.go` (the shape modelman's parser and its tests use).

The spec's live check before merge — "one `wt model add`/`rm` round trip", done by hand — is still open, and it is the owner's: this plan's executor is barred from live providers and from the real registry. Hand the owner these four commands to run on their own machine, against their real registry and a running ollama, with a tag that is pulled and not registered (`wt model list` shows one as `STATUS new`), from a build of this branch:

```bash
wt model add ollama <a pulled tag> --family <family>   # expect: added model, no warning about model_info, and the route synced
wt model list                                          # expect: the row, STATUS ok
wt model rm ollama/<the tag> --yes                     # expect: removed model, and the `ollama rm` line
wt litellm list                                        # expect: the routes as they were before the add (a pulled ollama model stays routed without a registry entry)
```

What it tells that Step 6 cannot: whether `ollama show`'s real output is parsed (the registry row gains `[models.model_info]` when the model supports tools or vision), and that the sync after each command runs cleanly against the real `config.yaml` and proxy. PR C merges after it.

Push and open the PR only after the owner's OK. Suggested title: `feat(wt): wt model add, edit and rm`.

---

## PR D — the layout helpers move to `internal/tuilayout`

Branch `refactor/wt-tuilayout`. A refactor with no change in behaviour: its proof is that every existing `internal/tui` test passes unedited. Independent of PRs A, B and C.

`internal/configeditor` already imports `internal/tui` (for `ThemedListDelegate`), and the Models tab needs the same fit rules the launcher's pickers have: a view never taller or wider than the terminal, and a table that drops whole columns. The spec moves those helpers to a package both import. What moves is everything in `internal/tui/layout.go` that does not mention the launcher's `model`, and `tableColumns`, generalised.

Create the branch from the monorepo root:

```bash
git fetch origin
git switch -c refactor/wt-tuilayout origin/main
```

### Task 10: `tuilayout`: frames, fitting, and a table whose columns each table orders

**Files:**
- Create: `wt/internal/tuilayout/columns_test.go`
- Create: `wt/internal/tuilayout/layout.go`, `wt/internal/tuilayout/columns.go`
- Modify: `wt/internal/tui/layout.go` (the generic helpers leave; forwarders stay), `wt/internal/tui/modeltable.go:52-127` (`tableColumns` leaves), `:131-147` (`padRunes`, `maxRunes` forward), `:243-252`, `:267` and `:307` (`renderTable` builds a `tuilayout.Columns`), `wt/internal/tui/model_list.go:61-62` (`modelItem`), `:164-190` (`tableTitleRoom`, `fitTableColumns` leave)
- Modify: `wt/CLAUDE.md`, `wt/docs/internals/tui.md`

**Interfaces:**
- Consumes: nothing from this plan.
- Produces (package `tuilayout`):
  - `type ListFrame func(listView string) string`
  - `func Clip(s string, width int) string`
  - `func ListExtent(l list.Model, avail int) (width, floor int)`
  - `func FitList(height, minList int, frames ...ListFrame) (ListFrame, int)`
  - `func FrameSides(frame ListFrame) int`
  - `func FitTo(l *list.Model, termWidth, termHeight int, frames ...ListFrame)` — sizes the list to the room its frames leave, and fits the table in it when its items are `TableItem`s
  - `func DrawnFrame(l *list.Model, termHeight int, frames ...ListFrame) ListFrame`
  - `func SizeList(l *list.Model, width, height int)`
  - `const ColSep = "  "`, `const TitleRoom = 3`
  - `type Columns struct { Heads []string; Widths []int; Tail int; Prefix int; DropOrder []int }` with `NewColumns(heads []string, widths []int, prefix int, dropOrder []int) *Columns`, `(*Columns).Fit(width int)`, `Width() int`, `Header() string`, `Line(cells []string) string`, `Shown(i int) bool`
  - `type TableItem interface { TableColumns() *Columns }`
  - `func PadRunes(s string, w int) string`, `func MaxRunes(min int, ss ...string) int`
- Produces (package `tui`, unchanged names): `type listFrame = tuilayout.ListFrame` and the forwarders `clip`, `listExtent`, `fitList`, `frameSides`, `fitTo`, `drawnFrame`, `padRunes`, `maxRunes`; `func (m modelItem) TableColumns() *tuilayout.Columns`

- [ ] **Step 1: Write the failing tests**

Create `wt/internal/tuilayout/columns_test.go`:

```go
package tuilayout

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/lipgloss"
)

// testRow is a table row: it draws itself through the table's shared Columns.
type testRow struct {
	cells []string
	cols  *Columns
}

func (r testRow) FilterValue() string    { return strings.Join(r.cells, " ") }
func (r testRow) Title() string          { return strings.Repeat(" ", r.cols.Prefix) + r.cols.Line(r.cells) }
func (r testRow) Description() string    { return "" }
func (r testRow) TableColumns() *Columns { return r.cols }

// The test table: NAME and STATE are never dropped; SIZE goes first, then
// NOTE, then KIND.
const (
	tName = iota
	tKind
	tState
	tSize
	tNote
)

func testColumns(prefix int) *Columns {
	heads := []string{PadRunes("NAME", 12), PadRunes("KIND", 5), PadRunes("STATE", 7), PadRunes("SIZE", 6), "NOTE"}
	return NewColumns(heads, []int{12, 5, 7, 6, 9}, prefix, []int{tSize, tNote, tKind})
}

// TestColumnsDropInTheTablesOwnOrder verifies a table gives up its columns
// in the order it named, whole, and never the ones it did not name. The
// launcher's picker and the Models tab have different columns to protect;
// one fixed order would drop the wrong ones in one of them.
func TestColumnsDropInTheTablesOwnOrder(t *testing.T) {
	cases := []struct {
		width  int
		header string
	}{
		{200, "NAME          KIND   STATE    SIZE    NOTE"},
		{50, "NAME          KIND   STATE    SIZE    NOTE"},
		// 12+5+7+6+9 and four gaps is 47; the header needs TitleRoom more
		// than its own 42, so 47 still fits and 46 drops SIZE.
		{47, "NAME          KIND   STATE    SIZE    NOTE"},
		{46, "NAME          KIND   STATE    NOTE"},
		{38, "NAME          KIND   STATE"},
		// The rows fit 28 columns exactly, but the header (26) needs
		// TitleRoom more, so KIND goes.
		{29, "NAME          KIND   STATE"},
		{28, "NAME          STATE"},
		// Narrower than the columns that are never dropped: they stay.
		{10, "NAME          STATE"},
	}
	for _, c := range cases {
		cols := testColumns(0)
		cols.Fit(c.width)
		if got := cols.Header(); got != c.header {
			t.Errorf("width %d: header = %q, want %q", c.width, got, c.header)
		}
		if !cols.Shown(tName) || !cols.Shown(tState) {
			t.Errorf("width %d: a column that is never dropped was dropped", c.width)
		}
	}
	// A wider list gets the columns back: nothing is forgotten.
	cols := testColumns(0)
	cols.Fit(10)
	cols.Fit(200)
	if !cols.Shown(tSize) || !cols.Shown(tNote) || !cols.Shown(tKind) {
		t.Error("columns dropped at a narrow width did not come back at a wide one")
	}
}

// TestColumnsLineMatchesTheHeader verifies a row shows exactly the header's
// columns at every width, that a row's trailing padding is trimmed (an empty
// last cell included), and that the header is indented by the rows' prefix.
// A row that kept a column its header dropped would put values under the
// wrong heading.
func TestColumnsLineMatchesTheHeader(t *testing.T) {
	cols := testColumns(2)
	full := []string{PadRunes("alpha", 12), PadRunes("local", 5), PadRunes("ok", 7), PadRunes("5.2 GB", 6), "a note"}
	bare := []string{PadRunes("beta", 12), PadRunes("cloud", 5), PadRunes("ok", 7), PadRunes("-", 6), ""}
	if got, want := cols.Header(), "  NAME          KIND   STATE    SIZE    NOTE"; got != want {
		t.Errorf("header = %q, want %q", got, want)
	}
	if got, want := cols.Line(full), "alpha         local  ok       5.2 GB  a note"; got != want {
		t.Errorf("line = %q, want %q", got, want)
	}
	if got, want := cols.Line(bare), "beta          cloud  ok       -"; got != want {
		t.Errorf("a row with an empty last cell = %q, want %q", got, want)
	}
	cols.Fit(30)
	if got, want := cols.Line(full), "alpha         ok"; got != want {
		t.Errorf("narrow line = %q, want %q", got, want)
	}
	// Width counts the prefix and the widest note rows append.
	cols.Fit(200)
	if got := cols.Width(); got != 2+47 {
		t.Errorf("Width = %d, want 49", got)
	}
	cols.Tail = 12
	if got := cols.Width(); got != 2+47+12 {
		t.Errorf("Width with a 12-column note = %d, want 61", got)
	}
}

// TestFitToFitsATableInsideItsFrame verifies FitTo, given a list of table
// rows inside a frame, sets the list's title to the header of the columns
// that fit and leaves the whole view inside the terminal at every size wt
// supports — the header and a row still on screen. This is the path both
// programs' tables take; the launcher's own fit tests cover its screens, and
// this covers the shared rule without them.
func TestFitToFitsATableInsideItsFrame(t *testing.T) {
	for _, width := range []int{40, 80, 120} {
		for _, height := range []int{12, 24, 50} {
			cols := testColumns(0)
			var items []list.Item
			for i := 0; i < 30; i++ {
				items = append(items, testRow{cols: cols, cells: []string{
					PadRunes("model-"+strings.Repeat("x", i%6), 12), PadRunes("local", 5), PadRunes("ok", 7), PadRunes("1.0 GB", 6), "note",
				}})
			}
			delegate := list.NewDefaultDelegate()
			delegate.ShowDescription = false
			delegate.SetSpacing(0)
			l := list.New(items, delegate, width, height)
			l.SetShowStatusBar(false)
			l.Styles.Title = lipgloss.NewStyle()
			l.Styles.TitleBar = lipgloss.NewStyle().Padding(0, 0, 1, 0)
			frame := ListFrame(func(listView string) string {
				return Clip("a status line that is rather long and must be cut at the edge of a narrow terminal", width) + "\n\n" + listView + "\n" + Clip("keys", width)
			})
			FitTo(&l, width, height, frame)
			view := DrawnFrame(&l, height, frame)(l.View())
			if h := lipgloss.Height(view); h > height {
				t.Errorf("%dx%d: the view is %d lines tall", width, height, h)
			}
			if w := lipgloss.Width(view); w > width {
				t.Errorf("%dx%d: the view is %d columns wide", width, height, w)
			}
			if !strings.Contains(view, "NAME") || !strings.Contains(view, "STATE") || !strings.Contains(view, "model-") {
				t.Errorf("%dx%d: the header or the rows are not on screen:\n%s", width, height, view)
			}
			if width == 40 && strings.Contains(view, "SIZE") {
				t.Errorf("%dx%d: SIZE should have been dropped at this width:\n%s", width, height, view)
			}
			if width == 120 && !strings.Contains(view, "NOTE") {
				t.Errorf("%dx%d: every column fits at this width:\n%s", width, height, view)
			}
		}
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run, from `wt/`: `go test -count=1 ./internal/tuilayout`

Expected: the package does not build (it has a test file and nothing else).

```text
FAIL	github.com/ohanaverse/local-ai-setup/wt/internal/tuilayout [build failed]
ok  	github.com/ohanaverse/local-ai-setup/wt/internal/tui	20.258s
FAIL
# github.com/ohanaverse/local-ai-setup/wt/internal/tuilayout [github.com/ohanaverse/local-ai-setup/wt/internal/tuilayout.test]
internal/tuilayout/columns_test.go:14:9: undefined: Columns
internal/tuilayout/columns_test.go:20:34: undefined: Columns
internal/tuilayout/columns_test.go:32:31: undefined: Columns
```

- [ ] **Step 3: Move the sizing helpers**

Create `wt/internal/tuilayout/layout.go`. Its functions are the ones in `wt/internal/tui/layout.go` today, with their comments, exported; the one change is that `FitTo` calls `fitColumns` (Step 4) where `fitTo` called `fitTableColumns`.

```go
// Package tuilayout sizes a Bubble Tea screen built around a bubbles list so
// that it is never taller or wider than the terminal, and lays out a table
// whose columns are dropped whole when it is too wide. internal/tui (the
// launcher's pickers) and internal/configeditor (`wt config`) both import it,
// so the two programs fit a terminal by one rule.
package tuilayout

import (
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/lipgloss"
)

// This file is the one place a list-backed screen's height is worked out.
//
// Bubble Tea draws a view that is taller than the terminal by dropping lines
// from its TOP. Every screen here puts its header and its status line at the
// top and sizes a bubbles list underneath, so a list sized without counting
// the lines around it does not produce a scrolled or clipped list: it silently
// removes the header and the status. The model picker did exactly that — its
// list was the window minus two, under six to eight lines of its own — so its
// agent/tag header and every status it ever set were never on screen.
//
// The rule that prevents it: a screen describes the lines around its list as
// a ListFrame, the same function its View renders with, and FitList measures
// that frame to get the list's height. The count cannot drift from the view,
// because it IS the view, rendered around an empty list.

// The same holds sideways: a line wider than the terminal is cut at the right
// edge, so a list sized without counting the columns its frame puts beside it
// loses its last columns past the edge instead of truncating them with an
// ellipsis inside it. FrameSides measures those columns from the frame, too.
//
// The measuring is done once, by fitLists, in the Update that changed
// something; View does none of it. View asks DrawnFrame for the fullest frame
// that leaves the list the height it already has, which is the frame fitLists
// sized it for as long as both read the same model state. So after changing
// state by hand (a test setting m.status, say), go through Update before
// measuring View(): until then the list is still sized for the old state.

// ListFrame renders a screen around its list's view: everything the screen
// prints above, below and beside the list.
type ListFrame func(listView string) string

// Clip cuts every line of s to at most width columns, which is what the
// terminal does to a line that is too long — the text simply ends at the edge;
// nothing wraps, so the line count the height fit relies on does not change.
// Frames Clip their own free text (a status line, a path, the key hints on a
// narrow terminal) so that it cannot make the frame wider than the terminal
// and push the list's columns off the edge with it. A width that is not
// positive (no size reported yet, or no room at all) clips nothing.
func Clip(s string, width int) string {
	if width <= 0 {
		return s
	}
	return lipgloss.NewStyle().MaxWidth(width).Render(s)
}

// ListExtent measures what l really draws when it is given the columns avail
// and as little height as possible, and returns the two sizes the fit needs:
//
//   - width, the widest width to give l at which no line it draws is wider
//     than avail. A bubbles list is not exactly as wide as it is told: its
//     title bar is padded one column past the width, and its help line is cut
//     at whole key bindings, so a list told 37 to 39 draws 48 columns where
//     one told 36 draws 33. Neither rule is worth restating here, so the
//     width is found by trying: avail first, then one column less at a time.
//   - floor, the least height at which l draws itself within the height it is
//     given. Asked for less, it still draws its title bar, one item, its
//     pagination line and its help: seven lines for the model table, nine for
//     a prompt of title+description choices, about twelve with the full help
//     (`?`) open.
//
// Both are measured, by rendering a copy of the list squeezed to one line,
// rather than kept as constants, because they follow the list's own state and
// styles: a constant floor is wrong the moment the help is expanded. The copy
// is sized twice for the reason SizeList gives (two passes reach its fixed
// point; the third there only confirms it).
//
// The search gives up after listWidthSlack columns: if the list is still too
// wide by then (a terminal narrower than a single key binding), it is given
// avail and the terminal cuts its lines at the edge, as it always did.
func ListExtent(l list.Model, avail int) (width, floor int) {
	measure := func(w int) (rendered, height int) {
		l.SetSize(w, 1)
		l.SetSize(w, 1)
		view := l.View()
		return lipgloss.Width(view), lipgloss.Height(view)
	}
	for w := avail; w >= 1 && w > avail-listWidthSlack; w-- {
		if rendered, height := measure(w); rendered <= avail {
			return w, height
		}
	}
	_, floor = measure(avail)
	return avail, floor
}

// listWidthSlack bounds ListExtent's search. The widest step it has to get
// past is one help-line key binding ("↓/j down • ", eleven columns).
const listWidthSlack = 16

// FitList chooses how a screen is laid out in a terminal of the given height
// and how tall its list may be. frames are the screen's layouts in order of
// preference, fullest first; the first one that leaves the list at least
// minList lines (its floor, from ListExtent) is used, and the list gets every line that
// frame does not.
//
// When even the sparest frame leaves less than minList, the list is given
// minList anyway — bubbles would draw that many lines whatever it was told —
// and the view is taller than the terminal. Bubble Tea then drops lines from
// the top of the view: first whatever the sparest frame prints above the list
// (a status line), and once that is gone the list's own top lines, starting
// with its title — for the model table, the column header. The rows nearest
// the bottom and the key hints under the list are what remain.
//
// Before the terminal has reported a size (height <= 0) the fullest frame is
// used; nothing is drawn from it until a size arrives.
func FitList(height, minList int, frames ...ListFrame) (ListFrame, int) {
	if height <= 0 {
		return frames[0], minList
	}
	for _, frame := range frames {
		// An empty list view still occupies one line, hence the -1.
		if h := height - (lipgloss.Height(frame("")) - 1); h >= minList {
			return frame, h
		}
	}
	return frames[len(frames)-1], minList
}

// FrameSides is the number of columns frame puts beside its list: padding, a
// margin, any prefix. It is measured by rendering the frame around a probe
// line wider than anything else the frame prints, so that the probe's line is
// the widest; what the frame's width exceeds the probe's by is beside the list.
func FrameSides(frame ListFrame) int {
	probe := strings.Repeat("x", lipgloss.Width(frame(""))+1)
	return lipgloss.Width(frame(probe)) - len(probe)
}

// FitTo sizes l to the room its frames leave in a terminal of termWidth by
// termHeight: the widest width at which l draws within the columns the frame
// leaves beside it, and the height the fullest frame that fits leaves over.
// It is the one place a list is measured (ListExtent renders up to
// listWidthSlack probes), which is why it runs in Update and never in View.
func FitTo(l *list.Model, termWidth, termHeight int, frames ...ListFrame) {
	// The side columns are the same for every layout of a screen — the
	// layouts differ in the lines they print, not in their padding — so the
	// fullest one stands for all.
	// Never a width below one: bubbles is not given zero or a negative size.
	width, floor := ListExtent(*l, max(1, termWidth-FrameSides(frames[0])))
	_, height := FitList(termHeight, floor, frames...)
	// A table shows the columns that fit this width; other lists have no
	// table and are left alone.
	fitColumns(l, width)
	SizeList(l, width, height)
}

// DrawnFrame is the frame to draw l in: the fullest one that leaves l the
// height it has. FitTo gave l the room its chosen frame left over, and every
// fuller frame leaves less than that, so this is the frame FitTo chose —
// found from the list's own height instead of by measuring the list again on
// every View (every keystroke of a filter is a View). When FitTo could fit no
// frame it gave l its floor and FitList's last resort, the sparest frame,
// which is what this returns too.
func DrawnFrame(l *list.Model, termHeight int, frames ...ListFrame) ListFrame {
	frame, _ := FitList(termHeight, l.Height(), frames...)
	return frame
}

// SizeList tells l its size, as many times as it takes for l to draw itself
// within it. bubbles' SetSize works out how many rows fit on a page from the
// lines the list prints around them, and two of those are read as they were
// BEFORE the call:
//
//   - the pagination line, one row with a single page and two with several,
//     which follows the page count the previous sizing left;
//   - the help, whose expanded form (`?`) is two rows taller once the
//     next/previous-page keys are enabled. SetSize enables them only after it
//     has counted the rows, and bubbles' own `?` handler re-paginates without
//     touching them at all.
//
// So a size that tips the list from one page to several is computed with too
// many rows per page, and the list draws one line taller than it was told —
// two more when `?` is what tipped it, since the pass that corrects the
// pagination line is the one that enables the page keys. In a view that
// otherwise fits exactly, those lines push the top of the screen off until
// the next message.
//
// Sizing again corrects it, and the loop stops at a fixed point: a pass that
// leaves the page count, the rows per page and the page keys as it found them
// read the same pagination line and help a further pass would, so a further
// pass would change nothing. The page count alone is not enough to stop on —
// `?` leaves it at two before the first pass and at two after it, with the
// rows per page still wrong.
//
// Three passes always get there. The first leaves the page keys in step with
// the page count, whatever state it started from. If it ended on several
// pages, the second reads the two-row line and the taller help — the most
// those ever take — so it fits no more rows than the first and stays on
// several pages; if it ended on one page, the second reads the least they
// take, fits no fewer rows and stays on one. Either way the second pass
// counts its rows from the state it ends in, which is the fixed point, and
// the third only observes that nothing moved.
func SizeList(l *list.Model, width, height int) {
	for pass := 0; pass < 3; pass++ {
		pages, perPage, pageKeys := l.Paginator.TotalPages, l.Paginator.PerPage, l.KeyMap.NextPage.Enabled()
		l.SetSize(width, height)
		if l.Paginator.TotalPages == pages && l.Paginator.PerPage == perPage && l.KeyMap.NextPage.Enabled() == pageKeys {
			break
		}
	}
}
```

- [ ] **Step 4: Write `Columns`**

Create `wt/internal/tuilayout/columns.go`. It is `tableColumns` with slices for the fixed arrays, the row prefix and the drop order as fields, and one rule for a row's last cell: trailing spaces are trimmed. (The picker's table special-cased its last column, SURVEY, which is empty for most rows; trimming gives the same text.)

```go
package tuilayout

import (
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/list"
)

// ColSep separates two columns of a table.
const ColSep = "  "

// TitleRoom is how much wider than a table's header its list must be for
// bubbles to draw that header whole. The list's title bar appends two spaces
// to the title (the gap before a status message, there even when the message
// is empty) and cuts the result, with an ellipsis, to the list's width less
// the one column it reserves for its spinner. A header closer to the edge
// than this loses the end of its last heading, or keeps it and gains a stray
// "…" where the two spaces were cut.
//
// The rows need no such room: a delegate whose title styles have no padding
// (tui.ThemedListDelegate) draws a row whole up to the list's full width.
const TitleRoom = 3

// Columns is one table's column layout, shared by its header and every one
// of its rows so that they always show the same columns. A column is shown
// whole or not at all — in the header and in every row alike — which keeps
// the columns aligned and means the terminal's edge never cuts one in half.
//
// A table is a bubbles list whose title is the header and whose items each
// render one row through the shared Columns; FitTo narrows it to the list's
// width (TableItem).
type Columns struct {
	// Heads are the column headings, each padded to its column's width. The
	// last may be left unpadded.
	Heads []string
	// Widths are the columns' widths in runes.
	Widths []int
	// Tail is the widest text any row appends after its columns (a note such
	// as "(via proxy)" and the space before it).
	Tail int
	// Prefix is how many columns every row prints before its first cell (a
	// marker, a count). The header is indented by as much.
	Prefix int
	// DropOrder lists the columns a narrow list gives up, by index, first to
	// go first. A column that is not in it is never dropped.
	DropOrder []int

	shown []bool
}

// NewColumns returns a layout with every column shown.
func NewColumns(heads []string, widths []int, prefix int, dropOrder []int) *Columns {
	c := &Columns{Heads: heads, Widths: widths, Prefix: prefix, DropOrder: dropOrder, shown: make([]bool, len(heads))}
	c.showAll()
	return c
}

func (c *Columns) showAll() {
	for i := range c.shown {
		c.shown[i] = true
	}
}

// Shown reports whether column i is drawn at the width last fitted.
func (c *Columns) Shown(i int) bool { return c.shown[i] }

// Fit chooses the columns to show in a list of the given width. It starts
// from the whole table and gives up columns in DropOrder until what is left
// fits: every row (Width) within the list's width, and the header with
// TitleRoom to spare, which the list's title bar needs to draw it whole. If
// the columns that are never dropped are still too wide — a very narrow
// terminal, or a very long id — they stay and the list cuts the line at its
// right edge, the header up to three columns before the rows: that is the
// one case in which content is cut, and no cell is abbreviated to avoid it.
func (c *Columns) Fit(width int) {
	c.showAll()
	for _, drop := range c.DropOrder {
		if c.Width() <= width && utf8.RuneCountInString(c.Header())+TitleRoom <= width {
			return
		}
		c.shown[drop] = false
	}
}

// Width is the widest a row can be with the columns now shown: the prefix,
// the columns at their full width, and the longest note after them.
func (c *Columns) Width() int {
	w, n := c.Prefix+c.Tail, 0
	for i, shown := range c.shown {
		if shown {
			w += c.Widths[i]
			n++
		}
	}
	return w + len(ColSep)*(n-1)
}

// Header is the header line for the columns now shown, indented by Prefix,
// with the last heading's padding trimmed.
func (c *Columns) Header() string {
	var cells []string
	for i, shown := range c.shown {
		if shown {
			cells = append(cells, c.Heads[i])
		}
	}
	return strings.Repeat(" ", c.Prefix) + strings.TrimRight(strings.Join(cells, ColSep), " ")
}

// Line is one row's cells for the columns now shown, without the prefix. The
// row's trailing spaces are trimmed: the padding of its last cell, and the
// separator before a last cell that is empty.
func (c *Columns) Line(cells []string) string {
	var out []string
	for i, shown := range c.shown {
		if shown {
			out = append(out, cells[i])
		}
	}
	return strings.TrimRight(strings.Join(out, ColSep), " ")
}

// TableItem is a list item that is a row of a table: it draws itself through
// the Columns its whole table shares.
type TableItem interface {
	// TableColumns returns the table's shared layout, or nil for an item
	// that is not part of one.
	TableColumns() *Columns
}

// fitColumns narrows or widens the table in l to a list of the given width:
// it picks the columns that fit (Columns.Fit) and sets the list's title, the
// header, to match. The rows redraw from the shared layout by themselves, so
// nothing is rebuilt: the cursor and any filter are untouched. A list with
// no table rows is left alone.
func fitColumns(l *list.Model, width int) {
	for _, it := range l.Items() {
		if ti, ok := it.(TableItem); ok {
			if cols := ti.TableColumns(); cols != nil {
				cols.Fit(width)
				l.Title = cols.Header()
				return
			}
		}
	}
}

// PadRunes pads s with spaces to w runes; a longer s is returned as it is.
func PadRunes(s string, w int) string {
	if n := utf8.RuneCountInString(s); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return s
}

// MaxRunes is the rune count of the longest of ss, and at least min.
func MaxRunes(min int, ss ...string) int {
	w := min
	for _, s := range ss {
		if n := utf8.RuneCountInString(s); n > w {
			w = n
		}
	}
	return w
}
```

- [ ] **Step 5: Run the new package**

Run, from `wt/`: `go test -count=1 ./internal/tuilayout`

Expected: `ok  	github.com/ohanaverse/local-ai-setup/wt/internal/tuilayout`

- [ ] **Step 6: Make `internal/tui` use it**

`layout.go` keeps the launcher's frames (`modelFrames`, `agentFrames`, `worktreeFrame`, `choiceFrame`, `fitLists`, `(*model).fitTo`) and forwards the rest:

```diff
--- a/wt/internal/tui/layout.go
+++ b/wt/internal/tui/layout.go
@@ -2,138 +2,39 @@ package tui
 
 import (
 	"fmt"
-	"strings"
 
 	"github.com/charmbracelet/bubbles/list"
 	"github.com/charmbracelet/lipgloss"
 	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
+	"github.com/ohanaverse/local-ai-setup/wt/internal/tuilayout"
 )
 
-// This file is the one place a list-backed screen's height is worked out.
-//
-// Bubble Tea draws a view that is taller than the terminal by dropping lines
-// from its TOP. Every screen here puts its header and its status line at the
-// top and sizes a bubbles list underneath, so a list sized without counting
-// the lines around it does not produce a scrolled or clipped list: it silently
-// removes the header and the status. The model picker did exactly that — its
-// list was the window minus two, under six to eight lines of its own — so its
-// agent/tag header and every status it ever set were never on screen.
-//
-// The rule that prevents it: a screen describes the lines around its list as
-// a listFrame, the same function its View renders with, and fitList measures
-// that frame to get the list's height. The count cannot drift from the view,
-// because it IS the view, rendered around an empty list.
+// How a list-backed screen is fitted to the terminal is internal/tuilayout's
+// to say (it is shared with `wt config`): a screen describes the lines around
+// its list as a listFrame, the same function its View renders with, and fitTo
+// measures that frame for the list's size. This file holds the launcher's own
+// frames, and the names its code and tests have always used for the shared
+// helpers.
 
-// The same holds sideways: a line wider than the terminal is cut at the right
-// edge, so a list sized without counting the columns its frame puts beside it
-// loses its last columns past the edge instead of truncating them with an
-// ellipsis inside it. frameSides measures those columns from the frame, too.
-//
-// The measuring is done once, by fitLists, in the Update that changed
-// something; View does none of it. View asks drawnFrame for the fullest frame
-// that leaves the list the height it already has, which is the frame fitLists
-// sized it for as long as both read the same model state. So after changing
-// state by hand (a test setting m.status, say), go through Update before
-// measuring View(): until then the list is still sized for the old state.
+// listFrame renders a screen around its list's view (tuilayout.ListFrame).
+type listFrame = tuilayout.ListFrame
 
-// listFrame renders a screen around its list's view: everything the screen
-// prints above, below and beside the list.
-type listFrame func(listView string) string
+func clip(s string, width int) string { return tuilayout.Clip(s, width) }
 
-// clip cuts every line of s to at most width columns, which is what the
-// terminal does to a line that is too long — the text simply ends at the edge;
-// nothing wraps, so the line count the height fit relies on does not change.
-// Frames clip their own free text (a status line, a path, the key hints on a
-// narrow terminal) so that it cannot make the frame wider than the terminal
-// and push the list's columns off the edge with it. A width that is not
-// positive (no size reported yet, or no room at all) clips nothing.
-func clip(s string, width int) string {
-	if width <= 0 {
-		return s
-	}
-	return lipgloss.NewStyle().MaxWidth(width).Render(s)
-}
+func listExtent(l list.Model, avail int) (width, floor int) { return tuilayout.ListExtent(l, avail) }
 
-// listExtent measures what l really draws when it is given the columns avail
-// and as little height as possible, and returns the two sizes the fit needs:
-//
-//   - width, the widest width to give l at which no line it draws is wider
-//     than avail. A bubbles list is not exactly as wide as it is told: its
-//     title bar is padded one column past the width, and its help line is cut
-//     at whole key bindings, so a list told 37 to 39 draws 48 columns where
-//     one told 36 draws 33. Neither rule is worth restating here, so the
-//     width is found by trying: avail first, then one column less at a time.
-//   - floor, the least height at which l draws itself within the height it is
-//     given. Asked for less, it still draws its title bar, one item, its
-//     pagination line and its help: seven lines for the model table, nine for
-//     a prompt of title+description choices, about twelve with the full help
-//     (`?`) open.
-//
-// Both are measured, by rendering a copy of the list squeezed to one line,
-// rather than kept as constants, because they follow the list's own state and
-// styles: a constant floor is wrong the moment the help is expanded. The copy
-// is sized twice for the reason sizeList gives (two passes reach its fixed
-// point; the third there only confirms it).
-//
-// The search gives up after listWidthSlack columns: if the list is still too
-// wide by then (a terminal narrower than a single key binding), it is given
-// avail and the terminal cuts its lines at the edge, as it always did.
-func listExtent(l list.Model, avail int) (width, floor int) {
-	measure := func(w int) (rendered, height int) {
-		l.SetSize(w, 1)
-		l.SetSize(w, 1)
-		view := l.View()
-		return lipgloss.Width(view), lipgloss.Height(view)
-	}
-	for w := avail; w >= 1 && w > avail-listWidthSlack; w-- {
-		if rendered, height := measure(w); rendered <= avail {
-			return w, height
-		}
-	}
-	_, floor = measure(avail)
-	return avail, floor
+func fitList(height, minList int, frames ...listFrame) (listFrame, int) {
+	return tuilayout.FitList(height, minList, frames...)
 }
 
-// listWidthSlack bounds listExtent's search. The widest step it has to get
-// past is one help-line key binding ("↓/j down • ", eleven columns).
-const listWidthSlack = 16
+func frameSides(frame listFrame) int { return tuilayout.FrameSides(frame) }
 
-// fitList chooses how a screen is laid out in a terminal of the given height
-// and how tall its list may be. frames are the screen's layouts in order of
-// preference, fullest first; the first one that leaves the list at least
-// minList lines (its floor, from listExtent) is used, and the list gets every line that
-// frame does not.
-//
-// When even the sparest frame leaves less than minList, the list is given
-// minList anyway — bubbles would draw that many lines whatever it was told —
-// and the view is taller than the terminal. Bubble Tea then drops lines from
-// the top of the view: first whatever the sparest frame prints above the list
-// (a status line), and once that is gone the list's own top lines, starting
-// with its title — for the model table, the column header. The rows nearest
-// the bottom and the key hints under the list are what remain.
-//
-// Before the terminal has reported a size (height <= 0) the fullest frame is
-// used; nothing is drawn from it until a size arrives.
-func fitList(height, minList int, frames ...listFrame) (listFrame, int) {
-	if height <= 0 {
-		return frames[0], minList
-	}
-	for _, frame := range frames {
-		// An empty list view still occupies one line, hence the -1.
-		if h := height - (lipgloss.Height(frame("")) - 1); h >= minList {
-			return frame, h
-		}
-	}
-	return frames[len(frames)-1], minList
+func fitTo(l *list.Model, termWidth, termHeight int, frames ...listFrame) {
+	tuilayout.FitTo(l, termWidth, termHeight, frames...)
 }
 
-// frameSides is the number of columns frame puts beside its list: padding, a
-// margin, any prefix. It is measured by rendering the frame around a probe
-// line wider than anything else the frame prints, so that the probe's line is
-// the widest; what the frame's width exceeds the probe's by is beside the list.
-func frameSides(frame listFrame) int {
-	probe := strings.Repeat("x", lipgloss.Width(frame(""))+1)
-	return lipgloss.Width(frame(probe)) - len(probe)
+func drawnFrame(l *list.Model, termHeight int, frames ...listFrame) listFrame {
+	return tuilayout.DrawnFrame(l, termHeight, frames...)
 }
 
 // pickerPadX is the model picker's horizontal padding on each side. It is
@@ -276,81 +177,7 @@ func (m *model) fitLists() {
 	}
 }
 
-// fitTo sizes l to the room its frames leave in a terminal of termWidth by
-// termHeight: the widest width at which l draws within the columns the frame
-// leaves beside it, and the height the fullest frame that fits leaves over.
-// It is the one place a list is measured (listExtent renders up to
-// listWidthSlack probes), which is why it runs in Update and never in View.
-func fitTo(l *list.Model, termWidth, termHeight int, frames ...listFrame) {
-	// The side columns are the same for every layout of a screen — the
-	// layouts differ in the lines they print, not in their padding — so the
-	// fullest one stands for all.
-	// Never a width below one: bubbles is not given zero or a negative size.
-	width, floor := listExtent(*l, max(1, termWidth-frameSides(frames[0])))
-	_, height := fitList(termHeight, floor, frames...)
-	// A model table shows the columns that fit this width; other lists have
-	// no table and are left alone.
-	fitTableColumns(l, width)
-	sizeList(l, width, height)
-}
-
-// drawnFrame is the frame to draw l in: the fullest one that leaves l the
-// height it has. fitTo gave l the room its chosen frame left over, and every
-// fuller frame leaves less than that, so this is the frame fitTo chose —
-// found from the list's own height instead of by measuring the list again on
-// every View (every keystroke of a filter is a View). When fitTo could fit no
-// frame it gave l its floor and fitList's last resort, the sparest frame,
-// which is what this returns too.
-func drawnFrame(l *list.Model, termHeight int, frames ...listFrame) listFrame {
-	frame, _ := fitList(termHeight, l.Height(), frames...)
-	return frame
-}
-
 // fitTo sizes l to the room its frames leave in this model's window.
 func (m *model) fitTo(l *list.Model, frames ...listFrame) {
 	fitTo(l, m.width, m.height, frames...)
 }
-
-// sizeList tells l its size, as many times as it takes for l to draw itself
-// within it. bubbles' SetSize works out how many rows fit on a page from the
-// lines the list prints around them, and two of those are read as they were
-// BEFORE the call:
-//
-//   - the pagination line, one row with a single page and two with several,
-//     which follows the page count the previous sizing left;
-//   - the help, whose expanded form (`?`) is two rows taller once the
-//     next/previous-page keys are enabled. SetSize enables them only after it
-//     has counted the rows, and bubbles' own `?` handler re-paginates without
-//     touching them at all.
-//
-// So a size that tips the list from one page to several is computed with too
-// many rows per page, and the list draws one line taller than it was told —
-// two more when `?` is what tipped it, since the pass that corrects the
-// pagination line is the one that enables the page keys. In a view that
-// otherwise fits exactly, those lines push the top of the screen off until
-// the next message.
-//
-// Sizing again corrects it, and the loop stops at a fixed point: a pass that
-// leaves the page count, the rows per page and the page keys as it found them
-// read the same pagination line and help a further pass would, so a further
-// pass would change nothing. The page count alone is not enough to stop on —
-// `?` leaves it at two before the first pass and at two after it, with the
-// rows per page still wrong.
-//
-// Three passes always get there. The first leaves the page keys in step with
-// the page count, whatever state it started from. If it ended on several
-// pages, the second reads the two-row line and the taller help — the most
-// those ever take — so it fits no more rows than the first and stays on
-// several pages; if it ended on one page, the second reads the least they
-// take, fits no fewer rows and stays on one. Either way the second pass
-// counts its rows from the state it ends in, which is the fixed point, and
-// the third only observes that nothing moved.
-func sizeList(l *list.Model, width, height int) {
-	for pass := 0; pass < 3; pass++ {
-		pages, perPage, pageKeys := l.Paginator.TotalPages, l.Paginator.PerPage, l.KeyMap.NextPage.Enabled()
-		l.SetSize(width, height)
-		if l.Paginator.TotalPages == pages && l.Paginator.PerPage == perPage && l.KeyMap.NextPage.Enabled() == pageKeys {
-			break
-		}
-	}
-}
```

The picker's table builds a `tuilayout.Columns`:

```diff
--- a/wt/internal/tui/modeltable.go
+++ b/wt/internal/tui/modeltable.go
@@ -3,7 +3,6 @@ package tui
 import (
 	"errors"
 	"fmt"
-	"strings"
 	"sync"
 	"unicode/utf8"
 
@@ -12,12 +11,13 @@ import (
 	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
 	"github.com/ohanaverse/local-ai-setup/wt/internal/refcount"
 	"github.com/ohanaverse/local-ai-setup/wt/internal/survey"
+	"github.com/ohanaverse/local-ai-setup/wt/internal/tuilayout"
 )
 
 // modelTable is the rendered selector: a header line (shown as the list's
 // title) and one item per sorted row. header is the full table's; the items
-// share one tableColumns, which decides how much of the table is drawn at the
-// width the list is given (fitTableColumns).
+// share one tuilayout.Columns, which decides how much of the table is drawn
+// at the width the list is given (tuilayout.FitTo).
 type modelTable struct {
 	header string
 	items  []*modelItem
@@ -38,112 +38,18 @@ const (
 	numCols
 )
 
-// colSep separates two columns.
-const colSep = "  "
-
 // colDropOrder is the order in which columns are given up when the table is
-// wider than its list: the survey segment first, then usage from the longest
+// wider than its list (tuilayout.Columns.DropOrder): the survey segment first, then usage from the longest
 // window to the shortest, cost, location and family. MODEL, STATUS and RUNNING
 // are not in it: they say what a row is and whether Enter launches or starts
 // it, so they are never dropped.
 var colDropOrder = []int{colSurvey, col30D, col7D, col1D, colCost, colLoc, colFamily}
 
-// tableColumns is one table's column layout, shared by its header and every
-// one of its rows so that they always show the same columns. A column is shown
-// whole or not at all — in the header and in every row alike — which keeps the
-// columns aligned and means the terminal's edge never cuts one in half.
-type tableColumns struct {
-	heads  [numCols]string // column headings, padded to the column's width (SURVEY is not padded: it is last)
-	widths [numCols]int    // each column's width in runes
-	// tail is the widest text any row appends after its columns: the
-	// exception note ("(via proxy)") and the space before it.
-	tail  int
-	shown [numCols]bool
-}
-
-// fit chooses the columns to show in a list of the given width. It starts
-// from the whole table and gives up columns in colDropOrder until what is
-// left fits: every row (width) within the list's width, and the header with
-// tableTitleRoom to spare, which the list's title bar needs to draw it whole.
-// If MODEL, STATUS and RUNNING alone are still too wide — a very narrow
-// terminal, or a very long model id — those three stay and the list cuts the
-// line at its right edge, the header up to three columns before the rows:
-// that is the one case in which content is cut, and the model id is never
-// abbreviated to avoid it.
-func (c *tableColumns) fit(width int) {
-	for i := range c.shown {
-		c.shown[i] = true
-	}
-	for _, drop := range colDropOrder {
-		if c.width() <= width && utf8.RuneCountInString(c.header())+tableTitleRoom <= width {
-			return
-		}
-		c.shown[drop] = false
-	}
-}
-
-// width is the widest a row can be with the columns now shown: the prefix,
-// the columns at their full width, and the longest exception note.
-func (c *tableColumns) width() int {
-	w, n := rowPrefixWidth+c.tail, 0
-	for i, shown := range c.shown {
-		if shown {
-			w += c.widths[i]
-			n++
-		}
-	}
-	return w + len(colSep)*(n-1)
-}
-
-// header is the header line for the columns now shown.
-func (c *tableColumns) header() string {
-	var cells []string
-	for i, shown := range c.shown {
-		if shown {
-			cells = append(cells, c.heads[i])
-		}
-	}
-	// With every column shown the last heading is SURVEY, which is not
-	// padded; with it dropped the last one is, and the padding is trimmed.
-	return strings.Repeat(" ", rowPrefixWidth) + strings.TrimRight(strings.Join(cells, colSep), " ")
-}
-
-// line is one row's columns for the layout now shown: the padded cells, then
-// the survey segment when that column is shown and the row has one. A row
-// that ends in a padded cell has its trailing spaces trimmed, exactly as the
-// full table's rows do.
-func (c *tableColumns) line(cells [numCols]string) string {
-	var out []string
-	for i, shown := range c.shown {
-		if shown && i != colSurvey {
-			out = append(out, cells[i])
-		}
-	}
-	line := strings.Join(out, colSep)
-	if c.shown[colSurvey] && cells[colSurvey] != "" {
-		return line + colSep + cells[colSurvey]
-	}
-	return strings.TrimRight(line, " ")
-}
-
 const rowPrefixWidth = 4 // ref column (2) + rotation marker (2), composed by modelItem.Title()
 
-func padRunes(s string, w int) string {
-	if n := utf8.RuneCountInString(s); n < w {
-		return s + strings.Repeat(" ", w-n)
-	}
-	return s
-}
+func padRunes(s string, w int) string { return tuilayout.PadRunes(s, w) }
 
-func maxRunes(min int, ss ...string) int {
-	w := min
-	for _, s := range ss {
-		if n := utf8.RuneCountInString(s); n > w {
-			w = n
-		}
-	}
-	return w
-}
+func maxRunes(min int, ss ...string) int { return tuilayout.MaxRunes(min, ss...) }
 
 // runningText is a row's RUNNING cell: "run" for a model that is serving,
 // "load" for one omlx is still loading (#259; Enter starts it, which waits for
@@ -239,17 +145,17 @@ func renderTable(rows []tableRow, cfg *config.Config, agent string, refs map[str
 		wS = maxRunes(wS, string(r.Status))
 	}
 	// One layout for the header and every row. It starts with every column
-	// shown; whoever sizes the list narrows it (fitTableColumns).
-	cols := &tableColumns{
-		heads: [numCols]string{
+	// shown; whoever sizes the list narrows it (tuilayout.FitTo).
+	cols := tuilayout.NewColumns(
+		[]string{
 			padRunes("FAMILY", famW), padRunes("MODEL", idW), padRunes("LOC", 5), padRunes("STATUS", wS),
 			padRunes("RUNNING", 7), padRunes("COST", costW),
 			padRunes("1D", w1), padRunes("7D", w7), padRunes("30D", w30), "SURVEY",
 		},
-		widths: [numCols]int{famW, idW, 5, wS, 7, costW, w1, w7, w30, len("SURVEY")},
-	}
-	cols.fit(int(^uint(0) >> 1))
-	header := cols.header()
+		[]int{famW, idW, 5, wS, 7, costW, w1, w7, w30, len("SURVEY")},
+		rowPrefixWidth, colDropOrder,
+	)
+	header := cols.Header()
 
 	items := make([]*modelItem, 0, len(rows))
 	for i, r := range rows {
@@ -257,17 +163,17 @@ func renderTable(rows []tableRow, cfg *config.Config, agent string, refs map[str
 		if loc == "" {
 			loc = "-"
 		}
-		// The last padded column would leave trailing spaces; tableColumns.line
-		// keeps them only when the survey segment follows (so it stays
-		// column-aligned).
-		cells := [numCols]string{
+		// The last padded column would leave trailing spaces; Columns.Line
+		// trims them, so they stay only when the survey segment follows (and
+		// keeps it column-aligned).
+		cells := []string{
 			padRunes(fam[i], famW), padRunes(r.Model.ID, idW), padRunes(loc, 5), padRunes(string(r.Status), wS),
 			padRunes(runningText(r.Row), 7), padRunes(cost[i], costW),
 			padRunes(c1[i], w1), padRunes(c7[i], w7), padRunes(c30[i], w30),
 			survey.FormatPickerSegment(r.stats),
 		}
-		cols.widths[colSurvey] = maxRunes(cols.widths[colSurvey], cells[colSurvey])
-		it := &modelItem{model: r.Model, line: cols.line(cells), cells: cells, cols: cols, marked: lastID != "" && r.Model.ID == lastID, ref: refs[r.Model.ID]}
+		cols.Widths[colSurvey] = maxRunes(cols.Widths[colSurvey], cells[colSurvey])
+		it := &modelItem{model: r.Model, line: cols.Line(cells), cells: cells, cols: cols, marked: lastID != "" && r.Model.ID == lastID, ref: refs[r.Model.ID]}
 		switch r.Action() {
 		case catalog.ActionBlock:
 			it.blocked = r.BlockReason()
@@ -304,7 +210,7 @@ func renderTable(rows []tableRow, cfg *config.Config, agent string, refs map[str
 			}
 		}
 		if it.exception != "" {
-			cols.tail = max(cols.tail, 1+utf8.RuneCountInString(it.exception))
+			cols.Tail = max(cols.Tail, 1+utf8.RuneCountInString(it.exception))
 		}
 		items = append(items, it)
 	}
```

And its rows say which table they belong to, which is how `tuilayout.FitTo` finds it:

```diff
--- a/wt/internal/tui/model_list.go
+++ b/wt/internal/tui/model_list.go
@@ -9,6 +9,7 @@ import (
 	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
 	"github.com/ohanaverse/local-ai-setup/wt/internal/refcount"
 	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
+	"github.com/ohanaverse/local-ai-setup/wt/internal/tuilayout"
 	"github.com/ohanaverse/local-ai-setup/wt/internal/usage"
 )
 
@@ -58,8 +59,8 @@ func realNewRefcountStore() refcount.Store { return refcount.NewStore() }
 type modelItem struct {
 	model     config.Model
 	line      string
-	cells     [numCols]string
-	cols      *tableColumns
+	cells     []string
+	cols      *tuilayout.Columns
 	marked    bool
 	ref       int
 	exception string
@@ -110,7 +111,7 @@ func (m modelItem) Title() string {
 	prefix := refColumn(m.ref)
 	line := m.line
 	if m.cols != nil {
-		line = m.cols.line(m.cells)
+		line = m.cols.Line(m.cells)
 	}
 	if m.exception != "" {
 		line += " " + m.exception
@@ -161,33 +162,12 @@ func styleTableTitle(l *list.Model, theme themes.Theme) {
 	l.Styles.TitleBar = lipgloss.NewStyle().Padding(0, 0, 1, 0)
 }
 
-// tableTitleRoom is how much wider than the table's header its list must be
-// for bubbles to draw that header whole. The list's title bar appends two
-// spaces to the title (the gap before a status message, there even when the
-// message is empty) and cuts the result, with an ellipsis, to the list's width
-// less the one column it reserves for its spinner. A header closer to the edge
-// than this loses the end of its last heading, or keeps it and gains a stray
-// "…" where the two spaces were cut.
-//
-// The rows need no such room. The delegate would keep a row clear of its
-// title styles' padding, but ThemedListDelegate's title styles have none, so a
-// row is drawn whole up to the list's full width.
-const tableTitleRoom = 3
-
-// fitTableColumns narrows or widens the table in l to a list of the given
-// width: it picks the columns that fit (tableColumns.fit) and sets the header
-// to match. The rows redraw from the shared layout by themselves, so nothing
-// is rebuilt: the inventory is not probed again and the cursor, the marked
-// row and any filter are untouched. A list with no rows has nothing to fit.
-func fitTableColumns(l *list.Model, width int) {
-	for _, it := range l.Items() {
-		if mi, ok := it.(*modelItem); ok && mi.cols != nil {
-			mi.cols.fit(width)
-			l.Title = mi.cols.header()
-			return
-		}
-	}
-}
+// TableColumns is the layout this row's table shares, which is how
+// tuilayout.FitTo finds the table in a list and shows the columns that fit
+// its width: nothing is rebuilt on a resize, so the inventory is not probed
+// again and the cursor, the marked row and any filter are untouched. nil for
+// an item built by hand.
+func (m modelItem) TableColumns() *tuilayout.Columns { return m.cols }
 
 // clampModelSelection guards against bubbles v1.0.0 leaving
 // m.Index() outside [0, len(VisibleItems())) after a filter
```

- [ ] **Step 7: Run the launcher's tests, unedited**

Run, from `wt/`: `go build ./... && go test -count=1 ./internal/tui ./internal/tuilayout ./internal/configeditor ./cmd/wt`

Expected: all four `ok` (`internal/tui` takes about 20 seconds). No file under `wt/internal/tui/*_test.go` was changed: `git status --short wt/internal/tui` lists only `layout.go`, `modeltable.go` and `model_list.go`. `TestModelTableUnchangedWhenItFits` compares the table with one captured before columns could be dropped, and `TestEveryListPhaseFitsTheTerminal` measures every launcher screen at nine sizes; both passing is what says the move changed nothing.

- [ ] **Step 8: Document it**

No `CHANGELOG.md` entry: nothing a user sees changes.

In `wt/CLAUDE.md`, find:

```text
themes,tui,configeditor,
```

and replace it with:

```text
themes,tui,tuilayout,configeditor,
```

In `wt/CLAUDE.md`, find:

```text
(`PickModel`, the route-gated variant, currently has no production caller) |
```

and replace it with:

```text
(`PickModel`, the route-gated variant, currently has no production caller) |
| `internal/tuilayout/` | what both TUIs fit a terminal with: `ListFrame`, `FitTo`, `DrawnFrame`, `Clip` (a list-backed screen is never taller or wider than the terminal) and `Columns` (a table that drops whole columns, in an order each table names; a list item joins one through `TableItem`) |
```

In `wt/CLAUDE.md`, find:

```text
- **A view fits the terminal** (`layout.go`):
```

and replace it with:

```text
- **A view fits the terminal** (`internal/tuilayout`, shared with `wt config`; `layout.go` holds the launcher's frames):
```

In `wt/CLAUDE.md`, find:

```text
(`tableColumns`, `fitTableColumns`)
```

and replace it with:

```text
(`tuilayout.Columns`, fitted by `tuilayout.FitTo`)
```

In `wt/docs/internals/tui.md`, find:

```text
> **A view must never be taller or wider than the terminal** (`layout.go`).
```

and replace it with:

```text
> **A view must never be taller or wider than the terminal** (`internal/tuilayout`, which `wt config` uses too; `layout.go` holds the launcher's frames and keeps the lowercase names its code and tests use — `listFrame`, `fitTo`, `fitList`, `frameSides`, `listExtent`, `drawnFrame`, `clip` — as one-line forwarders).
```

In `wt/docs/internals/tui.md`, find:

```text
(`modeltable.go`, `tableColumns`). The header and every row share one `tableColumns`; `fitTableColumns` (called by `fitTo` and by the standalone `pickModel`) shows
```

and replace it with:

```text
(`modeltable.go`, `tuilayout.Columns`). The header and every row share one `tuilayout.Columns`; `tuilayout.FitTo` (what `fitTo` and the standalone `pickModel` call) finds it through the rows' `TableColumns()` and shows
```

- [ ] **Step 9: Verify the PR and commit**

From `wt/`:

```bash
test -z "$(gofmt -l .)" && go vet ./... && go test -count=1 ./... && make check
```

Expected: every package `ok`; `make check` ends without an error. From the monorepo root: `make test-all && make check-links`. Expected: exit 0.

```bash
git add wt/internal/tuilayout wt/internal/tui wt/CLAUDE.md wt/docs/internals/tui.md
git commit -m "refactor(wt): the list-fitting helpers and the column-dropping table move to internal/tuilayout"
```

- [ ] **Step 10: Hand off**

Stop here. Tell the owner the branch is ready and what `make test-all` printed, and that no test file under `internal/tui` changed. Push and open the PR only after the owner's OK. Suggested title: `refactor(wt): internal/tuilayout, shared by the launcher and wt config`.

---

## PR E — the tab bar, the Models tab's list, removal, and the exit sync

Branch `feat/wt-models-tab`. Needs PR C (`modeladmin.Remove`, `syncRoutesAfterWrite`'s callers, `ollamaCaps`) and PR D (`tuilayout`) on `main`.

`wt config` is one Bubble Tea program, `internal/configeditor`, showing one list of agents. This PR gives it a tab bar and a second tab. The Agents tab keeps its buffer, `ctrl+s` and its quit prompt. The Models tab is different in one respect the user must be able to rely on: a change there is in `registry.toml` the moment it is confirmed. What is deferred is only the LiteLLM route sync, which runs once, in `cmd/wt`, after the editor has closed.

Two faults of the Agents list on `main` are fixed here, because the Models tab is built on the same pattern and would have had both: its `/` filter narrows nothing (the editor drops the list's filter results), and `esc` on the list ends wt without the unsaved-changes prompt (it is the list's own quit key). The Agents tab's one visible change besides the tab bar is its help line, which gains `tab models`.

Create the branch from the monorepo root:

```bash
git fetch origin
git switch -c feat/wt-models-tab origin/main
```

### Task 11: The tab bar and the Models tab

**Files:**
- Create: `wt/internal/configeditor/testmain_test.go`, `wt/internal/configeditor/models_tab_test.go`
- Modify: `wt/internal/configeditor/editor_test.go:183-194` (`TestRun_EmptyConfig_Launches`), `:274` (the title assertion)
- Create: `wt/internal/configeditor/tabs.go`, `wt/internal/configeditor/models_tab.go`
- Modify: `wt/internal/configeditor/editor.go:1-3` (package comment), `:37-39` (`model`), `:87-92` (`Init`), `:101-108` (`Update`), `:192` (`update`'s message switch), `:219-240` (key handling), `:278` (new `quit` and `leave`, before `handleQuitUpdate`), `:296-314` (`View`), `:343-352` (`Run`)
- Modify: `wt/internal/configeditor/agents_tab.go:7`, `:89-91` (`buildAgentsList`: no quit keys of its own; two help keys)
- Modify: `wt/cmd/wt/commands_config.go:26-28` (`configeditorRun` calls the new `Run`, so that `cmd/wt` builds; Task 12 gives it the options and the result)

**Interfaces:**
- Consumes: `modeladmin.Rows`, `Row`, `WeightsNote`, `FormatSize` (Task 4), `modeladmin.Remove` (Task 7); `tuilayout.Columns`, `NewColumns`, `ListFrame`, `FitTo`, `DrawnFrame`, `Clip`, `PadRunes`, `MaxRunes` (Task 10); existing: `tui.ThemedListDelegate(theme)`, `config.Load`, `localmodels.Inventory`, `config.DefaultSeedEnv`, `modeladmin.OllamaCapabilities`, the editor's `model`, `handleSave`, `statusBlock`, `fitList`, `buildAgentsList`; bubbles' `list.FilterMatchesMsg`, `(*list.Model).DisableQuitKeybindings`, `AdditionalShortHelpKeys`
- Produces (package `configeditor`):
  - `type Tab int` with `TabAgents`, `TabModels`
  - `type Options struct { StartTab Tab; Models ModelsDeps }`
  - `type ModelsDeps struct { Load func() (*config.Config, error); Probe func(*config.Config) localmodels.Snapshot; SeedEnv func() config.SeedEnv; Capabilities func(cfg *config.Config, name string) (map[string]any, error) }` — `Run` fills any nil field with the real function
  - `type Result struct { RegistryChanged bool }`
  - `func Run(theme themes.Theme, cfg *config.Config, cfgErr error, o Options, opts ...tea.ProgramOption) (Result, error)` — **the signature changes**; this task adapts `cmd/wt`'s one call so that it builds, and Task 12 makes use of the options and the result
  - unexported, for Task 14: `model.tab`, `model.opts`, `model.models` (`modelsTab` with `phase`, `list`, `cfg`, `rows`, `loaded`, `busy`, `writing`, `status`, `selectID`), `model.registryChanged`, `model.quitPending`, `modelsPhase` (`modelsList`, `modelsRemove`), `modelRow`, `(*model).probeCmd() tea.Cmd`, `selectedModel() (modeladmin.Row, bool)`, `quit() (tea.Model, tea.Cmd)` (the unsaved-changes prompt, then `leave`), `leave() (tea.Model, tea.Cmd)` (quits, or records `quitPending` while `models.writing`), `updateModels`, `modelsView`, `fitModels`, `tabBar(theme, current) string`, `var modelsHints []string` (fullest first), `fitHints(width int, hints []string) string`, `wrapText(s string, width int) string`, `middleCut(s string, w int) string`, `tildePath(path string) string`, the seam `var userHome = os.UserHomeDir`
  - test helpers, for Tasks 14 and 18: `tabRegistry`, `tabWeights`, `tabWeightsShown`, `tabMachine` with `newTabMachine(t, content)`, `text(t)` and `deps()`, `send(t, m, msg) *model`, `keys(t, m, ks...) *model`, `keyMsg(k string) tea.KeyMsg`, `modelsEditor(t, tm, width, height) *model`, `selectModel(t, m, id) *model`, `findRow(m, id)`, `flat(view string) string`, `assertFits(t, name, view, width, height)`

- [ ] **Step 1: Isolate the package's tests**

`internal/configeditor` has no `TestMain`. From this task on its tests write a registry through `config.UpdateRegistry`, so it needs one, and the assertion that the guard is armed. The package's other tests pass without this file — each redirects its own registry — so nothing fails when it is missing: Step 7 checks that it is there.

Create `wt/internal/configeditor/testmain_test.go`:

```go
package configeditor

import (
	"os"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// TestMain keeps this package's tests off the developer's machine. The Models
// tab writes registry.toml through config.UpdateRegistry, so the package
// needs the throwaway config home and the registry write guard it arms: a
// test that forgot to redirect the registry would otherwise edit the real
// one.
func TestMain(m *testing.M) {
	_, cleanup := config.IsolateConfigHomeForTest()
	// The tab abbreviates a path under the home directory; the tests' paths
	// are under this one, not the developer's.
	userHome = func() (string, error) { return "/Users/dev", nil }
	code := m.Run()
	cleanup()
	os.Exit(code)
}

// TestRegistryWriteGuardIsArmedHere verifies this test binary cannot write
// the developer's registry. The guard is off unless a TestMain arms it, so a
// package that reaches the writer and forgot would fail open.
func TestRegistryWriteGuardIsArmedHere(t *testing.T) {
	if !config.RegistryWriteGuardArmed() {
		t.Fatal("the registry write guard is not armed: TestMain must call config.IsolateConfigHomeForTest")
	}
}
```

- [ ] **Step 2: Write the failing tests**

Create `wt/internal/configeditor/models_tab_test.go`:

```go
package configeditor

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// tabRegistry is the registry the Models tab tests edit: an omlx and an
// openrouter provider, a long-id cloud model, an omlx model that is on disk
// and one that is not.
const tabRegistry = `[[providers]]
id = "omlx"
name = "oMLX"
location = "local"

[providers.auth]
type = "none"
base_url = "http://localhost:8000"

[[providers]]
id = "openrouter"
name = "OpenRouter"
location = "cloud"

[providers.auth]
type = "api_key"
secret_ref = "OPENROUTER_API_KEY"
base_url = "https://openrouter.ai/api/v1"

[[models]]
id = "openrouter/anthropic--claude-sonnet-4.5-thinking"
family = "sonnet"
provider_id = "openrouter"
model_name = "anthropic/claude-sonnet-4.5-thinking"
tags = [
    "code",
]

[models.cost]
input_price_per_million = 3
output_price_per_million = 15

[[models]]
id = "omlx/Qwen3.8-27B-Instruct-MLX-6bit"
family = "qwen3.8"
provider_id = "omlx"
model_name = "Qwen3.8-27B-Instruct-MLX-6bit"
tags = []

[[models]]
id = "omlx/Gone-4bit"
family = "qwen3.8"
provider_id = "omlx"
model_name = "Gone-4bit"
tags = []
`

// tabMachine is a throwaway machine for the Models tab: a registry file wt
// really reads and writes, and a canned probe. probes counts inventory
// rounds.
type tabMachine struct {
	registry string
	probes   int
}

// tabWeights is where the probe finds the omlx model; tabWeightsShown is how
// the tab writes it (TestMain makes /Users/dev the home directory).
const (
	tabWeights      = "/Users/dev/.omlx/models/Qwen3.8-27B-Instruct-MLX-6bit"
	tabWeightsShown = "~/.omlx/models/Qwen3.8-27B-Instruct-MLX-6bit"
)

// tabLongID is a 72-column id, wider than the MODEL column can be at 80
// columns: the name of a locally quantized model.
const tabLongID = "omlx/Qwen3.8-35B-A3B-Instruct-abliterated-heretic-MLX-dynamic-quant-6bit"

// tabLongRegistry is tabRegistry with the missing omlx model under tabLongID.
var tabLongRegistry = strings.ReplaceAll(tabRegistry, "Gone-4bit", strings.TrimPrefix(tabLongID, "omlx/"))

func newTabMachine(t *testing.T, content string) *tabMachine {
	t.Helper()
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("WT_REGISTRY", "")
	t.Setenv("MODELMAN_REGISTRY", "")
	path := filepath.Join(home, "local-ai", "registry.toml")
	if content != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return &tabMachine{registry: path}
}

func (tm *tabMachine) text(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(tm.registry)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// deps are the tab's ways out, pointed at this machine: the real config
// loader over the scratch registry, and a probe that finds the omlx model on
// disk and running, the other one missing, and one ollama model nobody
// registered.
func (tm *tabMachine) deps() ModelsDeps {
	return ModelsDeps{
		Load: func() (*config.Config, error) {
			cfg, err := config.Load()
			if errors.Is(err, config.ErrRegistryMissing) {
				err = nil
			}
			return cfg, err
		},
		Probe: func(cfg *config.Config) localmodels.Snapshot {
			tm.probes++
			snap := localmodels.Snapshot{Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK, "ollama": localmodels.StatusOK}}
			for _, m := range cfg.Models {
				switch m.ID {
				case "omlx/Qwen3.8-27B-Instruct-MLX-6bit":
					snap.Entries = append(snap.Entries, localmodels.Entry{ProviderID: "omlx", ModelID: m.ID, ModelName: m.ModelName, Artifact: m.ModelName,
						Registered: true, ArtifactKnown: true, Running: true, Path: tabWeights})
				case "omlx/Gone-4bit", tabLongID:
					snap.Entries = append(snap.Entries, localmodels.Entry{ProviderID: "omlx", ModelID: m.ID, ModelName: m.ModelName, Registered: true, ArtifactKnown: true})
				}
			}
			snap.Entries = append(snap.Entries, localmodels.Entry{ProviderID: "ollama", ModelID: "ollama/qwen3:8b", ModelName: "qwen3:8b", Artifact: "qwen3:8b", ArtifactKnown: true, Size: 5_200_000_000})
			return snap
		},
		SeedEnv:      func() config.SeedEnv { return config.SeedEnv{} },
		Capabilities: func(*config.Config, string) (map[string]any, error) { return nil, errors.New("not stubbed") },
	}
}

// send delivers msg and then runs every command it leads to, the way the
// Bubble Tea runtime would, until none is left. A quit ends the walk.
func send(t *testing.T, m *model, msg tea.Msg) *model {
	t.Helper()
	next, cmd := m.Update(msg)
	m = next.(*model)
	for cmd != nil {
		out := cmd()
		cmd = nil
		switch out := out.(type) {
		case nil, tea.QuitMsg:
		case tea.BatchMsg:
			for _, c := range out {
				if c != nil {
					m = send(t, m, c())
				}
			}
		default:
			next, cmd = m.Update(out)
			m = next.(*model)
		}
	}
	return m
}

func keys(t *testing.T, m *model, ks ...string) *model {
	t.Helper()
	for _, k := range ks {
		m = send(t, m, keyMsg(k))
	}
	return m
}

// keyMsg is the key message for a key's name, or for one typed character.
func keyMsg(k string) tea.KeyMsg {
	switch k {
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	case "ctrl+s":
		return tea.KeyMsg{Type: tea.KeyCtrlS}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
}

// modelsEditor is the editor in a terminal of the given size, opened on the
// Models tab with its first probe delivered.
func modelsEditor(t *testing.T, tm *tabMachine, width, height int) *model {
	t.Helper()
	cfg, _ := tm.deps().Load()
	m := newModel(testTheme(), cfg, nil)
	m.opts = Options{StartTab: TabModels, Models: tm.deps()}
	m.tab = TabModels
	m = send(t, m, tea.WindowSizeMsg{Width: width, Height: height})
	next, _ := m.Update(loadedMsg{cfg: cfg})
	m = next.(*model)
	return send(t, m, m.probeCmd()())
}

func selectModel(t *testing.T, m *model, id string) *model {
	t.Helper()
	for i, it := range m.models.list.VisibleItems() {
		if it.(modelRow).row.ID == id {
			m.models.list.Select(i)
			// Through Update, so the frame is fitted to the new selection.
			return send(t, m, tea.WindowSizeMsg{Width: m.width, Height: m.height})
		}
	}
	t.Fatalf("no row %q on the Models tab", id)
	return m
}

// assertFits fails when view is taller or wider than the terminal. Bubble Tea
// drops a too-tall view's top lines (the tab bar and the status) and cuts a
// too-wide line at the edge.
func assertFits(t *testing.T, name, view string, width, height int) {
	t.Helper()
	if h := lipgloss.Height(view); h > height {
		t.Errorf("%s at %dx%d: the view is %d lines tall:\n%s", name, width, height, h, view)
	}
	for _, line := range strings.Split(view, "\n") {
		if w := lipgloss.Width(line); w > width {
			t.Errorf("%s at %dx%d: a line is %d columns wide: %q", name, width, height, w, line)
		}
	}
}

// flat is a view with its line breaks and padding taken out, to look for text
// the view wraps at the terminal's width.
func flat(view string) string { return strings.Join(strings.Fields(view), "") }

// stillCursors stops the cursors of the two lists' filter inputs blinking. A
// blink is a command that sleeps for half a second, and send runs every
// command a key leads to; call it before typing a filter.
func stillCursors(m *model) *model {
	m.list.FilterInput.Cursor.SetMode(cursor.CursorStatic)
	m.models.list.FilterInput.Cursor.SetMode(cursor.CursorStatic)
	return m
}

// visibleIDs are the ids of the table's rows as filtered, in order.
func visibleIDs(m *model) []string {
	var ids []string
	for _, it := range m.models.list.VisibleItems() {
		ids = append(ids, it.(modelRow).row.ID)
	}
	return ids
}

// TestTabKeySwitchesTabsAndProbesOnce verifies tab moves between the Agents
// and Models tabs, that the Agents tab's help names the key, that the first
// visit to Models probes the providers in a command, that tab goes back while
// that probe is still out — a hung provider must not hold the user on the
// tab — and that coming back does not probe again (r does). A probe on every
// tab press would dial every local server each time.
func TestTabKeySwitchesTabsAndProbesOnce(t *testing.T) {
	tm := newTabMachine(t, tabRegistry)
	cfg, _ := tm.deps().Load()
	m := newModel(testTheme(), cfg, nil)
	m.opts = Options{Models: tm.deps()}
	m = send(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = send(t, m, loadedMsg{cfg: cfg})
	if !strings.HasPrefix(m.View(), "[Agents]   Models") || tm.probes != 0 {
		t.Fatalf("the editor should open on the Agents tab without probing; probes = %d, view:\n%s", tm.probes, m.View())
	}
	if !strings.Contains(m.View(), "tab models") || !strings.Contains(m.View(), "q quit") {
		t.Errorf("the Agents tab's help should name tab and q:\n%s", m.View())
	}
	// The key alone: the probe is a command, not something Update waits for.
	next, probe := m.Update(keyMsg("tab"))
	m = next.(*model)
	if m.tab != TabModels || probe == nil || tm.probes != 0 || !strings.Contains(m.View(), "Probing providers...") {
		t.Fatalf("tab should switch at once and hand the probe to a command; tab = %d, probes = %d, view:\n%s", m.tab, tm.probes, m.View())
	}
	// Back and forth before the probe has answered: no waiting, no second probe.
	next, cmd := m.Update(keyMsg("tab"))
	if m = next.(*model); m.tab != TabAgents || cmd != nil {
		t.Fatalf("tab while the first probe is out should go back to Agents; tab = %d", m.tab)
	}
	next, cmd = m.Update(keyMsg("tab"))
	if m = next.(*model); m.tab != TabModels || cmd != nil {
		t.Fatalf("back on Models with the probe still out: tab = %d, a second probe = %v", m.tab, cmd != nil)
	}
	m = send(t, m, probe())
	if tm.probes != 1 || !strings.Contains(m.View(), " Agents   [Models]") || !strings.Contains(m.View(), "omlx/Gone-4bit") {
		t.Fatalf("after the probe: probes = %d, view:\n%s", tm.probes, m.View())
	}
	m = keys(t, m, "tab")
	if m.tab != TabAgents {
		t.Fatal("tab on the Models tab should go back to Agents")
	}
	m = keys(t, m, "tab")
	if tm.probes != 1 {
		t.Errorf("probes = %d after returning to the Models tab, want still 1", tm.probes)
	}
	m = keys(t, m, "r")
	if tm.probes != 2 {
		t.Errorf("probes = %d after r, want 2", tm.probes)
	}
}

// TestModelsTabFitsTheTerminal measures the Models tab at every size wt
// supports, in each state that changes its height or its width: plain, with a
// 72-column id in the table, with that row selected, right after a removal
// (a status that wraps, and the routes-pending note), one key later, and the
// remove prompt. The view must stay inside the terminal with the tab bar and
// the table's header row on screen; STATUS and RUNNING are on every row from
// 80 columns up however long an id is; and the selected row's id is on screen
// whole, with its status, even where the table had to cut it.
func TestModelsTabFitsTheTerminal(t *testing.T) {
	const removed = "removed omlx/Qwen3.8-27B-Instruct-MLX-6bit; weights are still at\n" + tabWeightsShown
	states := []struct {
		name     string
		registry string
		set      func(t *testing.T, m *model) *model
	}{
		{"list", tabRegistry, func(t *testing.T, m *model) *model { return m }},
		{"list with a long id", tabLongRegistry, func(t *testing.T, m *model) *model { return m }},
		{"long id selected", tabLongRegistry, func(t *testing.T, m *model) *model { return selectModel(t, m, tabLongID) }},
		{"local model selected", tabRegistry, func(t *testing.T, m *model) *model {
			return selectModel(t, m, "omlx/Qwen3.8-27B-Instruct-MLX-6bit")
		}},
		{"just after a removal", tabRegistry, func(t *testing.T, m *model) *model {
			m.models.status, m.registryChanged = removed, true
			return send(t, m, tea.WindowSizeMsg{Width: m.width, Height: m.height})
		}},
		{"one key after a removal", tabRegistry, func(t *testing.T, m *model) *model {
			m.models.status, m.registryChanged = removed, true
			return keys(t, m, "down")
		}},
		{"remove prompt", tabRegistry, func(t *testing.T, m *model) *model {
			return keys(t, selectModel(t, m, "omlx/Qwen3.8-27B-Instruct-MLX-6bit"), "d")
		}},
	}
	for _, width := range []int{40, 80, 120} {
		for _, height := range []int{12, 24, 50} {
			for _, st := range states {
				tm := newTabMachine(t, st.registry)
				m := st.set(t, modelsEditor(t, tm, width, height))
				view := m.View()
				assertFits(t, st.name, view, width, height)
				at := func(format string, args ...any) {
					t.Helper()
					t.Errorf("%s at %dx%d: %s:\n%s", st.name, width, height, fmt.Sprintf(format, args...), view)
				}
				if !strings.Contains(view, "[Models]") {
					at("the tab bar is not on screen")
				}
				if st.name == "remove prompt" {
					// The prompt wraps at the terminal's width: compare with
					// the line breaks and the padding taken out.
					if !strings.Contains(flat(view), "Removeomlx/Qwen3.8-27B-Instruct-MLX-6bitfromtheregistry?[y/N]") || !strings.Contains(flat(view), tabWeightsShown) {
						at("the prompt or the weights path is not on screen")
					}
					continue
				}
				// The header row is the table's first line: it is what a
				// too-tall view loses. MODEL is always there; STATUS and
				// RUNNING are never dropped, and from 80 columns up there is
				// room for both beside any id.
				for _, head := range []string{"MODEL", "STATUS", "RUNNING"} {
					if !strings.Contains(view, head) && (head == "MODEL" || width >= 80) {
						at("header %s is not on screen", head)
					}
				}
				if st.name == "just after a removal" {
					// The status has the room: the path it ends with matters
					// more than the detail of whichever row is selected.
					if !strings.Contains(flat(view), flat(removed)) || !strings.Contains(view, routesPending) {
						at("the removal's status or the pending note is not whole")
					}
					continue
				}
				// The detail block, under the table: the id, whole, then the
				// status (the separator between them is dropped where the
				// line breaks).
				sel, _ := m.selectedModel()
				text := flat(view)
				i := strings.LastIndex(text, sel.ID)
				if i < 0 || !strings.HasPrefix(strings.TrimPrefix(text[i+len(sel.ID):], "·"), string(sel.Status)) {
					at("the selected row's id and status (%s, %s) are not whole on screen", sel.ID, sel.Status)
				}
				if st.name == "one key after a removal" {
					// The removal's status went with the key, and its lines
					// are the table's again.
					if strings.Contains(view, "removed omlx/") || !strings.Contains(view, routesPending) || !strings.Contains(view, "q quit") {
						at("want the pending note and the hints, and the status gone")
					}
					if rows := strings.Count(view, "ok ") + strings.Count(view, "missing ") + strings.Count(view, "new "); rows < 3 {
						at("%d table rows are on screen, want at least 3", rows)
					}
				}
				if st.name == "local model selected" && width >= 80 && !strings.Contains(view, tabWeightsShown) {
					at("the selected local model's path is not whole on one line")
				}
			}
		}
	}
}

// TestModelsTabKeepsStatusAndRunning verifies what a narrow terminal loses,
// and in which order: SIZE, LOC and FAMILY, never MODEL, STATUS or RUNNING;
// and that an id too long for what is left is cut in its middle — keeping the
// provider and the end of the name, where two variants of a model differ —
// instead of pushing STATUS and RUNNING off every row of the table.
func TestModelsTabKeepsStatusAndRunning(t *testing.T) {
	tm := newTabMachine(t, tabRegistry)
	wide := modelsEditor(t, tm, 120, 24).View()
	if !strings.Contains(wide, "FAMILY   MODEL                                             LOC    STATUS   RUNNING  SIZE") {
		t.Errorf("at 120 columns every column should show:\n%s", wide)
	}
	// FAMILY, a 48-column MODEL, STATUS and RUNNING are 75 columns with their
	// gaps; LOC would make it 82.
	at80 := modelsEditor(t, tm, 80, 24).View()
	if !strings.Contains(at80, "sonnet   openrouter/anthropic--claude-sonnet-4.5-thinking  ok") || strings.Contains(at80, "SIZE") || strings.Contains(at80, "LOC") {
		t.Errorf("at 80 columns SIZE and LOC go and the longest id stays whole:\n%s", at80)
	}
	narrow := modelsEditor(t, tm, 40, 24).View()
	for _, want := range []string{"MODEL                STATUS   RUNNING", "omlx/Qwen3.8…X-6bit  ok       run", "omlx/Gone-4bit       missing"} {
		if !strings.Contains(narrow, want) {
			t.Errorf("at 40 columns want %q — FAMILY and LOC gone, a long id cut in the middle, a short one whole:\n%s", want, narrow)
		}
	}

	// A 72-column id at 80 columns: FAMILY goes too, and the id gives up its
	// middle so that STATUS and RUNNING stay.
	long := modelsEditor(t, newTabMachine(t, tabLongRegistry), 80, 24).View()
	for _, want := range []string{
		"MODEL                                                        STATUS   RUNNING",
		"omlx/Qwen3.8-35B-A3B-Instruct-abliterat…-dynamic-quant-6bit  missing",
		"omlx/Qwen3.8-27B-Instruct-MLX-6bit                           ok       run",
	} {
		if !strings.Contains(long, want) {
			t.Errorf("with a 72-column id at 80 columns want %q:\n%s", want, long)
		}
	}
	for _, c := range []struct {
		s    string
		w    int
		want string
	}{{"omlx/abcdefghij", 15, "omlx/abcdefghij"}, {"omlx/abcdefghij", 12, "omlx/abc…hij"}, {"abcdef", 2, "ab"}} {
		if got := middleCut(c.s, c.w); got != c.want {
			t.Errorf("middleCut(%q, %d) = %q, want %q", c.s, c.w, got, c.want)
		}
	}
}

// TestModelsTabRemove verifies the remove flow: d shows a y/N prompt with the
// weights path, n leaves the registry alone, y deletes exactly that row at
// once, repeats the path in the status, marks the routes pending and
// re-probes. wt deletes no weights, so the path has to be in front of the
// user before and after — on a line of its own, written from ~.
func TestModelsTabRemove(t *testing.T) {
	tm := newTabMachine(t, tabRegistry)
	m := selectModel(t, modelsEditor(t, tm, 80, 24), "omlx/Qwen3.8-27B-Instruct-MLX-6bit")

	m = keys(t, m, "d")
	view := m.View()
	for _, want := range []string{"Remove omlx/Qwen3.8-27B-Instruct-MLX-6bit from the registry? [y/N]", "weights are still at", tabWeightsShown, "never deletes weights"} {
		if !strings.Contains(view, want) {
			t.Fatalf("the prompt should say %q:\n%s", want, view)
		}
	}
	m = keys(t, m, "n")
	if m.models.phase != modelsList || tm.text(t) != tabRegistry || m.registryChanged {
		t.Fatal("n must cancel the removal and write nothing")
	}

	probes := tm.probes
	m = keys(t, m, "d", "y")
	if got := tm.text(t); strings.Contains(got, "Qwen3.8-27B-Instruct-MLX-6bit") || !strings.Contains(got, `id = "omlx/Gone-4bit"`) {
		t.Errorf("y should remove exactly that row:\n%s", got)
	}
	if !m.registryChanged || tm.probes != probes+1 {
		t.Errorf("registryChanged = %v, probes = %d (was %d); want the change recorded and one re-probe", m.registryChanged, tm.probes, probes)
	}
	// The path is on a line of its own, so that it can be copied whole.
	for _, want := range []string{"removed omlx/Qwen3.8-27B-Instruct-MLX-6bit; weights are still at\n" + tabWeightsShown + "\n", routesPending} {
		if !strings.Contains(m.View(), want) {
			t.Errorf("after the removal the screen should say %q:\n%s", want, m.View())
		}
	}
	if _, still := findRow(m, "omlx/Qwen3.8-27B-Instruct-MLX-6bit"); still {
		t.Error("the removed row is still listed")
	}
}

func findRow(m *model, id string) (modelRow, bool) {
	for _, it := range m.models.list.Items() {
		if r := it.(modelRow); r.row.ID == id {
			return r, true
		}
	}
	return modelRow{}, false
}

// TestModelsTabRemoveRefusals verifies d on a discovered row (nothing to
// remove) only says so, and that a removal the registry refuses is reported
// on the status line with the routes not marked pending.
func TestModelsTabRemoveRefusals(t *testing.T) {
	tm := newTabMachine(t, tabRegistry)
	m := keys(t, selectModel(t, modelsEditor(t, tm, 80, 24), "ollama/qwen3:8b"), "d")
	if m.models.phase != modelsList || !strings.Contains(m.View(), "ollama/qwen3:8b is not in the registry") {
		t.Errorf("d on a discovered row should only set the status:\n%s", m.View())
	}

	// Another program removes the row while the prompt is open.
	m = keys(t, selectModel(t, m, "omlx/Gone-4bit"), "d")
	if err := os.WriteFile(tm.registry, []byte(strings.Replace(tabRegistry, `id = "omlx/Gone-4bit"`, `id = "omlx/Other"`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	m = keys(t, m, "y")
	if m.registryChanged || !strings.Contains(m.View(), `not removed: model not found: "omlx/Gone-4bit"`) {
		t.Errorf("a refused removal should be reported and leave the routes alone:\n%s", m.View())
	}
}

// TestModelsTabDropsAStaleProbe verifies the rows of an older probe do not
// replace a newer one's. A slow first probe that lands after a refresh would
// otherwise put back a row the user just removed.
func TestModelsTabDropsAStaleProbe(t *testing.T) {
	tm := newTabMachine(t, tabRegistry)
	m := modelsEditor(t, tm, 80, 24)
	stale := m.probeCmd()
	fresh := m.probeCmd()
	m = send(t, m, fresh())
	rows := len(m.models.rows)
	old := stale().(modelsLoadedMsg)
	old.rows = nil
	m = send(t, m, old)
	if len(m.models.rows) != rows {
		t.Errorf("a stale probe replaced the rows: %d rows, want %d", len(m.models.rows), rows)
	}
}

// TestModelsTabKeepsTheCursorAcrossARefresh verifies r rebuilds the table
// with the cursor on the row it was on. A refresh that jumped to the top
// would make the next d or enter act on a different model.
func TestModelsTabKeepsTheCursorAcrossARefresh(t *testing.T) {
	tm := newTabMachine(t, tabRegistry)
	m := selectModel(t, modelsEditor(t, tm, 80, 24), "omlx/Gone-4bit")
	m = keys(t, m, "r")
	if r, _ := m.selectedModel(); r.ID != "omlx/Gone-4bit" {
		t.Errorf("after r the cursor is on %q, want omlx/Gone-4bit", r.ID)
	}
}

// TestModelsTabFilterTakesTheTabsKeys verifies that while the filter is being
// typed, d, r, n and q are text, not commands: typing "qwen" must not quit,
// and "d" must not open the remove prompt.
func TestModelsTabFilterTakesTheTabsKeys(t *testing.T) {
	tm := newTabMachine(t, tabRegistry)
	m := stillCursors(modelsEditor(t, tm, 80, 24))
	probes := tm.probes
	m = keys(t, m, "/", "q", "d", "r")
	if m.models.phase != modelsList || tm.probes != probes || m.models.list.FilterValue() != "qdr" {
		t.Errorf("phase = %d, probes = %d (was %d), filter = %q; want the keys typed into the filter", m.models.phase, tm.probes, probes, m.models.list.FilterValue())
	}
}

// TestModelsTabFilterNarrowsTheTable verifies / filters the rows: the table
// shows only the matches, enter keeps them, d and the detail then act on the
// row the user sees, and esc clears the filter without leaving the editor.
// bubbles ranks the rows in a command and sends the result back as a
// message; a tab that dropped it showed "Filter: qwen" above every row, and d
// removed a model the user was not looking at.
func TestModelsTabFilterNarrowsTheTable(t *testing.T) {
	tm := newTabMachine(t, tabRegistry)
	m := stillCursors(modelsEditor(t, tm, 80, 24))
	all := len(visibleIDs(m))
	m = keys(t, m, "/", "q", "w", "e", "n")
	// The matches, best first: the two rows of the qwen3.8 family and the
	// unregistered qwen3:8b.
	shown := visibleIDs(m)
	want := []string{"ollama/qwen3:8b", "omlx/Gone-4bit", "omlx/Qwen3.8-27B-Instruct-MLX-6bit"}
	if got := slices.Sorted(slices.Values(shown)); !slices.Equal(got, want) {
		t.Fatalf("while typing /qwen the table shows %v, want %v", shown, want)
	}
	if view := m.View(); strings.Contains(view, "claude-sonnet") {
		t.Errorf("a row that does not match is still drawn:\n%s", view)
	}
	m = keys(t, m, "enter", "down", "d")
	if m.models.phase != modelsRemove || m.models.remove.ID != shown[1] {
		t.Errorf("d on the second filtered row asks about %q, want %q, the row under the cursor", m.models.remove.ID, shown[1])
	}
	m = keys(t, m, "n")
	// esc clears the filter; it is not the list's quit key here.
	next, cmd := m.Update(keyMsg("esc"))
	m = next.(*model)
	if cmd != nil || m.models.list.FilterState() != list.Unfiltered || len(visibleIDs(m)) != all {
		t.Errorf("esc on a filtered table should clear the filter and nothing else; rows = %d of %d", len(visibleIDs(m)), all)
	}

	// A result for the Agents tab's list, or for a table that has been
	// rebuilt since it was ranked, is not this table's.
	before := visibleIDs(m)
	m = send(t, m, filterMatchesMsg{tab: TabModels, built: m.models.built - 1})
	if got := visibleIDs(m); !slices.Equal(got, before) {
		t.Errorf("a filter result for an older table was applied: %v", got)
	}
}

// TestAgentsFilterNarrowsTheList verifies the same on the Agents tab, whose
// list lost its filter results the same way before the tabs: "/cla" showed
// every agent.
func TestAgentsFilterNarrowsTheList(t *testing.T) {
	cfg := &config.Config{Agents: []config.Agent{{Name: "claude"}, {Name: "pi"}}}
	m := newModel(testTheme(), cfg, nil)
	m = send(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = stillCursors(send(t, m, loadedMsg{cfg: cfg}))
	all := len(m.list.VisibleItems())
	m = keys(t, m, "/", "c", "l", "a", "u")
	got := m.list.VisibleItems()
	if len(got) != 1 || got[0].(agentItem).agent.Name != "claude" || all < 2 {
		t.Errorf("/clau shows %d of %d agents, want claude alone", len(got), all)
	}
}

// TestEscOnAListDoesNotQuit verifies esc on either tab's list does not end
// the editor. esc is the list's own quit key, and that quit skipped the
// unsaved-changes prompt: esc pressed once too often — it cancels the remove
// prompt — threw away every unsaved agent edit. q still asks.
func TestEscOnAListDoesNotQuit(t *testing.T) {
	tm := newTabMachine(t, tabRegistry)
	m := modelsEditor(t, tm, 80, 24)
	m.dirty = true
	m = keys(t, selectModel(t, m, "omlx/Gone-4bit"), "d", "esc")
	for _, tab := range []Tab{TabModels, TabAgents} {
		m.tab = tab
		next, cmd := m.Update(keyMsg("esc"))
		m = next.(*model)
		if cmd != nil || m.phase != phaseList {
			t.Fatalf("esc on the list of tab %d: a command = %v, phase = %d; want nothing to happen", tab, cmd != nil, m.phase)
		}
	}
	next, cmd := m.Update(keyMsg("q"))
	if cmd != nil || next.(*model).phase != phaseQuit {
		t.Error("q with unsaved agent edits should still reach the prompt")
	}
}

// TestCtrlSOnTheModelsTabDoesNothing verifies ctrl+s is the Agents tab's key
// only. On the Models tab there is nothing to save — each change is written
// when it is made — and a save of the agents from there would report its
// outcome on a tab the user is not looking at.
func TestCtrlSOnTheModelsTabDoesNothing(t *testing.T) {
	tm := newTabMachine(t, tabRegistry)
	m := modelsEditor(t, tm, 80, 24)
	m.dirty = true
	saves := 0
	old := saveCmd
	saveCmd = func(*config.Config) tea.Cmd { saves++; return nil }
	t.Cleanup(func() { saveCmd = old })
	next, cmd := m.Update(keyMsg("ctrl+s"))
	m = next.(*model)
	if cmd != nil || saves != 0 || !m.dirty || m.saving {
		t.Errorf("ctrl+s on the Models tab: saves = %d, dirty = %v; want the agents left unsaved for their own tab", saves, m.dirty)
	}
	m = keys(t, m, "tab")
	if _, cmd = m.Update(keyMsg("ctrl+s")); saves != 1 {
		t.Errorf("ctrl+s on the Agents tab: saves = %d, want 1", saves)
	}
}

// TestModelsTabStatusClearsOnTheNextKey verifies what the last action said
// stays until the user presses a key on the table and then goes, while the
// routes-pending note and a registry that does not load stay. A status that
// never cleared kept its lines from the table for the rest of the session.
func TestModelsTabStatusClearsOnTheNextKey(t *testing.T) {
	tm := newTabMachine(t, tabRegistry)
	m := keys(t, selectModel(t, modelsEditor(t, tm, 80, 24), "omlx/Gone-4bit"), "d", "y")
	if !strings.Contains(m.View(), "removed omlx/Gone-4bit; no weights were found on disk") {
		t.Fatalf("the removal's status should be on screen:\n%s", m.View())
	}
	m = keys(t, m, "down")
	if view := m.View(); strings.Contains(view, "removed omlx/Gone-4bit") || !strings.Contains(view, routesPending) {
		t.Errorf("after a key the status should be gone and the pending note still there:\n%s", view)
	}

	broken := modelsEditor(t, newTabMachine(t, "this is not = = toml\n"), 80, 24)
	broken = keys(t, broken, "down")
	if !strings.Contains(broken.View(), "registry: ") {
		t.Errorf("why the registry did not load must outlast a key:\n%s", broken.View())
	}
}

// TestQuitOnTheModelsTab verifies q leaves at once when only the registry
// changed — every change there is already saved — and that unsaved agent
// edits still get their prompt from the Models tab.
func TestQuitOnTheModelsTab(t *testing.T) {
	tm := newTabMachine(t, tabRegistry)
	m := modelsEditor(t, tm, 80, 24)
	m.registryChanged = true
	if _, cmd := m.Update(keyMsg("q")); cmd == nil {
		t.Fatal("q with only registry changes should quit: there is nothing unsaved")
	}
	m.dirty = true
	next, cmd := m.Update(keyMsg("q"))
	if cmd != nil || next.(*model).phase != phaseQuit || !strings.Contains(next.(*model).View(), "unsaved agent changes") {
		t.Errorf("q with unsaved agent edits should ask about them:\n%s", next.(*model).View())
	}
}

// TestQuitWaitsForARegistryWriteInFlight verifies a quit typed while a
// removal is still being written — d, y, q in one breath — waits for the
// write's result and only then ends the program, with the change recorded.
// The write runs in a command; a program that ended before its message was
// handled reported "nothing changed" for a registry that did change, and wt
// then skipped the route sync: the removed model kept its LiteLLM route.
func TestQuitWaitsForARegistryWriteInFlight(t *testing.T) {
	isQuit := func(cmd tea.Cmd) bool {
		if cmd == nil {
			return false
		}
		_, ok := cmd().(tea.QuitMsg)
		return ok
	}
	// removing presses d and y on a row and hands back the write, not run.
	removing := func(t *testing.T, dirty bool) (*tabMachine, *model, tea.Cmd) {
		tm := newTabMachine(t, tabRegistry)
		m := keys(t, selectModel(t, modelsEditor(t, tm, 80, 24), "omlx/Gone-4bit"), "d")
		m.dirty = dirty
		next, write := m.Update(keyMsg("y"))
		if write == nil {
			t.Fatal("y should hand the removal to a command")
		}
		return tm, next.(*model), write
	}

	t.Run("q", func(t *testing.T) {
		tm, m, write := removing(t, false)
		next, cmd := m.Update(keyMsg("q"))
		m = next.(*model)
		if cmd != nil || !m.quitPending || m.registryChanged {
			t.Fatalf("q with the write in flight: a command = %v, quitPending = %v; want the quit held back", cmd != nil, m.quitPending)
		}
		if !strings.Contains(m.View(), "quitting when the registry write is done") {
			t.Errorf("the tab should say why it has not left yet:\n%s", m.View())
		}
		next, cmd = m.Update(write())
		m = next.(*model)
		if !isQuit(cmd) || !m.registryChanged || strings.Contains(tm.text(t), "Gone-4bit") {
			t.Errorf("once the write is in: quit = %v, registryChanged = %v; want both, and the row gone", isQuit(cmd), m.registryChanged)
		}
	})
	t.Run("discarding agent edits from the quit prompt", func(t *testing.T) {
		_, m, write := removing(t, true)
		next, _ := m.Update(keyMsg("q"))
		if m = next.(*model); m.phase != phaseQuit {
			t.Fatalf("phase = %d, want the unsaved-changes prompt first", m.phase)
		}
		next, cmd := m.Update(keyMsg("n"))
		if m = next.(*model); cmd != nil || !m.quitPending {
			t.Fatal("n on the prompt with the write in flight should hold the quit back too")
		}
		if _, cmd = m.Update(write()); !isQuit(cmd) {
			t.Error("the held quit should happen when the write is in")
		}
	})
	t.Run("a refused write calls the quit off", func(t *testing.T) {
		tm, m, write := removing(t, false)
		// Another program takes the row first.
		if err := os.WriteFile(tm.registry, []byte(strings.Replace(tabRegistry, `id = "omlx/Gone-4bit"`, `id = "omlx/Other"`, 1)), 0o600); err != nil {
			t.Fatal(err)
		}
		next, _ := m.Update(keyMsg("q"))
		next, cmd := next.(*model).Update(write())
		m = next.(*model)
		if cmd != nil || m.quitPending || m.registryChanged || !strings.Contains(m.View(), "not removed: ") {
			t.Errorf("a refusal has to be read: want no quit and the reason on screen:\n%s", m.View())
		}
	})
	t.Run("a probe in flight holds nothing up", func(t *testing.T) {
		tm := newTabMachine(t, tabRegistry)
		m := modelsEditor(t, tm, 80, 24)
		next, probe := m.Update(keyMsg("r"))
		if probe == nil {
			t.Fatal("r should hand the probe to a command")
		}
		if _, cmd := next.(*model).Update(keyMsg("q")); !isQuit(cmd) {
			t.Error("q while only a probe is out should quit at once")
		}
	})
}

// TestModelsTabShowsARegistryThatDoesNotLoad verifies a registry wt cannot
// read still opens the tab, with the reason on the status line, instead of a
// blank screen or a crash.
func TestModelsTabShowsARegistryThatDoesNotLoad(t *testing.T) {
	tm := newTabMachine(t, "this is not = = toml\n")
	m := modelsEditor(t, tm, 80, 24)
	view := m.View()
	assertFits(t, "broken registry", view, 80, 24)
	if !strings.Contains(view, "registry: ") || !strings.Contains(view, "[Models]") {
		t.Errorf("the tab should say why the registry did not load:\n%s", view)
	}
}
```

Two existing tests meet the changes: `Run` gains an argument and a result, and the Agents tab's first line is the tab bar.

```diff
--- a/wt/internal/configeditor/editor_test.go
+++ b/wt/internal/configeditor/editor_test.go
@@ -181,10 +181,11 @@ func selectAgentItem(m *model, name string) {
 // skeleton; without it, a missing Init or zero-value model could deadlock
 // or crash on startup.
 func TestRun_EmptyConfig_Launches(t *testing.T) {
-	err := Run(
+	_, err := Run(
 		testTheme(),
 		&config.Config{},
 		nil,
+		Options{},
 		tea.WithInput(strings.NewReader("q")),
 		tea.WithoutRenderer(),
 	)
@@ -271,7 +272,7 @@ func TestStatusWrapsInsteadOfBeingCutOff(t *testing.T) {
 				break
 			}
 		}
-		if !strings.HasPrefix(view, "Agents (providers/models") {
+		if !strings.HasPrefix(view, "[Agents]") {
 			t.Errorf("%dx%d: the title is not the first line:\n%s", size.w, size.h, view)
 		}
 	}
```

- [ ] **Step 3: Run them to see them fail**

Run, from `wt/`: `go test -count=1 ./internal/configeditor`

Expected: the package does not build.

```text
FAIL	github.com/ohanaverse/local-ai-setup/wt/internal/configeditor [build failed]
FAIL
# github.com/ohanaverse/local-ai-setup/wt/internal/configeditor [github.com/ohanaverse/local-ai-setup/wt/internal/configeditor.test]
internal/configeditor/models_tab_test.go:123:30: undefined: ModelsDeps
internal/configeditor/models_tab_test.go:124:9: undefined: ModelsDeps
internal/configeditor/models_tab_test.go:218:4: m.opts undefined (type *model has no field or method opts)
internal/configeditor/models_tab_test.go:218:11: undefined: Options
internal/configeditor/models_tab_test.go:218:29: undefined: TabModels
internal/configeditor/models_tab_test.go:219:4: m.tab undefined (type *model has no field or method tab)
internal/configeditor/models_tab_test.go:219:10: undefined: TabModels
internal/configeditor/models_tab_test.go:514:36: undefined: modelRow
```

- [ ] **Step 4: Write the tabs, the options and the tab bar**

Create `wt/internal/configeditor/tabs.go`:

```go
package configeditor

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modeladmin"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// Tab is one of the editor's tabs.
type Tab int

const (
	// TabAgents edits the agents in wt's config.toml. Its edits are buffered
	// and saved with ctrl+s.
	TabAgents Tab = iota
	// TabModels manages the models in the shared registry.toml. Each change
	// there is written at once.
	TabModels
)

// Options are what `wt config` is opened with.
type Options struct {
	// StartTab is the tab shown first: TabModels for bare `wt model`.
	StartTab Tab
	// Models are the functions the Models tab reaches the machine through.
	// Run fills in the real ones for any left nil.
	Models ModelsDeps
}

// ModelsDeps are the Models tab's ways out of the program: reading the
// config, probing the providers, and what an add needs. Each is called from a
// tea.Cmd, never from Update, so a slow provider cannot freeze the screen.
// They are fields rather than package variables so that a test hands the tab
// a machine of its own.
type ModelsDeps struct {
	// Load re-reads config.toml and the registry (config.Load).
	Load func() (*config.Config, error)
	// Probe is one inventory round (localmodels.Inventory).
	Probe func(*config.Config) localmodels.Snapshot
	// SeedEnv describes the machine an add seeds provider rows for
	// (config.DefaultSeedEnv).
	SeedEnv func() config.SeedEnv
	// Capabilities asks ollama what a model can do
	// (modeladmin.OllamaCapabilities).
	Capabilities func(cfg *config.Config, name string) (map[string]any, error)
}

func (d ModelsDeps) withDefaults() ModelsDeps {
	if d.Load == nil {
		d.Load = config.Load
	}
	if d.Probe == nil {
		d.Probe = localmodels.Inventory
	}
	if d.SeedEnv == nil {
		d.SeedEnv = config.DefaultSeedEnv
	}
	if d.Capabilities == nil {
		d.Capabilities = modeladmin.OllamaCapabilities
	}
	return d
}

// Result is what the editor did that its caller has to act on.
type Result struct {
	// RegistryChanged is true when the Models tab wrote registry.toml. The
	// tab does not sync the LiteLLM routes itself — several changes in a row
	// would restart the proxy once each — so the caller runs one sync now.
	RegistryChanged bool
}

// tabNames are the tabs in the order they are drawn and switched.
var tabNames = []string{"Agents", "Models"}

// tabBar is the editor's first line: every tab, the current one in brackets
// (and in the accent colour, where there is colour) so that it reads as
// current on a terminal without any.
func tabBar(theme themes.Theme, current Tab) string {
	active := lipgloss.NewStyle().Foreground(theme.Token(themes.TokenAccent)).Bold(true)
	dim := lipgloss.NewStyle().Foreground(theme.Token(themes.TokenDim))
	bar := ""
	for i, name := range tabNames {
		if i > 0 {
			bar += "  "
		}
		if Tab(i) == current {
			bar += active.Render("[" + name + "]")
		} else {
			bar += dim.Render(" " + name + " ")
		}
	}
	return bar
}
```

- [ ] **Step 5: Write the Models tab**

Create `wt/internal/configeditor/models_tab.go`. Five things to hold on to while reading it.

- Nothing in `updateModels` reads a file or dials a server: the probe (`probeCmd`) and the removal are commands.
- The view is drawn through `modelsFrames`, the same frames `fitModels` measures, so the table can never push the tab bar or the status off the top.
- Text that can be longer than a line is wrapped by `flow`, which breaks a line between words and nowhere else: lipgloss's own wrapping also breaks at a hyphen, which is inside every model path.
- A removal sets `writing`, and `leave` (Step 6) holds a quit back while it is set; `applyModelRemoved` issues the quit once `registryChanged` is recorded.
- A command a list returns goes back to the runtime through `tagFilter`, so that the list's filter result comes back naming its list (`filterMatchesMsg`). Without it `/` filters nothing.

```go
package configeditor

import (
	"os"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modeladmin"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tui"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tuilayout"
)

// The Models tab's columns, left to right.
const (
	mcFamily = iota
	mcModel
	mcLoc
	mcStatus
	mcRunning
	mcSize
)

// modelsDropOrder is what a narrow terminal gives up, first to go first.
// MODEL, STATUS and RUNNING say what a row is and are never dropped; the
// detail block under the table still shows the rest for the selected row.
var modelsDropOrder = []int{mcSize, mcLoc, mcFamily}

// modelsMinID is the narrowest the MODEL column is ever cut to: room for a
// provider, an ellipsis and the end of the name.
const modelsMinID = 12

// modelsTableMin is the height the tab's fuller layouts leave the table: its
// header and the blank line under it, three rows, and the two lines bubbles
// keeps for the page dots.
const modelsTableMin = 7

// modelsPhase is what the Models tab is showing.
type modelsPhase int

const (
	modelsList modelsPhase = iota
	modelsRemove
)

// modelsTab is the Models tab's state. Its rows come from one probe run in a
// command (probeCmd); nothing here reads a file or dials a server.
type modelsTab struct {
	phase modelsPhase
	list  list.Model
	// cols is the table's layout, shared by its header and its rows; idWidth
	// is the longest id, the MODEL column's width when nothing is cut.
	cols    *tuilayout.Columns
	idWidth int
	// cfg is the config the rows were built from, re-read on every probe.
	cfg  *config.Config
	rows []modeladmin.Row
	// asked is true once the first probe has been dispatched; loaded once
	// its result is in.
	asked  bool
	loaded bool
	// gen numbers the probes: a result that is not the latest's is dropped,
	// so a slow probe cannot overwrite the rows of a later one. built counts
	// the tables built, so a filter result for an older table is dropped.
	gen   int
	built int
	// busy is true while a probe or a registry write is in flight; keys that
	// would start another are ignored. writing is true for the write alone:
	// a quit waits for it (leave), so the caller always learns of the change.
	busy    bool
	writing bool
	// status is what the last action said; the next key on the table clears
	// it. loadErr is why the registry did not load, and stays until a probe
	// reads it again.
	status  string
	loadErr string
	// selectID is the row the cursor goes to when the next rows arrive.
	selectID string
	// remove is the row the remove prompt is about.
	remove modeladmin.Row
}

// modelsLoadedMsg carries one probe's rows.
type modelsLoadedMsg struct {
	gen  int
	cfg  *config.Config
	rows []modeladmin.Row
	err  error
}

// modelRemovedMsg reports a removal the remove prompt confirmed.
type modelRemovedMsg struct {
	id   string
	note string
	err  error
}

// modelRow adapts a modeladmin.Row to a table row of the tab's list.
type modelRow struct {
	row   modeladmin.Row
	cells []string
	cols  *tuilayout.Columns
}

func (r modelRow) FilterValue() string              { return r.row.ID + " " + r.row.Family }
func (r modelRow) Description() string              { return "" }
func (r modelRow) TableColumns() *tuilayout.Columns { return r.cols }

// Title is the row's line. The MODEL cell is drawn at the column's width of
// the moment (fitModels narrows it when even the columns that are never
// dropped do not fit), so nothing is rebuilt when the terminal is resized.
func (r modelRow) Title() string {
	cells := slices.Clone(r.cells)
	w := r.cols.Widths[mcModel]
	cells[mcModel] = tuilayout.PadRunes(middleCut(r.row.ID, w), w)
	return r.cols.Line(cells)
}

// middleCut shortens s to w runes by taking out its middle: an id keeps its
// provider and the end of its name, which is where two variants of one model
// differ (…-4bit, …-6bit). A string that fits is returned as it is.
func middleCut(s string, w int) string {
	runes := []rune(s)
	if len(runes) <= w {
		return s
	}
	if w < 3 {
		return string(runes[:max(w, 0)])
	}
	tail := (w - 1) / 3
	return string(runes[:w-1-tail]) + "…" + string(runes[len(runes)-tail:])
}

// flow lays units out on lines of at most width runes, sep between two units
// on a line. A line is broken only between units — never inside one, and so
// never at a hyphen inside an id or a path — except that a unit longer than a
// whole line is cut at the line's end.
func flow(units []string, sep string, width int) []string {
	width = max(width, 1)
	var lines []string
	cur := ""
	for _, u := range units {
		if cur != "" {
			if utf8.RuneCountInString(cur+sep+u) <= width {
				cur += sep + u
				continue
			}
			lines, cur = append(lines, cur), ""
		}
		runes := []rune(u)
		for len(runes) > width {
			lines, runes = append(lines, string(runes[:width])), runes[width:]
		}
		cur = string(runes)
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

// wrapText wraps free text to width at its spaces, keeping the line breaks
// it has. A path on a line of its own is therefore cut only at the
// terminal's edge and can be copied as one token.
func wrapText(s string, width int) string {
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) == "" {
			lines = append(lines, "")
			continue
		}
		lines = append(lines, flow(strings.Fields(line), " ", width)...)
	}
	return strings.Join(lines, "\n")
}

// userHome is the home directory paths are abbreviated against. A seam: the
// package's TestMain names one, so no test reads the developer's.
var userHome = os.UserHomeDir

// tildePath writes a path under the home directory with ~ for the home, the
// way the registry's own model_dir does: shorter, and still a path a shell
// takes.
func tildePath(path string) string {
	home, err := userHome()
	if err != nil || home == "" || home == "/" {
		return path
	}
	if rest, ok := strings.CutPrefix(path, home); ok && (rest == "" || strings.HasPrefix(rest, "/")) {
		return "~" + rest
	}
	return path
}

// weightsLines is modeladmin.WeightsNote for the tab: the same words, with
// the path abbreviated and on a line of its own, so that wrapping never
// breaks it in the middle. Nil for a row with nothing on this machine.
func weightsLines(r modeladmin.Row) []string {
	r.Path = tildePath(r.Path)
	note := modeladmin.WeightsNote(r)
	switch {
	case note == "":
		return nil
	case r.Path != "" && strings.HasSuffix(note, " "+r.Path):
		return []string{strings.TrimSuffix(note, " "+r.Path), r.Path}
	}
	return []string{note}
}

// probeCmd reads the config and probes the providers once, off the update
// loop, and returns the rows.
func (m *model) probeCmd() tea.Cmd {
	m.models.gen++
	m.models.asked, m.models.busy = true, true
	gen, deps := m.models.gen, m.opts.Models
	return func() tea.Msg {
		cfg, err := deps.Load()
		if cfg == nil {
			cfg = &config.Config{}
		}
		return modelsLoadedMsg{gen: gen, cfg: cfg, err: err, rows: modeladmin.Rows(cfg, deps.Probe(cfg))}
	}
}

// buildModelsList lays rows out as a table: one shared column layout, one
// item per row, the header as the list's title. It returns the layout and
// the longest id's width with it.
func buildModelsList(theme themes.Theme, rows []modeladmin.Row) (list.Model, *tuilayout.Columns, int) {
	famW, idW, stW := len("FAMILY"), len("MODEL"), len("STATUS")
	sizes := make([]string, len(rows))
	for i, r := range rows {
		sizes[i] = modeladmin.FormatSize(r.Size)
		famW = tuilayout.MaxRunes(famW, dash(r.Family))
		idW = tuilayout.MaxRunes(idW, r.ID)
		stW = tuilayout.MaxRunes(stW, string(r.Status))
	}
	const locW, runW, sizeW = 5, 7, 8
	cols := tuilayout.NewColumns(
		[]string{
			tuilayout.PadRunes("FAMILY", famW), tuilayout.PadRunes("MODEL", idW), tuilayout.PadRunes("LOC", locW),
			tuilayout.PadRunes("STATUS", stW), tuilayout.PadRunes("RUNNING", runW), "SIZE",
		},
		[]int{famW, idW, locW, stW, runW, sizeW},
		0, modelsDropOrder,
	)
	items := make([]list.Item, len(rows))
	for i, r := range rows {
		// The MODEL cell is drawn by modelRow.Title, at the column's width.
		items[i] = modelRow{row: r, cols: cols, cells: []string{
			tuilayout.PadRunes(dash(r.Family), famW), "", tuilayout.PadRunes(dash(r.Location), locW),
			tuilayout.PadRunes(string(r.Status), stW), tuilayout.PadRunes(r.Running, runW), sizes[i],
		}}
	}
	delegate := tui.ThemedListDelegate(theme)
	delegate.ShowDescription = false
	delegate.SetSpacing(0)
	// No mark on a filter's matches: bubbles marks the runes it matched in
	// FilterValue (the id and the family), which are not where they are in
	// the row's line.
	delegate.Styles.FilterMatch = lipgloss.NewStyle()
	l := list.New(items, delegate, 0, 0)
	l.Title = cols.Header()
	l.Styles.Title = lipgloss.NewStyle().Foreground(theme.Token(themes.TokenDim))
	l.Styles.TitleBar = lipgloss.NewStyle().Padding(0, 0, 1, 0)
	l.SetShowStatusBar(false)
	// The tab prints its own key hints: the list's help line does not know
	// the tab's keys and costs a row.
	l.SetShowHelp(false)
	// q and esc are the list's own quit keys, and its quit goes round the
	// editor's (the unsaved-changes prompt, a write in flight). q is handled
	// by updateModels; esc only clears a filter.
	l.DisableQuitKeybindings()
	return l, cols, idW
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// setModelColumn gives the MODEL column a width.
func setModelColumn(cols *tuilayout.Columns, w int) {
	cols.Widths[mcModel] = w
	cols.Heads[mcModel] = tuilayout.PadRunes("MODEL", w)
}

// selectedModel is the row under the cursor.
func (m *model) selectedModel() (modeladmin.Row, bool) {
	if !m.models.loaded {
		return modeladmin.Row{}, false
	}
	it, ok := m.models.list.SelectedItem().(modelRow)
	return it.row, ok
}

// routesPending is the note the tab shows once it has written the registry.
const routesPending = "LiteLLM routes pending (sync on quit)"

// modelsStatus is the tab's status block: why the registry did not load,
// what the last action said, and a note that the LiteLLM routes are owed a
// sync once the registry has been written. It is wrapped, not cut: a
// removal's status ends with the path of the weights left behind.
func (m *model) modelsStatus() string {
	var parts []string
	for _, s := range []string{m.models.loadErr, m.models.status} {
		if s != "" {
			parts = append(parts, s)
		}
	}
	switch {
	case m.quitPending:
		parts = append(parts, "quitting when the registry write is done...")
	case m.registryChanged:
		parts = append(parts, routesPending)
	}
	return wrapText(strings.Join(parts, "\n"), m.width)
}

// modelsDetail is the block under the table, in two parts: the selected
// row's id, whole, with its status and running state (the table may have cut
// the id, and has no column for the rest) and its tags; and the path of its
// weights, on lines of its own.
func (m *model) modelsDetail() (id, path []string) {
	r, ok := m.selectedModel()
	if !ok {
		return nil, nil
	}
	parts := []string{r.ID, string(r.Status)}
	if running := map[string]string{modeladmin.RunningRun: "running", modeladmin.RunningLoad: "loading", modeladmin.RunningUnknown: "running?"}[r.Running]; running != "" {
		parts = append(parts, running)
	}
	if !strings.HasPrefix(r.ID, r.ProviderID+"/") {
		// Only when the id does not already say it (an omlx-6bit row's id
		// starts omlx/; --id can be anything).
		parts = append(parts, "provider "+r.ProviderID)
	}
	if len(r.Tags) > 0 {
		parts = append(parts, "tags "+strings.Join(r.Tags, ","))
	}
	if r.Pairing() {
		parts = append(parts, "target "+dash(r.Target), "draft "+dash(r.Draft))
	}
	id = flow(parts, " · ", m.width)
	if r.Path != "" && !r.Pairing() {
		path = flow([]string{tildePath(r.Path)}, "", m.width)
	}
	return id, path
}

// modelsHints are the tab's key hints, fullest first.
var modelsHints = []string{
	"d remove · r refresh · / filter · tab agents · q quit",
	"d · r · / · tab · q quit",
}

// fitHints is the fullest of hints that fits width; the last one, cut, when
// none does.
func fitHints(width int, hints []string) string {
	for _, h := range hints {
		if width <= 0 || utf8.RuneCountInString(h) <= width {
			return h
		}
	}
	return tuilayout.Clip(hints[len(hints)-1], width)
}

// modelsFrames are the tab's layouts, fullest first: the tab bar, the status,
// the table, the selected row's detail block and the key hints.
//
// The first three are used only where they leave the table modelsTableMin
// lines: blank lines round the status, and the path, are not worth a table
// of one row. What a short terminal gives up, in order: the blank lines, the
// path, table rows down to one, the hints, and last the id line. The tab bar,
// the status and the table are never given up by choice.
func (m *model) modelsFrames() []tuilayout.ListFrame {
	id, path := m.modelsDetail()
	status := m.modelsStatus()
	dim := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenDim))
	build := func(spaced bool, detail []string, withHints bool) tuilayout.ListFrame {
		return func(listView string) string {
			gap := "\n"
			if spaced {
				gap = "\n\n"
			}
			out := tabBar(m.theme, TabModels) + gap
			if status != "" {
				out += status + gap
			}
			out += listView
			if len(detail) > 0 {
				out += "\n" + dim.Render(strings.Join(detail, "\n"))
			}
			if withHints {
				out += "\n" + dim.Render(fitHints(m.width, modelsHints))
			}
			return out
		}
	}
	whole := slices.Concat(id, path)
	var frames []tuilayout.ListFrame
	for _, f := range []tuilayout.ListFrame{build(true, whole, true), build(false, whole, true), build(false, id, true)} {
		// An empty list view still occupies one line, hence the -1.
		if m.height <= 0 || m.height-(lipgloss.Height(f(""))-1) >= modelsTableMin {
			frames = append(frames, f)
		}
	}
	return append(frames, build(false, id, true), build(false, id, false), build(false, nil, false))
}

// fitModels sizes the table to the room its frame leaves. Update calls it
// after every message, like fitList for the Agents tab.
//
// The columns that fit are chosen with MODEL at its full width, the longest
// id (tuilayout.FitTo). When MODEL, STATUS and RUNNING — the three that are
// never dropped — are still wider than the list, MODEL is cut to what is
// left, and the ids longer than that lose their middle (middleCut); the
// detail block shows the selected one whole. One long id must not push
// STATUS and RUNNING off every row.
func (m *model) fitModels() {
	mt := &m.models
	if m.width <= 0 || m.height <= 0 || !mt.loaded {
		return
	}
	setModelColumn(mt.cols, mt.idWidth)
	tuilayout.FitTo(&mt.list, m.width, m.height, m.modelsFrames()...)
	need := max(mt.cols.Width(), utf8.RuneCountInString(mt.cols.Header())+tuilayout.TitleRoom)
	if over := need - mt.list.Width(); over > 0 {
		setModelColumn(mt.cols, max(mt.idWidth-over, modelsMinID))
	}
	mt.list.Title = mt.cols.Header()
}

// modelsView renders the Models tab.
func (m *model) modelsView() string {
	if m.models.phase == modelsRemove {
		return m.modelsRemoveView()
	}
	if !m.models.loaded {
		return tabBar(m.theme, TabModels) + "\n\n" + "Probing providers..."
	}
	return tuilayout.DrawnFrame(&m.models.list, m.height, m.modelsFrames()...)(m.models.list.View())
}

// applyModels takes a probe's result: the rows replace the table, with the
// cursor kept on the row it was on (or moved to selectID after a write) and
// an applied filter kept.
func (m *model) applyModels(msg modelsLoadedMsg) {
	mt := &m.models
	if msg.gen != mt.gen {
		return // a later probe is on its way
	}
	keep := mt.selectID
	if r, ok := m.selectedModel(); ok && keep == "" {
		keep = r.ID
	}
	filter := ""
	if mt.loaded && mt.list.FilterState() == list.FilterApplied {
		filter = mt.list.FilterValue()
	}
	mt.cfg, mt.rows, mt.loaded, mt.busy, mt.selectID = msg.cfg, msg.rows, true, false, ""
	mt.list, mt.cols, mt.idWidth = buildModelsList(m.theme, msg.rows)
	mt.built++
	if filter != "" {
		mt.list.SetFilterText(filter)
	}
	for i, it := range mt.list.VisibleItems() {
		if it.(modelRow).row.ID == keep {
			mt.list.Select(i)
		}
	}
	mt.loadErr = ""
	if msg.err != nil {
		mt.loadErr = "registry: " + msg.err.Error()
	}
}

// filterMatchesMsg is a bubbles list's filter result with the list it is
// for. The list ranks its items in a command and takes the result as a
// message; that message says nothing of which list sent it, and the editor
// has two. tab names the list, and built the Models table it was ranked
// over, so that a result is never applied to the other tab's list or to a
// table that has been rebuilt since.
type filterMatchesMsg struct {
	tab     Tab
	built   int
	matches list.FilterMatchesMsg
}

// tagFilter wraps a command a list returned, so that a filter result in what
// it produces comes back as a filterMatchesMsg.
func tagFilter(cmd tea.Cmd, tab Tab, built int) tea.Cmd {
	if cmd == nil {
		return nil
	}
	return func() tea.Msg {
		switch msg := cmd().(type) {
		case list.FilterMatchesMsg:
			return filterMatchesMsg{tab: tab, built: built, matches: msg}
		case tea.BatchMsg:
			for i := range msg {
				msg[i] = tagFilter(msg[i], tab, built)
			}
			return msg
		default:
			return msg
		}
	}
}

// applyFilterMatches hands a filter result to the list it was ranked for.
func (m *model) applyFilterMatches(msg filterMatchesMsg) tea.Cmd {
	var cmd tea.Cmd
	switch {
	case msg.tab == TabAgents && m.ready:
		m.list, cmd = m.list.Update(msg.matches)
	case msg.tab == TabModels && m.models.loaded && msg.built == m.models.built:
		m.models.list, cmd = m.models.list.Update(msg.matches)
	}
	return cmd
}

// updateModels handles a message while the Models tab is showing.
func (m *model) updateModels(msg tea.Msg) (tea.Model, tea.Cmd) {
	mt := &m.models
	if mt.phase == modelsRemove {
		return m.updateModelsRemove(msg)
	}
	key, isKey := msg.(tea.KeyMsg)
	if !isKey {
		return m, nil
	}
	if !mt.loaded {
		// The first probe is on its way and there is no table to act on,
		// but the user is not held on this tab until a slow provider answers.
		switch key.String() {
		case "q", "ctrl+c":
			return m.quit()
		case "tab":
			m.tab = TabAgents
		}
		return m, nil
	}
	if mt.list.FilterState() == list.Filtering {
		var cmd tea.Cmd
		mt.list, cmd = mt.list.Update(msg)
		return m, tagFilter(cmd, TabModels, mt.built)
	}
	// What the last action said has been read: the next key clears it, and
	// gives its lines back to the table.
	mt.status = ""
	switch key.String() {
	case "q", "ctrl+c":
		return m.quit()
	case "tab":
		m.tab = TabAgents
		return m, nil
	case "r":
		if mt.busy {
			return m, nil
		}
		return m, m.probeCmd()
	case "d":
		r, ok := m.selectedModel()
		switch {
		case !ok || mt.busy:
		case !r.Registered:
			mt.status = r.ID + " is not in the registry: there is nothing to remove"
		default:
			mt.phase, mt.remove = modelsRemove, r
		}
		return m, nil
	}
	var cmd tea.Cmd
	mt.list, cmd = mt.list.Update(msg)
	return m, tagFilter(cmd, TabModels, mt.built)
}

// updateModelsRemove handles the remove prompt: y removes, anything that
// means no goes back.
func (m *model) updateModelsRemove(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	mt := &m.models
	switch key.String() {
	case "y", "Y":
		row := mt.remove
		mt.phase, mt.busy, mt.writing, mt.status = modelsList, true, true, "removing "+row.ID+"..."
		return m, func() tea.Msg {
			// A registry write takes a file lock, so it is not for Update.
			return modelRemovedMsg{id: row.ID, note: strings.Join(weightsLines(row), "\n"), err: modeladmin.Remove([]string{row.ID})}
		}
	case "n", "N", "esc", "q":
		mt.phase = modelsList
	case "ctrl+c":
		return m.quit()
	}
	return m, nil
}

// modelsRemoveView is the remove prompt. It says where the weights are before
// the row is gone, because wt deletes none.
func (m *model) modelsRemoveView() string {
	r := m.models.remove
	text := "Remove " + r.ID + " from the registry? [y/N]\n\n"
	if lines := weightsLines(r); lines != nil {
		text += strings.Join(lines, "\n") + "\n"
	}
	text += "wt removes the registry entry only; it never deletes weights."
	return lipgloss.NewStyle().MaxHeight(max(m.height, 1)).Render(tabBar(m.theme, TabModels) + "\n\n" + wrapText(text, m.width))
}

// applyModelRemoved takes a removal's outcome and re-probes: the rows are
// rebuilt from the registry as it now is. A quit that was waiting for the
// write happens now, with the change recorded for the caller; after a
// refusal it does not, because the refusal has to be read.
func (m *model) applyModelRemoved(msg modelRemovedMsg) tea.Cmd {
	mt := &m.models
	mt.busy, mt.writing = false, false
	if msg.err != nil {
		mt.status = "not removed: " + msg.err.Error()
		m.quitPending = false
		return nil
	}
	m.registryChanged = true
	if m.quitPending {
		return tea.Quit
	}
	mt.status = "removed " + msg.id
	if msg.note != "" {
		// The path again: the prompt that showed it is gone.
		mt.status += "; " + msg.note
	}
	return m.probeCmd()
}
```

- [ ] **Step 6: Wire the tabs into the editor**

`quit` no longer returns `tea.Quit` itself: all three places the editor ends (a clean `q`, `n` on the quit prompt, the save that follows `y`) go through `leave`.

```diff
--- a/wt/internal/configeditor/editor.go
+++ b/wt/internal/configeditor/editor.go
@@ -1,6 +1,6 @@
-// Package configeditor provides a TUI for viewing and editing the agent
-// section of config.toml. Providers and models live in the shared
-// registry.toml, which this editor never writes, so it only manages agents.
+// Package configeditor is the TUI behind `wt config`: an Agents tab that
+// edits the agent section of wt's config.toml, and a Models tab that manages
+// the models in the shared registry.toml (through internal/modeladmin).
 package configeditor
 
 import (
@@ -35,16 +35,26 @@ type loadedMsg struct {
 }
 
 type model struct {
-	phase  phase
-	theme  themes.Theme
-	cfg    *config.Config
-	dirty  bool
-	width  int
-	height int
-	ready  bool
-	status string // shown above the list
-	saving bool   // prevents duplicate save dispatches
-	cfgErr error  // captured at construction for the initial loadedMsg
+	phase phase
+	// tab is the tab showing; opts are what the editor was opened with.
+	tab  Tab
+	opts Options
+	// models is the Models tab's state. registryChanged is true once that
+	// tab has written registry.toml (Result.RegistryChanged).
+	models          modelsTab
+	registryChanged bool
+	// quitPending is true when the user asked to leave while a registry
+	// write was in flight: the editor quits when the write's result is in.
+	quitPending bool
+	theme       themes.Theme
+	cfg         *config.Config
+	dirty       bool
+	width       int
+	height      int
+	ready       bool
+	status      string // shown above the list
+	saving      bool   // prevents duplicate save dispatches
+	cfgErr      error  // captured at construction for the initial loadedMsg
 
 	// cachedStatusBlock caches the rendered status block to avoid double rendering
 	// and re-creating lipgloss.Style on every call.
@@ -85,10 +95,15 @@ func newModel(theme themes.Theme, cfg *config.Config, cfgErr error) *model {
 // caller (cmd/wt) rather than reloaded inside the TUI, so a validation error
 // can be surfaced without hanging on the "Loading config..." screen.
 func (m *model) Init() tea.Cmd {
-	return func() tea.Msg {
+	loaded := func() tea.Msg {
 		// cfg and cfgErr are captured when newModel is called.
 		return loadedMsg{cfg: m.cfg, err: m.cfgErr}
 	}
+	if m.tab == TabModels {
+		// Opened on the Models tab (`wt model`): probe at once.
+		return tea.Batch(loaded, m.probeCmd())
+	}
+	return loaded
 }
 
 // Update handles a message and then re-fits the list: the status line can
@@ -102,6 +117,7 @@ func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
 	next, cmd := m.update(msg)
 	if nm, ok := next.(*model); ok && nm.ready {
 		nm.fitList()
+		nm.fitModels()
 		return nm, cmd
 	}
 	return next, cmd
@@ -189,6 +205,13 @@ func (m *model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
 		// at the wrong height if the hint makes status exceed terminal width.
 		m.fitList()
 		return m, nil
+	case modelsLoadedMsg:
+		m.applyModels(msg)
+		return m, nil
+	case modelRemovedMsg:
+		return m, m.applyModelRemoved(msg)
+	case filterMatchesMsg:
+		return m, m.applyFilterMatches(msg)
 	case saveMsg:
 		m.saving = false
 		if msg.err != nil {
@@ -202,7 +225,7 @@ func (m *model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
 		m.fitList()
 		if m.quitting {
 			m.quitting = false
-			return m, tea.Quit
+			return m.leave()
 		}
 		return m, nil
 	}
@@ -216,6 +239,9 @@ func (m *model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
 	if m.phase == phaseQuit {
 		return m.handleQuitUpdate(msg)
 	}
+	if m.tab == TabModels {
+		return m.updateModels(msg)
+	}
 
 	if msg, ok := msg.(tea.KeyMsg); ok {
 		// Delegate to the list while it is filtering so single-key global
@@ -223,7 +249,7 @@ func (m *model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
 		if m.ready && m.list.FilterState() == list.Filtering {
 			var cmd tea.Cmd
 			m.list, cmd = m.list.Update(msg)
-			return m, cmd
+			return m, tagFilter(cmd, TabAgents, 0)
 		}
 
 		// Global shortcuts take precedence over list delegation.
@@ -231,11 +257,14 @@ func (m *model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
 		case "ctrl+s":
 			return m.handleSave()
 		case "q", "ctrl+c":
-			if !m.dirty {
-				return m, tea.Quit
+			return m.quit()
+		case "tab":
+			m.tab = TabModels
+			if !m.models.asked {
+				// The first visit probes; later ones show what is there
+				// (r refreshes).
+				return m, m.probeCmd()
 			}
-			m.phase = phaseQuit
-			m.quitting = false
 			return m, nil
 		case "d":
 			if m.ready {
@@ -268,13 +297,40 @@ func (m *model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
 			var cmd tea.Cmd
 			m.list, cmd = m.list.Update(msg)
 			if cmd != nil {
-				return m, cmd
+				return m, tagFilter(cmd, TabAgents, 0)
 			}
 		}
 	}
 	return m, nil
 }
 
+// quit leaves the editor, by way of the unsaved-changes prompt when the
+// Agents tab has edits that were not saved. The prompt concerns agent edits
+// only: every change on the Models tab is already in registry.toml.
+func (m *model) quit() (tea.Model, tea.Cmd) {
+	if !m.dirty {
+		return m.leave()
+	}
+	m.phase = phaseQuit
+	m.quitting = false
+	return m, nil
+}
+
+// leave ends the program — once the registry write in flight, if there is
+// one, has reported. The write runs in a command; a program that ended before
+// its message was handled would return Result{RegistryChanged: false} for a
+// registry that did change, and the caller would skip the route sync it owes.
+// So the quit is recorded, and applyModelRemoved issues it. A probe in flight
+// holds nothing up.
+func (m *model) leave() (tea.Model, tea.Cmd) {
+	if m.models.writing {
+		m.quitPending = true
+		m.phase = phaseList
+		return m, nil
+	}
+	return m, tea.Quit
+}
+
 // handleQuitUpdate processes keys in the unsaved-changes quit prompt.
 func (m *model) handleQuitUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
 	if msg, ok := msg.(tea.KeyMsg); ok {
@@ -283,7 +339,7 @@ func (m *model) handleQuitUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
 			m.quitting = true
 			return m.handleSave()
 		case "n", "N":
-			return m, tea.Quit
+			return m.leave()
 		case "c", "C", "esc":
 			m.phase = phaseList
 			m.quitting = false
@@ -303,13 +359,16 @@ func (m *model) View() string {
 	case phaseDelete:
 		return m.deleteView()
 	case phaseQuit:
-		return "You have unsaved changes. Save before quitting?\n\n[y] save and quit  [n] discard and quit  [c] cancel\n"
+		return "You have unsaved agent changes. Save before quitting?\n\n[y] save and quit  [n] discard and quit  [c] cancel\n"
 	default:
+		if m.tab == TabModels {
+			return m.modelsView()
+		}
 		var status string
 		if s := m.statusBlock(); s != "" {
 			status = s + "\n\n"
 		}
-		return "Agents (providers/models: edit registry.toml by hand)\n\n" + status + m.list.View()
+		return tabBar(m.theme, TabAgents) + "\n\n" + status + m.list.View()
 	}
 }
 
@@ -342,11 +401,16 @@ func (m *model) handleFormUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
 
 // Run starts the config editor TUI with the config already loaded by the
 // caller. A non-nil cfgErr is surfaced as a status message so the user can
-// repair the config without the CLI exiting early.
-func Run(theme themes.Theme, cfg *config.Config, cfgErr error, opts ...tea.ProgramOption) error {
+// repair the config without the CLI exiting early. The Result says whether
+// the Models tab wrote the registry, in which case the caller owes the
+// LiteLLM routes one sync; it is reported even when the program ends with an
+// error, since the write happened.
+func Run(theme themes.Theme, cfg *config.Config, cfgErr error, o Options, opts ...tea.ProgramOption) (Result, error) {
 	m := newModel(theme, cfg, cfgErr)
+	o.Models = o.Models.withDefaults()
+	m.opts, m.tab = o, o.StartTab
 	allOpts := append([]tea.ProgramOption{tea.WithAltScreen()}, opts...)
 	p := tea.NewProgram(m, allOpts...)
 	_, err := p.Run()
-	return err
+	return Result{RegistryChanged: m.registryChanged}, err
 }
```

The Agents list gives up its own quit keys and names the two the editor handles for it:

```diff
--- a/wt/internal/configeditor/agents_tab.go
+++ b/wt/internal/configeditor/agents_tab.go
@@ -4,6 +4,7 @@ import (
 	"fmt"
 	"sort"
 
+	"github.com/charmbracelet/bubbles/key"
 	"github.com/charmbracelet/bubbles/list"
 	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
 	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
@@ -88,5 +89,17 @@ func buildAgentsList(theme themes.Theme, width, height int, cfg *config.Config)
 	l := list.New(items, tui.ThemedListDelegate(theme), width, height)
 	l.Title = "Agents"
 	l.SetShowStatusBar(false)
+	// q and esc are the list's own quit keys, and its quit goes round the
+	// editor's: esc on this list used to end wt without the unsaved-changes
+	// prompt. The editor handles q (quit); esc only clears a filter. The two
+	// keys the list does not know about are added to its help in their place.
+	l.DisableQuitKeybindings()
+	// q first: the help line of a 40-column terminal ends after it.
+	extra := []key.Binding{
+		key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
+		key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "models")),
+	}
+	l.AdditionalShortHelpKeys = func() []key.Binding { return extra }
+	l.AdditionalFullHelpKeys = func() []key.Binding { return extra }
 	return l
 }
```

`cmd/wt` calls `Run` in one place. It is adapted here only as far as building needs; the options and the result are Task 12's:

```diff
--- a/wt/cmd/wt/commands_config.go
+++ b/wt/cmd/wt/commands_config.go
@@ -24,7 +24,11 @@ import (
 // configeditorRun is the entry point for the config viewer TUI. It is a
 // package-level var so tests can verify it is called without needing a TTY.
 var configeditorRun = func(theme themes.Theme, cfg *config.Config, cfgErr error) error {
-	return configeditor.Run(theme, cfg, cfgErr)
+	// The editor's options and its result are not used yet: `wt config`
+	// opens on the Agents tab, and the route sync a change on the Models tab
+	// is owed waits for the next `wt start`, `wt stop` or `wt litellm sync`.
+	_, err := configeditor.Run(theme, cfg, cfgErr, configeditor.Options{})
+	return err
 }
 
 // configCmd returns the `wt config` command. With no subcommand, launches
```

- [ ] **Step 7: Run the tests**

Run, from `wt/`: `test -f internal/configeditor/testmain_test.go && go build ./... && go test -count=1 ./internal/configeditor ./cmd/wt`

Expected: both `ok` — the new tests, every existing editor test, and `cmd/wt` unchanged in behaviour. (If the command prints nothing and exits 1, Step 1's file is missing.)

- [ ] **Step 8: Commit**

```bash
cd wt && test -z "$(gofmt -l .)" && go vet ./... && cd ..
git add wt/internal/configeditor wt/cmd/wt/commands_config.go
git commit -m "feat(wt): wt config gains a Models tab: list, filter and remove registry models"
```

### Task 12: Bare `wt model`, and one route sync when the editor exits

**Files:**
- Modify: `wt/cmd/wt/commands_config_test.go` (`stubConfigEditor`; the two editor tests), `wt/cmd/wt/main_test.go:24-45`, `wt/cmd/wt/model_test.go` (`TestModelCommandGroup`; four new tests)
- Modify: `wt/cmd/wt/commands_config.go:24-48` (`configeditorRun`, `configCmd`; new `runConfigEditor`), `wt/cmd/wt/model.go` (`modelCmd`)

**Interfaces:**
- Consumes: Task 11's `configeditor.Run`, `Options`, `ModelsDeps`, `Result`, `TabAgents`, `TabModels`; existing in `cmd/wt`: `probeInventory`, `seedEnv`, `ollamaCaps` (Task 8), `syncRoutesAfterWrite`, `stdinTTY`, `app.theme`, `app.cfg`, `app.cfgErr`; test helpers `modelHome`, `writeRegistry`, `runWT`, `mustRead` (Task 8), `stubRouteSync`, `realRouteSync`, `stubProbeInventory`
- Produces:
  - `var configeditorRun func(theme themes.Theme, cfg *config.Config, cfgErr error, o configeditor.Options) (configeditor.Result, error)` — Task 11 left it with its old signature
  - `func runConfigEditor(out, errOut io.Writer, a *app, start configeditor.Tab) error` — opens the editor; when `Result.RegistryChanged`, runs `syncRoutesAfterWrite` once and prints its warning
  - bare `wt model`: `runConfigEditor(…, configeditor.TabModels)` on a terminal; without one, an error naming the commands that need none
  - test helper `stubConfigEditor(t, res configeditor.Result, err error) *editorCall` (`editorCall{called bool; cfg *config.Config; cfgErr error; opts configeditor.Options}`)

- [ ] **Step 1: Write the failing tests**

```diff
--- a/wt/cmd/wt/commands_config_test.go
+++ b/wt/cmd/wt/commands_config_test.go
@@ -14,6 +14,7 @@ import (
 	"testing"
 
 	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
+	"github.com/ohanaverse/local-ai-setup/wt/internal/configeditor"
 	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
 )
 
@@ -56,17 +57,33 @@ func newTestApp(t *testing.T) (*app, string) {
 	return a, tmp
 }
 
+// editorCall records one run of the config editor.
+type editorCall struct {
+	called bool
+	cfg    *config.Config
+	cfgErr error
+	opts   configeditor.Options
+}
+
+// stubConfigEditor replaces the editor (which needs a terminal) with one that
+// records how it was opened and returns res and err.
+func stubConfigEditor(t *testing.T, res configeditor.Result, err error) *editorCall {
+	t.Helper()
+	call := &editorCall{}
+	old := configeditorRun
+	configeditorRun = func(_ themes.Theme, cfg *config.Config, cfgErr error, o configeditor.Options) (configeditor.Result, error) {
+		*call = editorCall{called: true, cfg: cfg, cfgErr: cfgErr, opts: o}
+		return res, err
+	}
+	t.Cleanup(func() { configeditorRun = old })
+	return call
+}
+
 // TestConfigCmd_NoSubcommand_LaunchesEditor: wt config with no args now
 // launches the config editor TUI. We stub configeditorRun to avoid the
 // TTY requirement in tests.
 func TestConfigCmd_NoSubcommand_LaunchesEditor(t *testing.T) {
-	called := false
-	old := configeditorRun
-	configeditorRun = func(theme themes.Theme, cfg *config.Config, cfgErr error) error {
-		called = true
-		return nil
-	}
-	defer func() { configeditorRun = old }()
+	call := stubConfigEditor(t, configeditor.Result{}, nil)
 
 	a, _ := newTestApp(t)
 	cmd := configCmd(a)
@@ -76,24 +93,19 @@ func TestConfigCmd_NoSubcommand_LaunchesEditor(t *testing.T) {
 	if err := cmd.Execute(); err != nil {
 		t.Fatalf("Execute() error = %v", err)
 	}
-	if !called {
+	if !call.called {
 		t.Fatal("expected configeditorRun to be called")
 	}
+	if call.opts.StartTab != configeditor.TabAgents {
+		t.Errorf("wt config opened on tab %d, want the Agents tab", call.opts.StartTab)
+	}
 }
 
 // TestConfigCmd_InvalidConfig_LaunchesEditor verifies that `wt config`
 // launches the repair TUI even when the loaded config has a validation
 // error. Without this, a broken config.toml would be a dead end.
 func TestConfigCmd_InvalidConfig_LaunchesEditor(t *testing.T) {
-	var passedCfg *config.Config
-	var passedErr error
-	old := configeditorRun
-	configeditorRun = func(theme themes.Theme, cfg *config.Config, cfgErr error) error {
-		passedCfg = cfg
-		passedErr = cfgErr
-		return nil
-	}
-	defer func() { configeditorRun = old }()
+	call := stubConfigEditor(t, configeditor.Result{}, nil)
 
 	a, _ := newTestApp(t)
 	a.cfgErr = fmt.Errorf("validation failed")
@@ -104,10 +116,10 @@ func TestConfigCmd_InvalidConfig_LaunchesEditor(t *testing.T) {
 	if err := cmd.Execute(); err != nil {
 		t.Fatalf("Execute() error = %v", err)
 	}
-	if passedCfg == nil {
+	if call.cfg == nil {
 		t.Fatal("expected configeditorRun to receive the config")
 	}
-	if passedErr == nil {
+	if call.cfgErr == nil {
 		t.Fatal("expected configeditorRun to receive the validation error")
 	}
 }
```

```diff
--- a/wt/cmd/wt/main_test.go
+++ b/wt/cmd/wt/main_test.go
@@ -11,6 +11,7 @@ import (
 	"testing"
 
 	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
+	"github.com/ohanaverse/local-ai-setup/wt/internal/configeditor"
 	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
 	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
 	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
@@ -22,13 +23,7 @@ import (
 // args invokes the config editor TUI. We stub configeditorRun so the test
 // does not require a TTY.
 func TestConfig_NoSubcommand_LaunchesEditor(t *testing.T) {
-	called := false
-	old := configeditorRun
-	configeditorRun = func(theme themes.Theme, cfg *config.Config, cfgErr error) error {
-		called = true
-		return nil
-	}
-	defer func() { configeditorRun = old }()
+	call := stubConfigEditor(t, configeditor.Result{}, nil)
 
 	var buf bytes.Buffer
 	root := rootCmd()
@@ -39,7 +34,7 @@ func TestConfig_NoSubcommand_LaunchesEditor(t *testing.T) {
 	if err != nil {
 		t.Fatalf("unexpected error: %v", err)
 	}
-	if !called {
+	if !call.called {
 		t.Fatal("expected configeditorRun to be called")
 	}
 }
```

```diff
--- a/wt/cmd/wt/model_test.go
+++ b/wt/cmd/wt/model_test.go
@@ -10,6 +10,7 @@ import (
 	"testing"
 
 	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
+	"github.com/ohanaverse/local-ai-setup/wt/internal/configeditor"
 	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
 	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
 	"gopkg.in/yaml.v3"
@@ -428,11 +429,13 @@ func TestModelInitReportsABrokenRegistryLink(t *testing.T) {
 	}
 }
 
-// TestModelCommandGroup pins the `wt model` group as cobra serves it in this
-// step: `wt model init` runs with no registry (the case it exists for — the
-// root command would refuse on the same load error), bare `wt model` prints
-// help, an unknown word under it is an error rather than a worktree name, and
-// the removed `wt models` keeps its own message.
+// TestModelCommandGroup pins the `wt model` group as cobra serves it:
+// `wt model init` runs with no registry (the case it exists for — the root
+// command would refuse on the same load error), bare `wt model` opens the
+// editor on the Models tab (TestBareModelOpensTheModelsTab) and without a
+// terminal says which commands work without one, an unknown word under it is
+// an error rather than a worktree name, and the removed `wt models` keeps its
+// own message.
 func TestModelCommandGroup(t *testing.T) {
 	home := t.TempDir()
 	withCleanConfigEnv(t, home)
@@ -452,9 +455,11 @@ func TestModelCommandGroup(t *testing.T) {
 	if err != nil || !strings.Contains(out, `"providers_added":["ollama"]`) {
 		t.Errorf("wt model init --json = %q, %v", out, err)
 	}
-	out, err = run("model")
-	if err != nil || !strings.Contains(out, "init") || !strings.Contains(out, "Manage the model registry") {
-		t.Errorf("bare `wt model` should print the group's help, got %q, %v", out, err)
+	oldTTY := stdinTTY
+	stdinTTY = func() bool { return false }
+	t.Cleanup(func() { stdinTTY = oldTTY })
+	if _, err = run("model"); err == nil || !strings.Contains(err.Error(), "wt model needs a terminal to open the Models tab") || !strings.Contains(err.Error(), "`wt model list`") {
+		t.Errorf("bare `wt model` with no terminal: err = %v, want it to name the commands that need none", err)
 	}
 	if _, err = run("model", "bogus"); err == nil || !strings.Contains(err.Error(), `unknown command "bogus" for "wt model"`) {
 		t.Errorf("wt model bogus: err = %v, want an unknown-command error", err)
@@ -478,3 +483,177 @@ func TestRegistryWriteGuardIsArmedHere(t *testing.T) {
 		t.Fatal("registry write guard is not armed: TestMain must call config.IsolateConfigHomeForTest")
 	}
 }
+
+// TestBareModelOpensTheModelsTab verifies bare `wt model`, on a terminal,
+// opens the config editor on its Models tab and hands it this package's own
+// seams for the probe, the seeding environment and the ollama lookup — so the
+// tab reaches the machine the same way the commands do, and a test of either
+// can stand in for both.
+func TestBareModelOpensTheModelsTab(t *testing.T) {
+	modelHome(t, writeRegistry)
+	oldTTY := stdinTTY
+	stdinTTY = func() bool { return true }
+	t.Cleanup(func() { stdinTTY = oldTTY })
+	call := stubConfigEditor(t, configeditor.Result{}, nil)
+	syncs := stubRouteSync(t, "")
+	if _, err := runWT(t, "model"); err != nil {
+		t.Fatal(err)
+	}
+	if !call.called || call.opts.StartTab != configeditor.TabModels {
+		t.Fatalf("called = %v, start tab = %d; want the editor opened on the Models tab", call.called, call.opts.StartTab)
+	}
+	d := call.opts.Models
+	if d.Probe == nil || d.SeedEnv == nil || d.Capabilities == nil {
+		t.Errorf("the tab was not given cmd/wt's seams: %+v", d)
+	}
+	if *syncs != 0 {
+		t.Errorf("syncs = %d with nothing changed, want 0", *syncs)
+	}
+}
+
+// TestEditorSyncsRoutesOnceOnExit verifies the Models tab's one route sync:
+// it runs once after the editor closes, only when the tab wrote the registry,
+// a sync warning is printed, and it still runs when the editor ended with an
+// error — the registry write already happened. Per-change syncs would restart
+// the proxy once for every model added in a sitting.
+func TestEditorSyncsRoutesOnceOnExit(t *testing.T) {
+	modelHome(t, writeRegistry)
+	oldTTY := stdinTTY
+	stdinTTY = func() bool { return true }
+	t.Cleanup(func() { stdinTTY = oldTTY })
+
+	for _, args := range [][]string{{"model"}, {"config"}} {
+		stubConfigEditor(t, configeditor.Result{}, nil)
+		syncs := stubRouteSync(t, "")
+		if _, err := runWT(t, args...); err != nil || *syncs != 0 {
+			t.Errorf("wt %s, nothing changed: err = %v, syncs = %d; want no sync", args[0], err, *syncs)
+		}
+		stubConfigEditor(t, configeditor.Result{RegistryChanged: true}, nil)
+		syncs = stubRouteSync(t, "LiteLLM routes not synced: proxy config is read-only")
+		out, err := runWT(t, args...)
+		if err != nil || *syncs != 1 || !strings.Contains(out, "warning: LiteLLM routes not synced: proxy config is read-only") {
+			t.Errorf("wt %s, registry changed: err = %v, syncs = %d, out = %q; want one sync and its warning", args[0], err, *syncs, out)
+		}
+	}
+
+	stubConfigEditor(t, configeditor.Result{RegistryChanged: true}, errors.New("terminal went away"))
+	syncs := stubRouteSync(t, "")
+	if _, err := runWT(t, "model"); err == nil || *syncs != 1 {
+		t.Errorf("editor failed after a write: err = %v, syncs = %d; want the error and still one sync", err, *syncs)
+	}
+}
+
+// TestEditorExitSyncUnderARedirectedRegistry is the scratch-registry safety
+// test for the two commands that open the editor, `wt model` and `wt config`:
+// the Models tab writes the registry, and the sync that follows on exit obeys
+// the same rule as `wt model add`. LiteLLM's config.yaml follows neither
+// registry variable, so with the registry redirected and config.yaml not
+// named, the default file is not touched and the user is told why; named,
+// that file is the one synced. The exit status is 0 either way: the registry
+// change was made.
+func TestEditorExitSyncUnderARedirectedRegistry(t *testing.T) {
+	setup := func(t *testing.T) (registry, defaultYAML string) {
+		home := t.TempDir()
+		registry = filepath.Join(home, "scratch", "registry.toml")
+		defaultYAML = filepath.Join(home, ".config", "litellm", "config.yaml")
+		for path, content := range map[string]string{registry: writeRegistry, defaultYAML: "model_list: []\n"} {
+			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
+				t.Fatal(err)
+			}
+			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
+				t.Fatal(err)
+			}
+		}
+		withCleanConfigEnv(t, home)
+		t.Setenv("WT_REGISTRY", registry)
+		t.Setenv("WT_LITELLM_CONFIG", "")
+		t.Setenv("MODELMAN_LITELLM_CONFIG", "")
+		t.Setenv("OPENROUTER_API_KEY", "sk-test-not-a-key")
+		oldTTY := stdinTTY
+		stdinTTY = func() bool { return true }
+		t.Cleanup(func() { stdinTTY = oldTTY })
+		// The editor itself needs a terminal; what it reports is a registry
+		// its Models tab wrote.
+		stubConfigEditor(t, configeditor.Result{RegistryChanged: true}, nil)
+		realRouteSync(t)
+		// ollama answers its probe and has nothing pulled, so the sync has
+		// no provider to warn about.
+		stubProbeInventory(t, localmodels.Snapshot{Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK}})
+		return registry, defaultYAML
+	}
+	for _, command := range []string{"model", "config"} {
+		t.Run("wt "+command+": config.yaml not named", func(t *testing.T) {
+			registry, defaultYAML := setup(t)
+			out, err := runWT(t, command)
+			if err != nil {
+				t.Fatalf("the registry was written, so the command must exit 0: %v", err)
+			}
+			want := "warning: LiteLLM routes not touched: the registry is " + registry +
+				" but config.yaml is the default " + defaultYAML +
+				" — set WT_LITELLM_CONFIG to the config.yaml that registry belongs to\n"
+			if out != want {
+				t.Errorf("output = %q\nwant %q", out, want)
+			}
+			if got := mustRead(t, defaultYAML); got != "model_list: []\n" {
+				t.Errorf("the default config.yaml was touched:\n%s", got)
+			}
+		})
+		t.Run("wt "+command+": config.yaml named", func(t *testing.T) {
+			_, defaultYAML := setup(t)
+			scratchYAML := filepath.Join(t.TempDir(), "config.yaml")
+			if err := os.WriteFile(scratchYAML, []byte("model_list: []\n"), 0o600); err != nil {
+				t.Fatal(err)
+			}
+			t.Setenv("WT_LITELLM_CONFIG", scratchYAML)
+			out, err := runWT(t, command)
+			if err != nil || strings.Contains(out, "warning") {
+				t.Fatalf("wt %s = %q, %v; want a clean sync", command, out, err)
+			}
+			if got := mustRead(t, scratchYAML); !strings.Contains(got, "openrouter/a--b") {
+				t.Errorf("the named config.yaml was not synced:\n%s", got)
+			}
+			if got := mustRead(t, defaultYAML); got != "model_list: []\n" {
+				t.Errorf("the default config.yaml was touched:\n%s", got)
+			}
+		})
+	}
+}
+
+// TestRouteSyncThatChangesNothingDoesNotRestartTheProxy is the requirement
+// the exit sync rests on: a sync that leaves config.yaml byte-identical does
+// not restart the LiteLLM proxy. The first sync here writes the registry's
+// cloud route and restarts once; the second finds nothing to change, leaves
+// the file's bytes alone and runs no restart command. Without this, opening
+// the Models tab and renaming a family would bounce the proxy under every
+// running agent.
+func TestRouteSyncThatChangesNothingDoesNotRestartTheProxy(t *testing.T) {
+	modelHome(t, writeRegistry)
+	t.Setenv("OPENROUTER_API_KEY", "sk-test-not-a-key")
+	yaml := filepath.Join(t.TempDir(), "config.yaml")
+	if err := os.WriteFile(yaml, []byte("model_list: []\n"), 0o600); err != nil {
+		t.Fatal(err)
+	}
+	realRouteSync(t)
+	stubProbeInventory(t, localmodels.Snapshot{Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK}})
+	t.Setenv("WT_LITELLM_CONFIG", yaml)
+	restarts := filepath.Join(t.TempDir(), "restarts")
+	t.Setenv("WT_LITELLM_RESTART_CMD", "echo restart >> "+restarts)
+
+	var out, errOut bytes.Buffer
+	if w := syncRoutesAfterWrite(&out, &errOut); w != "" {
+		t.Fatalf("first sync: %s", w)
+	}
+	first := mustRead(t, yaml)
+	if !strings.Contains(first, "openrouter/a--b") || mustRead(t, restarts) != "restart\n" {
+		t.Fatalf("the first sync should write the cloud route and restart once; restarts = %q, config.yaml:\n%s", mustRead(t, restarts), first)
+	}
+	if w := syncRoutesAfterWrite(&out, &errOut); w != "" {
+		t.Fatalf("second sync: %s", w)
+	}
+	if mustRead(t, yaml) != first {
+		t.Error("a sync with nothing to change rewrote config.yaml")
+	}
+	if got := mustRead(t, restarts); got != "restart\n" {
+		t.Errorf("restarts = %q after a sync that changed nothing, want still one", got)
+	}
+}
```

- [ ] **Step 2: Run them to see them fail**

Run, from `wt/`: `go test -count=1 ./cmd/wt -run 'TestConfig|TestModel|TestBareModel|TestEditor|TestRouteSync'`

Expected: the package's tests do not build: `configeditorRun` still has the signature Task 11 left it.

```text
FAIL	github.com/ohanaverse/local-ai-setup/wt/cmd/wt [build failed]
FAIL
# github.com/ohanaverse/local-ai-setup/wt/cmd/wt [github.com/ohanaverse/local-ai-setup/wt/cmd/wt.test]
cmd/wt/commands_config_test.go:74:20: cannot use func(_ themes.Theme, cfg *config.Config, cfgErr error, o configeditor.Options) (configeditor.Result, error) {…} (value of type func(_ themes.Theme, cfg *config.Config, ...
```

- [ ] **Step 3: Open the editor through one function that owes the sync**

```diff
--- a/wt/cmd/wt/commands_config.go
+++ b/wt/cmd/wt/commands_config.go
@@ -12,6 +12,7 @@ package main
 
 import (
 	"fmt"
+	"io"
 	"sort"
 
 	"github.com/charmbracelet/lipgloss"
@@ -23,29 +24,47 @@ import (
 
 // configeditorRun is the entry point for the config viewer TUI. It is a
 // package-level var so tests can verify it is called without needing a TTY.
-var configeditorRun = func(theme themes.Theme, cfg *config.Config, cfgErr error) error {
-	// The editor's options and its result are not used yet: `wt config`
-	// opens on the Agents tab, and the route sync a change on the Models tab
-	// is owed waits for the next `wt start`, `wt stop` or `wt litellm sync`.
-	_, err := configeditor.Run(theme, cfg, cfgErr, configeditor.Options{})
+var configeditorRun = func(theme themes.Theme, cfg *config.Config, cfgErr error, o configeditor.Options) (configeditor.Result, error) {
+	return configeditor.Run(theme, cfg, cfgErr, o)
+}
+
+// runConfigEditor opens the editor on tab start and, when its Models tab
+// wrote the registry, runs the one LiteLLM route sync that tab owes: the tab
+// does not sync per change, so that a run of edits costs one proxy restart.
+// The sync runs even when the editor ended with an error, because the write
+// already happened; if wt dies before it, the next `wt start`, `wt stop` or
+// launch through LiteLLM repairs the routes.
+func runConfigEditor(out, errOut io.Writer, a *app, start configeditor.Tab) error {
+	res, err := configeditorRun(a.theme, a.cfg, a.cfgErr, configeditor.Options{
+		StartTab: start,
+		Models:   configeditor.ModelsDeps{Probe: probeInventory, SeedEnv: seedEnv, Capabilities: ollamaCaps},
+	})
+	if res.RegistryChanged {
+		if w := syncRoutesAfterWrite(out, errOut); w != "" {
+			fmt.Fprintf(errOut, "warning: %s\n", w)
+		}
+	}
 	return err
 }
 
 // configCmd returns the `wt config` command. With no subcommand, launches
-// an interactive TUI for viewing and editing agents in config.toml.
+// the interactive editor: agents in config.toml, models in registry.toml.
 // Subcommands configure specific concerns without entering the TUI.
 func configCmd(a *app) *cobra.Command {
 	cmd := &cobra.Command{
 		Use:   "config",
 		Short: "Manage wt preferences and config.toml",
 		Long: "Manage wt user preferences.\n\n" +
-			"With no subcommand, launches an interactive TUI to view and edit\n" +
-			"agents in config.toml.\n\n" +
+			"With no subcommand, launches an interactive TUI with two tabs (tab\n" +
+			"switches): Agents, the agents in config.toml, saved with ctrl+s; and\n" +
+			"Models, the models in registry.toml, where each change is written at\n" +
+			"once and the LiteLLM routes are synced when you quit. `wt model` opens\n" +
+			"it on the Models tab.\n\n" +
 			"Subcommands:\n" +
 			"  theme   active color theme\n" +
 			"  path    print the config directory",
 		RunE: func(cmd *cobra.Command, args []string) error {
-			return configeditorRun(a.theme, a.cfg, a.cfgErr)
+			return runConfigEditor(cmd.OutOrStdout(), cmd.ErrOrStderr(), a, configeditor.TabAgents)
 		},
 	}
 	cmd.AddCommand(configPathCmd(a), configThemeCmd(a))
```

- [ ] **Step 4: Make bare `wt model` open the Models tab**

```diff
--- a/wt/cmd/wt/model.go
+++ b/wt/cmd/wt/model.go
@@ -9,6 +9,7 @@ import (
 	"os"
 
 	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
+	"github.com/ohanaverse/local-ai-setup/wt/internal/configeditor"
 	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
 	"github.com/spf13/cobra"
 )
@@ -49,15 +50,25 @@ func realSyncRoutesAfterWrite(out, errOut io.Writer) string {
 	return "LiteLLM routes not synced: " + err.Error()
 }
 
-// modelCmd is the `wt model` group: list (model_list.go), add, edit and rm
-// (model_write.go), and init, which creates the registry and seeds its
-// provider rows.
+// modelCmd is the `wt model` group: bare, it opens `wt config` on the Models
+// tab; list (model_list.go), add, edit and rm (model_write.go) and init, which
+// creates the registry and seeds its provider rows, work without a terminal.
 func modelCmd(a *app) *cobra.Command {
 	c := &cobra.Command{
 		Use:   "model",
 		Short: "Manage the model registry (registry.toml)",
-		Args:  cobra.NoArgs,
-		RunE:  func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
+		Long: "Manage the models in the registry.\n\n" +
+			"With no subcommand, opens `wt config` on its Models tab (needs a terminal).\n" +
+			"The subcommands do the same work without one.",
+		Args: cobra.NoArgs,
+		// A missing terminal is not a usage mistake.
+		SilenceUsage: true,
+		RunE: func(cmd *cobra.Command, _ []string) error {
+			if !stdinTTY() {
+				return errors.New("wt model needs a terminal to open the Models tab; without one use `wt model list`, `wt model add`, `wt model edit` or `wt model rm`")
+			}
+			return runConfigEditor(cmd.OutOrStdout(), cmd.ErrOrStderr(), a, configeditor.TabModels)
+		},
 	}
 	var initJSON bool
 	initC := &cobra.Command{
```

- [ ] **Step 5: Run the tests**

Run, from `wt/`: `go build ./... && go test -count=1 ./cmd/wt ./internal/configeditor`

Expected: both `ok`. `TestRouteSyncThatChangesNothingDoesNotRestartTheProxy` is the spec's requirement that a sync leaving `config.yaml` byte-identical does not restart the proxy, verified end to end: it passes without any change to `internal/litellm`, which already skips the restart (`TestSyncUnchangedDoesNotRestart`). `TestEditorExitSyncUnderARedirectedRegistry` is the spec's scratch-registry safety test for the two commands that open the editor: with the registry redirected and no `WT_LITELLM_CONFIG`, the exit sync leaves the default `config.yaml` alone, says why, and the command still exits 0.

- [ ] **Step 6: Commit**

```bash
cd wt && test -z "$(gofmt -l .)" && go vet ./... && cd ..
git add wt/cmd/wt
git commit -m "feat(wt): wt model opens the Models tab, and the routes sync once when the editor exits"
```

### Task 13: Look at the real screens, and document the tab

A unit test of a view's text has passed here before while the real 80-column screen cut the line (#209). So before this PR hands off, the built binary is driven in a pty and the screens are read. The capture needs four small scripts; they live in a scratch directory, `/tmp/wt-step3-screens`, and are not committed.

**Files:**
- Create (scratch, not committed): `wt-screen.py`, `fake-providers.py`, `make-home.sh`, `run.sh` in `/tmp/wt-step3-screens`
- Modify: `wt/docs/wt-config.md`, `wt/docs/wt-model.md`, `wt/docs/internals/tui.md`, `wt/docs/internals/testing.md`, `wt/CLAUDE.md`, `wt/CHANGELOG.md`

**Interfaces:**
- Consumes: the binary built from this branch.
- Produces: nothing other tasks use. Task 16 writes the same four scripts again, in full: it runs in another session, on another branch, and does not count on this directory.

**How the steps are run.** Every command below stands on its own, with the paths written out: none relies on a variable, a directory change or an environment that an earlier step left behind, so each works in a fresh shell. wt is never run directly. It is run as `/tmp/wt-step3-screens/run.sh /tmp/wt-step3-screens/wt …`, and `run.sh` empties the environment before it sets a throwaway `HOME`, a scratch LiteLLM config and a restart command that does nothing — so a step run in the wrong shell cannot reach the real `HOME`, and nothing in the real one can leak into a screen.

- [ ] **Step 1: Write the pty driver**

wt's TUI asks the terminal for its background colour and the cursor position before it draws, and waits for both answers; the driver gives them. It then replays what wt wrote onto a grid, so what it prints is the screen as a terminal of that size shows it, each line with its width in front. If wt ends before every key has been sent, the driver says after which key (on stderr) and still prints the last screen.

```bash
mkdir -p /tmp/wt-step3-screens
```

Create `/tmp/wt-step3-screens/wt-screen.py`:

```python
"""Run a full-screen wt command in a pty and print the screen it shows.

Usage: python3 wt-screen.py [--size 80x24] [--keys 'KEYS'] [--wait SECONDS] <command> [args...]

KEYS are sent one at a time, 0.4s apart, after the first screen is drawn:
plain characters as they are, and {tab} {enter} {esc} {up} {down} {left}
{right} {ctrl+s} {ctrl+c} {bs} for the keys that have no character. The screen
printed is the one shown after the last key, each line with its width in
front. When the command ends before every key was sent, the last screen it
drew is printed all the same, and a line on stderr says after which key.
"""
import fcntl, os, pty, re, select, signal, struct, sys, termios, time

args = sys.argv[1:]
cols, rows, keys, wait = 80, 24, "", 2.0
while args and args[0].startswith("--"):
    flag, value, args = args[0], args[1], args[2:]
    if flag == "--size":
        cols, rows = (int(n) for n in value.split("x"))
    elif flag == "--keys":
        keys = value
    elif flag == "--wait":
        wait = float(value)

NAMED = {"tab": "\t", "enter": "\r", "esc": "\x1b", "up": "\x1b[A", "down": "\x1b[B", "right": "\x1b[C",
         "left": "\x1b[D", "ctrl+s": "\x13", "ctrl+c": "\x03", "bs": "\x7f"}
presses = [NAMED[a] if a else b for a, b in re.findall(r"\{([a-z+]+)\}|(.)", keys, re.S)]

pid, fd = pty.fork()
if pid == 0:
    # The child's stdin is the pty: size it before wt reads the size.
    fcntl.ioctl(0, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0))
    os.environ["TERM"] = "xterm-256color"
    os.execvp(args[0], args)

out, answered, ended = b"", False, False


def pump(seconds):
    """Read what wt draws for a while, answering its two terminal queries."""
    global out, answered, ended
    end = time.time() + seconds
    while time.time() < end and not ended:
        if not select.select([fd], [], [], 0.05)[0]:
            continue
        try:
            chunk = os.read(fd, 65536)
        except OSError:
            chunk = b""
        if not chunk:
            ended = True  # the command has left: its side of the pty is closed
            return
        out += chunk
        # wt asks the terminal for its background colour (OSC 11) and the
        # cursor position before it draws; without both answers it waits.
        if not answered and b"\x1b]11;?" in out and b"\x1b[6n" in out:
            os.write(fd, b"\x1b]11;rgb:0000/0000/0000\x1b\\\x1b[1;1R")
            answered = True


pump(wait)
for sent, press in enumerate(presses):
    try:
        if ended:
            raise OSError
        os.write(fd, press.encode())
    except OSError:
        # Writing to a pty whose command has gone fails (EIO on macOS).
        print(f"the command exited after key {sent} of {len(presses)}", file=sys.stderr)
        break
    pump(0.4)
pump(0.6)
# The screen is captured: end wt without choosing anything more. (When the
# keys ended with q, wt has already left by itself.)
try:
    os.kill(pid, signal.SIGKILL)
except ProcessLookupError:
    pass
os.waitpid(pid, 0)


def screen(data):
    """Replay the escape sequences wt wrote onto a rows x cols grid."""
    grid = [[" "] * cols for _ in range(rows)]
    r = c = 0
    text = data.decode("utf-8", "replace")
    i = 0
    while i < len(text):
        ch = text[i]
        if ch == "\x1b":
            m = re.match(r"\x1b\[([0-9;?]*)([A-Za-z])", text[i:])
            if m:
                nums = [int(n) if n.isdigit() else 0 for n in m.group(1).split(";")] if m.group(1) else []
                op = m.group(2)
                if op in "Hf":
                    r = (nums[0] or 1) - 1 if nums else 0
                    c = (nums[1] or 1) - 1 if len(nums) > 1 else 0
                elif op == "A":
                    r = max(0, r - ((nums[0] if nums else 0) or 1))
                elif op == "B":
                    r = min(rows - 1, r + ((nums[0] if nums else 0) or 1))
                elif op == "C":
                    c = min(cols - 1, c + ((nums[0] if nums else 0) or 1))
                elif op == "D":
                    c = max(0, c - ((nums[0] if nums else 0) or 1))
                elif op == "G":
                    c = ((nums[0] if nums else 0) or 1) - 1
                elif op == "K":
                    mode = nums[0] if nums else 0
                    lo, hi = (c, cols) if mode == 0 else (0, c + 1) if mode == 1 else (0, cols)
                    for k in range(lo, hi):
                        grid[r][k] = " "
                elif op == "J":
                    mode = nums[0] if nums else 0
                    if mode in (2, 3):
                        grid = [[" "] * cols for _ in range(rows)]
                    elif mode == 0:
                        for k in range(c, cols):
                            grid[r][k] = " "
                        for line in range(r + 1, rows):
                            grid[line] = [" "] * cols
                i += len(m.group(0))
                continue
            m = re.match(r"\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[=>()][0-9A-Za-z]?", text[i:])
            i += len(m.group(0)) if m else 1
            continue
        if ch == "\r":
            c = 0
        elif ch == "\n":
            r = min(rows - 1, r + 1)
        elif ch == "\b":
            c = max(0, c - 1)
        elif ch >= " ":
            if c < cols:
                grid[r][c] = ch
            c += 1
        i += 1
    return ["".join(line).rstrip() for line in grid]


for line in screen(out):
    print(f"{len(line):3d} |{line}")
```

- [ ] **Step 2: Write the fake providers, the throwaway home and the wrapper**

No real provider is probed: the registry in the throwaway home points ollama and omlx at two scratch ports, where a small script answers the read-only requests wt's inventory makes. Create `/tmp/wt-step3-screens/fake-providers.py`:

```python
"""A fake ollama (port 18434) and a fake omlx (port 18000) for screen captures:
canned answers to the read-only probes wt's inventory makes. Nothing here is a
real provider, and wt is never pointed at one."""
import json, threading
from http.server import BaseHTTPRequestHandler, HTTPServer

OLLAMA = {
    "/api/tags": {"models": [{"name": "gemma4:9b", "size": 5800000000}, {"name": "qwen3:8b", "size": 5200000000}]},
    "/api/ps": {"models": [{"name": "gemma4:9b"}]},
}
OMLX = {
    "/v1/models/status": {"final_ceiling": 64000000000, "current_model_memory": 21000000000, "models": [
        {"id": "Qwen3.8-27B-Instruct-MLX-6bit", "loaded": True, "is_loading": False, "resident_estimated_size": 21000000000},
        {"id": "Qwen3.8-4B-4bit", "loaded": False, "is_loading": False},
    ]},
}


SHOW = {
    "details": {"family": "qwen3", "parameter_size": "8.2B", "quantization_level": "Q4_K_M"},
    "model_info": {"general.architecture": "qwen3", "qwen3.context_length": 40960},
    "capabilities": ["completion", "tools", "vision"],
}


def handler(answers):
    class H(BaseHTTPRequestHandler):
        def do_GET(self):
            body = answers.get(self.path)
            self.send_response(200 if body is not None else 404)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(json.dumps(body if body is not None else {}).encode())

        def do_POST(self):
            # `ollama show` asks POST /api/show; answer for any model.
            self.rfile.read(int(self.headers.get("Content-Length") or 0))
            body = SHOW if self.path == "/api/show" else None
            self.send_response(200 if body is not None else 404)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(json.dumps(body if body is not None else {}).encode())

        def log_message(self, *a):
            pass
    return H


for port, answers in ((18434, OLLAMA), (18000, OMLX)):
    threading.Thread(target=HTTPServer(("127.0.0.1", port), handler(answers)).serve_forever, daemon=True).start()
threading.Event().wait()
```

The home has its two omlx models where a real install has them, `~/.omlx/models`, so the paths on screen are as long as real ones. Create `/tmp/wt-step3-screens/make-home.sh`:

```bash
#!/bin/bash
# Build a throwaway home for screen captures: a registry whose local providers
# are the fake ones (fake-providers.py, ports 18434 and 18000), two omlx model
# directories where a real install has them (~/.omlx/models), and a scratch
# LiteLLM config. Usage: make-home.sh <directory> (the directory is deleted
# and made again; it must be an absolute path, which the registry's model_dir
# is written from).
set -euo pipefail
home="$1"
case "$home" in /*) ;; *) echo "make-home.sh: $home is not an absolute path" >&2; exit 2 ;; esac
rm -rf "$home"
mkdir -p "$home/.config/local-ai" "$home/.config/agent-wt" "$home/.omlx/models/Qwen3.8-27B-Instruct-MLX-6bit" "$home/.omlx/models/Qwen3.8-4B-4bit"
echo '{}' > "$home/.omlx/models/Qwen3.8-27B-Instruct-MLX-6bit/config.json"
echo '{}' > "$home/.omlx/models/Qwen3.8-4B-4bit/config.json"
printf 'model_list: []\n' > "$home/litellm.yaml"
cat > "$home/.config/agent-wt/config.toml" <<'TOML'
default_tag = "code"

[[agents]]
name = "claude"
supported_providers = ["claude", "ollama", "omlx", "openrouter"]
TOML
cat > "$home/.config/local-ai/registry.toml" <<TOML
[[providers]]
id = "ollama"
name = "Ollama"
location = "local"
protocols = ["anthropic", "openai-chat"]

[providers.auth]
type = "none"
base_url = "http://127.0.0.1:18434"

[[providers]]
id = "omlx"
name = "oMLX"
location = "local"
model_dir = "$home/.omlx/models"

[providers.auth]
type = "none"
base_url = "http://127.0.0.1:18000"

[[providers]]
id = "openrouter"
name = "OpenRouter"
location = "cloud"

[providers.auth]
type = "api_key"
secret_ref = "OPENROUTER_API_KEY"
base_url = "https://openrouter.ai/api/v1"

[[providers]]
id = "claude"
name = "Claude"
location = "cloud"

[providers.auth]
type = "native"

[[providers]]
id = "agy"
name = "Agy"
location = "cloud"

[providers.auth]
type = "native"

[[models]]
id = "claude/opus"
family = "opus"
provider_id = "claude"
model_name = "opus"
tags = ["code"]

[[models]]
id = "openrouter/anthropic--claude-sonnet-4.5-thinking"
family = "sonnet"
provider_id = "openrouter"
model_name = "anthropic/claude-sonnet-4.5-thinking"
tags = ["code", "design"]

[models.cost]
input_price_per_million = 3
cache_price_per_million = 0.3
output_price_per_million = 15

[[models]]
id = "omlx/Qwen3.8-27B-Instruct-MLX-6bit"
family = "qwen3.8"
provider_id = "omlx"
model_name = "Qwen3.8-27B-Instruct-MLX-6bit"
tags = ["code"]

[[models]]
id = "omlx/Gone-4bit"
family = "qwen3.8"
provider_id = "omlx"
model_name = "Gone-4bit"
tags = []

[[models]]
id = "ollama/gemma4:9b"
family = "gemma4"
provider_id = "ollama"
model_name = "gemma4:9b"
tags = ["code"]
TOML
```

Create `/tmp/wt-step3-screens/run.sh`:

```bash
#!/bin/bash
# Run a command in the throwaway home that is beside this script, and in no
# other: the environment is emptied first, so nothing of the caller's — HOME,
# XDG_CONFIG_HOME, a registry or LiteLLM override, an API key — reaches wt,
# and wt reaches nothing of the caller's. PATH is the system's alone, so no
# installed provider (ollama, omlx, mtplx) is found. Usage: run.sh <command>
# [args...]
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd -P)"
cd "$here/home"
exec env -i HOME="$here/home" XDG_CONFIG_HOME="$here/home/.config" \
  WT_LITELLM_CONFIG="$here/home/litellm.yaml" WT_LITELLM_RESTART_CMD=true \
  OPENROUTER_API_KEY=sk-not-a-key PATH=/usr/bin:/bin TERM=xterm-256color "$@"
```

- [ ] **Step 3: Build, make the home, and start the fakes**

From the monorepo root:

```bash
(cd wt && go build -o /tmp/wt-step3-screens/wt ./cmd/wt)
chmod +x /tmp/wt-step3-screens/make-home.sh /tmp/wt-step3-screens/run.sh
/tmp/wt-step3-screens/make-home.sh /tmp/wt-step3-screens/home
nohup python3 /tmp/wt-step3-screens/fake-providers.py >/dev/null 2>&1 &
echo $! > /tmp/wt-step3-screens/fakes.pid
```

Check the wrapper: `/tmp/wt-step3-screens/run.sh env`

Expected: these seven variables and no other (`…` is the scratch directory with any link in it resolved; on macOS, `/private/tmp/wt-step3-screens`):

```text
HOME=…/home
XDG_CONFIG_HOME=…/home/.config
WT_LITELLM_CONFIG=…/home/litellm.yaml
WT_LITELLM_RESTART_CMD=true
OPENROUTER_API_KEY=sk-not-a-key
PATH=/usr/bin:/bin
TERM=xterm-256color
```

Check the fakes: `curl -s http://127.0.0.1:18434/api/tags`

Expected: `{"models": [{"name": "gemma4:9b", "size": 5800000000}, {"name": "qwen3:8b", "size": 5200000000}]}`. If a later screen shows `unknown` and `?` on the ollama and omlx rows, the fakes are no longer running (some shells end a background job with the command that started it): run the `nohup` line and the `echo` line again.

- [ ] **Step 4: The Models tab at 80x24**

Run: `python3 /tmp/wt-step3-screens/wt-screen.py --size 80x24 /tmp/wt-step3-screens/run.sh /tmp/wt-step3-screens/wt model`

Expected (here and below: trailing blank lines left out, and without the width the real output prints in front of each line — none is over 80):

```text
 Agents   [Models]

FAMILY   MODEL                                             STATUS   RUNNING

gemma4   ollama/gemma4:9b                                  ok       run
opus     claude/opus                                       ok
qwen3.8  omlx/Gone-4bit                                    missing
qwen3.8  omlx/Qwen3.8-27B-Instruct-MLX-6bit                ok       run
sonnet   openrouter/anthropic--claude-sonnet-4.5-thinking  ok
-        ollama/qwen3:8b                                   new
-        omlx/Qwen3.8-4B-4bit                              new











ollama/gemma4:9b · ok · running · tags code
d remove · r refresh · / filter · tab agents · q quit
```

What to check: the tab bar is the first line with `[Models]` in brackets; the header row is there; SIZE and LOC are not (FAMILY, a 48-column MODEL, STATUS and RUNNING are 75 columns; LOC would make 82); the longest id is whole; the registered omlx model that is not on disk reads `missing`; the two unregistered models read `new`; the last two lines are the selected row's detail — its id, status, running state and tags — and the key hints.

Run: `python3 /tmp/wt-step3-screens/wt-screen.py --size 80x24 --keys '{down}{down}{down}' /tmp/wt-step3-screens/run.sh /tmp/wt-step3-screens/wt model`

Expected: the same table with the cursor on the fourth row, and the detail of a local model, whose path is on a line of its own, whole, from `~`:

```text
 Agents   [Models]

FAMILY   MODEL                                             STATUS   RUNNING

gemma4   ollama/gemma4:9b                                  ok       run
opus     claude/opus                                       ok
qwen3.8  omlx/Gone-4bit                                    missing
qwen3.8  omlx/Qwen3.8-27B-Instruct-MLX-6bit                ok       run
sonnet   openrouter/anthropic--claude-sonnet-4.5-thinking  ok
-        ollama/qwen3:8b                                   new
-        omlx/Qwen3.8-4B-4bit                              new










omlx/Qwen3.8-27B-Instruct-MLX-6bit · ok · running · tags code
~/.omlx/models/Qwen3.8-27B-Instruct-MLX-6bit
d remove · r refresh · / filter · tab agents · q quit
```

- [ ] **Step 5: The filter, and `esc`**

Run: `python3 /tmp/wt-step3-screens/wt-screen.py --size 80x24 --keys '/qwen' /tmp/wt-step3-screens/run.sh /tmp/wt-step3-screens/wt model`

Expected: only the rows that match, best match first, under the filter being typed (it takes the header's line until `enter`):

```text
 Agents   [Models]

Filter: qwen

-        ollama/qwen3:8b                                   new
-        omlx/Qwen3.8-4B-4bit                              new
qwen3.8  omlx/Gone-4bit                                    missing
qwen3.8  omlx/Qwen3.8-27B-Instruct-MLX-6bit                ok       run














ollama/qwen3:8b · new
d remove · r refresh · / filter · tab agents · q quit
```

Run: `python3 /tmp/wt-step3-screens/wt-screen.py --size 80x24 --keys '/qwen{enter}{down}' /tmp/wt-step3-screens/run.sh /tmp/wt-step3-screens/wt model`

Expected: the header is back, the four rows stay, and the detail is that of the second of them — the cursor moves over the filtered rows:

```text
 Agents   [Models]

FAMILY   MODEL                                             STATUS   RUNNING

-        ollama/qwen3:8b                                   new
-        omlx/Qwen3.8-4B-4bit                              new
qwen3.8  omlx/Gone-4bit                                    missing
qwen3.8  omlx/Qwen3.8-27B-Instruct-MLX-6bit                ok       run













omlx/Qwen3.8-4B-4bit · new
~/.omlx/models/Qwen3.8-4B-4bit
d remove · r refresh · / filter · tab agents · q quit
```

Run: `python3 /tmp/wt-step3-screens/wt-screen.py --size 80x24 --keys '{esc}{down}' /tmp/wt-step3-screens/run.sh /tmp/wt-step3-screens/wt model | tail -2`

Expected: wt is still there after `esc` (the driver prints no "the command exited" line), and `down` moved the cursor to the second row:

```text
 28 |claude/opus · ok · tags code
 53 |d remove · r refresh · / filter · tab agents · q quit
```

- [ ] **Step 6: The remove prompt, and the screen after a removal**

Run: `python3 /tmp/wt-step3-screens/wt-screen.py --size 80x24 --keys '{down}{down}{down}d' /tmp/wt-step3-screens/run.sh /tmp/wt-step3-screens/wt model`

Expected:

```text
 Agents   [Models]

Remove omlx/Qwen3.8-27B-Instruct-MLX-6bit from the registry? [y/N]

weights are still at
~/.omlx/models/Qwen3.8-27B-Instruct-MLX-6bit
wt removes the registry entry only; it never deletes weights.
```

Run: `python3 /tmp/wt-step3-screens/wt-screen.py --size 80x24 --keys '{down}{down}{down}dy' /tmp/wt-step3-screens/run.sh /tmp/wt-step3-screens/wt model`

Expected:

```text
 Agents   [Models]

removed omlx/Qwen3.8-27B-Instruct-MLX-6bit; weights are still at
~/.omlx/models/Qwen3.8-27B-Instruct-MLX-6bit
LiteLLM routes pending (sync on quit)

FAMILY   MODEL                                             STATUS   RUNNING

gemma4   ollama/gemma4:9b                                  ok       run
opus     claude/opus                                       ok
qwen3.8  omlx/Gone-4bit                                    missing
sonnet   openrouter/anthropic--claude-sonnet-4.5-thinking  ok
-        ollama/qwen3:8b                                   new
-        omlx/Qwen3.8-27B-Instruct-MLX-6bit                new      run
-        omlx/Qwen3.8-4B-4bit                              new






omlx/Qwen3.8-27B-Instruct-MLX-6bit · new · running
~/.omlx/models/Qwen3.8-27B-Instruct-MLX-6bit
d remove · r refresh · / filter · tab agents · q quit
```

What to check: the prompt shows where the weights are before anything is removed, the path on a line of its own; afterwards the status repeats it the same way and a line says the routes are pending; the removed model is still running, so it is back at once as a `new` row with the cursor on it. Then confirm the write was immediate and the sync was not (the driver kills wt, so it never reached its exit):

```bash
grep -c 'Qwen3.8-27B-Instruct' /tmp/wt-step3-screens/home/.config/local-ai/registry.toml
cat /tmp/wt-step3-screens/home/litellm.yaml
ls /tmp/wt-step3-screens/home/.omlx/models
```

Expected: `0`; `model_list: []`; and both directories still there, `Qwen3.8-27B-Instruct-MLX-6bit` and `Qwen3.8-4B-4bit`.

- [ ] **Step 7: The exit sync**

```bash
/tmp/wt-step3-screens/make-home.sh /tmp/wt-step3-screens/home
python3 /tmp/wt-step3-screens/wt-screen.py --size 80x24 --keys 'q' /tmp/wt-step3-screens/run.sh /tmp/wt-step3-screens/wt model >/dev/null
cat /tmp/wt-step3-screens/home/litellm.yaml
```

Expected: `model_list: []` — quit with nothing changed, and no sync ran.

```bash
python3 /tmp/wt-step3-screens/wt-screen.py --size 80x24 --keys '{down}{down}{down}dyq' /tmp/wt-step3-screens/run.sh /tmp/wt-step3-screens/wt model >/dev/null
grep -o 'model_name: [^,]*' /tmp/wt-step3-screens/home/litellm.yaml
```

Expected: remove, then quit, and the file lists four routes:

```text
model_name: openrouter/anthropic--claude-sonnet-4.5-thinking
model_name: 'ollama/gemma4:9b'
model_name: 'ollama/qwen3:8b'
model_name: omlx/Qwen3.8-27B-Instruct-MLX-6bit
```

The driver sends `y` and `q` 0.4 seconds apart, so this run does not show what happens when they arrive together; `TestQuitWaitsForARegistryWriteInFlight` pins that. (With the keys a millisecond apart, six fresh homes out of six were synced when this plan was written.)

- [ ] **Step 8: 40x12, and the Agents tab**

Run: `/tmp/wt-step3-screens/make-home.sh /tmp/wt-step3-screens/home && python3 /tmp/wt-step3-screens/wt-screen.py --size 40x12 /tmp/wt-step3-screens/run.sh /tmp/wt-step3-screens/wt model`

Expected: twelve lines. FAMILY has gone too; STATUS and RUNNING have not; three rows are on screen, with the page dots under them; the hints are the short ones:

```text
 Agents   [Models]

MODEL                STATUS   RUNNING

ollama/gemma4:9b     ok       run
claude/opus          ok
omlx/Gone-4bit       missing

  •••
ollama/gemma4:9b · ok · running
tags code
d · r · / · tab · q quit
```

Run: `python3 /tmp/wt-step3-screens/wt-screen.py --size 40x12 --keys '{down}{down}{down}' /tmp/wt-step3-screens/run.sh /tmp/wt-step3-screens/wt model`

Expected: the fourth row's id does not fit beside STATUS and RUNNING, so it has lost its middle; the detail under the table has it whole. The path is not shown: it would take two more lines from the table.

```text
 Agents   [Models]
MODEL                STATUS   RUNNING

ollama/gemma4:9b     ok       run
claude/opus          ok
omlx/Gone-4bit       missing
omlx/Qwen3.8…X-6bit  ok       run

  ••
omlx/Qwen3.8-27B-Instruct-MLX-6bit · ok
running · tags code
d · r · / · tab · q quit
```

Run: `python3 /tmp/wt-step3-screens/wt-screen.py --size 40x12 --keys '{down}{down}{down}d' /tmp/wt-step3-screens/run.sh /tmp/wt-step3-screens/wt model`

Expected: the prompt wraps between words; the path is cut only at the edge:

```text
 Agents   [Models]

Remove
omlx/Qwen3.8-27B-Instruct-MLX-6bit from
the registry? [y/N]

weights are still at
~/.omlx/models/Qwen3.8-27B-Instruct-MLX-
6bit
wt removes the registry entry only; it
never deletes weights.
```

Run: `python3 /tmp/wt-step3-screens/wt-screen.py --size 40x12 --keys '{down}{down}{down}dy' /tmp/wt-step3-screens/run.sh /tmp/wt-step3-screens/wt model`

Expected: right after the removal the status has the room, and the table is down to its header and one row:

```text
 Agents   [Models]
removed
omlx/Qwen3.8-27B-Instruct-MLX-6bit;
weights are still at
~/.omlx/models/Qwen3.8-27B-Instruct-MLX-
6bit
LiteLLM routes pending (sync on quit)
MODEL                STATUS   RUNNING

omlx/Qwen3.8…X-6bit  new      run

  •••••••
```

Run: `/tmp/wt-step3-screens/make-home.sh /tmp/wt-step3-screens/home && python3 /tmp/wt-step3-screens/wt-screen.py --size 40x12 --keys '{down}{down}{down}dy{up}' /tmp/wt-step3-screens/run.sh /tmp/wt-step3-screens/wt model`

Expected: one key later the status is gone, the pending note stays on one line, and the table has its rows and its hints back:

```text
 Agents   [Models]
LiteLLM routes pending (sync on quit)
MODEL                STATUS   RUNNING

ollama/qwen3:8b      new
omlx/Qwen3.8…X-6bit  new      run
omlx/Qwen3.8…B-4bit  new


  ••
ollama/qwen3:8b · new
d · r · / · tab · q quit
```

Run: `/tmp/wt-step3-screens/make-home.sh /tmp/wt-step3-screens/home && python3 /tmp/wt-step3-screens/wt-screen.py --size 80x24 /tmp/wt-step3-screens/run.sh /tmp/wt-step3-screens/wt config`

Expected: the tab bar where the title was, with `[Agents]` current, and `q quit` and `tab models` in the help line; the rest of the tab is as it was:

```text
[Agents]   Models

   Agents

shell  (-)  command


agy  (agy)  ✗ not installed
not installed — install the binary

claude  (claude, ollama, omlx, openrouter)  ✗ not installed
not installed — install the binary

codex  (-)  ✗ not configured
not configured — add it to config.toml

copilot  (-)  ✗ not configured
not configured — add it to config.toml

  ••

  ↑/k up • ↓/j down • / filter • q quit • tab models • ? more
```

Run: `python3 /tmp/wt-step3-screens/wt-screen.py --size 80x24 --keys '{esc}{tab}' /tmp/wt-step3-screens/run.sh /tmp/wt-step3-screens/wt config | head -1`

Expected: ` 18 | Agents   [Models]` — `esc` on the Agents list did not end wt, and `tab` then switched.

If any screen differs from these in a way the checks above name, stop and fix the code before going on; if it differs only in what the fixture cannot fix (nothing should, with the environment emptied), say so in the hand-off.

- [ ] **Step 9: Clean up**

```bash
kill "$(cat /tmp/wt-step3-screens/fakes.pid)"
rm -rf /tmp/wt-step3-screens
```

- [ ] **Step 10: Document the tab**

In `wt/docs/wt-config.md`, find:

```text
- **`wt config`** (no subcommand) — interactive TUI for viewing and editing
  agents in `config.toml`
```

and replace it with:

```text
- **`wt config`** (no subcommand) — interactive TUI with two tabs: the agents
  in `config.toml`, and the models in `registry.toml`
```

In `wt/docs/wt-config.md`, find:

```text
Launching `wt config` with no subcommand opens a full-screen TUI that
lets you browse and edit the **Agents** section of `config.toml`.
Providers and models live in `registry.toml`, which `wt config` never
writes (`wt model init` creates it and adds provider rows; models are
edited by hand for now, then `wt litellm sync` — modelman's TUI is disabled).
```

and replace it with:

```text
Launching `wt config` with no subcommand opens a full-screen TUI with two
tabs, named on its first line; `Tab` switches between them.

- **Agents** — the agents in `config.toml`. Edits are held in memory and
  saved with `Ctrl+S`, from this tab. The sections below describe it.
- **Models** — the models in `registry.toml`: every registry model and every
  local model found on this machine. Each change is written at once, and the
  LiteLLM routes are synced once when you quit. `wt model` opens the editor
  on this tab; its keys and columns are in [`wt-model.md`](wt-model.md).
```

In `wt/docs/wt-config.md`, find:

```text
- `c` or `Esc` — return to the list
```

and replace it with:

```text
- `c` or `Esc` — return to the list

The prompt is about agent edits only: a change made on the Models tab is
already in `registry.toml` when it is made.

`Esc` on either tab's list does not quit: it clears a filter (`/`) and does
nothing else. Only `q` and `Ctrl+C` leave the editor.
```

In `wt/docs/wt-model.md`, find:

````text
```bash
wt model list [--json]     # every registry model
````

and replace it with:

````text
```bash
wt model                    # the Models tab of `wt config` (needs a terminal)
wt model list [--json]     # every registry model
````

In `wt/docs/wt-model.md`, add at the end of the file (after one blank line):

```text
## The Models tab (`wt model`, or `Tab` in `wt config`)

The same rows as `wt model list`, as a table: FAMILY, MODEL, LOC, STATUS,
RUNNING, SIZE. On a narrow terminal it gives up SIZE, then LOC, then FAMILY —
never MODEL, STATUS or RUNNING. When an id is still too long for the MODEL
column it loses its middle to an ellipsis
(`omlx/Qwen3.8-35B-A3B-Instruct-abliterat…-dynamic-quant-6bit`): the start
says which provider, the end which variant.

Under the table is the selected row's detail: its id, whole, then its status,
`running` / `loading` / `running?`, and its tags (for a pairing, its target
and draft); and the path of its weights on a line of its own, written from
`~`. A short terminal drops the path before it drops table rows.

| Key | Does |
|---|---|
| `↑`/`↓`, `j`/`k` | move |
| `d` | remove the selected registry model, after a `y/N` prompt that shows where its weights are; the path is repeated in the status afterwards |
| `r` | probe the providers again |
| `/` | filter by id or family: type, `Enter` to keep the filter, `Esc` to clear it |
| `Tab` | the Agents tab |
| `q`, `Ctrl+C` | quit |

`Esc` does not quit, and `Ctrl+S` does nothing here: there is nothing to save.
The providers are probed when the tab is first shown, not on every visit;
`Tab` and `q` work while that first probe is still out.

A removal is written to `registry.toml` at once. The LiteLLM routes are not
synced per change: the status says `LiteLLM routes pending (sync on quit)`,
and that one sync runs when the editor closes, only if the registry changed. A
quit typed while a change is still being written waits for it. If wt is
killed before the sync, the next `wt start`, `wt stop` or launch through
LiteLLM repairs the routes, and `wt litellm sync` does it at once. A sync that
leaves `config.yaml` unchanged does not restart the proxy.

What the last action said stays above the table until the next key.
```

In `wt/CLAUDE.md`, find:

```text
| `internal/configeditor/` | Bubble Tea forms behind `wt config`'s interactive editor |
```

and replace it with:

```text
| `internal/configeditor/` | the TUI behind `wt config`: the Agents tab (`config.toml`; edits buffered, saved with ctrl+s) and the Models tab (`registry.toml` through `internal/modeladmin`; each change written at once from a `tea.Cmd`, the probe in a `tea.Cmd` too). `Run` reports `Result.RegistryChanged`, and `cmd/wt`'s `runConfigEditor` then runs the one route sync the tab owes; a quit asked for while a write is in flight waits for it (`leave`, `quitPending`), so the result is never "unchanged" for a registry that changed |
```

In `wt/CLAUDE.md`, find:

```text
wt model list [--json]               # every registry model and every local model found, with live status
```

and replace it with:

```text
wt model                             # `wt config` on its Models tab (needs TTY)
wt model list [--json]               # every registry model and every local model found, with live status
```

In `wt/docs/internals/tui.md`, find:

```text
> **TTY required.** `WithAltScreen` opens `/dev/tty`
```

and replace it with:

```text
> **`wt config` is a second Bubble Tea program** (`internal/configeditor`), with the same two rules. Its Models tab draws a `tuilayout.Columns` table through `modelsFrames` and is sized by `tuilayout.FitTo` after every message (`fitModels`), which then cuts the MODEL column — and with it the middle of the ids longer than it (`middleCut`) — when the three columns that are never dropped do not fit; `modelsFrames` gives up, in order, the blank lines, the selected row's path, table rows down to one, the key hints and the id line. Free text is wrapped by `flow`, between words only (lipgloss also breaks at a hyphen, which is inside every model path); the remove prompt wraps its text to the terminal's width. Nothing on the tab reads a file or dials a server in `Update`: the probe (`probeCmd`) and every registry write are commands, and a probe's result carries a generation number so that a late one cannot replace a later one's rows. A bubbles list sends its filter's result back as a message that does not name the list; every command either tab's list returns goes through `tagFilter`, and the result comes back as a `filterMatchesMsg` for the right one — an editor that drops it has a `/` that filters nothing. Both lists have bubbles' own quit keys off (`DisableQuitKeybindings`): `esc` is one of them, and the list's quit goes round the editor's. The tab reaches the machine only through `Options.Models` (`ModelsDeps`: load, probe, seeding environment, ollama lookup), which `cmd/wt` fills from its own seams and a test fills with a machine of its own. `TestModelsTabFitsTheTerminal` measures it at widths 40/80/120 and heights 12/24/50.

> **TTY required.** `WithAltScreen` opens `/dev/tty`
```

In `wt/docs/internals/testing.md`, find:

```text
`internal/modeladmin` writes the registry in its tests, so it has an isolating `TestMain`, which also
```

and replace it with:

```text
`internal/modeladmin` and `internal/configeditor` (whose Models tab writes the registry) write it in their tests, so each has an isolating `TestMain` (configeditor's also names the home directory the tab abbreviates paths against, the `userHome` seam); modeladmin's also
```

In `wt/CHANGELOG.md`, find:

```text
## Unreleased

### Added
```

and replace it with:

```text
## Unreleased

### Added

- `wt config` has a second tab, Models (`Tab` switches; `wt model` opens the
  editor on it): every registry model and every local model found, with
  status and running state. `d` removes the selected model from the registry
  after a prompt that shows where its weights are; `r` probes again; `/`
  filters. A change is written at once, and the LiteLLM routes are synced
  once when the editor closes.
```

In `wt/CHANGELOG.md`, find:

```text
### Fixed

- `wt litellm sync`, `status`, `on`, `off` and `set` name the repair that
```

and replace it with:

```text
### Fixed

- `wt config`: `/` on the Agents tab filters the list (it showed `Filter: …`
  above every agent), and `Esc` on the list no longer ends the editor without
  the unsaved-changes prompt. Only `q` and `Ctrl+C` quit.
- `wt litellm sync`, `status`, `on`, `off` and `set` name the repair that
```

- [ ] **Step 11: Verify the PR and commit**

From `wt/`:

```bash
test -z "$(gofmt -l .)" && go vet ./... && go test -count=1 ./... && make check
```

Expected: every package `ok`; `make check` ends without an error. From the monorepo root: `make test-all && make check-links`. Expected: exit 0.

```bash
git add wt/docs wt/CLAUDE.md wt/CHANGELOG.md
git commit -m "docs(wt): the Models tab of wt config"
```

- [ ] **Step 12: Hand off**

Stop here. Tell the owner the branch is ready, what `make test-all` printed, and what Steps 4 to 8 showed, with the captured screens. Say what was not looked at: a terminal with colour (the captures are text), and a real provider. Say also that the Agents tab changed in two ways a user can see: `esc` on its list no longer quits, and `/` now filters. Push and open the PR only after the owner's OK. Suggested title: `feat(wt): a Models tab in wt config`.

---

## PR F — the form

Branch `feat/wt-models-tab-form`. Needs PR E on `main`. With this PR the tab can add, register and edit a model, so it is also the PR that tells users: the notice bare `modelman` prints, and the guides Step 2 rewrote to "edit `registry.toml` by hand", change here.

The spec's risk note: modelman's form is about 850 lines with 100 tests; this one is smaller (no Hugging Face parsing, no dual-model inputs) but is the largest new UI in wt and must fit 80x24. It is built like the agent form — `bubbles/textinput` fields, `renderFormFields` — with two additions: fields with a fixed set of values are choices, and the field rows scroll when the terminal is shorter than the form, with a marker row for the fields off the screen.

Create the branch from the monorepo root:

```bash
git fetch origin
git switch -c feat/wt-models-tab-form origin/main
```

### Task 14: The add, register and edit form

**Files:**
- Create: `wt/internal/configeditor/models_form_test.go`
- Modify: `wt/internal/configeditor/models_tab_test.go` (`keys` goes through a new `sendKey`)
- Create: `wt/internal/configeditor/models_form.go`
- Modify: `wt/internal/configeditor/models_tab.go` (`modelsForm` phase, `modelsTab.form`, `n` and `enter`, the two forms of the hints, `fitModels`), `wt/internal/configeditor/editor.go` (`modelSavedMsg`; resize)

**Interfaces:**
- Consumes: Task 11's `model`, `modelsTab` (its `writing` flag), `modelsPhase`, `probeCmd`, `selectedModel`, `quit`, `model.quitPending`, `tabBar`, `modelsHints`, `fitHints`, `wrapText`, `middleCut`, `Options.Models` (`SeedEnv`, `Capabilities`) and test helpers (`tabMachine`, `modelsEditor`, `selectModel`, `keys`, `keyMsg`, `send`, `findRow`, `flat`, `assertFits`, `tabRegistry`); Task 7's `modeladmin.Add`, `CheckAdd`, `Edit`, `AddRequest`, `Fields`, `FieldError`, the `Field…` names, `DeriveID`, `UsesOllama`; existing in `configeditor`: `newTextInput(value, placeholder) textinput.Model`, `renderFormFields(theme, fields []formField) string`, `formField{label, value string; focus bool}`; `tui.ErrorStyle(theme)`, `tuilayout.Clip`
- Produces:
  - `modelsForm`, a third `modelsPhase`; `modelsTab.form *modelForm`
  - field indexes `mfProvider`, `mfName`, `mfFamily`, `mfTags`, `mfLocation`, `mfInput`, `mfCache`, `mfOutput`, `mfSubPrice`, `mfSubPeriod`, `mfCount`; `modelFormLabels`
  - `type modelForm` with `mode` (`modelFormAdd`, `modelFormRegister`, `modelFormEdit`), `id`, `providers`, `cursor`, `err`, `saving`, and the methods `value(field int) string`, `editable(field int) bool`
  - `func (m *model) openModelForm(row *modeladmin.Row)` — nil: add; an unregistered row: register; a registry row: edit
  - `modelSavedMsg{id string; changed bool; notes []string; err error}`, handled by `applyModelSaved`, which also issues a quit that was waiting for the save
  - `var modelFormHints []string` (fullest first); `func formWindow(cursor, n, room int) (first, count int, above, below bool)`; `func endCut(s string, w int) string`; `func (m *model) modelFieldRoom(field int) int`
  - keys on the list: `n` (add), `enter` (edit, or register on a `new` row)
  - test helpers `typeText(t, m, s) *model`, `moveTo(t, m, field) *model`, `sendKey(t, m, msg) *model`

- [ ] **Step 1: Write the failing tests**

Create `wt/internal/configeditor/models_form_test.go`:

```go
package configeditor

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// typeText sends s to the focused field one character at a time.
func typeText(t *testing.T, m *model, s string) *model {
	t.Helper()
	for _, r := range s {
		m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return m
}

// moveTo moves the form's cursor down to field.
func moveTo(t *testing.T, m *model, field int) *model {
	t.Helper()
	for i := 0; i < mfCount && m.models.form.cursor != field; i++ {
		m = keys(t, m, "down")
	}
	if m.models.form.cursor != field {
		t.Fatalf("the cursor cannot reach field %d (%s)", field, modelFormLabels[field])
	}
	return m
}

// TestModelFormFitsTheTerminal measures the form at every size wt supports,
// in each of its three modes, with the cursor on every field it can reach
// and with an error showing. The view must stay inside the terminal with the
// tab bar and the focused field on screen: on a 12-line terminal the ten
// fields do not all fit, and a form that let Bubble Tea cut its top would
// hide the field being typed into.
func TestModelFormFitsTheTerminal(t *testing.T) {
	modes := []struct {
		name string
		open func(t *testing.T, m *model) *model
	}{
		{"add", func(t *testing.T, m *model) *model { return keys(t, m, "n") }},
		{"edit", func(t *testing.T, m *model) *model {
			return keys(t, selectModel(t, m, "openrouter/anthropic--claude-sonnet-4.5-thinking"), "enter")
		}},
		{"register", func(t *testing.T, m *model) *model { return keys(t, selectModel(t, m, "ollama/qwen3:8b"), "enter") }},
	}
	const longError = `input-price must be a number, got "a price that somebody pasted a whole sentence into by mistake"`
	for _, width := range []int{40, 80, 120} {
		for _, height := range []int{12, 24, 50} {
			for _, mode := range modes {
				tm := newTabMachine(t, tabRegistry)
				m := mode.open(t, modelsEditor(t, tm, width, height))
				if m.models.phase != modelsForm {
					t.Fatalf("%s: the form did not open", mode.name)
				}
				for field := 0; field < mfCount; field++ {
					if !m.models.form.editable(field) {
						continue
					}
					m = moveTo(t, m, field)
					for _, withError := range []bool{false, true} {
						m.models.form.err = ""
						if withError {
							m.models.form.err = longError
						}
						name := mode.name + " form on " + modelFormLabels[field]
						view := m.View()
						assertFits(t, name, view, width, height)
						if !strings.Contains(view, "[Models]") {
							t.Errorf("%s at %dx%d: the tab bar is not on screen:\n%s", name, width, height, view)
						}
						if !strings.Contains(view, "> "+modelFormLabels[field]+":") {
							t.Errorf("%s at %dx%d: the focused field is not on screen:\n%s", name, width, height, view)
						}
						if withError && !strings.Contains(view, "input-price must be a number") {
							t.Errorf("%s at %dx%d: the error is not on screen:\n%s", name, width, height, view)
						}
					}
				}
			}
		}
	}
}

// TestModelFormAddWritesAtOnce verifies n opens an empty form, and ctrl+s
// writes the model to the registry there and then — in a command, not in
// Update — goes back to the table with the cursor on the new row, and marks
// the routes pending. There is no second "save" for models: a user who quits
// after the form closes has not lost the model.
func TestModelFormAddWritesAtOnce(t *testing.T) {
	tm := newTabMachine(t, tabRegistry)
	m := keys(t, modelsEditor(t, tm, 80, 24), "n")
	f := m.models.form
	if f.mode != modelFormAdd || f.value(mfProvider) != "ollama" || f.cursor != mfProvider {
		t.Fatalf("an add should open on the provider, set to ollama; got %q, cursor %d", f.value(mfProvider), f.cursor)
	}
	// ollama, omlx, openrouter and the seedable mtplx; right three times is
	// openrouter.
	if got := strings.Join(f.providers, ","); got != "mtplx,ollama,omlx,openrouter" {
		t.Errorf("provider choices = %s", got)
	}
	m = keys(t, m, "right", "right")
	if got := m.models.form.value(mfProvider); got != "openrouter" {
		t.Fatalf("after right, right the provider is %q, want openrouter", got)
	}
	m = typeText(t, keys(t, m, "down"), "qwen/qwen3.8-27b")
	m = typeText(t, keys(t, m, "down"), "qwen-next")
	m = typeText(t, keys(t, m, "down"), "code, design")
	m = typeText(t, moveTo(t, m, mfInput), "0.5")
	m = typeText(t, moveTo(t, m, mfOutput), "2")

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m = next.(*model)
	if cmd == nil || tm.text(t) != tabRegistry || !strings.Contains(m.View(), "(saving...)") {
		t.Fatal("ctrl+s must hand the write to a command and say it is saving; nothing is written by Update itself")
	}
	m = send(t, m, cmd())
	want := tabRegistry + `
[[models]]
id = "openrouter/qwen--qwen3.8-27b"
family = "qwen-next"
provider_id = "openrouter"
model_name = "qwen/qwen3.8-27b"
tags = [
    "code",
    "design",
]

[models.cost]
input_price_per_million = 0.5
output_price_per_million = 2.0
`
	if got := tm.text(t); got != want {
		t.Errorf("registry after the add =\n%s\nwant\n%s", got, want)
	}
	if m.models.phase != modelsList || !m.registryChanged {
		t.Errorf("phase = %d, registryChanged = %v; want the table and the change recorded", m.models.phase, m.registryChanged)
	}
	if r, _ := m.selectedModel(); r.ID != "openrouter/qwen--qwen3.8-27b" {
		t.Errorf("the cursor is on %q, want the model just added", r.ID)
	}
	if view := strings.Join(strings.Fields(m.View()), " "); !strings.Contains(view, "added openrouter/qwen--qwen3.8-27b") || !strings.Contains(view, "LiteLLM routes pending") {
		t.Errorf("the status should name the model and the pending routes:\n%s", m.View())
	}
}

// TestModelFormEditSendsOnlyWhatChanged verifies an edit opens with the
// row's own values, that saving it untouched writes nothing at all, and that
// changing one field changes that key alone. A form that wrote back every
// field would add `tags = []` to rows that had none and turn integer prices
// into floats on every save.
func TestModelFormEditSendsOnlyWhatChanged(t *testing.T) {
	tm := newTabMachine(t, tabRegistry)
	m := keys(t, selectModel(t, modelsEditor(t, tm, 80, 24), "openrouter/anthropic--claude-sonnet-4.5-thinking"), "enter")
	f := m.models.form
	if f.mode != modelFormEdit || f.cursor != mfFamily {
		t.Fatalf("an edit should open on Family (provider and name are fixed); cursor = %d", f.cursor)
	}
	for field, want := range map[int]string{mfProvider: "openrouter", mfName: "anthropic/claude-sonnet-4.5-thinking", mfFamily: "sonnet", mfTags: "code",
		mfLocation: "", mfInput: "3", mfCache: "", mfOutput: "15", mfSubPrice: "", mfSubPeriod: ""} {
		if got := f.value(field); got != want {
			t.Errorf("%s opens as %q, want %q", modelFormLabels[field], got, want)
		}
	}
	view := m.View()
	if !strings.Contains(view, "Edit openrouter/anthropic--claude-sonnet-4.5-thinking") || !strings.Contains(view, "  Provider: openrouter") {
		t.Errorf("the edit form should show the id and the fixed provider:\n%s", view)
	}

	m = keys(t, m, "ctrl+s")
	if tm.text(t) != tabRegistry || m.registryChanged || !strings.Contains(m.View(), "no change to openrouter/anthropic--claude-sonnet-4.5-thinking") {
		t.Fatalf("saving an untouched form must write nothing and say so:\n%s", m.View())
	}

	m = keys(t, m, "enter")
	m = typeText(t, moveTo(t, m, mfTags), ",design")
	m = keys(t, moveTo(t, m, mfLocation), "right", "right") // (the provider's) -> local -> cloud
	m = keys(t, m, "ctrl+s")
	want := strings.Replace(tabRegistry,
		"model_name = \"anthropic/claude-sonnet-4.5-thinking\"\ntags = [\n    \"code\",\n]\n",
		"model_name = \"anthropic/claude-sonnet-4.5-thinking\"\nlocation = \"cloud\"\ntags = [\n    \"code\",\n    \"design\",\n]\n", 1)
	if got := tm.text(t); got != want {
		t.Errorf("registry after the edit =\n%s\nwant only tags and location changed", got)
	}
	if !m.registryChanged || !strings.Contains(m.View(), "saved openrouter/anthropic--claude-sonnet-4.5-thinking") {
		t.Errorf("the edit should be recorded and reported:\n%s", m.View())
	}
}

// TestModelFormRegistersADiscoveredModelUnderItsOwnId verifies enter on a
// `new` row opens the form with the provider and the name fixed, and that
// saving registers the model under the id it already had as a discovered
// row — the id its routes, usage history and rotation already use — with the
// capabilities ollama reports.
func TestModelFormRegistersADiscoveredModelUnderItsOwnId(t *testing.T) {
	tm := newTabMachine(t, tabRegistry)
	deps := tm.deps()
	asked := ""
	deps.Capabilities = func(_ *config.Config, name string) (map[string]any, error) {
		asked = name
		return map[string]any{"supports_function_calling": true}, nil
	}
	m := modelsEditor(t, tm, 80, 24)
	m.opts.Models = deps
	m = keys(t, selectModel(t, m, "ollama/qwen3:8b"), "enter")
	f := m.models.form
	if f.mode != modelFormRegister || f.editable(mfProvider) || f.editable(mfName) || f.cursor != mfFamily {
		t.Fatalf("registering should fix the provider and the name and open on Family; cursor = %d", f.cursor)
	}
	if !strings.Contains(m.View(), "Register ollama/qwen3:8b") {
		t.Errorf("the form should name the id it will register:\n%s", m.View())
	}
	m = keys(t, typeText(t, m, "qwen3"), "ctrl+s")
	got := tm.text(t)
	for _, want := range []string{"[[providers]]\nid = \"ollama\"", "id = \"ollama/qwen3:8b\"\nfamily = \"qwen3\"\nprovider_id = \"ollama\"\nmodel_name = \"qwen3:8b\"", "[models.model_info]\nsupports_function_calling = true"} {
		if !strings.Contains(got, want) {
			t.Errorf("the registry should now hold %q:\n%s", want, got)
		}
	}
	if asked != "qwen3:8b" {
		t.Errorf("ollama was asked about %q, want qwen3:8b", asked)
	}
	if !strings.Contains(m.View(), "added ollama/qwen3:8b\nadded provider ollama\n") {
		t.Errorf("the status should name the model and the provider row seeded with it:\n%s", m.View())
	}
	if r, _ := findRow(m, "ollama/qwen3:8b"); !r.row.Registered {
		t.Error("after the re-probe the row should be a registry row")
	}
}

// TestModelFormRefusalStaysOnTheFieldAtFault verifies a save the rules
// refuse writes nothing, keeps the form open with what was typed, shows why,
// and puts the cursor on the field to fix. A failed ollama lookup, by
// contrast, does not refuse the add: it is noted on the status line.
func TestModelFormRefusalStaysOnTheFieldAtFault(t *testing.T) {
	tm := newTabMachine(t, tabRegistry)
	m := keys(t, modelsEditor(t, tm, 80, 24), "n")
	m = typeText(t, moveTo(t, m, mfName), "tiny:1b")
	m = typeText(t, moveTo(t, m, mfFamily), "tiny")
	m = typeText(t, moveTo(t, m, mfCache), "cheap")
	m = keys(t, moveTo(t, m, mfSubPeriod), "ctrl+s")
	f := m.models.form
	if m.models.phase != modelsForm || f.cursor != mfCache || !strings.Contains(m.View(), `cache-price must be a number, got "cheap"`) {
		t.Fatalf("a bad price should keep the form open on Cache $/M with the reason; cursor = %d\n%s", f.cursor, m.View())
	}
	if tm.text(t) != tabRegistry || m.registryChanged || f.value(mfName) != "tiny:1b" {
		t.Error("a refused save must write nothing and keep what was typed")
	}

	// A missing name, with the cursor elsewhere: the cursor goes to the name.
	m = keys(t, m, "esc", "n", "down", "down")
	m = keys(t, typeText(t, m, "f"), "ctrl+s")
	if m.models.form.cursor != mfName || !strings.Contains(m.View(), "a model name is required") {
		t.Errorf("a missing name should move the cursor to Model name:\n%s", m.View())
	}

	// The lookup fails (tabMachine's Capabilities is not stubbed): the model
	// is still added, and the status says what is missing.
	m = keys(t, typeText(t, moveTo(t, m, mfName), "tiny:1b"), "ctrl+s")
	if !strings.Contains(tm.text(t), `id = "ollama/tiny:1b"`) || strings.Contains(tm.text(t), "model_info") {
		t.Errorf("a failed lookup must still add the model, without model_info:\n%s", tm.text(t))
	}
	if !strings.Contains(m.View(), "added ollama/tiny:1b\nadded without tool/vision capabilities: not stubbed\nadded provider ollama\n") {
		t.Errorf("the status should say the capabilities were not recorded:\n%s", m.View())
	}

	// The usual reason, ollama not installed, is said in a few words: the
	// whole error is three lines of a 40-column terminal.
	m.opts.Models.Capabilities = func(*config.Config, string) (map[string]any, error) {
		return nil, fmt.Errorf("`ollama show x` failed: %w", exec.ErrNotFound)
	}
	m = keys(t, m, "n")
	m = typeText(t, moveTo(t, m, mfName), "other:1b")
	m = keys(t, typeText(t, moveTo(t, m, mfFamily), "f"), "ctrl+s")
	if !strings.Contains(m.View(), "added without tool/vision capabilities: ollama is not installed\n") {
		t.Errorf("want the short note for a missing ollama:\n%s", m.View())
	}
}

// TestModelFormRefusesBeforeItAsksOllama verifies a save that is going to be
// refused for a value does not run the ollama lookup first: with the daemon
// down the lookup takes its whole timeout, and the user would wait ten
// seconds to be told a price is not a number.
func TestModelFormRefusesBeforeItAsksOllama(t *testing.T) {
	tm := newTabMachine(t, tabRegistry)
	m := modelsEditor(t, tm, 80, 24)
	asked := 0
	m.opts.Models.Capabilities = func(*config.Config, string) (map[string]any, error) { asked++; return nil, nil }
	m = keys(t, m, "n")
	m = typeText(t, moveTo(t, m, mfName), "tiny:1b")
	m = typeText(t, moveTo(t, m, mfFamily), "tiny")
	m = keys(t, typeText(t, moveTo(t, m, mfInput), "cheap"), "ctrl+s")
	if asked != 0 || m.models.phase != modelsForm || !strings.Contains(m.View(), "input-price must be a number") {
		t.Errorf("lookups = %d, want the price refused with none run:\n%s", asked, m.View())
	}
}

// TestQuitWaitsForAFormSaveInFlight is TestQuitWaitsForARegistryWriteInFlight
// for the form, where the window is wider: an add of an ollama model runs
// `ollama show` before it writes. ctrl+c while the save is out must not end
// the program before the save has reported; a refused save calls the quit
// off and stays on the form.
func TestQuitWaitsForAFormSaveInFlight(t *testing.T) {
	// saving fills the add form and presses ctrl+s, handing back the save,
	// not run.
	saving := func(t *testing.T, price string) (*tabMachine, *model, tea.Cmd) {
		tm := newTabMachine(t, tabRegistry)
		m := keys(t, modelsEditor(t, tm, 80, 24), "n", "right", "right") // ollama -> omlx -> openrouter
		m = typeText(t, moveTo(t, m, mfName), "qwen/qwen3.8-27b")
		m = typeText(t, moveTo(t, m, mfFamily), "qwen3.8")
		m = typeText(t, moveTo(t, m, mfInput), price)
		next, save := m.Update(keyMsg("ctrl+s"))
		if save == nil {
			t.Fatal("ctrl+s should hand the save to a command")
		}
		return tm, next.(*model), save
	}
	tm, m, save := saving(t, "0.5")
	next, cmd := m.Update(keyMsg("ctrl+c"))
	m = next.(*model)
	if cmd != nil || !m.quitPending {
		t.Fatalf("ctrl+c with the save in flight: a command = %v, quitPending = %v; want the quit held back", cmd != nil, m.quitPending)
	}
	next, cmd = m.Update(save())
	m = next.(*model)
	if cmd == nil || !m.registryChanged || !strings.Contains(tm.text(t), `id = "openrouter/qwen--qwen3.8-27b"`) {
		t.Fatalf("once the save is in: a command = %v, registryChanged = %v; want the quit, with the change recorded", cmd != nil, m.registryChanged)
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("the command after a held quit should be the quit, not a re-probe")
	}

	tm, m, save = saving(t, "cheap")
	next, _ = m.Update(keyMsg("ctrl+c"))
	next, cmd = next.(*model).Update(save())
	m = next.(*model)
	if cmd != nil || m.quitPending || m.models.phase != modelsForm || !strings.Contains(m.View(), "input-price must be a number") || tm.text(t) != tabRegistry {
		t.Errorf("a refused save has to be read: want no quit and the form still open with the reason:\n%s", m.View())
	}
}

// TestModelFormShowsWhatDoesNotFit verifies nothing in the form is cut
// without a sign of it on a 40-column, 12-line terminal: the fields that have
// scrolled off are counted above and below, the title wraps, a fixed value
// keeps both its ends, a field that is not being edited shows the start of
// its value with an ellipsis, and the key hints are the short ones, whole.
func TestModelFormShowsWhatDoesNotFit(t *testing.T) {
	tm := newTabMachine(t, tabRegistry)
	m := keys(t, modelsEditor(t, tm, 40, 12), "n")
	view := m.View()
	if !strings.Contains(view, "> Provider: < ollama >") || !strings.Contains(view, "  ↓ 2 more") || strings.Contains(view, "Subscription") {
		t.Errorf("at the top of the add form the two fields below should be counted:\n%s", view)
	}
	if !strings.Contains(view, "^s save · esc · ↑/↓ · ←/→ change") {
		t.Errorf("the short key hints should be whole at 40 columns:\n%s", view)
	}
	m = typeText(t, moveTo(t, m, mfName), "Qwen3.8-35B-A3B-Instruct-abliterated-heretic-MLX-6bit")
	m = moveTo(t, m, mfSubPeriod)
	view = m.View()
	if !strings.Contains(view, "  ↑ 2 more") || !strings.Contains(view, "> Subscription period: < (none) >") || strings.Contains(view, "Provider") {
		t.Errorf("at the bottom the two fields above should be counted:\n%s", view)
	}
	m = moveTo(t, m, mfFamily)
	if view = m.View(); !strings.Contains(view, "  Model name: Qwen3.8-35B-A3B-Instruct-…") {
		t.Errorf("a field that is not being edited should show the start of its value and an ellipsis:\n%s", view)
	}

	m = keys(t, m, "esc")
	m = keys(t, selectModel(t, m, "openrouter/anthropic--claude-sonnet-4.5-thinking"), "enter")
	view = m.View()
	if !strings.Contains(flat(view), "Editopenrouter/anthropic--claude-sonnet-4.5-thinking") {
		t.Errorf("the title should wrap, not lose the end of the id:\n%s", view)
	}
	if !strings.Contains(view, "  Model name: anthropic/claude-…thinking") {
		t.Errorf("the fixed model name should keep both its ends:\n%s", view)
	}
	assertFits(t, "edit form", view, 40, 12)

	for _, c := range []struct{ cursor, n, room, first, count int }{
		{0, 10, 10, 0, 10}, {0, 10, 9, 0, 8}, {8, 10, 9, 2, 8}, {5, 10, 5, 3, 3}, {9, 10, 2, 8, 2}, {4, 10, 1, 4, 1},
	} {
		first, count, _, _ := formWindow(c.cursor, c.n, c.room)
		if first != c.first || count != c.count {
			t.Errorf("formWindow(%d, %d, %d) = fields %d..%d, want %d..%d", c.cursor, c.n, c.room, first, first+count-1, c.first, c.first+c.count-1)
		}
	}
}

// TestModelFormFamilySuggestions verifies the Family field offers the
// registry's families: right at the end of the text accepts the suggestion,
// right in the middle of the text only moves the cursor, a name that matches
// nothing is kept as typed, and an accepted suggestion replaces what was
// typed, case and all. bubbles accepts a suggestion wherever the cursor is,
// which would rewrite a family the user went back to correct, and it only
// appends the rest of the suggestion, which made "QW" into "QWen3.8": a new
// family that does not group with qwen3.8.
func TestModelFormFamilySuggestions(t *testing.T) {
	tm := newTabMachine(t, tabRegistry)
	m := moveTo(t, keys(t, modelsEditor(t, tm, 80, 24), "n"), mfFamily)

	m = keys(t, typeText(t, m, "qw"), "right")
	if got := m.models.form.value(mfFamily); got != "qwen3.8" {
		t.Fatalf("right at the end of %q should accept the suggestion; got %q", "qw", got)
	}

	// Back to "s|o" with the cursor in the middle: right must not complete it
	// to "sonnet".
	for range len("qwen3.8") {
		m = keys(t, m, "backspace")
	}
	m = keys(t, typeText(t, m, "so"), "left", "right")
	if got := m.models.form.value(mfFamily); got != "so" {
		t.Errorf("right in the middle of the text changed it to %q", got)
	}
	m = keys(t, m, "right")
	if got := m.models.form.value(mfFamily); got != "sonnet" {
		t.Errorf("right at the end should now accept; got %q", got)
	}

	for range len("sonnet") {
		m = keys(t, m, "backspace")
	}
	m = keys(t, typeText(t, m, "brand-new"), "right")
	if got := m.models.form.value(mfFamily); got != "brand-new" {
		t.Errorf("a family that matches no suggestion should be kept as typed; got %q", got)
	}

	for range len("brand-new") {
		m = keys(t, m, "backspace")
	}
	m = keys(t, typeText(t, m, "QW"), "right")
	if got := m.models.form.value(mfFamily); got != "qwen3.8" {
		t.Errorf("an accepted suggestion should replace what was typed, case and all; got %q", got)
	}

	// Two families match "qw": ctrl+n moves to the next one, since up and
	// down are "previous field" and "next field" here.
	two := strings.Replace(tabRegistry, "id = \"omlx/Gone-4bit\"\nfamily = \"qwen3.8\"", "id = \"omlx/Gone-4bit\"\nfamily = \"qwen3\"", 1)
	m = moveTo(t, keys(t, modelsEditor(t, newTabMachine(t, two), 80, 24), "n"), mfFamily)
	m = sendKey(t, typeText(t, m, "qw"), tea.KeyMsg{Type: tea.KeyCtrlN})
	if got := keys(t, m, "right").models.form.value(mfFamily); got != "qwen3.8" {
		t.Errorf("ctrl+n then right should accept the second match, qwen3.8; got %q", got)
	}
}

// TestModelFormPrefilledFamilyDoesNotPanic is the regression test for a
// bubbles v1.0.0 fault: the accept key panics (slice bounds out of range)
// when a value was set after the suggestions matched. The edit form prefills
// Family with a value that is itself a suggestion, then the user presses
// right, shortens it and presses right again.
func TestModelFormPrefilledFamilyDoesNotPanic(t *testing.T) {
	tm := newTabMachine(t, tabRegistry)
	m := keys(t, selectModel(t, modelsEditor(t, tm, 80, 24), "omlx/Gone-4bit"), "enter")
	if m.models.form.value(mfFamily) != "qwen3.8" {
		t.Fatalf("fixture: the edit should open with the row's family, got %q", m.models.form.value(mfFamily))
	}
	m = keys(t, m, "right", "backspace", "backspace", "right")
	if got := m.models.form.value(mfFamily); got != "qwen3.8" {
		t.Errorf("after shortening and accepting, Family = %q, want qwen3.8", got)
	}
	m = keys(t, typeText(t, m, "-instruct"), "right")
	if got := m.models.form.value(mfFamily); got != "qwen3.8-instruct" {
		t.Errorf("Family = %q, want what was typed", got)
	}
}

// TestModelFormKeysAreTextNotCommands verifies that inside the form q, d, n
// and r are characters, esc closes the form without writing, and tab moves
// between fields instead of switching tabs. A form that quit on q could not
// take a model named "qwen".
func TestModelFormKeysAreTextNotCommands(t *testing.T) {
	tm := newTabMachine(t, tabRegistry)
	m := moveTo(t, keys(t, modelsEditor(t, tm, 80, 24), "n"), mfName)
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	m = next.(*model)
	if cmd != nil && isQuit(cmd) {
		t.Fatal("q in a text field quit the editor")
	}
	m = typeText(t, m, "dnr")
	if got := m.models.form.value(mfName); got != "qdnr" {
		t.Errorf("Model name = %q, want the four characters typed", got)
	}
	m = keys(t, m, "tab")
	if m.tab != TabModels || m.models.form.cursor != mfFamily {
		t.Errorf("tab should move to the next field; tab = %d, cursor = %d", m.tab, m.models.form.cursor)
	}
	// Up from the first field wraps to the last, and down from there back.
	m = keys(t, moveTo(t, m, mfProvider), "up")
	if m.models.form.cursor != mfSubPeriod {
		t.Errorf("up from the first field should wrap to the last; cursor = %d", m.models.form.cursor)
	}
	m = keys(t, m, "esc")
	if m.models.phase != modelsList || m.models.form != nil || tm.text(t) != tabRegistry || m.registryChanged {
		t.Error("esc must close the form and write nothing")
	}
}

func isQuit(cmd tea.Cmd) bool {
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

// TestModelFormOnAnEmptyRegistry verifies the form works before any
// registry exists: the provider choices are the ones wt can seed, and the
// first save creates the file with the model and its provider row.
func TestModelFormOnAnEmptyRegistry(t *testing.T) {
	tm := newTabMachine(t, "")
	deps := tm.deps()
	deps.Capabilities = func(*config.Config, string) (map[string]any, error) { return nil, errors.New("ollama is not running") }
	m := modelsEditor(t, tm, 80, 24)
	m.opts.Models = deps
	m = keys(t, m, "n")
	if got := strings.Join(m.models.form.providers, ","); got != "mtplx,ollama,omlx,openrouter" {
		t.Fatalf("provider choices on an empty registry = %s", got)
	}
	m = keys(t, m, "right") // ollama -> omlx
	m = typeText(t, moveTo(t, m, mfName), "Qwen3.8-4B-4bit")
	m = keys(t, typeText(t, moveTo(t, m, mfFamily), "qwen3.8"), "ctrl+s")
	got := tm.text(t)
	if !strings.Contains(got, "[[providers]]\nid = \"omlx\"") || !strings.Contains(got, `id = "omlx/Qwen3.8-4B-4bit"`) {
		t.Errorf("the first save should create the registry with the provider row and the model:\n%s", got)
	}
	if m.models.phase != modelsList || !m.registryChanged {
		t.Error("the form should close and the change be recorded")
	}
}
```

The tab's `keys` helper gets one rule for the form: a text input answers most keys with a cursor-blink command, which sleeps for half a second, so inside the form only `ctrl+s` has its command run.

```diff
--- a/wt/internal/configeditor/models_tab_test.go
+++ b/wt/internal/configeditor/models_tab_test.go
@@ -177,7 +177,7 @@ func send(t *testing.T, m *model, msg tea.Msg) *model {
 func keys(t *testing.T, m *model, ks ...string) *model {
 	t.Helper()
 	for _, k := range ks {
-		m = send(t, m, keyMsg(k))
+		m = sendKey(t, m, keyMsg(k))
 	}
 	return m
 }
@@ -209,6 +209,18 @@ func keyMsg(k string) tea.KeyMsg {
 	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
 }
 
+// sendKey delivers one key. Inside the model form only ctrl+s starts a
+// command worth running: the others a text input returns are cursor blinks,
+// each of which sleeps for half a second.
+func sendKey(t *testing.T, m *model, msg tea.KeyMsg) *model {
+	t.Helper()
+	if m.models.phase == modelsForm && msg.Type != tea.KeyCtrlS {
+		next, _ := m.Update(msg)
+		return next.(*model)
+	}
+	return send(t, m, msg)
+}
+
 // modelsEditor is the editor in a terminal of the given size, opened on the
 // Models tab with its first probe delivered.
 func modelsEditor(t *testing.T, tm *tabMachine, width, height int) *model {
```

- [ ] **Step 2: Run them to see them fail**

Run, from `wt/`: `go test -count=1 ./internal/configeditor`

Expected: the package does not build.

```text
FAIL	github.com/ohanaverse/local-ai-setup/wt/internal/configeditor [build failed]
FAIL
# github.com/ohanaverse/local-ai-setup/wt/internal/configeditor [github.com/ohanaverse/local-ai-setup/wt/internal/configeditor.test]
internal/configeditor/models_form_test.go:26:18: undefined: mfCount
internal/configeditor/models_form_test.go:26:38: m.models.form undefined (type modelsTab has no field or method form)
internal/configeditor/models_form_test.go:29:14: m.models.form undefined (type modelsTab has no field or method form)
```

- [ ] **Step 3: Write the form**

Create `wt/internal/configeditor/models_form.go`. Its save is a command (`saveModelFormCmd`): the registry write takes a file lock, and an ollama add first runs `ollama show` — after `modeladmin.CheckAdd`, so that a value that is going to be refused is refused without waiting for it. Nothing in `updateModelForm` waits. Pressing `ctrl+s` sets `models.writing`, which is what makes a `ctrl+c` during the save wait for it (`leave`, Task 11); `applyModelSaved` clears it and issues the quit.

```go
package configeditor

import (
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modeladmin"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tui"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tuilayout"
)

// The model form's fields, top to bottom.
const (
	mfProvider = iota
	mfName
	mfFamily
	mfTags
	mfLocation
	mfInput
	mfCache
	mfOutput
	mfSubPrice
	mfSubPeriod
	mfCount
)

// modelFormLabels are the fields' labels; modelFormFields the names
// modeladmin.FieldError uses for them, so a refused save puts the cursor on
// the field at fault.
var (
	modelFormLabels = [mfCount]string{"Provider", "Model name", "Family", "Tags", "Location", "Input $/M", "Cache $/M", "Output $/M", "Subscription $", "Subscription period"}
	modelFormFields = [mfCount]string{
		modeladmin.FieldProvider, modeladmin.FieldName, modeladmin.FieldFamily, modeladmin.FieldTags, modeladmin.FieldLocation,
		modeladmin.FieldInputPrice, modeladmin.FieldCachePrice, modeladmin.FieldOutputPrice, modeladmin.FieldSubscriptionPrice, modeladmin.FieldSubscriptionPeriod,
	}
	locationChoices = []string{"", "local", "cloud"}
	periodChoices   = []string{"", "month", "year"}
)

// modelFormMode is what a save of the form does.
type modelFormMode int

const (
	modelFormAdd      modelFormMode = iota // n: a new model, every field open
	modelFormRegister                      // enter on a `new` row: provider and name are the row's
	modelFormEdit                          // enter on a registry row: id, provider and name are fixed
)

// modelForm is the add / register / edit form of the Models tab, hand-built
// on bubbles/textinput like the agent form. Text fields are inputs; the three
// fields with a fixed set of values (provider, location, period) are choices
// changed with left and right.
type modelForm struct {
	mode modelFormMode
	// id is the model being edited (modelFormEdit).
	id string
	// providers are the provider choices of an add; choice[f] is the index
	// chosen in field f's list (providers, locationChoices, periodChoices).
	providers []string
	choice    [mfCount]int
	// text are the inputs of the text fields; the choice fields' are unused.
	text [mfCount]textinput.Model
	// initial are the fields' values when the form opened. An edit sends
	// only the fields whose value differs, so an untouched field is never
	// rewritten.
	initial [mfCount]string
	cursor  int
	err     string
	saving  bool
}

// modelSavedMsg reports a save of the form.
type modelSavedMsg struct {
	id      string
	changed bool
	notes   []string
	err     error
}

func isChoice(f int) bool { return f == mfProvider || f == mfLocation || f == mfSubPeriod }

// choices are field f's values.
func (f *modelForm) choices(field int) []string {
	switch field {
	case mfProvider:
		return f.providers
	case mfLocation:
		return locationChoices
	}
	return periodChoices
}

// value is field f's current value, as text.
func (f *modelForm) value(field int) string {
	if isChoice(field) {
		return f.choices(field)[f.choice[field]]
	}
	return strings.TrimSpace(f.text[field].Value())
}

// editable reports whether the cursor can land on a field. The provider and
// the model name are fixed once a model exists (or was found on disk): usage
// history, rotation and profiles key on the id they make.
func (f *modelForm) editable(field int) bool {
	return f.mode == modelFormAdd || (field != mfProvider && field != mfName)
}

// formProviders are the providers an add can choose: every registry provider
// and the ones wt can seed a row for when a model names them, without
// mlx_lm_server — a pairing is created on the command line (`wt model add
// mlx_lm_server <target> --draft <draft>`).
func formProviders(cfg *config.Config) []string {
	ids := []string{}
	for _, p := range cfg.Providers {
		ids = append(ids, p.ID)
	}
	for _, id := range []string{"ollama", "omlx", "mtplx", "openrouter"} {
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	ids = slices.DeleteFunc(ids, localmodels.RunningOnly)
	sort.Strings(ids)
	return ids
}

// formFamilies are the families the registry already uses, offered as
// suggestions for the Family field.
func formFamilies(cfg *config.Config) []string {
	var fams []string
	for _, m := range cfg.Models {
		if m.Family != "" && !slices.Contains(fams, m.Family) {
			fams = append(fams, m.Family)
		}
	}
	sort.Strings(fams)
	return fams
}

func priceText(p *float64) string {
	if p == nil {
		return ""
	}
	return strconv.FormatFloat(*p, 'f', -1, 64)
}

// openModelForm opens the form: for a new model (row nil), to register a
// discovered row, or to edit a registry row.
func (m *model) openModelForm(row *modeladmin.Row) {
	cfg := m.models.cfg
	f := &modelForm{mode: modelFormAdd, providers: formProviders(cfg)}
	// An add starts on ollama: the provider most models are added to, and
	// one that is always among the choices.
	f.choice[mfProvider] = max(0, slices.Index(f.providers, "ollama"))
	var values [mfCount]string
	switch {
	case row == nil:
	case !row.Registered:
		f.mode = modelFormRegister
		values[mfName] = row.ModelName
		f.providers, f.choice[mfProvider] = []string{row.ProviderID}, 0
	default:
		f.mode, f.id = modelFormEdit, row.ID
		f.providers, f.choice[mfProvider] = []string{row.ProviderID}, 0
		values[mfName], values[mfFamily], values[mfTags] = row.ModelName, row.Family, strings.Join(row.Tags, ",")
		if i := config.IndexModelByID(cfg.Models, row.ID); i >= 0 {
			// The row's own keys, not what they resolve to: a model that
			// inherits its provider's location has none of its own.
			mdl := cfg.Models[i]
			f.choice[mfLocation] = max(0, slices.Index(locationChoices, string(mdl.Location)))
			f.choice[mfSubPeriod] = max(0, slices.Index(periodChoices, mdl.Cost.SubscriptionPeriod))
			values[mfInput], values[mfCache], values[mfOutput] = priceText(mdl.Cost.InputPricePerMillion), priceText(mdl.Cost.CachePricePerMillion), priceText(mdl.Cost.OutputPricePerMillion)
			values[mfSubPrice] = priceText(mdl.Cost.SubscriptionPrice)
		}
	}
	for field := range f.text {
		// The value goes in before the suggestions: bubbles v1.0.0 panics on
		// the accept key when SetValue follows a matched suggestion.
		f.text[field] = newTextInput(values[field], "")
		// No "> " prompt: the label is beside it, and renderFormFields marks
		// the focused row.
		f.text[field].Prompt = ""
	}
	fam := &f.text[mfFamily]
	fam.ShowSuggestions = true
	// The form accepts a suggestion itself (updateModelForm: right, with the
	// cursor at the end of the text). bubbles' own accept key is off: it is
	// tab, which is "next field" here, it fires wherever the cursor is, and
	// it completes what was typed without correcting its case.
	fam.KeyMap.AcceptSuggestion = key.NewBinding(key.WithDisabled())
	fam.SetSuggestions(formFamilies(cfg))
	for field := 0; field < mfCount; field++ {
		f.initial[field] = f.value(field)
	}
	for !f.editable(f.cursor) {
		f.cursor++
	}
	m.models.form = f
	m.models.phase = modelsForm
	m.focusModelField()
	m.resizeModelForm()
}

// focusModelField gives the focus to the field under the cursor, with the
// cursor at the end of its text.
func (m *model) focusModelField() {
	f := m.models.form
	for field := range f.text {
		f.text[field].Blur()
	}
	if !isChoice(f.cursor) {
		f.text[f.cursor].Focus()
		f.text[f.cursor].CursorEnd()
	}
}

// modelFieldRoom is how many columns field's value has beside its label:
// renderFormFields draws "> Label: " in front of it.
func (m *model) modelFieldRoom(field int) int {
	return max(m.width-utf8.RuneCountInString(modelFormLabels[field])-len(">  : ")+1, 4)
}

// resizeModelForm fits each input to the columns left beside its own label —
// not the longest label's, which on a 40-column terminal would leave every
// field sixteen columns.
func (m *model) resizeModelForm() {
	f := m.models.form
	if f == nil {
		return
	}
	for field := range f.text {
		// One column less than the room: a textinput draws its cursor after
		// the text.
		f.text[field].Width = max(m.modelFieldRoom(field)-1, 3)
	}
}

// moveModelField moves the cursor to the next (step 1) or previous (step -1)
// field the user can change, wrapping round.
func (m *model) moveModelField(step int) {
	f := m.models.form
	for {
		f.cursor = (f.cursor + step + mfCount) % mfCount
		if f.editable(f.cursor) {
			break
		}
	}
	m.focusModelField()
}

// updateModelForm handles a message while the form is showing.
func (m *model) updateModelForm(msg tea.Msg) (tea.Model, tea.Cmd) {
	f := m.models.form
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	if f.saving {
		// The write is in a command; only ctrl+c is answered meanwhile.
		if k.Type == tea.KeyCtrlC {
			return m.quit()
		}
		return m, nil
	}
	switch k.Type {
	case tea.KeyCtrlC:
		return m.quit()
	case tea.KeyEsc:
		m.models.phase, m.models.form = modelsList, nil
		return m, nil
	case tea.KeyCtrlS:
		// writing: a quit typed before the write has reported waits for it
		// (leave).
		f.saving, f.err, m.models.writing = true, "", true
		return m, m.saveModelFormCmd()
	case tea.KeyTab, tea.KeyDown, tea.KeyEnter:
		m.moveModelField(1)
		return m, nil
	case tea.KeyShiftTab, tea.KeyUp:
		m.moveModelField(-1)
		return m, nil
	}
	if isChoice(f.cursor) {
		n := len(f.choices(f.cursor))
		switch k.Type {
		case tea.KeyLeft:
			f.choice[f.cursor] = (f.choice[f.cursor] + n - 1) % n
		case tea.KeyRight, tea.KeySpace:
			f.choice[f.cursor] = (f.choice[f.cursor] + 1) % n
		}
		return m, nil
	}
	in := &f.text[f.cursor]
	if f.cursor == mfFamily && k.Type == tea.KeyRight && in.Position() == len([]rune(in.Value())) {
		// Right at the end of the text accepts the suggestion; anywhere else
		// it only moves the cursor. The field takes the suggestion whole:
		// "QW" completed to "QWen3.8" would be a new family beside qwen3.8.
		if s := in.CurrentSuggestion(); s != "" {
			in.SetValue(s)
			in.CursorEnd()
		}
		return m, nil
	}
	var cmd tea.Cmd
	*in, cmd = in.Update(k)
	return m, cmd
}

// saveModelFormCmd writes the form's model in a command: a registry write
// takes a file lock, and an ollama add first runs `ollama show`.
func (m *model) saveModelFormCmd() tea.Cmd {
	f, deps, cfg := m.models.form, m.opts.Models, m.models.cfg
	given := func(field int) *string {
		v := f.value(field)
		if f.mode == modelFormEdit && v == f.initial[field] {
			return nil // untouched: an edit leaves the key as it is
		}
		if f.mode != modelFormEdit && v == "" && field != mfFamily {
			return nil // an add writes no key for an empty field
		}
		return &v
	}
	fields := modeladmin.Fields{
		Family: given(mfFamily), Tags: given(mfTags), Location: given(mfLocation),
		InputPrice: given(mfInput), CachePrice: given(mfCache), OutputPrice: given(mfOutput),
		SubscriptionPrice: given(mfSubPrice), SubscriptionPeriod: given(mfSubPeriod),
	}
	if f.mode == modelFormEdit {
		id := f.id
		return func() tea.Msg {
			changed, err := modeladmin.Edit(id, fields)
			return modelSavedMsg{id: id, changed: changed, err: err}
		}
	}
	req := modeladmin.AddRequest{ProviderID: f.value(mfProvider), ModelName: f.value(mfName), Fields: fields}
	return func() tea.Msg {
		// What can be refused without the registry is refused before the
		// lookup, which can take its whole timeout when ollama is down.
		if err := modeladmin.CheckAdd(req); err != nil {
			return modelSavedMsg{err: err}
		}
		var notes []string
		if modeladmin.UsesOllama(req.ProviderID) {
			info, err := deps.Capabilities(cfg, req.ModelName)
			switch {
			case errors.Is(err, exec.ErrNotFound):
				// The usual reason, in words that fit a status line.
				notes = append(notes, "added without tool/vision capabilities: ollama is not installed")
			case err != nil:
				notes = append(notes, "added without tool/vision capabilities: "+err.Error())
			}
			req.ModelInfo = info
		}
		res, err := modeladmin.Add(req, deps.SeedEnv())
		if err != nil {
			return modelSavedMsg{err: err}
		}
		for _, p := range res.ProvidersAdded {
			notes = append(notes, "added provider "+p)
		}
		return modelSavedMsg{id: res.ID, changed: true, notes: append(notes, res.Warnings...)}
	}
}

// applyModelSaved takes a save's outcome. A refusal stays on the form, with
// the cursor on the field at fault; a success goes back to the table, which
// is re-probed with the cursor on the saved model. A quit that was waiting
// for the write happens now, with the change recorded for the caller; after
// a refusal it does not, because the refusal has to be read.
func (m *model) applyModelSaved(msg modelSavedMsg) tea.Cmd {
	mt := &m.models
	mt.writing = false
	f := mt.form
	if f == nil {
		return nil
	}
	f.saving = false
	if msg.err != nil {
		m.quitPending = false
		f.err = msg.err.Error()
		var fe *modeladmin.FieldError
		if errors.As(msg.err, &fe) {
			if i := slices.Index(modelFormFields[:], fe.Field); i >= 0 && f.editable(i) {
				f.cursor = i
				m.focusModelField()
			}
		}
		return nil
	}
	verb := "saved "
	switch {
	case f.mode != modelFormEdit:
		verb = "added "
	case !msg.changed:
		verb = "no change to "
	}
	// Each note on a line of its own.
	mt.status = strings.Join(append([]string{verb + msg.id}, msg.notes...), "\n")
	mt.phase, mt.form = modelsList, nil
	m.registryChanged = m.registryChanged || msg.changed
	if m.quitPending {
		return tea.Quit
	}
	if !msg.changed {
		return nil
	}
	mt.selectID = msg.id
	return m.probeCmd()
}

// modelFormHints are the form's key hints, fullest first.
var modelFormHints = []string{
	"ctrl+s save · esc cancel · tab/↑/↓ move · ←/→ change",
	"^s save · esc · ↑/↓ · ←/→ change",
}

// formWindow chooses which of a form's n fields are drawn in room rows: count
// fields from first, always including the one under the cursor. When not all
// of them fit and there are rows to spare for it, the first and the last row
// go to a marker saying how many fields are above and below (above, below),
// so that a field off the screen is never a field the user does not know of.
func formWindow(cursor, n, room int) (first, count int, above, below bool) {
	if room >= n {
		return 0, n, false, false
	}
	if room < 3 {
		// No row to spare for a marker: the fields alone.
		return max(0, cursor-room+1), room, false, false
	}
	for first = 0; first < n; first++ {
		above = first > 0
		rows := room
		if above {
			rows--
		}
		count = min(rows, n-first)
		if below = first+count < n; below {
			count = rows - 1
		}
		if cursor < first+count {
			break
		}
	}
	return first, count, above, below
}

// endCut shortens s to w runes, the last of them an ellipsis.
func endCut(s string, w int) string {
	if runes := []rune(s); len(runes) > w && w > 0 {
		return string(runes[:w-1]) + "…"
	}
	return s
}

// modelFormView renders the form inside the terminal: the tab bar, a title,
// as many fields as fit — always the one under the cursor — an error when
// there is one, and the key hints. Bubble Tea drops a too-tall view's top
// lines, so on a short terminal the fields scroll instead, with a marker for
// the ones above and below (formWindow). Nothing is cut silently: the title
// wraps, a fixed value too long for its row loses its middle, and a field
// that is not being edited shows the start of its value and an ellipsis.
func (m *model) modelFormView() string {
	f := m.models.form
	width := max(m.width, 1)
	title := "Add a model"
	switch f.mode {
	case modelFormRegister:
		title = "Register " + modeladmin.DeriveID(f.value(mfProvider), f.value(mfName))
	case modelFormEdit:
		title = "Edit " + f.id
	}
	if f.saving {
		title += "  (saving...)"
	}
	errBlock := ""
	if f.err != "" {
		errBlock = tui.ErrorStyle(m.theme).Render(wrapText(f.err, width)) + "\n"
	}
	dim := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenDim))
	head := tabBar(m.theme, TabModels) + "\n" + wrapText(title, width) + "\n"
	foot := errBlock + dim.Render(fitHints(m.width, modelFormHints))
	// A blank line under the title and above the hints, when the terminal
	// has the two rows to spare.
	if m.height <= 0 || m.height >= lipgloss.Height(head+foot)+mfCount+2 {
		head, foot = head+"\n", "\n"+foot
	}

	// The rows the fields can have: what the head and the foot leave.
	room := mfCount
	if m.height > 0 {
		room = min(mfCount, max(1, m.height-lipgloss.Height(head+foot)))
	}
	first, count, above, below := formWindow(f.cursor, mfCount, room)
	var fields []formField
	for field := first; field < first+count; field++ {
		value := f.value(field)
		switch {
		case !f.editable(field):
			value = middleCut(value, m.modelFieldRoom(field))
		case isChoice(field):
			shown := value
			if shown == "" {
				shown = map[int]string{mfLocation: "(the provider's)", mfSubPeriod: "(none)"}[field]
			}
			value = "< " + shown + " >"
		case field == f.cursor:
			value = f.text[field].View()
		default:
			value = endCut(value, m.modelFieldRoom(field))
		}
		fields = append(fields, formField{modelFormLabels[field], value, field == f.cursor})
	}
	// renderFormFields ends every row with a newline; the last one's goes,
	// so that clipping does not pad an empty line under the fields.
	rows := tuilayout.Clip(strings.TrimRight(renderFormFields(m.theme, fields), "\n"), width)
	if above {
		rows = dim.Render(fmt.Sprintf("  ↑ %d more", first)) + "\n" + rows
	}
	if below {
		rows += "\n" + dim.Render(fmt.Sprintf("  ↓ %d more", mfCount-first-count))
	}
	return head + rows + "\n" + foot
}
```

- [ ] **Step 4: Open it from the list**

```diff
--- a/wt/internal/configeditor/models_tab.go
+++ b/wt/internal/configeditor/models_tab.go
@@ -46,6 +46,7 @@ type modelsPhase int
 const (
 	modelsList modelsPhase = iota
 	modelsRemove
+	modelsForm
 )
 
 // modelsTab is the Models tab's state. Its rows come from one probe run in a
@@ -83,6 +84,8 @@ type modelsTab struct {
 	selectID string
 	// remove is the row the remove prompt is about.
 	remove modeladmin.Row
+	// form is the add / register / edit form while it is open.
+	form *modelForm
 }
 
 // modelsLoadedMsg carries one probe's rows.
@@ -356,8 +359,8 @@ func (m *model) modelsDetail() (id, path []string) {
 
 // modelsHints are the tab's key hints, fullest first.
 var modelsHints = []string{
-	"d remove · r refresh · / filter · tab agents · q quit",
-	"d · r · / · tab · q quit",
+	"enter edit · n add · d remove · r refresh · / filter · tab agents · q quit",
+	"enter · n · d · r · / · tab · q quit",
 }
 
 // fitHints is the fullest of hints that fits width; the last one, cut, when
@@ -415,7 +418,10 @@ func (m *model) modelsFrames() []tuilayout.ListFrame {
 }
 
 // fitModels sizes the table to the room its frame leaves. Update calls it
-// after every message, like fitList for the Agents tab.
+// after every message, like fitList for the Agents tab. It measures the list
+// (up to sixteen probe renders), so it runs only while the table is what is
+// showing: the form and the remove prompt do not draw it, and the message
+// that brings the table back fits it.
 //
 // The columns that fit are chosen with MODEL at its full width, the longest
 // id (tuilayout.FitTo). When MODEL, STATUS and RUNNING — the three that are
@@ -425,7 +431,7 @@ func (m *model) modelsFrames() []tuilayout.ListFrame {
 // STATUS and RUNNING off every row.
 func (m *model) fitModels() {
 	mt := &m.models
-	if m.width <= 0 || m.height <= 0 || !mt.loaded {
+	if m.width <= 0 || m.height <= 0 || !mt.loaded || m.tab != TabModels || mt.phase != modelsList {
 		return
 	}
 	setModelColumn(mt.cols, mt.idWidth)
@@ -439,8 +445,11 @@ func (m *model) fitModels() {
 
 // modelsView renders the Models tab.
 func (m *model) modelsView() string {
-	if m.models.phase == modelsRemove {
+	switch m.models.phase {
+	case modelsRemove:
 		return m.modelsRemoveView()
+	case modelsForm:
+		return m.modelFormView()
 	}
 	if !m.models.loaded {
 		return tabBar(m.theme, TabModels) + "\n\n" + "Probing providers..."
@@ -529,8 +538,11 @@ func (m *model) applyFilterMatches(msg filterMatchesMsg) tea.Cmd {
 // updateModels handles a message while the Models tab is showing.
 func (m *model) updateModels(msg tea.Msg) (tea.Model, tea.Cmd) {
 	mt := &m.models
-	if mt.phase == modelsRemove {
+	switch mt.phase {
+	case modelsRemove:
 		return m.updateModelsRemove(msg)
+	case modelsForm:
+		return m.updateModelForm(msg)
 	}
 	key, isKey := msg.(tea.KeyMsg)
 	if !isKey {
@@ -566,6 +578,17 @@ func (m *model) updateModels(msg tea.Msg) (tea.Model, tea.Cmd) {
 			return m, nil
 		}
 		return m, m.probeCmd()
+	case "n":
+		if !mt.busy {
+			m.openModelForm(nil)
+		}
+		return m, nil
+	case "enter":
+		// Edit a registry row; register a discovered one.
+		if r, ok := m.selectedModel(); ok && !mt.busy {
+			m.openModelForm(&r)
+		}
+		return m, nil
 	case "d":
 		r, ok := m.selectedModel()
 		switch {
```

```diff
--- a/wt/internal/configeditor/editor.go
+++ b/wt/internal/configeditor/editor.go
@@ -183,6 +183,7 @@ func (m *model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
 		if m.phase == phaseForm {
 			m.resizeFormInputs()
 		}
+		m.resizeModelForm()
 	case loadedMsg:
 		m.ready = true
 		if msg.cfg == nil {
@@ -210,6 +211,8 @@ func (m *model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
 		return m, nil
 	case modelRemovedMsg:
 		return m, m.applyModelRemoved(msg)
+	case modelSavedMsg:
+		return m, m.applyModelSaved(msg)
 	case filterMatchesMsg:
 		return m, m.applyFilterMatches(msg)
 	case saveMsg:
```

- [ ] **Step 5: Run the package**

Run, from `wt/`: `go test -count=1 ./internal/configeditor`

Expected: `ok  	github.com/ohanaverse/local-ai-setup/wt/internal/configeditor`, in about a second. If it takes a minute, a test is running a text input's cursor-blink commands: every key sent to the form must go through `keys`, `typeText` or `sendKey`, not `send`.

- [ ] **Step 6: Commit**

```bash
cd wt && test -z "$(gofmt -l .)" && go vet ./... && cd ..
git add wt/internal/configeditor
git commit -m "feat(wt): the Models tab adds, registers and edits models from a form"
```

### Task 15: What bare `modelman` says

Bare `modelman` has printed a notice and exited 1 since Step 2. Its second bullet says to edit `registry.toml` by hand and that the Models tab "replaces this TUI when it ships". It has shipped. This is modelman's one source change in this step.

**Files:**
- Modify: `modelman/tests/commands/test_run_tui.py:45-63` (`test_no_args_does_not_open_the_tui_and_says_where_to_go`)
- Modify: `modelman/src/modelman/main.py:437-448` (`TUI_DISABLED_MESSAGE`)

**Interfaces:**
- Consumes: nothing from this plan.
- Produces: the notice's text. The guides quote none of it beyond `modelman's TUI is disabled`, which does not change.

- [ ] **Step 1: Write the failing test**

```diff
--- a/modelman/tests/commands/test_run_tui.py
+++ b/modelman/tests/commands/test_run_tui.py
@@ -52,7 +52,13 @@ def test_no_args_does_not_open_the_tui_and_says_where_to_go():
         run_tui.assert_not_called()
     assert "TUI is disabled" in result.output
     assert "wt model init" in result.output
-    assert "wt litellm sync" in result.output
+    # What replaced the TUI: wt's Models tab, and the commands behind it.
+    assert "`wt model` (the Models tab of `wt config`)" in result.output
+    for command in ("wt model add", "wt model edit", "wt model rm", "wt model list"):
+        assert command in result.output
+    # No model is added by editing the file any more.
+    assert "by hand" not in result.output
+    assert "wt litellm sync" not in result.output
     assert "modelman --help" in result.output
     # wt downloads nothing, so the notice names each provider's own tool.
     assert "ollama pull" in result.output
```

- [ ] **Step 2: Run it to see it fail**

Run, from `modelman/`: `uv run pytest tests/commands/test_run_tui.py -q`

Expected: one failure, on the first new assertion.

```text
tests/commands/test_run_tui.py F..............................           [100%]

=================================== FAILURES ===================================
___________ test_no_args_does_not_open_the_tui_and_says_where_to_go ____________
tests/commands/test_run_tui.py:56: in test_no_args_does_not_open_the_tui_and_says_where_to_go
    assert "`wt model` (the Models tab of `wt config`)" in result.output
```

- [ ] **Step 3: Change the notice**

```diff
--- a/modelman/src/modelman/main.py
+++ b/modelman/src/modelman/main.py
@@ -437,8 +437,8 @@ def _file_digest(path: Path) -> bytes | None:
 TUI_DISABLED_MESSAGE = (
     "modelman's TUI is disabled: wt writes registry.toml now, and modelman is being retired.\n"
     "  - create the registry and its provider rows: wt model init\n"
-    "  - add, edit or remove a model: edit registry.toml by hand, then run `wt litellm sync`\n"
-    "    (wt's Models tab in `wt config` replaces this TUI when it ships)\n"
+    "  - add, edit or remove a model: `wt model` (the Models tab of `wt config`), or without\n"
+    "    a terminal `wt model add`, `wt model edit`, `wt model rm`; `wt model list` shows them\n"
     "  - download a model (wt does not): `ollama pull <name:tag>` for ollama,\n"
     "    `hf download <org>/<repo> --local-dir ~/.omlx/models/<repo>` for omlx,\n"
     "    `mtplx pull <org>/<name>` for mtplx\n"
```

- [ ] **Step 4: Run the tests and the checks**

Run, from `modelman/`: `uv run pytest tests/commands/test_run_tui.py -q && make check`

Expected: `31 passed`; `make check` ends with `Success: no issues found in 43 source files`.

- [ ] **Step 5: Commit**

```bash
git add modelman/src/modelman/main.py modelman/tests/commands/test_run_tui.py
git commit -m "fix(modelman): the TUI-disabled notice names wt model and the Models tab"
```

### Task 16: Look at the form, and rewrite the "by hand" docs

**Files:**
- Create (scratch, not committed): `wt-screen.py`, `fake-providers.py`, `make-home.sh`, `run.sh` in `/tmp/wt-step3-screens`
- Modify: `wt/docs/wt-model.md`, `wt/docs/configuration.md`, `wt/docs/internals/tui.md`, `wt/CHANGELOG.md`
- Modify: `docs/guides/00-config-map.md`, `01-initial-setup.md`, `02-providers-and-models.md`, `03-model-families.md`, `04-litellm-config.md`, `06-wt-agents-and-models.md`, `08-maintenance-and-troubleshooting.md`, `10-mlx-lm-quantization.md`, `docs/reference/provider-artifacts.md`
- Modify: `CLAUDE.md`, `README.md`, `wt/README.md`, `modelman/README.md`, `.claude/skills/mlx-lm-quantization/SKILL.md`, `bin/mlx-quantize`

**Interfaces:**
- Consumes: the binary built from this branch.
- Produces: nothing other tasks use.

**How the steps are run.** As in Task 13: every command stands on its own, with the paths written out, and works in a fresh shell. wt is run only as `/tmp/wt-step3-screens/run.sh /tmp/wt-step3-screens/wt …`; `run.sh` empties the environment before it sets a throwaway `HOME`, a scratch LiteLLM config and a restart command that does nothing. The four scripts are the ones Task 13 used and are given here in full: this task runs on another branch, in another session, and that directory is gone.

- [ ] **Step 1: Write the four scripts**

```bash
rm -rf /tmp/wt-step3-screens && mkdir -p /tmp/wt-step3-screens
```

Create `/tmp/wt-step3-screens/wt-screen.py`. It runs a full-screen command in a pty, answers the two questions wt's TUI asks the terminal before it draws, sends the keys, and prints the screen a terminal of that size would show, each line with its width in front:

```python
"""Run a full-screen wt command in a pty and print the screen it shows.

Usage: python3 wt-screen.py [--size 80x24] [--keys 'KEYS'] [--wait SECONDS] <command> [args...]

KEYS are sent one at a time, 0.4s apart, after the first screen is drawn:
plain characters as they are, and {tab} {enter} {esc} {up} {down} {left}
{right} {ctrl+s} {ctrl+c} {bs} for the keys that have no character. The screen
printed is the one shown after the last key, each line with its width in
front. When the command ends before every key was sent, the last screen it
drew is printed all the same, and a line on stderr says after which key.
"""
import fcntl, os, pty, re, select, signal, struct, sys, termios, time

args = sys.argv[1:]
cols, rows, keys, wait = 80, 24, "", 2.0
while args and args[0].startswith("--"):
    flag, value, args = args[0], args[1], args[2:]
    if flag == "--size":
        cols, rows = (int(n) for n in value.split("x"))
    elif flag == "--keys":
        keys = value
    elif flag == "--wait":
        wait = float(value)

NAMED = {"tab": "\t", "enter": "\r", "esc": "\x1b", "up": "\x1b[A", "down": "\x1b[B", "right": "\x1b[C",
         "left": "\x1b[D", "ctrl+s": "\x13", "ctrl+c": "\x03", "bs": "\x7f"}
presses = [NAMED[a] if a else b for a, b in re.findall(r"\{([a-z+]+)\}|(.)", keys, re.S)]

pid, fd = pty.fork()
if pid == 0:
    # The child's stdin is the pty: size it before wt reads the size.
    fcntl.ioctl(0, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0))
    os.environ["TERM"] = "xterm-256color"
    os.execvp(args[0], args)

out, answered, ended = b"", False, False


def pump(seconds):
    """Read what wt draws for a while, answering its two terminal queries."""
    global out, answered, ended
    end = time.time() + seconds
    while time.time() < end and not ended:
        if not select.select([fd], [], [], 0.05)[0]:
            continue
        try:
            chunk = os.read(fd, 65536)
        except OSError:
            chunk = b""
        if not chunk:
            ended = True  # the command has left: its side of the pty is closed
            return
        out += chunk
        # wt asks the terminal for its background colour (OSC 11) and the
        # cursor position before it draws; without both answers it waits.
        if not answered and b"\x1b]11;?" in out and b"\x1b[6n" in out:
            os.write(fd, b"\x1b]11;rgb:0000/0000/0000\x1b\\\x1b[1;1R")
            answered = True


pump(wait)
for sent, press in enumerate(presses):
    try:
        if ended:
            raise OSError
        os.write(fd, press.encode())
    except OSError:
        # Writing to a pty whose command has gone fails (EIO on macOS).
        print(f"the command exited after key {sent} of {len(presses)}", file=sys.stderr)
        break
    pump(0.4)
pump(0.6)
# The screen is captured: end wt without choosing anything more. (When the
# keys ended with q, wt has already left by itself.)
try:
    os.kill(pid, signal.SIGKILL)
except ProcessLookupError:
    pass
os.waitpid(pid, 0)


def screen(data):
    """Replay the escape sequences wt wrote onto a rows x cols grid."""
    grid = [[" "] * cols for _ in range(rows)]
    r = c = 0
    text = data.decode("utf-8", "replace")
    i = 0
    while i < len(text):
        ch = text[i]
        if ch == "\x1b":
            m = re.match(r"\x1b\[([0-9;?]*)([A-Za-z])", text[i:])
            if m:
                nums = [int(n) if n.isdigit() else 0 for n in m.group(1).split(";")] if m.group(1) else []
                op = m.group(2)
                if op in "Hf":
                    r = (nums[0] or 1) - 1 if nums else 0
                    c = (nums[1] or 1) - 1 if len(nums) > 1 else 0
                elif op == "A":
                    r = max(0, r - ((nums[0] if nums else 0) or 1))
                elif op == "B":
                    r = min(rows - 1, r + ((nums[0] if nums else 0) or 1))
                elif op == "C":
                    c = min(cols - 1, c + ((nums[0] if nums else 0) or 1))
                elif op == "D":
                    c = max(0, c - ((nums[0] if nums else 0) or 1))
                elif op == "G":
                    c = ((nums[0] if nums else 0) or 1) - 1
                elif op == "K":
                    mode = nums[0] if nums else 0
                    lo, hi = (c, cols) if mode == 0 else (0, c + 1) if mode == 1 else (0, cols)
                    for k in range(lo, hi):
                        grid[r][k] = " "
                elif op == "J":
                    mode = nums[0] if nums else 0
                    if mode in (2, 3):
                        grid = [[" "] * cols for _ in range(rows)]
                    elif mode == 0:
                        for k in range(c, cols):
                            grid[r][k] = " "
                        for line in range(r + 1, rows):
                            grid[line] = [" "] * cols
                i += len(m.group(0))
                continue
            m = re.match(r"\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[=>()][0-9A-Za-z]?", text[i:])
            i += len(m.group(0)) if m else 1
            continue
        if ch == "\r":
            c = 0
        elif ch == "\n":
            r = min(rows - 1, r + 1)
        elif ch == "\b":
            c = max(0, c - 1)
        elif ch >= " ":
            if c < cols:
                grid[r][c] = ch
            c += 1
        i += 1
    return ["".join(line).rstrip() for line in grid]


for line in screen(out):
    print(f"{len(line):3d} |{line}")
```

Create `/tmp/wt-step3-screens/fake-providers.py`, a fake ollama and a fake omlx on two scratch ports, so that no real provider is probed:

```python
"""A fake ollama (port 18434) and a fake omlx (port 18000) for screen captures:
canned answers to the read-only probes wt's inventory makes. Nothing here is a
real provider, and wt is never pointed at one."""
import json, threading
from http.server import BaseHTTPRequestHandler, HTTPServer

OLLAMA = {
    "/api/tags": {"models": [{"name": "gemma4:9b", "size": 5800000000}, {"name": "qwen3:8b", "size": 5200000000}]},
    "/api/ps": {"models": [{"name": "gemma4:9b"}]},
}
OMLX = {
    "/v1/models/status": {"final_ceiling": 64000000000, "current_model_memory": 21000000000, "models": [
        {"id": "Qwen3.8-27B-Instruct-MLX-6bit", "loaded": True, "is_loading": False, "resident_estimated_size": 21000000000},
        {"id": "Qwen3.8-4B-4bit", "loaded": False, "is_loading": False},
    ]},
}


SHOW = {
    "details": {"family": "qwen3", "parameter_size": "8.2B", "quantization_level": "Q4_K_M"},
    "model_info": {"general.architecture": "qwen3", "qwen3.context_length": 40960},
    "capabilities": ["completion", "tools", "vision"],
}


def handler(answers):
    class H(BaseHTTPRequestHandler):
        def do_GET(self):
            body = answers.get(self.path)
            self.send_response(200 if body is not None else 404)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(json.dumps(body if body is not None else {}).encode())

        def do_POST(self):
            # `ollama show` asks POST /api/show; answer for any model.
            self.rfile.read(int(self.headers.get("Content-Length") or 0))
            body = SHOW if self.path == "/api/show" else None
            self.send_response(200 if body is not None else 404)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(json.dumps(body if body is not None else {}).encode())

        def log_message(self, *a):
            pass
    return H


for port, answers in ((18434, OLLAMA), (18000, OMLX)):
    threading.Thread(target=HTTPServer(("127.0.0.1", port), handler(answers)).serve_forever, daemon=True).start()
threading.Event().wait()
```

Create `/tmp/wt-step3-screens/make-home.sh`, which builds the throwaway home:

```bash
#!/bin/bash
# Build a throwaway home for screen captures: a registry whose local providers
# are the fake ones (fake-providers.py, ports 18434 and 18000), two omlx model
# directories where a real install has them (~/.omlx/models), and a scratch
# LiteLLM config. Usage: make-home.sh <directory> (the directory is deleted
# and made again; it must be an absolute path, which the registry's model_dir
# is written from).
set -euo pipefail
home="$1"
case "$home" in /*) ;; *) echo "make-home.sh: $home is not an absolute path" >&2; exit 2 ;; esac
rm -rf "$home"
mkdir -p "$home/.config/local-ai" "$home/.config/agent-wt" "$home/.omlx/models/Qwen3.8-27B-Instruct-MLX-6bit" "$home/.omlx/models/Qwen3.8-4B-4bit"
echo '{}' > "$home/.omlx/models/Qwen3.8-27B-Instruct-MLX-6bit/config.json"
echo '{}' > "$home/.omlx/models/Qwen3.8-4B-4bit/config.json"
printf 'model_list: []\n' > "$home/litellm.yaml"
cat > "$home/.config/agent-wt/config.toml" <<'TOML'
default_tag = "code"

[[agents]]
name = "claude"
supported_providers = ["claude", "ollama", "omlx", "openrouter"]
TOML
cat > "$home/.config/local-ai/registry.toml" <<TOML
[[providers]]
id = "ollama"
name = "Ollama"
location = "local"
protocols = ["anthropic", "openai-chat"]

[providers.auth]
type = "none"
base_url = "http://127.0.0.1:18434"

[[providers]]
id = "omlx"
name = "oMLX"
location = "local"
model_dir = "$home/.omlx/models"

[providers.auth]
type = "none"
base_url = "http://127.0.0.1:18000"

[[providers]]
id = "openrouter"
name = "OpenRouter"
location = "cloud"

[providers.auth]
type = "api_key"
secret_ref = "OPENROUTER_API_KEY"
base_url = "https://openrouter.ai/api/v1"

[[providers]]
id = "claude"
name = "Claude"
location = "cloud"

[providers.auth]
type = "native"

[[providers]]
id = "agy"
name = "Agy"
location = "cloud"

[providers.auth]
type = "native"

[[models]]
id = "claude/opus"
family = "opus"
provider_id = "claude"
model_name = "opus"
tags = ["code"]

[[models]]
id = "openrouter/anthropic--claude-sonnet-4.5-thinking"
family = "sonnet"
provider_id = "openrouter"
model_name = "anthropic/claude-sonnet-4.5-thinking"
tags = ["code", "design"]

[models.cost]
input_price_per_million = 3
cache_price_per_million = 0.3
output_price_per_million = 15

[[models]]
id = "omlx/Qwen3.8-27B-Instruct-MLX-6bit"
family = "qwen3.8"
provider_id = "omlx"
model_name = "Qwen3.8-27B-Instruct-MLX-6bit"
tags = ["code"]

[[models]]
id = "omlx/Gone-4bit"
family = "qwen3.8"
provider_id = "omlx"
model_name = "Gone-4bit"
tags = []

[[models]]
id = "ollama/gemma4:9b"
family = "gemma4"
provider_id = "ollama"
model_name = "gemma4:9b"
tags = ["code"]
TOML
```

Create `/tmp/wt-step3-screens/run.sh`, the only way wt is run:

```bash
#!/bin/bash
# Run a command in the throwaway home that is beside this script, and in no
# other: the environment is emptied first, so nothing of the caller's — HOME,
# XDG_CONFIG_HOME, a registry or LiteLLM override, an API key — reaches wt,
# and wt reaches nothing of the caller's. PATH is the system's alone, so no
# installed provider (ollama, omlx, mtplx) is found. Usage: run.sh <command>
# [args...]
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd -P)"
cd "$here/home"
exec env -i HOME="$here/home" XDG_CONFIG_HOME="$here/home/.config" \
  WT_LITELLM_CONFIG="$here/home/litellm.yaml" WT_LITELLM_RESTART_CMD=true \
  OPENROUTER_API_KEY=sk-not-a-key PATH=/usr/bin:/bin TERM=xterm-256color "$@"
```

- [ ] **Step 2: Build, make the home, and start the fakes**

From the monorepo root:

```bash
(cd wt && go build -o /tmp/wt-step3-screens/wt ./cmd/wt)
chmod +x /tmp/wt-step3-screens/make-home.sh /tmp/wt-step3-screens/run.sh
/tmp/wt-step3-screens/make-home.sh /tmp/wt-step3-screens/home
nohup python3 /tmp/wt-step3-screens/fake-providers.py >/dev/null 2>&1 &
echo $! > /tmp/wt-step3-screens/fakes.pid
```

Check the wrapper: `/tmp/wt-step3-screens/run.sh env`

Expected: these seven variables and no other (`…` is the scratch directory with any link in it resolved; on macOS, `/private/tmp/wt-step3-screens`):

```text
HOME=…/home
XDG_CONFIG_HOME=…/home/.config
WT_LITELLM_CONFIG=…/home/litellm.yaml
WT_LITELLM_RESTART_CMD=true
OPENROUTER_API_KEY=sk-not-a-key
PATH=/usr/bin:/bin
TERM=xterm-256color
```

Check the fakes: `curl -s http://127.0.0.1:18434/api/tags`

Expected: `{"models": [{"name": "gemma4:9b", "size": 5800000000}, {"name": "qwen3:8b", "size": 5200000000}]}`. If a later screen shows `unknown` and `?` on the ollama and omlx rows, the fakes are no longer running: run the `nohup` line and the `echo` line again.

- [ ] **Step 3: The add form at 80x24**

Run: `python3 /tmp/wt-step3-screens/wt-screen.py --size 80x24 --keys 'n' /tmp/wt-step3-screens/run.sh /tmp/wt-step3-screens/wt model`

Expected (here and below: trailing blank lines left out, and without the width the real output prints in front of each line):

```text
 Agents   [Models]
Add a model

> Provider: < ollama >
  Model name:
  Family:
  Tags:
  Location: < (the provider's) >
  Input $/M:
  Cache $/M:
  Output $/M:
  Subscription $:
  Subscription period: < (none) >

ctrl+s save · esc cancel · tab/↑/↓ move · ←/→ change
```

What to check: every field is on screen with the cursor (`>`) on Provider, which starts on `ollama`; the three choices show their value between `<` and `>`; the key hints are the last line.

- [ ] **Step 4: The edit form and the register form at 80x24**

Run: `python3 /tmp/wt-step3-screens/wt-screen.py --size 80x24 --keys '{down}{down}{down}{down}{enter}' /tmp/wt-step3-screens/run.sh /tmp/wt-step3-screens/wt model`

Expected (the fifth row is the cloud model):

```text
 Agents   [Models]
Edit openrouter/anthropic--claude-sonnet-4.5-thinking

  Provider: openrouter
  Model name: anthropic/claude-sonnet-4.5-thinking
> Family: sonnet
  Tags: code,design
  Location: < (the provider's) >
  Input $/M: 3
  Cache $/M: 0.3
  Output $/M: 15
  Subscription $:
  Subscription period: < (none) >

ctrl+s save · esc cancel · tab/↑/↓ move · ←/→ change
```

What to check: the title names the id; Provider and Model name are plain text, not fields; the cursor starts on Family; the prices read as the registry has them (`3`, not `3.0`).

Run: `python3 /tmp/wt-step3-screens/wt-screen.py --size 80x24 --keys '{down}{down}{down}{down}{down}{enter}' /tmp/wt-step3-screens/run.sh /tmp/wt-step3-screens/wt model`

Expected (the sixth row is an unregistered ollama model):

```text
 Agents   [Models]
Register ollama/qwen3:8b

  Provider: ollama
  Model name: qwen3:8b
> Family:
  Tags:
  Location: < (the provider's) >
  Input $/M:
  Cache $/M:
  Output $/M:
  Subscription $:
  Subscription period: < (none) >

ctrl+s save · esc cancel · tab/↑/↓ move · ←/→ change
```

- [ ] **Step 5: A refused save, a save, and `esc`**

Run: `python3 /tmp/wt-step3-screens/wt-screen.py --size 80x24 --keys 'n{right}{right}{down}qwen/qwen3.8-27b{down}qw{right}{down}code,design{down}{down}0.5{down}{down}cheap{ctrl+s}' /tmp/wt-step3-screens/run.sh /tmp/wt-step3-screens/wt model`

Expected: the form stays, with the reason above the hints and the cursor on the field at fault. Family reads `qwen3.8`: `qw` and right accepted the suggestion.

```text
 Agents   [Models]
Add a model

  Provider: < openrouter >
  Model name: qwen/qwen3.8-27b
  Family: qwen3.8
  Tags: code,design
  Location: < (the provider's) >
  Input $/M: 0.5
  Cache $/M:
> Output $/M: cheap
  Subscription $:
  Subscription period: < (none) >

output-price must be a number, got "cheap"
ctrl+s save · esc cancel · tab/↑/↓ move · ←/→ change
```

Run: `python3 /tmp/wt-step3-screens/wt-screen.py --size 80x24 --keys 'n{right}{right}{down}qwen/qwen3.8-27b{down}qw{right}{down}code,design{down}{down}0.5{down}{down}2{ctrl+s}' /tmp/wt-step3-screens/run.sh /tmp/wt-step3-screens/wt model`

Expected: back on the table, the new row under the cursor, the routes pending:

```text
 Agents   [Models]

added openrouter/qwen--qwen3.8-27b
LiteLLM routes pending (sync on quit)

FAMILY   MODEL                                             STATUS   RUNNING

gemma4   ollama/gemma4:9b                                  ok       run
opus     claude/opus                                       ok
qwen3.8  omlx/Gone-4bit                                    missing
qwen3.8  omlx/Qwen3.8-27B-Instruct-MLX-6bit                ok       run
qwen3.8  openrouter/qwen--qwen3.8-27b                      ok
sonnet   openrouter/anthropic--claude-sonnet-4.5-thinking  ok
-        ollama/qwen3:8b                                   new
-        omlx/Qwen3.8-4B-4bit                              new







openrouter/qwen--qwen3.8-27b · ok · tags code,design
enter edit · n add · d remove · r refresh · / filter · tab agents · q quit
```

Then confirm the write: `tail -13 /tmp/wt-step3-screens/home/.config/local-ai/registry.toml`

```toml
[[models]]
id = "openrouter/qwen--qwen3.8-27b"
family = "qwen3.8"
provider_id = "openrouter"
model_name = "qwen/qwen3.8-27b"
tags = [
    "code",
    "design",
]

[models.cost]
input_price_per_million = 0.5
output_price_per_million = 2.0
```

and that no sync ran (the driver kills wt before its exit): `cat /tmp/wt-step3-screens/home/litellm.yaml` prints `model_list: []`.

Run: `python3 /tmp/wt-step3-screens/wt-screen.py --size 80x24 --keys 'n{down}{down}QW{right}' /tmp/wt-step3-screens/run.sh /tmp/wt-step3-screens/wt model | grep Family`

Expected: ` 17 |> Family: qwen3.8` — the suggestion replaced what was typed, capitals and all.

Run: `python3 /tmp/wt-step3-screens/wt-screen.py --size 80x24 --keys 'n{esc}{esc}{down}' /tmp/wt-step3-screens/run.sh /tmp/wt-step3-screens/wt model | tail -2`

Expected: the first `esc` closed the form, the second did nothing — wt is still there, the driver prints no "the command exited" line — and `down` moved the cursor:

```text
 28 |claude/opus · ok · tags code
 74 |enter edit · n add · d remove · r refresh · / filter · tab agents · q quit
```

- [ ] **Step 6: The form and the table at 40x12**

Run: `/tmp/wt-step3-screens/make-home.sh /tmp/wt-step3-screens/home && python3 /tmp/wt-step3-screens/wt-screen.py --size 40x12 --keys 'n' /tmp/wt-step3-screens/run.sh /tmp/wt-step3-screens/wt model`

Expected: twelve lines. Ten fields do not fit under the tab bar and the title; the last row says how many are below, and the hints are the short ones, whole:

```text
 Agents   [Models]
Add a model
> Provider: < ollama >
  Model name:
  Family:
  Tags:
  Location: < (the provider's) >
  Input $/M:
  Cache $/M:
  Output $/M:
  ↓ 2 more
^s save · esc · ↑/↓ · ←/→ change
```

Run: `python3 /tmp/wt-step3-screens/wt-screen.py --size 40x12 --keys 'n{down}{down}{down}{down}{down}{down}{down}{down}{down}' /tmp/wt-step3-screens/run.sh /tmp/wt-step3-screens/wt model`

Expected: with the cursor on the last field the fields have scrolled, and the first row says how many are above:

```text
 Agents   [Models]
Add a model
  ↑ 2 more
  Family:
  Tags:
  Location: < (the provider's) >
  Input $/M:
  Cache $/M:
  Output $/M:
  Subscription $:
> Subscription period: < (none) >
^s save · esc · ↑/↓ · ←/→ change
```

Run: `python3 /tmp/wt-step3-screens/wt-screen.py --size 40x12 --keys '{down}{down}{down}{down}{enter}' /tmp/wt-step3-screens/run.sh /tmp/wt-step3-screens/wt model`

Expected: the title wraps instead of losing the end of the id, and the fixed model name keeps both its ends:

```text
 Agents   [Models]
Edit
openrouter/anthropic--claude-sonnet-4.5-
thinking
  Provider: openrouter
  Model name: anthropic/claude-…thinking
> Family: sonnet
  Tags: code,design
  Location: < (the provider's) >
  Input $/M: 3
  ↓ 4 more
^s save · esc · ↑/↓ · ←/→ change
```

Run: `python3 /tmp/wt-step3-screens/wt-screen.py --size 40x12 /tmp/wt-step3-screens/run.sh /tmp/wt-step3-screens/wt model`

Expected: the table with the short form of its new hints:

```text
 Agents   [Models]

MODEL                STATUS   RUNNING

ollama/gemma4:9b     ok       run
claude/opus          ok
omlx/Gone-4bit       missing

  •••
ollama/gemma4:9b · ok · running
tags code
enter · n · d · r · / · tab · q quit
```

Run: `python3 /tmp/wt-step3-screens/wt-screen.py --size 40x12 --keys '{down}{down}{down}{down}{down}{enter}qwen3{ctrl+s}' /tmp/wt-step3-screens/run.sh /tmp/wt-step3-screens/wt model`

Expected: an ollama model registered where `ollama` is not installed (the wrapper's `PATH` has none). The status takes four lines and the table keeps a row; the note about the capabilities is the short one:

```text
 Agents   [Models]
added ollama/qwen3:8b
added without tool/vision capabilities:
ollama is not installed
LiteLLM routes pending (sync on quit)
MODEL                STATUS   RUNNING

ollama/qwen3:8b      ok

  •••••••
ollama/qwen3:8b · ok
enter · n · d · r · / · tab · q quit
```

Run: `/tmp/wt-step3-screens/make-home.sh /tmp/wt-step3-screens/home && python3 /tmp/wt-step3-screens/wt-screen.py --size 40x12 --keys '{down}{down}{down}{down}{down}{enter}qwen3{ctrl+s}{up}' /tmp/wt-step3-screens/run.sh /tmp/wt-step3-screens/wt model`

Expected: one key later the status is gone and the table has its rows back:

```text
 Agents   [Models]
LiteLLM routes pending (sync on quit)
MODEL                STATUS   RUNNING

ollama/gemma4:9b     ok       run
claude/opus          ok
ollama/qwen3:8b      ok
omlx/Gone-4bit       missing

  ••
claude/opus · ok · tags code
enter · n · d · r · / · tab · q quit
```

If a screen differs from these in a way the checks name, stop and fix the code before going on.

- [ ] **Step 7: Clean up**

```bash
kill "$(cat /tmp/wt-step3-screens/fakes.pid)"
rm -rf /tmp/wt-step3-screens
```

- [ ] **Step 8: Document the form**

In `wt/docs/wt-model.md`, find:

```text
| `d` | remove the selected registry model,
```

and replace it with:

```text
| `enter` | edit the selected registry model; on a `new` row, register it |
| `n` | add a model |
| `d` | remove the selected registry model,
```

In `wt/docs/wt-model.md`, find:

```text
A removal is written to `registry.toml` at once.
```

and replace it with:

```text
### The form

`n`, and `enter`, open a form with the fields `wt model add` and `wt model
edit` take: Provider, Model name, Family, Tags, Location, the three
per-token prices, and the subscription price and period.

| Key | Does |
|---|---|
| `Tab`, `↓`, `Enter` | next field |
| `Shift+Tab`, `↑` | previous field |
| `←` / `→` | change Provider, Location or Subscription period |
| `→` at the end of Family | replace what was typed with the family the registry already has (`Ctrl+N` / `Ctrl+P`: the next and the previous one that matches) |
| `Ctrl+S` | save |
| `Esc` | cancel |

Provider offers every provider in the registry and the ones wt adds a row
for by itself (ollama, omlx, mtplx, openrouter), but not mlx_lm_server: a
pairing is added on the command line. When editing, and when registering a
`new` row, the provider and the model name are fixed. An edit writes only
the fields you changed; saving an untouched form writes nothing. A value
that cannot be used keeps the form open, with the reason and the cursor on
that field. On a terminal too short for every field the fields scroll, and
`↑ N more` / `↓ N more` count the ones off the screen.

A saved form and a removal are each written to `registry.toml` at once.
```

In `wt/docs/internals/tui.md`, find:

```text
wraps its text to the terminal's width.
```

and replace it with:

```text
wraps its text to the terminal's width, and the model form (`models_form.go`, hand-built on `bubbles/textinput` like the agent form) draws as many fields as the terminal has rows for, always including the focused one, with a marker row for the fields above and below (`formWindow`, `TestModelFormFitsTheTerminal`). Its Family field offers the registry's families as suggestions; the form accepts one itself — right, with the cursor at the end of the text, sets the field to the suggestion whole — and bubbles' own accept binding is off: bubbles v1.0.0 accepts wherever the cursor is, keeps the case of what was typed, and panics when a value is set after a suggestion matched.
```

In `wt/CHANGELOG.md`, find:

```text
## Unreleased

### Added
```

and replace it with:

```text
## Unreleased

### Added

- The Models tab of `wt config` adds and edits models: `n` opens a form for a
  new model, `enter` edits the selected one or registers a `new` row under
  the id it already has. The form has the fields `wt model add` takes, offers
  the registry's families as suggestions, and saves with `Ctrl+S` — straight
  to `registry.toml`.
```

In `docs/guides/02-providers-and-models.md`, find:

```text
— by hand, until wt's model editor ships — and download them with each provider's own tool. modelman's TUI, which used to do both, is disabled.
```

and replace it with:

```text
— with `wt model add`, `edit` and `rm`, or the Models tab of `wt config` — and download them with each provider's own tool. modelman's TUI, which used to do both, is disabled.
```

In `docs/guides/02-providers-and-models.md`, find:

```text
"${EDITOR:-vi}" ~/.config/local-ai/registry.toml   # add, change or delete a [[models]] block by hand (Steps 1–4)
```

and replace it with:

```text
wt model                                       # the Models tab of `wt config`: n add, enter edit, d remove (Step 1)
wt model add openrouter <model> --family <f>   # the same without a terminal; also `wt model edit <id> …`, `wt model rm <id>`
wt model list                                  # every registry model, and every local model found that has no entry
```

In `docs/guides/02-providers-and-models.md`, find:

```text
wt litellm sync                                # apply it — run after every hand edit
```

and replace it with:

```text
wt litellm sync                                # apply it — `wt model …` and the tab do this themselves; run it after a hand edit
```

In `docs/guides/02-providers-and-models.md`, find:

```text
**by hand** = adding, editing and removing models and providers in `registry.toml`, family display names, tags. **wt** = `wt model init` (the file and its provider rows),
```

and replace it with:

```text
**wt** = `wt model` (the Models tab of `wt config`) and `wt model add|edit|rm|list` (models: add, edit, remove, list), `wt model init` (the file and its provider rows),
```

In `docs/guides/02-providers-and-models.md`, find:

```text
**llmbench** = benchmarks.
```

and replace it with:

```text
**llmbench** = benchmarks. **By hand** = a provider row wt has no default for, a `local_path` entry ([10-mlx-lm-quantization](10-mlx-lm-quantization.md)), and `[[families]]` display names.
```

In `docs/guides/02-providers-and-models.md`, replace everything from the line `### 1. Edit `registry.toml` by hand` up to, but not including, the line `### 2. Add a cloud provider (OpenRouter)` with:

````text
### 1. Add, edit and remove models with `wt model`

modelman's TUI used to add, edit and delete models. It is disabled: bare `modelman` prints where to go and exits 1. wt does it now, two ways that write the same rows:

```bash
wt model init                    # 1. the file and its provider rows (once)
wt model                         # 2a. the Models tab of `wt config`: n add, enter edit, d remove
wt model add ollama qwen3:8b --family qwen3 --tags code      # 2b. the same from the command line
wt model edit ollama/qwen3:8b --tags code,design
wt model rm ollama/qwen3:8b
wt model list                    # 3. what the registry has, and what is on disk without an entry (STATUS new)
```

1. **`wt model init`** creates `registry.toml` when it is missing and appends the default `[[providers]]` row for each provider it finds a use for (an installed `ollama`/`omlx`/`mtplx`, a provider a model or a configured agent names, a native row per configured agent — `wt model init --help` has the full list). It never changes a row that exists, so running it again is safe. `wt model add` adds a missing default row too, in the same write as the model.
2. **Add, edit, remove.** `wt model add <provider> <name> --family <family>` registers a model under a derived id — the id wt already shows an unregistered local model under, or `<provider>/<name>` with `/` written `--` for a cloud model — and `--id` overrides it. `wt model edit <id>` changes family, tags, location and prices, and nothing else in the row; the id, the provider and the name are fixed. `wt model rm <id>` removes the row and prints where the weights are: wt never deletes them (Step 5). The Models tab does the same from a form. Every flag and key: `wt/docs/wt-model.md`.
3. **Routes follow by themselves.** Each command syncs the LiteLLM routes once after its write, and the tab once when you quit (Step 7). A sync that could not run is a warning; the registry change stands.

What to keep in mind:

- **A model's provider must have a `[[providers]]` row.** `wt model add` refuses a provider that has none and that it has no default row for, and names the file to add it to. A registry that already holds such a model makes `wt`, `wt start` and `wt stop` stop with `config error: model "<id>": unknown provider "<provider>"`, and `wt litellm sync` warns that it cannot route the model; `wt model list`, `wt model edit` and `wt model rm` still work, so the row can be repaired or removed.
- **A few things stay a hand edit of `registry.toml`:** a `[[providers]]` row for a provider wt has no default for (Step 2), a model served from a directory of your own (`[models.fetch] local_path`, guide 10), and `[[families]]` display names (guide 03). The procedure for one: edit the file; run `wt litellm sync --dry-run`, a read-only check that prints `config error: parse …` when the file is no longer valid TOML and otherwise the route changes a sync would make; then `wt litellm sync`, because no tool saw the edit. Steps 2–4 show a block per provider kind; every field name there is one the shared fixture `docs/contracts/registry.sample.toml` pins for both wt and modelman.
- **Comments do not survive.** The first tool that changes the file — a `wt model` command, or a modelman subcommand that writes it (`migrate`, `ollama-catalog sync`, `refresh-prices`, `sync` when it repairs a provider row) — lays the whole file out again without them. Keys neither tool knows are kept.
- **Do not edit while a modelman subcommand is running.** It saves the copy it loaded; when the file changed underneath it, it refuses (`registry.toml changed on disk; reload`) — run that command again.
- **What else the TUI did:** downloads are the provider's own tool now (Step 5), starting and stopping is `wt start`/`wt stop` (Step 7), and its ready toggle has no replacement because none is needed — wt probes the providers for what is on disk.
````

In `docs/guides/02-providers-and-models.md`, find:

```text
`registry.toml` is canonical — add the provider block to `~/.config/local-ai/registry.toml` by hand (Step 1). If `wt model init` already added an `openrouter` row (it does when a configured agent lists openrouter), do not add a second one: that row has
```

and replace it with:

```text
`registry.toml` is canonical. For OpenRouter the provider block is written for you: `wt model init` adds it when a model or a configured agent names openrouter, and the first `wt model add openrouter …` adds it in the same write as the model. The block of any other cloud provider is a hand edit — a provider row is one of the things wt has no command for (Step 1). Do not add a second `openrouter` row: the one wt writes has
```

In `docs/guides/02-providers-and-models.md`, find:

```text
That is all a cloud model needs: there is no download and no ready gate, and the next `wt litellm sync` routes it (Step 7).
```

and replace it with:

```text
The command for it is `wt model add openrouter <org>/<model> --family <family>` (Step 1), which adds the `openrouter` provider row when it is missing, writes a block like this one and syncs the route. The id it derives spells the `/` of the name as `--` (`openrouter/<org>--<model>`); `--id openrouter/<org>/<model>` gives the spelling above. That is all a cloud model needs: there is no download and no ready gate (Step 7).
```

In `docs/guides/02-providers-and-models.md`, find:

```text
Pull the model first (Step 5), then add its block by hand (Step 1) — or leave it out: an entry for a local model is optional (see *A local registry entry is an optional overlay* below). The TUI's add dialog used to fill `model_info` from `ollama show <name>`; by hand, read the capabilities `ollama show <name>` lists and set the matching keys yourself: `tools` → `supports_function_calling = true`, `vision` → `supports_vision = true`.
```

and replace it with:

```text
Pull the model first (Step 5), then register it with `wt model add ollama <name:tag> --family <family>` (Step 1) — or leave it out: an entry for a local model is optional (see *A local registry entry is an optional overlay* below). `wt model add` fills `model_info` from `ollama show <name>` as the TUI's add dialog did: `tools` → `supports_function_calling = true`, `vision` → `supports_vision = true`. If that lookup fails it says so and adds the model without them; set the keys in the block yourself then.
```

In `docs/guides/02-providers-and-models.md`, find:

```text
A **hand edit** of `registry.toml` is the case no tool sees, and it is now how models change (Step 1), so run it yourself after every edit:
```

and replace it with:

```text
`wt model add`, `edit` and `rm` sync once after their write, and the Models tab once when you quit (Step 1). A **hand edit** of `registry.toml` is the case no tool sees, so run it yourself after one:
```

In `docs/guides/02-providers-and-models.md`, find:

```text
add a `[[models]]` block for it by hand under its discovered id
```

and replace it with:

```text
register it with `wt model add <provider> <name> --family <family>` (or `enter` on its row in the Models tab), which keeps its discovered id
```

In `docs/guides/02-providers-and-models.md`, find:

```text
a cloud model — delete its `[[models]]` block from `registry.toml` (Step 1) and run `wt litellm sync`, which drops its row.
```

and replace it with:

```text
a cloud model — `wt model rm <id>` (Step 1), whose sync drops its row.
```

In `docs/guides/04-litellm-config.md`, find:

```text
To take a **cloud** model off the proxy, delete its `[[models]]` block from `registry.toml` (guide 02 Step 1) and run `wt litellm sync`, which drops its row.
```

and replace it with:

```text
To take a **cloud** model off the proxy, remove it from the registry — `wt model rm <id>`, or `d` on its row in the Models tab of `wt config` (guide 02 Step 1) — and the sync that follows drops its row.
```

In `docs/guides/02-providers-and-models.md`, find:

```text
Registry-side probe for a newly added model (after a hand edit — `modelman sync` never adds model ids);
```

and replace it with:

```text
Registry-side probe for a newly added model (after `wt model add` — `modelman sync` never adds model ids);
```

In `docs/guides/02-providers-and-models.md`, find:

```text
change HERE — edit `~/.config/local-ai/registry.toml` by hand (Step 1), not wt's config.
```

and replace it with:

```text
change HERE — with `wt model add|edit|rm` or the Models tab of `wt config` (Step 1), which write `~/.config/local-ai/registry.toml`; wt's own `config.toml` holds no model.
```

In `docs/guides/02-providers-and-models.md`, find:

```text
- **A hand edit is not routed until you sync.** Nothing watches `registry.toml`: run `wt litellm sync` after every edit (Step 7).
```

and replace it with:

```text
- **A hand edit is not routed until you sync.** Nothing watches `registry.toml`: `wt model …` and the Models tab sync for themselves, but after an edit of the file run `wt litellm sync` (Step 7).
```

In `docs/guides/00-config-map.md`, find:

```text
| you by hand (models: add, edit, remove — then `wt litellm sync`), `wt` (`wt model init`: creates it, adds provider rows) and `modelman` (non-interactive commands only — its TUI is disabled) |
```

and replace it with:

```text
| `wt` (`wt model add\|edit\|rm` and the Models tab of `wt config`: models; `wt model init`: creates it, adds provider rows), `modelman` (non-interactive commands only — its TUI is disabled), and you by hand for the few things wt has no command for |
```

In `docs/guides/00-config-map.md`, find:

```text
Until wt's Models tab ships, a model is added, edited or removed by editing this file by hand, then `wt litellm sync` — the procedure and a block per provider kind are in [02-providers-and-models](02-providers-and-models.md) Steps 1–4. `wt` — `wt model init` only, for now: it creates the file when it is missing and appends provider rows, never editing a row that exists.
```

and replace it with:

```text
`wt` — `wt model add`, `edit` and `rm`, and the Models tab of `wt config` (`wt model`), add, change and remove models, each followed by a route sync; `wt model init` creates the file when it is missing and appends provider rows, never editing a row that exists. The procedure is in [02-providers-and-models](02-providers-and-models.md) Step 1, which also lists what stays a hand edit (a provider row wt has no default for, a `local_path` entry, `[[families]]`).
```

In `docs/guides/00-config-map.md`, find:

```text
- **Models change by hand in `registry.toml`, for now.** modelman's TUI is disabled (bare `modelman` says so and exits 1) and wt's model editor has not shipped, so a model is added, edited or removed by editing the file and then running `wt litellm sync` — [02-providers-and-models](02-providers-and-models.md) Steps 1–4 show the procedure and a block per provider kind. `wt` writes the file only in `wt model init`, which adds provider rows; modelman's non-interactive commands rewrite it too, so don't edit while one is running.
```

and replace it with:

```text
- **Models change through `wt model`.** modelman's TUI is disabled (bare `modelman` says so and exits 1); a model is added, edited or removed with `wt model add|edit|rm` or on the Models tab of `wt config` (`wt model`), and the routes follow — [02-providers-and-models](02-providers-and-models.md) Step 1. modelman's non-interactive commands rewrite the file too, so don't hand-edit it while one is running.
```

In `docs/guides/00-config-map.md`, find:

```text
NO live providers/models — those live in `registry.toml`; `wt` joins that file in memory and `wt config` never writes providers or models.
```

and replace it with:

```text
NO live providers/models — those live in `registry.toml`; `wt` joins that file in memory, and nothing writes providers or models into this one: the Models tab of `wt config` and `wt model add|edit|rm` write `registry.toml`.
```

In `docs/guides/03-model-families.md`, find:

```text
set the `family` and `tags` of a model, and a family's display name, by hand in `registry.toml` (modelman's TUI, which used to edit families, is disabled),
```

and replace it with:

```text
set the `family` and `tags` of a model with `wt model edit` or the Models tab of `wt config`, and a family's display name by hand in `registry.toml`,
```

In `docs/guides/03-model-families.md`, find:

```text
| Hand-edit the `family = "…"` line of the model's `[[models]]` block |
```

and replace it with:

```text
| `wt model edit <id> --family <name>`, or the Family field of the Models tab's form |
```

In `docs/guides/03-model-families.md`, find:

```text
| Hand-edit the `tags = […]` line of the model's `[[models]]` block (Step 3) |
```

and replace it with:

```text
| `wt model edit <id> --tags code,design`, or the Tags field of the Models tab's form (Step 3) |
```

In `docs/guides/03-model-families.md`, find:

```text
"${EDITOR:-vi}" ~/.config/local-ai/registry.toml   # all three knobs are hand edits — the procedure is 02-providers-and-models Step 1
```

and replace it with:

```text
wt model edit <id> --family <name> --tags code,design   # family and tags (or: wt model, then enter on the row)
"${EDITOR:-vi}" ~/.config/local-ai/registry.toml          # a [[families]] display name is the one hand edit here
```

In `docs/guides/03-model-families.md`, find:

```text
To move a model to another family, edit its `family` line;
```

and replace it with:

```text
To move a model to another family, `wt model edit <id> --family <name>`;
```

In `docs/guides/03-model-families.md`, find:

```text
following the hand-edit procedure of [02-providers-and-models](02-providers-and-models.md) Step 1.
```

and replace it with:

```text
a hand edit of `registry.toml`, with the procedure in [02-providers-and-models](02-providers-and-models.md) Step 1 under *A few things stay a hand edit* (edit, `wt litellm sync --dry-run` to check the file, `wt litellm sync`).
```

In `docs/guides/03-model-families.md`, find:

```text
Tags are assigned by hand, and always were — modelman's TUI never had a tag editor. To make `wt -T`/tag rotation meaningful, edit the model's `tags` line in `~/.config/local-ai/registry.toml` ([02-providers-and-models](02-providers-and-models.md) Step 1), e.g. `tags = ["code"]` or `tags = ["code", "design"]`.
```

and replace it with:

```text
Tags are set with `wt model edit <id> --tags code` (or `--tags code,design`; `--tags ""` clears them), or in the Tags field of the Models tab's form ([02-providers-and-models](02-providers-and-models.md) Step 1). They are stored as the model's `tags` line in `~/.config/local-ai/registry.toml`, e.g. `tags = ["code"]`.
```

In `docs/guides/03-model-families.md`, find:

```text
`wt` never edits a family or a tag there (its one write, `wt model init`, adds provider rows); change families/tags by hand-editing the file ([02-providers-and-models](02-providers-and-models.md) Step 1) — modelman's TUI is disabled, and its subcommands keep what you wrote when they rewrite the file.
```

and replace it with:

```text
Change a model's family and tags with `wt model edit` or the Models tab ([02-providers-and-models](02-providers-and-models.md) Step 1); a `[[families]]` display name is a hand edit, and nothing reads it today. modelman's TUI is disabled, and its subcommands keep what is there when they rewrite the file.
```

In `docs/guides/06-wt-agents-and-models.md`, find:

```text
(`wt model init` creates it and adds provider rows; models are edited by hand until wt's model editor ships — modelman's TUI is disabled)
```

and replace it with:

```text
(`wt model init` creates it and adds provider rows; `wt model add|edit|rm` and the Models tab of `wt config` manage its models — modelman's TUI is disabled)
```

In `docs/guides/06-wt-agents-and-models.md`, find:

```text
wt's only write to `registry.toml` is `wt model init`, which adds provider rows, and `wt config` never writes providers/models — add, retag, or retire models by hand-editing `registry.toml`, then `wt litellm sync` ([02-providers-and-models](02-providers-and-models.md) Step 1; modelman's TUI is disabled).
```

and replace it with:

```text
The Agents tab of `wt config` never writes providers or models — add, retag or retire models with `wt model add|edit|rm`, or on the editor's Models tab (`wt model`), which write `registry.toml` and sync the routes ([02-providers-and-models](02-providers-and-models.md) Step 1; modelman's TUI is disabled).
```

In `docs/guides/06-wt-agents-and-models.md`, find:

```text
**wt NEVER writes providers/models** — `wt config`'s editor only touches agents and the default tag; registry data is overwritten in-memory on every load and never persisted back
```

and replace it with:

```text
**wt never writes providers or models into this file** — the Agents tab of `wt config` touches only agents and the default tag, and its Models tab and `wt model add|edit|rm` write `registry.toml`; registry data is overwritten in-memory on every load and never persisted back here
```

In `docs/guides/08-maintenance-and-troubleshooting.md`, find:

```text
so a missing route means no sync has run since it was added (a hand-edit of `registry.toml`) or the last sync could not build its row.
```

and replace it with:

```text
so a missing route means no sync has run since it was added (a hand edit of `registry.toml`; `wt model add` syncs for itself) or the last sync could not build its row.
```

In `docs/guides/08-maintenance-and-troubleshooting.md`, find:

```text
a hand edit of `registry.toml` followed by `wt litellm sync` to add, edit or remove a model;
```

and replace it with:

```text
`wt model` (the Models tab of `wt config`) or `wt model add|edit|rm` to add, edit or remove a model;
```

In `docs/guides/10-mlx-lm-quantization.md`, find:

```text
you register the output directory as a `local_path` on the `omlx` provider by hand-editing `registry.toml` (Step 2).
```

and replace it with:

```text
you register the output directory as a `local_path` on the `omlx` provider by hand-editing `registry.toml` (Step 2) — a `local_path` entry is one of the few things wt has no command for — or move it into omlx's model directory and `wt model add omlx <directory name> --family <family>`.
```

In `docs/guides/10-mlx-lm-quantization.md`, find:

```text
#   Next: register it by hand-editing registry.toml — add an [[models]] entry
```

and replace it with:

```text
#   Next: register it. Either hand-edit registry.toml — add an [[models]] entry
```

In `docs/guides/10-mlx-lm-quantization.md`, find:

```text
Register a `local_path`-sourced omlx model by hand-editing `registry.toml` — the way every model is added now that modelman's TUI is disabled ([02-providers-and-models](02-providers-and-models.md) Step 1 has the procedure):
```

and replace it with:

```text
Register a `local_path`-sourced omlx model by hand-editing `registry.toml`. This is one of the few entries wt has no command for: `wt model add` takes a model by the name its provider lists, and this one is in a directory of your own. (The other way is to move the directory into omlx's model directory, `~/.omlx/models/` by default, and run `wt model add omlx <directory name> --family <family>`; no `local_path` is needed then.) wt keeps the key, shows the path in `wt model list`, and takes the model's presence from a stat of it. The block:
```

In `docs/guides/01-initial-setup.md`, find:

```text
# (bare `uv run modelman` used to open a TUI; it is disabled — models are added by hand in registry.toml, guide 02)
```

and replace it with:

```text
# (bare `uv run modelman` used to open a TUI; it is disabled — models are added with `wt model add` or the Models tab of `wt config`, guide 02)
```

In `docs/guides/04-litellm-config.md`, find:

```text
(Its TUI is disabled; a hand edit of `registry.toml` is seen by no tool, so sync after it yourself.)
```

and replace it with:

```text
(Its TUI is disabled. `wt model add|edit|rm` and the Models tab of `wt config` sync for themselves; a hand edit of `registry.toml` is seen by no tool, so sync after it yourself.)
```

In `docs/guides/04-litellm-config.md`, find:

```text
To give it a family and tags, add a `[[models]]` block for it by hand, under its discovered id (guide 02 Steps 1 and 3).
```

and replace it with:

```text
To give it a family and tags, register it with `wt model add <provider> <name> --family <family>`, or `enter` on its row in the Models tab of `wt config`; either keeps its discovered id (guide 02 Steps 1 and 3).
```

In `docs/guides/04-litellm-config.md`, find:

```text
then let `wt litellm sync` write its row. `wt start`/`wt stop` update the routes themselves, and modelman runs that sync after its own changes (`start`/`stop`, `sync`, …); run it by hand after every hand edit of `registry.toml` — which is how a model is added, edited or removed now (guide 02 Step 1) — or after
```

and replace it with:

```text
and the sync that follows writes its row. `wt model add|edit|rm` and the Models tab of `wt config` run that sync after their own write (guide 02 Step 1), `wt start`/`wt stop` update the routes themselves, and modelman runs it after its own changes (`start`/`stop`, `sync`, …); run `wt litellm sync` yourself after a hand edit of `registry.toml`, or after
```

In `docs/guides/06-wt-agents-and-models.md`, find:

```text
set tags by hand in `registry.toml` ([03-model-families](03-model-families.md) Step 3).
```

and replace it with:

```text
set tags with `wt model edit <id> --tags code` or in the Tags field of the Models tab's form ([03-model-families](03-model-families.md) Step 3).
```

In `docs/guides/02-providers-and-models.md`, find:

```text
This is a hand edit like any other (Step 1);
```

and replace it with:

```text
This is a hand edit of the provider row — wt has no command for one (Step 1);
```

In `CLAUDE.md`, find:

```text
until wt's Models tab ships, models are added, edited and removed by hand in `registry.toml` (then `wt litellm sync`), and downloaded with the provider's own tool — `docs/guides/02-providers-and-models.md` Steps 1–5.
```

and replace it with:

```text
models are added, edited and removed with `wt model add|edit|rm` or the Models tab of `wt config` (`wt model`), and downloaded with the provider's own tool — `docs/guides/02-providers-and-models.md` Steps 1–5.
```

In `CLAUDE.md`, find:

```text
whose only caller so far is `wt model init` (create the file, seed provider rows);
```

and replace it with:

```text
called by `wt model init` (create the file, seed provider rows) and by `wt model add|edit|rm` and the Models tab of `wt config` (model rows, `internal/modeladmin`; `wt model list` shows them with live status);
```

In `README.md`, find:

```text
Models are added to `registry.toml` by hand for now: `docs/guides/02-providers-and-models.md` |
```

and replace it with:

```text
Models are managed with `wt model` (the Models tab of `wt config`) and `wt model add\|edit\|rm\|list`: `docs/guides/02-providers-and-models.md` |
```

In `wt/README.md`, find:

```text
models are added by editing `registry.toml` by hand for now (modelman's TUI is disabled), then `wt litellm sync`. `wt` joins the registry in memory with `config.toml`; `wt config` never writes providers or models.
```

and replace it with:

```text
models are listed, added, edited and removed with `wt model list|add|edit|rm`, or on the Models tab of `wt config` (`wt model`) — see `docs/wt-model.md`. `wt` joins the registry in memory with `config.toml`; nothing writes providers or models into `config.toml`.
```

In `wt/docs/configuration.md`, find:

```text
models are added by editing `registry.toml` by hand for now (modelman's TUI is disabled), then `wt litellm sync`.
```

and replace it with:

```text
models are added, edited and removed with `wt model add|edit|rm`, or on the Models tab of `wt config` (`wt model`); each syncs the LiteLLM routes itself (modelman's TUI is disabled).
```

In `docs/reference/provider-artifacts.md`, find:

```text
  (The TUI that issued those deletes is disabled; removing a model is now a
  hand edit of `registry.toml`, which touches no file on disk.)
```

and replace it with:

```text
  (The TUI that issued those deletes is disabled; a model is now removed with
  `wt model rm <id>`, or `d` on its row in the Models tab of `wt config`,
  which removes the registry entry, prints where the weights are and touches
  no file on disk.)
```

In `modelman/README.md`, find:

```text
> until wt's Models tab ships:
```

and replace it with:

```text
> is wt:
```

In `modelman/README.md`, find:

```text
> - **add, edit or remove a model** — edit `registry.toml` by hand, then
>   `wt litellm sync` (the procedure and a `[[models]]` block per provider
>   kind: `../docs/guides/02-providers-and-models.md` Steps 1–4)
```

and replace it with:

```text
> - **add, edit or remove a model** — `wt model` (the Models tab of
>   `wt config`), or `wt model add`, `wt model edit`, `wt model rm`; the
>   routes are synced for you (`../docs/guides/02-providers-and-models.md`
>   Step 1)
```

In `.claude/skills/mlx-lm-quantization/SKILL.md`, find:

```text
and register it in registry.toml by hand (bin/mlx-quantize).
```

and replace it with:

```text
and register it (bin/mlx-quantize): by hand as a local_path entry, or with wt model add once it is in omlx's model directory.
```

In `.claude/skills/mlx-lm-quantization/SKILL.md`, find:

```text
2. Register the output directory by hand-editing `registry.toml` (modelman's TUI is disabled; every model is added by hand until wt's model editor ships). Add an `[[models]]` entry
```

and replace it with:

```text
2. Register it. Either move the output directory into omlx's model directory (`~/.omlx/models/` by default) and run `wt model add omlx <directory name> --family <family>`, or leave it where it is and hand-edit `registry.toml` — a `local_path` entry is one of the few things wt has no command for: add an `[[models]]` entry
```

In `bin/mlx-quantize`, find:

```text
echo "Next: register it by hand-editing registry.toml — add an [[models]] entry"
echo "with provider_id = \"omlx\" and a [models.fetch] local_path = \"$out_dir\""
echo "(absolute path). See docs/guides/10-mlx-lm-quantization.md."
```

and replace it with:

```text
echo "Next: register it. Either hand-edit registry.toml — add an [[models]] entry"
echo "with provider_id = \"omlx\" and a [models.fetch] local_path = \"$out_dir\""
echo "(absolute path) — or move it into omlx's model directory and run"
echo "wt model add omlx <directory name> --family <family>."
echo "See docs/guides/10-mlx-lm-quantization.md."
```

- [ ] **Step 9: Check that nothing still says to add a model by hand**

The search is wider than the edits above: every guide, reference page, README, `CLAUDE.md`, wt doc and skill, for the ways the old wording said it — "by hand", "delete its … block", "never writes", "for now". Run, from the monorepo root:

```bash
grep -rn -i -E --exclude-dir=superpowers \
  'by hand|hand edit|hand-edit|delete its .*block|never writes|only write|one writer|model editor|for now' \
  docs/guides docs/reference README.md CLAUDE.md wt/README.md wt/CLAUDE.md wt/docs modelman/README.md .claude/skills bin/mlx-quantize \
  | grep -i -E 'registry\.toml|\[\[models\]\]' | cut -d: -f1,2 | LC_ALL=C sort
```

Expected: these 35 lines, and no other. (They are the lines of the files as this task leaves them on top of PRs A to E; if `main` has moved under one of those files the numbers shift, and what counts is that every line found is one of the kinds below.)

```text
.claude/skills/adding-a-benchmark-backend/SKILL.md:59
.claude/skills/mlx-lm-quantization/SKILL.md:12
.claude/skills/mlx-lm-quantization/SKILL.md:9
CLAUDE.md:32
bin/mlx-quantize:75
docs/guides/00-config-map.md:15
docs/guides/00-config-map.md:19
docs/guides/00-config-map.md:313
docs/guides/00-config-map.md:38
docs/guides/02-providers-and-models.md:281
docs/guides/02-providers-and-models.md:372
docs/guides/02-providers-and-models.md:57
docs/guides/02-providers-and-models.md:81
docs/guides/02-providers-and-models.md:88
docs/guides/03-model-families.md:206
docs/guides/03-model-families.md:3
docs/guides/03-model-families.md:32
docs/guides/03-model-families.md:37
docs/guides/03-model-families.md:81
docs/guides/04-litellm-config.md:185
docs/guides/04-litellm-config.md:188
docs/guides/04-litellm-config.md:31
docs/guides/06-wt-agents-and-models.md:186
docs/guides/06-wt-agents-and-models.md:279
docs/guides/06-wt-agents-and-models.md:96
docs/guides/08-maintenance-and-troubleshooting.md:160
docs/guides/10-mlx-lm-quantization.md:22
docs/guides/10-mlx-lm-quantization.md:49
docs/guides/10-mlx-lm-quantization.md:67
docs/guides/10-mlx-lm-quantization.md:7
modelman/README.md:341
wt/CLAUDE.md:167
wt/CLAUDE.md:175
wt/docs/wt-model.md:123
wt/docs/wt-stats.md:242
```

None of them tells a reader to add, edit or remove an ordinary model by editing `registry.toml`. What each is about:

- a `local_path` entry, which wt has no command for: guide 10 lines 7, 22 and 49, the `mlx-lm-quantization` skill's line 9, `bin/mlx-quantize`;
- until PR G, the pairing: guide 10 line 67 and the `mlx-lm-quantization` skill's line 12;
- a `[[families]]` display name: guide 03, all five lines;
- a provider row wt has no default for: guide 02 lines 57, 81 and 88, guide 00 lines 15 and 38, `wt/CLAUDE.md` line 175, the `adding-a-benchmark-backend` skill;
- "after a hand edit, run `wt litellm sync`": guide 02 lines 281 and 372, guide 04 lines 31, 185 and 188, guide 08, `modelman/README.md`, and the heading "What stays a hand edit" in `wt/docs/wt-model.md`;
- a file that is not the registry: guide 00 line 19 (the legacy `config.yaml`, always hand-written) and line 313 (do not hand-edit a managed row of LiteLLM's `config.yaml`), `CLAUDE.md` (modelman never writes LiteLLM's `config.yaml`);
- what never writes models into wt's own `config.toml`: guide 06 lines 96, 186 and 279, and `wt/CLAUDE.md` line 167 (`config.Load` never writes the registry);
- the repair wt names for a registry it cannot parse, "fix that file by hand": `wt/docs/wt-stats.md`.

A line that is not in the list is a line this task missed, or one a later change to `main` added. Read it: if it sends a reader to `registry.toml` to add, change or remove a model, replace that instruction with the command for it — `wt model add <provider> <name> --family <family>`, `wt model edit <id> --family <f>` or `--tags <a,b>`, `wt model rm <id>` — and name the Models tab of `wt config` beside it, as guide 02's Step 1 does; then run the search again.

Then `bash -n bin/mlx-quantize && make lint`. Expected: exit 0 (`bin/mlx-quantize` is in the Makefile's `SHELL_SCRIPTS`).

- [ ] **Step 10: Verify the PR and commit**

From `wt/`:

```bash
test -z "$(gofmt -l .)" && go vet ./... && go test -count=1 ./... && make check
```

Expected: every package `ok`; `make check` ends without an error. From `modelman/`: `make check && make test`. Expected: `Success: no issues found`, and every test passes (1,281 at the time this plan was written). From the monorepo root: `make test-all && make check-links`. Expected: exit 0.

```bash
git add wt/docs wt/CHANGELOG.md wt/README.md docs/guides docs/reference/provider-artifacts.md CLAUDE.md README.md modelman/README.md bin/mlx-quantize
git add -f .claude/skills/mlx-lm-quantization/SKILL.md
git commit -m "docs: models are managed with wt model and the Models tab, not by hand"
```

(`git add -f`: a global ignore covers `.claude/`; the repository tracks `.claude/skills/`.)

- [ ] **Step 11: Hand off**

Stop here. Tell the owner the branch is ready, what `make test-all` printed, and what Steps 3 to 6 showed, with the captured screens. Say what was not looked at: a terminal with colour, and an add of an ollama model against a running daemon (with the wrapper's `PATH` the lookup cannot run, so the form's capability path was seen only in tests). Push and open the PR only after the owner's OK. Suggested title: `feat(wt): add, register and edit models from the Models tab`.

---

## PR G — pairings and their hints

Branch `feat/wt-model-pairings`. Needs PR F on `main` (its tab test edits a pairing through the form).

An mlx_lm_server model is a target and a draft served together by `mlx_lm.server --draft-model`. wt has no engine for one and gets none in this step. What changes: wt can register a pairing, and the two messages that told the user to run `modelman start <id>` name the llmbench command that starts it beside the other models, with the pairing's own target and draft.

Create the branch from the monorepo root:

```bash
git fetch origin
git switch -c feat/wt-model-pairings origin/main
```

### Task 17: The hints name `llmbench provider isolate --solo`

**Files:**
- Modify: `wt/internal/catalog/catalog_test.go` (`TestBlockReasonNamesTheFix`, `TestMissingReason`, one call at line 131; new `TestPairingStartCommand`)
- Modify: `wt/cmd/wt/model_cmds_test.go:254-264`, `wt/cmd/wt/resolve_test.go:236-256`, `wt/internal/tui/modeltable_test.go:98-114` (each pinned the old hint)
- Modify: `wt/internal/catalog/catalog.go:163-190` (`MissingReason`), `:238-245` (`BlockReason`)
- Modify: the five callers of `MissingReason`: `wt/cmd/wt/resolve.go:89`, `start_json.go:57`, `model_cmds.go` (in `runStart`), `smoke.go:351`, `wt/internal/tui/app.go:856`

**Interfaces:**
- Consumes: `config.Model.Fetch`, `Model.Draft`, `ModelArtifact.Target()` (Task 3); existing: `localmodels.RunningOnly`, `config.IndexModelByID`
- Produces (package `catalog`):
  - `func PairingStartCommand(target, draft string) string` — `llmbench provider isolate --solo mlx_lm_server <target> --draft <draft>`; an empty side is spelled `<target>` or `<draft>`
  - `func MissingReason(cfg *config.Config, snap *localmodels.Snapshot, id string) string` — **a new first parameter**; `cfg` may be nil
  - `(Row).BlockReason()` unchanged in signature; its text for a pairing is ``local model "<id>" is not running, and wt cannot start an mlx_lm_server pairing — start it with `<command>` ``, and for any other provider with no engine ``… and wt cannot start provider "<id>" — start it with that provider's own tool``

- [ ] **Step 1: Write the failing tests**

```diff
--- a/wt/internal/catalog/catalog_test.go
+++ b/wt/internal/catalog/catalog_test.go
@@ -128,7 +128,7 @@ func TestBuildHiddenRegistryIDShadowsDiscovered(t *testing.T) {
 	if len(rows) != 0 {
 		t.Fatalf("rows = %v, want none (the hidden registry id shadows the same-id discovered artifact)", rowIDs(rows))
 	}
-	if got := MissingReason(inv, "ollama/foo"); !strings.Contains(got, "is not on disk") {
+	if got := MissingReason(nil, inv, "ollama/foo"); !strings.Contains(got, "is not on disk") {
 		t.Errorf("MissingReason = %q, want the not-on-disk reason for the pin", got)
 	}
 }
@@ -275,12 +275,23 @@ func TestRowActionRules(t *testing.T) {
 }
 
 // TestBlockReasonNamesTheFix verifies the status text for a blocked row names
-// what to do — `modelman start <id>` for a provider wt cannot start — and is
-// "" for rows that are not blocked.
+// what to do: for an mlx_lm_server pairing, the llmbench command that starts
+// it beside the other models, with the row's own target and draft; for any
+// other provider wt has no engine for, that provider's own tool. No reason
+// names modelman, which is being retired. It is "" for rows that are not
+// blocked.
 func TestBlockReasonNamesTheFix(t *testing.T) {
-	mlx := Row{Location: config.LocationLocal, Status: StatusOK, Model: config.Model{ID: "mlx_lm_server/p", ProviderID: "mlx_lm_server"}}
-	if h := mlx.BlockReason(); !strings.Contains(h, "modelman start mlx_lm_server/p") {
-		t.Errorf("mlx_lm_server reason = %q", h)
+	mlx := Row{Location: config.LocationLocal, Status: StatusOK, Model: config.Model{
+		ID: "mlx_lm_server/p", ProviderID: "mlx_lm_server",
+		Fetch: config.ModelArtifact{Repo: "org/target"}, Draft: config.ModelArtifact{LocalPath: "/models/draft"},
+	}}
+	want := "local model \"mlx_lm_server/p\" is not running, and wt cannot start an mlx_lm_server pairing — start it with `llmbench provider isolate --solo mlx_lm_server org/target --draft /models/draft`"
+	if h := mlx.BlockReason(); h != want {
+		t.Errorf("mlx_lm_server reason = %q\nwant %q", h, want)
+	}
+	other := Row{Location: config.LocationLocal, Status: StatusOK, Model: config.Model{ID: "llamacpp/old", ProviderID: "llamacpp"}}
+	if h := other.BlockReason(); h != `local model "llamacpp/old" is not running, and wt cannot start provider "llamacpp" — start it with that provider's own tool` {
+		t.Errorf("reason for a provider with no engine = %q", h)
 	}
 	if h := (Row{Location: config.LocationCloud}).BlockReason(); h != "" {
 		t.Errorf("cloud reason = %q, want empty", h)
@@ -290,6 +301,19 @@ func TestBlockReasonNamesTheFix(t *testing.T) {
 	}
 }
 
+// TestPairingStartCommand pins the command the hints print: --solo, so the
+// other local providers keep running (llmbench stops them without it), the
+// target as the positional and the draft behind --draft, and a placeholder
+// for a side the registry row does not record.
+func TestPairingStartCommand(t *testing.T) {
+	if got, want := PairingStartCommand("org/t", "org/d"), "llmbench provider isolate --solo mlx_lm_server org/t --draft org/d"; got != want {
+		t.Errorf("PairingStartCommand = %q, want %q", got, want)
+	}
+	if got, want := PairingStartCommand("", ""), "llmbench provider isolate --solo mlx_lm_server <target> --draft <draft>"; got != want {
+		t.Errorf("with nothing recorded = %q, want %q", got, want)
+	}
+}
+
 // TestFindReportsDiscoveredRows verifies Find looks a row up among ALL rows,
 // discovered ones included — the fix that lets `wt -M <discovered-id>` work.
 // A registry-only lookup would send users to a registry edit they do not need.
@@ -400,7 +424,8 @@ func TestBuildMarksUnmappedCloudRows(t *testing.T) {
 
 // TestMissingReason verifies the message a pin of a hidden registry model
 // gets (#179 Phase B): an overlay the probe confirmed is not on disk says so,
-// a non-running mlx_lm_server pairing names `modelman start`, and everything
+// a non-running mlx_lm_server pairing names the llmbench command that starts
+// it (with its target and draft, read from the registry row), and everything
 // that has a row — or that the probe could not vouch for — gets "" so the
 // caller falls back to its generic wording. Without it, `wt -M <absent-id>`
 // would say "not in the eligible list" and send the user hunting for a
@@ -416,19 +441,27 @@ func TestMissingReason(t *testing.T) {
 	}}
 	cases := map[string]string{
 		"omlx/gone":         "omlx/gone is not on disk — pull or download it first",
-		"mlx_lm_server/p":   "local model \"mlx_lm_server/p\" is not running — start it with `modelman start mlx_lm_server/p`",
+		"mlx_lm_server/p":   "local model \"mlx_lm_server/p\" is not running, and wt cannot start an mlx_lm_server pairing — start it with `llmbench provider isolate --solo mlx_lm_server org/target --draft org/draft`",
 		"omlx/here":         "",
 		"mlx_lm_server/run": "",
 		"ollama/unknown":    "",
 		"omlx/disc":         "",
 		"nope/x":            "",
 	}
+	cfg := &config.Config{Models: []config.Model{{
+		ID: "mlx_lm_server/p", ProviderID: "mlx_lm_server", ModelName: "target+draft-draft",
+		Fetch: config.ModelArtifact{Repo: "org/target"}, Draft: config.ModelArtifact{Repo: "org/draft"},
+	}}}
 	for id, want := range cases {
-		if got := MissingReason(snap, id); got != want {
+		if got := MissingReason(cfg, snap, id); got != want {
 			t.Errorf("MissingReason(%s) = %q, want %q", id, got, want)
 		}
 	}
-	if got := MissingReason(nil, "omlx/gone"); got != "" {
+	// With no config to read the pairing from, the command keeps its shape.
+	if got := MissingReason(nil, snap, "mlx_lm_server/p"); !strings.HasSuffix(got, "`llmbench provider isolate --solo mlx_lm_server <target> --draft <draft>`") {
+		t.Errorf("MissingReason without a config = %q, want the command with placeholders", got)
+	}
+	if got := MissingReason(cfg, nil, "omlx/gone"); got != "" {
 		t.Errorf("MissingReason(nil snapshot) = %q, want empty (no probe ran)", got)
 	}
 }
```

Three tests elsewhere pinned the words `modelman start`:

```diff
--- a/wt/cmd/wt/model_cmds_test.go
+++ b/wt/cmd/wt/model_cmds_test.go
@@ -251,7 +251,7 @@ func TestStartIdleModelStartsIt(t *testing.T) {
 // local models with no row exit with an error naming the problem and never
 // start anything. The two hidden models (omlx/c not on disk, a stopped
 // mlx_lm_server pairing) get their reason only through catalog.MissingReason
-// — the "modelman start" hint for the pairing, since wt cannot start it.
+// — the llmbench command for the pairing, since wt cannot start it.
 func TestStartInvalidArgErrors(t *testing.T) {
 	cfg, req := startFixture(t)
 	cfg.Providers = append(cfg.Providers, config.Provider{ID: "mlx_lm_server", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}})
@@ -261,7 +261,7 @@ func TestStartInvalidArgErrors(t *testing.T) {
 	)
 	cases := map[string]string{
 		"ollama/nope:9": "unknown model", "openrouter/x": "not a local model", "omlx/c": "not on disk",
-		"mlx_lm_server/p": "modelman start mlx_lm_server/p",
+		"mlx_lm_server/p": "llmbench provider isolate --solo mlx_lm_server <target> --draft <draft>",
 	}
 	for arg, want := range cases {
 		err := runStart(io.Discard, cfg, themes.Theme{}, arg, false)
```

```diff
--- a/wt/cmd/wt/resolve_test.go
+++ b/wt/cmd/wt/resolve_test.go
@@ -234,9 +234,10 @@ func TestResolveModelPinOnAbsentLocalReportsReason(t *testing.T) {
 }
 
 // TestResolveModelPinOnNoEngineProviderReportsReason verifies pinning a local
-// model whose provider wt cannot start (mlx_lm_server) reports the
-// `modelman start` hint instead of silently doing nothing — the user needs to
-// know which tool owns that provider's lifecycle.
+// model whose provider wt cannot start (mlx_lm_server) reports the llmbench
+// command that starts the pairing, with the target and the draft the registry
+// row records, instead of silently doing nothing — the user needs to know
+// which tool owns that provider's lifecycle.
 func TestResolveModelPinOnNoEngineProviderReportsReason(t *testing.T) {
 	stubProbeInventory(t, localmodels.Snapshot{
 		Providers: map[string]localmodels.Status{"mlx_lm_server": localmodels.StatusOK},
@@ -246,13 +247,14 @@ func TestResolveModelPinOnNoEngineProviderReportsReason(t *testing.T) {
 	})
 	cfg := &config.Config{
 		Providers: []config.Provider{{ID: "mlx_lm_server", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}}},
-		Models:    []config.Model{{ID: "mlx_lm_server/p", ProviderID: "mlx_lm_server", ModelName: "p", Tags: []string{"code"}}},
-		Agents:    []config.Agent{{Name: "pi", SupportedProviders: []string{"mlx_lm_server"}}},
+		Models: []config.Model{{ID: "mlx_lm_server/p", ProviderID: "mlx_lm_server", ModelName: "p", Tags: []string{"code"},
+			Fetch: config.ModelArtifact{Repo: "org/target"}, Draft: config.ModelArtifact{Repo: "org/draft"}}},
+		Agents: []config.Agent{{Name: "pi", SupportedProviders: []string{"mlx_lm_server"}}},
 	}
 
 	_, _, err := resolveModel("pi", cfg, "", "", "mlx_lm_server/p")
-	if err == nil || !strings.Contains(err.Error(), "modelman start mlx_lm_server/p") {
-		t.Errorf("err = %v, want the modelman start hint", err)
+	if err == nil || !strings.Contains(err.Error(), "`llmbench provider isolate --solo mlx_lm_server org/target --draft org/draft`") || strings.Contains(err.Error(), "modelman") {
+		t.Errorf("err = %v, want the llmbench command for this pairing", err)
 	}
 }
 
```

```diff
--- a/wt/internal/tui/modeltable_test.go
+++ b/wt/internal/tui/modeltable_test.go
@@ -95,8 +95,8 @@ func TestRenderTableSurveySegmentTrailing(t *testing.T) {
 }
 
 // TestRenderTableBlockedAndMarkers verifies a non-running omlx row is a
-// start row, a provider wt cannot start is blocked with the modelman start
-// reason, only the last-launched row is marked, and the in-use ref count is
+// start row, a provider wt cannot start is blocked with the command that
+// starts it, only the last-launched row is marked, and the in-use ref count is
 // picked up.
 func TestRenderTableBlockedAndMarkers(t *testing.T) {
 	tbl := renderTable(tableTestRows(), nil, "", map[string]int{"openrouter/cheap": 2}, "omlx/Qwen3.8-27B-4bit")
@@ -109,8 +109,8 @@ func TestRenderTableBlockedAndMarkers(t *testing.T) {
 	if tbl.items[3].blocked == "" || tbl.items[3].start {
 		t.Errorf("mlx_lm_server row: start = %v blocked = %q, want blocked and not startable", tbl.items[3].start, tbl.items[3].blocked)
 	}
-	if !strings.Contains(tbl.items[3].blocked, "modelman start mlx_lm_server/pair") {
-		t.Errorf("mlx_lm_server row blocked = %q, want the modelman start reason", tbl.items[3].blocked)
+	if !strings.Contains(tbl.items[3].blocked, "llmbench provider isolate --solo mlx_lm_server") {
+		t.Errorf("mlx_lm_server row blocked = %q, want the llmbench command", tbl.items[3].blocked)
 	}
 	if !tbl.items[1].marked || tbl.items[0].marked {
 		t.Error("only the last-launched row is marked")
```

- [ ] **Step 2: Run them to see them fail**

Run, from `wt/`: `go test -count=1 ./internal/catalog ./cmd/wt ./internal/tui -run 'Reason|PairingStartCommand|TestStartInvalidArgErrors|TestResolveModelPinOnNoEngine|TestRenderTableBlockedAndMarkers'`

Expected: `internal/catalog` does not build (`MissingReason` takes two arguments), and the three tests in the other two packages fail on the old wording.

```text
FAIL	github.com/ohanaverse/local-ai-setup/wt/internal/catalog [build failed]
--- FAIL: TestStartInvalidArgErrors (0.00s)
    model_cmds_test.go:269: runStart("mlx_lm_server/p") err = local model "mlx_lm_server/p" is not running — start it with `modelman start mlx_lm_server/p`, want "llmbench provider isolate --solo mlx_lm_server <target...
--- FAIL: TestResolveModelPinOnNoEngineProviderReportsReason (0.00s)
    resolve_test.go:257: err = local model "mlx_lm_server/p" is not running — start it with `modelman start mlx_lm_server/p`, want the llmbench command for this pairing
FAIL
FAIL	github.com/ohanaverse/local-ai-setup/wt/cmd/wt	0.162s
--- FAIL: TestRenderTableBlockedAndMarkers (0.00s)
    modeltable_test.go:113: mlx_lm_server row blocked = "local model \"mlx_lm_server/pair\" is not running — start it with `modelman start mlx_lm_server/pair`", want the llmbench command
FAIL
FAIL	github.com/ohanaverse/local-ai-setup/wt/internal/tui	0.180s
FAIL
# github.com/ohanaverse/local-ai-setup/wt/internal/catalog [github.com/ohanaverse/local-ai-setup/wt/internal/catalog.test]
```

- [ ] **Step 3: Rewrite the two reasons**

```diff
--- a/wt/internal/catalog/catalog.go
+++ b/wt/internal/catalog/catalog.go
@@ -160,18 +160,46 @@ func listed(e localmodels.Entry) bool {
 	return false
 }
 
+// PairingStartCommand is the command that starts an mlx_lm_server
+// target+draft pairing beside the other local models: wt has no engine for
+// one, llmbench does. --solo is what keeps the other providers running. An
+// empty target or draft (a row with no fetch or draft table) is spelled as a
+// placeholder, so the hint still shows the command's shape.
+func PairingStartCommand(target, draft string) string {
+	if target == "" {
+		target = "<target>"
+	}
+	if draft == "" {
+		draft = "<draft>"
+	}
+	return "llmbench provider isolate --solo mlx_lm_server " + target + " --draft " + draft
+}
+
+// notRunning is the one wording for a local model that is not running and
+// that wt cannot start: an mlx_lm_server pairing gets the command that
+// starts it; any other provider with no lifecycle engine in wt is named, and
+// left to its own tool.
+func notRunning(m config.Model) string {
+	if localmodels.RunningOnly(m.ProviderID) {
+		return fmt.Sprintf("local model %q is not running, and wt cannot start an mlx_lm_server pairing — start it with `%s`",
+			m.ID, PairingStartCommand(m.Fetch.Target(), m.Draft.Target()))
+	}
+	return fmt.Sprintf("local model %q is not running, and wt cannot start provider %q — start it with that provider's own tool", m.ID, m.ProviderID)
+}
+
 // MissingReason is the message for a pin of a registry local model that has
 // no row (#179 Phase B: local rows come only from the inventory): "<id> is
 // not on disk" when the probe confirmed its artifact is missing, or the
-// `modelman start` hint for a non-running model of a running-only family
+// command that starts it for a non-running model of a running-only family
 // (localmodels.RunningOnly: an mlx_lm_server pairing), which wt can neither
-// discover nor start. It is "" for any id the snapshot cannot vouch
-// for — a model with a row, an unknown artifact, a discovered or unknown id,
-// or a nil snapshot (no probe ran) — so the caller keeps its own wording.
-// `wt start`, `wt smoke` and `wt -M` (both paths) consult it only when
-// catalog.Find misses; `wt -M` additionally only for a pin in the agent's
-// eligible list, so a pin the agent cannot use at all is never told to pull.
-func MissingReason(snap *localmodels.Snapshot, id string) string {
+// discover nor start. cfg is where the pairing's target and draft are read
+// from. It is "" for any id the snapshot cannot vouch for — a model with a
+// row, an unknown artifact, a discovered or unknown id, or a nil snapshot
+// (no probe ran) — so the caller keeps its own wording. `wt start`, `wt
+// smoke` and `wt -M` (both paths) consult it only when catalog.Find misses;
+// `wt -M` additionally only for a pin in the agent's eligible list, so a pin
+// the agent cannot use at all is never told to pull.
+func MissingReason(cfg *config.Config, snap *localmodels.Snapshot, id string) string {
 	if snap == nil {
 		return ""
 	}
@@ -181,7 +209,13 @@ func MissingReason(snap *localmodels.Snapshot, id string) string {
 		}
 		switch {
 		case localmodels.RunningOnly(e.ProviderID):
-			return fmt.Sprintf("local model %q is not running — start it with `modelman start %s`", id, id)
+			m := config.Model{ID: id, ProviderID: e.ProviderID}
+			if cfg != nil {
+				if i := config.IndexModelByID(cfg.Models, id); i >= 0 {
+					m = cfg.Models[i]
+				}
+			}
+			return notRunning(m)
 		case e.ArtifactKnown && e.Artifact == "":
 			return fmt.Sprintf("%s is not on disk — pull or download it first", id)
 		}
@@ -236,12 +270,12 @@ func (r Row) Action() Action {
 }
 
 // BlockReason is the status line for an ActionBlock row ("" otherwise): a
-// provider wt cannot start needs modelman.
+// local model that is not running, on a provider wt has no engine to start.
 func (r Row) BlockReason() string {
 	if r.Action() != ActionBlock {
 		return ""
 	}
-	return fmt.Sprintf("local model %q is not running — start it with `modelman start %s`", r.Model.ID, r.Model.ID)
+	return notRunning(r.Model)
 }
 
 // RefusedByRoute reports whether the row must be refused because of how it
```

- [ ] **Step 4: Hand `MissingReason` the config at its five call sites**

```diff
--- a/wt/cmd/wt/resolve.go
+++ b/wt/cmd/wt/resolve.go
@@ -86,7 +86,7 @@ func resolveModel(agent string, cfg *config.Config, tags, family, pinned string)
 			// the eligible list (unsupported provider, filtered by -T/-F)
 			// would be told to pull, then refused as ineligible after.
 			if config.IndexModelByID(eligible, pinned) >= 0 {
-				if reason := catalog.MissingReason(&snap, pinned); reason != "" {
+				if reason := catalog.MissingReason(cfg, &snap, pinned); reason != "" {
 					return config.Model{}, launchable, errors.New(reason)
 				}
 			}
```

```diff
--- a/wt/cmd/wt/start_json.go
+++ b/wt/cmd/wt/start_json.go
@@ -54,7 +54,7 @@ func runStartJSON(out io.Writer, cfg *config.Config, id string, plan, replace bo
 	rows, snap := localRowsSnap(cfg)
 	row, ok := catalog.Find(rows, id)
 	if !ok {
-		if reason := catalog.MissingReason(&snap, id); reason != "" {
+		if reason := catalog.MissingReason(cfg, &snap, id); reason != "" {
 			return errors.New(reason)
 		}
 		return fmt.Errorf("unknown model %q", id)
```

```diff
--- a/wt/cmd/wt/model_cmds.go
+++ b/wt/cmd/wt/model_cmds.go
@@ -490,7 +490,7 @@ func runStart(out io.Writer, cfg *config.Config, theme themes.Theme, id string,
 	}
 	row, ok := catalog.Find(rows, id)
 	if !ok {
-		if reason := catalog.MissingReason(&snap, id); reason != "" {
+		if reason := catalog.MissingReason(cfg, &snap, id); reason != "" {
 			return errors.New(reason)
 		}
 		if config.IndexModelByID(cfg.Models, id) >= 0 {
```

```diff
--- a/wt/cmd/wt/smoke.go
+++ b/wt/cmd/wt/smoke.go
@@ -348,7 +348,7 @@ func resolveSmokeModel(cfg *config.Config, theme themes.Theme, modelID string) (
 			if row.Action() == catalog.ActionBlock {
 				return smokeTarget{}, errors.New(row.BlockReason())
 			}
-		} else if reason := catalog.MissingReason(&snap, modelID); reason != "" {
+		} else if reason := catalog.MissingReason(cfg, &snap, modelID); reason != "" {
 			return smokeTarget{}, errors.New(reason)
 		}
 		if config.IndexModelByID(cfg.Models, modelID) >= 0 {
```

```diff
--- a/wt/internal/tui/app.go
+++ b/wt/internal/tui/app.go
@@ -853,7 +853,7 @@ func (m model) enterModelPhase(agent string, models []config.Model, firstTag str
 			// the eligible list (unsupported provider, filtered by -T/-F)
 			// would be told to pull, then refused as ineligible after.
 			if config.IndexModelByID(models, m.pinnedModel) >= 0 {
-				if reason := catalog.MissingReason(snap, m.pinnedModel); reason != "" {
+				if reason := catalog.MissingReason(m.cfg, snap, m.pinnedModel); reason != "" {
 					return routeBack(reason)
 				}
 			}
```

- [ ] **Step 5: Run the tests**

Run, from `wt/`: `go build ./... && go test -count=1 ./internal/catalog ./cmd/wt ./internal/tui`

Expected: all three `ok`. Then check that no reason wt prints for a model still names modelman:

```bash
grep -rn 'modelman start' wt/cmd wt/internal --include='*.go' | grep -v '_test.go'
```

Run it from the monorepo root. Expected: one line, in `wt/cmd/wt/model_cmds.go`, in `wt warm`'s help text (it describes what modelman's own start does when omlx refuses a keyless request; the spec's Step 6 strings sweep owns it). No line in `internal/catalog`.

- [ ] **Step 6: Commit**

```bash
cd wt && test -z "$(gofmt -l .)" && go vet ./... && cd ..
git add wt/internal/catalog wt/cmd/wt wt/internal/tui
git commit -m "feat(wt): a pairing wt cannot start is told how llmbench starts it"
```

### Task 18: `wt model add mlx_lm_server <target> --draft <draft>`

**Files:**
- Modify: `wt/internal/modeladmin/apply_test.go` (`TestAddRefusals`' pairing case becomes two; new `TestAddAPairing`, `TestPairingName`), `wt/cmd/wt/model_write_test.go` (new `TestModelAddAPairing`)
- Create: `wt/internal/configeditor/models_pairing_test.go`
- Modify: `wt/internal/modeladmin/fields.go` (`FieldDraft`), `wt/internal/modeladmin/apply.go` (`AddRequest.Draft`; `planAdd`, `Add`, `sameArtifact`; new `PairingName`, `artifactTable`, `samePairing`), `wt/cmd/wt/model_write.go` (`--draft`; the start line)
- Modify: `wt/docs/wt-model.md`, `wt/docs/internals/local-models.md`, `docs/guides/10-mlx-lm-quantization.md`, `.claude/skills/mlx-lm-quantization/SKILL.md`, `wt/CHANGELOG.md`

**Interfaces:**
- Consumes: Task 7's `Add`, `AddRequest`, `FieldError`, and its unexported `planAdd(req AddRequest) (addPlan, error)`, `addPlan{req, id, set}`, `sameArtifact(models []*tomlw.Table, providerID, modelName string) string` (the id of a row with the same provider family and `model_name`, or `""`), `mustGet`, `tableString`; Task 17's `catalog.PairingStartCommand`; Task 14's form and Task 11's tab test helpers
- Produces:
  - `modeladmin.FieldDraft = "draft"`; `AddRequest.Draft string`
  - `func PairingName(target, draft string) string` — `<target's last segment>+draft-<draft's last segment>`
  - `Add` on an mlx_lm_server provider: needs `Draft`; `ModelName` is the target; writes `fetch` and `draft` as `repo` or `local_path`; a `Draft` on any other provider is a `FieldError` on `FieldDraft`
  - `addPlan` gains `target, draft map[string]any` (a pairing's two sides as their tables; nil otherwise)
  - `func samePairing(models []*tomlw.Table, target, draft map[string]any) string` — the id of an mlx_lm_server row with the same two sides (the same `repo` or `local_path` on each, a trailing `/` aside), or `""`. `Add` refuses that as the same pairing whatever `--id` says; `sameArtifact` now returns `""` for an mlx_lm_server provider, so two different pairings may share a `model_name`
  - When a pairing's derived id is taken by a different pairing, `Add` returns `config.ErrModelExists` wrapped with `…; pass --id mlx_lm_server/<name> to register this one under an id of its own`
  - `wt model add … --draft <draft>`, which also prints `start it with: <catalog.PairingStartCommand(target, draft)>`

- [ ] **Step 1: Write the failing tests**

```diff
--- a/wt/internal/modeladmin/apply_test.go
+++ b/wt/internal/modeladmin/apply_test.go
@@ -249,7 +249,8 @@ func TestAddRefusals(t *testing.T) {
 		{"NaN", func(r *AddRequest) { r.SubscriptionPrice = ptr("nan") }, FieldSubscriptionPrice, "subscription-price must be finite"},
 		{"a subscription with no period", func(r *AddRequest) { r.SubscriptionPrice = ptr("20") }, FieldSubscriptionPeriod, "a subscription price needs a subscription period (month or year)"},
 		{"a period that is not one", func(r *AddRequest) { r.SubscriptionPeriod = ptr("week") }, FieldSubscriptionPeriod, `subscription period must be month or year, got "week"`},
-		{"a pairing", func(r *AddRequest) { r.ProviderID = "mlx_lm_server" }, FieldProvider, "target+draft pairing"},
+		{"a pairing with no draft", func(r *AddRequest) { r.ProviderID = "mlx_lm_server" }, FieldDraft, "pass --draft <draft>"},
+		{"a draft on a provider that serves one model", func(r *AddRequest) { r.Draft = "org/d" }, FieldDraft, "--draft is for an mlx_lm_server pairing; openrouter serves one model at a time"},
 		{"an id with no slash", func(r *AddRequest) { r.ID = "noslash" }, FieldID, `an id is <provider>/<name> with no spaces, got "noslash"`},
 		{"an id with a space", func(r *AddRequest) { r.ID = "openrouter/has space" }, FieldID, `an id is <provider>/<name> with no spaces, got "openrouter/has space"`},
 		{"an id with nothing after the slash", func(r *AddRequest) { r.ID = "openrouter/" }, FieldID, "an id is <provider>/<name>"},
@@ -529,3 +530,94 @@ tags = []
 		t.Errorf("removing the bad row: %v", err)
 	}
 }
+
+// TestAddAPairing verifies `wt model add mlx_lm_server <target> --draft
+// <draft>` writes the row modelman's form wrote: an id and a model_name that
+// spell the pairing, the target under fetch and the draft under draft — each
+// a repo, or a local_path when it is spelled like a path — and the
+// mlx_lm_server provider row seeded beside it. llmbench reads fetch and draft
+// to start the server; a row without them is a pairing nothing can start.
+func TestAddAPairing(t *testing.T) {
+	path := scratchRegistry(t, "")
+	res, err := Add(AddRequest{
+		ProviderID: "mlx_lm_server", ModelName: "mlx-community/Qwen3.8-27B-4bit", Draft: "~/models/Qwen3.8-4B-4bit/",
+		Fields: Fields{Family: ptr("qwen3.8"), Tags: ptr("code")},
+	}, config.SeedEnv{})
+	if err != nil {
+		t.Fatal(err)
+	}
+	if res.ID != "mlx_lm_server/Qwen3.8-27B-4bit+draft-Qwen3.8-4B-4bit" || strings.Join(res.ProvidersAdded, ",") != "mlx_lm_server" {
+		t.Errorf("result = %+v", res)
+	}
+	want := `[[providers]]
+id = "mlx_lm_server"
+name = "mlx-lm server (target+draft)"
+location = "local"
+
+[providers.auth]
+type = "none"
+base_url = "http://localhost:8001/v1"
+
+[[models]]
+id = "mlx_lm_server/Qwen3.8-27B-4bit+draft-Qwen3.8-4B-4bit"
+family = "qwen3.8"
+provider_id = "mlx_lm_server"
+model_name = "Qwen3.8-27B-4bit+draft-Qwen3.8-4B-4bit"
+tags = [
+    "code",
+]
+
+[models.fetch]
+repo = "mlx-community/Qwen3.8-27B-4bit"
+
+[models.draft]
+local_path = "~/models/Qwen3.8-4B-4bit/"
+`
+	if got := read(t, path); got != want {
+		t.Errorf("registry =\n%s\nwant\n%s", got, want)
+	}
+	// The same pairing again — the same two sides, however the path is
+	// spelled and whatever id is asked for — is refused, not duplicated.
+	_, err = Add(AddRequest{ProviderID: "mlx_lm_server", ModelName: "mlx-community/Qwen3.8-27B-4bit", Draft: "~/models/Qwen3.8-4B-4bit", ID: "mlx_lm_server/again",
+		Fields: Fields{Family: ptr("qwen3.8")}}, config.SeedEnv{})
+	var fe *FieldError
+	if !errors.As(err, &fe) || !strings.Contains(fe.Msg, `model "mlx_lm_server/Qwen3.8-27B-4bit+draft-Qwen3.8-4B-4bit" already registers this pairing (target mlx-community/Qwen3.8-27B-4bit, draft ~/models/Qwen3.8-4B-4bit)`) {
+		t.Errorf("a second add of the pairing: err = %v, want it refused", err)
+	}
+
+	// A different pairing whose two sides end in the same names — here the
+	// target quantized locally instead of the one on Hugging Face — is not
+	// that pairing. The names make the id that is taken, so the add says to
+	// pass --id, and with one it goes in beside the first.
+	local := AddRequest{ProviderID: "mlx_lm_server", ModelName: "/quant/dwq/Qwen3.8-27B-4bit", Draft: "~/models/Qwen3.8-4B-4bit", Fields: Fields{Family: ptr("qwen3.8")}}
+	before := read(t, path)
+	if _, err = Add(local, config.SeedEnv{}); !errors.Is(err, config.ErrModelExists) || !strings.Contains(err.Error(), "pass --id mlx_lm_server/<name>") {
+		t.Errorf("a different pairing with the same names: err = %v, want the taken id and the way round it", err)
+	}
+	if read(t, path) != before {
+		t.Error("a refused add changed the registry")
+	}
+	local.ID = "mlx_lm_server/qwen-dwq"
+	res, err = Add(local, config.SeedEnv{})
+	if err != nil || res.ID != "mlx_lm_server/qwen-dwq" {
+		t.Fatalf("with --id: %+v, %v; want it added", res, err)
+	}
+	if got := read(t, path); !strings.Contains(got, "id = \"mlx_lm_server/qwen-dwq\"\nfamily = \"qwen3.8\"\nprovider_id = \"mlx_lm_server\"\nmodel_name = \"Qwen3.8-27B-4bit+draft-Qwen3.8-4B-4bit\"\ntags = []\n\n[models.fetch]\nlocal_path = \"/quant/dwq/Qwen3.8-27B-4bit\"\n") {
+		t.Errorf("the second pairing's row:\n%s", got)
+	}
+}
+
+// TestPairingName pins how a pairing is named from its two sides: the last
+// segment of each, whether it is a repo id or a path with a trailing slash.
+func TestPairingName(t *testing.T) {
+	cases := []struct{ target, draft, want string }{
+		{"org/T", "org/D", "T+draft-D"},
+		{"/models/target/", "~/d", "target+draft-d"},
+		{"plain", "./rel/draft", "plain+draft-draft"},
+	}
+	for _, c := range cases {
+		if got := PairingName(c.target, c.draft); got != c.want {
+			t.Errorf("PairingName(%q, %q) = %q, want %q", c.target, c.draft, got, c.want)
+		}
+	}
+}
```

```diff
--- a/wt/cmd/wt/model_write_test.go
+++ b/wt/cmd/wt/model_write_test.go
@@ -433,3 +433,37 @@ func TestModelWritesUnderARedirectedRegistry(t *testing.T) {
 		})
 	}
 }
+
+// TestModelAddAPairing verifies `wt model add mlx_lm_server <target> --draft
+// <draft>` registers the pairing and prints the llmbench command that starts
+// it, since wt has no engine for one, and that --draft on any other provider
+// is refused. Without the command the user has a registry row and no way to
+// find out how to run it.
+func TestModelAddAPairing(t *testing.T) {
+	registry := modelHome(t, writeRegistry)
+	stubSeedEnv(t, config.SeedEnv{})
+	stubRouteSync(t, "")
+	out, err := runWT(t, "model", "add", "mlx_lm_server", "mlx-community/Qwen3.8-27B-4bit", "--draft", "mlx-community/Qwen3.8-4B-4bit", "--family", "qwen3.8")
+	want := "added model: mlx_lm_server/Qwen3.8-27B-4bit+draft-Qwen3.8-4B-4bit\n" +
+		"start it with: llmbench provider isolate --solo mlx_lm_server mlx-community/Qwen3.8-27B-4bit --draft mlx-community/Qwen3.8-4B-4bit\n" +
+		"added provider: mlx_lm_server\n"
+	if err != nil || out != want {
+		t.Fatalf("add = %q, %v\nwant %q", out, err, want)
+	}
+	got := mustRead(t, registry)
+	if !strings.Contains(got, "[models.fetch]\nrepo = \"mlx-community/Qwen3.8-27B-4bit\"\n\n[models.draft]\nrepo = \"mlx-community/Qwen3.8-4B-4bit\"\n") {
+		t.Errorf("the pairing's two sides are not in the registry:\n%s", got)
+	}
+	before := got
+	for _, args := range [][]string{
+		{"model", "add", "mlx_lm_server", "org/target", "--family", "f"},
+		{"model", "add", "ollama", "qwen3:8b", "--family", "f", "--draft", "org/d"},
+	} {
+		if _, err := runWT(t, args...); err == nil || !strings.Contains(err.Error(), "--draft") {
+			t.Errorf("wt %s: err = %v, want a refusal about --draft", strings.Join(args, " "), err)
+		}
+	}
+	if mustRead(t, registry) != before {
+		t.Error("a refused add changed the registry")
+	}
+}
```

Create `wt/internal/configeditor/models_pairing_test.go`. This one pins what PRs E and F already do for a pairing row — the spec's "shown as rows; metadata editable in the tab" — so it passes as soon as it is written; it is here because a pairing row can only be written with a fixture until this task.

```go
package configeditor

import (
	"strings"
	"testing"
)

// pairingRegistry is tabRegistry plus an mlx_lm_server provider and one
// target+draft pairing, as `wt model add mlx_lm_server … --draft …` writes it
// (provider rows before model rows, the order wt and modelman write).
var pairingRegistry = strings.Replace(tabRegistry, "[[models]]", `[[providers]]
id = "mlx_lm_server"
name = "mlx-lm server (target+draft)"
location = "local"

[providers.auth]
type = "none"
base_url = "http://localhost:8001/v1"

[[models]]`, 1) + `
[[models]]
id = "mlx_lm_server/Qwen3.8-27B+draft-Qwen3.8-4B"
family = "qwen3.8"
provider_id = "mlx_lm_server"
model_name = "Qwen3.8-27B+draft-Qwen3.8-4B"
tags = []

[models.fetch]
repo = "mlx-community/Qwen3.8-27B"

[models.draft]
local_path = "~/models/Qwen3.8-4B"
`

// TestModelsTabShowsAndEditsAPairing verifies an mlx_lm_server pairing is a
// row of the Models tab (status "-": it cannot be enumerated), that its
// target and draft are on the detail line, that its metadata is edited like
// any model's with fetch and draft left alone, and that the add form does
// not offer mlx_lm_server: a pairing needs two artifacts, which the command
// line takes and the form does not.
func TestModelsTabShowsAndEditsAPairing(t *testing.T) {
	const id = "mlx_lm_server/Qwen3.8-27B+draft-Qwen3.8-4B"
	tm := newTabMachine(t, pairingRegistry)
	m := selectModel(t, modelsEditor(t, tm, 120, 24), id)
	r, _ := m.selectedModel()
	if !r.Pairing() || string(r.Status) != "-" {
		t.Fatalf("the pairing row = %+v, want a pairing with status -", r)
	}
	if view := m.View(); !strings.Contains(view, "target mlx-community/Qwen3.8-27B · draft ~/models/Qwen3.8-4B") {
		t.Errorf("the detail line should name the target and the draft:\n%s", view)
	}

	m = keys(t, m, "enter")
	if f := m.models.form; f == nil || f.mode != modelFormEdit || f.value(mfProvider) != "mlx_lm_server" {
		t.Fatal("enter on a pairing should open the edit form")
	}
	m = keys(t, typeText(t, moveTo(t, m, mfTags), "code"), "ctrl+s")
	want := strings.Replace(pairingRegistry,
		"model_name = \"Qwen3.8-27B+draft-Qwen3.8-4B\"\ntags = []\n",
		"model_name = \"Qwen3.8-27B+draft-Qwen3.8-4B\"\ntags = [\n    \"code\",\n]\n", 1)
	if got := tm.text(t); got != want {
		t.Errorf("registry after editing the pairing's tags =\n%s\nwant only the tags changed", got)
	}

	m = keys(t, m, "n")
	if got := strings.Join(m.models.form.providers, ","); strings.Contains(got, "mlx_lm_server") {
		t.Errorf("the add form offers %s; mlx_lm_server must not be among them", got)
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run, from `wt/`: `go test -count=1 ./internal/modeladmin ./cmd/wt ./internal/configeditor -run 'Pairing|TestAddRefusals'`

Expected: `internal/modeladmin` does not build, `cmd/wt` fails on the unknown flag, and `internal/configeditor` passes (see Step 1).

```text
FAIL	github.com/ohanaverse/local-ai-setup/wt/internal/modeladmin [build failed]
--- FAIL: TestModelAddAPairing (0.00s)
    model_write_test.go:451: add = "Error: unknown flag: --draft\n", unknown flag: --draft
        want "added model: mlx_lm_server/Qwen3.8-27B-4bit+draft-Qwen3.8-4B-4bit\nstart it with: llmbench provider isolate --solo mlx_lm_server mlx-community/Qwen3.8-27B-4bit --draft mlx-community/Qwen3.8-4B-4bit\nadde...
FAIL
FAIL	github.com/ohanaverse/local-ai-setup/wt/cmd/wt	0.156s
ok  	github.com/ohanaverse/local-ai-setup/wt/internal/configeditor	0.137s
FAIL
# github.com/ohanaverse/local-ai-setup/wt/internal/modeladmin [github.com/ohanaverse/local-ai-setup/wt/internal/modeladmin.test]
internal/modeladmin/apply_test.go:252:87: undefined: FieldDraft
internal/modeladmin/apply_test.go:253:75: r.Draft undefined (type *AddRequest has no field or method Draft)
internal/modeladmin/apply_test.go:253:94: undefined: FieldDraft
```

- [ ] **Step 3: Teach `Add` what a pairing is**

A pairing is its two sides. `model_name` is only the last path segment of each, so it cannot say whether two pairings are the same: `samePairing` compares the `fetch` and `draft` tables instead, and `sameArtifact` is no longer asked about an mlx_lm_server row.

```diff
--- a/wt/internal/modeladmin/fields.go
+++ b/wt/internal/modeladmin/fields.go
@@ -24,6 +24,7 @@ const (
 	FieldOutputPrice        = "output-price"
 	FieldSubscriptionPrice  = "subscription-price"
 	FieldSubscriptionPeriod = "subscription-period"
+	FieldDraft              = "draft"
 )
 
 // FieldError is a value the user gave that cannot be used. Field names the
```

```diff
--- a/wt/internal/modeladmin/apply.go
+++ b/wt/internal/modeladmin/apply.go
@@ -21,6 +21,9 @@ type AddRequest struct {
 	ModelName string
 	// ID overrides the derived id (DeriveID) when not empty.
 	ID string
+	// Draft makes the model an mlx_lm_server pairing: ModelName is then the
+	// target and Draft the draft, each a Hugging Face repo or a local path.
+	Draft string
 	Fields
 	// ModelInfo is written as the row's model_info table when not empty: the
 	// capabilities looked up before the write (OllamaCapabilities).
@@ -40,16 +43,20 @@ type AddResult struct {
 
 // addPlan is an add that passed every check that needs no registry: the
 // request with its text trimmed, the id the row gets, and the row's keys.
+// For a pairing, req.ModelName is the pairing's name (PairingName) and
+// target and draft are its two sides as their tables.
 type addPlan struct {
-	req AddRequest
-	id  string
-	set map[string]any
+	req           AddRequest
+	id            string
+	set           map[string]any
+	target, draft map[string]any
 }
 
 // planAdd checks a request by itself — what is required, each field's value,
 // the subscription rule, the shape of a given id — and works out the row.
 func planAdd(req AddRequest) (addPlan, error) {
-	req.ProviderID, req.ModelName, req.ID = strings.TrimSpace(req.ProviderID), strings.TrimSpace(req.ModelName), strings.TrimSpace(req.ID)
+	req.ProviderID, req.ModelName, req.ID, req.Draft = strings.TrimSpace(req.ProviderID), strings.TrimSpace(req.ModelName), strings.TrimSpace(req.ID), strings.TrimSpace(req.Draft)
+	pairing := localmodels.RunningOnly(req.ProviderID)
 	switch {
 	case req.ProviderID == "":
 		return addPlan{}, fieldErr(FieldProvider, "a provider is required")
@@ -57,8 +64,10 @@ func planAdd(req AddRequest) (addPlan, error) {
 		return addPlan{}, fieldErr(FieldName, "a model name is required")
 	case req.Family == nil:
 		return addPlan{}, fieldErr(FieldFamily, "family is required")
-	case localmodels.RunningOnly(req.ProviderID):
-		return addPlan{}, fieldErr(FieldProvider, "an mlx_lm_server model is a target+draft pairing, which this command cannot add yet: add it to %s by hand", config.RegistryPath())
+	case pairing && req.Draft == "":
+		return addPlan{}, fieldErr(FieldDraft, "an mlx_lm_server model is a target+draft pairing: name the target and pass --draft <draft>")
+	case !pairing && req.Draft != "":
+		return addPlan{}, fieldErr(FieldDraft, "--draft is for an mlx_lm_server pairing; %s serves one model at a time", req.ProviderID)
 	}
 	if err := checkID(req.ID); err != nil {
 		return addPlan{}, err
@@ -67,6 +76,14 @@ func planAdd(req AddRequest) (addPlan, error) {
 	if err != nil {
 		return addPlan{}, err
 	}
+	plan := addPlan{}
+	if pairing {
+		// The registry row names the pairing, and records each side where
+		// llmbench reads it to start the server.
+		plan.target, plan.draft = artifactTable(req.ModelName), artifactTable(req.Draft)
+		req.ModelName = PairingName(req.ModelName, req.Draft)
+		set["fetch"], set["draft"] = plan.target, plan.draft
+	}
 	_, hasPrice := set["cost.subscription_price"]
 	period, _ := set["cost.subscription_period"].(string)
 	if err := checkSubscription(hasPrice, period); err != nil {
@@ -82,7 +99,8 @@ func planAdd(req AddRequest) (addPlan, error) {
 	if id == "" {
 		id = DeriveID(req.ProviderID, req.ModelName)
 	}
-	return addPlan{req: req, id: id, set: set}, nil
+	plan.req, plan.id, plan.set = req, id, set
+	return plan, nil
 }
 
 // checkID refuses a given id that is not <provider>/<name>: a part on each
@@ -127,6 +145,9 @@ func Add(req AddRequest, env config.SeedEnv) (AddResult, error) {
 	_, err = config.UpdateRegistry(func(d *config.RegistryDoc) error {
 		// Everything assigned here is assigned afresh: apply may run again.
 		res = AddResult{ID: id}
+		if other := samePairing(d.Models(), plan.target, plan.draft); other != "" {
+			return fieldErr(FieldName, "model %q already registers this pairing (target %s, draft %s); edit that one (wt model edit %s)", other, sideText(plan.target), sideText(plan.draft), other)
+		}
 		if other := sameArtifact(d.Models(), req.ProviderID, req.ModelName); other != "" {
 			return fieldErr(FieldName, "model %q already registers %s on %s; edit that one (wt model edit %s)", other, req.ModelName, req.ProviderID, other)
 		}
@@ -155,19 +176,102 @@ func Add(req AddRequest, env config.SeedEnv) (AddResult, error) {
 		return nil
 	})
 	if err != nil {
+		if plan.target != nil && req.ID == "" && errors.Is(err, config.ErrModelExists) {
+			// Not the same pairing (samePairing would have said so): another
+			// one whose target and draft end in the same two names.
+			return AddResult{}, fmt.Errorf("%w: a different pairing already has the id these names make; pass --id mlx_lm_server/<name> to register this one under an id of its own", err)
+		}
 		return AddResult{}, describe(err, req.ProviderID)
 	}
 	return res, nil
 }
 
+// PairingName is the model_name of an mlx_lm_server pairing, and with the
+// provider in front its id: the last path segment of the target and of the
+// draft, as modelman's form named one (Qwen3.8-27B+draft-Qwen3.8-4B). It
+// says what the pairing is wherever a model id is shown.
+func PairingName(target, draft string) string {
+	return lastSegment(target) + "+draft-" + lastSegment(draft)
+}
+
+func lastSegment(repoOrPath string) string {
+	trimmed := strings.TrimRight(repoOrPath, "/")
+	if i := strings.LastIndex(trimmed, "/"); i >= 0 && trimmed[i+1:] != "" {
+		return trimmed[i+1:]
+	}
+	if trimmed == "" {
+		return repoOrPath
+	}
+	return trimmed
+}
+
+// artifactTable is one side of a pairing as a fetch or draft table: a
+// local_path for something that is spelled like a path (absolute, ~ or
+// dot-relative), a repo for anything else. A Hugging Face repo id is
+// org/name and never starts with one of those.
+func artifactTable(repoOrPath string) map[string]any {
+	for _, prefix := range []string{"/", "~", "./", "../"} {
+		if strings.HasPrefix(repoOrPath, prefix) {
+			return map[string]any{"local_path": repoOrPath}
+		}
+	}
+	return map[string]any{"repo": repoOrPath}
+}
+
+// samePairing returns the id of an mlx_lm_server row whose target and draft
+// are the ones given: the same repo or the same local path on each side. A
+// pairing is what its two sides are, not what they are called — a model
+// quantized locally to /quant/Big-4bit and mlx-community/Big-4bit make the
+// same pairing name with one draft, and are two pairings. "" when target is
+// nil (not a pairing) or no row matches.
+func samePairing(models []*tomlw.Table, target, draft map[string]any) string {
+	if target == nil {
+		return ""
+	}
+	side := func(row *tomlw.Table, key string) string {
+		t, _ := mustGet(row, key).(*tomlw.Table)
+		if t == nil {
+			return ""
+		}
+		return sideKey(map[string]any{"repo": mustGet(t, "repo"), "local_path": mustGet(t, "local_path")})
+	}
+	for _, m := range models {
+		if localmodels.RunningOnly(tableString(m, "provider_id")) && side(m, "fetch") == sideKey(target) && side(m, "draft") == sideKey(draft) {
+			return tableString(m, "id")
+		}
+	}
+	return ""
+}
+
+// sideKey is one side of a pairing in a form two spellings of it share: what
+// kind it is, and its text without a trailing slash.
+func sideKey(side map[string]any) string {
+	if p, _ := side["local_path"].(string); p != "" {
+		return "path:" + strings.TrimRight(p, "/")
+	}
+	repo, _ := side["repo"].(string)
+	return "repo:" + strings.TrimRight(repo, "/")
+}
+
+// sideText is one side of a pairing as the user gave it.
+func sideText(side map[string]any) string {
+	if p, _ := side["local_path"].(string); p != "" {
+		return p
+	}
+	repo, _ := side["repo"].(string)
+	return repo
+}
+
 // sameArtifact returns the id of a model row that already stands for the
 // same local artifact: the same provider family and model_name. The
 // inventory gives an artifact to the first row that matches it, so a second
 // row would read as "missing" for ever. "" for a provider with no probe (a
-// cloud provider may list one model under two ids, at two prices).
+// cloud provider may list one model under two ids, at two prices) and for an
+// mlx_lm_server pairing, which no inventory lists and whose model_name is
+// only the last segment of each side (samePairing judges those).
 func sameArtifact(models []*tomlw.Table, providerID, modelName string) string {
 	family := localmodels.Family(providerID)
-	if family == "" {
+	if family == "" || localmodels.RunningOnly(providerID) {
 		return ""
 	}
 	for _, m := range models {
```

- [ ] **Step 4: Add `--draft`, and print how to start what was added**

```diff
--- a/wt/cmd/wt/model_write.go
+++ b/wt/cmd/wt/model_write.go
@@ -11,6 +11,7 @@ import (
 	"slices"
 	"strings"
 
+	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
 	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
 	"github.com/ohanaverse/local-ai-setup/wt/internal/modeladmin"
 	"github.com/spf13/cobra"
@@ -60,7 +61,7 @@ func modelFieldFlags(c *cobra.Command) func() modeladmin.Fields {
 }
 
 func modelAddCmd(a *app) *cobra.Command {
-	var id string
+	var id, draft string
 	c := &cobra.Command{
 		Use:   "add <provider> <name> --family <family>",
 		Short: "Add a model to the registry",
@@ -81,18 +82,25 @@ func modelAddCmd(a *app) *cobra.Command {
 			"`ollama show` once and records whether the model supports tools and\n" +
 			"vision; if that fails the model is added without them and a warning says\n" +
 			"so.\n\n" +
+			"An mlx_lm_server model is a target+draft pairing: <name> is the target and\n" +
+			"--draft the draft, each a Hugging Face repo or a local path. wt registers a\n" +
+			"pairing and cannot start one; it prints the llmbench command that does. Two\n" +
+			"pairings whose target and draft end in the same names need --id for the\n" +
+			"second.\n\n" +
 			"The LiteLLM routes are synced once afterwards; a sync that cannot run is a\n" +
 			"warning, and the exit status is still 0.",
 		Example: "  wt model add ollama qwen3:8b --family qwen3 --tags code\n" +
-			"  wt model add openrouter qwen/qwen3.8-27b --family qwen3.8 --input-price 0.5 --output-price 2",
+			"  wt model add openrouter qwen/qwen3.8-27b --family qwen3.8 --input-price 0.5 --output-price 2\n" +
+			"  wt model add mlx_lm_server mlx-community/Qwen3.8-27B-4bit --draft mlx-community/Qwen3.8-4B-4bit --family qwen3.8",
 		Args:         cobra.ExactArgs(2),
 		SilenceUsage: true,
 	}
 	fields := modelFieldFlags(c)
 	c.Flags().StringVar(&id, "id", "", "the model's id, instead of the derived one")
+	c.Flags().StringVar(&draft, modeladmin.FieldDraft, "", "mlx_lm_server only: the pairing's draft model (a repo or a local path)")
 	_ = c.MarkFlagRequired(modeladmin.FieldFamily)
 	c.RunE = func(cmd *cobra.Command, args []string) error {
-		req := modeladmin.AddRequest{ProviderID: args[0], ModelName: args[1], ID: id, Fields: fields()}
+		req := modeladmin.AddRequest{ProviderID: args[0], ModelName: args[1], ID: id, Draft: draft, Fields: fields()}
 		return runModelAdd(cmd.OutOrStdout(), cmd.ErrOrStderr(), a.cfg, req)
 	}
 	return c
@@ -122,6 +130,10 @@ func runModelAdd(out, errOut io.Writer, cfg *config.Config, req modeladmin.AddRe
 		return err
 	}
 	fmt.Fprintf(out, "added model: %s\n", res.ID)
+	if req.Draft != "" {
+		// wt registers a pairing and has no engine to start one.
+		fmt.Fprintf(out, "start it with: %s\n", catalog.PairingStartCommand(req.ModelName, req.Draft))
+	}
 	for _, p := range res.ProvidersAdded {
 		fmt.Fprintf(out, "added provider: %s\n", p)
 	}
```

- [ ] **Step 5: Run the tests**

Run, from `wt/`: `go test -count=1 ./internal/modeladmin ./cmd/wt ./internal/configeditor`

Expected: all three `ok`.

- [ ] **Step 6: Document it**

In `wt/docs/wt-model.md`, find:

````text
wt model rm <id>... [--yes]
```
````

and replace it with:

````text
wt model rm <id>... [--yes]
wt model add mlx_lm_server <target> --draft <draft> --family F   # a target+draft pairing
```
````

In `wt/docs/wt-model.md`, find:

```text
## `wt model edit <id> [flags]`
```

and replace it with:

````text
### An mlx_lm_server pairing

An mlx_lm_server model is a target and a draft served together by
`mlx_lm.server --draft-model`. `wt model add mlx_lm_server <target> --draft
<draft> --family <family>` registers one: each side is a Hugging Face repo
(`org/name`) or a local path (starting `/`, `~`, `./` or `../`), written to the
row's `[models.fetch]` and `[models.draft]`. The id is
`mlx_lm_server/<target>+draft-<draft>`, from the last segment of each.

A pairing is its two sides, not their names. The same target and draft
again are refused, whatever `--id` says. A different pairing whose sides end
in the same two names — a target you quantized into `/quant/Big-4bit` beside
`mlx-community/Big-4bit`, with one draft — would get the id that is taken:
the add says so, and `--id mlx_lm_server/<name>` registers it.

wt registers a pairing and cannot start one. `add` prints the command that
does, and so does a launch or `wt start` of a pairing that is not running:

```bash
llmbench provider isolate --solo mlx_lm_server <target> --draft <draft>
```

(from the repository: `uv run --directory llmbench llmbench provider isolate
--solo …`; `--solo` leaves the other local providers running). A pairing is a
row of `wt model list` and of the Models tab with `STATUS -`, since no probe
can enumerate it; its family, tags and prices are edited like any model's,
and the tab's form does not create one.

## `wt model edit <id> [flags]`
````

In `wt/docs/internals/local-models.md`, find:

```text
(`modelman start <id>` hint; a stopped `mlx_lm_server` pairing has no row at all, so its pin gets the same hint from `MissingReason`)
```

and replace it with:

```text
(the reason names how to start it: for an `mlx_lm_server` pairing, `llmbench provider isolate --solo mlx_lm_server <target> --draft <draft>` with the row's own `fetch` and `draft` — `catalog.PairingStartCommand` — and for any other provider wt has no engine for, that provider's own tool; a stopped pairing has no row at all, so its pin gets the same reason from `MissingReason`, which reads the target and the draft from the config it is handed)
```

In `wt/docs/internals/local-models.md`, find:

```text
or the `modelman start` hint for a running-only family
```

and replace it with:

```text
or the llmbench command that starts it for a running-only family
```

In `docs/guides/10-mlx-lm-quantization.md`, find:

```text
Add the pairing to `registry.toml` by hand ([02-providers-and-models](02-providers-and-models.md) Step 1) — one `[[models]]` block whose `[models.fetch]` names the target and whose `[models.draft]` names the draft, each as a `repo` (HF repo id) or a `local_path` (absolute directory, e.g. Step 1's output):
```

and replace it with:

```text
Add the pairing with `wt model add mlx_lm_server <target> --draft <draft> --family <family>`, where each of `<target>` and `<draft>` is an HF repo id or a local path (e.g. Step 1's output). It writes one `[[models]]` block whose `[models.fetch]` names the target and whose `[models.draft]` names the draft, each as a `repo` or a `local_path`, and prints the command that starts the pairing. The block it writes:
```

In `.claude/skills/mlx-lm-quantization/SKILL.md`, find:

```text
register both sides by hand in one `[[models]]` block under provider `mlx_lm_server` (`[models.fetch]` = target, `[models.draft]` = draft; snippet in the guide's Step 3) and isolate with `uv run --directory llmbench llmbench provider isolate mlx_lm_server <target> --draft <draft>`
```

and replace it with:

```text
register it with `wt model add mlx_lm_server <target> --draft <draft> --family <family>` (it writes one `[[models]]` block: `[models.fetch]` = target, `[models.draft]` = draft; the guide's Step 3 shows it) and start it with `uv run --directory llmbench llmbench provider isolate --solo mlx_lm_server <target> --draft <draft>` (without `--solo` the other local providers are stopped first, which is what a benchmark wants)
```

In `wt/CHANGELOG.md`, find:

```text
## Unreleased

### Added
```

and replace it with:

```text
## Unreleased

### Added

- `wt model add mlx_lm_server <target> --draft <draft> --family F` registers a
  target+draft pairing, each side a Hugging Face repo or a local path, and
  prints the command that starts it. Two pairings are the same when their
  sides are, not their names. wt cannot start a pairing: where it
  used to say `modelman start <id>`, it now names `llmbench provider isolate
  --solo mlx_lm_server <target> --draft <draft>` with the pairing's own
  target and draft.
```

- [ ] **Step 7: Look at the real command**

In a throwaway home whose mlx_lm_server row points at a port nothing listens on, so that no real server is probed. wt is run only through `run.sh`, which empties the environment and sets the throwaway home, a scratch LiteLLM config and a restart command that does nothing. Each block below is one command and stands on its own.

Create the scratch directory, build into it, and write the home. From the monorepo root:

```bash
D=/tmp/wt-step3-pairing
rm -rf "$D" && mkdir -p "$D/home/.config/local-ai"
(cd wt && go build -o "$D/wt" ./cmd/wt)
printf 'model_list: []\n' > "$D/home/litellm.yaml"
cat > "$D/home/.config/local-ai/registry.toml" <<'TOML'
[[providers]]
id = "mlx_lm_server"
name = "mlx-lm server (target+draft)"
location = "local"

[providers.auth]
type = "none"
base_url = "http://127.0.0.1:9/v1"
TOML
```

Create `/tmp/wt-step3-pairing/run.sh`:

```bash
#!/bin/bash
# Run a command in the throwaway home that is beside this script, and in no
# other: the environment is emptied first, so nothing of the caller's — HOME,
# XDG_CONFIG_HOME, a registry or LiteLLM override, an API key — reaches wt,
# and wt reaches nothing of the caller's. PATH is the system's alone, so no
# installed provider (ollama, omlx, mtplx) is found. Usage: run.sh <command>
# [args...]
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd -P)"
cd "$here/home"
exec env -i HOME="$here/home" XDG_CONFIG_HOME="$here/home/.config" \
  WT_LITELLM_CONFIG="$here/home/litellm.yaml" WT_LITELLM_RESTART_CMD=true \
  OPENROUTER_API_KEY=sk-not-a-key PATH=/usr/bin:/bin TERM=xterm-256color "$@"
```

Then:

```bash
D=/tmp/wt-step3-pairing
chmod +x "$D/run.sh"
wt() { "$D/run.sh" "$D/wt" "$@"; }
wt model add mlx_lm_server mlx-community/Qwen3.8-27B-4bit --draft mlx-community/Qwen3.8-4B-4bit --family qwen3.8; echo "exit $?"
wt model add mlx_lm_server /quant/dwq/Qwen3.8-27B-4bit --draft mlx-community/Qwen3.8-4B-4bit --family qwen3.8; echo "exit $?"
wt model add mlx_lm_server /quant/dwq/Qwen3.8-27B-4bit --draft mlx-community/Qwen3.8-4B-4bit --family qwen3.8 --id mlx_lm_server/qwen-dwq; echo "exit $?"
wt model list
wt start mlx_lm_server/Qwen3.8-27B-4bit+draft-Qwen3.8-4B-4bit; echo "exit $?"
tail -12 "$D/home/.config/local-ai/registry.toml"
```

Expected, as observed (a refusal is printed twice, once by cobra and once by wt):

```text
added model: mlx_lm_server/Qwen3.8-27B-4bit+draft-Qwen3.8-4B-4bit
start it with: llmbench provider isolate --solo mlx_lm_server mlx-community/Qwen3.8-27B-4bit --draft mlx-community/Qwen3.8-4B-4bit
exit 0
Error: model already exists: "mlx_lm_server/Qwen3.8-27B-4bit+draft-Qwen3.8-4B-4bit": a different pairing already has the id these names make; pass --id mlx_lm_server/<name> to register this one under an id of its own
wt: model already exists: "mlx_lm_server/Qwen3.8-27B-4bit+draft-Qwen3.8-4B-4bit": a different pairing already has the id these names make; pass --id mlx_lm_server/<name> to register this one under an id of its own
exit 1
added model: mlx_lm_server/qwen-dwq
start it with: llmbench provider isolate --solo mlx_lm_server /quant/dwq/Qwen3.8-27B-4bit --draft mlx-community/Qwen3.8-4B-4bit
exit 0
MODEL                                                 FAMILY   LOC    STATUS  RUNNING  SIZE  PATH
mlx_lm_server/Qwen3.8-27B-4bit+draft-Qwen3.8-4B-4bit  qwen3.8  local  -                   -  -
mlx_lm_server/qwen-dwq                                qwen3.8  local  -                   -  -
Error: local model "mlx_lm_server/Qwen3.8-27B-4bit+draft-Qwen3.8-4B-4bit" is not running, and wt cannot start an mlx_lm_server pairing — start it with `llmbench provider isolate --solo mlx_lm_server mlx-community/Qwen3.8-27B-4bit --draft mlx-community/Qwen3.8-4B-4bit`
wt: local model "mlx_lm_server/Qwen3.8-27B-4bit+draft-Qwen3.8-4B-4bit" is not running, and wt cannot start an mlx_lm_server pairing — start it with `llmbench provider isolate --solo mlx_lm_server mlx-community/Qwen3.8-27B-4bit --draft mlx-community/Qwen3.8-4B-4bit`
exit 1
[[models]]
id = "mlx_lm_server/qwen-dwq"
family = "qwen3.8"
provider_id = "mlx_lm_server"
model_name = "Qwen3.8-27B-4bit+draft-Qwen3.8-4B-4bit"
tags = []

[models.fetch]
local_path = "/quant/dwq/Qwen3.8-27B-4bit"

[models.draft]
repo = "mlx-community/Qwen3.8-4B-4bit"
```

What to check: the second add names a different pairing — the target is a local directory — whose names make the id the first one has, and is refused with the way round it; with `--id` it is added, and the two rows share a `model_name`. `wt start` of a pairing names the command that starts it, with that pairing's own target and draft.

Then remove the scratch directory: `rm -rf /tmp/wt-step3-pairing`

- [ ] **Step 8: Verify the PR and commit**

From `wt/`:

```bash
test -z "$(gofmt -l .)" && go vet ./... && go test -count=1 ./... && make check
```

Expected: every package `ok`; `make check` ends without an error. From the monorepo root: `make test-all && make check-links`. Expected: exit 0.

```bash
git add wt/internal/modeladmin wt/internal/configeditor wt/cmd/wt wt/docs wt/CHANGELOG.md docs/guides/10-mlx-lm-quantization.md
git add -f .claude/skills/mlx-lm-quantization/SKILL.md
git commit -m "feat(wt): wt model add registers an mlx_lm_server pairing"
```

- [ ] **Step 9: Hand off**

Stop here. Tell the owner the branch is ready, what `make test-all` printed, and what Step 7 printed. Say what was not done: the printed llmbench command was not run (it loads two models), so that it starts the pairing rests on llmbench's own tests and on reading its `provider isolate` command (`llmbench/src/llmbench/providers/lifecycle/cli.py`: `provider_id`, a positional model, `--draft`, `--solo`). Push and open the PR only after the owner's OK. Suggested title: `feat(wt): register mlx_lm_server pairings, and say how to start one`.

---

## What Step 3 leaves for later steps

- **Start and stop from the tab.** The owner decided against a start/stop key on the Models tab in this step. `wt start`, `wt stop` and `wt stop --all` cover it. Adding one later means extracting the launcher's start flow (`internal/tui/start_flow.go`) into something `configeditor` can run.
- **Step 4 (`wt cloud-sync`)** builds on the same writer. Two things here are its to keep: `wt model edit` does not stamp `pricing_updated_at`, and `modeladmin.Edit` leaves `cost.time_prices` alone (pinned by `TestEditPatchesOnlyWhatWasGiven`). If cloud-sync needs to add models it should go through `modeladmin.Add` or `RegistryDoc.CloneModel`, not write rows of its own shape.
- **Step 6's guide 08 mapping table** can now be written as the spec has it: TUI keys `a`, `e`, `d` are the Models tab or `wt model add|edit|rm`; `start` with no argument is `wt model list`; `stop --all` is `wt stop --all`; `start <mlx_lm_server pairing>` is `llmbench provider isolate --solo mlx_lm_server <target> --draft <draft>`.
- **Step 6's strings sweep** still has these, which this step did not own: `wt warm`'s help text names `modelman start` (`cmd/wt/model_cmds.go`); `wt litellm sync` and a few other commands wrap a load error as ``(run `wt config` to repair)`` without going through `configError` (Step 2's plan recorded it); `modelman`'s notice still says an mlx_lm_server pairing is started with `modelman start`, which is true until `modelman/` is deleted; guides other than the ones Task 16 touched still describe `modelman start` and `modelman stop` for local models.
- **No command writes a provider row.** A provider wt has no default for, and any change to a provider's address or `secret_ref`, is a hand edit of `registry.toml`. The spec does not ask for one. `wt model add` under such a provider is refused, with the file to add the row to; that includes `wt model add omlx-6bit …` on a registry that has only an `omlx` row.
- **`[[families]]`** is not read or written by wt. `modelman delete-family` goes with modelman (the spec lists it under "Not ported").
- **A pairing's running state** is still a guess with two or more registered pairings (`Snapshot.Ambiguous`, shown as `?`). The spec's Step 6 refiles that from #194 as its own issue. Two pairings registered under one `model_name` (the second with `--id`, Task 18) are in the same position and add nothing to it: wt's name match is against what the server lists, which is neither pairing's name.
- **A side of a pairing spelled two ways** (`~/models/x` and `/Users/me/models/x`) is two sides to `samePairing`: it compares the text, a trailing `/` aside, and expands nothing.
- **The Agents tab** is not moved onto `tuilayout`. It keeps its own `fitList` arithmetic, which the tab bar did not change (one line replaced one line). Its agent form and its delete prompt still clip and scroll as they did; only the list's filter, its `esc` and its help line changed (Task 11).
- **`promptReplace`** (`cmd/wt/start.go`), the third copy of the y/N prompt, still opens `/dev/tty` itself. `promptStop` now goes through `openTTY`; folding the two into one function is a refactor of `wt start` this step has no reason to make.
- **The text listing below 80 columns.** `wt model list` is fitted at 80 columns and up, as the spec asks. Narrower than that, a family name longer than eight characters carries a line past the edge, because FAMILY is never dropped. If that matters, FAMILY joins `fitModelList`'s drop order after SIZE.

## What was not verified

- **`ollama show`'s real output.** The parser follows modelman's `parse_ollama_show` and its tests' sample (a line `Capabilities`, then one capability per indented line, ended by a blank line). It was not run against a live ollama daemon while this plan was written: the instructions for this work rule out touching a live provider beyond its own read-only probes. The first real `wt model add ollama …` will show whether `model_info` is recorded; if the daemon's format has moved, only `parseOllamaShow` and its fixture change.
- **A live provider of any kind.** The pty captures and the round trips ran against fake servers on scratch ports or against a port nothing listens on. `Entry.Path` for a real omlx directory, and `Entry.Size` from a real `/api/tags`, are covered by tests with real directories and a canned response.
- **The llmbench command the hints print.** Its shape was read off `llmbench/src/llmbench/providers/lifecycle/cli.py` (`isolate_cmd`: `provider_id`, a positional `model`, `--draft`, `--solo`); it was not run.
- **A terminal with colour.** The captures are text. The current tab and the focused field are also marked without colour (`[Models]`, `>`), which is what the captures show; the selected table row is marked by colour alone, as in the launcher's picker.
- **`make test-all` and `make check-links` at the end of each PR.** They were run once, on a copy of `main` at `d4a9ccf` with every code change and every documentation edit of this plan applied (exit 0; `ALL LINKS OK`), not on each PR's tree by itself: there only `gofmt`, `go vet` and `go test ./...` in `wt/` were run. Each PR's last task runs the real ones.
- **The documentation edits' prose in place.** Every "find" text was matched against `main` at `d4a9ccf` and the edits replayed in order, and `bin/check-links` passed on the result; the pages were not rendered or proofread whole.
- **The move out of modelman's old cost layout, through a whole registry.** `legacyCost` follows modelman's loader (`registry.py`, `_parse_cost`), and the tables it writes for a per-token and a subscription row were handed to that function directly: it reads them as the prices wt wrote, where the old-layout table with a new key beside it read as the old price. A registry holding such a row was not loaded through modelman end to end.
- **The capture directory's name.** The screens in Tasks 13 and 16 were captured with the four scripts as they are printed there, in a scratch directory other than `/tmp/wt-step3-screens`. `run.sh` and `make-home.sh` take every path from where they are, and the tab writes paths from `~`, so no screen shows the directory; the three outputs that do print it (`run.sh env`, and the paths in the two round trips) are given with a placeholder.
- **The owner's two calls.** Whether an add seeds openrouter's row ("Before PR C"), and the live `wt model add`/`rm` round trip the spec asks for before merge (Task 9, Step 8). Neither can be settled by whoever executes this plan.

## Spec coverage

| Spec, Step 3 | Task |
|---|---|
| Core: `internal/modeladmin`, no UI imports | 4, 7 |
| Rows from one live probe; `Entry.Path`/`Size`; `fetch.local_path` | 2, 3, 4 |
| Status and running values | 4 |
| Validation and ids | 6, 7 |
| Immutable and editable fields | 7 (`Fields` has no id, provider or name), 14 |
| Ollama capabilities | 7, 8, 14 |
| `wt model` opens `wt config` on the Models tab; needs a TTY | 12 |
| `wt model list [--json]`, borderless, width-aware, tested at 80 columns | 5 |
| `wt model add` with seeding in the same write | 6 (openrouter's row for a model), 7, 8 |
| `wt model edit`; no flags is a usage error | 7, 8 |
| `wt model rm <id>… [--yes]`, registry only, prints the weights' place | 4 (`WeightsNote`), 7, 8 |
| `wt stop --all [--yes]` | 1 |
| One `UpdateRegistry` then one route sync; exit codes | 7, 8 |
| `local_path` stays a hand edit; wt preserves the keys and shows the path | 4, 7 (`TestEditPatchesOnlyWhatWasGiven`), 16 (docs) |
| Tab bar: Agents, Models | 11 |
| Keys `enter`, `n`, `d`, `r`, `/`, `tab` | 11 (`d`, `r`, `/` — with the fix that makes it filter — and `tab`), 14 (`enter`, `n`) |
| The form, its fields, the accept rule, `ctrl+s`, `esc` | 14 |
| Remove: y/N with the weights path, repeated on the status line | 11 |
| Saving at once; the Agents tab keeps its buffer; its quit prompt | 11, 14 |
| Routes pending; one sync at exit, also for a quit typed while a write is in flight; unchanged `config.yaml` does not restart | 11, 12, 14 |
| Probing in a `tea.Cmd` | 11 |
| modelman's notice | 15 |
| Layout extraction; `tableColumns` to slices with a per-table drop order | 10 |
| Fit tests at 40/80/120 by 12/24/50 with the header row and the focused field | 11, 14 |
| Pty captures at 80x24 before the form PR merges | 13, 16 (and at 40x12) |
| Pairings: created by `wt model add … --draft`, shown as rows, metadata editable | 18 (and 4, 11, 14) |
| `MissingReason` and `BlockReason` name the llmbench command | 17 |
| Step 2's leftovers: the two cross-row validation rules; the silent sync; seeding in `add`; the keyless provider | 6, 9, 7, 7 |
| Testing: scratch-registry safety for every writing command | 8 (`add`, `edit`, `rm`), 12 (`wt model` and `wt config`, whose exit sync follows the tab's writes) |
| Testing: live check, one `wt model add`/`rm` round trip | Not done by this plan's executor, who is barred from live providers: it is the owner's, before PR C merges, with the commands in Task 9, Step 8. Task 9, Step 6 is a scratch round trip against no provider |
