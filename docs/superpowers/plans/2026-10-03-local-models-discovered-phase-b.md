# Local models are discovered — Phase B Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make local models discovered rather than configured: wt lists exactly what its live inventory finds (on disk, or running), routes every discovered model under its discovered id with no registry entry and no ready gate, and modelman stops auto-registering a model it starts.

**Architecture:** `catalog.Build` builds local rows only from `localmodels.Inventory` entries, with a registry entry as an optional overlay matched by family + `model_name`; a pin of a hidden registry id still explains itself through `catalog.MissingReason`. `internal/litellm` gains `DiscoveredModel`, `RowFamily` and one targeted write API, `ApplyChange(Change{Add, Remove, RemoveFamilies})`, which the lifecycle hooks use for marker-based family removal; `Sync` takes the local models as `[]config.Model` (registry overlays or discovered models) and freezes untrusted families with `Options.UntouchedFamilies`. A discovered route never replaces a hand-written row, and wt stops reading modelman's per-model state altogether.

**Tech Stack:** Go 1.26.7 (wt: cobra, yaml.v3, BurntSushi/toml), Python 3.13 (modelman: Typer, pytest, uv, ruff, mypy).

**Spec:** `docs/superpowers/specs/2026-10-02-configured-is-exposed-design.md` (Phase B sections B1–B4).

## Global Constraints

- Hand-written `config.yaml` rows are never removed or rewritten: a row is wt's only when it carries `model_info.wt_managed: true` or is named like a managed **registry** id; a discovered route never replaces an unmarked row of the same name (`ollama/q8`, `ollama/o35`, `ollama/llama3.2:3b` and the four `openrouter/qwen/*` rows on the reference host must survive every task).
- One proxy restart per change, and only when `config.yaml` changed (existing `applyPlanned`/`applyAndReport` behaviour; a forced bounce only when an earlier deferred write owes one).
- Registry ids are never migrated: a matched overlay keeps its registry id (usage, survey, rotation and profile keys stay valid); an unmatched model uses `config.DiscoveredModelID(family, artifact)`.
- A flaky probe never hides or removes a model: `ArtifactKnown == false` lists the row as `unknown`; a family whose probe is neither OK nor refused is in `Options.UntouchedFamilies`.
- Every Go `Test*` has a top-level `//` comment stating what it tests and why (wt/CLAUDE.md).
- modelman: `make check` (ruff, ruff format, mypy) passes before a task is called done; `ruff` B905 (`zip(strict=)`) is enforced.
- Each PR leaves `make test-all` (repo root) green.
- No live model state in `docs/guides/` (root CLAUDE.md rule): show the command that reveals state, label captured output as illustrative.
- Commit messages end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Subagent model selection: dispatch implementers with no `model` override (named aliases are not routable on this account).
- Plan notation: inside an inline-quoted passage to find or replace, `\n` marks a line break in the source file; fenced blocks are verbatim.

## Review Focus

1. **A hand-written row named exactly like a discovered id** (`ollama/llama3.2:3b` on the reference host) must survive both a sync and a start of that model — never added over, adopted or rewritten. Pinned in Task 7 (`TestApplyChangeNeverReplacesHandWrittenDiscoveredName`) and Task 9 (`TestSyncLeavesHandWrittenDiscoveredName`).
2. **A flaky probe must not hide a pulled model**: an unreachable ollama (`ArtifactKnown` false) lists its registry models as `unknown` and startable, never drops them. Pinned in Task 2 (`TestBuildUnknownLocalStatus`).
3. **Pinning a registry id that is not on disk** (`wt -M`, `wt start`, `wt smoke`, the TUI pin) must still say `<id> is not on disk — pull or download it first`, not "not in the eligible list", now that the row is gone. Pinned in Task 1 (`TestMissingReason`) and, after Task 2 hides the row, by the existing `TestResolveModelPinOnAbsentLocalReportsReason`, `TestStartInvalidArgErrors`, `TestResolveSmokeModelPinnedBlockedNamesReason` and `TestPinOnAbsentLocalRoutesBack`, which Task 2 must keep green unchanged.
4. **An untrusted family's discovered routes** (probe partial/unreachable/not probed) must be neither removed nor added by sync, while a registry cloud model on that family's provider is still routed. Pinned in Task 9 (`TestSyncSkipsUntouchedFamilies`, `TestUntrustedFamilies`).
5. **A model started without a registry entry must stay "running" in modelman**: its flag lives under its discovered id, and `running_model_ids()` used to skip every id with no registry entry — so the TUI's mount reconcile would clear a live model's flag (and a replace would never find it as the slot's occupant). Pinned in Task 12 (`test_running_model_ids_verifies_a_discovered_model_by_probing_it`, `test_same_provider_occupant_sees_a_flagged_discovered_model`, `test_stop_all_stops_a_running_discovered_model`). (The omlx/omlx-6bit spelling split stays pinned by Task 5's `TestDiscoveredModelAndRowFamily` and Task 8's `TestRouteAfterStartOmlx6bitClearsOmlxFamily` / `TestStopRemovesDiscoveredFamilyRoutesKeepsHandWritten`.)

## Design notes

Decisions D1–D4 are implemented as given; these entries record refinements.

- **`RowFamily` of a registry cloud model is `""` (refines D2.3).** D2.3 says "a registry model → `Family(m.ProviderID)`". Read literally, the registry cloud model `ollama/glm:cloud` (cloud, on the local ollama provider) would be an `ollama`-family row: an untrusted ollama probe would freeze its route (D2.6), and — worse — D2.7 would put it in the Recheck `local` set, so every sync whose Recheck ran would prune its add (`stoppedAgain`). `RowFamily` therefore answers by provider only when the model's location resolves local. A *deleted* cloud model's marked row (`ollama/x:cloud`, no registry entry) still classifies by prefix, which is D2.6's accepted cost, documented in `planSync`'s comment.
- **`catalog.MissingReason(snap, id)` takes no `cfg` (refines D1.3).** Everything it needs is in the snapshot's registered entries. It also keeps today's `modelman start <id>` wording for a non-running mlx_lm_server pairing, which D1.1 now hides, and it is wired into the TUI's `-M` route-back (`internal/tui/app.go`) as well as the three named paths, because `wt -M <id>` without `-A` goes through the TUI.
- **`Apply` stays** (D2.3 option) as the id-based convenience over the same write path: it still reports `not found in registry` per unknown id and the existing `TestApply*` tests keep exercising the shared `applyPlanned` mechanics. No production caller remains after Task 8.
- **`untrustedFamilies(snap)` is a sibling of `syncUntouchedAndWarnings` (refines D2.6's "fills it").** Keeping it separate leaves the four existing `syncUntouchedAndWarnings` call sites and their assertions untouched. **Controller ruling:** a frozen family with no registry local model but a marked row in `config.yaml` gets the existing `provider %q probe did not succeed (status %q); its model routes were left unchanged` warning; `syncUntouchedAndWarnings` adds it (so dry run and real sync report it identically), and `routedIDSet` now maps each id to whether a row of that name is marked (the refused-daemon check keeps using mere presence).
- **Default inventory stubs list every registry local model as on disk.** cmd/wt's and internal/tui's `TestMain`s used to return an empty snapshot; with B1 that would hide every local row in ~40 tests. Both now default to `onDiskSnapshot(cfg)` (every registry local model on disk, not running, family probe OK) — the state those tests always assumed. Tests that need another state still call `stubProbeInventory`/`stubInventory`. (PR 1's review then replaced the two copies with one shared `localmodels.OnDiskSnapshotForTest`, in `wt/internal/localmodels/fortest.go`; later tasks use that name.)
- **Controller ruling — a sole running discovered model still auto-resolves.** When the only launchable row is a running discovered model, a launch with no `-M` resolves to it (existing direct-mode behaviour, now also under LiteLLM because the refusal is gone): the spec restricts rotation, not the single-launchable-row shortcut. Pinned in Task 11 (`TestResolveModelSoleRunningDiscoveredAutoResolves`). Rotation itself walks `cfg.Models`, so it never lands on a discovered row (Tasks 4 and 11).
- **modelman keeps the running flag under the discovered id, and every flag consumer understands it.** `start_local_model` drives the provider from an unsaved `ModelEntry` (`_discovered_entry`) whose id is `<provider>/<native name>` — wt's discovered id — and writes `running = true` under it. Nothing is written to `registry.toml` and no `ready` flag is set. The consumers of `state.models` running flags were audited: `stop_local_model` (parses the id prefix) and `stop_all_local_models` (iterates flags) already handle such an id; `running_model_ids()` (and through it the TUI mount reconcile's `stale_ids`) skipped ids with no registry entry, so it now probes them through `_flagged_target` (the provider + native name the discovered id spells, provided that provider is in the registry); `same_provider_occupant()` only searched registry ids, so it now also counts flagged non-registry ids on the slot's provider(s). The TUI table's RUNNING column and `modelman start`'s no-arg listing iterate registry entries only — a discovered model has no row there by design (spec non-goal). The `family`/`registry_path` parameters and `DiscoveredModelNeedsFamily` are removed with the prompt.
- **Capability flags (D2.10)**: discovered rows carry no `supports_function_calling`/`supports_vision`. No code in this plan; acceptance B-2 (Task 15) decides. If an agent needs a flag, the follow-up is per-provider defaults in `PolicyFor` (spec B2), not mandatory overlays.

## File Structure

**PR 1 — wt: discovery-driven catalog (`feat/179-b1-catalog`)**
- `wt/internal/catalog/catalog.go` — `MissingReason`; `Build` lists local rows only from inventory (`listed`); delete `StatusAbsent` and its block branch/wording.
- `wt/internal/catalog/catalog_test.go` — rewritten presence tests, real-inventory overlay test, `TestMissingReason`.
- `wt/internal/catalog/registry_fixture_test.go` (new) — the contract fixture's overlay rows against a real inventory.
- `wt/cmd/wt/resolve.go`, `wt/cmd/wt/model_cmds.go`, `wt/cmd/wt/smoke.go`, `wt/internal/tui/app.go` — consult `MissingReason` when `catalog.Find` misses; `localRowsSnap`.
- `wt/cmd/wt/testmain_test.go`, `wt/internal/tui/testhelpers_test.go` — `onDiskSnapshot` default stub.
- `wt/cmd/wt/model_cmds_test.go`, `wt/cmd/wt/resolve_test.go`, `wt/internal/tui/{modeltable_test,pick_model_test,start_flow_test,table_refresh_test}.go` — fixtures that relied on absent rows or empty snapshots.
- `wt/internal/rotation/rotation_test.go` — rotation never picks a discovered model.
- `docs/contracts/registry.sample.toml` — mtplx provider, a `--`-style overlay, an overlay not on disk.
- `wt/internal/config/registry_fixture_test.go`, `modelman/tests/contracts/test_registry_fixture.py` — fixture counts and new rows.

**PR 2 — wt: discovered routing (`feat/179-b2-routing`)**
- `wt/internal/litellm/service.go` — `lookup`/`prepareModel` (no ready gate), `DiscoveredModel`, `RowFamily`, `isRegistryID`, `Change`/`ApplyChange`, `Options.UntouchedFamilies`, `Sync`/`PlanSync`/`planSync` over `[]config.Model`.
- `wt/internal/litellm/configfile.go` — `File.hasUnmarkedRow`.
- `wt/internal/litellm/policy.go` — `Policy.Cloud` comment.
- `wt/internal/litellm/discovered_test.go` (new) — discovered-routing tests; `wt/internal/litellm/service_test.go` — gate tests, `localFor`, `[]config.Model` call sites.
- `wt/internal/config/{config.go,modelman.go,registry.go}` — delete `ReadyFlag`, `SetReadyForTest`, `ReadyAllForTest`, `ModelmanEntry`, the per-model reader.
- `wt/internal/config/{modelman_test.go,modelman_fixture_test.go,config_test.go}`; delete `wt/internal/config/ready_test.go`; drop the `ReadyAllForTest()` lines from 13 test files and `SetReadyForTest` from `internal/tui/agent_model_test.go`.
- `docs/contracts/modelman.sample.toml` — comments: wt reads no per-model key.
- `wt/internal/lifecycle/routes.go` (+ `routes_test.go`, `wrappers_test.go`, `mtplx_test.go`) — `ApplyChange` seam, discovered start route, family removal; delete `familyModelIDs`.
- `wt/internal/localmodels/inventory.go` — `Families()`.
- `wt/cmd/wt/litellm.go` (+ `litellm_test.go`) — `desiredLocalEntries`/`desiredLocalModels`, `untrustedFamilies`.
- `docs/contracts/litellm-cli.sample.json`, `modelman/tests/contracts/test_litellm_cli_fixture.py` — a discovered route in `sync`/`sync_dry_run`.
- `wt/internal/catalog/catalog.go`, `wt/cmd/wt/resolve.go`, `wt/internal/smoke/smoke.go`, `wt/internal/tui/modeltable.go` (+ their tests) — drop the discovered "not in LiteLLM" refusal.

**PR 3 — modelman (`feat/179-b3-modelman`)**
- `modelman/src/modelman/local_control.py` — `_discovered_entry`, `_resolve_local_model`; delete `_register_discovered_model`, `_resolve_or_register`, `DiscoveredModelNeedsFamily`, the `family`/`registry_path` parameters.
- `modelman/src/modelman/main.py` — `start` without the family prompt; inventory wording.
- `modelman/src/modelman/screens/models.py`, `modelman/src/modelman/screens/forms.py` — comments that named the deleted function.
- `modelman/tests/test_local_control.py`, `modelman/tests/commands/test_local_control.py`.

**PR 4 — docs (`docs/179-b4-guides`)**
- `docs/guides/02-providers-and-models.md`, `03-model-families.md`, `06-wt-agents-and-models.md`, `08-maintenance-and-troubleshooting.md`.
- `wt/CLAUDE.md`, `modelman/CLAUDE.md`.
- `wt/docs/wt-start-stop.md`, `wt/docs/wt-smoke.md`, `wt/docs/wt-agents/README.md`.

---

# PR 1 — wt: discovery-driven catalog and overlay matching

Branch: `feat/179-b1-catalog` from `main`.

### Task 1: `catalog.MissingReason` — explain a pinned registry id that has no row

**Files:**
- Modify: `wt/internal/catalog/catalog.go` (add `MissingReason` directly above `Find`)
- Modify: `wt/cmd/wt/resolve.go` (`resolveModel` pin branch, ~line 79; `errors` import)
- Modify: `wt/cmd/wt/model_cmds.go` (`localRows` ~line 228, `runStart` ~line 240)
- Modify: `wt/cmd/wt/smoke.go` (`resolveSmokeModel` ~line 256)
- Modify: `wt/internal/tui/app.go` (`enterModelPhase` pin branch ~line 872; `catalog` import)
- Test: `wt/internal/catalog/catalog_test.go`

**Interfaces:**
- Consumes: `localmodels.Snapshot`, `localmodels.Entry` (`Registered`, `ModelID`, `Running`, `ArtifactKnown`, `Artifact`, `ProviderID`), `localmodels.Family`.
- Produces: `func MissingReason(snap *localmodels.Snapshot, id string) string` (package `catalog`); `func localRowsSnap(cfg *config.Config) ([]catalog.Row, localmodels.Snapshot)` (package `main`).

Today the absent row itself carries the "not on disk" reason. Task 2 removes that row, so this task first gives every pin path a second source for the message, consulted only when `catalog.Find` misses. Before Task 2 nothing changes for users (the absent row is still found first).

- [ ] **Step 1: Write the failing test**

Append to `wt/internal/catalog/catalog_test.go`:

```go
// TestMissingReason verifies the message a pin of a hidden registry model
// gets (#179 Phase B): an overlay the probe confirmed is not on disk says so,
// a non-running mlx_lm_server pairing names `modelman start`, and everything
// that has a row — or that the probe could not vouch for — gets "" so the
// caller falls back to its generic wording. Without it, `wt -M <absent-id>`
// would say "not in the eligible list" and send the user hunting for a
// filter or agent problem instead of a download.
func TestMissingReason(t *testing.T) {
	snap := &localmodels.Snapshot{Entries: []localmodels.Entry{
		{ProviderID: "omlx", ModelID: "omlx/gone", Registered: true, ArtifactKnown: true},
		{ProviderID: "omlx", ModelID: "omlx/here", Artifact: "here", Registered: true, ArtifactKnown: true},
		{ProviderID: "ollama", ModelID: "ollama/unknown", Registered: true},
		{ProviderID: "mlx_lm_server", ModelID: "mlx_lm_server/p", Registered: true},
		{ProviderID: "omlx", ModelID: "omlx/disc", Artifact: "disc", ArtifactKnown: true},
	}}
	cases := map[string]string{
		"omlx/gone":       "omlx/gone is not on disk — pull or download it first",
		"mlx_lm_server/p": "local model \"mlx_lm_server/p\" is not running — start it with `modelman start mlx_lm_server/p`",
		"omlx/here":       "",
		"ollama/unknown":  "",
		"omlx/disc":       "",
		"nope/x":          "",
	}
	for id, want := range cases {
		if got := MissingReason(snap, id); got != want {
			t.Errorf("MissingReason(%s) = %q, want %q", id, got, want)
		}
	}
	if got := MissingReason(nil, "omlx/gone"); got != "" {
		t.Errorf("MissingReason(nil snapshot) = %q, want empty (no probe ran)", got)
	}
}
```

- [ ] **Step 2: Run it — expect a build failure**

Run: `cd wt && go test ./internal/catalog -run TestMissingReason`
Expected: FAIL — `undefined: MissingReason`.

- [ ] **Step 3: Implement `MissingReason`**

In `wt/internal/catalog/catalog.go`, insert directly above `// Find returns the row for id`:

```go
// MissingReason is the message for a pin of a registry local model that has
// no row (#179 Phase B: local rows come only from the inventory): "<id> is
// not on disk" when the probe confirmed its artifact is missing, or the
// `modelman start` hint for a non-running mlx_lm_server pairing, which wt can
// neither discover nor start. It is "" for any id the snapshot cannot vouch
// for — a model with a row, an unknown artifact, a discovered or unknown id,
// or a nil snapshot (no probe ran) — so the caller keeps its own wording.
// `wt -M` (both paths), `wt start` and `wt smoke` consult it only when
// catalog.Find misses.
func MissingReason(snap *localmodels.Snapshot, id string) string {
	if snap == nil {
		return ""
	}
	for _, e := range snap.Entries {
		if !e.Registered || e.ModelID != id || e.Running {
			continue
		}
		switch {
		case localmodels.Family(e.ProviderID) == "mlx_lm_server":
			return fmt.Sprintf("local model %q is not running — start it with `modelman start %s`", id, id)
		case e.ArtifactKnown && e.Artifact == "":
			return fmt.Sprintf("%s is not on disk — pull or download it first", id)
		}
	}
	return ""
}
```

- [ ] **Step 4: Run it — expect PASS**

Run: `cd wt && go test ./internal/catalog -run TestMissingReason`
Expected: PASS.

- [ ] **Step 5: Consult it on every pin path**

`wt/cmd/wt/resolve.go` — add `"errors"` to the import block (above `"fmt"`), and in `resolveModel` replace

```go
		row, ok := catalog.Find(rows, pinned)
		if !ok {
			return config.Model{}, launchable, fmt.Errorf("model %q is not in the eligible list for agent %q", pinned, agent)
		}
```

with

```go
		row, ok := catalog.Find(rows, pinned)
		if !ok {
			if reason := catalog.MissingReason(&snap, pinned); reason != "" {
				return config.Model{}, launchable, errors.New(reason)
			}
			return config.Model{}, launchable, fmt.Errorf("model %q is not in the eligible list for agent %q", pinned, agent)
		}
```

`wt/cmd/wt/model_cmds.go` — replace the whole `localRows` function with:

```go
// localRows builds catalog rows for every configured and detected local model
// from one live inventory snapshot — screen 1's row set.
func localRows(cfg *config.Config) []catalog.Row {
	rows, _ := localRowsSnap(cfg)
	return rows
}

// localRowsSnap is localRows plus the snapshot the rows were built from, for
// callers that must explain an id with no row (catalog.MissingReason).
func localRowsSnap(cfg *config.Config) ([]catalog.Row, localmodels.Snapshot) {
	snap := probeInventory(cfg)
	var models []config.Model
	for _, m := range cfg.Models {
		if loc, err := cfg.ResolveLocation(m); err == nil && loc == config.LocationLocal {
			models = append(models, m)
		}
	}
	return catalog.Build(catalog.Input{Config: cfg, Models: models, Inventory: &snap}), snap
}
```

In `runStart`, change `rows := localRows(cfg)` to `rows, snap := localRowsSnap(cfg)`, and replace

```go
	row, ok := catalog.Find(rows, id)
	if !ok {
		if config.IndexModelByID(cfg.Models, id) >= 0 {
```

with

```go
	row, ok := catalog.Find(rows, id)
	if !ok {
		if reason := catalog.MissingReason(&snap, id); reason != "" {
			return errors.New(reason)
		}
		if config.IndexModelByID(cfg.Models, id) >= 0 {
```

`wt/cmd/wt/smoke.go` — in `resolveSmokeModel` replace

```go
		// A blocked local row (not on disk, no start backend) is not a candidate,
		// so name its real reason as `wt start` does rather than guessing.
		if row, ok := catalog.Find(localRows(cfg), modelID); ok && row.Action() == catalog.ActionBlock {
			return smokeTarget{}, errors.New(row.BlockReason())
		}
```

with

```go
		// A registry local model with no row (not on disk, or a stopped
		// mlx_lm_server pairing) or a blocked local row is not a candidate, so
		// name its real reason as `wt start` does rather than guessing.
		rows, snap := localRowsSnap(cfg)
		if reason := catalog.MissingReason(&snap, modelID); reason != "" {
			return smokeTarget{}, errors.New(reason)
		}
		if row, ok := catalog.Find(rows, modelID); ok && row.Action() == catalog.ActionBlock {
			return smokeTarget{}, errors.New(row.BlockReason())
		}
```

`wt/internal/tui/app.go` — add `"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"` to the import block (after the `agents` import), and in `enterModelPhase` replace

```go
		idx, ok := idIndex[m.pinnedModel]
		if !ok {
			return routeBack(fmt.Sprintf("model %q is not in the eligible list for agent %q", m.pinnedModel, agent))
		}
```

with

```go
		idx, ok := idIndex[m.pinnedModel]
		if !ok {
			if reason := catalog.MissingReason(snap, m.pinnedModel); reason != "" {
				return routeBack(reason)
			}
			return routeBack(fmt.Sprintf("model %q is not in the eligible list for agent %q", m.pinnedModel, agent))
		}
```

(`snap` is the `*localmodels.Snapshot` `enterModelPhase` already built for the table.)

- [ ] **Step 6: Run the affected packages — expect PASS**

Run: `cd wt && go build ./... && go vet ./... && go test ./internal/catalog ./cmd/wt ./internal/tui`
Expected: PASS. The existing pin tests (`TestResolveModelPinOnAbsentLocalReportsReason`, `TestStartInvalidArgErrors`, `TestResolveSmokeModelPinnedBlockedNamesReason`, `TestPinOnAbsentLocalRoutesBack`) still pass through the absent row; Task 2 makes them depend on this code (Review Focus 3).

- [ ] **Step 7: Commit**

```bash
git add wt/internal/catalog/catalog.go wt/internal/catalog/catalog_test.go wt/cmd/wt/resolve.go wt/cmd/wt/model_cmds.go wt/cmd/wt/smoke.go wt/internal/tui/app.go
git commit -m "feat(wt): explain a pinned local model with no catalog row (#179 phase B)

catalog.MissingReason names why a registry local model has no row — not
on disk, or a stopped mlx_lm_server pairing — and wt -M (both paths),
wt start and wt smoke consult it when catalog.Find misses, ahead of
hiding absent rows.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 2: Local rows come only from the inventory (absent rows hidden)

**Files:**
- Modify: `wt/internal/catalog/catalog.go` (`Status` consts, `Input` doc, `Build` local branch ~lines 95–115, `listed` (new), `Action`, `BlockReason`)
- Test: `wt/internal/catalog/catalog_test.go` (rewrite `TestBuildMarksAbsentAndRespectsAgentAndFilters`, `TestBuildUnknownLocalStatus`, `TestBuildLocalModelMissingFromSnapshot`; edit `TestRowActionRules`, `TestBlockReasonNamesTheFix`)
- Modify (fixtures that assumed configured rows): `wt/cmd/wt/testmain_test.go`, `wt/internal/tui/testhelpers_test.go`, `wt/cmd/wt/model_cmds_test.go`, `wt/cmd/wt/resolve_test.go`, `wt/internal/tui/modeltable_test.go`, `wt/internal/tui/pick_model_test.go`, `wt/internal/tui/start_flow_test.go`, `wt/internal/tui/table_refresh_test.go`

**Interfaces:**
- Consumes: `localmodels.Entry`, `localmodels.Family`; `MissingReason` (Task 1).
- Produces: `func listed(e localmodels.Entry) bool` (unexported); `catalog.StatusAbsent` is deleted (statuses left: `StatusOK`, `StatusUnknown`, `StatusNew`); test helper `func onDiskSnapshot(cfg *config.Config) localmodels.Snapshot` in packages `main` (cmd/wt) and `tui`.

- [ ] **Step 1: Write the failing tests**

In `wt/internal/catalog/catalog_test.go`, extend the import block to

```go
import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)
```

Delete `TestBuildMarksAbsentAndRespectsAgentAndFilters`, `TestBuildUnknownLocalStatus` and `TestBuildLocalModelMissingFromSnapshot` (each with its comment) and put these three in their place:

```go
// TestBuildHidesLocalModelsNotOnDisk verifies #179 Phase B's row rule: a
// registry local model gets a row only from its inventory entry. An overlay
// the probe confirmed is not on disk, and one with no inventory entry at all
// (its family has no probe), both get no row — the picker lists what exists,
// not what is configured. Agent supported_providers stays a hard constraint
// for discovered rows, HideDiscovered (-T/-F) drops them, and a discovered id
// equal to a registry row's id is dropped (the registry row wins).
func TestBuildHidesLocalModelsNotOnDisk(t *testing.T) {
	cfg := catalogTestCfg()
	models := []config.Model{
		{ID: "omlx/gone", ProviderID: "omlx", ModelName: "gone"},
		{ID: "omlx/here", ProviderID: "omlx", ModelName: "here"},
		{ID: "omlx/unprobed", ProviderID: "omlx", ModelName: "unprobed"},
	}
	inv := &localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "omlx", ModelID: "omlx/gone", Registered: true, ArtifactKnown: true},
			{ProviderID: "omlx", ModelID: "omlx/here", Artifact: "here", Registered: true, ArtifactKnown: true},
			{ProviderID: "mtplx", ModelID: "mtplx/x", Artifact: "x", ArtifactKnown: true},      // claude does not support mtplx
			{ProviderID: "ollama", ModelID: "omlx/here", Artifact: "dup", ArtifactKnown: true}, // id collision with a row
			{ProviderID: "ollama", ModelID: "ollama/ok", Artifact: "ok", ArtifactKnown: true},
		},
	}
	in := Input{Config: cfg, Agent: "claude", Models: models, Inventory: inv}
	rows := Build(in)
	if got := strings.Join(rowIDs(rows), ","); got != "omlx/here,ollama/ok" {
		t.Fatalf("rows = %s, want only the on-disk overlay and the discovered model", got)
	}
	if rows[0].Status != StatusOK || rows[0].Running || rows[0].Action() != ActionStart {
		t.Errorf("on-disk idle row = %+v, want status ok, startable", rows[0])
	}
	in.HideDiscovered = true
	if got := strings.Join(rowIDs(Build(in)), ","); got != "omlx/here" {
		t.Errorf("HideDiscovered rows = %s", got)
	}
}

