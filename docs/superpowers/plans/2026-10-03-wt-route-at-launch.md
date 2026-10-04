# wt Route at Launch Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Launching a running local model through wt writes its LiteLLM route when the route is missing, so a model wt did not start no longer answers `Invalid model name`.

**Architecture:** One ensure-only entry point in `internal/lifecycle` writes the same `litellm.Change` the start hook writes, and never starts or stops a model. The non-TUI launch, the TUI launch, `wt smoke` and `wt start <running id>` call it and then wait for the proxy. `lifecycle.Target` gains the catalog row's model id so the route is written under the id the picker showed.

**Tech Stack:** Go 1.26.7, module `github.com/ohanaverse/local-ai-setup/wt`. Bubble Tea for the TUI. Tests use package-level function seams, no mocks library.

**Spec:** `docs/superpowers/specs/2026-10-03-wt-route-at-launch-design.md`

## Global Constraints

- All work is in `wt/` plus docs. modelman is not touched.
- Run Go commands from `wt/`. Full check before the PR: `make test-all` from the repo root.
- **No test may write the developer's real `~/.config/litellm/config.yaml` or restart the real proxy.** `cmd/wt` and `internal/tui` each get a `TestMain` default stub for the new seam in the same commit that adds the seam (Tasks 3 and 4). Existing tests already launch a running local model through LiteLLM and would otherwise hit the real file.
- The ensure never fails a launch. Every error is a warning on stderr.
- The ensure never starts or stops a model. Do not call `lifecycle.Start` for a running row.
- A hand-written (unmarked) `config.yaml` row is never replaced. `litellm.ApplyChange` enforces this; add no second rule.
- At most one proxy restart per launch, and none when the route is present.
- The changed-route message is exactly: `wt: LiteLLM route for <id> updated`.
- Guides in `docs/guides/` stay state-independent: commands that reveal state, no model inventories.
- Match the surrounding comment density. This codebase explains *why* in comments above each seam and each non-obvious call.
- Commit messages end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Do not push or open a PR without the user's OK.

## Review Focus

1. **LiteLLM was never set up (`config.yaml` missing).** Launching a running local model prints nothing and proceeds. Pinned in Task 2 (`TestEnsureRouteSilentWhenConfigMissing`).
2. **Another wt process holds the `config.yaml` lock.** The ensure gives up after `ensureRouteLockTimeout` and the launch proceeds. Pinned in Task 2 (`TestEnsureModelRouteBoundsTheLockWait`).
3. **A local model whose location cannot be resolved (provider row missing or mistyped).** The ensure skips it without a write or a panic. Pinned in Task 2 (`TestEnsureModelRouteSkipsNonLocal`).
4. **A discovered model whose name already has a hand-written row.** Nothing is written and no "updated" line prints. Pinned in Task 2 (`TestEnsureRouteQuietWhenNothingChanged`).
5. **An artifact whose name fuzzy-matches a different registry model.** The route is written under the row's own id, not the registry model's. Pinned in Task 1 (`TestStartRouteChangeUsesTheRowsModelID`).

## File Structure

| File | Change | Responsibility |
|---|---|---|
| `wt/internal/lifecycle/lifecycle.go` | modify | `Target` gains `ModelID` |
| `wt/internal/lifecycle/routes.go` | modify | id-first model resolution; `EnsureRoute`, `EnsureModelRoute` |
| `wt/internal/lifecycle/routes_test.go` | modify | tests for the above |
| `wt/cmd/wt/start.go` | modify | `Target` carries the id; `ensureModelRoute` seam; `ensureRouteBeforeLaunch` |
| `wt/cmd/wt/launch.go` | modify | non-TUI launch calls the ensure |
| `wt/cmd/wt/model_cmds.go` | modify | `wt start <running id>` repairs the route |
| `wt/cmd/wt/smoke.go` | modify | smoke ensures a running target's route |
| `wt/cmd/wt/testmain_test.go` | modify | default stub + `stubEnsureRoute` helper |
| `wt/cmd/wt/{launch,model_cmds,smoke,litellm}_test.go` | modify | tests |
| `wt/internal/tui/start_flow.go` | modify | `Target` carries the id; seam; `ensureLaunchRoute` |
| `wt/internal/tui/app.go` | modify | `proceedToLaunch` calls the ensure |
| `wt/internal/tui/testhelpers_test.go` | modify | default stub + `stubEnsureRoute` helper |
| `wt/internal/tui/start_flow_test.go` | modify | tests |
| docs (Task 5) | modify | guides 04 and 08, `wt-start-stop.md`, both `CLAUDE.md`, `CHANGELOG.md` |

---

### Task 1: `Target.ModelID` and id-first route resolution

**Files:**
- Modify: `wt/internal/lifecycle/lifecycle.go:14`
- Modify: `wt/internal/lifecycle/routes.go:85-100` (`StartRouteChange`)
- Modify: `wt/cmd/wt/start.go:150`
- Modify: `wt/internal/tui/start_flow.go:118`
- Test: `wt/internal/lifecycle/routes_test.go`, `wt/cmd/wt/litellm_test.go:1203`, `wt/internal/tui/start_flow_test.go:193`

**Interfaces:**
- Consumes: `litellm.ModelFor(cfg, providerID, modelName) (config.Model, bool)`, `litellm.DiscoveredModel(providerID, artifact) config.Model`, `config.IndexModelByID(models, id) int`.
- Produces: `lifecycle.Target{ProviderID, ModelName, ModelID string}`. `StartRouteChange(cfg, t)` keeps its signature.

**Background.** `StartRouteChange` picks the model to route with `litellm.ModelFor`, which falls back to a lenient name match (`localmodels.NameMatches`: a path- or org-prefixed spelling matches). When an artifact `org/Y/Q35` exists beside a registry model named `Y/Q35`, the inventory lists the artifact under its own discovered id, but the fuzzy fallback resolves it to the registry model. The picker and the hook then disagree on the id.

- [ ] **Step 1: Write the failing tests**

Append to `wt/internal/lifecycle/routes_test.go`:

```go
// TestStartRouteChangeUsesTheRowsModelID pins #195: an artifact whose name
// fuzzy-matches a registry model ("org/Y/Q35" ends in "/Y/Q35") is a distinct
// model with its own discovered id. With the row's id carried in Target the
// route is written under that id; the lenient ModelFor fallback would route
// the registry model mtplx/Y--Q35, so the picker and the hook would disagree.
func TestStartRouteChangeUsesTheRowsModelID(t *testing.T) {
	cfg := routesCfg()
	ch := StartRouteChange(cfg, Target{ProviderID: "mtplx", ModelName: "org/Y/Q35", ModelID: "mtplx/org/Y/Q35"})
	if len(ch.Add) != 1 || ch.Add[0].ID != "mtplx/org/Y/Q35" {
		t.Fatalf("add = %+v, want the discovered id mtplx/org/Y/Q35", ch.Add)
	}
	if !slices.Equal(ch.RemoveFamilies, []string{"mtplx"}) {
		t.Errorf("families = %v, want the single-model family cleared", ch.RemoveFamilies)
	}
}

// TestStartRouteChangeExactRegistryID pins the other half: a ModelID that IS
// a registry id routes that registry model, whatever its ModelName spelling.
func TestStartRouteChangeExactRegistryID(t *testing.T) {
	ch := StartRouteChange(routesCfg(), Target{ProviderID: "mtplx", ModelName: "Y/Q35", ModelID: "mtplx/Y--Q35"})
	if len(ch.Add) != 1 || ch.Add[0].ID != "mtplx/Y--Q35" {
		t.Fatalf("add = %+v, want the registry model mtplx/Y--Q35", ch.Add)
	}
}

// TestStartRouteChangeWithoutModelIDKeepsTheFallback pins that a Target built
// without an id resolves as before: ModelFor, then the discovered model.
func TestStartRouteChangeWithoutModelIDKeepsTheFallback(t *testing.T) {
	ch := StartRouteChange(routesCfg(), Target{ProviderID: "mtplx", ModelName: "org/Y/Q35"})
	if len(ch.Add) != 1 || ch.Add[0].ID != "mtplx/Y--Q35" {
		t.Fatalf("add = %+v, want the fuzzy-matched registry model", ch.Add)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `cd wt && go test ./internal/lifecycle -run 'TestStartRouteChange' `
Expected: build failure, `unknown field ModelID in struct literal of type Target`.

- [ ] **Step 3: Add the field**

In `wt/internal/lifecycle/lifecycle.go`, replace line 14 (`type Target struct{ ProviderID, ModelName string }`) and keep whatever doc comment sits above it:

```go
// ModelID is the catalog row's id (a registry id or a discovered id). The
// route hook writes the route under it, so the id in config.yaml is the one
// the picker showed. It may be empty: the hook then derives the model from
// ProviderID and ModelName (see StartRouteChange).
type Target struct{ ProviderID, ModelName, ModelID string }
```

- [ ] **Step 4: Resolve by id first**

In `wt/internal/lifecycle/routes.go`, replace the body of `StartRouteChange` and add `routeModel` below it. Extend the existing doc comment on `StartRouteChange` with the last paragraph shown:

```go
// StartRouteChange is the route change routeAfterStart writes for a started
// target: the model to route (registry overlay, else discovered) and, for a
// single-model provider, its family to clear. It is exported so the id the
// start hook writes can be pinned against the id `wt litellm sync` desires
// for the same model (cmd/wt) — if the two derivations drift, every sync
// after a start removes the hook's route and adds its own.
func StartRouteChange(cfg *config.Config, t Target) litellm.Change {
	ch := litellm.Change{Add: []config.Model{routeModel(cfg, t)}}
	if SingleModel(t.ProviderID) {
		ch.RemoveFamilies = []string{localmodels.Family(t.ProviderID)}
	}
	return ch
}

// routeModel is the model a target's route is written for. A target that
// carries its row's id is resolved by that id alone: the registry model with
// exactly that id, else the discovered model. ModelFor's lenient name match is
// skipped on purpose — an artifact whose name merely resembles a registry
// model's ("org/name" beside "name") has its own discovered id, and matching
// it to the registry model would route an id the picker never showed (#195).
// Only a target with no id falls back to ModelFor.
func routeModel(cfg *config.Config, t Target) config.Model {
	if t.ModelID != "" {
		if i := config.IndexModelByID(cfg.Models, t.ModelID); i >= 0 {
			return cfg.Models[i]
		}
		return litellm.DiscoveredModel(t.ProviderID, t.ModelName)
	}
	if m, ok := litellm.ModelFor(cfg, t.ProviderID, t.ModelName); ok {
		return m
	}
	return litellm.DiscoveredModel(t.ProviderID, t.ModelName)
}
```

- [ ] **Step 5: Run the lifecycle tests**

Run: `cd wt && go test ./internal/lifecycle`
Expected: PASS, including every pre-existing `TestRouteAfterStart*` test (they build `Target` without an id and take the fallback).

- [ ] **Step 6: Set the id in both production constructors**

`wt/cmd/wt/start.go:150`:

```go
	target := lifecycle.Target{ProviderID: row.Model.ProviderID, ModelName: row.Model.ModelName, ModelID: row.Model.ID}
```

`wt/internal/tui/start_flow.go:118`, inside `beginStart`:

```go
	ch := runStart(ctx, m.cfg, lifecycle.Target{ProviderID: it.model.ProviderID, ModelName: it.model.ModelName, ModelID: it.model.ID}, allowReplace, id)
