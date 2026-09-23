# `nyt-litellm` provider for pi: routing through the enterprise LiteLLM gateway

## Problem

On this machine, `pi` currently reaches Claude models through a hand-maintained
side channel that bypasses `wt` entirely:

- `~/.pi/agent/activate-litellm.sh` — a wrapper script that shells out to
  `/usr/local/bin/litellm-credential-helper.sh` (NYT's enterprise Vault-backed
  credential helper — the same one Claude Code's managed settings already use
  as `apiKeyHelper`), exports the resulting key as `ANTHROPIC_API_KEY`, and
  `exec`s the real `pi` binary.
- `~/.pi/agent/models.json` has a hand-written `"anthropic"` provider block
  pointing at `https://llm-gateway.nyt.net`.

This means pi's model routing lives outside wt's config/registry model
entirely, and outside wt's `internal/agents/pi_models.go` sync logic that
every other pi provider entry goes through. It also means wt's registry
(`~/.config/local-ai/registry.toml`) and config (`~/.config/agent-wt/config.toml`)
are both currently empty on this machine — no provider, model, or agent is
configured for pi (or anything else) yet.

We want `pi` launched through `wt` (`wt -A pi`) to route through the same
enterprise gateway, with wt itself resolving the Vault-backed key at launch
time — no external wrapper script, no hand-written `models.json` block. This
will be the only provider configured for pi on this machine.

## Non-goals

- No changes to Claude Code's own managed-settings routing (it already uses
  the same credential helper via `apiKeyHelper`, unrelated to this).
- No changes to wt's `[litellm]` proxy on/off toggle or `wt litellm ...`
  command family. `nyt-litellm` is a **direct-mode** registry provider whose
  `base_url` happens to be NYT's own gateway — it is not routed through a
  second, locally-run LiteLLM proxy hop.
- No support for other agents (claude, codex, etc.) picking up this provider
  — scoped to pi only, since it's the only agent in use on this machine today.
  (Nothing prevents adding it to another agent's `supported_providers` later;
  the provider entry itself is agent-agnostic.)

## Design

### 1. New `exec:` secret-ref form (`wt/internal/config`)

`ResolveSecret` currently supports two forms — `os.environ/NAME` and a bare
`NAME` — both of which silently return `""` on a miss and never error. A
Vault-backed helper script needs a form that actually runs a command, and a
way to fail loudly when that command fails (no logged-in Vault session, no
`vault`/`jq` installed, permission denied on the Vault path, etc.).

Add a third form, `exec:<path> [args...]`, and change the function signature
to return an error:

```go
// ResolveSecret resolves a secret_ref-style value. Three forms:
//   - "exec:<path> [args...]" runs the command and returns its trimmed
//     stdout; a non-zero exit is an error (the command's stderr is
//     included). Memoized per raw ref for the lifetime of the process, so
//     multiple resolutions of the same ref (e.g. ResolveRoute and pi's
//     models.json sync in the same launch) only run the command once.
//   - "os.environ/NAME" or a bare "^[A-Z][A-Z0-9_]*$" name reads that env
//     var — unchanged, never errors (a missing var resolves to "").
//   - anything else is used verbatim, unchanged.
func ResolveSecret(ref string) (string, error)
```

Implementation notes:
- `exec:` command line is split with `strings.Fields` (no shell metachars —
  wt does not need to support shell pipelines here, matching the existing
  `litellm-credential-helper.sh` invocation shape).
- Result cached process-lifetime in a package-level `sync.Map` keyed by the
  raw `ref` string, so a Vault OIDC prompt/network call fires at most once
  per `wt` invocation even though both `ResolveRoute` (for `Route.APIKey`)
  and pi's `syncDirectProviders` (for `models.json`) resolve the same ref on
  every pi launch.
- On a non-zero exit, wrap `*exec.ExitError` so the command's own stderr text
  reaches the caller (e.g. the helper's `"No key found at ... /email"` or
  `"vault not found"` messages) rather than a bare exit-status error.

**Call-site fallout** — both existing callers change from "resolve, treat
error as impossible" to explicit error handling:

- `ResolveRoute` (`config.go`): wraps and returns the error instead of
  assigning `apiKey` unconditionally. A failed direct-mode resolution now
  aborts route resolution — surfaces through `BuildLaunchCmd` and up to the
  launch path before pi is ever exec'd (per the "fail the launch" decision
  below).
- `syncDirectProviders` (`pi_models.go`): today, an empty resolved key means
  "skip writing this provider's block" (`if wantAPIKey == "" { continue }`).
  That's still correct behavior for the `os.environ`/bare-name forms (a
  genuinely-unset env var), but an `exec:` **error** must not be silently
  swallowed as a skip — it needs to propagate as a real error from
  `SyncModels`, consistent with `ResolveRoute`'s behavor for the same ref.