// TestBuildUnknownLocalStatus verifies a flaky probe never hides a model: a
// family whose discovery failed (ollama unreachable, ArtifactKnown false)
// still lists its registry models, as "unknown" and startable — hiding every
// pulled model whenever the daemon hiccups would empty the picker. A running
// row reads ok even though its artifact could not be resolved
// (mlx_lm_server), while a NON-running mlx_lm_server pairing is hidden:
// mlx_lm_server is "running only", wt can neither discover nor start it.
func TestBuildUnknownLocalStatus(t *testing.T) {
	cfg := catalogTestCfg()
	cfg.Providers = append(cfg.Providers, config.Provider{ID: "mlx_lm_server", Location: config.LocationLocal})
	models := []config.Model{
		{ID: "ollama/unknown", ProviderID: "ollama", ModelName: "unknown", Location: config.LocationLocal},
		{ID: "mlx_lm_server/serving", ProviderID: "mlx_lm_server", ModelName: "serving", Location: config.LocationLocal},
		{ID: "mlx_lm_server/idle", ProviderID: "mlx_lm_server", ModelName: "idle", Location: config.LocationLocal},
	}
	inv := &localmodels.Snapshot{
		Providers: map[string]localmodels.Status{
			"ollama": localmodels.StatusUnreachable, "mlx_lm_server": localmodels.StatusOK,
		},
		Entries: []localmodels.Entry{
			{ProviderID: "ollama", ModelID: "ollama/unknown", Registered: true},
			{ProviderID: "mlx_lm_server", ModelID: "mlx_lm_server/serving", Registered: true, Running: true},
			{ProviderID: "mlx_lm_server", ModelID: "mlx_lm_server/idle", Registered: true},
		},
	}
	rows := Build(Input{Config: cfg, Agent: "claude", Models: models, Inventory: inv})
	byID := map[string]Row{}
	for _, r := range rows {
		byID[r.Model.ID] = r
	}
	if r, ok := byID["ollama/unknown"]; !ok || r.Status != StatusUnknown || r.Action() != ActionStart {
		t.Errorf("unreachable ollama row = %+v (present %v), want status unknown and startable (fail open)", r, ok)
	}
	if r, ok := byID["mlx_lm_server/serving"]; !ok || r.Status != StatusOK || !r.Running {
		t.Errorf("running mlx_lm_server row = %+v (present %v), want ok and running", r, ok)
	}
	if _, ok := byID["mlx_lm_server/idle"]; ok {
		t.Error("a non-running mlx_lm_server pairing must have no row")
	}
}

