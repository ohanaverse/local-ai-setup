# Code Review Fixes (2026-09-23 nyt-litellm-provider review) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the 8 findings from the `/code-review` pass on the `wt-nyt-litellm-provider` branch diff (the new `exec:` secret_ref form and its consumers).

**Architecture:** No new subsystems. Each task is a targeted fix inside `internal/config`, `internal/litellm`, `internal/agents`, or `internal/tui`, changing one mechanism at a time: `ResolveSecret`'s locking/caching model, `entry.go`'s LiteLLM row builder, `pi_models.go`'s sync-failure blast radius, and `renderTable`'s per-row route resolution.

**Tech Stack:** Go (wt module), existing test patterns (table-driven `_test.go`, package-level var seams).

**Spec:** The code-review findings reported in this conversation (no separate spec doc; findings summarized per task below).

## Global Constraints

- No new third-party dependencies (implement per-ref locking by hand rather than pulling in `golang.org/x/sync/singleflight`).
- Every touched exported/interface signature (`Syncer.SyncModels`) must update its one production caller and all test call sites in the same task.
- `go build ./... && go vet ./... && go test ./...` (run from `wt/`) must pass at the end of every task.

## Review Focus

- A helper that fails once (bad token, not logged in) and then would succeed must not stay broken for the rest of the process — covered by Task 1's "don't cache errors" test.
- Two distinct `exec:`-secured providers, one slow, one fast, must not have the fast one wait on the slow one — covered by Task 1's per-ref concurrency test and Task 5's parallel-row test.
- `openrouter`'s `secret_ref` in `[litellm].api_key` context (the one path `entry.go` covers) must resolve the same way direct-mode auth does — covered by Task 3.
- A registry with two direct-mode providers, one broken, must still let a launch against the healthy one succeed — covered by Task 4.
- Shrinking `execSecretTimeout` in a test must not leave the package timeout permanently changed for later tests in the same run — covered by Task 2's `t.Cleanup`.

---

### Task 1: `ResolveSecret` — per-ref locking, don't cache errors

Fixes findings: **#3** (a failed `exec:` resolution is cached forever, including the error) and **#6** (one global mutex serializes every `exec:` ref, not just concurrent calls for the *same* ref).

**Files:**
- Modify: `internal/config/config.go:283-315` (the `execSecretTimeout`/`execSecretMu`/`execSecretCache`/`ResolveSecret` block)
- Test: `internal/config/config_test.go` (extend `TestResolveSecretExecMemoizedConcurrent`'s neighborhood with two new tests)

**Interfaces:**
- Consumes: nothing new.
- Produces: `ResolveSecret(ref string) (string, error)` keeps its existing signature and memoization contract for the exec: form (concurrent callers for the same ref never run the command twice) — only the caching-on-error and cross-ref-locking behavior changes. `runExecSecret` is unchanged.

- [x] **Step 1: Write the failing tests**

Add to `internal/config/config_test.go` (near `TestResolveSecretExecMemoizedConcurrent`):

```go
// TestResolveSecretExecErrorNotCached pins that a failed exec: resolution is
// NOT memoized: a helper that fails once (e.g. not yet logged into Vault)
// must succeed on a later call once the underlying problem is fixed,
// without requiring a process restart. Caching the error here would mean a
// wt TUI session started before `vault login` stays broken until relaunched.
func TestResolveSecretExecErrorNotCached(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "flaky.sh")
	// First call fails (exit 1); second call (same ref) succeeds.
	body := `#!/bin/sh
if [ -f "` + dir + `/ok" ]; then
  echo sk-recovered
  exit 0
fi
touch "` + dir + `/ok"
echo "not logged in" >&2
exit 1
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	ref := "exec:" + script

	if _, err := ResolveSecret(ref); err == nil {
		t.Fatal("ResolveSecret: want error on first (failing) call, got nil")
	}
	got, err := ResolveSecret(ref)
	if err != nil {
		t.Fatalf("ResolveSecret second call: want success once the helper recovers, got error: %v", err)
	}
	if got != "sk-recovered" {
		t.Errorf("ResolveSecret second call = %q, want %q", got, "sk-recovered")
	}
}

// TestResolveSecretExecDistinctRefsRunConcurrently pins that resolving two
// DIFFERENT exec: refs at the same time does not serialize one behind the
// other — a single global lock held for a whole subprocess run would make
// an unrelated, already-cached-or-fast provider wait out a slow/hung one.
func TestResolveSecretExecDistinctRefsRunConcurrently(t *testing.T) {
	dir := t.TempDir()
	slow := filepath.Join(dir, "slow.sh")
	fast := filepath.Join(dir, "fast.sh")
	if err := os.WriteFile(slow, []byte("#!/bin/sh\nsleep 1\necho sk-slow\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fast, []byte("#!/bin/sh\necho sk-fast\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, err := ResolveSecret("exec:" + slow); err != nil {
			t.Errorf("slow ResolveSecret: %v", err)
		}
	}()
	// Give the slow call a moment to acquire and start its subprocess before
	// timing the fast one.
	time.Sleep(100 * time.Millisecond)

	start := time.Now()
	got, err := ResolveSecret("exec:" + fast)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("fast ResolveSecret: %v", err)
	}
	if got != "sk-fast" {
		t.Errorf("fast ResolveSecret = %q, want %q", got, "sk-fast")
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("fast ResolveSecret took %v while a distinct ref was in flight, want it to not wait on the slow ref", elapsed)
	}
	wg.Wait()
}
```

- [x] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/config/... -run TestResolveSecretExec -v`
Expected: `TestResolveSecretExecErrorNotCached` fails (second call still returns the cached first error). `TestResolveSecretExecDistinctRefsRunConcurrently` fails or is flaky-slow (the fast call waits behind the global mutex for ~1s, tripping the 500ms assertion).

