# omlx Pool, Part 1 (wt) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** wt treats omlx as a multi-model pool: a start loads beside what is loaded, a stop unloads one model, and routes follow each model's loaded state.

**Architecture:** `internal/localmodels` gains one reading of omlx's pool (status first). `internal/lifecycle` replaces its single-model boolean with a three-value tenancy and an eviction plan; the omlx backend loads and unloads through omlx's per-model endpoints and reconciles routes from the pool after every load. `cmd/wt` gains `wt start --plan/--json` for modelman.

**Tech Stack:** Go 1.26.7 (module root `wt/`), cobra, `net/http/httptest` fakes. No new dependencies.

**Spec:** [docs/superpowers/specs/2026-10-06-omlx-pool-design.md](../specs/2026-10-06-omlx-pool-design.md). This plan is PR 1 of the spec's two. PR 2 (modelman delegation) gets its own plan after this merges.

## Global Constraints

- Run every Go command from `wt/`, never the monorepo root.
- Every `Test*` function has a top-level `//` comment saying what it tests and why a regression matters to a user.
- Tests never touch the developer's machine: no real omlx, no real `config.yaml`. Lifecycle tests use `testEnv()`; `cmd/wt` tests rely on its `TestMain` stubs.
- mtplx and ollama behavior must not change. Their existing test assertions stay as they are; only helper names may change.
- omlx routes follow loaded state, per model. `wt stop <model>` leaves the service up. `wt stop omlx` halts it.
- `poolAdmissionMargin` is 10% of the ceiling, one named constant.
- `--json` output never prompts.
- Docs under `docs/guides/` never embed live model state.
- Before each commit: `gofmt -l .` prints nothing, `go vet ./...` passes.
- Read `wt/docs/internals/local-models.md` and `wt/docs/internals/testing.md` before starting.

## Review Focus

1. **omlx evicts a model the plan said would stay** (the soft watermark). Expected: its route is removed and a line says so. Pinned in Task 4 (`TestStartOnPoolReportsAnUnpredictedEviction`).
2. **The load fails after omlx already evicted something.** Expected: the evicted model's route still goes. Pinned in Task 4 (`TestStartOnPoolFailedLoadStillReconciles`).
3. **Status is refused (keyed omlx, no `secret_ref`).** Expected: wt still knows what is loaded, names every other loaded model as a possible victim, and asks. Pinned in Task 1 (`TestOmlxPoolFallsBackWhenStatusIsRefused`) and Task 3 (`TestPoolVictims`, "sizes unknown").
4. **A model is aliased in omlx and every model is loaded.** Expected: it reads as running under its directory name. Pinned in Task 1 (`TestOmlxPoolReportsOnDiskIDsForAnAliasedModel`).
5. **Two registry rows (`omlx` and `omlx-6bit`) name the same on-disk model.** Expected: its size is counted once in the plan. Pinned in Task 3 (`TestPoolVictims`, "two rows, one model").

## File Structure

| File | Change | Responsibility |
|---|---|---|
| `wt/internal/localmodels/pool.go` | create | `Pool`, `PoolModel`, `OmlxPool`: one reading of omlx's pool |
| `wt/internal/localmodels/served.go` | modify | `ServedIDs("omlx")` derives from `OmlxPool` |
| `wt/internal/localmodels/inventory.go` | modify | `Snapshot.OmlxPool` carried from the probe |
| `wt/internal/lifecycle/tenancy.go` | create | `Tenancy`, `TenancyOf` |
| `wt/internal/lifecycle/evictions.go` | create | `Evictions`, the pool victim walk |
| `wt/internal/lifecycle/omlxpool.go` | create | `omlxLoad`, `omlxUnload`, `NoRoomError` |
| `wt/internal/lifecycle/lifecycle.go` | modify | `start` plan and reconcile, `OccupiedError.Occupants`, `Options.OnUnloaded` |
| `wt/internal/lifecycle/omlx.go` | modify | `Pool` tenancy, load and unload |
| `wt/internal/lifecycle/routes.go` | modify | per-model removal for `Pool` |
| `wt/cmd/wt/start.go`, `wt/internal/tui/start_flow.go` | modify | render the occupant list |
| `wt/cmd/wt/model_cmds.go` | modify | `wt stop omlx` halts; `wt start --plan/--json` |
| `wt/cmd/wt/start_json.go` | create | the JSON start and plan |
| `docs/contracts/wt-start-cli.sample.json` | create | the JSON shapes modelman will parse |

---

### Task 1: The pool reading

**Files:**
- Create: `wt/internal/localmodels/pool.go`, `wt/internal/localmodels/pool_test.go`
- Modify: `wt/internal/localmodels/served.go:24-30`, `wt/internal/localmodels/inventory.go` (`source`, `Snapshot`, `probeFamily`, `inventory`), `wt/internal/localmodels/served_test.go`

**Interfaces:**
- Consumes: `getJSON`, `omlxLoaded`, `FamilyOrigin`, `FamilyAPIKey`, `NameMatches` (all existing in `localmodels`).
- Produces:
  - `type PoolModel struct { ID string; Loaded, Loading, Pinned bool; LastAccess float64; Size int64 }`
  - `type Pool struct { Models []PoolModel; Ceiling, InUse int64; SizesKnown bool }`
  - `func (p Pool) LoadedIDs() []string` (loaded or loading)
  - `func (p Pool) Find(name string) (PoolModel, bool)`
  - `func OmlxPool(cfg *config.Config, client *http.Client) (Pool, error)`
  - `Snapshot.OmlxPool *Pool` (nil when omlx was not probed or gave no reading)

- [ ] **Step 1: Extend the fake omlx**

In `served_test.go`, add fields to `fakeOmlx` and serve them from the status handler. Add after the `statusHits int` field:

```go
	noStatus   bool               // an omlx old enough to have no status endpoint
	sizes      map[string]int64   // resident_estimated_size per model
	pinned     map[string]bool
	lastAccess map[string]float64
	ceiling    int64 // final_ceiling
	inUse      int64 // current_model_memory
```

Replace the body of the `/v1/models/status` handler with:

```go
		f.statusHits++
		if f.noStatus {
			http.NotFound(w, r)
			return
		}
		if f.key != "" && r.Header.Get("Authorization") != "Bearer "+f.key {
			http.Error(w, `{"error":{"message":"API key required"}}`, http.StatusUnauthorized)
			return
		}
		ms := []map[string]any{}
		for id, l := range f.pool {
			ms = append(ms, map[string]any{
				"id": id, "loaded": l, "is_loading": id == f.loading,
				"pinned": f.pinned[id], "resident_estimated_size": f.sizes[id],
				"last_access": f.lastAccess[id],
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"final_ceiling": f.ceiling, "current_model_memory": f.inUse, "models": ms,
		})
```

- [ ] **Step 2: Write the failing tests**

Create `pool_test.go`:

```go
package localmodels

import (
	"slices"
	"testing"
)

// TestOmlxPoolReadsSizesAndFlags verifies the pool reading carries what the
// eviction plan needs: each model's size, pin and last access, and the pool's
// ceiling and total. Without them wt cannot tell a load that fits from one
// that unloads a model a session is using.
func TestOmlxPoolReadsSizesAndFlags(t *testing.T) {
	srv := &fakeOmlx{
		listed: []string{"A", "B"}, pool: map[string]bool{"A": true, "B": false},
		sizes: map[string]int64{"A": 20, "B": 5}, pinned: map[string]bool{"A": true},
		lastAccess: map[string]float64{"A": 100}, ceiling: 64, inUse: 20,
	}
	p, err := OmlxPool(omlxCfgAt(srv.serve(t), ""), testClient)
	if err != nil {
		t.Fatal(err)
	}
	if !p.SizesKnown || p.Ceiling != 64 || p.InUse != 20 {
		t.Errorf("pool = %+v, want sizes known, ceiling 64, in use 20", p)
	}
	a, ok := p.Find("A")
	if !ok || !a.Loaded || !a.Pinned || a.Size != 20 || a.LastAccess != 100 {
		t.Errorf("A = %+v ok=%v, want loaded, pinned, size 20, last access 100", a, ok)
	}
	if got := p.LoadedIDs(); !slices.Equal(got, []string{"A"}) {
		t.Errorf("loaded = %v, want [A]", got)
	}
}

// TestOmlxPoolReportsOnDiskIDsForAnAliasedModel pins #213 item 5. omlx lists
// an aliased model under its alias, and wt matches models by directory name,
// so reading "all loaded" off the list made an aliased model read as stopped:
// the picker offered to start a model that was already serving.
func TestOmlxPoolReportsOnDiskIDsForAnAliasedModel(t *testing.T) {
	srv := &fakeOmlx{listed: []string{"my-alias"}, pool: map[string]bool{"Qwen-4bit": true}}
	p, err := OmlxPool(omlxCfgAt(srv.serve(t), ""), testClient)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.LoadedIDs(); !slices.Equal(got, []string{"Qwen-4bit"}) {
		t.Errorf("loaded = %v, want the on-disk id [Qwen-4bit], not the alias", got)
	}
}

// TestOmlxPoolFallsBackWhenStatusIsRefused verifies a keyed omlx whose key the
// registry does not name still yields the loaded set from /health and the
// list, marked as having no sizes. wt must then ask before a second load
// instead of assuming it fits.
func TestOmlxPoolFallsBackWhenStatusIsRefused(t *testing.T) {
	srv := &fakeOmlx{listed: []string{"A"}, pool: map[string]bool{"A": false}, key: "sk-omlx"}
	p, err := OmlxPool(omlxCfgAt(srv.serve(t), ""), testClient)
	if err != nil {
		t.Fatalf("idle keyed pool, no key: %v", err)
	}
	if p.SizesKnown || len(p.LoadedIDs()) != 0 {
		t.Errorf("pool = %+v, want no sizes and nothing loaded", p)
	}
}

// TestInventoryCarriesTheOmlxPool verifies the snapshot hands the pool reading
// to its consumers, so the picker's replace question and the start engine's
// plan are computed from the same probe instead of two that can disagree.
func TestInventoryCarriesTheOmlxPool(t *testing.T) {
	srv := &fakeOmlx{listed: []string{"A"}, pool: map[string]bool{"A": true}, ceiling: 64, inUse: 20}
	cfg := omlxCfgAt(srv.serve(t), "")
	cfg.Providers[0].ModelDir = t.TempDir()
	snap := inventory(cfg, testClient)
	if snap.OmlxPool == nil || snap.OmlxPool.Ceiling != 64 {
		t.Fatalf("snapshot pool = %+v, want the reading with ceiling 64", snap.OmlxPool)
	}
}
```

- [ ] **Step 3: Run them and confirm they fail**

Run: `go test ./internal/localmodels -run 'TestOmlxPool|TestInventoryCarriesTheOmlxPool' -v`
Expected: build failure, `undefined: OmlxPool`.

- [ ] **Step 4: Implement `pool.go`**

