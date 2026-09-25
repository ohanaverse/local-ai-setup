# pi-local profile: accept-all permissions — Design

**Date:** 2026-09-25
**Status:** Approved

## Problem

pi sessions launched through the `pi-local` profile (`~/.config/agent-wt/profiles.toml`) run under little-coder's shell gate. Any command outside the built-in safe-prefix whitelist is refused — e.g. `bash ~/.agents/plugins/repo-cleanup/bin/repo-cleanup` was blocked, even though the agent explained what it wanted to run.

## Decision

Set `LITTLE_CODER_PERMISSION_MODE=accept-all` via the profile's `env` mechanism:

```toml
[[profiles]]
agent = "pi"
match = "location"
location = "local"
wrapper = { binary = "little-coder", args_template = ["{{args}}"] }
env = { LITTLE_CODER_PERMISSION_MODE = "accept-all" }
```

In `accept-all` mode little-coder skips the whitelist gate entirely; every shell call passes (same mode the benchmark runner sets).

## Scope

- Applies only to pi launched from the main repo location (`location = "local"`). Worktree launches match no profile and stay gated.
- Other agents (claude, codex, …) are unaffected.
- No restart is needed — the profile change takes effect on the next `wt` launch.

## Scope change (2026-09-25, post-review)

The original decision assumed the profile `env` mechanism was already usable for pi. It was not: wt's pi driver declared wrapper-only mechanisms (`wt/internal/agents/pi.go` `ProfileMechanisms`), and profile validation is global — an `env` line on the pi profile disabled every profile. First attempt was caught by verification and reverted.

Amended decision: extend wt's pi driver to also declare `profiles.MechanismEnv`. `cmd/wt/launch.go` already applies profile env to the launched process before the wrapper; with the little-coder wrapper the launched process is little-coder itself, which reads `LITTLE_CODER_PERMISSION_MODE`. The config change above is unchanged.

## Alternatives considered

- **`LITTLE_CODER_BASH_ALLOW="bash"`** — keeps the gate but adds the `bash` prefix. Rejected: `bash <anything>` is arbitrary execution anyway, so the protection gained is near-zero while friction for other prefixes persists.
- **Curated allow-list** — same friction problem, more maintenance.

## Security note

little-coder's README is explicit that the whitelist is a speed bump, not a sandbox. This change removes that speed bump for pi-local sessions only; `LITTLE_CODER_PERMISSION_MODE=manual` remains the fallback if a human gate is wanted later.

## Verification

- `wt profile list` — TOML parses and the profile shows the new env.
- `wt profile show -A pi -M <local-model>` — dry-run resolution shows `LITTLE_CODER_PERMISSION_MODE=accept-all` applied alongside the wrapper. A `-M` with a local-location model (e.g. `ollama/ornith-1.5:35b`) is required: the location tier can only match once the model resolves to `local`, and a cloud model correctly reports `(no matching profile)`.