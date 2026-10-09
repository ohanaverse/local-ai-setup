# Shared Model Registry

**Cross-repo design.** Affects `modelman` (this repo, becomes the registry owner)
and `agent-worktree` (becomes a read-only consumer). A copy of this spec should
be attached to any PR in `agent-worktree` that implements its side.

## Overview

Three repos — `agent-worktree` (`wt`), `modelman`, and `local-ai-setup` — each
grew their own model/provider metadata:

- `wt`'s `~/.config/agent-wt/config.toml`: Provider/Model/Agent entities, used
  to launch coding-agent CLIs with model rotation by tag/family.
- `modelman`'s `~/.config/local-ai/config.yaml` + `families/*.yaml`: family/variant
  entities, used to download/delete local model weights across ollama/llamacpp/omlx.
- LiteLLM's `~/.config/litellm/config.yaml` `model_list`: routing entries, used
  by `local-ai-setup`'s proxy setup, benchmarking, and `pi-wt`'s litellm wrapper.

This spec unifies model/provider *definitions* (not usage-state, not agent
launch config) into one file modelman owns, so all three areas of model
management — (1) provider/model registry across ollama/llama.cpp/omlx/openrouter,
(2) which of those models are exposed via LiteLLM, and (3) which model an agent
launches with — read from a single source of truth instead of three drifting
copies.

## Architecture

Four config files, each with exactly one owner:

```
~/.config/local-ai/registry.toml     # NEW — canonical, shared model/provider definitions
  Owner: modelman (read-write). wt: read-only.

~/.config/local-ai/modelman.toml     # modelman's per-machine state overlay
  Owner: modelman (read-write). Nothing else reads or writes it.

~/.config/litellm/config.yaml        # LiteLLM's own config — modelman manages model_list only
  Owner: modelman manages `model_list` entries. general_settings/database/redis
  untouched (one-time infra from local-ai-setup, out of scope here).

~/.config/agent-wt/config.toml       # wt's overlay — slimmed to Agents + prefs
  Owner: wt (read-write). Purely "configure an agent to use a chosen model."
```

modelman absorbs wt's current live-discovery logic (`internal/registry`:
`ollama list`, OpenRouter API) and the `wt config ollama` subcommand as a new
"sync with provider" capability. wt's `internal/registry` package and
`wt config ollama` are deleted once migration lands — wt becomes a pure
consumer of `registry.toml`, joining it in memory with its own `config.toml`
exactly as it joins Provider/Model/Agent today, just from two files instead
of one.

LiteLLM exposure is **independent of wt launchability**: wt's claude/codex/
copilot/opencode drivers talk directly to Ollama's own OpenAI-compatible
gateway at `:11434` today — LiteLLM is not in that path. LiteLLM matters for
unifying omlx/llama.cpp under an OpenAI-compatible endpoint, centralized
spend/usage tracking, and `pi-wt`'s existing litellm wrapper. A model can be
registered and launchable via wt without ever appearing in LiteLLM's
`model_list`.

## Schema

### `registry.toml`

