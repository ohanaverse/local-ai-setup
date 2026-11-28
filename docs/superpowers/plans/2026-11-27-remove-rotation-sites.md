# Remove rotation: site appendix

Generated 2026-11-27 from a read-only recon workflow (five readers plus a completeness critic) over the worktree at base `30dcfaac`. The cursor-flow reader's findings are in the plan, not here. Companion to `2026-11-27-remove-rotation.md`; line numbers are as of that base and drift once edits start, so find a site by its quoted text. `delete` = remove it; `move-to-usage` = becomes the `usage` call; `rewrite` = edit the wording or code; `check` = verify before acting; `keep` = leave. History (`docs/superpowers/**`, `docs/archive/**`, dated CHANGELOG entries) is never edited. Confusable non-sites (key/token/log rotation) are listed at the end.

## Source (non-test Go)

### `wt/cmd/wt-model/model_write.go`
- :254 **rewrite**, `wt-model model edit` long help: "The id, the provider and the model name cannot be edited: usage history,\... — User-visible help string: change to "usage history and launch profiles key on the id" (keep the \n split sensible). No test pins this text (grep found none). Same phrase is in wt/docs/wt-model.md:190 (docs, other scope). Three comments with the same wording: internal/modeladmin/apply.go:418, internal/modeleditor/models_form.go:121, internal/localmodels/catalog.go:72 ('would let rotation, usage and profiles'), all ...

### `wt/cmd/wt-session/commands.go`
- :1 **delete**, entire file: rotateCmd ("rotate <tag>") and rotateModelsRemote; imports context, fmt, catalog, config, mode... — File contains only these two functions; delete the whole file. Its tests are in wt/cmd/wt-session/stats_remote_test.go (uses rotateCmd(a) at ~251 and rotateModelsRemote at ~295; test file, outside this scope but must be deleted/edited in the same change). Only callers of both functions: main.go:655 and that test file.