```go
package localmodels

import (
	"net/http"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// PoolModel is one model of omlx's engine pool, named by its on-disk id (the
// model directory's name, never an alias).
type PoolModel struct {
	ID         string
	Loaded     bool
	Loading    bool
	Pinned     bool    // omlx never evicts a pinned model
	LastAccess float64 // omlx evicts the smallest first; 0 when never used
	Size       int64   // resident estimate in bytes; 0 when unknown
}

// Pool is one reading of omlx's engine pool. Ceiling is 0 when omlx's memory
// guard is off. SizesKnown is false when the reading came from the fallback
// (/health and the list), which names what is loaded and nothing else.
type Pool struct {
	Models     []PoolModel
	Ceiling    int64
	InUse      int64
	SizesKnown bool
}

// LoadedIDs lists the models that occupy the pool: loaded, or mid-load.
func (p Pool) LoadedIDs() []string {
	var ids []string
	for _, m := range p.Models {
		if m.Loaded || m.Loading {
			ids = append(ids, m.ID)
		}
	}
	return ids
}

// Find returns the pool model a provider-side name denotes, by the same
// lenient match the inventory uses for omlx artifacts.
func (p Pool) Find(name string) (PoolModel, bool) {
	for _, m := range p.Models {
		if NameMatches(m.ID, name) {
			return m, true
		}
	}
	return PoolModel{}, false
}

// OmlxPool reads omlx's pool. /v1/models/status is asked first: it names
// models by on-disk id, counts hidden ones, sees a model mid-load, and carries
// the sizes the eviction plan needs (#213). It is behind omlx's management
// auth, so the registry's key is sent when there is one. When status does not
// answer — a keyed server whose key the registry does not name, a server still
// initialising, an omlx without the endpoint — omlxLoaded's /health and list
// reading supplies the loaded ids alone, with its errors unchanged: a pool
// that will not say what is loaded is an error, never a guess.
func OmlxPool(cfg *config.Config, client *http.Client) (Pool, error) {
	origin, _ := FamilyOrigin(cfg, "omlx")
	key := FamilyAPIKey(cfg, "omlx")
	if p, ok := omlxStatusPool(client, origin, key); ok {
		return p, nil
	}
	ids, err := omlxLoaded(client, origin, key)
	if err != nil {
		return Pool{}, err
	}
	p := Pool{}
	for _, id := range ids {
		p.Models = append(p.Models, PoolModel{ID: id, Loaded: true})
	}
	return p, nil
}

// omlxStatusPool is the status half of OmlxPool; ok is false when status gave
// no model listing (a refusal answers with a JSON object too, without one).
func omlxStatusPool(client *http.Client, origin, key string) (Pool, bool) {
	var status struct {
		Ceiling int64 `json:"final_ceiling"`
		InUse   int64 `json:"current_model_memory"`
		Models  *[]struct {
			ID         string  `json:"id"`
			Loaded     bool    `json:"loaded"`
			Loading    bool    `json:"is_loading"`
			Pinned     bool    `json:"pinned"`
			LastAccess float64 `json:"last_access"`
			Resident   int64   `json:"resident_estimated_size"`
			Estimated  int64   `json:"estimated_size"`
		} `json:"models"`
	}
	if _, err := getJSON(client, origin+"/v1/models/status", key, &status); err != nil || status.Models == nil {
		return Pool{}, false
	}
	p := Pool{Ceiling: status.Ceiling, InUse: status.InUse, SizesKnown: true}
	for _, m := range *status.Models {
		if m.ID == "" {
			continue
		}
		size := m.Resident
		if size == 0 {
			size = m.Estimated
		}
		p.Models = append(p.Models, PoolModel{ID: m.ID, Loaded: m.Loaded, Loading: m.Loading, Pinned: m.Pinned, LastAccess: m.LastAccess, Size: size})
	}
	return p, true
}
```

- [ ] **Step 5: Derive `ServedIDs` and the snapshot from it**

In `served.go`, replace the omlx branch of `ServedIDs`:

```go
	if family == "omlx" {
		p, err := OmlxPool(cfg, client)
		if err != nil {
			return nil, err
		}
		return p.LoadedIDs(), nil
	}
```

Update `ServedIDs`'s doc comment's last line to: `omlx's is not (#201) — see OmlxPool.`

In `inventory.go`:

1. Add to `source`: `pool *Pool // omlx only: the reading loaded came from`
2. Add to `Snapshot`, after `ProbeFailures`:

```go
	// OmlxPool is the omlx pool reading this round's Running flags came
	// from: sizes, pins and the ceiling the eviction plan needs
	// (lifecycle.Evictions). Nil when omlx was not probed or gave no reading.
	OmlxPool *Pool
```

3. In `probeFamily`'s `case "omlx", "mtplx":`, replace `loaded, err := ServedIDs(cfg, client, family)` and the lines through `s.loaded = loaded` with:

```go
		var loaded []string
		var err error
		if family == "omlx" {
			var p Pool
			if p, err = OmlxPool(cfg, client); err == nil {
				s.pool = &p
				loaded = p.LoadedIDs()
			}
		} else {
			loaded, err = ServedIDs(cfg, client, family)
		}
		if err != nil {
			s.status = StatusPartial
			s.down = refused(err)
			s.probeErr = err
		}
		s.loaded = loaded
```

4. In `inventory`, inside the `for i, f := range families` loop that fills `snap.Providers`, add:

```go
		if results[i].pool != nil {
			snap.OmlxPool = results[i].pool
		}
```

- [ ] **Step 6: Update the legacy served tests**

The cost order they pinned (status asked only for a mixed pool) is gone: status is now asked first. In `served_test.go`:

- `TestServedIDsOmlxCountsOnlyLoadedModels`: delete the `statusHits` column from the table struct and every row, and drop `|| srv.statusHits != tc.statusHits` and the two `statusHits` format arguments from the assertion. In the `"no /health endpoint"` row add `noStatus: true` (an omlx that old has no status either), keeping `want: []string{"A", "B"}`. Rewrite the test's doc comment's last sentence to: `ServedIDs answers from what omlx says is loaded: status when it answers, else /health's counts and the list.`
- `TestServedIDsOmlxNeedsTheKeyOnlyForAMixedPool`: in the final `idle` assertion remove `|| idle.statusHits != 0` and the `status asked %d times` argument; replace its comment with `// A refused status does not make an idle pool an error: /health settles it.`

- [ ] **Step 7: Run the package**

Run: `go test ./internal/localmodels`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/localmodels
git commit -m "feat(wt): read omlx's pool from status first (#213)"
```

---

### Task 2: Tenancy replaces the single-model boolean

A pure refactor. omlx stays `Exclusive` in this task, so nothing changes behavior.

**Files:**
- Create: `wt/internal/lifecycle/tenancy.go`
- Modify: `wt/internal/lifecycle/lifecycle.go` (`backend`, `Occupant`, `resolveOccupant`, `SingleModel`), `ollama.go:20`, `omlx.go:18`, `mtplx.go:19`, `routes.go:142,392`, `wt/internal/survey/stop.go:159,166,293`, `wt/cmd/wt/model_cmds.go:169,174,193`, and the test helpers that implement `backend`

**Interfaces:**
- Produces:
  - `type Tenancy int` with `NoTenancy` (zero), `Exclusive`, `Shared`, `Pool`
  - `func TenancyOf(providerID string) Tenancy`
  - `backend.tenancy() Tenancy` (replaces `singleModel() bool`)

- [ ] **Step 1: Write the failing test**

Append to `lifecycle_test.go`:

```go
// TestTenancyOfEachFamily pins how wt treats each provider's server. The
// start, stop and route rules all switch on it, so a wrong value either
// replaces a model that could have stayed or leaves a stopped one routed.
func TestTenancyOfEachFamily(t *testing.T) {
	for id, want := range map[string]Tenancy{
		"ollama": Shared, "mtplx": Exclusive, "omlx": Exclusive, "omlx-6bit": Exclusive,
		"mlx_lm_server": NoTenancy, "llamacpp": NoTenancy,
	} {
		if got := TenancyOf(id); got != want {
			t.Errorf("TenancyOf(%q) = %v, want %v", id, got, want)
		}
	}
}
```

- [ ] **Step 2: Run it and confirm it fails**

Run: `go test ./internal/lifecycle -run TestTenancyOfEachFamily`
Expected: build failure, `undefined: Tenancy`.

- [ ] **Step 3: Create `tenancy.go`**

```go
package lifecycle

import "github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"

// Tenancy is how a provider's server holds models, which decides what a start
// displaces, what a stop takes down, and which routes each removes.
type Tenancy int

const (
	// NoTenancy: wt has no lifecycle backend for the provider.
	NoTenancy Tenancy = iota
	// Exclusive: one model per process (mtplx). A start replaces the
	// occupant, a stop stops the process, and the family's routes go as one.
	Exclusive
	// Shared: models load beside each other and a pulled model is served on
	// request (ollama). Routes follow the artifact, not loaded state.
	Shared
	// Pool: several models loaded at once under a memory ceiling, evicted by
	// least recent use (omlx). A start loads beside and may evict; a stop
	// unloads one model; routes follow each model's loaded state.
	Pool
)

// TenancyOf reports how wt treats providerID's family. backendsByFamily is the
// single source of truth, so this cannot drift from the engine.
func TenancyOf(providerID string) Tenancy {
	b := backendsByFamily[localmodels.Family(providerID)]
	if b == nil {
		return NoTenancy
	}
	return b.tenancy()
}
```

- [ ] **Step 4: Switch the interface and every caller**

In `lifecycle.go`, in `type backend interface`, replace the `singleModel` method and its comment with:

```go
	// tenancy reports how the provider's server holds models.
	tenancy() Tenancy
```

Delete `func SingleModel` and its doc comment. Then:

| File | Replace | With |
|---|---|---|
| `ollama.go` | `func (ollamaBackend) singleModel() bool { return false }` | `func (ollamaBackend) tenancy() Tenancy { return Shared }` |
| `omlx.go` | `func (omlxBackend) singleModel() bool { return true }` | `func (omlxBackend) tenancy() Tenancy { return Exclusive }` |
| `mtplx.go` | `func (mtplxBackend) singleModel() bool { return true }` | `func (mtplxBackend) tenancy() Tenancy { return Exclusive }` |
| `lifecycle.go` (`Occupant`, `resolveOccupant`) | `b == nil \|\| !b.singleModel()` | `b == nil \|\| b.tenancy() != Exclusive` |
| `routes.go` (both sites) | `SingleModel(x)` | `TenancyOf(x) == Exclusive` |
| `survey/stop.go` (three sites), `cmd/wt/model_cmds.go` (three sites) | `lifecycle.SingleModel(x)` | `lifecycle.TenancyOf(x) == lifecycle.Exclusive` |

Then run `grep -rn 'SingleModel\|singleModel' --include='*.go' .` and fix what is left. In `lifecycle_test.go` replace the fake's method:

```go
type fakeBackend struct {
	single   bool
	ten      Tenancy // overrides single when set
	calls    *[]string
	stopErr  error
	startErr error
}

