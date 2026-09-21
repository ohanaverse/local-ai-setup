# Lifecycle stop rule, delegation test, Startable predicate Implementation Plan (Batch C, issues #128, #127, #119)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** (1) Stop the post-exit picker reporting `failed:` when `ollama stop` fails on a model that is already unloaded; (2) make the single-model stop-delegation test prove the delegation; (3) give "which providers can wt start" one source of truth shared by `internal/lifecycle` and `internal/catalog`.

**Architecture:** All three live in `wt/internal/lifecycle` (+ a one-line delegate in `internal/catalog` and a tiny exported helper in `internal/localmodels`). #128 adds a re-probe of ollama's loaded set (`/api/ps`) after a non-zero `ollama stop` exit — the same "trust the port/state, not the exit code" shape omlx and mtplx already use. #127 is test-only. #119 exports `lifecycle.Startable` over `backendsByFamily` and has `catalog.startable` call it.

**Tech Stack:** Go 1.26, `net/http/httptest`.

**Spec:** GitHub issues #128, #127, #119 (issue text is the spec; #128's policy question is decided below).

## Global Constraints

- All commands run from `wt/`.
- Every test carries a comment saying what it does and why it matters (user rule).
- Do not push or open a PR without asking the user.
- Commit trailer: `Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>`; commits during execution reference the plan item (`- completes plan item #N`).
- Import graph must stay acyclic: `lifecycle` imports only `config` + `localmodels`; `catalog` may import `lifecycle`.
- **Run Batch B (`2026-09-20-wt-flaky-lifecycle-stop-tests.md`) first**, since these tasks add tests to the same files and need a stable suite.

## Decision recorded (#128)

Rule: **re-probe the loaded set after a non-zero exit and treat an absent model as success** (issue option 1). Rationale: it matches the port-poll shape omlx/mtplx use, and does not depend on ollama's wording. The issue's worry about swallowing a name typo does not apply here: the picker offers only models the inventory just reported running, so an absent model after a failed stop means it was unloaded in between. If the re-probe itself fails, the original ollama message is surfaced unchanged (fail closed). Confirm this with the user before Task 2 if they have changed their mind.

## File Structure

- Modify: `internal/lifecycle/ollama.go` — re-probe in `stopModel`
- Modify: `internal/localmodels/sources.go` — export `OllamaLoaded`
- Modify: `internal/lifecycle/stopmodel_test.go` — ollama failure tests, delegation test
- Modify: `internal/lifecycle/lifecycle.go` — `Startable`, `CanStop` delegates to it
- Modify: `internal/catalog/catalog.go` — `startable` delegates
- Modify: `internal/lifecycle/lifecycle_test.go` — guard test

---

### Task 1: #119 — export `lifecycle.Startable` and delegate catalog to it

**Files:**
- Modify: `internal/lifecycle/lifecycle.go` (near `CanStop`, end of file)
- Modify: `internal/catalog/catalog.go:138-146`
- Test: `internal/lifecycle/lifecycle_test.go`

**Interfaces:**
- Produces: `func Startable(providerID string) bool` — true iff `backendsByFamily` has a backend for `localmodels.Family(providerID)`. `CanStop` keeps its signature and now returns `Startable(providerID)` (every backend has both start and stop, so the two predicates are the same fact; keeping one implementation is the point).

- [ ] **Step 1: Write the failing guard test**

Append to `lifecycle_test.go`:

```go
// TestStartableMatchesBackendRegistry verifies Startable is true for exactly
// the families in backendsByFamily (including the omlx-6bit alias) and false
// for providers wt has no backend for. catalog.startable delegates to it, so
// this is the guard that a new backend cannot be registered yet still render
// as an unstartable, "modelman start" row in the pickers.
func TestStartableMatchesBackendRegistry(t *testing.T) {
	for family := range backendsByFamily {
		if !Startable(family) {
			t.Errorf("Startable(%q) = false, but %q is in backendsByFamily", family, family)
		}
	}
	for id, want := range map[string]bool{
		"omlx-6bit": true, "mlx_lm_server": false, "anthropic": false, "": false,
	} {
		if got := Startable(id); got != want {
			t.Errorf("Startable(%q) = %v, want %v", id, got, want)
		}
	}
}
```

- [ ] **Step 2: Run to see it fail**

Run: `go test ./internal/lifecycle -run TestStartableMatchesBackendRegistry -v`
Expected: FAIL to compile — `undefined: Startable`.

- [ ] **Step 3: Implement**

In `lifecycle.go`, replace `CanStop`:

```go
// Startable reports whether wt has a lifecycle backend for providerID's family
// (ollama, omlx/omlx-6bit, mtplx). backendsByFamily is the single source of
// truth; internal/catalog delegates here so the picker's "can wt start this?"
// answer cannot drift from the engine's.
func Startable(providerID string) bool {
	return backendsByFamily[localmodels.Family(providerID)] != nil
}

// CanStop reports whether wt has a stop backend for providerID's family, so a
// picker never offers a model (e.g. one on mlx_lm_server) that StopModel would
// refuse with *UnsupportedError. Every backend implements both start and
// stop, so this is Startable under its stop-side name.
func CanStop(providerID string) bool { return Startable(providerID) }
```

In `catalog.go`, replace `startable`'s body and doc:

```go
// startable reports whether wt has a lifecycle backend for the provider
// family. It delegates to lifecycle.Startable — the single source of truth —
// so adding a backend there updates every picker with no second edit here.
func startable(providerID string) bool { return lifecycle.Startable(providerID) }
```

Add `"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"` to catalog's imports; drop `localmodels` from them only if nothing else in the file uses it (it does: `localmodels.Snapshot`, so keep it).

- [ ] **Step 4: Run**

Run: `go build ./... && go test ./internal/lifecycle ./internal/catalog ./internal/tui ./cmd/wt -count=1`
Expected: PASS (existing `TestCanStop` and the catalog tests stay green — they are the regression net).

- [ ] **Step 5: Commit**

```bash
git add internal/lifecycle/lifecycle.go internal/lifecycle/lifecycle_test.go internal/catalog/catalog.go
git commit -m "refactor(wt): one Startable predicate shared by lifecycle and catalog - completes plan item #1 (#119)"
```

---

### Task 2: #128 — treat an already-unloaded model as a successful ollama stop

**Files:**
- Modify: `internal/localmodels/sources.go` (add `OllamaLoaded` under `ollamaModelNames`)
- Modify: `internal/lifecycle/ollama.go:21-36`
- Modify: `internal/lifecycle/stopmodel_test.go:39-56`

**Interfaces:**
- Produces: `func OllamaLoaded(client *http.Client, origin string) ([]string, error)` in `localmodels` — names of models ollama has loaded right now (`GET <origin>/api/ps`, remote-host models excluded, same parsing as inventory).
- Consumes: `localmodels.FamilyOrigin(cfg, "ollama") (string, bool)`, `sameModel("ollama", a, b)`, `env.probeClient`.

- [ ] **Step 1: Write the failing tests**

