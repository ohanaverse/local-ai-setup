# Picker alias band: design

Date: 2026-11-27. Size: small change (one sort function, its tests, four doc passages).

## Intent and why

The model picker lists alias rows (`alias/<name>`, the tiered groups such as `alias/claude-opus`) among the ordinary models. An alias has no band of its own, so it sorts by its score inside whichever band its state puts it in (a running local alias sits among the running local models, a cloud alias among the rest). The aliases are what the user now launches by choice, so they should sit together, right after the slot profiles.

Target order, top to bottom:

1. native models, alphabetical by id (unchanged);
2. slot-profile rows, alphabetical by id (unchanged);
3. **alias rows, by tier score, highest first (new)**;
4. local models that are ready to answer, by tier score (unchanged);
5. every other model, by tier score (unchanged).

## Goals, non-goals, anti-goals

Goals: aliases form one band between profiles and the running-local band, ordered by the TIER score the row shows.

Non-goals:
- **The default selection does not change in this change.** The launcher picker's cursor still starts on rotation's next model (`models_client.go`, `NextFromOrder`); the sort is only its fallback. Making the first row the default is the next PR, which removes rotation (see "Next PR").
- No change to rotation, `RegisteredOrdered` (a launch with no pin never rotates onto an alias), the non-TUI launch, the standalone pickers (`wt smoke`, `wt start`: their rows come from the model server's catalog as `config.Model`s, which drops the wire `Kind`, so `isAlias` never matches there and an alias sorts among the other models by its family, not in the alias band), the server catalog or any wire type.

Anti-goal: an alias row must not move a native or profile row down, and no model row's order may change.

## Approaches

One sensible way: add a band to `sortRows` (`wt/internal/tui/modelrows.go`). The alternatives do not fit. Sorting on the server would change the catalog's order, which rotation and `-M` resolution read. A display-only reorder after the sort would leave `sortRows`'s documented order, and the picker's default (the sort is also the fallback selection), out of step with what is drawn.

## Design (decided)

- `band` gains a case for an alias row. Numbering becomes native 0, profile 1, **alias 2**, running local 3, other 4. The alias case is tested before the running-local case, so a running local alias is in the alias band.
- An alias row is identified by the wire `Kind == modelapi.KindAlias` (what `catalog.RegisteredOrdered` tests), not by its id prefix.
- Order inside the band: own TIER score descending (`tier.Score()`, the rounded score the column shows); a row with no score sorts after every scored one; ties and unscored rows by id. **No family borrowing**: all aliases share the family `alias`, so the borrowing rule would give an unscored alias the best alias score. (A tier score on an alias row is the first-choice target's, copied by `copyTier`; that is existing behavior and not touched.)
- A blocked or unrouted alias stays in the band; state does not order it.
- The `sortRows` comment (four bands becomes five), `TestSortRowsBands` and `TestSortRowsPutsProfilesBetweenNativeAndTheRest` move to five bands; new tests pin: alias above a running local model and below a profile whatever the scores; score descending; unscored after scored with no family borrow; a running local alias lands in the alias band; a blocked alias stays in it.

## Docs to update (same change)

- `sortRows` doc comment;
- `wt/docs/internals/local-models.md` ("Sort: four bands…");
- `wt/docs/internals/tui.md` (TIER paragraph: "within the model bands");
- `wt/CLAUDE.md` TUI bullet ("within the running-local and other bands");
- `docs/guides/06-wt-agents-and-models.md` (the picker's order paragraph; it also says the cursor starts on rotation's next model, which stays true until the next PR);
- `grep -rn` for any other quote of the order before finishing.

## Constraints

`wt/CLAUDE.md`: "the sort order is also the default selection — changing `sortRows` changes what a bare Enter picks." This change moves a default only where the client picker has no rotation to follow. `NextFromOrder` already copes with a first launch and with a state file naming a model no longer listed (it starts at the first launchable registered model), so the table's first unblocked row decides the cursor only when no launchable registered row exists at all; it is then an alias when no native or profile row is unblocked, where before it was a model. The standalone pickers take `config.Model`s with no wire `Kind`, so `isAlias` never matches and their row 0 is unchanged (an alias there sorts by family, not in the alias band; making them match is a separate change that would move their row 0). `make pre-pr` is the gate (CI is off).

## Left to the builder

Whether any test helper needs an alias-row constructor (`sortCloud`/`sortLocal`/`sortProfile` exist beside `assertOrder`); exact test names.

## Next PR (not part of this change)

Remove rotation: the cursor, `rotation.state`, `NextFromOrder`, `FirstAfter`, `migrate` and `wt rotate <tag>`. The usage record and the `last-picked.state` marker stay; the builder of that PR chooses whether `internal/rotation` stays trimmed or its survivors move into `internal/usage` (it was left open). After it the cursor opens on the first unblocked row, so native, then profile, then alias is the default.