- [x] **Step 3: Replace the locking/caching implementation**

Replace lines 283-315 of `internal/config/config.go`:

```go
// execSecretTimeout bounds how long an exec: secret_ref command may run.
// This is the only unbounded-by-default subprocess wt would otherwise
// spawn; every other exec site (proxy restart, git fetch, lifecycle
// backends) already carries a deadline.
const execSecretTimeout = 15 * time.Second

var execSecretMu sync.Mutex
var execSecretCache = map[string]execSecretResult{}

type execSecretResult struct {
	value string
	err   error
}

func ResolveSecret(ref string) (string, error) {
	if cmdline, ok := strings.CutPrefix(ref, "exec:"); ok {
		execSecretMu.Lock()
		defer execSecretMu.Unlock()
		if r, ok := execSecretCache[ref]; ok {
			return r.value, r.err
		}
		value, err := runExecSecret(ref, cmdline)
		execSecretCache[ref] = execSecretResult{value, err}
		return value, err
	}
	if name, ok := strings.CutPrefix(ref, "os.environ/"); ok {
		return os.Getenv(name), nil
	}
	if envRefName.MatchString(ref) {
		return os.Getenv(ref), nil
	}
	return ref, nil
}
```

with:

```go
// execSecretTimeout bounds how long an exec: secret_ref command may run.
// This is the only unbounded-by-default subprocess wt would otherwise
// spawn; every other exec site (proxy restart, git fetch, lifecycle
// backends) already carries a deadline. A var (not const), following this
// package's seam convention (wt/CLAUDE.md's Go tests section — "a var x =
// realX plus a realX function"), so a test can shrink it instead of paying
// the real deadline in wall-clock time.
var execSecretTimeout = 15 * time.Second

// execSecretMu guards execSecretCache and execSecretInflight only long
// enough to read or write those two maps — never for the duration of a
// subprocess run. Holding one global lock across a whole `runExecSecret`
// call (the prior implementation) meant two DIFFERENT exec: refs could not
// resolve concurrently: a slow or hung helper for provider A would block an
// unrelated, already-fast provider B behind it. Deduping concurrent callers
// of the SAME ref onto one subprocess run (the memoization
// TestResolveSecretExecMemoizedConcurrent pins) is handled per-ref instead,
// via execSecretInflight.
var execSecretMu sync.Mutex

// execSecretCache holds only successful resolutions, for the process
// lifetime. A failed resolution is deliberately never cached: an exec:
// helper backed by e.g. Vault or 1Password can fail once for a transient
// reason (not yet logged in, a stale token) and succeed moments later, and
// wt's TUI is long-lived enough that caching the failure would force a full
// restart to recover.
var execSecretCache = map[string]string{}

// execSecretInflight tracks exec: resolutions currently running, one entry
// per ref, so concurrent callers for the same ref share a single
// subprocess run instead of each starting their own.
var execSecretInflight = map[string]*execSecretCall{}

// execSecretCall is one in-flight (or just-finished) exec: resolution.
// value/err are only written once, by the goroutine that created the
// entry, before done is closed — every other reader only touches them
// after receiving from done, so no further synchronization is needed.
type execSecretCall struct {
	done  chan struct{}
	value string
	err   error
}

func ResolveSecret(ref string) (string, error) {
	if cmdline, ok := strings.CutPrefix(ref, "exec:"); ok {
		return resolveExecSecret(ref, cmdline)
	}
	if name, ok := strings.CutPrefix(ref, "os.environ/"); ok {
		return os.Getenv(name), nil
	}
	if envRefName.MatchString(ref) {
		return os.Getenv(ref), nil
	}
	return ref, nil
}

// resolveExecSecret resolves one exec: ref. See execSecretMu/execSecretCache/
// execSecretInflight above for the concurrency and caching contract.
func resolveExecSecret(ref, cmdline string) (string, error) {
	execSecretMu.Lock()
	if v, ok := execSecretCache[ref]; ok {
		execSecretMu.Unlock()
		return v, nil
	}
	if call, ok := execSecretInflight[ref]; ok {
		execSecretMu.Unlock()
		<-call.done
		return call.value, call.err
	}
	call := &execSecretCall{done: make(chan struct{})}
	execSecretInflight[ref] = call
	execSecretMu.Unlock()

	value, err := runExecSecret(ref, cmdline)
	call.value, call.err = value, err
	close(call.done)

	execSecretMu.Lock()
	delete(execSecretInflight, ref)
	if err == nil {
		execSecretCache[ref] = value
	}
	execSecretMu.Unlock()

	return value, err
}
```

