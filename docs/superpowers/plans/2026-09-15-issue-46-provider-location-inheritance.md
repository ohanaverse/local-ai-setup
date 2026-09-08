# Issue #46 — Provider-Location Inheritance in Exposure Predicates — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make modelman's Python exposure predicates inherit provider `location = "cloud"` the way wt's Go `ResolveLocation` does, and pin that parity in the shared contract fixture (fixes #46).

**Architecture:** `is_cloud_effective` gains a required `registry` parameter and a third cloud check (`registry.provider(model.provider_id).location == "cloud"`, unknown provider → conservative False). `passes_ready_gate` and `is_effectively_exposed` thread the same parameter through; all call sites already hold a `Registry`. A new provider+model pair in the shared fixture (`pinned-cloud`) exercises the inheritance on both sides; docs are updated so the "same rule" claim in the config map becomes true.

**Tech Stack:** Python (modelman, pytest, uv), Go (wt, testing), shared TOML fixtures, markdown docs.

**Spec:** `docs/superpowers/specs/2026-09-15-issue-46-provider-location-inheritance-design.md`

**Work on branch `fix/issue-46-provider-location-inheritance`** (already exists, has the spec commit). A pre-commit hook blocks commits to `main`.

---

### Task 1: Contract fixture — add provider-location inheritance case

**Files:**
- Modify: `docs/contracts/registry.sample.toml`
- Modify: `docs/contracts/modelman.sample.toml`
- Modify: `modelman/tests/contracts/test_registry_fixture.py`
- Modify: `wt/internal/config/registry_fixture_test.go`

- [ ] **Step 1: Add the provider + model to `docs/contracts/registry.sample.toml`**

After the `[[providers]]` block for `agy`, add:

```toml
[[providers]]
id = "pinned-cloud"
name = "Pinned Cloud"
location = "cloud"
[providers.auth]
type = "api_key"
base_url = "https://pinned-cloud.example/v1"
secret_ref = "PINNED_CLOUD_API_KEY"
```

After the `[[models]]` block for `agy/contract-fixture:native`, add (note: **no** `location` field — that is the point):

```toml
# Provider-inherited location: model omits `location`, inheriting the
# provider's "cloud". Both exposure predicates must treat this as cloud
# (issue #46).
[[models]]
id = "pinned-cloud/contract-fixture:inherit"
family = "contract-fixture"
provider_id = "pinned-cloud"
model_name = "org/contract-fixture-inherit"
tags = ["code"]
```

- [ ] **Step 2: Add the exposure example to `docs/contracts/modelman.sample.toml`**

Next to the other `model_state` examples (after the `openrouter/contract-fixture:cloud-exposed` block), add:

```toml
# Provider-inherited cloud location (issue #46): model has no location of
# its own; the provider's location = "cloud" exempts it from the ready
# gate. Exposure: flag=true + provider location=cloud → exposed on BOTH
# sides (wt ResolveLocation and modelman is_cloud_effective).
[model_state."pinned-cloud/contract-fixture:inherit"]
litellm_exposed = true
```

- [ ] **Step 3: Update the Python contract test**

In `modelman/tests/contracts/test_registry_fixture.py`:

- Change `assert len(registry.providers) == 3` to `== 4` and add after the `agy` assertions:

```python
    pinned = registry.provider("pinned-cloud")
    assert pinned.auth.type == "api_key"
    assert pinned.location == "cloud"
    assert pinned.auth.secret_ref == "PINNED_CLOUD_API_KEY"
```

- Change `assert len(registry.models) == 3` to `== 4` and add after the `native_model` assertions:

```python
    inherit_model = registry.model("pinned-cloud/contract-fixture:inherit")
    assert inherit_model.location is None  # inherits provider location
    assert inherit_model.native is False
```

- Add a new test at the end of the file (add imports `from modelman.litellm import is_effectively_exposed` and `from modelman.state import ModelState, StateStore` at the top):

```python
def test_fixture_pins_provider_location_inheritance():
    """Issue #46: a model with no location of its own on a
    location="cloud" provider must be exposed on the Python side exactly
    as wt's ResolveLocation-based IsExposed treats it."""
    registry = load_registry(path=FIXTURE)
    state = StateStore()
    state.set(
        "pinned-cloud/contract-fixture:inherit",
        ModelState(ready=False, litellm_exposed=True),
    )
    model = registry.model("pinned-cloud/contract-fixture:inherit")
    assert is_effectively_exposed(model, state, registry=registry) is True
```

