# modelman-owned LiteLLM control + protocol routing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move the LiteLLM on/off switch from wt's own `config.toml` into modelman's `modelman.toml` (`[litellm]` table), decouple the `exposed` flag (renamed from `litellm_exposed`) from routing, and make wt dial each provider directly by its own registry `base_url` when LiteLLM is off — except for agent×provider pairs whose wire protocols don't overlap, which route through the proxy regardless of the setting.

**Architecture:** Two shared contract fixtures (`docs/contracts/*.sample.toml`) pin the cross-language schema; modelman owns and mutates `[litellm]` + `exposed` + provider `protocols`, wt reads them read-only. A new `Route` type replaces wt's argument-less `OllamaURLer` capability; `(*Config).ResolveRoute` resolves one route per launch by intersecting the launching agent's declared protocol set with the target provider's declared protocol set — empty intersection forces LiteLLM regardless of the toggle.

**Tech Stack:** Python 3.13 (`modelman/`, `uv`, `typer`, `textual`, `pytest`), Go 1.26 (`wt/`, `cobra`, `bubbletea`, `go test`).

**Spec:** `docs/superpowers/specs/2026-09-10-litellm-control-design.md`

## Global Constraints

- A `docs/contracts/*.sample.toml` change lands as one commit per fixture change — both `modelman-ci.yml` and `wt-ci.yml` trigger on `docs/contracts/**`, and a partial update fails both.
- Never rewrite `registry.toml`'s or `config.yaml`'s existing `base_url`/`api_base` values — `litellm.py:277` writes `api_base` verbatim into the proxy config, and canonicalizing would break OpenRouter's live rows and force a re-expose of every model.
- The LiteLLM on/off toggle is **routing policy only** — it must never start, stop, or restart the proxy (`restart_litellm_proxy` is called only by `expose`/`unexpose`, never by the new `litellm` commands).
- wt must never fail closed (refuse to run) on a `modelman.toml` value it disagrees with — it can't repair a file it doesn't own. Misconfiguration errors surface at launch (`ResolveRoute`), not at `Validate()`.
- Preserve verbatim: codex's `additional_drop_params: ["reasoning_effort"]` LiteLLM workaround, and copilot's `WIRE_API=completions` (the `responses` wire drops leading characters through the LiteLLM bridge).
- Every Go `Test*` and every Python test function carries a one-line comment stating what it tests and why a regression matters.
- Rendered TUI output (lipgloss/bubbletea) is never parsed in tests — assert against the unexported builder functions directly (e.g. `buildModelItems`), per `wt/CLAUDE.md`.

---

## File Structure

**modelman (Python):**
- `modelman/src/modelman/state.py` — `ModelState.exposed` (renamed), `LitellmState` dataclass, `StateStore.litellm`, back-compat read of `litellm_exposed`.
- `modelman/src/modelman/registry.py` — `Provider.protocols`, `base_origin()`.
- `modelman/src/modelman/litellm.py` — every `.litellm_exposed` attribute access → `.exposed`.
- `modelman/src/modelman/sync.py`, `queue.py`, `screens/models.py`, `benchmark/runner.py` — same attribute rename; `sync.py` also gets the omlx protocol/base_url backfill repair.
- `modelman/src/modelman/main.py` — new `litellm_app` sub-app (`status`/`on`/`off`/`set`).
- `modelman/src/modelman/migrate.py` — one-time `[gateway]` → `[litellm]` import from wt's config.
- `modelman/src/modelman/screens/families.py` — `l` keybinding + footer.

**wt (Go), module root `wt/`:**
- `wt/internal/config/modelman.go` — `loadModelmanState` gains a `LitellmState` return value; `Exposed`/`LitellmExposed` decode.
- `wt/internal/config/config.go` — `Provider.Protocols`, `BaseOrigin`, `ResolveSecret`, `Route`, `ResolveRoute`, deletion of `GatewayConfig` (Task 9).
- `wt/internal/config/protocol.go` — new file: `Protocol` type + constants.
- `wt/internal/agents/agents.go` — `Driver.Build` signature, `ProtocolDeclarer` interface, `BuildLaunchCmd` route resolution + forced-litellm stderr notice.
- `wt/internal/agents/{claude,codex,copilot,opencode,pi,pi_models,agy,shell}.go` — `Build` rewritten against `Route`.
- `wt/internal/ollamacheck/ollamacheck.go` — doc-comment fix, `IsOllamaModel` helper.
- `wt/internal/tui/model_list.go`, `app.go` — picker footer + exception markers.
- `wt/cmd/wt/launch.go` — gate + stderr notice call site for the non-TUI path.
- `wt/scripts/agents-smoke.sh` — `set_litellm_enabled`/`litellm_field` against `modelman.toml`.

**Shared:**
- `docs/contracts/modelman.sample.toml`, `docs/contracts/registry.sample.toml`.

---

### Task 1: Rename `litellm_exposed` → `exposed` and add `[litellm]` — contract + both languages, ONE commit

**Files:**
- Modify: `docs/contracts/modelman.sample.toml`
- Modify: `modelman/src/modelman/state.py`
- Modify: `modelman/src/modelman/litellm.py`
- Modify: `modelman/src/modelman/sync.py`
- Modify: `modelman/src/modelman/queue.py`
- Modify: `modelman/src/modelman/screens/models.py`
- Modify: `modelman/src/modelman/benchmark/runner.py`
- Test: `modelman/tests/contracts/test_modelman_fixture.py`
- Test: `modelman/tests/test_state.py`
- Modify: `wt/internal/config/modelman.go`
- Modify: `wt/internal/config/config.go` (only the `exposed` map field of `Config`, not `Provider`/`Route` yet)
- Test: `wt/internal/config/modelman_fixture_test.go`
- Test: `wt/internal/config/modelman_test.go`

**Interfaces:**
- Produces (Python): `ModelState.exposed: bool = False`; `LitellmState` dataclass (`enabled: bool = False`, `url: str | None = None`, `api_key: str | None = None`); `StateStore.litellm: LitellmState`.
- Produces (Go): `modelmanState.ModelState[id].Exposed bool`; `type LitellmState struct { Enabled bool; URL string; APIKey string }`; `loadModelmanState(path string) (map[string]modelStateEntry, LitellmState, error)` — **signature change**, update the one call site in `finalizeCfg` (`config.go:214-223`).

- [ ] **Step 1: Update the shared contract fixture**

Edit `docs/contracts/modelman.sample.toml`. Rename every `litellm_exposed = true/false` to `exposed = true/false`, **except one entry** which keeps the legacy spelling to pin the back-compat read:

```toml
# --- exposure rule ---
# native OR (exposed AND (ready OR cloud location))
# "exposed" was named litellm_exposed before <this change>; a bare
# litellm_exposed key (no "exposed" key) is still honored for one release.

[litellm]
enabled = true
url = "http://localhost:4000"
api_key = "sk-litellm-CONTRACT-FIXTURE-NOT-A-REAL-KEY"

[model_state."ollama/qwen3.8:27b-mlx"]
ready = true
disk_path = "ollama:qwen3.8:27b-mlx"
size_bytes = 19327352832
exposed = true

[model_state."ollama/kimi-k3:cloud"]
ready = false
exposed = true

# Legacy spelling — must still be honored as exposed=true (back-compat read)
[model_state."llamacpp/legacy-contract-fixture"]
downloaded = true
litellm_exposed = true
```

Keep every other existing entry in the file (native-exposure cases, provider-location-inheritance cases, etc.) — only rename the key.

- [ ] **Step 2: Update the Python contract test to assert the new shape (it will fail)**

Edit `modelman/tests/contracts/test_modelman_fixture.py`:

```python
def test_load_state_matches_shared_fixture():
    """Pins modelman's read of the shared docs/contracts/modelman.sample.toml
    fixture: both languages must decode `exposed`, the one legacy
    `litellm_exposed`-only entry, and the new `[litellm]` table identically,
    or a schema drift between modelman and wt ships silently."""
    state = load_state(path=FIXTURE)

    assert state.models["ollama/qwen3.8:27b-mlx"].exposed is True
    assert state.models["ollama/kimi-k3:cloud"].exposed is True
    assert state.models["llamacpp/legacy-contract-fixture"].exposed is True  # back-compat read

    assert state.litellm.enabled is True
    assert state.litellm.url == "http://localhost:4000"
    assert state.litellm.api_key == "sk-litellm-CONTRACT-FIXTURE-NOT-A-REAL-KEY"

    assert state.extra.get("price_refresh_last_run") == "2026-09-14"
```

- [ ] **Step 3: Run the Python contract test to verify it fails**

Run: `cd modelman && uv run pytest tests/contracts/test_modelman_fixture.py -v`
Expected: FAIL — `ModelState` has no attribute `exposed`, and `StateStore` has no attribute `litellm`.

- [ ] **Step 4: Rename the dataclass field and add `LitellmState`**

Edit `modelman/src/modelman/state.py`:

```python
@dataclass
class LitellmState:
    enabled: bool = False
    url: str | None = None
    api_key: str | None = None


@dataclass
class ModelState:
    ready: bool = False
    disk_path: str | None = None
    size_bytes: int | None = None
    exposed: bool = False  # was litellm_exposed
    extra: dict[str, Any] = field(default_factory=dict)


@dataclass
class StateStore:
    models: dict[str, ModelState] = field(default_factory=dict)
    families: dict[str, FamilyState] = field(default_factory=dict)
    litellm: LitellmState = field(default_factory=LitellmState)
    extra: dict[str, Any] = field(default_factory=dict)
```

In `load_state` (around `:99-110`), read either key, preferring the new one:

```python
exposed = entry_raw.get("exposed", entry_raw.get("litellm_exposed", False))
ready = entry_raw.get("ready", entry_raw.get("downloaded", False))
model_state = ModelState(
    ready=ready,
    disk_path=entry_raw.get("disk_path"),
    size_bytes=entry_raw.get("size_bytes"),
    exposed=exposed,
    extra=unknown_keys(entry_raw, {"ready", "downloaded", "disk_path", "size_bytes", "exposed", "litellm_exposed"}),
)
```

Decode `[litellm]` alongside `[model_state]`/`[families]` in `load_state`:

```python
litellm_raw = raw.get("litellm", {})
litellm = LitellmState(
    enabled=litellm_raw.get("enabled", False),
    url=litellm_raw.get("url"),
    api_key=litellm_raw.get("api_key"),
)
```

Add `"litellm"` to the top-level `unknown_keys(raw, {"model_state", "families", "litellm"})` call (`:121`) — otherwise `[litellm]` lands in `extra` *and* gets re-serialized from the new field, writing it twice.

In `save_state` (`:125-145`), emit `exposed` (never `litellm_exposed`) and the `[litellm]` table:

```python
payload = {
    "model_state": {
        model_id: {
            "ready": m.ready,
            **({"disk_path": m.disk_path} if m.disk_path else {}),
            **({"size_bytes": m.size_bytes} if m.size_bytes is not None else {}),
            "exposed": m.exposed,
            **m.extra,
        }
        for model_id, m in store.models.items()
    },
    "families": {...},  # unchanged
    "litellm": {
        "enabled": store.litellm.enabled,
        **({"url": store.litellm.url} if store.litellm.url else {}),
        **({"api_key": store.litellm.api_key} if store.litellm.api_key else {}),
    },
}
atomic_write_toml({**store.extra, **payload}, path)
```

- [ ] **Step 5: Rename every `.litellm_exposed` attribute access**

Grep and fix each site (attribute access on a `ModelState` instance, not a TOML key):

```bash
cd modelman && grep -rn '\.litellm_exposed\b' src/modelman/
```

Expected hits and fixes:
- `src/modelman/litellm.py` — `is_effectively_exposed`, `passes_ready_gate`, `_validated_entry`, `_set_exposed_flag`: change `model_state.litellm_exposed` → `model_state.exposed`.
- `src/modelman/sync.py` (`:186,202,213`): same rename when reading/preserving state across sync.
- `src/modelman/queue.py` (`:324,471-479`): same rename in the delete/ready-off cascade and apply-expose-queue paths.
- `src/modelman/screens/models.py` (`:408-424,582`): EXPOSED column computation and `action_toggle_expose`.
- `src/modelman/benchmark/runner.py` (`:78`): default benchmark target filter.