Also update the `ResolveSecret` doc comment above (lines 262-280) where it says "Memoized per raw ref for the lifetime of the process" — add one clause: "(a failure is not memoized — only a successful resolution is)".

- [x] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/config/... -v`
Expected: PASS, including the two new tests and the pre-existing `TestResolveSecretExecMemoized`, `TestResolveSecretExecMemoizedConcurrent`, `TestResolveSecret`, `TestResolveSecretExecEmptyOutputErrors`.

- [x] **Step 5: Commit**

```bash
cd wt
git add internal/config/config.go internal/config/config_test.go
git commit -m "$(cat <<'EOF'
fix(config): don't cache exec: secret_ref errors, unblock distinct refs

A failed exec: resolution was memoized forever (process lifetime),
so a transient credential-helper failure required a full wt restart
to recover. The single global mutex also serialized every exec: ref
through one subprocess-run-sized critical section, so one slow or
hung provider's helper blocked an unrelated, already-cached-or-fast
provider behind it.

completes plan item #1 (docs/superpowers/plans/2026-09-23-code-review-fixes.md)
EOF
)"
```

---

### Task 2: `execSecretTimeout` seam + fast timeout test

Fixes finding: **#7** (`TestResolveSecretExecTimesOut` genuinely sleeps ~17s because the timeout is a `const`).

**Files:**
- Modify: `internal/config/config_test.go:107-125` (`TestResolveSecretExecTimesOut`)

**Interfaces:**
- Consumes: `execSecretTimeout` is now a package-level `var` (made so in Task 1's Step 3).
- Produces: nothing new for other tasks.

- [x] **Step 1: Replace the test**

Replace `TestResolveSecretExecTimesOut` (lines 107-125) with:

```go
// TestResolveSecretExecTimesOut pins that a hung exec: command is killed
// after a bounded deadline instead of hanging wt forever — this is the
// only unbounded subprocess wt would otherwise spawn (every other exec
// site uses exec.CommandContext with a deadline). execSecretTimeout is
// shrunk for the duration of this test (wt/CLAUDE.md's var-seam
// convention) so the test doesn't pay the real ~15-17s deadline in
// wall-clock time on every run.
func TestResolveSecretExecTimesOut(t *testing.T) {
	orig := execSecretTimeout
	execSecretTimeout = 200 * time.Millisecond
	t.Cleanup(func() { execSecretTimeout = orig })

	script := filepath.Join(t.TempDir(), "hang.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err := ResolveSecret("exec:" + script)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("ResolveSecret: want timeout error for a hung command, got nil")
	}
	// Bounded by execSecretTimeout (200ms) plus runExecSecret's own 2s
	// WaitDelay grace period for the child's I/O pipes to close.
	if elapsed > 5*time.Second {
		t.Errorf("ResolveSecret took %v with execSecretTimeout=200ms, want it to time out quickly", elapsed)
	}
}
```

- [x] **Step 2: Run the test and confirm it's fast**

Run: `time go test ./internal/config/... -run TestResolveSecretExecTimesOut -v`
Expected: PASS in well under 3s (was ~17s before this change).

- [x] **Step 3: Run the full package once more**

Run: `go test ./internal/config/... -v`
Expected: PASS, and `execSecretTimeout` is back to 15s for every other test (the `t.Cleanup` ran).

- [x] **Step 4: Commit**

```bash
git add internal/config/config_test.go
git commit -m "$(cat <<'EOF'
test(config): shrink execSecretTimeout in the timeout test

