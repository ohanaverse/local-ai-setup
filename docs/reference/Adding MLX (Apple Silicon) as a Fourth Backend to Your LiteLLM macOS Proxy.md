# Adding MLX (Apple Silicon) as a Fourth Backend to Your LiteLLM macOS Proxy

## TL;DR
- **"omlx" is a real, specific project — oMLX (github.com/jundot/omlx, omlx.ai)**, an Apache-2.0, MLX-native inference server for Apple Silicon that exposes an OpenAI- and Anthropic-compatible API on port 8000 and is managed from a macOS menu bar (it has ~18.6k GitHub stars and was first open-sourced Feb 13, 2026). It is the correct tool your abbreviation refers to, though `mlx-omni-server` and mlx-lm's built-in `mlx_lm.server` are valid alternatives.
- **LiteLLM has no native "mlx" provider.** You wire any MLX server in as a generic OpenAI-compatible endpoint using the `openai/` prefix plus an `api_base` ending in `/v1` and a dummy `api_key` — exactly like you did for llama.cpp.
- **Recommended path:** install oMLX via Homebrew (`brew tap jundot/omlx https://github.com/jundot/omlx && brew install omlx`), run `omlx serve`, drop MLX-format models from the `mlx-community` HF org into your model dir, and add one `model_list` entry pointing LiteLLM at `http://localhost:8000/v1`.

## Key Findings
- MLX is Apple's array/ML framework for Apple Silicon; local LLM inference is built on the official `mlx-lm` package (ml-explore), which ships a built-in OpenAI-compatible server (`mlx_lm.server`, default port 8080). Its own docs state plainly: *"The MLX LM server is not recommended for production as it only implements basic security checks."*
- Three OpenAI-compatible MLX servers matter in 2026: (1) **oMLX** (`jundot/omlx`, port 8000) — the most feature-complete, with continuous batching, a two-tier RAM+SSD KV cache, multi-model serving, a menu-bar app and admin dashboard (it "started from vllm-mlx v0.1.0"); (2) **mlx-omni-server** (`madroidmaq/mlx-omni-server`, port 10240) — dual OpenAI + Anthropic API compatibility covering chat, embeddings, image gen, TTS/STT via `pip install`; (3) **`mlx_lm.server`** (port 8080) — the minimal official reference server.
- All three take **Hugging Face MLX repo IDs** (e.g. `mlx-community/Llama-3.2-3B-Instruct-4bit`) as model names and read the shared `~/.cache/huggingface/hub` cache.
- LiteLLM's providers list contains no MLX entry, so MLX = generic OpenAI-compatible endpoint. Per docs.litellm.ai/docs/providers/openai_compatible, the `api_base` must end in `/v1` (*"If you see `Not Found Error`... make sure your `api_base` has the `/v1` postfix"*) and the `openai/` prefix *"requires an API key for all requests"* (use `"not-needed"`); the `hosted_vllm/` prefix avoids the dummy key.
- MLX generally beats llama.cpp on Apple Silicon for models under ~14B — Groundy's benchmark roundup (citing arXiv 2601.19139 and 2511.05502) reports *"MLX delivers 20-87% higher generation throughput than llama.cpp"* below 14B, with the gap collapsing at 27B+ *"where the bottleneck is the chip's memory bandwidth ceiling, roughly 400 GB/s on M2 Ultra, 273 GB/s on standard M2 chips."*

## Details

### 1. What "omlx" is, and the MLX landscape in 2026

Your abbreviation "omlx" maps directly onto a shipping project: **oMLX** by developer jundot (GitHub `jundot/omlx`, site omlx.ai). It is an Apache-2.0-licensed, macOS-native LLM inference server built on Apple's MLX framework, with ~18.6k GitHub stars and a first open-source release on Feb 13, 2026. It exposes both OpenAI-compatible (`/v1/chat/completions`, `/v1/completions`, `/v1/embeddings`, `/v1/rerank`, `/v1/models`) and Anthropic-compatible (`/v1/messages`) endpoints, and is managed from a native Swift/SwiftUI menu-bar app plus a web admin dashboard at `/admin`. It started as a fork of `vllm-mlx` (v0.1.0) and adds continuous batching (via mlx-lm's `BatchGenerator`), a tiered hot (RAM) + cold (SSD) KV cache that persists across restarts, and multi-model serving (LLM, VLM, OCR, embedding, reranker). Default port is **8000**.