- [ ] **Step 6: Run the full Python test suite to verify Task 1's Python half passes**

Run: `cd modelman && uv run pytest -q`
Expected: PASS, including `tests/contracts/test_modelman_fixture.py` and the existing `tests/test_state.py`.

- [ ] **Step 7: Add the round-trip regression test for the rename**

Edit `modelman/tests/test_state.py`:

```python
def test_legacy_litellm_exposed_key_is_rewritten_as_exposed(tmp_path):
    """A modelman.toml written by a pre-rename modelman still has
    litellm_exposed keys. Loading and saving it must produce only the new
    `exposed` key — otherwise the file accumulates both spellings forever
    and the two languages' readers can silently disagree on which one wins."""
    path = tmp_path / "modelman.toml"
    path.write_text(
        '[model_state."ollama/x"]\n'
        "ready = true\n"
        "litellm_exposed = true\n"
    )
    store = load_state(path=path)
    assert store.models["ollama/x"].exposed is True

    save_state(store, path=path)
    written = path.read_text()
    assert "litellm_exposed" not in written
    assert "exposed = true" in written
```

- [ ] **Step 8: Run the new test to verify it passes**

Run: `cd modelman && uv run pytest tests/test_state.py -k legacy_litellm_exposed -v`
Expected: PASS.

- [ ] **Step 9: Update the Go contract fixture test to assert the new shape (it will fail)**

Edit `wt/internal/config/modelman_fixture_test.go`:

```go
// TestLoadModelmanStateMatchesSharedFixture pins wt's read of the shared
// docs/contracts/modelman.sample.toml fixture to the same field names and
// values modelman's Python test asserts — a schema drift between the two
// readers would otherwise ship silently since each side's CI only runs its
// own language's tests.
func TestLoadModelmanStateMatchesSharedFixture(t *testing.T) {
	models, litellm, err := loadModelmanState(fixturePath)
	if err != nil {
		t.Fatalf("loadModelmanState: %v", err)
	}
	if !models["ollama/qwen3.8:27b-mlx"].Exposed {
		t.Error("expected ollama/qwen3.8:27b-mlx to be exposed")
	}
	if !models["llamacpp/legacy-contract-fixture"].Exposed {
		t.Error("legacy litellm_exposed-only entry must still read as exposed")
	}
	if !litellm.Enabled || litellm.URL != "http://localhost:4000" {
		t.Errorf("got litellm=%+v", litellm)
	}
	if litellm.APIKey != "sk-litellm-CONTRACT-FIXTURE-NOT-A-REAL-KEY" {
		t.Errorf("got api key %q", litellm.APIKey)
	}
}
```

- [ ] **Step 10: Run the Go contract test to verify it fails**

Run: `cd wt && go test ./internal/config -run TestLoadModelmanStateMatchesSharedFixture -v`
Expected: FAIL — `loadModelmanState` returns two values today, not three; `Exposed` field doesn't exist.

- [ ] **Step 11: Update `modelmanState` decoding and `loadModelmanState`'s signature**

Edit `wt/internal/config/modelman.go`:

```go
type LitellmState struct {
	Enabled bool   `toml:"enabled"`
	URL     string `toml:"url"`
	APIKey  string `toml:"api_key"`
}

type modelmanState struct {
	PriceRefreshLastRun string `toml:"price_refresh_last_run"`
	ModelState          map[string]struct {
		Exposed        bool `toml:"exposed"`
		LitellmExposed bool `toml:"litellm_exposed"` // back-compat read
		Ready          bool `toml:"ready"`
		Downloaded     bool `toml:"downloaded"`
	} `toml:"model_state"`
	Litellm LitellmState `toml:"litellm"`
}
```

Update `loadModelmanState` (`:32-62`) to return the third value and compute the effective exposed bit per entry (`Exposed || LitellmExposed`):

```go
func loadModelmanState(path string) (map[string]exposureEntry, LitellmState, error) {
	var raw modelmanState
	// ... existing decode ...
	out := make(map[string]exposureEntry, len(raw.ModelState))
	for id, e := range raw.ModelState {
		out[id] = exposureEntry{
			Exposed: e.Exposed || e.LitellmExposed,
			Ready:   e.Ready || e.Downloaded,
		}
	}
	return out, raw.Litellm, nil
}
```

