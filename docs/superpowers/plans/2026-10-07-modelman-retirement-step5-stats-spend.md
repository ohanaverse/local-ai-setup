# Modelman Retirement, Step 5 (Spend in `wt stats`) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `wt stats` keeps its survey table and prints a second, per-model table for the same `--window` — MODEL, LAUNCHES, REQUESTS, PROMPT, COMPLETION, SPEND — so `modelman usage report` has a replacement in wt, with `--family` and `--json` added.

**Architecture:** Three small pieces under `wt/internal`, then the command. `usage.StoreImpl.AllCounts` enumerates every launched model in `usage.jsonl`; `litellm.DatabaseURL` resolves the proxy's Postgres connection string; a new `internal/spend` package runs one aggregated query through `psql` and parses the one JSON document it prints. `cmd/wt` joins launches and spend on the model id, renders a borderless width-aware table, and treats every way of having no spend data as a status with a one-line note, never as an error.

**Tech Stack:** Go 1.26.7 (module root `wt/`), cobra, lipgloss (width measurement only), `charmbracelet/x/term`, `gopkg.in/yaml.v3`, `os/exec` to `psql`. No new dependencies, no Postgres driver. No Python changes.

**Spec:** [docs/superpowers/specs/2026-10-06-modelman-retirement-design.md](../specs/2026-10-06-modelman-retirement-design.md), section "Step 5 — spend in wt stats", with its Decisions, End state and Testing sections and Step 6's "What stays" (the permanent `MODELMAN_LITELLM_DATABASE_URL` alias). This plan covers Step 5 only. Step 5 needs no other step and no other step needs it; Step 6 deletes the Python report this replaces.

## Global Constraints

- wt Go and docs only. **No file under `modelman/` changes**: `modelman usage report`, its code (`modelman/src/modelman/usage/`), its tests and the `psycopg2-binary` dependency all stay until Step 6.
- The usage table's columns are exactly `MODEL`, `LAUNCHES`, `REQUESTS`, `PROMPT`, `COMPLETION`, `SPEND`, for the same `--window` as the survey table.
- Launches come from `usage.StoreImpl.AllCounts(agent)`.
- Connection string order, exact: `WT_LITELLM_DATABASE_URL`, then `MODELMAN_LITELLM_DATABASE_URL`, then `general_settings.database_url` in LiteLLM's `config.yaml` (the path `litellm.DefaultPath()` resolves, so `WT_LITELLM_CONFIG` is honoured). An `os.environ/NAME` value is looked up in wt's own environment and then in the LiteLLM LaunchAgent plist's `EnvironmentVariables`, through the existing `litellm.LoadProxyEnv`; when `config.yaml` names no `database_url`, `DATABASE_URL` is looked up the same two ways (LiteLLM's own fallback). No second plist parser is written.
- A connection string that is empty or only whitespace counts as unset, whichever source it came from.
- One aggregated query over `"LiteLLM_SpendLogs"`, through `psql -X -w -q -At -v ON_ERROR_STOP=1`, returning one JSON document, with `PGCONNECT_TIMEOUT=3` and a 10-second deadline.
- Degradation, exact: with no `psql`, no reachable database or no configured URL, the launch counts print, spend cells show `-`, **one** note goes to stderr and the exit code is 0. Requests with no model are counted in a stderr note.
- Flags: `--family` (new; usage table only), `--json` (new; one document with `window`, `as_of`, `survey`, `usage`). `--agent` filters launches and skips spend with a note.
- **Dropped, and must not appear anywhere in code, flags, output or docs as a wt feature:** arbitrary `--days N`, the Markdown output, the Reconciliation sections, the "Last wt launch" line, and the reverse-index fallback for rows with an empty model group.
- Tests stub the query through a package-level seam and **never reach a database**. No test runs the real `psql`.
- The usage table is measured at 80 columns.
- The note for an unreachable database names the host and port `psql` tried (`cannot reach the LiteLLM database at 127.0.0.1:5432: Connection refused`), or the socket path. No note, error or `--json` field carries any other part of a connection string: not the user, the password or the database name. `psql`'s own text is never passed through where it can quote one (see "How much of `psql`'s own message goes in the note?" below).
- **Never, in any step of this plan:** connect to the owner's real LiteLLM database, run `psql` against a real connection string, or print a connection string, an API key, the contents of `~/.config/litellm/config.yaml` or of a LaunchAgent plist. The one task that reads the real database (Task 8, Steps 2 and 3) is run by the controller, under the owner's OK of 2026-10-07 ("you may run against the real database"), and prints only the report; no implementer subagent runs it. Every other command in this plan that runs the built binary does so in a throwaway home with a connection string that points at `127.0.0.1:1`. Never hand `psql` an empty connection string, or one without a host: libpq then tries the local default socket, where a real server may be listening.
- Run every Go command from `wt/`, never the monorepo root. A commit step says which directory its `git add` paths are relative to.
- Every `Test*` function has a top-level `//` comment saying what it tests and why a regression matters to a user.
- Tests never touch the developer's machine: nothing under `~/.config`, no real `config.yaml`, no real LaunchAgent plist, no real database. A test that resolves a connection string replaces `loadProxyEnv` (the `litellm` package's existing seam) or names a temp plist through `WT_LITELLM_PLIST`. `cmd/wt` tests rely on `TestMain`; the new `internal/spend` package gets its own.
- Guides never embed live model state: every example table in a doc is labelled illustrative.
- Per slice, from `wt/`: `test -z "$(gofmt -l .)" && go vet ./... && go test -count=1 ./... && make check`. `gofmt -l` exits 0 even when it lists files, which is why it is wrapped in `test -z`. At the end, `make test-all` from the monorepo root.
- Commit messages follow the repo's conventional style (`feat(wt): …`, `test(wt): …`, `docs(wt): …`), with the attribution trailer your session is told to add, if any.
- Work happens on a feature branch off `main`, one branch per PR slice. **Pushing a branch and opening a PR need the owner's OK.** Do not push, and do not run `gh pr create`, until the owner says so.
- Read `wt/CLAUDE.md`, `wt/docs/wt-stats.md` and `wt/docs/internals/testing.md` before starting.

## Review Focus

