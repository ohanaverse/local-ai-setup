# wt Slot Profiles Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a user pick a named, hand-written slot profile in wt's selection screen (or pin it with `-M profile/<name>`) so Claude Code launches through the LiteLLM proxy with every model slot mapped to a registry model.

**Architecture:** A slot profile is its own launch unit, not a model with extras. `internal/profiles` loads and statically validates `slot-profiles.toml`; `internal/catalog` turns each profile into a row by resolved validation against the registry, the live inventory and the `[litellm]` table; the claude driver builds one command whose inline `--settings` JSON carries the slot mapping. Both launch paths run the same ten steps over three new `internal/lifecycle` calls (a multi-model route ensure that returns errors, a restart-owed option on `Start`, a restart and a probe that report failure), then a fail-closed applier and profile-specific bookkeeping.

**Tech Stack:** Go 1.26.7 (`wt/go.mod`), cobra, Bubble Tea / bubbles / lipgloss, BurntSushi/toml, `encoding/json`; LiteLLM proxy (`config.yaml` routes) and Claude Code as the launched agent.

**Spec:** `docs/superpowers/specs/2026-10-13-wt-slot-profiles-design.md`

## Global Constraints

- **Always the proxy.** A slot-profile launch uses the `[litellm]` url and api key whatever `[litellm] enabled` says; no code on this path reads `cfg.IsLitellm()`.
- **At most one distinct local model per profile.** It may fill several slots; a second distinct local model blocks the row.
- **Fail closed.** A profile that cannot be fully applied never launches, and no failure degrades to an unprofiled launch. Every abort happens before the agent starts and before anything is recorded.
- **wt writes no settings file for a Claude profile launch.** `config_content` travels only in the inline `--settings` JSON; `.claude/settings.local.json` is never written. The proxy token is set in the process env only, never in argv.
- **Automatic profiles are never applied to a slot-profile launch.** The path never calls `applyProfileForLaunch`, never resolves `profiles.toml` and never prints the `apply? [Y/n]` prompt.
- **At most one proxy restart per profile launch**, and none when every slot's route already exists.
- **`rotation.state` stays one bare registry id.** A profile launch never writes it; the picker's marker moves through the new `last-picked.state` beside it.
- **Reach is the picker and a `-M profile/<name>` pin only.** Rotation, the single-launchable auto-launch, `wt start` and `wt smoke` never see a profile row; `profile` is a reserved id prefix.
- **Tests never touch the real config home and never restart the real proxy.** Every test runs under a scratch config home; `bin/check-config-dirs-untouched` must stay green.
- **Any ad-hoc `wt` run against a scratch registry sets `WT_LITELLM_CONFIG` too** (and `WT_LITELLM_RESTART_CMD=true`), or route writes are refused or reach the real `config.yaml`.
- **Guides never embed live model state.** `docs/guides/` shows the command that reveals state; every example is labelled illustrative.
- **Go 1.26.7**, the version in `wt/go.mod`. Run Go commands from `wt/`.

## Review Focus

The five inputs most likely to bite a user that the spec's Testing table does not obviously pin, most likely first.

1. `slot-profiles.toml` has a TOML typo, or one profile with a malformed name: pinned and rotated single-model launches behave exactly as before, wt prints exactly one warning line per problem for the whole run, and `-M profile/<name>` exits non-zero with the "not in the eligible list" message. **Test in Task 8.**
2. `[litellm] enabled = false` with a url and a key set: an all-cloud profile is still a launch row; with the key empty it is blocked with the `wt litellm set` wording, whatever `enabled` says. **Test in Task 4.**
3. Passthrough `-- --settings=x.json` or `-- --model=opus` (the `=` form): refused with the flag named, exactly as the two-argument form is, and no command is built. **Test in Task 3.**
4. A start refused because its server is occupied while a proxy restart is owed from a cloud-route write: no restart at the refusal; a confirmed retry gives exactly one restart in total; giving up and settling gives exactly one, so the written route is live. **Test in Task 6.**
5. ctrl+c while the profile's route write or proxy restart is in flight in the TUI: no agent starts after the quit, no usage event, `last-picked.state` or refcount line is written, and wt exits only after that stage finished. **Test in Task 9.**

## File Structure

| File | Responsibility | Task | New / Modify |
|---|---|---|---|
| `docs/superpowers/specs/2026-10-13-wt-slot-profiles-design.md` | Gains a `## Check results` section at the end | 1 | Modify |
| `docs/contracts/slot-profiles.sample.toml` | Contract fixture for the file format | 2 | New |
| `wt/internal/profiles/slot.go` | Slot-profile file types, path, parse, load; `SlotLaunch`, `SlotBuildInput`, `SlotCommand`, the `SlotCapable` driver interface; `MergeConfigContent` | 2 | New |
| `wt/internal/profiles/slot_validate.go` | Static validation: `SlotDrivers`, `SlotCheck`, `ValidateSlots`, the static error messages | 2 | New |
| `wt/internal/profiles/slot_test.go` | Parse, load, path, `FilledOrder`, `MergeConfigContent` tests | 2 | New |
| `wt/internal/profiles/slot_validate_test.go` | One case per static rule | 2 | New |
| `wt/internal/profiles/slot_fixture_test.go` | Contract test over the fixture | 2 | New |
| `wt/internal/agents/claude_slots.go` | `claudeDriver`'s slot names, reserved env keys, slot build, refused passthrough flags | 3 | New |
| `wt/internal/agents/slots.go` | Agent-neutral slot helpers: `SlotsFor`, `SlotDrivers`, `CheckSlotPassthrough`, `BuildSlotCommand`, `BuildSlotLaunchCmd`, `filterEnvPrefixes` | 3 | New |
| `wt/internal/agents/claude_slots_test.go` | Claude slot build tests, golden comparison | 3 | New |
| `wt/internal/agents/slots_test.go` | Builder tests; the fixture validated against the real driver | 3 | New |
| `wt/internal/agents/testdata/claude_slots_settings.golden.json` | Golden `--settings` payload | 3 | New |
| `wt/internal/catalog/catalog.go` | `Input.SlotProfiles`, `Row.Profile`, `Action`/`BlockReason` delegation, `Build` appends profile rows | 4 | Modify |
| `wt/internal/catalog/profile.go` | `ProfileRow`, `ResolveSlotProfile` (resolved validation), `SlotPrefixReserved`, row helpers | 4 | New |
| `wt/internal/catalog/profile_test.go` | Resolved-validation and catalog-row tests | 4 | New |
| `wt/internal/tui/modelrows.go` | `tableInput.slots`, `tableRow.costModel`, the profile sort band | 5 | Modify |
| `wt/internal/tui/modeltable.go` | Profile row cells; no `ResolveRoute` for a profile row | 5 | Modify |
| `wt/internal/tui/model_list.go` | `modelItem.profile` | 5 | Modify |
| `wt/internal/tui/layout.go` | `slotLine`; `modelFrames` uses it for a highlighted profile row | 5 | Modify |
| `wt/internal/tui/app.go` | T5: `model.slotChecks`, `tableFor`/`refreshTable` carry it, single-row shortcut guard. T7: marker from `LastPicked`, starting-cursor rule. T9: `phaseSlot`, `model.slot`, `Run` signature, Enter/pin dispatch, replace-dialog hooks, post-exit profile branch | 5, 7, 9 | Modify |
| `wt/internal/tui/slot_row_test.go` | Profile row cells, sort bands, slot line, screen fit; shared tui test fixtures | 5 | New |
| `wt/internal/tui/layout_test.go` | Profile rows added to `TestEveryListPhaseFitsTheTerminal` | 5 | Modify |
| `wt/cmd/wt/resolve.go` | T5: `pickerBlockedReason` and `launchableModels` guards. T8: opt-in input, `slotPin` return, the no-pin wording counts model rows only | 5, 8 | Modify |
| `wt/cmd/wt/resolve_test.go` | T5 guard tests; T8 pin tests | 5, 8 | Modify |
| `wt/internal/smoke/smoke_test.go` | Test: `Candidates` returns no profile row | 5 | Modify |
| `wt/internal/lifecycle/lifecycle.go` | `Options.RestartOwed`, `RestartStillOwed`, `startWith` change | 6 | Modify |
| `wt/internal/lifecycle/profile_routes.go` | `EnsureModelRoutes`, `RouteEnsureError`, `ErrNotRoutable`, `RestartProxy`, `ProbeProxy` | 6 | New |
| `wt/internal/lifecycle/profile_routes_test.go` | Restart-count and error-case tests | 6 | New |
| `wt/internal/rotation/rotation.go` | `last-picked.state`: `LastPicked`, `RecordProfile`; `RecordFor` writes both files | 7 | Modify |
| `wt/internal/rotation/rotation_test.go` | Last-picked tests | 7 | Modify |
| `wt/internal/refcount/refcount.go` | `RecordAll` | 7 | Modify |
| `wt/internal/refcount/refcount_test.go` | `RecordAll` tests | 7 | Modify |
| `wt/internal/tui/slot_cursor_test.go` | Marker and the three starting-cursor cases | 7 | New |
| `wt/cmd/wt/app.go` | `app.slotChecks`, `app.slotLoadErr`, `loadSlotStore` seam, load in `newApp` | 8 | Modify |
| `wt/cmd/wt/slot_launch.go` | `launchSlotChecks`, `slotPin`, the seams, `slotWarnings`, `launchSlotProfile`, `applySlotProfile`, `runSlotAgentCmd` | 8 | New |
| `wt/cmd/wt/start.go` | `startForLaunchOwed`; `startForLaunch` becomes a wrapper | 8 | Modify |
| `wt/cmd/wt/launch.go` | `launchFilteredImpl` dispatches a `*slotPin` | 8 | Modify |
| `wt/cmd/wt/main.go` | T8: `runLaunchPath` sets `launchSlotChecks`, prints warnings. T9: passes `tui.SlotProfiles` to both `tuiRun` calls | 8, 9 | Modify |
| `wt/cmd/wt/testmain_test.go` | Fail-closed defaults for the new cmd/wt seams | 8 | Modify |
| `wt/cmd/wt/slot_launch_test.go` | Launch-flow, applier, post-exit, fail-closed tests; shared cmd/wt slot fixtures | 8 | New |
| `wt/cmd/wt/app_test.go` | `newApp` slot-load tests | 8 | Modify |
| `wt/internal/tui/launch.go` | `SlotProfileApplier`, `SlotProfiles`, `slotApplier`, `withSlotProfiles`, `pendingSurvey.profile` | 9 | Modify |
| `wt/internal/tui/slot_flow.go` | The TUI profile launch: state, messages, seams, `beginSlotLaunch`, handlers, view, `runSlotAndWaitCmd`, `dropSlot`, `settleQuitSlot` | 9 | New |
| `wt/internal/tui/start_flow.go` | `runStart` and `beginStartOwed` carry `RestartOwed`; `finishStart` hands a profile start back to the slot flow | 9 | Modify |
| `wt/internal/tui/testhelpers_test.go` | Fail-closed defaults for the new tui seams | 9 | Modify |
| `wt/internal/tui/slot_flow_test.go` | TUI flow tests | 9 | New |
| `wt/cmd/wt/main_test.go`, `wt/cmd/wt/helpers_test.go` | The nine `tuiRun` stubs gain the twelfth parameter; `main_test.go` gains the test that `runLaunchPath` hands the TUI its slot profiles | 9 | Modify |
| `wt/cmd/wt/profile.go` | `list` prints the slot section; `show` branches on the `profile/` prefix | 10 | Modify |
| `wt/cmd/wt/profile_slots.go` | `writeSlotProfileList`, `showSlotProfile`, `maskKey` | 10 | New |
| `wt/cmd/wt/litellm.go` | `runLitellmStatus` calls `maskKey` | 10 | Modify |
| `wt/cmd/wt/stats_usage.go` | A profile's launch row carries no spend | 10 | Modify |
| `wt/cmd/wt/profile_slots_test.go`, `wt/cmd/wt/profile_test.go`, `wt/cmd/wt/stats_usage_rows_test.go` | Command and stats tests | 10 | New, Modify, Modify |
| `wt/docs/wt-agents/slot-profiles.md` | Operator page for slot profiles | 11 | New |
| `wt/docs/wt-agents/profiles.md`, `claude-wt.md`, `README.md` | Pointers; the `ANTHROPIC_DEFAULT_*_MODEL` caveat replaced by a pointer | 11 | Modify |
| `wt/docs/internals/launch-flow.md`, `local-models.md`, `tui.md`, `agents.md`, `packages.md`, `testing.md` | Launch flow, lifecycle seams, picker row, driver seam, package rows, new seams | 11 | Modify |
| `wt/docs/wt-config.md`, `wt/docs/wt-stats.md` | State-directory listing; the profile row in `wt stats` | 11 | Modify |
| `docs/guides/00-config-map.md`, `03-model-families.md`, `06-wt-agents-and-models.md`, `07-usage-and-spend.md` | The two new files and their owner; the lines describing `rotation.state` | 11 | Modify |
| `wt/CLAUDE.md`, `CLAUDE.md`, `wt/CHANGELOG.md` | Package notes; the two new files and their owner; an Unreleased entry | 11 | Modify |

New runtime files: `~/.config/agent-wt/slot-profiles.toml` (hand-written; wt reads it and never writes it) and `~/.config/agent-wt/last-picked.state` (written by wt; one line, a row id).

## Task order and pull requests

| # | Task | Deliverable |
|---|---|---|
| 1 | Real Claude Code checks | The six checks run by hand against the installed `claude` and the live proxy; a `## Check results` section appended to the spec; the decision each result triggers |
| 2 | Slot profile file: types, load, path and static validation | `profiles.LoadSlots` and `profiles.ValidateSlots`, the `SlotCapable` driver interface, the contract fixture and its test |
| 3 | Claude slot build | `claudeDriver`'s slot interface and `agents.BuildSlotLaunchCmd`, with a golden `--settings` payload |
| 4 | Resolved validation and catalog rows | `catalog.ResolveSlotProfile`, opt-in profile rows from `catalog.Build`, the reserved prefix, duplicates, `catalog.Find` on `profile/<name>` |
| 5 | Catalog consumers and the picker row | Profile rows drawn in the picker with their columns, sort band and slot line; `launchableModels` and smoke exclude them; no `ResolveRoute` on one |
| 6 | Lifecycle seams | `lifecycle.EnsureModelRoutes`, `Options.RestartOwed`, `RestartProxy`, `ProbeProxy`, with restart-count tests |
| 7 | Bookkeeping | `last-picked.state`, `rotation.RecordProfile`, `refcount.RecordAll`, the starting-cursor rule |
| 8 | Non-TUI profile launch | `-M profile/<name>` launches through `launchSlotProfile` (the ten steps), the fail-closed applier, the post-exit flow |
| 9 | TUI profile launch | Enter and a pin on a profile row run the same ten steps off the update goroutine; `tui.Run` takes the slot profiles and their applier |
| 10 | Commands and stats | `wt profile list` slot section, `wt profile show -A <agent> -M profile/<name>` with the token masked, a profile's own row in `wt stats` |
| 11 | Documentation | The slot-profiles page, pointers, internals notes, guide lines, both `CLAUDE.md` files; `make lint` and `make test-all` pass |
| 12 | Acceptance on the real stack | The spec's four acceptance checks and the 80x24 pty screen captures, recorded in the spec |

Dependencies: 2 → 3 and 4; 4 → 5; 5 → 7; 6 stands alone; 8 needs 2 to 7; 9 and 10 need 8.

Suggested stacked pull requests. Each one leaves `wt` working and releasable.

| PR | Tasks | State of `wt` after it merges |
|---|---|---|
| A | 1 | Spec only: the check results and the decisions they trigger. Stop here if check 2 fails |
| B | 2, 3, 4 | The file format, the Claude slot build and the catalog rows exist and are tested. No caller opts in, so nothing a user sees changes |
| C | 5, 6, 7 | The picker can draw a profile row and the lifecycle and bookkeeping calls exist, still with no caller that supplies profiles. One visible change: every single-model launch now also writes `last-picked.state` |
| D | 8, 9, 10, 11 | The feature is on: the pin, the picker row, the commands, the stats row, and the documentation in the same change. Task 12 runs on this branch before it merges, and its results are committed to the spec in it |

## Deviations from the spec, and decisions still open

The plan was checked by applying Tasks 2 to 10, in order and as written, to a scratch copy of `wt/`: `gofmt -l`, `go build ./...`, `go vet ./...` and `go test -count=1 ./...` were clean for all 26 packages. Tasks 1 and 12 are manual runs against the real stack and were not executed.

Where the plan departs from the spec's wording:

- **Restart owed.** A start that is refused (`*OccupiedError`, `*OccupancyUnknownError`) does not use up the owed restart; `lifecycle.RestartStillOwed(err)` tells the caller. Seeding the internal state literally would restart twice on a confirmed replace.
- **Abort after a route write.** `EnsureModelRoutes` takes the restart owed so far and, on its own error, runs that one restart itself, so a route already written is published.
- **Profile row identity.** A profile row keeps `profile/<name>` in `Row.Model.ID` with every other `Model` field zero. Consumers test `Row.Profile` before reading any other field.
- **Warnings.** Parse errors, unusable profiles and a reserved `profile` provider are printed once per run by `runLaunchPath`, not on every row build. `wt profile show` prints none; `wt profile list` reports the profile by position.
- **Proxy probe.** `ProbeProxy` needs a 200 from the liveness endpoint within 5 seconds.
- **Two added rules.** An empty slot value is a static error. A slot model whose provider is missing, or whose location does not resolve, blocks the row.
- **Profile names** match `^[a-z0-9][a-z0-9-]*$`, so a name cannot start with a hyphen.
- **`wt stats`.** A profile's launch row shows no spend only when its spend is zero and its id is not a registry id.

Known gaps against the spec, not fixed in this plan:

- **RUNNING on some blocked rows.** A row blocked before the local model is examined (for example, LiteLLM has no url) shows `-` in RUNNING even when its local model is running.
- **`config_content.env` that is not a table.** The row is shown as launchable and the launch then fails closed at the slot build with `config_content.env must be a table`. The spec says such a profile should be a blocked row.

Decisions that wait on Task 1:

- **Check 2 has no fallback.** If `--model <alias>` does not send the slot's registry id, stop after Task 1 and report to the owner.
- **Check 1 fallback and `config_content.env`.** If Claude Code ignores the `env` block in `--settings`, Task 3 Step 21 moves wt's variables to the process env, and keys under `config_content.env` then have no effect. The owner decides whether those keys should move to the process env too.
- **Check 3 fallback.** Task 3 Step 23 needs the model ids Task 1 records, so its list and golden lines cannot be written before Task 1 runs. The case where checks 1 and 3 both fail is not spelled out.
- **Task 11's page** keeps `<date>` and `<version>` markers and assumes the primary result of checks 1, 4, 5 and 6. Its Step 1 gives the replacement text for each other result.

---

### Task 1: Real Claude Code checks

This task is run by hand, in a plain terminal (zsh or bash), on the machine that runs the LiteLLM proxy. It writes no Go code and has no test file, so the write-test / see-it-fail steps do not apply: each step is one command block with the result to expect. It sends a few one-line prompts through the live proxy. It starts no model, restarts nothing and changes no wt state. Do not run it from inside a Claude Code session: that shell exports `CLAUDE_CODE_*` variables the checks must not inherit.

**Files:**
- Modify: `docs/superpowers/specs/2026-10-13-wt-slot-profiles-design.md` (append a `## Check results` section after the last section, `## Documentation`)
- Create, outside the repository: `${TMPDIR:-/tmp}/wt-slot-checks/checks.sh` (shell helpers; deleted in Step 11)

**Interfaces:**
- Consumes: the installed `claude` (`--model <model>`, `--settings <file-or-json>`, `-p`/`--print`, `--no-session-persistence`); the installed `wt` (`wt litellm status`, `wt litellm list`, both read-only); the `[litellm]` table of `~/.config/agent-wt/config.toml` (`url`, `api_key`); the proxy error log `~/.litellm.err.log`; the table `"LiteLLM_SpendLogs"` in the database named by `general_settings.database_url` of `~/.config/litellm/config.yaml`; `jq`; `/opt/homebrew/bin/psql`.
- Produces: the spec section `## Check results`, one table with the columns `# | Check | Result | Evidence | Decision`. Tasks 3 and 11 read the `Decision` column. Step 14 lists what each decision changes.

Facts read on 2026-10-13, to re-check in Step 1: `claude` is `~/.local/bin/claude`, version `2.1.296 (Claude Code)`; the proxy url is `http://localhost:4000`; the error log is `~/.litellm.err.log`; a rejected id is logged as `… 400: {'error': 'anthropic_messages: Invalid model name passed in model=claude-haiku-5-5. Call `/v1/models` …'}`; the spend table records a rejected request with an empty `model_group` and no id, so the escaped ids come from the log only.

- [ ] **Step 1: Confirm the machine facts**

```bash
which claude; claude --version
claude --help | grep -E -- '^  (--model|--settings|-p, --print|--no-session-persistence)'
wt litellm status
grep -A1 StandardErrorPath ~/Library/LaunchAgents/local.litellm.proxy.plist
grep 'Invalid model name' ~/.litellm.err.log | tail -1
which jq; ls /opt/homebrew/bin/psql
jq -r '(.env // {}) | keys | join(",")' ~/.claude/settings.json
jq -r '.model // "(no model field)"' ~/.claude/settings.json
env | grep -E '^(ANTHROPIC_|CLAUDE_CODE)' | sed 's/=.*//'
```

Expected: a `claude` path and a version line; four help lines, one per flag (`--model <model>`, `--settings <file-or-json>`, `-p, --print`, `--no-session-persistence`); `wt litellm status` prints `litellm: on` or `off`, the url and the key's last four characters only; the plist names `~/.litellm.err.log`; one log line of the shape above (or none on a proxy that never rejected a request); both tools found.

The last three commands record the starting state. Write down their output. An `env` key that starts with `ANTHROPIC_` in `~/.claude/settings.json`, or any line from the `env | grep`, changes what a check means: note it in the Evidence column of every check it could touch. If a flag is missing from the help output, stop: the installed Claude Code does not have the flag the design uses.

- [ ] **Step 2: Create the scratch directory and the helpers**

```bash
S="${TMPDIR:-/tmp}/wt-slot-checks"
mkdir -p "$S/work"
printf 'one\n' > "$S/work/a.txt"; printf 'two\n' > "$S/work/b.txt"
cat > "$S/checks.sh" <<'EOF'
# Helpers for the slot-profile checks. Source this file; it prints no secret.
S="${TMPDIR:-/tmp}/wt-slot-checks"
ERRLOG="$HOME/.litellm.err.log"
PSQL=/opt/homebrew/bin/psql
REJECT='Invalid model name passed in model='

litellm_field() {
  awk -v k="$1" '
    /^[ \t]*\[/ { on = ($1 == "[litellm]") }
    on && $1 == k { v = $0; sub(/^[^=]*=[ \t]*"/, "", v); sub(/"[ \t]*$/, "", v); print v }
  ' "$HOME/.config/agent-wt/config.toml"
}
PROXY_URL=$(litellm_field url); PROXY_URL="${PROXY_URL%/}"
PROXY_KEY=$(litellm_field api_key)
DSN=$(awk '$1 == "database_url:" { print $2 }' "$HOME/.config/litellm/config.yaml" | tr -d "\"'")

# cc runs a command in $S/work with the process env wt gives a slot launch
# and none of the model variables this shell may export.
# Usage: cc [NAME=VALUE ...] claude [ARG ...]
cc() {
  ( cd "$S/work" && env \
      -u ANTHROPIC_MODEL -u ANTHROPIC_SMALL_FAST_MODEL -u CLAUDE_CODE_SUBAGENT_MODEL \
      -u ANTHROPIC_DEFAULT_FABLE_MODEL -u ANTHROPIC_DEFAULT_OPUS_MODEL \
      -u ANTHROPIC_DEFAULT_SONNET_MODEL -u ANTHROPIC_DEFAULT_HAIKU_MODEL \
      ANTHROPIC_BASE_URL="$PROXY_URL" ANTHROPIC_AUTH_TOKEN="$PROXY_KEY" ANTHROPIC_API_KEY= \
      "$@" )
}

# mark notes the time and the number of rejections logged so far; seen
# reports what the proxy received since the last mark.
mark() {
  T0=$(date -u '+%Y-%m-%d %H:%M:%S')
  E0=$(grep -c "$REJECT" "$ERRLOG")
}
seen() {
  sleep "${SEEN_WAIT:-15}"
  "$PSQL" "$DSN" -At -F ' | ' -c "select coalesce(nullif(model_group, ''), '(no route)'), call_type, status, count(*) from \"LiteLLM_SpendLogs\" where \"startTime\" >= '$T0' group by 1, 2, 3 order by 1, 2, 3"
  E1=$(grep -c "$REJECT" "$ERRLOG")
  echo "new rejections: $((E1 - E0))"
  grep "$REJECT" "$ERRLOG" | tail -n "$((E1 - E0))" | sed -E 's/.*model=([^ ]*)\. Call.*/\1/' | sort | uniq -c
}

# routed says whether each id has a route in config.yaml.
routed() {
  for id in "$@"; do
    if wt litellm list | cut -f1 | grep -Fxq -- "$id"; then echo "routed      $id"; else echo "NOT ROUTED  $id"; fi
  done
}
EOF
source "$S/checks.sh"
echo "url=$PROXY_URL key=...${PROXY_KEY: -4} dsn=$([ -n "$DSN" ] && echo set)"
curl -s -o /dev/null -w '%{http_code}\n' "$PROXY_URL/health/liveliness"
mark; SEEN_WAIT=0 seen
```

Expected: `url=http://localhost:4000 key=...<four characters> dsn=set`; `200`; then zero or more spend rows from other sessions and `new rejections: 0`.

`seen` prints one line per registry id the proxy served since `mark`, as `<registry id> | <call type> | <status> | <count>`, then the number of ids it rejected and each rejected id with its count. It waits 15 seconds first because the proxy writes spend rows in batches; run `SEEN_WAIT=0 seen` again if a row is missing. Any other agent using the proxy during a check adds rows: close other wt sessions first.

If the terminal is closed, run `source "${TMPDIR:-/tmp}/wt-slot-checks/checks.sh"` and set the variables of Step 3 again.

- [ ] **Step 3: Choose five routed cloud ids and build the settings payload**

```bash
wt litellm list
```

Pick five different cloud ids from the first column, each a model that can call tools. Set them (the ids below are illustrative; replace all five):

```bash
ID_FABLE='ollama/kimi-k3:cloud'
ID_OPUS='ollama/glm-5.2:cloud'
ID_SONNET='ollama/glm-5.3-flash:cloud'
ID_HAIKU='ollama/deepseek-v4.1-flash:cloud'
ID_ALT='ollama/nemotron-3-ultra:cloud'
routed "$ID_FABLE" "$ID_OPUS" "$ID_SONNET" "$ID_HAIKU" "$ID_ALT"
```

Expected: five lines that start with `routed`. Replace any id reported `NOT ROUTED`.

```bash
SETTINGS=$(jq -cn --arg url "$PROXY_URL" \
  --arg f "$ID_FABLE" --arg o "$ID_OPUS" --arg s "$ID_SONNET" --arg h "$ID_HAIKU" '
  def name: sub("^[^/]*/"; "");
  {env: {
    ANTHROPIC_BASE_URL: $url,
    ANTHROPIC_API_KEY: "",
    ANTHROPIC_DEFAULT_FABLE_MODEL: $f,  ANTHROPIC_DEFAULT_FABLE_MODEL_NAME: ($f | name),
    ANTHROPIC_DEFAULT_OPUS_MODEL: $o,   ANTHROPIC_DEFAULT_OPUS_MODEL_NAME: ($o | name),
    ANTHROPIC_DEFAULT_SONNET_MODEL: $s, ANTHROPIC_DEFAULT_SONNET_MODEL_NAME: ($s | name),
    ANTHROPIC_DEFAULT_HAIKU_MODEL: $h,  ANTHROPIC_DEFAULT_HAIKU_MODEL_NAME: ($h | name)
  }}')
BASE_ONLY=$(jq -cn --arg url "$PROXY_URL" '{env: {ANTHROPIC_BASE_URL: $url, ANTHROPIC_API_KEY: ""}}')
echo "$SETTINGS" | jq -r '.env | keys | length'
```

Expected: `10`. `SETTINGS` is the payload the design sends: the proxy url, an empty API key, and for each slot the registry id and a display name (here the id without its provider prefix). It holds no token. `BASE_ONLY` is the same payload without any slot mapping.

- [ ] **Step 4: Check 2 — `--model <alias>` sends the slot's registry id**

Run this before check 1. Here the slot variables are process env, so the result does not depend on `--settings`.

```bash
for alias in fable opus sonnet haiku; do
  echo "== --model $alias"
  mark
  cc ANTHROPIC_DEFAULT_FABLE_MODEL="$ID_FABLE" ANTHROPIC_DEFAULT_OPUS_MODEL="$ID_OPUS" \
     ANTHROPIC_DEFAULT_SONNET_MODEL="$ID_SONNET" ANTHROPIC_DEFAULT_HAIKU_MODEL="$ID_HAIKU" \
     claude -p 'Reply with the single word ok.' --model "$alias" --no-session-persistence
  echo "exit=$?"
  seen
done
```

Expected for each alias: a short reply, `exit=0`, a `success` row for that alias's id (`$ID_FABLE` under `fable`, and so on), and `new rejections: 0`. A row for `$ID_HAIKU` under another alias is a background request and is fine.

Result: **pass** when all four aliases reached their own id. **fail** when any alias did not; record the alias and the id `seen` lists under `new rejections` (that is the id Claude Code sent instead).

- [ ] **Step 5: Check 1 — an `env` block in an inline `--settings` payload is honoured**

Now the slot variables are only in the payload. `cc` removes them from the process env.

```bash
for alias in sonnet opus; do
  echo "== --model $alias"
  mark
  cc claude -p 'Reply with the single word ok.' --model "$alias" --settings "$SETTINGS" --no-session-persistence
  echo "exit=$?"
  seen
done
```

Expected: a reply, `exit=0`, a `success` row for `$ID_SONNET` in the first run and for `$ID_OPUS` in the second, and `new rejections: 0`.

Result: **pass** when both runs reached their id. **fail** when a run was rejected or reached another id while Step 4 passed for the same alias: the payload's `env` block was not applied.

- [ ] **Step 6: Check 4 — two `--settings` flags on one command line**

The first payload maps only sonnet, the second only haiku. Which of the two runs reaches its id tells the four outcomes apart.

```bash
S1=$(jq -c --arg m "$ID_SONNET" '.env.ANTHROPIC_DEFAULT_SONNET_MODEL = $m' <<<"$BASE_ONLY")
S2=$(jq -c --arg m "$ID_HAIKU" '.env.ANTHROPIC_DEFAULT_HAIKU_MODEL = $m' <<<"$BASE_ONLY")
for alias in sonnet haiku; do
  echo "== --model $alias"
  mark
  cc claude -p 'Reply with the single word ok.' --model "$alias" --settings "$S1" --settings "$S2" --no-session-persistence
  echo "exit=$?"
  seen
done
```

Read the result from this table. "Reached" means a `success` row for the id; the other case is an id under `new rejections`.

| `--model sonnet` reached `$ID_SONNET` | `--model haiku` reached `$ID_HAIKU` | Result |
|---|---|---|
| yes | yes | merge |
| no | yes | last wins |
| yes | no | first wins |
| neither run sent a request; `claude` printed a usage error | | error |

Only when the result is **merge**, run one more command and record which id won a key both payloads set:

```bash
S3=$(jq -c --arg m "$ID_ALT" '.env.ANTHROPIC_DEFAULT_SONNET_MODEL = $m' <<<"$BASE_ONLY")
mark
cc claude -p 'Reply with the single word ok.' --model sonnet --settings "$S1" --settings "$S3" --no-session-persistence
seen
```

A row for `$ID_ALT` means the later flag wins a shared key; a row for `$ID_SONNET` means the earlier one does. Put that in the Evidence column.

- [ ] **Step 7: Check 5 — an inherited `ANTHROPIC_SMALL_FAST_MODEL`**

`ANTHROPIC_SMALL_FAST_MODEL` is set in the process env to `$ID_ALT`, an id no slot uses. The payload maps haiku to `$ID_HAIKU`.

```bash
for alias in haiku sonnet; do
  echo "== --model $alias"
  mark
  cc ANTHROPIC_SMALL_FAST_MODEL="$ID_ALT" \
     claude -p 'Reply with the single word ok.' --model "$alias" --settings "$SETTINGS" --no-session-persistence
  echo "exit=$?"
  seen
done
```

If neither run shows a row for `$ID_HAIKU` or `$ID_ALT` under `--model sonnet`, print mode made no background request. Then run one interactive session, accept the trust prompt, send `hi`, wait for the reply, type `/exit`, and read `seen`:

```bash
mark
cc ANTHROPIC_SMALL_FAST_MODEL="$ID_ALT" claude --model sonnet --settings "$SETTINGS"
seen
```

Result: **overrides haiku** when any run shows a row for `$ID_ALT`. **does not** when `$ID_ALT` never appears and `$ID_HAIKU` does. Record which run showed which id.

- [ ] **Step 8: Checks 3 and 6 — one interactive session that switches slots and runs a subagent**

Close every other Claude Code session first: any of them may write `~/.claude/settings.json` and blur check 6. Back the file up, then start the session.

```bash
if [ -f ~/.claude/settings.json ]; then cp -p ~/.claude/settings.json "$S/settings.json.before"; else : > "$S/settings.json.absent"; fi
mark
cc claude --model sonnet --settings "$SETTINGS"
```

Inside the session, in this order. Wait for each reply before the next line.

1. Accept the trust prompt for the directory.
2. Type `Reply with the single word ok.`
3. Type `/model opus`, then `Reply with the single word ok.`
4. Type `/model haiku`, then `Reply with the single word ok.`
5. Type `/model fable`, then `Reply with the single word ok.`
6. Type `/model` with no argument. Note whether the list shows the display names from the payload (the ids without their provider prefix) or Anthropic names. Pick `sonnet` in the list.
7. Type `Use a subagent to list the files in the current directory, then tell me how many there are.` Check that the transcript shows a subagent run (a nested task with its own tool calls) and the answer `2`. If the model answered without a subagent, send the line again; if it still does not, set `ID_SONNET` to a stronger tool-calling model, rebuild `SETTINGS` (Step 3) and repeat this step.
8. Type `/exit`.

Then:

```bash
seen
if [ -f "$S/settings.json.before" ]; then
  diff <(jq -S . "$S/settings.json.before") <(jq -S . ~/.claude/settings.json)
else
  cat ~/.claude/settings.json 2>/dev/null
fi
```

Expected from `seen`: `success` rows for all four ids and `new rejections: 0`.

Check 3 result: **pass** when `new rejections` is `0` and a subagent ran. **fail** when it is not: record every id listed, with its count, as the escaped ids.

Check 6 result: the output of the `diff` (or of the `cat`, when the file did not exist before). Copy the changed lines exactly, for example a `model` field and its value. No output means `/model` wrote nothing. Do not restore the file yet when check 3 failed: Step 9 runs first.

- [ ] **Step 9: Only if check 3 failed — try the two ways to close the gap**

Put the escaped ids from Step 8 in an array (the ids below are illustrative):

```bash
ESCAPED=(claude-haiku-5-5 claude-sonnet-5)
```

First re-run, with a `modelOverrides` entry that sends each escaped id to the default slot's registry id:

```bash
SETTINGS_MO="$SETTINGS"
for id in "${ESCAPED[@]}"; do
  SETTINGS_MO=$(jq -c --arg id "$id" --arg to "$ID_SONNET" '.modelOverrides[$id] = $to' <<<"$SETTINGS_MO")
done
mark
cc claude --model sonnet --settings "$SETTINGS_MO"
seen
```

Second re-run, with `CLAUDE_CODE_SUBAGENT_MODEL` in the payload's `env` block and no `modelOverrides`:

```bash
SETTINGS_SUB=$(jq -c --arg to "$ID_SONNET" '.env.CLAUDE_CODE_SUBAGENT_MODEL = $to' <<<"$SETTINGS")
mark
cc claude --model sonnet --settings "$SETTINGS_SUB"
seen
```

In each session repeat items 2 to 8 of Step 8. For each re-run record `new rejections` and the ids listed. A re-run **closes the gap** when it ends with `new rejections: 0`.

- [ ] **Step 10: The extra row — does `modelOverrides` have any effect behind the proxy**

First find the id Claude Code sends for `sonnet` when nothing maps it. This request is meant to be rejected.

```bash
mark
cc claude -p 'Reply with the single word ok.' --model sonnet --settings "$BASE_ONLY" --no-session-persistence
echo "exit=$?"
seen
```

Expected: an API error naming an invalid model, a non-zero exit, and under `new rejections` one id that contains `sonnet`. Set it (the id below is illustrative), map it to `$ID_ALT` and run again:

```bash
NATIVE_SONNET='claude-sonnet-5'
SETTINGS_OV=$(jq -c --arg id "$NATIVE_SONNET" --arg to "$ID_ALT" '.modelOverrides = {($id): $to}' <<<"$BASE_ONLY")
mark
cc claude -p 'Reply with the single word ok.' --model sonnet --settings "$SETTINGS_OV" --no-session-persistence
echo "exit=$?"
seen
```

Result: **effective** when the second run shows a `success` row for `$ID_ALT` and does not reject `$NATIVE_SONNET`. **no effect** when `$NATIVE_SONNET` is rejected again.

- [ ] **Step 11: Restore `~/.claude/settings.json` and remove the scratch directory**

```bash
if [ -f "$S/settings.json.before" ]; then
  cp -p "$S/settings.json.before" ~/.claude/settings.json
elif [ -f "$S/settings.json.absent" ]; then
  rm -f ~/.claude/settings.json
fi
jq -r '.model // "(no model field)"' ~/.claude/settings.json 2>/dev/null
rm -rf "$S"
```

Expected: the same line Step 1 printed for the `model` field (nothing, when the file did not exist before). The checks changed no wt state, no route and no proxy setting, so nothing else is restored.

- [ ] **Step 12: Append the results to the spec**

Open `docs/superpowers/specs/2026-10-13-wt-slot-profiles-design.md` and append this section after the last line of the file, with one blank line before the heading. Keep the heading, the column names and the seven row labels exactly as written; later tasks look them up.

```markdown
## Check results

Run on <date> with Claude Code <version> against the LiteLLM proxy at <url>.

| # | Check | Result | Evidence | Decision |
|---|---|---|---|---|
| 1 | `env` in an inline `--settings` payload is honoured | pass / fail | … | primary path / fallback 1 |
| 2 | `--model <alias>` sends the slot's registry id | pass / fail | … | proceed / stop |
| 3 | No new `Invalid model name … claude-*` line | pass / fail (ids: …) | … | primary path / fallback 3 |
| 4 | Two `--settings` flags | merge / last wins / first wins / error | … | refuse `--model` only / refuse both |
| 5 | Inherited `ANTHROPIC_SMALL_FAST_MODEL` | overrides haiku / does not | … | documentation only |
| 6 | What `/model` writes to `~/.claude/settings.json` | … | … | documentation only |
| – | `modelOverrides` behind the proxy | effective / no effect | … | documentation only |
```

Fill it in by these rules:

- `<date>` is the day the checks ran, `<version>` is the output of `claude --version`, `<url>` is the proxy url. Never write the proxy key.
- **Result**: keep one of the offered values. For check 6 write what the `diff` showed, for example ``writes `"model": "sonnet"` `` or `writes nothing`.
- **Evidence**: the `seen` lines that decided the result, with the ids as they were on the run date. For check 3 add the number of slot switches and that a subagent ran; when Step 9 ran, add both re-runs' `new rejections`. For check 4, on a merge, add which flag won the shared key. For check 6 add what the `/model` list showed for names (item 6 of Step 8).
- **Decision**, row by row:
  - 1: `primary path` on pass, `fallback 1` on fail.
  - 2: `proceed` on pass, `stop` on fail.
  - 3: `primary path` on pass. On fail, `fallback 3: modelOverrides` when the first re-run of Step 9 closed the gap, `fallback 3: CLAUDE_CODE_SUBAGENT_MODEL` when only the second did, `stop` when neither did.
  - 4: ``refuse `--model` only`` on merge, `refuse both` on any other result.
  - 5, 6 and the last row: `documentation only`.

- [ ] **Step 13: Verify and commit**

```bash
tail -n 13 docs/superpowers/specs/2026-10-13-wt-slot-profiles-design.md
git status --short
```

Expected: the section, with no `…`, `<date>`, `<version>` or `<url>` left in it, and no cell still offering two values; `git status` lists the spec and nothing else.

```bash
git add docs/superpowers/specs/2026-10-13-wt-slot-profiles-design.md
git commit -m "docs(spec): slot profiles, the check results on real Claude Code"
```

- [ ] **Step 14: Act on the decisions before starting Task 2**

Each decision changes the plan as follows. Where a row says "no code change", continue with the plan as written.

| Row | Decision | What changes |
|---|---|---|
| 1 | `primary path` | Nothing. Task 3 puts the slot variables, the `_NAME` variables and the profile's `env` in the `--settings` `env` block |
| 1 | `fallback 1` | **Task 3 changes.** `BuildSlots` puts `ANTHROPIC_DEFAULT_<SLOT>_MODEL`, `ANTHROPIC_DEFAULT_<SLOT>_MODEL_NAME` and the profile's `env` in `SlotCommand.Env` (the profile's `env` first, wt's variables after, so wt's win). `--settings` carries only the profile's `config_content`, and the flag and its value are left out when `config_content` is empty. The golden file and the build tests change with it. Task 10 needs no change. Task 11 words the page to match |
| 2 | `proceed` | Nothing. Task 3 passes `--model <default slot>` |
| 2 | `stop` | **Stop after this task.** The slot mapping is the feature and the spec has no replacement. Report the alias and the id the proxy received to the owner. Do not start Task 2 |
| 3 | `primary path` | Nothing. Task 3 writes no `modelOverrides` of its own |
| 3 | `fallback 3: modelOverrides` | **Task 3 changes.** `claude_slots.go` gains an unexported `var claudeEscapedIDs = []string{…}` holding the escaped ids recorded in the table. `BuildSlots` starts the settings object with `"modelOverrides": {id: <default slot's registry id>}` for each of them, before it merges the profile's `config_content`, so a profile's own entries win |
| 3 | `fallback 3: CLAUDE_CODE_SUBAGENT_MODEL` | **Task 3 changes.** `BuildSlots` writes `CLAUDE_CODE_SUBAGENT_MODEL=<default slot's registry id>` into the `env` block first, before the profile's `env`, so a profile's own value still wins. The key is not reserved |
| 3 | `stop` | **Stop after this task.** Report the escaped ids and both re-runs to the owner |
| 4 | `refuse both` | Nothing. Task 3's `RefusedSlotPassthrough` returns `{"--settings", "--model"}` |
| 4 | ``refuse `--model` only`` | **Task 3 changes.** `RefusedSlotPassthrough` returns `{"--model"}` only. Its test, and Task 11's page, say Claude Code merges a passthrough `--settings` |
| 5 | `documentation only` | No code change: Task 3 removes the variable from the inherited environment either way. **Task 11** states the result on the slot-profiles page and, when it overrides, adds that an `env` block in a user settings file that sets it still redirects the haiku tier |
| 6 | `documentation only` | No code change. **Task 11** describes exactly what was written and which model a later bare `claude` run then starts on |
| – | `documentation only` | No code change. **Task 11** describes `config_content.modelOverrides` as effective, or as passed through with no observed effect |

When a row says "Task 3 changes" or "Task 11" words something differently, write that row's decision at the top of that task in the plan before it is started, so the developer who sees only that task reads it.

### Task 2: Slot profile file: types, load, path and static validation

This task adds the slot-profile file format to package `profiles` (`wt/internal/profiles`). It adds two source files, three test files and one contract fixture. It changes no existing file and no existing behaviour: nothing calls the new code until later tasks.

A slot profile is one `[[profile]]` entry of `~/.config/agent-wt/slot-profiles.toml`. It names an agent, assigns a registry model id to some of that agent's model slots, and names one filled slot as the default. It is a different thing from the package's existing `Profile` (an automatic `profiles.toml` entry). The two share four optional fields: `env`, `args`, `config_content`, `wrapper`.

Validation has two stages. This task writes the first, **static** validation: the rules that need only the file and what each agent's driver declares. Whether a slot's model exists or is running is checked in Task 4.

Package `profiles` must not import `internal/agents`, `internal/catalog` or `internal/lifecycle`. It learns about agents through callbacks (`SlotDrivers`) and through an interface the drivers implement (`SlotCapable`).

No result of Task 1 changes this task.

**Files:**
- Create: `wt/internal/profiles/slot.go`
- Create: `wt/internal/profiles/slot_validate.go`
- Create: `docs/contracts/slot-profiles.sample.toml`
- Test: `wt/internal/profiles/slot_test.go`
- Test: `wt/internal/profiles/slot_validate_test.go`
- Test: `wt/internal/profiles/slot_fixture_test.go`

**Interfaces:**
- Consumes (existing code, all in `wt/internal/profiles` unless a package is named):
  - `func Path() string` (`profiles.go:87`) — `filepath.Join(config.Dir(), "profiles.toml")`
  - `type Mechanism string` and `MechanismEnv`, `MechanismArgs`, `MechanismConfigFile`, `MechanismWrapper` (`profiles.go:16-23`)
  - `type WrapperSpec struct { Binary string; ArgsTemplate []string }` (`profiles.go:51`)
  - `type Profile struct` with fields `Env map[string]string`, `Args []string`, `ConfigContent map[string]any`, `Wrapper *WrapperSpec` (`profiles.go:59`)
  - `func usedMechanisms(p Profile) []Mechanism` (`validate.go:71`) — order env, args, config_file, wrapper
  - `func mergeInto(dst, src map[string]any)` (`merge.go:13`)
  - `func Dir() string` in `internal/config` (`config.go:732`) — `$XDG_CONFIG_HOME/agent-wt`, else `~/.config/agent-wt`
  - `type Model struct` in `internal/config` (`config.go:547`)
  - `toml.Decode(data string, v any) (toml.MetaData, error)` from `github.com/BurntSushi/toml`
- Produces (in `slot.go`):
  - `const SlotIDPrefix = "profile/"`
  - `const SlotReservedProvider = "profile"`
  - `type SlotProfile struct { Name, Agent, Default string; Slots map[string]string; Env map[string]string; Args []string; ConfigContent map[string]any; Wrapper *WrapperSpec }`
  - `func (p SlotProfile) ID() string`
  - `func (p SlotProfile) FilledOrder(declared []string) []string`
  - `type SlotStore struct { Profiles []SlotProfile }`
  - `func SlotPath() string`
  - `func ParseSlots(data []byte) (SlotStore, error)`
  - `func LoadSlots() (SlotStore, error)`
  - `type SlotLaunch struct { Agent string; Models map[string]config.Model; Default string; Env map[string]string; ConfigContent map[string]any }`
  - `type SlotBuildInput struct { SlotLaunch; ProxyOrigin string; ProxyKey string; Yolo bool }`
  - `type SlotCommand struct { Bin string; Args []string; Env []string; ClearEnv []string; ClearEnvPrefixes []string }`
  - `type SlotCapable interface { SlotNames() []string; ReservedEnvKeys() []string; BuildSlots(in SlotBuildInput) (SlotCommand, error) }`
  - `func MergeConfigContent(dst, src map[string]any)`
- Produces (in `slot_validate.go`):
  - `type SlotDrivers struct { Known func(agent string) bool; Slots func(agent string) []string; ReservedEnv func(agent string) []string; Mechanisms func(agent string) []Mechanism; Required func(agent string, m Mechanism) (Mechanism, bool) }`
  - `var ErrSlotDuplicate = errors.New("duplicate profile name")`
  - `type SlotCheck struct { Index int; Profile SlotProfile; Usable bool; Duplicate bool; Declared []string; Err error }`
  - `func (c SlotCheck) Label() string`
  - `func ValidateSlots(store SlotStore, d SlotDrivers) []SlotCheck`
- Produces (fixture): `docs/contracts/slot-profiles.sample.toml`, two profiles named `mixed` and `one-slot`, both for agent `claude`. Task 3 reads it too.

**The static rules.** `ValidateSlots` checks them in this order. The first that fails is the profile's `Err`, and `Err.Error()` is exactly the message. `%q` is Go's quoted form.

| # | Rule | `Usable` | Message |
|---|---|---|---|
| 1 | `name` is empty | false | `name is missing` |
| 2 | `name` does not match `^[a-z0-9][a-z0-9-]*$` | false | `name %q is malformed: use lowercase letters, digits and hyphens` |
| 3 | `agent` is empty | false | `agent is missing` |
| 4 | `d.Known(agent)` is false | false | `agent %q is unknown` |
| 5 | `d.Slots(agent)` is empty | false | `agent %q does not support slot profiles` |
| 6 | Another usable profile has the same agent and name | true | `duplicate profile name` (`ErrSlotDuplicate`; `Duplicate` is set on every member of the group) |
| 7 | `slots` is empty | true | `slots is empty: assign at least one slot` |
| 8 | A `slots` key is not declared (keys in sorted order) | true | `slot %q is not a slot of agent %q (slots: %s)`, the last value being the declared names joined with `, ` |
| 9 | A `slots` value is empty (keys in sorted order) | true | `slot %q names no model` |
| 10 | `default` is empty | true | `default is missing` |
| 11 | `default` is not a key of `slots` | true | `default %q is not a slot this profile fills` |
| 12 | A used mechanism is not in `d.Mechanisms(agent)` (order: env, args, config_file, wrapper) | true | `agent %q does not support mechanism %q` |
| 13 | `d.Required` names a partner that is not used | true | `mechanism %q has no effect without %q on the same profile` |
| 14 | An `env` key is in `d.ReservedEnv(agent)` (keys in sorted order) | true | `env sets %s, which wt sets for the launch` |
| 15 | `config_content.env` is a table and one of its keys is reserved (sorted) | true | `config_content.env sets %s, which wt sets for the launch` |
| 16 | `wrapper` is set and `args_template` has no `"{{args}}"` element | true | `wrapper args_template %v has no "{{args}}" placeholder` |

Choices where the spec is silent, each following the surrounding code:

- A key the schema does not name is ignored, as `Load` ignores one in `profiles.toml`.
- A value of the wrong TOML type (for example `MAX_THINKING_TOKENS = 4096`, an integer where `env` wants a string) is a decode error, so the whole file fails to parse. That is what `toml.Decode` does for `profiles.toml` today.
- Rule 5 treats an empty slot list like a nil one. No driver returns an empty non-nil list; this only avoids a `(slots: )` message if one ever does.
- This package cannot import the real claude driver, so its tests describe agents with closures, as `validate_test.go` does for `Validate`. Task 3 validates the same fixture against the real driver.

Run every command from the repository root. No test here touches the real config home: the tests that reach `config.Dir()` first set `XDG_CONFIG_HOME` to `t.TempDir()`.

#### Behaviour 1: the file — types, path, parse, load

- [ ] **Step 1: Write the failing test**

Create `wt/internal/profiles/slot_test.go`:

```go
package profiles

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// writeSlotFile points the config home at a temp directory and writes body
// as its slot-profiles.toml.
func writeSlotFile(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "agent-wt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(SlotPath(), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestSlotPathSitsBesideProfilesToml checks that slot-profiles.toml is
// resolved in the same directory as profiles.toml. If it regresses, wt reads
// the file from somewhere the user did not write it and shows no profile rows.
func TestSlotPathSitsBesideProfilesToml(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	want := filepath.Join(dir, "agent-wt", "slot-profiles.toml")
	if got := SlotPath(); got != want {
		t.Errorf("SlotPath() = %q, want %q", got, want)
	}
	if filepath.Dir(SlotPath()) != filepath.Dir(Path()) {
		t.Errorf("SlotPath() dir = %q, want the directory of Path() %q", filepath.Dir(SlotPath()), filepath.Dir(Path()))
	}
}

// TestLoadSlotsMissingFileIsEmpty checks that a machine with no
// slot-profiles.toml loads an empty store and no error. If it regresses,
// every wt launch on a machine that never wrote the file prints a warning.
func TestLoadSlotsMissingFileIsEmpty(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	store, err := LoadSlots()
	if err != nil {
		t.Fatalf("LoadSlots() error = %v, want nil", err)
	}
	if len(store.Profiles) != 0 {
		t.Errorf("LoadSlots() profiles = %v, want none", store.Profiles)
	}
}

// TestLoadSlotsReadsEveryField checks that each documented field of a
// [[profile]] entry reaches the store, and that entries keep file order. If
// it regresses, a field the user wrote is dropped without a word, or the
// picker lists profiles in an order the file does not have.
func TestLoadSlotsReadsEveryField(t *testing.T) {
	writeSlotFile(t, `
[[profile]]
name    = "second-in-sort-order"
agent   = "claude"
default = "sonnet"

[profile.slots]
opus   = "openrouter/vendor/big"
sonnet = "ollama/mid:cloud"

[profile.env]
MAX_THINKING_TOKENS = "4096"

[profile.config_content.modelOverrides]
"claude-sonnet-4-6" = "ollama/other:cloud"

[[profile]]
name    = "first-in-sort-order"
agent   = "pi"
default = "main"
args    = ["--flag", "value"]
wrapper = { binary = "little-coder", args_template = ["--pi-args", "{{args}}"] }

[profile.slots]
main = "omlx/local-model"
`)
	store, err := LoadSlots()
	if err != nil {
		t.Fatalf("LoadSlots() error = %v, want nil", err)
	}
	want := []SlotProfile{
		{
			Name:    "second-in-sort-order",
			Agent:   "claude",
			Default: "sonnet",
			Slots:   map[string]string{"opus": "openrouter/vendor/big", "sonnet": "ollama/mid:cloud"},
			Env:     map[string]string{"MAX_THINKING_TOKENS": "4096"},
			ConfigContent: map[string]any{
				"modelOverrides": map[string]any{"claude-sonnet-4-6": "ollama/other:cloud"},
			},
		},
		{
			Name:    "first-in-sort-order",
			Agent:   "pi",
			Default: "main",
			Slots:   map[string]string{"main": "omlx/local-model"},
			Args:    []string{"--flag", "value"},
			Wrapper: &WrapperSpec{Binary: "little-coder", ArgsTemplate: []string{"--pi-args", "{{args}}"}},
		},
	}
	if !reflect.DeepEqual(store.Profiles, want) {
		t.Errorf("LoadSlots() profiles =\n%#v\nwant\n%#v", store.Profiles, want)
	}
}

// TestLoadSlotsParseErrorIsReturned checks that a file that is not TOML
// comes back as an error naming the file, not as an empty store. If it
// regresses, a typo makes every profile row vanish with no warning line.
func TestLoadSlotsParseErrorIsReturned(t *testing.T) {
	writeSlotFile(t, "[[profile]\nname = \"x\"\n")
	store, err := LoadSlots()
	if err == nil {
		t.Fatal("LoadSlots() error = nil, want a parse error")
	}
	if !strings.HasPrefix(err.Error(), "parse slot-profiles.toml: ") {
		t.Errorf("LoadSlots() error = %q, want the prefix %q", err, "parse slot-profiles.toml: ")
	}
	if len(store.Profiles) != 0 {
		t.Errorf("LoadSlots() profiles = %v, want none beside an error", store.Profiles)
	}
}

// TestParseSlots checks what counts as a file that parses. If it regresses,
// a file with a wrongly typed value either stops every profile that used to
// load, or loads with the value quietly missing.
func TestParseSlots(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		want    int // number of profiles
		wantErr bool
	}{
		{name: "empty file", body: "", want: 0},
		{name: "comments only", body: "# nothing yet\n", want: 0},
		{name: "one bare profile", body: "[[profile]]\nname = \"a\"\n", want: 1},
		{name: "two profiles", body: "[[profile]]\nname = \"a\"\n[[profile]]\nname = \"b\"\n", want: 2},
		{name: "an unknown key is ignored", body: "[[profile]]\nname = \"a\"\ncolour = \"red\"\n", want: 1},
		{name: "a [[profiles]] table is another file's shape", body: "[[profiles]]\nagent = \"claude\"\n", want: 0},
		{name: "syntax error", body: "[[profile]\n", wantErr: true},
		{name: "a slot id is not a string", body: "[[profile]]\n[profile.slots]\nopus = 3\n", wantErr: true},
		{name: "an env value is not a string", body: "[[profile]]\n[profile.env]\nMAX_THINKING_TOKENS = 4096\n", wantErr: true},
		{name: "args is not a list", body: "[[profile]]\nargs = \"--flag\"\n", wantErr: true},
		{name: "a key set twice", body: "[[profile]]\nname = \"a\"\nname = \"b\"\n", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store, err := ParseSlots([]byte(tc.body))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseSlots() error = nil, want a parse error; store = %#v", store)
				}
				if !strings.HasPrefix(err.Error(), "parse slot-profiles.toml: ") {
					t.Errorf("ParseSlots() error = %q, want the prefix %q", err, "parse slot-profiles.toml: ")
				}
				if len(store.Profiles) != 0 {
					t.Errorf("ParseSlots() profiles = %v, want none beside an error", store.Profiles)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseSlots() error = %v, want nil", err)
			}
			if len(store.Profiles) != tc.want {
				t.Errorf("ParseSlots() gave %d profiles, want %d", len(store.Profiles), tc.want)
			}
		})
	}
}

// TestSlotProfileID checks the row id a profile is listed and pinned under.
// If it regresses, `-M profile/<name>` no longer finds the row the picker
// shows.
func TestSlotProfileID(t *testing.T) {
	if got := (SlotProfile{Name: "mixed"}).ID(); got != "profile/mixed" {
		t.Errorf("ID() = %q, want %q", got, "profile/mixed")
	}
	if SlotIDPrefix != SlotReservedProvider+"/" {
		t.Errorf("SlotIDPrefix = %q, want SlotReservedProvider %q followed by a slash", SlotIDPrefix, SlotReservedProvider)
	}
}
```

- [ ] **Step 2: Run it and see it fail**

```bash
cd wt && go test ./internal/profiles -run 'TestSlotPath|TestLoadSlots|TestParseSlots|TestSlotProfileID'
```

Expected: the package does not compile. The first error is

```
internal/profiles/slot_test.go:20:25: undefined: SlotPath
```

and the run ends with

```
FAIL	github.com/ohanaverse/local-ai-setup/wt/internal/profiles [build failed]
```

- [ ] **Step 3: Write the implementation**

Create `wt/internal/profiles/slot.go`:

```go
package profiles

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// SlotIDPrefix prefixes a slot profile's row id: "profile/<name>".
const SlotIDPrefix = "profile/"

// SlotReservedProvider is the registry provider id whose model ids would
// collide with SlotIDPrefix.
const SlotReservedProvider = "profile"

// SlotProfile is one [[profile]] entry of slot-profiles.toml: a named,
// hand-written assignment of registry model ids to an agent's model slots.
// It is a different thing from Profile, an automatic profiles.toml entry
// matched to a launch; the two share only the four mechanism fields.
type SlotProfile struct {
	Name          string            `toml:"name"`
	Agent         string            `toml:"agent"`
	Default       string            `toml:"default"`
	Slots         map[string]string `toml:"slots"`
	Env           map[string]string `toml:"env,omitempty"`
	Args          []string          `toml:"args,omitempty"`
	ConfigContent map[string]any    `toml:"config_content,omitempty"`
	Wrapper       *WrapperSpec      `toml:"wrapper,omitempty"`
}

// ID is the profile's row id, SlotIDPrefix + Name.
func (p SlotProfile) ID() string { return SlotIDPrefix + p.Name }

// slotFileSchema is the on-disk shape.
type slotFileSchema struct {
	Profiles []SlotProfile `toml:"profile"`
}

// SlotStore is slot-profiles.toml as parsed, in file order.
type SlotStore struct {
	Profiles []SlotProfile
}

// SlotPath returns the slot-profiles.toml location, beside profiles.toml
// in config.Dir(). wt reads the file and never writes it.
func SlotPath() string { return filepath.Join(config.Dir(), "slot-profiles.toml") }

// ParseSlots decodes the file's bytes. It checks nothing but the TOML and
// the field types; ValidateSlots judges each profile. A key the schema
// does not name is ignored, as Load ignores one in profiles.toml.
func ParseSlots(data []byte) (SlotStore, error) {
	var fs slotFileSchema
	if _, err := toml.Decode(string(data), &fs); err != nil {
		return SlotStore{}, fmt.Errorf("parse slot-profiles.toml: %w", err)
	}
	return SlotStore{Profiles: fs.Profiles}, nil
}

// LoadSlots reads SlotPath(). A missing file is not an error: it is an
// empty store, so a machine with no slot profiles shows no profile rows.
func LoadSlots() (SlotStore, error) {
	data, err := os.ReadFile(SlotPath())
	if os.IsNotExist(err) {
		return SlotStore{}, nil
	}
	if err != nil {
		return SlotStore{}, err
	}
	return ParseSlots(data)
}
```

- [ ] **Step 4: Run the tests and see them pass**

```bash
cd wt && go test ./internal/profiles -run 'TestSlotPath|TestLoadSlots|TestParseSlots|TestSlotProfileID' -v
```

Expected: `--- PASS` for `TestSlotPathSitsBesideProfilesToml`, `TestLoadSlotsMissingFileIsEmpty`, `TestLoadSlotsReadsEveryField`, `TestLoadSlotsParseErrorIsReturned`, `TestParseSlots` (eleven subtests) and `TestSlotProfileID`, then `ok  	github.com/ohanaverse/local-ai-setup/wt/internal/profiles`.

#### Behaviour 2: slot order, the config_content merge and the driver seam

- [ ] **Step 5: Write the failing test**

In `wt/internal/profiles/slot_test.go`, replace the import block with this one (it adds `config`):

```go
import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)
```

Then append this to the end of the same file:

```go
// TestFilledOrder checks the order a profile's slots are listed in: the
// default first, then the rest in the driver's declared order. If it
// regresses, the picker's slot line and `wt profile show` list the slots in
// a random or misleading order, or show a slot the agent does not have.
func TestFilledOrder(t *testing.T) {
	declared := []string{"fable", "opus", "sonnet", "haiku"}
	tests := []struct {
		name     string
		profile  SlotProfile
		declared []string
		want     []string
	}{
		{
			name:     "default first, the rest in declared order",
			profile:  SlotProfile{Default: "sonnet", Slots: map[string]string{"haiku": "c", "sonnet": "b", "opus": "a"}},
			declared: declared,
			want:     []string{"sonnet", "opus", "haiku"},
		},
		{
			name:     "default is the first declared slot",
			profile:  SlotProfile{Default: "fable", Slots: map[string]string{"fable": "a", "haiku": "b"}},
			declared: declared,
			want:     []string{"fable", "haiku"},
		},
		{
			name:     "one slot",
			profile:  SlotProfile{Default: "opus", Slots: map[string]string{"opus": "a"}},
			declared: declared,
			want:     []string{"opus"},
		},
		{
			name:     "a default the profile does not fill is not listed",
			profile:  SlotProfile{Default: "opus", Slots: map[string]string{"haiku": "a", "sonnet": "b"}},
			declared: declared,
			want:     []string{"sonnet", "haiku"},
		},
		{
			name:     "an undeclared slot is not listed, as default or otherwise",
			profile:  SlotProfile{Default: "turbo", Slots: map[string]string{"turbo": "a", "extra": "b", "haiku": "c"}},
			declared: declared,
			want:     []string{"haiku"},
		},
		{
			name:     "no slots",
			profile:  SlotProfile{Default: "opus"},
			declared: declared,
			want:     nil,
		},
		{
			name:     "no declared slots",
			profile:  SlotProfile{Default: "opus", Slots: map[string]string{"opus": "a"}},
			declared: nil,
			want:     nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.profile.FilledOrder(tc.declared)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("FilledOrder() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestMergeConfigContent checks that a profile's config_content merges by
// the rule automatic profiles use: tables key by key, a scalar or an array
// whole. If it regresses, a profile that sets one key of a settings table
// wipes the table's other keys, or its override is ignored.
func TestMergeConfigContent(t *testing.T) {
	dst := map[string]any{
		"env":     map[string]any{"KEEP": "1", "REPLACE": "old"},
		"model":   "old",
		"allowed": []any{"a"},
		"other":   "untouched",
	}
	src := map[string]any{
		"env":            map[string]any{"REPLACE": "new", "ADD": "2"},
		"model":          "new",
		"allowed":        []any{"b", "c"},
		"modelOverrides": map[string]any{"claude-sonnet-4-6": "ollama/other:cloud"},
	}
	MergeConfigContent(dst, src)
	want := map[string]any{
		"env":            map[string]any{"KEEP": "1", "REPLACE": "new", "ADD": "2"},
		"model":          "new",
		"allowed":        []any{"b", "c"},
		"other":          "untouched",
		"modelOverrides": map[string]any{"claude-sonnet-4-6": "ollama/other:cloud"},
	}
	if !reflect.DeepEqual(dst, want) {
		t.Errorf("MergeConfigContent() dst =\n%#v\nwant\n%#v", dst, want)
	}
	wantSrc := map[string]any{
		"env":            map[string]any{"REPLACE": "new", "ADD": "2"},
		"model":          "new",
		"allowed":        []any{"b", "c"},
		"modelOverrides": map[string]any{"claude-sonnet-4-6": "ollama/other:cloud"},
	}
	if !reflect.DeepEqual(src, wantSrc) {
		t.Errorf("MergeConfigContent() changed src to\n%#v\nwant it left as\n%#v", src, wantSrc)
	}
	// Merging nothing, from nil or into an empty object, is not an error.
	MergeConfigContent(dst, nil)
	if !reflect.DeepEqual(dst, want) {
		t.Errorf("MergeConfigContent(dst, nil) changed dst to %#v", dst)
	}
}

// fakeSlotDriver is the smallest thing that satisfies SlotCapable. It
// echoes its input, so the test below can read what a driver receives.
type fakeSlotDriver struct{}

func (fakeSlotDriver) SlotNames() []string       { return []string{"big", "small"} }
func (fakeSlotDriver) ReservedEnvKeys() []string { return []string{"FAKE_MODEL"} }
func (fakeSlotDriver) BuildSlots(in SlotBuildInput) (SlotCommand, error) {
	return SlotCommand{
		Bin:              in.Agent,
		Args:             []string{"--model", in.Default, "--big", in.Models["big"].ID},
		Env:              []string{"FAKE_URL=" + in.ProxyOrigin, "FAKE_KEY=" + in.ProxyKey},
		ClearEnv:         []string{"FAKE_MODEL"},
		ClearEnvPrefixes: []string{"FAKE_DEFAULT_"},
	}, nil
}

// TestSlotCapableCarriesAResolvedProfileToADriver checks the shape of the
// driver seam: one value holds the resolved slot models, the default slot,
// the proxy origin and key and the yolo flag, and the driver answers with a
// command. If it regresses, internal/agents can no longer implement slot
// support without this package importing it, which is an import cycle.
func TestSlotCapableCarriesAResolvedProfileToADriver(t *testing.T) {
	var d SlotCapable = fakeSlotDriver{}
	in := SlotBuildInput{
		SlotLaunch: SlotLaunch{
			Agent:         "fake",
			Models:        map[string]config.Model{"big": {ID: "ollama/big:cloud"}, "small": {ID: "ollama/big:cloud"}},
			Default:       "big",
			Env:           map[string]string{"X": "1"},
			ConfigContent: map[string]any{"k": "v"},
		},
		ProxyOrigin: "http://localhost:4000",
		ProxyKey:    "sk-test",
		Yolo:        true,
	}
	got, err := d.BuildSlots(in)
	if err != nil {
		t.Fatalf("BuildSlots() error = %v, want nil", err)
	}
	want := SlotCommand{
		Bin:              "fake",
		Args:             []string{"--model", "big", "--big", "ollama/big:cloud"},
		Env:              []string{"FAKE_URL=http://localhost:4000", "FAKE_KEY=sk-test"},
		ClearEnv:         []string{"FAKE_MODEL"},
		ClearEnvPrefixes: []string{"FAKE_DEFAULT_"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("BuildSlots() = %#v, want %#v", got, want)
	}
	if !reflect.DeepEqual(d.SlotNames(), []string{"big", "small"}) {
		t.Errorf("SlotNames() = %v, want [big small]", d.SlotNames())
	}
}
```

- [ ] **Step 6: Run it and see it fail**

```bash
cd wt && go test ./internal/profiles -run 'TestFilledOrder|TestMergeConfigContent|TestSlotCapable'
```

Expected: the package does not compile. The first two errors are

```
internal/profiles/slot_test.go:249:22: tc.profile.FilledOrder undefined (type SlotProfile has no field or method FilledOrder)
internal/profiles/slot_test.go:274:2: undefined: MergeConfigContent
```

followed by `undefined: SlotBuildInput`, `undefined: SlotCommand`, `undefined: SlotCapable`, `undefined: SlotLaunch`, and the run ends with `FAIL	github.com/ohanaverse/local-ai-setup/wt/internal/profiles [build failed]`.

- [ ] **Step 7: Write the implementation**

Three edits to `wt/internal/profiles/slot.go`.

First, add `"slices"` to the import block, which becomes:

```go
import (
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/BurntSushi/toml"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)
```

Second, insert this method directly after the `ID` method (before the `slotFileSchema` type):

```go
// FilledOrder lists the slots p fills that are in declared: Default first
// (when it is filled and declared), then the rest in declared's order.
// A slot p fills that the agent does not declare is left out.
func (p SlotProfile) FilledOrder(declared []string) []string {
	filled := func(slot string) bool {
		_, ok := p.Slots[slot]
		return ok
	}
	var out []string
	if filled(p.Default) && slices.Contains(declared, p.Default) {
		out = append(out, p.Default)
	}
	for _, slot := range declared {
		if slot != p.Default && filled(slot) {
			out = append(out, slot)
		}
	}
	return out
}
```

Third, append this to the end of the file:

```go
// SlotLaunch is a resolved slot profile, ready for an agent's slot build.
type SlotLaunch struct {
	Agent         string
	Models        map[string]config.Model // every slot the agent declares → its model; an unfilled slot holds the default slot's model
	Default       string                  // the default slot's name
	Env           map[string]string       // the profile's env
	ConfigContent map[string]any          // the profile's config_content
}

// SlotBuildInput is what a driver's slot build receives.
type SlotBuildInput struct {
	SlotLaunch
	ProxyOrigin string // the [litellm] url, no trailing slash
	ProxyKey    string // the [litellm] api key
	Yolo        bool
}

// SlotCommand is what a driver's slot build returns: a command before the
// binary is looked up and the environment is assembled.
type SlotCommand struct {
	Bin              string
	Args             []string
	Env              []string // "KEY=VALUE", appended after the filtered inherited environment
	ClearEnv         []string // exact names removed from the inherited environment
	ClearEnvPrefixes []string // names with one of these prefixes are removed from the inherited environment
}

// SlotCapable is implemented by agents.Driver values that support slot
// profiles. It is declared here, beside ProfileCapable, so this package
// never imports internal/agents. An agent whose driver does not implement
// it cannot have slot profiles.
type SlotCapable interface {
	// SlotNames are the slots the agent accepts, in display order.
	SlotNames() []string
	// ReservedEnvKeys are the env keys wt owns for a slot-profile launch.
	ReservedEnvKeys() []string
	// BuildSlots builds the command for one resolved profile.
	BuildSlots(in SlotBuildInput) (SlotCommand, error)
}

// MergeConfigContent merges src into dst by the rule automatic profiles
// use (mergeInto): tables merge key by key, recursively; a scalar or an
// array replaces the value whole. src is not changed. A table that dst
// lacks is stored in dst as src's own map, not a copy, so a caller that
// goes on to write into dst's tables copies first.
func MergeConfigContent(dst, src map[string]any) { mergeInto(dst, src) }
```

`MergeConfigContent` is a one-line wrapper on purpose: the rule is `mergeInto`'s, already tested in `merge_test.go`, and a second implementation could drift from it.

- [ ] **Step 8: Run the tests and see them pass**

```bash
cd wt && go test ./internal/profiles -run 'TestFilledOrder|TestMergeConfigContent|TestSlotCapable' -v
```

Expected: `--- PASS` for `TestFilledOrder` (seven subtests), `TestMergeConfigContent` and `TestSlotCapableCarriesAResolvedProfileToADriver`, then `ok  	github.com/ohanaverse/local-ai-setup/wt/internal/profiles`.

- [ ] **Step 9: Format, vet and commit**

```bash
cd wt && gofmt -l ./internal/profiles && go vet ./internal/profiles && go test ./internal/profiles
```

Expected: `gofmt` and `go vet` print nothing; the last line is `ok  	github.com/ohanaverse/local-ai-setup/wt/internal/profiles`.

```bash
git add wt/internal/profiles/slot.go wt/internal/profiles/slot_test.go
git commit -m "feat(wt): the slot profile file, slot-profiles.toml: types, path, load and the driver seam"
```

#### Behaviour 3: static validation

- [ ] **Step 10: Write the failing test**

Create `wt/internal/profiles/slot_validate_test.go`:

```go
package profiles

import (
	"errors"
	"reflect"
	"slices"
	"testing"
)

// slotTestDrivers stands in for the registered agent drivers. It knows three
// agents:
//   - "claude": four slots, the env and config_file mechanisms, and Claude's
//     reserved keys;
//   - "wrapped": two slots, the env, args and wrapper mechanisms, where env
//     has no effect without wrapper;
//   - "plain": a known agent with no slot support.
func slotTestDrivers() SlotDrivers {
	slots := map[string][]string{
		"claude":  {"fable", "opus", "sonnet", "haiku"},
		"wrapped": {"big", "small"},
	}
	reserved := map[string][]string{
		"claude": {
			"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY",
			"ANTHROPIC_DEFAULT_FABLE_MODEL", "ANTHROPIC_DEFAULT_FABLE_MODEL_NAME",
			"ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL_NAME",
			"ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL_NAME",
			"ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL_NAME",
		},
		"wrapped": {"WRAPPED_MODEL"},
	}
	mechanisms := map[string][]Mechanism{
		"claude":  {MechanismEnv, MechanismConfigFile},
		"wrapped": {MechanismEnv, MechanismArgs, MechanismWrapper},
	}
	return SlotDrivers{
		Known:       func(agent string) bool { return slices.Contains([]string{"claude", "wrapped", "plain"}, agent) },
		Slots:       func(agent string) []string { return slots[agent] },
		ReservedEnv: func(agent string) []string { return reserved[agent] },
		Mechanisms:  func(agent string) []Mechanism { return mechanisms[agent] },
		Required: func(agent string, m Mechanism) (Mechanism, bool) {
			if agent == "wrapped" && m == MechanismEnv {
				return MechanismWrapper, true
			}
			return "", false
		},
	}
}

// validClaudeSlot is a claude profile that passes every static rule.
func validClaudeSlot() SlotProfile {
	return SlotProfile{
		Name:    "mixed",
		Agent:   "claude",
		Default: "sonnet",
		Slots:   map[string]string{"opus": "openrouter/vendor/big", "sonnet": "ollama/mid:cloud"},
	}
}

// validWrappedSlot is a profile for the "wrapped" agent that passes every
// static rule while using env, args and wrapper.
func validWrappedSlot() SlotProfile {
	return SlotProfile{
		Name:    "wrapped-one",
		Agent:   "wrapped",
		Default: "big",
		Slots:   map[string]string{"big": "ollama/mid:cloud"},
		Env:     map[string]string{"WRAPPER_MODE": "accept-all"},
		Args:    []string{"--flag"},
		Wrapper: &WrapperSpec{Binary: "wrapper-bin", ArgsTemplate: []string{"--inner", "{{args}}"}},
	}
}

// TestValidateSlotsRules has one case per static rule and pins each message
// word for word, since the message is what the blocked row and `wt profile
// list` show. If a rule regresses, a profile wt cannot launch is listed as
// ok and fails later, or a sound profile is blocked.
func TestValidateSlotsRules(t *testing.T) {
	tests := []struct {
		name       string
		base       func() SlotProfile // nil: validClaudeSlot
		edit       func(p *SlotProfile)
		wantErr    string // "": the profile passes
		wantUsable bool
	}{
		// Passing profiles.
		{name: "two of four slots filled", edit: func(p *SlotProfile) {}, wantUsable: true},
		{name: "every slot filled", edit: func(p *SlotProfile) {
			p.Slots = map[string]string{"fable": "a/1", "opus": "a/2", "sonnet": "a/3", "haiku": "a/4"}
		}, wantUsable: true},
		{name: "a name of digits and hyphens", edit: func(p *SlotProfile) { p.Name = "9-lives-2" }, wantUsable: true},
		{name: "env and config_content on an agent that allows both", edit: func(p *SlotProfile) {
			p.Env = map[string]string{"MAX_THINKING_TOKENS": "4096"}
			p.ConfigContent = map[string]any{
				"modelOverrides": map[string]any{"claude-sonnet-4-6": "ollama/other:cloud"},
				"env":            map[string]any{"CLAUDE_CODE_SUBAGENT_MODEL": "ollama/mid:cloud"},
			}
		}, wantUsable: true},
		{name: "env, args and wrapper on an agent that allows them", base: validWrappedSlot, edit: func(p *SlotProfile) {}, wantUsable: true},
		{name: "a config_content.env that is not a table is left to the slot build", edit: func(p *SlotProfile) {
			p.ConfigContent = map[string]any{"env": "ANTHROPIC_BASE_URL"}
		}, wantUsable: true},

		// Rule 1.
		{name: "name missing", edit: func(p *SlotProfile) { p.Name = "" }, wantErr: `name is missing`},
		// Rule 2.
		{name: "name with an upper-case letter", edit: func(p *SlotProfile) { p.Name = "Mixed" },
			wantErr: `name "Mixed" is malformed: use lowercase letters, digits and hyphens`},
		{name: "name with an underscore", edit: func(p *SlotProfile) { p.Name = "my_profile" },
			wantErr: `name "my_profile" is malformed: use lowercase letters, digits and hyphens`},
		{name: "name with a space", edit: func(p *SlotProfile) { p.Name = "my profile" },
			wantErr: `name "my profile" is malformed: use lowercase letters, digits and hyphens`},
		{name: "name with a slash", edit: func(p *SlotProfile) { p.Name = "profile/mixed" },
			wantErr: `name "profile/mixed" is malformed: use lowercase letters, digits and hyphens`},
		{name: "name starting with a hyphen", edit: func(p *SlotProfile) { p.Name = "-x" },
			wantErr: `name "-x" is malformed: use lowercase letters, digits and hyphens`},
		{name: "name ending with a newline", edit: func(p *SlotProfile) { p.Name = "mixed\n" },
			wantErr: `name "mixed\n" is malformed: use lowercase letters, digits and hyphens`},
		// Rule 3.
		{name: "agent missing", edit: func(p *SlotProfile) { p.Agent = "" }, wantErr: `agent is missing`},
		// Rule 4.
		{name: "agent unknown", edit: func(p *SlotProfile) { p.Agent = "cluade" }, wantErr: `agent "cluade" is unknown`},
		// Rule 5.
		{name: "agent without slot support", edit: func(p *SlotProfile) { p.Agent = "plain" },
			wantErr: `agent "plain" does not support slot profiles`},
		// Rule 7.
		{name: "no slots table", edit: func(p *SlotProfile) { p.Slots = nil },
			wantErr: `slots is empty: assign at least one slot`, wantUsable: true},
		{name: "an empty slots table", edit: func(p *SlotProfile) { p.Slots = map[string]string{} },
			wantErr: `slots is empty: assign at least one slot`, wantUsable: true},
		// Rule 8.
		{name: "an undeclared slot", edit: func(p *SlotProfile) { p.Slots["turbo"] = "a/1" },
			wantErr: `slot "turbo" is not a slot of agent "claude" (slots: fable, opus, sonnet, haiku)`, wantUsable: true},
		{name: "two undeclared slots report the first in sorted order", edit: func(p *SlotProfile) {
			p.Slots["turbo"] = "a/1"
			p.Slots["extra"] = "a/2"
		}, wantErr: `slot "extra" is not a slot of agent "claude" (slots: fable, opus, sonnet, haiku)`, wantUsable: true},
		{name: "a slot name in the wrong case", edit: func(p *SlotProfile) { p.Slots["Haiku"] = "a/1" },
			wantErr: `slot "Haiku" is not a slot of agent "claude" (slots: fable, opus, sonnet, haiku)`, wantUsable: true},
		// Rule 9.
		{name: "a slot with an empty id", edit: func(p *SlotProfile) { p.Slots["haiku"] = "" },
			wantErr: `slot "haiku" names no model`, wantUsable: true},
		// Rule 10.
		{name: "default missing", edit: func(p *SlotProfile) { p.Default = "" },
			wantErr: `default is missing`, wantUsable: true},
		// Rule 11.
		{name: "default names a declared slot the profile leaves unfilled", edit: func(p *SlotProfile) { p.Default = "haiku" },
			wantErr: `default "haiku" is not a slot this profile fills`, wantUsable: true},
		{name: "default names no slot at all", edit: func(p *SlotProfile) { p.Default = "turbo" },
			wantErr: `default "turbo" is not a slot this profile fills`, wantUsable: true},
		// Rule 12.
		{name: "args on an agent that does not allow them", edit: func(p *SlotProfile) { p.Args = []string{"--flag"} },
			wantErr: `agent "claude" does not support mechanism "args"`, wantUsable: true},
		{name: "wrapper on an agent that does not allow it", edit: func(p *SlotProfile) {
			p.Wrapper = &WrapperSpec{Binary: "w", ArgsTemplate: []string{"{{args}}"}}
		}, wantErr: `agent "claude" does not support mechanism "wrapper"`, wantUsable: true},
		{name: "args and wrapper both refused report args first", edit: func(p *SlotProfile) {
			p.Args = []string{"--flag"}
			p.Wrapper = &WrapperSpec{Binary: "w", ArgsTemplate: []string{"{{args}}"}}
		}, wantErr: `agent "claude" does not support mechanism "args"`, wantUsable: true},
		{name: "config_content on an agent that does not allow it", base: validWrappedSlot, edit: func(p *SlotProfile) {
			p.ConfigContent = map[string]any{"k": "v"}
		}, wantErr: `agent "wrapped" does not support mechanism "config_file"`, wantUsable: true},
		// Rule 13.
		{name: "a mechanism without its required partner", base: validWrappedSlot, edit: func(p *SlotProfile) { p.Wrapper = nil },
			wantErr: `mechanism "env" has no effect without "wrapper" on the same profile`, wantUsable: true},
		// Rule 14.
		{name: "env sets a reserved key", edit: func(p *SlotProfile) {
			p.Env = map[string]string{"MAX_THINKING_TOKENS": "4096", "ANTHROPIC_BASE_URL": "http://elsewhere"}
		}, wantErr: `env sets ANTHROPIC_BASE_URL, which wt sets for the launch`, wantUsable: true},
		{name: "env sets a slot's model variable", edit: func(p *SlotProfile) {
			p.Env = map[string]string{"ANTHROPIC_DEFAULT_HAIKU_MODEL": "claude-haiku-4-5"}
		}, wantErr: `env sets ANTHROPIC_DEFAULT_HAIKU_MODEL, which wt sets for the launch`, wantUsable: true},
		{name: "two reserved env keys report the first in sorted order", edit: func(p *SlotProfile) {
			p.Env = map[string]string{"ANTHROPIC_BASE_URL": "x", "ANTHROPIC_API_KEY": "y"}
		}, wantErr: `env sets ANTHROPIC_API_KEY, which wt sets for the launch`, wantUsable: true},
		// Rule 15.
		{name: "config_content.env sets a reserved key", edit: func(p *SlotProfile) {
			p.ConfigContent = map[string]any{"env": map[string]any{"OTHER": "1", "ANTHROPIC_DEFAULT_OPUS_MODEL_NAME": "x"}}
		}, wantErr: `config_content.env sets ANTHROPIC_DEFAULT_OPUS_MODEL_NAME, which wt sets for the launch`, wantUsable: true},
		// Rule 16.
		{name: "wrapper without the args placeholder", base: validWrappedSlot, edit: func(p *SlotProfile) {
			p.Wrapper.ArgsTemplate = []string{"--inner"}
		}, wantErr: `wrapper args_template [--inner] has no "{{args}}" placeholder`, wantUsable: true},
		{name: "wrapper with the placeholder only inside another element", base: validWrappedSlot, edit: func(p *SlotProfile) {
			p.Wrapper.ArgsTemplate = []string{"--inner={{args}}"}
		}, wantErr: `wrapper args_template [--inner={{args}}] has no "{{args}}" placeholder`, wantUsable: true},

		// The first failing rule is the one reported.
		{name: "a missing name is reported before a missing agent", edit: func(p *SlotProfile) { p.Name, p.Agent = "", "" },
			wantErr: `name is missing`},
		{name: "an unknown agent is reported before empty slots", edit: func(p *SlotProfile) { p.Agent, p.Slots = "cluade", nil },
			wantErr: `agent "cluade" is unknown`},
		{name: "an undeclared slot is reported before a missing default", edit: func(p *SlotProfile) {
			p.Slots["turbo"] = "a/1"
			p.Default = ""
		}, wantErr: `slot "turbo" is not a slot of agent "claude" (slots: fable, opus, sonnet, haiku)`, wantUsable: true},
		{name: "an unfilled default is reported before a refused mechanism", edit: func(p *SlotProfile) {
			p.Default = "haiku"
			p.Args = []string{"--flag"}
		}, wantErr: `default "haiku" is not a slot this profile fills`, wantUsable: true},
		{name: "a refused mechanism is reported before a reserved env key", edit: func(p *SlotProfile) {
			p.Args = []string{"--flag"}
			p.Env = map[string]string{"ANTHROPIC_BASE_URL": "x"}
		}, wantErr: `agent "claude" does not support mechanism "args"`, wantUsable: true},
		{name: "a reserved env key is reported before one under config_content.env", edit: func(p *SlotProfile) {
			p.Env = map[string]string{"ANTHROPIC_BASE_URL": "x"}
			p.ConfigContent = map[string]any{"env": map[string]any{"ANTHROPIC_API_KEY": "y"}}
		}, wantErr: `env sets ANTHROPIC_BASE_URL, which wt sets for the launch`, wantUsable: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			base := validClaudeSlot
			if tc.base != nil {
				base = tc.base
			}
			p := base()
			tc.edit(&p)
			checks := ValidateSlots(SlotStore{Profiles: []SlotProfile{p}}, slotTestDrivers())
			if len(checks) != 1 {
				t.Fatalf("ValidateSlots() gave %d checks, want 1", len(checks))
			}
			c := checks[0]
			got := ""
			if c.Err != nil {
				got = c.Err.Error()
			}
			if got != tc.wantErr {
				t.Errorf("Err = %q, want %q", got, tc.wantErr)
			}
			if c.Usable != tc.wantUsable {
				t.Errorf("Usable = %v, want %v", c.Usable, tc.wantUsable)
			}
			if c.Duplicate {
				t.Errorf("Duplicate = true for a file with one profile")
			}
			wantDeclared := []string(nil)
			if tc.wantUsable {
				wantDeclared = slotTestDrivers().Slots(p.Agent)
			}
			if !reflect.DeepEqual(c.Declared, wantDeclared) {
				t.Errorf("Declared = %v, want %v", c.Declared, wantDeclared)
			}
			if c.Index != 0 || !reflect.DeepEqual(c.Profile, p) {
				t.Errorf("check carries Index %d and Profile %#v, want 0 and the profile as given", c.Index, c.Profile)
			}
		})
	}
}

// TestValidateSlotsDuplicateNames checks rule 6: profiles that share a name
// for one agent are all marked duplicate, and nothing else is. If it
// regresses, two rows get the same id `profile/<name>` and a pin launches
// whichever comes first, or a profile is blocked for a name another agent
// or a broken entry happens to use.
func TestValidateSlotsDuplicateNames(t *testing.T) {
	first := validClaudeSlot()
	second := validClaudeSlot()
	second.Default = "" // would fail rule 10 if the duplicate rule did not come first
	otherAgent := validWrappedSlot()
	otherAgent.Name = "mixed"
	unknownAgent := validClaudeSlot()
	unknownAgent.Agent = "cluade"
	alone := validClaudeSlot()
	alone.Name = "alone"

	checks := ValidateSlots(SlotStore{Profiles: []SlotProfile{first, otherAgent, unknownAgent, second, alone}}, slotTestDrivers())
	if len(checks) != 5 {
		t.Fatalf("ValidateSlots() gave %d checks, want 5", len(checks))
	}
	for _, i := range []int{0, 3} {
		c := checks[i]
		if !c.Usable || !c.Duplicate || !errors.Is(c.Err, ErrSlotDuplicate) {
			t.Errorf("checks[%d]: Usable=%v Duplicate=%v Err=%v, want a usable duplicate with ErrSlotDuplicate", i, c.Usable, c.Duplicate, c.Err)
		}
		if c.Err == nil || c.Err.Error() != "duplicate profile name" {
			t.Errorf("checks[%d].Err = %v, want %q", i, c.Err, "duplicate profile name")
		}
		if !reflect.DeepEqual(c.Declared, []string{"fable", "opus", "sonnet", "haiku"}) {
			t.Errorf("checks[%d].Declared = %v, want claude's slots", i, c.Declared)
		}
	}
	if c := checks[1]; !c.Usable || c.Duplicate || c.Err != nil {
		t.Errorf("same name under another agent: Usable=%v Duplicate=%v Err=%v, want a passing profile", c.Usable, c.Duplicate, c.Err)
	}
	if c := checks[2]; c.Usable || c.Duplicate || c.Err == nil || c.Err.Error() != `agent "cluade" is unknown` {
		t.Errorf("same name under an unknown agent: Usable=%v Duplicate=%v Err=%v, want unusable with its own error", c.Usable, c.Duplicate, c.Err)
	}
	if c := checks[4]; !c.Usable || c.Duplicate || c.Err != nil {
		t.Errorf("a profile with its own name: Usable=%v Duplicate=%v Err=%v, want a passing profile", c.Usable, c.Duplicate, c.Err)
	}
}

// TestValidateSlotsJudgesEachProfileAlone checks that one check comes back
// per profile, in file order, and that a broken profile changes no other
// profile's verdict. If it regresses, one typo blocks every profile in the
// file, or a verdict is attached to the wrong row.
func TestValidateSlotsJudgesEachProfileAlone(t *testing.T) {
	noName := validClaudeSlot()
	noName.Name = ""
	badSlot := validClaudeSlot()
	badSlot.Name = "bad-slot"
	badSlot.Slots = map[string]string{"turbo": "a/1"}
	good := validClaudeSlot()
	wrapped := validWrappedSlot()

	checks := ValidateSlots(SlotStore{Profiles: []SlotProfile{noName, badSlot, good, wrapped}}, slotTestDrivers())
	if len(checks) != 4 {
		t.Fatalf("ValidateSlots() gave %d checks, want 4", len(checks))
	}
	wantLabels := []string{"slot-profiles.toml[0]", "profile/bad-slot", "profile/mixed", "profile/wrapped-one"}
	wantOK := []bool{false, false, true, true}
	for i, c := range checks {
		if c.Index != i {
			t.Errorf("checks[%d].Index = %d, want %d", i, c.Index, i)
		}
		if c.Label() != wantLabels[i] {
			t.Errorf("checks[%d].Label() = %q, want %q", i, c.Label(), wantLabels[i])
		}
		if (c.Err == nil) != wantOK[i] {
			t.Errorf("checks[%d].Err = %v, want passing = %v", i, c.Err, wantOK[i])
		}
	}
	if got := ValidateSlots(SlotStore{}, slotTestDrivers()); len(got) != 0 {
		t.Errorf("ValidateSlots() on an empty store = %v, want no checks", got)
	}
}

// TestSlotCheckLabel checks how a profile is named in a warning line and in
// `wt profile list`: by its row id when it has one, else by its position in
// the file. If it regresses, a profile with a malformed name is reported
// under an id that no row and no pin can ever have.
func TestSlotCheckLabel(t *testing.T) {
	tests := []struct {
		name  string
		check SlotCheck
		want  string
	}{
		{name: "usable", check: SlotCheck{Index: 4, Usable: true, Profile: SlotProfile{Name: "mixed"}}, want: "profile/mixed"},
		{name: "unusable with a malformed name", check: SlotCheck{Index: 4, Profile: SlotProfile{Name: "Mixed"}}, want: "slot-profiles.toml[4]"},
		{name: "unusable with no name", check: SlotCheck{Index: 0}, want: "slot-profiles.toml[0]"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.check.Label(); got != tc.want {
				t.Errorf("Label() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestValidateSlotsWithoutRequiredCallback checks that SlotDrivers.Required
// may be nil. If it regresses, a caller that has no partner rule to report
// crashes wt while it builds the picker's rows.
func TestValidateSlotsWithoutRequiredCallback(t *testing.T) {
	d := slotTestDrivers()
	d.Required = nil
	p := validWrappedSlot()
	p.Wrapper = nil // rule 13 would fail this with the callback set
	checks := ValidateSlots(SlotStore{Profiles: []SlotProfile{p}}, d)
	if len(checks) != 1 || checks[0].Err != nil {
		t.Errorf("ValidateSlots() with a nil Required = %+v, want one passing check", checks)
	}
}

// TestValidateSlotsDeclaredIsACopy checks that a check's Declared list is
// its own. If it regresses, a caller that sorts or trims one profile's list
// reorders the slots of every other profile and of the driver itself.
func TestValidateSlotsDeclaredIsACopy(t *testing.T) {
	shared := []string{"fable", "opus", "sonnet", "haiku"}
	d := slotTestDrivers()
	d.Slots = func(agent string) []string { return shared }
	checks := ValidateSlots(SlotStore{Profiles: []SlotProfile{validClaudeSlot()}}, d)
	checks[0].Declared[0] = "changed"
	if shared[0] != "fable" {
		t.Errorf("writing to Declared changed the driver's own slot list to %v", shared)
	}
}
```

- [ ] **Step 11: Run it and see it fail**

```bash
cd wt && go test ./internal/profiles -run 'TestValidateSlots|TestSlotCheckLabel'
```

Expected: the package does not compile. The first error is

```
internal/profiles/slot_validate_test.go:17:24: undefined: SlotDrivers
```

followed by `undefined: ValidateSlots`, `undefined: ErrSlotDuplicate` and `undefined: SlotCheck`, and the run ends with `FAIL	github.com/ohanaverse/local-ai-setup/wt/internal/profiles [build failed]`.

- [ ] **Step 12: Write the implementation**

Create `wt/internal/profiles/slot_validate.go`:

```go
package profiles

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
)

// SlotDrivers is how ValidateSlots asks about agents without importing
// internal/agents. Required may be nil; the other four must be set.
type SlotDrivers struct {
	Known       func(agent string) bool
	Slots       func(agent string) []string // nil: the agent has no slot support
	ReservedEnv func(agent string) []string
	Mechanisms  func(agent string) []Mechanism
	Required    func(agent string, m Mechanism) (Mechanism, bool)
}

// ErrSlotDuplicate is the static error of every profile that shares its
// name with another profile of the same agent.
var ErrSlotDuplicate = errors.New("duplicate profile name")

// SlotCheck is one profile's static verdict.
type SlotCheck struct {
	Index     int // 0-based position in the file
	Profile   SlotProfile
	Usable    bool     // the name is well formed and the agent is known and slot-capable
	Duplicate bool     // Usable, and another Usable profile has the same Agent and Name
	Declared  []string // the agent's slot names in display order; nil when !Usable
	Err       error    // the first failing static rule; nil when the profile passes
}

// Label names the profile in a message: Profile.ID() for a Usable check,
// else "slot-profiles.toml[<Index>]", since an unusable profile has no id a
// row or a pin could carry.
func (c SlotCheck) Label() string {
	if c.Usable {
		return c.Profile.ID()
	}
	return fmt.Sprintf("slot-profiles.toml[%d]", c.Index)
}

// slotNameRE is a well-formed profile name: lowercase letters, digits and
// hyphens, starting with a letter or digit so `-M profile/<name>` can never
// read as a flag.
var slotNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// ValidateSlots returns one SlotCheck per profile, in file order. It reads
// the file's content and the drivers' declarations only: whether a slot's
// model exists, is routable or is running is resolved validation, done
// where rows are built. One profile's error never changes another's
// verdict, except that profiles sharing a name for one agent are all
// duplicates.
func ValidateSlots(store SlotStore, d SlotDrivers) []SlotCheck {
	type nameKey struct{ agent, name string }
	checks := make([]SlotCheck, len(store.Profiles))
	sharing := map[nameKey]int{}
	for i, p := range store.Profiles {
		c := SlotCheck{Index: i, Profile: p}
		if err := slotIdentityErr(p, d); err != nil {
			c.Err = err
		} else {
			c.Usable = true
			c.Declared = slices.Clone(d.Slots(p.Agent))
			sharing[nameKey{p.Agent, p.Name}]++
		}
		checks[i] = c
	}
	for i := range checks {
		c := &checks[i]
		if !c.Usable {
			continue
		}
		if sharing[nameKey{c.Profile.Agent, c.Profile.Name}] > 1 {
			c.Duplicate = true
			c.Err = ErrSlotDuplicate
			continue
		}
		c.Err = slotBodyErr(c.Profile, c.Declared, d)
	}
	return checks
}

// slotIdentityErr applies rules 1 to 5, the ones that decide whether a
// profile has a usable name and agent at all.
func slotIdentityErr(p SlotProfile, d SlotDrivers) error {
	switch {
	case p.Name == "":
		return errors.New("name is missing")
	case !slotNameRE.MatchString(p.Name):
		return fmt.Errorf("name %q is malformed: use lowercase letters, digits and hyphens", p.Name)
	case p.Agent == "":
		return errors.New("agent is missing")
	case !d.Known(p.Agent):
		return fmt.Errorf("agent %q is unknown", p.Agent)
	case len(d.Slots(p.Agent)) == 0:
		return fmt.Errorf("agent %q does not support slot profiles", p.Agent)
	}
	return nil
}

// slotBodyErr applies rules 7 to 16 to a profile whose name and agent are
// usable, and returns the first that fails. Map keys are walked in sorted
// order, so the same file always reports the same error.
func slotBodyErr(p SlotProfile, declared []string, d SlotDrivers) error {
	if len(p.Slots) == 0 {
		return errors.New("slots is empty: assign at least one slot")
	}
	slotNames := slices.Sorted(maps.Keys(p.Slots))
	for _, slot := range slotNames {
		if !slices.Contains(declared, slot) {
			return fmt.Errorf("slot %q is not a slot of agent %q (slots: %s)", slot, p.Agent, strings.Join(declared, ", "))
		}
	}
	for _, slot := range slotNames {
		if p.Slots[slot] == "" {
			return fmt.Errorf("slot %q names no model", slot)
		}
	}
	if p.Default == "" {
		return errors.New("default is missing")
	}
	if _, ok := p.Slots[p.Default]; !ok {
		return fmt.Errorf("default %q is not a slot this profile fills", p.Default)
	}

	// The same four fields, judged "used" exactly as an automatic profile's are.
	used := usedMechanisms(Profile{Env: p.Env, Args: p.Args, ConfigContent: p.ConfigContent, Wrapper: p.Wrapper})
	allowed := d.Mechanisms(p.Agent)
	for _, mech := range used {
		if !slices.Contains(allowed, mech) {
			return fmt.Errorf("agent %q does not support mechanism %q", p.Agent, mech)
		}
	}
	if d.Required != nil {
		for _, mech := range used {
			if req, ok := d.Required(p.Agent, mech); ok && !slices.Contains(used, req) {
				return fmt.Errorf("mechanism %q has no effect without %q on the same profile", mech, req)
			}
		}
	}

	reserved := d.ReservedEnv(p.Agent)
	for _, key := range slices.Sorted(maps.Keys(p.Env)) {
		if slices.Contains(reserved, key) {
			return fmt.Errorf("env sets %s, which wt sets for the launch", key)
		}
	}
	// A config_content.env that is not a table is not judged here: the
	// agent's slot build refuses it.
	if env, ok := p.ConfigContent["env"].(map[string]any); ok {
		for _, key := range slices.Sorted(maps.Keys(env)) {
			if slices.Contains(reserved, key) {
				return fmt.Errorf("config_content.env sets %s, which wt sets for the launch", key)
			}
		}
	}

	if p.Wrapper != nil && !slices.Contains(p.Wrapper.ArgsTemplate, "{{args}}") {
		return fmt.Errorf("wrapper args_template %v has no \"{{args}}\" placeholder", p.Wrapper.ArgsTemplate)
	}
	return nil
}
```

Two things to keep when editing this file later:

- Rules 1 to 5 run for every profile before rule 6 is decided, because rule 6 counts only usable profiles. A profile with an unknown agent never makes another profile a duplicate.
- A duplicate's `Err` is the `ErrSlotDuplicate` value itself, not a wrapped copy, so `errors.Is(c.Err, ErrSlotDuplicate)` and `c.Err.Error() == "duplicate profile name"` both hold.

- [ ] **Step 13: Run the tests and see them pass**

```bash
cd wt && go test ./internal/profiles -run 'TestValidateSlots|TestSlotCheckLabel' -v
```

Expected: `--- PASS` for `TestValidateSlotsRules` (42 subtests), `TestValidateSlotsDuplicateNames`, `TestValidateSlotsJudgesEachProfileAlone`, `TestSlotCheckLabel` (three subtests), `TestValidateSlotsWithoutRequiredCallback` and `TestValidateSlotsDeclaredIsACopy`, then `ok  	github.com/ohanaverse/local-ai-setup/wt/internal/profiles`.

- [ ] **Step 14: Format, vet and commit**

```bash
cd wt && gofmt -l ./internal/profiles && go vet ./internal/profiles && go test ./internal/profiles
```

Expected: `gofmt` and `go vet` print nothing; the last line is `ok  	github.com/ohanaverse/local-ai-setup/wt/internal/profiles`.

```bash
git add wt/internal/profiles/slot_validate.go wt/internal/profiles/slot_validate_test.go
git commit -m "feat(wt): static validation of slot profiles, one verdict per profile"
```

#### Behaviour 4: the contract fixture

- [ ] **Step 15: Write the failing test**

Create `wt/internal/profiles/slot_fixture_test.go`. It reads the fixture by a path relative to the package directory, as `internal/usage/usage_fixture_test.go` reads its own. It reuses `slotTestDrivers` from `slot_validate_test.go` (Step 10), whose `claude` agent has the four slots, the two mechanisms and the eleven reserved keys the real driver declares in Task 3.

```go
package profiles

import (
	"os"
	"reflect"
	"testing"
)

// TestSlotProfilesFixtureMatchesTheParser reads the contract fixture
// docs/contracts/slot-profiles.sample.toml, the documented shape of
// ~/.config/agent-wt/slot-profiles.toml. It pins every field the fixture
// sets and checks that both profiles pass static validation for an agent
// with Claude's slots, mechanisms and reserved keys. If the parser and the
// documented format drift apart, a file written from the documentation
// loads with a field missing or as a blocked row; this test fails first.
func TestSlotProfilesFixtureMatchesTheParser(t *testing.T) {
	data, err := os.ReadFile("../../../docs/contracts/slot-profiles.sample.toml")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	store, err := ParseSlots(data)
	if err != nil {
		t.Fatalf("ParseSlots(fixture) error = %v, want nil", err)
	}
	want := []SlotProfile{
		{
			Name:    "mixed",
			Agent:   "claude",
			Default: "sonnet",
			Slots: map[string]string{
				"opus":   "openrouter/z-ai/glm-5.3-flash",
				"sonnet": "ollama/glm-5.3-flash:cloud",
				"haiku":  "omlx/mlx-community--Qwen3.8-27B-4bit",
			},
			Env: map[string]string{"MAX_THINKING_TOKENS": "4096"},
			ConfigContent: map[string]any{
				"modelOverrides": map[string]any{"claude-sonnet-4-6": "ollama/deepseek-v4-pro:cloud"},
			},
		},
		{
			Name:    "one-slot",
			Agent:   "claude",
			Default: "opus",
			Slots:   map[string]string{"opus": "ollama/glm-5.2:cloud"},
		},
	}
	if !reflect.DeepEqual(store.Profiles, want) {
		t.Fatalf("fixture profiles =\n%#v\nwant\n%#v", store.Profiles, want)
	}

	checks := ValidateSlots(store, slotTestDrivers())
	if len(checks) != len(want) {
		t.Fatalf("ValidateSlots(fixture) gave %d checks, want %d", len(checks), len(want))
	}
	for _, c := range checks {
		if !c.Usable || c.Duplicate || c.Err != nil {
			t.Errorf("%s: Usable=%v Duplicate=%v Err=%v, want a passing profile", c.Label(), c.Usable, c.Duplicate, c.Err)
		}
	}
	if got, want := checks[0].Profile.FilledOrder(checks[0].Declared), []string{"sonnet", "opus", "haiku"}; !reflect.DeepEqual(got, want) {
		t.Errorf("mixed: FilledOrder() = %v, want %v (the default, then the declared order)", got, want)
	}
	if got, want := checks[1].Label(), "profile/one-slot"; got != want {
		t.Errorf("checks[1].Label() = %q, want %q", got, want)
	}
}
```

- [ ] **Step 16: Run it and see it fail**

```bash
cd wt && go test ./internal/profiles -run TestSlotProfilesFixtureMatchesTheParser
```

Expected:

```
--- FAIL: TestSlotProfilesFixtureMatchesTheParser (0.00s)
    slot_fixture_test.go:19: read fixture: open ../../../docs/contracts/slot-profiles.sample.toml: no such file or directory
FAIL
```

- [ ] **Step 17: Write the fixture**

Create `docs/contracts/slot-profiles.sample.toml` with exactly this content. The model ids are illustrative; no test looks them up in a registry.

```toml
# Contract fixture for ~/.config/agent-wt/slot-profiles.toml, read by
# wt/internal/profiles/slot_fixture_test.go and wt/internal/agents/slots_test.go.
# The model ids are illustrative.

[[profile]]
name    = "mixed"
agent   = "claude"
default = "sonnet"

[profile.slots]
opus   = "openrouter/z-ai/glm-5.3-flash"
sonnet = "ollama/glm-5.3-flash:cloud"
haiku  = "omlx/mlx-community--Qwen3.8-27B-4bit"

[profile.env]
MAX_THINKING_TOKENS = "4096"

[profile.config_content.modelOverrides]
"claude-sonnet-4-6" = "ollama/deepseek-v4-pro:cloud"

[[profile]]
name    = "one-slot"
agent   = "claude"
default = "opus"

[profile.slots]
opus = "ollama/glm-5.2:cloud"
```

- [ ] **Step 18: Run the tests and see them pass**

```bash
cd wt && go test ./internal/profiles -run TestSlotProfilesFixtureMatchesTheParser -v
```

Expected: `--- PASS: TestSlotProfilesFixtureMatchesTheParser`, then `ok  	github.com/ohanaverse/local-ai-setup/wt/internal/profiles`.

- [ ] **Step 19: Run the whole module's checks**

```bash
cd wt && gofmt -l . && go build ./... && go vet ./... && go test ./internal/profiles ./internal/agents ./cmd/wt
```

Expected: `gofmt`, `go build` and `go vet` print nothing, and each of the three packages reports `ok`. The other two packages are run because they import `profiles`; this task changes nothing they use.

```bash
cd wt && GOMAXPROCS=1 go test -count=1 ./internal/profiles
```

Expected: `ok  	github.com/ohanaverse/local-ai-setup/wt/internal/profiles`. CI runs the suite this way too.

- [ ] **Step 20: Commit**

```bash
git add docs/contracts/slot-profiles.sample.toml wt/internal/profiles/slot_fixture_test.go
git commit -m "test(wt): a contract fixture for slot-profiles.toml, read by the parser's own test"
```

### Task 3: Claude slot build

This task gives the claude driver its slot support and adds the agents-level builder that turns a resolved slot profile into an `exec.Cmd`. Nothing calls the new code yet, so `wt` behaves as before.

Words used below. A **slot** is one of Claude Code's model tiers: `fable`, `opus`, `sonnet`, `haiku`. A **slot profile** assigns a registry model to each slot. A **registry id** is a model's `id` in `registry.toml` (for example `ollama/glm-5.3-flash:cloud`); the LiteLLM proxy knows the model by that id. The **provider-side name** is the model's `model_name` (`glm-5.3-flash:cloud`). Every model id in this task is illustrative.

What the build must produce (spec, "The Claude slot build"):

- Process env: `ANTHROPIC_BASE_URL=<proxy origin>`, `ANTHROPIC_AUTH_TOKEN=<proxy key>`, `ANTHROPIC_API_KEY=` (empty).
- Removed from the inherited env: every `ANTHROPIC_DEFAULT_*` variable, `ANTHROPIC_SMALL_FAST_MODEL`, `CLAUDE_CODE_SUBAGENT_MODEL`.
- Args: the yolo flag when asked, `--model <default slot>`, `--settings <json>`.
- The JSON holds the profile's `config_content` plus an `env` block. In that block, from low to high precedence: the profile's `env`, `config_content.env`, then wt's own variables (`ANTHROPIC_DEFAULT_<SLOT>_MODEL`, `ANTHROPIC_DEFAULT_<SLOT>_MODEL_NAME` for all four slots, `ANTHROPIC_BASE_URL`, an empty `ANTHROPIC_API_KEY`).
- The proxy key is never in the JSON or anywhere in argv. wt writes no file.

**Files:**
- Create: `wt/internal/agents/claude_slots.go`
- Create: `wt/internal/agents/slots.go`
- Create: `wt/internal/agents/testdata/claude_slots_settings.golden.json`
- Test: `wt/internal/agents/claude_slots_test.go`
- Test: `wt/internal/agents/slots_test.go`

No existing file is modified. `claudeDriver` (declared in `wt/internal/agents/claude.go:16`) gains methods from the new file `claude_slots.go`.

**Interfaces:**

- Consumes, from Task 2 (`wt/internal/profiles/slot.go`, `slot_validate.go`):

  ```go
  type SlotLaunch struct {
  	Agent         string
  	Models        map[string]config.Model // every slot the agent declares → its model; an unfilled slot holds the default slot's model
  	Default       string                  // the default slot's name
  	Env           map[string]string       // the profile's env
  	ConfigContent map[string]any          // the profile's config_content
  }
  type SlotBuildInput struct {
  	SlotLaunch
  	ProxyOrigin string // the [litellm] url, no trailing slash
  	ProxyKey    string // the [litellm] api key
  	Yolo        bool
  }
  type SlotCommand struct {
  	Bin              string
  	Args             []string
  	Env              []string // "KEY=VALUE", appended after the filtered inherited environment
  	ClearEnv         []string // exact names removed from the inherited environment
  	ClearEnvPrefixes []string // names with one of these prefixes are removed from the inherited environment
  }
  type SlotCapable interface {
  	SlotNames() []string
  	ReservedEnvKeys() []string
  	BuildSlots(in SlotBuildInput) (SlotCommand, error)
  }
  type SlotDrivers struct {
  	Known       func(agent string) bool
  	Slots       func(agent string) []string // nil: the agent has no slot support
  	ReservedEnv func(agent string) []string
  	Mechanisms  func(agent string) []Mechanism
  	Required    func(agent string, m Mechanism) (Mechanism, bool)
  }
  type SlotCheck struct {
  	Index     int
  	Profile   SlotProfile
  	Usable    bool
  	Duplicate bool
  	Declared  []string
  	Err       error
  }
  func MergeConfigContent(dst, src map[string]any) // tables merge key by key, recursively; a scalar or an array replaces
  func ParseSlots(data []byte) (SlotStore, error)
  func ValidateSlots(store SlotStore, d SlotDrivers) []SlotCheck
  ```

  and the fixture `docs/contracts/slot-profiles.sample.toml` (two claude profiles, `mixed` and `one-slot`).

- Consumes, existing code:

  ```go
  // wt/internal/agents/claude.go
  type claudeDriver struct{}                                   // :16
  func (claudeDriver) YoloFlag() string                        // :18, "--dangerously-skip-permissions"
  func (claudeDriver) ProfileMechanisms() []profiles.Mechanism // :85, {env, config_file}
  // wt/internal/agents/agents.go
  func ByName(name string) Driver                // :269, nil when unknown
  func SetDir(cmd *exec.Cmd, dir string)         // :403, sets cmd.Dir and replaces PWD in cmd.Env
  func filterEnv(env, clear []string) []string   // :419, exact-name match only
  // wt/internal/profiles/profiles.go
  type ProfileCapable interface{ ProfileMechanisms() []Mechanism }                                    // :29
  type MechanismRequirer interface{ RequiredMechanism(m Mechanism) (required Mechanism, ok bool) }    // :42
  // wt/internal/config/config.go
  func (c *Config) LitellmBaseURL() string             // :101, the url with trailing slashes trimmed
  func (c *Config) LitellmAPIKey() string              // :104
  func (c *Config) SetLitellmForTest(s LitellmState)   // :109, LitellmState{Enabled bool; URL, APIKey string}
  var ErrLitellmUnconfigured = errors.New("litellm routing required but not configured") // :120
  // wt/internal/agents/claude_test.go (test helpers, same package)
  func assertEnv(t *testing.T, env []string, key, want string)   // :12, checks the FIRST entry for key
  func assertEnvMissing(t *testing.T, env []string, key string)  // :28
  func argsFlagValue(args []string, flag string) (string, bool)  // :39
  ```

- Produces:

  ```go
  // wt/internal/agents/claude_slots.go
  var claudeSlots = []string{"fable", "opus", "sonnet", "haiku"}
  func claudeSlotModelKey(slot string) string          // "ANTHROPIC_DEFAULT_" + upper(slot) + "_MODEL"
  func (claudeDriver) SlotNames() []string             // a copy of claudeSlots
  func (claudeDriver) ReservedEnvKeys() []string       // eleven keys
  func (claudeDriver) BuildSlots(in profiles.SlotBuildInput) (profiles.SlotCommand, error)
  func (claudeDriver) RefusedSlotPassthrough() []string // {"--settings", "--model"}

  // wt/internal/agents/slots.go
  type SlotPassthroughRefuser interface{ RefusedSlotPassthrough() []string }
  func SlotsFor(agent string) (profiles.SlotCapable, bool)
  func SlotDrivers() profiles.SlotDrivers
  func CheckSlotPassthrough(agent string, extraArgs []string) error
  func BuildSlotCommand(sl profiles.SlotLaunch, yolo bool, proxyOrigin, proxyKey string) (profiles.SlotCommand, error)
  func BuildSlotLaunchCmd(sl profiles.SlotLaunch, worktreePath string, yolo bool, cfg *config.Config, extraArgs []string) (*exec.Cmd, error)
  func filterEnvPrefixes(env, prefixes []string) []string
  ```

  Error texts later tasks may match:

  | From | Text |
  |---|---|
  | `CheckSlotPassthrough`, `BuildSlotLaunchCmd` | `a slot profile sets --model itself: remove it from the arguments after --` (the flag named is the one found) |
  | `BuildSlotCommand`, `BuildSlotLaunchCmd` | `agent "codex" does not support slot profiles` |
  | `BuildSlotLaunchCmd` | `a slot profile always uses the LiteLLM proxy, but no URL is configured — run 'wt litellm set --url ... --api-key ...': litellm routing required but not configured` (wraps `config.ErrLitellmUnconfigured`) |
  | `BuildSlotLaunchCmd` | `a slot profile always uses the LiteLLM proxy, but no API key is configured — run 'wt litellm set --url ... --api-key ...': litellm routing required but not configured` (wraps `config.ErrLitellmUnconfigured`) |
  | `BuildSlotLaunchCmd` | `agent claude not installed` |
  | `claudeDriver.BuildSlots` | `default slot "turbo" is not a claude slot`, `slot haiku has no model`, `config_content.env must be a table` |

**Rules for this task:**

- Package `internal/agents` may import `internal/config` and `internal/profiles` only (of wt's own packages). Do not import `catalog`, `lifecycle` or `tui`.
- This package's `TestMain` isolates git only. No test here may call `config.Dir()` or read the config home. Build every `*config.Config` in memory.
- No code in this task reads `cfg.IsLitellm()`. A slot profile uses the proxy whatever the toggle says.
- Every `Test*` function has a comment saying what it pins and what breaks for the user if it regresses.
- Run every command from the repository root. Each command block starts there: a `cd wt && …` line does not carry over to the next block.

**Choices where the spec is silent** (each follows the surrounding code):

- `CheckSlotPassthrough` compares every argument after `--` literally. It does not work out which arguments are another flag's value. A false refusal is the safe side.
- `BuildSlotLaunchCmd` treats a nil config as an empty one, as `BuildLaunchCmdInfo` does (`agents.go:346`), so it reports "no URL is configured".
- An inherited `ANTHROPIC_BASE_URL` stays in the list and wt's value is appended after it. The child sees the last value. The single-model claude build does the same (`claude.go:73`).
- `BuildSlots` checks the default slot first, then `config_content.env`, then that every slot has a model.
- `json.Marshal` writes `<`, `>` and `&` as `\u003c`, `\u003e` and `\u0026`. That is valid JSON, and Claude Code decodes it to the same text.
- The test profile adds a `SHARED` key to both `env` and `config_content.env`, beyond what the contract fixture holds, so the golden file shows which one wins.

**Review Focus (plan header, item 3):** passthrough `-- --settings=x.json` or `-- --model=opus` (the `=` form) is refused with the flag named, exactly as the two-argument form is, and no command is built. Pinned by `TestCheckSlotPassthrough` (Step 11) and `TestBuildSlotLaunchCmdRefusesPassthrough` (Step 15).

---

- [ ] **Step 1: Check that Task 2 is in place**

Run from the repo root:

```bash
cd wt && go doc ./internal/profiles SlotCapable && ls ../docs/contracts/slot-profiles.sample.toml
```

Expected: the `type SlotCapable interface` declaration with its three methods, then the fixture's path. If `go doc` prints `doc: no symbol SlotCapable in package ...`, Task 2 is not merged; stop.

#### Behaviour 1: claude's slot names, reserved env keys and refused flags

- [ ] **Step 2: Write the failing test**

Create `wt/internal/agents/claude_slots_test.go`:


```go
package agents

import (
	"slices"
	"testing"
)

// TestClaudeSlotNamesAndReservedKeys pins the slots claude declares, in
// display order, and the eleven env keys it reserves. If the order regresses
// the picker's slot line and `wt profile show` list slots in the wrong order;
// if a reserved key goes missing a profile can set it and static validation
// no longer says so.
func TestClaudeSlotNamesAndReservedKeys(t *testing.T) {
	d := claudeDriver{}
	wantSlots := []string{"fable", "opus", "sonnet", "haiku"}
	if got := d.SlotNames(); !slices.Equal(got, wantSlots) {
		t.Fatalf("SlotNames = %v, want %v", got, wantSlots)
	}
	// A caller that sorts or edits the result must not change the driver.
	d.SlotNames()[0] = "changed"
	if got := d.SlotNames(); !slices.Equal(got, wantSlots) {
		t.Fatalf("SlotNames after a caller edited its copy = %v, want %v", got, wantSlots)
	}
	wantKeys := []string{
		"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY",
		"ANTHROPIC_DEFAULT_FABLE_MODEL", "ANTHROPIC_DEFAULT_FABLE_MODEL_NAME",
		"ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL_NAME",
		"ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL_NAME",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL_NAME",
	}
	if got := d.ReservedEnvKeys(); !slices.Equal(got, wantKeys) {
		t.Fatalf("ReservedEnvKeys = %v, want %v", got, wantKeys)
	}
	r, ok := Driver(d).(SlotPassthroughRefuser)
	if !ok {
		t.Fatal("claudeDriver does not implement SlotPassthroughRefuser")
	}
	if got, want := r.RefusedSlotPassthrough(), []string{"--settings", "--model"}; !slices.Equal(got, want) {
		t.Fatalf("RefusedSlotPassthrough = %v, want %v", got, want)
	}
}
```

- [ ] **Step 3: Run it and see it fail**

```bash
cd wt && go test ./internal/agents -run TestClaudeSlotNamesAndReservedKeys
```

Expected: a build failure that includes these lines.

```
internal/agents/claude_slots_test.go:16:14: d.SlotNames undefined (type claudeDriver has no field or method SlotNames)
internal/agents/claude_slots_test.go:31:14: d.ReservedEnvKeys undefined (type claudeDriver has no field or method ReservedEnvKeys)
internal/agents/claude_slots_test.go:34:22: undefined: SlotPassthroughRefuser
FAIL	github.com/ohanaverse/local-ai-setup/wt/internal/agents [build failed]
```

- [ ] **Step 4: Write the implementation**

Create `wt/internal/agents/claude_slots.go`:


```go
package agents

import (
	"slices"
	"strings"
)

// claudeSlots are Claude Code's model slots, in display order.
var claudeSlots = []string{"fable", "opus", "sonnet", "haiku"}

// claudeSlotModelKey is the variable Claude Code reads for one slot's model:
// "ANTHROPIC_DEFAULT_" + upper(slot) + "_MODEL". The same name with "_NAME"
// appended is the label Claude Code's own /model picker shows for it.
func claudeSlotModelKey(slot string) string {
	return "ANTHROPIC_DEFAULT_" + strings.ToUpper(slot) + "_MODEL"
}

// SlotNames returns a copy, so a caller cannot reorder the driver's list.
func (claudeDriver) SlotNames() []string { return slices.Clone(claudeSlots) }

// ReservedEnvKeys are the variables wt sets for a slot-profile launch. A
// profile may not set them, in env or under config_content.env.
func (claudeDriver) ReservedEnvKeys() []string {
	keys := []string{"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY"}
	for _, slot := range claudeSlots {
		keys = append(keys, claudeSlotModelKey(slot), claudeSlotModelKey(slot)+"_NAME")
	}
	return keys
}

// RefusedSlotPassthrough names the flags that, passed after `--`, would
// replace the slot mapping: --settings carries it and --model picks the slot.
func (claudeDriver) RefusedSlotPassthrough() []string { return []string{"--settings", "--model"} }
```

Create `wt/internal/agents/slots.go`. It holds only the interface for now; Steps 13 and 17 fill it.


```go
package agents

// SlotPassthroughRefuser is an optional capability of a slot-capable
// driver: the flags that, passed after `--`, would replace the slot mapping.
type SlotPassthroughRefuser interface {
	RefusedSlotPassthrough() []string
}
```

- [ ] **Step 5: Run the test and see it pass**

```bash
cd wt && go test ./internal/agents -run TestClaudeSlotNamesAndReservedKeys
```

Expected: `ok  	github.com/ohanaverse/local-ai-setup/wt/internal/agents`

#### Behaviour 2: the slot build and its `--settings` payload

- [ ] **Step 6: Write the failing tests**

In `wt/internal/agents/claude_slots_test.go`, replace the import block with:


```go
import (
	"bytes"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)
```

Append to the same file:


```go
// slotTestKey is the proxy key the slot tests use. Tests search the argv for
// it to prove the key stays out of the process list.
const slotTestKey = "sk-slot-test"

// slotTestLaunch is a resolved claude profile: sonnet is the default, fable is
// unfilled and so holds sonnet's model, and haiku is a local model. The ids
// are illustrative. Every call returns fresh maps.
func slotTestLaunch() profiles.SlotLaunch {
	opus := config.Model{ID: "openrouter/z-ai/glm-5.3-flash", ProviderID: "openrouter", ModelName: "z-ai/glm-5.3-flash", Location: config.LocationCloud}
	sonnet := config.Model{ID: "ollama/glm-5.3-flash:cloud", ProviderID: "ollama", ModelName: "glm-5.3-flash:cloud", Location: config.LocationCloud}
	haiku := config.Model{ID: "omlx/mlx-community--Qwen3.8-27B-4bit", ProviderID: "omlx", ModelName: "mlx-community--Qwen3.8-27B-4bit", Location: config.LocationLocal}
	return profiles.SlotLaunch{
		Agent:   "claude",
		Default: "sonnet",
		Models:  map[string]config.Model{"fable": sonnet, "opus": opus, "sonnet": sonnet, "haiku": haiku},
		Env: map[string]string{
			"MAX_THINKING_TOKENS": "4096",
			"SHARED":              "from-env",
		},
		ConfigContent: map[string]any{
			"modelOverrides": map[string]any{"claude-sonnet-4-6": "ollama/deepseek-v4-pro:cloud"},
			"env": map[string]any{
				"SHARED":                     "from-config-content",
				"CLAUDE_CODE_SUBAGENT_MODEL": "ollama/glm-5.3-flash:cloud",
			},
		},
	}
}

// slotTestInput is slotTestLaunch with the proxy origin and key a launch adds.
func slotTestInput(yolo bool) profiles.SlotBuildInput {
	return profiles.SlotBuildInput{
		SlotLaunch:  slotTestLaunch(),
		ProxyOrigin: "http://localhost:4000",
		ProxyKey:    slotTestKey,
		Yolo:        yolo,
	}
}

// slotSettings decodes the --settings value of a built command.
func slotSettings(t *testing.T, args []string) map[string]any {
	t.Helper()
	raw, ok := argsFlagValue(args, "--settings")
	if !ok {
		t.Fatalf("no --settings in %v", args)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		t.Fatalf("--settings is not a JSON object: %v\n%s", err, raw)
	}
	return obj
}

// TestClaudeBuildSlotsCommand pins everything of the slot build except the
// payload: the binary, the argument order with and without yolo, the process
// env, and what is removed from the inherited env. If it regresses a profile
// session starts on the wrong slot, talks to Anthropic instead of the proxy,
// or shows the proxy key in the process list.
func TestClaudeBuildSlotsCommand(t *testing.T) {
	for _, tc := range []struct {
		name     string
		yolo     bool
		wantHead []string
	}{
		{"plain", false, []string{"--model", "sonnet", "--settings"}},
		{"yolo", true, []string{"--dangerously-skip-permissions", "--model", "sonnet", "--settings"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sc, err := claudeDriver{}.BuildSlots(slotTestInput(tc.yolo))
			if err != nil {
				t.Fatal(err)
			}
			if sc.Bin != "claude" {
				t.Errorf("Bin = %q, want claude", sc.Bin)
			}
			if len(sc.Args) != len(tc.wantHead)+1 || !slices.Equal(sc.Args[:len(tc.wantHead)], tc.wantHead) {
				t.Fatalf("Args = %v, want %v followed by the payload", sc.Args, tc.wantHead)
			}
			wantEnv := []string{
				"ANTHROPIC_BASE_URL=http://localhost:4000",
				"ANTHROPIC_AUTH_TOKEN=" + slotTestKey,
				"ANTHROPIC_API_KEY=",
			}
			if !slices.Equal(sc.Env, wantEnv) {
				t.Errorf("Env = %v, want %v", sc.Env, wantEnv)
			}
			if want := []string{"ANTHROPIC_SMALL_FAST_MODEL", "CLAUDE_CODE_SUBAGENT_MODEL"}; !slices.Equal(sc.ClearEnv, want) {
				t.Errorf("ClearEnv = %v, want %v", sc.ClearEnv, want)
			}
			if want := []string{"ANTHROPIC_DEFAULT_"}; !slices.Equal(sc.ClearEnvPrefixes, want) {
				t.Errorf("ClearEnvPrefixes = %v, want %v", sc.ClearEnvPrefixes, want)
			}
			for _, a := range sc.Args {
				if strings.Contains(a, slotTestKey) || strings.Contains(a, "ANTHROPIC_AUTH_TOKEN") {
					t.Errorf("the proxy key reached argv: %s", a)
				}
			}
		})
	}
}

// TestClaudeBuildSlotsSettingsGolden compares the whole --settings payload
// with the golden file: the unfilled fable slot holds the default's model, a
// _NAME variable beside each slot, the profile's env under config_content.env
// under wt's own variables, config_content merged in, and no token. If it
// regresses a slot sends an Anthropic id the proxy rejects, or the payload
// carries the proxy key.
func TestClaudeBuildSlotsSettingsGolden(t *testing.T) {
	sc, err := claudeDriver{}.BuildSlots(slotTestInput(false))
	if err != nil {
		t.Fatal(err)
	}
	raw, ok := argsFlagValue(sc.Args, "--settings")
	if !ok {
		t.Fatalf("no --settings in %v", sc.Args)
	}
	var got bytes.Buffer
	if err := json.Indent(&got, []byte(raw), "", "  "); err != nil {
		t.Fatalf("--settings is not JSON: %v\n%s", err, raw)
	}
	got.WriteByte('\n')
	want, err := os.ReadFile("testdata/claude_slots_settings.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Bytes(), want) {
		t.Errorf("--settings payload differs from testdata/claude_slots_settings.golden.json\n--- got\n%s--- want\n%s", got.Bytes(), want)
	}
}

// TestClaudeBuildSlotsEnvPrecedence pins the two properties the golden file
// cannot show. wt's own variables win even over a reserved key that reached
// the build (static validation rejects one, so this is the second line of
// defence against a session redirected away from the proxy). And the build
// leaves the profile's maps alone: the same resolved profile is built again
// by `wt profile show` and by a retried launch.
func TestClaudeBuildSlotsEnvPrecedence(t *testing.T) {
	in := slotTestInput(false)
	in.Env["ANTHROPIC_DEFAULT_OPUS_MODEL"] = "from-env"
	in.ConfigContent["env"].(map[string]any)["ANTHROPIC_BASE_URL"] = "http://elsewhere.invalid"
	sc, err := claudeDriver{}.BuildSlots(in)
	if err != nil {
		t.Fatal(err)
	}
	env, ok := slotSettings(t, sc.Args)["env"].(map[string]any)
	if !ok {
		t.Fatal("--settings has no env table")
	}
	for key, want := range map[string]string{
		"ANTHROPIC_DEFAULT_OPUS_MODEL": "openrouter/z-ai/glm-5.3-flash",
		"ANTHROPIC_BASE_URL":           "http://localhost:4000",
		"SHARED":                       "from-config-content",
		"MAX_THINKING_TOKENS":          "4096",
	} {
		if got := env[key]; got != want {
			t.Errorf("env[%s] = %v, want %q", key, got, want)
		}
	}

	if got := in.Env["ANTHROPIC_DEFAULT_OPUS_MODEL"]; got != "from-env" {
		t.Errorf("the build changed the profile's env: %q", got)
	}
	if len(in.Env) != 3 {
		t.Errorf("the build added keys to the profile's env: %v", in.Env)
	}
	cc := in.ConfigContent["env"].(map[string]any)
	if len(cc) != 3 || cc["ANTHROPIC_BASE_URL"] != "http://elsewhere.invalid" {
		t.Errorf("the build changed config_content.env: %v", cc)
	}
}

// TestClaudeBuildSlotsErrors pins the three inputs the build refuses. Each
// would otherwise launch a session with a slot left on an Anthropic id, or
// with a settings payload Claude Code rejects.
func TestClaudeBuildSlotsErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(in *profiles.SlotBuildInput)
		want string
	}{
		{"a declared slot has no model", func(in *profiles.SlotBuildInput) { delete(in.Models, "haiku") }, "slot haiku has no model"},
		{"config_content.env is not a table", func(in *profiles.SlotBuildInput) { in.ConfigContent["env"] = "X=1" }, "config_content.env must be a table"},
		{"the default is not a claude slot", func(in *profiles.SlotBuildInput) { in.Default = "turbo" }, `default slot "turbo" is not a claude slot`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := slotTestInput(false)
			tc.edit(&in)
			sc, err := claudeDriver{}.BuildSlots(in)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if sc.Bin != "" || len(sc.Args) != 0 || len(sc.Env) != 0 {
				t.Errorf("a refused build returned a command: %+v", sc)
			}
		})
	}
}
```

- [ ] **Step 7: Run them and see them fail**

```bash
cd wt && go test ./internal/agents -run TestClaudeBuildSlots
```

Expected: a build failure. Each of the four new tests reports this line.

```
claudeDriver{}.BuildSlots undefined (type claudeDriver has no field or method BuildSlots)
FAIL	github.com/ohanaverse/local-ai-setup/wt/internal/agents [build failed]
```

- [ ] **Step 8: Write the implementation**

In `wt/internal/agents/claude_slots.go`, replace the import block with:


```go
import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)
```

Append to the same file:


```go
// BuildSlots builds the command of a slot-profile launch. The slot mapping
// travels in one inline --settings JSON, so wt writes no settings file. The
// proxy key is set in the process environment only: argv is visible in the
// process list, and the key must not be.
func (claudeDriver) BuildSlots(in profiles.SlotBuildInput) (profiles.SlotCommand, error) {
	if !slices.Contains(claudeSlots, in.Default) {
		return profiles.SlotCommand{}, fmt.Errorf("default slot %q is not a claude slot", in.Default)
	}
	payload, err := claudeSlotSettings(in)
	if err != nil {
		return profiles.SlotCommand{}, err
	}
	var args []string
	if in.Yolo {
		args = append(args, claudeDriver{}.YoloFlag())
	}
	args = append(args, "--model", in.Default, "--settings", payload)
	return profiles.SlotCommand{
		Bin:  "claude",
		Args: args,
		Env: []string{
			"ANTHROPIC_BASE_URL=" + in.ProxyOrigin,
			"ANTHROPIC_AUTH_TOKEN=" + in.ProxyKey,
			"ANTHROPIC_API_KEY=",
		},
		// A stray shell export of any of these could send a tier to an id
		// the profile never chose.
		ClearEnv:         []string{"ANTHROPIC_SMALL_FAST_MODEL", "CLAUDE_CODE_SUBAGENT_MODEL"},
		ClearEnvPrefixes: []string{"ANTHROPIC_DEFAULT_"},
	}, nil
}

// claudeSlotSettings returns the --settings payload: the profile's
// config_content, with an env block whose precedence, low to high, is the
// profile's env, config_content.env, then wt's own variables. It never holds
// ANTHROPIC_AUTH_TOKEN, and it mutates neither in.Env nor in.ConfigContent.
func claudeSlotSettings(in profiles.SlotBuildInput) (string, error) {
	env := map[string]any{}
	for k, v := range in.Env {
		env[k] = v
	}
	if raw, ok := in.ConfigContent["env"]; ok {
		table, ok := raw.(map[string]any)
		if !ok {
			return "", errors.New("config_content.env must be a table")
		}
		for k, v := range table {
			env[k] = v
		}
	}
	for _, slot := range claudeSlots {
		m, ok := in.Models[slot]
		if !ok {
			return "", fmt.Errorf("slot %s has no model", slot)
		}
		env[claudeSlotModelKey(slot)] = m.ID
		env[claudeSlotModelKey(slot)+"_NAME"] = m.ModelName
	}
	// Repeated here so an env block in the user's own settings files cannot
	// redirect the session.
	env["ANTHROPIC_BASE_URL"] = in.ProxyOrigin
	env["ANTHROPIC_API_KEY"] = ""

	obj := map[string]any{}
	profiles.MergeConfigContent(obj, in.ConfigContent)
	// obj["env"] is replaced, not written into: after the merge it is the
	// profile's own config_content.env table.
	obj["env"] = env
	payload, err := json.Marshal(obj)
	if err != nil {
		return "", fmt.Errorf("encode --settings: %w", err)
	}
	return string(payload), nil
}
```

Why `obj["env"]` is assigned and not merged into: `MergeConfigContent` stores the profile's own `config_content.env` table in `obj` by reference. Writing wt's keys into it would change the profile. The fresh `env` map already holds a copy of that table's entries.

- [ ] **Step 9: Run the tests; the golden test fails, then add the golden file**

```bash
cd wt && go test ./internal/agents -run TestClaudeBuildSlots
```

Expected: three tests pass and one fails.

```
--- FAIL: TestClaudeBuildSlotsSettingsGolden (0.00s)
    claude_slots_test.go:173: open testdata/claude_slots_settings.golden.json: no such file or directory
```

Create the directory `wt/internal/agents/testdata/` and the file `wt/internal/agents/testdata/claude_slots_settings.golden.json` with exactly this content. Two-space indent, keys in byte order, one newline after the last `}`. There is no update flag: when the payload changes on purpose, this file is edited by hand.


```json
{
  "env": {
    "ANTHROPIC_API_KEY": "",
    "ANTHROPIC_BASE_URL": "http://localhost:4000",
    "ANTHROPIC_DEFAULT_FABLE_MODEL": "ollama/glm-5.3-flash:cloud",
    "ANTHROPIC_DEFAULT_FABLE_MODEL_NAME": "glm-5.3-flash:cloud",
    "ANTHROPIC_DEFAULT_HAIKU_MODEL": "omlx/mlx-community--Qwen3.8-27B-4bit",
    "ANTHROPIC_DEFAULT_HAIKU_MODEL_NAME": "mlx-community--Qwen3.8-27B-4bit",
    "ANTHROPIC_DEFAULT_OPUS_MODEL": "openrouter/z-ai/glm-5.3-flash",
    "ANTHROPIC_DEFAULT_OPUS_MODEL_NAME": "z-ai/glm-5.3-flash",
    "ANTHROPIC_DEFAULT_SONNET_MODEL": "ollama/glm-5.3-flash:cloud",
    "ANTHROPIC_DEFAULT_SONNET_MODEL_NAME": "glm-5.3-flash:cloud",
    "CLAUDE_CODE_SUBAGENT_MODEL": "ollama/glm-5.3-flash:cloud",
    "MAX_THINKING_TOKENS": "4096",
    "SHARED": "from-config-content"
  },
  "modelOverrides": {
    "claude-sonnet-4-6": "ollama/deepseek-v4-pro:cloud"
  }
}
```

What the file shows: `fable` is unfilled and holds the default slot's (sonnet's) model; each slot has its `_NAME`; `SHARED` is `from-config-content`, so `config_content.env` beat the profile's `env`; `modelOverrides` came through from `config_content`; there is no `ANTHROPIC_AUTH_TOKEN`.

- [ ] **Step 10: Run the tests and see them pass**

```bash
cd wt && go test ./internal/agents -run 'TestClaudeSlot|TestClaudeBuildSlots' -v 2>&1 | grep -E '^(--- |ok|FAIL)'
```

Expected: `--- PASS` for `TestClaudeSlotNamesAndReservedKeys`, `TestClaudeBuildSlotsCommand`, `TestClaudeBuildSlotsSettingsGolden`, `TestClaudeBuildSlotsEnvPrecedence` and `TestClaudeBuildSlotsErrors`, then `ok  	github.com/ohanaverse/local-ai-setup/wt/internal/agents`.

#### Behaviour 3: driver lookups and the passthrough refusal

- [ ] **Step 11: Write the failing tests**

Create `wt/internal/agents/slots_test.go`. `TestSlotFixtureIsValidForTheRealClaudeDriver` and `TestSlotDriversRejectsWhatClaudeDoesNotAllow` run Task 2's `ValidateSlots` with the real drivers; the three error texts in the second are Task 2's static rules 12, 14 and 8.


```go
package agents

import (
	"os"
	"slices"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)

// TestSlotsFor pins which agents have slot support. If claude loses it every
// claude slot profile is reported as "does not support slot profiles"; if
// another agent gains it by accident its profiles pass static validation and
// fail at launch.
func TestSlotsFor(t *testing.T) {
	for _, tc := range []struct {
		agent string
		want  bool
	}{
		{"claude", true},
		{"codex", false},
		{"opencode", false},
		{"shell", false},
		{"no-such-agent", false},
	} {
		sc, ok := SlotsFor(tc.agent)
		if ok != tc.want {
			t.Errorf("SlotsFor(%q) ok = %v, want %v", tc.agent, ok, tc.want)
		}
		if ok && sc == nil {
			t.Errorf("SlotsFor(%q) returned ok with a nil value", tc.agent)
		}
	}
}

// TestSlotDriversAnswersFromTheRegisteredDrivers pins each callback static
// validation calls. If one is wired to the wrong driver capability a valid
// profile is blocked, or an invalid one reaches the launch.
func TestSlotDriversAnswersFromTheRegisteredDrivers(t *testing.T) {
	d := SlotDrivers()
	if !d.Known("claude") || !d.Known("codex") || d.Known("no-such-agent") {
		t.Errorf("Known: claude=%v codex=%v no-such-agent=%v, want true true false",
			d.Known("claude"), d.Known("codex"), d.Known("no-such-agent"))
	}
	if got, want := d.Slots("claude"), []string{"fable", "opus", "sonnet", "haiku"}; !slices.Equal(got, want) {
		t.Errorf("Slots(claude) = %v, want %v", got, want)
	}
	if got := d.Slots("codex"); got != nil {
		t.Errorf("Slots(codex) = %v, want nil (no slot support)", got)
	}
	if got := d.Slots("no-such-agent"); got != nil {
		t.Errorf("Slots(no-such-agent) = %v, want nil", got)
	}
	if got := d.ReservedEnv("claude"); len(got) != 11 || !slices.Contains(got, "ANTHROPIC_DEFAULT_HAIKU_MODEL_NAME") {
		t.Errorf("ReservedEnv(claude) = %v, want claude's eleven keys", got)
	}
	if got := d.ReservedEnv("codex"); got != nil {
		t.Errorf("ReservedEnv(codex) = %v, want nil", got)
	}
	if got, want := d.Mechanisms("claude"), []profiles.Mechanism{profiles.MechanismEnv, profiles.MechanismConfigFile}; !slices.Equal(got, want) {
		t.Errorf("Mechanisms(claude) = %v, want %v", got, want)
	}
	if got := d.Mechanisms("no-such-agent"); got != nil {
		t.Errorf("Mechanisms(no-such-agent) = %v, want nil", got)
	}
	// pi is the one driver with a required partner: env needs a wrapper.
	if need, ok := d.Required("pi", profiles.MechanismEnv); !ok || need != profiles.MechanismWrapper {
		t.Errorf("Required(pi, env) = %q, %v, want wrapper, true", need, ok)
	}
	if _, ok := d.Required("claude", profiles.MechanismEnv); ok {
		t.Error("Required(claude, env) reported a partner; claude has none")
	}
	if _, ok := d.Required("no-such-agent", profiles.MechanismEnv); ok {
		t.Error("Required(no-such-agent, env) reported a partner")
	}
}

// TestSlotFixtureIsValidForTheRealClaudeDriver reads the documented sample
// file and validates it against the registered drivers, not test closures.
// If the driver's slot names, mechanisms or reserved keys drift from the
// documented format, a user who copies the sample gets blocked rows.
func TestSlotFixtureIsValidForTheRealClaudeDriver(t *testing.T) {
	data, err := os.ReadFile("../../../docs/contracts/slot-profiles.sample.toml")
	if err != nil {
		t.Fatal(err)
	}
	store, err := profiles.ParseSlots(data)
	if err != nil {
		t.Fatal(err)
	}
	checks := profiles.ValidateSlots(store, SlotDrivers())
	if len(checks) != 2 {
		t.Fatalf("got %d checks, want 2 (the fixture's two profiles)", len(checks))
	}
	for _, c := range checks {
		if !c.Usable || c.Err != nil {
			t.Errorf("profile %q: Usable = %v, Err = %v; want a valid profile", c.Profile.Name, c.Usable, c.Err)
		}
		if want := []string{"fable", "opus", "sonnet", "haiku"}; !slices.Equal(c.Declared, want) {
			t.Errorf("profile %q: Declared = %v, want %v", c.Profile.Name, c.Declared, want)
		}
	}
}

// TestSlotDriversRejectsWhatClaudeDoesNotAllow runs static validation with
// the real drivers over three profiles only the driver can refuse. If it
// regresses, a claude profile with args, a reserved env key or an unknown
// slot passes validation, and its launch drops the setting silently or
// overrides the slot mapping.
func TestSlotDriversRejectsWhatClaudeDoesNotAllow(t *testing.T) {
	const head = "[[profile]]\nname = \"p\"\nagent = \"claude\"\ndefault = \"opus\"\n"
	for _, tc := range []struct {
		name, toml, want string
	}{
		{"args",
			head + "args = [\"--verbose\"]\n[profile.slots]\nopus = \"ollama/a\"\n",
			`agent "claude" does not support mechanism "args"`},
		{"reserved env key",
			head + "[profile.slots]\nopus = \"ollama/a\"\n[profile.env]\nANTHROPIC_DEFAULT_OPUS_MODEL = \"x\"\n",
			"env sets ANTHROPIC_DEFAULT_OPUS_MODEL, which wt sets for the launch"},
		{"unknown slot",
			head + "[profile.slots]\nopus = \"ollama/a\"\nturbo = \"ollama/a\"\n",
			`slot "turbo" is not a slot of agent "claude" (slots: fable, opus, sonnet, haiku)`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := profiles.ParseSlots([]byte(tc.toml))
			if err != nil {
				t.Fatal(err)
			}
			checks := profiles.ValidateSlots(store, SlotDrivers())
			if len(checks) != 1 || checks[0].Err == nil || checks[0].Err.Error() != tc.want {
				t.Fatalf("checks = %+v, want one check with Err %q", checks, tc.want)
			}
		})
	}
}

// TestCheckSlotPassthrough pins which arguments after `--` a claude slot
// profile refuses, in the two-argument form and in the "=" form. Either flag
// would replace the slot mapping, so a regression lets a profile session run
// on models the profile never chose. Flags that only share a prefix
// (--fallback-model) and agents that refuse nothing pass.
func TestCheckSlotPassthrough(t *testing.T) {
	for _, tc := range []struct {
		name  string
		agent string
		args  []string
		want  string // "" means no error; otherwise the flag the error names
	}{
		{"no args", "claude", nil, ""},
		{"harmless flags", "claude", []string{"--continue", "--verbose"}, ""},
		{"settings, two arguments", "claude", []string{"--settings", "x.json"}, "--settings"},
		{"settings, = form", "claude", []string{"--settings=x.json"}, "--settings"},
		{"model, two arguments", "claude", []string{"--model", "opus"}, "--model"},
		{"model, = form", "claude", []string{"--model=opus"}, "--model"},
		{"after other flags", "claude", []string{"--continue", "--model=opus"}, "--model"},
		{"the first refused flag is named", "claude", []string{"--model", "opus", "--settings", "x.json"}, "--model"},
		{"a flag that only shares a prefix", "claude", []string{"--fallback-model", "sonnet", "--setting-sources", "user"}, ""},
		{"an agent that refuses nothing", "codex", []string{"--model", "x"}, ""},
		{"an unknown agent", "no-such-agent", []string{"--model", "x"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckSlotPassthrough(tc.agent, tc.args)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("err = %v, want nil", err)
				}
				return
			}
			want := "a slot profile sets " + tc.want + " itself: remove it from the arguments after --"
			if err == nil || err.Error() != want {
				t.Fatalf("err = %v, want %q", err, want)
			}
		})
	}
}
```

- [ ] **Step 12: Run them and see them fail**

```bash
cd wt && go test ./internal/agents -run 'TestSlotsFor|TestSlotDrivers|TestSlotFixture|TestCheckSlotPassthrough'
```

Expected: a build failure that includes these lines.

```
internal/agents/slots_test.go:26:13: undefined: SlotsFor
internal/agents/slots_test.go:40:7: undefined: SlotDrivers
internal/agents/slots_test.go:163:11: undefined: CheckSlotPassthrough
FAIL	github.com/ohanaverse/local-ai-setup/wt/internal/agents [build failed]
```

- [ ] **Step 13: Write the implementation**

Replace the whole of `wt/internal/agents/slots.go` with:


```go
package agents

import (
	"fmt"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)

// SlotPassthroughRefuser is an optional capability of a slot-capable
// driver: the flags that, passed after `--`, would replace the slot mapping.
type SlotPassthroughRefuser interface {
	RefusedSlotPassthrough() []string
}

// SlotsFor returns the agent's slot support. ok is false for an unknown
// agent and for a driver without profiles.SlotCapable.
func SlotsFor(agent string) (profiles.SlotCapable, bool) {
	d := ByName(agent)
	if d == nil {
		return nil, false
	}
	sc, ok := d.(profiles.SlotCapable)
	return sc, ok
}

// SlotDrivers is the profiles.SlotDrivers backed by the registered drivers.
// internal/profiles cannot import this package, so static validation asks
// about agents through these callbacks.
func SlotDrivers() profiles.SlotDrivers {
	return profiles.SlotDrivers{
		Known: func(agent string) bool { return ByName(agent) != nil },
		Slots: func(agent string) []string {
			sc, ok := SlotsFor(agent)
			if !ok {
				return nil
			}
			return sc.SlotNames()
		},
		ReservedEnv: func(agent string) []string {
			sc, ok := SlotsFor(agent)
			if !ok {
				return nil
			}
			return sc.ReservedEnvKeys()
		},
		Mechanisms: func(agent string) []profiles.Mechanism {
			pc, ok := ByName(agent).(profiles.ProfileCapable)
			if !ok {
				return nil
			}
			return pc.ProfileMechanisms()
		},
		Required: func(agent string, m profiles.Mechanism) (profiles.Mechanism, bool) {
			mr, ok := ByName(agent).(profiles.MechanismRequirer)
			if !ok {
				return "", false
			}
			return mr.RequiredMechanism(m)
		},
	}
}

// CheckSlotPassthrough returns an error naming the first argument of
// extraArgs that is one of the agent's refused flags, either alone
// ("--model") or in its "=" form ("--model=opus"). It returns nil when there
// is none, or when the driver refuses nothing.
func CheckSlotPassthrough(agent string, extraArgs []string) error {
	refuser, ok := ByName(agent).(SlotPassthroughRefuser)
	if !ok {
		return nil
	}
	refused := refuser.RefusedSlotPassthrough()
	for _, arg := range extraArgs {
		for _, flag := range refused {
			if arg == flag || strings.HasPrefix(arg, flag+"=") {
				return fmt.Errorf("a slot profile sets %s itself: remove it from the arguments after --", flag)
			}
		}
	}
	return nil
}
```

`ByName(agent).(profiles.ProfileCapable)` is safe for an unknown agent: a type assertion on a nil interface value yields `ok == false`.

`--fallback-model` and `--setting-sources` are real Claude Code flags that share a prefix with the refused ones. The match is the whole flag or the flag followed by `=`, so they pass.

- [ ] **Step 14: Run the tests and see them pass**

```bash
cd wt && go test ./internal/agents -run 'TestSlotsFor|TestSlotDrivers|TestSlotFixture|TestCheckSlotPassthrough'
```

Expected: `ok  	github.com/ohanaverse/local-ai-setup/wt/internal/agents`

If `TestSlotDriversRejectsWhatClaudeDoesNotAllow` fails on a message's wording only, the wording belongs to Task 2: compare it with `wt/internal/profiles/slot_validate.go` and report the difference. Do not change either side to make it pass.

#### Behaviour 4: the command builder

- [ ] **Step 15: Write the failing tests**

In `wt/internal/agents/slots_test.go`, replace the import block with:


```go
import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)
```

Append to the same file:


```go
// slotTestCfg is a config whose [litellm] table has a url and a key and whose
// toggle is OFF: the slot path must use the proxy anyway. It is built in
// memory, so no test here reads the config home.
func slotTestCfg() *config.Config {
	cfg := &config.Config{}
	cfg.SetLitellmForTest(config.LitellmState{Enabled: false, URL: "http://localhost:4000/", APIKey: slotTestKey})
	return cfg
}

// fakeSlotClaude puts an executable named claude first on PATH and returns
// its path.
func fakeSlotClaude(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return bin
}

// envValues returns every value env holds for key, in order.
func envValues(env []string, key string) []string {
	var out []string
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, key+"="); ok {
			out = append(out, v)
		}
	}
	return out
}

// TestFilterEnvPrefixes pins the prefix filter: it drops a variable whose
// name starts with a prefix, keeps one that only contains it or carries it in
// its value, and keeps everything when there are no prefixes. If it regresses
// a stray ANTHROPIC_DEFAULT_* export survives into a profile session.
func TestFilterEnvPrefixes(t *testing.T) {
	env := []string{
		"ANTHROPIC_DEFAULT_OPUS_MODEL=stray",
		"ANTHROPIC_DEFAULT_=bare",
		"MY_ANTHROPIC_DEFAULT_X=keep",
		"NOTE=ANTHROPIC_DEFAULT_OPUS_MODEL",
		"ANTHROPIC_BASE_URL=keep",
		"NOVALUE",
	}
	got := filterEnvPrefixes(env, []string{"ANTHROPIC_DEFAULT_"})
	want := []string{"MY_ANTHROPIC_DEFAULT_X=keep", "NOTE=ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_BASE_URL=keep", "NOVALUE"}
	if !slices.Equal(got, want) {
		t.Errorf("filterEnvPrefixes = %v, want %v", got, want)
	}
	if got := filterEnvPrefixes(env, nil); !slices.Equal(got, env) {
		t.Errorf("filterEnvPrefixes with no prefixes = %v, want the input unchanged", got)
	}
}

// TestBuildSlotCommand pins the build `wt profile show` prints: the driver's
// command, with whatever key the caller passes and no binary lookup. If it
// regresses, `wt profile show` either needs claude installed or prints
// something other than what a launch runs.
func TestBuildSlotCommand(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no claude anywhere: the build must not look
	sc, err := BuildSlotCommand(slotTestLaunch(), true, "http://localhost:4000", "…test")
	if err != nil {
		t.Fatal(err)
	}
	if sc.Bin != "claude" || sc.Args[0] != "--dangerously-skip-permissions" {
		t.Errorf("command = %s %v, want claude with the yolo flag first", sc.Bin, sc.Args)
	}
	if got := envValues(sc.Env, "ANTHROPIC_AUTH_TOKEN"); !slices.Equal(got, []string{"…test"}) {
		t.Errorf("ANTHROPIC_AUTH_TOKEN = %v, want the key the caller passed", got)
	}

	sl := slotTestLaunch()
	sl.Agent = "codex"
	if _, err := BuildSlotCommand(sl, false, "http://localhost:4000", slotTestKey); err == nil || err.Error() != `agent "codex" does not support slot profiles` {
		t.Errorf("err = %v, want the no-slot-support error", err)
	}
}

// TestBuildSlotLaunchCmd pins the treatment a slot-profile command gets, the
// same a single-model launch gets: the binary found on PATH, the stray model
// variables removed from the inherited environment, the proxy variables set
// from [litellm] although its toggle is off, the worktree as directory and
// PWD, and the user's arguments appended unchanged. If it regresses a profile
// session opens the caller's directory, follows a shell export to a model the
// profile never chose, or drops the user's flags. It also pins that building
// writes nothing into the worktree.
func TestBuildSlotLaunchCmd(t *testing.T) {
	bin := fakeSlotClaude(t)
	work := t.TempDir()
	t.Setenv("PWD", "/stale/caller")
	t.Setenv("ANTHROPIC_DEFAULT_OPUS_MODEL", "stray-opus")
	t.Setenv("ANTHROPIC_DEFAULT_HAIKU_MODEL_NAME", "stray-name")
	t.Setenv("ANTHROPIC_SMALL_FAST_MODEL", "stray-fast")
	t.Setenv("CLAUDE_CODE_SUBAGENT_MODEL", "stray-subagent")
	t.Setenv("ANTHROPIC_BASE_URL", "http://stale.invalid")
	t.Setenv("WT_SLOT_TEST_KEEP", "1")

	cmd, err := BuildSlotLaunchCmd(slotTestLaunch(), work, true, slotTestCfg(), []string{"--continue", "--verbose"})
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Path != bin {
		t.Errorf("Path = %q, want the claude on PATH %q", cmd.Path, bin)
	}
	if cmd.Dir != work {
		t.Errorf("Dir = %q, want %q", cmd.Dir, work)
	}
	if got := envValues(cmd.Env, "PWD"); !slices.Equal(got, []string{work}) {
		t.Errorf("PWD entries = %v, want exactly %q", got, work)
	}

	// cmd.Args[0] is the binary; then wt's flags, the payload, the user's args.
	wantHead := []string{"--dangerously-skip-permissions", "--model", "sonnet", "--settings"}
	args := cmd.Args[1:]
	if len(args) != len(wantHead)+3 || !slices.Equal(args[:len(wantHead)], wantHead) {
		t.Fatalf("args = %v, want %v, the payload, then the passthrough args", args, wantHead)
	}
	if got, want := args[len(args)-2:], []string{"--continue", "--verbose"}; !slices.Equal(got, want) {
		t.Errorf("trailing args = %v, want the passthrough args %v unchanged", got, want)
	}
	for _, a := range cmd.Args {
		if strings.Contains(a, slotTestKey) {
			t.Errorf("the proxy key reached argv: %s", a)
		}
	}

	for _, key := range []string{
		"ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL_NAME",
		"ANTHROPIC_SMALL_FAST_MODEL", "CLAUDE_CODE_SUBAGENT_MODEL",
	} {
		assertEnvMissing(t, cmd.Env, key)
	}
	assertEnv(t, cmd.Env, "WT_SLOT_TEST_KEEP", "1")
	// An inherited value of a variable wt sets is still in the list; the
	// child sees the last one. The url's trailing slash is trimmed.
	if got := envValues(cmd.Env, "ANTHROPIC_AUTH_TOKEN"); len(got) == 0 || got[len(got)-1] != slotTestKey {
		t.Errorf("ANTHROPIC_AUTH_TOKEN values = %v, want the last to be the [litellm] key", got)
	}
	if got := envValues(cmd.Env, "ANTHROPIC_BASE_URL"); len(got) == 0 || got[len(got)-1] != "http://localhost:4000" {
		t.Errorf("ANTHROPIC_BASE_URL values = %v, want the last to be the proxy origin", got)
	}
	if got := envValues(cmd.Env, "ANTHROPIC_API_KEY"); len(got) == 0 || got[len(got)-1] != "" {
		t.Errorf("ANTHROPIC_API_KEY values = %v, want the last to be empty", got)
	}

	env, _ := slotSettings(t, args)["env"].(map[string]any)
	if got := env["ANTHROPIC_DEFAULT_HAIKU_MODEL"]; got != "omlx/mlx-community--Qwen3.8-27B-4bit" {
		t.Errorf("payload haiku model = %v, want the profile's", got)
	}
	if got := env["ANTHROPIC_BASE_URL"]; got != "http://localhost:4000" {
		t.Errorf("payload ANTHROPIC_BASE_URL = %v, want the proxy origin", got)
	}

	left, err := os.ReadDir(work)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Errorf("the build wrote into the worktree: %v", left)
	}
}

// TestBuildSlotLaunchCmdRefusesPassthrough pins that --settings or --model
// after `--` stops the build, in the "=" form exactly as in the two-argument
// form, with the flag named, before anything else is looked at: there is no
// claude on PATH here and the refusal still comes first. If it regresses a
// user's `-- --model=opus` silently replaces the profile's default slot.
func TestBuildSlotLaunchCmdRefusesPassthrough(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, tc := range []struct {
		name string
		args []string
		flag string
	}{
		{"settings, two arguments", []string{"--settings", "x.json"}, "--settings"},
		{"settings, = form", []string{"--settings=x.json"}, "--settings"},
		{"model, two arguments", []string{"--model", "opus"}, "--model"},
		{"model, = form", []string{"--model=opus"}, "--model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd, err := BuildSlotLaunchCmd(slotTestLaunch(), t.TempDir(), false, slotTestCfg(), tc.args)
			want := "a slot profile sets " + tc.flag + " itself: remove it from the arguments after --"
			if err == nil || err.Error() != want {
				t.Fatalf("err = %v, want %q", err, want)
			}
			if cmd != nil {
				t.Errorf("a refused launch built a command: %v", cmd.Args)
			}
		})
	}
}

// TestBuildSlotLaunchCmdErrors pins every other reason no command is built:
// [litellm] without a url or a key (whatever its toggle says), an agent
// without slot support, and a missing binary. If one regresses, a profile
// launch starts an agent that cannot reach the proxy, or fails with an
// unreadable exec error.
func TestBuildSlotLaunchCmdErrors(t *testing.T) {
	withLitellm := func(s config.LitellmState) *config.Config {
		cfg := &config.Config{}
		cfg.SetLitellmForTest(s)
		return cfg
	}
	codex := slotTestLaunch()
	codex.Agent = "codex"
	for _, tc := range []struct {
		name         string
		sl           profiles.SlotLaunch
		cfg          *config.Config
		installed    bool
		want         string
		unconfigured bool
	}{
		{"no url, toggle on", slotTestLaunch(), withLitellm(config.LitellmState{Enabled: true, APIKey: slotTestKey}), true,
			"a slot profile always uses the LiteLLM proxy, but no URL is configured — run 'wt litellm set --url ... --api-key ...': litellm routing required but not configured", true},
		{"no key, toggle on", slotTestLaunch(), withLitellm(config.LitellmState{Enabled: true, URL: "http://localhost:4000"}), true,
			"a slot profile always uses the LiteLLM proxy, but no API key is configured — run 'wt litellm set --url ... --api-key ...': litellm routing required but not configured", true},
		{"nil config", slotTestLaunch(), nil, true,
			"a slot profile always uses the LiteLLM proxy, but no URL is configured — run 'wt litellm set --url ... --api-key ...': litellm routing required but not configured", true},
		{"agent without slot support", codex, slotTestCfg(), true, `agent "codex" does not support slot profiles`, false},
		{"claude not installed", slotTestLaunch(), slotTestCfg(), false, "agent claude not installed", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.installed {
				fakeSlotClaude(t)
			} else {
				t.Setenv("PATH", t.TempDir())
			}
			cmd, err := BuildSlotLaunchCmd(tc.sl, t.TempDir(), false, tc.cfg, nil)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if got := errors.Is(err, config.ErrLitellmUnconfigured); got != tc.unconfigured {
				t.Errorf("errors.Is(err, config.ErrLitellmUnconfigured) = %v, want %v", got, tc.unconfigured)
			}
			if cmd != nil {
				t.Errorf("a failed build returned a command: %v", cmd.Args)
			}
		})
	}
}
```

- [ ] **Step 16: Run them and see them fail**

```bash
cd wt && go test ./internal/agents -run 'TestFilterEnvPrefixes|TestBuildSlot'
```

Expected: a build failure that includes these lines.

```
undefined: filterEnvPrefixes
undefined: BuildSlotCommand
undefined: BuildSlotLaunchCmd
FAIL	github.com/ohanaverse/local-ai-setup/wt/internal/agents [build failed]
```

- [ ] **Step 17: Write the implementation**

In `wt/internal/agents/slots.go`, replace the import block with:


```go
import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)
```

Append to the same file:


```go
// BuildSlotCommand runs the agent's slot build and nothing else: no binary
// lookup, no environment. `wt profile show` calls it with a masked key.
func BuildSlotCommand(sl profiles.SlotLaunch, yolo bool, proxyOrigin, proxyKey string) (profiles.SlotCommand, error) {
	sc, ok := SlotsFor(sl.Agent)
	if !ok {
		return profiles.SlotCommand{}, fmt.Errorf("agent %q does not support slot profiles", sl.Agent)
	}
	return sc.BuildSlots(profiles.SlotBuildInput{
		SlotLaunch:  sl,
		ProxyOrigin: proxyOrigin,
		ProxyKey:    proxyKey,
		Yolo:        yolo,
	})
}

// BuildSlotLaunchCmd builds the exec.Cmd of a slot-profile launch, giving the
// driver's slot build the treatment BuildLaunchCmdInfo gives a single-model
// launch: binary lookup, the inherited environment filtered, the worktree
// directory and PWD, and the user's passthrough args appended unchanged.
//
// It reads the [litellm] url and key directly and never consults
// cfg.IsLitellm(): a slot profile uses the proxy whatever the toggle says. It
// resolves no route, runs no Syncer and applies no automatic profile.
func BuildSlotLaunchCmd(sl profiles.SlotLaunch, worktreePath string, yolo bool, cfg *config.Config, extraArgs []string) (*exec.Cmd, error) {
	if err := CheckSlotPassthrough(sl.Agent, extraArgs); err != nil {
		return nil, err
	}
	if cfg == nil {
		cfg = &config.Config{}
	}
	origin, key := cfg.LitellmBaseURL(), cfg.LitellmAPIKey()
	if origin == "" {
		return nil, fmt.Errorf(
			"a slot profile always uses the LiteLLM proxy, but no URL is configured — "+
				"run 'wt litellm set --url ... --api-key ...': %w",
			config.ErrLitellmUnconfigured)
	}
	if key == "" {
		return nil, fmt.Errorf(
			"a slot profile always uses the LiteLLM proxy, but no API key is configured — "+
				"run 'wt litellm set --url ... --api-key ...': %w",
			config.ErrLitellmUnconfigured)
	}
	sc, err := BuildSlotCommand(sl, yolo, origin, key)
	if err != nil {
		return nil, err
	}
	bin, err := exec.LookPath(sc.Bin)
	if err != nil {
		return nil, fmt.Errorf("agent %s not installed", sc.Bin)
	}
	cmd := exec.Command(bin, sc.Args...)
	cmd.Env = append(filterEnvPrefixes(filterEnv(os.Environ(), sc.ClearEnv), sc.ClearEnvPrefixes), sc.Env...)
	SetDir(cmd, worktreePath)
	cmd.Args = append(cmd.Args, extraArgs...)
	return cmd, nil
}

// filterEnvPrefixes returns env without the entries whose key starts with
// one of prefixes. filterEnv matches whole names only; this removes a family
// of variables (every ANTHROPIC_DEFAULT_*) whose members wt cannot list.
func filterEnvPrefixes(env, prefixes []string) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		key := e
		if i := strings.IndexByte(e, '='); i >= 0 {
			key = e[:i]
		}
		drop := false
		for _, p := range prefixes {
			if strings.HasPrefix(key, p) {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, e)
		}
	}
	return out
}
```

The order inside `BuildSlotLaunchCmd` is fixed: the passthrough check, the `[litellm]` url and key, the driver's build, the binary lookup, the environment, the directory, the passthrough args. `TestBuildSlotLaunchCmdRefusesPassthrough` runs with no `claude` on `PATH` to pin that the refusal comes before the lookup.

- [ ] **Step 18: Run the tests and see them pass**

```bash
cd wt && go test ./internal/agents -run 'TestFilterEnvPrefixes|TestBuildSlot'
```

Expected: `ok  	github.com/ohanaverse/local-ai-setup/wt/internal/agents`

- [ ] **Step 19: Run the whole package and the module checks**

```bash
cd wt && gofmt -l internal/agents && go vet ./internal/agents && go build ./... && go test ./internal/agents
```

Expected: `gofmt` prints nothing, `go vet` and `go build` print nothing, and the last line is `ok  	github.com/ohanaverse/local-ai-setup/wt/internal/agents`. The package's existing tests still pass: no existing file changed.

- [ ] **Step 20: Commit**

Before committing, read the spec's `## Check results` table (written by Task 1). If row 1, 3 or 4 names a fallback, do the matching step below first and commit once.

```bash
git add wt/internal/agents/claude_slots.go wt/internal/agents/slots.go wt/internal/agents/claude_slots_test.go wt/internal/agents/slots_test.go wt/internal/agents/testdata/claude_slots_settings.golden.json
git commit -m "feat(wt): the claude slot build and the agents-level slot-profile command builder"
```

---

#### Only if Task 1 check 1 failed

- [ ] **Step 21: Move the slot variables to the process environment**

Do this step only when row 1 of the spec's `## Check results` table says **fail**: Claude Code does not honour an `env` block in an inline `--settings` payload. Skip it otherwise.

What changes: the slot variables, the `_NAME` variables and the profile's `env` become process environment variables (`SlotCommand.Env`), the profile's `env` first and wt's variables after, so wt's win. `--settings` carries only the profile's `config_content`, and the flag and its value are both left out when `config_content` is empty. Nothing in `slots.go` changes. One consequence to pass on to Task 11: keys under `config_content.env` still travel in the payload, where Claude Code ignores them; a profile must use `env` for variables.

1. In `wt/internal/agents/claude_slots.go`, add `"maps"` to the import block, so it reads:

```go
import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)
```

2. In the same file, replace `BuildSlots` and `claudeSlotSettings` (everything from the comment `// BuildSlots builds` to the end of the file) with:


```go
// BuildSlots builds the command of a slot-profile launch. Claude Code does not
// honour an env block in an inline --settings payload (spec, "Check results",
// check 1), so the slot mapping and the profile's env are process environment
// variables, and --settings carries only the profile's config_content. wt
// still writes no settings file, and the proxy key still stays out of argv.
func (claudeDriver) BuildSlots(in profiles.SlotBuildInput) (profiles.SlotCommand, error) {
	if !slices.Contains(claudeSlots, in.Default) {
		return profiles.SlotCommand{}, fmt.Errorf("default slot %q is not a claude slot", in.Default)
	}
	// Precedence is by position: the child sees the last value of a name. The
	// profile's env comes first and wt's own variables after it, so wt's win.
	var env []string
	for _, k := range slices.Sorted(maps.Keys(in.Env)) {
		env = append(env, k+"="+in.Env[k])
	}
	for _, slot := range claudeSlots {
		m, ok := in.Models[slot]
		if !ok {
			return profiles.SlotCommand{}, fmt.Errorf("slot %s has no model", slot)
		}
		env = append(env,
			claudeSlotModelKey(slot)+"="+m.ID,
			claudeSlotModelKey(slot)+"_NAME="+m.ModelName,
		)
	}
	env = append(env,
		"ANTHROPIC_BASE_URL="+in.ProxyOrigin,
		"ANTHROPIC_AUTH_TOKEN="+in.ProxyKey,
		"ANTHROPIC_API_KEY=",
	)
	var args []string
	if in.Yolo {
		args = append(args, claudeDriver{}.YoloFlag())
	}
	args = append(args, "--model", in.Default)
	if len(in.ConfigContent) > 0 {
		payload, err := claudeSlotSettings(in)
		if err != nil {
			return profiles.SlotCommand{}, err
		}
		args = append(args, "--settings", payload)
	}
	return profiles.SlotCommand{
		Bin:  "claude",
		Args: args,
		Env:  env,
		// A stray shell export of any of these could send a tier to an id
		// the profile never chose. The inherited ANTHROPIC_DEFAULT_* values
		// are removed before Env is appended, so wt's own survive.
		ClearEnv:         []string{"ANTHROPIC_SMALL_FAST_MODEL", "CLAUDE_CODE_SUBAGENT_MODEL"},
		ClearEnvPrefixes: []string{"ANTHROPIC_DEFAULT_"},
	}, nil
}

// claudeSlotSettings returns the --settings payload: the profile's
// config_content and nothing else. It does not mutate in.ConfigContent.
func claudeSlotSettings(in profiles.SlotBuildInput) (string, error) {
	if raw, ok := in.ConfigContent["env"]; ok {
		if _, ok := raw.(map[string]any); !ok {
			return "", errors.New("config_content.env must be a table")
		}
	}
	obj := map[string]any{}
	profiles.MergeConfigContent(obj, in.ConfigContent)
	payload, err := json.Marshal(obj)
	if err != nil {
		return "", fmt.Errorf("encode --settings: %w", err)
	}
	return string(payload), nil
}
```

3. In `wt/internal/agents/claude_slots_test.go`, replace `TestClaudeBuildSlotsCommand` (with its comment) with the first function below, add the second after it, and replace `TestClaudeBuildSlotsEnvPrecedence` (with its comment) with the third:


```go
// TestClaudeBuildSlotsCommand pins everything of the slot build except the
// payload: the binary, the argument order with and without yolo, the process
// env in precedence order, and what is removed from the inherited env. If it
// regresses a profile session starts on the wrong slot, sends a slot's
// Anthropic id to the proxy, or shows the proxy key in the process list.
func TestClaudeBuildSlotsCommand(t *testing.T) {
	for _, tc := range []struct {
		name     string
		yolo     bool
		wantHead []string
	}{
		{"plain", false, []string{"--model", "sonnet", "--settings"}},
		{"yolo", true, []string{"--dangerously-skip-permissions", "--model", "sonnet", "--settings"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sc, err := claudeDriver{}.BuildSlots(slotTestInput(tc.yolo))
			if err != nil {
				t.Fatal(err)
			}
			if sc.Bin != "claude" {
				t.Errorf("Bin = %q, want claude", sc.Bin)
			}
			if len(sc.Args) != len(tc.wantHead)+1 || !slices.Equal(sc.Args[:len(tc.wantHead)], tc.wantHead) {
				t.Fatalf("Args = %v, want %v followed by the payload", sc.Args, tc.wantHead)
			}
			// The profile's env first, in key order; then wt's own.
			wantEnv := []string{
				"MAX_THINKING_TOKENS=4096",
				"SHARED=from-env",
				"ANTHROPIC_DEFAULT_FABLE_MODEL=ollama/glm-5.3-flash:cloud",
				"ANTHROPIC_DEFAULT_FABLE_MODEL_NAME=glm-5.3-flash:cloud",
				"ANTHROPIC_DEFAULT_OPUS_MODEL=openrouter/z-ai/glm-5.3-flash",
				"ANTHROPIC_DEFAULT_OPUS_MODEL_NAME=z-ai/glm-5.3-flash",
				"ANTHROPIC_DEFAULT_SONNET_MODEL=ollama/glm-5.3-flash:cloud",
				"ANTHROPIC_DEFAULT_SONNET_MODEL_NAME=glm-5.3-flash:cloud",
				"ANTHROPIC_DEFAULT_HAIKU_MODEL=omlx/mlx-community--Qwen3.8-27B-4bit",
				"ANTHROPIC_DEFAULT_HAIKU_MODEL_NAME=mlx-community--Qwen3.8-27B-4bit",
				"ANTHROPIC_BASE_URL=http://localhost:4000",
				"ANTHROPIC_AUTH_TOKEN=" + slotTestKey,
				"ANTHROPIC_API_KEY=",
			}
			if !slices.Equal(sc.Env, wantEnv) {
				t.Errorf("Env = %v, want %v", sc.Env, wantEnv)
			}
			if want := []string{"ANTHROPIC_SMALL_FAST_MODEL", "CLAUDE_CODE_SUBAGENT_MODEL"}; !slices.Equal(sc.ClearEnv, want) {
				t.Errorf("ClearEnv = %v, want %v", sc.ClearEnv, want)
			}
			if want := []string{"ANTHROPIC_DEFAULT_"}; !slices.Equal(sc.ClearEnvPrefixes, want) {
				t.Errorf("ClearEnvPrefixes = %v, want %v", sc.ClearEnvPrefixes, want)
			}
			for _, a := range sc.Args {
				if strings.Contains(a, slotTestKey) || strings.Contains(a, "ANTHROPIC_AUTH_TOKEN") {
					t.Errorf("the proxy key reached argv: %s", a)
				}
			}
		})
	}
}

// TestClaudeBuildSlotsOmitsSettingsWithoutConfigContent pins that a profile
// with no config_content gets no --settings flag at all. If it regresses
// Claude Code is handed `--settings {}` or a dangling flag that swallows the
// user's first passthrough argument.
func TestClaudeBuildSlotsOmitsSettingsWithoutConfigContent(t *testing.T) {
	in := slotTestInput(true)
	in.ConfigContent = nil
	sc, err := claudeDriver{}.BuildSlots(in)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"--dangerously-skip-permissions", "--model", "sonnet"}; !slices.Equal(sc.Args, want) {
		t.Errorf("Args = %v, want %v", sc.Args, want)
	}
}

// TestClaudeBuildSlotsEnvPrecedence pins the two properties the other tests
// cannot show. wt's own variables come after a reserved key that reached the
// build in the profile's env, so the child sees wt's value (static validation
// rejects such a key; this is the second line of defence). And the build
// leaves the profile's maps alone: the same resolved profile is built again
// by `wt profile show` and by a retried launch.
func TestClaudeBuildSlotsEnvPrecedence(t *testing.T) {
	in := slotTestInput(false)
	in.Env["ANTHROPIC_DEFAULT_OPUS_MODEL"] = "from-env"
	sc, err := claudeDriver{}.BuildSlots(in)
	if err != nil {
		t.Fatal(err)
	}
	var opus []string
	for _, e := range sc.Env {
		if v, ok := strings.CutPrefix(e, "ANTHROPIC_DEFAULT_OPUS_MODEL="); ok {
			opus = append(opus, v)
		}
	}
	if want := []string{"from-env", "openrouter/z-ai/glm-5.3-flash"}; !slices.Equal(opus, want) {
		t.Errorf("ANTHROPIC_DEFAULT_OPUS_MODEL values = %v, want %v (wt's last)", opus, want)
	}
	if len(in.Env) != 3 || in.Env["ANTHROPIC_DEFAULT_OPUS_MODEL"] != "from-env" {
		t.Errorf("the build changed the profile's env: %v", in.Env)
	}
	if cc := in.ConfigContent["env"].(map[string]any); len(cc) != 2 {
		t.Errorf("the build changed config_content.env: %v", cc)
	}
	if len(in.ConfigContent) != 2 {
		t.Errorf("the build changed config_content: %v", in.ConfigContent)
	}
}
```

4. In the same file, replace the comment above `TestClaudeBuildSlotsSettingsGolden` with:

```go
// TestClaudeBuildSlotsSettingsGolden compares the whole --settings payload
// with the golden file: the profile's config_content and nothing wt added,
// so no slot variable and no token. If it regresses the payload carries the
// proxy key, or drops a setting the profile asked for.
```

5. Replace the whole of `wt/internal/agents/testdata/claude_slots_settings.golden.json` with:


```json
{
  "env": {
    "CLAUDE_CODE_SUBAGENT_MODEL": "ollama/glm-5.3-flash:cloud",
    "SHARED": "from-config-content"
  },
  "modelOverrides": {
    "claude-sonnet-4-6": "ollama/deepseek-v4-pro:cloud"
  }
}
```

6. In `wt/internal/agents/slots_test.go`, inside `TestBuildSlotLaunchCmd`, replace this block:


```go
	for _, key := range []string{
		"ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL_NAME",
		"ANTHROPIC_SMALL_FAST_MODEL", "CLAUDE_CODE_SUBAGENT_MODEL",
	} {
		assertEnvMissing(t, cmd.Env, key)
	}
```

with:


```go
	// The stray inherited values are gone; wt's own are the only ones left.
	if got := envValues(cmd.Env, "ANTHROPIC_DEFAULT_OPUS_MODEL"); !slices.Equal(got, []string{"openrouter/z-ai/glm-5.3-flash"}) {
		t.Errorf("ANTHROPIC_DEFAULT_OPUS_MODEL values = %v, want only the profile's", got)
	}
	if got := envValues(cmd.Env, "ANTHROPIC_DEFAULT_HAIKU_MODEL_NAME"); !slices.Equal(got, []string{"mlx-community--Qwen3.8-27B-4bit"}) {
		t.Errorf("ANTHROPIC_DEFAULT_HAIKU_MODEL_NAME values = %v, want only the profile's", got)
	}
	assertEnvMissing(t, cmd.Env, "ANTHROPIC_SMALL_FAST_MODEL")
	assertEnvMissing(t, cmd.Env, "CLAUDE_CODE_SUBAGENT_MODEL")
	if got := envValues(cmd.Env, "MAX_THINKING_TOKENS"); len(got) == 0 || got[len(got)-1] != "4096" {
		t.Errorf("MAX_THINKING_TOKENS values = %v, want the last to be the profile's", got)
	}
```

and replace this block:


```go
	env, _ := slotSettings(t, args)["env"].(map[string]any)
	if got := env["ANTHROPIC_DEFAULT_HAIKU_MODEL"]; got != "omlx/mlx-community--Qwen3.8-27B-4bit" {
		t.Errorf("payload haiku model = %v, want the profile's", got)
	}
	if got := env["ANTHROPIC_BASE_URL"]; got != "http://localhost:4000" {
		t.Errorf("payload ANTHROPIC_BASE_URL = %v, want the proxy origin", got)
	}
```

with:


```go
	if _, ok := slotSettings(t, args)["modelOverrides"]; !ok {
		t.Error("the payload lost the profile's config_content")
	}
```

7. Run:

```bash
cd wt && gofmt -l internal/agents && go vet ./internal/agents && go build ./... && go test ./internal/agents
```

Expected: `gofmt`, `go vet` and `go build` print nothing; the last line is `ok  	github.com/ohanaverse/local-ai-setup/wt/internal/agents`.

8. Commit. When Step 20 has not been committed yet, use Step 20's two commands instead and add ` (slot variables in the process env; Task 1 check 1 failed)` to the end of the message. Otherwise:

```bash
git add wt/internal/agents/claude_slots.go wt/internal/agents/claude_slots_test.go wt/internal/agents/slots_test.go wt/internal/agents/testdata/claude_slots_settings.golden.json
git commit -m "fix(wt): a claude slot profile sets its slot variables in the process env, since Claude Code ignores env in an inline --settings"
```

#### Only if Task 1 check 4 found that two `--settings` flags merge

- [ ] **Step 22: Refuse `--model` only**

Do this step only when row 4 of `## Check results` says **merge**. Any other result (last wins, first wins, an error) keeps the code above.

1. In `wt/internal/agents/claude_slots.go`, replace `RefusedSlotPassthrough` and its comment with:

```go
// RefusedSlotPassthrough names the flag that, passed after `--`, would
// replace the profile's default slot. A passthrough --settings is allowed:
// Claude Code merges it with wt's (spec, "Check results", check 4).
func (claudeDriver) RefusedSlotPassthrough() []string { return []string{"--model"} }
```

2. In `TestClaudeSlotNamesAndReservedKeys`, change `[]string{"--settings", "--model"}` to `[]string{"--model"}`.

3. In `TestCheckSlotPassthrough`, change the expected flag of the two `settings` cases from `"--settings"` to `""`:

```go
		{"settings, two arguments", "claude", []string{"--settings", "x.json"}, ""},
		{"settings, = form", "claude", []string{"--settings=x.json"}, ""},
```

4. In `TestBuildSlotLaunchCmdRefusesPassthrough`, delete the two `settings` cases, so the table holds only:

```go
		{"model, two arguments", []string{"--model", "opus"}, "--model"},
		{"model, = form", []string{"--model=opus"}, "--model"},
```

5. Run `cd wt && go test ./internal/agents`. Expected: `ok  	github.com/ohanaverse/local-ai-setup/wt/internal/agents`. Include the change in Step 20's commit, or commit it with `git commit -am "fix(wt): a claude slot profile refuses a passthrough --model only; Claude Code merges a second --settings"`.

#### Only if Task 1 check 3 failed and one of its two re-runs closed the gap

- [ ] **Step 23: Map the escaped ids**

Do this step only when row 3 of `## Check results` says **fail** and names which re-run closed the gap. If neither did, stop and report to the owner. The code below is for the primary path (Step 8); it edits `claudeSlotSettings` in `wt/internal/agents/claude_slots.go`.

When `modelOverrides` closed the gap: add this variable above `claudeSlotSettings`, filled with the ids row 3 records, one string per id.

```go
// claudeEscapedIDs are the Anthropic ids Claude Code still sent to the proxy
// with every slot mapped (spec, "Check results", check 3). The slot build
// maps each to the default slot's model through modelOverrides.
var claudeEscapedIDs = []string{}
```

and replace the two lines

```go
	obj := map[string]any{}
	profiles.MergeConfigContent(obj, in.ConfigContent)
```

with

```go
	obj := map[string]any{}
	if len(claudeEscapedIDs) > 0 {
		overrides := map[string]any{}
		for _, id := range claudeEscapedIDs {
			overrides[id] = in.Models[in.Default].ID
		}
		obj["modelOverrides"] = overrides
	}
	// Merged second, so the profile's own modelOverrides entries win.
	profiles.MergeConfigContent(obj, in.ConfigContent)
```

Then add one line per id to the `modelOverrides` object of the golden file, in byte order of the key, each with the value `"ollama/glm-5.3-flash:cloud"` (the test profile's default slot model).

When only `CLAUDE_CODE_SUBAGENT_MODEL` closed the gap: in `claudeSlotSettings`, replace the line `env := map[string]any{}` with

```go
	// Set first, so a profile's own value (env or config_content.env) wins.
	// Without it Claude Code sends an Anthropic id for subagents (spec,
	// "Check results", check 3). The key is not reserved.
	env := map[string]any{"CLAUDE_CODE_SUBAGENT_MODEL": in.Models[in.Default].ID}
```

The golden file does not change, because the test profile sets that key itself. Add this test to `claude_slots_test.go`:

```go
// TestClaudeBuildSlotsDefaultsTheSubagentModel pins that a profile that does
// not set CLAUDE_CODE_SUBAGENT_MODEL gets the default slot's model for it. If
// it regresses, subagents send an Anthropic id the proxy rejects.
func TestClaudeBuildSlotsDefaultsTheSubagentModel(t *testing.T) {
	in := slotTestInput(false)
	delete(in.ConfigContent["env"].(map[string]any), "CLAUDE_CODE_SUBAGENT_MODEL")
	sc, err := claudeDriver{}.BuildSlots(in)
	if err != nil {
		t.Fatal(err)
	}
	env, _ := slotSettings(t, sc.Args)["env"].(map[string]any)
	if got := env["CLAUDE_CODE_SUBAGENT_MODEL"]; got != "ollama/glm-5.3-flash:cloud" {
		t.Errorf("CLAUDE_CODE_SUBAGENT_MODEL = %v, want the default slot's model", got)
	}
}
```

Run `cd wt && go test ./internal/agents`. Expected: `ok  	github.com/ohanaverse/local-ai-setup/wt/internal/agents`. Include the change in Step 20's commit.

### Task 4: Resolved validation and catalog rows

This task makes `internal/catalog` able to turn a statically checked slot profile into a picker row. It adds resolved validation (the rules that need the registry, the live inventory and the `[litellm]` table), the opt-in input on `catalog.Build`, the reserved `profile` prefix, the single blocked row for duplicate names, and a test that `catalog.Find` resolves `profile/<name>`.

Nothing a user sees changes after this task: no production caller sets the new input until Task 8.

**Before you start.** Task 2 must be merged: `wt/internal/profiles/slot.go` and `wt/internal/profiles/slot_validate.go` exist. Check with `cd wt && go doc ./internal/profiles SlotCheck`; it must print the struct.

**Files:**
- Create: `wt/internal/catalog/profile.go`
- Modify: `wt/internal/catalog/catalog.go` (the import block; `type Row`; `type Input`; the last line of `Build`; the first lines of `Row.Action` and `Row.BlockReason`)
- Test: `wt/internal/catalog/profile_test.go` (new)

**Interfaces:**

Consumes, from Task 2 (`wt/internal/profiles`):

```go
const SlotReservedProvider = "profile"

type SlotProfile struct {
	Name          string
	Agent         string
	Default       string
	Slots         map[string]string // slot name → registry id
	Env           map[string]string
	Args          []string
	ConfigContent map[string]any
	Wrapper       *WrapperSpec
}
func (p SlotProfile) ID() string                               // "profile/" + Name
func (p SlotProfile) FilledOrder(declared []string) []string   // Default first, then the other filled slots in declared's order

type SlotCheck struct {
	Index     int         // 0-based position in the file
	Profile   SlotProfile
	Usable    bool        // well-formed name, known slot-capable agent
	Duplicate bool        // another Usable profile has the same Agent and Name
	Declared  []string    // the agent's slot names in display order; nil when !Usable
	Err       error       // the first failing static rule; nil when the profile passes
}
var ErrSlotDuplicate = errors.New("duplicate profile name")

type SlotLaunch struct {
	Agent         string
	Models        map[string]config.Model
	Default       string
	Env           map[string]string
	ConfigContent map[string]any
}
```

Consumes, from existing code:

```go
// internal/catalog/catalog.go
func Build(in Input) []Row
func Find(rows []Row, id string) (Row, bool)                                       // matches on Row.Model.ID
func MissingReason(cfg *config.Config, snap *localmodels.Snapshot, id string) string // "" when the snapshot cannot say why the id has no row
func (r Row) Action() Action
func (r Row) BlockReason() string
// internal/config/config.go
func IndexModelByID(models []Model, id string) int            // -1 when absent
func (c *Config) ProviderByID(id string) *Provider             // nil when absent
func (c *Config) ResolveLocation(m Model) (Location, error)
func (c *Config) LitellmBaseURL() string                       // "" when no url is configured
func (c *Config) LitellmAPIKey() string
func (c *Config) SetLitellmForTest(s LitellmState)             // tests only
// internal/litellm/policy.go
func PolicyFor(providerID string) (Policy, bool)
// internal/localmodels/inventory.go
type Snapshot struct { Entries []Entry; /* … */ }
type Entry struct { ProviderID, Artifact, ModelID, ModelName string; Registered, Running, Loading, ArtifactKnown bool; /* … */ }
```

Produces (Tasks 5, 7, 8, 9 and 10 rely on these exact names):

```go
// internal/catalog/catalog.go
type Row struct {
	// … existing fields unchanged …
	Profile *ProfileRow // set on a slot-profile row, nil on every model row
}
type Input struct {
	// … existing fields unchanged …
	SlotProfiles []profiles.SlotCheck // nil means no profile rows
}
func (r Row) Action() Action        // a profile row returns r.Profile.Verdict
func (r Row) BlockReason() string   // a profile row returns r.Profile.Reason

// internal/catalog/profile.go
type ProfileRow struct {
	Check   profiles.SlotCheck
	Models  map[string]config.Model // declared slot → resolved model
	Local   *Row                    // the one local slot model's own row, or nil
	Verdict Action
	Reason  string
	Loc     string                  // "cloud", "local", "mixed" or ""
}
func (r Row) IsProfile() bool
func SlotPrefixReserved(cfg *config.Config) bool
func ResolveSlotProfile(cfg *config.Config, snap *localmodels.Snapshot, c profiles.SlotCheck) Row
func (p *ProfileRow) Distinct() []config.Model
func (p *ProfileRow) Cloud() []config.Model
func (p *ProfileRow) SlotOf(modelID string) string
func (p *ProfileRow) Launch() profiles.SlotLaunch
func slotRows(in Input) []Row
```

**What a profile row looks like.** It is an ordinary `Row` with `Model: config.Model{ID: "profile/<name>"}` (the id and nothing else), `Location: ""`, `Status: StatusOK`, `Running` and `Loading` copied from the local model's own row, and `Profile` set. Keeping the id in `Model.ID` is what lets `Find`, the usage counts and the picker's id index work unchanged. `Find` itself is not edited.

**The resolved rules, in order.** The first one that fails blocks the row and is its reason. "Slots" means `c.Profile.FilledOrder(c.Declared)`. Each rule is checked over every slot before the next rule runs.

| # | Rule | Reason |
|---|---|---|
| 0 | `c.Err != nil` (a static failure or a duplicate name) | `c.Err.Error()` |
| 1 | The slot's id is not in `cfg.Models` | `slot %s: %s is not in the registry` |
| 2 | The model is `Native` | `slot %s: %s is a native model, which has no LiteLLM route` |
| 3a | `cfg.ProviderByID(m.ProviderID) == nil` | `slot %s: %s names provider %q, which is not in the registry` |
| 3b | `cfg.ResolveLocation(m)` fails | `slot %s: %v` |
| 3c | `litellm.PolicyFor(m.ProviderID)` is false | `slot %s: %s cannot be routed: provider %q has no LiteLLM mapping` |
| 4 | More than one distinct local model | `slots name two local models (%s and %s); a slot profile may name one` |
| 5a | `cfg.LitellmBaseURL() == ""` | `a slot profile always uses the LiteLLM proxy, but no URL is configured — run 'wt litellm set --url ... --api-key ...'` |
| 5b | `cfg.LitellmAPIKey() == ""` | `a slot profile always uses the LiteLLM proxy, but no API key is configured — run 'wt litellm set --url ... --api-key ...'` |
| 6a | The local model has no row of its own | `MissingReason(cfg, snap, id)`, or `slot %s: %s is not on disk or running` when that is `""` |
| 6b | The local model's own row has `Action() == ActionBlock` | `slot %s: ` + that row's `BlockReason()` |

After rule 6: the local row's `ActionStart` makes the profile a start row; anything else (`ActionLaunch`, or no local model) a launch row. Rule 5 never reads `cfg.IsLitellm()`: a slot profile uses the proxy whatever the on/off toggle says.

**Choices where the spec is silent** (each follows the surrounding code):

- A nil `cfg` passed to `ResolveSlotProfile` is treated as an empty registry, as `MissingReason` and `Build` tolerate a nil config.
- Rules 3a, 3b and 3c are each a full pass over the slots, like every other row of the table.
- `Local` is filled at rule 6, so a row blocked by an earlier rule has `Local == nil` and reads as not running.
- `Launch()` hands out a copy of `Models`, so a slot build cannot change a row the picker still holds.
- No result of Task 1's checks changes this task.

**Commands.** Run every command from the repository root. Each command block starts there: a `cd wt && …` line does not carry over to the next block.

**Test rules for this package.** `internal/catalog` has no `TestMain` and nothing in it reaches the config home: configs and snapshots are built in memory. Use the existing helpers `catalogTestCfg()` and `rowIDs(rows)` from `catalog_test.go`. Every `Test*` function carries a `//` comment saying what it tests and what breaks for the user if it regresses.

---

#### Behaviour 1: a row that carries a profile answers from the profile

- [ ] **Step 1: Write the failing test**

Create `wt/internal/catalog/profile_test.go` with exactly this content:

```go
// Tests for slot-profile rows: resolved validation (the rules that need the
// registry, the live inventory and the [litellm] table), the row Build emits
// for a profile, and what keeps those rows away from callers that did not ask
// for them.
package catalog

import (
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// TestProfileRowAnswersFromItsVerdict verifies that a row carrying a profile
// reports the profile's verdict and reason, not the location-and-provider
// answer a model row gives. A profile row has no location, so without this
// every profile would read as a launch row and a blocked one would start
// Claude Code with slots that cannot work.
func TestProfileRowAnswersFromItsVerdict(t *testing.T) {
	blocked := Row{Model: config.Model{ID: "profile/x"}, Profile: &ProfileRow{Verdict: ActionBlock, Reason: "slot haiku: nope"}}
	if !blocked.IsProfile() || blocked.Action() != ActionBlock || blocked.BlockReason() != "slot haiku: nope" {
		t.Errorf("blocked profile row: IsProfile=%v action=%v reason=%q", blocked.IsProfile(), blocked.Action(), blocked.BlockReason())
	}
	start := Row{Model: config.Model{ID: "profile/y"}, Profile: &ProfileRow{Verdict: ActionStart}}
	if start.Action() != ActionStart || start.BlockReason() != "" {
		t.Errorf("start profile row: action=%v reason=%q", start.Action(), start.BlockReason())
	}
	model := Row{Location: config.LocationCloud, Model: config.Model{ID: "openrouter/a"}}
	if model.IsProfile() || model.Action() != ActionLaunch {
		t.Errorf("model row: IsProfile=%v action=%v, want an ordinary launch row", model.IsProfile(), model.Action())
	}
}
```

- [ ] **Step 2: Run it and see it fail**

Run from the repo root:

```bash
cd wt && go test ./internal/catalog -run TestProfileRowAnswersFromItsVerdict
```

Expected: a build failure. The output includes these lines and ends with `[build failed]`:

```
internal/catalog/profile_test.go:19:55: unknown field Profile in struct literal of type Row
internal/catalog/profile_test.go:19:65: undefined: ProfileRow
internal/catalog/profile_test.go:20:14: blocked.IsProfile undefined (type Row has no field or method IsProfile)
```

- [ ] **Step 3: Write the implementation**

Edit `wt/internal/catalog/catalog.go` in three places.

(a) In `type Row struct`, add the `Profile` field after `Unmapped`. The struct's last lines become:

```go
	// model name". RefusedByRoute refuses it there; direct, it launches.
	Unmapped bool
	// Profile is set on a slot-profile row and nil on every model row. A
	// profile row's Model holds the row id ("profile/<name>") and nothing
	// else, so code that reads another Model field, resolves a route or
	// starts the model must test Profile first.
	Profile *ProfileRow
}
```

(b) In `func (r Row) Action() Action`, add three lines at the top. The function becomes:

```go
func (r Row) Action() Action {
	if r.Profile != nil {
		return r.Profile.Verdict
	}
	if r.Location != config.LocationLocal || r.Ready() {
		return ActionLaunch
	}
	if !lifecycle.Startable(r.Model.ProviderID) {
		return ActionBlock
	}
	return ActionStart
}
```

(c) In `func (r Row) BlockReason() string`, add three lines at the top. The function becomes:

```go
func (r Row) BlockReason() string {
	if r.Profile != nil {
		return r.Profile.Reason
	}
	if r.Action() != ActionBlock {
		return ""
	}
	return notRunning(r.Model)
}
```

Leave the comments above both functions as they are.

Create `wt/internal/catalog/profile.go` with exactly this content:

```go
package catalog

import (
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)

// ProfileRow is what a slot-profile row carries in place of a registry model.
type ProfileRow struct {
	// Check is the profile, its static verdict and the agent's declared slots.
	Check profiles.SlotCheck
	// Models maps a declared slot to its resolved model. It is complete on a
	// launch or start row (an unfilled slot holds the default slot's model)
	// and partial or nil on a blocked row.
	Models map[string]config.Model
	// Local is the one distinct local slot model's own row. It is nil for an
	// all-cloud profile, when that model has no row, and on a row blocked
	// before the local model was judged.
	Local *Row
	// Verdict is ActionLaunch, ActionStart or ActionBlock.
	Verdict Action
	// Reason is the block reason; "" unless Verdict is ActionBlock.
	Reason string
	// Loc is "cloud", "local" or "mixed"; "" when no slot resolved.
	Loc string
}

// IsProfile reports whether r is a slot-profile row.
func (r Row) IsProfile() bool { return r.Profile != nil }
```

- [ ] **Step 4: Run the tests and see them pass**

```bash
cd wt && go test ./internal/catalog
```

Expected: `ok  	github.com/ohanaverse/local-ai-setup/wt/internal/catalog`. The whole package runs, so this also shows that no existing row test changed.

---

#### Behaviour 2: resolved validation

- [ ] **Step 5: Write the failing tests**

In `wt/internal/catalog/profile_test.go`, replace the import block with:

```go
import (
	"errors"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)
```

Then append this to the end of the file. It adds the fixtures, the block-reason table, the `[litellm]` test (Review Focus 2: `enabled = false` with a url and a key is still a launch row; an empty key blocks whatever `enabled` says), the local-model verdict table and the every-slot-filled test:

```go
// slotDeclared are the slots the test agent declares, in display order.
var slotDeclared = []string{"fable", "opus", "sonnet", "haiku"}

// slotTestCfg is catalogTestCfg plus the providers and models the resolved
// rules need, with [litellm] configured. The ids are made up.
func slotTestCfg() *config.Config {
	cfg := catalogTestCfg()
	cfg.Providers = append(cfg.Providers,
		config.Provider{ID: "mlx_lm_server", Location: config.LocationLocal},
		config.Provider{ID: "claude", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "native"}},
		config.Provider{ID: "nopolicy", Location: config.LocationCloud},
	)
	cfg.Models = []config.Model{
		{ID: "openrouter/a", ProviderID: "openrouter", ModelName: "a"},
		{ID: "openrouter/b", ProviderID: "openrouter", ModelName: "b"},
		{ID: "omlx/q", ProviderID: "omlx", ModelName: "q"},
		{ID: "ollama/g", ProviderID: "ollama", ModelName: "g"},
		{ID: "mlx_lm_server/p", ProviderID: "mlx_lm_server", ModelName: "p"},
		{ID: "claude/native", ProviderID: "claude", ModelName: "native", Native: true},
		{ID: "nopolicy/x", ProviderID: "nopolicy", ModelName: "x"},
		{ID: "ghost/x", ProviderID: "ghost", ModelName: "x"},
		{ID: "openrouter/badloc", ProviderID: "openrouter", ModelName: "badloc", Location: "clod"},
	}
	cfg.SetLitellmForTest(config.LitellmState{URL: "http://localhost:4000", APIKey: "sk-test"})
	return cfg
}

// slotCheck is a profile for agent "claude" that passed static validation.
func slotCheck(index int, name, def string, slots map[string]string) profiles.SlotCheck {
	return profiles.SlotCheck{
		Index:    index,
		Profile:  profiles.SlotProfile{Name: name, Agent: "claude", Default: def, Slots: slots},
		Usable:   true,
		Declared: slotDeclared,
	}
}

// TestResolveSlotProfileBlockReasons pins each resolved rule that needs no
// probe, one case per message: a static error carried through, an id that is
// not in the registry, a native model, a provider missing from the registry,
// a location that does not resolve, a provider LiteLLM cannot route, and two
// distinct local models. The reason is the only thing the picker and a -M pin
// show for a profile that cannot launch, so each must name the slot and the
// cause. The last two cases pin the order: a rule is checked over every slot
// before the next rule runs.
func TestResolveSlotProfileBlockReasons(t *testing.T) {
	static := slotCheck(0, "broken", "", map[string]string{"opus": "openrouter/a"})
	static.Err = errors.New("default is missing")
	cases := []struct {
		name  string
		check profiles.SlotCheck
		want  string
	}{
		{"static error", static, "default is missing"},
		{"id not in the registry", slotCheck(0, "p", "opus", map[string]string{"opus": "openrouter/a", "haiku": "ollama/gone"}),
			"slot haiku: ollama/gone is not in the registry"},
		{"native model", slotCheck(0, "p", "opus", map[string]string{"opus": "claude/native"}),
			"slot opus: claude/native is a native model, which has no LiteLLM route"},
		{"provider not in the registry", slotCheck(0, "p", "opus", map[string]string{"opus": "ghost/x"}),
			`slot opus: ghost/x names provider "ghost", which is not in the registry`},
		{"location does not resolve", slotCheck(0, "p", "opus", map[string]string{"opus": "openrouter/badloc"}),
			`slot opus: model "openrouter/badloc" has location "clod"; expected "local" or "cloud"`},
		{"provider without a route policy", slotCheck(0, "p", "opus", map[string]string{"opus": "nopolicy/x"}),
			`slot opus: nopolicy/x cannot be routed: provider "nopolicy" has no LiteLLM mapping`},
		{"two local models", slotCheck(0, "p", "opus", map[string]string{"opus": "omlx/q", "sonnet": "openrouter/a", "haiku": "ollama/g"}),
			"slots name two local models (omlx/q and ollama/g); a slot profile may name one"},
		{"registry rule runs over every slot before the native rule", slotCheck(0, "p", "opus", map[string]string{"opus": "claude/native", "haiku": "ollama/gone"}),
			"slot haiku: ollama/gone is not in the registry"},
		{"native rule runs over every slot before the policy rule", slotCheck(0, "p", "opus", map[string]string{"opus": "nopolicy/x", "haiku": "claude/native"}),
			"slot haiku: claude/native is a native model, which has no LiteLLM route"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := ResolveSlotProfile(slotTestCfg(), nil, tc.check)
			if row.Action() != ActionBlock || row.BlockReason() != tc.want {
				t.Errorf("action = %v, reason = %q\nwant ActionBlock, %q", row.Action(), row.BlockReason(), tc.want)
			}
			if row.Model.ID != "profile/"+tc.check.Profile.Name || row.Profile == nil {
				t.Errorf("row id = %q, profile set = %v", row.Model.ID, row.Profile != nil)
			}
		})
	}
	// A static failure resolves nothing: no models, no location.
	row := ResolveSlotProfile(slotTestCfg(), nil, static)
	if row.Profile.Models != nil || row.Profile.Loc != "" {
		t.Errorf("static failure: models = %v, loc = %q, want none", row.Profile.Models, row.Profile.Loc)
	}
}

// TestResolveSlotProfileNeedsLitellmConfiguredNotEnabled verifies the proxy
// rule: a slot profile needs the [litellm] url and api key, and never reads
// the on/off toggle. With both set an all-cloud profile is a launch row
// whether routing is on or off; with either one empty the row is blocked with
// the `wt litellm set` wording, again whatever the toggle says. Reading the
// toggle would hide every profile from a user who dials providers directly
// for single-model launches, and skipping the key check would launch Claude
// Code with an empty token.
func TestResolveSlotProfileNeedsLitellmConfiguredNotEnabled(t *testing.T) {
	check := slotCheck(0, "cloudy", "sonnet", map[string]string{"sonnet": "openrouter/a", "opus": "openrouter/b"})
	const noURL = "a slot profile always uses the LiteLLM proxy, but no URL is configured — run 'wt litellm set --url ... --api-key ...'"
	const noKey = "a slot profile always uses the LiteLLM proxy, but no API key is configured — run 'wt litellm set --url ... --api-key ...'"
	cases := []struct {
		name       string
		url, key   string
		wantAction Action
		wantReason string
	}{
		{"url and key", "http://localhost:4000", "sk-test", ActionLaunch, ""},
		{"no key", "http://localhost:4000", "", ActionBlock, noKey},
		{"no url", "", "sk-test", ActionBlock, noURL},
		{"neither", "", "", ActionBlock, noURL},
	}
	for _, tc := range cases {
		for _, enabled := range []bool{false, true} {
			cfg := slotTestCfg()
			cfg.SetLitellmForTest(config.LitellmState{Enabled: enabled, URL: tc.url, APIKey: tc.key})
			row := ResolveSlotProfile(cfg, nil, check)
			if row.Action() != tc.wantAction || row.BlockReason() != tc.wantReason {
				t.Errorf("%s, enabled=%v: action = %v, reason = %q\nwant %v, %q", tc.name, enabled, row.Action(), row.BlockReason(), tc.wantAction, tc.wantReason)
			}
		}
	}
}

// TestResolveSlotProfileLocalModelVerdict pins the local-model verdict table:
// the profile's one local model is judged by the row the catalog builds for
// it from the same snapshot. No row blocks the profile (with MissingReason's
// wording when the probe can say why), a row wt cannot start blocks it with
// the slot named, an idle, loading or unknown-presence model makes it a start
// row, and a running one a launch row. A wrong answer here either launches
// Claude Code against a model that is not serving or refuses a profile wt
// could have started.
func TestResolveSlotProfileLocalModelVerdict(t *testing.T) {
	entries := func(e ...localmodels.Entry) *localmodels.Snapshot {
		return &localmodels.Snapshot{Entries: e}
	}
	cases := []struct {
		name        string
		local       string
		snap        *localmodels.Snapshot
		wantAction  Action
		wantReason  string
		wantLocal   bool
		wantRunning bool
		wantLoading bool
	}{
		{name: "confirmed not on disk", local: "omlx/q",
			snap:       entries(localmodels.Entry{ProviderID: "omlx", ModelID: "omlx/q", Registered: true, ArtifactKnown: true}),
			wantAction: ActionBlock, wantReason: "omlx/q is not on disk — pull or download it first"},
		{name: "stopped mlx_lm_server pairing", local: "mlx_lm_server/p",
			snap:       entries(localmodels.Entry{ProviderID: "mlx_lm_server", ModelID: "mlx_lm_server/p", Registered: true}),
			wantAction: ActionBlock, wantReason: "local model \"mlx_lm_server/p\" is not running, and wt cannot start an mlx_lm_server pairing — start it with `llmbench provider isolate --solo mlx_lm_server <target> --draft <draft>`"},
		{name: "no inventory entry", local: "omlx/q",
			snap:       entries(),
			wantAction: ActionBlock, wantReason: "slot haiku: omlx/q is not on disk or running"},
		{name: "a row wt cannot start", local: "mlx_lm_server/p",
			snap:       nil,
			wantAction: ActionBlock, wantReason: "slot haiku: local model \"mlx_lm_server/p\" is not running, and wt cannot start an mlx_lm_server pairing — start it with `llmbench provider isolate --solo mlx_lm_server <target> --draft <draft>`",
			wantLocal: true},
		{name: "on disk, not running", local: "omlx/q",
			snap:       entries(localmodels.Entry{ProviderID: "omlx", ModelID: "omlx/q", Artifact: "q", Registered: true, ArtifactKnown: true}),
			wantAction: ActionStart, wantLocal: true},
		{name: "still loading", local: "omlx/q",
			snap:       entries(localmodels.Entry{ProviderID: "omlx", ModelID: "omlx/q", Artifact: "q", Registered: true, ArtifactKnown: true, Running: true, Loading: true}),
			wantAction: ActionStart, wantLocal: true, wantRunning: true, wantLoading: true},
		{name: "presence unknown", local: "ollama/g",
			snap:       entries(localmodels.Entry{ProviderID: "ollama", ModelID: "ollama/g", Registered: true}),
			wantAction: ActionStart, wantLocal: true},
		{name: "no probe ran", local: "omlx/q",
			snap:       nil,
			wantAction: ActionStart, wantLocal: true},
		{name: "running", local: "omlx/q",
			snap:       entries(localmodels.Entry{ProviderID: "omlx", ModelID: "omlx/q", Artifact: "q", Registered: true, ArtifactKnown: true, Running: true}),
			wantAction: ActionLaunch, wantLocal: true, wantRunning: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			check := slotCheck(0, "mixed", "sonnet", map[string]string{"sonnet": "openrouter/a", "haiku": tc.local})
			row := ResolveSlotProfile(slotTestCfg(), tc.snap, check)
			if row.Action() != tc.wantAction || row.BlockReason() != tc.wantReason {
				t.Errorf("action = %v, reason = %q\nwant %v, %q", row.Action(), row.BlockReason(), tc.wantAction, tc.wantReason)
			}
			if got := row.Profile.Local != nil; got != tc.wantLocal {
				t.Errorf("Local set = %v, want %v", got, tc.wantLocal)
			}
			if tc.wantLocal && row.Profile.Local.Model.ID != tc.local {
				t.Errorf("Local = %q, want %q", row.Profile.Local.Model.ID, tc.local)
			}
			if row.Running != tc.wantRunning || row.Loading != tc.wantLoading {
				t.Errorf("Running = %v, Loading = %v, want %v, %v", row.Running, row.Loading, tc.wantRunning, tc.wantLoading)
			}
			if row.Profile.Loc != "mixed" {
				t.Errorf("Loc = %q, want mixed", row.Profile.Loc)
			}
		})
	}

	// An all-cloud profile has no local model to judge: it launches.
	cloud := ResolveSlotProfile(slotTestCfg(), entries(), slotCheck(0, "cloudy", "sonnet", map[string]string{"sonnet": "openrouter/a"}))
	if cloud.Action() != ActionLaunch || cloud.Profile.Local != nil || cloud.Running || cloud.Profile.Loc != "cloud" {
		t.Errorf("all-cloud profile: action = %v, Local set = %v, Running = %v, Loc = %q", cloud.Action(), cloud.Profile.Local != nil, cloud.Running, cloud.Profile.Loc)
	}
	// One local model may fill every slot the profile fills.
	local := ResolveSlotProfile(slotTestCfg(), nil, slotCheck(0, "localonly", "haiku", map[string]string{"haiku": "omlx/q", "opus": "omlx/q"}))
	if local.Action() != ActionStart || local.Profile.Loc != "local" {
		t.Errorf("all-local profile: action = %v, Loc = %q, want ActionStart, local", local.Action(), local.Profile.Loc)
	}
}

// TestResolveSlotProfileFillsEverySlot verifies the row a launchable profile
// produces: every slot the agent declares has a model, an unfilled slot
// holding the default slot's model, so no slot is left to send an Anthropic
// id the proxy rejects. It also pins the helpers the launch flow reads the
// row through: Distinct (each model once, the default's first), Cloud (the
// models whose routes are written without a start), SlotOf and Launch.
func TestResolveSlotProfileFillsEverySlot(t *testing.T) {
	check := slotCheck(0, "mixed", "sonnet", map[string]string{"opus": "openrouter/b", "sonnet": "openrouter/a", "haiku": "omlx/q"})
	check.Profile.Env = map[string]string{"MAX_THINKING_TOKENS": "4096"}
	check.Profile.ConfigContent = map[string]any{"modelOverrides": map[string]any{"x": "y"}}
	row := ResolveSlotProfile(slotTestCfg(), nil, check)
	p := row.Profile
	if row.Action() != ActionStart {
		t.Fatalf("action = %v (%s), want ActionStart", row.Action(), row.BlockReason())
	}
	want := map[string]string{"fable": "openrouter/a", "opus": "openrouter/b", "sonnet": "openrouter/a", "haiku": "omlx/q"}
	if len(p.Models) != len(want) {
		t.Fatalf("Models has %d slots, want %d: %v", len(p.Models), len(want), p.Models)
	}
	for slot, id := range want {
		if p.Models[slot].ID != id {
			t.Errorf("slot %s = %q, want %q", slot, p.Models[slot].ID, id)
		}
	}
	if p.Models["sonnet"].ModelName != "a" || p.Models["sonnet"].ProviderID != "openrouter" {
		t.Errorf("slot sonnet = %+v, want the whole registry model", p.Models["sonnet"])
	}
	if got := strings.Join(rowModelIDs(p.Distinct()), ","); got != "openrouter/a,openrouter/b,omlx/q" {
		t.Errorf("Distinct = %s", got)
	}
	if got := strings.Join(rowModelIDs(p.Cloud()), ","); got != "openrouter/a,openrouter/b" {
		t.Errorf("Cloud = %s", got)
	}
	if p.SlotOf("omlx/q") != "haiku" || p.SlotOf("openrouter/a") != "sonnet" || p.SlotOf("nope/x") != "" {
		t.Errorf("SlotOf: omlx/q=%q openrouter/a=%q nope/x=%q", p.SlotOf("omlx/q"), p.SlotOf("openrouter/a"), p.SlotOf("nope/x"))
	}
	// The row itself holds the id and nothing a model row would: no location,
	// no provider, so no caller can route or start "profile/mixed".
	if row.Model.ID != "profile/mixed" || row.Model.ProviderID != "" || row.Model.ModelName != "" || row.Location != "" || row.Status != StatusOK || row.Discovered || row.Unmapped {
		t.Errorf("row = %+v", row)
	}

	sl := p.Launch()
	if sl.Agent != "claude" || sl.Default != "sonnet" || len(sl.Models) != 4 || sl.Models["fable"].ID != "openrouter/a" {
		t.Errorf("Launch = %+v", sl)
	}
	if sl.Env["MAX_THINKING_TOKENS"] != "4096" || sl.ConfigContent["modelOverrides"] == nil {
		t.Errorf("Launch env = %v, config_content = %v", sl.Env, sl.ConfigContent)
	}
	sl.Models["fable"] = config.Model{ID: "changed"}
	if p.Models["fable"].ID != "openrouter/a" {
		t.Error("Launch().Models is the row's own map: a build could change the row")
	}

	// One local model in two slots is one distinct model, named by its first slot.
	twice := ResolveSlotProfile(slotTestCfg(), nil, slotCheck(0, "twice", "opus", map[string]string{"opus": "omlx/q", "haiku": "omlx/q", "sonnet": "openrouter/a"})).Profile
	if got := strings.Join(rowModelIDs(twice.Distinct()), ","); got != "omlx/q,openrouter/a" {
		t.Errorf("Distinct with a model in two slots = %s", got)
	}
	if got := strings.Join(rowModelIDs(twice.Cloud()), ","); got != "openrouter/a" {
		t.Errorf("Cloud with a local default = %s", got)
	}
	if twice.SlotOf("omlx/q") != "opus" {
		t.Errorf("SlotOf(omlx/q) = %q, want the default slot opus", twice.SlotOf("omlx/q"))
	}
}

// rowModelIDs lists the ids of models, in order.
func rowModelIDs(models []config.Model) []string {
	ids := make([]string, len(models))
	for i, m := range models {
		ids[i] = m.ID
	}
	return ids
}
```

- [ ] **Step 6: Run them and see them fail**

```bash
cd wt && go test ./internal/catalog -run 'TestResolveSlotProfile'
```

Expected: a build failure. Every error line reads `undefined: ResolveSlotProfile`, and the output ends with `[build failed]`.

- [ ] **Step 7: Write the implementation**

Replace the whole of `wt/internal/catalog/profile.go` with:

```go
package catalog

import (
	"fmt"
	"maps"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)

// ProfileRow is what a slot-profile row carries in place of a registry model.
type ProfileRow struct {
	// Check is the profile, its static verdict and the agent's declared slots.
	Check profiles.SlotCheck
	// Models maps a declared slot to its resolved model. It is complete on a
	// launch or start row (an unfilled slot holds the default slot's model)
	// and partial or nil on a blocked row.
	Models map[string]config.Model
	// Local is the one distinct local slot model's own row. It is nil for an
	// all-cloud profile, when that model has no row, and on a row blocked
	// before the local model was judged.
	Local *Row
	// Verdict is ActionLaunch, ActionStart or ActionBlock.
	Verdict Action
	// Reason is the block reason; "" unless Verdict is ActionBlock.
	Reason string
	// Loc is "cloud", "local" or "mixed"; "" when no slot resolved.
	Loc string
}

// IsProfile reports whether r is a slot-profile row.
func (r Row) IsProfile() bool { return r.Profile != nil }

// slotRule is one per-slot rule of resolved validation: "" when the slot's
// model passes, else the block reason.
type slotRule func(cfg *config.Config, slot string, m config.Model) string

// slotRules are the per-slot rules that follow the registry lookup, in the
// order they are checked. Each one is checked over every filled slot before
// the next one runs.
var slotRules = []slotRule{
	func(_ *config.Config, slot string, m config.Model) string {
		if m.Native {
			return fmt.Sprintf("slot %s: %s is a native model, which has no LiteLLM route", slot, m.ID)
		}
		return ""
	},
	func(cfg *config.Config, slot string, m config.Model) string {
		if cfg.ProviderByID(m.ProviderID) == nil {
			return fmt.Sprintf("slot %s: %s names provider %q, which is not in the registry", slot, m.ID, m.ProviderID)
		}
		return ""
	},
	func(cfg *config.Config, slot string, m config.Model) string {
		if _, err := cfg.ResolveLocation(m); err != nil {
			return fmt.Sprintf("slot %s: %v", slot, err)
		}
		return ""
	},
	func(_ *config.Config, slot string, m config.Model) string {
		if _, ok := litellm.PolicyFor(m.ProviderID); !ok {
			return fmt.Sprintf("slot %s: %s cannot be routed: provider %q has no LiteLLM mapping", slot, m.ID, m.ProviderID)
		}
		return ""
	},
}

// ResolveSlotProfile runs resolved validation for one statically checked,
// Usable profile and returns its row: the file's rules were checked by
// profiles.ValidateSlots, and this adds the ones that need the registry, the
// live inventory and the [litellm] table. The first failing rule blocks the
// row and is its reason. snap may be nil (no probe ran), in which case the
// local model is judged as Build judges an unprobed local model.
//
// The row's id is the profile's ("profile/<name>") and its Model holds
// nothing else. Whether [litellm] is switched on is never read: a slot
// profile always uses the proxy.
func ResolveSlotProfile(cfg *config.Config, snap *localmodels.Snapshot, c profiles.SlotCheck) Row {
	if cfg == nil {
		cfg = &config.Config{}
	}
	p := &ProfileRow{Check: c, Verdict: ActionLaunch}
	row := Row{Model: config.Model{ID: c.Profile.ID()}, Status: StatusOK, Profile: p}
	block := func(reason string) Row {
		p.Verdict, p.Reason = ActionBlock, reason
		return row
	}
	if c.Err != nil {
		return block(c.Err.Error())
	}

	// Every filled slot whose id is in the registry, then the default's model
	// for each slot the profile leaves unfilled, and the row's location from
	// the filled slots that have one. A blocked row keeps what resolved.
	slots := c.Profile.FilledOrder(c.Declared)
	p.Models = map[string]config.Model{}
	cloud, local := false, false
	for _, s := range slots {
		i := config.IndexModelByID(cfg.Models, c.Profile.Slots[s])
		if i < 0 {
			continue
		}
		m := cfg.Models[i]
		p.Models[s] = m
		switch loc, err := cfg.ResolveLocation(m); {
		case err != nil:
		case loc == config.LocationLocal:
			local = true
		default:
			cloud = true
		}
	}
	if def, ok := p.Models[c.Profile.Default]; ok {
		for _, s := range c.Declared {
			if _, filled := c.Profile.Slots[s]; !filled {
				p.Models[s] = def
			}
		}
	}
	switch {
	case cloud && local:
		p.Loc = "mixed"
	case local:
		p.Loc = string(config.LocationLocal)
	case cloud:
		p.Loc = string(config.LocationCloud)
	}

	for _, s := range slots {
		if _, ok := p.Models[s]; !ok {
			return block(fmt.Sprintf("slot %s: %s is not in the registry", s, c.Profile.Slots[s]))
		}
	}
	for _, rule := range slotRules {
		for _, s := range slots {
			if reason := rule(cfg, s, p.Models[s]); reason != "" {
				return block(reason)
			}
		}
	}

	var locals []config.Model
	for _, m := range p.Distinct() {
		if loc, _ := cfg.ResolveLocation(m); loc == config.LocationLocal {
			locals = append(locals, m)
		}
	}
	if len(locals) > 1 {
		return block(fmt.Sprintf("slots name two local models (%s and %s); a slot profile may name one", locals[0].ID, locals[1].ID))
	}

	switch {
	case cfg.LitellmBaseURL() == "":
		return block("a slot profile always uses the LiteLLM proxy, but no URL is configured — run 'wt litellm set --url ... --api-key ...'")
	case cfg.LitellmAPIKey() == "":
		return block("a slot profile always uses the LiteLLM proxy, but no API key is configured — run 'wt litellm set --url ... --api-key ...'")
	}

	if len(locals) == 0 {
		return row
	}
	// The local model is judged by the catalog's own row policy: one row built
	// for it alone from the same snapshot, whatever the agent's eligible list
	// and -T/-F say.
	m := locals[0]
	slot := p.SlotOf(m.ID)
	own, ok := Find(Build(Input{Config: cfg, Models: []config.Model{m}, Inventory: snap, HideDiscovered: true}), m.ID)
	if !ok {
		if reason := MissingReason(cfg, snap, m.ID); reason != "" {
			return block(reason)
		}
		return block(fmt.Sprintf("slot %s: %s is not on disk or running", slot, m.ID))
	}
	p.Local = &own
	row.Running, row.Loading = own.Running, own.Loading
	switch own.Action() {
	case ActionBlock:
		return block("slot " + slot + ": " + own.BlockReason())
	case ActionStart:
		p.Verdict = ActionStart
	}
	return row
}

// Distinct lists the resolved slot models once each, in the order of
// Check.Profile.FilledOrder(Check.Declared): the default slot's model first.
func (p *ProfileRow) Distinct() []config.Model {
	var out []config.Model
	seen := map[string]bool{}
	for _, s := range p.Check.Profile.FilledOrder(p.Check.Declared) {
		m, ok := p.Models[s]
		if !ok || seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		out = append(out, m)
	}
	return out
}

// Cloud is Distinct without the local model (Local.Model).
func (p *ProfileRow) Cloud() []config.Model {
	var out []config.Model
	for _, m := range p.Distinct() {
		if p.Local != nil && m.ID == p.Local.Model.ID {
			continue
		}
		out = append(out, m)
	}
	return out
}

// SlotOf is the first slot, in FilledOrder, whose model has the given id; ""
// when none.
func (p *ProfileRow) SlotOf(modelID string) string {
	for _, s := range p.Check.Profile.FilledOrder(p.Check.Declared) {
		if m, ok := p.Models[s]; ok && m.ID == modelID {
			return s
		}
	}
	return ""
}

// Launch is the profile as an agent's slot build takes it. Models is a copy,
// so the build cannot change the row.
func (p *ProfileRow) Launch() profiles.SlotLaunch {
	return profiles.SlotLaunch{
		Agent:         p.Check.Profile.Agent,
		Models:        maps.Clone(p.Models),
		Default:       p.Check.Profile.Default,
		Env:           p.Check.Profile.Env,
		ConfigContent: p.Check.Profile.ConfigContent,
	}
}
```

How the local model is judged: `Build` is called for that one model, with the same snapshot and with `HideDiscovered: true`, so the verdict is the catalog's own row policy and does not depend on the agent's eligible list or on `-T`/`-F`. That inner call carries no `SlotProfiles`, so it cannot recurse.

- [ ] **Step 8: Run the tests and see them pass**

```bash
cd wt && go test ./internal/catalog -run 'TestResolveSlotProfile|TestProfileRow' -v
```

Expected: `--- PASS` for `TestProfileRowAnswersFromItsVerdict`, `TestResolveSlotProfileBlockReasons`, `TestResolveSlotProfileNeedsLitellmConfiguredNotEnabled`, `TestResolveSlotProfileLocalModelVerdict` and `TestResolveSlotProfileFillsEverySlot`, then `ok`.

---

#### Behaviour 3: opt-in rows from `Build`, the reserved prefix, duplicate names, `Find`

- [ ] **Step 9: Write the failing tests**

Append this to the end of `wt/internal/catalog/profile_test.go`:

```go
// TestBuildEmitsProfileRowsOnlyOnRequest verifies who sees profile rows. Build
// emits them only when its input carries the slot profiles, only for the
// input's agent, and never under -T/-F (HideDiscovered). `wt start` and
// `wt smoke` build their catalogs without that input, so a profile can never
// be started, smoke-tested or rotated to as if it were a model.
func TestBuildEmitsProfileRowsOnlyOnRequest(t *testing.T) {
	cfg := slotTestCfg()
	models := []config.Model{cfg.Models[0], cfg.Models[2]} // openrouter/a, omlx/q
	inv := &localmodels.Snapshot{Entries: []localmodels.Entry{
		{ProviderID: "omlx", ModelID: "omlx/q", Artifact: "q", Registered: true, ArtifactKnown: true, Running: true},
		{ProviderID: "omlx", ModelID: "omlx/disc", Artifact: "disc", ArtifactKnown: true},
	}}
	other := slotCheck(1, "for-codex", "opus", map[string]string{"opus": "openrouter/a"})
	other.Profile.Agent = "codex"
	unusable := profiles.SlotCheck{Index: 2, Profile: profiles.SlotProfile{Name: "Bad Name", Agent: "claude"}, Err: errors.New(`name "Bad Name" is malformed: use lowercase letters, digits and hyphens`)}
	checks := []profiles.SlotCheck{
		slotCheck(0, "mixed", "sonnet", map[string]string{"sonnet": "openrouter/a", "haiku": "omlx/q"}),
		other,
		unusable,
		slotCheck(3, "dangling", "opus", map[string]string{"opus": "ollama/gone"}),
	}
	const modelRows = "openrouter/a,omlx/q,omlx/disc"
	cases := []struct {
		name string
		in   Input
		want string
	}{
		{"no slot profiles in the input", Input{Config: cfg, Agent: "claude", Models: models, Inventory: inv}, modelRows},
		{"opted in", Input{Config: cfg, Agent: "claude", Models: models, Inventory: inv, SlotProfiles: checks}, modelRows + ",profile/mixed,profile/dangling"},
		{"another agent's profile", Input{Config: cfg, Agent: "codex", Models: models, Inventory: inv, SlotProfiles: checks}, "openrouter/a,omlx/q,profile/for-codex"},
		{"no agent", Input{Config: cfg, Models: models, Inventory: inv, SlotProfiles: checks}, modelRows},
		{"under -T or -F", Input{Config: cfg, Agent: "claude", Models: models, Inventory: inv, HideDiscovered: true, SlotProfiles: checks}, "openrouter/a,omlx/q"},
		{"no probe ran", Input{Config: cfg, Agent: "claude", Models: models, SlotProfiles: checks}, "openrouter/a,omlx/q,profile/mixed,profile/dangling"},
	}
	for _, tc := range cases {
		if got := strings.Join(rowIDs(Build(tc.in)), ","); got != tc.want {
			t.Errorf("%s: rows = %s\nwant %s", tc.name, got, tc.want)
		}
	}

	// The rows carry resolved validation's verdict against Build's own snapshot:
	// the running local model makes "mixed" a launch row, and a slot left
	// dangling blocks its own row without touching the other.
	rows := Build(Input{Config: cfg, Agent: "claude", Models: models, Inventory: inv, SlotProfiles: checks})
	mixed, _ := Find(rows, "profile/mixed")
	if mixed.Action() != ActionLaunch || !mixed.Running || mixed.Profile.Check.Index != 0 {
		t.Errorf("profile/mixed: action = %v (%s), Running = %v, index = %d", mixed.Action(), mixed.BlockReason(), mixed.Running, mixed.Profile.Check.Index)
	}
	dangling, _ := Find(rows, "profile/dangling")
	if dangling.Action() != ActionBlock || dangling.BlockReason() != "slot opus: ollama/gone is not in the registry" {
		t.Errorf("profile/dangling: action = %v, reason = %q", dangling.Action(), dangling.BlockReason())
	}
}

// TestBuildCollapsesDuplicateProfileNames verifies that profiles sharing a
// name for one agent become a single blocked row, `duplicate profile name`,
// in the first one's place. Row ids stay unique, so the picker, a -M pin and
// the usage counts can never choose between two profiles of one name. The
// same name under another agent is a different profile and is untouched.
func TestBuildCollapsesDuplicateProfileNames(t *testing.T) {
	dup := func(index int, slots map[string]string) profiles.SlotCheck {
		c := slotCheck(index, "twin", "opus", slots)
		c.Duplicate, c.Err = true, profiles.ErrSlotDuplicate
		return c
	}
	codex := slotCheck(2, "twin", "opus", map[string]string{"opus": "openrouter/a"})
	codex.Profile.Agent = "codex"
	checks := []profiles.SlotCheck{
		dup(0, map[string]string{"opus": "openrouter/a"}),
		slotCheck(1, "solo", "opus", map[string]string{"opus": "openrouter/a"}),
		codex,
		dup(3, map[string]string{"opus": "openrouter/b"}),
	}
	rows := Build(Input{Config: slotTestCfg(), Agent: "claude", SlotProfiles: checks})
	if got := strings.Join(rowIDs(rows), ","); got != "profile/twin,profile/solo" {
		t.Fatalf("rows = %s, want profile/twin,profile/solo", got)
	}
	if rows[0].Action() != ActionBlock || rows[0].BlockReason() != "duplicate profile name" {
		t.Errorf("profile/twin: action = %v, reason = %q", rows[0].Action(), rows[0].BlockReason())
	}
	if rows[1].Action() != ActionLaunch {
		t.Errorf("profile/solo: action = %v (%s), want ActionLaunch", rows[1].Action(), rows[1].BlockReason())
	}
	other := Build(Input{Config: slotTestCfg(), Agent: "codex", SlotProfiles: checks})
	if len(other) != 1 || other[0].Model.ID != "profile/twin" || other[0].Action() != ActionLaunch {
		t.Errorf("codex rows = %v, want its own launchable profile/twin", rowIDs(other))
	}
}

// TestBuildYieldsToAProfileProvider verifies the reserved prefix: when the
// registry has a provider with id "profile", its models already own ids of
// the form profile/<name>, so Build emits no profile rows at all and the
// registry rows keep their ids. Emitting both would give two rows one id,
// and a -M pin would launch whichever came first.
func TestBuildYieldsToAProfileProvider(t *testing.T) {
	cfg := slotTestCfg()
	checks := []profiles.SlotCheck{slotCheck(0, "mixed", "sonnet", map[string]string{"sonnet": "openrouter/a"})}
	if SlotPrefixReserved(cfg) || SlotPrefixReserved(nil) {
		t.Fatal("SlotPrefixReserved is true for a registry with no profile provider, or for no config")
	}
	cfg.Providers = append(cfg.Providers, config.Provider{ID: "profile", Location: config.LocationCloud})
	registryModel := config.Model{ID: "profile/mixed", ProviderID: "profile", ModelName: "mixed"}
	cfg.Models = append(cfg.Models, registryModel)
	if !SlotPrefixReserved(cfg) {
		t.Fatal("SlotPrefixReserved is false for a registry with a profile provider")
	}
	rows := Build(Input{Config: cfg, Agent: "claude", Models: []config.Model{registryModel}, SlotProfiles: checks})
	if len(rows) != 1 || rows[0].IsProfile() || rows[0].Model.ProviderID != "profile" {
		t.Errorf("rows = %+v, want the one registry row and no profile row", rows)
	}
	if r, ok := Find(rows, "profile/mixed"); !ok || r.IsProfile() {
		t.Errorf("Find(profile/mixed): found = %v, IsProfile = %v, want the registry model's row", ok, r.IsProfile())
	}
}

// TestFindResolvesAProfilePin verifies that a -M profile/<name> pin finds the
// profile's row among an agent's rows, with the verdict the picker shows, and
// that a profile written for another agent is not found, so the caller gives
// its "not in the eligible list" message.
func TestFindResolvesAProfilePin(t *testing.T) {
	cfg := slotTestCfg()
	other := slotCheck(1, "for-codex", "opus", map[string]string{"opus": "openrouter/a"})
	other.Profile.Agent = "codex"
	rows := Build(Input{
		Config: cfg, Agent: "claude", Models: []config.Model{cfg.Models[0]},
		SlotProfiles: []profiles.SlotCheck{
			slotCheck(0, "mixed", "sonnet", map[string]string{"sonnet": "openrouter/a", "haiku": "omlx/q"}),
			other,
		},
	})
	r, ok := Find(rows, "profile/mixed")
	if !ok || !r.IsProfile() || r.Action() != ActionStart {
		t.Fatalf("Find(profile/mixed): found = %v, IsProfile = %v, action = %v", ok, r.IsProfile(), r.Action())
	}
	if got := strings.Join(rowModelIDs(r.Profile.Distinct()), ","); got != "openrouter/a,omlx/q" {
		t.Errorf("pinned profile's models = %s", got)
	}
	if _, ok := Find(rows, "profile/for-codex"); ok {
		t.Error("Find(profile/for-codex) found a row among claude's rows")
	}
	if m, ok := Find(rows, "openrouter/a"); !ok || m.IsProfile() {
		t.Errorf("Find(openrouter/a): found = %v, IsProfile = %v, want the model row", ok, m.IsProfile())
	}
}
```

- [ ] **Step 10: Run them and see them fail**

```bash
cd wt && go test ./internal/catalog -run 'TestBuild|TestFind'
```

Expected: a build failure whose lines read `unknown field SlotProfiles in struct literal of type Input` and `undefined: SlotPrefixReserved`, ending with `[build failed]`.

- [ ] **Step 11: Write the implementation**

Edit `wt/internal/catalog/catalog.go` in three places.

(a) Add the `profiles` import. The import block becomes:

```go
import (
	"fmt"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)
```

(b) Add the opt-in field to `type Input struct`. The struct becomes (the comment above it stays):

```go
type Input struct {
	Config         *config.Config
	Agent          string
	Models         []config.Model
	Inventory      *localmodels.Snapshot
	HideDiscovered bool
	// SlotProfiles opts in to slot-profile rows: the statically validated
	// profiles (profiles.ValidateSlots). nil means no profile rows. Only the
	// agent picker and the -M pin set it; `wt start` and `wt smoke` never do.
	SlotProfiles []profiles.SlotCheck
}
```

(c) Change the last statement of `func Build(in Input) []Row` from `return rows` to:

```go
	return append(rows, slotRows(in)...)
```

`slotRows` returns nil for every caller that did not opt in, so those callers get exactly the rows they got before.

Append this to the end of `wt/internal/catalog/profile.go`:

```go
// SlotPrefixReserved reports whether the registry has a provider with id
// profiles.SlotReservedProvider. Such a provider's model ids start with
// "profile/", the prefix of every profile row id, so Build emits no profile
// rows while it exists. cfg may be nil (false).
func SlotPrefixReserved(cfg *config.Config) bool {
	return cfg != nil && cfg.ProviderByID(profiles.SlotReservedProvider) != nil
}

// slotRows is Build's profile half: one row per Usable profile of in.Agent,
// in file order. It returns nil unless the caller opted in (SlotProfiles),
// named an agent and a config, applied no -T/-F filter (HideDiscovered), and
// the registry leaves the "profile" prefix free. A group of profiles sharing
// a name gets one row, blocked by the duplicate error every member carries,
// so row ids stay unique.
func slotRows(in Input) []Row {
	if len(in.SlotProfiles) == 0 || in.Agent == "" || in.Config == nil || in.HideDiscovered || SlotPrefixReserved(in.Config) {
		return nil
	}
	var rows []Row
	dup := map[string]bool{}
	for _, c := range in.SlotProfiles {
		if !c.Usable || c.Profile.Agent != in.Agent {
			continue
		}
		if c.Duplicate {
			if dup[c.Profile.Name] {
				continue
			}
			dup[c.Profile.Name] = true
		}
		rows = append(rows, ResolveSlotProfile(in.Config, in.Inventory, c))
	}
	return rows
}
```

Do not edit `Find`. It matches on `Row.Model.ID`, which is where a profile row keeps `profile/<name>`.

- [ ] **Step 12: Run the tests and see them pass**

```bash
cd wt && go test ./internal/catalog -v -run 'TestBuildEmitsProfileRowsOnlyOnRequest|TestBuildCollapsesDuplicateProfileNames|TestBuildYieldsToAProfileProvider|TestFindResolvesAProfilePin'
```

Expected: four `--- PASS` lines, then `ok`.

- [ ] **Step 13: Run the whole module's checks**

```bash
cd wt && gofmt -l . && go build ./... && go vet ./... && go test ./...
```

Expected: `gofmt -l .` prints nothing, `go build` and `go vet` print nothing, and every package line of `go test` starts with `ok` (or `?` for a package with no tests). No package outside `internal/catalog` changes behaviour: `wt start` (`localRowsSnap`), `wt smoke` (`smoke.walk`), the picker (`buildRows`) and `resolveModel` all call `Build` without `SlotProfiles`.

- [ ] **Step 14: Commit**

Run from the repo root:

```bash
git add wt/internal/catalog/catalog.go wt/internal/catalog/profile.go wt/internal/catalog/profile_test.go
git commit -m "feat(wt): slot-profile rows in the catalog, with resolved validation"
```

### Task 5: Catalog consumers and the picker row

**Files:**
- Create: `wt/internal/tui/slot_row_test.go`
- Modify: `wt/cmd/wt/resolve.go` (`pickerBlockedReason`, `launchableModels`)
- Modify: `wt/internal/tui/modelrows.go` (import block; new method `tableRow.costModel`; `pricedAt`, `price`, `timePriced`; `type tableInput`; `buildRows`; `sortRows`)
- Modify: `wt/internal/tui/modeltable.go` (`renderTable`)
- Modify: `wt/internal/tui/model_list.go` (import block; `type modelItem`)
- Modify: `wt/internal/tui/layout.go` (import block; new function `slotLine`; `modelFrames`)
- Modify: `wt/internal/tui/app.go` (import block; `type model`; the single-row shortcut at the end of `enterModelPhase`; `tableFor`; `refreshTable`)
- Test: `wt/internal/tui/slot_row_test.go`, `wt/internal/tui/layout_test.go` (`TestEveryListPhaseFitsTheTerminal`), `wt/cmd/wt/resolve_test.go`, `wt/internal/smoke/smoke_test.go`

**Interfaces:**
- Consumes, Task 2 (`wt/internal/profiles`):
  - `type SlotCheck struct { Index int; Profile SlotProfile; Usable bool; Duplicate bool; Declared []string; Err error }`
  - `type SlotProfile struct { Name, Agent, Default string; Slots map[string]string; … }`
  - `func (p SlotProfile) ID() string` — `"profile/" + Name`
  - `func (p SlotProfile) FilledOrder(declared []string) []string` — the filled slots, the default first, the rest in `declared`'s order
  - `const SlotIDPrefix = "profile/"`
  - `func SlotPath() string` — `<config dir>/slot-profiles.toml`
- Consumes, Task 4 (`wt/internal/catalog`):
  - `Input.SlotProfiles []profiles.SlotCheck` — opts in to profile rows; nil means none
  - `Row.Profile *ProfileRow` — non-nil on a profile row; `func (r Row) IsProfile() bool`
  - `type ProfileRow struct { Check profiles.SlotCheck; Models map[string]config.Model; Local *Row; Verdict Action; Reason string; Loc string }`
  - `func (r Row) Action() Action` and `func (r Row) BlockReason() string` answer from `Profile.Verdict` and `Profile.Reason` on a profile row
  - A profile row is `Row{Model: config.Model{ID: "profile/<name>"}, Status: StatusOK, Profile: …}` with `Location` empty and `Running`/`Loading` copied from the profile's local model
- Consumes, existing code:
  - `func (c *Config) ResolveRoute(m Model, agentProtocols []Protocol) (Route, error)` (`internal/config`) — fails with `unknown provider "profile" for model "profile/<name>"` on a profile row's id-only model
  - `func costCell(r tableRow) string`, `func runningText(r catalog.Row) string`, `func buildTable(in tableInput, refs refcount.Store, lastID string) modelTable` (`internal/tui/modeltable.go`)
  - `func modeLine(mode, cost string, width int) string`, `func cutLines(s string, width int) string`, `const pickerPadX = 2` (`internal/tui/layout.go`)
  - `func Candidates(cfg *config.Config) []Candidate`, `func SetSmokeProbeForTest(snap localmodels.Snapshot) (restore func())` (`internal/smoke`)
  - tui test helpers: `modelTestConfig()`, `runningOmlxSnapshot()`, `flowEnter(t, m, agent)`, `itemIDs(m)`, `indexOfID(m, id)`, `stubInventory(t, snap)`, `stubUsageStore(t)`, `stubRefcountStore(t)`, `drainCmds(t, m, cmd)`, `resized(t, m, width, height)`, `assertFits(t, name, view, width, height)`, `f64(v)`, `timedTableRows(at)`, `deepseekDear`, `ollamaPeak`
- Produces:
  - `tableInput.slots []profiles.SlotCheck` — `buildRows` passes it as `catalog.Input.SlotProfiles`
  - `func (r tableRow) costModel() config.Model` — the row's own model, or the default slot's model for a profile row
  - `sortRows` orders native rows, then profile rows by `Profile.Check.Index`, then every other row as before
  - `modelItem.profile *catalog.ProfileRow` — non-nil on a profile row; `modelItem.model` then holds only the row id
  - `func slotLine(p *catalog.ProfileRow, width int) string`
  - `model.slotChecks []profiles.SlotCheck` — read by `tableFor`, copied by `refreshTable`
  - `func pickerBlockedReason(cfg *config.Config, agent string, row catalog.Row) string` — returns `row.BlockReason()` for a profile row without calling `ResolveRoute`
  - `func launchableModels(cfg *config.Config, agent string, rows []catalog.Row) []config.Model` — never returns a profile row
  - Test fixtures in `wt/internal/tui/slot_row_test.go`, for Tasks 7 and 9:
    - `func slotTestConfig() *config.Config` — `modelTestConfig()` plus cloud provider `openrouter` with `openrouter/a` then `openrouter/b`, in that registry order; no model has `Native` set
    - `func slotCheckFor(index int, name, def string, slots map[string]string) profiles.SlotCheck`
    - `func slotPicker(t *testing.T, cfg *config.Config, checks []profiles.SlotCheck, width, height int) model`

**What you need to know before you start**

- wt's model picker is a table. Each line is a `catalog.Row`, wrapped by the picker in a `tableRow` and drawn as a `modelItem`.
- Task 4 added a second kind of row: a slot profile. Its `Row.Model` holds only the id `profile/<name>`. Every other `Model` field is zero. The profile itself is in `Row.Profile`.
- A profile row has no provider, so `cfg.ResolveRoute(row.Model, …)` fails on it with `unknown provider "profile"`. Code that asks for a route must skip a profile row. This task adds those guards.
- Nothing in production sets `model.slotChecks` or `catalog.Input.SlotProfiles` until Tasks 8 and 9. After this task the picker can draw a profile row, but only a test makes one appear.
- This task does not change what Enter does. Enter on a profile row is wired in Task 9. Until then only a test that sets `slotChecks` by hand can reach it.
- No result of Task 1's checks changes this task.

Choices where the spec is silent (each follows the surrounding code):

- `slotLine` with a width of zero or less cuts nothing, as `cutLines` does.
- LOC shows `-` for a profile none of whose slots resolved, as every empty cell of the table does.
- A blocked row's reason on the slot line is cut with an ellipsis (`cutLines`), not wrapped, so the frame keeps its height.
- A table whose only row is a profile keeps the picker open. The single-row shortcut launches the row as a model, which a profile row is not.
- The smoke test in Part B passes as soon as it is written. `smoke.walk` never sets `SlotProfiles`, and the test pins that.

How to read the steps:

- Every command is written to be run from the repo root.
- An edit is shown as a **Find** block and a **Replace it with** block. Each Find block occurs exactly once in its file, tabs included.
- Line numbers in expected output are those of the files as this plan leaves them. They may differ by a few lines.

Every `Test*` function needs a `//` comment above it that says what it tests and what breaks for the user if it regresses. The tests below have them; keep them.

---

#### Part A: the non-TUI path never treats a profile row as a model

- [ ] **Step 1: Write the failing tests**

In `wt/cmd/wt/resolve_test.go`, add one line to the import block so it reads:

```go
import (
	"errors"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rotation"
)
```

Append this to the end of the same file:

```go
// slotGuardRows builds the catalog rows for agent "claude" with three slot
// profiles opted in: "cloud" (every slot a cloud model, a launch row), "idle"
// (its one local model is on disk and not running, a start row) and "broken"
// (a slot names a model the registry does not have, a blocked row). It
// returns the config and the rows.
func slotGuardRows(t *testing.T) (*config.Config, []catalog.Row) {
	t.Helper()
	cfg := &config.Config{
		Providers: []config.Provider{
			{ID: "openrouter", Location: config.LocationCloud},
			{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}},
		},
		Models: []config.Model{
			{ID: "openrouter/a", ProviderID: "openrouter", ModelName: "a", Tags: []string{"code"}},
			{ID: "omlx/qwen3.8", ProviderID: "omlx", ModelName: "qwen3.8", Tags: []string{"code"}},
		},
		Agents: []config.Agent{{Name: "claude", SupportedProviders: []string{"openrouter", "omlx"}}},
	}
	cfg.SetLitellmForTest(config.LitellmState{Enabled: true, URL: "http://localhost:4000", APIKey: "sk-test"})
	check := func(index int, name string, slots map[string]string) profiles.SlotCheck {
		return profiles.SlotCheck{
			Index:    index,
			Profile:  profiles.SlotProfile{Name: name, Agent: "claude", Default: "opus", Slots: slots},
			Usable:   true,
			Declared: []string{"fable", "opus", "sonnet", "haiku"},
		}
	}
	snap := localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "omlx", ModelID: "omlx/qwen3.8", Artifact: "qwen3.8", ModelName: "qwen3.8", Registered: true, ArtifactKnown: true},
		},
	}
	eligible, err := cfg.EligibleModels("claude", "", "")
	if err != nil {
		t.Fatalf("EligibleModels: %v", err)
	}
	rows := catalog.Build(catalog.Input{
		Config: cfg, Agent: "claude", Models: eligible, Inventory: &snap,
		SlotProfiles: []profiles.SlotCheck{
			check(0, "cloud", map[string]string{"opus": "openrouter/a"}),
			check(1, "idle", map[string]string{"opus": "omlx/qwen3.8"}),
			check(2, "broken", map[string]string{"opus": "openrouter/gone"}),
		},
	})
	return cfg, rows
}

// TestLaunchableModelsExcludesProfileRows verifies a slot profile's row is
// never in the launchable list, whatever its verdict. Rotation and the
// single-launchable auto-launch read that list, and a profile row's model is
// only an id: returned from it, a bare `wt -W x -A claude` would try to
// launch "profile/cloud" as a model and fail with "unknown provider".
func TestLaunchableModelsExcludesProfileRows(t *testing.T) {
	cfg, rows := slotGuardRows(t)
	if row, ok := catalog.Find(rows, "profile/cloud"); !ok || row.Action() != catalog.ActionLaunch {
		t.Fatalf("fixture: profile/cloud found = %t action = %v, want a launch row", ok, row.Action())
	}

	var got []string
	for _, m := range launchableModels(cfg, "claude", rows) {
		got = append(got, m.ID)
	}

	if strings.Join(got, ",") != "openrouter/a" {
		t.Errorf("launchable = %v, want the one cloud model and no profile", got)
	}
}

// TestPickerBlockedReasonReadsAProfileRowsOwnVerdict verifies the non-TUI
// path judges a profile row by resolved validation and never asks
// ResolveRoute about it. A profile row has no provider, so the route lookup
// fails with "unknown provider": a start profile would be refused with that
// text instead of starting its local model, and a blocked profile would lose
// its real reason.
func TestPickerBlockedReasonReadsAProfileRowsOwnVerdict(t *testing.T) {
	cfg, rows := slotGuardRows(t)
	for _, c := range []struct {
		id     string
		action catalog.Action
		want   string
	}{
		{"profile/cloud", catalog.ActionLaunch, ""},
		{"profile/idle", catalog.ActionStart, ""},
		{"profile/broken", catalog.ActionBlock, "slot opus: openrouter/gone is not in the registry"},
	} {
		row, ok := catalog.Find(rows, c.id)
		if !ok || row.Action() != c.action {
			t.Fatalf("fixture: %s found = %t action = %v, want action %v", c.id, ok, row.Action(), c.action)
		}
		if got := pickerBlockedReason(cfg, "claude", row); got != c.want {
			t.Errorf("pickerBlockedReason(%s) = %q, want %q", c.id, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run them and see them fail**

Run: `cd wt && go test ./cmd/wt -run 'TestLaunchableModelsExcludesProfileRows|TestPickerBlockedReasonReadsAProfileRowsOwnVerdict'`

Expected: FAIL.

```
--- FAIL: TestLaunchableModelsExcludesProfileRows (0.00s)
    resolve_test.go:690: launchable = [openrouter/a profile/cloud], want the one cloud model and no profile
--- FAIL: TestPickerBlockedReasonReadsAProfileRowsOwnVerdict (0.00s)
    resolve_test.go:716: pickerBlockedReason(profile/idle) = "profile/idle cannot be launched: unknown provider \"profile\" for model \"profile/idle\"", want ""
    resolve_test.go:716: pickerBlockedReason(profile/broken) = "", want "slot opus: openrouter/gone is not in the registry"
FAIL
```

If a test stops at `fixture: …` instead, Task 4's rows differ from what this task expects. Fix that before going on.

- [ ] **Step 3: Write the implementation**

In `wt/cmd/wt/resolve.go`, replace `pickerBlockedReason` and `launchableModels` (the two functions and the comments above them) with:

```go
// pickerBlockedReason mirrors renderTable's route decoration for a single
// row and returns the reason the picker refuses it, or "" when the row is
// usable. The switch order matches renderTable's exactly: the route refusal
// (an Unmapped cloud row) wins over the route-error case, and a launch row
// with an unresolvable route stays usable (the picker leaves it selectable
// and reports the error on Enter). The refusal is catalog.Row.RefusedByRoute,
// the one rule the table and `wt smoke` also call; keeping this next to
// resolveModel is what lets the non-TUI path make the same decisions the
// table displays.
func pickerBlockedReason(cfg *config.Config, agent string, row catalog.Row) string {
	// A slot-profile row has no route of its own: its Model is the row id
	// only, and its verdict is resolved validation's (catalog.ResolveSlotProfile).
	if row.IsProfile() {
		return row.BlockReason()
	}
	route, err := cfg.ResolveRoute(row.Model, agents.ProtocolsFor(agent))
	switch {
	case row.RefusedByRoute(route, err):
		return row.RouteRefusal(route, err)
	case err != nil && row.Action() == catalog.ActionStart:
		return row.Model.ID + " cannot be launched: " + err.Error()
	}
	return ""
}
```

```go
// launchableModels narrows rows to the models a launch can use right now:
// cloud rows plus local rows the probe reports as running — minus Unmapped
// cloud rows the picker would refuse (unselectable there, so rotation must
// never pick them either). A running discovered row is launchable (#179
// Phase B: it is routed), but rotation itself only walks registry models, so
// it is never rotated into. A start row is
// deliberately excluded — rotation and auto-resolution must never start a
// server, only a -M pin may. A launch row whose route errors stays in: the
// picker keeps such rows selectable too and reports the failure on Enter.
func launchableModels(cfg *config.Config, agent string, rows []catalog.Row) []config.Model {
	var out []config.Model
	for _, r := range rows {
		// A slot profile is reached by a -M pin only: it is never rotated
		// into and never the single launchable model.
		if r.IsProfile() {
			continue
		}
		if r.Action() != catalog.ActionLaunch {
			continue
		}
		if r.Unmapped && pickerBlockedReason(cfg, agent, r) != "" {
			continue
		}
		out = append(out, r.Model)
	}
	return out
}
```

No import changes. `resolveModel` is not edited in this task.

- [ ] **Step 4: Run the tests and see them pass**

Run: `cd wt && go test ./cmd/wt -run 'TestLaunchableModelsExcludesProfileRows|TestPickerBlockedReasonReadsAProfileRowsOwnVerdict|TestResolveModel'`

Expected: `ok  	github.com/ohanaverse/local-ai-setup/wt/cmd/wt`. The existing `TestResolveModel…` tests pass unchanged, which is the check that the path without a pin behaves as before.

- [ ] **Step 5: Commit**

```bash
(cd wt && gofmt -l ./cmd/wt)
```

Expected: no output.

```bash
git add wt/cmd/wt/resolve.go wt/cmd/wt/resolve_test.go
git commit -m "feat(wt): keep slot-profile rows out of rotation and the route check on the non-TUI path"
```

---

#### Part B: `wt smoke` never sees a profile row

- [ ] **Step 6: Write the test**

In `wt/internal/smoke/smoke_test.go`, add one line to the import block. Its last group becomes:

```go
	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)
```

Append this to the end of the same file:

```go
// TestCandidatesNeverIncludeASlotProfile verifies `wt smoke` offers no slot
// profile, even with a valid slot-profiles.toml for one of its agents in the
// config home. Smoke tests one model against each agent; a profile is several
// models and one agent, and a profile row among the candidates would be run
// as a model named "profile/<name>" and fail every agent.
func TestCandidatesNeverIncludeASlotProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	if err := os.MkdirAll(filepath.Dir(profiles.SlotPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "[[profile]]\nname = \"all\"\nagent = \"claude\"\ndefault = \"opus\"\n\n[profile.slots]\nopus = \"ollama/running:1\"\n"
	if err := os.WriteFile(profiles.SlotPath(), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, restore := candidatesFixture(t)
	defer restore()

	got := Candidates(cfg)

	if len(got) != 2 {
		t.Errorf("Candidates returned %d rows, want the fixture's two (running and idle)", len(got))
	}
	for _, c := range got {
		if c.Row.IsProfile() || strings.HasPrefix(c.Row.Model.ID, profiles.SlotIDPrefix) {
			t.Errorf("Candidates offers the slot profile %s", c.Row.Model.ID)
		}
	}
}
```

The test sets `XDG_CONFIG_HOME` to a temp directory before it writes the file, so it never touches the real config home. `candidatesFixture` already exists in this file: three ollama models (one running, one idle, one not on disk) and one agent, `claude`.

- [ ] **Step 7: Run it and see it pass**

Run: `cd wt && go test ./internal/smoke -run TestCandidates -v`

Expected: PASS, with these three lines among the output.

```
--- PASS: TestCandidatesIncludeStartRowsButNotBlocked (0.00s)
--- PASS: TestCandidatesGateStartRowsPerAgent (0.00s)
--- PASS: TestCandidatesNeverIncludeASlotProfile (0.00s)
```

There is no failing run and no implementation step here. `smoke.walk` builds its catalog without `SlotProfiles`, and no code in `internal/smoke` reads `slot-profiles.toml`. The test fails the day either changes. Do not edit `wt/internal/smoke/smoke.go`.

- [ ] **Step 8: Commit**

```bash
git add wt/internal/smoke/smoke_test.go
git commit -m "test(wt): pin that wt smoke offers no slot profile"
```

---

#### Part C: the picker opts in to profile rows and sorts them in their own band

- [ ] **Step 9: Write the failing tests**

Create `wt/internal/tui/slot_row_test.go` with exactly this content:

```go
// Tests for a slot profile's row in the model picker: its cells, where it
// sorts, the slot line under the table, and the fit of the screen around it.
// The fixtures here are shared with the cursor and launch-flow tests.
package tui

import (
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)

// slotTestConfig is modelTestConfig (pin_model_test.go) plus an "openrouter"
// cloud provider with two models, both tagged "code" and supported by the
// "claude" agent: "openrouter/a" (family "a", provider-side name "a", priced
// 0.40 / 0.04 / 1.60 per million) and "openrouter/b" (name "b", no price).
// modelTestConfig already sets the [litellm] url and key.
func slotTestConfig() *config.Config {
	cfg := modelTestConfig()
	cfg.Providers = append(cfg.Providers, config.Provider{
		ID: "openrouter", Location: config.LocationCloud,
		Protocols: []config.Protocol{config.ProtocolOpenAIChat},
		Auth:      config.AuthConfig{Type: "api_key", BaseURL: "https://openrouter.ai/api"},
	})
	cfg.Models = append(cfg.Models,
		config.Model{ID: "openrouter/a", ProviderID: "openrouter", ModelName: "a", Family: "a", Tags: []string{"code"}, Cost: config.ModelCost{
			InputPricePerMillion: f64(0.40), CachePricePerMillion: f64(0.04), OutputPricePerMillion: f64(1.60)}},
		config.Model{ID: "openrouter/b", ProviderID: "openrouter", ModelName: "b", Family: "b", Tags: []string{"code"}},
	)
	cfg.Agents[0].SupportedProviders = append(cfg.Agents[0].SupportedProviders, "openrouter")
	return cfg
}

// slotCheckFor is a profile that passed static validation: usable, for agent
// "claude", with Claude's four slots declared. index is its position in the
// file, which is the order profile rows sort in.
func slotCheckFor(index int, name, def string, slots map[string]string) profiles.SlotCheck {
	return profiles.SlotCheck{
		Index:    index,
		Profile:  profiles.SlotProfile{Name: name, Agent: "claude", Default: def, Slots: slots},
		Usable:   true,
		Declared: []string{"fable", "opus", "sonnet", "haiku"},
	}
}

// slotPicker enters the model phase for "claude" on cfg with checks as the
// picker's slot profiles, in a terminal of width x height. It isolates the
// state directory and the usage and refcount stores (flowEnter). The
// inventory is whatever the test stubbed before the call; with no stub it is
// TestMain's: every local model on disk and none running.
func slotPicker(t *testing.T, cfg *config.Config, checks []profiles.SlotCheck, width, height int) model {
	t.Helper()
	m := flowEnter(t, model{cfg: cfg, agent: "claude", selectedPath: t.TempDir(), slotChecks: checks, width: width, height: height}, "claude")
	return resized(t, m, width, height)
}

// slotRowChecks are the three profiles the row tests draw: "cloud" (every
// slot a cloud model, a launch row), "mixed" (a cloud default and the
// fixture's local model) and "broken" (one slot names a model the registry
// does not have, a blocked row).
func slotRowChecks() []profiles.SlotCheck {
	return []profiles.SlotCheck{
		slotCheckFor(0, "cloud", "opus", map[string]string{"opus": "openrouter/a", "haiku": "openrouter/b"}),
		slotCheckFor(1, "mixed", "sonnet", map[string]string{"sonnet": "openrouter/a", "haiku": "omlx/qwen3.8"}),
		slotCheckFor(2, "broken", "opus", map[string]string{"opus": "openrouter/a", "haiku": "openrouter/gone"}),
	}
}

// idleOmlxSnapshot is the fixture's local model on disk and not running,
// which makes its own row, and a profile that names it, a start row.
func idleOmlxSnapshot() localmodels.Snapshot {
	return localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "omlx", Artifact: "qwen3.8", ModelID: "omlx/qwen3.8", ModelName: "qwen3.8", Registered: true, ArtifactKnown: true},
		},
	}
}

// slotTable builds the picker's table for "claude" over cfg and snap with
// checks opted in, the way tableFor does, with a usage store of its own.
func slotTable(t *testing.T, cfg *config.Config, snap localmodels.Snapshot, checks []profiles.SlotCheck) modelTable {
	t.Helper()
	models, err := cfg.EligibleModels("claude", "", "")
	if err != nil {
		t.Fatalf("EligibleModels: %v", err)
	}
	store := stubUsageStore(t)
	for i := 0; i < 2; i++ {
		if err := store.RecordFor("claude", "profile/cloud"); err != nil {
			t.Fatalf("seed usage: %v", err)
		}
	}
	return buildTable(tableInput{cfg: cfg, agent: "claude", models: models, inventory: &snap, usage: store, slots: checks}, stubRefcountStore(t), "")
}

// tableItem is the table's item for id.
func tableItem(t *testing.T, tbl modelTable, id string) *modelItem {
	t.Helper()
	for _, it := range tbl.items {
		if it.model.ID == id {
			return it
		}
	}
	t.Fatalf("the table has no row %q", id)
	return nil
}

// TestProfileRowsNeedTheOptIn pins that the table has profile rows only when
// its input carries the slot profiles, and none under -T or -F. `wt start`
// and `wt smoke` build their picker from the same function without them; a
// profile row there would be offered as a model to start.
func TestProfileRowsNeedTheOptIn(t *testing.T) {
	cfg := slotTestConfig()
	models, _ := cfg.EligibleModels("claude", "", "")
	snap := runningOmlxSnapshot()
	count := func(in tableInput) int {
		in.cfg, in.agent, in.models, in.inventory = cfg, "claude", models, &snap
		n := 0
		for _, it := range buildTable(in, nil, "").items {
			if strings.HasPrefix(it.model.ID, profiles.SlotIDPrefix) {
				n++
			}
		}
		return n
	}
	if got := count(tableInput{slots: slotRowChecks()}); got != 3 {
		t.Errorf("with the slot profiles: %d profile rows, want 3", got)
	}
	if got := count(tableInput{}); got != 0 {
		t.Errorf("without the slot profiles: %d profile rows, want none", got)
	}
	if got := count(tableInput{slots: slotRowChecks(), hideDiscovered: true}); got != 0 {
		t.Errorf("under -T or -F: %d profile rows, want none", got)
	}
}

// TestSortRowsPutsProfilesBetweenNativeAndTheRest pins the picker's three
// bands: native rows, then profile rows in file order, then every other row
// in the order it had before. The sort order is also the default selection,
// so a profile sorted by cost or id would move whenever a price changed, and
// one sorted above a native row would become what a bare Enter picks.
func TestSortRowsPutsProfilesBetweenNativeAndTheRest(t *testing.T) {
	profile := func(index int, name string, verdict catalog.Action) tableRow {
		c := slotCheckFor(index, name, "opus", map[string]string{"opus": "openrouter/a"})
		return tableRow{Row: catalog.Row{Model: config.Model{ID: c.Profile.ID()}, Status: catalog.StatusOK,
			Profile: &catalog.ProfileRow{Check: c, Verdict: verdict}}}
	}
	rows := []tableRow{
		{Row: catalog.Row{Model: config.Model{ID: "omlx/idle"}, Location: config.LocationLocal}},
		{Row: catalog.Row{Model: config.Model{ID: "openrouter/dear", Cost: config.ModelCost{OutputPricePerMillion: f64(9)}}, Location: config.LocationCloud}},
		profile(2, "zeta", catalog.ActionLaunch),
		{Row: catalog.Row{Model: config.Model{ID: "openrouter/cheap", Cost: config.ModelCost{OutputPricePerMillion: f64(1)}}, Location: config.LocationCloud}},
		profile(0, "start", catalog.ActionStart),
		{Row: catalog.Row{Model: config.Model{ID: "claude/native", Native: true}, Location: config.LocationCloud}},
		profile(1, "alpha", catalog.ActionBlock),
	}

	sortRows(rows)

	var got []string
	for _, r := range rows {
		got = append(got, r.Model.ID)
	}
	want := "claude/native,profile/start,profile/alpha,profile/zeta,openrouter/cheap,openrouter/dear,omlx/idle"
	if strings.Join(got, ",") != want {
		t.Errorf("order = %s\nwant    %s", strings.Join(got, ","), want)
	}
}

// TestRefreshedTableKeepsProfileRows pins that a table rebuilt after a start
// attempt still has its profile rows. The rebuild runs from a reduced copy of
// the model, and a field left out of that copy is lost on every refresh: the
// profiles would vanish from the picker the first time a start failed.
func TestRefreshedTableKeepsProfileRows(t *testing.T) {
	stubInventory(t, runningOmlxSnapshot())
	m := slotPicker(t, slotTestConfig(), slotRowChecks(), 80, 24)
	before := strings.Join(itemIDs(m), ",")

	m, cmd := m.refreshTable()
	m = drainCmds(t, m, cmd)

	if got := strings.Join(itemIDs(m), ","); got != before || !strings.Contains(got, "profile/mixed") {
		t.Errorf("rows after a refresh = %s, want %s", got, before)
	}
}
```

Notes on the fixtures:

- `slotTestConfig`, `slotCheckFor` and `slotPicker` are used by later tasks. Do not rename them or change what they return.
- `modelTestConfig()` builds its config in memory, so no model has `Native` set, and its local model is `omlx/qwen3.8`.
- `slotPicker` does not stub the inventory. The package's `TestMain` default reports every local model on disk and none running. A test that wants the local model running calls `stubInventory(t, runningOmlxSnapshot())` first.

- [ ] **Step 10: Run them and see them fail**

Run: `cd wt && go test ./internal/tui -run 'TestProfileRowsNeedTheOptIn|TestSortRowsPutsProfilesBetweenNativeAndTheRest|TestRefreshedTableKeepsProfileRows'`

Expected: the build fails.

```
internal/tui/slot_row_test.go:56:80: unknown field slotChecks in struct literal of type model
internal/tui/slot_row_test.go:97:106: unknown field slots in struct literal of type tableInput
internal/tui/slot_row_test.go:130:29: unknown field slots in struct literal of type tableInput
internal/tui/slot_row_test.go:136:29: unknown field slots in struct literal of type tableInput
FAIL	github.com/ohanaverse/local-ai-setup/wt/internal/tui [build failed]
```

- [ ] **Step 11: Write the implementation**

Five edits in `wt/internal/tui/modelrows.go`.

(a) The import block.

Find:

```go
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
```

Replace it with:

```go
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
```

(b) The opt-in field, at the end of `type tableInput struct`.

Find:

```go
	now time.Time
}
```

Replace it with:

```go
	now time.Time
	// slots opts in to slot-profile rows (catalog.Input.SlotProfiles): the
	// statically validated profiles. nil, which is what the picker of
	// `wt start` and `wt smoke` leaves it at, means no profile rows.
	slots []profiles.SlotCheck
}
```

(c) `buildRows` passes it on.

Find:

```go
		HideDiscovered: in.hideDiscovered,
	})
```

Replace it with:

```go
		HideDiscovered: in.hideDiscovered,
		SlotProfiles:   in.slots,
	})
```

(d) The comment above `sortRows` gains a paragraph.

Find:

```go
// native row that resolves local and is not running still sorts after the
// native group-1 rows, by id, not "in group-1 order".
//
```

Replace it with:

```go
// native row that resolves local and is not running still sorts after the
// native group-1 rows, by id, not "in group-1 order".
//
// Slot-profile rows are a band of their own between the two halves:
// native rows, then profile rows in the order of slot-profiles.toml, then
// every other row. A profile row is never compared by cost, usage or id.
//
```

(e) The band rule, at the top of `sortRows`.

Find:

```go
	group1 := func(r tableRow) bool { return r.Location != config.LocationLocal || r.Ready() }
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.Model.Native != b.Model.Native {
			return a.Model.Native
		}
```

Replace it with:

```go
	group1 := func(r tableRow) bool { return r.Location != config.LocationLocal || r.Ready() }
	// band is the first thing compared: 0 for a native row, 1 for a
	// slot-profile row, 2 for every other row. A lower band sorts first.
	band := func(r tableRow) int {
		switch {
		case r.Profile != nil:
			return 1
		case r.Model.Native:
			return 0
		}
		return 2
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if ba, bb := band(a), band(b); ba != bb {
			return ba < bb
		}
		if a.Profile != nil {
			return a.Profile.Check.Index < b.Profile.Check.Index
		}
```

After (e) the whole function reads:

```go
// sortRows orders rows in place, native models first (#172). A native model
// carries no per-token price, so plain cost-ascending sank it as "no cost
// data" below every priced model, when it is in fact the agent's own
// subscription model. The native/non-native split is the only special case:
// each half then falls through to the same group rules — group 1 (cloud +
// running local) by cost ascending then 7-day usage ascending then id; group 2
// (local that is not running, or still loading) alphabetical by id — so a
// native row that resolves local and is not running still sorts after the
// native group-1 rows, by id, not "in group-1 order".
//
// Slot-profile rows are a band of their own between the two halves:
// native rows, then profile rows in the order of slot-profiles.toml, then
// every other row. A profile row is never compared by cost, usage or id.
//
// This order is also the pickers' default selection: newPickModel never calls
// Select, so its highlighted row is index 0 (the first sorted row), and
// enterModelPhase's no-rotation fallback picks the first actionable row. A
// native model in the list therefore becomes what a bare Enter launches.
func sortRows(rows []tableRow) {
	// Ready, not Running: a model omlx is still loading is a start row (#259)
	// and sorts with them, so it is never the default pick while it cannot
	// answer.
	group1 := func(r tableRow) bool { return r.Location != config.LocationLocal || r.Ready() }
	// band is the first thing compared: 0 for a native row, 1 for a
	// slot-profile row, 2 for every other row. A lower band sorts first.
	band := func(r tableRow) int {
		switch {
		case r.Profile != nil:
			return 1
		case r.Model.Native:
			return 0
		}
		return 2
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if ba, bb := band(a), band(b); ba != bb {
			return ba < bb
		}
		if a.Profile != nil {
			return a.Profile.Check.Index < b.Profile.Check.Index
		}
		ga, gb := group1(a), group1(b)
		if ga != gb {
			return ga
		}
		if !ga {
			return a.Model.ID < b.Model.ID
		}
		ka, kb := rowCostKey(a), rowCostKey(b)
		if ka.noData != kb.noData {
			return !ka.noData
		}
		if ka.out != kb.out {
			return ka.out < kb.out
		}
		if ka.in != kb.in {
			return ka.in < kb.in
		}
		if a.counts.SevenDay != b.counts.SevenDay {
			return a.counts.SevenDay < b.counts.SevenDay
		}
		return a.Model.ID < b.Model.ID
	})
}
```

Bands 0 and 2 run the comparison that was there before, so the existing sort tests pass unchanged.

Four edits in `wt/internal/tui/app.go`.

(f) The import block.

Find:

```go
	"github.com/ohanaverse/local-ai-setup/wt/internal/ollamacheck"
```

Replace it with:

```go
	"github.com/ohanaverse/local-ai-setup/wt/internal/ollamacheck"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
```

(g) The field, in `type model struct`.

Find:

```go
	activeFamily string // comma-delimited family filter from -F/--family; "" = no filter
```

Replace it with:

```go
	activeFamily string // comma-delimited family filter from -F/--family; "" = no filter

	// slotChecks are the statically validated slot profiles
	// (profiles.ValidateSlots). nil means the picker shows no profile row.
	slotChecks []profiles.SlotCheck
```

(h) `tableFor` passes it to the table.

Find:

```go
		usage:          newUsageStore(), stats: surveyStats,
	}, newRefcountStore(), lastID)
```

Replace it with:

```go
		usage:          newUsageStore(), stats: surveyStats,
		slots: m.slotChecks,
	}, newRefcountStore(), lastID)
```

(i) `refreshTable` copies it. The table is rebuilt from a reduced copy of the model, and a field left out of that copy is lost on every refresh.

Find:

```go
	// The command captures only the values tableFor reads (cfg and the active
	// -T/-F filters), never the live model: the goroutine must not touch it,
	// and copying the whole struct would pin its lists and inputs for the
	// duration of the probe.
	src := model{cfg: m.cfg, activeTags: m.activeTags, activeFamily: m.activeFamily}
```

Replace it with:

```go
	// The command captures only the values tableFor reads (cfg, the active
	// -T/-F filters and the slot profiles), never the live model: the
	// goroutine must not touch it, and copying the whole struct would pin its
	// lists and inputs for the duration of the probe.
	src := model{cfg: m.cfg, activeTags: m.activeTags, activeFamily: m.activeFamily, slotChecks: m.slotChecks}
```

- [ ] **Step 12: Run the tests and see them pass**

Run: `cd wt && go test ./internal/tui -run 'TestProfileRowsNeedTheOptIn|TestSortRows|TestRefreshedTableKeepsProfileRows'`

Expected: `ok  	github.com/ohanaverse/local-ai-setup/wt/internal/tui`. The pattern `TestSortRows` also runs the five existing sort tests (`TestSortRowsTwoGroups`, `TestSortRowsNativeFirst`, `TestSortRowsNativeTieBreak`, `TestSortRowsPutsALoadingModelWithTheStartRows`, `TestSortRowsUsesThePriceInForce`).

- [ ] **Step 13: Commit**

```bash
(cd wt && gofmt -l ./internal/tui)
```

Expected: no output.

```bash
git add wt/internal/tui/modelrows.go wt/internal/tui/app.go wt/internal/tui/slot_row_test.go
git commit -m "feat(wt): the model picker takes slot profiles and sorts them after the native rows"
```

---

#### Part D: a profile row's cells, with no route lookup

- [ ] **Step 14: Write the failing tests**

Append this to the end of `wt/internal/tui/slot_row_test.go`. No import changes.

```go
// TestProfileRowCells pins what a slot profile's row shows in each column,
// and that the row is judged by resolved validation alone. FAMILY is
// "profile", LOC says cloud, local or mixed, RUNNING is the local model's,
// COST is the default slot model's price, and the usage columns count
// launches recorded under the profile's own id. A profile row has no route
// of its own, so asking ResolveRoute about it fails with "unknown provider":
// the row would be marked "(unavailable)", and a start profile would be
// turned into a blocked one that Enter refuses.
func TestProfileRowCells(t *testing.T) {
	for _, c := range []struct {
		name, id               string
		snap                   localmodels.Snapshot
		loc, running, cost, d7 string
		start                  bool
		blocked                string
	}{
		{"all cloud", "profile/cloud", runningOmlxSnapshot(), "cloud", "-", " 0.4000  0.0400  1.6000", "2", false, ""},
		{"mixed, local running", "profile/mixed", runningOmlxSnapshot(), "mixed", "run", " 0.4000  0.0400  1.6000", "0", false, ""},
		{"mixed, local idle", "profile/mixed", idleOmlxSnapshot(), "mixed", "-", " 0.4000  0.0400  1.6000", "0", true, ""},
		{"blocked", "profile/broken", runningOmlxSnapshot(), "cloud", "-", " 0.4000  0.0400  1.6000", "0", false,
			"slot haiku: openrouter/gone is not in the registry"},
	} {
		tbl := slotTable(t, slotTestConfig(), c.snap, slotRowChecks())
		it := tableItem(t, tbl, c.id)
		cell := func(col int) string { return strings.TrimRight(it.cells[col], " ") }
		for _, want := range []struct {
			col       int
			name, val string
		}{
			{colFamily, "FAMILY", "profile"}, {colModel, "MODEL", c.id}, {colLoc, "LOC", c.loc},
			{colStatus, "STATUS", "ok"}, {colRunning, "RUNNING", c.running}, {colCost, "COST", c.cost},
			{col7D, "7D", c.d7}, {colSurvey, "SURVEY", ""},
		} {
			if got := cell(want.col); got != want.val {
				t.Errorf("%s: %s = %q, want %q", c.name, want.name, got, want.val)
			}
		}
		if it.profile == nil || it.cost != "" || it.ref != 0 {
			t.Errorf("%s: profile set = %t, cost note = %q, ref = %d; want the profile carried, no price note and no in-use count", c.name, it.profile != nil, it.cost, it.ref)
		}
		if it.exception != "" || it.start != c.start || it.blocked != c.blocked {
			t.Errorf("%s: exception = %q start = %t blocked = %q, want no route note, start = %t and blocked = %q",
				c.name, it.exception, it.start, it.blocked, c.start, c.blocked)
		}
	}

	// A profile whose slots resolve to nothing has no location to name.
	none := []profiles.SlotCheck{slotCheckFor(0, "none", "opus", map[string]string{"opus": "openrouter/gone"})}
	it := tableItem(t, slotTable(t, slotTestConfig(), runningOmlxSnapshot(), none), "profile/none")
	if loc, cost := strings.TrimSpace(it.cells[colLoc]), strings.TrimSpace(it.cells[colCost]); loc != "-" || cost != "-" {
		t.Errorf("unresolved profile: LOC = %q COST = %q, want a dash in each", loc, cost)
	}
}

// TestProfileRowCostFollowsTheDefaultSlotsClock pins that a profile row is
// priced as its default slot's model at the table's own instant, with the
// "~" mark when that model is priced by time of day. Priced from the row's
// own (empty) model it would show a dash for every profile.
func TestProfileRowCostFollowsTheDefaultSlotsClock(t *testing.T) {
	cfg := slotTestConfig()
	cfg.Models[config.IndexModelByID(cfg.Models, "openrouter/a")].Cost = timedTableRows(deepseekDear)[0].Model.Cost
	models, _ := cfg.EligibleModels("claude", "", "")
	snap := runningOmlxSnapshot()
	for _, c := range []struct {
		name string
		in   tableInput
		want string
	}{
		{"dear window", tableInput{now: deepseekDear}, " 1.3200  0.0440  3.9600~"},
		{"cheap window", tableInput{now: ollamaPeak}, " 0.6600  0.0220  1.9800~"},
	} {
		in := c.in
		in.cfg, in.agent, in.models, in.inventory, in.slots = cfg, "claude", models, &snap, slotRowChecks()
		it := tableItem(t, buildTable(in, nil, ""), "profile/cloud")
		if got := strings.TrimRight(it.cells[colCost], " "); got != c.want {
			t.Errorf("%s: COST = %q, want %q", c.name, got, c.want)
		}
	}
}
```

- [ ] **Step 15: Run them and see them fail**

Run: `cd wt && go test ./internal/tui -run 'TestProfileRowCells|TestProfileRowCostFollowsTheDefaultSlotsClock'`

Expected: the build fails.

```
internal/tui/slot_row_test.go:228:9: it.profile undefined (type *modelItem has no field or method profile)
internal/tui/slot_row_test.go:229:135: it.profile undefined (type *modelItem has no field or method profile)
FAIL	github.com/ohanaverse/local-ai-setup/wt/internal/tui [build failed]
```

- [ ] **Step 16: Write the implementation**

Two edits in `wt/internal/tui/model_list.go`.

(a) The import block.

Find:

```go
	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
```

Replace it with:

```go
	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
```

(b) The field, at the end of `type modelItem struct`.

Find:

```go
	// price.
	cost string
}
```

Replace it with:

```go
	// price.
	cost string
	// profile is set on a slot-profile row; model then holds only the row id.
	profile *catalog.ProfileRow
}
```

Four edits in `wt/internal/tui/modelrows.go`. A profile row is priced as its default slot's model. The COST cell (`costCell`) and the `~` mark read `price()` and `timePriced()`, so they need no change of their own.

(c) The new method. Insert this directly above the comment that begins `// pricedAt is the row with its clock`:

```go
// costModel is the model a row is priced as: its own, or for a slot-profile
// row the default slot's model. A profile row whose default slot did not
// resolve (a blocked row) has the zero Model, which has no price.
func (r tableRow) costModel() config.Model {
	if r.Profile == nil {
		return r.Model
	}
	return r.Profile.Models[r.Profile.Check.Profile.Default]
}
```

(d) In `pricedAt`.

Find:

```go
		r.in, r.timed = r.Model.Cost.PriceAt(at), r.Model.Cost.TimePriced()
```

Replace it with:

```go
		cost := r.costModel().Cost
		r.in, r.timed = cost.PriceAt(at), cost.TimePriced()
```

(e) In `price`.

Find:

```go
		return r.Model.Cost.Flat()
```

Replace it with:

```go
		return r.costModel().Cost.Flat()
```

(f) In `timePriced`.

Find:

```go
		return r.Model.Cost.TimePriced()
	}
	return r.timed
```

Replace it with:

```go
		return r.costModel().Cost.TimePriced()
	}
	return r.timed
```

Four edits in `wt/internal/tui/modeltable.go`, all inside `renderTable`.

(g) No route is resolved for a profile row.

Find:

```go
		for i, r := range rows {
			wg.Add(1)
			go func
```

Replace it with:

```go
		for i, r := range rows {
			if r.Profile != nil {
				// A slot-profile row has no route of its own: its Model is
				// the row id only, and its verdict is resolved validation's.
				continue
			}
			wg.Add(1)
			go func
```

(h) FAMILY.

Find:

```go
		fam[i] = r.Model.Family
		if fam[i] == "" {
```

Replace it with:

```go
		fam[i] = r.Model.Family
		if r.Profile != nil {
			fam[i] = "profile"
		}
		if fam[i] == "" {
```

(i) LOC.

Find:

```go
		loc := string(r.Location)
		if loc == "" {
```

Replace it with:

```go
		loc := string(r.Location)
		if r.Profile != nil {
			loc = r.Profile.Loc
		}
		if loc == "" {
```

(j) The item carries the profile, and the route decoration is not entered for it.

Find:

```go
			it.start = true
		}
		if cfg != nil {
```

Replace it with:

```go
			it.start = true
		}
		if r.Profile != nil {
			// The slot line takes the mode line's place for this row
			// (slotLine), so the item carries the profile and no price note.
			it.profile, it.cost = r.Profile, ""
		}
		if cfg != nil && r.Profile == nil {
```

MODEL, STATUS, RUNNING, the usage columns and SURVEY need no edit: they already read `r.Model.ID`, `r.Status`, `runningText(r.Row)`, `r.counts` and `r.stats`, which a profile row fills like any other row. The in-use prefix is `refs[r.Model.ID]`, and nothing records a session under a profile id, so it stays blank.

- [ ] **Step 17: Run the tests and see them pass**

Run: `cd wt && go test ./internal/tui -run 'TestProfileRowCells|TestProfileRowCostFollowsTheDefaultSlotsClock|TestProfileRowsNeedTheOptIn'`

Expected: `ok  	github.com/ohanaverse/local-ai-setup/wt/internal/tui`

- [ ] **Step 18: Commit**

```bash
(cd wt && gofmt -l ./internal/tui)
```

Expected: no output.

```bash
git add wt/internal/tui/model_list.go wt/internal/tui/modelrows.go wt/internal/tui/modeltable.go wt/internal/tui/slot_row_test.go
git commit -m "feat(wt): draw a slot profile's row in the model table without resolving a route for it"
```

---

#### Part E: the slot line

- [ ] **Step 19: Write the failing test**

In `wt/internal/tui/slot_row_test.go`, add `lipgloss` to the import block so it reads:

```go
import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)
```

Append this to the end of the file:

```go
// slotLineRow is a four-slot profile with names of the length real ones
// have: the default is opus, so it is listed first, ahead of fable.
func slotLineRow() *catalog.ProfileRow {
	c := slotCheckFor(0, "ollama-cloud", "opus", map[string]string{
		"fable": "ollama/kimi-k3:cloud", "opus": "ollama/glm-5.2:cloud",
		"sonnet": "ollama/glm-5.3-flash:cloud", "haiku": "omlx/qwen3.8",
	})
	models := map[string]config.Model{}
	for slot, id := range c.Profile.Slots {
		_, name, _ := strings.Cut(id, "/")
		models[slot] = config.Model{ID: id, ModelName: name}
	}
	return &catalog.ProfileRow{Check: c, Models: models, Verdict: catalog.ActionLaunch, Loc: "mixed"}
}

// TestSlotLine pins the line under the table for a highlighted profile row:
// the filled slots, the default first and marked, each with its model's
// provider-side name; whole entries only, with the ones that do not fit
// counted as "+N more"; and a blocked row's reason in its place. The footer
// is clipped at the terminal's edge with no sign of the cut, so an entry cut
// there would read as another model's name.
func TestSlotLine(t *testing.T) {
	const (
		opus   = "opus* glm-5.2:cloud"
		fable  = "fable kimi-k3:cloud"
		sonnet = "sonnet glm-5.3-flash:cloud"
		haiku  = "haiku qwen3.8"
	)
	all := opus + " · " + fable + " · " + sonnet + " · " + haiku
	if lipgloss.Width(all) != 86 {
		t.Fatalf("the fixture's full line is %d columns, want 86: the widths below are counted from it", lipgloss.Width(all))
	}
	for _, c := range []struct {
		width int
		want  string
	}{
		{0, all},
		{116, all},
		{86, all},
		{85, opus + " · " + fable + " · " + sonnet + " · +1 more"},
		{80, opus + " · " + fable + " · " + sonnet + " · +1 more"},
		{79, opus + " · " + fable + " · +2 more"},
		{76, opus + " · " + fable + " · +2 more"},
		{51, opus + " · " + fable + " · +2 more"},
		{50, opus + " · +3 more"},
		{36, opus + " · +3 more"},
		{29, opus + " · +3 more"},
		{28, "+4 more"},
		{7, "+4 more"},
		{6, ""},
	} {
		got := slotLine(slotLineRow(), c.width)
		if got != c.want {
			t.Errorf("slotLine(width %d) = %q, want %q", c.width, got, c.want)
		}
		if c.width > 0 && lipgloss.Width(got) > c.width {
			t.Errorf("slotLine(width %d) is %d columns wide", c.width, lipgloss.Width(got))
		}
	}

	// Only the slots the profile fills are listed; the others fall back to
	// the default's model and would repeat it.
	two := slotLineRow()
	two.Check.Profile.Default = "sonnet"
	two.Check.Profile.Slots = map[string]string{"sonnet": "ollama/glm-5.3-flash:cloud", "opus": "ollama/glm-5.2:cloud"}
	if got, want := slotLine(two, 76), "sonnet* glm-5.3-flash:cloud · opus glm-5.2:cloud"; got != want {
		t.Errorf("two filled slots: slotLine = %q, want %q", got, want)
	}

	// A blocked row shows its reason, and says so when it is cut.
	blocked := &catalog.ProfileRow{Check: two.Check, Verdict: catalog.ActionBlock, Reason: "slot haiku: ollama/gemma4:31b-cloud is not in the registry"}
	if got := slotLine(blocked, 76); got != blocked.Reason {
		t.Errorf("blocked row: slotLine = %q, want the reason", got)
	}
	if got, want := slotLine(blocked, 36), "slot haiku: ollama/gemma4:31b-cloud…"; got != want {
		t.Errorf("blocked row at 36 columns: slotLine = %q, want %q", got, want)
	}
	if got := slotLine(nil, 76); got != "" {
		t.Errorf("slotLine(nil) = %q, want nothing", got)
	}
}
```

- [ ] **Step 20: Run it and see it fail**

Run: `cd wt && go test ./internal/tui -run TestSlotLine`

Expected: the build fails.

```
internal/tui/slot_row_test.go:323:10: undefined: slotLine
FAIL	github.com/ohanaverse/local-ai-setup/wt/internal/tui [build failed]
```

(The compiler lists every use of `slotLine`; the first line is enough.)

- [ ] **Step 21: Write the implementation**

Two edits in `wt/internal/tui/layout.go`.

(a) The import block.

Find:

```go
	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
```

Replace it with:

```go
	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
```

(b) The new function. Insert this directly above the line `func listExtent(l list.Model, avail int) (width, floor int) { return tuilayout.ListExtent(l, avail) }`, which follows `modeLine`:

```go
// slotLine is the line under the model table while a slot-profile row is
// highlighted; it takes the mode line's place and adds no line to the frame.
// A blocked row shows its reason, cut with an ellipsis when it is too wide.
// Any other row lists the slots the profile fills, the default first and
// marked "*", the rest in the agent's declared order, each with its model's
// provider-side name. Entries are shown whole or not at all, as the mode
// line's price is (modeLine): the longest run of leading entries that fits
// width is shown and the rest are counted as "+N more". It is "" when not
// even that count fits. A width that is not positive cuts nothing.
func slotLine(p *catalog.ProfileRow, width int) string {
	if p == nil {
		return ""
	}
	if p.Verdict == catalog.ActionBlock {
		return cutLines(p.Reason, width)
	}
	const sep = " · "
	def := p.Check.Profile.Default
	slots := p.Check.Profile.FilledOrder(p.Check.Declared)
	entries := make([]string, len(slots))
	for i, s := range slots {
		label := s
		if s == def {
			label += "*"
		}
		entries[i] = label + " " + p.Models[s].ModelName
	}
	for shown := len(entries); shown >= 0; shown-- {
		parts := append([]string(nil), entries[:shown]...)
		if rest := len(entries) - shown; rest > 0 {
			parts = append(parts, fmt.Sprintf("+%d more", rest))
		}
		line := strings.Join(parts, sep)
		if width <= 0 || lipgloss.Width(line) <= width {
			return line
		}
	}
	return ""
}
```

How the cut works: the loop tries every entry, then one fewer with `+1 more`, and so on down to `+N more` alone. The first line that fits is returned. Widths are display widths (`lipgloss.Width`), as the terminal counts them.

- [ ] **Step 22: Run the test and see it pass**

Run: `cd wt && go test ./internal/tui -run TestSlotLine -v`

Expected:

```
--- PASS: TestSlotLine (0.00s)
PASS
```

- [ ] **Step 23: Commit**

```bash
(cd wt && gofmt -l ./internal/tui)
```

Expected: no output.

```bash
git add wt/internal/tui/layout.go wt/internal/tui/slot_row_test.go
git commit -m "feat(wt): slotLine, a profile's slots in whole entries with +N more"
```

---

#### Part F: the slot line on screen, the single-row shortcut, and the fit

- [ ] **Step 24: Write the failing tests**

In `wt/internal/tui/slot_row_test.go`, add `fmt` to the import block so it reads:

```go
import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)
```

Append this to the end of the file:

```go
// footerLine is the line of view that sits just above the picker's key
// hints: the mode line, or the slot line when a profile row is highlighted.
func footerLine(t *testing.T, view string) string {
	t.Helper()
	lines := strings.Split(view, "\n")
	for i, line := range lines {
		if strings.Contains(line, "[↑/↓] navigate") && i > 0 {
			return strings.TrimSpace(lines[i-1])
		}
	}
	t.Fatalf("the view has no key hints:\n%s", view)
	return ""
}

// TestProfileRowScreenFitsAndShowsItsSlots pins the picker with profile rows
// on a real-sized screen, through Update and View, at 80x24, 40x12 and
// 120x50. With a profile row highlighted the line under the table is that
// profile's slot line, whole entries only, and names neither the LiteLLM
// mode nor a price; on a blocked row it is the reason. It takes the mode
// line's place, so the screen is no taller than with a model highlighted,
// and moving back to a model row brings the mode line back. A slot line on a
// line of its own would push the header off the top of a short terminal.
func TestProfileRowScreenFitsAndShowsItsSlots(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {40, 12}, {120, 50}} {
		width, height := size[0], size[1]
		name := fmt.Sprintf("%dx%d", width, height)
		stubInventory(t, runningOmlxSnapshot())
		m := slotPicker(t, slotTestConfig(), slotRowChecks(), width, height)
		if got, want := strings.Join(itemIDs(m), ","), "profile/cloud,profile/mixed,profile/broken,"; !strings.HasPrefix(got, want) {
			t.Fatalf("%s: rows = %s, want the three profiles first, in file order", name, got)
		}

		m.models.Select(indexOfID(m, "openrouter/a"))
		m = resized(t, m, width, height)
		plain := m.View()
		assertFits(t, name+", model row", plain, width, height)
		if got, want := footerLine(t, plain), "LiteLLM: on   cost 0.4/0.04/1.6"; got != want {
			t.Errorf("%s, model row: the line under the table = %q, want %q", name, got, want)
		}

		for id, want := range map[string]string{
			"profile/cloud":  "opus* a · haiku b",
			"profile/mixed":  "sonnet* a · haiku qwen3.8",
			"profile/broken": cutLines("slot haiku: openrouter/gone is not in the registry", width-2*pickerPadX),
		} {
			m.models.Select(indexOfID(m, id))
			m = resized(t, m, width, height)
			view := m.View()
			assertFits(t, name+", on "+id, view, width, height)
			if got := footerLine(t, view); got != want {
				t.Errorf("%s, on %s: the line under the table = %q, want %q", name, id, got, want)
			}
			if strings.Contains(view, "LiteLLM:") || strings.Contains(view, "cost ") {
				t.Errorf("%s, on %s: the view names the LiteLLM mode or a price:\n%s", name, id, view)
			}
			if got, want := lipgloss.Height(view), lipgloss.Height(plain); got != want {
				t.Errorf("%s, on %s: the view is %d lines, want the %d it is with a model row highlighted", name, id, got, want)
			}
			if !strings.Contains(view, id) {
				t.Errorf("%s, on %s: the row is not on screen:\n%s", name, id, view)
			}
		}
	}
}

// TestProfileSlotLineIsCutToWholeEntriesOnScreen pins the "+N more" cut on
// the screen itself: a profile whose slot list is wider than the picker shows
// as many whole entries as fit and counts the rest, at every size. Without
// the cut the footer's clip would end the line mid-name at the right edge.
func TestProfileSlotLineIsCutToWholeEntriesOnScreen(t *testing.T) {
	cfg := slotTestConfig()
	for _, name := range []string{"glm-5.2:cloud", "kimi-k3:cloud", "glm-5.3-flash:cloud"} {
		cfg.Models = append(cfg.Models, config.Model{ID: "openrouter/" + name, ProviderID: "openrouter", ModelName: name, Family: "x", Tags: []string{"code"}})
	}
	checks := []profiles.SlotCheck{slotCheckFor(0, "ollama-cloud", "opus", map[string]string{
		"fable": "openrouter/kimi-k3:cloud", "opus": "openrouter/glm-5.2:cloud",
		"sonnet": "openrouter/glm-5.3-flash:cloud", "haiku": "omlx/qwen3.8",
	})}
	const opus, fable, sonnet, haiku = "opus* glm-5.2:cloud", "fable kimi-k3:cloud", "sonnet glm-5.3-flash:cloud", "haiku qwen3.8"
	for _, c := range []struct {
		width, height int
		want          string
	}{
		{80, 24, opus + " · " + fable + " · +2 more"},
		{40, 12, opus + " · +3 more"},
		{120, 50, opus + " · " + fable + " · " + sonnet + " · " + haiku},
	} {
		stubInventory(t, runningOmlxSnapshot())
		m := slotPicker(t, cfg, checks, c.width, c.height)
		m.models.Select(indexOfID(m, "profile/ollama-cloud"))
		m = resized(t, m, c.width, c.height)
		view := m.View()
		assertFits(t, "slot line", view, c.width, c.height)
		if got := footerLine(t, view); got != c.want {
			t.Errorf("%dx%d: the line under the table = %q, want %q", c.width, c.height, got, c.want)
		}
	}
}

// TestLoneProfileRowKeepsThePickerOpen pins the single-row shortcut: a table
// whose one row is a profile opens the picker and waits for Enter. The
// shortcut launches the highlighted row as a model, and a profile row's model
// is only its id, so auto-launching it would fail with "unknown provider".
func TestLoneProfileRowKeepsThePickerOpen(t *testing.T) {
	cfg := slotTestConfig()
	// The agent's own eligible list is the local model alone, and the probe
	// confirms it is not on disk, so it has no row.
	cfg.Agents[0].SupportedProviders = []string{"omlx"}
	stubInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
		Entries:   []localmodels.Entry{{ProviderID: "omlx", ModelID: "omlx/qwen3.8", ModelName: "qwen3.8", Registered: true, ArtifactKnown: true}},
	})
	checks := []profiles.SlotCheck{slotCheckFor(0, "cloud", "opus", map[string]string{"opus": "openrouter/a"})}

	m := slotPicker(t, cfg, checks, 80, 24)

	if got := strings.Join(itemIDs(m), ","); got != "profile/cloud" {
		t.Fatalf("rows = %s, want the profile alone", got)
	}
	if m.phase != phaseModel || m.status != "" || m.launchModel.ID != "" {
		t.Errorf("phase = %v status = %q launch model = %q, want the picker open and nothing launched", m.phase, m.status, m.launchModel.ID)
	}
}
```

Then add profile rows to the screens `TestEveryListPhaseFitsTheTerminal` measures. Two edits in `wt/internal/tui/layout_test.go`, both inside that test.

(a) Add a fixture just above the `phases` table. Find:

```go
	phases := []struct {
		name string
		view sizedView
	}{
```

Replace it with:

```go
	// The picker with three slot-profile rows above its model rows and the
	// cursor on one of them, so the slot line (or a blocked row's reason) is
	// the line under the table.
	profilePicker := func(id, status string) sizedView {
		return modelPhase(func(t *testing.T, width, height int) model {
			stubInventory(t, runningOmlxSnapshot())
			m := slotPicker(t, slotTestConfig(), slotRowChecks(), width, height)
			m.models.Select(indexOfID(m, id))
			m.status = status
			return m
		})
	}
	phases := []struct {
		name string
		view sizedView
	}{
```

(b) Add three entries to the table. Find:

```go
		{"model picker, status", modelPicker(layoutStatus)},
```

Replace it with:

```go
		{"model picker, status", modelPicker(layoutStatus)},
		{"model picker, profile row", profilePicker("profile/mixed", "")},
		{"model picker, profile row, status", profilePicker("profile/mixed", layoutStatus)},
		{"model picker, blocked profile row", profilePicker("profile/broken", "")},
```

`modelPhase` sends the terminal's size through `Update` before it reads `View()`, which is the package's rule for a test that sets state by hand. `TestEveryListPhaseFitsTheTerminal` then measures each of the three at widths 40, 80 and 120 and heights 12, 24 and 50.

- [ ] **Step 25: Run them and see them fail**

Run: `cd wt && go test ./internal/tui -run 'TestProfileRowScreenFitsAndShowsItsSlots|TestProfileSlotLineIsCutToWholeEntriesOnScreen|TestLoneProfileRowKeepsThePickerOpen'`

Expected: FAIL. The output includes these lines (each failing size also prints the screen; the order of the `on profile/…` lines varies from run to run).

```
--- FAIL: TestProfileRowScreenFitsAndShowsItsSlots (0.01s)
    slot_row_test.go:405: 80x24, on profile/cloud: the line under the table = "LiteLLM: on", want "opus* a · haiku b"
    slot_row_test.go:405: 40x12, on profile/broken: the line under the table = "LiteLLM: on", want "slot haiku: openrouter/gone is not …"
--- FAIL: TestProfileSlotLineIsCutToWholeEntriesOnScreen (0.00s)
    slot_row_test.go:449: 80x24: the line under the table = "LiteLLM: on", want "opus* glm-5.2:cloud · fable kimi-k3:cloud · +2 more"
    slot_row_test.go:449: 40x12: the line under the table = "LiteLLM: on", want "opus* glm-5.2:cloud · +3 more"
    slot_row_test.go:449: 120x50: the line under the table = "LiteLLM: on", want "opus* glm-5.2:cloud · fable kimi-k3:cloud · sonnet glm-5.3-flash:cloud · haiku qwen3.8"
--- FAIL: TestLoneProfileRowKeepsThePickerOpen (0.00s)
    slot_row_test.go:475: phase = 0 status = "launch failed: unknown provider \"profile\" for model \"profile/cloud\"" launch model = "profile/cloud", want the picker open and nothing launched
FAIL
```

`TestEveryListPhaseFitsTheTerminal` already passes at this point: the mode line a profile row shows before Step 26 fits too. It is in this step so the profile screens are measured from now on.

- [ ] **Step 26: Write the implementation**

One edit in `wt/internal/tui/layout.go`, inside `modelFrames`. The slot line takes the mode line's place, so the footer stays two lines tall and nothing about the fit changes.

Find:

```go
			if it, ok := m.models.SelectedItem().(*modelItem); ok {
				mode = modeLine(mode, it.cost, inner)
			}
```

Replace it with:

```go
			//
			// A slot-profile row has the line to itself: its slots, or its
			// block reason (slotLine). Every profile launches through the
			// proxy, so neither the mode nor a price is named for it.
			if it, ok := m.models.SelectedItem().(*modelItem); ok {
				if it.profile != nil {
					mode = slotLine(it.profile, inner)
				} else {
					mode = modeLine(mode, it.cost, inner)
				}
			}
```

When `slotLine` returns `""` (a terminal too narrow for even `+N more`), the footer's first line is blank and the frame keeps its height.

One edit in `wt/internal/tui/app.go`, at the end of `enterModelPhase`.

Find:

```go
	// The single-row shortcut launches only: a lone start row must show the
	// picker and wait for Enter, never start a server unasked.
	if len(tbl.items) == 1 && tbl.items[0].blocked == "" && !tbl.items[0].start {
```

Replace it with:

```go
	// The single-row shortcut launches only: a lone start row must show the
	// picker and wait for Enter, never start a server unasked. A lone
	// slot-profile row waits too: the shortcut launches the row as a model,
	// and a profile row's model is only its id.
	if len(tbl.items) == 1 && tbl.items[0].blocked == "" && !tbl.items[0].start && tbl.items[0].profile == nil {
```

- [ ] **Step 27: Run the tests and see them pass**

Run: `cd wt && go test ./internal/tui -run 'TestProfileRowScreenFitsAndShowsItsSlots|TestProfileSlotLineIsCutToWholeEntriesOnScreen|TestLoneProfileRowKeepsThePickerOpen|TestEveryListPhaseFitsTheTerminal|TestModelPickerNamesTheHighlightedModelsPrice'`

Expected: `ok  	github.com/ohanaverse/local-ai-setup/wt/internal/tui`. `TestModelPickerNamesTheHighlightedModelsPrice` is the existing test of the mode line for model rows; it passes unchanged.

To see the nine sizes of each new screen measured, run `cd wt && go test ./internal/tui -run TestEveryListPhaseFitsTheTerminal -v | grep 'profile row'`. Expected: 27 lines, such as:

```
    layout_test.go:703: model picker, profile row     80x24: 24 lines,  63 columns
    layout_test.go:703: model picker, blocked profile row  40x12: 12 lines,  40 columns
```

- [ ] **Step 28: Run the whole module's checks**

Run:

```bash
(cd wt && gofmt -l . && go build ./... && go vet ./... && go test ./...)
```

Expected: `gofmt -l .` prints nothing, `go build` and `go vet` print nothing, and every package line of `go test` starts with `ok`. `internal/tui` takes about 20 seconds.

- [ ] **Step 29: Commit**

```bash
git add wt/internal/tui/layout.go wt/internal/tui/app.go wt/internal/tui/slot_row_test.go wt/internal/tui/layout_test.go
git commit -m "feat(wt): show a highlighted slot profile's slots in place of the mode line"
```

### Task 6: Lifecycle seams

**Files:**
- Create: `wt/internal/lifecycle/profile_routes.go`
- Modify: `wt/internal/lifecycle/lifecycle.go` (the import block at lines 3-11; the `Options` struct at lines 41-53, with the new function `RestartStillOwed` placed right after it; `startWith` at lines 286-302)
- Test: `wt/internal/lifecycle/profile_routes_test.go`

**Interfaces:**
- Consumes (existing code only; nothing from another task):
  - `internal/lifecycle/routes.go`: the seams `applyRoutes = litellm.ApplyChange`, `restartProxy = litellm.RestartContext`, `waitProxy = litellm.WaitReady`, `probeProxy = litellm.Listening`, `routesWarn io.Writer`; `func withRouteOutput(ctx context.Context, w io.Writer) context.Context`; `func routePrintf(ctx context.Context, format string, args ...any)`; `var routeWG sync.WaitGroup`; `var routesInFlight atomic.Int32`; `func WaitPendingRoutes()`; `func RoutesPending() bool`; the constants `proxyReadyTimeout` (30s) and `ensureRouteLockTimeout` (10s); `func modelRouteChange(cfg *config.Config, m config.Model) (litellm.Change, bool)`; `type routeWrite struct { changed bool; rows []string; said bool; missing []string }`; `func reportEnsured(ctx context.Context, w routeWrite, id string)`; `func bounceRoutes(ctx context.Context, cfg *config.Config)`; `func bounceProxyAsync(ctx context.Context, cfg *config.Config)`; `func routeAfterStart(ctx context.Context, cfg *config.Config, t Target, restartOwed bool)`
  - `internal/lifecycle/lifecycle.go`: `type Options struct { AllowReplace bool; Progress func(Stage); Out io.Writer }`; `type OccupiedError struct{ Occupants []localmodels.Entry }`; `type OccupancyUnknownError struct{ ProviderID, Origin string }`; `func Start(ctx context.Context, cfg *config.Config, t Target, opts Options) error`; `func SettleRoutes(ctx context.Context, cfg *config.Config)`; the field `restartOwed bool` of `env` (`env.go:41`)
  - `internal/litellm`: `func ApplyChange(cfg *config.Config, ch Change, o Options) (Result, error)`; `type Options struct { NoRestart bool; Ctx context.Context; … }`; `type Result struct { Outcomes []Outcome; Changed bool; … }`; `type Outcome struct { ID, Action string; Err error; Written bool }`; `const ActionAPIBaseSet`; `var ErrMissing, ErrInvalid, ErrRegistryRedirected`; `func WaitReady(ctx context.Context, baseURL string, timeout time.Duration) error`
  - `internal/config`: `func (c *Config) LitellmBaseURL() string`; `func (c *Config) SetLitellmForTest(s LitellmState)`; `var ErrLitellmUnconfigured`; `func IndexModelByID(models []Model, id string) int`
  - Test helpers already in the package: `stubRoutes`, `wrote`, `routesCfg`, `cloudRoutesCfg`, `ctxKey` (`routes_test.go`); `realRoutes`, `routedIDs`, `swapBackend`, `wrapCfg`, `ollamaSrv`, `openaiSrv`, `staleSiblingYAML` (`wrappers_test.go`); `fakeBackend` (`lifecycle_test.go`)
- Produces (Tasks 8 and 9 call these through package-level seams of their own):
  - `var ErrNotRoutable = errors.New("wt has no LiteLLM route for this model")`
  - `type RouteEnsureError struct { ModelID string; Err error }` with `func (e *RouteEnsureError) Error() string` (`"LiteLLM route for <ModelID> not written: <Err>"`) and `func (e *RouteEnsureError) Unwrap() error`
  - `func EnsureModelRoutes(ctx context.Context, out io.Writer, cfg *config.Config, models []config.Model, owedIn bool) (owed bool, err error)`
  - `func RestartProxy(ctx context.Context, cfg *config.Config) error`
  - `func ProbeProxy(ctx context.Context, cfg *config.Config) error`
  - `const proxyProbeTimeout = 5 * time.Second` (unexported)
  - the field `RestartOwed bool` on `lifecycle.Options`
  - `func RestartStillOwed(err error) bool`

**What you need to know before you start.**

- `internal/lifecycle` writes LiteLLM routes into `config.yaml` and restarts the proxy. Every route write goes through the package variable `applyRoutes`, every restart through `restartProxy`, every readiness wait through `waitProxy`. Tests swap these variables.
- The package's `TestMain` (`mtplx_test.go:26`) makes `applyRoutes` fail with "applyRoutes not stubbed in this test". It does not stub `restartProxy` or `waitProxy`. So every test below starts with `stubRoutes(t, …)` (all seams faked) or `realRoutes(t, yaml)` (the real writer on a `config.yaml` in `t.TempDir()`, named through `WT_LITELLM_CONFIG`). No test here reaches the real config home or the real proxy.
- The existing restart is asynchronous: `bounceProxyAsync` starts a goroutine. A test calls `WaitPendingRoutes()` before it reads a restart counter.
- The existing single-model check (`EnsureModelRoute`) restarts the proxy itself and never fails a launch. This task adds calls that return errors and leave the restart to the caller, so that a profile launch restarts at most once.
- Run every command from the repository root. The new tests take under two seconds.

Three behaviours follow, each with its own commit: the multi-model route ensure (steps 1-5), the restart and the probe that report failure (steps 6-10), the restart-owed option on `Start` (steps 11-17).

#### Behaviour 1: a multi-model route ensure that returns errors

- [ ] **Step 1: Write the failing tests**

Create `wt/internal/lifecycle/profile_routes_test.go` with exactly this content. The first block holds the helpers the whole file uses. `fakeRestarter` is the fake restarter every restart count is read from.

```go
package lifecycle

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
)

// proxyURL is the [litellm] url the tests in this file configure. Nothing
// dials it: every test installs stubRoutes or realRoutes, which replace the
// readiness wait.
const proxyURL = "http://localhost:4000"

// fakeRestarter replaces the proxy restart for one test and counts its runs.
// Install it after stubRoutes or realRoutes: its cleanup then runs first,
// waits for any restart still in flight, and only then puts the seam back.
func fakeRestarter(t *testing.T) *atomic.Int32 {
	t.Helper()
	var n atomic.Int32
	old := restartProxy
	restartProxy = func(context.Context) []string {
		n.Add(1)
		return nil
	}
	t.Cleanup(func() {
		WaitPendingRoutes()
		restartProxy = old
	})
	return &n
}

// profileCfg is cloudRoutesCfg with a second openrouter model and the
// [litellm] url set, the registry a mixed slot profile needs.
func profileCfg() *config.Config {
	cfg := cloudRoutesCfg()
	cfg.Models = append(cfg.Models, config.Model{ID: "openrouter/y", ProviderID: "openrouter", ModelName: "y", Location: config.LocationCloud})
	cfg.SetLitellmForTest(config.LitellmState{URL: proxyURL, APIKey: "sk-test-1234"})
	return cfg
}

// modelsByID returns cfg's models with the given ids, in the order asked.
func modelsByID(t *testing.T, cfg *config.Config, ids ...string) []config.Model {
	t.Helper()
	var out []config.Model
	for _, id := range ids {
		i := config.IndexModelByID(cfg.Models, id)
		if i < 0 {
			t.Fatalf("fixture has no model %q", id)
		}
		out = append(out, cfg.Models[i])
	}
	return out
}

// sortedRoutedIDs is routedIDs in a stable order: the order of rows in
// config.yaml is the writer's business, not these tests'.
func sortedRoutedIDs(t *testing.T, path string) []string {
	t.Helper()
	ids := routedIDs(t, path)
	slices.Sort(ids)
	return ids
}

// TestEnsureModelRoutesWritesMissingCloudRoutesAndRestartsNothing pins the
// first half of "at most one proxy restart per profile launch": two missing
// cloud routes are written by two locked writes, the call reports a restart
// as owed, and it restarts nothing itself. If it restarted per write, a
// profile with four cloud slots would bounce the proxy four times, each
// bounce killing the one before.
func TestEnsureModelRoutesWritesMissingCloudRoutesAndRestartsNothing(t *testing.T) {
	path, _, warn := realRoutes(t, "model_list: []\n")
	restarts := fakeRestarter(t)
	cfg := profileCfg()

	owed, err := EnsureModelRoutes(context.Background(), nil, cfg, modelsByID(t, cfg, "openrouter/x", "openrouter/y"), false)
	WaitPendingRoutes()
	if err != nil || !owed {
		t.Fatalf("EnsureModelRoutes = (%v, %v), want (true, nil); output %q", owed, err, warn.String())
	}
	if got, want := sortedRoutedIDs(t, path), []string{"openrouter/x", "openrouter/y"}; !slices.Equal(got, want) {
		t.Fatalf("routed = %v, want %v", got, want)
	}
	if n := restarts.Load(); n != 0 {
		t.Fatalf("restarts = %d, want 0: the caller owes the one restart", n)
	}
	want := "wt: LiteLLM route for openrouter/x updated\nwt: LiteLLM route for openrouter/y updated\n"
	if got := warn.String(); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

// TestEnsureModelRoutesErrors pins every result the single-model check
// treats as a non-failure and a profile launch must stop on. A profile that
// launched anyway would start Claude Code with a slot the proxy answers
// "Invalid model name" for. Each case also pins that nothing is restarted
// when nothing is owed.
func TestEnsureModelRoutesErrors(t *testing.T) {
	rowErr := errors.New(`secret_ref "OPENROUTER_API_KEY" resolved empty`)
	lockErr := fmt.Errorf("waiting for /x/config.yaml.lock: %w", context.DeadlineExceeded)
	tests := []struct {
		name      string
		id        string
		res       litellm.Result
		err       error
		wantIs    error
		wantCalls int
	}{
		{name: "config.yaml missing", id: "openrouter/x", err: fmt.Errorf("%w: /x/config.yaml", litellm.ErrMissing), wantIs: litellm.ErrMissing, wantCalls: 1},
		{name: "registry redirected", id: "openrouter/x", err: fmt.Errorf("%w: set WT_LITELLM_CONFIG", litellm.ErrRegistryRedirected), wantIs: litellm.ErrRegistryRedirected, wantCalls: 1},
		{name: "config.yaml invalid", id: "openrouter/x", err: fmt.Errorf("%w: model_list is not a list", litellm.ErrInvalid), wantIs: litellm.ErrInvalid, wantCalls: 1},
		{name: "lock timeout", id: "openrouter/x", err: lockErr, wantIs: context.DeadlineExceeded, wantCalls: 1},
		{name: "the row's own write error", id: "openrouter/x", res: litellm.Result{Outcomes: []litellm.Outcome{{ID: "openrouter/x", Err: rowErr}}}, wantIs: rowErr, wantCalls: 1},
		{name: "the row's own write error, local model", id: "ollama/a:1", res: litellm.Result{Outcomes: []litellm.Outcome{{ID: "ollama/a:1", Err: rowErr}}}, wantIs: rowErr, wantCalls: 1},
		{name: "native model", id: "claude/sonnet", wantIs: ErrNotRoutable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			calls, warn := stubRoutes(t, tc.res, tc.err)
			restarts := fakeRestarter(t)
			cfg := profileCfg()

			owed, err := EnsureModelRoutes(context.Background(), nil, cfg, modelsByID(t, cfg, tc.id), false)
			WaitPendingRoutes()

			var re *RouteEnsureError
			if !errors.As(err, &re) {
				t.Fatalf("err = %v (%T), want a *RouteEnsureError", err, err)
			}
			if re.ModelID != tc.id {
				t.Errorf("ModelID = %q, want %q", re.ModelID, tc.id)
			}
			if !errors.Is(err, tc.wantIs) {
				t.Errorf("err = %v, want it to wrap %v", err, tc.wantIs)
			}
			cause := tc.err
			if cause == nil {
				cause = tc.wantIs
			}
			if want := "LiteLLM route for " + tc.id + " not written: " + cause.Error(); err.Error() != want {
				t.Errorf("err = %q, want %q", err, want)
			}
			if owed {
				t.Error("owed = true beside an error, want false")
			}
			if len(*calls) != tc.wantCalls {
				t.Errorf("writes = %d, want %d", len(*calls), tc.wantCalls)
			}
			if n := restarts.Load(); n != 0 {
				t.Errorf("restarts = %d, want 0: nothing was owed", n)
			}
			if warn.Len() != 0 {
				t.Errorf("output = %q, want none: the error is returned, not printed", warn.String())
			}
		})
	}
}

// TestEnsureModelRoutesCloudModelNotInTheRegistry pins the second way a
// model is not wt's to route: a cloud model the registry does not hold. It
// is refused before config.yaml is opened.
func TestEnsureModelRoutesCloudModelNotInTheRegistry(t *testing.T) {
	calls, _ := stubRoutes(t, litellm.Result{}, nil)
	cfg := profileCfg()
	gone := config.Model{ID: "openrouter/gone", ProviderID: "openrouter", ModelName: "gone", Location: config.LocationCloud}

	owed, err := EnsureModelRoutes(context.Background(), nil, cfg, []config.Model{gone}, false)
	if owed || !errors.Is(err, ErrNotRoutable) {
		t.Fatalf("EnsureModelRoutes = (%v, %v), want (false, ErrNotRoutable)", owed, err)
	}
	if want := "LiteLLM route for openrouter/gone not written: wt has no LiteLLM route for this model"; err.Error() != want {
		t.Fatalf("err = %q, want %q", err, want)
	}
	if len(*calls) != 0 {
		t.Fatalf("writes = %+v, want none", *calls)
	}
}

// TestEnsureModelRoutesMissingConfigAgainstTheRealWriter runs the
// missing-config.yaml case through litellm.ApplyChange: the single-model
// check stays silent there and launches, and a profile launch must not.
func TestEnsureModelRoutesMissingConfigAgainstTheRealWriter(t *testing.T) {
	path, _, _ := realRoutes(t, "model_list: []\n")
	restarts := fakeRestarter(t)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	cfg := profileCfg()

	owed, err := EnsureModelRoutes(context.Background(), nil, cfg, modelsByID(t, cfg, "openrouter/x"), false)
	WaitPendingRoutes()
	if owed || !errors.Is(err, litellm.ErrMissing) {
		t.Fatalf("EnsureModelRoutes = (%v, %v), want (false, an error wrapping litellm.ErrMissing)", owed, err)
	}
	if n := restarts.Load(); n != 0 {
		t.Fatalf("restarts = %d, want 0", n)
	}
}

// TestEnsureModelRoutesSettlesWhatIsOwedOnError pins the choice that a
// failed ensure restarts once, asynchronously, for what is already in
// config.yaml, and hands back owed == false. Otherwise a route written
// before the failure stays unpublished: the next single-model launch of
// that model finds its row in place, restarts nothing, and the proxy
// answers "Invalid model name".
func TestEnsureModelRoutesSettlesWhatIsOwedOnError(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name   string
		owedIn bool
		// results holds one scripted write result per model, in order.
		results []litellm.Result
		errs    []error
		ids     []string
	}{
		{name: "owed by an earlier call", owedIn: true, ids: []string{"openrouter/x"}, results: []litellm.Result{{}}, errs: []error{boom}},
		{name: "owed by an earlier write of this call", ids: []string{"openrouter/x", "openrouter/y"}, results: []litellm.Result{wrote("openrouter/x"), {}}, errs: []error{nil, boom}},
		{name: "the failing write itself changed the file", ids: []string{"openrouter/x"}, results: []litellm.Result{{Changed: true, Outcomes: []litellm.Outcome{{ID: "ollama/b:1", Action: litellm.ActionAPIBaseSet}, {ID: "openrouter/x", Err: boom}}}}, errs: []error{nil}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stubRoutes(t, litellm.Result{}, nil)
			restarts := fakeRestarter(t)
			n := 0
			applyRoutes = func(*config.Config, litellm.Change, litellm.Options) (litellm.Result, error) {
				i := n
				n++
				return tc.results[i], tc.errs[i]
			}
			cfg := profileCfg()

			var out bytes.Buffer
			owed, err := EnsureModelRoutes(context.Background(), &out, cfg, modelsByID(t, cfg, tc.ids...), tc.owedIn)
			WaitPendingRoutes()

			var re *RouteEnsureError
			if !errors.As(err, &re) || !errors.Is(err, boom) {
				t.Fatalf("err = %v, want a *RouteEnsureError wrapping boom", err)
			}
			if want := tc.ids[len(tc.ids)-1]; re.ModelID != want {
				t.Errorf("ModelID = %q, want %q", re.ModelID, want)
			}
			if owed {
				t.Error("owed = true, want false: the call settled the restart itself")
			}
			if got := restarts.Load(); got != 1 {
				t.Errorf("restarts = %d, want exactly 1", got)
			}
		})
	}
}

// TestEnsureModelRoutesWriteOptions pins how each write is made: restart
// deferred, the config.yaml lock wait bounded, the row built here (no
// DeferMissingAdd, which is for the picker's non-blocking check), one write
// per distinct id, a cloud change that leaves an existing row alone and a
// local change that does not. An unbounded lock wait would hang a launch
// behind another wt process for as long as that process holds the file.
func TestEnsureModelRoutesWriteOptions(t *testing.T) {
	stubRoutes(t, litellm.Result{}, nil)
	restarts := fakeRestarter(t)
	type write struct {
		id          string
		missingOnly bool
		opts        litellm.Options
		left        time.Duration
	}
	var writes []write
	applyRoutes = func(_ *config.Config, ch litellm.Change, o litellm.Options) (litellm.Result, error) {
		w := write{id: ch.Add[0].ID, missingOnly: ch.AddMissingOnly, opts: o}
		if o.Ctx != nil {
			if dl, ok := o.Ctx.Deadline(); ok {
				w.left = time.Until(dl)
			}
		}
		if len(ch.Add) != 1 || len(ch.Remove) != 0 || len(ch.RemoveFamilies) != 0 {
			t.Errorf("change = %+v, want one add and no removal", ch)
		}
		writes = append(writes, w)
		return litellm.Result{}, nil
	}
	cfg := profileCfg()

	owed, err := EnsureModelRoutes(context.Background(), nil, cfg, modelsByID(t, cfg, "openrouter/x", "mtplx/Y--Q35", "openrouter/x", "ollama/g:cloud"), true)
	WaitPendingRoutes()
	if err != nil || !owed {
		t.Fatalf("EnsureModelRoutes = (%v, %v), want (true, nil): owedIn is carried through writes that change nothing", owed, err)
	}
	if len(writes) != 3 {
		t.Fatalf("writes = %d, want 3: the repeated id is written once", len(writes))
	}
	wantIDs := []string{"openrouter/x", "mtplx/Y--Q35", "ollama/g:cloud"}
	wantMissingOnly := []bool{true, false, true}
	for i, w := range writes {
		if w.id != wantIDs[i] || w.missingOnly != wantMissingOnly[i] {
			t.Errorf("write %d = %s (AddMissingOnly %v), want %s (%v)", i, w.id, w.missingOnly, wantIDs[i], wantMissingOnly[i])
		}
		if !w.opts.NoRestart || w.opts.ForceRestart || w.opts.DeferMissingAdd {
			t.Errorf("write %d options = %+v, want NoRestart only", i, w.opts)
		}
		if w.left <= 0 || w.left > ensureRouteLockTimeout {
			t.Errorf("write %d lock wait = %v, want a deadline within %v", i, w.left, ensureRouteLockTimeout)
		}
	}
	if n := restarts.Load(); n != 0 {
		t.Errorf("restarts = %d, want 0 on success", n)
	}
}

// TestEnsureModelRoutesPrintsToTheCallersWriter pins where the lines go.
// The TUI runs the profile launch under the alt screen, where stderr is not
// visible, so it passes its own writer; a line that leaked to stderr there
// would be lost, and one that leaked from another operation would be
// reported as this launch's.
func TestEnsureModelRoutesPrintsToTheCallersWriter(t *testing.T) {
	_, warn := stubRoutes(t, litellm.Result{}, nil)
	results := []litellm.Result{
		wrote("openrouter/x"),
		{Changed: true, Outcomes: []litellm.Outcome{{ID: "openrouter/y", Action: "routed"}, {ID: "ollama/b:1", Action: litellm.ActionAPIBaseSet}}},
		{Changed: true, Outcomes: []litellm.Outcome{{ID: "ollama/g:cloud", Action: "routed"}}},
		{Outcomes: []litellm.Outcome{{ID: "ollama/a:1", Action: "routed"}}},
	}
	n := 0
	applyRoutes = func(*config.Config, litellm.Change, litellm.Options) (litellm.Result, error) {
		n++
		return results[n-1], nil
	}
	cfg := profileCfg()

	var out bytes.Buffer
	owed, err := EnsureModelRoutes(context.Background(), &out, cfg, modelsByID(t, cfg, "openrouter/x", "openrouter/y", "ollama/g:cloud", "ollama/a:1"), false)
	if err != nil || !owed {
		t.Fatalf("EnsureModelRoutes = (%v, %v), want (true, nil)", owed, err)
	}
	want := "wt: LiteLLM route for openrouter/x updated\n" + // its own row was written
		"wt: LiteLLM route for ollama/b:1: api_base set\n" + // another row repaired by the second write
		"wt: LiteLLM config.yaml updated\n" // the third write changed the file, but no row: an enforced setting
	if got := out.String(); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	if warn.Len() != 0 {
		t.Fatalf("stderr got %q, want nothing: the caller passed a writer", warn.String())
	}
}
```

- [ ] **Step 2: Run them and see them fail**

```bash
cd wt && go test ./internal/lifecycle -run 'TestEnsureModelRoutes'
```

Expected: the package does not compile. The output starts with

```
# github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle [github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle.test]
internal/lifecycle/profile_routes_test.go:84:15: undefined: EnsureModelRoutes
internal/lifecycle/profile_routes_test.go:123:55: undefined: ErrNotRoutable
```

and ends with `FAIL	github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle [build failed]`.

- [ ] **Step 3: Write the implementation**

Create `wt/internal/lifecycle/profile_routes.go` with exactly this content.

```go
package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
)

// This file holds the route calls of a slot-profile launch. The launch-time
// check in routes.go (EnsureModelRoute) is for one model, restarts the proxy
// itself and never fails a launch. A profile launch routes several models,
// restarts at most once, and must stop when a route cannot be written.

// ErrNotRoutable is the cause when modelRouteChange declines a model: a
// native model, a cloud model sync would not route, or a model whose
// location does not resolve.
var ErrNotRoutable = errors.New("wt has no LiteLLM route for this model")

// RouteEnsureError names the model whose route could not be ensured.
type RouteEnsureError struct {
	ModelID string
	Err     error
}

func (e *RouteEnsureError) Error() string {
	return fmt.Sprintf("LiteLLM route for %s not written: %v", e.ModelID, e.Err)
}

func (e *RouteEnsureError) Unwrap() error { return e.Err }

// EnsureModelRoutes writes the missing LiteLLM route of each model, one
// locked write per model, and never restarts the proxy on success. Models
// with the same id are written once. owedIn says a restart is already owed
// by an earlier write of the same launch. It returns owed: owedIn, or true
// when a write here changed config.yaml.
//
// The writes are one per model because cloud and local models need different
// changes (modelRouteChange): litellm.Change.AddMissingOnly is a flag on the
// whole change.
//
// It returns an error, as a *RouteEnsureError, for: a model
// modelRouteChange declines (ErrNotRoutable); any error of the write
// itself, which includes litellm.ErrMissing, litellm.ErrRegistryRedirected
// and a config.yaml lock that was not free within ensureRouteLockTimeout;
// and an Outcome.Err of the model's own row. On an error it settles what
// is owed itself: one asynchronous restart (bounceProxyAsync) when owedIn
// or an earlier write of this call changed the file, and it returns
// owed == false. A caller about to exit still owes WaitPendingRoutes.
//
// What it prints goes to out (nil: stderr) through routePrintf: the
// "wt: LiteLLM route for <id> updated" line for a row it wrote and the
// "api_base set" line of a repaired row.
func EnsureModelRoutes(ctx context.Context, out io.Writer, cfg *config.Config, models []config.Model, owedIn bool) (owed bool, err error) {
	ctx = withRouteOutput(ctx, out)
	owed = owedIn
	fail := func(id string, cause error) (bool, error) {
		if owed {
			// Routes written before this failure are in config.yaml. Publish
			// them, or the next launch of one of those models finds its row
			// in place, restarts nothing, and the proxy answers "Invalid
			// model name".
			bounceProxyAsync(ctx, cfg)
		}
		return false, &RouteEnsureError{ModelID: id, Err: cause}
	}
	seen := map[string]bool{}
	for _, m := range models {
		if seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		ch, ok := modelRouteChange(cfg, m)
		if !ok {
			return fail(m.ID, ErrNotRoutable)
		}
		lockCtx, cancel := context.WithTimeout(ctx, ensureRouteLockTimeout)
		res, werr := applyRoutes(cfg, ch, litellm.Options{NoRestart: true, Ctx: lockCtx})
		cancel()
		if werr != nil {
			return fail(m.ID, werr)
		}
		if res.Changed {
			owed = true
		}
		w := routeWrite{changed: res.Changed}
		var rowErr error
		for _, o := range res.Outcomes {
			switch {
			case o.Err != nil:
				// The change adds one row, so a rejected id is this model's.
				rowErr = o.Err
			case o.Action == litellm.ActionAPIBaseSet:
				w.said = true
				routePrintf(ctx, "wt: LiteLLM route for %s: api_base set\n", o.ID)
			case o.Written:
				w.rows = append(w.rows, o.ID)
			}
		}
		if rowErr != nil {
			return fail(m.ID, rowErr)
		}
		reportEnsured(ctx, w, ch.Add[0].ID)
	}
	return owed, nil
}
```

Three points about this code:

- It calls the `applyRoutes` seam directly, not `applyReported`. `applyReported` turns `litellm.ErrMissing` into success and prints an `Outcome.Err` instead of returning it. A single-model launch wants that; a profile launch must stop on both.
- `reportEnsured` is the existing function that prints `wt: LiteLLM route for <id> updated` when the model's own row was written. It also prints `wt: LiteLLM config.yaml updated` when a write changed the file some other way and no more specific line was printed. The spec does not mention that line; it is kept because the surrounding launch-time check prints it in the same case.
- The error path settles with `bounceProxyAsync`, not with a waited restart: the launch is already aborting, so nothing needs the result. The process still calls `WaitPendingRoutes()` before it exits, as every wt command does.

- [ ] **Step 4: Run the tests and see them pass**

```bash
cd wt && go test ./internal/lifecycle -run 'TestEnsureModelRoutes' -v 2>&1 | grep -E '^(--- |ok|FAIL)'
```

Expected (the time on the last line varies):

```
--- PASS: TestEnsureModelRoutesWritesMissingCloudRoutesAndRestartsNothing (0.00s)
--- PASS: TestEnsureModelRoutesErrors (0.00s)
--- PASS: TestEnsureModelRoutesCloudModelNotInTheRegistry (0.00s)
--- PASS: TestEnsureModelRoutesMissingConfigAgainstTheRealWriter (0.00s)
--- PASS: TestEnsureModelRoutesSettlesWhatIsOwedOnError (0.00s)
--- PASS: TestEnsureModelRoutesWriteOptions (0.00s)
--- PASS: TestEnsureModelRoutesPrintsToTheCallersWriter (0.00s)
ok  	github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle	0.120s
```

Then check formatting and vet:

```bash
cd wt && gofmt -l ./internal/lifecycle && go vet ./internal/lifecycle
```

Expected: no output.

- [ ] **Step 5: Commit**

```bash
git add wt/internal/lifecycle/profile_routes.go wt/internal/lifecycle/profile_routes_test.go
git commit -m "feat(wt): lifecycle.EnsureModelRoutes, a multi-model route ensure that returns errors"
```

#### Behaviour 2: a restart that reports failure, and a proxy probe that returns its result

- [ ] **Step 6: Write the failing tests**

In `wt/internal/lifecycle/profile_routes_test.go`, replace the whole import block with this one (it adds `net/http` and `net/http/httptest`):

```go
import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
)
```

Then append these tests to the end of the file. `TestProfileRoutesRestartCounts` makes the route calls in the order a profile launch makes them and pins two of the three restart counts: zero when every route exists, exactly one for several missing cloud routes.

```go
// TestRestartProxy pins the restart a profile launch waits for and can fail
// on. The asynchronous restart prints a failed command or a readiness
// timeout as a warning and the launch goes on; here both are returned, so
// the agent never starts against a proxy that does not know its routes.
func TestRestartProxy(t *testing.T) {
	notReady := errors.New("LiteLLM proxy not ready at http://localhost:4000/health/liveliness: context deadline exceeded")
	tests := []struct {
		name      string
		warnings  []string
		waitErr   error
		wantErr   string
		wantWaits int
	}{
		{name: "restarted and ready", wantWaits: 1},
		{name: "restart command failed", warnings: []string{"failed to restart LiteLLM proxy (exit status 1); restart it manually: launchctl kickstart"}, wantErr: "failed to restart LiteLLM proxy (exit status 1); restart it manually: launchctl kickstart"},
		{name: "readiness timeout", waitErr: notReady, wantErr: notReady.Error(), wantWaits: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, warn := stubRoutes(t, litellm.Result{}, nil)
			var restartCtx context.Context
			var waits []string
			pendingDuringWait := false
			restartProxy = func(ctx context.Context) []string {
				restartCtx = ctx
				return tc.warnings
			}
			waitProxy = func(_ context.Context, url string, d time.Duration) error {
				waits = append(waits, fmt.Sprintf("%s %s", url, d))
				pendingDuringWait = RoutesPending()
				return tc.waitErr
			}
			// Nothing was listening before: the asynchronous restart skips
			// the readiness wait then. This call must not.
			probeProxy = func(context.Context, string, time.Duration) bool { return false }
			cfg := profileCfg()

			ctx, cancel := context.WithCancel(context.WithValue(context.Background(), ctxKey{}, "sentinel"))
			cancel() // a caller that was cancelled cannot call the restart off
			err := RestartProxy(ctx, cfg)

			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("RestartProxy = %v, want nil", err)
			case tc.wantErr != "" && (err == nil || err.Error() != tc.wantErr):
				t.Fatalf("RestartProxy = %v, want %q", err, tc.wantErr)
			}
			if restartCtx == nil {
				t.Fatal("the restart never ran")
			}
			if restartCtx.Err() != nil || restartCtx.Value(ctxKey{}) != "sentinel" {
				t.Errorf("restart ctx: err %v value %v, want the caller's values without its cancellation", restartCtx.Err(), restartCtx.Value(ctxKey{}))
			}
			if len(waits) != tc.wantWaits {
				t.Fatalf("readiness waits = %v, want %d", waits, tc.wantWaits)
			}
			if tc.wantWaits == 1 {
				if want := proxyURL + " " + proxyReadyTimeout.String(); waits[0] != want {
					t.Errorf("readiness wait = %q, want %q", waits[0], want)
				}
				if !pendingDuringWait {
					t.Error("RoutesPending() = false during the restart: a process exit would not wait for it")
				}
			}
			if RoutesPending() {
				t.Error("RoutesPending() = true after RestartProxy returned")
			}
			if warn.Len() != 0 {
				t.Errorf("output = %q, want none: the failure is returned, not printed", warn.String())
			}
		})
	}
}

// TestRestartProxyWaitsForARestartInFlight pins that two restarts never
// overlap: a restart that started while another was running would kill the
// proxy the first had just brought up.
func TestRestartProxyWaitsForARestartInFlight(t *testing.T) {
	stubRoutes(t, litellm.Result{}, nil)
	release := make(chan struct{})
	var running, overlapped atomic.Int32
	var count atomic.Int32
	restartProxy = func(context.Context) []string {
		if running.Add(1) > 1 {
			overlapped.Add(1)
		}
		if count.Add(1) == 1 {
			<-release // the first restart is still running
		}
		running.Add(-1)
		return nil
	}
	cfg := profileCfg()

	bounceRoutes(context.Background(), cfg) // an earlier operation's restart, in flight
	done := make(chan error, 1)
	go func() { done <- RestartProxy(context.Background(), cfg) }()

	select {
	case err := <-done:
		t.Fatalf("RestartProxy returned (%v) while another restart was in flight", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("RestartProxy = %v", err)
	}
	if count.Load() != 2 || overlapped.Load() != 0 {
		t.Fatalf("restarts = %d, overlapping = %d, want 2 in sequence", count.Load(), overlapped.Load())
	}
}

// TestRestartProxyWithoutAURL pins the unconfigured case: with no [litellm]
// url there is no address to wait for, so nothing is restarted and the
// error says why.
func TestRestartProxyWithoutAURL(t *testing.T) {
	stubRoutes(t, litellm.Result{}, nil)
	restarts := fakeRestarter(t)
	err := RestartProxy(context.Background(), routesCfg())
	if !errors.Is(err, config.ErrLitellmUnconfigured) {
		t.Fatalf("RestartProxy = %v, want an error wrapping config.ErrLitellmUnconfigured", err)
	}
	if n := restarts.Load(); n != 0 {
		t.Fatalf("restarts = %d, want 0", n)
	}
}

// TestProbeProxy pins the probe every profile launch makes before it starts
// the agent, including a launch that restarted nothing. It needs a 200:
// litellm.Listening counts a timeout as an answer, and a launch into a
// proxy that does not answer fails on the agent's first request instead of
// here, with the reason.
func TestProbeProxy(t *testing.T) {
	down := errors.New("LiteLLM proxy not ready at http://localhost:4000/health/liveliness: context deadline exceeded")
	tests := []struct {
		name    string
		url     string
		waitErr error
		wantErr string
		wantIs  error
		waits   int
	}{
		{name: "answers", url: proxyURL, waits: 1},
		{name: "a trailing slash is trimmed", url: proxyURL + "/", waits: 1},
		{name: "does not answer", url: proxyURL, waitErr: down, wantErr: "the LiteLLM proxy does not answer at http://localhost:4000: " + down.Error(), wantIs: down, waits: 1},
		{name: "no url", url: "", wantErr: "the LiteLLM proxy cannot be probed: " + config.ErrLitellmUnconfigured.Error(), wantIs: config.ErrLitellmUnconfigured},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stubRoutes(t, litellm.Result{}, nil)
			restarts := fakeRestarter(t)
			var waits []string
			waitProxy = func(_ context.Context, url string, d time.Duration) error {
				waits = append(waits, fmt.Sprintf("%s %s", url, d))
				return tc.waitErr
			}
			cfg := routesCfg()
			cfg.SetLitellmForTest(config.LitellmState{URL: tc.url, APIKey: "sk-test-1234"})

			err := ProbeProxy(context.Background(), cfg)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("ProbeProxy = %v, want nil", err)
			case tc.wantErr != "" && (err == nil || err.Error() != tc.wantErr):
				t.Fatalf("ProbeProxy = %v, want %q", err, tc.wantErr)
			}
			if tc.wantIs != nil && !errors.Is(err, tc.wantIs) {
				t.Errorf("err = %v, want it to wrap %v", err, tc.wantIs)
			}
			if len(waits) != tc.waits {
				t.Fatalf("waits = %v, want %d", waits, tc.waits)
			}
			if tc.waits == 1 {
				if want := proxyURL + " " + proxyProbeTimeout.String(); waits[0] != want {
					t.Errorf("wait = %q, want %q", waits[0], want)
				}
			}
			if n := restarts.Load(); n != 0 {
				t.Errorf("restarts = %d, want 0: a probe restarts nothing", n)
			}
		})
	}
}

// TestProbeProxyAgainstARealListener runs the probe through the real
// litellm.WaitReady and a local test server, so the seam's stub is not the
// only thing that ever answers it: a 200 on /health/liveliness is "there".
func TestProbeProxyAgainstARealListener(t *testing.T) {
	stubRoutes(t, litellm.Result{}, nil)
	waitProxy = litellm.WaitReady
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	cfg := routesCfg()
	cfg.SetLitellmForTest(config.LitellmState{URL: srv.URL, APIKey: "sk-test-1234"})

	if err := ProbeProxy(context.Background(), cfg); err != nil {
		t.Fatalf("ProbeProxy = %v, want nil", err)
	}
	if !slices.Equal(paths, []string{"/health/liveliness"}) {
		t.Fatalf("requests = %v, want one to /health/liveliness", paths)
	}
}

// TestProfileRoutesRestartCounts pins the restart budget of a profile
// launch against the real config.yaml writer, with the route calls made in
// the order the launch makes them: the cloud slots' routes, then the local
// model's when it is already running, then one restart if one is owed.
// Zero restarts when every route exists, so launching the same profile twice
// does not drop the first session's in-flight requests; exactly one when
// several are missing, so the routes are live before the agent starts.
func TestProfileRoutesRestartCounts(t *testing.T) {
	tests := []struct {
		name  string
		cloud []string
		local []string
	}{
		{name: "several missing cloud routes", cloud: []string{"openrouter/x", "openrouter/y", "ollama/g:cloud"}},
		{name: "missing cloud routes and a running local model", cloud: []string{"openrouter/x", "openrouter/y"}, local: []string{"ollama/a:1"}},
		{name: "one slot model named twice", cloud: []string{"openrouter/x", "openrouter/x"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path, _, warn := realRoutes(t, "model_list: []\n")
			restarts := fakeRestarter(t)
			cfg := profileCfg()
			ctx := context.Background()

			launch := func() {
				t.Helper()
				owed, err := EnsureModelRoutes(ctx, nil, cfg, modelsByID(t, cfg, tc.cloud...), false)
				if err != nil {
					t.Fatalf("cloud routes: %v", err)
				}
				if len(tc.local) > 0 {
					if owed, err = EnsureModelRoutes(ctx, nil, cfg, modelsByID(t, cfg, tc.local...), owed); err != nil {
						t.Fatalf("local route: %v", err)
					}
				}
				if owed {
					if err := RestartProxy(ctx, cfg); err != nil {
						t.Fatalf("RestartProxy: %v", err)
					}
				}
			}

			launch()
			if n := restarts.Load(); n != 1 {
				t.Fatalf("first launch: restarts = %d, want exactly 1 (output %q)", n, warn.String())
			}
			want := slices.Compact(slices.Sorted(slices.Values(append(slices.Clone(tc.cloud), tc.local...))))
			if got := sortedRoutedIDs(t, path); !slices.Equal(got, want) {
				t.Fatalf("routed = %v, want %v", got, want)
			}

			warn.Reset()
			launch()
			if n := restarts.Load(); n != 1 {
				t.Fatalf("second launch: restarts = %d in all, want still 1: every route exists", n)
			}
			if warn.Len() != 0 {
				t.Fatalf("second launch printed %q, want nothing", warn.String())
			}
		})
	}
}
```

- [ ] **Step 7: Run them and see them fail**

```bash
cd wt && go test ./internal/lifecycle -run 'TestRestartProxy|TestProbeProxy|TestProfileRoutesRestartCounts'
```

Expected: the package does not compile. The output holds these lines and ends with `[build failed]`:

```
internal/lifecycle/profile_routes_test.go:395:11: undefined: RestartProxy
internal/lifecycle/profile_routes_test.go:515:11: undefined: ProbeProxy
internal/lifecycle/profile_routes_test.go:529:33: undefined: proxyProbeTimeout
```

- [ ] **Step 8: Write the implementation**

In `wt/internal/lifecycle/profile_routes.go`, replace the whole import block with this one (it adds `strings` and `time`):

```go
import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
)
```

Then append this to the end of the file:

```go
// proxyProbeTimeout bounds ProbeProxy.
const proxyProbeTimeout = 5 * time.Second

// RestartProxy restarts the LiteLLM proxy and waits for it to answer,
// returning what bounceProxyAsync only prints: the restart command's
// failure, or the readiness timeout. It first waits for any restart
// already in flight (WaitPendingRoutes), counts itself in routeWG and
// routesInFlight while it runs, and always waits for readiness
// (waitProxy, proxyReadyTimeout). ctx gives it its values only; its
// cancellation is dropped (context.WithoutCancel), because a restart
// cannot be called off.
//
// An empty [litellm] url is an error wrapping config.ErrLitellmUnconfigured,
// returned before anything is restarted: there is no address to wait for.
func RestartProxy(ctx context.Context, cfg *config.Config) error {
	url := cfg.LitellmBaseURL()
	if url == "" {
		return fmt.Errorf("the LiteLLM proxy was not restarted: %w", config.ErrLitellmUnconfigured)
	}
	WaitPendingRoutes()
	routeWG.Add(1)
	routesInFlight.Add(1)
	defer routeWG.Done()
	defer routesInFlight.Add(-1)
	bg := context.WithoutCancel(ctx)
	if warnings := restartProxy(bg); len(warnings) > 0 {
		return errors.New(strings.Join(warnings, "; "))
	}
	return waitProxy(bg, url, proxyReadyTimeout)
}

// ProbeProxy checks that the proxy at cfg's [litellm] url answers 200 on
// its liveness endpoint within proxyProbeTimeout (one waitProxy call).
// Error: fmt.Errorf("the LiteLLM proxy does not answer at %s: %w", url, err).
// An empty url is an error wrapping config.ErrLitellmUnconfigured.
func ProbeProxy(ctx context.Context, cfg *config.Config) error {
	url := cfg.LitellmBaseURL()
	if url == "" {
		return fmt.Errorf("the LiteLLM proxy cannot be probed: %w", config.ErrLitellmUnconfigured)
	}
	if err := waitProxy(ctx, url, proxyProbeTimeout); err != nil {
		return fmt.Errorf("the LiteLLM proxy does not answer at %s: %w", url, err)
	}
	return nil
}
```

Three points about this code:

- `RestartProxy` adds itself to `routeWG` and `routesInFlight`. That makes `WaitPendingRoutes()` wait for it and `RoutesPending()` report it, so a process that is exiting (ctrl+c in the picker) does not leave while the restart runs.
- `ProbeProxy` uses `waitProxy` (`litellm.WaitReady`), which needs a 200 from `/health/liveliness`. The package's other probe, `probeProxy` (`litellm.Listening`), counts a timeout as an answer and would let a launch through to a proxy that is not serving.
- The spec does not say what `RestartProxy` does with an empty `[litellm]` url. It returns an error before restarting, as `ProbeProxy` does: there is no address to wait for. A slot profile is blocked earlier when the url is empty, so a launch does not reach this.

- [ ] **Step 9: Run the tests and see them pass**

```bash
cd wt && go test ./internal/lifecycle -run 'TestRestartProxy|TestProbeProxy|TestProfileRoutesRestartCounts' -v 2>&1 | grep -E '^(--- |ok|FAIL)'
```

Expected (the time on the last line varies):

```
--- PASS: TestRestartProxy (0.00s)
--- PASS: TestRestartProxyWaitsForARestartInFlight (0.10s)
--- PASS: TestRestartProxyWithoutAURL (0.00s)
--- PASS: TestProbeProxy (0.00s)
--- PASS: TestProbeProxyAgainstARealListener (0.00s)
--- PASS: TestProfileRoutesRestartCounts (0.00s)
ok  	github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle	0.221s
```

Then:

```bash
cd wt && gofmt -l ./internal/lifecycle && go vet ./internal/lifecycle
```

Expected: no output.

- [ ] **Step 10: Commit**

```bash
git add wt/internal/lifecycle/profile_routes.go wt/internal/lifecycle/profile_routes_test.go
git commit -m "feat(wt): lifecycle.RestartProxy and ProbeProxy return what the async restart only prints"
```

#### Behaviour 3: the restart-owed option on `lifecycle.Start`

The spec says a new `Options` field "seeds the internal `restartOwed` state". Seeding `env.restartOwed` directly would restart the proxy when a start is refused because its server is occupied, and again after the user confirms the replace: two restarts in one launch. So `startWith` reads the field instead, and a refusal that touched nothing leaves the restart with the caller. `RestartStillOwed` tells the caller which case it got:

| `Start`, called with `RestartOwed: true`, returned | The owed restart |
|---|---|
| `nil` | Settled by the start's own restart |
| an error with `RestartStillOwed(err) == true` | Not settled. The caller retries with `RestartOwed: true`, or calls `SettleRoutes` when it gives up |
| any other error | Settled: `Start` started one restart on its way out |

- [ ] **Step 11: Write the failing tests**

Append these tests to the end of `wt/internal/lifecycle/profile_routes_test.go`. The import block does not change. `TestIdleLocalModelPlusMissingCloudRouteRestartsOnce` pins the third restart count: exactly one for an idle local model plus a missing cloud route. `TestStartRefusedWhileRestartOwed` is the test for the plan's Review Focus 4.

```go
// TestRestartStillOwed pins which Start errors leave the caller's owed
// restart unsettled: only the two refusals that touch nothing. A caller
// that read a failed load as "still owed" would restart a second time; one
// that read a refusal as "settled" would never publish the route it wrote.
func TestRestartStillOwed(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"occupied", &OccupiedError{}, true},
		{"occupied, wrapped", fmt.Errorf("start: %w", &OccupiedError{}), true},
		{"occupancy unknown", &OccupancyUnknownError{ProviderID: "mtplx"}, true},
		{"occupancy unknown, wrapped", fmt.Errorf("start: %w", &OccupancyUnknownError{}), true},
		{"a failed load", errors.New("out of memory"), false},
		{"daemon down", &DaemonDownError{Provider: "ollama"}, false},
		{"cancelled", context.Canceled, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := RestartStillOwed(tc.err); got != tc.want {
				t.Errorf("RestartStillOwed(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestIdleLocalModelPlusMissingCloudRouteRestartsOnce pins the mixed
// profile: a cloud route is written first (restart owed), then the idle
// local model is started with Options.RestartOwed. The start's own restart
// publishes the cloud route too, so the launch costs exactly one restart.
// The second case is the one RestartOwed exists for: the local model's
// route is already in config.yaml, so the start's write changes nothing and
// without the option the proxy would never learn the cloud route — the
// session's cloud slot would answer "Invalid model name".
func TestIdleLocalModelPlusMissingCloudRouteRestartsOnce(t *testing.T) {
	for _, localRouted := range []bool{false, true} {
		t.Run(fmt.Sprintf("local route already written=%v", localRouted), func(t *testing.T) {
			path, _, warn := realRoutes(t, "model_list: []\n")
			restarts := fakeRestarter(t)
			var calls []string
			swapBackend(t, "ollama", &fakeBackend{calls: &calls})
			// a:1 is pulled and not loaded: an idle local model.
			cfg := wrapCfg(t, ollamaSrv(t, []string{"a:1"}, nil), openaiSrv(t, nil))
			cfg.Providers = append(cfg.Providers, config.Provider{ID: "openrouter", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "api_key", SecretRef: "sk-test", BaseURL: "https://openrouter.ai/api/v1"}})
			cfg.Models = append(cfg.Models, config.Model{ID: "openrouter/x", ProviderID: "openrouter", ModelName: "x", Location: config.LocationCloud})
			cfg.SetLitellmForTest(config.LitellmState{URL: proxyURL, APIKey: "sk-test-1234"})
			ctx := context.Background()
			local := Target{ProviderID: "ollama", ModelName: "a:1", ModelID: "ollama/a:1"}

			if localRouted {
				if _, err := EnsureModelRoutes(ctx, nil, cfg, modelsByID(t, cfg, "ollama/a:1"), false); err != nil {
					t.Fatalf("seeding the local route: %v", err)
				}
				warn.Reset()
			}

			owed, err := EnsureModelRoutes(ctx, nil, cfg, modelsByID(t, cfg, "openrouter/x"), false)
			if err != nil || !owed {
				t.Fatalf("cloud route = (%v, %v), want (true, nil)", owed, err)
			}
			if err := Start(ctx, cfg, local, Options{RestartOwed: owed}); err != nil {
				t.Fatalf("Start: %v", err)
			}
			WaitPendingRoutes()

			if !slices.Equal(calls, []string{"start:a:1"}) {
				t.Fatalf("backend calls = %v, want one start", calls)
			}
			if got, want := sortedRoutedIDs(t, path), []string{"ollama/a:1", "openrouter/x"}; !slices.Equal(got, want) {
				t.Fatalf("routed = %v, want %v (output %q)", got, want, warn.String())
			}
			if n := restarts.Load(); n != 1 {
				t.Fatalf("restarts = %d, want exactly 1", n)
			}
		})
	}
}

// TestStartRefusedWhileRestartOwed pins Review Focus 4. A profile wrote a
// cloud route (restart owed) and its local model's server is occupied, so
// the start is refused with *OccupiedError. The refusal must not restart
// the proxy: the user is about to be asked whether to replace, and the
// confirmed retry restarts once for everything. Restarting at the refusal
// too would bounce the proxy twice in one launch. A user who declines must
// still get the one restart (SettleRoutes), or the route written for them
// stays invisible to the proxy and the next launch of that cloud model,
// which finds its row in place, gets "Invalid model name".
func TestStartRefusedWhileRestartOwed(t *testing.T) {
	// refused sets the scene up to the refusal and returns what the two
	// endings need.
	refused := func(t *testing.T) (path string, restarts *atomic.Int32, cfg *config.Config, target Target, calls *[]string) {
		t.Helper()
		path, _, _ = realRoutes(t, staleSiblingYAML) // routes mtplx/Y--Q27, the occupant
		restarts = fakeRestarter(t)
		calls = new([]string)
		swapBackend(t, "mtplx", &fakeBackend{single: true, calls: calls})
		cfg = wrapCfg(t, ollamaSrv(t, nil, nil), openaiSrv(t, []string{"Y/Q27"}))
		cfg.Providers = append(cfg.Providers, config.Provider{ID: "openrouter", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "api_key", SecretRef: "sk-test", BaseURL: "https://openrouter.ai/api/v1"}})
		cfg.Models = append(cfg.Models, config.Model{ID: "openrouter/x", ProviderID: "openrouter", ModelName: "x", Location: config.LocationCloud})
		cfg.SetLitellmForTest(config.LitellmState{URL: proxyURL, APIKey: "sk-test-1234"})
		target = Target{ProviderID: "mtplx", ModelName: "Y/Q35", ModelID: "mtplx/Y--Q35"}

		owed, err := EnsureModelRoutes(context.Background(), nil, cfg, modelsByID(t, cfg, "openrouter/x"), false)
		if err != nil || !owed {
			t.Fatalf("cloud route = (%v, %v), want (true, nil)", owed, err)
		}
		err = Start(context.Background(), cfg, target, Options{RestartOwed: true})
		WaitPendingRoutes()
		var occupied *OccupiedError
		if !errors.As(err, &occupied) {
			t.Fatalf("Start = %v, want *OccupiedError", err)
		}
		if !RestartStillOwed(err) {
			t.Fatal("RestartStillOwed = false after a refusal that touched nothing")
		}
		if len(*calls) != 0 {
			t.Fatalf("backend calls = %v, want none at the refusal", *calls)
		}
		if n := restarts.Load(); n != 0 {
			t.Fatalf("restarts = %d at the refusal, want 0", n)
		}
		return path, restarts, cfg, target, calls
	}

	t.Run("confirmed retry restarts once in total", func(t *testing.T) {
		path, restarts, cfg, target, calls := refused(t)
		if err := Start(context.Background(), cfg, target, Options{AllowReplace: true, RestartOwed: true}); err != nil {
			t.Fatalf("retry: %v", err)
		}
		WaitPendingRoutes()
		if !slices.Equal(*calls, []string{"stop", "start:Y/Q35"}) {
			t.Fatalf("backend calls = %v, want the occupant stopped, then the start", *calls)
		}
		if got, want := sortedRoutedIDs(t, path), []string{"mtplx/Y--Q35", "openrouter/x"}; !slices.Equal(got, want) {
			t.Fatalf("routed = %v, want %v", got, want)
		}
		if n := restarts.Load(); n != 1 {
			t.Fatalf("restarts = %d, want exactly 1 for the whole launch", n)
		}
	})

	t.Run("giving up settles with one restart", func(t *testing.T) {
		path, restarts, cfg, _, calls := refused(t)
		SettleRoutes(context.Background(), cfg)
		WaitPendingRoutes()
		if len(*calls) != 0 {
			t.Fatalf("backend calls = %v, want none: the user declined", *calls)
		}
		if got, want := sortedRoutedIDs(t, path), []string{"mtplx/Y--Q27", "openrouter/x"}; !slices.Equal(got, want) {
			t.Fatalf("routed = %v, want %v: the written cloud route beside the occupant's", got, want)
		}
		if n := restarts.Load(); n != 1 {
			t.Fatalf("restarts = %d, want exactly 1, so the written route is live", n)
		}
	})
}

// TestStartFailureSettlesAnOwedRestart pins the other error branch: a start
// that got past the refusals and then failed has, per the caller's contract,
// settled the owed restart. The cloud route the profile wrote is published
// even though the launch aborts, and the caller restarts nothing more.
func TestStartFailureSettlesAnOwedRestart(t *testing.T) {
	path, _, _ := realRoutes(t, "model_list: []\n")
	restarts := fakeRestarter(t)
	var calls []string
	swapBackend(t, "ollama", &fakeBackend{calls: &calls, startErr: errors.New("out of memory")})
	cfg := wrapCfg(t, ollamaSrv(t, []string{"a:1"}, nil), openaiSrv(t, nil))

	for _, owed := range []bool{false, true} {
		err := Start(context.Background(), cfg, Target{ProviderID: "ollama", ModelName: "a:1", ModelID: "ollama/a:1"}, Options{RestartOwed: owed})
		WaitPendingRoutes()
		if err == nil || RestartStillOwed(err) {
			t.Fatalf("RestartOwed=%v: Start = %v, want a failure that is not a refusal", owed, err)
		}
		want := int32(0)
		if owed {
			want = 1
		}
		if n := restarts.Load(); n != want {
			t.Fatalf("RestartOwed=%v: restarts = %d, want %d", owed, n, want)
		}
	}
	if got := routedIDs(t, path); len(got) != 0 {
		t.Fatalf("routed = %v, want none: the model never started", got)
	}
}
```

- [ ] **Step 12: Run them and see them fail**

```bash
cd wt && go test ./internal/lifecycle -run 'TestRestartStillOwed|TestIdleLocalModel|TestStartRefused|TestStartFailureSettles'
```

Expected: the package does not compile. The output holds lines of these two kinds and ends with `[build failed]`:

```
undefined: RestartStillOwed
unknown field RestartOwed in struct literal of type Options
```

- [ ] **Step 13: Add the field and `RestartStillOwed`**

In `wt/internal/lifecycle/lifecycle.go`, add `"errors"` to the import block, so that it reads:

```go
import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)
```

In the same file, the `Options` struct ends with these three lines (lines 51-53):

```go
	// a start that succeeded).
	Out io.Writer
}
```

Replace them with:

```go
	// a start that succeeded).
	Out io.Writer
	// RestartOwed says config.yaml was already changed for this launch and
	// the proxy has not been restarted for it. Start's one restart then
	// publishes that change too: on success the route hook restarts even when
	// its own write changed nothing, and a start that fails after touching
	// anything restarts on its way out. A refusal that touched nothing
	// (RestartStillOwed) leaves the restart with the caller.
	RestartOwed bool
}

// RestartStillOwed reports whether a Start that was given RestartOwed and
// returned err left that restart unsettled: err is, or wraps, an
// *OccupiedError or an *OccupancyUnknownError, the two refusals that touch
// nothing. The caller then retries with RestartOwed set again, or calls
// SettleRoutes when it gives up. Every other outcome of such a Start, nil
// included, has started the one restart.
func RestartStillOwed(err error) bool {
	var occupied *OccupiedError
	var unknown *OccupancyUnknownError
	return errors.As(err, &occupied) || errors.As(err, &unknown)
}
```

- [ ] **Step 14: Run the tests and see the behaviour fail**

```bash
cd wt && go test ./internal/lifecycle -run 'TestRestartStillOwed|TestIdleLocalModel|TestStartRefused|TestStartFailureSettles'
```

Expected: the package compiles now. Two tests fail, because `Start` does not read the field yet (each message is prefixed with its line in `profile_routes_test.go`):

```
--- FAIL: TestIdleLocalModelPlusMissingCloudRouteRestartsOnce (0.00s)
    --- FAIL: TestIdleLocalModelPlusMissingCloudRouteRestartsOnce/local_route_already_written=true (0.00s)
        restarts = 0, want exactly 1
--- FAIL: TestStartFailureSettlesAnOwedRestart (0.00s)
    RestartOwed=true: restarts = 0, want 1
FAIL
```

`TestRestartStillOwed` and `TestStartRefusedWhileRestartOwed` pass already: nothing restarts at a refusal yet. The second one guards the next step. With the condition there written as `e.restartOwed || opts.RestartOwed`, the spec's literal wording, it fails with `restarts = 1 at the refusal, want 0`.

- [ ] **Step 15: Make `Start` read the field**

In `wt/internal/lifecycle/lifecycle.go`, `startWith` is:

```go
func startWith(ctx context.Context, e *env, cfg *config.Config, t Target, opts Options) error {
	ctx = withRouteOutput(ctx, opts.Out)
	if err := start(ctx, e, cfg, t, opts); err != nil {
		if e.restartOwed {
			// A stopped occupant's or an evicted model's route was already
			// dropped from config.yaml; the proxy still serves the old list
			// until it is bounced.
			bounceRoutes(ctx, cfg)
		}
		return err
	}
	if opts.Progress != nil {
		opts.Progress(StageRouting)
	}
	routeAfterStart(ctx, cfg, t, e.restartOwed)
	return nil
}
```

Replace the whole function with:

```go
func startWith(ctx context.Context, e *env, cfg *config.Config, t Target, opts Options) error {
	ctx = withRouteOutput(ctx, opts.Out)
	if err := start(ctx, e, cfg, t, opts); err != nil {
		if e.restartOwed || (opts.RestartOwed && !RestartStillOwed(err)) {
			// A stopped occupant's or an evicted model's route was already
			// dropped from config.yaml, or the caller wrote a route before
			// this start (Options.RestartOwed); the proxy still serves the
			// old list until it is bounced. A refusal that touched nothing
			// does not spend the caller's restart: the retry after a
			// confirmed replace would then restart a second time.
			bounceRoutes(ctx, cfg)
		}
		return err
	}
	if opts.Progress != nil {
		opts.Progress(StageRouting)
	}
	routeAfterStart(ctx, cfg, t, e.restartOwed || opts.RestartOwed)
	return nil
}
```

The last argument of `routeAfterStart` selects `restartForced`: the proxy restarts even when the start's own route write changed nothing. That is the case the option exists for. When the local model's route is already in `config.yaml`, the start changes nothing, and without the option the cloud route written before the start would never reach the proxy.

- [ ] **Step 16: Run the tests and see them pass**

```bash
cd wt && go test ./internal/lifecycle -run 'TestRestartStillOwed|TestIdleLocalModel|TestStartRefused|TestStartFailureSettles' -v 2>&1 | grep -E '^(--- |ok|FAIL)'
```

Expected (the time on the last line varies):

```
--- PASS: TestRestartStillOwed (0.00s)
--- PASS: TestIdleLocalModelPlusMissingCloudRouteRestartsOnce (0.00s)
--- PASS: TestStartRefusedWhileRestartOwed (0.00s)
--- PASS: TestStartFailureSettlesAnOwedRestart (0.00s)
ok  	github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle	0.127s
```

Then the whole package under the race detector:

```bash
cd wt && go test -race ./internal/lifecycle
```

Expected: one line, `ok  	github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle` and a time of about ten seconds.

Then the module-wide checks. `Options` gained a field whose zero value is today's behaviour, so every existing caller of `lifecycle.Start` (in `cmd/wt`, `internal/tui` and `internal/smoke`) compiles and behaves as before.

```bash
cd wt && gofmt -l . && go build ./... && go vet ./... && go test ./...
```

Expected: no line from `gofmt`, `go build` or `go vet`; then one `ok` line per package and no `FAIL`.

- [ ] **Step 17: Commit**

```bash
git add wt/internal/lifecycle/lifecycle.go wt/internal/lifecycle/profile_routes_test.go
git commit -m "feat(wt): lifecycle.Options.RestartOwed, so a start's one restart also publishes routes written before it"
```

**How later tasks use this.** Task 8 (`cmd/wt`) and Task 9 (`internal/tui`) call `EnsureModelRoutes`, `RestartProxy`, `ProbeProxy` and `SettleRoutes` through package-level seams of their own, and pass `Options.RestartOwed` to `lifecycle.Start`. The order is the one `TestProfileRoutesRestartCounts` and `TestIdleLocalModelPlusMissingCloudRouteRestartsOnce` use:

1. `EnsureModelRoutes` for the cloud slot models, with `owedIn` false.
2. Either `Start` with `RestartOwed: owed` (afterwards nothing is owed, unless `RestartStillOwed(err)`), or, for a local model that is already running, `EnsureModelRoutes` for it with `owedIn: owed`.
3. `RestartProxy`, only if a restart is still owed.
4. `ProbeProxy`, on every launch.

### Task 7: Bookkeeping

A slot-profile launch must move the picker's `> ` marker, count as a launch of the profile, and mark every model it uses as in use. It must not move the rotation position. This task adds the three pieces of state code that make that possible, and the rule for where the picker's cursor starts. It adds no caller of the profile path: Tasks 8 and 9 call `RecordProfile` and `RecordAll`.

One visible change ships with this task: every single-model launch now also writes `~/.config/agent-wt/last-picked.state`.

**Files:**
- Modify: `wt/internal/rotation/rotation.go` (package comment, the `Rotation` type comment, `RecordFor`; new `pickedPath`, `LastPicked`, `RecordProfile`)
- Modify: `wt/internal/refcount/refcount.go` (new `RecordAll`, placed between `Record` and `Sweep`)
- Modify: `wt/internal/tui/app.go` (`enterModelPhase`: the comment above `rot := rotation.New()` and the cursor block that ends at `m.models.Select(pos)`; `tableFor`: its doc comment and first line)
- Test: `wt/internal/rotation/rotation_test.go` (append)
- Test: `wt/internal/refcount/refcount_test.go` (append)
- Create: `wt/internal/tui/slot_cursor_test.go`

Line numbers in `wt/internal/tui/app.go` moved when Task 5 edited the file. Find each block by the text shown, not by number.

**Interfaces:**

- Consumes, existing code:
  - `func (r *Rotation) Last() (string, bool)` — the whole of `rotation.state`, trimmed; `("", false)` when missing or empty (`internal/rotation/rotation.go`)
  - `func (r *Rotation) NextFromEligible(eligible []config.Model, cfg *config.Config) (config.Model, bool)` — walks `cfg.Models` from the entry after `Last()`; unchanged
  - `func (r *Rotation) statePath() string` — `<dir>/rotation.state`
  - `func rotation.NewAt(dir string) *Rotation`, `func usage.NewStoreAt(dir string) *StoreImpl`, `func refcount.NewStoreAt(dir string) *StoreImpl`
  - `func (s *StoreImpl) CountsForAgent(agent string, modelIDs []string) map[string]UsageCounts` (`internal/usage`)
  - `func config.WriteFileAtomic(path string, data []byte, perm os.FileMode) error` — creates the parent directory, writes a temp file, renames it over `path`
  - `func (s *StoreImpl) withLock(fn func() error) error`, `type entry struct { Pid int; ModelID string }`, `Record`, `Release`, `Counts` (`internal/refcount/refcount.go`)
  - tui test helpers: `tempStateDir(t) string`, `stubUsageStore(t)`, `stubRefcountStore(t)`, `selectedModelID(m model) string`, `itemIDs(m model) []string`, `drainCmds(t, m, cmd) model`, `phaseModelWithList(t, cfg, agent, tag) model`, `testConfig() *config.Config`
- Consumes, Task 5:
  - `modelItem.profile *catalog.ProfileRow` — non-nil on a slot-profile row (`internal/tui/model_list.go`)
  - `model.slotChecks []profiles.SlotCheck` — the opt-in for profile rows (`internal/tui/app.go`)
  - test fixtures in `internal/tui/slot_row_test.go`: `func slotTestConfig() *config.Config` (registry order ends `openrouter/a`, `openrouter/b`; no native model) and `func slotCheckFor(index int, name, def string, slots map[string]string) profiles.SlotCheck`
- Produces:
  - `func (r *Rotation) LastPicked() (string, bool)` — the row id the marker sits on: `last-picked.state`, or `Last()` when that file is missing or empty
  - `func (r *Rotation) RecordProfile(agent, profileID string) error` — writes `last-picked.state`, appends the usage event, never touches `rotation.state`
  - `func (r *Rotation) RecordFor(agent, modelID string) error` — same signature; now writes `rotation.state`, then `last-picked.state`, then the usage event
  - `func (s *StoreImpl) RecordAll(pid int, modelIDs []string) error` — one refcount line per distinct, non-empty id, in one locked write; not on the `refcount.Store` interface
  - `tableFor`'s second return value is the last-picked row id
  - The starting-cursor rule in `enterModelPhase`
  - How Tasks 8 and 9 record a profile launch, in this order, after the applier succeeded and before the agent runs (`row` is the profile's `catalog.Row`; `ids` holds the `ID` of each `row.Profile.Distinct()` model):

    ```go
    rotation.New().RecordProfile(agent, row.Model.ID)
    refcount.NewStore().RecordAll(os.Getpid(), ids)
    ```

**Commands.** Run every command from the repository root. Each command block starts there: a `cd wt && …` line does not carry over to the next block, and the `git add` lines name paths from the root.

**Choices where the spec is silent:**

- A failed `last-picked.state` write does not skip the usage event. Both launch paths print the error as a note and launch anyway, so the launch is still counted. A failed `rotation.state` write returns early, as it does today.
- "The first actionable row in sort order" counts profile rows (contract, Task 7). With no rotation state and no native row, the cursor starts on the first profile that is not blocked.
- `phaseModelWithList` in `wt/internal/tui/agent_model_test.go` still reads `rotation.New().Last()`. It is left alone: every test that uses it seeds state with `RecordFor`, which writes the same id to both files.

---

#### Behaviour 1: `last-picked.state` in `internal/rotation`

- [ ] **Step 1: Write the failing tests**

Append to `wt/internal/rotation/rotation_test.go`. The file already imports `os`, `path/filepath`, `testing`, `time`, `config` and `usage`; no import changes. `claudeCfg` and `eligibleFor` are existing helpers in that file.

```go
// TestLastPicked verifies which id the picker's marker reads: the one in
// last-picked.state, and the one in rotation.state when that file is missing
// or empty. Without the fallback, a user upgrading wt would lose the marker
// until their next launch; without the preference, a slot-profile launch
// could never move the marker, since rotation.state never names a profile.
func TestLastPicked(t *testing.T) {
	missing := "\x00" // a file that is not written
	tests := []struct {
		name             string
		rotation, picked string
		want             string
		wantOK           bool
	}{
		{name: "no state at all", rotation: missing, picked: missing, want: "", wantOK: false},
		{name: "state from before last-picked.state existed", rotation: "ollama/a\n", picked: missing, want: "ollama/a", wantOK: true},
		{name: "a profile was launched after a model", rotation: "ollama/a\n", picked: "profile/mixed\n", want: "profile/mixed", wantOK: true},
		{name: "an empty last-picked.state falls back", rotation: "ollama/a\n", picked: " \n", want: "ollama/a", wantOK: true},
		{name: "a profile is the only launch so far", rotation: missing, picked: "profile/mixed\n", want: "profile/mixed", wantOK: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, content := range map[string]string{"rotation.state": tt.rotation, "last-picked.state": tt.picked} {
				if content == missing {
					continue
				}
				if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
					t.Fatalf("seed %s: %v", name, err)
				}
			}
			got, ok := NewAt(dir).LastPicked()
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("LastPicked = %q, %v; want %q, %v", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

// TestRecordForWritesLastPicked verifies a single-model launch writes both
// state files, each one line and mode 0600. If it wrote rotation.state alone,
// the picker's marker would stay on a slot profile launched earlier, pointing
// at a row the user did not launch last.
func TestRecordForWritesLastPicked(t *testing.T) {
	dir := t.TempDir()
	r := NewAt(dir)
	if err := r.RecordProfile("claude", "profile/mixed"); err != nil {
		t.Fatalf("RecordProfile: %v", err)
	}
	if err := r.RecordFor("claude", "ollama/a"); err != nil {
		t.Fatalf("RecordFor: %v", err)
	}
	for _, name := range []string{"rotation.state", "last-picked.state"} {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if string(data) != "ollama/a\n" {
			t.Errorf("%s = %q, want %q", name, data, "ollama/a\n")
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("%s perm = %o, want 0600", name, perm)
		}
	}
	if got, ok := r.LastPicked(); !ok || got != "ollama/a" {
		t.Errorf("LastPicked = %q, %v; want ollama/a, true", got, ok)
	}
}

// TestRecordProfileLeavesRotationStateAlone verifies a slot-profile launch
// moves the marker and counts as a launch of the profile, and leaves
// rotation.state byte for byte as it was. rotation.state is one bare registry
// id that an older wt binary reads whole; a profile id in it would make that
// binary, and rotation itself, treat the state as stale and restart rotation
// from the first model.
func TestRecordProfileLeavesRotationStateAlone(t *testing.T) {
	cfg := claudeCfg(
		config.Model{ID: "claude/sonnet", ProviderID: "claude"},
		config.Model{ID: "claude/opus", ProviderID: "claude"},
	)
	dir := t.TempDir()
	r := NewAt(dir)
	if err := r.RecordFor("claude", "claude/sonnet"); err != nil {
		t.Fatalf("RecordFor: %v", err)
	}
	before, err := os.ReadFile(r.statePath())
	if err != nil {
		t.Fatalf("read rotation.state: %v", err)
	}

	if err := r.RecordProfile("claude", "profile/mixed"); err != nil {
		t.Fatalf("RecordProfile: %v", err)
	}

	after, err := os.ReadFile(r.statePath())
	if err != nil {
		t.Fatalf("read rotation.state after the profile launch: %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("rotation.state = %q after a profile launch, want it unchanged (%q)", after, before)
	}
	if got, _ := r.Last(); got != "claude/sonnet" {
		t.Errorf("Last = %q, want claude/sonnet", got)
	}
	if got, ok := r.NextFromEligible(eligibleFor(t, cfg), cfg); !ok || got.ID != "claude/opus" {
		t.Errorf("NextFromEligible = %q, %v; want claude/opus, true (rotation did not move)", got.ID, ok)
	}
	if got, ok := r.LastPicked(); !ok || got != "profile/mixed" {
		t.Errorf("LastPicked = %q, %v; want profile/mixed, true", got, ok)
	}
	picked, err := os.ReadFile(r.pickedPath())
	if err != nil {
		t.Fatalf("read last-picked.state: %v", err)
	}
	if string(picked) != "profile/mixed\n" {
		t.Errorf("last-picked.state = %q, want %q", picked, "profile/mixed\n")
	}
	info, err := os.Stat(r.pickedPath())
	if err != nil {
		t.Fatalf("stat last-picked.state: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("last-picked.state perm = %o, want 0600", perm)
	}
	store := usage.NewStoreAt(dir)
	if got := store.CountsForAgent("claude", []string{"profile/mixed"})["profile/mixed"].ThirtyDay; got != 1 {
		t.Errorf("claude launches of profile/mixed = %d, want 1", got)
	}
	if got := store.CountsForAgent("claude", []string{"claude/sonnet"})["claude/sonnet"].ThirtyDay; got != 1 {
		t.Errorf("claude launches of claude/sonnet = %d, want 1 (the profile launch is not a launch of a model)", got)
	}
}

// TestRecordProfileDoesNotCreateRotationState verifies a slot-profile launch
// on a fresh install writes no rotation.state. A file created there would
// have to hold something, and anything but a registry id breaks the next
// single-model rotation.
func TestRecordProfileDoesNotCreateRotationState(t *testing.T) {
	dir := t.TempDir()
	r := NewAt(dir)
	if err := r.RecordProfile("claude", "profile/mixed"); err != nil {
		t.Fatalf("RecordProfile: %v", err)
	}
	if _, err := os.Stat(r.statePath()); !os.IsNotExist(err) {
		t.Errorf("rotation.state exists after a profile launch (stat err = %v), want it absent", err)
	}
	if got, ok := r.Last(); ok {
		t.Errorf("Last = %q, true; want no last-launched model", got)
	}
	if got, ok := r.LastPicked(); !ok || got != "profile/mixed" {
		t.Errorf("LastPicked = %q, %v; want profile/mixed, true", got, ok)
	}
}

// TestRecordKeepsUsageWhenLastPickedFails verifies a last-picked.state that
// cannot be written is reported and costs nothing else: rotation still
// advances and the launch is still counted. Both launch paths print the error
// as a note and launch anyway, so a launch dropped from usage.jsonl here
// would be missing from the picker's counts and from `wt stats`.
func TestRecordKeepsUsageWhenLastPickedFails(t *testing.T) {
	dir := t.TempDir()
	r := NewAt(dir)
	// A directory where the file belongs: the atomic rename onto it fails.
	if err := os.Mkdir(r.pickedPath(), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if err := r.RecordFor("claude", "ollama/a"); err == nil {
		t.Error("RecordFor = nil, want the last-picked.state write error")
	}
	if got, _ := r.Last(); got != "ollama/a" {
		t.Errorf("Last = %q, want ollama/a (rotation.state is written first)", got)
	}
	if err := r.RecordProfile("claude", "profile/mixed"); err == nil {
		t.Error("RecordProfile = nil, want the last-picked.state write error")
	}
	counts := usage.NewStoreAt(dir).CountsForAgent("claude", []string{"ollama/a", "profile/mixed"})
	for _, id := range []string{"ollama/a", "profile/mixed"} {
		if counts[id].ThirtyDay != 1 {
			t.Errorf("launches of %s = %d, want 1", id, counts[id].ThirtyDay)
		}
	}
}
```

- [ ] **Step 2: Run them and see them fail**

```bash
cd wt && go test ./internal/rotation -run 'TestLastPicked|TestRecordForWritesLastPicked|TestRecordProfile|TestRecordKeepsUsageWhenLastPickedFails'
```

Expected: the build fails, with lines like these.

```
internal/rotation/rotation_test.go:395:26: NewAt(dir).LastPicked undefined (type *Rotation has no field or method LastPicked)
internal/rotation/rotation_test.go:410:14: r.RecordProfile undefined (type *Rotation has no field or method RecordProfile)
internal/rotation/rotation_test.go:479:31: r.pickedPath undefined (type *Rotation has no field or method pickedPath)
FAIL	github.com/ohanaverse/local-ai-setup/wt/internal/rotation [build failed]
```

- [ ] **Step 3: Write the implementation**

In `wt/internal/rotation/rotation.go`, make three edits. No import changes.

Edit 1. Replace the package comment (the first two lines of the file) with:

```go
// Package rotation manages the global last-launched model, the row the
// picker's marker sits on, and computes the next model to present in the
// picker.
```

Edit 2. Replace the one-line comment above `type Rotation struct` (`// Rotation reads and writes the single global rotation.state file.`) with:

```go
// Rotation reads and writes two one-line files in the config directory:
// rotation.state, the registry id of the last single-model launch (the
// rotation position), and last-picked.state, the id of the last row launched
// from the picker, which may be a slot profile's (the picker's marker).
```

Edit 3. Replace the whole of `RecordFor`, comment included. This is the text to remove:

```go
// RecordFor writes modelID as the new last-launched model and records a usage
// event attributed to the agent.
func (r *Rotation) RecordFor(agent, modelID string) error {
	path := r.statePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := config.WriteFileAtomic(path, []byte(modelID+"\n"), 0o600); err != nil {
		return err
	}
	// Usage recording is best-effort; do not fail rotation write if usage fails.
	_ = r.store.RecordFor(agent, modelID)
	return nil
}
```

Put this in its place:

```go
// pickedPath is <dir>/last-picked.state.
func (r *Rotation) pickedPath() string {
	return filepath.Join(r.dir, "last-picked.state")
}

// LastPicked is the row id the picker's marker sits on: last-picked.state,
// or, when that file is missing or empty, Last(). The fallback keeps state
// written before last-picked.state existed working: there the last-launched
// model is also the last-picked row.
func (r *Rotation) LastPicked() (string, bool) {
	if data, err := os.ReadFile(r.pickedPath()); err == nil {
		if id := strings.TrimSpace(string(data)); id != "" {
			return id, true
		}
	}
	return r.Last()
}

// RecordFor writes modelID as the new last-launched model and as the
// last-picked row, and records a usage event attributed to the agent. Writing
// both files is what moves the picker's marker off a slot profile once a
// single model is launched after it.
func (r *Rotation) RecordFor(agent, modelID string) error {
	path := r.statePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := config.WriteFileAtomic(path, []byte(modelID+"\n"), 0o600); err != nil {
		return err
	}
	pickedErr := config.WriteFileAtomic(r.pickedPath(), []byte(modelID+"\n"), 0o600)
	// Usage recording is best-effort; do not fail rotation write if usage fails.
	// It runs whether or not last-picked.state was written: the launch goes
	// ahead either way, so it is counted either way.
	_ = r.store.RecordFor(agent, modelID)
	return pickedErr
}

// RecordProfile records a slot-profile launch: it writes profileID to
// last-picked.state and appends the usage event for agent and profileID
// (best-effort, as RecordFor's is). It never touches rotation.state, so the
// rotation position stays on the last single-model launch: rotation.state
// holds one bare registry id, and a profile id is not one.
func (r *Rotation) RecordProfile(agent, profileID string) error {
	pickedErr := config.WriteFileAtomic(r.pickedPath(), []byte(profileID+"\n"), 0o600)
	_ = r.store.RecordFor(agent, profileID)
	return pickedErr
}
```

`Last`, `NextFromEligible`, `migrate` and the format of `rotation.state` do not change. `migrate` looks only for files named `rotation-*.state`, so it never reads `last-picked.state`.

- [ ] **Step 4: Run the tests and see them pass**

```bash
cd wt && go test ./internal/rotation
```

Expected: `ok  	github.com/ohanaverse/local-ai-setup/wt/internal/rotation`. The older tests in the file pass unchanged.

- [ ] **Step 5: Commit**

```bash
cd wt && gofmt -l ./internal/rotation
```

Expected: no output.

```bash
git add wt/internal/rotation/rotation.go wt/internal/rotation/rotation_test.go
git commit -m "feat(wt): last-picked.state, the picker marker's own file beside rotation.state"
```

---

#### Behaviour 2: `RecordAll` in `internal/refcount`

- [ ] **Step 6: Write the failing tests**

Append to `wt/internal/refcount/refcount_test.go`. The file already imports `bufio`, `encoding/json`, `os`, `path/filepath`, `strings`, `sync` and `testing`; no import changes.

```go
// readEntries decodes every line of dir/refcount.jsonl, in file order. A
// missing file is no entries.
func readEntries(t *testing.T, dir string) []entry {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "refcount.jsonl"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read refcount file: %v", err)
	}
	var out []entry
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		var e entry
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			t.Fatalf("refcount line %q: %v", scanner.Text(), err)
		}
		out = append(out, e)
	}
	return out
}

// TestRecordAll verifies a slot-profile session is recorded as one line per
// distinct model it uses. A model that fills three slots must count as one
// session, not three: the picker's in-use column and the question `wt stop`
// asks before halting a server both count lines.
func TestRecordAll(t *testing.T) {
	tests := []struct {
		name string
		ids  []string
		want []string
	}{
		{name: "one line per model", ids: []string{"openrouter/a", "ollama/b:cloud", "omlx/c"}, want: []string{"openrouter/a", "ollama/b:cloud", "omlx/c"}},
		{name: "a model in several slots is recorded once", ids: []string{"omlx/c", "openrouter/a", "omlx/c", "omlx/c"}, want: []string{"omlx/c", "openrouter/a"}},
		{name: "an empty id is skipped", ids: []string{"", "openrouter/a"}, want: []string{"openrouter/a"}},
		{name: "nothing to record", ids: nil, want: nil},
		{name: "only empty ids", ids: []string{"", ""}, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			store := NewStoreAt(dir)
			if err := store.RecordAll(4242, tt.ids); err != nil {
				t.Fatalf("RecordAll: %v", err)
			}
			got := readEntries(t, dir)
			if len(got) != len(tt.want) {
				t.Fatalf("entries = %v, want one per id of %v", got, tt.want)
			}
			for i, e := range got {
				if e.Pid != 4242 || e.ModelID != tt.want[i] {
					t.Errorf("entry %d = %+v, want pid 4242 model %q", i, e, tt.want[i])
				}
			}
			if len(tt.want) == 0 {
				if _, err := os.Stat(filepath.Join(dir, "refcount.jsonl")); !os.IsNotExist(err) {
					t.Errorf("refcount.jsonl exists with nothing recorded (stat err = %v)", err)
				}
			}
		})
	}
}

// TestRecordAllKeepsOtherSessionsAndReleasesTogether verifies RecordAll
// appends beside another session's entry, each of its models then counts that
// session, and one Release drops every line of the pid. If a line outlived
// the session, the stop picker would never again offer that model.
func TestRecordAllKeepsOtherSessionsAndReleasesTogether(t *testing.T) {
	dir := t.TempDir()
	store := NewStoreAt(dir)
	if err := store.Record(111, "omlx/c"); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := store.RecordAll(222, []string{"openrouter/a", "omlx/c"}); err != nil {
		t.Fatalf("RecordAll: %v", err)
	}
	got := store.Counts([]string{"openrouter/a", "omlx/c"})
	if got["openrouter/a"] != 1 || got["omlx/c"] != 2 {
		t.Fatalf("Counts = %v, want openrouter/a:1 omlx/c:2", got)
	}

	if err := store.Release(222); err != nil {
		t.Fatalf("Release: %v", err)
	}
	got = store.Counts([]string{"openrouter/a", "omlx/c"})
	if got["openrouter/a"] != 0 || got["omlx/c"] != 1 {
		t.Fatalf("Counts after Release = %v, want openrouter/a:0 omlx/c:1 (the other session's line stays)", got)
	}
}
```

- [ ] **Step 7: Run them and see them fail**

```bash
cd wt && go test ./internal/refcount -run TestRecordAll
```

Expected: the build fails.

```
internal/refcount/refcount_test.go:315:20: store.RecordAll undefined (type *StoreImpl has no field or method RecordAll)
FAIL	github.com/ohanaverse/local-ai-setup/wt/internal/refcount [build failed]
```

- [ ] **Step 8: Write the implementation**

In `wt/internal/refcount/refcount.go`, insert this function after `Record` and before the comment that starts `// Sweep drops every entry`. No import changes. Do not add it to the `Store` interface: the tui test double `stubRefcountStore` and every other implementer would have to grow a method they never call.

```go
// RecordAll appends one live-session entry per distinct, non-empty id of
// modelIDs for pid, in one locked write. A slot-profile session uses several
// models at once, and each of them must read as in use for as long as the
// session lives; one write means another wt process never sees half of them.
// With nothing to record it writes nothing. It is not part of the Store
// interface: its callers hold the concrete type.
func (s *StoreImpl) RecordAll(pid int, modelIDs []string) error {
	var lines []byte
	seen := make(map[string]bool, len(modelIDs))
	for _, id := range modelIDs {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		line, err := json.Marshal(entry{Pid: pid, ModelID: id})
		if err != nil {
			return err
		}
		lines = append(lines, line...)
		lines = append(lines, '\n')
	}
	if len(lines) == 0 {
		return nil
	}
	return s.withLock(func() error {
		existing, err := os.ReadFile(s.path())
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		return config.WriteFileAtomic(s.path(), append(existing, lines...), 0o600)
	})
}
```

- [ ] **Step 9: Run the tests and see them pass**

```bash
cd wt && go test ./internal/refcount
```

Expected: `ok  	github.com/ohanaverse/local-ai-setup/wt/internal/refcount`.

- [ ] **Step 10: Commit**

```bash
cd wt && gofmt -l ./internal/refcount
```

Expected: no output.

```bash
git add wt/internal/refcount/refcount.go wt/internal/refcount/refcount_test.go
git commit -m "feat(wt): refcount.RecordAll, one in-use line per distinct model of a session"
```

---

#### Behaviour 3: the marker and the starting cursor in the picker

The rule, from the spec's "Marker and starting cursor":

1. The marker sits on the last-picked row. That may be a profile row.
2. When the last-picked row is a profile row that is present and not blocked, the cursor starts on it.
3. Otherwise the cursor follows today's rule, read from `rotation.state`: the registry model after the pointer among actionable model rows, else the first actionable row in sort order.

- [ ] **Step 11: Write the failing tests**

Create `wt/internal/tui/slot_cursor_test.go`:

```go
// Tests for the picker's marker and starting cursor once slot-profile rows
// exist: the marker reads last-picked.state, a usable profile picked last
// keeps the cursor, and every other case follows the rotation pointer.
package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rotation"
)

// slotCursorChecks is the two profiles these tests show: "broken" names a
// model the registry does not have, so its row is blocked; "ok" is an
// all-cloud launch row. File order is broken, then ok.
func slotCursorChecks() []profiles.SlotCheck {
	return []profiles.SlotCheck{
		slotCheckFor(0, "broken", "opus", map[string]string{"opus": "openrouter/missing"}),
		slotCheckFor(1, "ok", "opus", map[string]string{"opus": "openrouter/a"}),
	}
}

// slotCursorPicker enters the claude model phase on slotTestConfig with
// slotCursorChecks, after seed wrote the state files of earlier launches into
// the isolated state directory. It does not use slotPicker: that helper
// isolates the state directory itself, so nothing can be seeded before it.
func slotCursorPicker(t *testing.T, seed func(r *rotation.Rotation)) model {
	t.Helper()
	dir := tempStateDir(t)
	stubUsageStore(t)
	stubRefcountStore(t)
	if seed != nil {
		seed(rotation.NewAt(dir))
	}
	cfg := slotTestConfig()
	// The rotation cases below expect openrouter/b to follow openrouter/a in
	// the registry's order, which is the order rotation walks.
	if i := config.IndexModelByID(cfg.Models, "openrouter/a"); i < 0 || i+1 >= len(cfg.Models) || cfg.Models[i+1].ID != "openrouter/b" {
		t.Fatalf("fixture: slotTestConfig must list openrouter/b right after openrouter/a")
	}
	models, err := cfg.EligibleModels("claude", "", "")
	if err != nil {
		t.Fatalf("EligibleModels: %v", err)
	}
	m := model{cfg: cfg, agent: "claude", slotChecks: slotCursorChecks(), width: 80, height: 24}
	got, _ := m.enterModelPhase("claude", models, "code")
	if got.phase != phaseModel {
		t.Fatalf("phase = %v (status %q), want phaseModel", got.phase, got.status)
	}
	// slotTestConfig has no native model, so the profile band is on top.
	if ids := itemIDs(got); len(ids) < 2 || ids[0] != "profile/broken" || ids[1] != "profile/ok" {
		t.Fatalf("fixture: rows = %v, want profile/broken then profile/ok first", ids)
	}
	return got
}

// markedIDs is the id of every row that carries the last-picked marker.
func markedIDs(m model) []string {
	var ids []string
	for _, it := range m.models.Items() {
		if mi := it.(*modelItem); mi.marked {
			ids = append(ids, mi.model.ID)
		}
	}
	return ids
}

// record is a seed step that fails the test when a state write fails.
func record(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("seed state: %v", err)
	}
}

// TestSlotStartingCursor pins where the picker opens and which row carries
// the "> " marker, for each state a profile launch can leave behind. A bare
// Enter launches the row under the cursor, so a wrong rule here launches
// something the user did not expect: a profile must be offered again after
// it was used (profiles do not rotate), and must never capture the cursor
// when it is blocked, gone, or was followed by a single-model launch.
func TestSlotStartingCursor(t *testing.T) {
	tests := []struct {
		name       string
		seed       func(t *testing.T, r *rotation.Rotation)
		wantCursor string
		wantMarked []string
	}{
		{
			name: "a usable profile was picked last",
			seed: func(t *testing.T, r *rotation.Rotation) {
				record(t, r.RecordFor("claude", "openrouter/a"))
				record(t, r.RecordProfile("claude", "profile/ok"))
			},
			wantCursor: "profile/ok",
			wantMarked: []string{"profile/ok"},
		},
		{
			name: "a blocked profile was picked last",
			seed: func(t *testing.T, r *rotation.Rotation) {
				record(t, r.RecordFor("claude", "openrouter/a"))
				record(t, r.RecordProfile("claude", "profile/broken"))
			},
			wantCursor: "openrouter/b",
			wantMarked: []string{"profile/broken"},
		},
		{
			name: "the profile picked last is no longer in the file",
			seed: func(t *testing.T, r *rotation.Rotation) {
				record(t, r.RecordFor("claude", "openrouter/a"))
				record(t, r.RecordProfile("claude", "profile/gone"))
			},
			wantCursor: "openrouter/b",
			wantMarked: nil,
		},
		{
			name: "a model was launched after a profile",
			seed: func(t *testing.T, r *rotation.Rotation) {
				record(t, r.RecordProfile("claude", "profile/ok"))
				record(t, r.RecordFor("claude", "openrouter/a"))
			},
			wantCursor: "openrouter/b",
			wantMarked: []string{"openrouter/a"},
		},
		{
			name: "state from before last-picked.state existed",
			seed: func(t *testing.T, r *rotation.Rotation) {
				record(t, r.RecordFor("claude", "openrouter/a"))
				record(t, os.Remove(filepath.Join(config.Dir(), "last-picked.state")))
			},
			wantCursor: "openrouter/b",
			wantMarked: []string{"openrouter/a"},
		},
		{
			name:       "no state: the first row that is not blocked",
			seed:       nil,
			wantCursor: "profile/ok",
			wantMarked: nil,
		},
		{
			name: "a blocked profile is the only launch so far",
			seed: func(t *testing.T, r *rotation.Rotation) {
				record(t, r.RecordProfile("claude", "profile/broken"))
			},
			wantCursor: "profile/ok",
			wantMarked: []string{"profile/broken"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var seed func(*rotation.Rotation)
			if tt.seed != nil {
				seed = func(r *rotation.Rotation) { tt.seed(t, r) }
			}
			got := slotCursorPicker(t, seed)
			if id := selectedModelID(got); id != tt.wantCursor {
				t.Errorf("cursor on %q, want %q (rows %v)", id, tt.wantCursor, itemIDs(got))
			}
			if marked := markedIDs(got); !slices.Equal(marked, tt.wantMarked) {
				t.Errorf("marked rows = %v, want %v", marked, tt.wantMarked)
			}
		})
	}
}

// TestSlotMarkerSurvivesTableRefresh verifies a rebuilt table (the refresh
// after a failed or cancelled start) reads the marker the way the first build
// did. If the refresh read rotation.state, the marker would jump from the
// profile to the last single model as soon as a start was cancelled.
func TestSlotMarkerSurvivesTableRefresh(t *testing.T) {
	m := slotCursorPicker(t, func(r *rotation.Rotation) {
		record(t, r.RecordFor("claude", "openrouter/a"))
		record(t, r.RecordProfile("claude", "profile/ok"))
	})

	next, cmd := m.refreshTable()
	got := drainCmds(t, next, cmd)

	if marked := markedIDs(got); !slices.Equal(marked, []string{"profile/ok"}) {
		t.Errorf("marked rows after a refresh = %v, want [profile/ok]", marked)
	}
	if id := selectedModelID(got); id != "profile/ok" {
		t.Errorf("cursor after a refresh on %q, want it kept on profile/ok", id)
	}
}

// TestLaunchAndRecordMovesMarkerOffProfile verifies the TUI's single-model
// commit point writes last-picked.state as well as rotation.state. If it
// wrote rotation.state alone, the marker would stay on a profile launched
// earlier and the cursor would start there again, although the user's last
// launch was a single model.
func TestLaunchAndRecordMovesMarkerOffProfile(t *testing.T) {
	dir := tempStateDir(t)
	record(t, rotation.NewAt(dir).RecordProfile("claude", "profile/ok"))
	m := phaseModelWithList(t, testConfig(), "claude", "code")
	first, ok := m.models.Items()[0].(*modelItem)
	if !ok {
		t.Fatalf("items[0] is %T, want *modelItem", m.models.Items()[0])
	}
	m.launchModel = first.model
	// The returned command would run the agent; it is dropped, so nothing runs.
	m, _ = m.launchAndRecord(exec.Command("true"))

	for _, name := range []string{"rotation.state", "last-picked.state"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if want := first.model.ID + "\n"; string(data) != want {
			t.Errorf("%s = %q, want %q", name, data, want)
		}
	}
	if got, _ := rotation.NewAt(dir).LastPicked(); got != first.model.ID {
		t.Errorf("LastPicked = %q, want %q", got, first.model.ID)
	}
}
```

- [ ] **Step 12: Run them and see them fail**

```bash
cd wt && go test ./internal/tui -run 'TestSlotStartingCursor|TestSlotMarkerSurvivesTableRefresh|TestLaunchAndRecordMovesMarkerOffProfile'
```

Expected: these failures. The picker still reads the marker from `rotation.state`, so it never sees a profile id.

```
--- FAIL: TestSlotStartingCursor (0.01s)
    --- FAIL: TestSlotStartingCursor/a_usable_profile_was_picked_last (0.00s)
        slot_cursor_test.go:162: cursor on "openrouter/b", want "profile/ok" (rows [profile/broken profile/ok openrouter/a claude/opus openrouter/b omlx/qwen3.8])
        slot_cursor_test.go:165: marked rows = [openrouter/a], want [profile/ok]
    --- FAIL: TestSlotStartingCursor/a_blocked_profile_was_picked_last (0.00s)
        slot_cursor_test.go:165: marked rows = [openrouter/a], want [profile/broken]
    --- FAIL: TestSlotStartingCursor/the_profile_picked_last_is_no_longer_in_the_file (0.00s)
        slot_cursor_test.go:165: marked rows = [openrouter/a], want []
    --- FAIL: TestSlotStartingCursor/a_blocked_profile_is_the_only_launch_so_far (0.00s)
        slot_cursor_test.go:165: marked rows = [], want [profile/broken]
--- FAIL: TestSlotMarkerSurvivesTableRefresh (0.00s)
    slot_cursor_test.go:185: marked rows after a refresh = [openrouter/a], want [profile/ok]
    slot_cursor_test.go:188: cursor after a refresh on "openrouter/b", want it kept on profile/ok
FAIL
```

The order of the rows after the two profile rows may differ from the listing above. Three subtests of `TestSlotStartingCursor` pass already, and so does `TestLaunchAndRecordMovesMarkerOffProfile`: Step 3's `RecordFor` made that one true, and it stays to pin the TUI's commit point (`launchAndRecord`).

If a test stops at `fixture: ...` instead, Task 5's `slotTestConfig` is wrong (it must list `openrouter/b` right after `openrouter/a` and hold no native model). Fix the fixture, not this test.

- [ ] **Step 13: Write the implementation**

Three edits in `wt/internal/tui/app.go`. No import changes.

Edit 1, in `tableFor`. Replace its doc comment and its first line. Remove:

```go
// tableFor builds the selector table for agent from models and an inventory
// snapshot, returning the table and the rotation's last-launched id. It is
// the exact table build enterModelPhase performs (rotation last id from rot,
// agent-scoped survey stats, one usage + refcount store read), shared with
// refreshTable so a rebuilt table matches the original in every column.
func (m model) tableFor(agent string, models []config.Model, snap *localmodels.Snapshot, rot *rotation.Rotation) (modelTable, string) {
	lastID, _ := rot.Last()
```

Put in its place:

```go
// tableFor builds the selector table for agent from models and an inventory
// snapshot, returning the table and the last-picked row id, which is the row
// the marker sits on: a model's id or a slot profile's (rot.LastPicked). It
// is not the rotation pointer; a caller that needs that reads rot.Last. This
// is the exact table build enterModelPhase performs (last-picked id from rot,
// agent-scoped survey stats, one usage + refcount store read), shared with
// refreshTable so a rebuilt table matches the original in every column.
func (m model) tableFor(agent string, models []config.Model, snap *localmodels.Snapshot, rot *rotation.Rotation) (modelTable, string) {
	lastID, _ := rot.LastPicked()
```

Leave the rest of `tableFor` as it is. `refreshTable` calls `tableFor` too, so a rebuilt table gets the same marker with no further change.

Edit 2, in `enterModelPhase`. Replace the comment above the `rotation.New()` call. Remove:

```go
	// One Rotation for both the marker and the cursor (each New() scans the
	// config dir). A missing rotation.state yields "". tableFor also reads it
	// for the marker; the cursor logic below reuses rot so the config dir is
	// scanned once.
	rot := rotation.New()
	tbl, lastID := m.tableFor(agent, models, snap, rot)
```

Put in its place:

```go
	// One Rotation for both the marker and the cursor (each New() scans the
	// config dir). tableFor reads the last-picked row id from it for the
	// marker ("" when nothing was ever launched); the cursor logic below
	// reuses rot so the config dir is scanned once.
	rot := rotation.New()
	tbl, lastID := m.tableFor(agent, models, snap, rot)
```

Edit 3, in `enterModelPhase`. Replace the cursor block. Remove:

```go
	// Cursor: the rotation's next-to-use model among actionable rows
	// (launch or start — anything without a hint), otherwise the first
	// actionable row, otherwise the top.
	var actionable []config.Model
	first := -1
	for i, it := range tbl.items {
		if it.blocked == "" {
			actionable = append(actionable, it.model)
			if first < 0 {
				first = i
			}
		}
	}
	pos := 0
	if first >= 0 {
		pos = first
	}
	// Rotation positions the cursor only when the last-launched model is a
	// registry model: with no rotation state (or a stale/discovered id that
	// is never in cfg.Models) NextFromEligible would fall back to the
	// registry-first model, so the first launchable row is used instead.
	if lastID != "" && config.IndexModelByID(m.cfg.Models, lastID) >= 0 {
		if next, ok := rot.NextFromEligible(actionable, m.cfg); ok {
			if idx, ok := idIndex[next.ID]; ok {
				pos = idx
			}
		}
	}
	m.models.Select(pos)
```

Put in its place:

```go
	// Cursor, in this order: the last-picked row when it is a slot profile
	// that is here and not blocked (profiles do not rotate, so the one just
	// used is offered again); otherwise the rotation's next-to-use model
	// among actionable model rows (launch or start: anything without a
	// hint); otherwise the first actionable row, a profile row included;
	// otherwise the top.
	//
	// actionable holds model rows only. Rotation walks the registry, so a
	// profile row could never be its answer, and a profile row's model is an
	// id with nothing else in it.
	var actionable []config.Model
	first := -1
	for i, it := range tbl.items {
		if it.blocked != "" {
			continue
		}
		if first < 0 {
			first = i
		}
		if it.profile == nil {
			actionable = append(actionable, it.model)
		}
	}
	pos := 0
	if first >= 0 {
		pos = first
	}
	if idx, ok := idIndex[lastID]; ok && tbl.items[idx].profile != nil && tbl.items[idx].blocked == "" {
		pos = idx
	} else if rotLast, _ := rot.Last(); rotLast != "" && config.IndexModelByID(m.cfg.Models, rotLast) >= 0 {
		// Rotation positions the cursor only when the last-launched model is
		// a registry model: with no rotation state (or a stale/discovered id
		// that is never in cfg.Models) NextFromEligible would fall back to
		// the registry-first model, so the first actionable row is used
		// instead. The pointer is read from rotation.state (rot.Last), not
		// from lastID: after a profile launch the two differ.
		if next, ok := rot.NextFromEligible(actionable, m.cfg); ok {
			if idx, ok := idIndex[next.ID]; ok {
				pos = idx
			}
		}
	}
	m.models.Select(pos)
```

What changed in the block:

- `first` still counts every row that is not blocked, profile rows included. `actionable`, the list handed to rotation, now leaves profile rows out.
- A usable profile picked last takes the cursor before rotation is asked.
- The rotation guard reads `rot.Last()` where it read `lastID`. `lastID` is now the marker's id, and after a profile launch it is not the rotation pointer.

The pin branch above the block and the single-row shortcut below it are not touched.

- [ ] **Step 14: Run the tests and see them pass**

```bash
cd wt && go test ./internal/tui -run 'TestSlotStartingCursor|TestSlotMarkerSurvivesTableRefresh|TestLaunchAndRecordMovesMarkerOffProfile' -v
```

Expected: `PASS` for all three, with seven `--- PASS` subtests under `TestSlotStartingCursor`.

Then the packages this task touched, and the two that call `RecordFor`:

```bash
cd wt && go build ./... && go vet ./internal/rotation ./internal/refcount ./internal/tui && go test ./internal/rotation ./internal/refcount ./internal/tui ./cmd/wt ./internal/survey
```

Expected: five `ok` lines and no `FAIL`. The existing cursor tests (`TestSelectedEntryPositionsAfterLast`, `TestSelectedEntryLastDiscoveredIDFallsBackToFirstRow`, `TestEnterModelPhaseNoRotationDefaultsToNativeRow`, `TestModelPickerMarksLastLaunchedRow`) pass unchanged: with no profile row and state written by `RecordFor`, the new block decides exactly as the old one did.

- [ ] **Step 15: Commit**

```bash
cd wt && gofmt -l ./internal/tui
```

Expected: no output.

```bash
git add wt/internal/tui/app.go wt/internal/tui/slot_cursor_test.go
git commit -m "feat(wt): picker marker from last-picked.state; a usable profile picked last keeps the cursor"
```

### Task 8: Non-TUI profile launch

This task turns the feature on for the non-TUI path (`-W`, `--cwd`, or outside a repository). `newApp` loads and statically validates `slot-profiles.toml`. `resolveModel` resolves a `-M profile/<name>` pin to its catalog row. `launchSlotProfile` runs the ten steps of the spec's launch flow as one function. `applySlotProfile` is the fail-closed applier. `runSlotAgentCmd` runs the agent and the post-exit flow of a profile session.

Every abort happens before the agent starts and before anything is recorded. No failure degrades to an unprofiled launch.

**Before you start.** Tasks 2 to 7 must be merged. Check from the repo root:

```bash
cd wt && go doc ./internal/profiles ValidateSlots && go doc ./internal/agents BuildSlotLaunchCmd && go doc ./internal/catalog ResolveSlotProfile && go doc ./internal/lifecycle EnsureModelRoutes && go doc ./internal/rotation Rotation.RecordProfile && go doc ./internal/refcount StoreImpl.RecordAll
```

Each must print a declaration. Also check Task 5's two guards are in `wt/cmd/wt/resolve.go`: `grep -n 'IsProfile' wt/cmd/wt/resolve.go` must print two lines (one in `pickerBlockedReason`, one in `launchableModels`).

**Files:**
- Create: `wt/cmd/wt/slot_launch.go`
- Modify: `wt/cmd/wt/app.go` (`type app`; a new seam `loadSlotStore`; `newApp`)
- Modify: `wt/cmd/wt/start.go` (`startForLaunch` becomes a wrapper; new `startForLaunchOwed`)
- Modify: `wt/cmd/wt/resolve.go` (`resolveModel`: the `catalog.Input`, the pin branch, the two `len(rows) == 0` tests)
- Modify: `wt/cmd/wt/launch.go` (`launchFilteredImpl`: four lines after the model is resolved)
- Modify: `wt/cmd/wt/main.go` (`runLaunchPath`: after `sweepRefcounts()`)
- Modify: `wt/cmd/wt/testmain_test.go` (`TestMain`: fail-closed defaults for six seams)
- Test: `wt/cmd/wt/slot_launch_test.go` (new)
- Test: `wt/cmd/wt/app_test.go` (modify)
- Test: `wt/cmd/wt/resolve_test.go` (modify)

**Interfaces:**

Consumes, from existing code in `wt/cmd/wt`:

```go
var probeInventory = localmodels.Inventory                                  // resolve.go:17
var allowReplace bool                                                       // resolve.go:23
var osStderr io.Writer = os.Stderr                                          // start.go:160
var shellDir = os.Getwd                                                     // launch.go:129
var releaseSession = survey.ReleaseSession                                  // launch.go:35
var runStopPicker = realRunStopPicker                                       // launch.go:40
var lifecycleStart = lifecycle.Start                                        // start.go:35
func settleStart(ctx context.Context, cfg *config.Config, id string, t lifecycle.Target) error // start.go:73
func startFailure(id string, err error) error                               // start.go:350
func asStartError(err error) error                                          // start.go:358
func selfHealAgentConfigContentTarget(agent, worktreePath string)           // launch.go:303
func pickerBlockedReason(cfg *config.Config, agent string, row catalog.Row) string // resolve.go:145; returns row.BlockReason() for a profile row (Task 5)
```

Consumes, from existing packages:

```go
// wt/internal/agents
func Summary(agent string, m config.Model, d time.Duration) string   // "wt: <agent> · <m.ID> · <duration>"
func RunAndCleanup(cmd *exec.Cmd, cleanup func() error) (time.Duration, error) // cleanup must not be nil
func RelativeArgNotes(agent, shellDir, launchDir string, args []string) []string

// wt/internal/profiles
type ResolvedProfile struct {
	Env           map[string]string
	ExtraArgs     []string
	ConfigContent map[string]any
	Wrapper       *WrapperSpec
	Sources       []string
}
func ApplyEnvAndArgs(cmd *exec.Cmd, rp ResolvedProfile)   // appends rp.Env and rp.ExtraArgs to cmd
func ApplyWrapper(cmd *exec.Cmd, w *WrapperSpec) error    // error when the binary is not on PATH

// wt/internal/lifecycle
func WaitPendingRoutes()
func SettleRoutes(ctx context.Context, cfg *config.Config)
```

Consumes, from earlier tasks:

```go
// Task 2, wt/internal/profiles
const SlotIDPrefix = "profile/"
type SlotProfile struct {
	Name, Agent, Default string
	Slots                map[string]string
	Env                  map[string]string
	Args                 []string
	ConfigContent        map[string]any
	Wrapper              *WrapperSpec
}
type SlotStore struct{ Profiles []SlotProfile }
func LoadSlots() (SlotStore, error)   // a missing file is SlotStore{}, nil; a parse error is "parse slot-profiles.toml: …"
type SlotCheck struct {
	Index     int
	Profile   SlotProfile
	Usable    bool
	Duplicate bool
	Declared  []string
	Err       error
}
func (c SlotCheck) Label() string     // "profile/<name>" when Usable, else "slot-profiles.toml[<Index>]"
func ValidateSlots(store SlotStore, d SlotDrivers) []SlotCheck
type SlotLaunch struct { /* Agent, Models, Default, Env, ConfigContent */ }

// Task 3, wt/internal/agents
func SlotDrivers() profiles.SlotDrivers
func SlotsFor(agent string) (profiles.SlotCapable, bool)
func CheckSlotPassthrough(agent string, extraArgs []string) error   // `a slot profile sets %s itself: remove it from the arguments after --`
func BuildSlotLaunchCmd(sl profiles.SlotLaunch, worktreePath string, yolo bool, cfg *config.Config, extraArgs []string) (*exec.Cmd, error)

// Task 4, wt/internal/catalog
type Input struct { /* … */ SlotProfiles []profiles.SlotCheck }
type ProfileRow struct {
	Check   profiles.SlotCheck
	Models  map[string]config.Model
	Local   *Row
	Verdict Action
	Reason  string
	Loc     string
}
func (r Row) IsProfile() bool
func SlotPrefixReserved(cfg *config.Config) bool
func ResolveSlotProfile(cfg *config.Config, snap *localmodels.Snapshot, c profiles.SlotCheck) Row
func (p *ProfileRow) Distinct() []config.Model
func (p *ProfileRow) Cloud() []config.Model
func (p *ProfileRow) SlotOf(modelID string) string
func (p *ProfileRow) Launch() profiles.SlotLaunch

// Task 6, wt/internal/lifecycle
type Options struct { /* … */ RestartOwed bool }
type RouteEnsureError struct{ ModelID string; Err error }   // Error(): "LiteLLM route for <ModelID> not written: <Err>"
func EnsureModelRoutes(ctx context.Context, out io.Writer, cfg *config.Config, models []config.Model, owedIn bool) (owed bool, err error)
func RestartProxy(ctx context.Context, cfg *config.Config) error
func ProbeProxy(ctx context.Context, cfg *config.Config) error

// Task 7, wt/internal/rotation and wt/internal/refcount
func (r *Rotation) RecordProfile(agent, profileID string) error    // rotation: writes last-picked.state and the usage event; never rotation.state
func (s *StoreImpl) RecordAll(pid int, modelIDs []string) error    // refcount: one line per distinct, non-empty id
```

Produces:

```go
// wt/cmd/wt/app.go
type app struct {
	// … existing fields …
	slotChecks  []profiles.SlotCheck // slot-profiles.toml after static validation, in file order; nil when missing or not loaded
	slotLoadErr error                // profiles.LoadSlots' error
}
var loadSlotStore = profiles.LoadSlots

// wt/cmd/wt/start.go
func startForLaunchOwed(cfg *config.Config, row catalog.Row, allowReplace, restartOwed bool) (stillOwed bool, err error)
func startForLaunch(cfg *config.Config, row catalog.Row, allowReplace bool) error   // unchanged signature

// wt/cmd/wt/slot_launch.go
var launchSlotChecks []profiles.SlotCheck
type slotPin struct{ row catalog.Row }
func (p *slotPin) Error() string
var (
	ensureSlotRoutes = lifecycle.EnsureModelRoutes
	restartProxyNow  = lifecycle.RestartProxy
	probeProxyNow    = lifecycle.ProbeProxy
	settleRoutesNow  = lifecycle.SettleRoutes
	startSlotModel   = startForLaunchOwed
	buildSlotCmd     = agents.BuildSlotLaunchCmd
)
func slotWarnings(a *app) []string
func launchSlotProfile(agent, worktreePath string, cfg *config.Config, yolo bool, row catalog.Row, extraArgs []string) error
func applySlotProfile(cmd *exec.Cmd, agent string, row catalog.Row) (cleanup func() error, err error)   // Task 9 hands it to tui.Run
func runSlotAgentCmd(cmd *exec.Cmd, agent string, row catalog.Row, cfg *config.Config, cleanup func() error) error

// wt/cmd/wt/slot_launch_test.go — fixtures Task 10 uses
func slotFixtureConfig() *config.Config
func slotFixtureCheck(index int, name, def string, slots map[string]string) profiles.SlotCheck
func stubSlotChecks(t *testing.T, checks ...profiles.SlotCheck)
type slotSeams struct { /* events, ensureOwed, ensureErr, startStill, startErr, restartErr, probeErr, buildErr, cmd */ }
func stubSlotSeams(t *testing.T) *slotSeams
```

**Rules that hold for every step of this task:**

- Run every Go command from `wt/`. The commands below start with `cd wt &&` and are written for the repo root.
- Every `Test*` function has a `//` comment above it saying what it tests and what breaks for the user if it regresses.
- No code in this task reads `cfg.IsLitellm()`.
- No test here stubs `tuiRun`. Task 9 changes its signature and updates a fixed list of stubs.

**Where the spec is silent, this task follows the surrounding code:**

- A profile's `args` are appended after the user's passthrough arguments, because `profiles.ApplyEnvAndArgs` appends to whatever `cmd.Args` already holds, as it does for an automatic profile.
- Without a pin, `resolveModel` words its "nothing to launch" errors from the model rows only, so that path's messages are what they were before slot profiles existed (spec: "With no `-M`, that path is unchanged").
- The fail-closed default of `settleRoutesNow` panics, because `lifecycle.SettleRoutes` returns nothing and so cannot report "not stubbed" as an error.

---

#### Behaviour 1: `newApp` loads the slot profiles

- [ ] **Step 1: Write the failing tests**

In `wt/cmd/wt/app_test.go`, replace the line `import "testing"` with:

```go
import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)
```

Append this to the end of the file:

```go
// writeSlotProfilesTOML points XDG_CONFIG_HOME at a fresh temp directory,
// writes body there as agent-wt/slot-profiles.toml, and returns the file's
// path.
func writeSlotProfilesTOML(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "agent-wt"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "agent-wt", "slot-profiles.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestNewAppLoadsSlotProfiles verifies newApp reads slot-profiles.toml and
// keeps one static verdict per profile, in file order, against the real agent
// drivers. A profile with a malformed name stays in the list as not usable, so
// `wt profile list` can report it by position and the launch can warn once. If
// newApp dropped it, a typo in one profile would vanish without a word.
func TestNewAppLoadsSlotProfiles(t *testing.T) {
	writeSlotProfilesTOML(t, `
[[profile]]
name    = "mixed"
agent   = "claude"
default = "opus"

[profile.slots]
opus = "openrouter/a"

[[profile]]
name    = "Bad_Name"
agent   = "claude"
default = "opus"

[profile.slots]
opus = "openrouter/a"
`)
	a, err := newApp()
	if err != nil {
		t.Fatalf("newApp() error = %v", err)
	}
	if a.slotLoadErr != nil {
		t.Fatalf("a.slotLoadErr = %v, want nil", a.slotLoadErr)
	}
	if len(a.slotChecks) != 2 {
		t.Fatalf("a.slotChecks = %+v, want 2 checks", a.slotChecks)
	}
	if c := a.slotChecks[0]; !c.Usable || c.Err != nil || c.Label() != "profile/mixed" {
		t.Errorf("slotChecks[0] = %+v, want the usable profile/mixed with no error", c)
	}
	if c := a.slotChecks[1]; c.Usable || c.Err == nil || c.Label() != "slot-profiles.toml[1]" {
		t.Errorf("slotChecks[1] = %+v, want a not-usable check labelled by its position", c)
	}
}

// TestNewAppWithoutSlotProfilesFile verifies a machine with no
// slot-profiles.toml gets no checks and no error: the file is optional, and
// its absence must not print a warning on every launch.
func TestNewAppWithoutSlotProfilesFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	a, err := newApp()
	if err != nil {
		t.Fatalf("newApp() error = %v", err)
	}
	if a.slotChecks != nil || a.slotLoadErr != nil {
		t.Errorf("slotChecks = %+v, slotLoadErr = %v, want nil and nil", a.slotChecks, a.slotLoadErr)
	}
}

// TestNewAppSlotParseErrorDoesNotStopWt verifies a TOML typo in
// slot-profiles.toml is stored, not returned: wt still starts, automatic
// profiles still load, and the launch path can say what is wrong. Returning
// the error would make one bad hand-written file break every wt command.
func TestNewAppSlotParseErrorDoesNotStopWt(t *testing.T) {
	writeSlotProfilesTOML(t, "[[profile]]\nname = \"mixed\n")
	a, err := newApp()
	if err != nil {
		t.Fatalf("newApp() error = %v, want nil (slotLoadErr carries the problem)", err)
	}
	if a.slotLoadErr == nil || !strings.Contains(a.slotLoadErr.Error(), "parse slot-profiles.toml") {
		t.Fatalf("a.slotLoadErr = %v, want the parse error", a.slotLoadErr)
	}
	if a.slotChecks != nil {
		t.Errorf("a.slotChecks = %+v, want nil when the file did not load", a.slotChecks)
	}
	if a.profilesLoadErr != nil || !a.profiles.Enabled {
		t.Errorf("profiles.toml state = (%+v, %v), want it untouched by the slot file's error", a.profiles, a.profilesLoadErr)
	}
}
```

- [ ] **Step 2: Run them and see them fail**

```bash
cd wt && go test ./cmd/wt -run 'TestNewApp.*Slot' -v
```

Expected: the build fails with `a.slotLoadErr undefined (type *app has no field or method slotLoadErr)` and `a.slotChecks undefined (type *app has no field or method slotChecks)`, then `FAIL	github.com/ohanaverse/local-ai-setup/wt/cmd/wt [build failed]`.

- [ ] **Step 3: Write the implementation**

In `wt/cmd/wt/app.go`, add two fields at the end of `type app`, after `profilesValidateErr error`:

```go
	// slotChecks is slot-profiles.toml after static validation
	// (profiles.ValidateSlots), in file order; nil when the file is missing
	// or did not load. A check that is not Usable is kept: `wt profile list`
	// reports it by its position and a launch warns about it once.
	slotChecks []profiles.SlotCheck
	// slotLoadErr is profiles.LoadSlots' error: a parse or read failure.
	// It never stops wt; a launch prints it once and shows no profile rows.
	slotLoadErr error
```

Add the seam above `newApp`'s doc comment (the comment that starts `// newApp loads the config (best-effort)`), with one blank line between the seam and that comment, so the comment stays attached to `newApp`:

```go
// loadSlotStore is a seam for tests: production reads
// ~/.config/agent-wt/slot-profiles.toml (profiles.LoadSlots).
var loadSlotStore = profiles.LoadSlots
```

In `newApp`, replace the final `return &app{…}, nil` statement with:

```go
	// Slot profiles load independently of profiles.toml: an error in either
	// file never hides the other.
	var slotChecks []profiles.SlotCheck
	slotStore, slotLoadErr := loadSlotStore()
	if slotLoadErr == nil && len(slotStore.Profiles) > 0 {
		slotChecks = profiles.ValidateSlots(slotStore, agents.SlotDrivers())
	}
	return &app{
		cfg: cfg, cfgErr: cfgErr, loadErr: loadErr, theme: theme,
		profiles: store, profilesLoadErr: profilesLoadErr, profilesValidateErr: profilesValidateErr,
		slotChecks: slotChecks, slotLoadErr: slotLoadErr,
	}, nil
```

- [ ] **Step 4: Run the tests and see them pass**

```bash
cd wt && go test ./cmd/wt -run 'TestNewApp' -v
```

Expected: `--- PASS` for the three new tests and the three existing `TestNewApp…` tests, then `ok`.

- [ ] **Step 5: Commit**

```bash
git add wt/cmd/wt/app.go wt/cmd/wt/app_test.go
git commit -m "feat(wt): load and statically validate slot-profiles.toml at startup"
```

---

#### Behaviour 2: `startForLaunchOwed`, the start that carries an owed restart

- [ ] **Step 6: Write the failing test**

Create `wt/cmd/wt/slot_launch_test.go` with:

```go
package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// startCall is what one engine call of a start carried.
type startCall struct{ replace, owed bool }

// stubLifecycleStartCalls replaces the engine with scripted outcomes (the nth
// call returns outcomes[n], nil past the end) and records AllowReplace and
// RestartOwed of each call.
func stubLifecycleStartCalls(t *testing.T, outcomes []error) *[]startCall {
	t.Helper()
	calls := &[]startCall{}
	old := lifecycleStart
	lifecycleStart = func(_ context.Context, _ *config.Config, _ lifecycle.Target, opts lifecycle.Options) error {
		n := len(*calls)
		*calls = append(*calls, startCall{replace: opts.AllowReplace, owed: opts.RestartOwed})
		if n >= len(outcomes) {
			return nil
		}
		return outcomes[n]
	}
	t.Cleanup(func() { lifecycleStart = old })
	return calls
}

// TestStartForLaunchOwed verifies the start a profile launch uses. It passes
// the owed proxy restart to both engine attempts, and reports the restart as
// still owed only when it returns after a refusal it did not retry: then the
// engine touched nothing, and the caller must settle the restart itself. If
// it reported false there, the cloud routes written before the start would
// stay unpublished; if it reported true after the engine had restarted, the
// proxy would restart twice.
func TestStartForLaunchOwed(t *testing.T) {
	occupied := func() error {
		return &lifecycle.OccupiedError{Occupants: []localmodels.Entry{localmodelsEntry("omlx/other", "other")}}
	}
	unknown := func() error {
		return &lifecycle.OccupancyUnknownError{ProviderID: "mtplx", Origin: "http://localhost:8003"}
	}
	boom := errors.New("server exited")
	cases := []struct {
		name      string
		owed      bool
		replace   bool // --replace
		tty       bool
		answer    bool // the y/N answer when tty
		outcomes  []error
		wantCalls []startCall
		wantStill bool
		wantErr   string // "" means nil
	}{
		{"started at once", true, false, false, false, []error{nil}, []startCall{{false, true}}, false, ""},
		{"nothing owed is passed on as false", false, false, false, false, []error{nil}, []startCall{{false, false}}, false, ""},
		{"refused, replaced under --replace", true, true, false, false, []error{occupied(), nil}, []startCall{{false, true}, {true, true}}, false, ""},
		{"refused, confirmed at the prompt", true, false, true, true, []error{occupied(), nil}, []startCall{{false, true}, {true, true}}, false, ""},
		{"refused, declined at the prompt", true, false, true, false, []error{occupied()}, []startCall{{false, true}}, true, "cancelled — omlx/other still running"},
		{"refused, no terminal to ask on", true, false, false, false, []error{occupied()}, []startCall{{false, true}}, true, "--replace"},
		{"occupancy unknown, declined", true, false, true, false, []error{unknown()}, []startCall{{false, true}}, true, "cancelled — mtplx may still be serving a model"},
		{"refused and declined with nothing owed", false, false, true, false, []error{occupied()}, []startCall{{false, false}}, false, "cancelled"},
		{"any other failure", true, false, false, false, []error{boom}, []startCall{{false, true}}, false, "server exited"},
		{"replace granted, second attempt fails", true, true, false, false, []error{occupied(), boom}, []startCall{{false, true}, {true, true}}, false, "server exited"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubSignals(t)
			stubStdinTTY(t, tc.tty)
			oldConfirm, oldOut := confirmReplace, osStderr
			confirmReplace = func(string) (bool, error) { return tc.answer, nil }
			osStderr = io.Discard
			t.Cleanup(func() { confirmReplace, osStderr = oldConfirm, oldOut })
			calls := stubLifecycleStartCalls(t, tc.outcomes)

			still, err := startForLaunchOwed(&config.Config{}, startTestRow(), tc.replace, tc.owed)

			if still != tc.wantStill {
				t.Errorf("stillOwed = %v, want %v", still, tc.wantStill)
			}
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("err = %v, want nil", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Errorf("err = %v, want it to contain %q", err, tc.wantErr)
			}
			if len(*calls) != len(tc.wantCalls) {
				t.Fatalf("engine calls = %+v, want %+v", *calls, tc.wantCalls)
			}
			for i, want := range tc.wantCalls {
				if (*calls)[i] != want {
					t.Errorf("engine call %d = %+v, want %+v", i, (*calls)[i], want)
				}
			}
		})
	}
}
```

`stubSignals`, `startTestRow` and `localmodelsEntry` are existing helpers in `wt/cmd/wt/start_test.go`. `stubStdinTTY` is in `wt/cmd/wt/helpers_test.go`. `TestMain` already makes `lifecycleSettleStart` return nil and `sessionCounts` return an empty map.

- [ ] **Step 7: Run it and see it fail**

```bash
cd wt && go test ./cmd/wt -run TestStartForLaunchOwed -v
```

Expected: the build fails with `undefined: startForLaunchOwed`, then `FAIL	github.com/ohanaverse/local-ai-setup/wt/cmd/wt [build failed]`.

- [ ] **Step 8: Write the implementation**

In `wt/cmd/wt/start.go`, replace the whole of `startForLaunch` (the doc comment that starts `// startForLaunch starts the local model behind a -M pin` through the function's closing brace) with:

```go
// startForLaunch starts the local model behind a -M pin on the non-TUI path.
// It reports progress on stderr and cancels on Ctrl+C; a second Ctrl+C exits
// the process (the signal handler is restored once the first cancels, so a
// hung teardown cannot trap the user).
//
// It never asks for permission to stop an occupant unless the pin needs it:
// only an OccupiedError or an OccupancyUnknownError reaches the confirm path,
// and only on the first attempt. Any other failure is returned as-is.
func startForLaunch(cfg *config.Config, row catalog.Row, allowReplace bool) error {
	_, err := startForLaunchOwed(cfg, row, allowReplace, false)
	return err
}

// startForLaunchOwed is startForLaunch for a caller that already owes a proxy
// restart: a slot-profile launch that wrote a cloud slot's route before
// starting its local model. restartOwed goes to both engine attempts as
// lifecycle.Options.RestartOwed, so the start's own restart publishes that
// route too.
//
// stillOwed is true only when restartOwed was true and the function returns
// after a refusal it did not retry: the y/N prompt was declined, failed, or
// was interrupted. The engine touched nothing then, so the caller settles the
// restart (lifecycle.SettleRoutes). On every other return the engine has
// started the one restart, or nothing was owed.
func startForLaunchOwed(cfg *config.Config, row catalog.Row, allowReplace, restartOwed bool) (stillOwed bool, err error) {
	ctx, stop := startSignalCtx()
	defer stop()
	go func() {
		<-ctx.Done()
		// Restore the default handler so a second Ctrl+C kills wt.
		stop()
	}()

	id := row.Model.ID
	done := make(chan struct{})
	defer close(done)
	began := time.Now()
	report := startProgress(id, done, began)

	target := lifecycle.Target{ProviderID: row.Model.ProviderID, ModelName: row.Model.ModelName, ModelID: row.Model.ID}
	opts := lifecycle.Options{Progress: report, RestartOwed: restartOwed}

	err = lifecycleStart(ctx, cfg, target, opts)
	if err == nil {
		// The agent launch follows immediately and routes through LiteLLM:
		// settle the route hook's async proxy restart first.
		return false, settleStart(ctx, cfg, id, target)
	}
	if errors.Is(err, context.Canceled) {
		return false, fmt.Errorf("cancelled before %s started: %w", id, err)
	}

	// ask races the prompt against ctx: the read on /dev/tty cannot be
	// interrupted, and the signal handler swallows Ctrl+C, so without the race
	// Ctrl+C at the y/N prompt would only cancel ctx and leave the read
	// blocked until Enter. The abandoned read goroutine dies with the process.
	ask := func(question string) (bool, error) {
		type answer struct {
			ok  bool
			err error
		}
		ch := make(chan answer, 1)
		go func() {
			ok, aerr := askReplace(question)
			ch <- answer{ok, aerr}
		}()
		select {
		case a := <-ch:
			return a.ok, a.err
		case <-ctx.Done():
			return false, fmt.Errorf("cancelled before %s started: %w", id, ctx.Err())
		}
	}

	var occ *lifecycle.OccupiedError
	var unk *lifecycle.OccupancyUnknownError
	switch {
	case errors.As(err, &occ):
		counts := sessionCounts(occ.IDs())
		parts := make([]string, len(occ.Occupants))
		for i, oid := range occ.IDs() {
			parts[i] = oid
			if n := counts[oid]; n > 0 {
				parts[i] = fmt.Sprintf("%s (in use by %d wt session(s))", oid, n)
			}
		}
		names := strings.Join(parts, ", ")
		if !allowReplace {
			ok, perr := ask(fmt.Sprintf("starting %s will stop %s; continue?", id, names))
			if perr != nil {
				return restartOwed, perr
			}
			if !ok {
				return restartOwed, fmt.Errorf("cancelled — %s still running", names)
			}
		}
		fmt.Fprintf(osStderr, "wt: replacing %s\n", names)
	case errors.As(err, &unk):
		if !allowReplace {
			ok, perr := ask(fmt.Sprintf("cannot tell whether %s at %s is already serving a model; replace it with %s?", unk.ProviderID, unk.Origin, id))
			if perr != nil {
				return restartOwed, perr
			}
			if !ok {
				return restartOwed, fmt.Errorf("cancelled — %s may still be serving a model", unk.ProviderID)
			}
		}
		fmt.Fprintf(osStderr, "wt: replacing whatever %s is serving at %s\n", unk.ProviderID, unk.Origin)
	default:
		return false, startFailure(id, err)
	}

	opts.AllowReplace = true
	err = lifecycleStart(ctx, cfg, target, opts)
	if err == nil {
		// Same as the first attempt: the proxy must carry the new route (and
		// have dropped the replaced occupant's) before the agent launches.
		return false, settleStart(ctx, cfg, id, target)
	}
	if errors.Is(err, context.Canceled) {
		return false, fmt.Errorf("cancelled before %s started: %w", id, err)
	}
	// Replace was already granted; anything that fails now is a different
	// failure — report it rather than prompting again.
	return false, startFailure(id, err)
}
```

The body is the old `startForLaunch` with four changes: `RestartOwed: restartOwed` in `opts`; every `return X` became `return false, X`; the four returns inside `if !allowReplace { … }` return `restartOwed` in place of `false`; and the `ask` goroutine's local error is named `aerr` so it does not shadow the named result.

- [ ] **Step 9: Run the tests and see them pass**

```bash
cd wt && go test ./cmd/wt -run 'TestStartForLaunch' -v
```

Expected: `--- PASS` for `TestStartForLaunchOwed` (ten subtests) and for every existing `TestStartForLaunch…` test, then `ok`. The existing tests prove the wrapper kept `startForLaunch`'s behaviour.

- [ ] **Step 10: Commit**

```bash
git add wt/cmd/wt/start.go wt/cmd/wt/slot_launch_test.go
git commit -m "feat(wt): startForLaunchOwed carries an owed proxy restart into a pinned start"
```

---

#### Behaviour 3: the seams, the fixtures, and the `-M profile/<name>` pin

- [ ] **Step 11: Write the fixtures and the failing tests**

In `wt/cmd/wt/slot_launch_test.go`, replace the import block with:

```go
import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)
```

Append this to the end of the file:

```go
// slotFixtureConfig is a config with agent "claude" (providers "openrouter",
// "omlx"), cloud models "openrouter/a" and "openrouter/b", local model
// "omlx/q" (model_name "q"), and [litellm] url "http://localhost:4000",
// key "sk-test-1234", enabled false. Under TestMain's inventory stub
// (localmodels.OnDiskSnapshotForTest) "omlx/q" is on disk and not running:
// a start row.
func slotFixtureConfig() *config.Config {
	cfg := &config.Config{
		DefaultTag: "code",
		Providers: []config.Provider{
			{ID: "openrouter", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "api_key"}},
			{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}},
		},
		Models: []config.Model{
			{ID: "openrouter/a", ProviderID: "openrouter", ModelName: "a", Family: "a", Tags: []string{"code"}},
			{ID: "openrouter/b", ProviderID: "openrouter", ModelName: "b", Family: "b", Tags: []string{"code"}},
			{ID: "omlx/q", ProviderID: "omlx", ModelName: "q", Family: "q", Tags: []string{"code"}},
		},
		Agents: []config.Agent{{Name: "claude", SupportedProviders: []string{"openrouter", "omlx"}}},
	}
	cfg.SetLitellmForTest(config.LitellmState{URL: "http://localhost:4000", APIKey: "sk-test-1234"})
	return cfg
}

// slotFixtureCheck is a Usable check "profile/<name>" for agent "claude",
// with claude's four slots declared and no static error.
func slotFixtureCheck(index int, name, def string, slots map[string]string) profiles.SlotCheck {
	return profiles.SlotCheck{
		Index:    index,
		Profile:  profiles.SlotProfile{Name: name, Agent: "claude", Default: def, Slots: slots},
		Usable:   true,
		Declared: []string{"fable", "opus", "sonnet", "haiku"},
	}
}

// slotCloud and slotMixed are the two slot maps most tests use. The default
// slot is "opus" in both, so the distinct models come out in the order
// written here: openrouter/a first.
func slotCloud() map[string]string {
	return map[string]string{"opus": "openrouter/a", "haiku": "openrouter/b"}
}

func slotMixed() map[string]string {
	return map[string]string{"opus": "openrouter/a", "haiku": "omlx/q"}
}

// stubSlotChecks sets launchSlotChecks for one test.
func stubSlotChecks(t *testing.T, checks ...profiles.SlotCheck) {
	t.Helper()
	old := launchSlotChecks
	launchSlotChecks = checks
	t.Cleanup(func() { launchSlotChecks = old })
}

// slotSeams records, in order, what a profile launch asked of the engine:
// "ensure:<id>,<id>", "start:<id>:owed=<bool>", "restart", "probe",
// "settle", "build". Its fields script each seam's result.
type slotSeams struct {
	events     []string
	ensureOwed bool // what ensureSlotRoutes reports as owed when it adds nothing to owedIn
	ensureErr  error
	startStill bool
	startErr   error
	restartErr error
	probeErr   error
	buildErr   error
	cmd        *exec.Cmd // what buildSlotCmd returns; default exec.Command("true")
}

// log is the recorded events joined with ";".
func (s *slotSeams) log() string { return strings.Join(s.events, ";") }

// stubSlotSeams swaps the six seams of slot_launch.go for recorders and
// silences osStderr. The command buildSlotCmd hands back runs in the launch's
// worktree, as the real one does.
func stubSlotSeams(t *testing.T) *slotSeams {
	t.Helper()
	s := &slotSeams{}
	oldEnsure, oldRestart, oldProbe, oldSettle := ensureSlotRoutes, restartProxyNow, probeProxyNow, settleRoutesNow
	oldStart, oldBuild, oldOut := startSlotModel, buildSlotCmd, osStderr
	ensureSlotRoutes = func(_ context.Context, _ io.Writer, _ *config.Config, models []config.Model, owedIn bool) (bool, error) {
		ids := make([]string, len(models))
		for i, m := range models {
			ids[i] = m.ID
		}
		s.events = append(s.events, "ensure:"+strings.Join(ids, ","))
		if s.ensureErr != nil {
			return false, s.ensureErr
		}
		return owedIn || s.ensureOwed, nil
	}
	restartProxyNow = func(context.Context, *config.Config) error {
		s.events = append(s.events, "restart")
		return s.restartErr
	}
	probeProxyNow = func(context.Context, *config.Config) error {
		s.events = append(s.events, "probe")
		return s.probeErr
	}
	settleRoutesNow = func(context.Context, *config.Config) { s.events = append(s.events, "settle") }
	startSlotModel = func(_ *config.Config, row catalog.Row, _ bool, owed bool) (bool, error) {
		s.events = append(s.events, fmt.Sprintf("start:%s:owed=%t", row.Model.ID, owed))
		return s.startStill, s.startErr
	}
	buildSlotCmd = func(_ profiles.SlotLaunch, worktreePath string, _ bool, _ *config.Config, _ []string) (*exec.Cmd, error) {
		s.events = append(s.events, "build")
		if s.buildErr != nil {
			return nil, s.buildErr
		}
		cmd := s.cmd
		if cmd == nil {
			cmd = exec.Command("true")
		}
		cmd.Dir = worktreePath
		return cmd, nil
	}
	osStderr = io.Discard
	t.Cleanup(func() {
		ensureSlotRoutes, restartProxyNow, probeProxyNow, settleRoutesNow = oldEnsure, oldRestart, oldProbe, oldSettle
		startSlotModel, buildSlotCmd, osStderr = oldStart, oldBuild, oldOut
	})
	return s
}
```

In `wt/cmd/wt/resolve_test.go`, append this to the end of the file. It needs no new import: Task 5 already added `profiles` to this file's import block. Check with `grep -n 'internal/profiles' wt/cmd/wt/resolve_test.go`, which must print exactly one line. Do not add the import a second time; a duplicate does not compile (`profiles redeclared in this block`).

```go
// TestResolveModelSlotProfilePin verifies what resolveModel does with a
// `-M profile/<name>` pin. A launch or start row comes back as a *slotPin
// carrying the row, and nothing is started here: the profile launch starts
// its local model itself, after the cloud routes are written. A blocked row
// is an error with the row's reason. A profile of another agent, an unknown
// name, and any profile under -T get the existing "not in the eligible list"
// message. If a start row were started here, the proxy would restart twice;
// if a blocked row came back as a pin, a broken profile would launch.
func TestResolveModelSlotProfilePin(t *testing.T) {
	checks := []profiles.SlotCheck{
		slotFixtureCheck(0, "cloud", "opus", slotCloud()),
		slotFixtureCheck(1, "mixed", "opus", slotMixed()),
		slotFixtureCheck(2, "broken", "opus", map[string]string{"opus": "openrouter/gone"}),
	}
	cases := []struct {
		name       string
		agent      string
		tags       string
		pinned     string
		wantAction catalog.Action // read only when wantErr is ""
		wantErr    string         // "" means a *slotPin is expected
	}{
		{"launch row", "claude", "", "profile/cloud", catalog.ActionLaunch, ""},
		{"start row is not started here", "claude", "", "profile/mixed", catalog.ActionStart, ""},
		{"blocked row", "claude", "", "profile/broken", 0, "slot opus: openrouter/gone is not in the registry"},
		{"another agent's profile", "pi", "", "profile/cloud", 0, `model "profile/cloud" is not in the eligible list for agent "pi"`},
		{"unknown profile", "claude", "", "profile/nope", 0, `model "profile/nope" is not in the eligible list for agent "claude"`},
		{"hidden under -T", "claude", "code", "profile/cloud", 0, `model "profile/cloud" is not in the eligible list for agent "claude"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			started := stubStartDriver(t, nil)
			stubSlotChecks(t, checks...)
			cfg := slotFixtureConfig()
			cfg.Agents = append(cfg.Agents, config.Agent{Name: "pi", SupportedProviders: []string{"openrouter", "omlx"}})

			m, _, err := resolveModel(tc.agent, cfg, tc.tags, "", tc.pinned)

			if m.ID != "" {
				t.Errorf("model = %q, want none: a profile is not a model", m.ID)
			}
			if started.called {
				t.Error("resolveModel started a model for a profile pin")
			}
			var pin *slotPin
			isPin := errors.As(err, &pin)
			if tc.wantErr != "" {
				if isPin || err == nil || err.Error() != tc.wantErr {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if !isPin {
				t.Fatalf("err = %v, want a *slotPin", err)
			}
			if pin.row.Model.ID != tc.pinned || pin.row.Action() != tc.wantAction {
				t.Errorf("pin row = %s with action %v, want %s with action %v", pin.row.Model.ID, pin.row.Action(), tc.pinned, tc.wantAction)
			}
		})
	}
}

// TestResolveModelWithoutAPinIgnoresSlotProfiles verifies the non-TUI path
// with no -M is what it was before slot profiles existed: the same model, the
// same launchable list and the same error text with and without loaded
// profiles. A profile row must never be auto-launched, rotated to, or change
// the wording that tells the user why nothing can launch.
func TestResolveModelWithoutAPinIgnoresSlotProfiles(t *testing.T) {
	missing := localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "omlx", ModelID: "omlx/q", ModelName: "q", Registered: true, ArtifactKnown: true},
		},
	}
	cases := []struct {
		name   string
		models func([]config.Model) []config.Model
		snap   *localmodels.Snapshot
		slots  map[string]string
		want   string // a substring of the outcome, to prove the case is the one named
	}{
		{"several launchable models", func(ms []config.Model) []config.Model { return ms }, nil, slotCloud(), "multiple models match"},
		{"one launchable model", func(ms []config.Model) []config.Model { return ms[:1] }, nil, map[string]string{"opus": "openrouter/a"}, "openrouter/a|openrouter/a|"},
		{"no eligible model has a row", func(ms []config.Model) []config.Model { return ms[2:] }, &missing, map[string]string{"opus": "omlx/q"}, catalog.NoRowsReason("claude")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.snap != nil {
				stubProbeInventory(t, *tc.snap)
			}
			cfg := slotFixtureConfig()
			cfg.Models = tc.models(cfg.Models)
			outcome := func() string {
				m, launchable, err := resolveModel("claude", cfg, "", "", "")
				ids := make([]string, len(launchable))
				for i, l := range launchable {
					ids[i] = l.ID
				}
				msg := ""
				if err != nil {
					msg = err.Error()
				}
				return m.ID + "|" + strings.Join(ids, ",") + "|" + msg
			}
			old := launchSlotChecks
			t.Cleanup(func() { launchSlotChecks = old })

			launchSlotChecks = nil
			before := outcome()
			launchSlotChecks = []profiles.SlotCheck{slotFixtureCheck(0, "p", "opus", tc.slots)}
			after := outcome()

			if !strings.Contains(before, tc.want) {
				t.Fatalf("outcome without profiles = %q, want it to contain %q", before, tc.want)
			}
			if after != before {
				t.Errorf("outcome with a profile loaded = %q, want %q (unchanged)", after, before)
			}
		})
	}
}
```

- [ ] **Step 12: Run them and see them fail**

```bash
cd wt && go test ./cmd/wt -run 'TestResolveModelSlotProfilePin|TestResolveModelWithoutAPinIgnoresSlotProfiles' -v
```

Expected: the build fails with `undefined: launchSlotChecks`, `undefined: ensureSlotRoutes`, `undefined: slotPin` and the other seam names, then `FAIL	github.com/ohanaverse/local-ai-setup/wt/cmd/wt [build failed]`.

- [ ] **Step 13: Write the implementation**

Create `wt/cmd/wt/slot_launch.go`:

```go
// A slot-profile launch on the non-TUI path: the -M profile/<name> pin, the
// ten steps of the launch flow, the fail-closed applier and the post-exit
// flow. See docs/wt-agents/slot-profiles.md.
package main

import (
	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)

// launchSlotChecks is the process-wide set of slot profiles resolveModel
// offers as pin targets. runLaunchPath sets it from the app; it is nil
// everywhere else, so `wt start`, `wt smoke` and every other caller of the
// catalog see no profile row. Like allowReplace it is CLI mode state, not a
// parameter.
var launchSlotChecks []profiles.SlotCheck

// slotPin is the error resolveModel returns for a -M pin that names a slot
// profile whose row is not blocked. It is not a failure: the caller
// (launchFilteredImpl) launches row through launchSlotProfile. A sentinel
// error, as errCommandAgent is, because a profile is not a config.Model and
// resolveModel returns one.
type slotPin struct{ row catalog.Row }

func (p *slotPin) Error() string { return "model pin " + p.row.Model.ID + " is a slot profile" }

// Seams over the engine a profile launch drives. cmd/wt's TestMain makes each
// fail with "<name> not stubbed in this test", so no test writes the real
// config.yaml, restarts the real proxy or starts a real model; tests call
// stubSlotSeams.
var (
	ensureSlotRoutes = lifecycle.EnsureModelRoutes
	restartProxyNow  = lifecycle.RestartProxy
	probeProxyNow    = lifecycle.ProbeProxy
	settleRoutesNow  = lifecycle.SettleRoutes
	startSlotModel   = startForLaunchOwed
	buildSlotCmd     = agents.BuildSlotLaunchCmd
)
```

In `wt/cmd/wt/testmain_test.go`, add two imports to the import block: `"os/exec"` after `"os"`, and `"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"` after the `ollamacheck` line. Then, inside `TestMain`, add this directly after the line `ensureModelRoute = func(*config.Config, config.Model) bool { return false }`:

```go
	// The slot-profile launch seams (slot_launch.go): unstubbed they would
	// write the developer's real config.yaml, restart their proxy, start a
	// real model or run a real agent binary. Tests call stubSlotSeams.
	// settleRoutesNow returns nothing, so its fail-closed form is a panic.
	ensureSlotRoutes = func(context.Context, io.Writer, *config.Config, []config.Model, bool) (bool, error) {
		return false, errors.New("ensureSlotRoutes not stubbed in this test")
	}
	restartProxyNow = func(context.Context, *config.Config) error {
		return errors.New("restartProxyNow not stubbed in this test")
	}
	probeProxyNow = func(context.Context, *config.Config) error {
		return errors.New("probeProxyNow not stubbed in this test")
	}
	settleRoutesNow = func(context.Context, *config.Config) {
		panic("settleRoutesNow not stubbed in this test")
	}
	startSlotModel = func(*config.Config, catalog.Row, bool, bool) (bool, error) {
		return false, errors.New("startSlotModel not stubbed in this test")
	}
	buildSlotCmd = func(profiles.SlotLaunch, string, bool, *config.Config, []string) (*exec.Cmd, error) {
		return nil, errors.New("buildSlotCmd not stubbed in this test")
	}
```

In `wt/cmd/wt/resolve.go`, make three edits inside `resolveModel`.

Edit 1. Replace the `catalog.Build` call with:

```go
	rows := catalog.Build(catalog.Input{
		Config: cfg, Agent: agent, Models: eligible, Inventory: &snap,
		// Discovered rows carry no tags, so when -T/-F are active they are
		// dropped — the same rule the picker applies (app.go's
		// hideDiscovered), so rotation can never land on a model the
		// filter cannot describe.
		HideDiscovered: tags != "" || family != "",
		// Slot profiles are pin targets only: launchableModels leaves their
		// rows out, so neither the single-model auto-launch nor rotation can
		// return one. nil outside runLaunchPath.
		SlotProfiles: launchSlotChecks,
	})
```

Edit 2. In the pin branch, directly after the `pickerBlockedReason` check and before `switch row.Action() {`, add:

```go
		// A slot profile is its own launch unit: nothing is started here.
		// The caller runs launchSlotProfile, which writes the cloud slots'
		// routes first and then starts the local model, so one proxy restart
		// publishes both. A blocked profile never reaches this line: the
		// check above returned its reason.
		if row.IsProfile() {
			return config.Model{}, launchable, &slotPin{row: row}
		}
```

Edit 3. Replace the block that starts `if len(launchable) == 0 {` and ends at its closing brace with:

```go
	if len(launchable) == 0 {
		// Profile rows are pin targets only. Without a pin the wording below
		// is chosen from the model rows alone, so it is what it was before
		// slot profiles existed.
		modelRows := 0
		for _, r := range rows {
			if !r.IsProfile() {
				modelRows++
			}
		}
		if modelRows == 0 && len(eligible) > 0 {
			// Every eligible model is a local one with no row (#179 Phase B):
			// the filters matched, the models just are not on disk or running.
			return config.Model{}, nil, errors.New(catalog.NoRowsReason(agent))
		}
		if modelRows == 0 {
			return config.Model{}, nil, fmt.Errorf("no models match agent %q with tags %q and family %q", agent, tags, family)
		}
		// Models matched; none is usable without starting one. Say which
		// fix applies instead of the generic wording, which would send the
		// operator hunting for a -T/-F/config problem that isn't there.
		return config.Model{}, launchable, fmt.Errorf(
			"no cloud or running local model for agent %q — start one with `wt -M <id>`", agent)
	}
```

Also extend `resolveModel`'s doc comment. After the bullet that starts `//   - pinned != "" → the pin's row decides`, and its continuation lines, add:

```go
//   - pinned names a slot profile ("profile/<name>") whose row is not
//     blocked → a *slotPin carrying the row; nothing is started here.
```

- [ ] **Step 14: Run the tests and see them pass**

```bash
cd wt && go test ./cmd/wt -run 'TestResolveModel' -v
```

Expected: `--- PASS` for `TestResolveModelSlotProfilePin` (six subtests), `TestResolveModelWithoutAPinIgnoresSlotProfiles` (three subtests) and every existing `TestResolveModel…` test, then `ok`.

- [ ] **Step 15: Commit**

```bash
git add wt/cmd/wt/slot_launch.go wt/cmd/wt/slot_launch_test.go wt/cmd/wt/resolve.go wt/cmd/wt/resolve_test.go wt/cmd/wt/testmain_test.go
git commit -m "feat(wt): resolve a -M profile/<name> pin to its slot-profile row"
```

---

#### Behaviour 4: `applySlotProfile`, the fail-closed applier

- [ ] **Step 16: Write the failing tests**

In `wt/cmd/wt/slot_launch_test.go`, replace the import block with:

```go
import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)
```

Append this to the end of the file:

```go
// slotLaunchEnv gives one test its own config home and worktree. It returns
// wt's state directory inside that home (where usage.jsonl, rotation.state,
// last-picked.state and refcount.jsonl would be written) and the worktree.
func slotLaunchEnv(t *testing.T) (stateDir, worktree string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	return filepath.Join(home, "agent-wt"), t.TempDir()
}

// slotRow resolves check against cfg with no inventory probe, which is enough
// for an all-cloud profile.
func slotRow(cfg *config.Config, check profiles.SlotCheck) catalog.Row {
	return catalog.ResolveSlotProfile(cfg, nil, check)
}

// TestApplySlotProfileAppliesArgsAndWrapper verifies the applier applies the
// two mechanisms the slot build does not deliver: the profile's args are
// appended to the command, and its wrapper then wraps the whole command line.
// Claude allows neither today; an agent that does must not lose them.
func TestApplySlotProfileAppliesArgsAndWrapper(t *testing.T) {
	_, worktree := slotLaunchEnv(t)
	fakeAgentOnPath(t, "wt-test-wrapper")
	check := slotFixtureCheck(0, "cloud", "opus", slotCloud())
	check.Profile.Args = []string{"--effort", "high"}
	check.Profile.Wrapper = &profiles.WrapperSpec{Binary: "wt-test-wrapper", ArgsTemplate: []string{"run", "{{args}}"}}
	check.Profile.Env = map[string]string{"FROM_PROFILE": "1"}
	check.Profile.ConfigContent = map[string]any{"x": "1"}
	row := slotRow(slotFixtureConfig(), check)
	cmd := exec.Command("true", "--model", "opus")
	cmd.Dir = worktree

	cleanup, err := applySlotProfile(cmd, "claude", row)
	if err != nil {
		t.Fatalf("applySlotProfile() error = %v", err)
	}
	if cleanup == nil || cleanup() != nil {
		t.Error("cleanup must be a non-nil no-op")
	}
	if got, want := strings.Join(cmd.Args[1:], " "), "run --model opus --effort high"; got != want {
		t.Errorf("args = %q, want %q", got, want)
	}
	if filepath.Base(cmd.Path) != "wt-test-wrapper" {
		t.Errorf("cmd.Path = %q, want the wrapper binary", cmd.Path)
	}
	// env and config_content are the slot build's to deliver, not the
	// applier's: applied here too they would be set twice, once as a file.
	if slices.Contains(cmd.Env, "FROM_PROFILE=1") {
		t.Errorf("cmd.Env = %v: the applier must not apply the profile's env", cmd.Env)
	}
	if _, statErr := os.Stat(filepath.Join(worktree, ".claude")); !os.IsNotExist(statErr) {
		t.Error("the applier wrote under .claude in the worktree")
	}
}

// TestApplySlotProfileFailsClosed verifies an apply error is returned, not
// swallowed. The automatic-profile applier degrades to an unprofiled launch
// on a config error; for a slot profile that would start Claude Code without
// what the profile asked for, so the launch must stop.
func TestApplySlotProfileFailsClosed(t *testing.T) {
	_, worktree := slotLaunchEnv(t)
	check := slotFixtureCheck(0, "cloud", "opus", slotCloud())
	check.Profile.Wrapper = &profiles.WrapperSpec{Binary: "wt-test-definitely-missing-binary-xyz", ArgsTemplate: []string{"{{args}}"}}
	cmd := exec.Command("true")
	cmd.Dir = worktree

	cleanup, err := applySlotProfile(cmd, "claude", slotRow(slotFixtureConfig(), check))
	if err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("err = %v, want the wrapper-not-installed error", err)
	}
	if cleanup == nil {
		t.Error("cleanup must not be nil, even on an error")
	}

	if _, err := applySlotProfile(exec.Command("true"), "claude", catalog.Row{Model: config.Model{ID: "openrouter/a"}}); err == nil {
		t.Error("a model row was accepted: the applier must refuse a row that is not a slot profile")
	}
}

// TestApplySlotProfileSelfHealsALeftoverSettingsFile verifies the self-heal
// of a settings.local.json left behind by an earlier automatic-profile
// session still runs before a profile launch. Without it a profile session
// would start with that stale file in the worktree, and its env block could
// redirect the session.
func TestApplySlotProfileSelfHealsALeftoverSettingsFile(t *testing.T) {
	_, worktree := slotLaunchEnv(t)
	target := filepath.Join(worktree, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(`{"hooks":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	rp := profiles.ResolvedProfile{ConfigContent: map[string]any{"env": map[string]any{"X": "1"}}}
	if _, err := profiles.ApplyConfigContent(exec.Command("claude"), "claude", worktree, rp); err != nil {
		t.Fatalf("seed an orphaned backup: %v", err)
	}
	cmd := exec.Command("true")
	cmd.Dir = worktree
	row := slotRow(slotFixtureConfig(), slotFixtureCheck(0, "cloud", "opus", slotCloud()))

	var err error
	stderr := captureFile(t, &os.Stderr, func() {
		_, err = applySlotProfile(cmd, "claude", row)
	})
	if err != nil {
		t.Fatalf("applySlotProfile() error = %v", err)
	}
	if !strings.Contains(stderr, "wt: restored a leftover profile-managed file from a previous session: "+target) {
		t.Errorf("stderr = %q, want the self-heal notice", stderr)
	}
	// The restore rewrites the file's JSON, so compare what it holds, not
	// its bytes: the user's own key is back and the old profile's is gone.
	data, rerr := os.ReadFile(target)
	if rerr != nil || !strings.Contains(string(data), `"hooks"`) || strings.Contains(string(data), `"env"`) {
		t.Errorf("target = %q (%v), want the user's hooks kept and the old profile's env block removed", data, rerr)
	}
}

// TestSlotProfileWritesNoSettingsFile verifies a Claude profile with
// config_content, run through the real slot build and the applier, writes
// nothing: no .claude directory in the worktree, and nothing in the config
// home except the self-heal check's lock file. config_content travels in the
// inline --settings argument, so an interrupted session leaves no proxy-only
// ids behind for a later bare `claude` run. The proxy key stays out of argv.
func TestSlotProfileWritesNoSettingsFile(t *testing.T) {
	stateDir, worktree := slotLaunchEnv(t)
	fakeAgentOnPath(t, "claude")
	cfg := slotFixtureConfig()
	check := slotFixtureCheck(0, "cloud", "opus", slotCloud())
	check.Profile.Env = map[string]string{"MAX_THINKING_TOKENS": "4096"}
	check.Profile.ConfigContent = map[string]any{"modelOverrides": map[string]any{"claude-sonnet-4-6": "openrouter/b"}}
	row := slotRow(cfg, check)
	if row.Action() != catalog.ActionLaunch {
		t.Fatalf("fixture: the row is not a launch row: %s", row.BlockReason())
	}

	cmd, err := agents.BuildSlotLaunchCmd(row.Profile.Launch(), worktree, false, cfg, nil)
	if err != nil {
		t.Fatalf("BuildSlotLaunchCmd() error = %v", err)
	}
	cleanup, err := applySlotProfile(cmd, "claude", row)
	if err != nil {
		t.Fatalf("applySlotProfile() error = %v", err)
	}
	if cerr := cleanup(); cerr != nil {
		t.Fatalf("cleanup() error = %v", cerr)
	}

	i := slices.Index(cmd.Args, "--settings")
	if i < 0 || i+1 >= len(cmd.Args) || !strings.Contains(cmd.Args[i+1], "modelOverrides") {
		t.Errorf("args = %q, want config_content inside the --settings value", cmd.Args)
	}
	for _, a := range cmd.Args {
		if strings.Contains(a, "sk-test-1234") {
			t.Errorf("the proxy key is in argv: %q", a)
		}
	}
	if _, statErr := os.Stat(filepath.Join(worktree, ".claude")); !os.IsNotExist(statErr) {
		t.Error("a .claude directory exists in the worktree: a settings file was written")
	}
	entries, _ := os.ReadDir(worktree)
	if len(entries) != 0 {
		t.Errorf("the worktree holds %d entries, want none", len(entries))
	}
	// The one thing the applier may leave in the config home is the lock file
	// of the self-heal check (profile-backups/<key>.lock). A backup marker
	// there would mean a file was rewritten.
	_ = filepath.WalkDir(filepath.Dir(stateDir), func(path string, d os.DirEntry, werr error) error {
		if werr != nil || d.IsDir() {
			return nil
		}
		if filepath.Base(filepath.Dir(path)) == "profile-backups" && strings.HasSuffix(path, ".lock") {
			return nil
		}
		t.Errorf("the config home gained a file: %s", path)
		return nil
	})
}
```

- [ ] **Step 17: Run them and see them fail**

```bash
cd wt && go test ./cmd/wt -run 'TestApplySlotProfile|TestSlotProfileWritesNoSettingsFile' -v
```

Expected: the build fails with `undefined: applySlotProfile`, then `FAIL	github.com/ohanaverse/local-ai-setup/wt/cmd/wt [build failed]`.

- [ ] **Step 18: Write the implementation**

In `wt/cmd/wt/slot_launch.go`, replace the import block with:

```go
import (
	"fmt"
	"os/exec"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)
```

Append to the end of the file:

```go
// applySlotProfile is the fail-closed applier of a slot-profile launch. It
// runs the self-heal of a settings file left by an earlier automatic-profile
// session, then applies a hand-built profiles.ResolvedProfile that holds only
// ExtraArgs (the profile's args) and Wrapper. Env and ConfigContent stay
// empty: the agent's slot build delivered them (for claude, inside the inline
// --settings JSON), so nothing is written to disk here.
//
// It is not applyResolvedProfile: that one degrades to an unprofiled launch on
// an error, and a slot profile that cannot be fully applied must not launch.
// It never prompts, never reads profiles.toml and never resolves an automatic
// profile. An ApplyWrapper error is returned and aborts the launch. cleanup is
// always a non-nil no-op. Its signature is tui.SlotProfileApplier's, so the
// TUI is handed this same function.
func applySlotProfile(cmd *exec.Cmd, agent string, row catalog.Row) (cleanup func() error, err error) {
	noop := func() error { return nil }
	if row.Profile == nil {
		return noop, fmt.Errorf("%s is not a slot profile", row.Model.ID)
	}
	selfHealAgentConfigContentTarget(agent, cmd.Dir)
	sp := row.Profile.Check.Profile
	rp := profiles.ResolvedProfile{ExtraArgs: sp.Args, Wrapper: sp.Wrapper}
	profiles.ApplyEnvAndArgs(cmd, rp)
	if rp.Wrapper != nil {
		if werr := profiles.ApplyWrapper(cmd, rp.Wrapper); werr != nil {
			return noop, werr
		}
	}
	return noop, nil
}
```

- [ ] **Step 19: Run the tests and see them pass**

```bash
cd wt && go test ./cmd/wt -run 'TestApplySlotProfile|TestSlotProfileWritesNoSettingsFile' -v
```

Expected: `--- PASS` for `TestApplySlotProfileAppliesArgsAndWrapper`, `TestApplySlotProfileFailsClosed`, `TestApplySlotProfileSelfHealsALeftoverSettingsFile` and `TestSlotProfileWritesNoSettingsFile`, then `ok`.

**If check 1 failed** (spec, "Check results"): `--settings` then carries only `config_content`, which this profile sets, so `TestSlotProfileWritesNoSettingsFile` needs no change.

---

#### Behaviour 5: `runSlotAgentCmd`, the run and the post-exit flow

- [ ] **Step 20: Write the failing test**

Append to the end of `wt/cmd/wt/slot_launch_test.go`:

```go
// TestRunSlotAgentCmdPostExitFlow verifies what follows a profile session:
// the refcount lines are released, then the stop picker runs, then the
// summary line names the profile and the duration. There is no per-model
// price notice and no survey, since neither belongs to one model here. A
// stop picker that ran before the release would never offer the profile's
// own local model.
func TestRunSlotAgentCmdPostExitFlow(t *testing.T) {
	slotLaunchEnv(t)
	prevNotice, prevRelease, prevPicker := emitPriceNotice, releaseSession, runStopPicker
	t.Cleanup(func() { emitPriceNotice, releaseSession, runStopPicker = prevNotice, prevRelease, prevPicker })
	var order []string
	releaseSession = func() { order = append(order, "release") }
	runStopPicker = func(*config.Config) {
		order = append(order, "picker")
		fmt.Fprint(os.Stdout, "PICKER-MARKER\n")
	}
	emitPriceNotice = func(*config.Config) { order = append(order, "notice") }
	cfg := slotFixtureConfig()
	row := slotRow(cfg, slotFixtureCheck(0, "cloud", "opus", slotCloud()))
	cleaned := false

	var err error
	stdout := captureFile(t, &os.Stdout, func() {
		err = runSlotAgentCmd(exec.Command("true"), "claude", row, cfg, func() error { cleaned = true; return nil })
	})

	if err != nil {
		t.Fatalf("runSlotAgentCmd() error = %v", err)
	}
	if !cleaned {
		t.Error("the applier's cleanup was not called after the run")
	}
	if got := strings.Join(order, ","); got != "release,picker" {
		t.Errorf("order = %s, want release,picker (and no price notice)", got)
	}
	i, j := strings.Index(stdout, "PICKER-MARKER"), strings.Index(stdout, "\nwt: claude · profile/cloud · ")
	if i < 0 || j < 0 || i > j {
		t.Errorf("stdout = %q, want the stop picker before a summary line naming the profile", stdout)
	}
	if strings.Contains(stdout, "survey ·") {
		t.Errorf("stdout = %q, want no survey after a profile session", stdout)
	}
}

// TestRunSlotAgentCmdReturnsARunError verifies an agent that could not be
// run at all comes back as an error after the post-exit flow has run, so the
// refcount lines recorded for the launch are not left behind.
func TestRunSlotAgentCmdReturnsARunError(t *testing.T) {
	slotLaunchEnv(t)
	prevRelease := releaseSession
	t.Cleanup(func() { releaseSession = prevRelease })
	released := false
	releaseSession = func() { released = true }
	cfg := slotFixtureConfig()
	row := slotRow(cfg, slotFixtureCheck(0, "cloud", "opus", slotCloud()))

	var err error
	captureFile(t, &os.Stdout, func() {
		err = runSlotAgentCmd(exec.Command(filepath.Join(t.TempDir(), "no-such-agent")), "claude", row, cfg, func() error { return nil })
	})
	if err == nil {
		t.Fatal("err = nil, want the run error")
	}
	if !released {
		t.Error("the session's refcount lines were not released after a failed run")
	}
}
```

- [ ] **Step 21: Run it and see it fail**

```bash
cd wt && go test ./cmd/wt -run TestRunSlotAgentCmd -v
```

Expected: the build fails with `undefined: runSlotAgentCmd`, then `FAIL	github.com/ohanaverse/local-ai-setup/wt/cmd/wt [build failed]`.

- [ ] **Step 22: Write the implementation**

In `wt/cmd/wt/slot_launch.go`, replace the import block with:

```go
import (
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)
```

Append to the end of the file:

```go
// runSlotAgentCmd runs the built command of a slot-profile launch and then
// the post-exit flow of a profile session: release this session's refcount
// lines, run the stop picker (not filtered to the profile: it offers every
// running local model no live session uses), print the summary line naming
// the profile. There is no survey and no price notice: both are per model,
// and a profile session used several. It ends as runAgentCmd does, by
// propagating the agent's exit code.
//
// It never calls applyProfileForLaunch: automatic profiles are not applied to
// a slot-profile launch. cleanup is the applier's and must not be nil.
func runSlotAgentCmd(cmd *exec.Cmd, agent string, row catalog.Row, cfg *config.Config, cleanup func() error) error {
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	duration, err := agents.RunAndCleanup(cmd, cleanup)

	releaseSession()
	runStopPicker(cfg)
	// Leading "\n" for the same reason as runAgentCmd's: the agent's last
	// byte may not be a newline.
	fmt.Println("\n" + agents.Summary(agent, config.Model{ID: row.Model.ID}, duration))
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			// This os.Exit bypasses main's own wait, and the stop picker just
			// above may have started an async proxy restart.
			lifecycle.WaitPendingRoutes()
			os.Exit(ee.ExitCode())
		}
		return err
	}
	return nil
}
```

- [ ] **Step 23: Run the tests and see them pass**

```bash
cd wt && go test ./cmd/wt -run TestRunSlotAgentCmd -v
```

Expected: `--- PASS` for `TestRunSlotAgentCmdPostExitFlow` and `TestRunSlotAgentCmdReturnsARunError`, then `ok`.

- [ ] **Step 24: Commit**

```bash
git add wt/cmd/wt/slot_launch.go wt/cmd/wt/slot_launch_test.go
git commit -m "feat(wt): the fail-closed slot-profile applier and the profile post-exit flow"
```

---

#### Behaviour 6: `launchSlotProfile`, the ten steps, reached from `launchFilteredImpl`

- [ ] **Step 25: Write the failing tests**

In `wt/cmd/wt/slot_launch_test.go`, replace the import block with:

```go
import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
	"github.com/ohanaverse/local-ai-setup/wt/internal/refcount"
	"github.com/ohanaverse/local-ai-setup/wt/internal/usage"
)
```

Append to the end of the file:

```go
// runningQ is an inventory in which the fixture's local model is running.
func runningQ() localmodels.Snapshot {
	return localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "omlx", ModelID: "omlx/q", Artifact: "q", ModelName: "q", Registered: true, Running: true, ArtifactKnown: true},
		},
	}
}

// pinSlotProfile runs the non-TUI launch of `-A claude -M <pin>` with extra
// passthrough args, as launchFiltered runs it.
func pinSlotProfile(worktree string, cfg *config.Config, pin string, extraArgs ...string) error {
	return launchFiltered("claude", worktree, cfg, false, "", "", pin, true, extraArgs, nil, nil)
}

// slotStateFiles are the files a recorded launch writes under wt's state
// directory.
var slotStateFiles = []string{"usage.jsonl", "rotation.state", "last-picked.state", "refcount.jsonl"}

// TestLaunchSlotProfileSteps verifies the order of the launch flow and its
// restart count for each shape of profile. The cloud slots' routes are
// ensured first. An idle local model is then started and told whether a
// restart is owed, so its own restart publishes the cloud routes; a running
// one only has its route ensured. The proxy is restarted once when a write
// is still owed and never otherwise, and it is probed on every launch before
// the command is built. Two restarts would cost the user 20 to 40 seconds; a
// missing one would start Claude Code against a proxy that does not know the
// slot's id.
func TestLaunchSlotProfileSteps(t *testing.T) {
	cases := []struct {
		name       string
		slots      map[string]string
		running    bool // the local model is already running
		ensureOwed bool // a route write changed config.yaml
		want       string
	}{
		{"all cloud, every route exists", slotCloud(), false, false,
			"ensure:openrouter/a,openrouter/b;probe;build"},
		{"all cloud, a route was missing", slotCloud(), false, true,
			"ensure:openrouter/a,openrouter/b;restart;probe;build"},
		{"idle local model, a cloud route was missing", slotMixed(), false, true,
			"ensure:openrouter/a;start:omlx/q:owed=true;probe;build"},
		{"idle local model, routes in place", slotMixed(), false, false,
			"ensure:openrouter/a;start:omlx/q:owed=false;probe;build"},
		{"running local model, routes in place", slotMixed(), true, false,
			"ensure:openrouter/a;ensure:omlx/q;probe;build"},
		{"running local model, a route was missing", slotMixed(), true, true,
			"ensure:openrouter/a;ensure:omlx/q;restart;probe;build"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, worktree := slotLaunchEnv(t)
			if tc.running {
				stubProbeInventory(t, runningQ())
			}
			seams := stubSlotSeams(t)
			seams.ensureOwed = tc.ensureOwed
			sentinel := filepath.Join(t.TempDir(), "agent-ran")
			seams.cmd = exec.Command("touch", sentinel)
			stubSlotChecks(t, slotFixtureCheck(0, "p", "opus", tc.slots))

			var err error
			captureFile(t, &os.Stdout, func() {
				err = pinSlotProfile(worktree, slotFixtureConfig(), "profile/p")
			})

			if err != nil {
				t.Fatalf("launch error = %v", err)
			}
			if got := seams.log(); got != tc.want {
				t.Errorf("steps = %s\n want  %s", got, tc.want)
			}
			if n := strings.Count(seams.log(), "restart"); n > 1 {
				t.Errorf("%d proxy restarts, want at most one", n)
			}
			if _, statErr := os.Stat(sentinel); statErr != nil {
				t.Error("the agent command did not run")
			}
		})
	}
}

// TestLaunchSlotProfileRecordsTheProfile verifies the bookkeeping of a profile
// launch: one usage event under the profile's id, last-picked.state naming
// the profile, rotation.state byte-identical, and one refcount line per
// distinct slot model (a model filling two slots is counted once). The
// profile then shows its launches in the picker and in `wt stats`, the
// rotation of single-model launches is not disturbed, and another session's
// stop picker does not offer a model this session uses.
func TestLaunchSlotProfileRecordsTheProfile(t *testing.T) {
	stateDir, worktree := slotLaunchEnv(t)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	rotationState := filepath.Join(stateDir, "rotation.state")
	if err := os.WriteFile(rotationState, []byte("openrouter/b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stubSlotSeams(t)
	stubSlotChecks(t, slotFixtureCheck(0, "cloud", "opus",
		map[string]string{"opus": "openrouter/a", "sonnet": "openrouter/a", "haiku": "openrouter/b"}))

	var err error
	captureFile(t, &os.Stdout, func() {
		err = pinSlotProfile(worktree, slotFixtureConfig(), "profile/cloud")
	})
	if err != nil {
		t.Fatalf("launch error = %v", err)
	}

	counts := usage.NewStoreAt(stateDir).CountsForAgent("claude", []string{"profile/cloud", "openrouter/a"})
	if counts["profile/cloud"].ThirtyDay != 1 || counts["openrouter/a"].ThirtyDay != 0 {
		t.Errorf("usage = %+v, want one launch under profile/cloud and none under a slot model", counts)
	}
	if data, _ := os.ReadFile(filepath.Join(stateDir, "last-picked.state")); string(data) != "profile/cloud\n" {
		t.Errorf("last-picked.state = %q, want %q", data, "profile/cloud\n")
	}
	if data, _ := os.ReadFile(rotationState); string(data) != "openrouter/b\n" {
		t.Errorf("rotation.state = %q, want it byte-identical (%q)", data, "openrouter/b\n")
	}
	refs := refcount.NewStoreAt(stateDir).Counts([]string{"openrouter/a", "openrouter/b", "profile/cloud"})
	if refs["openrouter/a"] != 1 || refs["openrouter/b"] != 1 || refs["profile/cloud"] != 0 {
		t.Errorf("refcounts = %v, want one line per distinct slot model and none under the profile id", refs)
	}
}

// TestSlotProfileFailuresRunNoAgent is the fail-closed table: each row of the
// spec's failure table ends with an error, no agent command run, and nothing
// recorded. A failure that degraded to a launch would start Claude Code with
// some slots still sending Anthropic ids, which the proxy rejects.
func TestSlotProfileFailuresRunNoAgent(t *testing.T) {
	one := func(c profiles.SlotCheck) func() []profiles.SlotCheck {
		return func() []profiles.SlotCheck { return []profiles.SlotCheck{c} }
	}
	cloud := func() profiles.SlotCheck { return slotFixtureCheck(0, "p", "opus", slotCloud()) }
	mixed := func() profiles.SlotCheck { return slotFixtureCheck(0, "p", "opus", slotMixed()) }
	notEligible := `model "profile/p" is not in the eligible list for agent "claude"`
	cases := []struct {
		name   string
		checks func() []profiles.SlotCheck // nil: no profile loaded
		cfg    func(*config.Config)
		seams  func(*slotSeams)
		args   []string
		want   string // a substring of the error
		steps  string // the engine steps that ran before the abort
	}{
		{name: "slot-profiles.toml does not parse", want: notEligible},
		{name: "a profile has no usable name",
			checks: func() []profiles.SlotCheck {
				return []profiles.SlotCheck{{Profile: profiles.SlotProfile{Agent: "claude"}, Err: errors.New("name is missing")}}
			},
			want: notEligible},
		{name: "two profiles share a name",
			checks: func() []profiles.SlotCheck {
				a, b := cloud(), cloud()
				b.Index = 1
				a.Duplicate, b.Duplicate = true, true
				a.Err, b.Err = profiles.ErrSlotDuplicate, profiles.ErrSlotDuplicate
				return []profiles.SlotCheck{a, b}
			},
			want: "duplicate profile name"},
		{name: "a profile fails static validation",
			checks: func() []profiles.SlotCheck {
				c := cloud()
				c.Err = errors.New(`default "x" is not a slot this profile fills`)
				return []profiles.SlotCheck{c}
			},
			want: `default "x" is not a slot this profile fills`},
		{name: "a profile fails resolved validation",
			checks: one(slotFixtureCheck(0, "p", "opus", map[string]string{"opus": "openrouter/gone"})),
			want:   "slot opus: openrouter/gone is not in the registry"},
		{name: "the registry has a provider with id profile",
			checks: one(cloud()),
			cfg: func(c *config.Config) {
				c.Providers = append(c.Providers, config.Provider{ID: "profile", Location: config.LocationCloud})
			},
			want: notEligible},
		{name: "LiteLLM has no url",
			checks: one(cloud()),
			cfg:    func(c *config.Config) { c.SetLitellmForTest(config.LitellmState{APIKey: "sk-test-1234"}) },
			want:   "no URL is configured"},
		{name: "LiteLLM has no api key",
			checks: one(cloud()),
			cfg:    func(c *config.Config) { c.SetLitellmForTest(config.LitellmState{URL: "http://localhost:4000"}) },
			want:   "no API key is configured"},
		{name: "passthrough args contain --settings",
			checks: one(cloud()), args: []string{"--settings", "x.json"},
			want: "a slot profile sets --settings itself"},
		{name: "passthrough args contain --model=",
			checks: one(cloud()), args: []string{"--model=opus"},
			want: "a slot profile sets --model itself"},
		{name: "a slot's route cannot be written",
			checks: one(cloud()),
			seams: func(s *slotSeams) {
				s.ensureErr = &lifecycle.RouteEnsureError{ModelID: "openrouter/a", Err: litellm.ErrMissing}
			},
			want:  "slot opus: LiteLLM route for openrouter/a not written: LiteLLM config not found",
			steps: "ensure:openrouter/a,openrouter/b"},
		{name: "the local model's start is refused with a restart owed",
			checks: one(mixed()),
			seams: func(s *slotSeams) {
				s.ensureOwed, s.startStill = true, true
				s.startErr = errors.New("cancelled — omlx/other still running")
			},
			want:  "cancelled — omlx/other still running",
			steps: "ensure:openrouter/a;start:omlx/q:owed=true;settle"},
		{name: "the local model's start fails",
			checks: one(mixed()),
			seams:  func(s *slotSeams) { s.startErr = errors.New("failed to start omlx/q: server exited") },
			want:   "failed to start omlx/q",
			steps:  "ensure:openrouter/a;start:omlx/q:owed=false"},
		{name: "the proxy restart fails",
			checks: one(cloud()),
			seams: func(s *slotSeams) {
				s.ensureOwed = true
				s.restartErr = errors.New("LiteLLM proxy not ready at http://localhost:4000: timed out")
			},
			want:  "LiteLLM proxy not ready",
			steps: "ensure:openrouter/a,openrouter/b;restart"},
		{name: "the proxy does not answer the probe",
			checks: one(cloud()),
			seams: func(s *slotSeams) {
				s.probeErr = errors.New("the LiteLLM proxy does not answer at http://localhost:4000: connection refused")
			},
			want:  "does not answer at http://localhost:4000",
			steps: "ensure:openrouter/a,openrouter/b;probe"},
		{name: "the command cannot be built",
			checks: one(cloud()),
			seams:  func(s *slotSeams) { s.buildErr = errors.New("agent claude not installed") },
			want:   "agent claude not installed",
			steps:  "ensure:openrouter/a,openrouter/b;probe;build"},
		{name: "the wrapper cannot be applied",
			checks: func() []profiles.SlotCheck {
				c := cloud()
				c.Profile.Wrapper = &profiles.WrapperSpec{Binary: "wt-test-definitely-missing-binary-xyz", ArgsTemplate: []string{"{{args}}"}}
				return []profiles.SlotCheck{c}
			},
			want:  "not installed",
			steps: "ensure:openrouter/a,openrouter/b;probe;build"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stateDir, worktree := slotLaunchEnv(t)
			seams := stubSlotSeams(t)
			sentinel := filepath.Join(t.TempDir(), "agent-ran")
			seams.cmd = exec.Command("touch", sentinel)
			if tc.seams != nil {
				tc.seams(seams)
			}
			if tc.checks != nil {
				stubSlotChecks(t, tc.checks()...)
			} else {
				stubSlotChecks(t)
			}
			cfg := slotFixtureConfig()
			if tc.cfg != nil {
				tc.cfg(cfg)
			}

			var err error
			stdout := captureFile(t, &os.Stdout, func() {
				err = pinSlotProfile(worktree, cfg, "profile/p", tc.args...)
			})

			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
			if _, statErr := os.Stat(sentinel); !os.IsNotExist(statErr) {
				t.Error("the agent command ran after a failure")
			}
			if got := seams.log(); got != tc.steps {
				t.Errorf("steps = %q, want %q", got, tc.steps)
			}
			if strings.Contains(stdout, "wt: claude ·") {
				t.Errorf("stdout = %q, want no summary line: no session ran", stdout)
			}
			for _, name := range slotStateFiles {
				if _, statErr := os.Stat(filepath.Join(stateDir, name)); !os.IsNotExist(statErr) {
					t.Errorf("%s was written although the launch aborted", name)
				}
			}
		})
	}
}

// TestSlotProfileRouteErrorKeepsItsCause verifies the abort for a route that
// could not be written names the slot and still carries the cause on its
// chain, so a caller can tell a missing config.yaml from a lock timeout.
func TestSlotProfileRouteErrorKeepsItsCause(t *testing.T) {
	_, worktree := slotLaunchEnv(t)
	seams := stubSlotSeams(t)
	seams.ensureErr = &lifecycle.RouteEnsureError{ModelID: "openrouter/b", Err: litellm.ErrMissing}
	stubSlotChecks(t, slotFixtureCheck(0, "p", "opus", slotCloud()))

	err := pinSlotProfile(worktree, slotFixtureConfig(), "profile/p")
	if err == nil || !strings.HasPrefix(err.Error(), "slot haiku: ") {
		t.Fatalf("err = %v, want it to start with the slot that names openrouter/b", err)
	}
	if !errors.Is(err, litellm.ErrMissing) {
		t.Errorf("err = %v, want litellm.ErrMissing still on the chain", err)
	}
}

// TestSlotProfileStartErrorIsAStartError verifies a failed or refused start of
// the profile's local model is marked as a start's outcome. The root command
// reads that mark to print the message once; without it the message and the
// server's log tail would be printed twice.
func TestSlotProfileStartErrorIsAStartError(t *testing.T) {
	_, worktree := slotLaunchEnv(t)
	seams := stubSlotSeams(t)
	seams.startErr = errors.New("failed to start omlx/q: server exited")
	stubSlotChecks(t, slotFixtureCheck(0, "p", "opus", slotMixed()))

	err := pinSlotProfile(worktree, slotFixtureConfig(), "profile/p")
	if !isStartError(err) {
		t.Errorf("err = %v, want it marked as a start error", err)
	}
}

// TestSlotProfileLaunchSkipsAutomaticProfiles verifies a profile launch never
// resolves, prompts for or applies an automatic profile, even when
// profiles.toml has an entry that matches one of the slot models. The two
// kinds of profile do not stack: an automatic env or settings file applied on
// top could redirect a slot.
func TestSlotProfileLaunchSkipsAutomaticProfiles(t *testing.T) {
	stateDir, worktree := slotLaunchEnv(t)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "[[profiles]]\nagent = \"claude\"\nmatch = \"provider\"\nprovider = \"openrouter\"\nenv = { WT_TEST_AUTO_PROFILE = \"1\" }\n"
	if err := os.WriteFile(filepath.Join(stateDir, "profiles.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := slotFixtureConfig()
	store, err := profiles.Load()
	if err != nil {
		t.Fatalf("profiles.Load() error = %v", err)
	}
	if profiles.Resolve(store, "claude", cfg, cfg.Models[0]).Empty() {
		t.Fatal("fixture: the automatic profile does not match the slot model, so the test proves nothing")
	}
	loads, prompts := 0, 0
	oldLoad, oldConfirm := loadProfileStore, confirmProfile
	loadProfileStore = func() (profiles.Store, error) { loads++; return profiles.Load() }
	confirmProfile = func(profiles.ResolvedProfile) (bool, error) { prompts++; return true, nil }
	t.Cleanup(func() { loadProfileStore, confirmProfile = oldLoad, oldConfirm })
	seams := stubSlotSeams(t)
	cmd := exec.Command("true")
	seams.cmd = cmd
	stubSlotChecks(t, slotFixtureCheck(0, "cloud", "opus", slotCloud()))

	// The already-loaded store is handed in too, as runLaunchPath hands it to
	// every launch: the profile path must ignore it.
	pp := &precomputedProfiles{store: store}
	captureFile(t, &os.Stdout, func() {
		err = launchFiltered("claude", worktree, cfg, false, "", "", "profile/cloud", true, nil, nil, pp)
	})

	if err != nil {
		t.Fatalf("launch error = %v", err)
	}
	if loads != 0 || prompts != 0 {
		t.Errorf("profiles.toml loaded %d times and prompted %d times, want 0 and 0", loads, prompts)
	}
	if slices.Contains(cmd.Env, "WT_TEST_AUTO_PROFILE=1") {
		t.Errorf("cmd.Env = %v: the automatic profile was applied to a slot-profile launch", cmd.Env)
	}
}
```

- [ ] **Step 26: Run them and see them fail**

```bash
cd wt && go test ./cmd/wt -run 'TestLaunchSlotProfile|TestSlotProfile' -v
```

Expected: the package builds. `TestLaunchSlotProfileSteps`, `TestLaunchSlotProfileRecordsTheProfile`, `TestSlotProfileRouteErrorKeepsItsCause`, `TestSlotProfileStartErrorIsAStartError` and `TestSlotProfileLaunchSkipsAutomaticProfiles` fail with `launch error = model pin profile/p is a slot profile` (or the same text for `profile/cloud`) or an error mismatch naming that text, because `launchFilteredImpl` still returns the `*slotPin` as a plain error. In `TestSlotProfileFailuresRunNoAgent` the rows from "passthrough args contain --settings" on fail the same way; the rows before it pass.

- [ ] **Step 27: Write the implementation**

In `wt/cmd/wt/slot_launch.go`, replace the import block with:

```go
import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
	"github.com/ohanaverse/local-ai-setup/wt/internal/refcount"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rotation"
)
```

Insert this between the seam `var ( … )` block and `applySlotProfile`:

```go
// slotRouteError words a route that could not be ensured by the slot it
// fills: "slot haiku: LiteLLM route for <id> not written: <cause>". Any other
// error is returned as it is.
func slotRouteError(p *catalog.ProfileRow, err error) error {
	var re *lifecycle.RouteEnsureError
	if errors.As(err, &re) {
		return fmt.Errorf("slot %s: %w", p.SlotOf(re.ModelID), err)
	}
	return err
}

// launchSlotProfile is the whole launch of one slot profile on the non-TUI
// path: steps 1 to 10 of the spec's launch flow. row is the profile's catalog
// row as resolveModel found it.
//
// It fails closed. Every abort returns an error before the agent starts and
// before anything is recorded, and no failure degrades to an unprofiled
// launch. A local model started in step 4 is left running after a later
// abort. The endpoint is always the LiteLLM proxy: nothing here reads
// cfg.IsLitellm().
func launchSlotProfile(agent, worktreePath string, cfg *config.Config, yolo bool, row catalog.Row, extraArgs []string) error {
	if row.Profile == nil {
		return fmt.Errorf("%s is not a slot profile", row.Model.ID)
	}
	ctx := context.Background()

	// Step 1: re-resolve against fresh state. The row may be seconds old, and
	// its verdict decides whether a start runs.
	snap := probeInventory(cfg)
	row = catalog.ResolveSlotProfile(cfg, &snap, row.Profile.Check)
	p := row.Profile
	if row.Action() == catalog.ActionBlock {
		return errors.New(row.BlockReason())
	}

	// Step 2: a passthrough flag that would replace the slot mapping.
	if err := agents.CheckSlotPassthrough(agent, extraArgs); err != nil {
		return err
	}

	// Step 3: the cloud slots' missing routes, restart deferred. owed says a
	// write changed config.yaml and the proxy has not been restarted for it.
	owed, err := ensureSlotRoutes(ctx, nil, cfg, p.Cloud(), false)
	if err != nil {
		return slotRouteError(p, err)
	}

	switch {
	case row.Action() == catalog.ActionStart:
		// Step 4: start the local model. The start writes its route and
		// restarts the proxy itself; told a restart is owed, that one restart
		// publishes the cloud routes too.
		still, serr := startSlotModel(cfg, *p.Local, allowReplace, owed)
		if serr != nil {
			if still {
				// A refusal that was not retried touched nothing: the written
				// cloud routes are published here, so they are live for the
				// next attempt.
				settleRoutesNow(ctx, cfg)
			}
			return asStartError(serr)
		}
		owed = false
	case p.Local != nil:
		// Step 5: a local model that was already running gets its route.
		owed, err = ensureSlotRoutes(ctx, nil, cfg, []config.Model{p.Local.Model}, owed)
		if err != nil {
			return slotRouteError(p, err)
		}
	}

	// Step 6: one restart, only when a write is still owed.
	if owed {
		fmt.Fprintf(osStderr, "wt: restarting the LiteLLM proxy for %s\n", row.Model.ID)
		if err := restartProxyNow(ctx, cfg); err != nil {
			return err
		}
	}

	// Step 7: the proxy must answer, on every launch.
	if err := probeProxyNow(ctx, cfg); err != nil {
		return err
	}

	// Step 8: the command, through the agent's slot build.
	cmd, err := buildSlotCmd(p.Launch(), worktreePath, yolo, cfg, extraArgs)
	if err != nil {
		return err
	}
	if wd, werr := shellDir(); werr == nil {
		for _, note := range agents.RelativeArgNotes(agent, wd, cmd.Dir, extraArgs) {
			fmt.Fprintln(osStderr, note)
		}
	}

	// Step 9: args and wrapper, fail closed. Automatic profiles are never
	// applied: applyProfileForLaunch is not on this path.
	cleanup, err := applySlotProfile(cmd, agent, row)
	if err != nil {
		return err
	}

	// Step 10: record, then run. The profile's launch is recorded under its
	// own id and moves the picker's marker; rotation.state is not touched.
	// Each distinct slot model gets a refcount line, so no other session's
	// stop picker offers a model this one uses.
	if rerr := rotation.New().RecordProfile(agent, row.Model.ID); rerr != nil {
		fmt.Fprintf(os.Stderr, "note: last-picked state not saved: %v\n", rerr)
	}
	distinct := p.Distinct()
	ids := make([]string, len(distinct))
	for i, m := range distinct {
		ids[i] = m.ID
	}
	if rerr := refcount.NewStore().RecordAll(os.Getpid(), ids); rerr != nil {
		fmt.Fprintf(os.Stderr, "note: refcount state not saved: %v\n", rerr)
	}
	return runSlotAgentCmd(cmd, agent, row, cfg, cleanup)
}
```

In `wt/cmd/wt/launch.go`, inside `launchFilteredImpl`, find:

```go
	if eligible == nil {
		m, eligible, err = resolveModel(agent, cfg, tags, family, pinned)
	} else {
		m, err = resolveModelFromEligible(agent, eligible)
	}
```

Add directly after it, before the `if err != nil {` that holds the rotation fallback:

```go
	// A -M pin of a slot profile is its own launch unit: none of the
	// single-model steps below (the ollama check, the route check, rotation,
	// the automatic profile) apply to it.
	var pin *slotPin
	if errors.As(err, &pin) {
		return launchSlotProfile(agent, worktreePath, cfg, yolo, pin.row, extraArgs)
	}
```

- [ ] **Step 28: Run the tests and see them pass**

```bash
cd wt && go test ./cmd/wt -run 'TestLaunchSlotProfile|TestSlotProfile|TestLaunchFiltered' -v
```

Expected: `--- PASS` for `TestLaunchSlotProfileSteps` (six subtests), `TestLaunchSlotProfileRecordsTheProfile`, `TestSlotProfileFailuresRunNoAgent` (seventeen subtests), `TestSlotProfileRouteErrorKeepsItsCause`, `TestSlotProfileStartErrorIsAStartError`, `TestSlotProfileLaunchSkipsAutomaticProfiles`, `TestSlotProfileWritesNoSettingsFile` and every existing `TestLaunchFiltered…` test, then `ok`.

**If check 4 found that two `--settings` flags merge** (spec, "Check results"): Task 3 then refuses `--model` only. Delete the row "passthrough args contain --settings" from `TestSlotProfileFailuresRunNoAgent`. Nothing else in this task changes.

- [ ] **Step 29: Commit**

```bash
git add wt/cmd/wt/slot_launch.go wt/cmd/wt/slot_launch_test.go wt/cmd/wt/launch.go
git commit -m "feat(wt): launch a pinned slot profile through the ten-step flow, fail closed"
```

---

#### Behaviour 7: `runLaunchPath` offers the profiles and warns once

- [ ] **Step 30: Write the failing tests**

In `wt/cmd/wt/slot_launch_test.go`, add one import to the second group of the import block, after the `usage` line:

```go
	"github.com/spf13/cobra"
```

Append to the end of the file:

```go
// launchCobraCmd is a command with the two flags runLaunchPath reads.
func launchCobraCmd() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.Flags().StringP("model", "M", "", "")
	cmd.Flags().Bool("yolo", false, "")
	return cmd
}

// restoreLaunchSlotChecks puts launchSlotChecks back when the test ends:
// runLaunchPath sets it for the whole process.
func restoreLaunchSlotChecks(t *testing.T) {
	t.Helper()
	old := launchSlotChecks
	t.Cleanup(func() { launchSlotChecks = old })
}

// TestSlotWarnings verifies the warning lines of a launch: the load error;
// one line per profile that has no usable name or agent, naming its position
// in the file; and one line when the registry has a provider "profile" while
// a usable profile exists. A profile that merely fails validation gets no
// line here: it has a blocked row that carries its reason.
func TestSlotWarnings(t *testing.T) {
	usable := slotFixtureCheck(0, "cloud", "opus", slotCloud())
	blocked := slotFixtureCheck(1, "broken", "opus", slotCloud())
	blocked.Err = errors.New("default is missing")
	unusable := profiles.SlotCheck{Index: 2, Profile: profiles.SlotProfile{Agent: "claude"}, Err: errors.New("name is missing")}
	reserved := slotFixtureConfig()
	reserved.Providers = append(reserved.Providers, config.Provider{ID: "profile", Location: config.LocationCloud})
	cases := []struct {
		name string
		a    *app
		want []string
	}{
		{"nothing wrong", &app{cfg: slotFixtureConfig(), slotChecks: []profiles.SlotCheck{usable, blocked}}, nil},
		{"no file", &app{cfg: slotFixtureConfig()}, nil},
		{"the file does not parse",
			&app{cfg: slotFixtureConfig(), slotLoadErr: errors.New("parse slot-profiles.toml: toml: line 2: unexpected EOF")},
			[]string{"wt: parse slot-profiles.toml: toml: line 2: unexpected EOF — slot profiles are unavailable"}},
		{"a profile with no usable name",
			&app{cfg: slotFixtureConfig(), slotChecks: []profiles.SlotCheck{usable, unusable}},
			[]string{"wt: slot-profiles.toml[2]: name is missing — skipped (see 'wt profile list')"}},
		{"the reserved provider id",
			&app{cfg: reserved, slotChecks: []profiles.SlotCheck{usable}},
			[]string{`wt: the registry has a provider with id "profile" — slot profiles are unavailable`}},
		{"the reserved provider id with no usable profile",
			&app{cfg: reserved, slotChecks: []profiles.SlotCheck{unusable}},
			[]string{"wt: slot-profiles.toml[2]: name is missing — skipped (see 'wt profile list')"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := slotWarnings(tc.a); !slices.Equal(got, tc.want) {
				t.Errorf("slotWarnings() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestRunLaunchPathWarnsAboutSlotProfilesOncePerRun verifies who sees the
// warnings: a launch with no agent chosen yet, or with an agent that has
// slots, prints each line once; a shell launch prints none, since a slot
// profile could never apply to it.
func TestRunLaunchPathWarnsAboutSlotProfilesOncePerRun(t *testing.T) {
	cases := []struct {
		agent string
		want  int
	}{{"claude", 1}, {"", 1}, {"shell", 0}}
	for _, tc := range cases {
		t.Run("agent="+tc.agent, func(t *testing.T) {
			restoreLaunchSlotChecks(t)
			stubStdinTTY(t, false)
			oldLaunch, oldSweep := launchFiltered, sweepRefcounts
			launchFiltered = func(string, string, *config.Config, bool, string, string, string, bool, []string, []config.Model, *precomputedProfiles) error {
				return nil
			}
			sweepRefcounts = func() {}
			t.Cleanup(func() { launchFiltered, sweepRefcounts = oldLaunch, oldSweep })
			cfg := slotFixtureConfig()
			cfg.Agents = append(cfg.Agents, config.Agent{Name: "shell"})
			a := &app{cfg: cfg, slotLoadErr: errors.New("parse slot-profiles.toml: toml: line 2: unexpected EOF")}

			stderr := captureFile(t, &os.Stderr, func() {
				// Without a pin an agent launch ends at "the picker needs a
				// TTY"; the warnings are printed before that.
				_ = runLaunchPath(launchCobraCmd(), a, tc.agent, "", "", "", nil, ".", "")
			})

			if got := strings.Count(stderr, "slot profiles are unavailable"); got != tc.want {
				t.Errorf("warning printed %d times, want %d; stderr = %q", got, tc.want, stderr)
			}
		})
	}
}

// TestBrokenSlotFileLeavesModelLaunchesAlone is Review Focus 1. With a TOML
// typo in slot-profiles.toml, or one profile with a malformed name, a pinned
// single-model launch and a rotated one behave exactly as before, wt prints
// exactly one warning line for the problem, and `-M profile/<name>` exits
// non-zero with the "not in the eligible list" message. A hand-written file
// must never be able to break the launches that do not use it.
func TestBrokenSlotFileLeavesModelLaunchesAlone(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		pin     string
		warning string
	}{
		{"a TOML typo", "[[profile]]\nname = \"mixed\n", "profile/mixed", "slot profiles are unavailable"},
		{"a malformed name",
			"[[profile]]\nname = \"Bad_Name\"\nagent = \"claude\"\ndefault = \"opus\"\n\n[profile.slots]\nopus = \"openrouter/a\"\n",
			"profile/Bad_Name", "skipped (see 'wt profile list')"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			restoreLaunchSlotChecks(t)
			path := writeSlotProfilesTOML(t, tc.body)
			stateDir := filepath.Dir(path)
			worktree := t.TempDir()
			fakeAgentOnPath(t, "claude")
			stubEnsureRoute(t)
			stubSlotSeams(t)
			oldSweep := sweepRefcounts
			sweepRefcounts = func() {}
			t.Cleanup(func() { sweepRefcounts = oldSweep })

			// The app's slot state, loaded the way newApp loads it.
			a := &app{cfg: slotFixtureConfig()}
			store, lerr := loadSlotStore()
			a.slotLoadErr = lerr
			if lerr == nil && len(store.Profiles) > 0 {
				a.slotChecks = profiles.ValidateSlots(store, agents.SlotDrivers())
			}

			rotationState := func() string {
				data, _ := os.ReadFile(filepath.Join(stateDir, "rotation.state"))
				return strings.TrimSpace(string(data))
			}

			// A pinned single-model launch: it runs, and the warning is
			// printed once.
			var err error
			var stderr string
			captureFile(t, &os.Stdout, func() {
				stderr = captureFile(t, &os.Stderr, func() {
					err = runLaunchPath(launchCobraCmd(), a, "claude", "openrouter/a", "", "", nil, worktree, "")
				})
			})
			if err != nil {
				t.Fatalf("pinned launch error = %v", err)
			}
			if got := rotationState(); got != "openrouter/a" {
				t.Errorf("rotation.state = %q after the pinned launch, want openrouter/a", got)
			}
			if got := strings.Count(stderr, tc.warning); got != 1 {
				t.Errorf("warning printed %d times, want exactly once; stderr = %q", got, stderr)
			}
			slotLines := 0
			for _, line := range strings.Split(stderr, "\n") {
				if strings.Contains(line, "slot") {
					slotLines++
				}
			}
			if slotLines != 1 {
				t.Errorf("%d stderr lines mention slot profiles, want one; stderr = %q", slotLines, stderr)
			}

			// A rotated launch: rotation advances to the next launchable
			// model, as it does with no slot file at all.
			captureFile(t, &os.Stdout, func() {
				err = launchFiltered("claude", worktree, a.cfg, false, "", "", "", false, nil, nil, nil)
			})
			if err != nil {
				t.Fatalf("rotated launch error = %v", err)
			}
			if got := rotationState(); got != "openrouter/b" {
				t.Errorf("rotation.state = %q after the rotated launch, want openrouter/b", got)
			}

			// The profile itself cannot be pinned.
			captureFile(t, &os.Stderr, func() {
				err = runLaunchPath(launchCobraCmd(), a, "claude", tc.pin, "", "", nil, worktree, "")
			})
			want := fmt.Sprintf("model %q is not in the eligible list for agent %q", tc.pin, "claude")
			if err == nil || err.Error() != want {
				t.Errorf("profile pin error = %v, want %q", err, want)
			}
		})
	}
}

// TestRunLaunchPathLaunchesAPinnedSlotProfile verifies the whole non-TUI path
// from runLaunchPath: `-A claude -M profile/<name>` with a worktree reaches
// the profile launch and runs the agent, and a pin of a blocked profile
// returns its reason, which main turns into a non-zero exit.
func TestRunLaunchPathLaunchesAPinnedSlotProfile(t *testing.T) {
	restoreLaunchSlotChecks(t)
	_, worktree := slotLaunchEnv(t)
	oldSweep := sweepRefcounts
	sweepRefcounts = func() {}
	t.Cleanup(func() { sweepRefcounts = oldSweep })
	seams := stubSlotSeams(t)
	sentinel := filepath.Join(t.TempDir(), "agent-ran")
	seams.cmd = exec.Command("touch", sentinel)
	a := &app{cfg: slotFixtureConfig(), slotChecks: []profiles.SlotCheck{
		slotFixtureCheck(0, "cloud", "opus", slotCloud()),
		slotFixtureCheck(1, "broken", "opus", map[string]string{"opus": "openrouter/gone"}),
	}}

	var err error
	captureFile(t, &os.Stdout, func() {
		err = runLaunchPath(launchCobraCmd(), a, "claude", "profile/cloud", "", "", nil, worktree, "")
	})
	if err != nil {
		t.Fatalf("runLaunchPath() error = %v", err)
	}
	if _, statErr := os.Stat(sentinel); statErr != nil {
		t.Error("the agent command did not run for a pinned launch profile")
	}

	err = runLaunchPath(launchCobraCmd(), a, "claude", "profile/broken", "", "", nil, worktree, "")
	if err == nil || err.Error() != "slot opus: openrouter/gone is not in the registry" {
		t.Errorf("blocked pin error = %v, want the row's reason", err)
	}
}
```

`writeSlotProfilesTOML` is the helper added to `wt/cmd/wt/app_test.go` in Step 1. `stubEnsureRoute` is in `wt/cmd/wt/testmain_test.go`; it records the single-model route check and silences `osStderr`.

- [ ] **Step 31: Run them and see them fail**

```bash
cd wt && go test ./cmd/wt -run 'TestSlotWarnings|TestRunLaunchPathWarnsAboutSlotProfilesOncePerRun|TestBrokenSlotFileLeavesModelLaunchesAlone|TestRunLaunchPathLaunchesAPinnedSlotProfile' -v
```

Expected: the build fails with `undefined: slotWarnings`, then `FAIL	github.com/ohanaverse/local-ai-setup/wt/cmd/wt [build failed]`.

- [ ] **Step 32: Write the implementation**

In `wt/cmd/wt/slot_launch.go`, insert this between the seam `var ( … )` block and `slotRouteError`:

```go
// slotWarnings are the lines a launch prints once about slot-profiles.toml:
// the load error; one line per profile that is not Usable (no usable name or
// agent, so it has no row and cannot be pinned); and one line when the
// registry has a provider with id "profile" while at least one profile is
// Usable, since no profile row is then built. A Usable profile that fails
// validation gets no line: its blocked row carries the reason.
func slotWarnings(a *app) []string {
	var out []string
	if a.slotLoadErr != nil {
		out = append(out, fmt.Sprintf("wt: %v — slot profiles are unavailable", a.slotLoadErr))
	}
	usable := false
	for _, c := range a.slotChecks {
		if c.Usable {
			usable = true
			continue
		}
		out = append(out, fmt.Sprintf("wt: %s: %v — skipped (see 'wt profile list')", c.Label(), c.Err))
	}
	if usable && catalog.SlotPrefixReserved(a.cfg) {
		out = append(out, `wt: the registry has a provider with id "profile" — slot profiles are unavailable`)
	}
	return out
}
```

In `wt/cmd/wt/main.go`, inside `runLaunchPath`, add directly after the line `sweepRefcounts()`:

```go

	// Slot profiles are pin targets for resolveModel during this run. What is
	// wrong with slot-profiles.toml is said once, here, before any picker
	// opens, and only for a launch that could use a profile: no agent chosen
	// yet, or an agent whose driver has slots.
	launchSlotChecks = a.slotChecks
	if _, ok := agents.SlotsFor(agent); agent == "" || ok {
		for _, line := range slotWarnings(a) {
			fmt.Fprintln(os.Stderr, line)
		}
	}
```

`main.go` already imports `fmt`, `os` and `agents`.

- [ ] **Step 33: Run the tests and see them pass**

```bash
cd wt && go test ./cmd/wt -run 'TestSlotWarnings|TestRunLaunchPath|TestBrokenSlotFileLeavesModelLaunchesAlone' -v
```

Expected: `--- PASS` for `TestSlotWarnings` (six subtests), `TestRunLaunchPathWarnsAboutSlotProfilesOncePerRun` (three subtests), `TestBrokenSlotFileLeavesModelLaunchesAlone` (two subtests), `TestRunLaunchPathLaunchesAPinnedSlotProfile`, and the existing `TestRunLaunchPath` and `TestRunLaunchPathPassesWorkingProfileApplierToTUI`, then `ok`.

- [ ] **Step 34: Run the whole module's checks**

```bash
cd wt && gofmt -l . && go build ./... && go vet ./... && go test ./...
```

Expected: `gofmt -l .` prints nothing; `go build` and `go vet` print nothing; `go test` prints `ok` for every package and no `FAIL`.

- [ ] **Step 35: Commit**

```bash
git add wt/cmd/wt/slot_launch.go wt/cmd/wt/slot_launch_test.go wt/cmd/wt/main.go
git commit -m "feat(wt): offer slot profiles to the non-TUI launch and warn once about a broken file"
```

### Task 9: TUI profile launch

Enter on a profile row in the model picker, and a `-M profile/<name>` pin the TUI handles, run the spec's ten launch steps without ever blocking the Bubble Tea update goroutine. The work is five parts, each with its own commit. The feature stays dark until Part E, which is the one-line change that hands the loaded profiles to the TUI.

Read first: `wt/docs/internals/tui.md` and `wt/docs/internals/testing.md`. Two rules from them govern this task. The update goroutine never waits: every route write, restart and probe runs in a `tea.Cmd`. A test that sets state by hand goes through `Update` before it reads `View()`.

**Files:**
- Create: `wt/internal/tui/slot_flow.go`
- Create: `wt/internal/tui/slot_flow_test.go`
- Modify: `wt/internal/tui/launch.go` (imports; `pendingSurvey`; new declarations after `profileApplier`)
- Modify: `wt/internal/tui/app.go` (the `phase` constants; `model` struct; `update`: message cases, key dispatch, the `esc` case, Enter on `phaseModel` and on `phaseReplaceConfirm`; `View`; the pin `switch` in `enterModelPhase`; `proceedToLaunch`; `Run`; `flushRouteNotesAfterRun`; `printPendingSummaryAndSurvey`)
- Modify: `wt/internal/tui/start_flow.go` (`runStart`, `beginStart`, `finishStart`)
- Modify: `wt/internal/tui/testhelpers_test.go` (`TestMain`, imports)
- Modify: `wt/cmd/wt/main.go` (`runLaunchPath`: the two `tuiRun` calls)
- Modify: `wt/cmd/wt/helpers_test.go`, `wt/cmd/wt/main_test.go` (the nine `tuiRun` stubs; one new test in `main_test.go`)
- Test: `wt/internal/tui/slot_flow_test.go`, `wt/cmd/wt/main_test.go`

**Interfaces:**
- Consumes (existing code):
  - `func runAndWaitCmd(cmd *exec.Cmd, agent string, m config.Model) tea.Cmd`, `func flushRouteNotes(w io.Writer)`, `func queueLaunchNotes(agent string, cmd *exec.Cmd, extraArgs []string)`, `var currentProgram *tea.Program`, `var pendingSummary string`, `var pendingRouteNotes string`, `var pendingSurveyState pendingSurvey`, `type launchDoneMsg struct{ err error }` (`internal/tui/launch.go`)
  - `var runInventory = realRunInventory`, `func (m model) refreshTable() (model, tea.Cmd)`, `func (m model) routeBackToAgent(status string) (model, tea.Cmd)`, `func newRunModel(yolo, allowReplace bool, agent, pinned, tags, family string, extraArgs []string, theme themes.Theme, prePath string, cfg *config.Config) model` (`internal/tui/app.go`)
  - `var waitPendingRoutes = lifecycle.WaitPendingRoutes`, `func (m *model) recordRouteNotes(notes string)`, `type startState`, `type replaceState struct{ item *modelItem; choices list.Model }`, `type startDoneMsg` (`internal/tui/start_flow.go`)
  - `var releaseSession`, `var runStopPhase`, `var runSurvey`, `var emitPriceNotice` (`internal/tui/survey.go`)
  - `func agents.Summary(agent string, m config.Model, d time.Duration) string`, `func agents.RunAndCleanup(cmd *exec.Cmd, cleanup func() error) (time.Duration, error)`
- Consumes (earlier tasks):
  - Task 2: `profiles.SlotCheck`, `profiles.SlotLaunch`
  - Task 3: `func agents.CheckSlotPassthrough(agent string, extraArgs []string) error`, `func agents.BuildSlotLaunchCmd(sl profiles.SlotLaunch, worktreePath string, yolo bool, cfg *config.Config, extraArgs []string) (*exec.Cmd, error)`
  - Task 4: `func catalog.ResolveSlotProfile(cfg *config.Config, snap *localmodels.Snapshot, c profiles.SlotCheck) catalog.Row`, `catalog.ProfileRow` with fields `Check`, `Local *catalog.Row` and methods `Distinct() []config.Model`, `Cloud() []config.Model`, `SlotOf(modelID string) string`, `Launch() profiles.SlotLaunch`; `catalog.Row.Profile`, `Row.Action()`, `Row.BlockReason()`
  - Task 5: `modelItem.profile *catalog.ProfileRow`, `model.slotChecks []profiles.SlotCheck`, and the test fixtures `slotTestConfig() *config.Config`, `slotCheckFor(index int, name, def string, slots map[string]string) profiles.SlotCheck`, `slotPicker(t *testing.T, cfg *config.Config, checks []profiles.SlotCheck, width, height int) model` (`internal/tui/slot_row_test.go`)
  - Task 6: `func lifecycle.EnsureModelRoutes(ctx context.Context, out io.Writer, cfg *config.Config, models []config.Model, owedIn bool) (owed bool, err error)`, `func lifecycle.RestartProxy(ctx context.Context, cfg *config.Config) error`, `func lifecycle.ProbeProxy(ctx context.Context, cfg *config.Config) error`, `func lifecycle.SettleRoutes(ctx context.Context, cfg *config.Config)`, `func lifecycle.RestartStillOwed(err error) bool`, `type lifecycle.RouteEnsureError struct{ ModelID string; Err error }`, `lifecycle.Options.RestartOwed bool`
  - Task 7: `func (r *rotation.Rotation) RecordProfile(agent, profileID string) error`, `func (s *refcount.StoreImpl) RecordAll(pid int, modelIDs []string) error`
  - Task 8: `func applySlotProfile(cmd *exec.Cmd, agent string, row catalog.Row) (cleanup func() error, err error)`, `app.slotChecks []profiles.SlotCheck`, and the cmd/wt test helpers `slotFixtureCheck(index int, name, def string, slots map[string]string) profiles.SlotCheck` and `stubSlotChecks(t *testing.T, checks ...profiles.SlotCheck)` (`cmd/wt/slot_launch_test.go`)
- Produces:
  - `type SlotProfileApplier func(cmd *exec.Cmd, agent string, row catalog.Row) (cleanup func() error, err error)`
  - `type SlotProfiles struct { Checks []profiles.SlotCheck; Apply SlotProfileApplier }`
  - `var slotApplier SlotProfileApplier`
  - `pendingSurvey.profile bool`
  - `func Run(yolo, allowReplace bool, agent, pinned, tags, family string, extraArgs []string, theme themes.Theme, prePath string, cfg *config.Config, applyProfile ProfileApplier, slots SlotProfiles) error`
  - `phaseSlot`, `model.slot *slotFlow`, `model.slotRun int`
  - In `slot_flow.go`: the seams `ensureSlotRoutes`, `restartProxyNow`, `probeProxyNow`, `settleRoutesNow`, `buildSlotCmd`; the types `slotResult`, `slotFlow`, `slotPreparedMsg`, `slotSettledMsg`, `slotTickMsg`, `slotAbortMsg`; `beginSlotLaunch`, `handleSlotMsg`, `handleSlotKey`, `slotView`, `abortSlot`, `settleSlotAfterStart`, `launchSlot`, `runSlotAndWaitCmd`
  - In `start_flow.go`: `func runStart(ctx context.Context, cfg *config.Config, t lifecycle.Target, allow, restartOwed bool, id int) (<-chan tea.Msg, *startOutput)`, `func (m model) beginStartOwed(it *modelItem, allowReplace, restartOwed bool) (model, tea.Cmd)`; `beginStart` keeps its signature

Run every command from the repository root. Each command block starts there: a `cd wt && …` line does not carry over to the next block, and the `git add` lines name paths from the root.

Choices made where the spec is silent, each following the code beside it:

- `withSlotProfiles` is one unexported helper in `launch.go` that `Run` calls. `Run` itself needs a terminal and cannot be unit-tested, so the helper is what the wiring test calls, as `newRunModel` is for `Run`'s other arguments.
- A failed route write waits for the restart it settled (`waitPendingRoutes`) before the stage reports. The route check does the same: that restart writes its warnings into the stage's buffer until the wait returns.
- A quit that leaves a restart owed settles it once the alt screen is gone (`settleQuitSlot`): ctrl+c during stage one of a start row, and `q` or ctrl+c on the replace dialog. Without it a written cloud route stays unpublished.
- `launchSlot` sets `phaseModel` before it returns the run command, as `handleRouteMsg` does before `launchSelected`. `View` can be called once more before the terminal is released, and `m.slot` is nil by then.

---

#### Part A: plumbing and the post-exit branch

`Run` gains its twelfth parameter. Every call site passes the zero value for now, so no profile row can appear yet.

- [ ] **Step 1: Write the failing tests**

Create `wt/internal/tui/slot_flow_test.go`:

```go
package tui

import (
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// TestProfilePostExitSkipsSurveyAndPriceNotice pins what follows a
// slot-profile session: the refcount lines are released, the stop picker runs,
// the summary is printed, and neither the survey nor the price notice is. A
// profile is not one priced model: a survey would be stored under an id no
// registry model has, and the notice would name prices the session never used.
func TestProfilePostExitSkipsSurveyAndPriceNotice(t *testing.T) {
	prevSummary, prevSurvey := pendingSummary, pendingSurveyState
	prevRun, prevNotice, prevRelease, prevStop := runSurvey, emitPriceNotice, releaseSession, runStopPhase
	t.Cleanup(func() {
		pendingSummary, pendingSurveyState = prevSummary, prevSurvey
		runSurvey, emitPriceNotice, releaseSession, runStopPhase = prevRun, prevNotice, prevRelease, prevStop
	})

	var order []string
	pendingSummary = "wt: claude · profile/cloud · 1m0s"
	pendingSurveyState = pendingSurvey{agent: "claude", m: config.Model{ID: "profile/cloud"}, profile: true}
	releaseSession = func() { order = append(order, "release") }
	runSurvey = func(string, config.Model) string { order = append(order, "survey"); return "STATS-BLOCK" }
	runStopPhase = func(*config.Config) { order = append(order, "picker") }
	emitPriceNotice = func(*config.Config) { order = append(order, "notice") }

	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	printPendingSummaryAndSurvey(&config.Config{})
	w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)

	if got := strings.Join(order, ","); got != "release,picker" {
		t.Errorf("order = %s, want release,picker (no survey, no price notice)", got)
	}
	if s := string(out); !strings.Contains(s, "wt: claude · profile/cloud · 1m0s") || strings.Contains(s, "STATS-BLOCK") {
		t.Errorf("stdout = %q, want the summary line and no survey stats", s)
	}
	if pendingSurveyState.agent != "" || pendingSurveyState.profile || pendingSummary != "" {
		t.Errorf("pending state not reset: %+v %q", pendingSurveyState, pendingSummary)
	}
}

// TestWithSlotProfilesCarriesChecksAndApplier pins the one place Run's slot
// argument reaches the TUI: the checks land on the model, where tableFor reads
// them, and the applier lands in the slotApplier seam. Dropping either makes
// the feature vanish without an error: no profile rows, or every profile
// launch aborting with "no slot-profile applier".
func TestWithSlotProfilesCarriesChecksAndApplier(t *testing.T) {
	old := slotApplier
	t.Cleanup(func() { slotApplier = old })
	called := false
	apply := func(*exec.Cmd, string, catalog.Row) (func() error, error) {
		called = true
		return nil, nil
	}
	checks := []profiles.SlotCheck{{Index: 0, Usable: true, Profile: profiles.SlotProfile{Name: "cloud", Agent: "claude"}}}

	m := withSlotProfiles(newRunModel(false, false, "claude", "", "", "", nil, themes.Default, ".", &config.Config{}), SlotProfiles{Checks: checks, Apply: apply})

	if len(m.slotChecks) != 1 || m.slotChecks[0].Profile.Name != "cloud" {
		t.Fatalf("slotChecks = %+v, want the one check handed in", m.slotChecks)
	}
	if slotApplier == nil {
		t.Fatal("slotApplier is nil: a profile launch would abort")
	}
	if _, _ = slotApplier(nil, "", catalog.Row{}); !called {
		t.Fatal("slotApplier is not the function handed in")
	}
}
```

- [ ] **Step 2: Run them and see them fail**

```bash
cd wt && go test ./internal/tui -run 'TestProfilePostExitSkipsSurveyAndPriceNotice|TestWithSlotProfilesCarriesChecksAndApplier'
```

Expected: the package does not compile.

```
internal/tui/slot_flow_test.go: unknown field profile in struct literal of type pendingSurvey
internal/tui/slot_flow_test.go: undefined: slotApplier
internal/tui/slot_flow_test.go: undefined: withSlotProfiles
internal/tui/slot_flow_test.go: undefined: SlotProfiles
FAIL	github.com/ohanaverse/local-ai-setup/wt/internal/tui [build failed]
```

- [ ] **Step 3: Write the implementation**

In `wt/internal/tui/launch.go`, add two imports to the import block (keep it sorted):

```go
	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
```

Replace the `pendingSurvey` type:

```go
type pendingSurvey struct {
	agent string
	m     config.Model
	// profile marks a slot-profile session: no survey and no price notice.
	profile bool
}
```

Add after the `var profileApplier ProfileApplier` line:

```go
// SlotProfileApplier applies a slot profile's args and wrapper to cmd. It
// fails closed: an error aborts the launch. cmd/wt hands in applySlotProfile.
// It is a second closure beside ProfileApplier for the same reason that one
// exists: this package cannot import cmd/wt.
type SlotProfileApplier func(cmd *exec.Cmd, agent string, row catalog.Row) (cleanup func() error, err error)

// SlotProfiles is what Run needs for slot profiles: the statically validated
// profiles (the picker builds their rows) and the applier of a profile launch.
type SlotProfiles struct {
	Checks []profiles.SlotCheck
	Apply  SlotProfileApplier
}

// slotApplier is set by Run from SlotProfiles.Apply. nil means no applier:
// a profile launch then aborts with "no slot-profile applier". It is never a
// no-op, unlike a nil profileApplier: a slot profile that cannot be fully
// applied must not launch.
var slotApplier SlotProfileApplier

// withSlotProfiles is how Run's slot argument reaches the TUI: the checks go
// on the model, the applier into its seam. It is split out of Run so a test
// can pin it without a real tea.Program.
func withSlotProfiles(m model, s SlotProfiles) model {
	slotApplier = s.Apply
	m.slotChecks = s.Checks
	return m
}
```

In `wt/internal/tui/app.go`, change `Run`. Add this sentence to the end of its doc comment:

```go
// slots carries the slot profiles: the picker shows a row for each one that
// names this agent, and a profile launch applies slots.Apply in place of
// applyProfile.
```

Replace the signature line and the line that builds the program:

```go
func Run(yolo, allowReplace bool, agent, pinned, tags, family string, extraArgs []string, theme themes.Theme, prePath string, cfg *config.Config, applyProfile ProfileApplier, slots SlotProfiles) error {
	initial := withSlotProfiles(newRunModel(yolo, allowReplace, agent, pinned, tags, family, extraArgs, theme, prePath, cfg), slots)
	p := tea.NewProgram(initial, tea.WithAltScreen())
```

In `printPendingSummaryAndSurvey`, insert this block after the line `pendingSummary = ""` and before `var stats string`:

```go
	if launched.profile {
		// A slot-profile session. The refcount lines are released, then the
		// stop picker runs as after any non-native launch; it is not
		// filtered to the profile. No survey and no price notice: the
		// profile is not one priced registry model.
		releaseSession()
		runStopPhase(cfg)
		if summary != "" {
			fmt.Println("\n" + summary)
		}
		return
	}
```

In `wt/cmd/wt/main.go`, `runLaunchPath` has two `tuiRun` calls with identical text. Replace both with:

```go
		return tuiRun(yolo(cmd), allowReplace, agent, pinned, tags, family, args, a.theme, launchPath, a.cfg, applyProfile, tui.SlotProfiles{})
```

The nine `tuiRun` test stubs gain the parameter. Run this from the repo root:

```bash
cd wt && perl -0pi -e 's/(tuiRun = func\(bool, bool, string, string, string, string, \[\]string, themes\.Theme, string, \*config\.Config, tui\.ProfileApplier)\)/$1, tui.SlotProfiles)/g; s/(tuiRun = func\(yolo, allowReplace bool, agent, pinned, tags, family string, extraArgs \[\]string, theme themes\.Theme, prePath string, cfg \*config\.Config, (?:_|applyProfile) tui\.ProfileApplier)\)/$1, _ tui.SlotProfiles)/g' cmd/wt/helpers_test.go cmd/wt/main_test.go
grep -c 'tui.SlotProfiles' cmd/wt/helpers_test.go cmd/wt/main_test.go
```

Expected:

```
cmd/wt/helpers_test.go:1
cmd/wt/main_test.go:8
```

The three shapes the stubs now have:

```go
tuiRun = func(bool, bool, string, string, string, string, []string, themes.Theme, string, *config.Config, tui.ProfileApplier, tui.SlotProfiles) error {
tuiRun = func(yolo, allowReplace bool, agent, pinned, tags, family string, extraArgs []string, theme themes.Theme, prePath string, cfg *config.Config, _ tui.ProfileApplier, _ tui.SlotProfiles) error {
tuiRun = func(yolo, allowReplace bool, agent, pinned, tags, family string, extraArgs []string, theme themes.Theme, prePath string, cfg *config.Config, applyProfile tui.ProfileApplier, _ tui.SlotProfiles) error {
```

If a count differs, run `grep -n 'tuiRun = func' cmd/wt/*_test.go` and fix the lines the command missed by hand.

- [ ] **Step 4: Run the tests and see them pass**

```bash
cd wt && go build ./... && go vet ./internal/tui ./cmd/wt && go test ./internal/tui ./cmd/wt
```

Expected: `ok` for both packages, and no vet output.

- [ ] **Step 5: Commit**

```bash
git add wt/internal/tui/launch.go wt/internal/tui/app.go wt/internal/tui/slot_flow_test.go wt/cmd/wt/main.go wt/cmd/wt/helpers_test.go wt/cmd/wt/main_test.go
git commit -m "feat(wt): tui.Run takes the slot profiles and their applier, and a profile session skips the survey and the price notice"
```

---

#### Part B: Enter on a launch row

Enter on a profile row that is not blocked enters `phaseSlot`. One command runs steps 1 to 7 of the launch flow. Its message either launches (steps 8 to 10) or returns to the picker with the reason. ctrl+c is the only key; it quits, and wt settles the stage before it exits.

- [ ] **Step 6: Write the failing tests**

In `wt/internal/tui/slot_flow_test.go`, replace the import block with:

```go
import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
	"github.com/ohanaverse/local-ai-setup/wt/internal/refcount"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)
```

Append to the file:

```go
// slotStub scripts the five slot-flow seams and records, in order, what a
// profile launch asked of them: "ensure:<id>,<id>:owed=<owedIn>", "restart",
// "probe", "settle", "build". A stage runs in a command, which one test runs
// on its own goroutine, so every access takes the lock.
type slotStub struct {
	mu     sync.Mutex
	events []string

	ensureOwed   bool   // a write here changed config.yaml
	ensureOutput string // written to the writer the stage handed in
	ensureErr    error
	restartErr   error
	probeErr     error
	buildErr     error
	cmd          *exec.Cmd // what buildSlotCmd returns; default exec.Command("true")

	// ensureEntered and ensureRelease, when set, hold the first route write
	// open: ensureEntered is closed once it is inside, and it returns only
	// when ensureRelease is closed.
	ensureEntered, ensureRelease chan struct{}
	entered                      sync.Once
}

func (s *slotStub) add(event string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
}

// got is the recorded events, comma-joined.
func (s *slotStub) got() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.events, ",")
}

// stubSlotSeams swaps ensureSlotRoutes, restartProxyNow, probeProxyNow,
// settleRoutesNow and buildSlotCmd for recorders scripted by the returned
// slotStub, and restores them on cleanup. Nothing real is touched: no
// config.yaml, no proxy, no agent binary.
func stubSlotSeams(t *testing.T) *slotStub {
	t.Helper()
	s := &slotStub{}
	oldEnsure, oldRestart, oldProbe, oldSettle, oldBuild := ensureSlotRoutes, restartProxyNow, probeProxyNow, settleRoutesNow, buildSlotCmd
	t.Cleanup(func() {
		ensureSlotRoutes, restartProxyNow, probeProxyNow, settleRoutesNow, buildSlotCmd = oldEnsure, oldRestart, oldProbe, oldSettle, oldBuild
	})
	ensureSlotRoutes = func(_ context.Context, out io.Writer, _ *config.Config, models []config.Model, owedIn bool) (bool, error) {
		ids := make([]string, len(models))
		for i, m := range models {
			ids[i] = m.ID
		}
		event := "ensure:" + strings.Join(ids, ",") + ":owed=false"
		if owedIn {
			event = "ensure:" + strings.Join(ids, ",") + ":owed=true"
		}
		s.add(event)
		if s.ensureEntered != nil {
			s.entered.Do(func() {
				close(s.ensureEntered)
				<-s.ensureRelease
			})
		}
		if out != nil && s.ensureOutput != "" {
			_, _ = io.WriteString(out, s.ensureOutput)
		}
		if s.ensureErr != nil {
			return false, s.ensureErr
		}
		return owedIn || s.ensureOwed, nil
	}
	restartProxyNow = func(context.Context, *config.Config) error {
		s.add("restart")
		return s.restartErr
	}
	probeProxyNow = func(context.Context, *config.Config) error {
		s.add("probe")
		return s.probeErr
	}
	settleRoutesNow = func(context.Context, *config.Config) { s.add("settle") }
	buildSlotCmd = func(profiles.SlotLaunch, string, bool, *config.Config, []string) (*exec.Cmd, error) {
		s.add("build")
		if s.buildErr != nil {
			return nil, s.buildErr
		}
		if s.cmd != nil {
			return s.cmd, nil
		}
		return exec.Command("true"), nil
	}
	return s
}

// stubSlotApplier sets the slotApplier seam for one test.
func stubSlotApplier(t *testing.T, apply SlotProfileApplier) {
	t.Helper()
	old := slotApplier
	slotApplier = apply
	t.Cleanup(func() { slotApplier = old })
}

// The profiles the flow tests use, over slotTestConfig's models.
func cloudProfile() profiles.SlotCheck {
	return slotCheckFor(0, "cloud", "opus", map[string]string{"opus": "openrouter/a", "sonnet": "openrouter/b"})
}

func mixedProfile() profiles.SlotCheck {
	return slotCheckFor(1, "mixed", "opus", map[string]string{"opus": "openrouter/a", "haiku": "omlx/qwen3.8"})
}

// slotFlowFixture is the claude model picker at 80x24 with the given profiles,
// a worktree to launch in, and the pending notes, summary and survey state
// cleared for the test.
func slotFlowFixture(t *testing.T, snap localmodels.Snapshot, checks ...profiles.SlotCheck) model {
	t.Helper()
	stubInventory(t, snap)
	stubRouteNotes(t)
	prevSummary, prevSurvey := pendingSummary, pendingSurveyState
	pendingSummary, pendingSurveyState = "", pendingSurvey{}
	t.Cleanup(func() { pendingSummary, pendingSurveyState = prevSummary, prevSurvey })
	m := slotPicker(t, slotTestConfig(), checks, 80, 24)
	m.agent = "claude"
	m.selectedPath = t.TempDir()
	return m
}

// enterProfileRow puts the cursor on the row with id and presses Enter.
func enterProfileRow(t *testing.T, m model, id string) (model, tea.Cmd) {
	t.Helper()
	idx := indexOfID(m, id)
	if idx < 0 {
		t.Fatalf("no row %s in %v", id, itemIDs(m))
	}
	m.models.Select(idx)
	return updateMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
}

// slotStageFrom splits the batch a slot stage returned: the stage's command
// and its one-second tick. The tick is never run here; it would sleep.
func slotStageFrom(t *testing.T, cmd tea.Cmd) tea.Cmd {
	t.Helper()
	if cmd == nil {
		t.Fatal("no command: a slot stage must run in a command, off the update goroutine")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatalf("command = %T (%d), want a batch of the stage and its tick", batch, len(batch))
	}
	return batch[0]
}

// assertNothingRecorded fails when a launch wrote any bookkeeping under the
// test's state directory.
func assertNothingRecorded(t *testing.T) {
	t.Helper()
	for _, name := range []string{"last-picked.state", "rotation.state", "usage.jsonl", "refcount.jsonl"} {
		if _, err := os.Stat(filepath.Join(config.Dir(), name)); err == nil {
			t.Errorf("%s was written, want nothing recorded for a launch that did not happen", name)
		}
	}
}

// TestEnterOnProfileRowRunsTheStepsThenLaunches is the launch row's whole
// path. Enter enters phaseSlot and returns at once; the route writes, the one
// restart and the probe run in the command; its message builds the command
// and runs the agent through the slot applier, never the automatic one. The
// session is recorded under the profile id with one refcount line per slot
// model, and rotation.state is left alone, so the next rotated launch is
// unchanged. A regression here launches Claude Code with unmapped slots or
// moves the user's rotation.
func TestEnterOnProfileRowRunsTheStepsThenLaunches(t *testing.T) {
	m := slotFlowFixture(t, idleOmlxSnapshot(), cloudProfile())
	stub := stubSlotSeams(t)
	stub.ensureOwed = true
	applied := ""
	stubSlotApplier(t, func(_ *exec.Cmd, agent string, row catalog.Row) (func() error, error) {
		applied = agent + ":" + row.Model.ID
		return func() error { return nil }, nil
	})
	oldAuto := profileApplier
	profileApplier = func(*exec.Cmd, string, config.Model) (func() error, error) {
		t.Error("the automatic-profile applier ran for a slot-profile launch")
		return nil, nil
	}
	t.Cleanup(func() { profileApplier = oldAuto })

	preparing, cmd := enterProfileRow(t, m, "profile/cloud")
	if preparing.phase != phaseSlot || preparing.slot == nil {
		t.Fatalf("phase = %v slot = %v, want phaseSlot with a launch in flight", preparing.phase, preparing.slot)
	}
	if got := stub.got(); got != "" {
		t.Fatalf("events after Enter = %q, want none: the stage must not run on the update goroutine", got)
	}
	if view := preparing.View(); !strings.Contains(view, "Preparing profile/cloud") {
		t.Errorf("view = %q, want the preparing screen naming the profile", view)
	}

	launching, launch := updateMsg(preparing, slotStageFrom(t, cmd)())
	if got, want := stub.got(), "ensure:openrouter/a,openrouter/b:owed=false,restart,probe,build"; got != want {
		t.Fatalf("events = %q, want %q", got, want)
	}
	if launching.slot != nil || launching.phase != phaseModel || launch == nil {
		t.Fatalf("slot = %v phase = %v cmd nil = %v, want the flow ended and a run command", launching.slot, launching.phase, launch == nil)
	}
	if _, ok := launch().(launchDoneMsg); !ok {
		t.Fatal("the run command did not return launchDoneMsg")
	}

	if applied != "claude:profile/cloud" {
		t.Errorf("slot applier saw %q, want claude:profile/cloud", applied)
	}
	if b, err := os.ReadFile(filepath.Join(config.Dir(), "last-picked.state")); err != nil || string(b) != "profile/cloud\n" {
		t.Errorf("last-picked.state = %q, %v, want the profile id", b, err)
	}
	if _, err := os.Stat(filepath.Join(config.Dir(), "rotation.state")); err == nil {
		t.Error("rotation.state was written: a profile launch must not move the rotation pointer")
	}
	if b, _ := os.ReadFile(filepath.Join(config.Dir(), "usage.jsonl")); !strings.Contains(string(b), `"model_id":"profile/cloud"`) {
		t.Errorf("usage.jsonl = %q, want one event under the profile id", b)
	}
	counts := refcount.NewStore().Counts([]string{"openrouter/a", "openrouter/b", "profile/cloud"})
	if counts["openrouter/a"] != 1 || counts["openrouter/b"] != 1 || counts["profile/cloud"] != 0 {
		t.Errorf("refcounts = %v, want one line per slot model and none for the profile id", counts)
	}
	if !pendingSurveyState.profile || pendingSurveyState.m.ID != "profile/cloud" || !strings.Contains(pendingSummary, "profile/cloud") {
		t.Errorf("pending = %+v %q, want a profile session named in the summary", pendingSurveyState, pendingSummary)
	}
}

// TestProfileLaunchWithEveryRouteInPlaceRestartsNothing pins success
// criterion 5: when no route write changed config.yaml the proxy is not
// restarted, only probed. A restart on every profile launch would cost each
// one 10 to 20 seconds and cut every other session's requests.
func TestProfileLaunchWithEveryRouteInPlaceRestartsNothing(t *testing.T) {
	m := slotFlowFixture(t, idleOmlxSnapshot(), cloudProfile())
	stub := stubSlotSeams(t)

	preparing, cmd := enterProfileRow(t, m, "profile/cloud")
	updateMsg(preparing, slotStageFrom(t, cmd)())

	if got, want := stub.got(), "ensure:openrouter/a,openrouter/b:owed=false,probe,build"; got != want {
		t.Fatalf("events = %q, want %q (no restart)", got, want)
	}
}

// TestEnterOnBlockedProfileRowShowsItsReason pins the blocked row: Enter
// shows the validation reason and stays on the picker, with nothing written
// and nothing probed. A blocked profile that began its launch would write
// routes for a profile that can never run.
func TestEnterOnBlockedProfileRowShowsItsReason(t *testing.T) {
	broken := slotCheckFor(0, "broken", "opus", map[string]string{"opus": "openrouter/gone"})
	m := slotFlowFixture(t, idleOmlxSnapshot(), broken)
	stub := stubSlotSeams(t)

	got, cmd := enterProfileRow(t, m, "profile/broken")

	if got.phase != phaseModel || got.slot != nil || cmd != nil {
		t.Fatalf("phase = %v slot = %v cmd nil = %v, want the picker and no command", got.phase, got.slot, cmd == nil)
	}
	if want := "slot opus: openrouter/gone is not in the registry"; got.status != want {
		t.Errorf("status = %q, want %q", got.status, want)
	}
	if events := stub.got(); events != "" {
		t.Errorf("events = %q, want none", events)
	}
}

// TestSlotStageFailuresReturnToThePicker is the failure table's TUI half:
// every step that can fail before the agent starts ends on the picker with
// the reason as its status, no agent command run and nothing recorded. A
// failure that fell through to a launch would start Claude Code with slots
// the proxy cannot serve.
func TestSlotStageFailuresReturnToThePicker(t *testing.T) {
	const ensured = "ensure:openrouter/a,openrouter/b:owed=false"
	cases := []struct {
		name    string
		arrange func(m *model, s *slotStub)
		status  string
		events  string
	}{
		{
			name: "the profile no longer resolves",
			arrange: func(m *model, _ *slotStub) {
				m.cfg.Models = slices.DeleteFunc(slices.Clone(m.cfg.Models), func(x config.Model) bool { return x.ID == "openrouter/b" })
			},
			status: "slot sonnet: openrouter/b is not in the registry",
			events: "",
		},
		{
			name:    "a passthrough --model in its = form",
			arrange: func(m *model, _ *slotStub) { m.extraArgs = []string{"--model=opus"} },
			status:  "a slot profile sets --model itself: remove it from the arguments after --",
			events:  "",
		},
		{
			name: "a route that cannot be written",
			arrange: func(_ *model, s *slotStub) {
				s.ensureErr = &lifecycle.RouteEnsureError{ModelID: "openrouter/b", Err: errors.New("LiteLLM config not found")}
			},
			status: "slot sonnet: LiteLLM route for openrouter/b not written: LiteLLM config not found",
			events: ensured,
		},
		{
			name: "a proxy restart that fails",
			arrange: func(_ *model, s *slotStub) {
				s.ensureOwed, s.restartErr = true, errors.New("LiteLLM proxy not ready at http://localhost:4000: timed out")
			},
			status: "LiteLLM proxy not ready at http://localhost:4000: timed out",
			events: ensured + ",restart",
		},
		{
			name: "a proxy that does not answer",
			arrange: func(_ *model, s *slotStub) {
				s.probeErr = errors.New("the LiteLLM proxy does not answer at http://localhost:4000: connection refused")
			},
			status: "the LiteLLM proxy does not answer at http://localhost:4000: connection refused",
			events: ensured + ",probe",
		},
		{
			name:    "a command that cannot be built",
			arrange: func(_ *model, s *slotStub) { s.buildErr = errors.New("agent claude not installed") },
			status:  "launch failed: agent claude not installed",
			events:  ensured + ",probe,build",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := slotFlowFixture(t, idleOmlxSnapshot(), cloudProfile())
			stub := stubSlotSeams(t)
			stubSlotApplier(t, func(*exec.Cmd, string, catalog.Row) (func() error, error) {
				t.Error("the applier ran for a launch that was aborted")
				return nil, nil
			})
			c.arrange(&m, stub)

			preparing, cmd := enterProfileRow(t, m, "profile/cloud")
			back, after := updateMsg(preparing, slotStageFrom(t, cmd)())

			if back.phase != phaseModel || back.slot != nil {
				t.Fatalf("phase = %v slot = %v, want the picker with the flow ended", back.phase, back.slot)
			}
			if back.status != c.status {
				t.Errorf("status = %q, want %q", back.status, c.status)
			}
			if got := stub.got(); got != c.events {
				t.Errorf("events = %q, want %q", got, c.events)
			}
			// What comes back is the table refresh, never a run command.
			if after == nil {
				t.Fatal("no command: the picker must re-probe after an aborted launch")
			}
			if _, ok := after().(tableRefreshedMsg); !ok {
				t.Error("the command after an abort is not the table refresh")
			}
			assertNothingRecorded(t)
		})
	}
}

// TestSlotApplierErrorAbortsTheLaunch pins fail-closed at step 9: an applier
// error, or no applier at all, runs no agent, records nothing, and comes back
// as a slotAbortMsg that returns to the picker with the error. The automatic
// applier degrades to an unprofiled launch here; a slot profile must not.
func TestSlotApplierErrorAbortsTheLaunch(t *testing.T) {
	cases := []struct {
		name  string
		apply SlotProfileApplier
		want  string
	}{
		{"the applier fails", func(*exec.Cmd, string, catalog.Row) (func() error, error) {
			return nil, errors.New("wrapper little-coder not found on PATH")
		}, "wrapper little-coder not found on PATH"},
		{"no applier was handed in", nil, "no slot-profile applier"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := slotFlowFixture(t, idleOmlxSnapshot(), cloudProfile())
			stubSlotApplier(t, c.apply)
			sentinel := filepath.Join(t.TempDir(), "agent-ran")
			row := catalog.Row{Model: config.Model{ID: "profile/cloud"}, Profile: &catalog.ProfileRow{}}

			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			oldStderr := os.Stderr
			os.Stderr = w
			msg := runSlotAndWaitCmd(exec.Command("touch", sentinel), "claude", row)()
			os.Stderr = oldStderr
			_ = w.Close()
			stderr, _ := io.ReadAll(r)

			abort, ok := msg.(slotAbortMsg)
			if !ok || abort.err == nil || abort.err.Error() != c.want {
				t.Fatalf("message = %#v, want slotAbortMsg{%q}", msg, c.want)
			}
			if !strings.Contains(string(stderr), "wt: "+c.want) {
				t.Errorf("stderr = %q, want the error printed while the terminal is the user's", stderr)
			}
			if _, err := os.Stat(sentinel); err == nil {
				t.Error("the agent command ran after the applier failed")
			}
			assertNothingRecorded(t)
			if pendingSummary != "" || pendingSurveyState.agent != "" {
				t.Errorf("pending = %q %+v, want nothing: no session ran", pendingSummary, pendingSurveyState)
			}

			back, _ := updateMsg(m, msg)
			if back.phase != phaseModel || back.status != "launch failed: "+c.want {
				t.Errorf("phase = %v status = %q, want the picker with the apply error", back.phase, back.status)
			}
		})
	}
}

// TestCtrlCDuringSlotStageNeverLaunches is Review Focus 5. The user presses
// ctrl+c while the profile's route write is in flight. The stage runs on its
// own goroutine, as the tea runtime runs it, and Run()'s after-run flush
// settles the same stage from another. The flush announces the wait and
// returns only once the stage, its proxy restart included, has finished; the
// stage's message, delivered in the gap before QuitMsg, launches nothing; and
// no usage event, last-picked.state or refcount line exists afterwards.
// Without this wt would exit under a restart, or start an agent and record a
// session after the user quit.
func TestCtrlCDuringSlotStageNeverLaunches(t *testing.T) {
	const updated = "wt: LiteLLM route for openrouter/a updated\n"
	m := slotFlowFixture(t, idleOmlxSnapshot(), cloudProfile())
	stub := stubSlotSeams(t)
	stub.ensureOwed, stub.ensureOutput = true, updated
	stub.ensureEntered, stub.ensureRelease = make(chan struct{}), make(chan struct{})
	stubSlotApplier(t, func(*exec.Cmd, string, catalog.Row) (func() error, error) {
		t.Error("the applier ran after the user quit")
		return nil, nil
	})

	preparing, cmd := enterProfileRow(t, m, "profile/cloud")
	quitting, quit := updateMsg(preparing, tea.KeyMsg{Type: tea.KeyCtrlC})
	if preparing.slot.quitting {
		t.Fatal("ctrl+c wrote through to the model Update was given")
	}
	if quit == nil {
		t.Fatal("ctrl+c returned no command, want tea.Quit")
	}
	if _, ok := quit().(tea.QuitMsg); !ok {
		t.Fatal("ctrl+c did not quit")
	}
	stage := slotStageFrom(t, cmd)

	timeout := time.After(10 * time.Second)
	await := func(ch <-chan struct{}, what string) {
		t.Helper()
		select {
		case <-ch:
		case <-timeout:
			t.Fatalf("timed out waiting for %s", what)
		}
	}

	stageDone := make(chan struct{})
	var done tea.Msg
	go func() {
		defer close(stageDone)
		done = stage()
	}()
	await(stub.ensureEntered, "the stage to reach the route write")

	out := &signalWriter{first: make(chan struct{})}
	flushDone := make(chan struct{})
	go func() {
		defer close(flushDone)
		flushRouteNotesAfterRun(quitting, out)
	}()
	await(out.first, "the flush to announce the wait")
	select {
	case <-flushDone:
		t.Fatal("the flush returned while the route write was still in flight")
	case <-time.After(50 * time.Millisecond):
	}

	close(stub.ensureRelease)
	await(stageDone, "the stage")
	await(flushDone, "the flush")

	if got, want := out.String(), routeWaitingLine+updated; got != want {
		t.Fatalf("printed = %q, want %q", got, want)
	}
	if got, want := stub.got(), "ensure:openrouter/a,openrouter/b:owed=false,restart,probe"; got != want {
		t.Fatalf("events = %q, want %q: the stage runs once and finishes its restart", got, want)
	}

	final, launch := updateMsg(quitting, done)
	if launch != nil {
		t.Fatal("the stage's message after ctrl+c returned a command: the agent would start after the quit")
	}
	if final.phase != phaseSlot || final.slot == nil {
		t.Fatalf("phase = %v slot = %v, want the quitting flow left as it was", final.phase, final.slot)
	}
	if strings.Contains(stub.got(), "build") {
		t.Fatal("the agent command was built after the quit")
	}
	assertNothingRecorded(t)
}

// TestQuitDuringStartRowPrepareSettlesTheOwedRestart pins the other quit: a
// start row's first stage writes the cloud routes and leaves the restart to
// the start that follows. A quit before that start means nobody restarts, so
// the after-run flush must settle it, once. Without it config.yaml holds a
// route the proxy never loaded, and the next single-model launch of that
// model finds the row in place and gets "Invalid model name".
func TestQuitDuringStartRowPrepareSettlesTheOwedRestart(t *testing.T) {
	m := slotFlowFixture(t, idleOmlxSnapshot(), mixedProfile())
	stub := stubSlotSeams(t)
	stub.ensureOwed = true

	preparing, cmd := enterProfileRow(t, m, "profile/mixed")
	quitting, _ := updateMsg(preparing, tea.KeyMsg{Type: tea.KeyCtrlC})

	var out bytes.Buffer
	flushRouteNotesAfterRun(quitting, &out)
	// The command can still finish afterwards: it must not run the stage again.
	slotStageFrom(t, cmd)()

	if got, want := stub.got(), "ensure:openrouter/a:owed=false,settle"; got != want {
		t.Fatalf("events = %q, want %q", got, want)
	}
	if !strings.HasPrefix(out.String(), routeWaitingLine) {
		t.Errorf("printed = %q, want the wait announced", out.String())
	}
}

// TestSlotViewFitsTheTerminal pins the preparing screen at the three sizes
// the layout tests measure. Bubble Tea drops the top lines of a view that is
// too tall and cuts a line that is too wide, so a screen that does not fit
// loses the profile's name or the one key that works.
func TestSlotViewFitsTheTerminal(t *testing.T) {
	sizes := [][2]int{{80, 24}, {40, 12}, {120, 50}}
	for _, size := range sizes {
		stubInventory(t, idleOmlxSnapshot())
		stubSlotSeams(t)
		m := slotPicker(t, slotTestConfig(), []profiles.SlotCheck{cloudProfile()}, size[0], size[1])
		m.agent, m.selectedPath = "claude", t.TempDir()
		preparing, _ := enterProfileRow(t, m, "profile/cloud")
		if preparing.phase != phaseSlot {
			t.Fatalf("phase = %v, want phaseSlot", preparing.phase)
		}
		assertFits(t, "slot preparing", preparing.View(), size[0], size[1])
	}
}
```

The file still imports `themes` for the Part A test, and `signalWriter`, `routeWaitingLine`, `updateMsg`, `indexOfID`, `itemIDs`, `assertFits`, `stubInventory` and `stubRouteNotes` are existing helpers of this package. `idleOmlxSnapshot()` (the fixture's local model `omlx/qwen3.8` on disk and not running), `slotTestConfig`, `slotCheckFor` and `slotPicker` are Task 5's helpers in `wt/internal/tui/slot_row_test.go`. Do not declare any of them again here: this is one package, and a second declaration does not compile. Task 5's file also declares `slotRowChecks`, `slotTable`, `tableItem`, `slotLineRow` and `footerLine`, and Task 7's `slot_cursor_test.go` declares `slotCursorPicker`, `slotCursorChecks`, `markedIDs` and `record`; none of those names is free either.

- [ ] **Step 7: Run them and see them fail**

```bash
cd wt && go test ./internal/tui -run 'TestEnterOnProfileRow|TestProfileLaunchWithEveryRoute|TestEnterOnBlockedProfileRow|TestSlotStageFailures|TestSlotApplierError|TestCtrlCDuringSlotStage|TestQuitDuringStartRowPrepare|TestSlotViewFits'
```

Expected: the package does not compile.

```
internal/tui/slot_flow_test.go: undefined: ensureSlotRoutes
internal/tui/slot_flow_test.go: undefined: restartProxyNow
internal/tui/slot_flow_test.go: undefined: phaseSlot
internal/tui/slot_flow_test.go: undefined: runSlotAndWaitCmd
FAIL	github.com/ohanaverse/local-ai-setup/wt/internal/tui [build failed]
```

- [ ] **Step 8: Write the implementation**

Create `wt/internal/tui/slot_flow.go`:

```go
package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
	"github.com/ohanaverse/local-ai-setup/wt/internal/refcount"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rotation"
)

// Test seams of the slot-profile launch. Production writes config.yaml,
// restarts and probes the LiteLLM proxy and looks the agent binary up;
// TestMain makes each fail closed.
var (
	ensureSlotRoutes = lifecycle.EnsureModelRoutes
	restartProxyNow  = lifecycle.RestartProxy
	probeProxyNow    = lifecycle.ProbeProxy
	settleRoutesNow  = lifecycle.SettleRoutes
	buildSlotCmd     = agents.BuildSlotLaunchCmd
)

// slotResult is what one off-goroutine stage of a profile launch found.
type slotResult struct {
	row   catalog.Row // the profile row as re-resolved in step 1
	owed  bool        // a proxy restart is still owed
	err   error
	notes string // what the route writes printed
}

// slotFlow is a profile launch between Enter and the agent's start.
type slotFlow struct {
	// id is the run id; a message carrying another one is from a superseded
	// stage and is dropped, as routingState.id does for route checks.
	id   int
	item *modelItem  // the highlighted profile row
	row  catalog.Row // set once the first stage reported
	// owed says config.yaml was changed for this launch and the proxy has
	// not been restarted for it yet.
	owed  bool
	began time.Time
	// settle runs the stage in flight, at most once however many callers
	// there are: the stage's command calls it, and so does Run() when the
	// user quit before that command's message was handled. nil between
	// stages (while the local model starts, or on the replace dialog).
	settle func() slotResult
	// settled reports whether settle has finished. It is read from another
	// goroutine than the one settling, hence the atomic.
	settled *atomic.Bool
	// quitting is set once ctrl+c asked wt to quit. The stage's message can
	// still arrive before the QuitMsg; a quitting flow drops it, so no agent
	// starts and nothing is recorded after the user left.
	quitting bool
}

type slotPreparedMsg struct { // steps 1-3 done, and 5-7 for a launch row
	id  int
	res slotResult
}
type slotSettledMsg struct { // step 7 done after a start
	id  int
	res slotResult
}
type slotTickMsg struct{ id int }
type slotAbortMsg struct{ err error } // the applier failed after the terminal was released

func slotTick(id int) tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return slotTickMsg{id: id} })
}

// slotDeps are the seams one stage uses. They are read on the update
// goroutine and handed to the command, so a test's seam swap cannot race it.
type slotDeps struct {
	inventory func(*config.Config) localmodels.Snapshot
	ensure    func(context.Context, io.Writer, *config.Config, []config.Model, bool) (bool, error)
	restart   func(context.Context, *config.Config) error
	probe     func(context.Context, *config.Config) error
	wait      func()
}

// slotRouteError words a route failure the way the non-TUI path does: it
// names the slot whose model could not be routed.
func slotRouteError(p *catalog.ProfileRow, err error) error {
	var re *lifecycle.RouteEnsureError
	if errors.As(err, &re) {
		return fmt.Errorf("slot %s: %w", p.SlotOf(re.ModelID), err)
	}
	return err
}

// prepareSlot is the first stage: steps 1 to 3 of the launch flow and, for a
// row that needs no start, steps 5 to 7. It runs in a command; nothing in it
// touches the model.
//
//  1. Re-resolve the profile against a fresh probe.
//  2. Refuse a passthrough flag that would replace the slot mapping.
//  3. Write the cloud slots' missing routes, restart deferred.
//  5. Write the local model's route when it is already running.
//  6. Restart the proxy once if a write changed config.yaml.
//  7. Probe the proxy.
//
// A start row stops after step 3: the start that follows writes the local
// route and restarts the proxy itself, and is told a restart is owed.
func prepareSlot(d slotDeps, cfg *config.Config, agent string, extraArgs []string, check profiles.SlotCheck) slotResult {
	ctx := context.Background()
	buf := &bytes.Buffer{}
	inv := d.inventory(cfg)
	row := catalog.ResolveSlotProfile(cfg, &inv, check)
	res := slotResult{row: row}
	if row.Action() == catalog.ActionBlock {
		res.err = errors.New(row.BlockReason())
		return res
	}
	if err := agents.CheckSlotPassthrough(agent, extraArgs); err != nil {
		res.err = err
		return res
	}
	p := row.Profile
	routeFailed := func(err error) slotResult {
		// The ensure settled what was owed with an asynchronous restart,
		// which prints its warnings into buf until the wait returns.
		d.wait()
		res.owed, res.err, res.notes = false, slotRouteError(p, err), buf.String()
		return res
	}
	owed, err := d.ensure(ctx, buf, cfg, p.Cloud(), false)
	if err != nil {
		return routeFailed(err)
	}
	if row.Action() == catalog.ActionStart {
		res.owed, res.notes = owed, buf.String()
		return res
	}
	if p.Local != nil {
		if owed, err = d.ensure(ctx, buf, cfg, []config.Model{p.Local.Model}, owed); err != nil {
			return routeFailed(err)
		}
	}
	res.notes = buf.String()
	if owed {
		// Whether it succeeds or not, the restart was made: nothing is owed.
		if err := d.restart(ctx, cfg); err != nil {
			res.err = err
			return res
		}
	}
	if err := d.probe(ctx, cfg); err != nil {
		res.err = err
	}
	return res
}

// runSlotStage puts st in flight behind phaseSlot with stage as its work. The
// stage is wrapped the way checkLaunchRoute wraps its wait: once-only, so the
// command and Run()'s after-quit settle share one run and one result.
func (m model) runSlotStage(st slotFlow, stage func() slotResult, wrap func(id int, res slotResult) tea.Msg) (model, tea.Cmd) {
	var once sync.Once
	var res slotResult
	settled := &atomic.Bool{}
	settle := func() slotResult {
		once.Do(func() {
			res = stage()
			settled.Store(true)
		})
		return res
	}
	m.slotRun++
	id := m.slotRun
	st.id, st.settle, st.settled, st.quitting = id, settle, settled, false
	m.slot = &st
	m.phase = phaseSlot
	m.status = ""
	return m, tea.Batch(func() tea.Msg { return wrap(id, settle()) }, slotTick(id))
}

// beginSlotLaunch starts the launch of the profile row it. The row must not
// be blocked; its callers show a blocked row's reason instead.
func (m model) beginSlotLaunch(it *modelItem) (model, tea.Cmd) {
	if it == nil || it.profile == nil {
		return m, nil
	}
	d := slotDeps{inventory: runInventory, ensure: ensureSlotRoutes, restart: restartProxyNow, probe: probeProxyNow, wait: waitPendingRoutes}
	cfg, agent, extraArgs, check := m.cfg, m.agent, m.extraArgs, it.profile.Check
	// Any table refresh still in flight predates this launch.
	m.refreshGen++
	return m.runSlotStage(slotFlow{item: it, began: time.Now()},
		func() slotResult { return prepareSlot(d, cfg, agent, extraArgs, check) },
		func(id int, res slotResult) tea.Msg { return slotPreparedMsg{id: id, res: res} })
}

// settleSlotAfterStart is the stage after the profile's local model started.
// The start wrote the model's route and restarted the proxy, which published
// the cloud routes too, so nothing is owed; what is left is step 7, the probe.
func (m model) settleSlotAfterStart() (model, tea.Cmd) {
	probe, cfg := probeProxyNow, m.cfg
	st := *m.slot
	st.owed = false
	row := st.row
	return m.runSlotStage(st,
		func() slotResult { return slotResult{row: row, err: probe(context.Background(), cfg)} },
		func(id int, res slotResult) tea.Msg { return slotSettledMsg{id: id, res: res} })
}

// slotStageDone takes a finished stage's result into the flow: the row, what
// is owed, and the stage's notes, recorded once. It replaces the state with a
// copy, because Update must leave the model it was given as it found it.
func (m model) slotStageDone(res slotResult) model {
	st := *m.slot
	st.row, st.owed = res.row, res.owed
	st.settle, st.settled = nil, nil
	m.slot = &st
	m.recordRouteNotes(res.notes)
	return m
}

// handleSlotMsg processes the slot flow's messages. A message whose id is not
// the flow's, that arrives outside phaseSlot, or that belongs to a flow the
// user quit out of is dropped: acting on it would start an agent nobody asked
// for.
func (m model) handleSlotMsg(msg tea.Msg) (model, tea.Cmd) {
	live := func(id int) bool {
		return m.slot != nil && m.phase == phaseSlot && id == m.slot.id && !m.slot.quitting
	}
	switch msg := msg.(type) {
	case slotTickMsg:
		if !live(msg.id) {
			return m, nil
		}
		return m, slotTick(msg.id)
	case slotPreparedMsg:
		if !live(msg.id) {
			return m, nil
		}
		m = m.slotStageDone(msg.res)
		if msg.res.err != nil {
			return m.abortSlot(msg.res.err.Error())
		}
		if msg.res.row.Action() != catalog.ActionStart {
			return m.launchSlot()
		}
		// Step 4: the existing start flow, for the profile's one local model.
		p := msg.res.row.Profile
		if p == nil || p.Local == nil {
			return m.abortSlot(msg.res.row.Model.ID + " has no local model to start")
		}
		local := p.Local.Model
		// --replace is the -M pin's permission, as for a model row.
		allow := m.allowReplace && m.slot.item.model.ID == m.pinnedModel
		return m.beginStartOwed(&modelItem{model: local, line: local.ID}, allow, msg.res.owed)
	case slotSettledMsg:
		if !live(msg.id) {
			return m, nil
		}
		m = m.slotStageDone(msg.res)
		if msg.res.err != nil {
			return m.abortSlot(msg.res.err.Error())
		}
		return m.launchSlot()
	}
	return m, nil
}

// handleSlotKey handles keys in phaseSlot by handleRouteKey's rule: the
// routes are being written and a proxy restart cannot be called off, so
// ctrl+c quits wt (Run() settles the stage on the way out) and every other
// key is ignored.
func (m model) handleSlotKey(msg tea.KeyMsg) (model, tea.Cmd) {
	if msg.String() == "ctrl+c" {
		// A copy, not a write through the pointer. settle and settled are
		// shared by the copy, which is what keeps them once-only.
		quitting := *m.slot
		quitting.quitting = true
		m.slot = &quitting
		return m, tea.Quit
	}
	return m, nil
}

// slotView is phaseSlot's screen. It names no cancel key because there is
// none: see handleSlotKey.
func (m model) slotView() string {
	if m.slot == nil {
		return ""
	}
	elapsed := time.Since(m.slot.began).Round(time.Second)
	return fmt.Sprintf("Preparing %s — updating LiteLLM routes (%s)\n\n[ctrl+c] quit wt", m.slot.item.model.ID, elapsed)
}

// abortSlot ends the flow with no launch: back to the picker with reason as
// its status, over a re-probed table.
func (m model) abortSlot(reason string) (model, tea.Cmd) {
	m.slot = nil
	m.status = reason
	m.phase = phaseModel
	return m.refreshTable()
}

// dropSlot ends a flow the user backed out of on the replace dialog. A
// restart still owed by the cloud-route write is settled here, so the written
// routes go live; SettleRoutes only starts the restart, so this does not wait.
func (m model) dropSlot() model {
	if m.slot == nil {
		return m
	}
	if m.slot.owed {
		settleRoutesNow(context.Background(), m.cfg)
	}
	m.slot = nil
	return m
}

// launchSlot is steps 8 to 10: build the command through the driver's slot
// build and hand it to runSlotAndWaitCmd. It runs on the update goroutine and
// waits for nothing.
func (m model) launchSlot() (model, tea.Cmd) {
	row := m.slot.row
	cmd, err := buildSlotCmd(row.Profile.Launch(), m.selectedPath, m.yolo, m.cfg, m.extraArgs)
	if err != nil {
		return m.abortSlot("launch failed: " + err.Error())
	}
	queueLaunchNotes(m.agent, cmd, m.extraArgs)
	m.slot = nil
	// Back to the picker's phase first, as handleRouteMsg does: View can be
	// called once more before the terminal is released, and every way the
	// run command can fail returns to the model picker.
	m.phase = phaseModel
	return m, runSlotAndWaitCmd(cmd, m.agent, row)
}

// runSlotAndWaitCmd is runAndWaitCmd for a slot-profile launch. It releases
// the terminal, prints the route notes, applies the profile's args and
// wrapper through slotApplier, records the launch, runs the agent and
// captures the summary. It never calls profileApplier: an automatic profile
// is not applied to a slot-profile launch. An applier error, or no applier,
// runs nothing and records nothing, and comes back as a slotAbortMsg.
func runSlotAndWaitCmd(cmd *exec.Cmd, agent string, row catalog.Row) tea.Cmd {
	return func() tea.Msg {
		if currentProgram != nil {
			currentProgram.ReleaseTerminal()
			defer func() {
				if currentProgram != nil {
					_ = currentProgram.RestoreTerminal()
				}
			}()
		}
		flushRouteNotes(os.Stderr)
		var cleanup func() error
		err := errors.New("no slot-profile applier")
		if slotApplier != nil {
			cleanup, err = slotApplier(cmd, agent, row)
		}
		if err != nil {
			// The terminal is released here, so this reaches the user's
			// stderr; the picker's status repeats it.
			fmt.Fprintf(os.Stderr, "wt: %v\n", err)
			return slotAbortMsg{err: err}
		}
		if cleanup == nil {
			cleanup = func() error { return nil }
		}
		// The commit point: the applier succeeded and the agent runs next.
		// Both writes are best-effort, as launchAndRecord's are.
		if err := rotation.New().RecordProfile(agent, row.Model.ID); err != nil {
			fmt.Fprintf(os.Stderr, "note: last-picked state not saved: %v\n", err)
		}
		var ids []string
		for _, sm := range row.Profile.Distinct() {
			ids = append(ids, sm.ID)
		}
		if err := refcount.NewStore().RecordAll(os.Getpid(), ids); err != nil {
			fmt.Fprintf(os.Stderr, "note: refcount state not saved: %v\n", err)
		}
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		duration, runErr := agents.RunAndCleanup(cmd, cleanup)
		shown := config.Model{ID: row.Model.ID}
		pendingSummary = agents.Summary(agent, shown, duration)
		pendingSurveyState = pendingSurvey{agent: agent, m: shown, profile: true}
		return launchDoneMsg{err: runErr}
	}
}

// settleQuitSlot is the slot flow's part of flushRouteNotesAfterRun: what wt
// still owes a profile launch the user quit out of, once the alt screen is
// gone. A stage in flight is settled first, announced when there is really
// something to wait for, and its notes join the pending ones. A restart that
// is then still owed is started: by a start row's first stage, which left it
// to a start that will not happen now, or on the replace dialog. main() waits
// for that restart before the process exits. While the local model is
// starting the engine holds the owed restart, and the start's own settle
// covers it.
func settleQuitSlot(fm model, w io.Writer) {
	st := fm.slot
	if st == nil {
		return
	}
	owed := st.owed
	switch {
	case st.settle != nil:
		if !st.settled.Load() {
			fmt.Fprintln(w, "wt: waiting for the LiteLLM proxy restart…")
		}
		res := st.settle()
		pendingRouteNotes += res.notes
		owed = res.owed
	case fm.phase != phaseReplaceConfirm:
		return
	}
	if owed {
		settleRoutesNow(context.Background(), fm.cfg)
	}
}
```

In `wt/internal/tui/start_flow.go`, give the start flow its owed-restart parameter. Replace the signature of `runStart` and the engine call inside it:

```go
func runStart(ctx context.Context, cfg *config.Config, t lifecycle.Target, allow, restartOwed bool, id int) (<-chan tea.Msg, *startOutput) {
```

```go
		err := start(ctx, cfg, t, lifecycle.Options{AllowReplace: allow, Progress: stage, Out: out, RestartOwed: restartOwed})
```

Replace `beginStart` with:

```go
// beginStart starts it through the engine and enters phaseStarting.
func (m model) beginStart(it *modelItem, allowReplace bool) (model, tea.Cmd) {
	return m.beginStartOwed(it, allowReplace, false)
}

// beginStartOwed is beginStart for a caller that already owes a proxy
// restart: a profile launch whose cloud-route write changed config.yaml. The
// engine's one restart then publishes that write too (Options.RestartOwed).
func (m model) beginStartOwed(it *modelItem, allowReplace, restartOwed bool) (model, tea.Cmd) {
	ctx, cancel := context.WithCancel(context.Background())
	m.startRun++
	// Any refresh still in flight predates this start; finishStart issues its own.
	m.refreshGen++
	id := m.startRun
	ch, out := runStart(ctx, m.cfg, lifecycle.Target{ProviderID: it.model.ProviderID, ModelName: it.model.ModelName, ModelID: it.model.ID}, allowReplace, restartOwed, id)
	m.start = &startState{id: id, item: it, began: time.Now(), cancel: cancel, ch: ch, out: out}
	m.phase = phaseStarting
	m.status = ""
	return m, tea.Batch(waitForStart(ch), startTick(id))
}
```

In `wt/internal/tui/app.go`:

Add the phase, last in the `const` block, after the `phaseRouting` line:

```go
	phaseSlot                        // a slot-profile launch is writing its routes, restarting or probing the proxy
```

Add two fields to `model`, after the `routeRun int` line:

```go

	// slot-profile launch: Enter on a profile row runs the launch flow's
	// route writes, restart and probe behind phaseSlot. The flow is kept
	// while the profile's local model starts and on the replace dialog.
	slot    *slotFlow // in-flight profile launch; nil when idle
	slotRun int       // monotonically increasing run id; stale messages are dropped by id
```

In `update`, add two cases after the `case routeDoneMsg, routeTickMsg:` case:

```go
	case slotPreparedMsg, slotSettledMsg, slotTickMsg:
		// Likewise exactly the set handleSlotMsg handles.
		return m.handleSlotMsg(msg)
	case slotAbortMsg:
		// The slot applier failed after the terminal was released: no agent
		// ran and nothing was recorded.
		m.status = "launch failed: " + msg.err.Error()
		m.phase = phaseModel
		return m.refreshTable()
```

In the `case tea.KeyMsg:` branch, add after the `if m.phase == phaseRouting { return m.handleRouteKey(msg) }` block:

```go
		// And a profile launch's route stage, for the same reason.
		if m.phase == phaseSlot && m.slot != nil {
			return m.handleSlotKey(msg)
		}
```

In the Enter handler's `case phaseModel:`, add before the `if highlighted.start {` line:

```go
				if highlighted.profile != nil && highlighted.blocked == "" {
					// A slot profile is its own launch unit: its routes, its
					// local model's start and its command all come from the
					// profile, never from this row's id-only Model. A blocked
					// profile falls through to the reason below.
					return m.beginSlotLaunch(highlighted)
				}
```

In `View`, add after the `if m.phase == phaseRouting { … }` block:

```go
	if m.phase == phaseSlot {
		if m.width <= 0 || m.height <= 0 {
			return "slot profile launch (waiting for window size)"
		}
		return clip(m.slotView(), m.width)
	}
```

In `proceedToLaunch`, add at the top of the function body, before the comment `// The highlighted list item is what gets launched, regardless`:

```go
	// A guard, not a path: Enter and the pin send a profile row to
	// beginSlotLaunch themselves. Anything else that lands here with a
	// profile highlighted must not go on to ResolveRoute and the single-model
	// launch with the row's id-only Model.
	if it, ok := m.models.SelectedItem().(*modelItem); ok && it.profile != nil {
		if it.blocked != "" {
			m.status = it.blocked
			m.phase = phaseModel
			return m, nil
		}
		return m.beginSlotLaunch(it)
	}
```

In `flushRouteNotesAfterRun`, add before the `flushRouteNotes(w)` line:

```go
	if fm, ok := final.(model); ok {
		settleQuitSlot(fm, w)
	}
```

In `wt/internal/tui/testhelpers_test.go`, add `"os/exec"` and `"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"` to the imports, and add to `TestMain`, after the `routesPending = func() bool { return false }` line:

```go
	// The slot-profile launch: unstubbed, Enter on a profile row would
	// rewrite the developer's real config.yaml, restart their proxy and look
	// the agent binary up. Each seam fails closed; tests that reach the flow
	// call stubSlotSeams.
	ensureSlotRoutes = func(context.Context, io.Writer, *config.Config, []config.Model, bool) (bool, error) {
		return false, errors.New("ensureSlotRoutes not stubbed in this test")
	}
	restartProxyNow = func(context.Context, *config.Config) error {
		return errors.New("restartProxyNow not stubbed in this test")
	}
	probeProxyNow = func(context.Context, *config.Config) error {
		return errors.New("probeProxyNow not stubbed in this test")
	}
	settleRoutesNow = func(context.Context, *config.Config) {}
	buildSlotCmd = func(profiles.SlotLaunch, string, bool, *config.Config, []string) (*exec.Cmd, error) {
		return nil, errors.New("buildSlotCmd not stubbed in this test")
	}
```

- [ ] **Step 9: Run the tests and see them pass**

```bash
cd wt && gofmt -l internal/tui && go vet ./internal/tui && go test -race ./internal/tui -run 'TestEnterOnProfileRow|TestProfileLaunchWithEveryRoute|TestEnterOnBlockedProfileRow|TestSlotStageFailures|TestSlotApplierError|TestCtrlCDuringSlotStage|TestQuitDuringStartRowPrepare|TestSlotViewFits|TestProfilePostExit|TestWithSlotProfiles' && go test ./internal/tui
```

Expected: `gofmt` and `vet` print nothing; both test runs end in `ok  	github.com/ohanaverse/local-ai-setup/wt/internal/tui`. The race run must report no `DATA RACE`.

- [ ] **Step 10: Commit**

```bash
git add wt/internal/tui/slot_flow.go wt/internal/tui/slot_flow_test.go wt/internal/tui/start_flow.go wt/internal/tui/app.go wt/internal/tui/testhelpers_test.go
git commit -m "feat(wt): Enter on a slot-profile row writes its routes off the update goroutine, then launches or returns with the reason"
```

---

#### Part C: the start row

A profile whose local model is not running starts it through the existing start flow, replace dialog included, then probes the proxy and launches. The start is told a restart is owed when the cloud-route write changed `config.yaml`, so the whole launch costs one restart.

- [ ] **Step 11: Write the failing tests**

Append to `wt/internal/tui/slot_flow_test.go`:

```go
// mixedStarting drives a mixed profile to the point where its local model's
// start is in flight: Enter, then the first stage's message. owed scripts
// whether the cloud-route write changed config.yaml.
func mixedStarting(t *testing.T, owed bool) (model, *slotStub) {
	t.Helper()
	m := slotFlowFixture(t, idleOmlxSnapshot(), mixedProfile())
	stub := stubSlotSeams(t)
	stub.ensureOwed = owed
	preparing, cmd := enterProfileRow(t, m, "profile/mixed")
	starting, _ := updateMsg(preparing, slotStageFrom(t, cmd)())
	if starting.phase != phaseStarting || starting.start == nil || starting.slot == nil {
		t.Fatalf("phase = %v start = %v slot = %v, want the local model's start in flight with the flow kept", starting.phase, starting.start, starting.slot)
	}
	return starting, stub
}

var omlxTarget = lifecycle.Target{ProviderID: "omlx", ModelName: "qwen3.8", ModelID: "omlx/qwen3.8"}

// TestStartProfileRowStartsItsLocalModelThenLaunches pins success criteria 4
// and 5 for the picker: a profile whose local model is idle starts it through
// the existing start flow, tells the engine a restart is owed for the cloud
// route it just wrote, then probes and launches. The flow itself restarts
// nothing, so the launch costs the start's one restart. Without RestartOwed
// the cloud route would stay unpublished and its slot would fail at the proxy.
func TestStartProfileRowStartsItsLocalModelThenLaunches(t *testing.T) {
	calls := stubStartModel(t, func(int, context.Context, lifecycle.Target, lifecycle.Options) error { return nil })
	starting, stub := mixedStarting(t, true)
	stubSlotApplier(t, func(*exec.Cmd, string, catalog.Row) (func() error, error) {
		return func() error { return nil }, nil
	})

	waitStartCalls(t, calls, 1)
	c := calls.at(0)
	if c.target != omlxTarget {
		t.Errorf("start target = %+v, want the profile's local model", c.target)
	}
	if !c.opts.RestartOwed || c.opts.AllowReplace {
		t.Errorf("start options = %+v, want RestartOwed and no AllowReplace", c.opts)
	}

	settling, cmd := updateMsg(starting, recvStart(t, starting))
	if settling.phase != phaseSlot || settling.slot == nil || settling.slot.owed {
		t.Fatalf("phase = %v slot = %+v, want the probe stage with nothing owed", settling.phase, settling.slot)
	}
	launching, launch := updateMsg(settling, slotStageFrom(t, cmd)())

	if got, want := stub.got(), "ensure:openrouter/a:owed=false,probe,build"; got != want {
		t.Fatalf("events = %q, want %q (no restart of the flow's own, no second route write)", got, want)
	}
	if launching.slot != nil || launch == nil {
		t.Fatal("the flow did not end in a run command")
	}
	if _, ok := launch().(launchDoneMsg); !ok {
		t.Fatal("the run command did not return launchDoneMsg")
	}
	counts := refcount.NewStore().Counts([]string{"openrouter/a", "omlx/qwen3.8"})
	if counts["openrouter/a"] != 1 || counts["omlx/qwen3.8"] != 1 {
		t.Errorf("refcounts = %v, want one line per distinct slot model, the local one included", counts)
	}
}

// TestProfileStartRefusedKeepsTheOwedRestart pins the replace dialog for a
// profile's start. A refusal touched nothing, so the owed restart is kept,
// not spent: "Replace and start" passes it on to the second attempt, and
// every way of backing out (Cancel, esc, quitting wt) settles it exactly
// once. Spending it at the refusal would restart twice; dropping it would
// leave the written cloud route unpublished.
func TestProfileStartRefusedKeepsTheOwedRestart(t *testing.T) {
	refused := func(int, context.Context, lifecycle.Target, lifecycle.Options) error {
		return &lifecycle.OccupiedError{Occupants: []localmodels.Entry{{ModelID: "omlx/old"}}}
	}
	dialog := func(t *testing.T, owed bool) (model, *slotStub, *startCalls) {
		t.Helper()
		calls := stubStartModel(t, refused)
		starting, stub := mixedStarting(t, owed)
		got, _ := updateMsg(starting, recvStart(t, starting))
		if got.phase != phaseReplaceConfirm || got.slot == nil || got.slot.owed != owed {
			t.Fatalf("phase = %v slot = %+v, want the replace dialog with the flow kept", got.phase, got.slot)
		}
		if strings.Contains(stub.got(), "settle") {
			t.Fatal("the owed restart was settled at the refusal")
		}
		return got, stub, calls
	}

	t.Run("replace and start passes the owed restart on", func(t *testing.T) {
		got, stub, calls := dialog(t, true)
		got.replace.choices.Select(1)
		next, _ := updateMsg(got, tea.KeyMsg{Type: tea.KeyEnter})
		if next.start != nil && next.start.cancel != nil {
			t.Cleanup(next.start.cancel)
		}
		waitStartCalls(t, calls, 2)
		if c := calls.at(1); !c.opts.RestartOwed || !c.opts.AllowReplace || c.target != omlxTarget {
			t.Errorf("second start = %+v, want AllowReplace and RestartOwed for the same model", c)
		}
		if next.phase != phaseStarting || next.slot == nil {
			t.Errorf("phase = %v slot = %v, want the start in flight with the flow kept", next.phase, next.slot)
		}
		if strings.Contains(stub.got(), "settle") {
			t.Error("the owed restart was settled although the retry carries it")
		}
	})

	backOut := map[string]func(m model) model{
		"cancel": func(m model) model {
			next, _ := updateMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // Cancel is the default choice
			return next
		},
		"esc": func(m model) model {
			next, _ := updateMsg(m, tea.KeyMsg{Type: tea.KeyEsc})
			return next
		},
	}
	for name, leave := range backOut {
		t.Run(name+" settles it once", func(t *testing.T) {
			got, stub, calls := dialog(t, true)
			next := leave(got)
			if next.phase != phaseModel || next.slot != nil {
				t.Fatalf("phase = %v slot = %v, want the picker with the flow ended", next.phase, next.slot)
			}
			if got := stub.got(); got != "ensure:openrouter/a:owed=false,settle" {
				t.Errorf("events = %q, want exactly one settle", got)
			}
			if calls.len() != 1 {
				t.Errorf("startModel calls = %d, want 1", calls.len())
			}
			assertNothingRecorded(t)
		})
	}

	t.Run("quitting wt on the dialog settles it once", func(t *testing.T) {
		got, stub, _ := dialog(t, true)
		var out bytes.Buffer
		flushRouteNotesAfterRun(got, &out)
		if got := stub.got(); got != "ensure:openrouter/a:owed=false,settle" {
			t.Errorf("events = %q, want exactly one settle", got)
		}
	})

	t.Run("nothing owed, nothing settled", func(t *testing.T) {
		got, stub, _ := dialog(t, false)
		next, _ := updateMsg(got, tea.KeyMsg{Type: tea.KeyEnter})
		if next.slot != nil || strings.Contains(stub.got(), "settle") {
			t.Errorf("slot = %v events = %q, want the flow ended and no restart", next.slot, stub.got())
		}
	})
}

// TestProfileStartFailureReturnsToThePicker pins the failure table's start
// row: a start that fails returns to the picker with the start flow's own
// error, ends the profile launch, builds no command and records nothing. The
// engine settled the owed restart itself, so the flow must not start another.
func TestProfileStartFailureReturnsToThePicker(t *testing.T) {
	stubStartModel(t, func(int, context.Context, lifecycle.Target, lifecycle.Options) error {
		return errors.New("omlx refused the load")
	})
	starting, stub := mixedStarting(t, true)

	back, _ := updateMsg(starting, recvStart(t, starting))

	if back.phase != phaseModel || back.slot != nil {
		t.Fatalf("phase = %v slot = %v, want the picker with the flow ended", back.phase, back.slot)
	}
	if want := "failed to start omlx/qwen3.8: omlx refused the load"; back.status != want {
		t.Errorf("status = %q, want the start flow's own error, %q", back.status, want)
	}
	if got := stub.got(); got != "ensure:openrouter/a:owed=false" {
		t.Errorf("events = %q, want no settle, probe or build after a failed start", got)
	}
	assertNothingRecorded(t)
}
```

- [ ] **Step 12: Run them and see them fail**

```bash
cd wt && go test ./internal/tui -run 'TestStartProfileRowStartsItsLocalModel|TestProfileStartRefused|TestProfileStartFailure'
```

Expected: the code compiles and all three tests fail on an assertion. A start that succeeded still goes to `proceedToLaunch`, whose guard begins the profile launch a second time, and neither the replace dialog nor a failed start knows of the flow.

```
--- FAIL: TestStartProfileRowStartsItsLocalModelThenLaunches
    slot_flow_test.go: events = "ensure:openrouter/a:owed=false,ensure:openrouter/a:owed=false", want "ensure:openrouter/a:owed=false,probe,build" (no restart of the flow's own, no second route write)
--- FAIL: TestProfileStartRefusedKeepsTheOwedRestart
    slot_flow_test.go: second start = …, want AllowReplace and RestartOwed for the same model
    slot_flow_test.go: phase = … slot = 0x…, want the picker with the flow ended
--- FAIL: TestProfileStartFailureReturnsToThePicker
    slot_flow_test.go: phase = … slot = 0x…, want the picker with the flow ended
FAIL
```

- [ ] **Step 13: Write the implementation**

In `wt/internal/tui/start_flow.go`, `finishStart`.

Replace the opening of the `back` closure. The line `back := func(status string) (model, tea.Cmd) {` is followed by `m.status = withNote(note, status)`; insert between them:

```go
		if m.slot != nil {
			// This start belonged to a profile launch, which ends here. The
			// engine settled the owed restart on every error but a refusal;
			// a refusal can only reach this way out under a cancel.
			if m.slot.owed && lifecycle.RestartStillOwed(msg.err) {
				settleRoutesNow(context.Background(), m.cfg)
			}
			m.slot = nil
		}
```

In the success branch, the line `m.phase = phaseModel` directly under `if msg.err == nil {` is followed by a long comment and `if note != "" {`. Insert directly after `m.phase = phaseModel`:

```go
		if m.slot != nil {
			// The started model is a profile's local model. Its start wrote
			// the route and restarted the proxy, which published the profile's
			// cloud routes with it. The launch that follows is the profile's,
			// never proceedToLaunch's single-model one: probe, then build.
			if note != "" {
				m.recordRouteNotes(msg.out)
			}
			return m.settleSlotAfterStart()
		}
```

The refusal branch at the end of `finishStart` needs no change: it opens the dialog and leaves `m.slot` as it is.

In `wt/internal/tui/app.go`, the replace dialog.

Replace the `case phaseReplaceConfirm:` branch of the Enter handler:

```go
			case phaseReplaceConfirm:
				if item, ok := m.replace.choices.SelectedItem().(choiceItem); ok {
					switch item.choice {
					case replaceCancelChoice:
						// A profile launch that got this far ends here; a
						// restart it still owes is settled (dropSlot).
						m = m.dropSlot()
						m.phase = phaseModel
						return m, nil
					case replaceProceedChoice:
						if m.slot != nil {
							return m.beginStartOwed(m.replace.item, true, m.slot.owed)
						}
						return m.beginStart(m.replace.item, true)
					}
				}
```

In the `case "esc":` branch, replace the block that handles the two dialogs:

```go
			if m.phase == phaseOllamaWarn || m.phase == phaseReplaceConfirm {
				if m.phase == phaseReplaceConfirm {
					m = m.dropSlot()
				}
				m.phase = phaseModel
				m.status = ""
				return m, nil
			}
```

- [ ] **Step 14: Run the tests and see them pass**

```bash
cd wt && gofmt -l internal/tui && go test -race ./internal/tui -run 'TestStartProfileRowStartsItsLocalModel|TestProfileStartRefused|TestProfileStartFailure' && go test ./internal/tui
```

Expected: `gofmt` prints nothing; both runs end in `ok`. Every existing start-flow test still passes: with `m.slot == nil` none of the new branches is taken.

- [ ] **Step 15: Commit**

```bash
git add wt/internal/tui/start_flow.go wt/internal/tui/app.go wt/internal/tui/slot_flow_test.go
git commit -m "feat(wt): a slot profile's idle local model starts through the start flow, which carries the owed proxy restart"
```

---

#### Part D: the `-M profile/<name>` pin in the TUI

The TUI handles a pin when the worktree or the agent still had to be picked. The pin reads the same row the picker shows. A usable profile begins its launch at once. A blocked one returns to the agent picker with the reason, as a blocked model pin does.

- [ ] **Step 16: Write the failing tests**

Append to `wt/internal/tui/slot_flow_test.go`:

```go
// pinnedProfile enters the claude model phase with a -M pin and the given
// profiles, the way the TUI does after the agent was picked.
func pinnedProfile(t *testing.T, pin string, allowReplace bool, checks ...profiles.SlotCheck) (model, tea.Cmd) {
	t.Helper()
	tempStateDir(t)
	stubUsageStore(t)
	stubRefcountStore(t)
	stubRouteNotes(t)
	stubInventory(t, idleOmlxSnapshot())
	cfg := slotTestConfig()
	m := model{cfg: cfg, agent: "claude", pinnedModel: pin, allowReplace: allowReplace, slotChecks: checks, selectedPath: t.TempDir(), width: 80, height: 24}
	models, _ := cfg.EligibleModels("claude", "", "")
	return m.enterModelPhase("claude", models, "code")
}

// TestPinOnProfileBeginsItsLaunch pins `-M profile/<name>` in the TUI: the
// pin and the picker read the same row, and a usable profile begins its
// launch without waiting for Enter, as a pinned model does. --replace is the
// pin's permission to stop a running occupant when the profile's local model
// has to start. Without the pin branch the profile's id-only row would go to
// the single-model launch and fail with "unknown provider".
func TestPinOnProfileBeginsItsLaunch(t *testing.T) {
	t.Run("a launch row", func(t *testing.T) {
		stub := stubSlotSeams(t)
		got, cmd := pinnedProfile(t, "profile/cloud", false, cloudProfile(), mixedProfile())
		if got.phase != phaseSlot || got.slot == nil || got.slot.item.model.ID != "profile/cloud" {
			t.Fatalf("phase = %v slot = %+v, want the pinned profile's launch in flight", got.phase, got.slot)
		}
		if selectedModelID(got) != "profile/cloud" {
			t.Errorf("cursor on %q, want the pinned row, so a failed launch returns to it", selectedModelID(got))
		}
		if events := stub.got(); events != "" {
			t.Errorf("events = %q, want none before the command runs", events)
		}
		updateMsg(got, slotStageFrom(t, cmd)())
		if want := "ensure:openrouter/a,openrouter/b:owed=false,probe,build"; stub.got() != want {
			t.Errorf("events = %q, want %q", stub.got(), want)
		}
	})

	t.Run("a start row under --replace", func(t *testing.T) {
		stubSlotSeams(t)
		calls := stubStartModel(t, func(int, context.Context, lifecycle.Target, lifecycle.Options) error { return nil })
		got, cmd := pinnedProfile(t, "profile/mixed", true, cloudProfile(), mixedProfile())
		starting, _ := updateMsg(got, slotStageFrom(t, cmd)())
		if starting.start != nil && starting.start.cancel != nil {
			t.Cleanup(starting.start.cancel)
		}
		waitStartCalls(t, calls, 1)
		if c := calls.at(0); !c.opts.AllowReplace || c.target != omlxTarget {
			t.Errorf("start = %+v, want AllowReplace for the pinned profile's local model", c)
		}
	})
}

// TestPinOnUnusableProfileRoutesBackToTheAgentPicker pins the rejected pin:
// a blocked profile returns to the agent picker with the row's reason, and a
// profile that has no row for this agent gets the existing "not in the
// eligible list" message. Either way the pin is cleared and nothing is
// written or probed.
func TestPinOnUnusableProfileRoutesBackToTheAgentPicker(t *testing.T) {
	broken := slotCheckFor(0, "broken", "opus", map[string]string{"opus": "openrouter/gone"})
	cases := []struct {
		name, pin, status string
	}{
		{"a blocked profile", "profile/broken", "slot opus: openrouter/gone is not in the registry"},
		{"a profile with no row", "profile/nope", `model "profile/nope" is not in the eligible list for agent "claude"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stub := stubSlotSeams(t)
			got, cmd := pinnedProfile(t, c.pin, false, broken, cloudProfile())
			if got.phase != phaseAgent || got.slot != nil || cmd != nil {
				t.Fatalf("phase = %v slot = %v cmd nil = %v, want the agent picker and no command", got.phase, got.slot, cmd == nil)
			}
			if got.status != c.status {
				t.Errorf("status = %q, want %q", got.status, c.status)
			}
			if got.pinnedModel != "" {
				t.Errorf("pinnedModel = %q, want it cleared so re-entry validates fresh", got.pinnedModel)
			}
			if events := stub.got(); events != "" {
				t.Errorf("events = %q, want none", events)
			}
		})
	}
}
```

- [ ] **Step 17: Run them and see them fail**

```bash
cd wt && go test ./internal/tui -run 'TestPinOnProfileBeginsItsLaunch|TestPinOnUnusableProfile'
```

Expected: `TestPinOnUnusableProfileRoutesBackToTheAgentPicker` passes already (a blocked row and a missing row route back today). `TestPinOnProfileBeginsItsLaunch` fails in its second subtest: a pinned start row goes to `beginStart` with the profile row's id-only model, so the engine is asked to start `profile/mixed`.

```
--- FAIL: TestPinOnProfileBeginsItsLaunch/a_start_row_under_--replace
    slot_flow_test.go: start = {target:{ProviderID: ModelName: ModelID:profile/mixed} …}, want AllowReplace for the pinned profile's local model
FAIL
```

The first subtest passes already, through the `proceedToLaunch` guard of Part B. The pin branch below handles both kinds of row itself and puts the picker in `phaseModel` first.

- [ ] **Step 18: Write the implementation**

In `wt/internal/tui/app.go`, `enterModelPhase`, the pin's `switch`. Add a case between `case it.blocked != "":` and `case it.start:`:

```go
		case it.profile != nil:
			// A slot profile: begin its launch, as the non-TUI pin does. The
			// row is selected first so a failed launch returns to the picker
			// on it. --replace applies when its local model has to start
			// (handleSlotMsg reads it against the pin).
			m.models.Select(idx)
			m.phase = phaseModel
			pinned, ok := m.models.SelectedItem().(*modelItem)
			if !ok {
				return m, nil
			}
			return m.beginSlotLaunch(pinned)
```

Update the function's doc comment. The sentence about a blocked pin is wrapped over two lines there. Find these three lines:

```go
// start row is selected and shown for the user to start, and a blocked row
// routes back with the row's own reason. A mismatch routes to phaseAgent (the
// bad pin is cleared so re-entry validates fresh).
```

Replace them with:

```go
// start row is selected and shown for the user to start, and a blocked row
// routes back with the row's own reason. A pinned slot profile
// (-M profile/<name>) that is not blocked begins its launch through
// beginSlotLaunch; a blocked one routes back like any blocked row. A mismatch
// routes to phaseAgent (the bad pin is cleared so re-entry validates fresh).
```

- [ ] **Step 19: Run the tests and see them pass**

```bash
cd wt && gofmt -l internal/tui && go test ./internal/tui -run 'TestPinOn' && go test ./internal/tui
```

Expected: `gofmt` prints nothing; both runs end in `ok`. The `TestPinOn` pattern also runs the existing model-pin tests, which must still pass.

- [ ] **Step 20: Commit**

```bash
git add wt/internal/tui/app.go wt/internal/tui/slot_flow_test.go
git commit -m "feat(wt): a -M profile/<name> pin the TUI handles begins the profile's launch, and a blocked one returns to the agent picker"
```

---

#### Part E: turn it on

- [ ] **Step 21: Write the failing test**

Append to `wt/cmd/wt/main_test.go`:

```go
// TestRunLaunchPathHandsTheTUIItsSlotProfiles pins the one line that turns
// slot profiles on in the picker: runLaunchPath gives tui.Run the profiles
// newApp loaded and the fail-closed applier. With the zero value the picker
// shows no profile row and says nothing about why.
func TestRunLaunchPathHandsTheTUIItsSlotProfiles(t *testing.T) {
	// runLaunchPath sets the process-wide launchSlotChecks; this restores it.
	stubSlotChecks(t)
	var got tui.SlotProfiles
	oldTuiRun := tuiRun
	tuiRun = func(yolo, allowReplace bool, agent, pinned, tags, family string, extraArgs []string, theme themes.Theme, prePath string, cfg *config.Config, _ tui.ProfileApplier, slots tui.SlotProfiles) error {
		got = slots
		return nil
	}
	t.Cleanup(func() { tuiRun = oldTuiRun })

	a := &app{cfg: &config.Config{}}
	a.slotChecks = []profiles.SlotCheck{slotFixtureCheck(0, "cloud", "opus", map[string]string{"opus": "openrouter/a"})}

	if err := runLaunchPath(&cobra.Command{}, a, "", "", "", "", nil, "", ""); err != nil {
		t.Fatalf("runLaunchPath() error = %v", err)
	}
	if len(got.Checks) != 1 || got.Checks[0].Label() != "profile/cloud" {
		t.Errorf("Checks = %+v, want the app's one slot profile", got.Checks)
	}
	if got.Apply == nil {
		t.Error("Apply is nil: every profile launch from the picker would abort with \"no slot-profile applier\"")
	}
}
```

- [ ] **Step 22: Run it and see it fail**

```bash
cd wt && go test ./cmd/wt -run TestRunLaunchPathHandsTheTUIItsSlotProfiles
```

Expected:

```
--- FAIL: TestRunLaunchPathHandsTheTUIItsSlotProfiles
    main_test.go: Checks = [], want the app's one slot profile
    main_test.go: Apply is nil: every profile launch from the picker would abort with "no slot-profile applier"
FAIL
```

- [ ] **Step 23: Write the implementation**

In `wt/cmd/wt/main.go`, `runLaunchPath`, replace `tui.SlotProfiles{}` in both `tuiRun` calls. Each call becomes:

```go
		return tuiRun(yolo(cmd), allowReplace, agent, pinned, tags, family, args, a.theme, launchPath, a.cfg, applyProfile, tui.SlotProfiles{Checks: a.slotChecks, Apply: applySlotProfile})
```

Add to the comment above the `applyProfile := func(…)` closure:

```go
	// A slot-profile launch never reaches applyProfile. The TUI is handed
	// the loaded slot profiles and applySlotProfile, the fail-closed applier
	// the non-TUI path calls directly, as tui.SlotProfiles.
```

Check both call sites changed:

```bash
cd wt && grep -c 'tui.SlotProfiles{Checks: a.slotChecks, Apply: applySlotProfile}' cmd/wt/main.go
```

Expected: `2`.

- [ ] **Step 24: Run the tests and see them pass**

```bash
cd wt && gofmt -l . && go build ./... && go vet ./... && go test ./cmd/wt ./internal/tui && go test -race ./internal/tui -run 'Slot|Profile'
```

Expected: `gofmt` and `vet` print nothing; `ok` for both packages; the race run ends in `ok` with no `DATA RACE`.

- [ ] **Step 25: Commit**

```bash
git add wt/cmd/wt/main.go wt/cmd/wt/main_test.go
git commit -m "feat(wt): the model picker launches slot profiles"
```

### Task 10: Commands and stats

This task makes slot profiles visible outside a launch. `wt profile list` gains a slot-profile section that prints whatever state `profiles.toml` is in. `wt profile show -A <agent> -M profile/<name>` prints the picker's verdict for one profile, its resolved slots, and the command wt would run, with the proxy key masked. `wt stats` shows a profile's launches as its own row with no spend.

None of the three starts a model, writes a route or restarts the proxy.

**Before you start.** Task 8 must be merged. Check from the repo root:

```bash
grep -n 'slotChecks\|slotLoadErr' wt/cmd/wt/app.go && grep -n 'func slotFixtureConfig\|func slotFixtureCheck\|func stubSlotSeams\|func slotCloud\|func slotMixed' wt/cmd/wt/slot_launch_test.go && (cd wt && go doc ./internal/agents BuildSlotCommand)
```

It must print the two `app` fields, the five test helpers and the declaration of `BuildSlotCommand`.

**Files:**
- Create: `wt/cmd/wt/profile_slots.go`
- Modify: `wt/cmd/wt/profile.go` (the import block; the `list` command's `RunE`; the first lines of the `show` command's `RunE`; the `show` command's `Short` and its `-M` flag text)
- Modify: `wt/cmd/wt/litellm.go` (`runLitellmStatus`: the seven lines that mask the key)
- Modify: `wt/cmd/wt/stats_usage.go` (the import block; `buildUsageRows`; a new function `isSlotProfileID`)
- Test: `wt/cmd/wt/profile_slots_test.go` (new)
- Test: `wt/cmd/wt/profile_test.go` (modify)
- Test: `wt/cmd/wt/stats_usage_rows_test.go` (modify)

**Interfaces:**

Consumes, from existing code in `wt/cmd/wt`:

```go
func profileCmd(a *app) *cobra.Command                                       // profile.go:29
var probeInventory = localmodels.Inventory                                  // resolve.go:17; TestMain stubs it to localmodels.OnDiskSnapshotForTest
func runLitellmStatus(out io.Writer, cfg *config.Config, asJSON bool) error // litellm.go
type usageRow struct {
	Model        string
	Family       string
	Launches     int
	Spend        *spend.Row // nil prints "-" in the four spend cells and null in --json
	AlsoLoggedAs []string
}
func familyFor(id string, families map[string]string) string                // registry family, else the id's prefix before the first "/"
func buildUsageRows(counts map[string]usage.UsageCounts, window time.Duration, sp *spend.Result, families map[string]string, f statsFilter) []usageRow
func usageCells(r usageRow) [6]string                                        // stats_usage_table.go:20
```

Consumes, from existing packages:

```go
// wt/internal/config
func (c *Config) EligibleModels(agentName, tags, family string) ([]Model, error)   // an unknown agent is an error
func (c *Config) LitellmBaseURL() string
func (c *Config) LitellmAPIKey() string

// wt/internal/spend
type Row struct {
	Model            string
	Requests         int64
	PromptTokens     int64
	CompletionTokens int64
	Spend            float64
}
```

Consumes, from earlier tasks:

```go
// Task 2, wt/internal/profiles
const SlotIDPrefix = "profile/"
type SlotCheck struct {
	Index     int
	Profile   SlotProfile // Name, Agent, Default, Slots, Env, Args, ConfigContent, Wrapper
	Usable    bool
	Duplicate bool
	Declared  []string
	Err       error
}
func (c SlotCheck) Label() string   // "profile/<name>" when Usable, else "slot-profiles.toml[<Index>]"
type SlotCommand struct {
	Bin              string
	Args             []string
	Env              []string // "KEY=VALUE"
	ClearEnv         []string
	ClearEnvPrefixes []string
}

// Task 3, wt/internal/agents
func BuildSlotCommand(sl profiles.SlotLaunch, yolo bool, proxyOrigin, proxyKey string) (profiles.SlotCommand, error)
// For claude: Args are `--model <default slot> --settings <json>`; Env is
// ANTHROPIC_BASE_URL=<origin>, ANTHROPIC_AUTH_TOKEN=<key>, ANTHROPIC_API_KEY=;
// ClearEnv is ANTHROPIC_SMALL_FAST_MODEL, CLAUDE_CODE_SUBAGENT_MODEL;
// ClearEnvPrefixes is ANTHROPIC_DEFAULT_.

// Task 4, wt/internal/catalog
type Input struct { /* Config, Agent, Models, Inventory, HideDiscovered */ SlotProfiles []profiles.SlotCheck }
func Build(in Input) []Row
func Find(rows []Row, id string) (Row, bool)
func (r Row) IsProfile() bool
func (r Row) Action() Action        // ActionLaunch, ActionStart or ActionBlock
func (r Row) BlockReason() string
func SlotPrefixReserved(cfg *config.Config) bool   // false for a nil cfg
type ProfileRow struct {
	Check  profiles.SlotCheck
	Models map[string]config.Model // every declared slot on a launch or start row
	// …
}
func (p *ProfileRow) Launch() profiles.SlotLaunch

// Task 8, wt/cmd/wt
type app struct { /* … */ slotChecks []profiles.SlotCheck; slotLoadErr error }
func slotFixtureConfig() *config.Config   // agent "claude"; openrouter/a, openrouter/b (cloud), omlx/q (local, idle under TestMain's probe); [litellm] url http://localhost:4000, key sk-test-1234
func slotFixtureCheck(index int, name, def string, slots map[string]string) profiles.SlotCheck
func slotCloud() map[string]string        // opus → openrouter/a, haiku → openrouter/b
func slotMixed() map[string]string        // opus → openrouter/a, haiku → omlx/q
func stubSlotSeams(t *testing.T) *slotSeams   // records every engine call of a profile launch; (*slotSeams).log() joins them
func stubStartDriver(t *testing.T, err error) *startRequest   // testmain_test.go; .called says a start was asked for
```

Produces:

```go
// wt/cmd/wt/profile_slots.go
func maskKey(key string) string
func writeSlotProfileList(w io.Writer, a *app) error
func showSlotProfile(w io.Writer, a *app, agent, id string) error

// wt/cmd/wt/stats_usage.go
func isSlotProfileID(id string, families map[string]string) bool
```

**Rules that hold for every step of this task:**

- Run every Go command from `wt/`. The commands below start with `cd wt &&` and are written for the repo root.
- Every `Test*` function has a `//` comment above it saying what it tests and what breaks for the user if it regresses.
- The real proxy key is never passed to `agents.BuildSlotCommand`. Only `maskKey`'s result is.

**Where the spec is silent, this task follows the surrounding code:**

- A `wt profile show` error is returned to cobra like every other error of the `wt profile` commands; a blocked profile therefore exits 1.
- `wt profile show` does not print a profile's `args` or `wrapper`. The spec lists the resolved slots, the env and the `--settings` payload, and Claude allows neither mechanism.

---

#### Behaviour 1: `maskKey`, one masking rule for the proxy key

- [ ] **Step 1: Write the failing test**

Create `wt/cmd/wt/profile_slots_test.go`:

```go
package main

import "testing"

// TestMaskKey verifies the one rule wt shows the proxy key by: nothing of a
// key of four characters or fewer, the last four of a longer one. `wt litellm
// status` and `wt profile show` both use it; a second rule in either place
// could print a short key whole.
func TestMaskKey(t *testing.T) {
	for _, tc := range []struct{ key, want string }{
		{"", "(unset)"},
		{"ab", "***"},
		{"abcd", "***"},
		{"abcde", "***bcde"},
		{"sk-abcdef1234", "***1234"},
	} {
		if got := maskKey(tc.key); got != tc.want {
			t.Errorf("maskKey(%q) = %q, want %q", tc.key, got, tc.want)
		}
	}
}
```

- [ ] **Step 2: Run it and see it fail**

```bash
cd wt && go test ./cmd/wt -run TestMaskKey -v
```

Expected: the build fails with `undefined: maskKey`, then `FAIL	github.com/ohanaverse/local-ai-setup/wt/cmd/wt [build failed]`.

- [ ] **Step 3: Write the implementation**

Create `wt/cmd/wt/profile_slots.go`:

```go
// The slot-profile halves of `wt profile list` and `wt profile show`. See
// docs/wt-agents/slot-profiles.md.
package main

// maskKey is how wt shows the LiteLLM proxy key: "(unset)" when empty, "***"
// for a key of four characters or fewer (its last four would be the whole
// secret), "***" plus the last four otherwise.
func maskKey(key string) string {
	switch {
	case key == "":
		return "(unset)"
	case len(key) <= 4:
		return "***"
	}
	return "***" + key[len(key)-4:]
}
```

In `wt/cmd/wt/litellm.go`, inside `runLitellmStatus`, replace these seven lines:

```go
	key := "(unset)"
	if k := cfg.LitellmAPIKey(); k != "" {
		key = "***"
		if len(k) > 4 { // a short key's last-4 would be the whole secret
			key += k[len(k)-4:]
		}
	}
```

with:

```go
	key := maskKey(cfg.LitellmAPIKey())
```

- [ ] **Step 4: Run the tests and see them pass**

```bash
cd wt && go test ./cmd/wt -run 'TestMaskKey|TestLitellmStatus' -v
```

Expected: `--- PASS` for `TestMaskKey` and for the existing `TestLitellmStatus…` tests (`TestLitellmStatusTextMasksKey` proves `wt litellm status` prints what it printed before), then `ok`.

---

#### Behaviour 2: the slot-profile section of `wt profile list`

- [ ] **Step 5: Write the failing test**

In `wt/cmd/wt/profile_slots_test.go`, replace the line `import "testing"` with:

```go
import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)
```

Append to the end of the file:

```go
// runProfileCmd runs `wt profile <args>` on a and returns its stdout and its
// error. cobra's own "Error:" line goes nowhere.
func runProfileCmd(t *testing.T, a *app, args ...string) (string, error) {
	t.Helper()
	cmd := profileCmd(a)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

// TestProfileListSlotSection verifies the slot-profile section of `wt profile
// list`: one line per profile with its agent, its id and "ok" or its static
// error; a profile with no usable name or agent listed by its position in the
// file; and the section printed whatever state profiles.toml is in, missing,
// failing validation or unparseable. It needs no running provider, so the
// inventory probe must not run. If the section depended on profiles.toml, a
// user with a broken automatic-profile file could not see why a slot profile
// is missing from the picker.
func TestProfileListSlotSection(t *testing.T) {
	ok := slotFixtureCheck(0, "cloud", "opus", slotCloud())
	broken := slotFixtureCheck(1, "broken", "x", slotCloud())
	broken.Err = errors.New(`default "x" is not a slot this profile fills`)
	unusable := profiles.SlotCheck{Index: 2, Profile: profiles.SlotProfile{Agent: "claude"}, Err: errors.New("name is missing")}
	checks := []profiles.SlotCheck{ok, broken, unusable}
	section := "slot profiles:\n" +
		"  claude: profile/cloud ok\n" +
		"  claude: profile/broken error: default \"x\" is not a slot this profile fills\n" +
		"  slot-profiles.toml[2]: error: name is missing\n"
	automatic := profiles.Store{Enabled: true, Profiles: []profiles.Profile{{Agent: "pi", Match: "location", Location: "local"}}}
	profilesErr := errors.New("parse profiles.toml: simulated parse failure")
	slotErr := errors.New("parse slot-profiles.toml: toml: line 2: unexpected EOF")

	cases := []struct {
		name    string
		a       *app
		want    string // the whole of stdout
		wantErr string // "" means nil
	}{
		{"profiles.toml missing, no slot file",
			&app{profiles: profiles.Store{Enabled: true}},
			"(no profiles defined)\nslot profiles: (none defined)\n", ""},
		{"profiles.toml missing, slot profiles present",
			&app{profiles: profiles.Store{Enabled: true}, slotChecks: checks},
			"(no profiles defined)\n" + section, ""},
		{"both files have profiles",
			&app{profiles: automatic, slotChecks: checks},
			"pi: match=location location=local\n" + section, ""},
		{"profiles.toml does not parse",
			&app{profilesLoadErr: profilesErr, slotChecks: checks},
			section, "profiles.toml: parse profiles.toml: simulated parse failure"},
		{"profiles.toml fails validation",
			&app{profiles: automatic, profilesValidateErr: errors.New("agent does not support mechanism \"wrapper\""), slotChecks: checks},
			"pi: match=location location=local\n" +
				"warning: profiles.toml failed validation — profiles are disabled for every launch: agent does not support mechanism \"wrapper\"\n" +
				section, ""},
		{"slot-profiles.toml does not parse",
			&app{profiles: profiles.Store{Enabled: true}, slotLoadErr: slotErr},
			"(no profiles defined)\nslot profiles: parse slot-profiles.toml: toml: line 2: unexpected EOF\n",
			"parse slot-profiles.toml: toml: line 2: unexpected EOF"},
		{"neither file parses",
			&app{profilesLoadErr: profilesErr, slotLoadErr: slotErr},
			"slot profiles: parse slot-profiles.toml: toml: line 2: unexpected EOF\n",
			"profiles.toml: parse profiles.toml: simulated parse failure"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			old := probeInventory
			probeInventory = func(*config.Config) localmodels.Snapshot {
				t.Error("`wt profile list` probed the providers")
				return localmodels.Snapshot{}
			}
			t.Cleanup(func() { probeInventory = old })

			out, err := runProfileCmd(t, tc.a, "list")

			if out != tc.want {
				t.Errorf("stdout =\n%s\nwant\n%s", out, tc.want)
			}
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("err = %v, want nil", err)
			case tc.wantErr != "" && (err == nil || err.Error() != tc.wantErr):
				t.Errorf("err = %v, want %q", err, tc.wantErr)
			}
		})
	}
}
```

- [ ] **Step 6: Run it and see it fail**

```bash
cd wt && go test ./cmd/wt -run TestProfileListSlotSection -v
```

Expected: the package builds and `TestProfileListSlotSection` fails. Each subtest's stdout lacks every `slot profiles…` line, for example `stdout =` `(no profiles defined)` with `want` ending `slot profiles: (none defined)`.

- [ ] **Step 7: Write the implementation**

In `wt/cmd/wt/profile_slots.go`, add an import block after the `package main` line:

```go
import (
	"fmt"
	"io"
)
```

Append to the end of the file:

```go
// writeSlotProfileList prints the slot-profile section of `wt profile list`:
// one line per profile of slot-profiles.toml, in file order, with its agent,
// its id and "ok" or its static error. A profile with no usable name or agent
// is listed by its position in the file. The verdict is the static one only
// (profiles.ValidateSlots): no provider is probed, so a profile that is "ok"
// here may still be blocked in the picker; `wt profile show` says.
//
// It reads nothing of profiles.toml. It returns a.slotLoadErr after printing
// it.
func writeSlotProfileList(w io.Writer, a *app) error {
	if a.slotLoadErr != nil {
		fmt.Fprintf(w, "slot profiles: %v\n", a.slotLoadErr)
		return a.slotLoadErr
	}
	if len(a.slotChecks) == 0 {
		fmt.Fprintln(w, "slot profiles: (none defined)")
		return nil
	}
	fmt.Fprintln(w, "slot profiles:")
	for _, c := range a.slotChecks {
		switch {
		case !c.Usable:
			fmt.Fprintf(w, "  %s: error: %v\n", c.Label(), c.Err)
		case c.Err != nil:
			fmt.Fprintf(w, "  %s: %s error: %v\n", c.Profile.Agent, c.Label(), c.Err)
		default:
			fmt.Fprintf(w, "  %s: %s ok\n", c.Profile.Agent, c.Label())
		}
	}
	return nil
}
```

In `wt/cmd/wt/profile.go`, replace the whole `RunE` of the `list` command (the function literal inside `listC := &cobra.Command{…}`) with:

```go
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			// A load error means profiles.toml is malformed/unreadable —
			// a.profiles is NOT real data (it's the zero-value Store{}), so
			// rendering it as "(no profiles defined)" would misreport a
			// broken file as an empty one. Report the real error instead.
			// The slot-profile section is produced independently of it: it
			// is printed first, and this error is the one returned.
			if a.profilesLoadErr != nil {
				_ = writeSlotProfileList(out, a)
				return fmt.Errorf("profiles.toml: %w", a.profilesLoadErr)
			}
			if len(a.profiles.Profiles) == 0 {
				fmt.Fprintln(out, "(no profiles defined)")
			} else {
				for _, p := range a.profiles.Profiles {
					fmt.Fprintf(out, "%s: match=%s %s\n", p.Agent, p.Match, matchValueForDisplay(p))
				}
				if a.profilesValidateErr != nil {
					fmt.Fprintf(out, "warning: profiles.toml failed validation — profiles are disabled for every launch: %v\n", a.profilesValidateErr)
				}
			}
			return writeSlotProfileList(out, a)
		},
```

- [ ] **Step 8: Run the tests and see them pass**

```bash
cd wt && go test ./cmd/wt -run 'TestProfileList' -v
```

Expected: `--- PASS` for `TestProfileListSlotSection` (seven subtests) and for the existing `TestProfileListPrintsEveryProfile`, `TestProfileListFlagsInvalidMatchTier`, `TestProfileListWarnsOnValidateError` and `TestProfileListShowStatusReportLoadError`, then `ok`.

---

#### Behaviour 3: `wt profile show -A <agent> -M profile/<name>`

- [ ] **Step 9: Write the failing tests**

In `wt/cmd/wt/profile_slots_test.go`, add `"strings"` to the first group of the import block, after `"io"`. Then append to the end of the file:

```go
// slotShowApp is an app holding the fixture config and three slot profiles:
// an all-cloud one, one whose local model is idle, and one naming a model
// that is not in the registry.
func slotShowApp() *app {
	return &app{
		cfg:      slotFixtureConfig(),
		profiles: profiles.Store{Enabled: true},
		slotChecks: []profiles.SlotCheck{
			slotFixtureCheck(0, "cloud", "opus", slotCloud()),
			slotFixtureCheck(1, "mixed", "opus", slotMixed()),
			slotFixtureCheck(2, "broken", "opus", map[string]string{"opus": "openrouter/gone"}),
		},
	}
}

// TestProfileShowSlotProfile verifies `wt profile show -A claude -M
// profile/<name>` for a launch row, a start row and a blocked row. It prints
// the picker's verdict, each slot's registry id and provider-side name (the
// default marked, an unfilled slot shown with the default's model), and the
// command wt would run. The proxy key is masked everywhere. A blocked profile
// prints its reason and returns an error, so the command exits non-zero. It
// starts no model and touches no route: this is the command a user runs to
// find out why a profile will not launch, and it must be safe to run.
func TestProfileShowSlotProfile(t *testing.T) {
	cases := []struct {
		name    string
		id      string
		want    []string // lines stdout must contain
		wantErr string   // "" means nil
	}{
		{"launch row", "profile/cloud", []string{
			"profile/cloud (claude): launch\n",
			"slot fable: openrouter/a (a) [default's model]\n",
			"slot opus*: openrouter/a (a)\n",
			"slot sonnet: openrouter/a (a) [default's model]\n",
			"slot haiku: openrouter/b (b)\n",
			"env: ANTHROPIC_BASE_URL=http://localhost:4000\n",
			"env: ANTHROPIC_AUTH_TOKEN=***1234\n",
			"env: ANTHROPIC_API_KEY=\n",
			"unset: ANTHROPIC_SMALL_FAST_MODEL\n",
			"unset: CLAUDE_CODE_SUBAGENT_MODEL\n",
			"unset: ANTHROPIC_DEFAULT_*\n",
			"args: --model opus --settings <settings>\n",
			"settings:\n  {\n",
			`"ANTHROPIC_DEFAULT_HAIKU_MODEL": "openrouter/b"`,
			`"ANTHROPIC_DEFAULT_SONNET_MODEL": "openrouter/a"`,
		}, ""},
		{"start row", "profile/mixed", []string{
			"profile/mixed (claude): start\n",
			"slot opus*: openrouter/a (a)\n",
			"slot haiku: omlx/q (q)\n",
			"env: ANTHROPIC_AUTH_TOKEN=***1234\n",
			`"ANTHROPIC_DEFAULT_HAIKU_MODEL": "omlx/q"`,
		}, ""},
		{"blocked row", "profile/broken", []string{
			"profile/broken (claude): blocked\n",
			"reason: slot opus: openrouter/gone is not in the registry\n",
		}, "profile/broken is blocked: slot opus: openrouter/gone is not in the registry"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seams := stubSlotSeams(t)
			started := stubStartDriver(t, nil)

			out, err := runProfileCmd(t, slotShowApp(), "show", "-A", "claude", "-M", tc.id)

			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Errorf("stdout lacks %q; stdout =\n%s", want, out)
				}
			}
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("err = %v, want nil", err)
			case tc.wantErr != "" && (err == nil || err.Error() != tc.wantErr):
				t.Errorf("err = %v, want %q", err, tc.wantErr)
			}
			if strings.Contains(out, "sk-test-1234") {
				t.Errorf("the proxy key is printed:\n%s", out)
			}
			if tc.wantErr != "" && strings.Contains(out, "args:") {
				t.Errorf("a blocked profile printed a command:\n%s", out)
			}
			if seams.log() != "" || started.called {
				t.Errorf("`wt profile show` drove the engine: steps %q, start asked for: %v", seams.log(), started.called)
			}
		})
	}
}

// TestProfileShowSlotProfileErrors verifies the ways the slot branch of `wt
// profile show` says no: a name that is not defined, a profile of another
// agent, and a slot-profiles.toml that did not load. Each is an error, never
// an empty report that reads as "nothing would apply".
func TestProfileShowSlotProfileErrors(t *testing.T) {
	withPi := slotShowApp()
	withPi.cfg.Agents = append(withPi.cfg.Agents, config.Agent{Name: "pi", SupportedProviders: []string{"openrouter", "omlx"}})
	broken := slotShowApp()
	broken.slotChecks = nil
	broken.slotLoadErr = errors.New("parse slot-profiles.toml: toml: line 2: unexpected EOF")
	cases := []struct {
		name    string
		a       *app
		agent   string
		id      string
		wantErr string
	}{
		{"unknown name", slotShowApp(), "claude", "profile/nope", `slot profile "profile/nope" is not defined for agent "claude"`},
		{"another agent's profile", withPi, "pi", "profile/cloud", `slot profile "profile/cloud" is not defined for agent "pi"`},
		{"the file did not load", broken, "claude", "profile/cloud", "parse slot-profiles.toml: toml: line 2: unexpected EOF"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runProfileCmd(t, tc.a, "show", "-A", tc.agent, "-M", tc.id)
			if err == nil || err.Error() != tc.wantErr {
				t.Errorf("err = %v, want %q", err, tc.wantErr)
			}
			if out != "" {
				t.Errorf("stdout = %q, want nothing", out)
			}
		})
	}
}

// TestProfileShowSlotProfileIgnoresProfilesToml verifies the slot branch does
// not depend on profiles.toml: with that file unparseable, or failing
// validation, a slot profile is still shown. The two files are independent,
// and a broken automatic-profile file must not hide a working slot profile.
func TestProfileShowSlotProfileIgnoresProfilesToml(t *testing.T) {
	for name, damage := range map[string]func(*app){
		"profiles.toml does not parse": func(a *app) {
			a.profilesLoadErr = errors.New("parse profiles.toml: simulated parse failure")
		},
		"profiles.toml fails validation": func(a *app) {
			a.profilesValidateErr = errors.New("agent does not support mechanism \"wrapper\"")
		},
	} {
		t.Run(name, func(t *testing.T) {
			stubSlotSeams(t)
			a := slotShowApp()
			damage(a)
			out, err := runProfileCmd(t, a, "show", "-A", "claude", "-M", "profile/cloud")
			if err != nil || !strings.HasPrefix(out, "profile/cloud (claude): launch\n") {
				t.Errorf("show = (%q, %v), want the profile shown", out, err)
			}
		})
	}
}
```

In `wt/cmd/wt/profile_test.go`, append to the end of the file (it needs no new import):

```go
// TestProfileShowReservedPrefixIsARegistryModel verifies the one case where a
// -M value starting with "profile/" is not a slot profile: the registry has a
// provider with id "profile". No slot-profile row exists then, so the id is a
// registry model's and `wt profile show` resolves automatic profiles for it,
// as it does for any model id. Taking the slot branch would report an
// existing model as "not defined".
func TestProfileShowReservedPrefixIsARegistryModel(t *testing.T) {
	a := &app{
		cfg: &config.Config{
			Providers: []config.Provider{{ID: "profile", Location: config.LocationCloud}},
			Models:    []config.Model{{ID: "profile/x", ProviderID: "profile", ModelName: "x"}},
		},
		profiles: profiles.Store{Enabled: true, Profiles: []profiles.Profile{
			{Agent: "claude", Match: "model", Model: "profile/x", Env: map[string]string{"X": "1"}},
		}},
	}
	var out bytes.Buffer
	cmd := profileCmd(a)
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"show", "-A", "claude", "-M", "profile/x"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute error = %v", err)
	}
	if !strings.Contains(out.String(), "env: X=1") {
		t.Errorf("output = %q, want the automatic profile resolved for the registry model", out.String())
	}
}
```

- [ ] **Step 10: Run them and see them fail**

```bash
cd wt && go test ./cmd/wt -run 'TestProfileShowSlotProfile|TestProfileShowReservedPrefixIsARegistryModel' -v
```

Expected: the package builds. `TestProfileShowSlotProfile`, `TestProfileShowSlotProfileErrors` and `TestProfileShowSlotProfileIgnoresProfilesToml` fail: every subtest gets `model "profile/…" not found in registry` (or the `profiles.toml:` load error) from the automatic branch. `TestProfileShowReservedPrefixIsARegistryModel` passes already; it pins behaviour the next step must keep.

- [ ] **Step 11: Write the implementation**

In `wt/cmd/wt/profile_slots.go`, replace the import block with:

```go
import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
)
```

Append to the end of the file:

```go
// showSlotProfile is `wt profile show -A <agent> -M profile/<name>`. It builds
// the agent's catalog rows from one inventory probe, exactly as the picker
// does, and looks the profile's row up with catalog.Find, so the verdict it
// prints is the picker's. For a launch or a start row it prints each declared
// slot with its registry id and provider-side name, then the command the
// agent's slot build returns: the env it sets, the inherited variables it
// removes, the arguments, and the --settings payload.
//
// The slot build is given the masked key, never the real one, so the proxy
// key cannot appear in the output. A blocked profile prints its reason and
// returns an error. Nothing is started, no route is written and the proxy is
// not restarted. It reads nothing of profiles.toml.
func showSlotProfile(w io.Writer, a *app, agent, id string) error {
	if a.slotLoadErr != nil {
		return a.slotLoadErr
	}
	if a.cfg == nil {
		return fmt.Errorf("no config loaded")
	}
	eligible, err := a.cfg.EligibleModels(agent, "", "")
	if err != nil {
		return err
	}
	snap := probeInventory(a.cfg)
	rows := catalog.Build(catalog.Input{
		Config: a.cfg, Agent: agent, Models: eligible, Inventory: &snap,
		SlotProfiles: a.slotChecks,
	})
	row, ok := catalog.Find(rows, id)
	if !ok || !row.IsProfile() {
		return fmt.Errorf("slot profile %q is not defined for agent %q", id, agent)
	}

	verdict := "launch"
	switch row.Action() {
	case catalog.ActionStart:
		verdict = "start"
	case catalog.ActionBlock:
		verdict = "blocked"
	}
	fmt.Fprintf(w, "%s (%s): %s\n", id, agent, verdict)
	if row.Action() == catalog.ActionBlock {
		fmt.Fprintf(w, "reason: %s\n", row.BlockReason())
		return fmt.Errorf("%s is blocked: %s", id, row.BlockReason())
	}

	p := row.Profile
	sp := p.Check.Profile
	for _, slot := range p.Check.Declared {
		m := p.Models[slot]
		name := slot
		if slot == sp.Default {
			name += "*"
		}
		line := fmt.Sprintf("slot %s: %s (%s)", name, m.ID, m.ModelName)
		if _, filled := sp.Slots[slot]; !filled {
			line += " [default's model]"
		}
		fmt.Fprintln(w, line)
	}

	sc, err := agents.BuildSlotCommand(p.Launch(), false, a.cfg.LitellmBaseURL(), maskKey(a.cfg.LitellmAPIKey()))
	if err != nil {
		return err
	}
	for _, kv := range sc.Env {
		fmt.Fprintf(w, "env: %s\n", kv)
	}
	for _, name := range sc.ClearEnv {
		fmt.Fprintf(w, "unset: %s\n", name)
	}
	for _, prefix := range sc.ClearEnvPrefixes {
		fmt.Fprintf(w, "unset: %s*\n", prefix)
	}
	// The --settings value is one long JSON argument: it is shown as
	// <settings> on the args line and in full, indented, below it.
	args := slices.Clone(sc.Args)
	settings := ""
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--settings" {
			settings = args[i+1]
			args[i+1] = "<settings>"
		}
	}
	fmt.Fprintf(w, "args: %s\n", strings.Join(args, " "))
	if settings != "" {
		fmt.Fprintln(w, "settings:")
		var buf bytes.Buffer
		if json.Indent(&buf, []byte(settings), "  ", "  ") == nil {
			settings = buf.String()
		}
		fmt.Fprintf(w, "  %s\n", settings)
	}
	return nil
}
```

In `wt/cmd/wt/profile.go`, add one import to the import block, before the `config` line:

```go
	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
```

In the `show` command's `RunE`, find:

```go
			if showAgent == "" {
				return fmt.Errorf("-A/--agent is required")
			}
```

Add directly after it, before the `if a.profilesLoadErr != nil {` check:

```go
			// A -M value with the profile/ prefix names a slot profile. That
			// branch reads slot-profiles.toml only, so it runs before any
			// profiles.toml check. When the registry has a provider with id
			// "profile" no slot-profile row exists and the id is a registry
			// model's: the automatic branch below handles it.
			if strings.HasPrefix(showModel, profiles.SlotIDPrefix) && !catalog.SlotPrefixReserved(a.cfg) {
				return showSlotProfile(cmd.OutOrStdout(), a, showAgent, showModel)
			}
```

Replace the `show` command's `Use:` line and its `-M` flag line with:

```go
		Use: "show", Short: "Dry-run profile resolution for an agent/model, or show a slot profile (-M profile/<name>), without launching", Args: cobra.NoArgs, SilenceUsage: true,
```

```go
	showC.Flags().StringVarP(&showModel, "model", "M", "", "model id to resolve for, or profile/<name> for a slot profile")
```

- [ ] **Step 12: Run the tests and see them pass**

```bash
cd wt && go test ./cmd/wt -run 'TestProfile' -v
```

Expected: `--- PASS` for `TestProfileShowSlotProfile` (three subtests), `TestProfileShowSlotProfileErrors` (three subtests), `TestProfileShowSlotProfileIgnoresProfilesToml` (two subtests), `TestProfileShowReservedPrefixIsARegistryModel`, `TestProfileListSlotSection` and every existing `TestProfile…` test, then `ok`.

**If check 1 failed** (spec, "Check results"): Task 3 then puts the slot variables in `SlotCommand.Env`, and omits `--settings` when the profile has no `config_content`. `showSlotProfile` needs no change. In `TestProfileShowSlotProfile` change the expectations of the "launch row" case: replace `"args: --model opus --settings <settings>\n"` with `"args: --model opus\n"`, delete `"settings:\n  {\n"`, and replace the two JSON lines with `"env: ANTHROPIC_DEFAULT_HAIKU_MODEL=openrouter/b\n"` and `"env: ANTHROPIC_DEFAULT_SONNET_MODEL=openrouter/a\n"`. In the "start row" case replace the JSON line with `"env: ANTHROPIC_DEFAULT_HAIKU_MODEL=omlx/q\n"`.

- [ ] **Step 13: Commit**

```bash
git add wt/cmd/wt/profile_slots.go wt/cmd/wt/profile_slots_test.go wt/cmd/wt/profile.go wt/cmd/wt/profile_test.go wt/cmd/wt/litellm.go
git commit -m "feat(wt): wt profile list and show report slot profiles, with the proxy key masked"
```

---

#### Behaviour 4: `wt stats` shows a profile's launches as its own row

- [ ] **Step 14: Write the failing tests**

Append to the end of `wt/cmd/wt/stats_usage_rows_test.go` (it needs no new import):

```go
// TestBuildUsageRowsProfileRowHasNoSpend verifies a slot profile's launches
// appear as their own row: counted like any id's, in the family "profile",
// and with no spend at all, while a model row beside it keeps its numbers.
// The proxy logs each request under the slot model's registry id, so a zero
// in the profile's spend cells would read as "this profile cost nothing"; a
// nil Spend prints "-" and is null in --json.
func TestBuildUsageRowsProfileRowHasNoSpend(t *testing.T) {
	sp := &spend.Result{Rows: []spend.Row{
		{Model: "ollama/glm:cloud", Requests: 40, PromptTokens: 1000, CompletionTokens: 200, Spend: 0.01},
	}}
	families := map[string]string{"ollama/glm:cloud": "glm", "ollama/idle": "glm"}
	rows := buildUsageRows(map[string]usage.UsageCounts{
		"profile/mixed":    counts(1, 2, 3),
		"ollama/glm:cloud": counts(1, 1, 1),
		"ollama/idle":      counts(0, 4, 4),
	}, survey.Window7d, sp, families, statsFilter{})

	if len(rows) != 3 {
		t.Fatalf("rows = %+v, want 3", rows)
	}
	// Sorted by id: ollama/glm:cloud, ollama/idle, profile/mixed.
	if r := rows[0]; r.Spend == nil || r.Spend.Requests != 40 {
		t.Errorf("model row = %+v, want its spend kept", r)
	}
	if r := rows[1]; r.Spend == nil || r.Spend.Requests != 0 {
		t.Errorf("launch-only model row = %+v, want a zero spend row, never nil", r)
	}
	p := rows[2]
	if p.Model != "profile/mixed" || p.Family != "profile" || p.Launches != 2 {
		t.Errorf("profile row = %+v, want profile/mixed in family profile with 2 launches", p)
	}
	if p.Spend != nil {
		t.Errorf("profile row's Spend = %+v, want nil", p.Spend)
	}
	if got, want := usageCells(p), [6]string{"profile/mixed", "2", "-", "-", "-", "-"}; got != want {
		t.Errorf("profile row's cells = %v, want %v", got, want)
	}

	// --family profile names the profile rows and nothing else.
	only := buildUsageRows(map[string]usage.UsageCounts{
		"profile/mixed":    counts(1, 2, 3),
		"ollama/glm:cloud": counts(1, 1, 1),
	}, survey.Window7d, sp, families, statsFilter{family: "profile"})
	if len(only) != 1 || only[0].Model != "profile/mixed" {
		t.Errorf("--family profile rows = %+v, want the one profile row", only)
	}
}

// TestBuildUsageRowsProfileIDEdges verifies the two edges of the profile rule.
// A registry model whose id happens to start with "profile/" (a provider with
// that id) is a model: it keeps its zero spend row. Spend the proxy did log
// under a profile's id is kept, not thrown away: wt does not write it there,
// but hiding real money would be worse than an odd row.
func TestBuildUsageRowsProfileIDEdges(t *testing.T) {
	sp := &spend.Result{Rows: []spend.Row{{Model: "profile/logged", Requests: 3, Spend: 0.5}}}
	families := map[string]string{"profile/registered": "odd"}
	rows := buildUsageRows(map[string]usage.UsageCounts{
		"profile/registered": counts(1, 1, 1),
		"profile/logged":     counts(1, 1, 1),
	}, survey.Window7d, sp, families, statsFilter{})

	if len(rows) != 2 {
		t.Fatalf("rows = %+v, want 2", rows)
	}
	if r := rows[0]; r.Model != "profile/logged" || r.Spend == nil || r.Spend.Requests != 3 || r.Spend.Spend != 0.5 {
		t.Errorf("row = %+v, want the logged spend kept", r)
	}
	if r := rows[1]; r.Model != "profile/registered" || r.Spend == nil || r.Family != "odd" {
		t.Errorf("row = %+v, want a registry model's zero spend row and its registry family", r)
	}
	for id, want := range map[string]bool{"profile/mixed": true, "profile/registered": false, "ollama/glm:cloud": false, "profile": false} {
		if got := isSlotProfileID(id, families); got != want {
			t.Errorf("isSlotProfileID(%q) = %v, want %v", id, got, want)
		}
	}
}

// TestBuildUsageRowsProfileRowWithoutSpendData verifies a profile row is
// unchanged when the report has no spend data at all: launches counted, Spend
// nil, as for every row then.
func TestBuildUsageRowsProfileRowWithoutSpendData(t *testing.T) {
	rows := buildUsageRows(map[string]usage.UsageCounts{"profile/mixed": counts(1, 2, 3)}, survey.Window30d, nil, nil, statsFilter{})
	if len(rows) != 1 || rows[0].Launches != 3 || rows[0].Spend != nil || rows[0].Family != "profile" {
		t.Fatalf("rows = %+v, want one profile row with 3 launches and nil Spend", rows)
	}
}
```

- [ ] **Step 15: Run them and see them fail**

```bash
cd wt && go test ./cmd/wt -run 'TestBuildUsageRowsProfile' -v
```

Expected: the build fails with `undefined: isSlotProfileID`, then `FAIL	github.com/ohanaverse/local-ai-setup/wt/cmd/wt [build failed]`.

- [ ] **Step 16: Write the implementation**

In `wt/cmd/wt/stats_usage.go`, add one import to the import block, after the `litellm` line:

```go
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
```

Add this function directly above `buildUsageRows`' doc comment:

```go
// isSlotProfileID reports whether id is a slot profile's launch id: it has
// the profile/ prefix and is not a registry model id. A registry can hold a
// provider with id "profile", whose models' ids start the same way; wt builds
// no slot-profile row then, and such an id is a model's.
func isSlotProfileID(id string, families map[string]string) bool {
	if !strings.HasPrefix(id, profiles.SlotIDPrefix) {
		return false
	}
	_, registered := families[id]
	return !registered
}
```

Inside `buildUsageRows`, find the end of the `if sp != nil { … }` block:

```go
		for _, r := range byModel {
			sort.Strings(r.AlsoLoggedAs)
		}
	}
```

Add directly after that closing brace, before `rows := make([]usageRow, 0, len(byModel))`:

```go
	// A slot profile's row counts launches only. The proxy logs each request
	// of a profile session under the slot model's registry id, so the zero
	// spend the join gave this row would read as "this profile cost nothing".
	// Spend the proxy did log under the id is kept.
	for _, r := range byModel {
		if r.Spend != nil && isSlotProfileID(r.Model, families) && *r.Spend == (spend.Row{Model: r.Model}) {
			r.Spend = nil
		}
	}
```

Extend `buildUsageRows`' doc comment. After the sentence that ends `Rows are sorted by model id.`, add:

```go
//
// A slot profile's launches are recorded under its own id, "profile/<name>"
// (isSlotProfileID). Its row has its launch count and a nil Spend even when
// there is spend data: its spend lands on the slot models' rows, and this
// report does not attribute it back to the profile.
```

- [ ] **Step 17: Run the tests and see them pass**

```bash
cd wt && go test ./cmd/wt -run 'TestBuildUsageRows|TestStats' -v
```

Expected: `--- PASS` for `TestBuildUsageRowsProfileRowHasNoSpend`, `TestBuildUsageRowsProfileIDEdges`, `TestBuildUsageRowsProfileRowWithoutSpendData` and every existing `TestBuildUsageRows…` and `TestStats…` test, then `ok`.

- [ ] **Step 18: Run the whole module's checks**

```bash
cd wt && gofmt -l . && go build ./... && go vet ./... && go test ./...
```

Expected: `gofmt -l .` prints nothing; `go build` and `go vet` print nothing; `go test` prints `ok` for every package and no `FAIL`.

- [ ] **Step 19: Commit**

```bash
git add wt/cmd/wt/stats_usage.go wt/cmd/wt/stats_usage_rows_test.go
git commit -m "feat(wt): wt stats shows a slot profile's launches as its own row, with no spend"
```

### Task 11: Documentation

This task changes no Go code. It writes the operator page for slot profiles, replaces the statements the feature made untrue, and records the two new files and their owner. Every step gives the text to write. Paths in **Files** are from the repo root.

Rules that bind every step:

- `docs/guides/` never holds live model state. A guide shows the command that reveals the state, and its examples use placeholders or are labelled illustrative. `wt/docs/` may show example ids; label them illustrative there too.
- `bin/check-links` checks every repo-relative link in tracked `*.md` files outside `docs/superpowers/`. A link to a file that does not exist fails `make lint`.
- Name identifiers exactly as the code spells them. Before writing an identifier you have not seen, confirm it: `cd wt && grep -rn 'func launchSlotProfile' cmd/wt` (and likewise for the others).
- Write plainly: short sentences, one fact per sentence.

**Files:**
- Create: `wt/docs/wt-agents/slot-profiles.md`
- Modify: `wt/docs/wt-agents/profiles.md` (the opening section; the `ANTHROPIC_DEFAULT_*_MODEL` bullet under "Notes and caveats")
- Modify: `wt/docs/wt-agents/claude-wt.md` (new subsection after "LiteLLM routing")
- Modify: `wt/docs/wt-agents/README.md` ("Agents"; the `-M` row of "Common launcher flags"; "Post-exit order")
- Modify: `wt/docs/internals/launch-flow.md` (new section before "Sessions (Go)"; one bullet in "Post-exit flow")
- Modify: `wt/docs/internals/local-models.md` ("Local-model resolution", "Lifecycle", "Rotation (Go)")
- Modify: `wt/docs/internals/tui.md` (two notes after the route-check paragraph)
- Modify: `wt/docs/internals/agents.md` (after the capabilities table)
- Modify: `wt/docs/internals/packages.md` (two new rows; six cells extended)
- Modify: `wt/docs/internals/testing.md` (one paragraph)
- Modify: `wt/docs/wt-config.md` (the state-directory listing)
- Modify: `wt/docs/wt-stats.md` (one paragraph in "Usage table")
- Modify: `docs/guides/00-config-map.md` (TL;DR table; the `rotation.state` section; two new sections)
- Modify: `docs/guides/03-model-families.md` (two Gotchas bullets)
- Modify: `docs/guides/06-wt-agents-and-models.md` (§5 Rotation; new §10; one Gotchas bullet)
- Modify: `docs/guides/07-usage-and-spend.md` (two bullets; one new Gotchas bullet)
- Modify: `wt/CLAUDE.md` (Key flags, Post-exit flow, Docs, the package table, Local-model resolution, Lifecycle, Rotation, Profiles, TUI, Verification commands)
- Modify: `CLAUDE.md` (the `wt/` bullet under "Architecture")
- Modify: `wt/CHANGELOG.md` ("Unreleased" → "Added")
- Test: none. The checks are `make lint` and `make test-all`.

**Interfaces:**
- Consumes: the identifiers of Tasks 2 to 10, named in the text below exactly as declared: `profiles.LoadSlots`, `profiles.ValidateSlots`, `profiles.SlotCapable`, `profiles.SlotCheck`, `agents.SlotsFor`, `agents.CheckSlotPassthrough`, `agents.BuildSlotCommand`, `agents.BuildSlotLaunchCmd`, `catalog.Input.SlotProfiles`, `catalog.Row.Profile`, `catalog.ProfileRow`, `catalog.ResolveSlotProfile`, `catalog.SlotPrefixReserved`, `lifecycle.EnsureModelRoutes`, `lifecycle.Options.RestartOwed`, `lifecycle.RestartStillOwed`, `lifecycle.RestartProxy`, `lifecycle.ProbeProxy`, `(*rotation.Rotation).LastPicked`, `(*rotation.Rotation).RecordProfile`, `(*refcount.StoreImpl).RecordAll`, `launchSlotProfile`, `applySlotProfile`, `runSlotAgentCmd`, `startForLaunchOwed`, `launchSlotChecks`, `slotPin`, `slotWarnings`, `tui.SlotProfiles`, `tui.SlotProfileApplier`, `phaseSlot`, `slotLine`, `beginSlotLaunch`, `runSlotAndWaitCmd`, `writeSlotProfileList`, `showSlotProfile`, `maskKey`, `isSlotProfileID`. The `## Check results` section Task 1 appended to `docs/superpowers/specs/2026-10-13-wt-slot-profiles-design.md`.
- Produces: no declarations. Task 12 appends its acceptance results to the spec, not to these pages.

---

- [ ] **Step 1: Read the check results and pick the wording they decide**

Open `docs/superpowers/specs/2026-10-13-wt-slot-profiles-design.md` and read the `## Check results` table. Four sentences on the new page depend on it. The page text in Step 2 is written for the primary result of each check. Where your result differs, use the replacement in this table when you write Step 2.

| Check | Primary result (text as written in Step 2) | If the result differs, write this instead |
|---|---|---|
| 1 | `env` in `--settings` is honoured: the slot variables travel in the `--settings` payload | In "What Claude Code is given", move the `ANTHROPIC_DEFAULT_<SLOT>_MODEL` and `ANTHROPIC_DEFAULT_<SLOT>_MODEL_NAME` rows and the profile's `env` from the `--settings` list to the process-environment list, and replace the sentence under the JSON example with: `The slot variables and the profile's env are set in the process environment. --settings carries only config_content, and is omitted when the profile has none.` Replace the JSON example with the payload `wt profile show` prints on your machine |
| 4 | Two `--settings` flags do not merge: a passthrough `--settings` is refused | Replace the "Passthrough arguments" paragraph with: `wt refuses --model after --, in either form (--model opus or --model=opus): it would replace the session's slot. A --settings you pass after -- is merged with wt's by Claude Code; wt's slot variables are in its own payload.` In the Failures table, change the row to name `--model` only |
| 5 | An inherited `ANTHROPIC_SMALL_FAST_MODEL` does not override the haiku slot | Replace the sentence that starts "Checked on" under "Removed from the inherited environment" with: `Checked on <date> with Claude Code <version>: an inherited ANTHROPIC_SMALL_FAST_MODEL overrides ANTHROPIC_DEFAULT_HAIKU_MODEL, which is why wt removes it. An env block in your own Claude Code settings file that sets it still redirects the haiku slot.` |
| 6 | `/model` saves the chosen alias as `model` in `~/.claude/settings.json` | Replace the "Claude Code may write its own settings" paragraph with exactly what the check recorded: the key, the value, and what a later bare `claude` run starts on |
| – | `modelOverrides` has no observed effect behind the proxy | Replace the last sentence of the `config_content` row's note with: `Checked on <date>: modelOverrides is honoured behind the proxy; an id it names is sent as the id it maps to.` |

Fill `<date>` and `<version>` from the first line under `## Check results`.

- [ ] **Step 2: Write the slot-profiles page**

Create `wt/docs/wt-agents/slot-profiles.md` with this content:

`````markdown
# wt slot profiles: one launch, several models

A wt launch of Claude Code names one model, and Claude Code uses several:
a main model, a small fast model for background work, and whatever `/model`
or a subagent switches to. wt redirects only the one it names. The others
keep their Anthropic ids, and the LiteLLM proxy rejects them with
`Invalid model name passed in model=claude-…`.

A **slot profile** fixes that. It is a named, hand-written entry that
assigns a registry model to each of an agent's model slots. It appears as a
row in the model picker, and you choose it like a model. Claude Code is the
only agent with slots today; its slots are `fable`, `opus`, `sonnet` and
`haiku`.

Slot profiles are not the automatic profiles of [profiles.md](profiles.md).
An automatic profile has no name, is never chosen, and is applied to a
single-model launch that matches it. A slot profile is its own launch: wt
never applies an automatic profile to it, and `wt profile on|off` does not
hide it.

Design: `docs/superpowers/specs/2026-10-13-wt-slot-profiles-design.md`
(monorepo root). Anthropic does not support routing Claude Code to
non-Claude models through a gateway. Slot profiles, like every non-native wt
launch of Claude Code, are outside that support.

## Quick start

1. Configure the proxy once, if you have not: `wt litellm set --url … --api-key …`.
   A slot profile always uses the LiteLLM proxy, whatever `wt litellm status`
   says about the on/off toggle.
2. Write `~/.config/agent-wt/slot-profiles.toml` (next section). Take the
   ids from `wt model list`.
3. Check it: `wt profile list`, then `wt profile show -A claude -M profile/<name>`.
4. Launch it: pick the `profile/<name>` row in the claude model picker, or
   pin it with `claude-wt -M profile/<name>`.

## The file

`~/.config/agent-wt/slot-profiles.toml`, beside `profiles.toml`. You write
it by hand. wt reads it and never writes it. A missing file means no profile
rows.

```toml
[[profile]]
name    = "mixed"
agent   = "claude"
default = "sonnet"

[profile.slots]
opus   = "openrouter/z-ai/glm-5.3-flash"
sonnet = "ollama/glm-5.3-flash:cloud"
haiku  = "omlx/mlx-community--Qwen3.8-27B-4bit"

[profile.env]
MAX_THINKING_TOKENS = "4096"

[profile.config_content.modelOverrides]
"claude-sonnet-4-6" = "ollama/deepseek-v4-pro:cloud"
```

The model ids above are illustrative. The same entry is the first profile
of the contract fixture `docs/contracts/slot-profiles.sample.toml`, which a
Go test reads, so this format and the parser cannot drift apart.

| Field | Rule |
|---|---|
| `name` | Required. Lowercase letters, digits and hyphens, starting with a letter or digit. Unique per agent |
| `agent` | Required. An agent whose driver supports slots (`claude`) |
| `slots` | Required table, slot name → registry id. At least one entry. Every key must be a slot the agent declares |
| `default` | Required. A slot that `slots` fills. wt passes it as `--model`, so the session starts on it |
| `env` | Optional, string → string |
| `args` | Optional string list |
| `config_content` | Optional free-form table, merged into the agent's settings. `config_content.modelOverrides` is how a profile supplies Claude Code's `modelOverrides` setting; wt does not validate its keys. Checked on <date>: it had no observed effect behind the proxy. |
| `wrapper` | Optional `{binary, args_template}` |

**Unfilled slots use the `default` slot's model.** The example fills three
of four slots; `fable` gets the `sonnet` model. No slot is ever left sending
an Anthropic id.

**The optional fields follow the agent's allow-list**, the one automatic
profiles use ([profiles.md](profiles.md), "Mechanisms by agent"). Claude
allows `env` and `config_content`. `args` and `wrapper` are rejected for it.

**Reserved env keys.** A profile may not set a key wt sets for the launch,
in `env` or in `config_content.env`. For Claude: `ANTHROPIC_BASE_URL`,
`ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_API_KEY`, and
`ANTHROPIC_DEFAULT_<SLOT>_MODEL` and `ANTHROPIC_DEFAULT_<SLOT>_MODEL_NAME`
for each of the four slots.

**At most one local model.** A profile may mix cloud and local models and
models of different providers. It may name one distinct local model, which
may fill several slots.

## When a profile is blocked

wt checks a profile twice. The file alone is checked when it is loaded
(static). The file against the registry, the live inventory and the
`[litellm]` table is checked every time rows are built, and again at launch
(resolved). A profile that fails either check still has a row. The row is
blocked, and its reason is the first failing rule.

Static reasons:

| Reason | Cause |
|---|---|
| `duplicate profile name` | Two profiles share a name for one agent. Neither can launch; the picker shows one blocked row |
| `slots is empty: assign at least one slot` | No `slots` entry |
| `slot "x" is not a slot of agent "claude" (slots: fable, opus, sonnet, haiku)` | A `slots` key the agent does not declare |
| `slot "opus" names no model` | An empty id |
| `default is missing` / `default "x" is not a slot this profile fills` | `default` must name a filled slot |
| `agent "claude" does not support mechanism "wrapper"` | A field outside the agent's allow-list |
| `env sets ANTHROPIC_BASE_URL, which wt sets for the launch` | A reserved key (also `config_content.env sets …`) |
| `wrapper args_template [...] has no "{{args}}" placeholder` | The agent's own arguments would be dropped |

Resolved reasons:

| Reason | Cause |
|---|---|
| `slot haiku: <id> is not in the registry` | The id is not a registry model. This is what a profile shows after `wt cloud-sync` or `wt model rm` removed a model it names |
| `slot opus: <id> is a native model, which has no LiteLLM route` | A slot cannot use the native Anthropic subscription |
| `slot opus: <id> cannot be routed: provider "x" has no LiteLLM mapping` | wt has no route policy for the provider |
| `slots name two local models (<id> and <id>); a slot profile may name one` | More than one distinct local model |
| `a slot profile always uses the LiteLLM proxy, but no URL is configured — run 'wt litellm set --url ... --api-key ...'` | `[litellm]` has no url (or, with `no API key`, no key) |
| `slot haiku: <id> is not on disk or running`, or the pull hint `wt model list` gives | The local model has no row |
| `slot haiku: …` and the local model's own block reason | The local model cannot be started by wt |

Three problems produce no usable row:

- **No usable name or agent.** A profile whose `name` is missing or
  malformed, or whose `agent` is missing, unknown or without slots, has no
  row and cannot be pinned. wt prints one line per launch,
  `wt: slot-profiles.toml[<n>]: <reason> — skipped (see 'wt profile list')`,
  where `<n>` is the profile's position in the file, from 0.
- **A file that does not parse.** No profile rows; one line,
  `wt: parse slot-profiles.toml: … — slot profiles are unavailable`.
- **A registry provider with id `profile`.** `profile/` is a reserved id
  prefix. wt shows no profile rows and prints
  `wt: the registry has a provider with id "profile" — slot profiles are unavailable`.

None of these affects a single-model launch, another profile or an
automatic profile.

## In the picker

A valid profile for the agent appears after the native rows and before every
other row, in file order. The screen below is an illustrative mock-up with
some columns omitted.

```
  FAMILY   MODEL                 LOC    STATUS  RUNNING  COST            7D
  claude   claude/native         cloud  ok      -        -               41
> profile  profile/ollama-cloud  cloud  ok      -        0.40/0.04/1.60   6
  profile  profile/mixed         mixed  ok      -        0.15/0.02/0.60   2
  profile  profile/broken        cloud  ok      -        -
  glm      ollama/glm-5.3-fl...  cloud  ok      -        0.40/0.04/1.60  12

  opus* glm-5.2:cloud · fable kimi-k3:cloud · sonnet glm-5.3-flash:cloud · +1 more
```

| Column | A profile row shows |
|---|---|
| FAMILY | `profile` |
| MODEL | `profile/<name>` |
| LOC | `cloud`, `local`, or `mixed` |
| STATUS | `ok`. A start or blocked profile is shown as a start or blocked model row is, by its action and note |
| RUNNING | The local model's state (`run`, `load` or `-`); `-` for an all-cloud profile |
| COST | The `default` slot model's price in force, with `~` when it is time-priced |
| 1D / 7D / 30D | Launches of this profile |
| In-use prefix, SURVEY | Blank |

- **The line under the table** lists the highlighted profile's slots in
  place of the `LiteLLM: on|off` line: the default first, marked `*`, then
  the rest in the agent's order, each with the model's provider-side name.
  It shows whole entries only; what does not fit becomes `+N more`.
  `wt profile show` gives the full list. On a blocked row the line is the
  reason.
- **Enter** on a launch row launches. On a start row it starts the
  profile's local model through the usual start screen, replace dialog
  included, then launches. On a blocked row it shows the reason.
- **The `> ` marker** sits on the row you launched last, a profile row
  included. When that row is a profile that is present and not blocked, the
  cursor starts on it. Profiles do not rotate: rotation still walks the
  registry models, from where the last single-model launch left it.
- **Filters.** Under `-T` or `-F` profile rows are hidden.
- A table with a profile row never auto-launches its single row.

## Pinning a profile

`-M profile/<name>` launches the profile without the picker. The pin reads
the same row the picker shows.

```bash
claude-wt -W feat -M profile/mixed
```

- A pinned profile whose local model is idle starts it first, as a pinned
  model does. `--replace` lets that start stop a running model.
- A blocked pin: with `-W`, `--cwd` or outside a repository, wt exits
  non-zero with the reason. When the picker screens are showing, wt returns
  to the agent picker with the reason.
- A profile pinned under another agent, an unusable one, or any profile
  under `-T`/`-F` gets `model "profile/<name>" is not in the eligible list for agent "<agent>"`.

Without `-M`, a launch that skips the picker never chooses a profile.
`wt start`, `wt smoke` and rotation ignore slot profiles.

## What a launch does

In order. A failure at any step stops the launch before Claude Code starts,
and nothing is recorded.

1. Check the profile again against fresh state.
2. Refuse passthrough arguments that would replace the slot mapping.
3. Write the LiteLLM route of each cloud slot model that has none.
4. Start the local model if it is not running.
5. Write the local model's route if it was already running.
6. Restart the proxy once, if a route was written and no start restarted it.
7. Check that the proxy answers.
8. Build the command (next section).
9. Apply the profile's `args` and `wrapper`, for an agent that allows them.
10. Run the agent.

**A profile launch restarts the proxy at most once**, and not at all when
every slot's route already exists. A local model started in step 4 stays
running if a later step fails.

**Passthrough arguments.** wt refuses `--settings` and `--model` after
`--`, in either form (`--model opus` or `--model=opus`). Either would
replace the slot mapping. Everything else after `--` is passed on unchanged.

## What Claude Code is given

Process environment:

| Variable | Value |
|---|---|
| `ANTHROPIC_BASE_URL` | The `[litellm]` url |
| `ANTHROPIC_AUTH_TOKEN` | The `[litellm]` api key |
| `ANTHROPIC_API_KEY` | Empty |

Removed from the inherited environment: every `ANTHROPIC_DEFAULT_*`
variable, `ANTHROPIC_SMALL_FAST_MODEL` and `CLAUDE_CODE_SUBAGENT_MODEL`. A
stray shell export of one of them could send a tier to a model the profile
never chose. A profile that wants a subagent model sets
`CLAUDE_CODE_SUBAGENT_MODEL` in its own `env`. Checked on <date> with
Claude Code <version>: an inherited `ANTHROPIC_SMALL_FAST_MODEL` does not
override `ANTHROPIC_DEFAULT_HAIKU_MODEL`; wt removes it all the same.

Arguments: the yolo flag when `--yolo` is given, `--model <default slot>`
(an alias such as `sonnet`, so `/model` inside the session switches between
the profile's slots), and `--settings '<json>'`: one inline JSON object.
For the example profile, with the proxy at `http://localhost:4000`:

```json
{
  "env": {
    "ANTHROPIC_API_KEY": "",
    "ANTHROPIC_BASE_URL": "http://localhost:4000",
    "ANTHROPIC_DEFAULT_FABLE_MODEL": "ollama/glm-5.3-flash:cloud",
    "ANTHROPIC_DEFAULT_FABLE_MODEL_NAME": "glm-5.3-flash:cloud",
    "ANTHROPIC_DEFAULT_HAIKU_MODEL": "omlx/mlx-community--Qwen3.8-27B-4bit",
    "ANTHROPIC_DEFAULT_HAIKU_MODEL_NAME": "mlx-community--Qwen3.8-27B-4bit",
    "ANTHROPIC_DEFAULT_OPUS_MODEL": "openrouter/z-ai/glm-5.3-flash",
    "ANTHROPIC_DEFAULT_OPUS_MODEL_NAME": "z-ai/glm-5.3-flash",
    "ANTHROPIC_DEFAULT_SONNET_MODEL": "ollama/glm-5.3-flash:cloud",
    "ANTHROPIC_DEFAULT_SONNET_MODEL_NAME": "glm-5.3-flash:cloud",
    "MAX_THINKING_TOKENS": "4096"
  },
  "modelOverrides": {
    "claude-sonnet-4-6": "ollama/deepseek-v4-pro:cloud"
  }
}
```

The payload is illustrative and shown indented; wt passes it on one line.
`wt profile show` prints the real one.

- Each `ANTHROPIC_DEFAULT_<SLOT>_MODEL` is a registry id, the name the proxy
  routes. Each `_MODEL_NAME` is the provider-side name, which Claude Code's
  own `/model` picker shows.
- `ANTHROPIC_BASE_URL` and the empty `ANTHROPIC_API_KEY` are repeated in the
  payload so an `env` block in your own Claude Code settings cannot redirect
  the session.
- **Merge order.** `config_content` is merged into the object as automatic
  profiles merge it: tables key by key, a scalar or an array replaced whole.
  In the `env` block the profile's `env` is lowest, `config_content.env`
  next, wt's own variables highest.
- **The token stays out of the command line.** `ANTHROPIC_AUTH_TOKEN` is set
  in the process environment only, so the proxy key does not appear in the
  process list. An `env` block in your own Claude Code settings that sets
  `ANTHROPIC_AUTH_TOKEN` overrides it, as it does for every non-native wt
  launch.

**wt writes no file.** A Claude profile's `config_content` travels only in
`--settings`. `.claude/settings.local.json` is never written, so an
interrupted session leaves nothing behind for a later bare `claude` run in
that worktree. A leftover from an earlier automatic-profile session is still
restored before a profile launch.

**Claude Code may write its own settings.** Choosing a model with `/model`
during a session saves that choice as the `model` field of
`~/.claude/settings.json`. wt does not prevent it. Profile launches are
unaffected, because `--model` outranks the saved value. A later bare
`claude` run starts on the saved alias.

## What wt records

| Record | A profile launch writes |
|---|---|
| `usage.jsonl` | One event under `profile/<name>`. It feeds the row's 1D / 7D / 30D columns and `wt stats` |
| `rotation.state` | Nothing. It stays the id of the last single-model launch |
| `last-picked.state` | `profile/<name>`. This file holds the picker's `> ` marker; a single-model launch writes it too |
| `refcount.jsonl` | One line per distinct slot model, for the session's process |
| LiteLLM spend | Nothing new. The proxy logs each request under the slot model's registry id |

`wt stats` shows a profile as a row of its own, with its launches and `-`
in the spend columns. Its spend is on the slot models' rows. wt does not
attribute spend back to a profile.

After the session: wt prints the summary line naming the profile, releases
the refcount lines, and offers the stop picker as after any non-native
launch. The stop picker is not limited to the profile's models. There is no
survey and no price notice for a profile session.

## Commands

```bash
wt profile list                              # automatic profiles, then slot profiles
wt profile show -A claude -M profile/mixed   # the picker's verdict and what would be passed
```

`wt profile list` prints the slot section whatever state `profiles.toml` is
in, and needs no running provider:

```
slot profiles:
  claude: profile/mixed ok
  claude: profile/broken error: default "x" is not a slot this profile fills
  slot-profiles.toml[2]: error: name is missing
```

With no profile it prints `slot profiles: (none defined)`. A file that does
not parse is one line, `slot profiles: <error>`, and the command exits
non-zero. `list` reports the static check only. A profile it calls `ok` can
still be blocked by the registry or the proxy; `show` says.

`wt profile show -A <agent> -M profile/<name>` reports what the picker
would: `launch`, `start` or `blocked`. It probes the local providers once.
It starts no model, writes no route and restarts nothing. Output for the
example (illustrative):

```
profile/mixed (claude): start
slot fable: ollama/glm-5.3-flash:cloud (glm-5.3-flash:cloud) [default's model]
slot opus: openrouter/z-ai/glm-5.3-flash (z-ai/glm-5.3-flash)
slot sonnet*: ollama/glm-5.3-flash:cloud (glm-5.3-flash:cloud)
slot haiku: omlx/mlx-community--Qwen3.8-27B-4bit (mlx-community--Qwen3.8-27B-4bit)
env: ANTHROPIC_BASE_URL=http://localhost:4000
env: ANTHROPIC_AUTH_TOKEN=***1234
env: ANTHROPIC_API_KEY=
unset: ANTHROPIC_SMALL_FAST_MODEL
unset: CLAUDE_CODE_SUBAGENT_MODEL
unset: ANTHROPIC_DEFAULT_*
args: --model sonnet --settings <settings>
settings:
  { …the payload shown above… }
```

The proxy key is masked as `wt litellm status` masks it. A blocked profile
prints `reason: …` and the command exits non-zero.

`wt profile on|off` and `wt profile status` govern automatic profiles only.

## Failures

Each of these stops before the agent starts. Nothing degrades to a launch
without the profile.

| Failure | What you see |
|---|---|
| The file does not parse | No profile rows; one warning line |
| A profile has no usable name or agent | No row for it; one warning line; listed by `wt profile list` |
| Two profiles share a name | One blocked row, `duplicate profile name` |
| A profile fails a check | A blocked row with the reason |
| LiteLLM has no url or key | A blocked row with the `wt litellm set` hint |
| `--settings` or `--model` after `--` | `a slot profile sets --model itself: remove it from the arguments after --` |
| A route cannot be written, or `config.yaml` is missing | `slot <s>: LiteLLM route for <id> not written: <cause>` |
| The local model's start is refused or fails | The start's own error |
| The proxy restart fails or times out | The restart's error. Routes already written stay for the next attempt |
| The proxy does not answer | `the LiteLLM proxy does not answer at <url>: <cause>` |
| `args` or `wrapper` cannot be applied | The apply error |

In the picker the reason is the status line and you stay on the picker. With
a pin and no picker, wt exits non-zero with it.

## Limits

- Claude Code only. Another agent gets slot profiles when its driver
  implements `profiles.SlotCapable`.
- One distinct local model per profile.
- No slot can use the native Anthropic subscription: Claude Code takes one
  base URL.
- wt does not create or edit the file.
- If the profile's local model is stopped during a session, requests to
  that slot fail at the proxy until it runs again. wt does not detect this.
- A profile launch skips the `ollama list` pre-check, as every proxy launch
  does. An ollama slot model that is registered but not pulled launches, and
  that slot fails at ollama until the model is pulled.
- Claude Code assumes a 200K context window for an id it does not know. A
  profile can set `CLAUDE_CODE_AUTO_COMPACT_WINDOW` in its `env`.
`````

Replace both `<date>` and `<version>` placeholders with the values from the spec's `## Check results`. Apply any replacement Step 1 selected.

Check that no placeholder is left:

```bash
grep -n '<date>\|<version>' wt/docs/wt-agents/slot-profiles.md
```

Expected: no output.

- [ ] **Step 3: Point to the page from `profiles.md`, the claude page and the README**

`wt/docs/wt-agents/profiles.md`. After the paragraph that ends "this page is the operator-facing reference." (line 7), add a blank line and:

```markdown
This page covers **automatic profiles**: unnamed entries wt applies to a
single-model launch that matches them. A **slot profile** is a different
kind: a named entry you pick in the model picker, which assigns a registry
model to each of Claude Code's model slots. Slot profiles have their own
file and page: [slot-profiles.md](slot-profiles.md). `wt profile list` and
`wt profile show` report both kinds; `wt profile on|off|status` and
everything else on this page are about automatic profiles only.
```

Under "### Notes and caveats", delete the whole bullet that begins `- **The claude `ANTHROPIC_DEFAULT_*_MODEL` mapping cannot use` and ends `There is no `{{model_ref}}`/`{{model_id}}` placeholder yet.` (thirteen lines). Put this bullet in its place:

```markdown
- **Mapping claude's model slots is a slot profile's job, not this file's.**
  The `ANTHROPIC_DEFAULT_*_MODEL` variables need the registry id the proxy
  routes, and the only placeholder here, `{{model_name}}`, is the
  provider-side name. A slot profile names a registry model per slot and wt
  passes the ids itself: see [slot-profiles.md](slot-profiles.md). Do not
  set those variables in an automatic profile.
```

`wt/docs/wt-agents/claude-wt.md`. After the paragraph that begins "claude only speaks the `anthropic` wire protocol" and before `## Sessions`, add:

`````markdown
### Slot profiles

A single-model launch redirects only the model it names. Claude Code's other
tiers (background work, subagents, a `/model` switch) keep their Anthropic
ids, which the proxy rejects with `Invalid model name`. A slot profile maps
all four slots (`fable`, `opus`, `sonnet`, `haiku`) to registry models in
one launch, through the LiteLLM proxy:

```bash
claude-wt -M profile/<name>     # or pick the profile/<name> row in the model picker
```

wt then passes `--model <default slot>` and one inline `--settings` JSON
holding `ANTHROPIC_DEFAULT_<SLOT>_MODEL` for every slot. It writes no
settings file. The file format, the picker row and every failure are in
[slot-profiles.md](slot-profiles.md).
`````

`wt/docs/wt-agents/README.md`, three edits.

Under `## Agents`, after the agent table and before `## Verification convention`, add:

```markdown
Claude Code can also be launched with a **slot profile**, which maps each of
its model slots to a registry model in one launch: [slot-profiles.md](slot-profiles.md).
Automatic launch profiles, which adjust a single-model launch, are in
[profiles.md](profiles.md).
```

In the "Common launcher flags" table, replace the `-M` row with:

```markdown
| `-M <id>`, `--model <id>` | Pin the model as `<provider>/<name>` (e.g. `claude/opus`, `ollama/gemma4:9b`). A pinned local model that is not running is started first. Errors if the model is not in the eligible list or its row is blocked. Skips the model picker. Without `-A`, the agent+command picker is shown first, then the pin is validated against the chosen agent. `-M profile/<name>` pins a slot profile instead of a model ([slot-profiles.md](slot-profiles.md)). |
```

Under `### Post-exit order`, after the "Stop picker" bullet (its last line is `  Ctrl+C handling left on the path.`; the sentence is wrapped, so search for that line) and before "See `docs/wt-stats.md`", add:

```markdown
- **After a slot-profile session** the order is shorter: release the
  session's refcount entries (one per slot model) → stop picker → summary
  line. The summary names the profile (`wt: claude · profile/<name> · <duration>`).
  There is no survey and no pricing notice, since a profile is not one
  priced model. The stop picker is the same one: it offers every running
  local model no live session uses, not only the profile's.
```

- [ ] **Step 4: Write the internals notes**

`wt/docs/internals/launch-flow.md`.

Change the title line to:

```markdown
# wt launch flow: post-exit, survey, sessions, profiles, slot profiles
```

In `## Post-exit flow`, add after the "Stale-pricing notice" bullet:

```markdown
- **A slot-profile session has its own tail.** `runSlotAgentCmd` (`cmd/wt/slot_launch.go`) and the `launched.profile` branch of `printPendingSummaryAndSurvey` (`internal/tui/app.go`) run release → stop picker → summary, with no survey and no price notice. The summary is `agents.Summary` over an id-only `config.Model{ID: "profile/<name>"}`. Change the two together, as for the single-model tail.
```

Add this section before `## Sessions (Go)`:

```markdown
## Slot-profile launch

A slot profile (`~/.config/agent-wt/slot-profiles.toml`, hand-written; operator reference [docs/wt-agents/slot-profiles.md](../wt-agents/slot-profiles.md)) is its own launch unit, not a model with extras. It is loaded and statically validated once in `newApp` (`profiles.LoadSlots`, `profiles.ValidateSlots`, into `app.slotChecks`), becomes a catalog row only for a caller that opts in (`catalog.Input.SlotProfiles`), and is launched by one function per path that runs the same ten steps.

| Step | Non-TUI (`launchSlotProfile`, `cmd/wt/slot_launch.go`) | TUI (`internal/tui/slot_flow.go`) |
|---|---|---|
| 1 Re-resolve | `catalog.ResolveSlotProfile` over a fresh `probeInventory` | the same call over `runInventory`, in the first stage's command |
| 2 Passthrough | `agents.CheckSlotPassthrough` | the same |
| 3 Cloud routes | `lifecycle.EnsureModelRoutes` over `ProfileRow.Cloud()`, restart deferred | the same |
| 4 Start | `startForLaunchOwed(cfg, *Local, allowReplace, owed)` when the row is a start row | `beginStartOwed`: the existing start flow, with `lifecycle.Options.RestartOwed` |
| 5 Local route | `EnsureModelRoutes` for a local model that was already running | the same, in the first stage |
| 6 Restart | `lifecycle.RestartProxy` when still owed | the same |
| 7 Probe | `lifecycle.ProbeProxy`, on every launch | the same; after a start it is the second stage (`settleSlotAfterStart`) |
| 8 Build | `agents.BuildSlotLaunchCmd(row.Profile.Launch(), …)` | the same, in `launchSlot` |
| 9 Apply | `applySlotProfile` | `tui.SlotProfiles.Apply`, which `main.go` sets to `applySlotProfile` |
| 10 Run | record, then `runSlotAgentCmd` | `runSlotAndWaitCmd` records and runs |

- **Fail closed.** Every abort returns before the agent starts and before anything is recorded. No error on this path degrades to a launch without the profile. `applySlotProfile` is therefore not `applyResolvedProfile`, which reverts and launches unprofiled: it applies a hand-built `profiles.ResolvedProfile` holding only `ExtraArgs` and `Wrapper`, returns an `ApplyWrapper` error, never prompts and writes no file.
- **Automatic profiles are never applied.** Neither path calls `applyProfileForLaunch`, and the TUI's `runSlotAndWaitCmd` never calls `profileApplier`. `selfHealAgentConfigContentTarget` still runs first, so a leftover `settings.local.json` of an earlier automatic-profile session is restored.
- **`config_content` is delivered by the driver, not the applier.** For claude it travels in the inline `--settings` JSON (`claudeDriver.BuildSlots`, `internal/agents/claude_slots.go`); `.claude/settings.local.json` is never written. The proxy token is in the process environment only, never in argv.
- **The proxy, whatever the toggle says.** Nothing on this path reads `cfg.IsLitellm()`. The url and key come from `cfg.LitellmBaseURL()` and `cfg.LitellmAPIKey()`; a missing one blocks the row (`ResolveSlotProfile`, rules 5a and 5b).
- **One restart at most.** `EnsureModelRoutes` never restarts on success and reports whether a restart is owed. A start is handed that debt as `Options.RestartOwed`, so its one restart publishes the cloud routes too. A refusal (`*OccupiedError`, `*OccupancyUnknownError`) touched nothing and does not spend the debt (`lifecycle.RestartStillOwed`): the caller retries with it, or calls `lifecycle.SettleRoutes` when it gives up.
- **Recording** is two calls at the commit point, after the applier succeeded: `rotation.New().RecordProfile(agent, row.Model.ID)` (the usage event and `last-picked.state`; never `rotation.state`) and `refcount.NewStore().RecordAll(os.Getpid(), ids)` with the id of each `ProfileRow.Distinct()` model. A failed write prints a `note:` line and does not abort.
- **The pin.** `resolveModel` returns the sentinel error `*slotPin` for a `-M profile/<name>` pin whose row is not blocked, and `launchFilteredImpl` hands its row to `launchSlotProfile`. A blocked pin needs no code of its own: `pickerBlockedReason` returns the row's reason. `launchSlotChecks` is process-wide CLI state like `allowReplace`; `runLaunchPath` sets it and prints `slotWarnings` once per run.
- **Commands.** `wt profile list` prints `writeSlotProfileList` whatever `profiles.toml` did. `wt profile show -A <agent> -M profile/<name>` is `showSlotProfile`: one probe, `catalog.Build` with the opt-in input, `catalog.Find`, then `agents.BuildSlotCommand` with the key already masked (`maskKey`), so the real key cannot reach the output. `wt stats` gives a profile's launches a row with no spend (`isSlotProfileID` in `buildUsageRows`).
```

`wt/docs/internals/local-models.md`, three edits.

In `## Local-model resolution`, add at the end of the section's bullet list:

```markdown
- **Slot-profile rows are opt-in and are not model rows.** `catalog.Build` appends them only when `catalog.Input.SlotProfiles` is set, which `buildRows` (the agent's model picker), `resolveModel` (for a `-M profile/<name>` pin) and `wt profile show` do. `wt start` (`localRowsSnap`) and `wt smoke` (`smoke.walk`) never set it. A profile row is a `Row` whose `Model` holds only the id `profile/<name>` and whose `Profile` (`*catalog.ProfileRow`) holds the resolved slot models; `Row.Action` and `Row.BlockReason` return the profile's verdict (`catalog.ResolveSlotProfile`). **Code that reads a `Model` field other than `ID`, calls `ResolveRoute`, or hands `Model` to a launch or a start tests `r.Profile != nil` first** — `ResolveRoute` on the id-only model fails with `unknown provider`. `launchableModels` skips profile rows, so rotation and the single-launchable auto-launch never return one. Rows are hidden under `-T`/`-F` (`HideDiscovered`) and when the registry has a provider `profile` (`catalog.SlotPrefixReserved`). The profile's one local model is judged by the catalog's own policy: a one-model `Build` over the same snapshot, independent of the agent's eligible list.
```

In `## Lifecycle (`internal/lifecycle`)`, add after the bullet that begins `- **Replace (`Exclusive`):**`:

```markdown
- **The slot-profile seams** (`profile_routes.go`). The launch-time check (`EnsureModelRoute`) is single-model, restarts on change and never fails a launch, so a profile launch has its own calls. `EnsureModelRoutes(ctx, out, cfg, models, owedIn)` writes each distinct model's missing route with one locked write per model (`applyRoutes` with `NoRestart`; the changes cannot be one `litellm.Change`, since `AddMissingOnly` is a flag on the whole change and cloud and local slots need different values), never restarts on success, and returns whether a restart is owed. It returns, as a `*RouteEnsureError` naming the model, what the single-model check swallows: a missing `config.yaml` (`litellm.ErrMissing`), `litellm.ErrRegistryRedirected`, a lock that was not free in time, a row's own `Outcome.Err`, and a model `modelRouteChange` declines (`ErrNotRoutable`). On an error it settles what was owed with one asynchronous restart, so a route written before the failure is not left unpublished. `Options.RestartOwed` hands that debt to `Start`: a successful start restarts once for both, a failed one bounces once, and a refusal that touched nothing leaves it owed (`RestartStillOwed(err)`). `RestartProxy` restarts and waits, returning the failure `bounceProxyAsync` only prints; `ProbeProxy` requires a 200 from the liveness endpoint (`WaitReady`, not `Listening`, which counts a timeout as an answer).
```

In `## Rotation (Go)`, replace the first paragraph (it begins "Global rotation: each successful launch records one model id") with:

```markdown
Global rotation: each successful single-model launch records its model id in `~/.config/agent-wt/rotation.state` (atomic write); the picker cursor lands on the model *after* it (`FirstAfter`, shared by the picker and `wt rotate`). Launch paths call `rotation.RecordFor(agent, id)`, which also writes the same id to `~/.config/agent-wt/last-picked.state` and appends the agent-tagged usage event to `~/.config/agent-wt/usage.jsonl`. Rotation positions the cursor only when a last-launched registry model exists (else first launchable row) and never advances onto a discovered row.

**The marker and the rotation pointer are two files.** `rotation.state` is one bare registry id, read whole by `Last()`, so it cannot carry a second value without breaking an older binary. `last-picked.state` (one line, a row id) is where the picker's `> ` marker comes from (`LastPicked()`, which falls back to `Last()` when the file is missing or empty). A slot-profile launch calls `RecordProfile(agent, "profile/<name>")`: it writes `last-picked.state` and the usage event and never touches `rotation.state`, so the marker moves and the rotation position does not. A single-model launch writes both, so a stale profile marker never survives it.
```

In the same section, replace the bullet that begins `- Sort: native models first (#172);` by prefixing its text. The new bullet starts:

```markdown
- Sort: three bands. Native models first (#172); then slot-profile rows, in file order (`ProfileRow.Check.Index`); then every other row. Within the first and last band:
```

and continues with the bullet's existing text from "then cloud + running local by cost" to its end, unchanged.

Add after that bullet:

```markdown
- Starting cursor: when the last-picked row is a profile row that is present and not blocked, the cursor starts on it; profiles do not rotate. Otherwise today's rule, read from `rotation.state`: the next actionable registry model after the pointer (`NextFromEligible` over the actionable rows that are not profile rows), else the first actionable row in sort order, profile rows included.
```

`wt/docs/internals/tui.md`. After the paragraph that begins "Launch-time route check (`checkLaunchRoute`, `start_flow.go`, #192)", add:

```markdown
> **A slot-profile row is drawn from the profile, not from a model** (`modeltable.go`, `modelrows.go`, `layout.go`). `tableInput.slots` (from `model.slotChecks`, which `Run` sets from `tui.SlotProfiles.Checks`) opts the picker in; `refreshTable` copies it into the reduced model its command captures, or the rows vanish on the first refresh. For a row with `Profile != nil`, `renderTable` makes no `ResolveRoute` call and no route decoration: FAMILY is `profile`, LOC is `ProfileRow.Loc`, COST is priced as the default slot's model (`tableRow.costModel`), and the in-use prefix and SURVEY are blank. While it is highlighted, `slotLine` replaces the mode line under the table (`modelFrames`): the filled slots, default first and marked `*`, whole entries only, the rest as `+N more`; on a blocked row, the reason. It adds no line to the frame. The single-row shortcut never fires for a profile row.

Slot-profile launch (`slot_flow.go`): Enter on a profile row that is not blocked calls `beginSlotLaunch`, which enters `phaseSlot` and returns one command. That command re-resolves the profile, checks the passthrough args, writes the routes, restarts the proxy if a write changed `config.yaml`, and probes it: **none of it on the update goroutine**. The seams and `m.cfg` are read in `beginSlotLaunch` and handed to the command, as `checkLaunchRoute` does. Its `slotPreparedMsg` either aborts (`abortSlot`: back to the picker, the reason as status, a re-probed table) or goes on: a launch row to `launchSlot`, a start row to `beginStartOwed`, the existing start flow for the profile's local model with `lifecycle.Options.RestartOwed`. `m.slot` is kept across that start. `finishStart` reads it: success goes to `settleSlotAfterStart` (a second command, the probe) instead of `proceedToLaunch`; a failure or cancel ends the flow; a refusal opens the replace dialog with the flow kept, where "Replace and start" passes the owed restart on and Cancel or esc settles it (`dropSlot`). `launchSlot` builds the command and returns `runSlotAndWaitCmd`, which applies through `slotApplier`, records, and runs; an applier error comes back as `slotAbortMsg`. A `-M profile/<name>` pin reaches the same `beginSlotLaunch` from `enterModelPhase`, and a blocked pin routes back to the agent picker like any blocked row.

> **ctrl+c in `phaseSlot` quits and never launches.** `handleSlotKey` follows `handleRouteKey`: ctrl+c marks the flow `quitting` and quits; every other key is ignored. A quitting flow drops its stage's message, so no agent starts and nothing is recorded in the gap before `QuitMsg`. Each stage is a once-only `settle` (as `routingState.settle`), and `flushRouteNotesAfterRun` calls `settleQuitSlot`: it waits for the stage, announcing the wait when there is one, and starts the restart a quit left owed (a start row's first stage, or the replace dialog). wt never exits under a profile's route write or restart.
```

`wt/docs/internals/agents.md`. After the "Optional capabilities" table and before the paragraph that begins "**Pre-launch `ollamacheck.Check`**", add:

```markdown
**Slot support** is one more optional interface, `profiles.SlotCapable`, declared in `internal/profiles` beside `ProfileCapable` so that package never imports `internal/agents`: `SlotNames()` (the slots, in display order), `ReservedEnvKeys()` (the env keys wt owns for the launch) and `BuildSlots(profiles.SlotBuildInput) (profiles.SlotCommand, error)`. Only claude implements it (`claude_slots.go`: slots `fable`, `opus`, `sonnet`, `haiku`). `agents.SlotsFor(agent)` is the type assertion; `agents.BuildSlotLaunchCmd` gives the driver's `SlotCommand` the treatment `BuildLaunchCmdInfo` gives a single-model launch — binary lookup, `ClearEnv` plus `ClearEnvPrefixes` filtering of the inherited environment, the worktree directory and `PWD`, passthrough args appended unchanged — after `CheckSlotPassthrough` refused the flags the driver names (`RefusedSlotPassthrough`, the optional `SlotPassthroughRefuser`; `--settings` and `--model` for claude, in both the two-argument and the `=` form). It reads the `[litellm]` url and key directly and never `cfg.IsLitellm()` or `ResolveRoute`. `agents.BuildSlotCommand` is the driver call alone, for `wt profile show`. Adding slot profiles to another agent is implementing this interface; the catalog, the picker and the launch flow do not change.
```

`wt/docs/internals/packages.md`. Add two rows after the row for `cmd/wt/profile.go`:

```markdown
| `cmd/wt/slot_launch.go` | the non-TUI slot-profile launch: `launchSlotProfile` (the ten steps, fail closed), `applySlotProfile` (the fail-closed applier both paths use), `runSlotAgentCmd` (run and post-exit), `slotWarnings`, the `slotPin` sentinel `resolveModel` returns, `launchSlotChecks`; seams `ensureSlotRoutes`, `restartProxyNow`, `probeProxyNow`, `settleRoutesNow`, `startSlotModel`, `buildSlotCmd` |
| `cmd/wt/profile_slots.go` | the slot-profile half of `wt profile`: `writeSlotProfileList`, `showSlotProfile`, and `maskKey` (the one rule for showing the proxy key, shared with `wt litellm status`) |
```

Then extend six cells. For each, append the sentence to the end of the Purpose cell, before the closing ` |`:

| Row (Path) | Append |
|---|---|
| `cmd/wt/app.go` | `; also loads and statically validates slot-profiles.toml (app.slotChecks, app.slotLoadErr; seam loadSlotStore)` |
| `cmd/wt/start.go` | `; startForLaunchOwed is the same driver for a caller that already owes a proxy restart (a slot-profile launch)` |
| `internal/rotation/` | `; last-picked.state (the picker's marker: LastPicked, RecordProfile), which a slot-profile launch writes without moving rotation.state` |
| `internal/refcount/` | `; RecordAll records one line per distinct slot model of a profile session` |
| `internal/profiles/` | `; and slot profiles: slot.go (SlotProfile, LoadSlots, SlotPath, the SlotCapable driver interface, MergeConfigContent) and slot_validate.go (ValidateSlots, SlotCheck) — see [Slot-profile launch](launch-flow.md#slot-profile-launch)` |
| `internal/catalog/` | `; profile.go: slot-profile rows (ProfileRow, ResolveSlotProfile — resolved validation — and SlotPrefixReserved), emitted only for an Input that sets SlotProfiles` |

Write each identifier in the appended text in backticks, as the neighbouring cells do. Also append to the `internal/lifecycle/` and `internal/tui/` cells:

```markdown
; `profile_routes.go`: `EnsureModelRoutes`, `RestartProxy`, `ProbeProxy` for a slot-profile launch
```

```markdown
; `slot_flow.go`: the slot-profile launch behind `phaseSlot`
```

`wt/docs/internals/testing.md`. Add this paragraph after the list that begins "Four `TestMain`s guarantee no Go test probes a real server" (after its last bullet, the `internal/spend` one):

```markdown
**A slot-profile launch is closed the same way.** Its route writes, restart, probe and command build are seams in both launch packages, and each `TestMain` makes them fail closed. `cmd/wt` (`slot_launch.go`): `ensureSlotRoutes`, `restartProxyNow`, `probeProxyNow`, `settleRoutesNow`, `startSlotModel`, `buildSlotCmd`, each failing with "<name> not stubbed in this test"; `stubSlotSeams(t)` (`slot_launch_test.go`) swaps all six for recorders and returns the event list (`ensure:<ids>`, `start:<id>:owed=<bool>`, `restart`, `probe`, `settle`, `build`), and `loadSlotStore` stands in for `profiles.LoadSlots`. `internal/tui` (`slot_flow.go`): `ensureSlotRoutes`, `restartProxyNow`, `probeProxyNow`, `buildSlotCmd` fail and `settleRoutesNow` is a no-op; its own `stubSlotSeams(t)` (`slot_flow_test.go`) records the same events under a mutex, because a stage runs in a command that one test runs on its own goroutine, and `slotApplier` is set per test. A stage's batch holds a one-second tick: take the stage out of the batch (`slotStageFrom`) instead of draining it with `drainCmds`, which would sleep through the ticks. `internal/lifecycle`'s `TestMain` does **not** stub `restartProxy` or `waitProxy`: a test of `EnsureModelRoutes`, `RestartProxy` or `ProbeProxy` installs `stubRoutes` or `realRoutes` first. The slot-profile fixtures are `slotTestConfig`, `slotCheckFor` and `slotPicker` (`internal/tui/slot_row_test.go`) and `slotFixtureConfig`, `slotFixtureCheck` and `stubSlotChecks` (`cmd/wt/slot_launch_test.go`); `docs/contracts/slot-profiles.sample.toml` pins the file format.
```

- [ ] **Step 5: Update the two command references**

`wt/docs/wt-config.md`. Replace the state-directory listing (the fenced block that begins `~/.config/agent-wt/`) with:

````markdown
```
~/.config/agent-wt/
├── config.toml          # main agent registry (existing)
├── themes.toml          # active theme (this feature)
├── rotation.state       # the last single-model launch: the rotation position
├── last-picked.state    # the row launched last, model or slot profile: the picker's marker
├── slot-profiles.toml   # hand-written slot profiles; wt reads it and never writes it
└── ...                  # future subcommands land here
```
````

`wt/docs/wt-stats.md`. In `## Usage table`, add this paragraph directly before the heading `## Where spend comes from`:

`````markdown
**A slot profile has a row of its own.** A launch of a slot profile
([wt-agents/slot-profiles.md](wt-agents/slot-profiles.md)) is recorded under
`profile/<name>`, so that id appears as a row with its launch count and `-`
in `REQUESTS`, `PROMPT`, `COMPLETION` and `SPEND` (`null` with `--json`).
The proxy logs each request of the session under the slot model that served
it, so the spend is on those models' rows, which can show requests with no
launch of their own. wt does not add a profile's spend back to its row.
Example (illustrative values):

```
MODEL                        LAUNCHES  REQUESTS     PROMPT  COMPLETION    SPEND
ollama/glm-5.3-flash:cloud          0       212    981,204      40,118  $0.0000
profile/mixed                       3         -          -           -        -
```
`````

Check the link target resolves from `wt/docs/`:

```bash
ls wt/docs/wt-agents/slot-profiles.md
```

Expected: the path is printed.

- [ ] **Step 6: Update the guides**

No guide text below names a real model or a count. Keep it that way.

`docs/guides/00-config-map.md`.

In the TL;DR table, add two rows directly after the row for `~/.config/agent-wt/rotation.state` + `rotation-*.state`:

```markdown
| `~/.config/agent-wt/last-picked.state` | `wt` | `wt` | The row launched last, a model or a slot profile: the model picker's `> ` marker |
| `~/.config/agent-wt/slot-profiles.toml` | you by hand; wt never writes it | `wt` | Slot profiles: named sets that map each of Claude Code's model slots to a registry model ([06-wt-agents-and-models](06-wt-agents-and-models.md) §10). Absent by default |
```

In the section for `rotation.state` (its heading holds both `~/.config/agent-wt/rotation.state` and `rotation-*.state`, each in backticks; `grep -n '^### .*rotation.state' docs/guides/00-config-map.md` prints it), replace the **Purpose** bullet with the text below. It adds one sentence and drops the `:138-175` line range from the source path, which Task 7's edit of that file made stale:

```markdown
- **Purpose:** rotation position — the file body is just the model id of the last single-model launch. A slot-profile launch does not write it. Per-slot files such as `rotation-claude-code-_.state` / `rotation-pi-code-_.state`, if present, are legacy — wt deletes them after its one-time migration (source: `~/github/ohanaverse/local-ai-setup/wt/internal/rotation/rotation.go`).
```

Add two sections directly before the heading for the LaunchAgents plists. In the file that heading is written with backticks around the path; find it with `grep -n 'LaunchAgents/. plists' docs/guides/00-config-map.md`, which prints one line:

`````markdown
### `~/.config/agent-wt/last-picked.state`

- **Owner:** `wt` (every launch that records: a model launch and a slot-profile launch).
- **Consumers:** `wt` (the model picker's `> ` marker, and the starting cursor when the last pick was a slot profile).
- **Purpose:** one line, the id of the row launched last. It is separate from `rotation.state` so that launching a slot profile moves the marker without moving the rotation position. When the file is missing, wt reads `rotation.state` instead, so state written before this file existed keeps working.

Example (illustrative — your id will differ):

```
profile/<name>
```

### `~/.config/agent-wt/slot-profiles.toml`

- **Owner:** you, by hand. wt reads it and never writes it. Absent by default; a missing file means no profile rows.
- **Consumers:** `wt` (the claude model picker, `-M profile/<name>`, `wt profile list`, `wt profile show`).
- **Purpose:** slot profiles. Each `[[profile]]` names an agent, assigns a registry model id to some or all of the agent's model slots, and names the slot the session starts on.

Shape (the ids are placeholders — take real ones from `wt model list`):

```toml
[[profile]]
name    = "<name>"
agent   = "claude"
default = "sonnet"

[profile.slots]
opus   = "<registry id>"
sonnet = "<registry id>"
haiku  = "<registry id>"
```

See what wt makes of the file:

```bash
wt profile list                                # from: any dir, read-only
wt profile show -A claude -M profile/<name>    # from: any dir; probes local providers, changes nothing
```

Full reference: [wt/docs/wt-agents/slot-profiles.md](../../wt/docs/wt-agents/slot-profiles.md).
`````

`docs/guides/03-model-families.md`, under `## Gotchas`. Replace the bullet that begins `- **Tags and families drive agent rotation` with:

```markdown
- **Tags and families drive agent rotation — editing them changes what `wt` offers next launch.** The picker cursor starts after `rotation.state`'s last-launched id (or on the slot profile you launched last, when that was your last pick — [06-wt-agents-and-models](06-wt-agents-and-models.md) §5); narrow the tag/family sets too far and you get `no models for agent "…" in tag "…" — edit your config`. Under `-T` or `-F` slot-profile rows are hidden: a profile has no tags or family of its own.
```

Replace the bullet that begins `- **TUI behavior:**` with:

```markdown
- **TUI behavior:** there is no `d` tag-toggle key, `rotation.state` is a single global slot, and per-tag `rotation-<tag>.state` files are legacy migration inputs that are deleted after migration. The `> ` marker in the picker is read from `last-picked.state`, a second single-slot file beside it.
```

`docs/guides/06-wt-agents-and-models.md`.

In `### 5. Rotation`, replace the last bullet (it begins "- `rotation.state` is written by every model **launch**") with:

`````markdown
- `rotation.state` is written by every single-model **launch**, through the picker or not (`Rotation.RecordFor()`), which also writes `last-picked.state` and appends to `usage.jsonl` (§8). A cancelled ollama warning and `esc` leave rotation untouched.
- The picker's `> ` marker is read from `~/.config/agent-wt/last-picked.state`: the row launched last, which can be a slot profile (§10). Launching a slot profile moves the marker and **does not** move rotation: `rotation.state` keeps the last single-model launch, and the next rotated launch is what it would have been. When your last pick was a slot profile that is still usable, the cursor starts on it.

  ```bash
  cat ~/.config/agent-wt/last-picked.state   # from: any dir, read-only — one line, a row id
  ```
`````

Add this section after the last line of §9 (the line that begins "Full reference — all four agents' mechanisms") and before `## Verification`:

`````markdown
### 10. Slot profiles (Claude Code)

A launch names one model, and Claude Code uses several: its other tiers (background work, subagents, a `/model` switch) keep their Anthropic ids, which the proxy rejects. A **slot profile** maps each of Claude Code's four slots (`fable`, `opus`, `sonnet`, `haiku`) to a registry model, in one launch, always through the LiteLLM proxy. It is a different thing from the automatic profiles of §9: you write it, name it and choose it.

**Step 1 — write the file.** `~/.config/agent-wt/slot-profiles.toml`, by hand (wt never writes it). Take the ids from `wt model list`; the ones below are placeholders:

```toml
[[profile]]
name    = "<name>"          # lowercase letters, digits, hyphens
agent   = "claude"
default = "sonnet"          # the slot the session starts on

[profile.slots]             # slots you leave out use the default slot's model
opus   = "<registry id>"
sonnet = "<registry id>"
haiku  = "<registry id>"
```

A profile may mix cloud and local models and providers. It may name one distinct local model.

**Step 2 — check it.**

```bash
wt profile list                                # from: any dir, read-only — the file's own errors
wt profile show -A claude -M profile/<name>    # from: any dir — launch, start or blocked, and what claude would be given
```

`show` starts no model, writes no route and restarts nothing. A blocked profile prints its reason and exits non-zero.

**Step 3 — launch it.** Pick the `profile/<name>` row in the claude model picker (profile rows sit after the native rows), or pin it:

```bash
claude-wt -M profile/<name>
```

- A profile whose local model is not running starts it first, then launches.
- The launch writes any missing routes and restarts the proxy at most once; with every route in place it restarts nothing.
- A profile that cannot be fully applied does not launch. The picker shows the reason; a pin without the picker exits non-zero with it.
- `--settings` and `--model` after `--` are refused: either would replace the slot mapping.
- wt writes no Claude Code settings file for the launch.

Slot profiles are reached from the picker and the pin only. Rotation, `wt start` and `wt smoke` ignore them, and `-T`/`-F` hide them.

Full reference — every field, every blocked reason, what Claude Code is given: [wt/docs/wt-agents/slot-profiles.md](../../wt/docs/wt-agents/slot-profiles.md).
`````

Under `## Gotchas`, replace the bullet that begins "- **`rotation.state` is written by model launches only**" with:

```markdown
- **`rotation.state` is written by single-model launches only** (`RecordFor()`); the `wt rotate <tag>` probe, `esc`/canceled prompts and slot-profile launches never touch it. One global slot — per-tag/agent state files are legacy migration inputs. The picker's `> ` marker is a second file, `last-picked.state`, which every recorded launch writes.
```

`docs/guides/07-usage-and-spend.md`.

Replace the bullet that begins "- **The "Last wt launch" line.**" with:

```markdown
- **The "Last wt launch" line.** `cat ~/.config/agent-wt/last-picked.state` shows it: the row launched last, a model or a slot profile, and nothing more. `rotation.state` beside it is the last single-model launch (guide 06).
```

Under `## Gotchas`, replace the bullet that begins "- **`rotation.state` is the *last* model launch, nothing more**" with:

```markdown
- **`rotation.state` is the *last* single-model launch, nothing more** — one global slot; `esc`/canceled prompts and slot-profile launches never touch it. It is not a usage summary (guide 06), and `wt stats` does not print it.
- **A slot profile is a row with launches and no spend.** A launch of `profile/<name>` (guide 06 §10) is counted on its own row, and the spend columns there read `-`. The proxy logs the session's requests under the slot models, so the spend is on their rows, which can show requests without a launch. wt does not attribute spend back to a profile.
```

- [ ] **Step 7: Update `wt/CLAUDE.md`, the root `CLAUDE.md` and the changelog**

`wt/CLAUDE.md`, ten edits.

1. `## Key flags`: replace the `-M` row with:

```markdown
| `-M <id>`, `--model <id>` | Pin model as `<provider>/<name>`; cloud or running → launch; a non-running local starts (with a progress display); a blocked model errors with that row's reason. Without `-A`, prompts for the agent first, then validates the pin. `-M profile/<name>` pins a slot profile |
```

2. `## Post-exit flow`: add a bullet at the end of the bullet list:

```markdown
- A slot-profile session has a shorter tail on both paths: release → stop picker → summary, with no survey and no price notice (`runSlotAgentCmd`; the `profile` branch of `printPendingSummaryAndSurvey`).
```

3. `## Docs`: add after the `docs/wt-agents/profiles.md` line:

```markdown
- `docs/wt-agents/slot-profiles.md` — slot profiles: named multi-model launches for Claude Code
```

4. The package table in `## Go module`: add two rows after the `cmd/wt/profile.go` row:

```markdown
| `cmd/wt/slot_launch.go` | `launchSlotProfile` — the non-TUI slot-profile launch (ten steps, fail closed); `applySlotProfile`, `runSlotAgentCmd`, the `slotPin` sentinel |
| `cmd/wt/profile_slots.go` | the slot-profile half of `wt profile list` and `wt profile show`; `maskKey` |
```

and replace four rows:

```markdown
| `internal/rotation/` | global rotation state (`rotation.state`) + next-model selection; `last-picked.state`, the picker's marker |
| `internal/refcount/` | live-session "in use" counts: JSONL keyed by pid, swept for dead pids on every launch, recorded at each launch path's commit point (`RecordAll`: one line per slot model of a profile session) |
| `internal/profiles/` | launch-profile overlays and slot profiles — see [Profiles](#profiles-internalprofiles) |
| `internal/catalog/` | the shared row policy for every model list wt shows or resolves: `Build`, `Find`, `MissingReason`, `Row.Action`; slot-profile rows (`ResolveSlotProfile`), opt-in. No rendering, counts or sorting |
```

5. `## Local-model resolution`: add a bullet at the end of the list:

```markdown
- **A slot-profile row is not a model row**: its `Model` holds only the id `profile/<name>` and `Row.Profile` holds the rest. Rows are emitted only for a caller that sets `catalog.Input.SlotProfiles` (the picker, `resolveModel`, `wt profile show`), never for `wt start` or `wt smoke`. Test `r.Profile != nil` before reading a `Model` field other than `ID`, calling `ResolveRoute`, or launching or starting `Model`.
```

6. `## Lifecycle (`internal/lifecycle`)`: add a bullet after the "Route hook" bullet:

```markdown
- **A slot-profile launch uses its own route calls** (`profile_routes.go`): `EnsureModelRoutes` (several models, restart deferred, every failure returned), `Options.RestartOwed` on `Start` (one restart for the cloud routes and the started model; a refusal leaves it owed, `RestartStillOwed`), `RestartProxy` and `ProbeProxy` (failures returned, not printed). `EnsureModelRoute` stays the single-model check that never fails a launch.
```

7. `## Rotation (Go)`: replace the section's first paragraph and add one bullet. The paragraph becomes:

```markdown
Each successful single-model launch records its model id in `~/.config/agent-wt/rotation.state`; the picker cursor lands on the model after it (`FirstAfter`). `rotation.RecordFor(agent, id)` also writes `last-picked.state` and appends the usage event to `usage.jsonl`.
```

The new bullet, first in the list:

```markdown
- **The marker and the pointer are two files.** `rotation.state` stays one bare registry id. `last-picked.state` holds the row the `> ` marker sits on (`LastPicked`, falling back to `Last`). A slot-profile launch calls `RecordProfile`, which writes the marker and the usage event and never `rotation.state`; a profile row never rotates.
```

In the same list, replace the bullet "**The sort order is also the default selection**…" with:

```markdown
- **The sort order is also the default selection** — changing `sortRows` changes what a bare Enter picks. The bands are native rows, slot-profile rows in file order, then the rest.
```

8. `## Profiles (`internal/profiles`)`: add after the section's two bullets and before the "Read [docs/internals/launch-flow.md]…" line:

```markdown
**Slot profiles** are a second kind, from the hand-written `~/.config/agent-wt/slot-profiles.toml` (wt never writes it): a named entry that maps an agent's model slots to registry models, shown as a picker row and pinned with `-M profile/<name>`. Claude is the only agent with slots (`profiles.SlotCapable`). Operator reference: [docs/wt-agents/slot-profiles.md](docs/wt-agents/slot-profiles.md).

- **A slot profile is its own launch unit**: `launchSlotProfile` (`cmd/wt`) and `slot_flow.go` (`internal/tui`) run the same ten steps. Change both paths together.
- **Fail closed, always the proxy, no file written**: a profile that cannot be fully applied never launches; nothing on the path reads `cfg.IsLitellm()`; claude's `config_content` travels only in the inline `--settings` JSON, and the proxy token only in the process env.
- **Automatic profiles are never applied to a slot-profile launch**: it calls `applySlotProfile`, never `applyProfileForLaunch`.
```

and replace that "Read …" line with:

```markdown
Read [docs/internals/launch-flow.md](docs/internals/launch-flow.md#profiles-internalprofiles) before changing apply order, confirmation, `SelfHeal`, or `wt profile`, and [its slot-profile section](docs/internals/launch-flow.md#slot-profile-launch) before changing a slot-profile launch step, its applier or its recording.
```

9. `## TUI (Go)`: add a bullet after the "**The update goroutine never waits**" bullet:

```markdown
- **A slot-profile launch waits behind `phaseSlot`** (`slot_flow.go`): the route writes, the restart and the probe run in a command, the profile's local model starts through the existing start flow (`beginStartOwed`), and ctrl+c quits without launching or recording; `flushRouteNotesAfterRun` settles the stage and any restart still owed. A profile row is drawn from `Row.Profile`: `renderTable` makes no `ResolveRoute` call for it, and `slotLine` replaces the mode line while it is highlighted.
```

In the section's first paragraph, change "plus starting, routing, replace-confirm and ollama-warning screens" to "plus starting, routing, slot-profile, replace-confirm and ollama-warning screens".

10. `## Verification commands`: in the command block, replace the `wt profile show` line with:

```bash
wt profile show -A <agent> -M <id>   # dry-run profile resolution
wt profile list                      # automatic profiles, then slot profiles with each one's static verdict
wt profile show -A claude -M profile/<name>   # a slot profile: the picker's verdict, the slots, the --settings payload (key masked)
```

Root `CLAUDE.md`. In the `wt/` bullet under `## Architecture`, replace the phrase

```
rotation + usage state, and **all LiteLLM management**
```

with

```
rotation + usage state (`rotation.state`, `usage.jsonl`, and `last-picked.state`, the picker's marker, which a slot-profile launch moves without moving rotation), and **all LiteLLM management**
```

and append to the end of the same bullet (after "keep local-model routes current automatically"):

```
; reads the hand-written `~/.config/agent-wt/slot-profiles.toml` and never writes it — named slot profiles that map each of Claude Code's model slots to a registry model, picked in the model picker or pinned with `-M profile/<name>`, always launched through the LiteLLM proxy (`wt/docs/wt-agents/slot-profiles.md`)
```

Check the phrase was unique and is replaced:

```bash
grep -c 'last-picked.state' CLAUDE.md; grep -c 'slot-profiles.toml' CLAUDE.md
```

Expected: `1` and `1`.

`wt/CHANGELOG.md`. Add as the first entry under `## Unreleased` → `### Added`:

```markdown
- Slot profiles: a named, hand-written entry in
  `~/.config/agent-wt/slot-profiles.toml` assigns a registry model to each of
  Claude Code's model slots (`fable`, `opus`, `sonnet`, `haiku`), so
  background work, subagents and `/model` switches no longer send Anthropic
  ids the LiteLLM proxy rejects. A profile is a row in the claude model
  picker, after the native rows, and `-M profile/<name>` pins it. A launch
  always goes through the proxy, may mix cloud models with one local model
  (started first when idle), restarts the proxy at most once, writes no
  Claude Code settings file, and never starts unless the whole profile
  applies. `wt profile list` and `wt profile show -A claude -M profile/<name>`
  report profiles; `wt stats` gives each its own launch row. Rotation,
  `wt start` and `wt smoke` ignore slot profiles. Every single-model launch
  now also writes `last-picked.state`, the picker's marker, beside
  `rotation.state`. See `docs/wt-agents/slot-profiles.md`.
```

- [ ] **Step 8: Sweep for statements the feature made untrue**

```bash
grep -rn 'mapping cannot use' wt/docs
grep -Li 'slot.profile' wt/docs/wt-agents/slot-profiles.md wt/docs/wt-agents/profiles.md wt/docs/wt-agents/claude-wt.md wt/docs/wt-agents/README.md wt/docs/internals/launch-flow.md wt/docs/internals/local-models.md wt/docs/internals/tui.md wt/docs/internals/agents.md wt/docs/internals/packages.md wt/docs/internals/testing.md wt/docs/wt-config.md wt/docs/wt-stats.md docs/guides/00-config-map.md docs/guides/03-model-families.md docs/guides/06-wt-agents-and-models.md docs/guides/07-usage-and-spend.md wt/CLAUDE.md CLAUDE.md wt/CHANGELOG.md
grep -rnE 'profile/[a-z0-9]' docs/guides
grep -c 'last-picked.state' docs/guides/00-config-map.md docs/guides/06-wt-agents-and-models.md docs/guides/07-usage-and-spend.md docs/guides/03-model-families.md
```

Expected:

- The first command prints nothing: the old caveat is gone.
- The second prints nothing. `grep -L` lists the files with no match, so a path printed here is a file a step above missed.
- The third prints nothing: every profile a guide names is the placeholder `profile/<name>`, never a real one.
- The fourth prints a count of 1 or more for each of the four guides.

Then confirm every identifier the pages name exists. Each line must print at least one match:

```bash
cd wt && for name in 'func launchSlotProfile' 'func applySlotProfile' 'func runSlotAgentCmd' 'func startForLaunchOwed' 'func slotWarnings' 'func writeSlotProfileList' 'func showSlotProfile' 'func maskKey' 'func isSlotProfileID' 'func EnsureModelRoutes' 'func RestartProxy' 'func ProbeProxy' 'func RestartStillOwed' 'func ResolveSlotProfile' 'func SlotPrefixReserved' 'func BuildSlotLaunchCmd' 'func BuildSlotCommand' 'func CheckSlotPassthrough' 'func SlotsFor' 'RecordProfile' 'LastPicked' 'RecordAll' 'func slotLine' 'beginSlotLaunch' 'func runSlotAndWaitCmd' 'func settleQuitSlot' 'beginStartOwed' 'phaseSlot' 'type SlotCapable' 'func LoadSlots' 'func ValidateSlots'; do printf '%-32s %s\n' "$name" "$(grep -rl --include='*.go' -e "$name" . | head -1)"; done
```

Expected: a file path after every name. A name with no path was renamed during implementation: correct it in the pages before going on.

- [ ] **Step 9: Run the link and shell lint**

`bin/check-links` reads its file list from `git ls-files`, so the new page is checked only once git tracks it. Stage it first:

```bash
git add wt/docs/wt-agents/slot-profiles.md
make lint
```

Expected: the `bash -n` lines, then `bin/check-links` reports no broken link and the command exits 0. A broken link is reported as `<file>: <target>`; the usual cause here is a relative path from the wrong directory (`wt/docs/wt-stats.md` links `wt-agents/slot-profiles.md`; the guides link `../../wt/docs/wt-agents/slot-profiles.md`; the internals pages link `../wt-agents/slot-profiles.md`).

- [ ] **Step 10: Run the whole verification**

```bash
make test-all
```

Expected: lint passes again; `llmbench` and `modeltiers` `make check` and `make test` pass; `cd wt && go build ./... && go vet ./... && go test -count=1 ./...` prints `ok` for every package and no `FAIL`. No Go file changed in this task, so a failure here belongs to an earlier task: stop and fix it there.

CI runs `bin/check-config-dirs-untouched` after the Go tests. Do not run it here: on a machine that has ever run wt the directories exist and it always fails. It needs no attention in this task, since no test changed.

- [ ] **Step 11: Commit**

```bash
git add wt/docs/wt-agents/slot-profiles.md wt/docs/wt-agents/profiles.md wt/docs/wt-agents/claude-wt.md wt/docs/wt-agents/README.md \
  wt/docs/internals/launch-flow.md wt/docs/internals/local-models.md wt/docs/internals/tui.md wt/docs/internals/agents.md wt/docs/internals/packages.md wt/docs/internals/testing.md \
  wt/docs/wt-config.md wt/docs/wt-stats.md \
  docs/guides/00-config-map.md docs/guides/03-model-families.md docs/guides/06-wt-agents-and-models.md docs/guides/07-usage-and-spend.md \
  wt/CLAUDE.md CLAUDE.md wt/CHANGELOG.md
git commit -m "docs(wt): slot profiles — the operator page, the launch-flow and lifecycle notes, the guides, and last-picked.state beside rotation.state"
```

### Task 12: Acceptance on the real stack

This task is run by hand, in plain terminals (zsh or bash), on the machine that runs the LiteLLM proxy, after Tasks 2 to 11 are merged on the branch and `make test-all` passes. It writes no Go code and has no test file, so the write-test / see-it-fail steps do not apply: each step is one command block with the result to expect. Unlike the unit tests it uses the real config home, the real proxy and real models: it installs the branch's `wt`, writes `~/.config/agent-wt/slot-profiles.toml`, removes one route from `~/.config/litellm/config.yaml` (wt writes it back), restarts the proxy and starts one local model. Do not run it from inside a Claude Code session.

**Files:**
- Modify: `docs/superpowers/specs/2026-10-13-wt-slot-profiles-design.md` (append `### Acceptance` at the end of the `## Check results` section, which is the end of the file)
- Create, outside the repository: `${TMPDIR:-/tmp}/wt-slot-accept/accept.sh`, `drop_route.py`, `capture.py` (deleted in Step 12)
- Create, in the real config home: `~/.config/agent-wt/slot-profiles.toml` (hand-written sample; removed or restored in Step 12)

**Interfaces:**
- Consumes: the `wt` binary built from this branch (`make -C wt install`), with `wt -A claude` (the picker), `wt -A claude -M profile/<name>`, `wt -W <branch> -A claude -M profile/<name>`, `wt profile list`, `wt profile show -A claude -M profile/<name>`, `wt model list`, `wt litellm list`, `wt stop <id>`; the environment variable `WT_LITELLM_RESTART_CMD` (wt runs it with `/bin/sh -c` for every proxy restart); the files `~/.config/agent-wt/last-picked.state`, `rotation.state` and `refcount.jsonl`; the proxy error log `~/.litellm.err.log`; the table `"LiteLLM_SpendLogs"`; the LaunchAgent `local.litellm.proxy`; `jq`, `/opt/homebrew/bin/psql`, `uv` (to run the screen-capture driver with the `pyte` terminal emulator).
- Produces: the spec subsection `### Acceptance`, one table with the columns `# | Check | Result | Evidence`.

Messages this task expects, word for word:
- blocked reason: `slot haiku: ollama/wt-accept-not-in-registry is not in the registry`
- exit summary of a profile session: `wt: claude · profile/<name> · <duration>`
- restart notice on the non-TUI path: `wt: restarting the LiteLLM proxy for profile/<name>`

- [ ] **Step 1: Build and install the branch's wt**

From the repository root, on the branch that holds Tasks 2 to 11:

```bash
git status --short
make -C wt install
which wt
wt profile list | tail -n 3
```

Expected: `git status` prints nothing; `make` ends with `Installed wt scripts to <home>/.local/bin` (it runs `go build -o bin/wt ./cmd/wt`, copies `wt/bin/*` to `~/.local/bin/` and re-signs the copy); `which wt` prints `~/.local/bin/wt`; the last line of `wt profile list` is `slot profiles: (none defined)`, or the section of a file that already exists. A missing `slot profiles:` line means the old binary is still first on `PATH`.

This replaces the installed `wt`. To go back later: `git switch main && make -C wt install`.

- [ ] **Step 2: Create the scratch directory and the helpers**

```bash
S="${TMPDIR:-/tmp}/wt-slot-accept"
mkdir -p "$S/work"
printf 'one\n' > "$S/work/a.txt"; printf 'two\n' > "$S/work/b.txt"
git init -q "$S/repo" && git -C "$S/repo" commit -q --allow-empty -m init
cat > "$S/accept.sh" <<'EOF'
# Helpers for the slot-profile acceptance checks. Source this file in every
# terminal the checks use; it prints no secret.
S="${TMPDIR:-/tmp}/wt-slot-accept"
ERRLOG="$HOME/.litellm.err.log"
PSQL=/opt/homebrew/bin/psql
REJECT='Invalid model name passed in model='

litellm_field() {
  awk -v k="$1" '
    /^[ \t]*\[/ { on = ($1 == "[litellm]") }
    on && $1 == k { v = $0; sub(/^[^=]*=[ \t]*"/, "", v); sub(/"[ \t]*$/, "", v); print v }
  ' "$HOME/.config/agent-wt/config.toml"
}
PROXY_URL=$(litellm_field url); PROXY_URL="${PROXY_URL%/}"
PROXY_KEY=$(litellm_field api_key)
DSN=$(awk '$1 == "database_url:" { print $2 }' "$HOME/.config/litellm/config.yaml" | tr -d "\"'")

# Every proxy restart wt makes appends one byte to $S/restarts, then runs
# the restart wt would run without this variable.
export WT_LITELLM_RESTART_CMD="printf x >> '$S/restarts'; launchctl kickstart -k gui/$(id -u)/local.litellm.proxy"
restarts() { if [ -f "$S/restarts" ]; then wc -c < "$S/restarts" | tr -d ' '; else echo 0; fi; }

# mark notes the time and the number of rejections logged so far; seen
# reports what the proxy received since the last mark.
mark() {
  T0=$(date -u '+%Y-%m-%d %H:%M:%S')
  E0=$(grep -c "$REJECT" "$ERRLOG")
}
seen() {
  sleep "${SEEN_WAIT:-15}"
  "$PSQL" "$DSN" -At -F ' | ' -c "select coalesce(nullif(model_group, ''), '(no route)'), call_type, status, count(*) from \"LiteLLM_SpendLogs\" where \"startTime\" >= '$T0' group by 1, 2, 3 order by 1, 2, 3"
  E1=$(grep -c "$REJECT" "$ERRLOG")
  echo "new rejections: $((E1 - E0))"
  grep "$REJECT" "$ERRLOG" | tail -n "$((E1 - E0))" | sed -E 's/.*model=([^ ]*)\. Call.*/\1/' | sort | uniq -c
}

# routed says whether each id has a route in config.yaml.
routed() {
  for id in "$@"; do
    if wt litellm list | cut -f1 | grep -Fxq -- "$id"; then echo "routed      $id"; else echo "NOT ROUTED  $id"; fi
  done
}

# proxy_knows says whether the running proxy serves an id.
proxy_knows() {
  if curl -s -H @<(printf 'Authorization: Bearer %s' "$PROXY_KEY") "$PROXY_URL/v1/models" | jq -r '.data[].id' | grep -Fxq -- "$1"; then echo "proxy serves $1"; else echo "proxy does NOT serve $1"; fi
}
EOF
source "$S/accept.sh"
echo "url=$PROXY_URL key=...${PROXY_KEY: -4} dsn=$([ -n "$DSN" ] && echo set) restarts=$(restarts)"
curl -s -o /dev/null -w '%{http_code}\n' "$PROXY_URL/health/liveliness"
```

Expected: `url=http://localhost:4000 key=...<four characters> dsn=set restarts=0`, then `200`.

`seen` prints one line per registry id the proxy served since `mark`, as `<registry id> | <call type> | <status> | <count>`, then the number of ids it rejected and each rejected id. It waits 15 seconds first because the proxy writes spend rows in batches; run `SEEN_WAIT=0 seen` again if a row is missing. Close every other wt session before the checks: their requests add rows, and their restarts are not counted.

Every terminal a later step opens runs `source "${TMPDIR:-/tmp}/wt-slot-accept/accept.sh"` first, so its `wt` counts restarts too, then sets the variables of Step 3.

- [ ] **Step 3: Choose the models and write the sample `slot-profiles.toml`**

```bash
wt model list
```

Pick four different cloud models (LOC `cloud`, STATUS `ok`, not a `<agent>/native` row) that can call tools, and one local model (LOC `local`, STATUS `ok`) whose server is free to start it: an omlx or ollama model is simplest, because an mtplx model asks to replace whatever mtplx has loaded. Set them (the ids below are illustrative; replace all five with ids from the MODEL column):

```bash
CLOUD_A='ollama/kimi-k3:cloud'
CLOUD_B='ollama/glm-5.2:cloud'
CLOUD_C='ollama/glm-5.3-flash:cloud'
CLOUD_D='ollama/deepseek-v4.1-flash:cloud'
LOCAL='omlx/mlx-community--Qwen3.8-27B-4bit'
routed "$CLOUD_A" "$CLOUD_B" "$CLOUD_C" "$CLOUD_D"
```

Expected: four lines that start with `routed`.

Write the file. An existing one is kept as a backup.

```bash
SP=~/.config/agent-wt/slot-profiles.toml
if [ -f "$SP" ]; then cp -p "$SP" "$S/slot-profiles.toml.before"; fi
cat > "$SP" <<EOF
# Acceptance sample. The ids were chosen from 'wt model list' on the run date.

[[profile]]
name    = "accept-cloud"
agent   = "claude"
default = "sonnet"

[profile.slots]
fable  = "$CLOUD_A"
opus   = "$CLOUD_B"
sonnet = "$CLOUD_C"
haiku  = "$CLOUD_D"

[[profile]]
name    = "accept-mixed"
agent   = "claude"
default = "sonnet"

[profile.slots]
sonnet = "$CLOUD_C"
haiku  = "$LOCAL"

[[profile]]
name    = "accept-blocked"
agent   = "claude"
default = "sonnet"

[profile.slots]
sonnet = "$CLOUD_C"
haiku  = "ollama/wt-accept-not-in-registry"
EOF
wt profile list | sed -n '/^slot profiles:/,$p'
wt profile show -A claude -M profile/accept-cloud | head -n 5
wt profile show -A claude -M profile/accept-blocked; echo "exit=$?"
```

Expected:

```
slot profiles:
  claude: profile/accept-cloud ok
  claude: profile/accept-mixed ok
  claude: profile/accept-blocked ok
profile/accept-cloud (claude): launch
slot fable: <CLOUD_A> (<its provider-side name>)
slot opus: <CLOUD_B> (<its provider-side name>)
slot sonnet*: <CLOUD_C> (<its provider-side name>)
slot haiku: <CLOUD_D> (<its provider-side name>)
profile/accept-blocked (claude): blocked
reason: slot haiku: ollama/wt-accept-not-in-registry is not in the registry
```

then an error line that repeats the reason, and `exit=1`. All three profiles are `ok` in the list because the list shows the static check only; the missing id is found when the row is resolved. `wt profile show` starts no model and writes no route.

Run `wt profile show -A claude -M profile/accept-cloud | grep -c -F "$PROXY_KEY"`. Expected: `0`.

- [ ] **Step 4: Acceptance 1 — an all-cloud profile, `/model` switches and a subagent**

```bash
cp -p ~/.config/agent-wt/rotation.state "$S/rotation.state.before" 2>/dev/null
rm -f "$S/restarts"
mark
cd "$S/work" && wt -A claude
```

`$S/work` is outside any git repository, so the model picker opens at once. Expected on the screen:

- the native row first, then `profile/accept-cloud`, `profile/accept-mixed` and `profile/accept-blocked` in that order, then every other row;
- the three profile rows show `profile` under FAMILY; `accept-cloud` shows `cloud` under LOC and `-` under RUNNING, `accept-mixed` shows `mixed`.

Move the cursor to `profile/accept-cloud`. The line under the table changes from `LiteLLM: on` (or `off`) to the slot list: `sonnet*` and its model name first, then `fable`, `opus` and `haiku`, joined by ` · `, with `+N more` in place of entries that do not fit. Press Enter. Claude Code opens with no `apply? [Y/n]` prompt.

While the session is open, in a second terminal (after `source` and Step 3's variables):

```bash
ps -axo args= | grep -e '--settings' | grep -v grep | cut -c1-160
ps -axo args= | grep -e '--settings' | grep -v grep | grep -c -F "$PROXY_KEY"
find "$S/work" -name settings.local.json
```

Expected: one `claude` command line that holds `--model sonnet --settings {"env":{…`; `0` (the token is not in argv); no output from `find` (wt wrote no settings file).

Inside the session, in this order. Wait for each reply before the next line.

1. Accept the trust prompt for the directory.
2. Type `Reply with the single word ok.`
3. Type `/model opus`, then `Reply with the single word ok.`
4. Type `/model haiku`, then `Reply with the single word ok.`
5. Type `/model fable`, then `Reply with the single word ok.`
6. Type `/model sonnet`.
7. Type `Use a subagent to list the files in the current directory, then tell me how many there are.` Check that the transcript shows a subagent run and the answer `2`. If the model answered without a subagent, send the line again.
8. Type `/exit`.

After the exit wt prints `wt: claude · profile/accept-cloud · <duration>`. It asks no survey question and prints no price notice. Then:

```bash
seen
echo "restarts=$(restarts)"
cat ~/.config/agent-wt/last-picked.state
cmp ~/.config/agent-wt/rotation.state "$S/rotation.state.before" && echo "rotation.state unchanged"
```

Expected: `success` rows for `$CLOUD_A`, `$CLOUD_B`, `$CLOUD_C` and `$CLOUD_D`; `new rejections: 0`; `restarts=0` (every route existed, so the proxy was not restarted); `profile/accept-cloud`; `rotation.state unchanged` (when the file did not exist before the check, it must still not exist: `ls ~/.config/agent-wt/rotation.state` fails).

**Pass** when all of that holds. Keep the `seen` output for the results table.

- [ ] **Step 5: Acceptance 2, preparation — stop the local model and remove one cloud route**

```bash
wt model list | grep -F -- "$LOCAL"
```

If the RUNNING column shows `run` or `load`, run `wt stop "$LOCAL"` and repeat until it is blank.

Remove the route of `$CLOUD_C`, the mixed profile's cloud model, from `config.yaml`, and restart the proxy so the running proxy forgets it too. wt has no command that removes one cloud route, so a small script edits the file. No `wt` command may be running while it does.

```bash
cat > "$S/drop_route.py" <<'EOF'
#!/usr/bin/env python3
"""Remove one model_list entry from a LiteLLM config.yaml, by model_name.

usage: drop_route.py CONFIG_YAML MODEL_NAME
The entry runs from its "  - model_name: NAME" line to the next list item
or the next top-level key. The file is replaced in one rename.
"""
import os, sys

path, name = sys.argv[1], sys.argv[2]
with open(path) as f:
    lines = f.readlines()
out, dropping, dropped = [], False, 0
for line in lines:
    if line.startswith("  - "):
        value = line[len("  - model_name:"):].strip().strip("\"'")
        dropping = line.startswith("  - model_name:") and value == name
        dropped += dropping
    elif line[:1] not in (" ", "\n", "#"):
        dropping = False
    if not dropping:
        out.append(line)
if dropped != 1:
    sys.exit("expected one entry named %s, found %d; nothing written" % (name, dropped))
tmp = path + ".drop-route.tmp"
with open(tmp, "w") as f:
    f.writelines(out)
os.chmod(tmp, os.stat(path).st_mode & 0o777)
os.replace(tmp, path)
print("removed the route for " + name)
EOF
cp -p ~/.config/litellm/config.yaml "$S/config.yaml.before"
python3 "$S/drop_route.py" ~/.config/litellm/config.yaml "$CLOUD_C"
routed "$CLOUD_C"
launchctl kickstart -k "gui/$(id -u)/local.litellm.proxy"
until curl -sf -o /dev/null "$PROXY_URL/health/liveliness"; do sleep 1; done
proxy_knows "$CLOUD_C"
rm -f "$S/restarts"
```

Expected: `removed the route for <CLOUD_C>`; `NOT ROUTED  <CLOUD_C>`; after the restart, `proxy does NOT serve <CLOUD_C>`. The restart typed here does not go through wt, so the counter stays at zero.

If the check is abandoned after this step, `wt litellm sync` writes the route back.

- [ ] **Step 6: Acceptance 2 — the mixed profile starts its local model and restarts the proxy once**

```bash
mark
cd "$S/work" && wt -A claude
```

The `> ` marker and the cursor are on `profile/accept-cloud`, the last-picked row. Move to `profile/accept-mixed`. Expected: LOC `mixed`, RUNNING `-`, and the trailing start note a stopped local model's row shows. Press Enter. The start screen of the local model appears, as it does for a single stopped model; then Claude Code opens.

Leave the session open. In a second terminal (after `source` and Step 3's variables):

```bash
echo "restarts=$(restarts)"
routed "$CLOUD_C" "$LOCAL"
proxy_knows "$CLOUD_C"; proxy_knows "$LOCAL"
wt model list | grep -F -- "$LOCAL"
```

Expected: `restarts=1`; both ids `routed`; the proxy serves both; RUNNING shows `run`.

In the session:

1. Type `Reply with the single word ok.` (the default slot, the cloud model)
2. Type `/model haiku`, then `Reply with the single word ok.` (the local model)

Do not exit. In the second terminal:

```bash
seen
```

Expected: `success` rows for `$CLOUD_C` and `$LOCAL`, and `new rejections: 0`.

**Pass** when the model started, `restarts` is exactly `1`, and both slots answered.

- [ ] **Step 7: Acceptance 3 — a second session's stop picker does not offer the local model**

The session of Step 6 is still open. In the second terminal:

```bash
jq -c . ~/.config/agent-wt/refcount.jsonl
```

Expected: exactly two lines with the same `pid`, one with `"model_id":"<CLOUD_C>"` and one with `"model_id":"<LOCAL>"`: one line per distinct slot model, although `accept-mixed` maps four slots.

Still in the second terminal, launch and leave a second profile session:

```bash
cd "$S/work" && wt -A claude -M profile/accept-cloud
```

No picker opens: with an agent and a pin outside a repository this is the non-TUI path. When Claude Code is up, type `/exit`.

Expected after the exit: the stop picker does not list `$LOCAL`. If no other local model is running it does not appear at all, and the next thing printed is `wt: claude · profile/accept-cloud · <duration>`.

Now type `/exit` in the first session (the one from Step 6). Expected: its stop picker offers `$LOCAL`, which no session uses any more. Choose it, so the model is stopped for Step 10; if you declined, run `wt stop "$LOCAL"`.

```bash
wt model list | grep -F -- "$LOCAL"
jq -c . ~/.config/agent-wt/refcount.jsonl
```

Expected: RUNNING is blank; no refcount line is left for either session.

**Pass** when the second session was not offered `$LOCAL` and the first was.

- [ ] **Step 8: Acceptance 4 — `-W` with a pin, and a blocked pin**

`$S/repo` is a scratch git repository, so `-W` has a repository to create its worktree in. wt installs its main guard in that repository, as it does in any repository it launches from; the repository is deleted in Step 12.

```bash
rm -f "$S/restarts"
mark
cd "$S/repo" && wt -W accept-pin -A claude -M profile/accept-cloud
```

Expected: no picker; Claude Code opens in `$S/repo/.worktrees/accept-pin`. Type `Reply with the single word ok.`, then `/model haiku`, then `Reply with the single word ok.`, then `/exit`. wt prints `wt: claude · profile/accept-cloud · <duration>`.

```bash
seen
echo "restarts=$(restarts)"
find "$S/repo" -name settings.local.json
cat ~/.config/agent-wt/last-picked.state
```

Expected: `success` rows for `$CLOUD_C` and `$CLOUD_D`; `new rejections: 0`; `restarts=0`; no output from `find`; `profile/accept-cloud`.

The blocked pin:

```bash
mark
cd "$S/repo" && wt -W accept-pin -A claude -M profile/accept-blocked; echo "exit=$?"
SEEN_WAIT=5 seen
```

Expected: Claude Code does not open. The last line on stderr is `wt: slot haiku: ollama/wt-accept-not-in-registry is not in the registry` (cobra prints the same reason above it as `Error: …`, with the usage text), then `exit=1`. `seen` shows no row from this command and `new rejections: 0`.

**Pass** when the pinned profile launched without the picker and the blocked pin exited non-zero with that reason.

- [ ] **Step 9: Write the screen-capture driver**

The driver runs a program in a pty of a fixed size, answers the terminal queries wt's screens wait for (the OSC 11 background-colour query and the cursor-position request), feeds the output to a terminal emulator and writes the emulator's screen as text. `uv` fetches the emulator (`pyte`) into its own cache; nothing is installed.

```bash
cat > "$S/capture.py" <<'EOF'
#!/usr/bin/env python3
"""Drive a terminal program in a pty of a fixed size and save text screens.

usage: capture.py COLS ROWS OUTDIR STEP... -- COMMAND [ARG...]

Steps, run in order:
  wait:SECONDS   keep reading the program's output for that long
  key:NAME       send one key: up down enter esc tab home end ctrl-c,
                 or any single character
  snap:NAME      write the screen as OUTDIR/NN-NAME.txt
When the steps are done the pty is closed, which hangs the program up.
"""
import fcntl, os, pty, select, struct, sys, termios, time

import pyte

KEYS = {"up": b"\x1b[A", "down": b"\x1b[B", "enter": b"\r", "esc": b"\x1b",
        "tab": b"\t", "home": b"\x1b[H", "end": b"\x1b[F", "ctrl-c": b"\x03"}
OSC11_QUERY = b"\x1b]11;?"
OSC11_REPLY = b"\x1b]11;rgb:0000/0000/0000\x1b\\"
CURSOR_REPLY = b"\x1b[1;1R"


def main():
    cut = sys.argv.index("--")
    cols, rows, outdir = int(sys.argv[1]), int(sys.argv[2]), sys.argv[3]
    steps, command = sys.argv[4:cut], sys.argv[cut + 1:]
    os.makedirs(outdir, exist_ok=True)
    screen = pyte.Screen(cols, rows)
    stream = pyte.ByteStream(screen)

    pid, fd = pty.fork()
    if pid == 0:
        os.environ["TERM"] = "xterm-256color"
        os.execvp(command[0], command)
    fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0))

    def pump(seconds):
        end = time.time() + seconds
        while True:
            left = end - time.time()
            if left <= 0:
                return
            ready, _, _ = select.select([fd], [], [], left)
            if not ready:
                return
            try:
                data = os.read(fd, 65536)
            except OSError:
                return
            if not data:
                return
            if OSC11_QUERY in data:
                os.write(fd, OSC11_REPLY + CURSOR_REPLY)
            stream.feed(data)

    count = 0
    for step in steps:
        kind, _, arg = step.partition(":")
        if kind == "wait":
            pump(float(arg))
        elif kind == "key":
            os.write(fd, KEYS.get(arg, arg.encode()))
            pump(0.4)
        elif kind == "snap":
            count += 1
            path = os.path.join(outdir, "%02d-%s.txt" % (count, arg))
            lines = [line.rstrip() for line in screen.display]
            with open(path, "w") as f:
                f.write("\n".join(lines) + "\n")
            print(path)
        else:
            sys.exit("unknown step: " + step)
    os.close(fd)
    try:
        os.waitpid(pid, 0)
    except ChildProcessError:
        pass


main()
EOF
cd "$S/work" && uv run --no-project --with pyte python3 "$S/capture.py" 80 24 "$S/screens/smoke" \
  wait:2 snap:help -- wt --help
head -n 3 "$S/screens/smoke/01-help.txt"
```

Expected: the path `…/screens/smoke/01-help.txt`, then lines from the end of `wt --help` as an 80x24 terminal shows them. That proves the driver runs; `wt --help` asks the terminal nothing.

- [ ] **Step 10: Capture the picker at 80x24 — rows, slot line, blocked row**

This run opens the picker, moves the cursor and quits with ctrl+c. It launches nothing. `key:home` puts the cursor on the first row; each `key:down` moves it one row. With one native row above the profiles (`claude/native`), one `down` reaches `profile/accept-cloud`. If the first snapshot shows a different number of native rows, change the number of `key:down` steps before `snap:slot-line` to match and run it again.

```bash
cd "$S/work" && uv run --no-project --with pyte python3 "$S/capture.py" 80 24 "$S/screens/picker" \
  wait:4 snap:rows \
  key:home key:down snap:slot-line \
  key:down snap:start-row \
  key:down snap:blocked-row \
  key:enter snap:blocked-enter \
  key:ctrl-c wait:1 -- wt -A claude
for f in "$S"/screens/picker/*.txt; do echo "=== $f"; cat "$f"; done
```

Read the five snapshots. Each is the whole 80x24 screen. Check, by eye:

| Snapshot | What must be there |
|---|---|
| `01-rows` | The column header on the screen; the native row, then `profile/accept-cloud`, `profile/accept-mixed`, `profile/accept-blocked`, then the other rows; `> ` in front of `profile/accept-cloud` (last picked); `profile` under FAMILY; no line wrapped onto a second line |
| `02-slot-line` | Under the table, in place of the `LiteLLM: on` line: `sonnet*` with its model name first, then `fable`, `opus`, `haiku`; only whole entries, and `+N more` when one does not fit; no price note; the frame has the same number of lines as `01-rows` |
| `03-start-row` | `profile/accept-mixed` with LOC `mixed`, RUNNING `-` and the start note; the slot line lists `sonnet*` and `haiku` |
| `04-blocked-row` | `profile/accept-blocked`; the line under the table shows `slot haiku: ollama/wt-accept-not-in-registry is not in the registry`, cut with `…` if it is longer than the line, never wrapped |
| `05-blocked-enter` | Still the picker, with the reason shown; Claude Code did not start |

A table that lost its header, a wrapped line, or a slot line that ends in half an entry is a failure: fix it in Task 5's code and its screen-fit tests, then repeat this step.

- [ ] **Step 11: Capture the start flow at 80x24**

This run presses Enter on the start row, so it starts the local model for real and launches Claude Code inside the pty. The driver then closes the pty, which ends both. Make sure the model is stopped first.

```bash
wt model list | grep -F -- "$LOCAL"      # RUNNING must be blank; else: wt stop "$LOCAL"
cd "$S/work" && uv run --no-project --with pyte python3 "$S/capture.py" 80 24 "$S/screens/start" \
  wait:4 key:home key:down key:down snap:before \
  key:enter wait:1 snap:start-1 \
  wait:4 snap:start-2 \
  wait:15 snap:start-3 \
  wait:60 snap:after -- wt -A claude
for f in "$S"/screens/start/*.txt; do echo "=== $f"; cat "$f"; done
sleep 3
wt stop "$LOCAL"
wt model list | grep -F -- "$LOCAL"
```

Use the same number of `key:down` steps as Step 10 used to reach `profile/accept-mixed`. A model that takes longer than 80 seconds to load needs a larger last `wait`.

Check, by eye: `01-before` has the cursor's slot line for `accept-mixed`; `02-start-1` to `04-start-3` show the start screen for `$LOCAL` (the same screen a single stopped model shows), every line inside 80 columns and the frame inside 24 rows; `05-after` shows Claude Code (its trust prompt or its input box), not a wt error. After `wt stop`, RUNNING is blank again.

If a snapshot shows the replace dialog instead, the local model's server was occupied: pick a local model whose server is free (Step 3), or record the dialog as seen and answer it in a hand-run session.

- [ ] **Step 12: Restore the machine**

```bash
SP=~/.config/agent-wt/slot-profiles.toml
if [ -f "$S/slot-profiles.toml.before" ]; then cp -p "$S/slot-profiles.toml.before" "$SP"; else rm -f "$SP"; fi
routed "$CLOUD_C"
wt model list | grep -F -- "$LOCAL"
wt profile list | tail -n 1
unset WT_LITELLM_RESTART_CMD
```

Expected: `routed      <CLOUD_C>` (wt wrote the route back in Step 6; if it says `NOT ROUTED`, run `wt litellm sync`); RUNNING blank; `slot profiles: (none defined)`, or your own profiles.

Copy what the results table needs out of `$S` first (the `seen` outputs you kept and the snapshots under `$S/screens`), then:

```bash
rm -rf "$S"
```

Left behind on purpose: `~/.config/agent-wt/last-picked.state` holds `profile/accept-cloud`, which names no row any more, so the picker falls back to the rotation pointer; the launches of `profile/accept-cloud` and `profile/accept-mixed` stay in `usage.jsonl` and show as two rows in `wt stats`. To put the released `wt` back, run `git switch main && make -C wt install`; leave the branch's binary installed if the branch is about to merge.

- [ ] **Step 13: Append the results to the spec**

Open `docs/superpowers/specs/2026-10-13-wt-slot-profiles-design.md` and append this subsection at the end of the file, after the `## Check results` table, with one blank line before the heading.

```markdown
### Acceptance

Run on <date> with wt built from <branch> at <commit> and Claude Code <version>, against the LiteLLM proxy at <url>. The model ids are those of the run date.

| # | Check | Result | Evidence |
|---|---|---|---|
| 1 | An all-cloud profile: `/model` switches and a subagent | pass / fail | … |
| 2 | A mixed profile, local model idle, one cloud route removed | pass / fail | … |
| 3 | A second session's stop picker does not offer the in-use local model | pass / fail | … |
| 4 | `-W -M profile/<name>` launches; a blocked pin exits non-zero | pass / fail | … |
| – | Real screens at 80x24: rows, slot line, blocked row, start flow | pass / fail | … |
```

Fill it in by these rules:

- `<commit>` is `git rev-parse --short HEAD`; `<version>` is `claude --version`. Never write the proxy key.
- **Evidence**, row by row:
  - 1: the `seen` rows of Step 4, `new rejections: 0`, `restarts=0`, and that `rotation.state` was unchanged.
  - 2: `restarts=1`, the `seen` rows of Step 6, and that the model went from blank to `run`.
  - 3: the two refcount lines, and what each session's stop picker offered.
  - 4: the `seen` rows of Step 8, and the blocked pin's last stderr line with its exit code.
  - last row: the names of the snapshots looked at, and any line that was cut with `…` or `+N more`.
- A `fail` row names what was seen instead. Do not merge the branch with a `fail` row: fix the cause in the task that owns the code, re-run that check and replace the row.

- [ ] **Step 14: Verify and commit**

```bash
tail -n 11 docs/superpowers/specs/2026-10-13-wt-slot-profiles-design.md
git status --short
```

Expected: the subsection, with no `…`, `<date>`, `<branch>`, `<commit>`, `<version>` or `<url>` left in it and every Result `pass`; `git status` lists the spec and nothing else.

```bash
git add docs/superpowers/specs/2026-10-13-wt-slot-profiles-design.md
git commit -m "docs(spec): slot profiles, the acceptance results on the real stack"
```
