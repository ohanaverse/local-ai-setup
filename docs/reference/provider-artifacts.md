# Provider artifacts — llama.cpp (retired), LiteLLM, oMLX

> Use this to: restore or rebuild any provider on this machine from the
> exact files it shipped with. Verified against the host on 2026-09-07.
>
> llama.cpp was **retired** on 2026-09-07 (issue #33: its LaunchAgent
> pointed at a GGUF that no longer existed and crash-looped at every
> login). Its artifacts and the full re-enable procedure are preserved
> below. LiteLLM and oMLX remain **active**; their artifacts are captured
> here so the stack can be rebuilt without archaeology.

## Artifact inventory

| Artifact | Source on host (2026-09-07) | Redacted? |
|---|---|---|
| [`artifacts/llamacpp/local.llamacpp.server.plist`](artifacts/llamacpp/local.llamacpp.server.plist) | `~/Library/LaunchAgents/local.llamacpp.server.plist` | no secrets present |
| [`artifacts/llamacpp/local.llamacpp.server.plist.qwen3.8.bak`](artifacts/llamacpp/local.llamacpp.server.plist.qwen3.8.bak) | `~/Library/LaunchAgents/local.llamacpp.server.plist.qwen3.8.bak` | no secrets present |
| [`artifacts/litellm/llamacpp-model-rows.yaml`](artifacts/litellm/llamacpp-model-rows.yaml) | the two llama.cpp rows in `~/.config/litellm/config.yaml` | rows contain only `api_key: dummy-key` |
| [`artifacts/litellm/local.litellm.proxy.plist`](artifacts/litellm/local.litellm.proxy.plist) | `~/Library/LaunchAgents/local.litellm.proxy.plist` | **yes** — 5 values replaced with `REDACTED-<KEY>` |
| [`artifacts/omlx/homebrew.mxcl.omlx.plist`](artifacts/omlx/homebrew.mxcl.omlx.plist) | `~/Library/LaunchAgents/homebrew.mxcl.omlx.plist` | no secrets present |
| [`artifacts/registry/llamacpp-provider.toml`](artifacts/registry/llamacpp-provider.toml) | `[[providers]]` block in `~/.config/local-ai/registry.toml` | no secrets present |

The litellm plist's redacted keys — re-fill each from the live plist or a
secret store at restore time: `OPENROUTER_API_KEY`, `LITELLM_MASTER_KEY`,
`LITELLM_SALT_KEY`, `UI_PASSWORD`, `DATABASE_URL`.

## llama.cpp — RETIRED 2026-09-07

State at retirement: the LaunchAgent was loaded but crash-looping (exit 1)
because its pinned GGUF had been deleted:
`~/.cache/huggingface/hub/models--unsloth--Qwen3.8-27B-GGUF/snapshots/4ca720788d1e01f1bff70c033e0d0028fd02e502/Qwen3.8-27B-UD-Q4_K_M.gguf`
(~90k "failed to load model" lines in `~/.llamacpp.err.log`). Removed on the
host: both plists, both log files, and the Homebrew formula. Disabled in the
repo: the `llamacpp` entries in the benchmark scripts, `SUPPORTED_PROVIDER_IDS`
(`modelman/src/modelman/benchmark/isolation.py`), `DEFAULT_PROVIDER_IDS`
(`modelman/src/modelman/registry.py`), and the two litellm rows. **Kept:** the
provider implementation `modelman/src/modelman/providers/llamacpp.py` (marked
UNUSED in its module docstring) and the `llamacpp` case branch in
`bin/llm-isolate-provider`.

### To re-enable llama.cpp

1. `brew install llama.cpp` (the formula was uninstalled; confirms
   `/opt/homebrew/bin/llama-server` exists).
2. Restore the LaunchAgent:
   `cp docs/reference/artifacts/llamacpp/local.llamacpp.server.plist ~/Library/LaunchAgents/`
   (or start from the template in `01-initial-setup.md`'s history — the
   artifact copy already pins `--port 8080`, `--ctx-size 16384`,
   `--n-gpu-layers 999`).
3. Re-download the pinned GGUF to the exact path the plist names:
   `hf download unsloth/Qwen3.8-27B-GGUF` — verify the snapshot hash matches
   `4ca720788d1e01f1bff70c033e0d0028fd02e502`; if HF rebased the snapshot,
   update the plist's `-m` path instead of the hash.
4. `launchctl load -w ~/Library/LaunchAgents/local.llamacpp.server.plist`,
   then check `curl -s http://localhost:8080/v1/models`.
5. Re-add the litellm rows from
   [`artifacts/litellm/llamacpp-model-rows.yaml`](artifacts/litellm/llamacpp-model-rows.yaml)
   to `~/.config/litellm/config.yaml` and
   `launchctl kickstart -k gui/$(id -u)/local.litellm.proxy`.
6. Re-add the registry block from
   [`artifacts/registry/llamacpp-provider.toml`](artifacts/registry/llamacpp-provider.toml)
   to `~/.config/local-ai/registry.toml` (with modelman not running).
7. Re-enable the code wiring (each was removed 2026-09-07 — see git history
   of this repo for the exact diffs):
   - `llamacpp` back in `DEFAULT_PROVIDER_IDS` (`modelman/src/modelman/registry.py`)
   - `llamacpp` back in `SUPPORTED_PROVIDER_IDS` (`modelman/src/modelman/benchmark/isolation.py`)
   - `llama_cpp` entries back in `benchmarks/qwen3.8-benchmark` and
     `benchmarks/ornith-1.5-benchmark` (`DIRECT_URLS`, `DIRECT_MODELS`,
     `LITELLM_MODELS`, `ISOLATE_ID`, `ISOLATE_ENV`, `display_key`,
     `ensure_all_local_started`, backend loops)
   - llamacpp `restart_launchd` line back in `bin/llm-restore-providers`
   - remove the UNUSED marker from `modelman/src/modelman/providers/llamacpp.py`
   - drop the retirement notes from `docs/guides/` and root `CLAUDE.md`

## LiteLLM — active

- Proxy: `litellm --config ~/.config/litellm/config.yaml --port 4000`, kept
  alive by `~/Library/LaunchAgents/local.litellm.proxy.plist` (artifact:
  redacted copy — see inventory for the five keys to re-fill).
- `model_list`: modelman owns exposed ids; the hand-managed rows are the 3
  omlx variants, the `openrouter/qwen/qwen3.8-*` set, and `ollama/q8` /
  `ollama/o35`. (The 2 llama.cpp rows were retired — kept in
  [`artifacts/litellm/llamacpp-model-rows.yaml`](artifacts/litellm/llamacpp-model-rows.yaml).)
- Setup/deep-dive: [01-initial-setup.md](../guides/01-initial-setup.md),
  [04-litellm-config.md](../guides/04-litellm-config.md).
- Gotcha: never commit or paste the live config/plist — real `sk-or-v1-…`
  keys live in both.

## oMLX — active

- Server on `:8000` (`omlx start` / `omlx stop`), kept alive by
  `~/Library/LaunchAgents/homebrew.mxcl.omlx.plist` (installed by Homebrew;
  artifact: verbatim copy).
- Serves both the 4-bit and 6-bit MLX variants from one service — warmup
  must name the exact variant.
- Setup: [01-initial-setup.md](../guides/01-initial-setup.md),
  backend reference: [oMLX Download and Run.md](oMLX%20Download%20and%20Run.md).