1. **`config.yaml` says `database_url: os.environ/DATABASE_URL` — or names no `database_url` at all — and the variable is set only for the proxy (the LaunchAgent plist), not in the shell `wt stats` runs in.** Expected: spend prints with nothing exported, because wt reads the value from the plist; a value in wt's own shell wins over the plist's. When neither has it, launch counts print and the one note names the variable and both places wt looked, never a value — not a `psql` error about a database called `os.environ/DATABASE_URL`. A blank value anywhere is unset. Pinned in Task 2 (`TestDatabaseURLResolvesAnEnvironReference`, `TestDatabaseURLFallsBackToDatabaseURL`, `TestDatabaseURLWhenThePlistCannotBeRead`, `TestDatabaseURLBlankCountsAsUnset`).
2. **Real model ids on an 80-column terminal, and the same report piped to `grep`.** Expected: no line of the usage table wider than 80 on the terminal, no id shortened; one line per model when stdout is not a terminal. (The survey table above it keeps its bordered renderer and its width; see "The 80-Column Measurement".) Pinned in Task 5 (`TestRenderUsageTableFitsEightyColumns`, `TestRenderUsageTableWithoutAWidthLimit`, `TestRenderUsageTableOnANarrowTerminal`) and Task 6 (`TestStatsCmdUsesTheTerminalWidth`).
3. **`wt stats` run under a scratch `XDG_CONFIG_HOME` or `MODELMAN_REGISTRY`, or from a test.** Expected: it never queries the real proxy's database, and never reads the real LaunchAgent plist, unless the database or `config.yaml` is named. Pinned in Task 2 (`TestDatabaseURLSkipsTheDefaultConfigUnderARedirectedRegistry`), Task 3 (`TestSeamsFailClosedByDefault`) and Task 6 (`TestStatsNeverReachesADatabaseByDefault`).
4. **A database end that misbehaves: a connection string `psql` cannot use and quotes back (a password with an unencoded `/`, a bad percent escape), or a host that accepts the connection and never answers.** Expected: the note and `--json` show the host and port for a well-formed string and nothing else from it; for a string libpq mis-splits they show no address at all, because what libpq then calls the host and port are the user name and a piece of the password. `wt stats` returns within about 10 seconds with launches, `-` cells and one note. Pinned in Task 3 (`TestQueryNeverLeaksTheConnectionString`, which uses the messages libpq really prints, `TestQueryErrorMapping` and `TestQueryGivesUpAtTheDeadline`).
5. **Model ids wt does not control: one the registry no longer has, one with odd or control characters, an empty one, and a registry that cannot be read at all.** Expected: the first two are rows (family from the id's prefix; control characters shown escaped in the table), the empty one is counted in a note, and with no readable registry the report still prints and `--family` says it is matching prefixes. Pinned in Task 3 (`TestQueryKeepsOddModelIDsWhole`, `TestQueryParsesTheAggregate`), Task 4 (`TestBuildUsageRowsFamilyAndFilters`), Task 5 (`TestRenderUsageTableEscapesControlCharacters`) and Task 6 (`TestStatsCmdNotesRequestsWithNoModel`, `TestStatsCmdWorksWithoutARegistry`).

## Decisions This Plan Makes

The spec states the behaviour; these are the choices it leaves open. Each is pinned by a test named below, so a reviewer who disagrees changes one place.

| Question | Decision | Why | Pinned by |
|---|---|---|---|
| Which launch count does `LAUNCHES` show? | The one bucket matching `--window` (`OneDay`, `SevenDay` or `ThirtyDay`). A model with no launch in the window and no spend row has no row. | "For the same `--window`". modelman showed three fixed buckets beside a spend window of another length, which is what made its WT-only list hard to read. | Task 4, `TestBuildUsageRowsUsesTheWindowsLaunchBucket` |
| Does a launched model that has left the registry get a row? | Yes; its family is the id's provider prefix, else `unknown`. | `wt stats` already promises to work for a removed model (`stats.go:1-4`). modelman dropped such a row unless it also had spend (`reconcile.py:119-123`), which hides launches exactly when spend is unavailable. This is the one deliberate difference from the Python join. | Task 4, `TestBuildUsageRowsFamilyAndFilters` |
| What if `registry.toml` is missing or cannot be read? | The report prints as usual and every family is the id's provider prefix. With `--family`, one more stderr note says so. Exit 0. | `newApp` never fails on a bad registry: it leaves an empty model list and sets `a.loadErr` (`app.go:83-87`), and the `cfgErr` gate is on the launch path only. modelman exited 1 here (`cli.py:52-59`); that is deliberately not ported, because a report about the past must not depend on today's catalog. The note is there because `--family gemma4` would otherwise print `no usage data` with no hint why. | Task 6, `TestStatsCmdWorksWithoutARegistry` |
| How do `--family` and `--model` match? | Exact, one value each, applied to every observed id. | modelman's rule (`reconcile.py:84-87`), and `--model` already works that way in `wt stats`. Not `-F`'s comma list: that flag filters a picker, this one a report. | Task 4, `TestBuildUsageRowsFamilyAndFilters` |
| `wt` already has a persistent `-F/--family` (`main.go:410`). Which one does `wt stats --family` mean? | Its own: a local flag that shadows the root's on this command, one exact value, no `-F` shorthand. `wt stats -F x` and `wt -F x stats` are errors (`unknown shorthand flag: 'F'`); today they are accepted and ignored. `--family a,b` is one family named `a,b` and matches nothing. | `stats`' `--model` already shadows the root's `-M/--model` the same way, and `wt stats -M x` is already that error. Reusing the inherited flag would give one command two meanings for a comma. | Task 6, `TestStatsFamilyShadowsTheRootFlag` |
| Row order? | By model id. | There is no family column to sort by first, and the survey table sorts by model id within an agent. | Task 4, `TestBuildUsageRowsJoinsLaunchesAndSpend` |
| How is the table rendered? | Borderless, numbers right-aligned, in its own renderer. Not `renderTable`. | Measured (see "The 80-Column Measurement"): `renderTable` is `len(id)+51` columns for these six columns, so it passes 80 at a 30-character id, and costs two terminal lines per model. | Task 5, `TestRenderUsageTableFitsEightyColumns` |
| What happens to an id too long for the `MODEL` column? | It runs into the blank space left of its own `LAUNCHES` value when that leaves a two-space gap; otherwise it goes on a line of its own with the numbers on the next line. It is never truncated. | An id is what the reader copies into `-M` or `--model`; a shortened one is useless. Ids share prefixes and differ at the end, so no truncation point is safe. | Task 5, `TestRenderUsageTableFitsEightyColumns`, `TestRenderUsageTableOnANarrowTerminal` |
| What width is used? | The terminal's, when stdout is one; no limit otherwise. | A pipe has no width, and `grep`/`awk` need one row per line. | Task 5, `TestRenderUsageTableWithoutAWidthLimit`; Task 6, `TestStatsCmdUsesTheTerminalWidth` |
| What prints when a table is empty? | Each table has its own line: `no survey data`, a blank line, `no usage data`. The two existing tests that asserted stdout was only `no survey data` are updated. | Both tables always appear in the same order, so the output's shape never depends on the data. | Task 6, `TestStatsCmdEmptyStorePrintsMessage` (edited) |
| Is "no configured URL" silent? | No: it is the same one note as the other causes, `wt: spend unavailable: <reason>`. The causes are told apart only in `--json` (`not_configured` vs `unavailable`). | The spec lists it with the other two under "one note goes to stderr". | Task 6, `TestStatsCmdDegradesWhenSpendIsUnavailable`, `TestCollectUsageClassifiesMissingSpend` |
| Where is `os.environ/NAME` resolved from? | wt's own environment first, then the proxy's: `litellm.LoadProxyEnv`, the LaunchAgent plist's `EnvironmentVariables`. wt's own value wins when both have one. Unset in both is "not configured", with a note that names the variable and where wt looked, never a value. When the plist cannot answer (missing, binary, or a restart command is configured) `LoadProxyEnv` already stands wt's environment in for it, and the note then names only wt's environment. | The owner's ruling (2026-10-07): resolve automatically from the local LiteLLM configuration, the way the proxy itself would see it. The plist is where a LaunchAgent install keeps the variable. The existing reader is reused: `ProxyEnv` gains `Lookup`, the value behind `IsSet`, and no second plist parser is written. If wrong, the plist lookup is one branch in `proxyVar`. | Task 2, `TestDatabaseURLResolvesAnEnvironReference`, `TestDatabaseURLWhenThePlistCannotBeRead`, `TestProxyEnvLookupReturnsTheValue` |
| What if `config.yaml` names no `database_url`? | `DATABASE_URL` is looked up the same two ways: wt's environment, then the proxy's. Only when `config.yaml` exists; with no file, `DATABASE_URL` is not consulted. A literal `database_url` is never overridden by it. | LiteLLM's own fallback, and in the owner's ruling. The "only when the file exists" part is this plan's narrowing, not the ruling's: `DATABASE_URL` is a name many tools use, and without a proxy config nothing says the one in the shell is LiteLLM's. It is one `if` to move. | Task 2, `TestDatabaseURLFallsBackToDatabaseURL` |
| A connection string that is empty or only whitespace? | Unset, at every source: the two variables, the `config.yaml` value, a referenced variable in either environment, `DATABASE_URL`. The lookup moves on to the next source; a real value has its surrounding whitespace dropped. | The owner's ruling. A host-less string makes libpq try the local default socket, where a server that is not the proxy's may listen. No valid URL is blank. | Task 2, `TestDatabaseURLBlankCountsAsUnset` |
| Is `config.yaml` read under a redirected registry? | Not when nothing names it, and neither is the proxy's environment (the plist) or `DATABASE_URL`: `DatabaseURL` returns `ErrNoDatabase` before any of them. Naming the database or `config.yaml` lifts it. | The rule `checkRegistryPairing` (`configfile.go:72`) applies to route writes, for the same reason: a scratch run must not act on the real proxy. It is also the third of three guards that keep tests off the database. | Task 2, `TestDatabaseURLSkipsTheDefaultConfigUnderARedirectedRegistry` |
| An unparseable `config.yaml`? | `unavailable` (the error is `litellm.ErrInvalid`), not `not_configured`. | The user has a proxy config and it is broken; "nothing configured" would send them the wrong way. | Task 2, `TestDatabaseURLInvalidConfigIsNotNoDatabase` |
| How does the connection string reach `psql`? | On its argument list, after `-d`; the statement after `-c`. `PGTZ=UTC` is set beside `PGCONNECT_TIMEOUT=3`. | libpq does not expand a URI given in `PGDATABASE`. The cost: the string is visible in `ps` for the second the query runs; `litellm-session-logs/02_export_proxy_server_request.sh` does the same. Documented in `wt-stats.md`. | Task 3, `TestQueryInvocation` |
| How is the window written in SQL? | `"startTime" >= timestamp '<UTC>' AND "startTime" <= timestamp '<UTC>'`, zone-less literals formatted by Go. Nothing else is interpolated. | `startTime` is a zone-less `DateTime` in LiteLLM's Prisma schema, filled with UTC. Python passed zone-aware values. Totals can therefore differ from modelman's at the window's edges; the guide says so. | Task 3, `TestQuerySQL` |
| How are `psql` failures classified? | Exit status 2 is "cannot reach the LiteLLM database"; any other failure, or output that is not the JSON asked for, is "the spend query failed". | Status 2 is psql's own code for a failed connection (observed against an unreachable host). | Task 3, `TestQueryErrorMapping`, `TestQueryExitStatusFromARealProcess` |
| How much of `psql`'s own message goes in the note? | The host and port it tried, and libpq's reason: `cannot reach the LiteLLM database at 127.0.0.1:5432: Connection refused`. Over a socket, the socket path (`at socket /tmp/.s.PGSQL.5432`); for a name that did not resolve, the name. Nothing else from the connection string: not the user, the password or the database name (every double-quoted value in the reason becomes `"..."`), and not the IP a name resolved to. The address is taken only from libpq's `connection to server at "<host>" (<ip>), port <n> failed:` line (or its `on socket` and `could not translate host name` forms), only when it has the shape of a host name or IP address and a port number, and only when the string is written so libpq cannot have taken it from the user name or password: a URI with no `@` after its authority, or a keyword string of plain `key=value` fields with no `host` or `port` after `user` or `password`. Otherwise the reason alone is shown. Text in no known shape becomes a fixed sentence saying the message is withheld. For a query failure: the server's `ERROR:` line, else `psql exited with status N`. There is no redaction pass. | The owner's ruling (2026-10-07): name the host and port, to debug a wrong address, and never the user, database name or password. Observed with fake strings (Task 3): libpq quotes a mis-split password back as a `port`, a percent token, a keyword or a host name, and Go's `url.Parse` fails on the same strings, so redacting "the password" cannot work; an allow-list of shapes can. The written-plainly condition is this plan's addition to the ruling: for `u:1234/x@db` libpq's "host" is the user and its "port" the start of the password, so printing what libpq calls the address would print both. The same text is `usage.spend_reason` in `--json`, which the guide tells people to append to a file. If wrong, it is one function (`unreachable`) in the spend package. | Task 3, `TestQueryNeverLeaksTheConnectionString`, `TestQueryErrorMapping` |
| What is a "request with no model"? | A row whose `model_group` is NULL or empty. Their requests are summed into one count; their tokens and spend are not shown anywhere. | modelman dropped them silently (`reconcile.py:58-61`); the spec asks for a count. | Task 3, `TestQueryParsesTheAggregate`; Task 6, `TestStatsCmdNotesRequestsWithNoModel` |
| Is that note printed under `--model` or `--family`? | No. The count is the whole window's (the query is never filtered), so beside one model's rows it would read as that model's. `--json` still carries it, as `unattributed_requests`, documented as window-wide. | A note must be true of what is on the screen. | Task 6, `TestStatsCmdNotesRequestsWithNoModel` |
| Which clock ends the window? | `statsNow()` for the survey table and the spend query, and it is `--json`'s `as_of`. Launches are bucketed by `usage`'s own clock (`usage.now`) a few milliseconds later, because `AllCounts(agent)` keeps the signature the spec writes. | A launch would have to land in those milliseconds to be counted differently. The cost is in tests: one that pins `statsNow` still seeds launches relative to `time.Now()`. | Task 6, `TestStatsCmdPrintsTheUsageTable` (pins `statsNow`, seeds launches by age) |
| How is a model id with control characters shown? | Escaped in the text table (`\n`, `\x1b`), untouched in `--json`. | Spend-only ids come from the proxy's `model_group`, which wt does not write; a raw escape sequence or newline there would repaint the terminal or break the table. `encoding/json` already escapes them. | Task 5, `TestRenderUsageTableEscapesControlCharacters` |
| Do the guides change in this step? | Yes, by addition only: guide 07 gains a `wt stats` section and guide 00 one bullet. Nothing about `modelman usage report` is removed. | The spec lists no docs work under Step 5 and gives the guide rewrite to Step 6 slice 1. But a command that ships with no guide entry is undiscoverable for as long as Step 6 takes, and Step 6 then has text to keep instead of text to write. | Task 7 |
| With `--agent`, is the database queried? | No, not at all. | Nothing from the answer could be shown. | Task 6, `TestStatsCmdAgentFilterSkipsSpend` |
| What does `--json` contain? | One line. `survey` rows carry counts and unrounded averages, `agent` null for the all-agents row; `usage` carries `spend_status`, `spend_reason`, `unattributed_requests` and `rows` with `family`. Missing values are `null`, lists are `[]`. Notes still go to stderr. | The spec names the four top-level keys only. One line makes `>> file.jsonl` a history, which replaces the guide's `tee usage.md` habit. | Task 9, `TestStatsJSONDocument`, `TestStatsJSONIsOneDocumentInEveryMode` |
| Is `AllCounts` on the `usage.Store` interface? | No, only on `*StoreImpl`, as the spec writes it. Models with no launch in 30 days are left out. | Only `wt stats` needs it, and every other `Store` implementation is a test fake. | Task 1, `TestAllCountsEnumeratesEveryModelInTheFile` |
| Number formats? | Thousands separators for every count, `LAUNCHES` included; `$%.4f` for spend; `-` when not read. | modelman's (`report.py:36`, `:71-72`). | Task 5, `TestFormatCount`, `TestRenderUsageTableShowsDashesWithoutSpend`, `TestRenderUsageTableEscapesControlCharacters` (a four-digit launch count) |

## What Is Ported From Python

Read `modelman/src/modelman/usage/` before starting; it is small. What moves and what does not:

| Python | Go | Notes |
|---|---|---|
| `db.py:84-96`, the row-by-row `SELECT … FROM "LiteLLM_SpendLogs" WHERE "startTime" >= %s AND "startTime" <= %s` | `spend.querySQL` | Same table, same window predicate. Aggregated in SQL (`count(*)`, `sum`) instead of in Python; `request_id`, `model`, `custom_llm_provider`, `total_tokens` are no longer fetched (they were never shown). |
| `db.py:115-139`, `database_url` | `litellm.DatabaseURL` | Same order, with the `WT_` name first. Go goes further in two places Python stopped: an `os.environ/NAME` value and LiteLLM's `DATABASE_URL` fallback are resolved (wt's environment, then the proxy's plist), where Python handed the literal to the driver; and a blank value is unset. |
| `reconcile.py:52-62`, group rows by `model_group` | `GROUP BY 1` on `coalesce(model_group, '')` | The join key: a LiteLLM route's `model_name` is the registry id (`wt/internal/litellm/entry.go`), so `model_group` equals `usage.jsonl`'s `model_id`. |
| `reconcile.py:56-57`, the `_reverse_model_index` fallback | not ported | Dropped by the owner. |
| `reconcile.py:58-61`, drop rows with no model | `spend.Result.Unattributed` | Counted instead of dropped. |
| `reconcile.py:111-123`, `_family_for` | `familyFor` | Registry family, else prefix before `/`, else `unknown`. The `None` branch (unknown to registry and LiteLLM) is not ported: see the decisions table. Two edge cases differ on purpose: Python returns a registry entry's family even when it is empty, and gives the id `/x` the family `""`; Go falls through to the next rule in both, so no row has an empty family for `--family` to be unable to name. |
| `cli.py:52-59`, exit 1 on a missing or unreadable registry | not ported | `wt stats` prints the report with prefix families: see the decisions table. |
| `wt_state.py:17-22`, `wt_dir` and `MODELMAN_WT_DIR` | not ported | wt reads `usage.jsonl` from its own `config.Dir()`. `MODELMAN_WT_DIR` is not in the spec's list of permanent aliases ("What stays"), so it goes with modelman. |
| `reconcile.py:84-87`, filters on every observed id | `buildUsageRows` | Same. |
| `reconcile.py:101-106`, matched / wt_only / litellm_only lists | not ported | Dropped (Reconciliation). The table shows the same three cases as rows. |
| `wt_state.py:25-68`, `read_usage_counts` | `usage.StoreImpl.AllCounts` | wt reads its own file. Window edges are wt's existing ones (`age < window`), which agree with Python's `cutoff < ts`. |
| `wt_state.py:71-75`, `read_last_launched` | not ported | Dropped ("Last wt launch"). |
| `report.py`, Markdown | not ported | Dropped. `{:,}` and `${:.4f}` carry over. |
| `cli.py:26`, `--days` | not ported | Dropped. `--window` is the only window. |

The Python tests that carry over are `test_reconcile.py`'s matched, wt-only, litellm-only, model-filter, family-filter and "family filter excludes a spend-only model" cases (Task 4), `test_wt_state.py`'s window, missing-file and malformed-line cases (Task 1), `test_db.py`'s three `database_url` cases (Task 2) and `test_cli.py`'s "env var only, no config file" case (Task 2). `test_cli.py`'s "unreadable registry" and "dangling registry symlink" cases (`:93`, `:108`) carry over as inputs but with the opposite expectation: Task 6's `TestStatsCmdWorksWithoutARegistry` feeds `wt stats` the same three broken registries and expects the report and exit 0, where Python expected `error:` and exit 1.

## The 80-Column Measurement

Measured in a scratch build with the six headers and realistic cells (`17`, `1,204`, `12,345,678`, `1,234,567`, `$12.3456`):

| Model id | Length | `renderTable` width | Lines per model |
|---|---|---|---|
| `ollama/gemma4:9b` | 16 | 67 | 2 |
| `openrouter/qwen/qwen3.8-27b` | 27 | 78 | 2 |
| `ollama/deepseek-v4-flash:cloud` | 30 | 81 | 2 |
| `omlx/mlx-community--Qwen3.8-27B-4bit` | 36 | 87 | 2 |
| `mtplx/Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality` | 51 | 102 | 2 |

`renderTable` (`helpers.go:38-46`) is `table.New()…Border(lipgloss.NormalBorder()).BorderRow(true)` with no width, so it is `len(id) + 51` columns wide here and draws a rule between every row. It does not fit 80 for ids wt routes every day. Task 5 therefore adds `renderUsageTable`. With the same five ids at 80 columns it prints (this is the test's expected text):

```
MODEL                       LAUNCHES  REQUESTS      PROMPT  COMPLETION     SPEND
mtplx/Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality
                                   3     1,204  12,345,678   1,234,567   $0.0000
ollama/deepseek-v4-flash:cloud    17         0           0           0   $0.0000
ollama/gemma4:9b                   5         4         152         630   $0.0000
omlx/mlx-community--Qwen3.8-27B-4bit
                                   2        88     912,004      40,117   $0.0000
openrouter/qwen/qwen3.8-27b        0         5         517       3,135  $12.3456
```

The survey table keeps `renderTable`: the spec says it is kept, and its ids-plus-seven-columns width is not this step's subject.

## File Structure

| File | Change | PR | Responsibility |
|---|---|---|---|
| `wt/internal/usage/usage.go` | modify | 1 | `AllCounts`; `countsWhere` shares one `scan` with it |
| `wt/internal/usage/usage_test.go` | modify | 1 | `AllCounts` tests |
| `wt/internal/litellm/dburl.go` | create | 1 | `DatabaseURL`, `ErrNoDatabase`, `proxyVar` |
| `wt/internal/litellm/proxyenv.go` | modify | 1 | `ProxyEnv.Lookup`, the value behind `IsSet`; the plist reader itself is unchanged |
| `wt/internal/litellm/dburl_test.go` | create | 1 | Their tests |
| `wt/internal/spend/spend.go` | create | 1 | `Query`, the SQL, the `psql` run, error mapping, and the short list of `psql` message shapes an error may carry — the host and port among them |
| `wt/internal/spend/testmain_test.go` | create | 1 | Fails both seams by default |
| `wt/internal/spend/spend_test.go` | create | 1 | Its tests |
| `wt/cmd/wt/stats_usage.go` | create | 2 | `usageRow`, `buildUsageRows`, `familyFor`; then `usageReport`, `collectUsage`, the `querySpend` and `stdoutWidth` seams |
| `wt/cmd/wt/stats_usage_rows_test.go` | create | 2 | Join, family and filter tests |
| `wt/cmd/wt/stats_usage_table.go` | create | 2 | `renderUsageTable`, `formatCount`, `visibleID` |
| `wt/cmd/wt/stats_usage_table_test.go` | create | 2 | The 80-column tests |
| `wt/cmd/wt/stats.go` | modify | 2, 3 | Wiring, `--family`, notes; then `--json` |
| `wt/cmd/wt/stats_usage_test.go` | create | 2 | Command tests: both tables, degradation, notes, filters, an unreadable registry, the shadowed `-F` |
| `wt/cmd/wt/stats_test.go` | modify | 2 | Two assertions that stdout is only `no survey data` |
| `wt/cmd/wt/testmain_test.go` | modify | 2 | Default stubs for `querySpend` and `stdoutWidth` |
| `wt/cmd/wt/stats_json.go` | create | 3 | `statsJSON`, `buildStatsJSON`, `writeStatsJSON` |
| `wt/cmd/wt/stats_json_test.go` | create | 3 | Golden document and one-document-in-every-mode tests |
| `wt/docs/wt-stats.md` | modify | 2, 3 | The command reference |
| `wt/CLAUDE.md` | modify | 2, 3 | File map, test isolation, database lookup |
| `wt/docs/internals/testing.md` | modify | 2 | The new seams and `TestMain` |
| `wt/CHANGELOG.md` | modify | 2, 3 | One Added entry each |
| `docs/guides/07-usage-and-spend.md` | modify | 2, 3 | A `wt stats` section; the `modelman usage` text stays |
| `docs/guides/00-config-map.md` | modify | 2 | One bullet naming the database variables |

## Branches and PRs

Three PR slices, in this order, as the spec lists them. Each is one branch off an up-to-date `main` and one PR.

| PR | Tasks | Branch | User-visible change |
|---|---|---|---|
| 1 | 1, 2, 3 | `feat/wt-stats-spend-plumbing` | None |
| 2 | 4, 5, 6, 7, 8 | `feat/wt-stats-usage-table` | The usage table, `--family`, the notes |
| 3 | 9 | `feat/wt-stats-json` | `--json` |

Task 8 is the check against the real database. The controller runs it under the owner's OK (2026-10-07); it is not dispatched to a subagent, changes no file and gates PR 2's handoff.

Start each branch after the previous PR has merged, from the remote's `main`. This form also works inside a git worktree, where `git switch main` fails because `main` is checked out elsewhere:

```bash
git fetch origin
git switch -c <branch> origin/main
```

This plan is not on `main` until the branch that carries it (`docs/modelman-retirement-step2-plan` at the time of writing) merges; that commit, push and merge need the owner's OK too.

Line numbers below are as of `main` at `4a7e10a`. Within a PR they drift as earlier tasks land; each step also names the function or quotes the text, and that is what to match on.

Every code block in this plan was built and run in a scratch copy of `wt/` at `4a7e10a`, task by task in this order, with each "expected" line taken from that run — again after the owner's decisions of 2026-10-07 (host and port in the note, automatic resolution through the plist, blank values) were written in. The scratch runs used `psql` only against `postgresql://127.0.0.1:1/x`.

---

## PR 1 — plumbing

Three independent pieces with no caller yet. Nothing a user can see changes.

Create the branch from the monorepo root:

```bash
git fetch origin
git switch -c feat/wt-stats-spend-plumbing origin/main
```

### Task 1: `usage.StoreImpl.AllCounts`

`Counts` and `CountsForAgent` (`usage.go:137`, `:143`) answer only for ids the caller already has, and zero-fill them. `wt stats` has no id list: it must learn which models were launched from the file itself.

**Files:**
- Modify: `wt/internal/usage/usage.go:147-196` (`countsWhere`, to the end of the file)
- Test: `wt/internal/usage/usage_test.go` (append)

**Interfaces:**
- Consumes (existing, package `usage`): `type UsageCounts struct{ OneDay, SevenDay, ThirtyDay int }` (`usage.go:23`), `type event struct{ ModelID, Agent string; Timestamp time.Time }` (`:30`), `var now = time.Now` (`:62`), `const retentionWindow` (`:20`), `func NewStoreAt(dir string) *StoreImpl` (`:53`), `func (s *StoreImpl) path() string` (`:57`).
- Produces: `func (s *StoreImpl) AllCounts(agent string) map[string]UsageCounts` — `agent == ""` counts every launch, legacy agent-less lines included; a named agent counts only its own. The map is never nil and holds only models with a launch in the last 30 days. Not added to the `Store` interface.

- [ ] **Step 1: Write the failing tests**

Append to `wt/internal/usage/usage_test.go` (its imports already cover everything used):

```go
// writeEvents replaces the store's usage.jsonl with the given events, plus
// any raw lines appended verbatim (for malformed-line cases).
func writeEvents(t *testing.T, store *StoreImpl, events []event, raw ...string) {
	t.Helper()
	var data []byte
	for _, e := range events {
		line, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, append(line, '\n')...)
	}
	for _, r := range raw {
		data = append(data, []byte(r+"\n")...)
	}
	if err := os.WriteFile(store.path(), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestAllCountsEnumeratesEveryModelInTheFile verifies AllCounts needs no id
// list: it reports every model with a launch in the last 30 days, in the
// same 1d/7d/30d buckets Counts uses, and skips unparseable lines. `wt
// stats` builds its launch column from it; a model missing here is a model
// whose launches the usage table silently leaves out.
func TestAllCountsEnumeratesEveryModelInTheFile(t *testing.T) {
	store := NewStoreAt(t.TempDir())
	fixed := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	now = func() time.Time { return fixed }
	defer func() { now = time.Now }()

	writeEvents(t, store, []event{
		{ModelID: "ollama/a:1", Agent: "claude", Timestamp: fixed.Add(-30 * time.Minute)},
		{ModelID: "ollama/a:1", Agent: "codex", Timestamp: fixed.Add(-26 * time.Hour)},
		{ModelID: "ollama/a:1", Timestamp: fixed.Add(-10 * 24 * time.Hour)}, // legacy, no agent
		{ModelID: "removed/from-registry", Agent: "claude", Timestamp: fixed.Add(-2 * time.Hour)},
		{ModelID: "ollama/stale", Agent: "claude", Timestamp: fixed.Add(-40 * 24 * time.Hour)},
	}, "not json", `{"model_id":"ollama/a:1","timestamp":"garbage"}`)

	got := store.AllCounts("")
	want := map[string]UsageCounts{
		"ollama/a:1":            {OneDay: 1, SevenDay: 2, ThirtyDay: 3},
		"removed/from-registry": {OneDay: 1, SevenDay: 1, ThirtyDay: 1},
	}
	if len(got) != len(want) {
		t.Fatalf("AllCounts(\"\") = %+v, want exactly %+v (a model with no launch in 30 days is left out)", got, want)
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("AllCounts(\"\")[%q] = %+v, want %+v", id, got[id], w)
		}
	}
}

// TestAllCountsScopesToOneAgent verifies a named agent counts only the
// launches recorded for it: legacy agent-less lines and other agents'
// launches are left out, and a model that agent never launched does not
// appear at all. `wt stats --agent codex` would otherwise show every
// agent's launches under a filter that claims to narrow them.
func TestAllCountsScopesToOneAgent(t *testing.T) {
	store := NewStoreAt(t.TempDir())
	fixed := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	now = func() time.Time { return fixed }
	defer func() { now = time.Now }()

	writeEvents(t, store, []event{
		{ModelID: "ollama/a:1", Agent: "claude", Timestamp: fixed.Add(-1 * time.Hour)},
		{ModelID: "ollama/a:1", Agent: "codex", Timestamp: fixed.Add(-2 * time.Hour)},
		{ModelID: "ollama/a:1", Timestamp: fixed.Add(-3 * time.Hour)},
		{ModelID: "ollama/b:1", Agent: "claude", Timestamp: fixed.Add(-4 * time.Hour)},
	})

	got := store.AllCounts("codex")
	if len(got) != 1 || got["ollama/a:1"] != (UsageCounts{OneDay: 1, SevenDay: 1, ThirtyDay: 1}) {
		t.Fatalf("AllCounts(\"codex\") = %+v, want only ollama/a:1 with one launch", got)
	}
	if got := store.AllCounts("nobody"); len(got) != 0 {
		t.Errorf("AllCounts(\"nobody\") = %+v, want an empty map", got)
	}
}

// TestAllCountsWindowEdgesAndMissingFile verifies the bucket edges are the
// ones Counts uses (an event exactly 24h, 7d or 30d old is outside that
// window) and that a missing usage.jsonl reads as an empty, non-nil map.
// `wt stats` on a fresh install must print "no usage data", not crash, and
// its launch column must agree with the picker's 1d/7d/30d columns.
func TestAllCountsWindowEdgesAndMissingFile(t *testing.T) {
	store := NewStoreAt(t.TempDir())
	fixed := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	now = func() time.Time { return fixed }
	defer func() { now = time.Now }()

	if got := store.AllCounts(""); got == nil || len(got) != 0 {
		t.Fatalf("AllCounts on a missing file = %#v, want an empty non-nil map", got)
	}

	writeEvents(t, store, []event{
		{ModelID: "day", Timestamp: fixed.Add(-24 * time.Hour)},
		{ModelID: "week", Timestamp: fixed.Add(-7 * 24 * time.Hour)},
		{ModelID: "month", Timestamp: fixed.Add(-30 * 24 * time.Hour)},
		{ModelID: "month", Timestamp: fixed.Add(-30*24*time.Hour + time.Second)},
	})
	got := store.AllCounts("")
	want := map[string]UsageCounts{
		"day":   {SevenDay: 1, ThirtyDay: 1},
		"week":  {ThirtyDay: 1},
		"month": {ThirtyDay: 1},
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("AllCounts(\"\")[%q] = %+v, want %+v", id, got[id], w)
		}
	}
	if ids := []string{"day", "week", "month"}; len(got) != len(ids) {
		t.Errorf("AllCounts(\"\") = %+v, want exactly %v", got, ids)
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run from `wt/`: `go test -count=1 ./internal/usage -run TestAllCounts`

Expected: the build fails with

```
internal/usage/usage_test.go:401:15: store.AllCounts undefined (type *StoreImpl has no field or method AllCounts)
```

(and the same message for each other call).

- [ ] **Step 3: Implement**

In `wt/internal/usage/usage.go`, replace everything from the comment `// countsWhere returns 1d/7d/30d counts for each model in modelIDs, zero-filling` (line 147) to the end of the file with:

```go
// AllCounts returns 1d/7d/30d counts for every model that has a launch in
// the file within the last 30 days. Unlike Counts it takes no id list, so
// it also reports models that have since left the registry — `wt stats`
// has no other way to learn which models were launched. An empty agent
// counts every launch, legacy agent-less lines included (the Counts rule);
// a named agent counts only launches recorded for it (the CountsForAgent
// rule). A model whose only events are older than 30 days is left out, so a
// missing file and a stale one both read as an empty map.
func (s *StoreImpl) AllCounts(agent string) map[string]UsageCounts {
	out := map[string]UsageCounts{}
	s.scan(func(ev event, age time.Duration) {
		if agent != "" && ev.Agent != agent {
			return
		}
		if age >= retentionWindow {
			return
		}
		out[ev.ModelID] = out[ev.ModelID].add(age)
	})
	return out
}

// add returns c with one launch of the given age counted in every window
// it falls inside.
func (c UsageCounts) add(age time.Duration) UsageCounts {
	if age < 24*time.Hour {
		c.OneDay++
	}
	if age < 7*24*time.Hour {
		c.SevenDay++
	}
	if age < retentionWindow {
		c.ThirtyDay++
	}
	return c
}

// countsWhere returns 1d/7d/30d counts for each model in modelIDs, zero-filling
// every requested ID (a missing file or a model with no recent events reads
// as zero counts).
func (s *StoreImpl) countsWhere(modelIDs []string, match func(event) bool) map[string]UsageCounts {
	out := map[string]UsageCounts{}
	for _, id := range modelIDs {
		out[id] = UsageCounts{}
	}
	s.scan(func(ev event, age time.Duration) {
		if _, want := out[ev.ModelID]; !want || !match(ev) {
			return
		}
		out[ev.ModelID] = out[ev.ModelID].add(age)
	})
	return out
}

// scan calls fn once per parseable event in the file, with the event's age
// as of now. A missing file yields no calls.
//
// Best-effort for display: if the scan aborts partway (e.g. a single corrupt
// line exceeding bufio.Scanner's token limit), the events seen so far have
// been reported and no error is returned — a truncated read only skews
// displayed numbers. RecordFor's prune path is where scan errors are
// surfaced, because there the result overwrites the on-disk history.
func (s *StoreImpl) scan(fn func(ev event, age time.Duration)) {
	f, err := os.Open(s.path())
	if err != nil {
		return
	}
	defer f.Close()

	today := now().UTC()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var ev event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue
		}
		fn(ev, today.Sub(ev.Timestamp.UTC()))
	}
}
```

`countsWhere` behaves exactly as before (it used a separate `want` set; membership in `out` is the same set). Its "best-effort for display" comment moves to `scan`, which now owns the file read.

- [ ] **Step 4: Run the package**

Run from `wt/`: `go test -count=1 ./internal/usage`

Expected: `ok  	github.com/ohanaverse/local-ai-setup/wt/internal/usage`. The existing `TestCounts*` and `TestRecordFor*` tests pass unchanged; they are the proof the `countsWhere` rewrite changed nothing.

- [ ] **Step 5: Commit**

Run from `wt/` (the `git add` paths are relative to it):

```bash
test -z "$(gofmt -l .)" && go vet ./internal/usage
git add internal/usage/usage.go internal/usage/usage_test.go
git commit -m "feat(wt): usage.AllCounts enumerates every launched model"
```

### Task 2: `litellm.DatabaseURL`

`internal/litellm` already resolves `config.yaml` (`DefaultPath`, `configfile.go:42`: `WT_LITELLM_CONFIG`, then `MODELMAN_LITELLM_CONFIG`, then `~/.config/litellm/config.yaml`) and parses it into a `yaml.Node` tree (`Open`, `:124`), but reads only `model_list` from it. This task adds the one read of `general_settings.database_url`, resolved the way the proxy itself would see it.

That is the owner's ruling (2026-10-07): the connection string resolves automatically from the local LiteLLM configuration. On a LaunchAgent install `config.yaml` either says `database_url: os.environ/DATABASE_URL` or says nothing and lets LiteLLM read `DATABASE_URL` itself, and the variable is set only in the proxy's plist — not in the shell `wt stats` is typed in. The package already reads that plist: `LoadProxyEnv` (`proxyenv.go:74`) returns a `ProxyEnv`, which today answers only `IsSet(name)`. This task gives it `Lookup(name)`, the value behind `IsSet` — one small method over the map the existing parser already fills — and `DatabaseURL` asks it. **Do not write a second plist parser**, and do not read the plist anywhere else.

The lookup, in order: `WT_LITELLM_DATABASE_URL`; `MODELMAN_LITELLM_DATABASE_URL`; `general_settings.database_url` in `config.yaml`. A value `os.environ/NAME` is looked up in wt's own environment and then in the proxy's. When `config.yaml` exists and names no `database_url`, `DATABASE_URL` is looked up the same two ways. A value that is empty or only whitespace is unset wherever it is found.

The package's `TestMain` (`testmain_test.go`) stubs only `runShell`; it does **not** redirect any path, and it does not replace `loadProxyEnv`. So every test here names its own `config.yaml` through `WT_LITELLM_CONFIG`, clears the database variables and replaces `loadProxyEnv` (the package's existing seam, `proxyenv.go:70`) — the `dbConfig` and `stubProxyEnv` helpers below — and the redirected-registry test uses the existing `redirectedRegistry` helper (`service_test.go:1275`), which points `HOME` at a temp directory. Two tests run the real plist reader, against a temp file named by `WT_LITELLM_PLIST` (the existing `writePlist` helper, `proxyenv_test.go:37`). No test may reach `DefaultPath()`'s real default or the real plist.

**Files:**
- Create: `wt/internal/litellm/dburl.go`
- Modify: `wt/internal/litellm/proxyenv.go:41-48` (one method added after `IsSet`)
- Test: `wt/internal/litellm/dburl_test.go`

**Interfaces:**
- Consumes (existing, package `litellm`): `func DefaultPath() string` (`configfile.go:42`), `func namedPath() (string, bool)` (`:51`), `func Open(path string) (*File, error)` (`:124`, returns `ErrMissing` or `ErrInvalid`), `func (f *File) root() *yaml.Node` (`:152`), `func isNull(n *yaml.Node) bool` (`:178`), `func mergedGet(m *yaml.Node, key string) *yaml.Node` (`:470`, nil-safe, follows `<<` merges), `var ErrMissing`, `var ErrInvalid` (`:28`, `:31`); `config.RegistryRedirected() bool` and `config.RegistryPath() string` (`internal/config/registry.go:119`, `:23`); the proxy-environment reader in `proxyenv.go` — `type ProxyEnv struct { Source string; vars map[string]string; ambient bool }` (`:28`; `ambient` is set when the plist could not answer and wt's own environment stands in), `func (e ProxyEnv) IsSet(name string) bool` (`:41`), the seam `var loadProxyEnv = realLoadProxyEnv` (`:70`), `func LoadProxyEnv() ProxyEnv` (`:74`), `func realLoadProxyEnv() ProxyEnv` (`:85`); test helpers `func redirectedRegistry(t *testing.T) string` (`service_test.go:1275`), `func writePlist(t *testing.T, body string) string` and `const proxyPlist` (`proxyenv_test.go:37`, `:11`; the plist sets `IN_PLIST` to `http://box:11434` and `EMPTY_IN_PLIST` to `""`).
- Produces:
  - `func (e ProxyEnv) Lookup(name string) (string, bool)` — the value `name` has for the proxy and whether it is set there; wt's own environment when `e.ambient`
  - `var ErrNoDatabase = errors.New("no LiteLLM database configured")`
  - `func DatabaseURL() (string, error)` — the connection string, never blank, with surrounding whitespace dropped; or an error that wraps `ErrNoDatabase` (nothing names a database) or `ErrInvalid` (`config.yaml` unparseable). No returned error contains a connection string or any variable's value: the reasons name variables, `config.yaml` and the plist path.
  - unexported `func proxyVar(name string) (value, looked string)` — wt's environment, then `loadProxyEnv()`; `looked` is where it searched, for the note

- [ ] **Step 1: Write the failing tests**

Create `wt/internal/litellm/dburl_test.go`:

```go
package litellm

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stubProxyEnv replaces the proxy-environment seam (loadProxyEnv) for this
// test with a LaunchAgent environment holding vars, restored on cleanup. No
// test here may read the developer's real plist: it can hold the real
// connection string.
func stubProxyEnv(t *testing.T, vars map[string]string) {
	t.Helper()
	old := loadProxyEnv
	loadProxyEnv = func() ProxyEnv {
		return ProxyEnv{Source: "the test proxy environment", vars: vars}
	}
	t.Cleanup(func() { loadProxyEnv = old })
}

// dbConfig points WT_LITELLM_CONFIG at a fresh config.yaml holding body (no
// file when body is ""), clears every variable DatabaseURL reads and gives
// the proxy an empty environment, so no test here can fall through to the
// developer's real config.yaml or plist, or pick up a connection string from
// their shell. It returns the file's path.
func dbConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if body != "" {
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("WT_LITELLM_CONFIG", p)
	t.Setenv("MODELMAN_LITELLM_CONFIG", "")
	t.Setenv("WT_LITELLM_DATABASE_URL", "")
	t.Setenv("MODELMAN_LITELLM_DATABASE_URL", "")
	t.Setenv("DATABASE_URL", "")
	stubProxyEnv(t, nil)
	return p
}

// TestDatabaseURLPrecedence pins the lookup order the spec fixes:
// WT_LITELLM_DATABASE_URL, then the legacy MODELMAN_LITELLM_DATABASE_URL,
// then general_settings.database_url in config.yaml. The legacy name is a
// permanent alias, so a shell profile written for `modelman usage report`
// must keep working; and the WT_ name must win so it can override both.
func TestDatabaseURLPrecedence(t *testing.T) {
	const fromFile = "general_settings:\n  database_url: postgresql://file/db\n"
	cases := []struct {
		name         string
		wt, modelman string
		want         string
	}{
		{"file only", "", "", "postgresql://file/db"},
		{"legacy env beats the file", "", "postgresql://legacy/db", "postgresql://legacy/db"},
		{"WT env beats both", "postgresql://wt/db", "postgresql://legacy/db", "postgresql://wt/db"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dbConfig(t, fromFile)
			t.Setenv("WT_LITELLM_DATABASE_URL", c.wt)
			t.Setenv("MODELMAN_LITELLM_DATABASE_URL", c.modelman)
			got, err := DatabaseURL()
			if err != nil || got != c.want {
				t.Fatalf("DatabaseURL() = %q, %v; want %q", got, err, c.want)
			}
		})
	}
}

// TestDatabaseURLEnvNeedsNoConfigFile verifies a connection string from the
// environment is returned without config.yaml being opened at all: the file
// here is missing, and in a second run unparseable. modelman behaves the
// same, and a machine whose proxy config lives elsewhere depends on it.
func TestDatabaseURLEnvNeedsNoConfigFile(t *testing.T) {
	for _, body := range []string{"", "general_settings: [unclosed\n"} {
		dbConfig(t, body)
		t.Setenv("WT_LITELLM_DATABASE_URL", "postgresql://wt/db")
		if got, err := DatabaseURL(); err != nil || got != "postgresql://wt/db" {
			t.Errorf("config body %q: DatabaseURL() = %q, %v; want the env value", body, got, err)
		}
	}
}

// TestDatabaseURLResolvesAnEnvironReference verifies a config value written
// the way LiteLLM's own docs write it — os.environ/NAME — is resolved the way
// the proxy would see it: from wt's own environment, else from the proxy's
// LaunchAgent environment, with wt's own winning when both have it. On the
// usual install the variable is set only in the plist, so a lookup in wt's
// shell alone reports "not configured" on a machine whose proxy is logging
// spend. Unset in both, it is "not configured" with a message that names the
// variable and both places and holds no value from either.
func TestDatabaseURLResolvesAnEnvironReference(t *testing.T) {
	p := dbConfig(t, "general_settings:\n  database_url: os.environ/WT_TEST_DB_URL\n")

	// Only the proxy has it.
	stubProxyEnv(t, map[string]string{"WT_TEST_DB_URL": "postgresql://proxy/db"})
	if got, err := DatabaseURL(); err != nil || got != "postgresql://proxy/db" {
		t.Fatalf("set only for the proxy: DatabaseURL() = %q, %v; want the proxy's value", got, err)
	}

	// Both have it: the shell wt was typed in wins.
	t.Setenv("WT_TEST_DB_URL", "postgresql://shell/db")
	if got, err := DatabaseURL(); err != nil || got != "postgresql://shell/db" {
		t.Fatalf("set in both: DatabaseURL() = %q, %v; want wt's own value", got, err)
	}

	// Only wt's environment has it.
	stubProxyEnv(t, nil)
	if got, err := DatabaseURL(); err != nil || got != "postgresql://shell/db" {
		t.Fatalf("set only for wt: DatabaseURL() = %q, %v; want wt's own value", got, err)
	}

	// Neither has it. The proxy does have other variables, one of them a
	// connection string: none of their values may reach the message.
	t.Setenv("WT_TEST_DB_URL", "")
	stubProxyEnv(t, map[string]string{"OTHER_DB_URL": "postgresql://u:FAKEsecret@other/db"})
	_, err := DatabaseURL()
	if !errors.Is(err, ErrNoDatabase) {
		t.Fatalf("set in neither: err = %v, want ErrNoDatabase", err)
	}
	want := "no LiteLLM database configured: general_settings.database_url in " + p +
		" is os.environ/WT_TEST_DB_URL, and WT_TEST_DB_URL is not set in wt's environment or the test proxy environment" +
		" (set it, or WT_LITELLM_DATABASE_URL)"
	if err.Error() != want {
		t.Errorf("err = %q\nwant  %q", err, want)
	}
	if strings.Contains(err.Error(), "FAKEsecret") || strings.Contains(err.Error(), "postgresql://") {
		t.Errorf("err = %q quotes a value from the proxy's environment", err)
	}

	// The prefix with no name after it: there is no variable to report as
	// unset, so the message says that instead of printing two holes.
	p = dbConfig(t, "general_settings:\n  database_url: os.environ/\n")
	_, err = DatabaseURL()
	if !errors.Is(err, ErrNoDatabase) || !strings.Contains(err.Error(), "is os.environ/ with no variable name") || !strings.Contains(err.Error(), p) {
		t.Errorf("err = %v, want ErrNoDatabase saying the reference names no variable, and naming %s", err, p)
	}
	if strings.Contains(err.Error(), "  ") {
		t.Errorf("err = %q has a hole where a variable name should be", err)
	}
}

// TestDatabaseURLFallsBackToDatabaseURL verifies LiteLLM's own fallback: a
// config.yaml that names no database_url leaves the proxy reading
// DATABASE_URL from its environment, which is how a LaunchAgent install is
// usually wired. wt looks the variable up the same two ways as a reference —
// its own environment, then the proxy's — and says so by name when neither
// has it. A missing config.yaml is not that case: with no proxy config there
// is nothing to say a DATABASE_URL in the shell belongs to LiteLLM.
func TestDatabaseURLFallsBackToDatabaseURL(t *testing.T) {
	const noURL = "general_settings:\n  master_key: x\n"

	p := dbConfig(t, noURL)
	stubProxyEnv(t, map[string]string{"DATABASE_URL": "postgresql://proxy/db"})
	if got, err := DatabaseURL(); err != nil || got != "postgresql://proxy/db" {
		t.Fatalf("DATABASE_URL set only for the proxy: DatabaseURL() = %q, %v; want the proxy's value", got, err)
	}
	t.Setenv("DATABASE_URL", "postgresql://shell/db")
	if got, err := DatabaseURL(); err != nil || got != "postgresql://shell/db" {
		t.Fatalf("DATABASE_URL set in both: DatabaseURL() = %q, %v; want wt's own value", got, err)
	}

	t.Setenv("DATABASE_URL", "")
	stubProxyEnv(t, nil)
	_, err := DatabaseURL()
	want := "no LiteLLM database configured: " + p + " has no general_settings.database_url," +
		" and DATABASE_URL is not set in wt's environment or the test proxy environment (set WT_LITELLM_DATABASE_URL)"
	if !errors.Is(err, ErrNoDatabase) || err.Error() != want {
		t.Errorf("DATABASE_URL set in neither: err = %q\nwant  %q", err, want)
	}

	// A literal database_url is never overridden by DATABASE_URL.
	dbConfig(t, "general_settings:\n  database_url: postgresql://file/db\n")
	t.Setenv("DATABASE_URL", "postgresql://shell/db")
	if got, err := DatabaseURL(); err != nil || got != "postgresql://file/db" {
		t.Errorf("config.yaml names a database: DatabaseURL() = %q, %v; want the file's value", got, err)
	}

	// No config.yaml at all: DATABASE_URL is not consulted.
	dbConfig(t, "")
	t.Setenv("DATABASE_URL", "postgresql://shell/db")
	stubProxyEnv(t, map[string]string{"DATABASE_URL": "postgresql://proxy/db"})
	if got, err := DatabaseURL(); got != "" || !errors.Is(err, ErrNoDatabase) {
		t.Errorf("no config.yaml: DatabaseURL() = %q, %v; want \"\" and ErrNoDatabase", got, err)
	}
}

// TestDatabaseURLWhenThePlistCannotBeRead verifies the lookup with the real
// plist reader and a LaunchAgent plist that cannot answer — missing, binary,
// or bypassed by a configured restart command. wt's own environment is then
// the only source: a variable set there still resolves, and one that is not
// is reported as unset in wt's environment, with no claim about a plist that
// was never read. The plist here is a temp file named by WT_LITELLM_PLIST.
func TestDatabaseURLWhenThePlistCannotBeRead(t *testing.T) {
	dbConfig(t, "general_settings:\n  database_url: os.environ/WT_TEST_DB_URL\n")
	old := loadProxyEnv
	loadProxyEnv = realLoadProxyEnv
	t.Cleanup(func() { loadProxyEnv = old })

	check := func(label string) {
		t.Helper()
		t.Setenv("WT_TEST_DB_URL", "postgresql://shell/db")
		if got, err := DatabaseURL(); err != nil || got != "postgresql://shell/db" {
			t.Errorf("%s: DatabaseURL() = %q, %v; want wt's own value", label, got, err)
		}
		t.Setenv("WT_TEST_DB_URL", "")
		_, err := DatabaseURL()
		if !errors.Is(err, ErrNoDatabase) || !strings.HasSuffix(err.Error(), "and WT_TEST_DB_URL is not set in wt's environment (set it, or WT_LITELLM_DATABASE_URL)") {
			t.Errorf("%s: err = %v, want ErrNoDatabase naming wt's environment only", label, err)
		}
		if err != nil && strings.Contains(err.Error(), "LaunchAgent") {
			t.Errorf("%s: err = %q claims the LaunchAgent plist was consulted", label, err)
		}
	}
	writePlist(t, "")
	check("no plist")
	writePlist(t, "bplist00\x01\x02binary")
	check("binary plist")
	writePlist(t, proxyPlist)
	t.Setenv("WT_LITELLM_RESTART_CMD", "systemctl restart litellm")
	check("a restart command is configured")

	// The same reader, with a plist it can read: the value comes from the
	// file, and the note for a variable it lacks names the file.
	plist := writePlist(t, proxyPlist)
	dbConfig(t, "general_settings:\n  database_url: os.environ/IN_PLIST\n")
	loadProxyEnv = realLoadProxyEnv
	if got, err := DatabaseURL(); err != nil || got != "http://box:11434" {
		t.Errorf("readable plist: DatabaseURL() = %q, %v; want the plist's value for IN_PLIST", got, err)
	}
	dbConfig(t, "general_settings:\n  database_url: os.environ/NOT_IN_PLIST\n")
	loadProxyEnv = realLoadProxyEnv
	if _, err := DatabaseURL(); err == nil || !strings.Contains(err.Error(), "NOT_IN_PLIST is not set in wt's environment or the proxy LaunchAgent's EnvironmentVariables ("+plist+")") {
		t.Errorf("readable plist without the variable: err = %v, want it to name the plist %s", err, plist)
	}
}

// TestDatabaseURLBlankCountsAsUnset verifies a value that is empty or only
// whitespace is "unset" at every source: each of the two variables, the
// config.yaml value, a referenced variable in wt's environment and in the
// proxy's, and DATABASE_URL. A blank string handed to psql is a connection
// string with no host, and libpq then tries the local default socket — a
// server that is not the proxy's database. So the lookup moves on to the
// next source, and with none left answers ErrNoDatabase, never "".
func TestDatabaseURLBlankCountsAsUnset(t *testing.T) {
	const blank = " \t "
	const fromFile = "general_settings:\n  database_url: postgresql://file/db\n"

	// A blank WT_ variable does not win; the legacy variable is next.
	dbConfig(t, fromFile)
	t.Setenv("WT_LITELLM_DATABASE_URL", blank)
	t.Setenv("MODELMAN_LITELLM_DATABASE_URL", "postgresql://legacy/db")
	if got, err := DatabaseURL(); err != nil || got != "postgresql://legacy/db" {
		t.Errorf("blank WT variable: DatabaseURL() = %q, %v; want the legacy variable's value", got, err)
	}
	// Both blank: config.yaml is next.
	t.Setenv("MODELMAN_LITELLM_DATABASE_URL", blank)
	if got, err := DatabaseURL(); err != nil || got != "postgresql://file/db" {
		t.Errorf("both variables blank: DatabaseURL() = %q, %v; want the file's value", got, err)
	}

	// A blank value in config.yaml is no database_url: DATABASE_URL is next,
	// and a blank DATABASE_URL in wt's environment yields to the proxy's.
	dbConfig(t, "general_settings:\n  database_url: \"   \"\n")
	t.Setenv("DATABASE_URL", blank)
	stubProxyEnv(t, map[string]string{"DATABASE_URL": "postgresql://proxy/db"})
	if got, err := DatabaseURL(); err != nil || got != "postgresql://proxy/db" {
		t.Errorf("blank config value and blank DATABASE_URL: DatabaseURL() = %q, %v; want the proxy's value", got, err)
	}

	// Blank everywhere a reference can be resolved: not configured.
	dbConfig(t, "general_settings:\n  database_url: os.environ/WT_TEST_DB_URL\n")
	t.Setenv("WT_TEST_DB_URL", blank)
	stubProxyEnv(t, map[string]string{"WT_TEST_DB_URL": blank})
	got, err := DatabaseURL()
	if got != "" || !errors.Is(err, ErrNoDatabase) ||
		!strings.HasSuffix(err.Error(), "and WT_TEST_DB_URL is not set in wt's environment or the test proxy environment (set it, or WT_LITELLM_DATABASE_URL)") {
		t.Errorf("blank in both environments: DatabaseURL() = %q, %v; want \"\" and ErrNoDatabase naming the variable and both places", got, err)
	}

	// Surrounding whitespace on a real value is dropped, not passed to psql.
	dbConfig(t, "")
	t.Setenv("WT_LITELLM_DATABASE_URL", "  postgresql://wt/db\n")
	if got, err := DatabaseURL(); err != nil || got != "postgresql://wt/db" {
		t.Errorf("padded value: DatabaseURL() = %q, %v; want it trimmed", got, err)
	}
}

// TestProxyEnvLookupReturnsTheValue verifies ProxyEnv.Lookup, the value
// behind IsSet: the plist's string for a variable it sets (the empty string
// included), nothing for one only wt's shell has, and wt's own environment
// when that stands in for an unreadable plist. DatabaseURL resolves the
// proxy's connection string through it.
func TestProxyEnvLookupReturnsTheValue(t *testing.T) {
	writePlist(t, proxyPlist)
	t.Setenv("ONLY_IN_SHELL", "x")
	env := LoadProxyEnv()
	for name, want := range map[string]struct {
		value string
		set   bool
	}{"IN_PLIST": {"http://box:11434", true}, "EMPTY_IN_PLIST": {"", true}, "ONLY_IN_SHELL": {"", false}} {
		if got, ok := env.Lookup(name); got != want.value || ok != want.set {
			t.Errorf("Lookup(%q) = %q, %v; want %q, %v", name, got, ok, want.value, want.set)
		}
	}
	writePlist(t, "")
	if got, ok := LoadProxyEnv().Lookup("ONLY_IN_SHELL"); got != "x" || !ok {
		t.Errorf("no plist: Lookup(ONLY_IN_SHELL) = %q, %v; want wt's own value", got, ok)
	}
}

// TestDatabaseURLNotConfigured verifies each way of having no database is
// ErrNoDatabase and names config.yaml: no file, no general_settings, no
// database_url, a null, empty or blank value, a value that is not a string,
// an os.environ/ reference that names no variable — all with DATABASE_URL
// set nowhere. `wt stats` turns ErrNoDatabase into one quiet note and still
// prints launches; any other error here would read as a broken install on a
// machine that simply runs LiteLLM without spend logging.
func TestDatabaseURLNotConfigured(t *testing.T) {
	cases := map[string]string{
		"missing file":        "",
		"no general_settings": "model_list: []\n",
		"no database_url":     "general_settings:\n  master_key: x\n",
		"null value":          "general_settings:\n  database_url:\n",
		"empty value":         "general_settings:\n  database_url: \"\"\n",
		"blank value":         "general_settings:\n  database_url: \"  \"\n",
		"a mapping":           "general_settings:\n  database_url:\n    host: x\n",
		"bare os.environ/":    "general_settings:\n  database_url: os.environ/\n",
		"settings not a map":  "general_settings: 3\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			p := dbConfig(t, body)
			got, err := DatabaseURL()
			if got != "" || !errors.Is(err, ErrNoDatabase) {
				t.Fatalf("DatabaseURL() = %q, %v; want \"\" and ErrNoDatabase", got, err)
			}
			if !strings.Contains(err.Error(), p) {
				t.Errorf("err = %q, want it to name %s", err, p)
			}
		})
	}
}

// TestDatabaseURLInvalidConfigIsNotNoDatabase verifies an unparseable
// config.yaml is ErrInvalid, not ErrNoDatabase: the user has a proxy config
// and it is broken, which `wt stats` must say rather than report "nothing
// configured". It also pins that the file is not rewritten by the read.
func TestDatabaseURLInvalidConfigIsNotNoDatabase(t *testing.T) {
	p := dbConfig(t, "general_settings: [unclosed\n")
	before, _ := os.ReadFile(p)
	_, err := DatabaseURL()
	if !errors.Is(err, ErrInvalid) || errors.Is(err, ErrNoDatabase) {
		t.Fatalf("err = %v, want ErrInvalid and not ErrNoDatabase", err)
	}
	if after, _ := os.ReadFile(p); string(after) != string(before) {
		t.Errorf("config.yaml changed:\n%s", after)
	}
}

// TestDatabaseURLReadsThroughAMergeKey verifies a database_url supplied by a
// YAML merge ("<<: *defaults") is found, as the proxy itself would see it.
// Missed, `wt stats` says no database is configured on a machine whose proxy
// is logging spend.
func TestDatabaseURLReadsThroughAMergeKey(t *testing.T) {
	dbConfig(t, "defaults: &d\n  database_url: postgresql://merged/db\ngeneral_settings:\n  <<: *d\n  master_key: x\n")
	if got, err := DatabaseURL(); err != nil || got != "postgresql://merged/db" {
		t.Fatalf("DatabaseURL() = %q, %v; want the merged value", got, err)
	}
}

// TestDatabaseURLSkipsTheDefaultConfigUnderARedirectedRegistry verifies the
// scratch-run guard: with the registry redirected and nothing naming
// config.yaml, DatabaseURL answers ErrNoDatabase and never returns the
// database_url the default config.yaml holds. Without it, every `wt stats`
// in a test or under a scratch XDG_CONFIG_HOME would query the developer's
// real spend database. The guard comes before the DATABASE_URL fallback and
// before the proxy's environment is read at all, so a scratch run cannot
// pick the real connection string out of the LaunchAgent plist either.
// Naming the database or config.yaml lifts the guard.
func TestDatabaseURLSkipsTheDefaultConfigUnderARedirectedRegistry(t *testing.T) {
	p := redirectedRegistry(t)
	t.Setenv("WT_LITELLM_DATABASE_URL", "")
	t.Setenv("MODELMAN_LITELLM_DATABASE_URL", "")
	t.Setenv("DATABASE_URL", "postgresql://shell/db")
	asked := 0
	old := loadProxyEnv
	loadProxyEnv = func() ProxyEnv {
		asked++
		return ProxyEnv{Source: "the test proxy environment", vars: map[string]string{"DATABASE_URL": "postgresql://proxy/db"}}
	}
	t.Cleanup(func() { loadProxyEnv = old })
	body := "general_settings:\n  database_url: postgresql://real/db\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := DatabaseURL()
	if got != "" || !errors.Is(err, ErrNoDatabase) {
		t.Fatalf("DatabaseURL() = %q, %v; want \"\" and ErrNoDatabase", got, err)
	}
	if !strings.Contains(err.Error(), "registry is redirected") {
		t.Errorf("err = %q, want the redirected-registry reason", err)
	}
	if asked != 0 {
		t.Errorf("the proxy's environment was read %d times under the guard, want 0", asked)
	}

	t.Setenv("WT_LITELLM_CONFIG", p)
	if got, err := DatabaseURL(); err != nil || got != "postgresql://real/db" {
		t.Errorf("with config.yaml named: DatabaseURL() = %q, %v; want the file's value", got, err)
	}
	t.Setenv("WT_LITELLM_CONFIG", "")
	t.Setenv("WT_LITELLM_DATABASE_URL", "postgresql://explicit/db")
	if got, err := DatabaseURL(); err != nil || got != "postgresql://explicit/db" {
		t.Errorf("with the database named: DatabaseURL() = %q, %v; want the env value", got, err)
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run from `wt/`: `go test -count=1 ./internal/litellm -run 'TestDatabaseURL|TestProxyEnvLookup'`

Expected: the build fails with `undefined: DatabaseURL` and `undefined: ErrNoDatabase` (the compiler stops at ten errors, before it reaches `env.Lookup undefined`).

- [ ] **Step 3: Implement**

In `wt/internal/litellm/proxyenv.go`, directly after the `IsSet` method (it ends at line 48) and before the comment `// ProxyPlistPath is the proxy's LaunchAgent plist: WT_LITELLM_PLIST, else the`, insert:

```go
// Lookup returns the value name has for the proxy, and whether it is set
// there — the value behind IsSet, read from the same place: the plist's
// EnvironmentVariables, or wt's own environment when that stands in.
func (e ProxyEnv) Lookup(name string) (string, bool) {
	if e.ambient {
		return os.LookupEnv(name)
	}
	v, ok := e.vars[name]
	return v, ok
}
```

Nothing else in `proxyenv.go` changes: `plistEnvironment` already parses the values, and `IsSet` keeps its meaning (a variable set to `""` is set — it is `DatabaseURL` that treats a blank connection string as unset).

Create `wt/internal/litellm/dburl.go`:

```go
package litellm

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"gopkg.in/yaml.v3"
)

// ErrNoDatabase: nothing names the LiteLLM proxy's Postgres database. It is
// the "not configured" answer, distinct from a config.yaml that cannot be
// read (ErrInvalid) — `wt stats` reports both, but only this one means there
// is nothing to fix unless the user wants spend.
var ErrNoDatabase = errors.New("no LiteLLM database configured")

// databaseURLEnv are the variables that name the connection string outright,
// in precedence order. The MODELMAN_ name is the permanent legacy alias.
var databaseURLEnv = []string{"WT_LITELLM_DATABASE_URL", "MODELMAN_LITELLM_DATABASE_URL"}

// environRef is the prefix LiteLLM gives a config value that names an
// environment variable instead of holding the value.
const environRef = "os.environ/"

// proxyDatabaseEnv is the variable LiteLLM itself reads when config.yaml
// names no database_url.
const proxyDatabaseEnv = "DATABASE_URL"

// DatabaseURL resolves the connection string of the database the LiteLLM
// proxy logs spend to, the way the proxy itself would see it:
// WT_LITELLM_DATABASE_URL, then the legacy MODELMAN_LITELLM_DATABASE_URL,
// then general_settings.database_url in config.yaml (DefaultPath, so
// WT_LITELLM_CONFIG is honored). A config value of the form os.environ/NAME
// is looked up in wt's own environment and then in the proxy's (proxyVar).
// When config.yaml exists and names no database_url, DATABASE_URL is looked
// up the same two ways — LiteLLM's own fallback.
//
// A value that is empty or only whitespace counts as unset, whatever it came
// from: handed to psql it is a connection string with no host, and libpq
// then tries the local default socket, where some other server may listen.
//
// It only reads. It returns ErrNoDatabase (wrapped, with the reason) when
// nothing names a database, and ErrInvalid when config.yaml exists but
// cannot be parsed. No error it returns contains a connection string: the
// reasons name variables and files, never a value.
//
// Neither config.yaml nor the proxy's environment is consulted when the
// registry is redirected and nothing names config.yaml — the rule
// checkRegistryPairing applies to route writes. A scratch-registry run (a
// test, an experiment under XDG_CONFIG_HOME) must not go on to query the
// real proxy's database; the two variables above still work there, because
// naming the database is as explicit as naming config.yaml.
func DatabaseURL() (string, error) {
	for _, k := range databaseURLEnv {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v, nil
		}
	}
	if _, named := namedPath(); !named && config.RegistryRedirected() {
		return "", fmt.Errorf("%w: the registry is redirected to %s and nothing names config.yaml (set WT_LITELLM_DATABASE_URL, or WT_LITELLM_CONFIG)",
			ErrNoDatabase, config.RegistryPath())
	}
	path := DefaultPath()
	f, err := Open(path)
	if errors.Is(err, ErrMissing) {
		return "", fmt.Errorf("%w: %s does not exist (set WT_LITELLM_DATABASE_URL)", ErrNoDatabase, path)
	}
	if err != nil {
		return "", err
	}
	n := mergedGet(mergedGet(f.root(), "general_settings"), "database_url")
	if n == nil || n.Kind != yaml.ScalarNode || isNull(n) || strings.TrimSpace(n.Value) == "" {
		v, looked := proxyVar(proxyDatabaseEnv)
		if v != "" {
			return v, nil
		}
		return "", fmt.Errorf("%w: %s has no general_settings.database_url, and %s is not set in %s (set WT_LITELLM_DATABASE_URL)",
			ErrNoDatabase, path, proxyDatabaseEnv, looked)
	}
	v := strings.TrimSpace(n.Value)
	name, isRef := strings.CutPrefix(v, environRef)
	if !isRef {
		return v, nil
	}
	if name == "" {
		// No variable is named "": the sentence below would print two holes.
		// The spelling gets its own message, as it does for an api_base
		// (ollamaServeWarnings in configfile.go).
		return "", fmt.Errorf("%w: general_settings.database_url in %s is %s with no variable name (spell it os.environ/<VAR>, or set WT_LITELLM_DATABASE_URL)",
			ErrNoDatabase, path, environRef)
	}
	resolved, looked := proxyVar(name)
	if resolved != "" {
		return resolved, nil
	}
	return "", fmt.Errorf("%w: general_settings.database_url in %s is %s%s, and %s is not set in %s (set it, or WT_LITELLM_DATABASE_URL)",
		ErrNoDatabase, path, environRef, name, name, looked)
}

// proxyVar returns the value of a variable the proxy's configuration relies
// on: from wt's own environment first — what the user set in this shell wins
// — and then from the proxy's (LoadProxyEnv: the LaunchAgent plist's
// EnvironmentVariables). The plist is read only when wt's environment does
// not answer. A blank value is unset in both places.
//
// When there is no value, looked says where it was searched for, for the
// note to quote. When the plist cannot answer, LoadProxyEnv already stands
// wt's environment in for it, so only that is named: the note must not
// claim a file that was not read.
func proxyVar(name string) (value, looked string) {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v, ""
	}
	env := loadProxyEnv()
	if env.ambient {
		return "", "wt's environment"
	}
	if v, _ := env.Lookup(name); strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v), ""
	}
	return "", "wt's environment or " + env.Source
}
```

- [ ] **Step 4: Run the tests**

Run from `wt/`: `go test -count=1 ./internal/litellm -run 'TestDatabaseURL|TestProxyEnvLookup' -v`

Expected: eleven `--- PASS` lines (`TestDatabaseURLPrecedence`, `…EnvNeedsNoConfigFile`, `…ResolvesAnEnvironReference`, `…FallsBackToDatabaseURL`, `…WhenThePlistCannotBeRead`, `…BlankCountsAsUnset`, `TestProxyEnvLookupReturnsTheValue`, `TestDatabaseURLNotConfigured`, `…InvalidConfigIsNotNoDatabase`, `…ReadsThroughAMergeKey`, `…SkipsTheDefaultConfigUnderARedirectedRegistry`), then `ok`.

Then the whole package: `go test -count=1 ./internal/litellm` → `ok`. The existing `TestProxyEnv*` and `TestSync*` tests pass unchanged; they are the proof `proxyenv.go`'s reader behaves as before.

- [ ] **Step 5: Commit**

Run from `wt/` (the `git add` paths are relative to it):

```bash
test -z "$(gofmt -l .)" && go vet ./internal/litellm
git add internal/litellm/dburl.go internal/litellm/dburl_test.go internal/litellm/proxyenv.go
git commit -m "feat(wt): litellm.DatabaseURL resolves the spend database"
```

### Task 3: the `spend` package

One function, `Query`, that runs one statement through `psql` and returns typed rows or a typed error.

What was observed about `psql` (16.15, Homebrew) while writing this plan, with a constant statement, made-up credentials and an address nothing listens on — no database was contacted. Every string names a host and port: an empty or host-less string makes libpq try the local default socket, so none was used and none may be.

```
$ q() { psql -X -w -q -At -v ON_ERROR_STOP=1 -d "$1" -c 'SELECT 1'; echo "exit=$?"; }
$ q 'postgresql://127.0.0.1:1/x'
psql: error: connection to server at "127.0.0.1", port 1 failed: Connection refused
	Is the server running on that host and accepting TCP/IP connections?
exit=2
$ q 'postgresql://litellm:FAKEabc/def@127.0.0.1:1/x'
psql: error: invalid integer value "FAKEabc" for connection option "port"
exit=2
$ q 'postgresql://litellm:FAKEpa%zzword@127.0.0.1:1/x'
psql: error: invalid percent-encoded token: "FAKEpa%zzword"
exit=2
$ q 'host=127.0.0.1 port=1 user=litellm password=FAKEabc FAKEdef'
psql: error: missing "=" after "FAKEdef" in connection info string
exit=2
$ q 'postgresql://litellm:FAKEp@ss@127.0.0.1:1/x'
psql: error: could not translate host name "ss@127.0.0.1" to address: nodename nor servname provided, or not known
exit=2
$ q 'postgresql://litellm:FAKEpw@127.0.0.1:1/x?bogus=FAKEq'
psql: error: invalid URI query parameter: "bogus"
exit=2
```

Three things follow.

- A failed connection is exit status 2, and so is a connection string libpq cannot use. The first line is the useful one; later lines are hints.
- **`psql` quotes pieces of the connection string back, and the piece can be the password.** An unencoded `/` in a password (common in base64) makes libpq read `user:password` as `host:port`; a bad percent escape is printed whole; a stray `@` puts the tail of the password in a "host name". Go's `url.Parse` rejects the first two strings, so a redaction pass that asks Go for "the password" removes nothing exactly when it matters.
- So `Query` never passes `psql`'s text through. For exit status 2 it keeps one known shape — `connection to server at "<host>" (<ip>), port <n> failed: <reason>`, or the `on socket "<path>"` form — and from it `<reason>`, with every double-quoted value replaced by `"..."` (libpq's reason for a refused login is `FATAL:  password authentication failed for user "<user>"`, and a mis-split string can put anything in a user or database name). A `could not translate host name` line becomes a fixed phrase. Everything else becomes one fixed sentence. For any other exit status it keeps a line only if the server wrote it about the statement (`ERROR: …`), which cannot contain the connection string.
- **The owner wants the host and port in the note** (ruling, 2026-10-07), to debug a wrong address: `cannot reach the LiteLLM database at 127.0.0.1:5432: Connection refused`. They come from the same line, `<host>` and `<n>` (the socket path for the socket form, the name for a `could not translate host name` line; never `<ip>`), and pass two checks. First, shape: a host name or IP address, a port of one to five digits, a path ending `.s.PGSQL.<n>`. Second, the connection string must be written so that libpq's host and port are the user's: the second observation above means that for `postgresql://u:1234/x@db/…` libpq's "host" is `u` and its "port" is `1234`, the start of the password. Such a string always leaves an `@` after the first `/`, where a well-formed URI has none; `plainAddress` checks that (and, for a keyword string, that every field is a plain `key=value` with no `host` or `port` after `user` or `password`). When either check fails the note is the reason alone, as it was before the ruling.

Only the first message above (`127.0.0.1`, port 1) was observed for the connect-failure shape. The `(<ip>)`, IPv6, `on socket` and FATAL forms in the tests are libpq's documented formats, written by hand; no server was contacted to produce them.

The statement itself was checked for syntax with libpg_query (the real Postgres parser, through `pglast`, no server): it parses as one statement. The column names are from LiteLLM's installed Prisma schema (`model LiteLLM_SpendLogs`: `spend Float`, `prompt_tokens Int`, `completion_tokens Int`, `startTime DateTime`, `model_group String?`, with an index on `startTime`), the same ones `modelman/src/modelman/usage/db.py:84-96` and `litellm-session-logs/01_export_session_logs.sql` select. **The statement has not been run against a real `"LiteLLM_SpendLogs"` table**; Task 8 does that, against the real database, under the owner's OK.

The package has two seams and a `TestMain` that fails both, so a test that forgets to stub cannot find `psql`, let alone run it. Three tests run a real child process: a shell script written to a temp directory, never `psql`.

**Files:**
- Create: `wt/internal/spend/spend.go`
- Test: `wt/internal/spend/testmain_test.go`, `wt/internal/spend/spend_test.go`

**Interfaces:**
- Consumes: the standard library only.
- Produces (package `spend`):
  - `type Row struct { Model string; Requests, PromptTokens, CompletionTokens int64; Spend float64 }` (JSON tags `model`, `requests`, `prompt_tokens`, `completion_tokens`, `spend`)
  - `type Result struct { Rows []Row; Unattributed int64 }` — `Rows` in the order the database returned them (its collation, not Go's byte order; `buildUsageRows` sorts), never with an empty `Model`
  - `func Query(ctx context.Context, dsn string, start, end time.Time) (Result, error)`
  - `var ErrNoPsql`, `var ErrUnreachable`, `var ErrQuery` — every `Query` error is or wraps one of them. An `ErrUnreachable` reads `cannot reach the LiteLLM database at <host>:<port>: <reason>` (`at socket <path>`, or no `at …` when the address is withheld); no error contains any other part of `dsn`
  - unexported seams `lookPath func(string) (string, error)` and `runPsql func(ctx context.Context, bin string, args, env []string) (stdout, stderr []byte, exit int, err error)`; `var deadline = 10 * time.Second`

- [ ] **Step 1: Write the `TestMain` and the failing tests**

Create `wt/internal/spend/testmain_test.go`:

```go
package spend

import (
	"context"
	"errors"
	"os"
	"testing"
)

// TestMain fails both seams by default, so no test in this package can find
// or run the real psql — and so none can reach a real database, whatever
// connection string the developer's shell or config.yaml holds. A test that
// exercises Query sets the seams itself (stubPsql, or fakePsqlBinary for the
// three tests that run a real process: a shell script, never psql).
func TestMain(m *testing.M) {
	lookPath = func(string) (string, error) {
		return "", errors.New("lookPath not stubbed in this test")
	}
	runPsql = func(context.Context, string, []string, []string) ([]byte, []byte, int, error) {
		return nil, nil, 0, errors.New("runPsql not stubbed in this test")
	}
	os.Exit(m.Run())
}
```

Create `wt/internal/spend/spend_test.go`:

```go
package spend

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// psqlCall records what a stubbed psql was run with.
type psqlCall struct {
	bin  string
	args []string
	env  []string
	n    int
}

// stubPsql makes psql "installed" at /fake/psql and answers every run with
// the given output and exit status. Both seams are restored on cleanup.
func stubPsql(t *testing.T, stdout, stderr string, exit int) *psqlCall {
	t.Helper()
	call := &psqlCall{}
	oldLook, oldRun := lookPath, runPsql
	lookPath = func(string) (string, error) { return "/fake/psql", nil }
	runPsql = func(_ context.Context, bin string, args, env []string) ([]byte, []byte, int, error) {
		call.bin, call.args, call.env = bin, args, env
		call.n++
		return []byte(stdout), []byte(stderr), exit, nil
	}
	t.Cleanup(func() { lookPath, runPsql = oldLook, oldRun })
	return call
}

// fakePsqlBinary writes an executable shell script and points lookPath at
// it, with the real process runner restored. The script stands in for
// psql: no test runs the real one.
func fakePsqlBinary(t *testing.T, script string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "psql")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	oldLook, oldRun := lookPath, runPsql
	lookPath = func(string) (string, error) { return p, nil }
	runPsql = realRunPsql
	t.Cleanup(func() { lookPath, runPsql = oldLook, oldRun })
	return p
}

var (
	winStart = time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	winEnd   = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
)

// TestQueryParsesTheAggregate verifies the JSON document psql prints is
// decoded into rows: json_agg spreads the array over several lines, token
// sums exceed 32 bits, and the group with an empty model becomes the
// Unattributed count instead of a row. These are the numbers in `wt
// stats`'s REQUESTS, PROMPT, COMPLETION and SPEND columns.
func TestQueryParsesTheAggregate(t *testing.T) {
	stubPsql(t, `[{"model":"","requests":3,"prompt_tokens":0,"completion_tokens":0,"spend":0}, 
 {"model":"ollama/gemma4:9b","requests":4,"prompt_tokens":152,"completion_tokens":630,"spend":0}, 
 {"model":"openrouter/qwen/qwen3.8-27b","requests":5,"prompt_tokens":4294967294,"completion_tokens":3135,"spend":0.0085}]
`, "", 0)

	got, err := Query(context.Background(), "postgresql://h/db", winStart, winEnd)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	want := []Row{
		{Model: "ollama/gemma4:9b", Requests: 4, PromptTokens: 152, CompletionTokens: 630},
		{Model: "openrouter/qwen/qwen3.8-27b", Requests: 5, PromptTokens: 4294967294, CompletionTokens: 3135, Spend: 0.0085},
	}
	if !slices.Equal(got.Rows, want) {
		t.Errorf("Rows = %+v, want %+v", got.Rows, want)
	}
	if got.Unattributed != 3 {
		t.Errorf("Unattributed = %d, want 3 (the empty-model group's requests)", got.Unattributed)
	}
}

// TestQueryKeepsOddModelIDsWhole verifies a model group with characters a
// delimited format would choke on — a pipe (psql's own field separator), a
// quote, a comma, a space, non-ASCII — comes back exactly as logged. This
// is why the query returns JSON: the proxy logs whatever public model name
// a client was routed by, and wt joins on the exact string.
func TestQueryKeepsOddModelIDsWhole(t *testing.T) {
	stubPsql(t, `[{"model":"a|b, \"c\" d/é","requests":1,"prompt_tokens":0,"completion_tokens":0,"spend":0}]`, "", 0)
	got, err := Query(context.Background(), "postgresql://h/db", winStart, winEnd)
	if err != nil || len(got.Rows) != 1 || got.Rows[0].Model != `a|b, "c" d/é` {
		t.Fatalf("Query = %+v, %v; want the one model id unchanged", got, err)
	}
}

// TestQueryEmptyWindow verifies a window with no logged requests — psql
// prints "[]" — is an empty result and not an error. A proxy that logged
// nothing this week is a normal state, and `wt stats` must show zeros for
// it rather than "spend unavailable".
func TestQueryEmptyWindow(t *testing.T) {
	stubPsql(t, "[]\n", "", 0)
	got, err := Query(context.Background(), "postgresql://h/db", winStart, winEnd)
	if err != nil || len(got.Rows) != 0 || got.Unattributed != 0 {
		t.Fatalf("Query = %+v, %v; want an empty result and no error", got, err)
	}
}

// TestQueryInvocation pins how psql is run, which is the spec's wording:
// `psql -X -w -q -At -v ON_ERROR_STOP=1`, the connection string after -d
// (libpq does not expand a URI given in PGDATABASE), the statement after
// -c, and PGCONNECT_TIMEOUT=3 in the environment. -X keeps a ~/.psqlrc
// from changing the output format; -w keeps psql from ever stopping at a
// password prompt inside `wt stats`.
func TestQueryInvocation(t *testing.T) {
	call := stubPsql(t, "[]", "", 0)
	if _, err := Query(context.Background(), "postgresql://h/db", winStart, winEnd); err != nil {
		t.Fatalf("Query: %v", err)
	}
	want := []string{"-X", "-w", "-q", "-At", "-v", "ON_ERROR_STOP=1", "-d", "postgresql://h/db", "-c", querySQL(winStart, winEnd)}
	if call.bin != "/fake/psql" || !slices.Equal(call.args, want) {
		t.Errorf("ran %s %q\nwant /fake/psql %q", call.bin, call.args, want)
	}
	for _, kv := range []string{"PGCONNECT_TIMEOUT=3", "PGTZ=UTC"} {
		if !slices.Contains(call.env, kv) {
			t.Errorf("env lacks %s", kv)
		}
	}
	if call.n != 1 {
		t.Errorf("psql ran %d times, want 1 (one aggregated query)", call.n)
	}
}

// TestQuerySQL pins the statement itself: one aggregate over
// "LiteLLM_SpendLogs" grouped by model_group, the columns modelman's report
// summed, and a window written as zone-less UTC literals whatever zone the
// caller's times are in. The window is the port of modelman's
// `"startTime" >= start AND "startTime" <= end`. A times-in-local-zone
// literal here would shift the window by the machine's UTC offset.
func TestQuerySQL(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no tzdata")
	}
	got := querySQL(winStart.In(ny), winEnd.In(ny))
	want := `SELECT coalesce(json_agg(t), '[]'::json) FROM (` +
		`SELECT coalesce(model_group, '') AS model, count(*) AS requests, ` +
		`coalesce(sum(prompt_tokens), 0) AS prompt_tokens, ` +
		`coalesce(sum(completion_tokens), 0) AS completion_tokens, ` +
		`coalesce(sum(spend), 0) AS spend ` +
		`FROM "LiteLLM_SpendLogs" ` +
		`WHERE "startTime" >= timestamp '2026-09-07 12:00:00.000000' ` +
		`AND "startTime" <= timestamp '2026-10-07 12:00:00.000000' ` +
		`GROUP BY 1 ORDER BY 1) t`
	if got != want {
		t.Errorf("querySQL =\n%s\nwant\n%s", got, want)
	}
}

// TestQueryErrorMapping verifies each way psql fails becomes the right
// typed error with a one-line reason: no binary, a refused connection
// (exit 2, with the stderr text psql 16 really prints), a login the server
// refused, a host name that does not resolve, a SQL error, and output that
// is not JSON. `wt stats` prints the message as its one note, and the type
// decides between "not reachable" and "broken" in --json. An unreachable
// database is named by host and port (or socket path) so a wrong address
// can be seen; the user and the database name are blanked, and a host or
// port that is not shaped like one is left out.
func TestQueryErrorMapping(t *testing.T) {
	const refused = "psql: error: connection to server at \"127.0.0.1\", port 1 failed: Connection refused\n" +
		"\tIs the server running on that host and accepting TCP/IP connections?\n"
	// libpq's shape when the server answered and said no.
	const badLogin = "psql: error: connection to server at \"db.example\" (10.0.0.5), port 5432 failed: " +
		"FATAL:  password authentication failed for user \"litellm\"\n"
	const noSuchDB = "psql: error: connection to server on socket \"/tmp/.s.PGSQL.5432\" failed: " +
		"FATAL:  database \"litellm\" does not exist\n"
	const noSuchHost = "psql: error: could not translate host name \"db.example\" to address: " +
		"nodename nor servname provided, or not known\n"
	const ipv6 = "psql: error: connection to server at \"::1\", port 5432 failed: Connection refused\n"
	const oddHost = "psql: error: connection to server at \"not a host!\", port 5432 failed: Connection refused\n"
	const oddPort = "psql: error: connection to server at \"db.example\", port 54x failed: Connection refused\n"
	const oddName = "psql: error: could not translate host name \"not a host!\" to address: " +
		"nodename nor servname provided, or not known\n"
	const oddSocket = "psql: error: connection to server on socket \"/tmp/a b/.s.PGSQL.5432\" failed: No such file or directory\n"
	const withheld = "cannot reach the LiteLLM database: psql could not connect; its message is not shown because it can quote the connection string"
	cases := []struct {
		name           string
		stdout, stderr string
		exit           int
		want           error
		wantMsg        string
	}{
		{"connection refused", "", refused, 2, ErrUnreachable,
			"cannot reach the LiteLLM database at 127.0.0.1:1: Connection refused"},
		{"login refused", "", badLogin, 2, ErrUnreachable,
			`cannot reach the LiteLLM database at db.example:5432: password authentication failed for user "..."`},
		{"no such database, over a socket", "", noSuchDB, 2, ErrUnreachable,
			`cannot reach the LiteLLM database at socket /tmp/.s.PGSQL.5432: database "..." does not exist`},
		{"no such host", "", noSuchHost, 2, ErrUnreachable,
			"cannot reach the LiteLLM database at db.example: the database host name did not resolve"},
		{"an IPv6 address", "", ipv6, 2, ErrUnreachable,
			"cannot reach the LiteLLM database at [::1]:5432: Connection refused"},
		{"a host that is not shaped like one", "", oddHost, 2, ErrUnreachable,
			"cannot reach the LiteLLM database: Connection refused"},
		{"a port that is not a number", "", oddPort, 2, ErrUnreachable,
			"cannot reach the LiteLLM database: Connection refused"},
		{"a socket path that is not shaped like one", "", oddSocket, 2, ErrUnreachable,
			"cannot reach the LiteLLM database: No such file or directory"},
		{"an unresolved name that is not shaped like a host", "", oddName, 2, ErrUnreachable,
			"cannot reach the LiteLLM database: the database host name did not resolve"},
		{"an unknown connection failure", "", "psql: error: something libpq has not said before\n", 2, ErrUnreachable, withheld},
		{"exit 2 and no message", "", "", 2, ErrUnreachable, withheld},
		{"sql error", "", "ERROR:  relation \"LiteLLM_SpendLogs\" does not exist\nLINE 1: ...\n", 1, ErrQuery,
			`the spend query failed: ERROR:  relation "LiteLLM_SpendLogs" does not exist`},
		{"a failure that is not the server's", "", "psql: error: out of memory\n", 1, ErrQuery,
			"the spend query failed: psql exited with status 1"},
		{"silent failure", "", "", 3, ErrQuery,
			"the spend query failed: psql exited with status 3"},
		{"not json", "NOTICE: hello\n", "", 0, ErrQuery, ""},
		{"empty output", "", "", 0, ErrQuery, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stubPsql(t, c.stdout, c.stderr, c.exit)
			_, err := Query(context.Background(), "postgresql://h/db", winStart, winEnd)
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
			if c.wantMsg != "" && err.Error() != c.wantMsg {
				t.Errorf("err = %q\nwant  %q", err, c.wantMsg)
			}
			if strings.Contains(err.Error(), "\n") {
				t.Errorf("err = %q, want one line", err)
			}
		})
	}

	t.Run("no psql", func(t *testing.T) {
		old := lookPath
		lookPath = func(string) (string, error) { return "", errors.New("not found") }
		t.Cleanup(func() { lookPath = old })
		_, err := Query(context.Background(), "postgresql://h/db", winStart, winEnd)
		if !errors.Is(err, ErrNoPsql) || err.Error() != "psql not found on PATH" {
			t.Fatalf("err = %v, want ErrNoPsql", err)
		}
	})
}

