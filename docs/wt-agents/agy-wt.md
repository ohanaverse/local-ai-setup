# agy-wt

## Overview

`agy-wt` is the worktree launcher for [Antigravity CLI](https://antigravity.google/cli) (`agy`), Google's AI coding agent CLI. See [README](README.md) for launcher flags and model rotation.

## Installation

```bash
curl -fsSL https://antigravity.google/cli/install.sh | bash
```

This installs the `agy` binary. The `agy-wt` file in this repo is a shim that forwards to `wt --agent agy`. Install `agy-wt` itself by copying `bin/agy-wt` into `~/.local/bin/`.

## Configuration files & locations

Antigravity CLI reads from `~/.antigravity/` (per upstream docs). See the [Antigravity CLI documentation](https://antigravity.google/cli) for the full layout.

## Authentication & credentials

Antigravity CLI authenticates against a Google account; see the [Antigravity CLI docs](https://antigravity.google/cli) for the supported flow.

## Model selection

`agy-wt` does **not** pass a `--model` flag through to `agy`. Model selection happens inside `agy`'s TUI via `/model`.

`agy` is restricted to the `google` provider. The launcher only considers `google` models when building the eligible list, and it skips the model picker when exactly one eligible model exists.

## Agent init

`agy-wt --init` seeds project-level instruction files:

- `AGENTS.md` — shared instruction template (if missing)

Agy has no project-level instruction file convention, so no pointer file is created.

## Verified on this machine

**Not installed on this machine.** Statements above are sourced from the upstream Antigravity CLI install page. Re-verify before relying on details for the home directory layout or auth flow.
