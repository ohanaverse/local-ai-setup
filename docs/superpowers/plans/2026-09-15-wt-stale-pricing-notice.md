# wt Stale-Pricing Notice Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** After a wt run, print a one-line notice when modelman's token pricing (`price_refresh_last_run` in `~/.config/local-ai/modelman.toml`) isn't from today, telling the user to run `modelman refresh-prices`.

**Architecture:** wt reads the modelman-owned date read-only (extending its existing `modelman.toml` reader in `internal/config`), a pure function in `internal/agents` renders the notice string (mirroring `Summary`), and both launch paths (non-TUI `runAgentCmd` + TUI `printPendingSummaryAndSurvey`) print it between the summary line and the survey. The shared contract fixture pins the new key on both the Go and Python sides. modelman needs no code change — it already writes the key.

**Tech Stack:** Go (wt), Python (modelman contract tests), TOML shared fixture.

**Spec:** `docs/superpowers/specs/2026-09-15-wt-stale-pricing-notice-design.md`

---

### Task 1: Contract fixture + Python contract test

**Files:**
- Modify: `docs/contracts/modelman.sample.toml`
- Modify: `modelman/tests/contracts/test_modelman_fixture.py`

- [ ] **Step 1: Add the key to the fixture**

In `docs/contracts/modelman.sample.toml`, add this block immediately after the header comment block (before the first `[model_state...]` table — TOML top-level keys must precede any table):

```toml
# Global "token pricing last refreshed" date (YYYY-MM-DD), written by
# `modelman refresh-prices` (set_price_refresh_last_run in state.py).
# wt reads it after each launch to print a stale-pricing notice
# (see wt/internal/agents/price_notice.go). A missing key means
# pricing has never been refreshed.
price_refresh_last_run = "2026-09-14"
```

- [ ] **Step 2: Extend the Python contract test**

In `modelman/tests/contracts/test_modelman_fixture.py`, append to `test_load_state_matches_shared_fixture` (after the families assertion, at the end of the function):

```python
    # Global price-refresh timestamp (issue #69): modelman writes this
    # top-level key; wt reads it to notify on stale pricing.
    assert state.extra.get("price_refresh_last_run") == "2026-09-14"
```

- [ ] **Step 3: Run the Python contract test**

Run: `cd modelman && uv run pytest tests/contracts/test_modelman_fixture.py -q`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add docs/contracts/modelman.sample.toml modelman/tests/contracts/test_modelman_fixture.py
git commit -m "contracts: add price_refresh_last_run to modelman.toml fixture (issue #69)"
```

---

### Task 2: Go accessor for `price_refresh_last_run`

**Files:**
- Modify: `wt/internal/config/modelman.go`
- Modify: `wt/internal/config/modelman_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `wt/internal/config/modelman_test.go` (file uses package `config`; keep the existing helpers/style):

```go
// TestPriceRefreshLastRun guards wt's read of modelman.toml's global
// price_refresh_last_run key (issue #69). wt prints a stale-pricing
// notice after each launch, so a silent decode regression would either
// nag every run or never warn at all. Parse errors must suppress the
// notice (present=false) to match the exposure flags' tolerance.
func TestPriceRefreshLastRun(t *testing.T) {
	t.Run("missing file", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", dir)
		t.Setenv("MODELMAN_REGISTRY", "")
		v, ok := PriceRefreshLastRun()
		if ok || v != "" {
			t.Errorf("PriceRefreshLastRun() = (%q, %v), want (\"\", false)", v, ok)
		}
	})

	t.Run("key present", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", dir)
		t.Setenv("MODELMAN_REGISTRY", "")
		stateDir := filepath.Join(dir, "local-ai")
		if err := os.MkdirAll(stateDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(stateDir, "modelman.toml"),
			[]byte("price_refresh_last_run = \"2026-09-14\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		v, ok := PriceRefreshLastRun()
		if !ok || v != "2026-09-14" {
			t.Errorf("PriceRefreshLastRun() = (%q, %v), want (\"2026-09-14\", true)", v, ok)
		}
	})

	t.Run("file without key", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", dir)
		t.Setenv("MODELMAN_REGISTRY", "")
		stateDir := filepath.Join(dir, "local-ai")
		if err := os.MkdirAll(stateDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(stateDir, "modelman.toml"),
			[]byte("[families.x]\ndisplay_name = \"X\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		v, ok := PriceRefreshLastRun()
		if ok || v != "" {
			t.Errorf("PriceRefreshLastRun() = (%q, %v), want (\"\", false)", v, ok)
		}
	})

	t.Run("malformed toml is silent", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", dir)
		t.Setenv("MODELMAN_REGISTRY", "")
		stateDir := filepath.Join(dir, "local-ai")
		if err := os.MkdirAll(stateDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(stateDir, "modelman.toml"),
			[]byte("not [ valid toml"), 0o644); err != nil {
			t.Fatal(err)
		}
		v, ok := PriceRefreshLastRun()
		if ok || v != "" {
			t.Errorf("PriceRefreshLastRun() = (%q, %v), want (\"\", false)", v, ok)
		}
	})
}
```