// TestQueryNeverLeaksTheConnectionString verifies the only pieces of a
// connection string an error can show are the host and port, and those only
// when the string is written so libpq cannot have taken them from the user
// name or password. Every credential here starts with FAKE: the user, the
// password and the database name must never appear, whatever psql said.
//
// The first block is well-formed strings — a password with percent-escapes,
// a keyword string, a socket directory — where the address is shown and the
// names the server quotes back are blanked. The second is strings libpq
// mis-splits: an unencoded "/" or "@" in the password, a bad percent escape,
// a space in a keyword value. Four of those messages are the ones psql 16
// prints (recorded with made-up credentials against an address nothing
// listens on). The others are written in libpq's connect-failure shape for
// a mis-split string that happens to name a host that answers — there the
// "host" is the user name or the tail of the password, and the "port" a
// piece of the password, so the address is left out. Go's url.Parse
// rejects some of these strings and reads others the way libpq does, so
// nothing that asks Go for "the password" can protect them.
// `wt stats` prints the error on the terminal and in --json's spend_reason,
// which ends up in scrollback, history files and bug reports.
func TestQueryNeverLeaksTheConnectionString(t *testing.T) {
	const withheld = "cannot reach the LiteLLM database: psql could not connect; its message is not shown because it can quote the connection string"
	const refusedAtDB = "psql: error: connection to server at \"db.example\" (10.0.0.5), port 5432 failed: Connection refused\n"
	cases := []struct {
		name, dsn, stderr string
		exit              int
		want              string
	}{
		// Well-formed: the address is shown, nothing else.
		{"a refused login naming the user and database", "postgresql://FAKEuser:FAKEpw@db.example:5432/FAKEdb",
			"psql: error: connection to server at \"db.example\" (10.0.0.5), port 5432 failed: FATAL:  no pg_hba.conf entry for host \"10.0.0.9\", user \"FAKEuser\", database \"FAKEdb\", no encryption\n", 2,
			`cannot reach the LiteLLM database at db.example:5432: no pg_hba.conf entry for host "...", user "...", database "...", no encryption`},
		{"a password with percent-escapes", "postgresql://FAKEuser:FAKEp%40ss%2Fw%25@db.example:5432/FAKEdb",
			"psql: error: connection to server at \"db.example\" (10.0.0.5), port 5432 failed: FATAL:  password authentication failed for user \"FAKEuser\"\n", 2,
			`cannot reach the LiteLLM database at db.example:5432: password authentication failed for user "..."`},
		{"a keyword string", "host=db.example port=5432 user=FAKEuser password=FAKEpw dbname=FAKEdb",
			refusedAtDB, 2,
			"cannot reach the LiteLLM database at db.example:5432: Connection refused"},
		{"a socket directory", "postgresql://FAKEuser:FAKEpw@%2Fvar%2Frun%2Fpostgresql/FAKEdb",
			"psql: error: connection to server on socket \"/var/run/postgresql/.s.PGSQL.5432\" failed: FATAL:  database \"FAKEdb\" does not exist\n", 2,
			`cannot reach the LiteLLM database at socket /var/run/postgresql/.s.PGSQL.5432: database "..." does not exist`},
		{"a query failure that echoes the whole string", "postgresql://FAKEuser:FAKEpw@db.example:5432/FAKEdb",
			"psql: error: could not use postgresql://FAKEuser:FAKEpw@db.example:5432/FAKEdb\n", 1,
			"the spend query failed: psql exited with status 1"},
		{"a connection failure that echoes the whole string", "postgresql://FAKEuser:FAKEpw@db.example:5432/FAKEdb",
			"psql: error: could not use postgresql://FAKEuser:FAKEpw@db.example:5432/FAKEdb\n", 2, withheld},

		// Mis-split by libpq: no address, whatever psql called the host.
		{"a slash in the password, read as host:port", "postgresql://FAKEuser:FAKEabc/def@db.example:5432/FAKEdb",
			"psql: error: invalid integer value \"FAKEabc\" for connection option \"port\"\n", 2, withheld},
		{"a slash after digits in the password, read as a real port", "postgresql://FAKEuser:54321/FAKEdef@db.example:5432/FAKEdb",
			"psql: error: connection to server at \"FAKEuser\" (10.0.0.7), port 54321 failed: Connection refused\n", 2,
			"cannot reach the LiteLLM database: Connection refused"},
		{"a bad percent escape in the password", "postgresql://FAKEuser:FAKEpa%zzword@db.example:5432/FAKEdb",
			"psql: error: invalid percent-encoded token: \"FAKEpa%zzword\"\n", 2, withheld},
		{"an @ in the password, read as the host", "postgresql://FAKEuser:FAKEp@ss@db.example:5432/FAKEdb",
			"psql: error: could not translate host name \"ss@db.example\" to address: nodename nor servname provided, or not known\n", 2,
			"cannot reach the LiteLLM database: the database host name did not resolve"},
		{"an @ in the password, and a tail that is a host name", "postgresql://FAKEuser:FAKEp@ss.example:54321/x@db.example:5432/FAKEdb",
			"psql: error: connection to server at \"ss.example\" (10.0.0.7), port 54321 failed: Connection refused\n", 2,
			"cannot reach the LiteLLM database: Connection refused"},
		{"a keyword string with a space in the password", "host=db.example port=5432 user=FAKEuser password=FAKEabc FAKEdef",
			"psql: error: missing \"=\" after \"FAKEdef\" in connection info string\n", 2, withheld},
		{"a keyword string with a quoted password", "host=db.example port=5432 user=FAKEuser password='FAKE a'",
			refusedAtDB, 2,
			"cannot reach the LiteLLM database: Connection refused"},
		{"a keyword string whose password has a space before port=", "host=db.example user=FAKEuser password=FAKEab port=54321",
			"psql: error: connection to server at \"db.example\" (10.0.0.5), port 54321 failed: Connection refused\n", 2,
			"cannot reach the LiteLLM database: Connection refused"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stubPsql(t, "", c.stderr, c.exit)
			_, err := Query(context.Background(), c.dsn, winStart, winEnd)
			if err == nil {
				t.Fatal("err = nil, want an error")
			}
			if err.Error() != c.want {
				t.Errorf("err = %q\nwant  %q", err, c.want)
			}
			for _, piece := range []string{"FAKE", "zzword", "ss@", "ss.example", "54321", "10.0.0", "%", "postgresql://"} {
				if strings.Contains(err.Error(), piece) {
					t.Errorf("err = %q leaks %q from the connection string", err, piece)
				}
			}
		})
	}
}

