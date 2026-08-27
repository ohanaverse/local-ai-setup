# LiteLLM Proxy on macOS — Complete Setup Documentation

**Status**: Thisdoc captures all setup steps referenced in `@docs/reference/` and what has actually been done on this machine.

---

## Current State (as of this doc)

- **LiteLLM version**: 1.98.0 (installed via `uv tool install 'litellm[proxy]'` on Aug 25, 2026)
- **Existing config**: `~/.config/litellm/config.yaml` with Ollama integration for `qwen3.8:27b-mlx`
- **Backends currently configured**: Ollama only
- **Backends referenced in docs but not yet installed**: llama.cpp, oMLX/MLX, OpenRouter

---

## Part 1 — Installing the LiteLLM proxy on macOS ✅ DONE

```bash
# Install via uv (recommended in 2026)
uv tool install 'litellm[proxy]'
litellm --version
# LiteLLM: Current Version = 1.98.0
```

```bash
# Init config directory and file
mkdir -p ~/.config/litellm
touch ~/.config/litellm/config.yaml
```

**Starting the server**

```bash
litellm --config ~/.config/litellm/config.yaml --port 4000
# INFO: Proxy running on http://0.0.0.0:4000
```

**Verification**: Access at `http://localhost:4000/v1/` with master key auth.

---

## Part 2 — Ollama Integration ✅ DONE

**Install and run Ollama on macOS:**

```bash
brew install ollama          # install Ollama
ollama serve                 # starts the API on http://localhost:11434
ollama pull llama3.1         # pull a model
ollama list                  # confirm exact model tags
```

**LiteLLM config for Ollama** (already in `~/.config/litellm/config.yaml`):

```yaml
model_list:
  - model_name: ollama/qwen3.8:27b-mlx
    litellm_params:
      model: ollama_chat/qwen3.8:27b-mlx        # ollama_chat/ hits /api/chat (recommended)
      api_base: http://localhost:11434
    model_info:
      supports_function_calling: true
```

**Testing through the proxy:**

```bash
curl http://localhost:4000/v1/chat/completions \
  -H "Authorization: Bearer sk-1234" \
  -H "Content-Type: application/json" \
  -d '{"model":"ollama/qwen3.8:27b-mlx","messages":[{"role":"user","content":"Say hi in 5 words"}]}'
```

---

## Part 3 — llama.cpp Integration (Reference / Not Yet Installed)

**Install llama.cpp on macOS (Apple Silicon):**

```bash
xcode-select --install       # Command Line Tools (clang), if not present
brew install llama.cpp       # installs llama-cli, llama-server, etc.
```

**Run llama-server:**

```bash
llama-server \
  -m ~/models/your-model.Q4_K_M.gguf \
  --host 127.0.0.1 \
  --port 8080 \
  --n-gpu-layers 999 \       # offload all layers to the Metal GPU
  --ctx-size 16384
```

**LiteLLM config — two options:**

*Option 1 — generic `openai/` prefix (dummy key required):*

```yaml
  - model_name: local-llamacpp
    litellm_params:
      model: openai/local-llamacpp      # openai/ prefix routes as OpenAI-compatible
      api_base: http://localhost:8080/v1  # MUST include /v1
      api_key: "dummy-key"              # required by the OpenAI client; llama-server ignores it
```

*Option 2 — `hosted_vllm/` prefix (no fake key needed, recommended):*

```yaml
  - model_name: local-llamacpp
    litellm_params:
      model: hosted_vllm/local-llamacpp
      api_base: http://localhost:8080/v1
      # api_key optional for hosted_vllm
```

---

## Part 4 — OpenRouter Integration (Reference / Not Yet Installed)

