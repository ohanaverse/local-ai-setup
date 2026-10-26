# Design: one Provider interface in wt, and the rec/flint provider on top of it

Date: 2026-10-25

Adds `rec` (the engine that serves the Flint model) as a wt provider, by first giving wt one
`Provider` interface that every provider implements, with the process-server kind
(mtplx, rec) described by configuration instead of Go code. mtplx moves onto it in the same
project. Cloud providers join the interface for identity and routing only.

## Intent

Today a provider exists in wt as ~5 Go tables that must agree (`familyIDs`,
`discoveredFamily`, the LiteLLM `policies` map, `defaultProviderRow`/`installedProviderCommands`,
`backendsByFamily`) plus ~25-43 `switch provider` sites per layer, and a second Python copy
in `llmbench`. The tables have already drifted once (omlx-6bit had no LiteLLM policy). rec is
the next provider and probably not the last; every new one costs a code change in six places.

The user wants adding or changing a local provider to be a configuration change, and rec
supported properly: `wt start flint` brings it up, `wt stop` takes it down, flint is routed
through LiteLLM while it runs.

## What rec is (verified live 2026-10-25, rec 0.1.0)

- Install: single binary `~/.local/bin/rec`; data under `~/.local/share/rec`
  (`XDG_DATA_HOME` relocates). Model in a hash-named dir under `models/` (~21 GiB, int4
  MLX), so wt cannot scan artifacts by name. Needs Apple Silicon M4 Pro or better, 48 GB+.
- `rec serve [--port N (default 11500)] [--context TOKENS]` runs in the **foreground** and prints
  `flint is ready.`; wt must detach it (as it does mtplx). `rec stop` stops the managed instance
  (exit 0, ~6 ms, process tree gone within ~2 s, also ends a foreground `serve`); with nothing
  running `rec stop` and `rec status` exit 1 ("no managed rec instance is reachable").
  SIGTERM to `rec serve` also cleans up.
- **One instance per machine**: a second `rec serve`, even on another port, is refused
  ("another instance is starting or uses a different profile"). `--port`/`--context` apply only when
  nothing is running.
- **Loading**: the port is closed for ~10-12 s while runtime and weights load. `/health` returns
  200 *before* rec's own self-check finishes. `rec status` (one JSON line, exit 0 even when
  `ready:false`) reports `ready:true` only after the self-check. Readiness = `rec status` `ready:true`.
- **API**: `GET /health`, `GET /v1/models` (one entry, id `flint`), `POST /v1/chat/completions`
  (streaming, tools, logprobs non-stream). Nothing else (`/v1/messages` is 404 on 0.1.0 despite the
  README). **Every route needs a bearer key**, including `/health` and `/v1/models`; without it, 401. Key
  in `~/.local/share/rec/config/api-key`, printed by `rec auth token`.
- The served model id is exactly `flint`; any other `model` gets 400 ("The served model is flint").
  `flint-int4` (the checkpoint name in `rec status`) is rejected.
- Thinking comes back as `message.reasoning` / `delta.reasoning`, **not** `reasoning_content`.
  Tool calls come back in the standard OpenAI shape.
- The process's RSS is ~0.5 GB with the model loaded (weights are mmap'd), so process-memory figures
  under-report it.

## What wt looks like today (relevant facts)

- `lifecycle.backend` (`wt/internal/lifecycle/lifecycle.go:265`) has `tenancy/stop/stopModel/start`;
  `backendsByFamily` holds ollama, omlx, mtplx. mlx_lm_server is not startable by wt (llmbench starts it).
- The inventory side is separate: `localmodels.source` is filled by `probeFamily`; `matchArtifact`,
  `SameModel`, `ServedIDs`, `FamilyOrigin` switch on the family name.
- Probes send **no key**: `FetchModelIDsErr` does a bare GET and any non-2xx is an error;
  `waitForModel` is the same. Only warmup and omlx pool calls send `FamilyAPIKey`.