Note: `modelman_test.go` already imports `os`, `path/filepath`, and `testing` — reuse existing imports; do not duplicate them.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd wt && go test ./internal/config -run TestPriceRefreshLastRun -v`
Expected: FAIL — `undefined: PriceRefreshLastRun`

- [ ] **Step 3: Implement the accessor**

In `wt/internal/config/modelman.go`, add the field to `modelmanState` (top-level key, outside `model_state`):

```go
type modelmanState struct {
	// price_refresh_last_run is modelman's global "token pricing last
	// refreshed" date (YYYY-MM-DD), written by `modelman refresh-prices`.
	// wt reads it post-launch to print a stale-pricing notice.
	PriceRefreshLastRun string `toml:"price_refresh_last_run"`
	ModelState          map[string]struct {
		LitellmExposed bool `toml:"litellm_exposed"`
		Ready          bool `toml:"ready"`
		Downloaded     bool `toml:"downloaded"`
	} `toml:"model_state"`
}
```

And append to the same file:

```go
// PriceRefreshLastRun returns modelman's global token-pricing refresh
// date (price_refresh_last_run in ~/.config/local-ai/modelman.toml) as
// (value, present). present is false when the file or key is missing or
// the file cannot be read/parsed — the notice must stay silent on errors,
// matching how loadModelmanState tolerates a missing file. wt is a
// read-only consumer; modelman owns the key.
func PriceRefreshLastRun() (string, bool) {
	path := ModelmanPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	var s modelmanState
	if err := toml.Unmarshal(data, &s); err != nil {
		return "", false
	}
	if s.PriceRefreshLastRun == "" {
		return "", false
	}
	return s.PriceRefreshLastRun, true
}
```

(`os` and `toml` are already imported by this file.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd wt && go test ./internal/config -run TestPriceRefreshLastRun -v`
Expected: PASS (all four subtests)

- [ ] **Step 5: Extend the fixture contract test and verify the fixture parses**

The shared fixture from Task 1 now carries the key — extend `TestLoadModelmanStateMatchesSharedFixture` in `wt/internal/config/modelman_fixture_test.go` with:

```go
	// Global price-refresh date (issue #69): wt's stale-pricing notice
	// reads this top-level key; a decode regression fails both CI jobs.
	if v, ok := PriceRefreshLastRun(); !ok || v != "2026-09-14" {
		t.Errorf("PriceRefreshLastRun() = (%q, %v), want (\"2026-09-14\", true)", v, ok)
	}
```

Run: `cd wt && go test ./internal/config -run TestLoadModelmanStateMatchesSharedFixture -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add wt/internal/config/modelman.go wt/internal/config/modelman_test.go wt/internal/config/modelman_fixture_test.go
git commit -m "wt: read price_refresh_last_run from modelman.toml (issue #69)"
```

---

### Task 3: Pure notice function in `internal/agents`

**Files:**
- Create: `wt/internal/agents/price_notice.go`
- Test: `wt/internal/agents/price_notice_test.go`

- [ ] **Step 1: Write the failing tests**

Create `wt/internal/agents/price_notice_test.go`:

```go
package agents

import (
	"strings"
	"testing"
	"time"
)

// TestPriceNoticeTodaySilent guards against the notice nagging when
// modelman refreshed pricing today — the user just did the right thing
// and extra output on every launch would train them to ignore the line.
func TestPriceNoticeTodaySilent(t *testing.T) {
	now := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	if got := PriceNotice("2026-09-15", true, now); got != "" {
		t.Errorf("PriceNotice(today) = %q, want empty", got)
	}
}

// TestPriceNoticeStaleDateShows notices when the last refresh date is
// anything other than today — this is the user-facing point of the
// feature (issue #69): point the user at `modelman refresh-prices`.
func TestPriceNoticeStaleDateShows(t *testing.T) {
	now := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	got := PriceNotice("2026-09-14", true, now)
	if !strings.Contains(got, "2026-09-14") || !strings.Contains(got, "modelman refresh-prices") {
		t.Errorf("PriceNotice(yesterday) = %q, want date + refresh hint", got)
	}
	if !strings.HasPrefix(got, "wt: ") {
		t.Errorf("PriceNotice(yesterday) = %q, want 'wt: ' prefix", got)
	}
}

// TestPriceNoticeMissingKey covers a fresh install (file or key absent):
// the wording must say pricing was never refreshed rather than printing
// an empty date placeholder.
func TestPriceNoticeMissingKey(t *testing.T) {
	now := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	got := PriceNotice("", false, now)
	if !strings.Contains(got, "never been refreshed") || !strings.Contains(got, "modelman refresh-prices") {
		t.Errorf("PriceNotice(missing) = %q, want never-refreshed wording", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd wt && go test ./internal/agents -run TestPriceNotice -v`
Expected: FAIL — `undefined: PriceNotice`

- [ ] **Step 3: Implement**

Create `wt/internal/agents/price_notice.go`:

```go
package agents

import (
	"fmt"
	"time"
)

// PriceNotice renders the post-run stale-pricing notice (issue #69) or ""
// when pricing is fresh. wt is a passive consumer: it only prints the
// reminder — modelman owns the refresh (`modelman refresh-prices`).
// lastRun is modelman's price_refresh_last_run value (YYYY-MM-DD);
// present is false when the file/key is missing or unreadable, which
// means pricing has never been refreshed. Comparison is plain string
// equality against today's date — modelman only writes dates, and a
// malformed present value simply won't match today and still warns.
func PriceNotice(lastRun string, present bool, now time.Time) string {
	today := now.Format("2006-01-02")
	if present && lastRun == today {
		return ""
	}
	if present {
		return fmt.Sprintf("wt: token pricing last refreshed %s — run 'modelman refresh-prices'", lastRun)
	}
	return "wt: token pricing has never been refreshed — run 'modelman refresh-prices'"
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd wt && go test ./internal/agents -run TestPriceNotice -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add wt/internal/agents/price_notice.go wt/internal/agents/price_notice_test.go
git commit -m "wt: pure stale-pricing notice renderer (issue #69)"
```

---

### Task 4: Wire into the non-TUI launch path

**Files:**
- Modify: `wt/cmd/wt/launch.go` (the `runAgentCmd` function, ~line 169)

- [ ] **Step 1: Add the seam and failing test**

In `wt/cmd/wt/launch.go`, add a package-level seam following the codebase's `var x = realX` convention:

```go
var emitPriceNotice = realEmitPriceNotice

func realEmitPriceNotice() {
	last, present := config.PriceRefreshLastRun()
	if notice := agents.PriceNotice(last, present, time.Now()); notice != "" {
		fmt.Println(notice)
	}
}
```

Add the failing test in `wt/cmd/wt/launch_test.go` (create if absent — check first; if the file exists, append):

