# wt lifecycle engine — code-review fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the seven defects a high-effort `/code-review` found in the lifecycle engine (plan `2026-09-20-wt-lifecycle-engine.md`, items #1–#6), so that no start can silently evict a model the user is running, and every failure is either correct or loud.

**Architecture:** One root cause — the probe layer cannot say "I could not tell" — plus one duplicated fact (the mtplx port) and four smaller correctness/hygiene defects. The fix restores the tri-state that `ollama`'s `/api/ps` path already models: probe failure becomes `StatusPartial`, and the lifecycle adds a bounded live re-probe that fails closed only when the server genuinely refuses to answer.

**Tech Stack:** Go 1.26 (module root `wt/`), stdlib `net/http`, `net/url`, `os`, `io`, `httptest`, `testing`.

**Spec:** `docs/superpowers/specs/2026-09-20-wt-lifecycle-engine-design.md` (the engine being fixed). The findings themselves are the review recorded in this conversation; each task cites its finding.

## Global Constraints

- Wt writes nothing to modelman-owned state (`modelman.toml` flags, LiteLLM config) and never restarts the LiteLLM proxy.
- `Start` never replaces silently: with a *known* occupant and `Options.AllowReplace == false` it returns `*OccupiedError` before touching anything. Where the live state cannot be determined, it returns the new `*OccupancyUnknownError` instead — also before touching anything.
- **`Start` must never require confirmation for an ordinary cold start.** A provider that is definitively *down* (connection refused/reset, no listener) has no occupant and must start normally without `AllowReplace`. Only a *stalled* listener (accepted the connection, then failed to answer) is indeterminate. This distinction is the whole point of Task 1 and Task 4 — do not collapse it.
- Occupancy remains: none for ollama (multi-tenant); `omlx` and `omlx-6bit` are one domain; mtplx = any running mtplx model other than the target. Unsupported providers get `*UnsupportedError`.
- Ports/origins come from `localmodels.FamilyOrigin` (registry `auth.base_url`, else the family default). After Task 6 the numeric port passed to a command line comes from `localmodels.FamilyOriginPort`, so origin and port can never disagree.
- Ollama: daemon must answer `GET <origin>/api/tags`, else `*DaemonDownError`; wt does NOT kickstart the ollama daemon.
- Timeouts (unchanged): warmup 600s, model-load wait 300s, post-stop port-close wait 6s, port-up wait 90s, pre-bind wait 10s.
- Every `Test*` needs a top-level `//` comment stating what it tests and why it matters (repo rule, `wt/CLAUDE.md`).
- Run Go commands from `wt/`. Run `go vet ./...` and `gofmt -l .` before each commit (wt-ci gates on gofmt).
- Commit messages during execution end with `- completes plan item #N` and the `Co-Authored-By:` trailer.
- Continue on the existing `wt-lifecycle-engine` worktree. Never put the default branch in a linked worktree. Do not push or open a PR without asking.

## File Structure

| File | Responsibility |
|---|---|
| `wt/internal/localmodels/match.go` | Modify: add `FetchModelIDsErr`; `FetchModelIDs` becomes its wrapper |
| `wt/internal/localmodels/inventory.go` | Modify: omlx/mtplx probe failure → `StatusPartial`; add `FamilyOriginPort` |
| `wt/internal/localmodels/match_test.go`, `inventory_test.go` | Modify: tests for the above |
| `wt/internal/lifecycle/lifecycle.go` | Modify: `backendsByFamily` registry; `Occupant` reads it; `stop` seam; indeterminate-occupancy decision; `OccupancyUnknownError` |
| `wt/internal/lifecycle/probe.go` | Modify: `liveServed`; `tryChat` requires 2xx |
| `wt/internal/lifecycle/mtplx.go` | Modify: endpoint from `FamilyOriginPort` |
| `wt/internal/lifecycle/pidproc.go` | Modify: `logTail` seeks |
| `wt/internal/lifecycle/*_test.go` | Modify: tests for the above |
| `wt/internal/config/config.go` | Modify: `model_name` required in `validate()` |
| `wt/internal/config/config_test.go` | Modify: test for the above |
| `wt/CLAUDE.md` | Modify: lifecycle + localmodels package descriptions |

---

### Task 1: `localmodels` — the probe must be able to say "I could not tell"

**Finding #1 (root cause).** `FetchModelIDs` returns `nil` for a refused connection, a timeout, a non-2xx status, an undecodable body, *and* a genuinely empty list — its own doc comment admits "nil reads as 'nothing serving', never as 'unknown'". For a single-model family that turns an unanswerable probe into permission to replace a model that may well be serving.

**Files:**
- Modify: `wt/internal/localmodels/match.go`
- Modify: `wt/internal/localmodels/inventory.go:206-207`
- Test: `wt/internal/localmodels/match_test.go`, `wt/internal/localmodels/inventory_test.go`

**Interfaces:**
- Consumes: existing `FetchModelIDs` callers (`localgate`, `lifecycle/probe.go:110`).
- Produces: `func FetchModelIDsErr(client *http.Client, url string) ([]string, error)`; `FetchModelIDs` keeps its signature and nil-on-error behaviour. Also produced for Task 4: the omlx/mtplx families now report `localmodels.StatusPartial` when `/v1/models` fails, so `Snapshot.Providers[family]` distinguishes a trustworthy probe from an untrustworthy one.

- [ ] **Step 1: Write the failing tests**

Append to `match_test.go`:

```go
// TestFetchModelIDsErrDistinguishesEmptyFromFailure verifies the probe keeps
// "the server answered and is serving nothing" apart from "the server did not
// give a usable answer". The lifecycle engine starts normally on the first and
// fails closed on the second, so collapsing them either refuses every cold
// start or silently replaces a model that is still running.
func TestFetchModelIDsErrDistinguishesEmptyFromFailure(t *testing.T) {
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer empty.Close()
	ids, err := FetchModelIDsErr(empty.Client(), empty.URL)
	if err != nil {
		t.Errorf("server answered with an empty list: err = %v, want nil", err)
	}
	if len(ids) != 0 {
		t.Errorf("server answered with an empty list: ids = %v, want empty", ids)
	}

	serving := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"a"},{"id":"b"}]}`))
	}))
	defer serving.Close()
	if ids, err := FetchModelIDsErr(serving.Client(), serving.URL); err != nil || len(ids) != 2 {
		t.Errorf("serving server: ids=%v err=%v, want 2 ids and no error", ids, err)
	}

	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer broken.Close()
	if _, err := FetchModelIDsErr(broken.Client(), broken.URL); err == nil {
		t.Error("a non-2xx answer must be an error, not an empty model list")
	}
	if _, err := FetchModelIDsErr(broken.Client(), "http://127.0.0.1:1/v1/models"); err == nil {
		t.Error("a refused connection must be an error, not an empty model list")
	}
}
```

Append to `inventory_test.go` (reusing that file's `localProvider`, `mkdirs`, `modelsServer` helpers):

```go
// TestInventoryOmlxProbeFailureIsPartial verifies an unanswerable /v1/models
// leaves the omlx family StatusPartial rather than StatusOK-with-no-models.
// The lifecycle engine reads that status to decide whether Running can be
// trusted; reporting OK would let it treat a stalled daemon as empty and
// replace the model that daemon is serving.
func TestInventoryOmlxProbeFailureIsPartial(t *testing.T) {
	dir := t.TempDir()
	mkdirs(t, dir, "some-model")
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer down.Close()
	cfg := &config.Config{
		Providers: []config.Provider{localProvider("omlx", down.URL, dir)},
		Models:    []config.Model{{ID: "omlx/some-model", ProviderID: "omlx", ModelName: "some-model"}},
	}
	snap := inventory(cfg, testClient)
	if got := snap.Providers["omlx"]; got != StatusPartial {
		t.Errorf("omlx status with an unusable /v1/models = %q, want %q", got, StatusPartial)
	}
	// The model-dir scan still succeeded, so artifacts remain trustworthy.
	if en, ok := byModelID(snap, "omlx/some-model"); !ok || !en.ArtifactKnown {
		t.Errorf("probe failure must not make artifact discovery untrustworthy: %+v ok=%v", en, ok)
	}
}

