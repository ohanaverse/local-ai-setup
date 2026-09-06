# wt Exposure Predicate Alignment Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Align wt's exposure predicate with the TUI so both tools use the same rule: `is_native(provider) OR (litellm_exposed AND (ready OR model.location == "cloud"))`.

**Architecture:** Extend wt's modelman state reader to include `ready`, update `IsExposed()` to apply the cloud-location exemption, update contract fixtures and tests on both sides, and remove the "deliberate divergence" documentation.

**Tech Stack:** Go (wt), Python (modelman), TOML fixtures, markdown docs.

---

### Task 1: Extend modelman state struct with Ready field

**Files:**
- Modify: `wt/internal/config/modelman.go`
- Test: `wt/internal/config/modelman_test.go`

- [ ] **Step 1: Update the modelmanState struct to include Ready**

```go
// modelmanState mirrors the subset of ~/.config/local-ai/modelman.toml that
// wt needs read-only access to. The full file is owned by modelman.
type modelmanState struct {
	ModelState map[string]struct {
		LitellmExposed bool `toml:"litellm_exposed"`
		Ready          bool `toml:"ready"`
	} `toml:"model_state"`
}
```

- [ ] **Step 2: Update loadModelmanState to return the richer map**

```go
// loadModelmanState reads modelman.toml and returns a map of exposed model ids
// with their ready state. A missing file returns an empty map (every non-native
// model is unexposed).
func loadModelmanState() (map[string]struct {
	LitellmExposed bool
	Ready          bool
}, error) {
	path := ModelmanPath()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]struct {
			LitellmExposed bool
			Ready          bool
		}{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read modelman.toml: %w", err)
	}
	var s modelmanState
	if err := toml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse modelman.toml: %w", err)
	}
	out := make(map[string]struct {
		LitellmExposed bool
		Ready          bool
	}, len(s.ModelState))
	for id, st := range s.ModelState {
		out[id] = struct {
			LitellmExposed bool
			Ready          bool
		}{LitellmExposed: st.LitellmExposed, Ready: st.Ready}
	}
	return out, nil
}
```

- [ ] **Step 3: Run tests to verify the changes compile and pass**

```bash
cd wt && go test ./internal/config -v -run "TestLoadModelmanState"
```

Expected: All TestLoadModelmanState* tests pass.

- [ ] **Step 4: Commit**

```bash
cd wt
git add internal/config/modelman.go internal/config/modelman_test.go
git commit -m "refactor: extend modelman state with ready field"
```

---

### Task 2: Update Config.exposed and IsExposed() with new predicate

**Files:**
- Modify: `wt/internal/config/config.go`
- Test: `wt/internal/config/modelman_test.go`

- [ ] **Step 1: Update the exposed field type in Config struct**

```go
type Config struct {
	DefaultTag string          `toml:"default_tag"`
	Gateway    GatewayConfig   `toml:"gateway"`
	Providers  []Provider      `toml:"providers"`
	Models     []Model         `toml:"models"`
	Agents     []Agent         `toml:"agents"`
	exposed    map[string]struct {
		LitellmExposed bool
		Ready          bool
	} `toml:"-"` // from modelman.toml
}
```

- [ ] **Step 2: Update IsExposed() to implement the new predicate**

```go
// IsExposed reports whether m should appear in wt's model catalog.
// Native models are always exposed (they cannot route through LiteLLM).
// Non-native models require litellm_exposed AND (ready OR cloud location).
func (c *Config) IsExposed(m Model) bool {
	if m.Native {
		return true
	}
	st, ok := c.exposed[m.ID]
	if !ok || !st.LitellmExposed {
		return false
	}
	if m.Location == "cloud" {
		return true
	}
	return st.Ready
}
```

- [ ] **Step 3: Update SetExposedForTest to the new map shape**

```go
// SetExposedForTest replaces the in-memory exposed set. Tests only.
func (c *Config) SetExposedForTest(exposed map[string]struct {
	LitellmExposed bool
	Ready          bool
}) {
	c.exposed = exposed
}
```