// TestQueryRunsARealProcess runs Query end to end against a shell script
// standing in for psql: the arguments and the two environment variables
// reach a real child process, and its stdout comes back parsed. The stubbed
// tests cannot catch a mistake in the exec plumbing itself (a dropped
// environment, stdout and stderr swapped).
func TestQueryRunsARealProcess(t *testing.T) {
	rec := filepath.Join(t.TempDir(), "argv")
	fakePsqlBinary(t, `printf '%s\n' "$@" > "`+rec+`"
printf 'timeout=%s tz=%s\n' "$PGCONNECT_TIMEOUT" "$PGTZ" >> "`+rec+`"
echo 'noise on stderr' >&2
echo '[{"model":"m","requests":2,"prompt_tokens":10,"completion_tokens":20,"spend":0.5}]'
`)
	got, err := Query(context.Background(), "postgresql://h/db", winStart, winEnd)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got.Rows) != 1 || got.Rows[0] != (Row{Model: "m", Requests: 2, PromptTokens: 10, CompletionTokens: 20, Spend: 0.5}) {
		t.Errorf("Rows = %+v, want the script's one row", got.Rows)
	}
	data, _ := os.ReadFile(rec)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	want := []string{"-X", "-w", "-q", "-At", "-v", "ON_ERROR_STOP=1", "-d", "postgresql://h/db", "-c", querySQL(winStart, winEnd), "timeout=3 tz=UTC"}
	if !slices.Equal(lines, want) {
		t.Errorf("the child saw\n%q\nwant\n%q", lines, want)
	}
}

// TestQueryGivesUpAtTheDeadline verifies a psql that never answers is
// killed at the deadline and reported as unreachable. `wt stats` is a
// report people run while waiting for something else; a database host that
// accepts the connection and then hangs must cost seconds, not the
// terminal.
func TestQueryGivesUpAtTheDeadline(t *testing.T) {
	fakePsqlBinary(t, "exec sleep 30\n")
	old := deadline
	deadline = 200 * time.Millisecond
	t.Cleanup(func() { deadline = old })

	began := time.Now()
	_, err := Query(context.Background(), "postgresql://h/db", winStart, winEnd)
	if !errors.Is(err, ErrUnreachable) || !strings.Contains(err.Error(), "no answer within 200ms") {
		t.Fatalf("err = %v, want ErrUnreachable naming the deadline", err)
	}
	if took := time.Since(began); took > 5*time.Second {
		t.Errorf("Query took %s, want it to return at the deadline", took)
	}
}

// TestQueryExitStatusFromARealProcess verifies a real child's exit status 2
// and stderr are read as "unreachable" — the status psql uses for a failed
// connection. A runner that reported every non-zero exit as a Go error
// would turn a database that is simply down into "the spend query failed".
func TestQueryExitStatusFromARealProcess(t *testing.T) {
	fakePsqlBinary(t, `echo 'psql: error: connection to server at "127.0.0.1", port 1 failed: Connection refused' >&2
exit 2
`)
	_, err := Query(context.Background(), "postgresql://h/db", winStart, winEnd)
	if !errors.Is(err, ErrUnreachable) || err.Error() != "cannot reach the LiteLLM database at 127.0.0.1:1: Connection refused" {
		t.Fatalf("err = %v, want ErrUnreachable with the address and libpq's reason", err)
	}
}

// TestSeamsFailClosedByDefault verifies this package's TestMain: with
// neither seam stubbed, Query cannot find psql, so a test added later that
// forgets to stub still cannot run the real binary against the developer's
// database.
func TestSeamsFailClosedByDefault(t *testing.T) {
	if _, err := Query(context.Background(), "postgresql://h/db", winStart, winEnd); !errors.Is(err, ErrNoPsql) {
		t.Fatalf("err = %v, want ErrNoPsql from the TestMain default", err)
	}
	if _, _, _, err := runPsql(context.Background(), "psql", nil, nil); err == nil {
		t.Fatal("the default runPsql ran something; want a hard failure")
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run from `wt/`: `go test -count=1 ./internal/spend`

Expected: the build fails with `undefined: lookPath`, `undefined: runPsql` (and more of the same).

- [ ] **Step 3: Implement**

Create `wt/internal/spend/spend.go`:

```go
// Package spend reads per-model request, token and cost totals from the
// LiteLLM proxy's Postgres spend log ("LiteLLM_SpendLogs") by running one
// aggregated query through psql. wt links no Postgres driver: psql is on
// every machine that runs the proxy's database, and its absence is one of
// the cases Query reports rather than a build dependency.
package spend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

var (
	// ErrNoPsql: there is no psql on PATH.
	ErrNoPsql = errors.New("psql not found on PATH")
	// ErrUnreachable: psql could not connect (its exit status 2), or gave no
	// answer before the deadline.
	ErrUnreachable = errors.New("cannot reach the LiteLLM database")
	// ErrQuery: psql connected and the query failed, or its output was not
	// the JSON document the query asks for.
	ErrQuery = errors.New("the spend query failed")
)

// Row is one model's totals in the window.
type Row struct {
	Model            string  `json:"model"`
	Requests         int64   `json:"requests"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	Spend            float64 `json:"spend"`
}

// Result is the answer to one Query. Rows never holds a row with an empty
// Model: requests the proxy logged without a model group are counted in
// Unattributed instead. Rows comes in the order the database returned it —
// its collation, which is not Go's byte order — so a caller that needs an
// order sorts.
type Result struct {
	Rows         []Row
	Unattributed int64
}

// connectTimeout is PGCONNECT_TIMEOUT, in seconds: how long psql waits for
// a host that does not answer at all. A refused connection fails at once.
const connectTimeout = "3"

// deadline bounds the whole psql run. A var so a test can shorten it.
var deadline = 10 * time.Second

// lookPath finds psql. A seam: tests point it at a fake or fail it.
var lookPath = exec.LookPath

// runPsql runs the psql binary and returns what it printed and its exit
// status. A seam: no test may run the real psql against a real database.
var runPsql = realRunPsql

// timestampLayout is a Postgres timestamp literal with no zone.
const timestampLayout = "2006-01-02 15:04:05.000000"

// querySQL is the one statement Query runs. It returns a single JSON
// document — an array with one object per model group — so the caller
// parses one value instead of delimited rows whose model ids may contain
// any character.
//
// The two timestamps are the only values interpolated, and Go formats both
// from a time.Time, so nothing a user typed reaches the SQL. They are
// written in UTC with no zone because "startTime" is a zone-less timestamp
// column that LiteLLM fills with UTC.
func querySQL(start, end time.Time) string {
	return `SELECT coalesce(json_agg(t), '[]'::json) FROM (` +
		`SELECT coalesce(model_group, '') AS model, ` +
		`count(*) AS requests, ` +
		`coalesce(sum(prompt_tokens), 0) AS prompt_tokens, ` +
		`coalesce(sum(completion_tokens), 0) AS completion_tokens, ` +
		`coalesce(sum(spend), 0) AS spend ` +
		`FROM "LiteLLM_SpendLogs" ` +
		`WHERE "startTime" >= timestamp '` + start.UTC().Format(timestampLayout) + `' ` +
		`AND "startTime" <= timestamp '` + end.UTC().Format(timestampLayout) + `' ` +
		`GROUP BY 1 ORDER BY 1) t`
}

// Query returns per-model totals for requests the proxy logged between
// start and end, both inclusive. dsn is a libpq connection string or URI.
//
// Every failure is one of ErrNoPsql, ErrUnreachable or ErrQuery, wrapped
// with a one-line reason. An ErrUnreachable names the host and port psql
// tried (or the socket), so a wrong address can be seen; no other part of
// dsn appears in any error — not the user, the password or the database
// name. psql's own text is used only in the shapes unreachable and
// queryReason allow.
func Query(ctx context.Context, dsn string, start, end time.Time) (Result, error) {
	bin, err := lookPath("psql")
	if err != nil {
		return Result{}, ErrNoPsql
	}
	ctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()

	args := []string{"-X", "-w", "-q", "-At", "-v", "ON_ERROR_STOP=1", "-d", dsn, "-c", querySQL(start, end)}
	env := append(os.Environ(), "PGCONNECT_TIMEOUT="+connectTimeout, "PGTZ=UTC")
	stdout, stderr, exit, err := runPsql(ctx, bin, args, env)
	switch {
	case ctx.Err() != nil:
		return Result{}, fmt.Errorf("%w: no answer within %s", ErrUnreachable, deadline)
	case err != nil:
		// The process could not be run at all. Go's error names the binary,
		// never its arguments.
		return Result{}, fmt.Errorf("%w: psql could not be run (%v)", ErrQuery, err)
	case exit == 2:
		return Result{}, unreachable(dsn, stderr)
	case exit != 0:
		return Result{}, fmt.Errorf("%w: %s", ErrQuery, queryReason(stderr, exit))
	}

	var rows []Row
	if err := json.Unmarshal(bytes.TrimSpace(stdout), &rows); err != nil {
		return Result{}, fmt.Errorf("%w: psql did not print the JSON document asked for (%v)", ErrQuery, err)
	}
	res := Result{Rows: make([]Row, 0, len(rows))}
	for _, r := range rows {
		if r.Model == "" {
			res.Unattributed += r.Requests
			continue
		}
		res.Rows = append(res.Rows, r)
	}
	return res, nil
}

// realRunPsql runs bin and reports a non-zero exit as a status, not an
// error: err is set only when the process could not be run at all.
func realRunPsql(ctx context.Context, bin string, args, env []string) (stdout, stderr []byte, exit int, err error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = env
	// A killed psql can leave a child holding the pipes; do not wait on it.
	cmd.WaitDelay = time.Second
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err = cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return out.Bytes(), errb.Bytes(), ee.ExitCode(), nil
	}
	return out.Bytes(), errb.Bytes(), 0, err
}

// connectFailed matches the line libpq prints when it reached for a server
// and got no session, in its TCP and its socket form, capturing the host,
// the port, the socket path and the reason:
//
//	connection to server at "db" (10.0.0.5), port 5432 failed: <reason>
//	connection to server on socket "/tmp/.s.PGSQL.5432" failed: <reason>
var connectFailed = regexp.MustCompile(`^connection to server (?:at "([^"]*)"(?: \([^)]*\))?, port (\S+)|on socket "([^"]*)") failed: (.+)$`)

// translateFailed matches libpq's line for a host name with no address,
// capturing the name.
var translateFailed = regexp.MustCompile(`^could not translate host name "([^"]*)" to address: `)

// The only shapes of host, port and socket path an error may show: a host
// name or an IPv4 or IPv6 address, a port number, and a Postgres socket
// file. Anything else psql printed in their place is left out.
var (
	hostShape   = regexp.MustCompile(`^[A-Za-z0-9._:-]+$`)
	portShape   = regexp.MustCompile(`^[0-9]{1,5}$`)
	socketShape = regexp.MustCompile(`^/[A-Za-z0-9._/-]*\.s\.PGSQL\.[0-9]{1,5}$`)
)

// quotedValue matches a double-quoted value in a libpq or server message.
var quotedValue = regexp.MustCompile(`"[^"]*"`)

// stderrLines are the non-empty lines psql wrote, without its
// "psql: error: " prefix.
func stderrLines(stderr []byte) []string {
	var out []string
	for _, line := range strings.Split(string(stderr), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, strings.TrimPrefix(line, "psql: error: "))
		}
	}
	return out
}

// unreachable turns psql's stderr for exit status 2 into an ErrUnreachable
// that says where psql tried to connect and why it failed, and nothing else
// from the connection string:
//
//	cannot reach the LiteLLM database at 127.0.0.1:5432: Connection refused
//
// psql quotes pieces of a connection string it cannot use, and the piece
// can be the password: an unencoded "/" in one makes libpq read
// user:password as host:port and report `invalid integer value "<password>"
// for connection option "port"`. So nothing is passed through as it came.
// Of a "connection to server … failed: <reason>" line the reason is kept
// with every quoted value blanked — the server's refusals name the user and
// the database (`password authentication failed for user "x"`) — and the
// host and port, or the socket path, only as address allows. A host name
// that did not resolve is named on the same terms. Anything else is
// withheld.
func unreachable(dsn string, stderr []byte) error {
	plain := plainAddress(dsn)
	where, reason := "", "psql could not connect; its message is not shown because it can quote the connection string"
	for _, line := range stderrLines(stderr) {
		if m := connectFailed.FindStringSubmatch(line); m != nil {
			reason = quotedValue.ReplaceAllString(strings.TrimSpace(strings.TrimPrefix(m[4], "FATAL:")), `"..."`)
			if plain {
				where = address(m[1], m[2], m[3])
			}
			break
		}
		if m := translateFailed.FindStringSubmatch(line); m != nil {
			reason = "the database host name did not resolve"
			if plain && hostShape.MatchString(m[1]) {
				where = m[1]
			}
			break
		}
	}
	if where == "" {
		return fmt.Errorf("%w: %s", ErrUnreachable, reason)
	}
	return fmt.Errorf("%w at %s: %s", ErrUnreachable, where, reason)
}

// address formats where libpq said it tried to connect: host:port, or
// "socket <path>". It returns "" when a piece is not in the shape of a host
// name, an IP address, a port number or a Postgres socket file.
func address(host, port, socket string) string {
	switch {
	case socket != "":
		if socketShape.MatchString(socket) {
			return "socket " + socket
		}
	case hostShape.MatchString(host) && portShape.MatchString(port):
		return net.JoinHostPort(host, port)
	}
	return ""
}

// plainAddress reports whether dsn is written so that the host and port
// libpq reports are the ones the user meant, and not pieces of the user name
// or password.
//
// libpq splits a URI at the first "/" or "@" it meets, so a password with
// an unencoded "/" turns user:password into host:port — `u:1234/x@db` is
// host "u", port 1234. That leaves an "@" after the authority, where a
// well-formed URI has none. (An unencoded "@" in a password puts its tail
// in the host, which hostShape then refuses.) In a keyword string, a
// password with an unquoted space ends early, and what follows it can be
// read as a port. For any such string the address is left out of the error;
// the reason alone is still shown.
func plainAddress(dsn string) bool {
	rest, isURI := strings.CutPrefix(dsn, "postgresql://")
	if !isURI {
		rest, isURI = strings.CutPrefix(dsn, "postgres://")
	}
	if isURI {
		i := strings.IndexAny(rest, "/?#")
		return i < 0 || !strings.Contains(rest[i:], "@")
	}
	// A keyword/value string: every field a plain key=value, and the address
	// written before the user and password, never after them.
	fields := strings.Fields(dsn)
	afterCredentials := false
	for _, f := range fields {
		k, _, ok := strings.Cut(f, "=")
		switch {
		case !ok || k == "":
			return false
		case k == "user" || k == "password":
			afterCredentials = true
		case afterCredentials && (k == "host" || k == "hostaddr" || k == "port"):
			return false
		}
	}
	return len(fields) > 0
}

// queryReason is the reason for a failure after psql connected: the
// server's own ERROR line about the statement (`relation
// "LiteLLM_SpendLogs" does not exist`), which is about the SQL and cannot
// contain the connection string. Any other text is replaced by the exit
// status.
func queryReason(stderr []byte, exit int) string {
	if lines := stderrLines(stderr); len(lines) > 0 && strings.HasPrefix(lines[0], "ERROR:") {
		return lines[0]
	}
	return fmt.Sprintf("psql exited with status %d", exit)
}
```

- [ ] **Step 4: Run the tests**

Run from `wt/`: `go test -count=1 ./internal/spend -v`

Expected: eleven `--- PASS` lines — `TestQueryParsesTheAggregate`, `TestQueryKeepsOddModelIDsWhole`, `TestQueryEmptyWindow`, `TestQueryInvocation`, `TestQuerySQL`, `TestQueryErrorMapping`, `TestQueryNeverLeaksTheConnectionString`, `TestQueryRunsARealProcess`, `TestQueryGivesUpAtTheDeadline` (about 0.2s), `TestQueryExitStatusFromARealProcess`, `TestSeamsFailClosedByDefault` — then `ok`.

- [ ] **Step 5: Verify the slice**

Run from `wt/`:

```bash
test -z "$(gofmt -l .)" && go vet ./... && go test -count=1 ./... && make check
```

Expected: 21 lines starting with `ok` (one more than before: `internal/spend`), and `make check` ending with the Go format check passing.

- [ ] **Step 6: Commit**

Run from `wt/` (the `git add` path is relative to it):

```bash
git add internal/spend
git commit -m "feat(wt): spend package reads LiteLLM spend through psql"
```

- [ ] **Step 7: Hand off PR 1**

Run from the monorepo root: `make test-all`
Expected: lint passes, the llmbench and modelman suites pass, and every wt `go test` line starts with `ok`.

Stop here. Tell the owner the branch is ready and what `make test-all` printed. Push and open the PR only after their OK. Suggested title: `feat(wt): plumbing for spend in wt stats (retirement step 5, 1 of 3)`. The body says: no user-visible change; three pieces with no caller yet (`usage.AllCounts`, `litellm.DatabaseURL`, `internal/spend`); no test reaches a database or runs `psql`; the SQL has been syntax-checked but not yet run against a real table (that is PR 2's Task 8).

---

## PR 2 — the usage table

Create the branch from the monorepo root, after PR 1 has merged:

```bash
git fetch origin
git switch -c feat/wt-stats-usage-table origin/main
```

### Task 4: join launches and spend into rows

A pure function: launch counts and an optional spend result in, sorted rows out. No I/O, so it is tested directly, as `wt/docs/internals/testing.md` asks ("prefer asserting on unexported functions directly").

**Files:**
- Create: `wt/cmd/wt/stats_usage.go`
- Test: `wt/cmd/wt/stats_usage_rows_test.go`

**Interfaces:**
- Consumes: `usage.UsageCounts` (`internal/usage/usage.go:23`); `spend.Row`, `spend.Result` (Task 3); `survey.Window1d`, `survey.Window7d`, `survey.Window30d` (`internal/survey/stats.go:7`), the durations `parseStatsWindow` returns (`stats.go:91`).
- Produces (package `main`):
  - `type usageRow struct { Model, Family string; Launches int; Spend *spend.Row }` — `Spend` is nil exactly when the report has no spend data
  - `func buildUsageRows(counts map[string]usage.UsageCounts, window time.Duration, sp *spend.Result, families map[string]string, modelFilter, familyFilter string) []usageRow` — `sp == nil` means no spend data; `families` maps registry model id to family and may be nil
  - `func familyFor(id string, families map[string]string) string`
  - `func launchesIn(c usage.UsageCounts, window time.Duration) int`

- [ ] **Step 1: Write the failing tests**

Create `wt/cmd/wt/stats_usage_rows_test.go`:

```go
// Tests for the usage table's rows: the launch × spend join, the family
// rule and the filters. Pure functions, no I/O.
package main

