# Native-Provider Exposure Exemption Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add the native-provider exposure exemption to `modelman` so it matches `wt`, derive a shared cross-language contract fixture for the rule, and confirm the apply/routing gate intentionally stays separate from the display predicate.

**Architecture:** A post-load `_derive_native` pass in `modelman.registry` sets `ModelEntry.native` from the provider's `auth.type`, mirroring `wt`'s `deriveNative`. The TUI/catalog predicate `is_effectively_exposed` short-circuits to `True` for native models. The LiteLLM apply gate (`passes_ready_gate` / `_validated_entry`) is left unchanged because native providers have no LiteLLM mapping. A native provider/model pair is added to `docs/contracts/registry.sample.toml` and both language tests assert the same behavior from the same file.

**Tech Stack:** Python 3, `modelman` (dataclasses/TOML), `pytest`, Go, `toml`, `make test-all`.

---

## File Map

| File | Responsibility |
| --- | --- |
| `modelman/src/modelman/registry.py` | Add derived `native` field to `ModelEntry`; add `_derive_native`; call it from `load_registry`; `_model_to_dict` whitelist already excludes it. |
| `modelman/src/modelman/litellm.py` | Short-circuit `is_effectively_exposed` for `model.native`; update docstring; leave `passes_ready_gate` / `_validated_entry` alone, add a comment explaining why. |
| `docs/contracts/registry.sample.toml` | Shared cross-language fixture: add a native `agy` provider and an `agy/contract-fixture:native` model with no state row. |
| `modelman/tests/test_litellm.py` | Unit-test the native exemption in `is_effectively_exposed` and verify non-native cases are unchanged. |
| `modelman/tests/contracts/test_registry_fixture.py` | Update fixture counts and assert the native provider/model decode correctly and carry `native=True`. |
| `modelman/tests/test_registry.py` | Test that `load_registry` derives `native` and that `save_registry` does not persist it. |
| `wt/internal/config/registry_fixture_test.go` | Add Go-side contract test loading the same fixture and asserting `IsExposed` is `true` with no `model_state` row. |

---

### Task 1: Extend the shared registry contract fixture

**Files:**
- Modify: `docs/contracts/registry.sample.toml`

This fixture is read by both `wt/internal/config/registry_fixture_test.go` and `modelman/tests/contracts/test_registry_fixture.py`. Adding a native provider and model with no `model_state` row exercises the strongest case: native models are exposed even when unflagged and not ready.

- [ ] **Step 1: Append a native provider and model to the fixture**

Insert immediately after the `openrouter` provider block (before `[[families]]`):

```toml
[[providers]]
id = "agy"
name = "Agy"
location = "local"
[providers.auth]
type = "native"
```

Insert immediately after the `openrouter/contract-fixture:cloud` model block (at the end of the `[[models]]` section):

```toml
[[models]]
id = "agy/contract-fixture:native"
family = "contract-fixture"
provider_id = "agy"
model_name = "contract-fixture:native"
tags = ["native"]
```

- [ ] **Step 2: Validate the TOML is parseable**

Run:

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup/.worktrees/native-exposure-exemption
uv run python -c "import tomllib, pathlib; tomllib.load(open(pathlib.Path('docs/contracts/registry.sample.toml'), 'rb'))"
```

Expected: command exits 0 with no output.

- [ ] **Step 3: Commit**

```bash
git add docs/contracts/registry.sample.toml
git commit -m "contract(registry): add native provider/model fixture for exposure exemption"
```

---

### Task 2: Derive native-ness onto `ModelEntry`

**Files:**
- Modify: `modelman/src/modelman/registry.py`

- [ ] **Step 1: Add the `native` field to `ModelEntry`**

Change the dataclass around line 132:

```python
@dataclass
class ModelEntry:
    id: str
    family: str
    provider_id: str
    model_name: str
    location: str | None = None
    source: str | None = None  # "curated" | "discovered"
    tags: list[str] = field(default_factory=list)
    cost: Cost | None = None
    model_info: dict[str, Any] = field(default_factory=dict)
    fetch: Fetch | None = None
    native: bool = False
    extra: dict[str, Any] = field(default_factory=dict, repr=False)
