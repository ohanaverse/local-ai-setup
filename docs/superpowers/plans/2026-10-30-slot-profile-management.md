# Slot profile management Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `wt profile slots add|set|rm|usage|candidates` and a `manage-slot-profiles` skill that cleans up, updates and creates slot profiles from current tier scores.

**Architecture:** `wt` gains one comment-preserving, backed-up, re-validated writer for `slot-profiles.toml` (`internal/slotedit`), a ranking package over the registry and `tiers.toml` (`internal/candidates`), and read-only usage data. The skill drives the conversation, refreshes `modeltiers`, researches thin scores, and runs `wt smoke`; it never edits the file by hand.

**Tech Stack:** Go 1.26.7 (`wt/`, cobra, BurntSushi/toml), a project skill (`.claude/skills/`), Markdown docs.

**Spec:** `docs/superpowers/specs/2026-10-30-slot-profile-management-design.md`

## Global Constraints

- Module root is `wt/`; run `go build ./...` and `go test ./...` from there.
- wt reads `slot-profiles.toml` today and never writes it; this plan makes the new `slotedit` package its only writer, and updates every doc that says otherwise.
- Every write: back up first as `slot-profiles.toml.bak-<YYYYMMDD-HHMMSS>`, change only the targeted entry's bytes (comments elsewhere survive), write atomically (`config.WriteFileAtomic`), re-parse with `profiles.ParseSlots` + `profiles.ValidateSlots`, and restore the backup if the result is worse than the input.
- No change to `launchSlotProfile`, `slot_flow.go`, the picker, or `wt profile list|show`.
- No writes to `registry.toml`, `tiers.toml`, or the LiteLLM config. No network access in `wt`.
- Tests use scratch paths: set `XDG_CONFIG_HOME` (t.TempDir), `WT_REGISTRY`, `WT_LITELLM_CONFIG`, `MODELTIERS_OUT`. `bin/check-config-dirs-untouched` must still pass.
- Guides stay state-independent: no model names or counts in `docs/guides/`.
- The `tiers.toml` keys read here (`open_weights`, `tool_call`, `generated_at`, `[sources.*] status`) must be in `docs/contracts/tiers.sample.toml` in the same change.
- A comment belongs to the entry it sits directly above: contiguous `#` lines with no blank line before the `[[profile]]` line. A blank line ends the block.

## Review Focus

- Add to a missing file, an empty file, and a file whose last line has no trailing newline: each yields a valid file (Task 2).
- Set/rm on an entry with a `[profile.config_content.modelOverrides]` subtable or a mid-entry comment: every byte outside the change is identical (Task 2).
- Rm of the first entry keeps a file-header comment that is separated from it by a blank line (Task 2).
- A `set` that would break a profile (default names an unfilled slot, slot id not in the registry): file unchanged, no backup file left (Task 3).
- `candidates` with a missing or unparseable `tiers.toml`: every model listed as unrated with a note, no error exit; a model whose `tool_call` is unknown is listed with a flag, one with `tool_call = false` is excluded (Task 5).
- `usage` for a profile with no launch in the window shows count 0 and no last launch, not an error; history of a renamed profile (`profile/old`) is not reported (Task 4).

---

### Task 1: Read `open_weights`, `tool_call` and freshness from `tiers.toml`

**Files:**
- Modify: `wt/internal/tiers/tiers.go` (Entry, fileDoc, Parse; add Entries, Freshness)
- Modify: `docs/contracts/tiers.sample.toml` (ensure the keys appear; the contract test is `wt/internal/tiers/contract_test.go`)
- Test: `wt/internal/tiers/tiers_test.go`