import (
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/spend"
	"github.com/ohanaverse/local-ai-setup/wt/internal/survey"
	"github.com/ohanaverse/local-ai-setup/wt/internal/usage"
)

// counts builds a UsageCounts from its 1d, 7d and 30d values.
func counts(d1, d7, d30 int) usage.UsageCounts {
	return usage.UsageCounts{OneDay: d1, SevenDay: d7, ThirtyDay: d30}
}

// TestBuildUsageRowsJoinsLaunchesAndSpend verifies the join modelman's
// reconcile did, in one table: a model with both launches and spend, one
// with launches only (zero spend, not missing spend), and one with spend
// only (zero launches), sorted by model id. The three cases are the three
// things the report exists to show — traffic through the proxy, launches
// that bypassed it, and proxy use that did not come from wt.
func TestBuildUsageRowsJoinsLaunchesAndSpend(t *testing.T) {
	sp := &spend.Result{Rows: []spend.Row{
		{Model: "openrouter/qwen/qwen3.8-27b", Requests: 5, PromptTokens: 517, CompletionTokens: 3135, Spend: 0.0085},
		{Model: "ollama/both", Requests: 4, PromptTokens: 152, CompletionTokens: 630},
	}}
	rows := buildUsageRows(map[string]usage.UsageCounts{
		"ollama/both":        counts(0, 4, 5),
		"ollama/launch-only": counts(7, 15, 17),
	}, survey.Window7d, sp, nil, "", "")

	if len(rows) != 3 {
		t.Fatalf("rows = %+v, want 3", rows)
	}
	want := []struct {
		model    string
		launches int
		requests int64
	}{
		{"ollama/both", 4, 4},
		{"ollama/launch-only", 15, 0},
		{"openrouter/qwen/qwen3.8-27b", 0, 5},
	}
	for i, w := range want {
		r := rows[i]
		if r.Model != w.model || r.Launches != w.launches {
			t.Errorf("rows[%d] = %s with %d launches, want %s with %d", i, r.Model, r.Launches, w.model, w.launches)
		}
		if r.Spend == nil || r.Spend.Requests != w.requests {
			t.Errorf("rows[%d].Spend = %+v, want %d requests (zero, never nil, when there is spend data)", i, r.Spend, w.requests)
		}
	}
	if got := rows[2].Spend.Spend; got != 0.0085 {
		t.Errorf("spend-only row's spend = %v, want 0.0085", got)
	}
}

// TestBuildUsageRowsWithoutSpendData verifies that with no spend data (nil)
// every launched model still gets a row, with a nil Spend the table renders
// as "-". The spec's degradation rule: launch counts print whatever
// happened to the database.
func TestBuildUsageRowsWithoutSpendData(t *testing.T) {
	rows := buildUsageRows(map[string]usage.UsageCounts{"ollama/a": counts(1, 2, 3)}, survey.Window30d, nil, nil, "", "")
	if len(rows) != 1 || rows[0].Launches != 3 || rows[0].Spend != nil {
		t.Fatalf("rows = %+v, want one row with 3 launches and nil Spend", rows)
	}
}

// TestBuildUsageRowsUsesTheWindowsLaunchBucket verifies the launch count is
// the one bucket matching --window, and that a model with no launch in that
// window and no spend has no row. modelman showed all three buckets beside
// a spend window of a different length; here launches and spend cover the
// same period, so a row reading "0 launches, 5 requests" means what it says.
func TestBuildUsageRowsUsesTheWindowsLaunchBucket(t *testing.T) {
	c := map[string]usage.UsageCounts{"m": counts(0, 2, 5)}
	for _, tc := range []struct {
		window time.Duration
		want   int
	}{{survey.Window1d, -1}, {survey.Window7d, 2}, {survey.Window30d, 5}} {
		rows := buildUsageRows(c, tc.window, nil, nil, "", "")
		switch {
		case tc.want < 0 && len(rows) != 0:
			t.Errorf("window %s: rows = %+v, want none (no launch in the window)", tc.window, rows)
		case tc.want >= 0 && (len(rows) != 1 || rows[0].Launches != tc.want):
			t.Errorf("window %s: rows = %+v, want %d launches", tc.window, rows, tc.want)
		}
	}
}

// TestBuildUsageRowsFamilyAndFilters verifies family resolution and both
// filters, ported from modelman's reconcile tests: the registry's family,
// else the id's provider prefix, else "unknown" (an empty family or prefix
// counts as none, so every row has a family --family can name); --model and
// --family are exact and apply to ids the registry does not know. It also pins the one
// place wt differs from modelman on purpose: a launched model that has left
// the registry keeps its row (modelman dropped it unless it had spend).
func TestBuildUsageRowsFamilyAndFilters(t *testing.T) {
	families := map[string]string{"ollama/gemma4:9b": "gemma4"}
	c := map[string]usage.UsageCounts{
		"ollama/gemma4:9b":  counts(1, 1, 1),
		"ollama/gone:cloud": counts(1, 1, 1),
		"bare-id":           counts(1, 1, 1),
		"/no-prefix":        counts(1, 1, 1),
		"ollama/blank":      counts(1, 1, 1),
	}
	families["ollama/blank"] = "" // a registry entry with no family
	sp := &spend.Result{Rows: []spend.Row{{Model: "openrouter/x/y", Requests: 1}}}

	got := map[string]string{}
	for _, r := range buildUsageRows(c, survey.Window30d, sp, families, "", "") {
		got[r.Model] = r.Family
	}
	want := map[string]string{
		"ollama/gemma4:9b":  "gemma4",
		"ollama/gone:cloud": "ollama",
		"bare-id":           "unknown",
		"/no-prefix":        "unknown",
		"ollama/blank":      "ollama",
		"openrouter/x/y":    "openrouter",
	}
	if len(got) != len(want) {
		t.Fatalf("families = %v, want %v", got, want)
	}
	for id, f := range want {
		if got[id] != f {
			t.Errorf("family of %s = %q, want %q", id, got[id], f)
		}
	}

	ids := func(model, family string) string {
		var out []string
		for _, r := range buildUsageRows(c, survey.Window30d, sp, families, model, family) {
			out = append(out, r.Model)
		}
		return strings.Join(out, ",")
	}
	for _, tc := range []struct{ model, family, want string }{
		{"ollama/gone:cloud", "", "ollama/gone:cloud"},
		{"", "ollama", "ollama/blank,ollama/gone:cloud"},
		{"", "gemma4", "ollama/gemma4:9b"},
		{"", "openrouter", "openrouter/x/y"},
		{"", "gemma", ""}, // exact, not a prefix
		{"ollama/gemma4:9b", "ollama", ""},
	} {
		if got := ids(tc.model, tc.family); got != tc.want {
			t.Errorf("--model %q --family %q: rows = %q, want %q", tc.model, tc.family, got, tc.want)
		}
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run from `wt/`: `go test -count=1 ./cmd/wt -run TestBuildUsageRows`

Expected: the build fails with `undefined: buildUsageRows`.

- [ ] **Step 3: Implement**

Create `wt/cmd/wt/stats_usage.go`:

```go
// The usage half of `wt stats`: launches from usage.jsonl joined with the
// LiteLLM proxy's spend log, one row per model. Split from stats.go so the
// survey report and the usage report can each be read whole.
package main

import (
	"sort"
	"strings"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/spend"
	"github.com/ohanaverse/local-ai-setup/wt/internal/survey"
	"github.com/ohanaverse/local-ai-setup/wt/internal/usage"
)

// usageRow is one model in the usage table. Spend is nil when the report
// has no spend data at all (every status but spendOK); with spend data, a
// model the proxy logged nothing for has a zero Row, not nil.
type usageRow struct {
	Model    string
	Family   string
	Launches int
	Spend    *spend.Row
}

// familyFor is a model's family: the registry's, else the id's provider
// prefix (the part before the first "/"), else "unknown". The order is
// modelman's (usage/reconcile.py _family_for), so a model that has left the
// registry still answers to --family. Unlike modelman, an empty registry
// family or an empty prefix ("/x") falls through to the next rule: no row
// gets a family --family cannot name.
func familyFor(id string, families map[string]string) string {
	if f, ok := families[id]; ok && f != "" {
		return f
	}
	if prefix, _, ok := strings.Cut(id, "/"); ok && prefix != "" {
		return prefix
	}
	return "unknown"
}

// launchesIn picks the count for the report's window.
func launchesIn(c usage.UsageCounts, window time.Duration) int {
	switch window {
	case survey.Window1d:
		return c.OneDay
	case survey.Window7d:
		return c.SevenDay
	}
	return c.ThirtyDay
}

// buildUsageRows joins launch counts with spend rows on the model id — the
// LiteLLM route's model_name is the registry id, so the proxy's model_group
// and usage.jsonl's model_id are the same string. A model gets a row when
// it has a launch in the window or a spend row; sp is nil when there is no
// spend data. Both filters are exact matches and apply to every observed
// id, registered or not. Rows are sorted by model id.
func buildUsageRows(counts map[string]usage.UsageCounts, window time.Duration, sp *spend.Result, families map[string]string, modelFilter, familyFilter string) []usageRow {
	byModel := map[string]*usageRow{}
	row := func(id string) *usageRow {
		r, ok := byModel[id]
		if !ok {
			r = &usageRow{Model: id, Family: familyFor(id, families)}
			if sp != nil {
				r.Spend = &spend.Row{Model: id}
			}
			byModel[id] = r
		}
		return r
	}
	for id, c := range counts {
		if n := launchesIn(c, window); n > 0 {
			row(id).Launches = n
		}
	}
	if sp != nil {
		for _, s := range sp.Rows {
			row(s.Model).Spend = &s
		}
	}

	rows := make([]usageRow, 0, len(byModel))
	for _, r := range byModel {
		if modelFilter != "" && r.Model != modelFilter {
			continue
		}
		if familyFilter != "" && r.Family != familyFilter {
			continue
		}
		rows = append(rows, *r)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Model < rows[j].Model })
	return rows
}
```

- [ ] **Step 4: Run the tests**

Run from `wt/`: `go test -count=1 ./cmd/wt -run TestBuildUsageRows -v`

Expected: four `--- PASS` lines (`…JoinsLaunchesAndSpend`, `…WithoutSpendData`, `…UsesTheWindowsLaunchBucket`, `…FamilyAndFilters`), then `ok`.

- [ ] **Step 5: Commit**

Run from `wt/` (the `git add` paths are relative to it):

```bash
test -z "$(gofmt -l .)" && go vet ./cmd/wt
git add cmd/wt/stats_usage.go cmd/wt/stats_usage_rows_test.go
git commit -m "feat(wt): join launches and LiteLLM spend into usage rows"
```

### Task 5: the table, measured at 80 columns

See "The 80-Column Measurement" above for why this is not `renderTable`. The renderer takes the width as a parameter, so the tests measure it at 80, at 40 and with no limit, with no terminal involved.

**Files:**
- Create: `wt/cmd/wt/stats_usage_table.go`
- Test: `wt/cmd/wt/stats_usage_table_test.go`

**Interfaces:**
- Consumes: `usageRow` (Task 4); `lipgloss.Width` (display width; already a dependency).
- Produces (package `main`):
  - `func renderUsageTable(rows []usageRow, width int) string` — `width <= 0` means no limit; no trailing newline
  - `func formatCount(n int64) string` — `1234567` → `"1,234,567"`
  - `func visibleID(id string) string` — control characters escaped (`"a\nb"` → `a\nb` spelled with a backslash); the text table only
  - `var usageHeaders = [6]string{"MODEL", "LAUNCHES", "REQUESTS", "PROMPT", "COMPLETION", "SPEND"}`

- [ ] **Step 1: Write the failing tests**

Create `wt/cmd/wt/stats_usage_table_test.go`:

```go
// Tests for the usage table's text rendering, measured at 80 columns.
package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/spend"
)

// realisticRows are usage rows with model ids of the lengths wt really
// routes (16 to 51 characters) and large token counts.
func realisticRows() []usageRow {
	row := func(id string, launches int, s *spend.Row) usageRow {
		return usageRow{Model: id, Launches: launches, Spend: s}
	}
	return []usageRow{
		row("mtplx/Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality", 3, &spend.Row{Requests: 1204, PromptTokens: 12345678, CompletionTokens: 1234567}),
		row("ollama/deepseek-v4-flash:cloud", 17, &spend.Row{}),
		row("ollama/gemma4:9b", 5, &spend.Row{Requests: 4, PromptTokens: 152, CompletionTokens: 630}),
		row("omlx/mlx-community--Qwen3.8-27B-4bit", 2, &spend.Row{Requests: 88, PromptTokens: 912004, CompletionTokens: 40117}),
		row("openrouter/qwen/qwen3.8-27b", 0, &spend.Row{Requests: 5, PromptTokens: 517, CompletionTokens: 3135, Spend: 12.3456}),
	}
}

// TestRenderUsageTableFitsEightyColumns is the spec's measurement: with
// real model ids and seven-digit token counts, no line of the usage table
// is wider than an 80-column terminal, no id is truncated, and an id too
// long for the MODEL column either runs into the blank space left of its
// LAUNCHES value (the 30-character id below) or, with no room for that,
// sits on a line of its own with its numbers under the headers on the next
// line (the 36- and 51-character ids). The bordered renderTable the survey
// table uses is len(id)+51 columns wide for these six columns — 81 at a
// 30-character id — and wraps into an unreadable grid.
func TestRenderUsageTableFitsEightyColumns(t *testing.T) {
	got := renderUsageTable(realisticRows(), 80)
	want := strings.Join([]string{
		"MODEL                       LAUNCHES  REQUESTS      PROMPT  COMPLETION     SPEND",
		"mtplx/Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality",
		"                                   3     1,204  12,345,678   1,234,567   $0.0000",
		"ollama/deepseek-v4-flash:cloud    17         0           0           0   $0.0000",
		"ollama/gemma4:9b                   5         4         152         630   $0.0000",
		"omlx/mlx-community--Qwen3.8-27B-4bit",
		"                                   2        88     912,004      40,117   $0.0000",
		"openrouter/qwen/qwen3.8-27b        0         5         517       3,135  $12.3456",
	}, "\n")
	if got != want {
		t.Errorf("table at 80 columns =\n%s\nwant\n%s", got, want)
	}
	for _, line := range strings.Split(got, "\n") {
		if w := lipgloss.Width(line); w > 80 {
			t.Errorf("line is %d columns wide, want at most 80: %q", w, line)
		}
	}
	for _, r := range realisticRows() {
		if !strings.Contains(got, r.Model) {
			t.Errorf("model id %s is missing or truncated", r.Model)
		}
	}
}

// TestRenderUsageTableWithoutAWidthLimit verifies that with no width (a
// pipe or a file, width 0) every model is exactly one line, however long
// its id. `wt stats | grep qwen` and `| awk` depend on one row per line;
// the two-line form is for a terminal only.
func TestRenderUsageTableWithoutAWidthLimit(t *testing.T) {
	rows := realisticRows()
	lines := strings.Split(renderUsageTable(rows, 0), "\n")
	if len(lines) != len(rows)+1 {
		t.Fatalf("%d lines for %d rows, want one line per row plus the header:\n%s", len(lines), len(rows), strings.Join(lines, "\n"))
	}
	if !strings.HasPrefix(lines[1], "mtplx/Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality  ") || !strings.HasSuffix(lines[1], "$0.0000") {
		t.Errorf("long-id row = %q, want the id and its numbers on one line", lines[1])
	}
	// A wide terminal behaves the same: nothing needs to move.
	if wide := renderUsageTable(rows, 200); wide != strings.Join(lines, "\n") {
		t.Errorf("at 200 columns the table differs from the unlimited one:\n%s", wide)
	}
}

// TestRenderUsageTableShowsDashesWithoutSpend verifies the degraded table:
// with no spend data the launch count is still there and REQUESTS, PROMPT,
// COMPLETION and SPEND are each "-", never "0" or "$0.0000". A zero would
// claim the proxy logged nothing, when the truth is that nobody asked it.
func TestRenderUsageTableShowsDashesWithoutSpend(t *testing.T) {
	got := renderUsageTable([]usageRow{{Model: "ollama/gemma4:9b", Launches: 5}}, 80)
	want := "MODEL             LAUNCHES  REQUESTS  PROMPT  COMPLETION  SPEND\n" +
		"ollama/gemma4:9b         5         -       -           -      -"
	if got != want {
		t.Errorf("table =\n%s\nwant\n%s", got, want)
	}
}

// TestRenderUsageTableOnANarrowTerminal verifies a terminal too narrow for
// even the number columns (40 columns) still gets every id whole and every
// number: the MODEL column shrinks to its header and the lines run past the
// edge for the terminal to wrap. Truncating an id to fit would print
// something that cannot be pasted into `wt -M`.
func TestRenderUsageTableOnANarrowTerminal(t *testing.T) {
	got := renderUsageTable(realisticRows(), 40)
	for _, r := range realisticRows() {
		if !strings.Contains(got, r.Model+"\n") && !strings.Contains(got, r.Model+" ") {
			t.Errorf("model id %s is missing or truncated at 40 columns:\n%s", r.Model, got)
		}
	}
	if !strings.Contains(got, "12,345,678") || !strings.Contains(got, "$12.3456") {
		t.Errorf("numbers are missing at 40 columns:\n%s", got)
	}
}

// TestRenderUsageTableEscapesControlCharacters verifies a model id holding
// an escape sequence and a newline is printed with both spelled out, on one
// line, and that the launch count gets the same thousands separators as the
// other counts. Spend-only ids come from the proxy's model_group column,
// which wt does not write; printed raw, such an id recolours the terminal
// and splits its own row in two.
func TestRenderUsageTableEscapesControlCharacters(t *testing.T) {
	got := renderUsageTable([]usageRow{{Model: "evil\x1b[31mred\nline2", Launches: 1234, Spend: &spend.Row{Requests: 1}}}, 80)
	want := "MODEL                   LAUNCHES  REQUESTS  PROMPT  COMPLETION    SPEND\n" +
		`evil\x1b[31mred\nline2     1,234         1       0           0  $0.0000`
	if got != want {
		t.Errorf("table =\n%q\nwant\n%q", got, want)
	}
}

// TestFormatCount pins the thousands separators modelman's report used
// ("{:,}"), at each digit-count edge. Token counts reach nine digits in a
// 30-day window, and an unseparated 123456789 is not readable at a glance.
func TestFormatCount(t *testing.T) {
	for n, want := range map[int64]string{
		0: "0", 7: "7", 999: "999", 1000: "1,000", 12345: "12,345", 123456: "123,456",
		1234567: "1,234,567", 4294967294: "4,294,967,294", -1234: "-1,234",
	} {
		if got := formatCount(n); got != want {
			t.Errorf("formatCount(%d) = %q, want %q", n, got, want)
		}
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run from `wt/`: `go test -count=1 ./cmd/wt -run 'TestRenderUsageTable|TestFormatCount'`

Expected: the build fails with `undefined: renderUsageTable` and `undefined: formatCount`.

- [ ] **Step 3: Implement**

Create `wt/cmd/wt/stats_usage_table.go`:

```go
// The usage table's text rendering: borderless and width-aware.
package main

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/charmbracelet/lipgloss"
)

var usageHeaders = [6]string{"MODEL", "LAUNCHES", "REQUESTS", "PROMPT", "COMPLETION", "SPEND"}

// usageGap separates two columns.
const usageGap = "  "

// usageCells are one row's six cells. The four spend cells are "-" when
// the report has no spend data.
func usageCells(r usageRow) [6]string {
	c := [6]string{visibleID(r.Model), formatCount(int64(r.Launches)), "-", "-", "-", "-"}
	if r.Spend != nil {
		c[2] = formatCount(r.Spend.Requests)
		c[3] = formatCount(r.Spend.PromptTokens)
		c[4] = formatCount(r.Spend.CompletionTokens)
		c[5] = fmt.Sprintf("$%.4f", r.Spend.Spend)
	}
	return c
}

// visibleID is a model id made safe to print in a table: each control
// character (a newline, an escape) is spelled as Go would write it in a
// string literal. Launched ids are wt's own, but a spend-only id is
// whatever the proxy logged as model_group, and a raw escape sequence there
// would repaint the terminal and throw off the column widths. --json prints
// ids through encoding/json, which escapes them itself.
func visibleID(id string) string {
	if strings.IndexFunc(id, unicode.IsControl) < 0 {
		return id
	}
	var b strings.Builder
	for _, r := range id {
		if unicode.IsControl(r) {
			q := strconv.QuoteRune(r) // '\n', '\x1b'
			b.WriteString(q[1 : len(q)-1])
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// formatCount renders n with thousands separators: 1234567 → "1,234,567".
func formatCount(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, d := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(d)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// renderUsageTable lays the rows out without borders: MODEL left-aligned,
// the five number columns right-aligned and sized to their widest cell.
//
// width is the terminal's width, or 0 for no limit. When the longest model
// id and the number columns do not fit together, the MODEL column is
// narrowed to what is left. A model id wider than that runs on into the
// blank space left of its own LAUNCHES value when there is room for it and
// a gap; otherwise it is printed on a line of its own, with its numbers on
// the next line under their headers — what df does with a long device
// name. No id is ever truncated: the id is what a reader copies into
// `wt -M` or `--model`. (visibleID spells out control characters; that is
// the only change an id undergoes.)
//
// The five number columns need about 54 columns with seven-digit token
// counts; on a terminal narrower than those plus "MODEL", the lines are
// longer than the terminal and it wraps them.
func renderUsageTable(rows []usageRow, width int) string {
	cells := make([][6]string, len(rows))
	widths := [6]int{}
	for i, h := range usageHeaders {
		widths[i] = lipgloss.Width(h)
	}
	for i, r := range rows {
		cells[i] = usageCells(r)
		for j, c := range cells[i] {
			widths[j] = max(widths[j], lipgloss.Width(c))
		}
	}
	numbers := 0
	for _, w := range widths[1:] {
		numbers += len(usageGap) + w
	}
	if width > 0 && widths[0]+numbers > width {
		widths[0] = max(width-numbers, lipgloss.Width(usageHeaders[0]))
	}

	var b strings.Builder
	line := func(c [6]string) {
		// first is the column the LAUNCHES cell starts at; the model id may
		// use everything left of it but the gap.
		first := widths[0] + len(usageGap) + widths[1] - lipgloss.Width(c[1])
		model := c[0]
		if lipgloss.Width(model)+len(usageGap) > first {
			b.WriteString(model + "\n")
			model = ""
		}
		b.WriteString(model + strings.Repeat(" ", first-lipgloss.Width(model)) + c[1])
		for j := 2; j < 6; j++ {
			b.WriteString(usageGap + strings.Repeat(" ", widths[j]-lipgloss.Width(c[j])) + c[j])
		}
		b.WriteString("\n")
	}
	line(usageHeaders)
	for _, c := range cells {
		line(c)
	}
	return strings.TrimRight(b.String(), "\n")
}
```

- [ ] **Step 4: Run the tests**

Run from `wt/`: `go test -count=1 ./cmd/wt -run 'TestRenderUsageTable|TestFormatCount' -v`

Expected: six `--- PASS` lines (`TestRenderUsageTableFitsEightyColumns`, `…WithoutAWidthLimit`, `…ShowsDashesWithoutSpend`, `…OnANarrowTerminal`, `…EscapesControlCharacters`, `TestFormatCount`), then `ok`.

- [ ] **Step 5: Commit**

Run from `wt/` (the `git add` paths are relative to it):

```bash
test -z "$(gofmt -l .)" && go vet ./cmd/wt
git add cmd/wt/stats_usage_table.go cmd/wt/stats_usage_table_test.go
git commit -m "feat(wt): width-aware usage table that never truncates a model id"
```

### Task 6: wire the command — second table, `--family`, notes

`statsCmd` (`stats.go:31-88`) today builds survey rows, prints `no survey data` and returns when there are none, and otherwise prints one `renderTable`. After this task it always prints two sections and then the notes.

The seam the spec asks for is `querySpend`, a package-level var in `cmd/wt`. `TestMain` (`testmain_test.go:25`) replaces it for the whole package with a function that returns an error, so every existing and future `wt stats` test gets the degraded report unless it calls `stubSpend`. Two more seams ride along: `stdoutWidth` (so a table does not depend on where `go test` was started) and `statsNow` (so a test can pin the window it expects the database to be asked for).

Notes go to `cmd.ErrOrStderr()`, the stream `wt smoke` uses (`smoke.go:172`), each prefixed `wt: `.

Two facts about the app this command is handed, both from `newApp` (`app.go:83-87`). `a.cfg` is never nil: when `registry.toml` is missing or unreadable, `cfg` is an empty config and `a.loadErr` says why. And nothing gates `wt stats` on that error (the `cfgErr` gate, `main.go:334`, is on the launch path). So a broken registry reaches this code as "no models", every family silently becomes an id prefix, and `--family gemma4` finds nothing. The command says so in one more note, only when `--family` was given.

The new `--family` is a local flag. The root command already has a persistent `-F/--family` (`main.go:410`), which `wt stats` inherits and ignores today; the local flag shadows it on this command, exactly as `stats`' existing `--model` shadows the root's `-M/--model`. `wt stats -F x` therefore stops being accepted. That is a decision (see the table), and `TestStatsFamilyShadowsTheRootFlag` pins it.

**Files:**
- Modify: `wt/cmd/wt/stats_usage.go` (import block; append)
- Modify: `wt/cmd/wt/stats.go:1-4` (file comment), `:29-88` (`statsCmd`)
- Modify: `wt/cmd/wt/testmain_test.go:3-17` (imports), `:64-65` (after `confirmStop`)
- Modify: `wt/cmd/wt/stats_test.go:44-46`, `:56-58`, `:107-109`
- Test: `wt/cmd/wt/stats_usage_test.go`

**Interfaces:**
- Consumes: `a.cfg` and `a.loadErr` as `newApp` sets them (`app.go:83-87`: `cfg` is never nil — a registry that did not load leaves an empty model list — and `loadErr` holds why); the root command's persistent `-F/--family` (`main.go:410`), which the new local flag shadows; `rootCmd()` (`main.go:185`); `usage.NewStore().AllCounts(agent)` (Task 1; `NewStore` reads `config.Dir()`, which `TestMain` redirects); `litellm.DatabaseURL`, `litellm.ErrNoDatabase` (Task 2); `spend.Query`, `spend.Result` (Task 3); `buildUsageRows` (Task 4); `renderUsageTable`, `formatCount` (Task 5); existing `buildStatsRows` (`stats.go:110`), `parseStatsWindow` (`:91`), `renderTable` (`helpers.go:38`), `mustGetString` (`helpers.go:20`), `newTestApp(t) (*app, string)` (`commands_config_test.go:27`, returns the temp `XDG_CONFIG_HOME`), `seedSurveyEvents` (`stats_test.go:25`), `boolPtr` (`stats_test.go:21`).
- Produces (package `main`):
  - `var querySpend = realQuerySpend` with `func realQuerySpend(ctx context.Context, start, end time.Time) (spend.Result, error)`
  - `var stdoutWidth = realStdoutWidth` (`func() int`; 0 when stdout is not a terminal)
  - `var statsNow = func() time.Time { return time.Now().UTC() }`
  - `const spendOK = "ok"`, `spendNotConfigured = "not_configured"`, `spendUnavailable = "unavailable"`, `spendSkipped = "skipped"`
  - `type usageReport struct { Rows []usageRow; SpendStatus, SpendReason string; Unattributed int64; Narrowed bool }` with `func (r usageReport) notes() []string` — `Narrowed` is true under `--model` or `--family`, and silences the no-model note
  - `const registryNote` — the extra stderr note for `--family` when `a.loadErr != nil`
  - `func collectUsage(ctx context.Context, cfg *config.Config, window time.Duration, asOf time.Time, modelFilter, familyFilter, agentFilter string) usageReport`
  - `func surveyTableRows(rows []statsRow) [][]string`, `var surveyHeaders []string`
  - test helpers `stubSpend(t, res spend.Result, err error) *spendCall`, `seedLaunches(t, tmp string, launches ...launch)`, `runStats(t, a *app, args ...string) (stdout, stderr string)` — Task 9 uses all three

- [ ] **Step 1: Write the failing tests**

Create `wt/cmd/wt/stats_usage_test.go`:

```go
// Tests for the usage half of the `wt stats` command: both tables, the
// degraded modes, the notes and the filters. No test here reaches a
// database: TestMain replaces querySpend, and stubSpend is the only way a
// test gets spend rows.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/spend"
	"github.com/ohanaverse/local-ai-setup/wt/internal/survey"
)

// spendCall records what a stubbed querySpend was asked.
type spendCall struct {
	n          int
	start, end time.Time
}

// stubSpend makes querySpend answer res, err for the rest of the test.
func stubSpend(t *testing.T, res spend.Result, err error) *spendCall {
	t.Helper()
	call := &spendCall{}
	old := querySpend
	querySpend = func(_ context.Context, start, end time.Time) (spend.Result, error) {
		call.n++
		call.start, call.end = start, end
		return res, err
	}
	t.Cleanup(func() { querySpend = old })
	return call
}

// launch is one usage.jsonl line: a model launched by an agent, age ago.
type launch struct {
	model, agent string
	age          time.Duration
}

// seedLaunches writes <tmp>/agent-wt/usage.jsonl, where newTestApp's
// XDG_CONFIG_HOME points the usage store.
func seedLaunches(t *testing.T, tmp string, launches ...launch) {
	t.Helper()
	var data []byte
	for _, l := range launches {
		line, err := json.Marshal(map[string]any{
			"model_id": l.model, "agent": l.agent, "timestamp": time.Now().UTC().Add(-l.age),
		})
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, append(line, '\n')...)
	}
	if err := os.WriteFile(filepath.Join(tmp, "agent-wt", "usage.jsonl"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// runStats runs `wt stats` with args and returns stdout and stderr apart.
func runStats(t *testing.T, a *app, args ...string) (stdout, stderr string) {
	t.Helper()
	cmd := statsCmd(a)
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("wt stats %v: %v", args, err)
	}
	return out.String(), errOut.String()
}

// TestStatsCmdPrintsTheUsageTable verifies the whole command with spend
// available: the survey table is still there, the usage table follows it
// after a blank line with launches and spend joined per model, nothing is
// written to stderr, and the database is asked for exactly the --window
// ending now. This is `modelman usage report`'s replacement.
func TestStatsCmdPrintsTheUsageTable(t *testing.T) {
	a, tmp := newTestApp(t)
	seedSurveyEvents(t, tmp, []survey.Event{
		{Agent: "claude", ModelID: "ollama/gemma4:9b", Timestamp: time.Now().Add(-time.Hour), Worked: boolPtr(true)},
	})
	seedLaunches(t, tmp,
		launch{"ollama/gemma4:9b", "claude", time.Hour},
		launch{"ollama/gemma4:9b", "codex", 3 * 24 * time.Hour},
		launch{"ollama/old", "claude", 10 * 24 * time.Hour},
	)
	call := stubSpend(t, spend.Result{Rows: []spend.Row{
		{Model: "ollama/gemma4:9b", Requests: 1204, PromptTokens: 1234567, CompletionTokens: 630, Spend: 0},
		{Model: "openrouter/qwen/qwen3.8-27b", Requests: 5, PromptTokens: 517, CompletionTokens: 3135, Spend: 0.0085},
	}}, nil)
	asOf := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	oldNow := statsNow
	statsNow = func() time.Time { return asOf }
	t.Cleanup(func() { statsNow = oldNow })

	stdout, stderr := runStats(t, a, "--window", "7d")

	wantUsage := "\n\n" +
		"MODEL                        LAUNCHES  REQUESTS     PROMPT  COMPLETION    SPEND\n" +
		"ollama/gemma4:9b                    2     1,204  1,234,567         630  $0.0000\n" +
		"openrouter/qwen/qwen3.8-27b         0         5        517       3,135  $0.0085\n"
	if !strings.HasSuffix(stdout, wantUsage) {
		t.Errorf("stdout =\n%s\nwant it to end with a blank line and\n%s", stdout, wantUsage)
	}
	if !strings.Contains(stdout, "(all)") || strings.Contains(stdout, "no survey data") {
		t.Errorf("stdout =\n%s\nwant the survey table kept above the usage table", stdout)
	}
	if strings.Contains(stdout, "ollama/old") {
		t.Errorf("stdout lists ollama/old, whose only launch is outside the 7d window:\n%s", stdout)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want nothing when spend is available", stderr)
	}
	if call.n != 1 || !call.end.Equal(asOf) || !call.start.Equal(asOf.Add(-7*24*time.Hour)) {
		t.Errorf("querySpend called %d times for %s..%s, want once for the 7 days ending %s", call.n, call.start, call.end, asOf)
	}
}

// TestStatsCmdDegradesWhenSpendIsUnavailable verifies the spec's
// degradation for each cause — no psql, no reachable database, no
// configured URL: the launch counts print, the spend cells show "-",
// exactly one note goes to stderr, stdout carries no note, and the command
// succeeds. `wt stats` is a report; a laptop away from its database must
// not turn it into an error.
func TestStatsCmdDegradesWhenSpendIsUnavailable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		note string
	}{
		{"no psql", spend.ErrNoPsql, "wt: spend unavailable: psql not found on PATH\n"},
		{"database down", fmt.Errorf("%w at 127.0.0.1:5432: Connection refused", spend.ErrUnreachable),
			"wt: spend unavailable: cannot reach the LiteLLM database at 127.0.0.1:5432: Connection refused\n"},
		{"no url", fmt.Errorf("%w: /x/config.yaml has no general_settings.database_url (set WT_LITELLM_DATABASE_URL)", litellm.ErrNoDatabase),
			"wt: spend unavailable: no LiteLLM database configured: /x/config.yaml has no general_settings.database_url (set WT_LITELLM_DATABASE_URL)\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, tmp := newTestApp(t)
			seedLaunches(t, tmp, launch{"ollama/gemma4:9b", "claude", time.Hour})
			stubSpend(t, spend.Result{}, c.err)

			stdout, stderr := runStats(t, a)

			want := "no survey data\n\n" +
				"MODEL             LAUNCHES  REQUESTS  PROMPT  COMPLETION  SPEND\n" +
				"ollama/gemma4:9b         1         -       -           -      -\n"
			if stdout != want {
				t.Errorf("stdout =\n%s\nwant\n%s", stdout, want)
			}
			if stderr != c.note {
				t.Errorf("stderr = %q\nwant     %q (exactly one note)", stderr, c.note)
			}
		})
	}
}

// TestCollectUsageClassifiesMissingSpend verifies the report's spend status
// for each outcome: ok, not_configured (nothing names a database),
// unavailable (anything else that failed) and skipped (--agent). The status
// is what --json consumers branch on; "not configured" must not be lumped
// with "the database is down".
func TestCollectUsageClassifiesMissingSpend(t *testing.T) {
	a, _ := newTestApp(t)
	asOf := time.Now().UTC()
	for _, tc := range []struct {
		name, agent string
		err         error
		want        string
	}{
		{"ok", "", nil, spendOK},
		{"no database", "", fmt.Errorf("%w: reason", litellm.ErrNoDatabase), spendNotConfigured},
		{"bad config.yaml", "", fmt.Errorf("%w: reason", litellm.ErrInvalid), spendUnavailable},
		{"no psql", "", spend.ErrNoPsql, spendUnavailable},
		{"agent", "claude", nil, spendSkipped},
	} {
		stubSpend(t, spend.Result{Unattributed: 2}, tc.err)
		rep := collectUsage(context.Background(), a.cfg, survey.Window30d, asOf, "", "", tc.agent)
		if rep.SpendStatus != tc.want {
			t.Errorf("%s: SpendStatus = %q, want %q", tc.name, rep.SpendStatus, tc.want)
		}
		if (rep.SpendReason == "") != (tc.want == spendOK) {
			t.Errorf("%s: SpendReason = %q, want a reason exactly when spend is missing", tc.name, rep.SpendReason)
		}
		if wantUn := int64(0); tc.want == spendOK {
			wantUn = 2
			if rep.Unattributed != wantUn {
				t.Errorf("%s: Unattributed = %d, want %d", tc.name, rep.Unattributed, wantUn)
			}
		} else if rep.Unattributed != 0 {
			t.Errorf("%s: Unattributed = %d, want 0 without spend data", tc.name, rep.Unattributed)
		}
	}
}

// TestStatsCmdNotesRequestsWithNoModel verifies requests the proxy logged
// with an empty model group are counted in a stderr note rather than shown
// as a nameless row or dropped in silence (modelman dropped them without a
// word). The count explains a REQUESTS total that is lower than LiteLLM's
// own. Under --model or --family the note is not printed: the count is not
// about the rows shown.
func TestStatsCmdNotesRequestsWithNoModel(t *testing.T) {
	for n, want := range map[int64]string{
		1:    "wt: 1 request had no model and is not shown\n",
		1234: "wt: 1,234 requests had no model and are not shown\n",
	} {
		a, _ := newTestApp(t)
		stubSpend(t, spend.Result{Rows: []spend.Row{{Model: "m", Requests: 2}}, Unattributed: n}, nil)
		stdout, stderr := runStats(t, a)
		if stderr != want {
			t.Errorf("stderr = %q, want %q", stderr, want)
		}
		if strings.Contains(stdout, "no model") {
			t.Errorf("stdout carries the note; notes belong on stderr:\n%s", stdout)
		}
	}

	// Under --model or --family the count is still the whole window's (the
	// query is never filtered), so beside one model's rows the note would
	// read as that model's. It is left out.
	a, _ := newTestApp(t)
	stubSpend(t, spend.Result{Rows: []spend.Row{{Model: "ollama/m", Requests: 2}}, Unattributed: 7}, nil)
	for _, args := range [][]string{{"--model", "ollama/m"}, {"--family", "ollama"}} {
		stdout, stderr := runStats(t, a, args...)
		if stderr != "" {
			t.Errorf("wt stats %v: stderr = %q, want no note about a window-wide count beside a filtered table", args, stderr)
		}
		if !strings.Contains(stdout, "ollama/m") {
			t.Errorf("wt stats %v: stdout =\n%s\nwant the ollama/m row", args, stdout)
		}
	}
}

// TestStatsCmdAgentFilterSkipsSpend verifies --agent narrows the launch
// counts to that agent, does not query the database at all, and says why in
// one note. The spend log has no agent column; showing every agent's spend
// beside one agent's launches would be a number that means nothing.
func TestStatsCmdAgentFilterSkipsSpend(t *testing.T) {
	a, tmp := newTestApp(t)
	seedLaunches(t, tmp,
		launch{"ollama/gemma4:9b", "claude", time.Hour},
		launch{"ollama/gemma4:9b", "codex", time.Hour},
		launch{"ollama/gemma4:9b", "codex", 2 * time.Hour},
		launch{"ollama/other", "claude", time.Hour},
	)
	call := stubSpend(t, spend.Result{Rows: []spend.Row{{Model: "ollama/gemma4:9b", Requests: 9}}}, nil)

	stdout, stderr := runStats(t, a, "--agent", "codex")

	want := "no survey data\n\n" +
		"MODEL             LAUNCHES  REQUESTS  PROMPT  COMPLETION  SPEND\n" +
		"ollama/gemma4:9b         2         -       -           -      -\n"
	if stdout != want {
		t.Errorf("stdout =\n%s\nwant\n%s", stdout, want)
	}
	if call.n != 0 {
		t.Errorf("querySpend called %d times, want 0 with --agent", call.n)
	}
	if stderr != "wt: --agent narrows launches only; LiteLLM does not log which agent sent a request, so spend is not shown\n" {
		t.Errorf("stderr = %q, want the one --agent note", stderr)
	}
}

// TestStatsCmdFamilyFiltersOnlyTheUsageTable verifies --family narrows the
// usage table and leaves the survey table alone, and that --model narrows
// both. The survey rows carry no family, so a --family that emptied the
// survey table would hide answers the user did not ask to hide.
func TestStatsCmdFamilyFiltersOnlyTheUsageTable(t *testing.T) {
	a, tmp := newTestApp(t)
	seedSurveyEvents(t, tmp, []survey.Event{
		{Agent: "claude", ModelID: "ollama/a", Timestamp: time.Now().Add(-time.Hour), Worked: boolPtr(true)},
		{Agent: "claude", ModelID: "openrouter/b", Timestamp: time.Now().Add(-time.Hour), Worked: boolPtr(true)},
	})
	seedLaunches(t, tmp, launch{"ollama/a", "claude", time.Hour}, launch{"openrouter/b", "claude", time.Hour})
	stubSpend(t, spend.Result{}, nil)

	split := func(args ...string) (surveyPart, usagePart string) {
		stdout, _ := runStats(t, a, args...)
		surveyPart, usagePart, ok := strings.Cut(stdout, "\n\n")
		if !ok {
			t.Fatalf("wt stats %v: no blank line between the tables:\n%s", args, stdout)
		}
		return surveyPart, usagePart
	}

	s, u := split("--family", "openrouter")
	if !strings.Contains(s, "ollama/a") || !strings.Contains(s, "openrouter/b") {
		t.Errorf("--family changed the survey table:\n%s", s)
	}
	if strings.Contains(u, "ollama/a") || !strings.Contains(u, "openrouter/b") {
		t.Errorf("--family openrouter usage table =\n%s\nwant only openrouter/b", u)
	}

	s, u = split("--model", "ollama/a")
	if strings.Contains(s, "openrouter/b") || strings.Contains(u, "openrouter/b") {
		t.Errorf("--model ollama/a left openrouter/b in a table:\n%s\n\n%s", s, u)
	}

	_, u = split("--family", "nope")
	if u != "no usage data\n" {
		t.Errorf("--family nope usage part = %q, want \"no usage data\"", u)
	}
}

// TestStatsCmdWorksWithoutARegistry verifies the report needs no registry,
// in the three ways a machine really has none: registry.toml missing,
// unparseable, or a dangling symlink. newApp then carries an empty model
// list and a load error, and `wt stats` still prints launches and spend
// with each family taken from the id's provider prefix — so --family ollama
// still selects, and one note says prefixes are what it matched (without
// it, `--family gemma4` would print "no usage data" and no hint why).
// modelman's report exited 1 on each of these; a report about the past
// must not depend on today's catalog.
func TestStatsCmdWorksWithoutARegistry(t *testing.T) {
	breakRegistry := map[string]func(path string) error{
		"missing": os.Remove,
		"unparseable": func(path string) error {
			return os.WriteFile(path, []byte("models = [unclosed\n"), 0o644)
		},
		"dangling symlink": func(path string) error {
			if err := os.Remove(path); err != nil {
				return err
			}
			return os.Symlink(filepath.Join(filepath.Dir(path), "nowhere.toml"), path)
		},
	}
	for name, breakIt := range breakRegistry {
		t.Run(name, func(t *testing.T) {
			_, tmp := newTestApp(t)
			if err := breakIt(filepath.Join(tmp, "local-ai", "registry.toml")); err != nil {
				t.Fatal(err)
			}
			// The app as production builds it for this machine.
			a, err := newApp()
			if err != nil || a.loadErr == nil || a.cfg == nil || len(a.cfg.Models) != 0 {
				t.Fatalf("newApp() = cfg %+v, loadErr %v, err %v; want an empty config, a load error and no fatal error", a.cfg, a.loadErr, err)
			}
			seedLaunches(t, tmp, launch{"ollama/gone:cloud", "claude", time.Hour}, launch{"openrouter/x", "claude", time.Hour})
			stubSpend(t, spend.Result{Rows: []spend.Row{{Model: "ollama/gone:cloud", Requests: 3}}}, nil)

			stdout, stderr := runStats(t, a, "--family", "ollama")
			want := "no survey data\n\n" +
				"MODEL              LAUNCHES  REQUESTS  PROMPT  COMPLETION    SPEND\n" +
				"ollama/gone:cloud         1         3       0           0  $0.0000\n"
			if stdout != want {
				t.Errorf("stdout =\n%s\nwant\n%s", stdout, want)
			}
			if stderr != "wt: "+registryNote+"\n" {
				t.Errorf("stderr = %q, want the one note that --family matched prefixes", stderr)
			}

			// Without --family nothing depends on the registry: both rows, no note.
			stdout, stderr = runStats(t, a)
			if !strings.Contains(stdout, "ollama/gone:cloud") || !strings.Contains(stdout, "openrouter/x") || stderr != "" {
				t.Errorf("without --family: stdout =\n%s\nstderr = %q\nwant both rows and no note", stdout, stderr)
			}
		})
	}
}

// TestStatsFamilyShadowsTheRootFlag pins which --family `wt stats` has: its
// own, one exact value, with no -F shorthand — the way its --model already
// shadows the root's -M/--model. The root's persistent -F/--family is a
// comma list for the picker; inherited, `--family a,b` would mean two
// different things on one command. So -F is rejected on stats, before or
// after the word, and a comma is part of the family's name.
func TestStatsFamilyShadowsTheRootFlag(t *testing.T) {
	_, tmp := newTestApp(t) // rootCmd builds its own app from this throwaway home
	seedLaunches(t, tmp, launch{"ollama/a", "claude", time.Hour}, launch{"omlx/b", "claude", time.Hour})
	run := func(args ...string) (string, error) {
		root := rootCmd()
		var out, errOut bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&errOut)
		root.SetArgs(args)
		err := root.Execute()
		return out.String(), err
	}
	for _, args := range [][]string{{"stats", "-F", "ollama"}, {"-F", "ollama", "stats"}} {
		if _, err := run(args...); err == nil || !strings.Contains(err.Error(), "unknown shorthand flag: 'F'") {
			t.Errorf("wt %v: err = %v, want -F rejected on stats", args, err)
		}
	}
	if out, err := run("stats", "--family", "ollama"); err != nil || !strings.Contains(out, "ollama/a") || strings.Contains(out, "omlx/b") {
		t.Errorf("wt stats --family ollama = %q, %v; want only ollama/a", out, err)
	}
	if out, err := run("stats", "--family", "ollama,omlx"); err != nil || !strings.HasSuffix(out, "no usage data\n") {
		t.Errorf("wt stats --family ollama,omlx = %q, %v; want no usage data (one family, not a list)", out, err)
	}
}

// TestStatsCmdUsesTheTerminalWidth verifies the command hands the usage
// table the terminal's width: at 80 columns a 51-character id moves to its
// own line and no line of the usage table is wider than the terminal.
// Without this the width-aware table is only ever tested, never used. Only
// the usage table is measured: the survey table above it keeps its bordered
// renderer, whose width this step does not change.
func TestStatsCmdUsesTheTerminalWidth(t *testing.T) {
	a, tmp := newTestApp(t)
	const long = "mtplx/Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality"
	seedLaunches(t, tmp, launch{long, "claude", time.Hour})
	stubSpend(t, spend.Result{Rows: []spend.Row{{Model: long, Requests: 1204, PromptTokens: 12345678, CompletionTokens: 1234567}}}, nil)
	old := stdoutWidth
	stdoutWidth = func() int { return 80 }
	t.Cleanup(func() { stdoutWidth = old })

	stdout, _ := runStats(t, a)
	_, usagePart, _ := strings.Cut(stdout, "\n\n")
	if !strings.HasPrefix(usagePart, "MODEL") || !strings.Contains(usagePart, "\n"+long+"\n") {
		t.Fatalf("usage table =\n%s\nwant the long id on a line of its own at 80 columns", usagePart)
	}
	for _, line := range strings.Split(usagePart, "\n") {
		if w := lipgloss.Width(line); w > 80 {
			t.Errorf("usage table line is %d columns wide at an 80-column terminal: %q", w, line)
		}
	}
}

// TestStatsNeverReachesADatabaseByDefault pins both guards between a
// cmd/wt test and the developer's LiteLLM database. First, TestMain's
// querySpend stub: an unstubbed `wt stats` reports "not stubbed" instead of
// querying. Second, the real seam itself under this package's throwaway
// config home: the registry is redirected and nothing names config.yaml, so
// litellm.DatabaseURL refuses before it reads config.yaml or the proxy's
// LaunchAgent plist, and before psql is looked for. PATH is emptied and the
// plist path pointed at nothing for that half so that, if the guard ever
// regresses, the worst this test can do is fail — it still cannot read the
// real plist or run psql.
func TestStatsNeverReachesADatabaseByDefault(t *testing.T) {
	a, _ := newTestApp(t)
	_, stderr := runStats(t, a)
	if stderr != "wt: spend unavailable: querySpend not stubbed in this test\n" {
		t.Errorf("stderr = %q, want TestMain's stub to have answered", stderr)
	}

	t.Setenv("PATH", t.TempDir())
	t.Setenv("WT_LITELLM_PLIST", filepath.Join(t.TempDir(), "no-such.plist"))
	for _, k := range []string{"WT_LITELLM_DATABASE_URL", "MODELMAN_LITELLM_DATABASE_URL", "WT_LITELLM_CONFIG", "MODELMAN_LITELLM_CONFIG", "DATABASE_URL"} {
		t.Setenv(k, "")
	}
	_, err := realQuerySpend(context.Background(), time.Now().Add(-time.Hour), time.Now())
	if !errors.Is(err, litellm.ErrNoDatabase) {
		t.Fatalf("realQuerySpend err = %v, want litellm.ErrNoDatabase (redirected registry, config.yaml unnamed)", err)
	}
}
```

- [ ] **Step 2: Add the default stubs to `TestMain`**

In `wt/cmd/wt/testmain_test.go`, replace the tail of the import block:

```go
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/survey"
)
```

with:

```go
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/spend"
	"github.com/ohanaverse/local-ai-setup/wt/internal/survey"
)
```

and replace (lines 64-65):

```go
	confirmStop = func(string) (bool, error) { return false, nil }
	code := m.Run()
