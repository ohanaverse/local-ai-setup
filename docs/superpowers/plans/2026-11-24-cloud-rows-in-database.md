# Cloud rows as LiteLLM database rows Implementation Plan

> **Status: decisions taken 2026-11-24 (see *Decisions taken* below); Tasks 1 to 7 implemented in one PR, Task 8's live check owed.** It lists what is known from the code and the earlier phases, what was measured on the live proxy (Task 0) and the decisions that changed the design.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Adding, changing or removing a cloud model (an OpenRouter or ollama-cloud model, or the keyless `anthropic/` row of a Claude subscription) takes effect through LiteLLM's model API with no config rewrite and no proxy restart (#694), as a local model's start and stop and an alias already do.

**Architecture:** Phase 1 (#684) moved start/stop-driven local rows out of `config.yaml` into LiteLLM's database under the deterministic `model_info.id` `wt:<registry id>` (`litellm.DBID`), stamped with `wt_params_sha` (`ParamsHashKey`), planned by `PlanDB` and applied add-first by `ApplyDB`. Phase 2 (#686) put aliases in the same database under `wta:`. Cloud rows were left in the file because a row in `config.yaml` cannot be edited through the API, so each cloud change still costs one serialized proxy restart (a drain of up to three minutes). This change adds the cloud rows to the database half's desired set, migrates the wt-managed cloud rows out of `config.yaml` the way phase 1 migrated local ones, and leaves `config.yaml` holding settings and any row a person wrote by hand.

**Tech Stack:** Go 1.26.7 (`wt/` module), `internal/litellm` (`dbrow.go`, `dbreconcile.go`, `service_db.go`, `desired.go`), LiteLLM 1.109.1 `/model/new`, `/model/update`, `/model/delete`, `/model/info`.

**Spec:** `docs/superpowers/specs/2026-11-22-litellm-aliases-groups-design.md` (the database-routes approach; the spike evidence it cites). This plan adds no spec; the decisions below are the design questions it leaves open.

## What is known, and what is not

Known from the code and the earlier phases (each is something a task builds on, not something to assume afresh):

- `DBModelFromRow` turns a prepared `config.yaml` model row into the API's shape, and a cloud row is prepared by the same function that prepares a local one (`prepareModel`), so the row's content is already built; only its destination changes.
- A cloud row that `wt` writes to `config.yaml` carries the resolved secret value: `prepareModel`/`BuildEntry` resolves `provider.Auth.SecretRef` via `config.ResolveSecret` (`providerAPIKey`), even when that ref is `os.environ/<NAME>`. An `os.environ/<NAME>` reference survives only in a hand-written row (`DBModelFromRow` copies it verbatim). Phase 2's ruling for alias rows still applies: "cloud keys allowed in alias rows", compared by an HMAC stamp keyed with the proxy key, never by the params themselves (LiteLLM 1.109.1 strips `api_key` from what it lists, adds default params and answers a duplicate `/model/new` with HTTP 500). Decision 1 is therefore about what a migrated database row stores, not about restoring a ref that a wt-written file row already had.
- The subscription hazard check (`SubscriptionHazards`) already reads the proxy's own listing (`/model/info`), which includes database rows, so a keyless `anthropic/` row in the database is seen by it as it is in the file.

Not known; Task 0 measures each on the real proxy with a scratch row and records the result in `docs/superpowers/specs/` beside the earlier spikes:

1. Does LiteLLM resolve an `os.environ/<NAME>` value in a database row's `api_key` at request time, from the proxy's own environment? If it does not, a database cloud row must carry the resolved key, which changes questions 2 and 3 and Decision 1.
2. Where and how is a database row's key kept? LiteLLM encrypts stored credentials with `LITELLM_SALT_KEY`; what does `/model/info` and the spend log show for it? (The plan's Constraint: no secret in a log line, an error string or a test fixture other than a labelled sentinel.)
3. Does a database row survive a proxy restart and a salt-key unchanged, and what does it do when the salt key changes (the plan for a rotate of `LITELLM_SALT_KEY` is already "never change it once credentials are stored")?
4. Does `PATCH` of a row's `api_base` or `model` take effect on the next request, as the spike measured for an alias target?
5. What does a `config.yaml` row and a database row with the same `model_name` do (a silent two-deployment group, which phase 1 refused to create)? This decides the order of the migration's two writes.

## Global Constraints

- A database cloud row carries `model_info.wt_managed: true` and the deterministic id `wt:<registry id>`, as a local row does; wt touches no row without that marker.
- No secret reaches a log line, an error string, a printed plan or a test fixture other than a labelled sentinel. A dry run names a row by id, target, order and `h8`, never its params (`alias set --dry-run`'s rule).
- `config.yaml` rows a person wrote by hand are never touched, moved or duplicated; only rows wt wrote (the marker phase 1 reads) migrate.
- Fallback is correct, not silent: when the database half cannot run (database models off, the proxy not answering, no proxy pair), cloud routes keep their file-plus-restart path and one warning says why. This is the one place the alias rule ("no file fallback") cannot apply, because dropping the file path would leave a cloud model unrouted.
- A migration never leaves a window where a model is unrouted, and never restarts the proxy more than once.
- Tests stay off the machine: the admin client against `httptest`, per-call seams, no new package variable; `wt/CLAUDE.md` "Go tests".
- Docs that quote changed behavior move in the same change: `wt/docs/internals/litellm-routes.md`, `wt/docs/wt-model-server.md`, `docs/guides/04-litellm-config.md`, `docs/guides/14-litellm-install-and-configuration.md`, and the `STORE` column's description in `wt/docs/wt-model.md`.
- Implementers never read the LiteLLM LaunchAgent plist or print the proxy key (a subagent once did).
- The PR body says `Refs #694`; the issue stays open until the owner closes it.

## Decisions for the owner

Each has a recommendation; none is made.

1. **What a database row's `api_key` holds.** *Recommend the `os.environ/<NAME>` reference, if Task 0 shows LiteLLM resolves it for database rows.* Then the secret never enters the database or the stamp's input, and rotating a provider key is still an edit of the proxy's environment plus one restart. The alternative, a resolved key in the row, is simpler to reason about for rows that have no env var but puts a provider key in LiteLLM's database; if Task 0 shows the reference does not resolve, this decision is forced and the plan must say how a keyless `secret_ref` is handled.
2. **Which cloud rows move.** *Recommend all wt-written cloud rows, including the keyless `anthropic/` rows of `claude-subscription`.* Moving only some keeps a restart path alive for the rest, and the subscription row is the one a user adds most often. The risk is the hazard gate: while it is blocked, a subscription row is not routed and not added in either store, and an existing database row is deleted (`off`, as `subscriptionUnrouted` does for model rows and `aliasSubBlocked` does for alias targets); while the gate could not look, the row is kept untouched and none is added (`hold` / `aliasSubHold`). No frozen-but-present row is left behind.
3. **The fallback when the database half cannot run.** *Recommend a per-write fallback to today's file path with one warning* (Global Constraints), not a mode switch: a proxy with `store_model_in_db` off, or one that is briefly down, must still get its cloud models routed.
4. **The migration's trigger.** *Recommend the same first-sync migration as phase 1* (database wins during a sync, file wins outside one), not a separate command: an owner who upgrades wt should find the rows moved after the next `wt-model litellm sync` or serve start, with the restart that removes them from the file counted once.
5. **Whether `wt-model litellm list` keeps showing cloud rows as `STORE file`.** *Recommend it shows `db` for a migrated row and `file` for a hand-written one,* so the column keeps answering "what would a restart touch".

## Decisions taken

The owner took every recommendation on 2026-11-24:

1. **A database row's `api_key` holds the resolved key**, not an `os.environ/` reference. The spike showed a reference resolves in a database row but needs each provider key in the proxy's own environment and a restart per change; the resolved key keeps today's semantics, is stored encrypted by LiteLLM (`LITELLM_SALT_KEY`) and enters the stamp only as an HMAC.
2. **All wt-written cloud rows move**, the keyless `anthropic/` subscription rows included.
3. **Per-write fallback to the file path**, with one warning naming the reason, when the database half cannot carry a row: `store_model_in_db` off, no proxy pair, an add that is refused or not listed afterwards.
   - *Refinement made while building it:* a **listing that fails** is not that case. A migrated cloud row has no file row, so the fallback would write every cloud row to `config.yaml` beside database rows that may exist, with a restart, and the next healthy sync would move them back with another. `holdCloud` therefore writes nothing for a cloud add whose store is unknown and says `not routed until the database answers: <ids>`; the sync after the proxy answers adds what is missing. Local rows keep the file fallback (a started model must route).
4. **The migration runs on the first sync**, as in phase 1: the database wins during a sync, the file outside one; add the row, confirm by listing, remove the file row, one restart.
5. **`STORE` is `db` for a migrated row and `file` for a hand-written one.** No code was needed: `litellm list` already labelled every `wt:` database row `db`.

The salt-key refusal the spike asked for (question 3b) is in: a failed add with no wt row listed says `LITELLM_SALT_KEY` is the likely cause (`saltHint`), and `dropTwins` deletes only what a fresh listing shows.

Found while building it: the launch-time ensure (`AddMissingOnly`) would write a **file twin** of a migrated cloud row (it checks only `config.yaml`) and restart the proxy for it, so it asks the database first (`dbRoutes.holds`); and a batch whose one add fails left the database copy of a sibling add that fell back to the file (`dropMigratingTwins`, now `dropTwins`, deletes every such copy).

## Review Focus

1. **A model unrouted between the two writes of a migration.** The database row must be added and confirmed (the confirming re-list phase 2 uses) before the file row is removed, and the removal's restart must happen once, after every row moved.
2. **A secret in the wrong place.** The stamp, the dry run, `litellm list --json`, the spend log, an error from a failed `/model/new` and a test failure message are each a place a provider key could appear; each gets a sentinel test.
3. **A subscription row left live under a hazard.** A blocked gate must delete the database row, never freeze it (phase 3's rule for alias rows), and a gate that could not look must keep what exists and add nothing.
4. **Two writers.** The serve start sync and the CLI `litellm sync` reconcile at once: the same id added twice must converge (`addConverged`'s rule), and the file removal must not race a second migration.
5. **A registry rename or removal.** A model deleted from the registry must lose its database row and its stamp, and a provider whose `secret_ref` changed must update the row (the hash differs), without orphaning the old one.

## File Structure

- `wt/internal/litellm/desired.go`, `service_db.go`, `dbreconcile.go`: the database half's desired set gains the cloud rows; `PlanDB` already plans by id and hash.
- `wt/internal/litellm/service.go`: the file half stops writing a wt-managed cloud row once the database half owns it (the migration's second write), and keeps writing it on fallback.
- `wt/internal/litellm/report.go`, `cmd/wt-model/litellm.go`: `STORE` and the dry run's plan.
- `docs/contracts/` fixtures for the list and sync JSON if their shape gains a value.
- Tests beside each of the above; an `httptest` fake of the model API that the phase 1 and 2 tests already use.

---

### Task 0: The live spike (before any code)

Run on the real proxy with one scratch model name and a labelled sentinel key, removing everything after, and record the answers to the five open questions above in a new file under `docs/superpowers/specs/2026-11-24-cloud-rows-in-database-spike/`. Nothing else in this plan starts before it, because Decision 1 depends on question 1 and the migration's order on question 5.

- [ ] **Step 1:** Add a database row whose `api_key` is `os.environ/<NAME>` for a variable the proxy does and does not have; send one request each; record what LiteLLM does.
- [ ] **Step 2:** List the rows and read one spend-log entry; record whether the key, the reference or neither appears.
- [ ] **Step 3:** Restart the proxy through `bootout` and `bootstrap` (never `kickstart -k`, which keeps the old definition); record whether the row survives and answers.
- [ ] **Step 4:** Add a `config.yaml` row and a database row under one `model_name`; record the grouping and which answers.
- [ ] **Step 5:** `PATCH` a row's `api_base`; record the time to take effect and whether a stream already running finishes.

### Task 1: Cloud rows join the database half's desired set

**Files:** `wt/internal/litellm/desired.go`, `dbrow.go`, tests.

- [ ] **Step 1: Write failing tests:** a registry with a cloud model produces one desired database row under `wt:<id>` with `wt_managed` and the stamped hash; the row's params carry the reference (or the resolved key, per Decision 1) and the stamp is stable across runs and changes with the key; a keyless subscription row is built only while the gate is clean.
- [ ] **Step 2:** Run them; expect FAIL.
- [ ] **Step 3:** Build the rows with `prepareModel` and `DBModelFromRow`, stamp with `routeHash`, mark managed.
- [ ] **Step 4:** Run `go test ./internal/litellm -run 'TestCloudRow|TestDesired' -v`; expect PASS.
- [ ] **Step 5:** Commit.

### Task 2: The database reconcile plans and applies them

**Files:** `wt/internal/litellm/service_db.go`, `dbreconcile.go`, tests.

- [ ] **Step 1: Write failing tests:** adding a cloud model to the registry adds one database row and writes no `config.yaml` and calls no restart; changing its `model_name` updates the row; removing it deletes the row; an unmarked `config.yaml` row with the same name is left alone and no twin is added (phase 1's Review Focus 2).
- [ ] **Step 2:** Run them; expect FAIL.
- [ ] **Step 3:** Include the cloud rows in the plan `PlanDB` is given; keep add-first and the confirming re-list.
- [ ] **Step 4:** Run; expect PASS. **Step 5:** Commit.

### Task 3: Migration of the wt-written cloud rows out of `config.yaml`

> Migration note: today's migration predicate (`RowFamily(cfg, id) != ""`) excludes every cloud id, because `RowFamily` is `""` for cloud rows, so the pending add is left in `config.yaml` and any stray database row is deleted as a twin. This task must change that gate (and the standalone `ApplyChange`/`touchesLocal` notion that cloud rows never call the database) so a cloud row whose `config.yaml` entry is wt-written is migratable on the same add-first/confirm-then-remove ordering. Name the widened predicate in the plan and test a cloud id end to end.

**Files:** `wt/internal/litellm/service.go`, `service_db.go`, tests.

- [ ] **Step 1: Write failing tests (Review Focus 1):** a `config.yaml` holding a wt-written cloud row and a hand-written one, with the database empty: the first sync adds the database row, confirms it by listing, then removes only the wt-written file row, and calls the restart exactly once; the model answers at every step (the fake records the order); a second sync does nothing.
- [ ] **Step 2:** Run them; expect FAIL.
- [ ] **Step 3:** Implement the two-write migration with the order Task 0's question 5 settled; hand the one restart to the existing bounce.
- [ ] **Step 4:** Run; expect PASS. **Step 5:** Commit.

### Task 4: The fallback and the warnings

**Files:** `wt/internal/litellm/service.go`, `report.go`, tests.

- [ ] **Step 1: Write failing tests:** with database models off, the proxy not answering, or no proxy pair, a cloud change takes today's file-plus-restart path and prints one warning naming the reason, and a model is never left unrouted; a database that comes back later migrates the rows on the next sync.
- [ ] **Step 2:** Run them; expect FAIL. **Step 3:** Implement. **Step 4:** Run; expect PASS. **Step 5:** Commit.

### Task 5: The subscription gate over database rows

**Files:** `wt/internal/litellm/subscription_hazards.go`, `service_db.go`, tests.

- [ ] **Step 1: Write failing tests (Review Focus 3):** under a seen hazard the subscription database row is deleted and none is added; under a gate that could not look the row is kept and none is added; a clean gate adds it. The hazard check's own tests stay green with the row in the database.
- [ ] **Step 2:** Run them; expect FAIL. **Step 3:** Route the subscription rows through the same three-way outcome the rest of wt uses (`subscriptionUnrouted` for model rows; `aliasState` for alias targets only): `off` deletes and adds none, `hold` keeps and adds none, clean adds it. **Step 4:** Run; expect PASS. **Step 5:** Commit.

### Task 6: Secrets stay out of every output

**Files:** tests across `internal/litellm` and `cmd/wt-model`.

- [ ] **Step 1: Write failing tests (Review Focus 2):** with a sentinel provider key, none of the dry run, `litellm sync` output, `litellm list --json`, an `/model/new` failure message or the stamp input contains it.
- [ ] **Step 2:** Run them; expect FAIL where an output leaks. **Step 3:** Fix each leak at its source. **Step 4:** Run; expect PASS. **Step 5:** Commit.

### Task 7: Listing, reports and docs

**Files:** `wt/internal/litellm/report.go`, `cmd/wt-model/litellm.go`, the docs named in Global Constraints, contract fixtures.

- [ ] **Step 1:** `STORE` shows `db` for a migrated cloud row and `file` for a hand-written one (Decision 5), with a test pinning both.
- [ ] **Step 2:** Update the docs and any fixture in the same commit; `make check-links` passes.
- [ ] **Step 3:** Commit.

### Task 8: Live check and the pull request

- [ ] **Step 1:** On the real proxy with a scratch cloud model (never a real key in a command line): add it, change it, remove it; record that no restart ran, that a running request finished, and that the first sync after upgrade moved the wt-written rows with one restart.
- [ ] **Step 2:** `make pre-pr` green, run in the background with its exit read from the log.
- [ ] **Step 3:** Open the PR with `Refs #694`, the live-check result commented on it, and Decisions 1 to 5 stated as taken.

## Not in scope

- Moving settings (`litellm_settings`, `general_settings`) out of `config.yaml`: they have no database form.
- Rotating `LITELLM_SALT_KEY` or `LITELLM_MASTER_KEY`: unchanged (`wt-model setup`'s rotate keeps the salt key).
- A Redis-coordinated multi-proxy deployment: the live proxy is one process, as in phase 1.
- Alias rename, the Models-tab alias screen and multi-target aliases (#687, #688, #690): separate.
