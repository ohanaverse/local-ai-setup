# modelman-owned LiteLLM control + protocol-negotiated routing

Status: approved (brainstorming session 2026-09-10). Source of truth for the
implementation plan; do not diverge without updating this doc.

## Context

Today the LiteLLM on/off switch is `[gateway].mode` in **wt's own**
`config.toml` — hand-edited TOML with no UI. modelman, which already owns
`model_list`, the exposure flags, and the proxy restart, cannot control
whether wt actually uses the proxy. Meanwhile `litellm_exposed` does double
duty: "put this model in `model_list`" *and* "let wt offer this model at
all."

This change makes **modelman the single control plane for LiteLLM** and
separates the two concerns:

- `[litellm]` in `modelman.toml` owns `enabled` / `url` / `api_key`; wt reads
  it read-only. wt's `[gateway]` section is deleted.
- The per-model flag is renamed `litellm_exposed` → `exposed` and means only
  "visible in wt's selector", independent of LiteLLM state.
- Toggling is **routing policy only** — it never starts/stops the proxy, and
  `modelman expose` keeps writing `model_list` rows either way, so flipping
  back on needs no re-expose.

Two latent bugs surface once the toggle is a keystroke instead of a hand
edit, and both must be fixed here:

1. **"Direct" actually means "ollama."** Every driver hardcodes
   `config.OllamaBaseURL`; `Provider.Auth.BaseURL` is decoded
   (`config.go:76`) but never read at launch. With litellm off today, 12 of
   the 23 exposed models (`openrouter/*`) silently point at
   `localhost:11434`. `ollamacheck.Check` returns `(true, nil)` for any
   non-ollama provider, so nothing catches it — and the rationale comment at
   `gateway_matrix_test.go:23-29` asserts a protection that does not exist.
2. **Not every agent×provider pair can work direct.** Protocols don't
   interoperate: `POST /v1/responses` and `POST /v1/chat/completions` are
   different schemas (LiteLLM's crashing responses→completions bridge at
   `litellm-troubleshooting.md:113` is the proof). claude needs an
   Anthropic-compatible endpoint; codex ≥ 0.148 **removed**
   `wire_api="chat"` and always speaks responses
   (`litellm-troubleshooting.md:110-112`), which no local provider serves
   natively — so **codex structurally requires the proxy**.

**Outcome:** agents and providers declare which protocols they support.
Their intersection decides whether a direct dial is possible. Pairings with
an empty intersection route through LiteLLM **regardless of the on/off
setting** (announced on stderr); everything else respects the setting.

## Design

### Protocol negotiation (the core)

Protocol identifiers: `anthropic`, `openai-chat`, `openai-responses`.

**Agent protocols are driver knowledge**, not user config — hardcoded per
driver like `YoloFlag()`, ordered by preference:

| Agent | Protocols | Source |
|---|---|---|
| claude | `[anthropic]` | sets `ANTHROPIC_BASE_URL` (`claude.go:68-74`) |
| codex | `[openai-responses]` | `codex.go:63`; `wire_api="chat"` removed in codex ≥ 0.148 |
| copilot | `[openai-chat]` | pinned `WIRE_API=completions` (`copilot.go:45-60`) — responses drops leading chars |
| opencode | `[openai-chat]` | `@ai-sdk/openai-compatible` |
| pi | `[openai-chat]` | |
| agy, shell | `[]` | no model routing; never resolve a route |

**Provider protocols are registry metadata** — a new `protocols` array on
`[[providers]]`, modelman-owned, defaulting to `["openai-chat"]` when
absent. Seeded by `_DEFAULT_PROVIDER_TEMPLATES` (`registry.py:246`): ollama
`["anthropic","openai-chat"]`, omlx/mlx_lm_server/llamacpp/openrouter
`["openai-chat"]`. Native providers are exempt (no route).

**The LiteLLM proxy is not a registry provider** — its protocol set is a wt
constant: all three, since bridging is its purpose.

`(*Config).ResolveRoute(m Model, agent string) (Route, error)` decides:

1. `m.Native` → no route (drivers early-return; unchanged).
2. `intersect(agentProtocols, providerProtocols)`:
   - **non-empty** and litellm **disabled** → direct route; protocol =
     first agent-preference match.
   - **empty** → **forced litellm** regardless of the setting;
     `Route.Forced = true`. wt prints one stderr line before launch:
     `wt: codex requires LiteLLM for ollama/qwen3.8:27b-mlx (needs
     openai-responses; provider serves openai-chat) — routing through the
     proxy`.
   - litellm **enabled** → litellm route.
3. Forced-or-enabled but `[litellm].url` empty → actionable error naming
   `modelman litellm set --url … --api-key …` (or `modelman litellm on`).

### Model picker semantics

**One catalog row per model+provider — never per transport.**
`ollama/qwen3.8:27b-mlx` is a single registry entry; transport is resolved
at launch, not enumerated. The toggle therefore never changes the list's
length, and there is no "ollama direct" row beside an "ollama litellm" row.

The agent is always chosen **before** the model (worktree → agent →
model), so the filter chain is unchanged by this work except at the last
link:

`ModelsForAgent(agent)` (the `supported_providers` allowlist) → `-T`/`-F`
filters → exposure gate (`exposed` after the rename).

**The allowlist and protocol negotiation stay independent.**
`supported_providers` is a hand-curated preference (*what to offer*);
negotiation is a mechanical fact about wire protocols (*how to dial it*).
An allowlisted pairing that cannot dial direct simply routes through the
proxy. No config change, and negotiation never removes rows the allowlist
admitted.

**No row becomes unlaunchable by flipping the toggle** — forced-litellm
covers every gap. A row fails only under misconfiguration: litellm needed
but `url`/`api_key` absent, or direct needed but the provider has no
`base_url` (`omlx`, until the template backfill).

Because the agent is known at render time and `ResolveRoute` is pure config
math (no network), the picker annotates **exceptions only**: a footer line
states the current mode (`LiteLLM: off (direct)`), and a short suffix tags
only rows whose transport *differs* from it (`(via proxy)`) or that cannot
launch. Plain ASCII only — per `wt/CLAUDE.md`, Unicode geometric shapes are
East Asian Ambiguous width and misalign CJK terminals. Rows already carry
refcount, rotation marker, family, 30-day count, pricing and survey stats;
a per-row transport column would be noise when transport is uniform.

A pinned `-M` model validates routability too, failing fast with the same
actionable error rather than silently misrouting.

### `Route` replaces `OllamaURLer`

`OllamaURLer` (`agents.go:92-98`) takes no arguments and each impl returns a
constant — no seam for per-provider URLs. Replace it:

```go
type Route struct {
    BaseOrigin string   // scheme://host:port — no wire path
    APIKey     string
    ModelRef   string   // m.ID via proxy, m.ModelName direct — a property of the endpoint's catalog
    Display    string   // m.ModelName for catalog "name" fields
    ProviderID string   // namespace for pi/opencode provider maps
    Protocol   Protocol
    Litellm    bool
    Forced     bool
}
```

`Driver.Build(m, yolo, r config.Route)`; delete the `Gateway` alias
(`agents.go:27`) and `OllamaURLer`. Each driver keeps a private
`wireURL(r)` appending only its own suffix (claude `""`, copilot/opencode/pi
`"/v1"`, codex `"/v1/"`) with zero ollama knowledge. Resolve once in
`BuildLaunchCmd` (`agents.go:269-272`) **before** `Syncer`, returning the
error so `cmd/wt/launch.go` and `internal/tui/app.go` surface it
identically.

**This collapses branches rather than adding them.** opencode's litellm and
direct paths are the same shape with different endpoint/key/model-key
(`opencode.go:52-79`), so its direct-mode special case is *deleted*: use the
`agent-wt` custom provider in both modes, `model = "agent-wt/" + r.ModelRef`,
`models: {r.ModelRef: {name: r.Display}}`. This also drops the
builtin-`ollama` catalog dependency and fixes slashed openrouter names.