(`exposureEntry` is the existing anonymous-struct-turned-named-type for the per-model map value; name it if it wasn't already named, keeping `LitellmExposed` field naming internal to this decode step only — nothing downstream ever reads `LitellmExposed` again.)

Update the one call site in `finalizeCfg` (`config.go:214-223`) to accept and store the new return value (`cfg.litellm = litellm` — an unexported field on `Config`, wired fully in Task 9; for this task it can be stored and unused, or a `_ = litellm` placeholder is **not allowed** by "no placeholders" — instead add the unexported field now: `type Config struct { ...; litellm LitellmState }` and assign it here, even though nothing reads it until Task 9).

- [ ] **Step 12: Update `IsExposed` and its test to use the renamed field**

In `config.go:367-379`, the `Config.exposed` map's value struct field `LitellmExposed` (`:123-126`) renames to `Exposed`:

```go
type exposureFlags struct {
	Exposed bool
	Ready   bool
}
```

`IsExposed` (`:367-379`) changes `c.exposed[m.ID].LitellmExposed` → `c.exposed[m.ID].Exposed`. Update `SetExposedForTest`/`ExposeAllForTest` (`:382,391`) to set `Exposed` instead of `LitellmExposed`.

In `wt/internal/config/modelman_test.go`, rename `TestLoadExposesOnlyLitellmExposedModels` → `TestLoadExposesOnlyExposedModels` (keep the same assertions, only the field/name change) and update `TestIsExposedPredicate` (`:318`) to use `Exposed`.

- [ ] **Step 13: Run the full Go test suite to verify Task 1 passes**

Run: `cd wt && go build ./... && go vet ./... && go test ./...`
Expected: PASS.

- [ ] **Step 14: Run the full cross-language verification**

Run: `make test-all`
Expected: PASS.

- [ ] **Step 15: Commit — ONE commit covering the fixture and both languages**

```bash
git add docs/contracts/modelman.sample.toml \
  modelman/src/modelman/state.py modelman/src/modelman/litellm.py \
  modelman/src/modelman/sync.py modelman/src/modelman/queue.py \
  modelman/src/modelman/screens/models.py modelman/src/modelman/benchmark/runner.py \
  modelman/tests/contracts/test_modelman_fixture.py modelman/tests/test_state.py \
  wt/internal/config/modelman.go wt/internal/config/config.go \
  wt/internal/config/modelman_fixture_test.go wt/internal/config/modelman_test.go
git commit -m "rename litellm_exposed to exposed, add [litellm] table - completes plan item #1"
```

---

### Task 2: Provider `protocols` + `base_origin` normalizer + Go secret resolver

**Files:**
- Modify: `docs/contracts/registry.sample.toml`
- Modify: `modelman/src/modelman/registry.py`
- Test: `modelman/tests/contracts/test_registry_fixture.py`
- Test: `modelman/tests/test_registry.py`
- Modify: `wt/internal/config/config.go`
- Modify: `wt/internal/config/protocol.go` (new)
- Test: `wt/internal/config/registry_fixture_test.go`
- Test: `wt/internal/config/config_test.go`

**Interfaces:**
- Consumes: nothing new from Task 1.
- Produces (Python): `Provider.protocols: list[str]` (default `["openai-chat"]`); `registry.base_origin(url: str | None) -> str | None`.
- Produces (Go): `type Protocol string` + `ProtocolAnthropic`, `ProtocolOpenAIChat`, `ProtocolOpenAIResponses`; `Provider.Protocols []Protocol`; `(Provider).EffectiveProtocols() []Protocol` (defaults to `[ProtocolOpenAIChat]`); `BaseOrigin(url string) string`; `ResolveSecret(ref string) string`.

**Note on scope:** only Go gets `ResolveSecret`. modelman never makes the outbound HTTP call to a provider directly — `litellm.py:277` already writes `provider.auth.secret_ref` verbatim into `config.yaml`, and LiteLLM's own proxy resolves an `os.environ/NAME`-shaped value there itself (that convention is where the polymorphism in the fixture comes from). wt is the one dialing providers directly in Task 4, so it's the one that needs to turn a `secret_ref` or `[litellm].api_key` string into an actual header value. Adding a resolver to modelman would be dead code.

- [ ] **Step 1: Update the registry contract fixture**

Edit `docs/contracts/registry.sample.toml`, adding `protocols` to the provider blocks and a header note:

```toml
# `base_url` is an origin (scheme://host:port) — no wire-protocol suffix.
# Readers normalize (`base_origin`/`BaseOrigin`) before use; drivers append
# their own suffix (e.g. "/v1"). Do not change existing base_url values —
# they are written verbatim into ~/.config/litellm/config.yaml.
#
# `protocols` declares which wire protocols this provider serves natively.
# Absent -> ["openai-chat"]. An agent that speaks none of a provider's
# protocols is routed through LiteLLM regardless of the on/off setting.

[[providers]]
id = "ollama"
name = "Ollama"
location = "local"
protocols = ["anthropic", "openai-chat"]

[providers.auth]
type = "none"
base_url = "http://localhost:11434"

[[providers]]
id = "openrouter"
name = "OpenRouter"
location = "cloud"
protocols = ["openai-chat"]

[providers.auth]
type = "secret_ref"
secret_ref = "OPENROUTER_API_KEY"
base_url = "https://openrouter.ai/api/v1"
```

Leave every other existing provider/model block in the fixture untouched apart from adding `protocols` where a provider block already exists.

- [ ] **Step 2: Update the Python registry fixture test (it will fail)**

Edit `modelman/tests/contracts/test_registry_fixture.py`, adding an assertion in the existing provider-loading test:

```python
def test_registry_providers_match_shared_fixture():
    """Pins that modelman decodes the new `protocols` array the same way
    wt does — wt's direct-routing decision depends on this list matching
    on both sides."""
    registry = load_registry(path=FIXTURE)
    ollama = next(p for p in registry.providers if p.id == "ollama")
    assert ollama.protocols == ["anthropic", "openai-chat"]

    openrouter = next(p for p in registry.providers if p.id == "openrouter")
    assert openrouter.protocols == ["openai-chat"]
```

- [ ] **Step 3: Run to verify failure**

Run: `cd modelman && uv run pytest tests/contracts/test_registry_fixture.py -v`
Expected: FAIL — `Provider` has no attribute `protocols`.

- [ ] **Step 4: Add `protocols` to the Provider dataclass and `base_origin` helper**

Edit `modelman/src/modelman/registry.py`. Find the `Provider`-equivalent dataclass (the one with `id`, `name`, `location`, `auth: AuthConfig`) and add:

```python
protocols: list[str] = field(default_factory=lambda: ["openai-chat"])
```

Parse it in the provider-loading function (wherever `id`, `name`, `location` are currently read from `[[providers]]`):

```python
protocols=provider_raw.get("protocols", ["openai-chat"]),
```

Add a module-level helper near the other TOML string helpers:

```python
def base_origin(url: str | None) -> str | None:
    """Normalize a stored base_url to a bare origin (no /v1 suffix) so
    readers can compare/derive endpoints without mutating the stored
    value — registry base_url values are written verbatim into LiteLLM's
    config.yaml and must never be rewritten in place."""
    if url is None:
        return None
    trimmed = url.rstrip("/")
    if trimmed.endswith("/v1"):
        trimmed = trimmed[: -len("/v1")].rstrip("/")
    return trimmed
```

- [ ] **Step 5: Run to verify Task 2's Python half passes**

Run: `cd modelman && uv run pytest -q`
Expected: PASS.

- [ ] **Step 6: Add a unit test for `base_origin`**

Edit `modelman/tests/test_registry.py`:

```python
def test_base_origin_strips_trailing_v1_and_slash():
    """wt and modelman must agree on how a stored base_url maps to a
    connectable origin regardless of whether the value was stored with or
    without a /v1 suffix (both shapes exist in the wild: ollama has
    neither, openrouter has /api/v1)."""
    assert base_origin("http://localhost:11434") == "http://localhost:11434"
    assert base_origin("https://openrouter.ai/api/v1") == "https://openrouter.ai/api"
    assert base_origin("https://openrouter.ai/api/v1/") == "https://openrouter.ai/api"
    assert base_origin(None) is None
```

- [ ] **Step 7: Run to verify it passes**

Run: `cd modelman && uv run pytest tests/test_registry.py -k base_origin -v`
Expected: PASS.

- [ ] **Step 8: Update the Go registry fixture test (it will fail)**

Edit `wt/internal/config/registry_fixture_test.go`, adding:

```go
// TestRegistryFixtureProviderProtocols pins that wt decodes the shared
// `protocols` array the same way modelman does; ResolveRoute's direct-vs-
// litellm decision (Task 5) depends on both languages agreeing on this.
func TestRegistryFixtureProviderProtocols(t *testing.T) {
	reg := loadRegistryFixture(t)
	ollama := findProvider(t, reg, "ollama")
	if got := ollama.EffectiveProtocols(); !equalProtocols(got, []Protocol{ProtocolAnthropic, ProtocolOpenAIChat}) {
		t.Errorf("ollama protocols = %v", got)
	}
	openrouter := findProvider(t, reg, "openrouter")
	if got := openrouter.EffectiveProtocols(); !equalProtocols(got, []Protocol{ProtocolOpenAIChat}) {
		t.Errorf("openrouter protocols = %v", got)
	}
}
```

(`findProvider`/`equalProtocols` are small test-local helpers if the file doesn't already have equivalents — add them alongside.)

- [ ] **Step 9: Run to verify failure**

Run: `cd wt && go test ./internal/config -run TestRegistryFixtureProviderProtocols -v`
Expected: FAIL — `Protocol` type and `EffectiveProtocols` don't exist yet.

- [ ] **Step 10: Add the `Protocol` type**

Create `wt/internal/config/protocol.go`:

```go
package config

// Protocol identifies a wire protocol an agent CLI or provider endpoint
// speaks. Empty-intersection between an agent's and a provider's protocol
// sets means the pairing cannot be dialed direct and must route through
// LiteLLM regardless of the on/off setting.
type Protocol string

const (
	ProtocolAnthropic       Protocol = "anthropic"
	ProtocolOpenAIChat      Protocol = "openai-chat"
	ProtocolOpenAIResponses Protocol = "openai-responses"
)
```

- [ ] **Step 11: Add `Provider.Protocols`, `EffectiveProtocols`, `BaseOrigin`, `ResolveSecret`**

Edit `wt/internal/config/config.go`. Add to the `Provider` struct (alongside `Auth`, `:75-76` area):

```go
Protocols []Protocol `toml:"protocols,omitempty"`
```

```go
// EffectiveProtocols returns the provider's declared protocols, defaulting
// to openai-chat when the registry entry predates this field.
func (p Provider) EffectiveProtocols() []Protocol {
	if len(p.Protocols) == 0 {
		return []Protocol{ProtocolOpenAIChat}
	}
	return p.Protocols
}

// BaseOrigin normalizes a stored base_url to a bare origin (no /v1
// suffix). Mirrors Python's registry.base_origin — the two must agree.
func BaseOrigin(url string) string {
	trimmed := strings.TrimRight(url, "/")
	trimmed = strings.TrimSuffix(trimmed, "/v1")
	return strings.TrimRight(trimmed, "/")
}

// ResolveSecret resolves a secret_ref-style value: "os.environ/NAME" or a
// bare "^[A-Z][A-Z0-9_]*$" name reads that env var; anything else is used
// verbatim. Shared by direct-mode provider auth and [litellm].api_key.
var envRefName = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

func ResolveSecret(ref string) string {
	if name, ok := strings.CutPrefix(ref, "os.environ/"); ok {
		return os.Getenv(name)
	}
	if envRefName.MatchString(ref) {
		return os.Getenv(ref)
	}
	return ref
}
```

(Add `"os"`, `"regexp"`, `"strings"` to imports if not already present.)

- [ ] **Step 12: Run to verify Task 2's Go half passes**

Run: `cd wt && go build ./... && go vet ./... && go test ./...`
Expected: PASS.

- [ ] **Step 13: Add unit tests for `BaseOrigin` and `ResolveSecret`**

Edit `wt/internal/config/config_test.go`:

```go
// TestBaseOriginTrimsV1Suffix pins the normalization rule readers apply to
// stored base_url values — the values themselves are never rewritten
// in-place (openrouter's /api/v1 must round-trip into LiteLLM's config.yaml
// unchanged), so any URL comparison/derivation goes through this function.
func TestBaseOriginTrimsV1Suffix(t *testing.T) {
	cases := map[string]string{
		"http://localhost:11434":       "http://localhost:11434",
		"https://openrouter.ai/api/v1": "https://openrouter.ai/api",
		"https://openrouter.ai/api/v1/": "https://openrouter.ai/api",
	}
	for in, want := range cases {
		if got := BaseOrigin(in); got != want {
			t.Errorf("BaseOrigin(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestResolveSecretEnvAndLiteral pins that a secret_ref/api_key value can
// be either a literal (today's live registry shape) or an env-var
// reference (the fixture's shape) — a wrong resolution here would send a
// stale or empty credential to a real provider.
func TestResolveSecretEnvAndLiteral(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-or-from-env")
	if got := ResolveSecret("os.environ/OPENROUTER_API_KEY"); got != "sk-or-from-env" {
		t.Errorf("os.environ/ form: got %q", got)
	}
	if got := ResolveSecret("OPENROUTER_API_KEY"); got != "sk-or-from-env" {
		t.Errorf("bare env-name form: got %q", got)
	}
	if got := ResolveSecret("sk-or-v1-literal"); got != "sk-or-v1-literal" {
		t.Errorf("literal form: got %q", got)
	}
}
```

- [ ] **Step 14: Run to verify they pass**

Run: `cd wt && go test ./internal/config -run 'TestBaseOriginTrimsV1Suffix|TestResolveSecretEnvAndLiteral' -v`
Expected: PASS.

- [ ] **Step 15: Full verification and commit**

Run: `make test-all`

```bash
git add docs/contracts/registry.sample.toml \
  modelman/src/modelman/registry.py modelman/tests/contracts/test_registry_fixture.py modelman/tests/test_registry.py \
  wt/internal/config/config.go wt/internal/config/protocol.go \
  wt/internal/config/registry_fixture_test.go wt/internal/config/config_test.go
git commit -m "add provider protocols, base_origin normalizer, secret resolver - completes plan item #2"
```

---

### Task 3: Introduce `Route` and change `Driver.Build`'s signature — pure refactor, no behavior change

**Files:**
- Modify: `wt/internal/config/config.go`
- Modify: `wt/internal/agents/agents.go`
- Modify: `wt/internal/agents/{claude,codex,copilot,opencode,pi,agy,shell}.go`
- Modify: `wt/internal/agents/pi_models.go`
- Test: `wt/internal/agents/gateway_matrix_test.go` (no assertion changes — this task must not change any test's expected values)

**Interfaces:**
- Consumes: `config.Protocol`, `config.BaseOrigin` (Task 2).
- Produces: `type Route struct { BaseOrigin string; APIKey string; ModelRef string; Display string; ProviderID string; Protocol Protocol; Litellm bool; Forced bool }`; `func (c *Config) ResolveRoute(m Model) (Route, error)` (agent-protocol parameter added in Task 5); `Driver.Build(m Model, yolo bool, r Route) LaunchCmd`.

**Why this task changes nothing observable:** `ResolveRoute`'s body in this task reproduces today's exact behavior — direct mode always uses `config.OllamaBaseURL` for every non-native model, litellm mode always uses `cfg.Gateway`. `gateway_matrix_test.go` must pass **unmodified**, proving the interface change alone didn't move behavior. Per-provider routing is Task 4.

- [ ] **Step 1: Run the existing driver matrix tests to record the baseline (must currently pass)**

Run: `cd wt && go test ./internal/agents -run TestGatewayMatrix -v`
Expected: PASS (baseline, before any change in this task).

- [ ] **Step 2: Add `Route` and a same-behavior `ResolveRoute` to `config.go`**

```go
// Route is everything a driver needs to dial one model for one launch,
// resolved once by ResolveRoute so drivers stop knowing about specific
// providers (e.g. ollama) or transports.
type Route struct {
	BaseOrigin string   // scheme://host:port, no wire-path suffix
	APIKey     string
	ModelRef   string   // m.ID via the proxy, m.ModelName direct — a property of the endpoint's own catalog
	Display    string   // m.ModelName, for catalog "name" fields
	ProviderID string   // registry provider id; meaningful only when !Litellm
	Protocol   Protocol
	Litellm    bool
	Forced     bool     // true if litellm was required regardless of the on/off setting (Task 5)
}

// ResolveRoute resolves the Route for launching model m. In this revision
// it reproduces the pre-refactor behavior exactly (ollama origin for every
// non-native model in direct mode, cfg.Gateway in litellm mode) — later
// tasks change the body without changing this signature.
func (c *Config) ResolveRoute(m Model) (Route, error) {
	if c.Gateway.IsLitellm() {
		return Route{
			BaseOrigin: c.Gateway.BaseURL(),
			APIKey:     c.Gateway.APIKey,
			ModelRef:   m.ID,
			Display:    m.ModelName,
			ProviderID: m.ProviderID,
			Litellm:    true,
		}, nil
	}
	return Route{
		BaseOrigin: OllamaBaseURL,
		ModelRef:   m.ModelName,
		Display:    m.ModelName,
		ProviderID: m.ProviderID,
		Litellm:    false,
	}, nil
}
```

- [ ] **Step 3: Change the `Driver` interface and delete `OllamaURLer`**

Edit `wt/internal/agents/agents.go`. Replace:

```go
type OllamaURLer interface {
	OllamaURL() string
}
```

— delete it entirely. Change the `Driver` interface's `Build` method (wherever `Driver` is declared, alongside `YoloFlag()`):

```go
type Driver interface {
	Build(m config.Model, yolo bool, r config.Route) LaunchCmd
	YoloFlag() string
}
```

Delete the `Gateway` alias (`:27`, `type Gateway = config.GatewayConfig`) — add instead `type Route = config.Route` for the same ergonomic reason (drivers refer to `Route` unqualified within the `agents` package).

In `BuildLaunchCmd` (`:264-302`), replace the `gw := Gateway{}; if cfg != nil { gw = cfg.Gateway }` snapshot with a resolved route:

```go
route, err := cfg.ResolveRoute(m)
if err != nil {
	return nil, err
}
```

and thread `route` through to `Command`/`Build` wherever `gw` was passed.

- [ ] **Step 4: Rewrite each driver's `Build` signature and body**

For each driver, replace the `gw Gateway` parameter with `r Route`, and replace every `gw.IsLitellm()`/`gw.BaseURL()`/`gw.APIKey`/`(driverName{}).OllamaURL()` reference with the equivalent `r` field, preserving today's exact behavior:

`claude.go` (`:58-74`):
```go
func (claudeDriver) Build(m config.Model, yolo bool, r Route) LaunchCmd {
	lc := LaunchCmd{Bin: "claude"}
	if yolo {
		lc.Args = append(lc.Args, claudeDriver{}.YoloFlag())
	}
	if m.Native {
		return lc
	}
	baseURL := r.BaseOrigin
	if !r.Litellm {
		// direct mode wire suffix for claude is empty
	}
	lc.Env = append(lc.Env,
		"ANTHROPIC_BASE_URL="+baseURL,
		"ANTHROPIC_AUTH_TOKEN="+authToken(r),
	)
	lc.Args = append(lc.Args, "--model", r.ModelRef)
	return lc
}

func authToken(r Route) string {
	if r.Litellm {
		return r.APIKey
	}
	return "ollama"
}
```

`copilot.go` (`:39-62`): same shape — `COPILOT_PROVIDER_BASE_URL = r.BaseOrigin + "/v1"`, `COPILOT_PROVIDER_API_KEY = r.APIKey` (empty string in direct mode, matching today), `COPILOT_MODEL = r.ModelRef`, `WIRE_API=completions` unchanged and unconditional.

`codex.go` (`:46-74`): `baseURL := r.BaseOrigin + "/v1/"`; the four `-c` overrides use `r.ModelRef` where `m.ModelName`/`m.ID` were used before, gated on `r.Litellm` exactly as today.

`opencode.go` (`:52-79`): **do not collapse the branches yet** — that's Task 4's job once `Route` carries enough information to unify them. For this task, keep the two branches but change their signature and internals to read from `r` instead of `gw`/`m` directly (litellm branch uses `r.BaseOrigin+"/v1"`, `r.APIKey`, `r.ModelRef`; direct branch calls a new small `directOllamaURL()` local helper that returns `OllamaBaseURL+"/v1"` — preserving exact today-behavior).

`pi.go` (`:44-51`) and `pi_models.go`: change `Build`'s signature to accept `r Route`; `syncDirectOllama` keeps its current name and ollama-only behavior for this task (renamed to `syncDirectProviders` only in Task 4, once it has more than one provider to fan out over).

`agy.go`, `shell.go`: signature-only change (`gw Gateway` → `r Route`), no body change — both already ignore routing entirely.

- [ ] **Step 5: Update the driver test files' call sites**

Each of `claude_test.go`, `codex_test.go`, `copilot_test.go`, `opencode_test.go`, `pi_models_test.go`, and `gateway_matrix_test.go` calls `driver.Build(m, yolo, gw)` — update every call site to construct a `Route` with the equivalent fields a `Gateway` would have produced (e.g. a litellm-mode test that built `Gateway{Mode: "litellm", URL: "...", APIKey: "..."}` now builds `Route{Litellm: true, BaseOrigin: "...", APIKey: "...", ModelRef: m.ID, ...}`). **Do not change any expected assertion values** — only the construction of the input.

- [ ] **Step 6: Run the full test suite to verify no behavior moved**

Run: `cd wt && go build ./... && go vet ./... && go test ./...`
Expected: PASS, with every existing assertion value unchanged from Step 1's baseline.

- [ ] **Step 7: Commit**

```bash
git add wt/internal/config/config.go wt/internal/agents/
git commit -m "introduce Route, change Driver.Build signature (no behavior change) - completes plan item #3"
```

---

### Task 4: Per-provider direct routing (the actual behavior change)

**Files:**
- Modify: `wt/internal/config/config.go` (`ResolveRoute` body only)
- Modify: `wt/internal/agents/opencode.go` (collapse to one branch)
- Modify: `wt/internal/agents/pi_models.go` (`syncDirectOllama` → `syncDirectProviders`)
- Modify: `wt/internal/agents/pi.go`
- Modify: `wt/internal/ollamacheck/ollamacheck.go`
- Modify: `wt/cmd/wt/launch.go`, `wt/internal/tui/app.go` (ollamacheck gate)
- Test: `wt/internal/agents/gateway_matrix_test.go` (assertions change — this is the point of the task)
- Test: `wt/internal/config/config_test.go`
- Test: `wt/internal/ollamacheck/ollamacheck_test.go`

**Interfaces:**
- Consumes: `Provider.EffectiveProtocols` (unused until Task 5, but `m.ProviderID` → registry provider lookup is added here), `BaseOrigin`, `ResolveSecret` (Task 2); `Route` (Task 3).
- Produces: `ResolveRoute` now errors on a direct-mode provider with no `base_url`; `ollamacheck.IsOllamaModel(m Model) bool`.

- [ ] **Step 1: Write the failing test for per-provider direct routing**

Edit `wt/internal/config/config_test.go`:

```go
// TestResolveRouteDirectUsesProviderBaseURL is the regression for the
// original bug this feature exists to fix: before this change, every
// non-ollama provider silently dialed localhost:11434 in direct mode.
func TestResolveRouteDirectUsesProviderBaseURL(t *testing.T) {
	cfg := &Config{
		Providers: []Provider{{
			ID: "openrouter",
			Auth: AuthConfig{Type: "secret_ref", SecretRef: "sk-or-v1-test", BaseURL: "https://openrouter.ai/api/v1"},
		}},
	}
	m := Model{ID: "openrouter/z-ai/glm-4.6", ModelName: "z-ai/glm-4.6", ProviderID: "openrouter"}

	route, err := cfg.ResolveRoute(m)
	if err != nil {
		t.Fatalf("ResolveRoute: %v", err)
	}
	if route.BaseOrigin != "https://openrouter.ai/api" {
		t.Errorf("BaseOrigin = %q, want https://openrouter.ai/api", route.BaseOrigin)
	}
	if route.APIKey != "sk-or-v1-test" {
		t.Errorf("APIKey = %q", route.APIKey)
	}
	if route.ModelRef != "z-ai/glm-4.6" {
		t.Errorf("ModelRef = %q, want bare ModelName in direct mode", route.ModelRef)
	}
}

// TestResolveRouteMissingBaseURLErrors ensures a provider with no
// base_url fails at launch with an actionable message, instead of the
// pre-fix behavior of silently misrouting to ollama's port.
func TestResolveRouteMissingBaseURLErrors(t *testing.T) {
	cfg := &Config{
		Providers: []Provider{{ID: "omlx", Auth: AuthConfig{Type: "none"}}},
	}
	m := Model{ID: "omlx/some-model", ModelName: "some-model", ProviderID: "omlx"}

	_, err := cfg.ResolveRoute(m)
	if err == nil {
		t.Fatal("expected an error for a provider with no base_url in direct mode")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd wt && go test ./internal/config -run 'TestResolveRouteDirectUsesProviderBaseURL|TestResolveRouteMissingBaseURLErrors' -v`
Expected: FAIL — direct mode still hardcodes `OllamaBaseURL`.

- [ ] **Step 3: Rewrite `ResolveRoute`'s direct-mode body**

```go
func (c *Config) ResolveRoute(m Model) (Route, error) {
	if m.Native {
		return Route{}, nil // drivers never call this for native models
	}
	if c.Gateway.IsLitellm() {
		return Route{
			BaseOrigin: c.Gateway.BaseURL(),
			APIKey:     c.Gateway.APIKey,
			ModelRef:   m.ID,
			Display:    m.ModelName,
			ProviderID: m.ProviderID,
			Litellm:    true,
		}, nil
	}

	provider, ok := c.providerByID(m.ProviderID)
	if !ok {
		return Route{}, fmt.Errorf("direct routing: unknown provider %q for model %q", m.ProviderID, m.ID)
	}
	if provider.Auth.BaseURL == "" {
		return Route{}, fmt.Errorf(
			"direct routing: provider %q has no auth.base_url in registry.toml — "+
				"set one, or enable the proxy with 'modelman litellm on'", m.ProviderID)
	}
	apiKey := ""
	if provider.Auth.SecretRef != "" {
		apiKey = ResolveSecret(provider.Auth.SecretRef)
	}
	return Route{
		BaseOrigin: BaseOrigin(provider.Auth.BaseURL),
		APIKey:     apiKey,
		ModelRef:   m.ModelName,
		Display:    m.ModelName,
		ProviderID: m.ProviderID,
		Litellm:    false,
	}, nil
}
```

(`providerByID` is a small unexported lookup helper over `c.Providers` — add it if it doesn't already exist; several call sites likely already do this linear scan inline.)

- [ ] **Step 4: Run to verify Step 1's tests pass**

Run: `cd wt && go test ./internal/config -run 'TestResolveRouteDirectUsesProviderBaseURL|TestResolveRouteMissingBaseURLErrors' -v`
Expected: PASS.

- [ ] **Step 5: Fix `ollamacheck`'s misleading provider guard and doc comment**

Edit `wt/internal/ollamacheck/ollamacheck.go`. `Check` (`:13-24`) already returns `(true, nil)` for non-ollama providers — that's correct behavior (it should only check ollama), but its doc comment and the callers' gate comments wrongly imply it protects every provider. Add a named helper the call sites can use instead of duplicating the provider-id check inline:

```go
// IsOllamaModel reports whether m is served by the ollama provider — the
// only provider Check actually probes. Check silently passes any other
// provider; callers must not assume it validates reachability for them.
func IsOllamaModel(m config.Model) bool {
	return m.ProviderID == "ollama"
}
```

- [ ] **Step 6: Narrow the pre-launch gate to reflect what it actually protects**

Edit `wt/cmd/wt/launch.go:143-162` and `wt/internal/tui/app.go:421-435`. Both currently skip the ollama check when `cfg.Gateway.IsLitellm()`. Change the condition to also require the model actually be ollama-served, since a non-ollama direct-mode model was never protected by this check and skipping it makes that explicit:

```go
if route.Litellm || !ollamacheck.IsOllamaModel(m) {
	// skip: routed through the proxy, or not an ollama-served model
} else if ok, err := ollamacheck.Check(m); !ok {
	// existing error handling
}
```

(Adjust to match the exact existing control-flow shape at each call site — the semantic change is the added `!ollamacheck.IsOllamaModel(m)` condition.)

- [ ] **Step 7: Rewrite the gateway matrix test's rationale and assertions**

Edit `wt/internal/agents/gateway_matrix_test.go`. The comment at `:23-29` claims direct×llamacpp/omlx/openrouter is "unreachable in production" because ollamacheck rejects it — that premise was false and this task makes it moot by adding real routing. Replace the comment:

```go
// matrixModels covers every provider auth shape wt must route: ollama
// (direct + litellm), openrouter (secret_ref, cloud), and omlx/llamacpp
// (local OpenAI-compatible, no auth). Direct-mode assertions below pin
// ResolveRoute's per-provider base_url resolution — a regression here
// previously meant every non-ollama model silently misrouted to
// localhost:11434 with no error (fixed by ResolveRoute's provider lookup).
```

Add assertions to the existing `TestGatewayMatrix{Claude,Copilot,OpenCode}` direct-mode cases for the `openrouter` and `omlx` matrix models, asserting the driver's emitted base URL/env var equals the provider's `base_url` (normalized), not `localhost:11434`.

- [ ] **Step 8: Run the full matrix and verify the new assertions pass**

Run: `cd wt && go test ./internal/agents -run TestGatewayMatrix -v`
Expected: PASS with the new per-provider assertions.

- [ ] **Step 9: Fan out pi's direct-mode sync per provider**

Edit `wt/internal/agents/pi_models.go`. Rename `syncDirectOllama` → `syncDirectProviders` and change it to group `cfg.Models` (non-native) by `ProviderID`, upserting one pi provider entry per registry provider using each provider's resolved `Route`-equivalent base URL:

```go
// syncDirectProviders writes one pi provider entry per registry provider
// id (not just ollama) so a direct-mode launch of, e.g., an openrouter
// model resolves against openrouter's own endpoint instead of being
// folded into the "ollama" provider under a bare, possibly-slashed model
// name — pi splits --model on the first slash, so an unqualified
// "z-ai/glm-4.6" previously resolved provider "z-ai" silently.
func syncDirectProviders(cfg *config.Config) (piConfig, error) {
	byProvider := map[string][]config.Model{}
	for _, m := range cfg.Models {
		if m.Native {
			continue
		}
		byProvider[m.ProviderID] = append(byProvider[m.ProviderID], m)
	}
	providers := map[string]piProviderEntry{}
	for providerID, models := range byProvider {
		provider, ok := cfg.ProviderByID(providerID)
		if !ok || provider.Auth.BaseURL == "" {
			continue // unroutable in direct mode; surfaced by ResolveRoute at launch time
		}
		apiKey := "not-needed" // empty apiKey invalidates pi's whole models.json
		if provider.Auth.SecretRef != "" {
			apiKey = config.ResolveSecret(provider.Auth.SecretRef)
		}
		entry := piProviderEntry{
			BaseURL: config.BaseOrigin(provider.Auth.BaseURL) + "/v1",
			APIKey:  apiKey,
			API:     "openai-completions",
			Models:  map[string]piModelEntry{},
		}
		for _, m := range models {
			entry.Models[m.ModelName] = piModelEntry{Name: m.ModelName}
		}
		providers[providerID] = entry
	}
	return piConfig{Providers: providers}, nil
}
```

(`ProviderByID` should be exported from `config` if `pi_models.go` needs it from outside the package — otherwise reuse whatever existing accessor the file already has for provider lookup; align naming with `providerByID` added in Step 3 of this task, exporting it if cross-package access is required.)

Update `piDriver.Build` (`pi.go:44-51`) to send `r.ProviderID + "/" + r.ModelRef"` as the direct-mode `--model` argument instead of the bare `r.ModelRef`.

- [ ] **Step 10: Write the regression test for the pi slash bug**

Edit `wt/internal/agents/pi_models_test.go`:

```go
// TestPiDirectModelArgPrefixesProvider is the regression for a live bug:
// pi splits --model on the first slash to find the provider, so an
// unqualified model name containing a slash (e.g. openrouter's
// "z-ai/glm-4.6") was silently resolved against provider "z-ai" instead
// of "openrouter".
func TestPiDirectModelArgPrefixesProvider(t *testing.T) {
	m := config.Model{ID: "openrouter/z-ai/glm-4.6", ModelName: "z-ai/glm-4.6", ProviderID: "openrouter"}
	r := config.Route{ProviderID: "openrouter", ModelRef: "z-ai/glm-4.6", Litellm: false}

	lc := piDriver{}.Build(m, false, r)
	if !argsContain(lc.Args, "--model", "openrouter/z-ai/glm-4.6") {
		t.Errorf("args = %v, want --model openrouter/z-ai/glm-4.6", lc.Args)
	}
}
```

(`argsContain` — reuse an existing test helper in the package, or add a two-line one if none exists.)

- [ ] **Step 11: Run to verify it passes**

Run: `cd wt && go test ./internal/agents -run TestPiDirectModelArgPrefixesProvider -v`
Expected: PASS.

- [ ] **Step 12: Collapse opencode's two branches into one**

Edit `wt/internal/agents/opencode.go`. Replace the `if gw.IsLitellm() { ... } else { ... }` split (`:52-79`) with a single path — both branches already declare a custom `@ai-sdk/openai-compatible` provider; the only differences were the endpoint, key, and provider id:

```go
func (opencodeDriver) Build(m config.Model, yolo bool, r Route) LaunchCmd {
	lc := LaunchCmd{Bin: "opencode"}
	if yolo {
		lc.Args = append(lc.Args, opencodeDriver{}.YoloFlag())
	}
	baseURL := r.BaseOrigin + "/v1"
	modelRef := opencodeGatewayProviderID + "/" + r.ModelRef
	lc.Env = append(lc.Env, "OPENCODE_CONFIG_CONTENT="+fmt.Sprintf(
		`{"model":%q,"small_model":%q,"provider":{%q:{"npm":"@ai-sdk/openai-compatible","name":"Agent WT Gateway","options":{"baseURL":%q,"apiKey":%q},"models":{%q:{"name":%q}}}}}`,
		modelRef, modelRef, opencodeGatewayProviderID, baseURL, r.APIKey, r.ModelRef, r.Display,
	))
	return lc
}
```

This drops the dependency on opencode's builtin `ollama` provider catalog entirely (the reason the old direct branch existed was to avoid `ProviderModelNotFoundError` from the builtin catalog — declaring the model in the custom provider's own `models` map, as the litellm branch already did, sidesteps that catalog in both modes) and fixes slashed openrouter names the same way pi's fix does.

- [ ] **Step 13: Run the opencode driver tests**

Run: `cd wt && go test ./internal/agents -run TestOpenCode -v`
Expected: PASS. Update any test asserting the old builtin-`ollama` JSON shape for direct mode to expect the unified `agent-wt` shape instead.

- [ ] **Step 14: Full verification**

Run: `cd wt && go build ./... && go vet ./... && go test ./... && make lint`

- [ ] **Step 15: Commit**

```bash
git add wt/internal/config/config.go wt/internal/agents/ wt/internal/ollamacheck/ wt/cmd/wt/launch.go wt/internal/tui/app.go
git commit -m "resolve direct routes per-provider instead of hardcoding ollama - completes plan item #4"
```

---

### Task 5: Protocol negotiation and forced-litellm

**Files:**
- Modify: `wt/internal/config/config.go` (`ResolveRoute` gains an `agentProtocols` parameter)
- Modify: `wt/internal/agents/agents.go` (`ProtocolDeclarer`, `BuildLaunchCmd` wiring, stderr notice)
- Modify: `wt/internal/agents/{claude,codex,copilot,opencode,pi}.go` (`Protocols()` method each)
- Test: `wt/internal/config/config_test.go`
- Test: `wt/internal/agents/gateway_matrix_test.go`

**Interfaces:**
- Consumes: `Provider.EffectiveProtocols` (Task 2), `ResolveRoute` (Task 4).
- Produces: `ResolveRoute(m Model, agentProtocols []Protocol) (Route, error)` — **signature change** from Task 4; `type ProtocolDeclarer interface { Protocols() []Protocol }`; `Route.Forced bool` becomes meaningful.

- [ ] **Step 1: Write the failing negotiation tests**

Edit `wt/internal/config/config_test.go`:

```go
// TestClaudeForcesLitellmForOpenRouter: claude only speaks anthropic;
// openrouter only serves openai-chat. The empty intersection must force
// litellm even when the user has it switched off, rather than launching
// claude against an endpoint it cannot speak to.
func TestClaudeForcesLitellmForOpenRouter(t *testing.T) {
	cfg := &Config{Providers: []Provider{{
		ID:   "openrouter",
		Auth: AuthConfig{Type: "secret_ref", SecretRef: "k", BaseURL: "https://openrouter.ai/api/v1"},
	}}}
	m := Model{ID: "openrouter/z-ai/glm-4.6", ModelName: "z-ai/glm-4.6", ProviderID: "openrouter"}

	route, err := cfg.ResolveRoute(m, []Protocol{ProtocolAnthropic})
	if err != nil {
		t.Fatalf("ResolveRoute: %v", err)
	}
	if !route.Litellm || !route.Forced {
		t.Errorf("route = %+v, want Litellm=true Forced=true", route)
	}
}

// TestCodexForcesLitellmWhenProviderLacksResponses: codex speaks only
// openai-responses, and no local provider serves it — codex has no
// working direct path at all, and must always be routed through the
// proxy regardless of the on/off setting.
func TestCodexForcesLitellmWhenProviderLacksResponses(t *testing.T) {
	cfg := &Config{Providers: []Provider{{
		ID:   "ollama",
		Auth: AuthConfig{Type: "none", BaseURL: "http://localhost:11434"},
	}}}
	m := Model{ID: "ollama/qwen3.8:27b-mlx", ModelName: "qwen3.8:27b-mlx", ProviderID: "ollama"}

	route, err := cfg.ResolveRoute(m, []Protocol{ProtocolOpenAIResponses})
	if err != nil {
		t.Fatalf("ResolveRoute: %v", err)
	}
	if !route.Forced {
		t.Errorf("route = %+v, want Forced=true", route)
	}
}

// TestDirectRouteWhenProtocolsOverlap: when the intersection is non-empty
// and litellm is off, the pairing must dial direct — forced-litellm must
// not over-trigger for pairings that actually work (e.g. copilot+ollama).
func TestDirectRouteWhenProtocolsOverlap(t *testing.T) {
	cfg := &Config{Providers: []Provider{{
		ID:   "ollama",
		Auth: AuthConfig{Type: "none", BaseURL: "http://localhost:11434"},
	}}}
	m := Model{ID: "ollama/qwen3.8:27b-mlx", ModelName: "qwen3.8:27b-mlx", ProviderID: "ollama"}

	route, err := cfg.ResolveRoute(m, []Protocol{ProtocolOpenAIChat})
	if err != nil {
		t.Fatalf("ResolveRoute: %v", err)
	}
	if route.Litellm || route.Forced {
		t.Errorf("route = %+v, want a direct, non-forced route", route)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd wt && go test ./internal/config -run 'TestClaudeForcesLitellmForOpenRouter|TestCodexForcesLitellmWhenProviderLacksResponses|TestDirectRouteWhenProtocolsOverlap' -v`
Expected: FAIL — `ResolveRoute` doesn't take an `agentProtocols` parameter yet.

- [ ] **Step 3: Add negotiation to `ResolveRoute`**

```go
func (c *Config) ResolveRoute(m Model, agentProtocols []Protocol) (Route, error) {
	if m.Native {
		return Route{}, nil
	}

	provider, ok := c.providerByID(m.ProviderID)
	if !ok {
		return Route{}, fmt.Errorf("unknown provider %q for model %q", m.ProviderID, m.ID)
	}

	common := intersectProtocols(agentProtocols, provider.EffectiveProtocols())
	forced := len(common) == 0
	useLitellm := forced || c.Gateway.IsLitellm()

	if useLitellm {
		if c.Gateway.BaseURL() == "" {
			return Route{}, fmt.Errorf(
				"litellm routing is required for this model but no URL is configured — "+
					"run 'modelman litellm set --url ... --api-key ...' or 'modelman litellm on'")
		}
		return Route{
			BaseOrigin: c.Gateway.BaseURL(),
			APIKey:     c.Gateway.APIKey,
			ModelRef:   m.ID,
			Display:    m.ModelName,
			ProviderID: m.ProviderID,
			Litellm:    true,
			Forced:     forced,
		}, nil
	}

	if provider.Auth.BaseURL == "" {
		return Route{}, fmt.Errorf(
			"direct routing: provider %q has no auth.base_url in registry.toml — "+
				"set one, or enable the proxy with 'modelman litellm on'", m.ProviderID)
	}
	apiKey := ""
	if provider.Auth.SecretRef != "" {
		apiKey = ResolveSecret(provider.Auth.SecretRef)
	}
	return Route{
		BaseOrigin: BaseOrigin(provider.Auth.BaseURL),
		APIKey:     apiKey,
		ModelRef:   m.ModelName,
		Display:    m.ModelName,
		ProviderID: m.ProviderID,
		Protocol:   common[0],
		Litellm:    false,
	}, nil
}

func intersectProtocols(a, b []Protocol) []Protocol {
	set := make(map[Protocol]bool, len(b))
	for _, p := range b {
		set[p] = true
	}
	var out []Protocol
	for _, p := range a {
		if set[p] {
			out = append(out, p)
		}
	}
	return out
}
```

- [ ] **Step 4: Run to verify Step 1's tests pass**

Run: `cd wt && go test ./internal/config -run 'TestClaudeForcesLitellmForOpenRouter|TestCodexForcesLitellmWhenProviderLacksResponses|TestDirectRouteWhenProtocolsOverlap' -v`
Expected: PASS.

- [ ] **Step 5: Declare each agent's protocols and wire `BuildLaunchCmd`**

Edit `wt/internal/agents/agents.go`. Add the `Protocol` alias next to the existing `type Route = config.Route` (Task 3) — both exist so driver files in this package can write `Protocol`/`Route` unqualified while the constants (`config.ProtocolAnthropic`, etc.) stay qualified, which is valid Go since an alias is the same type, not a new one:

```go
type Protocol = config.Protocol

// ProtocolDeclarer is an optional Driver capability naming the wire
// protocols the agent CLI speaks, ordered by preference. Drivers without
// it (agy, shell) never route a model — agy's models are always native,
// shell never resolves a route.
type ProtocolDeclarer interface {
	Protocols() []Protocol
}

// ProtocolsFor returns the named agent's declared protocols, or nil if the
// agent is unknown or its driver doesn't implement ProtocolDeclarer. Used
// by BuildLaunchCmd (to resolve the actual launch route) and by the model
// picker (Task 6, to annotate rows before launch) — both need the same
// answer to "what can this agent dial directly", so it lives in one place.
func ProtocolsFor(agent string) []Protocol {
	driver, ok := ByName(agent)
	if !ok {
		return nil
	}
	if pd, ok := driver.(ProtocolDeclarer); ok {
		return pd.Protocols()
	}
	return nil
}
```

(`ByName` is the existing picker-catalog lookup named in `wt/CLAUDE.md`'s driver-abstraction section, alongside `ListEntries`/`IssueFor`/`IsCommand`/`Names`/`Installed` — reuse it rather than adding a second lookup.)

Add one line per driver:
- `claude.go`: `func (claudeDriver) Protocols() []Protocol { return []Protocol{config.ProtocolAnthropic} }`
- `codex.go`: `func (codexDriver) Protocols() []Protocol { return []Protocol{config.ProtocolOpenAIResponses} }`
- `copilot.go`: `func (copilotDriver) Protocols() []Protocol { return []Protocol{config.ProtocolOpenAIChat} }`
- `opencode.go`: `func (opencodeDriver) Protocols() []Protocol { return []Protocol{config.ProtocolOpenAIChat} }`
- `pi.go`: `func (piDriver) Protocols() []Protocol { return []Protocol{config.ProtocolOpenAIChat} }`

In `BuildLaunchCmd` (`:264-302`), resolve the route using `ProtocolsFor` and print the forced-litellm notice:

```go
route, err := cfg.ResolveRoute(m, ProtocolsFor(agent))
if err != nil {
	return nil, err
}
if route.Forced {
	fmt.Fprintf(os.Stderr, "wt: %s requires LiteLLM for %s (no direct protocol overlap with provider %q) — routing through the proxy\n", agent, m.ID, m.ProviderID)
}
```

- [ ] **Step 6: Run the full test suite**

Run: `cd wt && go build ./... && go vet ./... && go test ./...`
Expected: PASS.

- [ ] **Step 7: Extend the gateway matrix to assert forced-litellm for codex**

Edit `wt/internal/agents/gateway_matrix_test.go`, adding a codex case (if not already present) asserting that with `[gateway]`/litellm off, `cfg.ResolveRoute(m, codexDriver{}.Protocols())` still returns `Litellm: true, Forced: true` for every matrix model — codex has no direct path under any provider in the matrix.

Run: `cd wt && go test ./internal/agents -run TestGatewayMatrix -v`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add wt/internal/config/config.go wt/internal/agents/
git commit -m "add protocol negotiation and forced-litellm routing - completes plan item #5"
```

---

### Task 6: Model picker — footer + exception markers

**Files:**
- Modify: `wt/internal/tui/model_list.go`
- Modify: `wt/internal/tui/app.go`
- Test: `wt/internal/tui/model_list_test.go`

**Interfaces:**
- Consumes: `agents.ProtocolsFor(agent string) []config.Protocol` (Task 5); `cfg.ResolveRoute(m, protocols)` (Task 5). Footer text uses `cfg.Gateway.IsLitellm()` directly since the `[litellm]`-backed `cfg.IsLitellm()` method doesn't exist until Task 9 — the per-row `exception` marking is unaffected either way, since it comes from `ResolveRoute`'s `Forced`/error result, not from the footer's mode string.
- Produces: `buildModelItems` sets a per-row `exception string` field (e.g. `"(via proxy)"`, `"(unavailable: <reason>)"`); the picker footer renders the current mode.

- [ ] **Step 1: Write the failing test**

Edit `wt/internal/tui/model_list_test.go`:

```go
// TestBuildModelItemsMarksOnlyDeviatingRows: the picker must stay quiet
// for rows whose transport matches the current mode, and mark only rows
// that deviate (forced through the proxy) or cannot launch — a per-row
// transport column on every row would be noise when transport is uniform,
// and asserting against buildModelItems directly avoids coupling this
// test to lipgloss border/padding output (wt/CLAUDE.md).
func TestBuildModelItemsMarksOnlyDeviatingRows(t *testing.T) {
	cfg := directOnlyTestConfig(t) // helper: litellm off, one ollama + one openrouter model, agent = codex
	items := buildModelItems(cfg, "codex", []config.Model{ollamaTestModel, openrouterTestModel}, nil)

	var ollamaItem, openrouterItem modelItem
	for _, it := range items {
		switch it.model.ProviderID {
		case "ollama":
			ollamaItem = it
		case "openrouter":
			openrouterItem = it
		}
	}
	if ollamaItem.exception == "" {
		t.Error("codex+ollama has no direct path (codex speaks only openai-responses) and should be marked")
	}
	if openrouterItem.exception == "" {
		t.Error("codex+openrouter has no direct path and should be marked")
	}
}
```

(`directOnlyTestConfig`, `ollamaTestModel`, `openrouterTestModel` — small test fixtures colocated in the test file, built the same way existing `internal/tui` tests construct a minimal `config.Config`.)

- [ ] **Step 2: Run to verify failure**

Run: `cd wt && go test ./internal/tui -run TestBuildModelItemsMarksOnlyDeviatingRows -v`
Expected: FAIL — `modelItem` has no `exception` field.

- [ ] **Step 3: Add the `exception` field and populate it**

Edit `wt/internal/tui/model_list.go`. Add to the `modelItem` struct: `exception string`. In `buildModelItems`, after resolving each row's route using `agents.ProtocolsFor(agent)` (the same helper `BuildLaunchCmd` uses, added in Task 5 — `agent` is the already-resolved agent name, already a parameter of `buildModelItems`'s caller per the phase order worktree→agent→model):

```go
route, err := cfg.ResolveRoute(m, agents.ProtocolsFor(agent))
switch {
case err != nil:
	item.exception = "(unavailable)"
case route.Forced:
	item.exception = "(via proxy)"
}
```

In `Title()`, append `" "+i.exception` after the existing segments when non-empty (plain ASCII, matching the existing ASCII-only rotation-marker convention).

- [ ] **Step 4: Add the mode footer**

In the picker view construction (wherever the model list's static header/footer is rendered — colocated with the family/rotation footer already in `model_list.go` or `app.go`), add one line:

```go
mode := "LiteLLM: off (direct)"
if cfg.Gateway.IsLitellm() {
	mode = "LiteLLM: on"
}
```

(Replace `cfg.Gateway.IsLitellm()` with `cfg.IsLitellm()` once Task 9 introduces it — tracked as a follow-up edit in Task 9's steps.)

- [ ] **Step 5: Run to verify the test passes**

Run: `cd wt && go test ./internal/tui -run TestBuildModelItemsMarksOnlyDeviatingRows -v`
Expected: PASS.

- [ ] **Step 6: Full verification**

Run: `cd wt && go build ./... && go vet ./... && go test ./...`

- [ ] **Step 7: Commit**

```bash
git add wt/internal/tui/
git commit -m "annotate picker rows that deviate from the current litellm mode - completes plan item #6"
```

---

### Task 7: modelman `litellm` control CLI + TUI affordance

**Files:**
- Modify: `modelman/src/modelman/main.py`
- Modify: `modelman/src/modelman/screens/families.py`
- Test: `modelman/tests/test_litellm_cli.py` (new)

**Interfaces:**
- Consumes: `StateStore.litellm`, `LitellmState` (Task 1).
- Produces: `modelman litellm status|on|off|set --url --api-key` CLI commands.

- [ ] **Step 1: Write the failing CLI tests**

Create `modelman/tests/test_litellm_cli.py`:

```python
"""modelman litellm CLI: on/off/set/status must mutate only the [litellm]
table via locked_state, never touch model_state or restart the proxy — a
routing toggle that also restarted the shared LiteLLM service would turn a
config change into a brief service outage for every other user of it."""
from typer.testing import CliRunner
from modelman.main import app
from modelman.state import load_state

runner = CliRunner()


def test_litellm_on_sets_enabled_true(tmp_path, monkeypatch):
    state_path = tmp_path / "modelman.toml"
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    result = runner.invoke(app, ["litellm", "on"])
    assert result.exit_code == 0
    assert load_state(path=state_path).litellm.enabled is True


def test_litellm_off_sets_enabled_false(tmp_path, monkeypatch):
    state_path = tmp_path / "modelman.toml"
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    runner.invoke(app, ["litellm", "on"])
    result = runner.invoke(app, ["litellm", "off"])
    assert result.exit_code == 0
    assert load_state(path=state_path).litellm.enabled is False


def test_litellm_set_writes_url_and_key(tmp_path, monkeypatch):
    state_path = tmp_path / "modelman.toml"
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    result = runner.invoke(app, ["litellm", "set", "--url", "http://localhost:4000", "--api-key", "sk-test"])
    assert result.exit_code == 0
    state = load_state(path=state_path)
    assert state.litellm.url == "http://localhost:4000"
    assert state.litellm.api_key == "sk-test"


def test_litellm_status_redacts_key(tmp_path, monkeypatch):
    state_path = tmp_path / "modelman.toml"
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    runner.invoke(app, ["litellm", "set", "--url", "http://localhost:4000", "--api-key", "sk-secret-value"])
    result = runner.invoke(app, ["litellm", "status"])
    assert "sk-secret-value" not in result.stdout


def test_litellm_commands_never_restart_proxy(tmp_path, monkeypatch):
    state_path = tmp_path / "modelman.toml"
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    calls = []
    monkeypatch.setattr("modelman.litellm.restart_litellm_proxy", lambda *a, **k: calls.append(1))
    runner.invoke(app, ["litellm", "on"])
    runner.invoke(app, ["litellm", "off"])
    runner.invoke(app, ["litellm", "set", "--url", "http://x", "--api-key", "k"])
    assert calls == []
```

- [ ] **Step 2: Run to verify failure**

Run: `cd modelman && uv run pytest tests/test_litellm_cli.py -v`
Expected: FAIL — no `litellm` sub-app registered.

- [ ] **Step 3: Add the `litellm_app` sub-app**

Edit `modelman/src/modelman/main.py`, following the existing `benchmark_app`/`usage_app` registration pattern (`:29-30`):

```python
litellm_app = typer.Typer(help="Control whether wt routes agents through LiteLLM or dials providers directly. Never starts or stops the proxy service.")
app.add_typer(litellm_app, name="litellm")


@litellm_app.command("status")
def litellm_status():
    """Show the current [litellm] routing state. Does not touch the proxy."""
    state = load_state()
    mode = "on" if state.litellm.enabled else "off"
    key_display = "(unset)" if not state.litellm.api_key else "***" + state.litellm.api_key[-4:]
    typer.echo(f"litellm: {mode}")
    typer.echo(f"  url: {state.litellm.url or '(unset)'}")
    typer.echo(f"  api_key: {key_display}")


@litellm_app.command("on")
def litellm_on():
    """Route non-native models through LiteLLM. Does not start the proxy."""
    with locked_state() as state:
        state.litellm.enabled = True
    typer.echo("litellm: on")


@litellm_app.command("off")
def litellm_off():
    """Dial providers directly where possible. Does not stop the proxy."""
    with locked_state() as state:
        state.litellm.enabled = False
    typer.echo("litellm: off")


@litellm_app.command("set")
def litellm_set(
    url: str = typer.Option(None, help="LiteLLM proxy base URL"),
    api_key: str = typer.Option(None, "--api-key", help="LiteLLM proxy API key"),
):
    """Set the proxy URL/key wt will use when routing through LiteLLM."""
    with locked_state() as state:
        if url is not None:
            state.litellm.url = url
        if api_key is not None:
            state.litellm.api_key = api_key
    typer.echo("litellm: updated")
```

(`locked_state()` is the existing context manager from `state.py:151-170` — confirm its exact usage shape against an existing caller such as `pricing.py` before matching it here.)

- [ ] **Step 4: Run to verify Step 1's tests pass**

Run: `cd modelman && uv run pytest tests/test_litellm_cli.py -v`
Expected: PASS.

- [ ] **Step 5: Add the TUI keybinding and footer**

Edit `modelman/src/modelman/screens/families.py`. Add `l` to `BINDINGS` (`:40-47`, where `a/e/d/enter/g/q` are taken):

```python
Binding("l", "toggle_litellm", "LiteLLM"),
```

Add the action, reusing the `ConfirmModal` pattern from `screens/forms.py`:

```python
def action_toggle_litellm(self) -> None:
    with locked_state() as state:
        state.litellm.enabled = not state.litellm.enabled
    self.refresh_footer()  # or however this screen already triggers a footer re-render
```

Render the current state in the screen's footer/status area (wherever `families.py` already shows persistent status text):

```python
mode = "on" if self.app_state.litellm.enabled else "off"
url_display = f" ({self.app_state.litellm.url})" if mode == "on" and self.app_state.litellm.url else ""
f"LiteLLM: {mode}{url_display}"
```

- [ ] **Step 6: Manual verification (TUI, no automated test)**

Run: `cd modelman && uv run modelman` — navigate to the families screen, press `l`, confirm the footer toggles and `~/.config/local-ai/modelman.toml`'s `[litellm].enabled` flips (point `MODELMAN_STATE` at a scratch file first if testing against a real config is undesirable).

- [ ] **Step 7: Full verification and commit**

Run: `cd modelman && make check && make test`

```bash
git add modelman/src/modelman/main.py modelman/src/modelman/screens/families.py modelman/tests/test_litellm_cli.py
git commit -m "add modelman litellm status/on/off/set CLI and TUI toggle - completes plan item #7"
```

---

### Task 8: `modelman migrate` imports wt's `[gateway]`; omlx registry backfill

**Files:**
- Modify: `modelman/src/modelman/migrate.py`
- Modify: `modelman/src/modelman/registry.py` (`_DEFAULT_PROVIDER_TEMPLATES["omlx"]`)
- Modify: `modelman/src/modelman/sync.py` (backfill repair)
- Test: `modelman/tests/test_migrate.py`
- Test: `modelman/tests/test_sync.py`

**Interfaces:**
- Consumes: `LitellmState`, `locked_state()` (Task 1); `registry.base_origin` (Task 2, for backfill parity checks only — the backfill itself writes the template's stored value as-is).
- Produces: `migrate_wt_gateway_to_litellm(wt_config_path=None) -> bool` (returns whether an import happened); `sync.backfill_provider_defaults(registry) -> Registry` (fills only missing fields).

- [ ] **Step 1: Write the failing migration test**

Edit `modelman/tests/test_migrate.py`:

```python
def test_migrate_imports_wt_gateway_into_litellm_table(tmp_path, monkeypatch):
    """wt cannot write modelman.toml, so the one-time move of an existing
    [gateway] block (mode/url/api_key) into modelman's [litellm] table has
    to happen from modelman's side — otherwise every existing wt install
    loses its LiteLLM configuration the moment this feature ships."""
    wt_config = tmp_path / "wt-config.toml"
    wt_config.write_text(
        '[gateway]\nmode = "litellm"\nurl = "http://localhost:4000"\n'
        'api_key = "sk-litellm-existing"\n'
    )
    state_path = tmp_path / "modelman.toml"
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))

    imported = migrate_wt_gateway_to_litellm(wt_config_path=wt_config)
    assert imported is True

    state = load_state(path=state_path)
    assert state.litellm.enabled is True
    assert state.litellm.url == "http://localhost:4000"
    assert state.litellm.api_key == "sk-litellm-existing"


def test_migrate_gateway_import_is_idempotent(tmp_path, monkeypatch):
    """A second run (e.g. modelman migrate invoked twice) must not clobber
    a value the user has since changed via `modelman litellm set`."""
    wt_config = tmp_path / "wt-config.toml"
    wt_config.write_text('[gateway]\nmode = "litellm"\nurl = "http://old:4000"\napi_key = "old-key"\n')
    state_path = tmp_path / "modelman.toml"
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))

    migrate_wt_gateway_to_litellm(wt_config_path=wt_config)
    with locked_state(path=state_path) as state:
        state.litellm.url = "http://new:4000"  # user changed it since

    imported_again = migrate_wt_gateway_to_litellm(wt_config_path=wt_config)
    assert imported_again is False
    assert load_state(path=state_path).litellm.url == "http://new:4000"
