# LiteLLM Proxy on macOS: A Complete Guide to Unifying Ollama, llama.cpp, and OpenRouter

## TL;DR

- You can run one LiteLLM proxy on macOS (install with `uv tool install 'litellm[proxy]'` or `pip install 'litellm[proxy]'`, Python 3.10+) that exposes a single OpenAI-compatible endpoint (`http://localhost:4000`) fanning out to local Ollama (`ollama/` or `ollama_chat/` prefix, base `http://localhost:11434`), a local llama.cpp `llama-server` (via the generic `openai/` prefix with a dummy key, or `hosted_vllm/` to skip the key, base `http://localhost:8080/v1`), and cloud OpenRouter (`openrouter/` prefix, `OPENROUTER_API_KEY`).
- Every tool on Ollama’s integrations page is retargeted to LiteLLM by changing one of two things: for OpenAI-format tools (Open WebUI, Continue, Aider, VS Code Copilot BYOK, LangChain, LlamaIndex), point the base URL to `http://localhost:4000/v1` and use your LiteLLM master/virtual key; for Anthropic-format tools (Claude Code), point `ANTHROPIC_BASE_URL` to LiteLLM’s Anthropic-compatible route and set `ANTHROPIC_AUTH_TOKEN` to your LiteLLM key.
- The critical 2026 caveats: set a `master_key` (must start with `sk-`), pin your LiteLLM version to a known-safe release (a March 24, 2026 supply-chain compromise affected `litellm==1.82.7` and `litellm==1.82.8` — pin to v1.82.6 or earlier, or a current post-incident stable), don’t expose port 4000 publicly, and remember virtual keys and spend tracking require a Postgres database.

## Key Findings