func (f *fakeBackend) tenancy() Tenancy {
	switch {
	case f.ten != NoTenancy:
		return f.ten
	case f.single:
		return Exclusive
	}
	return Shared
}
```

Any other test type implementing `backend`, and any test asserting `.singleModel()` (for example `TestOllamaIsMultiTenant` in `backends_test.go`), changes the same way: `(ollamaBackend{}).singleModel()` becomes `(ollamaBackend{}).tenancy() != Shared`. Reword comments that say "single-model" only where they name the removed function.

- [ ] **Step 5: Run everything**

Run: `go build ./... && go test ./...`
Expected: PASS, with no assertion changed other than the ones named above.

- [ ] **Step 6: Commit**

```bash
git add -A .
git commit -m "refactor(wt): three-value tenancy replaces the single-model boolean (#213)"
```

---

### Task 3: The eviction plan and a list of occupants

**Files:**
- Create: `wt/internal/lifecycle/evictions.go`, `wt/internal/lifecycle/evictions_test.go`
- Modify: `wt/internal/lifecycle/lifecycle.go` (`OccupiedError`, delete `Occupant`, `resolveOccupant` → `resolveEvictions`, `start`), `wt/cmd/wt/start.go:265-278`, `wt/internal/tui/start_flow.go:415`, tests that read `.Occupant` or call `Occupant(`

**Interfaces:**
- Consumes: `Tenancy` (Task 2), `localmodels.Pool`, `Snapshot.OmlxPool` (Task 1).
- Produces:
  - `func Evictions(t Target, snap localmodels.Snapshot) (victims []localmodels.Entry, known bool)`
  - `type OccupiedError struct{ Occupants []localmodels.Entry }` and `func (e *OccupiedError) IDs() []string`
  - `func (e *env) resolveEvictions(ctx, cfg, family string, t Target, snap localmodels.Snapshot) (victims []localmodels.Entry, unknown bool)`

- [ ] **Step 1: Write the failing tests**

Create `evictions_test.go`:

```go
package lifecycle

import (
	"reflect"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

func ids(es []localmodels.Entry) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.ModelID)
	}
	return out
}