**pi fans out:** `syncDirectOllama` (`pi_models.go:242-290`) →
`syncDirectProviders` — group non-native models by `ProviderID`, upsert one
pi provider each (`apiKey` must be non-empty or pi's whole `models.json` is
invalid — keep the placeholder). `piDriver.Build` sends
`r.ProviderID + "/" + r.ModelRef`, fixing a live bug: pi splits `--model` on
the first slash, so today's bare `z-ai/glm-4.6` resolves provider `z-ai`.

### Base-URL normalization — readers change, data does not

Registry values are inconsistently shaped (ollama bare, openrouter
`/api/v1`, mlx_lm_server template `/v1`) and each driver appends its own
suffix. **Do not canonicalize `registry.toml` or `config.yaml`**:
`litellm.py:277` writes `api_base = provider.auth.base_url` *verbatim*, so
rewriting values breaks openrouter's proxy rows and forces a re-expose of
all 23 models, and `test_registry_fixture.py:45` pins mlx_lm_server's `/v1`.

Instead add a tolerant normalizer on each side — Go `config.BaseOrigin(s)`,
Python `registry.base_origin(p)` (trim trailing `/`, strip a trailing
`/v1`, trim again) — documented in `registry.sample.toml` as "`base_url` is
an origin; readers normalize, drivers append the wire path."

**omlx needs two fixes:** `_DEFAULT_PROVIDER_TEMPLATES["omlx"]` has no
`AuthConfig` at all, and `sync.py:130` only appends *wholly missing*
providers. Add `AuthConfig(type="none", base_url="http://localhost:8000")`
to the template **and** a sync repair that fills a *missing*
`auth.base_url`/`protocols` on an existing provider from its template,
leaving user-set values alone.

### Secrets

One shared resolver on each side: a value matching `os.environ/NAME` or a
bare `^[A-Z][A-Z0-9_]*$` reads that env var; anything else is used
verbatim. Reused for **both** `providers.auth.secret_ref` (already
polymorphic — the fixture uses an env-var name, the live registry a literal
`sk-or-v1-…`) and the new `[litellm].api_key`. The contract fixture uses a
non-key-shaped placeholder.

### modelman control surface

`modelman litellm status|on|off|set` as a typer sub-app mirroring
`benchmark_app`/`usage_app` (`main.py:29-30`); `set --url/--api-key`. All
mutations go through `locked_state()`; `status` redacts the key; **every
help string states it does not start or stop the proxy.** TUI: free key `l`
on `screens/families.py` (`a/e/d/enter/g/q` taken) → a `ConfirmModal` from
`screens/forms.py`; state renders in the footer as
`LiteLLM: on (http://localhost:4000)` / `LiteLLM: off (direct)`.

### `[litellm]` as a typed field, not `extra`

`price_refresh_last_run`'s accessor-on-`extra` pattern (`state.py:31-41`)
suits an unvalidated scalar. `[litellm]` is a typed 3-key table,
contract-pinned in two languages, mutated by CLI+TUI, read on wt's launch
path — it gets `LitellmState` + `StateStore.litellm`. Paired edits or it is
written twice: add `"litellm"` to `unknown_keys(raw, {...})`
(`state.py:121`) **and** emit it in `save_state`'s `payload`
(`state.py:127`).

### wt reads, but never fails closed

`loadModelmanState` returns `(map, LitellmState, error)`; `finalizeCfg`
(`config.go:214-223`) stores both. `IsLitellm()`/`IsDirect()` become
methods on `*Config`. **Delete the validation block at `config.go:245-260`**
— `Validate()` runs on every invocation, and failing closed on a file wt
cannot repair would lock you out of every wt command including
`wt config`. Errors move to `ResolveRoute` (launch time).

### Migration (live machine has `mode="litellm"`, url, api_key)

wt cannot write `modelman.toml`. `modelman migrate` already knows wt's
config path (`registry.py:_default_wt_config_path`) — add an idempotent
`[gateway]`→`[litellm]` import (read wt's file read-only, write via
`locked_state`, skip if `[litellm]` exists). wt side: a
`migrateConfigSchema` fixup re-decodes into a throwaway struct, prints one
stderr notice pointing at `modelman litellm status`, returns
`changed=true`. Because the `Gateway` field is *deleted* from `Config`, the
ensuing `Save` (`config.go:513-522`) stops emitting the block —
self-extinguishing, and resurrection is impossible once the field is gone.

