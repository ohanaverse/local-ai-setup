# agent-utilities

Small standalone utilities for working with specific coding agents, one directory per agent. Run by hand; nothing here is wired into `wt`, `llmbench` or CI.

| Directory | Agent | Utilities |
|-----------|-------|-----------|
| [`claude-utilities/`](claude-utilities/README.md) | Claude Code | `transcript_speed.py` — estimate output tokens/second from local session transcripts |

## Conventions

- Read-only: utilities read an agent's on-disk records and never modify them.
- Stdlib-only Python, run with `python3 -I <script>`; no virtualenv or install step.
- Output derived from client-side data is an estimate; each README says which numbers to trust.

See [`CLAUDE.md`](CLAUDE.md) for contributor notes and gotchas.