// TestPoolVictims pins who wt says a pool start would unload. Too few names
// and a session loses its model with no warning; too many and wt asks to
// replace models that would have stayed loaded.
func TestPoolVictims(t *testing.T) {
	a := localmodels.Entry{ProviderID: "omlx", ModelID: "omlx/A", ModelName: "A", Artifact: "A", Running: true}
	b := localmodels.Entry{ProviderID: "omlx", ModelID: "omlx/B", ModelName: "B", Artifact: "B", Running: true}
	a6 := localmodels.Entry{ProviderID: "omlx-6bit", ModelID: "omlx-6bit/A", ModelName: "A", Artifact: "A", Running: true}
	target := Target{ProviderID: "omlx", ModelName: "T"}
	pool := func(ceiling, inUse int64, ms ...localmodels.PoolModel) *localmodels.Pool {
		return &localmodels.Pool{Models: ms, Ceiling: ceiling, InUse: inUse, SizesKnown: true}
	}
	pm := func(id string, size int64, last float64, pinned bool) localmodels.PoolModel {
		return localmodels.PoolModel{ID: id, Loaded: id != "T", Size: size, LastAccess: last, Pinned: pinned}
	}
	for _, tc := range []struct {
		name   string
		pool   *localmodels.Pool
		others []localmodels.Entry
		want   []string
	}{
		{"fits under the margin", pool(100, 40, pm("A", 40, 1, false), pm("T", 40, 0, false)), []localmodels.Entry{a}, nil},
		// 40 + 55 = 95 is under the ceiling but over the 90 the margin leaves.
		{"inside the margin", pool(100, 40, pm("A", 40, 1, false), pm("T", 55, 0, false)), []localmodels.Entry{a}, []string{"omlx/A"}},
		{"oldest goes first, and only as many as needed", pool(100, 80, pm("A", 40, 9, false), pm("B", 40, 1, false), pm("T", 30, 0, false)), []localmodels.Entry{a, b}, []string{"omlx/B"}},
		{"a pinned model is never named", pool(100, 80, pm("A", 40, 9, false), pm("B", 40, 1, true), pm("T", 30, 0, false)), []localmodels.Entry{a, b}, []string{"omlx/A"}},
		{"cannot fit: every unpinned model", pool(100, 80, pm("A", 40, 9, false), pm("B", 40, 1, false), pm("T", 200, 0, false)), []localmodels.Entry{a, b}, []string{"omlx/B", "omlx/A"}},
		{"sizes unknown", &localmodels.Pool{Models: []localmodels.PoolModel{{ID: "A", Loaded: true}}}, []localmodels.Entry{a}, []string{"omlx/A"}},
		{"memory guard off", pool(0, 40, pm("A", 40, 1, false)), []localmodels.Entry{a}, []string{"omlx/A"}},
		{"no pool reading", nil, []localmodels.Entry{a}, []string{"omlx/A"}},
		{"target not in the pool counts as size zero", pool(100, 40, pm("A", 40, 1, false)), []localmodels.Entry{a}, nil},
		// omlx and omlx-6bit rows naming one directory are one loaded model:
		// both rows are named, its 60 is freed once, so B must go too.
		{"two rows, one model", pool(100, 100, pm("A", 60, 1, false), pm("B", 40, 2, false), pm("T", 80, 0, false)), []localmodels.Entry{a, a6, b}, []string{"omlx/A", "omlx-6bit/A", "omlx/B"}},
		{"nothing else loaded", pool(100, 0, pm("T", 200, 0, false)), nil, nil},
	} {
		got := ids(poolVictims(tc.pool, target, tc.others))
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: victims = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestEvictionsByTenancy pins the rule per kind of server: an exclusive server
// displaces its one occupant, a shared one nobody, a pool whoever the plan
// names — and a probe that cannot be trusted is "unknown", never "nobody",
// because acting on a false "nobody" replaces a model wt never saw.
func TestEvictionsByTenancy(t *testing.T) {
	snap := localmodels.Snapshot{Entries: []localmodels.Entry{
		running("mtplx", "mtplx/m1", "Org/M1"),
		running("omlx", "omlx/A", "A"),
		running("ollama", "ollama/a", "a:1b"),
	}}
	if v, known := evictions(Exclusive, "mtplx", Target{ProviderID: "mtplx", ModelName: "Org/M2"}, snap); !known || !reflect.DeepEqual(ids(v), []string{"mtplx/m1"}) {
		t.Errorf("exclusive: %v known=%v, want [mtplx/m1]", ids(v), known)
	}
	if v, known := evictions(Exclusive, "mtplx", Target{ProviderID: "mtplx", ModelName: "Org/M1"}, snap); !known || len(v) != 0 {
		t.Errorf("exclusive, target already running: %v, want none", ids(v))
	}
	if v, known := evictions(Shared, "ollama", Target{ProviderID: "ollama", ModelName: "b:2b"}, snap); !known || len(v) != 0 {
		t.Errorf("shared: %v known=%v, want none", ids(v), known)
	}
	if v, known := evictions(Pool, "omlx", Target{ProviderID: "omlx", ModelName: "T"}, snap); !known || !reflect.DeepEqual(ids(v), []string{"omlx/A"}) {
		t.Errorf("pool with no reading: %v known=%v, want [omlx/A]", ids(v), known)
	}
	snap.Providers = map[string]localmodels.Status{"omlx": localmodels.StatusPartial}
	if v, known := evictions(Pool, "omlx", Target{ProviderID: "omlx", ModelName: "T"}, snap); known || len(v) != 0 {
		t.Errorf("untrusted probe: %v known=%v, want none and unknown", ids(v), known)
	}
}

// TestOccupiedErrorNamesEveryOccupant verifies the refusal names each model a
// start would unload, since on a pool there can be several and the user is
// being asked to give all of them up.
func TestOccupiedErrorNamesEveryOccupant(t *testing.T) {
	err := &OccupiedError{Occupants: []localmodels.Entry{{ModelID: "omlx/A"}, {ModelID: "omlx/B"}}}
	if !reflect.DeepEqual(err.IDs(), []string{"omlx/A", "omlx/B"}) || !containsFold(err.Error(), "omlx/A, omlx/B") {
		t.Errorf("ids = %v, message = %q", err.IDs(), err.Error())
	}
}
```

- [ ] **Step 2: Run them and confirm they fail**

Run: `go test ./internal/lifecycle -run 'TestPoolVictims|TestEvictionsByTenancy|TestOccupiedErrorNamesEveryOccupant'`
Expected: build failure, `undefined: poolVictims`.

- [ ] **Step 3: Create `evictions.go`**

```go
package lifecycle

import (
	"sort"

	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// poolAdmissionMarginPct is the share of omlx's ceiling wt keeps free when it
// predicts whether a load fits. omlx starts evicting at a soft watermark below
// its ceiling and does not report where that is, so a prediction made against
// the ceiling itself would call "fits" on loads that evict.
const poolAdmissionMarginPct = 10

// Evictions reports the running models that starting t is expected to
// displace: an Exclusive server's one occupant, nobody on a Shared server, and
// on a Pool the models the plan says omlx would unload to make room. known is
// false when the snapshot's probe for the family cannot be trusted — a caller
// acting on "nobody" there would displace a model it never saw.
func Evictions(t Target, snap localmodels.Snapshot) (victims []localmodels.Entry, known bool) {
	family := localmodels.Family(t.ProviderID)
	b := backendsByFamily[family]
	if b == nil {
		return nil, true
	}
	return evictions(b.tenancy(), family, t, snap)
}

func evictions(ten Tenancy, family string, t Target, snap localmodels.Snapshot) ([]localmodels.Entry, bool) {
	if ten != Exclusive && ten != Pool {
		return nil, true
	}
	if !ProbeTrusted(snap, family) {
		return nil, false
	}
	others := runningOthers(snap, family, t)
	if ten == Exclusive {
		if len(others) > 1 {
			others = others[:1]
		}
		return others, true
	}
	return poolVictims(snap.OmlxPool, t, others), true
}

// runningOthers lists the family's running models other than the target.
func runningOthers(snap localmodels.Snapshot, family string, t Target) []localmodels.Entry {
	var others []localmodels.Entry
	for _, en := range snap.Entries {
		if !en.Running || localmodels.Family(en.ProviderID) != family || SameModel(family, en.ModelName, t.ModelName) {
			continue
		}
		others = append(others, en)
	}
	return others
}

// poolVictims is the pool plan: which of others omlx is expected to unload so
// that t fits. Without sizes or a ceiling wt cannot tell whether t fits, so
// every other loaded model is named. With them, the walk follows omlx's own
// order — least recently used first, pinned models never — until the
// projection fits under the margin; when it never does, every unpinned model
// is named and omlx gives the final answer at load time.
func poolVictims(pool *localmodels.Pool, t Target, others []localmodels.Entry) []localmodels.Entry {
	if len(others) == 0 {
		return nil
	}
	if pool == nil || !pool.SizesKnown || pool.Ceiling <= 0 {
		return others
	}
	var targetSize int64
	if m, ok := pool.Find(t.ModelName); ok {
		targetSize = m.Size
	}
	over := pool.InUse + targetSize - (pool.Ceiling - pool.Ceiling*poolAdmissionMarginPct/100)
	if over <= 0 {
		return nil
	}
	type cand struct {
		en localmodels.Entry
		m  localmodels.PoolModel
	}
	var cands []cand
	for _, en := range others {
		name := en.Artifact
		if name == "" {
			name = en.ModelName
		}
		if m, ok := pool.Find(name); ok && !m.Pinned {
			cands = append(cands, cand{en, m})
		}
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].m.LastAccess < cands[j].m.LastAccess })
	var victims []localmodels.Entry
	freed := map[string]bool{}
	for _, c := range cands {
		// Two registry rows can name one loaded model: both rows are
		// victims, and its memory is freed once.
		if over <= 0 && !freed[c.m.ID] {
			break
		}
		victims = append(victims, c.en)
		if !freed[c.m.ID] {
			freed[c.m.ID] = true
			over -= c.m.Size
		}
	}
	return victims
}
```

- [ ] **Step 4: Change `OccupiedError` and the engine**

In `lifecycle.go` replace `OccupiedError` and its `Error`:

```go
// OccupiedError means starting the target would displace running models and
// AllowReplace was false. Nothing was touched. An Exclusive server has exactly
// one occupant; a Pool can have several.
type OccupiedError struct{ Occupants []localmodels.Entry }

// IDs lists the occupants' model ids, in the order they would go.
func (e *OccupiedError) IDs() []string {
	ids := make([]string, len(e.Occupants))
	for i, o := range e.Occupants {
		ids[i] = o.ModelID
	}
	return ids
}

func (e *OccupiedError) Error() string {
	return fmt.Sprintf("starting this model would stop %s — confirm to replace", strings.Join(e.IDs(), ", "))
}
```

Add `"strings"` to the imports. Delete `func Occupant` and its comment. Replace `resolveOccupant` with:

```go
// resolveEvictions decides which running models starting t would displace. It
// prefers the snapshot, which is free, and re-probes the server only when the
// snapshot's probe could not be trusted — the case that used to read as "no
// occupant" and let a start evict the running model. The re-probe has no
// sizes, so on a Pool every other loaded model is a victim. unknown reports
// that even the re-probe could not tell; the caller must then require
// AllowReplace.
func (e *env) resolveEvictions(ctx context.Context, cfg *config.Config, family string, t Target, snap localmodels.Snapshot) (victims []localmodels.Entry, unknown bool) {
	b := e.backends[family]
	if b == nil {
		return nil, false
	}
	ten := b.tenancy()
	if v, known := evictions(ten, family, t, snap); known {
		return v, false
	}
	served, known := e.liveServed(ctx, cfg, family)
	if !known {
		return nil, true
	}
	for _, id := range served {
		if SameModel(family, id, t.ModelName) {
			continue // the target itself is already served: not an occupant
		}
		victims = append(victims, localmodels.Entry{ProviderID: t.ProviderID, ModelID: id, ModelName: id, Running: true})
		if ten == Exclusive {
			break
		}
	}
	return victims, false
}
```

In `start`, replace the block from `if occ, has, unknown := e.resolveOccupant(` through its closing brace with:

```go
	victims, unknown := e.resolveEvictions(ctx, cfg, family, t, snap)
	if unknown && !opts.AllowReplace {
		origin, _ := localmodels.FamilyOrigin(cfg, family)
		return &OccupancyUnknownError{ProviderID: t.ProviderID, Origin: origin}
	}
	if len(victims) > 0 && !opts.AllowReplace {
		return &OccupiedError{Occupants: victims}
	}
	if b.tenancy() == Exclusive && len(victims) > 0 {
		occ := victims[0]
		report(StageStoppingOccupant)
		if err := b.stop(ctx, e, cfg); err != nil {
			return fmt.Errorf("stopping %s before starting %s: %w", occ.ModelID, t.ModelName, err)
		}
		// The occupant is down now. Drop its route here rather than after the
		// start, because a start that fails from here on returns without any
		// route hook and would strand the dead occupant's model_list row.
		if e.onOccupantStopped != nil && e.onOccupantStopped(ctx, cfg, occ) {
			e.restartOwed = true
		}
	}
```

- [ ] **Step 5: Update the callers and legacy tests**

`cmd/wt/start.go`, the `errors.As(err, &occ)` case, becomes:

```go
	case errors.As(err, &occ):
		names := strings.Join(occ.IDs(), ", ")
		if !allowReplace {
			ok, perr := ask(fmt.Sprintf("starting %s will stop %s; continue?", id, names))
			if perr != nil {
				return perr
			}
			if !ok {
				return fmt.Errorf("cancelled — %s still running", names)
			}
		}
		fmt.Fprintf(osStderr, "wt: replacing %s\n", names)
```

Add `"strings"` to its imports if missing.

`internal/tui/start_flow.go:415` becomes:

```go
		title = fmt.Sprintf("Starting %s will stop %s", st.item.model.ID, strings.Join(occ.IDs(), ", "))
```

Then run `grep -rn 'Occupant(\|\.Occupant\b\|resolveOccupant\|OccupiedError{Occupant:' --include='*.go' .` and convert each hit:

- `Occupant(t, snap)` returning `(occ, ok)` becomes `v, known := Evictions(t, snap)`; `ok` is `len(v) > 0`, `occ` is `v[0]`. In `TestOccupantRules` the untrusted and unknown-backend cases keep expecting no victims.
- `&OccupiedError{Occupant: x}` becomes `&OccupiedError{Occupants: []localmodels.Entry{x}}`.
- `occ.Occupant.ModelID` in an assertion becomes `occ.IDs()[0]`.
- A test asserting the old prompt text `"is running; stop it and start"` asserts `"will stop"` and the occupant's id instead.

- [ ] **Step 6: Run everything**

Run: `go build ./... && go test ./...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add -A .
git commit -m "feat(wt): eviction plan and a list of occupants (#213)"
```

---

### Task 4: omlx becomes a pool

**Files:**
- Create: `wt/internal/lifecycle/omlxpool.go`, `wt/internal/lifecycle/omlxpool_test.go`
- Modify: `wt/internal/lifecycle/omlx.go`, `lifecycle.go` (`Options`, `start`, `StopModelDeferred`, `TestTenancyOfEachFamily`'s expectation), `routes.go` (`StartRouteChange`, `routeRemove`, `routeAfterStop`, `routeAfterOccupantStopped`), `message.go`, the legacy omlx tests

**Interfaces:**
- Consumes: `localmodels.OmlxPool`, `Pool.Find` (Task 1); `Tenancy` (Task 2); `resolveEvictions`, `runningOthers` (Task 3).
- Produces:
  - `type NoRoomError struct{ Model, Detail string }`
  - `Options.OnUnloaded func(localmodels.Entry)` — called once per model a pool start unloaded, on success and on failure
  - `omlxBackend.tenancy()` returns `Pool`
  - `routeRemoveModel(ctx, cfg, en localmodels.Entry, mode restartMode) bool` and `routeRemoveFamily(ctx, cfg, providerID string, mode restartMode) bool`

- [ ] **Step 1: Write the fake pool**

Create `omlxpool_test.go`:

```go
package lifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"sync"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// fakePool is an omlx 0.7.0 engine pool: status, load and unload by on-disk
// id. evictOnLoad names models omlx unloads when a load is asked for, whether
// or not the load then succeeds; loadCodes are answered in order before a load
// succeeds (507, 409...).
type fakePool struct {
	mu          sync.Mutex
	loaded      map[string]bool
	sizes       map[string]int64
	ceiling     int64
	evictOnLoad []string
	loadCodes   []int
	loads       []string
	unloads     []string
}

func (f *fakePool) serve(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	mux.HandleFunc("GET /v1/models/status", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		ms := []map[string]any{}
		var inUse int64
		for id, l := range f.loaded {
			if l {
				inUse += f.sizes[id]
			}
			ms = append(ms, map[string]any{"id": id, "loaded": l, "resident_estimated_size": f.sizes[id]})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"final_ceiling": f.ceiling, "current_model_memory": inUse, "models": ms})
	})
	mux.HandleFunc("POST /v1/models/{id}/load", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		id := r.PathValue("id")
		f.loads = append(f.loads, id)
		for _, v := range f.evictOnLoad {
			f.loaded[v] = false
		}
		if len(f.loadCodes) > 0 {
			code := f.loadCodes[0]
			f.loadCodes = f.loadCodes[1:]
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"detail":"Cannot load: would exceed the memory ceiling. unload another model."}`))
			return
		}
		if _, ok := f.loaded[id]; !ok {
			http.Error(w, `{"detail":"Model not found"}`, http.StatusNotFound)
			return
		}
		f.loaded[id] = true
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("POST /v1/models/{id}/unload", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		id := r.PathValue("id")
		f.unloads = append(f.unloads, id)
		if !f.loaded[id] {
			http.Error(w, `{"detail":"Model not loaded"}`, http.StatusBadRequest)
			return
		}
		f.loaded[id] = false
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

func (f *fakePool) isLoaded(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.loaded[id]
}

// poolEnv is a testEnv whose omlx backend is the real one, whose inventory is
// snap, and which records the occupant hook and fails the test on any CLI run
// (a pool start or model stop must never run `omlx stop`).
func poolEnv(t *testing.T, snap localmodels.Snapshot) (*env, *[]string) {
	t.Helper()
	e := testEnv()
	e.inventory = func(*config.Config) localmodels.Snapshot { return snap }
	e.lookPath = func(string) (string, error) { return "/bin/omlx", nil }
	e.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		t.Errorf("ran %s %v: a pool start or model stop must not run the omlx CLI", name, args)
		return nil, nil
	}
	var stopped []string
	e.onOccupantStopped = func(_ context.Context, _ *config.Config, occ localmodels.Entry) bool {
		stopped = append(stopped, occ.ModelID)
		return true
	}
	return e, &stopped
}

func poolSnap(ceiling int64, sizes map[string]int64, loaded ...string) localmodels.Snapshot {
	p := &localmodels.Pool{Ceiling: ceiling, SizesKnown: true}
	snap := localmodels.Snapshot{Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK}, OmlxPool: p}
	for id, size := range sizes {
		l := slices.Contains(loaded, id)
		p.Models = append(p.Models, localmodels.PoolModel{ID: id, Loaded: l, Size: size})
		if l {
			p.InUse += size
		}
		snap.Entries = append(snap.Entries, localmodels.Entry{ProviderID: "omlx", ModelID: "omlx/" + id, ModelName: id, Artifact: id, Running: l})
	}
	return snap
}

// captureRoutes redirects route output for one test.
func captureRoutes(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := routesWarn
	routesWarn = &buf
	t.Cleanup(func() { routesWarn = old })
	return &buf
}
```

- [ ] **Step 2: Write the failing tests**

Append to `omlxpool_test.go`:

```go
// TestStartOnPoolLoadsBesideALoadedModel pins #213 item 1: starting a second
// omlx model that fits loads it and leaves the first one loaded. It used to
// run `omlx stop`, unloading a model another session was using.
func TestStartOnPoolLoadsBesideALoadedModel(t *testing.T) {
	sizes := map[string]int64{"A": 20, "B": 20}
	fp := &fakePool{loaded: map[string]bool{"A": true, "B": false}, sizes: sizes, ceiling: 100}
	e, stopped := poolEnv(t, poolSnap(100, sizes, "A"))
	err := start(context.Background(), e, provCfg("omlx", fp.serve(t)), Target{ProviderID: "omlx", ModelName: "B", ModelID: "omlx/B"}, Options{})
	if err != nil {
		t.Fatalf("start = %v, want nil with no replace needed", err)
	}
	if !reflect.DeepEqual(fp.loads, []string{"B"}) || !fp.isLoaded("A") || len(*stopped) != 0 {
		t.Errorf("loads = %v, A loaded = %v, occupant hook = %v; want [B], true, none", fp.loads, fp.isLoaded("A"), *stopped)
	}
}

// TestStartOnPoolAsksBeforeEvicting verifies a start that does not fit is
// refused with the models it would unload, and sends no load: a load is what
// makes omlx evict, so asking after it would be too late.
func TestStartOnPoolAsksBeforeEvicting(t *testing.T) {
	sizes := map[string]int64{"A": 60, "B": 60}
	fp := &fakePool{loaded: map[string]bool{"A": true, "B": false}, sizes: sizes, ceiling: 100}
	e, _ := poolEnv(t, poolSnap(100, sizes, "A"))
	err := start(context.Background(), e, provCfg("omlx", fp.serve(t)), Target{ProviderID: "omlx", ModelName: "B"}, Options{})
	var occ *OccupiedError
	if !errors.As(err, &occ) || !reflect.DeepEqual(occ.IDs(), []string{"omlx/A"}) {
		t.Fatalf("start = %v, want *OccupiedError naming omlx/A", err)
	}
	if len(fp.loads) != 0 {
		t.Errorf("loads = %v, want none before the user agrees", fp.loads)
	}
}

// TestStartOnPoolRemovesAnEvictedModelsRoute verifies that once the user
// agrees, omlx does the evicting and wt drops the evicted model's route and
// tells the caller, so no route points at a model that is no longer loaded.
func TestStartOnPoolRemovesAnEvictedModelsRoute(t *testing.T) {
	out := captureRoutes(t)
	sizes := map[string]int64{"A": 60, "B": 60}
	fp := &fakePool{loaded: map[string]bool{"A": true, "B": false}, sizes: sizes, ceiling: 100, evictOnLoad: []string{"A"}}
	e, stopped := poolEnv(t, poolSnap(100, sizes, "A"))
	var unloaded []string
	opts := Options{AllowReplace: true, OnUnloaded: func(en localmodels.Entry) { unloaded = append(unloaded, en.ModelID) }}
	if err := start(context.Background(), e, provCfg("omlx", fp.serve(t)), Target{ProviderID: "omlx", ModelName: "B"}, opts); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*stopped, []string{"omlx/A"}) || !reflect.DeepEqual(unloaded, []string{"omlx/A"}) || !e.restartOwed {
		t.Errorf("hook = %v, OnUnloaded = %v, restartOwed = %v; want [omlx/A] twice and true", *stopped, unloaded, e.restartOwed)
	}
	if got := out.String(); !containsFold(got, "omlx unloaded omlx/A") || containsFold(got, "not predicted") {
		t.Errorf("output = %q, want the eviction line without 'not predicted'", got)
	}
}

// TestStartOnPoolReportsAnUnpredictedEviction covers omlx's soft watermark: a
// load wt predicted would fit can still evict. The route must go and the line
// must say wt did not see it coming, or a session loses its model silently.
func TestStartOnPoolReportsAnUnpredictedEviction(t *testing.T) {
	out := captureRoutes(t)
	sizes := map[string]int64{"A": 20, "B": 20}
	fp := &fakePool{loaded: map[string]bool{"A": true, "B": false}, sizes: sizes, ceiling: 100, evictOnLoad: []string{"A"}}
	e, stopped := poolEnv(t, poolSnap(100, sizes, "A"))
	if err := start(context.Background(), e, provCfg("omlx", fp.serve(t)), Target{ProviderID: "omlx", ModelName: "B"}, Options{}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*stopped, []string{"omlx/A"}) || !containsFold(out.String(), "omlx unloaded omlx/A to make room (not predicted)") {
		t.Errorf("hook = %v, output = %q", *stopped, out.String())
	}
}

// TestStartOnPoolFailedLoadStillReconciles verifies a load omlx refuses for
// want of memory is reported as that, and that a model omlx evicted on the way
// to refusing still loses its route.
func TestStartOnPoolFailedLoadStillReconciles(t *testing.T) {
	captureRoutes(t)
	sizes := map[string]int64{"A": 60, "B": 60}
	fp := &fakePool{loaded: map[string]bool{"A": true, "B": false}, sizes: sizes, ceiling: 100, evictOnLoad: []string{"A"}, loadCodes: []int{http.StatusInsufficientStorage}}
	e, stopped := poolEnv(t, poolSnap(100, sizes, "A"))
	err := start(context.Background(), e, provCfg("omlx", fp.serve(t)), Target{ProviderID: "omlx", ModelName: "B"}, Options{AllowReplace: true})
	var noRoom *NoRoomError
	if !errors.As(err, &noRoom) || !containsFold(noRoom.Error(), "memory ceiling") {
		t.Fatalf("start = %v, want *NoRoomError carrying omlx's message", err)
	}
	if !reflect.DeepEqual(*stopped, []string{"omlx/A"}) {
		t.Errorf("hook = %v, want the evicted omlx/A even though the load failed", *stopped)
	}
}

// TestOmlxLoadWaitsOutABusyAnswer verifies a 409 (the model is already
// loading) is waited for instead of failing: a second `wt start` of a model
// mid-load must end with it loaded, not with an error.
func TestOmlxLoadWaitsOutABusyAnswer(t *testing.T) {
	fp := &fakePool{loaded: map[string]bool{"B": false}, loadCodes: []int{http.StatusConflict, http.StatusConflict}}
	if err := omlxLoad(context.Background(), testEnv(), provCfg("omlx", fp.serve(t)), "org/B"); err != nil {
		t.Fatal(err)
	}
	if len(fp.loads) != 3 || !fp.isLoaded("B") {
		t.Errorf("loads = %v, B loaded = %v; want three attempts by on-disk id and loaded", fp.loads, fp.isLoaded("B"))
	}
}

// TestStopModelOnPoolUnloadsOnlyThatModel pins #213 item 1's other half:
// stopping one omlx model unloads it and leaves the service and its siblings
// up. It used to run `omlx stop`, taking every loaded model down.
func TestStopModelOnPoolUnloadsOnlyThatModel(t *testing.T) {
	fp := &fakePool{loaded: map[string]bool{"A": true, "B": true}}
	e, _ := poolEnv(t, localmodels.Snapshot{})
	if err := stopModel(context.Background(), e, provCfg("omlx", fp.serve(t)), "omlx", "A"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fp.unloads, []string{"A"}) || fp.isLoaded("A") || !fp.isLoaded("B") {
		t.Errorf("unloads = %v, A = %v, B = %v", fp.unloads, fp.isLoaded("A"), fp.isLoaded("B"))
	}
	// Already gone is success: the goal is "not loaded".
	if err := stopModel(context.Background(), e, provCfg("omlx", fp.serve(t)), "omlx", "A"); err != nil {
		t.Errorf("stopping an unloaded model = %v, want nil", err)
	}
}

// TestPoolRouteChanges pins the route rules for a pool: a start adds its model
// and clears nothing else, and a model stop removes that model's ids only.
// Clearing the family is what made sync and the start hook undo each other
// with two models loaded (#213 item 2).
func TestPoolRouteChanges(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}}},
		Models:    []config.Model{{ID: "omlx/reg", ProviderID: "omlx", ModelName: "org/Reg-4bit", Location: config.LocationLocal}},
	}
	if ch := StartRouteChange(cfg, Target{ProviderID: "omlx", ModelName: "org/Reg-4bit", ModelID: "omlx/reg"}); len(ch.RemoveFamilies) != 0 {
		t.Errorf("start removes families %v, want none on a pool", ch.RemoveFamilies)
	}
	got := poolRouteIDs(cfg, localmodels.Entry{ProviderID: "omlx", ModelID: "omlx/reg", ModelName: "org/Reg-4bit"})
	if !slices.Contains(got, "omlx/reg") {
		t.Errorf("route ids = %v, want the registry id", got)
	}
	disc := poolRouteIDs(cfg, localmodels.Entry{ProviderID: "omlx", ModelID: "Loose-4bit", ModelName: "Loose-4bit"})
	if !reflect.DeepEqual(disc, []string{"omlx/Loose-4bit"}) {
		t.Errorf("route ids for an entry the re-probe built = %v, want its discovered id only", disc)
	}
}
```

- [ ] **Step 3: Run them and confirm they fail**

Run: `go test ./internal/lifecycle -run 'OnPool|TestOmlxLoad|TestPoolRouteChanges'`
Expected: build failure, `undefined: NoRoomError`.

- [ ] **Step 4: Create `omlxpool.go`**

```go
package lifecycle

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// NoRoomError means omlx refused to load a model for want of memory: the model
// is larger than the ceiling, or nothing evictable frees enough. Detail is
// omlx's own message, which names what holds the memory.
type NoRoomError struct{ Model, Detail string }

func (e *NoRoomError) Error() string {
	return fmt.Sprintf("omlx has no room for %s: %s", e.Model, e.Detail)
}

// omlxPoolID is the on-disk id omlx knows modelName by: the pool model it
// matches, else the name's last path segment (registry names are HF repo ids,
// omlx serves directory basenames).
func omlxPoolID(cfg *config.Config, e *env, modelName string) string {
	if pool, err := localmodels.OmlxPool(cfg, e.probeClient); err == nil {
		if m, ok := pool.Find(modelName); ok {
			return m.ID
		}
	}
	return path.Base(modelName)
}

// omlxPost sends one bodiless management POST, with the registry's key when
// there is one, and returns the status and omlx's `detail` (else the body).
func omlxPost(ctx context.Context, e *env, target, key string) (code int, detail string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, nil)
	if err != nil {
		return 0, "", err
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := e.chatClient.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var doc struct {
		Detail string `json:"detail"`
	}
	if json.Unmarshal(body, &doc) == nil && doc.Detail != "" {
		return resp.StatusCode, doc.Detail, nil
	}
	return resp.StatusCode, truncateForError(body), nil
}

// omlxLoad loads modelName into the running omlx through its load endpoint,
// which blocks until the model is loaded. omlx evicts by its own rule when the
// model does not fit; the caller reconciles from the pool afterwards. A model
// already loading (409) and a server still initialising (503, or no answer
// yet) are waited for; a refused key and "no room" end at once, since waiting
// changes neither.
func omlxLoad(ctx context.Context, e *env, cfg *config.Config, modelName string) error {
	origin, _ := localmodels.FamilyOrigin(cfg, "omlx")
	key := localmodels.FamilyAPIKey(cfg, "omlx")
	id := omlxPoolID(cfg, e, modelName)
	target := origin + "/v1/models/" + url.PathEscape(id) + "/load"
	bounded, cancel := context.WithTimeout(ctx, e.warmupTimeout)
	defer cancel()
	last := "no attempts completed"
	for {
		code, detail, err := omlxPost(bounded, e, target, key)
		switch {
		case err != nil:
			last = err.Error()
		case code >= 200 && code < 300:
			return nil
		case code == http.StatusUnauthorized || code == http.StatusForbidden:
			return &KeyRefusedError{URL: target, KeySent: key != "", Detail: detail}
		case code == http.StatusInsufficientStorage:
			return &NoRoomError{Model: id, Detail: detail}
		case code == http.StatusNotFound:
			return fmt.Errorf("omlx has no model %q in its pool: %s", id, detail)
		case code == http.StatusConflict || code == http.StatusServiceUnavailable:
			last = fmt.Sprintf("HTTP %d: %s", code, detail)
		default:
			return fmt.Errorf("omlx could not load %s: HTTP %d: %s", id, code, detail)
		}
		if e.sleep(bounded, e.pollInterval) != nil {
			if err := ctx.Err(); err != nil {
				return err
			}
			return fmt.Errorf("timed out loading %s into omlx: %s", id, last)
		}
	}
}

// omlxUnload unloads one model and leaves the service and every other loaded
// model up. The goal is "not loaded", so omlx's "not loaded" (400) and "no
// such model" (404) answers are success, and the pool is read afterwards: a
// model it still shows loaded is a failed stop whatever the POST said.
func omlxUnload(ctx context.Context, e *env, cfg *config.Config, modelName string) error {
	origin, _ := localmodels.FamilyOrigin(cfg, "omlx")
	key := localmodels.FamilyAPIKey(cfg, "omlx")
	id := omlxPoolID(cfg, e, modelName)
	target := origin + "/v1/models/" + url.PathEscape(id) + "/unload"
	bounded, cancel := context.WithTimeout(ctx, e.loadTimeout)
	defer cancel()
	code, detail, err := omlxPost(bounded, e, target, key)
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	switch {
	case err != nil:
		return fmt.Errorf("unloading %s from omlx: %w", id, err)
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		return &KeyRefusedError{URL: target, KeySent: key != "", Detail: detail}
	case (code >= 200 && code < 300) || code == http.StatusBadRequest || code == http.StatusNotFound:
	default:
		return fmt.Errorf("omlx could not unload %s: HTTP %d: %s", id, code, detail)
	}
	if pool, perr := localmodels.OmlxPool(cfg, e.probeClient); perr == nil {
		if m, ok := pool.Find(modelName); ok && (m.Loaded || m.Loading) {
			return fmt.Errorf("omlx still has %s loaded", id)
		}
	}
	return nil
}
```

- [ ] **Step 5: Switch the omlx backend**

In `omlx.go`:

```go
// omlxBackend serves omlx and omlx-6bit, which are ONE physical daemon on one
// port holding a pool of loaded models.
type omlxBackend struct{}

func (omlxBackend) tenancy() Tenancy { return Pool }
```

In `start`, replace the last line `return omlxWarm(ctx, e, cfg, t.ModelName)` with `return omlxLoad(ctx, e, cfg, t.ModelName)`.

Replace `stopModel` and its comment:

```go
// stopModel unloads the one named model; the service and every other loaded
// model stay up (omlxUnload).
func (omlxBackend) stopModel(ctx context.Context, e *env, cfg *config.Config, modelName string) error {
	return omlxUnload(ctx, e, cfg, modelName)
}
```

`omlxWarm`, `Warm` and `warm` are unchanged; `wt warm` still uses them. Update `omlxWarm`'s doc comment's first sentence to say it is `wt warm`'s request, no longer the start path's.

- [ ] **Step 6: Reconcile in the engine**

In `lifecycle.go`, add to `Options`:

```go
	// OnUnloaded (optional) receives each model a Pool start unloaded to make
	// room, whether or not the start then succeeded.
	OnUnloaded func(localmodels.Entry)
```

and update the `Options` doc comment: `AllowReplace permits displacing running models (an Exclusive server's occupant, or a Pool's evictions).`

In `start`, replace the final `return b.start(ctx, e, cfg, t, report)` with:

```go
	if b.tenancy() != Pool {
		return b.start(ctx, e, cfg, t, report)
	}
	// What was loaded before the load, to compare with what is loaded after:
	// omlx decides what to evict, and can evict more or less than the plan
	// named — or evict and then fail the load.
	before := victims
	if ProbeTrusted(snap, family) {
		before = runningOthers(snap, family, t)
	}
	err := b.start(ctx, e, cfg, t, report)
	e.reconcilePool(ctx, cfg, before, victims, opts)
	return err
```

Add below `start`:

```go
// reconcilePool finds the models a pool load unloaded — those of before that
// omlx no longer has loaded — and, for each, removes its route (the deferred
// write Start's one settling bounce applies), says so, and tells the caller.
// planned is what the eviction plan named; anything else is marked, because
// omlx evicts from a soft watermark wt cannot see. A pool that cannot be read
// now changes nothing: the next sync reconciles the routes.
func (e *env) reconcilePool(ctx context.Context, cfg *config.Config, before, planned []localmodels.Entry, opts Options) {
	if len(before) == 0 {
		return
	}
	pool, err := localmodels.OmlxPool(cfg, e.probeClient)
	if err != nil {
		return
	}
	for _, en := range before {
		name := en.Artifact
		if name == "" {
			name = en.ModelName
		}
		if m, ok := pool.Find(name); ok && (m.Loaded || m.Loading) {
			continue
		}
		note := " (not predicted)"
		for _, p := range planned {
			if p.ModelID == en.ModelID {
				note = ""
			}
		}
		routePrintf(ctx, "wt: omlx unloaded %s to make room%s\n", en.ModelID, note)
		if e.onOccupantStopped != nil && e.onOccupantStopped(ctx, cfg, en) {
			e.restartOwed = true
		}
		if opts.OnUnloaded != nil {
			opts.OnUnloaded(en)
		}
	}
}
```

Update `Start`'s and `StopModelDeferred`'s doc comments: replace "single-model providers (omlx, mtplx)" with "an Exclusive provider (mtplx)" and add that on a Pool (omlx) a start loads beside and a model stop unloads one model. In `TestTenancyOfEachFamily` change `"omlx": Exclusive, "omlx-6bit": Exclusive` to `Pool`.

- [ ] **Step 7: Per-model route removal**

In `routes.go`:

1. In `StartRouteChange`, replace `if SingleModel(...)`/`if TenancyOf(t.ProviderID) == Exclusive` so that only `Exclusive` sets `RemoveFamilies` (already true after Task 2; update the function's and `routeAfterStart`'s comments to say a Pool start adds its model and clears nothing).

2. Replace `routeAfterStop`, `routeRemove` and `routeAfterOccupantStopped` with:

```go
func routeAfterStop(ctx context.Context, cfg *config.Config, providerID string) {
	routeRemoveFamily(ctx, cfg, providerID, restartIfChanged)
}

// routeRemoveFamily writes the removal for a provider that was stopped as a
// whole (lifecycle.Stop): on an Exclusive or Pool server every route of the
// family goes, since the server is down. For a family whose routes follow its
// artifacts (ollama) it writes nothing: a pulled model is still served on
// request (#179).
func routeRemoveFamily(ctx context.Context, cfg *config.Config, providerID string, mode restartMode) bool {
	family := localmodels.Family(providerID)
	if localmodels.RoutesFollowArtifact(family) {
		return false
	}
	if ten := TenancyOf(providerID); ten != Exclusive && ten != Pool {
		return false
	}
	return applyAndReport(ctx, cfg, litellm.Change{RemoveFamilies: []string{family}}, mode)
}

// routeRemoveModel writes the removal for one model that stopped serving and
// reports whether config.yaml changed. On an Exclusive server the model was
// the provider, so the family goes; on a Pool only that model's ids go, and
// its loaded siblings keep their routes (#213).
func routeRemoveModel(ctx context.Context, cfg *config.Config, en localmodels.Entry, mode restartMode) bool {
	if TenancyOf(en.ProviderID) != Pool {
		return routeRemoveFamily(ctx, cfg, en.ProviderID, mode)
	}
	return applyAndReport(ctx, cfg, litellm.Change{Remove: poolRouteIDs(cfg, en)}, mode)
}

// poolRouteIDs lists the ids a pool model can be routed under: its registry
// id when a registry model matches, and its discovered id. An entry built by
// the occupancy re-probe carries the server's raw id as ModelID, which is no
// route id, so ModelID is used only when it differs from the name.
func poolRouteIDs(cfg *config.Config, en localmodels.Entry) []string {
	var ids []string
	add := func(id string) {
		if id != "" && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	if en.ModelID != en.ModelName {
		add(en.ModelID)
	}
	if m, ok := litellm.ModelFor(cfg, en.ProviderID, en.ModelName); ok {
		add(m.ID)
	}
	if len(ids) == 0 {
		add(litellm.DiscoveredModel(en.ProviderID, en.ModelName).ID)
	}
	return ids
}

// routeAfterOccupantStopped removes the route of a model a start displaced: an
// Exclusive server's stopped occupant, or a model a Pool load evicted. It is
// the env hook defaultEnv wires in (env.go): the start may still fail, and a
// failed Start runs no success hook at all, so the removal has to be written
// at the moment the model went away. It only writes: the proxy restart is
// deferred to Start's single settling bounce (routeAfterStart on success,
// bounceRoutes on failure). It reports whether a restart is now owed.
func routeAfterOccupantStopped(ctx context.Context, cfg *config.Config, occ localmodels.Entry) bool {
	return routeRemoveModel(ctx, cfg, occ, restartDeferred)
}
```

3. In `lifecycle.go`, `StopModelDeferred`'s last line becomes:

```go
	return routeRemoveModel(ctx, cfg, localmodels.Entry{ProviderID: providerID, ModelName: modelName}, restartDeferred), nil
```

- [ ] **Step 8: Word the new error**

`StartErrorMessage`'s default arm already prefixes the model id (`failed to start <id>: omlx has no room for ...`). No change to `message.go` beyond adding `*NoRoomError` to the comment's list of errors that fall through to the default wording.

- [ ] **Step 9: Update the legacy omlx tests**

Run `go test ./internal/lifecycle ./cmd/wt ./internal/tui ./internal/survey`. Fix each failing omlx test by these rules, changing nothing about what mtplx or ollama tests assert:

| Test | Change |
|---|---|
| `TestOmlxAlreadyUpSkipsStartAndWarmsBasename` | Serve a `fakePool{loaded: {"Qwen3.8-27B-4bit": false}}`; assert `fp.loads == ["Qwen3.8-27B-4bit"]` in place of `srv.lastModel()`. Rename to `…LoadsBasename`. |
| `TestOmlxStartsDaemonWhenDown` | The handler `e.run` starts must answer the load: use `serveAt(t, addr, poolHandler)` where the handler is a `fakePool`'s mux. Extract `func (f *fakePool) handler() http.Handler` from `serve` for this. Stages stay `[StageStarting, StageWarming]`. |
| `TestOmlxStartSendsRegistryKey`, `…WithoutKeyFailsAtOnce`, `…WithWrongKeySaysTheKeyWasRefused` | `newKeyedOmlx` must also guard `POST /v1/models/{id}/load` with the key and answer 401 without it. Assertions unchanged (`*KeyRefusedError`, `KeySent`). |
| `TestStopModelOmlxRunsOmlxStopAndWaitsForPort` | Delete; `TestStopModelOnPoolUnloadsOnlyThatModel` replaces it. |
| `TestRouteAfterStartOmlx6bitClearsOmlxFamily` | Rename to `…KeepsOmlxFamily`; expect `families` empty. |
| `TestRouteHooksRequestOnlyRealFamilies` | An omlx-6bit **start** now requests no family; an omlx-6bit provider **stop** (`routeAfterStop`) still requests `"omlx"`. |
| `TestStopRemovesOmlx6bitSibling` and other `Stop(provider)` tests | Unchanged: a provider stop still clears the family. |
| `TestSyncAndStartHookAgreeWhenOmlxListsAnUnloadedSibling` (`cmd/wt`) | Still passes its id-equality check; if it also asserts the start hook's `RemoveFamilies`, expect none. |
| `cmd/wt` and `survey` tests asserting omlx collateral ("stopping it also stops") or family-wide session counts | Move the assertion to an mtplx fixture where the test is about the Exclusive rule; where it is about omlx, expect no collateral and per-model counts. |

- [ ] **Step 10: Run everything**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: PASS.

- [ ] **Step 11: Commit**

```bash
git add -A .
git commit -m "feat(wt): omlx is a pool — load beside, unload one, per-model routes (#213)"
```

---

### Task 5: `wt stop omlx` halts the service

After Task 4 the stop picker and `wt stop <omlx model>` already work per model, because the collateral and family-session rules apply to `Exclusive` only. What is left is the bare provider.

**Files:**
- Modify: `wt/cmd/wt/model_cmds.go` (`runStop`, seams), `wt/cmd/wt/model_cmds_test.go`

**Interfaces:**
- Consumes: `lifecycle.Stop(ctx, cfg, providerID) error` (existing), `lifecycle.TenancyOf` (Task 2).
- Produces: seam `var stopProvider = lifecycle.Stop`.

- [ ] **Step 1: Write the failing tests**

Append to `model_cmds_test.go`:

```go
// TestStopBareOmlxHaltsTheService verifies `wt stop omlx` stops the omlx
// service as a whole, even with nothing loaded. A per-model stop only unloads,
// so this is the one command that frees the server's memory.
func TestStopBareOmlxHaltsTheService(t *testing.T) {
	stopped := stubStop(t, nil)
	var halted []string
	old := stopProvider
	stopProvider = func(_ context.Context, _ *config.Config, id string) error { halted = append(halted, id); return nil }
	t.Cleanup(func() { stopProvider = old })
	cfg := &config.Config{Providers: []config.Provider{{ID: "omlx", Location: config.LocationLocal}}}
	var out bytes.Buffer
	if err := runStop(&out, cfg, "omlx", true); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(halted, []string{"omlx"}) || len(*stopped) != 0 {
		t.Errorf("halted = %v, per-model stops = %v; want [omlx] and none", halted, *stopped)
	}
}

// TestStopOneOmlxModelLeavesItsSiblings verifies `wt stop <omlx model>` stops
// that model alone and never announces collateral: on a pool the other loaded
// models stay up.
func TestStopOneOmlxModelLeavesItsSiblings(t *testing.T) {
	a := survey.Candidate{Entry: localmodels.Entry{ProviderID: "omlx", ModelID: "omlx/A", ModelName: "A", Running: true}}
	b := survey.Candidate{Entry: localmodels.Entry{ProviderID: "omlx", ModelID: "omlx/B", ModelName: "B", Running: true}}
	stopped := stubStop(t, []survey.Candidate{a, b})
	cfg := &config.Config{Providers: []config.Provider{{ID: "omlx", Location: config.LocationLocal}}}
	var out bytes.Buffer
	if err := runStop(&out, cfg, "omlx/A", true); err != nil {
		t.Fatal(err)
	}
	if len(*stopped) != 1 || (*stopped)[0].ModelID != "omlx/A" || strings.Contains(out.String(), "also stops") {
		t.Errorf("stopped = %+v, output = %q; want omlx/A alone and no collateral line", *stopped, out.String())
	}
}
```

Add any missing imports (`bytes`, `context`, `reflect`, `strings`).

- [ ] **Step 2: Run them and confirm the first fails**

Run: `go test ./cmd/wt -run 'TestStopBareOmlx|TestStopOneOmlxModel'`
Expected: build failure, `undefined: stopProvider`.

- [ ] **Step 3: Implement**

In `model_cmds.go` add to the seam `var (...)` block:

```go
	// stopProvider stops a provider's server as a whole (`wt stop omlx`).
	stopProvider = lifecycle.Stop
```

In `runStop`, replace the bare-provider `else` branch (from `} else {` through the `nothing running` return) with:

```go
	} else {
		pool := lifecycle.TenancyOf(arg) == lifecycle.Pool
		for _, c := range cands {
			// A pool's server is stopped as a whole, so every model of the
			// family is affected, whichever of its rows it is listed under.
			if c.Entry.ProviderID == arg || (pool && localmodels.Family(c.Entry.ProviderID) == localmodels.Family(arg)) {
				targets = append(targets, c)
			}
		}
		if len(targets) == 0 && !pool {
			fmt.Fprintf(out, "wt: nothing running on %s\n", arg)
			return nil
		}
	}
```

Replace the final `return stopEntries(out, cfg, entries)` with:

```go
	if !strings.Contains(arg, "/") && lifecycle.TenancyOf(arg) == lifecycle.Pool {
		// A model stop on a pool only unloads; the bare provider halts the
		// service, which is the one way to free the server itself.
		ctx, cancel := startSignalCtx()
		defer cancel()
		fmt.Fprintf(out, "Stopping %s... ", arg)
		if err := stopProvider(ctx, cfg, arg); err != nil {
			fmt.Fprintln(out, "failed")
			return err
		}
		fmt.Fprintln(out, "done")
		return nil
	}
	return stopEntries(out, cfg, entries)
```

Update `stopCmd`'s `Long` text: after the first sentence add `On omlx, stopping a model unloads that model and leaves the service and its other models up; "wt stop omlx" stops the service.`

- [ ] **Step 4: Run the package**

Run: `go test ./cmd/wt`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/wt
git commit -m "feat(wt): wt stop omlx halts the service; a model stop only unloads (#213)"
```

---

### Task 6: `wt start --plan` and `--json`

**Files:**
- Create: `wt/cmd/wt/start_json.go`, `wt/cmd/wt/start_json_test.go`, `docs/contracts/wt-start-cli.sample.json`
- Modify: `wt/cmd/wt/model_cmds.go` (`startCmd`), `wt/cmd/wt/start.go` (session counts in the prompt)

**Interfaces:**
- Consumes: `lifecycle.Evictions` (Task 3), `Options.OnUnloaded` (Task 4), `lifecycleStart`, `localRowsSnap`, `waitPendingRoutes`, `ensureRouteBeforeLaunch`, `startFailure`, `startSignalCtx` (existing in `cmd/wt`).
- Produces:
  - `wt start <id> --plan --json` → `{"id", "status": "running"|"fits"|"would_unload"|"unknown", "would_unload": [{"id", "sessions"}]}`
  - `wt start <id> [--replace] --json` → `{"id", "status": "started"|"already_running", "unloaded": [ids]}`
  - seam `var sessionCounts func(ids []string) map[string]int`

- [ ] **Step 1: Write the contract fixture**

Create `docs/contracts/wt-start-cli.sample.json`:

```json
{
  "_comment": "Shared fixture for cross-language contract tests of `wt start <id> --json`. Read by wt/cmd/wt/start_json_test.go (Go) and modelman/tests/contracts/ (Python, added with modelman's omlx delegation). A schema change on either side fails both CI jobs in the same PR.",
  "plan_running": {"id": "omlx/B", "status": "running", "would_unload": []},
  "plan_fits": {"id": "omlx/B", "status": "fits", "would_unload": []},
  "plan_would_unload": {"id": "omlx/B", "status": "would_unload", "would_unload": [{"id": "omlx/A", "sessions": 1}]},
  "plan_unknown": {"id": "omlx/B", "status": "unknown", "would_unload": []},
  "started": {"id": "omlx/B", "status": "started", "unloaded": ["omlx/A"]},
  "already_running": {"id": "omlx/B", "status": "already_running", "unloaded": []}
}
```

- [ ] **Step 2: Write the failing tests**

Create `start_json_test.go`:

```go
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// jsonStartFixture is an omlx with A loaded and B on disk, in a pool whose
// ceiling decides whether B fits beside A.
func jsonStartFixture(t *testing.T, ceiling int64) *config.Config {
	t.Helper()
	cfg := &config.Config{Providers: []config.Provider{{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://127.0.0.1:1"}}}}
	snap := localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "omlx", ModelID: "omlx/A", ModelName: "A", Artifact: "A", Running: true, ArtifactKnown: true},
			{ProviderID: "omlx", ModelID: "omlx/B", ModelName: "B", Artifact: "B", ArtifactKnown: true},
		},
		OmlxPool: &localmodels.Pool{Ceiling: ceiling, InUse: 60, SizesKnown: true, Models: []localmodels.PoolModel{
			{ID: "A", Loaded: true, Size: 60}, {ID: "B", Size: 60},
		}},
	}
	old := probeInventory
	probeInventory = func(*config.Config) localmodels.Snapshot { return snap }
	oldCounts := sessionCounts
	sessionCounts = func(ids []string) map[string]int { return map[string]int{"omlx/A": 1} }
	t.Cleanup(func() { probeInventory, sessionCounts = old, oldCounts })
	return cfg
}

func fixtureShape(t *testing.T, key string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("../../../docs/contracts/wt-start-cli.sample.json")
	if err != nil {
		t.Fatal(err)
	}
	// The fixture's "_comment" is a string, so decode one key at a time.
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var shape map[string]any
	if err := json.Unmarshal(doc[key], &shape); err != nil {
		t.Fatalf("fixture key %q: %v", key, err)
	}
	return shape
}

// TestStartPlanJSONMatchesTheContract verifies the dry run names what a start
// would unload, with session counts, in exactly the shape the shared fixture
// pins — modelman's confirm dialog is built from it, and it must change
// nothing.
func TestStartPlanJSONMatchesTheContract(t *testing.T) {
	started := stubLifecycleStart(t, nil)
	for _, tc := range []struct {
		ceiling int64
		key     string
	}{{100, "plan_would_unload"}, {1000, "plan_fits"}} {
		var out bytes.Buffer
		if err := runStartJSON(&out, jsonStartFixture(t, tc.ceiling), "omlx/B", true, false); err != nil {
			t.Fatalf("%s: %v", tc.key, err)
		}
		var got map[string]any
		if err := json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatalf("%s: output %q is not JSON: %v", tc.key, out.String(), err)
		}
		if want := fixtureShape(t, tc.key); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %v, want %v", tc.key, got, want)
		}
	}
	if len(*started) != 0 {
		t.Errorf("a plan started a model (%d starts)", len(*started))
	}
}

