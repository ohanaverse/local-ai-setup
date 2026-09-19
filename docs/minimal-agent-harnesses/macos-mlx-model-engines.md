Research wasn't available for this turn, so here's an Apple Silicon expansion built from targeted searches. It's dense but I've kept it structured the same way as PART 2. Note that MTPLX is a real project (not a typo) and that Ollama now has an MLX backend — both change the Mac landscape versus the earlier report.

## 1. Apple Silicon fundamentals that change the tuning logic

Three things are different from a discrete-GPU box:

**Unified memory, but not all of it.** macOS caps how much unified memory the GPU may wire down; the default is roughly 75% of RAM (measured 78% on an M2 Max 32 GB), and the rest is headroom the kernel keeps for itself. A model that "fits" in RAM can still refuse to load because of that ceiling. The override is `sudo sysctl iogpu.wired_limit_mb=<MB>` — no reboot required, and Ollama/llama.cpp logs will show a larger `recommendedMaxWorkingSetSize` afterwards. Reset with `=0`, and leave 8–16 GB for macOS rather than setting it to 100% of RAM. The general recommendation is no more than about 70% of physical RAM on smaller machines; Apple's own example for a 192 GB machine is 184320 MB. Two schools of thought on persistence: a LaunchDaemon that runs the sysctl at boot works, but some recommend treating it as a session-only change because it removes the headroom macOS uses to stay responsive. Persisting via `/etc/sysctl.conf` may require disabling SIP, so many users just re-run the command after reboot.

**Prefill is the bottleneck, not decode.** Macs have high memory bandwidth (good for token generation) but far less matmul throughput than an NVIDIA card, so processing a 20–30K-token agent prompt is slow. That is exactly why prompt/prefix caching is *more* important on a Mac than anywhere else. oMLX's author describes the failure mode directly: coding agents send dozens of requests with shifting prefixes, every existing MLX server invalidated the KV cache, and a few turns in you were waiting 30–90 s per response — SSD-backed prefix restore brought TTFT down to 1–3 s.

**MLX vs Metal-llama.cpp.** MLX is Apple's array framework built around unified memory and, with the M5 chips, uses the GPU Neural Accelerators (dedicated matrix-multiplication units) for faster inference. In 2026 most Mac stacks share MLX under the hood, and the gap versus llama.cpp is widest on prompt processing / TTFT because MLX uses the M5 accelerators and llama.cpp doesn't; on M1/M2 the gap is smaller. ANE (the Neural Engine) is essentially not used by any of these LLM stacks — the "neural accelerators" on M5 are in the GPU.

## 2. Per-stack tuning

### Ollama (Metal backend → MLX backend)

The big 2026 change: Ollama 0.19 (preview, March 30–31 2026) replaced the llama.cpp Metal backend with MLX on Apple Silicon, reporting a 93% decode and 57% prefill improvement in its own benchmarks. Ollama's blog says M5/M5 Pro/M5 Max chips get the GPU Neural Accelerators for both TTFT and generation speed, and that the test used Qwen3.5-35B-A3B in NVFP4 versus Q4_K_M on 0.18, with int4 reaching 1851 tok/s prefill and 134 tok/s decode. Independent measurements are more modest: typically 20–40% higher generation throughput than the Metal backend on the same hardware, and the MLX backend has a hard 32 GB unified-memory requirement — below that Ollama silently falls back to llama.cpp Metal with no error, so check `ollama serve` logs for "using mlx backend".

Practical config (settings below apply to both backends unless noted):

```bash
# ~/.zshrc, or `launchctl setenv` if using the menu-bar app
export OLLAMA_USE_MLX=1              # enable MLX backend (0.19+, ≥32 GB) — verify in logs
export OLLAMA_CONTEXT_LENGTH=65536   # real window; Ollama truncates silently otherwise
export OLLAMA_FLASH_ATTENTION=1
export OLLAMA_KV_CACHE_TYPE=q8_0     # Metal backend; verify support on MLX path
export OLLAMA_NUM_PARALLEL=1         # one agent → preserve cache locality
export OLLAMA_MAX_LOADED_MODELS=1
export OLLAMA_KEEP_ALIVE=-1          # keep the model resident
```