- [ ] **Step 4: Update ExposeAllForTest to mark models ready**

```go
// ExposeAllForTest marks every non-native model in cfg as exposed and ready.
// Tests only.
func (c *Config) ExposeAllForTest() {
	if c.exposed == nil {
		c.exposed = make(map[string]struct {
			LitellmExposed bool
			Ready          bool
		})
	}
	for _, m := range c.Models {
		if !m.Native {
			c.exposed[m.ID] = struct {
				LitellmExposed bool
				Ready          bool
			}{LitellmExposed: true, Ready: true}
		}
	}
}
```

- [ ] **Step 5: Update finalizeCfg to handle the new return type**

The existing code already assigns the return value directly, so no change needed here since the types align.

- [ ] **Step 6: Add comprehensive IsExposed table test**

Add to `wt/internal/config/modelman_test.go`:

```go
// TestIsExposedPredicate implements the exposure rule:
// native OR (litellm_exposed AND (ready OR cloud location)).
func TestIsExposedPredicate(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("MODELMAN_REGISTRY", "")

	writeRegistry(t, dir, `
[[providers]]
id = "ollama"
name = "Ollama"
location = "local"
auth = { type = "none", base_url = "http://localhost:11434" }

[[providers]]
id = "openrouter"
name = "OpenRouter"
location = "cloud"
auth = { type = "api_key", secret_ref = "OPENROUTER_API_KEY" }

[[models]]
id = "ollama/native-model"
family = "native"
provider_id = "ollama"
model_name = "native-model"
location = "local"
tags = ["code"]

[[models]]
id = "ollama/local-flag-ready"
family = "local-flag-ready"
provider_id = "ollama"
model_name = "local-flag-ready"
location = "local"
tags = ["code"]

[[models]]
id = "ollama/local-flag-not-ready"
family = "local-flag-not-ready"
provider_id = "ollama"
model_name = "local-flag-not-ready"
location = "local"
tags = ["code"]

[[models]]
id = "openrouter/cloud-flag"
family = "cloud-flag"
provider_id = "openrouter"
model_name = "cloud-flag"
location = "cloud"
tags = ["code"]
`)

	cfgDir := Dir()
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte("default_tag = \"code\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Native model: always exposed regardless of flag
	// Local model with flag+ready: exposed
	// Local model with flag+not-ready: NOT exposed
	// Cloud model with flag (no ready key): exposed
	writeModelmanState(t, dir, `
[model_state]

[model_state."ollama/native-model"]
litellm_exposed = false
ready = false

[model_state."ollama/local-flag-ready"]
litellm_exposed = true
ready = true

[model_state."ollama/local-flag-not-ready"]
litellm_exposed = true
ready = false

[model_state."openrouter/cloud-flag"]
litellm_exposed = true
`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	byID := map[string]Model{}
	for _, m := range cfg.Models {
		byID[m.ID] = m
	}

	tests := []struct {
		id       string
		expected bool
		reason   string
	}{
		{"ollama/native-model", true, "native models are always exposed"},
		{"ollama/local-flag-ready", true, "flag + ready = exposed"},
		{"ollama/local-flag-not-ready", false, "flag + not-ready (local) = not exposed"},
		{"openrouter/cloud-flag", true, "cloud location exempts from ready gate"},
	}

	for _, tt := range tests {
		m := byID[tt.id]
		if got := cfg.IsExposed(m); got != tt.expected {
			t.Errorf("IsExposed(%q) = %v, want %v (%s)", tt.id, got, tt.expected, tt.reason)
		}
	}
}
```

- [ ] **Step 7: Run tests to verify IsExposed predicate works**

```bash
cd wt && go test ./internal/config -v -run "TestIsExposedPredicate"
```

Expected: PASS

- [ ] **Step 8: Run all config tests**

```bash
cd wt && go test ./internal/config -v
```

Expected: All tests pass (may need to update existing tests that use SetExposedForTest)

- [ ] **Step 9: Fix any failing tests from the type change**

