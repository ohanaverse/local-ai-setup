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
wt stats [--window 1d|7d|30d] [--model <id>] [--agent <name>] [--family <family>] [--json]
```

- `--window` — defaults to `30d`. One window for both tables: survey
  answers, launches and spend all cover the same period, measured back from
  one instant read once when the command starts (`as_of` in `--json`). A
  launch or a survey answer recorded after that instant — or dated after it
  by a clock that was wrong — is not counted, as a request after it is not.
- `--model` — narrow both tables to one model id (exact match).
- `--agent` — narrow the survey table and the launch counts to one agent.
  The spend log does not record which agent sent a request, so spend is not
  shown with `--agent`; one note on stderr says so.
- `--family` — narrow the usage table to one model family: one exact value,
  not the comma list `wt -F` takes, and with no `-F` shorthand on `stats`.
  The survey table ignores it.
- `--json` — print one JSON document instead of the two tables; see
  [`--json`](#--json).

`wt stats` is a report, never a gate: it exits 0 with empty stores, without
`psql`, with the database down, and with a `registry.toml` it cannot read.
Only an invalid flag is an error.

## Survey table

One row per model's `(all)`-agents aggregate, plus one row per observed
(agent, model) combo — sorted by agent name with `(all)` first, then by
model id. A row with no data at all (nothing answered and nothing skipped
in the window) is omitted. A row whose every survey was skipped is kept,
with `-` in the three averaged columns: the combo was tried and never got a
verdict. `--json` holds exactly the rows the table prints. An empty store
(or a filter matching nothing) prints `no survey data`.

```
$ wt stats
┌────────────────────┬────────┬─────────┬─────────┬───────┬────┬─────────┐
│ MODEL              │ AGENT  │ WORKED% │ QUALITY │ SPEED │ N  │ SKIPPED │
├────────────────────┼────────┼─────────┼─────────┼───────┼────┼─────────┤
│ ollama/gemma4:9b   │ (all)  │ 96%     │ 4.2     │ 3.9   │ 12 │ 2       │
│ ollama/qwen3.8:27b │ (all)  │ 88%     │ 4.0     │ 4.1   │ 5  │ 1       │
│ ollama/gemma4:9b   │ claude │ 95%     │ 4.3     │ 4.0   │ 8  │ 1       │
│ ollama/qwen3.8:27b │ claude │ 90%     │ 4.1     │ 4.2   │ 3  │ 0       │
│ ollama/gemma4:9b   │ codex  │ 100%    │ 3.8     │ 3.5   │ 4  │ 1       │
│ ollama/qwen3.8:27b │ codex  │ 80%     │ 3.7     │ 3.9   │ 2  │ 1       │
└────────────────────┴────────┴─────────┴─────────┴───────┴────┴─────────┘
```

`WORKED%`, `QUALITY`, and `SPEED` render `-` when there is nothing to
average (zero answered surveys, or zero rated surveys for that column).
`WORKED%` is `worked / (worked + failed)` over *answered* surveys only —
skips are tracked in the `SKIPPED` column but excluded from that
percentage.

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
| `LAUNCHES` | wt launches of the model in the window, from `usage.jsonl` (the picker's 1d/7d/30d counts, except that a line dated after the report's instant is left out here) |
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

**One model, two spellings.** wt writes a `/` inside a model's name as `--`
(`mtplx/Org--Name`), and the proxy has logged some requests as
`provider/model_name` with the `/` kept (`mtplx/Org/Name`). Those requests
are added to the wt id's row instead of making a second one. A logged id is
folded only when it is not a known id itself and, with every `/` after the
provider prefix (the text up to and including the first `/`) written `--`,
it is exactly a known id — one in `registry.toml`, or one wt launched in the
last 30 days. When both spellings are known ids (`openrouter/z-ai/glm` beside
`openrouter/z-ai--glm`) they are two models and keep two rows. `--model` and
`--family` see the folded rows: `--model mtplx/Org--Name` includes those
requests, and `--model mtplx/Org/Name` matches nothing.

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
routed by) are shown escaped, as `\n` or `\x1b`, and so are characters
that take no space or reorder the text around them — bidi overrides,
zero-width characters, non-ASCII spaces — as `\u202e`.

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
(`WT_REGISTRY`, its legacy alias `MODELMAN_REGISTRY`, or `XDG_CONFIG_HOME`
naming another place) and nothing names `config.yaml`:
set `WT_LITELLM_DATABASE_URL` or `WT_LITELLM_CONFIG` for a scratch setup.

The query waits at most 3 seconds for a connection and 10 seconds in all,
and never prompts for a password. The window is `--window` ending at the
report's instant (the one the launch counts are measured from), in UTC. wt only reads; it writes nothing to the database.

The connection string is never on `psql`'s command line, where `ps` would
show it to every user of the machine. wt reads the string and gives `psql`
each part in libpq's environment variable for it (`PGHOST`, `PGPORT`,
`PGUSER`, `PGPASSWORD`, `PGDATABASE`, `PGSSLMODE`, …), and passes no `-d`.
While the query runs, the password is therefore readable in `psql`'s
environment by processes of your own user and by root (`ps -E` on macOS,
`/proc/<pid>/environ` on Linux), and by no other user.

Two things follow from that:

- **Only the connection string describes the connection.** `psql` gets none
  of the `PG*` variables set in your shell — a stray `PGHOST`, `PGSERVICE`
  or `PGSSLMODE` cannot redirect or weaken the query, and a `PGPASSWORD` or
  `PGPASSFILE` there is not used either. Put the password in the string, in
  the string's `passfile`, or in `~/.pgpass`; a password the string does
  not lead to shows up as `fe_sendauth: no password supplied`.
- **A string wt cannot hand over exactly is refused**, and `psql` is not
  run: see `the LiteLLM database connection string cannot be handed to
  psql` under "When spend is missing".

Both forms work, a URI (`postgresql://user:password@host:port/dbname?sslmode=require`,
several hosts included) and a keyword string (`host=… port=… dbname=…`,
with libpq's quoting). The options passed on are `host`, `hostaddr`,
`port`, `dbname`, `user`, `password`, `passfile`, `connect_timeout`,
`client_encoding`, `options`, `application_name`, `channel_binding`,
`sslmode`, `sslcompression`, `sslcert`, `sslkey`, `sslrootcert`, `sslcrl`,
`sslcrldir`, `sslsni`, `ssl_min_protocol_version`,
`ssl_max_protocol_version`, `requirepeer`, `gssencmode`, `krbsrvname`,
`gsslib` and `target_session_attrs`, plus the URI's `ssl=true`. A
`connect_timeout` in the string replaces the 3 seconds above.

When the database cannot be reached, the note names the host and port wt
tried — `cannot reach the LiteLLM database at 127.0.0.1:5432: Connection
refused` — so a wrong address shows. wt prints no other part of the
connection string, in a note or in `--json`: not the user, the password or
the database name. When the string is written so that `psql` may have read
part of the password as the host or port (an unencoded `/` or `@` in the
password, or a keyword string that quotes a value or names `host` or
`port` after any other keyword), the address is left out as well. The
reason is shown only when it is one wt knows cannot name the user or the
database.

## When spend is missing

Launch counts never depend on the database. When spend cannot be read, the
four spend cells show `-`, and one note on stderr says why. Example
(illustrative values):

```
MODEL             LAUNCHES  REQUESTS  PROMPT  COMPLETION  SPEND
ollama/gemma4:9b         1         -       -           -      -
wt: spend unavailable: psql not found on PATH
```

| Note | Meaning |
|---|---|
| `spend unavailable: psql not found on PATH` | Install a Postgres client that puts `psql` on `PATH` |
| `spend unavailable: cannot reach the LiteLLM database at <host>:<port>: …` | `psql` could not connect to that address (`at socket <path>` for a socket), or got no answer in 10 seconds (no address is shown for that). The reason follows: `Connection refused`, `password authentication failed for user "..."` (names are blanked), `fe_sendauth: no password supplied` (the server wants a password and the string, its `passfile` and `~/.pgpass` give none; a `PGPASSWORD` in your shell is not passed on), `the database host name did not resolve`. A reason wt does not recognise — a server that answers in another language, a connection pooler's own wording — is replaced by `the reason psql gave is not shown because it can quote the connection string`, with the address kept. When `psql` tried several addresses for one host, the last attempt is the one reported. If the address is not the one you expect, "Where spend comes from" lists where it can come from. When `psql`'s message could quote the connection string — it does for one it cannot parse — wt says so instead of printing it, and shows no address; run `psql` yourself to see it |
| `spend unavailable: the LiteLLM database connection string cannot be handed to psql: …` | wt gives `psql` the connection in environment variables, and this string cannot be given that way without changing what it means. `psql` was not run, and nothing from the string is quoted. `it sets an option wt cannot pass in psql's environment`: the string has an option outside the list in "Where spend comes from" — one with no environment variable (`keepalives`, `sslpassword`), `service`, one only recent libpq releases know (`require_auth`), or one that is not libpq's at all (Prisma's `schema`, which `psql` refuses too). `it is not written in a form wt can read the way psql would`: a URI with a space, a control character, a bad percent-escape or `%00` anywhere in it (write a space as `%20`), or a keyword string with an unclosed quote, a value cut by a space, or, in a value that is not in single quotes, one of the letters outside ASCII that `psql` can take for a space (`à` is one; write `password='…'`) |
| `spend unavailable: no LiteLLM database configured: …` | Nothing names a database; the rest says where wt looked — `config.yaml`, the variable it names, wt's environment and the proxy's plist — and which variable to set. It never quotes a value |
| `spend unavailable: the spend query failed: …` | Connected, but the query failed. The server's `ERROR:` line follows (for example the table does not exist: spend logging is not set up), or `psql`'s exit status |
| `spend unavailable: LiteLLM config is invalid: …` | `config.yaml` exists and cannot be parsed |
| `spend unavailable: open …/config.yaml: …` | `config.yaml` exists and cannot be read at all; the system's reason follows (`permission denied`, `is a directory`) |
| `--agent narrows launches only; …` | `--agent` was given |
| `wt's configuration did not load (…) …` | `--family` was given and wt could not load its configuration — `config.toml` or `registry.toml`; the parentheses quote what failed — so there are no registry families and only provider prefixes (`ollama`, `openrouter`) match |