Only the two existing forms' behavior is unchanged (never error, empty on
miss); only `exec:` can produce an error.

### 2. Registry entry (`~/.config/local-ai/registry.toml`)

The registry on this machine is currently empty (`providers = []`,
`models = []`). Add:

```toml
[[providers]]
id = "nyt-litellm"
name = "NYT LiteLLM Gateway"
location = "cloud"
protocols = ["openai-chat"]
[providers.auth]
type = "api_key"
base_url = "https://llm-gateway.nyt.net"
secret_ref = "exec:/usr/local/bin/litellm-credential-helper.sh"

[[families]]
name = "claude-sonnet-4-6"
display_name = "Claude Sonnet 4.6"

[[models]]
id = "nyt-litellm/claude-sonnet-4-6"
family = "claude-sonnet-4-6"
provider_id = "nyt-litellm"
model_name = "claude-sonnet-4-6"
tags = ["code"]
```

- `location = "cloud"` — a hosted gateway, not a local server process.
- `protocols = ["openai-chat"]` matches pi's declared protocol
  (`piDriver.Protocols()`), so pi resolves this provider **direct**, never
  forced through wt's own `[litellm]` proxy toggle.
- One model only: `claude-sonnet-4-6`, matching the model
  `activate-litellm.sh` already defaults pi to today when no
  `--model`/`--provider` is passed. Other models can be added as additional
  `[[models]]` rows later with no code change.
- No cost/pricing fields for now.

This is a machine-local, credential-bearing config change — applied by
directly editing `registry.toml` (modelman has no CLI for adding a
provider/model; it's TOML or its TUI's `a` add-model form either way), not
through a modelman command.

### 3. wt config (`~/.config/agent-wt/config.toml`)

Also currently absent on this machine. Create it fresh:

```toml
default_tag = "code"

[[agents]]
name = "pi"
supported_providers = ["nyt-litellm"]
default_provider = "nyt-litellm"
```

No `[litellm]` table — wt's proxy routing stays off/unconfigured, matching
`nyt-litellm` being a direct-mode provider.

### 4. pi's `models.json` sync (no new code)

wt's existing `syncDirectProviders` (`internal/agents/pi_models.go`) already
writes one provider block per non-ollama, non-native registry provider with
a `base_url` into `~/.pi/agent/models.json` — this logic is generic and
requires no changes; it activates purely from the Section 2 registry entry.
On the first `pi` launch through `wt -A pi`, it will write:

```json
"nyt-litellm": {
  "api": "openai-completions",
  "baseUrl": "https://llm-gateway.nyt.net",
  "apiKey": "<resolved via the exec: helper>",
  "models": [
    { "_launch": true, "id": "claude-sonnet-4-6", "contextWindow": 262144,
      "input": ["text", "image"], "reasoning": true }
  ],
  "_wtOwned": true
}
```

### 5. Retiring the old wrapper

Since `nyt-litellm` becomes the only pi provider on this machine:

- Delete `~/.pi/agent/activate-litellm.sh`.
- Remove the hand-written `"anthropic"` block from `~/.pi/agent/models.json`
  — wt's sync never touches provider blocks it doesn't own, so this stale
  block would otherwise persist indefinitely alongside the new one.
- Grep for any shell alias/PATH entry invoking `activate-litellm.sh` directly
  (rather than `wt -A pi` / a `pi-wt` shim) and flag it for the user to
  update by hand — shell rc files are not edited automatically as part of
  this change.

### 6. Failure behavior

A Vault/helper failure (not logged into Vault, `vault`/`jq` missing,
permission denied on the Vault KV path, no key provisioned) surfaces as a
route-resolution error and **aborts the launch** before pi is ever exec'd,
with the helper script's own stderr text included in wt's error message —
avoiding a confusing downstream 401 from pi itself. This applies uniformly
wherever `ResolveSecret` is called with this ref (`ResolveRoute` and pi's
`models.json` sync both fail closed).

## Testing

- Unit test `ResolveSecret` for all three forms, including: `exec:` success
  (stdout trimmed), `exec:` non-zero exit (error includes stderr text),
  memoization (a fake command that increments a counter file is invoked only
  once across two resolutions of the same ref in one process).
- Unit test `ResolveRoute` surfaces a `secret_ref` resolution error for a
  direct-mode provider instead of silently treating it as an empty key.
- Unit test `syncDirectProviders` (or its caller `SyncModels`) propagates a
  `secret_ref` exec error instead of skipping the provider block.
- Existing `TestSyncModelsDirectPreservesCustomProvider` and friends must
  keep passing unchanged (no behavior change for the `os.environ`/bare-name
  forms).
- Manual verification on this machine: `wt -A pi --cwd` launches pi with the
  gateway key resolved live, and `~/.pi/agent/models.json` shows the new
  `nyt-litellm` block with no `anthropic` block remaining.
