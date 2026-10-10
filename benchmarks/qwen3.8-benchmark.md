# qwen3.8 Benchmark — Comparing the Four Local + Cloud Variants

**Status**: Reference doc for benchmarking the four qwen3.8 model variants on this machine. Tracks setup, the benchmark script, results, and the bugs encountered while getting it to work.

---

## Why This Exists

The LiteLLM proxy at `localhost:4000` routes the same `model_name` to four different backends:

| Backend | Model | Format | Where it lives |
|---|---|---|---|
| **Ollama** | `ollama/qwen3.8:27b-mlx` | MLX (nvfp4, 18 GB) | `~/.ollama/models/` |
| **oMLX** | `omlx/Qwen3.8-27B-4bit` | MLX 4-bit | `~/.omlx/models/mlx-community/Qwen3.8-27B-4bit/` |
| **MTPLX** | `mtplx/Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality` | MLX | served by `mtplx serve` on `:8003` |
| **OpenRouter** | `openrouter/qwen/qwen3.8-{flash,27b,2.4t-a95b,max}` | cloud API | `https://openrouter.ai/api/v1` |

> **llama.cpp retired 2026-09-07** (issue #33) — no longer a benchmark
> backend; artifacts + re-enable steps in
> [provider-artifacts.md](../docs/reference/provider-artifacts.md). Dated result
> tables below keep their historical llama.cpp rows.

These are *different quantizations and runtimes* of the same Qwen3.8 27B model. Benchmarking them side-by-side tells us:
- Which backend is fastest on this Apple Silicon hardware
- How much overhead the LiteLLM proxy adds
- Where the local-vs-cloud tradeoff actually lies

---

## The Isolation Problem

The three local backends share Apple Silicon's GPU and unified memory. Running all three with their models loaded simultaneously means:
- ~50 GB of GPU/RAM contention for three ~16-18 GB models
- Thermal throttling affecting later tests
- Unfair comparisons because the *first* test gets a cold cache and *later* tests don't

**The benchmark runs each local backend in isolation**: before each test, all local models are unloaded (Ollama) or fully stopped (oMLX, MTPLX). Only the one being tested has its model loaded in GPU/RAM.

| Backend | "Stop" mechanism | What's left running |
|---|---|---|
| **Ollama** | `ollama stop <model>` | Daemon on `:11434`, but model is unloaded from GPU/RAM |
| **oMLX** | `omlx stop` | Nothing — service halts entirely |
| **MTPLX** | `mtplx stop --port 8003 --grace-seconds 10` | Nothing — the serve process exits |
| **OpenRouter** | n/a | Always live (cloud) |

The Ollama daemon keeps port 11434 bound even after the model is unloaded — that's expected. Isolation checks `ollama ps` for an empty model list rather than the port.

---

## The Script

**Location**: `benchmarks/qwen3.8-benchmark` in this repo. It is not installed on PATH; run it by path (`./qwen3.8-benchmark` from `benchmarks/`). It sources `lib/benchmark-common.sh` relative to its own location, so the working directory does not matter.

**Usage**:
```bash
./qwen3.8-benchmark               # default: 200 max_tokens
./qwen3.8-benchmark 100           # custom max_tokens
```

**What it does**:
1. Brings ollama, oMLX and LiteLLM up (`llmbench provider restore`)
2. Direct rows — for each local backend (ollama, omlx, mtplx):
   - Stops the other local services
   - Loads only the one being tested (`llmbench provider isolate`)
   - Runs the prompt against the backend directly
   - Measures TTFT (time to first token), total time, tokens, throughput
3. LiteLLM rows — isolates each local backend again and runs the same prompt through LiteLLM
4. Restores all local services
5. Tests OpenRouter (cloud, always live), each model direct and through LiteLLM

**Output**: Markdown file in `/tmp/qwen3.8-benchmark-<timestamp>.md`

**The prompt** (same for all backends):
```
Explain in detail the differences between REST and GraphQL APIs,
including trade-offs in caching, partial responses, and tooling.
Be thorough.
```

Plus `temperature=0.0` for reproducibility.

---

## Latest Results

Captured 2026-08-27 with **3 passes at `max_tokens=200`** (15s cool-down between passes, full isolation between local backends). Each cell is the **median of 3 runs**. OpenRouter now covers all four qwen3.8 variants, each tested both directly against the OpenRouter API and via LiteLLM.

### Throughput (tokens/sec) — median of 3 passes

| Backend | Direct | Via LiteLLM |
|---|---:|---:|
| **ollama** (qwen3.8:27b-mlx, MLX) | 20.53 | 20.40 |
| **omlx** (Qwen3.8-27B-4bit, MLX) | 14.19 | 13.47 |
| **llama.cpp** (Qwen3.8-27B-UD-Q4_K_M.gguf) | 8.58 | 9.43 |
| **openrouter flash** | 66.71* | 74.79 |
| **openrouter 27b** | 53.85 | 35.87 |
| **openrouter 2.4t-a95b** | 91.83 | 78.55 |
| **openrouter max** | 39.47 | 40.11 |

\* `flash` direct: n=1 (rate-limited upstream in 2 of 3 passes).

### TTFT (ms) — median of 3 passes

| Backend | Direct | Via LiteLLM |
|---|---:|---:|
| **ollama** | 629 | 581 |
| **omlx** | 42 | 34 |
| **llama.cpp** | 675 | 683 |
| **openrouter flash** | 918* | 13,808 |
| **openrouter 27b** | 611 | 1,136 |
| **openrouter 2.4t-a95b** | 1,172 | 1,243 |
| **openrouter max** | 1,376 | 1,411 |

### Total time (ms) — median of 3 passes

| Backend | Direct | Via LiteLLM |
|---|---:|---:|
| **ollama** | 219,134 | 10,384 |
| **omlx** | 14,138 | 14,881 |
| **llama.cpp** | 23,964 | 21,881 |
| **openrouter flash** | 3,916* | 16,482 |
| **openrouter 27b** | 4,410 | 16,278 |
| **openrouter 2.4t-a95b** | 3,350 | 4,231 |
| **openrouter max** | 6,887 | 6,408 |

### Observations

- **Ollama still wins among local backends** — median 20.53 tok/s direct, 20.40 via LiteLLM. The MLX nvfp4 quantization has the best speed/quality trade-off for this 27B model.
- **oMLX second** — 14.19 tok/s direct. Still slower than Ollama despite being purpose-built for MLX; the 4-bit quantization is slower than Ollama's nvfp4 on this hardware.
- **llama.cpp slowest local** — 8.58 tok/s direct. The Q4_K_M GGUF is a heavier quantization than the MLX 4-bit, and CPU/GPU offload overhead is higher.
- **OpenRouter 2.4t-a95b is the fastest overall** — 91.83 tok/s direct (MoE with 95B active params on cloud hardware). Flash is close behind at 74.79 via LiteLLM.
- **OpenRouter 27b and max are mid-pack** — 35–54 tok/s, comparable to or faster than the local backends but with cloud latency.
- **LiteLLM overhead is small** — for local backends the direct-vs-LiteLLM gap is within noise (ollama 20.53 vs 20.40, omlx 14.19 vs 13.47). For OpenRouter the gap varies by provider routing.
- **oMLX has the lowest TTFT** — 42ms direct vs 629ms for Ollama and 675ms for llama.cpp. Best choice for low-latency interactive use.
- **OpenRouter TTFT is dominated by cloud round-trip** — 600ms–1.4s direct, and flash via LiteLLM spiked to 13.8s (upstream provider variability).

### Caveats

- Three passes is the bare minimum for stable medians; OpenRouter numbers are especially variable (upstream provider is non-deterministic — the same prompt can hit different providers with different hardware).
- `flash` direct was rate-limited upstream (429 from Alibaba's shared pool) in 2 of 3 passes, so its direct numbers are n=1 and unreliable.
- The Ollama token counts (`tokens=3981/4485/4701`) are dominated by reasoning tokens (the model thinks extensively despite `max_tokens=200`). Divide by visible content tokens for fairer comparison.
- The bench ran on Apple Silicon; numbers will differ on NVIDIA/CUDA hardware.


## Multiple-Pass Mode (recommended for stable numbers)

For reliable measurements, run the benchmark 3-5 times and look at the median. A wrapper script, `benchmarks/qwen3.8-benchmark-multi`, handles this:

```bash
./qwen3.8-benchmark-multi 3           # 3 passes (default)
./qwen3.8-benchmark-multi 5 200       # 5 passes, 200 max_tokens each
./qwen3.8-benchmark-multi 3 200 30    # 3 passes, 200 max_tokens, 30s cool-down
```

The script runs N passes with configurable cool-down between them (default 15s, so the machine can cool slightly). Each pass writes a separate `/tmp/qwen3.8-benchmark-<timestamp>.md` file.

Compare results across files for stability. The script does not auto-aggregate; use a markdown viewer or `grep` to extract the throughput column from each result file:

```bash
for f in $(ls -t /tmp/qwen3.8-benchmark-*.md | head -3); do
    echo "=== $f ==="
    grep -E "^\| (ollama|omlx|llama\.cpp|openrouter)" "$f"
    echo
done
```

---

## Problems Encountered and Solutions

This section captures the bugs hit while getting the benchmark to work, so future sessions don't repeat them.

### 1. `/bin/bash` is macOS bash 3.2 — doesn't support `declare -A`

**Symptom**: `declare: -A: invalid option` when the script runs.

**Cause**: macOS ships `/bin/bash` as version 3.2.57, which predates bash 4.0 (released 2009). Associative arrays require 4.0+.

**Fix**: Changed shebang to `/opt/homebrew/bin/bash` (Homebrew's bash 5.3).

```bash
#!/opt/homebrew/bin/bash   # not /bin/bash
```

### 2. `declare -A X=(...)` doesn't reliably register string keys

**Symptom**: `${X[ollama]}` returns empty even though the array was "declared".

**Cause**: When bash sees `declare -A X=( [k]=v ... )`, it creates the array but the keys come in as quoted strings. Some bash versions don't rekey them properly after the declare.

**Fix**: Declare first, assign second.

```bash
# WRONG (works in some bash versions, fails in others):
declare -A X=([ollama]="http://...")

# RIGHT (works everywhere):
declare -A X
X[ollama]="http://..."
```

### 3. `$X[ollama]` vs `${X[ollama]}` — the bash subscript trap

**Symptom**: `curl` exited with code 3 ("URL malformed"), passing `[ollama]` as the URL.

**Cause**: **Bash treats `$X[ollama]` as the expansion of `$X` followed by literal `[ollama]`.** The array subscript syntax requires curly braces. Without them, the variable expands to empty (since `X` is an associative array with no element at index `[ollama]` after string parsing), and the `[ollama]` text stays in place.

This is invisible to `bash -x` debugging because the broken expansion looks syntactically correct in the trace:
```bash
+ curl -s -m 120 '[ollama]' -H 'Content-Type: application/json' ...
```

**Fix**: Always use curly braces for array subscripts:

```bash
# WRONG:
"$X[ollama]"

# RIGHT:
"${X[ollama]}"
```

The `${backend}` form (`${DIRECT_URLS[$backend]}`) was already correct — it's only the literal-key form that needed fixing.

### 4. Ollama model-load detection

**Symptom**: After `ollama stop`, the warmup request timed out at 120s.

**Cause**: Two issues compounded:
1. The long benchmark prompt forces a full prefill pass on the 18 GB model, which takes 60+ seconds on cold load.
2. `ollama ps` was polled asynchronously, but the warmup request was running in the background — the script returned "model loaded" before the warmup actually finished.

**Fix**: Use a *short* prompt for warmup ("hi" loads the model in ~6 seconds) and run the warmup *synchronously*, checking for `"done":true` in the response:

```bash
local warmup='{"model":"...","messages":[{"role":"user","content":"hi"}],"stream":false,"max_tokens":1,"temperature":0}'
local resp
resp=$(curl -s -m 120 "${DIRECT_URLS[ollama]}" -H "Content-Type: application/json" -d "$warmup" 2>/dev/null)
if echo "$resp" | grep -q '"done":true'; then
    echo "    ollama: model loaded"
    return 0
fi
```

### 5. llama.cpp `/v1/models` lies about model readiness

**Symptom**: `llama.cpp` warmup timed out at 120s even though the LaunchAgent loaded in 5 seconds.

**Cause**: `/v1/models` returns a list of available models *immediately*, before the GGUF is actually loaded into Metal. The actual model load takes 15-30 seconds.

**Fix**: Use a synchronous warmup chat request and check for `"object":"chat.completion"` in the response — that response only comes after the model is fully loaded and ready.

### 6. Ollama token counts include reasoning tokens

**Symptom**: Ollama reports `tokens=4040` when `max_tokens=30` was requested.

**Cause**: The Qwen3.8 MLX model uses thinking/reasoning mode by default. Ollama counts both `reasoning_tokens` and final output tokens under `completion_tokens` in the `usage` field.

**Fix**: This is correct behavior, not a bug. To get clean numbers:
- Disable thinking mode in the model (if the model supports it via `think: false` parameter)
- Or accept that throughput numbers need context: the actual visible output is small, but the model is doing more work than other backends for the same output

### 7. Service-restart side effect

**Symptom**: After `llm-restart ollama`, Ollama sometimes briefly shows exit code `-15` in `launchctl list`.

**Cause**: The `launchctl kickstart -k` sends SIGTERM, then `KeepAlive=true` in Ollama's plist respawns it. The `-15` is captured between the SIGTERM and the respawn.

**Fix**: Not a bug — just wait a moment for the respawn, or use `llm-restart` which has built-in verification. The fix in `llm-restart` was to detect Ollama via `launchctl list | grep com.ollama.ollama` instead of `brew services` (Ollama is auto-managed by macOS, not brew services on this machine).

---

## Re-running the Benchmark

To get fresh numbers:

```bash
# from: benchmarks/ (the script brings the local services up itself)

# Run a single 200-token pass
./qwen3.8-benchmark 200

# Or run multiple passes for stability
for i in 1 2 3; do
    ./qwen3.8-benchmark 200
    sleep 10  # cool-down between passes
done
```

Results are timestamped in `/tmp/qwen3.8-benchmark-<timestamp>.md`. The latest run is the most recent file:

```bash
ls -t /tmp/qwen3.8-benchmark-*.md | head -1
```

---

## Extending the Benchmark

To add another model (e.g., a different qwen3.8 quantization):

1. Edit `benchmarks/qwen3.8-benchmark`
2. Add an entry to the `DIRECT_URLS`, `DIRECT_MODELS` and `LITELLM_MODELS` associative arrays
3. Add the key to `ISOLATE_ID` (the `llmbench provider` id that isolates it) and to both `for backend in` loops
4. Add the model to `~/.config/litellm/config.yaml` first (the LiteLLM proxy won't know about it otherwise)

For a totally new provider (say, vLLM), add it to:
- `DIRECT_URLS`, `DIRECT_MODELS`, `LITELLM_MODELS`
- `ISOLATE_ID` and both `for backend in` loops
- llmbench: a `Backend` with the provider's start/stop/warmup in `llmbench/src/llmbench/providers/lifecycle/backends/`, registered in `BACKENDS` (see the `adding-a-benchmark-backend` skill). The script has no service-management code of its own; `isolate_one` calls `llmbench provider isolate`.

For a new OpenRouter model, add its slug to the `OPENROUTER_MODELS` array in the script and a matching `openrouter/<slug>` entry to `~/.config/litellm/config.yaml`. The OpenRouter loop tests each model both directly and via LiteLLM automatically.

---

## Related

- **Main setup doc**: [`../docs/Local AI Setup 2026-08-25.md`](../docs/archive/Local%20AI%20Setup%202026-08-25.md) — covers LiteLLM, Ollama, oMLX, llama.cpp, OpenRouter setup and auto-start
- **Service management**: `uv run --directory llmbench llmbench provider restore` (from the repo root) — brings ollama, oMLX and LiteLLM back up
- **LiteLLM config**: `~/.config/litellm/config.yaml` — the four model entries