```

- [ ] **Step 2: Add the post-load derivation function**

Insert before `load_registry` (after `default_provider_entry` is fine, e.g. around line 334):

```python
def _derive_native(registry: Registry) -> None:
    """Mark each model whose provider authenticates natively
    (auth.type == "native") as native. Mirrors wt's deriveNative.
    Runs after providers and models are parsed so the registry is the
    single source of truth for native-ness.
    """
    native_ids = {p.id for p in registry.providers if p.auth.type == "native"}
    for m in registry.models:
        m.native = m.provider_id in native_ids
```

- [ ] **Step 3: Call `_derive_native` at the end of `load_registry`**

Change `load_registry` so the parsed registry is passed through `_derive_native` before return:

```python
def load_registry(path: Path | None = None) -> Registry:
    registry_path = Path(path) if path else _default_registry_path()
    if not registry_path.exists():
        # Fall back to the pre-XDG location for users who created a registry
        # before the XDG alignment and have XDG_CONFIG_HOME set. The next
        # save_registry writes to the canonical (XDG) path, migrating it.
        legacy = Path("~/.config/local-ai/registry.toml").expanduser()
        if path is None and registry_path != legacy and legacy.exists():
            registry_path = legacy
        else:
            raise RegistryError(f"Registry file not found: {registry_path}")
    with open(registry_path, "rb") as f:
        raw = tomllib.load(f)
    registry = Registry(
        providers=[_parse_provider(p) for p in raw.get("providers", [])],
        families=[_parse_family(f) for f in raw.get("families", [])],
        models=[_parse_model(m) for m in raw.get("models", [])],
    )
    _derive_native(registry)
    return registry
```

- [ ] **Step 4: Verify the field is excluded from persistence**

Read `_model_to_dict` (around line 429). Its explicit whitelist does **not** contain `native`, so no code change is needed. Confirm by inspection:

```bash
grep -n "native" /Users/keith/github/ohanaverse/local-ai-setup/.worktrees/native-exposure-exemption/modelman/src/modelman/registry.py
```

Expected: output shows `native: bool = False`, `_derive_native`, and the `_derive_native(registry)` call — but no `native` key inside `_model_to_dict`.

- [ ] **Step 5: Run modelman lint/typecheck**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup/.worktrees/native-exposure-exemption/modelman
uv sync && make check
```

Expected: `ruff check src/ tests/` and `mypy src/` both pass.

- [ ] **Step 6: Commit**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup/.worktrees/native-exposure-exemption
git add modelman/src/modelman/registry.py
git commit -m "feat(registry): derive ModelEntry.native from provider auth.type"
```

---

### Task 3: Apply the exemption in `is_effectively_exposed`

**Files:**
- Modify: `modelman/src/modelman/litellm.py`

- [ ] **Step 1: Update `is_effectively_exposed` to short-circuit on native models**

Replace the function around line 134 with:

```python
def is_effectively_exposed(
    model: ModelEntry,
    state: StateStore,
    exposed_override: bool | None = None,
    ready_override: bool | None = None,
) -> bool:
    """Determine if a model is effectively exposed (catalog/display predicate).

    A model is effectively exposed when ANY of these hold:
    - it is a native model (provider auth.type == "native"); native providers
      cannot route through LiteLLM, so they are always considered exposed, OR
    - its `litellm_exposed` flag is True (or `exposed_override` is True) AND
      it passes the ready gate: it is ready (or `ready_override` is True) or
      it is a cloud model (exempt from the ready gate).

    This predicate is intentionally distinct from the apply/routing gate
    (`passes_ready_gate` / `_validated_entry`): the apply gate governs whether
    a model can be written into LiteLLM's config and may flip the flag it is
    checking. Native models have no LiteLLM mapping, so they are catalog-only
    and are rejected earlier by `_validated_entry` via the `policy is None` check.

    Args:
        model: The registry model entry to check.
        state: StateStore for ready/exposed flags.
        exposed_override: Override the persisted litellm_exposed flag.
        ready_override: Override the persisted ready flag.

    Returns:
        True if the model should show as exposed in the catalog, False otherwise.
    """
    if model.native:
        return True
    exposed = (
        exposed_override if exposed_override is not None else state.get(model.id).litellm_exposed
    )
    if not exposed:
        return False
    return passes_ready_gate(model, state, ready_override=ready_override)