```

- [ ] **Step 7: Update the two tests that pin the old `Target` shape**

`wt/internal/tui/start_flow_test.go:193` compares the whole struct. Change the expected value to include the id of the row that test starts:

```go
	if c.target != (lifecycle.Target{ProviderID: "ollama", ModelName: "gemma4:9b", ModelID: "ollama/gemma4:9b"}) {
```

`wt/cmd/wt/litellm_test.go:1203` (`TestStartHookAndSyncAgreeOnDiscoveredRoute`) builds the `Target` the start paths hand the hook. Make it the same shape production now builds, and update the sentence in the test's doc comment that says the start paths hand the hook `Target{row.Model.ProviderID, row.Model.ModelName}` to name all three fields:

```go
	ch := lifecycle.StartRouteChange(cfg, lifecycle.Target{ProviderID: row.Model.ProviderID, ModelName: row.Model.ModelName, ModelID: row.Model.ID})
```

- [ ] **Step 8: Run all wt tests**

Run: `cd wt && go build ./... && go vet ./... && go test ./...`
Expected: PASS. `TestStartHookAndSyncAgreeOnDiscoveredRoute` still passing proves the hook and sync agree on the id with `ModelID` set.

- [ ] **Step 9: Commit**

```bash
git add wt/internal/lifecycle wt/cmd/wt/start.go wt/cmd/wt/litellm_test.go wt/internal/tui/start_flow.go wt/internal/tui/start_flow_test.go
git commit -m "feat(wt): the route hook writes the route under the row's model id (#192, #195)

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: `lifecycle.EnsureRoute` and `EnsureModelRoute`

**Files:**
- Modify: `wt/internal/lifecycle/routes.go`
- Test: `wt/internal/lifecycle/routes_test.go`

**Interfaces:**
- Consumes: `StartRouteChange(cfg, t) litellm.Change` and `Target.ModelID` from Task 1; the unexported `applyAndReport(ctx, cfg, ch, mode) bool`, `restartIfChanged`, `routesWarn`.
- Produces:
  - `func EnsureRoute(ctx context.Context, cfg *config.Config, t Target) bool` — writes the route if missing; reports whether `config.yaml` changed.
  - `func EnsureModelRoute(cfg *config.Config, m config.Model) bool` — the form callers use: skips a non-local model, bounds the lock wait, builds the `Target`.
  - Neither waits for the proxy. The caller calls `lifecycle.WaitPendingRoutes()`.

**Background.** `applyAndReport` already writes synchronously, warns on stderr for every failure except a missing `config.yaml`, and restarts the proxy on a tracked goroutine only when the file changed. `stubRoutes(t, res, err)` in `routes_test.go` replaces the seams and records each write as a `routeCall{add, remove, families}`.

- [ ] **Step 1: Write the failing tests**

Append to `wt/internal/lifecycle/routes_test.go`:

```go
// TestEnsureRouteWritesTheStartHooksChange pins that the launch-time ensure
// and the start hook write the same change: the model's route and, for a
// single-model provider, its family cleared. It also pins the one line a
// changed write prints.
func TestEnsureRouteWritesTheStartHooksChange(t *testing.T) {
	calls, warn := stubRoutes(t, litellm.Result{Changed: true}, nil)
	changed := EnsureRoute(context.Background(), routesCfg(), Target{ProviderID: "mtplx", ModelName: "Y/Q35", ModelID: "mtplx/Y--Q35"})
	WaitPendingRoutes()
	if !changed {
		t.Fatal("changed = false, want true when the write changed config.yaml")
	}
	if len(*calls) != 1 {
		t.Fatalf("calls = %+v, want one", *calls)
	}
	c := (*calls)[0]
	if !slices.Equal(c.add, []string{"mtplx/Y--Q35"}) || len(c.remove) != 0 || !slices.Equal(c.families, []string{"mtplx"}) {
		t.Fatalf("call = %+v, want add [mtplx/Y--Q35] and the mtplx family cleared", c)
	}
	if got, want := warn.String(), "wt: LiteLLM route for mtplx/Y--Q35 updated\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

// TestEnsureRouteQuietWhenNothingChanged covers a route that is already
// there and a discovered id served by a hand-written row (ApplyChange skips
// it): both come back unchanged, so nothing prints and the proxy is left
// alone.
func TestEnsureRouteQuietWhenNothingChanged(t *testing.T) {
	_, warn := stubRoutes(t, litellm.Result{Changed: false}, nil)
	restarts := 0
	restartProxy = func(context.Context) []string { restarts++; return nil }
	changed := EnsureRoute(context.Background(), routesCfg(), Target{ProviderID: "ollama", ModelName: "a:1", ModelID: "ollama/a:1"})
	WaitPendingRoutes()
	if changed || warn.Len() != 0 || restarts != 0 {
		t.Fatalf("changed = %v output = %q restarts = %d, want an unchanged, silent no-op", changed, warn.String(), restarts)
	}
}

// TestEnsureRouteSilentWhenConfigMissing pins that a machine with no
// config.yaml — LiteLLM never set up — sees nothing at launch.
func TestEnsureRouteSilentWhenConfigMissing(t *testing.T) {
	_, warn := stubRoutes(t, litellm.Result{}, litellm.ErrMissing)
	if EnsureRoute(context.Background(), routesCfg(), Target{ProviderID: "ollama", ModelName: "a:1", ModelID: "ollama/a:1"}) {
		t.Fatal("changed = true on a missing config.yaml")
	}
	WaitPendingRoutes()
	if warn.Len() != 0 {
		t.Fatalf("output = %q, want nothing", warn.String())
	}
}

// TestEnsureRouteWarnsAndNeverFails pins that a failed write is a warning,
// never an error the launch could trip on, and prints no "updated" line.
func TestEnsureRouteWarnsAndNeverFails(t *testing.T) {
	_, warn := stubRoutes(t, litellm.Result{}, errors.New("boom"))
	if EnsureRoute(context.Background(), routesCfg(), Target{ProviderID: "ollama", ModelName: "a:1", ModelID: "ollama/a:1"}) {
		t.Fatal("changed = true on a failed write")
	}
	WaitPendingRoutes()
	if got, want := warn.String(), "wt: LiteLLM route not updated: boom\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

// TestEnsureModelRouteSkipsNonLocal pins the guard: a cloud model and a model
// whose location cannot be resolved (its provider row is missing) are never
// written, and the second does not panic.
func TestEnsureModelRouteSkipsNonLocal(t *testing.T) {
	calls, warn := stubRoutes(t, litellm.Result{Changed: true}, nil)
	cfg := routesCfg()
	cloud := config.Model{ID: "openrouter/x", ProviderID: "openrouter", ModelName: "x", Location: config.LocationCloud}
	orphan := config.Model{ID: "gone/y", ProviderID: "gone", ModelName: "y"}
	if EnsureModelRoute(cfg, cloud) || EnsureModelRoute(cfg, orphan) {
		t.Fatal("changed = true for a model that is not local")
	}
	if len(*calls) != 0 || warn.Len() != 0 {
		t.Fatalf("calls = %+v output = %q, want no write and no output", *calls, warn.String())
	}
}

// TestEnsureModelRouteBoundsTheLockWait pins that a launch cannot hang behind
// another wt holding the config.yaml lock: the write is handed a context with
// a deadline. It also pins the Target EnsureModelRoute builds — the row's id
// travels with it.
func TestEnsureModelRouteBoundsTheLockWait(t *testing.T) {
	stubRoutes(t, litellm.Result{}, nil)
	inner := applyRoutes
	var hadDeadline bool
	var added string
	applyRoutes = func(cfg *config.Config, ch litellm.Change, o litellm.Options) (litellm.Result, error) {
		if o.Ctx != nil {
			_, hadDeadline = o.Ctx.Deadline()
		}
		if len(ch.Add) == 1 {
			added = ch.Add[0].ID
		}
		return inner(cfg, ch, o)
	}
	EnsureModelRoute(routesCfg(), config.Model{ID: "mtplx/org/Y/Q35", ProviderID: "mtplx", ModelName: "org/Y/Q35", Location: config.LocationLocal})
	WaitPendingRoutes()
	if !hadDeadline {
		t.Error("the route write got no deadline: a contended config.yaml lock would hang the launch")
	}
	if added != "mtplx/org/Y/Q35" {
		t.Errorf("added = %q, want the row's own id mtplx/org/Y/Q35", added)
	}
}
```

`stubRoutes` restores `applyRoutes` and `restartProxy` in its cleanup, so the two tests that reassign a seam after calling it need no cleanup of their own.

- [ ] **Step 2: Run them to verify they fail**

Run: `cd wt && go test ./internal/lifecycle -run 'TestEnsure'`
Expected: build failure, `undefined: EnsureRoute` and `undefined: EnsureModelRoute`.

- [ ] **Step 3: Implement**

In `wt/internal/lifecycle/routes.go`, add to the `const` block that holds `proxyReadyTimeout`:

```go
	// ensureRouteLockTimeout bounds the config.yaml lock wait of a launch-time
	// route check. A launch must not hang behind another wt process holding
	// the lock; giving up only costs the check, and the launch proceeds.
	ensureRouteLockTimeout = 10 * time.Second
```

Add below `StartRouteChange` and `routeModel`:

```go
// EnsureRoute writes t's LiteLLM route when it is missing (#192). It is for a
// model that is already running but that wt did not start — an omlx or mtplx
// server started by hand, an ollama model pulled since the last sync — which
// has no route until something writes one, so an agent launched on it gets
// "Invalid model name" from the proxy.
//
// The change is StartRouteChange, the one the start hook writes, so the two
// cannot name a model's route differently. Unlike Start it never starts or
// stops anything: a caller holding a stale probe gets at worst a route for a
// model that has since stopped, which the next sync or start removes.
//
// It never fails the caller: a failed write is a warning (applyAndReport) and
// a missing config.yaml is silent. It reports whether config.yaml changed, and
// says so in one line when it did. The proxy restart that a change triggers is
// asynchronous — a caller about to use the proxy owes WaitPendingRoutes().
func EnsureRoute(ctx context.Context, cfg *config.Config, t Target) bool {
	ch := StartRouteChange(cfg, t)
	changed := applyAndReport(ctx, cfg, ch, restartIfChanged)
	if changed {
		fmt.Fprintf(routesWarn, "wt: LiteLLM route for %s updated\n", ch.Add[0].ID)
	}
	return changed
}

// EnsureModelRoute is EnsureRoute for a launch row's model, the form the
// launch paths call. It skips a model that is not local — a cloud route is
// sync's business, and a model whose location cannot be resolved is not one
// wt can route — and bounds the config.yaml lock wait so a launch cannot hang
// behind another wt process. The caller must only pass a model the probe
// reported running.
func EnsureModelRoute(cfg *config.Config, m config.Model) bool {
	if loc, err := cfg.ResolveLocation(m); err != nil || loc != config.LocationLocal {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), ensureRouteLockTimeout)
	defer cancel()
	return EnsureRoute(ctx, cfg, Target{ProviderID: m.ProviderID, ModelName: m.ModelName, ModelID: m.ID})
}
```

The deferred `cancel()` does not abort the proxy restart: `bounceProxyAsync` detaches from the caller's context (`context.WithoutCancel`).

- [ ] **Step 4: Run the tests**

Run: `cd wt && go test ./internal/lifecycle`
Expected: PASS.

- [ ] **Step 5: Update the `WaitPendingRoutes` doc comment**

Its second bullet lists the launch paths that wait. Add the new callers to that list so the comment stays true: "…the launch paths — cmd/wt's startForLaunch and ensureRouteBeforeLaunch, the TUI start flow and ensureLaunchRoute before the launch, wt smoke before its one-shot prompt — therefore wait here…".

- [ ] **Step 6: Commit**

```bash
git add wt/internal/lifecycle
git commit -m "feat(wt): lifecycle.EnsureRoute writes a running model's missing route (#192)

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: non-TUI launch, `wt start` and `wt smoke`

**Files:**
- Modify: `wt/cmd/wt/start.go` (seam + helper, next to `waitPendingRoutes`)
- Modify: `wt/cmd/wt/launch.go` (`launchFilteredImpl`, after `buildCommandForModel` succeeds)
- Modify: `wt/cmd/wt/model_cmds.go:287` (`runStart`, `ActionLaunch` branch)
- Modify: `wt/cmd/wt/smoke.go:126` (`runSmoke`, the `t.Start()` branch)
- Modify: `wt/cmd/wt/testmain_test.go`
- Test: `wt/cmd/wt/launch_test.go`, `wt/cmd/wt/model_cmds_test.go`, `wt/cmd/wt/smoke_test.go`

**Interfaces:**
- Consumes: `lifecycle.EnsureModelRoute(cfg *config.Config, m config.Model) bool` from Task 2; the existing seam `waitPendingRoutes func()` in `start.go`.
- Produces (package `main`, not used by other tasks): `var ensureModelRoute = lifecycle.EnsureModelRoute`, `func ensureRouteBeforeLaunch(cfg *config.Config, m config.Model)`, test helper `stubEnsureRoute(t) *[]string`.

**When each caller invokes it.**
- `launchFilteredImpl`: only when the resolved route goes through LiteLLM (`route.Litellm`). A direct launch never touches `config.yaml`. Every local model that reaches this point is running: it came from the launchable list or from a pin that was already running or was just started. After a pin that was just started the call is a redundant read of `config.yaml` and changes nothing.
- `wt start <running id>` and `wt smoke` on a running target: unconditional, matching the start hook, which writes the route whatever the routing toggle says.

- [ ] **Step 1: Add the seam default and the test helper**

In `wt/cmd/wt/testmain_test.go`, inside `TestMain` after the `lifecycleStart` stub:

```go
	// The launch-time route check (#192): an unstubbed test that launches a
	// running local model through LiteLLM would rewrite the developer's real
	// config.yaml and restart their proxy. Tests that assert on it call
	// stubEnsureRoute.
	ensureModelRoute = func(*config.Config, config.Model) bool { return false }
```

And at the end of the file:

```go
// stubEnsureRoute records, in order, each launch-time route check as
// "ensure:<model id>" and each wait for the proxy as "wait". Both seams are
// restored on cleanup.
func stubEnsureRoute(t *testing.T) *[]string {
	t.Helper()
	var events []string
	oldEnsure, oldWait := ensureModelRoute, waitPendingRoutes
	ensureModelRoute = func(_ *config.Config, m config.Model) bool {
		events = append(events, "ensure:"+m.ID)
		return true
	}
	waitPendingRoutes = func() { events = append(events, "wait") }
	t.Cleanup(func() { ensureModelRoute, waitPendingRoutes = oldEnsure, oldWait })
	return &events
}
```

- [ ] **Step 2: Write the failing tests**

Append to `wt/cmd/wt/launch_test.go`. The first test reuses the fixture of `TestLaunchFilteredSkipsOllamaCheckInLitellm` (a fake `claude` binary that exits 0, LiteLLM on, the model reported running):

```go
// TestLaunchFilteredEnsuresRouteForRunningLocalModel pins #192: a running
// local model launched through LiteLLM has its route checked, and the proxy
// waited for, before the agent runs. Such a model may never have been started
// by wt, so no start hook wrote its route.
func TestLaunchFilteredEnsuresRouteForRunningLocalModel(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("MODELMAN_REGISTRY", "")
	worktree := t.TempDir()
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "claude"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cfg := &config.Config{
		DefaultTag: "code",
		Providers:  []config.Provider{{ID: "ollama", Location: config.LocationLocal}},
		Models: []config.Model{
			{ID: "ollama/remote-only-model", ProviderID: "ollama", ModelName: "remote-only-model", Tags: []string{"code"}},
		},
		Agents: []config.Agent{{Name: "claude", SupportedProviders: []string{"ollama"}}},
	}
	cfg.SetLitellmForTest(config.LitellmState{Enabled: true, URL: "http://localhost:4000", APIKey: "sk-litellm"})
	stubProbeInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "ollama", ModelID: "ollama/remote-only-model", Artifact: "remote-only-model", ModelName: "remote-only-model", Registered: true, Running: true},
		},
	})
	events := stubEnsureRoute(t)

	if err := launchFiltered("claude", worktree, cfg, false, "", "", "", false, nil, nil, nil); err != nil {
		t.Fatalf("launchFiltered: %v", err)
	}
	if got := strings.Join(*events, ","); got != "ensure:ollama/remote-only-model,wait" {
		t.Fatalf("events = %q, want ensure:ollama/remote-only-model,wait", got)
	}
}