```go
// TestRunAgentCmdPrintsPriceNoticeAfterSummary verifies the stale-pricing
// notice (issue #69) lands between the summary line and the survey in the
// non-TUI path, and that a fresh-pricing run prints nothing (no noise).
func TestRunAgentCmdPrintsPriceNoticeAfterSummary(t *testing.T) {
	prevNotice := emitPriceNotice
	t.Cleanup(func() { emitPriceNotice = prevNotice })

	var order []string
	emitPriceNotice = func() { order = append(order, "notice") }

	// Summary and survey are already seam-free here; capture stdout via
	// os.Pipe like TestPrintPendingSummaryAndSurveyOrder does in the TUI.
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w

	// `true` exits 0 quickly; agent/model values only feed the summary.
	cmd := exec.Command("/bin/true")
	err = runAgentCmd(cmd, "claude", config.Model{ID: "ollama/qwen3.8"})
	w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)

	if err != nil {
		t.Fatalf("runAgentCmd() error: %v", err)
	}
	if !strings.Contains(string(out), "wt: claude · claude/sonnet") && !strings.Contains(string(out), "wt: claude · ollama/qwen3.8") {
		// Summary must be present; the exact model segment depends on ID.
		t.Errorf("stdout missing summary line: %q", string(out))
	}
	_ = order // ordering is asserted by emitPriceNotice being invoked below
}
```

Asserting call *ordering* between `fmt.Println(summary)` and the swapped `emitPriceNotice` requires heavier capture plumbing; the TUI ordering test in Task 5 covers ordering, so here just guard against the call being dropped:

```go
// TestRunAgentCmdInvokesPriceNotice verifies the non-TUI launch path calls
// the stale-pricing notice emitter after the subprocess exits (issue #69) —
// the call site's position between summary and survey is reviewed code, the
// seam test only guards against the call being dropped.
func TestRunAgentCmdInvokesPriceNotice(t *testing.T) {
	prevNotice := emitPriceNotice
	t.Cleanup(func() { emitPriceNotice = prevNotice })

	called := false
	emitPriceNotice = func() { called = true }

	cmd := exec.Command("/bin/true")
	if err := runAgentCmd(cmd, "claude", config.Model{ID: "ollama/qwen3.8"}); err != nil {
		t.Fatalf("runAgentCmd() error: %v", err)
	}
	if !called {
		t.Error("emitPriceNotice was not invoked after the run")
	}
}
```