## Phases

Each phase ends green and is independently committable.

**P1 — Contract + metadata, ONE commit** (a `docs/contracts/` edit gates
both CI jobs; templates:
`docs/superpowers/plans/2026-09-07-wt-exposure-predicate.md`,
`…/2026-09-15-issue-46-provider-location-inheritance.md`). No behavior
change.
- `docs/contracts/modelman.sample.toml`: rename every `litellm_exposed` →
  `exposed`; keep exactly **one** legacy entry still spelling
  `litellm_exposed` + `downloaded` to pin both back-compat reads; rewrite
  the header rule comment; add `[litellm]` with a placeholder key.
- `docs/contracts/registry.sample.toml`: add `protocols` to providers +
  document the `base_url`-is-an-origin rule.
- Python: `ModelState.litellm_exposed` → `exposed` reading
  `entry.get("exposed", entry.get("litellm_exposed", False))`; **
  `litellm_exposed` must join the known-key set** or it survives in
  `extra` and gets rewritten forever alongside the new key. `LitellmState`,
  `StateStore.litellm`, `registry.protocols`, `base_origin`, secret
  resolver.
- Go: `modelmanState` gains `Exposed` + `LitellmExposed` + a `Litellm`
  struct; `loadModelmanState` new signature; `Provider.Protocols`;
  `BaseOrigin`; secret resolver.
- Tests: both contract tests assert both spellings, `[litellm]` values, and
  provider protocols; `test_state.py` round-trip proving a legacy-key file
  is rewritten with **only** `exposed`.
- Verify: `make test-all`.

**P2 — Go routing.** `Route`, `Protocol`, `ResolveRoute` with negotiation +
forced-litellm; rewrite `Build` in `claude.go`, `codex.go`, `copilot.go`,
`opencode.go`, `pi.go`, `pi_models.go`, `agy.go`, `shell.go`, plus
`agents.go` `BuildLaunchCmd`/`Command`; stderr notice for `Forced`; fix
`ollamacheck`'s doc comment and narrow the gate to
`!route.Litellm && ollamacheck.IsOllamaModel(m)`. `Route.Litellm` still
derived from `cfg.Gateway` so this ships alone.

Picker annotation lands here too, since it consumes the same resolution:
`internal/tui/app.go` passes the resolved agent into `enterModelPhase`;
`internal/tui/model_list.go` `buildModelItems` resolves each row's route
and sets an `exception` string, `Title()` appends it after the existing
segments; the mode footer renders from `cfg.IsLitellm()`.
- Tests: `ResolveRoute` direct-uses-provider-base-URL / missing-base-URL-
  errors / env-and-literal `secret_ref`; `TestBaseOriginTrimsV1Suffix`;
  protocol negotiation incl. `TestCodexForcesLitellmWhenProviderLacksResponses`
  and `TestClaudeForcesLitellmForOpenRouter`; extend `gateway_matrix_test.go`
  so direct×openrouter/omlx assert real per-provider URLs and **correct the
  false rationale comment at :23-29**; `TestPiDirectModelArgPrefixesProvider`
  (regression: slashed openrouter names misroute);
  `TestBuildModelItemsMarksOnlyDeviatingRows` asserted against
  `buildModelItems` directly, not rendered lipgloss output (per
  `wt/CLAUDE.md`: parsing rendered output couples tests to border glyphs and
  flakes under forced-color ANSI).
- Verify: `cd wt && make lint && go build ./... && go vet ./... && go test ./...`.

**P3 — modelman control surface.** `litellm_app` CLI; `screens/families.py`
binding + footer; `migrate.py` gateway import; omlx template + `sync.py`
backfill repair.
- Tests: `test_litellm_cli.py` (on/off/set/status through `locked_state`,
  status redacts, **no restart command invoked** — a routing toggle causing
  a service outage is the regression that matters); `test_migrate.py`
  import + idempotency; sync backfill leaves user-set values alone.