1. Create an account at [openrouter.ai](https://openrouter.ai), add credits, and generate a key at `openrouter.ai/keys` (keys look like `sk-or-v1-...`).
2. Export the key:

```bash
export OPENROUTER_API_KEY="sk-or-v1-..."
```

3. LiteLLM specifically reads `OPENROUTER_API_KEY` (not `OPENAI_API_KEY` — a common misconfiguration that produces `401 No auth credentials found`).

4. Config:

```yaml
  - model_name: claude-sonnet
    litellm_params:
      model: openrouter/anthropic/claude-sonnet-4
      api_key: os.environ/OPENROUTER_API_KEY
```

**Naming convention**: `openrouter/<author>/<slug>` — e.g. `openrouter/anthropic/claude-sonnet-4`, `openrouter/openai/gpt-4o-mini`.

---

## Part 5 — oMLX / MLX Integration (Apple Silicon MLX Backend) (Reference / Not Yet Installed)

**"omlx" refers to [oMLX](https://github.com/jundot/omlx), an Apache-2.0 MLX-native inference server for Apple Silicon (port 8000).**

**Install (Homebrew — recommended):**

```bash
brew tap jundot/omlx https://github.com/jundot/omlx
brew install omlx
```

**Start the server:**

```bash
omlx serve --model-dir ~/.omlx/models
# or: omlx start  # managed background service
```

**Get models into the model directory:**

*Via admin dashboard:* `http://localhost:8000/admin` (built-in HF search/download UI)
*Via CLI:*

```bash
hf download mlx-community/Qwen3.8-27B-4bit --local-dir ~/.omlx/models/Qwen3.8-27B-4bit
```

**LiteLLM config — treat MLX as generic OpenAI-compatible endpoint:**

```yaml
  # oMLX (default port 8000)
  - model_name: mlx-llama-3.2-3b
    litellm_params:
      model: openai/mlx-community/Llama-3.2-3B-Instruct-4bit
      api_base: http://localhost:8000/v1     # /v1 suffix is required
      api_key: "not-needed"                  # dummy key required for openai/ prefix

  # Key-free alternative using hosted_vllm prefix:
  - model_name: mlx-llama-3.2-3b
    litellm_params:
      model: hosted_vllm/mlx-community/Llama-3.2-3B-Instruct-4bit
      api_base: http://localhost:8000/v1
```

**Verifying through the proxy (port 4000):**

```bash
curl http://localhost:4000/v1/chat/completions \
  -H "Authorization: Bearer sk-1234" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "mlx-llama-3.2-3b",
    "messages": [{"role": "user", "content": "Say hello from MLX!"}]
  }'
```

---

## Part 6 — Consolidated Example Config

Here's a complete `config.yaml` that combines all four backends (Ollama, llama.cpp placeholder, OpenRouter, oMLX):

```yaml
model_list:
  # ---- Ollama (local) ----
  - model_name: ollama/qwen3.8:27b-mlx
    litellm_params:
      model: ollama_chat/qwen3.8:27b-mlx
      api_base: http://localhost:11434

  # ---- llama.cpp placeholder (edit port/model as needed) ----
  - model_name: local-llamacpp
    litellm_params:
      model: hosted_vllm/local-llamacpp     # or: openai/local-model + api_key
      api_base: http://localhost:8080/v1
      # api_key optional (hosted_vllm) or: "dummy-key" (openai/ prefix)

  # ---- OpenRouter (cloud) ----
  - model_name: claude-sonnet
    litellm_params:
      model: openrouter/anthropic/claude-sonnet-4
      api_key: os.environ/OPENROUTER_API_KEY

  # ---- oMLX / MLX (Apple Silicon fourth backend) ----
  - model_name: mlx-llama-3.2-3b
    litellm_params:
      model: openai/mlx-community/Llama-3.2-3B-Instruct-4bit
      api_base: http://localhost:8000/v1
      api_key: "not-needed"

general_settings:
  master_key: sk-your-master-key-here    # must start with sk-; use a long random value in prod

litellm_settings:
  drop_params: true                       # silently drop unsupported params instead of erroring
```

---

## Part 7 — Starting and Testing the Full Proxy

```bash
# 1. Ensure backends are running
ollama serve                          # already running
# llama-server: llama-server -m ... -p 8080 ...
# oMLX: omlx serve --model-dir ~/.omlx/models

# 2. Export OpenRouter key (if using)
export OPENROUTER_API_KEY="sk-or-v1-..."

# 3. Start the LiteLLM proxy
litellm --config ~/.config/litellm/config.yaml --port 4000
# INFO: Proxy running on http://0.0.0.0:4000
```

**Test each backend through the single endpoint:**

*Ollama:*

```bash
curl http://localhost:4000/v1/chat/completions \
  -H "Authorization: Bearer sk-1234" \
  -H "Content-Type: application/json" \
  -d '{"model":"ollama/qwen3.8:27b-mlx","messages":[{"role":"user","content":"Say hi in 5 words"}]}'
```

*llama.cpp (if configured):*

```bash
curl http://localhost:4000/v1/chat/completions \
  -H "Authorization: Bearer sk-1234" \
  -H "Content-Type: application/json" \
  -d '{"model":"local-llamacpp","messages":[{"role":"user","content":"What are you?"}]}'
```

*oMLX / MLX (if configured):*

```bash
curl http://localhost:4000/v1/chat/completions \
  -H "Authorization: Bearer sk-1234" \
  -H "Content-Type: application/json" \
  -d '{"model":"mlx-llama-3.2-3b","messages":[{"role":"user","content":"Say hello from MLX!"}]}'
```

*OpenRouter (if configured):*

```bash
curl http://localhost:4000/v1/chat/completions \
  -H "Authorization: Bearer sk-1234" \
  -H "Content-Type: application/json" \
  -d '{"model":"claude-sonnet","messages":[{"role":"user","content":"Hello!"}]}'
```

---

## Part 8 — Repurposing Tools to Use LiteLLM

Any tool that talks to Ollama's OpenAI-compatible port (`http://localhost:11434/v1`) can be retargeted to LiteLLM at `http://localhost:4000/v1` using your LiteLLM key. Similarly, Anthropic-format tools point at LiteLLM's Anthropic-compatible route.

**Universal pattern:** swap the base URL + adjust the API key.

| Tool | Retargeting Method |
|---|---|
| **OpenAI-format tools** (Open WebUI, Continue, Aider, VS Code Copilot BYOK, LangChain, LlamaIndex) | Set base URL to `http://localhost:4000/v1`, API key to your LiteLLM master/virtual key, model to a `model_name` in your config |
| **Anthropic-format tools** (Claude Code) | Set `ANTHROPIC_BASE_URL=http://localhost:4000` and `ANTHROPIC_AUTH_TOKEN=sk-1234` |
| **Continue.dev** | Edit `~/.continue/config.yaml`: `provider: openai`, `apiBase: http://localhost:4000/v1`, `apiKey: sk-1234` |
| **Aider** | `--openai-api-base http://localhost:4000/v1 --openai-api-key sk-1234 --model openai/llama3.1-local` |
| **LangChain** | `ChatOpenAI(openai_api_base="http://localhost:4000/v1", openai_api_key="sk-1234", model="llama3.1-local")` |
| **LlamaIndex** | `OpenAILike(api_base="http://localhost:4000/v1", api_key="sk-1234", model="llama3.1-local", is_chat_model=True)` |
| **Open WebUI** | Settings → Admin → Connections → OpenAI API: Base URL `http://localhost:4000/v1`, API Key = virtual key |
| **Claude Code (Anthropic route)** | `export ANTHROPIC_BASE_URL="http://localhost:4000"`, `export ANTHROPIC_AUTH_TOKEN="sk-1234"` |
| **Copililot CLI** | `export COPILOT_PROVIDER_BASE_URL=http://localhost:4000/v1`, `export COPILOT_PROVIDER_API_KEY=sk-1234` |
| **oAuth/tool-specific** | Most tools that accept a custom OpenAI base URL / API key work unmodified |

---

## Part 9 — Authentication, Caveats, and 2026 Best Practices

### Master key vs. virtual keys

- Set `general_settings.master_key` (or `LITELLM_MASTER_KEY` env var); it must start with `sk-` and is your admin credential.
- **Never distribute the master key** to applications — generate **virtual keys** via `POST /key/generate` instead.
- Virtual keys, spend tracking, and budgets require a **Postgres database** (`database_url` env var). Without one, `store_model_in_db` and per-key limits don't function.
- If both run in Docker, use service names (`http://litellm:4000`), not `localhost`, and set `LITELLM_SALT_KEY` to encrypt provider keys stored in the DB.

### Common pitfalls

- **The requested `model` must exactly match a `model_name` in your config.** Requesting a model not listed will fail.
- **OpenRouter needs `OPENROUTER_API_KEY`**, not `OPENAI_API_KEY`; using the wrong variable yields a 401.
- **llama.cpp `api_base` must include `/v1`** or you get a `Not Found Error`; but don't append full route suffixes like `/v1/chat/completions`.
- **Docker `localhost` trap on macOS**: use `host.docker.internal` to reach host-run Ollama/llama.cpp from inside a container.
- **Tool calling**: not all Ollama models support native function calling. Register `supports_function_calling: true` in `model_info`, and pass `--jinja` to llama-server for its chat template.
- **Security**: Never expose port 4000 to the public internet. Bind it locally or place it behind a reverse proxy with TLS.

### Version hygiene (2026)

- **Pin your LiteLLM version** rather than using `latest` in production. The March 24, 2026 supply-chain incident affected `litellm==1.82.7` and `litellm==1.82.8` (obfuscated payload in `proxy_server.py` and `.pth` file auto-execution). **Pin to v1.82.6 or earlier, or a confirmed post-incident stable release.**
- **Never expose port 4000 to the public internet**; bind locally or use a reverse proxy with TLS.
- **oMLX and security**: oMLX and mlx-omni-server accept an optional `--api-key`. If set, mirror that value in LiteLLM's `api_key` field instead of the dummy `"not-needed"`. Keep the proxy `master_key` mandatory for any multi-user deployment.

---

## Part 10 — Recommendations by Use Case

| Goal | Recommended Setup |
|---|---|
| **Minimal local-first, single user** | Install with `uv tool install 'litellm[proxy]'`, write the consolidated config with `master_key`, start ollama/llama.cpp/oMLX, validate with curl, add virtual keys later if sharing. |
| **Code assistant workflow** (Continue / Aider / VS Code Copilot BYOK) | Route all tools through LiteLLM at `http://localhost:4000/v1`. Use virtual keys. Keep a direct Ollama fallback for latency-critical autocomplete. |
| **Multiple users / shared proxy** | Stand up Postgres + `LITELLM_DATABASE_URL`, generate scoped virtual keys per user/tool, never share master key. |
| **Latency-critical editor autocomplete** | Connect directly to Ollama/llama.cpp/oMLX rather than through the proxy (proxy adds ~3.25 ms overhead). |
| **Production / multi-node** | Use vLLM for throughput, reverse proxy (nginx/Caddy) with TLS, secrets management, and dedicated hardware. The proxy is designed for local/dev use. |
| **Experimenting with MLX models on Apple Silicon** | Install oMLX (`brew install omlx`), add the single `model_list` entry, verify through port 4000, then scale up. |

---
*Document generated from reference docs in `@docs/reference/` on 2026-08-24. See individual docs for detailed background: LiteLLM Proxy, Ollama, llama.cpp, OpenRouter, oMLX/MLX.*