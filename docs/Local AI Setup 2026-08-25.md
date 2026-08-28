# Steps
## Part 1 – Installing the LiteLLM proxy on macOS

```shell
uv tool install 'litellm[proxy]'
litellm --version

LiteLLM: Current Version = 1.98.0
```

```shell
# init config
mkdir -p ~/.config/litellm
```

**Important**: Use `echo "model_list: []"` instead of `touch` to create a valid LiteLLM config file. If you use `touch`, the file will be empty and LiteLLM will fail to start.

```yaml
model_list: []
```

```shell
echo "model_list: []" > ~/.config/litellm/config.yaml
```

**Starting the server**

```bash
litellm --config ~/.config/litellm/config.yaml --port 4000
# INFO: Proxy running on http://0.0.0.0:4000
```

## Part 2 — Ollama integration

Current working config (YAML):
```yaml
model_list:
  - model_name: ollama/qwen3.8:27b-mlx
    litellm_params:
      model: ollama_chat/qwen3.8:27b-mlx
      api_base: http://localhost:11434
    model_info:
      supports_function_calling: true
```

```bash
curl http://localhost:4000/v1/chat/completions \
  -H "Authorization: Bearer sk-1234" \
  -H "Content-Type: application/json" \
  -d '{"model":"ollama/qwen3.8:27b-mlx","messages":[{"role":"user","content":"Say hi in 5 words"}]}'
```

## Huggingface CLI

The `hf` CLI (part of `huggingface_hub`) is used to download and manage models from Hugging Face. These models can be used with multiple backends:

```shell
uv tool install huggingface_hub
```

Common usage:
- `hf download <model-id>` — download a model to your local cache (`~/.cache/huggingface`)
- `hf login` — authenticate with Hugging Face
- `hf ls` — list models or browse the registry

**Used by:**
- **llama.cpp** — download .gguf models via `hf download` and use with `llama-server`
- **oMLX** — download MLX-compatible models via `hf download` and serve with `omlx serve`



## Part 3 — llama.cpp integration

```bash
xcode-select --install       # Command Line Tools (clang), if not present
brew install llama.cpp       # installs llama-cli, llama-server, etc.
```

## Downloading a model with the `hf` CLI

Use the Hugging Face CLI to download a model into the local cache. Llama.cpp expects `.gguf` format models:

```shell
# Log in if needed (for gated models)
hf login

# Download a model (e.g., Qwen3.8 27B in 4-bit)
hf download "qqwookie/Qwen3.8-27B-4bit-GGUF"
```

The model will be saved to `~/.cache/huggingface/hub/<username>/<model-name>/<revision>/`. To convert it to GGUF format (if needed), see the llama.cpp model conversion guide.

## Verification that llama.cpp can run the model

After downloading and converting a model to `.gguf` format, start llama-server:

```shell
llama-server \
  -m ~/models/qwen3.8-27b.Q4_K_M.gguf \
  --host 127.0.0.1 \
  --port 8080 \
  --n-gpu-layers 999 \
  --ctx-size 16384
```

Verify it works:

```shell
curl http://localhost:8000/v1/models
```

Or test generation:

```shell
curl http://localhost:8000/completion \
  -d '{"model":"local-llamacpp","prompt":"Say hello:"}' \
  -H "Content-Type: application/json"
```