Note: this test will fail (TypeError) until Task 2 adds the `registry` parameter — that is expected; the contract tests are updated in the same PR. If you want every commit green, swap Task 1 and Task 2 order (do Task 2 first, then Task 1); the default order here keeps fixture and both language tests in one coherent commit.

- [ ] **Step 4: Update the Go fixture test**

In `wt/internal/config/registry_fixture_test.go`:

- Change `if len(providers) != 3` to `!= 4` and add after the `agy` check:

```go
	pinned := providers[3]
	if pinned.ID != "pinned-cloud" || pinned.Auth.Type != "api_key" || pinned.Location != LocationCloud {
		t.Errorf("pinned-cloud provider decoded wrong: %+v", pinned)
	}
```

- Change `if len(models) != 3` to `!= 4` and add after the `cloud` model check:

```go
	inherit := models[3]
	if inherit.ID != "pinned-cloud/contract-fixture:inherit" || inherit.Location != "" || inherit.ProviderID != "pinned-cloud" {
		t.Errorf("inherit model decoded wrong: %+v", inherit)
	}
```

- Add a new test after `TestLoadRegistryMatchesSharedFixture` pinning the Go-side predicate:

```go
// TestRegistryFixtureProviderLocationInheritance pins issue #46 from the
// Go side: IsExposed resolves the model's location through the provider,
// so a flag-on, not-ready model on a location=cloud provider is exposed.
func TestRegistryFixtureProviderLocationInheritance(t *testing.T) {
	t.Setenv("MODELMAN_REGISTRY", "../../../docs/contracts/registry.sample.toml")

	providers, models, err := loadRegistry()
	if err != nil {
		t.Fatalf("loadRegistry() error: %v", err)
	}
	c := &Config{Providers: providers, Models: models}
	var inherit *Model
	for i := range models {
		if models[i].ID == "pinned-cloud/contract-fixture:inherit" {
			inherit = &models[i]
		}
	}
	if inherit == nil {
		t.Fatal("fixture missing pinned-cloud/contract-fixture:inherit")
	}
	if !c.IsExposed(*inherit) {
		t.Error("IsExposed(inherit model) = false, want true (provider location=cloud must inherit)")
	}
}
```

- [ ] **Step 5: Run the Go fixture tests**