Search for uses of `SetExposedForTest` and update them:

```bash
cd wt && grep -r "SetExposedForTest" --include="*_test.go"
```

Update each to use the new struct type.

- [ ] **Step 10: Commit**

```bash
cd wt
git add internal/config/config.go internal/config/modelman_test.go
git commit -m "feat: implement new exposure predicate with cloud exemption"
```

---

### Task 3: Update contract fixture with new rule examples

**Files:**
- Modify: `docs/contracts/modelman.sample.toml`

- [ ] **Step 1: Update the fixture header comment**

```toml
# Shared fixture for cross-language contract tests of modelman.toml —
# the per-machine mutable state file modelman writes and wt reads
# read-only (litellm_exposed and ready flags, to filter the model picker).
#
# Read by:
#   - wt/internal/config/modelman_fixture_test.go (Go)
#   - modelman/tests/contracts/test_modelman_fixture.py (Python)
#
# The point of this file is that a schema change on either side (e.g. wt
# renaming its litellm_exposed key, modelman moving model_state) makes
# both CI jobs fail in the same PR.
#
# Exposure rule (both tools): native OR (litellm_exposed AND (ready OR cloud location)).
# - Local model with litellm_exposed=true, ready=false → NOT exposed
# - Cloud model with litellm_exposed=true (no ready key) → exposed
```

- [ ] **Step 2: Verify the existing entries already cover the cases**

The fixture already has:
- `ollama/contract-fixture:local`: `ready = true`, `litellm_exposed = false` → not exposed (flag off)
- `ollama/contract-fixture:subscription`: `ready = true`, `litellm_exposed = true` → exposed
- `llamacpp/legacy-contract-fixture`: `downloaded = true`, `litellm_exposed = true` → exposed (legacy)
- `openrouter/contract-fixture:cloud`: no keys (defaults: ready=false, litellm_exposed=false) → not exposed

Add a new entry for the key case (local, flag on, not ready):

```toml
[model_state."ollama/contract-fixture:local-not-ready"]
ready = false
disk_path = "ollama:contract-fixture:local-not-ready"
size_bytes = 1073741824
litellm_exposed = true
```

And a cloud model with flag on but no ready key (already covered by `openrouter/contract-fixture:cloud` but needs the flag):

```toml
[model_state."openrouter/contract-fixture:cloud-exposed"]
litellm_exposed = true
```

- [ ] **Step 3: Commit**

```bash
git add docs/contracts/modelman.sample.toml
git commit -m "docs: update contract fixture with exposure predicate cases"
```

---

### Task 4: Update wt contract test to assert the new predicate

**Files:**
- Modify: `wt/internal/config/modelman_fixture_test.go`

- [ ] **Step 1: Update the test to check the new cases**