- mtplx specifics: port 8003 fill-in in `Provider.Origin()` and `lifecycle.MtplxAddressError`;
  pidfile `/tmp/local-ai-setup-mtplx.pid` shared with llmbench; loading detection by verified pid
  (`mtplx_loading.go`, `procinfo.go`), because the port is closed while loading.
- Location (local/cloud) is per model, inherited from the provider; `ollama` hosts both.
- Routing: `litellm.policies` (Prefix, FixedModel, APIKey, SecretRef, Cloud, V1Base).

## Goals

1. One `Provider` interface (core) that local and cloud providers implement, built from a registry row
   plus built-in defaults, held in a single table. All the per-provider name switches in
   `wt` read it.
2. Optional capabilities discovered by type assertion: `Local` (tenancy, start, stop, stop-model,
   snapshot). Cloud providers implement the core only. (`Catalog` is a later capability: issue #453.)
3. A configuration-driven `processServer` implementation covering mtplx and rec. A new
   one-process-per-server provider is a registry/defaults entry, no Go.
4. `ollama` and `omlx` keep named drivers behind the same interface (their pool, deferred-stop and
   discovery protocols are code), selected by `driver = "ollama"|"omlx"`.
5. rec works end to end: seeded, startable, stoppable, probed with its key, routed through LiteLLM as
   `flint` while running, shown in the picker/`wt model list`, exclusive across the machine.
6. mtplx moves onto `processServer` with **no behavior change** (existing mtplx tests keep passing
   unmodified in what they assert).
7. `llmbench` reads the same provider description and gets an isolation backend for rec, instead of a
   second hand-coded provider table.
8. A conformance test runs over every built-in provider, so a provider missing a policy, family or
   tenancy fails a test (the omlx-6bit class of bug).

## Non-goals

- `wt cloud-sync` and the `Catalog` capability (issue #453). *Status 2026-10-26: the shipped provider layer is a facts table (`internal/provider`), not a Go interface with capabilities, so #453 was reduced to a flow table inside `cmd/wt` (`cloudFlows`); a `Catalog` fact on the provider table was not added.*
- Moving mlx_lm_server onto `processServer` (needs the target+draft pairing shape). It joins the core
  interface (identity, routing, running-only inventory) but stays llmbench-started.
- Rewriting ollama/omlx logic, the omlx pool/eviction planner, or ollama's deferred stop.
- An Anthropic-protocol route for rec (it has none). Claude-style agents reach flint only through LiteLLM.
- Fixing LiteLLM's handling of `reasoning` vs `reasoning_content` (verify and record; see below).
- A generic memory measurement for mmap'd weights.

## Anti-goals (would make this a failure even if it technically works)

- mtplx behaves differently after the migration (start, stop, loading detection, pidfile sharing with
  llmbench, error messages the user sees).
- A start of a rec model silently replaces or loads beside another running local model without the
  same confirmation flow every exclusive provider has.
- The spec language grows into a scripting language. If a provider needs more than the vocabulary, it
  is a named driver in Go.
- Existing `registry.toml` files stop loading or need a hand migration.
- Live model state leaking into the guides (repo rule): docs show the command, not the state.

## Decisions made by the user

- Scope: rec as a first-class local provider; generic design so local providers are configuration.
- Cloud providers share the interface for routing and identity; cloud-sync is a follow-up (#453).
- mtplx migrates onto the new engine in this project.
- Live starts/stops of rec are allowed for testing (rec 0.1.0 is installed; flint is downloaded).
- A rec already running that wt did not start (no pidfile, e.g. from `rec launch`): if it is already
  serving the requested model (`flint`), **adopt** it (no stop, no start; wt records nothing it does not
  own, and `wt stop` still stops it with `rec stop`). If it is not serving that model, **ask first** with
  the same confirmation flow other occupants get, then stop it and start the requested one. Never stop a
  foreign instance silently. Without a TTY, the existing no-TTY rule for that flow applies. Detection is by
  `rec status` (readiness command), not by pidfile.

## Design

### Interface

Core `Provider` (every provider, local and cloud):

- identity: id, display name, family (which provider ids share one server; default own id), location default
- `Origin()` base URL and `Auth()` (key via `secret_ref`; sent on every call wt makes to the server)
- protocols (wire formats it speaks; decides direct vs via-LiteLLM for agents)
- `SameModel(a, b)` name matching and `WireName(model)` (what goes in the request; rec: fixed `flint`)
- `Route(model)` the LiteLLM mapping (prefix, api_base form, key source, fixed model, cloud flag),
  or none for native providers

`Local` capability (type-asserted, local servers only): `Tenancy()` (shared/pool/exclusive plus
`exclusive_scope` port|machine), `Start`, `Stop`, `StopModel`, `Snapshot` (installed, loaded, status,
sizes, down/error as `localmodels.source` carries them today). Further optional capabilities
(`PoolReader` for omlx, deferred-unload for ollama) are separate small interfaces so the extras do not
widen the core.

Package placement is the builder's call, with one constraint: `config` cannot import `localmodels`
(the reason some tables are duplicated today), so the interface and its table live in a package both
consumers can import, with no cycle.

### Provider description (registry `[[providers]]` fields, sketch)

Illustrative, not final naming. The builder fixes names; the contents are the requirement.

```toml
[[providers]]
id = "rec"
location = "local"
driver = "process"            # "process" (config-driven) | "ollama" | "omlx"
family = "rec"
protocols = ["openai-chat"]
tenancy = "exclusive"
exclusive_scope = "machine"   # "port" (default) | "machine"
binary = "rec"
port_owner = "wt"             # wt hands the port to the server (mtplx, rec) | "server"
default_port = 11500
served_id = "fixed:flint"     # request-side model name; default: the registry model_name
inventory = "served"          # "served" | "dir" | "api" (named drivers) | "none"
routes_follow = "running"     # "running" | "artifact" (ollama)

[providers.auth]
base_url   = "http://127.0.0.1:11500/v1"
secret_ref = "exec:rec auth token"

[providers.start]
spawn   = ["rec", "serve", "--port", "{port}"]   # a user may append "--context", "N" here
log     = "~/.cache/local-ai/rec.log"
pidfile = "/tmp/local-ai-setup-rec.pid"

[providers.stop]
run         = ["rec", "stop"]
ignore_exit = true            # judge by the readiness/port check, never by rc
timeout     = "15s"

[providers.ready]
kind    = "command-json"      # "models-lists" | "http-any" | "command-json"
command = ["rec", "status"]
path    = "ready"
equals  = true
timeout = "120s"

[providers.litellm]
prefix  = "openai/"
v1_base = true
api_key = "secret_ref"        # key resolved from auth.secret_ref instead of a literal
```

mtplx is the same shape with `spawn = ["mtplx","serve","--model","{model}","--port","{port}",
"--host","127.0.0.1","--model-id","{model}"]`, `stop.run = ["mtplx","stop","--port","{port}",
"--grace-seconds","10"]`, `ready.kind = "models-lists"`, `inventory = "dir"` (flat `org--name` layout,
unchanged scanning rules), `api_key = "not-needed"`.

### Resolution and compatibility

A provider's effective description = built-in defaults for that id, overlaid by the registry row.
So today's registry rows for ollama/omlx/mtplx, which carry none of the new fields, keep
working with no edit. *(My call.)*

The built-in defaults live in one TOML file that both wt (embedded, tested equal) and llmbench
(read at runtime) use, placed with the other cross-language fixtures under `docs/contracts/`.
*(My call.)* A contract test asserts wt's embedded copy equals it.

### Engine behaviors the process kind must provide (generalised from mtplx)

- Pre-spawn: refuse if the port is already answering; refuse a `port_owner = "wt"` URL with no port on
  a non-local host (today's `MtplxAddressError`, generalised and reworded per provider).
- Spawn detached with log + pidfile (existing `pidProcess`); verified-pid identity before signalling
  (today argv-recognised for mtplx; the recogniser becomes data: the binary name plus subcommand
  prefix from `spawn`).
- **Loading state**: port closed while a verified own pid is alive means *loading*, not *nothing
  running*; routes are not deleted; residents/others check counts it. Generalises
  `loadingMtplx`/`evictions.go:189` to any process-kind family.
- Readiness by the three kinds above; `command-json` runs the command with a timeout and compares a
  JSON path. Fail fast if the spawned pid dies while waiting.
- Stop: run the stop command, ignore its exit code, wait until the readiness target is down and the
  port is closed within the timeout; fall back to SIGTERM via the verified pidfile process. Forget
  the pidfile only after the verified pid is gone.
- Warmup: one-token chat using `WireName` and the key.
- Every probe the server sees (inventory liveness, `ServedIDs`, `UnregisteredAnswering`, `waitForModel`,
  warmup, port-closed checks) sends the bearer key when the provider has a `secret_ref`. A 401 with a key
  present surfaces as a key-refused error, not as "server down". Providers without a `secret_ref`
  (all of today's) send nothing, so ollama/omlx/mtplx probes are byte-for-byte unchanged.
- `exclusive_scope = "machine"`: for occupancy, any running instance of the family counts as the
  occupant regardless of port (found via the readiness command or the pidfile), and a start first stops
  it with the stop command.
- LiteLLM api_key from `secret_ref` is written into `config.yaml` as today's `SecretRef` does for
  openrouter. `exec:` secrets are cached per process, so a rotated token is stale until next sync; on a
  401 the engine re-resolves once. *(My call.)*

### rec specifics

- Provider row seeded by `wt model init` / the Models-tab add form when `rec` is on PATH; model
  `rec/flint` with `model_name = "flint"`.
- No `--context` is passed by default, so rec uses its own default (262,144). There is no per-model
  context field in the registry today and this project adds none; a user who wants another context
  edits `start.spawn` in the provider row. *(My call.)*
- Resident size: rec reports no figure and mmap hides it, so the others-check and listings show the
  size as unknown ("not reported") rather than invent one. *(My call.)*
- `bin/update/rec` joins `bin/update-tools` (wraps `rec update`, follows the `[update:<tool>]` status
  contract). *(My call.)*
- `modeltiers`: an alias row for `rec/flint` only if public-source evidence exists; otherwise it stays
  listed and `unrated`, per modeltiers rules. *(My call.)*

### llmbench

`llmbench` provider isolation reads the same description (registry rows + the shared defaults file)
for ids, ports, binary, start/stop/ready, restore action, replacing its per-provider constants where the
provider is process-kind. rec gets an isolation backend that goes through that path (`restore_action =
stop`, never in the standing baseline, like mtplx). ollama/omlx isolation logic and the isolation
ordering guarantees (validate before teardown, stop others, then start/ready/warm) stay code.
mlx_lm_server keeps its Python backend.

### Docs and fixtures that move with it

`docs/contracts/registry.sample.toml`, `registry.written.sample.toml`, `model-list.sample.json`
(providers status map), `tiers.sample.toml`, `discovered-ids.sample.json`; guides 00, 02, 04, 05, 06, 08;
`docs/reference/provider-artifacts.md`; `docs/internals/*` that enumerate providers; root and `wt/` and
`llmbench/` CLAUDE.md. A new section in the provider docs says how to add a process-kind provider by
config. The `adding-a-provider` skill is rewritten to the config path (named-driver path kept for
ollama/omlx-style protocols). Guides show commands, never live model state.

### Tests

- Existing mtplx tests pass without loosening their assertions (they may be parameterised to run
  through the engine).
- A conformance test over every built-in provider: has a family, a tenancy (if Local), a route or an
  explicit native marker, an origin, a binary or none; omlx-6bit-style gaps fail.
- Golden tests: spec -> argv/ready/stop for mtplx and rec with fake `run`/`lookPath`/probe seams (the
  existing env seams).
- Probe-with-key tests (200 with key, 401 without, 401 with wrong key) for the inventory, `ServedIDs`
  and `waitForModel` paths.
- Exclusive-machine-scope test: a rec running on another port is the occupant.
- Live check, run by hand, not CI: start rec through `wt start`, route through LiteLLM, chat through the
  proxy, stop; record whether `reasoning` survives LiteLLM. If it does not, note it in the guides as a
  known limitation; fixing it is out of scope.

## Delivery

Sizing: a project. Suggested PR order, each green on `make test-all` and mergeable alone:

1. Provider interface + single table; `policies`, `familyIDs`, `discoveredFamily`, seed tables, default
   origins derive from it; behavior unchanged; conformance test.
2. `processServer` engine; mtplx migrated onto it; generic loading-state, key-on-every-probe plumbing
   (no provider has a key yet); command-json readiness; exclusive scope.
3. rec provider (defaults, seeding, `bin/update/rec`), LiteLLM route, picker/list integration, docs.
4. llmbench reads the description; rec isolation backend; modeltiers alias if evidence; remaining docs and
   skill rewrite.

## Calls made after the builder check

Answered by the repo or settled here (all mine unless marked):

- **Cross-family conflicts.** rec follows the existing cross-family "others" flow (`--others`, the TUI
  dialog, `lifecycle.StoppableIDs`) like every local start: starting flint asks about other running
  families' models. `exclusive_scope = "machine"` only widens what counts as a *rec* occupant (any rec
  instance on any port); it adds no new cross-family rule.
- **Secrets.** `config.yaml` already holds provider keys (openrouter's) and wt preserves the file's
  existing mode, so a keyed local route is not a new exposure. A failed `rec auth token` resolve is
  reported like a refused key ("rec's key could not be read: run `rec auth token`") and the provider
  shows as partial, never as down; it does not block seeding.
- **Key rotation.** `wt start` already rewrites the model's route and restarts LiteLLM only when the
  rows changed; that path re-resolves the key. A rotation mid-run stays stale until the next start or
  `wt litellm sync`, as for openrouter keys.
- **Listings and cost.** `rec status` talks to a local control socket (milliseconds), so each probe
  cycle may run it once. Listings call a family "running" from the keyed `/v1/models` answer but not
  "ready" until `rec status` says so; routes are only written once the start flow has seen readiness.
- **Installed vs running.** `inventory = "served"` families (rec) are offered for start when the
  registry has the model and the binary is on PATH; they are "running" only while served.
- **Registry overlay.** Merge is field-wise (a row overriding `start.spawn` keeps the default `log`).
  Unknown `driver` is a load error naming the row. A user-defined id with `driver = "process"` and no
  built-in default loads if its row is complete. wt never writes defaults back to `registry.toml`.
  The conformance test covers the built-ins; a separate validation test covers user-defined rows.
- **llmbench.** It is run from the repo checkout (`uv run --directory llmbench`), so it reads the shared
  defaults file repo-relative; bundling it is not needed.
- **Existing mtplx tests.** Call-site edits are fine (renames, moves into the engine); what each test
  asserts must not loosen.

## Left to the builder

Package layout and names; exact TOML field names; template engine details for `{model}/{port}/{context}`;
how the shared defaults file is located by llmbench at runtime; how much of the Python provider
lifecycle can be deleted versus wrapped; test layout.

## Risks

- The mtplx loading/pid logic is 355 lines of subtle safety code (pid reuse, start-time binding, zombie
  handling). Generalising it is the highest-risk step; it must stay behavior-identical.
- rec is v0.1.0 and new: its flags, status JSON and error text may change. The description is data, so
  that is a config edit, but `command-json` readiness depends on `rec status` keeping `ready`.
- Key plumbing touches every probe path; ollama/omlx/mtplx must stay unchanged when no key is configured.
- The earlier rec server (started 14:52 by `rec launch`) was stopped during testing and not restarted.