Run: `cd wt && go test ./internal/config/ -run 'TestRegistryFixture|TestRegistryFixtureProviderLocationInheritance' -v`
Expected: PASS (the new Python contract test may fail with TypeError until Task 2 — that's fine, but note it).

- [ ] **Step 6: Commit**

```bash
git add docs/contracts/registry.sample.toml docs/contracts/modelman.sample.toml \
  modelman/tests/contracts/test_registry_fixture.py wt/internal/config/registry_fixture_test.go
git commit -m "test(contracts): pin provider-location inheritance in shared fixture (#46)"
```

---

### Task 2: Python predicates inherit provider location

**Files:**
- Modify: `modelman/src/modelman/litellm.py` (`is_cloud_effective` ~line 100, `passes_ready_gate` ~line 114, `is_effectively_exposed` ~line 143, `_validated_entry` ~line 516)
- Modify: `modelman/src/modelman/screens/models.py` (call sites at ~line 376, ~line 430, ~line 550)
- Test: `modelman/tests/test_litellm.py`

- [ ] **Step 1: Write the failing tests**

Append to `modelman/tests/test_litellm.py` (the file already imports `is_cloud_effective`, `is_effectively_exposed`, `passes_ready_gate`, `StateStore`, `ModelState`, `ModelEntry`, and has the `_model` helper and `Registry` construction at lines ~544/572 — reuse those patterns):

```python
def _registry_with_providers(*providers):
    """Minimal Registry holding just providers (no models needed by the
    cloud predicates)."""
    return Registry(providers=list(providers))


def test_is_cloud_effective_provider_location_inherited():
    # Issue #46: a model with no location of its own on a
    # location="cloud" provider is effectively cloud, matching wt's
    # ResolveLocation (model location, then provider location).
    from modelman.registry import ProviderEntry

    model = _model("handmade/x", "handmade", "x")
    registry = _registry_with_providers(
        ProviderEntry(id="handmade", name="Handmade", location="cloud")
    )
    assert is_cloud_effective(model, registry) is True


def test_is_cloud_effective_provider_local_not_cloud():
    from modelman.registry import ProviderEntry

    model = _model("handmade/x", "handmade", "x")
    registry = _registry_with_providers(
        ProviderEntry(id="handmade", name="Handmade", location="local")
    )
    assert is_cloud_effective(model, registry) is False


def test_is_cloud_effective_unknown_provider_conservative():
    # Unknown provider (hand-edited registry referencing an undefined
    # provider) → not cloud, mirroring is_cloud's conservative fallback.
    model = _model("handmade/x", "handmade", "x")
    registry = _registry_with_providers()
    assert is_cloud_effective(model, registry) is False


def test_is_effectively_exposed_provider_cloud_not_ready():
    # The #46 failure scenario: flag on, ready false, model location
    # unset, provider location cloud → exposed on both sides.
    from modelman.registry import ProviderEntry

    model = _model("handmade/x", "handmade", "x")
    registry = _registry_with_providers(
        ProviderEntry(id="handmade", name="Handmade", location="cloud")
    )
    state = StateStore()
    state.set("handmade/x", ModelState(ready=False, litellm_exposed=True))
    assert is_effectively_exposed(model, state, registry=registry) is True


def test_passes_ready_gate_provider_cloud_not_ready():
    from modelman.registry import ProviderEntry

    model = _model("handmade/x", "handmade", "x")
    registry = _registry_with_providers(
        ProviderEntry(id="handmade", name="Handmade", location="cloud")
    )
    state = StateStore()
    state.set("handmade/x", ModelState(ready=False, litellm_exposed=True))
    assert passes_ready_gate(model, state, registry) is True
```

- [ ] **Step 2: Run the new tests to verify they fail**

Run: `cd modelman && uv run pytest tests/test_litellm.py -k "provider_location or unknown_provider or provider_cloud" -v`
Expected: FAIL with `TypeError: ... takes 1 positional argument but 2 were given` (or missing `registry`).

- [ ] **Step 3: Implement the change in `modelman/src/modelman/litellm.py`**

Replace `is_cloud_effective` with:

```python
def is_cloud_effective(model: ModelEntry, registry: Registry) -> bool:
    """True when a model should be exempt from the ready gate.

    A model is "effectively cloud" for exposure purposes when any of:
    - its provider policy declares it cloud (openrouter), or
    - the model itself is explicitly marked `location = "cloud"`, or
    - its provider's registry entry is `location = "cloud"` — the same
      model-then-provider resolution wt's `ResolveLocation` applies
      (issue #46 parity).

    An unknown provider id (hand-edited registry) is treated as
    not-cloud, mirroring `is_cloud`'s conservative fallback.

    Note: native providers are excluded upstream — `is_effectively_exposed`
    short-circuits on native models before this predicate runs.
    """
    if is_cloud(model.provider_id) or model.location == "cloud":
        return True
    try:
        return registry.provider(model.provider_id).location == "cloud"
    except KeyError:
        return False
```

Change `passes_ready_gate`'s signature and body (keep the docstring, add the parameter and note):

```python
def passes_ready_gate(
    model: ModelEntry,
    state: StateStore,
    registry: Registry,
    ready_override: bool | None = None,
) -> bool:
```

and its final line becomes:

```python
    return ready or is_cloud_effective(model, registry)
```

(In the docstring's Args section add: `registry: The model registry, for provider-location resolution.`)

Change `is_effectively_exposed`'s signature to:

```python
def is_effectively_exposed(
    model: ModelEntry,
    state: StateStore,
    registry: Registry,
    exposed_override: bool | None = None,
    ready_override: bool | None = None,
) -> bool:
```

and its final `return` line becomes:

```python
    return passes_ready_gate(model, state, registry, ready_override=ready_override)
```

(In its docstring Args section add the same `registry` line.)

Update the `_validated_entry` call site (~line 516) from:

```python
    if not passes_ready_gate(model, state):
```

to:

```python
    if not passes_ready_gate(model, state, registry):
```

- [ ] **Step 4: Update the TUI call sites in `modelman/src/modelman/screens/models.py`**

All three call `passes_ready_gate`/`is_effectively_exposed` with `self.registry` available (set in `__init__` at line 193).

At ~line 376 (`EXPOSED` column), change:

```python
                    if is_effectively_exposed(
                        m,
                        self.state,
                        exposed_override=exposed_override,
                        ready_override=ready_override,
                    )
```

to:

```python
                    if is_effectively_exposed(
                        m,
                        self.state,
                        self.registry,
                        exposed_override=exposed_override,
                        ready_override=ready_override,
                    )
```

At ~line 430 (`_enforce_expose_ready_rule`) and ~line 550 (`action_toggle_expose`), change both occurrences of:

```python
        if <cond> and not passes_ready_gate(
            entry,
            self.state,
            ready_override=self._projected_ready(mid),
        ):
```

to insert `self.registry,` after `self.state,` (identical edit in both places).

- [ ] **Step 5: Run the modelman test suite**

Run: `cd modelman && make test`
Expected: PASS. If any other callers or tests break on the new signatures (grep `grep -rn "passes_ready_gate\|is_effectively_exposed\|is_cloud_effective" src tests --include='*.py' | grep -v pyc`), update them the same way — every caller has a `Registry` in scope; if one genuinely does not, construct it from the loaded registry rather than making the parameter optional.

- [ ] **Step 6: Run the contract tests (now unblocked from Task 1)**

Run: `cd modelman && uv run pytest tests/contracts/ -v`
Expected: PASS, including `test_fixture_pins_provider_location_inheritance`.

- [ ] **Step 7: Commit**

```bash
git add modelman/src/modelman/litellm.py modelman/src/modelman/screens/models.py modelman/tests/test_litellm.py
git commit -m "fix(modelman): inherit provider cloud location in exposure predicates (#46)"
```

---

### Task 3: Docs — make the "same rule" claim true

**Files:**
- Modify: `docs/guides/00-config-map.md` (~line 61)

- [ ] **Step 1: Update the exposure-predicate wording**

Change the bullet:

```markdown
- **Exposure predicate (both tools):** A model is effectively exposed iff `litellm_exposed = true` AND (`ready = true` OR `location = "cloud"`). Native models (provider `auth.type = "native"`) are always exposed — they cannot route through LiteLLM. Both `wt` and the TUI apply this same rule, so a model offered by `wt` always shows `Y` in the TUI's EXPOSED column.
```

to:

```markdown
- **Exposure predicate (both tools):** A model is effectively exposed iff `litellm_exposed = true` AND (`ready = true` OR effective location is cloud), where effective location resolves model `location` first, then the provider's `location` (issue #46 parity). Native models (provider `auth.type = "native"`) are always exposed — they cannot route through LiteLLM. Both `wt` and the TUI apply this same rule, so a model offered by `wt` always shows `Y` in the TUI's EXPOSED column.
```

- [ ] **Step 2: Check guide snapshot drift (per CLAUDE.md)**

Run: `git grep -n "litellm_exposed = " docs/guides/`
Verify no guide other than 00-config-map.md needs changes (the fixture/sample changes here don't touch `~/.config/local-ai/modelman.toml` state, so no drift is expected — this step is a safety check).

- [ ] **Step 3: Validate links and commit**

Run: `make check-links`
Expected: PASS.

```bash
git add docs/guides/00-config-map.md
git commit -m "docs: exposure rule resolves model-then-provider location (#46)"
```

---

### Task 4: Full verification

- [ ] **Step 1: modelman suite**

Run: `cd modelman && make check && make test`
Expected: PASS.

- [ ] **Step 2: wt suite**

Run: `cd wt && go build ./... && go vet ./... && go test ./...`
Expected: PASS.

- [ ] **Step 3: Root lint**

Run: `make lint`
Expected: PASS.

- [ ] **Step 4: Cross-check parity manually (optional but cheap)**

Confirm the #46 scenario resolves identically: the fixture model `pinned-cloud/contract-fixture:inherit` (no location, provider `location = "cloud"`, flag on, not ready) is exposed in `test_fixture_pins_provider_location_inheritance` (Python) and `TestRegistryFixtureProviderLocationInheritance` (Go). Both green = parity pinned from both directions.