TestResolveSecretExecTimesOut paid the real ~15-17s deadline on
every run because execSecretTimeout was a const. Made it a var in
the prior commit (wt's own seam convention); this test now shrinks
it for its own duration and restores it via t.Cleanup.

completes plan item #2 (docs/superpowers/plans/2026-09-23-code-review-fixes.md)
EOF
)"
```

---

### Task 3: Resolve `exec:` secret_ref in `internal/litellm/entry.go`

Fixes finding: **#2** (`BuildEntry` writes `SecretRef` verbatim into `config.yaml`'s `api_key` for `SecretRef: true` policies, silently unsupporting the `exec:` form there).

**Files:**
- Modify: `internal/litellm/entry.go:86-104` (`BuildEntry`)
- Modify: `internal/litellm/policy.go` doc comment (`SecretRef` field)
- Test: `internal/litellm/entry_test.go`

**Interfaces:**
- Consumes: `config.ResolveSecret(ref string) (string, error)` (unchanged signature; `internal/config` already imported in `entry.go` as `config`).
- Produces: `BuildEntry` keeps its `(*yaml.Node, error)` signature; callers (`service.go`'s `prepare`) already propagate a `BuildEntry` error per-id, so no caller changes are needed.

- [x] **Step 1: Write the failing test**

Add to `internal/litellm/entry_test.go` (near the other `openrouter`/`SecretRef` tests — check the existing test file for the right provider fixture pattern first: `go test ./internal/litellm/... -run TestBuildEntry -v -list '.*'` or read the file to match its `cfg`/table style):

```go
// TestBuildEntrySecretRefResolvesExecForm pins that a SecretRef:true policy
// (currently only openrouter) resolves auth.secret_ref through
// config.ResolveSecret before writing config.yaml's api_key — including the
// exec: form. Writing the ref string verbatim (the pre-fix behavior) sends
// LiteLLM the literal string "exec:..." as a bearer token: a silent,
// confusing 401 with no error surfaced anywhere.
func TestBuildEntrySecretRefResolvesExecForm(t *testing.T) {
	script := filepath.Join(t.TempDir(), "key.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho sk-from-exec\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := config.Model{ID: "openrouter/z-ai/glm-4.6", ModelName: "z-ai/glm-4.6", ProviderID: "openrouter"}
	p := config.Provider{ID: "openrouter", Auth: config.AuthConfig{Type: "secret_ref", BaseURL: "https://openrouter.ai/api/v1", SecretRef: "exec:" + script}}

	node, err := BuildEntry(m, p)
	if err != nil {
		t.Fatalf("BuildEntry: %v", err)
	}
	apiKey := findMappingValue(t, findMappingValue(t, node, "litellm_params"), "api_key")
	if apiKey != "sk-from-exec" {
		t.Errorf("api_key = %q, want the exec: form resolved to %q", apiKey, "sk-from-exec")
	}
}

// TestBuildEntrySecretRefPropagatesResolveError pins that a failing
// secret_ref (e.g. a broken exec: helper) surfaces as a BuildEntry error
// instead of silently writing a bad api_key.
func TestBuildEntrySecretRefPropagatesResolveError(t *testing.T) {
	script := filepath.Join(t.TempDir(), "fail.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'no vault token' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := config.Model{ID: "openrouter/z-ai/glm-4.6", ModelName: "z-ai/glm-4.6", ProviderID: "openrouter"}
	p := config.Provider{ID: "openrouter", Auth: config.AuthConfig{Type: "secret_ref", BaseURL: "https://openrouter.ai/api/v1", SecretRef: "exec:" + script}}

	if _, err := BuildEntry(m, p); err == nil || !strings.Contains(err.Error(), "no vault token") {
		t.Fatalf("BuildEntry error = %v, want it to surface the helper's stderr", err)
	}
}
```

Note: `findMappingValue` may not exist yet in `entry_test.go` — check the file first; if the existing tests already have an equivalent helper (they likely decode the returned `*yaml.Node` some way to assert on `api_key`), reuse it and adjust the two tests above to match its actual name/signature instead of introducing a duplicate. Add `"os"`, `"path/filepath"`, `"strings"` imports if not already present.

- [x] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/litellm/... -run TestBuildEntrySecretRef -v`
Expected: FAIL — `api_key` is the literal ref string / no error is returned for the failing case.

- [x] **Step 3: Implement the fix**

In `internal/litellm/entry.go`, change:

```go
	switch {
	case pol.SecretRef:
		params = append(params, kv{"api_key", p.Auth.SecretRef})
	case pol.APIKey != "":
		params = append(params, kv{"api_key", pol.APIKey})
	}
```

to:

```go
	switch {
	case pol.SecretRef:
		key, err := config.ResolveSecret(p.Auth.SecretRef)
		if err != nil {
			return nil, fmt.Errorf("model %q: resolving credentials for provider %q: %w", m.ID, p.ID, err)
		}
		params = append(params, kv{"api_key", key})
	case pol.APIKey != "":
		params = append(params, kv{"api_key", pol.APIKey})
	}
```

Update the `Policy.SecretRef` doc comment in `internal/litellm/policy.go` (currently "api_key comes from the provider's auth.secret_ref instead") to: "api_key comes from resolving the provider's auth.secret_ref (config.ResolveSecret — env, exec:, or literal) instead."

Also update `internal/config/config.go`'s `ResolveSecret` doc comment (the line "Shared by direct-mode provider auth and [litellm].api_key (the latter never uses the exec: form today, but nothing prevents it)") to drop "never uses the exec: form today" since it now does.

- [x] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/litellm/... -v`
Expected: PASS, including every pre-existing `BuildEntry`/`openrouter` test (they use a literal `SecretRef`, e.g. `"OPENROUTER_API_KEY"` or a literal key — `config.ResolveSecret` on a literal/env-name ref returns it unchanged when the env var happens to be unset it returns `""`; check the existing openrouter fixture's `SecretRef` value and, if it's a bare env-var name, set that env var in the existing tests via `t.Setenv` if not already, or confirm the existing fixture already sets one — read `entry_test.go` before assuming).

- [x] **Step 5: Commit**

```bash
git add internal/litellm/entry.go internal/litellm/policy.go internal/litellm/entry_test.go internal/config/config.go
git commit -m "$(cat <<'EOF'
fix(litellm): resolve secret_ref (including exec:) in BuildEntry

BuildEntry wrote a SecretRef:true provider's auth.secret_ref
verbatim into config.yaml's api_key, so the new exec: form was
silently unsupported for LiteLLM-routed models — LiteLLM would send
the literal "exec:..." string as a bearer token, a confusing 401
with no error surfaced anywhere.

completes plan item #3 (docs/superpowers/plans/2026-09-23-code-review-fixes.md)
EOF
)"
```

---

### Task 4: Scope `pi_models.go` secret-resolution failures to the launch target

Fixes findings: **#4** (one broken provider's `exec:` helper aborts every launch, even unrelated ones), **#5** (which provider's error is reported is nondeterministic, from Go map iteration order), and **#8** (verbose `var`+assign vs. the sibling branch's `:=`).

**Files:**
- Modify: `internal/agents/agents.go:65-70` (`Syncer` interface)
- Modify: `internal/agents/agents.go:324-328` (`BuildLaunchCmd`'s `Syncer` call site)
- Modify: `internal/agents/pi.go:18-26` (`piDriver.SyncModels`)
- Modify: `internal/agents/pi_models.go:129-172` (`syncModels`), `:265-321` (`syncDirectProviders` doc comment + signature + loop)
- Test: `internal/agents/pi_models_test.go` (22 call-site updates + 1 test rewrite + 1 new test)

**Interfaces:**
- Consumes: nothing new.
- Produces: `Syncer.SyncModels(cfg *config.Config, target config.Model) error` — the model being launched, already in scope at `BuildLaunchCmd`'s only call site. `syncModels(cfg *config.Config, path string, target config.Model) error` and `syncDirectProviders(cfg *config.Config, f piModelsFile, target config.Model) (bool, error)` thread it through. `target` may be the zero `config.Model{}` when there is no specific launch (no test needs this today, but it must not panic: `target.ProviderID == ""` simply never matches a real provider id, so no provider is ever treated as "the target").

- [x] **Step 1: Update the `Syncer` interface and its one caller**

In `internal/agents/agents.go`, change:

```go
// Syncer is an optional Driver capability: a pre-launch step that needs the
// full config (e.g. pi syncing its model catalog). Launch paths call it once
// before Build.
type Syncer interface {
	SyncModels(cfg *config.Config) error
}
```

to:

```go
// Syncer is an optional Driver capability: a pre-launch step that needs the
// full config (e.g. pi syncing its model catalog). Launch paths call it once
// before Build. target is the model about to be launched, so an
// implementation can scope any per-provider, failure-prone work (e.g. pi's
// exec: secret_ref resolution) to the provider actually being launched
// instead of treating every registry provider as equally load-bearing for
// this one launch.
type Syncer interface {
	SyncModels(cfg *config.Config, target config.Model) error
}
```

And change the call site (line ~324-328):

```go
	if s, ok := d.(Syncer); ok {
		if err := s.SyncModels(cfg); err != nil {
			return nil, err
		}
	}
```

to:

```go
	if s, ok := d.(Syncer); ok {
		if err := s.SyncModels(cfg, m); err != nil {
			return nil, err
		}
	}
```

- [x] **Step 2: Update `piDriver.SyncModels`**

In `internal/agents/pi.go`:

```go
// SyncModels adds any non-native models from cfg that are missing from pi's
// models.json, so rotation-selected models are always available to pi.
func (piDriver) SyncModels(cfg *config.Config) error {
	path, err := piModelsPath()
	if err != nil {
		return err
	}
	return syncModels(cfg, path)
}
```

to:

```go
// SyncModels adds any non-native models from cfg that are missing from pi's
// models.json, so rotation-selected models are always available to pi.
// target is the model this launch is about to use (see Syncer's doc
// comment) — syncDirectProviders uses it to decide which provider's
// secret_ref failure, if any, should actually fail this launch.
func (piDriver) SyncModels(cfg *config.Config, target config.Model) error {
	path, err := piModelsPath()
	if err != nil {
		return err
	}
	return syncModels(cfg, path, target)
}
```

- [x] **Step 3: Update `syncModels` and `syncDirectProviders`**

In `internal/agents/pi_models.go`, change the `syncModels` signature (line 129) from `func syncModels(cfg *config.Config, path string) error` to `func syncModels(cfg *config.Config, path string, target config.Model) error`, and its direct-mode branch (lines 154-162):

```go
	} else {
		var err error
		var directMutated bool
		directMutated, err = syncDirectProviders(cfg, f)
		if err != nil {
			return err
		}
		mutated = directMutated || mutated
	}
