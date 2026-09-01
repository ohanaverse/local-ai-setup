# Route wt agent traffic through the LiteLLM proxy

## Summary

Add a `[gateway]` section to `~/.config/agent-wt/config.toml` so `wt`-launched
agents can route all non-native model traffic through the LiteLLM proxy at
`:4000` instead of directly at Ollama (`:11434`). At the same time, make
`wt` honor `modelman`'s `litellm_exposed` flag: only exposed models (and native
models) appear in the rotation/picker. This closes the gap where most agent
usage was invisible to the LiteLLM dashboard and `modelman usage report` showed
the majority of launches as WT-only.

## Background

- The LiteLLM proxy is already running on `:4000` with Postgres spend logging.
- `modelman` writes `model_list` entries into `~/.config/litellm/config.yaml`
  and tracks `litellm_exposed` in `~/.config/local-ai/modelman.toml`.
- `wt` currently launches agents against Ollama directly: each driver
  hardcodes its gateway URL (`ANTHROPIC_BASE_URL=http://localhost:11434` for
  Claude Code, `localhost:11434/v1` for codex/opencode/copilot, etc.).
- The result is that only ad-hoc/script traffic reaches LiteLLM; agent traffic
  bypasses it. `modelman usage report` therefore reconciles very little.

The fortunate coincidence that makes this straightforward: `modelman` already
uses the registry model id (e.g. `ollama/qwen3.8:27b-mlx`) as the client-facing
`model_name` in LiteLLM. `wt` already carries that same id, so in gateway mode
agents can pass it straight through.

## Decisions

1. **Gateway mode is opt-in per machine**, controlled by a new `[gateway]`
   section in `~/.config/agent-wt/config.toml`. Missing section = today's
   direct-to-Ollama behavior, unchanged.
2. **Non-native models route through the configured gateway**, using the
   registry model id as the client-visible model name.
3. **Native models stay unchanged** — they use the agent's own subscription and
   are not proxyable.
4. **The model catalog is filtered by `litellm_exposed`**, read from
   `~/.config/local-ai/modelman.toml`. The filter applies unconditionally:
   `wt` only offers models that `modelman` has exposed (or native models).
5. **pi's `models.json` provider block is updated in gateway mode** so pi
   talks to LiteLLM instead of Ollama. This breaks the previous
   "preserve pi's provider config verbatim" contract, but only when gateway
   mode is enabled.
6. **Fail fast, no silent fallback** to direct Ollama. A fallback would
   quietly recreate the bypass we are eliminating.

## Components

### 1. `~/.config/agent-wt/config.toml` — `[gateway]` section

```toml
[gateway]
mode    = "litellm"                # "direct" (default) | "litellm"
url     = "http://localhost:4000" # LiteLLM proxy base URL
api_key = "sk-…"                  # master key or a wt-specific virtual key
```

- `mode`: absent or `"direct"` = legacy behavior.
- `url`: base URL with **no trailing `/v1`**. Each driver appends its own
  protocol suffix (`claude` uses the root; `codex`/`opencode`/`copilot` use
  `/v1`).
- `api_key`: bearer token for LiteLLM. On this machine it can be the master key
  from the LaunchAgent plist or a dedicated "wt" virtual key created in the
  `/ui` dashboard. Virtual keys become per-agent later (sub-project 3).

### 2. `wt` reads `~/.config/local-ai/modelman.toml`

`wt` becomes a **read-only consumer** of `modelman.toml`, joining it with
`registry.toml` the same way it already joins `config.toml` with
`registry.toml`.

New contract on the file:

| File | Owner (writes) | New consumer | Purpose |
|---|---|---|---|
| `~/.config/local-ai/modelman.toml` | `modelman` | `wt` (read-only) | Per-machine state: `litellm_exposed` flags |

Behavior:

- Missing file → treat every non-native model as unexposed (only native
  models appear in `wt`).
- Malformed file → fail on load with a clear error, consistent with other
  config files.
- `wt` never writes this file.

### 3. Config-layer helpers

Add to the in-memory `config.Config`:

```go
type GatewayConfig struct {
    Mode    string // "direct" | "litellm"
    URL     string
    APIKey  string
}

func (c *Config) Gateway() GatewayConfig
func (c *Config) IsExposed(modelID string) bool
func (m Model) IsNative() bool // already exists
```

`IsExposed` returns `true` for native models and for any model whose
`[model_state.<id>].litellm_exposed = true` in `modelman.toml`.

### 4. Model catalog filtering

Apply in the picker (`internal/tui`) and in non-TUI model resolution
(`cmd/wt/resolve.go`):

```go
eligible := filter(cfg.Models, func(m Model) bool {
    return m.IsNative() || cfg.IsExposed(m.ID)
})
```

A non-native model that is not exposed is not offered. Native models are
always offered.

### 5. Driver routing changes

For each non-native driver, when gateway mode is enabled:

| Driver | Current direct URL | Gateway URL construction | Model argument |
|---|---|---|---|
| `claude` | `ANTHROPIC_BASE_URL=http://localhost:11434` | `[gateway].url` (root path) | registry `m.ID` |
| `codex` | `model_providers.*.base_url=11434/v1` | `[gateway].url` + `/v1` | registry `m.ID` |
| `opencode` | base URL `11434/v1` | `[gateway].url` + `/v1` | registry `m.ID` |
| `copilot` | `COPILOT_PROVIDER_BASE_URL=11434/v1` | `[gateway].url` + `/v1` | registry `m.ID` |
| `pi` | writes/uses pi's `models.json` ollama provider | update `baseUrl`/`apiKey` in `models.json` to gateway | registry `m.ID` |