```

with:

```go
	confirmStop = func(string) (bool, error) { return false, nil }
	// `wt stats` seams: no test may resolve the developer's LiteLLM database
	// or run psql against it. Unstubbed, a stats test sees "spend
	// unavailable" — the degraded report — and never a query. Tests that
	// want spend rows call stubSpend. The width is pinned to "not a
	// terminal" so a table does not depend on where `go test` was started.
	querySpend = func(context.Context, time.Time, time.Time) (spend.Result, error) {
		return spend.Result{}, errors.New("querySpend not stubbed in this test")
	}
	stdoutWidth = func() int { return 0 }
	code := m.Run()
```

- [ ] **Step 3: Update the two existing assertions**

Both tables now print, so stdout is no longer the single line these two tests expect. In `wt/cmd/wt/stats_test.go`, replace the comment above `TestStatsCmdEmptyStorePrintsMessage` (lines 44-46):

```go
// TestStatsCmdEmptyStorePrintsMessage verifies `wt stats` never errors on
// a fresh install (no survey.jsonl yet) — it reports "no survey data"
// instead.
```

with:

```go
// TestStatsCmdEmptyStorePrintsMessage verifies `wt stats` never errors on
// a fresh install (no survey.jsonl, no usage.jsonl, no spend) — it reports
// "no survey data" and "no usage data", one per table, instead.
```

In that test, replace (lines 56-58):

```go
	if strings.TrimSpace(out.String()) != "no survey data" {
		t.Fatalf("output = %q, want \"no survey data\"", out.String())
	}
```

with:

```go
	if out.String() != "no survey data\n\nno usage data\n" {
		t.Fatalf("output = %q, want \"no survey data\", a blank line, \"no usage data\"", out.String())
	}
```

In `TestStatsCmdWindowFilter`, replace (lines 107-109):

```go
	if strings.TrimSpace(out.String()) != "no survey data" {
		t.Errorf("output = %q, want \"no survey data\" (10-day-old event excluded from 1d window)", out.String())
	}
```

with:

```go
	if !strings.HasPrefix(out.String(), "no survey data\n") {
		t.Errorf("output = %q, want \"no survey data\" first (10-day-old event excluded from 1d window)", out.String())
	}
```

Nothing else in `stats_test.go` changes: the other tests use `strings.Contains` on survey rows, which still print.

- [ ] **Step 4: Run the tests to see them fail**

Run from `wt/`: `go test -count=1 ./cmd/wt -run 'TestStats|TestCollectUsage'`

Expected: the build fails with `undefined: querySpend`, `undefined: statsNow`, `undefined: spendOK` (and more of the same).

- [ ] **Step 5: Add the report and the seams to `stats_usage.go`**

In `wt/cmd/wt/stats_usage.go`, replace the import block with:

```go
import (
	"context"
	"errors"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/spend"
	"github.com/ohanaverse/local-ai-setup/wt/internal/survey"
	"github.com/ohanaverse/local-ai-setup/wt/internal/usage"
)
```

and append to the end of the file:

```go
// The four answers to "is there spend data in this report".
const (
	spendOK            = "ok"
	spendNotConfigured = "not_configured" // nothing names a database
	spendUnavailable   = "unavailable"    // psql missing, database down, query failed
	spendSkipped       = "skipped"        // --agent: the spend log has no agent
)

// usageReport is everything the usage table and its notes are built from.
type usageReport struct {
	Rows         []usageRow
	SpendStatus  string
	SpendReason  string // why spend is missing; "" when SpendStatus is spendOK
	Unattributed int64  // requests the proxy logged with no model, in the whole window
	Narrowed     bool   // --model or --family was given: Rows is part of the window
}

// registryNote is the extra note for --family when the registry did not
// load: every family is then an id's provider prefix, and a registry family
// (gemma4) matches nothing.
const registryNote = "the registry did not load (run `wt config` to repair), so --family matched each id's provider prefix"

// querySpend asks the LiteLLM database for per-model totals between start
// and end. A seam: cmd/wt's TestMain replaces it, so no test resolves a
// connection string or runs psql.
var querySpend = realQuerySpend

func realQuerySpend(ctx context.Context, start, end time.Time) (spend.Result, error) {
	dsn, err := litellm.DatabaseURL()
	if err != nil {
		return spend.Result{}, err
	}
	return spend.Query(ctx, dsn, start, end)
}

// stdoutWidth is the terminal's width in columns, or 0 when stdout is not
// a terminal (a pipe or a file has no width to fit). A seam so tests do not
// depend on where `go test` was run.
var stdoutWidth = realStdoutWidth

func realStdoutWidth() int {
	w, _, err := term.GetSize(os.Stdout.Fd())
	if err != nil {
		return 0
	}
	return w
}

// collectUsage builds the usage report for one window ending at asOf.
// It never fails: every way of having no spend data is a status and a
// reason, and the launch counts are reported regardless.
func collectUsage(ctx context.Context, cfg *config.Config, window time.Duration, asOf time.Time, modelFilter, familyFilter, agentFilter string) usageReport {
	rep := usageReport{SpendStatus: spendOK, Narrowed: modelFilter != "" || familyFilter != ""}
	var sp *spend.Result
	if agentFilter != "" {
		rep.SpendStatus = spendSkipped
		rep.SpendReason = "--agent narrows launches only; LiteLLM does not log which agent sent a request, so spend is not shown"
	} else if res, err := querySpend(ctx, asOf.Add(-window), asOf); err != nil {
		rep.SpendStatus = spendUnavailable
		if errors.Is(err, litellm.ErrNoDatabase) {
			rep.SpendStatus = spendNotConfigured
		}
		rep.SpendReason = "spend unavailable: " + err.Error()
	} else {
		sp = &res
		rep.Unattributed = res.Unattributed
	}
	rep.Rows = buildUsageRows(usage.NewStore().AllCounts(agentFilter), window, sp, registryFamilies(cfg), modelFilter, familyFilter)
	return rep
}

// notes are the lines `wt stats` writes to stderr after the tables: at
// most one about missing spend, and one counting requests with no model.
// The count is the whole window's — the query is never filtered — so it is
// left out when --model or --family narrowed the rows, where it would read
// as a fact about the model shown.
func (r usageReport) notes() []string {
	var out []string
	if r.SpendReason != "" {
		out = append(out, r.SpendReason)
	}
	switch {
	case r.Narrowed:
	case r.Unattributed == 1:
		out = append(out, "1 request had no model and is not shown")
	case r.Unattributed > 1:
		out = append(out, formatCount(r.Unattributed)+" requests had no model and are not shown")
	}
	return out
}

// registryFamilies maps each registry model id to its family. cfg is never
// nil (newApp substitutes an empty config when the registry does not load),
// and an empty model list simply yields no families: familyFor then falls
// back to each id's prefix.
func registryFamilies(cfg *config.Config) map[string]string {
	out := map[string]string{}
	for _, m := range cfg.Models {
		out[m.ID] = m.Family
	}
	return out
}
```

- [ ] **Step 6: Rewire `statsCmd`**

In `wt/cmd/wt/stats.go`, replace the file comment (lines 1-4, everything above `package main`) with:

```go
// wt stats — a read-only report: the survey table over survey.jsonl (wt
// collects, wt reports), then the usage table (stats_usage.go) joining
// usage.jsonl's launches with the LiteLLM proxy's spend log. Model ids are
// read straight from the events and the spend rows, so both tables work
// for a model that has since been removed from registry.toml; the registry
// is consulted only for a model's family.
```

Then replace everything from the comment `// statsCmd returns the` (line 29) down to, but not including, the comment `// parseStatsWindow maps a --window flag value to its duration.` (line 90) with:

```go
// statsCmd returns the `wt stats` command. It is a report, never a gate:
// an empty store, a missing psql and an unreachable database all exit 0.
// Only a malformed --window value is an error.
func statsCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stats",
		Short: "Report survey stats, launches and LiteLLM spend per model",
		Long: "Report two tables for one window.\n\n" +
			"The survey table: accumulated post-session survey stats (worked%, quality,\n" +
			"speed) per model and per agent×model combo.\n\n" +
			"The usage table: per model, wt launches from usage.jsonl beside the\n" +
			"requests, prompt and completion tokens and spend the LiteLLM proxy logged.\n" +
			"Spend is read with psql from the proxy's database, found the way the proxy\n" +
			"finds it: WT_LITELLM_DATABASE_URL, else general_settings.database_url in\n" +
			"LiteLLM's config.yaml, else DATABASE_URL — the last two looked up in wt's\n" +
			"environment and then in the proxy's LaunchAgent plist. Without psql, a\n" +
			"reachable database or a configured URL, the launches still print, the\n" +
			"spend cells show \"-\", and one note on stderr says why.",
		RunE: func(cmd *cobra.Command, args []string) error {
			window, err := parseStatsWindow(mustGetString(cmd, "window"))
			if err != nil {
				return err
			}
			modelFilter := mustGetString(cmd, "model")
			agentFilter := mustGetString(cmd, "agent")
			familyFilter := mustGetString(cmd, "family")

			asOf := statsNow()
			rows := buildStatsRows(survey.NewStore().Events(), window, asOf, modelFilter, agentFilter)
			rep := collectUsage(cmd.Context(), a.cfg, window, asOf, modelFilter, familyFilter, agentFilter)

			out := cmd.OutOrStdout()
			if table := surveyTableRows(rows); len(table) == 0 {
				fmt.Fprintln(out, "no survey data")
			} else {
				fmt.Fprintln(out, renderTable(surveyHeaders, table, a.theme))
			}
			fmt.Fprintln(out)
			if len(rep.Rows) == 0 {
				fmt.Fprintln(out, "no usage data")
			} else {
				fmt.Fprintln(out, renderUsageTable(rep.Rows, stdoutWidth()))
			}
			notes := rep.notes()
			if familyFilter != "" && a.loadErr != nil {
				notes = append(notes, registryNote)
			}
			for _, note := range notes {
				fmt.Fprintf(cmd.ErrOrStderr(), "wt: %s\n", note)
			}
			return nil
		},
	}
	cmd.Flags().String("window", "30d", "Window for both tables: 1d, 7d, or 30d")
	cmd.Flags().String("model", "", "Filter both tables to one model id")
	cmd.Flags().String("agent", "", "Filter to one agent (survey and launches; spend is then not shown)")
	// Local, so it shadows the root's persistent -F/--family (a comma list
	// for the picker) on this command, as --model above shadows -M/--model.
	cmd.Flags().String("family", "", "Filter the usage table to one model family")
	return cmd
}

// statsNow is the report's "as of" instant. A seam so a test can pin the
// as_of a --json document carries.
var statsNow = func() time.Time { return time.Now().UTC() }

var surveyHeaders = []string{"MODEL", "AGENT", "WORKED%", "QUALITY", "SPEED", "N", "SKIPPED"}

// surveyTableRows renders the survey rows as table cells.
func surveyTableRows(rows []statsRow) [][]string {
	tableRows := make([][]string, 0, len(rows))
	for _, r := range rows {
		quality, qok := r.Stats.QualityAvg()
		speed, sok := r.Stats.SpeedAvg()

		// Exclude rows with no data: rows where both Answered == 0 and
		// Skipped == 0 have no survey information at all. Rows with
		// Answered == 0 but Skipped > 0 ("all skipped") are kept to show
		// that the agent×model combo was tried but never produced data.
		// buildStatsRows applies the same rule.
		if r.Stats.Answered == 0 && r.Stats.Skipped == 0 {
			continue
		}

		tableRows = append(tableRows, []string{
			r.ModelID,
			r.Agent,
			formatPctCell(r.Stats),
			formatAvgCell(quality, qok),
			formatAvgCell(speed, sok),
			strconv.Itoa(r.Stats.Answered),
			strconv.Itoa(r.Stats.Skipped),
		})
	}
	return tableRows
}
```

The import block of `stats.go` does not change: `fmt`, `sort`, `strconv`, `time`, `survey` and `cobra` are all still used. `surveyTableRows` is the old loop body moved out unchanged (its comment no longer cites line numbers that have moved).

- [ ] **Step 7: Run the tests**

Run from `wt/`: `go test -count=1 ./cmd/wt -run 'TestStats|TestCollectUsage|TestBuildUsageRows|TestRenderUsageTable|TestFormatCount' -v 2>&1 | grep -c '^--- PASS'`

Expected: `27` — the 7 tests already in `stats_test.go`, 4 from Task 4, 6 from Task 5, and the 10 new ones here (`TestStatsCmdPrintsTheUsageTable`, `TestStatsCmdDegradesWhenSpendIsUnavailable`, `TestCollectUsageClassifiesMissingSpend`, `TestStatsCmdNotesRequestsWithNoModel`, `TestStatsCmdAgentFilterSkipsSpend`, `TestStatsCmdFamilyFiltersOnlyTheUsageTable`, `TestStatsCmdWorksWithoutARegistry`, `TestStatsFamilyShadowsTheRootFlag`, `TestStatsCmdUsesTheTerminalWidth`, `TestStatsNeverReachesADatabaseByDefault`).

The run prints `wt: spend unavailable: querySpend not stubbed in this test` on stderr for the older tests. That is the `TestMain` stub answering, and it is what those tests should see.

- [ ] **Step 8: See it work, with no database**

Build and run the command in a throwaway home, against an address nothing listens on. This contacts no database and touches nothing under `~/.config`:

```bash
go build -o /tmp/wt-verify ./cmd/wt
H=$(mktemp -d)
mkdir -p "$H/xdg/agent-wt" "$H/xdg/local-ai"
printf 'default_tag = "code"\n' > "$H/xdg/agent-wt/config.toml"
printf 'providers = []\nmodels = []\n' > "$H/xdg/local-ai/registry.toml"
printf '{"model_id":"ollama/gemma4:9b","agent":"claude","timestamp":"%s"}\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$H/xdg/agent-wt/usage.jsonl"
env -i HOME="$H" XDG_CONFIG_HOME="$H/xdg" PATH="$PATH" \
  WT_LITELLM_DATABASE_URL=postgresql://127.0.0.1:1/x /tmp/wt-verify stats; echo "exit=$?"
```

Expected (the first line appears on the first run only, when wt migrates the fresh `config.toml`):

```
wt: migrated config to native-provider alignment (renamed google→agy, rewired opencode to ollama-only)
no survey data

MODEL             LAUNCHES  REQUESTS  PROMPT  COMPLETION  SPEND
ollama/gemma4:9b         1         -       -           -      -
wt: spend unavailable: cannot reach the LiteLLM database at 127.0.0.1:1: Connection refused
exit=0
```

Then without the variable, which exercises the redirected-registry guard:

```bash
env -i HOME="$H" XDG_CONFIG_HOME="$H/xdg" PATH="$PATH" /tmp/wt-verify stats 2>&1 | tail -1
```

Expected: `wt: spend unavailable: no LiteLLM database configured: the registry is redirected to <H>/xdg/local-ai/registry.toml and nothing names config.yaml (set WT_LITELLM_DATABASE_URL, or WT_LITELLM_CONFIG)`.

Then the automatic resolution, still inside the throwaway home: a `config.yaml` that names a variable, and a LaunchAgent plist — both temp files, named by `WT_LITELLM_CONFIG` and `WT_LITELLM_PLIST` — that sets it to the same dead address. Nothing is exported to `wt`:

```bash
printf 'general_settings:\n  database_url: os.environ/DATABASE_URL\n' > "$H/config.yaml"
cat > "$H/proxy.plist" <<'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
<key>EnvironmentVariables</key>
<dict><key>DATABASE_URL</key><string>postgresql://127.0.0.1:1/x</string></dict>
</dict></plist>
EOF
env -i HOME="$H" XDG_CONFIG_HOME="$H/xdg" PATH="$PATH" \
  WT_LITELLM_CONFIG="$H/config.yaml" WT_LITELLM_PLIST="$H/proxy.plist" /tmp/wt-verify stats 2>&1 | tail -1
printf '<plist version="1.0"><dict></dict></plist>\n' > "$H/proxy.plist"
env -i HOME="$H" XDG_CONFIG_HOME="$H/xdg" PATH="$PATH" \
  WT_LITELLM_CONFIG="$H/config.yaml" WT_LITELLM_PLIST="$H/proxy.plist" /tmp/wt-verify stats 2>&1 | tail -1
rm -rf "$H" /tmp/wt-verify
```

Expected, first (the plist supplied the string, so `psql` was run against the dead address):

```
wt: spend unavailable: cannot reach the LiteLLM database at 127.0.0.1:1: Connection refused
```

Expected, second (the plist no longer sets it; `<H>` is the temp directory):

```
wt: spend unavailable: no LiteLLM database configured: general_settings.database_url in <H>/config.yaml is os.environ/DATABASE_URL, and DATABASE_URL is not set in wt's environment or the proxy LaunchAgent's EnvironmentVariables (<H>/proxy.plist) (set it, or WT_LITELLM_DATABASE_URL)
```

- [ ] **Step 9: Verify the slice so far, and commit**

Run from `wt/`:

```bash
test -z "$(gofmt -l .)" && go vet ./... && go test -count=1 ./... && make check
```

Expected: 21 lines starting with `ok`; `make check` passes.

Then commit, still from `wt/` (the `git add` paths are relative to it):

```bash
git add cmd/wt/stats.go cmd/wt/stats_usage.go cmd/wt/stats_usage_test.go cmd/wt/stats_test.go cmd/wt/testmain_test.go
git commit -m "feat(wt): wt stats prints launches and LiteLLM spend per model"
```

### Task 7: document the usage table

Docs only. The `modelman usage report` text in guide 07 stays: modelman still works, and Step 6 rewrites that guide. (Why the guides are touched in this step at all is in the decisions table.)

One file per step. In Steps 3 to 7 each edit replaces one exact piece of text, and every "replace" string occurs exactly once in its file.

**Files:**
- Modify: `wt/docs/wt-stats.md`, `wt/CLAUDE.md`, `wt/docs/internals/testing.md`, `wt/CHANGELOG.md`, `docs/guides/07-usage-and-spend.md`, `docs/guides/00-config-map.md`

**Interfaces:**
- Consumes: the behaviour of Tasks 1-6.
- Produces: nothing code depends on. Task 9 edits `wt-stats.md`, `wt/CLAUDE.md`, `wt/CHANGELOG.md` and guide 07 again, anchored on text this task writes.

- [ ] **Step 1: Rewrite the top of `wt/docs/wt-stats.md`**

Replace everything from line 1 through the paragraph that ends "`wt stats` is a report, never a gate." (lines 1-28) with:

````markdown
# `wt stats`

