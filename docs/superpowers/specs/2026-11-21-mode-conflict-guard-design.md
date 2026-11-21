# Mode-conflict guard (2026-11-21) — design

## Intent

The Setup tab (`wt-model setup`) and the Mode tab (`wt config`) write the same
`[model-server]` table of `~/.config/agent-wt/config.toml` — one `mode`,
`url`, `token_file`. Applying one screen silently overwrites the other's
choice, and a local `wt-model` can keep running (at every login) while
`remote` is in effect and nothing uses it. Guard the crossing actions and
inform on both screens. The user's own words: "there should be some guard to
prevent running the local wt-model when remote is in effect; the user should
be informed when settings chosen in `wt-model setup` and `wt config` -> Mode
conflict with each other".

## Goals

- Gate (confirm, default no) the two actions that cross the screens: the
  Setup tab applying a mode that would replace `remote`'s stored url/token,
  and the Setup tab's `s`/`R` starting a server no session would use.
- Standing conflict notes on both screens, naming the fix.
- Every gate message and standing note ends with the next step that resolves
  the conflict once the plan is applied (`next: …`), so the user knows what
  remains to be done — not only what is wrong. (The plan text and the result
  view may repeat it; the gate must carry it.)
- The Mode tab's leaving-local plan note names the running/answering local
  server and how to stop it.

## Non-goals

- No change to what any planner writes (the `[model-server]` values are
  unchanged); the gate sits before the plan.
- The Mode tab still never touches wt-model's LaunchAgent (documented
  contract): it informs, it does not stop or remove anything.
- No change to `wt-model serve`, `wt start`/`stop`/`smoke` or the job API.
- No cross-home awareness: VMs (or other users) with their own config home are
  separate cases; each config.toml is judged on its own. One wt-model server
  per machine; the Setup tab controls that one.

## Anti-goals

- No new prompt on non-crossing paths (applying a mode the stored one already
  matches, `x` stop, a Mode tab apply while local is not set up).
- No silent auto-correct and no hard refusal: every gate answers to `y`.

## Constraints

- `modelmode`/`modeview` stay session-safe leaves: the Mode tab knows the
  local server only through its server check (the ping of the listen
  address), never through launchd. The Setup tab may use its `AgentState`.
- Gates and conflict notes key off the **stored** mode (config.toml). Env
  overrides (`WT_MODEL_MODE`, `WT_MODEL_URL` + token) are per-process and are
  already flagged by the existing `env` row.
- Keys, exit codes and the plan flow stay as documented, apart from the added
  gate prompt (default no, `n`/`esc` cancels, like every other confirm).

## Approaches

1. **Notes only** — standing notes and stronger plan wording, nothing refuses
   or pauses. Cheap, but a hurried `a`,`y` still clobbers remote's url/token.
2. **Confirm-gate the crossing actions, plus notes** (picked) — one extra
   `[y/N]` exactly where settings would cross or an unused server would
   start; everything else unchanged. Principle: gate what overwrites the other
   screen's settings or starts an unused server; loudly note what merely
   strands the other screen's state. Chosen because the plan-first flow stays
   intact and non-crossing paths keep exactly one prompt.
3. **Hard block** — refuse outright, auto-offer to stop a running server.
   Strongest, but surprising refusals and it changes the Mode tab's
   never-touches-the-agent contract. The user picked 2.

## The how (decided with the partner)

1. **Setup tab, `a` with the stored mode `remote`** and the chosen mode
   `local` or `native` (both replace or clear remote's url/token): before the
   plan is built, `config.toml says remote; applying <local|native> replaces
   its url and token — continue? [y/N]` and
   `next: to use that server again, set remote on `wt config`'s Mode tab`.
   Default no; `n`/`esc` returns to the form, nothing planned. `y` continues
   into the existing plan + `Apply this plan? [y/N]`.
2. **Setup tab, `s`/`R` with the stored mode not `local`**: `the mode is
   <mode>; run the local server no session will use? [y/N]` and
   `next: stop it with x when done, or make the mode local on `wt config`'s
   Mode tab`. Default no. `x` (stop) is never gated.
3. **Standing notes** (kept whole with the mode choice):
   - Setup tab, stored `remote` and the chosen mode differs: `config.toml says
     remote: applying <chosen> replaces its url and token`.
   - Setup tab, the local server is loaded/running while the stored mode is
     not `local`: `the local server runs but <mode> is in effect: nothing uses
     it — x stops it, native here removes it` (Setup tab has `AgentState`, so
     it may name loaded/running/pid as the agent row already does).
   - Mode tab, leaving local for `remote`/`native` while the server check says
     the local server answers: the existing note (`wt-model's LaunchAgent is
     untouched; remove it with `wt-model setup``) strengthened to name it:
     `the local server answers at <url> and will be unused — stop it with x on
     `wt-model setup``, and keep the remove-it clause,
     `next: stop it with x on `wt-model setup`'s Setup tab, or apply native
     there to remove the LaunchAgent`. When the server does not answer, the
     existing short note stands.

## Residual gap (stated, accepted under option 2)

A server loaded before the switch keeps running (and again at every login)
until stopped with `x` or removed by applying `native` on the Setup tab. The
standing note names that fix; nothing forces the server down.

## Left to the builder

Exact wrapping/widths and prompt-kind plumbing (the Setup tab's confirm phase
gains one prompt kind, `s`/`R` another), test fixtures (`remoteState()` and
friends), and wording polish within the meanings above. Docs to follow:
`wt/docs/wt-model-server.md` (Setup tab), `wt/docs/wt-config.md` (the
strengthened leaving-local note).

## Approved

Approved by the partner 2026-11-21 with one amendment, folded in above:
every gate/warning prints the next step that resolves the issue once the plan
is applied.