For direct mode, behavior stays exactly as today.

Claude specifics:

```go
lc.Env = append(lc.Env,
    "ANTHROPIC_AUTH_TOKEN="+gateway.APIKey,
    "ANTHROPIC_API_KEY=",
    "ANTHROPIC_BASE_URL="+gateway.URL,
)
lc.Args = append(lc.Args, "--model", m.ID)
```

pi specifics in gateway mode:

- `SyncModels` updates the `providers.ollama.baseUrl` field to
  `[gateway].url + "/v1"` and `apiKey` to `[gateway].api_key`.
- New entries use `m.ID` as the `id` field (instead of `m.ModelName`).
- In direct mode, `SyncModels` preserves the existing provider block and uses
  `m.ModelName` as today.

### 6. `BuildLaunchCmd` plumbing

`BuildLaunchCmd` already receives the resolved `config.Model`. It needs access
to the loaded `config.Config` (or at least the gateway + exposure state) to
pass the right URL/model-id to drivers. If the signature change is too noisy,
an internal `Gateway` object can be threaded through the existing launch paths.

The spec leaves the exact Go signature to the implementation plan, but the
behavioral contract is:

- Direct mode: every driver behaves exactly like today.
- Gateway mode: non-native drivers use the gateway URL + registry id.

## Data flow

```
[registry.toml]  ──┐
                  ├──►  wt in-memory config  ──►  picker / rotation
[modelman.toml] ──┘          │
                             │
                             ▼
                    BuildLaunchCmd(cfg, m, yolo)
                             │
              ┌──────────────┼──────────────┐
              ▼              ▼              ▼
           claude          codex        opencode / copilot / pi
              │              │              │
              └──────────────┴──────────────┘
                             │
                             ▼
                   LiteLLM proxy :4000
                             │
           ┌─────────────────┼─────────────────┐
           ▼                 ▼                 ▼
      ollama :11434   llama.cpp :8080   oMLX :8000   OpenRouter
                             │
                             ▼
                  LiteLLM_SpendLogs (Postgres)
                             │
           ┌─────────────────┴─────────────────┐
           ▼                                   ▼
    LiteLLM /ui dashboard           modelman usage report
```

## Error handling

- **Gateway configured but proxy down**: the agent fails at first request with
  a connection error. Optional: add a pre-flight health check before spawning,
  surfacing something like `litellm proxy at http://localhost:4000 is not
  reachable`.
- **Exposed model missing from LiteLLM `model_list`**: LiteLLM returns its own
  400/invalid model error. This is a modelman↔LiteLLM config mismatch, not a
  `wt` bug.
- **`modelman.toml` malformed**: `wt` exits on load with a clear config error.
- **No `[gateway]` section**: legacy behavior, no new errors.

## Testing

Add or update tests in `internal/agents/`:

- `claude_test.go`, `codex_test.go`, `opencode_test.go`, `copilot_test.go`:
  assert gateway-mode env construction (URL, key, model id) vs direct-mode
  unchanged.
- `pi_models_test.go`: assert gateway mode writes `baseUrl`/`apiKey` and uses
  `m.ID`; assert direct mode preserves provider block and uses `m.ModelName`.
- `internal/config/` tests: loading `[gateway]`, `IsExposed` join logic,
  malformed modelman.toml handling.
- `internal/tui` / `cmd/wt/resolve.go` tests: picker/rotation only includes
  exposed + native models.
- Existing direct-mode tests must continue to pass unchanged.

## Non-goals

- Per-agent virtual keys (sub-project 3).
- Propagating `[models.cost]` from `registry.toml` into LiteLLM cost fields
  (sub-project 2).
- Migrating the 9 hand-managed LiteLLM entries into `registry.toml`.
- Fallback routing to Ollama if LiteLLM is down.
- Changing `modelman expose`/`unexpose` semantics.

## Cross-repo impact

| Repo | Change |
|---|---|
| `agent-worktree` | This spec: new config section, driver routing, exposure filter, tests. |
| `local-ai-setup` | Update `00-config-map.md` to list `wt` as a consumer of `modelman.toml`. Update `06-wt-agents-and-models.md` with `[gateway]` setup and exposure-filter behavior. Update `07-usage-and-spend.md` to note that WT-only rows should shrink to native/unexpected direct traffic after enabling the gateway. |
| `modelman` | No code change. README may mention that `litellm_exposed` now also controls `wt` visibility. |

## Migration / rollout on this machine

1. Create a virtual key in the LiteLLM `/ui` dashboard (or reuse the master
   key from the LaunchAgent plist).
2. Add `[gateway]` to `~/.config/agent-wt/config.toml`.
3. Expose the models you want in `wt` with `uv run modelman expose <id>`.
4. Rebuild `wt` over `~/.local/bin/wt` (per guide 08 §4).
5. Verify with `wt -W <worktree> -A claude -M ollama/qwen3.8:27b-mlx` and then
   `modelman usage report --days 1` to confirm the launch is no longer WT-only.
