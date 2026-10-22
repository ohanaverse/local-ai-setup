# modeltiers

Advisory capability tiers for the models wt lists. `modeltiers` reads the inventory from `wt model list --json`, scores each model against four public sources (models.dev, OpenRouter, Arena, Epoch AI), and writes a tier (`marathon`, `deep`, `standard`, `quick` or `unrated`) with the evidence behind it to `~/.config/local-ai/tiers.toml`. wt's model picker shows it as a TIER column. Nothing routes on a tier.

```bash
uv run --directory modeltiers modeltiers run --dry-run   # fetch, resolve, assign, report; writes no tiers.toml
uv run --directory modeltiers modeltiers run             # the same, then write tiers.toml
uv run --directory modeltiers modeltiers show            # the tier table, from the file
uv run --directory modeltiers modeltiers explain <provider_id/model_name>
```

Needs `wt` on PATH and network access to the four sources; no API key. It never writes `registry.toml`, `config.yaml` or any wt state.

- User guide (what a tier means, running it, calibrating): [docs/guides/12-model-tiers.md](../docs/guides/12-model-tiers.md)
- Commands, exit codes, gotchas and rules for working in this package: [CLAUDE.md](CLAUDE.md)
- Module map, identity and assignment rules, file formats, test patterns: [docs/internals.md](docs/internals.md)
- Design: [2026-10-12-model-tiers-design.md](../docs/superpowers/specs/2026-10-12-model-tiers-design.md)

Development: `make install`, `make check` (ruff, mypy strict), `make test`, `make all`.