// TestLaunchFilteredSkipsEnsureOnDirectRoute pins that a launch dialing the
// provider directly never touches config.yaml: the route it would write is
// not on this launch's path. The provider speaks claude's wire protocol and
// has a base_url, so with LiteLLM off the route resolves direct.
func TestLaunchFilteredSkipsEnsureOnDirectRoute(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("MODELMAN_REGISTRY", "")
	worktree := t.TempDir()
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "claude"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cfg := &config.Config{
		DefaultTag: "code",
		Providers: []config.Provider{
			{ID: "omlx", Location: config.LocationLocal, Protocols: []config.Protocol{config.ProtocolAnthropic}, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}},
		},
		Models: []config.Model{
			{ID: "omlx/m", ProviderID: "omlx", ModelName: "m", Tags: []string{"code"}},
		},
		Agents: []config.Agent{{Name: "claude", SupportedProviders: []string{"omlx"}}},
	}
	stubProbeInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "omlx", ModelID: "omlx/m", Artifact: "m", ModelName: "m", Registered: true, Running: true, ArtifactKnown: true},
		},
	})
	events := stubEnsureRoute(t)

	// The launch must succeed: an error here would mean it never reached the
	// point where the check would run, and the assertion below would pass
	// without proving anything.
	if err := launchFiltered("claude", worktree, cfg, false, "", "", "", false, nil, nil, nil); err != nil {
		t.Fatalf("launchFiltered on a direct route: %v", err)
	}
	if len(*events) != 0 {
		t.Fatalf("events = %v, want none on a direct route", *events)
	}
}
```

If `strings` is not yet imported in `launch_test.go`, add it.

Append to `wt/cmd/wt/model_cmds_test.go`:

```go
// TestStartRunningModelRepairsRoute pins #192: `wt start` on a model that is
// already running — possibly started outside wt — checks its LiteLLM route
// and waits for the proxy, instead of only reporting "already running". It
// still never calls the start driver.
func TestStartRunningModelRepairsRoute(t *testing.T) {
	cfg, req := startFixture(t)
	events := stubEnsureRoute(t)
	var out bytes.Buffer
	if err := runStart(&out, cfg, themes.Theme{}, "ollama/a:1", false); err != nil {
		t.Fatal(err)
	}
	if req.called {
		t.Fatal("the start driver ran for a model that is already running")
	}
	if got := strings.Join(*events, ","); got != "ensure:ollama/a:1,wait" {
		t.Fatalf("events = %q, want ensure:ollama/a:1,wait", got)
	}
	if !strings.Contains(out.String(), "already running") {
		t.Fatalf("out = %q, want the already-running line", out.String())
	}
}
```

Append to `wt/cmd/wt/smoke_test.go`. It mirrors `TestSmokeCmdIdlePinStartsRunsThenStops`, pinning the fixture's running model instead of the idle one:

```go
// TestSmokeCmdRunningPinEnsuresRouteBeforeRows pins #192 for smoke: a target
// that is already running skips the start, so no start hook writes its route.
// Smoke checks the route and waits for the proxy before the first row, or a
// model started outside wt reports a spurious FAIL on every row.
func TestSmokeCmdRunningPinEnsuresRouteBeforeRows(t *testing.T) {
	cfg := smokeFixtureConfig(t)
	events := stubEnsureRoute(t)
	oldStart, oldRel, oldPick, oldTTY, oldExit := startModel, releaseSession, runStopPicker, stdinTTY, smokeExit
	t.Cleanup(func() {
		startModel, releaseSession, runStopPicker, stdinTTY, smokeExit = oldStart, oldRel, oldPick, oldTTY, oldExit
	})
	startModel = func(*config.Config, catalog.Row, bool) error {
		*events = append(*events, "start")
		return nil
	}
	releaseSession = func() {}
	runStopPicker = func(*config.Config) { *events = append(*events, "stop") }
	stdinTTY = func() bool { return true }
	smokeExit = func(code int) { *events = append(*events, fmt.Sprintf("exit%d", code)) }
	t.Cleanup(smoke.SetBuildAndRunForTest(func(_ *config.Config, _ string, _ config.Model, _ string, _ string, _ time.Duration, _ smoke.ProfileApplier, cleanup *func() error) smoke.ExecOutcome {
		*events = append(*events, "row")
		*cleanup = func() error { return nil }
		return smoke.StubOutcome("boom", 1)
	}))

	cmd := smokeCmd(&app{cfg: cfg})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"ollama/qwen3.8:27b-mlx"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(*events, ","); got != "ensure:ollama/qwen3.8:27b-mlx,wait,row,stop,exit1" {
		t.Fatalf("events = %s, want ensure:ollama/qwen3.8:27b-mlx,wait,row,stop,exit1", got)
	}
}
```

- [ ] **Step 3: Run them to verify they fail**

Run: `cd wt && go test ./cmd/wt -run 'TestLaunchFilteredEnsuresRoute|TestLaunchFilteredSkipsEnsure|TestStartRunningModelRepairsRoute|TestSmokeCmdRunningPinEnsuresRoute'`
Expected: build failure, `undefined: ensureModelRoute`.

- [ ] **Step 4: Add the seam and the helper**

In `wt/cmd/wt/start.go`, directly below the `waitPendingRoutes` seam:

```go
// ensureModelRoute is a test seam over lifecycle.EnsureModelRoute. Production
// rewrites config.yaml and restarts the LiteLLM proxy; TestMain stubs it so no
// test touches the developer's real proxy.
var ensureModelRoute = lifecycle.EnsureModelRoute

