---
name: adding-a-provider
description: Steps to add a new model provider to wt (LiteLLM policy, default registry row, live inventory probe, start/stop backend). Use when asked to add support for a new local or cloud model provider to wt.
---

## Adding a new provider

A cloud provider needs steps 1 and 2 only. A local provider that wt should
list, start and stop needs all of them.

1. **LiteLLM mapping.** Add the provider to `policies` in
   `internal/litellm/policy.go` (prefix, api-key rule, `Cloud`, `V1Base`).
   That table is the single source of truth for routing: a model of an
   unmapped provider is never routed (`provider "<id>" has no LiteLLM
   mapping`, and the picker refuses the row as "(not in LiteLLM)"). A native
   provider (`auth.type = "native"`) gets no entry: it never routes through
   LiteLLM.
2. **Registry row.** A model names its provider with `provider_id`, and the
   registry needs a `[[providers]]` row with that id. For a provider wt has
   no default for, the row is a hand edit of `registry.toml` (guide 02,
   Step 2). To have `wt model init` and `wt model add` write the row
   themselves, add the id to `defaultProviderIDs` and a row to
   `defaultProviderRow` in `internal/config/registry_seed.go`, and, if the
   provider has a command whose presence on PATH means "installed", an entry
   in `installedProviderCommands`.
3. **Inventory (local only).** `internal/localmodels` decides what is on disk
   and what is running. Map the provider id to a probe family in `familyIDs`
   (`inventory.go`; `Family` reads it) and add the family's source in
   `sources.go` (what is on disk, what the server says it is serving). Without this the provider's
   registry models are rows wt cannot probe: they are listed and never
   startable.
4. **Lifecycle (local only).** Add a backend to `backendsByFamily` in
   `internal/lifecycle/lifecycle.go` and give it a `tenancy()` (the kinds
   are in `tenancy.go`): `Exclusive` (one model per process, a start replaces the occupant),
   `Shared` (models load on request) or `Pool` (models load side by side
   under a memory ceiling). If the server can say what it is serving, add the
   family to `servedFamilies` in `cmd/wt/model_cmds.go` so `wt served`
   answers for it.
5. **Tests and docs.** Each of the tables above has a test that enumerates
   it; run `go test ./...` from `wt/` and extend the ones that fail. Add the
   provider to `docs/internals/local-models.md` and, for the user, to
   `../docs/guides/02-providers-and-models.md`.

For the benchmarks (`llmbench provider isolate <id>`), a local provider also
needs an llmbench backend: see the `adding-a-benchmark-backend` skill at the
repo root.