```go
// TestLoadModelmanStateMatchesSharedFixture guards wt's modelman.toml
// decoding against the shape modelman actually writes. The fixture at
// docs/contracts/modelman.sample.toml is also read by modelman's
// tests/contracts/test_modelman_fixture.py — a schema change not
// reflected in both tests fails both CI jobs in the same PR instead of
// wt's picker silently losing exposure state. wt only consumes the
// litellm_exposed and ready flags; every other field (disk_path,
// size_bytes, families) is modelman-only and must stay ignorable here.
func TestLoadModelmanStateMatchesSharedFixture(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("MODELMAN_REGISTRY", "")

	// ModelmanPath() resolves to $XDG_CONFIG_HOME/local-ai/modelman.toml,
	// so the fixture must be copied there — wt has no MODELMAN_STATE
	// override (a deliberate asymmetry: wt is a read-only consumer and
	// never needs to redirect the state file the way tests redirect the
	// registry).
	stateDir := filepath.Join(dir, "local-ai")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("../../../docs/contracts/modelman.sample.toml")
	if err != nil {
		t.Fatalf("read shared fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "modelman.toml"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	exposed, err := loadModelmanState()
	if err != nil {
		t.Fatalf("loadModelmanState() error: %v", err)
	}

	// Check the new predicate cases:
	// - ollama/contract-fixture:subscription (flag+ready) → should be in exposed map
	// - llamacpp/legacy-contract-fixture (flag+downloaded) → should be in exposed map
	// - ollama/contract-fixture:local (flag=false) → NOT in exposed map
	// - ollama/contract-fixture:local-not-ready (flag=true, ready=false) → in map but not exposed by IsExposed()
	// - openrouter/contract-fixture:cloud (no flag) → NOT in exposed map
	// - openrouter/contract-fixture:cloud-exposed (flag=true, no ready) → in map, cloud exempts from ready gate

	expectedExposed := map[string]bool{
		"ollama/contract-fixture:subscription": true,
		"llamacpp/legacy-contract-fixture":     true,
	}

	for id, shouldBeExposed := range expectedExposed {
		st, ok := exposed[id]
		if !ok {
			t.Errorf("expected %q in modelman state, got missing", id)
			continue
		}
		if shouldBeExposed && !st.LitellmExposed {
			t.Errorf("expected %q to have litellm_exposed=true", id)
		}
	}

	// Verify the local-not-ready case: flag on, ready off
	st, ok := exposed["ollama/contract-fixture:local-not-ready"]
	if !ok {
		t.Errorf("expected ollama/contract-fixture:local-not-ready in state")
	} else if !st.LitellmExposed {
		t.Errorf("expected ollama/contract-fixture:local-not-ready to have litellm_exposed=true")
	} else if st.Ready {
		t.Errorf("expected ollama/contract-fixture:local-not-ready to have ready=false")
	}

	// Verify cloud-exposed case: flag on, no ready key (defaults to false)
	st, ok = exposed["openrouter/contract-fixture:cloud-exposed"]
	if !ok {
		t.Errorf("expected openrouter/contract-fixture:cloud-exposed in state")
	} else if !st.LitellmExposed {
		t.Errorf("expected openrouter/contract-fixture:cloud-exposed to have litellm_exposed=true")
	}
	// ready defaults to false for missing key
}
```

- [ ] **Step 2: Run the test**

```bash
cd wt && go test ./internal/config -v -run "TestLoadModelmanStateMatchesSharedFixture"
```

Expected: PASS

- [ ] **Step 3: Commit**

```bash
cd wt
git add internal/config/modelman_fixture_test.go
git commit -m "test: update wt contract test for new exposure predicate"
```

---

### Task 5: Update modelman contract test to assert the new predicate

**Files:**
- Modify: `modelman/tests/contracts/test_modelman_fixture.py`

- [ ] **Step 1: Add assertions for the exposure predicate**

```python
from pathlib import Path

from modelman.state import load_state

FIXTURE = Path(__file__).resolve().parents[3] / "docs" / "contracts" / "modelman.sample.toml"


def test_load_state_matches_shared_fixture():
    """Guards modelman's modelman.toml schema against wt's Go reader
    (wt/internal/config/modelman_fixture_test.go reads the same file,
    for the litellm_exposed and ready flags its model picker filters on).
    A schema change not reflected in both tests fails both CI jobs.
    
    Exposure predicate (both tools): native OR (litellm_exposed AND (ready OR cloud location)).
    """
    state = load_state(path=FIXTURE)

    # Fully-populated model entry: every field modelman writes round-trips.
    local = state.get("ollama/contract-fixture:local")
    assert local.ready is True
    assert local.disk_path == "ollama:contract-fixture:local"
    assert local.size_bytes == 2147483648
    assert local.litellm_exposed is False

    sub = state.get("ollama/contract-fixture:subscription")
    assert sub.litellm_exposed is True
    assert sub.ready is True

    # Legacy spelling: `downloaded` must still be accepted as `ready`
    # (pre-registry files keep working; wt only reads the exposure flag).
    legacy = state.get("llamacpp/legacy-contract-fixture")
    assert legacy.ready is True
    assert legacy.disk_path == "/hf/cache/legacy-contract-fixture.q4.gguf"
    assert legacy.litellm_exposed is True

    # Bare entry with no keys: all defaults, not an error.
    cloud = state.get("openrouter/contract-fixture:cloud")
    assert cloud.ready is False
    assert cloud.disk_path is None
    assert cloud.size_bytes is None
    assert cloud.litellm_exposed is False

    # New case: local model with flag on but ready off
    local_not_ready = state.get("ollama/contract-fixture:local-not-ready")
    assert local_not_ready is not None
    assert local_not_ready.ready is False
    assert local_not_ready.litellm_exposed is True

    # New case: cloud model with flag on (no ready key)
    cloud_exposed = state.get("openrouter/contract-fixture:cloud-exposed")
    assert cloud_exposed is not None
    assert cloud_exposed.ready is False  # defaults to false
    assert cloud_exposed.litellm_exposed is True

    # Legacy families table stays loadable (display names moved to
    # registry.toml [[families]], but old entries must not break loads).
    family = state.families.get("contract-fixture")
    assert family is not None
    assert family.display_name == "Contract Fixture (legacy)"
```

