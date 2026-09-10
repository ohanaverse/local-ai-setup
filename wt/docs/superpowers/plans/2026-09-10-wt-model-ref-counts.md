# wt model ref counts implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Show a single-digit "in use" count in the model picker's leftmost column, sourced from a new `internal/refcount` package that tracks live `wt` sessions by pid, so a user can see at a glance that a model is already being driven by another terminal.

**Architecture:** A new `internal/refcount` package mirrors `internal/usage`'s store/lock/atomic-write shape (JSONL state file + sidecar flock), but tracks live-session state (keyed by pid, no retention window) instead of 30-day history. A sweep call at the top of `runLaunchPath` (the single funnel every launch goes through, including `shell-wt`) drops dead-pid entries; a record call at each launch path's existing commit point (next to `rotation.Record`) appends this process's pid + launched model; `buildModelItems` queries `Counts` in the same pass it already uses for usage counts and renders a 2-rune ref column before the existing rotation marker.

**Tech Stack:** Go 1.26.7, `encoding/json` JSONL, `syscall.Flock`/`syscall.Kill` (mirrors `internal/usage`), `testing`.

**Spec:** `wt/docs/superpowers/specs/2026-09-10-wt-model-ref-counts-design.md`

## Global Constraints

- State file `~/.config/agent-wt/refcount.jsonl`, one entry per line: `{"pid":12345,"model_id":"ollama/qwen3.8:27b-mlx"}`. No retention window — an entry lives until a sweep finds its pid dead.
- `Store` interface: `Record(pid int, modelID string) error`, `Sweep() error`, `Counts(modelIDs []string) map[string]int` (zero-filling every requested ID).
- Pid liveness seam `var pidAlive = func(pid int) bool` wrapping `syscall.Kill(pid, 0)`: `nil` or `EPERM` → alive; `ESRCH` (or any other error) → dead.
- Concurrency: sidecar `refcount.jsonl.lock` + POSIX `flock`, mirroring `usage.Record`.
- Sweep runs once at the top of `runLaunchPath` (`cmd/wt/main.go`) — before the guard install, unconditionally, for every launch path including `shell-wt`. Best-effort: errors ignored.
- Record runs at the existing single commit point in each launch path (TUI: `launchAndRecord`; non-TUI: `launchFilteredImpl`), using `os.Getpid()` + the launched model's `ID`. Best-effort, mirroring `rotation.Record`.
- Command agents (`shell`) never record — no explicit guard needed; they already take an early-return path before the record point in both launch paths.
- `buildModelItems` gains a `refcount.Store` param; `Title()` prepends a 2-char ref column **before** the existing rotation marker; both stay out of `.line` (fuzzy-match/`FilterValue` unaffected). Count clamps at `9`.
- **No** launch gating, warnings, or behavior changes — display-only. **No** changes to `usage`, `rotation`, `survey`, config, or the registry.

---

## File structure

- **Create:** `wt/internal/refcount/refcount.go` — `Store` interface, `StoreImpl`, `Record`/`Sweep`/`Counts`, `pidAlive` seam.
- **Create:** `wt/internal/refcount/refcount_test.go` — unit tests via `NewStoreAt(dir)` + injected `pidAlive`.
- **Modify:** `wt/cmd/wt/main.go` — `sweepRefcounts` seam + call at the top of `runLaunchPath`.
- **Modify:** `wt/cmd/wt/main_test.go` — extend `TestRunLaunchPath` to assert the sweep runs for every branch.
- **Modify:** `wt/internal/tui/app.go` — `launchAndRecord` records a refcount entry; `enterModelPhase` passes a refcount store to `buildModelItems`.
- **Modify:** `wt/internal/tui/agent_model_test.go` — new refcount-record test; `phaseModelWithList`'s `buildModelItems` call gains the new param.
- **Modify:** `wt/cmd/wt/launch.go` — `launchFilteredImpl` records a refcount entry next to `rotation.Record`.
- **Modify:** `wt/cmd/wt/launch_test.go` — new refcount-record and command-agent-skip tests.
- **Modify:** `wt/internal/tui/model_list.go` — `modelItem.ref`, `refColumn`, `Title()` prefix, `buildModelItems` signature.
- **Modify:** `wt/internal/tui/testhelpers_test.go`, `wt/internal/tui/model_family_test.go`, `wt/internal/tui/model_line_test.go`, `wt/internal/tui/model_list_test.go` — update all `buildModelItems` call sites; add ref-column rendering tests.
- **Modify:** `wt/CLAUDE.md` — package table row + package list (Task 1); rotation-section bullet documenting the ref column (Task 4).
- **No changes:** `internal/usage`, `internal/rotation`, `internal/survey`, `internal/config`, the registry.

---

### Task 1: `internal/refcount` package

**Files:**
- Create: `wt/internal/refcount/refcount.go`
- Test: `wt/internal/refcount/refcount_test.go`
- Modify: `wt/CLAUDE.md`

**Interfaces:**
- Produces: `refcount.Store` interface (`Record(pid int, modelID string) error`, `Sweep() error`, `Counts(modelIDs []string) map[string]int`); `refcount.StoreImpl` with `refcount.NewStore() *StoreImpl` and `refcount.NewStoreAt(dir string) *StoreImpl` constructors. Consumed by Task 2 (`Sweep`), Task 3 (`Record`), Task 4 (`Counts`).

- [ ] **Step 1: Write the failing test file**

Create `wt/internal/refcount/refcount_test.go`:

```go
package refcount

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestRecordAndCounts verifies a recorded pid+model pair is reflected in
// Counts: the core "one session, one model, one count" path the model
// picker's ref column depends on. Counts is a pure read — liveness is
// Sweep's job — so no pidAlive stub is needed here.
func TestRecordAndCounts(t *testing.T) {
	dir := t.TempDir()
	store := NewStoreAt(dir)
	if err := store.Record(111, "ollama/gemma4:9b"); err != nil {
		t.Fatalf("Record: %v", err)
	}
	got := store.Counts([]string{"ollama/gemma4:9b"})
	if got["ollama/gemma4:9b"] != 1 {
		t.Fatalf("Counts = %d, want 1", got["ollama/gemma4:9b"])
	}
}

// TestCountsZeroFillsRequestedIDs verifies every requested model ID appears
// in the result even with no recorded sessions, so the picker's ref column
// renders blank (not a missing map key) for an unused model.
func TestCountsZeroFillsRequestedIDs(t *testing.T) {
	dir := t.TempDir()
	store := NewStoreAt(dir)
	got := store.Counts([]string{"unused"})
	if v, ok := got["unused"]; !ok || v != 0 {
		t.Fatalf("Counts[unused] = %d, ok=%v, want 0, true", v, ok)
	}
}

// TestCountsIgnoresUnrequestedModels verifies a recorded entry for a model
// outside the requested set never leaks into the result.
func TestCountsIgnoresUnrequestedModels(t *testing.T) {
	dir := t.TempDir()
	store := NewStoreAt(dir)
	if err := store.Record(111, "other/model"); err != nil {
		t.Fatalf("Record: %v", err)
	}
	got := store.Counts([]string{"wanted"})
	if len(got) != 1 || got["wanted"] != 0 {
		t.Fatalf("Counts = %v, want only {wanted: 0}", got)
	}
}

// TestCountsMultipleSessionsSameModel verifies two different pids recorded
// against the same model both count — this is what lets a second concurrent
// wt session show "2" instead of clobbering the first session's entry.
func TestCountsMultipleSessionsSameModel(t *testing.T) {
	dir := t.TempDir()
	store := NewStoreAt(dir)
	if err := store.Record(111, "ollama/gemma4:9b"); err != nil {
		t.Fatalf("Record pid 111: %v", err)
	}
	if err := store.Record(222, "ollama/gemma4:9b"); err != nil {
		t.Fatalf("Record pid 222: %v", err)
	}
	got := store.Counts([]string{"ollama/gemma4:9b"})
	if got["ollama/gemma4:9b"] != 2 {
		t.Fatalf("Counts = %d, want 2", got["ollama/gemma4:9b"])
	}
}

// TestSweepDropsDeadPids verifies Sweep removes an entry whose pid is
// reported dead by the injected pidAlive seam, and keeps a live one — the
// mechanism that fixes stale "in use" counts after a crashed or killed wt
// session, per the design's launch-time sweep.
func TestSweepDropsDeadPids(t *testing.T) {
	dir := t.TempDir()
	store := NewStoreAt(dir)

	old := pidAlive
	defer func() { pidAlive = old }()
	pidAlive = func(pid int) bool { return pid != 999 }

	seedRefcountFile(t, dir, entry{Pid: 999, ModelID: "dead/model"}, entry{Pid: 111, ModelID: "live/model"})

	if err := store.Sweep(); err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	raw, err := os.ReadFile(store.path())
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	got := string(raw)
	if strings.Contains(got, "dead/model") {
		t.Errorf("state still contains dead pid's entry: %q", got)
	}
	if !strings.Contains(got, "live/model") {
		t.Errorf("state dropped the live pid's entry: %q", got)
	}
}

// TestSweepIgnoresCorruptLines verifies a corrupt JSONL line is dropped
// rather than aborting the sweep or crashing the scanner, matching Sweep's
// best-effort contract (a stale-count risk, never a blocked launch).
func TestSweepIgnoresCorruptLines(t *testing.T) {
	dir := t.TempDir()
	store := NewStoreAt(dir)

	old := pidAlive
	defer func() { pidAlive = old }()
	pidAlive = func(int) bool { return true }

	if err := os.WriteFile(store.path(), []byte("not json\n"), 0o600); err != nil {
		t.Fatalf("seed corrupt line: %v", err)
	}
	if err := store.Record(111, "ok/model"); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := store.Sweep(); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	got := store.Counts([]string{"ok/model"})
	if got["ok/model"] != 1 {
		t.Fatalf("Counts = %d, want 1 (valid entry survives a corrupt sibling line)", got["ok/model"])
	}
}

// TestSweepMissingFileIsNoop verifies Sweep succeeds with no state file yet
// (fresh install) instead of erroring — the launch-time sweep call in
// runLaunchPath is unconditional and must never fail a launch.
func TestSweepMissingFileIsNoop(t *testing.T) {
	dir := t.TempDir()
	store := NewStoreAt(dir)
	if err := store.Sweep(); err != nil {
		t.Fatalf("Sweep on missing file: %v", err)
	}
}

// TestPidAliveDefaultDetectsCurrentProcess sanity-checks the real
// (unstubbed) pidAlive wrapper against the test's own pid, which is always
// alive during the test run — a cheap, deterministic check that the
// syscall.Kill(pid, 0) wrapper isn't inverted.
func TestPidAliveDefaultDetectsCurrentProcess(t *testing.T) {
	if !pidAlive(os.Getpid()) {
		t.Error("pidAlive(os.Getpid()) = false, want true (the running test process is alive)")
	}
}

// TestRecordLockFileCreated verifies Record creates the sidecar lock file
// in the same directory as refcount.jsonl, mirroring usage.Record's
// concurrency contract.
func TestRecordLockFileCreated(t *testing.T) {
	dir := t.TempDir()
	store := NewStoreAt(dir)
	if err := store.Record(111, "x"); err != nil {
		t.Fatalf("Record: %v", err)
	}
	lockPath := filepath.Join(dir, "refcount.jsonl.lock")
	info, err := os.Stat(lockPath)
	if err != nil {
		t.Fatalf("lock file missing at %s: %v", lockPath, err)
	}
	if info.IsDir() {
		t.Fatalf("lock path is a directory, want a file: %s", lockPath)
	}
}

// TestRecordSerializesConcurrentProcesses verifies the flock around Record's
// read-modify-write critical section prevents lost writes when N goroutines
// (in-process stand-in for N concurrent wt processes) call Record at the
// same time — without it, two goroutines could read the same starting state
// and each rewrite the file, silently dropping the other's entry.
func TestRecordSerializesConcurrentProcesses(t *testing.T) {
	dir := t.TempDir()
	store := NewStoreAt(dir)

	const n = 8
	var wg sync.WaitGroup
	wg.Add(n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			errs[i] = store.Record(1000+i, "model-x")
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("Record[%d]: %v", i, err)
		}
	}

	raw, err := os.ReadFile(store.path())
	if err != nil {
		t.Fatalf("read state file: %v", err)
	}
	scanner := bufio.NewScanner(strings.NewReader(string(raw)))
	lines := 0
	for scanner.Scan() {
		lines++
	}
	if lines != n {
		t.Fatalf("state file has %d lines, want %d (lost writes)", lines, n)
	}
}

// seedRefcountFile writes entries directly to dir/refcount.jsonl, bypassing
// Record, so Sweep tests can set up state without depending on Record.
func seedRefcountFile(t *testing.T, dir string, entries ...entry) {
	t.Helper()
	var data []byte
	for _, e := range entries {
		line, err := json.Marshal(e)
		if err != nil {
			t.Fatalf("marshal seed entry: %v", err)
		}
		data = append(data, append(line, '\n')...)
	}
	if err := os.WriteFile(filepath.Join(dir, "refcount.jsonl"), data, 0o600); err != nil {
		t.Fatalf("seed refcount file: %v", err)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd wt && go test ./internal/refcount/... -v`