Notes: prefer baking `PARAMETER num_ctx` into a Modelfile for reliability; NVFP4 is a new quant option on the MLX path that Ollama credits to NVIDIA contributors. Ollama remains the easiest way to get an Anthropic Messages endpoint for Claude Code (≥0.14). I could not verify whether `OLLAMA_KV_CACHE_TYPE` is honored by the MLX backend — treat KV-quant on Ollama-MLX as unverified and check memory in `ollama ps`.

### llama.cpp / llama-server (Metal)

`brew install llama.cpp` ships a Metal build; building from source with `-DGGML_METAL=ON` is only needed for bleeding-edge features. A representative agent-serving command:

```bash
llama-server -m model.gguf \
  -c 65536 -ngl 99 -fa on \
  --cache-type-k q8_0 --cache-type-v q8_0 \
  --jinja --reasoning-format auto \
  --cache-reuse 256 --cache-ram -1 \
  -b 2048 -ub 512 -t 8 \
  --parallel 1 --port 8080
```

Mac-specific points:
- **Drop x86-server flags.** `--numa`, `--main-gpu`, `--tensor-split`, `--split-mode`, `--cpu-mask` etc. are no-ops or harmful on a single-GPU unified-memory Mac.
- **Check `iogpu.wired_limit_mb` before filing "Metal allocation failed" issues** — a 64 GB Mac defaults to roughly 48 GB usable for GPU.
- `-t` should match performance cores (8 on M-series Pro/Max), not total cores.
- Raise `-b`/`-ub` to improve prefill throughput on Metal if memory allows; batch size is the main prefill lever.
- `--n-cpu-moe` is less relevant on a Mac (everything is already in the same memory pool) — you'd only use it if the wired limit blocks a large MoE.
- The earlier report's caveats still apply: KV-quant requires flash attention; a Metal + KV-offload crash has the `--cache-ram 0` / `--no-kv-offload` workaround; changing the front of the prompt busts the cache.

### mlx-lm (`mlx_lm.server`)

The reference MLX server; OpenAI-compatible on port 8080 by default. Install `pip install -U mlx-lm` and pull 4/6/8-bit models from the mlx-community Hugging Face organization; some tokenizers need `--trust-remote-code`.

```bash
mlx_lm.server --model mlx-community/Qwen3-Coder-30B-A3B-Instruct-4bit \
  --port 8080 \
  --prompt-cache-size 24 --prompt-cache-bytes 8589934592 \
  --kv-bits 8 --kv-group-size 64 --quantized-kv-start 4096 \
  --prefill-step-size 2048
```

What the flags do: the server's docs state `--kv-group-size` sets the group size and `--quantized-kv-start` the token position where quantization begins (default 5000), that attention on a quantized cache isn't fused so it keeps a `prefill_step_size × context_length` score matrix (decrease `--prefill-step-size`), and that a quantized KV cache doesn't support batching — with `--kv-bits` requests are processed one at a time. For an agent, single-request mode is fine and prompt caching matters more; a 2026 PR fixed `--prompt-cache-bytes` not being honored by the server's LRU cache, and a related issue notes reusing a cached conversation deep-copied the KV and doubled peak memory — so run a recent version. Request-level knobs (`max_tokens` default 512 — raise it; `temperature` default 0.0; `top_k`, `min_p`, `repetition_penalty`, `draft_model` and `num_draft_tokens` for speculative decoding) are passed in the JSON body. For CLI use, `--max-kv-size` gives a rotating fixed-size KV cache (512 = tiny/low quality, 4096+ better) and `mlx_lm.cache_prompt` saves a prompt's KV to a safetensors file for reuse, but `max-kv-size` has been requested for the server and wasn't exposed there as of late 2025. Tool calling: mlx-lm relies on the model's chat template and returns whatever the model emits; oMLX documents that it supports all function-calling formats available in mlx-lm, which implies the parsing layer is upstream but harness-side normalization may still be needed.

### oMLX

Best fit for coding agents on a Mac today. It's an MLX-based server with a native menu-bar app, a two-tier KV cache (hot RAM + cold SSD) that persists blocks across requests and server restarts, and both OpenAI `/v1/chat/completions` and Anthropic `/v1/messages` endpoints, making it a drop-in backend for Claude Code, Cursor, OpenCode, and Codex. It started from vllm-mlx v0.1.0 and added multi-model serving, tiered caching, VLM support, an admin panel, and the menu-bar app. Activity is very high — 118 tagged releases since a February 2026 launch and ~20K stars by August — and it runs continuous batching instead of queuing requests, keeping the KV cache alive on disk. Tool calling auto-detects Llama/Qwen/DeepSeek-style formats, suppresses tool-call markup from streamed text, and emits structured tool calls after the turn completes; it requires the chat template to accept `tools`. It reads the standard Hugging Face cache and your LM Studio folder, so no re-download is needed. Install via the DMG (`omlx start` runs as a background service on `localhost:8000/v1`); point `--model-dir` at a folder of MLX-format model subdirectories. An experimental distributed mode splits one model across multiple Macs over Thunderbolt using MLX pipeline parallelism.