```

- [ ] **Step 2: Run to verify failure**

Run: `cd modelman && uv run pytest tests/test_migrate.py -k gateway -v`
Expected: FAIL — function doesn't exist.

- [ ] **Step 3: Implement the import**

Edit `modelman/src/modelman/migrate.py`:

```python
def migrate_wt_gateway_to_litellm(wt_config_path: Path | None = None) -> bool:
    """One-time import of wt's legacy [gateway] block into modelman's
    [litellm] table. Read-only on wt's file; skips if [litellm] is already
    populated so a user's later `modelman litellm set` is never clobbered."""
    path = wt_config_path or _default_wt_config_path()
    if not path.exists():
        return False
    wt_raw = tomllib.loads(path.read_text())
    gateway = wt_raw.get("gateway")
    if not gateway:
        return False

    with locked_state() as state:
        if state.litellm.url or state.litellm.api_key:
            return False  # already configured; do not overwrite
        state.litellm.enabled = gateway.get("mode") == "litellm"
        state.litellm.url = gateway.get("url")
        state.litellm.api_key = gateway.get("api_key")
    return True
```

Call it from the existing `modelman migrate` command's body, alongside the other one-time imports.

- [ ] **Step 4: Run to verify the tests pass**

Run: `cd modelman && uv run pytest tests/test_migrate.py -k gateway -v`
Expected: PASS.

- [ ] **Step 5: Write the failing omlx backfill test**

Edit `modelman/tests/test_sync.py`:

```python
def test_sync_backfills_missing_omlx_base_url(tmp_path):
    """omlx's registry template historically had no auth.base_url at all,
    so every omlx model was unroutable in direct mode with no clear error.
    sync must fill the gap for an existing provider without disturbing a
    base_url or protocols value the user has already set by hand."""
    registry = Registry(providers=[Provider(id="omlx", name="oMLX", location="local", auth=AuthConfig(type="none"))])
    result = backfill_provider_defaults(registry)
    omlx = next(p for p in result.providers if p.id == "omlx")
    assert omlx.auth.base_url == "http://localhost:8000"
    assert omlx.protocols == ["openai-chat"]


