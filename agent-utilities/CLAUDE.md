# CLAUDE.md

Standalone utilities for working with specific coding agents, one subdirectory per agent (`claude-utilities/` so far). Run by hand; nothing here is wired into `wt`, `llmbench`, the Makefile or CI.

## Commands
- `uv run --directory claude-utilities python transcript_speed.py [--min-tokens N] [--csv] ~/.claude/projects` — summary per model, or per-call CSV rows
- `uv run --directory claude-utilities python transcript_speed.py --json ~/.claude/projects > X.json`, then `... transcript_speed.py --compare A.json B.json` — aggregate-only stats per model and output-size band, per context-size band, session startup size and requested-vs-answered model counts, and a side-by-side of two machines' files (`claude-utilities/README.md`)
- `uv run --directory claude-utilities python otel_receiver.py [--port 4318] [--out FILE]` — loopback OTLP/JSON trace receiver; then `... python otel_speed.py FILE` for TTFT and decode-rate percentiles (setup and caveats in `claude-utilities/README.md`)
- `cd claude-utilities && make check test` — ruff lint + format check, then pytest

## Layout
- `claude-utilities/` — Claude Code utilities. `transcript_speed.py` estimates output tokens/second per API call from the session transcripts in `~/.claude/projects/*/*.jsonl`. `otel_receiver.py` + `otel_speed.py` do the same from Claude Code OTLP traces (adds TTFT; untested end-to-end). Usage, column definitions, caveats and a dated sample result live in `claude-utilities/README.md`.

## Conventions
- **Read-only against agent state.** A utility reads the agent's own on-disk records (transcripts, logs) and never writes to them, to `registry.toml`, or to any `wt`/LiteLLM config.
- **uv-managed, stdlib-only at runtime.** Each `<agent>-utilities/` dir has its own `pyproject.toml` (`requires-python = "==3.13.*"`, `package = false`, ruff + pytest as dev deps), `Makefile` and `tests/`, like `modeltiers/`. Scripts import only the standard library; if one needs a runtime dependency, weigh promoting it to a real package like `llmbench/` or `modeltiers/`.
- **One directory per agent**, named `<agent>-utilities` (`claude-utilities/`, later e.g. `codex-utilities/`, `pi-utilities/`). A new directory gets its own README for usage; add a line to Layout above.
- **Estimates are labeled as estimates.** These scripts derive numbers from client-side timestamps and counters, so say which columns are lower bounds or unreliable (see the README's column table) rather than presenting them as measurements.

## Gotchas
- **Transcript format is Claude Code's, not a stable API.** `transcript_speed.py` relies on `api-request` records, one `assistant` line per content block, and `message.usage.output_tokens` being the final total repeated on every block. A Claude Code update can change this silently; if the call count drops to zero or looks wrong, check the record shapes first.
- **`--json` files carry a `schema` number; regenerate old ones before comparing.** Schema 2 split the top output band (`1000+` became `1000-3000` and `3000+`) and added the context, startup and fallback sections. `--compare` still loads an older file but prints `n/a` for what it lacks and lists the mismatched bands under "only in A/B". Compare a model-by-model table, never the overall summary: it mixes models, and Opus runs slower than Sonnet.
- **Run these scripts with `uv run`, never bare `python3`.** This machine's system `python3` is 3.9; `transcript_speed.py` needs >= 3.11 (`fromisoformat` and the transcripts' trailing `Z`) and exits with a message on older Pythons. An empty result on a version without that guard means the interpreter, not the format.
- **`api-request` records only exist from 2026-10-14.** Older transcripts contribute zero calls to `transcript_speed.py` (about 39% of messages on the author's data); that is expected, not a parser bug. Check a transcript's date before debugging a low count.
- **Inspecting real OTel spans:** `OTEL_TRACES_EXPORTER=console claude -p "..." >out.txt 2>&1` prints the full `llm_request` span (`ttft_ms`, `duration_ms`, `attempt`...). Redirect to a file rather than piping to `awk`/`head`, which exit early and leave `claude -p` hanging.
- **Shell traps when testing:** macOS has no `timeout`, and `kill %1` does nothing in the non-interactive Bash tool. Start background servers with `&`, keep `$!`, and `kill` that PID. A leftover receiver makes the next run fail with "Address already in use".
- **OTel traces can be redirected by managed settings.** A managed `OTEL_EXPORTER_OTLP_ENDPOINT` makes Claude Code drop developer-set per-signal endpoints, so `otel_receiver.py` may receive nothing and spans may go to the managed collector. Never edit managed settings to get around it. The receiver binds 127.0.0.1 only.
- **Transcripts are private session data.** Don't commit transcripts, CSV output from real sessions, or paths that expose project names. The README's sample result is aggregate-only; keep it that way.
- **Not covered by the root `make test-all` or CI.** The root `test-all` and the Python CI jobs cover `llmbench/` and `modeltiers/` only, so run `make check test` in the utility's directory yourself. `make check-links` does cover the markdown here, so keep links repo-relative and valid.