// TestBuildOverlayMatchedAgainstRealInventory runs a real inventory round
// (temp model directories, refused live probes — never a real server) and
// pins overlay matching end to end: a discovered artifact matching a registry
// entry's model_name takes that entry's id, family and tags — both for an id
// that spells the repo's "/" as "--" and for one shaped provider/model_name —
// an overlay whose artifact is missing gets no row, and an unmatched artifact
// becomes a discovered row under config.DiscoveredModelID with
// Source=discovered and no family. Stats and rotation are keyed on these ids,
// so a matching regression silently forks a model's history.
func TestBuildOverlayMatchedAgainstRealInventory(t *testing.T) {
	gone := httptest.NewServer(http.NotFoundHandler())
	refused := gone.URL
	gone.Close()
	omlxDir, mtplxDir := t.TempDir(), t.TempDir()
	for _, d := range []string{
		filepath.Join(omlxDir, "Qwen3.8-9B-4bit"),
		filepath.Join(omlxDir, "stray-model"),
		filepath.Join(mtplxDir, "mlx-community--Qwen3.8-27B-4bit"),
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Config{
		Providers: []config.Provider{
			{ID: "omlx", Location: config.LocationLocal, ModelDir: omlxDir, Auth: config.AuthConfig{Type: "none", BaseURL: refused}},
			{ID: "mtplx", Location: config.LocationLocal, ModelDir: mtplxDir, Auth: config.AuthConfig{Type: "none", BaseURL: refused}},
		},
		Models: []config.Model{
			{ID: "omlx/Qwen3.8-9B-4bit", ProviderID: "omlx", ModelName: "Qwen3.8-9B-4bit", Family: "qwen", Tags: []string{"code"}},
			{ID: "mtplx/mlx-community--Qwen3.8-27B-4bit", ProviderID: "mtplx", ModelName: "mlx-community/Qwen3.8-27B-4bit", Family: "qwen", Tags: []string{"code"}},
			{ID: "omlx/absent-4bit", ProviderID: "omlx", ModelName: "absent-4bit", Family: "qwen", Tags: []string{"code"}},
		},
	}
	snap := localmodels.Inventory(cfg)
	rows := Build(Input{Config: cfg, Models: cfg.Models, Inventory: &snap})
	want := "omlx/Qwen3.8-9B-4bit,mtplx/mlx-community--Qwen3.8-27B-4bit," + config.DiscoveredModelID("omlx", "stray-model")
	if got := strings.Join(rowIDs(rows), ","); got != want {
		t.Fatalf("rows = %s, want %s", got, want)
	}
	for _, r := range rows[:2] {
		if r.Discovered || r.Model.Family != "qwen" || !r.Model.HasTag("code") || r.Status != StatusOK {
			t.Errorf("matched overlay row %s = %+v, want the registry family/tags, status ok", r.Model.ID, r)
		}
	}
	d := rows[2]
	if !d.Discovered || d.Model.Source != config.SourceDiscovered || d.Model.Family != "" || d.Status != StatusNew {
		t.Errorf("unmatched row = %+v, want discovered, Source=discovered, no family, status new", d)
	}
}
```

In `TestRowActionRules`, delete the two cases

```go
		{"absent omlx", local("omlx", StatusAbsent, false, false), ActionBlock},
		{"absent ollama", local("ollama", StatusAbsent, false, false), ActionBlock},
```

and change the last two sentences of its comment to `... a discovered row and an unknown-presence row; a provider with no start engine (mlx_lm_server) is blocked.` Replace `TestBlockReasonNamesTheFix` with:

```go
// TestBlockReasonNamesTheFix verifies the status text for a blocked row names
// what to do — `modelman start <id>` for a provider wt cannot start — and is
// "" for rows that are not blocked.
func TestBlockReasonNamesTheFix(t *testing.T) {
	mlx := Row{Location: config.LocationLocal, Status: StatusOK, Model: config.Model{ID: "mlx_lm_server/p", ProviderID: "mlx_lm_server"}}
	if h := mlx.BlockReason(); !strings.Contains(h, "modelman start mlx_lm_server/p") {
		t.Errorf("mlx_lm_server reason = %q", h)
	}
	if h := (Row{Location: config.LocationCloud}).BlockReason(); h != "" {
		t.Errorf("cloud reason = %q, want empty", h)
	}
	if h := (Row{Location: config.LocationLocal, Status: StatusOK, Model: config.Model{ProviderID: "omlx"}}).BlockReason(); h != "" {
		t.Errorf("startable row reason = %q, want empty", h)
	}
}
```

- [ ] **Step 2: Run them — expect FAIL**

Run: `cd wt && go test ./internal/catalog`
Expected: FAIL — `TestBuildHidesLocalModelsNotOnDisk` (rows include `omlx/gone`, `omlx/unprobed`), `TestBuildUnknownLocalStatus` (the idle mlx_lm_server pairing has a row), `TestBuildOverlayMatchedAgainstRealInventory` (`omlx/absent-4bit` has a row).

- [ ] **Step 3: Implement**

In `wt/internal/catalog/catalog.go`:

1. Delete the `StatusAbsent` constant (`StatusAbsent  Status = "absent"  // the provider answered and does not have this model`).
2. Replace the `Input` type and its doc comment with:

```go
// Input gathers everything Build needs. Models is the caller's eligible list
// (every catalog model the agent supports, already filtered by
// agent/-T/-F); Inventory is nil when no local probe ran, in which case no
// local row is assessed at all: locals read as not running with status ok.
// When Inventory is set, local rows come only from it (#179 Phase B): a
// registry local model gets a row only when its inventory entry is running,
// on disk, or of a family whose discovery failed (status unknown — a flaky
// probe must not hide a pulled model); a model confirmed missing from disk,
// a non-running mlx_lm_server pairing, and a model with no inventory entry
// get none. A local row's status is ok, unknown, or new (discovered,
// unregistered). HideDiscovered drops discovered rows; callers set it
// whenever -T/-F is active, since a discovered row has no family or tags a
// filter could match.
type Input struct {
	Config         *config.Config
	Agent          string
	Models         []config.Model
	Inventory      *localmodels.Snapshot
	HideDiscovered bool
}
```

3. In `Build`, replace the local branch

```go
		} else if loc == config.LocationLocal && in.Inventory != nil {
			e, ok := byID[m.ID]
			switch {
			case !ok:
				// No registered entry: the probe skipped this model (an
				// unresolvable location) or its family has no probe at all.
				// Nothing was discovered about it, so presence is unknown.
				r.Status = StatusUnknown
			case e.Running:
				// Serving right now, so nothing about the row is missing.
				// Checked before the artifact tests because a running
				// mlx_lm_server row never has a resolved artifact.
				r.Running = true
			case !e.ArtifactKnown:
				r.Status = StatusUnknown
			case e.Artifact == "":
				r.Status = StatusAbsent
			}
		}
```

with

```go
		} else if loc == config.LocationLocal && in.Inventory != nil {
			e, ok := byID[m.ID]
			if !ok || !listed(e) {
				continue
			}
			r.Running = e.Running
			if !e.Running && !e.ArtifactKnown {
				r.Status = StatusUnknown
			}
		}
```

4. Insert `listed` directly above `MissingReason`:

```go
// listed reports whether a registered inventory entry gets a row: it is
// serving, or its artifact is on disk, or discovery could not tell (unknown,
// not missing) — except for mlx_lm_server, whose pairings can never be
// discovered, so only a serving one is listed.
func listed(e localmodels.Entry) bool {
	switch {
	case e.Running, e.Artifact != "":
		return true
	case !e.ArtifactKnown:
		return localmodels.Family(e.ProviderID) != "mlx_lm_server"
	}
	return false
}
```

5. Replace `Action` and `BlockReason` with:

```go
// Action is what selecting a row does.
type Action int

// BlockReason is the status line for an ActionBlock row ("" otherwise): a
// provider wt cannot start needs modelman.
func (r Row) BlockReason() string {
	if r.Action() != ActionBlock {
		return ""
	}
	return fmt.Sprintf("local model %q is not running — start it with `modelman start %s`", r.Model.ID, r.Model.ID)
}
```

- [ ] **Step 4: Run the catalog tests — expect PASS**

Run: `cd wt && go test ./internal/catalog`
Expected: PASS.

- [ ] **Step 5: Run the dependents — expect FAIL**

Run: `cd wt && go vet ./... ; go test ./cmd/wt ./internal/tui`
Expected: `go vet` fails on `undefined: catalog.StatusAbsent` in `internal/tui/modeltable_test.go`; after that, tests fail because both packages' `TestMain` stubs the probe with an empty `localmodels.Snapshot{}`, which now hides every configured local row (e.g. `TestSelectedEntryMsgPositionsCursorAtNextToUse: cursor on ""`).

- [ ] **Step 6: Default the inventory stubs to "every registry local model on disk"**

`wt/cmd/wt/testmain_test.go`: in `TestMain` replace
`probeInventory = func(*config.Config) localmodels.Snapshot { return localmodels.Snapshot{} }`
with `probeInventory = onDiskSnapshot`, and append:

```go
// onDiskSnapshot is the default inventory stub: every registry local model
// is on disk (artifact = its model_name) and nothing is running, with every
// family's probe OK. Since #179 Phase B a local row comes only from the
// inventory, so an empty snapshot would hide every configured local model.
func onDiskSnapshot(cfg *config.Config) localmodels.Snapshot {
	snap := localmodels.Snapshot{Providers: map[string]localmodels.Status{}}
	for _, m := range cfg.Models {
		if loc, err := cfg.ResolveLocation(m); err != nil || loc != config.LocationLocal {
			continue
		}
		if fam := localmodels.Family(m.ProviderID); fam != "" {
			snap.Providers[fam] = localmodels.StatusOK
		}
		snap.Entries = append(snap.Entries, localmodels.Entry{
			ProviderID: m.ProviderID, ModelID: m.ID, ModelName: m.ModelName,
			Artifact: m.ModelName, Registered: true, ArtifactKnown: true,
		})
	}
	return snap
}
```

`wt/internal/tui/testhelpers_test.go`: in `TestMain` replace
`runInventory = func(*config.Config) localmodels.Snapshot { return localmodels.Snapshot{} }`
with `runInventory = onDiskSnapshot`, and append the same function:

```go
// onDiskSnapshot is the default inventory stub: every registry local model
// is on disk (artifact = its model_name) and nothing is running, with every
// family's probe OK. Since #179 Phase B a local row comes only from the
// inventory, so an empty snapshot would hide every configured local model.
func onDiskSnapshot(cfg *config.Config) localmodels.Snapshot {
	snap := localmodels.Snapshot{Providers: map[string]localmodels.Status{}}
	for _, m := range cfg.Models {
		if loc, err := cfg.ResolveLocation(m); err != nil || loc != config.LocationLocal {
			continue
		}
		if fam := localmodels.Family(m.ProviderID); fam != "" {
			snap.Providers[fam] = localmodels.StatusOK
		}
		snap.Entries = append(snap.Entries, localmodels.Entry{
			ProviderID: m.ProviderID, ModelID: m.ID, ModelName: m.ModelName,
			Artifact: m.ModelName, Registered: true, ArtifactKnown: true,
		})
	}
	return snap
}
```

- [ ] **Step 7: Fix the fixtures that relied on an absent row or an empty snapshot**

`wt/cmd/wt/model_cmds_test.go` — add `"slices"` to the imports; in `TestStartNoArgUsesPickerAndRunningPickIsNoOp` replace

```go
	if len(offered) != 3 {
		t.Errorf("picker offered %v, want all three local models (running, idle, blocked)", offered)
	}
```

with

```go
	if !slices.Equal(offered, []string{"ollama/a:1", "ollama/b:1"}) {
		t.Errorf("picker offered %v, want the running and idle models only (omlx/c is not on disk, so it has no row)", offered)
	}
```

`wt/cmd/wt/resolve_test.go` — in `TestResolveModelAllLocalGivesPinMessage` make the entry an on-disk idle model:

```go
		Entries:   []localmodels.Entry{{ProviderID: "omlx", ModelID: "omlx/qwen3.8", ModelName: "qwen3.8", Artifact: "qwen3.8", Registered: true, ArtifactKnown: true}},
```

and in `TestResolveModelFiltersHideDiscoveredRows` add the registered model's entry after the discovered one:

```go
			{ProviderID: "mtplx", ModelID: "mtplx/registered", Artifact: "registered", ModelName: "registered", Registered: true, ArtifactKnown: true},
```

`wt/internal/tui/modeltable_test.go` — the fourth `tableTestRows()` row becomes a non-startable row (the only blocked local row left):

```go
		{Row: catalog.Row{Model: config.Model{ID: "mlx_lm_server/pair", ProviderID: "mlx_lm_server", Family: "qwen"}, Location: config.LocationLocal, Status: catalog.StatusUnknown}},
```

In `TestRenderTableHeaderAndCells` rename `absent` to `unknown` in the `cloud, run, disc, absent := ...` line and replace the last check with

```go
	if !strings.Contains(unknown, "unknown") {
		t.Errorf("unknown line = %q", unknown)
	}
```

and in its comment change `STATUS ok/new/absent` to `STATUS ok/new/unknown`. In `TestRenderTableColumnsAlign` replace the STATUS check with

```go
	if got := string(line(3)[col("STATUS") : col("STATUS")+7]); got != "unknown" {
		t.Errorf("STATUS cell = %q", got)
	}
```

In `TestRenderTableBlockedAndMarkers` replace the two `tbl.items[3]` checks with

```go
	if tbl.items[3].blocked == "" || tbl.items[3].start {
		t.Errorf("mlx_lm_server row: start = %v blocked = %q, want blocked and not startable", tbl.items[3].start, tbl.items[3].blocked)
	}
	if !strings.Contains(tbl.items[3].blocked, "modelman start mlx_lm_server/pair") {
		t.Errorf("mlx_lm_server row blocked = %q, want the modelman start reason", tbl.items[3].blocked)
	}
```

and change its comment's `an absent row is blocked with the not-on-disk reason` to `a provider wt cannot start is blocked with the modelman start reason`.

`wt/internal/tui/pick_model_test.go` — in `TestPickStartModelIgnoresLaunchRoutes`, mark the discovered model as discovered (the picker always receives it that way from `localRows`):

```go
		{ID: "omlx/disc", ProviderID: "omlx", ModelName: "disc", Location: config.LocationLocal, Source: config.SourceDiscovered},
```

and replace the `omlx/gone` assertion with

```go
	if it, ok := start["omlx/gone"]; ok {
		t.Errorf("start-only omlx/gone: %+v, want no row (not on disk)", it)
	}
```

changing the comment's `a row that is not on disk stays blocked` to `a model that is not on disk has no row`.

`wt/internal/tui/start_flow_test.go` — replace `TestEndToEndAbsentRowIsBlocked` (with its comment) by:

```go
// TestEndToEndAbsentRowIsHidden verifies a registered omlx model the probe
// confirmed is not on disk gets no picker row at all (#179 Phase B: local rows
// come only from the inventory), so it can neither be started nor selected —
// starting a model that is not there would just wait out the warmup timeout.
func TestEndToEndAbsentRowIsHidden(t *testing.T) {
	stubInventory(t, localmodels.Snapshot{Entries: []localmodels.Entry{
		{ProviderID: "omlx", ModelID: "omlx/qwen3.8", Registered: true, ArtifactKnown: true},
	}})
	calls := stubStartModel(t, func(call int, ctx context.Context, target lifecycle.Target, opts lifecycle.Options) error {
		t.Error("startModel must not be called for an absent model")
		return errors.New("must not start")
	})

	got := flowEnter(t, model{cfg: startCfg("omlx", "omlx/qwen3.8", "qwen3.8"), agent: "claude", selectedPath: t.TempDir(), width: 80, height: 24}, "claude")
	if idx := indexOfID(got, "omlx/qwen3.8"); idx >= 0 {
		t.Fatalf("absent omlx/qwen3.8 has a row at %d in %v, want none", idx, itemIDs(got))
	}
	if calls.len() != 0 {
		t.Errorf("startModel calls = %d, want 0", calls.len())
	}
}
```

`wt/internal/tui/table_refresh_test.go` — in `TestRefreshTableReprobesAndKeepsCursor` make both probes report both models on disk:

```go
	runInventory = func(*config.Config) localmodels.Snapshot {
		probes++
		if probes == 1 {
			return localmodels.Snapshot{Entries: []localmodels.Entry{
				{ProviderID: "omlx", Artifact: "a", ModelID: "omlx/a", Registered: true, Running: true, ArtifactKnown: true},
				{ProviderID: "omlx", Artifact: "b", ModelID: "omlx/b", Registered: true, ArtifactKnown: true},
			}}
		}
		return localmodels.Snapshot{Entries: []localmodels.Entry{
			{ProviderID: "omlx", Artifact: "a", ModelID: "omlx/a", Registered: true, ArtifactKnown: true},
			{ProviderID: "omlx", Artifact: "b", ModelID: "omlx/b", Registered: true, ArtifactKnown: true},
		}}
	}
```

- [ ] **Step 8: Run all of wt — expect PASS**

Run: `cd wt && gofmt -l . && go vet ./... && go test ./...`
Expected: no gofmt output; PASS. The four Review Focus 3 pin tests pass unchanged — now through `MissingReason`.

- [ ] **Step 9: Commit**

```bash
git add wt/internal/catalog wt/cmd/wt/testmain_test.go wt/cmd/wt/model_cmds_test.go wt/cmd/wt/resolve_test.go wt/internal/tui/testhelpers_test.go wt/internal/tui/modeltable_test.go wt/internal/tui/pick_model_test.go wt/internal/tui/start_flow_test.go wt/internal/tui/table_refresh_test.go
git commit -m "feat(wt): local catalog rows come only from the inventory (#179 phase B)

A registry local model gets a row only when the live inventory finds it
running or on disk, or cannot tell (unknown, so a flaky probe hides
nothing); an overlay confirmed missing, a stopped mlx_lm_server pairing
and a model with no probe get none. The absent status and its block are
gone; pins of a hidden id keep their reason through MissingReason.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 3: Contract fixture — overlay matching by `model_name` (both CI sides)

**Files:**
- Modify: `docs/contracts/registry.sample.toml` (header comment; an `mtplx` provider; two `[[models]]` appended)
- Create: `wt/internal/catalog/registry_fixture_test.go`
- Modify: `wt/internal/config/registry_fixture_test.go` (`TestLoadRegistryMatchesSharedFixture`: counts 5→6 providers, 5→7 models, new rows)
- Modify: `modelman/tests/contracts/test_registry_fixture.py` (`test_load_registry_matches_shared_fixture`)

**Interfaces:**
- Consumes: `localmodels.Inventory`, `catalog.Build` (Task 2), `config.Provider.ModelDir`.
- Produces: fixture rows `mtplx/org--contract-fixture-dashed` (`model_name = "org/contract-fixture-dashed"`) and `ollama/contract-fixture:absent`, provider `mtplx`; read by both CI jobs.

- [ ] **Step 1: Write the failing Go test**

Create `wt/internal/catalog/registry_fixture_test.go`:

```go
package catalog

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// TestRegistryFixtureLocalOverlays pins the shared contract fixture's #179
// Phase B rows (docs/contracts/registry.sample.toml, also parsed by
// modelman's tests/contracts/test_registry_fixture.py) against a real
// inventory round — a fake ollama daemon, a temp mtplx model directory, and
// refused probes, never a real server. The "--"-style overlay matches its
// on-disk artifact through model_name, the provider/model_name-style one
// matches as before, and the overlay that is not on disk gets no row though
// both languages still parse it. A one-sided change to how overlays match
// fails here and in modelman's fixture test.
func TestRegistryFixtureLocalOverlays(t *testing.T) {
	var reg struct {
		Providers []config.Provider `toml:"providers"`
		Models    []config.Model    `toml:"models"`
	}
	if _, err := toml.DecodeFile("../../../docs/contracts/registry.sample.toml", &reg); err != nil {
		t.Fatal(err)
	}
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			_, _ = w.Write([]byte(`{"models":[{"name":"contract-fixture:local"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	defer ollama.Close()
	gone := httptest.NewServer(http.NotFoundHandler())
	refused := gone.URL
	gone.Close()
	mtplxDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(mtplxDir, "org--contract-fixture-dashed"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Providers: reg.Providers, Models: reg.Models}
	for i := range cfg.Providers {
		switch p := &cfg.Providers[i]; p.ID {
		case "ollama":
			p.Auth.BaseURL = ollama.URL
		case "mtplx":
			p.Auth.BaseURL, p.ModelDir = refused, mtplxDir
		case "mlx_lm_server":
			p.Auth.BaseURL = refused
		}
	}
	var local []config.Model
	for _, m := range cfg.Models {
		if loc, err := cfg.ResolveLocation(m); err == nil && loc == config.LocationLocal {
			local = append(local, m)
		}
	}
	snap := localmodels.Inventory(cfg)
	rows := Build(Input{Config: cfg, Models: local, Inventory: &snap})
	want := []string{"ollama/contract-fixture:local", "mtplx/org--contract-fixture-dashed"}
	if got := rowIDs(rows); !slices.Equal(got, want) {
		t.Fatalf("rows = %v, want %v (the absent overlay and the stopped pairing get none)", got, want)
	}
	for _, r := range rows {
		if r.Discovered || r.Model.Family != "contract-fixture" {
			t.Errorf("%s = %+v, want the overlay's family, not a discovered row", r.Model.ID, r)
		}
	}
}
```

- [ ] **Step 2: Run it — expect FAIL**

Run: `cd wt && go test ./internal/catalog -run TestRegistryFixtureLocalOverlays`
Expected: FAIL — `rows = [ollama/contract-fixture:local], want [ollama/contract-fixture:local mtplx/org--contract-fixture-dashed]` (the fixture has no mtplx overlay yet).

- [ ] **Step 3: Extend the fixture**

In `docs/contracts/registry.sample.toml`, replace the header's first paragraph ending `# a per-model location override, and a free model.` with:

```toml
# Shared fixture for cross-language contract tests. Exercises every schema
# variant modelman writes and wt reads: all three provider auth types
# (none, api_key, native), full per-token + subscription pricing, model_info,
# a per-model location override, and a free model. Since #179 Phase B it also
# pins local overlay matching: a local registry entry is an overlay matched to
# a discovered model by provider family + model_name, never by id — so an id
# spelling "/" as "--" matches exactly like one shaped provider/model_name, and
# an overlay whose artifact is not on disk is parsed by both sides but gets no
# row in wt's catalog.
```

Insert this provider directly above `[[families]]`:

```toml
# mtplx: a single-model local server whose on-disk artifacts are
# "<org>--<model>" directories, discovered as "org/model".
[[providers]]
id = "mtplx"
name = "MTPLX"
location = "local"
[providers.auth]
type = "none"
base_url = "http://localhost:8003/v1"
```

Append at the end of the file:

```toml

# Local overlay whose id spells the repo's "/" as "--" (the legacy shape two
# reference-host ids use). It matches the discovered artifact
# "org/contract-fixture-dashed" through model_name, not through its id.
[[models]]
id = "mtplx/org--contract-fixture-dashed"
family = "contract-fixture"
provider_id = "mtplx"
model_name = "org/contract-fixture-dashed"
tags = ["code"]

# Local overlay that is not on disk: still a valid registry entry (modelman
# lists it as downloadable), but wt's catalog has no row for it.
[[models]]
id = "ollama/contract-fixture:absent"
family = "contract-fixture"
provider_id = "ollama"
model_name = "contract-fixture:absent"
tags = ["code"]
```

- [ ] **Step 4: Update both decoders' fixture tests**

`wt/internal/config/registry_fixture_test.go`, in `TestLoadRegistryMatchesSharedFixture`: change `len(providers) != 5` / `"got %d providers, want 5"` to `6`, `len(models) != 5` / `"got %d models, want 5"` to `7`, and append before the function's closing brace (after the `pair` check):

```go

	// #179 Phase B overlay rows: a "--"-style local id whose model_name is
	// the repo form, and an overlay that is not on disk. Both must decode
	// like any local model; the catalog test proves which one gets a row.
	mtplx := providers[5]
	if mtplx.ID != "mtplx" || mtplx.Location != LocationLocal || mtplx.Auth.BaseURL != "http://localhost:8003/v1" {
		t.Errorf("mtplx provider decoded wrong: %+v", mtplx)
	}
	dashed, absent := models[5], models[6]
	if dashed.ID != "mtplx/org--contract-fixture-dashed" || dashed.ModelName != "org/contract-fixture-dashed" || dashed.ProviderID != "mtplx" {
		t.Errorf("dashed overlay decoded wrong: %+v", dashed)
	}
	if absent.ID != "ollama/contract-fixture:absent" || absent.ModelName != "contract-fixture:absent" || absent.ProviderID != "ollama" {
		t.Errorf("absent overlay decoded wrong: %+v", absent)
	}
```

`modelman/tests/contracts/test_registry_fixture.py`, in `test_load_registry_matches_shared_fixture`: change `assert len(registry.providers) == 5` to `== 6`, `assert len(registry.models) == 5` to `== 7`, and insert directly above `family = registry.family("contract-fixture")`:

```python
    # #179 Phase B local overlays: a "--"-style id whose model_name is the
    # repo form (wt matches it to the discovered artifact through model_name),
    # and an overlay that is not on disk — still a valid, parsed entry that
    # modelman lists as downloadable while wt's catalog shows no row for it
    # (wt/internal/catalog/registry_fixture_test.go).
    mtplx = registry.provider("mtplx")
    assert mtplx.location == "local"
    assert mtplx.auth.base_url == "http://localhost:8003/v1"
    dashed = registry.model("mtplx/org--contract-fixture-dashed")
    assert dashed.provider_id == "mtplx"
    assert dashed.model_name == "org/contract-fixture-dashed"
    absent = registry.model("ollama/contract-fixture:absent")
    assert absent.provider_id == "ollama"
    assert absent.model_name == "contract-fixture:absent"
```

- [ ] **Step 5: Run both sides — expect PASS**

Run: `cd wt && go test ./internal/catalog ./internal/config ./cmd/wt`
Expected: PASS (`cmd/wt`'s `validationOnlyApp` builds on this fixture and must still load).
Run: `cd modelman && uv run pytest tests/contracts -q`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add docs/contracts/registry.sample.toml wt/internal/catalog/registry_fixture_test.go wt/internal/config/registry_fixture_test.go modelman/tests/contracts/test_registry_fixture.py
git commit -m "test(contracts): pin local overlay matching to model_name (#179 phase B)

registry.sample.toml gains an mtplx provider, a \"--\"-style local overlay
and an overlay that is not on disk; wt's catalog test runs a real
inventory over the fixture, and both decoders assert the new rows.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 4: Pin "rotation never selects a discovered row"; open PR 1

**Files:**
- Test: `wt/internal/rotation/rotation_test.go`

**Interfaces:**
- Consumes: `(*Rotation).NextFromEligible(eligible []config.Model, cfg *config.Config) (config.Model, bool)`, `NewAt(dir string) *Rotation`, `config.DiscoveredModelID`.
- Produces: nothing new (a regression pin for existing behaviour, D1.4).

- [ ] **Step 1: Write the test**

Append to `wt/internal/rotation/rotation_test.go`:

```go
// TestNextFromEligibleNeverPicksDiscovered pins #179 Phase B's rotation rule:
// rotation walks the registry's model order, so a discovered (unregistered)
// model is never auto-selected even when it is launchable — the user can
// still pick one in the picker or pin it with -M. A discovered model has no
// overlay family or tags, so rotating onto it would launch a model the user
// never configured.
func TestNextFromEligibleNeverPicksDiscovered(t *testing.T) {
	cfg := &config.Config{Models: []config.Model{
		{ID: "omlx/registered", ProviderID: "omlx", ModelName: "registered"},
	}}
	disc := config.Model{ID: config.DiscoveredModelID("omlx", "stray"), ProviderID: "omlx", ModelName: "stray", Source: config.SourceDiscovered}
	r := NewAt(t.TempDir())
	m, ok := r.NextFromEligible([]config.Model{disc, cfg.Models[0]}, cfg)
	if !ok || m.ID != "omlx/registered" {
		t.Fatalf("NextFromEligible = (%q, %v), want the registered model", m.ID, ok)
	}
	if m, ok := r.NextFromEligible([]config.Model{disc}, cfg); ok {
		t.Fatalf("NextFromEligible(discovered only) = %q, want no pick", m.ID)
	}
}
```

- [ ] **Step 2: Run it — expect PASS**

Run: `cd wt && go test ./internal/rotation -run TestNextFromEligibleNeverPicksDiscovered`
Expected: PASS — this pins existing behaviour (`NextFromEligible` walks `cfg.Models`); it must keep passing when PR 2 makes discovered rows launchable under LiteLLM. To prove the test can fail, temporarily change `m := cfg.Models[idx]` in `NextFromEligible` to `m := eligible[idx%len(eligible)]`, see it FAIL, and revert.

- [ ] **Step 3: Commit**

```bash
git add wt/internal/rotation/rotation_test.go
git commit -m "test(wt): rotation never selects a discovered model (#179 phase B)

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

- [ ] **Step 4: Verify the PR**

Run: `make test-all` (repo root)
Expected: PASS (shell lint, link check, modelman check+test, wt build/vet/test).

- [ ] **Step 5: Push and open PR 1**

```bash
git push -u origin feat/179-b1-catalog
gh pr create --title "feat(wt): discovery-driven local catalog (#179 phase B, 1/4)" --body "$(cat <<'EOF'
Phase B, PR 1 of 4 (spec: docs/superpowers/specs/2026-10-02-configured-is-exposed-design.md, B1).

- Local rows come only from the live inventory: an overlay not on disk, a stopped mlx_lm_server pairing and a model with no probe get no row; a family whose discovery failed lists its models as `unknown` (a flaky probe hides nothing). The `absent` status and its block are gone.
- `catalog.MissingReason` keeps `<id> is not on disk — pull or download it first` for pins of a hidden id (`wt -M`, TUI pin, `wt start`, `wt smoke`).
- Contract fixture: mtplx provider, a `--`-style overlay and an absent overlay, asserted by both CI jobs; wt's catalog test runs a real inventory over it.
- Rotation-never-picks-discovered pinned.

Routing of discovered models lands in PR 2.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

---

# PR 2 — wt: discovered routing; remove the "not in LiteLLM" refusal and the ready gate

Branch: `feat/179-b2-routing` from `main` after PR 1 merges.

### Task 5: Build routes for models outside the registry; drop the ready gate from `internal/litellm`

**Files:**
- Modify: `wt/internal/litellm/service.go` (`Options` header ~lines 37–48; delete `isCloud` and `prepare` ~lines 97–131, add `lookup`, `prepareModel`, `DiscoveredModel`, `RowFamily`; `plannedAdd`; `Apply`; `applyPlanned`'s add loop; `planSync`'s build call; `Sync`)
- Modify: `wt/internal/litellm/policy.go` (`Policy.Cloud` comment)
- Modify: `wt/internal/lifecycle/routes.go` (`applyAndReport` options), `wt/internal/lifecycle/routes_test.go` (`stubRoutes`)
- Create: `wt/internal/litellm/discovered_test.go`
- Test: `wt/internal/litellm/service_test.go` (`TestApplyGateRejections`, `TestSyncRestartsOnceAndSkipsReadyGate`, every `SkipReadyGate` use)

**Interfaces:**
- Consumes: `config.DiscoveredModelID(providerID, artifactName string) string`, `localmodels.Family`, `BuildEntry(m config.Model, p config.Provider) (*yaml.Node, error)`, `PolicyFor`.
- Produces: `func lookup(cfg *config.Config, id string) (config.Model, error)`; `func prepareModel(cfg *config.Config, m config.Model) (*yaml.Node, error)`; `func DiscoveredModel(providerID, artifact string) config.Model`; `func RowFamily(cfg *config.Config, id string) string`; `plannedAdd{id string; row *yaml.Node; err error}`. Removed: `Options.SkipReadyGate`, `prepare`, `isCloud`.

- [ ] **Step 1: Write the failing tests**

Create `wt/internal/litellm/discovered_test.go`:

```go
package litellm

import (
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// discoveredCfg is testConfig plus the omlx family (omlx and omlx-6bit, one
// physical server) with one registry 6-bit model, and an ollama cloud model —
// a cloud model on the LOCAL ollama provider.
func discoveredCfg() *config.Config {
	cfg := testConfig()
	cfg.Providers = append(cfg.Providers,
		config.Provider{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}},
		config.Provider{ID: "omlx-6bit", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}},
	)
	cfg.Models = append(cfg.Models,
		config.Model{ID: "omlx-6bit/Six", ProviderID: "omlx-6bit", ModelName: "Six", Location: config.LocationLocal},
		config.Model{ID: "ollama/glm:cloud", ProviderID: "ollama", ModelName: "glm:cloud", Location: config.LocationCloud},
	)
	return cfg
}

// TestDiscoveredModelAndRowFamily pins the two id rules discovered routing
// rests on (#179 Phase B): a discovered model's id is the catalog's
// config.DiscoveredModelID under its provider FAMILY — two slashes for an
// mtplx repo id — and RowFamily classifies a config.yaml row by family:
// registry local models by provider (omlx-6bit is the omlx family), registry
// cloud models never (even on the local ollama provider — otherwise an
// untrusted ollama probe would freeze its cloud routes), and any other id by
// its local-provider prefix. A wrong family either strands a stale route or
// lets a stop delete a sibling's.
func TestDiscoveredModelAndRowFamily(t *testing.T) {
	m := DiscoveredModel("mtplx", "mlx-community/Qwen3.8-27B-4bit")
	want := config.Model{ID: "mtplx/mlx-community/Qwen3.8-27B-4bit", ProviderID: "mtplx", ModelName: "mlx-community/Qwen3.8-27B-4bit", Location: config.LocationLocal, Source: config.SourceDiscovered}
	if !equalModel(m, want) {
		t.Fatalf("DiscoveredModel = %+v, want %+v", m, want)
	}
	cfg := discoveredCfg()
	cases := map[string]string{
		"omlx-6bit/Six":                   "omlx",
		"mtplx/Youssofal--Q":              "mtplx",
		"ollama/glm:cloud":                "",
		"openrouter/x/y":                  "",
		"omlx/stray-model":                "omlx",
		"omlx-6bit/deleted":               "omlx",
		"mtplx/mlx-community/Qwen3.8-27B": "mtplx",
		"openrouter/qwen/deleted":         "",
		"my-alias":                        "",
	}
	for id, want := range cases {
		if got := RowFamily(cfg, id); got != want {
			t.Errorf("RowFamily(%s) = %q, want %q", id, got, want)
		}
	}
}

// equalModel compares the identity fields DiscoveredModel sets.
func equalModel(a, b config.Model) bool {
	return a.ID == b.ID && a.ProviderID == b.ProviderID && a.ModelName == b.ModelName && a.Location == b.Location && a.Source == b.Source
}

// TestPrepareModelBuildsDiscoveredRow pins the row a discovered model gets:
// the provider policy supplies the prefixed model, the /v1 api_base and the
// literal key; pricing is the explicit $0 every local row carries; and the
// row is marked as wt's, so a later stop or sync can remove it. Without the
// marker a discovered route would read as hand-written and never go away.
func TestPrepareModelBuildsDiscoveredRow(t *testing.T) {
	cfg := discoveredCfg()
	node, err := prepareModel(cfg, DiscoveredModel("omlx", "stray-model"))
	if err != nil {
		t.Fatal(err)
	}
	got := decode(t, node)
	if got["model_name"] != "omlx/stray-model" {
		t.Errorf("model_name = %v", got["model_name"])
	}
	params := got["litellm_params"].(map[string]any)
	if params["model"] != "openai/stray-model" || params["api_base"] != "http://localhost:8000/v1" || params["api_key"] != "not-needed" {
		t.Errorf("litellm_params = %v", params)
	}
	info := got["model_info"].(map[string]any)
	if info["input_cost_per_token"] != 0 || info["output_cost_per_token"] != 0 || info[ManagedKey] != true {
		t.Errorf("model_info = %v, want $0 pricing and the marker", info)
	}
	if _, err := prepareModel(cfg, DiscoveredModel("ghost", "x")); err == nil {
		t.Error("a discovered model whose provider is not in the registry must be rejected")
	}
}
```

In `wt/internal/litellm/service_test.go`, replace `TestApplyGateRejections` (with its comment) by:

```go
// TestApplyGateRejections pins the route gate: an unknown model and a native
// provider are each reported per id without blocking the valid ids in the
// same batch, and a local model is routed with no "ready" check at all
// (#179 Phase B: the live inventory, not modelman's download flag, decides
// what is routed — a stale flag must never cost a running model its route).
func TestApplyGateRejections(t *testing.T) {
	o, _, _ := opts(t, "model_list: []\n")
	res, err := Apply(testConfig(), []string{"nope/x", "claude/sonnet", "ollama/gemma:9b", "openrouter/x/y"}, nil, o)
	if err != nil {
		t.Fatal(err)
	}
	errs := map[string]string{}
	for _, oc := range res.Outcomes {
		if oc.Err != nil {
			errs[oc.ID] = oc.Err.Error()
		}
	}
	if len(errs) != 2 || !strings.Contains(errs["nope/x"], "not found") || !strings.Contains(errs["claude/sonnet"], "native and is not routed") {
		t.Fatalf("errs = %v, want only the unknown and the native model rejected", errs)
	}
}
```

and replace `TestSyncRestartsOnceAndSkipsReadyGate` (with its comment) by:

```go
// TestSyncRestartsOnce pins that a Sync that changes routes restarts the
// proxy exactly once — each restart kills in-flight agent requests — while
// routing the running local model and, since #179, the configured cloud
// model in the same pass.
func TestSyncRestartsOnce(t *testing.T) {
	o, restarts, p := opts(t, `model_list:
  - model_name: ollama/gemma:9b
    litellm_params: {model: ollama_chat/gemma:9b}
`)
	res, err := Sync(testConfig(), []string{"mtplx/Youssofal--Q"}, o)
	if err != nil || !res.Changed || *restarts != 1 {
		t.Fatalf("err=%v changed=%v restarts=%d, want nil/true/1", err, res.Changed, *restarts)
	}
	f, _ := Open(p)
	if got := strings.Join(f.RoutedIDs(), ","); got != "openrouter/x/y,mtplx/Youssofal--Q" {
		t.Fatalf("routed = %s", got)
	}
}
```

Then drop every remaining `SkipReadyGate` use in that file:

```bash
cd wt && sed -i '' '/o.SkipReadyGate = true/d; /o3.SkipReadyGate = true/d' internal/litellm/service_test.go \
  && sed -i '' 's/, SkipReadyGate: true})/})/' internal/litellm/service_test.go \
  && grep -n SkipReadyGate internal/litellm/service_test.go
```

Expected: no `grep` output.

- [ ] **Step 2: Run them — expect a build failure**

Run: `cd wt && go test ./internal/litellm`
Expected: FAIL — `undefined: DiscoveredModel`, `undefined: RowFamily`, `undefined: prepareModel`.

- [ ] **Step 3: Implement**

In `wt/internal/litellm/service.go`, replace the head of the `Options` type

```go
// Options tunes Apply/Sync.
//   - Path          — config.yaml; "" means DefaultPath().
//   - SkipReadyGate — skip the "model must be ready" check (the lifecycle
//     hook passes it: the model is verifiably running; Sync forces it: its
//     desired set is already the routeable cloud models plus the running
//     local ones).
//   - Restart       — proxy restart hook; nil means Restart. Tests inject.
//   - Ctx           — bounds the config.yaml lock wait; nil means no bound.
type Options struct {
	Path          string
	SkipReadyGate bool
	Restart       func() []string
```

with

```go
// Options tunes Apply/ApplyChange/Sync.
//   - Path    — config.yaml; "" means DefaultPath().
//   - Restart — proxy restart hook; nil means Restart. Tests inject.
//   - Ctx     — bounds the config.yaml lock wait; nil means no bound.
//
// There is no ready gate (#179 Phase B): a local model is routed because the
// live inventory found it, never because modelman flagged it downloaded.
type Options struct {
	Path    string
	Restart func() []string
```

Delete `isCloud` and `prepare` (both with their comments) and put in their place:

```go
// lookup finds id's registry model.
func lookup(cfg *config.Config, id string) (config.Model, error) {
	i := config.IndexModelByID(cfg.Models, id)
	if i < 0 {
		return config.Model{}, fmt.Errorf("model %q not found in registry", id)
	}
	return cfg.Models[i], nil
}

// prepareModel validates m and builds its row. m is a registry model or a
// DiscoveredModel; either way its provider must be in the registry (the row
// dials that provider's base_url) and have a LiteLLM mapping.
func prepareModel(cfg *config.Config, m config.Model) (*yaml.Node, error) {
	p := cfg.ProviderByID(m.ProviderID)
	if p == nil {
		return nil, fmt.Errorf("model %q references unknown provider %q", m.ID, m.ProviderID)
	}
	if m.Native || p.Auth.Type == "native" {
		return nil, fmt.Errorf("provider %q is native and is not routed through LiteLLM", m.ProviderID)
	}
	pol, ok := PolicyFor(p.ID)
	if !ok {
		return nil, fmt.Errorf("provider %q has no LiteLLM mapping", p.ID)
	}
	// Config.validate's per-model data rule; wt commands no longer refuse on
	// validation errors, so enforce it here. FixedModel providers (llamacpp)
	// use a fixed LiteLLM model string and ignore model_name.
	if strings.TrimSpace(m.ModelName) == "" && !pol.FixedModel {
		return nil, fmt.Errorf("model %q: empty model_name", m.ID)
	}
	return BuildEntry(m, *p)
}

// DiscoveredModel is the minimal model wt routes for a discovered local
// artifact that no registry overlay matches (#179 Phase B): its id is the
// catalog's config.DiscoveredModelID — the same id -M, usage and the picker
// use — and PolicyFor supplies api_base, the model prefix and the key. It has
// no cost, so BuildEntry writes the explicit $0 pricing every local row gets.
func DiscoveredModel(providerID, artifact string) config.Model {
	return config.Model{
		ID:         config.DiscoveredModelID(localmodels.Family(providerID), artifact),
		ProviderID: providerID,
		ModelName:  artifact,
		Location:   config.LocationLocal,
		Source:     config.SourceDiscovered,
	}
}

// RowFamily is the local provider family a config.yaml row belongs to, or ""
// for a cloud or unrecognised row. A registry model answers by its provider
// when its location resolves local (a registry cloud model — even one on the
// local ollama provider — is never a local family's row). Any other id is
// classified by its prefix up to the first "/" when that prefix is a local
// provider: discovered ids are exactly "<family>/<artifact>", and a row of a
// deleted local overlay keeps its provider prefix.
func RowFamily(cfg *config.Config, id string) string {
	if i := config.IndexModelByID(cfg.Models, id); i >= 0 {
		m := cfg.Models[i]
		if loc, err := cfg.ResolveLocation(m); err != nil || loc != config.LocationLocal {
			return ""
		}
		return localmodels.Family(m.ProviderID)
	}
	prefix, _, ok := strings.Cut(id, "/")
	if !ok {
		return ""
	}
	return localmodels.Family(prefix)
}
```

Replace `plannedAdd` (with its comment) by:

```go
// plannedAdd is one add from a plan: the id and either its built row or the
// reason it could not be built. Every plan builds its rows while planning, so
// the write path never prepares a row a second time.
type plannedAdd struct {
	id  string
	row *yaml.Node
	err error
}
```

In `Apply`, build each row while planning — replace

```go
		out := make([]plannedAdd, len(add))
		for i, id := range add {
			out[i] = plannedAdd{id: id}
		}
```

with

```go
		out := make([]plannedAdd, len(add))
		for i, id := range add {
			out[i] = plannedAdd{id: id}
			m, err := lookup(cfg, id)
			if err == nil {
				out[i].row, err = prepareModel(cfg, m)
			}
			out[i].err = err
		}
```

In `applyPlanned`, replace the add loop's head

```go
		for _, a := range add {
			row := a.row
			if row == nil {
				var perr error
				if row, perr = prepare(cfg, a.id, o.SkipReadyGate); perr != nil {
					res.Outcomes = append(res.Outcomes, Outcome{ID: a.id, Err: perr})
					continue
				}
			}
			if err := f.SetRow(a.id, row); err != nil {
```

with

```go
		for _, a := range add {
			if a.err != nil {
				res.Outcomes = append(res.Outcomes, Outcome{ID: a.id, Err: a.err})
				continue
			}
			if err := f.SetRow(a.id, a.row); err != nil {
```

In `planSync`, replace

```go
		row, err := prepare(cfg, id, true)
		if err != nil {
```

with

```go
		m, err := lookup(cfg, id)
		var row *yaml.Node
		if err == nil {
			row, err = prepareModel(cfg, m)
		}
		if err != nil {
```

In `Sync`, delete the line `o.SkipReadyGate = true`.

In `wt/internal/litellm/policy.go`, change `//   - Cloud      — the model lives remotely: exempt from the ready gate.` to `//   - Cloud      — the model lives remotely (`wt litellm providers` reports it).`

In `wt/internal/lifecycle/routes.go`'s `applyAndReport`, delete the `SkipReadyGate: true,` line from the `litellm.Options` literal. In `wt/internal/lifecycle/routes_test.go`'s `stubRoutes`, delete

```go
		if !o.SkipReadyGate {
			t.Error("lifecycle hook must skip the ready gate: the model is verifiably running")
		}
```

- [ ] **Step 4: Run wt — expect PASS**

Run: `cd wt && gofmt -l . && go vet ./... && go test ./...`
Expected: no gofmt output; PASS (`config.ReadyFlag` still exists, now unused by `internal/litellm`; Task 6 deletes it).

- [ ] **Step 5: Commit**

```bash
git add wt/internal/litellm/service.go wt/internal/litellm/policy.go wt/internal/litellm/service_test.go wt/internal/litellm/discovered_test.go wt/internal/lifecycle/routes.go wt/internal/lifecycle/routes_test.go
git commit -m "feat(wt): build LiteLLM rows for discovered models; drop the ready gate (#179 phase B)

prepare splits into lookup + prepareModel so a model outside the registry
gets a row: DiscoveredModel names it by its catalog id, PolicyFor supplies
api_base, prefix and key, pricing is the local \$0 and BuildEntry stamps
the marker. RowFamily classifies a config.yaml row by local family. The
ready check and Options.SkipReadyGate are gone.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 6: Delete wt's modelman per-model reader (`ReadyFlag` and friends)

**Files:**
- Modify: `wt/internal/config/modelman.go` (`ModelmanEntry`, `modelmanState`, `loadModelmanState`)
- Modify: `wt/internal/config/config.go` (`Config.modelman` field ~line 505; both `loadModelmanState` calls in `Load` ~lines 571, 605; `finalizeCfg` ~line 654; delete `ReadyFlag`, `SetReadyForTest`, `ReadyAllForTest` ~lines 860–900)
- Modify: `wt/internal/config/registry.go` (`ModelmanPath` comment)
- Modify: `docs/contracts/modelman.sample.toml` (comments only)
- Test: `wt/internal/config/modelman_test.go`, `wt/internal/config/modelman_fixture_test.go`, `wt/internal/config/config_test.go`; delete `wt/internal/config/ready_test.go`; drop `ReadyAllForTest()` from `wt/cmd/wt/{launch_test,main_test,model_cmds_test,resolve_test,smoke_test}.go`, `wt/internal/rotation/rotation_test.go`, `wt/internal/smoke/smoke_test.go`, `wt/internal/tui/{agent_picker_test,app_test,modeltable_flow_test,pin_model_test,start_flow_test,table_refresh_test}.go`; drop `SetReadyForTest` from `wt/internal/tui/agent_model_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: `func loadModelmanState() (*LitellmState, error)`; `func finalizeCfg(cfg *Config, providers []Provider, models []Model, legacy *LitellmState) (*Config, error)`; `modelmanState{PriceRefreshLastRun string; Litellm *LitellmState}`. Removed: `ModelmanEntry`, `Config.modelman`, `(*Config).ReadyFlag`, `(*Config).SetReadyForTest`, `(*Config).ReadyAllForTest`.

Verified unused after Task 5: `grep -rn 'ReadyFlag\|SetReadyForTest\|ReadyAllForTest\|ModelmanEntry' wt --include='*.go'` lists only `internal/config` and test files. The Python side of `modelman.sample.toml` is unchanged.

- [ ] **Step 1: Write the failing test**

In `wt/internal/config/modelman_test.go`, delete `TestLoadModelmanStateMissingFileReturnsEmptySet`, `TestLoadModelmanStateHonorsXDG`, `TestLoadModelmanStateMalformedTOMLError`, `TestLoadModelmanStateReadsLegacyDownloadedAsReady`, `TestLoadModelmanStateIgnoresExposedKeys` and `TestLoadModelmanStateIgnoresRunningFlag` (the whole run from `// TestLoadModelmanStateMissingFileReturnsEmptySet` down to, not including, `// TestLoadModelExposureAcrossNativeLocalCloud`) and put in their place:

```go
// TestLoadModelmanStateMissingFile asserts that a missing modelman.toml is
// not an error: there is simply no legacy [litellm] table to fall back to.
// This is the first-run state before modelman has written anything.
func TestLoadModelmanStateMissingFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	litellm, err := loadModelmanState()
	if err != nil {
		t.Fatalf("loadModelmanState() error = %v, want nil", err)
	}
	if litellm != nil {
		t.Errorf("litellm = %+v, want nil", litellm)
	}
}

// TestLoadModelmanStateHonorsXDG asserts that loadModelmanState reads from
// $XDG_CONFIG_HOME/local-ai/modelman.toml, not a hardcoded ~/.config path.
// Without this guard, a custom XDG location populated by modelman would be
// ignored and the legacy [litellm] fallback would never be found.
func TestLoadModelmanStateHonorsXDG(t *testing.T) {
	dir := t.TempDir()
	want := writeLitellmState(t, dir, true, "http://localhost:4000", "sk-xdg")

	litellm, err := loadModelmanState()
	if err != nil {
		t.Fatalf("loadModelmanState() error = %v", err)
	}
	if litellm == nil || *litellm != want {
		t.Errorf("litellm = %+v, want %+v", litellm, want)
	}
}

// TestLoadModelmanStateMalformedTOMLError asserts that a malformed
// modelman.toml surfaces a clear parse error. Hand-edited TOML can contain
// syntax mistakes, and silent failure would hide the legacy routing state.
func TestLoadModelmanStateMalformedTOMLError(t *testing.T) {
	dir := t.TempDir()
	writeModelmanState(t, dir, `this is not toml {{{`)

	_, err := loadModelmanState()
	if err == nil {
		t.Fatal("expected error for malformed modelman.toml, got nil")
	}
	if !strings.Contains(err.Error(), "parse modelman.toml") {
		t.Errorf("error = %q, want it to mention 'parse modelman.toml'", err)
	}
}

// TestModelmanStateReadsNoPerModelState pins #179 Phase B's boundary: wt
// reads only modelman.toml's legacy [litellm] table and the global
// price_refresh_last_run — no [model_state] field at all, so neither
// `ready`/`downloaded`, `running` nor the retired `exposed` flags can creep
// back into a routing or picker decision. Local presence and running state
// come from wt's live inventory. A file carrying every per-model key still
// loads.
func TestModelmanStateReadsNoPerModelState(t *testing.T) {
	typ := reflect.TypeOf(modelmanState{})
	var fields []string
	for i := 0; i < typ.NumField(); i++ {
		fields = append(fields, typ.Field(i).Name)
	}
	if want := []string{"PriceRefreshLastRun", "Litellm"}; !reflect.DeepEqual(fields, want) {
		t.Fatalf("modelmanState fields = %v, want exactly %v", fields, want)
	}
	dir := t.TempDir()
	writeModelmanState(t, dir, `
[model_state."omlx/qwen3.8"]
ready = true
downloaded = true
running = true
exposed = true
litellm_exposed = true
`)
	if _, err := loadModelmanState(); err != nil {
		t.Fatalf("loadModelmanState() error = %v, want per-model keys ignored", err)
	}
}
```

- [ ] **Step 2: Run it — expect a build failure**

Run: `cd wt && go test ./internal/config -run 'TestModelmanState|TestLoadModelmanState'`
Expected: FAIL — `assignment mismatch: 2 variables but loadModelmanState returns 3 values`.

- [ ] **Step 3: Implement**

In `wt/internal/config/modelman.go`, replace everything from `// ModelmanEntry is the decoded-in-memory subset` through the end of `loadModelmanState` with:

```go
// modelmanState mirrors the subset of ~/.config/local-ai/modelman.toml that
// wt needs read-only access to. The full file is owned by modelman.
//
// wt reads no per-model state at all (#179 Phase B): what is on disk and
// what is running come from its live inventory, never from modelman's
// `ready`/`downloaded`, `running` or retired `exposed` flags.
type modelmanState struct {
	// price_refresh_last_run is modelman's global "token pricing last
	// refreshed" date (YYYY-MM-DD), written by `modelman refresh-prices`.
	// wt reads it post-launch to print a stale-pricing notice.
	PriceRefreshLastRun string        `toml:"price_refresh_last_run"`
	Litellm             *LitellmState `toml:"litellm"`
}

// loadModelmanState reads modelman.toml and returns its legacy [litellm]
// routing state (nil when the table or the file is absent).
func loadModelmanState() (*LitellmState, error) {
	path := ModelmanPath()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read modelman.toml: %w", err)
	}
	var s modelmanState
	if err := toml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse modelman.toml: %w", err)
	}
	return s.Litellm, nil
}
```

In `wt/internal/config/config.go`:
- delete the field line `modelman        map[string]ModelmanEntry `toml:"-"` // from modelman.toml` from `Config` (then `gofmt -w internal/config/config.go` re-aligns the neighbouring field tags);
- in `Load`, change both `mstate, legacy, err := loadModelmanState()` to `legacy, err := loadModelmanState()`, `return finalizeCfg(cfg, providers, models, mstate, legacy)` to `return finalizeCfg(cfg, providers, models, legacy)`, and `c, err := finalizeCfg(fresh, providers, models, mstate, legacy)` to `c, err := finalizeCfg(fresh, providers, models, legacy)`;
- replace `finalizeCfg`'s doc comment and first three statements with:

```go
// finalizeCfg joins registry providers/models into cfg, derives native-ness
// from provider auth types, and applies the already-loaded legacy [litellm]
// table (legacy). Callers must already have loaded config.toml,
// registry.toml, and modelman.toml (loadModelmanState) — finalizeCfg never
// reads modelman.toml itself, so Load reads it exactly once.
func finalizeCfg(cfg *Config, providers []Provider, models []Model, legacy *LitellmState) (*Config, error) {
	cfg.Providers, cfg.Models = providers, models
	deriveNative(cfg)
```

  (the `cfg.modelman = mstate` line goes; the `switch` that follows is unchanged);
- delete `ReadyFlag`, `SetReadyForTest` and `ReadyAllForTest` with their comments.

In `wt/internal/config/registry.go`'s `ModelmanPath` comment, change `The subset wt\n// reads (the per-model ready flags) is pinned by` to `The subset wt\n// reads (price_refresh_last_run and the legacy [litellm] table) is pinned by`.

- [ ] **Step 4: Remove the test helpers' callers and the dead tests**

```bash
cd wt && git rm -q internal/config/ready_test.go \
  && grep -rl 'ReadyAllForTest()' --include='*_test.go' . | xargs sed -i '' -E '/^[[:space:]]*[A-Za-z_]+\.ReadyAllForTest\(\)[[:space:]]*$/d' \
  && sed -i '' '/cfg.SetReadyForTest("ollama\/flagged", true)/d' internal/tui/agent_model_test.go \
  && grep -rn 'ReadyAllForTest\|SetReadyForTest\|ReadyFlag' --include='*.go' .
```

Expected: no `grep` output. The package still fails to build until the two test files below drop `ModelmanEntry` and the three-value `loadModelmanState`.

`wt/internal/config/config_test.go`: in `TestInCatalogNativeAlways` change `cfg := &Config{modelman: map[string]ModelmanEntry{}}` to `cfg := &Config{}`; in `TestInCatalogNonNativeAlwaysIncluded` delete the line `modelman:  map[string]ModelmanEntry{"cloudprov/flagged": {Ready: true}},`.

`wt/internal/config/modelman_fixture_test.go`: in `TestLoadModelmanStateMatchesSharedFixture` replace everything from `models, litellm, err := loadModelmanState()` down to (not including) `if litellm == nil {` with:

```go
	litellm, err := loadModelmanState()
	if err != nil {
		t.Fatalf("loadModelmanState() error: %v", err)
	}

	// The fixture's [model_state] rows (ready/downloaded, running, the
	// legacy exposed keys) are deliberately NOT read: since #179 Phase B wt
	// takes local presence and running state from its live inventory, and
	// reads only the legacy [litellm] table and price_refresh_last_run
	// (pinned by TestModelmanStateReadsNoPerModelState). The file must still
	// load with them present.
```

`docs/contracts/modelman.sample.toml` (comments only — modelman's Python test is unchanged): replace the first comment paragraph with

```toml
# Shared fixture for cross-language contract tests of modelman.toml —
# the per-machine mutable state file modelman writes and wt reads
# read-only. Since #179 Phase B wt reads only two things here: the global
# `price_refresh_last_run` date and the `[litellm]` table below (a legacy
# fallback — the routing table is wt-owned in wt's config.toml). wt reads
# NO per-model [model_state] key: `ready`/`downloaded` (download state),
# `running` and the retired `exposed` flags are modelman's alone; wt takes
# local presence and running state from its live probes of the providers.
```

and change `wt reads it as ready\n# too.` (legacy `downloaded` row) to `wt ignores it like\n# every per-model key.`, `# Both readers yield ready=false.` to `# modelman yields ready=false.`, and `# Local model that is not ready: both readers must yield ready=false.` to `# Local model that is not ready: modelman must yield ready=false.`

- [ ] **Step 5: Run — expect PASS**

Run: `cd wt && gofmt -l . && go vet ./... && go test ./...`
Expected: no gofmt output; PASS.
Run: `cd modelman && uv run pytest tests/contracts/test_modelman_fixture.py -q`
Expected: PASS (comments only).

- [ ] **Step 6: Commit**

```bash
git add -A wt/internal/config wt/cmd/wt wt/internal/rotation wt/internal/smoke wt/internal/tui docs/contracts/modelman.sample.toml
git commit -m "refactor(wt): stop reading modelman's per-model state (#179 phase B)

With the ready gate gone nothing reads ReadyFlag: delete it, its test
helpers and ModelmanEntry. wt now reads only price_refresh_last_run and
the legacy [litellm] table from modelman.toml.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 7: `litellm.ApplyChange` — one targeted write with marker-based family removal

**Files:**
- Modify: `wt/internal/litellm/service.go` (add `isRegistryID` after `RowFamily`; add `Change` and `ApplyChange` directly above `applyPlanned`)
- Modify: `wt/internal/litellm/configfile.go` (add `hasUnmarkedRow` directly above `row`)
- Test: `wt/internal/litellm/discovered_test.go`

**Interfaces:**
- Consumes: `prepareModel`, `RowFamily`, `LocalModels`, `applyPlanned`, `plannedAdd`, `plannedRemove` (Task 5 / existing).
- Produces: `type Change struct { Add []config.Model; Remove []string; RemoveFamilies []string }`; `func ApplyChange(cfg *config.Config, ch Change, o Options) (Result, error)`; `func isRegistryID(cfg *config.Config, id string) bool`; `func (f *File) hasUnmarkedRow(id string) bool`.

- [ ] **Step 1: Write the failing tests**

In `wt/internal/litellm/discovered_test.go`, extend the imports to `"os"`, `"slices"`, `"testing"` plus the config import, and append:

```go
// TestApplyChangeRemovesFamilyMarkedRows pins RemoveFamilies (#179 Phase B):
// a single-model start or stop removes every marked row of the family —
// discovered siblings the registry never named included — plus the family's
// registry ids even when their row predates the marker, but never an
// unmarked row that is not a registry id, another family's row, a cloud row,
// or the id being added. One write, one restart.
func TestApplyChangeRemovesFamilyMarkedRows(t *testing.T) {
	o, restarts, p := opts(t, `model_list:
  - model_name: omlx/disc-a
    litellm_params: {model: openai/disc-a}
    model_info: {wt_managed: true}
  - model_name: omlx/disc-b
    litellm_params: {model: openai/disc-b}
    model_info: {wt_managed: true}
  - model_name: omlx-6bit/Six
    litellm_params: {model: openai/Six}
  - model_name: omlx/hand
    litellm_params: {model: openai/hand}
  - model_name: mtplx/Youssofal--Q
    litellm_params: {model: openai/Youssofal/Q}
    model_info: {wt_managed: true}
  - model_name: openrouter/x/y
    litellm_params: {model: openrouter/x/y}
    model_info: {wt_managed: true}
`)
	ch := Change{Add: []config.Model{DiscoveredModel("omlx", "disc-a")}, RemoveFamilies: []string{"omlx"}}
	res, err := ApplyChange(discoveredCfg(), ch, o)
	if err != nil {
		t.Fatal(err)
	}
	want := []RowInfo{{"omlx/disc-a", true}, {"omlx/hand", false}, {"mtplx/Youssofal--Q", true}, {"openrouter/x/y", true}}
	if got := readRows(t, p); !slices.Equal(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	if !res.Changed || *restarts != 1 {
		t.Fatalf("changed=%v restarts=%d, want true/1", res.Changed, *restarts)
	}
}

// handWrittenLlama is the reference host's hand-written ollama row whose
// name is exactly the discovered id of the pulled llama3.2:3b, carrying a
// hand-set timeout, in a file every earlier wt write already normalised
// (litellm_settings and the ollama drop params), so any change is the row.
const handWrittenLlama = `model_list:
  - model_name: ollama/llama3.2:3b
    litellm_params: {model: ollama_chat/llama3.2:3b, timeout: 600, additional_drop_params: [reasoning_effort]}
litellm_settings:
  drop_params: true
  use_chat_completions_url_for_anthropic_messages: true
`

// TestApplyChangeNeverReplacesHandWrittenDiscoveredName pins the Phase B
// safety rule on the start path: a discovered model whose id already names a
// hand-written (unmarked) row is not written — no add, no adoption, no
// rewrite, no restart — so the user's row (and its timeout) keeps serving the
// name. A registry id keeps Phase A adoption: its unmarked row is wt's.
func TestApplyChangeNeverReplacesHandWrittenDiscoveredName(t *testing.T) {
	o, restarts, p := opts(t, handWrittenLlama)
	res, err := ApplyChange(discoveredCfg(), Change{Add: []config.Model{DiscoveredModel("ollama", "llama3.2:3b")}}, o)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != handWrittenLlama || res.Changed || *restarts != 0 {
		t.Fatalf("hand-written row touched: changed=%v restarts=%d\n%s", res.Changed, *restarts, b)
	}

	o2, _, p2 := opts(t, "model_list:\n  - model_name: ollama/gemma:9b\n    litellm_params: {model: ollama_chat/gemma:9b}\n")
	gemma := discoveredCfg().Models[:1] // ollama/gemma:9b, a registry id
	if _, err := ApplyChange(discoveredCfg(), Change{Add: gemma}, o2); err != nil {
		t.Fatal(err)
	}
	if got := readRows(t, p2); !slices.Equal(got, []RowInfo{{"ollama/gemma:9b", true}}) {
		t.Fatalf("registry row = %v, want it adopted (marked)", got)
	}
}
```

- [ ] **Step 2: Run them — expect a build failure**

Run: `cd wt && go test ./internal/litellm -run TestApplyChange`
Expected: FAIL — `undefined: Change`, `undefined: ApplyChange`.

- [ ] **Step 3: Implement**

In `wt/internal/litellm/service.go`, directly after `RowFamily`:

```go
// isRegistryID reports whether id names a registry model. A discovered route
// (no registry entry) never replaces a hand-written row of the same name;
// registry ids keep Phase A adoption.
func isRegistryID(cfg *config.Config, id string) bool {
	return config.IndexModelByID(cfg.Models, id) >= 0
}
```

Directly above `// applyPlanned is Apply with the add/remove decision`:

```go
// Change is one targeted route write — the lifecycle hooks' unit (#179
// Phase B).
//   - Add            — models to route: registry overlays or DiscoveredModels.
//     A DISCOVERED id that already has a hand-written (unmarked) row is
//     skipped: the user's row serves that name and is never replaced.
//   - Remove         — ids whose rows go: a registry id's rows whether marked
//     or not (Phase A ownership), any other id's marked rows only.
//   - RemoveFamilies — local families (localmodels.Family values) whose
//     routes all go: every marked row whose RowFamily is the family —
//     discovered siblings included — plus the family's registry local ids,
//     marked or legacy-unmarked. Never an unmarked row that is not a registry
//     id, and never an id in Add.
type Change struct {
	Add            []config.Model
	Remove         []string
	RemoveFamilies []string
}

// ApplyChange writes ch in one locked read-modify-write and at most one
// restart, deciding the family removals against the document as read under
// the lock (so a route another process just wrote is seen). Per-model build
// failures are reported in Outcomes and do not block the rest; file-level
// failures are Apply's.
func ApplyChange(cfg *config.Config, ch Change, o Options) (Result, error) {
	return applyPlanned(cfg, func(f *File) ([]plannedAdd, []plannedRemove) {
		adding := map[string]bool{}
		var add []plannedAdd
		for _, m := range ch.Add {
			adding[m.ID] = true
			if !isRegistryID(cfg, m.ID) && f.hasUnmarkedRow(m.ID) {
				continue
			}
			row, err := prepareModel(cfg, m)
			add = append(add, plannedAdd{id: m.ID, row: row, err: err})
		}
		var rm []plannedRemove
		seen := map[string]bool{}
		drop := func(id string, markedOnly bool) {
			if adding[id] || seen[id] {
				return
			}
			seen[id] = true
			rm = append(rm, plannedRemove{id: id, markedOnly: markedOnly})
		}
		for _, id := range ch.Remove {
			drop(id, !isRegistryID(cfg, id))
		}
		for _, fam := range ch.RemoveFamilies {
			for _, m := range LocalModels(cfg) {
				if localmodels.Family(m.ProviderID) == fam {
					drop(m.ID, false)
				}
			}
			for _, r := range f.Rows() {
				if r.Managed && RowFamily(cfg, r.ID) == fam {
					drop(r.ID, true)
				}
			}
		}
		return add, rm
	}, o)
}
```

In `wt/internal/litellm/configfile.go`, directly above `// row returns the first mapping row named id, or nil.`:

```go
// hasUnmarkedRow reports whether any mapping row named id lacks wt's marker:
// a hand-written row of that name, which a discovered route never replaces.
func (f *File) hasUnmarkedRow(id string) bool {
	for _, r := range f.Rows() {
		if r.ID == id && !r.Managed {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Run — expect PASS**

Run: `cd wt && gofmt -l . && go vet ./internal/litellm && go test ./internal/litellm`
Expected: no gofmt output; PASS.

- [ ] **Step 5: Commit**

```bash
git add wt/internal/litellm/service.go wt/internal/litellm/configfile.go wt/internal/litellm/discovered_test.go
git commit -m "feat(wt): ApplyChange writes routes with marker-based family removal (#179 phase B)

Change{Add, Remove, RemoveFamilies} is decided under the config.yaml lock:
a family removal takes every marked row of the family (discovered
siblings included) plus its registry ids, never an unmarked non-registry
row or an id being added; a discovered add never replaces a hand-written
row of the same name.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 8: Lifecycle route hooks route discovered models and clear whole families

**Files:**
- Modify: `wt/internal/lifecycle/routes.go` (`applyRoutes` seam ~line 19; `routeAfterStart`, `routeAfterStop`, `routeRemove` ~lines 67–129; `bounceRoutes` comment; delete `familyModelIDs` ~lines 161–172; `applyAndReport` ~line 191)
- Test: `wt/internal/lifecycle/routes_test.go`, `wt/internal/lifecycle/wrappers_test.go`, `wt/internal/lifecycle/mtplx_test.go` (`TestMain` seam)

**Interfaces:**
- Consumes: `litellm.ApplyChange`, `litellm.Change`, `litellm.DiscoveredModel`, `litellm.ModelFor` (Task 7 / existing).
- Produces: `applyRoutes` seam type `func(*config.Config, litellm.Change, litellm.Options) (litellm.Result, error)`; `func applyAndReport(ctx context.Context, cfg *config.Config, ch litellm.Change, mode restartMode) bool`. Removed: `familyModelIDs`.

- [ ] **Step 1: Write the failing tests**

In `wt/internal/lifecycle/routes_test.go`, replace `type routeCall struct{ add, remove []string }` with

```go
// routeCall is one recorded route write: the added ids, the explicit
// removals and the families whose routes all go.
type routeCall struct{ add, remove, families []string }
```

In `stubRoutes`, change the stub's signature line to `applyRoutes = func(_ *config.Config, ch litellm.Change, o litellm.Options) (litellm.Result, error) {` and replace `calls = append(calls, routeCall{add, remove})` with

```go
		var add []string
		for _, m := range ch.Add {
			add = append(add, m.ID)
		}
		calls = append(calls, routeCall{add, ch.Remove, ch.RemoveFamilies})
```

Replace `TestRouteAfterStartSingleModelReplacesSiblings`, `TestRouteAfterStartMultiTenantOnlyAdds`, `TestRouteAfterStop` and `TestRouteNeverFailsTheCaller` (with comments) by:

```go
// TestRouteAfterStartSingleModelReplacesSiblings pins the bug fix: starting a
// single-model provider's model (mtplx) adds ITS route and removes every
// other route of the provider's family, since starting it stopped whatever
// ran before. Since #179 Phase B the removal is the whole family (marked rows
// of discovered siblings included), not a registry-derived id list that
// missed them. Without it a replaced model keeps a dead route.
func TestRouteAfterStartSingleModelReplacesSiblings(t *testing.T) {
	calls, _ := stubRoutes(t, litellm.Result{}, nil)
	routeAfterStart(context.Background(), routesCfg(), Target{ProviderID: "mtplx", ModelName: "Y/Q35"}, false)
	if len(*calls) != 1 {
		t.Fatalf("calls = %+v, want one", *calls)
	}
	c := (*calls)[0]
	if !slices.Equal(c.add, []string{"mtplx/Y--Q35"}) || len(c.remove) != 0 || !slices.Equal(c.families, []string{"mtplx"}) {
		t.Fatalf("call = %+v, want add [mtplx/Y--Q35] and the mtplx family removed", c)
	}
}

// TestRouteAfterStartMultiTenantOnlyAdds pins that ollama (many models at
// once) only adds the started model's route and never removes siblings that
// may still be loaded.
func TestRouteAfterStartMultiTenantOnlyAdds(t *testing.T) {
	calls, _ := stubRoutes(t, litellm.Result{}, nil)
	routeAfterStart(context.Background(), routesCfg(), Target{ProviderID: "ollama", ModelName: "a:1"}, false)
	if len(*calls) != 1 || !slices.Equal((*calls)[0].add, []string{"ollama/a:1"}) || len((*calls)[0].remove) != 0 || len((*calls)[0].families) != 0 {
		t.Fatalf("calls = %+v", *calls)
	}
}

// TestRouteAfterStartRoutesDiscoveredModel pins #179 Phase B: starting a model
// with no registry overlay routes it under its discovered id — the id the
// picker, -M and usage use — instead of warning "not in the registry" and
// leaving every LiteLLM-forced agent (codex) unable to reach it. A two-slash
// mtplx artifact keeps both slashes in its id.
func TestRouteAfterStartRoutesDiscoveredModel(t *testing.T) {
	calls, warn := stubRoutes(t, litellm.Result{}, nil)
	routeAfterStart(context.Background(), routesCfg(), Target{ProviderID: "ollama", ModelName: "llama3.2:3b"}, false)
	routeAfterStart(context.Background(), routesCfg(), Target{ProviderID: "mtplx", ModelName: "mlx-community/Qwen3.8-27B-4bit"}, false)
	if len(*calls) != 2 || warn.Len() != 0 {
		t.Fatalf("calls = %+v warn = %q, want two writes and no warning", *calls, warn.String())
	}
	if got := (*calls)[0]; !slices.Equal(got.add, []string{"ollama/llama3.2:3b"}) || len(got.families) != 0 {
		t.Errorf("ollama discovered start = %+v, want add [ollama/llama3.2:3b] only", got)
	}
	if got := (*calls)[1]; !slices.Equal(got.add, []string{"mtplx/mlx-community/Qwen3.8-27B-4bit"}) || !slices.Equal(got.families, []string{"mtplx"}) {
		t.Errorf("mtplx discovered start = %+v, want the two-slash id added and the mtplx family removed", got)
	}
}

// TestRouteAfterStartOmlx6bitClearsOmlxFamily pins the omlx/omlx-6bit
// spelling split: a registry omlx-6bit model and a discovered artifact (whose
// id is always spelled "omlx/<artifact>") share one physical server, so
// starting the 6-bit model must remove the whole "omlx" family — the family
// name, never the provider id "omlx-6bit", which no discovered row carries.
func TestRouteAfterStartOmlx6bitClearsOmlxFamily(t *testing.T) {
	calls, _ := stubRoutes(t, litellm.Result{}, nil)
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "omlx-6bit", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}}},
		Models:    []config.Model{{ID: "omlx-6bit/Six", ProviderID: "omlx-6bit", ModelName: "Six", Location: config.LocationLocal}},
	}
	routeAfterStart(context.Background(), cfg, Target{ProviderID: "omlx-6bit", ModelName: "Six"}, false)
	if len(*calls) != 1 || !slices.Equal((*calls)[0].add, []string{"omlx-6bit/Six"}) || !slices.Equal((*calls)[0].families, []string{"omlx"}) {
		t.Fatalf("calls = %+v, want add [omlx-6bit/Six] and the omlx family removed", *calls)
	}
}

// TestRouteAfterStop pins what a stop does to routes: stopping a single-model
// provider's model removes every route of that provider's family (the whole
// provider went down), while stopping an ollama model writes nothing — ollama
// only unloads it, and a pulled model is still served on request, so removing
// its route would break the next request through LiteLLM (#179).
func TestRouteAfterStop(t *testing.T) {
	calls, _ := stubRoutes(t, litellm.Result{}, nil)
	routeAfterStop(context.Background(), routesCfg(), "ollama", "a:1")
	if len(*calls) != 0 {
		t.Fatalf("ollama stop wrote routes: %+v", *calls)
	}
	routeAfterStop(context.Background(), routesCfg(), "mtplx", "Y/Q35")
	if len(*calls) != 1 {
		t.Fatalf("calls = %+v", *calls)
	}
	if c := (*calls)[0]; len(c.add) != 0 || len(c.remove) != 0 || !slices.Equal(c.families, []string{"mtplx"}) {
		t.Errorf("mtplx stop = %+v, want only the mtplx family removed", c)
	}
}

// TestRouteNeverFailsTheCaller pins the failure contract: a LiteLLM error is a
// stderr warning and a missing config.yaml is silent (LiteLLM not set up).
// Failing a successful start over a missing route would strand a running
// model.
func TestRouteNeverFailsTheCaller(t *testing.T) {
	_, warn := stubRoutes(t, litellm.Result{}, errors.New("boom"))
	routeAfterStart(context.Background(), routesCfg(), Target{ProviderID: "ollama", ModelName: "a:1"}, false)
	if !bytes.Contains(warn.Bytes(), []byte("boom")) {
		t.Fatalf("no warning for a failed apply: %q", warn.String())
	}

	_, warn = stubRoutes(t, litellm.Result{}, litellm.ErrMissing)
	routeAfterStart(context.Background(), routesCfg(), Target{ProviderID: "ollama", ModelName: "a:1"}, false)
	if warn.Len() != 0 {
		t.Fatalf("missing config.yaml must be silent, got %q", warn.String())
	}
}
```

Replace `TestRouteAfterStartUnregisteredSettlesOwedRestart` (with its comment) by:

```go
// TestRouteAfterStartDiscoveredSettlesOwedRestart pins that a start of a
// model with no registry overlay still bounces the proxy when an earlier
// deferred occupant-route removal is owed, even when its own route write
// changed nothing (e.g. a hand-written row already serves its name) — and
// that with nothing owed an unchanged write does not bounce. Skipping the
// owed bounce would leave the proxy serving the replaced model's dead route
// until a manual restart.
func TestRouteAfterStartDiscoveredSettlesOwedRestart(t *testing.T) {
	calls, _ := stubRoutes(t, litellm.Result{}, nil)
	restarted := false
	restartProxy = func(context.Context) []string { restarted = true; return nil }
	routeAfterStart(context.Background(), routesCfg(), Target{ProviderID: "ollama", ModelName: "unregistered:9"}, true)
	// The settling bounce restarts asynchronously: let it finish before the
	// second stubRoutes below re-points the seams underneath it.
	WaitPendingRoutes()
	if len(*calls) != 1 || !restarted {
		t.Fatalf("owed restart: calls=%+v restarted=%v, want one write and a bounce", *calls, restarted)
	}
	calls, _ = stubRoutes(t, litellm.Result{}, nil)
	restarted = false
	restartProxy = func(context.Context) []string { restarted = true; return nil }
	routeAfterStart(context.Background(), routesCfg(), Target{ProviderID: "ollama", ModelName: "unregistered:9"}, false)
	WaitPendingRoutes()
	if len(*calls) != 1 || restarted {
		t.Fatalf("nothing owed: calls=%+v restarted=%v, want one write and no bounce", *calls, restarted)
	}
}
```

Change both direct calls `applyAndReport(context.Background(), routesCfg(), []string{"ollama/a:1"}, nil, restartIfChanged)` to `applyAndReport(context.Background(), routesCfg(), litellm.Change{Add: routesCfg().Models[:1]}, restartIfChanged)`.

In `wt/internal/lifecycle/mtplx_test.go`'s `TestMain`, change the stub's signature to `applyRoutes = func(*config.Config, litellm.Change, litellm.Options) (litellm.Result, error) {`.

In `wt/internal/lifecycle/wrappers_test.go`'s `realRoutes`, change `applyRoutes = litellm.Apply` to `applyRoutes = litellm.ApplyChange` (and `the REAL litellm.Apply` to `the REAL litellm.ApplyChange` in its comment), then append:

```go
// TestStopRemovesDiscoveredFamilyRoutesKeepsHandWritten drives a real stop
// through the real litellm.ApplyChange (#179 Phase B): stopping the omlx-6bit
// registry model takes the one oMLX server down, so every marked route of the
// omlx family goes — the 6-bit model's own row and a discovered sibling's
// "omlx/<artifact>" row the registry never named — while an unmarked
// hand-written row that merely shares the family prefix survives, as do
// other families' rows.
func TestStopRemovesDiscoveredFamilyRoutesKeepsHandWritten(t *testing.T) {
	const rows = `model_list:
  - model_name: omlx-6bit/Six
    litellm_params: {model: openai/Six, api_base: http://localhost:8000/v1, api_key: not-needed}
    model_info: {wt_managed: true}
  - model_name: omlx/stray-model
    litellm_params: {model: openai/stray-model, api_base: http://localhost:8000/v1, api_key: not-needed}
    model_info: {wt_managed: true}
  - model_name: omlx/my-hand-row
    litellm_params: {model: openai/my-hand-row, api_base: http://localhost:8000/v1, api_key: not-needed}
  - model_name: mtplx/org/other
    litellm_params: {model: openai/org/other, api_base: http://localhost:8003/v1, api_key: not-needed}
    model_info: {wt_managed: true}
`
	path, _, warn := realRoutes(t, rows)
	var calls []string
	swapBackend(t, "omlx", wrapBackend{single: true, calls: &calls})
	srv := openaiSrv(t, nil)
	cfg := &config.Config{
		Providers: []config.Provider{
			{ID: "omlx-6bit", Location: config.LocationLocal, ModelDir: t.TempDir(), Auth: config.AuthConfig{Type: "none", BaseURL: srv + "/v1"}},
		},
		Models: []config.Model{
			{ID: "omlx-6bit/Six", ProviderID: "omlx-6bit", ModelName: "Six", Location: config.LocationLocal},
		},
	}

	if owed, err := StopModelDeferred(context.Background(), cfg, "omlx-6bit", "Six"); err != nil || !owed {
		t.Fatalf("StopModelDeferred(omlx-6bit) = (%v, %v), want (true, nil)", owed, err)
	}
	SettleRoutes(context.Background(), cfg)
	WaitPendingRoutes()
	if got := routedIDs(t, path); !slices.Equal(got, []string{"omlx/my-hand-row", "mtplx/org/other"}) {
		t.Fatalf("routed = %v, want only the hand-written omlx row and the mtplx row (warn %q)", got, warn.String())
	}
}

// TestStartWrapperRoutesDiscoveredModel drives the real public Start for a
// pulled ollama model with no registry overlay through the real
// litellm.ApplyChange: the route lands under its discovered id, marked as
// wt's, so a LiteLLM-forced agent can reach it (#179 Phase B). Before, the
// hook warned "not in the registry" and wrote nothing.
func TestStartWrapperRoutesDiscoveredModel(t *testing.T) {
	path, _, warn := realRoutes(t, "model_list: []\n")
	var calls []string
	swapBackend(t, "ollama", wrapBackend{calls: &calls})
	cfg := wrapCfg(t, ollamaSrv(t, []string{"llama3.2:3b"}, nil), openaiSrv(t, nil))

	if err := Start(context.Background(), cfg, Target{ProviderID: "ollama", ModelName: "llama3.2:3b"}, Options{}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	WaitPendingRoutes()
	f, err := litellm.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Rows(); !slices.Equal(got, []litellm.RowInfo{{ID: "ollama/llama3.2:3b", Managed: true}}) {
		t.Fatalf("rows = %v, want the discovered id, marked (warn %q)", got, warn.String())
	}
}
```

- [ ] **Step 2: Run them — expect a build failure**

Run: `cd wt && go test ./internal/lifecycle`
Expected: FAIL — `cannot use (func(*config.Config, litellm.Change, litellm.Options) ...) as func(*config.Config, []string, []string, litellm.Options) ...` for the seam.

- [ ] **Step 3: Implement**

In `wt/internal/lifecycle/routes.go`, change the seam to `applyRoutes            = litellm.ApplyChange`. Replace `routeAfterStart`, `routeAfterStop` and `routeRemove` (with comments) by:

```go
// routeAfterStart adds the started model's LiteLLM route: its registry
// overlay's id when one matches, else its discovered id (#179 Phase B — the
// id the catalog, -M and usage use). A single-model provider (omlx, mtplx)
// can serve only one model, so starting one replaced whatever ran before:
// every other route of its family — discovered siblings included — is
// removed in the same write. restartOwed means an earlier deferred write (the
// replaced occupant's route removal) has not been followed by a restart yet;
// this call's bounce settles it, so a replace costs one proxy restart, not
// two. It never fails the caller — a missing route is a warning, not a failed
// start.
func routeAfterStart(ctx context.Context, cfg *config.Config, t Target, restartOwed bool) {
	mode := restartIfChanged
	if restartOwed {
		mode = restartForced
	}
	m, ok := litellm.ModelFor(cfg, t.ProviderID, t.ModelName)
	if !ok {
		m = litellm.DiscoveredModel(t.ProviderID, t.ModelName)
	}
	ch := litellm.Change{Add: []config.Model{m}}
	if SingleModel(t.ProviderID) {
		ch.RemoveFamilies = []string{localmodels.Family(t.ProviderID)}
	}
	applyAndReport(ctx, cfg, ch, mode)
}

// routeAfterStop removes the stopped model's route. Stopping a single-model
// provider's model takes the whole provider down, so every route of its
// family goes. Stopping an ollama model only unloads it, so its route stays
// (see routeRemove). modelName may be "" for a provider-wide stop.
func routeAfterStop(ctx context.Context, cfg *config.Config, providerID, modelName string) {
	routeRemove(ctx, cfg, providerID, modelName, restartIfChanged)
}

// routeRemove writes a stop's route removal and reports whether config.yaml
// changed. For a family whose routes follow its artifact (ollama) it writes
// nothing and returns false: stopping unloads the model, but a pulled model is
// still served on request, so its route stays (#179).
func routeRemove(ctx context.Context, cfg *config.Config, providerID, modelName string, mode restartMode) bool {
	if localmodels.RoutesFollowArtifact(localmodels.Family(providerID)) {
		return false
	}
	var ch litellm.Change
	switch {
	case SingleModel(providerID):
		ch.RemoveFamilies = []string{localmodels.Family(providerID)}
	default:
		// A multi-tenant backend whose routes do not follow artifacts; none
		// exists today (ollama, the only multi-tenant one, returned above).
		m, ok := litellm.ModelFor(cfg, providerID, modelName)
		if !ok {
			m = litellm.DiscoveredModel(providerID, modelName)
		}
		ch.Remove = []string{m.ID}
	}
	return applyAndReport(ctx, cfg, ch, mode)
}
```

Delete `familyModelIDs` and its comment. In `bounceRoutes`' comment change `start that followed them failed or found nothing to route, or when a batch of` to `start that followed them failed, or when a batch of`. Replace the head of `applyAndReport`

```go
func applyAndReport(ctx context.Context, cfg *config.Config, add, remove []string, mode restartMode) bool {
	res, err := applyRoutes(cfg, add, remove, litellm.Options{
```

with

```go
func applyAndReport(ctx context.Context, cfg *config.Config, ch litellm.Change, mode restartMode) bool {
	res, err := applyRoutes(cfg, ch, litellm.Options{
```

and in its comment change `ForceRestart is deliberately not passed — Apply restarts on it even` to `ForceRestart is deliberately not passed — ApplyChange restarts on it even`.

- [ ] **Step 4: Run — expect PASS**

Run: `cd wt && gofmt -l . && go vet ./... && go test ./...`
Expected: no gofmt output; PASS.

- [ ] **Step 5: Commit**

```bash
git add wt/internal/lifecycle
git commit -m "feat(wt): route discovered models on start; clear whole families on stop (#179 phase B)

routeAfterStart routes a model with no registry overlay under its
discovered id instead of warning. A single-model start or stop removes
the family through ApplyChange's RemoveFamilies, so discovered siblings'
marked routes go too; familyModelIDs is gone.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 9: `wt litellm sync` routes discovered models and freezes untrusted families

**Files:**
- Modify: `wt/internal/litellm/service.go` (`Options.UntouchedFamilies`; `planSync` rewrite; `PlanSync`/`Sync` signatures and docs)
- Modify: `wt/internal/localmodels/inventory.go` (add `Families` above `Family`)
- Modify: `wt/cmd/wt/litellm.go` (`runLitellmSync`; `syncUntouchedAndWarnings`; `routedIDSet`; replace `desiredLocalIDs`; add `desiredLocalEntries`, `desiredLocalModels`, `untrustedFamilies`)
- Test: `wt/internal/litellm/discovered_test.go`, `wt/internal/litellm/service_test.go` (`localFor` + call sites), `wt/cmd/wt/litellm_test.go`

**Interfaces:**
- Consumes: `DiscoveredModel`, `RowFamily`, `isRegistryID`, `File.hasUnmarkedRow` (Tasks 5, 7); `localmodels.Snapshot`.
- Produces: `Options.UntouchedFamilies []string`; `func PlanSync(cfg *config.Config, local []config.Model, o Options) (SyncPlan, error)`; `func Sync(cfg *config.Config, local []config.Model, o Options) (Result, error)`; `func planSync(cfg *config.Config, f *File, localIn []config.Model, o Options) (SyncPlan, map[string]*yaml.Node)`; `func localmodels.Families() []string`; `func desiredLocalIDs(snap localmodels.Snapshot) []string` (now registered or discovered); `func desiredLocalEntries(snap localmodels.Snapshot) []localmodels.Entry`; `func desiredLocalModels(cfg *config.Config, snap localmodels.Snapshot) []config.Model`; `func untrustedFamilies(snap localmodels.Snapshot) []string`; `routedIDSet() map[string]bool` now maps each id to "a row of this name is marked" (`syncUntouchedAndWarnings` reads presence for the refused-daemon check and the value for the discovered-only-family warning); test helper `func localFor(cfg *config.Config, ids ...string) []config.Model`. `Options.Recheck` keeps returning fresh desired-local ids (`[]string`).

- [ ] **Step 1: Write the failing tests**

Append to `wt/internal/litellm/discovered_test.go`:

```go
// TestSyncRoutesDiscoveredModels pins sync's Phase B desired set: a
// discovered local model the caller found running (or pulled) gets a marked
// route under its discovered id — two-slash mtplx ids included — and loses it
// once it is no longer found, because the marked row is wt's.
func TestSyncRoutesDiscoveredModels(t *testing.T) {
	o, _, p := opts(t, "model_list: []\n")
	cfg := discoveredCfg()
	local := []config.Model{DiscoveredModel("ollama", "llama3.2:3b"), DiscoveredModel("mtplx", "mlx-community/Qwen3.8-27B-4bit")}
	if _, err := Sync(cfg, local, o); err != nil {
		t.Fatal(err)
	}
	want := []RowInfo{{"openrouter/x/y", true}, {"ollama/glm:cloud", true}, {"ollama/llama3.2:3b", true}, {"mtplx/mlx-community/Qwen3.8-27B-4bit", true}}
	if got := readRows(t, p); !slices.Equal(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	if _, err := Sync(cfg, nil, o); err != nil {
		t.Fatal(err)
	}
	if got := readRows(t, p); !slices.Equal(got, want[:2]) {
		t.Fatalf("rows after the models went away = %v, want only the cloud routes", got)
	}
}

// TestSyncLeavesHandWrittenDiscoveredName pins Review Focus 1 on the sync
// path: the reference host's hand-written "ollama/llama3.2:3b" row is named
// exactly like the discovered id of the pulled model, yet sync neither plans
// nor performs an add or adoption for it, and keeps it when the model is gone
// — the row is the user's, not wt's.
func TestSyncLeavesHandWrittenDiscoveredName(t *testing.T) {
	o, _, p := opts(t, handWrittenLlama)
	cfg := discoveredCfg()
	local := []config.Model{DiscoveredModel("ollama", "llama3.2:3b")}
	plan, err := PlanSync(cfg, local, o)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(plan.Add, "ollama/llama3.2:3b") || slices.Contains(plan.Remove, "ollama/llama3.2:3b") {
		t.Fatalf("plan = %+v, want the hand-written row left alone", plan)
	}
	for _, l := range [][]config.Model{local, nil} {
		if _, err := Sync(cfg, l, o); err != nil {
			t.Fatal(err)
		}
		if got := readRows(t, p); !slices.Contains(got, RowInfo{"ollama/llama3.2:3b", false}) {
			t.Fatalf("rows = %v, want the hand-written row kept unmarked", got)
		}
	}
}

// TestSyncSkipsUntouchedFamilies pins UntouchedFamilies: while a family's
// probe cannot vouch for it, sync neither removes its discovered routes nor
// adds new ones — a flaky probe must never wipe routes it cannot see — while
// a trusted family's stale discovered row still goes, and a registry cloud
// model on the untrusted family's provider is still routed (it is not that
// family's row).
func TestSyncSkipsUntouchedFamilies(t *testing.T) {
	o, _, p := opts(t, `model_list:
  - model_name: omlx/stray-model
    litellm_params: {model: openai/stray-model, api_base: http://localhost:8000/v1, api_key: not-needed}
    model_info: {wt_managed: true}
  - model_name: mtplx/org/gone
    litellm_params: {model: openai/org/gone, api_base: http://localhost:8003/v1, api_key: not-needed}
    model_info: {wt_managed: true}
`)
	o.UntouchedFamilies = []string{"omlx", "ollama"}
	local := []config.Model{DiscoveredModel("omlx", "new-model")}
	if _, err := Sync(discoveredCfg(), local, o); err != nil {
		t.Fatal(err)
	}
	want := []RowInfo{{"omlx/stray-model", true}, {"openrouter/x/y", true}, {"ollama/glm:cloud", true}}
	if got := readRows(t, p); !slices.Equal(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
}

// TestSyncRecheckCoversDiscoveredIDs pins that the under-lock Recheck guards
// discovered routes exactly like registry ones: a discovered model that
// stopped before the lock gets no fresh route, and a discovered model that
// started meanwhile keeps the marked row the stale probe would have removed.
func TestSyncRecheckCoversDiscoveredIDs(t *testing.T) {
	o, _, p := opts(t, `model_list:
  - model_name: omlx/started-meanwhile
    litellm_params: {model: openai/started-meanwhile, api_base: http://localhost:8000/v1, api_key: not-needed}
    model_info: {wt_managed: true}
`)
	o.Recheck = func() []string { return []string{"omlx/started-meanwhile"} }
	local := []config.Model{DiscoveredModel("omlx", "stopped-meanwhile")}
	if _, err := Sync(discoveredCfg(), local, o); err != nil {
		t.Fatal(err)
	}
	want := []RowInfo{{"omlx/started-meanwhile", true}, {"openrouter/x/y", true}, {"ollama/glm:cloud", true}}
	if got := readRows(t, p); !slices.Equal(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
}

// TestSyncDeletedOverlayKeepsTheModelRouted pins spec B3's "deleting a local
// overlay entry does not change routing": once its registry entry is gone, a
// running model is found as discovered and keeps a marked route under its
// discovered id. For an overlay whose id was provider/model_name the route
// name does not even change; for a legacy "--"-style id the old marked row
// goes and the model is routed under its discovered id (the documented stats
// split for such ids). Only a stop removes a local route.
func TestSyncDeletedOverlayKeepsTheModelRouted(t *testing.T) {
	o, _, p := opts(t, `model_list:
  - model_name: ollama/llama3.2:3b
    litellm_params: {model: ollama_chat/llama3.2:3b}
    model_info: {wt_managed: true}
  - model_name: mtplx/org--deleted
    litellm_params: {model: openai/org/deleted}
    model_info: {wt_managed: true}
`)
	local := []config.Model{DiscoveredModel("ollama", "llama3.2:3b"), DiscoveredModel("mtplx", "org/deleted")}
	if _, err := Sync(discoveredCfg(), local, o); err != nil {
		t.Fatal(err)
	}
	want := []RowInfo{{"ollama/llama3.2:3b", true}, {"openrouter/x/y", true}, {"ollama/glm:cloud", true}, {"mtplx/org/deleted", true}}
	if got := readRows(t, p); !slices.Equal(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
}
```

In `wt/internal/litellm/service_test.go`, add above `opts`:

```go
// localFor returns cfg's registry models named by ids — the shape Sync and
// PlanSync take for the local models a probe found running (or pulled).
func localFor(cfg *config.Config, ids ...string) []config.Model {
	var out []config.Model
	for _, id := range ids {
		if i := config.IndexModelByID(cfg.Models, id); i >= 0 {
			out = append(out, cfg.Models[i])
		}
	}
	return out
}
```

and move every `Sync`/`PlanSync`/`planSync` call from running ids to models (12 call sites; `nil` stays `nil`):

```bash
cd wt && perl -0pi -e 's/\b((?:Sync|PlanSync|planSync)\((testConfig\(\)|cfg)(, f)?), \[\]string\{([^}]*)\}/$1, localFor($2, $4)/g' internal/litellm/service_test.go \
  && grep -c 'localFor(' internal/litellm/service_test.go
```

Expected: `13` — the helper's declaration plus 12 converted call sites.

In `wt/cmd/wt/litellm_test.go`, update `TestDesiredLocalIDsOllamaFollowsPulled`: replace its comment with

```go
// TestDesiredLocalIDsOllamaFollowsPulled pins #179's rule for which local
// models sync routes: any running one, plus an ollama model that is pulled
// whether or not it is loaded — registered or discovered (Phase B: a pulled
// model with no overlay is routed under its discovered id). ollama lazy-loads
// on request and unloads idle models, so keying ollama on "loaded" made a
// flag-only start get no route and made every sync after an idle unload drop
// a working route. "Pulled" must mean the probe actually saw the artifact
// (ArtifactKnown), and it must not widen to single-model families, which
// serve nothing until started.
```

and its `want` with `want := []string{"ollama/pulled:1", "ollama/discovered:1", "mtplx/live", "ollama/loaded:1"}`. Append:

```go
// TestLitellmSyncRoutesDiscoveredModels drives the real `wt litellm sync`
// over a stubbed probe (#179 Phase B): a pulled ollama model with no registry
// overlay gets a marked route under its discovered id, a running mtplx model
// with a two-slash discovered id gets one too, and a family whose probe did
// not succeed (omlx, unreachable) keeps its existing discovered route instead
// of losing it to a probe that could not see it.
func TestLitellmSyncRoutesDiscoveredModels(t *testing.T) {
	p := litellmEnv(t, `model_list:
  - model_name: omlx/stray-model
    litellm_params: {model: openai/stray-model, api_base: http://localhost:8000/v1, api_key: not-needed, use_chat_completions_api: true}
    model_info: {wt_managed: true}
litellm_settings:
  drop_params: true
  use_chat_completions_url_for_anthropic_messages: true
`)
	cfg := litellmTestConfig()
	cfg.Providers = append(cfg.Providers,
		config.Provider{ID: "mtplx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8003/v1"}},
		config.Provider{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}},
	)
	stubProbeInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK, "mtplx": localmodels.StatusOK, "omlx": localmodels.StatusUnreachable},
		Entries: []localmodels.Entry{
			{ProviderID: "ollama", ModelID: "ollama/llama3.2:3b", ModelName: "llama3.2:3b", Artifact: "llama3.2:3b", ArtifactKnown: true},
			{ProviderID: "mtplx", ModelID: "mtplx/org/Qwen3.8-27B", ModelName: "org/Qwen3.8-27B", Artifact: "org/Qwen3.8-27B", ArtifactKnown: true, Running: true},
		},
	})
	var out, errOut bytes.Buffer
	if err := runLitellmSync(&out, &errOut, cfg, false, false); err != nil {
		t.Fatalf("sync: %v (stderr %q)", err, errOut.String())
	}
	f, err := litellm.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	want := []litellm.RowInfo{{ID: "omlx/stray-model", Managed: true}, {ID: "ollama/llama3.2:3b", Managed: true}, {ID: "mtplx/org/Qwen3.8-27B", Managed: true}}
	if got := f.Rows(); !slices.Equal(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
}

// TestUntrustedFamilies pins which families sync freezes: every probe family
// whose probe is neither OK nor refused (Down) — partial, unreachable, or not
// probed at all — so their discovered routes, which carry no registry id the
// per-id Untouched list could name, are left exactly as they are.
func TestUntrustedFamilies(t *testing.T) {
	snap := localmodels.Snapshot{
		Providers: map[string]localmodels.Status{
			"ollama": localmodels.StatusOK, "omlx": localmodels.StatusPartial, "mtplx": localmodels.StatusUnreachable,
		},
		Down: map[string]bool{"mtplx": true},
	}
	if got := untrustedFamilies(snap); !slices.Equal(got, []string{"mlx_lm_server", "omlx"}) {
		t.Fatalf("untrustedFamilies = %v, want [mlx_lm_server omlx]", got)
	}
}

// TestLitellmSyncWarnsForUntrustedDiscoveredOnlyFamily pins that a family
// sync freezes only for its discovered routes — no registry local model, so
// the per-id Untouched list never names it — still gets the "probe did not
// succeed" warning when config.yaml holds a marked row of that family, in
// the dry run and the real sync alike. Without it a dead omlx probe left
// discovered omlx routes frozen with no word to the user; a family with no
// marked row stays silent, as the everyday unused-provider case must.
func TestLitellmSyncWarnsForUntrustedDiscoveredOnlyFamily(t *testing.T) {
	const want = `provider "omlx" probe did not succeed (status "partial"); its model routes were left unchanged`
	litellmEnv(t, `model_list:
  - model_name: omlx/stray-model
    litellm_params: {model: openai/stray-model, api_base: http://localhost:8000/v1, api_key: not-needed, use_chat_completions_api: true}
    model_info: {wt_managed: true}
litellm_settings:
  drop_params: true
  use_chat_completions_url_for_anthropic_messages: true
`)
	cfg := litellmTestConfig()
	cfg.Providers = append(cfg.Providers, config.Provider{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}})
	stubProbeInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK, "omlx": localmodels.StatusPartial, "mtplx": localmodels.StatusUnreachable},
	})
	for _, dryRun := range []bool{true, false} {
		var out, errOut bytes.Buffer
		if err := runLitellmSync(&out, &errOut, cfg, true, dryRun); err != nil {
			t.Fatalf("dryRun=%v: %v", dryRun, err)
		}
		var doc struct{ Warnings []string }
		if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
			t.Fatalf("dryRun=%v: not JSON: %q", dryRun, out.String())
		}
		if !slices.Equal(doc.Warnings, []string{want}) {
			t.Errorf("dryRun=%v: warnings = %q, want only %q (mtplx has no marked row, so it stays silent)", dryRun, doc.Warnings, want)
		}
	}
}
```

- [ ] **Step 2: Run them — expect a build failure**

Run: `cd wt && go vet ./internal/litellm ./cmd/wt`
Expected: FAIL — `cannot use localFor(...) (value of type []config.Model) as []string value in argument to Sync`, `o.UntouchedFamilies undefined`, `undefined: untrustedFamilies`. (Once it builds, `TestLitellmSyncWarnsForUntrustedDiscoveredOnlyFamily` fails until Step 4's `syncUntouchedAndWarnings` change: no warning is printed.)

- [ ] **Step 3: Implement the litellm side**

In `wt/internal/litellm/service.go`, add to `Options` directly after the `Untouched []string` field:

```go
	// UntouchedFamilies lists local provider families (localmodels.Family
	// values) whose probe could not vouch for their state. Sync neither adds,
	// removes nor rewrites a row whose RowFamily is one of them — discovered
	// routes included, which Untouched (registry ids only) cannot name.
	UntouchedFamilies []string
```

Replace `planSync` (with its comment) by:

```go
// planSync decides a sync against f. Desired = every CloudModels id plus
// the caller's local models (#179 Phase B: running or pulled, registered or
// discovered). A row is wt's to remove when it carries the marker or is named
// like a managed registry id (cloud or local) — the latter covers rows
// written before the marker existed. Unmarked rows with any other name are
// hand-written and never touched, and a desired DISCOVERED id that already
// has a hand-written row is left to it (no add, no adoption). Untouched ids,
// and every row whose RowFamily is in UntouchedFamilies, are neither added,
// rewritten nor removed; Recheck (see Options) re-verifies local ids. Built
// rows for plan.Add are returned alongside the plan: Sync's write path reuses
// them instead of preparing every changed row a second time.
//
// Accepted cost of UntouchedFamilies: a marked row of a DELETED cloud model
// whose id starts with a local family prefix (e.g. "ollama/x:cloud") reads as
// that family's row, so it stays while the family is untrusted and goes on
// the next sync whose probe is healthy.
func planSync(cfg *config.Config, f *File, localIn []config.Model, o Options) (SyncPlan, map[string]*yaml.Node) {
	localModels := LocalModels(cfg)
	untouchedFam := map[string]bool{}
	for _, fam := range o.UntouchedFamilies {
		untouchedFam[fam] = true
	}
	frozen := func(id string) bool {
		return slices.Contains(o.Untouched, id) || untouchedFam[RowFamily(cfg, id)]
	}
	managed := map[string]bool{}
	var desired []config.Model
	for _, m := range CloudModels(cfg) {
		managed[m.ID] = true
		desired = append(desired, m)
	}
	// local is every id Recheck may prune: registry local ids, the desired
	// local ids (discovered ones included) and every row of a local family.
	local := map[string]bool{}
	for _, m := range localModels {
		managed[m.ID] = true
		local[m.ID] = true
	}
	// gap holds registry models dropped from both lists by a data gap (a
	// provider_id naming no provider, or no location to resolve): their rows
	// stay until the registry is repaired. Removing them would delete every
	// route — and its hand-added params — the moment `providers = []` lands.
	gap := map[string]bool{}
	for _, m := range cfg.Models {
		if _, err := cfg.ResolveLocation(m); !m.Native && (cfg.ProviderByID(m.ProviderID) == nil || err != nil) {
			gap[m.ID] = true
		}
	}
	wantLocal := map[string]bool{}
	for _, m := range localIn {
		wantLocal[m.ID] = true
	}
	// Registry local models keep registry order; discovered ones follow in
	// the caller's order.
	for _, m := range localModels {
		if wantLocal[m.ID] && !frozen(m.ID) {
			desired = append(desired, m)
			local[m.ID] = true
		}
	}
	for _, m := range localIn {
		if isRegistryID(cfg, m.ID) || frozen(m.ID) {
			continue
		}
		local[m.ID] = true
		if f.hasUnmarkedRow(m.ID) {
			continue // a hand-written row already serves this name
		}
		desired = append(desired, m)
	}
	for _, r := range f.Rows() {
		if RowFamily(cfg, r.ID) != "" {
			local[r.ID] = true
		}
	}
	var plan SyncPlan
	built := map[string]*yaml.Node{}
	want := map[string]bool{}
	// rowsPerID counts the mapping rows sharing each model_name: LiteLLM
	// load-balances across duplicates, so a value-equal FIRST row still needs
	// SetRow when a second row shares the id (SetRow replaces the first row
	// and drops the rest). Only a single value-equal row is a clean no-op.
	rows := f.Rows()
	rowsPerID := map[string]int{}
	for _, r := range rows {
		rowsPerID[r.ID]++
	}
	// credErr resolves each SecretRef provider's credentials once per plan.
	// A failure is never cached by config.ResolveSecret and an exec: helper
	// may take its full timeout, so resolving per model multiplied one broken
	// helper by the provider's model count.
	credErr := map[string]error{}
	for _, m := range desired {
		id := m.ID
		want[id] = true
		if err := providerCredErr(cfg, id, credErr); err != nil {
			plan.Errors = append(plan.Errors, Outcome{ID: id, Err: fmt.Errorf("model %q: %w", id, err)})
			continue
		}
		row, err := prepareModel(cfg, m)
		if err != nil {
			plan.Errors = append(plan.Errors, Outcome{ID: id, Err: err})
			continue
		}
		old := f.row(id)
		if old != nil {
			carryUserParams(old, row)
			// Compare by value, not bytes: a row read back from disk keeps
			// quoting styles ('ollama/gemma:9b') a freshly built one lacks,
			// so byte equality would report every unchanged row as changed.
			var oldv, newv any
			if old.Decode(&oldv) == nil && row.Decode(&newv) == nil && reflect.DeepEqual(oldv, newv) && rowsPerID[id] == 1 {
				continue
			}
		}
		built[id] = row
		plan.Add = append(plan.Add, id)
		switch {
		case old == nil:
		case IsManaged(old):
			plan.Rewrite = append(plan.Rewrite, id)
		default:
			plan.Adopt = append(plan.Adopt, id)
		}
	}
	// Iterate rows, not models: a row outlives a registry entry that was deleted
	// or renamed, so a loop over the model lists would never visit it and would
	// strand the stale route (TestSyncRemovesMarkedRowOfDeletedModel). Ownership
	// is read per row — its marker, or `managed` for the unmarked pre-#179 rows
	// TestSyncRemovesLegacyUnmarkedLocalRoute pins — so a model-shaped rewrite of
	// this loop loses both clauses.
	//
	// seen deduplicates: two rows can share one id and one RemoveRow drops both,
	// so the id belongs in the plan once. Without it the same removal was
	// announced twice in the report and handed twice to callers of PlanSync.
	//
	// want is filled before prepare runs — deliberately, since `managed` holds
	// every cloud id: a desired cloud row whose build fails still short-circuits
	// this loop by name and keeps its route, where moving that assignment below
	// the error branch would let one transient build failure delete a live route.
	seen := map[string]bool{}
	for _, r := range rows {
		if want[r.ID] || gap[r.ID] || frozen(r.ID) || seen[r.ID] {
			continue
		}
		if r.Managed || managed[r.ID] {
			seen[r.ID] = true
			plan.Remove = append(plan.Remove, r.ID)
			if !managed[r.ID] {
				if plan.markedOnly == nil {
					plan.markedOnly = map[string]bool{}
				}
				plan.markedOnly[r.ID] = true
			}
		}
	}
	if o.Recheck != nil {
		touchesLocal := slices.ContainsFunc(plan.Add, func(id string) bool { return local[id] }) ||
			slices.ContainsFunc(plan.Remove, func(id string) bool { return local[id] })
		if touchesLocal {
			if fresh, ok := runRecheck(o); ok {
				plan.Remove = slices.DeleteFunc(plan.Remove, func(id string) bool { return local[id] && slices.Contains(fresh, id) })
				// The same predicate the Add side gets, so Adopt and Rewrite stay
				// the subsets of Add that SyncPlan documents: an id pruned from
				// Add must not stay in either, or the plan describes a change it
				// will not perform.
				stoppedAgain := func(id string) bool { return local[id] && !slices.Contains(fresh, id) }
				plan.Add = slices.DeleteFunc(plan.Add, stoppedAgain)
				plan.Adopt = slices.DeleteFunc(plan.Adopt, stoppedAgain)
				plan.Rewrite = slices.DeleteFunc(plan.Rewrite, stoppedAgain)
			}
		}
	}
	return plan, built
}
```

(The tail of `planSync` below the build loop — the removal loop and the Recheck block — is the existing code except that the removal loop's first condition becomes `if want[r.ID] || gap[r.ID] || frozen(r.ID) || seen[r.ID] {`; the block above shows the whole function.)

Change `func PlanSync(cfg *config.Config, running []string, o Options) (SyncPlan, error) {` to `func PlanSync(cfg *config.Config, local []config.Model, o Options) (SyncPlan, error) {` and its `plan, _ := planSync(cfg, f, running, o)` to `plan, _ := planSync(cfg, f, local, o)`. Replace `Sync`'s doc comment and signature through its `planSync` call with:

```go
// Sync reconciles config.yaml with the registry and the local models the
// caller found (#179): every CloudModels id and every model in local —
// registry overlay or DiscoveredModel — gets a marked route; rows wt owns
// that are no longer desired are removed; hand-written rows are never touched
// (see planSync). The plan is made under the config.yaml lock. A config.yaml
// whose model_list is not a list is refused (ErrInvalid) before anything is
// planned or written, whatever the plan turns out to be — PlanSync checks the
// same shape, so a dry run and a real sync always agree. Outcome actions:
// "routed", "adopted", "rewritten", "unrouted"; per-id build failures are
// reported with Err.
func Sync(cfg *config.Config, local []config.Model, o Options) (Result, error) {
	var plan SyncPlan
	res, err := applyPlanned(cfg, func(f *File) ([]plannedAdd, []plannedRemove) {
		p, built := planSync(cfg, f, local, o)
```

In `wt/internal/localmodels/inventory.go`, directly above `// Family maps a registry provider id to its probe family`:

```go
// Families lists every probe family wt knows, sorted.
func Families() []string { return []string{"mlx_lm_server", "mtplx", "ollama", "omlx"} }

// Family maps a registry provider id to its probe family ("omlx-6bit" shares
// "omlx"); "" when wt has no probe for it.
func Family(providerID string) string { return familyOf(providerID) }

// RoutesFollowArtifact reports whether a family's LiteLLM routes follow
// artifact presence rather than running state. True only for ollama: it
// lazy-loads a model on request (and unloads idle ones), so a pulled model is
// servable whether or not it is loaded. family is a Family value.
func RoutesFollowArtifact(family string) bool { return family == "ollama" }

// source is one family's probe result.
type source struct {
	family     string
	status     Status
	artifacts  []string // discovered names, provider spelling (mtplx: repo id form)
	loaded     []string // names serving right now
	registered int      // local registry models in this family
	down       bool     // the server refused the connection (nothing listening)
}
```

- [ ] **Step 4: Implement the CLI side**

In `wt/cmd/wt/litellm.go`, replace `runLitellmSync` by:

```go
func runLitellmSync(out, errOut io.Writer, cfg *config.Config, asJSON, dryRun bool) error {
	snap := probeInventory(cfg)
	// The local models to route: running ones, plus pulled ollama models,
	// registered or discovered (desiredLocalModels). That set is
	// untrustworthy for a family whose probe did not fully succeed; leave its
	// routes — discovered ones included — exactly as they are. The exception
	// is a server that refused the connection: nothing is listening, so
	// nothing is running, and its routes are stale and must go (the case a
	// provider stopped outside wt leaves behind).
	desired := desiredLocalModels(cfg, snap)
	untouched, probeWarns := syncUntouchedAndWarnings(cfg, snap, routedIDSet())
	o := litellm.Options{
		Untouched:         untouched,
		UntouchedFamilies: untrustedFamilies(snap),
		// The probe above predates the config.yaml lock; a start that lands
		// in between must not lose its route, so removals are re-verified
		// under the lock.
		Recheck: func() []string { return desiredLocalIDs(probeInventory(cfg)) },
	}
	if dryRun {
		plan, err := litellm.PlanSync(cfg, desired, o)
		if err != nil {
			return err
		}
		return reportSyncPlan(out, errOut, plan, probeWarns, asJSON)
	}
	res, err := litellm.Sync(cfg, desired, o)
	if err != nil {
		return err
	}
	res.Warnings = append(res.Warnings, probeWarns...)
	return reportLitellm(out, errOut, res, asJSON)
}
```

and replace `desiredLocalIDs` (with its comment) by:

```go
// desiredLocalIDs lists the local model ids sync should route — registered
// or discovered (#179 Phase B): every entry a probe found running, plus every
// entry of a family whose routes follow its artifact (ollama) that the probe
// saw pulled, loaded or not — ollama serves a pulled model on request (#179).
// Pulled counts only when the family's probe is fully OK: a partial probe
// (e.g. /api/ps refused after /api/tags answered — the daemon died) cannot
// vouch that anything is serving. An unknown artifact (ArtifactKnown false)
// never counts as pulled. The real sync, its dry run and the under-lock
// Recheck all call this one function so they cannot drift.
func desiredLocalIDs(snap localmodels.Snapshot) []string {
	var desired []string
	for _, e := range desiredLocalEntries(snap) {
		desired = append(desired, e.ModelID)
	}
	return desired
}

// desiredLocalEntries is desiredLocalIDs' rule, returning the entries.
func desiredLocalEntries(snap localmodels.Snapshot) []localmodels.Entry {
	var desired []localmodels.Entry
	for _, e := range snap.Entries {
		fam := localmodels.Family(e.ProviderID)
		pulled := e.ArtifactKnown && e.Artifact != "" &&
			localmodels.RoutesFollowArtifact(fam) &&
			snap.Providers[fam] == localmodels.StatusOK
		if e.Running || pulled {
			desired = append(desired, e)
		}
	}
	return desired
}

// desiredLocalModels maps desiredLocalIDs' entries to the models Sync
// routes: a registered entry's registry overlay, else a DiscoveredModel under
// the entry's discovered id.
func desiredLocalModels(cfg *config.Config, snap localmodels.Snapshot) []config.Model {
	var out []config.Model
	for _, e := range desiredLocalEntries(snap) {
		if i := config.IndexModelByID(cfg.Models, e.ModelID); e.Registered && i >= 0 {
			out = append(out, cfg.Models[i])
			continue
		}
		out = append(out, litellm.DiscoveredModel(e.ProviderID, e.Artifact))
	}
	return out
}

// untrustedFamilies lists the local provider families whose routes sync must
// leave exactly as they are: every family wt can probe whose probe is
// neither OK nor Down (refused) — including one that was not probed at all.
// Unlike syncUntouchedAndWarnings' per-id list, a family covers the rows of
// discovered models too, which carry no registry id.
func untrustedFamilies(snap localmodels.Snapshot) []string {
	var fams []string
	for _, f := range localmodels.Families() {
		if snap.Providers[f] != localmodels.StatusOK && !snap.Down[f] {
			fams = append(fams, f)
		}
	}
	return fams
}
```

Replace `syncUntouchedAndWarnings` and `routedIDSet` (with comments) by the blocks below — the changes are the doc paragraph on `routed`, the loop that adds a frozen family with only discovered marked rows to `skipped` (so it gets the existing "probe did not succeed" warning in both sync modes), the refused-daemon check reading presence (`_, ok := routed[m.ID]`), and `routedIDSet` recording whether a row is marked:

```go
// syncUntouchedAndWarnings derives both sync modes' view of the provider
// probes: the ids whose routes must not move (families the probe could not
// vouch for), and the warnings the run reports. One helper for the real sync
// and the dry run, so the two always report the same warnings — the dry run
// used to differ from the real run's output, promising a clean run that then
// arrived degraded.
//
// A family whose server REFUSED the connection gets no untouched entries
// (nothing is listening, so nothing is running and its local routes are
// stale). It gets a warning only when that matters: a local route of the
// family is in config.yaml (routed holds its ids) and is about to go, or the
// family serves registry cloud models, which sync still routes though they
// dial the same refused daemon — a failure that must be reported, not
// swallowed. A stopped provider with neither is the everyday case and stays
// silent.
//
// routed maps every model_name in config.yaml to whether a row of that name
// carries wt's marker. A family sync freezes through UntouchedFamilies
// (untrustedFamilies) that has no registry local model — only discovered
// routes (#179 Phase B) — gets the same "probe did not succeed" warning when
// config.yaml holds a marked row of the family, so its frozen routes are
// never left unchanged silently.
func syncUntouchedAndWarnings(cfg *config.Config, snap localmodels.Snapshot, routed map[string]bool) (untouched, warnings []string) {
	skipped := map[string]bool{}
	for _, m := range litellm.LocalModels(cfg) {
		fam := localmodels.Family(m.ProviderID)
		if fam == "" {
			// No probe exists for this provider (retired llamacpp), so
			// nothing can vouch for its state: leave it, with no probe warning.
			untouched = append(untouched, m.ID)
			continue
		}
		if snap.Providers[fam] != localmodels.StatusOK && !snap.Down[fam] {
			untouched = append(untouched, m.ID)
			skipped[fam] = true
		}
	}
	// Every untrusted family with a registry local model is in skipped
	// already; the rest are frozen only for their discovered rows.
	for _, f := range untrustedFamilies(snap) {
		for id, managed := range routed {
			if !skipped[f] && managed && litellm.RowFamily(cfg, id) == f {
				skipped[f] = true
			}
		}
	}
	fams := make([]string, 0, len(skipped))
	for f := range skipped {
		fams = append(fams, f)
	}
	sort.Strings(fams)
	for _, f := range fams {
		if snap.Ambiguous[f] {
			// The probe answered; it is the served model that cannot be
			// tied to one registered pairing, not a probe failure. Worded for
			// both shapes (a foreign id, and an empty list): "is serving"
			// would be a lie in the second.
			warnings = append(warnings, fmt.Sprintf("provider %q answered, but wt cannot tell which of its registered models it is serving (status %q); its model routes were left unchanged", f, snap.Providers[f]))
			continue
		}
		warnings = append(warnings, fmt.Sprintf("provider %q probe did not succeed (status %q); its model routes were left unchanged", f, snap.Providers[f]))
	}
	downs := make([]string, 0, len(snap.Down))
	for f := range snap.Down {
		if snap.Down[f] {
			downs = append(downs, f)
		}
	}
	sort.Strings(downs)
	inFamily := func(f string, ms []config.Model, keep func(config.Model) bool) bool {
		return slices.ContainsFunc(ms, func(m config.Model) bool { return localmodels.Family(m.ProviderID) == f && keep(m) })
	}
	for _, f := range downs {
		local := inFamily(f, litellm.LocalModels(cfg), func(m config.Model) bool { _, ok := routed[m.ID]; return ok })
		cloud := inFamily(f, litellm.CloudModels(cfg), func(config.Model) bool { return true })
		if !local && !cloud {
			continue
		}
		msg := fmt.Sprintf("provider %q refused the probe connection: nothing is listening there", f)
		if local {
			msg += ", so its local routes are treated as stale"
		}
		if cloud {
			msg += "; its cloud models stay routed but fail until it is back up"
		}
		warnings = append(warnings, msg)
	}
	return untouched, warnings
}

// routedIDSet maps every model_name id in config.yaml to whether a row of
// that name carries wt's marker, for the probe warnings. An unreadable file
// yields an empty map: sync itself then refuses the file with the real error.
func routedIDSet() map[string]bool {
	out := map[string]bool{}
	f, err := litellm.Open(litellm.DefaultPath())
	if err != nil {
		return out
	}
	for _, r := range f.Rows() {
		out[r.ID] = out[r.ID] || r.Managed
	}
	return out
}
```

- [ ] **Step 5: Run — expect PASS**

Run: `cd wt && gofmt -l . && go vet ./... && go test ./...`
Expected: no gofmt output; PASS.

- [ ] **Step 6: Commit**

```bash
git add wt/internal/litellm wt/internal/localmodels/inventory.go wt/cmd/wt/litellm.go wt/cmd/wt/litellm_test.go
git commit -m "feat(wt): sync routes discovered models and freezes untrusted families (#179 phase B)

Sync/PlanSync take the local models as []config.Model — a registry
overlay, or a DiscoveredModel under its discovered id — so every running
(or pulled ollama) model is routed, registered or not. A family whose
probe is neither OK nor refused is in Options.UntouchedFamilies, so its
discovered routes are neither removed nor added — with the usual probe
warning when it holds a marked row; a discovered id that already has a
hand-written row is left to it.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 10: Contract fixture — a discovered route in `wt litellm sync --json`

**Files:**
- Modify: `docs/contracts/litellm-cli.sample.json` (`sync.outcomes`, `sync_dry_run.plan.add`)
- Test: `wt/cmd/wt/litellm_test.go` (`TestLitellmSyncDryRunJSONMatchesContract`, `TestLitellmSyncJSONMatchesContract`), `modelman/tests/contracts/test_litellm_cli_fixture.py` (`test_sync_fixture_parses`)

**Interfaces:**
- Consumes: `litellm.Sync(cfg, local []config.Model, o)`, `litellm.DiscoveredModel` (Task 9).
- Produces: fixture outcome `{"id": "ollama/llama3.2:3b", "action": "routed"}` read by both CI jobs.

- [ ] **Step 1: Write the failing tests**

`wt/cmd/wt/litellm_test.go`, `TestLitellmSyncDryRunJSONMatchesContract`: change the plan's `Add:    []string{"openrouter/x/y"},` to `Add:    []string{"openrouter/x/y", "ollama/llama3.2:3b"},`.

`TestLitellmSyncJSONMatchesContract`: replace its comment's second sentence with `The result comes from a real litellm.Sync over a config.yaml that exercises every outcome — unrouted, rewritten, adopted, routed (a cloud model, and since #179 Phase B a discovered local model under its discovered id, written marked) and a per-id error — so the action names are pinned where Sync assigns them, not restated by hand.`, and replace

```go
	res, err := litellm.Sync(cfg, nil, litellm.Options{Path: p, Restart: func() []string { return []string{restartWarn} }})
	if err != nil {
		t.Fatal(err)
	}
```

with

```go
	local := []config.Model{litellm.DiscoveredModel("ollama", "llama3.2:3b")}
	res, err := litellm.Sync(cfg, local, litellm.Options{Path: p, Restart: func() []string { return []string{restartWarn} }})
	if err != nil {
		t.Fatal(err)
	}
	f, err := litellm.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(f.Rows(), litellm.RowInfo{ID: "ollama/llama3.2:3b", Managed: true}) {
		t.Fatalf("rows = %v, want the discovered route written marked", f.Rows())
	}
```

`modelman/tests/contracts/test_litellm_cli_fixture.py`, `test_sync_fixture_parses`: add `("ollama/llama3.2:3b", "routed"),` after `("openrouter/new", "routed"),` in the expected list, and extend its comment's first sentence to `every sync action survives the parse — a discovered local model's route (#179 Phase B) arrives under its discovered id like any other — a per-id build error ...`.

- [ ] **Step 2: Run them — expect FAIL**

Run: `cd wt && go test ./cmd/wt -run Contract`
Expected: FAIL — `sync --json = ... fixture = ...` (the output carries the new outcome, the fixture does not) and the dry-run block mismatch.
Run: `cd modelman && uv run pytest tests/contracts/test_litellm_cli_fixture.py -q`
Expected: FAIL — the parsed outcomes lack `("ollama/llama3.2:3b", "routed")`.

- [ ] **Step 3: Update the fixture**

In `docs/contracts/litellm-cli.sample.json`, insert `{"id": "ollama/llama3.2:3b", "action": "routed"},` after the `openrouter/new` outcome in `sync.outcomes`, and change `sync_dry_run.plan.add` to `["openrouter/x/y", "ollama/llama3.2:3b"]`.

- [ ] **Step 4: Run both sides — expect PASS**

Run: `cd wt && go test ./cmd/wt -run Contract`
Expected: PASS.
Run: `cd modelman && uv run pytest tests/contracts -q`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add docs/contracts/litellm-cli.sample.json wt/cmd/wt/litellm_test.go modelman/tests/contracts/test_litellm_cli_fixture.py
git commit -m "test(contracts): a discovered local route in wt litellm sync's JSON (#179 phase B)

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 11: Drop the "not in LiteLLM" refusal for discovered rows; open PR 2

**Files:**
- Modify: `wt/internal/catalog/catalog.go` (`RefusedByRoute`, `RouteRefusal`)
- Modify: `wt/cmd/wt/resolve.go` (`launchableModels`; comments on `resolveModel`, `pickerBlockedReason`, `launchableModels`)
- Modify: `wt/internal/smoke/smoke.go` (`walk`'s route check and comment; `Eligibility` comment)
- Modify: `wt/internal/tui/modeltable.go` (comment in `renderTable`'s `RefusedByRoute` case)
- Test: `wt/internal/catalog/catalog_test.go` (`TestRefusedByRoute`), `wt/cmd/wt/resolve_test.go`, `wt/internal/smoke/smoke_test.go`, `wt/internal/tui/modeltable_test.go`

**Interfaces:**
- Consumes: `catalog.Row.Unmapped` (existing), `rotation.NewAt`, `(*Rotation).NextFromEligible`.
- Produces: `RefusedByRoute` true only for `Unmapped` rows; `RouteRefusal` returns only the cloud wording.

- [ ] **Step 1: Write the failing tests**

`wt/internal/catalog/catalog_test.go` — replace `TestRefusedByRoute` (with its comment) by:

```go
// TestRefusedByRoute verifies the one route-refusal rule: only an Unmapped
// cloud row whose route resolved AND goes through LiteLLM (routed or forced)
// is refused. A discovered row is never refused (#179 Phase B: wt routes it
// under its discovered id), and a route error is not a refusal (the picker
// reports it on Enter instead). The picker, the non-TUI -M pin and `wt smoke`
// all call this, so a change here moves all three.
func TestRefusedByRoute(t *testing.T) {
	direct, viaProxy, forced := config.Route{}, config.Route{Litellm: true}, config.Route{Forced: true}
	cases := []struct {
		name  string
		row   Row
		route config.Route
		err   error
		want  bool
	}{
		{"discovered via litellm", Row{Discovered: true}, viaProxy, nil, false},
		{"discovered forced", Row{Discovered: true}, forced, nil, false},
		{"unmapped via litellm", Row{Unmapped: true}, viaProxy, nil, true},
		{"unmapped forced", Row{Unmapped: true}, forced, nil, true},
		{"unmapped direct", Row{Unmapped: true}, direct, nil, false},
		{"unmapped with route error", Row{Unmapped: true}, viaProxy, errors.New("no route"), false},
		{"registered via litellm", Row{}, viaProxy, nil, false},
	}
	for _, c := range cases {
		if got := c.row.RefusedByRoute(c.route, c.err); got != c.want {
			t.Errorf("%s: RefusedByRoute = %v, want %v", c.name, got, c.want)
		}
	}
}
```

`wt/cmd/wt/resolve_test.go` — add `"github.com/ohanaverse/local-ai-setup/wt/internal/rotation"` to the imports; replace `TestResolveModelPinOnDiscoveredUnderLitellmRefuses` (with its comment) by:

```go
// TestResolveModelPinOnDiscoveredForcedThroughLitellm pins #179 Phase B's
// end of the "not in LiteLLM" refusal: codex speaks only openai-responses, so
// its route to omlx is forced through the proxy, and a -M pin on a discovered
// model now starts it (idle) or launches it (running) instead of being
// refused — wt routes a discovered model under its discovered id as soon as
// it runs. Before, the only way to use one from codex was to register it.
func TestResolveModelPinOnDiscoveredForcedThroughLitellm(t *testing.T) {
	disc := config.DiscoveredModelID("omlx", "extra")
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}}},
		Agents:    []config.Agent{{Name: "codex", SupportedProviders: []string{"omlx"}}},
	}
	// Routing off but configured: codex's protocol forces LiteLLM anyway.
	cfg.SetLitellmForTest(config.LitellmState{URL: "http://localhost:4000", APIKey: "sk-test"})
	for _, running := range []bool{false, true} {
		stubProbeInventory(t, localmodels.Snapshot{
			Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
			Entries: []localmodels.Entry{
				{ProviderID: "omlx", ModelID: disc, Artifact: "extra", ModelName: "extra", ArtifactKnown: true, Running: running},
			},
		})
		calls := stubStartDriver(t, nil)
		m, _, err := resolveModel("codex", cfg, "", "", disc)
		if err != nil || m.ID != disc {
			t.Fatalf("running=%v: resolveModel = (%q, %v), want the discovered model", running, m.ID, err)
		}
		if calls.called == running {
			t.Errorf("running=%v: start driver called = %v, want a start only for the idle model", running, calls.called)
		}
	}
}
```

and replace both `TestResolveModelPinOnRunningDiscoveredUnderLitellmRefuses` and `TestResolveModelRotationSkipsDiscoveredUnderLitellm` (with comments) by the single:

```go
// TestResolveModelDiscoveredUnderLitellmNeverRotatedInto verifies the no-pin
// path under LiteLLM routing (#179 Phase B): a running discovered model is
// now launchable — it is routed, so the picker no longer refuses it — but
// rotation still never lands on it: with a running registry model beside it,
// resolveModel reports the ambiguity and the rotation fallback picks the
// registry model, because rotation walks the registry's order.
func TestResolveModelDiscoveredUnderLitellmNeverRotatedInto(t *testing.T) {
	disc := config.DiscoveredModelID("omlx", "extra")
	stubProbeInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "omlx", ModelID: "omlx/reg", Artifact: "reg", ModelName: "reg", Registered: true, ArtifactKnown: true, Running: true},
			{ProviderID: "omlx", ModelID: disc, Artifact: "extra", ModelName: "extra", ArtifactKnown: true, Running: true},
		},
	})
	calls := stubStartDriver(t, nil)
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}}},
		Models:    []config.Model{{ID: "omlx/reg", ProviderID: "omlx", ModelName: "reg", Tags: []string{"code"}}},
		Agents:    []config.Agent{{Name: "pi", SupportedProviders: []string{"omlx"}}},
	}
	cfg.SetLitellmForTest(config.LitellmState{Enabled: true, URL: "http://localhost:4000", APIKey: "sk-test"})

	_, launchable, err := resolveModel("pi", cfg, "", "", "")
	if err == nil || !strings.Contains(err.Error(), "multiple models match") {
		t.Fatalf("err = %v, want the multiple-models ambiguity", err)
	}
	if len(launchable) != 2 {
		t.Fatalf("launchable = %v, want the registry and the discovered model", launchable)
	}
	if next, ok := rotation.NewAt(t.TempDir()).NextFromEligible(launchable, cfg); !ok || next.ID != "omlx/reg" {
		t.Errorf("rotation picked (%q, %v), want the registry model", next.ID, ok)
	}
	if calls.called {
		t.Error("rotation must not start anything")
	}
}
```

and append the controller-ruling pin (Design notes):

```go
// TestResolveModelSoleRunningDiscoveredAutoResolves pins a Phase B ruling
// (#179): when the only launchable row is a running discovered model, a
// launch with no -M resolves to it — under LiteLLM routing as in direct mode,
// now that it is routed. The spec restricts rotation (which never lands on a
// discovered row), not this single-launchable-row shortcut; this test makes
// that a decision rather than an accident.
func TestResolveModelSoleRunningDiscoveredAutoResolves(t *testing.T) {
	disc := config.DiscoveredModelID("omlx", "extra")
	stubProbeInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "omlx", ModelID: disc, Artifact: "extra", ModelName: "extra", ArtifactKnown: true, Running: true},
		},
	})
	calls := stubStartDriver(t, nil)
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}}},
		Agents:    []config.Agent{{Name: "pi", SupportedProviders: []string{"omlx"}}},
	}
	for _, enabled := range []bool{false, true} {
		cfg.SetLitellmForTest(config.LitellmState{Enabled: enabled, URL: "http://localhost:4000", APIKey: "sk-test"})
		m, launchable, err := resolveModel("pi", cfg, "", "", "")
		if err != nil || m.ID != disc || len(launchable) != 1 {
			t.Errorf("litellm enabled=%v: resolveModel = (%q, %d launchable, %v), want the discovered model", enabled, m.ID, len(launchable), err)
		}
	}
	if calls.called {
		t.Error("a launch row must not reach the start driver")
	}
}
```

`wt/internal/smoke/smoke_test.go` — replace `TestEligibilityExcludesDiscoveredUnderLitellm` (with its comment) by:

```go
// TestEligibilityIncludesDiscoveredUnderLitellm verifies a discovered running
// model routed through LiteLLM IS eligible (#179 Phase B): wt routes it under
// its discovered id, so a real launch accepts it and smoke must offer it —
// before, the picker refused such a row and smoke hid it to match.
func TestEligibilityIncludesDiscoveredUnderLitellm(t *testing.T) {
	cfg := smokeFixtureConfig(t)
	disc := config.DiscoveredModelID("ollama", "extra")
	stubSmokeProbe(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "ollama", Artifact: "extra", ModelID: disc, ModelName: "extra", Running: true},
		},
	})
	cfg.SetLitellmForTest(config.LitellmState{Enabled: true, URL: "http://localhost:4000", APIKey: "sk-test"})
	models, _ := Eligibility(cfg)
	if !slices.ContainsFunc(models, func(m config.Model) bool { return m.ID == disc }) {
		t.Errorf("discovered model routed through LiteLLM not eligible: %+v", models)
	}
}
```

`wt/internal/tui/modeltable_test.go` — replace `TestRenderTableDiscoveredBlockedUnderLitellm` (with its comment) by:

```go
// TestRenderTableDiscoveredSelectableUnderLitellm verifies a discovered model
// is selectable with LiteLLM routing on, exactly as in direct mode (#179
// Phase B): wt routes it under its discovered id, so the old "(not in
// LiteLLM)" block would refuse a model the proxy serves. Under routing it is
// labelled "(via proxy)" only when the route is forced, like any other row.
func TestRenderTableDiscoveredSelectableUnderLitellm(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "omlx", Location: config.LocationLocal, Protocols: []config.Protocol{config.ProtocolOpenAIChat}, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}}},
		Agents:    []config.Agent{{Name: "opencode", SupportedProviders: []string{"omlx"}}},
	}
	row := tableRow{Row: catalog.Row{Model: config.Model{ID: "omlx/disc", ProviderID: "omlx", ModelName: "disc", Location: config.LocationLocal}, Location: config.LocationLocal, Status: catalog.StatusNew, Running: true, Discovered: true}}

	for _, enabled := range []bool{false, true} {
		cfg.SetLitellmForTest(config.LitellmState{Enabled: enabled, URL: "http://localhost:4000", APIKey: "sk-test"})
		tbl := renderTable([]tableRow{row}, cfg, "opencode", nil, "")
		if it := tbl.items[0]; it.blocked != "" || it.exception != "" {
			t.Errorf("litellm enabled=%v: blocked=%q exception=%q, want a selectable row", enabled, it.blocked, it.exception)
		}
	}
}
```

and change `TestRenderTableDiscoveredLitellmUnconfigured`'s comment ending `: the "(not in LiteLLM)" block only\n// applies when the route resolves without error, so a config error must not be\n// masked by it.` to `: a route error is reported on\n// Enter, like any launch row's.`

- [ ] **Step 2: Run them — expect FAIL**

Run: `cd wt && go test ./internal/catalog ./cmd/wt ./internal/smoke ./internal/tui`
Expected: FAIL — `TestRefusedByRoute: discovered via litellm: RefusedByRoute = true, want false`, `TestResolveModelPinOnDiscoveredForcedThroughLitellm` (refused with "not in LiteLLM"), `TestResolveModelDiscoveredUnderLitellmNeverRotatedInto` (launchable has 1 model), `TestResolveModelSoleRunningDiscoveredAutoResolves` (litellm enabled: refused), `TestEligibilityIncludesDiscoveredUnderLitellm`, `TestRenderTableDiscoveredSelectableUnderLitellm` (`exception="(not in LiteLLM)"`).

- [ ] **Step 3: Implement**

`wt/internal/catalog/catalog.go` — replace `RefusedByRoute` and `RouteRefusal` (with comments) by:

```go
// RefusedByRoute reports whether the row must be refused because of how it
// routes: an Unmapped cloud model has no LiteLLM route, so it cannot be
// launched through the proxy. A discovered local model is never refused
// (#179 Phase B): wt routes it under its discovered id once it is running. It
// is false when the route failed to resolve (a launch row with a route error
// stays selectable and reports the error on Enter). The picker table, the
// non-TUI -M pin and `wt smoke` all decide through this one rule, and
// RouteRefusal gives them its one wording.
func (r Row) RefusedByRoute(route config.Route, routeErr error) bool {
	return r.Unmapped && routeErr == nil && (route.Litellm || route.Forced)
}

// RouteRefusal is RefusedByRoute's reason, or "" when the row is not refused.
func (r Row) RouteRefusal(route config.Route, routeErr error) string {
	if !r.RefusedByRoute(route, routeErr) {
		return ""
	}
	return fmt.Sprintf("cloud model %s is not in LiteLLM (provider %q has no LiteLLM mapping) — turn LiteLLM routing off (wt litellm off) to use it", r.Model.ID, r.Model.ProviderID)
}
```

`wt/cmd/wt/resolve.go` — in `resolveModel`'s comment change `minus discovered rows the picker refuses` to `minus Unmapped cloud rows the picker refuses`, and replace `pickerBlockedReason` and `launchableModels` (with comments) by the blocks below (only their comments and `launchableModels`' condition — now `if r.Unmapped && ...` — change):

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
	route, err := cfg.ResolveRoute(row.Model, agents.ProtocolsFor(agent))
	switch {
	case row.RefusedByRoute(route, err):
		return row.RouteRefusal(route, err)
	case err != nil && row.Action() == catalog.ActionStart:
		return row.Model.ID + " cannot be launched: " + err.Error()
	}
	return ""
}

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

`wt/internal/smoke/smoke.go` — in `walk` replace

```go
			// Keep the exact route rules the old loop applied: a discovered or
			// Unmapped row routed through LiteLLM is refused (smoke must not advertise what a
			// real launch refuses); a start row whose route errors is refused
			// like pickerBlockedReason does. A launch row with a route error
			// stays eligible (the picker reports it on Enter).
			if r.Discovered || r.Unmapped || act == catalog.ActionStart {
```

with

```go
			// The picker's route rules: an Unmapped row routed through
			// LiteLLM is refused (smoke must not advertise what a real launch
			// refuses); a start row whose route errors is refused like
			// pickerBlockedReason does. A launch row with a route error stays
			// eligible (the picker reports it on Enter). A discovered row is
			// never refused (#179 Phase B: wt routes it).
			if r.Unmapped || act == catalog.ActionStart {
```

and in `Eligibility`'s comment replace `or a local row the probe reports as running — plus discovered running\n// rows, which a -M pin can launch — minus discovered rows routed through\n// LiteLLM, which the picker makes unselectable and the CLI refuses. Because` with `or a local row the probe reports as running — discovered running rows\n// included, which a -M pin can launch — minus Unmapped cloud rows routed\n// through LiteLLM, which the picker makes unselectable and the CLI refuses.\n// Because`.

`wt/internal/tui/modeltable.go` — in `renderTable`'s `case r.RefusedByRoute(route, err):` replace the comment `A discovered or Unmapped model is not in LiteLLM's\n// model_list: routing it through the proxy cannot work, so it\n// is unselectable.` with `An Unmapped cloud model is not in LiteLLM's model_list:\n// routing it through the proxy cannot work, so it is\n// unselectable.` (the rest of the comment and the code are unchanged).

- [ ] **Step 4: Run — expect PASS**

Run: `cd wt && gofmt -l . && go vet ./... && go test ./...`
Expected: no gofmt output; PASS.

- [ ] **Step 5: Commit**

```bash
git add wt/internal/catalog wt/cmd/wt/resolve.go wt/cmd/wt/resolve_test.go wt/internal/smoke wt/internal/tui/modeltable.go wt/internal/tui/modeltable_test.go
git commit -m "feat(wt): stop refusing discovered models through LiteLLM (#179 phase B)

A discovered model is routed under its discovered id once it runs, so the
picker, the -M pin and wt smoke no longer refuse it; codex and every other
LiteLLM-forced agent can use it. Only an Unmapped cloud row is refused.
Rotation still never lands on a discovered row.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

- [ ] **Step 6: Verify the PR**

Run: `make test-all` (repo root)
Expected: PASS.

- [ ] **Step 7: Push and open PR 2**

```bash
git push -u origin feat/179-b2-routing
gh pr create --title "feat(wt): route discovered local models (#179 phase B, 2/4)" --body "$(cat <<'EOF'
Phase B, PR 2 of 4 (spec B2).

- `litellm.DiscoveredModel` + `prepareModel`: a model outside the registry gets a marked row under its discovered id; the ready gate and wt's whole modelman per-model reader are gone.
- `litellm.ApplyChange(Change{Add, Remove, RemoveFamilies})`: the lifecycle hooks route a discovered start and remove every marked route of a single-model family on start/stop (discovered siblings included); `familyModelIDs` is gone.
- `wt litellm sync` routes every running (or pulled ollama) model, registered or discovered; families whose probe cannot vouch for them are frozen (`Options.UntouchedFamilies`).
- A discovered route never replaces a hand-written row of the same name (e.g. `ollama/llama3.2:3b`).
- The "not in LiteLLM" refusal for discovered rows is removed; codex can launch a discovered model.
- Contract fixture: a discovered route in `sync --json`, asserted by both CI jobs.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

---

# PR 3 — modelman: stop auto-registering on start

Branch: `feat/179-b3-modelman` from `main` after PR 2 merges (wt must already route discovered models, or the start's sync would leave the model unrouted).

### Task 12: `modelman start <artifact>` runs an on-disk model without registering it; open PR 3

**Files:**
- Modify: `modelman/src/modelman/local_control.py` (delete `DiscoveredModelNeedsFamily` ~line 99, `_register_discovered_model` ~line 573, `_resolve_or_register` ~line 626; add `_discovered_entry`, `_resolve_local_model`, `_flagged_target`; `same_provider_occupant` ~line 300; `running_model_ids` ~line 750; `start_local_model` ~line 776; imports ~line 56)
- Modify: `modelman/src/modelman/main.py` (`start` command ~line 542; imports ~line 19)
- Modify: `modelman/src/modelman/screens/models.py` (~line 846 comment), `modelman/src/modelman/screens/forms.py` (`_submit_discovered` docstring ~line 1118)
- Test: `modelman/tests/test_local_control.py`, `modelman/tests/commands/test_local_control.py`

**Interfaces:**
- Consumes: `DiscoveredModel(provider_id, variant_id, path, size_bytes)`, `_find_discovered(registry, name)`, `_name_matches`, `ModelEntry`, `Fetch`, `LOCATION_LOCAL`; `sync_routes()` (one `wt litellm sync`).
- Produces: `def _discovered_entry(match: DiscoveredModel) -> ModelEntry`; `def _resolve_local_model(registry: Registry, model_id: str) -> ModelEntry`; `def start_local_model(registry: Registry, model_id: str, state_path: Path | None = None, *, litellm_path: Path | None = None) -> StartResult`; `def _flagged_target(model_id: str, models_by_id: dict[str, ModelEntry], providers_by_id: dict[str, ProviderEntry]) -> tuple[str, str] | None`; `running_model_ids` and `same_provider_occupant` (signatures unchanged) now cover flagged discovered ids. Removed: `DiscoveredModelNeedsFamily`, `_register_discovered_model`, `_resolve_or_register`, the `family` and `registry_path` keyword arguments.

- [ ] **Step 1: Write the failing tests**

`modelman/tests/test_local_control.py`: remove `DiscoveredModelNeedsFamily,` and `locked_registry,` from the imports. Delete `test_start_unregistered_name_with_no_family_raises_needs_family`, `test_start_unregistered_name_with_family_registers_and_starts`, `test_start_unregistered_name_registry_write_failure_raises_local_control_error` and `test_start_discovers_and_registers_omlx_artifact_resolvable_afterward`, and put in their place:

```python
def test_start_discovered_artifact_runs_without_registering(tmp_path, wt_calls):
    # #179 Phase B: an on-disk artifact with no registry.toml entry is started
    # as-is — no family prompt, no registry entry, no ready flag — and the
    # start's single `wt litellm sync` is what routes it, under the id wt's
    # catalog gives it (<provider>/<native name>). The running flag lives
    # under that same id so `modelman stop` can find it. Typing wt's id works
    # as well as the bare native name.
    registry = _registry()
    registry_path = tmp_path / "registry.toml"
    save_registry(registry, registry_path)
    before = registry_path.read_text()
    state_path = _state_path(tmp_path)
    mapping = {
        "ollama": [
            {"variant_id": "llama3.2:3b", "path": "ollama:llama3.2:3b", "size_bytes": 2_000_000_000}
        ]
    }

    for typed in ("llama3.2:3b", "ollama/llama3.2:3b"):
        with (
            _patch_provider_local_models(mapping),
            patch("modelman.local_control.isolate_provider") as mock_isolate,
        ):
            result = start_local_model(registry, typed, state_path)
        mock_isolate.assert_not_called()  # ollama is flag-only
        assert result.model_id == "ollama/llama3.2:3b"

    assert registry_path.read_text() == before
    assert [m.id for m in registry.models] == [m.id for m in _registry().models]
    model_state = load_state(state_path).get("ollama/llama3.2:3b")
    assert model_state.running is True
    assert model_state.ready is False
    # One sync per start: the second start found the flag and probed it, and
    # the idempotent path re-syncs too.
    assert [c for c in wt_calls if c[:1] == ["sync"]] == [["sync", "--json"]] * 2


def test_start_discovered_omlx_artifact_drives_the_provider_by_its_name(tmp_path):
    # The unsaved entry must carry enough for the provider to start the
    # artifact: omlx gets the on-disk name through its model env var, using
    # the real OMLXProvider listing (not the name-keyed stub) because omlx's
    # artifact names are directory basenames. registry.toml stays untouched.
    model_dir = _omlx_model_dir(tmp_path, basename="Qwen3.8-27B-4bit")
    registry = Registry(
        providers=[
            ProviderEntry(
                id="omlx",
                name="oMLX",
                location="local",
                model_dir=str(model_dir),
                auth=AuthConfig(type="none"),
            )
        ],
    )
    registry_path = tmp_path / "registry.toml"
    save_registry(registry, registry_path)
    state_path = _state_path(tmp_path)

    with (
        patch("modelman.local_control.isolate_provider") as mock_isolate,
        patch("modelman.local_control._probe_running", return_value=False),
    ):
        mock_isolate.return_value = IsolateResult(
            provider="omlx",
            model="Qwen3.8-27B-4bit",
            direct_url="http://localhost:8000/v1/chat/completions",
            ok=True,
            error=None,
        )
        result = start_local_model(registry, "Qwen3.8-27B-4bit", state_path)

    assert result.model_id == "omlx/Qwen3.8-27B-4bit"
    mock_isolate.assert_called_once_with(
        "omlx", env={"LLM_ISOLATE_OMLX_4BIT_MODEL": "Qwen3.8-27B-4bit"}, solo=True
    )
    assert load_registry(registry_path).models == []
    assert load_state(state_path).get("omlx/Qwen3.8-27B-4bit").running is True
```

Delete `test_register_discovered_model_persists_registry_before_syncing` and `test_register_discovered_model_refuses_an_id_that_already_exists` (both test the registration this task removes). Drop the now-unsupported `registry_path=registry_path` argument from the remaining `start_local_model` calls: in `test_start_unregistered_name_ambiguous_across_providers_raises` (`start_local_model(registry, "shared-name")`, and change its comment to `# A discovered name matching on-disk artifacts from two different\n    # providers is genuinely ambiguous — starting either one would silently\n    # guess wrong, so this must raise and ask for the full <provider>/<name>.`), in `test_start_native_name_resolves_existing_registered_model_without_reregistering` (`start_local_model(registry, "llama3.2:3b", state_path)`), in `test_start_omlx_directory_basename_resolves_the_registered_full_repo_entry` (delete the `registry_path=registry_path,` line), and in `test_find_discovered_omlx_basename_does_not_rediscover_registered_model` (`result = start_local_model(registry, "mlx-community/Qwen3.8-27B-4bit", state_path)`, and change its comment to `# The same join, exercised through start_local_model's resolution path:\n    # an already-registered omlx model must never be started as a\n    # discovered artifact just because its on-disk spelling differs.`).

Add `same_provider_occupant,` to the `from modelman.local_control import (...)` block (after `running_model_ids,`) and append the running-flag tests (Review Focus 5):

```python
def test_running_model_ids_verifies_a_discovered_model_by_probing_it(tmp_path):
    # #179 Phase B: a model started without a registry entry keeps its
    # running flag under its discovered id <provider>/<native name>. The
    # verified-running view must probe it like a registered model — the TUI
    # mount reconcile clears every flag this function does not return, so
    # skipping unregistered ids would wipe a live model's flag on every
    # mount. A dead one is cleared; an id whose provider is not in the
    # registry has nothing to probe and is not verified.
    state_path = _state_path(
        tmp_path,
        {"ollama/llama3.2:3b": True, "omlx/gone-4bit": True, "ghost/x": True},
    )
    probed: list[tuple[str, str]] = []

    def probe(provider_id, model_name, base):
        probed.append((provider_id, model_name))
        return model_name == "llama3.2:3b"

    with patch("modelman.local_control._probe_running", side_effect=probe):
        ids = running_model_ids(_registry(), load_state(state_path), state_path)

    assert ids == ["ollama/llama3.2:3b"]
    assert sorted(probed) == [("ollama", "llama3.2:3b"), ("omlx", "gone-4bit")]
    state = load_state(state_path)
    assert state.get("ollama/llama3.2:3b").running is True
    assert state.get("omlx/gone-4bit").running is False


def test_same_provider_occupant_sees_a_flagged_discovered_model(tmp_path):
    # Starting a registered omlx model replaces whatever omlx serves, and a
    # model started without a registry entry is flagged only under its
    # discovered id: the occupant lookup must find it, or its flag would
    # outlive the replacement.
    state_path = _state_path(tmp_path, {"omlx/stray-4bit": True})
    occupant = same_provider_occupant(_registry(), load_state(state_path), "omlx/model-a", "omlx")
    assert occupant == "omlx/stray-4bit"


def test_stop_all_stops_a_running_discovered_model(tmp_path, wt_calls):
    # `modelman stop --all` works off the flags, so a model started without a
    # registry entry is stopped and its flag cleared like any other, with the
    # one closing sync.
    state_path = _state_path(tmp_path, {"ollama/llama3.2:3b": True})
    with patch("modelman.local_control.stop_all_local_providers") as mock_stop_all:
        result = stop_all_local_models(state_path)
    mock_stop_all.assert_called_once()
    assert result.stopped == ["ollama/llama3.2:3b"]
    assert load_state(state_path).get("ollama/llama3.2:3b").running is False
    assert [c for c in wt_calls if c[:1] == ["sync"]] == [["sync", "--json"]]
```

`modelman/tests/commands/test_local_control.py`: replace `test_start_command_discovers_and_prompts_for_family` and `test_start_command_empty_family_reprompts` by:

```python
def test_start_command_starts_a_discovered_model_without_prompting(tmp_path, monkeypatch):
    # #179 Phase B: `modelman start <name>` against an on-disk artifact with
    # no registry.toml entry starts it as-is — no family prompt, nothing
    # registered — under the id wt routes it by (<provider>/<native name>).
    # The old prompt-and-register onboarding is gone: local models are
    # discovered, and an overlay is optional metadata added later.
    registry_path = tmp_path / "registry.toml"
    registry_text = (
        '[[providers]]\nid = "ollama"\nname = "Ollama"\nlocation = "local"\n'
        'auth = { type = "none" }\n\n'
    )
    registry_path.write_text(registry_text)
    state_path = tmp_path / "modelman.toml"
    litellm_path = tmp_path / "config.yaml"
    litellm_path.write_text("model_list: []\n")
    monkeypatch.setenv("MODELMAN_REGISTRY", str(registry_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    monkeypatch.setenv("MODELMAN_LITELLM_CONFIG", str(litellm_path))

    def get_class(name):
        return object if name == "ollama" else None

    def get(name, config):
        return _stub_provider(
            [
                {
                    "variant_id": "llama3.2:3b",
                    "path": "ollama:llama3.2:3b",
                    "size_bytes": 2_000_000_000,
                }
            ]
        )

    with (
        patch("modelman.local_control.ProviderRegistry.get_class", side_effect=get_class),
        patch("modelman.local_control.ProviderRegistry.get", side_effect=get),
        patch("modelman.local_control.isolate_provider") as mock_isolate,
    ):
        result = runner.invoke(app, ["start", "llama3.2:3b"], input="")

    assert result.exit_code == 0, result.output
    mock_isolate.assert_not_called()  # ollama is flag-only
    assert "Started ollama/llama3.2:3b." in result.stdout
    assert "isn't registered yet" not in result.output
    assert registry_path.read_text() == registry_text
    assert load_state(path=state_path).get("ollama/llama3.2:3b").running is True
```

- [ ] **Step 2: Run them — expect FAIL**

Run: `cd modelman && uv run pytest tests/test_local_control.py tests/commands/test_local_control.py -q`
Expected: FAIL — `test_start_discovered_artifact_runs_without_registering` raises `DiscoveredModelNeedsFamily`; the omlx and CLI tests fail the same way (the CLI prompts `isn't registered yet`); `test_running_model_ids_verifies_a_discovered_model_by_probing_it` returns `[]` (the unregistered id is skipped, never probed); `test_same_provider_occupant_sees_a_flagged_discovered_model` gets `None`. `test_stop_all_stops_a_running_discovered_model` already passes — it pins that the flag-driven stop-all keeps covering such ids.

- [ ] **Step 3: Implement**

`modelman/src/modelman/local_control.py`:
- delete the `DiscoveredModelNeedsFamily` class;
- delete `_register_discovered_model` and `_resolve_or_register`, and put in their place:

```python
def _discovered_entry(match: DiscoveredModel) -> ModelEntry:
    """An in-memory ModelEntry for an on-disk artifact with no registry.toml
    overlay, so start_local_model can drive its provider. It is never
    written to registry.toml (#179 Phase B: local models are discovered, not
    configured): its id is `<provider>/<native name>` — the id wt's catalog
    and LiteLLM route give the running model — and the start's one
    `wt litellm sync` routes it under that id. `fetch.repo` lets omlx, which
    derives its on-disk path from fetch rather than model_name, find the
    artifact; name-keyed providers (ollama, mtplx) never read it.
    """
    return ModelEntry(
        id=f"{match.provider_id}/{match.variant_id}",
        family="",
        provider_id=match.provider_id,
        model_name=match.variant_id,
        location=LOCATION_LOCAL,
        source="discovered",
        fetch=Fetch(repo=match.variant_id),
    )


def _resolve_local_model(registry: Registry, model_id: str) -> ModelEntry:
    """Resolve `model_id` to a ModelEntry, trying — in order — a registry
    id, an existing model's native provider-side name, and finally an
    on-disk artifact with no registry entry (an unsaved _discovered_entry;
    nothing is registered). Raises LocalControlError('unknown model: ...')
    if none match.
    """
    try:
        return registry.model(model_id)
    except KeyError:
        pass

    # _name_matches, not ==: the name a user types comes from the provider's
    # spelling (the discovered listing prints omlx's directory basename,
    # "Qwen3.8-27B-4bit") while model_name holds the registry's (the full repo
    # id, "mlx-community/Qwen3.8-27B-4bit"). An exact comparison missed that
    # model and fell through to the discovered-artifact step below, starting
    # an already-registered artifact under a second id.
    providers_by_id = _provider_by_id(registry)
    native_matches = [
        m
        for m in registry.models
        if _name_matches(m.model_name, model_id)
        and model_has_local_artifact(m, providers_by_id.get(m.provider_id))
    ]
    if len(native_matches) == 1:
        return native_matches[0]
    if len(native_matches) > 1:
        ids = ", ".join(sorted(m.id for m in native_matches))
        raise LocalControlError(
            f"{model_id!r} matches multiple registered models ({ids}) — use the full model id"
        )

    discovered = _find_discovered(registry, model_id)
    if not discovered:
        raise LocalControlError(f"unknown model: {model_id}")
    if len(discovered) > 1:
        providers = ", ".join(sorted(m.provider_id for m in discovered))
        raise LocalControlError(
            f"{model_id!r} matches on-disk models from multiple providers ({providers}) — "
            "use the full <provider>/<name> id"
        )
    return _discovered_entry(discovered[0])
```

- remove `known_families,` and `locked_registry,` from the `from .registry import (...)` block (no other use remains);
- make every running-flag consumer understand a discovered id. Replace the body of `same_provider_occupant` after its docstring with:

```python
    if provider_id == "ollama":
        return None
    slot_providers = _OMLX_PROVIDER_IDS if provider_id in _OMLX_PROVIDER_IDS else {provider_id}
    domain = {m.id for m in registry.models if m.provider_id in slot_providers}
    # A model started without a registry entry (#179 Phase B) is flagged
    # under its discovered id `<provider>/<name>`: it occupies the slot too.
    registered = {m.id for m in registry.models}
    domain |= {
        mid
        for mid in state.models
        if mid not in registered and mid.partition("/")[0] in slot_providers
    }
    return _same_provider_occupant(state, domain, exclude_model_id=model_id)
```

  (the name is `slot_providers` because the module already imports a `providers` package). Add directly above `running_model_ids`:

```python
def _flagged_target(
    model_id: str,
    models_by_id: dict[str, ModelEntry],
    providers_by_id: dict[str, ProviderEntry],
) -> tuple[str, str] | None:
    """(provider_id, model_name) to probe for a flagged model id: its
    registry entry's, or — for a model started without one (#179 Phase B,
    see _discovered_entry) — the `<provider>/<native name>` its discovered
    id spells, provided that provider is in the registry. None when the id
    names neither (nothing to probe, so the flag is not verified)."""
    model = models_by_id.get(model_id)
    if model is not None:
        return model.provider_id, model.model_name
    provider_id, _, model_name = model_id.partition("/")
    if model_name and provider_id in providers_by_id:
        return provider_id, model_name
    return None
```

  and in `running_model_ids` replace

```python
        model = models_by_id.get(model_id)
        if model is None:
            continue
        provider = providers_by_id.get(model.provider_id)
        probe_origin = base_origin(provider.auth.base_url) if provider and provider.auth else None
        if _probe_running(model.provider_id, model.model_name, probe_origin):
```

  with

```python
        target = _flagged_target(model_id, models_by_id, providers_by_id)
        if target is None:
            continue
        provider_id, model_name = target
        provider = providers_by_id.get(provider_id)
        probe_origin = base_origin(provider.auth.base_url) if provider and provider.auth else None
        if _probe_running(provider_id, model_name, probe_origin):
```

- in `start_local_model`, change the keyword parameters to `*, litellm_path: Path | None = None,` (drop `family` and `registry_path`); replace the docstring paragraph starting `model_id may be a registry id, an existing model's native` with

```python
    model_id may be a registry id, an existing model's native
    provider-side name, or the native name (or `<provider>/<name>` id) of
    an on-disk artifact with no registry.toml entry — see
    _resolve_local_model. The last case is started without registering it
    (#179 Phase B): its one `wt litellm sync` routes it under its
    discovered id, and its running flag is kept under that id.
```

  replace

```python
    state = load_state(state_path)
    model = _resolve_or_register(registry, state, model_id, family, registry_path, state_path)
    resolved_id = model.id
```

  with

```python
    model = _resolve_local_model(registry, model_id)
    resolved_id = model.id
```

  and replace the comment above `fresh_state = load_state(state_path)` (`# Re-read rather than reusing the state loaded above: _resolve_or_register() ...` — three lines) with

```python
    # Read as late as possible: a concurrent start/stop may have changed
    # flags while the provider checks above ran.
```

`modelman/src/modelman/main.py`: remove `DiscoveredModelNeedsFamily,` from the `from .local_control import (...)` block; in `start`'s docstring replace `disk but that has no registry.toml entry yet — the last case prompts\n    for a family, then registers and starts it in one step.` with `disk with no registry.toml entry — that one starts without being\n    registered (#179 Phase B), and wt routes it under its discovered id\n    `<provider>/<name>`.`; change the empty-inventory message to `"No local models found. `modelman start <name>` starts one it finds on disk."` and the discovered heading to `"Discovered (not in registry.toml — `modelman start <name>` runs one):"`; and replace the whole `family: str | None = None` / `while True:` retry loop with

```python
    try:
        # start_local_model owns the marker read/write (short locked_state
        # transactions around it); the stop-all/warmup subprocesses must run
        # outside any state lock.
        result = start_local_model(registry, model_id)
    except LocalControlError as exc:
        typer.echo(f"error: {exc}", err=True)
        raise typer.Exit(1) from exc
```

`modelman/src/modelman/screens/models.py`: in the comment above `model_state = ModelState(` (discovered-register path) delete the trailing sentence `Mirrors\n        # the CLI's equivalent path (local_control.py's\n        # _register_discovered_model), which writes through locked_state\n        # for the same reason.` so it ends at `an in-memory-only ready flag would be lost.`

`modelman/src/modelman/screens/forms.py`: in `_submit_discovered`'s docstring replace `Id/repo derivation mirrors local_control.py's\n        _register_discovered_model exactly, so a model registered from\n        the TUI resolves identically to one registered via `modelman\n        start <name>`.` with `The id spells a "/" in the reported name as "--"; fetch.repo is the\n        reported name, so omlx (which derives its on-disk path from\n        fetch.repo) can find the artifact again.`

- [ ] **Step 4: Run — expect PASS**

Run: `cd modelman && uv run pytest tests/test_local_control.py tests/commands/test_local_control.py -q && make check && make test`
Expected: PASS; `make check` clean (ruff, format, mypy).

- [ ] **Step 5: Commit**

```bash
git add modelman/src/modelman/local_control.py modelman/src/modelman/main.py modelman/src/modelman/screens/models.py modelman/src/modelman/screens/forms.py modelman/tests/test_local_control.py modelman/tests/commands/test_local_control.py
git commit -m "feat(modelman): start a discovered model without registering it (#179 phase B)

modelman start <name> on an on-disk artifact with no registry entry now
starts it from an unsaved entry — no family prompt, nothing written to
registry.toml — and its one wt litellm sync routes it under its
discovered id <provider>/<name>, under which its running flag is kept.
running_model_ids (and so the TUI mount reconcile) probes such a flag
instead of skipping it, and the occupant lookup counts it. Existing
source = \"discovered\" entries stay valid overlays.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

- [ ] **Step 6: Verify, push and open PR 3**

Run: `make test-all` (repo root)
Expected: PASS.

```bash
git push -u origin feat/179-b3-modelman
gh pr create --title "feat(modelman): stop auto-registering on start (#179 phase B, 3/4)" --body "$(cat <<'EOF'
Phase B, PR 3 of 4 (spec B3).

- `modelman start <artifact>` starts an on-disk model with no registry entry as-is: no family prompt, no registry.toml write; the start's single `wt litellm sync` routes it under its discovered id (wt PR 2).
- `_register_discovered_model`, `_resolve_or_register`, `DiscoveredModelNeedsFamily` and the `family`/`registry_path` parameters are removed; existing `source = "discovered"` entries stay valid overlays.
- A discovered model's running flag (under its discovered id) is verified by probing in `running_model_ids` (so the TUI mount reconcile keeps a live one and clears a dead one) and counted as a slot occupant.
- Deleting a local overlay does not change routing (pinned in wt: `TestSyncDeletedOverlayKeepsTheModelRouted`).

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

---

# PR 4 — Docs and guides

Branch: `docs/179-b4-guides` from `main` after PR 3 merges.

Docs tasks have no unit test; each verifies with `make check-links` plus a `grep` that the retired wording is gone. No live model state goes into `docs/guides/` (Global Constraints).

### Task 13: Guides 02, 03, 06, 08

**Files:**
- Modify: `docs/guides/02-providers-and-models.md`, `docs/guides/03-model-families.md`, `docs/guides/06-wt-agents-and-models.md`, `docs/guides/08-maintenance-and-troubleshooting.md`

**Interfaces:**
- Consumes: the behaviour shipped in PRs 1–3.
- Produces: user-facing docs for discovered local models and the overlay id convention.

- [ ] **Step 1: Guide 02 — overlays, routing, convention**

In the `> Use this to:` line, replace `Routing through the LiteLLM proxy on :4000 follows on its own: **add a model and it is routed** — a cloud model as soon as it is configured, a local model while it runs.` with `Routing through the LiteLLM proxy on :4000 follows on its own: **add a cloud model and it is routed**, and a local model is routed while it runs (an ollama model while it is pulled) — with or without a registry entry.`

In Step 3, directly after the paragraph that begins `**`id` is the route name; `model_name` is what the provider is asked for.**`, add:

```markdown
**A local registry entry is an optional overlay** (#179). wt lists the local models its live inventory finds — on disk, or running — whether or not `registry.toml` names them. An entry adds family, tags, cost and `model_info` to the model whose provider family and `model_name` match it; without one the model still appears in `wt`'s picker (status `new`, no family or tags) and is routed under its discovered id, `<provider>/<artifact>`. An entry whose artifact is not on disk is not listed by wt at all; modelman's TUI still lists it, so you can download it. Deleting a local entry does not change routing: a running model keeps its route under its discovered id. Convention for a new local entry: `id = "<provider>/<model_name>"` — the id wt already gives the model without an overlay, so its usage and survey history carries over. (An entry whose id spells a `/` as `--` — the shape the TUI's `+`-row form derives for repo-shaped names — works, but adding or removing it starts a separate history key.)
```

In Step 7's bullet list, replace `- **A local model** is routed while it runs; **an ollama model while it is pulled** (ollama loads it on the first request, so stopping it only unloads it and the route stays).` with `- **A local model** is routed while it runs; **an ollama model while it is pulled** (ollama loads it on the first request, so stopping it only unloads it and the route stays). That holds with or without a registry entry: a model with none is routed under its discovered id, `<provider>/<artifact>`.`

Replace the paragraph starting `**Taking a model off the proxy**` with `**Taking a model off the proxy**: a cloud model — delete it from the registry (TUI `d`) and the sync that closes the apply drops its row. A local model — stop it: on a single-model provider (omlx, mtplx, mlx_lm_server) the stop removes every wt-written route of that provider; a pulled ollama model keeps its route until the model is removed from ollama and a sync runs. Deleting a local registry entry alone does not unroute a running model. A row you wrote by hand in `config.yaml` is never removed or rewritten, even when its name equals a discovered model's id — delete it yourself if you want wt's row instead.`

After the `uv run modelman start ollama/gpt-oss:20b` example block in Step 7 (the one followed by `Started ollama/gpt-oss:20b.`), add: `` `modelman start <name>` also takes the native name (or `<provider>/<name>` id) of an on-disk model with no registry entry: it starts it without registering it, and the sync routes it under that id. ``

- [ ] **Step 2: Guide 03 — filters hide discovered rows**

In §3, after the bullet `- neither set → **no filter** (the full agent-eligible catalog is listed);`, add `- a discovered local model (on disk, no registry entry) has no family or tags, so any `-T`/`-F` hides it; with no filter every on-disk model is listed;`.

In §4, append to the paragraph starting `` `wt` picks worktree → agent → model from the joined catalog `` the sentence: `Local rows come from wt's live inventory, not from the registry: a local entry only adds family, tags and cost to a model wt finds on disk or running, and an entry whose artifact is missing is not offered (see [02-providers-and-models](02-providers-and-models.md) Step 3).`

- [ ] **Step 3: Guide 06 — STATUS, blocked rows, discovered routing**

In the long `The tag slot shows ...` bullet, replace `STATUS is `ok`, `absent` (the provider answered and does not have the model), `unknown` (the probe could not tell — a failed local probe, a model it has no entry for, or a non-running `mlx_lm_server` row), or `new` (discovered, not in the registry); RUNNING is `run` while a model is serving.` with `STATUS is `ok`, `unknown` (the probe could not tell — a failed local probe, so wt lists the model rather than hide it), or `new` (discovered, not in the registry); RUNNING is `run` while a model is serving. A local model that is not on disk has no row (modelman's TUI still lists it as downloadable), and a non-running `mlx_lm_server` pairing is not listed. A discovered model is routed like any other running local model — under its discovered id — so it stays selectable with LiteLLM routing on.`

Replace the bullet starting `- **Rows that cannot start**` with `- **Rows that cannot start** say why instead of starting: a local provider wt has no lifecycle backend for (`local model "<id>" is not running — start it with modelman start <id>`). A registry model that is not on disk has no row; pinning it (`-M`, `wt start`, `wt smoke`) says `<id> is not on disk — pull or download it first` (starting it would just wait out the warmup timeout).`

- [ ] **Step 4: Guide 08 — troubleshooting**

In Step 3's `- **Local model** (omlx/mtplx/mlx_lm_server)` bullet, replace `A model *missing from wt's picker* is a different question: run `wt -A <agent>` and read the STATUS/RUNNING columns — wt probes ollama/omlx/mtplx live, and shows a non-running local model as a start row.` with `A model *missing from wt's picker* is a different question: wt lists only what its live probes find, so a local model that is not on disk is not listed at all — download it (modelman TUI `r`, or `ollama pull`). Run `wt -A <agent>` and read the STATUS/RUNNING columns: a non-running local model on disk is a start row, and a model with no registry entry is listed as `new`.`

In Step 9, replace `routes exactly the local models that are *running* at that moment — plus, for ollama, every registry model that is *pulled* —` with `routes exactly the local models that are *running* at that moment — plus, for ollama, every model that is *pulled*, registered or not —`, and replace `` `wt litellm sync` skips the ready gate entirely and `` with `` `wt litellm sync` has no ready gate and ``.

At the end of Step 9 (after `(Nothing in `modelman.toml` records routing, so there is no flag that could disagree with sync.)`), add: `A `(hand-written)` row in `wt litellm list` whose name equals a discovered model's id (an alias you wrote before #179, say) keeps serving that name: wt never replaces it, so delete the row from `config.yaml` if you want wt's marked row, then run `wt litellm sync`.`

- [ ] **Step 5: Verify**

Run: `make check-links`
Expected: PASS.
Run: `grep -n "absent\` (the provider answered\|not in LiteLLM\|skips the ready gate\|every registry model that is \*pulled\*" docs/guides/0[2368]-*.md`
Expected: no output.

- [ ] **Step 6: Commit**

```bash
git add docs/guides/02-providers-and-models.md docs/guides/03-model-families.md docs/guides/06-wt-agents-and-models.md docs/guides/08-maintenance-and-troubleshooting.md
git commit -m "docs(guides): local models are discovered; registry entries are overlays (#179 phase B)

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 14: Package docs (`wt/CLAUDE.md`, `modelman/CLAUDE.md`, wt command docs); open PR 4

**Files:**
- Modify: `wt/CLAUDE.md` (Go tests §, Go module table rows for `internal/catalog` and `internal/litellm`, Registry § catalog paragraph, Local-model resolution §, Lifecycle route-hook bullet, LiteLLM routes gotchas)
- Modify: `modelman/CLAUDE.md` (Start and Discovery bullets)
- Modify: `wt/docs/wt-start-stop.md`, `wt/docs/wt-smoke.md`, `wt/docs/wt-agents/README.md`

**Interfaces:**
- Consumes: the behaviour shipped in PRs 1–3.
- Produces: package docs that match it.

- [ ] **Step 1: `wt/CLAUDE.md`**

> **Already landed in PR 1 (its code review, commit `a1f7e49`) — do not redo:** the two `TestMain` bullets (they name `localmodels.OnDiskSnapshotForTest`, the shared stub that replaced `onDiskSnapshot`), the `internal/catalog/` table row, the **rows**/**launch**/**start** bullets, the `-M` pin paragraph, and the `localRows` bullet in Start/stop §. The edits below are what is left; each "replace" quotes the text as PR 1 left it, so match on that.

Go module table: the `internal/litellm/` row becomes `| `internal/litellm/` | the sole implementation of LiteLLM `config.yaml` route management (replaced modelman's Python writer): `configfile.go`, `entry.go`/`policy.go` (entries, provider mappings), `service.go` (`ApplyChange` — the targeted route writer the lifecycle hook uses, with `Change.RemoveFamilies`; `Apply` its id-based form; `DiscoveredModel`, `RowFamily`; sync), `restart.go` (`RestartContext`, `Listening`, `WaitReady`) |`.

Registry §: replace `(from modelman.toml's `[model_state]` it reads only `ready`, legacy `downloaded`)` with `(from modelman.toml it reads only `price_refresh_last_run` and the legacy `[litellm]` table — no `[model_state]` key at all, #179 Phase B)`; in the **Catalog membership** paragraph replace `modelman's `exposed`/`ready` flags no longer gate wt's lists.` with `modelman's `exposed`/`ready` flags no longer gate wt's lists, and a local model's *row* additionally needs the live inventory to find it (see Local-model resolution).`

Local-model resolution §, the **rows** bullet: replace its last sentence `A hidden registry id still reserves its id, so an unregistered artifact spelling the same id is not listed under it.` with `A registry entry is an overlay matched by family + `model_name` (`inventory.go`); unmatched artifacts are `new` rows under `config.DiscoveredModelID`, hidden whenever `-T`/`-F` is set. A hidden registry id still reserves its id, so an unregistered artifact spelling the same id is not listed under it.`

The **block** bullet: replace `Separately, a row LiteLLM cannot serve — a discovered model, or an `Unmapped` cloud model (its provider has no `PolicyFor` mapping, so sync never routes it) — is refused ("not in LiteLLM") when its route goes through the proxy.` with `Separately, an `Unmapped` cloud model (its provider has no `PolicyFor` mapping, so sync never routes it) is refused ("not in LiteLLM") when its route goes through the proxy; a discovered model is never refused — it is routed under its discovered id.`, and delete the bullet's closing sentence `Direct (LiteLLM off) such rows launch normally.` Leave the bullet's opening (`a provider with no start engine such as `mlx_lm_server``) as it is.

In the **Non-TUI** paragraph, change `with no `-M`, only launch rows are eligible (`launchableModels`) — rotation never hands an agent a server that is not up, and a start row is never auto-selected; only a pin may start one.` to `with no `-M`, only launch rows are eligible (`launchableModels`) — rotation never hands an agent a server that is not up, a start row is never auto-selected (only a pin may start one), and rotation (`NextFromEligible`, over `cfg.Models`) never lands on a discovered row.`

Lifecycle §, the **Route hook** bullet: replace `single-model providers (omlx, mtplx) replace sibling routes on start and drop all family routes on stop; ollama adds one on start;` with `a started model is routed under its registry overlay's id, else its discovered id (`litellm.DiscoveredModel`); single-model providers (omlx, mtplx) remove every marked route of the family (`Change.RemoveFamilies` — discovered siblings included, plus the family's registry ids; never a hand-written row) on start (minus the started id) and on stop; ollama adds one on start;`.

LiteLLM routes §, first gotcha: replace `Desired = every `CloudModels` id plus the running registry local models;` with `Desired = every `CloudModels` id plus every running (or pulled ollama) local model, registered or discovered (`desiredLocalModels`; a discovered one as `litellm.DiscoveredModel`);`, and append to that bullet: `A desired discovered id that already has an unmarked row is left to that row (no add, no adoption) — on the start path too (`ApplyChange`).` Second gotcha: replace `A family whose probe is `partial` (e.g. omlx/mtplx `/v1/models` failing) is left alone with a warning,` with `A family whose probe is neither `ok` nor refused is frozen — its registry ids via `Options.Untouched` (with a warning) and every row whose `litellm.RowFamily` is that family, discovered routes included, via `Options.UntouchedFamilies` (`untrustedFamilies`; the same warning when `config.yaml` holds a marked row of a family with no registry model);`.

- [ ] **Step 2: `modelman/CLAUDE.md`**

In the **Start** bullet, replace `or a discovered artifact's native name, which is auto-registered (and so routed) after an interactive family prompt (`_resolve_or_register` raises `DiscoveredModelNeedsFamily` before writing anything; the CLI prompts and retries)` with `or a discovered artifact's native name (or wt's `<provider>/<name>` id), which is started from an unsaved `_discovered_entry` — nothing is registered, no family is asked; the closing sync routes it under that discovered id and its running flag is kept under the same id (`_resolve_local_model`, #179 Phase B); `running_model_ids` probes such a flag through `_flagged_target` and `same_provider_occupant` counts it`. In the **Discovery** bullet, change `(`modelman start` no-arg inventory, auto-register, TUI `+` rows)` to `(`modelman start` no-arg inventory, discovered starts, TUI `+` rows)`.

- [ ] **Step 3: wt command docs**

`wt/docs/wt-start-stop.md`: the picker description and the "Not on disk" / "No lifecycle backend" bullets already landed in PR 1. In `## wt start [model]`, add one bullet after the `- No lifecycle backend (mlx_lm_server): …` bullet and before `- Cloud or unknown id: exits 1.`:

```markdown
- A started model is routed under its registry id, or its discovered id
  when it has no registry entry; stopping an omlx/mtplx model removes
  every wt-written route of that provider.
```

`wt/docs/wt-smoke.md`: change `` - `model-id` — a registry model id (`provider/name`). `` to `` - `model-id` — a model id (`provider/name`): a registry id, or a discovered model's id. ``, and in `## Eligibility` (PR 1 already dropped "not on disk" from the blocked-row list) replace `Blocked rows — no lifecycle backend, not in LiteLLM — are excluded, and a\nlocal model that is not on disk has no row; passing either by id names the\nreason (not on disk, no lifecycle backend) as `wt start` does,` with `Rows that cannot run — no lifecycle backend, or an `Unmapped` cloud model\nrouted through LiteLLM — are excluded, and a local model that is not on disk\nhas no row; passing either by id names the reason (not on disk, no\nlifecycle backend) as `wt start` does,` and add after that paragraph: `A discovered model (no registry entry) is eligible like any other: wt routes it under its discovered id, so LiteLLM-forced agents (codex) can run it.`

`wt/docs/wt-agents/README.md`: change `wt litellm sync [--dry-run]       # route every registry cloud model + the running local models` to `wt litellm sync [--dry-run]       # route every registry cloud model + every running (or pulled ollama) local model`, and change `local models are listed from live\nprovider probes, and their routes are added and removed automatically by` to `local models are listed from live\nprovider probes — with or without a registry entry (an unregistered model is\nrouted under its discovered id) — and their routes are added and removed automatically by`.

- [ ] **Step 4: Verify**

Run: `make check-links`
Expected: PASS.
Run: `grep -n "ready gate\|is a no-op;\|auto-registered\|DiscoveredModelNeedsFamily\|not in LiteLLM —" wt/CLAUDE.md modelman/CLAUDE.md wt/docs/wt-start-stop.md wt/docs/wt-smoke.md wt/docs/wt-agents/README.md`
Expected: no output.

- [ ] **Step 5: Commit**

```bash
git add wt/CLAUDE.md modelman/CLAUDE.md wt/docs/wt-start-stop.md wt/docs/wt-smoke.md wt/docs/wt-agents/README.md
git commit -m "docs: local models are discovered in wt and modelman notes (#179 phase B)

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

- [ ] **Step 6: Verify, push and open PR 4**

Run: `make test-all` (repo root)
Expected: PASS.

```bash
git push -u origin docs/179-b4-guides
gh pr create --title "docs: local models are discovered (#179 phase B, 4/4)" --body "$(cat <<'EOF'
Phase B, PR 4 of 4 (spec B4 docs).

- Guides 02, 03, 06, 08: local models come from wt's live inventory; a local registry entry is an optional overlay (convention `id = "<provider>/<model_name>"`); absent entries are hidden in wt but downloadable in modelman; discovered models are routed under their discovered id; hand-written rows are never replaced.
- `wt/CLAUDE.md`, `modelman/CLAUDE.md`, `wt-start-stop.md`, `wt-smoke.md`, `wt-agents/README.md` updated to match.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

---

### Task 15: Phase B acceptance on the reference host (user approval required)

**Files:** none (verification only; anything that writes `~/.config`, `~/.local/bin` or starts a model needs the user's explicit go-ahead first).

**Interfaces:** none.

- [ ] **Step 1: Install the merged build** — *needs approval (writes `~/.local/bin`)*

Run (repo root, `main` after PR 4): `make install`
Expected: `wt` and modelman rebuilt; `wt --version` prints the new build.

- [ ] **Step 2: Back up `config.yaml` and review the plan** — read-only plus one copy

```bash
cp ~/.config/litellm/config.yaml ~/.config/litellm/config.yaml.pre-179b
wt litellm sync --dry-run
wt litellm list
```

Expected: the dry run proposes no `would unroute` for any `(hand-written)` row (`ollama/q8`, `ollama/o35`, `ollama/llama3.2:3b`, the four `openrouter/qwen/*`), and no `would adopt` for `ollama/llama3.2:3b`. The user reviews the plan before Step 3.

- [ ] **Step 3: Acceptance 1 — `wt` lists exactly what is on disk** — *needs approval for the real sync*

```bash
ollama list
ls ~/.omlx/models ~/.mtplx/models
wt start            # TTY picker: compare its rows with the two listings above, then cancel
```

Expected: every listed artifact has a row (registry id when an overlay matches, else `<provider>/<artifact>` with status `new`); no not-downloaded ollama entry appears. Then `uv run --directory modelman modelman` (TUI): those not-downloaded entries are still listed there. With approval, run `wt litellm sync` and confirm `wt litellm list` matches the reviewed plan.

- [ ] **Step 4: Acceptance 2 — `wt smoke` with a discovered ollama model and a two-slash id, every agent** — *needs approval (starts models)*

```bash
wt smoke <discovered-ollama-id>                 # e.g. an ollama model with no registry entry, from Step 3
wt smoke <two-slash-mtplx-or-omlx-id>           # e.g. mtplx/<org>/<model> from Step 3
```

Expected: PASS for every eligible agent, codex included (it is forced through LiteLLM). If an agent fails only for lack of a capability flag (`supports_function_calling`/`supports_vision`), record it and open the follow-up for per-provider defaults in `PolicyFor` (Design notes); do not make overlays mandatory.

- [ ] **Step 5: Acceptance 3 — stopping mtplx removes its marked route; hand-written rows survive** — *needs approval*

```bash
wt start <mtplx-id>
wt litellm list | grep mtplx            # the started id, no "(hand-written)" suffix
wt stop mtplx
wt litellm list                          # no mtplx row; every (hand-written) row from Step 2 still present
```

- [ ] **Step 6: Acceptance 4 — history under existing ids** — read-only

Run: `wt stats`
Expected: rows for the existing registry ids carry their pre-Phase-B history (no reset; a `--`-style id whose overlay was untouched keeps its key).

- [ ] **Step 7: Acceptance 5**

Run: `make test-all`
Expected: PASS. Then remove the backup only if the user asks: `rm ~/.config/litellm/config.yaml.pre-179b`.

## Follow-ups (not in this plan)

- modelman TUI `+`-row register form (`screens/forms.py` `_submit_discovered`) still derives `<provider>/<name with "/" as "--">` ids, against the documented `provider/model_name` overlay convention (controller ruling: out of Phase B scope).
- Capability flags for discovered rows (D2.10): if acceptance B-2 finds an agent needing `supports_function_calling`/`supports_vision`, add per-provider defaults in `PolicyFor`; never make overlays mandatory.
- Phase A register #17: TUI add → start → Discard sequence (left open).
- Phase A register #18: an unpulled ollama start gives no warning — moot for wt rows (absent rows are hidden) but `modelman start ollama/x` still has it.
- Phase A register #19a/b: mlx_lm_server running-model identification (untouched by Phase B).