def test_sync_backfill_preserves_user_set_base_url(tmp_path):
    """A user-configured base_url (e.g. omlx moved to a non-default port)
    must never be overwritten by the backfill."""
    registry = Registry(providers=[Provider(id="omlx", name="oMLX", location="local", auth=AuthConfig(type="none", base_url="http://localhost:9999"))])
    result = backfill_provider_defaults(registry)
    omlx = next(p for p in result.providers if p.id == "omlx")
    assert omlx.auth.base_url == "http://localhost:9999"
```

- [ ] **Step 6: Run to verify failure**

Run: `cd modelman && uv run pytest tests/test_sync.py -k backfill -v`
Expected: FAIL — no `backfill_provider_defaults` function, and the omlx template still has no `base_url`.

- [ ] **Step 7: Add the omlx template fields and the backfill repair**

Edit `modelman/src/modelman/registry.py`, `_DEFAULT_PROVIDER_TEMPLATES["omlx"]` (`:246`):

```python
"omlx": ProviderTemplate(
    name="oMLX",
    location="local",
    auth=AuthConfig(type="none", base_url="http://localhost:8000"),
    protocols=["openai-chat"],
),
```

Edit `modelman/src/modelman/sync.py`, adding (near the existing "append wholly missing providers" logic at `:130`):

```python
def backfill_provider_defaults(registry: Registry) -> Registry:
    """Fill auth.base_url/protocols on an *existing* provider entry from
    its default template when the field is missing — sync.py:130 only
    ever appends providers that are wholly absent, so a provider added
    before this field existed (every pre-upgrade omlx entry) would
    otherwise stay permanently unroutable in direct mode."""
    for provider in registry.providers:
        template = _DEFAULT_PROVIDER_TEMPLATES.get(provider.id)
        if template is None:
            continue
        if not provider.auth.base_url and template.auth.base_url:
            provider.auth.base_url = template.auth.base_url
        if not provider.protocols and template.protocols:
            provider.protocols = template.protocols
    return registry
