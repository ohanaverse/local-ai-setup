# wt: ensure the LiteLLM route at launch — design

Date: 2026-10-03
Issue: #192 (and one item of #195)
Roadmap: [2026-10-03 issue roadmap](../plans/2026-10-03-issue-roadmap.md), milestone A

## Problem

A local model that is running, but that wt did not start, has no LiteLLM route until something runs `wt litellm sync`. Two common cases: an omlx or mtplx model started by hand, and an ollama model pulled since the last sync.

wt lists such a model as launchable. Since #188 it no longer refuses it as "not in LiteLLM", so the launch goes ahead and the proxy answers `Invalid model name`.

`wt start <id>` does not repair it. For a running model it prints `<id> is already running` and returns (`wt/cmd/wt/model_cmds.go:287`).

## Goal

Launching a running local model through wt works, whoever started it. The user never has to run `wt litellm sync` to make a model wt already lists usable.

**Success criteria**

- A model started outside wt launches through wt on the first try.
- A launch whose route already exists restarts nothing.
- wt never starts or stops a model as a side effect of this check.
- A hand-written `config.yaml` row is never replaced.

## Current behavior

The route write has one home. `lifecycle.Start` calls `routeAfterStart`, which calls `applyAndReport(StartRouteChange(cfg, t), …)` (`wt/internal/lifecycle/routes.go`). `applyAndReport` writes `config.yaml` synchronously and, only when the file changed, restarts the proxy on a goroutine that `WaitPendingRoutes()` rejoins.

That hook runs only when a launch path calls `lifecycle.Start`, and launch paths call it only for a row whose action is "start". A row that is already running skips it. Three hand-off points are affected:

| Path | Where | Covers |
|---|---|---|
| Non-TUI | `launchFilteredImpl`, `wt/cmd/wt/launch.go` | `-M` pin, single auto-resolved model, rotation |
| TUI | `proceedToLaunch`, `wt/internal/tui/app.go` | picker selection, resume prompt |
| Smoke | `runSmoke`, `wt/cmd/wt/smoke.go`, when `t.Start()` is false | every smoke row |

## Approach

Add one ensure-only entry point, `lifecycle.EnsureRoute`, and call it at each hand-off point and from `wt start`.

Two alternatives were rejected:

- **Call `lifecycle.Start` for running rows.** `Start` is a no-op for a running model and then runs the hook, so it would work on a fresh probe. On a stale one it would start a server. Rotation and auto-resolution must never start a server (`launchableModels` in `wt/cmd/wt/resolve.go`), so this path is unsafe.
- **Run a full sync before each local launch.** It probes every provider and can remove routes. A launch should change one route at most.

## Design

### `lifecycle.EnsureRoute`

```go
// EnsureRoute writes t's LiteLLM route when it is missing. It never starts or
// stops a model and never fails the caller.
func EnsureRoute(ctx context.Context, cfg *config.Config, t Target) (changed bool)
```

- It applies the `Add` half of `StartRouteChange(cfg, t)` through `applyAndReport` with `restartIfChanged`. The model to route is derived exactly as the start hook derives it, so the hook, the ensure and `wt litellm sync` agree on the id.
- It never removes a route. The start hook also clears a single-model provider's family, which is safe there because `Start` has just stopped or refused any occupant. The ensure has no such guard: a single-model server can list sibling variants as running together, sync routes all of them, and a family clear at launch would delete a running sibling's route and restart the proxy on every alternating launch. Stale sibling routes are left for the next start, stop or sync.
- `ApplyChange` already skips a discovered id that has a hand-written row. `EnsureRoute` adds no rule of its own.
- A write failure prints the existing `wt: LiteLLM route not updated: …` warning and returns false. A missing `config.yaml` (LiteLLM not set up) stays silent, as today.
- When the write changed `config.yaml` it prints one line on stderr: `wt: LiteLLM route for <id> updated`. When nothing changed it prints nothing. The wording is "updated", not "added", because a change can also be a rewrite of a row that drifted from the registry.
- It returns as soon as the file is written. The caller owes `WaitPendingRoutes()` before using the proxy.

Callers use a thin wrapper, `EnsureModelRoute(cfg, model) bool`. It skips a model whose location is not local, bounds the `config.yaml` lock wait at 10s so a launch cannot hang behind another wt process, and builds the `Target` from the model.

### When callers invoke it

A caller invokes `EnsureRoute` only when all three hold:

1. The model's location is local.
2. The probe reported it running. Every hand-off point already holds a row or a launchable list built from a probe.
3. Its route for this agent resolves through LiteLLM (`cfg.ResolveRoute`).

A cloud model, a direct route, a command agent, and routing switched off all skip the call.

Condition 3 applies to agent launches. `wt start` and `wt smoke` have no single agent route to consult, so they call it unconditionally for a running local model, as the start hook does.

A launch that just ran `lifecycle.Start` does not need the call, since the hook has already written the route. The TUI and `wt smoke` skip it there. The non-TUI launch cannot tell a just-started pin from a running one at its hand-off point, so it calls it anyway; that costs one read of `config.yaml` and changes nothing.

Each caller then calls `WaitPendingRoutes()` before handing off. It costs nothing when no restart is pending.

### Probe trust

The issue requires no write when a provider's probe is untrusted. A row marked running came from a probe that answered, so condition 2 satisfies it. An unreachable or unknown family has no running row.

