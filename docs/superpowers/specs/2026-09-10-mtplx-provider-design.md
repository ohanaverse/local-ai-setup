# Design: MTPLX local provider (issue #66)

## Goal

Add [MTPLX](https://github.com/youssofal/MTPLX) as an on-demand local model
provider so `modelman start mtplx/...` can serve a cached MTPLX model through
its OpenAI-compatible API, and wt can offer that model alongside cloud models.

## Context

- MTPLX is installed on this machine at `/Users/keith/.mtplx/bin/mtplx`.
- One model is cached: `Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality`.
- MTPLX serves an OpenAI-compatible API via `mtplx serve --model <repo> --port <port>`.
- Its default port is `8000`, which conflicts with oMLX, so this provider will
  use a dedicated port (`8003`).
- The project already has a "one local model at a time" gate (issue #65):
  `modelman start` stops any running local model before starting the requested
  one, and records it in `modelman.toml` as `[local].running_model`.

## Scope

In scope for this design:

1. Add the `mtplx` provider to the registry, with a default entry and one
   discovered model.
2. Implement on-demand start/stop/warmup for MTPLX.
3. Wire MTPLX into the benchmark isolation path (both `modelman benchmark` and
   the legacy bash benchmark scripts).
4. Add `mtplx` to the wt-readable `[local].running_model` gate.

Out of scope (see follow-up issue #79):

- Removing oMLX's standing LaunchAgent or making oMLX fully on-demand.
- Retiring the bash isolation helpers (`bin/llm-isolate-provider`,
  `bin/llm-restore-providers`).
- Consolidating all local provider lifecycle logic into a single Python module.
  This design takes a first step in that direction by introducing a Python
  lifecycle module for MTPLX and keeping the bash scripts as shims.

## Registry entry

### Provider

```toml
[[providers]]
id = "mtplx"
name = "MTPLX"
location = "local"
model_dir = "~/.mtplx/models"
protocols = ["openai-chat"]
[providers.auth]
  type = "none"
  base_url = "http://localhost:8003/v1"
```

This entry is added to `_DEFAULT_PROVIDER_TEMPLATES` and
`DEFAULT_PROVIDER_IDS` in `modelman/src/modelman/registry.py`.

### Model

```toml
[[models]]
id = "mtplx/Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality"
family = "qwen3.8"
provider_id = "mtplx"
model_name = "Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality"
location = "local"
source = "discovered"
tags = []
```

`model_name` is the full Hugging Face repo id that `mtplx serve --model`
accepts. The model id uses the same `<provider>/<rest>` convention as
OpenRouter, where `rest` may contain `/` characters.

## Lifecycle module

A new Python module, `modelman.providers.lifecycle`, owns start/stop/warmup
for supported local providers. For this PR it handles `mtplx` directly and
continues to delegate ollama/omlx/mlx_lm_server to the existing bash helper
during a transition period. The interface is shaped so those providers can
later be migrated into the module.

### Interface

```python
@dataclass
class LifecycleResult:
    provider: str
    model: str
    direct_url: str
    ok: bool
    error: str | None

def isolate(provider_id: str, model: str | None = None, *,
            extra_args: tuple[str, ...] = ()) -> LifecycleResult:
    """Stop every other local provider and start the requested one."""


def stop(provider_id: str) -> LifecycleResult:
    """Stop one provider."""


def stop_all() -> LifecycleResult:
    """Stop every local provider."""
```

### MTPLX-specific start contract

```bash
mtplx serve \
  --model "Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality" \
  --port 8003 \
  --host 127.0.0.1
```

The process runs in the background, managed by a pidfile at
`/tmp/local-ai-setup-mtplx.pid` and a log at `/tmp/local-ai-setup-mtplx.log`.
The module waits until `http://localhost:8003/v1/models` responds and lists
the target model, then warms up with a 1-token chat completion.

### MTPLX-specific stop contract

```bash
mtplx stop --port 8003 --grace-seconds 10
```

If the server is not running, `mtplx stop` exits successfully, so this is safe
to call during `stop_all`.

### Bash shim

`bin/llm-isolate-provider` keeps its existing CLI and JSON output contract
but delegates the actual work to the lifecycle module. This preserves the
legacy benchmark scripts and `modelman benchmark` without changing their call
sites.

Example delegation:

```bash
python3 -m modelman.providers.lifecycle isolate "$PROVIDER" "$MODEL"
```

The shim maps its positional/env args to the module's parameters and prints the
same JSON envelope.

## `modelman start` / `modelman stop`

`modelman/src/modelman/local_control.py` is updated to support `mtplx`:

- `SUPPORTED_PROVIDER_IDS` in `isolation.py` adds `mtplx`.
- `start_local_model` resolves the MTPLX model name and invokes the
  lifecycle module.
- Idempotency probe uses `http://localhost:8003/v1/models`.
- `stop_local_model` calls the lifecycle `stop_all()` and clears the marker.

No new CLI surface is needed; `modelman start mtplx/...` and `modelman stop`
already exist.

## LiteLLM routing

When the model is exposed via `modelman expose`, the LiteLLM row is:

```yaml
model_list:
  - model_name: mtplx/Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality
    litellm_params:
      model: openai/Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality
      api_base: http://localhost:8003/v1
```

This follows the same `openai/<model>` pattern used by oMLX.

## Benchmarks

### Legacy bash benchmarks

`benchmarks/qwen3.8-benchmark` is updated to include `mtplx`:

- `DIRECT_URLS[mtplx] = "http://localhost:8003/v1/chat/completions"`
- `DIRECT_MODELS[mtplx] = "Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality"`
- `LITELLM_MODELS[mtplx] = "mtplx/Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality"`
- `ISOLATE_ID[mtplx] = "mtplx"`
- `ISOLATE_ENV[mtplx]` is not needed; the model name is resolved from the
  registry by the lifecycle module.
- Add `mtplx` to the backend loop.

The `ornith-1.5-benchmark` script does not include MTPLX variants because the
cached model is a Qwen3.8 27B, not an Ornith-1.5 model.

### `modelman benchmark`

- `modelman/src/modelman/benchmark/isolation.py` `SUPPORTED_PROVIDER_IDS` adds
  `mtplx`.
- `modelman/src/modelman/benchmark/runner.py` `LOCAL_PROVIDERS` adds `mtplx`.
- The existing `llm-isolate-provider` shim handles the actual start/stop
  through the lifecycle module.

## MTPLX provider module

`modelman/src/modelman/providers/mtplx.py` implements the `Provider` base class
so that `modelman sync` and the TUI can discover and size cached MTPLX models.

- `model_dir` resolves to `~/.mtplx/models`.
- `is_downloaded()` checks for a non-empty directory named after the model
  (MTPLX stores models as `<org>--<model>` under `~/.mtplx/models`, e.g.
  `Youssofal--Qwen3.8-27B-MTPLX-Optimized-Quality`).
- `list_local()` enumerates `~/.mtplx/models` and maps directory names back to
  the upstream `org/model` form.
- `size_of()` sums files in the model directory.
- `download()` raises an error — MTPLX manages its own cache via the `mtplx`
  CLI; modelman only discovers what is already present.
- `delete()` removes the model directory (with the same shared-artifact guard as
  oMLX).

## wt integration

No explicit wt code change is required. wt already reads
`[local].running_model` and probes the provider's base URL. Because MTPLX uses
`http://localhost:8003/v1`, the generic OpenAI `/v1/models` probe used for
oMLX/MLX will work as long as it is not hard-coded to a single port.

If wt has a hard-coded port-to-provider map, a new `mtplx` branch is added
that probes port `8003`.

## Error handling

- Missing `mtplx` binary → clear error at start time.
- Requested model not cached → `mtplx serve` will fail to start; the module
  surfaces the command stderr.
- Port `8003` already in use (e.g., stale process) → `mtplx serve` fails; the
  module suggests `modelman stop`.
- Warmup timeout → `LifecycleResult.ok = False` with a descriptive error.

## Testing

### Unit tests

- MTPLX start/stop with mocked subprocess and HTTP probes.
- Idempotency: starting an already-running MTPLX model returns
  `already_running=True`.
- `stop_all` calls the correct stop commands for each supported provider.
- Lifecycle module preserves the JSON contract expected by bash callers.

### Contract tests

- wt Go tests read a fixture containing `[local].running_model =
  "mtplx/Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality"`.

### Manual smoke test

```bash
modelman start mtplx/Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality
curl -s http://localhost:8003/v1/models
modelman stop
./benchmarks/qwen3.8-benchmark 30
```

## Follow-up work

Issue #79 tracks the broader consolidation: moving ollama/omlx lifecycle into
the Python module, removing standing local-only services, and retiring the bash
helpers once all callers are migrated.