### MTPLX

Real project, not a typo. MTPLX (youssofal/MTPLX, Apache-2.0) is a native macOS app and CLI that speeds up decoding on Apple Silicon by using a model's own multi-token-prediction heads for speculative decoding — no separate draft model — with reported 1.6–2.24× decode gains and no quality loss, first released in 2025. The DMG at mtplx.com sets up its own Python engine, recommends a model that fits your memory, installs fan control, and measures your machine to pick the fastest decoding depth; the recommended coding model is Qwen 3.8 27B "Optimized Speed" (4-bit dynamic quant), with "Optimized Quality" (8-bit) alongside. Its Metal verify kernels are also used inside oMLX's "Lightning MTP" path, so you get MTP acceleration in oMLX too. Scope: MTPLX is a decode accelerator with an OpenAI-compatible endpoint; it does not (to my knowledge) offer oMLX's persistent SSD prefix cache or Anthropic endpoint, so for agent workloads the usual pairing is oMLX (caching) with MTP-capable models, or MTPLX alone when decode speed matters more than long-context prefill. Note the attribution requirement if you ship a product on it.

### LM Studio (MLX engine)

The GUI route: filter the model marketplace by MLX, load, and flip on the OpenAI-compatible server; version 0.4.1+ also serves an Anthropic-compatible endpoint (from the earlier report). KV-quant, flash attention, context length, and speculative decoding are set in the loader panel. Good default for people who don't want a terminal; less control than oMLX over prefix caching.

### vllm-mlx (honorable mention)

The origin of oMLX; continuous batching with a memory-aware prefix cache (`--cache-memory-mb`, default 20% of RAM), but KV-cache quantization isn't exposed as a server flag and per-sequence KV has no upper bound by default, so oversized prompts can push the host into swap. Better for many concurrent agents than for one; benchmarks show it winning on aggregate throughput while Ollama-MLX wins on single-user latency.

## 3. System-level tuning checklist

- Raise `iogpu.wired_limit_mb` (see §1); keep 8–16 GB free.
- Avoid swap at all costs: MLX's memory limit is a guideline, not a hard ceiling — an allocation past it only fails when RAM *and swap* are exhausted, so on a large swap file the effective ceiling is RAM + swap and you silently thrash.
- `sudo pmset -a disablesleep 1` and disable Low Power Mode for long agent sessions; MacBooks thermal-throttle under sustained prefill, Mac Studio/Mini don't — MTPLX's installer includes fan control for this reason.
- Keep one model resident (keep-alive) and run exactly one sequence; parallelism destroys cache locality on a single-user box.
- macOS 15+ for current mlx-lm features (some features require macOS 15.0+).

## 4. Memory budgeting by tier (coding-agent use)

Rule of thumb: weights + KV cache + 8–16 GB OS headroom, where f16 KV for a 30B-class model at 64K context can exceed the weights — hence q8 KV. Community consensus: 32 GB is the entry point for useful work and runs the 30B-A3B MoE class at ~100 tok/s; 64 GB adds 70B dense models and context headroom; 96–128 GB is for bf16 70B or larger MoEs.

| Unified memory | Recommended combo for agents |
|---|---|
| 16–24 GB | Qwen3-Coder-30B-A3B is borderline; 14B dense at 4-bit, 16–32K ctx, q8 KV; Metal-llama.cpp or mlx-lm (Ollama-MLX needs 32 GB) |
| 32–36 GB | Qwen3.5/3.6-35B-A3B or Qwen3-Coder-30B-A3B 4-bit, 32–64K ctx, q8 KV; oMLX or Ollama-MLX |
| 48–64 GB | Same MoEs at 6/8-bit, 64–128K ctx; Qwen 3.8 27B 4-bit via MTPLX/oMLX; 70B dense at 4-bit for review-only tasks |
| 96–128 GB | gpt-oss-120b MXFP4, Qwen3.5-122B-A10B 4-bit, GLM-4.x-Air 6–8-bit; raise wired limit to ~90–120 GB |
| 192–512 GB | 235B+/Kimi-class MoEs at 4-bit; consider oMLX multi-Mac pipeline parallelism |