**Interfaces:**
- Produces:
  - `Entry` gains `OpenWeights bool`, `ToolCall bool`, `HasToolCall bool`.
  - `func (ix Index) Entries() []Entry` — all entries sorted by `ProviderID`, then `ModelName`.
  - `type Freshness struct { GeneratedAt time.Time; StaleSources []string }`; `StaleSources` lists the names of `[sources.<name>]` whose `status` is not `"ok"`, sorted.
  - `func ParseFreshness(data []byte) (Freshness, error)` and `func ReadFreshness() (Freshness, bool)` (reads `Path()`; false when the file is missing, unreadable or has no `generated_at`).

- [ ] **Step 1: Write failing tests** `TestParseReadsToolCallAndOpenWeights` (entry with `tool_call = true`, `open_weights = true` → both set; entry without `tool_call` → `HasToolCall == false`), `TestIndexEntriesSorted`, `TestParseFreshness` (fixture with `generated_at = "2026-10-30T15:30:47Z"`, `[sources.openrouter] status = "stale"`, `[sources.arena] status = "ok"` → `StaleSources == ["openrouter"]`), `TestReadFreshnessMissingFile` (false).
- [ ] **Step 2: Run** `cd wt && go test ./internal/tiers -run 'ToolCall|Entries|Freshness' -v`. Expected: FAIL (undefined).
- [ ] **Step 3: Implement** the fields and functions above. `tool_call` and `open_weights` decode as `any` and read as none unless a bool (the file's existing "one odd key must not unrate every model" rule).
- [ ] **Step 4: Update the fixture** so it contains `open_weights`, `tool_call`, `generated_at` and a `[sources.*] status`; run `go test ./internal/tiers ./cmd/wt -run 'Contract' -v`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(wt): tiers reader exposes tool_call, open_weights and freshness`.

---

### Task 2: `slotedit` text editor (Add, Set, Remove)

**Files:**
- Create: `wt/internal/slotedit/edit.go`
- Test: `wt/internal/slotedit/edit_test.go`

**Interfaces:**
- Consumes: `profiles.SlotProfile` (fields `Name, Agent, Default, Slots, Env`).
- Produces:
  - `type Patch struct { Slots map[string]string; Default *string; SetEnv map[string]string; UnsetEnv []string }`
  - `func Add(src []byte, p profiles.SlotProfile, comment string) ([]byte, error)` — appends an entry; error if `p.Name` already exists. Layout matches the hand-written file: optional `# ` comment lines, `[[profile]]` with `name`, `agent`, `default`, `[profile.slots]` in the order fable, opus, sonnet, haiku, `[profile.env]` with keys sorted; one blank line between entries; the result ends in exactly one newline.
  - `func Set(src []byte, name string, patch Patch) ([]byte, error)` — error if `name` matches no entry or more than one.
  - `func Remove(src []byte, name string) ([]byte, error)` — removes the entry's lines and the comment block directly above it.
  - Entry block = from its `[[profile]]` line to the line before the next `[[profile]]`'s comment block (or EOF). Detection is line-based on lines whose trimmed text is `[[profile]]`; the name comes from the first `name = "..."` line in the block.

- [ ] **Step 1: Write failing tests** (table tests over inline TOML strings):
  - `TestAddToEmptyAndMissingFile` (nil and `[]byte("")` both give a parsable file with one profile via `profiles.ParseSlots`).
  - `TestAddKeepsExistingBytes` (output has the input as a prefix; input without trailing newline gets one).
  - `TestAddDuplicateName` errors.
  - `TestSetReplacesSlotLineOnly` (change `sonnet`; diff against input is exactly that one line).
  - `TestSetAddsMissingSlotAndEnvTable` (an entry with no `[profile.env]` gains one; its comments untouched).
  - `TestSetUnsetEnvRemovesLineOnly`.
  - `TestSetKeepsSubtablesAndMidEntryComments` (entry with `[profile.config_content.modelOverrides]` and a comment above `[profile.env]`).
  - `TestRemoveTakesCommentBlockNotHeader` (file header comment, blank line, comment + entry: header survives, comment block goes).
  - `TestRemoveMiddleEntryLeavesNeighbours` (neighbour's comment block is intact).
  - `TestSetAmbiguousName` (two entries named alike) errors.
- [ ] **Step 2: Run** `cd wt && go test ./internal/slotedit -v`. Expected: FAIL (package missing).
- [ ] **Step 3: Implement** with a small block scanner (`splitEntries(src) (header []byte, entries []entryBlock)`) shared by all three functions; `Set` edits lines inside the block by table (`[profile.slots]`, `[profile.env]`), inserting a missing key at the end of its table and a missing table at the end of the block before any trailing blank lines.
- [ ] **Step 4: Run tests.** Expected: PASS.
- [ ] **Step 5: Commit** `feat(wt): slotedit edits slot-profiles.toml entries in place`.

---

### Task 3: Safe write: backup, validate, atomic, restore

**Files:**
- Create: `wt/internal/slotedit/apply.go`
- Test: `wt/internal/slotedit/apply_test.go`

**Interfaces:**
- Consumes: Task 2 functions; `profiles.ParseSlots`, `profiles.ValidateSlots(store, d) []profiles.SlotCheck` (fields `Profile`, `Usable`, `Err`), `profiles.SlotPath()`, `config.WriteFileAtomic(path string, data []byte, perm os.FileMode) error`.
- Produces:
  - `type Options struct { Drivers profiles.SlotDrivers; ModelIDs []string; Target string; Now func() time.Time }` — `Target` is the profile that must be usable afterwards ("" for a removal); `ModelIDs` are the registry ids a slot may name.
  - `func Apply(path string, edit func([]byte) ([]byte, error), o Options) (backup string, err error)` — a missing `path` is an empty input and creates no backup. Refuses (file untouched, no backup written) when: the new text does not parse; `Target` is not usable (`SlotCheck.Err` returned in the error); any slot of `Target` names an id not in `ModelIDs`; a profile that was usable before is not usable after; `Unknown` has more keys than before. Backup name `<path>.bak-<20060102-150405>` from `o.Now()`.

- [ ] **Step 1: Write failing tests** with a stub `SlotDrivers` from `agents.SlotDrivers()` and a temp dir: `TestApplyWritesBackupAndFile`, `TestApplyRefusesUnknownModelID` (file bytes unchanged, `filepath.Glob(path+".bak-*")` empty), `TestApplyRefusesDefaultOnUnfilledSlot`, `TestApplyRefusesBreakingAnotherProfile`, `TestApplyMissingFileCreatesWithoutBackup`, `TestApplyBackupNamesDoNotCollideSameSecond` (second backup in the same second gets a `-2` suffix).
- [ ] **Step 2: Run** `go test ./internal/slotedit -run Apply -v`. Expected: FAIL.
- [ ] **Step 3: Implement `Apply`** as: read → edit → `ParseSlots` → compare `ValidateSlots` before/after → write backup (copy, mode 0644) → `WriteFileAtomic`.
- [ ] **Step 4: Run** `go test ./internal/slotedit -v`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(wt): slotedit.Apply backs up and validates every write`.

---

### Task 4: Launch history per profile

**Files:**
- Modify: `wt/internal/usage/usage.go` (add method)
- Test: `wt/internal/usage/usage_test.go`

**Interfaces:**
- Consumes: the private `scan(asOf time.Time, fn func(ev event, age time.Duration))` and `AllCounts(agent string, asOf time.Time) map[string]UsageCounts`.
- Produces: `func (s *StoreImpl) LastLaunch(agent string, asOf time.Time) map[string]time.Time` — last event time per `model_id` for the agent, within the retention window.

- [ ] **Step 1: Write failing test** `TestLastLaunch`: three events for `profile/a` (agent claude) and one for `profile/b` (agent pi); `LastLaunch("claude", now)` has only `profile/a`, with the latest timestamp.
- [ ] **Step 2: Run** `go test ./internal/usage -run LastLaunch -v`. Expected: FAIL.
- [ ] **Step 3: Implement** by scanning once and keeping the maximum timestamp per id.
- [ ] **Step 4: Run** `go test ./internal/usage -v`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(wt): usage reports the last launch per model id`.

---

### Task 5: `candidates` ranking package

**Files:**
- Create: `wt/internal/candidates/candidates.go`
- Test: `wt/internal/candidates/candidates_test.go`

**Interfaces:**
- Consumes: `config.Model` (`ID, Family, ProviderID, ModelName, Location, Cost`), `config.ModelCost` (`OutputPricePerMillion *float64, SubscriptionPrice *float64, SubscriptionPeriod string`), `tiers.Index` (`Lookup(provider, name)`), `tiers.Entry` (Task 1 fields).
- Produces:
  - `type Query struct { OpenOnly bool; Providers []string; Near string; PerProvider int }` — `Near` is a registry id; with `Near` set the result is, per provider, the `PerProvider` (default 3) rated models nearest to the near model's score.
  - `type Candidate struct { ID, Provider, Location, Tier, Confidence string; Score float64; HasScore, Rated, OpenWeights bool; Context int; OutputPerMillion *float64; Subscription *float64; SubscriptionPeriod string; Flags []string }`
  - `func Rank(models []config.Model, ix tiers.Index, q Query) []Candidate` — order: open weights first, then score descending (no score last), then output price ascending (no price last), then ID. Models with `HasToolCall && !ToolCall` are excluded. Flags: `"unrated"`, `"low-confidence"`, `"tool_call unknown"`, `"subscription"` (has `SubscriptionPrice`; not comparable per token). Non-model rows (`ID` with prefix `profile/`, `claude/`) and embedding models without an entry are skipped.

- [ ] **Step 1: Write failing tests** with hand-built models and an `Index` from `tiers.Parse` of an inline file: `TestRankOrdersOpenThenScoreThenPrice`; `TestRankExcludesToolCallFalse`; `TestRankFlagsUnknownToolCall`; `TestRankMissingTiersLeavesAllUnratedWithFlag` (empty Index); `TestRankSubscriptionFlag`; `TestRankProvidersFilter`; `TestRankNearPerProvider` (near an ollama model at score 44.8 returns the 3 closest per provider from `ollama` and `openrouter`, each side sorted by distance).
- [ ] **Step 2: Run** `go test ./internal/candidates -v`. Expected: FAIL.
- [ ] **Step 3: Implement** `Rank` as filter → map to `Candidate` → stable sort; `Near` as a second pass grouped by provider.
- [ ] **Step 4: Run tests.** Expected: PASS.
- [ ] **Step 5: Commit** `feat(wt): candidates ranks registry models by tier score and price`.

---

### Task 6: `wt profile slots add|set|rm`

**Files:**
- Create: `wt/cmd/wt/profile_slots_manage.go`
- Modify: `wt/cmd/wt/profile.go:166` (register `slotsCmd(a)` in `c.AddCommand(...)`)
- Test: `wt/cmd/wt/profile_slots_manage_test.go` (use the `runProfileCmd(t, a, args...)` helper in `profile_slots_test.go`)

**Interfaces:**
- Consumes: Tasks 2–3 (`slotedit.Add/Set/Remove/Apply`), `a.cfg.Models` (model ids), `agents.SlotDrivers()`, `profiles.SlotPath()`.
- Produces: `func slotsCmd(a *app) *cobra.Command` (`Use: "slots"`) with:
  - `add <name> --slot fable=<id> --slot opus=<id> ... [--default sonnet] [--env K=V ...] [--comment "text"]` — agent is `claude`; `--default` defaults to `sonnet`; refuses a name outside `^[a-z0-9][a-z0-9-]*$`.
  - `set <name> [--slot s=<id>]... [--default s] [--env K=V]... [--unset-env K]...`
  - `rm <name>` — removes the entry; prints the backup path.
  - Every command prints the backup path (or "created") and then runs the equivalent of `wt profile list` for the file; non-zero exit on any `Apply` refusal with its message.

- [ ] **Step 1: Write failing tests** (scratch `XDG_CONFIG_HOME`, registry models via `a.cfg`): `TestSlotsAddWritesProfile` (file parses, list says `ok`), `TestSlotsAddRejectsUnknownModel`, `TestSlotsAddRejectsBadName`, `TestSlotsSetChangesOneSlot`, `TestSlotsSetUnsetEnv`, `TestSlotsRmRemovesEntryKeepsOthers`, `TestSlotsRmUnknownName` errors, `TestSlotsWriteRefusedLeavesFileUnchanged`.
- [ ] **Step 2: Run** `cd wt && go test ./cmd/wt -run TestSlots -v`. Expected: FAIL.
- [ ] **Step 3: Implement `slotsCmd`**; build `slotedit.Options` with `ModelIDs` from `a.cfg.Models` and `Target` = the name (empty for `rm`).
- [ ] **Step 4: Run** `go test ./cmd/wt -run 'TestSlots|TestProfile' -v`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(wt): wt profile slots add|set|rm`.

---

### Task 7: `wt profile slots usage` and `candidates`

**Files:**
- Modify: `wt/cmd/wt/profile_slots_manage.go` (two more subcommands)
- Test: `wt/cmd/wt/profile_slots_manage_test.go`

**Interfaces:**
- Consumes: Task 4 `LastLaunch`, `usage.StoreImpl.AllCounts`; Task 5 `candidates.Rank`; Task 1 `tiers.Load()`, `tiers.ReadFreshness()`; the slot store (`a.slotChecks` / `loadSlotStore`) for each profile's leading comment (read the file's lines directly: contiguous `#` lines above the `[[profile]]`).
- Produces:
  - `usage [--json]` — per profile in file order: name, launches in 1/7/30 days, last launch (or none), leading comment. JSON: `[{"name","launches_30d","last_launch","comment","slots":{...}}]` with `last_launch` null when none.
  - `candidates [--open-only] [--provider ollama,openrouter] [--near <id>] [--per-provider N] [--json]` — table of `candidates.Candidate`. JSON object: `{"tiers_generated_at": "...", "stale_sources": [...], "candidates": [...]}`; `tiers_generated_at` null when the file is missing. The table prints a one-line `tiers.toml` age and stale-source note first.

- [ ] **Step 1: Write failing tests**: `TestSlotsUsageCountsAndLastLaunch` (usage store seeded via `usage.NewStoreAt(dir)` and `RecordFor`), `TestSlotsUsageNoLaunchesShowsZero`, `TestSlotsUsageIgnoresRenamedProfileHistory`, `TestSlotsUsageIncludesLeadingComment`, `TestSlotsCandidatesJSONIncludesFreshness`, `TestSlotsCandidatesNoTiersFile` (exit 0, `tiers_generated_at` null, every row flagged `unrated`), `TestSlotsCandidatesNearBothProviders`.
- [ ] **Step 2: Run** `go test ./cmd/wt -run 'TestSlotsUsage|TestSlotsCandidates' -v`. Expected: FAIL.
- [ ] **Step 3: Implement** both subcommands.
- [ ] **Step 4: Run** `go test ./cmd/wt -v`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(wt): wt profile slots usage and candidates`.

---

### Task 8: The `manage-slot-profiles` skill

**Files:**
- Create: `.claude/skills/manage-slot-profiles/SKILL.md` (frontmatter style of `.claude/skills/sentinel-failure/SKILL.md`)

**Interfaces:**
- Consumes: the Task 6–7 commands, `uv run --directory modeltiers modeltiers run [--dry-run]` (exit 0 written, 1 not written, 2 written with stale snapshot, 3 sentinel moved → hand off to the `sentinel-failure` skill), `wt smoke <model-id> --tools [--hops N] --json`, `wt profile list`.
- Produces: a skill with `description` that triggers on slot profile creation, cleanup, review of models, or "new models released".

- [ ] **Step 1: Write the skill** with these sections, in order: *When to use*; *1 Freshness* (`wt profile slots candidates --json` → `tiers_generated_at` older than 7 days or any `stale_sources` → offer `modeltiers run`); *2 Cleanup* (`usage --json`; list profiles with `launches_30d == 0` with their comments; remove only what the user confirms, one at a time, via `rm`); *3 Review* (for each profile, `candidates --near <slot model id> --provider ollama,openrouter`; propose swaps with tier gain and price; apply accepted ones with `set`); *4 Create* (walk fable, opus, sonnet, haiku, then `default` incl. `opusplan`, `REMEMBER_MODEL`, `CLAUDE_CODE_MAX_CONTEXT_TOKENS` for a local model Claude Code does not recognize, a dated `--comment`; twin pair `<stem>-ollama` / `<stem>-openrouter`, each side picked by the user from the nearest-scoring models of that provider); *5 Research* (for unrated or low-confidence models the user is considering, web-search release notes and independent benchmarks and label them as supporting evidence); *6 Verify* (offer `wt smoke <id> --tools` per distinct slot model, because a profile launch only exercises `sonnet`; report PASS/FAIL per slot; leave wt's stop prompt to wt); *Slot guidance* (the haiku and local-model rules from spec section 3, with the costs); *Rules* (never edit the file by hand; every write is confirmed; open weights first, price second; say when confidence is low; do not embed model names in docs).
- [ ] **Step 2: Dry-run walkthrough** against a scratch file: set `XDG_CONFIG_HOME` to a temp dir seeded with a copy of `~/.config/agent-wt/slot-profiles.toml`, follow the skill's steps 2–4 once with `wt profile slots`, and record the commands run and `wt profile list` output at the end of the plan's PR description. Expected: every command exits 0 and `wt profile list` shows `ok` for every profile.
- [ ] **Step 3: Commit** `feat: manage-slot-profiles skill`.

---

### Task 9: Documentation

**Files:**
- Modify: `wt/docs/wt-agents/slot-profiles.md` (the "You write it by hand. wt reads it and never writes it" passage and Quick start; add a "Managing the file" section for the five commands)
- Modify: `wt/CLAUDE.md` ("never written by wt" in the Profiles section and the `slot-profiles` mentions), root `CLAUDE.md` (`wt/` architecture line: "Reads, never writes, the hand-written … slot-profiles.toml"; add the skill to the Docs list)
- Modify: `docs/guides/00-config-map.md` (ownership row for `slot-profiles.toml`)
- Modify: `docs/guides/08-maintenance-and-troubleshooting.md` (a state-independent "Keeping slot profiles current" section: the skill name and the commands, no model names)
- Modify: `wt/docs/internals/packages.md` (add `slotedit`, `candidates`)

- [ ] **Step 1: Make the edits** above; hand-editing stays documented as valid.
- [ ] **Step 2: Run** `make check-links lint-shell` from the repo root. Expected: PASS.
- [ ] **Step 3: Run the full gate** `make test-all` and `bin/check-config-dirs-untouched`. Expected: PASS.
- [ ] **Step 4: Commit** `docs: slot profile management commands and skill`.

---

## Self-review

- **Spec coverage:** cleanup (Tasks 4, 7, 8), keep current (1, 5, 7, 8), create/edit (2, 3, 6, 8), twins (5 `--near` per provider, 8), smoke (8), write safety (3), docs (9), tools in `wt` (6–7). The spec's `--slot` flag on `candidates` is dropped: requirements are slot-independent (spec call 2), so the flag would change nothing.
- **Types:** `Patch`, `Options`, `Candidate`, `Freshness` and `LastLaunch` are named identically where Tasks 3, 6, 7 consume them.
- **Proportion:** nine tasks; test lists carry the behavior, bodies are left to the implementer.