```

Call `backfill_provider_defaults` from wherever `modelman sync` already runs its reconciliation pass over the registry.

- [ ] **Step 8: Run to verify the tests pass**

Run: `cd modelman && uv run pytest tests/test_sync.py -k backfill -v`
Expected: PASS.

- [ ] **Step 9: Full verification and commit**

Run: `cd modelman && make check && make test`

```bash
git add modelman/src/modelman/migrate.py modelman/src/modelman/registry.py modelman/src/modelman/sync.py \
  modelman/tests/test_migrate.py modelman/tests/test_sync.py
git commit -m "migrate wt gateway config into modelman litellm table, backfill omlx base_url - completes plan item #8"
```

---

### Task 9: wt cutover — delete `GatewayConfig`, wire `IsLitellm`/`IsDirect` from modelman

**Files:**
- Modify: `wt/internal/config/config.go`
- Modify: `wt/internal/config/modelman.go` (`finalizeCfg` wiring)
- Modify: `wt/internal/config/migrate.go` (`migrateConfigSchema` fixup)
- Modify: `wt/internal/tui/model_list.go` (swap `cfg.Gateway.IsLitellm()` → `cfg.IsLitellm()`, per Task 6 Step 4's note)
- Modify: `wt/internal/agents/agents.go` (`BuildLaunchCmd` uses `cfg.IsLitellm()`/passes `cfg.litellm` into `ResolveRoute` instead of `cfg.Gateway`)
- Test: `wt/internal/config/config_test.go` (delete gateway-specific tests, add the two below)

**Interfaces:**
- Consumes: `Config.litellm LitellmState` (stored in Task 1, unused until now).
- Produces: `(*Config).IsLitellm() bool`, `(*Config).IsDirect() bool`; deletion of `GatewayConfig`/`Config.Gateway`.

- [ ] **Step 1: Write the failing migration-fixup test**

Edit `wt/internal/config/config_test.go`:

```go
// TestMigrateDropsLegacyGatewayBlock: a config.toml written before this
// feature still has [gateway]. Once GatewayConfig is deleted from Config,
// the struct simply can't round-trip that block — Save must not
// resurrect it, and the user should see one notice pointing them at the
// new control surface instead of silent data loss.
func TestMigrateDropsLegacyGatewayBlock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	os.WriteFile(path, []byte("default_tag = \"code\"\n\n[gateway]\nmode = \"litellm\"\nurl = \"http://localhost:4000\"\napi_key = \"sk-x\"\n"), 0o644)

	cfg, err := Load(WithConfigPath(path)) // adjust to the real Load option/signature
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	written, _ := os.ReadFile(path)
	if strings.Contains(string(written), "[gateway]") {
		t.Error("Save must not re-emit a deleted [gateway] block")
	}
}