```

to (also resolves finding #8's style nit):

```go
	} else {
		directMutated, err := syncDirectProviders(cfg, f, target)
		if err != nil {
			return err
		}
		mutated = directMutated || mutated
	}
```

Replace the `syncDirectProviders` doc comment's "A secret_ref resolution failure..." paragraph (lines 285-296):

```go
// A secret_ref resolution failure (e.g. a failing exec: credential helper)
// for ANY provider with models aborts the whole sync, not just the block
// for the provider actually being launched — because this loop resolves
// every provider's secret up front rather than only the launch target's.
// A registry with a second provider whose helper is broken (not logged in,
// etc.) will therefore fail launches against an unrelated, healthy
// provider too. Deliberately left this way for now (see the 2026-09-23
// nyt-litellm-provider plan/review): fixing it means threading "which
// provider is actually being launched" into this function, which today
// only sees the whole registry. Revisit if/when a second exec:-secured
// provider is added.
```

with:

```go
// A secret_ref resolution failure (e.g. a failing exec: credential helper)
// only aborts the sync when it belongs to target's own provider — the
// model actually being launched. Any other provider's broken helper is
// logged to stderr and that provider's block is left as-is (skipped this
// run, not written or resynced); the sync still succeeds for target's own
// provider and every other healthy one. target may be the zero Model (no
// specific launch in progress), in which case no provider is ever treated
// as the target and every failure is logged rather than fatal. Map
// iteration order over byProvider means, when multiple non-target
// providers fail in the same run, which one's warning prints first is
// unspecified — harmless now that no such failure can abort the sync.
```

And update the signature and the failure branch inside the loop (around lines 298-322):

```go
func syncDirectProviders(cfg *config.Config, f piModelsFile) (bool, error) {
	byProvider := map[string][]config.Model{}
	for _, m := range cfg.Models {
		if m.Native || m.ModelName == "" {
			continue
		}
		byProvider[m.ProviderID] = append(byProvider[m.ProviderID], m)
	}

	mutated := false
	for providerID, models := range byProvider {
		provider := cfg.ProviderByID(providerID)
		if provider == nil || provider.Auth.BaseURL == "" {
			continue
		}

		wantBaseURL := config.BaseOrigin(provider.Auth.BaseURL) + "/v1"
		wantAPIKey := defaultPiOllamaAPIKey
		if provider.Auth.SecretRef != "" {
			var err error
			wantAPIKey, err = config.ResolveSecret(provider.Auth.SecretRef)
			if err != nil {
				return false, fmt.Errorf("pi models.json sync: provider %q: %w", providerID, err)
			}
		}
```

to:

```go
func syncDirectProviders(cfg *config.Config, f piModelsFile, target config.Model) (bool, error) {
	byProvider := map[string][]config.Model{}
	for _, m := range cfg.Models {
		if m.Native || m.ModelName == "" {
			continue
		}
		byProvider[m.ProviderID] = append(byProvider[m.ProviderID], m)
	}

	mutated := false
	for providerID, models := range byProvider {
		provider := cfg.ProviderByID(providerID)
		if provider == nil || provider.Auth.BaseURL == "" {
			continue
		}

		wantBaseURL := config.BaseOrigin(provider.Auth.BaseURL) + "/v1"
		wantAPIKey := defaultPiOllamaAPIKey
		if provider.Auth.SecretRef != "" {
			var err error
			wantAPIKey, err = config.ResolveSecret(provider.Auth.SecretRef)
			if err != nil {
				if providerID == target.ProviderID {
					return false, fmt.Errorf("pi models.json sync: provider %q: %w", providerID, err)
				}
				fmt.Fprintf(os.Stderr, "wt: pi models.json sync: provider %q: %v (skipping — not the model being launched)\n", providerID, err)
				continue
			}
		}
```

(`os` is already imported in `pi_models.go`.)

- [x] **Step 4: Update test call sites**

In `internal/agents/pi_models_test.go`, replace every occurrence of `syncModels(cfg, path)` with `syncModels(cfg, path, config.Model{})` (22 occurrences — a plain find/replace, since none of those 22 tests exercise a specific-provider failure).

Then fix up `TestSyncModelsDirectPropagatesExecSecretError` (around line 394) to pass the failing provider as the target — it's testing exactly the "the model you're launching has a broken helper" case:

```go
	err := syncModels(cfg, path, config.Model{})
```

becomes:

```go
	err := syncModels(cfg, path, cfg.Models[0])
```

- [x] **Step 5: Write a new regression test for the fixed blast radius**

Add to `internal/agents/pi_models_test.go`:

```go
// TestSyncModelsDirectUnrelatedProviderFailureDoesNotAbort pins the fix for
// a real launch-blocking bug: a registry with two direct-mode providers,
// one broken (a failing exec: credential helper) and one healthy, must
// still let a launch of the healthy one's model succeed. Before this fix,
// syncDirectProviders resolved every provider's secret up front and
// aborted the whole sync on the first failure, so launching a healthy
// ollama model failed just because an unrelated, broken nyt-litellm
// provider was also in the registry.
func TestSyncModelsDirectUnrelatedProviderFailureDoesNotAbort(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	writeFile(t, path, emptyPiModels)

	scriptDir := t.TempDir()
	failScript := filepath.Join(scriptDir, "fail.sh")
	if err := os.WriteFile(failScript, []byte("#!/bin/sh\necho 'no key found' >&2\nexit 2\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Providers: []config.Provider{
			{ID: "ollama", Protocols: []config.Protocol{config.ProtocolAnthropic, config.ProtocolOpenAIChat}, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:11434"}},
			{ID: "nyt-litellm", Auth: config.AuthConfig{Type: "api_key", BaseURL: "https://llm-gateway.nyt.net", SecretRef: "exec:" + failScript}},
		},
		Models: []config.Model{
			{ID: "ollama/qwen3.8:27b-mlx", ModelName: "qwen3.8:27b-mlx", ProviderID: "ollama"},
			{ID: "nyt-litellm/claude-sonnet-4-6", ModelName: "claude-sonnet-4-6", ProviderID: "nyt-litellm"},
		},
	}

	// Launching the ollama model: nyt-litellm's broken helper must not
	// abort the sync.
	target := cfg.Models[0]
	if err := syncModels(cfg, path, target); err != nil {
		t.Fatalf("syncModels: want nil (unrelated provider's failure must not abort), got %v", err)
	}
	f := readPiModels(t, path)
	if _, ok := f.Providers["ollama"]; !ok {
		t.Error("ollama provider block missing — the launch target's own sync must still succeed")
	}
	if _, ok := f.Providers["nyt-litellm"]; ok {
		t.Error("nyt-litellm block written despite its secret_ref failing — should be skipped, not written")
	}
}
```

- [x] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/agents/... -v`
Expected: PASS, including `TestSyncModelsDirectPropagatesExecSecretError` (still errors, now because its target IS the failing provider) and the new `TestSyncModelsDirectUnrelatedProviderFailureDoesNotAbort`.

- [x] **Step 7: Run the whole module's build/vet/tests**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: clean (the `Syncer` interface change has exactly one implementer — `pi.go` — and one caller — `agents.go` — both already updated).

- [x] **Step 8: Commit**

```bash
git add internal/agents/agents.go internal/agents/pi.go internal/agents/pi_models.go internal/agents/pi_models_test.go
git commit -m "$(cat <<'EOF'
fix(agents): scope pi sync secret failures to the launch target

syncDirectProviders resolved every registry provider's secret_ref
up front and aborted the whole pi models.json sync on the first
failure — so a broken exec: helper on one provider blocked launches
against a completely unrelated, healthy provider too. Threading the
launch target model through Syncer.SyncModels lets it fail only
when the failing provider is the one actually being launched; any
other provider's failure is logged and its block left unsynced
this run. This also removes the prior nondeterminism (map iteration
order deciding which of several failures got reported).

completes plan item #4 (docs/superpowers/plans/2026-09-23-code-review-fixes.md)
EOF
)"
```

---

### Task 5: Resolve model-picker routes concurrently across rows

Mitigates finding: **#1** (the TUI can block in `Update()` for up to ~17s resolving a slow/hung `exec:` secret while building the model picker table).

**Scope note (read before starting):** This does **not** eliminate the block — `enterModelPhase` already runs a synchronous local-model inventory probe on the same code path (`app.go:840`, comment: "probing here would freeze the picker on each retry" — that comment is about `refreshTable`'s *re*-probe, but the *initial* `enterModelPhase` probe is synchronous too), so blocking on initial entry is an existing, accepted pattern in this codebase, not something this task can undo without a larger async-table-build redesign (out of scope for a review-fix pass). What this task fixes is the specific failure mode the review called out: **N distinct `exec:`-secured providers currently sum their timeouts** (provider A's 15s hang fully blocks provider B's otherwise-instant lookup behind it). After this task, the wall-clock cost of resolving a table's routes is bounded by the single slowest row, not the sum — using the per-ref concurrency Task 1 already made safe.

**Files:**
- Modify: `internal/tui/modeltable.go:74-159` (`renderTable`)
- Test: `internal/tui/modeltable_test.go` (find the existing route-resolution test fixture — search first)

**Interfaces:**
- Consumes: `cfg.ResolveRoute(m config.Model, agentProtocols []Protocol) (config.Route, error)` (unchanged) — now called from goroutines instead of the main loop; `internal/config`'s `ResolveRoute`/`ResolveSecret` do not mutate `cfg` (read-only), so concurrent calls against one shared `*config.Config` are safe.
- Produces: `renderTable`'s external behavior (returned `modelTable`) is unchanged — this is a pure internal-implementation change. No new exported symbols.

- [x] **Step 1: Locate existing route-resolution tests**

Run: `grep -n 'func Test' internal/tui/modeltable_test.go internal/tui/*_test.go | grep -i route`

Read whichever test(s) already exercise `renderTable`'s route-resolution branch (the `(not in LiteLLM)` / `(unavailable)` / `(via proxy)` cases) to match its `tableInput`/`cfg` construction style before writing a new one.

- [x] **Step 2: Write the failing test**

Add to `internal/tui/modeltable_test.go` (adapt the `cfg`/row construction to match whatever helper the existing route tests use — this is illustrative, not literal, since the exact fixture helper name is unknown until Step 1):

```go
// TestRenderTableResolvesRoutesConcurrently pins that renderTable resolves
// different rows' routes in parallel rather than one at a time: a slow
// provider (e.g. a hung exec: secret_ref helper) must not add its own
// latency on top of every other row's otherwise-fast resolution. Before
// this fix, a sequential per-row loop meant N distinct slow providers
// summed their costs into renderTable's total wall-clock time.
func TestRenderTableResolvesRoutesConcurrently(t *testing.T) {
	scriptDir := t.TempDir()
	slowScript := filepath.Join(scriptDir, "slow.sh")
	if err := os.WriteFile(slowScript, []byte("#!/bin/sh\nsleep 1\necho sk-slow\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Providers: []config.Provider{
			{ID: "slow-provider", Auth: config.AuthConfig{Type: "api_key", BaseURL: "http://localhost:9001", SecretRef: "exec:" + slowScript}},
			{ID: "slow-provider-2", Auth: config.AuthConfig{Type: "api_key", BaseURL: "http://localhost:9002", SecretRef: "exec:" + slowScript}},
		},
	}
	rows := []tableRow{
		{Model: config.Model{ID: "slow-provider/m1", ModelName: "m1", ProviderID: "slow-provider"}},
		{Model: config.Model{ID: "slow-provider-2/m2", ModelName: "m2", ProviderID: "slow-provider-2"}},
	}

	start := time.Now()
	renderTable(rows, cfg, "claude", nil, "")
	elapsed := time.Since(start)

	// Two distinct providers each with a ~1s exec: helper: sequential
	// resolution would take >=2s, parallel resolution ~1s.
	if elapsed > 1500*time.Millisecond {
		t.Errorf("renderTable took %v resolving 2 distinct 1s-slow providers, want ~1s (parallel), not ~2s (sequential)", elapsed)
	}
}
```

Adjust imports (`os`, `path/filepath`, `time`) and the `tableRow`/`config.Provider` construction to match the package's actual field names — re-check `internal/tui/modelrows.go`'s `tableRow` struct definition before finalizing this test.

- [x] **Step 3: Run the test to verify it fails**

Run: `go test ./internal/tui/... -run TestRenderTableResolvesRoutesConcurrently -v`
Expected: FAIL (or takes ~2s, tripping the 1500ms assertion) against the current sequential implementation.

- [x] **Step 4: Implement parallel route resolution**

In `internal/tui/modeltable.go`, add near the top of `renderTable` (after the existing per-row `cost`/`fam`/`c1`/`c7`/`c30` slice setup, before the header-building loop) a pre-resolution pass, and simplify the per-row route lookup inside the existing loop to read from it.

Add a small result type near the top of the file (or just above `renderTable`):

```go
// resolvedRoute pairs one row's ResolveRoute result so it can be computed
// ahead of the per-row rendering loop.
type resolvedRoute struct {
	route config.Route
	err   error
}
```

Change:

```go
func renderTable(rows []tableRow, cfg *config.Config, agent string, refs map[string]int, lastID string) modelTable {
	cost := make([]string, len(rows))
```

to:

```go
func renderTable(rows []tableRow, cfg *config.Config, agent string, refs map[string]int, lastID string) modelTable {
	// Resolve every row's route up front, in parallel, instead of one at a
	// time in the rendering loop below: cfg.ResolveRoute may dial an exec:
	// secret_ref subprocess (up to execSecretTimeout) per distinct
	// provider, and resolving rows sequentially would let one slow/hung
	// provider's helper add its full cost on top of every other row's
	// otherwise-fast resolution. config.ResolveSecret memoizes per ref with
	// its own per-ref locking (internal/config), so concurrent rows for the
	// SAME provider still only pay for one subprocess run; this only
	// parallelizes across DISTINCT providers. All goroutines are joined
	// (wg.Wait) before this function returns, so there is nothing left
	// running in the background afterward.
	var routes []resolvedRoute
	if cfg != nil {
		routes = make([]resolvedRoute, len(rows))
		var wg sync.WaitGroup
		for i, r := range rows {
			wg.Add(1)
			go func(i int, m config.Model) {
				defer wg.Done()
				route, err := cfg.ResolveRoute(m, agents.ProtocolsFor(agent))
				routes[i] = resolvedRoute{route: route, err: err}
			}(i, r.Model)
		}
		wg.Wait()
	}

	cost := make([]string, len(rows))
```

Then inside the existing per-row loop, change:

```go
		if cfg != nil {
			route, err := cfg.ResolveRoute(r.Model, agents.ProtocolsFor(agent))
			switch {
```

to:

```go
		if cfg != nil {
			route, err := routes[i].route, routes[i].err
			switch {
```

Add `"sync"` to the import block in `internal/tui/modeltable.go`.

- [x] **Step 5: Run the test to verify it passes**

Run: `go test ./internal/tui/... -run TestRenderTableResolvesRoutesConcurrently -v`
Expected: PASS, well under 1.5s.

- [x] **Step 6: Run the whole TUI package's tests**

Run: `go test ./internal/tui/... -v`
Expected: PASS — every pre-existing `renderTable`/`buildTable` test (the `(not in LiteLLM)`/`(unavailable)`/`(via proxy)` cases, empty-`cfg` skip-route case) is unaffected since per-row output is identical, only computed earlier and in parallel.

- [x] **Step 7: Run the whole module's build/vet/tests**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: clean.

- [x] **Step 8: Commit**

```bash
git add internal/tui/modeltable.go internal/tui/modeltable_test.go
git commit -m "$(cat <<'EOF'
perf(tui): resolve model-picker routes in parallel across rows

renderTable resolved each row's route one at a time, so N distinct
exec:-secured providers summed their subprocess timeouts into the
model picker's build time. Resolving all rows' routes up front, in
goroutines joined before the function returns, bounds the cost to
the single slowest provider instead. Does not eliminate the
existing synchronous block on initial model-phase entry (the local
inventory probe there is unconditionally synchronous too) — full
elimination needs an async table-build redesign, out of scope here.

completes plan item #5 (docs/superpowers/plans/2026-09-23-code-review-fixes.md)
EOF
)"
```

---

## Final verification (after all 5 tasks)

```bash
cd wt
go build ./...
go vet ./...
go test ./...
```

All must pass with zero failures before considering this plan complete.