A `-` means "not read". A `0` means the proxy logged nothing for that model.

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
    "requests":4,"prompt_tokens":152,"completion_tokens":630,"spend":0,"also_logged_as":[]}]}}
```

- `window` is the `--window` value; `as_of` is the end of the window, UTC —
  the one instant the survey rows, the launch counts and the spend query
  were all measured from.
- `survey` holds the survey table's rows. `agent` is `null` for a model's
  all-agents aggregate (`(all)` in the table). `worked_pct`, `quality_avg`
  and `speed_avg` are unrounded, and `null` where the table shows `-`.
- `usage.spend_status` is `ok`, `not_configured`, `unavailable` or `skipped`
  (`--agent`); `usage.spend_reason` is the note's text, `""` for `ok`.
- `usage.rows` holds the usage table's rows, with each model's `family`.
  `requests`, `prompt_tokens`, `completion_tokens` and `spend` are `null`
  unless `spend_status` is `ok`, and so is `unattributed_requests`.
- A row's `also_logged_as` lists the other spellings of its `model` whose
  requests are included in the row (see "One model, two spellings" above):
  `["mtplx/Org/Name"]` on the `mtplx/Org--Name` row, `[]` on most rows.
- `usage.unattributed_requests` counts requests with no model in the whole
  window, whatever `--model` or `--family` selected.
- Model ids are JSON strings, so a control character in one is escaped by
  JSON's own rules (`\n`, `\u001b`), not the table's. JSON leaves most
  invisible characters (a bidi override, a zero-width space) as they are;
  the table escapes them.
- `survey`, `usage.rows` and `also_logged_as` are always arrays, `[]` when
  empty.

To keep a history, append one line per run: `wt stats --json >> ~/notes/wt-stats.jsonl`.

## Where the survey data comes from

Every model-driven agent launch (TUI or non-TUI) prompts up to four
questions immediately after the agent exits, before the stop picker and the
summary line:

1. **Did it work?** `[y]es / [n]o / [s]kip (Enter=skip)` — `n` records a
   failure and stops the rating questions; `s`/Enter records a skip and
   stops all further questions.
2. **Speed 1(slow)-5(fast)?** (Enter=skip) — only asked after `y`.
3. **Quality 1(bad)-5(great)?** (Enter=skip) — only asked after `y`.
4. **What task were you doing?** (Enter=skip) — free text, asked after any
   non-skip verdict (both `y` and `n`).

The prompt is silent (no output at all) when stdin is not a TTY, when the
launch had no model (command agents like `shell`), or when the model is
native (`config.Model.Native` — a native launch never touches a surveyed,
priced model, issue #116). There is no config toggle to disable it —
every-exit with a one-keypress skip is the intended trade-off.

The same accumulated stats `wt stats` reports are printed for the current
model (all agents) and the current agent×model combo, at the 1d/7d/30d
windows — after the stop picker and the summary line, so the interactive
prompts cannot scroll them away.
