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
repo on that day: the `llamacpp` entries in the benchmark scripts,
`SUPPORTED_PROVIDER_IDS`, `DEFAULT_PROVIDER_IDS`
(`llmbench/src/llmbench/registry.py`; wt's `defaultProviderIDs` in
`wt/internal/config/registry_seed.go` never listed it), and the two litellm
rows.

**Removed from the repo** in the modelman retirement (Step 6): modelman's
`providers/llamacpp.py` with modelman itself, and llmbench's
`backends/llamacpp.py` (the `LlamaCppBackend`, with its test). Both are in git
history, but in two commits one apart — the backend outlived modelman by one
commit, so the parent of each is a different SHA (the earlier one is the only
commit that has both files):

- the commit that deleted `modelman/`
  (`git log --diff-filter=D --format=%H -1 -- modelman/pyproject.toml`); its
  parent has both files
- the commit that deleted the backend
  (`git log --diff-filter=D --format=%H -1 -- llmbench/src/llmbench/providers/lifecycle/backends/llamacpp.py`);
  its parent no longer has modelman's copy

What that changes on a machine: `llmbench provider list` has no `llamacpp`
row, `llmbench provider isolate llamacpp` and `llmbench provider stop llamacpp`
say `unknown provider: llamacpp` and stop nothing, and
`LLM_ISOLATE_LLAMACPP_MODEL` is read by nothing.
`llmbench provider stop-all --keep llamacpp` exits 1 with
`unknown provider for --keep: llamacpp` and stops nothing; it used to be
accepted. `llmbench provider stop-all`
(and the teardown `isolate` runs first) no longer unloads the LaunchAgent; if
the plist is installed, `launchctl unload` it by hand:
`launchctl unload ~/Library/LaunchAgents/local.llamacpp.server.plist`.

