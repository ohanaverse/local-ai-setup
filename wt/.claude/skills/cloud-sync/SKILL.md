---
name: cloud-sync
description: Refresh cloud model prices and the ollama cloud catalog with `wt cloud-sync` — OpenRouter prices into registry.toml, and https://ollama.com/pricing mirrored into the registry, ollama and LiteLLM (new cloud models registered and pulled, ones Ollama no longer lists unregistered and removed, prices including off-peak). Use when asked to update or refresh model prices, ollama cloud models or ollama pricing, when wt prints "token pricing last refreshed … — run 'wt cloud-sync'", or when `wt cloud-sync` exits 3 (the ollama pricing page changed shape and the parser needs repair).
---

# Cloud sync

`wt cloud-sync` runs two independent flows and prefixes every line with the
one it is about. Both run unless `--only prices` or `--only catalog` picks
one; a flow that fails or is refused does not stop the other.

- **`prices:`** re-prices the registry's OpenRouter-priced models from
  OpenRouter's public model list and stamps each one it matched. The stamps
  are what clear wt's `token pricing last refreshed <date> — run 'wt
  cloud-sync'` notice.
- **`catalog:`** makes three things match <https://ollama.com/pricing>: the
  ollama cloud entries in `registry.toml` with their prices (off-peak
  included), the cloud tags in `ollama list`, and, through one route sync at
  the end, LiteLLM's routes. It adds and `ollama pull`s new page models,
  removes entries the page no longer lists and `ollama rm`s their tags, and
  `ollama rm`s pulled cloud tags that have no entry.

Full reference, for anything not covered here: `wt/docs/wt-cloud-sync.md`.

## Rules that hold throughout

- **Stop and ask the human before anything is removed.** Never pass `--yes`,
  `--approve-removals` or `--force` on your own judgement: each stands for
  an approval only the user can give, for the plan they were shown.
- **Never run `ollama pull` or `ollama rm` yourself**, and never hand-edit
  `registry.toml` to "finish" a sync. The command does both, in the right
  order, against the daemon the registry names.
- **A skipped flow is not a failure.** A registry with no OpenRouter-priced
  model prints `prices: no OpenRouter-priced model in the registry; nothing
  to refresh`; one with no `ollama` provider row prints `catalog: no ollama
  provider in the registry; nothing to mirror`. Either is exit 0, however
  the flow was asked for (`--only catalog` and the catalog's flags
  included). Report the line and go on; do not add a provider row to make a
  flow run unless the user asks for that.
- **Real local models are never touched.** Only ollama cloud entries and
  `*:cloud` / `*-cloud` tags are in scope. An entry marked
  `location = "cloud"` whose tag is not a cloud tag is still removed from
  the registry when the page does not list it, and the plan carries a
  `warning:` saying its tag is left in ollama: show the user that line.
- **`--html FILE` is not an offline mode.** It replaces only the fetch of
  the pricing page; cloud tags are still looked up on ollama.com/library,
  and `ollama list` is still run.

## Steps

1. **Dry run:** `wt cloud-sync --dry-run`. It changes nothing.
   - First run `wt cloud-sync --help`. If it does not print the two flows
     (`prices`, `catalog`), the installed `wt` predates the command: stop
     and ask the user to run `make install` from the repo root. Do not run
     anything else with that binary (what an older `wt` does with the word
     `cloud-sync` has not been checked).
   - Summarize **both** plans for the user. Prices: each `id: old -> new`
     under `Price updates`. Catalog: `Price updates`, `Registry additions`
     (id and family; each also gets a route), `ollama pull`, `Registry
     removals` (each also loses its route; `(re-tagged as …)` marks an entry
     that comes straight back under the tag ollama publishes, with a new id
     and so a new route name), and `ollama rm` (pulled cloud tags with no
     entry).
   - Read out every `warning:` line: an OpenRouter-priced model with no
     match or no usable price, ollama cloud entries that disagree on
     subscription pricing, an unrecognized price cell, a model whose cloud
     tag did not resolve (it is skipped: nothing is added or pulled for it,
     and nothing with its name is removed), a removed entry whose tag is not
     a cloud tag.
   - If it prints `prices: no model could be refreshed, so nothing is
     stamped and wt's stale-pricing notice is not cleared; set
     openrouter_priced = false on a provider whose model names are not
     OpenRouter ids`, tell the user: running the sync will not clear the
     notice, and that provider key is the fix. It is their decision.
   - Note the line `catalog: Removal digest: <digest> (apply
     non-interactively with …)` if there is one. No such line means the
     catalog plan deletes nothing.
   - A non-zero exit here is 1, 2 or 3 from the table below (a dry run never
     exits 4 or 5). Nothing was changed.
