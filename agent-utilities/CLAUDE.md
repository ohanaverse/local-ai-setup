# CLAUDE.md

Standalone utilities for working with specific coding agents, one subdirectory per agent (`claude-utilities/` so far). Run by hand; nothing here is wired into `wt`, `llmbench`, the Makefile or CI.

## Commands
- `python3 -I claude-utilities/transcript_speed.py [--min-tokens N] [--csv] ~/.claude/projects` — summary per model, or per-call CSV rows

## Layout
- `claude-utilities/` — Claude Code utilities. `transcript_speed.py` estimates output tokens/second per API call from the session transcripts in `~/.claude/projects/*/*.jsonl`. Usage, column definitions, caveats and a dated sample result live in `claude-utilities/README.md`.

## Conventions
- **Read-only against agent state.** A utility reads the agent's own on-disk records (transcripts, logs) and never writes to them, to `registry.toml`, or to any `wt`/LiteLLM config.
- **Stdlib-only Python, no setup.** Single-file scripts run with `python3 -I <script>` (no venv, no `uv`, no dependencies). If a utility needs a third-party package, it probably belongs in its own uv package like `llmbench/` or `modeltiers/`, not here.
- **One directory per agent**, named `<agent>-utilities` (`claude-utilities/`, later e.g. `codex-utilities/`, `pi-utilities/`). A new directory gets its own README for usage; add a line to Layout above.
- **Estimates are labeled as estimates.** These scripts derive numbers from client-side timestamps and counters, so say which columns are lower bounds or unreliable (see the README's column table) rather than presenting them as measurements.

## Gotchas
- **Transcript format is Claude Code's, not a stable API.** `transcript_speed.py` relies on `api-request` records, one `assistant` line per content block, and `message.usage.output_tokens` being the final total repeated on every block. A Claude Code update can change this silently; if the call count drops to zero or looks wrong, check the record shapes first.
- **Transcripts are private session data.** Don't commit transcripts, CSV output from real sessions, or paths that expose project names. The README's sample result is aggregate-only; keep it that way.
- **Not covered by `make lint` or `make test-all`.** `lint-shell` only checks the Makefile's `SHELL_SCRIPTS`, and the Python CI jobs cover `llmbench/` and `modeltiers/` only. Check a script by running it against a real or hand-made `.jsonl` file. `make check-links` does cover the markdown here, so keep links repo-relative and valid.