In `stopmodel_test.go`, add the imports `net/http`, `net/http/httptest`, and this helper + tests. Replace the first half of `TestStopModelOllamaSurfacesFailure` (which used `&config.Config{}` and would now probe the developer's real ollama on `:11434` after a failing `run`) with a config pointing at a stub daemon:

```go
// psServing is a stub ollama daemon whose /api/ps lists the given loaded
// models; used so the post-failure re-probe never touches a real daemon.
func psServing(t *testing.T, loaded ...string) *httptest.Server {
	t.Helper()
	body := `{"models":[`
	for i, n := range loaded {
		if i > 0 {
			body += ","
		}
		body += `{"name":"` + n + `"}`
	}
	body += `]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// failingOllamaStop is an env whose `ollama stop` exits 1 with "model not found".
func failingOllamaStop() *env {
	e := testEnv()
	e.lookPath = func(string) (string, error) { return "/bin/ollama", nil }
	e.run = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("model not found\n"), errors.New("exit 1")
	}
	return e
}

// TestStopModelOllamaAlreadyUnloadedIsSuccess verifies that when `ollama stop`
// exits non-zero but /api/ps shows the model is no longer loaded (daemon
// restart or eviction between the picker's probe and the stop), the stop is a
// success. The goal — the model is not loaded — is met, and a spurious
// "failed: model not found" reads as a broken provider CLI.
func TestStopModelOllamaAlreadyUnloadedIsSuccess(t *testing.T) {
	daemon := psServing(t) // nothing loaded
	err := stopModel(context.Background(), failingOllamaStop(), provCfg("ollama", daemon.URL), "ollama", "x")
	if err != nil {
		t.Fatalf("err = %v, want nil: the model is already unloaded", err)
	}
}

// TestStopModelOllamaStillLoadedSurfacesFailure verifies a failed `ollama stop`
// is still an error when /api/ps shows the model loaded — including under
// ollama's implicit ":latest" tag — so a real failure is never masked.
func TestStopModelOllamaStillLoadedSurfacesFailure(t *testing.T) {
	for _, loaded := range []string{"x", "x:latest"} {
		daemon := psServing(t, loaded)
		err := stopModel(context.Background(), failingOllamaStop(), provCfg("ollama", daemon.URL), "ollama", "x")
		if err == nil || err.Error() != "model not found" {
			t.Errorf("loaded=%q: err = %v, want ollama's own message", loaded, err)
		}
	}
}

// TestStopModelOllamaReprobeFailureKeepsOriginalError verifies that when the
// re-probe itself cannot get an answer (daemon gone), the original CLI message
// is returned rather than guessing success: with no evidence the model is
// unloaded, the stop is reported as it failed.
func TestStopModelOllamaReprobeFailureKeepsOriginalError(t *testing.T) {
	daemon := psServing(t)
	url := daemon.URL
	daemon.Close()
	err := stopModel(context.Background(), failingOllamaStop(), provCfg("ollama", url), "ollama", "x")
	if err == nil || err.Error() != "model not found" {
		t.Fatalf("err = %v, want ollama's own message", err)
	}
}
```

Keep the `BinaryMissingError` half of the old test as its own function `TestStopModelOllamaMissingBinary` (no probe happens, so `&config.Config{}` is fine there). Delete the old `TestStopModelOllamaSurfacesFailure`.

- [ ] **Step 2: Run to see them fail**

Run: `go test ./internal/lifecycle -run 'TestStopModelOllama' -v`
Expected: `TestStopModelOllamaAlreadyUnloadedIsSuccess` FAILS (`err = model not found`); the other two pass or fail to compile only if `provCfg` is missing (it exists in `backends_test.go`).

- [ ] **Step 3: Export the loaded-models read**

In `internal/localmodels/sources.go`, after `ollamaModelNames`:

```go
// OllamaLoaded returns the local models ollama has loaded right now (its
// /api/ps), with the same parsing inventory uses. An error means the daemon
// gave no usable answer, so an empty list must not be read as "none loaded".
func OllamaLoaded(client *http.Client, origin string) ([]string, error) {
	return ollamaModelNames(client, origin+"/api/ps")
}
```

- [ ] **Step 4: Implement the re-probe**

Replace `ollamaBackend.stopModel` in `ollama.go`:

```go
// stopModel unloads one model with `ollama stop <name>`; the daemon and every
// other loaded model stay up. A non-zero exit is not by itself a failure: the
// goal is "the model is not loaded", so it re-probes /api/ps and succeeds when
// the model is already gone (a daemon restart or eviction between the picker's
// probe and this stop makes `ollama stop` exit 1 with "model not found") —
// the same trust-the-state-not-the-exit-code shape omlx and mtplx use. When
// the re-probe cannot answer, or the model is still loaded, the error carries
// ollama's own message.
func (ollamaBackend) stopModel(ctx context.Context, e *env, cfg *config.Config, modelName string) error {
	bin, err := e.lookPath("ollama")
	if err != nil {
		return &BinaryMissingError{Binary: "ollama"}
	}
	out, runErr := e.run(ctx, bin, "stop", modelName)
	if runErr == nil {
		return nil
	}
	origin, _ := localmodels.FamilyOrigin(cfg, "ollama")
	if loaded, perr := localmodels.OllamaLoaded(e.probeClient, origin); perr == nil && !anySameModel("ollama", loaded, modelName) {
		return nil
	}
	if msg := strings.TrimSpace(string(out)); msg != "" {
		return errors.New(msg)
	}
	return fmt.Errorf("ollama stop %s failed: %w", modelName, runErr)
}

// anySameModel reports whether any name in names denotes want under family's
// matching rule.
func anySameModel(family string, names []string, want string) bool {
	for _, n := range names {
		if sameModel(family, n, want) {
			return true
		}
	}
	return false
}
```

- [ ] **Step 5: Run**

Run: `go test ./internal/lifecycle ./internal/survey -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/localmodels/sources.go internal/lifecycle/ollama.go internal/lifecycle/stopmodel_test.go
git commit -m "fix(wt): ollama stop succeeds when the model is already unloaded - completes plan item #2 (#128)"
```

---

### Task 3: #127 — make the delegation test prove the delegation

**Files:**
- Modify: `internal/lifecycle/stopmodel_test.go:54-70`

**Interfaces:**
- Consumes: `testEnv()`, `serveFree` (from Batch B; if Batch B has not landed, use `freeAddr` + `serveAt` as in `TestOmlxStopWaitsForPortClose`), `provCfg`, `chatHandler`, `stopModel`.

- [ ] **Step 1: Rename the existing routing test honestly**

Rename `TestStopModelSingleModelDelegatesToStop` to `TestStopModelRoutesToProviderBackend` and change its comment to say it verifies only dispatch: the picker's `stopModel` reaches the backend registered for each provider id (including the `omlx-6bit` alias). Body unchanged.

- [ ] **Step 2: Write the delegation tests through the real backends**

```go
// TestStopModelOmlxRunsOmlxStopAndWaitsForPort verifies the omlx stopModel
// thunk actually reaches the daemon stop: it invokes `omlx stop` and returns
// only after the port closes. A thunk that returned nil without stopping would
// leave the post-exit picker printing "done" for a model still loaded.
func TestStopModelOmlxRunsOmlxStopAndWaitsForPort(t *testing.T) {
	srv, addr := serveFree(t, chatHandler())
	e := testEnv()
	e.lookPath = func(string) (string, error) { return "/bin/omlx", nil }
	var ran []string
	e.run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		ran = append(ran, name+" "+strings.Join(args, " "))
		_ = srv.Close()
		return nil, nil
	}
	if err := stopModel(context.Background(), e, provCfg("omlx", "http://"+addr), "omlx", "m"); err != nil {
		t.Fatalf("stopModel: %v", err)
	}
	if len(ran) != 1 || ran[0] != "/bin/omlx stop" {
		t.Fatalf("ran = %v, want [/bin/omlx stop]", ran)
	}

	// The port never closes: the thunk must report it, not "done".
	_, addr2 := serveFree(t, chatHandler())
	e.run = func(context.Context, string, ...string) ([]byte, error) { return nil, nil }
	e.stopTimeout = 60 * time.Millisecond
	if err := stopModel(context.Background(), e, provCfg("omlx", "http://"+addr2), "omlx", "m"); err == nil {
		t.Fatal("stopModel returned nil while the daemon still holds its port")
	}
}

