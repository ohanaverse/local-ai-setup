# Flaky lifecycle stop tests Implementation Plan (Batch B, issue #123)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop `TestOmlxStopWaitsForPortClose` and `TestMtplxStopRunsMtplxStopAndConfirmsPortClosed` from failing intermittently in wt-ci, or — if the cause cannot be reproduced — harden the helpers and leave a diagnostic that identifies it on the next failure.

**Architecture:** Test-only change in `wt/internal/lifecycle`. The two tests build a server with `freeAddr` (bind `:0`, record the port, close) + `serveAt` (rebind, `go srv.Serve`), then stop it from inside the stubbed `e.run`. We (1) try to reproduce under load, (2) replace the free-then-rebind helper pair with a listener handoff over `httptest.Server`, whose `Close` closes the listener synchronously, and (3) make the "still listening" failure self-diagnosing. Production code (`portClosedWithin`) is unchanged unless step 1 proves it wrong.

**Tech Stack:** Go 1.26, `net/http/httptest`, `go test`.

**Spec:** GitHub issue #123 (no design doc; the issue's "Proposed fix" is the spec).

## Global Constraints

- All commands run from `wt/` (`cd /Users/keith/github/ohanaverse/local-ai-setup/wt`).
- Every test carries a comment saying what it does and why it matters (user rule).
- Do not push or open a PR without asking the user. Task 4 needs a push and is gated on approval.
- Commit trailer: `Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>`. Commits during execution reference the plan item, e.g. `- completes plan item #2`.
- Do not change `portClosedWithin`'s semantics (a stalled listener counts as "still holds the port") unless Task 1 shows that is the bug: a listener that accepts and stalls really does hold the port.

## Background the implementer needs

- `probe` (`internal/lifecycle/probe.go`) returns `(responded, timedOut)`. `timedOut` = the connection was accepted (or the dial hung) and no response came within the timeout. `portClosedWithin` treats `responded || timedOut` as "still open".
- Every reported failure is at ~1.01s: exactly one 1s probe timeout. `testEnv().stopTimeout` is 200ms, so the loop makes one probe, it times out, the deadline has passed, and `false, nil` becomes "still listening".
- `http.Server.Close()` does **not** close a listener that `Serve` has not yet registered. `serveAt` starts `go srv.Serve(l)`, and both failing tests call `srv.Close()` from `e.run`, moments later. `httptest.Server.Close()` closes `s.Listener` directly, so it has no such window.
- The issue's own hypothesis (another package's test binary grabs the freed ephemeral port) is unconfirmed, as is the one above. Task 1 exists to find out; do not skip it.

## File Structure

- Modify: `internal/lifecycle/backends_test.go` — `freeAddr`, `serveAt` (helpers), `TestOmlxStopWaitsForPortClose`
- Modify: `internal/lifecycle/mtplx_test.go` — `TestMtplxStopRunsMtplxStopAndConfirmsPortClosed`
- Create: `internal/lifecycle/servehelpers_test.go` — new listener-handoff helpers and their regression test (keeps the helper story in one small file)
- Modify (only if Task 1 implicates it): `internal/lifecycle/probe.go`

---

### Task 1: Reproduce under load and record what answers the probe

**Files:**
- Create: `internal/lifecycle/servehelpers_test.go`

**Interfaces:**
- Consumes: `testEnv()`, `freeAddr(t)`, `serveAt(t, addr, h)`, `chatHandler()`, `(*env).probe`
- Produces: `TestServeAtCloseLeavesNoStalledListener` (kept as a permanent regression test if it can fail; otherwise deleted at the end of this task and the outcome recorded in the issue)

- [ ] **Step 1: Write the probe-after-Close test against the current helpers**

```go
package lifecycle

import (
	"context"
	"testing"
	"time"
)

// TestServeAtCloseLeavesNoStalledListener verifies that once a test server's
// Close returns, its port refuses connections instead of accepting and
// stalling. The stop tests close the server from inside a stubbed `run` and
// then expect portClosedWithin to see a closed port; a listener that outlives
// Close reads as "still holds the port" (timedOut) and fails them at ~1s.
func TestServeAtCloseLeavesNoStalledListener(t *testing.T) {
	e := testEnv()
	for i := 0; i < 300; i++ {
		addr := freeAddr(t)
		srv := serveAt(t, addr, chatHandler())
		srv.Close() // immediately: the case where Serve's goroutine may not have run yet
		responded, timedOut := e.probe(context.Background(), "http://"+addr, 100*time.Millisecond)
		if responded || timedOut {
			t.Fatalf("iteration %d: probe after Close: responded=%v timedOut=%v, want a refused connection", i, responded, timedOut)
		}
	}
}
```

- [ ] **Step 2: Run it repeatedly, then under CPU load, and record the result**