2. **Ask.** Get the user's go-ahead for the whole of both plans. List every
   registry removal and every `ollama rm` by name and get an explicit yes to
   those in particular. A dry run does not flag a mass removal (more than
   half the ollama cloud entries, re-tagged ones not counted); the apply
   does. Estimate it: the `Registry removals` lines without `(re-tagged as
   …)`, against the counts of `Price updates`, `Unchanged prices` and
   `Registry removals` added together. If that looks like more than half,
   say so now: the apply refuses it without `--force` (exit 4, whose line
   gives the exact `<n> of <m>`), and that many removals usually means the
   page parsed wrong, not that the catalog shrank.
3. **Apply:** `wt cloud-sync --yes --approve-removals <digest>`, with the
   digest from step 1. Leave `--approve-removals` out if the dry run printed
   no digest. Your shell has no terminal, so the command's one question
   cannot be answered: without `--yes` it stops with `error: not applied:
   there is no terminal to confirm on — rerun with --yes to apply without
   asking` (exit 1).
   - Success reads `prices: refreshed N model(s); N price(s) changed`,
     `catalog: updated N, added N and removed N model(s)`, then one
     `catalog: pulled <tag>` or `catalog: removed <tag>` per tag. Report
     those to the user.
   - `catalog: ollama at <address>` names the daemon the pulls and removals
     go to. It is the registry's ollama provider row's address, whatever
     `OLLAMA_HOST` your shell exports.
   - Exit 5: go back to step 1 and show the user the new plan. Never reuse
     the old digest, and never retry with a digest the user did not see
     next to its plan.
   - Exit 4: go back to the user. Add `--force` only if they confirm the
     removals are real. `--force` does not replace the digest.
   - With exit 2 to 5 the `prices:` flow may still have been applied in the
     same run. Read its lines before telling the user that nothing changed.
4. **Check the routes:** `wt litellm list`. After a `routes: warning:`, an
   exit 1, or a run that was interrupted (Ctrl-C, a crash), run `wt litellm
   sync` first: the route sync is the last thing a run does.
5. **Families.** A new entry's family is derived (an existing ollama entry
   with the same name stem, else the stem itself). Ask whether any should
   change, and change it with `wt model edit <id> --family <name>`, which
   syncs the routes itself. Not a hand edit.

To run one flow: `--only prices` or `--only catalog`. A catalog flag
(`--html`, `--approve-removals`, `--force`) together with `--only prices`
is a usage error, and so is `--only ""`.

## Exit codes

The last line on stderr says which: `wt: cloud-sync: the catalog flow
changed nothing: <why>` for 2 to 5, `wt: cloud-sync: a step failed; see the
error lines above` for 1. Codes 2 to 5 are about the catalog flow only, and
win over 1: with 2 to 5, also read the `prices:` lines for an `error:`.

| Code | Meaning | Do |
|---|---|---|
| 0 | Every selected flow finished, was skipped, had nothing to do, or was declined at the question | Step 4 |
| 1 | A step failed in either flow; see "Exit 1" below. Also a usage error, and a registry that is missing or cannot be read | Read every `error:` line, then the matching entry below |
| 2 | Catalog changed nothing: an input could not be read; see "Exit 2" below | Fix the cause the `catalog: error:` line names, then step 1 |
| 3 | Catalog changed nothing: the pricing page changed shape. Its HTML is saved and the path printed | "Repairing the parser" below |
| 4 | Catalog changed nothing: the plan removes more than half the ollama cloud entries and `--force` was not given | Ask the user; `--force` only on their confirmation |
| 5 | Catalog changed nothing: under `--yes` the plan deletes and no digest, or another plan's, was given (`…: removals not approved`); or the registry changed after the plan was printed (`…: the registry changed after the plan was printed`) | Step 1 again; get the user's yes for the new plan; use its digest |

### Exit 1

- `prices: error: could not read OpenRouter's prices: …; no price was
  changed` — the fetch or its parse failed. Retry later.
- `prices: error: no price was changed: …` or `catalog: error: nothing was
  changed: model "<id>" is in the registry twice …` — an OpenRouter-priced
  model (prices), or a row the catalog plan addresses, has a duplicated id.
  Refused before the plan is printed, in a
  dry run too. Removing one of two rows that share an id is a hand edit of
  `registry.toml`, and which row to keep is the user's call: stop and ask.