The full MLX serving landscape you should know:

- **`mlx-lm`** (official, `ml-explore/mlx-lm`): the core Python package for running/fine-tuning LLMs with MLX. It includes a built-in HTTP server, `mlx_lm.server`, whose API mimics the OpenAI chat API. Default port **8080**. Officially flagged as not for production.
- **`mlx-omni-server`** (`madroidmaq/mlx-omni-server`, v0.5.3 on PyPI): community server providing a broad OpenAI + Anthropic API surface — chat, embeddings, image generation, TTS, STT. Installed via `pip install mlx-omni-server`, requires Python 3.11+, default port **10240**.
- **oMLX** (`jundot/omlx`): the most polished/complete option for a persistent local server; recommended if you want a "set it and forget it" menu-bar-managed backend.

There is **no dedicated LiteLLM "mlx" provider** — confirmed by reviewing LiteLLM's full provider list at docs.litellm.ai/docs/providers, which lists Ollama, LM Studio, Llamafile, vLLM, etc., but no MLX entry. All MLX servers are therefore consumed by LiteLLM as generic OpenAI-compatible endpoints.

### 2. Installation on macOS (Apple Silicon required)

MLX requires an Apple Silicon Mac (M1/M2/M3/M4). Pick ONE server.

**Option A — oMLX (recommended; the "omlx" you asked about)**

Requirements: macOS 15.0+ (Sequoia), Python 3.11–3.13, Apple Silicon.

```bash
# Homebrew (installs the CLI + can run as a background service)
brew tap jundot/omlx https://github.com/jundot/omlx
brew install omlx

# Start a managed background server (zero-config defaults: ~/.omlx/models, port 8000)
omlx start
# ...or run in the foreground pointed at your own model dir
omlx serve --model-dir ~/models
```

Alternatively download the `.dmg` from Releases and drag to Applications for the menu-bar app (note: the DMG app installs a lightweight `~/.omlx/bin/omlx` CLI shim, but for full terminal use Homebrew or source is cleaner). From source: `git clone https://github.com/jundot/omlx.git && cd omlx && pip install -e .`. The server discovers models from subdirectories of the model dir automatically; any OpenAI-compatible client connects at `http://localhost:8000/v1`, and there is a built-in chat UI at `http://localhost:8000/admin/chat`.

**Option B — mlx-omni-server (complete OpenAI/Anthropic suite via pip)**

Requirements: Python 3.11+, Apple Silicon, MLX installed.

```bash
pip install mlx-omni-server      # or: uv pip install mlx-omni-server
mlx-omni-server                  # starts on default port 10240
# custom port:
mlx-omni-server --port 8000
```

**Option C — mlx-lm built-in server (minimal, official)**

```bash
pip install mlx-lm               # pipx/uv also work: uv pip install mlx-lm
# Start the OpenAI-compatible server (default port 8080)
mlx_lm.server --model mlx-community/Mistral-7B-Instruct-v0.3-4bit
```

The model is downloaded from the given Hugging Face repo on first use and cached locally. `mlx_lm.server` defaults `max_tokens` to 512, so clients should set it explicitly.

**Model format:** all three want **MLX-format (typically quantized) models**, most easily pulled from the **`mlx-community` org on Hugging Face** (e.g. `mlx-community/Llama-3.2-3B-Instruct-4bit`, `mlx-community/gemma-3-1b-it-4bit-DWQ`). You can convert/quantize your own with `mlx_lm.convert --model <hf-repo> -q`. All three servers share the standard `~/.cache/huggingface/hub` cache, so a model pulled once is reusable across them.

### 3. LiteLLM config.yaml — adding MLX as the fourth backend

Because there is no `mlx` provider, treat the MLX server as an OpenAI-compatible endpoint. Two rules, straight from LiteLLM's OpenAI-compatible docs:

1. The `api_base` **must end in `/v1`** — *"If you see `Not Found Error` when testing make sure your `api_base` has the `/v1` postfix."*
2. With the `openai/` prefix, a non-empty `api_key` is **required** (the docs note this provider *"requires an API key for all requests"*); use a dummy such as `"not-needed"`. If you prefer no key, use the `hosted_vllm/` prefix instead.

Add whichever of these matches the server you installed:

```yaml
  # ---- MLX via oMLX (default port 8000) ----
  - model_name: mlx-llama-3.2-3b
    litellm_params:
      model: openai/mlx-community/Llama-3.2-3B-Instruct-4bit
      api_base: http://localhost:8000/v1     # /v1 suffix is required
      api_key: "not-needed"                  # dummy key required for openai/ prefix

  # ---- MLX via mlx-omni-server (default port 10240) ----
  - model_name: mlx-omni-llama-3.2-3b
    litellm_params:
      model: openai/mlx-community/Llama-3.2-3B-Instruct-4bit
      api_base: http://localhost:10240/v1
      api_key: "not-needed"

  # ---- MLX via mlx_lm.server (default port 8080) ----
  - model_name: mlx-lm-llama-3.2-3b
    litellm_params:
      model: openai/mlx-community/Llama-3.2-3B-Instruct-4bit
      api_base: http://localhost:8080/v1
      api_key: "not-needed"
```

Key-free alternative using the vLLM prefix (no dummy key needed):

```yaml
  - model_name: mlx-llama-3.2-3b
    litellm_params:
      model: hosted_vllm/mlx-community/Llama-3.2-3B-Instruct-4bit
      api_base: http://localhost:8000/v1
```

**Consolidated example config** merging MLX with your existing three backends:

```yaml
model_list:
  # ---- Ollama ----
  - model_name: llama3
    litellm_params:
      model: ollama/llama3.2:3b
      api_base: http://localhost:11434

  # ---- llama.cpp (llama-server, OpenAI-compatible) ----
  - model_name: llamacpp-model
    litellm_params:
      model: openai/local-model
      api_base: http://localhost:8081/v1
      api_key: "not-needed"

  # ---- OpenRouter ----
  - model_name: openrouter-claude
    litellm_params:
      model: openrouter/anthropic/claude-3.5-sonnet
      api_key: os.environ/OPENROUTER_API_KEY

  # ---- MLX (oMLX; new fourth backend) ----
  - model_name: mlx-llama-3.2-3b
    litellm_params:
      model: openai/mlx-community/Llama-3.2-3B-Instruct-4bit
      api_base: http://localhost:8000/v1
      api_key: "not-needed"

general_settings:
  master_key: sk-your-master-key-here
```