```bash
go test ./internal/lifecycle -run TestServeAtCloseLeavesNoStalledListener -count=20 -race
GOMAXPROCS=1 go test ./internal/lifecycle -run TestServeAtCloseLeavesNoStalledListener -count=20
# Load: 2x CPU burners while the parallel-package situation is simulated
for i in $(seq 1 $(( $(sysctl -n hw.ncpu) * 2 ))); do (yes >/dev/null &) ; done
go test ./... -count=1 &   # other packages' test binaries running concurrently
go test ./internal/lifecycle -run 'Stop' -count=300
pkill yes; wait
```

Expected: either FAIL with `responded=false timedOut=true` (hypothesis confirmed: the helper leaves a stalled listener) or PASS everywhere (hypothesis not reproduced). **Write which happened, and the exact commands, into a comment on issue #123** (draft the comment text; ask the user before posting).

- [ ] **Step 3: Also try the issue's own hypothesis**

Run the whole package while a second process repeatedly binds ephemeral ports and holds them with a stalling accept loop:

```bash
cat > /tmp/portsquat.go <<'EOF'
package main

import ("net"; "time")

func main() {
	for {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil { continue }
		time.Sleep(20 * time.Millisecond) // accepts in the kernel backlog, never Accepts
		l.Close()
	}
}
EOF
go run /tmp/portsquat.go &
SQUAT=$!
go test ./internal/lifecycle -run 'Stop' -count=300
kill $SQUAT
```

Expected: record pass/fail. (Use the scratchpad directory rather than `/tmp` if the harness insists: any writable path works.)

- [ ] **Step 4: Commit only if the test can fail**

If Step 2 or 3 failed, keep the test (it becomes the regression test for Task 2) and commit:

```bash
git add internal/lifecycle/servehelpers_test.go
git commit -m "test(wt): reproduce lifecycle port still-listening flake - completes plan item #1"
```

If nothing failed, delete the file, do not commit it, and continue to Task 2 anyway: the listener handoff is still strictly safer.

---

### Task 2: Replace free-then-rebind with a listener handoff

**Files:**
- Modify: `internal/lifecycle/servehelpers_test.go` (create it now if Task 1 deleted it)
- Modify: `internal/lifecycle/backends_test.go:63-84` (`freeAddr`, `serveAt`)

**Interfaces:**
- Produces:
  - `serveOn(t *testing.T, l net.Listener, h http.Handler) *httptest.Server` — serves on an already-bound listener; `Close` closes the listener synchronously; registers `t.Cleanup(srv.Close)`
  - `serveFree(t *testing.T, h http.Handler) (srv *httptest.Server, addr string)` — binds `127.0.0.1:0` and serves on that same listener (no window)
  - `serveAt(t, addr, h) *httptest.Server` — same signature shape as today but the return type changes from `*http.Server` to `*httptest.Server`; both existing callers only call `.Close()`

- [ ] **Step 1: Add the helpers**

In `servehelpers_test.go`:

```go
import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

// serveOn serves h on the already-bound listener l. httptest.Server.Close
// closes l itself before returning, so unlike http.Server.Close there is no
// window in which a Close that races the Serve goroutine leaves the listener
// open — the window the stop tests need to be closed, because they assert the
// port is refused right after Close.
func serveOn(t *testing.T, l net.Listener, h http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(h)
	_ = srv.Listener.Close() // discard the listener NewUnstartedServer bound
	srv.Listener = l
	srv.Start()
	t.Cleanup(srv.Close)
	return srv
}

// serveFree binds an ephemeral loopback port and serves h on that same
// listener, returning its address. Use it when the address is not needed before
// the server exists; freeAddr followed by serveAt leaves a gap in which another
// process can take the port.
func serveFree(t *testing.T, h http.Handler) (*httptest.Server, string) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return serveOn(t, l, h), l.Addr().String()
}
```

- [ ] **Step 2: Reimplement `serveAt` on top of `serveOn`**

Replace the body in `backends_test.go`:

```go
func serveAt(t *testing.T, addr string, h http.Handler) *httptest.Server {
	t.Helper()
	l, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	return serveOn(t, l, h)
}
```

Keep `freeAddr` (still needed by the "daemon down, then comes up" tests). Fix imports (`net/http` is still used elsewhere in the file; `httptest` is already imported there).

- [ ] **Step 3: Run**

Run: `go vet ./internal/lifecycle && go test ./internal/lifecycle -count=1`
Expected: PASS. Run `go test ./internal/lifecycle -run TestServeAtCloseLeavesNoStalledListener -count=20 -race` (if kept): PASS, never `timedOut=true`.

- [ ] **Step 4: Commit**

```bash
git add internal/lifecycle/servehelpers_test.go internal/lifecycle/backends_test.go
git commit -m "test(wt): serve lifecycle test servers via httptest so Close releases the port - completes plan item #2"
```