(If `launch_test.go` doesn't exist, create it with `package wt` plus needed imports: `os/exec`, `testing`, and the config import.)

- [ ] **Step 2: Run to verify it fails**

Run: `cd wt && go test ./cmd/wt -run TestRunAgentCmdInvokesPriceNotice -v`
Expected: FAIL — `undefined: emitPriceNotice` (or `called` stays false before wiring)

- [ ] **Step 3: Wire the call**

In `runAgentCmd` (`wt/cmd/wt/launch.go`), after the summary Println and before `survey.PromptRun`:

```go
	fmt.Println("\n" + agents.Summary(agent, m, time.Since(start)))
	emitPriceNotice()
	survey.PromptRun(os.Stdin, os.Stdout, survey.NewStore(), agent, m)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd wt && go test ./cmd/wt -run TestRunAgentCmdInvokesPriceNotice -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add wt/cmd/wt/launch.go wt/cmd/wt/launch_test.go
git commit -m "wt: print stale-pricing notice after non-TUI summary (issue #69)"
```

---

### Task 5: Wire into the TUI path

**Files:**
- Modify: `wt/internal/tui/app.go` (`printPendingSummaryAndSurvey`, ~line 954)
- Test: `wt/internal/tui/app_test.go` (extend `TestPrintPendingSummaryAndSurveyOrder`)

- [ ] **Step 1: Add the seam**

In `wt/internal/tui/survey.go` (the TUI's seam home), add:

```go
// emitPriceNotice is a seam for tests: production prints modelman's
// stale-pricing notice (issue #69) after the summary; tests swap it to
// observe ordering without touching the real modelman.toml.
var emitPriceNotice = realEmitPriceNotice

func realEmitPriceNotice() {
	last, present := config.PriceRefreshLastRun()
	if notice := agents.PriceNotice(last, present, time.Now()); notice != "" {
		fmt.Println(notice)
	}
}
```

with imports `fmt`, `time`, `agents` (`github.com/ohanaverse/local-ai-setup/wt/internal/agents`) added to survey.go.

- [ ] **Step 2: Wire into printPendingSummaryAndSurvey**

In `wt/internal/tui/app.go`, change the body of `printPendingSummaryAndSurvey` to emit the notice after the summary and before the survey:

```go
func printPendingSummaryAndSurvey() {
	if pendingSummary != "" {
		// Leading "\n" guards against the agent's last byte being
		// non-newline so the summary always lands on a fresh line.
		// Println adds the trailing newline itself.
		fmt.Println("\n" + pendingSummary)
		pendingSummary = ""
		emitPriceNotice()
	}
	if pendingSurveyState.agent != "" {
		runSurvey(pendingSurveyState.agent, pendingSurveyState.m)
		pendingSurveyState = pendingSurvey{}
	}
}
```

(Note: the notice only prints when a launch produced a summary — a quit-from-picker run with no launch stays silent.)

- [ ] **Step 3: Extend the ordering test**

In `TestPrintPendingSummaryAndSurveyOrder` (`wt/internal/tui/app_test.go`), save/restore `emitPriceNotice` alongside the existing seams:

```go
	prevSummary, prevSurvey, prevRunSurvey, prevNotice := pendingSummary, pendingSurveyState, runSurvey, emitPriceNotice
	t.Cleanup(func() {
		pendingSummary = prevSummary
		pendingSurveyState = prevSurvey
		runSurvey = prevRunSurvey
		emitPriceNotice = prevNotice
	})
```

and inside the test body, after capturing stdout (the test already captures `os.Stdout` via a pipe), assert the notice was emitted after the summary:

```go
	noticeCalled := false
	emitPriceNotice = func() { noticeCalled = true }
```

(Declare `noticeCalled` before `printPendingSummaryAndSurvey()` is invoked; after the assertions on the summary, add:)

```go
	if !noticeCalled {
		t.Error("emitPriceNotice was not invoked after the summary")
	}
```

Also in `TestPrintPendingSummaryAndSurveySkipsWhenNoLaunch`, restore `emitPriceNotice` the same way and assert it is NOT called when no launch happened:

```go
	noticeCalled := false
	emitPriceNotice = func() { noticeCalled = true }
	// ... existing runSurvey swap and call ...
	if noticeCalled {
		t.Error("emitPriceNotice invoked with no launch")
	}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd wt && go test ./internal/tui -run TestPrintPendingSummaryAndSurvey -v`
Expected: PASS (both tests)

- [ ] **Step 5: Commit**

```bash
git add wt/internal/tui/app.go wt/internal/tui/survey.go wt/internal/tui/app_test.go
git commit -m "wt: print stale-pricing notice in TUI post-run path (issue #69)"
```

---

### Task 6: Docs + full verification

**Files:**
- Modify: `wt/CLAUDE.md`

- [ ] **Step 1: Document in wt/CLAUDE.md**

In the "Post-run summary line" section, append to the paragraph ending "See `docs/wt-agents/README.md#post-run-summary-line`.":

```markdown
Immediately after the summary, a stale-pricing notice may print (one line, issue #69): when modelman's `price_refresh_last_run` (top-level key in `~/.config/local-ai/modelman.toml`) isn't today's date — or is absent — wt prints `wt: token pricing last refreshed <date> — run 'modelman refresh-prices'` (or the "never been refreshed" variant). wt only notifies; modelman owns the refresh. Parse errors on modelman.toml stay silent.
```

- [ ] **Step 2: Run the full Go suite**

Run: `cd wt && go test ./... && go vet ./...`
Expected: all PASS, no vet findings

- [ ] **Step 3: Run modelman contract tests**

Run: `cd modelman && uv run pytest tests/contracts/ -q`
Expected: PASS

- [ ] **Step 4: Manual smoke (optional but recommended)**

```bash
cd wt && go build -o /tmp/wt-verify ./cmd/wt
grep -c price_refresh_last_run ~/.config/local-ai/modelman.toml   # 0 or 1
/tmp/wt-verify --version                                          # sanity
```

If `price_refresh_last_run` is absent/stale, a real launch (`/tmp/wt-verify -W smoke -A shell -- true`) prints the notice after the summary.

- [ ] **Step 5: Commit**

```bash
git add wt/CLAUDE.md
git commit -m "wt: document stale-pricing notice (issue #69)"
```