// TestStopModelMtplxRunsMtplxStopAndWaitsForPort is the mtplx counterpart: the
// thunk must run `mtplx stop --port N ...` and confirm the port closed.
func TestStopModelMtplxRunsMtplxStopAndWaitsForPort(t *testing.T) {
	srv, addr := serveFree(t, chatHandler())
	_, portStr, _ := net.SplitHostPort(addr)
	e := testEnv()
	e.lookPath = func(string) (string, error) { return "/bin/mtplx", nil }
	var ran []string
	e.run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		ran = append([]string{name}, args...)
		_ = srv.Close()
		return nil, nil
	}
	if err := stopModel(context.Background(), e, provCfg("mtplx", "http://"+addr+"/v1"), "mtplx", "m"); err != nil {
		t.Fatalf("stopModel: %v", err)
	}
	want := []string{"/bin/mtplx", "stop", "--port", portStr, "--grace-seconds", "10"}
	if !reflect.DeepEqual(ran, want) {
		t.Fatalf("ran = %v, want %v", ran, want)
	}
}
```

Add imports `net`, `reflect`, `time` to `stopmodel_test.go`.

- [ ] **Step 3: Run**

Run: `go test ./internal/lifecycle -run 'TestStopModel' -count=1 -v`
Expected: PASS.

- [ ] **Step 4: Mutation check (the whole point of the issue)**

Temporarily change `omlxBackend.stopModel`'s body in `omlx.go` to `return nil`, run the two new tests: `TestStopModelOmlxRunsOmlxStopAndWaitsForPort` must FAIL. Repeat for `mtplxBackend.stopModel` with the mtplx test. `git checkout internal/lifecycle/omlx.go internal/lifecycle/mtplx.go` to restore. Confirm `TestStopModelRoutesToProviderBackend` alone would have stayed green under the mutation (that is the gap this task closes).

- [ ] **Step 5: Commit**

```bash
git add internal/lifecycle/stopmodel_test.go
git commit -m "test(wt): prove single-model stopModel delegates to the real stop - completes plan item #3 (#127)"
```

## Final verification

- [ ] Run `go build ./... && go vet ./... && go test ./... -count=1` and `gofmt -l .` (expect no output). Report the outputs.
- [ ] Update `wt/CLAUDE.md` only if it names `catalog.startable`'s hard-coded list (`grep -n "startable\|backendsByFamily" CLAUDE.md`); if it does, point it at `lifecycle.Startable`.
- [ ] Ask the user before pushing or opening a PR. Suggested PR closes #128, #127, #119.

## Self-review

- Coverage: #119 (Task 1, incl. the guard test), #128 (Task 2, policy stated), #127 (Task 3 with mutation check).
- Type consistency: `Startable(providerID string) bool`, `OllamaLoaded(*http.Client, string) ([]string, error)`, `anySameModel(family string, names []string, want string) bool` — each defined where first used and used consistently.
- Risk: Task 2's re-probe adds one HTTP round trip on the failure path only; tests that fail `run` now must provide a stub origin (done) or they would probe a developer's real ollama.