// ensureRouteBeforeLaunch makes sure a running local model has its LiteLLM
// route, then waits for the proxy to carry it (#192). A model wt did not
// start — an omlx or mtplx server started by hand, an ollama model pulled
// since the last sync — has no route until something writes one; without this
// the launch reaches the proxy and gets "Invalid model name". It is a no-op
// for a cloud model, costs one read of config.yaml when the route is already
// there, and never fails the launch. m must be a model the probe reported
// running: this never starts one.
func ensureRouteBeforeLaunch(cfg *config.Config, m config.Model) {
	ensureModelRoute(cfg, m)
	waitPendingRoutes()
}
```

- [ ] **Step 5: Call it from the non-TUI launch**

In `wt/cmd/wt/launch.go`, in `launchFilteredImpl`, between the `buildCommandForModel` error check and the `rotation.New().RecordFor` call:

```go
	cmd, berr := buildCommandForModel(agent, m, worktreePath, cfg, yolo, extraArgs)
	if berr != nil {
		return berr
	}
	// The agent is about to dial m through the proxy. A local model here is
	// running (the launchable list holds no other kind, and a pin was started
	// above if it was idle), but wt may not be what started it, so its route
	// may not exist. Only a launch that goes through LiteLLM needs the route;
	// a direct launch leaves config.yaml alone.
	if route.Litellm {
		ensureRouteBeforeLaunch(cfg, m)
	}
