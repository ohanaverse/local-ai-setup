---
name: adding-a-provider
description: Steps to add a new model provider to wt (provider table row, live inventory probe, start/stop backend). Use when asked to add support for a new local or cloud model provider to wt.
---

## Adding a new provider

A cloud provider needs step 1 only. A local provider that wt should list,
start and stop needs all of them.

1. **The provider table.** Add a row to `providers` in
   `internal/provider/table.go` — it is the one place that names a provider:
   - `Policy` is the LiteLLM mapping (prefix, api-key rule, `Cloud`, `V1Base`).
     A model of a provider with no policy is never routed (`provider "<id>"
     has no LiteLLM mapping`, and the picker refuses the row as "(not in
     LiteLLM)"). A native provider (`auth.type = "native"`) is not in the
     table: it never routes through LiteLLM.
   - `Seed` is the default `[[providers]]` row `wt model init` and `wt model
     add` write, and `Seed.Command` the binary whose presence on PATH means
     "installed". Leave it nil for a provider wt has no default for; its row
     is then a hand edit of `registry.toml` (guide 02, Step 2).
   - `Family` names the server the id talks to (omlx-6bit shares omlx's), and
     a local family also needs a row in `families` — default origin and model
     directory, the port wt hands the server (`WtPort`, 0 if the server owns
     it), the `RunningOnly` / `ServedOnly` / `RoutesFollowArtifact` / `Served`
     flags (below), and the
     `Tenancy`: `Exclusive` (one model per process, a start replaces the
     occupant), `Shared` (models load on request) or `Pool` (models load side
     by side under a memory ceiling); `NoTenancy` when wt cannot start it.
   `go test ./internal/provider` checks the table agrees with itself (a
   family member without a policy fails there).

   **Served-only families.** `ServedOnly` means the family's probe is
   `GET /v1/models` alone and wt lists no artifacts for it: only registered
   models appear, and running is what the server says it serves. Add
   `RunningOnly` too only when wt cannot start the family (mlx_lm_server: a
   stopped pairing has no row and a pin is blocked). `ServedOnly` without
   `RunningOnly` (rec) is a family wt starts: its stopped registered model is
   a start row. `probeFamily` selects the served-only probe by the flag, not
   by name, so a new served-only family needs no inventory code beyond its
   `isRunning` rule.
2. **Inventory (local only).** `internal/localmodels` decides what is on disk
   and what is running. Add the family's probe in `probeFamily`
   (`inventory.go`) and its matching rules (`matchArtifact`, `isRunning`,
   `loadedSize`), and `sources.go` for what the server says it is serving.
   Without this the provider's registry models are rows wt cannot probe: they
   are listed and never startable.
3. **Lifecycle (local only).** A server wt runs as a process (`<bin> serve
   ...` per model on one port) needs no backend code: add a `Process`
   (`*provider.ProcessSpec`) to the family's row in `internal/provider/table.go`
   — `Binary`, `Spawn`/`Stop` argv templates (`{model}`, `{port}`), pidfile and
   log defaults (`PidfileEnv` for a scratch override), how its running argv is
   recognised (`Subcommand`, `DaemonModule`, `PortFlag`, `ModelFlag`), a `Ready`
   check (`ReadyModelsList`, or `ReadyCommandJSON` with `Command`, `Path`,
   `Equals`, `ModelPath`, `EndpointPath`) and the family `Scope` (`ScopePort`,
   or `ScopeMachine` for one instance on the whole machine, which needs a
   `ReadyCommandJSON` check; give it an `EndpointPath` so an instance is
   adopted only at the registry's address) — plus the provider's `Seed` and
   `Policy` (`SecretRef: true` when the server needs its key on every
   request, as rec does). mtplx (`ScopePort`, one model per spawn) and rec
   (`ScopeMachine`, one fixed model: `Spawn` without `{model}`) are the model
   rows. `lifecycle` registers `processServer` for every
   `provider.ProcessFamilies()` family on its own. Until the registry overlay
   lands in a later change, such a provider is a data row in `table.go`, not a
   registry entry. Any other kind of server (ollama, omlx style) still needs a
   backend in `backendsByFamily` (`internal/lifecycle/lifecycle.go`) with a
   `tenancy()` equal to the family's row (`TestBackendsMatchProviderTable`).
4. **Tests and docs.** Run `go test ./...` from `wt/` and extend the tests
   that fail. Add the provider to `docs/internals/local-models.md` and, for the
   user, to `../docs/guides/02-providers-and-models.md`.

For the benchmarks (`llmbench provider isolate <id>`), a local provider also
needs an llmbench backend: see the `adding-a-benchmark-backend` skill at the
repo root.