```

## oMLX

MLX backend for Apple Silicon (installed via `brew install omlx`). Model files are stored in `~/.omlx/models/`:

```shell
# Download model using the HF CLI (already done)
hf download mlx-community/Qwen3.8-27B-4bit
# Models saved to: ~/.omlx/models/
ls ~/.omlx/models/
```

## oMLX Server

Start the oMLX server:

```shell
omlx serve --model-dir ~/.omlx/models
# or: omlx start  # managed background service
```

### Configuration

The server reads `~/.omlx/settings.json`. Common settings:

```json
{
  "api_key": "",
  "skip_api_key_verification": true
}
```

Setting `skip_api_key_verification` to `true` allows running without an API key for local development.

### Configuring LiteLLM for oMLX

To use oMLX models through the LiteLLM proxy, add the model to your `~/.config/litellm/config.yaml`. The updated config now includes:

```yaml
model_list:
  # ---- Ollama (local) ----
  - model_name: ollama/qwen3.8:27b-mlx
    litellm_params:
      model: ollama_chat/qwen3.8:27b-mlx
      api_base: http://localhost:11434
    model_info:
      supports_function_calling: true

  # ---- oMLX / MLX (Apple Silicon fourth backend) ----
  - model_name: mlx-llama-3.2-3b
    litellm_params:
      model: openai/mlx-community/Llama-3.2-3B-Instruct-4bit
      api_base: http://localhost:8000/v1
      api_key: "not-needed"
```

With this config, you can test oMLX through the proxy:

```shell
curl http://localhost:4000/v1/chat/completions \
  -H "Authorization: Bearer sk-1234" \
  -H "Content-Type: application/json" \
  -d '{"model":"mlx-llama-3.2-3b","messages":[{"role":"user","content":"hi"}]}'`
```

```shell
# Or without auth (if skip_api_key_verification: true in settings.json)
curl http://localhost:4000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"mlx-llama-3.2-3b","messages":[{"role":"user","content":"hi"}]}'`
```





## Configuring LiteLLM for OpenRouter

To use OpenRouter models through the LiteLLM proxy, add the model to your \`~/.config/litellm/config.yaml\`:

```yaml
model_list:
  # ---- Ollama (local) ----
  - model_name: ollama/qwen3.8:27b-mlx
    litellm_params:
      model: ollama_chat/qwen3.8:27b-mlx
      api_base: http://localhost:11434
    model_info:
      supports_function_calling: true

  # ---- oMLX / MLX (Apple Silicon fourth backend) ----
  - model_name: omlx/Qwen3.8-27B-4bit
    litellm_params:
      model: omlx/Qwen3.8-27B-4bit
      api_base: http://localhost:8000/v1
      api_key: "not-needed"

  # ---- OpenRouter (cloud) ----
  - model_name: openrouter/qwen/qwen3.8-27b
    litellm_params:
      model: openrouter/qwen/qwen3.8-27b
      api_key: os.environ/OPENROUTER_API_KEY
      api_base: https://openrouter.ai/api/v1
```

### API Key Storage Options

**Option 1: Store directly in config.yaml** (simple, not for production/commits):
```yaml
api_key: "sk-or-v1-..."
```
*Convenient for local development, but do not commit this file to version control with real keys.*

**Option 2: Use environment variable** (recommended for production/sharing):
```yaml
api_key: os.environ/OPENROUTER_API_KEY
```
*Reads the key from your shell at runtime. Set `export OPENROUTER_API_KEY="sk-or-v1-..."` before starting LiteLLM.*

**Option 3: Via environment variable (alternative syntax)**:
You can also just set the env var and LiteLLM will pick it up automatically without needing the `os.environ/` prefix in the config.

### Setup Steps

1. **Get an OpenRouter API key:**
   - Go to [openrouter.ai/keys](https://openrouter.ai/keys)
   - Create an account and generate a key (starts with `sk-or-v1-`)
   
2. **Choose your storage method:**
   - **Direct in config**: `api_key: "sk-or-v1-..."` (local dev only)
   - **Environment variable**: `api_key: os.environ/OPENROUTER_API_KEY` (recommended)
   
3. **Test through the proxy:**
   ```shell
   curl http://localhost:4000/v1/chat/completions \
     -H "Content-Type: application/json" \
     -d '{"model":"openrouter/qwen/qwen3.8-27b","messages":[{"role":"user","content":"hi"}]}'
   ```
   
   Or with explicit auth:
   ```shell
   curl http://localhost:4000/v1/chat/completions \
     -H "Authorization: Bearer sk-1234" \
     -H "Content-Type: application/json" \
     -d '{"model":"openrouter/qwen/qwen3.8-27b","messages":[{"role":"user","content":"hi"}]}'
   ```
## Configuring LiteLLM for llama.cpp

To use llama.cpp models through the LiteLLM proxy, add the model to your \`~/.config/litellm/config.yaml\`:

```yaml
model_list:
  # ---- Ollama (local) ----
  - model_name: ollama/qwen3.8:27b-mlx
    litellm_params:
      model: ollama_chat/qwen3.8:27b-mlx
      api_base: http://localhost:11434
    model_info:
      supports_function_calling: true

  # ---- oMLX / MLX (Apple Silicon fourth backend) ----
  - model_name: omlx/Qwen3.8-27B-4bit
    litellm_params:
      model: omlx/Qwen3.8-27B-4bit
      api_base: http://localhost:8000/v1
      api_key: "not-needed"

  # ---- llama.cpp (local inference) ----
  - model_name: llama.cpp/local-llama
    litellm_params:
      model: openai/local-model
      api_base: http://localhost:8080/v1
      api_key: "dummy-key"