Expected: build failure — `package refcount is not in std` / undefined `NewStoreAt`, `entry`, `pidAlive` (the package doesn't exist yet).

- [ ] **Step 3: Write the implementation**

Create `wt/internal/refcount/refcount.go`:

```go
// Package refcount tracks how many live wt sessions are currently using
// each model, for the model picker's "in use" display (issue #73).
package refcount

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// entry is one line in the refcount JSONL file: one live wt process pid
// paired with the model it launched. Unlike usage.jsonl (30-day retained
// history), refcount.jsonl has no retention window — an entry lives until
// Sweep finds its pid dead.
type entry struct {
	Pid     int    `json:"pid"`
	ModelID string `json:"model_id"`
}

// Store is an interface for recording and querying live-session model usage.
type Store interface {
	Record(pid int, modelID string) error
	Sweep() error
	Counts(modelIDs []string) map[string]int
}

// StoreImpl reads and appends to the refcount state file.
type StoreImpl struct {
	dir string
}

func NewStore() *StoreImpl {
	return NewStoreAt(config.Dir())
}

func NewStoreAt(dir string) *StoreImpl {
	return &StoreImpl{dir: dir}
}

func (s *StoreImpl) path() string {
	return filepath.Join(s.dir, "refcount.jsonl")
}

func (s *StoreImpl) lockPath() string {
	return filepath.Join(s.dir, "refcount.jsonl.lock")
}

// pidAlive reports whether pid is a live process. nil or EPERM (process
// exists but is owned by another user) means alive; ESRCH — or any other
// error — means dead. Package-level var so tests can inject live/dead pids
// without spawning real processes.
var pidAlive = func(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// withLock runs fn while holding an exclusive POSIX flock on a sidecar
// refcount.jsonl.lock file, serializing Record/Sweep's read-modify-write
// critical sections across concurrent wt processes. Mirrors usage.Record.
func (s *StoreImpl) withLock(fn func() error) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	lock, err := os.OpenFile(s.lockPath(), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	return fn()
}

// Record appends one live-session entry for pid+modelID. Best-effort at the
// call site (see cmd/wt/launch.go and internal/tui/app.go): a failure here
// means a stale/undercounted "in use" column, never a blocked launch.
func (s *StoreImpl) Record(pid int, modelID string) error {
	e := entry{Pid: pid, ModelID: modelID}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	line = append(line, '\n')

	return s.withLock(func() error {
		existing, _ := os.ReadFile(s.path())
		return config.WriteFileAtomic(s.path(), append(existing, line...), 0o600)
	})
}

// Sweep drops every entry whose pid is no longer alive and rewrites the
// state file. Called once at the top of every launch (runLaunchPath) so
// counts stay accurate after a crashed or killed wt session. A corrupt line
// is dropped along with dead entries — Sweep favors a clean file over
// surfacing a parse error, since callers treat it as best-effort.
func (s *StoreImpl) Sweep() error {
	return s.withLock(func() error {
		data, err := os.ReadFile(s.path())
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		var out []byte
		scanner := bufio.NewScanner(bytes.NewReader(data))
		for scanner.Scan() {
			line := scanner.Bytes()
			var e entry
			if err := json.Unmarshal(line, &e); err != nil {
				continue
			}
			if !pidAlive(e.Pid) {
				continue
			}
			out = append(out, line...)
			out = append(out, '\n')
		}
		if err := scanner.Err(); err != nil {
			return err
		}
		return config.WriteFileAtomic(s.path(), out, 0o600)
	})
}

// Counts returns the live-session count per model in modelIDs, zero-filling
// every requested ID. It is a pure read: liveness is only re-checked by
// Sweep, not here, so a caller that wants fresh counts must have swept
// first (runLaunchPath does, before the picker is built).
func (s *StoreImpl) Counts(modelIDs []string) map[string]int {
	out := make(map[string]int, len(modelIDs))
	want := make(map[string]bool, len(modelIDs))
	for _, id := range modelIDs {
		out[id] = 0
		want[id] = true
	}

	f, err := os.Open(s.path())
	if err != nil {
		return out
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var e entry
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			continue
		}
		if !want[e.ModelID] {
			continue
		}
		out[e.ModelID]++
	}
	return out
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd wt && go test ./internal/refcount/... -v`
Expected: PASS (all cases above).

- [ ] **Step 5: Document the new package in `wt/CLAUDE.md`**

In the package table (around the `internal/usage/` row), add a row right after it:

```
| `internal/refcount/` | live-session "in use" model counts: JSONL state file keyed by pid, swept for dead pids on every launch, recorded at each launch path's commit point, consumed by the model picker's ref column |
```

In the `Package list:` line, add `refcount` to the brace list:

```
Package list: `internal/{config,rotation,usage,survey,refcount,agents,guard,worktree,initseed,session,themes,tui,configeditor,ollamacheck}`, `cmd/wt`. Run `grep -c '^func Test' <pkg>/*_test.go` for current counts — each test's focus is documented in its own `//` comment (see above).
```

- [ ] **Step 6: Commit**

```bash
cd wt && go vet ./internal/refcount/...
git add internal/refcount/refcount.go internal/refcount/refcount_test.go CLAUDE.md
git commit -m "$(cat <<'EOF'
feat(wt): add internal/refcount package for live-session model counts

Mirrors internal/usage's store/lock/atomic-write shape but tracks
live pid-keyed session state (no retention window) instead of 30-day
history. Record/Sweep/Counts + a pidAlive test seam.

Completes plan item: internal/refcount package (issue #73)

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01VhkWs2JKfoSgd7ZVQC9zsJ
EOF
)"
```

---

### Task 2: Sweep on launch (`cmd/wt/main.go`)

**Files:**
- Modify: `wt/cmd/wt/main.go`
- Modify: `wt/cmd/wt/main_test.go`

**Interfaces:**
- Consumes: `refcount.NewStore() *refcount.StoreImpl` and its `.Sweep() error` method (Task 1).
- Produces: `sweepRefcounts` package-level `func()` seam in `cmd/wt` (mirrors `maybeInstallGuard`) — no other task depends on it, but establishes the pattern used by Task 3's tests to isolate real launches.

- [ ] **Step 1: Write the failing test**

In `wt/cmd/wt/main_test.go`, extend `TestRunLaunchPath` (around line 607) to stub and assert `sweepRefcounts`. Replace the whole function body with:

```go
func TestRunLaunchPath(t *testing.T) {
	repo := t.TempDir()
	if err := exec.Command("git", "-C", repo, "init", "-q").Run(); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Agents: []config.Agent{{Name: "shell", SupportedProviders: nil}},
	}
	a := &app{cfg: cfg}

	cases := []struct {
		name       string
		agent      string
		pinned     string
		launchPath string
		root       string
		wantPath   string
		wantTUI    bool // true: expect tuiRun; false: expect launchFiltered
		wantGuard  bool // true: expect maybeInstallGuard called
	}{
		{"cwd", "shell", "", repo, repo, repo, false, true},
		{"outside", "shell", "", ".", "", ".", false, false},
		{"tui", "shell", "", "", "", "", true, false},
		{"tui-pinned-model", "claude", "ollama/x", "", "", "", true, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Stub tuiRun, launchFiltered, maybeInstallGuard, and
			// sweepRefcounts to capture the dispatched path, guard
			// behavior, and refcount sweep — the sweep must fire on every
			// branch (including "shell", which never builds a picker),
			// since it is the single funnel every launch goes through.
			oldTUI := tuiRun
			oldLaunch := launchFiltered
			oldGuard := maybeInstallGuard
			oldSweep := sweepRefcounts
			var gotPath string
			var gotTUI, gotLaunch, gotGuard, gotSweep bool
			tuiRun = func(bool, string, string, string, string, []string, themes.Theme, string, *config.Config) error {
				gotTUI = true
				gotPath = c.launchPath
				return nil
			}
			launchFiltered = func(agent, worktreePath string, cfg *config.Config, yolo bool, tags, family, pinned string, pinnedSupplied bool, extraArgs []string, eligible []config.Model) error {
				gotLaunch = true
				gotPath = worktreePath
				return nil
			}
			maybeInstallGuard = func() { gotGuard = true }
			sweepRefcounts = func() { gotSweep = true }
			defer func() {
				tuiRun = oldTUI
				launchFiltered = oldLaunch
				maybeInstallGuard = oldGuard
				sweepRefcounts = oldSweep
			}()

			cmd := &cobra.Command{}
			cmd.Flags().StringP("model", "M", "", "")
			cmd.Flags().Bool("yolo", false, "")

			err := runLaunchPath(cmd, a, c.agent, c.pinned, "", "", nil, c.launchPath, c.root)
			if err != nil {
				t.Fatalf("runLaunchPath: %v", err)
			}
			if gotPath != c.wantPath {
				t.Errorf("dispatched path = %q, want %q", gotPath, c.wantPath)
			}
			if gotTUI != c.wantTUI {
				t.Errorf("tuiRun called = %v, want %v", gotTUI, c.wantTUI)
			}
			if gotLaunch != !c.wantTUI {
				t.Errorf("launchFiltered called = %v, want %v", gotLaunch, !c.wantTUI)
			}
			if gotGuard != c.wantGuard {
				t.Errorf("maybeInstallGuard called = %v, want %v", gotGuard, c.wantGuard)
			}
			if !gotSweep {
				t.Error("sweepRefcounts not called; the sweep must run on every runLaunchPath branch, including shell-wt")
			}
		})
	}
}
```

(Only the `sweepRefcounts` stub/assert lines and their doc comment are new; the rest reproduces the existing test body so the diff is additive.)

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd wt && go test ./cmd/wt/... -run TestRunLaunchPath -v`
Expected: build failure — `undefined: sweepRefcounts`.

- [ ] **Step 3: Add the seam and wire the sweep call**

In `wt/cmd/wt/main.go`, add the import:

```go
	"github.com/ohanaverse/local-ai-setup/wt/internal/refcount"
```

(inserted alphabetically among the existing `internal/*` imports, after `internal/initseed` and before `internal/session`).

Add the seam right after the `tuiRun` var declaration (near the top of the file):

```go
// sweepRefcounts is a seam for tests: production sweeps the live-session
// refcount state at the top of every launch; tests stub it to observe the
// call without touching a real state file.
var sweepRefcounts = realSweepRefcounts

// realSweepRefcounts drops dead-pid entries from refcount.jsonl (issue
// #73). Best-effort: a sweep failure means stale "in use" counts, never a
// blocked launch.
func realSweepRefcounts() {
	_ = refcount.NewStore().Sweep()
}
```

Modify `runLaunchPath` to call it first, before the guard install:

```go
func runLaunchPath(
	cmd *cobra.Command,
	a *app,
	agent, pinned, tags, family string,
	args []string,
	launchPath, root string,
) error {
	// Sweep dead-pid entries from the live-session refcount state before
	// anything else — this is the single funnel every launch goes through
	// (TUI, non-TUI, -W, --cwd, outside-repo, and shell-wt), and the sweep
	// must run even for shell-wt (which never builds a model picker) to
	// keep other concurrent sessions' counts accurate. Best-effort.
	sweepRefcounts()

	// Install the guard once when inside any git repo.
	if root != "" {
		maybeInstallGuard()
	}
```

(the rest of the function body is unchanged).

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd wt && go test ./cmd/wt/... -run TestRunLaunchPath -v`
Expected: PASS for all four subtests.

- [ ] **Step 5: Run the full `cmd/wt` test package to catch any other break**

Run: `cd wt && go test ./cmd/wt/... -v 2>&1 | tail -40`
Expected: PASS (no other test calls `runLaunchPath` directly, so no further breaks).

- [ ] **Step 6: Commit**

```bash
cd wt && go vet ./cmd/wt/...
git add cmd/wt/main.go cmd/wt/main_test.go
git commit -m "$(cat <<'EOF'
feat(wt): sweep dead-pid refcount entries at the top of every launch

runLaunchPath is the single funnel every launch goes through (TUI,
non-TUI, -W, --cwd, outside-repo, and shell-wt), so the sweep is an
explicit startup call rather than hidden in buildModelItems' read
path — shell-wt never builds a picker, so a lazy sweep would never
fire for it.

Completes plan item: sweep on launch (issue #73)

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01VhkWs2JKfoSgd7ZVQC9zsJ
EOF
)"
```

---

### Task 3: Record on launch (TUI + non-TUI)

**Files:**
- Modify: `wt/internal/tui/app.go`
- Modify: `wt/internal/tui/agent_model_test.go`
- Modify: `wt/cmd/wt/launch.go`
- Modify: `wt/cmd/wt/launch_test.go`

**Interfaces:**
- Consumes: `refcount.NewStore() *refcount.StoreImpl`'s `.Record(pid int, modelID string) error` and `refcount.NewStoreAt(dir string) *StoreImpl`'s `.Counts([]string) map[string]int` (Task 1, for test verification).
- Produces: no new public API — pins the two commit-point call sites so recorded data exists for Task 4's `Counts` query to find in an end-to-end run.

- [ ] **Step 1: Write the failing TUI test**

In `wt/internal/tui/agent_model_test.go`, add the import:

```go
	"github.com/ohanaverse/local-ai-setup/wt/internal/refcount"
```

(alphabetically among the existing `internal/*` imports).

Add this test right after `TestLaunchAndRecordWritesLast`:

```go
// TestLaunchAndRecordWritesRefcount asserts that launchAndRecord also
// records a live-session refcount entry (this test process's pid + the
// launched model), so the picker's "in use" column can reflect a
// concurrent wt session. This is the TUI-side counterpart to
// TestLaunchAndRecordWritesLast, verifying the second state write at the
// same commit point.
func TestLaunchAndRecordWritesRefcount(t *testing.T) {
	dir := tempStateDir(t)
	m := phaseModelWithList(t, testConfig(), "claude", "code")
	first, ok := m.models.Items()[0].(*modelItem)
	if !ok {
		t.Fatalf("items[0] is %T, want *modelItem", m.models.Items()[0])
	}
	m.launchModel = first.model
	m, _ = m.launchAndRecord(exec.Command("true"))

	got := refcount.NewStoreAt(dir).Counts([]string{first.model.ID})
	if got[first.model.ID] != 1 {
		t.Fatalf("refcount Counts = %d, want 1", got[first.model.ID])
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd wt && go test ./internal/tui/... -run TestLaunchAndRecordWritesRefcount -v`
Expected: FAIL — `refcount Counts = 0, want 1` (nothing records yet).

- [ ] **Step 3: Record in `launchAndRecord`**

In `wt/internal/tui/app.go`, add imports:

```go
	"os"

	"github.com/ohanaverse/local-ai-setup/wt/internal/refcount"
```

(`"os"` goes in the stdlib import block above `"os/exec"`; `refcount` goes alphabetically among the `internal/*` imports, after `internal/ollamacheck` and before `internal/rotation`).

Replace `launchAndRecord`:

```go
// launchAndRecord records the model as last-launched (so the next picker
// entry advances rotation), records a live-session refcount entry for the
// model picker's "in use" column, and then runs the agent. Recording
// happens here — the single commit point reached only after the ollama
// check and resume prompt have been satisfied — so a cancelled ollama
// warning, a cancelled resume prompt, or a failed ollama check never
// advances the rotation or the refcount. Both state writes are
// best-effort: a failure surfaces in m.status and the launch still
// proceeds.
func (m model) launchAndRecord(cmd *exec.Cmd) (model, tea.Cmd) {
	if err := rotation.New().Record(m.launchModel.ID); err != nil {
		m.status = "rotation state not saved: " + err.Error()
	}
	if err := refcount.NewStore().Record(os.Getpid(), m.launchModel.ID); err != nil && m.status == "" {
		m.status = "refcount state not saved: " + err.Error()
	}
	return m, runAndWaitCmd(cmd, m.agent, m.launchModel)
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd wt && go test ./internal/tui/... -run TestLaunchAndRecordWritesRefcount -v`
Expected: PASS.

- [ ] **Step 5: Write the failing non-TUI tests**

In `wt/cmd/wt/launch_test.go`, add the import:

```go
	"github.com/ohanaverse/local-ai-setup/wt/internal/refcount"
```

(alphabetically among the existing `internal/*` imports).

Add these two tests after `TestLaunchFilteredRotationRespectsTagFilter`:

```go
// TestLaunchFilteredRecordsRefcount verifies the non-TUI launch path
// records a live-session refcount entry (this process's pid + the launched
// model) at the same commit point as rotation.Record, so the picker's "in
// use" column can see a launch made through -W/--cwd/outside-repo, not
// just the TUI.
func TestLaunchFilteredRecordsRefcount(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("MODELMAN_REGISTRY", "")
	worktree := t.TempDir()

	binDir := t.TempDir()
	claudeBin := filepath.Join(binDir, "claude")
	if err := os.WriteFile(claudeBin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cfg := &config.Config{
		DefaultTag: "code",
		Providers: []config.Provider{
			{ID: "claude", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "native"}},
		},
		Models: []config.Model{
			{ID: "claude/opus", ProviderID: "claude", ModelName: "opus", Tags: []string{"code"}},
		},
		Agents: []config.Agent{
			{Name: "claude", SupportedProviders: []string{"claude"}},
		},
	}
	cfg.ExposeAllForTest()

	if err := launchFiltered("claude", worktree, cfg, false, "", "", "", false, nil, nil); err != nil {
		t.Fatalf("launchFiltered: %v", err)
	}

	store := refcount.NewStoreAt(filepath.Join(dir, "agent-wt"))
	got := store.Counts([]string{"claude/opus"})
	if got["claude/opus"] != 1 {
		t.Fatalf("refcount Counts = %d, want 1", got["claude/opus"])
	}
}

// TestCommandAgentDoesNotRecordRefcount verifies a shell (command agent)
// launch never writes a refcount entry — command agents have no model
// layer, so there is nothing to attribute an "in use" count to. Command
// agents take the early-return path in launchFilteredImpl, before the
// record call, so this pins that control-flow contract (the design notes
// no explicit guard is needed; this test is the regression lock for that
// claim).
func TestCommandAgentDoesNotRecordRefcount(t *testing.T) {
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skip("`true` not available")
	}
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("MODELMAN_REGISTRY", "")
	worktree := t.TempDir()

	cfg := &config.Config{
		Agents: []config.Agent{{Name: "shell", SupportedProviders: nil}},
	}

	if err := launchFiltered("shell", worktree, cfg, false, "", "", "", false, []string{truePath}, nil); err != nil {
		t.Fatalf("launchFiltered: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "agent-wt", "refcount.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("refcount.jsonl unexpectedly created for a command agent launch (err=%v)", err)
	}
}
```

- [ ] **Step 6: Run the tests to verify they fail**

Run: `cd wt/cmd/wt && go test -run 'TestLaunchFilteredRecordsRefcount|TestCommandAgentDoesNotRecordRefcount' -v`
Expected: `TestLaunchFilteredRecordsRefcount` FAILs (`refcount Counts = 0, want 1`); `TestCommandAgentDoesNotRecordRefcount` currently PASSes vacuously (no refcount writes exist at all yet) — that's fine, Step 8 re-runs both together as the real regression lock once recording exists.

- [ ] **Step 7: Record in `launchFilteredImpl`**

In `wt/cmd/wt/launch.go`, add the import:

```go
	"github.com/ohanaverse/local-ai-setup/wt/internal/refcount"
```

(alphabetically among the existing `internal/*` imports, after `internal/ollamacheck` and before `internal/rotation`).

In `launchFilteredImpl`, modify the block right before the final `return runAgentCmd(...)`:

```go
	cmd, berr := buildCommandForModel(agent, m, worktreePath, cfg, yolo, extraArgs)
	if berr != nil {
		return berr
	}
	if rerr := rotation.New().Record(m.ID); rerr != nil {
		fmt.Fprintf(os.Stderr, "note: rotation state not saved: %v\n", rerr)
	}
	if rerr := refcount.NewStore().Record(os.Getpid(), m.ID); rerr != nil {
		fmt.Fprintf(os.Stderr, "note: refcount state not saved: %v\n", rerr)
	}
	return runAgentCmd(cmd, agent, m)
```

- [ ] **Step 8: Run the tests to verify they pass**

Run: `cd wt/cmd/wt && go test -run 'TestLaunchFilteredRecordsRefcount|TestCommandAgentDoesNotRecordRefcount' -v`
Expected: PASS for both.

- [ ] **Step 9: Run the full `internal/tui` and `cmd/wt` packages**

Run: `cd wt && go test ./internal/tui/... ./cmd/wt/... -v 2>&1 | tail -60`
Expected: PASS.

- [ ] **Step 10: Commit**

```bash
cd wt && go vet ./internal/tui/... ./cmd/wt/...
git add internal/tui/app.go internal/tui/agent_model_test.go cmd/wt/launch.go cmd/wt/launch_test.go
git commit -m "$(cat <<'EOF'
feat(wt): record a live-session refcount entry at each launch commit point

Right next to rotation.Record in both the TUI (launchAndRecord) and
non-TUI (launchFilteredImpl) paths, using os.Getpid() + the launched
model's ID. Command agents (shell) already take an early-return path
before this point in both launch paths, so no explicit guard is
needed.

Completes plan item: record on launch (issue #73)

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01VhkWs2JKfoSgd7ZVQC9zsJ
EOF
)"
```

---

### Task 4: Display in the picker

**Files:**
- Modify: `wt/internal/tui/model_list.go`
- Modify: `wt/internal/tui/app.go` (production call site only)
- Modify: `wt/internal/tui/agent_model_test.go` (call site only, separate edit from Task 3's new test)
- Modify: `wt/internal/tui/testhelpers_test.go`
- Modify: `wt/internal/tui/model_family_test.go`
- Modify: `wt/internal/tui/model_line_test.go`
- Modify: `wt/internal/tui/model_list_test.go`
- Modify: `wt/CLAUDE.md`

**Interfaces:**
- Consumes: `refcount.Store` interface, `refcount.NewStore()`, `refcount.NewStoreAt(dir)` (Task 1).
- Produces: `buildModelItems(models []config.Model, familyOf map[string]string, s usage.Store, refStore refcount.Store, lastID string, stats map[string]survey.Stats) []*modelItem` (new signature — the `refcount.Store` param is inserted right after the existing `usage.Store` param); `modelItem.ref int` field; `refColumn(ref int) string` helper; `newRefcountStore` seam var of type `func() refcount.Store`.

This task's signature change must land in one commit — `buildModelItems` has no default parameter, so every call site changes atomically or the package doesn't compile. The steps below are ordered so each edit is small, but they are verified together at the end, not incrementally.

- [ ] **Step 1: Update `model_list.go` — the `refcount.Store` seam, `modelItem.ref`, and the ref column**

Add the import to `wt/internal/tui/model_list.go`:

```go
	"github.com/ohanaverse/local-ai-setup/wt/internal/refcount"
```

(alphabetically among the existing `internal/*` imports, after `internal/config` and before `internal/survey`).

Add the seam right after `realNewUsageStore`:

```go
// newRefcountStore is a seam for tests: production uses
// realNewRefcountStore (the default config dir); tests swap it to isolate
// from the real refcount.jsonl.
var newRefcountStore = realNewRefcountStore

// realNewRefcountStore is the production implementation of the
// newRefcountStore seam: a Store rooted at the default config dir.
func realNewRefcountStore() refcount.Store { return refcount.NewStore() }
```

Replace the `modelItem` struct and its doc comment:

```go
// modelItem adapts a config.Model to a list.Item for the model picker.
// The compact representation is baked onto .line; marked records the
// rotation's last-launched row and ref is the live "in use" session count
// (issue #73) — both are composed into the rendered prefix by Title()
// rather than baked into .line, so FilterValue (fuzzy matching) and every
// .line consumer see the unprefixed format.
type modelItem struct {
	model  config.Model
	line   string
	marked bool
	ref    int
}
```

Add the ref-column helper right after the `markerMarked`/`markerBlank` const block:

```go
// refClamp is the highest digit the ref column ever renders — a single
// glyph keeps the column width fixed regardless of how many concurrent
// sessions are actually using a model.
const refClamp = 9

// refColumn renders the ref count's 2-rune prefix: "<digit> " when ref > 0
// (clamped at refClamp), or two blank spaces when the model is unused.
// Kept the same width as markerMarked/markerBlank so every row's columns
// line up regardless of ref/marked state.
func refColumn(ref int) string {
	if ref <= 0 {
		return "  "
	}
	if ref > refClamp {
		ref = refClamp
	}
	return fmt.Sprintf("%d ", ref)
}
```

Replace `Title()`:

```go
// Title renders the compact one-line model representation with the ref
// column (issue #73's "in use" count) prepended before the last-launched
// marker prefix.
func (m modelItem) Title() string {
	prefix := refColumn(m.ref)
	if m.marked {
		return prefix + markerMarked + m.line
	}
	return prefix + markerBlank + m.line
}
```

- [ ] **Step 2: Update `buildModelItems`'s signature and body**

Replace the doc comment and signature line:

```go
// buildModelItems returns usage-sorted items for the model picker,
// computing a compact one-line representation for each model that
// includes family context and usage counts. familyOf maps the FULL
// catalog's model IDs to families so family totals are accurate even
// when tags or families narrow the eligible slice. refStore supplies the
// live "in use" session count (issue #73) rendered as Title()'s leading
// ref column; it is queried over the same full-catalog IDs as the usage
// counts, in the same pass.
func buildModelItems(models []config.Model, familyOf map[string]string, s usage.Store, refStore refcount.Store, lastID string, stats map[string]survey.Stats) []*modelItem {
```

Right after `familyCounts := usage.AggregateByFamily(familyOf, modelCounts)`, add:

```go
	// One Counts pass over the same full-catalog IDs used for usage, so a
	// filtered (-T/-F) picker still shows accurate "in use" counts for
	// every row it renders.
	refCounts := refStore.Counts(catalogIDs)
```

In the `items = append(...)` call at the bottom of the loop, add the `ref` field:

```go
		items = append(items, &modelItem{
			model:  m,
			line:   line,
			marked: lastID != "" && m.ID == lastID,
			ref:    refCounts[m.ID],
		})
```

- [ ] **Step 3: Update the production call site in `app.go`**

In `wt/internal/tui/app.go`, change:

```go
	items := buildModelItems(models, familyOf, newUsageStore(), lastID, surveyStats)
```

to:

```go
	items := buildModelItems(models, familyOf, newUsageStore(), newRefcountStore(), lastID, surveyStats)
```

- [ ] **Step 4: Update the `phaseModelWithList` call site in `agent_model_test.go`**

Change:

```go
	items := buildModelItems(models, familyOf, newUsageStore(), lastID, nil)
```

to:

```go
	items := buildModelItems(models, familyOf, newUsageStore(), newRefcountStore(), lastID, nil)
```

(`phaseModelWithList` already requires an isolated `XDG_CONFIG_HOME` via `tempStateDir(t)`, which `newRefcountStore()`'s default `refcount.NewStore()` honors the same way `newUsageStore()` does — no extra stubbing needed.)

- [ ] **Step 5: Update the `compactModelList` call site in `testhelpers_test.go`**

Change:

```go
	items := buildModelItems(models, familyOf, newUsageStore(), "", nil)
```

to:

```go
	items := buildModelItems(models, familyOf, newUsageStore(), newRefcountStore(), "", nil)
```

- [ ] **Step 6: Update `model_family_test.go`'s four call sites**

Add the import:

```go
	"github.com/ohanaverse/local-ai-setup/wt/internal/refcount"
```

(alphabetically among the existing `internal/*` imports).

These four call sites don't isolate `XDG_CONFIG_HOME`, so pass a real `refcount.Store` rooted at a fresh temp dir directly (no seam needed — they don't touch the production call site).

Change (unique — only occurrence with `newUsageStore()` in this file):

```go
	items := buildModelItems(modelFamilies(), familyOfFor(), newUsageStore(), "", nil)
```

to:

```go
	items := buildModelItems(modelFamilies(), familyOfFor(), newUsageStore(), refcount.NewStoreAt(t.TempDir()), "", nil)
```

Change (use `replace_all` — this exact text appears twice, in `TestBuildModelItemsFamilyColumnShowsFamilyTotal` and `TestBuildModelItemsFamilyCountsUseFullCatalog`; both need the identical transformation):

```go
	items := buildModelItems(models, familyOfFor(), store, "", nil)
```

to:

```go
	items := buildModelItems(models, familyOfFor(), store, refcount.NewStoreAt(t.TempDir()), "", nil)
```

Change (unique — the `modelFamilies()` + `store` combination, in `TestBuildModelItemsEmptyFamilyShowsAggregate`):

```go
	items := buildModelItems(modelFamilies(), familyOfFor(), store, "", nil)
```

to:

```go
	items := buildModelItems(modelFamilies(), familyOfFor(), store, refcount.NewStoreAt(t.TempDir()), "", nil)
```

- [ ] **Step 7: Update `model_line_test.go`'s call site**

Add the import:

```go
	"github.com/ohanaverse/local-ai-setup/wt/internal/refcount"
```

Change:

```go
			items := buildModelItems(tt.models, familyOf, store, "", nil)
```

to:

```go
			items := buildModelItems(tt.models, familyOf, store, refcount.NewStoreAt(t.TempDir()), "", nil)
```

- [ ] **Step 8: Update `model_list_test.go`'s eight call sites, and add the ref-column tests**

Add the import:

```go
	"github.com/ohanaverse/local-ai-setup/wt/internal/refcount"
```

Change (in `TestModelItemDescriptionEmptyCountsInLine`):

```go
	}, map[string]string{"ollama/gemma4:9b": "gemma4"}, store, "", nil)
```

to:

```go
	}, map[string]string{"ollama/gemma4:9b": "gemma4"}, store, refcount.NewStoreAt(t.TempDir()), "", nil)
```

Change (in `TestModelItemLinePricingAfterUsageCounts`):

```go
	items := buildModelItems(models, map[string]string{
		"priced":   "test",
		"unpriced": "test",
	}, store, "", nil)
```

to:

```go
	items := buildModelItems(models, map[string]string{
		"priced":   "test",
		"unpriced": "test",
	}, store, refcount.NewStoreAt(t.TempDir()), "", nil)
```

Change (in `TestModelItemLinePartialPerTokenPricing`):

```go
	items := buildModelItems(models, map[string]string{"partial": "test"}, store, "", nil)
```

to:

```go
	items := buildModelItems(models, map[string]string{"partial": "test"}, store, refcount.NewStoreAt(t.TempDir()), "", nil)
```

Change (in `TestBuildModelItemsMarksLastLaunchedRow`):

```go
	items := buildModelItems(models, familyOf, store, "ollama/gemma4:14b", nil)
```

to:

```go
	items := buildModelItems(models, familyOf, store, refcount.NewStoreAt(t.TempDir()), "ollama/gemma4:14b", nil)
```

Change (in `TestBuildModelItemsAppendsSurveySegment`):

```go
	items := buildModelItems(models, familyOf, store, "", stats)
```

to:

```go
	items := buildModelItems(models, familyOf, store, refcount.NewStoreAt(t.TempDir()), "", stats)
```

Change (in `TestBuildModelItemsOmitsSurveySegmentWhenNoAnswered` — both lines together):

```go
	withoutStats := buildModelItems(models, familyOf, store, "", nil)
	withZeroStats := buildModelItems(models, familyOf, store, "", map[string]survey.Stats{"ollama/gemma4:9b": {}})
```

to:

```go
	withoutStats := buildModelItems(models, familyOf, store, refcount.NewStoreAt(t.TempDir()), "", nil)
	withZeroStats := buildModelItems(models, familyOf, store, refcount.NewStoreAt(t.TempDir()), "", map[string]survey.Stats{"ollama/gemma4:9b": {}})
```

Change (in `TestBuildModelItemsNoMarkerWithoutLastLaunched`):

```go
		items := buildModelItems(models, familyOf, store, lastID, nil)
```

to:

```go
		items := buildModelItems(models, familyOf, store, refcount.NewStoreAt(t.TempDir()), lastID, nil)
```

Add these four new tests at the end of the file, after `TestBuildModelItemsNoMarkerWithoutLastLaunched`:

```go
// TestBuildModelItemsRefColumnBlankWhenUnused verifies a model with zero
// live sessions renders no ref digit — Title()'s 4-rune prefix stays two
// blank ref-column spaces followed by the (also blank) marker.
func TestBuildModelItemsRefColumnBlankWhenUnused(t *testing.T) {
	store := &mockStore{counts: map[string]usage.UsageCounts{}}
	refStore := refcount.NewStoreAt(t.TempDir())
	models := []config.Model{{ID: "ollama/gemma4:9b", ProviderID: "ollama", Family: "gemma4", Location: config.LocationLocal}}
	familyOf := map[string]string{"ollama/gemma4:9b": "gemma4"}
	items := buildModelItems(models, familyOf, store, refStore, "", nil)
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	if got := items[0].Title(); !strings.HasPrefix(got, "    ") {
		t.Errorf("Title() = %q, want a 4-space blank prefix (no ref digit, no marker)", got)
	}
}

// TestBuildModelItemsRefColumnRendersDigit verifies a model with N live
// sessions (1 <= N <= 9) renders that exact digit as the first rune of
// Title(), ahead of the marker prefix.
func TestBuildModelItemsRefColumnRendersDigit(t *testing.T) {
	store := &mockStore{counts: map[string]usage.UsageCounts{}}
	dir := t.TempDir()
	refStore := refcount.NewStoreAt(dir)
	for pid := 1; pid <= 3; pid++ {
		if err := refStore.Record(pid, "ollama/gemma4:9b"); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}
	models := []config.Model{{ID: "ollama/gemma4:9b", ProviderID: "ollama", Family: "gemma4", Location: config.LocationLocal}}
	familyOf := map[string]string{"ollama/gemma4:9b": "gemma4"}
	items := buildModelItems(models, familyOf, store, refStore, "", nil)
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	if got := items[0].Title(); !strings.HasPrefix(got, "3   ") {
		t.Errorf("Title() = %q, want it to start with \"3   \" (ref digit, then blank marker)", got)
	}
}

// TestBuildModelItemsRefColumnClampsAtNine verifies a model with more than
// 9 live sessions still renders a single "9" — the design's fixed-width
// column would misalign if a two-digit count were ever rendered.
func TestBuildModelItemsRefColumnClampsAtNine(t *testing.T) {
	store := &mockStore{counts: map[string]usage.UsageCounts{}}
	dir := t.TempDir()
	refStore := refcount.NewStoreAt(dir)
	for pid := 1; pid <= 12; pid++ {
		if err := refStore.Record(pid, "ollama/gemma4:9b"); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}
	models := []config.Model{{ID: "ollama/gemma4:9b", ProviderID: "ollama", Family: "gemma4", Location: config.LocationLocal}}
	familyOf := map[string]string{"ollama/gemma4:9b": "gemma4"}
	items := buildModelItems(models, familyOf, store, refStore, "", nil)
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	if got := items[0].Title(); !strings.HasPrefix(got, "9   ") {
		t.Errorf("Title() = %q, want it clamped to \"9   \" for 12 live sessions", got)
	}
}

// TestBuildModelItemsRefColumnBeforeMarker verifies the ref digit and the
// last-launched marker compose correctly when both apply to the same row —
// "1 > " — matching the design's table (ref column, then the rotation
// marker, then the line).
func TestBuildModelItemsRefColumnBeforeMarker(t *testing.T) {
	store := &mockStore{counts: map[string]usage.UsageCounts{}}
	dir := t.TempDir()
	refStore := refcount.NewStoreAt(dir)
	if err := refStore.Record(111, "ollama/gemma4:9b"); err != nil {
		t.Fatalf("Record: %v", err)
	}
	models := []config.Model{{ID: "ollama/gemma4:9b", ProviderID: "ollama", Family: "gemma4", Location: config.LocationLocal}}
	familyOf := map[string]string{"ollama/gemma4:9b": "gemma4"}
	items := buildModelItems(models, familyOf, store, refStore, "ollama/gemma4:9b", nil)
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	if got := items[0].Title(); !strings.HasPrefix(got, "1 > ") {
		t.Errorf("Title() = %q, want it to start with \"1 > \" (ref digit before the last-launched marker)", got)
	}
}
```

- [ ] **Step 9: Run the full `internal/tui` package**

Run: `cd wt && go test ./internal/tui/... -v 2>&1 | tail -80`
Expected: PASS — every pre-existing test still compiles and passes with the new `buildModelItems` signature, and all four new ref-column tests pass.

- [ ] **Step 10: Document the ref column in `wt/CLAUDE.md`**

In the "Rotation (Go)" section, right after the bullet ending "...the cursor still lands on the rotation's next-to-use model.", add:

```
- The model picker's leftmost column shows a live "in use" session count
  (issue #73): `buildModelItems` queries `refcount.Store.Counts` over the
  same full-catalog IDs used for usage, in the same pass, and sets
  `modelItem.ref`; `Title()` renders it as a 2-rune prefix ("`3 `" or two
  blank spaces, clamped at 9) *before* the rotation marker. See
  `internal/refcount`.
```

- [ ] **Step 11: Run the full repo verification**

Run: `cd wt && go build ./... && go vet ./... && go test ./...`
Expected: all PASS, no build/vet errors anywhere in the module.

- [ ] **Step 12: Commit**

```bash
cd wt && git add internal/tui/model_list.go internal/tui/app.go internal/tui/agent_model_test.go \
  internal/tui/testhelpers_test.go internal/tui/model_family_test.go internal/tui/model_line_test.go \
  internal/tui/model_list_test.go CLAUDE.md
git commit -m "$(cat <<'EOF'
feat(wt): show a live "in use" session count in the model picker

buildModelItems gains a refcount.Store param, queried in the same
pass as usage counts over the full catalog. Title() prepends a 2-rune
ref column (a digit, clamped at 9, or blank) before the existing
rotation marker; both stay out of .line so fuzzy filtering is
unaffected. Display-only — no launch gating or warnings.

Completes plan item: display in the picker (issue #73)

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01VhkWs2JKfoSgd7ZVQC9zsJ
EOF
)"
```

---

## Spec coverage check

- §1 New package `internal/refcount` (state file, `Store` interface, `pidAlive` seam, flock concurrency) → Task 1.
- §2 Sweep on launch, run unconditionally at the top of `runLaunchPath` including for `shell-wt` → Task 2.
- §3 Record on launch at the TUI and non-TUI commit points; command agents never record → Task 3.
- §4 Display in the picker: `buildModelItems` param, `ref` field, `Title()` prefix ordering, clamp at 9 → Task 4.
- Scope: no launch gating/warnings added, no changes to `usage`/`rotation`/`survey`/config/registry → respected throughout (verified — no task touches those files).
- Testing section: refcount unit tests (Task 1), `buildModelItems` ref-column tests (Task 4), record-point tests including the command-agent negative case (Task 3), sweep-on-launch test (Task 2), full `go test ./...`/`go vet ./...` (Task 4, Step 11).
