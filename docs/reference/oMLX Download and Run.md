oMLX doesn't have a single "download-and-run-this-repo" flag like llama.cpp's `-hf`. Instead it's: **start the server, get the model into its model directory, and it auto-discovers it.**

**1. Install (if you haven't already):**

```bash
brew tap jundot/omlx https://github.com/jundot/omlx
brew install omlx
```

**2. Start the server** pointed at a model directory:

```bash
omlx serve --model-dir ~/.omlx/models
```

(or `omlx start` for the managed background service, which uses the same default dir). Server listens at `http://localhost:8000/v1`.

**3. Get the model into that directory.** Two options:

- **Easiest:** use the admin dashboard at `http://localhost:8000/admin` — it has a built-in HF search/download UI, no CLI needed.
- **CLI:** download with `hf` into a flat folder under your model dir (not the raw HF cache — there's an open issue where oMLX can't parse the nested `~/.cache/huggingface/hub` snapshot structure):

```bash
hf download mlx-community/Qwen3.8-27B-4bit --local-dir ~/.omlx/models/Qwen3.8-27B-4bit
```

oMLX picks it up automatically (restart `omlx serve` if it was already running, or it may hot-detect depending on version).

**4. Test it:**

```bash
curl http://localhost:8000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"Qwen3.8-27B-4bit","messages":[{"role":"user","content":"hi"}]}'
```

**5. Wire it into LiteLLM** the same way as before:

```yaml
  - model_name: qwen3.8-mlx
    litellm_params:
      model: openai/Qwen3.8-27B-4bit
      api_base: http://localhost:8000/v1
      api_key: "not-needed"
```

Useful extra flags: `--api-key <key>` to require auth, `--paged-ssd-cache-dir ~/.omlx/cache` for KV cache persistence, `--memory-guard-gb 48` to cap RAM use.