```

With this config, you can test llama.cpp through the proxy:

```shell
curl http://localhost:4000/v1/chat/completions \
  -H "Authorization: Bearer sk-1234" \
  -H "Content-Type: application/json" \
  -d '{"model":"llama.cpp/local-llama","messages":[{"role":"user","content":"hi"}]}'
```

```shell
# Or without auth (if using custom auth setup)
curl http://localhost:4000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"llama.cpp/local-llama","messages":[{"role":"user","content":"hi"}]}'
```
### Testing oMLX through LiteLLM proxy

```shell
curl http://localhost:8000/v1/chat/completions   -H "Content-Type: application/json"   -d '{"model":"mlx-llama-3.2-3b","messages":[{"role":"user","content":"hi"}]}'`

```shell
curl http://localhost:4000/v1/chat/completions   -H "Authorization: Bearer sk-1234"   -H "Content-Type: application/json"   -d '{"model":"mlx-llama-3.2-3b","messages":[{"role":"user","content":"hi"}]}'`
```

```

## Auto-Start on Machine Restart

Goal: All LLM providers (Ollama is already configured; llama.cpp, oMLX, LiteLLM proxy below) should be running whenever the machine boots — no manual steps required.

### oMLX (managed background service)

oMLX ships with a managed-service command:

```bash
# Start now and enable autostart on login
omlx start

# Verify
omlx status
```

This registers oMLX with `launchd` and keeps it running on `localhost:8000`.

### llama.cpp (custom LaunchAgent)

`llama.cpp` doesn't have a built-in service command. Create a `launchd` plist:

```bash
# 1. Create the plist directory
mkdir -p ~/Library/LaunchAgents

# 2. Write the plist (adjust model path and port as needed)
cat > ~/Library/LaunchAgents/local.llamacpp.server.plist << 'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>local.llamacpp.server</string>
    <key>ProgramArguments</key>
    <array>
        <string>/opt/homebrew/bin/llama-server</string>
        <string>-m</string>
        <string>/Users/keith/models/qwen3.8-27b.Q4_K_M.gguf</string>
        <string>--host</string>
        <string>127.0.0.1</string>
        <string>--port</string>
        <string>8080</string>
        <string>--n-gpu-layers</string>
        <string>999</string>
        <string>--ctx-size</string>
        <string>16384</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>/Users/keith/.llamacpp.log</string>
    <key>StandardErrorPath</key>
    <string>/Users/keith/.llamacpp.err.log</string>
</dict>
</plist>
EOF

# 3. Load and start
launchctl load -w ~/Library/LaunchAgents/local.llamacpp.server.plist

# 4. Verify
launchctl list | grep llamacpp
```

### LiteLLM proxy (custom LaunchAgent)

LiteLLM was installed via `uv tool install 'litellm[proxy]'`. The binary lives at `~/.local/bin/litellm`. Wrap it in a LaunchAgent:

```bash
# 1. Write the plist
cat > ~/Library/LaunchAgents/local.litellm.proxy.plist << 'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>local.litellm.proxy</string>
    <key>ProgramArguments</key>
    <array>
        <string>/Users/keith/.local/bin/litellm</string>
        <string>--config</string>
        <string>/Users/keith/.config/litellm/config.yaml</string>
        <string>--port</string>
        <string>4000</string>
    </array>
    <key>EnvironmentVariables</key>
    <dict>
        <key>PATH</key>
        <string>/Users/keith/.local/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin</string>
        <key>OPENROUTER_API_KEY</key>
        <string>sk-or-v1-REPLACE_WITH_YOUR_KEY</string>
    </dict>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>/Users/keith/.litellm.log</string>
    <key>StandardErrorPath</key>
    <string>/Users/keith/.litellm.err.log</string>
</dict>
</plist>
EOF

# 2. Load and start
launchctl load -w ~/Library/LaunchAgents/local.litellm.proxy.plist

# 3. Verify
launchctl list | grep litellm
```

### Verification (all four running)

```bash
# Quick check
lsof -iTCP -sTCP:LISTEN -P | grep -E "11434|8000|8080|4000"

# Or test each endpoint
curl -s http://localhost:11434/api/tags > /dev/null && echo "Ollama: OK"
curl -s http://localhost:8000/v1/models > /dev/null && echo "oMLX: OK"
curl -s http://localhost:8080/v1/models > /dev/null && echo "llama.cpp: OK"
curl -s http://localhost:4000/v1/models > /dev/null && echo "LiteLLM: OK"
```

### Stopping / disabling autostart

```bash
# oMLX
omlx stop

# llama.cpp
launchctl unload ~/Library/LaunchAgents/local.llamacpp.server.plist

# LiteLLM
launchctl unload ~/Library/LaunchAgents/local.litellm.proxy.plist
```

### Notes

- `brew services` and `launchctl` agents run in the **user** session, so they start on **login** (not on bare boot without a user). For headless/auto-login scenarios, move plists to `/Library/LaunchAgents/` (system-wide).
- `KeepAlive=true` ensures the service restarts if it crashes.
- Logs go to `~/.litellm.log`, `~/.llamacpp.log`, etc. — useful for debugging startup failures.
- The LiteLLM plist exports `OPENROUTER_API_KEY` directly so the proxy works even when the shell session is not sourced.
- For the Ollama model used here (`qwen3.8:27b-mlx`), it must already be pulled (`ollama pull qwen3.8:27b-mlx` once) before the proxy can route to it.

## When to Restart Services

After the LaunchAgents are in place, services run continuously across reboots. However, some changes require an explicit restart to take effect:

| Event | Restart needed? | Service |
|---|---|---|
| Edited `~/.config/litellm/config.yaml` | **Yes** | LiteLLM |
| Edited `~/.omlx/settings.json` | **Yes** | oMLX |
| Edited a LaunchAgent plist (port, args, env vars) | **Yes** | that service |
| Downloaded new model (`hf download`, `ollama pull`) | Sometimes | oMLX (re-scan), Ollama (auto) |
| Deleted a model from disk | Sometimes | oMLX (stale entry), llama.cpp (if loaded) |
| Upgraded `litellm` (`uv tool install --reinstall litellm[proxy]`) | **Yes** | LiteLLM |
| Upgraded `llama.cpp` (`brew upgrade llama.cpp`) | **Yes** | llama.cpp |
| Upgraded `omlx` (`brew upgrade omlx`) | **Yes** | oMLX |
| Machine reboot | No — handled by `KeepAlive=true` and `RunAtLoad` | all |

**Rule of thumb:** if you changed a **config file** or **binary**, restart. If you changed a **model file**, restart only the backend that needs to re-scan its model directory.

## Restart Script: `llm-restart`

A wrapper script is installed at `~/.local/bin/llm-restart` to restart all four providers in one command:

```bash
llm-restart              # restart all four
llm-restart litellm      # restart only LiteLLM
llm-restart omlx         # restart only oMLX
llm-restart llama.cpp    # restart only llama.cpp server
llm-restart ollama       # restart only Ollama
```

