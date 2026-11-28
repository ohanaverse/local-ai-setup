# Remove rotation from wt: design

Date: 2026-11-27. Follows `2026-11-27-picker-alias-band-design.md` (#752). Size: a large diff (about 10 source files, 18 test files, 20 docs) but few decisions, so this is a short design.

## Intent and why

The user routes with LiteLLM's random selection over alias groups, not with wt's "next model after your last launch" rotation. Rotation still decides where the launcher picker's cursor starts, which overrides the sort order. With rotation gone the cursor opens on the first unblocked row of the sorted list, and since #752 that order is native, slot profiles, aliases, running local, other: aliases (after native and profiles) become the default, which is what the user wants.

## Goals, non-goals, anti-goals

Goals:
- the launcher picker's cursor starts on the first unblocked row (or the `-M` pin, as now);
- delete `rotation.state`, `Last`, `NextFromOrder`, `FirstAfter`, the old-per-slot-files `migrate` and the `wt rotate <tag>` command, with their tests and docs.

Non-goals (stay exactly as they are):
- usage recording: `usage.jsonl`, the 1D/7D/30D columns, `wt stats`, `wt profile slots usage`;
- the `> ` marker on the last launched row (`last-picked.state`);
- the non-TUI launch with no `-M` (it never rotated), `catalog.RegisteredOrdered`'s rules, the server catalog and wire types;
- nothing deletes a user's leftover `rotation.state` or old per-slot files: they stay on disk, unread.

Anti-goals: usage counts or the marker must not stop being written; no new config key or flag.

## Approaches

1. **Behavior only**: stop the cursor using rotation, leave the package. Leaves `rotation.state` written, `wt rotate` and `NextFromOrder` dead; contradicts "removed".
2. **Remove the logic, keep a trimmed `internal/rotation`** for the two surviving jobs. Smaller diff, but a package named `rotation` that rotates nothing.
3. **Remove the logic and the package; fold the survivors into `internal/usage`** (recommended, picked by me, not yet confirmed by the user). `usage` already imports `config` (so no cycle) and already owns `usage.jsonl`; the marker file is the same kind of launch history. More churn (4 call sites, test moves, depcheck, docs), nothing misnamed afterwards.

## Design (approach 3)

- **One recording call.** Without `rotation.state`, `RecordFor` (single model) and `RecordProfile` (slot profile) do the same thing: write `last-picked.state` and append the usage event. They become one: `RecordLaunch(agent, id string) error` on `usage.Store` (best-effort usage append; an error only when `last-picked.state` cannot be written, as today). The four call sites move to it: `cmd/wt-session/launch_remote.go`, `cmd/wt-session/slot_launch.go`, `internal/tui/slot_flow.go`, `internal/tui/remote_start.go`.
- **The marker read.** `LastPicked() (string, bool)` moves to `internal/usage` and reads `last-picked.state` only; the old fallback to `rotation.state` goes (a user with only the old file sees no marker until their next launch).
- **The cursor.** `models_client.go` stops computing a rotation cursor; `msg.cursor` stays the `-M` pin or empty, and `newModelList`'s existing rule (the first row that is not blocked) decides. Its `lastID` for the marker comes from `usage.LastPicked`.
- **First unblocked row includes a row that needs a start** (a stopped local alias or model): the user's Enter on it is the decision to start it, which is how every start row already works; it is my call, flagged for the user.
- **`wt rotate <tag>`** and `rotateModelsRemote` are deleted; `main.go` stops registering it.
- Files and error text: the `rotation.state not saved` message goes; `last-picked.state not saved` stays.
- **Docs and changelog**: `wt/CLAUDE.md` "Rotation (Go)" section is rewritten as a short "Launch history" note; `docs/internals/{local-models,launch-flow,tui,testing,packages}.md`, `wt/docs/{wt-session,wt-config,wt-model-server,wt-agents/*}.md` and guides 00, 03, 06, 14 are corrected wherever they describe the rotation cursor, `rotation.state` or `wt rotate`; `wt/CHANGELOG.md` records the behavior change and the removed command. Historical plans and specs under `docs/superpowers/` and `docs/archive/` are left as they are.

## Constraints

`make pre-pr` is the gate (CI off), rerun on an idle machine if only `internal/modelclient` timing tests fail (memory note). `internal/depcheck`'s package list loses `internal/rotation`. Every `Test*` keeps its what-and-why comment. Tests of removed behavior are deleted with it; tests of surviving behavior (usage attribution, the marker, a profile launch leaving the marker on the profile) move to `internal/usage`.

## Left to the builder

Exact name of the `usage` entry points beyond the two above; whether `catalog.RegisteredOrdered` (used by rotation and `wt rotate`) still has a caller after the removal and, if none, whether to delete it (delete only if nothing else uses it); how to split the work into commits.
