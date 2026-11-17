# wt Phase 3, work package 1: the import-graph ratchet and the first leaf moves

Status: scoped from a read-only research pass on 2026-11-17 (go list evidence on main bb38e55d); the order (Phase 3, then Phase 4) and the Phase 4 config-home and `model list` rulings were agreed with the user the same day. This is a pure refactor: no behavior change.

## Intent

The spec's Migration step 3 says "untangle `internal/tui` into model-admin screens and session screens". The measured reality is different: `internal/tui` has **no admin screens** (the Models tab lives in `internal/configeditor`, the stop picker in `internal/survey`, the commands in `cmd/wt-session`). What puts the admin packages (`lifecycle`, `litellm`, `localmodels`, `cloudsync`, `modeladmin`, `configeditor`, `spend`) into the session binary is (a) `cmd/wt-session` itself importing them for the host commands (Phase 4 moves those) and (b) three thin edges in session library packages: `catalog` -> `lifecycle`/`litellm`/`localmodels`, `tui` -> `lifecycle` (one type), `survey/stop.go`, and `cleanup` -> `localmodels` (one formatter). Phase 3 is therefore a package-graph cleanup, delivered in small PRs behind a `go list -deps` ratchet test that makes the boundary enforceable in CI.

## This package (WP1)

1. **The ratchet test** (new, `go list -deps` through `os/exec`, skipped with a clear message when `go` is not on PATH):
   - (a) the session library packages `internal/cleanup`, `internal/agents`, `internal/modelclient`, `internal/smoke`, `internal/profiles`, `internal/catalog`, `internal/survey` and `internal/tui` must not link `lifecycle`, `litellm`, `localmodels`, `cloudsync`, `spend`, `modeladmin`, `configeditor`, `modelserver`, `modelgateway`, `modeljobs`, `sessionreg`, `candidates`. Packages that cannot meet this yet are on an **explicit, per-package allow-list of expected offenders that can only shrink**: the test also fails when an allow-listed offender is no longer linked, so a fixed edge must be deleted from the list (that is the ratchet).
   - (b) `cmd/wt-model` must not link `tui`, `configeditor`, `cleanup`, `agents`, `rotation`, `rescue`, `rootmove`, `removal` or `modelclient` (it passes today; the test keeps it that way).
   - `provider` and `config` stay allowed in the session closure by ruling (`config` imports `provider`, a pure data table; banning it means splitting `config`).
   - It complements, not replaces, `cmd/wt-session/boundary_test.go` (an AST call scan): the scan catches host calls that need no new import, the graph test catches new imports.
