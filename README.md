# local-ai-setup

Entry point for running local and routed LLMs on macOS.

This repo is the orchestration layer. It contains the LiteLLM proxy
configuration, provider setup scripts, and the high-level glue between
three related tools:

| Repo | Role | You use it to |
|---|---|---|
| `local-ai-setup` (this repo) | Install and run the local AI infrastructure | Configure LiteLLM, Ollama, llama.cpp, oMLX, and OpenRouter backends |
| `modelman` | Manage the shared model registry | Add/edit models, expose/unexpose them to LiteLLM, run benchmarks, print usage reports |
| `agent-worktree` (`wt`) | Launch AI coding agents in git worktrees | Pick a worktree, rotate models, and start Claude, Codex, Copilot, OpenCode, pi, agy, or a shell |

---

## Quick overview

The stack works like this:

1. `local-ai-setup` gets the backends running and the LiteLLM proxy up.
2. `modelman` becomes the single source of truth for which models exist on
   which providers and whether they are exposed through LiteLLM.
3. `wt` reads that registry, rotates through models, and launches an agent
   pointing at the right provider/LiteLLM endpoint.

---

## Install

Start with the main setup guide:

- [`docs/Local AI Setup 2026-08-25.md`](docs/archive/Local%20AI%20Setup%202026-08-25.md)

It covers:

- Installing the LiteLLM proxy (`uv tool install 'litellm[proxy]'`)
- Ollama
- llama.cpp
- oMLX (Apple Silicon)
- OpenRouter
- PostgreSQL + Redis + the LiteLLM LaunchAgent

For the Admin UI / web dashboard:

- [`docs/litellm-admin-ui-setup.md`](docs/litellm-admin-ui-setup.md)

---

## Configure models with `modelman`

After the proxy is running, use `modelman` to register, download, and expose
models.

```bash
# Run the interactive TUI
uv run --package modelman modelman

# Or, from a local clone of the modelman repo:
uv run modelman
```

`modelman` reads and writes:

| File | Purpose |
|---|---|
| `~/.config/local-ai/registry.toml` | Canonical providers and models (shared, read-only by `wt`) |
| `~/.config/local-ai/modelman.toml` | Per-machine mutable state: downloads, LiteLLM exposure flags |
| `~/.config/local-ai/settings.yaml` | User preferences |

Key workflows:

- Add or edit providers/models in the TUI.
- Press `l` on a model to expose/unexpose it to LiteLLM (writes `model_list`
  entries in `~/.config/litellm/config.yaml`).
- Run `modelman sync` to reconcile ollama/llama.cpp/oMLX state with the
  registry.

See `modelman/README.md` and `modelman/docs/ROADMAP.md` for the full tool
reference.

---

## Launch an agent with `wt`

`wt` is the worktree-aware launcher in `agent-worktree`.

```bash
# Build and install the Go binary
cd ../agent-worktree
go build -o "$(go env GOPATH)/bin/wt" ./cmd/wt

# Or use the shims from this repo once wt is on $PATH:
#   claude-wt, codex-wt, copilot-wt, opencode-wt, pi-wt, agy-wt, shell-wt

# Launch the TUI to pick a worktree and model
claude-wt

# Skip the worktree picker
claude-wt -W my-feature

# Skip both worktree and model picker
claude-wt -W my-feature -A claude -M ollama/qwen3.8:27b-mlx
```

`wt` reads `~/.config/local-ai/registry.toml` read-only and shows models by
tag/family. It records launches in `~/.config/agent-wt/usage.jsonl` and
`~/.config/agent-wt/rotation.state`.

See `agent-worktree/README.md` for all flags and TUI behavior.

---

## Benchmark models

After exposing models to LiteLLM, run benchmarks:

```bash
modelman benchmark run --family qwen3.8
modelman benchmark run --model ollama/ornith-1.5:35b
```

Results are written as JSON + Markdown artifacts on disk; only a latest-run
pointer lives in `modelman.toml`.

Design: `modelman/docs/superpowers/specs/2026-09-05-modelman-benchmark-design.md`
Plan: `modelman/docs/superpowers/plans/2026-09-05-modelman-benchmark.md`

---

## Track usage and spend

`modelman` can reconcile `wt` launches with LiteLLM's Postgres spend logs:

```bash
modelman usage report --days 7
```

Output is a Markdown report with:

- Per-model WT launch counts (1d/7d/30d)
- LiteLLM request counts, prompt/completion tokens, and spend
- Reconciliation sections: matched, WT-only launches, LiteLLM-only spend
- Last launched model from `rotation.state`

Design: `modelman/docs/superpowers/specs/2026-08-28-modelman-usage-design.md`
Plan: `modelman/docs/superpowers/plans/2026-08-28-modelman-usage.md`

---

## Reference docs

Backend-specific guides:

- [LiteLLM Proxy on macOS: Unifying Ollama, llama.cpp, and OpenRouter](docs/reference/LiteLLM%20Proxy%20on%20macOS_%20Unifying%20Ollama%2C%20llama_cpp%2C%20and%20OpenRouter.md)
- [Adding MLX (Apple Silicon) as a Fourth Backend to Your LiteLLM macOS Proxy](docs/reference/Adding%20MLX%20(Apple%20Silicon)%20as%20a%20Fourth%20Backend%20to%20Your%20LiteLLM%20macOS%20Proxy.md)
- [oMLX Download and Run](docs/reference/oMLX%20Download%20and%20Run.md)
- [Downloading and Managing Hugging Face Models on macOS for Local LLM Inference (2026)](docs/reference/Downloading%20and%20Managing%20Hugging%20Face%20Models%20on%20macOS%20for%20Local%20LLM%20Inference%20(2026).md)

One-off benchmark write-ups (legacy ad hoc scripts):

- [Ornith-1.5 Benchmark](benchmarks/ornith-1.5-benchmark.md)
- [Qwen3.8 OpenRouter Benchmark](benchmarks/qwen3.8-benchmark.md)

---

## Repo layout

```
.
├── bin/
│   ├── llm-isolate-provider      # isolate one provider for benchmarking
│   └── llm-restore-providers     # restore providers after isolation
├── benchmarks/
│   ├── ornith-1.5-benchmark      # legacy bash benchmark script
│   ├── qwen3.8-benchmark          # legacy bash benchmark script
│   └── results/                   # benchmark outputs
├── docs/
│   ├── Local AI Setup 2026-08-25.md
│   ├── litellm-admin-ui-setup.md
│   └── reference/
└── README.md                       # this file
```

> Note: LaunchAgent plists and the LiteLLM config live outside the repo at
> `~/Library/LaunchAgents/*.plist` and `~/.config/litellm/config.yaml`. The
> setup guide in `docs/` walks through creating them.

---

## Status

Cross-repo tracker:
`agent-worktree/docs/superpowers/plans/2026-08-27-model-management-consolidation-status.md`

All three sub-projects of the model-management consolidation are merged:

1. Shared model registry (`modelman` registry owner, `wt` read-only consumer)
2. Benchmark tooling (`modelman benchmark` + isolation helpers here)
3. Usage/spend tracking (`modelman usage report`)