MoE models are the sweet spot on Macs: low active parameters keep decode fast on bandwidth, while unified memory holds the full expert set.

## 5. Tool calling, thinking, and harness interop on MLX stacks

- Everything hinges on the chat template. oMLX and mlx-lm apply the model's template; models whose template doesn't take `tools` won't tool-call at all. Prefer mlx-community conversions of Qwen3-Coder / Qwen3.5+ / GLM / gpt-oss, which have tested templates.
- Reasoning: oMLX auto-handles `<think>` tags for DeepSeek/MiniMax/Qwen reasoning models. Disable thinking for execution steps at the request level (`enable_thinking=false` via chat-template kwargs where supported) as in the earlier report.
- Harness wiring: Claude Code → oMLX or LM Studio's Anthropic endpoint (or Ollama). OpenCode/Codex/Pi/little-coder → any of the OpenAI endpoints (oMLX :8000, mlx-lm :8080, llama-server :8080, Ollama :11434/v1, MTPLX per its docs). Hugging Face model cards for MTPLX-branded models now carry a "use with Pi" snippet, which tells you the Pi/little-coder path is expected.
- Byte-stable prompts matter doubly here: oMLX's paged cache and llama.cpp's `--cache-reuse` both key on prefix identity, so the Claude Code attribution-header fix (`CLAUDE_CODE_ATTRIBUTION_HEADER=0` in settings.json) is mandatory on a Mac.

## 6. Comparison and recommendation

| | Ollama (MLX) | llama.cpp Metal | mlx-lm server | oMLX | MTPLX | LM Studio MLX |
|---|---|---|---|---|---|---|
| Prefill/TTFT | Good (M5 accel.) | Slowest on M4/M5 | Good | Best for agents (SSD prefix cache) | Good | Good |
| Decode | Very good | Good | Good | Very good (+MTP) | Best (native MTP) | Good |
| Persistent prefix cache | No | In-RAM only (`--cache-reuse`) | In-RAM LRU | RAM + SSD, survives restart | No | Session only |
| KV quant | Metal path yes; MLX unverified | Yes (needs FA) | Yes (single-request only) | Inherits mlx-lm | — | Yes (GUI) |
| Tool-call parsing | Yes | Yes (`--jinja`) | Template-dependent | Yes, multi-format | Template-dependent | Yes |
| Anthropic API | Yes | No | No | Yes | No | Yes (0.4.1+) |
| Multi-model | Yes | One per process | One per process | Yes | One | One loaded |
| Min RAM for MLX | 32 GB | n/a | any | any | any | any |
| Maturity | Preview backend | Very mature | Reference impl. | Young, very active | Young | Mature |

Recommendation for a coding-agent workflow on a Mac: **oMLX** as the primary server on ≥32 GB (persistent prefix cache is the single biggest win for agent loops), MLX-format 4–6-bit MoE models, MTP-enabled builds where available; **Ollama 0.19-MLX** when you want zero setup and Claude Code's Anthropic endpoint; **llama.cpp Metal** on 16 GB Macs or for GGUF-only models; **mlx-lm** when you want the reference behavior or need `--kv-bits`; **MTPLX** when decode speed on a 27B dense model is the priority. Regardless of stack: raise the wired limit, set a real 32–64K context, quantize KV, one sequence at a time, and keep the prompt prefix stable.

## Caveats

- Ollama's MLX numbers are its own; independent tests show smaller gains and the 32 GB floor may change. Whether KV-cache quantization applies on the MLX path is unverified.
- oMLX and MTPLX ship releases weekly; flags above come from README/discussion snapshots. Some secondary sources (SEO-style blogs) were used for tier guidance — verify against your own `llama-bench`/mlx-chronos runs.
- I could not confirm the exact mlx-lm server behavior for `--max-kv-size` in the current release, nor the M5 prefill deltas for specific coder models.

A broader investigation could add measured pp/tg numbers per chip and model, verify the Ollama-MLX KV-quant and oMLX flag details against current docs, and benchmark oMLX vs mlx-lm vs llama.cpp on the same agent trace.