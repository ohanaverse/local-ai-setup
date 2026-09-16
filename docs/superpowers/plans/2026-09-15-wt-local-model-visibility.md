# wt Local-Model Visibility Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [x]`) syntax for tracking.

**Goal:** Make wt's model picker show a local model whenever it's live-verified `running`, regardless of its `exposed`/`ready` flags — while keeping LiteLLM-forced routes (e.g. `claude`+`mtplx`) working by having `modelman start` keep `exposed` in sync automatically.

**Architecture:** Two independent, mechanically-separate changes: (1) `wt/internal/config/config.go`'s `IsExposed` gets a local-model bypass so wt's Stage-1 catalog filter (`EligibleModelsIn`) stops excluding local models on `exposed`/`ready` — Stage-2 (`internal/localgate.Apply`/`FilterToRunningLocal`, unchanged) remains the sole local-visibility gate. (2) `modelman`'s `local_control.start_local_model()` calls `expose_model()` alongside its `running=True` flag write (both on a fresh start and on the idempotent already-running path) so LiteLLM's `model_list` stays in sync without a separate manual `modelman expose` step; failures degrade to a warning, never abort the start.

**Tech Stack:** Go 1.26 (wt), Python 3.13 (modelman), TOML config files, LiteLLM YAML config.

**Spec:** `docs/superpowers/specs/2026-09-15-wt-local-model-visibility-design.md`

## Global Constraints