// TestStartJSONReportsWhatWasUnloaded verifies a JSON start with --replace
// reports the models the start unloaded. modelman clears their running flags
// from this; without it they would read as running forever.
func TestStartJSONReportsWhatWasUnloaded(t *testing.T) {
	cfg := jsonStartFixture(t, 100)
	old := lifecycleStart
	lifecycleStart = func(_ context.Context, _ *config.Config, _ lifecycle.Target, opts lifecycle.Options) error {
		if !opts.AllowReplace {
			t.Error("--replace did not reach the engine")
		}
		opts.OnUnloaded(localmodels.Entry{ModelID: "omlx/A"})
		return nil
	}
	t.Cleanup(func() { lifecycleStart = old })
	var out bytes.Buffer
	if err := runStartJSON(&out, cfg, "omlx/B", false, true); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.Unmarshal(out.Bytes(), &got)
	if want := fixtureShape(t, "started"); !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// TestStartJSONRefusesToEvictWithoutReplace verifies a scripted start never
// evicts on its own: without --replace it prints the plan, exits non-zero and
// starts nothing, so a caller can ask its user first.
func TestStartJSONRefusesToEvictWithoutReplace(t *testing.T) {
	started := stubLifecycleStart(t, nil)
	var out bytes.Buffer
	err := runStartJSON(&out, jsonStartFixture(t, 100), "omlx/B", false, false)
	if err == nil || len(*started) != 0 {
		t.Fatalf("err = %v, starts = %d; want a refusal and no start", err, len(*started))
	}
	var got map[string]any
	_ = json.Unmarshal(out.Bytes(), &got)
	if got["status"] != "would_unload" {
		t.Errorf("stdout = %q, want the plan", out.String())
	}
}
```

If `stubLifecycleStart(t, nil)` does not return a usable call log for an empty outcome list, read its definition at `start_test.go:31` and pass the outcomes it needs.

- [ ] **Step 3: Run them and confirm they fail**

Run: `go test ./cmd/wt -run 'TestStartPlanJSON|TestStartJSON'`
Expected: build failure, `undefined: runStartJSON`.

- [ ] **Step 4: Implement `start_json.go`**

```go
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/refcount"
)