Note the port for llama.cpp above is illustrative — keep whatever `llama-server` port your existing guide uses; just make sure oMLX's 8000, mlx-omni-server's 10240, or mlx_lm.server's 8080 don't collide with it. (oMLX and vllm-mlx both default to 8000, so don't run both unchanged.)

### 4. Verifying the MLX backend through the unified proxy (port 4000)

Start the proxy (`litellm --config /path/to/config.yaml`) and the MLX server, then:

```bash
# curl through the LiteLLM proxy
curl http://localhost:4000/v1/chat/completions \
  -H "Authorization: Bearer sk-your-master-key-here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "mlx-llama-3.2-3b",
    "messages": [{"role": "user", "content": "Say hello from MLX!"}]
  }'
```

```python
# Python (OpenAI SDK) through the LiteLLM proxy
import openai
client = openai.OpenAI(api_key="sk-your-master-key-here", base_url="http://localhost:4000")
resp = client.chat.completions.create(
    model="mlx-llama-3.2-3b",
    messages=[{"role": "user", "content": "Say hello from MLX!"}],
)
print(resp.choices[0].message.content)
```

You can also hit the MLX server directly to isolate problems, e.g. `curl http://localhost:8000/v1/models` (oMLX) or `curl localhost:8080/v1/models` (mlx_lm.server).

### 5. Caveats, performance and best practices (2026)

- **Apple Silicon only.** MLX uses Metal + unified memory; there is no x86/Intel Mac or Linux path.
- **Performance vs llama.cpp.** Community benchmarks (Groundy, citing arXiv 2601.19139 Jan 2026 and 2511.05502 Nov 2025) show *"MLX delivers 20-87% higher generation throughput than llama.cpp"* for models under 14B, thanks to native unified-memory design; the gap collapses at 27B+ where memory bandwidth (~400 GB/s on M2 Ultra, ~273 GB/s on standard M2) dominates. Ollama switched its Apple Silicon backend from the llama.cpp Metal backend to MLX in version 0.19, shipped March 30, 2026, with decode speed on the same machine going *"from 58 tokens per second to 112"* (≈93% faster; ~130 tok/s vs 43 on a Mac mini M4 Pro running Qwen3-Coder-30B-A3B).
- **But watch long-context prefill.** MLX prefill can be slower at very long contexts. One independent M1 Max test (Groundy) on Qwen3.5-35B at 8.5K context found effective wall-clock throughput *"collapsed to ~3 tok/s, while the UI reported 51 tok/s,"* and the GGUF (llama.cpp) runtime finished the same response faster (43.4s vs 52.3s). Treat single-number "tokens/sec" claims with caution.
- **Quantization.** Prefer 4-bit or 8-bit MLX-format models from `mlx-community`; 4-bit is the sweet spot for memory. DWQ/mixed-precision variants exist. Rough RAM guidance: 7B-Q4 ≈ 16GB, 14B-Q4 ≈ 24GB, 70B-Q4 ≈ 48GB+ unified memory.
- **oMLX vs mlx-omni-server vs mlx_lm.server.** Choose oMLX for a persistent, multi-model, menu-bar-managed server with SSD KV caching (best for coding agents/long sessions); mlx-omni-server if you want embeddings + image + TTS/STT from one pip-installed process; mlx_lm.server for the simplest official option or scripting. None is hardened for public exposure — bind to localhost. `mlx_lm.server` in particular is officially *"not recommended for production as it only implements basic security checks."*
- **oMLX custom kernels.** For GLM-5.2 / MiniMax M3 / Qwen3.5 families, a plain `pip install -e .` silently falls back to slow generic paths; the native Metal kernels (the project measured ~30x faster fused DSA prefill for GLM-5.2 — 845 vs ~29 tok/s on an M3 Ultra) require full Xcode or the official DMG build.
- **Security.** oMLX and mlx-omni-server accept an optional API key (`--api-key` for oMLX). If you set one on the MLX server, put that value in LiteLLM's `api_key` field instead of the dummy. Keep the proxy `master_key` set.

## Recommendations
1. **Install oMLX first (Homebrew), because it is exactly the "omlx" you referenced and is the most capable persistent backend.** Run `omlx serve --model-dir ~/models`, confirm `curl http://localhost:8000/v1/models` responds, then add the single oMLX `model_list` entry above to your existing config and reload the proxy.
2. **Verify end-to-end through port 4000** with the curl/Python snippets in §4 before wiring any client apps to the new `mlx-*` model name.
3. **If you only need quick, script-driven single-model serving**, skip oMLX and use `pip install mlx-lm` + `mlx_lm.server`; if you need embeddings/TTS/STT/image alongside chat from one process, use `mlx-omni-server` (port 10240) instead.
4. **Prefer 4-bit `mlx-community` models sized to your RAM** (see the RAM guidance above). Start with `mlx-community/Llama-3.2-3B-Instruct-4bit` to validate the pipeline, then scale up.
5. **Decision thresholds that would change the above:**
   - If you run models **≥27B or very long contexts (>40K)** and see slow prefill, keep those specific models on your existing llama.cpp backend and reserve MLX for <14B models — that is where MLX's speed advantage is real.
   - If you plan to **expose the proxy beyond localhost**, set a real `--api-key` on the MLX server and mirror it in LiteLLM, and keep the proxy `master_key` mandatory.
   - If you serve **GLM-5.2 / MiniMax M3 / Qwen3.5** on oMLX, install the native custom kernels (full Xcode or official DMG); otherwise expect large silent slowdowns.

## Caveats
- Benchmark numbers here come from community/independent write-ups (Groundy, Towards AI) and vendor/self-reported figures (Ollama's own 0.19 numbers, oMLX's own kernel measurements), not a single controlled study; they vary heavily by chip, model, quantization, context length, and batch size. Use them directionally, not as guarantees.
- `mlx_lm` and `mlx-omni-server` version numbers move fast (mlx-lm was at 0.31.x and mlx-omni-server at 0.5.3 at the time of writing); pin versions in your environment for reproducibility.
- The LiteLLM `/v1`-suffix and dummy-`api_key` requirements were verified against the current docs.litellm.ai OpenAI-compatible provider page; if a future LiteLLM release adds a first-class MLX provider, prefer it over the generic `openai/` shim.
- oMLX requires macOS 15.0+ (Sequoia); mlx-omni-server and mlx-lm are less strict but still require Apple Silicon and Python 3.11+. Confirm your macOS/Python versions before installing.