- Cloud and native model exposure semantics are unchanged — only local-model handling changes.
- `stop_local_model()` must NOT be changed — `exposed` stays sticky after a stop.
- An expose failure inside `start_local_model()` must never abort an otherwise-successful start — it degrades to a warning string on `StartResult.warnings`.
- No test may write to, or read from, the developer's real `~/.config/litellm/config.yaml` or `~/.config/local-ai/*` — every test must redirect via existing env-var/tmp_path conventions.
- Run only the test files/packages touched by each task during that task; run the full aggregate check (`make test-all` from the monorepo root, or the two languages' full suites) only in the final task.

---

### Task 1: wt — local models bypass the exposed/ready predicate

**Files:**
- Modify: `wt/internal/config/config.go:550-569` (`IsExposed`)
- Modify: `wt/internal/config/modelman_test.go:358-488` (`TestIsExposedPredicate` — doc comment, fixture, state comment, test table)
- Modify: `wt/internal/localgate/localgate_test.go` (new test, appended at end of file)

**Interfaces:**
- Consumes: `Config.ResolveLocation(m Model) (Location, error)` (`wt/internal/config/config.go:676`), `Config.exposed map[string]ExposureEntry`, `LocationLocal`/`LocationCloud` constants — all pre-existing, unchanged.
- Produces: `Config.IsExposed(m Model) bool` keeps its exact signature; only its local-model branch changes. `EligibleModelsIn` (config.go:897, unchanged) and `internal/localgate.Apply`/`FilterToRunningLocal` (unchanged) are the callers/consumers that pick up the new behavior automatically.

**Contract fixture note:** the spec's testing section flags `docs/contracts/modelman.sample.toml` and its two per-language contract tests (`wt/internal/config/modelman_fixture_test.go`, `modelman/tests/contracts/test_modelman_fixture.py`) as needing a check. Both were read during planning: they assert on the raw parsed `Exposed`/`Ready`/`Running` struct fields only (e.g. `models["ollama/contract-fixture:local"].Exposed` — the fixture already has a `running=true, exposed=false` local entry), never on the derived `IsExposed()` predicate. Neither file needs a change for this task — no step below touches them.

- [x] **Step 1: Update `TestIsExposedPredicate`'s doc comment and fixture to describe/exercise the new contract (will fail against current code)**

In `wt/internal/config/modelman_test.go`, replace the function's doc comment (lines 358-361):

```go
// TestIsExposedPredicate implements the exposure rule:
// native OR (exposed AND (ready OR cloud location)).
// Cloud location may be inherited from the provider even when the model row
// omits its own `location` key.
```

with:

```go
// TestIsExposedPredicate implements the exposure rule (2026-09-15 local-
// model visibility design): native OR local OR (exposed AND (ready OR
// cloud location)). Local models bypass the exposed/ready check entirely
// here — their catalog membership is governed solely by the live-verified
// running gate (internal/localgate), pinned end-to-end by
// TestLocalModelVisibilityIgnoresExposedFlag in internal/localgate. Cloud
// location may be inherited from the provider even when the model row
// omits its own `location` key.
```

Add one new model to the fixture registry (after the `ollama/local-flag-not-ready` model block, before the `openrouter/cloud-flag` block):

```toml
[[models]]
id = "ollama/local-flag-unexposed"
family = "local-flag-unexposed"
provider_id = "ollama"
model_name = "local-flag-unexposed"
location = "local"
tags = ["code"]
```

Replace the inline comment block above `writeModelmanState` (lines 434-438):

```go
	// Native model: always exposed regardless of flag
	// Local model with flag+ready: exposed
	// Local model with flag+not-ready: NOT exposed
	// Cloud model with flag (no ready key): exposed
	// Cloud-inherited model with flag (no ready key, no model location): exposed
```

with:

```go
	// Native model: always exposed regardless of flag
	// Local model with flag+ready: exposed (unchanged)
	// Local model with flag+not-ready: exposed too (2026-09-15: local
	//   models bypass the ready gate — running-verification is the real gate)
	// Local model with exposed=false: exposed too (bypasses the exposed
	//   flag itself, not just ready)
	// Cloud model with flag (no ready key): exposed
	// Cloud-inherited model with flag (no ready key, no model location): exposed
```

Add a new `model_state` block to the same TOML literal, right after `[model_state."ollama/local-flag-not-ready"]`'s two lines:

```toml
[model_state."ollama/local-flag-unexposed"]
exposed = false
ready = true
```

Update the test table (replace the two existing local rows and add one):

```go
	tests := []struct {
		id       string
		expected bool
		reason   string
	}{
		{"native-provider/native-model", true, "native models are always exposed"},
		{"ollama/local-flag-ready", true, "local models are exposed to wt regardless of the exposed/ready flags"},
		{"ollama/local-flag-not-ready", true, "local models bypass the ready gate — visibility is governed by the running probe, not this predicate"},
		{"ollama/local-flag-unexposed", true, "local models bypass the exposed flag entirely — visibility is governed by the running probe, not this predicate"},
		{"openrouter/cloud-flag", true, "cloud location exempts from ready gate"},
		{"openrouter/cloud-inherited", true, "cloud location inherited from provider exempts from ready gate"},
	}
```

- [x] **Step 2: Run the test to confirm it fails against current code**

```bash
cd wt && go test ./internal/config -run TestIsExposedPredicate -v
```

Expected: FAIL — `IsExposed("ollama/local-flag-not-ready")` currently returns `false`, and the new `ollama/local-flag-unexposed` case also returns `false`, but both now want `true`.

- [x] **Step 3: Implement the local-model bypass in `IsExposed`**

In `wt/internal/config/config.go`, replace (lines 550-569):

```go
// IsExposed reports whether m should appear in wt's model catalog.
// Native models are always exposed (they cannot route through LiteLLM).
// Non-native models require exposed AND (ready OR cloud location).
//
// The cloud-location check uses ResolveLocation: a model may omit its own
// `location` and inherit it from the provider. validate() already guarantees
// the location is resolvable, so an error here is treated as non-cloud.
func (c *Config) IsExposed(m Model) bool {
	if m.Native {
		return true
	}
	st, ok := c.exposed[m.ID]
	if !ok || !st.Exposed {
		return false
	}
	if loc, err := c.ResolveLocation(m); err == nil && loc == "cloud" {
		return true
	}
	return st.Ready
}
```

with:

```go
// IsExposed reports whether m should appear in wt's model catalog.
//
// Native models are always exposed (they cannot route through LiteLLM).
//
// Local models are always exposed here too (2026-09-15 local-model
// visibility design): their catalog membership is governed entirely by
// the live-verified running gate (internal/localgate.Apply /
// FilterToRunningLocal), applied downstream of this Stage-1 check, not by
// the exposed/ready flags in modelman.toml. modelman's start_local_model
// keeps `exposed` in sync on start so LiteLLM-forced routes still work —
// see docs/superpowers/specs/2026-09-15-wt-local-model-visibility-design.md.
//
// Cloud (and any model whose location cannot be resolved — a registry
// data gap, treated conservatively as non-local) requires exposed AND
// (ready OR cloud location), unchanged from before. The cloud-location
// check uses ResolveLocation: a model may omit its own `location` and
// inherit it from the provider.
func (c *Config) IsExposed(m Model) bool {
	if m.Native {
		return true
	}
	loc, locErr := c.ResolveLocation(m)
	if locErr == nil && loc == LocationLocal {
		return true
	}
	st, ok := c.exposed[m.ID]
	if !ok || !st.Exposed {
		return false
	}
	if locErr == nil && loc == LocationCloud {
		return true
	}
	return st.Ready
}
```

- [x] **Step 4: Run the test to confirm it passes**

```bash
cd wt && go test ./internal/config -run TestIsExposedPredicate -v
```

Expected: PASS.

- [x] **Step 5: Run the full `internal/config` package test suite to check for regressions**

```bash
cd wt && go test ./internal/config -v 2>&1 | tail -60
```

Expected: PASS. `TestIsExposedNativeAlways`, `TestIsExposedNonNativeRequiresFlag`, `TestRegistryFixtureNativeExposure`, `TestRegistryFixtureProviderLocationInheritance` are all unaffected — they never set `Location`/`Providers` such that `ResolveLocation` resolves to `"local"` (`TestIsExposedNonNativeRequiresFlag`'s models have no provider registered at all, so `ResolveLocation` errors and falls through to the old cloud/native check unchanged; `TestRegistryFixtureProviderLocationInheritance`'s fixture model resolves to `"cloud"` via its provider, also unaffected).

- [x] **Step 6: Write the failing end-to-end integration test in `internal/localgate`**

Append to `wt/internal/localgate/localgate_test.go`:

```go
// TestLocalModelVisibilityIgnoresExposedFlag pins the 2026-09-15 local-
// model visibility design end-to-end: a local model with exposed=false
// must still surface through the full wt picker pipeline
// (EligibleModelsIn's Stage-1 filter, then Apply's Stage-2 running-
// verified gate) as long as it's actually running, and must NOT surface
// when it isn't running — proving `running` alone, not `exposed`, is the
// real local-model visibility gate.
func TestLocalModelVisibilityIgnoresExposedFlag(t *testing.T) {
	srv := httptest.NewServer(modelsHandler("model-a"))
	defer srv.Close()
	defer SetOmlxProbeURLForTest(srv.URL)()

	cfg := &config.Config{
		Agents: []config.Agent{
			{Name: "testagent", SupportedProviders: []string{"omlx"}},
		},
		Models: []config.Model{
			{ID: "omlx/model-a", ModelName: "model-a", ProviderID: "omlx", Location: config.LocationLocal},
		},
	}
	cfg.SetExposedForTest(map[string]config.ExposureEntry{}) // nothing exposed

	t.Run("running and unexposed is eligible end-to-end", func(t *testing.T) {
		cfg.SetLocalRunningForTest("omlx/model-a")
		eligible, err := cfg.EligibleModels("testagent", "", "")
		if err != nil {
			t.Fatalf("EligibleModels() error: %v", err)
		}
		if len(eligible) != 1 || eligible[0].ID != "omlx/model-a" {
			t.Fatalf("Stage 1 eligible = %v, want [omlx/model-a] despite exposed=false", idsOf(eligible))
		}
		res := Apply(cfg, eligible, "")
		if got := idsOf(res.Eligible); len(got) != 1 || got[0] != "omlx/model-a" {
			t.Errorf("Stage 2 Eligible = %v, want [omlx/model-a]", got)
		}
	})

	t.Run("not running and unexposed is excluded by Stage 2", func(t *testing.T) {
		cfg.SetLocalRunningForTest() // nothing running
		eligible, err := cfg.EligibleModels("testagent", "", "")
		if err != nil {
			t.Fatalf("EligibleModels() error: %v", err)
		}
		if len(eligible) != 1 {
			t.Fatalf("Stage 1 eligible = %v, want [omlx/model-a] (Stage 1 never checks running)", idsOf(eligible))
		}
		res := Apply(cfg, eligible, "")
		if len(res.Eligible) != 0 {
			t.Errorf("Stage 2 Eligible = %v, want none (not running)", idsOf(res.Eligible))
		}
	})
}
```

This test would already pass once Step 3 lands (it exercises the same `IsExposed` code path Step 1-4 already proved), so there is no separate red/green cycle for it — it's an additional end-to-end pin, not a new behavior. Confirm this directly:

```bash
cd wt && go test ./internal/localgate -run TestLocalModelVisibilityIgnoresExposedFlag -v
```

Expected: PASS (already, since Task 1 Step 3 is complete by this point).

- [x] **Step 7: Run the full wt test suite**

```bash
cd wt && go test ./... && go vet ./...
```

Expected: PASS, no vet issues.

- [x] **Step 8: Commit**

```bash
cd wt
git add internal/config/config.go internal/config/modelman_test.go internal/localgate/localgate_test.go
git commit -m "$(cat <<'EOF'
wt: local models bypass exposed/ready — running is the sole visibility gate

Local-model catalog membership in wt's picker no longer requires
exposed/ready; the existing live-verified running gate
(internal/localgate) is now the sole local-visibility check. Cloud and
native model handling is unchanged.

Implements docs/superpowers/specs/2026-09-15-wt-local-model-visibility-design.md,
completes plan item #1.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: modelman — `start_local_model` keeps `exposed` in sync

**Files:**
- Modify: `modelman/tests/conftest.py` (new autouse fixture)
- Modify: `modelman/tests/test_local_control.py` (new tests)
- Modify: `modelman/src/modelman/local_control.py` (import, new helper, two call sites)

**Interfaces:**
- Consumes: `expose_model(registry: Registry, state: StateStore, model_id: str, litellm_path: Path) -> list[str]` and `ExposeError`/`LiteLLMConfigError` (`modelman/src/modelman/litellm.py`), `default_litellm_config_path() -> Path`, `ModelState`/`StateStore`/`locked_state`/`load_state` (`modelman/src/modelman/state.py`) — all pre-existing.
- Produces: a new private helper `_expose_for_start(registry: Registry, state: StateStore, model_id: str, litellm_path: Path | None) -> list[str]` in `local_control.py`, consumed by `start_local_model`'s two success paths only.

- [x] **Step 1: Add the autouse fixture that keeps `expose_model` calls hermetic**

In `modelman/tests/conftest.py`, add after the existing `_never_restart_live_proxy` fixture (around line 38):

```python
@pytest.fixture(autouse=True)
def _default_litellm_config(monkeypatch, tmp_path):
    """start_local_model's auto-expose (2026-09-15 local-model visibility
    design) resolves an unset litellm_path via default_litellm_config_path(),
    which would otherwise read/write the developer's real
    ~/.config/litellm/config.yaml whenever a test starts a ready, non-cloud
    local model without passing its own litellm_path. Point the default at
    a scratch file instead; tests that pass litellm_path explicitly are
    unaffected — that argument always wins over this env-var default.
    """
    path = tmp_path / "auto-litellm-config.yaml"
    path.write_text("model_list: []\n")
    monkeypatch.setenv("MODELMAN_LITELLM_CONFIG", str(path))
```

This has no independent test — it's infrastructure the rest of this task's tests rely on to stay hermetic without each needing its own `litellm_path`. Verify it doesn't break anything yet:

```bash
cd modelman && uv run pytest tests/test_local_control.py -q
```

Expected: PASS (identical to before this step — the fixture only changes an env var no code reads yet).

- [x] **Step 2: Write the failing tests for the new auto-expose behavior**

Add to `modelman/tests/test_local_control.py` (near the other `test_start_*` tests, e.g. after `test_start_already_running_and_probed_serving_is_idempotent`):

```python
def test_start_exposes_previously_unexposed_ready_model(tmp_path):
    # The motivating bug: a model started via `modelman start` but never
    # separately exposed showed as running in modelman's own TUI but
    # never appeared in wt's picker, because wt used to also require
    # exposed=true for local models. Under the 2026-09-15 design wt no
    # longer checks `exposed` for local models at all, so the flag's only
    # remaining job is keeping LiteLLM's model_list in sync for agents
    # whose route is forced through the proxy — a fresh start of an
    # already-ready, not-yet-exposed model must expose it too, with no
    # separate `modelman expose` step required.
    state_path = tmp_path / "modelman.toml"
    store = StateStore()
    store.set("ollama/qwen3.8:27b-mlx", ModelState(ready=True, exposed=False))
    save_state(store, state_path)
    litellm_path = tmp_path / "config.yaml"
    litellm_path.write_text("model_list: []\n")

    result = start_local_model(
        _registry(), "ollama/qwen3.8:27b-mlx", state_path, litellm_path=litellm_path
    )

    assert result.already_running is False
    state = load_state(state_path)
    assert state.get("ollama/qwen3.8:27b-mlx").running is True
    assert state.get("ollama/qwen3.8:27b-mlx").exposed is True


def test_start_already_running_unexposed_model_gets_exposed(tmp_path):
    # Re-running `modelman start` on a model that's already running but
    # was never exposed (drifted state from before this feature, or a
    # manually-cleared exposed flag on a still-running model) is the
    # remediation path — it must expose the model without a full
    # teardown/restart, since the probe already confirms it's healthy.
    state_path = tmp_path / "modelman.toml"
    store = StateStore()
    store.set("ollama/qwen3.8:27b-mlx", ModelState(ready=True, exposed=False, running=True))
    save_state(store, state_path)
    litellm_path = tmp_path / "config.yaml"
    litellm_path.write_text("model_list: []\n")

    with patch("modelman.local_control._probe_running", return_value=True):
        result = start_local_model(
            _registry(), "ollama/qwen3.8:27b-mlx", state_path, litellm_path=litellm_path
        )

    assert result.already_running is True
    assert load_state(state_path).get("ollama/qwen3.8:27b-mlx").exposed is True


def test_start_succeeds_with_warning_when_expose_fails(tmp_path):
    # A model that isn't `ready` yet can still be started — expose_model's
    # ready gate rejects it, but that must degrade to a warning rather
    # than aborting an otherwise-successful start: wt's local-model
    # visibility no longer depends on `exposed` at all, only on the
    # running probe, so a failed expose must not block getting the model
    # running.
    state_path = _state_path(tmp_path, {})  # ready defaults to False

    result = start_local_model(_registry(), "ollama/qwen3.8:27b-mlx", state_path)

    assert result.already_running is False
    assert load_state(state_path).get("ollama/qwen3.8:27b-mlx").running is True
    assert any("could not be exposed" in w for w in result.warnings)


def test_stop_local_model_does_not_clear_exposed_flag(tmp_path):
    # exposed is sticky across stop/start cycles — stopping a model's
    # process must not revert its LiteLLM model_list membership, so
    # restarting it later doesn't need to re-expose.
    state_path = tmp_path / "modelman.toml"
    store = StateStore()
    store.set("ollama/qwen3.8:27b-mlx", ModelState(ready=True, exposed=True, running=True))
    save_state(store, state_path)

    with patch("modelman.local_control._stop_ollama_model") as mock_stop_ollama:
        stop_local_model("ollama/qwen3.8:27b-mlx", state_path)
    mock_stop_ollama.assert_called_once_with("qwen3.8:27b-mlx")

    state = load_state(state_path)
    assert state.get("ollama/qwen3.8:27b-mlx").running is False
    assert state.get("ollama/qwen3.8:27b-mlx").exposed is True
```

- [x] **Step 3: Run the new tests to confirm the first three fail (the fourth passes trivially — it pins `stop_local_model`, which this task does not change)**

```bash
cd modelman && uv run pytest tests/test_local_control.py -k "exposes_previously_unexposed or already_running_unexposed or succeeds_with_warning or does_not_clear_exposed" -v
```

Expected: `test_start_exposes_previously_unexposed_ready_model`, `test_start_already_running_unexposed_model_gets_exposed`, and `test_start_succeeds_with_warning_when_expose_fails` FAIL (no code calls `expose_model` from `start_local_model` yet, so `exposed` stays `False` and no warning is added). `test_stop_local_model_does_not_clear_exposed_flag` PASSES already.

- [x] **Step 4: Add `LiteLLMConfigError` to the `.litellm` import**

In `modelman/src/modelman/local_control.py`, replace:

```python
from .litellm import ExposeError, default_litellm_config_path, expose_model
```

with:

```python
from .litellm import ExposeError, LiteLLMConfigError, default_litellm_config_path, expose_model
```

- [x] **Step 5: Add the `_expose_for_start` helper**

In `modelman/src/modelman/local_control.py`, add immediately before `def start_local_model(` (currently line 746):

```python
def _expose_for_start(
    registry: Registry,
    state: StateStore,
    model_id: str,
    litellm_path: Path | None,
) -> list[str]:
    """Best-effort expose of a model that is (or is about to be) running,
    so LiteLLM's model_list stays in sync for agents whose route to it is
    forced through the proxy — see
    docs/superpowers/specs/2026-09-15-wt-local-model-visibility-design.md.

    Mutates `state.models[model_id]` in place on success (mirrors
    `_resolve_or_register`'s own expose_model call above) but never
    raises: an expose failure degrades to a warning string rather than
    blocking an otherwise-successful start, since wt's local-model picker
    no longer depends on `exposed` at all.
    """
    try:
        return expose_model(registry, state, model_id, litellm_path or default_litellm_config_path())
    except (ExposeError, LiteLLMConfigError) as exc:
        return [
            f"{model_id} is running but could not be exposed to LiteLLM: {exc} — "
            f"agents whose route to it is forced through LiteLLM won't reach it "
            f"until you run `modelman expose {model_id}`"
        ]
```

- [x] **Step 6: Wire the helper into the already-running branch**

In `start_local_model`, replace:

```python
    already = fresh_state.get(resolved_id).running
    if already:
        if _probe_running(model.provider_id, model.model_name, probe_origin):
            return StartResult(
                model_id=resolved_id, already_running=True,
                warnings=registration_warnings, other_running=other_running,
            )
        _clear_stale_running_flag(resolved_id, state_path)
```

with:

```python
    already = fresh_state.get(resolved_id).running
    if already:
        if _probe_running(model.provider_id, model.model_name, probe_origin):
            expose_warnings = _expose_for_start(registry, fresh_state, resolved_id, litellm_path)
            try:
                with locked_state(state_path) as fresh:
                    existing = fresh.models.get(resolved_id, ModelState())
                    fresh.models[resolved_id] = replace(
                        existing, exposed=fresh_state.models.get(resolved_id, existing).exposed
                    )
            except OSError as exc:
                expose_warnings = expose_warnings + [
                    f"{resolved_id}'s exposed flag could not be persisted: {exc}"
                ]
            return StartResult(
                model_id=resolved_id, already_running=True,
                warnings=registration_warnings + expose_warnings, other_running=other_running,
            )
        _clear_stale_running_flag(resolved_id, state_path)
```

- [x] **Step 7: Wire the helper into the fresh-start final block**

In `start_local_model`, replace:

```python
    try:
        with locked_state(state_path) as fresh:
            existing = fresh.models.get(resolved_id, ModelState())
            fresh.models[resolved_id] = replace(existing, running=True)
    except OSError as exc:
        raise LocalControlError(
            f"{resolved_id} started successfully but its running flag could not be "
            f"persisted: {exc} — wt's picker will not see it as running until this succeeds"
        ) from exc
    return StartResult(
        model_id=resolved_id, already_running=False, direct_url=direct_url,
        warnings=registration_warnings, other_running=other_running,
    )
```

with:

```python
    expose_warnings = _expose_for_start(registry, fresh_state, resolved_id, litellm_path)
    try:
        with locked_state(state_path) as fresh:
            existing = fresh.models.get(resolved_id, ModelState())
            fresh.models[resolved_id] = replace(
                existing,
                running=True,
                exposed=fresh_state.models.get(resolved_id, existing).exposed,
            )
    except OSError as exc:
        raise LocalControlError(
            f"{resolved_id} started successfully but its running flag could not be "
            f"persisted: {exc} — wt's picker will not see it as running until this succeeds"
        ) from exc
    return StartResult(
        model_id=resolved_id, already_running=False, direct_url=direct_url,
        warnings=registration_warnings + expose_warnings, other_running=other_running,
    )
```

- [x] **Step 8: Run the new tests to confirm they pass**

```bash
cd modelman && uv run pytest tests/test_local_control.py -k "exposes_previously_unexposed or already_running_unexposed or succeeds_with_warning or does_not_clear_exposed" -v
```

Expected: all four PASS.

- [x] **Step 9: Run the full `test_local_control.py` suite to check for regressions**

```bash
cd modelman && uv run pytest tests/test_local_control.py -q
```

Expected: PASS. Every other `test_start_*` test either reaches `_expose_for_start` and gets a harmless real-but-hermetic expose attempt (via Step 1's fixture) or a caught `ExposeError` (most models in `_registry()`/`_state_path()` default to `ready=False` unless a test explicitly sets it), and no test asserts `result.warnings == []` exactly (confirmed by inspection — grep `\.warnings` in the test file before this task found no such assertion).

- [x] **Step 10: Run the full modelman test suite**

```bash
cd modelman && make check && uv run pytest -k "not screen" -q
```

Expected: PASS, no lint/typecheck errors.

- [x] **Step 11: Commit**

```bash
cd modelman
git add tests/conftest.py tests/test_local_control.py src/modelman/local_control.py
git commit -m "$(cat <<'EOF'
modelman: start_local_model keeps exposed in sync for LiteLLM-forced routes

wt no longer requires exposed=true for local-model visibility, but
LiteLLM can still only route to what's in its model_list. start_local_model
now exposes a model alongside its running=True flag write (both on a
fresh start and the idempotent already-running path); an expose failure
degrades to a warning rather than blocking the start. stop_local_model is
unchanged — exposed stays sticky after a stop.

Implements docs/superpowers/specs/2026-09-15-wt-local-model-visibility-design.md,
completes plan item #2.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: Documentation updates

**Files:**
- Modify: `wt/CLAUDE.md` (Exposure predicate section)
- Modify: `modelman/CLAUDE.md` (Local-model lifecycle section)

**Interfaces:** None — prose only, no code interfaces.

- [x] **Step 1: Update wt/CLAUDE.md's Exposure predicate section**

Replace:

```markdown
**Exposure predicate (shared with modelman):** wt filters models using the same rule as the TUI:
- Native models (provider `auth.type = "native"`): always exposed
- Non-native: `exposed` true (legacy `litellm_exposed` still read, ORed) AND (`ready = true` OR `location = "cloud"`)

This ensures wt's model picker never offers a model the TUI would show as `–` in the EXPOSED column.
```

with:

```markdown
**Exposure predicate (2026-09-15 local-model visibility design):** wt's `IsExposed` decides Stage-1 (tag/family/provider) catalog membership:
- Native models (provider `auth.type = "native"`): always exposed.
- Local models (location resolves to `"local"`): always exposed here too — catalog membership for local models is governed entirely by the live-verified running gate below (`internal/localgate.Apply`/`FilterToRunningLocal`), not by `exposed`/`ready`. A model whose location can't be resolved (registry data gap) falls back to the cloud/native check below, fail-closed.
- Cloud (and any model whose location doesn't resolve to local): `exposed` true (legacy `litellm_exposed` still read, ORed) AND (`ready = true` OR `location = "cloud"`), unchanged from before.

This means wt's picker can now show a local model modelman's own TUI still renders `–` for in its EXPOSED column — that divergence is intentional for local models; `modelman start` keeps `exposed` in sync automatically so LiteLLM-forced routes (see the Agents table below) keep working without a separate manual expose step. See `docs/superpowers/specs/2026-09-15-wt-local-model-visibility-design.md`.
```

- [x] **Step 2: Update modelman/CLAUDE.md's Local-model lifecycle section**

In the paragraph beginning "`modelman start <model_id>` / `modelman stop <model_id>` / `modelman stop --all`, and the modelman TUI's `s` keybinding...", after the sentence ending "...wt's picker) verifies it with a live probe first.", insert:

```markdown
`start_local_model()` also exposes the model (flips `exposed=True` and writes its LiteLLM `model_list` entry, mirroring `modelman expose`) as part of every start — including the idempotent already-running path — so wt's picker, which no longer checks `exposed` for local models (see `wt/CLAUDE.md`'s Exposure predicate), still works for agents whose route to it is forced through LiteLLM; a failure to expose degrades to a warning rather than blocking the start. `stop_local_model()` leaves `exposed` untouched. See `docs/superpowers/specs/2026-09-15-wt-local-model-visibility-design.md`.
```

- [x] **Step 3: Check links**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup/.worktrees/local-model-visibility && make check-links
```

Expected: PASS (the new doc references point at the already-committed spec file from Task 0's brainstorming step).

- [x] **Step 4: Commit**

```bash
git add wt/CLAUDE.md modelman/CLAUDE.md
git commit -m "$(cat <<'EOF'
docs: describe the 2026-09-15 local-model visibility predicate

wt/CLAUDE.md's Exposure predicate and modelman/CLAUDE.md's Local-model
lifecycle sections now describe local models bypassing exposed/ready in
wt and start_local_model's auto-expose, matching the code changes in
plan items #1-2.

completes plan item #3.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: Final verification

**Files:** None modified.

**Interfaces:** None.

- [x] **Step 1: Run the full monorepo verification**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup/.worktrees/local-model-visibility && make test-all
```

Expected: PASS — lint, modelman `make check`/`make test`, wt `go build`/`vet`/`test` all green.

- [x] **Step 2: Manually re-verify the original motivating case, if the mtplx model from the bug report is still running**

```bash
cd wt && cat > /tmp/verify-local-visibility.go <<'EOF'
package main

import (
	"fmt"
	"os"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localgate"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Println("load error:", err)
		os.Exit(1)
	}
	for _, agent := range []string{"claude", "pi"} {
		models, err := cfg.EligibleModels(agent, "", "")
		if err != nil {
			fmt.Println(agent, "eligible error:", err)
			continue
		}
		result := localgate.Apply(cfg, models, "")
		for _, m := range result.Eligible {
			if m.ProviderID == "mtplx" {
				fmt.Println(agent, "gate-passed mtplx model:", m.ID)
			}
		}
	}
}
EOF
go run /tmp/verify-local-visibility.go
rm /tmp/verify-local-visibility.go
```

Expected: prints the running mtplx model id for both `claude` and `pi`, matching the manual verification already done during the original debugging session — this time without needing to first run `modelman expose` by hand.

- [x] **Step 3: Report completion**

No commit needed for this task — it's verification only. If Step 1 or 2 surfaces a regression, return to the relevant task above and fix it there (with its own commit), then re-run this task.