2. **The leaf moves that make part (a) true for `cleanup` (and shrink `tui`, `catalog`):**
   - `lifecycle.OthersAnswer` (a 3-field type the tui uses) moves to a leaf both sides can import (`modelapi` is the wire vocabulary for the others-stop and the natural home; the builder may pick a tiny leaf if `modelapi` is wrong, with a stated reason).
   - `localmodels.FormatSize` (used by `cleanup/present.go`) moves out of `localmodels` to a leaf formatting home.
   - `catalog` stops calling `litellm.PolicyFor` (a one-line wrapper) and calls `provider.PolicyFor` directly.
   - `catalog.Row.Action` stops calling `lifecycle.Startable` at read time: `Build` stamps the answer on the row (or input) [as built: `Row.Action` derives it from `internal/provider`'s table through `catalog.startable`, which `TestBackendsMatchProviderTable` pins to `lifecycle.Startable`; nothing is stamped, and the answers are the same], so the picker, smoke and modelserver answers stay **identical** (modelserver catalog tests and `catalog_test.go` cover it; the wire path already carries the action from the server).
   - `catalog.Input` keeps its `localmodels` type (`Input.Inventory`; it has no `lifecycle` type left): `catalog` and `smoke` stay on the allow-list and shrink in WP2 [done in WP2: see "WP2 as built"].
3. **Docs in the same change:** `wt/docs/internals/packages.md` (package graph rules), `wt/docs/internals/testing.md` (the new test), `wt/CLAUDE.md` (a short rule: the allow-list only shrinks), and the spec's Phase 3 wording (it is a package-graph change, not a screen move).

## Non-goals (later packages)

WP2 splits `catalog`'s host half (`Build`, `Input`, `MissingReason`) out; WP3 splits `survey/stop.go`; WP4 detaches `configeditor` from `tui` (`ErrorStyle`, `ThemedListDelegate`); converting the tui package-var seams (#621) happens while those files are touched. The final assertion that the whole `cmd/wt-session` closure bans the admin set waits for Phase 4 (the commands still live there).

## Constraints

No behavior change; no wire change (`docs/contracts/` fixtures untouched); every move goes DOWN to a leaf (no new shared package that both sides import, to avoid cycles); `Row.Action` answers identical for every row kind; tests keep their assertions and top-level `//` comments; per-call seams; no company-specific details.

## Builder's calls

The exact home of `OthersAnswer` and `FormatSize`; how `Build` stamps `Startable` (a `Row` field, an `Input` flag, or a function value on `Input`); the test's package and name; which packages beyond the listed ones to include if they already pass.

## WP2 as built

`catalog`'s host half moved to `internal/localmodels` (`catalog.go`): `catalog.Build` is `localmodels.CatalogRows`, `catalog.Input` is `localmodels.CatalogInput`, `catalog.MissingReason` is `localmodels.MissingReason`, and `listed` went with them. `localmodels` imports `catalog` for `Row`, which is cycle-free because `catalog`, `provider`, `config` and `modelapi` reach no `localmodels` path. Callers re-qualified: `modelserver` (three sites) and `tui/modelrows.go`.

- `catalog.NotRunning` is exported and stays in `catalog`: `Row.BlockReason` (session) and `localmodels.MissingReason` (host) say the identical wording, and `localmodels` -> `catalog` is the allowed direction. `PairingStartCommand` stays too (`cmd/wt-session/model_write.go` uses it).
- `catalog` no longer calls `localmodels.RunningOnly`: it reads `provider`'s table through its own `runningOnly` (the same pattern as `startable`), pinned to `localmodels.RunningOnly` by `TestRunningOnlyMatchesLocalmodels`.
- Allow-list: `internal/catalog` and `internal/smoke` deleted (smoke's edge was transitive through `catalog`). `internal/survey` and `internal/tui` are unchanged (their admin edges are `survey/stop.go`, WP3, see "WP3 as built").
- Tests: the `Build`/`MissingReason` tests moved with the code as `TestCatalogRows*` (`TestMissingReason` and `TestALoadingRowIsAStartRow` keep their names in the moved file, `catalog_rows_test.go`), with the two old inventory-round tests renamed `TestCatalogRowsListsStoppedRecAsStartRow` and `TestCatalogRowsHidesStoppedMlxLmServer`; `TestStartableMatchesLifecycle` and the new running-only pin moved to the external test package `catalog_test` (`pins_test.go`), because `lifecycle` and `localmodels` now import `catalog`.

## WP3 as built

`survey/stop.go` was split by who needs what. The host half (`ReadStopState`, `ReadStopStateSessions`, `StopEntries`, `Candidate`, `FamilyState`, `StopState`, `ErrStopCancelled`, the stop deps and their tests) moved to the new admin-side package `internal/hoststop`: it drives `lifecycle` and reads `localmodels`, and its only importer is `cmd/wt-session`'s host stop commands (and the `tui` guard test names it). It is a new package, not a shared one, since no session package may import it: `internal/hoststop` joins the banned admin set. `Select` (with `chooseLabeled` and `lockedWriter`), the numbered prompt of the session-side `wt stop` picker, needs neither `lifecycle` nor an inventory and stays in `survey` as `select.go`.

- Allow-list: the `internal/survey` entry is deleted, and `internal/tui` shrinks to `{internal/localmodels}`: its `lifecycle` and `litellm` edges came only through `survey`.
- The last `tui` edge is `modelrows.go`'s `localmodels.CatalogRows` call (WP2), which never passes an inventory: the picker's rows come from the model server's catalog elsewhere, and this call only resolves locations and marks unmapped cloud rows. Removing the entry means an inventory-free row builder in `catalog` that `CatalogRows` shares; not done in WP3 (see "Inventory-free rows as built").

## Inventory-free rows as built

`catalog.ModelRow(cfg, m)` and `catalog.ModelRows(cfg, models)` are the row of a model before any inventory is read: location resolved through the config, status ok, not running, a cloud model with no LiteLLM mapping marked `Unmapped`. `localmodels.CatalogRows` starts every row from `catalog.ModelRow` and only refines it from the snapshot (the Unmapped mark moved up front: it needs a cloud row, and the rows the inventory hides are local, so the result is unchanged). `internal/tui`'s `buildRows` calls `catalog.ModelRows` instead of `CatalogRows`, since the picker never passes an inventory (its models come from the model server). That removed the last `tui` edge: the allow-list is **empty**, so every session library package is off the admin set outright. The dead `tableInput.hideDiscovered` went with it (it filtered discovered rows, which exist only with an inventory). `TestCatalogRowsWithoutInventoryEqualsModelRows` pins `CatalogRows` with a nil inventory to `ModelRows`.
