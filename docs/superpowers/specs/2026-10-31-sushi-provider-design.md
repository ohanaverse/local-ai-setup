# Sushi provider: design

Date: 2026-10-31. Status: draft for approval.

## Intent

Sushi (`beamivalice/tap/sushi` 1.4.1, installed) runs large MoE models on Apple Silicon by streaming weights and an n-gram table from SSD. Keith wants to compare Qwen3.8-Flash-Next (125B MoE, pack `beamster/Qwen3.8-Flash-Next-Sushi-STQ-2.75bpw`, 46 GB resident, 75.9 GB on disk) against `ollama/qwen3.8:27b-mlx` on a 64 GB Mac Studio. That needs Sushi to be a first-class local provider:

1. **wt** can list, start and stop its model, and launch `claude` and `pi` against it.
2. **llmbench** can isolate it and use it as a row in `llmbench eval`, and in the throughput and agent benchmarks.

Sushi cannot be added by editing the registry. `llmbench eval` rows must name a model in `registry.toml`, and the registry may only name a provider the wt table knows (routing, listing and lifecycle fail otherwise). So the provider row, the contract file and the llmbench side ship together.

## Goals

- `wt start|stop|list` and `wt -A claude|pi -M sushi/...` work for the Qwen3.8-Flash-Next pack.
- `llmbench provider isolate|stop sushi`, `restore`, and `llmbench eval|run|agent` rows with a sushi model work, serially with ollama (never two big models resident).
- The context window is always 131072 (Keith's baseline) and can never silently be Sushi's 4096 default.
- A start that cannot work (wired limit too low) fails early with the fix command, before other providers are stopped.
- Existing providers (mtplx, rec, omlx, ollama) behave byte-identically after the shared-engine changes.

## Non-goals

- Mirroring `sushi launch <agent>`. wt writes `~/.pi/agent/models.json`; Sushi keeps its own `~/.sushi/pi`.
- Discovering unregistered packs, or `wt` per-pack unload (`/v1/unload-model` is unverified). Every pack needs a registry row; eval needs one anyway.
- Setting `iogpu.wired_limit_mb` (needs sudo, resets on reboot). We check and tell; we never set it.
- Declaring the `anthropic` protocol so claude dials Sushi's `/v1/messages` directly. Untested against Claude Code. Claude goes through LiteLLM.
- Per-model `serve_args` for Sushi (no ServeArgs on the family).
- Editing eval judge code (PR #505 owns `eval/suite.py` JUDGE_ROUTES, `eval/runner.py` judge factory, `judge_core.py`).
- A `bin/update/sushi` tool, a modeltiers alias, a `sysctl`-setting helper. Later, optional.

## Anti-goals (a failure even if it technically works)

- A benchmark that ran at 4096 (or any unknown) context.
- Running the bare `sushi` binary as a "stop" command.
- Every pack showing Running because `/v1/models` lists unloaded packs.
- Killing a process wt did not start, or one whose pid/argv/start time does not verify.
- Tests that run the real `sushi`, dial port 12345, or touch the real `/tmp/local-ai-setup-sushi.pid`.
- A mtplx/rec regression from the shared-engine changes.

## Measured Sushi facts relied on

Measured on this machine with scratch servers on ports 12396-12399 (all stopped afterwards), `sushi --help`/`serve --help`, `strings` on the binary, and `~/.sushi/logs`. No model was loaded in the design phase.

| Fact | How measured |
|---|---|
| `sushi serve` is headless and model-less: serves every pack under `~/.sushi/models`, loads one on demand by exact id `org/dir`. Default port 12345, host 127.0.0.1. | startup banner, logs |
| `/health` returns `200 {"status":"ok"}` about 0.06 s after spawn, with nothing loaded. | curl |
| `GET /v1/models` lists all packs with `loaded` (bool), `state` (`unloaded`/`ready`), `bytes_resident`, `bytes_on_disk`, `capabilities` (tool_use, vision, ...). `context_length` is the pack max (1048576), NOT `--ctx-size`. | curl |
| Unknown model id on any inference route gives `503 No default model configured`, not 404. | curl |
| Argv shows exactly as passed (`sushi serve --port N --ctx-size M`); no child processes; no pid or lock file. | ps, find |
| SIGTERM and SIGINT: clean exit 0 in about 1 s when idle. SIGHUP kills abruptly. Second `serve` on a used port exits 1 at once. | signal test |
| Default context was 4096 in the earlier real session log (`auto`); `--help` says "model max". Treated as 4096 unless `--ctx-size` is passed. `--ctx-size` up to 2000000 accepted at spawn. | log, `--help` |
| Cold load about 115 s, about 10 s with cached files. A prompt over ctx gets `400 exceeds ctx_size`. | log |
| Loopback is exempt from `--api-key` (unless `--api-key-strict`), so no secret is needed on 127.0.0.1. | `serve --help`, curl |
| Flags: `--max-resident-models` (default 3, LRU), `--no-update-check`, `--max-tokens` (help: default 100 for requests that omit it, in serve mode), `--reasoning-budget`, `--max-concurrent` (1), `--timeout` (stall, 300 s). | `--help` |
| Per-request reasoning budget 8192 is the `medium` effort default (`reasoning_effort_budgets`), not `--reasoning-budget`. | `/v1/models`, log |
| Routes include `/v1/chat/completions`, `/v1/messages`, `/v1/responses`, `/v1/load-model`, `/v1/unload-model`. Load/unload bodies unverified. | banner, `strings` |
| `sysctl -n iogpu.wired_limit_mb` is readable without sudo (59000 now). | run |

**Measured in a pre-build probe (2026-10-31, scratch server on port 12398 with the exact Spawn argv below, the pack loaded):**

| Fact | Result |
|---|---|
| The Spawn argv is accepted by `sushi serve`, `/health` answers | yes, 0.12 s after spawn |
| A request with no `max_tokens` | generated 1,138 tokens and ended `finish_reason: stop`, so `--max-tokens 32768` is the default for omitted fields (per `--help` the unflagged default is 100, which would truncate every eval answer: the eval transport sends no `max_tokens`) |
| Where Qwen thinking text lands | `message.reasoning_content`; `message.content` holds only the answer (a clean 1,091-character list) |
| `/v1/models` during the cold load | the pack goes `state: unloaded` (`loaded: false`) to `loading` (`loaded: false`) to `ready` (`loaded: true`); cold load took 50 s this time (10 s to 115 s seen across runs) |
| Loaded entry | `bytes_resident` 49,483,997,626, `bytes_on_disk` 49,506,491,792 (the safetensors only; the 32 GB `ngram_table.bin` is a separate file and is not resident), `context_length` 131072 (reflects `--ctx-size` once loaded) |
| SIGTERM with the pack loaded | exit code 0 in 1.2 s, port closed |

**Still unverified:** behaviour through `wt` itself (tool calls through LiteLLM, the Exclusive replacement flow), and SIGTERM while a load is in flight. Both belong to the acceptance run.

## Chosen architecture: rec-style ServedOnly process family

Judges' winner is **B**: one model-less `sushi serve` process per machine, as a spec-driven process family (`Exclusive`, `ServedOnly`, `ScopePort`, WtPort 12345), with grafts from A and C.

| Alternative | Why rejected |
|---|---|
| A: one `sushi --serve --model <dir>` per pack | Relies on a legacy mode never run with a real pack (only bogus ids tried); needs a new `{model_path}` placeholder, llmbench serve_args forwarding (absent today) and RequiredServeArg; per-pack process contradicts Sushi's design. |
| C: hand-written Shared-tenancy backends | A Process spec must be Exclusive, so C loses the contract row, the auto-registered backends and the pidfile machinery; it needs hand-written Go and Python backends and a second contract, about 2x B. Its extras (discovery, per-pack unload) are not needed for the goal. |

B fits because a process family is auto-registered in wt (`lifecycle`) and llmbench (`ProcessServerBackend` from `process-providers.toml`); only the table row, the TOML block and four generic engine extensions are needed.

### Spec (identical in `table.go` and `process-providers.toml`)

- Provider and family id `sushi`. Provider Policy `&openaiLocal`. Seed: Name "Sushi", local, AuthType none, BaseURL `http://127.0.0.1:12345/v1`, ModelDir `~/.sushi/models`, Command `sushi`. No Protocols (default openai-chat). No secret_ref.
- Family: Served, ServedOnly, Tenancy Exclusive, Scope ScopePort, WtPort 12345, DefaultOrigin `http://127.0.0.1:12345`, DefaultModelDir `~/.sushi/models`, ServeArgs false.
- Process: Binary `sushi`, Subcommand `serve`, PortFlag `--port`, ModelFlag "", DaemonModule "".
- Spawn: `serve --port {port} --host 127.0.0.1 --ctx-size 131072 --max-resident-models 1 --no-update-check --max-tokens 32768`. No `{model}` (serve is model-less). All four flags (`--ctx-size`, `--max-resident-models`, `--no-update-check`, `--max-tokens`) are in `sushi serve --help` and this exact argv was run (see the probe table). `--max-tokens` is required, not cosmetic: the eval transport sends no `max_tokens`, so without it answers would stop at Sushi's low default.
- Stop: `[]` (see E3). Pidfile `/tmp/local-ai-setup-sushi.pid` (bare pid, shared by wt and llmbench), log `/tmp/local-ai-setup-sushi.log`, PidfileEnv `WT_SUSHI_PIDFILE`.
- Ready: `http-get` `/health` (E2). `restore_action = "stop"`. `warmup_seconds = 400`.

### Four generic engine extensions

**E1: loaded-aware served list.** `/v1/models` lists every pack. Decode an optional `loaded *bool` in `FetchModelIDsKeyed` (`localmodels/match.go`) and drop entries where it is explicitly false; entries without the field (mtplx, mlx_lm_server, ollama) are unchanged; omlx keeps its own branch in `ServedIDs`. Every "what is serving" site (inventory probes, `readyNow`, `waitForModel`, `liveServed`, `startedGone`, `cmd/wt/model_cmds.go:786`) goes through the decoder; any that decodes ids itself is routed through it. Python: same rule in `probe.py` and `process_server._lists`/`already_serving`. "Running" means the registered pack id is listed with `loaded:true`.

**E2: `http-get` ready kind.** With E1, `models-list` readiness cannot be true before the first request (the pack loads only on a request), so readiness would wait out the 300 s timeout. Add `ReadyHTTP` (contract kind `http-get`, Path required, no Command): HTTP 200 on the path. `TestTableInvariants` accepts it; `process_spec.py` `READY_KINDS` gains it. The warmup then performs the cold load.

**E3: empty Stop means signal the verified pid.** Today `processServer.stop` runs `<binary> <Stop...>` unconditionally, so an empty Stop would run bare `sushi`. New rule, both sides:
- Go: when `len(spec.Stop)==0`, extract the verification and signal path from `stopLoadingServer` into a shared helper: own uid, pid>1 and not wt, `<pidfile>.wt` start record, `serverArgvMatches` (`sushi serve ... --port N`); SIGTERM to the group, 10 s grace, SIGKILL only if the same process (start time) remains, wait for the port to close, then remove pidfile and start record. No pidfile (hand-started server): clear error "not started by wt; stop it yourself". wt never kills unknown processes. `TestTableInvariants`: empty Stop requires PidfileDefault, Subcommand, PortFlag.
- Python: in `stop_and_wait`, when `spec.stop` is empty use `self._pidproc.stop()` (`PidfileProcess`, identity guard: "sushi" in `ps` command), then wait for the port to close. No pidfile is a no-op.
- `process_spec.py` must parse `stop = []` (add a test).

**E4: per-spec warmup budget.** `processServer.start` hard-codes 120 s and `tryChat` caps each request at 120 s, against a 115 s cold load. Add `ProcessSpec.WarmupSeconds` (contract key `warmup_seconds`), explicit in all entries: mtplx 120, rec 120, sushi 400. `start` and `tryChat` take the value. Python: `WARM_TIMEOUT` becomes `spec.warmup_seconds`, `probe.warmup` takes a timeout, and `TimeoutError` is caught so it becomes an `ok=False` envelope. Because the key is required on all entries, the Go contract test struct and the Python parser change in the same commit; a malformed entry disables all process providers in llmbench.

### Grafts

- **Wired-limit preflight** (from A/C): before spawning, and in llmbench before stopping other providers, run `sysctl -n iogpu.wired_limit_mb` (darwin only). Need = 1.1 x the sum of the pack directory's `*.safetensors` file sizes, in MiB; skip if the pack directory cannot be read. This counts the resident weights only and excludes the SSD-streamed `ngram_table.bin` (pack on disk 75.9 GB = 49.5 GB safetensors + 32 GB n-gram table), the same figure `/v1/models` reports as `bytes_on_disk`. For this pack: 47,213 MiB, so need 51,934 MiB. Measured: it passes at the current limit of 59,000 and fails at the macOS default (about 49,152 MiB, 75% of 64 GiB), which is the case the check exists for. If limit < need, fail with `sudo sysctl iogpu.wired_limit_mb=59000` and "resets on reboot". Also, a warmup failure whose text looks like a memory refusal appends the same hint. Hard failure; threshold derived as above (calibration: limit 59000 gave a 57261 MB ceiling and 46.1 GB resident).
- **Unowned server** (from A): llmbench `isolate` fails closed when a Sushi already answers on the port and there is no verified pidfile (ctx unknowable). Keyed on empty `stop`, not a new contract key. wt: a running foreign server with the target pack loaded shows Running (start is a no-op) and `wt` documents that it cannot see its flags; a foreign idle server gives `PortBusyError` with a message to stop it.
- **bytes_resident** (from C): optional small improvement to feed the `--others` memory prompt instead of "memory use unknown". Only if cheap; not required.
- **Pack id check** (from C): the warmup validates the registry `model_name` against `/v1/models` first, because an unknown id returns a misleading 503.
- **`context_window` field**: separate, last commit (below).

## Model identity

- Registry provider id `sushi`, `base_url = "http://localhost:12345/v1"`, `model_dir = "~/.sushi/models"`.
- Registry model id `sushi/beamster/Qwen3.8-Flash-Next-Sushi-STQ-2.75bpw` (wt's derived id: a local id keeps the slash, `--` is the cloud spelling; the provider is the text before the first slash). `model_name = "beamster/Qwen3.8-Flash-Next-Sushi-STQ-2.75bpw"`, exactly Sushi's served id; used verbatim for the warmup, direct requests, `openai/<model_name>` LiteLLM routes and the pi models.json key (pi splits on the first slash only).
- `model_info`: `supports_function_calling = true`, `supports_vision = true` (hand-set; `/v1/models` reports tool_use and vision; `wt model add` only detects these for ollama).
- ServedOnly: wt lists registered models only; a stopped registered pack is a start row. No inventory.go code, no artifact scan.
- Ordering: the wt build with the table row is built and installed BEFORE any registry row names sushi.
- Correction 2026-10-31: the first draft assumed a `--` spelling; wt's derived id keeps the slash (found when the docs were verified against `wt model add`).

## Context size

`--ctx-size 131072` is a literal in Spawn in both mirrors. It is therefore wt-owned, not overridable by serve_args, one number serves wt and llmbench, and the 4096 default cannot occur through our tools. To run another window (e.g. 262144), edit the Spawn literal in `table.go` and `process-providers.toml` in one commit and rebuild; a restart applies it. Known gaps, documented not coded: the effective ctx of a running server is invisible (`/v1/models` shows 1048576); a hand-started server is refused by llmbench and warned about by wt.

Agents: Claude Code assumes a 200K window (`CLAUDE_CODE_MAX_CONTEXT_TOKENS` is declared only for cloud tiers) and pi's models.json hard-codes `contextWindow 262144` in `pi_models.go launchEntry`. Interim: document `CLAUDE_CODE_MAX_CONTEXT_TOKENS=131072` in a slot-profile env. Follow-up commit (separate, own tests, default path byte-identical for all other providers): optional registry model field `context_window` read by `pi_models.go` and `claude_window.go`; the sushi row sets 131072.

## Start, stop, warmup, readiness

Start (`wt start`, `llmbench provider isolate sushi`):
1. Binary on PATH; wired-limit preflight (before stopping anything).
2. Origin and port from the registry (default 12345). Port must be closed within 10 s, else PortBusyError. Exclusive: wt asks about / stops other occupants; llmbench stops all other providers first.
3. Spawn with Setsid (protects from SIGHUP), stdout/stderr to the log, bare pid to the pidfile, `.wt` start record.
4. Ready: `GET /health` 200 (about 0.1 s).
5. Warmup: validate the id against `/v1/models`, then 1-token chat naming `model_name`, timeout 400 s. This is the cold load (about 115 s, about 10 s cached).
6. Confirm `loaded:true` via the E1 list. On failure: kill the process (wt-spawned only), remove pidfile and `.wt`, show the HTTP error and log tail.

Running = pack listed with `loaded:true`. Stop = E3 for `wt stop`, `wt stop sushi`, Exclusive replacement, any other provider's isolate, and `restore`. `/v1/unload-model` is deliberately unused. During a cold load Running is false while the port is open, so a second `wt start` meets PortBusyError.

## llmbench

- `process-providers.toml`: new `[[process]]` for sushi, `stop = []`, ready `kind = "http-get"` `path = "/health"` (other ready keys empty strings; all keys required), `warmup_seconds` on all three entries. Same commit as `table.go` (pinned both ways by `TestProcessProvidersContractMatchesTheProviderTable`).
- No backend module: `backends/__init__.py` auto-registers it, so `provider list`, stop-all fan-out and restore include it.
- `registry.py` `DEFAULT_PROVIDER_IDS += "sushi"` (otherwise `llmbench run` silently skips it).
- `benchmark/isolation.py` `POSITIONAL_MODEL_PROVIDERS += "sushi"` (each row warms its own pack by name). Fix stale skill text claiming only mtplx is positional (rec is too).
- Eval: rows reference `sushi/beamster/Qwen3.8-Flash-Next-Sushi-STQ-2.75bpw`; `route = "direct"` needs `[routes.direct.sushi] base_url = "http://localhost:12345/v1"` in the suite (model sent = `model_name`, no `direct_model` needed); `route = "litellm"` also isolates by provider. Rows group by provider, so sushi and ollama run serially; `restore` stops Sushi; the claude-cli judge (PR #505) runs after restore, so no memory contention. Add `benchmarks/suites/eval-sushi-vs-ollama.toml` with a sushi row and an `ollama/qwen3.8:27b-mlx` row, pinning `[generation]` temperature, top_p, top_k explicitly for fairness.
- Agent benchmark: pi window comes from the live `~/.pi/agent/models.json` entry; direct rows need `[routes.direct.sushi]` with `api`. Until `context_window` ships, pi is told 262144.
- Sushi has `--max-concurrent 1` and a 300 s stall timeout: run sushi rows with low worker counts.

## wt agents

- `wt start|stop|list` from the table. Registered pack: start row when stopped, Running when loaded.
- `wt -A claude -M sushi/...`: protocols default to openai-chat only, so claude is LiteLLM-forced (needs the proxy configured); the proxy maps `/v1/messages` to `/v1/chat/completions` for the loopback `openai/beamster/...` row. `wt -A pi`: direct (provider `sushi`, `baseUrl` origin+`/v1`) or via the proxy. Agent configs' `supported_providers` must list sushi for claude and pi (guide 06).
- `wt smoke --tools` is agent-generic and is the acceptance test for tool use.

## Memory and prerequisites

Only one big model fits (46 GB of 64 GB). `--max-resident-models 1` makes Exclusive tenancy true. isolate sushi stops ollama/omlx/mtplx/rec/mlx_lm_server; every other isolate stops Sushi. Prerequisite for any real load: `sudo sysctl iogpu.wired_limit_mb=59000` (user runs it; resets on reboot). `--skip-mem-preflight` must never be used.

## Change list

**wt Go**
- `wt/internal/provider/table.go`: provider row, family row, consts (`sushiPort`, `sushiOrigin`).
- `wt/internal/provider/provider.go`: `ProcessSpec.WarmupSeconds`, `ReadyHTTP`; set WarmupSeconds 120 on mtplx and rec; allow empty Stop with pidfile.
- `wt/internal/lifecycle/server.go`: stop branch (E3), start uses WarmupSeconds, preflight, memory-hint on failure. `server_loading.go`: extract the shared verified-pid stop helper. `ready.go`: `ReadyHTTP`. `probe.go`: `tryChat` timeout parameter. `procinfo.go`: `IsolateProcessesForTest` covers `WT_SUSHI_PIDFILE` (automatic via PidfileEnv; confirm).
- `wt/internal/localmodels/match.go`, `served.go` (+ `cmd/wt/model_cmds.go:786`): E1.
- New small package or file for the wired-limit check (sysctl behind an injectable seam).
- Help strings that enumerate providers: `cmd/wt/model.go:79-81`, `model_write.go:92`, `model_cmds.go:74`.
- Follow-up commit: `config` model field `context_window`; `agents/pi_models.go`, `agents/claude_window.go`.

**Contract and fixtures**: `docs/contracts/process-providers.toml` (sushi block, `warmup_seconds` on all). `wt/internal/config/process_providers_contract_test.go` compares the new field. Samples (`registry.sample.toml`, `discovered-ids.sample.json`, `model-list.sample.json`) are unchanged (no artifact scan, no enumeration of providers).

**llmbench**: `providers/process_spec.py` (`warmup_seconds`, `http-get`, empty `stop`), `providers/lifecycle/backends/process_server.py` (per-kind readiness, loaded-aware `_lists`/`already_serving`, empty-stop path, warm timeout from spec, `TimeoutError`, unowned-server refusal, wired-limit preflight), `providers/probe.py` (timeout parameter, loaded filter), `registry.py`, `benchmark/isolation.py`.

**Non-code**: `benchmarks/suites/eval-sushi-vs-ollama.toml`; after install, the registry rows (provider row via `wt model init`, model via `wt model add`, not part of the PR).

## Tests (hermetic)

Rules: no real `sushi`, no dial of 12345, no real `/tmp` pidfile; random ports and httptest; doc comment on every Go test.

Go (`go test ./...` from `wt/`):
- Update pinned sets: `provider_test.go` (Families, ServedFamilies, `ProcessFamilies == [mtplx rec sushi]`, ServedOnly, SeedableLocalIDs, InstalledCommands, WtPortFor, tenancy/flags/defaults), `config/provider_table_test.go`, `localmodels/family_table_test.go`, `litellm/policy_table_test.go`; run the whole suite and fix whatever else enumerates providers (`registry_seed_test`, `cmd/wt/model_test`, `litellm_test`, `pi_models_test`).
- `TestSushiRows` and seed-row test modeled on `TestRecRows`/`TestSeedRowRec`; `TestTableInvariants` for `http-get` and empty Stop.
- Contract test passes with the new block and `warmup_seconds`.
- E1: table test of `/v1/models` bodies with `loaded` true, false, absent, for each existing family (unchanged) and sushi.
- Inventory: `loaded:false` gives a stopped start row, `loaded:true` Running; start is a no-op only when loaded.
- Lifecycle with a fake `sushi` script and httptest: `/health` readiness; warmup gets the configured timeout; 503 warmup kills the process and cleans pidfile/`.wt`; empty-Stop signals only a pid satisfying uid, start-time and argv checks and never executes the binary; missing pidfile gives a clear error; wrong-argv pid refused; mtplx and rec paths unchanged.
- Preflight: injected sysctl value below/above need; fails before other providers are stopped.
- Add `WT_SUSHI_PIDFILE` to `cmd/wt` TestMain isolation.

Python (`ruff format --check`, B905 `zip(strict=)`, `make check`/mypy):
- `tests/conftest.py` `_FAKE_BINARIES += "sushi"`; `tests/providers/lifecycle/test_hermeticity.py` parametrize case; `tests/benchmark/test_isolation.py:130` literal set; `tests/test_main.py:47-56` provider list (sushi after rec); `eval/conftest.py` `_BINARY_BACKENDS` only if a sushi eval test needs it.
- `test_process_spec.py`: sushi values, `stop=[]`, `http-get`, `warmup_seconds`, unknown ready kind rejected.
- Backend (patch names as imported into `process_server`, patch the instance `_pidproc`): loaded-aware `already_serving`, http-get readiness, empty-stop calls `_pidproc.stop` and never `subprocess.run`, warm timeout from spec, `TimeoutError` to `ok=False`, unowned-server refusal, preflight with patched sysctl.
- `test_orchestrate.py`: stop-all/restore patch the sushi `stop_and_wait`.

**Acceptance run, after the build (needs Keith's go-ahead because it replaces `~/.local/bin/wt` and writes registry rows in the real config; loads the 46 GB pack, wired limit 59000):** `make install`, `wt model init` and `wt model add` for the pack, one `wt start`, `wt smoke --tools` (claude and pi through LiteLLM), `wt stop`, and one cheap eval request. Record the tool-call behaviour and the Exclusive replacement flow. Not part of CI. The Sushi-level facts it used to cover were measured before the build (probe table above).

## Docs to update (same PR)

`CLAUDE.md` (root), `wt/CLAUDE.md` (isolation envs, process families, empty-Stop rule), `llmbench/CLAUDE.md`, `llmbench/docs/internals.md`, `wt/docs/internals/local-models.md`, `wt/docs/wt-start-stop.md` (signal-stop paragraph), `wt/docs/wt-model.md` (default-provider list), `docs/guides/02-providers-and-models.md` (install via tap, `sushi pull`, row example, 4096 trap, wired limit), `05-benchmarks.md` (isolate list, Stops column, restore, gotchas), `06` (supported_providers), `08` (log home `~/.sushi/logs`, "wt start sushi fails?"), `11` (eval recipe), `docs/reference/provider-artifacts.md` (new `## sushi` section), `wt/CHANGELOG.md`, both skills (`adding-a-provider`: stop-by-pid, http-get; `adding-a-benchmark-backend`: rec and sushi are positional). Commands not run (e.g. `sushi pull`, 75.9 GB) carry `<!-- UNVERIFIED -->`; `make check-links` must pass.

## Risks

- E1 hides a server that reports `loaded:false` for a servable model. Mitigation: filter only when the field is present; per-family tests.
- Contract schema change touches mtplx/rec entries; a bad entry disables all process providers in llmbench. Go and Python parsers change in the same commit.
- A future Sushi release could rename or drop a Spawn flag; a rejected flag fails every spawn. All four are verified against 1.4.1 only. The daily self-update check is off (`--no-update-check`), but `brew upgrade` still replaces the binary.
- Foreign or stale Sushi: ctx invisible. llmbench refuses; wt warns.
- SIGTERM with the pack resident exited 0 in 1.2 s (measured), so the 10 s grace is ample; SIGTERM during an in-flight load is unmeasured.
- A brew wrapper or argv[0] change breaks identification; `sushi update`/`brew upgrade` can replace the binary under a running server (only the daily check is disabled).
- Reasoning default (medium, 8192) and thinking-output location affect eval comparability; eval takes `message.content` only and raises on null.
- PR #505 merge seam (below).
- Registry written before wt is installed breaks provider routing.

## PR #505 merge seam

#505 (open, `eval-judge-claude-cli`) edits `llmbench/tests/conftest.py` (`_FAKE_BINARIES` line and comment, `_guarded_popen`), `tests/providers/lifecycle/test_hermeticity.py` (parametrize block), `eval/suite.py`, `eval/runner.py`, `llmbench/CLAUDE.md`, `docs/internals.md`, `benchmarks/suites/eval-sweep.toml`, guide 11. Expect textual conflicts in the `_FAKE_BINARIES` line, the hermeticity parametrize block, and possibly `internals.md`. Sushi does not touch JUDGE_ROUTES or judge code. Rebase after #505 merges and add `sushi` beside `claude`.

## Decisions I made (Keith may override)

1. Architecture B (model-less `sushi serve`, rec-style ServedOnly process family).
2. `--ctx-size 131072`, `--max-resident-models 1`, `--no-update-check` as Spawn literals; override means editing both mirrors.
3. Registry id `sushi/beamster/Qwen3.8-Flash-Next-Sushi-STQ-2.75bpw`, `model_name` = Sushi's pack id.
4. Seed protocols left default (claude via LiteLLM); `anthropic` not declared.
5. Empty Stop means signal the verified pid; `/v1/unload-model` unused.
6. Readiness is `http-get /health`; the warmup does the load with a 400 s budget (`warmup_seconds`, 120 for mtplx and rec).
7. Loaded-aware served list as a generic filter on the `loaded` field.
8. Wired-limit preflight is a hard failure, before other providers are stopped, threshold 1.1 x safetensors bytes.
9. llmbench refuses to reuse an unowned Sushi; wt warns.
10. No artifact scan or discovery; one registry row per pack.
11. `sushi` added to `DEFAULT_PROVIDER_IDS` and `POSITIONAL_MODEL_PROVIDERS`.
12. `context_window` registry field is a separate follow-up commit; interim is a profile env.
13. No `bin/update/sushi`, no modeltiers alias in this change.
14. Suite pins `[generation]` sampling explicitly for the ollama comparison.

15. **Reasoning for the comparison: defaults on both sides, nothing pinned in Spawn.** Sushi runs thinking on at medium effort (8192-token budget); ollama's 27B also thinks by default. In the offline run neither model came near a budget (every finish reason was `stop`, mean reasoning under 1.5K tokens), and both answers land in `content`. If a stricter equal-effort comparison is wanted later it is a suite-file change, not a Spawn change.
16. **`context_window` is a second PR, after this one.** This PR stays the provider; the shared `claude_window.go` / `pi_models.go` change gets its own review. Interim: `CLAUDE_CODE_MAX_CONTEXT_TOKENS=131072` in a slot-profile env.
17. **Build against current `main` and rebase after PR #505 merges.** The conflicts are confined to the `_FAKE_BINARIES` line, the hermeticity parametrize block and possibly `internals.md`.
18. **Claude through LiteLLM needs no proxy changes.** wt writes the route rows itself (`wt litellm sync`), and a provider whose model ids contain a slash (mtplx: `Youssofal/...`) already routes as `openai/<org>/<name>`; Sushi follows the same pattern.

## Needs Keith's decision

Nothing blocks the plan. Two things to weigh in on if you disagree:

1. **The four generic engine extensions (E1 to E4) change code that mtplx and rec also run through.** They are written to be byte-identical for existing providers and pinned by tests, but they are the main regression risk, and the only way to avoid them is a hand-written backend (rejected: about twice the work and a second contract).
2. **The acceptance run installs the built `wt` over `~/.local/bin/wt` and writes registry rows.** It happens only when you say so, after the build is reviewed.
