# local-ai-setup

Entry point for running local and routed LLMs on macOS. One monorepo, three
components — fresh install starts at
[docs/guides/01-initial-setup.md](docs/guides/01-initial-setup.md).

| Component | Role |
|---|---|
| Root (`bin/`, `benchmarks/`, `docs/`) | backends (LiteLLM proxy, Ollama, llama.cpp, oMLX) + LaunchAgents + benchmarks + user guides |
| `modelman/` | model registry TUI/CLI — canonical source of truth for providers/models, exposure, benchmarks, usage |
| `wt/` | worktree agent launcher with model rotation (`wt` binary + `*-wt` shims) |

## User guides (docs/guides/)

The how-to lives in the guides now; this README is just the index. Read
[00-config-map.md](docs/guides/00-config-map.md) first for config-file ownership.

| Guide | Use when |
|---|---|
| [`00-config-map.md`](docs/guides/00-config-map.md) | which tool owns, writes, reads each config file |
| [`01-initial-setup.md`](docs/guides/01-initial-setup.md) | fresh-machine install; smoke-test the stack |
| [`02-providers-and-models.md`](docs/guides/02-providers-and-models.md) | register, download, expose providers and models |
| [`03-model-families.md`](docs/guides/03-model-families.md) | families, tags, and wt model rotation |
| [`04-litellm-config.md`](docs/guides/04-litellm-config.md) | audit proxy config, exposure, admin UI |
| [`05-benchmarks.md`](docs/guides/05-benchmarks.md) | safe isolated modelman benchmark runs |
| [`06-wt-agents-and-models.md`](docs/guides/06-wt-agents-and-models.md) | pick worktree/agent/model, then launch |
| [`07-usage-and-spend.md`](docs/guides/07-usage-and-spend.md) | reconcile wt launches vs LiteLLM spend |
| [`08-maintenance-and-troubleshooting.md`](docs/guides/08-maintenance-and-troubleshooting.md) | health checks, restarts, log triage, upgrades |

## 60-second health check

From [08-maintenance-and-troubleshooting.md](docs/guides/08-maintenance-and-troubleshooting.md)
TL;DR — all read-only, safe to run any time:

```bash
curl -s -m 2 http://localhost:4000/v1/models -o /dev/null -w "4000(litellm):%{http_code}\n"   # 401 = proxy up, demanding key
curl -s -m 2 http://localhost:8080/health -o /dev/null -w "8080(llama.cpp):%{http_code}\n"
curl -s -m 2 http://localhost:8000/health -o /dev/null -w "8000(omlx):%{http_code}\n"         # /health — plain / gives 404
curl -s -m 2 http://localhost:11434/api/tags -o /dev/null -w "11434(ollama):%{http_code}\n"
launchctl list | grep -E 'litellm|llamacpp|omlx|postgresql|redis|ollama'
pg_isready -h localhost
redis-cli ping
```

Expected: `401` on :4000 = proxy up (correctly demanding a key); `200` on the
other three. `launchctl list` columns are PID / last-exit-status / label —
`0` or `-15` in the middle column is healthy.

Any line that differs → [08-maintenance-and-troubleshooting.md](docs/guides/08-maintenance-and-troubleshooting.md)
§1 (restarts) / §3 (logs).

## Reference docs (docs/reference/)

- [litellm-admin-ui-setup.md](docs/reference/litellm-admin-ui-setup.md) — admin dashboard install
- [LiteLLM Proxy on macOS_ Unifying Ollama, llama_cpp, and OpenRouter](docs/reference/LiteLLM%20Proxy%20on%20macOS_%20Unifying%20Ollama%2C%20llama_cpp%2C%20and%20OpenRouter.md)
- [Adding MLX (Apple Silicon) as a Fourth Backend to Your LiteLLM macOS Proxy](docs/reference/Adding%20MLX%20%28Apple%20Silicon%29%20as%20a%20Fourth%20Backend%20to%20Your%20LiteLLM%20macOS%20Proxy.md)
- [oMLX Download and Run](docs/reference/oMLX%20Download%20and%20Run.md)
- [Downloading and Managing Hugging Face Models on macOS for Local LLM Inference (2026)](docs/reference/Downloading%20and%20Managing%20Hugging%20Face%20Models%20on%20macOS%20for%20Local%20LLM%20Inference%20%282026%29.md)

## Legacy

One-off benchmark write-ups (legacy ad hoc scripts): [ornith-1.5](benchmarks/ornith-1.5-benchmark.md),
[qwen3.8](benchmarks/qwen3.8-benchmark.md). Archived full-setup doc, superseded by the guides:
[Local AI Setup 2026-08-25.md](docs/archive/Local%20AI%20Setup%202026-08-25.md).

## Repo layout

```
.
├── bin/                # llm-isolate-provider, llm-restore-providers (benchmark isolation)
├── benchmarks/         # legacy benchmark scripts + write-ups
│   └── results/        # benchmark output artifacts
├── docs/
│   ├── guides/         # user playbooks — index above
│   ├── reference/      # backend-specific guides
│   ├── contracts/      # cross-language config-format fixtures (read by wt Go + modelman Python tests)
│   ├── archive/        # superseded docs
│   └── superpowers/    # plans + specs
├── modelman/           # model registry TUI/CLI (Python/uv) — has its own CLAUDE.md
├── wt/                 # worktree agent launcher (Go) — has its own CLAUDE.md
├── .github/workflows/ # CI: shell-ci, wt-ci, modelman-ci
├── CLAUDE.md           # agent entry point
├── Makefile            # make lint (shellcheck), make check-links, make test-all
└── README.md           # this file — index only
```

> LiteLLM proxy config and the five LaunchAgent plists live outside the repo
> (`~/.config/litellm/config.yaml`, `~/Library/LaunchAgents/*.plist`) — who
> owns what: [00-config-map.md](docs/guides/00-config-map.md).

## Status

All three model-management consolidation sub-projects are merged (shared model
registry, benchmark tooling, usage/spend tracking). The cross-repo drift signal
is now automatic: the shared-format fixtures in
[docs/contracts/](docs/contracts) are read by contract tests on both sides
(`wt` Go tests, `modelman` Python tests), replacing the hand-maintained
cross-repo tracker doc.

Follow-up items from the guide-set review: [issues.md](issues.md).