```

`route` is the variable already resolved a few lines above for the ollama check.

- [ ] **Step 6: Call it from `wt start`**

In `wt/cmd/wt/model_cmds.go`, the `ActionLaunch` case of `runStart`:

```go
	case catalog.ActionLaunch:
		// Already running, but not necessarily started by wt: repair its
		// route as a start would have written it. Unconditional, like the
		// start hook — `wt start` has no agent whose route could say whether
		// the proxy is on its path.
		ensureRouteBeforeLaunch(cfg, row.Model)
		fmt.Fprintf(out, "wt: %s is already running\n", id)
		return nil
```

- [ ] **Step 7: Call it from `wt smoke`**

In `wt/cmd/wt/smoke.go`, add an `else` to the `if t.Start() { … }` block in `runSmoke`:

```go
	} else {
		// Already running, so no start hook ran: a model started outside wt
		// has no route yet, and every row below would FAIL against the proxy
		// with "Invalid model name". A no-op for a cloud target.
		ensureRouteBeforeLaunch(a.cfg, m)
	}
```

- [ ] **Step 8: Run the tests**

Run: `cd wt && go test ./cmd/wt`
Expected: PASS, the four new tests and every existing one. `TestStartRunningModelIsNoOp`, `TestStartDiscoveredRunningModel` and `TestStartNoArgUsesPickerAndRunningPickIsNoOp` keep passing on the `TestMain` default stub.

If `TestLaunchFilteredSkipsEnsureOnDirectRoute` fails at its `launchFiltered` call, the direct-route fixture is wrong, not the feature: fix the fixture so the launch succeeds (compare `smokeFixtureConfig` in `smoke_test.go`, which builds a direct route for claude). Do not loosen the `err != nil` check.

- [ ] **Step 9: Update the `--help` text**

`wt start`'s long help describes what the command does for a running model. Find it with `grep -n '"start' wt/cmd/wt/model_cmds.go wt/cmd/wt/commands.go` and, where it says a running model is a no-op, say instead that a running model is left running and its LiteLLM route is repaired if missing. If the help text does not mention the running case, leave it.

- [ ] **Step 10: Commit**

```bash
git add wt/cmd/wt
git commit -m "feat(wt): ensure the LiteLLM route on launch, wt start and wt smoke (#192)

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: TUI launch

**Files:**
- Modify: `wt/internal/tui/start_flow.go` (seam + helper, next to `waitPendingRoutes`)
- Modify: `wt/internal/tui/app.go:1093` (`proceedToLaunch`)
- Modify: `wt/internal/tui/testhelpers_test.go`
- Test: `wt/internal/tui/start_flow_test.go`

**Interfaces:**
- Consumes: `lifecycle.EnsureModelRoute(cfg *config.Config, m config.Model) bool` from Task 2; the existing seam `waitPendingRoutes func()` in `start_flow.go`; `modelItem.start bool` (true for a row Enter starts).
- Produces (package `tui`, not used by other tasks): `var ensureModelRoute = lifecycle.EnsureModelRoute`, `func ensureLaunchRoute(cfg *config.Config, agent string, m config.Model)`, test helper `stubEnsureRoute(t) *[]string`.

**Background.** `proceedToLaunch` is the one function every TUI launch passes through: Enter on a launch row, the ollama-warning "proceed" choice, a `-M` pin on a running row, and `finishStart` after a successful start. In the last case the row's `start` flag is still true (the table is not rebuilt in between) and the start hook has already written the route, so the check is skipped there.

- [ ] **Step 1: Add the seam default and the test helper**

In `wt/internal/tui/testhelpers_test.go`, inside `TestMain` after the `startModel` stub:

```go
	// The launch-time route check (#192): unstubbed, a test that launches a
	// running local model through LiteLLM would rewrite the developer's real
	// config.yaml and restart their proxy.
	ensureModelRoute = func(*config.Config, config.Model) bool { return false }
```

And at the end of the file:

```go
// stubEnsureRoute records, in order, each launch-time route check as
// "ensure:<model id>" and each wait for the proxy as "wait". Both seams are
// restored on cleanup.
func stubEnsureRoute(t *testing.T) *[]string {
	t.Helper()
	var events []string
	oldEnsure, oldWait := ensureModelRoute, waitPendingRoutes
	ensureModelRoute = func(_ *config.Config, m config.Model) bool {
		events = append(events, "ensure:"+m.ID)
		return true
	}
	waitPendingRoutes = func() { events = append(events, "wait") }
	t.Cleanup(func() { ensureModelRoute, waitPendingRoutes = oldEnsure, oldWait })
	return &events
}
```

- [ ] **Step 2: Write the failing tests**

Append to `wt/internal/tui/start_flow_test.go`. They use `modelTestConfig()` and `runningOmlxSnapshot()` from `pin_model_test.go` (LiteLLM on; `omlx/qwen3.8` running; `claude/opus` native), `flowEnter` and `indexOfID` as `startFixture` and `enterStartRow` in this file use them, and the unregistered-agent trick of `TestSuccessfulStartWaitsForPendingRoutesBeforeLaunching`: with `m.agent` set to a name no driver knows, the launch fails into `status` right after `proceedToLaunch` did its work, so no real process starts.

```go
// launchRowFixture is the model picker over modelTestConfig with omlx/qwen3.8
// running, the cursor on id, and the agent swapped for one no driver knows so
// that Enter runs proceedToLaunch and then fails the launch observably.
func launchRowFixture(t *testing.T, id string) model {
	t.Helper()
	tempStateDir(t)
	stubUsageStore(t)
	stubRefcountStore(t)
	stubInventory(t, runningOmlxSnapshot())
	m := flowEnter(t, model{cfg: modelTestConfig(), agent: "claude", selectedPath: t.TempDir(), width: 80, height: 24}, "claude")
	idx := indexOfID(m, id)
	if idx < 0 {
		t.Fatalf("no row %s in %v", id, itemIDs(m))
	}
	m.models.Select(idx)
	m.agent = "not-a-real-agent"
	return m
}

// TestLaunchOfRunningLocalRowEnsuresRoute pins #192 for the picker: Enter on
// a local row that is already running checks its LiteLLM route and waits for
// the proxy before the launch. Such a model may never have been started by
// wt, so no start hook wrote its route.
func TestLaunchOfRunningLocalRowEnsuresRoute(t *testing.T) {
	m := launchRowFixture(t, "omlx/qwen3.8")
	events := stubEnsureRoute(t)

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if got := strings.Join(*events, ","); got != "ensure:omlx/qwen3.8,wait" {
		t.Fatalf("events = %q, want ensure:omlx/qwen3.8,wait", got)
	}
	if status := next.(model).status; !strings.Contains(status, "launch failed") {
		t.Fatalf("status = %q, want the launch to have been attempted after the check", status)
	}
}

// TestLaunchOfNativeRowSkipsEnsureRoute pins that a model that does not go
// through LiteLLM (a native one never does) leaves config.yaml alone.
func TestLaunchOfNativeRowSkipsEnsureRoute(t *testing.T) {
	m := launchRowFixture(t, "claude/opus")
	events := stubEnsureRoute(t)

	m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if len(*events) != 0 {
		t.Fatalf("events = %v, want none for a native model", *events)
	}
}

// TestLaunchAfterStartSkipsEnsureRoute pins that a row wt just started is not
// checked again: the start hook wrote its route, and finishStart already
// waited for the proxy. The only wait recorded is finishStart's.
func TestLaunchAfterStartSkipsEnsureRoute(t *testing.T) {
	m := startFixture(t, "ollama", "ollama/gemma4:9b", "gemma4:9b")
	m.cfg.SetLitellmForTest(config.LitellmState{Enabled: true, URL: "http://localhost:4000", APIKey: "sk-test"})
	stubStartModel(t, func(int, context.Context, lifecycle.Target, lifecycle.Options) error { return nil })
	m.agent = "not-a-real-agent"
	events := stubEnsureRoute(t)

	got, _ := enterStartRow(t, m, "ollama/gemma4:9b")
	next, _ := updateMsg(got, recvStart(t, got))

	if joined := strings.Join(*events, ","); joined != "wait" {
		t.Fatalf("events = %q, want only finishStart's wait", joined)
	}
	if !strings.Contains(next.status, "launch failed") {
		t.Fatalf("status = %q, want the launch to have been attempted", next.status)
	}
}
```

- [ ] **Step 3: Run them to verify they fail**

Run: `cd wt && go test ./internal/tui -run 'TestLaunchOfRunningLocalRowEnsuresRoute|TestLaunchOfNativeRowSkipsEnsureRoute|TestLaunchAfterStartSkipsEnsureRoute'`
Expected: build failure, `undefined: ensureModelRoute`.

- [ ] **Step 4: Add the seam and the helper**

In `wt/internal/tui/start_flow.go`, directly below the `waitPendingRoutes` seam. Add `"github.com/ohanaverse/local-ai-setup/wt/internal/agents"` to the imports if the file does not have it:

```go
// ensureModelRoute is a test seam over lifecycle.EnsureModelRoute. Production
// rewrites config.yaml and restarts the LiteLLM proxy; TestMain stubs it.
var ensureModelRoute = lifecycle.EnsureModelRoute

// ensureLaunchRoute makes sure a running local model has its LiteLLM route,
// then waits for the proxy to carry it (#192). A model wt did not start has
// no route until something writes one, and the agent launched on it would get
// "Invalid model name" from the proxy. Only a launch that goes through
// LiteLLM needs the route: a direct or native launch leaves config.yaml
// alone. Like waitPendingRoutes in finishStart, the wait runs on the update
// goroutine — it is the last thing before the agent takes the terminal. mdl
// must be a row the probe reported running: this never starts a model.
func ensureLaunchRoute(cfg *config.Config, agent string, mdl config.Model) {
	if cfg == nil {
		return
	}
	if route, _ := cfg.ResolveRoute(mdl, agents.ProtocolsFor(agent)); !route.Litellm {
		return
	}
	ensureModelRoute(cfg, mdl)
	waitPendingRoutes()
}
```

- [ ] **Step 5: Call it from `proceedToLaunch`**

In `wt/internal/tui/app.go`, in `proceedToLaunch`, directly after `m.launchModel = highlighted.model`:

