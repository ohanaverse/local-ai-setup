# pi-wt LiteLLM wrapper auto-detection — Design

**Date:** 2026-06-01
**Status:** Approved, ready for implementation plan
**Scope:** `bin/pi-wt` (single file), plus a small `bin/README.md` note

## Problem

`pi-wt` fails to launch a working pi session for users whose interactive `pi` is wrapped by `~/.pi/agent/activate-litellm.sh`. The wrapper fetches an enterprise LiteLLM API key, exports `ANTHROPIC_API_KEY` / `LITELLM_API_KEY`, and injects a default `--provider anthropic --model claude-opus-4-7` when neither flag is present, then execs the real `pi`.

In the affected setup, the wrapper is exposed via a zsh **function** in `~/.zshrc`:

```zsh
pi() { "$HOME/.pi/agent/activate-litellm.sh" "$@"; }
```

Shell functions are not inherited by child bash scripts. `pi-wt` (a bash script) calls `exec pi …`, which performs PATH lookup and finds the raw npm-installed `pi` binary (`~/.asdf/shims/pi`). That binary has no LiteLLM credentials, so pi reports:

> Warning: No models available. Use /login to log into a provider via OAuth or API key.

Vanilla-pi users (no LiteLLM, no wrapper) are unaffected.

## Goal

Make `pi-wt` honor `activate-litellm.sh` when it exists, with zero impact on users who don't have it.

## Non-goals

- Generalizing wrapper detection to other `*-wt` launchers (claude-wt, codex-wt, etc.). They use different credential paths; fix when/if they break.
- Adding a config knob, env var, or opt-out flag. YAGNI.
- Modifying `activate-litellm.sh` itself.
- Changing the unrelated `WARNING: no main guard installed` message from `wt-core.sh`.
- Introducing automated tests. The repo has no `*-wt` test suite per `CLAUDE.md` ("No formal test suite — validate structure + manual verification only").

## Design

### Single change: introduce `pi_launcher()` in `bin/pi-wt`

Add a helper that resolves which binary `wt_exec` should exec:

```bash
pi_launcher() {
  if [[ -x "$HOME/.pi/agent/activate-litellm.sh" ]]; then
    printf '%s\n' "$HOME/.pi/agent/activate-litellm.sh"
  else
    printf 'pi\n'
  fi
}
```

Replace every `exec pi …` call inside `wt_exec` with `exec "$(pi_launcher)" …`. There are five such call sites in the current `wt_exec`:

1. `exec pi "$@"` (skip-warning branch for non-pi `native:*` models)
2. `exec pi "$@"` (`native` / `native:pi` branch)
3. `exec pi --model "$model_to_use" "$@"` (model-found branch)
4. `exec pi "$@"` (model-not-in-`models.json` fallback)
5. `exec pi "$@"` (no model specified branch)

All five become `exec "$(pi_launcher)" …` with the same argument list.

### Detection rule

`-x "$HOME/.pi/agent/activate-litellm.sh"` (executable test, not just file existence). Rationale:
- An executable wrapper is unambiguous user intent.
- A non-executable file is treated as absent — defensive against partial installs.
- No env var, no flag, no config: the file's presence *is* the configuration.

### Why this composes with `activate-litellm.sh`'s default-injection

`activate-litellm.sh` only injects `--provider anthropic --model claude-opus-4-7` when no `--model` and no `--provider` is already present in `"$@"`. The matrix:

| Rotation outcome (selected by `pi-wt`) | What `pi-wt` execs | What `activate-litellm.sh` does | Net result |
|---|---|---|---|
| `native:pi` (default when no `models.conf`) | `activate-litellm.sh` (no extra args) | injects `--provider anthropic --model claude-opus-4-7` | opus-4-7 via LiteLLM ✓ |
| Model present in `~/.pi/agent/models.json` | `activate-litellm.sh --model <id>` | sees `--model`, no injection | selected model via LiteLLM ✓ |
| Model not in `models.json` | `activate-litellm.sh` (warning printed by `pi-wt`) | injects opus-4-7 | falls back to opus-4-7 ✓ |
| `native:*` for another agent | `activate-litellm.sh` (skip-warning printed) | injects opus-4-7 | falls back to opus-4-7 ✓ |
| No model specified at all | `activate-litellm.sh` | injects opus-4-7 | opus-4-7 via LiteLLM ✓ |

For users without `activate-litellm.sh`, every row falls back to plain `pi` and behavior is unchanged from today.

### Edge cases

- **Credential helper fails:** `activate-litellm.sh` already exits non-zero with a clear stderr message. `pi-wt`'s `exec` surfaces that exit code. No new handling needed.
- **Wrapper exists but is not executable:** `-x` test fails, falls back to plain `pi`. Same as today.
- **Wrapper exists, vanilla pi binary is missing:** `activate-litellm.sh`'s final `exec pi` will fail with "command not found." Acceptable — this is true today as well, and the user's environment is already broken.
- **`HOME` unset:** Bash under `set -u` would error. In practice `HOME` is always set in the contexts `pi-wt` runs in. Not worth defending against.

## Documentation

Update `bin/README.md`: add a short note (≤3 lines) under the `pi-wt` row in the agent-flags table, or as a footnote, stating that `pi-wt` automatically uses `~/.pi/agent/activate-litellm.sh` if it exists and is executable.

## Verification

Manual, per `CLAUDE.md` conventions:

1. **Wrapper present (LiteLLM user):** From a clean shell, `pi-wt -w some-branch`. Expected: pi launches showing a model in the status line, no "No models available" warning.
2. **Wrapper absent (vanilla user):** `mv ~/.pi/agent/activate-litellm.sh ~/.pi/agent/activate-litellm.sh.bak; pi-wt …`. Expected: identical behavior to before this change. Restore after.
3. **Shellcheck:** `shellcheck bin/pi-wt` — no new warnings.
4. **Marketplace validation:** `python3 scripts/validate_marketplace.py` — passes.

## Risks

- **Low.** The change is gated on file existence and is a drop-in replacement for `pi` in the exec call. No state, no side effects beyond what the wrapper itself already does interactively.
- One reviewer concern to anticipate: hard-coding a path (`$HOME/.pi/agent/activate-litellm.sh`). Mitigation: that path is fixed by upstream pi convention (it's where pi installs its agent config), not invented here.

## Out-of-scope follow-ups (not part of this work)

- Consider whether `claude-wt` / `codex-wt` need analogous wrappers in their respective deployments.
- If multiple `*-wt` scripts ever need wrapper auto-detection, lift the helper into `wt-core.sh` with a per-wrapper hook. Not warranted for one launcher.