---

### Task 3: Use the no-window helper in the two failing tests and make failures self-diagnosing

**Files:**
- Modify: `internal/lifecycle/backends_test.go:209-227` (`TestOmlxStopWaitsForPortClose`, first half)
- Modify: `internal/lifecycle/mtplx_test.go:208-225` (`TestMtplxStopRunsMtplxStopAndConfirmsPortClosed`, first half)

**Interfaces:**
- Consumes: `serveFree`, `provCfg`, `chatHandler`
- Produces: `logPortHolder(t *testing.T, addr string)` (in `servehelpers_test.go`) — best-effort `lsof -nP -iTCP:<port>` output via `t.Log`

- [ ] **Step 1: Add the diagnostic helper**

```go
import (
	"os/exec"
	"strings"
)

// logPortHolder logs who holds addr's port (best effort, silent when lsof is
// missing). It is called only on a "still listening" failure so the next CI
// flake names the process that answered the probe instead of leaving only a
// 1.01s timeout to go on.
func logPortHolder(t *testing.T, addr string) {
	t.Helper()
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return
	}
	out, err := exec.Command("lsof", "-nP", "-iTCP:"+addr[i+1:]).CombinedOutput()
	if err == nil {
		t.Logf("port %s holders:\n%s", addr[i+1:], out)
	}
}
```

- [ ] **Step 2: Switch the omlx test's first half**

Replace `addr := freeAddr(t); srv := serveAt(t, addr, chatHandler())` with:

```go
	srv, addr := serveFree(t, chatHandler())
```

and change the first failure check to:

```go
	if err := (omlxBackend{}).stop(context.Background(), e, provCfg("omlx", "http://"+addr)); err != nil {
		logPortHolder(t, addr)
		t.Fatalf("stop: %v", err)
	}
```

- [ ] **Step 3: Do the same in the mtplx test**

```go
	srv, addr := serveFree(t, chatHandler())
	_, portStr, _ := net.SplitHostPort(addr)
	...
	if err := (mtplxBackend{}).stop(context.Background(), e, cfg); err != nil {
		logPortHolder(t, addr)
		t.Fatalf("stop: %v", err)
	}
```

Leave the second halves (`freeAddr` + `serveAt` for the deliberately-still-listening case) alone: those servers are meant to stay open, so the window is harmless.

- [ ] **Step 4: Stress**

```bash
go test ./internal/lifecycle -run 'TestOmlxStopWaitsForPortClose|TestMtplxStopRunsMtplxStopAndConfirmsPortClosed' -count=500 -race
GOMAXPROCS=1 go test ./internal/lifecycle -run 'Stop' -count=300
go test ./... -count=3
```

Expected: all PASS. (Run the load recipe from Task 1 Step 2 again if Task 1 reproduced anything; the reproduction must now be gone.)

- [ ] **Step 5: Commit**

```bash
git add internal/lifecycle/backends_test.go internal/lifecycle/mtplx_test.go internal/lifecycle/servehelpers_test.go
git commit -m "test(wt): bind-and-serve the stop-test servers on one listener, log port holders on failure - completes plan item #3"
```

---

### Task 4: Decide on `portClosedWithin`, then validate in CI (needs user approval to push)

**Files:**
- Modify (comment only, unless Task 1 proved otherwise): `internal/lifecycle/probe.go` (`portClosedWithin` doc)

- [ ] **Step 1: Record the decision**

If Task 1 reproduced the failure and Task 2/3 removed it, add one sentence to `portClosedWithin`'s doc comment: a stalled listener is deliberately treated as holding the port, and tests must close their servers with a helper whose `Close` releases the listener synchronously (`serveOn`). If Task 1 could not reproduce it, do not touch `probe.go`.

- [ ] **Step 2: Ask the user**

Ask: "Ready to push this branch so wt-ci can run it a few times?" Do not push without an answer.

- [ ] **Step 3 (only after approval): validate in CI**

Push, and re-run the wt-ci workflow at least 5 times (`gh run rerun <id>`); on any `still listening` failure, the log now contains the `lsof` output. Report results.

- [ ] **Step 4: Report the issue outcome to the user (do not close it yourself)**

Recommend closing #123 only if Task 1 reproduced and fixed it, or CI stays green over the 5 reruns. Otherwise recommend leaving it open with the diagnostic in place.

## Self-review

- Coverage: issue's three proposals — reproduce first (Task 1), hand the bound listener to the server (Tasks 2–3), reconsider `portClosedWithin` (Task 4 decision) — each have a task.
- No production behavior change by default; the risk is confined to test helpers.
- Type consistency: `serveAt` now returns `*httptest.Server`; the only two callers that keep the return value (`backends_test.go:212`, `mtplx_test.go:211`) call `.Close()`, which both types have. Task 3 replaces both call sites.
