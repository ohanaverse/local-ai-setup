# dsh-tui-wt

## Overview

`dsh-tui-wt` is the worktree launcher for the [DeepSeek Harness](https://github.com/deepseek-ai/deepseek-harness) (`dsh`) **terminal TUI** — an interactive terminal app, installed via the optional turtle-ui profile plugin. It launches `ollama launch dsh -- --profile tui`.

dsh is an open-source coding agent whose single binary ships three run modes (browser, terminal TUI, headless). wt exposes each mode as a separate agent: `dsh-webui` (browser), `dsh-tui` (terminal, this file), and `dsh-headless` (one-shot CLI). dsh is **ollama-only** — there is no native provider.

## Installation

The terminal TUI requires the turtle-ui profile plugin, which is not shipped by default:

```bash
dsh plugin --profile tui add github:deepseek-ai/turtle-ui
```

`dsh-tui-wt` is a one-line shim that forwards to the `wt` binary (`exec wt --agent dsh-tui "$@"`). Build `wt` and put it on `$PATH` (see the repo's top-level `CLAUDE.md`), then put `bin/dsh-tui-wt` on `$PATH` too, e.g. via `make install`.

## Usage

```bash
# Launch the terminal TUI in a chosen worktree
dsh-tui-wt

# Use/create a named worktree
dsh-tui-wt -W my-feature

# Forward a dsh flag after --
dsh-tui-wt -- --foo
```

## Model selection

`dsh-tui` is a **command** (no model layer): its run command is fixed by the `--profile tui` selector, so wt skips the model picker and rotation. The model is reused from dsh's stored settings (`~/.ollama/launch/dsh/settings.yaml`), configured once via `ollama launch dsh --config`.

## Web search

Web search requires Ollama cloud access (`ollama signin`) and a model that supports tools. It is **off by default**; the exact opt-in flag is pending dsh documentation.

## Configuration

The `dsh-tui` agent is pinned to the ollama provider in `~/.config/agent-wt/config.toml`:

```toml
[[agents]]
name = "dsh-tui"
supported_providers = ["ollama"]
default_provider = "ollama"
```

## Verified on this machine

**Convention only — not verified on this machine.**