- `prices: error: the registry changed after the plan was printed; no price
  was changed — run it again` — another program wrote the registry while
  the plan was open. Step 1 again.
- `… error: not applied: there is no terminal to confirm on — rerun with
  --yes to apply without asking` — nothing was changed. `--yes` needs the
  user's approval of the plan (step 2).
- `… error: registry.toml was not changed: …`, `prices: error: the price
  changes were not written: …` or `catalog: error: the catalog's changes
  were not written: …` — the write was refused; the message names the row or
  file and, where there is one, the repair. When a row one flow must change
  would not load, each flow is written on its own, so the other flow may
  have been applied: read both.
- ``catalog: error: `ollama pull <tag>` failed: <ollama's last line>``,
  followed by ``<tag> is in the registry but not pulled, so it is not routed;
  run `wt cloud-sync` again to retry the pull``. The registry is already
  written and the other pulls and removals still ran. Run the sync again
  (a pull needs no digest, but go through step 1 anyway: the plan is
  different now). Whether `ollama pull` of a cloud model needs the user to
  be signed in to ollama has not been verified; if ollama's line reads like
  an authorization failure, show it to the user and ask them to sign in.
- ``catalog: error: `ollama rm <tag>` failed: …``, followed by `<tag> is
  left pulled; the next run lists it as a stray tag — start again from
  --dry-run, since the removal digest may have changed`. Do exactly that:
  step 1, a new approval, the new digest. (An `ollama rm` that answers "not
  found" and names the tag counts as done; that match is on those words
  and the tag, and ollama's exact wording has not been checked against a
  real daemon.)
- `… timed out after 10m (wt's own limit)` (pull; `1m` for rm) — wt
  stopped the command, not ollama. Treat it as the failed pull or rm above.

### Exit 2

Each is one `catalog: error:` line ending `nothing was changed`:

- `could not fetch ollama.com/pricing: …` — network. Retry.
- `cannot read <file>: …` — the `--html` file.
- ``could not run `ollama list` against <address> (is the ollama daemon
  up?): …`` — ask the user to start ollama; do not start it yourself unless
  asked. Without the hint, the reason is `the ollama command is not
  installed (not on PATH)`.
- `could not resolve a cloud tag for any model on ollama.com/library` —
  ollama.com/library is unreachable or changed; the `catalog:` lines above
  it say why for each model. Retry later.
- `the ollama provider row's base_url names no daemon (it reads as "…"):
  set auth.base_url to http://host:port` — a registry fix for the user to
  make or approve.

## Repairing the parser (exit 3)

The error names the check that failed and where the page was saved:
`catalog: raw HTML saved to <path>`, a file
`$TMPDIR/ollama-pricing-<YYYYMMDD-HHMMSS>.html`, mode 0600. (A `--html FILE`
that does not parse is copied there too, and the message still says
`could not parse ollama.com/pricing`.) If the line is instead `catalog:
the raw HTML could not be saved: <why>`, there is no file: ask the user for
a saved copy of the page and work from that. Work from `wt/`.

1. Open the saved HTML and find the pricing table.
2. Change **only** `internal/cloudsync/pricingpage.go`: `ParsePricing`, and
   `collectTables` / `mapColumns` if the structure moved. Keep every check:
   columns found by header text, at least `MinRows` rows, a model name that
   is an ollama tag, no duplicate row, every off-peak row has a base row,
   and most price cells parse. A check that fires is the parser working: a
   catalog that silently lost rows would plan the removal of every ollama
   cloud entry.
3. Replace `internal/cloudsync/testdata/ollama_pricing.html` with the saved
   page, update `TestParsePricingFixture`'s expected count and prices, and
   run `go test ./internal/cloudsync && make check`.
4. Try it on the page that broke it:
   `go run ./cmd/wt cloud-sync --only catalog --dry-run --html <saved page>`
   (it still reads ollama.com/library and runs `ollama list`), then go
   through the steps above from step 1. The first plan after a parser change
   deserves a slower read than usual: check its model count and removals
   with the user before any apply.

If the page's off-peak **window** wording changes (it is not parsed), update
`offpeakRow` in `internal/cloudsync/catalog.go` and its test.

## While modelman still exists

`modelman refresh-prices` and `modelman ollama-catalog sync` still work and
are frozen; use `wt cloud-sync`. Both tools stamp the same
`pricing_updated_at` key, and they print the same removal digest for the
same plan.