The script:
1. Reloads each LaunchAgent via `launchctl unload` + `load -w`
2. For brew-managed services, uses `brew services restart`
3. Verifies each endpoint with up to 15s of retries (model loading can take time)
4. Prints a green/red summary so you can spot failures immediately

**Typical workflow after editing the LiteLLM config:**

```bash
$EDITOR ~/.config/litellm/config.yaml
llm-restart litellm
```

**After a new HF model download for oMLX:**

```bash
hf download mlx-community/Qwen3.8-27B-4bit
llm-restart omlx
```

Source of the script is documented inline (`llm-restart --help` prints the comment block at the top).

## Model Switching — Hot-Swap vs Restart Required

The `wt` CLI in `agent-worktree/` selects a model via its `<provider>/<model>` format (e.g. `ollama/qwen3.8:27b-mlx`). Behind the scenes, every request goes through the LiteLLM proxy at `localhost:4000`, which routes to the right backend.

**The LiteLLM proxy itself never needs a restart to switch models** — it routes based on the `model` field in each request. But whether the **backend service** needs a restart depends on the provider:

| Provider | Hot-swap? | Notes |
|---|---|---|
| **Ollama** (11434) | ✅ Yes | Loads any pulled model on demand; unloads after idle TTL |
| **OpenRouter** (`openrouter/*`) | ✅ Yes | Pure API; LiteLLM just forwards the model slug |
| **oMLX** (8000) | ⚠️ Restart | Loads one model from `~/.omlx/models/` at startup |
| **llama.cpp** (8080) | ⚠️ Restart | Loads one `.gguf` file at startup via the `-m` plist arg |

### Practical implications for `wt`

```bash
# These need NO restart — just pass a different -M:
wt -M ollama/qwen3.8:27b-mlx            # local Ollama
wt -M openrouter/qwen/qwen3.8-27b      # cloud OpenRouter

# These DO need a restart of the backend first:
wt -M omlx/Qwen3.8-27B-4bit            # uses whatever oMLX loaded at startup
wt -M llama.cpp/local-llama             # uses whatever llama.cpp loaded at startup
```

### Switching the oMLX model

The oMLX server scans `~/.omlx/models/` once at startup. To use a different model:

```bash
# 1. Download (if needed)
hf download mlx-community/<new-model>

# 2. Restart oMLX to pick it up
llm-restart omlx
```

After restart, omlx picks the model to load based on its own logic (typically the most recent or all models; check `~/.omlx/settings.json` and the admin dashboard at `http://localhost:8000/admin`).

### Switching the llama.cpp model

Edit the `-m` path in the LaunchAgent plist:

```bash
# 1. Download the GGUF model
hf download <user>/<model>.gguf
mv ~/.cache/huggingface/hub/<user>/<model>/*.gguf ~/models/

# 2. Update the plist
$EDITOR ~/Library/LaunchAgents/local.llamacpp.server.plist   # change -m path

# 3. Restart
llm-restart llama.cpp
```

### Quick decision table