- Verify: `cd modelman && make check && make test`.

**P4 — wt cutover.** Delete `GatewayConfig`, its methods, the `Gateway`
field, the `config.go:245-260` validation block, and the gateway tests in
`config_test.go`; add `Config.IsLitellm/IsDirect`, `finalizeCfg` wiring,
the `migrateConfigSchema` fixup + notice.
- Tests: `TestMigrateDropsLegacyGatewayBlock`;
  `TestLitellmEnabledMissingURLFailsAtLaunchNotValidate` (regression:
  fail-closed validation on a file wt cannot write locks the user out of
  every wt command).
- Verify: `go test ./...`, then `make test-all`.

**P5 — smoke + docs.** `agents-smoke.sh`: `set_gateway_mode`→
`set_litellm_enabled` (awk-rewrite `enabled =` inside `[litellm]` in
**modelman.toml**, new `MODELMAN_FILE`, trap restores it), `gateway_field`→
`litellm_field`; keep `DEFAULT_MODES` and `is_mode_invariant`.
- Docs: `docs/guides/00` (:16,:21,:58,:61,:63-80,:167,:287), `02`
  (:44,:64,:217,:221-263,:282-284), `04` (§2,§3,:128,:283), `05`:10, `06`:
  89-107 — its "In this mode: wt only shows models with `litellm_exposed`"
  wrongly implies mode-gated filtering, `07`:101-108, `08`:148-181 — its
  "`config.yaml` is routing truth" is true only when the flag is on;
  `wt/docs/wt-config.md`, `wt/docs/wt-agents/README.md`, the five
  `### Gateway mode (LiteLLM)` sections (`claude-wt.md:54-64`,
  `codex-wt.md:59-74`, `copilot-wt.md:45-58`, `opencode-wt.md:61-69`,
  `pi-wt.md:48-57`), `litellm-troubleshooting.md`, both `CLAUDE.md`s (incl.
  the new protocol table).
- **Guides 00, 02, 04, 05, 06, 08 all paste live `litellm_exposed` output —
  the rename invalidates every one.** Run
  `git grep -n "litellm_exposed = " docs/guides/` before and after.
- Verify: `make lint` (shell lint + `check-links`), then
  `cd wt && make test-agents`.

## Verification

- Per phase as above; `make test-all` mirrors all three CI jobs.
- **Live end-to-end** (`make test-agents` flips the flag and runs every
  agent × configured models in both states): confirm ollama direct still
  works for claude/copilot/opencode/pi; confirm openrouter direct works for
  the `openai-chat` agents; confirm codex is announced as forced-litellm
  rather than silently failing.
- Manual: `modelman litellm off` → `wt --cwd -A copilot -M openrouter/…`
  dials `https://openrouter.ai/api/v1`; `wt --cwd -A claude -M
  openrouter/…` announces forced-litellm; `modelman litellm status`
  redacts the key; the proxy on :4000 stays up across both toggles.

## Risks

1. **The protocol table is a claim about the world.** It rests on the
   repo's documented wire findings, not a live probe (network was
   unreachable during design). Wrong entries cause wt to refuse or force a
   pairing incorrectly. `make test-agents` is the arbiter; treat the table
   as provisional until it passes.
2. **codex may have no working direct path at all** — expected, and the
   forced-litellm rule handles it. Preserve the LiteLLM ≤1.98.0 codex
   caveat (`additional_drop_params`) and copilot's `/v1` +
   `WIRE_API=completions` verbatim; do not "simplify" either.
3. **opencode's direct-mode provider swap** (builtin `ollama` →
   `agent-wt`) is live behavior no unit test can validate.
4. **Downgrade window:** an older `wt` reading a post-rename
   `modelman.toml` hides every non-native model. P1 lands as one commit,
   green, before pushing.
5. `atomic_write` uses mkstemp 0600 — now desirable, worth asserting in a
   test. Note in the guides that `modelman.toml` is no longer safe to paste
   into an issue if a literal key is stored (the env-ref form avoids this
   entirely).
