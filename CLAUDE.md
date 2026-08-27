# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project overview

`modelman` is a small Python 3.13 Textual TUI for managing local LLM model families across multiple providers (Ollama, llama.cpp, oMLX). A *family* is a YAML manifest that groups related model variants; the TUI lets you browse families, drill into per-provider model lists, and queue changes (add/edit/delete/download) that are applied on exit. `download` is a Typer subcommand that opens the TUI at a family's model screen.

## Common development commands

The project uses `uv` for packaging and dependency management. Python 3.13 is required (`requires-python = "==3.13.*"`).

- **Install dependencies:** `make install` (runs `uv sync`)
- **Run the CLI during development:** `uv run modelman` (TUI) or `uv run modelman download <family>`
- **Run all tests:** `make test`
- **Run a single test:** `uv run pytest tests/path/to/test.py::test_name`
- **Lint / format / typecheck:** `make lint`, `make format`, `make typecheck` (or `make check` to run lint+typecheck together)
- **Build a wheel:** `uv build`

The Makefile wraps the standard dev commands. Run `make help` to list targets.

## Architecture

### Entry point

- `src/modelman/main.py` defines the Typer `app`. A single `@app.callback(invoke_without_command=True)` opens the TUI when no subcommand is given; the `download <family>` subcommand calls `run_tui(family)`, which launches the Textual app and pushes a `ModelScreen` for that family.

### Textual TUI

- `src/modelman/app.py` — `ModelmanApp(App[None])`. `on_mount` pushes `FamilyScreen`.
- `src/modelman/screens/families.py` — `FamilyScreen`: DataTable of families (family · display · variants · downloaded · size). Actions: `action_add_family` (`a`), `action_delete_family` (`d`, blocked if any downloaded models), `action_open_family` (Enter / `DataTable.RowSelected`).
- `src/modelman/screens/models.py` — `ModelScreen`: two-pane layout (providers DataTable left, models DataTable right with name · status · size · path). Holds `queued_downloads` / `queued_deletes` dicts. Actions: `a` add model, `e` edit (id/provider fixed), `d` queue delete, `x` toggle download, `escape` back/apply. Provider selection drives a `RowHighlighted` handler that reloads the right pane.
- `src/modelman/screens/forms.py` — modal screens: `AddFamilyModal`, `ConfirmModal` (y/n with keybindings), `ModelForm` (add/edit with `disabled=editing` on provider/id for immutability), `ConfirmExitDialog` (shows pending set, applies on confirm).

### Pending changes queue

- `src/modelman/queue.py` — `PendingChanges(manifest, manifest_path, providers, downloads, deletes, failures)`. `apply()` runs deletes first (so downloads free up disk), then downloads (each calls `provider.download(variant)` → `manifest.mark_downloaded(...)`), then a single `save_manifest()`. Failures are captured per-step, processing continues.

### Provider plugin system

- `src/modelman/providers/base.py` defines the `Provider` abstract base class and the `VariantSpec` / `LocalModel` TypedDicts. `VariantSpec` is `total=False` with a freeform `model_info: dict[str, Any] | None` for LiteLLM-style capability keys. `Provider` requires `name`, `is_downloaded`, `download`, `list_local`, and `size_of(variant) -> int | None` (default returns `None`).
- `src/modelman/providers/registry.py` — `ProviderRegistry.register(cls)` / `.get(name, config)`.
- Each provider module (`ollama.py`, `llamacpp.py`, `omlx.py`) calls `ProviderRegistry.register(ItsProvider)` at import time.
- `src/modelman/providers/__init__.py` imports every provider module solely to trigger registration. Code that needs providers should import from `modelman.providers` rather than a single submodule.

### Ollama capability detection

- `src/modelman/ollama_caps.py` — `parse_ollama_show(stdout)` translates `Capabilities` section entries into `model_info` (e.g. `tools` → `supports_function_calling: True`). `auto_detect_model_info(name, runner)` runs `ollama show <name>` and returns the parsed dict (or `{}` on failure). `ModelForm` calls this on add for ollama variants.

### Config and manifests

- `src/modelman/config.py` loads `~/.config/local-ai/config.yaml` into a `Config` dataclass. Path overridable with `MODELMAN_CONFIG`.
- `src/modelman/manifest.py` loads/saves family manifests from `~/.config/local-ai/families/<family>.yaml` as `FamilyManifest`. Directory overridable with `MODELMAN_FAMILY_DIR`. `FamilyManifest.variant_by_id(vid)` looks up a variant. `downloaded` is a dict keyed by variant id; `mark_downloaded` stores an ISO timestamp and local path. `save_manifest` rewrites the whole YAML file with `sort_keys=False`. `load_manifest` derives a variant `name` from `files[0]` or the repo basename if not provided.

### Adding a new provider

1. Create `src/modelman/providers/<name>.py` with a class extending `Provider`.
2. Call `ProviderRegistry.register(TheProvider)` at the bottom of the module.
3. Add the provider to `~/.config/local-ai/config.yaml` under `providers:`.
4. Use `provider: <name>` in family manifests.
5. (Optional) Override `size_of` so the size column is populated for downloaded variants.

No changes to `main.py` are required unless a new CLI subcommand is also added.

## Testing patterns

- TUI tests use `pytest-asyncio` (`asyncio_mode = "auto"` in pyproject) and `ModelmanApp.run_test()` with a `pilot`. Drive interactions with `await pilot.press("a")` etc. and assert on `app.screen` / `query_one(DataTable)`. Modal text-input flows explicitly focus the target `Input` before pressing characters (Tab cycling is unreliable in tests).
- Provider unit tests inject a `runner` callable (commonly the `mock_runner` fixture in `tests/conftest.py`) so subprocess behavior can be mocked without shelling out.
- CLI entry-point tests use `typer.testing.CliRunner` and `unittest.mock.patch("modelman.main.run_tui")` to assert the TUI is invoked with the right argument.
- Tests redirect config/manifest paths via `MODELMAN_CONFIG` and `MODELMAN_FAMILY_DIR`.

## Important implementation notes

- The `OllamaProvider` methods accept an optional `runner` argument so tests can substitute a mock. The default runner just calls `subprocess.run`.
- `FamilyManifest.mark_downloaded` stores an ISO timestamp and local path; `save_manifest` rewrites the whole YAML file with `sort_keys=False`.
- `load_manifest` derives a variant `name` from `files[0]` or the repo basename if `name` is not explicitly provided.
- `llamacpp` checks the Hugging Face cache (`HF_HOME/hub`) for the requested files; `omlx` downloads into `model_dir/<repo-basename>`.
- `size_of` is implemented per provider: Ollama parses the `ollama list` SIZE column (two-token `<number> <UNIT>`), llamacpp stats the primary file in the HF cache snapshot dir, omlx sums files in the model directory.
- `PendingChanges.apply()` is the single integration point between the TUI queue and the providers. It deliberately reorders to deletes-before-downloads so a queued delete frees disk before a queued download fills it.
- The TUI's `action_back` (`escape` from ModelScreen) shows the exit confirmation only if the queue is non-empty; otherwise it pops immediately.

## Configuration for end users

See `README.md` for the exact YAML schemas for `~/.config/local-ai/config.yaml`, family manifests, and the variant field reference.

## Foreign agent configs

`~/.codex/` and `~/.gemini/` exist on this machine. To import user-level instructions, MCP servers, slash commands, subagents, or skills from those configs, run `/import` in Claude Code (or `claude import` from a terminal) to scan what's available, then `/import --yes=<digest>` to apply the items you want.