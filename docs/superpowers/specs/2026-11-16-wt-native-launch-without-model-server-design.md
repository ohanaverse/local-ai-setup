# wt-session: native launch when no model server is configured (#615)

Status: approved by the user in conversation 2026-11-16 (approach A). Follow-up for external enterprise gateways (pi): #618.

## Intent

Since Phase 2 (#616) every launch of a model-driven agent refuses without a `[model-server]` (exit 1, "nothing was launched"). A machine that never used wt-model, or whose agent reaches its model through the agent's own configuration (for example org-managed Claude Code settings that point at a company gateway and supply credentials), can no longer launch `claude-wt` at all. The user's words: "without wt-model, claude-wt just runs claude without model prompt."

## Goals

- With **no model server configured at all** (neither `url` nor `token_file`/token, from `config.toml` or `WT_MODEL_URL`/`WT_MODEL_TOKEN_FILE`/`WT_MODEL_TOKEN`), `wt -A <agent>` and the agent picker launch the agent natively: no model prompt, no catalog, no client session, the agent's own configuration decides the model.
- wt keeps doing everything that is not model selection: worktree creation (`-W`), `--root`, `--cwd`, launch warnings and the guard, session claims, `--yolo`, passthrough args after `--`, the post-exit summary.
- One `wt:` stderr line says what happened, so the fallback is never silent.

## Non-goals

- No model picker, `-M`, `-T`, `-F` or slot profile without a server. They refuse exactly as today.
- No direct routes, no `[litellm] enabled`, no provider keys on the session (anti-goal 3 stands). External enterprise gateways for pi and other agents: #618.
- No change for a configured server: unreachable, wrong token, API/schema skew all fail fast as today.

## Anti-goals

- A fallback that fires when a server **is** configured (even only half) would hide misconfiguration. It must not.
- A native launch must never receive the gateway token or origin (Review Focus 4): a native model gets the zero `Route`.

## Constraints

- The spec/plan rule "never falls back to anything local" is about a *local wt-model* (spec Anti-goals, "Connection and configuration"). A native launch uses no model route and no server, so it is not that fallback; the spec, guide 13 and `wt-session.md` get an explicit carve-out.
- Exit codes follow the plan's table: the agent's own status for a launch; refusals stay exit 1.
- N7 wording (`client session(s)`), M-rule message shapes, per-call test seams, top-level `//` comment on every `Test*` (`wt/CLAUDE.md`, `wt/docs/style-guide.md`).

## Approaches considered

1. **Automatic fallback when nothing is configured (chosen).** Smallest user-visible change, matches the issue ("default to using a native agent session"). One stderr line keeps it from being silent.
2. **Explicit opt-in (`[launch] native = true`).** Safer against surprises but needs setup on every machine and contradicts "default". Rejected.
3. **Native-only launcher shim.** Duplicates launch logic, does not fix `claude-wt`. Rejected.

## Design

**Trigger.** `modelclient.ResolveEndpoint` returns `*NotConfiguredError`. Fall back only when `MissingURL && MissingToken` (the "no model server is configured" lead). Exactly one of the two missing is a half-configured server and still refuses, naming the missing setting. A configured server is never bypassed.

**Where.** The single pre-flight gate in `cmd/wt-session/main.go` (`buildModels(launchStill)`, ~460-474), `launchFilteredRemote` (`launch_remote.go` ~78-111), the picker pre-flight in `runLaunchPath` (`main.go` ~130-143) and `tui.enterModelPhaseForAgent` (`internal/tui/app.go` ~749). Command agents (`shell`) already launch without a server through `launchCommandImpl`; the native path follows the same shape with `config.Model{Native: true, ModelName: "native"}` (the pre-flip `passthroughModel()` sentinel), which every driver already accepts and `agents.GatewayRoute` maps to the zero `Route`.

**Behavior with no server configured:**

| Invocation | Result |
|---|---|
| `wt -A <agent>` (claude, codex, copilot, pi, opencode, agy) | launches natively; stderr note; agent's exit status |
| `wt -W n -A <agent>`, `--root`, `--cwd` | same, after the usual worktree/warning/guard steps (no early refusal for this case) |
| bare `wt` on a terminal | worktree picker, then agent picker; choosing a model-driven agent launches it natively, no model phase |
| bare `wt` without a terminal | unchanged: picker needs a TTY (names `-A`) |
| `-M`, `-T`, `-F`, `-M profile/<name>` | refuse as today (`*NotConfiguredError`, "nothing was launched") |
| `wt start|stop|job|smoke|stats|model list --server`, profile slots add/set/candidates | refuse as today (they need a server) |
| `-A shell` and host commands | unchanged |

**Note line.** One line on stderr per launch, M-rule shape, lower-case, naming the agent and the way out: `wt: note: no model server is configured — launching <agent> natively, with its own configuration (see <doc> to use wt-model)`. Exact wording is the builder's, within the style guide.

**Native launch specifics (verified in recon).** claude native: bare `claude` (+ `--dangerously-skip-permissions` with `--yolo`), no `--model`; claude/copilot clear `ANTHROPIC_*`/provider auth env inherited from the shell (org-managed settings are applied by claude itself and are unaffected; to be confirmed on a managed machine); codex, opencode, pi run bare with their own config; agy is already native. `profiles.toml` overlays do not apply to a native launch (existing `applyProfileForLaunch` no-op); say so in the docs. No client session is registered or ended; no rotation/usage record is written (there is no model id).

## Builder's calls

- Whether the TUI learns "native mode" through a new field on the TUI run arguments or by a nil `ModelClient` plus a bool; keep the TUI's no-server status text for the refuse cases.
- Exact stderr wording and test names; exact refactor of the shared "launch natively" helper (one function used by the CLI path and the TUI).
- Which existing tests pin the refusal (`flip_test.go` "the worktree picker, no agent", `TestModelLaunchRefusesBeforeAnyGitWork`, the `-A claude` cases) and how to split each into "refuses" vs "launches natively".

## Docs that change with the code (D3)

`wt/docs/wt-session.md` ("There is no bare launch without a model any more", the per-command refusal table, interim limits), `docs/guides/13-deployment-use-cases.md` (~116 "instead of falling back to anything local"), the spec's Anti-goals/"Connection and configuration"/"Migration of existing installs" (native-only upgraders), `wt/CLAUDE.md` (R13 note, the gate), `wt/CHANGELOG.md`, root `CLAUDE.md` if it quotes the refusal, `docs/guides/08` if it does. `grep -rn` the old wording across `CLAUDE.md`, `docs/`, `wt/docs/`, `docs/contracts/` and test comments.