// TestInventoryOmlxEmptyAnswerIsOK verifies a server that answers with an
// empty model list is StatusOK, not StatusPartial. That case is "nothing is
// loaded", which must start normally without a confirmation prompt.
func TestInventoryOmlxEmptyAnswerIsOK(t *testing.T) {
	dir := t.TempDir()
	mkdirs(t, dir, "some-model")
	up := modelsServer(t) // answers {"data":[]}
	defer up.Close()
	cfg := &config.Config{
		Providers: []config.Provider{localProvider("omlx", up.URL, dir)},
		Models:    []config.Model{{ID: "omlx/some-model", ProviderID: "omlx", ModelName: "some-model"}},
	}
	if got := inventory(cfg, testClient).Providers["omlx"]; got != StatusOK {
		t.Errorf("omlx status with an empty model list = %q, want %q", got, StatusOK)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd wt && go test ./internal/localmodels -run 'TestFetchModelIDsErr|TestInventoryOmlxProbe' -v`
Expected: FAIL — compile error `undefined: FetchModelIDsErr`; after that is added, `TestInventoryOmlxProbeFailureIsPartial` fails with `omlx status ... = "ok", want "partial"`.

- [ ] **Step 3: Add `FetchModelIDsErr` and make `FetchModelIDs` its wrapper**

In `match.go`, replace the whole `FetchModelIDs` function (its comment is now wrong — delete it) with:

```go
// FetchModelIDs GETs an OpenAI-compatible /v1/models endpoint and returns the
// ids the server is serving, or nil on any failure. Because nil cannot
// distinguish "nothing is serving" from "could not ask", callers that need
// that distinction must use FetchModelIDsErr instead.
func FetchModelIDs(client *http.Client, url string) []string {
	ids, err := FetchModelIDsErr(client, url)
	if err != nil {
		return nil
	}
	return ids
}

// FetchModelIDsErr is FetchModelIDs with the failure mode preserved. A non-nil
// error means the server gave no usable answer (refused connection, timeout,
// non-2xx, undecodable body), so an empty id list must not be read as "nothing
// is serving". A nil error with zero ids means the server answered and is
// serving nothing.
func FetchModelIDsErr(client *http.Client, url string) ([]string, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	ids := make([]string, 0, len(body.Data))
	for _, d := range body.Data {
		if d.ID != "" {
			ids = append(ids, d.ID)
		}
	}
	return ids, nil
}
```

Add `"fmt"` to `match.go`'s imports.

- [ ] **Step 4: Make omlx/mtplx report the failed probe**

In `inventory.go`, replace line 207 (`s.loaded = FetchModelIDs(client, origin+"/v1/models")`) with:

```go
		// A failed /v1/models must not read as "nothing is loaded": for a
		// single-model family that turns an unanswerable probe into permission to
		// replace a model that may well be serving. StatusPartial records the same
		// "Running flags are not trustworthy" state ollama already uses for a
		// failed /api/ps.
		loaded, err := FetchModelIDsErr(client, origin+"/v1/models")
		if err != nil {
			s.status = StatusPartial
		}
		s.loaded = loaded
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd wt && go test ./internal/localmodels -v`
Expected: PASS, including the two new tests.

- [ ] **Step 6: Check the blast radius on existing consumers**

Run: `cd wt && go test ./internal/localgate ./internal/tui ./internal/smoke ./cmd/wt`
Expected: PASS. `localgate.ResolveAll` probes `/v1/models` directly (not via `Snapshot.Providers`), and the TUI reads `ArtifactKnown`/`Running`, both unchanged by a `StatusOK`→`StatusPartial` shift. If a test fails here, it is asserting the old conflation — fix the assertion, do not revert the change.

- [ ] **Step 7: Commit**

```bash
git add wt/internal/localmodels/
git commit -m "fix(localmodels): let the probe report failure separately from empty - completes plan item #1"
```

---

### Task 2: `config` — `model_name` is required

**Finding #3 (guard).** `Config.validate` checks id/provider/location but not `model_name`, while modelman's registry loader requires that key. A hand-edited or legacy `registry.toml` missing it yields `Entry.ModelName == ""`, which makes every name match fail — so the lifecycle sees the model as never running and warms `""` for the full 600s budget before failing opaquely.

Verified safe before writing this task: every `[[models]]` block in `docs/contracts/registry.sample.toml` (5/5) and in the config test fixtures carries `model_name`; `convertModels` (`migrate.go:205-244`) sets it on every legacy path; and no existing test asserts that a `model_name`-less model validates. So this adds no fixture churn.

**Files:**
- Modify: `wt/internal/config/config.go:456-470`
- Test: `wt/internal/config/config_test.go`

**Interfaces:**
- Consumes: existing `validate()` error-collection shape.
- Produces: a new `validate()` error, `model %q: model_name is required (run 'modelman sync' to repair the registry)`, surfaced by `Validate()` at `cmd/wt/app.go:27` and by `ValidateAll()` in the config editor.

- [ ] **Step 1: Write the failing test**

Append to `config_test.go`:

```go
// TestValidate_ModelNameRequired verifies a model with an id but no model_name
// is rejected. model_name is the provider-side name the lifecycle engine
// matches against what a provider reports as running; an empty one makes every
// match fail, so a start would warm the empty name for the full 600s budget
// and then fail with no explanation of the real cause.
func TestValidate_ModelNameRequired(t *testing.T) {
	cfg := &Config{
		DefaultTag: "code",
		Providers:  []Provider{{ID: "ollama", Name: "Ollama", Location: "local"}},
		Models:     []Model{{ID: "ollama/qwen3.8:27b-mlx", ProviderID: "ollama"}},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected an error for a model with no model_name")
	}
	if !strings.Contains(err.Error(), "model_name") {
		t.Errorf("Validate() = %v, want an error naming model_name", err)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd wt && go test ./internal/config -run TestValidate_ModelNameRequired -v`
Expected: FAIL — `expected an error for a model with no model_name`.

- [ ] **Step 3: Add the check to `validate()`**

In `config.go`, inside the `// Models` loop, after the `modelIDs[m.ID] = true` line and before the `if !provIDs[m.ProviderID]` check:

```go
		if m.ModelName == "" {
			// The lifecycle engine matches a start target against a provider's
			// reported names by this field; an empty one matches nothing, so the
			// model would look permanently stopped and warm the empty name.
			errs = append(errs, fmt.Errorf("model %q: model_name is required (run 'modelman sync' to repair the registry)", m.ID))
		}
```

- [ ] **Step 4: Run the package and the whole tree**

Run: `cd wt && go test ./internal/config -v && go test ./...`
Expected: PASS. `TestValidateAll_CollectsMultipleErrors` (config_test.go:363) asserts with `strings.Contains` per message and so tolerates the extra error; `TestLitellmEnabledMissingURLFailsAtLaunchNotValidate` (config_test.go:957) declares no models.

- [ ] **Step 5: Commit**

```bash
git add wt/internal/config/
git commit -m "fix(config): require model_name in registry models - completes plan item #2"
```

---

### Task 3: `lifecycle` — one source for which backends are single-model

**Finding #5.** `Occupant` hardcodes `family != "omlx" && family != "mtplx"` while `start()` consults the authoritative `backend.singleModel()`. Adding a fourth single-model backend (e.g. `mlx_lm_server` in sub-project 4c) and missing the list means `start()` sees `singleModel() == true` but `Occupant` always returns false — the running model is replaced with no `*OccupiedError`, and nothing fails to compile.

**Files:**
- Modify: `wt/internal/lifecycle/lifecycle.go:103-107`, `wt/internal/lifecycle/env.go:43`
- Test: `wt/internal/lifecycle/lifecycle_test.go`

**Interfaces:**
- Consumes: existing `backend` interface, `localmodels.Family`.
- Produces: package-level `var backendsByFamily map[string]backend`, the single source `defaultEnv()` fills `env.backends` from. `Occupant` reads it for its single-model rule.

- [ ] **Step 1: Write the failing test**

Append to `lifecycle_test.go`:

```go
// TestOccupantDerivesSingleModelFromBackendRegistry verifies Occupant's
// single-model rule comes from the same registry defaultEnv builds, for every
// registered family. A backend that is startable (singleModel true) while
// Occupant reports no occupant is exactly the state in which a running model
// gets replaced with no confirmation — the trap a hand-maintained family list
// leaves for the next backend.
func TestOccupantDerivesSingleModelFromBackendRegistry(t *testing.T) {
	if len(defaultEnv().backends) != len(backendsByFamily) {
		t.Fatalf("defaultEnv registers %d backends, backendsByFamily has %d — they must not drift",
			len(defaultEnv().backends), len(backendsByFamily))
	}
	for family, b := range backendsByFamily {
		snap := localmodels.Snapshot{Entries: []localmodels.Entry{running(family, family+"/occupant", "occupant")}}
		_, ok := Occupant(Target{ProviderID: family, ModelName: "wanted"}, snap)
		if ok != b.singleModel() {
			t.Errorf("family %s: Occupant reported an occupant=%v but singleModel()=%v", family, ok, b.singleModel())
		}
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd wt && go test ./internal/lifecycle -run TestOccupantDerivesSingleModelFromBackendRegistry -v`
Expected: FAIL — compile error `undefined: backendsByFamily`.

- [ ] **Step 3: Add the registry and point both consumers at it**

In `lifecycle.go`, directly above `Occupant`:

```go
// backendsByFamily is the single source of truth for which providers wt can
// start and which of them serve one model at a time. defaultEnv copies it, and
// Occupant reads it (Occupant has no *env). Keeping these two in agreement used
// to be manual — Occupant held its own hardcoded family list — so a new
// single-model backend could be startable while Occupant still reported no
// occupant, replacing a running model without a confirmation.
var backendsByFamily = map[string]backend{
	"ollama": ollamaBackend{},
	"omlx":   omlxBackend{},
	"mtplx":  mtplxBackend{},
}
```

Replace `Occupant`'s opening guard (currently `if family != "omlx" && family != "mtplx" {`) with:

```go
	if b := backendsByFamily[family]; b == nil || !b.singleModel() {
		return localmodels.Entry{}, false
	}
```

In `env.go`, replace the `backends:` line of `defaultEnv` with:

```go
		backends:       maps.Clone(backendsByFamily),
```

and add `"maps"` to `env.go`'s imports.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd wt && go test ./internal/lifecycle -v`
Expected: PASS, including `TestOccupantRules` and `TestDefaultEnvRegistersBackends`.

- [ ] **Step 5: Commit**

```bash
git add wt/internal/lifecycle/
git commit -m "refactor(lifecycle): derive single-model occupancy from the backend registry - completes plan item #3"
```

---

### Task 4: `lifecycle` — an indeterminate probe must not mean "replace it"

**Finding #1 (behaviour).** With the omlx daemon up and serving A but its `/v1/models` slow or failing, every entry reads `Running == false`, so `isRunning` and `Occupant` report no occupant, `Start(..., AllowReplace: false)` returns no error, and `omlxBackend.start` finds the port answering, skips `omlx start`, and warms B into the same daemon — unloading A without confirmation. mtplx has a `PortBusyError` backstop for this; omlx has none.

**Chosen policy (user decision):** re-probe live, then fail closed. **Refined during design:** `!known` means *the listener accepted the connection and then failed to answer*. A refused connection means the provider is definitively down, which is the ordinary cold start and must proceed with no confirmation. `env.probe` (`probe.go:28`) already returns exactly this three-way signal.

**Files:**
- Modify: `wt/internal/lifecycle/probe.go` (add `liveServed`)
- Modify: `wt/internal/lifecycle/lifecycle.go` (`isRunning`, `Occupant` trust guard, `start` decision, new error type)
- Test: `wt/internal/lifecycle/lifecycle_test.go`

**Interfaces:**
- Consumes: `localmodels.Status`/`StatusOK`/`StatusPartial` (Task 1), `env.probe`, `localmodels.FetchModelIDsErr` (Task 1), `env.prebindTimeout`.
- Produces:
  - `func (e *env) liveServed(ctx context.Context, cfg *config.Config, family string) (ids []string, known bool)` — `known == false` only when the listener stalled or answered unusably.
  - `type OccupancyUnknownError struct{ ProviderID, Origin string }` implementing `error`.
  - `func (e *env) resolveOccupant(ctx context.Context, cfg *config.Config, family string, t Target, snap localmodels.Snapshot) (localmodels.Entry, bool, bool)` returning `(occupant, hasOccupant, unknown)`.
  - `func probeTrusted(snap localmodels.Snapshot, family string) bool` — true when the family's probe is `StatusOK` or absent.

- [ ] **Step 1: Write the failing tests**

Append to `lifecycle_test.go`. Add `"net/http"`, `"net/http/httptest"`, `"time"` to that file's imports if absent.

```go
// TestStartRefusesWhenListenerStalls verifies a single-model provider whose
// /v1/models accepts the connection and then stalls produces
// *OccupancyUnknownError and touches nothing. This is the silent-eviction case:
// the daemon is up and serving a model, but the probe cannot see it, so
// starting would replace it with no confirmation.
func TestStartRefusesWhenListenerStalls(t *testing.T) {
	stall := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(400 * time.Millisecond)
	}))
	defer stall.Close()

	var calls []string
	e := fakeEnv(localmodels.Snapshot{Providers: map[string]localmodels.Status{"omlx": localmodels.StatusPartial}}, true, &calls)
	cfg := provCfg("omlx", stall.URL)

	err := start(context.Background(), e, cfg, Target{ProviderID: "omlx", ModelName: "b"}, Options{})
	var unknown *OccupancyUnknownError
	if !errors.As(err, &unknown) {
		t.Fatalf("start with a stalled listener = %v, want *OccupancyUnknownError", err)
	}
	if len(calls) != 0 {
		t.Errorf("start touched the provider (%v) while the occupant was unknown", calls)
	}
}

// TestStartProceedsWhenProviderIsDown verifies a definitively-down provider
// still starts with no confirmation. This is the ordinary cold start for
// omlx/mtplx: making a refused connection count as indeterminate would demand
// AllowReplace for every normal start, which is worse than the bug.
func TestStartProceedsWhenProviderIsDown(t *testing.T) {
	var calls []string
	e := fakeEnv(localmodels.Snapshot{Providers: map[string]localmodels.Status{"omlx": localmodels.StatusPartial}}, true, &calls)
	cfg := provCfg("omlx", "http://"+freeAddr(t))

	if err := start(context.Background(), e, cfg, Target{ProviderID: "omlx", ModelName: "b"}, Options{}); err != nil {
		t.Fatalf("start with the provider down = %v, want nil", err)
	}
	if !reflect.DeepEqual(calls, []string{"start:b"}) {
		t.Errorf("calls = %v, want [start:b]", calls)
	}
}

// TestStartFindsOccupantTheSnapshotMissed verifies the live re-probe catches a
// model the inventory snapshot could not report, and returns the ordinary
// *OccupiedError for it. Falling through to a start here is the eviction.
func TestStartFindsOccupantTheSnapshotMissed(t *testing.T) {
	serving := modelsServing(t, "a")
	defer serving.Close()

	var calls []string
	e := fakeEnv(localmodels.Snapshot{Providers: map[string]localmodels.Status{"omlx": localmodels.StatusPartial}}, true, &calls)
	cfg := provCfg("omlx", serving.URL)

	err := start(context.Background(), e, cfg, Target{ProviderID: "omlx", ModelName: "b"}, Options{})
	var occ *OccupiedError
	if !errors.As(err, &occ) {
		t.Fatalf("start over a live occupant = %v, want *OccupiedError", err)
	}
	if !containsFold(occ.Occupant.ModelID, "a") {
		t.Errorf("occupant = %q, want the model the server reports (a)", occ.Occupant.ModelID)
	}
	if len(calls) != 0 {
		t.Errorf("start touched the provider (%v) without AllowReplace", calls)
	}
}

// TestStartProceedsWhenServerReportsNoModel verifies a server that answers with
// an empty model list is started into normally. It answered, so it is neither
// unknown nor occupied.
func TestStartProceedsWhenServerReportsNoModel(t *testing.T) {
	empty := modelsServing(t)
	defer empty.Close()

	var calls []string
	e := fakeEnv(localmodels.Snapshot{Providers: map[string]localmodels.Status{"omlx": localmodels.StatusPartial}}, true, &calls)
	cfg := provCfg("omlx", empty.URL)

	if err := start(context.Background(), e, cfg, Target{ProviderID: "omlx", ModelName: "b"}, Options{}); err != nil {
		t.Fatalf("start against an idle server = %v, want nil", err)
	}
	if !reflect.DeepEqual(calls, []string{"start:b"}) {
		t.Errorf("calls = %v, want [start:b]", calls)
	}
}

// TestStartUsesSnapshotWhenProbeIsTrustworthy verifies a trustworthy snapshot
// still decides occupancy on its own, so the common path costs no extra HTTP
// request and the existing Occupant rules keep governing.
func TestStartUsesSnapshotWhenProbeIsTrustworthy(t *testing.T) {
	var calls []string
	snap := localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
		Entries:   []localmodels.Entry{running("omlx", "omlx/a", "a")},
	}
	e := fakeEnv(snap, true, &calls)
	cfg := provCfg("omlx", "http://"+freeAddr(t)) // no listener: a live probe must not be consulted

	err := start(context.Background(), e, cfg, Target{ProviderID: "omlx", ModelName: "b"}, Options{})
	var occ *OccupiedError
	if !errors.As(err, &occ) {
		t.Fatalf("start with a trustworthy snapshot showing an occupant = %v, want *OccupiedError", err)
	}
	if len(calls) != 0 {
		t.Errorf("start touched the provider (%v) without AllowReplace", calls)
	}
}

// TestOccupantIgnoresUntrustworthySnapshot verifies Occupant does not claim
// "no occupant" from a probe it knows failed. Its contract is that the caller
// may act on a false result, so it must not answer from untrustworthy data.
func TestOccupantIgnoresUntrustworthySnapshot(t *testing.T) {
	snap := localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusPartial},
		Entries:   []localmodels.Entry{running("omlx", "omlx/a", "a")},
	}
	if _, ok := Occupant(Target{ProviderID: "omlx", ModelName: "b"}, snap); ok {
		t.Error("Occupant must not report an occupant from an untrustworthy probe")
	}
}
```

Add this helper next to the other test helpers in `lifecycle_test.go`:

```go
// modelsServing is an OpenAI-compatible /v1/models server reporting the given
// ids (none when called with no arguments).
func modelsServing(t *testing.T, ids ...string) *httptest.Server {
	t.Helper()
	body := `{"data":[`
	for i, id := range ids {
		if i > 0 {
			body += ","
		}
		body += `{"id":"` + id + `"}`
	}
	body += `]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	return srv
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd wt && go test ./internal/lifecycle -run 'TestStartRefusesWhenListenerStalls|TestStartProceedsWhenProviderIsDown|TestStartFindsOccupantTheSnapshotMissed|TestStartProceedsWhenServerReportsNoModel|TestStartUsesSnapshotWhenProbeIsTrustworthy|TestOccupantIgnoresUntrustworthySnapshot' -v`
Expected: FAIL — compile errors `undefined: OccupancyUnknownError`, `undefined: modelsServing`.

- [ ] **Step 3: Add `liveServed` to `probe.go`**

Append to `probe.go`:

```go
// liveServed asks a single-model provider's server directly what it is serving,
// and reports whether that answer can be trusted.
//
// known is false only when the server failed to give a usable answer — it
// accepted the connection and stalled, or answered with a non-2xx or
// undecodable body. A refused or reset connection is NOT unknown: it means
// nothing is listening, so known is true with no ids, and an ordinary cold
// start proceeds without a confirmation. Collapsing those two cases either
// refuses every cold start or permits the silent replacement this exists to
// prevent.
func (e *env) liveServed(ctx context.Context, cfg *config.Config, family string) (ids []string, known bool) {
	origin, _ := localmodels.FamilyOrigin(cfg, family)
	modelsURL := origin + "/v1/models"
	responded, timedOut := e.probe(ctx, modelsURL, e.prebindTimeout)
	switch {
	case responded:
		ids, err := localmodels.FetchModelIDsErr(e.probeClient, modelsURL)
		if err != nil {
			return nil, false
		}
		return ids, true
	case timedOut:
		return nil, false
	default:
		// Connection refused/reset: definitively nothing listening.
		return nil, true
	}
}
```

- [ ] **Step 4: Add the trust guard, the error type and the decision to `lifecycle.go`**

Add the error type after `PortBusyError`:

```go
// OccupancyUnknownError means the provider's live state could not be
// determined: its server accepted a connection but did not give a usable
// answer, so starting could replace a model that is still running. Nothing was
// touched.
type OccupancyUnknownError struct{ ProviderID, Origin string }

func (e *OccupancyUnknownError) Error() string {
	return fmt.Sprintf("cannot tell whether %s at %s is already serving a model — confirm before replacing it", e.ProviderID, e.Origin)
}
```

Add `probeTrusted` next to `isRunning`:

```go
// probeTrusted reports whether snap's Running flags for family were produced by
// a probe that could actually determine them. An absent status counts as
// trusted: StatusUnsupported (mlx_lm_server) is not a single-model family, and
// hand-built snapshots in tests carry no status map.
func probeTrusted(snap localmodels.Snapshot, family string) bool {
	st, ok := snap.Providers[family]
	if !ok {
		return true
	}
	return st == localmodels.StatusOK
}
```

Change `isRunning` so it cannot claim "running" from a probe that failed:

```go
func isRunning(snap localmodels.Snapshot, family string, t Target) bool {
	if !probeTrusted(snap, family) {
		return false
	}
	for _, en := range snap.Entries {
		...unchanged...
	}
}
```

Change `Occupant` so it cannot claim "no occupant" from a probe that failed. Its guard becomes:

```go
	family := localmodels.Family(t.ProviderID)
	if b := backendsByFamily[family]; b == nil || !b.singleModel() {
		return localmodels.Entry{}, false
	}
	if !probeTrusted(snap, family) {
		return localmodels.Entry{}, false
	}
```

Add `resolveOccupant` after `Occupant`:

```go
// resolveOccupant decides whether starting t would replace a running model of a
// single-model family. It prefers the snapshot, which is free, and re-probes
// the server only when the snapshot's probe could not be trusted — the case
// that used to read as "no occupant" and let a start evict the running model.
// unknown reports that even the re-probe could not tell; the caller must then
// require AllowReplace.
func (e *env) resolveOccupant(ctx context.Context, cfg *config.Config, family string, t Target, snap localmodels.Snapshot) (occ localmodels.Entry, has, unknown bool) {
	if b := e.backends[family]; b == nil || !b.singleModel() {
		return localmodels.Entry{}, false, false
	}
	if occ, ok := Occupant(t, snap); ok {
		return occ, true, false
	}
	if probeTrusted(snap, family) {
		return localmodels.Entry{}, false, false
	}
	ids, known := e.liveServed(ctx, cfg, family)
	if !known {
		return localmodels.Entry{}, false, true
	}
	for _, id := range ids {
		if sameModel(family, id, t.ModelName) {
			continue // the target itself is already served: not an occupant
		}
		return localmodels.Entry{ProviderID: t.ProviderID, ModelID: id, ModelName: id, Running: true}, true, false
	}
	return localmodels.Entry{}, false, false
}
```

Replace the occupancy block in `start` (currently lines 143-153) with:

```go
	if occ, has, unknown := e.resolveOccupant(ctx, cfg, family, t, snap); unknown {
		if !opts.AllowReplace {
			origin, _ := localmodels.FamilyOrigin(cfg, family)
			return &OccupancyUnknownError{ProviderID: t.ProviderID, Origin: origin}
		}
	} else if has {
		if !opts.AllowReplace {
			return &OccupiedError{Occupant: occ}
		}
		report(StageStoppingOccupant)
		if err := b.stop(ctx, e, cfg); err != nil {
			return fmt.Errorf("stopping %s before starting %s: %w", occ.ModelID, t.ModelName, err)
		}
	}
```

Note this drops the outer `if b.singleModel()` wrapper: `resolveOccupant` already returns `has=false, unknown=false` for multi-tenant families, so the behaviour for ollama is unchanged and the gate now lives in exactly one place.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd wt && go test ./internal/lifecycle -v`
Expected: PASS, including all six new tests and the existing `TestOccupantRules` / `TestStartNeverReplacesSilently` / `TestStartAlreadyRunningIsNoOp`.

- [ ] **Step 6: Commit**

```bash
git add wt/internal/lifecycle/
git commit -m "fix(lifecycle): fail closed when the live occupant cannot be determined - completes plan item #4"
```

---

### Task 5: `lifecycle` — give `Stop` the same seam as `Start`

**Finding #7.** `Stop` builds `defaultEnv()` inline, so the only exported function that stops a user's running model is unreachable from a test. Sub-projects 4b/4c depend on it for replacement, and a regression (a provider mapping to no backend, or `b.stop` reporting success while the model stays loaded) would ship with no failing test. `start` already has exactly the shape needed.

**Files:**
- Modify: `wt/internal/lifecycle/lifecycle.go:157-166`
- Test: `wt/internal/lifecycle/lifecycle_test.go`

**Interfaces:**
- Consumes: `fakeEnv`, `fakeBackend` (existing test helpers).
- Produces: `func stop(ctx context.Context, e *env, cfg *config.Config, providerID string) error`; `Stop` delegates to it.

- [ ] **Step 1: Write the failing test**

Append to `lifecycle_test.go`:

```go
// TestStopUsesInjectedEnv verifies Stop's provider dispatch runs through the
// injectable env, mirroring Start/start. Without this seam the exported stop
// path — which replacement depends on — cannot be tested, so a stop that
// reports success while the model stays loaded would ship unnoticed.
func TestStopUsesInjectedEnv(t *testing.T) {
	var calls []string
	e := fakeEnv(localmodels.Snapshot{}, true, &calls)
	cfg := &config.Config{}

	if err := stop(context.Background(), e, cfg, "omlx"); err != nil {
		t.Fatalf("stop(omlx) = %v, want nil", err)
	}
	if !reflect.DeepEqual(calls, []string{"stop"}) {
		t.Errorf("calls = %v, want [stop]", calls)
	}

	err := stop(context.Background(), e, cfg, "ghost")
	var unsupported *UnsupportedError
	if !errors.As(err, &unsupported) {
		t.Errorf("stop(ghost) = %v, want *UnsupportedError", err)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd wt && go test ./internal/lifecycle -run TestStopUsesInjectedEnv -v`
Expected: FAIL — compile error `undefined: stop`.

- [ ] **Step 3: Split `Stop`**

Replace `Stop` (`lifecycle.go:157-166`) with:

```go
// Stop stops the provider's running model (used for replacement; wt has no
// user-facing stop command). A no-op for multi-tenant ollama.
func Stop(ctx context.Context, cfg *config.Config, providerID string) error {
	return stop(ctx, defaultEnv(), cfg, providerID)
}

// stop is Stop's injectable core, the same shape Start/start uses so tests can
// drive the real dispatch without a live provider.
func stop(ctx context.Context, e *env, cfg *config.Config, providerID string) error {
	b := e.backends[localmodels.Family(providerID)]
	if b == nil {
		return &UnsupportedError{ProviderID: providerID}
	}
	return b.stop(ctx, e, cfg)
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd wt && go test ./internal/lifecycle -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add wt/internal/lifecycle/
git commit -m "refactor(lifecycle): add an env seam to Stop - completes plan item #5"
```

---

### Task 6: `localmodels` + `lifecycle` — one source for the mtplx origin and port

**Finding #2.** `mtplxEndpoint` derives the port from the origin URL with a hardcoded `8003` fallback, then builds `modelsURL` from the origin. A registry `auth.base_url` of `http://localhost` (no port) parses to `u.Port() == ""`, so `serve --port 8003` and `mtplx stop --port 8003` use 8003 while `waitForModel` polls `http://localhost/v1/models` — port 80 — until the full 300s `loadTimeout` expires, then kills the server it just spawned. A stale server already on 8003 is likewise never detected. `localmodels` already owns `defaultMtplxOrigin` (`inventory.go:39`), the constant `localgate`'s comment warns must stay in lockstep.

**Files:**
- Modify: `wt/internal/localmodels/inventory.go` (add `FamilyOriginPort`)
- Modify: `wt/internal/lifecycle/mtplx.go:22-32`
- Test: `wt/internal/localmodels/inventory_test.go`, `wt/internal/lifecycle/mtplx_test.go`

**Interfaces:**
- Consumes: `FamilyOrigin`, `config.BaseOrigin`, `config.OllamaBaseURL`, `defaultOmlxOrigin`, `defaultMtplxOrigin`.
- Produces: `func FamilyOriginPort(cfg *config.Config, family string) (origin string, port int, err error)`. The returned origin always carries that port, so a caller that builds a URL from the origin and a caller that passes the port to a command line cannot disagree.

- [ ] **Step 1: Write the failing tests**

Append to `inventory_test.go`:

```go
// TestFamilyOriginPortAgreesWithOrigin verifies the port returned alongside an
// origin is the port that origin actually addresses, including when a registry
// base_url omits one. A caller passing the port to a command line while the
// origin says something else spawns a server on one port and polls another,
// which shows up as a full-length timeout instead of a failure.
func TestFamilyOriginPortAgreesWithOrigin(t *testing.T) {
	// No port in the registry value: the family default must be applied to the
	// origin too, not just to the returned port.
	bare := &config.Config{Providers: []config.Provider{localProvider("mtplx", "http://localhost", "")}}
	origin, port, err := FamilyOriginPort(bare, "mtplx")
	if err != nil {
		t.Fatalf("FamilyOriginPort: %v", err)
	}
	if origin != "http://localhost:8003" || port != 8003 {
		t.Errorf("bare origin: origin=%q port=%d, want http://localhost:8003 and 8003", origin, port)
	}
	if u, err := url.Parse(origin); err != nil || u.Port() != strconv.Itoa(port) {
		t.Errorf("origin %q does not address the returned port %d", origin, port)
	}

	// An explicit port wins, and is preserved in the origin.
	explicit := &config.Config{Providers: []config.Provider{localProvider("mtplx", "http://127.0.0.1:9123", "")}}
	origin, port, err = FamilyOriginPort(explicit, "mtplx")
	if err != nil {
		t.Fatalf("FamilyOriginPort: %v", err)
	}
	if origin != "http://127.0.0.1:9123" || port != 9123 {
		t.Errorf("explicit origin: origin=%q port=%d, want http://127.0.0.1:9123 and 9123", origin, port)
	}

	// The registry-free default still resolves to the family default.
	if origin, port, err = FamilyOriginPort(&config.Config{}, "omlx"); err != nil || port != 8000 {
		t.Errorf("default omlx: origin=%q port=%d err=%v, want 8000 and no error", origin, port, err)
	}
}
```

Append to `mtplx_test.go`:

```go
// TestMtplxEndpointPortMatchesModelsURL verifies the port mtplx is spawned with
// and the URL it is polled at come from one resolution. When they diverge the
// spawn succeeds and the wait then times out for the full load budget before
// killing the server it just started.
func TestMtplxEndpointPortMatchesModelsURL(t *testing.T) {
	cfg := provCfg("mtplx", "http://localhost") // registry value with no port
	origin, modelsURL, port := mtplxEndpoint(cfg)
	if origin != "http://localhost:8003" {
		t.Errorf("origin = %q, want http://localhost:8003", origin)
	}
	if modelsURL != "http://localhost:8003/v1/models" {
		t.Errorf("modelsURL = %q, want http://localhost:8003/v1/models", modelsURL)
	}
	if port != 8003 {
		t.Errorf("port = %d, want 8003", port)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd wt && go test ./internal/localmodels ./internal/lifecycle -run 'TestFamilyOriginPort|TestMtplxEndpointPortMatches' -v`
Expected: FAIL — `undefined: FamilyOriginPort`, and `TestMtplxEndpointPortMatchesModelsURL` reports `origin = "http://localhost", want http://localhost:8003`.

- [ ] **Step 3: Add `FamilyOriginPort`**

Append to `inventory.go` (after `FamilyOrigin`):

```go
// FamilyOriginPort is FamilyOrigin with the URL's port resolved, for callers
// that must hand a numeric port to a command line as well as dial the origin.
// When the origin carries no port the family default is applied to the origin
// itself, not only to the returned number, so the two can never describe
// different servers.
func FamilyOriginPort(cfg *config.Config, family string) (string, int, error) {
	origin, _ := FamilyOrigin(cfg, family)
	u, err := url.Parse(origin)
	if err != nil {
		return "", 0, fmt.Errorf("origin %q: %w", origin, err)
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil {
			return "", 0, fmt.Errorf("origin %q: bad port %q", origin, p)
		}
		return origin, n, nil
	}
	def, ok := defaultPortFor(family)
	if !ok {
		return "", 0, fmt.Errorf("origin %q has no port and family %q has no default", origin, family)
	}
	u.Host = net.JoinHostPort(u.Hostname(), strconv.Itoa(def))
	return u.String(), def, nil
}

// defaultPortFor is the port a family's default origin uses, so a registry base
// url that omits a port resolves to the same server the defaults describe.
func defaultPortFor(family string) (int, bool) {
	switch family {
	case "ollama":
		return 11434, true
	case "omlx":
		return 8000, true
	case "mtplx":
		return 8003, true
	}
	return 0, false
}
```

Add `"net"`, `"net/url"`, `"strconv"`, `"fmt"` to `inventory.go`'s imports if absent.

- [ ] **Step 4: Point `mtplxEndpoint` at it**

Replace `mtplxEndpoint` (`mtplx.go:22-32`) with:

```go
// mtplxEndpoint returns the origin, /v1/models URL and port for the family.
// All three come from one resolution so the port passed to `mtplx serve` cannot
// differ from the port the wait polls.
func mtplxEndpoint(cfg *config.Config) (origin, modelsURL string, port int) {
	origin, port, err := localmodels.FamilyOriginPort(cfg, "mtplx")
	if err != nil {
		// FamilyOriginPort only fails on an unparseable registry origin. Fall back
		// through the same resolver with no registry rather than to a local
		// constant, so the port still has exactly one source.
		origin, port, _ = localmodels.FamilyOriginPort(&config.Config{}, "mtplx")
	}
	return origin, origin + "/v1/models", port
}
```

Do **not** add a `defaultMtplxOrigin`/`defaultMtplxPort` constant to `mtplx.go`: `localmodels` already owns that value (`inventory.go:39`) and a second copy is the duplication this task exists to remove.

Then fix `mtplx.go`'s imports: `net/url` becomes unused (delete it); `strconv` is still used by the spawn argv's `strconv.Itoa(port)` and stays. Confirm with `go build ./internal/lifecycle`.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd wt && go test ./internal/localmodels ./internal/lifecycle -v && go vet ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add wt/internal/localmodels/ wt/internal/lifecycle/
git commit -m "fix(lifecycle): resolve mtplx origin and port from one source - completes plan item #6"
```

---

### Task 7: `lifecycle` — a non-2xx warmup answer is not a warmup

**Finding #4.** `tryChat` ignores `resp.StatusCode` and returns true whenever the body matches the `chat.completion` marker. The ported source (`probe.py`) raises `HTTPError` for any non-2xx and retries. A server answering a failed or in-progress load with a non-2xx status but a marker-bearing body (an error envelope echoing the request, a proxy debug body) is recorded as warmed, so `Start` returns nil while the model is not resident.

**Files:**
- Modify: `wt/internal/lifecycle/probe.go:167-173`
- Test: `wt/internal/lifecycle/probe_test.go`

**Interfaces:**
- Consumes: existing `env.warmup`, `chatCompletionMarker`.
- Produces: no new API; `tryChat` gains a status requirement.

- [ ] **Step 1: Write the failing test**

Append to `probe_test.go`:

```go
// TestWarmupRejectsNon2xx verifies a non-2xx answer is not accepted as a
// successful warmup even when its body carries the chat.completion marker.
// Accepting it reports a model as resident when it is not, so the caller's
// "ready to use" assumption is wrong on the very next request.
func TestWarmupRejectsNon2xx(t *testing.T) {
	e := testEnv()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"object":"chat.completion"}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	if err := e.warmup(context.Background(), srv.URL, "m", srv.URL, e.warmupTimeout); err == nil {
		t.Error("warmup must fail on a non-2xx response carrying a chat.completion body")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd wt && go test ./internal/lifecycle -run TestWarmupRejectsNon2xx -v`
Expected: FAIL — `warmup must fail on a non-2xx response carrying a chat.completion body`.

- [ ] **Step 3: Require a 2xx status in `tryChat`**

Replace `tryChat`'s tail (`probe.go:167-173`) with:

```go
	resp, err := e.chatClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false
	}
	// A non-2xx answer is not a warmup, even when its body echoes a
	// chat.completion marker: the ported probe raises HTTPError here and
	// retries. Accepting it reports a model as resident that is not.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false
	}
	return chatCompletionMarker.Match(body)
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd wt && go test ./internal/lifecycle -v`
Expected: PASS, including the existing warmup tests (which answer 200 with the marker).

- [ ] **Step 5: Commit**

```bash
git add wt/internal/lifecycle/
git commit -m "fix(lifecycle): reject non-2xx warmup responses - completes plan item #7"
```

---

### Task 8: `lifecycle` — `logTail` must not read the whole log

**Finding #6.** `logTail` calls `os.ReadFile` and then slices the last 512 bytes. `/tmp/local-ai-setup-mtplx.log` is append-only and shared with modelman, so it grows across every start; a failed start on the user-visible error path slurps the entire file. The ported source seeks to the tail for exactly this reason.

**Files:**
- Modify: `wt/internal/lifecycle/pidproc.go:85-95`
- Test: `wt/internal/lifecycle/mtplx_test.go`

**Interfaces:**
- Consumes: existing `pidProcess.logfile`, callers `pidproc.go:53` and `mtplx.go:62`.
- Produces: no API change; `logTail` reads at most `max` bytes.

- [ ] **Step 1: Write the failing test**

Append to `mtplx_test.go`:

```go
// TestLogTailReadsOnlyTheTail verifies logTail returns the last max bytes of a
// log, and nothing when the file is missing. The mtplx log is append-only and
// shared with modelman, so it grows across every start; a failed start must not
// depend on the whole file fitting in memory to show a 512-byte tail.
func TestLogTailReadsOnlyTheTail(t *testing.T) {
	dir := t.TempDir()
	p := pidProcess{
		name:    "mtplx",
		pidfile: filepath.Join(dir, "mtplx.pid"),
		logfile: filepath.Join(dir, "mtplx.log"),
	}
	content := append(bytes.Repeat([]byte("A"), 4096), []byte("TAIL")...)
	if err := os.WriteFile(p.logfile, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := p.logTail(4); got != "TAIL" {
		t.Errorf("logTail(4) = %q, want %q", got, "TAIL")
	}
	if got := p.logTail(1 << 20); got != string(content) {
		t.Errorf("logTail larger than the file = %d bytes, want all %d", len(got), len(content))
	}
	missing := pidProcess{logfile: filepath.Join(dir, "nope.log")}
	if got := missing.logTail(8); got != "" {
		t.Errorf("logTail on a missing file = %q, want empty", got)
	}
}
```

Add `"bytes"`, `"os"`, `"path/filepath"` to `mtplx_test.go`'s imports if absent.

**Note on what this test proves:** it pins the returned bytes, not the memory profile. The seek is a resource property that shows up in the diff rather than in an assertion — do not add a timing or allocation assertion for it, which would be flaky.

- [ ] **Step 2: Run the test to verify it behaves as expected**

Run: `cd wt && go test ./internal/lifecycle -run TestLogTailReadsOnlyTheTail -v`
Expected: **PASS against the current implementation.** The old `os.ReadFile`-then-slice returns the same bytes, so this test does not fail first — the defect is memory, not output. Write it as a regression pin for the returned bytes, then do Step 3 to make the resource property real.

**Note on what this test proves:** it pins the returned bytes, not the memory profile. The seek is a resource property visible in the diff rather than in an assertion — do not add a timing or allocation assertion for it, which would be flaky.

- [ ] **Step 3: Seek to the tail**

Replace `logTail` (`pidproc.go:85-95`) with:

```go
// logTail returns up to max trailing bytes of the log ("" when unreadable). It
// seeks from the end instead of reading the file: the log is append-only and
// shared with modelman, so it grows without bound, and a failed start must not
// read all of it to show a 512-byte tail.
func (p pidProcess) logTail(max int) string {
	f, err := os.Open(p.logfile)
	if err != nil {
		return ""
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return ""
	}
	if size := fi.Size(); size > int64(max) {
		if _, err := f.Seek(size-int64(max), io.SeekStart); err != nil {
			return ""
		}
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return ""
	}
	return string(b)
}
```

Add `"io"` to `pidproc.go`'s imports if absent.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd wt && go test ./internal/lifecycle -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add wt/internal/lifecycle/
git commit -m "perf(lifecycle): seek to the log tail instead of reading the whole file - completes plan item #8"
```

---

### Task 9: Docs

**Files:**
- Modify: `wt/CLAUDE.md` (the `internal/lifecycle/` and `internal/localmodels/` rows, and the **Key Gotchas** list if it names stop mechanisms)

**Interfaces:**
- Consumes: the behaviour implemented in Tasks 1–8.
- Produces: prose only.

- [ ] **Step 1: Update the `internal/lifecycle/` row**

In `wt/CLAUDE.md`'s package table, extend the `internal/lifecycle/` description so it states the new rule. Add to the existing sentence about `AllowReplace`:

> Where the live probe cannot determine the running state — a single-model provider's server accepted the connection and then stalled, or answered unusably — `Start` returns `*OccupancyUnknownError` instead of assuming no occupant, and only `AllowReplace` proceeds. A provider that is definitively down (connection refused) is not indeterminate: that is an ordinary cold start and needs no confirmation.

- [ ] **Step 2: Update the `internal/localmodels/` row**

In the same table, extend that row's status sentence to:

> Status per family is `ok`/`partial` (the live running-state probe failed — ollama `/api/ps`, or omlx/mtplx `/v1/models` — so Running is untrustworthy)/`unreachable`/`unsupported`.

and add `FamilyOriginPort(cfg, family)` to its list of exported helpers, described as: returning the family origin **and** its numeric port from one resolution, with the family default applied to the origin when the registry value omits a port, so a command-line port and a dialed URL cannot disagree.

- [ ] **Step 3: Verify the docs still link and lint**

Run: `cd .. && make check-links && cd wt && gofmt -l . && go vet ./...`
Expected: no output from `gofmt -l` (or only pre-existing files), no broken links, no vet findings.

- [ ] **Step 4: Commit**

```bash
git add wt/CLAUDE.md
git commit -m "docs(lifecycle): document indeterminate occupancy and honest probe status - completes plan item #9"
```

---

## Final verification

- [ ] `cd wt && gofmt -l . && go vet ./... && go test ./...` — all clean.
- [ ] From the monorepo root: `make test-all` — lint + modelman + wt, the same gate CI runs.
- [ ] Re-read the seven findings and confirm each has a task: #1 → Tasks 1 + 4; #2 → Task 6; #3 → Tasks 1 (root) + 2 (guard); #4 → Task 7; #5 → Task 3; #6 → Task 8; #7 → Task 5.
- [ ] Confirm the two invariants by inspection, since they are the reason this plan exists:
  - a cold start (`providers` down) never returns `*OccupancyUnknownError` — `TestStartProceedsWhenProviderIsDown`;
  - a stalled listener never reaches `b.start` without `AllowReplace` — `TestStartRefusesWhenListenerStalls`.
- [ ] Do **not** create a PR. Report the result and ask.