```go
	// A launch row is a model that was already running, and wt may not be
	// what started it, so its LiteLLM route may not exist yet. A row this
	// flow just started (start is still set: the table is not rebuilt in
	// between) had its route written by the start hook, and finishStart has
	// already waited for the proxy.
	if !highlighted.start {
		ensureLaunchRoute(m.cfg, m.agent, highlighted.model)
	}
```

- [ ] **Step 6: Run the tests**

Run: `cd wt && go test ./internal/tui`
Expected: PASS, the three new tests and every existing one.

If `launchRowFixture` fails because `flowEnter` lands somewhere other than the model picker for `modelTestConfig()`, read `flowEnter` and `startFixture` in this file and build the fixture the way `startFixture` does. Keep the three assertions as written.

- [ ] **Step 7: Run everything**

Run: `cd wt && go build ./... && go vet ./... && go test ./...`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add wt/internal/tui
git commit -m "feat(wt): the picker ensures a running model's LiteLLM route before launch (#192)

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: Docs

**Files:**
- Modify: `docs/guides/08-maintenance-and-troubleshooting.md` (the missing-route bullets near line 159 and Step 9 near line 232)
- Modify: `docs/guides/04-litellm-config.md` (where it lists what changes routes)
- Modify: `wt/docs/wt-start-stop.md`
- Modify: `wt/CLAUDE.md`, `CLAUDE.md` (the "Routing is derived, not stored" gotcha)
- Modify: `wt/CHANGELOG.md` (`## Unreleased`)

**Interfaces:** none. Read each section before editing; keep its voice and its level of detail.

- [ ] **Step 1: Guide 08**

In the per-kind missing-route bullets (the ollama bullet reads "A pulled ollama model with no route usually means no sync has run since the pull: run `wt litellm sync`"), add that launching the model through wt, or `wt start <id>`, also writes the route when the model is running. Keep `wt litellm sync` as the fix for a cloud model and for a client that reaches the proxy without wt.

In Step 9 (a route dropped by `wt litellm sync`), keep "start the model" as the durable fix and add one sentence: a model that is already running, started by hand, gets its route back from `wt start <id>` or from the next launch through wt.

- [ ] **Step 2: Guide 04**

Find the list of what changes routes: `grep -n 'wt start\|wt stop\|wt litellm sync' docs/guides/04-litellm-config.md`. Add the launch to it: launching an agent on a running local model through LiteLLM writes that model's route if it is missing. State that it never removes a route other than the stale siblings of a single-model provider, and never replaces a hand-written row.

- [ ] **Step 3: `wt/docs/wt-start-stop.md`**

Where it describes `wt start` on a running model, replace the no-op description: the model is left running, its LiteLLM route is written if missing (`wt: LiteLLM route for <id> updated`), then `wt: <id> is already running`.

- [ ] **Step 4: Both `CLAUDE.md` files**

Root `CLAUDE.md`, "Routing is derived, not stored": the sentence listing what changes `config.yaml` membership (`wt start`/`wt stop`/`wt litellm sync`) gains "and a launch of a running local model through LiteLLM".

`wt/CLAUDE.md`: find the routing or lifecycle section with `grep -n 'WaitPendingRoutes\|routeAfterStart\|start hook' wt/CLAUDE.md` and add two facts next to it: `lifecycle.EnsureModelRoute` is the launch-time route check and never starts a model; `cmd/wt` and `internal/tui` each stub the `ensureModelRoute` seam in `TestMain`, and a new launch path must call it through that seam.

- [ ] **Step 5: Changelog**

Under `## Unreleased` in `wt/CHANGELOG.md`, in a `### Fixed` section (create it if the release has none), in the file's existing entry style:

```markdown
- Launching a running local model that wt did not start no longer fails with
  `Invalid model name` (#192). wt writes the model's LiteLLM route, if it is
  missing, before handing the model to an agent — on a `-M` pin, on rotation,
  in the picker and in `wt smoke` — and `wt start <id>` on a running model now
  repairs the route instead of only reporting `already running`. It prints
  `wt: LiteLLM route for <id> updated` when it wrote one. A hand-written row
  is never replaced, and nothing is written when the launch dials the provider
  directly.
- The start hook writes a model's route under the id the picker showed. An
  artifact whose name resembles a registry model's (`org/name` beside `name`)
  was routed under the registry model's id (#195).
```

- [ ] **Step 6: Check links and commit**

Run: `make lint` (from the repo root)
Expected: `ALL LINKS OK` and no shell lint errors.

```bash
git add docs/guides wt/docs wt/CLAUDE.md CLAUDE.md wt/CHANGELOG.md
git commit -m "docs: wt ensures the LiteLLM route at launch (#192)

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: Verification

**Files:** none modified, unless a check finds a defect.

- [ ] **Step 1: Full local check**

Run: `make test-all` (from the repo root)
Expected: lint, modelman `check`/`test`, and wt `build`/`vet`/`test` all pass.

- [ ] **Step 2: Install the build under test**

Run: `make install`, then `wt --version`.

- [ ] **Step 3: Live check on the host**

Back up first: `cp ~/.config/litellm/config.yaml ~/.config/litellm/config.yaml.bak-192`. Hand-written rows that must survive every step: `ollama/q8`, `ollama/o35`, `ollama/llama3.2:3b`, and the four `openrouter/qwen/*` rows.

1. Start one omlx or mtplx model outside wt (`omlx start`, or `mtplx` on port 8003). Confirm `wt litellm list` does not show it. If it does, a sync has already routed it: stop it, run `wt litellm sync`, and start it by hand again.
2. From a scratch git repo (not this one), launch an agent on it with `-M <id>`. Expected: `wt: LiteLLM route for <id> updated` on stderr, and the agent gets a reply.
3. Launch again. Expected: no "updated" line, and the proxy's process start time is unchanged (`launchctl list | grep litellm`, or the PID in `ps`).
4. Remove the route (`wt litellm sync` while the model is stopped, then start it by hand again) and run `wt start <id>`. Expected: the "updated" line, then `already running`.
5. Same unrouted state, then `wt smoke <id>` from a scratch dir. Expected: the "updated" line before the first row, and no row failing with `Invalid model name`.
6. Confirm the hand-written rows listed above are still in `wt litellm list`, marked `(hand-written)`.
7. Stop the model (`modelman provider stop mtplx`, or `omlx stop`).

- [ ] **Step 4: Ask the user for the TUI check**

An agent cannot drive the picker. Ask the user to repeat step 2 through the `wt` picker: a hand-started model's row, Enter, and confirm the agent gets a reply.

- [ ] **Step 5: Report**

Report what passed, what the user still has to check, and any deviation from the spec. Ask before pushing the branch or opening the PR.