> The post-session survey is switched off for now (#136), so no new answers
> are recorded. `wt stats` still reports the answers already collected; rows
> drop out as they pass the 30-day window, and the survey table then reads
> `no survey data`. The usage table below it is unaffected.

Reports two tables for one window:

- the **survey table** — did an agent×model combo work, how fast, how good.
  wt collects this through the post-session survey prompt (see
  [wt-agents/README.md#post-run-summary-line](./wt-agents/README.md#post-run-summary-line));
- the **usage table** — per model, how often wt launched it, and what the
  LiteLLM proxy logged for it: requests, prompt and completion tokens, spend.

```bash
wt stats [--window 1d|7d|30d] [--model <id>] [--agent <name>] [--family <family>]
```

- `--window` — defaults to `30d`. One window for both tables: survey
  answers, launches and spend all cover the same period.
- `--model` — narrow both tables to one model id (exact match).
- `--agent` — narrow the survey table and the launch counts to one agent.
  The spend log does not record which agent sent a request, so spend is not
  shown with `--agent`; one note on stderr says so.
- `--family` — narrow the usage table to one model family: one exact value,
  not the comma list `wt -F` takes, and with no `-F` shorthand on `stats`.
  The survey table ignores it.

`wt stats` is a report, never a gate: it exits 0 with empty stores, without
`psql`, with the database down, and with a `registry.toml` it cannot read.
Only an invalid flag is an error.

## Survey table

One row per model's `(all)`-agents aggregate, plus one row per observed
(agent, model) combo — sorted by agent name with `(all)` first, then by
model id. A combo with no real data (zero answered surveys, and no
calculated averages) is omitted — this includes rows where all surveys
were skipped. An empty store (or a filter matching nothing) prints
`no survey data`.
````

Keep, unchanged, what follows: the fenced `$ wt stats` example and the paragraph starting "`WORKED%`, `QUALITY`, and `SPEED` render `-`" (lines 30-48).

- [ ] **Step 2: Add the usage sections to `wt/docs/wt-stats.md`**

Directly after that paragraph, and before the heading `## Where the data comes from`, insert:

````markdown
## Usage table

One row per model that has a launch in the window or a request in the
proxy's spend log, sorted by model id. Example (illustrative values):

```
MODEL                        LAUNCHES  REQUESTS     PROMPT  COMPLETION    SPEND
ollama/gemma4:9b                    2     1,204  1,234,567         630  $0.0000
openrouter/qwen/qwen3.8-27b         0         5        517       3,135  $0.0085
```

| Column | Source |
|---|---|
| `LAUNCHES` | wt launches of the model in the window, from `usage.jsonl` (the same counts the picker's 1d/7d/30d columns show) |
| `REQUESTS` | rows the LiteLLM proxy logged for the model in the window |
| `PROMPT`, `COMPLETION` | summed prompt and completion tokens of those rows |
| `SPEND` | summed cost of those rows, in dollars |

How to read a row:

- **Launches and requests both above zero** — the model's agent traffic went
  through the proxy.
- **Launches, zero requests** — wt launched it and the proxy logged nothing:
  a native or direct launch (ollama, omlx, a subscription agent), which
  never reaches LiteLLM.
- **Zero launches, requests** — something used the proxy without a wt
  launch: `curl`, a script, another client.

A model that has left `registry.toml` keeps its row; the registry is read
only for `--family`. A model's family is the registry's, else the id's
provider prefix (the part before the first `/`), else `unknown`. When
`registry.toml` is missing or cannot be read, every family is the prefix,
and `--family` adds a note on stderr saying so.

Requests the proxy logged with no model (failed requests, mostly) are not a
row. They are counted in a note on stderr: `wt: 12 requests had no model and
are not shown`. The count covers the whole window, so the note is left out
under `--model` and `--family`.

**Width.** The table has no borders. On a terminal, a model id too long for
the `MODEL` column runs into the blank space before its `LAUNCHES` value,
and one too long even for that is printed on a line of its own with its
numbers on the next line. An id is never shortened. When stdout is not a
terminal (`wt stats | grep qwen`, `> file`), every model is one line.
Control characters in an id (the proxy logs whatever name a client was
routed by) are shown escaped, as `\n` or `\x1b`.

Nothing is printed for an empty table but `no usage data`.

## Where spend comes from

wt runs one aggregated query against the proxy's Postgres table
`"LiteLLM_SpendLogs"` with `psql`, which must be on `PATH`. It finds the
database the way the proxy does, so there is normally nothing to export.
The connection string is the first of:

1. `WT_LITELLM_DATABASE_URL`
2. `MODELMAN_LITELLM_DATABASE_URL` (legacy alias)
3. `general_settings.database_url` in LiteLLM's `config.yaml`
   (`WT_LITELLM_CONFIG`, default `~/.config/litellm/config.yaml`)
4. `DATABASE_URL`, when `config.yaml` exists and names no `database_url` —
   LiteLLM's own fallback

A `config.yaml` value written `os.environ/NAME`, and `DATABASE_URL` in
step 4, are looked up in the environment `wt stats` runs in and then in the
proxy's: the `EnvironmentVariables` of its LaunchAgent plist
(`WT_LITELLM_PLIST`, default
`~/Library/LaunchAgents/local.litellm.proxy.plist`), which is where a
LaunchAgent install keeps the variable. A value in your shell wins over the
plist's. When the plist cannot be read, or `WT_LITELLM_RESTART_CMD` says
something other than that LaunchAgent starts the proxy, only wt's own
environment is consulted. A value that is empty or only whitespace counts
as unset at every step.

Neither `config.yaml` nor the plist is read when the registry is redirected
(`MODELMAN_REGISTRY`, `XDG_CONFIG_HOME`) and nothing names `config.yaml`:
set `WT_LITELLM_DATABASE_URL` or `WT_LITELLM_CONFIG` for a scratch setup.

The query waits at most 3 seconds for a connection and 10 seconds in all,
and never prompts for a password. The window is `--window` ending now, in
UTC. wt only reads; it writes nothing to the database.

The connection string is passed to `psql` as an argument, so for the second
or so the query runs it is visible to other users of the machine in `ps`.
Keep the password out of it (a `~/.pgpass` entry or `PGPASSWORD` works with
`psql` as usual) on a shared machine.

When the database cannot be reached, the note names the host and port wt
tried — `cannot reach the LiteLLM database at 127.0.0.1:5432: Connection
refused` — so a wrong address shows. wt prints no other part of the
connection string, in a note or in `--json`: not the user, the password or
the database name. When the string is written so that `psql` may have read
part of the password as the host or port (an unencoded `/` or `@` in the
password), the address is left out as well.

## When spend is missing

Launch counts never depend on the database. When spend cannot be read, the
four spend cells show `-`, and one note on stderr says why:

```
MODEL             LAUNCHES  REQUESTS  PROMPT  COMPLETION  SPEND
ollama/gemma4:9b         1         -       -           -      -
wt: spend unavailable: psql not found on PATH
```

| Note | Meaning |
|---|---|
| `spend unavailable: psql not found on PATH` | Install a Postgres client that puts `psql` on `PATH` |
| `spend unavailable: cannot reach the LiteLLM database at <host>:<port>: …` | `psql` could not connect to that address (`at socket <path>` for a socket), or got no answer in 10 seconds (no address is shown for that). The reason follows: `Connection refused`, `password authentication failed for user "..."` (names are blanked), `the database host name did not resolve`. If the address is not the one you expect, "Where spend comes from" lists where it can come from. When `psql`'s message could quote the connection string — it does for one it cannot parse — wt says so instead of printing it, and shows no address; run `psql` yourself to see it |
| `spend unavailable: no LiteLLM database configured: …` | Nothing names a database; the rest says where wt looked — `config.yaml`, the variable it names, wt's environment and the proxy's plist — and which variable to set. It never quotes a value |
| `spend unavailable: the spend query failed: …` | Connected, but the query failed. The server's `ERROR:` line follows (for example the table does not exist: spend logging is not set up), or `psql`'s exit status |
| `spend unavailable: LiteLLM config is invalid: …` | `config.yaml` exists and cannot be parsed |
| `--agent narrows launches only; …` | `--agent` was given |
| `the registry did not load …` | `--family` was given and `registry.toml` could not be read, so only provider prefixes (`ollama`, `openrouter`) match |

A `-` means "not read". A `0` means the proxy logged nothing for that model.
````

Then rename the heading `## Where the data comes from` to `## Where the survey data comes from`. Its text does not change.

- [ ] **Step 3: Update `wt/CLAUDE.md`**

Seven replacements.

In `wt/CLAUDE.md`, replace:

````text
| `cmd/wt/stats.go` | `wt stats` — read-only report over `survey.jsonl` |
````

with:

````text
| `cmd/wt/stats.go` | `wt stats` — read-only report: the survey table over `survey.jsonl`, then the usage table |
| `cmd/wt/stats_usage.go` | `wt stats`' usage half: `collectUsage` (launches from `usage.jsonl` joined with LiteLLM spend), `buildUsageRows`, the `querySpend` and `stdoutWidth` seams |
| `cmd/wt/stats_usage_table.go` | `renderUsageTable` — the borderless, width-aware usage table (an id is never truncated) |
````

In `wt/CLAUDE.md`, replace:

````text
`CountsForAgent` per agent×model, legacy agent-less lines count toward `Counts` only |
````

with:

````text
`CountsForAgent` per agent×model, legacy agent-less lines count toward `Counts` only; `AllCounts(agent)` enumerates every model in the file (for `wt stats`) |
````

In `wt/CLAUDE.md`, replace:

````text
`restart.go` (`RestartContext`, `Listening`, `WaitReady`) |
````

with:

````text
`restart.go` (`RestartContext`, `Listening`, `WaitReady`), `dburl.go` (`DatabaseURL` — the spend database's connection string; reads, never writes) |
| `internal/spend/` | per-model request, token and cost totals from the proxy's `"LiteLLM_SpendLogs"` table: one aggregated query through `psql` (`Query`), typed failures (`ErrNoPsql`, `ErrUnreachable`, `ErrQuery`), no Postgres driver |
````

In `wt/CLAUDE.md`, replace:

````text
lifecycle,litellm,smoke}`.
````

with:

````text
lifecycle,litellm,spend,smoke}`.
````

In `wt/CLAUDE.md`, replace:

````text
wt stats                             # survey report
````

with:

````text
wt stats [--window 7d] [--family F]  # survey table, then launches and LiteLLM spend per model
````

In `wt/CLAUDE.md`, replace:

````text
stub the inventory probe, hard-fail model starts, and no-op the route check. A new package
````

with:

````text
stub the inventory probe, hard-fail model starts, and no-op the route check. `cmd/wt`'s also replaces `querySpend`, and `internal/spend`'s fails its `lookPath`/`runPsql` seams, so no test runs `psql` or reaches the LiteLLM database. A new package
````

In `wt/CLAUDE.md`, replace:

````text
A redirected registry without `WT_LITELLM_CONFIG` is refused (`litellm.ErrRegistryRedirected`).
````

with:

````text
A redirected registry without `WT_LITELLM_CONFIG` is refused (`litellm.ErrRegistryRedirected`).
- The spend database is `litellm.DatabaseURL()`: `WT_LITELLM_DATABASE_URL`, legacy `MODELMAN_LITELLM_DATABASE_URL`, then `general_settings.database_url` in `config.yaml`. An `os.environ/NAME` value — and `DATABASE_URL`, when `config.yaml` names no `database_url` — is looked up in wt's own environment and then in the proxy's (`ProxyEnv.Lookup` over `LoadProxyEnv`, the LaunchAgent plist); wt's own wins, and a blank value is unset everywhere. Read-only, used only by `wt stats`; under a redirected registry with `config.yaml` unnamed it answers `ErrNoDatabase` rather than read the default file or the plist. Its errors name variables and files, never a value; a `spend.ErrUnreachable` names the host and port `psql` tried and nothing else from the connection string.
````

- [ ] **Step 4: Update `wt/docs/internals/testing.md`**

Three replacements.

In `wt/docs/internals/testing.md`, replace:

````text
`profileApplier`, `loadProxyEnv`)
````

with:

````text
`profileApplier`, `loadProxyEnv`, `querySpend`, `stdoutWidth`, `statsNow`)
````

In `wt/docs/internals/testing.md`, replace:

````text
Three `TestMain`s guarantee no Go test probes a real server, starts a real model, or drains the developer's terminal input queue:
````

with:

````text
Four `TestMain`s guarantee no Go test probes a real server, starts a real model, queries a real database, or drains the developer's terminal input queue:
````

In `wt/docs/internals/testing.md`, replace:

````text
- `internal/survey`: `flushTTY` is a no-op.
````

with:

````text
- `internal/survey`: `flushTTY` is a no-op.
- `internal/spend`: `lookPath` and `runPsql` both fail, so no test finds or runs the real `psql`. Tests use `stubPsql` (canned output and exit status) or `fakePsqlBinary` (a shell script run as a real child process, for the exec plumbing and the deadline). `cmd/wt`'s `TestMain` replaces `querySpend` one level up with a failure, so an unstubbed `wt stats` test gets the degraded report; `stubSpend(t, result, err)` supplies spend rows. `TestStatsNeverReachesADatabaseByDefault` pins both, and `litellm.DatabaseURL` adds a third guard: under the throwaway config home the registry is redirected, so it will not read the default `config.yaml` or the proxy's LaunchAgent plist. `internal/litellm`'s own `DatabaseURL` tests replace `loadProxyEnv` (`stubProxyEnv`) or name a temp plist (`writePlist`); none reads the real one.
````

- [ ] **Step 5: Add the `wt/CHANGELOG.md` entry**

In `wt/CHANGELOG.md`, replace:

````text
## Unreleased

### Changed
````

with:

````text
## Unreleased

### Added

- `wt stats` reports launches and LiteLLM spend per model. Below the survey
  table it prints a second table for the same `--window`: MODEL, LAUNCHES,
  REQUESTS, PROMPT, COMPLETION, SPEND. Launches come from `usage.jsonl`;
  the rest is one query against the proxy's `"LiteLLM_SpendLogs"` table
  through `psql`. The database is found the way the proxy finds it, with
  nothing to export: `WT_LITELLM_DATABASE_URL` (legacy alias
  `MODELMAN_LITELLM_DATABASE_URL`), else `general_settings.database_url` in
  LiteLLM's `config.yaml`, else `DATABASE_URL` — an `os.environ/NAME` value
  and `DATABASE_URL` are read from wt's environment and then from the
  proxy's LaunchAgent plist. Without `psql`, a reachable database or a
  configured URL, the launch counts still print, the spend cells show `-`,
  one note on stderr says why (an unreachable database is named by host and
  port, and by nothing else from the connection string), and the exit code
  stays 0. `--family` narrows
  the new table; `--agent` narrows its launch counts and leaves spend out.
  `--family` is `stats`' own flag and takes one exact family: the root
  command's `-F` shorthand, which `wt stats` used to accept and ignore, is
  now an error there. This replaces `modelman usage report`, which still works until modelman
  is removed. Not carried over: `--days N`, the Markdown output, the
  Reconciliation sections and the "Last wt launch" line.

### Changed
````

- [ ] **Step 6: Add the bullet to `docs/guides/00-config-map.md`**

In `docs/guides/00-config-map.md`, replace:

````text
wt refuses to write (or dry-run) routes until `WT_LITELLM_CONFIG` names the `config.yaml` that registry belongs to.
````

with:

````text
wt refuses to write (or dry-run) routes until `WT_LITELLM_CONFIG` names the `config.yaml` that registry belongs to.
- **Spend database:** `wt stats` reads spend from the database named by `WT_LITELLM_DATABASE_URL` (legacy alias `MODELMAN_LITELLM_DATABASE_URL`), else by `general_settings.database_url` in this file, else by `DATABASE_URL`. An `os.environ/NAME` value in this file, and `DATABASE_URL`, are read from wt's environment and then from the proxy's LaunchAgent plist, so the proxy's own setup is enough. It only reads. See [07-usage-and-spend](07-usage-and-spend.md).
````

- [ ] **Step 7: Add the `wt stats` section to `docs/guides/07-usage-and-spend.md`**

In `docs/guides/07-usage-and-spend.md`, replace:

````text
## Verification
````

with:

````text
## The same numbers from wt: `wt stats`

`wt stats` prints launches and LiteLLM spend per model without modelman: one
table, below its survey table, for one window (`--window 1d|7d|30d`, default
`30d`).

```bash
wt stats --window 7d
```

Example usage table (illustrative values — your models and numbers will
differ; the survey table above it is left out here):

```
MODEL                        LAUNCHES  REQUESTS     PROMPT  COMPLETION    SPEND
ollama/gemma4:9b                    2     1,204  1,234,567         630  $0.0000
openrouter/qwen/qwen3.8-27b         0         5        517       3,135  $0.0085
```

What differs from `modelman usage report`:

| `modelman usage report` | `wt stats` |
|---|---|
| `--days N`, default 7 | `--window 1d\|7d\|30d`, default `30d`; no other lengths |
| Three launch buckets (1d/7d/30d) beside a spend window of `--days` | One launch count and one spend total, both for `--window` |
| `## Reconciliation` lists | Read them off the table: launches with `0` requests bypassed the proxy; requests with `0` launches did not come from wt |
| `## Last wt launch` | Not shown (`cat ~/.config/agent-wt/rotation.state`) |
| Markdown on stdout | A plain table |
| Fails without `config.yaml` or a database | Prints the launch counts, `-` in the spend cells, one note on stderr, exit 0 |
| Exits 1 when `registry.toml` is missing or unreadable | Prints the report; a model's family is then its id's provider prefix |
| Requests with no model are dropped silently | Counted in a note on stderr |
| `--model`, `--family` | Same flags; `--agent` also narrows the launch counts (and leaves spend out) |
| Needs the connection string as a literal in `config.yaml`, or exported | Also resolves an `os.environ/NAME` value, and `DATABASE_URL`, from the proxy's LaunchAgent plist |

It needs `psql` on `PATH` and finds the database with nothing exported:
`WT_LITELLM_DATABASE_URL`, then `MODELMAN_LITELLM_DATABASE_URL`, then
`general_settings.database_url` in LiteLLM's `config.yaml`, then
`DATABASE_URL` — the last two looked up in your shell and then in the
proxy's LaunchAgent plist, as the proxy itself sees them. If the database
cannot be reached, the note names the host and port wt tried. Totals can
differ from `modelman usage report` at the edges of the window: `wt stats`
compares the proxy's timestamps as UTC. Full reference:
[wt/docs/wt-stats.md](../../wt/docs/wt-stats.md).

## Verification
````

- [ ] **Step 8: Check the links and the wording**

Run from the monorepo root: `make check-links`
Expected: no broken link reported (the new links are `../../wt/docs/wt-stats.md` from the guide and `07-usage-and-spend.md` from the config map).

Then confirm no dropped feature is described as a wt feature. Run from the monorepo root:

```bash
grep -n -- '--days\|Reconciliation\|Last wt launch\|Markdown' wt/docs/wt-stats.md wt/CLAUDE.md wt/docs/internals/testing.md
```

Expected: no output. (`wt/CHANGELOG.md` and guide 07 do name them, in the "Not carried over" sentence and the comparison table, which is where a reader should learn they are gone.)

- [ ] **Step 9: Commit**

Run from the monorepo root:

```bash
git add wt/docs/wt-stats.md wt/CLAUDE.md wt/docs/internals/testing.md wt/CHANGELOG.md docs/guides/07-usage-and-spend.md docs/guides/00-config-map.md
git commit -m "docs(wt): wt stats reports launches and LiteLLM spend"
```

### Task 8: Check against the real database, then hand PR 2 off (run by the controller)

Everything so far ran against stubs, a shell script and an address nothing listens on. Three things can only be learned from the owner's real proxy database:

1. that the statement runs against the real `"LiteLLM_SpendLogs"` table;
2. that `model_group` holds wt model ids for this proxy's traffic (rows with both launches and requests exist);
3. that the connection string resolves on this machine with nothing exported — from a literal in `config.yaml`, an `os.environ/NAME` reference, or `DATABASE_URL`, the last two through the proxy's LaunchAgent plist.

**The owner has said this may run against the real database (2026-10-07: "you may run against the real database"). The controller runs this task itself; it is never dispatched to a subagent.** Steps 2 and 3 read the owner's database, read-only. Steps 1, 4 and 5 touch no database. Never print, `echo`, `cat` or `grep` the connection string, `~/.config/litellm/config.yaml` or the LaunchAgent plist — not even to find out which of them supplied the string: `wt stats` prints none of them, and its notes say where it looked. Never export a connection string either: the point of the task is that none is needed.

**Files:** none.

**Interfaces:**
- Consumes: the branch as built.
- Produces: a report to the owner.

- [ ] **Step 1: Build the branch**

Run from `wt/`: `go build -o /tmp/wt-verify ./cmd/wt`

(`~/.local/bin/wt` is whatever was last installed and predates the branch.)

- [ ] **Step 2: Run the report with nothing exported**

Run in the real environment, with the three variables that could name a database unset for this one command, so that what is tested is the automatic lookup:

```bash
env -u WT_LITELLM_DATABASE_URL -u MODELMAN_LITELLM_DATABASE_URL -u DATABASE_URL \
  /tmp/wt-verify stats --window 7d
```

Check, and write down:

- The usage table appears under the survey table. (Run outside a terminal, as a controller's shell usually is, every model is one line whatever its length; the 80-column layout is Task 5's and Task 6's tests. In a terminal, no line of the usage table is wider than it.)
- **If there is no `spend unavailable` note:** the connection string resolved and the query ran (facts 1 and 3). At least one row should have both `LAUNCHES` and `REQUESTS` above zero (fact 2). Write down the count in any `requests had no model` note.
- **If the note is `spend unavailable: no LiteLLM database configured: …`:** the automatic lookup found nothing. The note holds only variable names and file paths, so copy it whole. It says which case this is: `config.yaml` does not exist; it has no `general_settings.database_url` and `DATABASE_URL` is not set; or it names `os.environ/NAME` and `NAME` is not set. "…is not set in wt's environment or the proxy LaunchAgent's EnvironmentVariables (<path>)" means the plist was read and does not set it; "…is not set in wt's environment" alone means the plist could not be read, or `WT_LITELLM_RESTART_CMD` is set. Stop and report the note to the owner. Do not open `config.yaml` or the plist to look further.
- **If the note is `spend unavailable: cannot reach the LiteLLM database at <host>:<port>: <reason>`:** the string resolved and `psql` could not connect there. Copy the note — the host and port are what the owner asked to see — and report it: either the database is down or the address is wrong.
- **If the note is `spend unavailable: cannot reach the LiteLLM database: …` with no address:** either `psql` gave no answer in 10 seconds, or the string is one wt will not quote an address from (`psql could not connect; its message is not shown …`, or a reason with no `at`). Report the note as it is. Do not run `psql` by hand with the string, and do not ask the owner to paste it.
- **If the note is `spend unavailable: the spend query failed: ERROR: …`:** copy the line (it is the server's message about the statement, and holds nothing from the connection string) and stop. The statement or a column name is wrong for this LiteLLM version; that is a bug in Task 3 to fix before handoff.

- [ ] **Step 3: Compare one model with the Python report**

Pick a model id from a row with requests. Run from the monorepo root:

```bash
uv run --directory modelman modelman usage report --days 7 --model '<id>'
env -u WT_LITELLM_DATABASE_URL -u MODELMAN_LITELLM_DATABASE_URL -u DATABASE_URL \
  /tmp/wt-verify stats --window 7d --model '<id>'
```

Expected: the same Requests, Prompt tokens, Completion tokens and Spend, or a difference of a few requests that fall within one UTC offset of either end of the 7-day window (wt compares the proxy's timestamps as UTC; Python passed zone-aware values). The launch count is modelman's middle (`7d`) number. A larger difference is a finding: report both outputs to the owner.

`modelman usage report` resolves less than `wt stats` does: it needs the connection string as a literal in `config.yaml` or in `MODELMAN_LITELLM_DATABASE_URL`. If it fails to connect where `wt stats` succeeded, that is the difference this step's plan change made, not a finding. Write down that the comparison could not be made and go on; do not export a connection string to make the Python report work.

- [ ] **Step 4: The two safe degraded runs**

These cannot reach a database on any machine: both run in a throwaway home, with a connection string that points at an address nothing listens on, and the first with a `PATH` that holds no `psql` at all. Do not run them in the real environment (`PATH=/usr/bin:/bin wt stats` is only safe where `psql` happens to live elsewhere).

```bash
H=$(mktemp -d)
mkdir -p "$H/xdg/agent-wt" "$H/xdg/local-ai" "$H/empty"
printf 'default_tag = "code"\n' > "$H/xdg/agent-wt/config.toml"
printf 'providers = []\nmodels = []\n' > "$H/xdg/local-ai/registry.toml"
printf '{"model_id":"ollama/gemma4:9b","agent":"claude","timestamp":"%s"}\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$H/xdg/agent-wt/usage.jsonl"
env -i HOME="$H" XDG_CONFIG_HOME="$H/xdg" PATH="$H/empty" \
  WT_LITELLM_DATABASE_URL=postgresql://127.0.0.1:1/x /tmp/wt-verify stats 2>&1 | tail -3
env -i HOME="$H" XDG_CONFIG_HOME="$H/xdg" PATH="$PATH" \
  WT_LITELLM_DATABASE_URL=postgresql://127.0.0.1:1/x /tmp/wt-verify stats 2>&1 | tail -3
rm -rf "$H"
```

Expected, first run (no `psql`):

```
MODEL             LAUNCHES  REQUESTS  PROMPT  COMPLETION  SPEND
ollama/gemma4:9b         1         -       -           -      -
wt: spend unavailable: psql not found on PATH
```

Expected, second run (nothing listening):

```
MODEL             LAUNCHES  REQUESTS  PROMPT  COMPLETION  SPEND
ollama/gemma4:9b         1         -       -           -      -
wt: spend unavailable: cannot reach the LiteLLM database at 127.0.0.1:1: Connection refused
```

- [ ] **Step 5: Clean up and hand off**

```bash
rm -f /tmp/wt-verify
```

Run from the monorepo root: `make test-all`
Expected: lint passes, the llmbench and modelman suites pass, and every wt `go test` line starts with `ok`.

Stop here. Tell the owner: what Steps 2 to 4 showed — whether the string resolved with nothing exported, and any note, copied whole — and what `make test-all` printed. Push and open the PR only after their OK. Suggested title: `feat(wt): wt stats reports launches and LiteLLM spend per model (retirement step 5, 2 of 3)`. The body lists what is not carried over from `modelman usage report` (`--days N`, Markdown, Reconciliation, "Last wt launch", the reverse-index fallback), says modelman's report is untouched until Step 6, and states the deliberate differences from the Python report (a launched model that has left the registry keeps its row; the connection string also resolves through the proxy's LaunchAgent plist).

---

## PR 3 — `--json`

Create the branch from the monorepo root, after PR 2 has merged:

```bash
git fetch origin
git switch -c feat/wt-stats-json origin/main
```

### Task 9: `wt stats --json`

wt's other `--json` flags print one compact line with `json.NewEncoder(out).Encode(doc)` from a typed struct (`litellm.go:48`, `:97`; `model_cmds.go:323`); this follows them. There is no `testdata/` directory in `cmd/wt`: expected documents are written inline in the test, as here. No contract fixture is added under `docs/contracts/`, because no other program parses this document.

The document is built from the same `[]statsRow` and `usageReport` the tables are rendered from, so the two cannot disagree.

**Files:**
- Create: `wt/cmd/wt/stats_json.go`
- Modify: `wt/cmd/wt/stats.go` (`statsCmd`'s `RunE` and flags)
- Modify: `wt/docs/wt-stats.md`, `wt/CLAUDE.md`, `wt/CHANGELOG.md`, `docs/guides/07-usage-and-spend.md`
- Test: `wt/cmd/wt/stats_json_test.go`

**Interfaces:**
- Consumes (Task 6): `usageReport`, `usageRow`, `statsRow` (`stats.go:23`), `statsAllAgents` (`stats.go:19`), `spendOK`, `statsNow`, and the test helpers `stubSpend`, `seedLaunches`, `launch`, `runStats`; `survey.Stats`' `WorkedPct()`, `QualityAvg()`, `SpeedAvg()`, each `(float64, bool)` (`internal/survey/stats.go:29-52`).
- Produces (package `main`):
  - `type statsJSON struct { Window, AsOf string; Survey []surveyRowJSON; Usage usageJSON }` (keys `window`, `as_of`, `survey`, `usage`)
  - `func buildStatsJSON(window string, asOf time.Time, survey []statsRow, rep usageReport) statsJSON`
  - `func writeStatsJSON(out io.Writer, doc statsJSON) error`
  - the `--json` flag on `wt stats`

- [ ] **Step 1: Write the failing tests**

Create `wt/cmd/wt/stats_json_test.go`:

```go
package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/spend"
	"github.com/ohanaverse/local-ai-setup/wt/internal/survey"
)

// pinStatsNow fixes the report's as-of instant for the test.
func pinStatsNow(t *testing.T, at time.Time) {
	t.Helper()
	old := statsNow
	statsNow = func() time.Time { return at }
	t.Cleanup(func() { statsNow = old })
}

// TestStatsJSONDocument pins the --json document byte for byte with spend
// available: one line holding window, as_of, survey and usage; the
// all-agents survey row has a null agent; averages with nothing rated are
// null; a launch-only model has zero spend fields, not null. Scripts and
// the archived `wt stats --json >> usage.jsonl` habit parse exactly this.
func TestStatsJSONDocument(t *testing.T) {
	a, tmp := newTestApp(t)
	asOf := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	pinStatsNow(t, asOf)
	seedSurveyEvents(t, tmp, []survey.Event{
		{Agent: "claude", ModelID: "ollama/gemma4:9b", Timestamp: asOf.Add(-time.Hour), Worked: boolPtr(true), Speed: intPtr(4), Quality: intPtr(5)},
		{Agent: "claude", ModelID: "ollama/gemma4:9b", Timestamp: asOf.Add(-2 * time.Hour), Worked: boolPtr(false)},
	})
	seedLaunches(t, tmp,
		launch{"ollama/gemma4:9b", "claude", time.Hour},
		launch{"ollama/launch-only", "claude", time.Hour},
	)
	stubSpend(t, spend.Result{Unattributed: 3, Rows: []spend.Row{
		{Model: "ollama/gemma4:9b", Requests: 4, PromptTokens: 152, CompletionTokens: 630, Spend: 0},
		{Model: "openrouter/qwen/qwen3.8-27b", Requests: 5, PromptTokens: 517, CompletionTokens: 3135, Spend: 0.0085},
	}}, nil)

	stdout, stderr := runStats(t, a, "--json", "--window", "7d")

	want := `{"window":"7d","as_of":"2026-10-07T12:00:00Z",` +
		`"survey":[` +
		`{"model":"ollama/gemma4:9b","agent":null,"answered":2,"worked":1,"failed":1,"skipped":0,"worked_pct":50,"quality_avg":5,"speed_avg":4},` +
		`{"model":"ollama/gemma4:9b","agent":"claude","answered":2,"worked":1,"failed":1,"skipped":0,"worked_pct":50,"quality_avg":5,"speed_avg":4}],` +
		`"usage":{"spend_status":"ok","spend_reason":"","unattributed_requests":3,"rows":[` +
		`{"model":"ollama/gemma4:9b","family":"ollama","launches":1,"requests":4,"prompt_tokens":152,"completion_tokens":630,"spend":0},` +
		`{"model":"ollama/launch-only","family":"ollama","launches":1,"requests":0,"prompt_tokens":0,"completion_tokens":0,"spend":0},` +
		`{"model":"openrouter/qwen/qwen3.8-27b","family":"openrouter","launches":0,"requests":5,"prompt_tokens":517,"completion_tokens":3135,"spend":0.0085}]}}` + "\n"
	if stdout != want {
		t.Errorf("stdout =\n%s\nwant\n%s", stdout, want)
	}
	if stderr != "wt: 3 requests had no model and are not shown\n" {
		t.Errorf("stderr = %q, want the unattributed note there and not in the document's place", stderr)
	}
}

// TestStatsJSONIsOneDocumentInEveryMode verifies that whatever happened to
// spend — unavailable, not configured, skipped by --agent — and with both
// stores empty, stdout is exactly one JSON document: the status and reason
// are fields, the spend fields and unattributed_requests are null, both
// lists are [] rather than null, and the note still goes to stderr. A
// stray line on stdout breaks `wt stats --json | jq` precisely when the
// database is down.
func TestStatsJSONIsOneDocumentInEveryMode(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		err        error
		status     string
		wantReason string
	}{
		{"unavailable", nil, spend.ErrNoPsql, "unavailable", "spend unavailable: psql not found on PATH"},
		{"not configured", nil, fmt.Errorf("%w: nothing names one", litellm.ErrNoDatabase), "not_configured",
			"spend unavailable: no LiteLLM database configured: nothing names one"},
		{"skipped", []string{"--agent", "claude"}, nil, "skipped",
			"--agent narrows launches only; LiteLLM does not log which agent sent a request, so spend is not shown"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, tmp := newTestApp(t)
			seedLaunches(t, tmp, launch{"ollama/gemma4:9b", "claude", time.Hour})
			stubSpend(t, spend.Result{}, c.err)

			stdout, stderr := runStats(t, a, append([]string{"--json"}, c.args...)...)

			if strings.Count(stdout, "\n") != 1 || !strings.HasSuffix(stdout, "}\n") {
				t.Fatalf("stdout = %q, want exactly one line holding one object", stdout)
			}
			var doc statsJSON
			dec := json.NewDecoder(strings.NewReader(stdout))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&doc); err != nil {
				t.Fatalf("stdout is not the document: %v\n%s", err, stdout)
			}
			if doc.Window != "30d" || doc.Usage.SpendStatus != c.status || doc.Usage.SpendReason != c.wantReason {
				t.Errorf("window %q, status %q, reason %q; want 30d, %q, %q", doc.Window, doc.Usage.SpendStatus, doc.Usage.SpendReason, c.status, c.wantReason)
			}
			if doc.Usage.UnattributedRequests != nil {
				t.Errorf("unattributed_requests = %d, want null without spend data", *doc.Usage.UnattributedRequests)
			}
			if len(doc.Usage.Rows) != 1 || doc.Usage.Rows[0].Launches != 1 || doc.Usage.Rows[0].Requests != nil || doc.Usage.Rows[0].Spend != nil {
				t.Errorf("rows = %+v, want one row with 1 launch and null spend fields", doc.Usage.Rows)
			}
			if !strings.Contains(stdout, `"survey":[]`) {
				t.Errorf("stdout = %s, want \"survey\":[] for an empty survey store", stdout)
			}
			if stderr != "wt: "+c.wantReason+"\n" {
				t.Errorf("stderr = %q, want the one note", stderr)
			}
		})
	}

	t.Run("nothing at all", func(t *testing.T) {
		a, _ := newTestApp(t)
		stubSpend(t, spend.Result{}, nil)
		pinStatsNow(t, time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC))
		stdout, stderr := runStats(t, a, "--json")
		want := `{"window":"30d","as_of":"2026-10-07T12:00:00Z","survey":[],` +
			`"usage":{"spend_status":"ok","spend_reason":"","unattributed_requests":0,"rows":[]}}` + "\n"
		if stdout != want || stderr != "" {
			t.Errorf("stdout = %s stderr = %q\nwant %s and no stderr", stdout, stderr, want)
		}
	})
}

// TestStatsJSONAgreesWithTheTable verifies the document and the text
// tables are two renderings of one report: the same filters select the same
// models in both. A --json that ignored --family or --model would archive
// numbers the user did not ask for.
func TestStatsJSONAgreesWithTheTable(t *testing.T) {
	a, tmp := newTestApp(t)
	seedLaunches(t, tmp, launch{"ollama/a", "claude", time.Hour}, launch{"openrouter/b", "claude", time.Hour})
	stubSpend(t, spend.Result{}, nil)

	for _, args := range [][]string{{"--family", "openrouter"}, {"--model", "openrouter/b"}} {
		text, _ := runStats(t, a, args...)
		stdout, _ := runStats(t, a, append([]string{"--json"}, args...)...)
		var doc statsJSON
		if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
			t.Fatal(err)
		}
		if len(doc.Usage.Rows) != 1 || doc.Usage.Rows[0].Model != "openrouter/b" {
			t.Errorf("%v --json rows = %+v, want only openrouter/b", args, doc.Usage.Rows)
		}
		if strings.Contains(text, "ollama/a") || !strings.Contains(text, "openrouter/b") {
			t.Errorf("%v text =\n%s\nwant only openrouter/b", args, text)
		}
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run from `wt/`: `go test -count=1 ./cmd/wt -run TestStatsJSON`

Expected: the build fails with `undefined: statsJSON`.

- [ ] **Step 3: Implement the document**

Create `wt/cmd/wt/stats_json.go`:

```go
// `wt stats --json`: both tables as one JSON document on stdout.
package main

import (
	"encoding/json"
	"io"
	"time"
)

// statsJSON is the document `wt stats --json` prints: one line, one
// object. Every list is present and is [] when empty; a value that does not
// exist (an average with nothing rated, spend with no spend data) is null,
// never 0.
type statsJSON struct {
	Window string          `json:"window"`
	AsOf   string          `json:"as_of"`
	Survey []surveyRowJSON `json:"survey"`
	Usage  usageJSON       `json:"usage"`
}

// surveyRowJSON is one survey table row. Agent is null for a model's
// all-agents aggregate (the table's "(all)").
type surveyRowJSON struct {
	Model      string   `json:"model"`
	Agent      *string  `json:"agent"`
	Answered   int      `json:"answered"`
	Worked     int      `json:"worked"`
	Failed     int      `json:"failed"`
	Skipped    int      `json:"skipped"`
	WorkedPct  *float64 `json:"worked_pct"`
	QualityAvg *float64 `json:"quality_avg"`
	SpeedAvg   *float64 `json:"speed_avg"`
}

// usageJSON is the usage table. SpendStatus is one of ok, not_configured,
// unavailable, skipped; SpendReason is the stderr note's text ("" for ok).
type usageJSON struct {
	SpendStatus          string         `json:"spend_status"`
	SpendReason          string         `json:"spend_reason"`
	UnattributedRequests *int64         `json:"unattributed_requests"`
	Rows                 []usageRowJSON `json:"rows"`
}

// usageRowJSON is one model. The four spend fields are null unless
// spend_status is ok.
type usageRowJSON struct {
	Model            string   `json:"model"`
	Family           string   `json:"family"`
	Launches         int      `json:"launches"`
	Requests         *int64   `json:"requests"`
	PromptTokens     *int64   `json:"prompt_tokens"`
	CompletionTokens *int64   `json:"completion_tokens"`
	Spend            *float64 `json:"spend"`
}

// optional returns &v when ok, else nil (JSON null).
func optional(v float64, ok bool) *float64 {
	if !ok {
		return nil
	}
	return &v
}

// buildStatsJSON assembles the document from the same rows the text tables
// are rendered from, so the two renderings cannot disagree.
func buildStatsJSON(window string, asOf time.Time, survey []statsRow, rep usageReport) statsJSON {
	doc := statsJSON{
		Window: window,
		AsOf:   asOf.UTC().Format(time.RFC3339),
		Survey: []surveyRowJSON{},
		Usage:  usageJSON{SpendStatus: rep.SpendStatus, SpendReason: rep.SpendReason, Rows: []usageRowJSON{}},
	}
	for _, r := range survey {
		if r.Stats.Answered == 0 && r.Stats.Skipped == 0 {
			continue
		}
		j := surveyRowJSON{
			Model: r.ModelID, Answered: r.Stats.Answered, Worked: r.Stats.Worked,
			Failed: r.Stats.Failed, Skipped: r.Stats.Skipped,
			WorkedPct:  optional(r.Stats.WorkedPct()),
			QualityAvg: optional(r.Stats.QualityAvg()),
			SpeedAvg:   optional(r.Stats.SpeedAvg()),
		}
		if r.Agent != statsAllAgents {
			agent := r.Agent
			j.Agent = &agent
		}
		doc.Survey = append(doc.Survey, j)
	}
	if rep.SpendStatus == spendOK {
		n := rep.Unattributed
		doc.Usage.UnattributedRequests = &n
	}
	for _, r := range rep.Rows {
		j := usageRowJSON{Model: r.Model, Family: r.Family, Launches: r.Launches}
		if r.Spend != nil {
			s := *r.Spend
			j.Requests, j.PromptTokens, j.CompletionTokens, j.Spend = &s.Requests, &s.PromptTokens, &s.CompletionTokens, &s.Spend
		}
		doc.Usage.Rows = append(doc.Usage.Rows, j)
	}
	return doc
}

// writeStatsJSON prints doc as one line.
func writeStatsJSON(out io.Writer, doc statsJSON) error {
	return json.NewEncoder(out).Encode(doc)
}
```

- [ ] **Step 4: Add the flag and the branch to `statsCmd`**

In `wt/cmd/wt/stats.go`, inside `statsCmd`'s `RunE`, replace:

```go
			window, err := parseStatsWindow(mustGetString(cmd, "window"))
```

with:

```go
			windowName := mustGetString(cmd, "window")
			window, err := parseStatsWindow(windowName)
```

Replace:

```go
			out := cmd.OutOrStdout()
			if table := surveyTableRows(rows); len(table) == 0 {
				fmt.Fprintln(out, "no survey data")
			} else {
				fmt.Fprintln(out, renderTable(surveyHeaders, table, a.theme))
			}
			fmt.Fprintln(out)
			if len(rep.Rows) == 0 {
				fmt.Fprintln(out, "no usage data")
			} else {
				fmt.Fprintln(out, renderUsageTable(rep.Rows, stdoutWidth()))
			}
```

with:

```go
			out := cmd.OutOrStdout()
			if asJSON, _ := cmd.Flags().GetBool("json"); asJSON {
				if windowName == "" {
					windowName = "30d"
				}
				if err := writeStatsJSON(out, buildStatsJSON(windowName, asOf, rows, rep)); err != nil {
					return err
				}
			} else {
				if table := surveyTableRows(rows); len(table) == 0 {
					fmt.Fprintln(out, "no survey data")
				} else {
					fmt.Fprintln(out, renderTable(surveyHeaders, table, a.theme))
				}
				fmt.Fprintln(out)
				if len(rep.Rows) == 0 {
					fmt.Fprintln(out, "no usage data")
				} else {
					fmt.Fprintln(out, renderUsageTable(rep.Rows, stdoutWidth()))
				}
			}
```

(`parseStatsWindow` accepts an empty value as 30 days, so the document names it `30d`.) The loop that prints the notes to stderr stays below this block and runs in both modes.

After the `--family` flag line, add:

```go
	cmd.Flags().Bool("json", false, "Print one JSON document (window, as_of, survey, usage) instead of the tables")
```

- [ ] **Step 5: Run the tests**

Run from `wt/`: `go test -count=1 ./cmd/wt -run TestStatsJSON -v`

Expected: three `--- PASS` lines (`TestStatsJSONDocument`, `TestStatsJSONIsOneDocumentInEveryMode`, `TestStatsJSONAgreesWithTheTable`), then `ok`.

- [ ] **Step 6: Update `wt/CLAUDE.md`**

In Steps 6 to 9 each edit replaces one exact piece of text, and every "replace" string occurs exactly once in its file. Two replacements here.

In `wt/CLAUDE.md`, replace:

````text
| `cmd/wt/stats_usage_table.go` | `renderUsageTable` — the borderless, width-aware usage table (an id is never truncated) |
````

with:

````text
| `cmd/wt/stats_usage_table.go` | `renderUsageTable` — the borderless, width-aware usage table (an id is never truncated) |
| `cmd/wt/stats_json.go` | `wt stats --json` — `buildStatsJSON`, one document built from the same rows as the tables |
````

In `wt/CLAUDE.md`, replace:

````text
wt stats [--window 7d] [--family F]  # survey table, then launches and LiteLLM spend per model
````

with:

````text
wt stats [--window 7d] [--family F] [--json]  # survey table, then launches and LiteLLM spend per model
````

- [ ] **Step 7: Add the `wt/CHANGELOG.md` entry**

In `wt/CHANGELOG.md`, replace:

````text
  Reconciliation sections and the "Last wt launch" line.
````

with:

````text
  Reconciliation sections and the "Last wt launch" line.
- `wt stats --json` prints both tables as one JSON document (`window`,
  `as_of`, `survey`, `usage`) on stdout; notes stay on stderr.
````

- [ ] **Step 8: Add `--json` to `wt/docs/wt-stats.md`**

Three replacements.

In `wt/docs/wt-stats.md`, replace:

````text
wt stats [--window 1d|7d|30d] [--model <id>] [--agent <name>] [--family <family>]
````

with:

````text
wt stats [--window 1d|7d|30d] [--model <id>] [--agent <name>] [--family <family>] [--json]
````

In `wt/docs/wt-stats.md`, replace:

````text
  The survey table ignores it.
````

with:

````text
  The survey table ignores it.
- `--json` — print one JSON document instead of the two tables; see
  [`--json`](#--json).
````

In `wt/docs/wt-stats.md`, replace:

````text
## Where the survey data comes from
````

with:

````text
## `--json`

`wt stats --json` prints one JSON document on one line instead of the two
tables. Notes still go to stderr, so stdout is always exactly the document.
Example (illustrative values, wrapped here for reading):

```json
{"window":"7d","as_of":"2026-10-07T12:00:00Z",
 "survey":[
  {"model":"ollama/gemma4:9b","agent":null,"answered":2,"worked":1,"failed":1,"skipped":0,
   "worked_pct":50,"quality_avg":5,"speed_avg":4},
  {"model":"ollama/gemma4:9b","agent":"claude","answered":2,"worked":1,"failed":1,"skipped":0,
   "worked_pct":50,"quality_avg":5,"speed_avg":4}],
 "usage":{"spend_status":"ok","spend_reason":"","unattributed_requests":3,
  "rows":[
   {"model":"ollama/gemma4:9b","family":"gemma4","launches":1,
    "requests":4,"prompt_tokens":152,"completion_tokens":630,"spend":0}]}}
```

- `window` is the `--window` value; `as_of` is the end of the window, UTC.
- `survey` holds the survey table's rows. `agent` is `null` for a model's
  all-agents aggregate (`(all)` in the table). `worked_pct`, `quality_avg`
  and `speed_avg` are unrounded, and `null` where the table shows `-`.
- `usage.spend_status` is `ok`, `not_configured`, `unavailable` or `skipped`
  (`--agent`); `usage.spend_reason` is the note's text, `""` for `ok`.
- `usage.rows` holds the usage table's rows, with each model's `family`.
  `requests`, `prompt_tokens`, `completion_tokens` and `spend` are `null`
  unless `spend_status` is `ok`, and so is `unattributed_requests`.
- `usage.unattributed_requests` counts requests with no model in the whole
  window, whatever `--model` or `--family` selected.
- Model ids are JSON strings, so a control character in one is escaped by
  JSON's own rules (`\n`, `\u001b`), not the table's.
- `survey` and `usage.rows` are always arrays, `[]` when empty.

To keep a history, append one line per run: `wt stats --json >> ~/notes/wt-stats.jsonl`.

## Where the survey data comes from
````

- [ ] **Step 9: Add the `--json` paragraph to `docs/guides/07-usage-and-spend.md`**

In `docs/guides/07-usage-and-spend.md`, replace:

````text
[wt/docs/wt-stats.md](../../wt/docs/wt-stats.md).
````

with:

````text
[wt/docs/wt-stats.md](../../wt/docs/wt-stats.md).

For a copy to keep, `wt stats --json` prints the same report as one JSON
line; append it to a file to build a history (`wt stats --json >>
~/notes/wt-stats.jsonl`).
````

- [ ] **Step 10: Verify the slice**

Run from `wt/`:

```bash
test -z "$(gofmt -l .)" && go vet ./... && go test -count=1 ./... && make check
```

Expected: 21 lines starting with `ok`; `make check` passes.

Run from the monorepo root: `make check-links`
Expected: no broken link reported.

- [ ] **Step 11: Commit**

Run from the monorepo root:

```bash
git add wt/cmd/wt/stats.go wt/cmd/wt/stats_json.go wt/cmd/wt/stats_json_test.go wt/docs/wt-stats.md wt/CLAUDE.md wt/CHANGELOG.md docs/guides/07-usage-and-spend.md
git commit -m "feat(wt): wt stats --json"
```

- [ ] **Step 12: Verify as CI does, then hand off**

Run from the monorepo root: `make test-all`
Expected: lint passes, the llmbench and modelman suites pass, and every wt `go test -count=1 ./...` line starts with `ok`.

Stop here. Tell the owner the branch is ready and what `make test-all` printed. Push and open the PR only after their OK. Suggested title: `feat(wt): wt stats --json (retirement step 5, 3 of 3)`. The body says: one line on stdout with `window`, `as_of`, `survey`, `usage`; notes stay on stderr; the document is built from the same rows as the tables.

---

## After Step 5

Nothing in this plan deletes Python. Step 6's deletion PR removes `modelman/src/modelman/usage/`, `modelman/tests/usage/` and the `psycopg2-binary` dependency, and its docs PR replaces guide 07's `modelman usage report` text with the `wt stats` section added here.

One known flake, seen once while this plan's code was being replayed and unrelated to it: `internal/lifecycle`'s `TestEnsureModelRouteToSendsItsOutputToTheCaller` failed in one full run and passed in eight reruns and every other full run. If it fails during a verification step here, rerun before looking for a cause in this work.