> **History (issue #79):** the bash isolation helpers this section originally
> pointed at (`bin/llm-isolate-provider`'s `llamacpp` case branch,
> `bin/llm-restore-providers`' `restart_launchd` line) were deleted when
> provider isolation was ported to Python. The Python backend that replaced
> them is the one removed above, so re-enabling llamacpp starts with restoring
> that module from git history (see step 7).

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
   to `~/.config/local-ai/registry.toml`.
7. Re-enable the code wiring (see git history of this repo for the exact
   diffs):
   - restore `llmbench/src/llmbench/providers/lifecycle/backends/llamacpp.py`
     and its test
     (`llmbench/tests/providers/lifecycle/backends/test_llamacpp.py`) from git
     history: `git show <that commit>^:<path> > <path>`, with the commit the
     second `git log` command above prints
   - register it in
     `llmbench/src/llmbench/providers/lifecycle/backends/__init__.py`
     (`from .llamacpp import LLAMACPP`, `BACKENDS[LLAMACPP.id] = LLAMACPP`);
     `SUPPORTED_PROVIDER_IDS` is derived from `BACKENDS`, so that also makes
     it isolable and stoppable by name. The restored module sets
     `respects_solo`, an attribute `Backend` no longer has and nothing reads:
     delete that line
   - restore `LLAMACPP_PLIST` in
     `llmbench/src/llmbench/providers/lifecycle/launchd.py` and its entry in
     `llmbench/tests/test_conftest_guards.py`
   - set the backend's `restore_action` to `"restart"` (it was `"skip"` when
     removed) so `llmbench provider restore` restarts the LaunchAgent again
   - add its id to the literal lists the tests keep
     (`tests/benchmark/test_isolation.py`, `tests/test_main.py`) and to
     `DEFAULT_PROVIDER_IDS` (`llmbench/src/llmbench/registry.py`) if the
     benchmarks should treat its models as local targets
   - correct the tests that pin the retired state, which fail once the
     backend is registered again. In
     `llmbench/tests/providers/lifecycle/test_orchestrate.py`: drop `llamacpp`
     from the ids of `test_stop_rejects_an_id_no_backend_has`, delete
     `test_isolate_rejects_the_retired_llamacpp_id`, and patch
     `BACKENDS["llamacpp"].restore` in every test that calls
     `orchestrate.restore()` (the two that assert `result.ok is True` fail
     without it; the others pass only after the restore's wait times out).
     In the restored `test_llamacpp.py`: delete
     `test_llamacpp_registered_in_backends_but_not_supported_ids` and
     `test_llamacpp_does_not_respect_solo`, and update the two tests that
     rely on `restore_action == "skip"` (`test_llamacpp_is_never_restored`,
     `test_restore_no_ops_while_restore_action_is_skip`)
   - add `llamacpp` to `defaultProviderIDs` in
     `wt/internal/config/registry_seed.go` if wt should seed its row. wt has
     no lifecycle backend for it and never had: `wt start` cannot start it
   - `llama_cpp` entries back in `benchmarks/qwen3.8-benchmark` and
     `benchmarks/ornith-1.5-benchmark` (`DIRECT_URLS`, `DIRECT_MODELS`,
     `LITELLM_MODELS`, `ISOLATE_ID`, `display_key`,
     `ensure_all_local_started`, backend loops; `ISOLATE_ID` and
     `DIRECT_MODELS` hold the `llmbench provider` CLI's provider ids — see
     `benchmarks/lib/benchmark-common.sh`)
   - drop the retirement notes from `docs/guides/` and root `CLAUDE.md`

## LiteLLM — active

- Proxy: `litellm --config ~/.config/litellm/config.yaml --port 4000`, kept
  alive by `~/Library/LaunchAgents/local.litellm.proxy.plist` (artifact:
  redacted copy — see inventory for the five keys to re-fill).
- `model_list`: wt (`wt litellm sync`) writes the routes for every configured cloud model and the
  running (or, for ollama, pulled) local models, each marked `model_info.wt_managed: true`; any other
  row is hand-managed — `wt litellm list` marks those `(hand-written)`. (The 2 llama.cpp rows were retired — kept in
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

## mlx_lm_server — active, on-demand only (no standing artifact)

- Not a LaunchAgent — a plain backgrounded `mlx_lm.server --draft-model`
  subprocess, pidfile-managed (`/tmp/local-ai-setup-mlx-lm-server.pid`, log
  at `/tmp/local-ai-setup-mlx-lm-server.log`) by
  `llmbench/src/llmbench/providers/lifecycle/pidproc.py`'s generic
  pidfile-tracked-process helper (`PidfileProcess`), used by
  `llmbench/src/llmbench/providers/lifecycle/backends/mlx_lm_server.py`
  (formerly `bin/lib/mlx-lm-server.sh`, deleted — issue #79). One behavior
  change from the bash version: the old script truncated the log file on
  every start (`>"$MLX_LM_SERVER_LOG"`); `PidfileProcess.start()` opens it
  for append (`"ab"`) instead, so the log now accumulates across restarts
  within a session rather than resetting each time — worth knowing if
  you're tailing it expecting only the current run's output.
  A plist would bake in one fixed target+draft pairing, defeating the goal
  of sweeping many pairings per benchmark session — so there is nothing to
  restore from a plist here, only the code wiring below.
- Registry: `[[providers]] id = "mlx_lm_server"` with
  `auth.base_url = "http://localhost:8001/v1"` (`defaultProviderRow` in
  `wt/internal/config/registry_seed.go`); one variant = one target+draft
  pairing (`ModelEntry.fetch` = target, `ModelEntry.draft` = draft).
- Code wiring: `defaultProviderIDs` (`wt/internal/config/registry_seed.go`)
  and `DEFAULT_PROVIDER_IDS` (`llmbench/src/llmbench/registry.py`), `SUPPORTED_PROVIDER_IDS`
  (`llmbench/src/llmbench/providers/lifecycle/backends/__init__.py`,
  re-exported by `llmbench/src/llmbench/benchmark/isolation.py`),
  the provider→LiteLLM mapping table (`wt/internal/litellm/policy.go`), the
  `MlxLmServerBackend` class in
  `llmbench/src/llmbench/providers/lifecycle/backends/mlx_lm_server.py`,
  and its unconditional stop inside `orchestrate.restore()` (this provider
  is never part of the standing baseline, so restoring the *others* is not
  enough — it must always be stopped too).
- **Local-path artifact ownership (shared with the `omlx` provider's
  `local_path` support):** a directory produced by `bin/mlx-quantize` or
  hand-run `mlx_lm.convert`/`dwq` is user-produced. wt never deletes weights:
  `wt model rm <id>`, or `d` on its row in the Models tab of `wt config`,
  removes the registry entry, prints where the weights are and touches no
  file on disk.
  Cleanup of an abandoned experiment is a manual `rm -rf`.
- Guide: [10-mlx-lm-quantization.md](../guides/10-mlx-lm-quantization.md).