- [ ] **Step 2: Run the test**

```bash
cd modelman && uv run pytest tests/contracts/test_modelman_fixture.py -v
```

Expected: PASS

- [ ] **Step 3: Commit**

```bash
cd modelman
git add tests/contracts/test_modelman_fixture.py
git commit -m "test: update modelman contract test for new exposure predicate"
```

---

### Task 6: Update documentation to remove divergence note

**Files:**
- Modify: `docs/guides/00-config-map.md`
- Modify: `modelman/CLAUDE.md`
- Modify: `wt/CLAUDE.md`

---

### Task 7: Post-review fixes — legacy `downloaded` fallback and provider location inheritance

**Discovered during `/code-review`.** The original implementation left two cross-tool divergences:

1. **Go dropped the legacy `downloaded` fallback.** Python's `state.py` reads `entry.get("ready", entry.get("downloaded", False))`, so legacy `modelman.toml` files with `downloaded=true` remain ready in modelman. wt's Go reader only looked at `ready`, decoding a missing key to `false`. Fix: add a `downloaded` field to the intermediate `modelmanState` struct and make `Ready = st.Ready || st.Downloaded`, matching Python.

2. **Cloud exemption used raw `m.Location`.** The predicate checked `m.Location == "cloud"`, but a model can inherit `cloud` from its provider (`ResolveLocation`). `validate()` already guarantees the location is resolvable, so the check should use the resolved value. Fix: in `IsExposed`, call `c.ResolveLocation(m)` (ignoring the error, since `validate()` already enforced resolvability) and compare to `"cloud"`.

**Files:**
- Modify: `wt/internal/config/modelman.go`
- Modify: `wt/internal/config/config.go`
- Modify: `wt/internal/config/modelman_fixture_test.go`
- Modify: `wt/internal/config/modelman_test.go` (add Ready assertion via downloaded fallback)

- [ ] **Step 1: Update docs/guides/00-config-map.md section on modelman.toml**

Find the section about `litellm_exposed` vs the TUI EXPOSED column and update it:

Old text (approximate location in the modelman.toml section):
> `litellm_exposed` vs the TUI EXPOSED column: wt's picker and the proxy itself read the flag alone — a model with `litellm_exposed = true` is offered by wt and routed by LiteLLM. The TUI EXPOSED column is stricter: it shows `Y` only when the flag is set AND `state.ready = true`.

Replace with:
> **Exposure predicate (both tools):** A model is effectively exposed iff `litellm_exposed = true` AND (`ready = true` OR `location = "cloud"`). Native models (provider `auth.type = "native"`) are always exposed — they cannot route through LiteLLM. Both wt and the TUI apply this same rule, so a model offered by wt always shows `Y` in the TUI's EXPOSED column.

- [ ] **Step 2: Update modelman/CLAUDE.md**

Search for any mention of exposure semantics and ensure it matches the new predicate. Add to the "Registry and state" or "litellm.py" section:

```markdown
**Exposure predicate:** A model is effectively exposed for LiteLLM routing iff:
- `litellm_exposed = true` AND
- (`ready = true` OR `model.location = "cloud"`)

Native models (provider `auth.type = "native"`) are always exposed — they bypass LiteLLM entirely. This rule is shared with wt (both read `modelman.toml` and apply the same predicate).
```

- [ ] **Step 3: Update wt/CLAUDE.md**

Search for exposure-related documentation and update. In the "Registry (modelman-owned)" section, add:

```markdown
**Exposure predicate (shared with modelman):** wt filters models using the same rule as the TUI:
- Native models: always exposed
- Non-native: `litellm_exposed = true` AND (`ready = true` OR `location = "cloud"`)

This ensures wt's model picker never offers a model the TUI would show as `–` in the EXPOSED column.
```

- [ ] **Step 4: Run check-links**

```bash
make check-links
```

Expected: No broken links

- [ ] **Step 5: Commit**

```bash
git add docs/guides/00-config-map.md modelman/CLAUDE.md wt/CLAUDE.md
git commit -m "docs: align exposure predicate docs across tools"
```

---

### Task 7: Run full verification

**Files:**
- All modified files

- [ ] **Step 1: Build and test wt**

```bash
cd wt && go build ./... && go vet ./... && go test ./...
```

Expected: All pass

- [ ] **Step 2: Build and test modelman**

```bash
cd modelman && make check && make test
```

Expected: All pass

- [ ] **Step 3: Run monorepo lint**

```bash
make lint
```

Expected: All shell scripts pass lint and links are valid

- [ ] **Step 4: Manual smoke test (optional but recommended)**

```bash
# Load the fixture into a temp location
cd wt
XDG_CONFIG_HOME=$(mktemp -d) go test ./internal/config -v -run "TestLoadModelmanStateMatchesSharedFixture"
```

- [ ] **Step 5: Final commit if any stragglers**

```bash
git status
git add -A
git commit -m "chore: final cleanup"
```

---

## Self-Review Checklist

**1. Spec coverage:**
- ✅ `wt/internal/config/modelman.go` — extended with Ready field (Change #1)
- ✅ `wt/internal/config/config.go` — updated IsExposed() predicate (Change #2)
- ✅ `docs/contracts/modelman.sample.toml` — added fixture entries (Change #3)
- ✅ `wt/internal/config/modelman_fixture_test.go` — updated assertions (Change #4)
- ✅ `modelman/tests/contracts/test_modelman_fixture.py` — updated assertions (Change #4)
- ✅ `wt/internal/config/modelman_test.go` — added IsExposed table test (Change #5)
- ✅ `docs/guides/00-config-map.md` — removed divergence note (Change #6)
- ✅ `modelman/CLAUDE.md` and `wt/CLAUDE.md` — cross-referenced rule (Change #6)

**2. Placeholder scan:** No TBD/TODO/fill-in patterns found.

**3. Type consistency:** 
- `modelmanState.ModelState` values: `struct { LitellmExposed bool; Ready bool }`
- `Config.exposed`: `map[string]struct { LitellmExposed bool; Ready bool }`
- `loadModelmanState()` return type matches `Config.exposed`
- `SetExposedForTest()` parameter type matches
- All uses of `st.LitellmExposed` and `st.Ready` are consistent

**4. Predicate implementation matches spec:**
```
if m.Native { return true }
st, ok := c.exposed[m.ID]
if !ok || !st.LitellmExposed { return false }
if m.Location == "cloud" { return true }
return st.Ready
```
This is exactly: `is_native(provider) OR (litellm_exposed AND (ready OR model.location == "cloud"))`

---

Plan complete and saved to `docs/superpowers/plans/2026-09-07-wt-exposure-predicate.md`. Two execution options:

**1. Subagent-Driven (recommended)** - I dispatch a fresh subagent per task, review between tasks, fast iteration

**2. Inline Execution** - Execute tasks in this session using executing-plans, batch execution with checkpoints

**Which approach?**