```toml
[[providers]]
  id = "ollama"
  name = "Ollama"
  location = "local"
  # model_dir omitted — ollama manages its own model store internally
  [providers.auth]
    type = "none"
    base_url = "http://localhost:11434"

[[providers]]
  id = "omlx"
  name = "oMLX"
  location = "local"
  model_dir = "~/.omlx/models"
  [providers.auth]
    type = "none"
    base_url = "http://localhost:8000"

[[providers]]
  id = "llamacpp"
  name = "llama.cpp"
  location = "local"
  model_dir = "~/models"
  [providers.auth]
    type = "none"
    base_url = "http://localhost:8080"

[[providers]]
  id = "openrouter"
  name = "OpenRouter"
  location = "cloud"
  [providers.auth]
    type = "api_key"
    secret_ref = "openrouter"

[[providers]]
  id = "claude"
  name = "Claude Code"
  location = "cloud"
  [providers.auth]
    type = "native"

# codex, copilot, agy providers follow the same native-auth shape as today

[[models]]
  id = "ollama/qwen3.8:27b-mlx"
  family = "qwen3.8"
  provider_id = "ollama"
  model_name = "qwen3.8:27b-mlx"
  location = "local"
  source = "discovered"                 # or "curated"
  tags = ["code", "design"]
  cost = { kind = "free" }
  model_info = { supports_function_calling = true }

[[models]]
  id = "llamacpp/qwen3.8-27b-q4"
  family = "qwen3.8"
  provider_id = "llamacpp"
  model_name = "qwen3.8-27b-q4"
  location = "local"
  tags = ["code"]
  cost = { kind = "free" }
  fetch = { repo = "unsloth/Qwen3.8-27B-GGUF", files = ["Qwen3.8-27B-UD-Q4_K_XL.gguf"] }

[[models]]
  id = "openrouter/qwen/qwen3.8-27b"
  family = "qwen3.8"
  provider_id = "openrouter"
  model_name = "qwen/qwen3.8-27b"
  location = "cloud"
  tags = ["code", "design"]
  cost = { kind = "per_token", price_per_million_tokens = 0.40 }

[[models]]
  id = "claude/native"
  family = "claude"
  provider_id = "claude"
  model_name = "native"
  location = "cloud"
  tags = ["code", "design"]
  cost = { kind = "subscription", price_per_period = 20.0, period = "month" }
```

Field notes:

- **`id`**: `provider_id/model_name`, unchanged from wt's current scheme —
  adopted as canonical over modelman's arbitrary variant ids.
- **`family`**: cross-provider grouping, unchanged from wt's current field.
- **`tags`**: freeform `[]string`. No new typed fields for capability grouping
  (`code`, `design`, etc.) — conventions only.
- **`cost`**: typed, one of three shapes:
  - `{ kind = "free" }`
  - `{ kind = "per_token", price_per_million_tokens = <float> }`
  - `{ kind = "subscription", price_per_period = <float>, period = "<string>" }`
- **`fetch`**: optional, only present for providers modelman downloads files
  for (llamacpp, omlx). Carries `repo` + `files` (llamacpp) or `repo` +
  `quantizations` (omlx). Omitted for ollama (self-managing), openrouter/native
  (no local files).
- **`model_info`**: freeform map, carried over from modelman's existing
  LiteLLM-style capability metadata (e.g. auto-populated via `ollama show`).
- Provider **`model_dir`**: optional, local storage directory for providers
  where modelman itself places downloaded files. Omitted for self-managing
  (ollama) or storage-less (cloud) providers.

### `modelman.toml`

```toml
[model_state."llamacpp/qwen3.8-27b-q4"]
  downloaded = true
  disk_path = "/Users/keith/.cache/huggingface/hub/models--unsloth--Qwen3.8-27B-GGUF/.../Qwen3.8-27B-UD-Q4_K_XL.gguf"
  size_bytes = 17179869184
  litellm_exposed = true
```

Keyed by registry model id. Holds only per-machine mutable state: whether a
model is currently downloaded (and where/how big), and whether it's currently
exposed via LiteLLM. Never holds anything that describes the model itself —
that's `registry.toml`'s job.

### `~/.config/agent-wt/config.toml` (wt-owned, slimmed)

```toml
default_tag = "code"

[[agents]]
  name = "claude"
  supported_providers = ["claude", "ollama"]
  default_provider = "claude"

[[agents]]
  name = "pi"
  supported_providers = ["ollama", "claude", "openrouter"]
```

Unchanged shape from wt's current `Agent` struct — just Providers/Models are
gone from this file, since they now live in `registry.toml`.

## Data Flow

**Provider sync (modelman, new capability):**

1. For each provider with a discovery mechanism — ollama (`ollama list`),
   openrouter (HTTP API), llamacpp/omlx (scan `model_dir`) — modelman queries
   what's actually available.
2. New models not yet in `registry.toml` are added with `source = "discovered"`;
   `tags`/`cost` are left empty for the user to fill in; `model_info` is
   auto-populated where possible (existing `ollama show` translation).
3. **Curated wins**: sync never overwrites an existing row's `tags`/`cost`/
   `family`, whether the row is `curated` or a previously-discovered row the
   user has since edited. It only adds new rows or refreshes fields the user
   has never touched.
4. Sync updates `modelman.toml`'s `downloaded`/`disk_path`/`size_bytes` for
   local providers based on what's found on disk / in `model_dir`.