1. **LiteLLM requires Python 3.10+** as of version 1.84.0 (released May 14, 2026). The official Proxy Quick Start warns that on Python 3.9 “pip silently resolves down to the last release that still allowed 3.9, which is 1.83.9, with no error.” `uv tool install` provisions a compatible Python automatically. 
1. **Ollama** should be integrated with the `ollama_chat/` prefix (LiteLLM officially recommends it over `ollama/` “for better responses” because it hits Ollama’s `/api/chat` route), with `api_base: http://localhost:11434`.
1. **llama.cpp has no dedicated LiteLLM provider prefix** (feature request GitHub issue #9138 remains open). You route to `llama-server` as a generic OpenAI-compatible endpoint using the `openai/` prefix (which forces a dummy API key) or the cleaner `hosted_vllm/` prefix (which makes the key optional). The `api_base` must include the `/v1` suffix.
1. **OpenRouter** uses the `openrouter/` prefix plus the upstream provider path (e.g. `openrouter/anthropic/claude-sonnet-4`), and LiteLLM specifically expects the `OPENROUTER_API_KEY` environment variable.
1. **Almost every tool** on Ollama’s integrations list is OpenAI- or Anthropic-compatible, so retargeting to LiteLLM is a base-URL + API-key change with no code rewrite.

## Details

### Part 1 — Installing the LiteLLM proxy on macOS

**Prerequisites**

- **Python 3.10 or higher.** LiteLLM 1.84.0+ declares `requires-python >=3.10`. Check with `python3 --version`.
- **Homebrew** is the easiest way to get modern Python and the other backends: install it from brew.sh, then `brew install python@3.12`.
- Optionally install **`uv`** (`brew install uv`) or **`pipx`** (`brew install pipx`) for isolated CLI installs.

**Method A — uv (recommended in 2026)**

```bash
uv tool install 'litellm[proxy]'
litellm --version
```

`uv tool install` provisions a compatible Python automatically, avoiding the Python 3.9 downgrade trap described in the LiteLLM docs.

**Method B — pip inside a virtual environment**

```bash
mkdir ~/litellm && cd ~/litellm
python3 -m venv venv
source venv/bin/activate
pip install 'litellm[proxy]'
```

The `[proxy]` extra installs the proxy-server dependencies  (the bare `litellm` package is only the Python SDK).

**Method C — pipx (isolated global CLI)**

```bash
pipx install 'litellm[proxy]'
```

**Method D — Docker**

```bash
docker run \
  -v $(pwd)/config.yaml:/app/config.yaml \
  -e OPENROUTER_API_KEY=sk-or-... \
  -e LITELLM_MASTER_KEY=sk-1234 \
  -p 4000:4000 \
  docker.litellm.ai/berriai/litellm:main-latest \
  --config /app/config.yaml
```

Note the macOS networking caveat: inside Docker, `localhost` refers to the container, so local Ollama/llama.cpp must be referenced as `http://host.docker.internal:11434` and `http://host.docker.internal:8080/v1` respectively. If you run the proxy natively (Methods A–C) you can use plain `localhost`. For production with virtual keys, use the `litellm-database` image plus Postgres.

**Starting the server**

```bash
litellm --config /path/to/config.yaml --port 4000
# INFO: Proxy running on http://0.0.0.0:4000
```

Add `--detailed_debug` to see exactly what LiteLLM sends upstream. 

### Part 2 — Ollama integration

**Install and run Ollama on macOS:**

```bash
brew install ollama          # or download the app from ollama.com
ollama serve                 # starts the API on http://localhost:11434
ollama pull llama3.1         # pull a model
ollama pull qwen3:8b
ollama list                  # confirm exact model tags
```

Ollama’s native API listens on port 11434 and also exposes an OpenAI-compatible surface at `http://localhost:11434/v1` (the key is required by SDKs but ignored by Ollama — any string works).

**LiteLLM config for Ollama:**

```yaml
model_list:
  - model_name: llama3.1                 # the alias clients will call
    litellm_params:
      model: ollama_chat/llama3.1        # ollama_chat/ hits /api/chat (recommended)
      api_base: http://localhost:11434
    model_info:
      supports_function_calling: true
```

Naming conventions:

- `ollama/<model>` → routes to Ollama’s `/api/generate`.
- `ollama_chat/<model>` → routes to `/api/chat`;  LiteLLM’s docs recommend this for better responses.
- The model name after the slash must exactly match a tag from `ollama list` (e.g. `ollama_chat/qwen3:8b`).
- Optional `keep_alive: "8m"` (or `-1` for forever) controls how long the model stays loaded. 

### Part 3 — llama.cpp integration

**Install llama.cpp on macOS (Apple Silicon):**

```bash
xcode-select --install       # Command Line Tools (clang), if not present
brew install llama.cpp       # installs llama-cli, llama-server, etc.
```

Homebrew’s build enables Metal GPU acceleration automatically on Apple Silicon,  so no extra flags are needed. To build from source instead:

```bash
git clone https://github.com/ggml-org/llama.cpp.git
cd llama.cpp
cmake -B build           # Metal is on by default on macOS
cmake --build build --config Release
```

**Run llama-server** with an OpenAI-compatible endpoint:

```bash
llama-server \
  -m ~/models/your-model.Q4_K_M.gguf \
  --host 127.0.0.1 \
  --port 8080 \
  --n-gpu-layers 999 \       # offload all layers to the Metal GPU
  --ctx-size 16384 \
  --jinja                    # enable the model's chat template (needed for tool calls)
```

`--n-gpu-layers 999` (aliased `-ngl`) pushes all layers onto the Apple Silicon GPU via Metal. The server exposes `http://localhost:8080/v1/chat/completions`, `/v1/completions`, `/v1/models`, and `/v1/embeddings`. By default llama-server binds to `127.0.0.1:8080`.  You can also download-and-run a Hugging Face model directly with `-hf <repo>`.

**LiteLLM config for llama.cpp.** There is **no dedicated `llama.cpp`/`llama_cpp` provider prefix** in LiteLLM (the feature request, GitHub issue #9138, remains open). Use one of two approaches:

*Option 1 — generic `openai/` prefix (dummy key required):*

```yaml
model_list:
  - model_name: local-llamacpp
    litellm_params:
      model: openai/local-llamacpp      # openai/ prefix routes as OpenAI-compatible
      api_base: http://localhost:8080/v1  # MUST include /v1
      api_key: "dummy-key"              # required by the OpenAI client; llama-server ignores it
```

Because `openai/` routes through the official OpenAI Python client, LiteLLM’s docs state the client “requires an API key for all requests” — so a placeholder is mandatory. If you see a `Not Found Error`, confirm the `api_base` ends in `/v1`; but do **not** append full route paths like `/v1/chat/completions` (the OpenAI client appends those automatically).

*Option 2 — `hosted_vllm/` prefix (no fake key needed, recommended):*

```yaml
model_list:
  - model_name: local-llamacpp
    litellm_params:
      model: hosted_vllm/local-llamacpp
      api_base: http://localhost:8080/v1
      # api_key optional for hosted_vllm
```

LiteLLM’s own OpenAI-compatible docs suggest using `hosted_vllm` or `llamafile` “if you don’t want to provide a fake API key.” Since `llama-server` speaks the same OpenAI dialect as vLLM, `hosted_vllm/` works and keeps the config clean.

### Part 4 — OpenRouter integration

1. Create an account at openrouter.ai, add credits, and generate a key at openrouter.ai/keys (keys look like `sk-or-v1-...`). OpenRouter uses a prepaid credit balance. 
1. Export the key using the exact variable LiteLLM expects:

```bash
export OPENROUTER_API_KEY="sk-or-v1-..."
```

LiteLLM specifically reads `OPENROUTER_API_KEY` (not `OPENAI_API_KEY`) for this provider — a common misconfiguration that produces `401 No auth credentials found`.

1. Config:

```yaml
model_list:
  - model_name: claude-sonnet
    litellm_params:
      model: openrouter/anthropic/claude-sonnet-4
      api_key: os.environ/OPENROUTER_API_KEY
```

Naming convention: `openrouter/<author>/<slug>` — the OpenRouter model identifier in author/slug form,  e.g. `openrouter/openai/gpt-4o-mini`, `openrouter/meta-llama/llama-3-70b`. LiteLLM defaults the base URL to `https://openrouter.ai/api/v1`;  you can override it with `api_base` or `OPENROUTER_API_BASE`.

### Part 5 — Consolidated example config.yaml

```yaml
model_list:
  # ---- Ollama (local) ----
  - model_name: llama3.1-local
    litellm_params:
      model: ollama_chat/llama3.1
      api_base: http://localhost:11434
    model_info:
      supports_function_calling: true

  - model_name: qwen-local
    litellm_params:
      model: ollama_chat/qwen3:8b
      api_base: http://localhost:11434

  # ---- llama.cpp (local llama-server on :8080) ----
  - model_name: llamacpp-local
    litellm_params:
      model: hosted_vllm/llamacpp-local   # or: openai/llamacpp-local + api_key
      api_base: http://localhost:8080/v1

  # ---- OpenRouter (cloud) ----
  - model_name: claude-sonnet
    litellm_params:
      model: openrouter/anthropic/claude-sonnet-4
      api_key: os.environ/OPENROUTER_API_KEY

  - model_name: gpt-4o-mini
    litellm_params:
      model: openrouter/openai/gpt-4o-mini
      api_key: os.environ/OPENROUTER_API_KEY

general_settings:
  master_key: sk-1234          # 👈 must start with sk-; use a long random value in prod

litellm_settings:
  drop_params: true            # silently drop unsupported params instead of erroring
```

Start it:

```bash
export OPENROUTER_API_KEY="sk-or-v1-..."
litellm --config config.yaml --port 4000
```

### Part 6 — Starting and testing

Confirm each backend is reachable through the single endpoint. All requests carry the master key as a Bearer token.

**Ollama via curl:**

```bash
curl http://localhost:4000/v1/chat/completions \
  -H "Authorization: Bearer sk-1234" \
  -H "Content-Type: application/json" \
  -d '{"model":"llama3.1-local","messages":[{"role":"user","content":"Say hi in 5 words"}]}'
```

**llama.cpp via curl:**

```bash
curl http://localhost:4000/v1/chat/completions \
  -H "Authorization: Bearer sk-1234" \
  -H "Content-Type: application/json" \
  -d '{"model":"llamacpp-local","messages":[{"role":"user","content":"What are you?"}]}'
```

**OpenRouter via curl:**

```bash
curl http://localhost:4000/v1/chat/completions \
  -H "Authorization: Bearer sk-1234" \
  -H "Content-Type: application/json" \
  -d '{"model":"claude-sonnet","messages":[{"role":"user","content":"Hello!"}]}'
```

**OpenAI Python SDK (works for all three — just change `model`):**

```python
from openai import OpenAI
client = OpenAI(api_key="sk-1234", base_url="http://localhost:4000/v1")
for m in ["llama3.1-local", "llamacpp-local", "claude-sonnet"]:
    r = client.chat.completions.create(
        model=m, messages=[{"role": "user", "content": "One-sentence hello"}])
    print(m, "→", r.choices[0].message.content)
```

A quick smoke test of the whole config is `litellm --test`.

### Part 7 — Repointing Ollama’s integration tools at LiteLLM

Ollama’s integrations page (docs.ollama.com/integrations) lists terminal coding agents (Claude Code, OpenCode, DeepSeek Harness), assistants (OpenClaw, Hermes Agent), and editor integrations (VS Code). It surfaces the newest items via `ollama launch`. Below is how to retarget these plus the broader ecosystem the user named (Continue, Open WebUI, Aider, LangChain, LlamaIndex). The universal rule: **anything that talks to Ollama’s OpenAI-compatible port (`http://localhost:11434/v1`) can instead talk to LiteLLM at `http://localhost:4000/v1` using your LiteLLM key**; anything that talks to Ollama’s Anthropic-compatible surface repoints via `ANTHROPIC_BASE_URL`.

**Claude Code (terminal + VS Code) — Anthropic format.** Ollama connects Claude Code through its Anthropic-compatible API by setting `ANTHROPIC_BASE_URL=http://localhost:11434` and `ANTHROPIC_AUTH_TOKEN=ollama`. To route through LiteLLM instead, point at LiteLLM’s Anthropic-compatible route and use your LiteLLM key:

```bash
export ANTHROPIC_BASE_URL="http://localhost:4000"
export ANTHROPIC_AUTH_TOKEN="sk-1234"
export ANTHROPIC_MODEL="claude-sonnet"     # a model_name from your LiteLLM config
```

In the VS Code extension, set the same values under the `claudeCode.environmentVariables` setting in `settings.json`. LiteLLM exposes its own Anthropic-compatible route, so requests pass through without lossy conversion. 

**OpenCode / DeepSeek Harness / other OpenAI-compatible terminal agents.** These accept a custom OpenAI base URL and key — set the provider’s base URL to `http://localhost:4000/v1`, the API key to your LiteLLM key, and the model to a LiteLLM `model_name`.

**VS Code (GitHub Copilot Chat BYOK).** VS Code’s “Bring Your Own Key” supports an **OpenAI Compatible** provider: run `Chat: Manage Language Models`, pick *OpenAI Compatible*, set the base URL to `http://localhost:4000/v1` and paste your LiteLLM key.  (Note: VS Code’s built-in Ollama provider is deprecated in favor of the official Ollama extension;  for LiteLLM use the OpenAI-Compatible provider, not the Ollama one.) For **Copilot CLI**:

```bash
export COPILOT_PROVIDER_BASE_URL=http://localhost:4000/v1
export COPILOT_PROVIDER_API_KEY=sk-1234
export COPILOT_MODEL=llama3.1-local
```

**Continue (continue.dev).** Edit `~/.continue/config.yaml`, use the `openai` provider with `apiBase` pointed at LiteLLM:

```yaml
models:
  - name: Llama via LiteLLM
    provider: openai
    model: llama3.1-local        # LiteLLM model_name
    apiBase: http://localhost:4000/v1
    apiKey: sk-1234
    roles: [chat, edit]
```

Continue explicitly lists BerriAI/litellm among supported OpenAI-compatible providers.  (Known quirk: some Continue versions have had trouble with the `openai` provider for *tab autocomplete* specifically — see GitHub issue #4542  — so test autocomplete separately.)

**Open WebUI.** In *Settings → Admin → Connections → OpenAI API*, click add, set **API Base URL** to `http://localhost:4000/v1`  (or `http://litellm:4000/v1` if both run in Docker on the same network — not `localhost`), and **API Key** to a LiteLLM virtual key. Verify the connection; LiteLLM-configured models then appear in the model picker.  Best practice is to generate a dedicated virtual key so you can scope which models Open WebUI sees. 

**Aider.**

```bash
aider --openai-api-base http://localhost:4000/v1 \
      --openai-api-key sk-1234 \
      --model openai/llama3.1-local
```

Or via environment variables `OPENAI_API_BASE` and `OPENAI_API_KEY`. Prefix the model with `openai/` so Aider’s own LiteLLM layer treats the proxy as an OpenAI-compatible endpoint.

**LangChain (Python).**

```python
from langchain_openai import ChatOpenAI
chat = ChatOpenAI(
    openai_api_base="http://localhost:4000/v1",
    openai_api_key="sk-1234",
    model="llama3.1-local",
)
```

**LlamaIndex (Python).** Use the `OpenAILike` LLM (it doesn’t validate model names against OpenAI’s catalog):

```python
from llama_index.llms.openai_like import OpenAILike
llm = OpenAILike(
    api_base="http://localhost:4000/v1",
    api_key="sk-1234",
    model="claude-sonnet",
    is_chat_model=True,
)
```

LiteLLM documents that any framework accepting a custom `openai_api_base`/`base_url` works unmodified  — this covers LangChain, LlamaIndex, the OpenAI JS SDK, Instructor, AutoGen, and similar.

### Part 8 — Authentication, caveats, and 2026 best practices

**Master key vs. virtual keys.** Set `general_settings.master_key` (or the `LITELLM_MASTER_KEY` env var); it must start with `sk-` and is your admin credential and Admin UI login. Never hand the master key to applications — it can create other keys. Instead generate **virtual keys** via `POST /key/generate`. Critically, **virtual keys, spend tracking, and budgets require a Postgres database** (`database_url`); without one, `store_model_in_db` and per-key limits don’t function, and `litellm_settings.max_budget` will not actually cap spend.

- Master-key precedence: if set in both places, `general_settings.master_key` overrides the env var.
- If both run in Docker, use service names (`http://litellm:4000`), not `localhost`, and set `LITELLM_SALT_KEY` to encrypt provider keys stored in the DB.

**Common pitfalls.**

- **The requested `model` must exactly match a `model_name` in your config.** Requesting `openai/gpt-4o` when the config defines `gpt-4o` will not match.
- **OpenRouter needs `OPENROUTER_API_KEY`**, not `OPENAI_API_KEY`; using the wrong variable yields a 401.
- **llama.cpp `api_base` must include `/v1`** or you get a `Not Found Error`; but don’t append full route suffixes.
- **Docker `localhost` trap** on macOS: use `host.docker.internal` to reach host-run Ollama/llama.cpp from inside a container.
- **Tool calling:** not all Ollama models support native function calling; register `supports_function_calling: true` in `model_info`, and pass `--jinja` to llama-server for its chat template. Gemma models don’t support tool calls and will hard-fail with agents like Claude Code.

**Performance.** LiteLLM’s official Proxy Performance docs state the proxy “adds 0.00325 seconds latency as compared to using the Raw OpenAI API” (~3.25 ms); under load, the v1.71.1-stable release notes cite roughly 40 ms median latency overhead at 200 RPS per instance. For latency-critical local paths (e.g. editor autocomplete), connect directly to Ollama/llama.cpp and route only everything else through the proxy.

**Security & version hygiene (2026).**

- **Pin your LiteLLM version** rather than using `latest` in production; check the changelog for breaking changes between minors.
- **The March 24, 2026 supply-chain incident.** Per LiteLLM’s official “Security Update: Suspected Supply Chain Incident” post, the compromised packages `litellm==1.82.7` and `litellm==1.82.8` were live on PyPI “from 10:39 UTC for about 40 minutes before being quarantined by PyPI,” and LiteLLM advises pinning “to a known safe version such as v1.82.6 or earlier.” Independent analyses describe the mechanics: Zscaler ThreatLabz reports version 1.82.7 “embedded an obfuscated Base64-encoded payload in proxy_server.py that executes on import,” while 1.82.8 “added a .pth file (litellm_init.pth) that Python automatically executes at startup — even without importing LiteLLM.” The attack was attributed to a threat actor tracked as “TeamPCP” by Datadog Security Labs and Bitsight; CloudSEK called it “the largest AI supply chain breach of 2026.” **Verify you are outside the 1.82.7/1.82.8 window and rotate any provider keys stored in `.env` if you ran either.**
- **Never expose port 4000 to the public internet;** bind it locally or place it behind a reverse proxy with TLS. Set `WEBUI_AUTH=true` on Open WebUI if paired. 

## Recommendations

1. **Start minimal, native, and locally.** Install with `uv tool install 'litellm[proxy]'`, write the consolidated config from Part 5 with a `master_key`, run `ollama serve` and `llama-server` locally, and validate all three backends with the Part 6 curl commands before wiring up any tools. Benchmark to change: if a backend curl fails, add `--detailed_debug` and confirm base URLs (`:11434`, `:8080/v1`) and model-name matches.
1. **Prefer `ollama_chat/` for Ollama and `hosted_vllm/` for llama.cpp.** These give better chat behavior and avoid the dummy-key requirement respectively.
1. **Add a database and virtual keys before sharing the proxy.** The moment more than one person/tool uses it, stand up Postgres (use the `litellm-database` Docker image), set `LITELLM_SALT_KEY`, and issue scoped virtual keys per tool — never distribute the master key. Threshold: any multi-user or internet-adjacent deployment.
1. **Retarget tools in this order of ease:** OpenAI-format tools first (Open WebUI, Continue, Aider, LangChain/LlamaIndex, Copilot BYOK) by swapping base URL + key; then Anthropic-format tools (Claude Code) via `ANTHROPIC_BASE_URL`. Keep a direct-to-Ollama fallback for latency-critical autocomplete.
1. **Pin the version and monitor.** Choose a specific stable tag confirmed to be outside the 1.82.7/1.82.8 window (v1.82.6 or a vetted later stable), enable logging/alerting, and re-run the smoke tests on each upgrade.

## Caveats

- Ollama’s integrations page is dynamic (it says to run `ollama launch` for the latest list) and at the time of research prominently featured Claude Code, OpenCode, DeepSeek Harness, OpenClaw, Hermes Agent, and VS Code — the specific roster may change. The retargeting principles (OpenAI base-URL swap vs. Anthropic base-URL swap) remain valid regardless of the exact roster.
- Several configuration specifics (exact port numbers, model tags, key formats) depend on your versions; treat provider dashboards and version-specific behavior as “verify against the current console.”
- llama.cpp integration relies on the generic OpenAI-compatibility path because LiteLLM has no first-class llama.cpp provider; if that feature request lands, a dedicated prefix may become the preferred route.
- Some cited step-by-step values (e.g. specific model names like `qwen3.5:27b`) and third-party threat-intelligence figures come from blogs and vendor advisories rather than official docs; the load-bearing configuration facts (prefixes, base URLs, key handling) and the incident summary are from official LiteLLM, Ollama, llama.cpp, and OpenRouter documentation plus LiteLLM’s own security post.
- This guide assumes single-machine, local-first use on macOS (Apple Silicon emphasized for Metal). Production, multi-node, or high-throughput serving has additional considerations (vLLM for throughput, reverse proxy, secrets management) beyond this scope.