// sessionCounts is the number of live wt sessions using each model id. A test
// seam; production sweeps dead sessions first, as the stop picker does.
var sessionCounts = func(ids []string) map[string]int {
	store := refcount.NewStore()
	_ = store.Sweep()
	return store.Counts(ids)
}

type startPlanUnload struct {
	ID       string `json:"id"`
	Sessions int    `json:"sessions"`
}

// startPlanJSON is `wt start <id> --plan --json`: what a start would do,
// decided from one inventory snapshot, with nothing changed.
type startPlanJSON struct {
	ID          string            `json:"id"`
	Status      string            `json:"status"` // running | fits | would_unload | unknown
	WouldUnload []startPlanUnload `json:"would_unload"`
}

// startResultJSON is `wt start <id> --json` after a start.
type startResultJSON struct {
	ID       string   `json:"id"`
	Status   string   `json:"status"` // started | already_running
	Unloaded []string `json:"unloaded"`
}

// runStartJSON implements `wt start <id> --json`, the form modelman drives. It
// never prompts: with plan it reports and changes nothing; without replace a
// start that would displace a running model prints the plan and fails; with
// replace it starts and reports what was unloaded. Both shapes are pinned by
// docs/contracts/wt-start-cli.sample.json.
func runStartJSON(out io.Writer, cfg *config.Config, id string, plan, replace bool) error {
	if id == "" {
		return errors.New("wt start --json needs a model id")
	}
	rows, snap := localRowsSnap(cfg)
	row, ok := catalog.Find(rows, id)
	if !ok {
		if reason := catalog.MissingReason(&snap, id); reason != "" {
			return errors.New(reason)
		}
		return fmt.Errorf("unknown model %q", id)
	}
	emit := func(v any) error { return json.NewEncoder(out).Encode(v) }
	switch row.Action() {
	case catalog.ActionLaunch:
		if plan {
			return emit(startPlanJSON{ID: id, Status: "running", WouldUnload: []startPlanUnload{}})
		}
		ensureRouteBeforeLaunch(cfg, row.Model)
		return emit(startResultJSON{ID: id, Status: "already_running", Unloaded: []string{}})
	case catalog.ActionBlock:
		return errors.New(row.BlockReason())
	}

	target := lifecycle.Target{ProviderID: row.Model.ProviderID, ModelName: row.Model.ModelName, ModelID: row.Model.ID}
	victims, known := lifecycle.Evictions(target, snap)
	p := startPlanJSON{ID: id, Status: "fits", WouldUnload: []startPlanUnload{}}
	switch {
	case !known:
		p.Status = "unknown"
	case len(victims) > 0:
		p.Status = "would_unload"
		ids := make([]string, len(victims))
		for i, v := range victims {
			ids[i] = v.ModelID
		}
		counts := sessionCounts(ids)
		for _, vid := range ids {
			p.WouldUnload = append(p.WouldUnload, startPlanUnload{ID: vid, Sessions: counts[vid]})
		}
	}
	if plan {
		return emit(p)
	}
	if p.Status != "fits" && !replace {
		if err := emit(p); err != nil {
			return err
		}
		if p.Status == "unknown" {
			return fmt.Errorf("cannot tell what starting %s would unload — rerun with --replace to confirm", id)
		}
		names := make([]string, len(p.WouldUnload))
		for i, u := range p.WouldUnload {
			names[i] = u.ID
		}
		return fmt.Errorf("starting %s would stop %s — rerun with --replace to confirm", id, strings.Join(names, ", "))
	}

	ctx, cancel := startSignalCtx()
	defer cancel()
	res := startResultJSON{ID: id, Status: "started", Unloaded: []string{}}
	opts := lifecycle.Options{
		AllowReplace: replace,
		OnUnloaded:   func(en localmodels.Entry) { res.Unloaded = append(res.Unloaded, en.ModelID) },
	}
	if err := lifecycleStart(ctx, cfg, target, opts); err != nil {
		return startFailure(id, err)
	}
	waitPendingRoutes()
	return emit(res)
}
```

- [ ] **Step 5: Wire the flags**

In `model_cmds.go`, `startCmd`: declare `var asJSON, plan bool` before the command, and in `RunE` after reading `replace`:

```go
			if plan && !asJSON {
				return errors.New("--plan needs --json")
			}
			if asJSON {
				return runStartJSON(cmd.OutOrStdout(), a.cfg, id, plan, replace)
			}