```

- [ ] **Step 2: Add a clarifying comment to `passes_ready_gate`**

After the existing docstring opening of `passes_ready_gate` (around line 104), add one sentence if not already covered. The docstring already says "The gate apply-time validation enforces (`_validated_entry` rejects an expose with 'model is not ready')". Ensure the final docstring reads:

```python
    """Whether a model passes the expose-time readiness gate.

    The gate apply-time validation enforces (`_validated_entry` rejects an
    expose with "model is not ready"): ready is required unless the model
    is effectively cloud — cloud rows are exempt from the ready gate.

    This is a routing gate, distinct from the catalog/display predicate
    `is_effectively_exposed`. In particular it does NOT include the native
    exemption: native providers have no LiteLLM mapping and are rejected by
    `_validated_entry` before this gate is reached.
    ...
```

Only add the two new sentences if they are missing. Do not duplicate the rest of the docstring.

- [ ] **Step 3: Add an inline comment on `_validated_entry`'s policy check** (optional but recommended)

Around line 482 in `_validated_entry`, the existing code is:

```python
    policy = provider_policy(model.provider_id)
    if policy is None:
        raise ExposeError(f"provider {model.provider_id!r} has no LiteLLM mapping")
```

This already correctly rejects native providers. Leave it as-is; the comment is not strictly needed because the docstring update above explains it. If you add a comment, keep it minimal:

```python
    # Native providers hit this check: auth.type == "native" means no LiteLLM mapping.
```

- [ ] **Step 4: Run modelman lint/typecheck**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup/.worktrees/native-exposure-exemption/modelman
make check
```

Expected: passes.

- [ ] **Step 5: Commit**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup/.worktrees/native-exposure-exemption
git add modelman/src/modelman/litellm.py
git commit -m "feat(litellm): native models are always effectively exposed"
```

---

### Task 4: Test the native exemption in `test_litellm.py`

**Files:**
- Modify: `modelman/tests/test_litellm.py`

The existing helper `_model` builds non-native models. We need a native provider + model for the new test.

- [ ] **Step 1: Add a native test helper or inline the model**

Append the new test after `test_is_effectively_exposed_ready_override`:

```python
def test_is_effectively_exposed_native_not_exposed_not_ready():
    # Native models bypass LiteLLM entirely, so they are always catalog-exposed
    # even when unflagged and not ready. This must match wt's IsExposed.
    native_provider = _provider("agy", auth_type="native")
    model = _model("agy/contract-fixture:native", "agy", "contract-fixture:native")
    model.native = True
    state = StateStore()
    state.set("agy/contract-fixture:native", ModelState(ready=False, litellm_exposed=False))

    assert is_effectively_exposed(model, state) is True
```

- [ ] **Step 2: Run the new test and the four existing exposure tests**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup/.worktrees/native-exposure-exemption/modelman
uv run pytest tests/test_litellm.py -k "is_effectively_exposed" -v
```

Expected: all five tests pass, including the new native test.

- [ ] **Step 3: Commit**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup/.worktrees/native-exposure-exemption
git add modelman/tests/test_litellm.py
git commit -m "test(litellm): native model is exposed even when unflagged and not ready"
```

---

### Task 5: Update the Python contract test

**Files:**
- Modify: `modelman/tests/contracts/test_registry_fixture.py`

- [ ] **Step 1: Update provider and model counts and add native assertions**

Replace the existing assertions in `test_load_registry_matches_shared_fixture` with the extended version:

```python
def test_load_registry_matches_shared_fixture():
    """Guards modelman's registry.toml schema against wt's Go decoder
    (wt/internal/config/registry_fixture_test.go reads the same file). A
    schema change not reflected in both tests fails both CI jobs in the
    same PR instead of drifting silently.
    """
    registry = load_registry(path=FIXTURE)

    assert len(registry.providers) == 3
    ollama = registry.provider("ollama")
    assert ollama.auth.type == "none"
    assert ollama.auth.base_url == "http://localhost:11434"
    openrouter = registry.provider("openrouter")
    assert openrouter.auth.type == "api_key"
    assert openrouter.auth.secret_ref == "OPENROUTER_API_KEY"
    agy = registry.provider("agy")
    assert agy.auth.type == "native"

    assert len(registry.models) == 3

    free_model = registry.model("ollama/contract-fixture:local")
    assert free_model.cost is None
    assert free_model.native is False

    cloud_model = registry.model("openrouter/contract-fixture:cloud")
    assert cloud_model.location == "cloud"
    assert cloud_model.model_info == {"supports_function_calling": True}
    assert cloud_model.cost is not None
    assert cloud_model.cost.input_price_per_million == 0.50
    assert cloud_model.cost.cache_price_per_million == 0.25
    assert cloud_model.cost.output_price_per_million == 1.00
    assert cloud_model.cost.subscription_price == 19.99
    assert cloud_model.cost.subscription_period == "month"
    assert cloud_model.native is False

    native_model = registry.model("agy/contract-fixture:native")
    assert native_model.provider_id == "agy"
    assert native_model.native is True

    family = registry.family("contract-fixture")
    assert family is not None
    assert family.display_name == "Contract Fixture"
```

- [ ] **Step 2: Run the contract test**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup/.worktrees/native-exposure-exemption/modelman
uv run pytest tests/contracts/test_registry_fixture.py -v
```

Expected: passes.

- [ ] **Step 3: Commit**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup/.worktrees/native-exposure-exemption
git add modelman/tests/contracts/test_registry_fixture.py
git commit -m "test(contract): assert native provider/model in shared registry fixture"
```

---

### Task 6: Add registry persistence and derivation tests

**Files:**
- Modify: `modelman/tests/test_registry.py`

- [ ] **Step 1: Add a test for native derivation on load**

Append near the other `load_registry` tests:

```python
def test_load_registry_derives_native_from_provider_auth(tmp_path):
    path = tmp_path / "registry.toml"
    path.write_text(
        '[[providers]]\n'
        'id = "ollama"\n'
        'name = "Ollama"\n'
        '[providers.auth]\n'
        'type = "none"\n\n'
        '[[providers]]\n'
        'id = "agy"\n'
        'name = "Agy"\n'
        '[providers.auth]\n'
        'type = "native"\n\n'
        '[[models]]\n'
        'id = "ollama/x"\n'
        'family = "x"\n'
        'provider_id = "ollama"\n'
        'model_name = "x"\n\n'
        '[[models]]\n'
        'id = "agy/x"\n'
        'family = "x"\n'
        'provider_id = "agy"\n'
        'model_name = "x"\n'
    )
    loaded = load_registry(path)
    assert loaded.model("ollama/x").native is False
    assert loaded.model("agy/x").native is True
```

- [ ] **Step 2: Add a test that `native` is not persisted**

Append near the other save/load round-trip tests:

```python
def test_save_registry_does_not_persist_native_field(tmp_path):
    # native is derived from provider auth, not stored in registry.toml.
    # If it leaked out, a load→derive→save cycle would create a diff on disk.
    path = tmp_path / "registry.toml"
    registry = Registry(
        providers=[
            ProviderEntry(id="agy", name="Agy", auth=AuthConfig(type="native")),
        ],
        models=[
            ModelEntry(
                id="agy/x",
                family="x",
                provider_id="agy",
                model_name="x",
                native=True,
            )
        ],
    )
    save_registry(registry, path)
    text = path.read_text()
    assert "native" not in text
    loaded = load_registry(path)
    assert loaded.model("agy/x").native is True
```

- [ ] **Step 3: Run the new registry tests**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup/.worktrees/native-exposure-exemption/modelman
uv run pytest tests/test_registry.py -k "derive_native or persist_native" -v
```

Expected: both tests pass.

- [ ] **Step 4: Commit**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup/.worktrees/native-exposure-exemption
git add modelman/tests/test_registry.py
git commit -m "test(registry): native derivation and non-persistence"
```

---

### Task 7: Add the Go cross-language contract test

**Files:**
- Modify: `wt/internal/config/registry_fixture_test.go`

`Config.IsExposed` needs an `exposed` map to be loaded. The fixture has no `model_state` row for the native model, so the test must inject an empty exposed set via `SetExposedForTest` and then assert `IsExposed` still returns `true`.

- [ ] **Step 1: Add `TestRegistryFixtureNativeExposure`**

Append at the end of the file:

```go
// TestRegistryFixtureNativeExposure pins the cross-language rule that a
// native model is always exposed even without a model_state row. The same
// fixture file is read by modelman's contract test.
func TestRegistryFixtureNativeExposure(t *testing.T) {
	t.Setenv("MODELMAN_REGISTRY", "../../../docs/contracts/registry.sample.toml")

	providers, models, err := loadRegistry()
	if err != nil {
		t.Fatalf("loadRegistry() error: %v", err)
	}

	cfg := &Config{Providers: providers, Models: models}
	deriveNative(cfg)
	cfg.SetExposedForTest(map[string]struct {
		LitellmExposed bool
		Ready          bool
	}{})

	native := cfg.Models[2]
	if native.ID != "agy/contract-fixture:native" {
		t.Fatalf("expected third model to be the native fixture, got %q", native.ID)
	}
	if !native.Native {
		t.Errorf("native model %q has Native=%v, want true", native.ID, native.Native)
	}
	if !cfg.IsExposed(native) {
		t.Errorf("IsExposed(native model %q) = false, want true", native.ID)
	}
}
```

- [ ] **Step 2: Update the provider/model count assertion in the existing Go contract test**

The existing `TestLoadRegistryMatchesSharedFixture` still expects 2 providers and 2 models. Update the hard-coded counts to 3:

```go
	if len(providers) != 3 {
		t.Fatalf("got %d providers, want 3", len(providers))
	}
```

and

```go
	if len(models) != 3 {
		t.Fatalf("got %d models, want 3", len(models))
	}
```

- [ ] **Step 3: Run the Go contract tests**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup/.worktrees/native-exposure-exemption/wt
go test ./internal/config -run "TestLoadRegistryMatchesSharedFixture|TestRegistryFixtureNativeExposure" -v
```

Expected: both tests pass.

- [ ] **Step 4: Commit**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup/.worktrees/native-exposure-exemption
git add wt/internal/config/registry_fixture_test.go
git commit -m "test(config): pin native exposure rule with shared fixture"
```

---

### Task 8: Run the full regression suite

**Files:**
- None; verification only.

- [ ] **Step 1: Run `make test-all`**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup/.worktrees/native-exposure-exemption
make test-all
```

Expected: lint, check-links, modelman `make check`, modelman `make test`, `go build ./...`, `go vet ./...`, and `go test ./...` all pass.

- [ ] **Step 2: If any test fails, debug and fix**

Use `systematic-debugging` if needed. Common issues:

- The Python contract test or Go contract test still expects old counts.
- `ModelEntry.native` leaks into saved TOML because `_model_to_dict` was modified elsewhere.
- `mypy` complains about the new field in dataclass construction sites (add `native=False` explicitly where required, or rely on the default).

- [ ] **Step 3: Final commit if fixes were needed**

```bash
git add -A
git commit -m "fix(native-exposure): address regression suite feedback"
```

---

## Self-Review Checklist

1. **Spec coverage**
   - [ ] `ModelEntry.native` derived from provider auth.type — Task 2.
   - [ ] `is_effectively_exposed` short-circuits on `model.native` — Task 3.
   - [ ] Apply/routing gate intentionally unchanged — Task 3 (docstring comment, no logic change).
   - [ ] Shared contract fixture extended with native provider/model — Task 1.
   - [ ] Python unit test for native exemption — Task 4.
   - [ ] Python contract test updated — Task 5.
   - [ ] Python registry derivation/persistence tests — Task 6.
   - [ ] Go contract test pins the same rule — Task 7.
   - [ ] Full regression via `make test-all` — Task 8.

2. **Placeholder scan**
   - [ ] No "TBD", "TODO", "implement later", "fill in details".
   - [ ] No vague "add appropriate error handling" or "write tests for the above".
   - [ ] No "Similar to Task N" shortcuts.
   - [ ] Every code step contains the actual code.

3. **Type consistency**
   - [ ] `ModelEntry.native` is `bool` everywhere.
   - [ ] `is_effectively_exposed` signature unchanged.
   - [ ] Fixture provider id is `"agy"` in TOML, Python tests, and Go tests.
   - [ ] Fixture model id is `"agy/contract-fixture:native"` in TOML, Python tests, and Go tests.