| You want to use… | Restart needed? |
|---|---|
| Any **ollama/** model | None — Ollama hot-loads |
| Any **openrouter/** model | None — pure API routing |
| A new **omlx/** model | `llm-restart omlx` |
| A new **llama.cpp/** model | `llm-restart llama.cpp` (after updating the plist) |
| A **brand-new** model added to litellm config | `llm-restart litellm` (so the proxy knows about it) |

### Adding a new model to the LiteLLM config (no agent change needed)

Once a model is in `~/.config/litellm/config.yaml`, it's immediately usable from `wt` without any agent-worktree code change — as long as the ID matches `<provider>/<name>`:

```bash
$EDITOR ~/.config/litellm/config.yaml      # add new model entry
llm-restart litellm                        # reload proxy
wt -M <provider>/<new-model>               # use it
```

## Setup Audit — qwen3.8 Models

This doc tracks only the **qwen3.8** models actually downloaded on this machine (other models mentioned in `docs/reference/` are ignored per user direction).

### Models actually downloaded

| Provider | Model | Format | Location |
|---|---|---|---|
| **Ollama** | `qwen3.8:27b-mlx` | MLX (nvfp4, 18 GB) | `~/.ollama/models/` |
| **oMLX** | `Qwen3.8-27B-4bit` (mlx-community) | MLX 4-bit | `~/.omlx/models/mlx-community/Qwen3.8-27B-4bit/` |
| **llama.cpp** | `Qwen3.8-27B-UD-Q4_K_XL.gguf` (unsloth) | GGUF Q4_K_XL (16 GB) | `~/.cache/huggingface/hub/models--unsloth--Qwen3.8-27B-GGUF/snapshots/.../Qwen3.8-27B-UD-Q4_K_XL.gguf` |
| **OpenRouter** | `qwen/qwen3.8-27b` | cloud API | routed via `https://openrouter.ai/api/v1` |

### Service & auto-start status (all four ✅)

| Provider | Service running | LiteLLM entry | LaunchAgent | Port |
|---|---|---|---|---|
| **Ollama** | ✅ | ✅ `ollama/qwen3.8:27b-mlx` | ✅ `com.ollama.ollama` (system) | 11434 |
| **oMLX** | ✅ | ✅ `omlx/Qwen3.8-27B-4bit` | ✅ `homebrew.mxcl.omlx` (brew services) | 8000 |
| **llama.cpp** | ✅ | ✅ `llama.cpp/local-llama` | ✅ `local.llamacpp.server` (loaded) | 8080 |
| **LiteLLM proxy** | ✅ | ✅ all of the above | ✅ `local.litellm.proxy` (loaded) | 4000 |
| **OpenRouter** | ✅ API | ✅ `openrouter/qwen/qwen3.8-27b` | n/a (cloud) | — |

### Verification

All four endpoints respond. `wt -M <provider>/<model>` works for any of the qwen3.8 entries:

```bash
llm-restart      # all four green
curl http://localhost:4000/v1/models   # shows all litellm-routed models
```

### Done — no remaining setup

- ✅ All four LaunchAgents loaded with `KeepAlive=true` and `RunAtLoad`
- ✅ `OPENROUTER_API_KEY` embedded in the LiteLLM plist (no shell source needed after reboot)
- ✅ `skip_api_key_verification: true` in `~/.omlx/settings.json`
- ✅ llama.cpp plist points at the **actual GGUF** in the HF cache (not a placeholder)
- ✅ `llm-restart` script handles all four; for Ollama uses `launchctl kickstart -k gui/$(id -u)/com.ollama.ollama`

### Benchmarking the four qwen3.8 variants

For comparing the four backends (Ollama, oMLX, llama.cpp, OpenRouter) under controlled conditions, see **[`benchmarks/qwen3.8-benchmark.md`](../benchmarks/qwen3.8-benchmark.md)**. That doc covers:

- The isolation strategy (only one local model loaded at a time)
- The `~/.local/bin/qwen3.8-benchmark` script
- Latest benchmark results
- Problems encountered while getting the script to work (bash version traps, array subscript gotchas, model-load detection issues)
- How to extend the benchmark for new models/providers

### Benchmark isolation helpers

`modelman benchmark` delegates service isolation to two helpers in this
repo's `bin/` directory:

- **`bin/llm-isolate-provider <provider-id>`** — stops the other local
  providers, starts + warms up the requested one (ollama / llamacpp /
  omlx / omlx-6bit), and prints a JSON result with the provider, model,
  and direct backend URL. `omlx` warms up the 4-bit variant and
  `omlx-6bit` the 6-bit variant.
- **`bin/llm-restore-providers`** — brings all local providers (and the
  LiteLLM proxy) back up after a benchmark run.

Ensure `local-ai-setup/bin` is on `PATH` so `modelman benchmark` can find
them. Model names and URLs are overridable via `LLM_ISOLATE_*` env vars.