```

Before `return cmd` (the command must be assigned to `cmd` first, as `stopCmd` does):

```go
	cmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable output; never prompts")
	cmd.Flags().BoolVar(&plan, "plan", false, "with --json: report what a start would unload, and change nothing")
```

Replace the last paragraph of `Long` with:

```
"On a provider that serves one model (mtplx), starting another replaces it.\n" +
"On omlx a model loads beside the ones already loaded; when it does not fit,\n" +
"omlx unloads the least recently used. wt asks before either; --replace skips\n" +
"the question."
```

Set `SilenceUsage: true` on the command if it is not already, so a refused JSON start prints no usage text.

- [ ] **Step 6: Show session counts in the interactive prompt**

In `cmd/wt/start.go`, in the `errors.As(err, &occ)` case from Task 3, replace `names := strings.Join(occ.IDs(), ", ")` with:

```go
		counts := sessionCounts(occ.IDs())
		parts := make([]string, len(occ.Occupants))
		for i, oid := range occ.IDs() {
			parts[i] = oid
			if n := counts[oid]; n > 0 {
				parts[i] = fmt.Sprintf("%s (in use by %d wt session(s))", oid, n)
			}
		}
		names := strings.Join(parts, ", ")
```

`cmd/wt`'s `TestMain` must stub `sessionCounts` to return an empty map (add it beside the other stubs it installs), so no test reads the developer's refcount file.

- [ ] **Step 7: Run the package**

Run: `go test ./cmd/wt`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add cmd/wt ../docs/contracts/wt-start-cli.sample.json
git commit -m "feat(wt): wt start --plan and --json for scripted callers (#213)"
```

