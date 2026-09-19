# wt: local model inventory (discovery + live running-state)

Date: 2026-09-19
Status: approved, pending implementation plan

## Context

Sub-project 2 of 5 in the "make wt's model selector look like modelman's"
effort. Long-term goal: migrate away from modelman, so discovery is ported to
Go rather than shelled out. Sub-project 1 (PR #110) added per-agent usage
counts and `config.DiscoveredModelID`.

| # | Sub-project | Depends on |
|---|---|---|
| 1 | Stats and surveys for any agent-model pair (done, #110) | none |
| 2 | Go discovery and running-state of local models (this spec) | 1 |
| 3 | New selector table (headings, columns, union, sort) | 1, 2 |
| 4 | Go start + single-model-provider replacement warning; run discovered models without writing config | 2 |
| 5 | Launch-time smoke gate (exit non-zero on failure) | 4 |

## Problem

wt only knows local models that are in `registry.toml`, and decides "running"
from modelman's per-model `running` flag plus a live probe
(`internal/localgate`). A model present on disk (or pulled in ollama) but not
registered is invisible, and it has no flag, so its running-state cannot be
derived. wt also never reads a provider's `model_dir`, and hard-codes probe
ports.

## Design

### Package and shape

New package `wt/internal/localmodels`. `Inventory(cfg *config.Config) Snapshot`
does one concurrent probe round (2s timeout per probe) and returns:

| Field | Meaning |
|---|---|
| `Entry.ProviderID`, `Entry.Artifact` | provider and pulled/on-disk name, e.g. `omlx` / `Qwen3.8-27B-4bit` |
| `Entry.ModelID` | registry id when the artifact matches a registry model, else `config.DiscoveredModelID(provider, artifact)` |
| `Entry.Registered` | matched a registry entry |
| `Entry.Running` | serving right now (live probe only) |
| `Snapshot.Providers` | per-provider status: `ok`, `unreachable`, `unsupported` |

A provider failure never fails the snapshot. Registered local models with no
matching artifact still appear (`Registered=true`, `Running=false`, empty
`Artifact`) so the selector can show a configured model missing from disk.

`localgate`'s probe helpers (`nameMatches`, `fetchModelIDs`) move into
`localmodels`; `localgate` calls them from there so there is one copy.
`localgate.Apply` and its modelman-flag policy are unchanged until
sub-projects 3-4 retire them.

### Providers

| Provider | Discovery | Running |
|---|---|---|
| ollama | `GET /api/tags` (pulled models; no subprocess) | `GET /api/ps` (loaded set) |
| omlx | subdirectories of `model_dir` (default `~/.omlx/models`) | name-checked `GET /v1/models` on `:8000`; the probe also serves a hand-added `omlx-6bit` row |
| mtplx | subdirectories of `model_dir` (default `~/.mtplx/models`); dir `Youssofal--X` maps to `Youssofal/X` | name-checked `/v1/models` on `:8003` |
| mlx_lm_server | none (target+draft pairing is not discoverable) | non-empty `/v1/models` on `:8001`, registered rows only |

omlx and omlx-6bit are one physical server (modelman `_OMLX_PROVIDER_IDS`), so
on-disk oMLX directories are attributed to `omlx` only.

The registry parser starts decoding each provider's `model_dir` and
`auth.base_url` (both ignored today); probe URLs derive from `base_url`,
falling back to today's hard-coded ports.

### Matching, running, errors

- **Registry matching** uses the existing lenient-prefix, strict-suffix rule
  (equal, or one is a `/`-suffix of the other; suffix strict so `-4bit` never
  matches `-8bit`). oMLX dir `Qwen3.8-27B-4bit` matches registry name
  `mlx-community/Qwen3.8-27B-4bit`. Ollama matching is exact with a `:latest`
  fallback.
- **Running is purely live.** The snapshot never reads modelman's `running`
  flag, so registered and discovered models behave identically and there is no
  modelman dependency. Consequence: an ollama model started by `modelman start`
  (flag-only, no warmup) reads not-running until first use; sub-project 4's
  start will warm it.
- **Ollama down:** provider status `unreachable`; its registered models still
  list, not running. A missing model directory yields zero artifacts, not an
  error.

### Out of scope

Selector UI (#3), starting/stopping models (#4), smoke gate (#5), removing
`localgate`.

## Testing

`httptest` servers and temp directories via URL/path seams (the pattern
`localgate` uses). Each test carries the required what/why comment. Cases:

- ollama: `/api/tags` + `/api/ps` decode; daemon down -> `unreachable`.
- omlx/mtplx: directory scan; mtplx `--` to `/` mapping; missing dir.
- Registry matching: omlx dir vs full HF name; 4bit vs 8bit never collide;
  ollama `:latest`.
- `ModelID` is the registry id when registered, `DiscoveredModelID` otherwise.
- Registered model absent from disk still appears, not running.
- One provider failing does not affect others; probes run concurrently.
- Registry parser reads `model_dir` and `auth.base_url`.
- `localgate` behavior unchanged after the helper move (existing tests pass).

Update `wt/CLAUDE.md` (package table, "never shells out for discovery" note)
when implemented.