**LiteLLM exposure (modelman):** toggling a model's `litellm_exposed` writes
or removes the corresponding `model_list` entry in LiteLLM's `config.yaml`
(keyed by the registry model id as `model_name`, using the provider's
`base_url`), and flips the flag in `modelman.toml`. `general_settings` is
never touched.

**wt launch path:** loads `registry.toml` (read-only) + its own `config.toml`
(Agents+prefs), joins in memory exactly as today. Filters by
`Agent.SupportedProviders`, then by tag, then rotates. No writes to
`registry.toml`.

**One-time migration** (first phase of implementation, not this spec's
concern in detail):

- wt's current `config.toml` Provider/Model rows seed `registry.toml`; its
  Agent rows move into the slimmed `config.toml`.
- modelman's current `config.yaml` (providers) + `families/*.yaml` (variants)
  merge into `registry.toml`; `downloaded` state splits off into the new
  `modelman.toml`.
- Collision policy: modelman's data wins for fields it now owns (tags, cost,
  fetch, model_info) going forward, since modelman becomes the sole editor of
  those fields from this point on.

## Error Handling

- **Malformed `registry.toml`**: modelman fails closed everywhere except its
  own editor/TUI screens (mirroring wt's existing `wt config` repair-mode
  precedent). wt fails closed always — it has no editor for this file.
- **wt references a provider/model id not in `registry.toml`**: wt raises
  `provider %q referenced by agent %q not found in registry` at resolution
  time, matching the shape of its existing "agent has no default provider
  configured" error.
- **`fetch` resolution failure** (repo/files unreachable, `model_dir`
  missing/unwritable): surfaced in modelman's existing status-screen log
  stream; the existing queued-changes/apply-on-exit pattern isolates partial
  failures from the manifest write.
- **LiteLLM `config.yaml` missing or hand-edited oddly**: modelman parses and
  updates only `model_list` rows keyed by ids it recognizes, preserving
  unrecognized entries and all of `general_settings` verbatim. If the file is
  entirely missing, modelman errors rather than inventing one — DB/Redis/auth
  settings are outside its authority to synthesize.
- **Concurrent writes**: not a real concern — modelman is the sole writer of
  `registry.toml`/`modelman.toml`/LiteLLM's `model_list`; wt is the sole
  writer of its own `config.toml`. Each still uses atomic write (temp file +
  rename) since a single tool can be interrupted mid-write.

## Testing

- **modelman**: unit tests for the new sync logic (per-provider discovered-vs-
  curated merge, curated-wins-on-conflict), `registry.toml`/`modelman.toml`
  read/write round-trip, LiteLLM `model_list` add/remove that preserves
  unrelated file content, and the one-time migration importer (old
  `config.yaml` + `families/*.yaml` → new files) with a fixture covering id
  collisions.
- **wt**: refactor existing config-loading tests to the two-file split; add
  tests for the new "reference not found in registry" error path; existing
  rotation/tag-filter/agent-eligibility tests are otherwise unchanged since
  the in-memory model shape doesn't change.
- **Cross-repo**: no automated cross-repo CI. A shared `testdata/registry.toml`
  fixture checked into both repos (kept in sync manually) lets each repo's
  tests exercise the same real-world shape. A manual smoke-test sequence
  (modelman sync → wt launch) is the actual integration check.

## Explicitly Out of Scope

Per user direction, this is one sub-project of a larger effort and is
expected to ship as its own multi-phase implementation, not a single pass.
Deliberately deferred to separate future brainstorming sessions:

- Benchmark tooling (formalizing `local-ai-setup`'s bash scripts) — will
  consume `registry.toml` once it exists, but its design is separate.
- Usage/spend tracking beyond what LiteLLM's own admin UI already provides,
  and any relationship to wt's existing `usage.jsonl`/rotation state.
- Any change to LiteLLM's `general_settings` (DB/Redis/auth) — that's
  one-time infra already covered by `local-ai-setup`'s docs.
- Any change to wt's actual routing mechanism (still direct-to-ollama-gateway
  for most agents; LiteLLM stays optional/orthogonal per the "LiteLLM scope"
  decision above).
