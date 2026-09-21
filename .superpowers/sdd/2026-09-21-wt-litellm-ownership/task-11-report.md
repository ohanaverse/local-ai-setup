# Task 11 report

## Implemented
- state.py: removed LitellmState / StateStore.litellm; raw [litellm] table stays in `extra` (no longer excluded), written back verbatim; absent table stays absent. Tests in tests/test_state.py (round trip incl. api_key + unknown keys, survives locked_state mutation, absent stays absent). RED: 3 failed before; GREEN after.
- migrate.py: migrate_wt_gateway_to_litellm operates on state.extra["litellm"], same skip rule/return; legacy-rescue comment added; tests (test_migrate, commands/test_migrate, contracts fixture) updated.
- main.py: `litellm status` prints wt text as-is; on/off/set call wt_bridge, same confirmation lines; WtBridgeError -> `error: msg` on stderr, exit 1. Incomplete-config warning kept via wt_bridge.litellm_status() (the bridge discards wt's own stderr warning, so no double warning); a failed status read never masks a successful change. tests/test_litellm_cli.py rewritten (RED: 6 failed; GREEN 8 passed).
- screens/models.py: status line reads wt once (mount + after toggle), cached in `_litellm_status`; "unavailable" when wt unreachable; toggle uses bridge, no modelman.toml write, no self.state touch, notify on error. Also fixed a pre-existing display bug: "[l]" was eaten by Rich markup (now escaped).
- Expose fix: direction computed first; only EXPOSE blocked by missing mapping; message distinguishes wt-unavailable ("wt is unavailable — cannot expose (install wt)") vs unmapped. Added litellm.provider_table_available(). 6 new screen tests (RED: all failed on old screen; GREEN).

## Checks
- `uv run pytest -q`: 1457 passed. `make check`: ruff/format clean, mypy "Success: no issues found in 87 source files".
- git grep in modelman/ for `state.litellm`, `.litellm.enabled`, `LitellmState`, `.litellm.url`: only a historical `state.litellm_exposed` mention in a dated design doc.

## Deferred / concerns
- Item 5 (EXPOSED column for local models from `wt litellm list`) SKIPPED: needs per-refresh routed_ids fetch plus column-logic change and the test fake's `list` is static-empty; flag-based display still works. Deferred.
- litellm_status() in on_mount is a synchronous subprocess with the bridge default 120s timeout; a hung wt could delay mount (bridge left unchanged per amendment 6).
- provider_table_available() treats an empty table as "unavailable" (a real wt table is never empty).
- No docs/CLAUDE.md touched (Task 12).
