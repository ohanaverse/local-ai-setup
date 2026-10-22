# Configured is exposed — Phase A Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the `exposed` concept for cloud models: every registry cloud model is in wt's catalog and routed through LiteLLM, kept current by a reconciling `wt litellm sync`; modelman's expose machinery is replaced by one sync call.

**Architecture:** wt gains an ownership marker (`model_info.wt_managed: true`) on every `config.yaml` row it writes and a reconciling `wt litellm sync` (desired set = registry cloud models + running local models; removes only rows it owns). modelman deletes its expose/unexpose code and calls `wt litellm sync` once after any change. A new contract fixture pins the cross-language catalog/pricing/routing predicates (#180).

**Tech Stack:** Go 1.26 (wt: cobra, yaml.v3, BurntSushi/toml), Python 3.13 (modelman: Typer, Textual, pytest, uv).

**Spec:** `docs/superpowers/specs/2026-10-02-configured-is-exposed-design.md` (Phase A sections A1–A4). Phase B gets its own plan after Phase A merges.

## Global Constraints

- wt deletes a `config.yaml` row only if it **owns** it: the row carries `model_info.wt_managed: true`, or its `model_name` is the id of a registry model wt manages (cloud or local). Every other row is hand-written and is never removed or rewritten.
- One proxy restart per sync, and only when `config.yaml` changed (existing `applyPlanned` behaviour).
- A wt failure during modelman's sync is a **warning**, never a failed command.
- `ready` stays for local models (download state); cloud models stop reading/writing it.
- No live model state in `docs/guides/` (root CLAUDE.md rule).
- Every Go `Test*` has a top-level `//` comment stating what it tests and why (wt/CLAUDE.md).
- modelman: run `make check` (ruff, format, mypy) before calling any task done; `ruff` B905 (`zip(strict=)`) is enforced.
- Each PR leaves `make test-all` (repo root) green.
- Commit messages end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Review Focus

1. **A hand-written row named like a deleted registry model** (e.g. an OpenRouter alias the user wrote whose id is not in the registry) must survive every sync — pinned in Task 3 (`TestSyncNeverTouchesHandWrittenRows`).
2. **A legacy unmarked local route for a stopped registry model** must still be removed by sync, as it is today — pinned in Task 3 (`TestSyncRemovesLegacyUnmarkedLocalRoute`).
3. **A registry `model_info` that itself sets `wt_managed: false`** must not let a hand edit disown a wt row — pinned in Task 2 (`TestBuildEntryMarkerWins`).
4. **`wt litellm sync` timing out from modelman** must warn, not crash or claim success — pinned in Task 7 (`test_sync_routes_timeout_warns`).
5. **A registry model whose provider entry is missing** (dangling `provider_id`, even with `location = "cloud"`) must stay out of wt's catalog and never be routed — pinned by the contract fixture in Task 1.

---

## File Structure

**wt (PR 1, PR 3)**
- `internal/config/config.go` — `InCatalog` (replaces `IsExposed`), `OpenRouterPriced`; later delete `ExposedFlag`/exposure map.
- `internal/config/catalog_predicates_fixture_test.go` (new) — loads the new contract fixture.
- `internal/agents/price_notice.go` — `HasOpenRouterPricedModel` delegates to `cfg.OpenRouterPriced`.
- `internal/litellm/entry.go` — stamps the marker.
- `internal/litellm/configfile.go` — `IsManaged`, `Rows`, `row`, `carryPreservedParams`.
- `internal/litellm/service.go` — `CloudModels`, `SyncPlan`, `PlanSync`, reconciling `Sync`.
- `internal/litellm/catalog_predicates_fixture_test.go` (new) — routable-cloud assertion.
- `cmd/wt/litellm.go` — `sync --dry-run`, `list` rows; PR 3 removes `expose|unexpose`.
- `internal/catalog/catalog.go`, `internal/tui/modeltable.go` — PR 3 removes the EXPOSED column.

**contracts**
- `docs/contracts/catalog-predicates.sample.toml` (new), `docs/contracts/catalog-predicates.expected.json` (new).
- `docs/contracts/litellm-cli.sample.json`, `docs/contracts/modelman.sample.toml` — updated.

**modelman (PR 2)**
- `src/modelman/wt_bridge.py` — `sync`, `parse_sync_plan`; drop `expose`/`unexpose`/`_reconcile_after_timeout`.
- `src/modelman/litellm.py` — `sync_routes`; delete expose machinery and predicates.
- `src/modelman/queue.py`, `src/modelman/main.py` — drop `exposes`; `run_queued_ops`/`run_tui` sync once; drop `expose`/`unexpose` commands; `refresh-prices` syncs.
- `src/modelman/screens/models.py`, `src/modelman/screens/forms.py` — drop `x`, EXPOSED, queued exposes.
- `src/modelman/local_control.py` — start/stop/stop-all sync instead of expose/unexpose.
- `src/modelman/ollama_catalog.py`, `src/modelman/ollama_catalog_cli.py` — drop `routes`/exposed section; sync at end.
- `src/modelman/benchmark/runner.py`, `src/modelman/benchmark/cli.py` — explicit models required.
- `src/modelman/state.py`, `src/modelman/sync.py` — drop `exposed`.
- `tests/conftest.py` — wt fake handles `sync`, records calls.

---

# PR 1 — wt: marker, reconciling sync, `InCatalog`, contract fixture

Branch: `feat/179-a1-wt-sync` from `main`.

### Task 1: Contract fixture + `InCatalog` + `OpenRouterPriced` (closes #180)

**Files:**
- Create: `docs/contracts/catalog-predicates.sample.toml`, `docs/contracts/catalog-predicates.expected.json`
- Create: `wt/internal/config/catalog_predicates_fixture_test.go`
- Create: `modelman/tests/contracts/test_catalog_predicates_fixture.py`
- Modify: `wt/internal/config/config.go` (`IsExposed` → `InCatalog`; add `OpenRouterPriced`; `EligibleModels` call site)
- Modify: `wt/internal/agents/price_notice.go` (`HasOpenRouterPricedModel`)
- Modify: tests referencing `IsExposed` (`wt/internal/config/eligible_test.go`, `config_test.go`, `ready_test.go`, `registry_fixture_test.go`)

**Interfaces:**
- Produces: `func (c *Config) InCatalog(m Model) bool`, `func (c *Config) OpenRouterPriced(m Model) bool`; fixture files read by Task 3 and by modelman.

- [ ] **Step 1: Write the fixture**

`docs/contracts/catalog-predicates.sample.toml`:

```toml
# Cross-language contract for the catalog / pricing / routing predicates
# (#179, #180). Each model exercises one branch; the expected id lists live in
# catalog-predicates.expected.json, read by:
#   - wt/internal/config/catalog_predicates_fixture_test.go  (in_catalog, openrouter_priced)
#   - wt/internal/litellm/catalog_predicates_fixture_test.go (routable_cloud)
#   - modelman/tests/contracts/test_catalog_predicates_fixture.py (openrouter_priced)
# A one-sided change to any predicate fails both CI jobs.

[[providers]]
id = "openrouter"
name = "OpenRouter"
location = "cloud"
[providers.auth]
type = "api_key"
base_url = "https://openrouter.ai/api/v1"
secret_ref = "OPENROUTER_API_KEY"

[[providers]]
id = "agy"
name = "Agy"
location = "cloud"
[providers.auth]
type = "native"

[[providers]]
id = "ollama"
name = "Ollama"
location = "local"
[providers.auth]
type = "none"
base_url = "http://localhost:11434"

# A non-native cloud provider with no LiteLLM mapping in wt's policy table.
[[providers]]
id = "acme"
name = "Acme"
location = "cloud"
[providers.auth]
type = "api_key"
base_url = "https://acme.example/v1"
secret_ref = "ACME_API_KEY"

[[models]]
id = "openrouter/vendor-x"
family = "fixture"
provider_id = "openrouter"
model_name = "vendor/x"

[[models]]
id = "agy/native"
family = "fixture"
provider_id = "agy"
model_name = "native"

# ollama cloud model: cloud location on the LOCAL ollama provider.
[[models]]
id = "ollama/glm:cloud"
family = "fixture"
provider_id = "ollama"
model_name = "glm:cloud"
location = "cloud"

# Inherits "cloud" from the acme provider.
[[models]]
id = "acme/model-a"
family = "fixture"
provider_id = "acme"
model_name = "vendor/model-a"

# Dangling provider_id: no [[providers]] entry named "ghost".
[[models]]
id = "ghost/model-g"
family = "fixture"
provider_id = "ghost"
model_name = "model-g"
location = "cloud"

[[models]]
id = "ollama/local:1"
family = "fixture"
provider_id = "ollama"
model_name = "local:1"
location = "local"
```

`docs/contracts/catalog-predicates.expected.json`:

```json
{
  "in_catalog": ["openrouter/vendor-x", "agy/native", "ollama/glm:cloud", "acme/model-a", "ollama/local:1"],
  "openrouter_priced": ["openrouter/vendor-x", "acme/model-a"],
  "routable_cloud": ["openrouter/vendor-x", "ollama/glm:cloud"]
}
```

- [ ] **Step 2: Write the failing Go test**

`wt/internal/config/catalog_predicates_fixture_test.go`:

```go
package config

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
)

const predicatesDir = "../../../docs/contracts/"

type predicateExpectations struct {
	InCatalog        []string `json:"in_catalog"`
	OpenRouterPriced []string `json:"openrouter_priced"`
	RoutableCloud    []string `json:"routable_cloud"`
}

func loadPredicateFixture(t *testing.T) (*Config, predicateExpectations) {
	t.Helper()
	t.Setenv("MODELMAN_REGISTRY", predicatesDir+"catalog-predicates.sample.toml")
	providers, models, err := loadRegistry()
	if err != nil {
		t.Fatalf("loadRegistry: %v", err)
	}
	cfg := &Config{Providers: providers, Models: models}
	deriveNative(cfg)
	raw, err := os.ReadFile(predicatesDir + "catalog-predicates.expected.json")
	if err != nil {
		t.Fatal(err)
	}
	var want predicateExpectations
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	return cfg, want
}

// TestCatalogPredicatesFixture pins InCatalog and OpenRouterPriced to the
// shared contract (#179, #180). modelman asserts the same openrouter_priced
// list against pricing._is_openrouter_priced, so a one-sided rule change —
// e.g. a new excluded provider kind in Python only — fails both CI jobs
// instead of silently making wt nag about refreshes with nothing to refresh.
func TestCatalogPredicatesFixture(t *testing.T) {
	cfg, want := loadPredicateFixture(t)
	var inCatalog, priced []string
	for _, m := range cfg.Models {
		if cfg.InCatalog(m) {
			inCatalog = append(inCatalog, m.ID)
		}
		if cfg.OpenRouterPriced(m) {
			priced = append(priced, m.ID)
		}
	}
	if !slices.Equal(inCatalog, want.InCatalog) {
		t.Errorf("in_catalog = %v, want %v", inCatalog, want.InCatalog)
	}
	if !slices.Equal(priced, want.OpenRouterPriced) {
		t.Errorf("openrouter_priced = %v, want %v", priced, want.OpenRouterPriced)
	}
}
```

- [ ] **Step 3: Run it — expect a build failure**

Run: `cd wt && go test ./internal/config -run TestCatalogPredicatesFixture`
Expected: FAIL — `cfg.InCatalog undefined`, `cfg.OpenRouterPriced undefined`.

- [ ] **Step 4: Implement**

In `wt/internal/config/config.go`, replace the whole `IsExposed` function (doc comment included) with:

```go
// InCatalog reports whether m appears in wt's model catalog (#179):
// configured means exposed. Native, local and cloud registry models are all
// in; the only exclusion is a registry data gap — a provider_id naming no
// provider, or no location on model or provider — which stays out,
// fail-closed, even when the model sets its own location.
func (c *Config) InCatalog(m Model) bool {
	if m.Native {
		return true
	}
	if c.ProviderByID(m.ProviderID) == nil {
		return false
	}
	_, err := c.ResolveLocation(m)
	return err == nil
}

// OpenRouterPriced reports whether m's price comes from OpenRouter — what
// `modelman refresh-prices` refreshes: an openrouter model, or a model of a
// non-native cloud provider. Keyed on the provider's location, not the
// model's, so ollama cloud models don't count. Mirrors modelman's
// pricing._is_openrouter_priced; both are pinned by
// docs/contracts/catalog-predicates.sample.toml.
func (c *Config) OpenRouterPriced(m Model) bool {
	if m.Native {
		return false
	}
	p := c.ProviderByID(m.ProviderID)
	if p != nil && p.Auth.Type == "native" {
		return false
	}
	if m.ProviderID == "openrouter" {
		return true
	}
	return p != nil && p.Location == LocationCloud
}
```

In `EligibleModels`, change `if !c.IsExposed(m) {` to `if !c.InCatalog(m) {`.

In `wt/internal/agents/price_notice.go`, replace the loop body of `HasOpenRouterPricedModel` so it delegates (keep the nil check and doc comment, updating "mirrors modelman's _is_openrouter_priced" to "delegates to config.OpenRouterPriced"):

```go
	for _, m := range cfg.Models {
		if cfg.OpenRouterPriced(m) {
			return true
		}
	}
	return false
```

Update the two comment-only mentions: `cmd/wt/smoke.go:208` and `internal/profiles/resolve.go:35` — replace `Config.IsExposed` with `Config.InCatalog`.

- [ ] **Step 5: Fix the tests that pinned the old rule**

Run: `cd wt && go vet ./... && go test ./internal/config ./internal/catalog ./internal/tui ./internal/agents ./cmd/wt`

Every `IsExposed(` call in tests becomes `InCatalog(`. A test that asserted a **cloud model with `exposed = false` (or no state row) is hidden** now pins removed behaviour: change its expectation to "included" and rewrite its `//` comment to say configured means exposed (#179). A test that asserted a dangling-provider model is included changes to excluded. Do not delete tests that assert native/local inclusion — they still hold.

Expected after edits: PASS.

- [ ] **Step 6: Write the Python side (openrouter_priced)**

`modelman/tests/contracts/test_catalog_predicates_fixture.py`:

```python
"""Cross-language contract for the OpenRouter-priced predicate (#180).

wt's config.OpenRouterPriced asserts the same expected list
(wt/internal/config/catalog_predicates_fixture_test.go), so a one-sided
change to the refresh-candidate rule fails both CI jobs.
"""

import json
from pathlib import Path

from modelman.pricing import _is_openrouter_priced
from modelman.registry import load_registry

CONTRACTS = Path(__file__).resolve().parents[3] / "docs" / "contracts"


def test_openrouter_priced_matches_shared_fixture():
    registry = load_registry(path=CONTRACTS / "catalog-predicates.sample.toml")
    expected = json.loads((CONTRACTS / "catalog-predicates.expected.json").read_text())
    priced = [m.id for m in registry.models if _is_openrouter_priced(registry, m)]
    assert priced == expected["openrouter_priced"]
```

Run: `cd modelman && uv run pytest tests/contracts/test_catalog_predicates_fixture.py -q`
Expected: PASS (the Python rule already matches; this pins it). If `load_registry` rejects the dangling provider, note it and drop only that model from the Python assertion by filtering `m.provider_id in {p.id for p in registry.providers}` — but first check: it must still parse, since modelman can meet a hand-edited registry.

- [ ] **Step 7: Commit**

```bash
git add docs/contracts/catalog-predicates.* wt/internal wt/cmd/wt/smoke.go modelman/tests/contracts/test_catalog_predicates_fixture.py
git commit -m "feat(wt): configured cloud models are in the catalog (InCatalog); predicate contract fixture (#179, #180)"
```

### Task 2: Ownership marker on every row wt writes

**Files:**
- Modify: `wt/internal/litellm/entry.go` (`BuildEntry`)
- Modify: `wt/internal/litellm/configfile.go` (add `ManagedKey`, `IsManaged`, `RowInfo`, `Rows`, `row`, `carryPreservedParams`; `SetRow` uses the latter)
- Test: `wt/internal/litellm/entry_test.go`, `wt/internal/litellm/configfile_test.go`

**Interfaces:**
- Produces: `const ManagedKey = "wt_managed"`; `func IsManaged(row *yaml.Node) bool`; `type RowInfo struct{ ID string; Managed bool }`; `func (f *File) Rows() []RowInfo`; unexported for Task 3: `func (f *File) row(id string) *yaml.Node`, `func carryPreservedParams(old, row *yaml.Node)`.

- [ ] **Step 1: Write the failing tests**

Append to `wt/internal/litellm/entry_test.go`:

```go
// TestBuildEntryStampsMarker pins #179's ownership marker: every row wt
// builds carries model_info.wt_managed: true, which is what lets sync delete
// its own stale rows while never touching hand-written ones.
func TestBuildEntryStampsMarker(t *testing.T) {
	cfg := testConfig()
	for _, id := range []string{"ollama/gemma:9b", "openrouter/x/y"} {
		m := cfg.Models[config.IndexModelByID(cfg.Models, id)]
		n, err := BuildEntry(m, *cfg.ProviderByID(m.ProviderID))
		if err != nil {
			t.Fatal(err)
		}
		if !IsManaged(n) {
			t.Errorf("%s: row not marked wt_managed: %v", id, decode(t, n))
		}
	}
}

// TestBuildEntryMarkerWins pins that a registry model_info cannot disown a
// wt row: wt_managed: false in model_info is overridden, or sync would treat
// its own route as hand-written and strand it forever.
func TestBuildEntryMarkerWins(t *testing.T) {
	cfg := testConfig()
	m := cfg.Models[config.IndexModelByID(cfg.Models, "openrouter/x/y")]
	m.ModelInfo = map[string]any{ManagedKey: false}
	n, err := BuildEntry(m, *cfg.ProviderByID(m.ProviderID))
	if err != nil {
		t.Fatal(err)
	}
	if !IsManaged(n) {
		t.Fatalf("model_info override disowned the row: %v", decode(t, n))
	}
}
```

Append to `wt/internal/litellm/configfile_test.go`:

```go
// TestRowsReportsManaged pins Rows(): file order, and Managed only for rows
// whose model_info carries wt_managed: true — a hand-written row (no marker,
// or a non-bool value) is never reported as wt's.
func TestRowsReportsManaged(t *testing.T) {
	f, err := Open(writeConfig(t, `model_list:
  - model_name: hand/alias
    litellm_params: {model: openrouter/x}
  - model_name: ollama/a:1
    litellm_params: {model: ollama_chat/a:1}
    model_info: {wt_managed: true}
  - model_name: odd/one
    litellm_params: {model: openai/x}
    model_info: {wt_managed: "yes"}
`))
	if err != nil {
		t.Fatal(err)
	}
	want := []RowInfo{{"hand/alias", false}, {"ollama/a:1", true}, {"odd/one", false}}
	if got := f.Rows(); !slices.Equal(got, want) {
		t.Fatalf("Rows() = %v, want %v", got, want)
	}
}
```

(Add `"slices"` to the test file's imports if absent.)

- [ ] **Step 2: Run — expect build failure**

Run: `cd wt && go test ./internal/litellm -run 'Marker|RowsReportsManaged'`
Expected: FAIL — `IsManaged`, `ManagedKey`, `RowInfo`, `Rows` undefined.

- [ ] **Step 3: Implement**

In `configfile.go`, after `rowName`:

```go
// ManagedKey is the model_info key wt stamps on every row it writes (#179).
// Sync removes only rows wt owns; the marker is how a row says so.
const ManagedKey = "wt_managed"

// IsManaged reports whether row carries wt's ownership marker
// (model_info.wt_managed: true; any other value counts as hand-written).
func IsManaged(row *yaml.Node) bool {
	return isTrue(mapGet(mapGet(row, "model_info"), ManagedKey))
}

// RowInfo is one model_list row's name and whether wt's marker is on it.
type RowInfo struct {
	ID      string
	Managed bool
}

// Rows returns every named model_list mapping row, in file order.
func (f *File) Rows() []RowInfo {
	ml := mapGet(f.root(), "model_list")
	if ml == nil || ml.Kind != yaml.SequenceNode {
		return nil
	}
	var out []RowInfo
	for _, row := range ml.Content {
		if id := rowName(row); row.Kind == yaml.MappingNode && id != "" {
			out = append(out, RowInfo{ID: id, Managed: IsManaged(row)})
		}
	}
	return out
}

// row returns the first mapping row named id, or nil.
func (f *File) row(id string) *yaml.Node {
	ml := mapGet(f.root(), "model_list")
	if ml == nil || ml.Kind != yaml.SequenceNode {
		return nil
	}
	for _, r := range ml.Content {
		if r.Kind == yaml.MappingNode && rowName(r) == id {
			return r
		}
	}
	return nil
}

// carryPreservedParams copies the user-managed presence-based params
// (preservedParamKeys) from old into row when row lacks them — what SetRow
// does on replace, factored out so sync can compare a rebuilt row with the
// one on disk exactly as SetRow would write it.
func carryPreservedParams(old, row *yaml.Node) {
	oldParams, newParams := mapGet(old, "litellm_params"), mapGet(row, "litellm_params")
	if oldParams == nil || newParams == nil {
		return
	}
	for _, k := range preservedParamKeys {
		if v := mapGet(oldParams, k); v != nil && mapGet(newParams, k) == nil {
			mapSet(newParams, k, v)
		}
	}
}
```

In `SetRow`, replace the inline `oldParams, newParams := ...` block (the `if oldParams != nil && newParams != nil { for _, k := range preservedParamKeys {...} }`) with `carryPreservedParams(old, row)`; behaviour is unchanged (the existing SetRow tests pin it).

Confirm `mapGet` returns nil for a nil node (`if m == nil || m.Kind != yaml.MappingNode`); it does.

In `entry.go` `BuildEntry`, after the `for _, k := range extra { ... }` loop and before building `paramsNode`, add:

```go
	// The ownership marker (#179) is set last so a registry model_info can
	// never disown a wt row.
	info = slices.DeleteFunc(info, func(e kv) bool { return e.key == ManagedKey })
	info = append(info, kv{ManagedKey, true})
```

Add `"slices"` to `entry.go`'s imports.

- [ ] **Step 4: Run the package**

Run: `cd wt && go test ./internal/litellm ./internal/lifecycle`
Expected: the two new tests PASS. Existing tests that compare an exact row shape (e.g. `TestBuildEntryCloudRow`) fail because the row gained `wt_managed: true`: add `"wt_managed": true` to their expected `model_info` maps. `lifecycle`'s `realRoutes` tests compare routed ids only and should pass unchanged.

- [ ] **Step 5: Commit**

```bash
git add wt/internal/litellm
git commit -m "feat(wt): stamp model_info.wt_managed on every LiteLLM row wt writes (#179)"
```

### Task 3: Reconciling sync (`CloudModels`, `PlanSync`, `Sync`)

**Files:**
- Modify: `wt/internal/litellm/service.go`
- Create: `wt/internal/litellm/catalog_predicates_fixture_test.go`
- Test: `wt/internal/litellm/service_test.go`

**Interfaces:**
- Consumes: `IsManaged`, `Rows`, `row`, `carryPreservedParams` (Task 2).
- Produces:
  - `func CloudModels(cfg *config.Config) []config.Model`
  - `type SyncPlan struct { Add, Adopt, Rewrite, Remove []string; Errors []Outcome }` (`Rewrite` added in the PR #183 review round)
  - `func PlanSync(cfg *config.Config, running []string, o Options) (SyncPlan, error)` (read-only; dry run)
  - `Sync(cfg, running, o)` keeps its signature; outcome actions become `"routed"`, `"adopted"`, `"rewritten"`, `"unrouted"`.

- [ ] **Step 1: Write the routable-cloud fixture test**

`wt/internal/litellm/catalog_predicates_fixture_test.go`:

```go
package litellm

import (
	"encoding/json"
	"os"
	"slices"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// TestCloudModelsMatchesSharedFixture pins sync's desired cloud set to the
// shared contract (#179): openrouter and ollama cloud models are routed; a
// native model, a cloud provider with no LiteLLM mapping, a dangling
// provider_id and a local model are not.
func TestCloudModelsMatchesSharedFixture(t *testing.T) {
	const dir = "../../../docs/contracts/"
	var reg struct {
		Providers []config.Provider `toml:"providers"`
		Models    []config.Model    `toml:"models"`
	}
	if _, err := toml.DecodeFile(dir+"catalog-predicates.sample.toml", &reg); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Providers: reg.Providers, Models: reg.Models}
	for i := range cfg.Models {
		if p := cfg.ProviderByID(cfg.Models[i].ProviderID); p != nil && p.Auth.Type == "native" {
			cfg.Models[i].Native = true
		}
	}
	raw, err := os.ReadFile(dir + "catalog-predicates.expected.json")
	if err != nil {
		t.Fatal(err)
	}
	var want struct {
		RoutableCloud []string `json:"routable_cloud"`
	}
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range CloudModels(cfg) {
		got = append(got, m.ID)
	}
	if !slices.Equal(got, want.RoutableCloud) {
		t.Fatalf("CloudModels = %v, want %v", got, want.RoutableCloud)
	}
}
```

- [ ] **Step 2: Write the failing sync tests**

Append to `wt/internal/litellm/service_test.go` (uses the existing `opts`, `testConfig`, `Open`):

```go
// readRows opens path and returns its rows (id + managed).
func readRows(t *testing.T, p string) []RowInfo {
	t.Helper()
	f, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	return f.Rows()
}

// TestSyncRoutesEveryCloudModel pins #179's core rule: configured means
// exposed, so sync adds a marked route for every registry cloud model with no
// expose step, and skips native models.
func TestSyncRoutesEveryCloudModel(t *testing.T) {
	o, restarts, p := opts(t, "model_list: []\n")
	res, err := Sync(testConfig(), nil, o)
	if err != nil {
		t.Fatal(err)
	}
	if got := readRows(t, p); !slices.Equal(got, []RowInfo{{"openrouter/x/y", true}}) {
		t.Fatalf("rows = %v", got)
	}
	if !res.Changed || *restarts != 1 {
		t.Fatalf("changed=%v restarts=%d", res.Changed, *restarts)
	}
}

// TestSyncRemovesMarkedRowOfDeletedModel pins that deleting a cloud model
// from the registry removes its route: the marked row is wt's and is no
// longer desired.
func TestSyncRemovesMarkedRowOfDeletedModel(t *testing.T) {
	o, _, p := opts(t, `model_list:
  - model_name: openrouter/gone
    litellm_params: {model: openrouter/gone}
    model_info: {wt_managed: true}
`)
	if _, err := Sync(testConfig(), nil, o); err != nil {
		t.Fatal(err)
	}
	for _, r := range readRows(t, p) {
		if r.ID == "openrouter/gone" {
			t.Fatalf("stale marked row survived: %v", readRows(t, p))
		}
	}
}

// TestSyncNeverTouchesHandWrittenRows pins the safety rule: an unmarked row
// whose name is not a registry id wt manages — even one shaped like a
// registry id — is never removed or rewritten.
func TestSyncNeverTouchesHandWrittenRows(t *testing.T) {
	const hand = `  - model_name: openrouter/deleted-before-upgrade
    litellm_params: {model: openrouter/deleted-before-upgrade}
  - model_name: my-alias
    litellm_params: {model: openrouter/x/y}
`
	o, _, p := opts(t, "model_list:\n"+hand)
	if _, err := Sync(testConfig(), nil, o); err != nil {
		t.Fatal(err)
	}
	got := readRows(t, p)
	for _, id := range []string{"openrouter/deleted-before-upgrade", "my-alias"} {
		if !slices.Contains(got, RowInfo{id, false}) {
			t.Fatalf("hand-written %s changed or removed: %v", id, got)
		}
	}
}

// TestSyncAdoptsUnmarkedRowOfManagedModel pins the one-time migration: a
// pre-upgrade wt row (unmarked, named like a registry cloud id) is rewritten
// with the marker and reported as adopted.
func TestSyncAdoptsUnmarkedRowOfManagedModel(t *testing.T) {
	o, _, p := opts(t, `model_list:
  - model_name: openrouter/x/y
    litellm_params: {model: openrouter/x/y}
`)
	res, err := Sync(testConfig(), nil, o)
	if err != nil {
		t.Fatal(err)
	}
	if got := readRows(t, p); !slices.Equal(got, []RowInfo{{"openrouter/x/y", true}}) {
		t.Fatalf("rows = %v", got)
	}
	if len(res.Outcomes) != 1 || res.Outcomes[0].Action != "adopted" {
		t.Fatalf("outcomes = %+v, want one adopted", res.Outcomes)
	}
}

// TestSyncRemovesLegacyUnmarkedLocalRoute pins that a pre-upgrade route of a
// stopped REGISTRY local model is still removed, as sync did before #179 —
// ownership covers unmarked rows named like a managed registry id.
func TestSyncRemovesLegacyUnmarkedLocalRoute(t *testing.T) {
	o, _, p := opts(t, `model_list:
  - model_name: ollama/gemma:9b
    litellm_params: {model: ollama_chat/gemma:9b}
`)
	if _, err := Sync(testConfig(), nil, o); err != nil {
		t.Fatal(err)
	}
	for _, r := range readRows(t, p) {
		if r.ID == "ollama/gemma:9b" {
			t.Fatalf("stopped local model kept its route: %v", readRows(t, p))
		}
	}
}

// TestSyncUnchangedDoesNotRestart pins that a second sync with nothing to do
// writes nothing and does not bounce the proxy (each bounce drops requests).
func TestSyncUnchangedDoesNotRestart(t *testing.T) {
	o, restarts, _ := opts(t, "model_list: []\n")
	if _, err := Sync(testConfig(), nil, o); err != nil {
		t.Fatal(err)
	}
	*restarts = 0
	res, err := Sync(testConfig(), nil, o)
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed || *restarts != 0 || len(res.Outcomes) != 0 {
		t.Fatalf("second sync: changed=%v restarts=%d outcomes=%+v", res.Changed, *restarts, res.Outcomes)
	}
}

// TestPlanSyncWritesNothing pins `sync --dry-run`: the plan names the add
// and the removal, and the file is byte-identical afterwards.
func TestPlanSyncWritesNothing(t *testing.T) {
	const body = `model_list:
  - model_name: openrouter/gone
    litellm_params: {model: openrouter/gone}
    model_info: {wt_managed: true}
`
	o, restarts, p := opts(t, body)
	plan, err := PlanSync(testConfig(), nil, o)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(plan.Add, []string{"openrouter/x/y"}) || !slices.Equal(plan.Remove, []string{"openrouter/gone"}) {
		t.Fatalf("plan = %+v", plan)
	}
	after, _ := os.ReadFile(p)
	if string(after) != body || *restarts != 0 {
		t.Fatalf("dry run changed the file or restarted: %q restarts=%d", after, *restarts)
	}
}
```

Add `"slices"` to the imports of `service_test.go`.

- [ ] **Step 3: Run — expect build failure**

Run: `cd wt && go test ./internal/litellm -run 'Sync|CloudModels|PlanSync'`
Expected: FAIL — `CloudModels`, `PlanSync` undefined.

- [ ] **Step 4: Implement**

In `service.go`, add after `LocalModels`:

```go
// CloudModels lists the registry cloud models sync routes (#179: configured
// means exposed): non-native, with a provider entry LiteLLM can map, whose
// location resolves to cloud. Pinned by docs/contracts/catalog-predicates.
func CloudModels(cfg *config.Config) []config.Model {
	var out []config.Model
	for _, m := range cfg.Models {
		if m.Native {
			continue
		}
		p := cfg.ProviderByID(m.ProviderID)
		if p == nil || p.Auth.Type == "native" {
			continue
		}
		if loc, err := cfg.ResolveLocation(m); err != nil || loc != config.LocationCloud {
			continue
		}
		if _, ok := PolicyFor(m.ProviderID); !ok {
			continue
		}
		out = append(out, m)
	}
	return out
}

// SyncPlan is what one sync changes. Add holds desired ids whose row is
// missing or differs (each written with the marker); Adopt is the subset of
// Add that replaces an unmarked row; Remove holds owned rows no longer
// desired; Errors holds desired ids whose row could not be built.
type SyncPlan struct {
	Add    []string
	Adopt  []string
	Remove []string
	Errors []Outcome
}

// planSync decides a sync against f. Desired = every CloudModels id plus the
// running registry local models. A row is wt's to remove when it carries the
// marker or is named like a managed registry id (cloud or local) — the
// latter covers rows written before the marker existed. Unmarked rows with
// any other name are hand-written and never touched. Untouched ids are
// neither added nor removed; Recheck (see Options) re-verifies local ids.
func planSync(cfg *config.Config, f *File, running []string, o Options) SyncPlan {
	local := map[string]bool{}
	for _, m := range LocalModels(cfg) {
		local[m.ID] = true
	}
	managed := map[string]bool{}
	var desired []string
	for _, m := range CloudModels(cfg) {
		managed[m.ID] = true
		desired = append(desired, m.ID)
	}
	for id := range local {
		managed[id] = true
	}
	for _, m := range LocalModels(cfg) {
		if slices.Contains(running, m.ID) && !slices.Contains(o.Untouched, m.ID) {
			desired = append(desired, m.ID)
		}
	}
	var plan SyncPlan
	want := map[string]bool{}
	for _, id := range desired {
		want[id] = true
		row, err := prepare(cfg, id, true)
		if err != nil {
			plan.Errors = append(plan.Errors, Outcome{ID: id, Err: err})
			continue
		}
		old := f.row(id)
		if old != nil {
			carryPreservedParams(old, row)
			// Compare by value, not bytes: a row read back from disk keeps
			// quoting styles ('ollama/gemma:9b') a freshly built one lacks,
			// so byte equality would report every unchanged row as changed.
			var oldv, newv any
			if old.Decode(&oldv) == nil && row.Decode(&newv) == nil && reflect.DeepEqual(oldv, newv) {
				continue
			}
		}
		plan.Add = append(plan.Add, id)
		if old != nil && !IsManaged(old) {
			plan.Adopt = append(plan.Adopt, id)
		}
	}
	for _, r := range f.Rows() {
		if want[r.ID] || slices.Contains(o.Untouched, r.ID) {
			continue
		}
		if r.Managed || managed[r.ID] {
			plan.Remove = append(plan.Remove, r.ID)
		}
	}
	if o.Recheck != nil {
		touchesLocal := slices.ContainsFunc(plan.Add, func(id string) bool { return local[id] }) ||
			slices.ContainsFunc(plan.Remove, func(id string) bool { return local[id] })
		if touchesLocal {
			if fresh, ok := runRecheck(o); ok {
				plan.Remove = slices.DeleteFunc(plan.Remove, func(id string) bool { return local[id] && slices.Contains(fresh, id) })
				plan.Add = slices.DeleteFunc(plan.Add, func(id string) bool { return local[id] && !slices.Contains(fresh, id) })
			}
		}
	}
	return plan
}

// PlanSync reports what Sync would change without writing (`sync --dry-run`).
func PlanSync(cfg *config.Config, running []string, o Options) (SyncPlan, error) {
	f, err := Open(o.path())
	if err != nil {
		return SyncPlan{}, err
	}
	return planSync(cfg, f, running, o), nil
}
```

Add `"reflect"` to `service.go`'s imports. Replace the existing `Sync` function (doc comment included) with:

```go
// Sync reconciles config.yaml with the registry and the running local
// models (#179): every CloudModels id and every running registry local
// model gets a marked route; rows wt owns that are no longer desired are
// removed; hand-written rows are never touched (see planSync). The plan is
// made under the config.yaml lock. Outcome actions: "routed", "adopted",
// "unrouted"; per-id build failures are reported with Err.
func Sync(cfg *config.Config, running []string, o Options) (Result, error) {
	o.SkipReadyGate = true
	var plan SyncPlan
	res, err := applyPlanned(cfg, func(f *File) (add, remove []string) {
		plan = planSync(cfg, f, running, o)
		return plan.Add, plan.Remove
	}, o)
	if err != nil {
		return res, err
	}
	for i := range res.Outcomes {
		switch res.Outcomes[i].Action {
		case "exposed":
			res.Outcomes[i].Action = "routed"
			if slices.Contains(plan.Adopt, res.Outcomes[i].ID) {
				res.Outcomes[i].Action = "adopted"
			}
		case "unexposed":
			res.Outcomes[i].Action = "unrouted"
		}
	}
	res.Outcomes = append(res.Outcomes, plan.Errors...)
	return res, nil
}
```

Update `LocalModels`' doc comment: "the local half of the set Sync manages (CloudModels is the other)".

- [ ] **Step 5: Run the package and fix superseded tests**

Run: `cd wt && go test ./internal/litellm`

The pre-#179 Sync tests (`TestSyncReconcilesLocalRoutes`, `TestSyncRestartsOnceAndSkipsReadyGate`, `TestSyncLeavesUntouchedModelsAlone`, `TestSyncDecidesUnderTheLock`, the three `TestSyncRecheck*`) assumed sync ignores cloud models. `testConfig()` has a cloud model (`openrouter/x/y`), so these now also see it routed. Update each assertion to account for `openrouter/x/y` being added (and an extra "routed" outcome), keeping each test's original subject (untouched ids, lock, recheck). Do not weaken the Recheck tests: they must still prove local adds/removes are re-verified.

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add wt/internal/litellm
git commit -m "feat(wt): wt litellm sync reconciles cloud + running local routes; removes only owned rows (#179)"
```

### Task 4: CLI — `sync --dry-run`, `list` ownership, contract JSON

**Files:**
- Modify: `wt/cmd/wt/litellm.go` (`runLitellmSync`, `runLitellmList`, `litellmCmd`)
- Modify: `docs/contracts/litellm-cli.sample.json`
- Modify: `modelman/tests/contracts/test_litellm_cli_fixture.py` (only if it asserts the exact key set of the list document)
- Test: `wt/cmd/wt/litellm_test.go`

**Interfaces:**
- Consumes: `litellm.PlanSync`, `litellm.SyncPlan`, `File.Rows` (Tasks 2–3).
- Produces: JSON shapes consumed by modelman in Task 7:
  - `wt litellm sync --json --dry-run` → `{"dry_run": true, "plan": {"add": [...], "adopt": [...], "rewrite": [...], "remove": [...], "errors": [{"id": "...", "error": "..."}]}}`
  - `wt litellm sync --json` → unchanged `litellmResultJSON` (actions `routed`/`adopted`/`rewritten`/`unrouted`)
  - `wt litellm list --json` → `{"routed": [...ids], "rows": [{"id": "...", "managed": true}]}` (`routed` kept for existing readers)

- [ ] **Step 1: Write the failing tests**

Append to `wt/cmd/wt/litellm_test.go` (it already has `litellmEnv`, `litellmTestConfig` and `stubProbeInventory`; add `"slices"` to its imports):

```go
// litellmCloudTestConfig is litellmTestConfig plus one openrouter cloud
// model, the shape sync now routes unconditionally (#179).
func litellmCloudTestConfig() *config.Config {
	cfg := litellmTestConfig()
	cfg.Providers = append(cfg.Providers, config.Provider{
		ID: "openrouter", Location: config.LocationCloud,
		Auth: config.AuthConfig{Type: "api_key", SecretRef: "sk-test", BaseURL: "https://openrouter.ai/api/v1"},
	})
	cfg.Models = append(cfg.Models, config.Model{ID: "openrouter/x", ProviderID: "openrouter", ModelName: "x", Location: config.LocationCloud})
	return cfg
}

// TestLitellmSyncDryRunJSON pins `wt litellm sync --dry-run --json`: the plan
// is printed in the shape modelman parses and config.yaml is not written.
func TestLitellmSyncDryRunJSON(t *testing.T) {
	p := litellmEnv(t, "model_list: []\n")
	stubProbeInventory(t, localmodels.Snapshot{Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK}})
	before, _ := os.ReadFile(p)
	var out, errOut bytes.Buffer
	if err := runLitellmSync(&out, &errOut, litellmCloudTestConfig(), true, true); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		DryRun bool `json:"dry_run"`
		Plan   struct {
			Add    []string `json:"add"`
			Remove []string `json:"remove"`
		} `json:"plan"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("not JSON: %q", out.String())
	}
	if !doc.DryRun || !slices.Equal(doc.Plan.Add, []string{"openrouter/x"}) || len(doc.Plan.Remove) != 0 {
		t.Fatalf("doc = %+v", doc)
	}
	if after, _ := os.ReadFile(p); string(after) != string(before) {
		t.Fatalf("dry run wrote config.yaml:\n%s", after)
	}
}

// TestLitellmListJSONRows pins list's ownership rows next to the legacy
// "routed" id list modelman already reads.
func TestLitellmListJSONRows(t *testing.T) {
	litellmEnv(t, `model_list:
  - model_name: ollama/gemma:9b
    litellm_params: {model: ollama_chat/gemma:9b}
    model_info: {wt_managed: true}
  - model_name: hand/alias
    litellm_params: {model: openrouter/x}
`)
	var out bytes.Buffer
	if err := runLitellmList(&out, true); err != nil {
		t.Fatal(err)
	}
	const want = `{"routed":["ollama/gemma:9b","hand/alias"],"rows":[{"id":"ollama/gemma:9b","managed":true},{"id":"hand/alias","managed":false}]}`
	if got := strings.TrimSpace(out.String()); got != want {
		t.Fatalf("list = %s\nwant   %s", got, want)
	}
}
```

Also update the existing calls `runLitellmSync(&out, &errOut, <cfg>, <json>)` in this file to pass a trailing `false` (not dry-run), and change `TestLitellmListAndProviders`' exact expectation to `{"routed":["a/b"],"rows":[{"id":"a/b","managed":false}]}`.

- [ ] **Step 2: Run — expect failure**

Run: `cd wt && go test ./cmd/wt -run 'LitellmSyncDryRun|LitellmListJSONRows'`
Expected: FAIL — `runLitellmSync` takes 4 args; `rows` missing.

- [ ] **Step 3: Implement**

Change `runLitellmSync`'s signature to `func runLitellmSync(out, errOut io.Writer, cfg *config.Config, asJSON, dryRun bool) error`. After computing `running` and `untouched` (unchanged code), branch before calling `litellm.Sync`:

```go
	o := litellm.Options{
		Untouched: untouched,
		Recheck:   func() []string { return runningIDs(probeInventory(cfg)) },
	}
	if dryRun {
		o.Recheck = nil // a dry run reports the probe it took; no lock, no recheck
		plan, err := litellm.PlanSync(cfg, running, o)
		if err != nil {
			return err
		}
		return reportSyncPlan(out, errOut, plan, asJSON)
	}
	res, err := litellm.Sync(cfg, running, o)
```

(keep the existing `Untouched`/`Recheck` comments on the moved lines; the skipped-family warnings loop stays after the real sync.)

Add:

```go
type syncPlanJSON struct {
	DryRun bool `json:"dry_run"`
	Plan   struct {
		Add    []string             `json:"add"`
		Adopt  []string             `json:"adopt"`
		Remove []string             `json:"remove"`
		Errors []litellmOutcomeJSON `json:"errors"`
	} `json:"plan"`
}

// reportSyncPlan prints a dry-run plan; exit status 1 when any desired id
// could not be built, like a real sync.
func reportSyncPlan(out, errOut io.Writer, plan litellm.SyncPlan, asJSON bool) error {
	doc := syncPlanJSON{DryRun: true}
	doc.Plan.Add = append([]string{}, plan.Add...)
	doc.Plan.Adopt = append([]string{}, plan.Adopt...)
	doc.Plan.Remove = append([]string{}, plan.Remove...)
	doc.Plan.Errors = []litellmOutcomeJSON{}
	for _, e := range plan.Errors {
		doc.Plan.Errors = append(doc.Plan.Errors, litellmOutcomeJSON{ID: e.ID, Error: e.Err.Error()})
	}
	if asJSON {
		if err := json.NewEncoder(out).Encode(doc); err != nil {
			return err
		}
	} else {
		for _, id := range plan.Add {
			verb := "route"
			if slices.Contains(plan.Adopt, id) {
				verb = "adopt"
			}
			fmt.Fprintf(out, "%s: would %s\n", id, verb)
		}
		for _, id := range plan.Remove {
			fmt.Fprintf(out, "%s: would unroute\n", id)
		}
		for _, e := range doc.Plan.Errors {
			fmt.Fprintf(errOut, "%s: %s\n", e.ID, e.Error)
		}
	}
	if len(plan.Errors) > 0 {
		return errLitellmIDFailed
	}
	return nil
}
```

Replace `runLitellmList`'s body after opening the file:

```go
	rows := f.Rows()
	ids := make([]string, 0, len(rows))
	type rowJSON struct {
		ID      string `json:"id"`
		Managed bool   `json:"managed"`
	}
	jrows := make([]rowJSON, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
		jrows = append(jrows, rowJSON{r.ID, r.Managed})
	}
	if asJSON {
		return json.NewEncoder(out).Encode(map[string]any{"routed": ids, "rows": jrows})
	}
	for _, r := range rows {
		owner := "wt"
		if !r.Managed {
			owner = "hand-written"
		}
		fmt.Fprintf(out, "%s\t%s\n", r.ID, owner)
	}
	return nil
```

In `litellmCmd`: add `var syncDryRun bool`, pass it (`runLitellmSync(..., syncJSON, syncDryRun)`), register `syncC.Flags().BoolVar(&syncDryRun, "dry-run", false, "print the plan; change nothing")`, and change `syncC`'s `Short` to `"Make LiteLLM routes match the registry's cloud models and the running local models"`. Add `"slices"` to imports if needed.

- [ ] **Step 4: Update the contract JSON**

In `docs/contracts/litellm-cli.sample.json`, add a `sync_dry_run` example with the shape above and add `"rows"` to the `list` example (keep `routed`). Keep the existing expose/unexpose examples until PR 3. Run the Go and Python fixture tests:

Run: `cd wt && go test ./cmd/wt -run Fixture && cd ../modelman && uv run pytest tests/contracts -q`
Expected: PASS (update a fixture test only where it asserts the exact key set).

- [ ] **Step 5: Full wt suite, commit**

Run: `cd wt && go build ./... && go vet ./... && go test ./... && test -z "$(gofmt -l cmd internal)"`
Expected: PASS.

```bash
git add wt/cmd/wt docs/contracts/litellm-cli.sample.json modelman/tests/contracts
git commit -m "feat(wt): wt litellm sync --dry-run; list reports row ownership (#179)"
```

### Task 5: Verify LiteLLM accepts the marker (gate before any real write)

No code. Do this before `make install` puts PR 1's binary on PATH, since `wt start` would then write marked rows to the real config.

- [ ] **Step 1:** Find the proxy binary: `plutil -extract ProgramArguments json -o - ~/Library/LaunchAgents/local.litellm.proxy.plist` (first element, or the `litellm` path in the args).
- [ ] **Step 2:** Write `$SCRATCH/marker-check.yaml`:

```yaml
model_list:
  - model_name: marker/check
    litellm_params: {model: ollama_chat/does-not-matter, api_base: http://localhost:11434}
    model_info: {input_cost_per_token: 0, output_cost_per_token: 0, wt_managed: true}
```

- [ ] **Step 3:** Run `<litellm> --config $SCRATCH/marker-check.yaml --port 4999` in the background, poll `curl -s localhost:4999/health/liveliness` until it answers (≤60s), then `curl -s localhost:4999/model/info | python3 -m json.tool | grep -n wt_managed`.
Expected: the proxy starts and `/model/info` lists `marker/check` (with `wt_managed` in its `model_info`).
- [ ] **Step 4:** Stop the process. If the proxy refused the key, stop and switch the marker to a YAML comment (spec A2 fallback) before merging PR 1 — that is a plan change; report it.

### Task 6: PR 1 acceptance on the real host, then open the PR

- [ ] **Step 1:** Build without installing: `cd wt && go build -o $SCRATCH/wt-pr1 ./cmd/wt`.
- [ ] **Step 2:** Back up: `cp ~/.config/litellm/config.yaml $SCRATCH/config.yaml.bak`.
- [ ] **Step 3:** Catalog unchanged (spec acceptance A-1): for each agent in `wt config` (claude, codex, copilot, opencode, pi, agy), capture the eligible list with the installed `wt` and with `$SCRATCH/wt-pr1` (use the same non-interactive listing command for both — e.g. `wt rotate <tag>` per tag, or a tiny `go run` helper over `cfg.EligibleModels`) and `diff` them. Expected: identical.
- [ ] **Step 4:** `$SCRATCH/wt-pr1 litellm sync --dry-run`. Show the output to the user. Expected: adds/adoptions for wt's own rows; **no `would unroute` for any hand-written row**. Wait for the user's go-ahead.
- [ ] **Step 5:** After approval: `$SCRATCH/wt-pr1 litellm sync`, then `$SCRATCH/wt-pr1 litellm list` — hand-written rows still listed as `hand-written`; `curl -s localhost:4000/health/liveliness` answers.
- [ ] **Step 6:** `make test-all` from the repo root. Push and open PR 1 ("feat(wt): reconciling LiteLLM sync with ownership marker (#179 phase A, 1/4)"), body listing the acceptance results; it closes #180.

---

# PR 2 — modelman: one sync call replaces expose/unexpose

Branch: `feat/179-a2-modelman-sync` from `main` after PR 1 merges (needs `wt litellm sync` with the new semantics; run `make install` first).

### Task 7: `wt_bridge.sync` and `litellm.sync_routes`

**Files:**
- Modify: `modelman/src/modelman/wt_bridge.py`
- Modify: `modelman/src/modelman/litellm.py`
- Modify: `modelman/tests/conftest.py` (`_never_call_real_wt`)
- Test: `modelman/tests/test_wt_bridge.py`, `modelman/tests/test_routes_sync.py` (new)

**Interfaces:**
- Produces:
  - `wt_bridge.sync(*, litellm_path: Path | None = None) -> BridgeResult`
  - `litellm.sync_routes(*, litellm_path: Path | None = None) -> list[str]` — never raises; returns warnings to show.
  - conftest fixture `wt_calls` → `list[list[str]]` of argv tails passed to the fake `wt litellm`.

- [ ] **Step 1: Make the conftest fake record and answer `sync`**

In `tests/conftest.py`'s `_never_call_real_wt`: create `calls: list[list[str]] = []` before defining `fake`; at the top of `fake(args, ...)` do `calls.append(list(args))`; add a branch answering `args[:1] == ["sync"]` with `{"outcomes": [], "changed": False, "warnings": []}` (exit 0); change the fixture to `yield calls` (keeping its cleanup after the yield). Add:

```python
@pytest.fixture
def wt_calls(_never_call_real_wt):
    """argv tails (after `wt litellm`) the autouse fake received this test."""
    return _never_call_real_wt
```

- [ ] **Step 2: Write the failing tests**

`modelman/tests/test_routes_sync.py`:

```python
"""sync_routes: modelman's single LiteLLM touchpoint after #179."""

import subprocess

from modelman import wt_bridge
from modelman.litellm import sync_routes


def test_sync_routes_runs_wt_sync_once(wt_calls):
    assert sync_routes() == []
    assert wt_calls == [["sync", "--json"]]


def test_sync_routes_reports_per_id_errors_and_warnings(monkeypatch):
    def fake(args, env=None, timeout=None):
        out = '{"outcomes":[{"id":"x/y","error":"provider \\"x\\" has no LiteLLM mapping"}],"changed":true,"warnings":["restart: boom"]}'
        return subprocess.CompletedProcess(args, 1, out, "")

    monkeypatch.setattr(wt_bridge, "_run", fake)
    warnings = sync_routes()
    assert "restart: boom" in warnings
    assert any(w.startswith("x/y: ") for w in warnings)


def test_sync_routes_timeout_warns(monkeypatch):
    def fake(args, env=None, timeout=None):
        raise wt_bridge.WtBridgeTimeoutError("wt litellm sync timed out after 120s")

    monkeypatch.setattr(wt_bridge, "_run", fake)
    warnings = sync_routes()
    assert len(warnings) == 1
    assert "timed out" in warnings[0] and "wt litellm sync" in warnings[0]


def test_sync_routes_wt_missing_warns(monkeypatch):
    def fake(args, env=None, timeout=None):
        raise wt_bridge.WtNotFoundError("wt not found on PATH; install it with `make install`")

    monkeypatch.setattr(wt_bridge, "_run", fake)
    assert "wt not found" in sync_routes()[0]
```

- [ ] **Step 3: Run — expect ImportError**

Run: `cd modelman && uv run pytest tests/test_routes_sync.py -q`
Expected: FAIL — `cannot import name 'sync_routes'`.

- [ ] **Step 4: Implement**

`wt_bridge.py` — add:

```python
def sync(*, litellm_path: Path | None = None) -> BridgeResult:
    """`wt litellm sync --json`: reconcile config.yaml with the registry's
    cloud models and the running local models (#179)."""
    return _change(["sync", "--json"], litellm_path)
```

and in `_change`, replace the timeout branch so `sync` never reaches `_reconcile_after_timeout` (whose `args.index("--")` assumes expose/unexpose):

```python
    except WtBridgeTimeoutError:
        if args[:1] not in (["expose"], ["unexpose"]):
            raise
        return _reconcile_after_timeout(args, litellm_path)
```

`litellm.py` — add after `_bridge`:

```python
def sync_routes(*, litellm_path: Path | None = None) -> list[str]:
    """Run `wt litellm sync` once and return warnings to show the user.

    modelman's only LiteLLM write path since #179: every command that changes
    the registry or local-model state calls this once at the end. It never
    raises — the registry is the source of truth and the next sync converges —
    so a failure becomes a warning naming the command that fixes it.
    """
    try:
        result = wt_bridge.sync(litellm_path=litellm_path)
    except wt_bridge.WtBridgeError as exc:
        return [f"LiteLLM routes not synced: {exc} — run `wt litellm sync`"]
    warnings = list(result.warnings)
    warnings += [f"{o.id}: {o.error}" for o in result.outcomes if o.error]
    return warnings
```

- [ ] **Step 5: Run, check, commit**

Run: `cd modelman && uv run pytest tests/test_routes_sync.py tests/test_wt_bridge.py -q && make check`
Expected: PASS.

```bash
git add modelman/src/modelman/wt_bridge.py modelman/src/modelman/litellm.py modelman/tests/conftest.py modelman/tests/test_routes_sync.py
git commit -m "feat(modelman): sync_routes — one wt litellm sync call (#179)"
```

### Task 8: Queue and TUI exit path sync once; drop queued exposes

**Files:**
- Modify: `modelman/src/modelman/queue.py` (`QueuedOps.exposes`, `PendingChanges.exposes`, the two unexpose cascades, the `if self.exposes:` apply block, the failed-download expose drop, the `apply_expose_queue` import)
- Modify: `modelman/src/modelman/main.py` (`run_queued_ops`, `run_tui`, the `expose:*`/`unexpose:*` event printing and counting)
- Modify: `modelman/src/modelman/screens/models.py`, `modelman/src/modelman/screens/forms.py`
- Test: `modelman/tests/test_queue.py`, `modelman/tests/commands/` (run_queued_ops wiring), `modelman/tests/screens/`

**Interfaces:**
- Consumes: `sync_routes` (Task 7).
- Produces: `QueuedOps(ready, deletes, moves)` and `PendingChanges` without `exposes`; `run_queued_ops` calls `sync_routes()` exactly once after `apply()`; `run_tui` calls it once when the TUI exits with no queued ops but the registry file changed.

- [ ] **Step 1: Write the failing tests**

Add to `modelman/tests/commands/test_run_tui.py` (it has `_seed`, `ModelEntry`, `model_entry_to_variant`, `MagicMock`, `patch`):

```python
def test_run_queued_ops_syncs_routes_once(tmp_path, monkeypatch, wt_calls):
    """#179: one `wt litellm sync` after an applied queue — no per-model
    expose/unexpose calls, whatever the queue held."""
    entry = ModelEntry(id="ollama/x", family="f", provider_id="ollama", model_name="x:7b")
    _seed(tmp_path, monkeypatch, models=[entry])
    with patch("modelman.main.ProviderRegistry.get", return_value=MagicMock()):
        failed = run_queued_ops(QueuedOps(deletes={"ollama/x": model_entry_to_variant(entry)}))
    assert failed is False
    assert [c for c in wt_calls if c[:1] == ["sync"]] == [["sync", "--json"]]
    assert not [c for c in wt_calls if c[:1] in (["expose"], ["unexpose"])]
```

Delete `test_run_queued_ops_counts_cascaded_unexpose_in_total` (its subject, the cascaded unexpose, is removed).

Add to the same file (`run_tui` imports `ModelmanApp` lazily, so the tests patch `modelman.app.ModelmanApp`, as `test_run_tui_discard_or_empty_does_not_apply` does):

```python
def test_run_tui_syncs_when_registry_changed_without_queue(tmp_path, monkeypatch, wt_calls):
    """An add/edit in the TUI writes registry.toml immediately and queues
    nothing; the exit must still sync so the new cloud model gets its route."""
    reg = tmp_path / "registry.toml"
    reg.write_text("models = []\n")
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg))

    class FakeApp:
        def run(self):
            reg.write_text('models = []\n# edited\n')
            return None

    monkeypatch.setattr("modelman.app.ModelmanApp", FakeApp)
    from modelman.main import run_tui

    run_tui()
    assert [c for c in wt_calls if c[:1] == ["sync"]] == [["sync", "--json"]]


def test_run_tui_no_change_no_sync(tmp_path, monkeypatch, wt_calls):
    reg = tmp_path / "registry.toml"
    reg.write_text("models = []\n")
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg))

    class FakeApp:
        def run(self):
            return None

    monkeypatch.setattr("modelman.app.ModelmanApp", FakeApp)
    from modelman.main import run_tui

    run_tui()
    assert not [c for c in wt_calls if c[:1] == ["sync"]]
```

- [ ] **Step 2: Run — expect failures**

Run: `cd modelman && uv run pytest tests/commands -q -k "sync"`
Expected: FAIL (no sync call yet).

- [ ] **Step 3: Implement queue.py**

- Delete the `exposes` field from `QueuedOps` and from `PendingChanges` (with its comment), and `from .litellm import apply_expose_queue`.
- In the deletes loop, delete the block from `# If the model was exposed through LiteLLM, queue an unexpose` through `self.exposes.append((model_id, False))` (keep the `self.state.models.pop(model_id, None)` and `_touched_model_ids.add` lines that follow).
- In the download failure branch, delete the `if (model_id, True) in self.exposes:` block and its comment.
- Delete the ready-off cascade block (`# Cascade: turning ready off ...` through its `self.exposes.append((model_id, False))`).
- Delete the whole `if self.exposes:` block.
- In `apply()`'s early return, drop `and not self.exposes`; fix the `apply()` docstring ("deletes, moves, downloads", no exposes).
- Remove `replace`/`is_model_local` imports only if now unused (ruff will say).

- [ ] **Step 4: Implement main.py**

- `run_queued_ops`: remove `exposes=...` from the `PendingChanges(...)` call, the `+ len(pending.exposes)` term, `counted_expose_ids` and the `expose:*`/`unexpose:*` branches in the event printer and step counter. After `pending.apply(...)` returns (in the same place failures are reported), add:

```python
    # wt reads registry.toml and the live providers from disk; apply() has
    # saved both, so one sync brings LiteLLM's routes in line (#179).
    for warning in sync_routes():
        typer.echo(f"warning: {warning}", err=True)
```

- `run_tui`:

```python
def run_tui() -> None:
    # (keep the existing docstring)
    from .app import ModelmanApp
    from .registry import _default_registry_path

    before = _file_digest(_default_registry_path())
    queued = ModelmanApp().run()
    if queued is None:
        # Add/edit write registry.toml immediately and queue nothing; route
        # what they changed (#179). run_queued_ops syncs on its own.
        if _file_digest(_default_registry_path()) != before:
            for warning in sync_routes():
                typer.echo(f"warning: {warning}", err=True)
        return
    if run_queued_ops(queued):
        raise typer.Exit(1)


def _file_digest(path: Path) -> bytes | None:
    try:
        return hashlib.sha256(path.read_bytes()).digest()
    except OSError:
        return None
```

Import `hashlib`, `Path` (if not present) and `sync_routes` from `.litellm`. Note: `run_tui` imports `ModelmanApp` lazily inside the function, which is why the tests patch `modelman.app.ModelmanApp`.

- [ ] **Step 5: Implement the TUI removals**

`screens/models.py`:
- Delete the `("x", "toggle_expose", "Toggle exposed")` binding, `action_toggle_expose`, `_enforce_expose_ready_rule`, the expose-cascade undo helper, `queued_exposes`, `_ready_cascade_for_expose`, `_routed_ids_cache`, `_load_routed_ids_cache` and its `pool.submit` in the prefetch, `_is_locally_routed` (the helper reading the cache), and every call to these.
- Delete the EXPOSED column: its `add_column`, the `exposed_override`/`flag_exposed`/`locally_routed`/`exposed_str` computation in the row builder, and `exposed_str` from the row tuple.
- In the stale-running reconcile merge (`fresh_exposed`), keep the `running=False` reset and drop the `exposed=` argument.
- Remove `expose {len(self.queued_exposes)}` from the pending-summary string, `queued_exposes` from `has_pending_changes()`, the `QueuedOps(...)` construction, and the `ConfirmExitDialog(...)` call.
- Remove the `is_effectively_exposed`/`passes_ready_gate` imports.

`screens/forms.py`: delete the `exposes` parameter of `ConfirmExitDialog.__init__`, `self._exposes`, and the `for model_id, exposed in self._exposes:` label loop.

Verify: `grep -n "expos" modelman/src/modelman/screens/*.py modelman/src/modelman/queue.py` — expected: no matches.

- [ ] **Step 6: Fix the tests**

Run: `cd modelman && uv run pytest -q`
Delete tests whose subject was the removed behaviour (the `x` key, EXPOSED rendering, expose-ready cascade, queued exposes in `ConfirmExitDialog`, queue expose batches and unexpose cascades). Rewrite tests that only incidentally passed `exposes=`/asserted expose calls to the new contract (no `exposes`; one sync in `run_queued_ops`). Do not delete tests for deletes, moves, downloads or cancellation — only strip their expose assertions.

Expected: PASS; `make check` clean.

- [ ] **Step 7: Commit**

```bash
git add modelman
git commit -m "feat(modelman): queue and TUI drop exposes; one LiteLLM sync after apply/exit (#179)"
```

### Task 9: Local-model start/stop sync instead of expose/unexpose

**Files:**
- Modify: `modelman/src/modelman/local_control.py`
- Test: `modelman/tests/test_local_control*.py` (find with `grep -rln local_control modelman/tests`)

**Interfaces:**
- Consumes: `sync_routes` (Task 7).
- Produces: `start_local_model`, `stop_local_model`, `stop_all_local_models` each call `sync_routes()` exactly once on success paths and append its warnings to their result's `warnings`; no code reads or writes `ModelState.exposed`.

- [ ] **Step 1: Write the failing tests**

Add to `modelman/tests/test_local_control.py` (it has `_registry`, `_state_path`, `patch`, `load_state`):

```python
def test_start_syncs_routes_once_and_writes_no_exposed(tmp_path, wt_calls):
    """#179: starting a model routes it via one `wt litellm sync`; modelman
    no longer flips an exposed flag or calls expose."""
    state_path = _state_path(tmp_path, {})
    with (
        patch("modelman.local_control.isolate_provider"),
        patch("modelman.local_control.stop_provider"),
        patch("modelman.local_control.stop_all_local_providers"),
    ):
        start_local_model(_registry(), "ollama/qwen3.8:27b-mlx", state_path)
    assert [c for c in wt_calls if c[:1] == ["sync"]] == [["sync", "--json"]]
    assert not [c for c in wt_calls if c[:1] in (["expose"], ["unexpose"])]
    assert "exposed" not in state_path.read_text()


def test_stop_all_syncs_routes_once(tmp_path, wt_calls):
    """#179: stop --all costs one sync for every model it stopped."""
    state_path = _state_path(tmp_path, {"ollama/qwen3.8:27b-mlx": True, "omlx/model-a": True})
    with patch("modelman.local_control.stop_all_local_providers"):
        result = stop_all_local_models(state_path)
    assert sorted(result.stopped) == ["ollama/qwen3.8:27b-mlx", "omlx/model-a"]
    assert [c for c in wt_calls if c[:1] == ["sync"]] == [["sync", "--json"]]
```

Delete the `test_stop_all_local_models_*unexpose*` and `*expose*` tests in the same file (their subject is removed) once Step 3 lands; the first new test's `"exposed" not in` check passes only after Task 11 removes the field from the writer — until then assert `load_state(state_path).get("ollama/qwen3.8:27b-mlx").exposed is False` instead, and switch to the text check in Task 11.

- [ ] **Step 2: Run — expect failure**

Run: `cd modelman && uv run pytest tests -q -k "syncs_routes"`
Expected: FAIL (expose calls still made).

- [ ] **Step 3: Implement**

- Replace `_expose_for_start(...)` and its caller block (the `expose_ok, expose_warnings = ...` through the `replace(existing, exposed=True)` merge) with `warnings += sync_routes(litellm_path=litellm_path)` at the point the old expose ran. Delete `_expose_for_start`.
- Replace `_unexpose_for_stop(...)` in `stop_local_model` with `warnings += sync_routes(litellm_path=litellm_path)` **after** the provider stop and flag clear (wt probes live state, so the stop must have happened first). Delete `_unexpose_for_stop`.
- In `stop_all_local_models`, replace the `apply_unexpose_queue(...)` call with one `sync_routes(...)` after all stops.
- In `_clear_stale_running_flag`, delete the unexpose (and the `exposed=` merge); it only clears `running`.
- In `_register_discovered_model`, delete the `expose_model(...)` call, its except branch and the post-expose state merge; update its docstring ("registers the entry and marks it ready"). The start that follows syncs.
- Drop the imports `apply_unexpose_queue`, `expose_model`, `unexpose_model`, `ExposeError` as they become unused.
- Update module docstrings/comments that say "exposes"/"un-exposes".

Verify: `grep -n "expos" modelman/src/modelman/local_control.py` — expected: no matches.

- [ ] **Step 4: Run the suite, fix superseded tests, check**

Run: `cd modelman && uv run pytest -q && make check`
Tests asserting expose/unexpose bridge calls or `exposed` writes in start/stop become sync-call assertions (one per operation). Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add modelman
git commit -m "feat(modelman): local start/stop sync LiteLLM routes instead of expose/unexpose (#179)"
```

### Task 10: CLI — drop expose/unexpose; refresh-prices and ollama-catalog sync; explicit benchmark models

**Files:**
- Modify: `modelman/src/modelman/main.py` (`expose`, `unexpose` commands; `refresh_prices`)
- Modify: `modelman/src/modelman/ollama_catalog.py` (`SyncPlan.routes`, its computation, `format_plan`)
- Modify: `modelman/src/modelman/ollama_catalog_cli.py`
- Modify: `modelman/src/modelman/benchmark/runner.py` (`discover_targets`), `modelman/src/modelman/benchmark/cli.py`
- Test: `modelman/tests/test_expose.py` (delete), `modelman/tests/commands/test_refresh_prices.py`, `modelman/tests/test_ollama_catalog*.py`, `modelman/tests/benchmark/test_runner*.py`

**Interfaces:**
- Consumes: `sync_routes` (Task 7).
- Produces: `discover_targets(registry, state, model_ids=None, family=None)` raises `ValueError("name models (--model) or pass --family")` when both are None.

- [ ] **Step 1: Write the failing tests**

`tests/commands/test_refresh_prices.py` — add:

```python
def test_refresh_prices_syncs_routes(tmp_path, monkeypatch, wt_calls):
    """#179: new prices only reach LiteLLM through a sync."""
    _seed_registry(tmp_path, monkeypatch)
    payload = {"data": [{"id": "openai/gpt-4o", "pricing": {"prompt": "0.0000025", "completion": "0.00001"}}]}
    with patch("modelman.pricing._default_runner", side_effect=_runner(payload)):
        result = CliRunner().invoke(app, ["refresh-prices"])
    assert result.exit_code == 0
    assert [c for c in wt_calls if c[:1] == ["sync"]] == [["sync", "--json"]]
```

Benchmark (in the existing `discover_targets` test module):

```python
def test_discover_targets_requires_explicit_selection():
    """#179: the exposed-models default is gone; benchmarking needs a choice."""
    with pytest.raises(ValueError, match="--model"):
        discover_targets(Registry(), StateStore())
```

ollama-catalog (in the existing CLI test module): a sync run with price changes but no pulls/removals must end with exactly one `["sync", "--json"]` call; `format_plan` output must not contain "Not yet exposed".

- [ ] **Step 2: Run — expect failures**

Run: `cd modelman && uv run pytest tests/commands/test_refresh_prices.py tests/benchmark tests -q -k "syncs_routes or requires_explicit or ollama_catalog"`
Expected: FAIL.

- [ ] **Step 3: Implement**

- `main.py`: delete the `expose` and `unexpose` commands and their imports (`ExposeError`, `expose_model`, `unexpose_model`). In `refresh_prices`, after the stamp block, add:

```python
    for warning in sync_routes():
        typer.echo(f"warning: {warning}", err=True)
```

- `ollama_catalog.py`: delete `SyncPlan.routes` and the code filling it (the "Registry ids of every page model: (re)exposed" block); change `format_plan(plan: SyncPlan) -> str` (drop `exposed`), delete the "(exposed — will be unexposed)" suffix and the "LiteLLM routes … Not yet exposed" section.
- `ollama_catalog_cli.py`: drop the `exposed = {...}` line and pass `format_plan(plan)`; drop `exposes=` from `QueuedOps(...)`; change the condition to `if ops.ready or ops.deletes:` → `run_queued_ops(ops)` (which syncs), `else:` → `for w in sync_routes(): typer.echo(f"warning: {w}", err=True)`; fix the comments about re-exposing.
- `benchmark/runner.py` `discover_targets`: at the top add

```python
    if model_ids is None and family is None:
        raise ValueError("name models (--model) or pass --family")
```

and delete the `state.get(model.id).exposed` default block. In `benchmark/cli.py`, catch that `ValueError` where `discover_targets` is called and exit 2 with `typer.echo(f"error: {exc}", err=True)`.

- [ ] **Step 4: Remove obsolete tests, run, check**

Delete `tests/test_expose.py` and `tests/commands/test_expose*.py` (their subject is gone). Update ollama-catalog tests that asserted `plan.routes`/`exposes`. Update benchmark tests that relied on the exposed default to pass `family=` or `model_ids=`.

Run: `cd modelman && uv run pytest -q && make check`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add modelman
git commit -m "feat(modelman): drop expose/unexpose CLI; refresh-prices and ollama-catalog sync routes; benchmark needs explicit models (#179)"
```

### Task 11: Delete the exposed flag and the expose machinery

**Files:**
- Modify: `modelman/src/modelman/state.py` (`ModelState.exposed`, reader, writer, docstring)
- Modify: `modelman/src/modelman/sync.py` (`exposed=existing.exposed` ×2, docstring)
- Modify: `modelman/src/modelman/litellm.py` (delete `ProviderPolicy`'s exposure doc wording, `ExposeError`, `is_cloud_effective`, `passes_ready_gate`, `is_effectively_exposed`, `_set_exposed_flag`, `_provider_flags_for_write`, `_validate_locally`, `_outcome_error`, `expose_model`, `unexpose_model`, `apply_expose_queue`, `apply_unexpose_queue`; rewrite the module docstring)
- Modify: `modelman/src/modelman/wt_bridge.py` (delete `expose`, `unexpose`, `_reconcile_after_timeout`, the timeout special-case in `_change`)
- Modify: `docs/contracts/modelman.sample.toml`, `modelman/tests/contracts/test_modelman_fixture.py`, `wt/internal/config/modelman_fixture_test.go` (only the comment/expectations that mention exposure — wt keeps reading the flag until PR 3)
- Test: `modelman/tests/test_state.py`

- [ ] **Step 1: Write the failing test**

```python
def test_state_drops_legacy_exposed_keys(tmp_path):
    """#179: exposed/litellm_exposed are read-ignored and never written back,
    so they leave modelman.toml on the next save."""
    p = tmp_path / "modelman.toml"
    p.write_text('[models."ollama/a:1"]\nready = true\nexposed = true\nlitellm_exposed = true\n')
    state = load_state(p)
    save_state(state, p)
    text = p.read_text()
    assert "exposed" not in text
    assert state.get("ollama/a:1").ready is True
```

(`save_state` and `load_state` are `modelman.state`'s public read/write functions.)

- [ ] **Step 2: Run — expect failure**

Run: `cd modelman && uv run pytest tests/test_state.py -q -k legacy_exposed`
Expected: FAIL (`exposed = true` written back).

- [ ] **Step 3: Implement**

- `state.py`: delete the `exposed` field; in the reader drop `exposed=...` and keep `"exposed"`, `"litellm_exposed"` in the known-keys tuple so they are **not** preserved as unknown extras; in the writer drop `"exposed": s.exposed`; fix the module docstring (wt no longer reads `exposed`).
- `sync.py`: drop both `exposed=existing.exposed` arguments and the docstring sentence.
- `litellm.py` / `wt_bridge.py`: delete the listed functions; `provider_policy`, `provider_table_available` and `is_cloud` stay only if something still imports them (`grep -rn "provider_policy\|provider_table_available\|is_cloud\b" modelman/src`); delete them otherwise. New module docstring: "LiteLLM helpers: `sync_routes` (the one write path, via `wt litellm sync`) and read-only config.yaml helpers for `modelman usage`."
- `docs/contracts/modelman.sample.toml`: delete the exposure-rule header and the exposure-only rows; keep one row that still carries `exposed = true` with a comment "legacy key — both readers ignore it (#179)". Update `test_modelman_fixture.py` accordingly (assert the row loads and `ready` is read). wt's `modelman_fixture_test.go` keeps passing (wt still reads the flag until PR 3); update its counts if a row was removed.

Verify: `grep -rn "exposed\|expose_model\|ExposeError" modelman/src` — expected: no matches (except the `"exposed", "litellm_exposed"` known-keys tuple in `state.py`).

- [ ] **Step 4: Run, check**

Run: `cd modelman && uv run pytest -q && make check && cd ../wt && go test ./internal/config`
Expected: PASS.

- [ ] **Step 5: Commit and open PR 2**

```bash
git add modelman docs/contracts wt/internal/config/modelman_fixture_test.go
git commit -m "refactor(modelman): delete the exposed flag and expose machinery (#179)"
```

Run `make test-all`, push, open PR 2 ("feat(modelman): one LiteLLM sync replaces expose/unexpose (#179 phase A, 2/4)").

---

# PR 3 — wt: remove expose/unexpose and the flag readers

Branch: `feat/179-a3-wt-cleanup` from `main` after PR 2 merges.

### Task 12: Remove `wt litellm expose|unexpose`, the exposure map and the EXPOSED column

**Files:**
- Modify: `wt/cmd/wt/litellm.go` (`litellmFlags.DryRun/SkipReadyGate`, `runLitellmChange`, the `change(...)` builder and its two `AddCommand` entries)
- Modify: `wt/internal/litellm/service.go` (`Check`; `Options.SkipReadyGate` stays — `Sync` and the lifecycle hook use it)
- Modify: `wt/internal/config/config.go` (`exposed` field, `ExposedFlag`, `SetExposureForTest`, `SetExposedForTest`; `ReadyFlag` stays — the local ready gate in `prepare` uses it until Phase B), `wt/internal/config/modelman.go` (`ExposureEntry.Exposed`, legacy `litellm_exposed` parsing)
- Modify: `wt/internal/catalog/catalog.go` (`Row.Exposed`), `wt/internal/tui/modeltable.go` (EXPOSED header and cell)
- Modify: `docs/contracts/litellm-cli.sample.json` (drop expose/unexpose and "cannot be exposed" examples), `modelman/tests/contracts/test_litellm_cli_fixture.py`
- Test: whatever references the removed names

> **Spec gap closed here:** the spec's Phase A lists modelman's EXPOSED column but not wt's picker column. With no flag left to show, wt's EXPOSED column goes too; the spec is updated in this plan's commit.

- [ ] **Step 1: Write the failing test**

Change the two existing header assertions (they become the failing tests):

- `wt/internal/tui/model_line_test.go:88` — expected header becomes `"FAMILY MODEL LOC STATUS RUNNING COST 1D 7D 30D SURVEY"`.
- `wt/internal/tui/modeltable_test.go:32` — drop `"EXPOSED"` from the header list, delete the `col("EXPOSED")` cell assertion (lines ~72–73), and drop "EXPOSED Y/-" from the test's `//` comment.

- [ ] **Step 2: Run — expect failure**

Run: `cd wt && go test ./internal/tui`
Expected: FAIL.

- [ ] **Step 3: Implement the removals**

Delete the listed code. Rename `ExposureEntry` to `ModelmanEntry` holding only `Ready` (keep the loader reading `ready` with `downloaded` fallback). In `modeltable.go`, remove the EXPOSED header cell and `padRunes(flag(r.Exposed, "Y"), 7)`. Remove `Exposed` from `catalog.Row` and its assignment.

Verify: `grep -rn "Exposed\|expose" wt --include='*.go' | grep -v _test` — expected: only `Options.SkipReadyGate` doc text mentioning exposure history, if any (reword it).

- [ ] **Step 4: Update tests and fixtures**

Delete tests whose subject was `expose`/`unexpose`/`Check`/`ExposedFlag`; convert row-shape tests that listed the EXPOSED column. Update `litellm-cli.sample.json` and the modelman fixture test.

Run: `cd wt && go build ./... && go vet ./... && go test ./... && test -z "$(gofmt -l cmd internal)" && cd ../modelman && uv run pytest tests/contracts -q`
Expected: PASS.

- [ ] **Step 5: Commit and open PR 3**

```bash
git add wt docs/contracts modelman/tests/contracts docs/superpowers/specs/2026-10-02-configured-is-exposed-design.md
git commit -m "refactor(wt): remove litellm expose/unexpose, the exposed flag and the EXPOSED column (#179)"
```

`make test-all`, push, open PR 3 ("refactor(wt): remove the exposed concept (#179 phase A, 3/4)").

---

# PR 4 — Docs and guides

Branch: `docs/179-a4-guides` from `main` after PR 3 merges.

### Task 13: Rewrite the docs around sync

**Files:** root `CLAUDE.md`; `modelman/CLAUDE.md`; `wt/CLAUDE.md`; `README.md`; `modelman/README.md`; `docs/guides/00-config-map.md`, `01-initial-setup.md`, `02-providers-and-models.md`, `04-litellm-config.md`, `05-benchmarks.md`, `06-wt-agents-and-models.md`, `08-maintenance-and-troubleshooting.md`, `10-mlx-lm-quantization.md`; `modelman/.claude/skills/ollama-catalog/SKILL.md`.

- [ ] **Step 1: Find every stale mention**

Run: `git grep -n -i "expos" -- '*.md' ':!docs/superpowers' ':!docs/archive' ':!**/CHANGELOG.md'`

- [ ] **Step 2: Apply these rules to every hit**

- "expose X" as a user action → "add X to the registry (modelman); `wt litellm sync` routes it".
- `modelman expose|unexpose`, `wt litellm expose|unexpose`, the `x` key, the EXPOSED column → removed; point to `wt litellm sync [--dry-run]` and `wt litellm list` (which marks hand-written rows).
- Root `CLAUDE.md`: delete the "`exposed` is only authoritative for cloud/native models" gotcha; in the "Guides never embed live model state" gotcha replace the `grep -c "exposed = true" …` example with `wt litellm list`; change the quick-test example `tests/test_expose.py` to `tests/test_routes_sync.py`.
- `wt/CLAUDE.md`: replace the "Exposure predicate" section with "Catalog membership (`InCatalog`)" (native/local/cloud in; dangling provider out) and add the ownership marker + reconciliation rules under "LiteLLM routes"; drop EXPOSED from the Rotation columns list.
- `modelman/CLAUDE.md`: delete the `expose`/`unexpose` command row, the EXPOSED/ready-gate invariants, the exposure predicate paragraph and the `apply_expose_queue` description; document `sync_routes` as the only LiteLLM write path; update the queue's apply order (no exposes step).
- Guides 02 and 04 are restructured, not patched: 02 becomes "add a model → it is routed", 04 explains sync, the marker, hand-written rows and `--dry-run`.
- Guide 05: the benchmark needs `--model` or `--family`.
- No guide may include live counts or route lists (CLAUDE.md rule).
- Add a dated `wt/CHANGELOG.md` entry (Unreleased → Changed/Removed).

- [ ] **Step 3: Verify**

Run: `git grep -n -i "expose" -- '*.md' ':!docs/superpowers' ':!docs/archive' ':!**/CHANGELOG.md'` — expected: no matches (or only historical sentences explicitly labelled as such). Then `make check-links`.

- [ ] **Step 4: Commit and open PR 4**

```bash
git add -A '*.md' modelman/.claude/skills
git commit -m "docs: configured is exposed — guides and CLAUDE.md around wt litellm sync (#179)"
```

`make test-all`, push, open PR 4 ("docs: configured is exposed (#179 phase A, 4/4)").

### Task 14: Phase A acceptance (after PR 4 merges)

- [ ] `make install` from main.
- [ ] Spec A-1 (already checked in Task 6 against PR 1; re-run once with the final binary): eligible lists per agent unchanged versus the Task 6 capture.
- [ ] Spec A-2: `wt litellm list` — every hand-written row still present and labelled `hand-written`.
- [ ] Spec A-3: add a throwaway openrouter model in modelman (TUI add, then quit) → `wt litellm list` shows it marked; delete it (TUI `d`, Apply) → its route is gone.
- [ ] Spec A-4: `curl -s localhost:4000/model/info` answers; one agent launch through LiteLLM works (`wt smoke <an openrouter id> --only claude`).
- [ ] Spec A-5: `make test-all`.
- [ ] Comment the results on #179 and start the Phase B plan.