`EnsureRoute` does not probe again. The consequence is accepted: in the TUI the picker can stay open, and a model that stopped meanwhile gets a route written for it. No other route is touched. That launch fails with or without the write, and the next sync or start corrects the file. Probing again would add up to 2s to every local launch.

### Model id in `lifecycle.Target`

```go
type Target struct{ ProviderID, ModelName, ModelID string }
```

`ModelID` is the id the catalog row carries. Every launch and start caller sets it from the row.

`StartRouteChange` resolves the model to route in this order:

1. `ModelID` set and a registry model has that exact id: that model.
2. `ModelID` set and no registry model has it: `litellm.DiscoveredModel(ProviderID, ModelName)`.
3. `ModelID` empty: today's behavior, `litellm.ModelFor` and then the discovered model.

Step 2 no longer goes through `ModelFor`'s fuzzy fallback. This fixes the #195 item where two artifacts name-match one registry entry: the inventory gives the second a discovered id, the fuzzy fallback resolved it to the registry model, and the hook and the picker disagreed. With the id carried, the hook writes the id the picker showed.

Step 3 keeps a `Target` built without an id working as it does today. Both production constructors (`wt/cmd/wt/start.go`, `wt/internal/tui/start_flow.go`) build it from a row and will set the id, so step 3 is a fallback, not a supported path for new callers.

The stop side (`routeRemove`) does not take a `Target` and is unchanged.

### `wt start <running id>`

The `ActionLaunch` branch of `runStart` calls the ensure and `WaitPendingRoutes()`, then prints `wt: <id> is already running`. The updated-route line appears above it when a route was written. The call is unconditional: `wt start` on an idle model writes the route whatever the routing toggle says, and on a running model it now matches.

### Cost

One proxy restart when the route was missing. None otherwise: one locked read of `config.yaml`.

In the TUI the picker must not freeze during that restart, and the check's output must not be lost under the alt screen:

- The write itself runs on the update goroutine. It is a fast locked read of `config.yaml`, plus a write when the route is missing.
- When the write changed nothing, the launch continues at once with no extra screen.
- When it changed the file, the picker enters a routing phase that shows `Updating the LiteLLM route for <id> — restarting the proxy (<elapsed>)`. The wait for the proxy runs in a command, and the launch continues when it finishes. The restart cannot be cancelled, so the only key is ctrl+c to quit.
- Route output is captured while the picker owns the terminal (`lifecycle.SetRouteOutput`). It is printed on the real terminal when the agent takes it, or after wt exits if no launch happened, and its last line is shown in the picker's status line.

## Error handling

| Case | Behavior |
|---|---|
| `config.yaml` missing | Silent. Launch proceeds. |
| `config.yaml` unparseable or lock wait cancelled | Warning on stderr. Launch proceeds. |
| Row cannot be built for the model | Per-id warning on stderr. Launch proceeds. |
| Proxy restart fails or does not come back | Existing restart warnings. Launch proceeds. |
| Hand-written row holds the id | No write, no message. The user's row serves the name. |

The launch always proceeds. A missing route then shows as the proxy's own error, as it does today.

## Testing

**`internal/lifecycle`**

- Adds a missing route and reports changed.
- Route present: reports unchanged, no restart.
- Hand-written row for the id: unchanged.
- Single-model provider: the write carries no family removal.
- Write error: warning, false, no restart.
- `StartRouteChange` with `ModelID`: exact registry id, discovered id, and the two-artifacts-one-entry case yielding two distinct ids.

**`cmd/wt`**

- `launchFilteredImpl` calls the ensure for a running local model routed through LiteLLM, and waits before `runAgentCmd`. It skips direct and command-agent launches; a cloud model is a no-op inside `EnsureModelRoute`. A pin that was just started is checked again (see Design), which changes nothing.
- `runStart` on a running id calls the ensure and prints both lines.
- `runSmoke` calls the ensure when the target is already running.
- The existing test that pins `StartRouteChange` against sync's desired id (`wt/cmd/wt/litellm_test.go`) passes `ModelID` and still holds.

**`internal/tui`**

- `proceedToLaunch` calls the ensure for a running local row. A changed write enters the routing phase and launches only after the wait; an unchanged one launches at once without waiting. It skips the other cases.
- Captured route output reaches the status line and the post-release notes; keys other than ctrl+c are ignored while routing; a stale completion message is dropped.

All through the existing seams (`applyRoutes`, `restartProxy`, `waitPendingRoutes`). No test starts a model or a proxy.

**Live**

1. Start an omlx or mtplx model by hand. Confirm `wt litellm list` lacks it.
2. `wt -M <id>` with an agent: the updated-route line prints, and the agent gets a reply.
3. Launch again: no updated-route line, no proxy restart.
4. `wt start <id>` on a running, unrouted model: route added.
5. TUI picker launch of the same model. This step needs the user.

`make test-all` before the PR.

## Docs

- `docs/guides/08-maintenance-and-troubleshooting.md`: the missing-route steps no longer need `wt litellm sync` for a running local model launched through wt. The sync remains the fix for cloud models and for use outside wt.
- `docs/guides/04-litellm-config.md`, where it lists what changes routes: add the launch.
- `wt/docs/wt-start-stop.md`: `wt start` on a running model repairs its route.
- `wt/CLAUDE.md`, root `CLAUDE.md` (the "Routing is derived" gotcha), and the changelog.

Guides stay state-independent.

## Out of scope

- A cloud model with a missing route.
- Removing stale routes at launch. The ensure only adds.
- The other #195 items. They are milestone C.
