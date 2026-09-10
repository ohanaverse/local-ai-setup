# wt stale-pricing notice — design

Issue: https://github.com/ohanaverse/local-ai-setup/issues/69 (deferred follow-up to #63/#64 price-refresh work)

## Summary

wt prints a one-line post-run notice when modelman's token pricing is stale, telling the user to run `modelman refresh-prices`. wt is a passive consumer — it never triggers the refresh itself.

## Behavior

After the post-run summary line, wt prints one line to stdout when `price_refresh_last_run` (top-level key in `~/.config/local-ai/modelman.toml`, owned by modelman, format `YYYY-MM-DD`) is not today's date:

- Date present and ≠ today:
  `wt: token pricing last refreshed <date> — run 'modelman refresh-prices'`
- Key missing or empty:
  `wt: token pricing has never been refreshed — run 'modelman refresh-prices'`
- Date present and = today, or read/parse error: silence.

Semantics deliberately match modelman's own staleness rule (`should_run_price_refresh`: stale when the date isn't today). The notice shows on every eligible launch — no dedupe, no config knob. It prints on success and non-zero exit and never affects the exit code.

## Placement

Emitted in both launch paths (non-TUI `runAgentCmd` and TUI), immediately after the existing summary line and before the session survey. Implementation mirrors `internal/agents.Summary`: a pure function (e.g. `PriceNotice(lastRun string, present bool, now func() time.Time) string`) returning the notice or `""`, with callers reading state and printing. State access via a small accessor on `internal/config` (e.g. `PriceRefreshLastRun() (string, bool)`) — `(value, present)`; any read/parse error returns `("", false)` so a broken file stays silent.

## Reading the state

`internal/config/modelman.go` already parses `~/.config/local-ai/modelman.toml` read-only (exposure flags). Add a top-level `PriceRefreshLastRun string \`toml:"price_refresh_last_run"\`` field to the `modelmanState` struct and expose it through the accessor above. Comparison is plain string equality against `time.Now().Format("2006-01-02")` — modelman only writes dates; a present value is printed as-is if stale.

## Cross-language contract

`docs/contracts/modelman.sample.toml` gains a top-level `price_refresh_last_run = "2026-09-14"` key. Both contract tests (wt `internal/config` fixture test and modelman `tests/contracts/`) are updated so the schema is pinned on both sides.

modelman requires no code change — `modelman refresh-prices` already writes the key (via `set_price_refresh_last_run` in `state.py`). Only the fixture and contract-test updates land on that side.

## Error handling

- Missing `modelman.toml` → no notice (present=false → "never refreshed"… note: a missing *file* means modelman has never run; treat as missing key, i.e. the never-refreshed notice still applies — same as a file present without the key).
- Malformed TOML / unreadable file → silence (parse errors suppress the notice, matching the exposure flags' tolerance).
- Clock/`now` injected for testability; never parsed as a date, only string-compared.

## Testing

Go (same-package, direct-function style per wt conventions):

- Accessor: missing file, file without key, key present, malformed TOML.
- Notice function: today → `""`; yesterday → date wording; empty/missing → never-refreshed wording.
- Launch-path integration: notice printed after the summary line in `runAgentCmd`.

## Docs

- `wt/CLAUDE.md` — one line in the post-run summary section.
- Contract fixture comments updated to mention the new key.
