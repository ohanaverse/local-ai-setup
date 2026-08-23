# dsh-webui-wt

## Overview

`dsh-webui-wt` is the worktree launcher for the [DeepSeek Harness](https://github.com/deepseek-ai/deepseek-harness) (`dsh`) **browser TUI** — dsh's default run mode. It launches `ollama launch dsh --model <name>`, which opens the terminal-on-the-web UI in the user's browser at `http://127.0.0.1:3080`.

dsh is an open-source coding agent whose single binary ships three run modes (browser, terminal TUI, headless). wt exposes each mode as a separate agent: `dsh-webui` (browser, this file), `dsh-tui` (terminal), and `dsh-headless` (one-shot CLI). dsh is **ollama-only** — there is no native provider, just as the local `pi` agent is.

## Installation

dsh is launched through Ollama, not as a standalone binary:

```bash
ollama launch dsh --config   # configure dsh (model, web search) without launching
```

`dsh-webui-wt` is a one-line shim that forwards to the `wt` binary (`exec wt --agent dsh-webui "$@"`). Build `wt` and put it on `$PATH` (see the repo's top-level `CLAUDE.md`), then put `bin/dsh-webui-wt` on `$PATH` too, e.g. via `make install`.

## Usage

```bash
# Launch the browser TUI in a chosen worktree (model picker shown)
dsh-webui-wt

# Pin the model, skip the model picker
dsh-webui-wt -M ollama/deepseek-v4-pro:cloud

# Use/create a named worktree
dsh-webui-wt -W my-feature

# Forward a dsh flag (e.g. a non-default port) after --
dsh-webui-wt -- --port 3081
```

## Model selection

`dsh-webui` is model-driven: wt pins the model in the launch command via `--model <name>`, so it goes through the model picker like any other agent. The bare provider name (`m.ModelName`, e.g. `deepseek-v4-pro:cloud`) is passed — never the registry key (`m.ID`), which would double-prefix `ollama/`.

## Web search

Web search requires Ollama cloud access (`ollama signin`) and a model that supports tools. It is **off by default**; the exact opt-in flag is pending dsh documentation.

## Configuration

dsh stores its managed settings in `~/.ollama/launch/dsh/settings.yaml`. The `dsh-webui` agent is pinned to the ollama provider in `~/.config/agent-wt/config.toml`:

```toml
[[agents]]
name = "dsh-webui"
supported_providers = ["ollama"]
default_provider = "ollama"
```

## Verified on this machine

**Convention only — not verified on this machine.**
