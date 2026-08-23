# dsh-headless-wt

## Overview

`dsh-headless-wt` is the worktree launcher for the [DeepSeek Harness](https://github.com/deepseek-ai/deepseek-harness) (`dsh`) **headless** run mode — a non-interactive one-shot CLI. It launches `ollama launch dsh -- --profile headless`, takes a task string as its positional argument, prints the final answer to stdout, and exits.

dsh is an open-source coding agent whose single binary ships three run modes (browser, terminal TUI, headless). wt exposes each mode as a separate agent: `dsh-webui` (browser), `dsh-tui` (terminal), and `dsh-headless` (one-shot CLI, this file). dsh is **ollama-only** — there is no native provider.

## Installation

dsh is launched through Ollama, not as a standalone binary:

```bash
ollama launch dsh --config   # configure dsh (model, web search) without launching
```

`dsh-headless-wt` is a one-line shim that forwards to the `wt` binary (`exec wt --agent dsh-headless "$@"`). Build `wt` and put it on `$PATH` (see the repo's top-level `CLAUDE.md`), then put `bin/dsh-headless-wt` on `$PATH` too, e.g. via `make install`.

## Usage

```bash
# Run a one-shot task in a chosen worktree; dsh prints the answer and exits
dsh-headless-wt -- "fix the failing test in internal/agents"

# Use/create a named worktree
dsh-headless-wt -W my-feature -- "explain this repo's launch flow"
```

The task string is forwarded to dsh after the `--` separator, so it is treated as dsh's positional argument rather than an `ollama launch` flag.

## Model selection

`dsh-headless` is a **command** (no model layer): its run command is fixed by the `--profile headless` selector, so wt skips the model picker and rotation. The model is reused from dsh's stored settings (`~/.ollama/launch/dsh/settings.yaml`), configured once via `ollama launch dsh --config`.

## Web search

Web search requires Ollama cloud access (`ollama signin`) and a model that supports tools. It is **off by default**; the exact opt-in flag is pending dsh documentation.

## Configuration

The `dsh-headless` agent is pinned to the ollama provider in `~/.config/agent-wt/config.toml`:

```toml
[[agents]]
name = "dsh-headless"
supported_providers = ["ollama"]
default_provider = "ollama"
```

## Verified on this machine

**Convention only — not verified on this machine.**