---

### Task 7: Docs

**Files:**
- Modify: `wt/docs/internals/local-models.md`, `wt/docs/wt-start-stop.md`, `wt/CLAUDE.md`, `wt/CHANGELOG.md`, `CLAUDE.md` (root)

- [ ] **Step 1: `wt/docs/internals/local-models.md`**

- In the "omlx's `/v1/models` is not what is running" paragraph: describe `localmodels.OmlxPool` as the one reading (status first, with the registry key; `/health` and the list only when status does not answer; `SizesKnown` false then). Delete the sentence beginning "Not changed: wt still treats omlx as single-model".
- In **Lifecycle**: backends line becomes "ollama (`Shared`), omlx (`Pool`: `omlx start` if down, then `POST /v1/models/{id}/load`; a model stop is `/unload`), mtplx (`Exclusive`)". Add a bullet **Tenancy and the eviction plan** covering `TenancyOf`, `Evictions`, `poolAdmissionMarginPct` and why the margin exists (omlx's unexposed soft watermark), and `reconcilePool` running on failure too.
- In the **Route hook** bullet: an `Exclusive` start removes the family; a `Pool` start adds one id and removes only evicted ids; `routeRemoveModel` versus `routeRemoveFamily`.
- In **Start/stop**: `withFamilyCollateral` applies to `Exclusive` (mtplx) only; `wt stop omlx` calls `lifecycle.Stop`; `wt start --plan/--json` and the contract fixture.

- [ ] **Step 2: `wt/docs/wt-start-stop.md`**

Add a section "omlx: a pool of loaded models" stating: a start loads beside; when it does not fit wt names what omlx is expected to unload and asks; omlx can still unload a model wt did not name, and wt prints it; `wt stop <model>` unloads one model; `wt stop omlx` stops the service; an agent that dials omlx directly can make omlx load and evict with no prompt. Document `--plan` and `--json` with the two JSON examples from the fixture.

- [ ] **Step 3: `wt/CLAUDE.md` and root `CLAUDE.md`**

- `wt/CLAUDE.md`, Lifecycle section: replace "A replace needs `AllowReplace`" bullet's text with one that names both cases (an Exclusive occupant, a Pool eviction). Add `wt start <id> --plan --json` to the verification command list.
- Root `CLAUDE.md`, "Stop mechanisms per backend": change the oMLX entry to "oMLX `wt stop <model>` unloads one model (`POST /v1/models/{id}/unload`); `omlx stop` / `wt stop omlx` halts the service".

- [ ] **Step 4: `wt/CHANGELOG.md`**

Under `## Unreleased`, add a `### Changed` entry:

```markdown
- omlx is handled as the multi-model pool it is (#213). `wt start` loads an
  omlx model beside the ones already loaded instead of stopping the service
  first, and asks only when the model does not fit, naming what omlx is
  expected to unload. `wt stop <model>` unloads that model and leaves the
  others up; `wt stop omlx` stops the service. Routes follow each model's
  loaded state, so two loaded omlx models are both routed. `wt start --json`
  and `--plan` give scripted callers the plan and the result.
```

- [ ] **Step 5: Check and commit**

Run from the monorepo root: `make lint`
Expected: `ALL LINKS OK` and shell lint passing.

```bash
git add -A
git commit -m "docs(wt): omlx pool behavior, tenancy, wt start --json (#213)"
```

---

### Task 8: Verify against a real omlx

This task needs the user: a second omlx model on disk, and approval for any download.

- [ ] **Step 1: Full local verification**

Run from the monorepo root: `make test-all`
Expected: PASS.

- [ ] **Step 2: Build the branch binary**

Run from `wt/`: `go build -o /tmp/wt-verify ./cmd/wt`

- [ ] **Step 3: Ask the user for a second omlx model**

Ask which small model to place in `~/.omlx/models` (under 1 GB is enough) and wait for the answer. Do not download without it.

- [ ] **Step 4: Check the fits path against a scratch registry**

With a scratch `XDG_CONFIG_HOME` holding a copy of the registry, and `WT_LITELLM_CONFIG` pointing at a scratch copy of `config.yaml` (both are mandatory: a redirected registry without `WT_LITELLM_CONFIG` is refused, and the real file must not be touched):

```bash
/tmp/wt-verify start omlx/<first>
/tmp/wt-verify start omlx/<second> --plan --json   # expect "fits"
/tmp/wt-verify start omlx/<second>                 # expect no prompt
/tmp/wt-verify served omlx                         # expect both
/tmp/wt-verify litellm list                        # expect both routed
/tmp/wt-verify stop omlx/<second>
/tmp/wt-verify served omlx                         # expect the first only
/tmp/wt-verify litellm list                        # expect the first only
```

Record the output of each.

- [ ] **Step 5: Check the eviction path if omlx allows a low ceiling**

Look in omlx's settings for the memory limit (`omlx --help`, the admin settings file). If it can be set below the two models' combined size for one run, restart omlx with it and repeat Step 4's first three commands: `--plan --json` must report `would_unload`, the start must ask, and after `--replace` the first model must be unrouted with the "omlx unloaded" line printed. Restore the setting afterwards. If the ceiling cannot be lowered, say in the PR description that the eviction path is covered by the fake-server tests only.

- [ ] **Step 6: Capture the replace screen**

Drive the wt picker through the pty driver at 80x24 and capture the replace-confirm screen for a start that would evict, to confirm the occupant list fits the terminal.

- [ ] **Step 7: Report**

Summarise for the user which success criteria (spec, 1–4 and 6) were verified live, which by tests only, and anything that failed.