// TestLitellmEnabledMissingURLFailsAtLaunchNotValidate: wt cannot repair
// modelman.toml, so a bad value there must not fail Validate() (which
// runs on every wt invocation, including `wt config`) — it must only
// surface when a route is actually resolved at launch.
func TestLitellmEnabledMissingURLFailsAtLaunchNotValidate(t *testing.T) {
	cfg := &Config{litellm: LitellmState{Enabled: true}} // no URL
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() must not fail on an incomplete [litellm] table: %v", err)
	}
	_, err := cfg.ResolveRoute(Model{ID: "ollama/x", ProviderID: "ollama"}, []Protocol{ProtocolOpenAIChat})
	if err == nil {
		t.Error("ResolveRoute must error when litellm is required but unconfigured")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd wt && go test ./internal/config -run 'TestMigrateDropsLegacyGatewayBlock|TestLitellmEnabledMissingURLFailsAtLaunchNotValidate' -v`
Expected: FAIL (or does not compile — `Config.litellm` field exists from Task 1 but `IsLitellm`/`IsDirect` methods and the deletion of `GatewayConfig` haven't happened).

- [ ] **Step 3: Delete `GatewayConfig` and add `IsLitellm`/`IsDirect`**

Edit `wt/internal/config/config.go`:

- Delete the `GatewayConfig` struct (`:40-44`), its methods `IsDirect`/`IsLitellm`/`BaseURL` (`:47-60`), the `Gateway GatewayConfig` field on `Config` (`:119`), and the validation block (`:245-260`).
- Add methods on `*Config` backed by the modelman-sourced field:

```go
func (c *Config) IsLitellm() bool { return c.litellm.Enabled }
func (c *Config) IsDirect() bool  { return !c.litellm.Enabled }
```

- Update `ResolveRoute` (Task 5's version) to read `c.litellm.URL`/`c.litellm.APIKey`/`c.IsLitellm()` everywhere it previously read `c.Gateway.BaseURL()`/`c.Gateway.APIKey`/`c.Gateway.IsLitellm()`.

- [ ] **Step 4: Delete the now-dead gateway tests**

Remove `TestValidateGatewayMode`, the url-required/api_key-required cases, and `TestGatewayConfigDirectByDefault` from `config_test.go` — the behavior they pinned (fail-fast validation of a wt-owned `[gateway]`) no longer exists by design (Global Constraints: wt never fails closed on `modelman.toml`).

- [ ] **Step 5: Add the `migrateConfigSchema` fixup**

Edit `wt/internal/config/migrate.go` (`migrateConfigSchema`, `:340+`):

```go
// dropLegacyGateway detects a config.toml still carrying [gateway] (from
// before GatewayConfig was deleted) and reports a one-time notice. The
// Gateway field no longer exists on Config, so decoding config.toml
// simply ignores an unknown [gateway] table (toml decoders skip unknown
// keys by default) — this fixup only needs to detect it well enough to
// tell the user where their settings moved.
func dropLegacyGateway(path string) (changed bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var probe struct {
		Gateway *struct{} `toml:"gateway"`
	}
	if err := toml.Unmarshal(raw, &probe); err != nil || probe.Gateway == nil {
		return false
	}
	fmt.Fprintln(os.Stderr, "wt: found a legacy [gateway] block in config.toml — LiteLLM routing is now controlled by modelman (see 'modelman litellm status'); this block will be dropped on next save")
	return true
}
```

Call `dropLegacyGateway(path)` from `migrateConfigSchema`'s existing per-`Load()` fixup chain, OR-ing its result into the function's overall `changed` return so `Load()` triggers a `Save()` that (per Step 3) no longer emits `[gateway]` — self-extinguishing after one notice.

- [ ] **Step 6: Update `finalizeCfg` to populate `c.litellm` for real**

Edit `wt/internal/config/modelman.go`. `finalizeCfg` (`config.go:214-223`) already calls `loadModelmanState` (Task 1) and stores its `LitellmState` return value in `cfg.litellm` — confirm that assignment is present and is the only place `cfg.litellm` is set outside tests.

- [ ] **Step 7: Update the two remaining `cfg.Gateway` call sites from earlier tasks**

- `wt/internal/tui/model_list.go` (Task 6, Step 4): replace `cfg.Gateway.IsLitellm()` with `cfg.IsLitellm()`.
- `wt/internal/agents/agents.go` `BuildLaunchCmd`: confirm it calls `cfg.ResolveRoute(m, agentProtocols)` with no remaining reference to `cfg.Gateway`.

- [ ] **Step 8: Run to verify Step 1's tests pass**

Run: `cd wt && go test ./internal/config -run 'TestMigrateDropsLegacyGatewayBlock|TestLitellmEnabledMissingURLFailsAtLaunchNotValidate' -v`
Expected: PASS.

- [ ] **Step 9: Full verification**

Run: `cd wt && go build ./... && go vet ./... && go test ./... && make lint`
Then: `make test-all` from the repo root.

- [ ] **Step 10: Commit**

```bash
git add wt/internal/config/ wt/internal/tui/model_list.go wt/internal/agents/agents.go
git commit -m "delete wt [gateway] config, source litellm state from modelman - completes plan item #9"
```

---

### Task 10: Smoke script + documentation sweep

**Files:**
- Modify: `wt/scripts/agents-smoke.sh`
- Modify: `docs/guides/00-config-map.md`, `02-providers-and-models.md`, `04-litellm-config.md`, `05-benchmarks.md`, `06-wt-agents-and-models.md`, `07-usage-and-spend.md`, `08-maintenance-and-troubleshooting.md`
- Modify: `wt/docs/wt-config.md`, `wt/docs/wt-agents/README.md`, `wt/docs/wt-agents/{claude,codex,copilot,opencode,pi}-wt.md`, `wt/docs/wt-agents/litellm-troubleshooting.md`
- Modify: `modelman/CLAUDE.md`, `wt/CLAUDE.md`

**Interfaces:** none — documentation and shell script only, no code interfaces produced.

- [ ] **Step 1: Rewrite the smoke script's mode-flip helpers**

Edit `wt/scripts/agents-smoke.sh`. Replace `set_gateway_mode` (`:202-226`, which awk-rewrites the `mode =` line inside `[gateway]` in wt's `config.toml`) with `set_litellm_enabled`, targeting modelman's `modelman.toml` instead:

```bash
# set_litellm_enabled MODELMAN_FILE on|off
# Rewrites the `enabled =` line inside [litellm] in modelman.toml. Errors
# loudly if no [litellm] table exists, mirroring the old gateway helper's
# fail-fast behavior — a silent no-op here would run the whole matrix
# against the wrong mode without telling you.
set_litellm_enabled() {
	local file="$1" value="$2"
	if ! grep -q '^\[litellm\]' "$file"; then
		echo "error: no [litellm] table in $file" >&2
		return 1
	fi
	awk -v val="$value" '
		/^\[litellm\]/ { in_section=1 }
		/^\[/ && !/^\[litellm\]/ { in_section=0 }
		in_section && /^\s*enabled\s*=/ { print "enabled = " val; next }
		{ print }
	' "$file" > "$file.tmp" && mv "$file.tmp" "$file"
}
```

Replace `gateway_field` (`:233`) with `litellm_field`, reading `enabled`/`url`/`api_key` from `[litellm]` in the same file. Replace every `CONFIG_FILE` reference used for gateway flips with a new `MODELMAN_FILE` pointing at `${MODELMAN_STATE:-$HOME/.config/local-ai/modelman.toml}`, and update the trap that restores the original file on exit to restore `MODELMAN_FILE` instead of (or in addition to) `CONFIG_FILE`. Keep `DEFAULT_MODES="direct,litellm"` and `is_mode_invariant` unchanged.

- [ ] **Step 2: Manually smoke-test the script's mode-flip logic in isolation**

Run: `cd wt && bash -n scripts/agents-smoke.sh` (syntax check)
Run: `shellcheck --severity=error scripts/agents-smoke.sh`
Expected: both clean.

- [ ] **Step 3: Update `docs/guides/06-wt-agents-and-models.md`**

Rewrite §4 "LiteLLM gateway mode" (`:89-107`): remove the `[gateway] mode/url/api_key` TOML block, replace with `modelman litellm status|on|off|set`; correct "In this mode: wt only shows models with `litellm_exposed = true`" — the exposure filter (`exposed` after Task 1's rename) has always applied unconditionally, independent of the on/off setting; state that explicitly. Remove the stale "OpenCode continues to use Ollama directly" line (opencode has had gateway support since before this change).

- [ ] **Step 4: Update `docs/guides/00-config-map.md`**

- `~/.config/local-ai/modelman.toml` section (`:16,:58,:61,:63-80`): add `[litellm]` to the shown excerpt; rewrite the "Exposure predicate (both tools)" note to use `exposed`.
- `~/.config/agent-wt/config.toml` section (`:21,:167`): remove "`[gateway]` is a new wt-owned section for routing agents through LiteLLM" — replace with a pointer to modelman's `[litellm]` table as the source of truth, and note wt reads it read-only.
- Gotchas (`:287`): update the exposure-flag gotcha to the new field name.

- [ ] **Step 5: Update the remaining guides**

- `docs/guides/02-providers-and-models.md` (`:44,:64,:217,:221-263,:282-284`): rename `litellm_exposed` → `exposed` in every command example and the verification greps; add a line documenting the new `protocols` field on providers.
- `docs/guides/04-litellm-config.md` (§2,§3,`:128,:283`): same rename; note that toggling `[litellm].enabled` no longer requires re-exposing models.
- `docs/guides/05-benchmarks.md` (`:10`): rename in the benchmark-target-discovery description.
- `docs/guides/07-usage-and-spend.md` (`:101-108`): replace "After enabling `[gateway]` in `wt`…" with the `modelman litellm on` flow.
- `docs/guides/08-maintenance-and-troubleshooting.md` (`:148-181`): correct "`config.yaml` is routing truth; `litellm_exposed` is bookkeeping" — `exposed` now is the sole truth for what wt shows, independent of whether `config.yaml`/LiteLLM routing is active; rename the `grep -c 'litellm_exposed = true'` command.

Run `git grep -n "litellm_exposed = " docs/guides/` before and after this step — the CLAUDE.md gotcha names guides 00, 02, 04, 05, 06, 08 as embedding live snapshots; confirm the count of remaining hits is zero (or intentionally only the historical/legacy-key documentation added in Task 1, if any guide chooses to mention it).

- [ ] **Step 6: Update wt-side docs**

- `wt/docs/wt-config.md`: add a short note that LiteLLM routing is controlled by modelman (`modelman litellm status`), not `wt config` — there was never a gateway surface in the editor, so this is purely additive.
- `wt/docs/wt-agents/README.md`: update "Migrating to modelman exposure" to say `exposed`; update "LiteLLM proxy lifecycle" to note the on/off *routing* decision (not just the proxy's own lifecycle) is also modelman-owned now.
- `wt/docs/wt-agents/{claude,codex,copilot,opencode,pi}-wt.md`: in each "### Gateway mode (LiteLLM)" section, replace "When `[gateway].mode = \"litellm\"` is set in `~/.config/agent-wt/config.toml`…" with "When LiteLLM routing is enabled (`modelman litellm status`)…"; for codex specifically, add a line noting codex always routes through LiteLLM regardless of the setting (no local provider serves the responses API it requires).
- `wt/docs/wt-agents/litellm-troubleshooting.md`: add a short note near the top pointing at the new forced-litellm stderr notice as the expected (not erroneous) behavior for codex.

- [ ] **Step 7: Update both `CLAUDE.md` files**

- `modelman/CLAUDE.md`: document the `litellm` sub-app, the `[litellm]` table, and the `protocols` registry field.
- `wt/CLAUDE.md`: replace the `[gateway]`/`GatewayConfig` documentation with `Route`/`ResolveRoute`/`ProtocolDeclarer`; add the agent-protocol table from the spec; update the "Model id contract" section (the `m.ID` vs `m.ModelName` split is now a property of `Route.Litellm`, not a per-driver `if gw.IsLitellm()` check); update "Adding a new agent driver" to mention implementing `ProtocolDeclarer`.

- [ ] **Step 8: Run link-check and shell lint**

Run: `make lint`
Expected: PASS (`lint-shell` + `check-links`).

- [ ] **Step 9: Run the live agent smoke test**

Run: `cd wt && make test-agents`
Expected: PASS for every agent in both modes; codex's run should show the forced-litellm stderr notice rather than failing.

- [ ] **Step 10: Final full verification and commit**

Run: `make test-all`

```bash
git add wt/scripts/agents-smoke.sh docs/guides/ wt/docs/ modelman/CLAUDE.md wt/CLAUDE.md
git commit -m "update smoke script and docs for modelman-owned litellm control - completes plan item #10"
```

---

## Verification (end-to-end, after Task 10)

- `make test-all` from the repo root (mirrors all three CI jobs).
- `cd wt && make test-agents` (live: every agent × configured models, both `[litellm]` states).
- Manual: `modelman litellm off && wt --cwd -A copilot -M openrouter/<any-exposed-id>` dials `https://openrouter.ai/api/v1` directly (check with a debug print or a local proxy capture if available); `wt --cwd -A claude -M openrouter/<any-exposed-id>` prints the forced-litellm stderr notice and succeeds via the proxy; `modelman litellm status` never shows a raw API key; the LaunchAgent-managed proxy on :4000 stays running across both `modelman litellm on` and `off`.