### `wt/cmd/wt-session/launch_remote.go`
- :16 **rewrite**, import ".../wt/internal/rotation" — Replace with import ".../wt/internal/usage" (not imported yet in this file; no local identifier named usage in it, so no shadowing).
- :28 **rewrite**, launchNative doc: "registers no client session and records no rotation or usage" — "records no usage or last-picked state".
- :70 **rewrite**, step 2 doc: "the caller opens the picker, whose cursor is the rotation's next row" — Say the picker opens with its cursor on the first row that is not blocked (the sorted list's first actionable row).
- :78 **rewrite**, step 5 doc: "build the agent against the gateway, record the rotation, run it" — "record the launch (last-picked marker and usage)".
- :124 **rewrite**, `row, rot, err := resolveModelRemote(...)`; `len(rot) > 0`; comment "(its cursor on the rotation's next row... — Behavior is unchanged (non-TUI launch stays as is). Rename local `rot` to `registered`/`several` for clarity and fix the comment at 128 to "the picker (its cursor on the first row that is not blocked)". Do NOT change the len(rot)>0 test: it keeps registered-only gating of the picker hand-off.
- :183 **move-to-usage**, if rerr := rotation.New().RecordFor(agent, m.ID); rerr != nil { // RecordFor names the file that failed. — Becomes `if rerr := usage.NewStore().RecordLaunch(agent, m.ID); rerr != nil {` and the comment `// RecordLaunch names the file that failed.` Keep the `wt: note: %v` print.

### `wt/cmd/wt-session/main.go`
- :280 **rewrite**, comment "(config, start, rotate, …)" about registered subcommand names — Replace `rotate` with another real subcommand, e.g. "(config, start, smoke, …)".
- :570 **rewrite**, comment "rotation and usage state are keyed by model, agent and pid" — Change to "usage and last-picked state are keyed by model and agent, never by a repository or a branch" (note the old text said 'agent and pid'; keep meaning, drop rotation).
- :655 **rewrite**, cmd.AddCommand(rotateCmd(a), configCmd(a), statsCmd(a), ...) — Drop `rotateCmd(a), ` from the AddCommand list; the rest unchanged.

### `wt/cmd/wt-session/resolve_remote.go`
- :23 **rewrite**, doc: "The second result is the list rotation walks: registered rows only, in Order, never a blocked or star... — Reword without rotation: second result is the registered launchable rows (not alias, not subscription), whose only use is that several of them send a terminal launch to the picker. Rename return/local var `rotation` (line 67 `var launchable, rotation []modelapi.Row`, line 79 `rotation = append(...)`, returns at lines 90 and 92) to e.g. `registered`. Tests (resolve_remote_test.go) only name it `list`, so the rename...

### `wt/cmd/wt-session/slot_launch.go`
- :20 **rewrite**, import ".../wt/internal/rotation" — Replace with the usage import (not imported yet in this file).
- :337 **move-to-usage**, if rerr := rotation.New().RecordProfile(agent, row.Model.ID); rerr != nil { — Becomes usage.NewStore().RecordLaunch(agent, row.Model.ID); keep the `wt: note: %v` print.

### `wt/internal/agents/native.go`
- :14 **rewrite**, NativeModel doc: "so no rotation, usage or survey record is made for it" — "so no usage, last-picked or survey record is made for it".

### `wt/internal/agents/shell.go`
- :9 **rewrite**, comment "Unlike other agents, shell has no model rotation and no yolo flag." — "shell has no model and no yolo flag."

### `wt/internal/catalog/wire.go`
- :132 **delete**, RegisteredOrdered(rows []modelapi.Row) []config.Model, doc 'the list rotation walks ... decision 14: a laun... — After the removal no non-test caller remains (see notes). Delete the function and its doc comment; also delete its tests in internal/catalog/wire_test.go (TestLaunchableAndRegisteredOrdered ~214-230 and the RegisteredOrdered use at ~164-174; keep the Launchable assertions, so split rather than delete the whole test) and wire_subscription_test.go:74, and internal/rotation/rotation_test.go:602 goes with the package....

### `wt/internal/config/config.go`
- :1665 **rewrite**, FirstTag doc: "the shared form of the rotation slot's tag component: both the non-TUI launch path and the T... — Only caller left is internal/tui/models_client.go:415 (verified by grep; no non-test caller in cmd/). Reword to "the tag the picker shows: the first -T tag, else the default tag". Do not delete FirstTag (still used; its tests are config_test).

### `wt/internal/config/fortest.go`
- :17 **rewrite**, IsolateConfigHomeForTest doc: "every run rewrote the real rotation.state with a test model and appended wt-... — "rewrote the real last-picked.state with a test model and appended ..." (history-style explanation; last-picked.state is the file now written).

### `wt/internal/depcheck/depcheck_test.go`
- :30 **rewrite**, sessionBanned list contains "internal/rotation" — TEST file (outside the stated scope) but required: remove "internal/rotation" from sessionBanned. depcheck/doc.go has no package list and no rotation mention. internal/usage is not in any banned list, and cmd/wt-model does not import it, so no new edge problem; cmd/wt-session may import usage (it already does in profile_slots_manage.go and stats_usage.go).

### `wt/internal/localmodels/catalog.go`
- :72 **rewrite**, comment "would let rotation, usage and profiles — all keyed by id — resolve it" — Drop 'rotation,'.

### `wt/internal/modeladmin/apply.go`
- :418 **rewrite**, comment "usage history, rotation and launch profiles key on the id" — Drop 'rotation'.

### `wt/internal/modeleditor/models_form.go`
- :121 **rewrite**, comment "history, rotation and profiles key on the id they make" — Drop 'rotation'.

### `wt/internal/modelping/classify.go`
- :158 **keep**, "for a host that is not loopback, to rotate it" / "rotate the token on the host (wt-model setup)" (line 163) — Token rotation, unrelated to model rotation.

### `wt/internal/modelsetup/plan.go`
- :58 **keep**, LiteLLMRotateKey, planLiteLLMRotate, BackupDir 'rotation' (also modelsetup/apply.go, observe.go, modeledito... — All LiteLLM master key / token rotation, unrelated to model rotation. Not sites.

### `wt/internal/rotation/rotation.go`
- :1 **delete**, whole package (Rotation type, New/NewAt, Last, LastPicked, RecordFor, RecordProfile, NextFromOrder, migrate... — Delete the package directory (rotation.go and rotation_test.go). Two jobs move to internal/usage; everything else is deleted. Fates: New/NewAt delete (usage already has NewStore/NewStoreAt); Rotation.Last delete; Rotation.LastPicked move-to-usage WITHOUT the Last() fallback; Rotation.RecordFor and RecordProfile merge into usage RecordLaunch(agent, id); pickedPath move-to-usage (as a StoreImpl helper); NextFromOrde...

### `wt/internal/tui/app.go`
- :77 **rewrite**, tag   string // active rotation tag group — Field m.tag is still used (header 'tag : %s' in layout.go:150 and the no-models error at models_client.go:455, set at models_client.go:415 via config.FirstTag). Comment becomes "active tag (first of -T, else the default tag)". Keep the field.
- :212 **rewrite**, committed doc: "a second agent, a second rotation/usage record" — "a second usage record" (or "launch record").
- :576 **rewrite**, comment "committing an ollama check (and potentially rotation/usage state)" — "(and potentially usage state)".

### `wt/internal/tui/model_list.go`
- :25 **rewrite**, modelItem doc: "marked records the rotation's last-launched row" — "marked records the last-launched row (last-picked.state)".

### `wt/internal/tui/modelrows.go`
- :173 **rewrite**, isAlias doc: "by the Kind the model server gave it, the test catalog.RegisteredOrdered makes" — Mentions a function that will be deleted; reword to "by the Kind the model server gave it" (drop the clause).
- :209 **rewrite**, sortRows doc: "newModelList's no-rotation fallback picks the first row that is not blocked" — Now it is simply the rule: "newModelList's cursor starts on the first row that is not blocked (or the -M pin)". Keep the rest of the sentence (a stopped alias or a model that needs a start counts).

### `wt/internal/tui/models_client.go`
- :22 **rewrite**, import ".../wt/internal/rotation" — Replace with `.../wt/internal/usage` (check: this file uses the seam newUsageStore() at line 257, which returns the usage.Store interface from model_list.go; the package may already import usage elsewhere but not in this file, so add the import).
- :88 **rewrite**, catalogLoadedMsg doc: "the usage and rotation reads happen in the command" — "the usage and last-picked reads".
- :96 **keep**, cursor field comment "the id the cursor lands on; \"\" is the first actionable row" — Still accurate (now the -M pin or ""). newModelList (line 475) doc 'cursor on cursor, else on the first row that is not blocked' also stays.
- :274 **move-to-usage**, rot := rotation.New()\n lastID, _ := rot.LastPicked()\n msg.tbl = renderTable(rows, r.agent.Name, refs, las... — Replace the first two lines with `lastID, _ := usage.NewStore().LastPicked()`; renderTable line unchanged. LastPicked is on the concrete *StoreImpl, NOT on the usage.Store interface returned by the newUsageStore seam; usage.NewStore() is config.Dir()-based and isolated by the tui TestMain (config.IsolateConfigHomeForTest), exactly as rotation.New() was. If the plan prefers the seam, add LastPicked to the Store int...
- :277 **delete**, after `if r.pinned != "" { msg.cursor = r.pinned; return msg }` the whole block lines 281-291 (comment 'The... — Delete lines 281-291. Final shape: `if r.pinned != "" { msg.cursor = r.pinned }` then `return msg` (the existing `return msg` inside the if can stay or be collapsed). msg.cursor stays the -M pin or "" and newModelList (line 477) picks the first row with it.blocked == "". After the deletion catalog.Launchable is no longer called in this file but catalog is still imported/used (EligibleWire, WireByID, ...); modelapi...

### `wt/internal/tui/modeltable.go`
- :56 **rewrite**, const rowPrefixWidth = 4 // ref column (2) + rotation marker (2) — "last-launched marker (2)".

### `wt/internal/tui/remote_start.go`
- :29 **rewrite**, import ".../wt/internal/rotation" — Replace with the usage import (not imported yet in this file).
- :570 **rewrite**, launchRemote doc: "which records the rotation once the launch gate is past" — "which records the launch (last-picked marker and usage) once the launch gate is past".
- :706 **move-to-usage**, record := func() string { if err := rotation.New().RecordFor(agent, id); err != nil { — usage.NewStore().RecordLaunch(agent, id). Note `usage` is not a local identifier in this function (locals are agent, id, h).

### `wt/internal/tui/slot_flow.go`
- :18 **rewrite**, import ".../wt/internal/rotation" — Replace with the usage import (not imported yet in this file).
- :86 **move-to-usage**, if err := rotation.New().RecordProfile(agent, row.Model.ID); err != nil {  // comment above: "Both writes a... — Becomes usage.NewStore().RecordLaunch(agent, row.Model.ID). Comment still true (marker and usage write).

### `wt/internal/usage/usage.go`
- :17 **keep**, comments mentioning RecordFor (lines 17, 64, 69, 114, 143, 254) — RecordFor stays as the low-level usage append; these comments remain true. Only line 37 mentions rotation.
- :37 **rewrite**, Store interface comment: "rotation records launches, the picker reads counts for the models it lists" — Reword (launches are recorded through RecordLaunch/RecordFor). Interface members RecordFor/Counts/CountsForAgent can stay unchanged; RecordLaunch and LastPicked need NOT be in the interface (the picker's seam newUsageStore returns usage.Store but models_client reads LastPicked from a concrete store; see models_client.go entry). Keeping the interface unchanged avoids touching test doubles.
- :55 **move-to-usage**, type StoreImpl struct{ dir string }; NewStore() / NewStoreAt(dir) — Add to *StoreImpl: (1) func (s *StoreImpl) pickedPath() string { return filepath.Join(s.dir, "last-picked.state") }; (2) func (s *StoreImpl) RecordLaunch(agent, id string) error: write pickedPath with config.WriteFileAtomic(path, []byte(id+"\n"), 0o600) (WriteFileAtomic already does MkdirAll, so no extra MkdirAll needed), THEN always call s.RecordFor(agent, id) and ignore its error (best-effort, runs even when the...

## Tests

### `wt/cmd/wt-session/cwd_norepo_test.go`
- :152 **rewrite**, wantCwdBookkeeping loops over {"rotation.state","last-picked.state"}, each expected to contain claude/opus;... — Loop reduces to last-picked.state alone; add an assertion that rotation.state does NOT exist. Update comment. Callers: TestCwdOutsideARepoLaunchesInTheCurrentDirectory (197), TestCwdInsideARepoIsUnchanged (229), TestCwdInAGitDirectoryWithNoCheckoutLaunches (374).
- :183 **rewrite**, var cwdStateFiles = {"last-picked.state","profile-backups","rotation.state","usage.jsonl","usage.jsonl.lock... — Drop "rotation.state" from the sorted list (slices.Equal at lines 210 and 250 compares it with res.stateFiles); fix the comment (two state files). A leftover entry would fail both TestCwdOutsideARepo... and TestCwdInsideARepoIsUnchanged.

### `wt/cmd/wt-session/flip_test.go`
- :165 **delete**, TestEachModelNeedingPathRefuses table row {"rotate TAG", []string{"rotate","code"}, false, "nothing was pri... — Delete only this table row (function at line 149 stays). With `rotate` unregistered, `wt rotate code` would reach the root command, so the row cannot stay.

### `wt/cmd/wt-session/launch_remote_test.go`
- :596 **rewrite**, TestSeveralMatchesOpenThePickerOnTheServer comments: 'cursor lands on the rotation's next row' (593) and 'n... — Comment-only; logic (picker opens on a TTY, errModelPickerNeedsTTY without one, no agent built) is unchanged. Say 'cursor on the first unblocked row' and 'never a silent pick'.

### `wt/cmd/wt-session/launch_test.go`
- :1448 **keep**, TestLaunchRecordsUsageForAgent: runs launchFilteredRemote and reads usage via usage.NewStoreAt(...).CountsF... — Pins that the non-TUI launch path records usage through launch_remote.go's call; works unchanged after the swap to RecordLaunch. Good candidate to also assert last-picked.state == 'claude/opus' (optional).

### `wt/cmd/wt-session/main_test.go`
- :1062 **rewrite**, TestRootHelpListsModelSubcommands expected help words: "config", "litellm", "profile", "stats", "rotate" — Remove the "rotate" string only. Note substring match: nothing else in help contains 'rotate' guaranteed, so deleting it is correct. Consider also asserting help does NOT list a `rotate` command line. TestNoWtCommandIsHidden (1079) needs no change.

### `wt/cmd/wt-session/mode_gate_test.go`
- :307 **delete**, TestOneShotCommandsRefuseNamingWtConfigAndNeverOpenTheEditor table row {"rotate", []string{"rotate","code"}... — Delete only this table row (function at line 301 stays).

### `wt/cmd/wt-session/native_test.go`
- :128 **rewrite**, TestLaunchWithoutAServerRunsEveryAgentNatively: loop over {"rotation.state","last-picked.state","usage.json... — Drop "rotation.state" from the list (a never-written file would pass vacuously); doc comment 'leaves no rotation or usage record' becomes 'no launch history or usage record'. Keeps the native-launch-records-nothing guarantee.

### `wt/cmd/wt-session/profile_slots_manage_test.go`
- :348 **keep**, st.RecordFor("claude","profile/base") (348) and st.RecordFor(..."profile/old-name") (405) on usage.NewStore... — These call usage.StoreImpl.RecordFor (the usage-only append), not rotation. Unaffected as long as StoreImpl.RecordFor keeps its name and signature; if the implementer renames it, update these two lines, internal/usage/usage_test.go (about 12 calls), tui/modelrows_test.go:45-46 and tui/testhelpers_test.go:413.

### `wt/cmd/wt-session/resolve_remote_test.go`
- :30 **keep**, TestResolveModelRemoteNoPin (list named 'rotation list', t.Errorf 'rotation list = ...'), TestResolveModelR... — resolveModelRemote's second result (the registered, launchable, non-alias, non-subscription rows in Order) still feeds launch_remote.go:124 (several matches => picker or 'name -M' refusal), so these tests stay. Reword 'rotation list'/'rotation entry' in comments and failure messages (lines 26-27, 50, 183, 218) only if the source comment in resolve_remote.go:20 is reworded; no code change.

### `wt/cmd/wt-session/slot_launch_test.go`
- :75 **rewrite**, slotLaunchEnv doc: 'where usage.jsonl, rotation.state, last-picked.state would be written' — Comment only; drop rotation.state.
- :319 **rewrite**, TestLaunchSlotProfileRecordsTheProfile: seeds rotation.state with openrouter/b, launches profile/mix, asser... — Surviving behavior (profile launch leaves the marker on the profile, usage under the profile id). Remove the rotationState seed (lines 322-325) and the byte-identical assertion (342-344); instead seed a prior single-model marker (usage.NewStoreAt(stateDir).RecordLaunch('claude','openrouter/b')) so the test proves the profile launch moves the marker off it, and assert rotation.state does not exist. Fix doc comment ...
- :587 **rewrite**, TestBrokenSlotFileLeavesModelLaunchesAlone: rotationState() reads rotation.state and expects 'openrouter/a'... — Replace the closure and assertion (lines 616-635) with a read of last-picked.state (same expected 'openrouter/a'); update doc comment 'records the rotation' (line 582) and the t.Errorf text. Subtests: 'a TOML typo', 'a malformed name'.

### `wt/cmd/wt-session/stats_remote_test.go`
- :237 **delete**, TestRotateListsCatalogRowsByOrder: drives rotateCmd, writes agent-wt/rotation.state — Tests `wt rotate TAG` (removed command, rotateCmd, rotation.state).
- :284 **delete**, TestRotateSkipsSubscriptionRows: rotateModelsRemote(ctx, a, 'code') — rotateModelsRemote is deleted. The same rule is pinned for the surviving no-pin launch by TestResolveModelRemoteNoPinNeverPicksASubscriptionRow (resolve_remote_test.go:223). Check afterwards that fmt/bytes/filepath imports in this file are still used and that localRow/pointAtFake helpers keep other callers.

### `wt/cmd/wt-session/testmain_test.go`
- :92 **rewrite**, TestConfigHomeIsNotTheDevelopersOwn doc: 'rotation.state, usage.jsonl ... rewrites the developer's real rot... — Comment only. The isolation itself (config.IsolateConfigHomeForTest in TestMain, also tui/testhelpers_test.go TestMain) needs no change: last-picked.state lives under config.Dir() too. No test setup anywhere isolates a separate 'rotation directory'; no testmain/testhelpers code references the rotation package.

### `wt/internal/catalog/wire_test.go`
- :167 **check**, TestRotationSkipsAliasRows and TestLaunchableAndRegisteredOrdered (216): call catalog.RegisteredOrdered — RegisteredOrdered's only non-test callers are rotateModelsRemote (commands.go:70) and the picker's rotation cursor (models_client.go:290); both go away. resolveModelRemote does not call it. If the implementer deletes RegisteredOrdered (design: delete only if nothing else uses it), delete TestRotationSkipsAliasRows, TestRotationSkipsSubscriptionRows (wire_subscription_test.go:72) and the RegisteredOrdered half of T...

### `wt/internal/config/config_test.go`
- :463 **rewrite**, stale comments: 'DefaultTag drives which rotation group is used' (463), 'when the TUI or rotation tries to ... — Comment-only wording fixes (optional polish, no behavior): replace 'rotation' with 'the default-tag group' / 'no-pin launch' / 'launch history' as fits. No test logic changes.

### `wt/internal/depcheck/depcheck_test.go`
- :30 **delete**, sessionBanned list contains "internal/rotation" — Remove the string from the list (design already says so). usage is not banned for cmd/wt-model, so no replacement.

### `wt/internal/removal/deletionscan_test.go`
- :44 **delete**, fileOpAllowed entry "internal/rotation/rotation.go": {1, ...removes legacy rotation-*.state files...} — Delete the map entry. The scan fails otherwise ('fileOpAllowed names ... which is gone: drop the stale entry'). The new usage code needs no entry: it uses config.WriteFileAtomic only (usage.go has no os.Remove/Rename).

### `wt/internal/rotation/rotation_test.go`
- :1 **delete**, whole file: package rotation tests (27 Test* plus helpers eligibleFor, claudeCfg) — Delete the file together with wt/internal/rotation/rotation.go. Surviving behavior is re-created in wt/internal/usage (see the per-test rows below). Imports catalog, config, modelapi, usage disappear with it.
- :16 **delete**, TestLastMissingFile: Last() false when rotation.state absent — Last() is removed. The missing-file case is covered by the 'no state at all' row of the new usage.TestLastPicked.
- :25 **delete**, TestRecordAndLastRoundTrip: RecordFor then Last() — Tests rotation.state. The last-picked analogue is a round trip in usage.TestLastPicked / usage.TestRecordLaunchWritesLastPicked.
- :41 **move-to-usage**, TestRecordOverwrites: second RecordFor replaces the saved id — Rewrite as usage.TestRecordLaunchOverwrites: RecordLaunch('', 'alpha') then 'beta', LastPicked() == beta (read last-picked.state, not Last()).
- :54 **move-to-usage**, TestRecordWritesSingleLineAnd0600: rotation.state is 'alpha\n' mode 0600 — Fold into usage.TestRecordLaunchWritesLastPicked: assert last-picked.state is exactly 'id\n' with perm 0600 (drop the statePath()/rotation.state half).
- :76 **delete**, helpers eligibleFor and claudeCfg (lines 76 and 88) — Used only by the TestNext* and TestRecordProfileLeavesRotationStateAlone cases; go with them.
- :103 **delete**, TestNextReturnsFirstEligibleWhenEmpty — NextFromOrder removed.
- :120 **delete**, TestNextAdvancesAfterLast — NextFromOrder removed; this is the rotation-advance behavior being deleted.
- :137 **delete**, TestNextWrapsAround — NextFromOrder removed.
- :154 **delete**, TestNextSkipsIneligibleModels — NextFromOrder removed.
- :171 **delete**, TestNextReturnsFalseWhenNoModels — NextFromOrder removed.
- :192 **delete**, TestNextFallsBackToStartWhenLastUnknown — NextFromOrder removed.
- :209 **delete**, TestMigrationImportsNewestOldFile — The old per-slot rotation-*.state migrate is removed (design: leftover files stay on disk unread).
- :235 **delete**, TestMigrationReadsLegacyTwoLineFile — migrate removed.
- :249 **delete**, TestMigrationDoesNotOverwriteExistingState — migrate removed.
- :266 **delete**, TestFirstAfterMiddle — FirstAfter removed.
- :276 **delete**, TestFirstAfterWraps — FirstAfter removed.
- :286 **delete**, TestFirstAfterMissingTarget — FirstAfter removed.
- :296 **delete**, TestFirstAfterEmpty — FirstAfter removed.
- :304 **delete**, TestRotationNextFromOrder — NextFromOrder removed.
- :331 **move-to-usage**, TestRecordForAttributesUsageToAgent: usage event tagged with the agent — Surviving behavior (usage attribution). Rewrite as usage.TestRecordLaunchAttributesUsageToAgent: store := NewStoreAt(dir); store.RecordLaunch('claude','omlx/Qwen3.8-27B-4bit'); CountsForAgent('claude', ids).ThirtyDay == 1; replace the Last() assertion with LastPicked().
- :352 **delete**, TestNextFromOrderNeverPicksDiscovered — NextFromOrder removed.
- :372 **move-to-usage**, TestLastPicked: table of rotation.state vs last-picked.state — Rewrite as usage.TestLastPicked and drop the rotation column and the fallback. Cases: no file; a model id; a profile id; whitespace-only file reads as none (was: fell back to Last()); NEW case: only a leftover rotation.state present reads as none (design: no fallback, no marker until next launch). Update the comment, which currently justifies the fallback.
- :409 **move-to-usage**, TestRecordForWritesLastPicked: model launch after a profile writes both files, each 'ollama/a\n' 0600 — Rewrite as usage.TestRecordLaunchWritesLastPicked: RecordLaunch(profile) then RecordLaunch(model); last-picked.state == 'ollama/a\n' mode 0600; LastPicked() == ollama/a; ADD assertion that rotation.state does not exist (os.IsNotExist) so nothing writes it again. Keeps the 'marker moves off a profile once a model is launched' guarantee.
- :446 **move-to-usage**, TestRecordProfileLeavesRotationStateAlone: profile launch moves marker, counts under the profile id, leaves... — Rewrite as usage.TestRecordLaunchOfAProfileMovesTheMarker: drop every rotation.state/Last()/NextFromOrder/claudeCfg line; keep: last-picked.state == 'profile/mixed\n' mode 0600, LastPicked()=='profile/mixed', CountsForAgent('claude', profile/mixed)==1, and a model launched earlier keeps its own count of 1 (the profile launch is not a launch of a model). This is the 'profile launch leaves the marker on the profile'...
- :508 **delete**, TestRecordProfileDoesNotCreateRotationState — Only about rotation.state. Its 'profile launch on a fresh dir leaves the marker on the profile' half is covered by the rewritten TestRecordLaunchOfAProfileMovesTheMarker run on a fresh TempDir; its 'no rotation.state' half by the new assertion in TestRecordLaunchWritesLastPicked.
- :530 **move-to-usage**, TestRecordKeepsUsageWhenLastPickedFails: directory in last-picked.state's place; error returned, usage stil... — Rewrite as usage.TestRecordLaunchKeepsUsageWhenLastPickedFails: mkdir <dir>/last-picked.state; RecordLaunch('claude','ollama/a') and ('claude','profile/mixed') each return an error containing 'last-picked.state not saved' and each id still has ThirtyDay==1. Drop the 'rotation.state is written first' assertion and the separate RecordProfile call (one entry point now; keep two ids to cover model and profile).
- :559 **delete**, TestNextFromOrderMatchesRegistryRotation — NextFromOrder removed.
- :596 **delete**, TestNextFromOrderSkipsDiscovered (uses catalog.RegisteredOrdered) — NextFromOrder removed; also removes this test's only non-catalog caller of RegisteredOrdered.

### `wt/internal/tui/agent_model_test.go`
- :17 **rewrite**, tempStateDir doc: 'rotation.New/Last and the picker's marker read the global rotation.state' — Helper body stays (the marker read, usage.LastPicked(), still goes through config.Dir()/XDG_CONFIG_HOME). Reword the comment to last-picked.state / launch history.
- :34 **rewrite**, seedState: rotation.NewAt(dir).RecordFor("", modelID) — Replace with usage.NewStoreAt(dir).RecordLaunch("", modelID); fix the 'seed rotation state' message and comment. Same side effects as before (marker file plus one usage event), so sort tie-breaks keep their behavior. Drop the rotation import.
- :67 **rewrite**, phaseModelWithList helper: lastID from rotation.New().Last(); cursor via rotation.New().NextFromOrder(launc... — Mirror the new production rule: lastID, _ := usage.NewStore().LastPicked() (config.Dir()); delete the whole 'Rotation positions the cursor' block (lines ~126-135) and the idIndex map, leaving pos = first launchable row (the existing `first` variable). Fix the t.Fatal text ('host's real rotation.state') and the doc comment 'cursor on the globally rotated model'. Callers TestModelScreenMKeyIsNoOp (285), TestViewMode...
- :407 **rewrite**, TestSelectedEntryMsgPositionsCursorAtNextToUse: seed last=9b, expect cursor on 14b (the rotation's next) — Asserts cursor placement that now changes. Rename (e.g. TestSelectedEntryCursorStartsOnFirstUnblockedRow), keep seedState(9b) to prove a seeded last launch no longer moves the cursor, expect the first unblocked row (14b at index 0 under the current sort; re-derive after running). Update comment.
- :428 **rewrite**, TestPhaseModelWithListBuildsAndPositionsCursor: expects selectedModelID == ollama/gemma4:14b after seeding 9b — Item count (2) assertion stays. Cursor assertion becomes 'first launchable row' (helper no longer rotates); drop the rotation wording (Last()/NextFromOrder) in the comment. Re-check the expected id after the helper change.
- :454 **rewrite**, TestModelPickerMarksLastLaunchedRow: marker on 9b (index 1) and cursor on rotation-next 14b — Marker half is surviving behavior and stays (marker row index 1 = ollama/gemma4:9b, from last-picked.state). Cursor half: the 14b expectation happens to still equal the first unblocked row, but its message and comment say 'rotation-next'; reword to 'first unblocked row, not the marked row' so the marker-vs-cursor point survives.
- :492 **rewrite**, TestModelPickerNoMarkerWithoutRotationState (no seed, no marker anywhere) — Behavior survives (no last-picked.state => no marker). Rename to ...WithoutLastPickedState and fix comment/error text that name rotation.state and rotation.Last(). Consider adding a leftover rotation.state file in the isolated dir to pin 'no fallback' at the picker level.
- :526 **rewrite**, TestPhaseModelUpDownMovesCursor comment about the host's real rotation file positioning the cursor — Test logic unchanged (it Selects row 0 itself). Comment only: drop the rotation wording; tempStateDir stays for marker/usage isolation.
- :558 **rewrite**, TestNoRKeyInModelPhase: 'r' is a no-op; comment says rotation advances via launchAndRecord — Assertions stay (no in-picker key). Fix the comment to say 'launch history is recorded at commit, so the picker must ignore r'. Not rotation-dependent.
- :581 **rewrite**, TestSelectedEntryNoLastStartsAtZero (no state, cursor on first launchable row) — Assertion unchanged and now the general rule. Rename (TestSelectedEntryStartsOnFirstUnblockedRow) and drop the 'rotation positions the cursor only when a last-launched registry model exists' text.
- :603 **delete**, TestSelectedEntryPositionsAfterLast: seed 14b, running 9b/14b/design, expects cursor on gemma4:design (afte... — Tests removed behavior (cursor one past the last launch). Its replacement is the rewritten TestSelectedEntryMsgPositionsCursorAtNextToUse above (a seeded last launch does not move the cursor).
- :626 **rewrite**, TestSelectedEntryLastMissingFallsBackToZero: seed an id not in the registry, cursor on first launchable row — Assertion stays true; premise (rotation.Last guard) is gone. Keep as a marker/cursor test for an unknown last id: cursor on 14b and no row marked; fix comments mentioning rotation.Last.
- :646 **rewrite**, TestSelectedEntryLastDiscoveredIDFallsBackToFirstRow: seed a discovered id; comment about the registry guar... — Assertion (cursor on 14b at index 0) stays true. Reword the comment (no NextFromOrder registry guard any more); optionally merge with TestSelectedEntryLastMissingFallsBackToZero.
- :668 **delete**, TestSelectedEntryLastLastInListWrapsToZero: seed 14b, expects cursor index 2 (wrap to design) — Tests rotation wrap-around, removed behavior.
- :684 **delete**, comment block 'TestToggleTagRebuildsRotation was removed...' referencing rotation.Last/FirstAfter and other... — Historical tombstone comment naming FirstAfter and the deleted TestSelectedEntryPositionsAfterLast; remove the block (lines ~684-692).

### `wt/internal/tui/agent_picker_test.go`
- :113 **rewrite**, TestPhaseModelHonorsFilters comment: 'rotation state (via XDG_CONFIG_HOME - enterModelPhase positions the c... — Comment only; the isolation call tempStateDir(t) (line 118) is still needed for the marker and usage reads.

### `wt/internal/tui/app_test.go`
- :31 **rewrite**, comments mentioning rotation/rotation state: testConfig doc (30-31), lines 253 and 275 (tempStateDir 'so ro... — Comment-only edits (launch history / marker instead of rotation; the picker no longer reads state for cursor positioning, only for the marker). Calls to tempStateDir stay.
- :1366 **rewrite**, TestRemoteLaunchRunsTheAgentOnTheGateway: asserts rotation.New().Last() == launched model — Replace the Last() check (line ~1394) with usage.NewStore().LastPicked() == 'openrouter/glm-5.3' and add a CountsForAgent assertion (usage event recorded for the agent): this is the only TUI-level pin of remote_start.go's record() call. Fix the doc comment ('the rotation is recorded'). Replace the rotation import (line 23) with usage.

### `wt/internal/tui/model_line_test.go`
- :26 **check**, mockStore.RecordFor(agent, modelID string) error — Keep while usage.Store keeps RecordFor. If the implementer removes RecordFor from the interface (only StoreImpl.RecordLaunch writes now), delete this method and fix stubUsageStore's return type, whose callers (testhelpers_test.go:413, modelrows_test.go:45) call store.RecordFor.

### `wt/internal/tui/model_list_test.go`
- :180 **rewrite**, TestBuildTableMarksLastLaunchedRow / TestBuildTableNoMarkerWithoutLastLaunched (243) / TestBuildTableRefCol... — Behavior (the '> ' marker, lastID parameter of buildTable/renderTable) is unchanged and must stay. Comment-only: 'the rotation's last-launched ID' (174, 305) and 'no rotation.state' (240) become last-picked wording.

### `wt/internal/tui/models_client_test.go`
- :614 **rewrite**, TestPickerRotationCursorSkipsStartRows: omlx start row (order 1), a, b; RecordFor('claude','openrouter/b');... — Tests removed rotation behavior and contradicts the design (first unblocked row INCLUDES a start row). Rename (e.g. TestPickerCursorStartsOnTheFirstUnblockedRowEvenAStartRow), drop the rotation.New().RecordFor seed (or use usage.NewStore().RecordLaunch to prove a last launch does not move it), and assert the cursor is on the first row of the sorted table that is not blocked: the stopped omlx start row if it sorts ...

### `wt/internal/tui/pinned_flags_test.go`
- :129 **rewrite**, TestALaunchThatNeverCommitsLeavesRotationAlone: no rotation.state/last-picked.state after Enter, esc, or fa... — Surviving behavior (a launch that never commits records nothing). Rename (TestALaunchThatNeverCommitsRecordsNothing) and extend the file list to {"last-picked.state","usage.jsonl"}; keeping 'rotation.state' in the list is harmless but now meaningless, so drop it. Fix the comment ('rotation advances on cancelled launches').

### `wt/internal/tui/slot_cursor_test.go`
- :1 **delete**, whole file: helpers slotCursorChecks, slotCursorPicker(t, seed func(r *rotation.Rotation)), markedIDs, reco... — Verified by grep: nothing in the module calls slotCursorChecks, slotCursorPicker, markedIDs or record (they are dead since the tests were removed). Deleting the file removes the rotation import (line 11). If the slot-profile marker/cursor coverage is wanted, write a new test: launch profile/ok via usage.RecordLaunch, enter the picker, assert marker on profile/ok and cursor on the first unblocked row. No existing T...

### `wt/internal/tui/testhelpers_test.go`
- :276 **rewrite**, TestConfigHomeIsNotTheDevelopersOwn doc: 'everything wt keeps ... rotation.state, usage.jsonl ... rewrites ... — Comment only; list last-picked.state instead of rotation.state. TestMain's config.IsolateConfigHomeForTest is what isolates the directory and needs no code change. stubUsageStore (line 58) comment 'seed events via RecordFor' stays valid if StoreImpl.RecordFor stays.

### `wt/internal/usage/usage_test.go`
- :346 **check**, TestStoreInterfaceIncludesCountsForAgent plus the Store interface (usage.go:41) whose comment says 'rotatio... — The new tests go in usage_test.go (or a new usage/launch_test.go) using NewStoreAt(t.TempDir()); usage has no TestMain, so never call the config.Dir()-based NewStore() in a usage test. The Store interface keeps RecordFor because tui/testhelpers_test.go:413, tui/modelrows_test.go:45 and the mockStore in tui/model_line_test.go:26 call/implement it through usage.Store; put RecordLaunch on *StoreImpl (the design says ...

## Docs under wt/

### `wt/CHANGELOG.md`
- :7 **rewrite**, Unreleased/Changed: 'The cursor still starts where rotation puts it; the order is only what you see and wha... — This is in the Unreleased section so it IS editable and becomes false. Replace the last sentence with: 'The cursor opens on the first row that is not blocked (or the -M pin), so with rotation gone the alias band is where it starts after the native models and slot profiles.' Keep the first sentence (ordering rule) unchanged.
- :8 **rewrite**, insert after the #752 entry (new entry in Unreleased ### Changed, before the cleanup-list `R` entry on line 8) — Add two entries. (1) Changed, **Breaking**: 'Rotation is removed. The launcher picker's cursor no longer lands on the model after your last launch: it opens on the first row that is not blocked (a stopped local model or alias counts, Enter starts it), or on the -M pin. `wt rotate <tag>` is deleted (exit 1 as an unknown command). The `> ` marker still shows the last launched row, and usage.jsonl and the 1D/7D/30D c...
- :33 **keep**, Unreleased entry on LiteLLM plist writes: 'it rotates `LITELLM_MASTER_KEY` ... ' (and line 40 '(use the pli... — Key rotation, unrelated. Line 30 (native mode 'no rotation or usage record') is in the Unreleased section too, but it states a then-true fact about a released-in-this-cycle feature: reword only if the plan wants it exact ('no launch record'); it is not wrong after the change (rotation.state was the record). Safer: leave and let the new Breaking entry explain.

### `wt/CLAUDE.md`
- :11 **rewrite**, 3. **Which model** — ... or picked (eligible models rotate on successive launches). — Replace the parenthesis: 'or picked (the picker's cursor opens on the first row that is not blocked, or on the -M pin; the > marker shows the last launch)'. Nothing 'rotates on successive launches' any more.
- :95 **delete**, Packages list: `internal/{config,claudelogin,provider,tomlw,tiers,candidates,slotedit,cloudsync,rotation,us... — Remove the word `rotation,` from the brace list (package deleted); `usage` stays and now also owns last-picked.state. Check the rest of that long line for any 'rotation' mention (grep shows only this one).
- :112 **rewrite**, native-mode sentence: '...zero route, no client session, no rotation or usage record, no profile overlay' — Change 'no rotation or usage record' to 'no launch record (usage.jsonl, last-picked.state)'. Meaning unchanged: a native launch writes neither the usage event nor the marker. Same wording appears in wt/docs/internals/tui.md:13, wt/docs/wt-session.md:360, wt/docs/internals/launch-flow.md:117, wt/docs/wt-agents/README.md:246 and wt/CHANGELOG.md:30 (the last is an older entry: do not edit).
- :144 **rewrite**, Read docs/internals/local-models.md before changing row policy, the inventory probes, omlx loaded-state det... — Drop 'rotation,' from the list; if the Launch history section keeps a local-models.md anchor, say 'launch history (the marker)'.
- :167 **rewrite**, ## Rotation (Go)  (section lines 167-174) — Rewrite the whole section as '## Launch history (Go)'. Body: 'Each successful launch, a single model or a slot profile, calls usage.Store.RecordLaunch(agent, id): it writes ~/.config/agent-wt/last-picked.state (the row the picker's > marker sits on) and appends the agent-tagged event to usage.jsonl (best-effort; an error only when last-picked.state cannot be written). usage.LastPicked() reads last-picked.state onl...
- :169 **delete**, Each successful single-model launch records its id in `~/.config/agent-wt/rotation.state` (`rotation.Record... — Replace by the new first sentence given for the line-167 section (RecordLaunch writes last-picked.state and usage.jsonl; no cursor effect).
- :171 **delete**, - **Marker and pointer are two files**: `rotation.state` stays one bare registry id; ... `RecordProfile` ..... — Whole bullet goes: rotation.state no longer exists, RecordProfile is folded into RecordLaunch.
- :172 **keep**, - **The sort order is also the default selection** — changing `sortRows` changes what a bare Enter picks. — Now more true than before (the cursor opens on the first unblocked row of the sorted list). Optionally append: 'the launcher picker's cursor opens on the first row that is not blocked, so the sort order is the default there too'.
- :174 **rewrite**, Details and pinned tests: [docs/internals/local-models.md](docs/internals/local-models.md#rotation-go). — Point at the renamed heading of local-models.md (e.g. #launch-history-go). This is the only inbound #rotation-go link in the markdown (grep'd). check-links strips fragments, but keep it correct.
- :320 **rewrite**, Alias commands bullet: '...a no-pin launch never chooses or rotates onto an alias (`resolveModelRemote` ski... — Say 'a no-pin launch never chooses an alias (resolveModelRemote skips alias rows)'. Drop 'or rotates onto' and the `catalog.RegisteredOrdered` reference; if RegisteredOrdered is deleted in code (design leaves that to the builder) it must not stay named here. The picker does list aliases and opens its cursor on one when it is the first unblocked row.

### `wt/README.md`
- :3 **rewrite**, Launch AI coding agent CLIs in git worktrees with model rotation. — 'Launch AI coding agent CLIs in git worktrees on the model you pick.' (or drop the clause).
- :5 **rewrite**, picks a worktree or branch, rotates models by tag, and launches the selected agent — 'picks a worktree or branch, picks a model (filterable by tag), and launches the selected agent'.
- :9 **rewrite**, | Launcher | Agent | Model rotation |  (table header, rows 10-16 are Yes/No) — Rename the column to 'Model picker' (or 'Models') keeping Yes/No: it means the agent is model-driven. Do not keep 'rotation'.
- :63 **rewrite**, | `enter` | Select worktree → agent+model screen → launch (and advance rotation) | — Drop '(and advance rotation)'; optionally '(records the launch for the > marker and usage)'.
- :92 **rewrite**, Model rotation happens inside the TUI — there are no `--code`/`--design`/`--native` flags. Use `-T <tag>` t... — 'Model choice happens inside the TUI — there are no --code/--design/--native flags. Use -T <tag> to filter to a tag group, then up/down to pick a model; the cursor opens on the first row that is not blocked.' Delete the 'advances the rotation' clause.
- :114 **check**, ...wt-owned state — Agents (...) and the default rotation tag, plus the legacy `[launch]` table — 'default rotation tag' describes a legacy config.toml key. Verify in internal/config whether such a key is still parsed (grep 'rotation' in config structs); if not, drop 'and the default rotation tag'. Otherwise rewrite as 'the default model tag'. Not a rotation.state reference.
- :122 **keep**, legacy bash models.conf migration: '...merging models in both code and design rotations' — Describes the legacy bash models.conf (CODE_MODELS/DESIGN_MODELS) import, a different thing from rotation.state. Leave; it could confuse a grep.
- :140 **rewrite**, ## Model rotation  (section lines 140-153: intro paragraph 144, four bullets 146-149, code block 151-153 wi... — Replace the section with '## Launch history' (or delete the section and its Works-for sentence). Content: 'Every launch records the row in ~/.config/agent-wt/last-picked.state (the row the picker's > marker sits on, a slot-profile launch included) and appends a usage event to usage.jsonl (the 1D/7D/30D columns, wt stats). The picker's cursor does not depend on it: it opens on the first row that is not blocked, or ...
- :147 **rewrite**, - `~/.config/agent-wt/last-picked.state` holds the row the picker's `> ` marker sits on. Every launch write... — Keep the bullet, drop ', which leaves `rotation.state` alone'.
- :225 **check**, | `cmd/wt-session/commands.go` | Subcommand constructors: `rotate` (debug helper) | — Delete the row if commands.go has no other constructor after `rotate` is removed (the Go side deletes rotateModelsRemote; confirm whether the file or other constructors remain: if the file is deleted, the row goes; else reword to what remains).
- :229 **delete**, | `internal/rotation/` | Tag-based model rotation with snapshot-based model set and persistent state | — Delete the row. Optionally add to the `internal/usage/` row, if README has one, 'launch history: last-picked.state marker and usage.jsonl'.
- :263 **rewrite**, - `cmd/wt-session/` — unified Go tool: cobra CLI, Bubble Tea TUI, model registry, rotation, guard, init see... — Drop 'rotation, '.

### `wt/docs/internals/launch-flow.md`
- :19 **rewrite**, - **No launch state is derived from git.** `rotation.state` and `last-picked.state` hold a model id, `usage... — '`last-picked.state` holds a row id' (drop 'rotation.state and'; 'hold' becomes 'holds'). Rest of the sentence unchanged ('it writes the same files as a launch inside one').
- :31 **rewrite**, 1. `resolveModelRemote` (...). Several and no pin: `rotation.NextFromOrder` over the launchable registered ... — Stale vs code (resolveModelRemote errors 'multiple models match for agent X' on several launchable rows). New: 'Several and no pin: the launch refuses with "multiple models match for agent X" (the picker is the way to choose; nothing is picked for you). Alias and Claude subscription rows (#597) are never the no-pin choice.' Delete 'never rotate' and the NextFromOrder/RegisteredOrdered text. Keep the subscriptionGu...
- :34 **rewrite**, 4. `agents.BuildLaunchCmdGateway` ... `rotation.RecordFor`, then `runLaunch` with `runTail{...}`. — Replace '`rotation.RecordFor`' with '`usage.RecordLaunch(agent, m.ID)` (last-picked.state and the usage event)'. Matches cmd/wt-session/launch_remote.go:183.
- :90 **rewrite**, - **Recording** is one call at the commit point, after the applier succeeded: `rotation.New().RecordProfile... — '`usage.RecordLaunch(agent, row.Model.ID)` (the usage event and `last-picked.state`)'. Delete '; never `rotation.state`'. Keep the failed-write sentence (a note: line, no abort).
- :117 **rewrite**, nothing is created, claimed, recorded or summarised — no rotation or usage state, no summary line or survey — 'no last-picked or usage record' (replace 'no rotation or usage state'). Rest unchanged.
- :142 **rewrite**, > **A launch that has a directory needs no repository.** ... gates the guard install and nothing else: rota... — 'the launch record (last-picked.state and usage.jsonl) is keyed by model and agent, never by a repository or branch'.

### `wt/docs/internals/litellm-routes.md`
- :38 **keep**, 'so rotating that key replaces keyed rows once' (the proxy master key) — Key rotation, unrelated.

### `wt/docs/internals/local-models.md`
- :1 **rewrite**, # wt local models: rows, inventory, lifecycle, rotation — Drop ', rotation' from the title; optionally ', launch history'.
- :24 **rewrite**, **Non-TUI** ...: '— rotation never hands an agent a server that is not up, a start row is never auto-select... — Docs are stale vs code: resolveModelRemote (cmd/wt-session/resolve_remote.go:60-92) returns launchable[0] for one row and errors 'multiple models match for agent X' for several; it never rotated (the design says so too). Rewrite: 'with no -M, only launch rows of the server's catalog are eligible (catalog.Launchable): a server that is not up is never handed to an agent, a start row is never auto-selected (only a pi...
- :76 **rewrite**, ## Rotation (Go)  (lines 76-81 intro + marker paragraph) — Rename to '## Launch history (Go)' (this is the target of wt/CLAUDE.md:174's anchor).
- :78 **rewrite**, Global rotation: each successful single-model launch records its model id in `~/.config/agent-wt/rotation.s... — New text: 'Every successful launch, a single model or a slot profile, calls usage.Store.RecordLaunch(agent, id): it writes the row id to ~/.config/agent-wt/last-picked.state (atomic write) and appends the agent-tagged usage event to ~/.config/agent-wt/usage.jsonl. Nothing positions the picker's cursor from it: the cursor opens on the first row that is not blocked, or on the -M pin. Callers: launch_remote.go, slot_...
- :80 **rewrite**, **The marker and the rotation pointer are two files.** `rotation.state` is one bare registry id, read whole... — Replace with one short paragraph: '**The marker file.** last-picked.state (one line, a row id) is where the picker's > marker comes from (usage.LastPicked(), which reads this file only: with no file the marker is absent until the next launch). A slot-profile launch writes profile/<name> through the same RecordLaunch, so the marker moves to the profile row and a single-model launch moves it back. A leftover rotatio...
- :82 **rewrite**, ...The launcher's client picker places its cursor by rotation instead (next bullet but one), so native-firs... — Replace 'places its cursor by rotation instead (next bullet but one), so native-first is not its default' with 'opens its cursor on the first row that is not blocked (Starting cursor bullet below), so there the sort order, native, slot profiles, aliases, running local, other, is the default too'. Keep the rest, and the TestPickStartModelDefaultsToNativeRow reference.
- :83 **keep**, - The last-launched row gets a `> ` prefix — plain ASCII on purpose — Marker unchanged. Optionally append '(from last-picked.state via usage.LastPicked)'.
- :84 **rewrite**, ...the live client-session count of each row ..., before the rotation marker. — 'before the last-launched marker' (it is the `> ` marker, not a rotation marker).
- :88 **rewrite**, - Starting cursor (the client picker, `models_client.go`): a pinned row when `-M` named one; otherwise alwa... — New bullet: '- Starting cursor (the client picker, models_client.go): the row `-M` pinned when one was named; otherwise newModelList's own rule, the first row that is not blocked in the sorted order (so native, then slot profiles, then aliases, then running local, then others). A start row (a stopped local model or alias) counts as not blocked: Enter on it is the decision to start it. If every row is blocked the c...
- :91 **delete**, ```bash go run ./cmd/wt-session rotate code    # debug helper: print the model after the last-launched in t... — Delete the whole fenced block (the only content of the fence is the removed `rotate` command); also check the blank lines around it so the bullet list before it (TUI callers fetch the agent's full catalog...) still ends cleanly. Line numbers: the grep hit is line 92, the fence opens at 91 and closes at 93.
- :123 **rewrite**, - **The marker and the pointer are two files.** `rotation.state` stays one bare registry id. `last-picked.s... — This is in the slot-profile rows section. Replace with: '- **The marker.** last-picked.state holds the row the > marker sits on (usage.LastPicked). A slot-profile launch records profile/<name> through RecordLaunch: it writes the marker and the usage event.' Delete the rotation.state / fallback to Last / 'never rotates' wording.

### `wt/docs/internals/packages.md`
- :24 **check**, | `cmd/wt-session/commands.go` | `rotate` subcommand (debug helper) | — Delete the row if commands.go is deleted by the Go change; otherwise reword to the constructors that remain (rotate is gone).
- :60 **move-to-usage**, | `internal/rotation/` | global rotation state (`rotation.state`) + next-model selection; `last-picked.stat... — Delete this row (package deleted) and fold its surviving content into the next row (line 61, internal/usage).
- :61 **rewrite**, | `internal/usage/` | append-only JSONL launch history (1d/7d/30d); `RecordFor` tags the agent; `CountsForA... — Replace '`RecordFor` tags the agent' with '`RecordLaunch(agent, id)` is the one recording call of every launch (a single model or a slot profile): it writes `last-picked.state` (the picker's `> ` marker row) and appends the agent-tagged event, best-effort on the event, an error only when last-picked.state cannot be written; `LastPicked()` reads last-picked.state only (no fallback to the removed rotation.state)'. K...
- :134 **rewrite**, `cmd/wt-model` must not link the session half (`sessionBanned`): `tui`, `cleanup`, `agents`, `rotation`, `r... — Remove `rotation` from the list. internal/depcheck/depcheck_test.go (line 30, not in my scope) holds the same list; `usage` is in otherPackages (line 182), so after the fold nothing now bans wt-model from linking usage: keep it in otherPackages unless the plan decides to add usage to sessionBanned (the old rotation ban meant wt-model never linked last-picked/rotation code; if cmd/wt-model must still not write last...

### `wt/docs/internals/testing.md`
- :11 **rewrite**, Before this, every run of `internal/tui` rewrote the real `rotation.state` with a test model and appended `... — This is a historical explanation of why the tests isolate config. Change `rotation.state` to `last-picked.state` ('rewrote the real last-picked.state with a test model') so the sentence names a file that still exists; it stays true in spirit for the TestMain isolation.
- :34 **rewrite**, `AllCounts` never reads `internal/usage`'s own unexported `now` (that clock stamps `RecordFor` and feeds th... — Replace `RecordFor` with `RecordLaunch`.

### `wt/docs/internals/tui.md`
- :13 **rewrite**, (a) '...no client session, rotation or usage record), or, for a command agent...' (native launch) — 'no client session, no launch record (last-picked.state, usage.jsonl)'.
- :13 **rewrite**, (b) '`handlePrepared` hands the built command to the run command (`runRecordAndWaitWith`), which records th... — 'which records the launch (usage.RecordLaunch) once the launch gate is past'. The Go function runRecordAndWaitWith may be renamed by the Go change; keep the doc in step.
- :13 **rewrite**, (c) 'The picker's rotation cursor and the non-TUI endpoint launch rotate over the same list: the launchable... — Delete the first sentence ('The picker's rotation cursor and the non-TUI endpoint launch rotate over the same list...'). Replace with: 'The picker's cursor opens on the -M pin, else on the first row that is not blocked in the sorted list (a start row counts: Enter on it starts it).' Keep the 'Without a terminal ... refuses (pickerNeedsTTYError)' sentence as is: it is the true non-TUI behaviour. The line is a very ...
- :35 **rewrite**, > **TTY required.** ... Flag paths (`--version`, `wt rotate`) skip the TUI. — 'Flag paths (`--version`) skip the TUI.' (wt rotate is deleted; check whether another flag path, e.g. `wt model list --server`, should replace it: it is the same kind of non-TUI path, but the original only listed two, so just drop `wt rotate`).

### `wt/docs/wt-agents/README.md`
- :124 **rewrite**, The launcher contract (flags, rotation, install) lives in the Go tool — '(flags, model picking, install)'.
- :175 **rewrite**, With no flags, `wt` presents the worktree picker, then the agent+command picker, then the model picker (for... — Delete the sentence 'Multiple eligible models rotate on successive launches via a single global rotation state file.' (lines 175-176) and replace it with 'The model picker's cursor opens on the first row that is not blocked (or the -M pin); the > marker shows the last launch.' Keep 'Each picker is skipped only when ...' as is.
- :246 **rewrite**, (agent and model pickers, `-M` pins and slot profiles, the model server's gateway, automatic profiles, rota... — 'the launch record (last-picked.state and usage.jsonl), the summary afterwards' in place of 'rotation and usage records, the summary afterwards'.
- :395 **rewrite**, Model rotation is now a single global sequence (implicit on launch in the TUI, or `wt rotate <tag>` for deb... — This is a 'Legacy notes' paragraph. Rewrite the rotation sentences: 'Model rotation was retired: the picker's cursor opens on the first row that is not blocked, and `wt rotate <tag>` is gone. The earlier per-slot (agent, tag, family) scheme was retired before that; ...' and 'The pi models.json auto-sync behavior described in earlier versions of this doc has been ported to Go.' (drop 'bash model-rotation and'). Kee...

### `wt/docs/wt-agents/agy-wt.md`
- :5 **rewrite**, See [README](README.md) for launcher flags and model rotation. — 'for launcher flags and model picking.' Same one-word change in: wt/docs/wt-agents/copilot-wt.md:5, opencode-wt.md:5, pi-wt.md:5, codex-wt.md:5, claude-wt.md:5 (all line 5, identical trailing phrase 'launcher flags and model rotation').

### `wt/docs/wt-agents/claude-wt.md`
- :5 **rewrite**, See [README](README.md) for launcher flags and model rotation. — 'model picking' instead of 'model rotation'.
- :63 **rewrite**, wt never chooses or rotates onto one by itself (pin it with `-M`, pick it in the picker or name it in a slo... — 'wt never chooses one by itself' (drop 'or rotates onto').

### `wt/docs/wt-agents/codex-wt.md`
- :5 **rewrite**, See [README](README.md) for launcher flags and model rotation. — 'model picking' instead of 'model rotation'.

### `wt/docs/wt-agents/copilot-wt.md`
- :5 **rewrite**, See [README](README.md) for launcher flags and model rotation. — 'model picking' instead of 'model rotation'.

### `wt/docs/wt-agents/opencode-wt.md`
- :5 **rewrite**, See [README](README.md) for launcher flags and model rotation. — 'model picking' instead of 'model rotation'.

### `wt/docs/wt-agents/pi-wt.md`
- :5 **rewrite**, See [README](README.md) for launcher flags and model rotation. — 'model picking' instead of 'model rotation'.
- :98 **keep**, 'an enterprise one with a rotating credential' and line 114 'a rotated credential is picked up mid-session' — Credential rotation, unrelated.

### `wt/docs/wt-agents/shell-wt.md`
- :86 **rewrite**, Legacy bash flags `--code`, `--design`, and `--native` are not supported by `wt` ... since model rotation i... — Replace 'model rotation is a single global sequence and shell has no model concept' with 'wt picks the model in its picker and shell has no model concept'. Check the full sentence start at line 84-86 so the 'since' clause still reads.

### `wt/docs/wt-agents/slot-profiles.md`
- :307 **rewrite**, **The `> ` marker** sits on the row you launched last, a profile row included. When that row is a profile t... — The cursor sentence is now WRONG: the cursor no longer starts on a last-launched profile (the cursor opens on the first row that is not blocked, or the -M pin; the picker never repositions it from the marker). New bullet: '**The `> ` marker** sits on the row you launched last, a profile row included. It only marks: the cursor opens on the first row that is not blocked (a profile row is first unblocked after native...
- :341 **rewrite**, `wt start`, `wt smoke` and rotation ignore slot profiles. — '`wt start` and `wt smoke` ignore slot profiles.' (drop 'and rotation'). Sentence before it says a no-pin launch never chooses a profile, keep it.
- :552 **delete**, | `rotation.state` | Nothing. It stays the id of the last single-model launch |  (table in 'What wt records... — Delete this table row.
- :553 **keep**, | `last-picked.state` | `profile/<name>`. This file holds the picker's `> ` marker; a single-model launch w... — Still true. Optionally say 'written by the same RecordLaunch call as the usage event'.

### `wt/docs/wt-config.md`
- :210 **delete**, ├── rotation.state       # the last single-model launch: the rotation position — Delete this line of the ~/.config/agent-wt/ tree. If the doc wants to flag the leftover, add a comment on the next line only if it is brief ('a leftover rotation.state is not read'); prefer deleting. Tree alignment of the comment column must stay.
- :211 **keep**, ├── last-picked.state    # the row launched last, model or slot profile: the picker's marker — Still true. Optionally also mention it is the only launch pointer and that usage.jsonl sits beside it, if that file is listed in the tree.

### `wt/docs/wt-model-server.md`
- :160 **keep**, To rotate, write a new file (the same command), restart `wt-model serve` (also 372-373, 436 'Renewal and ro... — All of these are token / LITELLM_MASTER_KEY / certificate rotation. Not model rotation; do not edit. Listed so a grep is not mistaken.
- :856 **rewrite**, a launch with no `-M` counts it among the launchable models (one model plus an alias over it is then "multi... — Describes an older client treating an alias as a model. Replace 'and may rotate onto it' with nothing (the no-pin launch refuses on several matches, it never rotated); the sentence ends '...is then "multiple models match"), and a launch of it goes to the gateway under the alias id...'. Mind the grammar: '(one model plus an alias over it is then "multiple models match"), and a launch of it goes ...'.

### `wt/docs/wt-model.md`
- :190 **rewrite**, The id, the provider and the name cannot be changed afterwards: usage history, rotation and launch profiles... — 'usage history, the last-launched marker and launch profiles key on the id.' (drop 'rotation').
- :576 **rewrite**, **Launching.** ... A launch with no pin (and rotation) never picks an alias: it walks the registered models... — 'A launch with no pin never picks an alias: it launches the one launchable registered model, or refuses when several match. The picker lists aliases in their own band after the slot profiles and can open its cursor on one.' Drop '(and rotation)' and 'walks the registered models only'.

### `wt/docs/wt-session.md`
- :126 **delete**, `nothing was printed` (`wt rotate TAG`), `nothing was listed` — Remove the '`nothing was printed` (`wt rotate TAG`), ' entry from the per-command list of what-did-not-happen clauses. Check the same list in cmd/wt-session/remote_errors (Go side) so the doc and the code lose it together.
- :186 **keep**, `; rotate the token on the host (wt-model setup)`  and line 192 'Rotate the token when the server is on ano... — Token rotation, unrelated to model rotation. Not a site; only a grep false positive.
- :335 **rewrite**, - `wt smoke`, `wt rotate TAG`, `wt model list --server`;  (commands that refuse in native mode) — '- `wt smoke`, `wt model list --server`;'
- :360 **rewrite**, The agent gets no gateway origin or token (the zero route), no rotation or usage record is written (there i... — 'no last-picked or usage record is written (there is no model id)'.
- :376 **rewrite**, `smoke`, `rotate`, `model list --server` and the slot-profile commands that need a server refuse as before. — Drop '`rotate`, ': '`smoke`, `model list --server` and ...'.

### `wt/docs/wt-smoke.md`
- :29 **rewrite**, usage, cost, tags, rotation marker) the `wt` agent flow's model picker renders — 'last-launched marker' in place of 'rotation marker'.

### `wt/docs/wt-start-stop.md`
- :571 **rewrite**, without one the server's one launchable model launches, and when several match the next one after the last ... — Already false vs code. Replace by 'and when several match the launch refuses ("multiple models match") and names -M; pick one in the picker or pin it'. Check the exact refusal text in cmd/wt-session/resolve_remote.go:90 (`multiple models match for agent %q`).

## Docs and files outside wt/

### `CLAUDE.md`
- :51 **rewrite**, wt bullet: "rotation/usage state (`rotation.state`, `usage.jsonl`, `last-picked.state`)" — Replace with "launch history (`usage.jsonl`, `last-picked.state`)". Same line also says "Owns ... rotation/usage state (..., `rotation.state`...)" only once; also check the same bullet's text "rotation.state" appears nowhere else. Mention nothing of `internal/rotation`.

### `README.md`
- :13 **rewrite**, `wt/` row: "worktree agent launcher with model rotation" — Change "with model rotation" to "with a model picker" (or drop the phrase): "worktree agent launcher with a model picker (`wt` binary + `*-wt` shims); ...".
- :25 **rewrite**, guide index row: "families, tags, and wt's model rotation" (03-model-families.md) — Change to "families, tags, and how wt's picker uses them" (match the retitled guide 03 heading).

### `bin/check-config-dirs-untouched`
- :8 **rewrite**, comment: "that same test rewrites live state (wt's rotation.state and usage.jsonl were being overwritten by... — Comment only, historical (#221). Safe to keep as history; if kept consistent with the removal, change to "wt's last-picked.state and usage.jsonl" is NOT accurate for #221's time, so prefer keep. Recommended: keep (action effectively keep). No code in the script reads rotation.state; it only checks directory existence.

### `docs/guides/00-config-map.md`
- :26 **rewrite**, config.toml table row: "Agents, default rotation tag, `[model-server]` ..." — "default rotation tag" -> "default tag (`default_tag`, the picker header's tag slot)". default_tag itself stays (it still labels the picker header and `-T` fallback).
- :28 **check**, models.conf row: "Legacy bash rotation config" — Describes the pre-wt bash-era file (CODE_MODELS rotation arrays); historical description of that legacy file, not wt's rotation. Keep, or soften to "Legacy bash model-list config". Lines 216 and 221-224 ("legacy rotation + provider config", "selected rotation array", "Coding rotation") describe the same legacy file and its verbatim example: keep as is (check: fine to leave).
- :30 **delete**, table row: `~/.config/agent-wt/rotation.state` + `rotation-*.state` | wt | wt | Rotation position — Delete the row. Optionally add one sentence in the section below (or in the last-picked row) that a leftover `rotation.state` / `rotation-*.state` is no longer read or written and may be deleted by hand; wt never deletes it.
- :31 **keep**, table row: `last-picked.state` ... the model picker's `> ` marker — Keep; the file is now written by the single RecordLaunch call in internal/usage. No wording change needed unless the section below changes (see line 256-260).
- :182 **rewrite**, "Purpose: agents and default rotation tag (`default_tag = \"code\"`)" — "default rotation tag" -> "default tag". Line 190 (`default_tag = "code"` example) and line 343 (trap note naming `default_tag`) stay unchanged.
- :244 **delete**, Section heading "### `~/.config/agent-wt/rotation.state` (+ `rotation-*.state`)" with Owner/Consumers/Purpo... — Delete the whole section (heading, 3 bullets, the `ollama/glm-5.3-flash:cloud` example block). Replace with at most a one-line note under the last-picked section: "An old `rotation.state` or `rotation-*.state` on disk is inert: wt neither reads, writes nor deletes it."
- :256 **rewrite**, `last-picked.state` section: Consumers "(the model picker's `> ` marker, and the starting cursor when the l... — Consumers: only "the model picker's `> ` marker" (the cursor no longer starts on a slot profile; it starts on the first unblocked row or the -M pin). Purpose: "one line, the id of the row launched last, a model or a slot profile. Written by every launch that records (a model launch and a slot-profile launch). When the file is missing there is no marker until the next launch; wt does not fall back to `rotation.stat...
- :346 **rewrite**, "`rotation*.state` / `usage.jsonl` after the first `wt` launch" — Change to "`last-picked.state` / `usage.jsonl` after the first `wt` launch".

### `docs/guides/02-providers-and-models.md`
- :132 **rewrite**, `tags = []                      # e.g. ["code"] — rotation groups, guide 03` — -> `# e.g. ["code"] — tag groups for -T, guide 03` (keep the column alignment).

### `docs/guides/03-model-families.md`
- :1 **rewrite**, Title "# Model families — grouping, display names, tags, and wt rotation" — Drop ", and wt rotation" (e.g. "# Model families — grouping, display names, and tags"). Also check the guide's own front-matter/`> Use this to:` blockquote and the README.md line 25 summary for the same phrase.
- :26 **rewrite**, table row: "`tags` per model (rotation groups, e.g. `code`/`design`) ... `wt` (`-T` filter, tag rotation)" — "rotation groups" -> "tag groups"; consumers "`wt` (`-T` filter, tag rotation)" -> "`wt` (`-T` filter)".
- :64 **rewrite**, "Nothing reads them: wt shows and rotates families by their id." — "shows and rotates families by their id" -> "shows families by their id".
- :68 **rewrite**, "*any* tag is a valid rotation group" — -> "*any* tag is a valid filter group for `-T`".
- :74 **rewrite**, "no model is tagged yet and tag filters/rotation have nothing to match" — Drop "/rotation": "tag filters have nothing to match".
- :102 **rewrite**, "...and the rotation advances on the launch that follows (see 06 §2). ... it labels the rotation slot, not ... — Remove "and the rotation advances on the launch that follows (see ... §2)"; "labels the rotation slot" -> "labels the tag slot"; replace the last sentence with "The picker cursor starts on the first row that is not blocked (or on the `-M` pin), in the picker's sort order (06 §2); `enter` launches or starts; there is no `r` re-roll key." Keep the footer text and the default_tag explanation.
- :104 **delete**, "### 5. Rotation behavior" through the end of the section (lines 104-182): rotation.state example, UNVERIFI... — Delete the whole Step 5 (everything from the `### 5. Rotation behavior` heading to just before `## Gotchas`). If a replacement is wanted, a 2-3 line "### 5. Launch history" saying: every launch writes `last-picked.state` (the `> ` marker) and appends to `usage.jsonl` (06 §8); nothing rotates; leftover `rotation*.state` files are unread. Also fix any other step numbering / Verification list / table of contents in t...
- :185 **rewrite**, Gotcha: "**Tags and families drive agent rotation — editing them changes what `wt` offers next launch.** Th... — Rename to "**Tags and families drive what `wt` offers — editing them changes the next launch.**"; delete the sentence about the cursor starting after `rotation.state`'s id / on the last slot profile; keep the "narrow the sets too far and you get `no models for agent ...`" and the `-T`/`-F` hides slot profiles sentences.
- :186 **rewrite**, "Display names are read by nothing. They never change what `wt` shows or rotates." — "shows or rotates" -> "shows".
- :189 **rewrite**, "TUI behavior: there is no `d` tag-toggle key, `rotation.state` is a single global slot, and per-tag `rotat... — Keep "there is no `d` tag-toggle key"; drop the `rotation.state` and `rotation-<tag>.state` clauses; make it "The `> ` marker in the picker is read from `last-picked.state` (one line, the row launched last)."
- :190 **rewrite**, Gotcha: "Symptom: `wt rotate code`/`design` return models although `registry.toml` has no tags: the old bui... — The symptom uses the removed command as its probe. Rewrite the symptom to something observable without it: e.g. "Symptom: the picker lists tagged models (or `-T code` narrows the list) although `registry.toml` has no tags". Rest of the gotcha (rebuild with `make install` from wt/) stays.
- :194 **rewrite**, "wt README (flags, TUI keys, rotation, config split): ...wt/README.md" — Drop "rotation, " from the parenthetical (wt/README.md loses its "Model rotation" section in the wt-scope change).

### `docs/guides/04-litellm-config.md`
- :204 **rewrite**, "`wt start <artifact>` ... a `new` row with no family or tags, which `-T`/`-F` hide and rotation never picks." — Drop "and rotation never picks": "which `-T`/`-F` hide".

### `docs/guides/06-wt-agents-and-models.md`
- :3 **rewrite**, "> Use this to: ... understand what the picker offers, where rotation state lives, and which flags skip whi... — "where rotation state lives" -> "where launch history lives (`last-picked.state`, `usage.jsonl`)".
- :25 **rewrite**, "which decide picker contents and rotation order" — -> "which decide picker contents"; (order is tier/running, not tags).
- :31 **rewrite**, `claude-wt                    # TUI: pick worktree/branch + model (advances rotation)` — Drop "(advances rotation)": "# TUI: pick worktree/branch + model".
- :49 **rewrite**, Shim matrix header `| Launcher | Agent | Model rotation |` with Yes/No per launcher (lines 49-57); text say... — Rename the column to match whatever wt/README.md's table becomes (suggest "Model picker" / "Model layer"), and drop "verbatim" wording only if the wording diverges. Coordinate with the wt-scope agent's rename of the wt/README.md table (wt/README.md:9) and the agents-table field named rotating (internal/agents); keep the Yes/No values.
- :73 **rewrite**, Long picker paragraph, last sentences: "The cursor starts on the rotation's next model (see §5): the next l... — Replace those two cursor sentences with: "The cursor starts on the first row that is not blocked in the order above (a stopped local model or alias counts: Enter on it starts it), or on the `-M` pin; `> ` marks the row launched last (`last-picked.state`, §5). With the sort above that first row is the native row, then a slot profile, then an alias, then running local models." Keep the next sentence about `wt smoke`...
- :99 **rewrite**, "With no `-M`: one launchable model → that model; several → the rotation cursor decides (§5)." — -> "several → the picker opens, its cursor on the first row that is not blocked (§2)". The remainder (launchable definition, error text, source refs resolve_remote.go/launch_remote.go) stays; verify against the wt-scope change that resolveModelRemote no longer returns a rotation list.
- :153 **rewrite**, "### 5. Rotation" section: lines 155 ("Launching a model advances rotation..."), 156 (state file `rotation.... — Retitle "### 5. Launch history" (or "The `> ` marker") and keep only: (1) every launch that records (a model launch or a slot-profile launch, through the picker or not) writes `~/.config/agent-wt/last-picked.state` and appends to `usage.jsonl` (§8) in one call (`usage.Store.RecordLaunch`); `esc`/canceled prompts write nothing; (2) the picker's `> ` marker reads `last-picked.state`: the row launched last, a model o...
- :220 **rewrite**, "Every model launch appends one line to usage.jsonl (`Rotation.RecordFor()` → `usage.Store.RecordFor()`)" — -> "(`usage.Store.RecordLaunch()`, which also writes `last-picked.state`)"; note a slot-profile launch also appends there if the guide's later text says otherwise (check §10: the existing JSON record has model_id/agent/timestamp).
- :319 **rewrite**, "Slot profiles are reached from the picker and the pin only. Rotation, `wt start` and `wt smoke` ignore the... — -> "`wt start` and `wt smoke` ignore them" (drop "Rotation,").
- :330 **rewrite**, "wt never picks or rotates onto one by itself: pin it with `-M`..." — -> "wt never picks one by itself" (drop "or rotates onto").
- :343 **delete**, Verification list item: "`wt rotate code` → one model id (example: `ollama/kimi-k2.7-code:cloud`), exit 0, ... — Delete the bullet (the command is removed). Optionally replace with "`wt rotate code` -> unknown command, exit 1" only if the wt-scope change leaves cobra's unknown-command error; do not add it otherwise.
- :355 **rewrite**, Gotcha: "**`agy-wt` and `shell-wt` have no rotation** (no model layer at all); `--yolo` is a no-op for pi." — -> "**`agy-wt` and `shell-wt` have no model layer** (no model picker at all)"; keep the --yolo clause.
- :357 **rewrite**, Gotcha: "**Tag rotation is latent while models are untagged:** ... `-T`-less rotation walks the whole list ... — Retitle "**Tag filtering is latent while models are untagged:**" and replace "`-T`-less rotation walks the whole list" with "a `-T`-less launch lists every model"; keep the grep commands and the `wt-model model edit <id> --tags code` advice.
- :361 **rewrite**, Gotcha: "**`rotation.state` is written by single-model launches only** (`RecordFor()`); the `wt rotate <tag... — Replace with: "**`last-picked.state` is written by every recorded launch** (a model or a slot profile, `RecordLaunch()`); `esc` and canceled prompts never touch it. One line, a row id; an old `rotation.state` beside it is unread."
- :365 **keep**, "Going deeper": `wt/README.md` — agents, install, guard, copilot passthrough env vars — No rotation wording; no change.

### `docs/guides/07-usage-and-spend.md`
- :89 **rewrite**, "... the row launched last, a model or a slot profile, and nothing more. `rotation.state` beside it is the ... — Delete the sentence "`rotation.state` beside it is the last single-model launch (guide 06)."
- :108 **rewrite**, "**`rotation.state` is the *last* single-model launch, nothing more** — one global slot; `esc`/canceled pro... — Rewrite about the surviving file: "**`last-picked.state` is the *last* launch, nothing more** — one line, a model or a slot profile; `esc`/canceled prompts never touch it. It is not a usage summary (guide 06 §5), and `wt stats` does not print it." (or delete the bullet if the guide has no other mention; line 89 already covers it).
- :116 **rewrite**, "Launch/rotation side of the data: 06 (picker, `rotation.state` life cycle, `usage.jsonl` writer)." — -> "Launch side of the data: 06 (picker, `last-picked.state` and `usage.jsonl` writers)."

### `docs/guides/12-model-tiers.md`
- :38 **rewrite**, "No launch, rotation or route looks at it." — -> "No launch or route looks at it." (the tier is displayed and orders picker rows; the sentence is about routing/decisions, so check that the preceding sentence still reads; the CLAUDE.md line 'Nothing routes on a tier' is unaffected).

### `docs/guides/14-litellm-install-and-configuration.md`
- :171 **keep**, design lists guide 14 but every hit (171, 173, 221, 259, 269, 271, 280-283, 335, 347) is LiteLLM master-key... — Not model rotation; no change. Same for docs/guides/00-config-map.md lines 24, 35, 172, docs/guides/13-deployment-use-cases.md 42/130/132 (matched only on a substring such as 'configuration'/'migration'), 08-maintenance 234 and 254 (log rotation), issues.md 9-24 (master key), docs/reference/litellm-admin-ui-setup.md (key rotation). Verified: guide 14 needs no edit; the design's mention of it is unneeded.

### `docs/reference/worktrunk-comparison.md`
- :29 **rewrite**, "*which model* (the rotating picker over the local model stack)" — "the rotating picker" -> "the model picker". The sentence is a dated snapshot comparison; changing one adjective is still correct since rotation no longer exists.

### `issues.md`
- :40 **keep**, "`:120` — rotation state is the single global `rotation.state`; per-tag `rotation-<tag>.state` files are le... — Historical record of the 2026-08-29 review (items kept as a historical record per CLAUDE.md); do not edit.

### `litellm-session-logs/session-log-sources.md`
- :1148 **check**, "wt's agent table marks it neither rotating nor resuming" — Refers to a field of wt's agent table (internal/agents, likely a `Rotates`/rotating capability flag). If the wt-scope change renames or removes that field, reword to "wt's agent table marks it as having no model layer and no resume"; otherwise keep. Depends on the wt-scope decision on the agents table column.

## Added by the completeness critic

- `wt/internal/catalog/wire_subscription_test.go`:69 **delete**, TestRotationSkipsSubscriptionRows (lines 69-79): calls RegisteredOrdered(subRows()) and asserts [ollama/x]; co — Not in the covered list. Real site. It exists only to pin RegisteredOrdered's subscription filter. Once RegisteredOrdered is deleted (see the overlooked section) delete this whole test with its comment. Check the file's imports afterwards: `slices` is still used by TestNonNativeModelsDropsSubscripti
- `wt/internal/localmodels/catalog_rows_test.go`:114 **rewrite**, comment on TestCatalogRowsHiddenRegistryIDShadowsDiscovered: "...and non-TUI rotation — which returns the regi — Not in the covered list. Comment only, no code change. Rotation no longer exists and the non-TUI no-pin path never rotated. Reword to 'and a no-pin non-TUI launch, which takes the catalog's one launchable row, would launch the missing model_name'. Check that this is still accurate against resolve_re
- `wt/internal/localmodels/catalog_rows_test.go`:183 **rewrite**, comment on TestCatalogRowsOverlayMatchedAgainstRealInventory: "Stats and rotation are keyed on these ids, so a — Not in the covered list. Comment only. Change 'Stats and rotation are keyed on' to 'Stats, usage history and the picker's last-launched marker are keyed on'.
- `wt/internal/modeleditor/models_form_test.go`:257 **rewrite**, comment on TestModelFormRegistersADiscoveredModelUnderItsOwnId: "the id its routes, usage history and rotation — Not in the covered list (only models_form.go is). Comment only: drop 'and rotation' so it reads 'its routes and usage history already use'.
- `wt/internal/config/registry_doc_test.go`:185 **rewrite**, comment on TestPatchModelRefusals: "the id itself cannot be patched (history, rotation and profiles key on it) — Not in the covered list. Comment only: 'history, rotation and profiles key on it' becomes 'usage history and profiles key on it'. The matching source comments are in the covered wt/internal/modeladmin/apply.go:418 and wt/cmd/wt-model/model_write.go:254, which also say 'rotation and launch profiles k

## Confusable non-sites (do not edit)

- benchmarks/tasks/eval/doc_summary/items.toml:55 (on-call rotation, fixture text)
- docs/guides/08-maintenance-and-troubleshooting.md:234,254 (Ollama log files rotated as server-N.log)
- docs/guides/13-deployment-use-cases.md:42,130,132 (token rotation, certificate renewal and rotation)
- docs/reference/litellm-admin-ui-setup.md (rotated master key and salt, <rotate-me> placeholders)
- docs/reference/LiteLLM Proxy on macOS_ Unifying Ollama, llama_cpp, and OpenRouter.md (key rotation wording)
- llmbench/src/llmbench/providers/lifecycle/backends/process_server.py:284 and llmbench/tests/providers/lifecycle/backends/test_process_server.py:671 (rotated API key)
- wt/cmd/wt-model/stop_test.go and stop_sessions_test.go (a test helper named recordFor matched RecordFor case-insensitively; it builds a servestate record)
- wt/internal/lifecycle/server_loading_test.go (writeStartRecordFor, start record helper)
- wt/internal/litellm/dbreconcile.go:96, cloudrows_db_test.go, fixwave_db_test.go:140 (LiteLLM master-key rotation and route hashes)
- wt/internal/modelclient/session.go:199 and session_test.go:264,286 (heartbeat refused because the token was rotated)
- wt/internal/modelping/classify_test.go:188-207 (rotate the token on the host)
- wt/internal/modeleditor/setup_litellm.go:59 and setup_litellm_test.go (optRotate and LiteLLMRotateKey, the LiteLLM key rotation menu)
- wt/internal/modelsetup/{apply,observe,plan,finalreview,pairurl,steplabel}*.go and tests (key and token rotation steps). wt/internal/modelsetup/plan.go is in the covered list; its hit is probably also key rotation, so the plan writer should check it and take it out if it is.
- wt/internal/config/config_test.go:463,505,687 and config.go:1665 (comments saying 'rotation group', 'rotation slot's tag component', 'ModelsWithTag is the primary filter for rotation'). These are covered files with wording-only edits. DefaultTag and ModelsWithTag themselves stay.
- wt/docs/superpowers/** (plans and specs from 2026-08 that mention rotation.state and last-picked.state): excluded as historical, the same rule as docs/superpowers. The grep -d skip also dropped them from the main sweep, and they were checked separately.
- wt/cmd/wt-session/launch_test.go: in the covered list but has no hit for any rotation pattern. It only imports usage and has a record-pwd recorder script. Nothing to change unless the plan adds a RecordLaunch test there.
- No hits at all in: Makefile, .github, .claude/skills, bin/ (apart from check-config-dirs-untouched), agent-utilities, modeltiers, litellm-session-logs/*.py, docs/contracts/* (no fixture mentions rotation.state or last-picked.state), wt/scripts, wt/tools.
