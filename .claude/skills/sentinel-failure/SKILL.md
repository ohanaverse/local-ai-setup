---
name: sentinel-failure
description: Investigate a failed modeltiers sentinel check (modeltiers run exits 3, prints "sentinels: F of N FAILED" or "a sentinel moved"), say what moved and why, and recommend a contracts.toml fix. Diagnoses and recommends; asks before editing anything. Use when a modeltiers run fails on a sentinel or the user asks why one moved.
---

## What a sentinel is

`contracts.toml` pins a few well-known models to the votes and tier they had when the floors were calibrated. Every `run` re-derives them from live data; if any differ, nothing is written (exit 3) and there is no override flag. The point is to catch a source changing scale, a model's identity going wrong or a bad rule edit before a wrong `tiers.toml` reaches wt's picker. So the job is to find out *which* of those happened, not just to make the check pass. Re-pinning a vote that moved for a bad reason hides the problem the check exists to show.

The rules and the three-row decision table live in `docs/guides/12-model-tiers.md`, step 5. Read that section first and follow it; this skill is the order to investigate in and what to hand back. Do not copy the guide's rules into the answer from memory.

## Steps

1. **Reproduce without writing anything.** From the repo root, with a helper (a shell function; a variable holding the command does not work in zsh):
   ```bash
   m() { uv run --directory modeltiers modeltiers "$@"; }
   m run --dry-run --offline > /tmp/run.out 2> /tmp/run.err; echo "exit $?"
   sed -n '/^sentinels:/,$p' /tmp/run.out   # the sentinel table is the last block of a long report
   ```
   A failed live run has already saved the downloads it read, so `--offline` sees the same data and nothing new is fetched; every source then prints `stale`, which is expected. It writes no `tiers.toml` and prunes nothing. If the user wants fresh data, drop `--offline` and say so. Read **every** `FAIL` block (`sentinels: F of N FAILED` gives the count) with its `scores:`, `votes:`, `pinned:` and `differs:` lines; sentinels often fail for different reasons at once, so classify each separately. Pass a scratch `MODELTIERS_OUT` if the real file must stay untouched. A scratch `MODELTIERS_CACHE` is empty, so with `--offline` it exits 1 (no good snapshot); use one only for a live run, which fills it.
2. **Classify it** with the guide's table, from the `differs:` lines:
   - one sentinel, one signal: that source re-scored the model;
   - several sentinels, same signal, same direction: the source's scale moved (Arena's score is relative to its pool and drifts when the pool changes);
   - `voted nothing, pinned <tier>`: the model left that source;
   - votes all match but `differs: tier before the gates is X, pinned Y`: the Arena lower bound crossed `marathon_ci_lower` (or fell back under it), not a floor of a vote. Check `arena_agent_ci_lower` (step 3); the fix is the sentinel's `tier`, or `marathon_ci_lower` if many models sit near it;
   - sentinels that changed order (`<low>` not under `<high>` in the guide's window step): not a scale move; re-pin instead.
   A sentinel naming an unknown key is exit 1, not a sentinel failure: that is a `contracts.toml` or `aliases.toml` mistake.
3. **Check the evidence before recommending.** For each failing sentinel and signal:
   - `m ladder <signal> --low <a little under> --high <a little over>` shows the resolved scores the run used. The signal is `intelligence_index`, `arena_agent` or `eci`, or `arena_agent_ci_lower` for the marathon bar. Leave a flag out when no sentinel sits on that side of the floor. Confirm the model's row is where the table says, and that the number really moved rather than the identity (an alias or name match now pointing at another model's row shows up as a score near a different model's).
   - `m explain <provider_id/model_name or canonical key>` reads only the last written `tiers.toml`, so it answers only for a key that file holds, and shows what that file had, for old and new scores side by side. If there is no file, or the key is not in it, say there is nothing to compare with. Under a scratch `MODELTIERS_OUT` it reads that file, which may not be a real last-passing one: say which.
   - Say whether the move looks plausible for that source (a small re-score near a floor) or suspicious (a large jump, a negative or missing score, several models moving together).
4. **Work out the proposed edit.** For a re-score: the sentinel's pinned `votes` entry (and its `tier`, only if the tier before the gates moved too) set to what the run printed; a signal that now votes nothing is pinned `unrated` (the file accepts it), or its pin is dropped, which loses that check. For a scale move: the floor(s) to move, with the guide's window and midpoint procedure (`ladder` gives the gaps). For a model that left a source: drop that signal from its `votes`, or replace the sentinel. Always: `version` set to today's date.
5. **Report, then stop and ask.** Hand back one short report:
   - **What failed:** the model(s), the signal(s), pinned vs now, and the scores.
   - **Why (your reading, and how sure):** re-score, scale move, left the source, or identity/alias problem, with the evidence from step 3.
   - **Recommended fix:** the exact lines to change in `modeltiers/src/modeltiers/data/contracts.toml`, including the `version` line.
   - **Alternatives:** the other reasonable choices and what each gives up (for example, dropping a pin loses a check). For a floor move, name the other models it would flip, read from `ladder` in the gap it would move across; a floor move affects every model near it.
   - **Not a fix:** if the evidence points at something other than a contracts edit (an alias, a source problem), say that instead of recommending a re-pin.
   Then ask whether to apply the recommendation. **Do not edit any file until the user says yes.**
6. **After approval only:** make the edit, then run the guide's acceptance checks: `run --offline` into a scratch `MODELTIERS_OUT` prints `sentinels: N of N hold` and exit 0, and the `tierdump` diff against the last passing file has no line you cannot account for (guide step 5, item 4); `make -C modeltiers test` passes. Show the result. Do not write the real `tiers.toml`, commit or push unless the user asks.

## Gotchas

- `--dry-run` still writes snapshots (not `tiers.toml`); use `MODELTIERS_CACHE` at a scratch path for any trial that must not touch the real cache.
- A run that fails sentinels writes nothing, so the "before" table for the diff is the file the last passing run wrote, not anything from the failed run.
- Several sentinels moving on one signal in the same direction is a re-fit of that signal's floors, not several separate re-pins.
- Keep `contracts.toml` edits in their own commit; they re-calibrate the tier rule and should not ride along with code changes.
