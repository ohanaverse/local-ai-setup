# `wt cloud-sync`

Brings `registry.toml` up to date with what two public services publish:
OpenRouter's per-token prices, and ollama's cloud catalog. Nothing runs it
for you: wt never fetches prices on a launch, it only reminds you (see
[The stale-pricing notice](#the-stale-pricing-notice)).

```bash
wt cloud-sync --dry-run                              # print both plans, change nothing
wt cloud-sync                                        # print both plans, ask once, apply
wt cloud-sync --yes --approve-removals <digest>      # apply without a terminal
wt cloud-sync --only openrouter                      # one flow
```

An agent running the sync for you follows the `cloud-sync` skill
(`wt/.claude/skills/cloud-sync/SKILL.md`), which is this page as a
procedure.

## The two flows

Both run unless `--only` names one. They are independent: a flow that fails
or is refused does not stop the other. Every output line says which flow it
is about (`openrouter:`, `ollama:`); the route sync's lines are `routes:`.

### openrouter

Fetches `https://openrouter.ai/api/v1/models` and re-prices the registry's
OpenRouter-priced models: the models whose `provider_id` is `openrouter`,
and no other ([guide 02](../../docs/guides/02-providers-and-models.md)). A
model is matched on its `model_name`. A model of any other provider is never
fetched for, changed or warned about, whatever its name is.

- Input and output prices always take OpenRouter's value. The cache price is
  replaced only when OpenRouter reports one. A subscription price and any
  `time_prices` row are never touched.
- Every matched model is stamped (`pricing_updated_at`), whether or not its
  price moved.
- A model OpenRouter does not list (`warning: No OpenRouter match for …`),
  lists with no price (`warning: No pricing data for …`), or lists with a
  price that is negative or not a number (`warning: Could not use
  OpenRouter's pricing for …`; OpenRouter gives `-1` for a price that
  varies) is left exactly as it is, and is not stamped.
- When there are OpenRouter-priced models and not one could be matched, the
  run says so: `openrouter: no model could be refreshed, so nothing is stamped
  and wt's stale-pricing notice is not cleared; the warnings above say why
  for each model`.
- **The provider key `openrouter_priced` is no longer read.** It used to put
  another provider's models into this flow (`true`) or take a provider's
  models out (`false`). A registry that still has it loads as before, the
  key decides nothing, nothing warns about it, and wt leaves it in the file
  when it writes; delete it by hand when you like.
- **A model under any other cloud provider is no longer refreshed**, whether
  or not that key was ever set. Until #322 a model of a cloud provider that
  is not an agent's native one (a gateway that serves OpenRouter ids, say)
  was refreshed from OpenRouter and counted for the stale-pricing notice.
  It now keeps the price it has, and the notice does not watch it. To have
  it refreshed, register it under the `openrouter` provider
  (`provider_id = "openrouter"`); it is then also reached through
  OpenRouter. To keep it where it is, set its price yourself:
  `wt model edit <id> --input-price … --output-price …`
  ([wt-model.md](wt-model.md)).

### ollama

Mirrors `https://ollama.com/pricing` into three places: the registry's
ollama cloud entries, the cloud tags `ollama list` shows, and (through the
route sync) LiteLLM's routes.

- It updates prices, the off-peak row included; adds an entry for each new
  page model and `ollama pull`s it; pulls the tag of an existing entry that
  `ollama list` does not show (which is how a run after a failed pull
  retries it); removes entries the page no longer lists and `ollama rm`s
  their tags; and `ollama rm`s pulled cloud tags that have no entry.
- **A page name does not determine its tag.** A name that pins a size maps
  directly (`<name>:<size>` to `<name>:<size>-cloud`). A bare name is looked
  up on `https://ollama.com/library/<name>/tags`: `<name>:cloud` if ollama
  publishes it, else the single `<name>:<size>-cloud`. A name whose
  registry entry's tag is already pulled is not looked up again — unless
  two pulled entries claim it, the mark of an earlier re-tag killed before
  its removal, where the library lookup decides. A model
  with no cloud tag, several, or a library page that could not be read is
  skipped with a `warning:`: nothing is added or pulled for it, and no entry
  or pulled tag with its name is removed; an entry wt can still match to it
  gets the page's prices. An outage of ollama.com/library is therefore never read
  as "these models are gone".
- **Re-tagging.** An entry under a tag ollama does not publish is replaced
  by one under the real tag; its removal line reads `(re-tagged as <new
  id>)`. The replacement is a copy of the old row (family, tags, and every
  other key) with four overrides: `id` and `model_name` become the new
  tag, and `location` and `source` are set to `cloud` and `curated`. Its
  LiteLLM route name changes with the id, because the route name is the
  model id.
- **A new entry's family** is that of an existing ollama entry with the same
  name stem, else the stem itself. Change it afterwards with
  `wt model edit <id> --family <name>` ([wt-model.md](wt-model.md)).
- **A new entry's subscription** price and period are the ones every
  existing ollama cloud entry shares. If they disagree it gets none, and the
  plan says `warning: ollama cloud entries disagree on subscription pricing;
  new entries get none`.
- **An id that is already taken is not added**: `warning: <id> already
  exists …; not adding`.
- **A price cell the parser does not recognize keeps the registry's price**
  (`warning: <name> input: unrecognized price '<cell>'; existing price
  kept`). Only an empty or `-` cell clears a price. A page where most cells
  are unrecognized is exit 3 instead.
- **Off-peak prices** are stored as the `[[models.cost.time_prices]]` row
  labelled `off-peak`: UTC, weekdays outside 12:00–18:00, all day at
  weekends. That window is ollama's published one and is written, not read
  from the page. Rows with any other label are yours and are kept. The
  model picker applies the row: outside ollama's peak hours it shows the
  off-peak price ([The price the picker
  shows](#the-price-the-picker-shows)). LiteLLM's route carries the peak
  price at every hour.
- **Local models are never touched.** Only ollama cloud entries and `*:cloud`
  / `*-cloud` tags are in scope. An entry marked `location = "cloud"` whose
  `model_name` is not a cloud tag is removed from the registry like any other
  entry the page does not list, but its tag is never handed to `ollama rm`;
  the plan carries a `warning:` that says so.
- **`ollama` is run as a command** (`list`, `pull`, `rm`), pinned with
  `OLLAMA_HOST` to the address of the registry's `ollama` provider row,
  whatever `OLLAMA_HOST` your shell exports. A run with pulls or removals
  prints that address once: `ollama: daemon at <address>`.

## A flow the registry does not use is skipped

Each flow is one sync for one service. When the registry does not use that
service the flow prints one line on stdout, fetches nothing, and counts as
finished (exit 0). This is never an error, however the flow was asked for.

| The registry has | Line printed |
|---|---|
| no OpenRouter-priced model | `openrouter: no OpenRouter-priced model in the registry; nothing to refresh` |
| no `ollama` provider row | `ollama: no ollama provider in the registry; nothing to mirror` |

- The openrouter rule is judged by the models, not by whether an `openrouter`
  provider row exists: a row with no model under it is skipped, and a model
  whose `provider_id` is `openrouter` counts even when the row is missing.
- The ollama rule holds for `--only ollama`, `--html`,
  `--approve-removals` and `--force` too; with no `ollama` row the `--html`
  file is not even opened. `wt cloud-sync` never adds a provider row:
  `wt model init` does, for one on a machine where `ollama` is on `PATH`
  ([wt-model.md](wt-model.md)).
- With neither, the command prints the two lines and nothing else: it asks
  nothing, writes nothing and syncs no route.

## What a run does, in order

1. **Reads the registry and plans both flows.** Nothing is locked or
   written. A model id that is in the registry more than once stops a flow
   here (exit 1), in a dry run too, because the write could never address
   the row: for openrouter, on any OpenRouter-priced model, before anything is
   fetched; for ollama, on a row its plan re-prices, removes or copies
   a re-tagged entry from.
2. **Prints both plans.** `--dry-run` stops here.
3. **Applies the ollama flow's gates** ([Removals need approval](#removals-need-approval)).
   A gated ollama plan is dropped; the openrouter plan goes on.
4. **Asks one question** on the terminal, `Apply these changes? [y/N]`,
   covering every plan still pending. Only `y` or `yes` applies; anything
   else prints `<flow>: not applied (declined)` for each pending flow, which
   then counts as finished (exit 0, unless another flow failed or step 3
   refused the ollama flow). Input piped to the command can never approve.
   `--yes` skips the question; with no terminal and no `--yes` the run stops
   (`<flow>: error: not applied: there is no terminal to confirm on — rerun
   with --yes to apply without asking` for each pending flow; exit 1, or
   the ollama flow's 4 or 5 if step 3 refused it). A plan with nothing to apply
   is not asked about.
5. **Writes `registry.toml` once**, for both flows. Under the file's lock
   each plan is made again from the file as it then is, and a plan that no
   longer prints as it did is not applied; the other flow's still is. A
   registry removed since the plan was printed is not created. Each flow
   that was written says so: `openrouter: refreshed N model(s); N price(s)
   changed`, `ollama: updated N, added N and removed N model(s)`.
6. **Runs the ollama flow's `ollama pull`s, then its `ollama rm`s.** A failure
   is an `error:` line and exit 1, and the rest still run. A tag is removed
   only if it is a cloud tag, `ollama list` showed it, and no remaining
   registry entry names it. Each one that worked is a line: `ollama:
   pulled <tag>`, `ollama: removed <tag>`.
7. **Syncs the LiteLLM routes once**, if a price or the set of models
   changed, or a tag was pulled or removed. A run that only re-stamped
   prices that were already current does not sync, so it cannot restart the
   proxy. On a machine with no LiteLLM `config.yaml` there is nothing to
   sync, and no warning. A sync that fails is a `routes: warning:` and
   never changes the exit status; run `wt litellm sync`.

The route sync is the last step. A run interrupted before it (Ctrl-C during
a pull) leaves the routes behind the registry; the next `wt cloud-sync`
syncs them only if it still has a tag to pull or remove, so after an
interrupted run, run `wt litellm sync`.

An illustrative dry run (made-up models and prices; yours will differ):

```text
openrouter: openrouter.ai: 2 OpenRouter-priced models in the registry (prices are input/cached/output per million tokens)
openrouter: Price updates (1):
openrouter:   openrouter/acme--alpha-1: 1/-/4 -> 0.8/-/3.2
openrouter: Unchanged prices: 1
ollama: ollama.com/pricing: 6 models (prices are input/cached/output per million tokens)
ollama: Price updates (0):
ollama: Registry additions (1):
ollama:   ollama/beta-2:cloud [family beta-2]: 0.5/0.05/2 (off-peak 0.25/0.025/1)
ollama: Unchanged prices: 5
ollama: ollama pull (1):
ollama:   ollama/beta-2:cloud
ollama: Registry removals — off ollama.com/pricing or under a tag ollama doesn't publish; `ollama rm` if pulled (1):
ollama:   ollama/gamma-3:cloud
ollama: ollama rm — pulled, unregistered, off the page (0):
ollama: Removal digest: 0bcc560b956e (apply non-interactively with `--yes --approve-removals 0bcc560b956e`)
```

## Removals need approval

Answering `y` at the question approves the plans on the screen, removals
included. `--yes` has nobody reading, so it never deletes on its own.

- **The removal digest.** When the ollama plan removes anything (a registry
  entry, or a stray pulled tag), it ends with a `Removal digest:` line, as
  in the example above. Under `--yes` that plan is applied only with
  `--approve-removals` and that digest. The digest covers exactly the
  registry removals and the stray `ollama rm`s, so a page that changed since
  the dry run cannot delete a model nobody reviewed. Without `--yes`,
  `--approve-removals` is ignored and the question is asked.
- **Mass removal.** A plan that would remove more than half the ollama cloud
  entries is refused unless `--force` is given, with or without `--yes`: it
  is more likely a page that parsed wrong than a catalog that shrank.
  Re-tagged entries do not count, since they come straight back. `--force`
  does not replace the digest. A dry run of such a plan still exits 0; the
  refusal is the apply's.

Either refusal means the ollama flow changes nothing at all: no price
update, no addition, no pull.

## Flags

| Flag | Meaning |
|---|---|
| `--only <flows>` | Run only these flows: a comma list of `openrouter`, `ollama`. Default: both |
| `--dry-run` | Print the plans and change nothing |
| `--yes` | Apply without asking. Removals still need `--approve-removals` |
| `--approve-removals <digest>` | ollama, with `--yes`: the removal digest a reviewed `--dry-run` printed |
| `--force` | ollama: apply even if more than half the ollama cloud entries would be removed |
| `--html <file>` | ollama: parse this saved pricing page instead of fetching it |

- `--html` is not an offline mode: cloud tags are still looked up on
  ollama.com/library, and `ollama list` is still run.
- Usage errors (exit 1, nothing fetched): `--only` with an empty value
  (`--only: no flow named (valid: openrouter, ollama)`) or an unknown name; and
  `--html`, `--approve-removals` or `--force` together with `--only openrouter`
  (`--force is for the ollama flow, which --only openrouter leaves out`). An
  ignored `--force` or digest would be a gate you believe was passed.

## Exit status

| Code | Meaning |
|---|---|
| 0 | Every selected flow finished, was skipped, had nothing to do, or was declined at the question |
| 1 | A step failed in either flow, or a usage error |
| 2 | ollama changed nothing: an input could not be read |
| 3 | ollama changed nothing: the pricing page changed shape |
| 4 | ollama changed nothing: mass removal refused |
| 5 | ollama changed nothing: removals not approved, or the registry changed after the plan was printed |

Codes 2 to 5 describe the ollama flow only, and win over 1. The openrouter flow
may have been applied, or have failed, in the same run; its lines say which.
Every other wt command exits 1 on any error.

The last line on stderr gives the reason: `wt: cloud-sync: the ollama flow
changed nothing: <why>` for 2 to 5, and `wt: cloud-sync: a step failed; see
the error lines above` for 1.

**Exit 1** — each cause has its own `error:` line:

- openrouter: OpenRouter's list could not be fetched or read (`could not read
  OpenRouter's prices: …; no price was changed`).
- openrouter: the registry changed after the plan was printed (`no price was
  changed — run it again`).
- either flow: a model id is in the registry twice — on any
  OpenRouter-priced model (openrouter), or on a row the ollama plan addresses.
  Refused before the plan is printed.
- either flow: no terminal to confirm on, without `--yes`.
- either flow: the registry write was refused (`registry.toml was not
  changed: …`). When the refusal is a row that would not load and both flows
  were pending, each flow is then written on its own, so the broken row
  holds up only the flow that owns it; that flow's line names the row (`the
  price changes were not written: …` / `the ollama flow's changes were not
  written: …`).
- ollama: an `ollama pull` or an `ollama rm` failed, or wt stopped it at
  its time limit. See [Failure and recovery](#failure-and-recovery).
- a usage error; a `config.toml` or registry that could not be loaded; a
  missing registry (``model registry not found at <path> — seed it with
  `wt model init` ``).

**Exit 2** — an input could not be read. Each line ends `nothing was
changed`:

- the pricing page could not be fetched (`could not fetch
  ollama.com/pricing: …`);
- the `--html` file could not be read (`cannot read <file>: …`);
- `ollama list` could not be run (``could not run `ollama list` against
  <address> (is the ollama daemon up?): …``; with no `ollama` command on
  `PATH` the reason is `the ollama command is not installed (not on PATH)`);
- not one page model's cloud tag could be resolved (`could not resolve a
  cloud tag for any model on ollama.com/library`);
- the `ollama` provider row's `base_url` names no daemon (`… (it reads as
  "<value>"): set auth.base_url to http://host:port`). This one is checked
  before anything is fetched. ollama reads an empty or unusable
  `OLLAMA_HOST` as its default daemon, so wt refuses rather than pull into,
  or remove from, a daemon the registry does not name. A row with no
  `base_url` at all has wt's default address and is fine.

**Exit 3** — the pricing page no longer reads as a price table. The line
names the check that failed, and the page is saved:
`ollama: raw HTML saved to <path>`, a new file
`$TMPDIR/ollama-pricing-<YYYYMMDD-HHMMSS>.html`, mode 0600. A `--html` file
that does not parse is copied there too, and its message still says `could
not parse ollama.com/pricing`. If the page cannot be saved, that line
reads `ollama: the raw HTML could not be saved: <why>` instead: fetch the
page again yourself, or pass a saved copy with `--html`. The repair is a
code change to `wt/internal/cloudsync/pricingpage.go`; the `cloud-sync`
skill has the steps.

**Exit 4** — `<n> of <m> ollama cloud entries would be removed — check the
page parsed correctly, then re-run with --force. Nothing was changed by
the ollama flow.` The line's `<n>` counts every removal line, a re-tagged one
included, though the gate above leaves re-tagged entries out.

**Exit 5** — two causes, told apart by the last line:

- `…: removals not approved`: under `--yes` the plan deletes, and
  `--approve-removals` was missing or carried another plan's digest. The
  error line prints the digest of the plan it just made; review a dry run
  before using it.
- `…: the registry changed after the plan was printed`: another program
  wrote the registry between the plan and the write, so this is no longer
  the plan that was approved. Run it again. (The openrouter flow's version of
  this is exit 1.)

## Failure and recovery

The registry is written before ollama is touched, so a failure after the
write leaves the two out of step in a known way, and the next run finishes
the job.

- **A failed pull.** One line, ``ollama: error: `ollama pull <tag>`
  failed: <ollama's last line>``, then ``<tag> is in the registry but not
  pulled, so it is not routed; run `wt cloud-sync` again to retry the
  pull``. A pull needs no digest. Whether pulling a cloud model needs you to
  be signed in to ollama has not been verified; if it does, this is where
  it shows.
- **A failed rm.** ``ollama: error: `ollama rm <tag>` failed: …``, then
  `<tag> is left pulled; the next run lists it as a stray tag — start again
  from --dry-run, since the removal digest may have changed`. When the tag
  belonged to a removed entry, that entry is already gone from the
  registry, so the digest is a different one and the same approved command
  would exit 5; a stray tag's digest may be unchanged. Either way, start
  from `--dry-run`.
- **An `ollama rm` that answers "not found"** and names the tag counts as
  done: the tag is gone, which is what was wanted. wt matches those words
  and the tag in ollama's message; ollama's exact wording has not been
  checked against a real daemon, so if a removal of an already-gone tag is
  reported as failed, that match is the place to look.
- **A timeout** reads `timed out after 10m (wt's own limit)` for a pull, `1m`
  for an rm, `30s` for `ollama list` (which is exit 2). The limit is wt's,
  not ollama's.
- **An interrupted run.** Run `wt cloud-sync --dry-run` again to see what is
  left, and `wt litellm sync` for the routes.

## What it reads and writes

- **Reads** the registry, the three public pages named above
  (`openrouter.ai/api/v1/models`, `ollama.com/pricing`,
  `ollama.com/library/<name>/tags`), and `ollama list`. No API key is sent
  to any of them.
- **Writes `registry.toml`** through wt's one registry writer, which keeps
  every key it was not asked to change. openrouter: the cost keys that moved,
  and `pricing_updated_at`. ollama: the same on ollama cloud entries, plus
  the `off-peak` row of `cost.time_prices` and `catalog_name` (the page name
  an entry was matched to); ollama cloud rows added and removed. As with
  every tool that writes the registry, comments in the file do not survive.
- **Writes LiteLLM's `config.yaml`** only through the route sync.
- **Changes ollama's store** through `ollama pull` and `ollama rm`.

### Against a copy of the registry

A trial run that redirects the registry (`WT_REGISTRY=/path/to/copy`, or
`XDG_CONFIG_HOME`) must also set `WT_LITELLM_CONFIG`. `config.yaml`'s path
follows neither variable, so without it wt refuses the route sync rather
than rewrite the real proxy's routes from a scratch registry:

```text
routes: warning: LiteLLM routes not touched: the registry is <copy> but config.yaml is the default <path> — set WT_LITELLM_CONFIG to the config.yaml that registry belongs to
```

The copy is still written and the exit status is unchanged. Redirecting the
registry does not redirect ollama: an apply still runs the real `ollama`
against the address in the copy's provider row. Use `--dry-run`, or put a
stand-in `ollama` first on `PATH`.

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
  it. A window whose `end` is before its `start` runs past midnight, into
  the next day ([A row you write by hand](#a-row-you-write-by-hand)). The
  rows the sync writes are all UTC.
- This holds for every row, whoever wrote it: the ollama flow's `off-peak`
  row (the model's own price is the peak one) and a row you wrote by hand.
- **A price that depends on the time is marked `~`** after its three
  numbers, and the column's heading then reads `COST (~ varies by time)`.
  The mark says the model has a row that applies, at any hour: the price
  beside it is the one in force now and will change. The mark and its
  heading take room. The mark makes the COST column one character wider, so
  a table with a time-priced model in it can show one usage column fewer
  (1D, 7D, 30D or SURVEY) than the same table without one, at the one
  width where that column only just fitted. And where COST is the last
  column the terminal has room for, the longer heading needs three columns
  more than `COST` does, so at those three widths such a table has no COST
  column where a table of single prices has one. The line under the table
  names the price there (next bullet).
- **The line under the table names the highlighted model's price** beside
  the LiteLLM mode: `LiteLLM: on   cost~ 0.66/0.022/1.98` (input, cached
  input and output per million tokens; `cost~` when the price depends on
  the time; illustrative). The COST column is not always drawn: a narrow
  table gives up whole columns, and one with a model id longer than about
  15 characters in it, which is most tables, has no COST column at 80
  columns. This line is there when the column is not. The price on it is whole or absent, never cut
  short: on a terminal too narrow for the mode and the price together (at
  40 columns, a price with many digits under `LiteLLM: on` and all but the
  shortest under `LiteLLM: off (direct)`) the price takes the line and the
  mode gives way, and a price too long even for that leaves the line as
  the mode alone. The pickers of `wt start` and `wt smoke` have no such
  line; they have the COST column only.
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
`mon` to `sun`) and `start` and `end` as `"HH:MM"`; `"24:00"` is the end of
a day.

**A window may run past midnight.** When `end` is before `start`, the
window starts at `start` on each listed day and ends at `end` on the next
day: `22:00` to `06:00` on `mon` is Monday 22:00 to Tuesday 06:00
(illustrative):

```toml
[[models.cost.time_prices]]
label = "night"
timezone = "America/New_York"
output_price_per_million = 10.0
windows = [
    { days = ["mon", "tue", "wed", "thu", "fri"], start = "22:00", end = "06:00" },
]
```

`days` are the days a window starts on, so this row holds Saturday 05:00
(Friday's night) and not Monday 05:00 (Sunday is not listed). The next day
is a calendar day on the row's own clock: on a night the zone's clocks
change, the window is an hour longer or shorter and still ends when the
clock reads 06:00. `start` and `end` must differ; a whole day is `00:00`
to `24:00`. The rows the sync writes never run past midnight (each day's
hours are under that day), and the sync keeps a row of yours that does, as
the one window you wrote. (As for any entry, the first write by wt lays a
file typed by hand out again in its own layout and drops comments.) A wt
built before this change still loads a row with such a window but refuses
to write its model (`start must be before end`), so write one only once
the installed wt has this change.

A row that breaks one of these rules is still loaded, and it is **not
applied**, whole, at any hour: the picker shows the model's own price, and
no `~` unless another row of the model applies. `wt model list` names the
row and the rule on stderr; for a time written `9:00`, which is not
`HH:MM`: `<id>: cost.time_prices[0]: windows[0]: start must be HH:MM, got
9:00; wt reads it as absent (fix the entry in <registry>)`. The Models tab
of `wt config` shows the same under the selected model. wt also refuses to
write that model's entry until the row is fixed: `wt model edit`, a save
in `wt config`, or a `wt cloud-sync` with a change for that model is
refused with `invalid registry entry: model "<id>": cost: time_prices[0]:
windows[0]: start must be HH:MM, got 9:00`, and nothing is written.

A value of the wrong TOML type is another matter. `windows = "always"`, or a
price in quotes, makes the registry unreadable, and every wt command stops
with `wt: config error: parse <registry>: toml: line N (last key
"models.cost.time_prices.windows"): incompatible types: TOML value has type
string; destination has type slice (fix that file by hand)` until that line
is fixed, as for a wrong type anywhere else in the registry.

## The stale-pricing notice

After a launch wt prints `wt: token pricing last refreshed <date> — run 'wt
cloud-sync'` when the newest `pricing_updated_at` among the OpenRouter-priced
models is more than 7 days old, or `wt: token pricing has never been
refreshed — run 'wt cloud-sync'` when none has a stamp. Nothing else stores
the date. The ollama flow's stamps on ollama cloud entries do not count,
and with no OpenRouter-priced model the notice is silent.

Running `wt cloud-sync` clears it as long as the openrouter flow matches at
least one model. When it matches none, every model of the `openrouter`
provider has a `warning:` that says why (a `model_name` OpenRouter does not
list, most often); correct the name, or remove the model
(`wt model rm <id>`), and the notice goes with the last of them.

The notice watches the models of the `openrouter` provider only. A model
under any other cloud provider neither raises it nor clears it, and the
provider key `openrouter_priced`, which once changed that, is no longer
read (see [openrouter](#openrouter)).
